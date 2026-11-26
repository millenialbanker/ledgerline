package ops

import (
	"context"
	"fmt"
	"time"

	"ledgerline/internal/docs"
)

// ---- the resilience screen and its tick ----
//
// Security → Cyber controls carries, beside the records of resilience.go:
// the recovery targets and the RPO actually achieved (backuprpo.go),
// capacity (capacity.go), Ledgerline's release evidence and the cloud
// evidence pack (relevidence.go), the test environment and inspection
// access (sandbox.go), confidentiality agreements and the API caller list
// (cyberset.go), and the firm's VAPT and cyber audit cycle.

// AuditCycle says how often the firm's VAPT and cyber audit fall due.
type AuditCycle struct {
	QSB   string `json:"qsb"` // yes, no, unsure
	Cycle string `json:"cycle"`
	Line  string `json:"line"`
}

func (o *Ops) auditCycle(ctx context.Context, tenant string) AuditCycle {
	v, why := o.cyberNeed(ctx, tenant, "qsb")
	c := AuditCycle{QSB: triWord(v)}
	switch v {
	case triYes:
		c.Cycle, c.Line = "Half-yearly", "As a Qualified Stock Broker the firm has its VAPT and cyber audit every half year, whatever its CSCRF category (SEBI FAQs on CSCRF, FAQ 14); a QSB that is also a CDSL participant sends CDSL a half-yearly VAPT too (CDSL/IS/DP/POLCY/2025/822)."
	case triNo:
		c.Cycle, c.Line = "By CSCRF category", "Not a Qualified Stock Broker: the VAPT and cyber audit follow the firm's CSCRF category (Compliance calendar)."
	default:
		c.Cycle, c.Line = "By CSCRF category", "VAPT and cyber audit follow the CSCRF category; a Qualified Stock Broker has both every half year. "+why+"."
	}
	return c
}

// logRetentionLine warns when security logs are kept for less than two
// years and the firm's CSCRF category asks for two (NSE/INSP/76768 Annex B
// 14: Small, Mid and Qualified REs; CSCRF PR.AA.S9 for every RE's access
// logs of critical systems).
func (o *Ops) logRetentionLine(ctx context.Context, tenant string) (string, bool) {
	set := o.SecuritySettings(ctx, tenant)
	if set.RetentionDays >= 730 {
		return fmt.Sprintf("Security logs are kept for %d days: at least the two years CSCRF asks.", set.RetentionDays), false
	}
	v, _ := o.cyberNeed(ctx, tenant, "cscrf:small,mid,qualified")
	if v == triYes {
		return fmt.Sprintf("Security logs are kept for only %d days. For the firm's CSCRF category NSE's audit terms ask for two years (six months online): raise it to 730 on Incidents → Rules and contacts, or keep the exported logs for the rest.", set.RetentionDays), true
	}
	return retentionWarn(set), false
}

// CyberRes is the screen's data.
type CyberRes struct {
	Settings     CyberSettings     `json:"settings"`
	Intraday     []int             `json:"intraday_choices"`
	RPO          RPOStatus         `json:"rpo"`
	Capacity     CapacityView      `json:"capacity"`
	Release      ReleaseView       `json:"release"`
	Cloud        []CloudPrinciple  `json:"cloud"`
	Sandbox      SandboxInfo       `json:"sandbox"`
	Procedure    []string          `json:"inspection_procedure"`
	InspKinds    []string          `json:"inspection_kinds"`
	Visits       []InspectionVisit `json:"visits"`
	Agreements   []AgreementRow    `json:"agreements"`
	AuditCycle   AuditCycle        `json:"audit_cycle"`
	Retention    string            `json:"retention"`
	RetentionBad bool              `json:"retention_bad"`
	RootedPolicy string            `json:"rooted_policy"`
}

// CyberResView is the screen. Compliance and Admin.
func (o *Ops) CyberResView(ctx context.Context, tenant, actor string) (CyberRes, error) {
	if err := o.require(ctx, tenant, actor, "Seeing cyber controls", RoleCompliance); err != nil {
		return CyberRes{}, err
	}
	v := CyberRes{Settings: o.CyberSettingsOf(ctx, tenant), Intraday: IntradayChoices, RPO: o.rpoStatus(ctx, tenant), Capacity: o.capacityView(ctx, tenant),
		Release: o.releaseView(ctx), Cloud: o.cloudPrinciples(ctx, tenant), Sandbox: o.sandboxInfo(ctx, tenant), Procedure: InspectionProcedure, InspKinds: InspectionKinds,
		Visits: o.inspectionVisits(ctx, tenant), Agreements: o.agreementRows(ctx, tenant), AuditCycle: o.auditCycle(ctx, tenant), RootedPolicy: firstOf(o.mobileSettings(ctx, tenant).RootedPolicy, "warn")}
	v.Retention, v.RetentionBad = o.logRetentionLine(ctx, tenant)
	return v, nil
}

// cyberResTick runs with the security tick: the glitch clocks and the
// capacity figures every quarter hour, intraday backups when due.
func (o *Ops) cyberResTick(ctx context.Context, tenant string, now time.Time) {
	o.intradayTick(ctx, tenant, now)
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cCyberState, "quarter", cyberState{})
	q := capQuarter(now)
	if st.Last == q {
		return
	}
	st.Last = q
	_ = docs.Upsert(ctx, o.Docs, tenant, cCyberState, "quarter", st, SystemActor)
	o.capacityTick(ctx, tenant, now)
	o.glitchTick(ctx, tenant, now)
}
