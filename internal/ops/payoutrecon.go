package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// ---- pay-out of securities: reconciliation and deadlines ----
//
// Pay-out of securities must go only to the client's own demat account,
// and the client-wise pay-out must match the actual depository transfers;
// mismatches are explained to the exchange when it asks (SEBI master
// circular for stock brokers, para 18.3). Securities (and funds) reach
// the client within 24 hours of pay-out unless the client agreed
// otherwise (para 24.3); a client without a running account authorisation
// is paid within 24 hours of pay-out (para 23.1.1); the DP moves pay-out
// securities from the pool to the client within one working day (CDSL
// master circular for DPs, 4.10.2.1). Where NSE Clearing credits the
// direct pay-out to the member's pool (no verified primary account, a
// depository rejection, an excess pay-in), the member transfers it to the
// client (NCL/CMPT/73996 item 6.2, 7.10(ii)).
//
// The reconciliation sets, per client and ISIN of a trade date:
//
//	Due        Ledgerline's net purchase, netted as the clearing
//	           corporation nets it (settlement.go)
//	Paid       NSE Clearing's direct pay-out file, to the client's own
//	           accounts; or Ledgerline's own pay-out where there is no file
//	Pool       pay-out credited to one of the firm's accounts instead
//	Depository the pay-out credits CDSL reports in the client's accounts
//	           with the firm's DP (its transaction file), when loaded
//
// and gives the reason for each difference. Client requests to have their
// securities transferred (para 23.1.1(i): within one working day when the
// firm holds them, three when they lie with the clearing member or
// corporation) are kept and timed here too.

const (
	cSecTransfer = "sectransfer" // request ID -> SecTransferRequest
)

// PayoutReconRow is one client's pay-out of one ISIN.
type PayoutReconRow struct {
	UCC        string `json:"ucc"`
	Name       string `json:"name"`
	ISIN       string `json:"isin"`
	Symbol     string `json:"symbol"`
	Due        int64  `json:"due"`
	Paid       int64  `json:"paid"`
	Pool       int64  `json:"pool"`
	Depository int64  `json:"depository"` // -1: not known (no CDSL transactions loaded)
	Ledgerline string `json:"ledgerline"` // the obligation's status
	OK         bool   `json:"ok"`
	Reason     string `json:"reason,omitempty"`
	// Action is what to do: transfer from the pool, explain, nothing.
	Action string `json:"action,omitempty"`
}

// PayoutRecon is a trade date's pay-out reconciliation.
type PayoutRecon struct {
	TradeDate  string           `json:"trade_date"`
	PayOut     string           `json:"pay_out"`
	FileLoaded bool             `json:"file_loaded"`
	File       string           `json:"file,omitempty"`
	CDSL       bool             `json:"cdsl"` // CDSL transactions of the pay-out days are loaded
	Rows       []PayoutReconRow `json:"rows"`
	Mismatches int              `json:"mismatches"`
}

// paidOutStatus says whether an obligation's status means the shares
// reached the client (or are held for it under the rules).
func paidOutStatus(s string) bool {
	return strings.HasPrefix(s, "Paid out") || s == CUSPAHeld || s == "Closed out"
}

// ReconcilePayout reconciles a trade date's pay-out of securities.
func (o *Ops) ReconcilePayout(ctx context.Context, tenant, date string) (PayoutRecon, error) {
	if !validDate(date) {
		return PayoutRecon{}, invalid("give the trade date (YYYY-MM-DD)")
	}
	set, _ := o.Settings(ctx, tenant)
	st, err := o.Obligations(ctx, tenant, date)
	if err != nil {
		return PayoutRecon{}, err
	}
	out := PayoutRecon{TradeDate: date, PayOut: st.PayIn, Rows: []PayoutReconRow{}}
	cs, _ := o.ClientMap(ctx, tenant)
	type acc struct {
		row      *PayoutReconRow
		rejected []string
		foreign  []string
		pool     []string
		short    bool
	}
	m := map[string]*acc{}
	at := func(u, isin, sym string) *acc {
		k := u + "|" + isin
		if m[k] == nil {
			m[k] = &acc{row: &PayoutReconRow{UCC: u, Name: cs[u].Name, ISIN: isin, Symbol: sym, Depository: -1}}
		}
		return m[k]
	}
	for _, ob := range st.Obligations {
		if ob.NetQty <= 0 || strings.HasPrefix(ob.ISIN, "UNKNOWN:") {
			continue
		}
		a := at(ob.UCC, ob.ISIN, ob.Symbol)
		a.row.Due += ob.NetQty
		a.row.Ledgerline = ob.Status
		a.short = a.short || ob.Status == "Awaiting auction" || ob.Status == "Awaiting close-out"
	}
	// NSE Clearing's direct pay-out file of the pay-out day.
	own := func(c Client, acct string) bool {
		acct = normDematID(acct)
		for _, d := range demats(c) {
			if normDematID(d.ID) == acct {
				return true
			}
		}
		return false
	}
	for _, d := range o.ccData(ctx, tenant, "directpayout", st.PayIn, "") {
		out.FileLoaded, out.File = true, d.File
		for _, r := range d.Rows {
			u, isin := strings.ToUpper(r["ClntId"]), strings.ToUpper(r["ISIN"])
			kind := strings.ToUpper(r["TxTp"] + " " + r["TxInd"])
			if u == "" || isin == "" || strings.Contains(kind, "SHRT") || strings.Contains(kind, "SHORT") {
				continue // a shortage line, not a pay-out
			}
			q := decOr0(r["QtyORShrtQty"])
			n := int64(q.Float64())
			a := at(u, isin, r["TckrSymb"])
			tgt := normDematID(r["TrgtDpstryAcct"])
			rj := strings.TrimSpace(r["DpstryConfRjctnCd"])
			switch {
			case rj != "" && rj != "0" && !strings.EqualFold(rj, "ACCEPTED") && !strings.EqualFold(rj, "SUCCESS"):
				a.row.Pool += n
				a.rejected = append(a.rejected, rj)
			case set.FirmDemats[tgt] != "":
				a.row.Pool += n
				a.pool = append(a.pool, set.FirmDemats[tgt])
			case tgt != "" && !own(cs[u], tgt):
				a.foreign = append(a.foreign, MaskAccount(tgt))
				a.row.Paid += n
			default:
				a.row.Paid += n
			}
		}
	}
	// CDSL's pay-out credits to the clients' accounts with the firm's DP,
	// on the pay-out day and the next working day.
	to := set.NextBusinessDay(st.PayIn)
	txs, _ := docs.List[DPTxn](ctx, o.Docs, tenant, cDPTxn, "")
	for _, t := range txs {
		if t.Date < st.PayIn || t.Date > to {
			continue
		}
		out.CDSL = true
		if t.Kind != "Pay-out from clearing" || t.Qty.Sign() <= 0 {
			continue
		}
		k := strings.ToUpper(t.UCC) + "|" + strings.ToUpper(t.ISIN)
		if a := m[k]; a != nil {
			if a.row.Depository < 0 {
				a.row.Depository = 0
			}
			a.row.Depository += int64(t.Qty.Float64())
		}
	}
	for _, k := range sortedKeys(m) {
		a := m[k]
		r := a.row
		if !out.FileLoaded && paidOutStatus(r.Ledgerline) {
			r.Paid = r.Due // the firm's own pay-out from its pool
		}
		if out.CDSL && r.Depository < 0 && cs[r.UCC].UCC != "" && o.firmDP(ctx, tenant).owns(primaryDemat(cs[r.UCC]).ID) {
			r.Depository = 0
		}
		var why []string
		switch {
		case len(a.rejected) > 0:
			why = append(why, fmt.Sprintf("the depository rejected the direct pay-out (%s), so it went to the pool", strings.Join(a.rejected, ", ")))
			r.Action = "Transfer from the pool to the client"
		case len(a.pool) > 0:
			why = append(why, fmt.Sprintf("credited to the firm's %s account, not the client's", strings.Join(a.pool, ", ")))
			r.Action = "Transfer from the pool to the client"
		}
		if len(a.foreign) > 0 {
			why = append(why, "paid to an account that is not one of the client's ("+strings.Join(a.foreign, ", ")+")")
			r.Action = firstOf(r.Action, "Explain to the exchange; correct the client's demat details")
		}
		switch {
		case r.Due == 0 && r.Paid+r.Pool > 0:
			why = append(why, "a pay-out with no purchase in Ledgerline")
			r.Action = firstOf(r.Action, "Check the trades: a missing trade or a wrong client code")
		case r.Due > r.Paid+r.Pool && a.short:
			why = append(why, fmt.Sprintf("%d short: waiting for the auction or close-out", r.Due-r.Paid-r.Pool))
		case r.Due > r.Paid+r.Pool && r.Ledgerline == CUSPAHeld && !out.FileLoaded:
			// unpaid purchase held in the client's account under CUSPA
		case r.Due > r.Paid+r.Pool && out.FileLoaded:
			why = append(why, fmt.Sprintf("due %d, NSE Clearing paid out %d", r.Due, r.Paid+r.Pool))
			r.Action = firstOf(r.Action, "Explain: check the obligation and the direct pay-out file")
		case r.Due > r.Paid+r.Pool:
			why = append(why, fmt.Sprintf("due %d, not paid out yet (%s)", r.Due, firstOf(r.Ledgerline, "no status")))
			r.Action = firstOf(r.Action, "Pay out on Settlement")
		case r.Paid+r.Pool > r.Due:
			why = append(why, fmt.Sprintf("due %d, paid out %d", r.Due, r.Paid+r.Pool))
			r.Action = firstOf(r.Action, "Explain: check the obligation and the direct pay-out file")
		}
		if r.Depository >= 0 && r.Paid > 0 && r.Depository != r.Paid {
			why = append(why, fmt.Sprintf("CDSL shows %d credited to the client against %d paid out", r.Depository, r.Paid))
			r.Action = firstOf(r.Action, "Check the depository transfer")
		}
		r.OK = len(why) == 0
		if !r.OK {
			r.Reason = strings.ToUpper(why[0][:1]) + why[0][1:]
			if len(why) > 1 {
				r.Reason += "; " + strings.Join(why[1:], "; ")
			}
			out.Mismatches++
		}
		out.Rows = append(out.Rows, *r)
	}
	sort.SliceStable(out.Rows, func(i, j int) bool { return !out.Rows[i].OK && out.Rows[j].OK })
	return out, nil
}

// payoutReconSection is the reconciliation on Clearing data, for the
// trade date whose pay-out is on date (and date's own when it is one).
func (o *Ops) payoutReconSection(ctx context.Context, tenant, date string, cs map[string]Client) CCSection {
	sec := CCSection{ID: "payout_recon", Title: "Pay-out of securities against depository transfers", About: "Each client's net purchase against NSE Clearing's direct pay-out (to the client's own account, or to the firm's pool) and the pay-out credits CDSL shows, with the reason for each difference (SEBI master circular for stock brokers, para 18.3). Pay-out to the pool goes to the client within one working day.",
		Columns: []string{"Trade date", "Client", "Name", "Security", "Due", "Paid to client", "To the pool", "CDSL credits", "Reason", "Action"}}
	set, _ := o.Settings(ctx, tenant)
	td := set.PrevBusinessDay(date)
	if set.NextSettlementDay(td) != date {
		td = date
	}
	rc, err := o.ReconcilePayout(ctx, tenant, td)
	if err != nil {
		return sec
	}
	sec.Loaded, sec.File, sec.Diffs = rc.FileLoaded || len(rc.Rows) > 0, rc.File, rc.Mismatches
	for _, r := range rc.Rows {
		dep := "—"
		if r.Depository >= 0 {
			dep = fmt.Sprint(r.Depository)
		}
		sec.Rows = append(sec.Rows, []string{rc.TradeDate, r.UCC, r.Name, firstOf(r.Symbol, r.ISIN), fmt.Sprint(r.Due), fmt.Sprint(r.Paid), fmt.Sprint(r.Pool), dep, firstOf(r.Reason, "Matched"), r.Action})
	}
	return sec
}

// payoutReconTick raises, once after 18:00 on a pay-out day, each pay-out
// credited to the pool or rejected (to transfer to the client within one
// working day) and each other mismatch (to explain). It also raises
// transfer requests and account closures nearing or past their deadline.
func (o *Ops) payoutReconTick(ctx context.Context, tenant string, now time.Time) {
	now = now.In(ist)
	today := now.Format("2006-01-02")
	if now.Format("15:04") < "18:00" {
		return
	}
	set, err := o.Settings(ctx, tenant)
	if err != nil || !set.IsBusinessDay(today) {
		return
	}
	id := "payoutrecon|" + today
	if _, _, err := docs.Get[struct{ At string }](ctx, o.Docs, tenant, cSettleTick, id); err == nil {
		return
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cSettleTick, id, struct{ At string }{now.Format(time.RFC3339)}, SystemActor)
	o.RaisePayoutMismatches(ctx, tenant, set.PrevBusinessDay(today))
	o.raiseOverdueTransfers(ctx, tenant, today)
	o.raiseOverdueClosures(ctx, tenant, today)
}

// RaisePayoutMismatches raises a work item for each pay-out mismatch of a
// trade date; it returns how many.
func (o *Ops) RaisePayoutMismatches(ctx context.Context, tenant, date string) int {
	rc, err := o.ReconcilePayout(ctx, tenant, date)
	if err != nil {
		return 0
	}
	set, _ := o.Settings(ctx, tenant)
	n := 0
	for _, r := range rc.Rows {
		if r.OK || r.Action == "" {
			continue
		}
		n++
		title := "Pay-out mismatch · " + r.UCC + " " + firstOf(r.Symbol, r.ISIN)
		detail := fmt.Sprintf("%s (pay-out of %s). %s.", r.Reason, fmtDay(rc.PayOut), r.Action)
		if r.Pool > 0 {
			title = "Pay-out in the pool: transfer to " + r.UCC
			detail = fmt.Sprintf("%d %s of %s's purchase of %s went to the firm's pool on %s: %s. Transfer them to the client's demat account by %s (one working day).", r.Pool, firstOf(r.Symbol, r.ISIN), r.UCC, fmtDay(rc.TradeDate), fmtDay(rc.PayOut), r.Reason, fmtDay(set.NextBusinessDay(rc.PayOut)))
		}
		o.raiseWork(ctx, tenant, "payout", "recon|"+rc.TradeDate+"|"+r.UCC+"|"+r.ISIN, title, detail, r.UCC, RoleChecker, "ccdata", rc.PayOut)
	}
	return n
}

// ---- securities transferred on the client's request ----

// SecTransferRequest is a client's request to transfer securities the
// firm holds for the client (in its pool, as margin, or unpaid) to the
// client's demat account.
type SecTransferRequest struct {
	ID     string `json:"id"`
	Date   string `json:"date"` // received
	UCC    string `json:"ucc"`
	Name   string `json:"name"`
	ISIN   string `json:"isin"`
	Symbol string `json:"symbol"`
	Qty    int64  `json:"qty"`
	// Where the securities lie: "Firm" (the firm's own accounts: one
	// working day) or "Clearing" (with the clearing member or clearing
	// corporation: three working days).
	Where    string `json:"where"`
	Due      string `json:"due"`
	Source   string `json:"source"`   // Client, Branch, Head office
	Evidence string `json:"evidence"` // how the request came (email, letter, call recording)
	Status   string `json:"status"`   // Open, Done, Rejected
	By       string `json:"by"`
	DoneOn   string `json:"done_on,omitempty"`
	DoneBy   string `json:"done_by,omitempty"`
	Ref      string `json:"ref,omitempty"` // the depository transfer's reference
	Reason   string `json:"reason,omitempty"`
}

// SecTransferInput is a request to record.
type SecTransferInput struct {
	Date     string `json:"date"`
	UCC      string `json:"ucc"`
	ISIN     string `json:"isin"`
	Qty      int64  `json:"qty"`
	Where    string `json:"where"`
	Source   string `json:"source"`
	Evidence string `json:"evidence"`
}

// secTransferDue is the deadline: one working day when the firm holds the
// securities, three when they lie with the clearing member or corporation.
func secTransferDue(set Settings, date, where string) string {
	n := 1
	if where == "Clearing" {
		n = 3
	}
	d := date
	for range n {
		d = set.NextBusinessDay(d)
	}
	return d
}

// RequestSecTransfer records a client's request and raises it for the
// operations checker with its deadline.
func (o *Ops) RequestSecTransfer(ctx context.Context, tenant, actor string, in SecTransferInput) (SecTransferRequest, error) {
	if err := o.require(ctx, tenant, actor, "Recording a transfer request", RoleMaker, RoleChecker); err != nil {
		return SecTransferRequest{}, err
	}
	in.UCC, in.ISIN = strings.ToUpper(strings.TrimSpace(in.UCC)), strings.ToUpper(strings.TrimSpace(in.ISIN))
	if in.Date == "" {
		in.Date = o.now().In(ist).Format("2006-01-02")
	}
	if !validDate(in.Date) || len(in.ISIN) != 12 || in.Qty <= 0 {
		return SecTransferRequest{}, invalid("give the date received, the ISIN and the quantity")
	}
	if in.Where != "Firm" && in.Where != "Clearing" {
		return SecTransferRequest{}, invalid("say where the securities lie: Firm (the firm's accounts) or Clearing (with the clearing member or corporation)")
	}
	if strings.TrimSpace(in.Evidence) == "" {
		return SecTransferRequest{}, invalid("say how the client asked (email, letter, call recording reference)")
	}
	c, err := o.Client(ctx, tenant, in.UCC)
	if err != nil {
		return SecTransferRequest{}, err
	}
	set, _ := o.Settings(ctx, tenant)
	n, err := o.next(ctx, tenant, "sectransfer", actor)
	if err != nil {
		return SecTransferRequest{}, err
	}
	sym := in.ISIN
	if ref, _, err := docs.Get[SecRef](ctx, o.Docs, tenant, cSecRef, in.ISIN); err == nil {
		sym = firstOf(ref.Symbol, firstOf(ref.Name, in.ISIN))
	}
	x := SecTransferRequest{ID: fmt.Sprintf("ST-%05d", n), Date: in.Date, UCC: c.UCC, Name: c.Name, ISIN: in.ISIN, Symbol: sym, Qty: in.Qty, Where: in.Where,
		Due: secTransferDue(set, in.Date, in.Where), Source: firstOf(strings.TrimSpace(in.Source), "Client"), Evidence: strings.TrimSpace(in.Evidence), Status: "Open", By: actor}
	if _, err := docs.Put(ctx, o.Docs, tenant, cSecTransfer, x.ID, x, 0, actor); err != nil {
		return x, err
	}
	o.log(ctx, tenant, actor, "securities_transfer.requested", x.ID, map[string]any{"ucc": x.UCC, "isin": x.ISIN, "qty": x.Qty, "due": x.Due})
	o.raiseWork(ctx, tenant, "sectransfer", x.ID, fmt.Sprintf("Transfer %d %s to %s by %s", x.Qty, x.Symbol, x.UCC, fmtDay(x.Due)),
		fmt.Sprintf("%s asked on %s (%s) for %d %s to be transferred to the client's demat account. SEBI's master circular (para 23.1.1(i)) allows %s. Transfer it, then mark the request done with the depository reference.",
			x.UCC, fmtDay(x.Date), x.Evidence, x.Qty, x.Symbol, map[string]string{"Firm": "one working day when the firm holds the securities", "Clearing": "three working days when they lie with the clearing member or corporation"}[x.Where]), x.UCC, RoleChecker, "settlement", x.Date)
	return x, nil
}

// FinishSecTransfer marks a request done (with the transfer's reference)
// or rejected (with the reason told to the client).
func (o *Ops) FinishSecTransfer(ctx context.Context, tenant, actor, id string, done bool, ref, reason string) (SecTransferRequest, error) {
	if err := o.require(ctx, tenant, actor, "Completing a transfer request", RoleChecker); err != nil {
		return SecTransferRequest{}, err
	}
	if done && strings.TrimSpace(ref) == "" {
		return SecTransferRequest{}, invalid("give the depository transfer's reference")
	}
	if !done && strings.TrimSpace(reason) == "" {
		return SecTransferRequest{}, invalid("give the reason the request cannot be met")
	}
	x, err := docs.Update(ctx, o.Docs, tenant, cSecTransfer, id, actor, func(x *SecTransferRequest) error {
		if x.Status != "Open" {
			return fmt.Errorf("%w: %s is %s", ErrState, x.ID, strings.ToLower(x.Status))
		}
		x.DoneOn, x.DoneBy = o.now().In(ist).Format("2006-01-02"), actor
		if done {
			x.Status, x.Ref = "Done", strings.TrimSpace(ref)
		} else {
			x.Status, x.Reason = "Rejected", strings.TrimSpace(reason)
		}
		return nil
	})
	if err != nil {
		return x, err
	}
	o.log(ctx, tenant, actor, "securities_transfer."+strings.ToLower(x.Status), x.ID, map[string]any{"ref": x.Ref, "reason": x.Reason})
	o.closeWork(ctx, tenant, actor, "sectransfer", x.ID, strings.ToLower(x.Status))
	return x, nil
}

// SecTransfers lists the requests, newest first.
func (o *Ops) SecTransfers(ctx context.Context, tenant string) ([]SecTransferRequest, error) {
	xs, err := docs.List[SecTransferRequest](ctx, o.Docs, tenant, cSecTransfer, "")
	sort.SliceStable(xs, func(i, j int) bool { return xs[i].ID > xs[j].ID })
	if xs == nil {
		xs = []SecTransferRequest{}
	}
	return xs, err
}

// raiseOverdueTransfers re-raises open requests past their deadline.
func (o *Ops) raiseOverdueTransfers(ctx context.Context, tenant, today string) {
	xs, _ := o.SecTransfers(ctx, tenant)
	for _, x := range xs {
		if x.Status == "Open" && x.Due < today {
			o.raiseWork(ctx, tenant, "sectransfer", "late|"+x.ID, "Transfer request overdue · "+x.UCC,
				fmt.Sprintf("%s's request %s (%d %s) was due by %s and is not done. Transfer it today and record the reference.", x.UCC, x.ID, x.Qty, x.Symbol, fmtDay(x.Due)), x.UCC, RoleChecker, "settlement", today)
		}
	}
}

// ---- self-inspection tests ----

// siPayoutSecurities: each purchase of the last 10 business days reached
// the client within one working day of pay-out.
func siPayoutSecurities(e *siEnv) (siOut, error) {
	var s siOut
	if siFirstTrade(e) == "" {
		return siSkip("Nothing to check: no trades in Ledgerline yet.")
	}
	day := e.date
	checkedDays := 0
	for i := 0; i < 20 && checkedDays < 10; i++ {
		day = e.set.PrevBusinessDay(day)
		po := e.set.NextSettlementDay(day)
		due := e.set.NextBusinessDay(po)
		if due >= e.date {
			continue
		}
		checkedDays++
		rc, err := e.o.ReconcilePayout(e.ctx, e.tenant, day)
		if err != nil {
			return s, err
		}
		if checkedDays == 1 {
			s.input("Obligations and pay-out", day)
		}
		for _, r := range rc.Rows {
			if r.Due == 0 {
				continue
			}
			s.checked++
			switch {
			case r.Pool > 0 || strings.HasPrefix(r.Reason, "The depository rejected"):
				s.bad(fmt.Sprintf("%s · %s · trade %s: %s", siClient(r.UCC), firstOf(r.Symbol, r.ISIN), fmtDay(day), r.Reason))
			case r.Paid >= r.Due || paidOutStatus(r.Ledgerline) || strings.Contains(r.Reason, "waiting for the auction"):
			default:
				s.bad(fmt.Sprintf("%s · %s × %d · trade %s, pay-out %s: %s (due by %s)", siClient(r.UCC), firstOf(r.Symbol, r.ISIN), r.Due, fmtDay(day), fmtDay(po), firstOf(r.Reason, "not paid out"), fmtDay(due)))
			}
		}
	}
	s.summary = fmt.Sprintf("%d client purchases of the last %d settlement days checked; %d did not reach the client's own demat account within one working day of pay-out.", s.checked, checkedDays, s.found)
	return s, nil
}

// siPayoutFundsNoRAS: a client without a running account authorisation
// left with free credit (beyond the margin required) at the end of a day
// and still at the end of the next working day was not paid within 24
// hours.
func siPayoutFundsNoRAS(e *siEnv) (siOut, error) {
	var s siOut
	if siFirstTrade(e) == "" {
		return siSkip("Nothing to check: no trades in Ledgerline yet.")
	}
	noRAS := map[string]bool{}
	for u, c := range e.clients {
		if c.RAS != "Monthly" && c.RAS != "Quarterly" {
			noRAS[u] = true
		}
	}
	if len(noRAS) == 0 {
		return siSkip("Nothing to check: every client has a running account authorisation.")
	}
	free := func(day string) (map[string]money.Dec, error) {
		f, err := e.o.clientFunds(e.ctx, e.tenant, day, "")
		if err != nil {
			return nil, err
		}
		need := map[string]money.Dec{}
		if rv, err := e.o.riskView(e.ctx, e.tenant, day, true); err == nil {
			for _, r := range rv.Rows {
				need[r.UCC] = r.allRequired()
			}
		}
		out := map[string]money.Dec{}
		for u, a := range f {
			if noRAS[u] {
				if x := a.Dec().Sub(need[u]); x.Sign() > 0 {
					out[u] = x
				}
			}
		}
		return out, nil
	}
	end := e.set.PrevBusinessDay(e.date)
	days := []string{end}
	for len(days) < 6 {
		days = append([]string{e.set.PrevBusinessDay(days[0])}, days...)
	}
	s.input("Ledger balances and margins", days[0])
	s.input("Clients' running account authorisation", e.date)
	prev, err := free(days[0])
	if err != nil {
		return s, err
	}
	flagged := map[string]bool{}
	for _, d := range days[1:] {
		cur, err := free(d)
		if err != nil {
			return s, err
		}
		for _, u := range sortedKeys(prev) {
			s.checked++
			left := money.Min(prev[u], cur[u])
			if left.Cmp(money.MustDec("1")) > 0 && !flagged[u] {
				flagged[u] = true
				s.bad(fmt.Sprintf("%s · no running account authorisation · ₹%s free on %s still not paid by the end of %s", siClient(u), formatINR(left), fmtDay(e.set.PrevBusinessDay(d)), fmtDay(d)))
			}
		}
		prev = cur
	}
	s.summary = fmt.Sprintf("%d client-days of free credit of clients without a running account authorisation checked over %d working days; %d clients not paid within 24 hours.", s.checked, len(days)-1, s.found)
	return s, nil
}

// siSecTransfers: each client request to transfer securities was done by
// its deadline (one or three working days).
func siSecTransfers(e *siEnv) (siOut, error) {
	var s siOut
	xs, err := e.o.SecTransfers(e.ctx, e.tenant)
	if err != nil {
		return s, err
	}
	from := addDays(e.date, -60)
	s.input("Securities transfer requests", from)
	for _, x := range xs {
		if x.Date < from || x.Date > e.date || x.Status == "Rejected" {
			continue
		}
		s.checked++
		switch {
		case x.Status == "Done" && x.DoneOn > x.Due:
			s.bad(fmt.Sprintf("%s · %s · %d %s asked %s, transferred %s (due by %s)", x.ID, siClient(x.UCC), x.Qty, x.Symbol, fmtDay(x.Date), fmtDay(x.DoneOn), fmtDay(x.Due)))
		case x.Status == "Open" && x.Due < e.date:
			s.bad(fmt.Sprintf("%s · %s · %d %s asked %s, not transferred by %s", x.ID, siClient(x.UCC), x.Qty, x.Symbol, fmtDay(x.Date), fmtDay(x.Due)))
		}
	}
	s.summary = fmt.Sprintf("%d transfer requests of the last 60 days checked; %d not done within one working day (firm's accounts) or three (with the clearing corporation).", s.checked, s.found)
	return s, nil
}
