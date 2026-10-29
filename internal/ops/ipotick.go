package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
)

// ---- the IPO day: mandate tracker, API polling, allotment follow-up ----

func init() {
	EventVars["ipo.bid_ack"] = []string{"ack", "action", "issue", "bid", "lots", "amount", "upi", "close"}
	EventVars["ipo.mandate_pending"] = []string{"issue", "bid", "amount", "upi", "cutoff"}
	EventVars["ipo.mandate_lapsed"] = []string{"issue", "bid", "amount"}
	EventVars["ipo.allotment"] = []string{"issue", "bid", "lots", "shares", "release", "credit_by"}
	t := func(ev, ch, cat, subj, body, dlt string) Template {
		return Template{Event: ev, Channel: ch, Category: cat, Subject: subj, Body: body, DLT: dlt, Active: true}
	}
	extraTemplates = append(extraTemplates,
		t("ipo.bid_ack", ChEmail, "Transactional", "{issue}: your instruction {ack}", "Dear {name},\n\nWe have recorded your instruction to {action} your application {bid} in {issue}: {lots} lot(s), ₹{amount}. Acknowledgement number: {ack}.\n\nFor a UPI bid, approve the mandate request in your UPI app ({upi}) when it arrives: bids close at {close}, and mandates must be approved by 5 pm on the closing day or the bid is not considered.\n\nIf you did not give this instruction, tell us at once.\n\n{firm}", ""),
		t("ipo.bid_ack", ChSMS, "Transactional", "", "Dear {name}, your instruction to {action} IPO application {bid} in {issue} ({lots} lot(s), Rs {amount}) is recorded. Ack {ack}. Approve the UPI mandate in your UPI app when it arrives. -{firm}", "1107166000000000021"),
		t("ipo.mandate_pending", ChSMS, "Transactional", "", "Dear {name}, the UPI mandate for your {issue} application {bid} (Rs {amount}) is not approved yet. Approve it in your UPI app ({upi}) before {cutoff} today or the bid will not count. -{firm}", "1107166000000000022"),
		t("ipo.mandate_pending", ChWhatsApp, "Transactional", "", "{name}, approve the UPI mandate for your {issue} application {bid} (₹{amount}) in your UPI app ({upi}) before {cutoff} today. A bid without an approved mandate is not considered.", ""),
		t("ipo.mandate_lapsed", ChEmail, "Transactional", "{issue}: your application {bid} was not considered", "Dear {name},\n\nThe UPI mandate for your application {bid} in {issue} (₹{amount}) was not approved by the 5 pm cut-off on the closing day, so the bid will not be considered for allotment. No money was blocked for it.\n\n{firm}", ""),
		t("ipo.allotment", ChEmail, "Transactional", "{issue}: allotment of your application {bid}", "Dear {name},\n\nThe basis of allotment of {issue} is final. Your application {bid}: {lots} lot(s) allotted ({shares} shares). Blocked money not used, ₹{release}, is to be released by your bank, and allotted shares are credited to your demat account, by {credit_by}.\n\nIf the money is not released by then, tell us: the bank responsible pays compensation for the delay.\n\n{firm}", ""),
		t("ipo.allotment", ChSMS, "Transactional", "", "Dear {name}, {issue} allotment: application {bid}, {lots} lot(s) allotted. Unused money Rs {release} to be released and shares credited by {credit_by}. -{firm}", "1107166000000000023"),
	)
}

// ipoTickState records what the IPO day has done for an issue.
type ipoTickState struct {
	Done map[string]string `json:"done"`
}

// ipoOnce reports whether key is new for the issue, and records it.
func (o *Ops) ipoOnce(ctx context.Context, tenant, issue, key string, now time.Time) bool {
	id := "tick|" + issue
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cIPOState, id, ipoTickState{})
	if st.Done == nil {
		st.Done = map[string]string{}
	}
	if st.Done[key] != "" {
		return false
	}
	st.Done[key] = now.Format(time.RFC3339)
	return docs.Upsert(ctx, o.Docs, tenant, cIPOState, id, st, SystemActor) == nil
}

func (o *Ops) ipoDone(ctx context.Context, tenant, issue string) map[string]string {
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cIPOState, "tick|"+issue, ipoTickState{})
	if st.Done == nil {
		return map[string]string{}
	}
	return st.Done
}

// ipoTick runs the IPO day for every issue: at the close (4 pm on T)
// clients whose UPI mandates are not approved are reminded and the desk
// gets the list; at the mandate cut-off (5 pm) mandates not approved lapse
// and the desk gets what lapsed; with the eIPO API switched on, waiting
// bids are sent and bids in flight asked about; from the credit day (T+2)
// demat credits are confirmed and late credits and releases raised.
func (o *Ops) ipoTick(ctx context.Context, tenant string, now time.Time) {
	is, err := docs.List[IPOIssue](ctx, o.Docs, tenant, cIPOIssue, "")
	if err != nil || len(is) == 0 {
		return
	}
	set, _ := o.Settings(ctx, tenant)
	now = now.In(ist)
	today, hm := now.Format("2006-01-02"), now.Format("15:04")
	eipo, eipoOK := o.eipoConnection(ctx, tenant)
	for _, iss := range is {
		iss.defaults()
		iss.Timeline = iss.timeline(set)
		if iss.Withdrawn || today < iss.Open {
			continue
		}
		if eipoOK && eipo.send && eipo.usable && contains(iss.Exchanges, "NSE") && iss.Type != IssueOFS {
			o.eipoTick(ctx, tenant, iss, set, now)
		}
		if iss.Type != IssueOFS && today == iss.Close {
			if hm >= iss.CloseTime {
				o.mandateAlert(ctx, tenant, iss, "close", now)
			}
			if iss.MandateCutoff != "" && hm >= iss.MandateCutoff {
				o.mandateAlert(ctx, tenant, iss, "cutoff", now)
			}
		}
		if c := iss.Timeline.Credit; c != "" && today >= c && hm >= "18:30" && daysBetween(c, today) <= 30 {
			o.ipoFollowUp(ctx, tenant, iss, now)
		}
	}
}

// eipoTick sends waiting bids every 5 minutes while bidding is open, and
// asks about bids in flight every 15 minutes until the day after the close.
func (o *Ops) eipoTick(ctx context.Context, tenant string, iss IPOIssue, set Settings, now time.Time) {
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cIPOState, "eipo", EIPOState{})
	since := func(s string) time.Duration {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return time.Hour * 24
		}
		return now.Sub(t)
	}
	if iss.liveAt(set, now) && since(st.LastSend) >= 5*time.Minute {
		if ws, _ := o.waitingBids(ctx, tenant, iss, "NSE"); len(ws) > 0 {
			_, _ = o.EIPOSend(ctx, tenant, SystemActor, iss.ID)
		}
	}
	if now.Format("2006-01-02") <= set.NextBusinessDay(iss.Close) && since(st.LastInquiry) >= 15*time.Minute {
		_, _ = o.EIPOInquire(ctx, tenant, SystemActor, iss.ID)
	}
}

// mandateAlert runs the 4 pm (close) or 5 pm (cutoff) step of the
// closing day, once.
func (o *Ops) mandateAlert(ctx context.Context, tenant string, iss IPOIssue, kind string, now time.Time) {
	if !o.ipoOnce(ctx, tenant, iss.ID, kind+":"+iss.Close, now) {
		return
	}
	bids, _ := o.issueBids(ctx, tenant, iss.ID)
	var pending, unsent []IPOApp
	for _, b := range bids {
		switch {
		case b.Status == BidDraft || b.Pending == "New":
			unsent = append(unsent, b)
		case b.Mode == ModeUPI && (b.Status == BidUploaded || b.Status == BidMandatePending):
			pending = append(pending, b)
		}
	}
	line := func(b IPOApp) string {
		return fmt.Sprintf("%s · %s %s · ₹%s · %s (%s)", b.ID, b.UCC, b.Name, formatINR(b.Amount.Dec()), b.UPI, b.Status)
	}
	var lines []string
	for _, b := range pending {
		lines = append(lines, line(b))
	}
	switch kind {
	case "close":
		for _, b := range pending {
			o.notify(ctx, tenant, SystemActor, "ipo.mandate_pending", b.UCC, map[string]string{"issue": iss.Name, "bid": b.ID, "amount": formatINR(b.Amount.Dec()), "upi": b.UPI, "cutoff": iss.MandateCutoff})
		}
		if len(pending)+len(unsent) > 0 {
			d := fmt.Sprintf("Bidding closed at %s. These clients have not approved the UPI mandate; it can be approved until %s, after which the bid is not considered. Clients were reminded by SMS and WhatsApp.\n%s", iss.CloseTime, iss.MandateCutoff, strings.Join(lines, "\n"))
			if len(unsent) > 0 {
				d += fmt.Sprintf("\n%d bid(s) were never sent to the exchange and missed the close.", len(unsent))
			}
			o.raiseWork(ctx, tenant, "ipo", "mandates-"+iss.ID, fmt.Sprintf("%s: %d UPI mandate(s) not approved; acceptance closes at %s", iss.Name, len(pending), iss.MandateCutoff), d, "", RoleMaker, "ipo", iss.Close)
		}
	case "cutoff":
		for _, b := range append(append([]IPOApp{}, pending...), unsent...) {
			unsentBid := b.Status == BidDraft || b.Pending == "New"
			_, _ = docs.Update(ctx, o.Docs, tenant, cIPO, b.ID, SystemActor, func(x *IPOApp) error {
				if unsentBid {
					x.Status, x.Pending, x.Allotment, x.Mandate = BidRejected, "", "Not sent before the close", "Not sent"
					x.event(now, SystemActor, "not sent to the exchange before the close")
					return nil
				}
				x.Status, x.Mandate, x.MandateWhy, x.MandateAt = BidFailed, "Failed", "not approved by the "+iss.MandateCutoff+" cut-off on the closing day; the bid is not considered", now.Format(time.RFC3339)
				x.event(now, SystemActor, "UPI mandate lapsed at the %s cut-off", iss.MandateCutoff)
				return nil
			})
			if !unsentBid {
				o.notify(ctx, tenant, SystemActor, "ipo.mandate_lapsed", b.UCC, map[string]string{"issue": iss.Name, "bid": b.ID, "amount": formatINR(b.Amount.Dec())})
			}
		}
		o.closeWork(ctx, tenant, SystemActor, "ipo", "mandates-"+iss.ID, "mandate cut-off passed")
		if len(pending)+len(unsent) > 0 {
			d := fmt.Sprintf("UPI mandates not approved by %s lapsed; these bids are not considered for allotment. Answer clients who ask, and record any complaint against the bid.\n%s", iss.MandateCutoff, strings.Join(lines, "\n"))
			if len(unsent) > 0 {
				d += fmt.Sprintf("\n%d bid(s) never reached the exchange.", len(unsent))
			}
			o.raiseWork(ctx, tenant, "ipo", "lapsed-"+iss.ID, fmt.Sprintf("%s: %d UPI mandate(s) lapsed at the cut-off", iss.Name, len(pending)), d, "", RoleMaker, "ipo", iss.Close)
		}
	}
	o.log(ctx, tenant, SystemActor, "ipo.mandate_alert", iss.ID, map[string]any{"step": kind, "pending": len(pending), "unsent": len(unsent)})
}

// ipoFollowUp confirms demat credits and raises late credits and late
// releases, once a day from the credit day.
func (o *Ops) ipoFollowUp(ctx context.Context, tenant string, iss IPOIssue, now time.Time) {
	today := now.Format("2006-01-02")
	if !o.ipoOnce(ctx, tenant, iss.ID, "follow:"+today, now) {
		return
	}
	_, _ = o.SyncIPOCredits(ctx, tenant, SystemActor, iss.ID)
	rec, err := o.IPOReconciliation(ctx, tenant, SystemActor, iss.ID)
	if err != nil {
		return
	}
	var lines []string
	credit, release := 0, 0
	for _, r := range rec.Rows {
		switch {
		case r.Credit == "Late" || r.Credit == "Short":
			credit++
			lines = append(lines, fmt.Sprintf("%s · %s · %d of %d shares credited (due %s)", r.Bid, r.UCC, r.Credited, r.Shares, r.CreditBy))
		}
		if r.Unblock == "Late" {
			release++
			lines = append(lines, fmt.Sprintf("%s · %s · ₹%s not released (due %s); compensation so far ₹%s", r.Bid, r.UCC, formatINR(r.ToRelease.Dec()), r.UnblockBy, formatINR(r.Compensation.Dec())))
		}
	}
	if credit+release == 0 {
		o.closeWork(ctx, tenant, SystemActor, "ipo", "followup-"+iss.ID, "all credited and released")
		return
	}
	o.raiseWork(ctx, tenant, "ipo", "followup-"+iss.ID, fmt.Sprintf("%s: %d demat credit(s) and %d release(s) of blocked money late", iss.Name, credit, release),
		"Take these up with the registrar ("+firstOf(iss.Registrar, "see the issue")+") and the clients' banks. A release later than "+iss.Timeline.Unblock+" earns the client compensation (₹100 a day or 15% a year, whichever is higher).\n"+strings.Join(lines, "\n"), "", RoleMaker, "ipo", today)
}

// IPOMandateIssue is one issue on the mandate tracker.
type IPOMandateIssue struct {
	Issue    IPOIssue          `json:"issue"`
	Cutoff   string            `json:"cutoff"` // date and time mandates are accepted to
	Pending  []IPOApp          `json:"pending"`
	Failed   []IPOApp          `json:"failed"` // failed or declined: may be sent again once
	Accepted int               `json:"accepted"`
	Unsent   int               `json:"unsent"`
	Mismatch int               `json:"dp_mismatch"`
	Alerts   map[string]string `json:"alerts"`
	Resend   bool              `json:"resend_open"` // a failed mandate can be sent again now
}

// IPOMandates is the mandate tracker: issues bidding now or just closed.
func (o *Ops) IPOMandates(ctx context.Context, tenant, actor string) ([]IPOMandateIssue, error) {
	is, err := o.IPOIssues(ctx, tenant)
	if err != nil {
		return nil, err
	}
	set, _ := o.Settings(ctx, tenant)
	now := o.now()
	today, hm := now.Format("2006-01-02"), now.Format("15:04")
	f := o.ClientScope(ctx, tenant, actor)
	out := []IPOMandateIssue{}
	for _, iss := range is {
		if iss.Type == IssueOFS || iss.Withdrawn || today < iss.Open || today > set.NextBusinessDay(iss.Close) {
			continue
		}
		m := IPOMandateIssue{Issue: iss, Cutoff: iss.Close + " " + iss.MandateCutoff, Pending: []IPOApp{}, Failed: []IPOApp{}, Alerts: o.ipoDone(ctx, tenant, iss.ID)}
		m.Resend = !iss.liveAt(set, now) && today >= iss.Open && (today < iss.Close || today == iss.Close && hm < iss.OpenTime)
		bids, _ := o.issueBids(ctx, tenant, iss.ID)
		for _, b := range bids {
			if f != nil && !f(b.UCC) {
				continue
			}
			switch {
			case b.Status == BidDraft || b.Pending == "New":
				m.Unsent++
			case b.Mode == ModeUPI && (b.Status == BidUploaded || b.Status == BidMandatePending):
				m.Pending = append(m.Pending, b)
			case b.Status == BidFailed || b.Status == BidDeclined:
				m.Failed = append(m.Failed, b)
			case b.Mandate == "Accepted":
				m.Accepted++
			}
			if b.DPCheck == "Mismatch" && b.live() {
				m.Mismatch++
			}
		}
		sort.SliceStable(m.Pending, func(i, j int) bool { return m.Pending[i].Amount > m.Pending[j].Amount })
		out = append(out, m)
	}
	return out, nil
}
