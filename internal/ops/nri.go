package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// ---- non-resident clients (portfolio investment scheme) ----
//
// NRIs buy and sell listed equity through a PIS account at a designated
// bank branch, repatriable (NRE) or not (NRO). They may not trade intraday
// or sell short in the cash market, may trade derivatives only with a
// custodial participant code and NRO funds, and may not buy companies on
// the RBI's caution or ban list for foreign investment. Every trade is
// reported to the PIS bank, and tax is deducted at source on capital gains
// when they sell.

const (
	cNRI     = "nri"
	cNRIList = "nrilist"
)

// NRIDetails are a non-resident client's PIS details.
type NRIDetails struct {
	UCC        string `json:"ucc"`
	Account    string `json:"account_type"` // NRE, NRO
	Bank       string `json:"pis_bank"`
	Branch     string `json:"pis_branch"`
	PISAccount string `json:"pis_account"`
	Permission string `json:"pis_permission"` // RBI / bank permission reference
	PermDate   string `json:"pis_permission_date"`
	Country    string `json:"country"`
	CPCode     string `json:"cp_code,omitempty"` // custodial participant code for F&O
	By         string `json:"by"`
}

// NRICaution is a company on the RBI caution or ban list.
type NRICaution struct {
	ISIN   string `json:"isin"`
	Symbol string `json:"symbol"`
	Status string `json:"status"` // Caution, Ban
}

type nriListDoc struct {
	Items []NRICaution `json:"items"`
	AsOf  string       `json:"as_of"`
}

// SetNRI registers a client as an NRI under PIS, or updates the details.
func (o *Ops) SetNRI(ctx context.Context, tenant, actor string, d NRIDetails) (NRIDetails, error) {
	if err := o.require(ctx, tenant, actor, "NRI registration", RoleChecker, RoleCompliance); err != nil {
		return d, err
	}
	c, err := o.Client(ctx, tenant, d.UCC)
	if err != nil {
		return d, invalid("client %s not found", d.UCC)
	}
	d.UCC, d.Account = c.UCC, strings.ToUpper(d.Account)
	switch {
	case d.Account != "NRE" && d.Account != "NRO":
		return d, invalid("account type must be NRE (repatriable) or NRO (non-repatriable)")
	case strings.TrimSpace(d.Bank) == "" || strings.TrimSpace(d.PISAccount) == "":
		return d, invalid("give the designated bank and the PIS account number")
	case strings.TrimSpace(d.Permission) == "" || !ledger.ValidDate(d.PermDate):
		return d, invalid("give the PIS permission reference and date")
	case strings.TrimSpace(d.Country) == "":
		return d, invalid("give the country of residence")
	}
	if contains(c.Segments, "FO") && d.CPCode == "" {
		return d, invalid("%s trades derivatives: give the custodial participant (CP) code", c.UCC)
	}
	d.By = actor
	if err := docs.Upsert(ctx, o.Docs, tenant, cNRI, c.UCC, d, actor); err != nil {
		return d, err
	}
	if c.Type != "NRI" {
		if _, err := docs.Update(ctx, o.Docs, tenant, cClient, c.UCC, actor, func(x *Client) error {
			x.Type = "NRI"
			if !contains(x.CSC, "NRI") {
				x.CSC = append(x.CSC, "NRI")
			}
			return nil
		}); err != nil {
			return d, err
		}
	}
	o.log(ctx, tenant, actor, "nri.registered", c.UCC, map[string]any{"account": d.Account, "bank": d.Bank})
	return d, nil
}

// NRIViolation is a trade that breaks the NRI rules.
type NRIViolation struct {
	UCC     string `json:"ucc"`
	Name    string `json:"name"`
	TradeNo string `json:"trade_no"`
	Time    string `json:"time"`
	What    string `json:"what"`
	Rule    string `json:"rule"`
}

// NRIRow is one NRI client for a date.
type NRIRow struct {
	NRIDetails
	Name     string       `json:"name"`
	Missing  bool         `json:"details_missing"`
	Trades   int          `json:"trades"`
	Bought   money.Amount `json:"bought"`
	Sold     money.Amount `json:"sold"`
	Holdings money.Dec    `json:"holdings_value"`
}

// NRIView is the NRI desk.
type NRIView struct {
	Date        string         `json:"date"`
	Rows        []NRIRow       `json:"rows"`
	Violations  []NRIViolation `json:"violations"`
	Caution     []NRICaution   `json:"caution"`
	CautionAsOf string         `json:"caution_as_of"`
	TDS         []NRITDS       `json:"tds"`
}

// NRI builds the desk for a date and checks the day's NRI trades, raising
// an alert for each violation.
func (o *Ops) NRI(ctx context.Context, tenant, actor, date string) (NRIView, error) {
	v := NRIView{Date: date, Rows: []NRIRow{}, Violations: []NRIViolation{}, Caution: []NRICaution{}, TDS: []NRITDS{}}
	cs, err := o.ClientMap(ctx, tenant)
	if err != nil {
		return v, err
	}
	cl, _, _ := docs.GetOr(ctx, o.Docs, tenant, cNRIList, "current", nriListDoc{})
	v.Caution, v.CautionAsOf = cl.Items, cl.AsOf
	if v.Caution == nil {
		v.Caution = []NRICaution{}
	}
	caution := map[string]string{}
	for _, x := range cl.Items {
		caution[x.ISIN] = x.Status
		caution[x.Symbol] = x.Status
	}
	ts, err := o.exchangeTrades(ctx, tenant, date)
	if err != nil {
		return v, err
	}
	_, look, _ := o.Market(ctx, tenant, date)
	hs, _ := o.Holdings(ctx, tenant, "")
	held := map[string]int64{}
	hval := map[string]money.Dec{}
	for _, h := range hs {
		held[h.UCC+"|"+h.Symbol] += h.Qty
		if i, ok := look.ByISIN(h.ISIN); ok {
			hval[h.UCC] = hval[h.UCC].Add(i.LTP.MulInt(h.Qty))
		}
	}
	rows := map[string]*NRIRow{}
	for _, u := range sortedKeys(cs) {
		c := cs[u]
		if c.Type != "NRI" || c.Status != "Active" {
			continue
		}
		d, _, err := docs.Get[NRIDetails](ctx, o.Docs, tenant, cNRI, u)
		r := &NRIRow{NRIDetails: d, Name: c.Name, Missing: err != nil, Holdings: hval[u]}
		r.UCC = u
		rows[u] = r
	}
	sells := map[string]int64{}
	sort.Slice(ts, func(i, j int) bool { return ts[i].TradeTime < ts[j].TradeTime })
	for _, t := range ts {
		r := rows[t.UCC]
		if r == nil {
			continue
		}
		r.Trades++
		val := t.Value().Round(money.UnitPaisa)
		if t.Side == "B" {
			r.Bought += val
		} else {
			r.Sold += val
		}
		bad := func(what, rule string) {
			v.Violations = append(v.Violations, NRIViolation{UCC: t.UCC, Name: r.Name, TradeNo: t.TradeNo, Time: t.TradeTime, What: what, Rule: rule})
		}
		desc := fmt.Sprintf("%s %d %s @ %s", map[string]string{"B": "Buy", "S": "Sell"}[t.Side], t.Qty, t.Symbol, t.Price.Fixed(2))
		if r.Missing {
			bad(desc, "no PIS details on record")
		}
		switch t.Segment {
		case "CM":
			if t.Product == "MIS" {
				bad(desc, "intraday trading is not permitted under PIS")
			}
			if t.Side == "S" && t.Product == "CNC" {
				sells[t.UCC+"|"+t.Symbol] += t.Qty
				if sells[t.UCC+"|"+t.Symbol] > held[t.UCC+"|"+t.Symbol] {
					bad(desc, "short sale: NRIs may sell only shares they hold")
				}
			}
			if t.Side == "B" {
				isin := ""
				if i, ok := look.ForTrade(t); ok {
					isin = i.ISIN
				}
				if st := firstOf(caution[isin], caution[t.Symbol]); st == "Ban" {
					bad(desc, "company is on the RBI ban list for NRI purchases")
				} else if st == "Caution" {
					bad(desc, "company is on the RBI caution list: purchases need the bank's prior approval")
				}
			}
		case "FO":
			if r.CPCode == "" {
				bad(desc, "derivatives need a custodial participant code")
			}
			if r.Account == "NRE" {
				bad(desc, "derivatives may be funded only from an NRO account")
			}
		default:
			bad(desc, "NRIs may not trade in this segment")
		}
	}
	for _, x := range v.Violations {
		o.raiseAlert(ctx, tenant, actor, Alert{Source: "Internal", Type: "NRI trading rule", UCC: x.UCC, Severity: "High", Detail: x.What + ": " + x.Rule}, "NRI:"+date+":"+x.TradeNo)
	}
	for _, u := range sortedKeys(rows) {
		v.Rows = append(v.Rows, *rows[u])
	}
	v.TDS, _ = o.NRITDS(ctx, tenant, date, date)
	return v, nil
}

// PISFile is the daily trade report to the PIS banks, one row per trade.
func (o *Ops) PISFile(ctx context.Context, tenant, date string) ([]byte, int, error) {
	ts, err := o.exchangeTrades(ctx, tenant, date)
	if err != nil {
		return nil, 0, err
	}
	cs, _ := o.ClientMap(ctx, tenant)
	_, look, _ := o.Market(ctx, tenant, date)
	rows := [][]string{{"TradeDate", "PISBank", "PISAccount", "AccountType", "ClientCode", "ClientName", "PAN", "Exchange", "TradeNo", "ISIN", "Symbol", "BuySell", "Quantity", "Price", "Value"}}
	n := 0
	for _, t := range ts {
		c := cs[t.UCC]
		if c.Type != "NRI" || t.Segment != "CM" {
			continue
		}
		d, _, _ := docs.Get[NRIDetails](ctx, o.Docs, tenant, cNRI, t.UCC)
		isin := ""
		if i, ok := look.ForTrade(t); ok {
			isin = i.ISIN
		}
		rows = append(rows, []string{date, d.Bank, d.PISAccount, d.Account, t.UCC, c.Name, c.PAN, t.Exchange, t.TradeNo, isin, t.Symbol, t.Side, fmt.Sprint(t.Qty), t.Price.Fixed(2), t.Value().Fixed(2)})
		n++
	}
	return csvBytesOf(rows), n, nil
}

// NRITDS is tax to deduct on one NRI sale.
type NRITDS struct {
	UCC      string       `json:"ucc"`
	Name     string       `json:"name"`
	Date     string       `json:"date"`
	Symbol   string       `json:"symbol"`
	Qty      int64        `json:"qty"`
	Category string       `json:"category"`
	Gain     money.Amount `json:"gain"`
	Exempt   money.Amount `json:"exempt"`
	RatePct  money.Dec    `json:"rate_pct"`
	Tax      money.Amount `json:"tax"`
	Cess     money.Amount `json:"cess"`
	TDS      money.Amount `json:"tds"`
}

// NRI capital gains rates on listed equity (sales from 23 July 2024):
// short-term 20% (s.111A), long-term 12.5% above ₹1.25 lakh a year
// (s.112A); speculative (intraday) income at 30%; 4% health and
// education cess. Surcharge depends on total
// income and is left to the bank.
var (
	nriSTCG    = money.DecInt(20)
	nriLTCG    = money.MustDec("12.5")
	nriExempt  = money.Amount(12500000)
	nriCessPct = money.DecInt(4)
)

// NRITDS computes TDS on NRI clients' equity sales between from and to,
// using the first-in first-out purchase lots behind the capital gains
// report. The ₹1.25 lakh long-term exemption is used up through the year.
func (o *Ops) NRITDS(ctx context.Context, tenant, from, to string) ([]NRITDS, error) {
	cs, err := o.ClientMap(ctx, tenant)
	if err != nil {
		return nil, err
	}
	out := []NRITDS{}
	fy := fyStartOf(from)
	for _, u := range sortedKeys(cs) {
		c := cs[u]
		if c.Type != "NRI" {
			continue
		}
		_, sales, _, err := o.equityBook(ctx, tenant, u, to)
		if err != nil {
			return out, err
		}
		var ltcgSoFar money.Amount
		for _, s := range sales {
			if s.Date < fy {
				continue
			}
			g := s.Gain.Round(money.UnitPaisa)
			if s.Category == CatLTCG && s.Date < from {
				ltcgSoFar += g
			}
			if s.Date < from || s.Date > to {
				continue
			}
			t := NRITDS{UCC: u, Name: c.Name, Date: s.Date, Symbol: s.Symbol, Qty: s.Qty, Category: s.Category, Gain: g}
			switch s.Category {
			case CatSTCG:
				t.RatePct = nriSTCG
			case CatSpec:
				t.RatePct = money.DecInt(30) // speculative business income, highest slab
			case CatLTCG:
				t.RatePct = nriLTCG
				left := nriExempt - ltcgSoFar
				if left < 0 {
					left = 0
				}
				if g > 0 {
					t.Exempt = min(g, left)
					ltcgSoFar += g
				}
			default:
				t.RatePct = nriSTCG // cost unknown: the whole proceeds are treated as gain until cost is given
				t.Gain = s.Proceeds.Round(money.UnitPaisa)
			}
			if taxable := t.Gain - t.Exempt; taxable > 0 {
				t.Tax = taxable.Dec().Pct(t.RatePct).Round(money.UnitPaisa)
				t.Cess = t.Tax.Dec().Pct(nriCessPct).Round(money.UnitPaisa)
				t.TDS = t.Tax + t.Cess
			}
			out = append(out, t)
		}
	}
	return out, nil
}

// loadNRICaution stores the RBI caution / ban list.
func (o *Ops) loadNRICaution(ctx context.Context, tenant, actor, date string, rows []row) (func() (int, int, error), []ingest.ParseError) {
	var errs []ingest.ParseError
	var items []NRICaution
	for _, r := range rows {
		st := strings.ToLower(r.get("Status"))
		if st != "" {
			st = strings.ToUpper(st[:1]) + st[1:]
		}
		if st != "Caution" && st != "Ban" {
			errs = append(errs, ingest.ParseError{Line: r.line, Field: "Status", Msg: "must be Caution or Ban"})
			continue
		}
		items = append(items, NRICaution{ISIN: strings.ToUpper(r.get("ISIN")), Symbol: strings.ToUpper(r.get("Symbol")), Status: st})
	}
	return func() (int, int, error) {
		return len(items), 0, docs.Upsert(ctx, o.Docs, tenant, cNRIList, "current", nriListDoc{Items: items, AsOf: date}, actor)
	}, errs
}
