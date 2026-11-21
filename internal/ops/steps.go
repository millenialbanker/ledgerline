package ops

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
)

// ---- the day's steps: due times and owners ----
//
// Every step of the day, week and month that Ledgerline knows about is a
// step with a due-by time (IST) and an owner (a role, or a named person):
// the Autopilot automations, the periodic work, the files of the daily file
// checklist, a few manual steps, and the firm's own steps. The defaults
// come from the step's own schedule and role; an Admin changes them under
// Settings → Step reminders (SaveStepSettings).
//
// stepremind.go reminds the owner of a step not done by its due time,
// escalates it, and keeps it on the pending list until it is done. A step is
// kept generic (its definition, owner, due time and how to tell it is done)
// so that a later hand-off chain can link one step to the next: StepDef.Next
// is reserved for that and is not used yet.

const (
	cStepSet = "stepset" // "main": the firm's step settings; "tick": the last reminder run

	stepEscalateDefault = 30 // minutes after the due time, by default
)

// Step kinds.
const (
	StepAutopilot = "autopilot"
	StepPeriodic  = "periodic"
	StepFile      = "file"
	StepManual    = "manual"
)

// StepDef is one step.
type StepDef struct {
	ID    string `json:"id"` // kind:source id, e.g. autopilot:interest_daily
	Kind  string `json:"kind"`
	Name  string `json:"name"`
	About string `json:"about,omitempty"`
	Area  string `json:"area"`
	// Days the step runs: business (exchange business days, without the
	// firm's own holidays), business+sat (and Saturdays that are not
	// holidays), weekly:Mon…Sat, or monthly:1…28 (that day, or the next
	// business day).
	Days string `json:"days"`
	Due  string `json:"due"`  // default due-by time, HH:MM IST
	Role string `json:"role"` // default owner
	View string `json:"view,omitempty"`
	// Exceptions marks a step with exclusion criteria: its reminder lists
	// them for a one-click confirmation (stepexcept.go).
	Exceptions bool `json:"exceptions,omitempty"`
	// Quiet steps send no reminder unless the firm switches them on.
	Quiet bool `json:"quiet,omitempty"`
	// Custom is a step the firm added itself.
	Custom bool `json:"custom,omitempty"`
	// Next is reserved for a hand-off chain (the step that follows this
	// one). Nothing reads it yet.
	Next string `json:"next,omitempty"`

	ref string // the automation, periodic task or file it stands for
}

// stepStageDue are the default due times of the Autopilot stages: a while
// after each stage's scheduled run (seedAutopilotJobs).
var stepStageDue = map[string]string{"bod": "09:00", "intraday": "14:00", "preclose": "15:20", "eod": "20:30", "commodity": "08:30"}

// stepAutoDue are automations due at their own time.
var stepAutoDue = map[string]string{"payout_batch": "17:30", "interest_daily": "20:30", "reports": "21:00", "day_close": "21:00"}

// stepAutoAlways are automations whose work is needed every business day
// whatever Autopilot's setting: switched off, a person does it by hand,
// and it is still a step. The other automations are steps only while on.
var stepAutoAlways = map[string]bool{"recon": true, "billing": true, "settlement": true, "notes": true, "day_close": true, "interest_daily": true}

// stepAreaRole is the role that owns an area's steps by default.
func stepAreaRole(area string) string {
	switch area {
	case "Risk":
		return RoleRisk
	case "Compliance":
		return RoleCompliance
	case "Clients", "Client insights", "Depository":
		return RoleMaker
	}
	return RoleChecker
}

// stepManual are the steps that are done outside Ledgerline's automations
// but that Ledgerline can still tell are done.
var stepManual = []StepDef{
	{ID: "manual:reports_upload", Kind: StepManual, Name: "Daily reports uploaded to the exchanges", About: "Every daily regulatory report generated, validated, uploaded on the exchange portal and marked uploaded on Regulatory reports.", Area: "Compliance", Days: "business", Due: "23:00", Role: RoleCompliance, View: "reports"},
	{ID: "manual:payout_upload", Kind: StepManual, Name: "Payout bank file uploaded", About: "The day's payout batch uploaded on the bank's site and its UTRs entered (or sent by the bank API). Done when no payout batch of the day is waiting for its upload.", Area: "Money", Days: "business", Due: "18:30", Role: RoleChecker, View: "money"},
}

// stepAutoDefs are the Autopilot steps. Event-driven automations (file
// intake, payouts on request…) have no time of day and are not steps; the
// month-end postings are the periodic step "Month-end postings".
func stepAutoDefs() []StepDef {
	var out []StepDef
	for _, d := range AutoDefs {
		due := firstOf(stepAutoDue[d.ID], stepStageDue[d.Stage])
		if due == "" {
			continue
		}
		role := stepAreaRole(d.Area)
		if d.ID == "interest_daily" {
			role = RoleChecker // PostDailyInterest: Ops checker or Risk manager
		}
		out = append(out, StepDef{ID: StepAutopilot + ":" + d.ID, Kind: StepAutopilot, Name: d.Name, About: d.About, Area: d.Area, Days: "business", Due: due, Role: role, View: "autopilot",
			Exceptions: stepExceptionFns[StepAutopilot+":"+d.ID] != nil, Quiet: d.Area == "Client insights", ref: d.ID})
	}
	return out
}

// stepPeriodicDefs are the periodic tasks as steps.
func stepPeriodicDefs() []StepDef {
	var out []StepDef
	for _, t := range PeriodicTasks {
		due, days := "17:00", "business"
		if t.Freq == "Daily" {
			due = "10:00"
		}
		if t.ID == "funds_securities" {
			days = "business+sat" // sent on Saturday, for the week to Friday
		}
		out = append(out, StepDef{ID: StepPeriodic + ":" + t.ID, Kind: StepPeriodic, Name: t.Name, About: t.About, Area: t.Area, Days: days, Due: due, Role: stepAreaRole(t.Area), View: firstOf(t.View, "periodic"), ref: t.ID})
	}
	return out
}

// stepFileDefs are the files of the firm's daily checklist (under its
// clearing arrangements), except the intraday ones, which have no single
// time. Each is due its usual time plus the missing-file alerts' grace.
func stepFileDefs(cl FileChecklist, grace int) []StepDef {
	var out []StepDef
	seen := map[string]bool{}
	for _, it := range cl.Items {
		if it.Intraday || seen[it.ID] {
			continue
		}
		seen[it.ID] = true
		due := it.At
		if t, err := time.Parse("15:04", it.At); err == nil {
			if t2 := t.Add(time.Duration(grace) * time.Minute); t2.Day() == t.Day() {
				due = t2.Format("15:04")
			} else {
				due = "23:59"
			}
		}
		verb := "received from "
		if it.Direction == "out" {
			verb = "written for "
		}
		about := it.Title + " " + verb + it.Party + " (for " + strings.Join(it.For, ", ") + ")."
		if it.NextDay {
			about += " It comes the next business day."
		}
		if it.HandNote != "" {
			about += " " + it.HandNote
		}
		out = append(out, StepDef{ID: StepFile + ":" + it.ID, Kind: StepFile, Name: it.Title, About: about, Area: "Files", Days: "business", Due: due, Role: dailyFileRole(it.DailyFile), View: "xfiles", ref: it.ID})
	}
	return out
}

// StepSetting is the firm's change to one step. Empty fields keep the
// default.
type StepSetting struct {
	Due         string `json:"due,omitempty"`          // HH:MM IST
	Owner       string `json:"owner,omitempty"`        // a role, or a user ID
	EscalateMin int    `json:"escalate_min,omitempty"` // minutes after the due time
	// Off: no reminder for the step (false switches on a quiet step).
	Off *bool `json:"off,omitempty"`
}

// StepSettings are the firm's step reminder settings.
type StepSettings struct {
	// Off switches every step reminder off.
	Off bool `json:"off"`
	// EscalateTo is who hears of a step still not done after its delay: a
	// role or a user ID (the head of the desk). Empty: the Admin.
	EscalateTo string                 `json:"escalate_to,omitempty"`
	Steps      map[string]StepSetting `json:"steps"`
	// Custom are the firm's own steps (always done by hand).
	Custom    []StepDef `json:"custom"`
	UpdatedBy string    `json:"updated_by,omitempty"`
	UpdatedAt string    `json:"updated_at,omitempty"`
}

func (o *Ops) stepSettings(ctx context.Context, tenant string) StepSettings {
	s, _, _ := docs.GetOr(ctx, o.Docs, tenant, cStepSet, "main", StepSettings{})
	if s.Steps == nil {
		s.Steps = map[string]StepSetting{}
	}
	if s.Custom == nil {
		s.Custom = []StepDef{}
	}
	for i := range s.Custom {
		s.Custom[i].Kind, s.Custom[i].Custom, s.Custom[i].Area = StepManual, true, firstOf(s.Custom[i].Area, "Firm's own")
	}
	return s
}

// effective applies the firm's setting to a step: due time, owner,
// escalation delay and whether it reminds.
func (s StepSettings) effective(d StepDef) (due, owner string, esc int, on bool) {
	x := s.Steps[d.ID]
	due, owner, esc, on = firstOf(x.Due, d.Due), firstOf(x.Owner, d.Role), x.EscalateMin, !d.Quiet
	if esc <= 0 {
		esc = stepEscalateDefault
	}
	if x.Off != nil {
		on = !*x.Off
	}
	return
}

var (
	reStepDays = regexp.MustCompile(`^(business|business\+sat|weekly:(Mon|Tue|Wed|Thu|Fri|Sat)|monthly:([1-9]|1\d|2[0-8]))$`)
	reStepSlug = regexp.MustCompile(`[^a-z0-9]+`)
)

// staffRoles are the roles a step can belong to.
var staffRoles = []string{RoleMaker, RoleChecker, RoleRisk, RoleCompliance, RoleAdmin}

// checkStepOwner accepts a staff role or the ID of an active staff user.
func (o *Ops) checkStepOwner(ctx context.Context, tenant, who string) error {
	if contains(staffRoles, who) {
		return nil
	}
	us, _ := o.Users(ctx, tenant)
	for _, u := range us {
		if u.ID == who {
			if !u.Active {
				return invalid("%s is not an active user", who)
			}
			if !contains(staffRoles, u.Role) {
				return invalid("%s is %s: steps belong to the firm's own staff", who, u.Role)
			}
			return nil
		}
	}
	return invalid("%q is not a role (%s) or a user", who, strings.Join(staffRoles, ", "))
}

// SaveStepSettings checks and stores the firm's step settings.
func (o *Ops) SaveStepSettings(ctx context.Context, tenant, actor string, in StepSettings) (StepSettings, error) {
	if err := o.require(ctx, tenant, actor, "Changing step due times and owners", RoleAdmin); err != nil {
		return in, err
	}
	in.EscalateTo = strings.TrimSpace(in.EscalateTo)
	if in.EscalateTo != "" {
		if err := o.checkStepOwner(ctx, tenant, in.EscalateTo); err != nil {
			return in, fmt.Errorf("escalation: %w", err)
		}
	}
	known := map[string]StepDef{}
	defs, _ := o.StepDefs(ctx, tenant)
	for _, d := range defs {
		if !d.Custom {
			known[d.ID] = d
		}
	}
	custom := []StepDef{}
	seen := map[string]bool{}
	for _, c := range in.Custom {
		c.Name, c.About = strings.TrimSpace(c.Name), strings.TrimSpace(c.About)
		if c.Name == "" {
			return in, invalid("give the firm's step a name")
		}
		if c.ID == "" {
			c.ID = "custom:" + strings.Trim(reStepSlug.ReplaceAllString(strings.ToLower(c.Name), "-"), "-")
		}
		if !strings.HasPrefix(c.ID, "custom:") || len(c.ID) > 60 || len(c.ID) <= len("custom:") || strings.ContainsAny(c.ID, "|") {
			return in, invalid("step ID %q: use custom: and lower-case letters, digits and dashes", c.ID)
		}
		if seen[c.ID] {
			return in, invalid("two of the firm's steps are called %q", c.Name)
		}
		seen[c.ID] = true
		if c.Days == "" {
			c.Days = "business"
		}
		if !reStepDays.MatchString(c.Days) {
			return in, invalid("%s: days must be business, business+sat, weekly:Mon…Sat or monthly:1…28", c.Name)
		}
		if _, err := time.Parse("15:04", c.Due); err != nil {
			return in, invalid("%s: the due time must be HH:MM", c.Name)
		}
		c.Role = firstOf(strings.TrimSpace(c.Role), RoleMaker)
		if err := o.checkStepOwner(ctx, tenant, c.Role); err != nil {
			return in, fmt.Errorf("%s: %w", c.Name, err)
		}
		c.Kind, c.Custom, c.Area, c.Exceptions, c.Quiet, c.Next = StepManual, true, firstOf(strings.TrimSpace(c.Area), "Firm's own"), false, false, ""
		custom = append(custom, c)
		known[c.ID] = c
	}
	steps := map[string]StepSetting{}
	for id, x := range in.Steps {
		d, ok := known[id]
		if !ok {
			return in, invalid("unknown step %q", id)
		}
		x.Due, x.Owner = strings.TrimSpace(x.Due), strings.TrimSpace(x.Owner)
		if x.Due != "" {
			if _, err := time.Parse("15:04", x.Due); err != nil {
				return in, invalid("%s: the due time must be HH:MM (IST)", d.Name)
			}
		}
		if x.Owner != "" {
			if err := o.checkStepOwner(ctx, tenant, x.Owner); err != nil {
				return in, fmt.Errorf("%s: %w", d.Name, err)
			}
		}
		if x.EscalateMin < 0 || x.EscalateMin > 600 {
			return in, invalid("%s: escalate 1 to 600 minutes after the due time", d.Name)
		}
		if x.Due == d.Due {
			x.Due = ""
		}
		if x.Owner == d.Role {
			x.Owner = ""
		}
		if x.EscalateMin == stepEscalateDefault {
			x.EscalateMin = 0
		}
		if x.Off != nil && *x.Off == d.Quiet {
			x.Off = nil
		}
		if x != (StepSetting{}) {
			steps[id] = x
		}
	}
	out := StepSettings{Off: in.Off, EscalateTo: in.EscalateTo, Steps: steps, Custom: custom, UpdatedBy: actor, UpdatedAt: o.now().Format(time.RFC3339)}
	if err := docs.Upsert(ctx, o.Docs, tenant, cStepSet, "main", out, actor); err != nil {
		return out, err
	}
	o.log(ctx, tenant, actor, "step.settings", "main", map[string]any{"off": out.Off, "escalate_to": out.EscalateTo, "changed": len(steps), "custom": len(custom)})
	return out, nil
}

// StepDefs lists the firm's steps today: the Autopilot steps, the periodic
// work that applies to it, its checklist files, the manual steps and its own.
func (o *Ops) StepDefs(ctx context.Context, tenant string) ([]StepDef, error) {
	cl, err := o.DailyFileChecklist(ctx, tenant, o.now().Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	return o.stepDefsWith(ctx, tenant, cl, o.stepSettings(ctx, tenant)), nil
}

func (o *Ops) stepDefsWith(ctx context.Context, tenant string, cl FileChecklist, ss StepSettings) []StepDef {
	out := stepAutoDefs()
	for _, d := range stepPeriodicDefs() {
		if a := o.periodicApplicability(ctx, tenant, d.ref); a != nil && a.Verdict == ApplyNo {
			continue
		}
		out = append(out, d)
	}
	out = append(out, stepFileDefs(cl, o.FileAlertSettings(ctx, tenant).GraceMinutes)...)
	out = append(out, stepManual...)
	out = append(out, ss.Custom...)
	return out
}

// worksOn says whether the step runs on a day: holidays and weekends are
// respected, Saturdays only for the steps that run on Saturdays.
func (d StepDef) worksOn(set Settings, date string) bool {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return false
	}
	satOK := func() bool { h, _ := set.IsHoliday(date); return t.Weekday() == time.Saturday && !h }
	switch {
	case d.Days == "business+sat":
		return set.IsBusinessDay(date) || satOK()
	case strings.HasPrefix(d.Days, "weekly:"):
		if t.Weekday().String()[:3] != strings.TrimPrefix(d.Days, "weekly:") {
			return false
		}
		return set.IsBusinessDay(date) || satOK()
	case strings.HasPrefix(d.Days, "monthly:"):
		var n int
		fmt.Sscanf(strings.TrimPrefix(d.Days, "monthly:"), "%d", &n)
		if !set.IsBusinessDay(date) || t.Day() < n {
			return false
		}
		// The Nth, or the first business day after it in the month.
		for x := fmt.Sprintf("%s-%02d", date[:7], n); x < date; x = addDays(x, 1) {
			if set.IsBusinessDay(x) {
				return false
			}
		}
		return true
	}
	return set.IsBusinessDay(date)
}

// StepSettingRow is one step with the firm's setting, for Settings.
type StepSettingRow struct {
	StepDef
	EffDue      string `json:"eff_due"`
	Owner       string `json:"owner"`
	EscalateMin int    `json:"escalate_min"`
	On          bool   `json:"on"`
	Changed     bool   `json:"changed"`
}

// StepSettingsView is the step reminders part of Settings.
type StepSettingsView struct {
	Off        bool             `json:"off"`
	EscalateTo string           `json:"escalate_to"`
	Rows       []StepSettingRow `json:"rows"`
	Custom     []StepDef        `json:"custom"`
	Roles      []string         `json:"roles"`
	Users      []User           `json:"users"`
	UpdatedBy  string           `json:"updated_by,omitempty"`
	UpdatedAt  string           `json:"updated_at,omitempty"`
}

// StepSettingsFor shows every step with its due time and owner.
func (o *Ops) StepSettingsFor(ctx context.Context, tenant, actor string) (StepSettingsView, error) {
	if err := o.require(ctx, tenant, actor, "Viewing step reminders", RoleMaker, RoleChecker, RoleRisk, RoleCompliance); err != nil {
		return StepSettingsView{}, err
	}
	ss := o.stepSettings(ctx, tenant)
	defs, err := o.StepDefs(ctx, tenant)
	if err != nil {
		return StepSettingsView{}, err
	}
	v := StepSettingsView{Off: ss.Off, EscalateTo: ss.EscalateTo, Custom: ss.Custom, Roles: staffRoles, Users: []User{}, UpdatedBy: ss.UpdatedBy, UpdatedAt: ss.UpdatedAt}
	for _, d := range defs {
		due, owner, esc, on := ss.effective(d)
		_, changed := ss.Steps[d.ID]
		v.Rows = append(v.Rows, StepSettingRow{StepDef: d, EffDue: due, Owner: owner, EscalateMin: esc, On: on, Changed: changed})
	}
	us, _ := o.Users(ctx, tenant)
	for _, u := range us {
		if u.Active && contains(staffRoles, u.Role) {
			v.Users = append(v.Users, User{ID: u.ID, Name: u.Name, Role: u.Role, Active: true})
		}
	}
	sort.Slice(v.Users, func(i, j int) bool { return v.Users[i].ID < v.Users[j].ID })
	return v, nil
}
