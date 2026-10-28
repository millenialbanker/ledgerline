package ops

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
	"ledgerline/internal/xfile"
)

// ---- clearing and depository data ----
//
// The files the clearing corporations and CDSL send every day are loaded
// here and put to work:
//
//   - security masters (NSE, BSE, NSE Clearing's applicable symbols, CDSL's
//     ISIN master) name the securities clients hold;
//   - CDSL closing prices, mutual fund NAVs and NSE Clearing's VaR file value
//     holdings and collateral and give the haircuts where the day's market
//     data has none;
//   - the clearing corporations' client margin files (NSE Clearing MG13 and
//     the standardised margin file, MCXCCL) are the basis of the margin
//     reporting files sent back with the margin collected;
//   - short allocations (SA04) are reported back with reasons;
//   - N-MASS collateral, stamp duty, the cash market obligation, direct
//     pay-outs and the re-pledge report are reconciled with Ledgerline's own
//     figures on the Clearing data screen;
//   - CDSL's transaction file becomes the DP transaction register (DP
//     statements and billing) and its calendar is kept with the settlement
//     calendar.

const (
	cCCData   = "ccdata"    // kind|date|segment: rows of a day's file
	cSecRef   = "secref"    // ISIN: names and codes
	cISINPx   = "isinprice" // date: ISIN -> price
	cSecVaR   = "secvar"    // date: ISIN -> VaR, ELM, haircut
	cSAReason = "sareason"  // date|segment|ucc|code: reported short-allocation reasons
)

// CCData is one day's rows of a clearing or depository file, kept as loaded.
type CCData struct {
	ID       string              `json:"id"`
	Kind     string              `json:"kind"`
	Date     string              `json:"date"`
	Segment  string              `json:"segment,omitempty"`
	File     string              `json:"file"`
	LoadedAt string              `json:"loaded_at"`
	Rows     []map[string]string `json:"rows"`
}

// SecRef is what the masters say about a security.
type SecRef struct {
	ISIN    string `json:"isin"`
	Symbol  string `json:"symbol,omitempty"`
	Series  string `json:"series,omitempty"`
	Name    string `json:"name,omitempty"`
	BSECode string `json:"bse_code,omitempty"`
	BSEGrp  string `json:"bse_group,omitempty"`
	Type    string `json:"type,omitempty"`
	FaceVal string `json:"face_value,omitempty"`
	Status  string `json:"status,omitempty"`
}

// ISINPrices is one day's closing prices by ISIN.
type ISINPrices struct {
	ID     string               `json:"id"`
	Prices map[string]money.Dec `json:"prices"`
	Src    map[string]string    `json:"src,omitempty"`
}

// SecVaR is one security's margin rates (per cent).
type SecVaR struct {
	VaR     money.Dec `json:"var"`
	ELM     money.Dec `json:"elm"`
	Haircut money.Dec `json:"haircut"`
}

// SecVaRs is one day's margin rates by ISIN.
type SecVaRs struct {
	ID    string            `json:"id"`
	Rates map[string]SecVaR `json:"rates"`
}

func ymdFromName(name string) string {
	for _, rx := range []*regexp.Regexp{regexp.MustCompile(`(20\d\d)(0[1-9]|1[0-2])([0-3]\d)`), regexp.MustCompile(`([0-3]\d)(0[1-9]|1[0-2])(20\d\d)`)} {
		if m := rx.FindStringSubmatch(name); m != nil {
			d := m[1] + "-" + m[2] + "-" + m[3]
			if len(m[1]) == 2 {
				d = m[3] + "-" + m[2] + "-" + m[1]
			}
			if validDate(d) {
				return d
			}
		}
	}
	return ""
}

func anyDate(v string) string {
	v = strings.TrimSpace(v)
	for _, l := range []string{"2006-01-02", "02-Jan-2006", "02Jan2006", "02012006", "20060102", "02-01-2006", "02/01/2006", "2006-01-02T15:04:05"} {
		if len(v) >= len(l) {
			if t, err := time.Parse(l, v[:len(l)]); err == nil {
				return t.Format("2006-01-02")
			}
		}
	}
	return ""
}

func (o *Ops) putCCData(ctx context.Context, tenant, actor, kind, date, seg, file string, rows []xfile.Row, keep []string) (int, error) {
	d := CCData{ID: kind + "|" + date + "|" + seg, Kind: kind, Date: date, Segment: seg, File: file, LoadedAt: o.now().Format("2006-01-02 15:04")}
	for _, r := range rows {
		m := map[string]string{}
		if keep == nil {
			for k, v := range r.Values {
				if strings.TrimSpace(v) != "" {
					m[k] = strings.TrimSpace(v)
				}
			}
		} else {
			for _, k := range keep {
				if v := strings.TrimSpace(r.Get(k)); v != "" {
					m[k] = v
				}
			}
		}
		if r.Record != "" && r.Record != "(text)" {
			m["_rec"] = r.Record
		}
		d.Rows = append(d.Rows, m)
	}
	return len(d.Rows), docs.Upsert(ctx, o.Docs, tenant, cCCData, d.ID, d, actor)
}

// ccData returns a day's rows of a kind (all segments when seg is "").
func (o *Ops) ccData(ctx context.Context, tenant, kind, date, seg string) []CCData {
	p := kind + "|" + date + "|"
	if seg != "" {
		p += seg
	}
	ds, _ := docs.List[CCData](ctx, o.Docs, tenant, cCCData, p)
	return ds
}

// latestDoc finds the latest dated document of a collection on or before date.
func latestDoc[T any](ctx context.Context, o *Ops, tenant, coll, date string) (T, bool) {
	var zero T
	idx, _, _ := docs.Get[struct{ Dates []string }](ctx, o.Docs, tenant, coll, "_index")
	best := ""
	for _, d := range idx.Dates {
		if d <= date && d > best {
			best = d
		}
	}
	if best == "" {
		return zero, false
	}
	v, _, err := docs.Get[T](ctx, o.Docs, tenant, coll, best)
	return v, err == nil
}

func (o *Ops) indexDate(ctx context.Context, tenant, coll, date, actor string) {
	idx, _, _ := docs.Get[struct{ Dates []string }](ctx, o.Docs, tenant, coll, "_index")
	if !contains(idx.Dates, date) {
		idx.Dates = append(idx.Dates, date)
		sort.Strings(idx.Dates)
		_ = docs.Upsert(ctx, o.Docs, tenant, coll, "_index", idx, actor)
	}
}

// ---- loading ----

// loadCCFile handles the clearing and depository files; ok is false for
// files it does not handle.
func (o *Ops) loadCCFile(ctx context.Context, tenant, actor string, s *xfile.Spec, f xfile.File, rows []xfile.Row, r *XFileResult) bool {
	fail := func(err error) { r.Loaded = "Not loaded: " + strings.TrimPrefix(err.Error(), "invalid: ") }
	date := ymdFromName(f.Name)
	seg := func(name string) string {
		switch {
		case strings.HasPrefix(strings.ToUpper(name), "F_"), strings.HasPrefix(strings.ToUpper(name), "FO_"), strings.Contains(strings.ToUpper(name), "_FO_"):
			return "FO"
		case strings.HasPrefix(strings.ToUpper(name), "X_"), strings.Contains(strings.ToUpper(name), "_CD_"):
			return "CD"
		case strings.HasPrefix(strings.ToUpper(name), "MCX"), strings.Contains(strings.ToUpper(name), "_CO_"):
			return "COM"
		}
		return "CM"
	}(f.Name)
	need := func() bool {
		if date == "" {
			fail(fmt.Errorf("the date is not in the file name"))
			return false
		}
		return true
	}
	switch s.ID {
	case "nse-security-master", "bse-scrip-series", "ncl-applicable-symbol", "cdsl-isin-master":
		n := 0
		for _, row := range rows {
			isin := strings.ToUpper(firstNonEmptyStr(row.Get("Security ISIN"), row.Get("ISIN")))
			if len(isin) != 12 {
				continue
			}
			ref, _, _ := docs.Get[SecRef](ctx, o.Docs, tenant, cSecRef, isin)
			ref.ISIN = isin
			set := func(dst *string, v string) {
				if v = strings.TrimSpace(v); v != "" {
					*dst = v
				}
			}
			switch s.ID {
			case "nse-security-master":
				set(&ref.Symbol, row.Get("Security Symbol"))
				set(&ref.Series, row.Get("Security Series"))
				set(&ref.Name, row.Get("Security Description"))
			case "bse-scrip-series":
				set(&ref.BSECode, row.Get("Security BSE Scrip Code"))
				set(&ref.BSEGrp, row.Get("Security BSE Group"))
				if ref.Name == "" {
					set(&ref.Name, row.Get("Security Description"))
				}
			case "ncl-applicable-symbol":
				set(&ref.Symbol, row.Get("Security Applicable Symbol"))
				if ref.Name == "" {
					set(&ref.Name, row.Get("Security Description"))
				}
			case "cdsl-isin-master":
				set(&ref.Name, firstNonEmptyStr(row.Get("ISINDesc"), ref.Name))
				set(&ref.Type, row.Get("FinInstrmTp"))
				set(&ref.FaceVal, row.Get("ParVal"))
				set(&ref.Status, row.Get("SctySts"))
			}
			if err := docs.Upsert(ctx, o.Docs, tenant, cSecRef, isin, ref, actor); err != nil {
				fail(err)
				return true
			}
			n++
		}
		r.Loaded, r.Added = "Loaded into the security master (names, symbols, codes by ISIN)", n
	case "cdsl-closing-price", "ncl-mf-var":
		day := date
		px := ISINPrices{Prices: map[string]money.Dec{}, Src: map[string]string{}}
		rates := map[string]SecVaR{}
		for _, row := range rows {
			isin := strings.ToUpper(row.Get("ISIN"))
			if s.ID == "cdsl-closing-price" {
				if d := anyDate(row.Get("ActlDt")); d != "" && d > day {
					day = d
				}
				if p, err := money.ParseDec(row.Get("Price")); err == nil && p.Sign() > 0 && len(isin) == 12 {
					px.Prices[isin], px.Src[isin] = p, "CDSL"
				}
				continue
			}
			if p, err := money.ParseDec(row.Get("NAV")); err == nil && p.Sign() > 0 && len(isin) == 12 {
				px.Prices[isin], px.Src[isin] = p, "MF NAV"
			}
			if h, err := money.ParseDec(row.Get("HAIRCUT")); err == nil && len(isin) == 12 {
				rates[isin] = SecVaR{Haircut: h}
			}
		}
		if day == "" {
			fail(fmt.Errorf("the date is not in the file name"))
			return true
		}
		old, _, _ := docs.Get[ISINPrices](ctx, o.Docs, tenant, cISINPx, day)
		for k, v := range old.Prices {
			if _, ok := px.Prices[k]; !ok {
				px.Prices[k], px.Src[k] = v, old.Src[k]
			}
		}
		px.ID = day
		if err := docs.Upsert(ctx, o.Docs, tenant, cISINPx, day, px, actor); err != nil {
			fail(err)
			return true
		}
		o.indexDate(ctx, tenant, cISINPx, day, actor)
		if len(rates) > 0 {
			o.mergeVaR(ctx, tenant, actor, day, rates)
		}
		r.Loaded, r.Added = "Loaded as closing prices of "+day+" (values holdings and collateral without an exchange price)", len(px.Prices)
	case "ncl-var":
		if !need() {
			return true
		}
		rates := map[string]SecVaR{}
		for _, row := range rows {
			if row.Record != "20" {
				continue
			}
			isin := strings.ToUpper(row.Get("ISIN"))
			v := SecVaR{VaR: decOr0(row.Get("VaRMargin")), ELM: decOr0(row.Get("ExtremeLossRate")), Haircut: decOr0(row.Get("ApplicableMarginRate"))}
			if len(isin) == 12 {
				rates[isin] = v
			}
		}
		o.mergeVaR(ctx, tenant, actor, date, rates)
		r.Loaded, r.Added = "Loaded as VaR, extreme loss and haircut rates of "+date, len(rates)
	case "ncl-span-client", "ncl-span-client-fo", "mcx-client-margin", "ncl-short-allocation", "ncl-nmass-cm", "ncl-nmass-fo", "stamp-duty", "nse-cm-obligation", "ncl-direct-payout", "ncl-repledge-status", "ncl-client-margin", "cdsl-calendar", "cdsl-member-master":
		kind := map[string]string{"ncl-span-client": "mg13", "ncl-span-client-fo": "mg13", "mcx-client-margin": "mcxmargin", "ncl-short-allocation": "sa04", "ncl-nmass-cm": "nmass", "ncl-nmass-fo": "nmass",
			"stamp-duty": "stamp", "nse-cm-obligation": "obligation", "ncl-direct-payout": "directpayout", "ncl-repledge-status": "repledge", "ncl-client-margin": "nclmargin", "cdsl-calendar": "cdslcal", "cdsl-member-master": "cdslmembers"}[s.ID]
		if len(rows) > 0 {
			for _, k := range []string{"Date", "RptgDt", "BizDt", "ClctnDt", "DueDt", "Obligation Date"} {
				if d := anyDate(rows[0].Get(k)); d != "" {
					date = d
					break
				}
			}
			if sg := strings.ToUpper(rows[0].Get("Sgmt")); sg != "" && kind != "stamp" {
				seg = map[string]string{"CO": "COM"}[sg]
				if seg == "" {
					seg = sg
				}
			}
		}
		if s.ID == "cdsl-calendar" || s.ID == "cdsl-member-master" {
			date, seg = firstOf(date, o.now().Format("2006-01-02")), "CDSL"
		}
		if !need() {
			return true
		}
		n, err := o.putCCData(ctx, tenant, actor, kind, date, seg, f.Name, rows, nil)
		if err != nil {
			fail(err)
			return true
		}
		r.Loaded, r.Added = map[string]string{
			"mg13": "Loaded as the clearing corporation's client margins (used for the margin reporting file and checked on Clearing data)", "mcxmargin": "Loaded as MCXCCL's client margins (used for the margin reporting file)",
			"sa04": "Loaded as short allocations, to report back with reasons on Clearing data", "nmass": "Loaded and reconciled with Ledgerline's collateral allocation on Clearing data",
			"stamp": "Loaded and reconciled with the stamp duty Ledgerline charged on Clearing data", "obligation": "Loaded and reconciled with Ledgerline's trades on Clearing data",
			"directpayout": "Loaded as securities paid out directly to clients (shown on Clearing data)", "repledge": "Loaded and reconciled with clients' pledges on Clearing data",
			"nclmargin": "Loaded as the clearing corporation's client margins with peak (checked on Clearing data)", "cdslcal": "Loaded as CDSL's settlement calendar", "cdslmembers": "Loaded as CDSL's participant master",
		}[kind], n
	case "cdsl-transactions":
		n, skipped, err := o.loadCDSLTxns(ctx, tenant, actor, rows)
		if err != nil {
			fail(err)
			return true
		}
		r.Loaded, r.Added, r.Skipped = "Loaded into the DP transaction register (DP statements and billing)", n, skipped
	default:
		return false
	}
	return true
}

func (o *Ops) mergeVaR(ctx context.Context, tenant, actor, date string, rates map[string]SecVaR) {
	old, _, _ := docs.Get[SecVaRs](ctx, o.Docs, tenant, cSecVaR, date)
	if old.Rates == nil {
		old.Rates = map[string]SecVaR{}
	}
	for k, v := range rates {
		x := old.Rates[k]
		if v.VaR.Sign() != 0 {
			x.VaR = v.VaR
		}
		if v.ELM.Sign() != 0 {
			x.ELM = v.ELM
		}
		if v.Haircut.Sign() != 0 {
			x.Haircut = v.Haircut
		}
		old.Rates[k] = x
	}
	old.ID = date
	_ = docs.Upsert(ctx, o.Docs, tenant, cSecVaR, date, old, actor)
	o.indexDate(ctx, tenant, cSecVaR, date, actor)
}

// addRefData fills the day's lookup from the clearing and depository
// files: prices of securities without an exchange price, and margin rates
// and haircuts where the market data has none.
func (o *Ops) addRefData(ctx context.Context, tenant, date string, l *Lookup) {
	if px, ok := latestDoc[ISINPrices](ctx, o, tenant, cISINPx, date); ok {
		for isin, p := range px.Prices {
			if _, have := l.byISIN[isin]; have {
				continue
			}
			sym := isin
			if ref, _, err := docs.Get[SecRef](ctx, o.Docs, tenant, cSecRef, isin); err == nil && firstOf(ref.Symbol, ref.Name) != "" {
				sym = firstOf(ref.Symbol, ref.Name)
			}
			l.byISIN[isin] = Instrument{Exchange: px.Src[isin], Segment: "REF", Symbol: sym, Type: "EQ", ISIN: isin, LTP: p, Close: p}
		}
	}
	vr, ok := latestDoc[SecVaRs](ctx, o, tenant, cSecVaR, date)
	if !ok {
		return
	}
	fill := func(i Instrument) Instrument {
		v, ok := vr.Rates[i.ISIN]
		if !ok {
			return i
		}
		if i.VaRPct.Sign() == 0 {
			i.VaRPct = v.VaR
		}
		if i.ELMPct.Sign() == 0 {
			i.ELMPct = v.ELM
		}
		if i.HaircutPct.Sign() == 0 {
			i.HaircutPct = v.Haircut
		}
		return i
	}
	for k, i := range l.m {
		if i.ISIN != "" {
			l.m[k] = fill(i)
		}
	}
	for k, i := range l.bySym {
		if i.ISIN != "" {
			l.bySym[k] = fill(i)
		}
	}
	for k, i := range l.byISIN {
		l.byISIN[k] = fill(i)
	}
}

// ---- CDSL transactions into the DP register ----

// loadCDSLTxns adds CDSL's transactions of the firm's DP clients to the DP
// transaction register: credits and debits (TxnCd CRD/DBT) by demat
// account, with the counterparty accounts; earmarks and other non-balance
// records are left out. Debits and credits are billable (pledges are not);
// those to or from the firm's charge-free pool accounts are not charged.
func (o *Ops) loadCDSLTxns(ctx context.Context, tenant, actor string, rows []xfile.Row) (int, int, error) {
	cs, err := o.ClientMap(ctx, tenant)
	if err != nil {
		return 0, 0, err
	}
	byBO := map[string]string{}
	for _, c := range cs {
		if c.Demat.ID != "" {
			byBO[strings.TrimSpace(c.Demat.ID)] = c.UCC
		}
	}
	seen := map[string]bool{}
	existing, _ := docs.List[DPTxn](ctx, o.Docs, tenant, cDPTxn, "")
	for _, t := range existing {
		if strings.HasPrefix(t.Ref, "CDSL:") {
			seen[t.Ref] = true
		}
	}
	n, skipped := 0, 0
	for _, r := range rows {
		ucc, ok := byBO[strings.TrimSpace(r.Get("ClntId"))]
		cd := strings.ToUpper(r.Get("TxnCd"))
		q, err := money.ParseDec(r.Get("Qty"))
		if !ok || (cd != "CRD" && cd != "DBT") || err != nil {
			skipped++
			continue
		}
		ref := "CDSL:" + r.Get("TxnID") + ":" + r.Get("LineNb") + ":" + r.Get("ISIN") + ":" + cd
		if seen[ref] {
			skipped++
			continue
		}
		seen[ref] = true
		qty := q.Round(money.UnitRupee).Paise() / 100
		if cd == "DBT" {
			qty = -qty
		}
		t := DPTxn{Date: firstOf(anyDate(r.Get("ExecDt")), anyDate(r.Get("BizDt"))), UCC: ucc, ISIN: strings.ToUpper(r.Get("ISIN")), Qty: qty,
			Kind: cdslTxnKind(r.Get("TxnTyp")), Ref: ref, Billable: !pledgeTxn[strings.ToUpper(r.Get("TxnTyp"))]}
		for _, c := range []string{r.Get("CntrBnfclOwnrId"), r.Get("OthrDpstryPtcpt") + r.Get("OthrDpstryClntCd"), r.Get("CuspaAcct"), r.Get("EPAcct")} {
			if c = normDematID(c); len(c) >= 8 {
				t.Counterparty = append(t.Counterparty, c)
			}
		}
		if ref, _, err := docs.Get[SecRef](ctx, o.Docs, tenant, cSecRef, t.ISIN); err == nil {
			t.Symbol = firstOf(ref.Symbol, ref.Name)
		}
		if err := o.dpTxn(ctx, tenant, actor, t); err != nil {
			return n, skipped, err
		}
		n++
	}
	return n, skipped, nil
}

// pledgeTxn are CDSL transaction types charged as pledges, not transfers.
var pledgeTxn = map[string]bool{"PLG": true, "PCC": true}

func cdslTxnKind(tp string) string {
	if k, ok := map[string]string{"NPY": "Pay-out from clearing", "IDO": "Delivery to clearing (on-market)", "IDD": "Inter-depository transfer", "IPO": "IPO / corporate action credit",
		"PLG": "Pledge", "PCC": "Pledge (clearing corporation)", "WTR": "Off-market transfer"}[strings.ToUpper(tp)]; ok {
		return k
	}
	return "CDSL " + tp
}

// ---- the clearing data screen ----

// CCSection is one reconciliation or list on the Clearing data screen.
type CCSection struct {
	ID      string     `json:"id"`
	Title   string     `json:"title"`
	About   string     `json:"about"`
	File    string     `json:"file,omitempty"`
	Columns []string   `json:"columns"`
	Rows    [][]string `json:"rows"`
	Diffs   int        `json:"differences"`
	Loaded  bool       `json:"loaded"`
}

// ClearingData reconciles the day's clearing and depository files with
// Ledgerline's figures.
func (o *Ops) ClearingData(ctx context.Context, tenant, date string) ([]CCSection, error) {
	if !validDate(date) {
		return nil, invalid("give a date (YYYY-MM-DD)")
	}
	cs, _ := o.ClientMap(ctx, tenant)
	alloc, _ := o.Allocation(ctx, tenant, date)
	ab := map[string]AllocationRow{}
	for _, a := range alloc {
		ab[a.Segment+"|"+a.UCC] = a
	}
	near := func(a, b money.Dec) bool { return a.Sub(b).Abs().Cmp(money.MustDec("1")) <= 0 }
	var out []CCSection

	// Margins: clearing corporation vs Ledgerline.
	mg := CCSection{ID: "margin", Title: "Client margins", About: "Margin the clearing corporation requires for each client (MG13, MCXCCL margin file) against the margin Ledgerline computed for the segment.",
		Columns: []string{"Segment", "Client", "Name", "Clearing corporation", "Ledgerline", "Difference"}}
	for _, d := range append(o.ccData(ctx, tenant, "mg13", date, ""), o.ccData(ctx, tenant, "mcxmargin", date, "")...) {
		mg.Loaded, mg.File = true, strings.TrimPrefix(mg.File+", "+d.File, ", ")
		for _, r := range d.Rows {
			ucc := strings.ToUpper(firstOf(r["ClientCode"], r["Client Code"]))
			var cc money.Dec
			if d.Kind == "mg13" {
				cc = decOr0(r["EODMarginToCollect"])
				if d.Segment != "CM" {
					cc = decOr0(r["Field8"])
				}
				if r["ClientProFlag"] == "P" {
					continue
				}
			} else {
				cc = decOr0(r["InitialMargin"]).Add(decOr0(r["OtherMargin"])).Add(decOr0(r["MTM"]))
				if ucc == "*OWN*" {
					continue
				}
			}
			l := ab[d.Segment+"|"+ucc].Required
			row := []string{d.Segment, ucc, cs[ucc].Name, cc.Fixed(2), l.Fixed(2), cc.Sub(l).Fixed(2)}
			if !near(cc, l) {
				mg.Diffs++
			}
			mg.Rows = append(mg.Rows, row)
		}
	}
	out = append(out, mg)

	// Collateral: N-MASS vs Ledgerline's allocation.
	nm := CCSection{ID: "collateral", Title: "Collateral with the clearing corporation", About: "Cash and non-cash collateral NSE Clearing holds against each client (N-MASS) against Ledgerline's allocation for the segment.",
		Columns: []string{"Segment", "Client", "Name", "Cash (NCL)", "Cash (Ledgerline)", "Non-cash (NCL)", "Non-cash (Ledgerline)", "Difference"}}
	for _, d := range o.ccData(ctx, tenant, "nmass", date, "") {
		nm.Loaded, nm.File = true, d.File
		for _, r := range d.Rows {
			ucc := strings.ToUpper(firstOf(r["CLIENT CODE"], r["Client Code"]))
			if ucc == "" {
				continue
			}
			cash := decOr0(firstOf(r["CASH(A)"], r["Cash allocated (b)"]))
			non := decOr0(firstOf(r["NON CASH(B)"], r["Non-cash Securities (d)"]))
			a := ab[d.Segment+"|"+ucc]
			diff := cash.Add(non).Sub(a.Cash.Add(a.NonCash))
			if !near(diff, money.Dec{}) {
				nm.Diffs++
			}
			nm.Rows = append(nm.Rows, []string{d.Segment, ucc, cs[ucc].Name, cash.Fixed(2), a.Cash.Fixed(2), non.Fixed(2), a.NonCash.Fixed(2), diff.Fixed(2)})
		}
	}
	out = append(out, nm)

	// Short allocations and the reasons reported.
	sa := CCSection{ID: "short_allocation", Title: "Short allocation of collateral", About: "Clients whose collateral fell short of the minimum margin (SA04). Record the reason and the collateral available, then download the reporting file from Exchange files.",
		Columns: []string{"Segment", "Snapshot", "Client", "Name", "Minimum margin", "Collateral", "Short", "Reasons reported"}}
	for _, d := range o.ccData(ctx, tenant, "sa04", date, "") {
		sa.Loaded, sa.File = true, d.File
		for _, r := range d.Rows {
			ucc := strings.ToUpper(r["ClientCode"])
			rs, _ := docs.List[SAReason](ctx, o.Docs, tenant, cSAReason, date+"|"+d.Segment+"|"+ucc+"|")
			var why []string
			for _, x := range rs {
				why = append(why, x.Code+" ₹"+x.Amount.Fixed(2))
			}
			if len(why) == 0 && decOr0(r["ShortAllocation"]).Sign() > 0 {
				sa.Diffs++
			}
			sa.Rows = append(sa.Rows, []string{d.Segment, r["Indicator"], ucc, cs[ucc].Name, decOr0(r["MinimumMarginRequired"]).Fixed(2), decOr0(r["TotalCollateral"]).Fixed(2), decOr0(r["ShortAllocation"]).Fixed(2), strings.Join(why, ", ")})
		}
	}
	out = append(out, sa)

	// Stamp duty: clearing corporation vs what Ledgerline charged.
	st := CCSection{ID: "stamp", Title: "Stamp duty", About: "Stamp duty the clearing corporation collected per client against the stamp duty on Ledgerline's bills for the day.",
		Columns: []string{"Segment", "Client", "Name", "Clearing corporation", "Ledgerline", "Difference"}}
	for _, d := range o.ccData(ctx, tenant, "stamp", date, "") {
		st.Loaded, st.File = true, strings.TrimPrefix(st.File+", "+d.File, ", ")
		per := map[string]money.Dec{}
		for _, r := range d.Rows {
			if u := strings.ToUpper(r["ClntId"]); u != "" {
				per[u] = per[u].Add(decOr0(r["StmpDtyAmt"]))
			}
		}
		for _, u := range sortedKeys(per) {
			bt, _ := o.bills(ctx, tenant, u, date, date)
			l := bt.charges["Stamp duty"].Neg()
			if l.Sign() < 0 {
				l = l.Neg()
			}
			if !near(per[u], l) {
				st.Diffs++
			}
			st.Rows = append(st.Rows, []string{d.Segment, u, cs[u].Name, per[u].Fixed(2), l.Fixed(2), per[u].Sub(l).Fixed(2)})
		}
	}
	out = append(out, st)

	// Cash market obligation by security vs the day's trades.
	ob := CCSection{ID: "obligation", Title: "Cash market obligation", About: "NSE's obligation per security (buy and sell quantity) against the firm's trades of the day in Ledgerline.",
		Columns: []string{"Security", "Series", "Buy (NSE)", "Buy (Ledgerline)", "Sell (NSE)", "Sell (Ledgerline)", "Difference"}}
	if ds := o.ccData(ctx, tenant, "obligation", date, ""); len(ds) > 0 {
		ts, _ := o.exchangeTrades(ctx, tenant, date)
		type bs struct{ b, s int64 }
		mine := map[string]*bs{}
		for _, t := range ts {
			if t.Segment != "CM" || t.Exchange != "NSE" {
				continue
			}
			k := t.Symbol
			if mine[k] == nil {
				mine[k] = &bs{}
			}
			if t.Side == "B" {
				mine[k].b += t.Qty
			} else {
				mine[k].s += t.Qty
			}
		}
		for _, d := range ds {
			ob.Loaded, ob.File = true, d.File
			for _, r := range d.Rows {
				sym := r["Security Symbol"]
				b, s := decOr0(r["Daily Buy Volume"]), decOr0(r["Daily Sell Volume"])
				m := mine[sym]
				if m == nil {
					m = &bs{}
				}
				diff := b.Sub(money.DecInt(m.b)).Abs().Add(s.Sub(money.DecInt(m.s)).Abs())
				if diff.Sign() != 0 {
					ob.Diffs++
				}
				ob.Rows = append(ob.Rows, []string{sym, r["Security Series"], b.Fixed(0), fmt.Sprint(m.b), s.Fixed(0), fmt.Sprint(m.s), diff.Fixed(0)})
			}
		}
	}
	out = append(out, ob)

	// Re-pledged securities accepted by the clearing corporation vs clients' pledges.
	rp := CCSection{ID: "repledge", Title: "Securities re-pledged to the clearing corporation", About: "Quantity NSE Clearing accepted as collateral per client and security against the quantity the client has pledged to the firm.",
		Columns: []string{"Client", "Name", "ISIN", "Security", "Accepted (NCL)", "Pledged (Ledgerline)", "Haircut %", "Net value (NCL)"}}
	if ds := o.ccData(ctx, tenant, "repledge", date, ""); len(ds) > 0 {
		pl := map[string]int64{}
		hs, _ := o.Holdings(ctx, tenant, "")
		for _, h := range hs {
			pl[h.UCC+"|"+h.ISIN] = h.Pledged
		}
		for _, d := range ds {
			rp.Loaded, rp.File = true, d.File
			for _, r := range d.Rows {
				ucc, isin := strings.ToUpper(r["ClientCpCode"]), strings.ToUpper(r["Isin"])
				if len(isin) != 12 || ucc == "" || ucc == "CLIENT/CPCODE" {
					continue
				}
				q := decOr0(r["Qty"])
				if q.Cmp(money.DecInt(pl[ucc+"|"+isin])) != 0 {
					rp.Diffs++
				}
				rp.Rows = append(rp.Rows, []string{ucc, cs[ucc].Name, isin, r["Symbol"], q.Fixed(0), fmt.Sprint(pl[ucc+"|"+isin]), r["Haircut"], r["NetValue"]})
			}
		}
	}
	out = append(out, rp)

	// Direct pay-outs.
	dp := CCSection{ID: "direct_payout", Title: "Securities paid out directly to clients", About: "NSE Clearing's pay-out of securities straight to clients' demat accounts (and short deliveries), as reported in the direct pay-out file.",
		Columns: []string{"Client", "Name", "ISIN", "Security", "Quantity", "Type", "To demat", "Status"}}
	for _, d := range o.ccData(ctx, tenant, "directpayout", date, "") {
		dp.Loaded, dp.File = true, d.File
		for _, r := range d.Rows {
			u := strings.ToUpper(r["ClntId"])
			dp.Rows = append(dp.Rows, []string{u, cs[u].Name, r["ISIN"], r["TckrSymb"], r["QtyORShrtQty"], firstOf(r["TxTp"], r["TxInd"]), r["TrgtDpstryAcct"], firstOf(r["DpstryConfRjctnCd"], r["Rmks"])})
		}
	}
	out = append(out, dp)
	for i := range out {
		if out[i].Rows == nil {
			out[i].Rows = [][]string{}
		}
	}
	return out, nil
}

// ---- short-allocation reasons ----

// SAReason is one reason reported for a client's short allocation.
type SAReason struct {
	ID     string    `json:"id"`
	Date   string    `json:"date"`
	Seg    string    `json:"segment"`
	UCC    string    `json:"ucc"`
	Code   string    `json:"code"`
	Amount money.Dec `json:"amount"`
	CC     string    `json:"cc_code,omitempty"`
	By     string    `json:"by"`
}

// SAReasonCodes are NSE Clearing's reason codes for short allocation.
var SAReasonCodes = map[string]string{"01": "Excess collateral with another clearing corporation", "02": "Early pay-in of securities sold on the day", "03": "Trades in a wrong client code (intraday)",
	"04": "Trades for NRI clients", "05": "Allocation submitted, accepted later (intraday)", "06": "Re-pledge not yet processed (intraday)", "07": "Custodian early pay-in of funds (intraday)"}

// SaveSAReason records (or with a zero amount removes) a reason.
func (o *Ops) SaveSAReason(ctx context.Context, tenant, actor string, x SAReason) (SAReason, error) {
	if err := o.require(ctx, tenant, actor, "Reporting short allocation", RoleMaker, RoleChecker, RoleRisk); err != nil {
		return x, err
	}
	x.UCC, x.Seg = strings.ToUpper(x.UCC), strings.ToUpper(x.Seg)
	if !validDate(x.Date) || x.UCC == "" || x.Seg == "" {
		return x, invalid("give the date, segment and client")
	}
	if _, ok := SAReasonCodes[x.Code]; !ok {
		return x, invalid("reason code must be 01 to 07")
	}
	if x.Code == "01" && strings.TrimSpace(x.CC) == "" {
		return x, invalid("reason 01 needs the other clearing corporation's code")
	}
	if x.Code != "01" {
		x.CC = ""
	}
	x.ID, x.By = x.Date+"|"+x.Seg+"|"+x.UCC+"|"+x.Code, actor
	if err := docs.Upsert(ctx, o.Docs, tenant, cSAReason, x.ID, x, actor); err != nil {
		return x, err
	}
	o.log(ctx, tenant, actor, "short_allocation.reason", x.ID, map[string]any{"amount": x.Amount.Fixed(2)})
	return x, nil
}

// ---- outgoing files built from the clearing data ----

// marginReportingFile writes NSE Clearing's (cash) or MCXCCL's margin
// reporting upload: the clearing corporation's own row, unaltered, with the
// margin collected from Ledgerline's allocation for the segment.
func (o *Ops) marginReportingFile(ctx context.Context, tenant string, s *xfile.Spec, set Settings, date string, vars xfile.Vars) (string, []byte, error) {
	alloc, err := o.Allocation(ctx, tenant, date)
	if err != nil {
		return "", nil, err
	}
	seg := "CM"
	if s.ID == "mcx-margin-reporting" {
		seg = "COM"
	}
	have := map[string]money.Dec{}
	for _, a := range alloc {
		if a.Segment == seg {
			have[a.UCC] = a.Cash.Add(a.NonCash)
		}
	}
	var rows []xfile.Row
	if s.ID == "mcx-margin-reporting" {
		ds := o.ccData(ctx, tenant, "mcxmargin", date, "COM")
		if len(ds) == 0 {
			return "", nil, invalid("load MCXCCL's client margin file for %s first", date)
		}
		for _, r := range ds[0].Rows {
			v := map[string]string{}
			for k, x := range r {
				v[k] = x
			}
			ucc := strings.ToUpper(r["ClientCode"])
			im, oth, mtm, peak := decOr0(r["InitialMargin"]), decOr0(r["OtherMargin"]), decOr0(r["MTM"]), decOr0(r["PeakMargin"])
			got := have[ucc]
			if ucc == "*OWN*" || ucc == "" {
				got = im.Add(oth).Add(mtm) // the firm's own positions: its own funds
			}
			mc := money.Min(got, mtm)
			rest := got.Sub(mc)
			ic := money.Min(rest, im)
			oc := rest.Sub(ic)
			v["MTMCollected"], v["IMCollected"], v["OtherMarginCollected"], v["PeakMarginCollected"] = mc.Fixed(2), ic.Fixed(2), oc.Fixed(2), money.Max(got, money.Min(got, peak)).Fixed(2)
			rows = append(rows, xfile.Row{Values: v})
		}
	} else {
		ds := o.ccData(ctx, tenant, "mg13", date, "CM")
		if len(ds) == 0 {
			return "", nil, invalid("load NSE Clearing's client margin file (C_MG13) for %s first", date)
		}
		for _, r := range ds[0].Rows {
			v := map[string]string{}
			for k, x := range r {
				v[k] = x
			}
			got := have[strings.ToUpper(r["ClientCode"])]
			if r["ClientProFlag"] == "P" {
				got = decOr0(r["EODMarginToCollect"])
			}
			v["EODMarginCollected"], v["PeakMarginCollected"] = got.Fixed(2), got.Fixed(2)
			rows = append(rows, xfile.Row{Values: v})
		}
	}
	content, err := s.Write(rows)
	if err != nil {
		return "", nil, err
	}
	name, err := s.FileName(vars)
	return name, content, err
}

// shortAllocationFile writes the short-allocation reporting upload: one
// line per reason recorded, each with the SA04 row unaltered.
func (o *Ops) shortAllocationFile(ctx context.Context, tenant string, s *xfile.Spec, date string, vars xfile.Vars) (string, []byte, error) {
	var rows []xfile.Row
	for _, d := range o.ccData(ctx, tenant, "sa04", date, "CM") {
		for _, r := range d.Rows {
			rs, _ := docs.List[SAReason](ctx, o.Docs, tenant, cSAReason, date+"|CM|"+strings.ToUpper(r["ClientCode"])+"|")
			for _, x := range rs {
				v := map[string]string{}
				for k, y := range r {
					v[k] = y
				}
				v["CollateralAvailable"], v["ReasonCode"], v["CCCode"] = x.Amount.Fixed(2), x.Code, x.CC
				rows = append(rows, xfile.Row{Values: v})
			}
		}
	}
	if len(rows) == 0 {
		return "", nil, fmt.Errorf("%w: no reasons recorded for the cash market short allocations of %s (Clearing data)", ErrState, date)
	}
	content, err := s.Write(rows)
	if err != nil {
		return "", nil, err
	}
	name, err := s.FileName(vars)
	return name, content, err
}

// shortageReportingFile writes the client-wise securities shortage report
// for the settlements whose pay-in was on date.
func (o *Ops) shortageReportingFile(ctx context.Context, tenant string, s *xfile.Spec, date string, vars xfile.Vars) (string, []byte, error) {
	ss, err := o.Shortages(ctx, tenant)
	if err != nil {
		return "", nil, err
	}
	rows := []xfile.Row{{Record: "", Values: map[string]string{"RecordType": "10", "SettlementDate": fmtDDMMYYYY(date)}}}
	for _, x := range ss {
		if x.PayIn != date || x.Status != "Settled" {
			continue
		}
		st, ok := o.SettlementFor(ctx, tenant, "CM", "NORMAL1", x.Date)
		if !ok {
			return "", nil, invalid("load NSE Clearing's settlement master for trades of %s first", x.Date)
		}
		d := x.Qty - x.Auction - x.Internal
		recon := "0"
		if d > 0 {
			recon = "2"
		}
		rows = append(rows, xfile.Row{Record: "#12", Values: map[string]string{"RecordType": "20", "MarketType": "01", "SettlementNo": "M" + st.No, "Ucc": x.UCC, "Isin": x.ISIN, "Symbol": x.Symbol,
			"TransactionType": "S", "QuantityShortA": fmt.Sprint(x.Qty), "ClearedInAuctionB": fmt.Sprint(x.Auction), "ClearedByCloseOutC": fmt.Sprint(x.Internal), "SettledInternallyD": fmt.Sprint(d), "ReconciliationE": recon}})
	}
	if len(rows) == 1 {
		return "", nil, fmt.Errorf("%w: no settled shortages with pay-in on %s", ErrState, date)
	}
	rows[0].Values["TotalRecords"] = fmt.Sprint(len(rows) - 1)
	content, err := s.Write(rows)
	if err != nil {
		return "", nil, err
	}
	name, err := s.FileName(vars)
	return name, content, err
}

// disMasterFile writes CDSL's transaction upload for the DIS books issued
// in the seven days to date (TxnTyp DMU: DIS issue to the BO).
func (o *Ops) disMasterFile(ctx context.Context, tenant string, s *xfile.Spec, set Settings, date string, vars xfile.Vars) (string, []byte, error) {
	sender := strings.TrimSpace(set.MemberCodes["CDSL-SENDER"])
	if sender == "" {
		return "", nil, invalid("set the CDSL sender ID (member code CDSL-SENDER) in Settings")
	}
	books, _ := docs.List[DISBook](ctx, o.Docs, tenant, cDISBook, "")
	cs, _ := o.ClientMap(ctx, tenant)
	sort.Slice(books, func(i, j int) bool { return books[i].ID < books[j].ID })
	var rows []xfile.Row
	batch := strings.ReplaceAll(date, "-", "")
	for _, b := range books {
		if b.Issued > date || b.Issued <= addDays(date, -7) {
			continue
		}
		bo := strings.TrimSpace(cs[b.UCC].Demat.ID)
		if len(bo) != 16 {
			continue
		}
		rows = append(rows, xfile.Row{Values: map[string]string{"BtchId": batch, "SndrId": sender, "BrnchId": "1", "CntrlSctiesDpstryPtcptRole": "DP", "TxnTyp": "DMU",
			"LineNb": fmt.Sprint(len(rows) + 1), "SeqNm": fmt.Sprintf("%04d", len(rows)+1), "SrcCntrlSctiesDpstryPtcpt": bo[2:8], "SrcClntIdOrBnfclOwnrId": bo,
			"DISIssncDt": b.Issued, "DISBookltTp": "NRM", "DISIssdToClntOrPwrOfAttnyHldr": "BO", "DISSrlNbFr": fmt.Sprintf("%012d", b.From), "DISSrlNbTo": fmt.Sprintf("%012d", b.To),
			"ISTxTp": "NR", "DISTxTp": "DIS", "NbOfDISLeavs": fmt.Sprint(b.To - b.From + 1), "IntlRefNb": b.ID, "FrzInittdBy": "DP", "IsseFlg": "DISI", "Prty": "HP", "DISISDTxTp": "DFP"}})
	}
	if len(rows) == 0 {
		return "", nil, fmt.Errorf("%w: no DIS books issued in the week to %s", ErrState, date)
	}
	vars["dp_id"] = rows[0].Values["SrcCntrlSctiesDpstryPtcpt"]
	content, err := s.Write(rows)
	if err != nil {
		return "", nil, err
	}
	name, err := s.FileName(vars)
	return name, content, err
}
