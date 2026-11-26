package ops

import (
	"context"
	"fmt"
	"strings"

	"ledgerline/internal/docs"
)

// ---- notices to clients: complaints, nominations ----
//
// Complaints (NSE/ISC/73908, master circular on investor grievance
// redressal, Part A 7.2(c)-(f); MCX's equivalent): every complaint is
// acknowledged with its ticket number and the escalation matrix, every
// reply says SCORES and the SMART ODR portal are there if the client is
// not satisfied, and a complaint still unresolved when its 21 days run out
// gets a notice of the ODR route (SEBI master circular for stock brokers,
// Annex 37, the investor charter).
//
// Nominations (SEBI/HO/OIAE/OIAE_IAD-3/P/CIR/2026/12676 para 9; CDSL
// operating instructions 3.4.2; BSE inspection master circular item 1.10):
// every nomination action is acknowledged to the client, and every
// nomination recorded gets a serial number in the register of nominations
// (CDSL 3.4.2.1-3.4.2.3). SEBI's master circular for stock brokers paras
// 22.6-22.7: clients who have given no nomination choice are reminded
// every fortnight by email and SMS (Periodic work, nomination_reminders).

// EscalationContact is one level of the grievance escalation matrix.
type EscalationContact struct {
	Level string `json:"level"` // Customer care, Head of customer care, Compliance officer, CEO
	Name  string `json:"name"`
	Email string `json:"email"`
	Phone string `json:"phone,omitempty"`
	Hours string `json:"hours,omitempty"`
}

// EscalationLevels are the matrix's four levels, in order.
var EscalationLevels = []string{"Customer care", "Head of customer care", "Compliance officer", "CEO"}

// escalationText is the matrix in words, for messages. Levels the firm
// has not filled fall back to the compliance officer's email.
func escalationText(set Settings) string {
	var ls []string
	for i, x := range set.Escalation {
		lv := firstOf(x.Level, EscalationLevels[min(i, len(EscalationLevels)-1)])
		ls = append(ls, fmt.Sprintf("%d. %s: %s", i+1, lv, strings.Join(nonEmpty(x.Name, x.Email, x.Phone, x.Hours), ", ")))
	}
	if len(ls) == 0 {
		co := strings.Join(nonEmpty(set.ComplianceOfficer, set.ComplianceEmail, set.CompliancePhone), ", ")
		ls = append(ls, "Compliance officer: "+firstOf(co, "see our website"))
	}
	return strings.Join(ls, "\n")
}

// odrLine is what every complaint reply says about SCORES and ODR.
const odrLine = "If you are not satisfied with our response, you can lodge your complaint on SEBI's SCORES (scores.sebi.gov.in) or start online dispute resolution on the SMART ODR portal (smartodr.in)."

const (
	evTicketODR    = "ticket.odr"
	evNominationAk = "nomination.acknowledged"
	evNominationRm = "nomination.reminder"
	cNomRegister   = "nomregister"
)

func init() {
	EventVars["ticket.opened"] = []string{"id", "subject", "due", "escalation", "odr"}
	EventVars["ticket.reply"] = []string{"id", "subject", "status", "odr"}
	EventVars[evTicketODR] = []string{"id", "subject", "opened", "odr"}
	EventVars[evNominationAk] = []string{"what", "ref", "serial", "date"}
	EventVars[evNominationRm] = []string{"how"}
	t := func(ev, ch, cat, subj, body, dlt string) Template {
		return Template{Event: ev, Channel: ch, Category: cat, Subject: subj, Body: body, DLT: dlt, Active: true}
	}
	extraTemplates = append(extraTemplates,
		t(evTicketODR, ChEmail, "Service", "Your complaint {id}: SMART ODR", "Dear {name},\n\nYour complaint {id} ({subject}), received on {opened}, is not yet resolved within the 21 days the rules allow. We are sorry for the delay and are still working on it.\n\n{odr}\n\n{firm}", ""),
		t(evTicketODR, ChSMS, "Service", "", "Dear {name}, complaint {id} is not resolved in 21 days. You may approach SEBI SCORES or the SMART ODR portal smartodr.in. -{firm}", "1107166000000000081"),
		t(evNominationAk, ChEmail, "Service", "Nomination: {what}", "Dear {name},\n\nWe acknowledge your nomination request for account {ucc}: {what} (reference {ref}, {date}).{serial}\n\nIf you did not make this request, tell us at once.\n\n{firm}", ""),
		t(evNominationAk, ChSMS, "Service", "", "Dear {name}, your nomination request {ref} for {ucc}: {what}. Not you? Call us. -{firm}", "1107166000000000082"),
		t(evNominationRm, ChEmail, "Service", "Please give your nomination choice for account {ucc}", "Dear {name},\n\nOur records show no nomination choice for your account {ucc}. You can name up to three nominees, or opt out by the declaration.\n\n{how}\n\n{firm}", ""),
		t(evNominationRm, ChSMS, "Service", "", "Dear {name}, no nomination choice is recorded for {ucc}. Nominate or opt out in our client portal (e-Sign or OTP) or on the signed form. -{firm}", "1107166000000000083"),
	)
}

// ---- complaints ----

// odrNotices tells each client whose complaint is still open past its 21
// days about SCORES and the SMART ODR portal, once per complaint.
func (o *Ops) odrNotices(ctx context.Context, tenant, date string) int {
	ts, err := docs.List[Ticket](ctx, o.Docs, tenant, cTicket, "")
	if err != nil {
		return 0
	}
	n := 0
	for _, t := range ts {
		if t.Kind != KindComplaint || t.UCC == "" || t.ODRNoticeOn != "" || t.Due == "" || t.Due >= date {
			continue
		}
		if t.Status == "Resolved" || t.Status == "Closed" {
			continue
		}
		o.notify(ctx, tenant, SystemActor, evTicketODR, t.UCC, map[string]string{"id": t.ID, "subject": t.Subject, "opened": fmtDay(t.OpenedOn), "odr": odrLine})
		_, _ = docs.Update(ctx, o.Docs, tenant, cTicket, t.ID, SystemActor, func(x *Ticket) error {
			x.ODRNoticeOn = date
			return nil
		})
		n++
	}
	return n
}

// ---- nominations ----

// NominationEntry is one line of the register of nominations.
type NominationEntry struct {
	Serial     int64  `json:"serial"`
	No         string `json:"no"` // NOM-000001
	UCC        string `json:"ucc"`
	Name       string `json:"name"`
	Action     string `json:"action"` // Nomination, Change, Opt-out
	Nominees   string `json:"nominees,omitempty"`
	Mode       string `json:"mode,omitempty"`
	Ref        string `json:"ref,omitempty"` // the change request or source
	RecordedOn string `json:"recorded_on"`
	By         string `json:"by"`
}

// registerNomination gives the client's nomination as it now stands its
// serial number in the register, and acknowledges it to the client.
func (o *Ops) registerNomination(ctx context.Context, tenant, actor string, c Client, prev, ref string) (NominationEntry, error) {
	seq, err := o.next(ctx, tenant, "nomination", actor)
	if err != nil {
		return NominationEntry{}, err
	}
	var names []string
	for _, x := range c.Nominees {
		names = append(names, fmt.Sprintf("%s (%s)", x.Name, x.Relation))
	}
	act := "Nomination"
	switch {
	case c.Nominee == "Opted out":
		act = "Opt-out"
	case prev == "Opted in":
		act = "Change of nomination"
	}
	e := NominationEntry{Serial: seq, No: fmt.Sprintf("NOM-%06d", seq), UCC: c.UCC, Name: c.Name, Action: act, Nominees: strings.Join(names, "; "), Mode: c.NominationMode, Ref: ref,
		RecordedOn: o.now().Format("2006-01-02"), By: actor}
	if err := docs.Upsert(ctx, o.Docs, tenant, cNomRegister, e.No, e, actor); err != nil {
		return e, err
	}
	o.log(ctx, tenant, actor, "nomination.registered", e.No, map[string]any{"ucc": c.UCC, "action": act, "ref": ref})
	o.nominationAck(ctx, tenant, actor, c.UCC, strings.ToLower(act)+" recorded", ref, e.No)
	return e, nil
}

// nominationAck acknowledges a nomination action to the client.
func (o *Ops) nominationAck(ctx context.Context, tenant, actor, ucc, what, ref, serial string) {
	s := ""
	if serial != "" {
		s = " It is entered in our register of nominations as " + serial + "."
	}
	o.notify(ctx, tenant, actor, evNominationAk, ucc, map[string]string{"what": what, "ref": ref, "serial": s, "date": fmtDay(o.now().Format("2006-01-02"))})
}

// NominationRegister lists the register, oldest first.
func (o *Ops) NominationRegister(ctx context.Context, tenant string) ([]NominationEntry, error) {
	es, err := docs.List[NominationEntry](ctx, o.Docs, tenant, cNomRegister, "")
	for i := 1; i < len(es); i++ {
		for j := i; j > 0 && es[j].Serial < es[j-1].Serial; j-- {
			es[j], es[j-1] = es[j-1], es[j]
		}
	}
	return es, err
}

// nominationReminderTargets are the active clients with no nomination
// choice: neither nominated nor opted out by the declaration.
func (o *Ops) nominationReminderTargets(ctx context.Context, tenant string) []Client {
	cs, err := o.Clients(ctx, tenant)
	if err != nil {
		return nil
	}
	var out []Client
	for _, c := range cs {
		if c.Status == "Active" && c.NominationState() == "No declaration" {
			out = append(out, c)
		}
	}
	return out
}

// sendNominationReminders sends the fortnight's reminder to each client
// without a nomination choice.
func (o *Ops) sendNominationReminders(ctx context.Context, tenant, actor, period string) (int, error) {
	how := "Sign in to our client portal and choose Profile → Nomination: nominate or opt out, confirmed with e-Sign or an OTP to your registered mobile and email. Or ask us for the nomination form or the opt-out declaration and send it signed."
	n := 0
	for _, c := range o.nominationReminderTargets(ctx, tenant) {
		if _, err := o.sendEvent(ctx, tenant, actor, evNominationRm, c.UCC, map[string]string{"how": how}, ""); err != nil {
			return n, err
		}
		n++
	}
	o.log(ctx, tenant, actor, "nomination.reminders", period, map[string]any{"clients": n})
	return n, nil
}

// nominationRegisterRows is the register as a report.
func (o *Ops) nominationRegisterRows(ctx context.Context, tenant string) [][]string {
	rows := [][]string{{"SerialNo", "RecordedOn", "UCC", "Name", "Action", "Nominees", "Mode", "Reference", "RecordedBy"}}
	es, _ := o.NominationRegister(ctx, tenant)
	for _, e := range es {
		rows = append(rows, []string{e.No, e.RecordedOn, e.UCC, e.Name, e.Action, e.Nominees, e.Mode, e.Ref, e.By})
	}
	return rows
}
