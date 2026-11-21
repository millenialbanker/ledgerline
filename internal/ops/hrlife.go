package ops

import (
	"context"
	"fmt"
	"strings"
	"time"

	"ledgerline/internal/docs"
)

// hrTick is the HRMS part of the scheduler's tick (every edition: these
// are reminders and the leaver's login, not automation of the firm's
// work): leavers' logins on their last day, certificate and document
// reminders, the annual holdings declaration, attendance housekeeping and
// salary batches with the bank.
func (o *Ops) hrTick(ctx context.Context, tenant string, now time.Time) {
	if !o.ModuleOn(ctx, tenant, ModHRMS) {
		return
	}
	o.leaverTick(ctx, tenant, now)
	o.hrCertTick(ctx, tenant, now)
	o.annualDeclTick(ctx, tenant, now)
	o.attendanceTick(ctx, tenant, now)
	if o.ModuleOn(ctx, tenant, ModPayroll) {
		o.salaryBankTick(ctx, tenant, now)
	}
}

// ---- joiners, movers and leavers, tied to access ----
//
// Joining raises a checklist (a Ledgerline login, the trading terminal and
// the exchange approved-user registration for a dealer, the certificates
// the duties need, a background check, the signed code of conduct, the
// own-trading declarations); each item is a work item for the role that
// does it. A change of role, duties, office or manager raises an access
// review. Leaving needs a second person's approval; on the last day (at
// the policy's time, 18:30 by default) the person's Ledgerline login is
// switched off by itself, and work items go out to withdraw terminals,
// exchange registrations, devices, bank and CBX roles and email, with an
// exit checklist.

const (
	cHRList   = "hr_checklist"
	cHRLeaver = "hr_leaver"
)

// HRTask is one thing to do on a checklist.
type HRTask struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Role  string `json:"role"`
	Done  bool   `json:"done"`
	NA    bool   `json:"na,omitempty"` // not applicable
	By    string `json:"by,omitempty"`
	At    string `json:"at,omitempty"`
	Note  string `json:"note,omitempty"`
}

// HRChecklist is a joining, move or leaving checklist.
type HRChecklist struct {
	ID       string   `json:"id"` // code|kind|date
	Employee string   `json:"employee"`
	Name     string   `json:"name"`
	Kind     string   `json:"kind"` // join, move, leave
	Date     string   `json:"date"`
	Detail   string   `json:"detail,omitempty"`
	Items    []HRTask `json:"items"`
	Status   string   `json:"status"` // Open, Done
	Created  string   `json:"created"`
}

func (c *HRChecklist) settle() {
	c.Status = "Done"
	for _, it := range c.Items {
		if !it.Done && !it.NA {
			c.Status = "Open"
		}
	}
}

func listKey(code, kind, date string) string { return code + "|" + kind + "|" + date }

func (o *Ops) saveList(ctx context.Context, tenant, actor string, c HRChecklist) {
	c.settle()
	_ = docs.Upsert(ctx, o.Docs, tenant, cHRList, c.ID, c, actor)
	for _, it := range c.Items {
		wk := c.Employee + ":" + c.Kind + ":" + c.Date + ":" + it.Key
		if it.Done || it.NA {
			o.closeWork(ctx, tenant, actor, wHR, wk, firstOf(it.Note, "done"))
			continue
		}
		o.raiseWork(ctx, tenant, wHR, wk, fmt.Sprintf("%s (%s): %s", c.Name, c.Employee, it.Label),
			map[string]string{"join": "Joining checklist", "move": "Access review after a change of role or office", "leave": "Leaving checklist"}[c.Kind]+". "+c.Detail, "", it.Role, "people", c.Date)
	}
}

// startJoining raises the joining checklist for a new employee.
func (o *Ops) startJoining(ctx context.Context, tenant, actor string, e Employee) {
	p := o.hrPolicy(ctx, tenant)
	c := HRChecklist{ID: listKey(e.Code, "join", e.JoinedOn), Employee: e.Code, Name: e.Name, Kind: "join", Date: e.JoinedOn, Created: o.now().Format(time.RFC3339),
		Detail: fmt.Sprintf("%s joins as %s on %s.", e.Name, firstOf(e.Designation, "staff"), fmtDay(e.JoinedOn))}
	add := func(key, label, role string, done bool, note string) {
		c.Items = append(c.Items, HRTask{Key: key, Label: label, Role: role, Done: done, Note: note})
	}
	add("ll-user", "Create the Ledgerline login and link it to the employee", RoleAdmin, e.User != "", map[bool]string{true: "linked to " + e.User}[e.User != ""])
	if e.dealer() {
		add("terminal", "Request the trading terminal / CTCL user ID", RoleCompliance, false, "")
		add("exch-reg", "Register as an approved user with the exchange(s) and add to the dealers register", RoleCompliance, e.Dealer != "", "")
	}
	needs := certNeeds(e, p, e.JoinedOn)
	if len(needs) > 0 {
		var names []string
		ok := true
		for _, n := range needs {
			names = append(names, n.Cert)
			ok = ok && (n.State == "Valid" || n.State == "Expiring")
		}
		add("certs", "Certificates on file: "+strings.Join(names, ", "), RoleCompliance, ok, "")
	}
	add("bgcheck", "Background check", RoleHR, false, "")
	add("coc", "Code of conduct signed (upload it to the employee's documents)", RoleHR, false, "")
	add("trading", "Own trading accounts declared and holdings declared on joining", RoleCompliance, false, "")
	o.saveList(ctx, tenant, actor, c)
	o.log(ctx, tenant, actor, "hr.joining.started", e.Code, map[string]any{"items": len(c.Items)})
}

// maybeAccessReview raises an access review when a person's role, duties,
// office or manager changes: their logins, terminals and bank roles may no
// longer fit.
func (o *Ops) maybeAccessReview(ctx context.Context, tenant, actor string, old, e Employee) {
	var what []string
	if old.Designation != e.Designation {
		what = append(what, fmt.Sprintf("designation %s → %s", firstOf(old.Designation, "—"), firstOf(e.Designation, "—")))
	}
	if old.Department != e.Department {
		what = append(what, fmt.Sprintf("department %s → %s", firstOf(old.Department, "—"), firstOf(e.Department, "—")))
	}
	if old.Branch != e.Branch {
		what = append(what, fmt.Sprintf("office %s → %s", firstOf(old.Branch, "head office"), firstOf(e.Branch, "head office")))
	}
	if strings.Join(old.Duties, ",") != strings.Join(e.Duties, ",") {
		what = append(what, fmt.Sprintf("duties %s → %s", firstOf(strings.Join(old.Duties, ", "), "—"), firstOf(strings.Join(e.Duties, ", "), "—")))
	}
	if old.Manager != e.Manager {
		what = append(what, fmt.Sprintf("manager %s → %s", firstOf(old.Manager, "—"), firstOf(e.Manager, "—")))
	}
	if len(what) == 0 || e.Status == EmpLeft {
		return
	}
	today := o.today()
	c := HRChecklist{ID: listKey(e.Code, "move", today), Employee: e.Code, Name: e.Name, Kind: "move", Date: today, Created: o.now().Format(time.RFC3339),
		Detail: "Changed: " + strings.Join(what, "; ") + "."}
	if x, _, err := docs.Get[HRChecklist](ctx, o.Docs, tenant, cHRList, c.ID); err == nil {
		c.Items, c.Detail = x.Items, x.Detail+" "+c.Detail // a second change the same day joins the first review
	} else {
		c.Items = []HRTask{{Key: "ll-role", Label: "Review the Ledgerline login: role and office", Role: RoleAdmin}}
		if old.dealer() || e.dealer() {
			c.Items = append(c.Items, HRTask{Key: "terminals", Label: "Review trading terminals, exchange registrations and the dealers register", Role: RoleCompliance})
		}
		c.Items = append(c.Items, HRTask{Key: "bank-roles", Label: "Review bank and CBX roles (maker, verifier, authoriser)", Role: RoleAdmin},
			HRTask{Key: "certs", Label: "Certificates the new duties need", Role: RoleCompliance, Done: certsOK(e, o.hrPolicy(ctx, tenant), today), NA: len(certNeeds(e, o.hrPolicy(ctx, tenant), today)) == 0})
	}
	o.saveList(ctx, tenant, actor, c)
	o.log(ctx, tenant, actor, "hr.access_review.raised", e.Code, map[string]any{"changes": what})
}

func certsOK(e Employee, p HRPolicy, day string) bool {
	for _, n := range certNeeds(e, p, day) {
		if n.State != "Valid" && n.State != "Expiring" {
			return false
		}
	}
	return true
}

// tickItem marks a checklist item done on the person's open checklist of
// that kind (when there is one).
func (o *Ops) tickItem(ctx context.Context, tenant, actor, code, kind, key, note string) {
	cs, _ := docs.List[HRChecklist](ctx, o.Docs, tenant, cHRList, code+"|"+kind+"|")
	for _, c := range cs {
		if c.Status != "Open" {
			continue
		}
		for i := range c.Items {
			if c.Items[i].Key == key && !c.Items[i].Done {
				c.Items[i].Done, c.Items[i].By, c.Items[i].At, c.Items[i].Note = true, actor, o.now().Format(time.RFC3339), note
				o.saveList(ctx, tenant, actor, c)
				return
			}
		}
	}
}

// Checklists lists checklists (open first). HR, compliance and Admin see
// all; anyone sees the checklists with an item for their role.
func (o *Ops) Checklists(ctx context.Context, tenant, actor string, all bool) ([]HRChecklist, error) {
	if err := o.needModule(ctx, tenant, ModHRMS); err != nil {
		return nil, err
	}
	role, err := o.RoleOf(ctx, tenant, actor)
	if err != nil {
		return nil, err
	}
	cs, err := docs.List[HRChecklist](ctx, o.Docs, tenant, cHRList, "")
	out := []HRChecklist{}
	for _, c := range cs {
		if !all && c.Status != "Open" {
			continue
		}
		mine := role == RoleAdmin || role == RoleHR
		for _, it := range c.Items {
			mine = mine || it.Role == role
		}
		if mine {
			out = append(out, c)
		}
	}
	sortByDateDesc(out)
	return out, err
}

func sortByDateDesc(cs []HRChecklist) {
	for i := 1; i < len(cs); i++ {
		for j := i; j > 0 && (cs[j].Status == "Open" && cs[j-1].Status != "Open" || cs[j].Status == cs[j-1].Status && cs[j].Date > cs[j-1].Date); j-- {
			cs[j], cs[j-1] = cs[j-1], cs[j]
		}
	}
}

// DoCheckItem marks an item done or not applicable. The item's role, HR
// managers or Admin.
func (o *Ops) DoCheckItem(ctx context.Context, tenant, actor, id, key string, na bool, note string) (HRChecklist, error) {
	if err := o.needModule(ctx, tenant, ModHRMS); err != nil {
		return HRChecklist{}, err
	}
	c, _, err := docs.Get[HRChecklist](ctx, o.Docs, tenant, cHRList, id)
	if err != nil {
		return c, err
	}
	for i := range c.Items {
		it := &c.Items[i]
		if it.Key != key {
			continue
		}
		if it.Key == "deactivate" {
			return c, invalid("the login is switched off on the last day once a second person has approved the leaving (Approve leaving)")
		}
		if role, err := o.RoleOf(ctx, tenant, actor); err != nil {
			return c, err
		} else if role != it.Role {
			if err := o.require(ctx, tenant, actor, "Completing HR checklist items (or the item's own role)", RoleHR); err != nil {
				return c, err
			}
		}
		if it.Done || it.NA {
			return c, fmt.Errorf("%w: already done", ErrState)
		}
		if na && strings.TrimSpace(note) == "" {
			return c, invalid("say why the item does not apply")
		}
		it.Done, it.NA, it.By, it.At, it.Note = !na, na, actor, o.now().Format(time.RFC3339), trim(note, 300)
		o.saveList(ctx, tenant, actor, c)
		o.log(ctx, tenant, actor, "hr.checklist.item", c.ID, map[string]any{"item": key, "na": na})
		c, _, err = docs.Get[HRChecklist](ctx, o.Docs, tenant, cHRList, id)
		return c, err
	}
	return c, ErrNotFound
}

// ---- leaving ----

// Leaver is a person's leaving, approved by a second person.
type Leaver struct {
	Employee    string `json:"employee"`
	Name        string `json:"name"`
	LastDay     string `json:"last_day"`
	Reason      string `json:"reason"`
	RequestedBy string `json:"requested_by"`
	RequestedAt string `json:"requested_at"`
	ApprovedBy  string `json:"approved_by,omitempty"`
	ApprovedAt  string `json:"approved_at,omitempty"`
	User        string `json:"user,omitempty"`
	Deactivated string `json:"deactivated,omitempty"` // when the login was switched off
	Status      string `json:"status"`                // Pending approval, Approved, Done, Withdrawn
}

// StartLeaving records that a person is leaving on lastDay, raises the
// leaving checklist and asks a second person to approve. HR managers.
func (o *Ops) StartLeaving(ctx context.Context, tenant, actor, code, lastDay, reason string) (Leaver, error) {
	if err := o.needModule(ctx, tenant, ModHRMS); err != nil {
		return Leaver{}, err
	}
	if err := o.require(ctx, tenant, actor, "Recording a leaver", RoleHR); err != nil {
		return Leaver{}, err
	}
	if !validDay(lastDay) {
		return Leaver{}, invalid("give the last working day (YYYY-MM-DD)")
	}
	if strings.TrimSpace(reason) == "" {
		return Leaver{}, invalid("give the reason for leaving (resignation, retirement, termination...)")
	}
	e, err := o.employee(ctx, tenant, code)
	if err != nil {
		return Leaver{}, err
	}
	if e.Status == EmpLeft {
		return Leaver{}, fmt.Errorf("%w: %s has already left", ErrState, e.Code)
	}
	if lastDay < e.JoinedOn {
		return Leaver{}, invalid("the last day is before the date of joining")
	}
	l := Leaver{Employee: e.Code, Name: e.Name, LastDay: lastDay, Reason: trim(reason, 300), RequestedBy: actor, RequestedAt: o.now().Format(time.RFC3339), User: e.User, Status: "Pending approval"}
	if _, err := docs.Put(ctx, o.Docs, tenant, cHRLeaver, e.Code, l, 0, actor); err != nil {
		if old, _, err2 := docs.Get[Leaver](ctx, o.Docs, tenant, cHRLeaver, e.Code); err2 == nil && old.Status != "Withdrawn" {
			return old, fmt.Errorf("%w: %s's leaving is already recorded (%s)", ErrState, e.Code, old.Status)
		}
		if err := docs.Upsert(ctx, o.Docs, tenant, cHRLeaver, e.Code, l, actor); err != nil {
			return l, err
		}
	}
	e, _ = docs.Update(ctx, o.Docs, tenant, cEmployee, e.Code, actor, func(x *Employee) error { x.Status, x.LeftOn = EmpLeaving, lastDay; return nil })
	o.log(ctx, tenant, actor, "hr.leaving.requested", e.Code, map[string]any{"last_day": lastDay, "reason": l.Reason})
	o.raiseWork(ctx, tenant, wHR, e.Code+":leave-approve:"+lastDay, fmt.Sprintf("Approve %s (%s) leaving on %s", e.Name, e.Code, fmtDay(lastDay)),
		"A second person must approve the leaving before the login is switched off on the last day. Requested by "+actor+".", "", RoleHR, "people", o.today())
	// The leaving checklist.
	c := HRChecklist{ID: listKey(e.Code, "leave", lastDay), Employee: e.Code, Name: e.Name, Kind: "leave", Date: lastDay, Created: o.now().Format(time.RFC3339),
		Detail: fmt.Sprintf("%s leaves; last working day %s (%s).", e.Name, fmtDay(lastDay), l.Reason)}
	items := []HRTask{{Key: "deactivate", Label: "Ledgerline login switched off on the last day (automatic, after approval)", Role: RoleHR, NA: e.User == ""}}
	if e.dealer() || e.Dealer != "" {
		terms := "the person's terminals"
		if d, _, err := docs.Get[Dealer](ctx, o.Docs, tenant, cDealer, e.Dealer); err == nil {
			var ids []string
			for _, t := range d.openTerminals(lastDay) {
				ids = append(ids, t.Exchange+" "+t.TerminalID)
			}
			if len(ids) > 0 {
				terms = strings.Join(ids, ", ")
			}
		}
		items = append(items, HRTask{Key: "terminals", Label: "Withdraw trading terminals / CTCL user IDs: " + terms, Role: RoleCompliance},
			HRTask{Key: "exch-reg", Label: "Withdraw exchange approved-user registrations and mark the dealer as left", Role: RoleCompliance})
	}
	items = append(items,
		HRTask{Key: "devices", Label: "Collect devices (laptop, phone, USB tokens, access cards)", Role: RoleHR},
		HRTask{Key: "bank-roles", Label: "Remove bank and CBX roles (maker, verifier, authoriser) and signing authorities", Role: RoleAdmin},
		HRTask{Key: "email", Label: "Disable email and other accounts; forward mail to the manager", Role: RoleAdmin},
		HRTask{Key: "handover", Label: "Handover of work, files and passwords to the manager", Role: RoleHR},
		HRTask{Key: "holdings", Label: "Holdings declaration on leaving (own trading)", Role: RoleCompliance},
		HRTask{Key: "exit", Label: "Exit interview, clearance and relieving letter", Role: RoleHR})
	if o.ModuleOn(ctx, tenant, ModPayroll) {
		items = append(items, HRTask{Key: "fnf", Label: "Full and final settlement", Role: RolePayroll})
	}
	c.Items = items
	o.saveList(ctx, tenant, actor, c)
	return l, nil
}

// ApproveLeaving is the second person's approval. It cannot be the person
// who recorded the leaving. HR managers.
func (o *Ops) ApproveLeaving(ctx context.Context, tenant, actor, code string) (Leaver, error) {
	if err := o.needModule(ctx, tenant, ModHRMS); err != nil {
		return Leaver{}, err
	}
	if err := o.require(ctx, tenant, actor, "Approving a leaver", RoleHR); err != nil {
		return Leaver{}, err
	}
	code = strings.ToUpper(code)
	l, err := docs.Update(ctx, o.Docs, tenant, cHRLeaver, code, actor, func(l *Leaver) error {
		if l.Status != "Pending approval" {
			return fmt.Errorf("%w: the leaving is %s", ErrState, strings.ToLower(l.Status))
		}
		if l.RequestedBy == actor {
			return fmt.Errorf("%w: a second person must approve the leaving (you recorded it)", ErrForbidden)
		}
		if me, ok := o.employeeOf(ctx, tenant, actor); ok && me.Code == l.Employee {
			return fmt.Errorf("%w: you cannot approve your own leaving", ErrForbidden)
		}
		l.Status, l.ApprovedBy, l.ApprovedAt = "Approved", actor, o.now().Format(time.RFC3339)
		return nil
	})
	if err != nil {
		return l, err
	}
	o.closeWork(ctx, tenant, actor, wHR, l.Employee+":leave-approve:"+l.LastDay, "approved by "+actor)
	o.log(ctx, tenant, actor, "hr.leaving.approved", l.Employee, map[string]any{"last_day": l.LastDay, "requested_by": l.RequestedBy})
	// Approved after the moment has passed: switch off now.
	o.leaverTick(ctx, tenant, o.now())
	l, _, _ = docs.Get[Leaver](ctx, o.Docs, tenant, cHRLeaver, code)
	return l, nil
}

// WithdrawLeaving cancels a leaving not yet done (the person stays).
func (o *Ops) WithdrawLeaving(ctx context.Context, tenant, actor, code, note string) (Leaver, error) {
	if err := o.needModule(ctx, tenant, ModHRMS); err != nil {
		return Leaver{}, err
	}
	if err := o.require(ctx, tenant, actor, "Recording a leaver", RoleHR); err != nil {
		return Leaver{}, err
	}
	code = strings.ToUpper(code)
	l, err := docs.Update(ctx, o.Docs, tenant, cHRLeaver, code, actor, func(l *Leaver) error {
		if l.Status == "Done" || l.Status == "Withdrawn" {
			return fmt.Errorf("%w: the leaving is %s", ErrState, strings.ToLower(l.Status))
		}
		l.Status = "Withdrawn"
		return nil
	})
	if err != nil {
		return l, err
	}
	_, _ = docs.Update(ctx, o.Docs, tenant, cEmployee, code, actor, func(x *Employee) error { x.Status, x.LeftOn = EmpActive, ""; return nil })
	o.closeWork(ctx, tenant, actor, wHR, code+":leave-approve:"+l.LastDay, "withdrawn")
	if c, _, err := docs.Get[HRChecklist](ctx, o.Docs, tenant, cHRList, listKey(code, "leave", l.LastDay)); err == nil {
		for i := range c.Items {
			if !c.Items[i].Done {
				c.Items[i].NA, c.Items[i].Note = true, "leaving withdrawn"
			}
		}
		o.saveList(ctx, tenant, actor, c)
	}
	o.log(ctx, tenant, actor, "hr.leaving.withdrawn", code, map[string]any{"note": note})
	return l, nil
}

// Leavers lists leavings (HR, Admin).
func (o *Ops) Leavers(ctx context.Context, tenant, actor string) ([]Leaver, error) {
	if err := o.needModule(ctx, tenant, ModHRMS); err != nil {
		return nil, err
	}
	if err := o.require(ctx, tenant, actor, "Seeing leavers", RoleHR, RoleCompliance); err != nil {
		return nil, err
	}
	ls, err := docs.List[Leaver](ctx, o.Docs, tenant, cHRLeaver, "")
	if ls == nil {
		ls = []Leaver{}
	}
	return ls, err
}

// leaverTick switches off the logins of approved leavers whose last day
// has reached the policy's time, and marks them left; it reminds about
// leavings still waiting for approval on their last day.
func (o *Ops) leaverTick(ctx context.Context, tenant string, now time.Time) {
	ls, _ := docs.List[Leaver](ctx, o.Docs, tenant, cHRLeaver, "")
	if len(ls) == 0 {
		return
	}
	p := o.hrPolicy(ctx, tenant)
	now = now.In(ist)
	today, hm := now.Format("2006-01-02"), now.Format("15:04")
	for _, l := range ls {
		due := today > l.LastDay || (today == l.LastDay && hm >= p.DeactivateAt)
		if !due {
			continue
		}
		switch l.Status {
		case "Pending approval":
			o.raiseWork(ctx, tenant, wHR, l.Employee+":leave-unapproved:"+l.LastDay, fmt.Sprintf("%s (%s) has reached the last day but the leaving is not approved", l.Name, l.Employee),
				"The login stays on until a second person approves the leaving. Approve it now, or withdraw it if the person is staying.", "", RoleHR, "people", today)
		case "Approved":
			by := firstOf(l.ApprovedBy, SystemActor)
			if l.User != "" {
				if u, err := o.user(ctx, tenant, l.User); err == nil && u.Active {
					u.Active = false
					if err := o.PutUser(ctx, tenant, SystemActor, u); err != nil {
						continue
					}
					o.log(ctx, tenant, SystemActor, "hr.leaver.deactivated", l.Employee, map[string]any{"user": l.User, "approved_by": by, "requested_by": l.RequestedBy})
				}
			}
			l.Status, l.Deactivated = "Done", now.Format(time.RFC3339)
			_ = docs.Upsert(ctx, o.Docs, tenant, cHRLeaver, l.Employee, l, SystemActor)
			_, _ = docs.Update(ctx, o.Docs, tenant, cEmployee, l.Employee, SystemActor, func(x *Employee) error { x.Status = EmpLeft; return nil })
			o.closeWork(ctx, tenant, SystemActor, wHR, l.Employee+":leave-unapproved:"+l.LastDay, "approved and done")
			o.tickItem(ctx, tenant, SystemActor, l.Employee, "leave", "deactivate", "login "+firstOf(l.User, "(none)")+" switched off "+now.Format("2006-01-02 15:04"))
		}
	}
}
