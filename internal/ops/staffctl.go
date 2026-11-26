package ops

import (
	"context"
	"fmt"
	"strings"
	"time"

	"ledgerline/internal/docs"
)

// ---- staff controls: AML-sensitive positions, the DP compliance
// officer, photo identity of DP staff ----
//
//   - SEBI AML/CFT master circular SEBI/HO/MIRSD/MIRSDSECFATF/P/CIR/2024/78
//     para 80: screen employees at hiring, identify the key positions
//     sensitive to money laundering and terrorist financing, and make sure
//     the people in them are suitable and competent. The firm lists the
//     sensitive duties (by default the compliance officer, DP operations
//     and back office, which handle KYC, payments and alerts, plus the
//     principal officer and designated director as key managerial
//     personnel) and records, for each person in one, the check made and
//     who made it.
//   - CDSL master circular for DPs (CDSL/OPS/DP/POLCY/2026/257) 10.1.1 to
//     10.1.2: the DP's compliance officer is an employee who is a
//     director, or a graduate with 2 years', or an SSLC holder with 5
//     years', securities market experience, and gets NISM Series III-A
//     within a year of joining.
//   - CDSL operating instructions 17.7.2: photo identity documents of
//     everyone engaged in DP operations, at the main office, branches and
//     service centres.

// DefaultAMLSensitive are the duties treated as AML-sensitive unless the
// firm sets its own list.
var DefaultAMLSensitive = []string{"Compliance officer", "DP operations", "Back office"}

// AMLSensitiveKMP are key managerial positions always treated as
// AML-sensitive.
var AMLSensitiveKMP = []string{"Principal officer", "Designated director"}

// AMLCheck is the suitability and competence check of a person in an
// AML-sensitive position.
type AMLCheck struct {
	Date  string `json:"date"`
	By    string `json:"by"`
	Note  string `json:"note"` // what was checked: references, background, training
	Train string `json:"training,omitempty"`
}

// COEligibility is the DP compliance officer's eligibility record.
type COEligibility struct {
	Director       bool   `json:"director"`
	Qualification  string `json:"qualification"` // Graduate, SSLC
	ExperienceYrs  int    `json:"experience_years"`
	Employee       bool   `json:"employee"` // on the rolls (not a consultant)
	Note           string `json:"note,omitempty"`
	By             string `json:"by"`
	At             string `json:"at"`
	EligibleReason string `json:"eligible_reason,omitempty"`
}

// eligible: CDSL 10.1.1.
func (c COEligibility) eligible() (bool, string) {
	switch {
	case !c.Employee:
		return false, "the compliance officer must be an employee"
	case c.Director:
		return true, "a director"
	case c.Qualification == "Graduate" && c.ExperienceYrs >= 2:
		return true, fmt.Sprintf("a graduate with %d years' securities market experience", c.ExperienceYrs)
	case c.Qualification == "SSLC" && c.ExperienceYrs >= 5:
		return true, fmt.Sprintf("an SSLC holder with %d years' securities market experience", c.ExperienceYrs)
	}
	return false, "needs to be a director, a graduate with 2 years' or an SSLC holder with 5 years' securities market experience"
}

func (p HRPolicy) amlSensitive() []string {
	if len(p.AMLSensitive) > 0 {
		return p.AMLSensitive
	}
	return DefaultAMLSensitive
}

// amlSensitive: the person holds an AML-sensitive position.
func (e Employee) amlSensitive(p HRPolicy) bool {
	for _, d := range p.amlSensitive() {
		if contains(e.Duties, d) {
			return true
		}
	}
	for _, k := range AMLSensitiveKMP {
		if strings.EqualFold(strings.TrimSpace(e.KMP), k) {
			return true
		}
	}
	return false
}

// hasPhotoID: an identity document is on the person's file.
func (e Employee) hasPhotoID() bool {
	for _, d := range e.Docs {
		if d.Kind == "ID proof" || d.Kind == "Photo ID" {
			return true
		}
	}
	return false
}

// StaffControlRow is one person with a gap.
type StaffControlRow struct {
	Code   string `json:"code"`
	Name   string `json:"name"`
	Branch string `json:"branch,omitempty"`
	What   string `json:"what"`
	Detail string `json:"detail"`
}

// StaffControls is the check of the three controls.
type StaffControls struct {
	Sensitive   []string          `json:"sensitive_duties"`
	AMLPeople   int               `json:"aml_people"`
	DPStaff     int               `json:"dp_staff"`
	COs         int               `json:"compliance_officers"`
	Gaps        []StaffControlRow `json:"gaps"`
	DocKindHint string            `json:"doc_kind_hint"`
}

// staffControls finds the gaps on a day.
func (o *Ops) staffControls(ctx context.Context, tenant, day string) StaffControls {
	p := o.hrPolicy(ctx, tenant)
	v := StaffControls{Sensitive: append(append([]string{}, p.amlSensitive()...), AMLSensitiveKMP...), Gaps: []StaffControlRow{}, DocKindHint: "ID proof"}
	es, _ := o.employees(ctx, tenant)
	for _, e := range es {
		if e.Status == EmpLeft || !e.inService(day) && e.Status != EmpJoining {
			continue
		}
		row := func(what, detail string) {
			v.Gaps = append(v.Gaps, StaffControlRow{Code: e.Code, Name: e.Name, Branch: e.Branch, What: what, Detail: detail})
		}
		if e.amlSensitive(p) {
			v.AMLPeople++
			if e.AMLCheck == nil {
				pos := strings.Join(e.Duties, ", ")
				if e.KMP != "" {
					pos = strings.TrimPrefix(pos+", "+e.KMP, ", ")
				}
				row("AML-sensitive position not checked", "Record the suitability and competence check (references, background, AML training) for "+pos+".")
			}
		}
		if contains(e.Duties, "DP operations") {
			v.DPStaff++
			if !e.hasPhotoID() {
				row("No photo ID on file (DP staff)", "Add an ID proof with photograph to the person's file (CDSL operating instructions 17.7.2).")
			}
		}
		if contains(e.Duties, "Compliance officer") {
			v.COs++
			if e.COEligible == nil {
				row("Compliance officer's eligibility not recorded", "Record whether the person is a director, or their qualification and years of securities market experience (CDSL master circular 10.1.1).")
			} else if ok, why := e.COEligible.eligible(); !ok {
				row("Compliance officer not eligible", why+".")
			}
			if e.JoinedOn != "" && daysBetween(e.JoinedOn, day) > 365 {
				held := false
				for _, n := range certNeeds(e, p, day) {
					if strings.Contains(n.Cert, "III-A") && (n.State == "Valid" || n.State == "Expiring") {
						held = true
					}
				}
				if !held {
					row("Compliance officer without NISM Series III-A after a year", fmt.Sprintf("Joined %s; the certificate is due within one year of employment (CDSL master circular 10.1.2).", fmtDay(e.JoinedOn)))
				}
			}
		}
	}
	return v
}

// StaffControlsView returns the check. HR and compliance.
func (o *Ops) StaffControlsView(ctx context.Context, tenant, actor string) (StaffControls, error) {
	if err := o.needModule(ctx, tenant, ModHRMS); err != nil {
		return StaffControls{}, err
	}
	if err := o.require(ctx, tenant, actor, "Seeing staff controls", RoleHR, RoleCompliance); err != nil {
		return StaffControls{}, err
	}
	return o.staffControls(ctx, tenant, o.today()), nil
}

// RecordAMLCheck records the suitability check of a person in an
// AML-sensitive position. HR and compliance; not for oneself.
func (o *Ops) RecordAMLCheck(ctx context.Context, tenant, actor, code string, c AMLCheck) (Employee, error) {
	if err := o.needModule(ctx, tenant, ModHRMS); err != nil {
		return Employee{}, err
	}
	if err := o.require(ctx, tenant, actor, "Recording staff checks", RoleHR, RoleCompliance); err != nil {
		return Employee{}, err
	}
	if me, ok := o.employeeOf(ctx, tenant, actor); ok && strings.EqualFold(me.Code, code) {
		return Employee{}, fmt.Errorf("%w: someone else checks your own position", ErrForbidden)
	}
	c.Note = strings.TrimSpace(c.Note)
	if len(c.Note) < 10 {
		return Employee{}, invalid("say what was checked (references, background, AML training)")
	}
	if c.Date == "" {
		c.Date = o.today()
	}
	if !validDay(c.Date) || c.Date > o.today() {
		return Employee{}, invalid("give the date of the check (not in the future)")
	}
	c.By, c.Note, c.Train = actor, trim(c.Note, 1000), trim(strings.TrimSpace(c.Train), 300)
	e, err := docs.Update(ctx, o.Docs, tenant, cEmployee, strings.ToUpper(code), actor, func(x *Employee) error {
		x.AMLCheck = &c
		x.UpdatedBy, x.UpdatedAt = actor, o.now().Format(time.RFC3339)
		return nil
	})
	if err != nil {
		return e, err
	}
	o.closeWork(ctx, tenant, actor, wHR, e.Code+":aml-check", "checked")
	o.log(ctx, tenant, actor, "hr.aml_check", e.Code, map[string]any{"date": c.Date})
	return e, nil
}

// RecordCOEligibility records the DP compliance officer's eligibility.
// Compliance and HR.
func (o *Ops) RecordCOEligibility(ctx context.Context, tenant, actor, code string, c COEligibility) (Employee, error) {
	if err := o.needModule(ctx, tenant, ModHRMS); err != nil {
		return Employee{}, err
	}
	if err := o.require(ctx, tenant, actor, "Recording staff checks", RoleHR, RoleCompliance); err != nil {
		return Employee{}, err
	}
	if c.Qualification != "" && c.Qualification != "Graduate" && c.Qualification != "SSLC" && c.Qualification != "Other" {
		return Employee{}, invalid("the qualification is Graduate, SSLC or Other")
	}
	if c.ExperienceYrs < 0 || c.ExperienceYrs > 60 {
		return Employee{}, invalid("give the years of securities market experience")
	}
	c.By, c.At, c.Note = actor, o.now().Format(time.RFC3339), trim(strings.TrimSpace(c.Note), 500)
	ok, why := c.eligible()
	if ok {
		c.EligibleReason = why
	}
	e, err := docs.Update(ctx, o.Docs, tenant, cEmployee, strings.ToUpper(code), actor, func(x *Employee) error {
		if !contains(x.Duties, "Compliance officer") {
			return invalid("%s does not have the compliance officer duty", x.Code)
		}
		x.COEligible = &c
		x.UpdatedBy, x.UpdatedAt = actor, o.now().Format(time.RFC3339)
		return nil
	})
	if err != nil {
		return e, err
	}
	o.log(ctx, tenant, actor, "hr.co_eligibility", e.Code, map[string]any{"eligible": ok, "why": why})
	if !ok {
		return e, fmt.Errorf("%w: recorded, but %s %s", ErrState, e.Name, why)
	}
	return e, nil
}

// staffControlsTick puts each gap on the work queue once a day.
func (o *Ops) staffControlsTick(ctx context.Context, tenant string, now time.Time) {
	today := now.In(ist).Format("2006-01-02")
	last, _, _ := docs.GetOr(ctx, o.Docs, tenant, cHRTick, "staffctl", hrTickDoc{})
	if last.Date >= today {
		return
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cHRTick, "staffctl", hrTickDoc{Date: today}, SystemActor)
	for _, g := range o.staffControls(ctx, tenant, today).Gaps {
		key := g.Code + ":staffctl:" + strings.ToLower(strings.ReplaceAll(g.What, " ", "-"))
		if strings.HasPrefix(g.What, "AML-sensitive") {
			key = g.Code + ":aml-check"
		}
		o.raiseWork(ctx, tenant, wHR, key, fmt.Sprintf("%s (%s): %s", g.Name, g.Code, strings.ToLower(g.What[:1])+g.What[1:]), g.Detail, "", RoleCompliance, "people", today)
	}
}
