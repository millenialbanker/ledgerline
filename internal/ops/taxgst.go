package ops

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// ---- TDS and GST checks on the firm's own purchases and supplies ----
//
// Vendor bills (vendors.go) already suggest each section's rate. This adds
// what the rate depends on beyond the section:
//
//   - purchases of goods (s.393(1) Table Sl. 8(ii), was 194Q): only a
//     buyer whose turnover was above ₹10 crore in the previous year
//     deducts, and only on what it buys from one seller beyond ₹50 lakh in
//     the year (the firm's answer in Firm practices; the vendor's year to
//     date from its bills);
//   - a PAN that is missing or inoperative (s.397, was 206AA): the higher
//     rate, 20% (5% for purchases of goods);
//   - a lower or nil deduction certificate, and a no-deduction
//     declaration, as the vendor gave them (Compliance → Registers).
//
// Each is a warning on the bill, never a block: the person booking it
// decides. The quarterly statement needs the bills taxed under a
// certificate or declaration with their reason: TDSLowerCSV lists them.
//
// GST: the input tax credit taken on vendor bills is matched with GSTR-2B
// as downloaded from the GST portal (LoadGSTR2B), and credit on a bill
// unpaid 180 days after its date is to be reversed (CGST Act s.16(2),
// second proviso; rule 37). Both are self-inspection tests and the second
// also a work item.

var (
	lakh50      = money.MustAmount("5000000")
	tds194QRate = money.MustDec("0.1")
)

// vendorPANInoperative says whether the vendor's latest PAN check found
// it inoperative.
func (o *Ops) vendorPANInoperative(ctx context.Context, tenant, vendor string) bool {
	last := ""
	st := ""
	for _, r := range o.compRecs(ctx, tenant, "pan-status") {
		if r.V("vendor") == vendor && r.V("checked_on") >= last {
			last, st = r.V("checked_on"), r.V("status")
		}
	}
	return st == "Inoperative"
}

// noPANRate is the rate without an operative PAN.
func noPANRate(section string) string {
	if section == "194Q" {
		return "5"
	}
	return "20"
}

// vendorTDSWarnings are the TDS checks of a bill beyond its section's rate.
func (o *Ops) vendorTDSWarnings(ctx context.Context, tenant string, v Vendor, b VendorBill, all []VendorBill) []string {
	var w []string
	live := func(x VendorBill) bool {
		return x.Vendor == v.ID && x.Status != BillRejected && x.Status != BillCancelled
	}
	want := func(rate string, base money.Amount) money.Amount {
		r, err := money.ParseDec(rate)
		if err != nil {
			return -1
		}
		return base.Dec().Pct(r).Round(money.UnitRupee)
	}
	off := func(a, b money.Amount) bool { return a-b > 100 || b-a > 100 }
	sec := firstOf(b.TDSSection, v.TDSSection)

	// A no-deduction declaration for the year.
	fy := fyLabel(b.BillDate)
	for _, r := range o.compRecs(ctx, tenant, "tds-decl") {
		if r.V("vendor") == v.ID && r.V("fy") == fy && b.TDS > 0 {
			w = append(w, fmt.Sprintf("The vendor's no-deduction declaration %s for %s is on record: no TDS is expected unless it no longer holds.", r.V("ref"), fy))
		}
	}
	// A PAN not operative.
	if sec != "" && (v.PAN == "" || o.vendorPANInoperative(ctx, tenant, v.ID)) {
		why := "has no PAN"
		if v.PAN != "" {
			why = "has an inoperative PAN (last check)"
		}
		if x := want(noPANRate(sec), b.Taxable); b.TDS < x-100 {
			w = append(w, fmt.Sprintf("The vendor %s: TDS is at the higher rate of %s%%, %s (Income-tax Act, 2025 s.397, was 206AA); %s was entered.", why, noPANRate(sec), x, b.TDS))
		}
	}
	// A lower or nil deduction certificate in force.
	for _, r := range o.compRecs(ctx, tenant, "ldc") {
		if r.V("vendor") != v.ID || r.V("section") != sec || b.BillDate < r.V("from") || b.BillDate > r.V("to") {
			continue
		}
		limit, _ := money.ParseAmount(r.V("limit"))
		var used money.Amount
		for _, x := range all {
			if live(x) && firstOf(x.TDSSection, v.TDSSection) == sec && x.BillDate >= r.V("from") && x.BillDate <= r.V("to") {
				used += x.Taxable
			}
		}
		switch {
		case used >= limit:
			w = append(w, fmt.Sprintf("Lower deduction certificate %s is used up (₹%s of ₹%s): the usual rate applies.", r.V("cert_no"), formatINR(used.Dec()), formatINR(limit.Dec())))
		case used+b.Taxable > limit:
			w = append(w, fmt.Sprintf("Lower deduction certificate %s covers only ₹%s more: %s%% on that part, the usual rate on the rest.", r.V("cert_no"), formatINR((limit-used).Dec()), r.V("rate")))
		default:
			if x := want(r.V("rate"), b.Taxable); off(x, b.TDS) {
				w = append(w, fmt.Sprintf("Lower deduction certificate %s (%s%%, valid to %s): TDS would be %s; %s was entered.", r.V("cert_no"), r.V("rate"), fmtDay(r.V("to")), x, b.TDS))
			}
		}
	}
	// Purchases of goods.
	opt, vals := o.tcPractice(ctx, tenant, "tax-turnover")
	var prev money.Amount
	for _, x := range all {
		if live(x) && fyLabel(x.BillDate) == fy && x.BillDate <= b.BillDate {
			prev += x.Taxable
		}
	}
	cur := prev + b.Taxable
	if vals["fy"] != "" && vals["fy"] != fy && opt != "" {
		w = append(w, "The firm's turnover answer in Firm practices is for "+vals["fy"]+": answer it again for "+fy+".")
	}
	switch {
	case sec == "194Q" && opt == "below":
		if b.TDS > 0 {
			w = append(w, "The firm's turnover was ₹10 crore or less last year (Firm practices): TDS on purchases of goods does not apply.")
		}
	case sec == "194Q":
		over := cur - max(prev, lakh50)
		if over < 0 {
			over = 0
		}
		if x := over.Dec().Pct(tds194QRate).Round(money.UnitRupee); off(x, b.TDS) && !(v.PAN == "" || o.vendorPANInoperative(ctx, tenant, v.ID)) {
			w = append(w, fmt.Sprintf("Purchases of goods: 0.1%% only on what is bought from the seller beyond ₹50 lakh this year (₹%s before this bill): TDS would be %s; %s was entered.", formatINR(prev.Dec()), x, b.TDS))
		}
	case opt == "above" && b.TDSSection == "" && prev <= lakh50 && cur > lakh50:
		w = append(w, "Purchases from this vendor this year have crossed ₹50 lakh: if they are goods, TDS of 0.1% applies on the amount beyond ₹50 lakh (Income-tax Act, 2025 s.393(1) Table Sl. 8(ii), was 194Q).")
	}
	return w
}

// vendorTDSOverride says whether something other than the section's
// usual rate sets the bill's TDS: a certificate or declaration in force,
// no operative PAN, or the goods threshold.
func (o *Ops) vendorTDSOverride(ctx context.Context, tenant string, v Vendor, b VendorBill) bool {
	sec := firstOf(b.TDSSection, v.TDSSection)
	if sec == "194Q" || v.PAN == "" || o.vendorPANInoperative(ctx, tenant, v.ID) {
		return true
	}
	for _, r := range o.compRecs(ctx, tenant, "ldc") {
		if r.V("vendor") == v.ID && r.V("section") == sec && b.BillDate >= r.V("from") && b.BillDate <= r.V("to") {
			return true
		}
	}
	for _, r := range o.compRecs(ctx, tenant, "tds-decl") {
		if r.V("vendor") == v.ID && r.V("fy") == fyLabel(b.BillDate) {
			return true
		}
	}
	return false
}

// TDSLowerCSV lists the bills of a period taxed under a lower or nil
// deduction certificate or a no-deduction declaration, for the quarterly
// statement.
func (o *Ops) TDSLowerCSV(ctx context.Context, tenant, actor, from, to string) ([]byte, error) {
	if err := o.require(ctx, tenant, actor, "Viewing vendor bills", RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return nil, err
	}
	if !ledger.ValidDate(from) || !ledger.ValidDate(to) || from > to {
		return nil, invalid("give a period: from on or before to")
	}
	all, err := o.vendorBills(ctx, tenant)
	if err != nil {
		return nil, err
	}
	ldc, decl := o.compRecs(ctx, tenant, "ldc"), o.compRecs(ctx, tenant, "tds-decl")
	rows := [][]string{{"Bill", "Bill date", "Vendor", "Vendor PAN", "Section", "Amount paid or credited", "TDS", "Reason", "Certificate or declaration"}}
	vs := map[string]Vendor{}
	for _, b := range all {
		if b.BillDate < from || b.BillDate > to || b.Status == BillRejected || b.Status == BillCancelled {
			continue
		}
		v, ok := vs[b.Vendor]
		if !ok {
			v, _ = o.Vendor(ctx, tenant, b.Vendor)
			vs[b.Vendor] = v
		}
		sec := firstOf(b.TDSSection, v.TDSSection)
		reason, ref := "", ""
		for _, r := range ldc {
			if r.V("vendor") == v.ID && r.V("section") == sec && b.BillDate >= r.V("from") && b.BillDate <= r.V("to") {
				reason, ref = "Lower or nil deduction certificate", r.V("cert_no")
			}
		}
		for _, r := range decl {
			if r.V("vendor") == v.ID && r.V("fy") == fyLabel(b.BillDate) && b.TDS == 0 {
				reason, ref = "No deduction: self-declaration", r.V("ref")
			}
		}
		if reason == "" {
			continue
		}
		rows = append(rows, []string{b.ID, b.BillDate, v.Name, v.PAN, tdsRef(sec, b.BillDate), b.Taxable.String(), b.TDS.String(), reason, ref})
	}
	return CSV(rows), nil
}

// ---- TDS certificates ----

// tdsCertDue is the last day to give the quarter's certificate: 15 days
// after the quarterly statement's due date (31 July, 31 October, 31
// January, 31 May).
func tdsCertDue(fy, quarter string) string {
	y := 0
	fmt.Sscanf(fy, "%d", &y)
	switch quarter {
	case "Q1":
		return fmt.Sprintf("%d-08-15", y)
	case "Q2":
		return fmt.Sprintf("%d-11-15", y)
	case "Q3":
		return fmt.Sprintf("%d-02-15", y+1)
	case "Q4":
		return fmt.Sprintf("%d-06-15", y+1)
	}
	return ""
}

func quarterOf(d string) string {
	switch {
	case d[5:7] >= "04" && d[5:7] <= "06":
		return "Q1"
	case d[5:7] >= "07" && d[5:7] <= "09":
		return "Q2"
	case d[5:7] >= "10" && d[5:7] <= "12":
		return "Q3"
	}
	return "Q4"
}

// TDSCertDue is a certificate due to a payee.
type TDSCertDue struct {
	PayeeKind string       `json:"payee_kind"`
	Payee     string       `json:"payee"`
	Name      string       `json:"name"`
	FY        string       `json:"fy"`
	Quarter   string       `json:"quarter"`
	TDS       money.Amount `json:"tds"`
	Due       string       `json:"due"`
	Issued    string       `json:"issued,omitempty"` // the certificate number
}

// tdsCertsDue lists the certificates each payee with TDS in a quarter is
// owed (vendors from their bills, APs from posted payouts), with any given.
func (o *Ops) tdsCertsDue(ctx context.Context, tenant string) []TDSCertDue {
	by := map[string]*TDSCertDue{}
	add := func(kind, payee, name, date string, tds money.Amount) {
		if tds <= 0 || len(date) < 10 {
			return
		}
		fy, q := fyLabel(date), quarterOf(date)
		k := kind + "|" + payee + "|" + fy + "|" + q
		if by[k] == nil {
			by[k] = &TDSCertDue{PayeeKind: kind, Payee: payee, Name: name, FY: fy, Quarter: q, Due: tdsCertDue(fy, q)}
		}
		by[k].TDS += tds
	}
	bills, _ := o.vendorBills(ctx, tenant)
	for _, b := range bills {
		if b.Status != BillRejected && b.Status != BillCancelled {
			add("Vendor", b.Vendor, b.VendorName, b.BillDate, b.TDS)
		}
	}
	aps, _ := docs.List[APPayout](ctx, o.Docs, tenant, cAPPayout, "")
	for _, a := range aps {
		if a.Status == "Posted" {
			add("Authorised person", a.Partner, a.Name, a.To, a.TDS)
		}
	}
	for _, r := range o.compRecs(ctx, tenant, "tds-cert") {
		if x := by[r.V("payee_kind")+"|"+strings.ToUpper(r.V("payee"))+"|"+r.V("fy")+"|"+r.V("quarter")]; x != nil {
			x.Issued = r.V("cert_no")
		}
	}
	var out []TDSCertDue
	for _, k := range sortedKeys(by) {
		out = append(out, *by[k])
	}
	return out
}

// ---- GSTR-2B ----

const cGSTR2B = "gstr2b" // YYYY-MM -> GSTR2B

// GSTR2BRow is one B2B invoice in the firm's GSTR-2B.
type GSTR2BRow struct {
	GSTIN   string       `json:"gstin"`
	Name    string       `json:"name,omitempty"`
	Invoice string       `json:"invoice"`
	Date    string       `json:"date"`
	Taxable money.Amount `json:"taxable"`
	IGST    money.Amount `json:"igst"`
	CGST    money.Amount `json:"cgst"`
	SGST    money.Amount `json:"sgst"`
	// NoITC: the portal marks the credit not available (itcavl N).
	NoITC bool `json:"no_itc,omitempty"`
}

// GSTR2B is one month's statement as loaded.
type GSTR2B struct {
	Period string      `json:"period"`
	Rows   []GSTR2BRow `json:"rows"`
	File   string      `json:"file"`
	By     string      `json:"by"`
	At     string      `json:"at"`
}

// parseGSTR2BJSON reads the GST portal's GSTR-2B JSON download: data,
// docdata, b2b (one per supplier: ctin, trdnm, inv), each invoice with
// inum, dt (DD-MM-YYYY), itcavl and its items (txval, igst, cgst, sgst),
// or the tax amounts on the invoice itself. Amendments (b2ba) are read
// the same way.
func parseGSTR2BJSON(b []byte) ([]GSTR2BRow, error) {
	var f struct {
		Data struct {
			DocData map[string]json.RawMessage `json:"docdata"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	type item struct {
		Txval, Igst, Cgst, Sgst json.Number
	}
	type inv struct {
		Inum, Dt, Itcavl        string
		Txval, Igst, Cgst, Sgst json.Number
		Items                   []item
	}
	type sup struct {
		Ctin, Trdnm string
		Inv         []inv
	}
	amt := func(n json.Number) money.Amount {
		a, _ := money.ParseAmount(n.String())
		return a
	}
	var out []GSTR2BRow
	for _, sec := range []string{"b2b", "b2ba"} {
		raw, ok := f.Data.DocData[sec]
		if !ok {
			continue
		}
		var ss []sup
		if err := json.Unmarshal(raw, &ss); err != nil {
			return nil, fmt.Errorf("%s: %v", sec, err)
		}
		for _, s := range ss {
			for _, i := range s.Inv {
				r := GSTR2BRow{GSTIN: strings.ToUpper(s.Ctin), Name: s.Trdnm, Invoice: i.Inum, Date: anyDate(i.Dt), NoITC: strings.EqualFold(i.Itcavl, "N"),
					Taxable: amt(i.Txval), IGST: amt(i.Igst), CGST: amt(i.Cgst), SGST: amt(i.Sgst)}
				if len(i.Items) > 0 {
					r.Taxable, r.IGST, r.CGST, r.SGST = 0, 0, 0, 0
					for _, it := range i.Items {
						r.Taxable += amt(it.Txval)
						r.IGST += amt(it.Igst)
						r.CGST += amt(it.Cgst)
						r.SGST += amt(it.Sgst)
					}
				}
				out = append(out, r)
			}
		}
	}
	return out, nil
}

// parseGSTR2BCSV reads a CSV with a header naming the supplier's GSTIN,
// the invoice number and date, the taxable value and the taxes (as the
// portal's Excel sheet B2B has them, saved as CSV).
func parseGSTR2BCSV(b []byte) ([]GSTR2BRow, error) {
	cr := csv.NewReader(bytes.NewReader(b))
	cr.FieldsPerRecord, cr.LazyQuotes = -1, true
	col := map[string]int{}
	var out []GSTR2BRow
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(col) == 0 {
			for i, h := range rec {
				h = strings.ToLower(strings.TrimSpace(h))
				switch {
				case strings.Contains(h, "gstin"):
					col["gstin"] = i
				case strings.Contains(h, "trade") || strings.Contains(h, "legal name"):
					col["name"] = i
				case strings.Contains(h, "invoice number") || strings.Contains(h, "invoice no"):
					col["inv"] = i
				case strings.Contains(h, "invoice date"):
					col["date"] = i
				case strings.Contains(h, "taxable"):
					col["tx"] = i
				case strings.Contains(h, "integrated"):
					col["igst"] = i
				case strings.Contains(h, "central"):
					col["cgst"] = i
				case strings.Contains(h, "state"):
					col["sgst"] = i
				case strings.Contains(h, "itc availab"):
					col["itc"] = i
				}
			}
			if _, ok := col["gstin"]; !ok {
				col = map[string]int{}
				continue // a title line above the header
			}
			if _, ok := col["inv"]; !ok {
				return nil, invalid("the CSV has no invoice number column")
			}
			continue
		}
		g := func(k string) string {
			if i, ok := col[k]; ok && i < len(rec) {
				return strings.TrimSpace(rec[i])
			}
			return ""
		}
		a := func(k string) money.Amount {
			x, _ := money.ParseAmount(strings.ReplaceAll(g(k), ",", ""))
			return x
		}
		if g("gstin") == "" || g("inv") == "" {
			continue
		}
		out = append(out, GSTR2BRow{GSTIN: strings.ToUpper(g("gstin")), Name: g("name"), Invoice: g("inv"), Date: anyDate(g("date")),
			Taxable: a("tx"), IGST: a("igst"), CGST: a("cgst"), SGST: a("sgst"), NoITC: strings.EqualFold(g("itc"), "No")})
	}
	if len(col) == 0 {
		return nil, invalid("no header with the supplier's GSTIN found")
	}
	return out, nil
}

// LoadGSTR2B loads one month's GSTR-2B (the portal's JSON, or a CSV).
func (o *Ops) LoadGSTR2B(ctx context.Context, tenant, actor, period, file string, data []byte) (GSTR2B, error) {
	if err := o.require(ctx, tenant, actor, "Loading GSTR-2B", RoleChecker, RoleCompliance); err != nil {
		return GSTR2B{}, err
	}
	if !reMonth.MatchString(period) {
		return GSTR2B{}, invalid("give the return period as YYYY-MM")
	}
	var rows []GSTR2BRow
	var err error
	if t := bytes.TrimSpace(data); len(t) > 0 && t[0] == '{' {
		rows, err = parseGSTR2BJSON(t)
	} else {
		rows, err = parseGSTR2BCSV(data)
	}
	if err != nil {
		return GSTR2B{}, invalid("could not read the GSTR-2B file: %v", err)
	}
	g := GSTR2B{Period: period, Rows: rows, File: clip(file, 120), By: actor, At: o.now().Format(time.RFC3339)}
	if g.Rows == nil {
		g.Rows = []GSTR2BRow{}
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cGSTR2B, period, g, actor); err != nil {
		return g, err
	}
	o.log(ctx, tenant, actor, "gst.gstr2b_loaded", period, map[string]any{"invoices": len(rows)})
	return g, nil
}

// ITCMatchRow is one vendor bill's credit against GSTR-2B.
type ITCMatchRow struct {
	Bill     string       `json:"bill"`
	Vendor   string       `json:"vendor"`
	GSTIN    string       `json:"gstin"`
	BillNo   string       `json:"bill_no"`
	BillDate string       `json:"bill_date"`
	GST      money.Amount `json:"gst"`
	Period   string       `json:"period,omitempty"` // the 2B it is in
	Status   string       `json:"status"`           // In GSTR-2B, Not in GSTR-2B, Tax differs, Credit not available
	In2B     money.Amount `json:"gst_2b"`
}

// ITCMatch is the credit taken against the GSTR-2Bs loaded.
type ITCMatch struct {
	Periods  []string      `json:"periods"`
	Rows     []ITCMatchRow `json:"rows"`
	NotTaken []GSTR2BRow   `json:"not_taken"` // in 2B, no bill booked
}

func (o *Ops) itcMatch(ctx context.Context, tenant string) ITCMatch {
	m := ITCMatch{Periods: []string{}, Rows: []ITCMatchRow{}, NotTaken: []GSTR2BRow{}}
	gs, _ := docs.List[GSTR2B](ctx, o.Docs, tenant, cGSTR2B, "")
	type hit struct {
		r      GSTR2BRow
		period string
		used   bool
	}
	idx := map[string]*hit{}
	for _, g := range gs {
		m.Periods = append(m.Periods, g.Period)
		for _, r := range g.Rows {
			idx[r.GSTIN+"|"+billNoKey(r.Invoice)] = &hit{r: r, period: g.Period}
		}
	}
	sort.Strings(m.Periods)
	if len(m.Periods) == 0 {
		return m
	}
	bills, _ := o.vendorBills(ctx, tenant)
	for _, b := range bills {
		if !b.ITC || b.gst() == 0 || b.Status == BillRejected || b.Status == BillCancelled || b.RCM {
			continue
		}
		r := ITCMatchRow{Bill: b.ID, Vendor: b.VendorName, GSTIN: b.VendorGSTIN, BillNo: b.BillNo, BillDate: b.BillDate, GST: b.gst(), Status: "Not in GSTR-2B"}
		if h := idx[b.VendorGSTIN+"|"+billNoKey(b.BillNo)]; h != nil {
			h.used = true
			r.Period, r.In2B = h.period, h.r.IGST+h.r.CGST+h.r.SGST
			switch {
			case h.r.NoITC:
				r.Status = "Credit not available"
			case r.In2B-r.GST > 100 || r.GST-r.In2B > 100:
				r.Status = "Tax differs"
			default:
				r.Status = "In GSTR-2B"
			}
		}
		m.Rows = append(m.Rows, r)
	}
	for _, k := range sortedKeys(idx) {
		if !idx[k].used {
			m.NotTaken = append(m.NotTaken, idx[k].r)
		}
	}
	return m
}

// GSTR2BMatch is the match for the screen.
func (o *Ops) GSTR2BMatch(ctx context.Context, tenant, actor string) (ITCMatch, error) {
	if err := o.require(ctx, tenant, actor, "Viewing vendor bills", RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return ITCMatch{}, err
	}
	return o.itcMatch(ctx, tenant), nil
}

// ITC180Row is a bill whose credit is to be reversed: not paid 180 days
// after its date.
type ITC180Row struct {
	Bill    string       `json:"bill"`
	Vendor  string       `json:"vendor"`
	Date    string       `json:"bill_date"`
	Limit   string       `json:"limit"`
	Unpaid  money.Amount `json:"unpaid"`
	Reverse money.Amount `json:"reverse"` // the credit on the unpaid part
}

// itcUnpaid180 lists the bills with credit taken and some of the value not
// paid 180 days after the bill date, on date.
func (o *Ops) itcUnpaid180(ctx context.Context, tenant, date string) []ITC180Row {
	bills, _ := o.vendorBills(ctx, tenant)
	var out []ITC180Row
	for _, b := range bills {
		if !b.ITC || b.gst() == 0 || b.Status == BillRejected || b.Status == BillCancelled || b.RCM || b.Payable <= 0 {
			continue
		}
		lim := addDays(b.BillDate, 180)
		if lim > date || b.Paid >= b.Payable {
			continue
		}
		unpaid := b.Payable - b.Paid
		rev := money.Amount(int64(b.gst()) * int64(unpaid) / int64(b.Payable))
		out = append(out, ITC180Row{Bill: b.ID, Vendor: b.VendorName, Date: b.BillDate, Limit: lim, Unpaid: unpaid, Reverse: rev})
	}
	return out
}

// ITCUnpaid180 is the list for the screen.
func (o *Ops) ITCUnpaid180(ctx context.Context, tenant, actor, date string) ([]ITC180Row, error) {
	if err := o.require(ctx, tenant, actor, "Viewing vendor bills", RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return nil, err
	}
	rs := o.itcUnpaid180(ctx, tenant, firstOf(date, o.businessDate(ctx, tenant)))
	if rs == nil {
		rs = []ITC180Row{}
	}
	return rs, nil
}

// ---- stamp duty rates ----

// stampScheduleI is the Indian Stamp Act's Schedule I rate (%) on the
// buyer for each charge bucket (Finance Act, 2019; Indian Stamp
// (Collection of Stamp-Duty through Stock Exchanges, Clearing
// Corporations and Depositories) Rules, 2019): delivery 0.015%,
// non-delivery 0.003%, futures 0.002%, options 0.003% on the premium,
// currency and interest rate derivatives 0.0001%. Commodity derivatives
// are taken as futures and options.
var stampScheduleI = map[string]string{"CM-DEL": "0.015", "CM-INTRA": "0.003", "FO-FUT": "0.002", "FO-OPT": "0.003",
	"CD-FUT": "0.0001", "CD-OPT": "0.0001", "COM-FUT": "0.002", "COM-OPT": "0.003"}
