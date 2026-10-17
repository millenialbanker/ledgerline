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

// ---- margin trading facility (MTF) ----
//
// Enrolled clients may buy eligible securities with part of the cost
// funded by the firm from its own funds or borrowings, never from other
// clients' money. The client puts up the initial margin (for Group I
// securities in F&O: VaR + 3 × ELM; other Group I: VaR + 5 × ELM; the
// firm's minimum applies), and the funded securities are pledged to the
// firm under an MTF pledge. Interest accrues daily on the funded amount
// (exempt from GST). Each day positions are marked to market; when the
// client's equity falls below the maintenance margin a margin call is
// raised, and positions are liquidated if it is not met in time or if the
// pledge is not made by T+1. The firm's MTF book is limited to a multiple
// of its net worth, and funded positions are reported to the exchanges
// daily.
//
// Ledger: funding moves the funded part of the client's debit to an MTF
// loan receivable, and the firm moves the same amount from its own bank
// to the client bank account.

const (
	cMTFConf  = "mtfconf"
	cMTFEnrol = "mtfenrol"
	cMTFPos   = "mtfpos"
	cMTFList  = "mtflist"

	IncomeMTFInterest = "INCOME:MTF_INTEREST"
)

func mtfLoan(ucc string) string { return "RECEIVABLE:MTF:" + ucc }

// MTFConfig is the firm's MTF policy.
type MTFConfig struct {
	RatePA       money.Dec    `json:"rate_pa"`        // % a year
	MinMarginPct money.Dec    `json:"min_margin_pct"` // floor on initial margin %
	MaintainPct  money.Dec    `json:"maintain_pct"`   // of initial margin, below which a call is raised
	CallDays     int          `json:"call_days"`      // business days to meet a call
	NetWorth     money.Amount `json:"net_worth"`
	Borrowed     money.Amount `json:"borrowed"`
	MaxTimes     int64        `json:"max_times_net_worth"` // book ≤ this × net worth
}

// DefaultMTFConfig is a common policy.
func DefaultMTFConfig() MTFConfig {
	return MTFConfig{RatePA: money.MustDec("18"), MinMarginPct: money.MustDec("20"), MaintainPct: money.MustDec("80"), CallDays: 1, NetWorth: 2500000000, Borrowed: 0, MaxTimes: 5}
}

// MTFEnrolment is a client's MTF agreement.
type MTFEnrolment struct {
	UCC    string       `json:"ucc"`
	Name   string       `json:"name"`
	Agreed string       `json:"agreed_on"`
	Limit  money.Amount `json:"limit"`
	RatePA money.Dec    `json:"rate_pa"`
	Active bool         `json:"active"`
	By     string       `json:"by"`
}

// MTFEligible is a security on the firm's MTF list.
type MTFEligible struct {
	ISIN      string    `json:"isin"`
	Symbol    string    `json:"symbol"`
	MarginPct money.Dec `json:"margin_pct"`
	InFO      bool      `json:"in_fo"`
	LTP       money.Dec `json:"ltp"`
}

// MTFPosition is one funded purchase.
type MTFPosition struct {
	ID         string       `json:"id"`
	UCC        string       `json:"ucc"`
	Name       string       `json:"name"`
	ISIN       string       `json:"isin"`
	Symbol     string       `json:"symbol"`
	BuyDate    string       `json:"buy_date"`
	Qty        int64        `json:"qty"`
	Price      money.Dec    `json:"price"`
	Cost       money.Amount `json:"cost"`
	MarginPct  money.Dec    `json:"margin_pct"`
	Funded     money.Amount `json:"funded"` // outstanding
	Interest   money.Amount `json:"interest_posted"`
	InterestTo string       `json:"interest_to,omitempty"` // last date interest was posted to
	Pledge     string       `json:"pledge"`                // Pending, Pledged
	PledgeRef  string       `json:"pledge_ref,omitempty"`
	CallSince  string       `json:"call_since,omitempty"`
	Status     string       `json:"status"` // Open, Closed, Liquidated
	Entry      string       `json:"entry"`
	// Computed for a date.
	LTP      money.Dec    `json:"ltp"`
	Value    money.Amount `json:"value"`
	Equity   money.Amount `json:"equity"`
	Required money.Amount `json:"required"`
	Accrued  money.Amount `json:"accrued"`
	State    string       `json:"state"` // OK, Margin call, Liquidate, Pledge pending
	Why      string       `json:"why,omitempty"`
}

func (o *Ops) mtfConfig(ctx context.Context, tenant string) MTFConfig {
	c, _, _ := docs.GetOr(ctx, o.Docs, tenant, cMTFConf, "main", DefaultMTFConfig())
	return c
}

// SaveMTFConfig sets the firm's MTF policy.
func (o *Ops) SaveMTFConfig(ctx context.Context, tenant, actor string, c MTFConfig) (MTFConfig, error) {
	if err := o.require(ctx, tenant, actor, "Changing the MTF policy", RoleAdmin, RoleRisk); err != nil {
		return c, err
	}
	if c.RatePA.Sign() < 0 || c.MinMarginPct.Sign() <= 0 || c.MinMarginPct.Cmp(money.DecInt(100)) >= 0 || c.MaintainPct.Sign() <= 0 || c.MaintainPct.Cmp(money.DecInt(100)) > 0 {
		return c, invalid("rates and margins must be sensible percentages")
	}
	if c.CallDays < 0 || c.CallDays > 5 || c.MaxTimes < 1 || c.NetWorth <= 0 {
		return c, invalid("give the net worth, a book limit of at least 1× and 0 to 5 days to meet a call")
	}
	if c.NetWorth < 30000000 {
		return c, invalid("a broker needs a net worth of at least ₹3 crore to offer MTF")
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cMTFConf, "main", c, actor); err != nil {
		return c, err
	}
	o.log(ctx, tenant, actor, "mtf.policy", "main", nil)
	return c, nil
}

// MTFList is the eligible list for a date: securities on the MTF-ELIGIBLE
// file if one is loaded, else every priced cash-market equity, with the
// margin from VaR and ELM.
func (o *Ops) MTFList(ctx context.Context, tenant, date string) (map[string]MTFEligible, error) {
	conf := o.mtfConfig(ctx, tenant)
	m, look, err := o.Market(ctx, tenant, date)
	if err != nil {
		return nil, err
	}
	fo := map[string]bool{}
	for _, i := range m.Instruments {
		if i.Type == "FUT" {
			fo[i.Symbol] = true
		}
	}
	listed, _ := docs.List[MTFEligible](ctx, o.Docs, tenant, cMTFList, "")
	out := map[string]MTFEligible{}
	add := func(isin, sym string, given money.Dec) {
		i, ok := look.ByISIN(isin)
		if !ok {
			return
		}
		e := MTFEligible{ISIN: isin, Symbol: i.Symbol, InFO: fo[i.Symbol], LTP: i.LTP}
		mult := int64(5)
		if e.InFO {
			mult = 3
		}
		e.MarginPct = i.VaRPct.Add(i.ELMPct.MulInt(mult))
		if given.Cmp(e.MarginPct) > 0 {
			e.MarginPct = given
		}
		if e.MarginPct.Cmp(conf.MinMarginPct) < 0 {
			e.MarginPct = conf.MinMarginPct
		}
		out[isin] = e
	}
	if len(listed) > 0 {
		for _, l := range listed {
			add(l.ISIN, l.Symbol, l.MarginPct)
		}
		return out, nil
	}
	for _, i := range m.Instruments {
		if i.Segment == "CM" && i.Type == "EQ" && i.ISIN != "" && i.LTP.Sign() > 0 && i.VaRPct.Sign() > 0 {
			add(i.ISIN, i.Symbol, money.Dec{})
		}
	}
	return out, nil
}

// EnrolMTF records a client's MTF agreement.
func (o *Ops) EnrolMTF(ctx context.Context, tenant, actor string, e MTFEnrolment) (MTFEnrolment, error) {
	if err := o.require(ctx, tenant, actor, "MTF enrolment", RoleChecker, RoleRisk); err != nil {
		return e, err
	}
	c, err := o.Client(ctx, tenant, e.UCC)
	if err != nil {
		return e, invalid("client %s not found", e.UCC)
	}
	if c.Status != "Active" || !contains(c.Segments, "CM") {
		return e, invalid("%s must be active in the equity cash segment", c.UCC)
	}
	if !ledger.ValidDate(e.Agreed) {
		return e, invalid("give the date the client signed the MTF agreement (rights and obligations)")
	}
	if e.Limit <= 0 {
		return e, invalid("give the client's funding limit")
	}
	conf := o.mtfConfig(ctx, tenant)
	if e.RatePA.Sign() == 0 {
		e.RatePA = conf.RatePA
	}
	e.UCC, e.Name, e.Active, e.By = c.UCC, c.Name, true, actor
	if err := docs.Upsert(ctx, o.Docs, tenant, cMTFEnrol, c.UCC, e, actor); err != nil {
		return e, err
	}
	o.log(ctx, tenant, actor, "mtf.enrolled", c.UCC, map[string]any{"limit": e.Limit.String(), "rate": e.RatePA.String()})
	return e, nil
}

// MTFCandidate is a delivery buy that can be funded.
type MTFCandidate struct {
	UCC       string       `json:"ucc"`
	Name      string       `json:"name"`
	ISIN      string       `json:"isin"`
	Symbol    string       `json:"symbol"`
	Qty       int64        `json:"qty"`
	Price     money.Dec    `json:"price"`
	Value     money.Amount `json:"value"`
	MarginPct money.Dec    `json:"margin_pct"`
	Fundable  money.Amount `json:"fundable"`
	Funded    int64        `json:"funded_qty"`
}

// MTFView is the MTF desk.
type MTFView struct {
	Date       string         `json:"date"`
	Config     MTFConfig      `json:"config"`
	Enrolled   []MTFEnrolment `json:"enrolled"`
	Positions  []MTFPosition  `json:"positions"`
	Candidates []MTFCandidate `json:"candidates"`
	Eligible   []MTFEligible  `json:"eligible"`
	Book       money.Amount   `json:"book"`
	BookLimit  money.Amount   `json:"book_limit"`
	Calls      int            `json:"calls"`
	Liquidate  int            `json:"liquidate"`
	Accrued    money.Amount   `json:"accrued"`
}

func dayInterest(funded money.Amount, rate money.Dec, days int) money.Amount {
	if days <= 0 || funded <= 0 {
		return 0
	}
	v := funded.Dec().Pct(rate).MulInt(int64(days))
	q, _ := v.Quo(money.DecInt(365))
	return q.Round(money.UnitPaisa)
}

// MTF builds the desk for a date: positions marked to market with margin
// state and interest accrued, enrolled clients, and today's delivery buys
// by enrolled clients in eligible securities that can still be funded.
func (o *Ops) MTF(ctx context.Context, tenant, date string) (MTFView, error) {
	conf := o.mtfConfig(ctx, tenant)
	v := MTFView{Date: date, Config: conf, Enrolled: []MTFEnrolment{}, Positions: []MTFPosition{}, Candidates: []MTFCandidate{}, Eligible: []MTFEligible{}}
	v.BookLimit = conf.NetWorth * money.Amount(conf.MaxTimes)
	el, err := o.MTFList(ctx, tenant, date)
	if err != nil {
		return v, err
	}
	for _, k := range sortedKeys(el) {
		v.Eligible = append(v.Eligible, el[k])
	}
	sort.Slice(v.Eligible, func(i, j int) bool { return v.Eligible[i].Symbol < v.Eligible[j].Symbol })
	ens, _ := docs.List[MTFEnrolment](ctx, o.Docs, tenant, cMTFEnrol, "")
	enr := map[string]MTFEnrolment{}
	for _, e := range ens {
		if e.Active {
			enr[e.UCC] = e
			v.Enrolled = append(v.Enrolled, e)
		}
	}
	set, _ := o.Settings(ctx, tenant)
	_, look, _ := o.Market(ctx, tenant, date)
	ps, _ := docs.List[MTFPosition](ctx, o.Docs, tenant, cMTFPos, "")
	fundedQty := map[string]int64{}
	for _, p := range ps {
		if p.BuyDate == date {
			fundedQty[p.UCC+"|"+p.ISIN] += p.Qty
		}
		if p.Status != "Open" {
			continue
		}
		if i, ok := look.ByISIN(p.ISIN); ok {
			p.LTP = i.LTP
		} else {
			p.LTP = p.Price
		}
		p.Value = p.LTP.MulInt(p.Qty).Round(money.UnitPaisa)
		p.Equity = p.Value - p.Funded
		p.Required = p.Value.Dec().Pct(p.MarginPct).Pct(conf.MaintainPct).Round(money.UnitPaisa)
		from := p.InterestTo
		if from == "" {
			from = p.BuyDate
		}
		rate := conf.RatePA
		if e, ok := enr[p.UCC]; ok {
			rate = e.RatePA
		}
		p.Accrued = dayInterest(p.Funded, rate, daysBetween(from, date))
		v.Accrued += p.Accrued
		v.Book += p.Funded
		p.State = "OK"
		switch {
		case p.Pledge != "Pledged" && date > set.nextBusinessDay(p.BuyDate):
			p.State, p.Why = "Liquidate", "MTF pledge not made by T+1"
		case p.Equity < p.Required:
			p.State = "Margin call"
			p.Why = fmt.Sprintf("equity ₹%s below maintenance ₹%s; shortfall ₹%s", formatINR(p.Equity.Dec()), formatINR(p.Required.Dec()), formatINR((p.Required - p.Equity).Dec()))
			if p.CallSince != "" {
				due := p.CallSince
				for i := 0; i < conf.CallDays; i++ {
					due = set.nextBusinessDay(due)
				}
				if date > due {
					p.State = "Liquidate"
					p.Why = "margin call of " + p.CallSince + " not met by " + due + "; " + p.Why
				}
			}
		case p.Pledge != "Pledged":
			p.State, p.Why = "Pledge pending", "pledge the shares to the firm (MTF) by T+1"
		}
		if p.State == "Margin call" {
			v.Calls++
		}
		if p.State == "Liquidate" {
			v.Liquidate++
		}
		v.Positions = append(v.Positions, p)
	}
	sort.Slice(v.Positions, func(i, j int) bool {
		return v.Positions[i].UCC+v.Positions[i].ID < v.Positions[j].UCC+v.Positions[j].ID
	})
	if len(enr) > 0 {
		ts, err := o.exchangeTrades(ctx, tenant, date)
		if err != nil {
			return v, err
		}
		agg := map[string]*MTFCandidate{}
		for _, t := range ts {
			if t.Segment != "CM" || t.Product != "CNC" {
				continue
			}
			if _, ok := enr[t.UCC]; !ok {
				continue
			}
			i, ok := look.ForTrade(t)
			if !ok {
				continue
			}
			e, ok := el[i.ISIN]
			if !ok {
				continue
			}
			k := t.UCC + "|" + i.ISIN
			c := agg[k]
			if c == nil {
				c = &MTFCandidate{UCC: t.UCC, Name: enr[t.UCC].Name, ISIN: i.ISIN, Symbol: i.Symbol, MarginPct: e.MarginPct}
				agg[k] = c
			}
			v := t.Price.MulInt(t.Qty).Round(money.UnitPaisa)
			if t.Side == "B" {
				c.Qty += t.Qty
				c.Value += v
			} else {
				c.Qty -= t.Qty
				c.Value -= v
			}
		}
		for _, k := range sortedKeys(agg) {
			c := agg[k]
			c.Funded = fundedQty[k]
			if c.Qty-c.Funded <= 0 {
				continue
			}
			c.Price, _ = c.Value.Dec().Quo(money.DecInt(c.Qty))
			c.Fundable = c.Value - c.Value.Dec().Pct(c.MarginPct).Round(money.UnitPaisa)
			v.Candidates = append(v.Candidates, *c)
		}
	}
	return v, nil
}

// FundMTF funds qty of a client's delivery buy on date. The client keeps
// the initial margin in their ledger; the rest becomes an MTF loan funded
// from the firm's own bank.
func (o *Ops) FundMTF(ctx context.Context, tenant, actor, date, ucc, isin string, qty int64) (MTFPosition, error) {
	if err := o.require(ctx, tenant, actor, "Funding under MTF", RoleChecker, RoleRisk); err != nil {
		return MTFPosition{}, err
	}
	v, err := o.MTF(ctx, tenant, date)
	if err != nil {
		return MTFPosition{}, err
	}
	var c *MTFCandidate
	for i := range v.Candidates {
		if v.Candidates[i].UCC == strings.ToUpper(ucc) && v.Candidates[i].ISIN == isin {
			c = &v.Candidates[i]
		}
	}
	if c == nil {
		return MTFPosition{}, invalid("no unfunded delivery buy by an enrolled client in %s on %s", isin, date)
	}
	if qty <= 0 || qty > c.Qty-c.Funded {
		return MTFPosition{}, invalid("fund between 1 and %d shares", c.Qty-c.Funded)
	}
	cost := c.Price.MulInt(qty).Round(money.UnitPaisa)
	margin := cost.Dec().Pct(c.MarginPct).Round(money.UnitPaisa)
	funded := cost - margin
	en, _, _ := docs.Get[MTFEnrolment](ctx, o.Docs, tenant, cMTFEnrol, c.UCC)
	var clientBook money.Amount
	for _, p := range v.Positions {
		if p.UCC == c.UCC {
			clientBook += p.Funded
		}
	}
	if clientBook+funded > en.Limit {
		return MTFPosition{}, invalid("funding ₹%s takes %s over their MTF limit of ₹%s (₹%s already funded)", formatINR(funded.Dec()), c.UCC, formatINR(en.Limit.Dec()), formatINR(clientBook.Dec()))
	}
	if v.Book+funded > v.BookLimit {
		return MTFPosition{}, invalid("the firm's MTF book would exceed %d× net worth (₹%s)", v.Config.MaxTimes, formatINR(v.BookLimit.Dec()))
	}
	n, err := o.next(ctx, tenant, "mtf", actor)
	if err != nil {
		return MTFPosition{}, err
	}
	p := MTFPosition{ID: fmt.Sprintf("MTF-%06d", n), UCC: c.UCC, Name: c.Name, ISIN: c.ISIN, Symbol: c.Symbol, BuyDate: date, Qty: qty, Price: c.Price, Cost: cost,
		MarginPct: c.MarginPct, Funded: funded, Pledge: "Pending", Status: "Open"}
	desc := fmt.Sprintf("MTF funding %s %d %s @ %s", p.ID, qty, c.Symbol, c.Price.Fixed(2))
	e, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: tenant, Key: "MTF:" + p.ID, ValueDate: date, CreatedBy: actor, Narration: desc, Source: ledger.SourceRef{Kind: "MTF", ID: p.ID},
		Postings: []ledger.Posting{{Account: mtfLoan(c.UCC), Debit: funded, Memo: desc}, {Account: ledger.ClientAccount(c.UCC), Credit: funded, Memo: desc},
			{Account: ledger.BankClient, Debit: funded, Memo: "own funds for MTF"}, {Account: ledger.BankOwn, Credit: funded, Memo: "own funds for MTF"}}})
	if err != nil {
		return p, err
	}
	p.Entry = e.ID
	if err := docs.Upsert(ctx, o.Docs, tenant, cMTFPos, p.ID, p, actor); err != nil {
		return p, err
	}
	o.log(ctx, tenant, actor, "mtf.funded", p.ID, map[string]any{"ucc": c.UCC, "isin": isin, "qty": qty, "funded": funded.String()})
	return p, nil
}

// MarkMTFPledged records the MTF pledge of a position's shares.
func (o *Ops) MarkMTFPledged(ctx context.Context, tenant, actor, id, ref string) (MTFPosition, error) {
	if err := o.require(ctx, tenant, actor, "Recording MTF pledges", RoleMaker, RoleChecker, RoleRisk); err != nil {
		return MTFPosition{}, err
	}
	if strings.TrimSpace(ref) == "" {
		return MTFPosition{}, invalid("give the pledge reference (PSN)")
	}
	p, err := docs.Update(ctx, o.Docs, tenant, cMTFPos, id, actor, func(p *MTFPosition) error {
		if p.Status != "Open" {
			return fmt.Errorf("%w: %s is %s", ErrState, id, strings.ToLower(p.Status))
		}
		p.Pledge, p.PledgeRef = "Pledged", ref
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "mtf.pledged", id, map[string]any{"ref": ref})
	}
	return p, err
}

// RaiseMTFCalls records margin calls for positions short of margin on a
// date, and messages the clients.
func (o *Ops) RaiseMTFCalls(ctx context.Context, tenant, actor, date string) (int, error) {
	if err := o.require(ctx, tenant, actor, "MTF margin calls", RoleRisk, RoleChecker); err != nil {
		return 0, err
	}
	v, err := o.MTF(ctx, tenant, date)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, p := range v.Positions {
		if p.State == "Margin call" && p.CallSince == "" {
			if _, err := docs.Update(ctx, o.Docs, tenant, cMTFPos, p.ID, actor, func(x *MTFPosition) error { x.CallSince = date; return nil }); err != nil {
				return n, err
			}
			o.notify(ctx, tenant, actor, "margin.shortfall", p.UCC, map[string]string{"shortfall": formatINR((p.Required - p.Equity).Dec()), "date": date})
			n++
		}
		if p.State == "OK" && p.CallSince != "" {
			_, _ = docs.Update(ctx, o.Docs, tenant, cMTFPos, p.ID, actor, func(x *MTFPosition) error { x.CallSince = ""; return nil })
		}
	}
	o.log(ctx, tenant, actor, "mtf.calls", date, map[string]any{"raised": n})
	return n, nil
}

// LiquidationFile lists sell orders for positions to be liquidated.
func (o *Ops) MTFLiquidationFile(ctx context.Context, tenant, date string) ([]byte, int, error) {
	v, err := o.MTF(ctx, tenant, date)
	if err != nil {
		return nil, 0, err
	}
	rows := [][]string{{"ClientCode", "Exchange", "Symbol", "ISIN", "Side", "Quantity", "OrderType", "Product", "Reason", "Position"}}
	n := 0
	for _, p := range v.Positions {
		if p.State == "Liquidate" {
			rows = append(rows, []string{p.UCC, "NSE", p.Symbol, p.ISIN, "S", fmt.Sprint(p.Qty), "MARKET", "MTF", p.Why, p.ID})
			n++
		}
	}
	return csvBytesOf(rows), n, nil
}

// CloseMTF repays a position from the client's ledger credit (after they
// add funds or sell the shares) and closes it, in whole or for qty shares.
// Interest to the date is posted first.
func (o *Ops) CloseMTF(ctx context.Context, tenant, actor, date, id string, qty int64, how string) (MTFPosition, error) {
	if err := o.require(ctx, tenant, actor, "Closing MTF positions", RoleChecker, RoleRisk); err != nil {
		return MTFPosition{}, err
	}
	if _, err := o.PostMTFInterest(ctx, tenant, actor, date, id); err != nil {
		return MTFPosition{}, err
	}
	p, _, err := docs.Get[MTFPosition](ctx, o.Docs, tenant, cMTFPos, id)
	if err != nil {
		return p, err
	}
	if p.Status != "Open" {
		return p, fmt.Errorf("%w: %s is %s", ErrState, id, strings.ToLower(p.Status))
	}
	if qty <= 0 || qty > p.Qty {
		qty = p.Qty
	}
	repay := p.Funded
	if qty < p.Qty {
		q, _ := p.Funded.Dec().MulInt(qty).Quo(money.DecInt(p.Qty))
		repay = q.Round(money.UnitPaisa)
	}
	funds, err := o.clientFunds(ctx, tenant, date, "")
	if err != nil {
		return p, err
	}
	if funds[p.UCC] < repay {
		return p, invalid("%s has ₹%s in the ledger; ₹%s is needed to repay (add funds, or sell the shares first)", p.UCC, formatINR(funds[p.UCC].Dec()), formatINR(repay.Dec()))
	}
	n, err := o.next(ctx, tenant, "mtfrepay", actor)
	if err != nil {
		return p, err
	}
	desc := fmt.Sprintf("MTF repayment %s, %d %s (%s)", p.ID, qty, p.Symbol, how)
	if _, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: tenant, Key: fmt.Sprintf("MTFR:%s:%d", p.ID, n), ValueDate: date, CreatedBy: actor, Narration: desc, Source: ledger.SourceRef{Kind: "MTF", ID: p.ID},
		Postings: []ledger.Posting{{Account: ledger.ClientAccount(p.UCC), Debit: repay, Memo: desc}, {Account: mtfLoan(p.UCC), Credit: repay, Memo: desc},
			{Account: ledger.BankOwn, Debit: repay, Memo: "MTF funds returned"}, {Account: ledger.BankClient, Credit: repay, Memo: "MTF funds returned"}}}); err != nil {
		return p, err
	}
	p, err = docs.Update(ctx, o.Docs, tenant, cMTFPos, id, actor, func(x *MTFPosition) error {
		x.Funded -= repay
		x.Cost -= (x.Cost.Dec().MulInt(qty)).Round(money.UnitPaisa) / money.Amount(x.Qty)
		x.Qty -= qty
		if x.Qty == 0 {
			x.Status = map[bool]string{true: "Liquidated", false: "Closed"}[how == "liquidated"]
			x.Funded = 0
		}
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "mtf.repaid", id, map[string]any{"qty": qty, "repaid": repay.String(), "how": how})
	}
	return p, err
}

// PostMTFInterest posts interest accrued to date on one position, or on
// all open positions when id is empty.
func (o *Ops) PostMTFInterest(ctx context.Context, tenant, actor, date, id string) (money.Amount, error) {
	if err := o.require(ctx, tenant, actor, "Posting MTF interest", RoleChecker, RoleRisk); err != nil {
		return 0, err
	}
	v, err := o.MTF(ctx, tenant, date)
	if err != nil {
		return 0, err
	}
	var total money.Amount
	for _, p := range v.Positions {
		if (id != "" && p.ID != id) || p.Accrued <= 0 {
			continue
		}
		desc := fmt.Sprintf("MTF interest %s on ₹%s to %s", p.ID, formatINR(p.Funded.Dec()), date)
		if _, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: tenant, Key: "MTFI:" + p.ID + ":" + date, ValueDate: date, CreatedBy: actor, Narration: desc, Source: ledger.SourceRef{Kind: "MTF_INTEREST", ID: p.ID},
			Postings: []ledger.Posting{{Account: ledger.ClientAccount(p.UCC), Debit: p.Accrued, Memo: desc}, {Account: IncomeMTFInterest, Credit: p.Accrued, Memo: desc}}}); err != nil {
			return total, err
		}
		if _, err := docs.Update(ctx, o.Docs, tenant, cMTFPos, p.ID, actor, func(x *MTFPosition) error {
			x.Interest += p.Accrued
			x.InterestTo = date
			return nil
		}); err != nil {
			return total, err
		}
		total += p.Accrued
	}
	if id == "" {
		o.log(ctx, tenant, actor, "mtf.interest", date, map[string]any{"total": total.String()})
	}
	return total, nil
}

// MTFReport is the daily exchange report of funded positions.
func (o *Ops) MTFReport(ctx context.Context, tenant, date string) ([]byte, error) {
	v, err := o.MTF(ctx, tenant, date)
	if err != nil {
		return nil, err
	}
	cs, _ := o.ClientMap(ctx, tenant)
	rows := [][]string{{"Date", "ClientCode", "PAN", "ClientName", "Symbol", "ISIN", "Quantity", "FundedAmount", "ClientMargin", "MarketValue", "PledgeStatus"}}
	for _, p := range v.Positions {
		rows = append(rows, []string{date, p.UCC, cs[p.UCC].PAN, p.Name, p.Symbol, p.ISIN, fmt.Sprint(p.Qty), p.Funded.String(), (p.Cost - p.Funded).String(), p.Value.String(), p.Pledge})
	}
	rows = append(rows, []string{}, []string{"Summary", "OwnFunds", "BorrowedFunds", "TotalFunded", "NetWorth", "TimesNetWorth"})
	times, _ := v.Book.Dec().Quo(v.Config.NetWorth.Dec())
	rows = append(rows, []string{date, (v.Book - v.Config.Borrowed).String(), v.Config.Borrowed.String(), v.Book.String(), v.Config.NetWorth.String(), times.Fixed(2)})
	return csvBytesOf(rows), nil
}

// nextBusinessDay is the business day after d.
func (s Settings) nextBusinessDay(d string) string {
	t, err := time.Parse("2006-01-02", d)
	if err != nil {
		return d
	}
	for i := 0; i < 15; i++ {
		t = t.AddDate(0, 0, 1)
		if x := t.Format("2006-01-02"); s.IsBusinessDay(x) {
			return x
		}
	}
	return t.Format("2006-01-02")
}
