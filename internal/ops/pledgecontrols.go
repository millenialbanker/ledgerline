package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
	"ledgerline/internal/xfile"
)

// ---- controls on pledges and collateral ----
//
//   - The collateral register (SEBI stock broker master circular
//     SEBI/HO/MIRSD/MIRSD-PoD/P/CIR/2025/90, para 25.1.2): every receipt of
//     collateral with the client's acknowledgement (the confirmation at
//     CDSL), the deposit with the clearing corporation (re-pledge), the
//     return to the client (unpledge), invocations, and corporate action
//     benefits credited on pledged securities; exported for inspection.
//   - The daily statement of collateral by type (para 25.1.4): cash,
//     fixed deposits, bank guarantees and securities, each deposited,
//     utilised and available (collateralSection, on the margin statement).
//   - The pledge set-up letter to the pledgor (CDSL operating instructions
//     8.5.2): a signed letter with the PSN for each pledge set up.
//   - The daily reconciliation of pledge, unpledge and invocation requests
//     received, executed and pending, and of maker entries against checker
//     verifications (CDSL operating instructions 8.8).
//   - Locked-in securities are not invoked before the lock-in ends (CDSL
//     operating instructions 8.5.3-8.5.5): the lock-in balances of CDSL's
//     statement of holding are kept and taken off what may be invoked.
//   - The confiscation-cum-early pay-in of unpaid securities is executed by
//     9:45 a.m. on the pay-in day, T+1 (CDSL master circular
//     CDSL/OPS/DP/POLCY/2026/257, 4.10.2.1 and 4.10.4.3; later only best
//     effort).
//   - Margin pledge, unpledge and invocation charges are recovered at the
//     depository's actual cost and shown in the tariff (NSE/INSP/73976,
//     Exhibit 17 Q27): the firm records CDSL's charge for each and the
//     tariff and the DP plans are checked against it.

const (
	cLockIn         = "lockin"     // key: UCC|ISIN|BOID
	cPledgeCost     = "pledgecost" // key: main
	cPledgeCtlState = "pledgectl"  // key: tick
	DocPledgeLetter = "pledge_letter"
	cepCutoff       = "09:45"
	wPledgeCtl      = "pledge-controls"
)

func init() {
	DocKindNames[DocPledgeLetter] = "Pledge set-up letter"
}

// ---- the collateral register ----

// CollRegRow is one line of the register.
type CollRegRow struct {
	Date   string `json:"date"`
	Time   string `json:"time,omitempty"`
	UCC    string `json:"ucc"`
	Name   string `json:"name,omitempty"`
	Event  string `json:"event"`
	Ref    string `json:"ref"`
	PSN    string `json:"psn,omitempty"`
	ISIN   string `json:"isin,omitempty"`
	Symbol string `json:"symbol,omitempty"`
	Qty    string `json:"qty"`
	Value  string `json:"value,omitempty"`
	Status string `json:"status,omitempty"`
	Ack    string `json:"ack,omitempty"` // the client's acknowledgement or authorisation
	Maker  string `json:"maker,omitempty"`
	Check  string `json:"checker,omitempty"`
	Letter string `json:"letter,omitempty"` // the set-up letter's dispatch
}

func istDay(ts string) (string, string) {
	if t, err := time.Parse(time.RFC3339, ts); err == nil {
		t = t.In(ist)
		return t.Format("2006-01-02"), t.Format("15:04")
	}
	if t, err := time.ParseInLocation("2006-01-02 15:04", ts, ist); err == nil {
		return t.Format("2006-01-02"), t.Format("15:04")
	}
	if len(ts) >= 10 && validDate(ts[:10]) {
		return ts[:10], ""
	}
	return "", ""
}

// CollateralRegister lists the register between two dates (Risk,
// Compliance, Ops).
func (o *Ops) CollateralRegister(ctx context.Context, tenant, actor, from, to string) ([]CollRegRow, error) {
	out := []CollRegRow{}
	if err := o.require(ctx, tenant, actor, "Viewing the collateral register", RoleRisk, RoleCompliance, RoleMaker, RoleChecker); err != nil {
		return out, err
	}
	if !validDate(from) || !validDate(to) || from > to {
		return out, invalid("give the period as two dates, from before to")
	}
	cs, _ := o.ClientMap(ctx, tenant)
	in := func(d string) bool { return d != "" && d >= from && d <= to }
	letters := o.pledgeLetterMap(ctx, tenant)
	pledged := map[string]bool{} // UCC|ISIN pledged to the firm at some time
	ps, _ := docs.List[PledgeRequest](ctx, o.Docs, tenant, cPledge, "")
	for _, p := range ps {
		d, tm := istDay(p.Time)
		if p.Status == "Accepted" {
			pledged[p.UCC+"|"+p.ISIN] = true
		}
		if !in(d) {
			continue
		}
		ack := "Accepted by the DP from the depository's pledge request"
		if p.Via == cplVia {
			ack = "Client's pledge request; confirmed by the client at CDSL with CDSL's OTP"
			if p.Status != "Accepted" {
				ack = "Client's pledge request; not confirmed at CDSL"
			}
		}
		r := CollRegRow{Date: d, Time: tm, UCC: p.UCC, Name: cs[p.UCC].Name, Event: "Collateral received (margin pledge" + map[bool]string{true: ", MTF)", false: ")"}[p.Kind == "MF"], Ref: p.ID, PSN: p.PSN, ISIN: p.ISIN, Symbol: p.Symbol,
			Qty: fmt.Sprint(p.Qty), Status: p.Status, Ack: ack, Maker: p.RequestedBy, Check: p.DecidedBy, Letter: letters[p.ID]}
		out = append(out, r)
	}
	for _, b := range o.cplRepledges(ctx, tenant) {
		d, tm := istDay(firstOf(b.SentAt, b.At))
		if !in(d) {
			continue
		}
		for _, l := range b.Lines {
			out = append(out, CollRegRow{Date: d, Time: tm, UCC: l.UCC, Name: cs[l.UCC].Name, Event: "Deposited with the clearing corporation (re-pledge)", Ref: b.ID, PSN: l.PSN, ISIN: l.ISIN, Symbol: l.Symbol,
				Qty: fmt.Sprint(l.Qty), Value: l.Value, Status: l.Status, Ack: "Authorised by the client in the margin pledge", Maker: b.Maker, Check: b.Checker})
		}
	}
	for _, r := range o.cplReleases(ctx, tenant) {
		d, tm := istDay(firstOf(r.DoneAt, r.At))
		if !in(d) {
			continue
		}
		ev := "Returned to the client (unpledge)"
		if r.Kind == "CF" {
			ev = "Invoked (" + firstOf(r.Reason, "invocation") + ")"
		}
		if r.Of == cuspaOf {
			ev += ", unpaid securities (CUSPA)"
		}
		out = append(out, CollRegRow{Date: d, Time: tm, UCC: r.UCC, Name: firstOf(r.Name, cs[r.UCC].Name), Event: ev, Ref: r.ID, PSN: r.PSN, ISIN: r.ISIN, Symbol: r.Symbol, Qty: fmt.Sprint(r.Qty), Value: r.Value, Status: r.Status,
			Ack: map[string]string{"Client": "Client's unpledge request", "Desk": "Unpledge asked at the desk"}[r.Source], Maker: r.RequestedBy, Check: r.ApprovedBy})
	}
	// Corporate action benefits credited on securities pledged to the firm.
	ts, _ := docs.List[DPTxn](ctx, o.Docs, tenant, cDPTxn, "")
	for _, t := range ts {
		k := strings.ToLower(t.Kind)
		if !in(t.Date) || !strings.Contains(k, "corporate action") && !strings.Contains(k, "bonus") && !strings.Contains(k, "split") || t.Qty.Sign() <= 0 || !pledged[t.UCC+"|"+t.ISIN] {
			continue
		}
		out = append(out, CollRegRow{Date: t.Date, UCC: t.UCC, Name: cs[t.UCC].Name, Event: "Corporate action benefit credited on pledged securities", Ref: firstOf(t.Ref, t.ID), ISIN: t.ISIN, Symbol: t.Symbol, Qty: fmtQty(t.Qty), Status: "Credited", Ack: t.Kind})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Date != out[j].Date {
			return out[i].Date < out[j].Date
		}
		return out[i].Time < out[j].Time
	})
	return out, nil
}

// CollateralRegisterCSV is the register as a CSV file.
func (o *Ops) CollateralRegisterCSV(ctx context.Context, tenant, actor, from, to string) (string, []byte, error) {
	rs, err := o.CollateralRegister(ctx, tenant, actor, from, to)
	if err != nil {
		return "", nil, err
	}
	rows := [][]string{{"Date", "Time", "UCC", "Client", "Event", "Reference", "PSN", "ISIN", "Security", "Quantity", "Value", "Status", "Acknowledgement / authorisation", "Maker", "Checker", "Set-up letter"}}
	for _, r := range rs {
		rows = append(rows, []string{r.Date, r.Time, r.UCC, r.Name, r.Event, r.Ref, r.PSN, r.ISIN, r.Symbol, r.Qty, r.Value, r.Status, r.Ack, r.Maker, r.Check, r.Letter})
	}
	o.log(ctx, tenant, actor, "collateral.register.exported", from+".."+to, map[string]any{"rows": len(rs)})
	return fmt.Sprintf("CollateralRegister_%s_%s.csv", from, to), csvBytesOf(rows), nil
}

// ---- the statement of collateral by type ----

// collateralSection is the margin statement's collateral by type: what is
// deposited, what the day's margin used and what is left. The margin is
// met from cash first, then from securities (counted up to the cash, as
// the exchanges allow). Ledgerline keeps no fixed deposits or bank
// guarantees of clients, so they show nil.
func collateralSection(r MarginRow) Section {
	need := r.EOD
	cash := money.Max(r.Funds, money.Dec{})
	sec := money.Max(r.Collateral, money.Dec{})
	uc := money.Min(cash, need)
	us := money.Min(sec, money.Max(need.Sub(uc), money.Dec{}))
	zero := money.Dec{}
	status := func(dep, used money.Dec) string {
		switch {
		case dep.Sign() == 0:
			return "None held"
		case used.Cmp(dep) >= 0:
			return "Fully utilised"
		case used.Sign() > 0:
			return "Partly utilised; balance available"
		}
		return "Available"
	}
	s := Section{Title: "Collateral by type", Columns: []Column{{"type", "Collateral", ColText}, {"deposited", "Deposited", ColMoney}, {"utilised", "Utilised", ColMoney}, {"available", "Available", ColMoney}, {"status", "Status", ColText}}}
	s.Rows = [][]string{
		{"Cash (funds with the firm and UPI blocks)", formatINR(cash), formatINR(uc), formatINR(cash.Sub(uc)), status(cash, uc)},
		{"Fixed deposit receipts", formatINR(zero), formatINR(zero), formatINR(zero), "None held"},
		{"Bank guarantees", formatINR(zero), formatINR(zero), formatINR(zero), "None held"},
		{"Securities pledged (after haircut)", formatINR(sec), formatINR(us), formatINR(sec.Sub(us)), status(sec, us)},
	}
	if r.CUSPA.Sign() > 0 {
		s.Rows = append(s.Rows, []string{"of which unpaid securities in the CUSPA", formatINR(r.CUSPA), "", "", "Counted as margin; no exposure"})
	}
	s.Totals = []string{"Total", formatINR(cash.Add(sec)), formatINR(uc.Add(us)), formatINR(cash.Add(sec).Sub(uc.Add(us))), map[bool]string{true: "Due: ₹" + formatINR(r.Short) + " short at peak", false: "Available"}[r.Short.Sign() > 0]}
	s.Note = "Utilised is the end-of-day margin, met from cash first and then from securities."
	return s
}

// ---- pledge set-up letters ----

// pledgeLetterMap is, by pledge request, the dispatch of its letter.
func (o *Ops) pledgeLetterMap(ctx context.Context, tenant string) map[string]string {
	ds, _ := docs.List[Dispatch](ctx, o.Docs, tenant, cDispatch, "")
	m := map[string]string{}
	for _, d := range ds {
		if d.Kind == DocPledgeLetter && (d.Status == "Sent" || strings.HasPrefix(d.Status, "Physical")) {
			m[d.Ref] = d.ID
		}
	}
	return m
}

// pledgeLetter is the letter to the pledgor for a pledge set up.
func (o *Ops) pledgeLetter(ctx context.Context, tenant string, p PledgeRequest) (ClientReport, error) {
	c, err := o.Client(ctx, tenant, p.UCC)
	if err != nil {
		return ClientReport{}, err
	}
	set, _ := o.Settings(ctx, tenant)
	d, tm := istDay(firstOf(p.DoneAt, p.Time))
	rep := ClientReport{Kind: "pledge", Title: "Pledge set-up letter", UCC: c.UCC, Name: c.Name, PAN: MaskPAN(c.PAN), To: d, Period: "Pledge set up on " + d, Generated: o.now().Format("02-Jan-2006 15:04 IST")}
	rep.Summary = []SummaryItem{{Label: "PSN", Value: p.PSN, Type: ColText}, {Label: "Security", Value: p.Symbol + " (" + p.ISIN + ")", Type: ColText}, {Label: "Quantity", Value: fmt.Sprint(p.Qty), Type: ColText}}
	kind := "Margin pledge"
	if p.Kind == "MF" {
		kind = "Margin trading funding pledge"
	}
	rep.Sections = []Section{{Title: "Pledge", Columns: []Column{{"item", "Item", ColText}, {"value", "Details", ColText}}, Rows: [][]string{
		{"Pledgor", c.Name + " (" + c.UCC + ")"}, {"Pledgor's demat account", firstOf(p.BOID, primaryDemat(c).ID)}, {"Pledgee", set.Firm + " (trading member, client securities margin pledge account)"},
		{"Type", kind}, {"Pledge sequence number (PSN)", p.PSN}, {"Security", p.Symbol + " · " + p.ISIN}, {"Quantity", fmt.Sprint(p.Qty)},
		{"Set up on", strings.TrimSpace(d + " " + tm)}, {"Expiry", firstOf(p.Expiry, "until released")}, {"Our reference", p.ID},
	}}}
	rep.Notes = []string{"The securities stay in your demat account, pledged to us as margin. You may ask for them to be released at any time; we release them unless they are needed for your obligations or margin.",
		"If you did not ask for this pledge, write to us at once at " + firstOf(set.ComplianceEmail, "our compliance desk") + ". This letter is digitally signed for " + set.Firm + "."}
	return rep, nil
}

// SendPledgeLetters sends the set-up letter of every pledge set up and not
// yet sent one (Ops maker or checker, Risk; the scheduler).
func (o *Ops) SendPledgeLetters(ctx context.Context, tenant, actor string) (SendStatementsResult, error) {
	res := SendStatementsResult{Kind: DocPledgeLetter}
	if actor != SystemActor {
		if err := o.require(ctx, tenant, actor, "Sending pledge set-up letters", RoleMaker, RoleChecker, RoleRisk); err != nil {
			return res, err
		}
	}
	set, _ := o.Settings(ctx, tenant)
	for _, p := range o.pledgeLettersDue(ctx, tenant) {
		if o.delivered(ctx, tenant, DocPledgeLetter, p.ID, p.UCC) {
			res.Skipped++
			continue
		}
		rep, err := o.pledgeLetter(ctx, tenant, p)
		if err != nil {
			res.add(Dispatch{}, err)
			continue
		}
		d, _ := istDay(firstOf(p.DoneAt, p.Time))
		res.add(o.dispatchDoc(ctx, tenant, actor, docRequest{Kind: DocPledgeLetter, Ref: p.ID, Date: d, UCC: p.UCC, Report: reportPDF(rep, set), Title: "Pledge set-up letter " + p.PSN,
			FileName: "PledgeLetter_" + p.UCC + "_" + p.PSN + ".pdf", Text: fmt.Sprintf("Dear %s,\n\nYour pledge of %d %s (PSN %s) has been set up. Please find attached our letter confirming it.", rep.Name, p.Qty, p.Symbol, p.PSN)}))
	}
	o.closeWork(ctx, tenant, actor, wPledgeCtl, "letters", fmt.Sprintf("%d sent", res.Sent))
	return res, nil
}

// pledgeLettersDue are the pledges set up (with a PSN) in the last 60 days
// with no letter sent.
func (o *Ops) pledgeLettersDue(ctx context.Context, tenant string) []PledgeRequest {
	sent := o.pledgeLetterMap(ctx, tenant)
	from := addDays(o.now().In(ist).Format("2006-01-02"), -60)
	ps, _ := docs.List[PledgeRequest](ctx, o.Docs, tenant, cPledge, "")
	var out []PledgeRequest
	for _, p := range ps {
		d, _ := istDay(firstOf(p.DoneAt, p.Time))
		if p.Status == "Accepted" && p.PSN != "" && sent[p.ID] == "" && d >= from {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ---- the daily reconciliation ----

// PledgeReconRow is one kind of request on a day.
type PledgeReconRow struct {
	Kind       string `json:"kind"`
	Received   int    `json:"received"`
	Executed   int    `json:"executed"`
	NotDone    int    `json:"not_done"` // rejected, failed, lapsed, cancelled
	Pending    int    `json:"pending"`  // received by the day and still open
	Entered    int    `json:"entered"`  // maker entries of the day
	Verified   int    `json:"verified"` // of them, verified by a checker
	Unverified int    `json:"unverified"`
}

// PledgeOpen is a request still open at the end of the day.
type PledgeOpen struct {
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	UCC    string `json:"ucc"`
	Since  string `json:"since"`
	Status string `json:"status"`
}

// PledgeRecon is the day's reconciliation.
type PledgeRecon struct {
	Date string           `json:"date"`
	Rows []PledgeReconRow `json:"rows"`
	Open []PledgeOpen     `json:"open"`
}

// PledgeDailyRecon reconciles the day's pledge, re-pledge, unpledge and
// invocation requests.
func (o *Ops) PledgeDailyRecon(ctx context.Context, tenant, date string) PledgeRecon {
	v := PledgeRecon{Date: date, Rows: []PledgeReconRow{}, Open: []PledgeOpen{}}
	type item struct {
		kind, id, ucc, at, done, status, maker, checker string
		open, ok, bad                                   bool
	}
	var items []item
	ps, _ := docs.List[PledgeRequest](ctx, o.Docs, tenant, cPledge, "")
	for _, p := range ps {
		items = append(items, item{kind: "Margin pledge", id: p.ID, ucc: p.UCC, at: p.Time, done: p.DoneAt, status: p.Status, maker: firstOf(p.RequestedBy, "client"), checker: p.DecidedBy,
			open: p.Status == "Pending" || p.Status == "Awaiting client", ok: p.Status == "Accepted", bad: p.Status == "Rejected" || p.Status == "Failed"})
	}
	for _, b := range o.cplRepledges(ctx, tenant) {
		items = append(items, item{kind: "Re-pledge", id: b.ID, at: b.At, done: b.SentAt, status: b.Status, maker: b.Maker, checker: b.Checker,
			open: b.Status == "Pending" || b.Status == "Sent" || b.Status == "No reply", ok: b.Status == "Done" || b.Status == "Partly done", bad: b.Status == "Rejected" || b.Status == "Failed"})
	}
	for _, n := range o.cplNormals(ctx, tenant) {
		items = append(items, item{kind: "Pledge to a lender", id: n.ID, ucc: n.UCC, at: n.At, done: n.SentAt, status: n.Status, maker: n.Maker, checker: n.Checker,
			open: n.Status == "Pending" || n.Status == "Sent" || n.Status == "No reply", ok: n.Status == "Done" || n.Status == "Partly done", bad: n.Status == "Rejected" || n.Status == "Failed"})
	}
	for _, r := range o.cplReleases(ctx, tenant) {
		k := map[string]string{"UN": "Unpledge", "CF": "Invocation"}[r.Kind]
		items = append(items, item{kind: k, id: r.ID, ucc: r.UCC, at: r.At, done: r.DoneAt, status: r.Status, maker: r.RequestedBy, checker: r.ApprovedBy,
			open: !cplReleaseClosed(r.Status) && r.Status != "Done", ok: r.Status == "Done", bad: cplReleaseClosed(r.Status) && r.Status != "Done"})
	}
	rows := map[string]*PledgeReconRow{}
	var order []string
	for _, k := range []string{"Margin pledge", "Re-pledge", "Pledge to a lender", "Unpledge", "Invocation"} {
		rows[k], order = &PledgeReconRow{Kind: k}, append(order, k)
	}
	for _, it := range items {
		r := rows[it.kind]
		if r == nil {
			continue
		}
		at, _ := istDay(it.at)
		done, _ := istDay(it.done)
		if at == "" || at > date {
			continue
		}
		if at == date {
			r.Received++
			r.Entered++
			if it.checker != "" && it.checker != it.maker {
				r.Verified++
			} else if !it.open || it.status != "Pending" && it.status != "Awaiting compliance" {
				// A request decided with no checker of its own on record.
				if it.checker == "" && it.kind != "Margin pledge" {
					r.Unverified++
				}
			}
		}
		switch {
		case it.open:
			r.Pending++
			v.Open = append(v.Open, PledgeOpen{Kind: it.kind, ID: it.id, UCC: it.ucc, Since: at, Status: it.status})
		case done == date && it.ok:
			r.Executed++
		case done == date && it.bad:
			r.NotDone++
		}
	}
	for _, k := range order {
		v.Rows = append(v.Rows, *rows[k])
	}
	sort.SliceStable(v.Open, func(i, j int) bool { return v.Open[i].Since < v.Open[j].Since })
	return v
}

// ---- lock-in ----

// LockIn is a locked-in balance in a client's demat account.
type LockIn struct {
	UCC    string `json:"ucc"`
	BOID   string `json:"bo_id,omitempty"`
	ISIN   string `json:"isin"`
	Qty    int64  `json:"qty"`
	Until  string `json:"until,omitempty"` // the lock-in release date
	Source string `json:"source"`          // CDSL statement of holding, or Recorded
	Date   string `json:"date"`            // as of
	By     string `json:"by,omitempty"`
}

func lockKey(ucc, isin, bo string) string { return ucc + "|" + isin + "|" + bo }

// noteLockIns keeps the lock-in balances of CDSL's statement of holding
// (LckdInBal, LckInRlsDt): what the file has replaces what an earlier file
// gave.
func (o *Ops) noteLockIns(ctx context.Context, tenant, actor string, rows []xfile.Row) {
	byBO := o.boClients(ctx, tenant)
	day := ""
	seen := map[string]bool{}
	for _, r := range rows {
		if d := strings.TrimSpace(r.Get("StmtDt")); validDate(d) && d > day {
			day = d
		}
	}
	day = firstOf(day, o.now().In(ist).Format("2006-01-02"))
	for _, r := range rows {
		bo := normDematID(r.Get("BnfclOwnrId"))
		ucc, ok := byBO[bo]
		q := decOr0(r.Get("LckdInBal"))
		if !ok || q.Sign() <= 0 {
			continue
		}
		isin := strings.ToUpper(strings.TrimSpace(r.Get("ISIN")))
		k := lockKey(ucc, isin, bo)
		seen[k] = true
		until := strings.TrimSpace(r.Get("LckInRlsDt"))
		if !validDate(until) {
			until = ""
		}
		_ = docs.Upsert(ctx, o.Docs, tenant, cLockIn, k, LockIn{UCC: ucc, BOID: bo, ISIN: isin, Qty: int64(q.Float64()), Until: until, Source: "CDSL statement of holding", Date: day}, actor)
	}
	old, _ := docs.List[LockIn](ctx, o.Docs, tenant, cLockIn, "")
	for _, l := range old {
		k := lockKey(l.UCC, l.ISIN, l.BOID)
		if l.Source == "CDSL statement of holding" && !seen[k] && l.Qty > 0 && l.Date < day {
			l.Qty, l.Date = 0, day
			_ = docs.Upsert(ctx, o.Docs, tenant, cLockIn, k, l, actor)
		}
	}
}

// RecordLockIn records a lock-in the statement of holding does not show
// (Ops maker or checker, Risk).
func (o *Ops) RecordLockIn(ctx context.Context, tenant, actor string, l LockIn) (LockIn, error) {
	if err := o.require(ctx, tenant, actor, "Recording a lock-in", RoleMaker, RoleChecker, RoleRisk); err != nil {
		return l, err
	}
	l.UCC, l.ISIN, l.BOID = strings.ToUpper(strings.TrimSpace(l.UCC)), strings.ToUpper(strings.TrimSpace(l.ISIN)), normDematID(l.BOID)
	if _, err := o.Client(ctx, tenant, l.UCC); err != nil {
		return l, err
	}
	if len(l.ISIN) != 12 || l.Qty < 0 || l.Until != "" && !validDate(l.Until) {
		return l, invalid("give the ISIN, the quantity locked in and the date the lock-in ends (YYYY-MM-DD)")
	}
	l.Source, l.Date, l.By = "Recorded", o.now().In(ist).Format("2006-01-02"), actor
	err := docs.Upsert(ctx, o.Docs, tenant, cLockIn, lockKey(l.UCC, l.ISIN, l.BOID), l, actor)
	o.log(ctx, tenant, actor, "lockin.recorded", l.UCC, map[string]any{"isin": l.ISIN, "qty": l.Qty, "until": l.Until})
	return l, err
}

// lockedIn is the client's quantity of an ISIN locked in on date.
func (o *Ops) lockedIn(ctx context.Context, tenant, ucc, isin, date string) (int64, string) {
	ls, _ := docs.List[LockIn](ctx, o.Docs, tenant, cLockIn, ucc+"|"+isin+"|")
	var q int64
	until := ""
	for _, l := range ls {
		if l.Qty > 0 && (l.Until == "" || l.Until > date) {
			q += l.Qty
			if l.Until > until {
				until = l.Until
			}
		}
	}
	return q, until
}

// lockInWhy says why q shares of a PSN may not be invoked on date ("" when
// they may): CDSL's instructions bar invoking locked-in securities before
// the lock-in ends. The locked-in quantity is taken as part of what is
// pledged, the safe reading.
func (o *Ops) lockInWhy(ctx context.Context, tenant string, a PSNAvail, q int64, date string) string {
	l, until := o.lockedIn(ctx, tenant, a.UCC, a.ISIN, date)
	if l <= 0 || q <= a.Free-l {
		return ""
	}
	u := "no end date on record"
	if until != "" {
		u = "until " + fmtDay(until)
	}
	return fmt.Sprintf("%d %s of %s are locked in (%s); only %d may be invoked", l, a.Symbol, a.UCC, u, max(a.Free-l, 0))
}

// ---- the confiscation-cum-early pay-in cut-off ----

// CEPRow is an unpaid-securities line sold, with when its confiscation was
// done against the 9:45 a.m. cut-off of the pay-in day.
type CEPRow struct {
	CUSPA  string `json:"cuspa"`
	UCC    string `json:"ucc"`
	Symbol string `json:"symbol"`
	Sold   int64  `json:"sold"`
	SoldOn string `json:"sold_on"`
	PayIn  string `json:"pay_in"`
	DoneAt string `json:"done_at,omitempty"`
	State  string `json:"state"` // On time, Late, Due, Missed
}

// cepRows are the lines sold between two dates.
func (o *Ops) cepRows(ctx context.Context, tenant, from, to string, now time.Time) []CEPRow {
	set, _ := o.Settings(ctx, tenant)
	items, _ := docs.List[CUSPAItem](ctx, o.Docs, tenant, cCUSPA, "")
	done := map[string]string{} // CUSPA line → when its invocation was done at CDSL
	for _, r := range o.cplReleases(ctx, tenant) {
		if r.Kind == "CF" && r.CUSPA != "" && r.Status == "Done" && r.DoneAt != "" && (done[r.CUSPA] == "" || r.DoneAt < done[r.CUSPA]) {
			done[r.CUSPA] = r.DoneAt
		}
	}
	var out []CEPRow
	for _, it := range items {
		if it.Sold <= 0 || it.SoldOn == "" || it.SoldOn < from || it.SoldOn > to {
			continue
		}
		payIn := set.NextTradingDay(PartEquity, it.SoldOn)
		cut, _ := time.ParseInLocation("2006-01-02 15:04", payIn+" "+cepCutoff, ist)
		r := CEPRow{CUSPA: it.ID, UCC: it.UCC, Symbol: it.Symbol, Sold: it.Sold, SoldOn: it.SoldOn, PayIn: payIn}
		switch at := done[it.ID]; {
		case at != "":
			r.DoneAt = at
			t, _ := time.Parse(time.RFC3339, at)
			r.State = map[bool]string{true: "On time", false: "Late"}[!t.After(cut)]
		case it.InvokedOn != "" && it.InvokedOn <= it.SoldOn:
			// Invoked on CDSL's system before the sale: done in time.
			r.DoneAt, r.State = it.InvokedOn, "On time"
		case it.InvokedOn != "" && it.InvokedOn > payIn:
			r.DoneAt, r.State = it.InvokedOn, "Late"
		case it.InvokedOn == payIn:
			r.DoneAt, r.State = it.InvokedOn, "On time" // on the pay-in day by CDSL's system; the time is not known
		case now.After(cut):
			r.State = "Missed"
		default:
			r.State = "Due"
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SoldOn+out[i].CUSPA < out[j].SoldOn+out[j].CUSPA })
	return out
}

// ---- margin pledge charges at actual cost ----

// PledgeCosts are the depository's charges to the DP for each margin
// pledge event (₹ per instruction per ISIN, before GST), as CDSL's tariff
// gives them, and where they come from.
type PledgeCosts struct {
	Costs     map[string]money.Amount `json:"costs"` // PLGM, UNPM, INVM, RPLG
	Source    string                  `json:"source"`
	UpdatedBy string                  `json:"updated_by,omitempty"`
	UpdatedAt string                  `json:"updated_at,omitempty"`
}

// marginPledgeEvents are the events recovered from the client at actual
// cost.
var marginPledgeEvents = []string{MarginPledgeCreated, MarginPledgeClosed, MarginPledgeInvoked, MarginRepledge}

func (o *Ops) PledgeCostsNow(ctx context.Context, tenant string) PledgeCosts {
	c, _, _ := docs.GetOr(ctx, o.Docs, tenant, cPledgeCost, "main", PledgeCosts{})
	if c.Costs == nil {
		c.Costs = map[string]money.Amount{}
	}
	return c
}

// SavePledgeCosts records them (Admin or Ops checker, as the DP tariff).
func (o *Ops) SavePledgeCosts(ctx context.Context, tenant, actor string, c PledgeCosts) (PledgeCosts, error) {
	if err := o.require(ctx, tenant, actor, "Recording the depository's pledge charges", RoleChecker); err != nil {
		return c, err
	}
	for k, v := range c.Costs {
		if !contains(marginPledgeEvents, k) || v < 0 {
			return c, invalid("costs are for %s, and not below nil", strings.Join(marginPledgeEvents, ", "))
		}
	}
	if strings.TrimSpace(c.Source) == "" {
		return c, invalid("say where the charges come from (CDSL's tariff circular)")
	}
	c.UpdatedBy, c.UpdatedAt = actor, o.now().Format(time.RFC3339)
	err := docs.Upsert(ctx, o.Docs, tenant, cPledgeCost, "main", c, actor)
	o.log(ctx, tenant, actor, "pledge.costs.saved", "main", map[string]any{"source": c.Source})
	return c, err
}

// PledgeChargeIssue is a tariff line above the depository's charge.
type PledgeChargeIssue struct {
	Where string `json:"where"` // Firm tariff, or the plan
	Event string `json:"event"`
	What  string `json:"what"`
}

// pledgeChargeIssues checks the firm's tariff and every DP plan's rates in
// force on date against the recorded costs.
func (o *Ops) pledgeChargeIssues(ctx context.Context, tenant, date string) ([]PledgeChargeIssue, bool) {
	costs := o.PledgeCostsNow(ctx, tenant)
	var out []PledgeChargeIssue
	check := func(where string, lines []DPChargeLine) {
		for _, l := range lines {
			if !contains(marginPledgeEvents, l.Code) || l.Off {
				continue
			}
			cost, ok := costs.Costs[l.Code]
			name := firstOf(dpChargeCodes[l.Code], l.Code)
			switch {
			case !ok:
				if l.Flat > 0 || l.Pct.Sign() > 0 || l.Min > 0 {
					out = append(out, PledgeChargeIssue{where, name, "charged, but the depository's charge for it is not recorded"})
				}
			case l.Pct.Sign() > 0:
				out = append(out, PledgeChargeIssue{where, name, fmt.Sprintf("charged as %s%% of value; the depository charges ₹%s an instruction", l.Pct.String(), formatINR(cost.Dec()))})
			case l.Flat > cost || l.Min > cost:
				out = append(out, PledgeChargeIssue{where, name, fmt.Sprintf("₹%s an instruction, above the depository's ₹%s", formatINR(max(l.Flat, l.Min).Dec()), formatINR(cost.Dec()))})
			}
		}
	}
	if t, err := o.DPTariffNow(ctx, tenant); err == nil {
		check("Firm tariff", t.PledgeCharges)
	}
	ps, _ := o.DPPlans(ctx, tenant)
	for _, p := range ps {
		if v := p.at(date); v != nil && !p.Pool {
			check("Plan "+p.Code, v.Lines)
		}
	}
	return out, len(costs.Costs) > 0
}

// ---- the screen ----

// PledgeControlsView is Pledges (CDSL) → Controls.
type PledgeControlsView struct {
	Date        string              `json:"date"`
	Recon       PledgeRecon         `json:"recon"`
	LettersDue  []PledgeRequest     `json:"letters_due"`
	LockIns     []LockIn            `json:"lock_ins"`
	CEP         []CEPRow            `json:"cep"`
	Costs       PledgeCosts         `json:"costs"`
	Charges     []PledgeChargeIssue `json:"charges"`
	CostsKnown  bool                `json:"costs_known"`
	Events      map[string]string   `json:"events"`
	CanSend     bool                `json:"can_send"`
	CanCosts    bool                `json:"can_costs"`
	CanLockIn   bool                `json:"can_lock_in"`
	CanRegister bool                `json:"can_register"`
}

// PledgeControls builds it.
func (o *Ops) PledgeControls(ctx context.Context, tenant, actor, date string) (PledgeControlsView, error) {
	if !validDate(date) {
		date = o.businessDate(ctx, tenant)
	}
	v := PledgeControlsView{Date: date, LettersDue: []PledgeRequest{}, LockIns: []LockIn{}, CEP: []CEPRow{}, Charges: []PledgeChargeIssue{}, Events: map[string]string{}}
	if err := o.require(ctx, tenant, actor, "Viewing pledge controls", RoleRisk, RoleCompliance, RoleMaker, RoleChecker); err != nil {
		return v, err
	}
	v.CanSend = o.require(ctx, tenant, actor, "", RoleMaker, RoleChecker, RoleRisk) == nil
	v.CanCosts = o.require(ctx, tenant, actor, "", RoleChecker) == nil
	v.CanLockIn = v.CanSend
	v.CanRegister = true
	v.Recon = o.PledgeDailyRecon(ctx, tenant, date)
	v.LettersDue = append(v.LettersDue, o.pledgeLettersDue(ctx, tenant)...)
	ls, _ := docs.List[LockIn](ctx, o.Docs, tenant, cLockIn, "")
	for _, l := range ls {
		if l.Qty > 0 && (l.Until == "" || l.Until > date) {
			v.LockIns = append(v.LockIns, l)
		}
	}
	v.CEP = append(v.CEP, o.cepRows(ctx, tenant, addDays(date, -30), date, o.now())...)
	v.Costs = o.PledgeCostsNow(ctx, tenant)
	cs, known := o.pledgeChargeIssues(ctx, tenant, date)
	v.Charges, v.CostsKnown = append(v.Charges, cs...), known
	for _, e := range marginPledgeEvents {
		v.Events[e] = dpChargeCodes[e]
	}
	return v, nil
}

// ---- the daily run ----

type pledgeCtlState struct {
	Day    string `json:"day"`
	CEPDay string `json:"cep_day,omitempty"`
}

// pledgeControlsTick runs from the CDSL pledge tick. On a trading day from
// 9 a.m. it warns of confiscations due by 9:45; after 6 p.m., once a day,
// it raises the reconciliation's open requests and the set-up letters not
// sent.
func (o *Ops) pledgeControlsTick(ctx context.Context, tenant string, now time.Time) {
	t := now.In(ist)
	day, hm := t.Format("2006-01-02"), t.Format("15:04")
	set, _ := o.Settings(ctx, tenant)
	if !set.IsBusinessDay(day) {
		return
	}
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cPledgeCtlState, "tick", pledgeCtlState{})
	if hm >= "09:00" && hm < "10:00" && st.CEPDay != day {
		st.CEPDay = day
		_ = docs.Upsert(ctx, o.Docs, tenant, cPledgeCtlState, "tick", st, SystemActor)
		for _, r := range o.cepRows(ctx, tenant, set.PrevTradingDay(PartEquity, day), set.PrevTradingDay(PartEquity, day), now) {
			if r.State == "Due" {
				o.workDue(ctx, tenant, wPledgeCtl, "cep:"+r.CUSPA+":"+r.PayIn, "Confiscation-cum-early pay-in of "+r.UCC+" by 9:45", fmt.Sprintf("%d %s of CUSPA line %s were sold on %s. CDSL executes the confiscation-cum-early pay-in by 9:45 a.m. on the pay-in day (%s); after that only on a best-effort basis. Give it now.", r.Sold, r.Symbol, r.CUSPA, fmtDay(r.SoldOn), fmtDay(r.PayIn)), r.UCC, RoleRisk, "settlement", day, day+" "+cepCutoff)
			}
		}
	}
	if hm < "18:00" || st.Day == day {
		return
	}
	st.Day = day
	_ = docs.Upsert(ctx, o.Docs, tenant, cPledgeCtlState, "tick", st, SystemActor)
	rc := o.PledgeDailyRecon(ctx, tenant, day)
	old := 0
	for _, x := range rc.Open {
		if x.Since < day {
			old++
		}
	}
	if old > 0 {
		o.raiseWork(ctx, tenant, wPledgeCtl, "recon:"+day, fmt.Sprintf("Pledge reconciliation %s: %d request%s open from earlier days", fmtDay(day), old, plural(old)),
			"CDSL's operating instructions (8.8) ask for a daily reconciliation of pledge, unpledge and invocation requests received, executed and pending. Clear or follow up the open ones on Pledges (CDSL) → Controls.", "", RoleRisk, "cdslpledge", day)
	}
	if n := len(o.pledgeLettersDue(ctx, tenant)); n > 0 {
		o.raiseWork(ctx, tenant, wPledgeCtl, "letters", fmt.Sprintf("Send %d pledge set-up letter%s", n, plural(n)),
			"CDSL's operating instructions (8.5.2) ask for a signed pledge set-up letter to the pledgor for each pledge set up. Send them from Pledges (CDSL) → Controls.", "", RoleMaker, "cdslpledge", day)
	}
}

// ---- self-inspection ----

func init() {
	siTests = append(siTests,
		SITest{ID: "pledge_letters", Title: "Pledge set-up letter sent for each pledge", Area: "Depository", Severity: "low", Owner: RoleMaker,
			Source: "CDSL operating instructions for DPs, 8.5.2", Checks: "Pledges set up in the last 30 days: a signed set-up letter sent to the pledgor.",
			Fix: "Send the letters from Pledges (CDSL) → Controls.", Screen: "cdslpledge", ScreenName: "Collateral → Pledges (CDSL) → Controls", Real: true, run: siPledgeLetters},
		SITest{ID: "pledge_recon", Title: "Pledge requests do not stay open", Area: "Depository", Severity: "medium", Owner: RoleRisk,
			Source: "CDSL operating instructions for DPs, 8.8", Checks: "Pledge, re-pledge, unpledge and invocation requests open for more than 3 days, and requests executed without a checker other than the maker.",
			Fix: "Follow up or close the open requests on Pledges (CDSL) → Controls.", Screen: "cdslpledge", ScreenName: "Collateral → Pledges (CDSL) → Controls", Real: true, run: siPledgeRecon},
		SITest{ID: "cuspa_cep_cutoff", Title: "Confiscation-cum-early pay-in given by 9:45 a.m. on the pay-in day", Area: "Settlement", Severity: "medium", Owner: RoleRisk,
			Source: "CDSL master circular CDSL/OPS/DP/POLCY/2026/257, 4.10.2.1 and 4.10.4.3", Checks: "Unpaid securities sold in the last 30 days: their confiscation done at CDSL by 9:45 a.m. on the next trading day.",
			Fix: "Give confiscations of sold CUSPA lines first thing on the pay-in day (the 9 a.m. work item).", Screen: "cdslpledge", ScreenName: "Collateral → Pledges (CDSL) → Controls", Real: true, run: siCEPCutoff},
		SITest{ID: "pledge_charges_at_cost", Title: "Margin pledge charges no more than the depository's", Area: "Depository", Severity: "medium", Owner: RoleChecker,
			Source: "NSE/INSP/73976 (inspection master circular), Exhibit 17 Q27", Checks: "The firm tariff's and each DP plan's margin pledge, unpledge, re-pledge and invocation charges against CDSL's charges recorded on Pledges (CDSL) → Controls.",
			Fix: "Record CDSL's charges, and bring the tariff and plan rates down to them.", Screen: "cdslpledge", ScreenName: "Collateral → Pledges (CDSL) → Controls", Real: true, run: siPledgeCharges},
	)
}

func siPledgeLetters(e *siEnv) (siOut, error) {
	var s siOut
	s.input("Pledges (CDSL)", e.date)
	sent := e.o.pledgeLetterMap(e.ctx, e.tenant)
	from := addDays(e.date, -siWindow)
	ps, _ := docs.List[PledgeRequest](e.ctx, e.o.Docs, e.tenant, cPledge, "")
	for _, p := range ps {
		d, _ := istDay(firstOf(p.DoneAt, p.Time))
		if p.Status != "Accepted" || p.PSN == "" || d < from || d > e.date {
			continue
		}
		s.checked++
		if sent[p.ID] == "" {
			s.bad(fmt.Sprintf("Pledge %s · %s · PSN %s set up %s: no letter sent", p.ID, siClient(p.UCC), p.PSN, fmtDay(d)))
		}
	}
	s.summary = fmt.Sprintf("%d pledges set up in the last %d days; %d without a set-up letter.", s.checked, siWindow, s.found)
	return s, nil
}

func siPledgeRecon(e *siEnv) (siOut, error) {
	var s siOut
	s.input("Pledges (CDSL)", e.date)
	rc := e.o.PledgeDailyRecon(e.ctx, e.tenant, e.date)
	for _, x := range rc.Open {
		s.checked++
		if x.Since != "" && x.Since < addDays(e.date, -3) {
			s.bad(fmt.Sprintf("%s %s · %s open since %s (%s)", x.Kind, x.ID, siClient(x.UCC), fmtDay(x.Since), x.Status))
		}
	}
	for _, r := range rc.Rows {
		s.checked += r.Received
		if r.Unverified > 0 {
			s.bad(fmt.Sprintf("%s: %d of today's requests decided without a checker", r.Kind, r.Unverified))
		}
	}
	s.summary = fmt.Sprintf("%d requests looked at; %d open too long or not verified.", s.checked, s.found)
	return s, nil
}

func siCEPCutoff(e *siEnv) (siOut, error) {
	var s siOut
	s.input("Unpaid securities (CUSPA)", e.date)
	for _, r := range e.o.cepRows(e.ctx, e.tenant, addDays(e.date, -siWindow), e.date, e.now) {
		s.checked++
		if r.State == "Late" || r.State == "Missed" {
			s.bad(fmt.Sprintf("CUSPA %s · %s · sold %s: confiscation %s (cut-off 9:45 on %s)", r.CUSPA, siClient(r.UCC), fmtDay(r.SoldOn), strings.ToLower(r.State), fmtDay(r.PayIn)))
		}
	}
	s.summary = fmt.Sprintf("%d unpaid-securities sales checked; %d confiscated after the 9:45 cut-off or not at all.", s.checked, s.found)
	return s, nil
}

func siPledgeCharges(e *siEnv) (siOut, error) {
	var s siOut
	s.input("DP tariff and plans", e.date)
	is, known := e.o.pledgeChargeIssues(e.ctx, e.tenant, e.date)
	s.checked = len(marginPledgeEvents)
	if !known {
		return siSkip("CDSL's charges for margin pledge events are not recorded yet (Pledges (CDSL) → Controls)", SIInput{Name: "DP tariff and plans", Date: e.date})
	}
	for _, i := range is {
		s.bad(i.Where + " · " + i.Event + ": " + i.What)
	}
	s.summary = fmt.Sprintf("%d margin pledge charges above the depository's.", s.found)
	return s, nil
}
