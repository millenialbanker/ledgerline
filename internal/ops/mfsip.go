package ops

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// ---- SIPs and mandates ----
//
// SIPs here are XSIPs: each instalment is debited from the investor's own
// bank account by a mandate in the clearing corporation's favour. (A
// platform's plain SIP, where the member collects each instalment, is not
// offered: the firm never takes MF money.)
//
// The rules, from NSE MF Invest's FAQ (July 2026), applied to both
// platforms until BSE's are confirmed:
//   - a physical NACH mandate takes up to 20 working days to register, an
//     e-mandate up to 5; a mandate allows at most ₹1 crore a debit;
//   - an XSIP can start 2 working days after its mandate is approved, or,
//     with the mandate still being processed, 5 (e-mandate) or 20
//     (physical) calendar days later still (FAQ 6.25);
//   - the debit file goes to the bank the working day before an
//     instalment (T-1); an XSIP can be cancelled until T-2;
//   - at most 2 pauses over an XSIP's life; 3 failed instalments in a row
//     cancel it.
// How long a pause may be is not in the material read; up to 6
// instalments is allowed here.

const cMFSIP = "mfsip"

// MFSIPFrequencies are offered on both platforms.
var MFSIPFrequencies = []string{"MONTHLY", "QUARTERLY", "WEEKLY"}

// MFPause is a pause of an XSIP's instalments due From to To.
type MFPause struct {
	From string `json:"from"`
	To   string `json:"to"`
	At   string `json:"at"`
	By   string `json:"by"`
}

// MFSIPEvent is one thing that happened to an XSIP.
type MFSIPEvent struct {
	At   string `json:"at"`
	What string `json:"what"`
	By   string `json:"by"`
}

// MFSIP is an XSIP.
type MFSIP struct {
	ID           string       `json:"id"`
	UCC          string       `json:"ucc"`
	Name         string       `json:"name,omitempty"`
	Platform     string       `json:"platform"`
	ISIN         string       `json:"isin"`
	Scheme       string       `json:"scheme"`
	SchemeCode   string       `json:"scheme_code"`
	Plan         string       `json:"plan"`
	Amount       money.Amount `json:"amount"`
	Frequency    string       `json:"frequency"`
	StartDate    string       `json:"start_date"`
	Installments int          `json:"installments"`
	MandateID    string       `json:"mandate_id"`
	Mode         string       `json:"mode"`
	Folio        string       `json:"folio,omitempty"`
	EUIN         string       `json:"euin,omitempty"`
	ARN          string       `json:"arn,omitempty"`
	FirstOrder   bool         `json:"first_order_today,omitempty"`
	// Status: Active, Cancelled, Completed, Registration failed. Display
	// shows Paused while a pause covers the next instalment.
	Status      string       `json:"status"`
	Display     string       `json:"display,omitempty"`
	RegID       string       `json:"reg_id,omitempty"`
	Ref         string       `json:"ref,omitempty"`
	Remark      string       `json:"remark,omitempty"`
	Pauses      []MFPause    `json:"pauses"`
	Failures    int          `json:"failures"` // consecutive
	Done        int          `json:"done"`
	Failed      int          `json:"failed"`
	NextDue     string       `json:"next_due,omitempty"`
	AlertedFor  string       `json:"alerted_for,omitempty"`
	LastResult  string       `json:"last_result,omitempty"` // date of the last instalment recorded
	Events      []MFSIPEvent `json:"events"`
	Source      string       `json:"source"` // placed, portal
	By          string       `json:"by"`
	At          string       `json:"at"`
	CancelledOn string       `json:"cancelled_on,omitempty"`
	CancelledBy string       `json:"cancelled_by,omitempty"`
}

func (s *MFSIP) event(at time.Time, by, what string) {
	s.Events = append(s.Events, MFSIPEvent{At: at.Format(time.RFC3339), What: what, By: by})
}

// dueDate is instalment k (from 0).
func (s MFSIP) dueDate(k int) string {
	t, err := time.Parse("2006-01-02", s.StartDate)
	if err != nil {
		return ""
	}
	switch s.Frequency {
	case "WEEKLY":
		t = t.AddDate(0, 0, 7*k)
	case "QUARTERLY":
		t = t.AddDate(0, 3*k, 0)
	default:
		t = t.AddDate(0, k, 0)
	}
	return t.Format("2006-01-02")
}

func (s MFSIP) paused(date string) bool {
	for _, p := range s.Pauses {
		if date >= p.From && date <= p.To {
			return true
		}
	}
	return false
}

// nextDue is the first instalment on or after date that is not paused
// ("" when the XSIP has run its course), and whether a pause covers the
// first instalment on or after date.
func (s MFSIP) nextDue(date string) (string, bool) {
	paused := false
	first := true
	for k := 0; k < s.Installments; k++ {
		d := s.dueDate(k)
		if d < date || (s.LastResult != "" && d <= s.LastResult) {
			continue
		}
		if s.paused(d) {
			if first {
				paused = true
			}
			first = false
			continue
		}
		return d, paused
	}
	return "", paused
}

func (o *Ops) fillSIP(s *MFSIP, today string) {
	s.Display = s.Status
	if s.Status != "Active" {
		s.NextDue = ""
		return
	}
	d, paused := s.nextDue(today)
	s.NextDue = d
	if paused {
		s.Display = "Paused"
	}
	if d == "" {
		s.Display = "Completed"
	}
	if s.Pauses == nil {
		s.Pauses = []MFPause{}
	}
	if s.Events == nil {
		s.Events = []MFSIPEvent{}
	}
}

// MFSIPs lists XSIPs, for one client or all, with their next instalment.
func (o *Ops) MFSIPs(ctx context.Context, tenant, ucc string) ([]MFSIP, error) {
	all, err := docs.List[MFSIP](ctx, o.Docs, tenant, cMFSIP, "")
	if err != nil {
		return nil, err
	}
	today := o.now().Format("2006-01-02")
	out := []MFSIP{}
	for _, s := range all {
		if ucc != "" && s.UCC != strings.ToUpper(ucc) {
			continue
		}
		o.fillSIP(&s, today)
		out = append(out, s)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At > out[j].At })
	return out, nil
}

// MFSIPInput starts an XSIP.
type MFSIPInput struct {
	UCC          string `json:"ucc"`
	Platform     string `json:"platform"`
	ISIN         string `json:"isin"`
	Amount       string `json:"amount"`
	Frequency    string `json:"frequency"`
	StartDate    string `json:"start_date"`
	Installments int    `json:"installments"`
	MandateID    string `json:"mandate_id"`
	FirstOrder   bool   `json:"first_order_today"`
	Folio        string `json:"folio,omitempty"`
}

// StartMFSIP registers an XSIP for a client, as staff.
func (o *Ops) StartMFSIP(ctx context.Context, tenant, actor string, in MFSIPInput) (MFSIP, error) {
	if err := o.require(ctx, tenant, actor, "Managing SIPs", RoleMaker, RoleChecker); err != nil {
		return MFSIP{}, err
	}
	return o.startSIP(ctx, tenant, actor, in, "placed")
}

// PortalMFSIP registers an XSIP from the client portal (execution-only).
func (o *Ops) PortalMFSIP(ctx context.Context, tenant, actor string, in MFSIPInput) (MFSIP, error) {
	if err := o.portalOwns(ctx, tenant, actor, in.UCC); err != nil {
		return MFSIP{}, err
	}
	return o.startSIP(ctx, tenant, actor, in, "portal")
}

// mfEarliestStart is the first day an XSIP may start given its mandate.
func mfEarliestStart(set Settings, today string, m MFMandate) string {
	d := set.NextBusinessDay(set.NextBusinessDay(today))
	if m.Status == "Approved" {
		return d
	}
	t, _ := time.Parse("2006-01-02", d)
	days := 20
	if m.Type == "E-mandate" {
		days = 5
	}
	return t.AddDate(0, 0, days).Format("2006-01-02")
}

func (o *Ops) startSIP(ctx context.Context, tenant, actor string, in MFSIPInput, source string) (MFSIP, error) {
	in.Platform = strings.ToUpper(strings.TrimSpace(in.Platform))
	set, c, p, cn, err := o.mfReady(ctx, tenant, in.UCC, in.Platform)
	if err != nil {
		return MFSIP{}, err
	}
	now := o.now()
	today := now.Format("2006-01-02")
	sc, err := o.mfScheme(ctx, tenant, in.ISIN)
	if err != nil {
		return MFSIP{}, err
	}
	if sc.Codes[in.Platform] == "" {
		return MFSIP{}, invalid("%s has no %s scheme code: load the platform's scheme file", sc.Name, in.Platform)
	}
	if contains(sc.NoSIP, in.Platform) {
		return MFSIP{}, invalid("%s does not take SIPs on %s", sc.Name, in.Platform)
	}
	amt, err := money.ParseAmount(strings.ReplaceAll(in.Amount, ",", ""))
	if err != nil || amt <= 0 {
		return MFSIP{}, invalid("give the instalment in rupees")
	}
	in.Frequency = strings.ToUpper(strings.TrimSpace(in.Frequency))
	if !contains(MFSIPFrequencies, in.Frequency) {
		return MFSIP{}, invalid("the frequency is MONTHLY, QUARTERLY or WEEKLY")
	}
	if in.Installments < 1 || in.Installments > 1200 {
		return MFSIP{}, invalid("give the number of instalments")
	}
	if !validDate(in.StartDate) {
		return MFSIP{}, invalid("the start date is YYYY-MM-DD")
	}
	if in.Frequency != "WEEKLY" {
		if day := in.StartDate[8:]; day > "28" {
			return MFSIP{}, invalid("monthly and quarterly instalments fall on the 1st to the 28th")
		}
	}
	m, ok := mfMandateOf(p, in.MandateID)
	switch {
	case !ok || m.Platform != in.Platform:
		return MFSIP{}, invalid("choose one of %s's %s mandates", c.UCC, in.Platform)
	case m.Status != "Approved" && m.Status != "Pending":
		return MFSIP{}, invalid("mandate %s is %s", m.ID, strings.ToLower(m.Status))
	case m.Status == "Pending" && m.PlatformID == "":
		return MFSIP{}, invalid("mandate %s has no %s mandate number yet", m.ID, in.Platform)
	}
	others, _ := o.MFSIPs(ctx, tenant, c.UCC)
	used := amt
	for _, s := range others {
		if s.Status == "Active" && s.MandateID == m.ID {
			used += s.Amount
		}
	}
	if used > m.Limit {
		return MFSIP{}, invalid("XSIPs on mandate %s would need ₹%s a debit, above its limit of ₹%s", m.ID, used, m.Limit)
	}
	gs, _ := o.Settings(ctx, tenant)
	if first := mfEarliestStart(gs, today, m); in.StartDate < first {
		return MFSIP{}, invalid("with mandate %s %s, the first instalment can be on %s at the earliest", m.ID, strings.ToLower(m.Status), first)
	}
	fresh := strings.TrimSpace(in.Folio) == "" && !o.mfHolds(ctx, tenant, c.UCC, sc)
	if fresh && mfNeedsNomination(p, today) {
		return MFSIP{}, invalid("a new folio from 1 September 2026 needs up to 3 nominees or the investor's opt-out: record it in %s's MF profile", c.UCC)
	}
	lock, err := o.mfCheckRoute(ctx, tenant, set, p, sc.Plan)
	if err != nil {
		return MFSIP{}, err
	}
	s := MFSIP{UCC: c.UCC, Name: c.Name, Platform: in.Platform, ISIN: sc.ISIN, Scheme: sc.Name, SchemeCode: sc.Codes[in.Platform], Plan: sc.Plan, Amount: amt,
		Frequency: in.Frequency, StartDate: in.StartDate, Installments: in.Installments, MandateID: m.ID, Mode: p.Mode, Folio: strings.TrimSpace(in.Folio),
		FirstOrder: in.FirstOrder, Source: source, By: actor, At: now.Format(time.RFC3339), Pauses: []MFPause{}, Events: []MFSIPEvent{}}
	if sc.Plan == MFRegular {
		s.ARN = set.ARN
		if source != "portal" {
			e, ok := set.euinOf(actor)
			if !ok {
				return s, invalid("Regular plan SIPs registered by staff carry the employee's EUIN: add yours in MF settings")
			}
			s.EUIN = e.EUIN
		}
	}
	n, err := o.next(ctx, tenant, "mfsip", actor)
	if err != nil {
		return s, err
	}
	s.ID = fmt.Sprintf("SIP-%06d", n)
	if s.Ref, err = o.mfRef(ctx, tenant, actor, cn, now); err != nil {
		return s, err
	}
	rep, err := mfPlatformFor(in.Platform).RegisterSIP(ctx, cn, o.sipReq(c, p, s, sc, m))
	s.RegID, s.Remark = rep.RegID, rep.Remark
	if err != nil || !rep.OK {
		s.Status = "Registration failed"
		if err != nil {
			s.Remark = err.Error()
		}
		s.event(now, actor, "Registration refused: "+s.Remark)
		_ = docs.Upsert(ctx, o.Docs, tenant, cMFSIP, s.ID, s, actor)
		o.log(ctx, tenant, actor, "mf.sip.failed", s.ID, map[string]any{"ucc": s.UCC, "remark": s.Remark})
		if err != nil {
			return s, err
		}
		return s, fmt.Errorf("%w: %s refused the XSIP: %s", ErrState, MFPlatformNames[in.Platform][0], firstOf(s.Remark, "no reason given"))
	}
	s.Status = "Active"
	s.event(now, actor, fmt.Sprintf("Registered on %s, registration %s", MFPlatformNames[in.Platform][0], s.RegID))
	if rep.OrderNo != "" {
		s.event(now, actor, "First instalment placed today, order "+rep.OrderNo)
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cMFSIP, s.ID, s, actor); err != nil {
		return s, err
	}
	o.mfLockRoute(ctx, tenant, actor, c.UCC, sc.Plan, lock)
	o.log(ctx, tenant, actor, "mf.sip.registered", s.ID, map[string]any{"ucc": s.UCC, "platform": s.Platform, "isin": s.ISIN, "amount": s.Amount.String(), "frequency": s.Frequency,
		"start": s.StartDate, "installments": s.Installments, "reg_id": s.RegID, "mandate": s.MandateID, "source": source})
	o.fillSIP(&s, o.now().Format("2006-01-02"))
	return s, nil
}

func (o *Ops) sipReq(c Client, p MFProfile, s MFSIP, sc MFScheme, m MFMandate) mfSIPReq {
	return mfSIPReq{Ref: s.Ref, ClientCode: s.UCC, SchemeCode: s.SchemeCode, AMCCode: sc.AMCCode[s.Platform], DPTxn: mfDPTxn(c, p), StartDate: s.StartDate,
		Frequency: s.Frequency, Amount: s.Amount, Installments: s.Installments, Folio: s.Folio, EUIN: s.EUIN, MandateID: m.PlatformID, RegID: s.RegID,
		FirstOrder: s.FirstOrder, Mobile: c.Mobile, Email: c.Email, Remarks: s.ID}
}

// mfBeforeCutoff checks an instalment due is at least T-2 away.
func (o *Ops) mfBeforeCutoff(ctx context.Context, tenant, due, what string) error {
	gs, _ := o.Settings(ctx, tenant)
	last := gs.PrevBusinessDay(gs.PrevBusinessDay(due))
	if today := o.now().Format("2006-01-02"); today > last {
		return invalid("the instalment of %s is already in the bank's debit run (%s closes on T-2, %s)", due, what, last)
	}
	return nil
}

// CancelMFSIP cancels an XSIP. Through the API where the platform has it
// here (BSE); otherwise staff cancel on the platform's site and confirm
// with onPlatform.
func (o *Ops) CancelMFSIP(ctx context.Context, tenant, actor, id, reason string, onPlatform bool) (MFSIP, error) {
	if err := o.require(ctx, tenant, actor, "Managing SIPs", RoleMaker, RoleChecker); err != nil {
		return MFSIP{}, err
	}
	return o.cancelSIP(ctx, tenant, actor, id, reason, onPlatform)
}

// PortalCancelMFSIP cancels the client's own XSIP from the portal.
func (o *Ops) PortalCancelMFSIP(ctx context.Context, tenant, actor, id string) (MFSIP, error) {
	s, _, err := docs.Get[MFSIP](ctx, o.Docs, tenant, cMFSIP, id)
	if err != nil {
		return s, err
	}
	if err := o.portalOwns(ctx, tenant, actor, s.UCC); err != nil {
		return MFSIP{}, err
	}
	s, err = o.cancelSIP(ctx, tenant, actor, id, "cancelled by the investor", false)
	if errors.Is(err, errMFNoAPI) {
		return s, fmt.Errorf("%w: this SIP is cancelled on %s by the firm: ask us from the Help desk", ErrState, MFPlatformNames[s.Platform][0])
	}
	return s, err
}

func (o *Ops) cancelSIP(ctx context.Context, tenant, actor, id, reason string, onPlatform bool) (MFSIP, error) {
	s, _, err := docs.Get[MFSIP](ctx, o.Docs, tenant, cMFSIP, id)
	if err != nil {
		return s, err
	}
	if s.Status != "Active" {
		return s, fmt.Errorf("%w: %s is %s", ErrState, id, strings.ToLower(s.Status))
	}
	if strings.TrimSpace(reason) == "" {
		return s, invalid("give the reason for cancelling")
	}
	today := o.now().Format("2006-01-02")
	if due, _ := s.nextDue(today); due != "" && !onPlatform {
		if err := o.mfBeforeCutoff(ctx, tenant, due, "cancellation"); err != nil {
			return s, err
		}
	}
	how := "on " + MFPlatformNames[s.Platform][0] + "'s site"
	if !onPlatform {
		cn, err := o.mfConn(ctx, tenant, s.Platform)
		if err != nil {
			return s, err
		}
		c, _ := o.Client(ctx, tenant, s.UCC)
		p, _ := o.MFProfile(ctx, tenant, s.UCC)
		sc, _ := o.mfScheme(ctx, tenant, s.ISIN)
		m, _ := mfMandateOf(p, s.MandateID)
		rep, err := mfPlatformFor(s.Platform).CancelSIP(ctx, cn, o.sipReq(c, p, s, sc, m))
		if err != nil {
			return s, err
		}
		if !rep.OK {
			return s, fmt.Errorf("%w: the platform refused the cancellation: %s", ErrState, firstOf(rep.Remark, "no reason given"))
		}
		how = "through the API: " + firstOf(rep.Remark, "cancelled")
	}
	now := o.now()
	s, err = docs.Update(ctx, o.Docs, tenant, cMFSIP, id, actor, func(x *MFSIP) error {
		x.Status, x.CancelledOn, x.CancelledBy = "Cancelled", today, actor
		x.event(now, actor, "Cancelled "+how+": "+reason)
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "mf.sip.cancelled", id, map[string]any{"reason": reason, "on_platform": onPlatform})
		o.fillSIP(&s, today)
	}
	return s, err
}

// PauseMFSIP pauses count instalments from the one due on from. Neither
// platform's pause API could be read, so staff pause on the platform's
// site and record it here (onPlatform).
func (o *Ops) PauseMFSIP(ctx context.Context, tenant, actor, id, from string, count int, onPlatform bool) (MFSIP, error) {
	if err := o.require(ctx, tenant, actor, "Managing SIPs", RoleMaker, RoleChecker); err != nil {
		return MFSIP{}, err
	}
	if !onPlatform {
		return MFSIP{}, invalid("pause the SIP on the platform's site first, then record it here")
	}
	s, _, err := docs.Get[MFSIP](ctx, o.Docs, tenant, cMFSIP, id)
	if err != nil {
		return s, err
	}
	switch {
	case s.Status != "Active":
		return s, fmt.Errorf("%w: %s is %s", ErrState, id, strings.ToLower(s.Status))
	case len(s.Pauses) >= 2:
		return s, invalid("an SIP can be paused only twice; %s has been paused %d times", id, len(s.Pauses))
	case count < 1 || count > 6:
		return s, invalid("pause 1 to 6 instalments")
	}
	k := -1
	for i := 0; i < s.Installments; i++ {
		if s.dueDate(i) == from {
			k = i
			break
		}
	}
	if k < 0 {
		return s, invalid("%s is not an instalment date of %s", from, id)
	}
	if err := o.mfBeforeCutoff(ctx, tenant, from, "a pause"); err != nil {
		return s, err
	}
	to := s.dueDate(min(k+count-1, s.Installments-1))
	now := o.now()
	s, err = docs.Update(ctx, o.Docs, tenant, cMFSIP, id, actor, func(x *MFSIP) error {
		x.Pauses = append(x.Pauses, MFPause{From: from, To: to, At: now.Format(time.RFC3339), By: actor})
		x.event(now, actor, fmt.Sprintf("Paused %d instalment(s), %s to %s (done on the platform)", count, from, to))
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "mf.sip.paused", id, map[string]any{"from": from, "to": to})
		o.fillSIP(&s, now.Format("2006-01-02"))
	}
	return s, err
}

// RecordMFSIPInstalment records an instalment's result from the platform's
// or bank's report. Three failures in a row cancel the XSIP (the platform
// does the same and tells the RTA).
func (o *Ops) RecordMFSIPInstalment(ctx context.Context, tenant, actor, id, date string, ok bool, orderNo, reason string) (MFSIP, error) {
	if err := o.require(ctx, tenant, actor, "Managing SIPs", RoleMaker, RoleChecker); err != nil {
		return MFSIP{}, err
	}
	s, _, err := docs.Get[MFSIP](ctx, o.Docs, tenant, cMFSIP, id)
	if err != nil {
		return s, err
	}
	if s.Status != "Active" {
		return s, fmt.Errorf("%w: %s is %s", ErrState, id, strings.ToLower(s.Status))
	}
	found := false
	for i := 0; i < s.Installments; i++ {
		found = found || s.dueDate(i) == date
	}
	if !found {
		return s, invalid("%s is not an instalment date of %s", date, id)
	}
	if s.LastResult != "" && date <= s.LastResult {
		return s, fmt.Errorf("%w: instalments up to %s are already recorded", ErrState, s.LastResult)
	}
	now := o.now()
	cancelled := false
	s, err = docs.Update(ctx, o.Docs, tenant, cMFSIP, id, actor, func(x *MFSIP) error {
		x.LastResult = date
		if ok {
			x.Done++
			x.Failures = 0
			x.event(now, actor, "Instalment of "+date+" debited"+map[bool]string{true: ", order " + orderNo, false: ""}[orderNo != ""])
		} else {
			x.Failed++
			x.Failures++
			x.event(now, actor, fmt.Sprintf("Instalment of %s failed: %s", date, firstOf(reason, "no reason given")))
			if x.Failures >= 3 {
				x.Status, x.CancelledOn, x.CancelledBy, cancelled = "Cancelled", now.Format("2006-01-02"), SystemActor, true
				x.event(now, SystemActor, "Cancelled after 3 failed instalments in a row")
			}
		}
		if x.Done+x.Failed >= x.Installments && x.Status == "Active" {
			x.Status = "Completed"
		}
		return nil
	})
	if err != nil {
		return s, err
	}
	if ok && orderNo != "" {
		// An instalment is a purchase order on the platform: keep it so
		// its allotment can be recorded.
		ord := MFOrder{ID: "", UCC: s.UCC, Name: s.Name, Scheme: s.Scheme, ISIN: s.ISIN, SchemeCode: s.SchemeCode, Plan: s.Plan, Type: "Purchase", Amount: s.Amount,
			Platform: s.Platform, Status: MFPendRTA, Date: date, Source: "sip", OrderNo: orderNo, Mode: s.Mode, Folio: s.Folio, EUIN: s.EUIN, ARN: s.ARN,
			Payment: "Mandate debit", SIP: s.ID, By: actor, At: now.Format(time.RFC3339)}
		if n, err := o.next(ctx, tenant, "mforder", actor); err == nil {
			ord.ID = fmt.Sprintf("MFO-%06d", n)
			_ = docs.Upsert(ctx, o.Docs, tenant, cMF, ord.ID, ord, actor)
		}
	}
	o.log(ctx, tenant, actor, "mf.sip.instalment", id, map[string]any{"date": date, "ok": ok, "order_no": orderNo, "reason": reason, "failures": s.Failures})
	if cancelled {
		o.mfNotify(ctx, tenant, actor, "mf.sip.cancelled", s.UCC, map[string]string{"scheme": s.Scheme, "amount": formatINR(s.Amount.Dec()), "reason": "three instalments in a row could not be debited"})
	}
	o.fillSIP(&s, now.Format("2006-01-02"))
	return s, nil
}

// ---- mandates ----

// MFMandateInput records or requests a mandate.
type MFMandateInput struct {
	Platform   string `json:"platform"`
	Type       string `json:"type"` // E-mandate, Physical NACH
	Limit      string `json:"limit"`
	From       string `json:"from"`
	Till       string `json:"till,omitempty"`
	PlatformID string `json:"platform_id,omitempty"` // the platform's mandate number, once registered there
	UMRN       string `json:"umrn,omitempty"`
	Status     string `json:"status,omitempty"` // Pending (default) or Approved
}

// AddMFMandate records a client's mandate. Without the platform's mandate
// number it asks the platform's API to register one; neither platform's
// mandate API could be read, so staff register it on the platform's site
// and enter its number here. The mandate is always on the client's own
// registered bank account and in the clearing corporation's favour.
func (o *Ops) AddMFMandate(ctx context.Context, tenant, actor, ucc string, in MFMandateInput) (MFMandate, error) {
	if err := o.require(ctx, tenant, actor, "Recording MF mandates", RoleMaker, RoleChecker); err != nil {
		return MFMandate{}, err
	}
	in.Platform = strings.ToUpper(strings.TrimSpace(in.Platform))
	names, ok := MFPlatformNames[in.Platform]
	if !ok {
		return MFMandate{}, invalid("platform is BSE or NSE")
	}
	c, err := o.Client(ctx, tenant, ucc)
	if err != nil {
		return MFMandate{}, err
	}
	if c.Bank.Account == "" || c.Bank.IFSC == "" {
		return MFMandate{}, invalid("%s has no registered bank account", c.UCC)
	}
	if in.Type != "E-mandate" && in.Type != "Physical NACH" {
		return MFMandate{}, invalid("the mandate is an E-mandate or a Physical NACH")
	}
	limit, err := money.ParseAmount(strings.ReplaceAll(in.Limit, ",", ""))
	if err != nil || limit <= 0 {
		return MFMandate{}, invalid("give the mandate's limit per debit in rupees")
	}
	if limit > MFMandateMax {
		return MFMandate{}, invalid("a mandate allows at most ₹1 crore a debit")
	}
	today := o.now().Format("2006-01-02")
	if in.From == "" {
		in.From = today
	}
	if !validDate(in.From) || (in.Till != "" && (!validDate(in.Till) || in.Till <= in.From)) {
		return MFMandate{}, invalid("the mandate runs from a date to a later date (YYYY-MM-DD; leave the end empty for until cancelled)")
	}
	if in.Status == "" {
		in.Status = "Pending"
	}
	if in.Status != "Pending" && in.Status != "Approved" {
		return MFMandate{}, invalid("a new mandate is Pending or Approved")
	}
	if in.PlatformID == "" {
		cn, err := o.mfConn(ctx, tenant, in.Platform)
		if err != nil {
			return MFMandate{}, err
		}
		rep, err := mfPlatformFor(in.Platform).RegisterMandate(ctx, cn, mfMandateReq{ClientCode: c.UCC, Amount: limit, Type: in.Type, Account: c.Bank.Account, IFSC: c.Bank.IFSC, From: in.From, Till: in.Till})
		if err != nil {
			return MFMandate{}, err
		}
		if !rep.OK {
			return MFMandate{}, fmt.Errorf("%w: the platform refused the mandate: %s", ErrState, rep.Remark)
		}
		in.PlatformID = rep.RegID
	}
	gs, _ := o.Settings(ctx, tenant)
	exp := today
	days := 20
	if in.Type == "E-mandate" {
		days = 5
	}
	for i := 0; i < days; i++ {
		exp = gs.NextBusinessDay(exp)
	}
	n, err := o.next(ctx, tenant, "mfmandate", actor)
	if err != nil {
		return MFMandate{}, err
	}
	m := MFMandate{ID: fmt.Sprintf("MDT-%06d", n), Platform: in.Platform, PlatformID: strings.TrimSpace(in.PlatformID), UMRN: strings.ToUpper(strings.TrimSpace(in.UMRN)),
		Type: in.Type, Limit: limit, Bank: c.Bank.Bank, Account: MaskAccount(c.Bank.Account), IFSC: c.Bank.IFSC, Favour: names[1], From: in.From, Till: in.Till,
		Status: in.Status, Requested: today, ExpectedBy: exp, By: actor}
	p, err := o.MFProfile(ctx, tenant, c.UCC)
	if err != nil {
		return m, err
	}
	p.Mandates = append(p.Mandates, m)
	if err := o.putMFProfile(ctx, tenant, actor, p); err != nil {
		return m, err
	}
	o.log(ctx, tenant, actor, "mf.mandate.added", c.UCC, map[string]any{"mandate": m.ID, "platform": m.Platform, "platform_id": m.PlatformID, "limit": m.Limit.String(), "type": m.Type, "status": m.Status})
	return m, nil
}

// SetMFMandateStatus records a mandate's status as the platform or bank
// reports it.
func (o *Ops) SetMFMandateStatus(ctx context.Context, tenant, actor, ucc, id, status, umrn, remark string) (MFMandate, error) {
	if err := o.require(ctx, tenant, actor, "Recording MF mandates", RoleMaker, RoleChecker); err != nil {
		return MFMandate{}, err
	}
	if !contains([]string{"Pending", "Approved", "Rejected", "Cancelled"}, status) {
		return MFMandate{}, invalid("status is Pending, Approved, Rejected or Cancelled")
	}
	p, err := o.MFProfile(ctx, tenant, ucc)
	if err != nil {
		return MFMandate{}, err
	}
	for i, m := range p.Mandates {
		if m.ID != id {
			continue
		}
		if status == "Cancelled" || status == "Rejected" {
			ss, _ := o.MFSIPs(ctx, tenant, p.UCC)
			for _, s := range ss {
				if s.Status == "Active" && s.MandateID == id && status == "Cancelled" {
					return m, invalid("%s is debited by %s: cancel the SIP first", id, s.ID)
				}
			}
		}
		p.Mandates[i].Status, p.Mandates[i].Remark = status, remark
		if umrn != "" {
			p.Mandates[i].UMRN = strings.ToUpper(strings.TrimSpace(umrn))
		}
		if err := o.putMFProfile(ctx, tenant, actor, p); err != nil {
			return m, err
		}
		o.log(ctx, tenant, actor, "mf.mandate.status", p.UCC, map[string]any{"mandate": id, "status": status, "umrn": umrn})
		return p.Mandates[i], nil
	}
	return MFMandate{}, ErrNotFound
}

// MFMandateRow is a mandate with its client, for the list.
type MFMandateRow struct {
	MFMandate
	UCC     string `json:"ucc"`
	Name    string `json:"name"`
	Overdue bool   `json:"overdue"` // still pending after the registration time
	SIPs    int    `json:"sips"`    // active SIPs on it
}

// MFMandates lists every client's mandates, pending first.
func (o *Ops) MFMandates(ctx context.Context, tenant string) ([]MFMandateRow, error) {
	ps, err := docs.List[MFProfile](ctx, o.Docs, tenant, cMFProf, "")
	if err != nil {
		return nil, err
	}
	ss, _ := o.MFSIPs(ctx, tenant, "")
	on := map[string]int{}
	for _, s := range ss {
		if s.Status == "Active" {
			on[s.MandateID]++
		}
	}
	cm, _ := o.ClientMap(ctx, tenant)
	today := o.now().Format("2006-01-02")
	out := []MFMandateRow{}
	for _, p := range ps {
		for _, m := range p.Mandates {
			out = append(out, MFMandateRow{MFMandate: m, UCC: p.UCC, Name: cm[p.UCC].Name, Overdue: m.Status == "Pending" && m.ExpectedBy < today, SIPs: on[m.ID]})
		}
	}
	rank := map[string]int{"Pending": 0, "Approved": 1, "Rejected": 2, "Cancelled": 3}
	sort.SliceStable(out, func(i, j int) bool {
		if rank[out[i].Status] != rank[out[j].Status] {
			return rank[out[i].Status] < rank[out[j].Status]
		}
		return out[i].ID > out[j].ID
	})
	return out, nil
}
