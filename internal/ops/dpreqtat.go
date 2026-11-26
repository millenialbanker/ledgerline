package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// ---- DP requests: the checks before they go, and their timelines ----
//
// Closures (CDSL DPOI 10.5.3.6, 10.5.3.20-10.5.3.24, 10.7.2-10.7.3; CDSL
// DP2025-451 for closure-cum-shifting, the same timelines):
//   - no dues: the free balances transferred and the account closed within
//     2 working days of the request;
//   - dues: the holder told within 2 working days, given up to 30 days to
//     pay, the account closed within 2 working days of payment, or the
//     request rejected with the dues details within 2 working days of the
//     30 days running out;
//   - the closure confirmed to the holder within 2 working days, with the
//     final statement (appendix DP-0087/DP-0017); a rejection's reason told
//     to the holder (NSE/INSP/73976 item 1.15 (viii) for trading accounts;
//     the same rule is kept for demat closures);
//   - not set up while the account has a freeze or a pledge (DPOI 10.7.2),
//     or a demat request pending (appendix DP-0251);
//   - moving the holdings to an account whose holders have the same PANs
//     is free (MC 1.10.2-1.10.3); to another account, the holder signs a
//     DIS and confirms by OTP (appendix DP-0087/DP-0017);
//   - closed by the DP itself only after 30 days' written notice, approved
//     by an authorised official (appendix DP-0119/DP-0252; Ledgerline takes
//     the compliance officer as the authorised official).
// Service times after the documents are complete (SEBI investor charter,
// MC Annexure 21 s4(1)): demat 7 days, transmission 7 days (also DPOI
// 7.5.11), closure 30 days (the 2-working-day rules are stricter and come
// first). Calendar days, as the charter says "days".
// Demat requests (appendix DP-0194): the RTA followed up 21 days after the
// request went to it; documents of a rejected request got back from the
// RTA within 30 days and returned to the holder within 7 days.
// Transmission (CDSL DP2026-575/580, DPOI 7.6.1): the claim's category by
// value (quick processing up to ₹30,000; simplified documents up to ₹30
// lakh; full documents above), deficiencies told to the claimant, and a
// fresh nomination from the survivors when a joint holder dies (appendix
// DP-0158/DP-0227).
// Freezes (CDSL DP2025-267; appendix DP-0260/DP-0262): the holder's signed
// request for a freeze the holder asks for, the authority's order for a
// lien, and an intimation to the holder of every freeze and unfreeze. When
// it must go is not in the text Ledgerline has: the next working day.

const (
	wDPCtl = "dpctl" // work items of the DP controls

	closureWD      = 2  // working days
	closureDuesDay = 30 // days to pay the dues
	dpNoticeDays   = 30 // the DP's notice before closing an account itself
	dematRTADays   = 21
	dematBackDays  = 30
	dematReturnDay = 7
)

// dpCharterDays are the investor charter's service times by request kind.
var dpCharterDays = map[string]int{"Demat": 7, "Transmission": 7, "Closure": 30}

// DPStep is one thing done on a request.
type DPStep struct {
	On   string `json:"on"`
	By   string `json:"by"`
	Ref  string `json:"ref,omitempty"`
	Note string `json:"note,omitempty"`
}

func (r *DPRequest) step(k, on, by, ref string) {
	if r.Steps == nil {
		r.Steps = map[string]DPStep{}
	}
	r.Steps[k] = DPStep{On: on, By: by, Ref: ref}
}

func (r DPRequest) stepOn(k string) string { return r.Steps[k].On }

// day is the request's date (IST).
func (r DPRequest) day() string {
	if t, err := time.Parse(time.RFC3339, r.At); err == nil {
		return t.In(ist).Format("2006-01-02")
	}
	if len(r.At) >= 10 {
		return r.At[:10]
	}
	return r.At
}

// DPStepNames are the steps that can be recorded, by request kind.
var DPStepNames = map[string]map[string]string{
	"Closure": {"dues_notice": "Holder told of the dues", "dues_paid": "Dues paid", "closure_letter": "Closure confirmed with the final statement",
		"rejection_sent": "Holder told why it was rejected", "docs_complete": "Documents complete"},
	"Demat": {"docs_complete": "Documents complete", "rta_followup": "RTA followed up", "rta_docs_back": "Rejected documents back from the RTA",
		"docs_returned": "Documents returned to the holder", "rejection_sent": "Holder told why it was rejected"},
	"Transmission": {"docs_complete": "Documents complete", "deficiency": "Deficiency told to the claimant", "nomination": "Fresh nomination from the survivors",
		"rejection_sent": "Claimant told why it was rejected"},
	"Freeze":   {"intimated": "Holder told (signed acknowledgement)", "lien_notice": "Lien notice to the holder"},
	"Unfreeze": {"intimated": "Holder told (signed acknowledgement)"},
}

// RecordDPStep records something done on a request: a notice or letter to
// the holder, the dues paid, documents complete or returned.
func (o *Ops) RecordDPStep(ctx context.Context, tenant, actor, id, step, on, ref, note string) (DPRequest, error) {
	if err := o.require(ctx, tenant, actor, "DP requests", RoleMaker, RoleChecker, RoleCompliance, RoleBranch); err != nil {
		return DPRequest{}, err
	}
	today := o.now().In(ist).Format("2006-01-02")
	on = firstOf(strings.TrimSpace(on), today)
	if !validDate(on) || on > today {
		return DPRequest{}, invalid("give the date it was done (not after today)")
	}
	r, err := docs.Update(ctx, o.Docs, tenant, cDPReq, id, actor, func(x *DPRequest) error {
		name, ok := DPStepNames[x.Kind][step]
		if !ok {
			return invalid("%s is not a step of a %s request", step, strings.ToLower(x.Kind))
		}
		if on < x.day() {
			return invalid("%s was on or after the request (%s)", strings.ToLower(name), fmtDay(x.day()))
		}
		needRef := map[string]bool{"dues_notice": true, "closure_letter": true, "rejection_sent": true, "deficiency": true, "nomination": true, "intimated": true, "lien_notice": true, "docs_returned": true}
		if needRef[step] && strings.TrimSpace(ref) == "" {
			return invalid("give the reference (letter, e-mail or courier number; the nomination form or opt-out)")
		}
		switch step {
		case "closure_letter", "intimated", "lien_notice", "docs_returned", "rta_docs_back":
			if x.Status != "Completed" && x.Status != "Rejected" {
				return fmt.Errorf("%w: %s is %s", ErrState, id, strings.ToLower(x.Status))
			}
		case "rejection_sent":
			if x.Status != "Rejected" {
				return fmt.Errorf("%w: %s was not rejected", ErrState, id)
			}
		case "dues_notice", "dues_paid", "deficiency", "nomination", "docs_complete", "rta_followup":
			if x.Status == "Completed" || x.Status == "Rejected" {
				return fmt.Errorf("%w: %s is %s", ErrState, id, strings.ToLower(x.Status))
			}
		}
		x.step(step, on, actor, strings.TrimSpace(ref))
		if n := strings.TrimSpace(note); n != "" {
			s := x.Steps[step]
			s.Note = n
			x.Steps[step] = s
		}
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "dp.request_step", id, map[string]any{"step": step, "on": on, "ref": ref})
		o.dpReqWork(ctx, tenant, r, today)
	}
	return r, err
}

// ---- checks ----

// dpDues is what the client owes on the ledger the DP bills post to (the
// equity ledger, CLIENT:<ucc>), as a positive amount (0: nothing owed).
func (o *Ops) dpDues(ctx context.Context, tenant, ucc string) money.Amount {
	acct := ledger.ClientAccount(ucc)
	es, err := o.App.Ledger.Store.List(ctx, tenant, ledger.Filter{Account: acct})
	if err != nil {
		return 0
	}
	var bal money.Amount
	for _, e := range es {
		for _, p := range e.Postings {
			if p.Account == acct {
				bal += p.Credit - p.Debit
			}
		}
	}
	if bal < 0 {
		return -bal
	}
	return 0
}

// holderPANs are the PANs of every holder of the account.
func holderPANs(c Client, b BOAccount) []string {
	ps := []string{strings.ToUpper(strings.TrimSpace(c.PAN))}
	for _, j := range b.JointHolders {
		ps = append(ps, strings.ToUpper(strings.TrimSpace(j.PAN)))
	}
	sort.Strings(ps)
	return ps
}

func panList(s string) []string {
	var ps []string
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' || r == ' ' }) {
		ps = append(ps, strings.ToUpper(p))
	}
	sort.Strings(ps)
	return ps
}

// pendingDemats are the account's demat requests not finished.
func (o *Ops) pendingDemats(ctx context.Context, tenant string, c Client, b BOAccount) []string {
	rs, _ := docs.List[DPRequest](ctx, o.Docs, tenant, cDPReq, "")
	var out []string
	for _, r := range rs {
		if r.Kind == "Demat" && r.UCC == c.UCC && normDematID(r.BOID) == normDematID(b.BOID) && r.Status != "Completed" && r.Status != "Rejected" {
			out = append(out, r.ID)
		}
	}
	return out
}

// closureBlock says why the account cannot be closed now ("" if it can):
// a freeze (other than the one transmission leaves) or a pledge on it
// (DPOI 10.7.2), or a demat request pending (DP-0251).
func (o *Ops) closureBlock(ctx context.Context, tenant string, c Client, b BOAccount) string {
	if p := o.pendingDemats(ctx, tenant, c, b); len(p) > 0 {
		return "a demat request is pending (" + strings.Join(p, ", ") + "): finish or reject it first"
	}
	transmitted := b.Status == "Frozen" && strings.HasPrefix(b.FreezeWhy, "Transmission completed")
	if (b.Status == "Frozen" && !transmitted) || b.CDSLFreeze != "" || b.Sanctioned != "" {
		return "the account is frozen (" + b.freezeWhy() + "): a closure is not set up while a freeze is on"
	}
	hs, _ := o.Holdings(ctx, tenant, c.UCC)
	acct := holdingAcct(c, b.BOID)
	for _, h := range hs {
		if h.BOID == acct && h.Pledged.Sign() > 0 {
			return "the account has " + firstOf(h.Symbol, h.ISIN) + " pledged: close the pledge first"
		}
	}
	return ""
}

// cdslClosureAPI says whether closures go to CDSL by its account transfer
// cum closure API, where the client confirms the transfer with an OTP on
// CDSL's page (cdslredirect.go).
func (o *Ops) cdslClosureAPI(ctx context.Context, tenant string, b BOAccount) bool {
	x, ok := o.cdslConnFor(ctx, tenant)
	return ok && x.On(cdslFamTransfer) && b.Depository == "CDSL" && b.BOID != ""
}

// dpRequestChecks are RaiseDPRequest's checks of dpreqtat.go; they fill in
// what Ledgerline works out (dues, identical holders, the claim's
// category).
func (o *Ops) dpRequestChecks(ctx context.Context, tenant string, c Client, b BOAccount, kind string, d map[string]string) error {
	trim := func(k string) string { d[k] = strings.TrimSpace(d[k]); return d[k] }
	switch kind {
	case "Closure":
		if why := o.closureBlock(ctx, tenant, c, b); why != "" {
			return fmt.Errorf("%w: %s", ErrState, why)
		}
		switch by := firstOf(trim("initiated_by"), "Client"); by {
		case "Client":
			d["initiated_by"] = by
		case "DP":
			if !validDate(trim("notice_sent")) {
				return invalid("a closure the DP starts needs the date its 30 days' written notice was sent")
			}
			d["initiated_by"] = by
		default:
			return invalid("the closure is initiated by the Client or the DP")
		}
		if to := trim("transfer_to"); to != "" {
			if trim("target_pan") != "" {
				same := strings.Join(panList(d["target_pan"]), ",") == strings.Join(holderPANs(c, b), ",")
				d["identical_holders"] = map[bool]string{true: "Yes", false: "No"}[same]
			}
			if d["identical_holders"] != "Yes" && !o.cdslClosureAPI(ctx, tenant, b) && (trim("dis_slip") == "" || trim("otp_ref") == "") {
				return invalid("the holdings go to an account whose holders are not shown to be the same (give the target account's holders' PANs from its client master): the holder signs a DIS and confirms by OTP; give the slip number and the OTP reference")
			}
		}
		if due := o.dpDues(ctx, tenant, c.UCC); due > 0 {
			d["dues"] = due.String()
		} else {
			delete(d, "dues")
		}
	case "Freeze":
		switch by := firstOf(trim("by"), "DP"); by {
		case "Client":
			if trim("signed_request") == "" {
				return invalid("a freeze the holder asks for needs the holder's signed request (all holders of a joint account)")
			}
			d["by"] = by
		case "Authority":
			if trim("order_ref") == "" {
				return invalid("a freeze by order of an authority (lien, attachment) needs the order's reference")
			}
			d["by"] = by
		case "DP":
			d["by"] = by
		default:
			return invalid("the freeze is asked by the Client, the DP or an Authority")
		}
	case "Transmission":
		// The claim's category by value at the last close (DPOI 7.6.1 as
		// amended by CDSL DP2026-575/580).
		hs, _ := o.Holdings(ctx, tenant, c.UCC)
		_, look, _ := o.Market(ctx, tenant, o.now().In(ist).Format("2006-01-02"))
		acct := holdingAcct(c, b.BOID)
		var v money.Dec
		for _, h := range hs {
			if h.BOID == acct && h.Qty.Sign() > 0 {
				if i, ok := look.ByISIN(h.ISIN); ok {
					v = v.Add(i.LTP.Mul(h.Qty))
				}
			}
		}
		cat, need := transmissionCategory(v)
		d["value"], d["category"], d["documents_needed"] = v.Fixed(2), cat, need
		if s := strings.ToLower(trim("survivors")); s == "yes" || s == "y" {
			d["survivors"] = "Yes"
		}
	}
	return nil
}

// transmissionCategory is the claim's category and the documents asked,
// by value. The lists name the forms the CDSL communiqués name; the full
// list is CDSL's SOP (DP2026-575 Annexure A1), to check against it.
func transmissionCategory(v money.Dec) (string, string) {
	base := "transmission request form (TRF); death certificate (a verifiable copy; QR-code copies accepted); the claimant's KYC"
	switch {
	case v.Cmp(money.DecInt(30000)) <= 0:
		return "Quick transmission (up to ₹30,000)", base + "; the quick transmission processing (QTP) form"
	case v.Cmp(money.DecInt(3000000)) <= 0:
		return "Simplified documents (up to ₹30 lakh)", base + "; indemnity; combined affidavit-cum-NOC of the other legal heirs (no probate insisted on)"
	}
	return "Full documents (above ₹30 lakh)", base + "; the documents CDSL's SOP asks above ₹30 lakh (succession certificate, probate or letter of administration as it says)"
}

// dpApproveChecks are ActOnDPRequest's checks before a request is
// approved.
func (o *Ops) dpApproveChecks(ctx context.Context, tenant, actor string, r DPRequest, c Client, b BOAccount) error {
	today := o.now().In(ist).Format("2006-01-02")
	switch r.Kind {
	case "Closure":
		if why := o.closureBlock(ctx, tenant, c, b); why != "" {
			return fmt.Errorf("%w: %s", ErrState, why)
		}
		if r.Detail["initiated_by"] == "DP" {
			if end := addDays(r.Detail["notice_sent"], dpNoticeDays); today < end {
				return fmt.Errorf("%w: the DP's 30 days' notice runs to %s", ErrState, fmtDay(end))
			}
		}
		if due := o.dpDues(ctx, tenant, c.UCC); due > 0 && r.stepOn("dues_paid") == "" {
			return fmt.Errorf("%w: ₹%s is due from the holder: record the payment, or reject the request with the dues once the 30 days have run out", ErrState, due.String())
		}
	case "Transmission":
		if r.Detail["survivors"] == "Yes" && r.stepOn("nomination") == "" {
			return fmt.Errorf("%w: the account goes on with the surviving holders: record their fresh nomination (or opt-out) first", ErrState)
		}
	}
	return nil
}

// dpRejectNote is the rejection reason, with the dues for a closure
// rejected because they were not paid.
func (o *Ops) dpRejectNote(ctx context.Context, tenant string, r DPRequest, note string) string {
	if r.Kind != "Closure" {
		return note
	}
	if due := o.dpDues(ctx, tenant, r.UCC); due > 0 && !strings.Contains(note, "₹") {
		return strings.TrimSpace(note) + fmt.Sprintf(" Dues outstanding: ₹%s.", due.String())
	}
	return note
}

// closureFree says whether a slip moves holdings on closure to an account
// of identical holders, which is not charged (MC 1.10.2-1.10.3).
func (o *Ops) closureFree(ctx context.Context, tenant string, d DIS) bool {
	if d.ForClosure == "" {
		return false
	}
	r, _, err := docs.Get[DPRequest](ctx, o.Docs, tenant, cDPReq, d.ForClosure)
	return err == nil && r.Kind == "Closure" && r.Detail["identical_holders"] == "Yes" && strings.EqualFold(r.Detail["transfer_to"], d.ToBO)
}

// forClosureCheck checks the closure request a slip names.
func (o *Ops) forClosureCheck(ctx context.Context, tenant string, c Client, b BOAccount, d DIS) error {
	if d.ForClosure == "" {
		return nil
	}
	r, _, err := docs.Get[DPRequest](ctx, o.Docs, tenant, cDPReq, d.ForClosure)
	if err != nil || r.Kind != "Closure" || r.UCC != c.UCC || normDematID(r.BOID) != normDematID(b.BOID) || r.Status != "Pending" {
		return invalid("%s is not a pending closure of this account", d.ForClosure)
	}
	if !strings.EqualFold(r.Detail["transfer_to"], d.ToBO) {
		return invalid("the closure's holdings go to %s", r.Detail["transfer_to"])
	}
	return nil
}

// ---- timelines ----

// DPDuty is one thing due on a request.
type DPDuty struct {
	What string `json:"what"`
	Due  string `json:"due"`
	Done string `json:"done,omitempty"`
	Late bool   `json:"late,omitempty"`
	Step string `json:"step,omitempty"` // the step that records it, if any
}

// dpDuties are the request's timelines, as of today.
func dpDuties(set Settings, r DPRequest, today string) []DPDuty {
	var out []DPDuty
	add := func(what, due, done, step string) {
		if due == "" {
			return
		}
		late := (done != "" && done > due) || (done == "" && today > due)
		out = append(out, DPDuty{What: what, Due: due, Done: done, Late: late, Step: step})
	}
	start := firstOf(r.stepOn("docs_complete"), r.day())
	if r.Kind == "Closure" && r.Detail["initiated_by"] == "DP" {
		// The DP's own closure follows its 30 days' notice, not the
		// holder's timelines; the closure is still confirmed.
		if r.Status == "Completed" {
			add("Closure confirmed with the final statement within 2 working days", addWorkingDays(set, r.DoneOn, closureWD), r.stepOn("closure_letter"), "closure_letter")
		}
		return out
	}
	if n, ok := dpCharterDays[r.Kind]; ok && r.Kind != "Closure" {
		add(fmt.Sprintf("Completed within %d days of the documents (investor charter)", n), addDays(start, n), r.DoneOn, "")
	}
	switch r.Kind {
	case "Closure":
		if r.Detail["dues"] == "" {
			add("Closed within 2 working days of the request", addWorkingDays(set, r.day(), closureWD), r.DoneOn, "")
		} else {
			notice := r.stepOn("dues_notice")
			add("Holder told of the dues within 2 working days", addWorkingDays(set, r.day(), closureWD), notice, "dues_notice")
			if paid := r.stepOn("dues_paid"); paid != "" {
				add("Closed within 2 working days of the payment", addWorkingDays(set, paid, closureWD), r.DoneOn, "")
			} else if notice != "" {
				add("Rejected with the dues within 2 working days of the 30 days", addWorkingDays(set, addDays(notice, closureDuesDay), closureWD), r.DoneOn, "")
			}
		}
		add("Completed within 30 days (investor charter)", addDays(start, dpCharterDays["Closure"]), r.DoneOn, "")
		if r.Status == "Completed" {
			add("Closure confirmed with the final statement within 2 working days", addWorkingDays(set, r.DoneOn, closureWD), r.stepOn("closure_letter"), "closure_letter")
		}
	case "Demat":
		if sent := r.stepOn("sent"); sent != "" && r.Status == "Sent to RTA" {
			add("RTA followed up 21 days after the request went to it", addDays(sent, dematRTADays), r.stepOn("rta_followup"), "rta_followup")
		}
		if r.Status == "Rejected" {
			if r.stepOn("sent") != "" {
				add("Rejected documents back from the RTA within 30 days", addDays(r.DoneOn, dematBackDays), r.stepOn("rta_docs_back"), "rta_docs_back")
			}
			from := firstOf(r.stepOn("rta_docs_back"), r.DoneOn)
			add("Documents returned to the holder within 7 days", addDays(from, dematReturnDay), r.stepOn("docs_returned"), "docs_returned")
		}
	case "Freeze", "Unfreeze":
		if r.Status == "Completed" {
			add("Holder told of the "+strings.ToLower(r.Kind)+" (signed acknowledgement)", set.NextBusinessDay(r.DoneOn), r.stepOn("intimated"), "intimated")
			if r.Kind == "Freeze" && r.Detail["by"] == "Authority" {
				add("Lien notice to the holder", set.NextBusinessDay(r.DoneOn), r.stepOn("lien_notice"), "lien_notice")
			}
		}
	}
	if r.Status == "Rejected" && DPStepNames[r.Kind]["rejection_sent"] != "" {
		add("Told why it was rejected within 2 working days", addWorkingDays(set, r.DoneOn, closureWD), r.stepOn("rejection_sent"), "rejection_sent")
	}
	return out
}

// DPRequestTimeline is a request with its duties.
type DPRequestTimeline struct {
	DPRequest
	Duties []DPDuty `json:"duties"`
	Late   int      `json:"late"`
}

// DPRequestTimelines lists the requests with their duties, those with a
// late duty first.
func (o *Ops) DPRequestTimelines(ctx context.Context, tenant, actor string) ([]DPRequestTimeline, error) {
	if err := o.require(ctx, tenant, actor, "Viewing the DP controls", RoleMaker, RoleChecker, RoleCompliance, RoleBranch); err != nil {
		return nil, err
	}
	set, _ := o.Settings(ctx, tenant)
	today := o.now().In(ist).Format("2006-01-02")
	rs, err := docs.List[DPRequest](ctx, o.Docs, tenant, cDPReq, "")
	out := []DPRequestTimeline{}
	for _, r := range rs {
		t := DPRequestTimeline{DPRequest: r, Duties: dpDuties(set, r, today)}
		open := false
		for _, d := range t.Duties {
			if d.Late && d.Done == "" {
				t.Late++
			}
			open = open || d.Done == ""
		}
		if !open && r.DoneOn != "" && r.DoneOn < addDays(today, -90) {
			continue // done long ago
		}
		out = append(out, t)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].Late > 0) != (out[j].Late > 0) {
			return out[i].Late > 0
		}
		return out[i].ID > out[j].ID
	})
	return out, err
}

// dpReqWork raises a work item for each duty of the request falling due
// (from the day before) and not done, and closes those done.
func (o *Ops) dpReqWork(ctx context.Context, tenant string, r DPRequest, today string) {
	set, _ := o.Settings(ctx, tenant)
	for _, d := range dpDuties(set, r, today) {
		key := "req:" + r.ID + ":" + dutySlug(d.What)
		if d.Done != "" {
			o.closeWork(ctx, tenant, SystemActor, wDPCtl, key, "done "+d.Done)
			continue
		}
		if set.PrevBusinessDay(d.Due) > today && d.Due > today {
			continue
		}
		o.workDue(ctx, tenant, wDPCtl, key, fmt.Sprintf("%s %s of %s: %s", r.Kind, r.ID, r.UCC, strings.ToLower(d.What[:1])+d.What[1:]),
			fmt.Sprintf("%s request %s (%s) is due: %s, by %s. Record it on Depository → Demat accounts & DIS (Requests) once done.", r.Kind, r.ID, r.Name, d.What, fmtDay(d.Due)),
			r.UCC, map[bool]string{true: RoleChecker, false: RoleMaker}[r.Kind == "Closure"], "dp", r.day(), d.Due+" 17:00")
	}
}

// dutySlug is a short key for a duty: its first words, in letters and
// digits.
func dutySlug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
		if b.Len() >= 40 {
			break
		}
	}
	return strings.TrimRight(b.String(), "-")
}
