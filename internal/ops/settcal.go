package ops

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
	"ledgerline/internal/xfile"
)

// ---- settlement calendar (NSE Clearing's settlement master) ----

const (
	cSettCal   = "settcal"
	cGSecPrice = "gsecprice"
)

// SettlementDay is one settlement of the clearing corporation's calendar.
type SettlementDay struct {
	ID         string `json:"id"` // segment|type|number
	Segment    string `json:"segment"`
	Type       string `json:"type"` // NORMAL1 (T+1 rolling), NORMAL0 (T+0), TFT1, ITP1, AUCTION1 …
	No         string `json:"no"`
	TradeDate  string `json:"trade_date"`
	FundsIn    string `json:"funds_pay_in"`
	FundsOut   string `json:"funds_pay_out"`
	SecIn      string `json:"securities_pay_in"`
	SecOut     string `json:"securities_pay_out"`
	ObligDate  string `json:"obligation"`
	LoadedFrom string `json:"loaded_from"`
}

// settlementTypeLetters are the one-letter settlement types the clearing
// corporation's member files use.
var settlementTypeLetters = map[string]string{"NORMAL1": "M", "NORMAL0": "Z", "TFT1": "W", "AUCTION1": "A", "ITP1": "I"}

func (o *Ops) loadSettlementMaster(ctx context.Context, tenant, actor, file string, rows []xfile.Row) (int, error) {
	n := 0
	for _, r := range rows {
		d := SettlementDay{Segment: strings.ToUpper(r.Get("Sgmt")), Type: strings.ToUpper(r.Get("SttlmTp")), No: strings.TrimSpace(r.Get("SctiesSttlmTxId")),
			TradeDate: dayOf(r.Get("OrgnlTradDtTm")), FundsIn: r.Get("FndsPayInDtAndTm"), FundsOut: r.Get("FndsPayOutDtAndTm"),
			SecIn: r.Get("SctiesPayInDtAndTm"), SecOut: r.Get("SctiesPayOutDtAndTm"), ObligDate: r.Get("OblgtnDtTm"), LoadedFrom: file}
		if d.Segment == "" || d.Type == "" || d.No == "" {
			continue
		}
		d.ID = d.Segment + "|" + d.Type + "|" + d.No
		if err := docs.Upsert(ctx, o.Docs, tenant, cSettCal, d.ID, d, actor); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func dayOf(dt string) string {
	if len(dt) >= 10 {
		return dt[:10]
	}
	return dt
}

// SettlementFor finds the settlement of a segment and type for a trade date.
func (o *Ops) SettlementFor(ctx context.Context, tenant, segment, typ, tradeDate string) (SettlementDay, bool) {
	ds, _ := docs.List[SettlementDay](ctx, o.Docs, tenant, cSettCal, strings.ToUpper(segment)+"|"+strings.ToUpper(typ)+"|")
	for _, d := range ds {
		if d.TradeDate == tradeDate {
			return d, true
		}
	}
	return SettlementDay{}, false
}

// SettlementCalendar lists the loaded settlements from a date on.
func (o *Ops) SettlementCalendar(ctx context.Context, tenant, from string) ([]SettlementDay, error) {
	ds, err := docs.List[SettlementDay](ctx, o.Docs, tenant, cSettCal, "")
	var out []SettlementDay
	for _, d := range ds {
		if d.TradeDate >= from {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TradeDate != out[j].TradeDate {
			return out[i].TradeDate < out[j].TradeDate
		}
		return out[i].ID < out[j].ID
	})
	return out, err
}

// ---- CCIL prices of G-secs and T-bills ----

// GSecPrices is one CCIL price file.
type GSecPrices struct {
	ID     string               `json:"id"` // price date
	Date   string               `json:"date"`
	File   string               `json:"file"`
	Prices map[string]GSecPrice `json:"prices"` // by ISIN
}

// GSecPrice is one security's CCIL valuation price.
type GSecPrice struct {
	Kind        string    `json:"kind"` // T-BILL, G-SEC, SDL … as CCIL heads the table
	Description string    `json:"description"`
	Coupon      string    `json:"coupon,omitempty"`
	Maturity    string    `json:"maturity,omitempty"`
	Price       money.Dec `json:"price"`
}

var ccilDate = regexp.MustCompile(`(?i)MTMPRICE(\d{2})(\d{2})(\d{4})`)

func (o *Ops) loadGSecPrices(ctx context.Context, tenant, actor, file string, rows []xfile.Row) (string, int, error) {
	m := ccilDate.FindStringSubmatch(file)
	if m == nil {
		return "", 0, invalid("the price date is not in the file name (MTMPRICEddmmyyyy)")
	}
	date := m[3] + "-" + m[2] + "-" + m[1]
	if !validDate(date) {
		return "", 0, invalid("the date in the file name is not a date")
	}
	p := GSecPrices{ID: date, Date: date, File: file, Prices: map[string]GSecPrice{}}
	for _, r := range rows {
		isin := strings.ToUpper(strings.TrimSpace(r.Get("isin")))
		px, err := money.ParseDec(strings.TrimSpace(r.Get("price")))
		if len(isin) != 12 || err != nil || px.Sign() <= 0 {
			continue
		}
		cp := strings.TrimSpace(r.Get("coupon"))
		if cp == "-" {
			cp = ""
		}
		p.Prices[isin] = GSecPrice{Kind: r.Record, Description: r.Get("description"), Coupon: cp, Maturity: r.Get("maturity"), Price: px}
	}
	if len(p.Prices) == 0 {
		return date, 0, invalid("no prices in the file")
	}
	return date, len(p.Prices), docs.Upsert(ctx, o.Docs, tenant, cGSecPrice, date, p, actor)
}

// gsecPricesOn returns the latest CCIL prices on or before date.
func (o *Ops) gsecPricesOn(ctx context.Context, tenant, date string) (GSecPrices, bool) {
	all, err := o.Docs.List(ctx, tenant, cGSecPrice, "")
	if err != nil {
		return GSecPrices{}, false
	}
	best := ""
	for _, d := range all {
		if d.ID <= date && d.ID > best {
			best = d.ID
		}
	}
	if best == "" {
		return GSecPrices{}, false
	}
	p, _, err := docs.Get[GSecPrices](ctx, o.Docs, tenant, cGSecPrice, best)
	return p, err == nil
}

// addGSecPrices lets holdings and collateral in G-secs and T-bills be
// valued at CCIL's price when the exchange market data has none.
func (o *Ops) addGSecPrices(ctx context.Context, tenant, date string, l *Lookup) {
	p, ok := o.gsecPricesOn(ctx, tenant, date)
	if !ok {
		return
	}
	for isin, g := range p.Prices {
		if _, have := l.byISIN[isin]; have {
			continue
		}
		l.byISIN[isin] = Instrument{Exchange: "CCIL", Segment: "GS", Symbol: strings.TrimSpace(g.Description), Type: "GSEC", ISIN: isin, LTP: g.Price, Close: g.Price}
	}
}

// ---- CUSPA and MTF delivery pay-out reporting ----
//
// NSE Clearing's consolidated circular (NCL/CMPT/73996 items 6.2.1 and
// 7.7(iii)-(iv); earlier NCL/CMPT/66862 and 67751 Part C): for each
// settlement, the member flags the clients' purchases that are not to be
// paid out free: unpaid (T, the trading member's CUSPA; C, the clearing
// member's) or MTF (M), so NSE Clearing pays them out to the client's
// demat account with the pledge. The flags are given per settlement
// number on NMASS, in every settlement type with deliveries (the normal
// rolling settlement and trade-for-trade), never for clients trading with
// UPI blocks (their shortfall is handled by the block) nor for the
// member's own (proprietary) code, and they are not carried forward: each
// settlement is reported on its own. The upload window closes at 10:00 on
// the settlement day (cuspaMTFCutoff); NCL/CMPT/70409 also opens one on
// the trade day's afternoon and evening.

// cuspaMTFCutoff is NSE Clearing's cut-off on the settlement day.
const cuspaMTFCutoff = "10:00"

// cuspaSettTypes are the settlement types the file is made for, in order.
var cuspaSettTypes = []string{"NORMAL1", "TFT1"}

// cuspaSettType is the settlement a purchase of the ISIN falls in: the
// trade-for-trade series (BE, BZ in the security master) settle in TFT1,
// the rest in the normal rolling settlement. (ITP trades, institutional
// only, are not expected for clients with unpaid purchases.)
func (o *Ops) cuspaSettType(ctx context.Context, tenant, isin string) (string, string) {
	if ref, _, err := docs.Get[SecRef](ctx, o.Docs, tenant, cSecRef, isin); err == nil {
		if se := strings.ToUpper(strings.TrimSpace(ref.Series)); sipT2TSeries[se] {
			return "TFT1", se
		}
	}
	return "NORMAL1", "EQ"
}

// cuspaMTFFile lists one settlement's purchases that are not to be paid
// out free to clients' demat accounts: unpaid purchases held in CUSPA and
// MTF purchases kept for the MTF pledge. vars["sett_type"] picks the
// settlement type (NORMAL1 or TFT1); without it the first type with
// anything to report is made.
func (o *Ops) cuspaMTFFile(ctx context.Context, tenant string, s *xfile.Spec, set Settings, date string, vars xfile.Vars) (string, []byte, error) {
	member := set.MemberCodes["NSE"]
	if member == "" {
		return "", nil, invalid("set the firm's NSE member code in Settings")
	}
	_, look, _ := o.Market(ctx, tenant, date)
	cs, _ := o.ClientMap(ctx, tenant)
	ub := o.upixBlocked(ctx, tenant)
	own := map[string]bool{"PRO": true, strings.ToUpper(strings.TrimSpace(set.FirmUCC)): true}
	type key struct{ typ, ucc, isin, flag string }
	qty := map[key]int64{}
	sym := map[key][2]string{}
	var order []key
	// T: kept in the trading member's CUSPA; M: kept for the MTF pledge.
	add := func(ucc, isin, symbol, flag string, q int64) {
		u := strings.ToUpper(ucc)
		if own[u] {
			return // the member's own code: never flagged
		}
		if c, ok := cs[ucc]; ok && upiBlock(c, ub).Sign() > 0 {
			return // UPI-block clients: not flagged (NCL/CMPT/73996 item 7.7)
		}
		typ, se := o.cuspaSettType(ctx, tenant, isin)
		if i, ok := look.ByISIN(isin); ok && i.Symbol != "" {
			symbol = i.Symbol
		}
		k := key{typ, ucc, isin, flag}
		if _, ok := qty[k]; !ok {
			order = append(order, k)
		}
		qty[k] += q
		sym[k] = [2]string{symbol, se}
	}
	cu, _ := docs.List[CUSPAItem](ctx, o.Docs, tenant, cCUSPA, "")
	for _, it := range cu {
		if it.Date == date && it.total() > 0 {
			add(it.UCC, it.ISIN, it.Symbol, "T", it.total())
		}
	}
	ms, _ := docs.List[MTFPosition](ctx, o.Docs, tenant, cMTFPos, "")
	for _, m := range ms {
		if m.BuyDate == date && m.Status == "Open" {
			add(m.UCC, m.ISIN, m.Symbol, "M", m.Qty)
		}
	}
	want := vars["sett_type"]
	if want == "" {
		for _, t := range cuspaSettTypes {
			for _, k := range order {
				if k.typ == t && want == "" {
					want = t
				}
			}
		}
	}
	var rows []xfile.Row
	for _, k := range order {
		if k.typ != want {
			continue
		}
		rows = append(rows, xfile.Row{Values: map[string]string{"MemberCode": member, "ClientCode": k.ucc, "Symbol": sym[k][0], "Series": sym[k][1], "Quantity": fmt.Sprint(qty[k]), "CTMFlag": k.flag, "ActionFlag": ""}})
	}
	if len(rows) == 0 {
		return "", nil, fmt.Errorf("%w: no unpaid (CUSPA) or MTF purchases on %s to report%s", ErrState, date, map[bool]string{true: " in " + want, false: ""}[want != ""])
	}
	st, ok := o.SettlementFor(ctx, tenant, "CM", want, date)
	if !ok {
		return "", nil, invalid("no NSE Clearing %s settlement is loaded for trades of %s: load the settlement master file first", want, date)
	}
	vars["stype"], vars["settno"] = settlementTypeLetters[st.Type], st.No
	content, err := s.Write(rows)
	if err != nil {
		return "", nil, err
	}
	name, err := s.FileName(vars)
	return name, content, err
}
