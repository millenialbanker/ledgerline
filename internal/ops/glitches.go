package ops

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
)

// ---- technical glitches: the register, the clocks, the reports ----
//
// SEBI's technical glitch framework (SEBI HO/38/44/12(1)2026-MIRSD-TPD1,
// with the exchanges' joint guidelines MCX/TECH/015/2026 Annexure-1 and
// NSE/COMP/72254) covers a broker that offers internet or app trading
// (IBT/STWT) to more than 10,000 registered clients, or that an exchange
// names. It is separate from security incidents (secincidents.go): a
// glitch is a stoppage, slowdown or variance of the trading systems, with
// or without an attacker. One event can be both; the two records are then
// linked and each keeps its own clock.
//
//   - Definition (para 5.1-5.2; MCX Annex-1 para 1.1-1.2): a malfunction of
//     the trading or risk systems, the firm's own or outsourced, during
//     trading hours, that stops, slows or makes vary login, the order life
//     cycle (placement, modification, cancellation, execution) or the view
//     of margin, collateral or funds, for 5 contiguous minutes or more.
//     Left out: a global outage of a cloud provider, a problem at an
//     exchange, clearing corporation or depository, KYC and account
//     opening systems, back-office-only systems and payment gateways. The
//     rows Ledgerline was given cut the list of exclusions short, so
//     "Other exclusion" takes the firm's reason in words; the firm checks
//     it against the circular.
//   - Clocks (para 7.1-7.5; MCX Annex-1 para 4.1-4.5; NSE/INSP/70505
//     Annexure A items 3-4): intimation to the exchanges and to clients
//     within 2 hours, to NSE within 1 hour where the firm is an NSE
//     member; the Preliminary Incident Report (Annexure B) by T+1 (the
//     next trading day if T+1 is a holiday); the Root Cause Analysis
//     (Annexure C) within 14 working days. Both reports go through the
//     common "Samuhik Prativedan Manch" portal in the exchanges' format.
//     The clocks start when the glitch began (the safe reading: the
//     circular counts from occurrence).
//   - Ledgerline prepares the content of Annexures B and C from the
//     register; the firm copies it into the portal's forms and records the
//     reference. The forms' own field lists are not in what Ledgerline
//     was given: the drafts carry the facts the circular names and say so.
//   - Clients are told through the existing client messages (the
//     "glitch.notice" templates: e-mail, SMS, WhatsApp) and the firm
//     records its website and app pop-up notices.
//   - MCX's exemption from the financial disincentive for a minor or
//     app-only glitch (MCX-0120): audit-based evidence that the unique
//     clients affected stayed below 2% of the quarter's average or 2,000.
//     The row is cut after "or 2,000", so Ledgerline takes the lower of
//     the two (the safe reading).

const cGlitch = "glitch" // TG-nnnnnn

// Glitch statuses.
const (
	GlitchOpen     = "Open"     // still going on
	GlitchResolved = "Resolved" // over; reports may still be due
	GlitchClosed   = "Closed"
)

// GlitchFunctions are what a glitch can affect (para 5.1).
var GlitchFunctions = []string{"Login", "Order placement", "Order modification", "Order cancellation", "Order execution", "Margin, collateral or funds view"}

// GlitchKinds are how it showed.
var GlitchKinds = []string{"Stoppage", "Slowdown", "Variance"}

// GlitchExclusion is a cause the framework leaves out (para 5.2).
type GlitchExclusion struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// GlitchExclusions are the causes left out.
var GlitchExclusions = []GlitchExclusion{
	{"cloud", "Global outage of a cloud service provider"},
	{"mii", "Problem at an exchange, clearing corporation or depository"},
	{"kyc", "KYC or account opening systems"},
	{"backoffice", "Back-office systems only"},
	{"payment", "Payment gateway or bank"},
	{"other", "Other exclusion in the circular (say which)"},
}

func glitchExclusion(id string) GlitchExclusion {
	for _, x := range GlitchExclusions {
		if x.ID == id {
			return x
		}
	}
	return GlitchExclusion{}
}

// GlitchReport is an intimation or report made.
type GlitchReport struct {
	Stage     string   `json:"stage"` // nse, exchanges, prelim, rca
	To        []string `json:"to"`
	At        string   `json:"at"`
	How       string   `json:"how"` // Portal, Email, Phone, Other
	Reference string   `json:"reference,omitempty"`
	Note      string   `json:"note,omitempty"`
	By        string   `json:"by"`
	Recorded  string   `json:"recorded"`
}

// GlitchNotice is a notice to clients.
type GlitchNotice struct {
	Channel string         `json:"channel"` // Client messages, Website, App pop-up, Other
	At      string         `json:"at"`
	Target  string         `json:"target,omitempty"`
	Text    string         `json:"text"`
	Results map[string]int `json:"results,omitempty"` // message statuses → count
	By      string         `json:"by"`
}

// GlitchExemption is the evidence for MCX's exemption (MCX-0120).
type GlitchExemption struct {
	QuarterAvg int    `json:"quarter_avg"` // the quarter's average unique clients
	Affected   int    `json:"affected"`    // unique clients affected
	Limit      int    `json:"limit"`       // the lower of 2% of the average and 2,000
	Below      bool   `json:"below"`
	AppOnly    bool   `json:"app_only"`
	Evidence   string `json:"evidence"` // the audit trail or logs it rests on
	Auditor    string `json:"auditor,omitempty"`
	By         string `json:"by"`
	At         string `json:"at"`
}

// Glitch is one entry in the register.
type Glitch struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	System      string   `json:"system"`           // the system that failed
	Vendor      string   `json:"vendor,omitempty"` // the outsourced provider, if any
	Functions   []string `json:"functions"`
	Kind        string   `json:"kind"`
	Started     string   `json:"started"`
	Noticed     string   `json:"noticed"`
	Ended       string   `json:"ended,omitempty"`
	// OutsideHours: it happened outside trading hours (then it is not a
	// glitch under the framework).
	OutsideHours  bool             `json:"outside_hours,omitempty"`
	Exclusion     string           `json:"exclusion,omitempty"`
	ExclusionNote string           `json:"exclusion_note,omitempty"`
	Exchanges     []string         `json:"exchanges"`
	Clients       int              `json:"clients_affected,omitempty"`
	Incident      string           `json:"incident,omitempty"` // the linked security incident
	Status        string           `json:"status"`
	RootCause     string           `json:"root_cause,omitempty"`
	Impact        string           `json:"impact,omitempty"`
	Corrective    string           `json:"corrective,omitempty"`
	Preventive    string           `json:"preventive,omitempty"`
	Reports       []GlitchReport   `json:"reports"`
	Notices       []GlitchNotice   `json:"notices"`
	Exemption     *GlitchExemption `json:"exemption,omitempty"`
	Notes         []IncidentNote   `json:"notes"`
	Updated       string           `json:"updated"`
	UpdatedBy     string           `json:"updated_by"`
	ClosedAt      string           `json:"closed_at,omitempty"`
	// OnCalendar: the glitch's events are on the compliance calendar.
	OnCalendar bool `json:"on_calendar,omitempty"`
}

// GlitchClock is one deadline of a glitch.
type GlitchClock struct {
	Stage   string `json:"stage"`
	Name    string `json:"name"`
	Rule    string `json:"rule"`
	Due     string `json:"due"` // RFC 3339
	DueIST  string `json:"due_ist"`
	Done    bool   `json:"done"`
	DoneAt  string `json:"done_at,omitempty"`
	Late    bool   `json:"late,omitempty"`
	Overdue bool   `json:"overdue,omitempty"`
	LeftMin int    `json:"left_min"`
}

// GlitchView is a glitch with its classification and clocks.
type GlitchView struct {
	Glitch
	Minutes    int           `json:"minutes"` // how long it lasted (so far)
	Reportable bool          `json:"reportable"`
	Why        string        `json:"why"`
	Clocks     []GlitchClock `json:"clocks"`
}

// glitchClass applies the 5-minute test and the exclusions.
func glitchClass(g Glitch, now time.Time) (int, bool, string) {
	st := mustTime(g.Started)
	end := now
	if g.Ended != "" {
		end = mustTime(g.Ended)
	}
	m := max(int(end.Sub(st)/time.Minute), 0)
	switch {
	case g.OutsideHours:
		return m, false, "Outside trading hours: not a technical glitch under the framework"
	case g.Exclusion != "":
		x := glitchExclusion(g.Exclusion).Name
		if g.ExclusionNote != "" {
			x += " (" + g.ExclusionNote + ")"
		}
		return m, false, "Left out of the framework: " + x
	case len(g.Functions) == 0:
		return m, false, "No login, order or margin/funds view affected: not a technical glitch"
	case m < 5 && g.Ended != "":
		return m, false, fmt.Sprintf("Lasted %d min, under 5 contiguous minutes: not reportable", m)
	case m < 5:
		return m, false, fmt.Sprintf("Going on for %d min: reportable once it reaches 5 contiguous minutes", m)
	}
	return m, true, fmt.Sprintf("Reportable: %s of %s for %s during trading hours", strings.ToLower(g.Kind), strings.ToLower(strings.Join(g.Functions, ", ")), minutesText(m))
}

func (g Glitch) reported(stage string) (string, []string) {
	at, to := "", []string{}
	for _, r := range g.Reports {
		if r.Stage == stage {
			if at == "" || r.At < at {
				at = r.At
			}
			to = append(to, r.To...)
		}
	}
	return at, to
}

// glitchView works out the clocks. set gives the trading calendar.
func glitchView(g Glitch, set Settings, now time.Time) GlitchView {
	if g.Reports == nil {
		g.Reports = []GlitchReport{}
	}
	if g.Notices == nil {
		g.Notices = []GlitchNotice{}
	}
	if g.Notes == nil {
		g.Notes = []IncidentNote{}
	}
	if g.Exchanges == nil {
		g.Exchanges = []string{}
	}
	v := GlitchView{Glitch: g, Clocks: []GlitchClock{}}
	v.Minutes, v.Reportable, v.Why = glitchClass(g, now)
	if !v.Reportable {
		return v
	}
	st := mustTime(g.Started).In(ist)
	day := st.Format("2006-01-02")
	add := func(stage, name, rule string, due time.Time, doneAt string) {
		c := GlitchClock{Stage: stage, Name: name, Rule: rule, Due: due.Format(time.RFC3339), DueIST: due.Format("02 Jan 2006 15:04") + " IST", LeftMin: int(due.Sub(now) / time.Minute)}
		if doneAt != "" {
			c.Done, c.DoneAt = true, doneAt
			c.Late = mustTime(doneAt).After(due)
		} else if g.Status != GlitchClosed && now.After(due) {
			c.Overdue = true
		}
		v.Clocks = append(v.Clocks, c)
	}
	endOf := func(d string) time.Time {
		t, _ := time.ParseInLocation("2006-01-02 15:04", d+" 23:59", ist)
		return t
	}
	others := []string{}
	for _, x := range g.Exchanges {
		if x != "NSE" {
			others = append(others, x)
		}
	}
	if contains(g.Exchanges, "NSE") {
		at, _ := g.reported("nse")
		add("nse", "Intimate NSE", "within 1 hour (NSE/INSP/70505)", st.Add(time.Hour), at)
	}
	if len(others) > 0 {
		at, to := g.reported("exchanges")
		if at != "" {
			for _, x := range others {
				if !contains(to, x) {
					at = "" // every exchange affected is told
				}
			}
		}
		add("exchanges", "Intimate "+strings.Join(others, ", "), "within 2 hours (SEBI para 7.1)", st.Add(2*time.Hour), at)
	}
	cat := ""
	for _, n := range g.Notices {
		if cat == "" || n.At < cat {
			cat = n.At
		}
	}
	add("clients", "Tell clients (website, SMS/e-mail/app)", "within 2 hours (SEBI para 7.1-7.2)", st.Add(2*time.Hour), cat)
	pre, _ := g.reported("prelim")
	add("prelim", "Preliminary Incident Report (Annexure B)", "by T+1, the next trading day if a holiday (para 7.3)", endOf(set.NextBusinessDay(day)), pre)
	rca, _ := g.reported("rca")
	add("rca", "Root Cause Analysis (Annexure C)", "within 14 working days (para 7.4-7.5)", endOf(addWorkingDays(set, day, 14)), rca)
	return v
}

// GlitchesScreen is the register.
type GlitchesScreen struct {
	Applies    string            `json:"applies"` // yes, no, unsure
	Why        string            `json:"why"`
	Specified  string            `json:"specified"`
	SpecWhy    string            `json:"specified_why"`
	Check      GlitchCheck       `json:"check"`
	Glitches   []GlitchView      `json:"glitches"`
	Open       int               `json:"open"`
	Overdue    int               `json:"overdue"`
	Functions  []string          `json:"functions"`
	Kinds      []string          `json:"kinds"`
	Exclusions []GlitchExclusion `json:"exclusions"`
	Exchanges  []string          `json:"exchanges"` // the firm's exchanges
	Enhanced   []string          `json:"enhanced"`  // a specified stockbroker's extra duties
	Now        string            `json:"now"`
}

func triWord(v tri) string {
	switch v {
	case triYes:
		return "yes"
	case triNo:
		return "no"
	}
	return "unsure"
}

// specifiedDuties are the enhanced obligations of a specified stockbroker
// (NSE/COMP/74381, Circular 51/2026, within two months of being named),
// as the rows Ledgerline was given describe them.
var specifiedDuties = []string{
	"Meet the enhanced technical glitch obligations within two months of NSE naming the firm (NSE/COMP/74381 para 1-3).",
	"Capacity planning of the trading systems every half year instead of yearly, installed capacity well above the peak (MCX/TECH/015/2026).",
	"BCP and DR: a DR site in a different seismic zone, DR drills, RTO and RPO as the exchanges set (SEBI framework; SEBI-NEW-0024).",
	"API-based logging and monitoring (LAMA) of the trading systems for the exchanges.",
}

// Glitches is the register, newest first. Compliance, Risk and Admin.
func (o *Ops) Glitches(ctx context.Context, tenant, actor string) (GlitchesScreen, error) {
	if err := o.require(ctx, tenant, actor, "Seeing technical glitches", RoleCompliance, RoleRisk); err != nil {
		return GlitchesScreen{}, err
	}
	v, why := o.cyberNeed(ctx, tenant, "ibt:10k|specified")
	sv, swhy := o.cyberNeed(ctx, tenant, "specified")
	sc := GlitchesScreen{Applies: triWord(v), Why: why, Specified: triWord(sv), SpecWhy: swhy, Check: o.glitchCheck(ctx, tenant, o.today()), Glitches: []GlitchView{},
		Functions: GlitchFunctions, Kinds: GlitchKinds, Exclusions: GlitchExclusions, Exchanges: o.firmExchanges(ctx, tenant), Enhanced: specifiedDuties, Now: o.now().Format(time.RFC3339)}
	xs, err := docs.List[Glitch](ctx, o.Docs, tenant, cGlitch, "")
	if err != nil {
		return sc, err
	}
	set, _ := o.Settings(ctx, tenant)
	now := o.now()
	for _, x := range xs {
		gv := glitchView(x, set, now)
		if x.Status != GlitchClosed {
			sc.Open++
		}
		for _, c := range gv.Clocks {
			if c.Overdue {
				sc.Overdue++
			}
		}
		sc.Glitches = append(sc.Glitches, gv)
	}
	sort.Slice(sc.Glitches, func(i, j int) bool { return sc.Glitches[i].Started > sc.Glitches[j].Started })
	return sc, nil
}

func (o *Ops) firmExchanges(ctx context.Context, tenant string) []string {
	f := o.firmFacts(ctx, tenant)
	out := []string{}
	for _, x := range []string{"NSE", "BSE", "MCX", "MSE", "NCDEX"} {
		if f.Member[x] {
			out = append(out, x)
		}
	}
	return out
}

// GlitchInput records or changes a glitch.
type GlitchInput struct {
	Title         string   `json:"title"`
	Description   string   `json:"description"`
	System        string   `json:"system"`
	Vendor        string   `json:"vendor"`
	Functions     []string `json:"functions"`
	Kind          string   `json:"kind"`
	Started       string   `json:"started"`
	Noticed       string   `json:"noticed"`
	Ended         string   `json:"ended"`
	OutsideHours  bool     `json:"outside_hours"`
	Exclusion     string   `json:"exclusion"`
	ExclusionNote string   `json:"exclusion_note"`
	Exchanges     []string `json:"exchanges"`
	Clients       int      `json:"clients_affected"`
	RootCause     string   `json:"root_cause"`
	Impact        string   `json:"impact"`
	Corrective    string   `json:"corrective"`
	Preventive    string   `json:"preventive"`
	Note          string   `json:"note"`
}

func (o *Ops) glitchGate(ctx context.Context, tenant string) error {
	if v, why := o.cyberNeed(ctx, tenant, "ibt:10k|specified"); v == triNo {
		return fmt.Errorf("%w: the technical glitch framework does not apply to the firm (%s). If an exchange has named the firm, say so in Settings → Firm modules", ErrState, why)
	}
	return nil
}

// apply checks the input and writes it on g.
func (in GlitchInput) apply(g *Glitch, now time.Time) error {
	in.Title, in.System = strings.TrimSpace(in.Title), strings.TrimSpace(in.System)
	if len(in.Title) < 5 || in.System == "" {
		return invalid("say what happened and which system failed")
	}
	if !contains(GlitchKinds, in.Kind) {
		return invalid("say whether it was a %s", strings.Join(GlitchKinds, ", "))
	}
	for _, f := range in.Functions {
		if !contains(GlitchFunctions, f) {
			return invalid("unknown function %q", f)
		}
	}
	if in.Exclusion != "" && glitchExclusion(in.Exclusion).ID == "" {
		return invalid("unknown exclusion %q", in.Exclusion)
	}
	if in.Exclusion == "other" && len(strings.TrimSpace(in.ExclusionNote)) < 5 {
		return invalid("say which exclusion of the circular it falls under")
	}
	for _, x := range in.Exchanges {
		if !contains([]string{"NSE", "BSE", "MCX", "MSE", "NCDEX"}, x) {
			return invalid("unknown exchange %q", x)
		}
	}
	if in.Clients < 0 {
		return invalid("the clients affected cannot be negative")
	}
	st, err := parseISTTime(in.Started)
	if err != nil {
		return invalid("give when the glitch began as a date and time")
	}
	if st.After(now.Add(time.Minute)) {
		return invalid("the start is in the future")
	}
	no := st
	if in.Noticed != "" {
		if no, err = parseISTTime(in.Noticed); err != nil || no.Before(st) || no.After(now.Add(time.Minute)) {
			return invalid("the time it was noticed is after the start and not in the future")
		}
	}
	end := ""
	if in.Ended != "" {
		e, err := parseISTTime(in.Ended)
		if err != nil || e.Before(st) || e.After(now.Add(time.Minute)) {
			return invalid("the end is after the start and not in the future")
		}
		end = e.In(ist).Format(time.RFC3339)
	}
	g.Title, g.Description, g.System, g.Vendor = trim(in.Title, 200), trim(strings.TrimSpace(in.Description), 4000), trim(in.System, 120), trim(strings.TrimSpace(in.Vendor), 120)
	g.Functions, g.Kind, g.OutsideHours, g.Exclusion, g.ExclusionNote = in.Functions, in.Kind, in.OutsideHours, in.Exclusion, trim(strings.TrimSpace(in.ExclusionNote), 300)
	g.Started, g.Noticed, g.Ended = st.In(ist).Format(time.RFC3339), no.In(ist).Format(time.RFC3339), end
	g.Exchanges, g.Clients = in.Exchanges, in.Clients
	for _, p := range []struct {
		dst *string
		v   string
	}{{&g.RootCause, in.RootCause}, {&g.Impact, in.Impact}, {&g.Corrective, in.Corrective}, {&g.Preventive, in.Preventive}} {
		if s := strings.TrimSpace(p.v); s != "" {
			*p.dst = trim(s, 4000)
		}
	}
	if g.Functions == nil {
		g.Functions = []string{}
	}
	if g.Ended != "" && g.Status == GlitchOpen {
		g.Status = GlitchResolved
	}
	return nil
}

// RecordGlitch adds a glitch to the register. Compliance, Risk and Admin.
// The exchanges default to the firm's own.
func (o *Ops) RecordGlitch(ctx context.Context, tenant, actor string, in GlitchInput) (GlitchView, error) {
	if err := o.require(ctx, tenant, actor, "Recording technical glitches", RoleCompliance, RoleRisk); err != nil {
		return GlitchView{}, err
	}
	if err := o.glitchGate(ctx, tenant); err != nil {
		return GlitchView{}, err
	}
	if len(in.Exchanges) == 0 {
		in.Exchanges = o.firmExchanges(ctx, tenant)
	}
	now := o.now()
	g := Glitch{Status: GlitchOpen}
	if err := in.apply(&g, now); err != nil {
		return GlitchView{}, err
	}
	n, err := o.next(ctx, tenant, "glitch", actor)
	if err != nil {
		return GlitchView{}, err
	}
	ts := now.Format(time.RFC3339)
	g.ID, g.Updated, g.UpdatedBy = fmt.Sprintf("TG-%06d", n), ts, actor
	g.Reports, g.Notices = []GlitchReport{}, []GlitchNotice{}
	g.Notes = []IncidentNote{{At: ts, By: actor, Text: firstOf(trim(strings.TrimSpace(in.Note), 2000), "Recorded by "+actor), Status: g.Status}}
	if err := docs.Upsert(ctx, o.Docs, tenant, cGlitch, g.ID, g, actor); err != nil {
		return GlitchView{}, err
	}
	set, _ := o.Settings(ctx, tenant)
	v := glitchView(g, set, now)
	o.log(ctx, tenant, actor, "glitch.recorded", g.ID, map[string]any{"title": g.Title, "reportable": v.Reportable, "minutes": v.Minutes})
	o.glitchWork(ctx, tenant, v, now)
	return v, nil
}

// UpdateGlitch changes a glitch: its end, classification, the clients
// affected, the root cause; or closes it (a note says why).
func (o *Ops) UpdateGlitch(ctx context.Context, tenant, actor, id string, in GlitchInput, status string) (GlitchView, error) {
	if err := o.require(ctx, tenant, actor, "Recording technical glitches", RoleCompliance, RoleRisk); err != nil {
		return GlitchView{}, err
	}
	if status != "" && !contains([]string{GlitchOpen, GlitchResolved, GlitchClosed}, status) {
		return GlitchView{}, invalid("unknown status %q", status)
	}
	now := o.now()
	ts := now.Format(time.RFC3339)
	set, _ := o.Settings(ctx, tenant)
	g, err := upd(ctx, o.Docs, tenant, cGlitch, id, actor, nil, func(g *Glitch) error {
		if g.ID == "" {
			return ErrNotFound
		}
		if g.Status == GlitchClosed && status == "" {
			return fmt.Errorf("%w: the glitch is closed; reopen it to change it", ErrState)
		}
		if in.Title != "" {
			if err := in.apply(g, now); err != nil {
				return err
			}
		}
		n := IncidentNote{At: ts, By: actor, Text: trim(strings.TrimSpace(in.Note), 2000)}
		if status != "" && status != g.Status {
			if status == GlitchClosed {
				v := glitchView(*g, set, now)
				var open []string
				for _, c := range v.Clocks {
					if !c.Done {
						open = append(open, c.Name)
					}
				}
				if len(open) > 0 && len(n.Text) < 10 {
					return invalid("not done yet: %s. Say why it is closed anyway", strings.Join(open, ", "))
				}
				if len(n.Text) < 5 {
					n.Text = "Closed"
				}
				g.ClosedAt = ts
			} else {
				g.ClosedAt = ""
			}
			g.Status, n.Status = status, status
		}
		if n.Text != "" || n.Status != "" {
			g.Notes = append(g.Notes, n)
		}
		g.Updated, g.UpdatedBy = ts, actor
		return nil
	})
	if errors.Is(err, docs.ErrNotFound) {
		return GlitchView{}, ErrNotFound
	}
	if err != nil {
		return GlitchView{}, err
	}
	v := glitchView(g, set, now)
	o.log(ctx, tenant, actor, "glitch.updated", g.ID, map[string]any{"status": g.Status, "reportable": v.Reportable, "minutes": v.Minutes})
	o.glitchWork(ctx, tenant, v, now)
	return v, nil
}

// ReportGlitch records an intimation or report: to NSE (nse), the other
// exchanges (exchanges), the preliminary report (prelim) or the RCA (rca)
// on the Samuhik Prativedan Manch portal.
func (o *Ops) ReportGlitch(ctx context.Context, tenant, actor, id string, r GlitchReport) (GlitchView, error) {
	if err := o.require(ctx, tenant, actor, "Reporting technical glitches", RoleCompliance); err != nil {
		return GlitchView{}, err
	}
	if !contains([]string{"nse", "exchanges", "prelim", "rca"}, r.Stage) {
		return GlitchView{}, invalid("the report is nse, exchanges, prelim or rca")
	}
	if !contains([]string{"Portal", "Email", "Phone", "Other"}, r.How) {
		return GlitchView{}, invalid("say how it was made: Portal, Email, Phone or Other")
	}
	now := o.now()
	at := now
	if r.At != "" {
		t, err := parseISTTime(r.At)
		if err != nil || t.After(now.Add(time.Minute)) {
			return GlitchView{}, invalid("give when it was made, not in the future")
		}
		at = t
	}
	r.Reference, r.Note = trim(strings.TrimSpace(r.Reference), 120), trim(strings.TrimSpace(r.Note), 2000)
	if (r.Stage == "prelim" || r.Stage == "rca") && r.Reference == "" {
		return GlitchView{}, invalid("give the portal's reference number")
	}
	if r.Reference == "" && len(r.Note) < 5 {
		return GlitchView{}, invalid("give the reference, or say whom you told")
	}
	switch r.Stage {
	case "nse":
		r.To = []string{"NSE"}
	case "prelim", "rca":
		r.To = []string{"Samuhik Prativedan Manch"}
	}
	r.At, r.By, r.Recorded = at.In(ist).Format(time.RFC3339), actor, now.Format(time.RFC3339)
	set, _ := o.Settings(ctx, tenant)
	g, err := upd(ctx, o.Docs, tenant, cGlitch, id, actor, nil, func(g *Glitch) error {
		if g.ID == "" {
			return ErrNotFound
		}
		if r.Stage == "exchanges" {
			if len(r.To) == 0 {
				return invalid("say which exchanges were told")
			}
			for _, x := range r.To {
				if !contains(g.Exchanges, x) {
					return invalid("%s is not an exchange this glitch affected", x)
				}
			}
		}
		if at.Before(mustTime(g.Started).Add(-time.Minute)) {
			return invalid("the report is before the glitch began")
		}
		g.Reports = append(g.Reports, r)
		g.Notes = append(g.Notes, IncidentNote{At: r.Recorded, By: actor, Text: fmt.Sprintf("%s to %s by %s at %s%s", glitchStageName(r.Stage), strings.Join(r.To, ", "), strings.ToLower(r.How), at.In(ist).Format("02 Jan 2006 15:04 IST"), map[bool]string{true: ", reference " + r.Reference}[r.Reference != ""])})
		g.Updated, g.UpdatedBy = r.Recorded, actor
		return nil
	})
	if errors.Is(err, docs.ErrNotFound) {
		return GlitchView{}, ErrNotFound
	}
	if err != nil {
		return GlitchView{}, err
	}
	o.log(ctx, tenant, actor, "glitch.reported", g.ID, map[string]any{"stage": r.Stage, "to": r.To, "reference": r.Reference})
	v := glitchView(g, set, now)
	o.glitchWork(ctx, tenant, v, now)
	return v, nil
}

func glitchStageName(s string) string {
	return map[string]string{"nse": "Intimation", "exchanges": "Intimation", "prelim": "Preliminary Incident Report", "rca": "Root Cause Analysis", "clients": "Notice to clients"}[s]
}

// ---- notices to clients ----

const evGlitchNotice = "glitch.notice"

func init() {
	EventVars[evGlitchNotice] = []string{"what", "started", "status"}
	t := func(ch, subj, body, dlt string) Template {
		return Template{Event: evGlitchNotice, Channel: ch, Category: "Service", Subject: subj, Body: body, DLT: dlt, Active: true}
	}
	extraTemplates = append(extraTemplates,
		t(ChEmail, "Trading system issue: {status}", "Dear {name},\n\nSince {started} our trading system has had a problem: {what}\n\nStatus: {status}. We will tell you when it is fixed. To place or close orders in the meantime, call our dealing desk.\n\n{firm}", ""),
		t(ChSMS, "", "Dear {name}, our trading system has a problem since {started}: {what} Status: {status}. Call our dealing desk to place orders. -{firm}", "1107166000000000091"),
		t(ChWhatsApp, "", "{name}, our trading system has a problem since {started}: {what} Status: {status}. Call our dealing desk to place or close orders.", ""),
	)
}

// NoticeGlitch tells clients: through the client messages (channel
// "Client messages": target all, TAG:x or a list of UCCs), or records a
// notice on the website or an app pop-up.
func (o *Ops) NoticeGlitch(ctx context.Context, tenant, actor, id, channel, target string, uccs []string, text, status string) (GlitchView, error) {
	if err := o.require(ctx, tenant, actor, "Telling clients about a technical glitch", RoleCompliance); err != nil {
		return GlitchView{}, err
	}
	text = strings.TrimSpace(text)
	if len(text) < 10 {
		return GlitchView{}, invalid("write what clients are told")
	}
	if !contains([]string{"Client messages", "Website", "App pop-up", "Other"}, channel) {
		return GlitchView{}, invalid("the channel is Client messages, Website, App pop-up or Other")
	}
	g, _, err := docs.Get[Glitch](ctx, o.Docs, tenant, cGlitch, id)
	if err != nil {
		return GlitchView{}, ErrNotFound
	}
	now := o.now()
	n := GlitchNotice{Channel: channel, At: now.Format(time.RFC3339), Text: trim(text, 600), By: actor, Target: target}
	if channel == "Client messages" {
		cs, err := o.ClientMap(ctx, tenant)
		if err != nil {
			return GlitchView{}, err
		}
		var to []string
		switch {
		case target == "all":
			for _, u := range sortedKeys(cs) {
				if cs[u].Status == "Active" {
					to = append(to, u)
				}
			}
		case strings.HasPrefix(target, "TAG:"):
			for _, u := range sortedKeys(cs) {
				if cs[u].Status == "Active" && contains(cs[u].Tags, strings.TrimPrefix(target, "TAG:")) {
					to = append(to, u)
				}
			}
		default:
			n.Target = fmt.Sprintf("%d clients", len(uccs))
			to = uccs
		}
		if len(to) == 0 {
			return GlitchView{}, invalid("no clients to send to")
		}
		n.Results = map[string]int{}
		data := map[string]string{"what": n.Text, "started": mustTime(g.Started).In(ist).Format("02 Jan 15:04"), "status": firstOf(strings.TrimSpace(status), "being fixed")}
		for _, u := range to {
			ms, err := o.sendEvent(ctx, tenant, actor, evGlitchNotice, u, data, "")
			if err != nil {
				n.Results["Not found"]++
				continue
			}
			for _, m := range ms {
				n.Results[m.Status]++
			}
		}
	}
	set, _ := o.Settings(ctx, tenant)
	g, err = upd(ctx, o.Docs, tenant, cGlitch, id, actor, nil, func(x *Glitch) error {
		x.Notices = append(x.Notices, n)
		x.Notes = append(x.Notes, IncidentNote{At: n.At, By: actor, Text: "Clients told (" + channel + "): " + trim(n.Text, 200)})
		x.Updated, x.UpdatedBy = n.At, actor
		return nil
	})
	if err != nil {
		return GlitchView{}, err
	}
	o.log(ctx, tenant, actor, "glitch.notice", id, map[string]any{"channel": channel, "target": n.Target, "results": n.Results})
	v := glitchView(g, set, now)
	o.glitchWork(ctx, tenant, v, now)
	return v, nil
}

// ---- MCX's exemption, and the link to a security incident ----

// exemptionLimit is the lower of 2% of the quarter's average unique
// clients (rounded up) and 2,000.
func exemptionLimit(avg int) int {
	return min(int(math.Ceil(float64(avg)*0.02)), 2000)
}

// GlitchExempt records the evidence for MCX's exemption from the
// financial disincentive (MCX-0120).
func (o *Ops) GlitchExempt(ctx context.Context, tenant, actor, id string, e GlitchExemption) (GlitchView, error) {
	if err := o.require(ctx, tenant, actor, "Reporting technical glitches", RoleCompliance); err != nil {
		return GlitchView{}, err
	}
	if e.QuarterAvg <= 0 || e.Affected < 0 {
		return GlitchView{}, invalid("give the quarter's average unique clients and the unique clients affected")
	}
	e.Evidence = trim(strings.TrimSpace(e.Evidence), 2000)
	if len(e.Evidence) < 10 {
		return GlitchView{}, invalid("say what the count rests on (the logs or audit trail, and who checked it)")
	}
	e.Limit = exemptionLimit(e.QuarterAvg)
	e.Below = e.Affected < e.Limit
	e.Auditor, e.By, e.At = trim(strings.TrimSpace(e.Auditor), 120), actor, o.now().Format(time.RFC3339)
	set, _ := o.Settings(ctx, tenant)
	g, err := upd(ctx, o.Docs, tenant, cGlitch, id, actor, nil, func(g *Glitch) error {
		if g.ID == "" {
			return ErrNotFound
		}
		g.Exemption, g.Clients = &e, e.Affected
		g.Notes = append(g.Notes, IncidentNote{At: e.At, By: actor, Text: fmt.Sprintf("Exemption evidence: %d unique clients affected against the limit of %d (%s)", e.Affected, e.Limit, map[bool]string{true: "below: the exemption can be claimed", false: "not below: no exemption"}[e.Below])})
		return nil
	})
	if errors.Is(err, docs.ErrNotFound) {
		return GlitchView{}, ErrNotFound
	}
	if err != nil {
		return GlitchView{}, err
	}
	o.log(ctx, tenant, actor, "glitch.exemption", id, map[string]any{"affected": e.Affected, "limit": e.Limit, "below": e.Below})
	return glitchView(g, set, o.now()), nil
}

// LinkGlitchIncident links the glitch to a security incident (an attack
// that also stopped trading): an existing one, or a new one opened from
// the glitch. Each keeps its own clock.
func (o *Ops) LinkGlitchIncident(ctx context.Context, tenant, actor, id, incident, severity string) (GlitchView, error) {
	if err := o.require(ctx, tenant, actor, "Recording and reporting security incidents", RoleCompliance); err != nil {
		return GlitchView{}, err
	}
	g, _, err := docs.Get[Glitch](ctx, o.Docs, tenant, cGlitch, id)
	if err != nil {
		return GlitchView{}, ErrNotFound
	}
	if incident == "" {
		iv, err := o.RecordIncident(ctx, tenant, actor, RecordIncidentInput{Title: "Also technical glitch " + g.ID + ": " + g.Title, Description: g.Description, Severity: firstOf(severity, SevHigh), Kind: "technical glitch", NoticedAt: g.Noticed})
		if err != nil {
			return GlitchView{}, err
		}
		incident = iv.ID
	} else if _, _, err := docs.Get[Incident](ctx, o.Docs, tenant, cIncident, incident); err != nil {
		return GlitchView{}, invalid("no security incident %s", incident)
	}
	ts := o.now().Format(time.RFC3339)
	_, _ = upd(ctx, o.Docs, tenant, cIncident, incident, actor, nil, func(in *Incident) error {
		in.Notes = append(in.Notes, IncidentNote{At: ts, By: actor, Text: "Linked to technical glitch " + g.ID + " (its exchange and client clocks are on Security → Technical glitches)."})
		return nil
	})
	set, _ := o.Settings(ctx, tenant)
	g, err = upd(ctx, o.Docs, tenant, cGlitch, id, actor, nil, func(x *Glitch) error {
		x.Incident = incident
		x.Notes = append(x.Notes, IncidentNote{At: ts, By: actor, Text: "Linked to security incident " + incident + " (its CERT-In clock is on Security → Incidents)."})
		return nil
	})
	if err != nil {
		return GlitchView{}, err
	}
	o.log(ctx, tenant, actor, "glitch.linked", id, map[string]any{"incident": incident})
	return glitchView(g, set, o.now()), nil
}

// ---- the drafts of Annexures B and C ----

// GlitchDraft is the content Ledgerline prepares for the portal's
// Preliminary Incident Report (B) or Root Cause Analysis (C).
func (o *Ops) GlitchDraft(ctx context.Context, tenant, actor, id, which string) ([]byte, string, error) {
	if err := o.require(ctx, tenant, actor, "Reporting technical glitches", RoleCompliance); err != nil {
		return nil, "", err
	}
	if which != "B" && which != "C" {
		return nil, "", invalid("the draft is Annexure B or C")
	}
	g, _, err := docs.Get[Glitch](ctx, o.Docs, tenant, cGlitch, id)
	if err != nil {
		return nil, "", ErrNotFound
	}
	set, _ := o.Settings(ctx, tenant)
	v := glitchView(g, set, o.now())
	f := func(s string) string {
		if s == "" {
			return "—"
		}
		return mustTime(s).In(ist).Format("02 Jan 2006 15:04 IST")
	}
	var b strings.Builder
	w := func(k, val string) {
		fmt.Fprintf(&b, "%-34s %s\n", k+":", firstOf(strings.TrimSpace(val), "(to fill)"))
	}
	title := map[string]string{"B": "Annexure B: Preliminary Incident Report", "C": "Annexure C: Root Cause Analysis"}[which]
	fmt.Fprintf(&b, "%s\nTechnical glitch %s, prepared by Ledgerline from the glitch register on %s.\n", title, g.ID, o.now().In(ist).Format("02 Jan 2006 15:04 IST"))
	b.WriteString("This is the content for the Samuhik Prativedan Manch form. The form on the portal is the authority: copy each item across and add what it asks that is not here.\n\n")
	codes := []string{}
	for _, k := range sortedKeys(set.MemberCodes) {
		codes = append(codes, k+" "+set.MemberCodes[k])
	}
	w("Member", set.Firm)
	w("Member codes", strings.Join(codes, ", "))
	w("SEBI registration", set.SEBIReg)
	w("Glitch reference", g.ID)
	w("Began", f(g.Started))
	w("Noticed", f(g.Noticed))
	w("Ended", f(g.Ended))
	w("Duration", minutesText(v.Minutes))
	w("System affected", g.System)
	w("Outsourced provider", firstOf(g.Vendor, "none (the firm's own)"))
	w("Nature", g.Kind)
	w("Functions affected", strings.Join(g.Functions, ", "))
	w("Exchanges affected", strings.Join(g.Exchanges, ", "))
	w("Unique clients affected", map[bool]string{true: fmt.Sprint(g.Clients)}[g.Clients > 0])
	w("Classification", v.Why)
	w("Description", g.Description)
	b.WriteString("\nIntimations and notices\n")
	for _, r := range g.Reports {
		fmt.Fprintf(&b, "  %s to %s at %s (%s%s)\n", glitchStageName(r.Stage), strings.Join(r.To, ", "), f(r.At), strings.ToLower(r.How), map[bool]string{true: ", ref " + r.Reference}[r.Reference != ""])
	}
	for _, n := range g.Notices {
		fmt.Fprintf(&b, "  Clients told by %s at %s: %s\n", n.Channel, f(n.At), n.Text)
	}
	if which == "C" {
		b.WriteString("\nRoot cause analysis\n")
		w("Root cause", g.RootCause)
		w("Impact", g.Impact)
		w("Corrective action", g.Corrective)
		w("Preventive action", g.Preventive)
		if g.Incident != "" {
			w("Linked security incident", g.Incident)
		}
	}
	b.WriteString("\nTimeline (from the register)\n")
	for _, n := range g.Notes {
		fmt.Fprintf(&b, "  %s  %s: %s\n", f(n.At), n.By, n.Text)
	}
	o.log(ctx, tenant, actor, "glitch.draft", id, map[string]any{"annexure": which})
	return []byte(b.String()), fmt.Sprintf("%s-annexure-%s.txt", strings.ToLower(g.ID), strings.ToLower(which)), nil
}

// ---- work items ----

// glitchWork puts each clock not met on the compliance officer's queue,
// and closes the items once met.
func (o *Ops) glitchWork(ctx context.Context, tenant string, v GlitchView, now time.Time) {
	today := now.In(ist).Format("2006-01-02")
	stages := map[string]bool{}
	for _, c := range v.Clocks {
		stages[c.Stage] = true
		k := v.ID + "|" + c.Stage
		if c.Done || v.Status == GlitchClosed {
			o.closeWork(ctx, tenant, SystemActor, "glitch", k, map[bool]string{true: "done", false: "glitch closed"}[c.Done])
			continue
		}
		o.raiseWork(ctx, tenant, "glitch", k, fmt.Sprintf("Glitch %s: %s by %s", v.ID, c.Name, c.DueIST),
			v.Title+". "+v.Why+". "+c.Rule+".", "", RoleCompliance, "glitches", today)
	}
	if v.Reportable && !v.OnCalendar && v.Status != GlitchClosed {
		// The calendar's event duties (CC-T-GLITCH-*): best effort, the
		// register's own clocks are the ones that count.
		st := mustTime(v.Started).In(ist)
		for _, id := range []string{"CC-T-GLITCH-INTIMATE", "CC-T-GLITCH-PRELIM", "CC-T-GLITCH-RCA"} {
			_, _ = o.RecordDutyEvent(ctx, tenant, SystemActor, today, id, DutyEventReq{Date: st.Format("2006-01-02"), Time: st.Format("15:04"), What: "Technical glitch " + v.ID + ": " + trim(v.Title, 200)})
		}
		_, _ = upd(ctx, o.Docs, tenant, cGlitch, v.ID, SystemActor, nil, func(g *Glitch) error { g.OnCalendar = true; return nil })
	}
	if !v.Reportable {
		for _, s := range []string{"nse", "exchanges", "clients", "prelim", "rca"} {
			o.closeWork(ctx, tenant, SystemActor, "glitch", v.ID+"|"+s, "not reportable")
		}
	}
}

// glitchTick, every 15 minutes: an open glitch passing 5 minutes becomes
// reportable and its clocks go on the queue.
func (o *Ops) glitchTick(ctx context.Context, tenant string, now time.Time) {
	xs, err := docs.List[Glitch](ctx, o.Docs, tenant, cGlitch, "")
	if err != nil || len(xs) == 0 {
		return
	}
	set, _ := o.Settings(ctx, tenant)
	for _, x := range xs {
		if x.Status == GlitchClosed {
			continue
		}
		o.glitchWork(ctx, tenant, glitchView(x, set, now), now)
	}
}
