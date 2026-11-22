package ops

import (
	"context"
	"strings"

	"ledgerline/internal/money"
)

// ---- Settings → Firm practices → Firm securities ----
//
// The questions a firm answers about its own securities (firmsec.go),
// asked as their own section of Firm practices with the same rules: one
// source of truth (each question reads and writes FSSettings, nothing is
// copied), Other for a firm that works another way, and a second approver
// for a change that affects money (how the books value, cost or split
// the securities, or who approves entries).

// SectionFirmSecurities is the section these questions are asked in.
const SectionFirmSecurities = "Firm securities"

// fsQ builds a question whose behaviour is one FSSettings field.
func fsQ(id, short, question, why, note string, money bool, opts []PracticeOption, get func(FSSettings) string, set func(*FSSettings, string)) practiceDef {
	return practiceDef{ID: id, Section: SectionFirmSecurities, Short: short, Question: question, Why: why, Note: note, Money: money, Options: opts,
		Setting: "Firm securities settings (Firm securities screen)",
		read: func(o *Ops, ctx context.Context, tenant string) practiceState {
			return practiceState{Option: get(o.FirmSecSettings(ctx, tenant))}
		},
		write: func(o *Ops, ctx context.Context, tenant, actor, opt string, vals map[string]string) error {
			return o.saveFS(ctx, tenant, actor, func(s *FSSettings) error { set(s, opt); return nil })
		},
	}
}

func fsOpt(id, label, note string) PracticeOption {
	return PracticeOption{ID: id, Label: label, Available: true, Note: note}
}

func fsNotYet(id, label string) PracticeOption {
	return PracticeOption{ID: id, Label: label, Note: practiceNotYet}
}

func onOffFS(b bool) string { return map[bool]string{true: "on", false: "off"}[b] }

// fsSplitCodes reads a list of codes typed with commas or spaces.
func fsSplitCodes(s string) []string {
	out := []string{}
	for _, f := range strings.FieldsFunc(strings.ToUpper(s), func(r rune) bool { return r == ',' || r == ';' || r == ' ' }) {
		if f != "" && !contains(out, f) {
			out = append(out, f)
		}
	}
	return out
}

var fsPracticeDefs = []practiceDef{
	func() practiceDef {
		d := fsQ("fs-portfolios", "Where the firm keeps its own securities",
			"Where does the firm keep its own securities?",
			"Investments are held as an inventory of lots at cost; stock-in-trade is a purchase and sale book with closing stock valued at the period end. The books, the reports and the tax treatment follow from this.",
			"Every other Firm securities question applies only when the firm keeps one or both portfolios.", true,
			[]PracticeOption{fsOpt("sit", "As stock-in-trade (a trading book)", ""), fsOpt("inv", "As investments", ""), fsOpt("both", "Both: an investment portfolio and a trading book", ""), fsOpt(FSNone, "The firm holds no securities of its own", "")},
			func(s FSSettings) string { return s.Portfolios }, func(s *FSSettings, v string) { s.Portfolios = v })
		return d
	}(),
	{ID: "fs-demats", Section: SectionFirmSecurities, Short: "Demat accounts of the portfolios",
		Question: "If both, how are the two portfolios kept apart?",
		Why:      "A separate demat account for each portfolio keeps them apart at the depository too; the daily demat check compares each account with its portfolio.",
		Note:     "Give the demat accounts (16-digit CDSL BO ID, or IN and 14 digits for NSDL). They may be at the firm's own DP or another, CDSL or NSDL.",
		Setting:  "Firm securities settings (Firm securities screen)",
		Options:  []PracticeOption{fsOpt("separate", "A separate own demat account for each portfolio (same DP, another DP, or the other depository)", ""), fsOpt("one", "One demat account; the register keeps the portfolios apart", "")},
		Fields:   []PracticeField{{Key: "inv_demat", Label: "Demat account of the investments", Hint: "BO ID or NSDL client ID"}, {Key: "sit_demat", Label: "Demat account of the stock-in-trade", Hint: "With one demat account, give it here"}},
		read: func(o *Ops, ctx context.Context, tenant string) practiceState {
			s := o.FirmSecSettings(ctx, tenant)
			v := map[string]string{}
			if s.InvDemat != "" {
				v["inv_demat"] = s.InvDemat
			}
			if s.SITDemat != "" {
				v["sit_demat"] = s.SITDemat
			}
			return practiceState{Option: s.Demats, Values: v}
		},
		write: func(o *Ops, ctx context.Context, tenant, actor, opt string, vals map[string]string) error {
			return o.saveFS(ctx, tenant, actor, func(s *FSSettings) error {
				for _, k := range []string{"inv_demat", "sit_demat"} {
					if x := vals[k]; x != "" && normDematID(x) == "" && !strings.HasPrefix(strings.ToUpper(x), "OWN") {
						return invalid("%s is not a demat account number (16 digits, or IN and 14 digits)", x)
					}
				}
				s.Demats = opt
				if x, ok := vals["inv_demat"]; ok {
					s.InvDemat = strings.ToUpper(strings.TrimSpace(x))
				}
				if x, ok := vals["sit_demat"]; ok {
					s.SITDemat = strings.ToUpper(strings.TrimSpace(x))
				}
				return nil
			})
		},
	},
	{ID: "fs-default", Section: SectionFirmSecurities, Short: "Portfolio of a purchase",
		Question: "How does a purchase get its portfolio?",
		Why:      "The firm's own trades come in from the exchange trade files under its trading codes; each needs a portfolio before its entry is posted.",
		Note:     "With the default changeable, Firm securities → Purchases & sales shows a Change button on each trade-file line until the day is closed; after that a holding moves only by a stock journal.",
		Setting:  "Firm securities settings (Firm securities screen)", Money: true,
		Options: []PracticeOption{fsOpt("change", "A default from the trading code, changeable before the day's close", ""), fsOpt("fixed", "The trading code decides; never changed", "")},
		Fields:  []PracticeField{{Key: "sit_codes", Label: "Trading codes whose trades are stock-in-trade", Hint: "For example PRO; separate several with commas"}, {Key: "inv_codes", Label: "Trading codes whose trades are investments", Hint: "Leave blank if none"}},
		read: func(o *Ops, ctx context.Context, tenant string) practiceState {
			s := o.FirmSecSettings(ctx, tenant)
			v := map[string]string{}
			if len(s.SITCodes) > 0 {
				v["sit_codes"] = strings.Join(s.SITCodes, ", ")
			}
			if len(s.InvCodes) > 0 {
				v["inv_codes"] = strings.Join(s.InvCodes, ", ")
			}
			return practiceState{Option: s.Default, Values: v}
		},
		write: func(o *Ops, ctx context.Context, tenant, actor, opt string, vals map[string]string) error {
			return o.saveFS(ctx, tenant, actor, func(s *FSSettings) error {
				s.Default = opt
				if x, ok := vals["sit_codes"]; ok {
					s.SITCodes = fsSplitCodes(x)
				}
				if x, ok := vals["inv_codes"]; ok {
					s.InvCodes = fsSplitCodes(x)
				}
				for _, c := range s.InvCodes {
					if contains(s.SITCodes, c) {
						return invalid("%s cannot be both a stock-in-trade and an investment code", c)
					}
				}
				return nil
			})
		},
	},
	fsQ("fs-moves", "Moving holdings between the portfolios", "Can holdings move between the two portfolios?",
		"Moving a holding changes how it is taxed (section 45(2) or 28(via) of the Income-tax Act, 1961): it needs a record at market value on the day and a second person's approval.",
		"A stock journal: quantity and amount on both sides, out at cost, in at that day's market value, the difference posted, the tax date and value recorded.", true,
		[]PracticeOption{fsOpt("journal", "Allowed with a second person's approval and a dated record at market value (stock journal)", ""), fsOpt("no", "Not allowed", "")},
		func(s FSSettings) string { return s.Moves }, func(s *FSSettings, v string) { s.Moves = v }),
	{ID: "fs-instruments", Section: SectionFirmSecurities, Short: "Instruments covered",
		Question: "Which instruments should this cover?",
		Why:      "Each kind has its own account, its own ICDS VIII category for closing stock and its own capital gains rules.",
		Note:     "Instrument types of the firm's own are asked one by one: the Admin gets a work item to say how each is treated (Firm securities → Holdings).",
		Setting:  "Firm securities settings (Firm securities screen)",
		Options: []PracticeOption{fsOpt("all", "Listed shares and ETFs, mutual fund units, bonds, debentures and government securities, sovereign gold bonds, and unlisted shares", ""),
			fsOpt("listed", "Listed shares and ETFs only", "")},
		Fields: []PracticeField{{Key: "extra", Label: "Other instrument types the firm holds", Hint: "Separate with commas; each is asked how it is treated"}},
		read: func(o *Ops, ctx context.Context, tenant string) practiceState {
			s := o.FirmSecSettings(ctx, tenant)
			v := map[string]string{}
			if len(s.Extra) > 0 {
				v["extra"] = strings.Join(s.Extra, ", ")
			}
			return practiceState{Option: s.Instruments, Values: v}
		},
		write: func(o *Ops, ctx context.Context, tenant, actor, opt string, vals map[string]string) error {
			return o.saveFS(ctx, tenant, actor, func(s *FSSettings) error {
				s.Instruments = opt
				if x, ok := vals["extra"]; ok {
					s.Extra = nil
					for _, n := range strings.Split(x, ",") {
						if n = strings.TrimSpace(n); n != "" && len(n) <= 60 {
							s.Extra = append(s.Extra, n)
						}
					}
				}
				return nil
			})
		},
	},
	fsQ("fs-inv-method", "Lots taken out first (investments)", "Which lots are taken out first when investments are sold?",
		"The lots sold decide the cost of the sale, the profit and the capital gain.", "Perpetual: each sale takes its lots when it is entered.", true,
		[]PracticeOption{fsOpt("fifo", "First in, first out (FIFO)", ""), fsNotYet("average", "Weighted average cost"), fsNotYet("specific", "Lots chosen at each sale")},
		func(s FSSettings) string { return s.InvMethod }, func(s *FSSettings, v string) { s.InvMethod = v }),
	fsQ("fs-inv-cost", "Cost of an investment lot", "What goes into the cost of an investment lot?",
		"The cost decides the carrying value and the gain on sale. STT is never part of cost (it is an expense of its own).",
		"Whatever goes into cost, the reports can show each head (price, brokerage, exchange and SEBI charges, stamp duty, GST, other) as its own column.", true,
		[]PracticeOption{fsOpt("full", "Price, brokerage, stamp duty, exchange and SEBI charges, and GST on charges (no input credit taken)", ""),
			fsOpt("full-itc", "The same, without GST (input credit taken)", ""), fsOpt("price", "Price only; charges are expenses", "")},
		func(s FSSettings) string { return s.InvCost }, func(s *FSSettings, v string) { s.InvCost = v }),
	fsQ("fs-standard", "Accounting standards", "Which accounting standards does the firm follow?",
		"Under Ind AS a trading book is measured at fair value through profit or loss; under the Accounting Standards, inventories at the lower of cost and net realisable value. Ledgerline warns when the valuation chosen does not fit.", "", true,
		[]PracticeOption{fsOpt("as", "Accounting Standards (AS)", ""), fsOpt("indas", "Indian Accounting Standards (Ind AS)", "")},
		func(s FSSettings) string { return s.Standard }, func(s *FSSettings, v string) { s.Standard = v }),
	fsQ("fs-term", "Long-term or current investments", "How are investments split between long-term and current?",
		"AS 13 values current investments at the lower of cost and fair value, long-term ones at cost.", "", false,
		[]PracticeOption{fsOpt("lot", "Each lot marked when it is bought (long-term unless marked current)", ""), fsOpt("period", "By the holding period on the reporting date (more than 12 months: long-term)", "")},
		func(s FSSettings) string { return s.Term }, func(s *FSSettings, v string) { s.Term = v }),
	fsQ("fs-inv-value", "Valuing investments", "How are investments valued at month end and year end?",
		"The valuation entry (a provision for diminution, or a fair value adjustment) is proposed at each month or year end and posted by a second person.", "", true,
		[]PracticeOption{fsOpt("cost", "At cost", ""), fsOpt("lcm", "Lower of cost and closing price for current investments; long-term at cost", ""), fsOpt("lcm-all", "Lower of cost and closing price, every investment", ""), fsOpt("fair", "Fair value (closing price): Ind AS", "")},
		func(s FSSettings) string { return s.InvValue }, func(s *FSSettings, v string) { s.InvValue = v }),
	fsQ("fs-corp", "Corporate actions on own holdings", "How are corporate actions on the firm's own holdings handled?",
		"Bonus, split, merger and rights change the lots; dividends are income.", "The corporate actions announced on Operations → Corporate actions apply to the firm's lots on their ex-date (bonus at nil cost, split keeping cost and dates).", false,
		[]PracticeOption{fsOpt("engine", "The same corporate action engine as for clients", ""), fsNotYet("manual", "Entered by hand")},
		func(s FSSettings) string { return s.Corp }, func(s *FSSettings, v string) { s.Corp = v }),
	fsQ("fs-cg", "Capital gains register", "Should Ledgerline keep a capital gains register?",
		"The firm's own capital gains (investments, and investments converted into stock-in-trade) for its return; clients' capital gains statements are always produced.", "", false,
		onOff("Yes", "No"), func(s FSSettings) string { return onOffFS(s.CGRegister) }, func(s *FSSettings, v string) { s.CGRegister = v == "on" }),
	{ID: "fs-income", Section: SectionFirmSecurities, Short: "Dividends and interest",
		Question: "How are dividends and interest on investments booked?",
		Why:      "Dividends are income when the right to receive them is fixed (the record date); the TDS the company deducts is recoverable from the firm's tax.",
		Note:     "Booked dividends are matched to the credits in the firm's bank statement on Firm securities → Reconciliation.",
		Setting:  "Firm securities settings (Firm securities screen)", Money: true,
		Options: []PracticeOption{fsOpt("auto", "Automatically from the corporate actions, with TDS as receivable", ""), fsOpt("manual", "Entered by hand", "")},
		Fields:  []PracticeField{{Key: "tds_pct", Label: "TDS rate on dividends (%)", Hint: "10 for a resident with a PAN (section 194)"}, {Key: "tds_threshold", Label: "No TDS until the year's dividends from a company pass (₹)", Hint: "10000 from 1 April 2025"}},
		read: func(o *Ops, ctx context.Context, tenant string) practiceState {
			s := o.FirmSecSettings(ctx, tenant)
			return practiceState{Option: s.Income, Values: map[string]string{"tds_pct": s.TDSPct, "tds_threshold": s.TDSThreshold}}
		},
		write: func(o *Ops, ctx context.Context, tenant, actor, opt string, vals map[string]string) error {
			return o.saveFS(ctx, tenant, actor, func(s *FSSettings) error {
				if x := vals["tds_pct"]; x != "" {
					if d, err := money.ParseDec(x); err != nil || d.Sign() < 0 || d.Cmp(money.DecInt(100)) > 0 {
						return invalid("the TDS rate is a percentage from 0 to 100")
					}
					s.TDSPct = x
				}
				if x := vals["tds_threshold"]; x != "" {
					if a, err := money.ParseAmount(x); err != nil || a < 0 {
						return invalid("the threshold is an amount in rupees")
					}
					s.TDSThreshold = x
				}
				s.Income = opt
				return nil
			})
		},
	},
	fsQ("fs-sit-source", "Where stock-in-trade entries come from", "Where do purchases and sales of stock-in-trade come from?",
		"The exchange trade files already carry the firm's own trades under its trading codes.", "Trades are brought in after billing (their charges come from the bill) and again before the day's close.", false,
		[]PracticeOption{fsOpt("auto", "Automatically from the trade files, plus manual entry for the rest", ""), fsOpt("manual", "Entered by hand", "")},
		func(s FSSettings) string { return s.SITSource }, func(s *FSSettings, v string) { s.SITSource = v }),
	fsQ("fs-spec", "Intraday kept apart", "Should intraday and delivery trades be kept apart?",
		"A scrip bought and sold the same day and not delivered is speculative (section 43(5)); its loss is set off only against speculative income.", "", true,
		[]PracticeOption{fsOpt("on", "Kept apart automatically", ""), fsOpt("off", "Not kept apart", "")},
		func(s FSSettings) string { return onOffFS(s.Spec) }, func(s *FSSettings, v string) { s.Spec = v == "on" }),
	fsQ("fs-sit-method", "Lots taken out first (stock-in-trade)", "Which lots are taken out first when stock-in-trade is sold?",
		"The lots sold decide the cost of what was sold and the closing stock.", "", true,
		[]PracticeOption{fsOpt("fifo", "First in, first out (FIFO)", ""), fsNotYet("average", "Weighted average cost")},
		func(s FSSettings) string { return s.SITMethod }, func(s *FSSettings, v string) { s.SITMethod = v }),
	fsQ("fs-closing", "Valuing closing stock", "How is closing stock valued?",
		"The closing stock goes into the profit and loss account and the balance sheet. ICDS VIII values it for tax at the lower of cost and market value category by category.",
		"The closing stock statement always shows all three values (at cost, scrip by scrip, category by category); this chooses the one the books take.", true,
		[]PracticeOption{fsOpt("cost", "At cost", ""), fsOpt("lcm-scrip", "Lower of cost and market value, scrip by scrip", ""), fsOpt("lcm-category", "Lower of cost and market value, category by category (ICDS VIII)", ""), fsOpt("fair", "Fair value (closing price): Ind AS", "")},
		func(s FSSettings) string { return s.Closing }, func(s *FSSettings, v string) { s.Closing = v }),
	fsQ("fs-sit-charges", "Charges on own trades", "How are the charges on the firm's own trades treated?",
		"Charges in cost raise the closing stock; as expenses they reduce this year's profit.", "STT is always an expense (section 36(1)(xv)).", true,
		[]PracticeOption{fsOpt("expense", "All charges as expenses", ""), fsOpt("cost", "Charges other than STT added to the purchase cost", "")},
		func(s FSSettings) string { return s.SITCharges }, func(s *FSSettings, v string) { s.SITCharges = v }),
	fsQ("fs-demat-check", "Demat check", "Should the firm's holdings be checked against the depository?",
		"A difference between the register and the demat account is found the next day, not at the audit.", "Purchases before their pay-out date and sales before their pay-in date are in transit, not differences.", false,
		[]PracticeOption{fsOpt("daily", "Every day", ""), fsOpt("monthly", "At each month end", ""), fsOpt("off", "Not checked", "")},
		func(s FSSettings) string { return s.DematCheck }, func(s *FSSettings, v string) { s.DematCheck = v }),
	fsQ("fs-pledges", "Pledges of own securities", "Should own securities pledged as margin be tracked?",
		"Securities pledged to a clearing corporation, a bank or an NBFC cannot be delivered against a sale; they are also deducted in the net worth.", "", false,
		[]PracticeOption{fsOpt("track", "Tracked, not blocked: a warning before a pledged lot is sold; pledges to banks and NBFCs too; a pledges report on demand", ""), fsNotYet("block", "Blocked: a pledged lot cannot be sold"), fsOpt("off", "Not tracked", "")},
		func(s FSSettings) string { return s.Pledges }, func(s *FSSettings, v string) { s.Pledges = v }),
	{ID: "fs-disclosure", Section: SectionFirmSecurities, Short: "Proprietary trading disclosure",
		Question: "Should Ledgerline add the proprietary trading disclosure to client documents?",
		Why:      "A broker that trades on its own account tells its clients so when they open an account (SEBI circular SEBI/MRD/SE/Cir-42/2003 of 19 November 2003).",
		Note:     "With Yes, online account opening shows the disclosure as a declaration the client accepts, and it is printed on the signed form. Give your own wording, or leave it blank for Ledgerline's.",
		Setting:  "Firm securities settings (Firm securities screen)",
		Options:  onOff("Yes, automatically", "No"),
		Fields:   []PracticeField{{Key: "text", Label: "Disclosure wording (optional)", Hint: "Blank: \"The stock broker also trades on its own account (proprietary trading).\""}},
		read: func(o *Ops, ctx context.Context, tenant string) practiceState {
			s := o.FirmSecSettings(ctx, tenant)
			v := map[string]string{}
			if s.DisclosureText != "" {
				v["text"] = s.DisclosureText
			}
			return practiceState{Option: onOffFS(s.Disclosure), Values: v}
		},
		write: func(o *Ops, ctx context.Context, tenant, actor, opt string, vals map[string]string) error {
			return o.saveFS(ctx, tenant, actor, func(s *FSSettings) error {
				s.Disclosure = opt == "on"
				if x, ok := vals["text"]; ok {
					s.DisclosureText = strings.TrimSpace(x)
				}
				return nil
			})
		},
	},
	fsQ("fs-net-worth", "Securities in the net worth", "Should investments feed the net worth worked out for the exchanges?",
		"The exchanges ask for the net worth each half-year; the firm's own securities, pledges and unlisted holdings change it.",
		"The computation follows Schedule VI of the SEBI (Stock Brokers) Regulations; its layout is provisional until it is matched to the firm's auditor's certificate.", true,
		[]PracticeOption{fsOpt("cost", "Securities at cost, worked out by the Schedule VI formula", ""), fsOpt("book", "Securities as carried in the books", "")},
		func(s FSSettings) string { return s.NetWorth }, func(s *FSSettings, v string) { s.NetWorth = v }),
	fsQ("fs-checker", "Who approves firm securities entries", "Who enters and approves these entries?",
		"Entries from the trade files match the exchange's record; entries typed by hand do not.", "", true,
		[]PracticeOption{fsOpt("manual", "One person enters, another approves, for manual and off-market entries only (trade-file lines post at once)", ""), fsOpt("all", "One person enters, another approves, every entry", ""), fsOpt("none", "No second person", "")},
		func(s FSSettings) string { return s.Checker }, func(s *FSSettings, v string) { s.Checker = v }),
	fsQ("fs-accounts", "Securities in the chart of accounts", "How do the securities appear in the chart of accounts?",
		"Scrip-by-scrip accounts make the chart long; the register keeps the detail and the accounts drill down to it.", "", false,
		[]PracticeOption{fsOpt("kind", "One account per portfolio and kind, scrip detail in the register", ""), fsNotYet("scrip", "One account per scrip")},
		func(s FSSettings) string { return s.Accounts }, func(s *FSSettings, v string) { s.Accounts = v }),
	fsQ("fs-opening", "Bringing in current holdings", "How are the firm's current holdings brought into Ledgerline?",
		"The lots carry the acquisition dates and costs that later sales and capital gains need.", "Load FIRM-LOTS on Operations → Files → Other uploads (or through the migration tool); a second person posts them.", false,
		[]PracticeOption{fsOpt("lots", "Lot by lot from the old system", ""), fsNotYet("average", "One lot per scrip at average cost")},
		func(s FSSettings) string { return s.Opening }, func(s *FSSettings, v string) { s.Opening = v }),
}

func init() { practiceDefs = append(practiceDefs, fsPracticeDefs...) }

// fsPracticeApplies says whether a Firm securities question applies: all
// but the first wait until the firm keeps a portfolio.
func (o *Ops) fsPracticeApplies(ctx context.Context, tenant string, d practiceDef) bool {
	if d.Section != SectionFirmSecurities || d.ID == "fs-portfolios" {
		return true
	}
	return o.FirmSecSettings(ctx, tenant).Portfolios != FSNone
}

// DisclosureDeclaration is the proprietary trading declaration a client
// accepts when opening an account online ("" when the firm does not add it).
func (o *Ops) DisclosureDeclaration(ctx context.Context, tenant string) string {
	s := o.FirmSecSettings(ctx, tenant)
	if !s.Disclosure || s.Portfolios == FSNone {
		return ""
	}
	return firstOf(s.DisclosureText, "I have been told that the stock broker also trades on its own account (proprietary trading).")
}
