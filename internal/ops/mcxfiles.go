package ops

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/money"
	"ledgerline/internal/xfile"
)

// ---- MCX and MCXCCL files ----
//
// The layouts are MCXCCL's File Formats (Version 1) and MCX circular
// MCX/TECH/394/2023 (File Formats 1.2), and MCXCCL's UDiFF formats
// (config/formats/mcx-*.json, mcxccl-*.json). A trading member, whether it
// clears itself or through another clearing member, receives the
// trading-member files below; a clearing member receives the CM versions
// of NOBL, MCX_MRG and the peak margin file, which share the names and are
// told apart by their lines. What each feeds:
//
//   - MCX_ProductMaster.csv: the commodity contract master (lot, tick,
//     expiry, delivery unit and mode) and the day's MCX contracts in the
//     market data, whose lots commodity billing uses;
//   - Bhavcopy_MCX_CO_…: closing and settlement prices of the MCX
//     contracts (market data: valuation, MTM, margins);
//   - MCX_NOBL / MCX_PNOBL: the day's commodity obligation per client,
//     reconciled with Ledgerline's own MCX obligation (Clearing data);
//   - MCX_MRG, Margin_MCXCCL_… (UDiFF) and MCX_PeakMargin…: the margin
//     MCXCCL charged and its peak snapshots; live RMS takes MCXCCL's figure
//     when higher than Ledgerline's, margin reporting shows the peak, and
//     Clearing data compares the margin with Ledgerline's;
//   - MCX_NCMR and Penalty_MCXCCL_…: the shortfall and penalty MCXCCL
//     levied: margin reporting shows it as the client's penalty, and only
//     the pass-through rules (penaltypass.go) can put it on a client;
//   - MCX_WebAllocationDeallocation…: each client's commodity allocation
//     (Clearing allocation levels, Clearing data); MCX_CD (collateral) and
//     MCX_CD_…_UCC (each client's collateral) are kept and shown;
//   - CTT_ and Stamp_MCXCCL_… (UDiFF): the CTT and stamp duty
//     reconciliations, as NSE Clearing's STT and stamp files;
//   - Position_MCXCCL_…: the commodity positions carried into the next day;
//   - MCX_…ORD: the MCX order log, for order-to-trade ratios;
//   - debit/credit notes, EOD and intraday short allocation, the CP master
//     and the daily index file are kept and listed.

const cMCXContracts = "mcxcontracts" // "master": the latest product master

// mcxKinds are the clearing-data kinds the MCX files are kept as.
var mcxKinds = map[string]string{
	"mcx-obligation-tm": "mcxobl", "mcx-obligation-cm": "mcxobl", "mcx-margin-tm": "mcxmrg", "mcx-margin-cm": "mcxmrg",
	"mcx-peak-margin-tm": "mcxpeak", "mcx-peak-margin-cm": "mcxpeakcm", "mcx-ncmr": "mcxncmr", "mcxccl-margin": "mcxudmargin",
	"mcxccl-penalty": "mcxpenalty", "mcx-collateral": "mcxcoll", "mcx-collateral-client": "mcxcollcli", "mcx-web-allocation": "mcxweballoc",
	"mcx-eod-short-allocation": "mcxeodsar", "mcx-intraday-short-allocation": "mcxintrasar", "mcx-dc-note": "mcxdcnote",
	"mcx-cp-master": "mcxcp", "mcx-daily-index": "mcxindex", "mcx-orders": "mcxorders", "mcxccl-stamp": "stamp",
}

var mcxLoaded = map[string]string{
	"mcxobl":      "Loaded as the commodity settlement obligation; reconciled with Ledgerline's MCX obligation on Clearing data",
	"mcxpobl":     "Loaded as the provisional commodity obligation (Clearing data)",
	"mcxmrg":      "Loaded as the margin MCXCCL charged each client: used in live RMS and checked on Clearing data",
	"mcxudmargin": "Loaded as the margin MCXCCL charged each client (UDiFF): used in live RMS and checked on Clearing data",
	"mcxpeak":     "Loaded as an MCXCCL peak margin snapshot: live RMS and margin reporting",
	"mcxpeakcm":   "Loaded as an MCXCCL peak margin snapshot of the trading members",
	"mcxncmr":     "Loaded as the shortfall and penalty MCXCCL levied: margin reporting; passed to a client only through the pass-through rules",
	"mcxpenalty":  "Loaded as the penalty MCXCCL levied: margin reporting; passed to a client only through the pass-through rules",
	"mcxcoll":     "Loaded as the firm's collateral with MCXCCL (Clearing data)", "mcxcollcli": "Loaded as each client's collateral with MCXCCL (Clearing data)",
	"mcxweballoc": "Loaded as each client's commodity allocation with MCXCCL (Clearing allocation and Clearing data)",
	"mcxeodsar":   "Loaded as MCXCCL's end-of-day short allocations (Clearing data)", "mcxintrasar": "Loaded as MCXCCL's intraday short allocations (Clearing data)",
	"mcxdcnote": "Loaded as MCXCCL's debit and credit notes (Clearing data)", "mcxcp": "Loaded as MCX's custodian participant master",
	"mcxindex": "Loaded as MCX's index values of the day", "stamp": "Loaded and reconciled with the stamp duty Ledgerline charged on MCX trades (Clearing data)",
}

var reBatch = regexp.MustCompile(`_(\d+)\.csv$`)

// loadMCXFile handles the MCX and MCXCCL files; ok is false for others.
func (o *Ops) loadMCXFile(ctx context.Context, tenant, actor string, s *xfile.Spec, f xfile.File, rows []xfile.Row, r *XFileResult) bool {
	fail := func(err error) { r.Loaded = "Not loaded: " + strings.TrimPrefix(err.Error(), "invalid: ") }
	if s.ID == "mcx-product-master" {
		n, date, err := o.loadMCXProducts(ctx, tenant, actor, f.Name, rows)
		if err != nil {
			fail(err)
			return true
		}
		r.Loaded, r.Added = "Loaded as the MCX contract master and the MCX contracts of "+date+" (lots, expiries, ticks, delivery)", n
		return true
	}
	kind, ok := mcxKinds[s.ID]
	if !ok {
		return false
	}
	if kind == "mcxobl" && strings.HasPrefix(strings.ToUpper(f.Name), "MCX_PNOBL") {
		kind = "mcxpobl"
	}
	date := ymdFromName(f.Name)
	if date == "" && len(rows) > 0 {
		date = firstOf(anyDate(rows[0].Get("Date")), anyDate(rows[0].Get("Trade Date")))
	}
	if date == "" {
		date = o.now().In(ist).Format("2006-01-02")
	}
	seg := "COM"
	if kind == "mcxpeak" || kind == "mcxpeakcm" {
		if m := reBatch.FindStringSubmatch(f.Name); m != nil {
			n, _ := strconv.Atoi(m[1])
			seg += fmt.Sprintf("-%02d", n) // the snapshot batch, so each is kept
		}
	}
	if kind == "stamp" {
		rows = udiffFinest(rows)
	}
	n, err := o.putCCData(ctx, tenant, actor, kind, date, seg, f.Name, rows, nil)
	if err != nil {
		fail(err)
		return true
	}
	r.Loaded, r.Added = mcxLoaded[kind], n
	switch kind {
	case "mcxweballoc":
		o.levelsFromMCXWebAlloc(ctx, tenant, actor, date, rows)
	case "mcxorders":
		day := o.mcxOTR(ctx, tenant, actor, date, f.Name, rows)
		r.Loaded = fmt.Sprintf("Loaded as the MCX order log of %s: order-to-trade ratio for %d member, client and terminal lines (one line per order: changes are not separate events in this file)", date, len(day.Lines))
	case "mcxncmr", "mcxpenalty":
		o.queueLeviedPenalties(ctx, tenant, date)
	}
	return true
}

// udiffFinest keeps a UDiFF report's most detailed level (RptHdr 40, per
// client and contract, under the client, TM and CM totals), so totals are
// not counted twice; files without levels are kept whole.
func udiffFinest(rows []xfile.Row) []xfile.Row {
	best := ""
	for _, r := range rows {
		if h := strings.TrimSpace(r.Get("RptHdr")); h > best {
			best = h
		}
	}
	if best == "" {
		return rows
	}
	var out []xfile.Row
	for _, r := range rows {
		if strings.TrimSpace(r.Get("RptHdr")) == best {
			out = append(out, r)
		}
	}
	return out
}

// ---- the contract master ----

// MCXContract is one MCX contract from the product master.
type MCXContract struct {
	ID          string `json:"id"` // MCX's unique identifier
	Instrument  string `json:"instrument"`
	Symbol      string `json:"symbol"`
	Underlying  string `json:"underlying,omitempty"`
	Type        string `json:"type"` // FUT, CE, PE
	Expiry      string `json:"expiry"`
	Strike      string `json:"strike,omitempty"`
	Description string `json:"description,omitempty"`
	// Lot is the tradable lot in quotation units: what the price is
	// multiplied by for one lot (and the lot commodity billing counts).
	Lot int64 `json:"lot"`
	// QuoteQty and QuoteUnit are what one price quote is for (10 GMS for a
	// price per 10 grams); Lot × QuoteQty QuoteUnit is the physical lot.
	QuoteQty       int64     `json:"quotation_qty"`
	QuoteUnit      string    `json:"quotation_unit,omitempty"`
	Tick           money.Dec `json:"tick"`
	DeliveryUnit   string    `json:"delivery_unit,omitempty"`
	DeliveryWeight money.Dec `json:"delivery_weight"`
	Settlement     string    `json:"settlement"`
	PriceNum       money.Dec `json:"price_numerator"`
	PriceDen       money.Dec `json:"price_denominator"`
	GenNum         money.Dec `json:"general_numerator"`
	GenDen         money.Dec `json:"general_denominator"`
	Currency       string    `json:"currency,omitempty"`
	Month          string    `json:"product_month,omitempty"`
}

// Key matches the market data's and trades' instrument key.
func (c MCXContract) Key() string { return c.Symbol + "|" + c.Type + "|" + c.Expiry + "|" + c.Strike }

// MCXContractMaster is the latest product master.
type MCXContractMaster struct {
	File      string        `json:"file"`
	Date      string        `json:"date"`
	LoadedAt  string        `json:"loaded_at"`
	Contracts []MCXContract `json:"contracts"`
}

// mcxSettlement names a product master delivery mode.
var mcxSettlement = map[string]string{"0": "Delivery (both options)", "1": "Delivery (seller's option)", "2": "Compulsory delivery", "-1": "Cash settled"}

// mcxType is Ledgerline's instrument type for an MCX instrument name.
func mcxType(instrument, optType string) string {
	in := strings.ToUpper(strings.TrimSpace(instrument))
	switch {
	case strings.HasPrefix(in, "FUT"), in == "COF", in == "IDF":
		return "FUT"
	case strings.HasPrefix(in, "OPT"), in == "FUO", in == "COO", in == "IDO":
		if t := strings.ToUpper(strings.TrimSpace(optType)); t == "CE" || t == "PE" {
			return t
		}
	}
	return ""
}

func strikeKey(v string) string {
	if k, err := money.ParseDec(strings.TrimSpace(v)); err == nil && !k.IsZero() {
		return k.String()
	}
	return ""
}

// loadMCXProducts keeps the product master and puts its live contracts
// into the day's market data (lots, names), keeping the prices there. One
// document each, so a 17,000-line master loads in one pass.
func (o *Ops) loadMCXProducts(ctx context.Context, tenant, actor, name string, rows []xfile.Row) (int, string, error) {
	date := o.now().In(ist).Format("2006-01-02")
	dec := func(r xfile.Row, k string) money.Dec { return decOr0(r.Get(k)) }
	m := MCXContractMaster{File: name, Date: date, LoadedAt: o.now().Format(time.RFC3339), Contracts: []MCXContract{}}
	for _, r := range rows {
		if strings.EqualFold(strings.TrimSpace(r.Get("Record Deleted")), "Y") {
			continue
		}
		if u := strings.TrimSpace(r.Get("Underlying Unique Identifier")); u == "" || u == "0" {
			continue // an underlying asset, not a contract
		}
		typ := mcxType(r.Get("Instrument Name"), r.Get("Options Type"))
		exp := anyDate(r.Get("Expiry Date"))
		if typ == "" || exp == "" || exp < date {
			continue
		}
		c := MCXContract{ID: strings.TrimSpace(r.Get("Unique Identifier")), Instrument: strings.ToUpper(strings.TrimSpace(r.Get("Instrument Name"))),
			Symbol: strings.ToUpper(strings.TrimSpace(r.Get("Symbol"))), Underlying: strings.TrimSpace(r.Get("Underlying Asset")), Type: typ, Expiry: exp,
			Description: strings.TrimSpace(r.Get("Product Description")), QuoteUnit: strings.TrimSpace(r.Get("Quotation Unit")), Tick: dec(r, "Price Tick"),
			DeliveryUnit: strings.TrimSpace(r.Get("Delivery Unit")), DeliveryWeight: dec(r, "Delivery Weight"), Settlement: mcxSettlement[strings.TrimSpace(r.Get("Delivery Mode"))],
			PriceNum: dec(r, "Price Numerator"), PriceDen: dec(r, "Price Denominator"), GenNum: dec(r, "General Numerator"), GenDen: dec(r, "General Denominator"),
			Currency: strings.TrimSpace(r.Get("Currency Code")), Month: strings.TrimSpace(r.Get("Product Month"))}
		if typ != "FUT" {
			c.Strike = strikeKey(r.Get("Strike Price"))
			if c.Strike == "" {
				continue
			}
		}
		c.Lot, _ = strconv.ParseInt(strings.TrimSpace(r.Get("Tradable Lot")), 10, 64)
		c.QuoteQty, _ = strconv.ParseInt(strings.TrimSpace(r.Get("Quotation Quantity")), 10, 64)
		if c.Symbol == "" || c.Lot <= 0 {
			continue
		}
		m.Contracts = append(m.Contracts, c)
	}
	if len(m.Contracts) == 0 {
		return 0, date, fmt.Errorf("no live contract in the product master")
	}
	sort.Slice(m.Contracts, func(i, j int) bool { return m.Contracts[i].Key() < m.Contracts[j].Key() })
	if err := docs.Upsert(ctx, o.Docs, tenant, cMCXContracts, "master", m, actor); err != nil {
		return 0, date, err
	}
	mk, _, err := docs.Get[Market](ctx, o.Docs, tenant, cMarket, date)
	if errors.Is(err, docs.ErrNotFound) {
		prev, _, _ := o.Market(ctx, tenant, date)
		mk = Market{Instruments: append([]Instrument(nil), prev.Instruments...)}
	} else if err != nil {
		return 0, date, err
	}
	mk.Date = date
	at := map[string]int{}
	for i, x := range mk.Instruments {
		if x.Exchange == "MCX" && x.Segment == "COM" {
			at[x.Key()] = i
		}
	}
	for _, c := range m.Contracts {
		if i, ok := at[c.Key()]; ok {
			x := &mk.Instruments[i]
			x.Lot, x.Company = c.Lot, firstOf(c.Description, x.Company)
			continue
		}
		at[c.Key()] = len(mk.Instruments)
		mk.Instruments = append(mk.Instruments, Instrument{Exchange: "MCX", Segment: "COM", Symbol: c.Symbol, Type: c.Type, Expiry: c.Expiry, Strike: c.Strike,
			Company: c.Description, Lot: c.Lot, Index: strings.HasSuffix(c.Instrument, "IDX")})
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cMarket, date, mk, actor); err != nil {
		return 0, date, err
	}
	return len(m.Contracts), date, nil
}

// MCXContracts returns the latest product master's contracts, optionally
// of one symbol.
func (o *Ops) MCXContracts(ctx context.Context, tenant, symbol string) (MCXContractMaster, error) {
	m, _, err := docs.Get[MCXContractMaster](ctx, o.Docs, tenant, cMCXContracts, "master")
	if errors.Is(err, docs.ErrNotFound) {
		return MCXContractMaster{Contracts: []MCXContract{}}, nil
	}
	if err != nil {
		return m, err
	}
	if symbol = strings.ToUpper(strings.TrimSpace(symbol)); symbol != "" {
		var cs []MCXContract
		for _, c := range m.Contracts {
			if c.Symbol == symbol {
				cs = append(cs, c)
			}
		}
		m.Contracts = cs
	}
	if m.Contracts == nil {
		m.Contracts = []MCXContract{}
	}
	return m, nil
}

// ---- allocation, order log, margins and penalties ----

// levelsFromMCXWebAlloc takes each client's end-of-day allocation from
// MCXCCL's web allocation report as the commodity segment's level.
func (o *Ops) levelsFromMCXWebAlloc(ctx context.Context, tenant, actor, date string, rows []xfile.Row) {
	for _, r := range rows {
		ucc := strings.ToUpper(strings.TrimSpace(r.Get("End Client/CP Code")))
		if ucc == "" || strings.TrimSpace(r.Get("TM ID")) == "" {
			continue // the trading member's own line, or a CP's
		}
		_ = o.setLevel(ctx, tenant, actor, AllocLevel{Segment: "COM", UCC: ucc, Amount: decOr0(r.Get("Allocation at EoD")), AsOf: date + "T23:59:00+05:30", Source: "MCXCCL web allocation " + date}, false)
	}
}

// mcxOTR works out the day's MCX order-to-trade ratios from the order log.
func (o *Ops) mcxOTR(ctx context.Context, tenant, actor, date, file string, rows []xfile.Row) OTRDay {
	var orders []otrOrder
	for _, r := range rows {
		orders = append(orders, otrOrder{ucc: strings.ToUpper(strings.TrimSpace(r.Get("Account ID"))), terminal: strings.TrimSpace(r.Get("User Id"))})
	}
	var trades []otrTrade
	ts, _ := o.App.Trades.List(ctx, tenant, ingest.FromExchange, date)
	for _, t := range ts {
		if t.Exchange == "MCX" {
			trades = append(trades, otrTrade{t.UCC, t.Terminal})
		}
	}
	return o.otrCheck(ctx, tenant, actor, "MCX", date, file, orders, trades)
}

// mcxMRGTotal is a client's margin in an MCX_MRG summary (record 30): the
// margin levied after benefits, with the lean period margin, net buy
// premium, ELM, concentration and devolvement margins, which the file
// gives apart from it.
func mcxMRGTotal(r map[string]string) money.Dec {
	t := decOr0(r["Actual Margin Levied"])
	for _, k := range []string{"Additional Lean period margin", "Net Buy Premium", "Extreme Loss Margin", "Concentration Margin", "Devolvement Margin"} {
		t = t.Add(decOr0(r[k]))
	}
	return t
}

// mcxUDiFFMargins are each client's margins in the UDiFF margin file: the
// portfolio-level rows where there are any, else the contract rows.
func mcxUDiFFMargins(rows []map[string]string) map[string]money.Dec {
	port := map[string]bool{}
	for _, r := range rows {
		if strings.EqualFold(r["Lvl"], "P") {
			port[strings.ToUpper(r["ClntId"])] = true
		}
	}
	out := map[string]money.Dec{}
	for _, r := range rows {
		u := strings.ToUpper(r["ClntId"])
		if u == "" || port[u] != strings.EqualFold(r["Lvl"], "P") {
			continue
		}
		var t money.Dec
		for _, k := range []string{"SPANMrgn", "LossMrgn", "PrmAmt", "DlvrySttlmtMrgn", "SpclMrgn", "TndrMrgn", "AddtlMrgn", "CncntrtnMrgn", "DvlmntMrgn"} {
			t = t.Add(decOr0(r[k]))
		}
		out[u] = out[u].Add(t)
	}
	return out
}

// mcxChargedMargins are the margins MCXCCL charged each client on a date
// at end of day (MCX_MRG, else the UDiFF margin file), and the file.
func (o *Ops) mcxChargedMargins(ctx context.Context, tenant, date string) (map[string]money.Dec, string) {
	out := map[string]money.Dec{}
	for _, d := range o.ccData(ctx, tenant, "mcxmrg", date, "") {
		for _, r := range d.Rows {
			if r["_rec"] == "30" && r["Account Type"] != "P" && r["Account ID"] != "" {
				u := strings.ToUpper(r["Account ID"])
				out[u] = out[u].Add(mcxMRGTotal(r))
			}
		}
		if len(out) > 0 {
			return out, d.File
		}
	}
	for _, d := range o.ccData(ctx, tenant, "mcxudmargin", date, "") {
		var rows []map[string]string
		for _, r := range d.Rows {
			if r["ClntTp"] != "P" {
				rows = append(rows, r)
			}
		}
		for u, v := range mcxUDiFFMargins(rows) {
			out[u] = out[u].Add(v)
		}
		return out, d.File
	}
	return out, ""
}

// mcxPeak is a client's highest upfront margin across MCXCCL's peak
// snapshots of a day.
type mcxPeak struct {
	Peak, Latest money.Dec
	At           string // snapshot time of the peak
	LatestAt     string
	Short        money.Dec // highest short allocation
}

func (o *Ops) mcxPeaks(ctx context.Context, tenant, date string) map[string]*mcxPeak {
	out := map[string]*mcxPeak{}
	ds := o.ccData(ctx, tenant, "mcxpeak", date, "")
	sort.Slice(ds, func(i, j int) bool { return ds[i].Segment < ds[j].Segment })
	for _, d := range ds {
		for _, r := range d.Rows {
			u := strings.ToUpper(r["Client ID"])
			if u == "" || u == "*OWN*" {
				continue
			}
			p := out[u]
			if p == nil {
				p = &mcxPeak{}
				out[u] = p
			}
			v := decOr0(r["Total Upfront Margin"])
			if v.Cmp(p.Peak) > 0 {
				p.Peak, p.At = v, r["Snapshot Interval"]
			}
			p.Latest, p.LatestAt = v, r["Snapshot Interval"]
			p.Short = money.Max(p.Short, decOr0(r["Short allocation"]))
		}
	}
	return out
}

// mcxClientMargins is MCXCCL's margin of each client for live RMS: the
// latest peak snapshot of the day, or the end-of-day margin charged when
// higher, and when the figure was true.
func (o *Ops) mcxClientMargins(ctx context.Context, tenant, date string) (map[string]money.Dec, string) {
	out, file := o.mcxChargedMargins(ctx, tenant, date)
	at := ""
	if file != "" {
		at = date + "T23:59:00+05:30"
	}
	latest := ""
	for u, p := range o.mcxPeaks(ctx, tenant, date) {
		out[u] = money.Max(out[u], p.Latest)
		if p.LatestAt > latest {
			latest = p.LatestAt
		}
	}
	if latest != "" && at == "" {
		at = date + "T" + latest + "+05:30"
	}
	return out, at
}

// laterTime is the later of two RFC 3339 times ("" is earliest).
func laterTime(a, b string) string {
	ta, ea := time.Parse(time.RFC3339, a)
	tb, eb := time.Parse(time.RFC3339, b)
	switch {
	case eb != nil:
		return a
	case ea != nil || tb.After(ta):
		return b
	}
	return a
}

// mcxLevy is the shortfall and penalty MCXCCL levied on a client.
type mcxLevy struct {
	Short, Pct, Penalty money.Dec
	File                string
}

// mcxLevied are the penalties MCXCCL levied for a date: from the NCMR
// file, else the UDiFF penalty file.
func (o *Ops) mcxLevied(ctx context.Context, tenant, date string) map[string]mcxLevy {
	out := map[string]mcxLevy{}
	for _, d := range o.ccData(ctx, tenant, "mcxncmr", date, "") {
		for _, r := range d.Rows {
			u := strings.ToUpper(r["Client Code"])
			if u == "" {
				continue
			}
			l := out[u]
			l.Short, l.Penalty, l.File = l.Short.Add(decOr0(r["Shortfall on which penalty has been levied"])), l.Penalty.Add(decOr0(r["Penalty Amount"])), d.File
			l.Pct = money.Max(l.Pct, decOr0(r["Applicable Penalty %"]))
			out[u] = l
		}
	}
	if len(out) > 0 {
		return out
	}
	for _, d := range o.ccData(ctx, tenant, "mcxpenalty", date, "") {
		for _, r := range d.Rows {
			u := strings.ToUpper(r["ClntId"])
			if u == "" {
				continue
			}
			l := out[u]
			l.Short, l.Penalty, l.File = l.Short.Add(decOr0(r["DfcitAmt"])), l.Penalty.Add(decOr0(r["PnltyAmt"])), d.File
			if l.Short.Sign() > 0 {
				l.Pct, _ = l.Penalty.MulInt(100).Quo(l.Short)
				l.Pct = l.Pct.Round(money.UnitPaisa).Dec()
			}
			out[u] = l
		}
	}
	return out
}

// queueLeviedPenalties prepares, as PreparePenalties does, a decision for
// each penalty MCXCCL levied when the firm passes penalties on: nothing
// is debited; a person gives the reason and evidence and another approves.
func (o *Ops) queueLeviedPenalties(ctx context.Context, tenant, date string) {
	set, err := o.Settings(ctx, tenant)
	if err != nil || !set.PenaltyPassThrough || date < PenaltyPassFrom {
		return
	}
	r := &runCtx{o: o, ctx: ctx, tenant: tenant, date: date, process: wPenaltyPass, keys: map[string]bool{}}
	lev := o.mcxLevied(ctx, tenant, date)
	for _, u := range sortedKeys(lev) {
		l := lev[u]
		if l.Penalty.Round(money.UnitPaisa) <= 0 {
			continue
		}
		if _, ok := o.penaltyPass(ctx, tenant, date, u); ok {
			continue
		}
		r.queue(penaltyPassID(date, u), fmt.Sprintf("Margin penalty ₹%s of %s: the firm's, unless the client caused it", formatINR(l.Penalty), u),
			fmt.Sprintf("MCXCCL levied ₹%s on a shortfall of ₹%s on %s (%s%%, %s). It may be passed to the client only for a dishonoured cheque or a hedge break disclosed in the risk policy: give the reason and attach the evidence on RMS → Margin reporting, or dismiss to bear it.", formatINR(l.Penalty), formatINR(l.Short), fmtDay(date), l.Pct.String(), l.File),
			u, RoleCompliance, "rms", "", nil)
	}
}

// ---- the Clearing data sections ----

// mcxSections reconcile the day's MCX and MCXCCL files with Ledgerline.
func (o *Ops) mcxSections(ctx context.Context, tenant, date string, cs map[string]Client, ab map[string]AllocationRow) []CCSection {
	near := func(a, b money.Dec) bool { return a.Sub(b).Abs().Cmp(money.MustDec("1")) <= 0 }
	var out []CCSection

	// Obligation: MCXCCL's per client against Ledgerline's MCX obligation.
	ob := CCSection{ID: "mcx_obligation", Title: "Commodity obligation (MCXCCL)", About: "MCXCCL's funds obligation per client (MTM, premium, exercise and assignment, from MCX_NOBL) against Ledgerline's MCX obligation of the day from the bills.",
		Columns: []string{"Client", "Name", "MTM (MCXCCL)", "Premium (MCXCCL)", "Exercise / assignment (MCXCCL)", "MCXCCL", "Ledgerline", "Difference"}}
	if ds := o.ccData(ctx, tenant, "mcxobl", date, ""); len(ds) > 0 {
		type amt struct{ mtm, prem, ex money.Dec }
		cc := map[string]*amt{}
		for _, d := range ds {
			ob.Loaded, ob.File = true, d.File
			for _, r := range d.Rows {
				rec, u := r["_rec"], strings.ToUpper(r["Account ID"])
				if (rec != "20" && rec != "30") || u == "" || r["Account Type"] == "P" {
					continue
				}
				a := cc[u]
				if a == nil {
					a = &amt{}
					cc[u] = a
				}
				a.mtm = a.mtm.Add(decOr0(firstOf(r["MTM Profit/Loss"], r["MTM"])))
				a.prem = a.prem.Add(decOr0(firstOf(r["Premium Pay-In/PayOut"], r["Premium Pay-In/Pay-Out"])))
				a.ex = a.ex.Add(decOr0(r["Exercise/Assignment Value"]))
			}
		}
		mine := map[string]money.Dec{}
		if st, err := o.Obligations(ctx, tenant, date); err == nil {
			for _, m := range st.MTM {
				if m.Segment == "MCX-COM" {
					mine[m.UCC] = mine[m.UCC].Add(m.Amount)
				}
			}
		}
		keys := map[string]bool{}
		for u := range cc {
			keys[u] = true
		}
		for u := range mine {
			keys[u] = true
		}
		for _, u := range sortedKeys(keys) {
			a := cc[u]
			if a == nil {
				a = &amt{}
			}
			t := a.mtm.Add(a.prem).Add(a.ex)
			if !near(t, mine[u]) {
				ob.Diffs++
			}
			ob.Rows = append(ob.Rows, []string{u, cs[u].Name, a.mtm.Fixed(2), a.prem.Fixed(2), a.ex.Fixed(2), t.Fixed(2), mine[u].Fixed(2), t.Sub(mine[u]).Fixed(2)})
		}
	}
	out = append(out, ob)

	// Margin charged by MCXCCL against Ledgerline's commodity margin.
	mg := CCSection{ID: "mcx_margin", Title: "Commodity margin charged (MCXCCL)", About: "The margin MCXCCL charged each client at end of day (MCX_MRG, or the UDiFF margin file) and its highest peak snapshot, against the commodity margin Ledgerline computed.",
		Columns: []string{"Client", "Name", "MCXCCL end of day", "MCXCCL peak", "Peak at", "Ledgerline", "Difference"}}
	charged, file := o.mcxChargedMargins(ctx, tenant, date)
	peaks := o.mcxPeaks(ctx, tenant, date)
	if file != "" || len(peaks) > 0 {
		mg.Loaded, mg.File = true, file
		keys := map[string]bool{}
		for u := range charged {
			keys[u] = true
		}
		for u := range peaks {
			keys[u] = true
		}
		for _, u := range sortedKeys(keys) {
			p := peaks[u]
			if p == nil {
				p = &mcxPeak{}
			}
			l := ab["COM|"+u].Required
			if _, ok := charged[u]; ok && !near(charged[u], l) {
				mg.Diffs++
			}
			mg.Rows = append(mg.Rows, []string{u, cs[u].Name, charged[u].Fixed(2), p.Peak.Fixed(2), p.At, l.Fixed(2), charged[u].Sub(l).Fixed(2)})
		}
	}
	out = append(out, mg)

	// Shortfall and penalty levied.
	pn := CCSection{ID: "mcx_penalty", Title: "Commodity margin shortfall and penalty (MCXCCL)", About: "The shortfall and penalty MCXCCL levied per client (MCX_NCMR, or the UDiFF penalty file). The penalty is the firm's: it reaches a client only through a reasoned, approved pass-through on Margin reporting.",
		Columns: []string{"Client", "Name", "Shortfall", "Penalty %", "Penalty", "Pass-through"}}
	lev := o.mcxLevied(ctx, tenant, date)
	for _, u := range sortedKeys(lev) {
		l := lev[u]
		pn.Loaded, pn.File = true, l.File
		pass := "Borne by the firm"
		if p, ok := o.penaltyPass(ctx, tenant, date, u); ok {
			pass = p.Status
		}
		if l.Penalty.Sign() > 0 {
			pn.Diffs++
		}
		pn.Rows = append(pn.Rows, []string{u, cs[u].Name, l.Short.Fixed(2), l.Pct.String(), l.Penalty.Fixed(2), pass})
	}
	out = append(out, pn)

	// Collateral allocation: MCXCCL's web allocation report and the client
	// collateral file against Ledgerline's commodity allocation.
	co := CCSection{ID: "mcx_collateral", Title: "Commodity collateral allocation (MCXCCL)", About: "Each client's allocation at end of day and own collateral (web allocation report, MCX_CD_…_UCC) against Ledgerline's allocation for commodities.",
		Columns: []string{"Client", "Name", "Allocated (MCXCCL)", "Own collateral (MCXCCL)", "Total (MCXCCL)", "Ledgerline", "Difference"}}
	type col struct{ alloc, own money.Dec }
	cc := map[string]*col{}
	for _, d := range o.ccData(ctx, tenant, "mcxweballoc", date, "") {
		co.Loaded, co.File = true, d.File
		for _, r := range d.Rows {
			u := strings.ToUpper(r["End Client/CP Code"])
			if u == "" || r["TM ID"] == "" {
				continue
			}
			c := &col{alloc: decOr0(r["Allocation at EoD"]), own: decOr0(r["OWN Collateral at EoD"]).Add(decOr0(r["OWN Others Collateral at EOD"]))}
			cc[u] = c
		}
	}
	for _, d := range o.ccData(ctx, tenant, "mcxcollcli", date, "") {
		co.Loaded, co.File = true, strings.TrimPrefix(co.File+", "+d.File, ", ")
		for _, r := range d.Rows {
			u := strings.ToUpper(r["End Client/CP Code"])
			if r["_rec"] != "10" || u == "" {
				continue
			}
			if cc[u] == nil {
				cc[u] = &col{own: decOr0(r["Cash"]).Add(decOr0(r["Cash Collateral"])).Add(decOr0(r["Other Collateral"]))}
			}
		}
	}
	for _, u := range sortedKeys(cc) {
		c := cc[u]
		a := ab["COM|"+u]
		l := a.Cash.Add(a.NonCash)
		t := c.alloc.Add(c.own)
		if !near(t, l) {
			co.Diffs++
		}
		co.Rows = append(co.Rows, []string{u, cs[u].Name, c.alloc.Fixed(2), c.own.Fixed(2), t.Fixed(2), l.Fixed(2), t.Sub(l).Fixed(2)})
	}
	out = append(out, co)

	// Short allocations (EOD and intraday) and debit/credit notes: listed.
	sa := CCSection{ID: "mcx_short_allocation", Title: "Commodity short allocation (MCXCCL)", About: "Clients whose allocation fell short of the margin at end of day (EODSAR) or at a peak snapshot (INTRASAR).",
		Columns: []string{"File", "Client", "Name", "Short allocation"}}
	for _, k := range []struct{ kind, label, col string }{{"mcxeodsar", "End of day", "EOD Short Allocation"}, {"mcxintrasar", "Intraday", "Intra Short Allocation"}} {
		for _, d := range o.ccData(ctx, tenant, k.kind, date, "") {
			sa.Loaded, sa.File = true, strings.TrimPrefix(sa.File+", "+d.File, ", ")
			for _, r := range d.Rows {
				u := strings.ToUpper(r["Client Code"])
				v := decOr0(r[k.col])
				if u == "" || v.Sign() <= 0 {
					continue
				}
				sa.Diffs++
				sa.Rows = append(sa.Rows, []string{k.label, u, cs[u].Name, v.Fixed(2)})
			}
		}
	}
	out = append(out, sa)
	dn := CCSection{ID: "mcx_dc_notes", Title: "MCXCCL debit and credit notes", About: "Charges MCXCCL debited or credited to the member, with GST. They are the member's: Ledgerline bills clients its own schedule, so they are listed for the accounts, not matched to client charges.",
		Columns: []string{"Note", "Debit/credit", "Charge", "Contract", "Amount", "GST", "Total"}}
	for _, d := range o.ccData(ctx, tenant, "mcxdcnote", date, "") {
		dn.Loaded, dn.File = true, d.File
		for _, r := range d.Rows {
			dn.Rows = append(dn.Rows, []string{r["Dr/Cr Note No."], r["Debit/Credit"], r["Charge Description"], strings.TrimSpace(r["Symbol"] + " " + r["Expiry Date"]),
				decOr0(r["Charge Amount"]).Fixed(2), decOr0(r["Tax Amount"]).Fixed(2), decOr0(r["Total Amount"]).Fixed(2)})
		}
	}
	out = append(out, dn)
	return out
}
