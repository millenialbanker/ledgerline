package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
)

// ---- Ledgerline's own release evidence, and the cloud evidence pack ----
//
// NSE/INSP/76768 Annex B 23(a) asks, for customised COTS and SaaS, the
// application security tests (SAST, DAST), a functional audit and a VAPT
// after each major release, SOC log integration and an SBOM. The SBOM
// comes from the running server (resilience.go). The test evidence is
// Ledgerline's own: the provider records each release's tests in the
// platform register (/provider/release-evidence), and every firm sees and
// downloads it. Nothing here is the firm's own VAPT, which its CERT-In
// empanelled auditor still does.
//
// Annex B 21(a)-(n) and SEBI's cloud framework (SEBI/HO/ITD/ITD_VAPT/P/
// CIR/2023/033) principles 1-9: the evidence pack maps each principle to
// what Ledgerline shows of itself (where data is held, firm-held keys,
// approved support access, the exit and destruction, backups and drills,
// the vendor exit plan) and to what stays the firm's part.

const cRelEvidence = "relevidence" // platform: REL-nnnnn

// ReleaseEvidence is one test of one Ledgerline release.
type ReleaseEvidence struct {
	ID       string `json:"id"`
	Version  string `json:"version"`  // the release, e.g. v2026.11.3 or git-abc123
	Released string `json:"released"` // YYYY-MM-DD
	Kind     string `json:"kind"`     // SAST, DAST, VAPT, Functional audit, Dependency scan, Other
	Tool     string `json:"tool"`     // the tool or the auditor
	CERTIn   bool   `json:"cert_in"`  // done by a CERT-In empanelled auditor
	Result   string `json:"result"`   // Passed, Findings fixed, Findings open
	Findings string `json:"findings,omitempty"`
	Report   string `json:"report_sha256,omitempty"` // the report's SHA-256, for the firm to match the copy it is given
	Note     string `json:"note,omitempty"`
	By       string `json:"by"`
	At       string `json:"at"`
}

// ReleaseKinds and ReleaseResults are the choices.
var (
	ReleaseKinds   = []string{"SAST", "DAST", "VAPT", "Functional audit", "Dependency scan", "Other"}
	ReleaseResults = []string{"Passed", "Findings fixed", "Findings open"}
	reRelSHA256    = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// RecordReleaseEvidence adds a test of a release to the platform
// register (the provider's endpoint).
func (o *Ops) RecordReleaseEvidence(ctx context.Context, by string, e ReleaseEvidence) (ReleaseEvidence, error) {
	e.Version, e.Tool = strings.TrimSpace(e.Version), strings.TrimSpace(e.Tool)
	if e.Version == "" || e.Tool == "" {
		return e, invalid("give the release and the tool or auditor")
	}
	if !contains(ReleaseKinds, e.Kind) || !contains(ReleaseResults, e.Result) {
		return e, invalid("the kind is one of %s and the result one of %s", strings.Join(ReleaseKinds, ", "), strings.Join(ReleaseResults, ", "))
	}
	if !validDay(e.Released) {
		return e, invalid("the release date is YYYY-MM-DD")
	}
	e.Report = strings.ToLower(strings.TrimSpace(e.Report))
	if e.Report != "" && !reRelSHA256.MatchString(e.Report) {
		return e, invalid("the report hash is a SHA-256 in hex")
	}
	if e.Result != "Passed" && len(strings.TrimSpace(e.Findings)) < 5 {
		return e, invalid("summarise the findings")
	}
	n, err := o.next(ctx, PlatformTenant, "relevidence", by)
	if err != nil {
		return e, err
	}
	e.ID, e.By, e.At = fmt.Sprintf("REL-%05d", n), by, o.now().Format(time.RFC3339)
	e.Findings, e.Note = trim(strings.TrimSpace(e.Findings), 2000), trim(strings.TrimSpace(e.Note), 1000)
	if err := docs.Upsert(ctx, o.Docs, PlatformTenant, cRelEvidence, e.ID, e, by); err != nil {
		return e, err
	}
	o.PlatformEvent(ctx, SecEvent{Kind: "platform.release_evidence", Actor: by, Subject: e.ID, Message: e.Kind + " of " + e.Version + ": " + e.Result})
	return e, nil
}

// releaseEvidence lists the register, newest release first.
func (o *Ops) releaseEvidence(ctx context.Context) []ReleaseEvidence {
	xs, _ := docs.List[ReleaseEvidence](ctx, o.Docs, PlatformTenant, cRelEvidence, "")
	sort.SliceStable(xs, func(i, j int) bool {
		if xs[i].Released != xs[j].Released {
			return xs[i].Released > xs[j].Released
		}
		return xs[i].ID > xs[j].ID
	})
	if xs == nil {
		xs = []ReleaseEvidence{}
	}
	return xs
}

// ReleaseEvidenceList is the provider's view.
func (o *Ops) ReleaseEvidenceList(ctx context.Context) []ReleaseEvidence {
	return o.releaseEvidence(ctx)
}

// runningVersion is this server's version from its build.
func runningVersion() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "devel"
	}
	if bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	for _, s := range bi.Settings {
		if s.Key == "vcs.revision" {
			return "git-" + s.Value
		}
	}
	return "devel"
}

// ReleaseView is the firm's view of the evidence.
type ReleaseView struct {
	Running  string            `json:"running"`
	Evidence []ReleaseEvidence `json:"evidence"`
	Covered  []string          `json:"covered"` // the kinds on record for the running release
	Missing  []string          `json:"missing"` // SAST, DAST, VAPT not on record for it
	Line     string            `json:"line"`
}

func (o *Ops) releaseView(ctx context.Context) ReleaseView {
	v := ReleaseView{Running: runningVersion(), Evidence: o.releaseEvidence(ctx), Covered: []string{}, Missing: []string{}}
	for _, e := range v.Evidence {
		if e.Version == v.Running && !contains(v.Covered, e.Kind) {
			v.Covered = append(v.Covered, e.Kind)
		}
	}
	for _, k := range []string{"SAST", "DAST", "VAPT"} {
		if !contains(v.Covered, k) {
			v.Missing = append(v.Missing, k)
		}
	}
	if len(v.Missing) == 0 {
		v.Line = "SAST, DAST and VAPT are on record for the running release " + v.Running + "."
	} else {
		v.Line = "For the running release " + v.Running + ", Ledgerline has not recorded: " + strings.Join(v.Missing, ", ") + ". Ask Ledgerline for them; earlier releases' evidence is below."
	}
	return v
}

// CloudPrinciple is one principle of SEBI's cloud framework with the
// evidence.
type CloudPrinciple struct {
	No        int      `json:"no"`
	Name      string   `json:"name"`
	Ledger    []string `json:"ledgerline"` // what Ledgerline shows
	FirmsPart string   `json:"firms_part"`
}

// cloudPrinciples builds the pack from what this firm's Ledgerline shows.
func (o *Ops) cloudPrinciples(ctx context.Context, tenant string) []CloudPrinciple {
	res := o.Residency
	where := "where the server is hosted is set by the operator (Settings → Data in India)"
	if res.Region != "" {
		where = "hosted at " + res.Region
		if res.IndiaOnly {
			where += ", data kept in India"
		}
	}
	rpo := o.rpoStatus(ctx, tenant)
	ds := o.restoreDrills(ctx, tenant)
	drill := "no restore drill recorded yet"
	if len(ds) > 0 {
		drill = fmt.Sprintf("last restore drill %s: RTO %d min, RPO %d min", fmtDay(ds[0].Started[:10]), ds[0].RTOMin, ds[0].RPOMin)
	}
	rv := o.releaseView(ctx)
	return []CloudPrinciple{
		{1, "Governance, risk and compliance", []string{"Audit trail of every action, sealed and checked each night", "Security events and incidents register with the CERT-In clock", "Half-yearly review of the Admins' activity"}, "The board-approved cloud policy and the risk assessment of using a SaaS back office."},
		{2, "Selection of the cloud service provider", []string{"Ledgerline's hosting as the operator declares it: " + where}, "Due diligence that the provider is empanelled by MeitY and meets the framework."},
		{3, "Data ownership and data localisation", []string{where, "Full export at any time in JSON and CSV (Settings → Data & exit)", "The firm can hold its own encryption key (Settings → Encryption keys)"}, "Confirm in the contract that the firm owns its data and that it stays in India."},
		{4, "Responsibility of the regulated entity", []string{"Support access only with the firm's approval, masked and recorded (Support access)"}, "The firm stays responsible for its outsourced back office: name the owner and review it."},
		{5, "Due diligence by the regulated entity", []string{"SBOM of the running release (download)", "Release test evidence: " + rv.Line}, "Review the SBOM, the test evidence and the provider's certifications each year."},
		{6, "Security controls", []string{"Encryption at rest with the firm's keys; TLS in transit", "Sign-in lockout, password expiry and history, two-factor sign-in", "API caller list and rate limit (Cyber controls → Settings)", "Logs to the firm's SIEM or SOC (Log export)"}, "The firm's own VAPT and cyber audit cover its use of Ledgerline."},
		{7, "Contractual and regulatory obligations", []string{"Inspection access: full data export, inspection packs, the test environment (Cyber controls → Inspection)"}, "The contract gives SEBI, the exchanges and the firm's auditors the right to inspect."},
		{8, "BCP, disaster recovery and cyber resilience", []string{rpo.Line, drill}, "The firm's BCP and DR plan includes Ledgerline, and its drills restore it."},
		{9, "Vendor lock-in and concentration risk", []string{"Exit plan for Ledgerline pre-filled (Cyber controls → Vendors)", "Data exit with a destruction certificate when the firm leaves"}, "Review the exit plan each year and test the export."},
	}
}

// EvidencePack is the download for the firm's auditors.
func (o *Ops) EvidencePack(ctx context.Context, tenant, actor string) ([]byte, string, error) {
	if err := o.require(ctx, tenant, actor, "Downloading Ledgerline's evidence pack", RoleCompliance); err != nil {
		return nil, "", err
	}
	sbom, _ := buildSBOM(o.now())
	set, _ := o.Settings(ctx, tenant)
	pack := map[string]any{
		"firm": set.Firm, "made": o.now().In(ist).Format(time.RFC3339),
		"note":             "Ledgerline's evidence about itself for the firm's CSCRF, cloud framework and system audit files. The firm's own VAPT and cyber audit are separate.",
		"release":          o.releaseView(ctx),
		"cloud_principles": o.cloudPrinciples(ctx, tenant),
		"recovery_point":   o.rpoStatus(ctx, tenant),
		"capacity":         o.capacityView(ctx, tenant),
		"sbom":             json.RawMessage(sbom),
	}
	b, err := json.MarshalIndent(pack, "", "  ")
	if err != nil {
		return nil, "", err
	}
	o.log(ctx, tenant, actor, "security.evidence_pack.downloaded", "ledgerline", nil)
	return b, "ledgerline-evidence-" + o.today() + ".json", nil
}
