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

// ---- pledges of the firm's own securities (questionnaire c2) ----
//
// Pledges are tracked, not blocked: a pledge to a clearing corporation
// for margin, or to a bank or NBFC as collateral for a loan, is recorded
// with its pledgee, and a sale that would leave fewer units than are
// pledged goes through with a warning (and a work item) instead of being
// stopped. The pledges report lists them on any date.

const (
	cFSPledge = "fspledge"

	FSPledgeOpen     = "Open"
	FSPledgeReleased = "Released"
	FSPledgeInvoked  = "Invoked"
)

// FSPledgeeTypes are the kinds of pledgee.
var FSPledgeeTypes = []string{"Clearing corporation (margin)", "Bank", "NBFC", "Other"}

// FSPledge is one pledge of the firm's own securities.
type FSPledge struct {
	ID          string    `json:"id"`
	Date        string    `json:"date"`
	Portfolio   string    `json:"portfolio"`
	ISIN        string    `json:"isin"`
	Symbol      string    `json:"symbol"`
	Qty         money.Dec `json:"qty"`
	Pledgee     string    `json:"pledgee"`
	PledgeeType string    `json:"pledgee_type"`
	Purpose     string    `json:"purpose,omitempty"`
	Ref         string    `json:"ref,omitempty"` // the depository's pledge number
	Status      string    `json:"status"`
	Closed      string    `json:"closed,omitempty"` // release or invocation date
	Maker       string    `json:"maker"`
	Note        string    `json:"note,omitempty"`
	Created     string    `json:"created"`
	// Filled in when listed: the cost of the pledged units on the date.
	Cost money.Amount `json:"cost,omitempty"`
}

func (p FSPledge) openOn(date string) bool {
	return p.Date <= date && (p.Status == FSPledgeOpen || p.Closed > date)
}

func (o *Ops) fsPledges(ctx context.Context, tenant string) []FSPledge {
	ps, _ := docs.List[FSPledge](ctx, o.Docs, tenant, cFSPledge, "")
	return ps
}

// fsPledgedOn is the quantity pledged on a date, by portfolio and ISIN.
func (o *Ops) fsPledgedOn(ctx context.Context, tenant, date string) map[string]money.Dec {
	out := map[string]money.Dec{}
	for _, p := range o.fsPledges(ctx, tenant) {
		if p.openOn(date) {
			k := fsKey(p.Portfolio, p.ISIN)
			out[k] = out[k].Add(p.Qty)
		}
	}
	return out
}

// fsPledgeWarning says when a sale leaves fewer units than are pledged
// ("" when it does not). left is what the portfolio holds after it.
func (o *Ops) fsPledgeWarning(ctx context.Context, tenant string, t FSTxn, left money.Dec) string {
	s := o.FirmSecSettings(ctx, tenant)
	if s.Pledges == "off" {
		return ""
	}
	pledged := o.fsPledgedOn(ctx, tenant, t.Date)[fsKey(t.Portfolio, t.ISIN)]
	if pledged.Sign() == 0 || left.Cmp(pledged) >= 0 {
		return ""
	}
	var to []string
	for _, p := range o.fsPledges(ctx, tenant) {
		if p.openOn(t.Date) && p.Portfolio == t.Portfolio && p.ISIN == t.ISIN && !contains(to, p.Pledgee) {
			to = append(to, p.Pledgee)
		}
	}
	if left.Sign() < 0 {
		left = money.Dec{}
	}
	return fmt.Sprintf("%s of %s are pledged (to %s); after this sale %s remain: release the pledge or sell other units", pledged, t.Symbol, strings.Join(to, ", "), left)
}

// fsPledgeWarnings checks the day's trade-file sales against the pledges
// and raises a work item for each one that sells into pledged units.
func (o *Ops) fsPledgeWarnings(ctx context.Context, tenant, actor, date string) []string {
	s, ts, b := o.fsBookTo(ctx, tenant, date)
	if s.Pledges == "off" {
		return nil
	}
	var out []string
	for _, t := range ts {
		if t.Date != date || t.Side != "S" || !t.live() || t.Type != FSTypeTrade {
			continue
		}
		if w := o.fsPledgeWarning(ctx, tenant, t, b.held(t.Portfolio, t.ISIN)); w != "" {
			out = append(out, w)
			o.raiseWork(ctx, tenant, "firmsec", "pledged|"+t.ID, "Pledged firm securities sold: "+t.Symbol, w+". Pledges are tracked, not blocked: check with the pledgee and release the pledge.", "", RoleChecker, "firmsec", date)
		}
	}
	return out
}

// FSPledgeInput is a pledge to record.
type FSPledgeInput struct {
	Date        string `json:"date"`
	Portfolio   string `json:"portfolio"`
	ISIN        string `json:"isin"`
	Qty         string `json:"qty"`
	Pledgee     string `json:"pledgee"`
	PledgeeType string `json:"pledgee_type"`
	Purpose     string `json:"purpose"`
	Ref         string `json:"ref"`
	Note        string `json:"note"`
}

// AddFirmSecPledge records a pledge. It warns, and does not stop, when
// more is pledged than the portfolio holds.
func (o *Ops) AddFirmSecPledge(ctx context.Context, tenant, actor string, in FSPledgeInput) (FSPledge, []string, error) {
	if err := o.require(ctx, tenant, actor, "Entering firm securities", RoleMaker, RoleChecker, RoleRisk); err != nil {
		return FSPledge{}, nil, err
	}
	s := o.FirmSecSettings(ctx, tenant)
	in.ISIN = strings.ToUpper(strings.TrimSpace(in.ISIN))
	switch {
	case s.Pledges == "off":
		return FSPledge{}, nil, fmt.Errorf("%w: the firm does not track pledges of its own securities (Settings → Firm practices → Firm securities)", ErrState)
	case !ledger.ValidDate(in.Date):
		return FSPledge{}, nil, invalid("date must be YYYY-MM-DD")
	case !s.has(in.Portfolio):
		return FSPledge{}, nil, invalid("choose one of the firm's portfolios")
	case strings.TrimSpace(in.Pledgee) == "":
		return FSPledge{}, nil, invalid("name the pledgee (the clearing corporation, bank or NBFC)")
	case !contains(FSPledgeeTypes, in.PledgeeType):
		return FSPledge{}, nil, invalid("pledgee type must be one of: %s", strings.Join(FSPledgeeTypes, ", "))
	}
	qty, err := parseQty(in.Qty)
	if err != nil || qty.Sign() <= 0 {
		return FSPledge{}, nil, invalid("give the quantity pledged, above zero")
	}
	_, _, b := o.fsBookTo(ctx, tenant, in.Date)
	lots := b.Lots[fsKey(in.Portfolio, in.ISIN)]
	if len(lots) == 0 {
		return FSPledge{}, nil, invalid("%s holds no %s on %s", PortfolioName(in.Portfolio), in.ISIN, in.Date)
	}
	var warn []string
	already := o.fsPledgedOn(ctx, tenant, in.Date)[fsKey(in.Portfolio, in.ISIN)]
	if held := b.held(in.Portfolio, in.ISIN); already.Add(qty).Cmp(held) > 0 {
		warn = append(warn, fmt.Sprintf("%s holds %s of %s; with this pledge %s are pledged", PortfolioName(in.Portfolio), held, lots[0].Symbol, already.Add(qty)))
	}
	n, err := o.next(ctx, tenant, "fspledge", actor)
	if err != nil {
		return FSPledge{}, nil, err
	}
	p := FSPledge{ID: fmt.Sprintf("FP-%04d", n), Date: in.Date, Portfolio: in.Portfolio, ISIN: in.ISIN, Symbol: lots[0].Symbol, Qty: qty, Pledgee: strings.TrimSpace(in.Pledgee),
		PledgeeType: in.PledgeeType, Purpose: strings.TrimSpace(in.Purpose), Ref: strings.TrimSpace(in.Ref), Status: FSPledgeOpen, Maker: actor, Note: strings.TrimSpace(in.Note), Created: o.now().Format(time.RFC3339)}
	if err := docs.Upsert(ctx, o.Docs, tenant, cFSPledge, p.ID, p, actor); err != nil {
		return p, warn, err
	}
	o.log(ctx, tenant, actor, "firm_securities.pledged", p.ID, map[string]any{"isin": p.ISIN, "qty": p.Qty.String(), "pledgee": p.Pledgee})
	return p, warn, nil
}

// CloseFirmSecPledge releases a pledge, or records that the pledgee
// invoked it. An invocation takes the units out: it enters an off-market
// sale at the price given (which waits for a second person as the firm's
// practice says).
func (o *Ops) CloseFirmSecPledge(ctx context.Context, tenant, actor, id, action, date, price string) (FSPledge, error) {
	if err := o.require(ctx, tenant, actor, "Entering firm securities", RoleMaker, RoleChecker, RoleRisk); err != nil {
		return FSPledge{}, err
	}
	p, _, err := docs.Get[FSPledge](ctx, o.Docs, tenant, cFSPledge, id)
	if err != nil {
		return p, err
	}
	if p.Status != FSPledgeOpen {
		return p, fmt.Errorf("%w: %s is %s", ErrState, id, strings.ToLower(p.Status))
	}
	if !ledger.ValidDate(date) || date < p.Date {
		return p, invalid("give the date, on or after the pledge date %s", p.Date)
	}
	switch action {
	case "release":
		p.Status = FSPledgeReleased
	case "invoke":
		if _, err := o.AddFirmSecEntry(ctx, tenant, actor, FSEntryInput{Date: date, Portfolio: p.Portfolio, Side: "S", Type: FSTypeOffMarket, ISIN: p.ISIN, Symbol: p.Symbol,
			Qty: p.Qty.String(), Price: price, Counter: ledger.BankOwn, Note: "Pledge " + p.ID + " invoked by " + p.Pledgee}); err != nil {
			return p, err
		}
		p.Status = FSPledgeInvoked
	default:
		return p, invalid("action must be release or invoke")
	}
	p.Closed = date
	if err := docs.Upsert(ctx, o.Docs, tenant, cFSPledge, p.ID, p, actor); err != nil {
		return p, err
	}
	o.log(ctx, tenant, actor, "firm_securities.pledge_"+action, p.ID, map[string]any{"date": date})
	return p, nil
}

// ---- dividends and interest (questionnaire i8) ----
//
// With the firm's practice "auto", a dividend announced in the corporate
// actions is booked on its record date for the units each portfolio held
// before the ex-date: Dr dividend receivable (net), Dr TDS receivable,
// Cr dividend income. TDS is worked out as the company deducts it under
// section 194 of the Income-tax Act, 1961 (section 393(1) of the 2025 Act
// from 1 April 2026): at the rate set (10% for a resident with a PAN)
// once the dividends from the company in the financial year pass the
// threshold set (₹10,000 from 1 April 2025, Finance Act, 2025; ₹5,000
// before), the first payment over it taking the TDS on the earlier ones.
// Interest on bonds is entered by hand. Each is matched later to the
// credit in the firm's bank statement (FirmSecMatchIncome).

const cFSIncome = "fsincome"

// FSIncome is a dividend or interest booked on the firm's securities.
type FSIncome struct {
	ID        string       `json:"id"`
	Kind      string       `json:"kind"` // Dividend, Interest
	Corp      string       `json:"corp,omitempty"`
	Portfolio string       `json:"portfolio"`
	ISIN      string       `json:"isin"`
	Symbol    string       `json:"symbol"`
	Date      string       `json:"date"`     // booked on (record date)
	PayDate   string       `json:"pay_date"` // expected in the bank
	Qty       money.Dec    `json:"qty"`
	Rate      money.Dec    `json:"rate,omitempty"` // per unit
	Gross     money.Amount `json:"gross"`
	TDS       money.Amount `json:"tds"`
	Net       money.Amount `json:"net"`
	Status    string       `json:"status"` // Booked, Received
	Entry     string       `json:"entry,omitempty"`
	Received  string       `json:"received,omitempty"`
	BankRef   string       `json:"bank_ref,omitempty"`
	Bank      string       `json:"bank,omitempty"`
	RecvEntry string       `json:"recv_entry,omitempty"`
	By        string       `json:"by"`
}

const (
	FSIncomeBooked   = "Booked"
	FSIncomeReceived = "Received"
)

func (o *Ops) fsIncomes(ctx context.Context, tenant string) []FSIncome {
	xs, _ := docs.List[FSIncome](ctx, o.Docs, tenant, cFSIncome, "")
	sort.SliceStable(xs, func(i, j int) bool {
		if xs[i].Date != xs[j].Date {
			return xs[i].Date > xs[j].Date
		}
		return xs[i].ID > xs[j].ID
	})
	return xs
}

// fsTDS is the TDS on a payment: none until the year's dividends from the
// company pass the threshold; then the rate on this payment and on the
// earlier ones of the year that bore none.
func fsTDS(s FSSettings, gross money.Amount, earlier []FSIncome) money.Amount {
	thr, _ := money.ParseAmount(firstOf(strings.TrimSpace(s.TDSThreshold), "10000"))
	pct := pctSetting(s.TDSPct, 10)
	var before, untaxed money.Amount
	for _, x := range earlier {
		before += x.Gross
		if x.TDS == 0 {
			untaxed += x.Gross
		}
	}
	if before+gross <= thr {
		return 0
	}
	return share(gross+untaxed, pct, money.DecInt(100))
}

// fsBookDividends books the dividends whose record date has come, once
// for each corporate action and portfolio. It returns how many it booked.
func (o *Ops) fsBookDividends(ctx context.Context, tenant, actor, date string) (int, error) {
	s := o.FirmSecSettings(ctx, tenant)
	if s.Income != "auto" || s.Portfolios == FSNone {
		return 0, nil
	}
	done := map[string]bool{}
	all := o.fsIncomes(ctx, tenant)
	for _, x := range all {
		done[x.ID] = true
	}
	n := 0
	ts := o.fsTxns(ctx, tenant)
	cas := o.fsCorpActions(ctx, tenant)
	for _, c := range cas {
		if c.Type != "Dividend" || c.RecordDate == "" || c.RecordDate > date || c.DPS.Sign() <= 0 {
			continue
		}
		// Units held at the end of the day before the ex-date: a purchase on
		// the ex-date or later does not get the dividend.
		b := fsReplay(s, ts, cas, addDays(firstOf(c.ExDate, c.RecordDate), -1))
		for _, port := range s.Ports() {
			id := "FD-" + c.ID + "-" + port
			if done[id] {
				continue
			}
			held := b.held(port, c.ISIN)
			if held.Sign() <= 0 {
				continue
			}
			gross := c.DPS.Mul(held).Round(money.UnitPaisa)
			_, fyFrom, fyTo := fyOf(c.RecordDate)
			var earlier []FSIncome
			for _, x := range all {
				if x.Kind == "Dividend" && x.ISIN == c.ISIN && x.Date >= fyFrom && x.Date <= fyTo {
					earlier = append(earlier, x)
				}
			}
			tds := fsTDS(s, gross, earlier)
			x := FSIncome{ID: id, Kind: "Dividend", Corp: c.ID, Portfolio: port, ISIN: c.ISIN, Symbol: c.Symbol, Date: c.RecordDate, PayDate: firstOf(c.PayDate, c.RecordDate),
				Qty: held, Rate: c.DPS, Gross: gross, TDS: tds, Net: gross - tds, Status: FSIncomeBooked, By: actor}
			if err := o.fsPostIncome(ctx, tenant, actor, s, &x); err != nil {
				return n, err
			}
			all = append(all, x)
			done[id] = true
			n++
		}
	}
	return n, nil
}

func (o *Ops) fsPostIncome(ctx context.Context, tenant, actor string, s FSSettings, x *FSIncome) error {
	if err := o.checkLock(ctx, tenant, x.Date); err != nil {
		return err
	}
	inc := AcctFSDividend
	if x.Kind == "Interest" {
		inc = AcctFSInterest
	}
	ps := []ledger.Posting{{Account: AcctFSIncomeRecv, Debit: x.Net, Memo: x.Kind + " receivable"}}
	if x.TDS > 0 {
		ps = append(ps, ledger.Posting{Account: AcctFSTDS, Debit: x.TDS, Memo: "TDS deducted by the payer"})
	}
	ps = append(ps, ledger.Posting{Account: inc, Credit: x.Gross, Memo: fmt.Sprintf("%s on %s %s", x.Kind, x.Qty, x.Symbol)})
	o.ensureFSAccounts(ctx, tenant, s, []string{AcctFSIncomeRecv, AcctFSTDS, inc})
	e, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: tenant, Key: "FSI:" + x.ID, ValueDate: x.Date, CreatedBy: actor, Source: ledger.SourceRef{Kind: "FIRMSEC", ID: x.ID},
		Narration: fmt.Sprintf("Firm securities · %s · %s %s × %s · %s", strings.ToLower(x.Kind), x.Symbol, x.Qty, x.Rate, PortfolioName(x.Portfolio)), Postings: ps})
	if err != nil {
		return err
	}
	x.Entry = e.ID
	if err := docs.Upsert(ctx, o.Docs, tenant, cFSIncome, x.ID, *x, actor); err != nil {
		return err
	}
	o.log(ctx, tenant, actor, "firm_securities.income_booked", x.ID, map[string]any{"gross": x.Gross.String(), "tds": x.TDS.String()})
	return nil
}

// FSIncomeInput is a dividend or interest entered by hand.
type FSIncomeInput struct {
	Kind      string `json:"kind"`
	Date      string `json:"date"`
	PayDate   string `json:"pay_date"`
	Portfolio string `json:"portfolio"`
	ISIN      string `json:"isin"`
	Gross     string `json:"gross"`
	TDS       string `json:"tds"`
}

// AddFirmSecIncome books a dividend or interest entered by hand (interest
// on bonds, a dividend the corporate actions do not have).
func (o *Ops) AddFirmSecIncome(ctx context.Context, tenant, actor string, in FSIncomeInput) (FSIncome, error) {
	if err := o.require(ctx, tenant, actor, "Entering firm securities", RoleMaker, RoleChecker); err != nil {
		return FSIncome{}, err
	}
	s := o.FirmSecSettings(ctx, tenant)
	in.ISIN = strings.ToUpper(strings.TrimSpace(in.ISIN))
	if in.Kind != "Dividend" && in.Kind != "Interest" {
		return FSIncome{}, invalid("kind must be Dividend or Interest")
	}
	if !ledger.ValidDate(in.Date) || !s.has(in.Portfolio) {
		return FSIncome{}, invalid("give the date (YYYY-MM-DD) and one of the firm's portfolios")
	}
	gross, err := money.ParseAmount(strings.TrimSpace(in.Gross))
	if err != nil || gross <= 0 {
		return FSIncome{}, invalid("give the gross amount in rupees")
	}
	var tds money.Amount
	if strings.TrimSpace(in.TDS) != "" {
		if tds, err = money.ParseAmount(strings.TrimSpace(in.TDS)); err != nil || tds < 0 || tds > gross {
			return FSIncome{}, invalid("TDS is an amount in rupees, not more than the gross")
		}
	}
	_, _, b := o.fsBookTo(ctx, tenant, in.Date)
	lots := b.Lots[fsKey(in.Portfolio, in.ISIN)]
	sym := in.ISIN
	if len(lots) > 0 {
		sym = lots[0].Symbol
	}
	n, err := o.next(ctx, tenant, "fsincome", actor)
	if err != nil {
		return FSIncome{}, err
	}
	x := FSIncome{ID: fmt.Sprintf("FI-%05d", n), Kind: in.Kind, Portfolio: in.Portfolio, ISIN: in.ISIN, Symbol: sym, Date: in.Date, PayDate: firstOf(in.PayDate, in.Date),
		Qty: b.held(in.Portfolio, in.ISIN), Gross: gross, TDS: tds, Net: gross - tds, Status: FSIncomeBooked, By: actor}
	return x, o.fsPostIncome(ctx, tenant, actor, s, &x)
}

// FSBankCredit is a credit line from the firm's own bank statement.
type FSBankCredit struct {
	Date   string `json:"date"`
	Amount string `json:"amount"`
	Ref    string `json:"ref"`
	Text   string `json:"text"` // the narration
}

// FSMatchResult is what matching the credits did.
type FSMatchResult struct {
	Matched   []FSIncome     `json:"matched"`
	Unmatched []FSBankCredit `json:"unmatched"`
}

// FirmSecMatchIncome matches credits from the firm's own bank statement to
// the dividends and interest booked: the same net amount, within 15 days
// of the expected pay date, the narration naming the scrip when more than
// one fits. A match posts Dr the bank, Cr the receivable. Credits that fit
// nothing are given back to be dealt with on the bank's side.
func (o *Ops) FirmSecMatchIncome(ctx context.Context, tenant, actor, bank string, credits []FSBankCredit) (FSMatchResult, error) {
	res := FSMatchResult{Matched: []FSIncome{}, Unmatched: []FSBankCredit{}}
	if err := o.require(ctx, tenant, actor, "Entering firm securities", RoleMaker, RoleChecker); err != nil {
		return res, err
	}
	bank = strings.ToUpper(strings.TrimSpace(firstOf(bank, ledger.BankOwn)))
	if t, err := ledger.TypeOf(bank); err != nil || t != ledger.Asset || !strings.HasPrefix(bank, "BANK:") {
		return res, invalid("choose one of the firm's own bank accounts")
	}
	if bank == ledger.BankClient {
		return res, invalid("dividends on the firm's own securities do not go into the client bank account")
	}
	open := []FSIncome{}
	for _, x := range o.fsIncomes(ctx, tenant) {
		if x.Status == FSIncomeBooked {
			open = append(open, x)
		}
	}
	used := map[string]bool{}
	for _, c := range credits {
		amt, err := money.ParseAmount(strings.ReplaceAll(strings.TrimSpace(c.Amount), ",", ""))
		if err != nil || amt <= 0 || !ledger.ValidDate(c.Date) {
			res.Unmatched = append(res.Unmatched, c)
			continue
		}
		var fits []int
		for i, x := range open {
			if used[x.ID] || x.Net != amt {
				continue
			}
			if d := daysBetween(x.PayDate, c.Date); d < -15 || d > 15 {
				continue
			}
			fits = append(fits, i)
		}
		if len(fits) > 1 {
			var named []int
			up := strings.ToUpper(c.Text)
			for _, i := range fits {
				if strings.Contains(up, open[i].Symbol) {
					named = append(named, i)
				}
			}
			fits = named
		}
		if len(fits) != 1 {
			res.Unmatched = append(res.Unmatched, c)
			continue
		}
		x := open[fits[0]]
		if err := o.checkLock(ctx, tenant, c.Date); err != nil {
			return res, err
		}
		e, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: tenant, Key: "FSIR:" + x.ID, ValueDate: c.Date, CreatedBy: actor, Source: ledger.SourceRef{Kind: "FIRMSEC", ID: x.ID},
			Narration: fmt.Sprintf("Firm securities · %s received · %s · %s", strings.ToLower(x.Kind), x.Symbol, strings.TrimSpace(c.Ref)),
			Postings:  []ledger.Posting{{Account: bank, Debit: amt, Memo: c.Text}, {Account: AcctFSIncomeRecv, Credit: amt, Memo: x.ID}}})
		if err != nil {
			return res, err
		}
		x.Status, x.Received, x.BankRef, x.Bank, x.RecvEntry = FSIncomeReceived, c.Date, strings.TrimSpace(c.Ref), bank, e.ID
		if err := docs.Upsert(ctx, o.Docs, tenant, cFSIncome, x.ID, x, actor); err != nil {
			return res, err
		}
		used[x.ID] = true
		res.Matched = append(res.Matched, x)
	}
	o.log(ctx, tenant, actor, "firm_securities.income_matched", bank, map[string]any{"matched": len(res.Matched), "unmatched": len(res.Unmatched)})
	return res, nil
}

// ---- the demat check (questionnaire c1) ----
//
// The register says what the firm's demat accounts should hold; the
// depository says what they do. A purchase is expected in the demat from
// its pay-out date and a sale leaves on its pay-in date, so until then
// the units are in transit, not a break. The demat side comes from a
// statement loaded here (from the DP's holding statement for the firm's
// own accounts), or else from the DP holdings Ledgerline already has for
// the firm's own demat accounts (DP-HOLDINGS rows whose BOID is one of
// them, or whose UCC is one of the firm's own trading codes).

const cFSDemat = "fsdemat"

// FSDematLine is one ISIN in a demat statement.
type FSDematLine struct {
	ISIN    string    `json:"isin"`
	Symbol  string    `json:"symbol,omitempty"`
	Qty     money.Dec `json:"qty"`
	Pledged money.Dec `json:"pledged,omitempty"`
}

// FSDematStatement is a demat account's holdings on a date.
type FSDematStatement struct {
	ID     string        `json:"id"` // demat|date
	Demat  string        `json:"demat"`
	Date   string        `json:"date"`
	Lines  []FSDematLine `json:"lines"`
	Source string        `json:"source"`
	By     string        `json:"by"`
}

// fsDemats are the firm's demat accounts and the portfolios each holds.
func (s FSSettings) fsDemats() map[string][]string {
	out := map[string][]string{}
	if s.Demats == "one" {
		d := firstOf(s.InvDemat, firstOf(s.SITDemat, "OWN"))
		out[d] = s.Ports()
		return out
	}
	for _, p := range s.Ports() {
		d := s.InvDemat
		if p == PortSIT {
			d = s.SITDemat
		}
		d = firstOf(d, "OWN-"+p)
		out[d] = append(out[d], p)
	}
	return out
}

// SaveFirmSecDemat records a demat statement of the firm's own account.
func (o *Ops) SaveFirmSecDemat(ctx context.Context, tenant, actor string, st FSDematStatement) (FSDematStatement, error) {
	if err := o.require(ctx, tenant, actor, "Entering firm securities", RoleMaker, RoleChecker); err != nil {
		return st, err
	}
	s := o.FirmSecSettings(ctx, tenant)
	if _, ok := s.fsDemats()[st.Demat]; !ok {
		return st, invalid("%q is not one of the firm's demat accounts (Settings → Firm practices → Firm securities)", st.Demat)
	}
	if !ledger.ValidDate(st.Date) {
		return st, invalid("date must be YYYY-MM-DD")
	}
	for i := range st.Lines {
		st.Lines[i].ISIN = strings.ToUpper(strings.TrimSpace(st.Lines[i].ISIN))
		if st.Lines[i].ISIN == "" || st.Lines[i].Qty.Sign() < 0 {
			return st, invalid("line %d: give the ISIN and a quantity", i+1)
		}
	}
	st.ID, st.By, st.Source = st.Demat+"|"+st.Date, actor, firstOf(st.Source, "Entered")
	if err := docs.Upsert(ctx, o.Docs, tenant, cFSDemat, st.ID, st, actor); err != nil {
		return st, err
	}
	o.log(ctx, tenant, actor, "firm_securities.demat_statement", st.ID, map[string]any{"lines": len(st.Lines)})
	o.fsDematCheck(ctx, tenant, st.Date)
	return st, nil
}

// fsDematOn is a demat account's holdings on a date: the latest statement
// on or before it, else the DP holdings Ledgerline keeps for it.
func (o *Ops) fsDematOn(ctx context.Context, tenant string, s FSSettings, demat, date string) (map[string]FSDematLine, string, bool) {
	sts, _ := docs.List[FSDematStatement](ctx, o.Docs, tenant, cFSDemat, demat+"|")
	var best *FSDematStatement
	for i := range sts {
		if sts[i].Date <= date && (best == nil || sts[i].Date > best.Date) {
			best = &sts[i]
		}
	}
	out := map[string]FSDematLine{}
	if best != nil {
		for _, l := range best.Lines {
			x := out[l.ISIN]
			x.ISIN, x.Symbol, x.Qty, x.Pledged = l.ISIN, firstOf(x.Symbol, l.Symbol), x.Qty.Add(l.Qty), x.Pledged.Add(l.Pledged)
			out[l.ISIN] = x
		}
		return out, fmt.Sprintf("statement of %s (%s)", fmtDay(best.Date), strings.ToLower(best.Source)), true
	}
	hs, _ := o.Holdings(ctx, tenant, "")
	codes := s.ownCodes()
	want := normDematID(demat)
	found := false
	for _, h := range hs {
		bo := normDematID(h.BOID)
		if !(want != "" && bo == want) && !(len(s.fsDemats()) == 1 && contains(codes, strings.ToUpper(h.UCC))) {
			continue
		}
		found = true
		x := out[h.ISIN]
		x.ISIN, x.Symbol, x.Qty, x.Pledged = h.ISIN, firstOf(x.Symbol, h.Symbol), x.Qty.Add(h.Qty), x.Pledged.Add(h.Pledged)
		out[h.ISIN] = x
	}
	return out, "DP holdings loaded in Ledgerline", found
}

// FSReconRow is one ISIN of a demat account compared.
type FSReconRow struct {
	Demat      string    `json:"demat"`
	ISIN       string    `json:"isin"`
	Symbol     string    `json:"symbol"`
	Register   money.Dec `json:"register"`    // the register's holding on the date
	TransitIn  money.Dec `json:"transit_in"`  // bought, pay-out still to come
	TransitOut money.Dec `json:"transit_out"` // sold, pay-in still to come
	Expected   money.Dec `json:"expected"`    // what the demat should hold
	DematQty   money.Dec `json:"demat_qty"`
	Difference money.Dec `json:"difference"` // demat − expected
	Pledged    money.Dec `json:"pledged"`    // pledged in the register
	DematPl    money.Dec `json:"demat_pledged"`
	Status     string    `json:"status"` // Agreed, In transit, Break
}

// FSReconView is the demat check on a date.
type FSReconView struct {
	Date    string            `json:"date"`
	Rows    []FSReconRow      `json:"rows"`
	Sources map[string]string `json:"sources"`
	Breaks  int               `json:"breaks"`
	Transit int               `json:"transit"`
	Missing []string          `json:"missing"` // demat accounts with no holdings
	Check   string            `json:"check"`
}

// FirmSecRecon compares the register with the firm's demat accounts.
func (o *Ops) FirmSecRecon(ctx context.Context, tenant, actor, date string) (FSReconView, error) {
	if err := o.require(ctx, tenant, actor, "Viewing firm securities", RoleMaker, RoleChecker, RoleRisk, RoleCompliance); err != nil {
		return FSReconView{}, err
	}
	if !ledger.ValidDate(date) {
		return FSReconView{}, invalid("date must be YYYY-MM-DD")
	}
	return o.fsRecon(ctx, tenant, date), nil
}

func (o *Ops) fsRecon(ctx context.Context, tenant, date string) FSReconView {
	s, ts, b := o.fsBookTo(ctx, tenant, date)
	v := FSReconView{Date: date, Rows: []FSReconRow{}, Sources: map[string]string{}, Missing: []string{}, Check: s.DematCheck}
	pledged := o.fsPledgedOn(ctx, tenant, date)
	for _, demat := range sortedKeys(s.fsDemats()) {
		ports := s.fsDemats()[demat]
		have, src, ok := o.fsDematOn(ctx, tenant, s, demat, date)
		if !ok {
			v.Missing = append(v.Missing, demat)
		}
		v.Sources[demat] = src
		rows := map[string]*FSReconRow{}
		get := func(isin, sym string) *FSReconRow {
			r := rows[isin]
			if r == nil {
				r = &FSReconRow{Demat: demat, ISIN: isin, Symbol: sym}
				rows[isin] = r
			}
			if r.Symbol == "" || r.Symbol == r.ISIN {
				r.Symbol = firstOf(sym, r.Symbol)
			}
			return r
		}
		for _, p := range ports {
			for _, l := range b.lotsOf(p) {
				r := get(l.ISIN, l.Symbol)
				r.Register = r.Register.Add(l.Qty)
			}
			for k, q := range pledged {
				if pp, isin, _ := strings.Cut(k, "|"); pp == p {
					r := get(isin, isin)
					r.Pledged = r.Pledged.Add(q)
				}
			}
			for _, t := range ts {
				if !t.live() || t.Portfolio != p || t.Date > date || t.PayDate == "" || t.PayDate <= date || t.Type == FSTypeJournal || t.Type == FSTypeOpening {
					continue
				}
				// The part of a day's line that was squared off the same day
				// never touches the demat.
				q := t.Qty
				if r := b.Res[t.ID]; r != nil {
					q = q.Sub(r.SpecQty)
				}
				r := get(t.ISIN, t.Symbol)
				if t.Side == "B" {
					r.TransitIn = r.TransitIn.Add(q)
				} else {
					r.TransitOut = r.TransitOut.Add(q)
				}
			}
		}
		for isin, h := range have {
			r := get(isin, h.Symbol)
			r.DematQty, r.DematPl = h.Qty, h.Pledged
		}
		for _, isin := range sortedKeys(rows) {
			r := rows[isin]
			r.Expected = r.Register.Sub(r.TransitIn).Add(r.TransitOut)
			r.Difference = r.DematQty.Sub(r.Expected)
			switch {
			case r.Difference.Sign() != 0:
				r.Status = "Break"
				v.Breaks++
			case r.TransitIn.Sign() != 0 || r.TransitOut.Sign() != 0:
				r.Status = "In transit"
				v.Transit++
			default:
				r.Status = "Agreed"
			}
			if r.Register.Sign() == 0 && r.DematQty.Sign() == 0 && r.TransitIn.Sign() == 0 && r.TransitOut.Sign() == 0 {
				continue
			}
			v.Rows = append(v.Rows, *r)
		}
	}
	return v
}

// fsDematCheck raises a work item when the firm's demat accounts do not
// agree with the register on a date (the firm's practice c1: daily, or
// on a month's last day).
func (o *Ops) fsDematCheck(ctx context.Context, tenant, date string) {
	s := o.FirmSecSettings(ctx, tenant)
	if s.Portfolios == FSNone || s.DematCheck == "off" {
		return
	}
	if s.DematCheck == "monthly" && addDays(date, 1)[:7] == date[:7] {
		return
	}
	v := o.fsRecon(ctx, tenant, date)
	if v.Breaks == 0 {
		o.closeWork(ctx, tenant, SystemActor, "firmsec", "demat|"+date, "agreed")
		return
	}
	var ex []string
	for _, r := range v.Rows {
		if r.Status == "Break" && len(ex) < 5 {
			ex = append(ex, fmt.Sprintf("%s: demat %s, expected %s", r.Symbol, r.DematQty, r.Expected))
		}
	}
	o.raiseWork(ctx, tenant, "firmsec", "demat|"+date, fmt.Sprintf("Firm's demat does not agree with the register on %s", fmtDay(date)),
		fmt.Sprintf("%d differences (%s). Purchases before their pay-out and sales before their pay-in are in transit and not counted. Look on Firm securities → Reconciliation.", v.Breaks, strings.Join(ex, "; ")),
		"", RoleChecker, "firmsec", date)
}
