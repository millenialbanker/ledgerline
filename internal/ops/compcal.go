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

// ---- the firm's compliance profile and the exchanges' RAS calendar ----
//
// Running accounts are settled on the dates the stock exchanges set jointly
// for each financial year (SEBI/HO/MIRSD/MIRSD-PoD1/P/CIR/2023/197 of 28
// December 2023, clause 47.1 of the stock brokers' master circular as
// amended; NSE/INSP/60014). Each date is a Friday and/or the Saturday after
// it, separately for clients who chose monthly and quarterly settlement.
// The calendar is a dated list here, kept by the compliance officer or an
// admin, seeded with FY 2026-27 as published on 20-21 January 2026 (for
// example MSE/INSP/18499/2026, the same calendar on every exchange).
//
// The RAS run, Periodic work and Autopilot read the next monthly and
// quarterly dates from Settings (ras_monthly, ras_quarterly); those two
// fields follow this list: they are moved to the next date in it whenever
// the list is saved and every morning by the compliance job.
//
// The profile also says how the firm's system audit is cycled (NSE/INSP/73850
// of 22 April 2026) and whether it is approved for margin trading (the net
// worth certificate is due sooner, NSE/INSP/70505).

const cCompProfile = "compprofile"

// RAS settlement kinds.
const (
	RASKindMonthly   = "Monthly"
	RASKindQuarterly = "Quarterly"
)

// RASDate is one settlement in the exchanges' calendar.
type RASDate struct {
	Kind   string   `json:"kind"`   // Monthly or Quarterly
	Period string   `json:"period"` // 2026-04 for a month; 2026-27 Q1 for a quarter (Q1 is April-June)
	Dates  []string `json:"dates"`  // the Friday and/or the Saturday the exchanges set
	RunOn  string   `json:"run_on"` // the day the firm settles: one of Dates
}

// Last is the last day of the settlement window.
func (r RASDate) Last() string {
	l := r.RunOn
	for _, d := range r.Dates {
		if d > l {
			l = d
		}
	}
	return l
}

// System audit cycles (NSE/INSP/73850, Annexure A).
const (
	SysAuditHalfYearly = "half-yearly" // Type III: NEAT, NNF and algo
	SysAuditYearly     = "yearly"      // Type I or II annual
	SysAuditTwoYearly  = "two-yearly"  // Type II once in 2 years (≤10 locations, ≤50 terminals, not a DP)
	SysAuditNone       = "none"        // NEAT only, not a DP
)

// ComplianceProfile is the firm's compliance set-up the calendar needs.
type ComplianceProfile struct {
	RAS       []RASDate `json:"ras"`
	RASSource string    `json:"ras_source"`
	// SysAudit is the system audit cycle; Auditor the appointed auditor and
	// AuditorEmpanelled whether NSE has empanelled them (NSE/INSP/69631).
	SysAudit          string `json:"sys_audit"`
	Auditor           string `json:"auditor,omitempty"`
	AuditorEmpanelled bool   `json:"auditor_empanelled"`
	AuditorRef        string `json:"auditor_ref,omitempty"`
	// MTF: the firm is approved for margin trading.
	MTF bool   `json:"mtf"`
	By  string `json:"by,omitempty"`
	At  string `json:"at,omitempty"`
}

// rasSourceFY2627 names where the seeded calendar comes from.
const rasSourceFY2627 = "Exchanges' joint annual calendar for FY 2026-27 (January 2026, e.g. MSE/INSP/18499/2026 of 21 January 2026), under SEBI/HO/MIRSD/MIRSD-PoD1/P/CIR/2023/197"

// RASCalendarFY2627 is the exchanges' FY 2026-27 calendar: each date is a
// Friday "and/or" the Saturday after it.
func RASCalendarFY2627() []RASDate {
	fs := func(kind, period, fri string) RASDate {
		return RASDate{Kind: kind, Period: period, Dates: []string{fri, addDays(fri, 1)}, RunOn: fri}
	}
	return []RASDate{
		fs(RASKindQuarterly, "2026-27 Q1", "2026-04-17"),
		fs(RASKindQuarterly, "2026-27 Q2", "2026-07-03"),
		fs(RASKindQuarterly, "2026-27 Q3", "2026-10-16"),
		fs(RASKindQuarterly, "2026-27 Q4", "2027-01-01"),
		fs(RASKindMonthly, "2026-04", "2026-04-17"),
		fs(RASKindMonthly, "2026-05", "2026-05-15"),
		fs(RASKindMonthly, "2026-06", "2026-06-05"),
		fs(RASKindMonthly, "2026-07", "2026-07-03"),
		fs(RASKindMonthly, "2026-08", "2026-08-07"),
		fs(RASKindMonthly, "2026-09", "2026-09-04"),
		fs(RASKindMonthly, "2026-10", "2026-10-16"),
		fs(RASKindMonthly, "2026-11", "2026-11-06"),
		fs(RASKindMonthly, "2026-12", "2026-12-04"),
		fs(RASKindMonthly, "2027-01", "2027-01-01"),
		fs(RASKindMonthly, "2027-02", "2027-02-05"),
		fs(RASKindMonthly, "2027-03", "2027-03-05"),
	}
}

// DefaultComplianceProfile is the profile of a new firm.
func DefaultComplianceProfile() ComplianceProfile {
	return ComplianceProfile{RAS: RASCalendarFY2627(), RASSource: rasSourceFY2627, SysAudit: SysAuditYearly}
}

// CompProfile is the firm's compliance profile.
func (o *Ops) CompProfile(ctx context.Context, tenant string) ComplianceProfile {
	p, _, _ := docs.GetOr(ctx, o.Docs, tenant, cCompProfile, "main", DefaultComplianceProfile())
	if p.RAS == nil {
		p.RAS = []RASDate{}
	}
	if p.SysAudit == "" {
		p.SysAudit = SysAuditYearly
	}
	sortRAS(p.RAS)
	return p
}

func sortRAS(rs []RASDate) {
	sort.SliceStable(rs, func(i, j int) bool {
		if rs[i].RunOn != rs[j].RunOn {
			return rs[i].RunOn < rs[j].RunOn
		}
		return rs[i].Kind > rs[j].Kind // quarterly first on a shared date
	})
}

var (
	reRASMonth   = regexp.MustCompile(`^\d{4}-(0[1-9]|1[0-2])$`)
	reRASQuarter = regexp.MustCompile(`^(\d{4})-(\d{2}) Q[1-4]$`)
)

// SaveCompProfile replaces the profile (compliance officer or admin).
func (o *Ops) SaveCompProfile(ctx context.Context, tenant, actor string, p ComplianceProfile) (ComplianceProfile, error) {
	if err := o.require(ctx, tenant, actor, "Changing the compliance calendar", RoleCompliance); err != nil {
		return p, err
	}
	seen := map[string]bool{}
	for i := range p.RAS {
		r := &p.RAS[i]
		r.Period = strings.TrimSpace(r.Period)
		switch r.Kind {
		case RASKindMonthly:
			if !reRASMonth.MatchString(r.Period) {
				return p, invalid("a monthly settlement's period is the month, like 2026-11 (got %q)", r.Period)
			}
		case RASKindQuarterly:
			m := reRASQuarter.FindStringSubmatch(r.Period)
			if m == nil || fmt.Sprintf("%02d", (atoiOr0(m[1])+1)%100) != m[2] {
				return p, invalid("a quarterly settlement's period is the financial year and quarter, like 2026-27 Q3 (got %q)", r.Period)
			}
		default:
			return p, invalid("a settlement is Monthly or Quarterly (got %q)", r.Kind)
		}
		if seen[r.Kind+"|"+r.Period] {
			return p, invalid("%s %s is in the calendar twice", r.Kind, r.Period)
		}
		seen[r.Kind+"|"+r.Period] = true
		var ds []string
		for _, d := range r.Dates {
			if d = strings.TrimSpace(d); d != "" {
				ds = append(ds, d)
			}
		}
		if len(ds) == 0 || len(ds) > 2 {
			return p, invalid("%s %s needs its Friday and/or Saturday from the exchanges' calendar", r.Kind, r.Period)
		}
		for _, d := range ds {
			t, err := time.Parse("2006-01-02", d)
			if err != nil {
				return p, invalid("date %q must be YYYY-MM-DD", d)
			}
			if wd := t.Weekday(); wd != time.Friday && wd != time.Saturday {
				return p, invalid("%s %s: %s is a %s; the exchanges set Fridays and/or Saturdays", r.Kind, r.Period, d, wd)
			}
		}
		sort.Strings(ds)
		r.Dates = ds
		if r.RunOn == "" {
			r.RunOn = ds[0]
		}
		if !contains(ds, r.RunOn) {
			return p, invalid("%s %s: the firm settles on one of the calendar's dates (%s)", r.Kind, r.Period, strings.Join(ds, " or "))
		}
	}
	sortRAS(p.RAS)
	switch p.SysAudit {
	case SysAuditHalfYearly, SysAuditYearly, SysAuditTwoYearly, SysAuditNone:
	case "":
		p.SysAudit = SysAuditYearly
	default:
		return p, invalid("system audit cycle is half-yearly, yearly, two-yearly or none")
	}
	p.Auditor, p.AuditorRef, p.RASSource = strings.TrimSpace(p.Auditor), strings.TrimSpace(p.AuditorRef), strings.TrimSpace(p.RASSource)
	p.By, p.At = actor, o.now().Format(time.RFC3339)
	if p.RAS == nil {
		p.RAS = []RASDate{}
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cCompProfile, "main", p, actor); err != nil {
		return p, err
	}
	o.log(ctx, tenant, actor, "compliance.profile", "main", map[string]any{"ras_dates": len(p.RAS), "sys_audit": p.SysAudit, "auditor_empanelled": p.AuditorEmpanelled, "mtf": p.MTF})
	date := o.businessDate(ctx, tenant)
	o.syncRASSettings(ctx, tenant, actor, date)
	_, _ = o.Duties(ctx, tenant, date) // the calendar follows the profile
	return p, nil
}

func atoiOr0(s string) int {
	n := 0
	fmt.Sscanf(s, "%d", &n)
	return n
}

// nextRAS is the first settlement of a kind the firm runs on or after date.
func (p ComplianceProfile) nextRAS(kind, date string) (RASDate, bool) {
	for _, r := range p.RAS {
		if r.Kind == kind && r.RunOn >= date {
			return r, true
		}
	}
	return RASDate{}, false
}

// rasWindow is the first settlement of a kind whose window has not closed
// by date (the due date of its duty is the window's last day).
func (p ComplianceProfile) rasWindow(kind, from string) (RASDate, bool) {
	for _, r := range p.RAS {
		if r.Kind == kind && r.Last() >= from {
			return r, true
		}
	}
	return RASDate{}, false
}

// RASDates are the next monthly and quarterly settlement dates on or after
// date from the calendar ("" when the calendar has none left). The RAS run
// can call this instead of reading Settings.
func (o *Ops) RASDates(ctx context.Context, tenant, date string) (monthly, quarterly string) {
	p := o.CompProfile(ctx, tenant)
	if r, ok := p.nextRAS(RASKindMonthly, date); ok {
		monthly = r.RunOn
	}
	if r, ok := p.nextRAS(RASKindQuarterly, date); ok {
		quarterly = r.RunOn
	}
	return monthly, quarterly
}

// syncRASSettings moves Settings' next RAS dates to the calendar's. A date
// with no calendar entry left is not changed.
func (o *Ops) syncRASSettings(ctx context.Context, tenant, actor, date string) {
	m, q := o.RASDates(ctx, tenant, date)
	set, err := o.Settings(ctx, tenant)
	if err != nil || (m == "" || m == set.RASMonthly) && (q == "" || q == set.RASQuarterly) {
		return
	}
	_, err = docs.Update(ctx, o.Docs, tenant, cSettings, "main", actor, func(s *Settings) error {
		if m != "" {
			s.RASMonthly = m
		}
		if q != "" {
			s.RASQuarterly = q
		}
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "ras.dates.synced", "settings", map[string]any{"monthly": m, "quarterly": q})
	}
}

// rasCalendarEnds is the last settlement date in the calendar.
func (p ComplianceProfile) rasCalendarEnds() string {
	last := ""
	for _, r := range p.RAS {
		if l := r.Last(); l > last {
			last = l
		}
	}
	return last
}

// ---- exchange memberships and filing with NSE only ----

// memberExchanges are the exchanges the firm has a member code for.
func memberExchanges(set Settings) []string {
	var out []string
	for _, x := range discExchangeOrder {
		if strings.TrimSpace(set.MemberCodes[x]) != "" {
			out = append(out, x)
		}
	}
	return out
}

// sharedFilingNote says where a submission that the exchanges share goes.
// A member of NSE and other exchanges files common submissions with NSE
// only (NSE/INSP/69184 of 18 July 2025, Phase I; NSE/INSP/70505 of 29
// September 2025, Phase II).
func sharedFilingNote(set Settings) string {
	exs := memberExchanges(set)
	switch {
	case contains(exs, "NSE") && len(exs) > 1:
		return "File with NSE (shared with the other exchanges)"
	case contains(exs, "NSE"):
		return "File with NSE"
	case len(exs) > 0:
		return "File with each exchange: " + strings.Join(exs, ", ")
	}
	return "File with the exchanges"
}

// eachExchangeNote is for submissions every exchange takes separately.
func eachExchangeNote(set Settings) string {
	if exs := memberExchanges(set); len(exs) > 0 {
		return "File with each exchange: " + strings.Join(exs, ", ")
	}
	return "File with each exchange"
}

// ---- system audit (NSE/INSP/73850; Type III half-yearly NSE/INSP/76769) ----

// sysAuditRule is the due-date rule of a system audit milestone for a cycle:
// the yearly audit (April-March) and the October-March half year have the
// audit plan on ENIT by 5 June, the preliminary report by 30 June and the
// action taken report by 30 September; the April-September half year has
// them by 5 November, 30 November and 28 February.
func sysAuditRule(milestone, cycle string) string {
	half := cycle == SysAuditHalfYearly
	switch milestone {
	case "plan":
		if half {
			return "06-05,11-05"
		}
		return "06-05"
	case "prelim":
		if half {
			return "06-30,11-30"
		}
		return "06-30"
	case "atr":
		if half {
			return "02-28,09-30"
		}
		return "09-30"
	}
	return ""
}

func sysAuditFrequency(cycle string) string {
	switch cycle {
	case SysAuditHalfYearly:
		return "Half-yearly"
	case SysAuditTwoYearly:
		return "Every 2 years"
	}
	return "Yearly"
}

// ---- API ----

// CompProfileView is the profile with what it means today.
type CompProfileView struct {
	ComplianceProfile
	NextMonthly   string   `json:"next_monthly"`
	NextQuarterly string   `json:"next_quarterly"`
	CalendarEnds  string   `json:"calendar_ends"`
	Exchanges     []string `json:"exchanges"`
	SharedFiling  string   `json:"shared_filing"`
	Warnings      []string `json:"warnings"`
}

// CompProfileFor is the profile for the screen.
func (o *Ops) CompProfileFor(ctx context.Context, tenant, actor, date string) (CompProfileView, error) {
	if err := o.require(ctx, tenant, actor, "Viewing the compliance calendar", RoleCompliance, RoleChecker, RoleMaker, RoleRisk); err != nil {
		return CompProfileView{}, err
	}
	if !validDate(date) {
		date = o.businessDate(ctx, tenant)
	}
	p := o.CompProfile(ctx, tenant)
	set, _ := o.Settings(ctx, tenant)
	v := CompProfileView{ComplianceProfile: p, CalendarEnds: p.rasCalendarEnds(), Exchanges: memberExchanges(set), SharedFiling: sharedFilingNote(set), Warnings: []string{}}
	v.NextMonthly, v.NextQuarterly = o.RASDates(ctx, tenant, date)
	if v.Exchanges == nil {
		v.Exchanges = []string{}
	}
	if v.NextMonthly == "" || v.NextQuarterly == "" {
		v.Warnings = append(v.Warnings, "The RAS calendar has no settlement left after "+fmtDay(date)+": add the exchanges' calendar for the next financial year.")
	} else if daysBetween(date, v.CalendarEnds) < 60 {
		v.Warnings = append(v.Warnings, "The RAS calendar ends on "+fmtDay(v.CalendarEnds)+": add the next financial year's dates when the exchanges publish them (usually in January).")
	}
	if p.SysAudit != SysAuditNone && !p.AuditorEmpanelled {
		v.Warnings = append(v.Warnings, "Record the system auditor as empanelled by NSE: NSE accepts system audit reports only from an auditor on its empanelled list (NSE/INSP/69631, NSE/INSP/73850).")
	}
	return v, nil
}
