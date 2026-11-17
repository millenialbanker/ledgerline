package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/money"
	"ledgerline/internal/xfile"
)

// ---- the UCC-wise report of each running account settlement ----
//
// After each settlement of clients' running accounts the trading member
// reports, client by client, what it retained and what it paid, within 10
// trading days of the settlement date (NSE/INSP/55024 of 28 December 2022).
// The summary report due within 2 trading days was discontinued by
// NSE/INSP/61904 of 6 May 2024 (MSE/INSP/15322/2024); the UCC-wise report
// stays. It is uploaded on NSE's member portal (inspection.nseindia.com/
// MemberPortal: Inspection > Statement Upload > Settlement Statement >
// Submit Data > Submit UCC wise Details) as one CSV inside a ZIP of the same
// name, PAN_QS_DDMMYYYY_SeqNo: a name accepted once cannot be used again,
// and bank account numbers are put in double quotes so they are not rounded
// (NSE/INSP/55135 of 5 January 2023). A member of NSE and other exchanges
// files it with NSE only from 15 October 2025: NSE shares it (NSE/INSP/70505
// of 29 September 2025, item 26; MSE/INSP/17882/2025).
//
// Who is in it (NSE/INSP/56148 of 27 March 2023, Annexure 1): clients whose
// funds were neither retained nor paid are left out; clients whose funds
// were only retained have NA for the four bank fields; clients paid have
// both bank accounts, the firm's being one reported under enhanced
// supervision, now a DSCNBA. The totals must match what was retained and
// paid.
//
// The 17 fields are the revised format the exchanges applied from the
// settlement of 6 October 2023 (BSE notice 20230929-39; MSE/INSP/14235/2023
// of 3 October 2023; NCDEX/COMPLIANCE-100/2023 of 29 September 2023), as
// NCDEX's Master Circular - Member Inspection 2025-26 (NCDEX/MEMBER
// INSPECTION-013/2026 of 30 April 2026, Annexure A) lists them; the layout
// is config/formats/nse-ras-ucc.json.
//
// A late or missing report costs ₹10,000 a day; after a week no new client
// may be registered, and after two weeks the member is disabled in all
// segments until it reports (NSE/INSP/56261 of 3 April 2023).

const (
	cRASUCC       = "ras_ucc"     // what was made and filed for a settlement, by date
	wRASUCC       = "ras_ucc"     // the work items reminding of it
	rasUCCFormat  = "nse-ras-ucc" // config/formats
	rasUCCDays    = 10            // trading days after the settlement (NSE/INSP/55024)
	rasUCCPenalty = 10000         // ₹ a day late (NSE/INSP/56261)
)

// RASUCCFiled is an upload recorded, or a "not applicable" declaration.
type RASUCCFiled struct {
	Ref      string `json:"ref"`            // the portal's reference or acknowledgement
	File     string `json:"file,omitempty"` // the file uploaded
	On       string `json:"on"`             // the day it was uploaded or declared
	Late     bool   `json:"late"`
	DaysLate int    `json:"days_late,omitempty"` // calendar days after the due date
	Note     string `json:"note,omitempty"`
	By       string `json:"by"`
	At       string `json:"at"`
}

// RASUCCFile is one file made for a settlement.
type RASUCCFile struct {
	Seq          int       `json:"seq"`
	Name         string    `json:"name"`    // the ZIP
	CSV          string    `json:"csv"`     // the CSV inside it
	FileID       string    `json:"file_id"` // the kept ZIP
	Exchange     string    `json:"exchange"`
	Rows         int       `json:"rows"`
	PaidRows     int       `json:"paid_rows"`
	RetainedRows int       `json:"retained_rows"`
	Paid         money.Dec `json:"paid"`
	Retained     money.Dec `json:"retained"`
	Hash         string    `json:"hash"` // of the CSV, to tell when the settlement changed
	By           string    `json:"by"`
	At           string    `json:"at"`
}

// RASUCCRecord is what was made and filed for one settlement date.
type RASUCCRecord struct {
	Date          string        `json:"date"`
	Files         []RASUCCFile  `json:"files"`
	Uploads       []RASUCCFiled `json:"uploads"`
	NotApplicable *RASUCCFiled  `json:"not_applicable,omitempty"`
}

func (r RASUCCRecord) filed() *RASUCCFiled {
	if r.NotApplicable != nil {
		return r.NotApplicable
	}
	if len(r.Uploads) > 0 {
		return &r.Uploads[0]
	}
	return nil
}

// RASUCCRow is one client of the report: the file's fields by key (the
// spec's column keys) and what needs fixing.
type RASUCCRow struct {
	UCC      string            `json:"ucc"`
	Name     string            `json:"name"`
	Kind     string            `json:"kind"` // Paid, Retained only
	Values   map[string]string `json:"values"`
	Problems []string          `json:"problems,omitempty"`
}

// public masks the bank account numbers for the screen.
func (r RASUCCRow) public() RASUCCRow {
	v := map[string]string{}
	for k, x := range r.Values {
		if (k == "tm_account" || k == "client_account") && x != "NA" {
			x = MaskAccount(x)
		}
		v[k] = x
	}
	r.Values = v
	return r
}

// RASUCCSettlement is one settlement on the screen.
type RASUCCSettlement struct {
	Date           string       `json:"date"`
	Kind           string       `json:"kind"` // Monthly, Quarterly, Monthly and quarterly, Not in the calendar
	Window         []string     `json:"window,omitempty"`
	Due            string       `json:"due"`
	DaysLeft       int          `json:"days_left"` // trading days to the due date
	Settled        bool         `json:"settled"`   // a settlement run is recorded
	Batches        int          `json:"batches"`
	Paid           int          `json:"paid"`
	Retained       int          `json:"retained"`
	PaidAmount     money.Dec    `json:"paid_amount"`
	RetainedAmount money.Dec    `json:"retained_amount"`
	Held           int          `json:"held"`
	Status         string       `json:"status"` // Upcoming, To report, Overdue, Uploaded, Uploaded late, Not applicable, Not applicable (late)
	DaysLate       int          `json:"days_late,omitempty"`
	Penalty        money.Dec    `json:"penalty"`
	Escalation     string       `json:"escalation,omitempty"`
	CanNA          bool         `json:"can_na"`
	NextFile       string       `json:"next_file,omitempty"`
	Record         RASUCCRecord `json:"record"`
	Problems       []string     `json:"problems"`
}

// RASUCCView is the reports for the Running account screen.
type RASUCCView struct {
	Date        string             `json:"date"`
	Exchange    string             `json:"exchange"` // filed with
	FiledWith   string             `json:"filed_with"`
	Exchanges   []string           `json:"exchanges"`
	PAN         string             `json:"pan"`
	TMCode      string             `json:"tm_code"`
	Settlements []RASUCCSettlement `json:"settlements"`
	Fields      []string           `json:"fields"`
	Unconfirmed []string           `json:"unconfirmed"`
	Source      string             `json:"source"`
	Warnings    []string           `json:"warnings"`
	CanFile     bool               `json:"can_file"`
	CanRecord   bool               `json:"can_record"`
}

// rasUCCUnconfirmed is what the circulars found do not settle.
var rasUCCUnconfirmed = []string{
	"The header text of NSE's current template on the portal: the file uses the field names of the circular. Compare it with the template you download on the portal before the first upload.",
	"The date in the file name: the settlement date here. NSE/INSP/55135 gave the quarter-end date when settlement was quarterly.",
	"What fields 11 to 17 hold for a client whose funds were only retained: NA in field 11 here.",
	"The sequence number on the first file: every file here has one, from 01, so a corrected file never repeats a name the portal accepted. NSE/INSP/55135 names a single file PAN_QS_DDMMYYYY and adds _SeqNo, from 01, when more files are needed.",
}

// rasUCCExchange is the exchange the report is filed with: NSE when the
// firm is a member (it shares it with the other exchanges), else the first
// exchange the firm is a member of.
func rasUCCExchange(set Settings) (string, string) {
	exs := memberExchanges(set)
	switch {
	case contains(exs, "NSE") && len(exs) > 1:
		return "NSE", "NSE only: NSE shares it with " + strings.Join(without(exs, "NSE"), " and ") + " (NSE/INSP/70505, item 26, from 15 October 2025)"
	case contains(exs, "NSE"):
		return "NSE", "NSE"
	case len(exs) > 0:
		return exs[0], "Each exchange you are a member of: " + strings.Join(exs, ", ") + " (the same layout; check each exchange's portal for its file name)"
	}
	return "NSE", "NSE (no member codes in Settings yet)"
}

func without(xs []string, x string) []string {
	var out []string
	for _, y := range xs {
		if y != x {
			out = append(out, y)
		}
	}
	return out
}

// rasRetainedOn are the clients already recorded as settled by retention on
// a settlement date.
func (o *Ops) rasRetainedOn(ctx context.Context, tenant, date string) map[string]bool {
	out := map[string]bool{}
	bs, _ := o.RASBatches(ctx, tenant, date)
	for _, b := range bs {
		for _, it := range b.Retained {
			out[it.UCC] = true
		}
	}
	return out
}

// rasUCCDate is one settlement that is (or will be) reported.
type rasUCCDate struct {
	Date    string
	Kind    string
	Window  []string
	Due     string
	Batches []RASBatch
}

// rasUCCDates are the settlements from since (empty: from the first
// settlement or trade on record) up to date, and the next one after it: the
// exchanges' calendar dates the firm settles on, and any other day a
// settlement was run. A run on a day of a calendar date's window is that
// settlement. Each is due 10 trading days after the last day of its window.
func (o *Ops) rasUCCDates(ctx context.Context, tenant string, set Settings, since, date string) []rasUCCDate {
	prof := o.CompProfile(ctx, tenant)
	all, _ := o.RASBatches(ctx, tenant, "")
	byDate := map[string][]RASBatch{}
	for _, b := range all {
		byDate[b.Date] = append(byDate[b.Date], b)
	}
	if since == "" {
		since = o.rasUCCSince(ctx, tenant, all)
	}
	out := map[string]*rasUCCDate{}
	put := func(d, kind string, window []string, due string) {
		if u := out[d]; u != nil {
			if u.Kind != kind {
				u.Kind = "Monthly and quarterly"
			}
			return
		}
		out[d] = &rasUCCDate{Date: d, Kind: kind, Window: window, Due: due}
	}
	for _, r := range prof.RAS {
		due := addTradingDays(set, r.Last(), rasUCCDays)
		// The day the firm settles on, or the days of the window it ran a
		// settlement on.
		days := []string{}
		for _, x := range r.Dates {
			if len(byDate[x]) > 0 {
				days = append(days, x)
			}
		}
		if len(days) == 0 {
			days = []string{r.RunOn}
		}
		for _, d := range days {
			put(d, r.Kind, r.Dates, due)
		}
	}
	for d := range byDate {
		put(d, "Not in the calendar", nil, addTradingDays(set, d, rasUCCDays))
	}
	var list []rasUCCDate
	next := ""
	for _, d := range sortedKeys(out) {
		u := *out[d]
		u.Batches = byDate[d]
		if d > date {
			if next == "" {
				next = d
				list = append(list, u)
			}
			continue
		}
		if len(u.Batches) == 0 && (since == "" || d < since) {
			continue
		}
		list = append(list, u)
	}
	return list
}

// rasUCCSince is where reporting starts: the first settlement run or trade
// in Ledgerline (calendar dates before the firm's data are not chased).
func (o *Ops) rasUCCSince(ctx context.Context, tenant string, bs []RASBatch) string {
	first := ""
	for _, b := range bs {
		if first == "" || b.Date < first {
			first = b.Date
		}
	}
	if ts, err := o.App.Trades.List(ctx, tenant, ingest.FromExchange, ""); err == nil {
		for _, t := range ts {
			if first == "" || t.TradeDate < first {
				first = t.TradeDate
			}
		}
	}
	return first
}

func (o *Ops) rasUCCRecord(ctx context.Context, tenant, date string) RASUCCRecord {
	r, _, _ := docs.GetOr(ctx, o.Docs, tenant, cRASUCC, date, RASUCCRecord{Date: date})
	if r.Files == nil {
		r.Files = []RASUCCFile{}
	}
	if r.Uploads == nil {
		r.Uploads = []RASUCCFiled{}
	}
	return r
}

// rasUCCRows builds the report of a settlement from its batches.
func (o *Ops) rasUCCRows(ctx context.Context, tenant string, set Settings, date, exchange string, bs []RASBatch) ([]RASUCCRow, []string) {
	var problems []string
	tmCode, pan := strings.TrimSpace(set.MemberCodes[exchange]), firmPAN(set)
	if tmCode == "" {
		problems = append(problems, "No "+exchange+" member code in Settings: the file needs the TM code.")
	}
	if pan == "" {
		problems = append(problems, "No PAN of the firm in Settings (or GSTIN to take it from): the file and its name need it.")
	}
	reg := o.firmBanks(ctx, tenant)
	type acc struct {
		it       RASItem
		paid     money.Amount
		retained money.Dec
		from     string
		fromIFSC string
		onlyKept bool
	}
	sort.SliceStable(bs, func(i, j int) bool { return bs[i].At < bs[j].At })
	per := map[string]*acc{}
	held := 0
	for _, b := range bs {
		held += len(b.Held)
		from, ifsc := b.PaidFrom, b.PaidFromIFSC
		if from == "" {
			from, ifsc = o.rasPaidFrom(ctx, tenant)
		}
		if ifsc == "" {
			if fb, ok := firmBankByAccount(reg, from); ok {
				ifsc = fb.IFSC
			}
		}
		for _, it := range b.Items {
			a := per[it.UCC]
			if a == nil || a.onlyKept {
				a = &acc{it: it}
				per[it.UCC] = a
			}
			a.paid += it.Amount
			a.retained = money.Max(it.Retained, money.Dec{})
			a.from, a.fromIFSC = from, ifsc
		}
		for _, it := range b.Retained {
			if per[it.UCC] == nil {
				per[it.UCC] = &acc{it: it, retained: it.Credit, onlyKept: true}
			}
		}
	}
	if held > 0 {
		problems = append(problems, fmt.Sprintf("%d client%s due a payout %s held because the bank account is incomplete: not in the file until paid.", held, plural(held), map[bool]string{true: "is", false: "are"}[held == 1]))
	}
	sent := o.rasStatementsSent(ctx, tenant, date)
	intim := o.rasIntimations(ctx, tenant, date)
	ddmm := func(d string) string {
		if t, err := time.Parse("2006-01-02", d); err == nil {
			return t.Format("2006-01-02")
		}
		return ""
	}
	var rows []RASUCCRow
	for _, u := range sortedKeys(per) {
		a := per[u]
		r := RASUCCRow{UCC: u, Name: a.it.Name, Kind: "Paid", Values: map[string]string{
			"date": date, "tm_code": tmCode, "tm_pan": pan, "ucc": u,
			"retained": a.retained.Fixed(2), "paid": a.paid.Dec().Fixed(2),
		}}
		v := r.Values
		if a.onlyKept || a.paid <= 0 {
			r.Kind = "Retained only"
			// NSE/INSP/56148, Annexure 1, B: the bank details are NA.
			v["tm_account"], v["tm_ifsc"], v["client_account"], v["client_ifsc"] = "NA", "NA", "NA", "NA"
			v["electronic"] = "NA"
		} else {
			v["tm_account"], v["tm_ifsc"] = a.from, a.fromIFSC
			v["client_account"], v["client_ifsc"] = a.it.Account, a.it.IFSC
			v["electronic"] = "Yes" // Ledgerline pays by bank transfer only (NEFT, RTGS, IMPS)
			if a.from == "" {
				r.Problems = append(r.Problems, "the account the client was paid from is not known: add the DSCNBA on Receipts & payouts → Client money reconciliation → Firm bank accounts")
			} else if fb, ok := firmBankByAccount(reg, a.from); len(reg) > 0 && (!ok || fb.Kind != BankDSCNBA) {
				r.Problems = append(r.Problems, "paid from "+MaskAccount(a.from)+", not a DSCNBA in the firm's bank accounts")
			}
			if a.fromIFSC == "" {
				r.Problems = append(r.Problems, "no IFSC for the account paid from: add it on Receipts & payouts → Client money reconciliation → Firm bank accounts")
			}
			if a.it.Account == "" || a.it.IFSC == "" {
				r.Problems = append(r.Problems, "the client's bank account or IFSC is missing")
			}
		}
		v["not_electronic_reason"] = ""
		if d := sent[u]; d != "" {
			v["statement_sent"], v["statement_date"] = "Yes", ddmm(d)
		} else {
			v["statement_sent"], v["statement_date"] = "No", ""
		}
		mail, sms := intim[u+"|"+ChEmail], intim[u+"|"+ChSMS]
		if mail == "" && sent[u+"|email"] != "" {
			mail = sent[u+"|email"] // the statement's email tells the client what was paid
		}
		v["email_date"], v["sms_date"] = ddmm(mail), ddmm(sms)
		v["intimation_sent"] = map[bool]string{true: "Yes", false: "No"}[mail != "" || sms != ""]
		rows = append(rows, r)
	}
	return rows, problems
}

// rasStatementsSent are the settlement statements delivered for a date: the
// day each client's went (by UCC), and the day of the email (UCC|email).
func (o *Ops) rasStatementsSent(ctx context.Context, tenant, date string) map[string]string {
	out := map[string]string{}
	ds, _ := docs.List[Dispatch](ctx, o.Docs, tenant, cDispatch, "")
	for _, d := range ds {
		if d.Kind != DocRAS || d.Ref != date {
			continue
		}
		on := ""
		switch d.Status {
		case "Sent":
			on = dayOf(firstOf(d.SentAt, d.Created))
		case "Physical sent", "Physical pending":
			on = dayOf(firstOf(d.CourierAt, d.Created))
		default:
			continue
		}
		if on == "" {
			continue
		}
		if out[d.UCC] == "" || on < out[d.UCC] {
			out[d.UCC] = on
		}
		if d.Channel == "email" && d.Status == "Sent" && (out[d.UCC+"|email"] == "" || on < out[d.UCC+"|email"]) {
			out[d.UCC+"|email"] = on
		}
	}
	return out
}

// rasIntimations are the messages about a settlement sent to clients: the
// first day by email and by SMS (UCC|Email, UCC|SMS). Any event of the
// running account settlement counts ("ras." …).
func (o *Ops) rasIntimations(ctx context.Context, tenant, date string) map[string]string {
	out := map[string]string{}
	ms, _ := docs.List[Message](ctx, o.Docs, tenant, cMessage, "")
	for _, m := range ms {
		if !strings.HasPrefix(m.Event, "ras.") || m.Status != "Sent" {
			continue
		}
		on := dayOf(m.At)
		if on == "" || on < date {
			continue
		}
		k := m.UCC + "|" + m.Channel
		if out[k] == "" || on < out[k] {
			out[k] = on
		}
	}
	return out
}

// rasUCCSettlement fills in one settlement's line.
func (o *Ops) rasUCCSettlement(ctx context.Context, tenant string, set Settings, u rasUCCDate, exchange, today string) RASUCCSettlement {
	s := RASUCCSettlement{Date: u.Date, Kind: u.Kind, Window: u.Window, Due: u.Due, Batches: len(u.Batches), Settled: len(u.Batches) > 0, Problems: []string{}}
	s.Record = o.rasUCCRecord(ctx, tenant, u.Date)
	if s.Settled {
		rows, probs := o.rasUCCRows(ctx, tenant, set, u.Date, exchange, u.Batches)
		s.Problems = append(s.Problems, probs...)
		var pa, ra money.Dec
		for _, r := range rows {
			if r.Kind == "Paid" {
				s.Paid++
			} else {
				s.Retained++
			}
			pa, ra = pa.Add(dec(r.Values["paid"])), ra.Add(dec(r.Values["retained"]))
			for _, p := range r.Problems {
				s.Problems = append(s.Problems, r.UCC+": "+p)
			}
		}
		s.PaidAmount, s.RetainedAmount = pa, ra
		for _, b := range u.Batches {
			s.Held += len(b.Held)
		}
	}
	if today <= s.Due {
		s.DaysLeft = businessDaysBetween(set, today, s.Due)
	}
	f := s.Record.filed()
	switch {
	case s.Record.NotApplicable != nil:
		s.Status = "Not applicable"
		if f.Late {
			s.Status, s.DaysLate = "Not applicable (late)", f.DaysLate
		}
	case f != nil:
		s.Status = "Uploaded"
		if f.Late {
			s.Status, s.DaysLate = "Uploaded late", f.DaysLate
		}
	case u.Date > today:
		s.Status = "Upcoming"
	case today > s.Due:
		s.Status, s.DaysLate = "Overdue", daysBetween(s.Due, today)
	default:
		s.Status = "To report"
	}
	if s.DaysLate > 0 {
		s.Penalty = money.DecInt(int64(s.DaysLate * rasUCCPenalty))
		switch {
		case s.DaysLate > 14:
			s.Escalation = "Over two weeks late: NSE disables the member in all segments until it reports (NSE/INSP/56261)."
		case s.DaysLate > 7:
			s.Escalation = "Over a week late: no new client may be registered, and NSE gives seven days' notice of disabling trading (NSE/INSP/56261)."
		}
	}
	s.CanNA = !s.Settled && f == nil && u.Date <= today
	if s.Settled && f == nil {
		if lib, err := XFiles(); err == nil {
			if spec, ok := lib.Get(rasUCCFormat); ok {
				seq := o.peekSeq(ctx, tenant, rasUCCSeqName(set, u.Date)) + 1
				if n, err := spec.FileName(xfile.Vars{"member_pan": firmPAN(set), "date": u.Date, "seq": fmt.Sprint(seq)}); err == nil {
					s.NextFile = strings.TrimSuffix(n, ".csv") + ".zip"
				}
			}
		}
	}
	return s
}

func rasUCCSeqName(set Settings, date string) string {
	d := strings.ReplaceAll(date, "-", "")
	if len(d) == 8 {
		d = d[6:] + d[4:6] + d[:4] // DDMMYYYY
	}
	return "rasucc-" + firmPAN(set) + "-" + d
}

// peekSeq is a sequence's last number, without taking the next.
func (o *Ops) peekSeq(ctx context.Context, tenant, name string) int {
	s, _, err := docs.Get[seqDoc](ctx, o.Docs, tenant, cSeq, name)
	if err != nil {
		return 0
	}
	return int(s.N)
}

// RASUCCReports is every settlement's report for the screen.
func (o *Ops) RASUCCReports(ctx context.Context, tenant, actor, date string) (RASUCCView, error) {
	if err := o.require(ctx, tenant, actor, "Viewing the UCC-wise settlement reports", RoleMaker, RoleChecker, RoleCompliance, RoleRisk); err != nil {
		return RASUCCView{}, err
	}
	if !validDate(date) {
		date = o.businessDate(ctx, tenant)
	}
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return RASUCCView{}, err
	}
	v := RASUCCView{Date: date, Exchanges: memberExchanges(set), PAN: firmPAN(set), Settlements: []RASUCCSettlement{}, Unconfirmed: rasUCCUnconfirmed, Warnings: []string{}}
	if v.Exchanges == nil {
		v.Exchanges = []string{}
	}
	v.Exchange, v.FiledWith = rasUCCExchange(set)
	v.TMCode = set.MemberCodes[v.Exchange]
	if lib, err := XFiles(); err == nil {
		if spec, ok := lib.Get(rasUCCFormat); ok {
			for _, c := range spec.Columns {
				v.Fields = append(v.Fields, c.Name)
			}
			v.Source = spec.Source
		}
	}
	ds := o.rasUCCDates(ctx, tenant, set, "", date)
	for i := len(ds) - 1; i >= 0; i-- { // newest first
		v.Settlements = append(v.Settlements, o.rasUCCSettlement(ctx, tenant, set, ds[i], v.Exchange, date))
	}
	if v.PAN == "" {
		v.Warnings = append(v.Warnings, "Enter the firm's PAN in Settings: the file and its name need it.")
	}
	if v.TMCode == "" {
		v.Warnings = append(v.Warnings, "Enter the "+v.Exchange+" member code in Settings: the file needs the TM code.")
	}
	v.CanFile = o.require(ctx, tenant, actor, "Making the UCC-wise settlement report", RoleMaker, RoleChecker, RoleCompliance) == nil
	v.CanRecord = o.require(ctx, tenant, actor, "Recording the UCC-wise settlement report as filed", RoleChecker, RoleCompliance) == nil
	return v, nil
}

// RASUCCDetail is one settlement's report, client by client.
type RASUCCDetail struct {
	Settlement RASUCCSettlement `json:"settlement"`
	Exchange   string           `json:"exchange"`
	Fields     []string         `json:"fields"`
	Keys       []string         `json:"keys"`
	Rows       []RASUCCRow      `json:"rows"`
}

// RASUCCReport is one settlement's report with its rows (account numbers
// masked).
func (o *Ops) RASUCCReport(ctx context.Context, tenant, actor, date string) (RASUCCDetail, error) {
	if err := o.require(ctx, tenant, actor, "Viewing the UCC-wise settlement reports", RoleMaker, RoleChecker, RoleCompliance, RoleRisk); err != nil {
		return RASUCCDetail{}, err
	}
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return RASUCCDetail{}, err
	}
	u, ok := o.rasUCCDateOf(ctx, tenant, set, date)
	if !ok {
		return RASUCCDetail{}, fmt.Errorf("%w: no running account settlement on %s", ErrNotFound, date)
	}
	ex, _ := rasUCCExchange(set)
	d := RASUCCDetail{Settlement: o.rasUCCSettlement(ctx, tenant, set, u, ex, o.businessDate(ctx, tenant)), Exchange: ex, Rows: []RASUCCRow{}}
	if lib, err := XFiles(); err == nil {
		if spec, ok := lib.Get(rasUCCFormat); ok {
			for _, c := range spec.Columns {
				d.Fields, d.Keys = append(d.Fields, c.Name), append(d.Keys, c.Key)
			}
		}
	}
	rows, _ := o.rasUCCRows(ctx, tenant, set, date, ex, u.Batches)
	for _, r := range rows {
		d.Rows = append(d.Rows, r.public())
	}
	return d, nil
}

// rasUCCDateOf finds a settlement by its date (a calendar date or a day a
// settlement was run).
func (o *Ops) rasUCCDateOf(ctx context.Context, tenant string, set Settings, date string) (rasUCCDate, bool) {
	if !validDate(date) {
		return rasUCCDate{}, false
	}
	for _, u := range o.rasUCCDates(ctx, tenant, set, date, addDays(date, 1)) {
		if u.Date == date {
			return u, true
		}
	}
	return rasUCCDate{}, false
}

// PrepareRASUCC makes the report's file for a settlement: the CSV in a ZIP
// of the same name, with the next sequence number for the date (never one
// used before). With fresh false, the last file is given again when nothing
// in the settlement changed since it was made.
func (o *Ops) PrepareRASUCC(ctx context.Context, tenant, actor, date string, fresh bool) (RASUCCFile, error) {
	if err := o.require(ctx, tenant, actor, "Making the UCC-wise settlement report", RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return RASUCCFile{}, err
	}
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return RASUCCFile{}, err
	}
	u, ok := o.rasUCCDateOf(ctx, tenant, set, date)
	if !ok || len(u.Batches) == 0 {
		return RASUCCFile{}, invalid("no running account settlement is recorded for %s, so there is nothing to report; if no client was settled, record it as not applicable", fmtDay(date))
	}
	ex, _ := rasUCCExchange(set)
	rows, probs := o.rasUCCRows(ctx, tenant, set, date, ex, u.Batches)
	var stop []string
	for _, p := range probs {
		if strings.HasPrefix(p, "No ") {
			stop = append(stop, p)
		}
	}
	for _, r := range rows {
		for _, p := range r.Problems {
			if strings.Contains(p, "not known") || strings.Contains(p, "missing") || strings.Contains(p, "no IFSC") {
				stop = append(stop, r.UCC+": "+p)
			}
		}
	}
	if len(stop) > 0 {
		if len(stop) > 5 {
			stop = append(stop[:5], fmt.Sprintf("and %d more", len(stop)-5))
		}
		return RASUCCFile{}, invalid("the file cannot be made yet: %s", strings.Join(stop, "; "))
	}
	if len(rows) == 0 {
		return RASUCCFile{}, invalid("no client was paid or retained on %s: record it as not applicable instead", fmtDay(date))
	}
	lib, err := XFiles()
	if err != nil {
		return RASUCCFile{}, err
	}
	spec, ok := lib.Get(rasUCCFormat)
	if !ok {
		return RASUCCFile{}, fmt.Errorf("%w: file layout %s", ErrNotFound, rasUCCFormat)
	}
	var xr []xfile.Row
	f := RASUCCFile{Exchange: ex, Rows: len(rows), By: actor, At: o.now().Format(time.RFC3339)}
	for _, r := range rows {
		xr = append(xr, xfile.Row{Values: r.Values})
		if r.Kind == "Paid" {
			f.PaidRows++
		} else {
			f.RetainedRows++
		}
		f.Paid, f.Retained = f.Paid.Add(dec(r.Values["paid"])), f.Retained.Add(dec(r.Values["retained"]))
	}
	content, err := spec.Write(xr)
	if err != nil {
		return RASUCCFile{}, invalid("%v", err)
	}
	f.Hash = sha256Hex(append([]byte(ex+"|"), content...))
	rec := o.rasUCCRecord(ctx, tenant, date)
	if n := len(rec.Files); !fresh && n > 0 && rec.Files[n-1].Hash == f.Hash {
		return rec.Files[n-1], nil
	}
	seq, err := o.next(ctx, tenant, rasUCCSeqName(set, date), actor)
	if err != nil {
		return RASUCCFile{}, err
	}
	f.Seq = int(seq)
	f.CSV, err = spec.FileName(xfile.Vars{"member_pan": firmPAN(set), "date": date, "seq": fmt.Sprint(seq)})
	if err != nil {
		return RASUCCFile{}, invalid("%v", err)
	}
	var zipped []byte
	f.Name, zipped, err = spec.Pack(f.CSV, content)
	if err != nil {
		return RASUCCFile{}, err
	}
	kept, err := o.keepFile(ctx, tenant, f.Name, zipped)
	if err != nil {
		return RASUCCFile{}, err
	}
	f.FileID = kept.ID
	rec.Files = append(rec.Files, f)
	if err := docs.Upsert(ctx, o.Docs, tenant, cRASUCC, date, rec, actor); err != nil {
		return RASUCCFile{}, err
	}
	o.log(ctx, tenant, actor, "ras.ucc.file", date, map[string]any{"file": f.Name, "rows": f.Rows, "paid": f.Paid.Fixed(2), "retained": f.Retained.Fixed(2)})
	return f, nil
}

// RASUCCFileData is a file made for a settlement (seq 0: the last), as the
// ZIP for the portal or, with csv, the CSV inside it.
func (o *Ops) RASUCCFileData(ctx context.Context, tenant, actor, date string, seq int, csv bool) (string, []byte, error) {
	if err := o.require(ctx, tenant, actor, "Making the UCC-wise settlement report", RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return "", nil, err
	}
	rec := o.rasUCCRecord(ctx, tenant, date)
	var f *RASUCCFile
	for i := range rec.Files {
		if seq == 0 || rec.Files[i].Seq == seq {
			f = &rec.Files[i]
		}
	}
	if f == nil {
		return "", nil, fmt.Errorf("%w: no file made for the settlement of %s", ErrNotFound, date)
	}
	raw, err := o.App.Ingest.Raw.Get(ctx, tenant, f.FileID)
	if err != nil {
		return "", nil, err
	}
	o.log(ctx, tenant, actor, "ras.ucc.download", date, map[string]any{"file": f.Name})
	if !csv {
		return f.Name, raw.Content, nil
	}
	fs, err := xfile.Unpack(f.Name, raw.Content)
	if err != nil || len(fs) != 1 {
		return "", nil, fmt.Errorf("the kept file %s cannot be read: %v", f.Name, err)
	}
	return fs[0].Name, fs[0].Data, nil
}

// RASUCCFileReq records the report as uploaded, or (NotApplicable) that no
// client was settled.
type RASUCCFileReq struct {
	Ref           string `json:"ref"`
	File          string `json:"file,omitempty"`
	On            string `json:"on"`
	Note          string `json:"note,omitempty"`
	NotApplicable bool   `json:"not_applicable,omitempty"`
}

// RecordRASUCC records the report as uploaded on the portal, with the
// portal's reference, who and when; late when after the due date. With
// NotApplicable it records the portal's not-applicable declaration, which
// is allowed only when no client was settled.
func (o *Ops) RecordRASUCC(ctx context.Context, tenant, actor, date string, req RASUCCFileReq) (RASUCCRecord, error) {
	if err := o.require(ctx, tenant, actor, "Recording the UCC-wise settlement report as filed", RoleChecker, RoleCompliance); err != nil {
		return RASUCCRecord{}, err
	}
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return RASUCCRecord{}, err
	}
	u, ok := o.rasUCCDateOf(ctx, tenant, set, date)
	if !ok {
		return RASUCCRecord{}, fmt.Errorf("%w: no running account settlement on %s", ErrNotFound, date)
	}
	today := o.now().Format("2006-01-02")
	req.Ref, req.File, req.Note = clip(strings.TrimSpace(req.Ref), 80), strings.TrimSpace(req.File), clip(strings.TrimSpace(req.Note), 300)
	if req.On == "" {
		req.On = today
	}
	switch {
	case req.Ref == "":
		return RASUCCRecord{}, invalid("give the portal's reference or acknowledgement number")
	case !validDate(req.On) || req.On < date || req.On > today:
		return RASUCCRecord{}, invalid("give the day it was done: on or after %s and not after today", fmtDay(date))
	}
	rec := o.rasUCCRecord(ctx, tenant, date)
	if rec.NotApplicable != nil {
		return rec, fmt.Errorf("%w: the settlement of %s is already recorded as not applicable", ErrState, fmtDay(date))
	}
	f := RASUCCFiled{Ref: req.Ref, On: req.On, Note: req.Note, By: actor, At: o.now().Format(time.RFC3339), Late: req.On > u.Due}
	if f.Late {
		f.DaysLate = daysBetween(u.Due, req.On)
	}
	if req.NotApplicable {
		if len(u.Batches) > 0 {
			return rec, invalid("clients were settled on %s: upload the report instead", fmtDay(date))
		}
		if len(rec.Uploads) > 0 {
			return rec, fmt.Errorf("%w: a report for %s is already recorded as uploaded", ErrState, fmtDay(date))
		}
		if v, err := o.RAS(ctx, tenant, date); err == nil {
			n := 0
			for _, r := range v.Rows {
				if r.dueIn(date) && r.Credit.Sign() > 0 {
					n++
				}
			}
			if n > 0 {
				return rec, invalid("%d client%s had a credit balance to settle on %s: settle %s on Running account first", n, plural(n), fmtDay(date), map[bool]string{true: "it", false: "them"}[n == 1])
			}
		}
		rec.NotApplicable = &f
	} else {
		if f.File == "" && len(rec.Files) > 0 {
			f.File = rec.Files[len(rec.Files)-1].Name
		}
		if f.File == "" {
			return rec, invalid("make the file first (or give the name of the file uploaded)")
		}
		rec.Uploads = append(rec.Uploads, f)
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cRASUCC, date, rec, actor); err != nil {
		return rec, err
	}
	what := "ras.ucc.uploaded"
	if req.NotApplicable {
		what = "ras.ucc.not_applicable"
	}
	o.log(ctx, tenant, actor, what, date, map[string]any{"ref": f.Ref, "file": f.File, "on": f.On, "late": f.Late, "days_late": f.DaysLate})
	o.closeWork(ctx, tenant, actor, wRASUCC, date, "recorded as "+map[bool]string{true: "not applicable", false: "uploaded"}[req.NotApplicable]+" ("+f.Ref+")")
	return rec, nil
}

// rasUCCQueue reminds the compliance officer to file a settlement's report;
// the item stays open until the upload (or not applicable) is recorded.
func (o *Ops) rasUCCQueue(ctx context.Context, tenant, date string) {
	set, _ := o.Settings(ctx, tenant)
	u, ok := o.rasUCCDateOf(ctx, tenant, set, date)
	if !ok || o.rasUCCRecord(ctx, tenant, date).filed() != nil {
		return
	}
	ex, _ := rasUCCExchange(set)
	title := fmt.Sprintf("Upload the UCC-wise settlement report for %s to %s by %s", fmtDay(date), ex, fmtDay(u.Due))
	detail := fmt.Sprintf("Make the file on Running account, upload it on the member portal (Inspection > Statement Upload > Settlement Statement > Submit UCC wise Details) and record the portal's reference. It is due within %d trading days of the settlement; ₹10,000 a day after that (NSE/INSP/56261).", rasUCCDays)
	if len(u.Batches) == 0 {
		title = fmt.Sprintf("Report the settlement of %s to %s by %s", fmtDay(date), ex, fmtDay(u.Due))
		detail = "No settlement run is recorded for this date. Settle the clients due on Running account, or, if no client had funds to settle, record the portal's not-applicable declaration there."
	}
	rc := &runCtx{o: o, ctx: ctx, tenant: tenant, date: o.businessDate(ctx, tenant), process: wRASUCC, keys: map[string]bool{}}
	rc.queue(date, title, detail, "", RoleCompliance, "ras", u.Due+" 18:00", nil)
}

// ---- Periodic work: ras_ucc_report ----

// rasUCCPeriod is the settlement the task works on: the earliest one not
// filed, else the next one coming.
func (o *Ops) rasUCCPeriod(ctx context.Context, tenant, date string) (string, string, string) {
	set, _ := o.Settings(ctx, tenant)
	ds := o.rasUCCDates(ctx, tenant, set, "", date)
	for _, u := range ds {
		if u.Date <= date && o.rasUCCRecord(ctx, tenant, u.Date).filed() == nil {
			return u.Date, "Settlement of " + fmtDay(u.Date), u.Due
		}
	}
	for _, u := range ds {
		if u.Date > date {
			return u.Date, "Settlement of " + fmtDay(u.Date), u.Due
		}
	}
	return "", "", ""
}

func (o *Ops) rasUCCDone(ctx context.Context, tenant, period string) (bool, string) {
	rec := o.rasUCCRecord(ctx, tenant, period)
	f := rec.filed()
	if f == nil {
		return false, ""
	}
	what := "Uploaded"
	if rec.NotApplicable != nil {
		what = "Not applicable"
	}
	late := ""
	if f.Late {
		late = fmt.Sprintf(", %d days late", f.DaysLate)
	}
	return true, fmt.Sprintf("%s on %s (reference %s%s)", what, fmtDay(f.On), f.Ref, late)
}

// rasUCCRun makes the file of a settlement that has been run and keeps the
// reminder open; the upload itself is a person's.
func (o *Ops) rasUCCRun(ctx context.Context, tenant, actor, period, date string) (string, []PeriodicFile, error) {
	if period > date {
		return "", nil, invalid("the settlement of %s has not come", fmtDay(period))
	}
	set, _ := o.Settings(ctx, tenant)
	u, ok := o.rasUCCDateOf(ctx, tenant, set, period)
	if !ok {
		return "", nil, fmt.Errorf("%w: no settlement on %s", ErrNotFound, period)
	}
	o.rasUCCQueue(ctx, tenant, period)
	if len(u.Batches) == 0 {
		return "", nil, &pendingErr{"No settlement run is recorded: settle the clients due, or record not applicable on Running account", "Waiting for the settlement"}
	}
	f, err := o.PrepareRASUCC(ctx, tenant, actor, period, false)
	if err != nil {
		return "", nil, err
	}
	return "", []PeriodicFile{{ID: f.FileID, Name: f.Name}}, &pendingErr{fmt.Sprintf("%s made (%d clients); upload it on the member portal by %s and record the reference on Running account", f.Name, f.Rows, fmtDay(u.Due)), "Waiting for upload"}
}

// ---- self-inspection: ras_ucc_report ----

// siRASUCC checks every settlement whose 10 trading days have passed was
// reported (or declared not applicable) by its due date.
func siRASUCC(e *siEnv) (siOut, error) {
	var s siOut
	s.input("Running account settlements", e.date)
	s.input("UCC-wise reports filed", e.date)
	for _, u := range e.o.rasUCCDates(e.ctx, e.tenant, e.set, "", e.date) {
		if u.Due >= e.date {
			continue
		}
		s.checked++
		f := e.o.rasUCCRecord(e.ctx, e.tenant, u.Date).filed()
		switch {
		case f == nil:
			s.bad(fmt.Sprintf("Settlement of %s · not reported (due %s)", fmtDay(u.Date), fmtDay(u.Due)))
		case f.Late:
			s.bad(fmt.Sprintf("Settlement of %s · reported %s, %d days after the due date %s", fmtDay(u.Date), fmtDay(f.On), f.DaysLate, fmtDay(u.Due)))
		}
	}
	if s.checked == 0 {
		return siSkip("Nothing to check yet: no settlement's 10 trading days have passed.", s.inputs...)
	}
	s.summary = fmt.Sprintf("%d settlements past their 10 trading days; %d not reported on time.", s.checked, s.found)
	return s, nil
}
