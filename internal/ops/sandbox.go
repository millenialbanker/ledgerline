package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
)

// ---- the test environment and inspection access ----
//
// CSCRF PR.DS.S5 asks for development and test environments kept apart
// from production, with at least one non-production environment for
// critical software; MCX/TECH/279/2024 Annexure II cl.8 asks a back
// office vendor to keep a UAT environment and to let MCX inspect the
// software at site without notice and retrieve data for the regulators.
//
//   - The test environment is a separate firm code on the server that the
//     operator marks as non-production (LEDGERLINE_SANDBOX_TENANTS=
//     <test code>:<firm code>). It has its own data and logins: nothing in
//     it touches the firm's books. An Admin prepares it from the firm's
//     Cyber controls screen: a login of the same user ID with a one-time
//     password, and the sample trading day loaded. It has no connections
//     until someone adds them there; production credentials must never go
//     into it.
//   - Inspection access: the procedure the firm follows when SEBI, an
//     exchange or a depository inspects, and the register of each visit
//     and what the inspectors were given.

const cInspVisit = "inspvisit" // IV-nnnnn

// SandboxOf returns the test environment of a firm, if the operator set
// one up.
func (o *Ops) SandboxOf(tenant string) string {
	var out []string
	for sb, prod := range o.Sandboxes {
		if prod == tenant {
			out = append(out, sb)
		}
	}
	sort.Strings(out)
	if len(out) == 0 {
		return ""
	}
	return out[0]
}

// IsSandbox reports a non-production firm code.
func (o *Ops) IsSandbox(tenant string) bool { _, ok := o.Sandboxes[tenant]; return ok }

// SandboxInfo is what the firm sees of its test environment.
type SandboxInfo struct {
	Code       string   `json:"code,omitempty"`
	IsSandbox  bool     `json:"is_sandbox"` // this firm code is itself a test environment
	Production string   `json:"production,omitempty"`
	Users      int      `json:"users"`
	SampleDays []string `json:"sample_days"`
	Line       string   `json:"line"`
}

func (o *Ops) sandboxInfo(ctx context.Context, tenant string) SandboxInfo {
	if prod, ok := o.Sandboxes[tenant]; ok {
		return SandboxInfo{IsSandbox: true, Production: prod, SampleDays: []string{}, Line: "This is the test environment of " + prod + ": not production. Nothing here reaches the firm's books, the exchanges or clients unless someone connects it; never enter production credentials here."}
	}
	sb := o.SandboxOf(tenant)
	if sb == "" {
		return SandboxInfo{SampleDays: []string{}, Line: "No test environment is set up for this firm. Ask Ledgerline support for one: it is a separate firm code on the server with its own data and logins."}
	}
	v := SandboxInfo{Code: sb, SampleDays: []string{}}
	us, _ := o.Users(ctx, sb)
	v.Users = len(us)
	if st, err := o.SampleStatus(ctx, sb); err == nil {
		v.SampleDays = append(v.SampleDays, st.Loaded...)
	}
	switch {
	case v.Users == 0:
		v.Line = "The test environment " + sb + " is set up but empty. Prepare it: a login for you and a sample trading day."
	default:
		v.Line = fmt.Sprintf("The test environment %s has %d login(s) and %d sample day(s). Sign in to it with the firm code %s.", sb, v.Users, len(v.SampleDays), sb)
	}
	return v
}

// PrepareSandbox makes the Admin a login in the firm's test environment
// (if it has none) and loads the first sample day there (if none is
// loaded). It returns the one-time password of a new login.
func (o *Ops) PrepareSandbox(ctx context.Context, tenant, actor string, trades TradeIngester) (SandboxInfo, string, error) {
	if err := o.require(ctx, tenant, actor, "Preparing the test environment", RoleAdmin); err != nil {
		return SandboxInfo{}, "", err
	}
	sb := o.SandboxOf(tenant)
	if sb == "" {
		return SandboxInfo{}, "", fmt.Errorf("%w: no test environment is set up for this firm; ask Ledgerline support", ErrState)
	}
	pw := ""
	if _, err := o.user(ctx, sb, actor); err != nil {
		me, _ := o.user(ctx, tenant, actor)
		u := User{ID: actor, Name: firstOf(me.Name, actor), Role: RoleAdmin, Active: true}
		if err := o.PutUser(ctx, sb, SystemActor, u); err != nil {
			return SandboxInfo{}, "", err
		}
		pw = TempPassword()
		if err := o.SetPassword(ctx, sb, SystemActor, actor, pw, true); err != nil {
			return SandboxInfo{}, "", err
		}
		o.log(ctx, sb, SystemActor, "user.created", actor, map[string]any{"role": RoleAdmin, "from": "test environment prepared by " + tenant})
	}
	if st, err := o.SampleStatus(ctx, sb); err == nil && len(st.Loaded) == 0 && trades != nil {
		set, _ := o.Settings(ctx, sb)
		day := o.businessDate(ctx, sb)
		if !set.IsTradingDay(PartEquity, day) {
			day = set.PrevTradingDay(PartEquity, day)
		}
		if _, err := o.LoadSampleFor(ctx, sb, actor, day, trades); err != nil {
			return o.sandboxInfo(ctx, tenant), pw, fmt.Errorf("the login is ready but the sample day did not load: %w", err)
		}
	}
	o.log(ctx, tenant, actor, "security.sandbox.prepared", sb, map[string]any{"new_login": pw != ""})
	return o.sandboxInfo(ctx, tenant), pw, nil
}

// ---- inspection access ----

// InspectionProcedure is what the firm does when an inspector arrives.
var InspectionProcedure = []string{
	"Ask for the authority letter and identity of each inspector; record them in the register below.",
	"Give access the same day, without waiting for notice: supervised screens in Ledgerline, an inspection pack (Compliance → Inspection packs) and, for data the regulator asks for, a full data export (Settings → Data & exit).",
	"For hands-on testing, use the test environment, never production; give an inspector a login only through Users with a role that cannot change anything, and switch it off when the visit ends.",
	"Ledgerline support helps with data retrieval through Support access, approved by the firm and recorded.",
	"Record what was given and close the visit with the inspectors' observations; their follow-up goes on the compliance calendar.",
}

// InspectionKinds are what the firm can give.
var InspectionKinds = []string{"Supervised screens", "Inspection pack", "Full data export", "Test environment", "Read-only login", "Other"}

// InspectionVisit is one inspection.
type InspectionVisit struct {
	ID         string   `json:"id"`
	Authority  string   `json:"authority"` // SEBI, NSE, BSE, MCX, CDSL, NSDL, Other
	Inspectors string   `json:"inspectors"`
	From       string   `json:"from"`
	To         string   `json:"to,omitempty"`
	Given      []string `json:"given"`
	Note       string   `json:"note,omitempty"`
	Closed     bool     `json:"closed"`
	Outcome    string   `json:"outcome,omitempty"`
	By         string   `json:"by"`
	At         string   `json:"at"`
}

// SaveInspectionVisit records a visit, or closes it (Outcome). Compliance
// and Admin.
func (o *Ops) SaveInspectionVisit(ctx context.Context, tenant, actor string, v InspectionVisit) (InspectionVisit, error) {
	if err := o.require(ctx, tenant, actor, "Recording inspections", RoleCompliance); err != nil {
		return v, err
	}
	if !contains([]string{"SEBI", "NSE", "BSE", "MCX", "MSE", "NCDEX", "CDSL", "NSDL", "Other"}, v.Authority) {
		return v, invalid("say who inspects: SEBI, an exchange, a depository or Other")
	}
	v.Inspectors = trim(strings.TrimSpace(v.Inspectors), 300)
	if v.Inspectors == "" || !validDay(v.From) || v.From > o.today() || v.To != "" && (!validDay(v.To) || v.To < v.From) {
		return v, invalid("give the inspectors' names and the dates of the visit")
	}
	for _, g := range v.Given {
		if !contains(InspectionKinds, g) {
			return v, invalid("unknown access %q", g)
		}
	}
	if v.Closed && len(strings.TrimSpace(v.Outcome)) < 5 {
		return v, invalid("say what the inspectors observed when closing the visit")
	}
	if v.ID == "" {
		n, err := o.next(ctx, tenant, "inspvisit", actor)
		if err != nil {
			return v, err
		}
		v.ID = fmt.Sprintf("IV-%05d", n)
	}
	if v.Given == nil {
		v.Given = []string{}
	}
	v.Note, v.Outcome = trim(strings.TrimSpace(v.Note), 2000), trim(strings.TrimSpace(v.Outcome), 2000)
	v.By, v.At = actor, o.now().Format(time.RFC3339)
	if err := docs.Upsert(ctx, o.Docs, tenant, cInspVisit, v.ID, v, actor); err != nil {
		return v, err
	}
	o.log(ctx, tenant, actor, "security.inspection", v.ID, map[string]any{"authority": v.Authority, "from": v.From, "given": v.Given, "closed": v.Closed})
	return v, nil
}

func (o *Ops) inspectionVisits(ctx context.Context, tenant string) []InspectionVisit {
	xs, _ := docs.List[InspectionVisit](ctx, o.Docs, tenant, cInspVisit, "")
	sort.Slice(xs, func(i, j int) bool { return xs[i].From > xs[j].From })
	if xs == nil {
		xs = []InspectionVisit{}
	}
	return xs
}
