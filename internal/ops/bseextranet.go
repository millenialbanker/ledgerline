package ops

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
	"ledgerline/internal/xfile"
)

// ---- BSE extranet: member reports ----
//
// The daily reports BSE puts in the member's extranet folder, besides the
// trade, stamp duty and bhavcopy files (bsefiles.go):
//
//   - UCCs not permitted to trade (EQX cash, EQD derivatives), with the
//     reason; a work item per client says what to fix. BSEUCCBlocked gives
//     the reasons per client.
//   - UCCs permitted to trade, compared with Ledgerline's active clients.
//   - Order charges and turnover / transaction charges; the transaction
//     charges are reconciled with those billed on BSE trades.
//   - The clearing house account statement (VA<DDMM>), kept as BSE
//     clearing ledger entries with a reminder to reconcile within 15 days.
//   - PANs debarred by SEBI (DEBR): added to the debarred list, active
//     clients blocked (flagged only for scrip-specific orders).
//   - Mobile and email exceptions (NOME), bulk deals (BULK) and the order
//     log (BO): kept for compliance and surveillance.
//   - The demat-check responses to the UCC upload (CDSL_/NSDL_UCCMSTR):
//     kept per client as received.
//
// The bulk deal, order log and demat response layouts are provisional
// (their specs say so) until BSE's own descriptions are in hand.

const (
	cBSENotPermitted = "bsenotpermitted" // date|seg: not-permitted list
	cBSEUCCList      = "bseuccstat"      // date|seg: permitted list exceptions
	cBSEOrdChg       = "bseordchg"       // YYYY-MM
	cBSETRC          = "bsetrc"          // date
	cBSEClrStmt      = "bseclrstmt"      // date
	cBSEDebr         = "bsedebr"         // date
	cBSEContact      = "bsecontact"      // date
	cBSEBulk         = "bsebulk"         // date
	cBSEOrderLog     = "bseorderlog"     // date
	cBSEUCCDemat     = "bseuccdemat"     // ucc|depository

	wBSEUCCBlocked = "bse-ucc-blocked"
	wBSEUCCStatus  = "bse-ucc-status"
	wBSETxn        = "bse-txn-charges"
	wBSEClrStmt    = "bse-clearing-statement"
	wBSEDebarred   = "bse-debarred"
	wBSEContact    = "bse-contact"

	// DebarredReason is the block reason for clients on BSE's DEBR report.
	DebarredReason = "SEBI debarred (BSE DEBR report)"
)

var (
	reBSEDDMMYY = regexp.MustCompile(`(?i)^[A-Z]+_?([0-3]\d)(0[1-9]|1[0-2])(\d\d)\.`)
	reBSEDDMM   = regexp.MustCompile(`(?i)^VA([0-3]\d)(0[1-9]|1[0-2])\.`)
	reBSESeq    = regexp.MustCompile(`_(\d+)\.csv$`)
	reAmount    = regexp.MustCompile(`^-?[\d,]*\d\.\d+(DR|CR|Dr|Cr)?$`)
)

// bseFileDate is the date a BSE file is for, from its name: YYYYMMDD or
// DDMMYYYY, DDMMYY after the report's letters (DEBR161026.923), or DDMM
// for the clearing statement (the latest such day not after today).
func bseFileDate(name string, now time.Time) string {
	if d := ymdFromName(name); d != "" {
		return d
	}
	base := name[strings.LastIndexAny(name, `/\`)+1:]
	if m := reBSEDDMMYY.FindStringSubmatch(base); m != nil {
		if d := "20" + m[3] + "-" + m[2] + "-" + m[1]; validDate(d) {
			return d
		}
	}
	if m := regexp.MustCompile(`_([0-3]\d)(0[1-9]|1[0-2])(\d\d)\.[A-Za-z]+$`).FindStringSubmatch(base); m != nil {
		if d := "20" + m[3] + "-" + m[2] + "-" + m[1]; validDate(d) {
			return d
		}
	}
	if m := reBSEDDMM.FindStringSubmatch(base); m != nil {
		y := now.In(ist).Year()
		d := fmt.Sprintf("%d-%s-%s", y, m[2], m[1])
		if d > now.In(ist).Format("2006-01-02") {
			d = fmt.Sprintf("%d-%s-%s", y-1, m[2], m[1])
		}
		if validDate(d) {
			return d
		}
	}
	return ""
}

func bseSegOf(name string) string {
	if strings.HasPrefix(strings.ToUpper(name[strings.LastIndexAny(name, `/\`)+1:]), "EQD") {
		return "FO"
	}
	return "CM"
}

func bseSeq(name string) int {
	if m := reBSESeq.FindStringSubmatch(name); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}

// workDue opens (or refreshes) a work item with a due time.
func (o *Ops) workDue(ctx context.Context, tenant, process, key, title, detail, ucc, role, view, date, due string) {
	rc := &runCtx{o: o, ctx: ctx, tenant: tenant, date: firstOf(date, o.now().Format("2006-01-02")), process: process, keys: map[string]bool{}}
	rc.queue(key, title, detail, ucc, role, view, due, nil)
}

// loadBSEExtranet handles the member reports above; ok is false for others.
func (o *Ops) loadBSEExtranet(ctx context.Context, tenant, actor string, s *xfile.Spec, f xfile.File, rows []xfile.Row, r *XFileResult) bool {
	fail := func(err error) { r.Loaded = "Not loaded: " + strings.TrimPrefix(err.Error(), "invalid: ") }
	date := bseFileDate(f.Name, o.now())
	if date == "" && s.ID != "bse-ucc-demat-cdsl" && s.ID != "bse-ucc-demat-nsdl" {
		fail(fmt.Errorf("the date is not in the file name"))
		return true
	}
	var err error
	switch s.ID {
	case "bse-ucc-not-permitted":
		err = o.loadBSENotPermitted(ctx, tenant, actor, date, f.Name, rows, r)
	case "bse-ucc-trading-status":
		err = o.loadBSETradingStatus(ctx, tenant, actor, date, f.Name, rows, r)
	case "bse-order-charges":
		err = o.loadBSEOrderCharges(ctx, tenant, actor, f.Name, rows, r)
	case "bse-turnover-trc":
		err = o.loadBSETRC(ctx, tenant, actor, date, f.Name, rows, r)
	case "bse-clearing-statement":
		err = o.loadBSEClearingStmt(ctx, tenant, actor, date, f.Name, rows, r)
	case "bse-debarred-pan":
		err = o.loadBSEDebarred(ctx, tenant, actor, date, f.Name, rows, r)
	case "bse-contact-exceptions":
		err = o.loadBSEContact(ctx, tenant, actor, date, f.Name, rows, r)
	case "bse-bulk-deals":
		err = o.loadBSEBulk(ctx, tenant, actor, date, f.Name, rows, r)
	case "bse-order-log":
		err = o.loadBSEOrderLog(ctx, tenant, actor, date, f.Name, rows, r)
	case "bse-ucc-demat-cdsl", "bse-ucc-demat-nsdl":
		err = o.loadBSEUCCDemat(ctx, tenant, actor, firstOf(date, o.now().In(ist).Format("2006-01-02")), strings.ToUpper(strings.TrimPrefix(s.ID, "bse-ucc-demat-")), f.Name, rows, r)
	default:
		return false
	}
	if err != nil {
		fail(err)
	}
	return true
}

// ---- UCCs not permitted to trade ----

// BSENotPermittedList is the latest not-permitted list of a date and segment.
type BSENotPermittedList struct {
	ID      string              `json:"id"`
	Date    string              `json:"date"`
	Segment string              `json:"segment"`
	File    string              `json:"file"`
	Seq     int                 `json:"seq"`
	Clients map[string][]string `json:"clients"` // ucc -> reasons in plain English, each with the fix
	BSE     map[string][]string `json:"bse"`     // ucc -> BSE's own text
}

// bseNotPermittedWhy explains a not-permitted column and what to do.
var bseNotPermittedWhy = map[string][2]string{
	"IsActiveStatus":        {"the client is not active at BSE", "upload the client's UCC to BSE again with status Active"},
	"Approved":              {"the client's UCC is not approved at BSE", "check BSE's response to the UCC upload, correct the rejected details and upload again"},
	"PAN Validation Status": {"the PAN did not verify with the Income Tax database", "check the PAN, name and date of birth against the PAN card and upload the UCC again"},
	"PAN Status":            {"the KRA holds the PAN as invalid", "re-validate the PAN at the KRA (an inoperative PAN must first be linked with Aadhaar)"},
	"KRA Status":            {"the client's KYC is not validated at the KRA", "complete or re-validate the client's KYC at the KRA"},
}

func bseWhy(col string) (string, string) {
	if w, ok := bseNotPermittedWhy[col]; ok {
		return w[0], w[1]
	}
	if strings.HasPrefix(col, "PrimaryOrSecondaryDP") {
		n := strings.TrimSuffix(strings.TrimPrefix(col, "PrimaryOrSecondaryDP"), "Status")
		return "demat account " + n + " (primary or secondary DP) is pending or invalid", "correct the DP ID and client ID of demat account " + n + " in the UCC and upload again"
	}
	return "BSE does not permit the client to trade", "check the client's UCC at BSE"
}

var segWord = map[string]string{"CM": "Cash", "FO": "Derivatives"}

func (o *Ops) loadBSENotPermitted(ctx context.Context, tenant, actor, date, name string, rows []xfile.Row, r *XFileResult) error {
	seg, seq := bseSeg(name), bseSeq(name)
	id := date + "|" + seg
	if old, _, err := docs.Get[BSENotPermittedList](ctx, o.Docs, tenant, cBSENotPermitted, id); err == nil && old.Seq > seq {
		r.Loaded = fmt.Sprintf("Not used: run %02d of the day is already loaded", old.Seq)
		return nil
	}
	run := BSENotPermittedList{ID: id, Date: date, Segment: seg, File: name, Seq: seq, Clients: map[string][]string{}, BSE: map[string][]string{}}
	cols := []string{"IsActiveStatus", "Approved", "PAN Validation Status", "PAN Status", "KRA Status"}
	for i := 1; i <= 5; i++ {
		cols = append(cols, fmt.Sprintf("PrimaryOrSecondaryDP%dStatus", i))
	}
	for _, row := range rows {
		ucc := strings.ToUpper(strings.TrimSpace(row.Get("UCC")))
		if ucc == "" {
			continue
		}
		for _, c := range cols {
			v := strings.TrimSpace(row.Get(c))
			if v == "" {
				continue
			}
			why, fix := bseWhy(c)
			run.Clients[ucc] = append(run.Clients[ucc], segWord[seg]+": "+why+"; to fix, "+fix+".")
			run.BSE[ucc] = append(run.BSE[ucc], v)
		}
		if _, ok := run.Clients[ucc]; !ok {
			run.Clients[ucc] = []string{segWord[seg] + ": BSE does not permit the client to trade (no reason given); check the client's UCC at BSE."}
		}
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cBSENotPermitted, id, run, actor); err != nil {
		return err
	}
	o.syncBSEUCCWork(ctx, tenant, actor, date)
	r.Loaded, r.Added = fmt.Sprintf("Loaded as clients BSE does not permit to trade (%s, run %02d); a work item per client says what to fix", segWord[seg], seq), len(run.Clients)
	return nil
}

func bseSeg(name string) string { return bseSegOf(name) }

// BSEUCCBlocked are the reasons BSE does not permit each client to trade,
// from the latest not-permitted list of each segment.
func (o *Ops) BSEUCCBlocked(ctx context.Context, tenant string) map[string][]string {
	all, _ := docs.List[BSENotPermittedList](ctx, o.Docs, tenant, cBSENotPermitted, "")
	latest := map[string]BSENotPermittedList{}
	for _, x := range all {
		if x.Date > latest[x.Segment].Date {
			latest[x.Segment] = x
		}
	}
	out := map[string][]string{}
	for _, seg := range []string{"CM", "FO"} {
		for u, rs := range latest[seg].Clients {
			out[u] = append(out[u], rs...)
		}
	}
	return out
}

// syncBSEUCCWork raises a work item per blocked client and closes those of
// clients no longer on any list.
func (o *Ops) syncBSEUCCWork(ctx context.Context, tenant, actor, date string) {
	bl := o.BSEUCCBlocked(ctx, tenant)
	cs, _ := o.ClientMap(ctx, tenant)
	for _, u := range sortedKeys(bl) {
		name := u
		if c, ok := cs[u]; ok {
			name = c.Name + " (" + u + ")"
		}
		o.raiseWork(ctx, tenant, wBSEUCCBlocked, u, "BSE does not permit "+u+" to trade", name+": "+strings.Join(bl[u], " "), u, RoleMaker, "clients", date)
	}
	ws, _ := docs.List[WorkItem](ctx, o.Docs, tenant, cWorkItem, "")
	for _, w := range ws {
		if w.Process == wBSEUCCBlocked && w.Status == "Open" {
			if _, still := bl[w.UCC]; !still {
				o.closeWork(ctx, tenant, actor, wBSEUCCBlocked, w.UCC, "No longer on BSE's not-permitted list")
			}
		}
	}
}

// ---- UCCs permitted to trade ----

// BSETradingStatus compares BSE's permitted list with Ledgerline's clients.
type BSETradingStatus struct {
	ID       string   `json:"id"`
	Date     string   `json:"date"`
	Segment  string   `json:"segment"`
	File     string   `json:"file"`
	Seq      int      `json:"seq"`
	Listed   int      `json:"listed"`
	Missing  []string `json:"missing"`  // active in Ledgerline, not permitted at BSE
	Inactive []string `json:"inactive"` // permitted at BSE, inactive or closed in Ledgerline
	Unknown  []string `json:"unknown"`  // permitted at BSE, not a client in Ledgerline
}

func (o *Ops) loadBSETradingStatus(ctx context.Context, tenant, actor, date, name string, rows []xfile.Row, r *XFileResult) error {
	seg, seq := bseSeg(name), bseSeq(name)
	id := date + "|" + seg
	if old, _, err := docs.Get[BSETradingStatus](ctx, o.Docs, tenant, cBSEUCCList, id); err == nil && old.Seq > seq {
		r.Loaded = fmt.Sprintf("Not used: run %02d of the day is already loaded", old.Seq)
		return nil
	}
	listed := map[string]bool{}
	for _, row := range rows {
		if u := strings.ToUpper(strings.TrimSpace(row.Get("UCC"))); u != "" {
			listed[u] = true
		}
	}
	cs, err := o.ClientMap(ctx, tenant)
	if err != nil {
		return err
	}
	st := BSETradingStatus{ID: id, Date: date, Segment: seg, File: name, Seq: seq, Listed: len(listed), Missing: []string{}, Inactive: []string{}, Unknown: []string{}}
	for _, u := range sortedKeys(cs) {
		c := cs[u]
		if c.Status == "Active" && contains(c.Segments, seg) && !listed[u] {
			st.Missing = append(st.Missing, u)
		}
	}
	for _, u := range sortedKeys(listed) {
		c, ok := cs[u]
		switch {
		case !ok:
			st.Unknown = append(st.Unknown, u)
		case c.Status != "Active":
			st.Inactive = append(st.Inactive, u)
		}
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cBSEUCCList, id, st, actor); err != nil {
		return err
	}
	if n := len(st.Missing) + len(st.Inactive) + len(st.Unknown); n > 0 {
		o.raiseWork(ctx, tenant, wBSEUCCStatus, seg, fmt.Sprintf("BSE %s permitted list: %d exceptions", strings.ToLower(segWord[seg]), n),
			fmt.Sprintf("Active in Ledgerline but not permitted at BSE: %s. Permitted at BSE but inactive or closed in Ledgerline: %s. Permitted at BSE, not a client: %s. Upload the UCC changes to BSE or correct the client's status.",
				listOrNone(st.Missing), listOrNone(st.Inactive), listOrNone(st.Unknown)), "", RoleMaker, "ccdata", date)
	} else {
		o.closeWork(ctx, tenant, actor, wBSEUCCStatus, seg, "BSE's permitted list matches Ledgerline")
	}
	r.Loaded, r.Added = fmt.Sprintf("Compared with Ledgerline's active %s clients: %d not permitted at BSE, %d inactive in Ledgerline, %d unknown", strings.ToLower(segWord[seg]), len(st.Missing), len(st.Inactive), len(st.Unknown)), st.Listed
	return nil
}

func listOrNone(xs []string) string {
	if len(xs) == 0 {
		return "none"
	}
	if len(xs) > 40 {
		return strings.Join(xs[:40], ", ") + fmt.Sprintf(" and %d more", len(xs)-40)
	}
	return strings.Join(xs, ", ")
}

// ---- order charges ----

// BSEOrderCharge is one day of BSE's order charges.
type BSEOrderCharge struct {
	Date       string    `json:"date"`
	Orders     money.Dec `json:"orders"`
	Charges    money.Dec `json:"charges"`
	Cumulative money.Dec `json:"cumulative"`
}

// BSEOrderCharges are a month's order charges as BSE reports them.
type BSEOrderCharges struct {
	ID   string                    `json:"id"` // YYYY-MM
	File string                    `json:"file"`
	Days map[string]BSEOrderCharge `json:"days"`
}

// Total is the month's charge to date.
func (b BSEOrderCharges) Total() money.Dec {
	var t money.Dec
	for _, d := range b.Days {
		t = t.Add(d.Charges)
	}
	return t
}

func (o *Ops) loadBSEOrderCharges(ctx context.Context, tenant, actor, name string, rows []xfile.Row, r *XFileResult) error {
	by := map[string]*BSEOrderCharges{}
	for _, row := range rows {
		d := anyDate(row.Get("Trade Date"))
		if d == "" {
			continue
		}
		m := d[:7]
		if by[m] == nil {
			x, _, _ := docs.Get[BSEOrderCharges](ctx, o.Docs, tenant, cBSEOrdChg, m)
			x.ID, x.File = m, name
			if x.Days == nil {
				x.Days = map[string]BSEOrderCharge{}
			}
			by[m] = &x
		}
		by[m].Days[d] = BSEOrderCharge{Date: d, Orders: decOr0(row.Get("Daily Number of Orders")), Charges: decOr0(row.Get("Daily Charges")), Cumulative: decOr0(row.Get("Daily Cumulative Charges"))}
	}
	n := 0
	for _, m := range sortedKeys(by) {
		if err := docs.Upsert(ctx, o.Docs, tenant, cBSEOrdChg, m, *by[m], actor); err != nil {
			return err
		}
		n += len(by[m].Days)
	}
	r.Loaded, r.Added = "Loaded as BSE's order charges by day (to check BSE's monthly bill)", n
	return nil
}

// BSEMonthOrderCharges is the month's order charges to date (YYYY-MM), for
// checking BSE's bill.
func (o *Ops) BSEMonthOrderCharges(ctx context.Context, tenant, month string) (BSEOrderCharges, bool) {
	x, _, err := docs.Get[BSEOrderCharges](ctx, o.Docs, tenant, cBSEOrdChg, month)
	return x, err == nil
}

// ---- turnover and transaction charges ----

// BSETRCRow is one group's line of the turnover file.
type BSETRCRow struct {
	Group      string    `json:"group"`
	Exclusive  string    `json:"exclusive"`
	Settlement string    `json:"settlement"`
	Turnover   money.Dec `json:"turnover"`
	Charges    money.Dec `json:"charges"`
	Payable    money.Dec `json:"payable"`
}

// BSETRC is a day's transaction charges by BSE and as billed by Ledgerline.
type BSETRC struct {
	ID       string      `json:"id"`
	File     string      `json:"file"`
	Rows     []BSETRCRow `json:"rows"`
	Turnover money.Dec   `json:"turnover"`
	Charges  money.Dec   `json:"charges"`
	Billed   money.Dec   `json:"billed"` // exchange transaction charges on BSE bills of the day
	Diff     money.Dec   `json:"difference"`
}

// TxnTolerance is the difference allowed between BSE's transaction
// charges and those billed (rounding across clients).
var TxnTolerance = money.MustDec("1.00")

// billedTxn is the exchange transaction charge billed on an exchange's trades of a day.
func (o *Ops) billedTxn(ctx context.Context, tenant, exch, date string) money.Dec {
	acct := ledger.ExchangeTxnPayable(exch)
	es, _ := o.App.Ledger.Store.List(ctx, tenant, ledger.Filter{Account: acct, From: date, To: date})
	var t money.Dec
	for _, e := range es {
		for _, p := range e.Postings {
			if p.Account == acct {
				t = t.Add(p.Credit.Dec()).Sub(p.Debit.Dec())
			}
		}
	}
	return t
}

func (o *Ops) loadBSETRC(ctx context.Context, tenant, actor, date, name string, rows []xfile.Row, r *XFileResult) error {
	x := BSETRC{ID: date, File: name}
	for _, row := range rows {
		g := BSETRCRow{Group: strings.TrimSpace(row.Get("Group")), Exclusive: strings.TrimSpace(row.Get("BSE Exclusive/Non-Exclusive")), Settlement: strings.TrimSpace(row.Get("Settlement Number")),
			Turnover: decOr0(row.Get("Daily Turnover")), Charges: decOr0(row.Get("Transaction Charges")), Payable: decOr0(row.Get("Amount Payable"))}
		x.Rows = append(x.Rows, g)
		x.Turnover, x.Charges = x.Turnover.Add(g.Turnover), x.Charges.Add(g.Charges)
	}
	// Ledgerline bills transaction charges per client and segment, not by
	// scrip group, so the day is reconciled in total.
	x.Billed = o.billedTxn(ctx, tenant, "BSE", date)
	x.Diff = x.Charges.Sub(x.Billed)
	if err := docs.Upsert(ctx, o.Docs, tenant, cBSETRC, date, x, actor); err != nil {
		return err
	}
	if x.Diff.Abs().Cmp(TxnTolerance) > 0 {
		o.raiseWork(ctx, tenant, wBSETxn, date, "BSE transaction charges differ on "+fmtDay(date),
			fmt.Sprintf("BSE charged ₹%s on turnover ₹%s; Ledgerline billed ₹%s on BSE trades (difference ₹%s). Check the charge rates in the schedule and any trades missing from Ledgerline.", x.Charges.Fixed(2), x.Turnover.Fixed(2), x.Billed.Fixed(2), x.Diff.Fixed(2)),
			"", RoleChecker, "ccdata", date)
		o.log(ctx, tenant, actor, "recon.bse_txn_break", date, map[string]any{"bse": x.Charges.Fixed(2), "billed": x.Billed.Fixed(2)})
	} else {
		o.closeWork(ctx, tenant, actor, wBSETxn, date, "Matches within tolerance")
	}
	r.Loaded, r.Added = fmt.Sprintf("Reconciled with the transaction charges billed on BSE trades: BSE ₹%s, Ledgerline ₹%s", x.Charges.Fixed(2), x.Billed.Fixed(2)), len(x.Rows)
	return nil
}

// ---- clearing house account statement ----

// BSEClearingEntry is one line of BSE's clearing house statement.
type BSEClearingEntry struct {
	Date        string    `json:"date"`
	Code        string    `json:"code"`
	Particulars string    `json:"particulars"`
	DocNo       string    `json:"doc_no"`
	Debit       money.Dec `json:"debit"`
	Credit      money.Dec `json:"credit"`
	Balance     money.Dec `json:"balance"`
	BalanceDrCr string    `json:"balance_dr_cr,omitempty"`
	AsOf        string    `json:"as_of,omitempty"`
}

// BSEClearingStmt is a clearing house statement as received.
type BSEClearingStmt struct {
	ID          string             `json:"id"`
	File        string             `json:"file"`
	Entries     []BSEClearingEntry `json:"entries"`
	TotalDebit  money.Dec          `json:"total_debit"`
	TotalCredit money.Dec          `json:"total_credit"`
	ReportBy    string             `json:"report_by"` // last day to report discrepancies
}

func stmtDate(v string) string {
	v = strings.TrimSpace(v)
	for _, l := range []string{"02/01/2006", "02-01-2006", "02/01/06", "02-01-06", "02-Jan-2006", "02-Jan-06", "02Jan2006", "2006-01-02"} {
		if t, err := time.Parse(l, v); err == nil {
			return t.Format("2006-01-02")
		}
	}
	return ""
}

func stmtAmt(v string) (money.Dec, string) {
	v = strings.ReplaceAll(strings.TrimSpace(v), ",", "")
	side := ""
	if u := strings.ToUpper(v); strings.HasSuffix(u, "DR") || strings.HasSuffix(u, "CR") {
		side, v = u[len(u)-2:], v[:len(v)-2]
	}
	return decOr0(v), side
}

type tok struct {
	s          string
	start, end int
}

func tokens(line string, from int) []tok {
	var out []tok
	i := from
	for i < len(line) {
		for i < len(line) && line[i] == ' ' {
			i++
		}
		j := i
		for j < len(line) && line[j] != ' ' {
			j++
		}
		if j > i {
			out = append(out, tok{line[i:j], i, j})
		}
		i = j
	}
	return out
}

// parseClearingStmt reads the fixed-width report, finding the columns from
// its heading line.
func parseClearingStmt(lines []string) ([]BSEClearingEntry, money.Dec, money.Dec, error) {
	h := -1
	for i, l := range lines {
		if strings.Contains(l, "Trn.Date") && strings.Contains(l, "Particulars") {
			h = i
			break
		}
	}
	if h < 0 {
		return nil, money.Dec{}, money.Dec{}, fmt.Errorf("the statement has no column heading (Trn.Date … Particulars …)")
	}
	hd := lines[h]
	pos := func(s string) int { return strings.Index(hd, s) }
	pCode, pPart, pDoc := pos("Code"), pos("Particulars"), pos("Doc.No.")
	type col struct {
		name     string
		start, e int
	}
	var cols []col
	for _, c := range []string{"D E B I T S", "C R E D I T S", "B A L A N C E S"} {
		if p := pos(c); p >= 0 {
			cols = append(cols, col{c, p, p + len(c)})
		}
	}
	if pCode < 0 || pPart < 0 || pDoc < 0 || len(cols) != 3 {
		return nil, money.Dec{}, money.Dec{}, fmt.Errorf("the column heading is not the one Ledgerline knows")
	}
	cut := func(l string, a, b int) string {
		if a >= len(l) {
			return ""
		}
		return strings.TrimSpace(l[a:min(b, len(l))])
	}
	// numbers go to the amount column whose heading ends nearest the number's end
	place := func(l string) (vals [3]string, asOf, docNo string) {
		var doc []string
		for _, t := range tokens(l, pDoc) {
			switch {
			case stmtDate(t.s) != "":
				asOf = stmtDate(t.s)
			case reAmount.MatchString(t.s):
				best, bd := 0, 1<<30
				for i, c := range cols {
					if d := iabs(t.end - c.e); d < bd {
						best, bd = i, d
					}
				}
				vals[best] = t.s
			case (strings.EqualFold(t.s, "Dr") || strings.EqualFold(t.s, "Cr")) && vals[2] != "":
				vals[2] += t.s
			case t.start < cols[0].start:
				doc = append(doc, t.s)
			}
		}
		return vals, asOf, strings.Join(doc, " ")
	}
	var out []BSEClearingEntry
	var td, tc money.Dec
	dashed := func(l string, c byte) bool {
		t := strings.TrimSpace(l)
		return len(t) > 3 && strings.Trim(t, string(c)+" ") == ""
	}
	afterDash := false
	for _, l := range lines[h+1:] {
		switch {
		case dashed(l, '='):
			afterDash = false
			continue
		case dashed(l, '-'):
			afterDash = len(out) > 0
			continue
		}
		f := strings.Fields(l)
		if len(f) == 0 {
			continue
		}
		if d := stmtDate(f[0]); d != "" && len(l) > pPart {
			v, asOf, doc := place(l)
			e := BSEClearingEntry{Date: d, Code: cut(l, pCode, pPart), Particulars: cut(l, pPart, pDoc), DocNo: doc, AsOf: asOf}
			e.Debit, _ = stmtAmt(v[0])
			e.Credit, _ = stmtAmt(v[1])
			e.Balance, e.BalanceDrCr = stmtAmt(v[2])
			out = append(out, e)
			continue
		}
		if afterDash {
			v, _, _ := place(l)
			if v[0] != "" || v[1] != "" {
				td, _ = stmtAmt(v[0])
				tc, _ = stmtAmt(v[1])
			}
			afterDash = false
		}
	}
	return out, td, tc, nil
}

func iabs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func textLines(rows []xfile.Row) []string {
	var out []string
	for _, r := range rows {
		out = append(out, r.Get("line"))
	}
	return out
}

func (o *Ops) loadBSEClearingStmt(ctx context.Context, tenant, actor, date, name string, rows []xfile.Row, r *XFileResult) error {
	lines := textLines(rows)
	if !strings.Contains(strings.ToUpper(strings.Join(lines, "\n")), "CLEARING HOUSE") {
		return fmt.Errorf("this is not BSE's clearing house statement")
	}
	es, td, tc, err := parseClearingStmt(lines)
	if err != nil {
		return err
	}
	var sd, sc money.Dec
	for _, e := range es {
		sd, sc = sd.Add(e.Debit), sc.Add(e.Credit)
	}
	if (td.Sign() != 0 || tc.Sign() != 0) && (sd.Cmp(td) != 0 || sc.Cmp(tc) != 0) {
		return fmt.Errorf("the entries total debit %s and credit %s, the statement's totals are %s and %s", sd.Fixed(2), sc.Fixed(2), td.Fixed(2), tc.Fixed(2))
	}
	by := addDays(date, 15)
	x := BSEClearingStmt{ID: date, File: name, Entries: es, TotalDebit: sd, TotalCredit: sc, ReportBy: by}
	if err := docs.Upsert(ctx, o.Docs, tenant, cBSEClrStmt, date, x, actor); err != nil {
		return err
	}
	o.workDue(ctx, tenant, wBSEClrStmt, date, "Reconcile BSE's clearing house statement of "+fmtDay(date),
		fmt.Sprintf("%d entries, debits ₹%s, credits ₹%s. Check them against the firm's BSE clearing account and report any discrepancy to BSE's clearing house by %s (15 days).", len(es), sd.Fixed(2), sc.Fixed(2), fmtDay(by)),
		"", RoleChecker, "ccdata", date, by+" 17:00")
	r.Loaded, r.Added = "Kept as BSE clearing ledger entries; a checker is reminded to reconcile by "+by, len(es)
	return nil
}

// ---- PANs debarred by SEBI ----

// BSEDebarredRow is one line of BSE's DEBR statement.
type BSEDebarredRow struct {
	Scrip   string `json:"scrip,omitempty"`
	UCC     string `json:"ucc"`
	Name    string `json:"name"`
	PAN     string `json:"pan"`
	From    string `json:"from,omitempty"`
	Till    string `json:"till,omitempty"` // empty: not specified
	Segment string `json:"segment"`
	Action  string `json:"action,omitempty"` // Blocked, Flagged, Not a client, Not active
}

// BSEDebarred is a day's DEBR statement.
type BSEDebarred struct {
	ID         string           `json:"id"`
	File       string           `json:"file"`
	ReportDate string           `json:"report_date"`
	Rows       []BSEDebarredRow `json:"rows"`
}

func parseDebarred(lines []string) (string, []BSEDebarredRow) {
	rep := ""
	var out []BSEDebarredRow
	for _, l := range lines {
		u := strings.ToUpper(l)
		if i := strings.Index(u, "REPORT DATE"); i >= 0 && rep == "" {
			rest := strings.TrimLeft(l[i+len("REPORT DATE"):], " :-\t")
			if f := strings.Fields(rest); len(f) > 0 {
				rep = stmtDate(f[0])
			}
		}
		t := strings.TrimSpace(l)
		if !strings.HasPrefix(t, "|") || strings.Contains(u, "SR NO") {
			continue
		}
		f := strings.Split(strings.Trim(t, "|"), "|")
		if len(f) < 8 {
			continue
		}
		for i := range f {
			f[i] = strings.TrimSpace(f[i])
		}
		if _, err := strconv.Atoi(f[0]); err != nil {
			continue
		}
		row := BSEDebarredRow{Scrip: f[1], UCC: strings.ToUpper(f[2]), Name: f[3], PAN: strings.ToUpper(f[4]), From: stmtDate(f[5]), Till: stmtDate(f[6]), Segment: f[7]}
		if strings.Contains(strings.ToUpper(row.Scrip), "NOT") || row.Scrip == "-" {
			row.Scrip = ""
		}
		out = append(out, row)
	}
	return rep, out
}

func (o *Ops) loadBSEDebarred(ctx context.Context, tenant, actor, date, name string, rows []xfile.Row, r *XFileResult) error {
	lines := textLines(rows)
	if !strings.Contains(strings.ToUpper(strings.Join(lines, "\n")), "DEBARRED") {
		return fmt.Errorf("this is not BSE's statement of PANs debarred by SEBI")
	}
	rep, list := parseDebarred(lines)
	rep = firstOf(rep, date)
	cs, err := o.Clients(ctx, tenant)
	if err != nil {
		return err
	}
	byUCC, byPAN := map[string]Client{}, map[string]Client{}
	for _, c := range cs {
		byUCC[c.UCC] = c
		if c.PAN != "" {
			byPAN[strings.ToUpper(c.PAN)] = c
		}
	}
	blocked, flagged := 0, 0
	for i := range list {
		d := &list[i]
		if d.Till != "" && d.Till < rep {
			d.Action = "Period over"
			continue
		}
		order := "BSE DEBR " + rep
		if d.Scrip != "" {
			order += " scrip " + d.Scrip
		}
		// A debarment for every security goes into the debarred list (so
		// the PAN cannot be onboarded); a scrip-specific one does not.
		if d.Scrip == "" && rePAN.MatchString(d.PAN) {
			e := Debarred{PAN: d.PAN, Name: d.Name, Regulator: "SEBI", OrderNo: order, OrderDate: firstOf(d.From, rep), Till: d.Till}
			e.ID = "BSEDEBR-" + d.PAN
			if _, _, err := docs.Get[Debarred](ctx, o.Docs, tenant, cDebarred, e.ID); err != nil {
				if err := docs.Upsert(ctx, o.Docs, tenant, cDebarred, e.ID, e, actor); err != nil {
					return err
				}
			}
		}
		c, ok := byUCC[d.UCC]
		if !ok {
			c, ok = byPAN[d.PAN]
		}
		switch {
		case !ok:
			d.Action = "Not a client"
			continue
		case c.Status != "Active":
			d.Action = "Not active"
			continue
		}
		d.UCC = c.UCC
		if d.Scrip == "" {
			if !c.Blocked {
				if _, err := docs.Update(ctx, o.Docs, tenant, cClient, c.UCC, actor, func(x *Client) error {
					x.Blocked, x.BlockedReason = true, DebarredReason
					return nil
				}); err != nil {
					return err
				}
				o.log(ctx, tenant, actor, "client.debarred", c.UCC, map[string]any{"order": order, "regulator": "SEBI", "pan": MaskPAN(d.PAN)})
			}
			d.Action = "Blocked"
			blocked++
			o.raiseAlert(ctx, tenant, actor, Alert{Source: "BSE", Type: "Debarred entity", UCC: c.UCC, Severity: "High",
				Detail: fmt.Sprintf("On BSE's list of PANs debarred by SEBI (report of %s, segment %s); trading blocked", fmtDay(rep), d.Segment)}, "BSEDEBR:"+c.UCC)
			o.raiseWork(ctx, tenant, wBSEDebarred, c.UCC, "Urgent: "+c.UCC+" debarred by SEBI (BSE report)",
				fmt.Sprintf("%s (PAN %s) is on BSE's statement of PANs debarred by SEBI from %s%s. The client is blocked in RMS. Close open positions as the order allows and record the order on the client.", c.Name, MaskPAN(d.PAN), firstOf(fmtDay(d.From), "—"), tillText(d.Till)),
				c.UCC, RoleCompliance, "clients", date)
		} else {
			d.Action = "Flagged"
			flagged++
			o.raiseAlert(ctx, tenant, actor, Alert{Source: "BSE", Type: "Debarred entity (scrip-specific)", UCC: c.UCC, Severity: "High",
				Detail: fmt.Sprintf("On BSE's list of PANs debarred by SEBI for scrip %s (report of %s); not blocked, compliance decides", d.Scrip, fmtDay(rep))}, "BSEDEBR:"+c.UCC+":"+d.Scrip)
			o.raiseWork(ctx, tenant, wBSEDebarred, c.UCC+":"+d.Scrip, "Urgent: "+c.UCC+" debarred by SEBI in scrip "+d.Scrip,
				fmt.Sprintf("%s (PAN %s) is debarred by SEBI in scrip %s from %s%s (BSE report). The client is not blocked: decide whether to block the client or stop trades in that scrip.", c.Name, MaskPAN(d.PAN), d.Scrip, firstOf(fmtDay(d.From), "—"), tillText(d.Till)),
				c.UCC, RoleCompliance, "clients", date)
		}
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cBSEDebr, date, BSEDebarred{ID: date, File: name, ReportDate: rep, Rows: list}, actor); err != nil {
		return err
	}
	r.Loaded, r.Added = fmt.Sprintf("Added to the debarred list; %d active clients blocked, %d flagged (scrip-specific) for compliance", blocked, flagged), len(list)
	return nil
}

func tillText(t string) string {
	if t == "" {
		return " (end not specified)"
	}
	return " to " + fmtDay(t)
}

// ---- mobile and email exceptions ----

// BSEContactRow is one client on BSE's mobile and email exceptions.
type BSEContactRow struct {
	UCC    string `json:"ucc"`
	Mobile string `json:"mobile,omitempty"`
	Email  string `json:"email,omitempty"`
	Note   string `json:"note"`
}

func (o *Ops) loadBSEContact(ctx context.Context, tenant, actor, date, name string, rows []xfile.Row, r *XFileResult) error {
	cs, err := o.ClientMap(ctx, tenant)
	if err != nil {
		return err
	}
	var list []BSEContactRow
	var lines []string
	for _, row := range rows {
		x := BSEContactRow{UCC: strings.ToUpper(strings.TrimSpace(row.Get("UCC"))), Mobile: strings.TrimSpace(row.Get("Mobile")), Email: strings.TrimSpace(row.Get("Email"))}
		if x.UCC == "" {
			continue
		}
		c, ok := cs[x.UCC]
		switch {
		case !ok:
			x.Note = "not a client in Ledgerline"
		case x.Mobile != "" && x.Mobile != c.Mobile && !strings.HasSuffix(c.Mobile, x.Mobile):
			x.Note = "BSE has mobile " + maskMobile(x.Mobile) + ", Ledgerline " + maskMobile(c.Mobile)
		case x.Email != "" && !strings.EqualFold(x.Email, c.Email):
			x.Note = "BSE has email " + maskEmail(x.Email) + ", Ledgerline " + maskEmail(c.Email)
		case x.Mobile != "":
			x.Note = "mobile " + maskMobile(x.Mobile) + " flagged by BSE (shared, invalid or missing?)"
		default:
			x.Note = "email " + maskEmail(x.Email) + " flagged by BSE (shared, invalid or missing?)"
		}
		list = append(list, x)
		lines = append(lines, x.UCC+": "+x.Note)
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cBSEContact, date, struct {
		ID   string          `json:"id"`
		File string          `json:"file"`
		Rows []BSEContactRow `json:"rows"`
	}{date, name, list}, actor); err != nil {
		return err
	}
	if len(list) > 0 {
		o.raiseWork(ctx, tenant, wBSEContact, date, fmt.Sprintf("Check mobile and email of %d clients (BSE exceptions)", len(list)),
			"BSE lists these clients' mobile or email: "+strings.Join(lines, "; ")+". Confirm the client's own mobile and email, update them and upload the UCC change.", "", RoleMaker, "clients", date)
	}
	o.log(ctx, tenant, actor, "bse.contact_exceptions", date, map[string]any{"clients": len(list)}) // no numbers or addresses in the log
	r.Loaded, r.Added = "Compared with the clients' mobile and email; one work item lists the clients to check", len(list)
	return nil
}

// ---- bulk deals ----

// BSEBulkDeal is one bulk deal as BSE lists it.
type BSEBulkDeal struct {
	Member, Scrip, UCC, Name, Side string
	Qty, Price                     money.Dec
	Date                           string
}

func (o *Ops) loadBSEBulk(ctx context.Context, tenant, actor, date, name string, rows []xfile.Row, r *XFileResult) error {
	cs, _ := o.Clients(ctx, tenant)
	byName := map[string]string{}
	for _, c := range cs {
		byName[strings.ToUpper(strings.Join(strings.Fields(c.Name), " "))] = c.UCC
	}
	var list []BSEBulkDeal
	found := 0
	for _, row := range rows {
		b := BSEBulkDeal{Member: strings.TrimSpace(row.Get("MemberCode")), Scrip: strings.TrimSpace(row.Get("ScripCode")), UCC: strings.ToUpper(strings.TrimSpace(row.Get("ClientCode"))),
			Name: strings.Join(strings.Fields(row.Get("ClientName")), " "), Side: strings.ToUpper(strings.TrimSpace(row.Get("BuySell"))), Qty: decOr0(row.Get("Qty")), Price: decOr0(row.Get("Price")), Date: firstOf(stmtDate(row.Get("Date")), date)}
		if b.UCC == "" {
			b.UCC = byName[strings.ToUpper(b.Name)]
		}
		list = append(list, b)
		if b.UCC != "" {
			found++
			o.raiseAlert(ctx, tenant, actor, Alert{Source: "BSE", Type: "Bulk deal", UCC: b.UCC, Severity: "Low", Date: b.Date,
				Detail: fmt.Sprintf("Bulk deal in scrip %s: %s %s at ₹%s (BSE bulk deal file; for review, no action needed unless out of profile)", b.Scrip, map[string]string{"B": "bought", "S": "sold"}[b.Side], b.Qty.Fixed(0), b.Price.Fixed(2))},
				"BULK:"+b.Date+":"+b.UCC+":"+b.Scrip+":"+b.Side)
		}
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cBSEBulk, date, struct {
		ID    string        `json:"id"`
		File  string        `json:"file"`
		Deals []BSEBulkDeal `json:"deals"`
	}{date, name, list}, actor); err != nil {
		return err
	}
	r.Loaded, r.Added = fmt.Sprintf("Kept for surveillance (provisional layout); %d deals by the firm's clients noted as low-priority alerts", found), len(list)
	return nil
}

// ---- order log ----

// BSEOrder is one order event of BSE's order log.
type BSEOrder struct {
	Side     string    `json:"side"`
	Scrip    string    `json:"scrip"`
	Symbol   string    `json:"symbol"`
	Qty      int64     `json:"qty"`
	Qty2     int64     `json:"qty2"`
	Price    money.Dec `json:"price"` // rupees (converted when the file is in paise)
	Time     string    `json:"time"`
	OrderID  string    `json:"order_id"`
	Type     string    `json:"type"`
	UCC      string    `json:"ucc"`
	Validity string    `json:"validity"`
	Action   string    `json:"action"`
	Message  string    `json:"message"`
	Code     string    `json:"code"`
	Terminal string    `json:"terminal"` // CTCL ID: the dealer is found from it
}

// OTRRow is the order-to-trade ratio of a client or terminal.
type OTRRow struct {
	Key    string    `json:"key"`
	Orders int       `json:"orders"`
	Trades int       `json:"trades"`
	Ratio  money.Dec `json:"ratio"`
}

// BSEOrderLog is a day's order log with its ratios.
type BSEOrderLog struct {
	ID         string     `json:"id"`
	File       string     `json:"file"`
	PriceUnit  string     `json:"price_unit"` // rupees, paise or unchecked
	Orders     []BSEOrder `json:"orders"`
	ByClient   []OTRRow   `json:"by_client"`
	ByTerminal []OTRRow   `json:"by_terminal"`
}

func otr(orders, trades map[string]int) []OTRRow {
	var out []OTRRow
	for _, k := range sortedKeys(orders) {
		x := OTRRow{Key: k, Orders: orders[k], Trades: trades[k]}
		x.Ratio, _ = money.DecInt(int64(x.Orders)).Quo(money.DecInt(int64(max(x.Trades, 1))))
		out = append(out, x)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Ratio.Cmp(out[j].Ratio) > 0 })
	return out
}

func (o *Ops) loadBSEOrderLog(ctx context.Context, tenant, actor, date, name string, rows []xfile.Row, r *XFileResult) error {
	x := BSEOrderLog{ID: date, File: name, PriceUnit: "unchecked"}
	for _, row := range rows {
		q, _ := strconv.ParseInt(strings.TrimSpace(row.Get("Qty")), 10, 64)
		q2, _ := strconv.ParseInt(strings.TrimSpace(row.Get("Qty2")), 10, 64)
		x.Orders = append(x.Orders, BSEOrder{Side: strings.TrimSpace(row.Get("BuySell")), Scrip: strings.TrimSpace(row.Get("ScripCode")), Symbol: strings.ToUpper(strings.TrimSpace(row.Get("Symbol"))),
			Qty: q, Qty2: q2, Price: decOr0(row.Get("Price")), Time: strings.TrimSpace(row.Get("Time")), OrderID: strings.TrimSpace(row.Get("OrderID")), Type: strings.TrimSpace(row.Get("OrderType")),
			UCC: strings.ToUpper(strings.TrimSpace(row.Get("ClientCode"))), Validity: strings.TrimSpace(row.Get("Validity")), Action: strings.TrimSpace(row.Get("Action")),
			Message: strings.TrimSpace(row.Get("Message")), Code: strings.TrimSpace(row.Get("Code")), Terminal: strings.TrimSpace(row.Get("CTCLID"))})
	}
	// Paise or rupees: compare the order prices with the day's BSE trades
	// of the same client and symbol.
	ts, _ := o.App.Trades.List(ctx, tenant, ingest.FromExchange, date)
	px := map[string]money.Dec{}
	tradesBy, tradesTerm := map[string]int{}, map[string]int{}
	for _, t := range ts {
		if t.Exchange != "BSE" {
			continue
		}
		px[t.UCC+"|"+t.Symbol] = t.Price
		tradesBy[t.UCC]++
		if t.Terminal != "" {
			tradesTerm[t.Terminal]++
		}
	}
	paise, rupees := 0, 0
	for _, od := range x.Orders {
		tp, ok := px[od.UCC+"|"+od.Symbol]
		if !ok || tp.Sign() <= 0 || od.Price.Sign() <= 0 {
			continue
		}
		ratio, _ := od.Price.Quo(tp)
		switch {
		case ratio.Cmp(money.DecInt(50)) > 0 && ratio.Cmp(money.DecInt(200)) < 0:
			paise++
		case ratio.Cmp(money.MustDec("0.5")) > 0 && ratio.Cmp(money.DecInt(2)) < 0:
			rupees++
		}
	}
	switch {
	case paise > rupees:
		x.PriceUnit = "paise"
		for i := range x.Orders {
			x.Orders[i].Price, _ = x.Orders[i].Price.Quo(money.DecInt(100))
		}
	case rupees > 0:
		x.PriceUnit = "rupees"
	}
	ordBy, ordTerm := map[string]int{}, map[string]int{}
	for _, od := range x.Orders {
		ordBy[od.UCC]++
		if od.Terminal != "" {
			ordTerm[od.Terminal]++
		}
	}
	x.ByClient, x.ByTerminal = otr(ordBy, tradesBy), otr(ordTerm, tradesTerm)
	if err := docs.Upsert(ctx, o.Docs, tenant, cBSEOrderLog, date, x, actor); err != nil {
		return err
	}
	r.Loaded, r.Added = fmt.Sprintf("Kept as the order log for surveillance (provisional layout; prices %s); order-to-trade ratios by client and terminal", x.PriceUnit), len(x.Orders)
	return nil
}

// BSEOrderToTrade is a day's order log summary (nil when not loaded).
func (o *Ops) BSEOrderToTrade(ctx context.Context, tenant, date string) *BSEOrderLog {
	x, _, err := docs.Get[BSEOrderLog](ctx, o.Docs, tenant, cBSEOrderLog, date)
	if err != nil {
		return nil
	}
	return &x
}

// ---- demat check responses to the UCC upload ----

// BSEUCCDemat is BSE's demat-check response for a client, kept as received.
type BSEUCCDemat struct {
	ID         string   `json:"id"`
	UCC        string   `json:"ucc"`
	Depository string   `json:"depository"`
	Date       string   `json:"date"`
	File       string   `json:"file"`
	DPID       string   `json:"dp_id"`
	Status     string   `json:"status"` // the last filled field (to confirm against BSE's layout)
	Fields     []string `json:"fields"` // every field in order, PAN masked
}

func (o *Ops) loadBSEUCCDemat(ctx context.Context, tenant, actor, date, dep, name string, rows []xfile.Row, r *XFileResult) error {
	lib, _ := XFiles()
	s, _ := lib.Get("bse-ucc-demat-" + strings.ToLower(dep))
	n := 0
	for _, row := range rows {
		ucc := strings.ToUpper(strings.TrimSpace(row.Get("ClientCode")))
		if ucc == "" {
			continue
		}
		x := BSEUCCDemat{ID: ucc + "|" + dep, UCC: ucc, Depository: dep, Date: date, File: name, DPID: strings.TrimSpace(row.Get("DPID"))}
		for _, c := range s.Columns {
			v := strings.TrimSpace(row.Get(c.Key))
			if c.Key == "PAN" {
				v = MaskPAN(v)
			}
			x.Fields = append(x.Fields, v)
			if v != "" {
				x.Status = v
			}
		}
		if err := docs.Upsert(ctx, o.Docs, tenant, cBSEUCCDemat, x.ID, x, actor); err != nil {
			return err
		}
		n++
	}
	o.log(ctx, tenant, actor, "bse.ucc_demat_response", name, map[string]any{"depository": dep, "clients": n}) // no PANs in the log
	r.Loaded, r.Added = "Kept per client as BSE's "+dep+" demat check (provisional layout; shown with the client)", n
	return nil
}

// BSEUCCDematResponses are the demat-check responses held for a client.
func (o *Ops) BSEUCCDematResponses(ctx context.Context, tenant, ucc string) []BSEUCCDemat {
	xs, _ := docs.List[BSEUCCDemat](ctx, o.Docs, tenant, cBSEUCCDemat, strings.ToUpper(ucc)+"|")
	return xs
}

// ---- on the Clearing data screen ----

func (o *Ops) bseSections(ctx context.Context, tenant, date string, cs map[string]Client) []CCSection {
	var out []CCSection
	np := CCSection{ID: "bse_ucc", Title: "BSE: clients not permitted to trade", About: "BSE's latest not-permitted list of the day (cash and derivatives) with what to fix; each client has a work item.",
		Columns: []string{"Segment", "Client", "Name", "Reason and fix"}}
	for _, seg := range []string{"CM", "FO"} {
		if x, _, err := docs.Get[BSENotPermittedList](ctx, o.Docs, tenant, cBSENotPermitted, date+"|"+seg); err == nil {
			np.Loaded, np.File = true, strings.TrimPrefix(np.File+", "+x.File, ", ")
			for _, u := range sortedKeys(x.Clients) {
				np.Rows = append(np.Rows, []string{seg, u, cs[u].Name, strings.Join(x.Clients[u], " ")})
				np.Diffs++
			}
		}
	}
	out = append(out, np)
	ts := CCSection{ID: "bse_ucc_status", Title: "BSE: permitted list against Ledgerline", About: "Clients active in Ledgerline that BSE does not permit, and clients BSE permits that are inactive, closed or unknown in Ledgerline.",
		Columns: []string{"Segment", "Client", "Name", "Exception"}}
	for _, seg := range []string{"CM", "FO"} {
		if x, _, err := docs.Get[BSETradingStatus](ctx, o.Docs, tenant, cBSEUCCList, date+"|"+seg); err == nil {
			ts.Loaded, ts.File = true, strings.TrimPrefix(ts.File+", "+x.File, ", ")
			for _, g := range []struct {
				us  []string
				why string
			}{{x.Missing, "Active in Ledgerline, not permitted at BSE"}, {x.Inactive, "Permitted at BSE, inactive or closed in Ledgerline"}, {x.Unknown, "Permitted at BSE, not a client in Ledgerline"}} {
				for _, u := range g.us {
					ts.Rows = append(ts.Rows, []string{seg, u, cs[u].Name, g.why})
					ts.Diffs++
				}
			}
		}
	}
	out = append(out, ts)
	tx := CCSection{ID: "bse_txn", Title: "BSE: transaction charges", About: "BSE's transaction charges of the day by group, against the exchange transaction charges on Ledgerline's bills for BSE trades (in total; Ledgerline does not bill by group).",
		Columns: []string{"Group", "Exclusive", "Settlement", "Turnover", "BSE charges", "Ledgerline", "Difference"}}
	if x, _, err := docs.Get[BSETRC](ctx, o.Docs, tenant, cBSETRC, date); err == nil {
		tx.Loaded, tx.File = true, x.File
		for _, g := range x.Rows {
			tx.Rows = append(tx.Rows, []string{g.Group, g.Exclusive, g.Settlement, g.Turnover.Fixed(2), g.Charges.Fixed(2), "", ""})
		}
		tx.Rows = append(tx.Rows, []string{"Total", "", "", x.Turnover.Fixed(2), x.Charges.Fixed(2), x.Billed.Fixed(2), x.Diff.Fixed(2)})
		if x.Diff.Abs().Cmp(TxnTolerance) > 0 {
			tx.Diffs++
		}
	}
	out = append(out, tx)
	oc := CCSection{ID: "bse_order_charges", Title: "BSE: order charges (month to date)", About: "BSE's order charges per day, to check against BSE's monthly bill.",
		Columns: []string{"Date", "Orders", "Charges", "Cumulative"}}
	if x, ok := o.BSEMonthOrderCharges(ctx, tenant, date[:7]); ok {
		oc.Loaded, oc.File = true, x.File
		for _, d := range sortedKeys(x.Days) {
			v := x.Days[d]
			oc.Rows = append(oc.Rows, []string{d, v.Orders.Fixed(0), v.Charges.Fixed(2), v.Cumulative.Fixed(2)})
		}
	}
	out = append(out, oc)
	cl := CCSection{ID: "bse_clearing", Title: "BSE: clearing house statement", About: "BSE clearing house account entries; discrepancies must be reported to BSE within 15 days.",
		Columns: []string{"Date", "Code", "Particulars", "Doc no.", "Debit", "Credit", "Balance", "As of"}}
	if x, _, err := docs.Get[BSEClearingStmt](ctx, o.Docs, tenant, cBSEClrStmt, date); err == nil {
		cl.Loaded, cl.File = true, x.File+" (report by "+x.ReportBy+")"
		for _, e := range x.Entries {
			cl.Rows = append(cl.Rows, []string{e.Date, e.Code, e.Particulars, e.DocNo, e.Debit.Fixed(2), e.Credit.Fixed(2), strings.TrimSpace(e.Balance.Fixed(2) + " " + e.BalanceDrCr), e.AsOf})
		}
	}
	out = append(out, cl)
	ol := CCSection{ID: "bse_orders", Title: "BSE: order-to-trade ratio", About: "From BSE's order log (provisional layout): order events against trades, by client and by terminal (CTCL ID).",
		Columns: []string{"By", "Client or terminal", "Orders", "Trades", "Ratio"}}
	if x := o.BSEOrderToTrade(ctx, tenant, date); x != nil {
		ol.Loaded, ol.File = true, x.File
		for _, g := range []struct {
			by string
			rs []OTRRow
		}{{"Client", x.ByClient}, {"Terminal", x.ByTerminal}} {
			for i, rr := range g.rs {
				if i == 20 {
					break
				}
				ol.Rows = append(ol.Rows, []string{g.by, rr.Key, fmt.Sprint(rr.Orders), fmt.Sprint(rr.Trades), rr.Ratio.Fixed(2)})
			}
		}
	}
	out = append(out, ol)
	return out
}
