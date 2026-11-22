package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// ---- valuation at a month or year end ----
//
// Stock-in-trade (t4): the closing stock is worked out three ways and all
// three are shown: at cost; at the lower of cost and market value scrip
// by scrip; and at the lower of cost and market value category by
// category, as ICDS VIII ("Securities", Part A, paras 4 to 8) has it for
// tax (shares, debt securities, convertible securities, any other;
// securities not listed, or listed but not quoted regularly, at actual
// cost). The firm's setting chooses the one the books take. Under Ind AS
// a trading portfolio is held for trading and measured at fair value
// through profit or loss (Ind AS 109, para 4.1.4); Ledgerline warns when
// the firm follows Ind AS and values at cost or the lower of cost and
// market (fsWarnings).
//
// Investments (i5): at cost, or the lower of cost and closing price
// (AS 13: current investments at the lower of cost and fair value;
// long-term investments at cost less a decline other than temporary), or
// fair value (Ind AS). The difference goes to a provision for diminution
// (or a fair value adjustment), so the lots stay at cost.
//
// The proposed entries bring the accounts to the values: the closing
// stock account of each kind against changes in inventories, and the
// provision against its charge. Someone proposes them and a second person
// posts them.

const cFSValuation = "fsvaluation"

// FSValueRow is a scrip's value in a portfolio.
type FSValueRow struct {
	Portfolio string       `json:"portfolio"`
	Kind      string       `json:"kind"`
	KindName  string       `json:"kind_name"`
	Category  string       `json:"category"`
	ISIN      string       `json:"isin"`
	Symbol    string       `json:"symbol"`
	Term      string       `json:"term,omitempty"`
	Qty       money.Dec    `json:"qty"`
	Cost      money.Amount `json:"cost"`
	Price     money.Dec    `json:"price"`
	Market    money.Amount `json:"market"`
	HasPrice  bool         `json:"has_price"`
	Listed    bool         `json:"listed"`
	LCM       money.Amount `json:"lcm"`   // lower of cost and market, this scrip
	Value     money.Amount `json:"value"` // as the firm's setting values it
}

// FSCategoryRow is an ICDS VIII category's comparison.
type FSCategoryRow struct {
	Category string       `json:"category"`
	Name     string       `json:"name"`
	Cost     money.Amount `json:"cost"`
	Market   money.Amount `json:"market"`
	AtCost   money.Amount `json:"at_cost"` // unlisted and not regularly quoted: at cost
	Value    money.Amount `json:"value"`
}

// FSProposed is one line of the proposed entry.
type FSProposed struct {
	Account string       `json:"account"`
	Name    string       `json:"name"`
	Target  money.Amount `json:"target"`  // the balance it should have (debit +)
	Balance money.Amount `json:"balance"` // the balance now
	Change  money.Amount `json:"change"`  // debit + / credit −
}

// FSValuationView is a portfolio's valuation on a date.
type FSValuationView struct {
	Date       string          `json:"date"`
	Portfolio  string          `json:"portfolio"`
	Basis      string          `json:"basis"`
	BasisName  string          `json:"basis_name"`
	Rows       []FSValueRow    `json:"rows"`
	Categories []FSCategoryRow `json:"categories,omitempty"`
	AtCost     money.Amount    `json:"at_cost"`
	Market     money.Amount    `json:"market"`
	LCMScrip   money.Amount    `json:"lcm_scrip"`
	LCMCat     money.Amount    `json:"lcm_category"`
	Value      money.Amount    `json:"value"`
	Proposed   []FSProposed    `json:"proposed"`
	Warnings   []string        `json:"warnings"`
	Saved      []FSValuation   `json:"saved"`
	NoPrice    int             `json:"no_price"`
}

// FSValuation is a proposed (and later posted) valuation entry.
type FSValuation struct {
	ID        string       `json:"id"`
	Date      string       `json:"date"`
	Portfolio string       `json:"portfolio"`
	Basis     string       `json:"basis"`
	Lines     []FSProposed `json:"lines"`
	Value     money.Amount `json:"value"`
	Status    string       `json:"status"`
	Maker     string       `json:"maker"`
	Checker   string       `json:"checker,omitempty"`
	Reason    string       `json:"reason,omitempty"`
	Entry     string       `json:"entry,omitempty"`
	Created   string       `json:"created"`
}

var fsBasisNames = map[string]string{
	"cost": "At cost", "lcm-scrip": "Lower of cost and market value, scrip by scrip", "lcm-category": "Lower of cost and market value, category by category (ICDS VIII)",
	"fair": "Fair value (closing price)", "lcm": "Lower of cost and closing price for current investments; long-term at cost", "lcm-all": "Lower of cost and closing price, every lot",
}

// fsWarnings says where the valuation chosen does not fit the firm's
// accounting standard (questionnaire i3).
func (s FSSettings) fsWarnings() []string {
	var w []string
	if s.Standard == "indas" {
		if s.has(PortSIT) && s.Closing != "fair" {
			w = append(w, "The firm follows Ind AS, but stock-in-trade is valued "+strings.ToLower(fsBasisNames[s.Closing])+". Under Ind AS a trading portfolio is held for trading and measured at fair value through profit or loss (Ind AS 109): choose fair value, or confirm the treatment with the auditor.")
		}
		if s.has(PortInv) && s.InvValue != "fair" {
			w = append(w, "The firm follows Ind AS, but investments are valued "+strings.ToLower(fsBasisNames[s.InvValue])+". Ind AS 109 measures equity investments at fair value (through profit or loss, or other comprehensive income where so designated); cost only where fair value cannot be measured reliably.")
		}
	} else {
		if s.has(PortSIT) && s.Closing == "fair" {
			w = append(w, "The firm follows the Accounting Standards (AS), which value inventories at the lower of cost and net realisable value (AS 2), not at fair value above cost.")
		}
		if s.has(PortInv) && s.InvValue == "fair" {
			w = append(w, "The firm follows the Accounting Standards (AS): AS 13 carries investments at cost (current ones at the lower of cost and fair value), not at fair value above cost.")
		}
	}
	return w
}

// FirmSecValuation values a portfolio on a date, with the entry that
// brings the books to the value.
func (o *Ops) FirmSecValuation(ctx context.Context, tenant, actor, date, port string) (FSValuationView, error) {
	if err := o.require(ctx, tenant, actor, "Viewing firm securities", RoleMaker, RoleChecker, RoleRisk, RoleCompliance); err != nil {
		return FSValuationView{}, err
	}
	if !ledger.ValidDate(date) {
		return FSValuationView{}, invalid("date must be YYYY-MM-DD")
	}
	s, _, b := o.fsBookTo(ctx, tenant, date)
	if !s.has(port) {
		return FSValuationView{}, invalid("the firm does not keep a %s portfolio", PortfolioName(port))
	}
	_, look, _ := o.Market(ctx, tenant, date)
	v := o.fsValue(ctx, tenant, s, b, look, date, port)
	v.Warnings = append(v.Warnings, s.fsWarnings()...)
	saved, _ := docs.List[FSValuation](ctx, o.Docs, tenant, cFSValuation, port+"|")
	sort.SliceStable(saved, func(i, j int) bool { return saved[i].ID > saved[j].ID })
	v.Saved = saved
	if v.Saved == nil {
		v.Saved = []FSValuation{}
	}
	return v, nil
}

func (o *Ops) fsValue(ctx context.Context, tenant string, s FSSettings, b fsBook, look Lookup, date, port string) FSValuationView {
	v := FSValuationView{Date: date, Portfolio: port, Rows: []FSValueRow{}, Proposed: []FSProposed{}, Warnings: []string{}}
	v.Basis = s.Closing
	if port == PortInv {
		v.Basis = s.InvValue
	}
	v.BasisName = fsBasisNames[v.Basis]
	// Scrip by scrip (investments: by scrip and term).
	type key struct{ isin, term string }
	rows := map[key]*FSValueRow{}
	var order []key
	for _, l := range b.lotsOf(port) {
		term := ""
		if port == PortInv {
			term = fsTermOn(s, l, date)
		}
		k := key{l.ISIN, term}
		r := rows[k]
		if r == nil {
			kd := s.kind(l.Kind)
			r = &FSValueRow{Portfolio: port, Kind: kd.ID, KindName: kd.Name, Category: kd.Category, ISIN: l.ISIN, Symbol: l.Symbol, Term: term, Listed: kd.Listed}
			rows[k] = r
			order = append(order, k)
		}
		r.Qty = r.Qty.Add(l.Qty)
		r.Cost += l.Cost
	}
	cats := map[string]*FSCategoryRow{}
	for _, k := range order {
		r := rows[k]
		if p, ok := o.fsPrice(look, r.ISIN); ok && r.Listed {
			r.Price, r.HasPrice = p, true
			r.Market = p.Mul(r.Qty).Round(money.UnitPaisa)
		} else {
			// Unlisted, or no price: at cost (ICDS VIII para 5).
			r.Market = r.Cost
			if r.Listed {
				v.NoPrice++
			}
		}
		r.LCM = r.Cost
		if r.Market < r.Cost {
			r.LCM = r.Market
		}
		v.AtCost += r.Cost
		v.Market += r.Market
		v.LCMScrip += r.LCM
		c := cats[r.Category]
		if c == nil {
			c = &FSCategoryRow{Category: r.Category}
			for _, x := range FSCategories {
				if x.ID == r.Category {
					c.Name = x.Name
				}
			}
			cats[r.Category] = c
		}
		if r.HasPrice {
			c.Cost += r.Cost
			c.Market += r.Market
		} else {
			c.AtCost += r.Cost
		}
		v.Rows = append(v.Rows, *r)
	}
	for _, x := range FSCategories {
		c := cats[x.ID]
		if c == nil {
			continue
		}
		c.Value = c.Cost
		if c.Market < c.Cost {
			c.Value = c.Market
		}
		c.Value += c.AtCost
		v.LCMCat += c.Value
		v.Categories = append(v.Categories, *c)
	}
	// Each row's value as the setting has it.
	for i := range v.Rows {
		r := &v.Rows[i]
		switch v.Basis {
		case "lcm-scrip", "lcm-all":
			r.Value = r.LCM
		case "lcm":
			r.Value = r.Cost
			if r.Term == TermCurrent {
				r.Value = r.LCM
			}
		case "fair":
			r.Value = r.Market
		case "lcm-category":
			r.Value = r.Cost // the category's comparison is below
		default:
			r.Value = r.Cost
		}
		v.Value += r.Value
	}
	if v.Basis == "lcm-category" {
		v.Value = v.LCMCat
	}
	sort.SliceStable(v.Rows, func(i, j int) bool {
		if v.Rows[i].Symbol != v.Rows[j].Symbol {
			return v.Rows[i].Symbol < v.Rows[j].Symbol
		}
		return v.Rows[i].Term < v.Rows[j].Term
	})
	bal, _ := o.App.Ledger.Balances(ctx, tenant, date)
	m := o.accountMaster(ctx, tenant)
	name := func(code string) string {
		if n, _ := s.fsAccountName(code); n != "" {
			return n
		}
		return accountName(code, m)
	}
	if port == PortSIT {
		// The closing stock of each kind; a category's write-down is
		// shared over its kinds by cost.
		target := map[string]money.Amount{}
		catCost := map[string]money.Amount{}
		for _, r := range v.Rows {
			target[r.Kind] += r.Value
			catCost[r.Category] += r.Cost
		}
		if v.Basis == "lcm-category" {
			for _, c := range v.Categories {
				down := c.Cost + c.AtCost - c.Value
				if down <= 0 || catCost[c.Category] == 0 {
					continue
				}
				left := down
				var last string
				for _, r := range v.Rows {
					if r.Category == c.Category {
						x := share(down, money.DecInt(int64(r.Cost)), money.DecInt(int64(catCost[c.Category])))
						target[r.Kind] -= x
						left -= x
						last = r.Kind
					}
				}
				target[last] -= left
			}
		}
		kinds := map[string]bool{}
		for k := range target {
			kinds[k] = true
		}
		for code := range bal {
			if k, ok := strings.CutPrefix(code, "ASSET:FSSIT_"); ok {
				kinds[k] = true
			}
		}
		var tot money.Amount
		for _, k := range sortedKeys(kinds) {
			code := fsSITAcct(k)
			p := FSProposed{Account: code, Name: name(code), Target: target[k], Balance: bal[code]}
			p.Change = p.Target - p.Balance
			if p.Change != 0 {
				v.Proposed = append(v.Proposed, p)
				tot += p.Change
			}
		}
		if tot != 0 {
			v.Proposed = append(v.Proposed, FSProposed{Account: AcctFSChangeInv, Name: name(AcctFSChangeInv), Change: -tot, Balance: bal[AcctFSChangeInv], Target: bal[AcctFSChangeInv] - tot})
		}
		return v
	}
	// Investments: the provision (or fair value adjustment) is the
	// difference between cost and value.
	adj := AcctFSProvision
	if v.Basis == "fair" {
		adj = AcctFSFVAdj
	}
	want := v.Value - v.AtCost // negative: a provision (credit balance)
	for _, a := range []string{AcctFSProvision, AcctFSFVAdj} {
		t := money.Amount(0)
		if a == adj {
			t = want
		}
		p := FSProposed{Account: a, Name: name(a), Target: t, Balance: bal[a]}
		p.Change = p.Target - p.Balance
		if p.Change == 0 {
			continue
		}
		v.Proposed = append(v.Proposed, p)
		against := AcctFSProvExp
		if a == AcctFSFVAdj {
			against = AcctFSFairValue
		}
		v.Proposed = append(v.Proposed, FSProposed{Account: against, Name: name(against), Change: -p.Change, Balance: bal[against], Target: bal[against] - p.Change})
	}
	return v
}

// fsTermOn is an investment lot's term on a date: as marked when bought
// (i4: lot), or by its holding (period: more than 12 months long-term).
func fsTermOn(s FSSettings, l FSLot, date string) string {
	if s.Term == "period" {
		if c, _ := gainCategory(l.Date, date); c == CatLTCG {
			return TermLong
		}
		return TermCurrent
	}
	return firstOf(l.Term, TermLong)
}

// ProposeFirmSecValuation saves the proposed entry for a second person.
func (o *Ops) ProposeFirmSecValuation(ctx context.Context, tenant, actor, date, port string) (FSValuation, error) {
	if err := o.require(ctx, tenant, actor, "Entering firm securities", RoleMaker, RoleChecker); err != nil {
		return FSValuation{}, err
	}
	v, err := o.FirmSecValuation(ctx, tenant, actor, date, port)
	if err != nil {
		return FSValuation{}, err
	}
	if len(v.Proposed) == 0 {
		return FSValuation{}, fmt.Errorf("%w: the books already carry %s at %s on %s", ErrState, PortfolioName(port), formatINR(v.Value.Dec()), date)
	}
	for _, x := range v.Saved {
		if x.Status == FSPending {
			return FSValuation{}, fmt.Errorf("%w: %s from %s is waiting for approval; approve or reject it first", ErrState, x.ID, x.Maker)
		}
	}
	n, err := o.next(ctx, tenant, "fsvaluation", actor)
	if err != nil {
		return FSValuation{}, err
	}
	x := FSValuation{ID: fmt.Sprintf("%s|%s|%04d", port, date, n), Date: date, Portfolio: port, Basis: v.Basis, Lines: v.Proposed, Value: v.Value, Status: FSPending, Maker: actor, Created: o.now().Format(time.RFC3339)}
	if err := docs.Upsert(ctx, o.Docs, tenant, cFSValuation, x.ID, x, actor); err != nil {
		return x, err
	}
	o.raiseWork(ctx, tenant, "firmsec", "valuation|"+x.ID, "Valuation entry to approve: "+PortfolioName(port)+" on "+date,
		fmt.Sprintf("%s proposes the valuation entry bringing %s to ₹%s (%s). A second person posts it on Firm securities → Valuation.", actor, PortfolioName(port), formatINR(v.Value.Dec()), strings.ToLower(v.BasisName)),
		"", RoleChecker, "firmsec", date)
	o.log(ctx, tenant, actor, "firm_securities.valuation_proposed", x.ID, map[string]any{"value": v.Value.String(), "basis": v.Basis})
	return x, nil
}

// DecideFirmSecValuation posts or rejects a proposed valuation entry.
func (o *Ops) DecideFirmSecValuation(ctx context.Context, tenant, actor, id string, approve bool, note string) (FSValuation, error) {
	if err := o.require(ctx, tenant, actor, "Approving firm securities entries", RoleChecker); err != nil {
		return FSValuation{}, err
	}
	x, _, err := docs.Get[FSValuation](ctx, o.Docs, tenant, cFSValuation, id)
	if err != nil {
		return x, err
	}
	if x.Status != FSPending {
		return x, fmt.Errorf("%w: %s is %s", ErrState, id, x.Status)
	}
	if !approve {
		if strings.TrimSpace(note) == "" {
			return x, invalid("say why it is rejected")
		}
		x.Status, x.Checker, x.Reason = FSRejected, actor, strings.TrimSpace(note)
	} else {
		if x.Maker == actor {
			return x, fmt.Errorf("%w: you proposed %s; another person posts it", ErrForbidden, id)
		}
		if err := o.checkLock(ctx, tenant, x.Date); err != nil {
			return x, err
		}
		// Worked out again: the books may have moved since.
		v, err := o.FirmSecValuation(ctx, tenant, actor, x.Date, x.Portfolio)
		if err != nil {
			return x, err
		}
		var ps []ledger.Posting
		var codes []string
		for _, l := range v.Proposed {
			codes = append(codes, l.Account)
			if l.Change > 0 {
				ps = append(ps, ledger.Posting{Account: l.Account, Debit: l.Change, Memo: "Valuation " + x.Date})
			} else if l.Change < 0 {
				ps = append(ps, ledger.Posting{Account: l.Account, Credit: -l.Change, Memo: "Valuation " + x.Date})
			}
		}
		if len(ps) >= 2 {
			s := o.FirmSecSettings(ctx, tenant)
			o.ensureFSAccounts(ctx, tenant, s, codes)
			e, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: tenant, Key: "FSV:" + x.ID, ValueDate: x.Date, CreatedBy: actor, Source: ledger.SourceRef{Kind: "FIRMSEC", ID: x.ID},
				Narration: fmt.Sprintf("Firm securities · valuation of %s on %s · %s", PortfolioName(x.Portfolio), x.Date, strings.ToLower(v.BasisName)), Postings: ps})
			if err != nil {
				return x, err
			}
			x.Entry = e.ID
		}
		x.Lines, x.Value, x.Status, x.Checker = v.Proposed, v.Value, FSPosted, actor
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cFSValuation, x.ID, x, actor); err != nil {
		return x, err
	}
	o.closeWork(ctx, tenant, actor, "firmsec", "valuation|"+x.ID, strings.ToLower(x.Status))
	o.log(ctx, tenant, actor, "firm_securities.valuation_"+map[bool]string{true: "posted", false: "rejected"}[approve], x.ID, map[string]any{"note": note})
	return x, nil
}

// ---- net worth (questionnaire c4) ----
//
// The net worth the exchanges ask a stock broker to have certified each
// half-year, worked out as Schedule VI of the SEBI (Stock Brokers)
// Regulations (as amended in 2022, SEBI/LAD-NRO/GN/2022/73 of 23
// February 2022) and the exchanges' net worth certificate (format C-1)
// describe it: capital and free reserves, less the non-allowable assets
// (a) fixed assets, (b) pledged securities, (c) the member's card, (d)
// non-allowable securities (unlisted), (e) bad deliveries, (f) debts and
// advances except trade debtors of less than three months, (g) prepaid
// expenses and losses, (h) intangible assets and (i) 30% of marketable
// securities. The firm's own securities are taken at cost (the firm's
// answer). Sources: NSE's net worth FAQ (nsearchives.nseindia.com) and
// the exchanges' certificate formats, read 22 November 2026.
//
// PROVISIONAL LAYOUT: the lines below follow the published format; they
// will be matched to the firm's auditor's certificate when it is shared.
// The layout is the fsNetWorthLayout table alone, so lines can be
// renamed, reordered, added or dropped there without touching the sums.

// FSNetWorthInputs are the figures the books do not hold.
type FSNetWorthInputs struct {
	Card           money.Amount `json:"card"`            // value of the membership card
	BadDeliveries  money.Amount `json:"bad_deliveries"`  // bad deliveries
	Prepaid        money.Amount `json:"prepaid"`         // prepaid expenses not separately kept in the books
	Intangible     money.Amount `json:"intangible"`      // intangible assets
	OtherDeduction money.Amount `json:"other_deduction"` // anything else the auditor deducts
	BaseRequired   money.Amount `json:"base_required"`   // the base net worth for the firm's category
	Variable       money.Amount `json:"variable"`        // variable net worth: 10% of the average client funds, six months
	By             string       `json:"by,omitempty"`
	At             string       `json:"at,omitempty"`
}

// FSNetWorthLine is one line of the computation.
type FSNetWorthLine struct {
	Key    string       `json:"key"`
	Label  string       `json:"label"`
	Sign   int          `json:"sign"` // +1 added, −1 deducted, 0 a total
	Amount money.Amount `json:"amount"`
	Source string       `json:"source"`
}

// FSNetWorth is the computation on a date.
type FSNetWorth struct {
	Date        string           `json:"date"`
	Provisional bool             `json:"provisional"`
	Lines       []FSNetWorthLine `json:"lines"`
	NetWorth    money.Amount     `json:"net_worth"`
	Required    money.Amount     `json:"required"` // the higher of base and variable
	Surplus     money.Amount     `json:"surplus"`
	Inputs      FSNetWorthInputs `json:"inputs"`
	Notes       []string         `json:"notes"`
}

type nwLine struct {
	key, label string
	sign       int
	source     string
}

// fsNetWorthLayout is the computation's layout (provisional, see above).
var fsNetWorthLayout = []nwLine{
	{"capital", "Capital", 1, "Capital and reserves accounts in the books"},
	{"reserves", "Free reserves (with the profit or loss to date)", 1, "Income less expenditure in the books"},
	{"gross", "Capital and free reserves", 0, ""},
	{"fixed", "(a) Fixed assets", -1, "Fixed assets head"},
	{"pledged", "(b) Pledged securities", -1, "Firm securities pledged, at cost"},
	{"card", "(c) Member's card", -1, "Entered"},
	{"unlisted", "(d) Non-allowable securities (unlisted securities)", -1, "Firm securities not listed, at cost"},
	{"baddel", "(e) Bad deliveries", -1, "Entered"},
	{"debts", "(f) Debts and advances, except trade debtors of less than 3 months", -1, "Receivables and other current assets heads, and client debits over 90 days"},
	{"prepaid", "(g) Prepaid expenses and losses", -1, "Entered"},
	{"intangible", "(h) Intangible assets", -1, "Entered"},
	{"haircut", "(i) 30% of marketable securities", -1, "30% of the firm's listed securities not pledged, at cost"},
	{"other", "Other deductions", -1, "Entered"},
	{"networth", "Net worth", 0, ""},
}

const cFSNetWorth = "fsnetworth"

// SaveFirmSecNetWorthInputs records the figures the books do not hold.
func (o *Ops) SaveFirmSecNetWorthInputs(ctx context.Context, tenant, actor string, in FSNetWorthInputs) (FSNetWorthInputs, error) {
	if err := o.require(ctx, tenant, actor, "Entering firm securities", RoleChecker, RoleCompliance); err != nil {
		return in, err
	}
	for _, a := range []money.Amount{in.Card, in.BadDeliveries, in.Prepaid, in.Intangible, in.OtherDeduction, in.BaseRequired, in.Variable} {
		if a < 0 {
			return in, invalid("amounts cannot be negative")
		}
	}
	in.By, in.At = actor, o.now().Format(time.RFC3339)
	if err := docs.Upsert(ctx, o.Docs, tenant, cFSNetWorth, "inputs", in, actor); err != nil {
		return in, err
	}
	o.log(ctx, tenant, actor, "firm_securities.net_worth_inputs", "inputs", nil)
	return in, nil
}

// FirmSecNetWorth works out the net worth on a date.
func (o *Ops) FirmSecNetWorth(ctx context.Context, tenant, actor, date string) (FSNetWorth, error) {
	if err := o.require(ctx, tenant, actor, "Viewing firm securities", RoleMaker, RoleChecker, RoleRisk, RoleCompliance); err != nil {
		return FSNetWorth{}, err
	}
	if !ledger.ValidDate(date) {
		return FSNetWorth{}, invalid("date must be YYYY-MM-DD")
	}
	in, _, _ := docs.GetOr(ctx, o.Docs, tenant, cFSNetWorth, "inputs", FSNetWorthInputs{})
	nw := FSNetWorth{Date: date, Provisional: true, Inputs: in, Notes: []string{}}
	bal, err := o.App.Ledger.Balances(ctx, tenant, date)
	if err != nil {
		return nw, err
	}
	hs := o.heads(ctx, tenant)
	m := o.accountMaster(ctx, tenant)
	amt := map[string]money.Amount{}
	s, _, b := o.fsBookTo(ctx, tenant, date)
	own := map[string]bool{}
	for _, c := range s.ownCodes() {
		own[ledger.ClientAccount(c)] = true
	}
	for code, x := range bal {
		t, err := ledger.TypeOf(code)
		if err != nil {
			continue
		}
		switch t {
		case ledger.Equity:
			amt["capital"] -= x
		case ledger.Income, ledger.Expense:
			amt["reserves"] -= x
		}
		if ledger.IsClientAccount(code) {
			continue
		}
		switch hs.root(hs.headOf(code, m)) {
		case "A-FIXED":
			amt["fixed"] += x
		case "A-RECV", "A-OTHER":
			if x > 0 {
				amt["debts"] += x
			}
		}
	}
	// Client debits over 90 days (the firm's own trading codes left out).
	if dv, err := o.Debits(ctx, tenant, date); err == nil {
		for _, r := range dv.Rows {
			if own[ledger.ClientAccount(r.UCC)] || len(r.Ageing) < 5 {
				continue
			}
			amt["debts"] += r.Ageing[3] + r.Ageing[4]
		}
	}
	// The firm's securities at cost (or as carried, with the provision).
	pledged := o.fsPledgedOn(ctx, tenant, date)
	var listedFree money.Amount
	for _, l := range b.lotsOf("") {
		k := s.kind(l.Kind)
		pq := money.Min(pledged[fsKey(l.Portfolio, l.ISIN)], l.Qty)
		var p money.Amount
		if pq.Sign() > 0 {
			p = share(l.Cost, pq, l.Qty)
			amt["pledged"] += p
			pledged[fsKey(l.Portfolio, l.ISIN)] = pledged[fsKey(l.Portfolio, l.ISIN)].Sub(pq)
		}
		if !k.Listed {
			amt["unlisted"] += l.Cost - p
		} else {
			listedFree += l.Cost - p
		}
	}
	amt["haircut"] = share(listedFree, money.DecInt(30), money.DecInt(100))
	amt["card"], amt["baddel"], amt["prepaid"], amt["intangible"], amt["other"] = in.Card, in.BadDeliveries, in.Prepaid, in.Intangible, in.OtherDeduction
	if s.NetWorth == "book" {
		nw.Notes = append(nw.Notes, "Securities are taken as carried in the books (with any provision); the deductions use their cost.")
	}
	var run money.Amount
	for _, l := range fsNetWorthLayout {
		x := FSNetWorthLine{Key: l.key, Label: l.label, Sign: l.sign, Source: l.source}
		switch l.sign {
		case 1:
			x.Amount = amt[l.key]
			run += x.Amount
		case -1:
			x.Amount = amt[l.key]
			run -= x.Amount
		default:
			x.Amount = run
		}
		nw.Lines = append(nw.Lines, x)
	}
	nw.NetWorth = run
	nw.Required = in.BaseRequired
	if in.Variable > nw.Required {
		nw.Required = in.Variable
	}
	nw.Surplus = nw.NetWorth - nw.Required
	nw.Notes = append(nw.Notes,
		"Provisional layout: it follows Schedule VI of the SEBI (Stock Brokers) Regulations and the exchanges' net worth certificate; it will be matched to your auditor's certificate.",
		"The firm's own securities are at cost. Pledged securities, unlisted securities and 30% of the rest of the listed securities are deducted.",
		"Required net worth is the higher of the base net worth for the firm's category and the variable net worth (10% of the average client funds of the last six months); enter both.")
	return nw, nil
}
