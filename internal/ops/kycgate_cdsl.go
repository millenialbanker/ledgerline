package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
)

// ---- the demat side of the KYC rules (CDSL) ----
//
// The rules, as the compliance gap report states them (the communiqués
// themselves were not re-read here; CDSL's reason codes are taken from
// it):
//   - CDSL/OPS/DP/POLCY/2026/234: every demat account where any holder's
//     KYC is not verified by a KRA is frozen for debit and credit under
//     reason code 27 (KYC non-compliant); such BOs do not transact until
//     the KRA verifies the KYC. CDSL/OPS/DP/POLCY/2026/770: the KYC of all
//     BOs, joint holders included, is at a KRA; frozen accounts are
//     unfrozen by the prescribed procedure once it is.
//   - CDSL/OPS/DP/POLCY/2026/325 and the monthly series: each cycle CDSL
//     puts in the DP's billing folder the BOs whose KYC is KRA-deficient;
//     the DP follows each up before CDSL's freeze date (loaded as the
//     cdsl-kra-deficient list, kycgate.go).
//   - CDSL/OPS/DP/POLCY/2025/618: a BO whose mobile is on DoT's
//     revocation list and who does not respond gets a 30-day notice to
//     register a valid mobile, saying the account will be frozen for
//     debits; then it is frozen for debits under reason code 31 (MNRL/FRI
//     non-compliant). Freezes made earlier under code 21 for an invalid
//     mobile are moved to code 31.
//
// Ledgerline does not freeze in CDAS itself: it raises the DP freeze
// requests (dp.go, maker-checker) with the reason code, and reads CDSL's
// freeze master (DPE6, cdslfreeze.go) to see the freeze in force.

const (
	cdslReasonKYC  = "27" // KYC non-compliant (CDSL/OPS/DP/POLCY/2026/234)
	cdslReasonMNRL = "31" // MNRL/FRI non-compliant (CDSL/OPS/DP/POLCY/2025/618)
	cdslReasonOld  = "21" // the old code moved to 31 for invalid mobiles

	// mnrlNoticeDays: the notice to register a valid mobile runs 30 days.
	mnrlNoticeDays = 30
)

func cdslCodeIs(code, want string) bool {
	return strings.TrimLeft(strings.TrimSpace(code), "0") == want
}

// DematHolderKYC is one holder of a demat account and the KRA status.
type DematHolderKYC struct {
	Holder    string `json:"holder"` // First, Second, Third
	Name      string `json:"name"`
	PAN       string `json:"pan"` // masked
	KRAStatus string `json:"kra_status"`
	Verified  bool   `json:"verified"`
}

// DematKYCRow is a demat account with a holder whose KYC is not verified.
type DematKYCRow struct {
	UCC     string           `json:"ucc"`
	Name    string           `json:"name"`
	BOID    string           `json:"bo_id"`
	Holders []DematHolderKYC `json:"holders"`
	// Frozen27: CDSL's freeze master shows a freeze under reason 27 in
	// force on the whole account; Request the DP freeze request raised.
	Frozen27 bool      `json:"frozen_27"`
	Freeze   string    `json:"freeze,omitempty"` // CDSL freeze in force (Debit, Credit, Both)
	Request  string    `json:"request,omitempty"`
	Holds    []KYCHold `json:"holds,omitempty"` // on CDSL's KRA-deficient list
}

// MNRLNoticeRow is a revoked-mobile match on a CDSL account and where its
// 30-day notice stands.
type MNRLNoticeRow struct {
	Match    string `json:"match"`
	UCC      string `json:"ucc"`
	Name     string `json:"name"`
	BOID     string `json:"bo_id,omitempty"`
	Holder   string `json:"holder"`
	Mobile   string `json:"mobile"`
	Status   string `json:"status"`
	NoticeOn string `json:"notice_on,omitempty"`
	Expiry   string `json:"expiry,omitempty"`
	Expired  bool   `json:"expired"`
	Request  string `json:"request,omitempty"` // the debit-freeze request (reason 31)
	Frozen31 bool   `json:"frozen_31"`         // CDSL shows the reason-31 freeze
	Next     string `json:"next"`              // what to do now
}

// Code21Row is a CDSL freeze still under the old code 21.
type Code21Row struct {
	UCC      string `json:"ucc"`
	BOID     string `json:"bo_id"`
	FreezeID string `json:"freeze_id"`
	Since    string `json:"since,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// DematKYCView is the demat part of the KYC trading gate screen.
type DematKYCView struct {
	Accounts []DematKYCRow   `json:"accounts"`
	Notices  []MNRLNoticeRow `json:"notices"`
	Code21   []Code21Row     `json:"code21"`
	Counts   map[string]int  `json:"counts"`
}

// holderKRA is a holder's KRA status: the client's own when the PAN is a
// client's, else what the KRA status file said for the joint holder.
func holderKRA(byPAN map[string]Client, pan, own string) string {
	if c, ok := byPAN[strings.ToUpper(strings.TrimSpace(pan))]; ok && pan != "" {
		return c.KRAStatus
	}
	return own
}

// dematKYCAccounts are the DP's CDSL accounts with a holder whose KYC is
// not verified, with what CDSL shows and what was requested.
func (o *Ops) dematKYCAccounts(ctx context.Context, tenant string, cs map[string]Client, date string) []DematKYCRow {
	byPAN := map[string]Client{}
	for _, c := range cs {
		if c.PAN != "" {
			byPAN[strings.ToUpper(c.PAN)] = c
		}
	}
	reqs := o.freezeRequests(ctx, tenant, cdslReasonKYC)
	var out []DematKYCRow
	for _, b := range o.boAccounts(ctx, tenant) {
		c, ok := cs[b.UCC]
		if !ok || b.BOID == "" || b.Status == "Closed" || b.Status == "Not opened" || !strings.EqualFold(firstOf(b.Depository, dematDepository(b.BOID)), "CDSL") {
			continue
		}
		hs := []DematHolderKYC{{Holder: "First", Name: c.Name, PAN: MaskPAN(c.PAN), KRAStatus: c.KRAStatus, Verified: kraVerified(c.KRAStatus)}}
		for i, j := range b.JointHolders {
			st := holderKRA(byPAN, j.PAN, j.KRAStatus)
			hs = append(hs, DematHolderKYC{Holder: []string{"Second", "Third"}[min(i, 1)], Name: j.Name, PAN: MaskPAN(j.PAN), KRAStatus: st, Verified: kraVerified(st)})
		}
		bad := false
		for _, h := range hs {
			bad = bad || !h.Verified
		}
		if !bad {
			continue
		}
		r := DematKYCRow{UCC: b.UCC, Name: c.Name, BOID: b.BOID, Holders: hs, Freeze: b.CDSLFreeze, Request: reqs[normDematID(b.BOID)]}
		for _, f := range o.cdslFreezesOf(ctx, tenant, b.BOID) {
			if f.ISIN == "" && f.blocking(date) && f.debit() && f.credit() && cdslCodeIs(f.ReasonCode, cdslReasonKYC) {
				r.Frozen27 = true
			}
		}
		for _, h := range c.KYCHolds {
			if h.List == KYCListCDSLDeficient && normDematID(h.BOID) == normDematID(b.BOID) {
				r.Holds = append(r.Holds, h)
			}
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UCC+out[i].BOID < out[j].UCC+out[j].BOID })
	return out
}

// freezeRequests are the DP freeze requests (not rejected) under a CDSL
// reason code, by BO ID.
func (o *Ops) freezeRequests(ctx context.Context, tenant, code string) map[string]string {
	rs, _ := docs.List[DPRequest](ctx, o.Docs, tenant, cDPReq, "")
	out := map[string]string{}
	for _, r := range rs {
		if r.Kind == "Freeze" && r.Status != "Rejected" && cdslCodeIs(r.Detail["reason_code"], code) {
			out[normDematID(r.BOID)] = r.ID
		}
	}
	return out
}

// dematKYC is the demat part of the screen.
func (o *Ops) dematKYC(ctx context.Context, tenant string, cs map[string]Client, date string) DematKYCView {
	v := DematKYCView{Accounts: []DematKYCRow{}, Notices: []MNRLNoticeRow{}, Code21: []Code21Row{}, Counts: map[string]int{}}
	v.Accounts = append(v.Accounts, o.dematKYCAccounts(ctx, tenant, cs, date)...)
	for _, a := range v.Accounts {
		switch {
		case a.Frozen27:
			v.Counts["Frozen (27)"]++
		case a.Request != "":
			v.Counts["Freeze requested"]++
		default:
			v.Counts["To freeze"]++
		}
	}
	v.Notices = append(v.Notices, o.mnrlNotices(ctx, tenant, cs, date)...)
	for _, n := range v.Notices {
		if n.Expired && n.Request == "" {
			v.Counts["Notices expired"]++
		}
	}
	v.Code21 = append(v.Code21, o.code21Freezes(ctx, tenant, date)...)
	v.Counts["Code 21 freezes"] = len(v.Code21)
	return v
}

// RaiseKYCFreezes raises a DP freeze request (debit and credit, CDSL
// reason 27) for every CDSL account with a holder whose KYC is not
// verified that is neither frozen under 27 nor already requested. A
// checker approves each on DP requests and the DP desk sets it in CDAS.
func (o *Ops) RaiseKYCFreezes(ctx context.Context, tenant, actor string) (int, error) {
	if err := o.require(ctx, tenant, actor, "Raising KYC freeze requests", RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return 0, err
	}
	cs, err := o.ClientMap(ctx, tenant)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, a := range o.dematKYCAccounts(ctx, tenant, cs, o.now().In(ist).Format("2006-01-02")) {
		if a.Frozen27 || a.Request != "" {
			continue
		}
		var who []string
		for _, h := range a.Holders {
			if !h.Verified {
				who = append(who, strings.ToLower(h.Holder)+" holder "+firstOf(h.KRAStatus, "no KRA status"))
			}
		}
		_, err := o.RaiseDPRequest(ctx, tenant, actor, DPRequest{Kind: "Freeze", UCC: a.UCC, BOID: a.BOID, Detail: map[string]string{
			"type": "Both", "reason_code": cdslReasonKYC,
			"reason": "KYC non-compliant (CDSL reason 27): " + strings.Join(who, ", ") + " not verified by a KRA"}})
		if err != nil {
			return n, err
		}
		n++
	}
	o.log(ctx, tenant, actor, "kycgate.freeze_requests", "", map[string]any{"raised": n, "reason_code": cdslReasonKYC})
	return n, nil
}

// ---- revoked mobiles on CDSL accounts: the 30-day notice ----

// mnrlCDSLAccount is the CDSL account of the DP a match is about ("" when
// none: an NSDL account, or a client without a CDSL account at the DP).
func (o *Ops) mnrlCDSLAccount(ctx context.Context, tenant string, m MNRLMatch) string {
	if m.Account != "" {
		if strings.EqualFold(firstOf(m.Depository, dematDepository(m.Account)), "CDSL") {
			if _, ok := o.boAccount(ctx, tenant, m.Account); ok {
				return normDematID(m.Account)
			}
		}
		return ""
	}
	for _, b := range o.boAccounts(ctx, tenant) {
		if b.UCC == m.UCC && b.BOID != "" && b.Status != "Closed" && strings.EqualFold(firstOf(b.Depository, dematDepository(b.BOID)), "CDSL") {
			return normDematID(b.BOID)
		}
	}
	return ""
}

// mnrlNotices are the open matches on CDSL accounts with their notice.
func (o *Ops) mnrlNotices(ctx context.Context, tenant string, cs map[string]Client, date string) []MNRLNoticeRow {
	ms, _ := docs.List[MNRLMatch](ctx, o.Docs, tenant, cMNRLMatch, "")
	reqs := o.freezeRequests(ctx, tenant, cdslReasonMNRL)
	var out []MNRLNoticeRow
	for _, m := range ms {
		if !mnrlOpen(m.Status) && m.NoticeOn == "" {
			continue
		}
		bo := o.mnrlCDSLAccount(ctx, tenant, m)
		if bo == "" {
			continue
		}
		r := MNRLNoticeRow{Match: m.ID, UCC: m.UCC, Name: m.Name, BOID: bo, Holder: m.Holder, Mobile: maskMobile(m.Mobile), Status: m.Status, NoticeOn: m.NoticeOn, Expiry: m.NoticeExpiry,
			Request: firstOf(m.FreezeRequest, reqs[bo])}
		for _, f := range o.cdslFreezesOf(ctx, tenant, bo) {
			if f.ISIN == "" && f.blocking(date) && f.debit() && cdslCodeIs(f.ReasonCode, cdslReasonMNRL) {
				r.Frozen31 = true
			}
		}
		r.Expired = r.Expiry != "" && r.Expiry < date
		switch {
		case !mnrlOpen(m.Status):
			if !r.Frozen31 && r.Request == "" {
				continue // updated before the notice ran out: nothing to do
			}
			r.Next = "Number updated: unfreeze the account (reason 31) once CDSL shows the new mobile"
		case r.NoticeOn == "":
			r.Next = "Send the 30-day notice to register a valid mobile (the account will be frozen for debits), and record it"
		case !r.Expired:
			r.Next = "Notice runs to " + fmtDay(r.Expiry)
		case r.Request == "":
			r.Next = "Notice expired: raise the debit freeze under reason 31"
		case !r.Frozen31:
			r.Next = "Freeze requested (" + r.Request + "): set it in CDAS"
		default:
			r.Next = "Frozen for debits (31) until a valid mobile is registered"
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UCC+out[i].Match < out[j].UCC+out[j].Match })
	_ = cs
	return out
}

// RecordMNRLNotice records the 30-day notice sent for an open match on a
// CDSL account (the date, how it was sent); it expires 30 days later.
func (o *Ops) RecordMNRLNotice(ctx context.Context, tenant, actor, id, on, note string) (MNRLMatch, error) {
	if err := o.require(ctx, tenant, actor, "Working revoked mobile numbers", RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return MNRLMatch{}, err
	}
	m, _, err := docs.Get[MNRLMatch](ctx, o.Docs, tenant, cMNRLMatch, id)
	if err != nil {
		return m, err
	}
	if !mnrlOpen(m.Status) {
		return m, fmt.Errorf("%w: %s is already %s", ErrState, id, strings.ToLower(m.Status))
	}
	if m.NoticeOn != "" {
		return m, fmt.Errorf("%w: the notice was recorded on %s", ErrState, fmtDay(m.NoticeOn))
	}
	if o.mnrlCDSLAccount(ctx, tenant, m) == "" {
		return m, fmt.Errorf("%w: the match is not about a CDSL account of the DP", ErrState)
	}
	today := o.now().In(ist).Format("2006-01-02")
	if on == "" {
		on = today
	}
	if !validDate(on) || on > today {
		return m, invalid("give the day the notice was sent (not a future date)")
	}
	if note = strings.TrimSpace(note); len(note) < 4 {
		return m, invalid("say how the notice was sent (registered e-mail, letter) and its reference")
	}
	m.NoticeOn, m.NoticeExpiry = on, addDays(on, mnrlNoticeDays)
	m.History = append(m.History, MNRLEvent{At: o.now().Format(time.RFC3339), By: actor, Status: m.Status, Note: "30-day notice sent " + fmtDay(on) + ", expires " + fmtDay(m.NoticeExpiry) + ": " + note})
	if err := docs.Upsert(ctx, o.Docs, tenant, cMNRLMatch, m.ID, m, actor); err != nil {
		return m, err
	}
	o.log(ctx, tenant, actor, "mnrl.notice", m.ID, map[string]any{"ucc": m.UCC, "on": on, "expiry": m.NoticeExpiry})
	return m, nil
}

// RaiseMNRLFreeze raises the debit freeze (CDSL reason 31) for a match
// whose notice expired with the mobile still not updated.
func (o *Ops) RaiseMNRLFreeze(ctx context.Context, tenant, actor, id string) (DPRequest, error) {
	if err := o.require(ctx, tenant, actor, "Working revoked mobile numbers", RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return DPRequest{}, err
	}
	m, _, err := docs.Get[MNRLMatch](ctx, o.Docs, tenant, cMNRLMatch, id)
	if err != nil {
		return DPRequest{}, err
	}
	today := o.now().In(ist).Format("2006-01-02")
	switch {
	case !mnrlOpen(m.Status):
		return DPRequest{}, fmt.Errorf("%w: %s is %s", ErrState, id, strings.ToLower(m.Status))
	case m.NoticeExpiry == "" || m.NoticeExpiry >= today:
		return DPRequest{}, fmt.Errorf("%w: the 30-day notice has not expired", ErrState)
	case m.FreezeRequest != "":
		return DPRequest{}, fmt.Errorf("%w: the freeze was requested (%s)", ErrState, m.FreezeRequest)
	}
	bo := o.mnrlCDSLAccount(ctx, tenant, m)
	if bo == "" {
		return DPRequest{}, fmt.Errorf("%w: the match is not about a CDSL account of the DP", ErrState)
	}
	r, err := o.RaiseDPRequest(ctx, tenant, actor, DPRequest{Kind: "Freeze", UCC: m.UCC, BOID: bo, Detail: map[string]string{"type": "Debit", "reason_code": cdslReasonMNRL,
		"reason": "MNRL/FRI non-compliant (CDSL reason 31): no valid mobile registered within the 30-day notice of " + fmtDay(m.NoticeOn) + " (" + m.ID + ")"}})
	if err != nil {
		return r, err
	}
	m.FreezeRequest = r.ID
	m.History = append(m.History, MNRLEvent{At: o.now().Format(time.RFC3339), By: actor, Status: m.Status, Note: "Debit freeze under reason 31 requested: " + r.ID})
	if err := docs.Upsert(ctx, o.Docs, tenant, cMNRLMatch, m.ID, m, actor); err != nil {
		return r, err
	}
	o.closeWork(ctx, tenant, actor, wKYCGate, "mnrl31:"+m.ID, "requested "+r.ID)
	return r, nil
}

// mnrlNoticeDaily raises a work item for each notice that has expired with
// no freeze requested.
func (o *Ops) mnrlNoticeDaily(ctx context.Context, tenant, day string) {
	for _, n := range o.mnrlNotices(ctx, tenant, nil, day) {
		if n.Expired && n.Request == "" && mnrlOpen(n.Status) {
			o.raiseWork(ctx, tenant, wKYCGate, "mnrl31:"+n.Match, "Freeze "+n.UCC+"'s demat for debits (CDSL reason 31)",
				fmt.Sprintf("The 30-day notice to register a valid mobile (match %s) expired on %s and the mobile is still on DoT's revocation list. Raise the debit freeze under reason 31 on Compliance → KYC trading gate (Demat accounts), have a checker approve it and set it in CDAS.", n.Match, fmtDay(n.Expiry)),
				n.UCC, RoleMaker, "kycgate", day)
		}
	}
}

// code21Freezes are freezes in force still under the old code 21, to be
// moved to 31 where they were for an invalid mobile.
func (o *Ops) code21Freezes(ctx context.Context, tenant, date string) []Code21Row {
	fs, _ := docs.List[CDSLFreeze](ctx, o.Docs, tenant, cCDSLFreeze, "")
	var out []Code21Row
	for _, f := range fs {
		if !f.blocking(date) || !cdslCodeIs(f.ReasonCode, cdslReasonOld) {
			continue
		}
		out = append(out, Code21Row{UCC: f.UCC, BOID: f.BOID, FreezeID: f.ID, Since: firstOf(f.ActiveFrom, f.InitAt), Reason: f.Reason})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].BOID+out[i].FreezeID < out[j].BOID+out[j].FreezeID })
	return out
}
