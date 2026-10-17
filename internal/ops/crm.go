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

// ---- leads, prospects and appointments ----
//
// Leads are owned by a staff member, branch or authorised person, move
// through stages to conversion, and carry calls, notes and appointments.
// Mobile numbers are masked for everyone but checkers and admins; calls go
// through the telephony bridge so the caller never sees the number. Staff
// earn an incentive for each converted lead that is activated as a client.

// LeadStages in order.
var LeadStages = []string{"New", "Contacted", "Interested", "Documents pending", "Converted", "Lost"}

// LeadSources are where leads come from.
var LeadSources = []string{"Website", "Referral", "Walk-in", "Campaign", "Authorised person", "Social media", "Other"}

// LeadNote is a call, note or meeting on a lead.
type LeadNote struct {
	At      string `json:"at"`
	By      string `json:"by"`
	Kind    string `json:"kind"` // Note, Call, Meeting, Stage
	Text    string `json:"text"`
	Outcome string `json:"outcome,omitempty"`
}

// Appointment is a meeting with a prospect or client.
type Appointment struct {
	ID      string `json:"id"`
	At      string `json:"at"` // YYYY-MM-DDTHH:MM
	With    string `json:"with"`
	Purpose string `json:"purpose"`
	Place   string `json:"place,omitempty"`
	Status  string `json:"status"` // Scheduled, Done, No show, Cancelled
}

// Lead is a prospective client.
type Lead struct {
	ID         string        `json:"id"`
	Name       string        `json:"name"`
	Mobile     string        `json:"mobile"`
	Email      string        `json:"email,omitempty"`
	City       string        `json:"city,omitempty"`
	Source     string        `json:"source"`
	Interest   []string      `json:"interest"`
	Owner      string        `json:"owner"` // user ID
	Branch     string        `json:"branch,omitempty"`
	Partner    string        `json:"partner,omitempty"`
	ReferredBy string        `json:"referred_by,omitempty"` // UCC of a referring client
	Stage      string        `json:"stage"`
	Next       string        `json:"next_action,omitempty"` // date of the next follow-up
	Notes      []LeadNote    `json:"notes"`
	Appts      []Appointment `json:"appointments"`
	UCC        string        `json:"ucc,omitempty"`
	Created    string        `json:"created"`
	Converted  string        `json:"converted_on,omitempty"`
	LostReason string        `json:"lost_reason,omitempty"`
	Masked     bool          `json:"masked,omitempty"`
}

func maskMobile(m string) string {
	if len(m) < 4 {
		return m
	}
	return strings.Repeat("•", len(m)-4) + m[len(m)-4:]
}

// leadAccess returns the caller's role and whether they may see a lead.
func (o *Ops) leadAccess(ctx context.Context, tenant, actor string) (User, func(Lead) bool, error) {
	u, err := o.user(ctx, tenant, actor)
	if err != nil {
		role, rerr := o.RoleOf(ctx, tenant, actor)
		if rerr != nil {
			return u, nil, rerr
		}
		u = User{ID: actor, Role: role}
	}
	switch u.Role {
	case RoleClient, RoleRisk:
		return u, nil, fmt.Errorf("%w: leads are not available to %s logins", ErrForbidden, u.Role)
	case RoleBranch:
		return u, func(l Lead) bool { return l.Branch == u.Branch || l.Owner == u.ID }, nil
	case RoleAP:
		return u, func(l Lead) bool { return l.Partner == u.Partner || l.Owner == u.ID }, nil
	}
	return u, func(Lead) bool { return true }, nil
}

func (l Lead) view(u User) Lead {
	if u.Role != RoleAdmin && u.Role != RoleChecker {
		l.Mobile, l.Masked = maskMobile(l.Mobile), true
	}
	if l.Notes == nil {
		l.Notes = []LeadNote{}
	}
	if l.Appts == nil {
		l.Appts = []Appointment{}
	}
	if l.Interest == nil {
		l.Interest = []string{}
	}
	return l
}

// SaveLead creates a lead, or updates its details.
func (o *Ops) SaveLead(ctx context.Context, tenant, actor string, l Lead) (Lead, error) {
	u, can, err := o.leadAccess(ctx, tenant, actor)
	if err != nil {
		return l, err
	}
	l.Name, l.Email, l.City = strings.TrimSpace(l.Name), strings.TrimSpace(l.Email), strings.TrimSpace(l.City)
	if l.Name == "" {
		return l, invalid("enter the prospect's name")
	}
	if l.Source == "" {
		l.Source = "Other"
	}
	if !contains(LeadSources, l.Source) {
		return l, invalid("source must be one of %s", strings.Join(LeadSources, ", "))
	}
	if l.ReferredBy != "" {
		if _, err := o.Client(ctx, tenant, l.ReferredBy); err != nil {
			return l, invalid("referring client %s not found", l.ReferredBy)
		}
	}
	now := o.now()
	if l.ID == "" {
		if !reMobile.MatchString(l.Mobile) {
			return l, invalid("enter a 10-digit Indian mobile number")
		}
		all, _ := docs.List[Lead](ctx, o.Docs, tenant, cLead, "")
		for _, x := range all {
			if x.Mobile == l.Mobile && x.Stage != "Lost" && x.Stage != "Converted" {
				return l, invalid("this mobile number is already an open lead (%s, owned by %s)", x.ID, x.Owner)
			}
		}
		n, err := o.next(ctx, tenant, "lead", actor)
		if err != nil {
			return l, err
		}
		l.ID, l.Stage, l.Created = fmt.Sprintf("LD-%05d", n), "New", now.Format("2006-01-02")
		if l.Owner == "" {
			l.Owner = actor
		}
		if u.Role == RoleBranch {
			l.Branch = u.Branch
		}
		if u.Role == RoleAP {
			l.Partner, l.Owner = u.Partner, actor
		}
		l.Notes = []LeadNote{{At: now.Format(time.RFC3339), By: actor, Kind: "Stage", Text: "Lead created from " + l.Source}}
		if _, err := docs.Put(ctx, o.Docs, tenant, cLead, l.ID, l, 0, actor); err != nil {
			return l, err
		}
		o.log(ctx, tenant, actor, "lead.created", l.ID, map[string]any{"source": l.Source, "owner": l.Owner})
		return l.view(u), nil
	}
	out, err := docs.Update(ctx, o.Docs, tenant, cLead, l.ID, actor, func(x *Lead) error {
		if !can(*x) {
			return ErrNotFound
		}
		x.Name, x.Email, x.City, x.Source, x.Interest, x.ReferredBy = l.Name, l.Email, l.City, l.Source, l.Interest, l.ReferredBy
		if l.Owner != "" && u.Role != RoleAP {
			x.Owner = l.Owner
		}
		if l.Branch != "" && u.Role != RoleBranch && u.Role != RoleAP {
			x.Branch = l.Branch
		}
		if l.Next != "" {
			x.Next = l.Next
		}
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "lead.updated", l.ID, nil)
	}
	return out.view(u), err
}

// LeadAction records activity on a lead: a note, a call (through the
// telephony bridge), an appointment, an appointment outcome, or a stage
// change.
type LeadAction struct {
	Kind    string `json:"kind"` // note, call, appointment, appointment_status, stage
	Text    string `json:"text"`
	Outcome string `json:"outcome"`
	At      string `json:"at"`
	With    string `json:"with"`
	Place   string `json:"place"`
	Appt    string `json:"appointment"`
	Status  string `json:"status"`
	Stage   string `json:"stage"`
	Next    string `json:"next_action"`
	UCC     string `json:"ucc"`
}

// ActOnLead applies a LeadAction.
func (o *Ops) ActOnLead(ctx context.Context, tenant, actor, id string, a LeadAction) (Lead, string, error) {
	u, can, err := o.leadAccess(ctx, tenant, actor)
	if err != nil {
		return Lead{}, "", err
	}
	now := o.now()
	ts := now.Format(time.RFC3339)
	msg := ""
	var ucc Client
	if a.Kind == "stage" && a.Stage == "Converted" {
		if a.UCC == "" {
			return Lead{}, "", invalid("enter the UCC the lead was opened as")
		}
		ucc, err = o.Client(ctx, tenant, a.UCC)
		if err != nil {
			return Lead{}, "", invalid("client %s not found", a.UCC)
		}
	}
	l, err := docs.Update(ctx, o.Docs, tenant, cLead, id, actor, func(l *Lead) error {
		if !can(*l) {
			return ErrNotFound
		}
		if a.Next != "" {
			l.Next = a.Next
		}
		switch a.Kind {
		case "note":
			if strings.TrimSpace(a.Text) == "" {
				return invalid("write the note")
			}
			l.Notes = append(l.Notes, LeadNote{At: ts, By: actor, Kind: "Note", Text: strings.TrimSpace(a.Text)})
		case "call":
			if a.Outcome == "" {
				a.Outcome = "Connected"
			}
			l.Notes = append(l.Notes, LeadNote{At: ts, By: actor, Kind: "Call", Text: strings.TrimSpace(a.Text), Outcome: a.Outcome})
			msg = "Call placed through the telephony bridge to " + maskMobile(l.Mobile) + "; your phone rings first."
			if l.Stage == "New" {
				l.Stage = "Contacted"
			}
		case "appointment":
			if _, err := time.Parse("2006-01-02T15:04", a.At); err != nil {
				return invalid("appointment time must be a date and time")
			}
			if a.With == "" {
				a.With = actor
			}
			ap := Appointment{ID: fmt.Sprintf("%s-A%d", l.ID, len(l.Appts)+1), At: a.At, With: a.With, Purpose: strings.TrimSpace(a.Text), Place: a.Place, Status: "Scheduled"}
			l.Appts = append(l.Appts, ap)
			l.Notes = append(l.Notes, LeadNote{At: ts, By: actor, Kind: "Meeting", Text: "Appointment on " + strings.Replace(a.At, "T", " ", 1) + " with " + a.With + ": " + ap.Purpose})
		case "appointment_status":
			ok := false
			for i := range l.Appts {
				if l.Appts[i].ID == a.Appt {
					if !contains([]string{"Done", "No show", "Cancelled", "Scheduled"}, a.Status) {
						return invalid("status must be Done, No show, Cancelled or Scheduled")
					}
					l.Appts[i].Status, ok = a.Status, true
				}
			}
			if !ok {
				return invalid("appointment %s not found", a.Appt)
			}
			l.Notes = append(l.Notes, LeadNote{At: ts, By: actor, Kind: "Meeting", Text: a.Appt + ": " + a.Status + optional(a.Text)})
		case "stage":
			if !contains(LeadStages, a.Stage) {
				return invalid("stage must be one of %s", strings.Join(LeadStages, ", "))
			}
			if l.Stage == "Converted" {
				return fmt.Errorf("%w: %s is already converted to %s", ErrState, l.ID, l.UCC)
			}
			switch a.Stage {
			case "Lost":
				if strings.TrimSpace(a.Text) == "" {
					return invalid("give the reason the lead was lost")
				}
				l.LostReason = strings.TrimSpace(a.Text)
			case "Converted":
				l.UCC, l.Converted = ucc.UCC, now.Format("2006-01-02")
			}
			l.Notes = append(l.Notes, LeadNote{At: ts, By: actor, Kind: "Stage", Text: l.Stage + " → " + a.Stage + optional(a.Text)})
			l.Stage = a.Stage
		default:
			return invalid("unknown action %q", a.Kind)
		}
		return nil
	})
	if err != nil {
		return l, "", err
	}
	o.log(ctx, tenant, actor, "lead."+a.Kind, id, map[string]any{"stage": l.Stage, "outcome": a.Outcome})
	return l.view(u), msg, nil
}

func optional(s string) string {
	if s = strings.TrimSpace(s); s != "" {
		return " · " + s
	}
	return ""
}

// LeadIncentive is one owner's conversions and incentive for a period.
type LeadIncentive struct {
	Owner     string       `json:"owner"`
	Leads     int          `json:"leads"`
	Converted int          `json:"converted"`
	Activated int          `json:"activated"`
	Rate      string       `json:"conversion_pct"`
	Incentive money.Amount `json:"incentive"`
	Appts     int          `json:"appointments"`
	Calls     int          `json:"calls"`
}

// CRMView is the leads desk.
type CRMView struct {
	Leads      []Lead          `json:"leads"`
	Stages     map[string]int  `json:"stages"`
	Today      []Appointment   `json:"today"`
	Due        int             `json:"follow_ups_due"`
	Incentives []LeadIncentive `json:"incentives"`
	PerClient  money.Amount    `json:"incentive_per_client"`
	Sources    []string        `json:"sources"`
	StageList  []string        `json:"stage_list"`
	From, To   string
}

// CRM lists the caller's leads with stage counts, today's appointments and
// incentives for leads created between from and to.
func (o *Ops) CRM(ctx context.Context, tenant, actor, date, from, to string) (CRMView, error) {
	v := CRMView{Leads: []Lead{}, Stages: map[string]int{}, Today: []Appointment{}, Incentives: []LeadIncentive{}, Sources: LeadSources, StageList: LeadStages, From: from, To: to}
	u, can, err := o.leadAccess(ctx, tenant, actor)
	if err != nil {
		return v, err
	}
	all, err := docs.List[Lead](ctx, o.Docs, tenant, cLead, "")
	if err != nil {
		return v, err
	}
	set, _ := o.Settings(ctx, tenant)
	v.PerClient = money.Amount(50000)
	if set.LeadIncentive != "" {
		if a, err := money.ParseAmount(set.LeadIncentive); err == nil {
			v.PerClient = a
		}
	}
	cs, _ := o.ClientMap(ctx, tenant)
	inc := map[string]*LeadIncentive{}
	for _, l := range all {
		if !can(l) {
			continue
		}
		v.Leads = append(v.Leads, l.view(u))
		v.Stages[l.Stage]++
		if l.Next != "" && l.Next <= date && l.Stage != "Converted" && l.Stage != "Lost" {
			v.Due++
		}
		for _, a := range l.Appts {
			if strings.HasPrefix(a.At, date) && a.Status == "Scheduled" {
				a.Purpose = l.Name + ": " + a.Purpose
				v.Today = append(v.Today, a)
			}
		}
		if l.Created < from || l.Created > to {
			continue
		}
		r := inc[l.Owner]
		if r == nil {
			r = &LeadIncentive{Owner: l.Owner}
			inc[l.Owner] = r
		}
		r.Leads++
		for _, n := range l.Notes {
			if n.Kind == "Call" {
				r.Calls++
			}
		}
		r.Appts += len(l.Appts)
		if l.Stage == "Converted" {
			r.Converted++
			if c, ok := cs[l.UCC]; ok && c.Status == "Active" {
				r.Activated++
			}
		}
	}
	for _, k := range sortedKeys(inc) {
		r := inc[k]
		if r.Leads > 0 {
			t := (r.Converted*1000 + r.Leads/2) / r.Leads
			r.Rate = fmt.Sprintf("%d.%d", t/10, t%10)
		}
		r.Incentive = v.PerClient * money.Amount(r.Activated)
		v.Incentives = append(v.Incentives, *r)
	}
	sort.Slice(v.Leads, func(i, j int) bool { return v.Leads[i].ID > v.Leads[j].ID })
	sort.Slice(v.Today, func(i, j int) bool { return v.Today[i].At < v.Today[j].At })
	return v, nil
}
