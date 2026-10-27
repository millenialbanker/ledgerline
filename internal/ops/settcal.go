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

// cuspaMTFFile lists the settlement's purchases that are not to be paid
// out to clients' demat accounts: unpaid purchases held in CUSPA and MTF
// purchases kept for the MTF pledge.
func (o *Ops) cuspaMTFFile(ctx context.Context, tenant string, s *xfile.Spec, set Settings, date string, vars xfile.Vars) (string, []byte, error) {
	member := set.MemberCodes["NSE"]
	if member == "" {
		return "", nil, invalid("set the firm's NSE member code in Settings")
	}
	_, look, _ := o.Market(ctx, tenant, date)
	series := func(isin, sym string) (string, string) {
		if i, ok := look.ByISIN(isin); ok && i.Symbol != "" {
			return i.Symbol, "EQ"
		}
		return sym, "EQ"
	}
	type key struct{ ucc, isin, flag string }
	qty := map[key]int64{}
	sym := map[key]string{}
	var order []key
	// T: kept in the trading member's CUSPA; M: kept for the MTF pledge.
	add := func(ucc, isin, symbol, flag string, q int64) {
		k := key{ucc, isin, flag}
		if _, ok := qty[k]; !ok {
			order = append(order, k)
		}
		qty[k] += q
		sym[k] = symbol
	}
	cu, _ := docs.List[CUSPAItem](ctx, o.Docs, tenant, cCUSPA, "")
	for _, it := range cu {
		if it.Date == date && it.Status == "Held in CUSPA" {
			add(it.UCC, it.ISIN, it.Symbol, "T", it.Qty)
		}
	}
	ms, _ := docs.List[MTFPosition](ctx, o.Docs, tenant, cMTFPos, "")
	for _, m := range ms {
		if m.BuyDate == date && m.Status == "Open" {
			add(m.UCC, m.ISIN, m.Symbol, "M", m.Qty)
		}
	}
	if len(order) == 0 {
		return "", nil, fmt.Errorf("%w: no unpaid (CUSPA) or MTF purchases on %s to report", ErrState, date)
	}
	st, ok := o.SettlementFor(ctx, tenant, "CM", "NORMAL1", date)
	if !ok {
		return "", nil, invalid("no NSE Clearing settlement is loaded for trades of %s: load the settlement master file first", date)
	}
	vars["stype"], vars["settno"] = settlementTypeLetters[st.Type], st.No
	var rows []xfile.Row
	for _, k := range order {
		sy, se := series(k.isin, sym[k])
		rows = append(rows, xfile.Row{Values: map[string]string{"MemberCode": member, "ClientCode": k.ucc, "Symbol": sy, "Series": se, "Quantity": fmt.Sprint(qty[k]), "CTMFlag": k.flag, "ActionFlag": ""}})
	}
	content, err := s.Write(rows)
	if err != nil {
		return "", nil, err
	}
	name, err := s.FileName(vars)
	return name, content, err
}
