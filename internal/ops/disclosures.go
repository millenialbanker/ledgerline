package ops

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
)

// ---- exchange disclosures: AI & ML systems and SaaS use ----
//
// Stockbrokers tell the exchanges which AI and ML systems they use (SEBI
// circular SEBI/HO/MIRSD/DOS2/CIR/P/2019/10 of 4 January 2019: originally
// quarterly within 15 days of the quarter's end; NSE and MCX now collect
// it half-yearly for members trading through algorithms, April–September
// and October–March, within 15 days, and yearly for other members) and
// how they use SaaS (SEBI's advisory on SaaS solutions,
// SEBI/HO/MIRSD2/DOR/CIR/P/2020/221 of 3 November 2020: half-yearly,
// January–June and July–December, by the end of the next month on the
// exchanges' member portals). NSE shares an NSE member's AI & ML filing
// with BSE, MCX and the other exchanges; the firm can file once at NSE.
//
// This prints ready answers for each form from the facts in Ledgerline:
// the firm's settings, the AI inventory and its on/off history and answer
// log, and where Ledgerline is deployed. The compliance officer edits any
// answer, files it on the exchange's portal, copying each answer, and
// marks it submitted with the date and reference (and a screenshot or the
// acknowledgement). Each exchange's filing is a duty in the compliance
// calendar, due and chased until it is marked submitted.

const (
	cDisclosure  = "disclosure"
	cDiscProfile = "discprofile"
	// DiscAIML is the AI & ML systems report; DiscSaaS the SaaS report.
	DiscAIML = "aiml"
	DiscSaaS = "saas"
	// MaxDisclosureEvidence is the largest screenshot or acknowledgement.
	MaxDisclosureEvidence = 10 << 20
)

// discExchangeOrder are the exchanges a broker can file with.
var discExchangeOrder = []string{"NSE", "BSE", "MCX", "NCDEX", "MSE"}

// DisclosureProfile holds what the forms ask that Ledgerline cannot know.
type DisclosureProfile struct {
	Category           string `json:"category"` // e.g. Stock broker
	InfoSecName        string `json:"infosec_name"`
	InfoSecDesignation string `json:"infosec_designation"`
	InfoSecEmail       string `json:"infosec_email"`
	InfoSecPhone       string `json:"infosec_phone"`
	// AIMLCycle: half-yearly (members trading through algorithms, the
	// default), yearly (other members) or quarterly (the 2019 circular).
	AIMLCycle string `json:"aiml_cycle"`
	// SingleFiling: an NSE member files the AI & ML report at NSE only,
	// which shares it with the other exchanges.
	SingleFiling bool `json:"single_filing"`
	// Exchanges overrides the exchanges filed with (default: those the
	// firm has a member code for).
	Exchanges []string  `json:"exchanges"`
	SaaSGRC   bool      `json:"saas_grc"` // a SaaS-based GRC solution is used
	GRCTool   string    `json:"grc_tool,omitempty"`
	OtherSaaS []SaaSRow `json:"other_saas"` // SaaS used outside Ledgerline
	By        string    `json:"by,omitempty"`
	At        string    `json:"at,omitempty"`
}

// SaaSRow is one SaaS service on the SaaS report.
type SaaSRow struct {
	Key         string `json:"key,omitempty"`
	Service     string `json:"service"`
	Provider    string `json:"provider"`
	Nature      string `json:"nature"`
	Environment string `json:"environment"`
	GeoLocation string `json:"geo_location"`
	Gaps        string `json:"gaps"`
	Deadline    string `json:"deadline"`
}

// DiscPeriod is one reporting period.
type DiscPeriod struct {
	ID    string `json:"id"` // 2026-04_2026-09
	From  string `json:"from"`
	To    string `json:"to"`
	Due   string `json:"due"`
	Label string `json:"label"`
	FY    string `json:"fy"`
}

// DisclosureField is one answer on a form.
type DisclosureField struct {
	Key       string `json:"key"`
	Section   string `json:"section"`
	Label     string `json:"label"`
	Kind      string `json:"kind"` // yn, ynna, text
	Value     string `json:"value"`
	Generated string `json:"generated"`
	Edited    bool   `json:"edited,omitempty"`
	Hint      string `json:"hint,omitempty"`
}

// DiscSubmission is a filing on one exchange's portal.
type DiscSubmission struct {
	Exchange string            `json:"exchange"`
	Date     string            `json:"date"`
	Ref      string            `json:"ref"`
	Note     string            `json:"note,omitempty"`
	FileID   string            `json:"file_id,omitempty"`
	FileName string            `json:"file_name,omitempty"`
	By       string            `json:"by"`
	At       string            `json:"at"`
	Late     bool              `json:"late,omitempty"`
	Answers  []DisclosureField `json:"answers"` // as filed
}

// DisclosureRecord is the stored state of one report for one period.
type DisclosureRecord struct {
	ID          string            `json:"id"`
	Kind        string            `json:"kind"`
	Period      string            `json:"period"`
	Overrides   map[string]string `json:"overrides"`
	Submissions []DiscSubmission  `json:"submissions"`
	EditedBy    string            `json:"edited_by,omitempty"`
	EditedAt    string            `json:"edited_at,omitempty"`
}

// DiscExchange is one exchange's filing of the report.
type DiscExchange struct {
	Exchange  string          `json:"exchange"`
	Duty      string          `json:"duty"`
	Submitted *DiscSubmission `json:"submitted,omitempty"`
}

// DisclosureView is a report ready to file.
type DisclosureView struct {
	Kind       string            `json:"kind"`
	Title      string            `json:"title"`
	Basis      string            `json:"basis"`
	Period     DiscPeriod        `json:"period"`
	Periods    []DiscPeriod      `json:"periods"`
	Answer     string            `json:"answer"` // Yes or No: the form's first question
	Fields     []DisclosureField `json:"fields"`
	Systems    []string          `json:"systems"`
	Notes      []string          `json:"notes"`
	Exchanges  []DiscExchange    `json:"exchanges"`
	Locked     bool              `json:"locked"`
	Status     string            `json:"status"` // Due, Overdue, Upcoming, Submitted, Partly submitted
	Profile    DisclosureProfile `json:"profile"`
	Deployment DeploymentInfo    `json:"deployment"`
}

// DeploymentInfo is where Ledgerline runs for this firm.
type DeploymentInfo struct {
	Deployment string `json:"deployment"`
	Label      string `json:"label"`
	Provider   string `json:"provider"`
	Region     string `json:"region"`
}

var deploymentLabels = map[string]string{"hosted": "Hosted by Ledgerline (SaaS)", "self": "The broker's own server", "own_cloud": "The broker's own cloud account"}

// checkDeployment validates the deployment settings.
func checkDeployment(s *Settings) error {
	s.Deployment = strings.TrimSpace(s.Deployment)
	if s.Deployment != "" && deploymentLabels[s.Deployment] == "" {
		return invalid("deployment must be hosted, self or own_cloud")
	}
	s.HostingProvider, s.HostingRegion = strings.TrimSpace(s.HostingProvider), strings.TrimSpace(s.HostingRegion)
	return nil
}

func (o *Ops) deployment(set Settings) DeploymentInfo {
	d := DeploymentInfo{Deployment: firstOf(set.Deployment, "hosted"), Provider: set.HostingProvider, Region: firstOf(set.HostingRegion, o.Residency.Region)}
	d.Label = deploymentLabels[d.Deployment]
	return d
}

// SaveDeployment records where Ledgerline runs for the firm.
func (o *Ops) SaveDeployment(ctx context.Context, tenant, actor, deployment, provider, region string) (DeploymentInfo, error) {
	if err := o.require(ctx, tenant, actor, "Setting the deployment for exchange disclosures", RoleCompliance); err != nil {
		return DeploymentInfo{}, err
	}
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return DeploymentInfo{}, err
	}
	set.Deployment, set.HostingProvider, set.HostingRegion = deployment, provider, region
	if err := checkDeployment(&set); err != nil {
		return DeploymentInfo{}, err
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cSettings, "main", set, actor); err != nil {
		return DeploymentInfo{}, err
	}
	o.log(ctx, tenant, actor, "settings.deployment", "settings", map[string]any{"deployment": set.Deployment, "provider": set.HostingProvider, "region": set.HostingRegion})
	return o.deployment(set), nil
}

func (o *Ops) discProfile(ctx context.Context, tenant string) DisclosureProfile {
	p, _, _ := docs.GetOr(ctx, o.Docs, tenant, cDiscProfile, "main", DisclosureProfile{})
	if p.Category == "" {
		p.Category = "Stock broker"
	}
	if p.AIMLCycle == "" {
		p.AIMLCycle = "half-yearly"
	}
	if p.Exchanges == nil {
		p.Exchanges = []string{}
	}
	if p.OtherSaaS == nil {
		p.OtherSaaS = []SaaSRow{}
	}
	return p
}

// SaveDisclosureProfile saves the details the forms ask for.
func (o *Ops) SaveDisclosureProfile(ctx context.Context, tenant, actor string, p DisclosureProfile) (DisclosureProfile, error) {
	if err := o.require(ctx, tenant, actor, "Editing exchange disclosures", RoleCompliance); err != nil {
		return p, err
	}
	switch p.AIMLCycle {
	case "", "half-yearly", "quarterly", "yearly":
	default:
		return p, invalid("the AI & ML report is half-yearly, yearly or quarterly")
	}
	var exs []string
	for _, x := range p.Exchanges {
		x = strings.ToUpper(strings.TrimSpace(x))
		if !contains(discExchangeOrder, x) {
			return p, invalid("exchange %q: use %s", x, strings.Join(discExchangeOrder, ", "))
		}
		if !contains(exs, x) {
			exs = append(exs, x)
		}
	}
	p.Exchanges = exs
	if p.InfoSecEmail != "" && !reEmail.MatchString(p.InfoSecEmail) {
		return p, invalid("the information security contact's email is not an email address")
	}
	for i := range p.OtherSaaS {
		r := &p.OtherSaaS[i]
		if strings.TrimSpace(r.Service) == "" || strings.TrimSpace(r.Provider) == "" {
			return p, invalid("each other SaaS service needs its name and provider")
		}
		r.Key = fmt.Sprintf("other-%d", i+1)
	}
	p.By, p.At = actor, o.now().Format(time.RFC3339)
	if err := docs.Upsert(ctx, o.Docs, tenant, cDiscProfile, "main", p, actor); err != nil {
		return p, err
	}
	o.log(ctx, tenant, actor, "disclosure.profile", "main", map[string]any{"aiml_cycle": p.AIMLCycle, "single_filing": p.SingleFiling, "exchanges": p.Exchanges})
	date := o.businessDate(ctx, tenant)
	_, _ = o.Duties(ctx, tenant, date) // the calendar follows the filing exchanges and cycle
	return o.discProfile(ctx, tenant), nil
}

// discExchanges are the exchanges the firm files the report with.
func (o *Ops) discExchanges(set Settings, p DisclosureProfile, kind string) []string {
	exs := p.Exchanges
	if len(exs) == 0 {
		for _, x := range discExchangeOrder {
			if strings.TrimSpace(set.MemberCodes[x]) != "" {
				exs = append(exs, x)
			}
		}
	}
	if kind == DiscAIML && p.SingleFiling && contains(exs, "NSE") {
		return []string{"NSE"}
	}
	return exs
}

// ---- periods ----

func discFY(date string) string {
	y, m := 0, 0
	fmt.Sscanf(date, "%d-%d", &y, &m)
	if m < 4 {
		y--
	}
	return fmt.Sprintf("%d-%02d", y, (y+1)%100)
}

func monLabel(d string) string {
	t, err := time.Parse("2006-01-02", d)
	if err != nil {
		return d
	}
	return t.Format("Jan 2006")
}

// discPeriodsFor are the periods of a report ending on or before date,
// latest first.
func discPeriodsFor(kind, cycle, date string, n int) []DiscPeriod {
	// starts are the months a period starts, and its length in months;
	// due is the days after the period's end it is due (0: end of next month).
	var starts []int
	months, dueDays := 6, 15
	switch {
	case kind == DiscSaaS:
		starts, dueDays = []int{1, 7}, 0
	case cycle == "quarterly":
		starts, months = []int{1, 4, 7, 10}, 3
	case cycle == "yearly":
		starts, months = []int{4}, 12
	default:
		starts = []int{4, 10}
	}
	y := 0
	fmt.Sscanf(date, "%d", &y)
	var out []DiscPeriod
	for yy := y; yy >= y-6 && len(out) < n; yy-- {
		for i := len(starts) - 1; i >= 0 && len(out) < n; i-- {
			from := fmt.Sprintf("%d-%02d-01", yy, starts[i])
			to := addDays(addMonths(from, months), -1)
			if to >= date {
				continue
			}
			due := addDays(to, dueDays)
			if dueDays == 0 {
				due = addDays(addMonths(addDays(to, 1), 1), -1)
			}
			label := monLabel(from)[:3] + "–" + monLabel(to)
			if from[:4] != to[:4] {
				label = monLabel(from) + " – " + monLabel(to)
			}
			out = append(out, DiscPeriod{ID: from[:7] + "_" + to[:7], From: from, To: to, Due: due, Label: label, FY: discFY(to)})
		}
	}
	return out
}

func discPeriodByID(kind, cycle, id, date string) (DiscPeriod, bool) {
	for _, p := range discPeriodsFor(kind, cycle, addDays(date, 1), 24) {
		if p.ID == id {
			return p, true
		}
	}
	return DiscPeriod{}, false
}

// dutyRule is the compliance calendar's due-date rule for a report.
func discDutyRule(kind, cycle string) string {
	switch {
	case kind == DiscSaaS:
		return "01-31,07-31"
	case cycle == "quarterly":
		return "01-15,04-15,07-15,10-15"
	case cycle == "yearly":
		return "04-15"
	}
	return "04-15,10-15"
}

func discFrequency(kind, cycle string) string {
	if kind == DiscAIML && cycle == "quarterly" {
		return "Quarterly"
	}
	if kind == DiscAIML && cycle == "yearly" {
		return "Yearly"
	}
	return "Half-yearly"
}

func discDutyID(kind, exchange string) string { return "OB-" + strings.ToUpper(kind) + "-" + exchange }

func discTitle(kind, cycle string) string {
	if kind == DiscSaaS {
		return "SaaS use report (half-yearly)"
	}
	return "AI & ML systems report (" + cycle + ")"
}

// ensureDisclosureDuties adds the calendar duty for each report and
// exchange the firm files with, and keeps the AI & ML rule on the cycle.
func (o *Ops) ensureDisclosureDuties(ctx context.Context, tenant, date string, obs []Duty) []Duty {
	set, _ := o.Settings(ctx, tenant)
	p := o.discProfile(ctx, tenant)
	have := map[string]int{}
	for i, d := range obs {
		have[d.ID] = i
	}
	for _, kind := range []string{DiscAIML, DiscSaaS} {
		for _, ex := range o.discExchanges(set, p, kind) {
			id, rule, title := discDutyID(kind, ex), discDutyRule(kind, p.AIMLCycle), discTitle(kind, p.AIMLCycle)+" to "+ex
			if i, ok := have[id]; ok {
				if obs[i].Rule != rule || obs[i].Title != title {
					obs[i].Rule, obs[i].Title, obs[i].Frequency = rule, title, discFrequency(kind, p.AIMLCycle)
					_ = docs.Upsert(ctx, o.Docs, tenant, cObligation, id, obs[i], SystemActor)
				}
				continue
			}
			auth := "SEBI / " + ex
			ev := "The exchange portal's acknowledgement: Compliance → Exchange disclosures → Mark submitted"
			d := Duty{ID: id, Title: title, Authority: auth, Frequency: discFrequency(kind, p.AIMLCycle),
				Rule: rule, Role: RoleCompliance, Evidence: ev, Active: true, Since: date, Done: []DutyRun{}}
			if docs.Upsert(ctx, o.Docs, tenant, cObligation, id, d, SystemActor) == nil {
				obs = append(obs, d)
			}
		}
	}
	return obs
}

// ---- building the answers ----

type discBuilder struct {
	fields []DisclosureField
	ov     map[string]string
}

func (b *discBuilder) add(key, section, label, kind, gen, hint string) {
	f := DisclosureField{Key: key, Section: section, Label: label, Kind: kind, Value: gen, Generated: gen, Hint: hint}
	if v, ok := b.ov[key]; ok {
		f.Value, f.Edited = v, v != gen
	}
	b.fields = append(b.fields, f)
}

func discYN(b bool) string {
	if b {
		return "Y"
	}
	return "N"
}

func (o *Ops) discRecord(ctx context.Context, tenant, kind, period string) DisclosureRecord {
	id := kind + "|" + period
	r, _, _ := docs.GetOr(ctx, o.Docs, tenant, cDisclosure, id, DisclosureRecord{ID: id, Kind: kind, Period: period})
	if r.Overrides == nil {
		r.Overrides = map[string]string{}
	}
	if r.Submissions == nil {
		r.Submissions = []DiscSubmission{}
	}
	return r
}

// Disclosure builds a report for a period ("" = the latest ended).
func (o *Ops) Disclosure(ctx context.Context, tenant, actor, kind, period string) (DisclosureView, error) {
	if err := o.require(ctx, tenant, actor, "Viewing exchange disclosures", RoleCompliance); err != nil {
		return DisclosureView{}, err
	}
	return o.disclosure(ctx, tenant, kind, period)
}

func (o *Ops) disclosure(ctx context.Context, tenant, kind, period string) (DisclosureView, error) {
	if kind != DiscAIML && kind != DiscSaaS {
		return DisclosureView{}, invalid("report must be aiml or saas")
	}
	today := o.now().Format("2006-01-02")
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return DisclosureView{}, err
	}
	prof := o.discProfile(ctx, tenant)
	v := DisclosureView{Kind: kind, Profile: prof, Deployment: o.deployment(set), Notes: []string{}, Systems: []string{}, Exchanges: []DiscExchange{}}
	v.Periods = discPeriodsFor(kind, prof.AIMLCycle, today, 8)
	if period == "" {
		v.Period = v.Periods[0]
	} else if p, ok := discPeriodByID(kind, prof.AIMLCycle, period, today); ok {
		v.Period = p
	} else {
		return v, invalid("no %s period %q", kind, period)
	}
	rec := o.discRecord(ctx, tenant, kind, v.Period.ID)
	b := &discBuilder{ov: rec.Overrides}
	if kind == DiscAIML {
		o.buildAIML(ctx, tenant, set, prof, &v, b)
	} else {
		o.buildSaaS(ctx, tenant, set, prof, &v, b)
	}
	v.Fields = b.fields
	v.Locked = len(rec.Submissions) > 0
	done := 0
	for _, ex := range o.discExchanges(set, prof, kind) {
		e := DiscExchange{Exchange: ex, Duty: discDutyID(kind, ex)}
		for i := range rec.Submissions {
			if rec.Submissions[i].Exchange == ex {
				s := rec.Submissions[i]
				e.Submitted = &s
				done++
			}
		}
		v.Exchanges = append(v.Exchanges, e)
	}
	// Filings on exchanges no longer in the list still show.
	for i := range rec.Submissions {
		s := rec.Submissions[i]
		found := false
		for _, e := range v.Exchanges {
			found = found || e.Exchange == s.Exchange
		}
		if !found {
			v.Exchanges = append(v.Exchanges, DiscExchange{Exchange: s.Exchange, Duty: discDutyID(kind, s.Exchange), Submitted: &s})
		}
	}
	switch {
	case len(v.Exchanges) > 0 && done >= len(v.Exchanges):
		v.Status = "Submitted"
	case done > 0:
		v.Status = "Partly submitted"
	case today > v.Period.Due:
		v.Status = "Overdue"
	default:
		v.Status = "Due"
	}
	if len(v.Exchanges) == 0 {
		v.Notes = append(v.Notes, "No exchange to file with: enter the firm's member codes in Settings, or choose the exchanges here.")
	}
	return v, nil
}

func (o *Ops) buildAIML(ctx context.Context, tenant string, set Settings, prof DisclosureProfile, v *DisclosureView, b *discBuilder) {
	v.Title = "AI & ML systems in use"
	v.Basis = "SEBI circular SEBI/HO/MIRSD/DOS2/CIR/P/2019/10 (4 January 2019); filed " + prof.AIMLCycle + " within 15 days of the period's end on the exchange's member portal"
	pan := set.FirmPAN
	if pan == "" && len(set.FirmGSTIN) == 15 {
		pan = set.FirmGSTIN[2:12]
	}
	h := "Header"
	b.add("h.sebi_reg", h, "SEBI registration number", "text", set.SEBIReg, "")
	b.add("h.category", h, "Category of intermediary", "text", prof.Category, "")
	b.add("h.entity", h, "Name of the entity", "text", set.Firm, "")
	b.add("h.pan", h, "PAN of the entity", "text", pan, "")
	b.add("h.year", h, "Financial year", "text", v.Period.FY, "")
	b.add("h.period", h, map[string]string{"quarterly": "Quarter", "yearly": "Year", "half-yearly": "Half-year"}[prof.AIMLCycle], "text", v.Period.Label, "")
	sys, notes := o.aiSystemsDuring(ctx, tenant, v.Period.From, v.Period.To)
	v.Notes = append(v.Notes, notes...)
	anyAI := len(sys) > 0
	b.add("any", "AI & ML systems", "Does the firm use any system that falls under these categories: natural language processing (including chatbots and sentiment analysis), neural networks, supervised or unsupervised machine learning (decision trees, random forest, gradient boosting), statistical heuristics, systems tuned by feedback, knowledge-base systems?", "yn", discYN(anyAI), "")
	v.Answer = map[bool]string{true: "Yes", false: "No"}[anyAI]
	if !anyAI {
		v.Notes = append(v.Notes, "No AI system was in use in the period: file the No answer (no system falls under these categories).")
		return
	}
	for i, s := range sys {
		v.Systems = append(v.Systems, s.Name)
		sec := fmt.Sprintf("System %d: %s", i+1, s.Name)
		k := "s." + s.ID + "."
		b.add(k+"order_execution", sec, "Does the system involve order initiation, routing and execution?", "yn", firstOf(s.OrderExecution, "N"), "")
		b.add(k+"discretionary", sec, "Does it fall under discretionary investment or portfolio management?", "yn", firstOf(s.Discretionary, "N"), "")
		b.add(k+"advice", sec, "Does it disseminate investment or trading advice or strategies?", "yn", firstOf(s.Advice, "N"), map[bool]string{true: "It answers staff questions about operations, from the help articles.", false: ""}[s.ID == AIFeatureLedgy])
		b.add(k+"cyber", sec, "Is it used in cyber security to detect attacks?", "yn", firstOf(s.CyberSecurity, "N"), "")
		b.add(k+"claims", sec, "What claims have been made about the AI/ML application?", "text", s.Claims, "")
		b.add(k+"tool", sec, "Name of the tool or technology", "text", firstOf(s.Tool, s.Name), "")
		b.add(k+"implemented", sec, "How was the project implemented (in-house, vendor…)?", "text", firstOf(s.Implemented, map[bool]string{true: "Vendor (" + s.Vendor + ")", false: "In-house"}[s.Vendor != ""]), "")
		b.add(k+"controls", sec, "Key controls and control points, in line with SEBI's cyber security circulars", "text", strings.Join(s.Controls, " "), "")
		b.add(k+"in_system_audit", sec, "Is the system included in the system audit?", "ynna", firstOf(s.InSystemAudit, "NA"), "Confirm with the firm's system auditor that the system is in the audit's scope.")
		b.add(k+"description", sec, "Describe the application and how it uses AI/ML as portrayed in the product", "text", firstOf(s.Description, s.Purpose), "")
		b.add(k+"safeguards", sec, "Safeguards against abnormal behaviour", "text", s.Safeguards, "")
	}
}

func (o *Ops) buildSaaS(ctx context.Context, tenant string, set Settings, prof DisclosureProfile, v *DisclosureView, b *discBuilder) {
	v.Title = "SaaS use"
	v.Basis = "SEBI's advisory on SaaS solutions (SEBI/HO/MIRSD2/DOR/CIR/P/2020/221, 3 November 2020) and the exchanges' circulars; filed half-yearly by the end of the next month on the exchange's member portal"
	dep := v.Deployment
	contact := strings.Join(nonEmpty(prof.InfoSecName, prof.InfoSecDesignation, prof.InfoSecEmail, prof.InfoSecPhone), ", ")
	h := "Header"
	b.add("h.fy", h, "Financial year", "text", v.Period.FY, "")
	b.add("h.audit_period", h, "Audit period", "text", monLabel(v.Period.From)+" to "+monLabel(v.Period.To), "")
	b.add("h.contact", h, "Information security contact person", "text", contact, map[bool]string{true: "Enter the contact under Details for the forms.", false: ""}[contact == ""])
	var rows []SaaSRow
	region := dep.Region
	switch dep.Deployment {
	case "hosted":
		gaps, deadline := "None identified", "Not applicable"
		switch {
		case region == "":
			gaps, deadline = "Hosting region not declared: confirm data is stored and processed in India", ""
			v.Notes = append(v.Notes, "Enter the hosting provider and region under Deployment.")
		case !InIndia(region):
			gaps, deadline = "Hosted outside India: SEBI expects regulated data to be kept in India", ""
		}
		csp := firstOf(dep.Provider, "Not entered: the cloud provider Ledgerline runs on")
		rows = append(rows, SaaSRow{Key: "ledgerline", Service: "Ledgerline back office", Provider: csp, Nature: "Back-office application",
			Environment: "Production; data exchanged: client KYC, ledger, holdings, trades", GeoLocation: firstOf(region, "Not declared"), Gaps: gaps, Deadline: deadline})
	case "self":
		v.Notes = append(v.Notes, "Ledgerline runs on the broker's own server, so it is not a SaaS service.")
	case "own_cloud":
		v.Notes = append(v.Notes, "Ledgerline runs in the broker's own cloud account (infrastructure the broker controls), so it is not a SaaS service; report the cloud account under the cloud framework.")
	}
	// The AI providers Ledgerline's AI features called or could call.
	provs := map[string][]string{}
	for _, f := range aiFeatures {
		if on, _ := o.aiOnDuring(ctx, tenant, f, v.Period.From, v.Period.To); !on {
			continue
		}
		es, _ := o.aiLogBetween(ctx, tenant, v.Period.From, v.Period.To)
		seen := false
		for _, e := range es {
			if e.Feature == f && e.Provider != "" {
				provs[e.Provider] = appendUnique(provs[e.Provider], f)
				seen = true
			}
		}
		if !seen && o.LedgyAI != nil {
			provs[o.LedgyAI.Provider()] = appendUnique(provs[o.LedgyAI.Provider()], f)
		}
	}
	names := map[string]string{}
	for _, s := range o.ledgerlineAISystems() {
		names[s.ID] = s.Name
	}
	var pk []string
	for p := range provs {
		pk = append(pk, p)
	}
	sort.Strings(pk)
	for _, p := range pk {
		info := aiProviderInfo[p]
		var feats []string
		data := []string{}
		for _, f := range provs[p] {
			feats = append(feats, names[f])
			data = append(data, map[string]string{AIFeatureLedgy: "staff questions with identifiers masked and help article text", AIFeatureBillScan: "images of the firm's own vendor bills"}[f])
		}
		rows = append(rows, SaaSRow{Key: "ai-" + p, Service: info.Name + " API, through Ledgerline (" + strings.Join(feats, "; ") + ")", Provider: info.Company, Nature: "AI model API",
			Environment: "Production; data exchanged: " + strings.Join(data, "; ") + "; no client records", GeoLocation: info.Where,
			Gaps: "Processed outside India; only masked text and the firm's own documents are sent, with a kill switch and an answer log", Deadline: ""})
	}
	// AI tools the firm added that are cloud services.
	if sys, _ := o.aiSystemsDuring(ctx, tenant, v.Period.From, v.Period.To); len(sys) > 0 {
		for _, s := range sys {
			if s.Source != "firm" || !s.SaaS {
				continue
			}
			rows = append(rows, SaaSRow{Key: "tool-" + s.ID, Service: s.Name, Provider: firstOf(s.Vendor, s.Provider), Nature: "AI tool: " + s.Purpose,
				Environment: firstOf(s.DataNature, s.DataSent), GeoLocation: firstOf(s.HostedAt, "Not entered"), Gaps: "", Deadline: ""})
		}
	}
	rows = append(rows, prof.OtherSaaS...)
	b.add("h.hosted", h, "Data hosted on SaaS services?", "yn", discYN(len(rows) > 0), "")
	b.add("h.grc", h, "Use of SaaS-based GRC solutions?", "yn", discYN(prof.SaaSGRC), map[bool]string{true: prof.GRCTool, false: ""}[prof.SaaSGRC])
	v.Answer = map[bool]string{true: "Yes", false: "No"}[len(rows) > 0]
	for i, r := range rows {
		v.Systems = append(v.Systems, r.Service)
		sec := fmt.Sprintf("SaaS %d: %s", i+1, r.Service)
		k := "r." + r.Key + "."
		b.add(k+"service", sec, "SaaS service used", "text", r.Service, "")
		b.add(k+"provider", sec, "Cloud service provider", "text", r.Provider, "")
		b.add(k+"nature", sec, "Nature of service consumed", "text", r.Nature, "")
		b.add(k+"environment", sec, "Environment usage, including the nature of data exchanged", "text", r.Environment, "")
		b.add(k+"geo", sec, "Geo-location of hosting", "text", r.GeoLocation, "")
		b.add(k+"gaps", sec, "Gaps against the circular", "text", r.Gaps, "The compliance officer's assessment against SEBI's SaaS advisory.")
		b.add(k+"deadline", sec, "Deadline to close the gaps", "text", r.Deadline, "")
	}
	if len(rows) == 0 {
		v.Notes = append(v.Notes, "No SaaS service in the period: file the No answer.")
	}
}

func nonEmpty(xs ...string) []string {
	var out []string
	for _, x := range xs {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

func appendUnique(xs []string, x string) []string {
	if contains(xs, x) {
		return xs
	}
	return append(xs, x)
}

// ---- editing and filing ----

// SetDisclosureAnswer changes one answer before the report is filed ("" =
// back to the generated answer).
func (o *Ops) SetDisclosureAnswer(ctx context.Context, tenant, actor, kind, period, key, value string) (DisclosureView, error) {
	if err := o.require(ctx, tenant, actor, "Editing exchange disclosures", RoleCompliance); err != nil {
		return DisclosureView{}, err
	}
	v, err := o.disclosure(ctx, tenant, kind, period)
	if err != nil {
		return v, err
	}
	if v.Locked {
		return v, fmt.Errorf("%w: this report is marked submitted; its answers are kept as filed", ErrState)
	}
	var f *DisclosureField
	for i := range v.Fields {
		if v.Fields[i].Key == key {
			f = &v.Fields[i]
		}
	}
	if f == nil {
		return v, invalid("no answer %q on this report", key)
	}
	value = strings.TrimSpace(value)
	if value != "" {
		switch f.Kind {
		case "yn", "ynna":
			value = strings.ToUpper(value)
			if !ynOK(value, f.Kind == "ynna") {
				return v, invalid("answer %s", map[bool]string{true: "Y, N or NA", false: "Y or N"}[f.Kind == "ynna"])
			}
		}
	}
	id := kind + "|" + v.Period.ID
	rec := o.discRecord(ctx, tenant, kind, v.Period.ID)
	if value == "" || value == f.Generated {
		delete(rec.Overrides, key)
	} else {
		rec.Overrides[key] = value
	}
	rec.EditedBy, rec.EditedAt = actor, o.now().Format(time.RFC3339)
	if err := docs.Upsert(ctx, o.Docs, tenant, cDisclosure, id, rec, actor); err != nil {
		return v, err
	}
	o.log(ctx, tenant, actor, "disclosure.edited", id, map[string]any{"field": key, "reset": value == ""})
	return o.disclosure(ctx, tenant, kind, v.Period.ID)
}

// UploadDisclosureEvidence keeps a screenshot or acknowledgement of a filing.
func (o *Ops) UploadDisclosureEvidence(ctx context.Context, tenant, actor, name string, data []byte) (PeriodicFile, error) {
	if err := o.require(ctx, tenant, actor, "Filing exchange disclosures", RoleCompliance); err != nil {
		return PeriodicFile{}, err
	}
	if len(data) == 0 || len(data) > MaxDisclosureEvidence {
		return PeriodicFile{}, invalid("the file must be between 1 byte and 10 MB")
	}
	switch ct := http.DetectContentType(data); {
	case ct == "application/pdf", ct == "image/png", ct == "image/jpeg", ct == "image/gif", ct == "image/webp":
	default:
		return PeriodicFile{}, invalid("upload a screenshot (PNG or JPEG) or a PDF (this looks like %s)", ct)
	}
	name = strings.TrimSpace(strings.NewReplacer("/", "_", "\\", "_").Replace(name))
	if name == "" {
		name = "acknowledgement"
	}
	rf, err := o.App.Ingest.Raw.Put(ctx, ingest.RawFile{Tenant: tenant, Source: "DISCLOSURE-EVIDENCE", Name: name, SHA256: sha256Hex(data), Size: int64(len(data)), Content: data, ReceivedAt: o.now()})
	if err != nil {
		return PeriodicFile{}, err
	}
	o.log(ctx, tenant, actor, "disclosure.evidence", rf.ID, map[string]any{"name": name, "sha256": rf.SHA256})
	return PeriodicFile{ID: rf.ID, Name: name}, nil
}

// DisclosureEvidence returns an uploaded screenshot or acknowledgement.
func (o *Ops) DisclosureEvidence(ctx context.Context, tenant, actor, id string) (ingest.RawFile, error) {
	if err := o.require(ctx, tenant, actor, "Viewing exchange disclosures", RoleCompliance); err != nil {
		return ingest.RawFile{}, err
	}
	f, err := o.App.Ingest.Raw.Get(ctx, tenant, id)
	if err != nil {
		return f, err
	}
	if f.Source != "DISCLOSURE-EVIDENCE" {
		return ingest.RawFile{}, fmt.Errorf("%w: file %s", ErrNotFound, id)
	}
	return f, nil
}

// MarkDisclosureSubmitted records the filing on one exchange's portal and
// closes that exchange's duty for the period in the compliance calendar.
func (o *Ops) MarkDisclosureSubmitted(ctx context.Context, tenant, actor, kind, period string, s DiscSubmission) (DisclosureView, error) {
	if err := o.require(ctx, tenant, actor, "Filing exchange disclosures", RoleCompliance); err != nil {
		return DisclosureView{}, err
	}
	v, err := o.disclosure(ctx, tenant, kind, period)
	if err != nil {
		return v, err
	}
	s.Exchange = strings.ToUpper(strings.TrimSpace(s.Exchange))
	if !contains(discExchangeOrder, s.Exchange) {
		return v, invalid("exchange must be one of %s", strings.Join(discExchangeOrder, ", "))
	}
	if !validDate(s.Date) {
		return v, invalid("give the date it was submitted (YYYY-MM-DD)")
	}
	if s.Date < v.Period.To {
		return v, invalid("the period ends on %s; a filing for it cannot be dated %s", v.Period.To, s.Date)
	}
	if s.Ref = strings.TrimSpace(s.Ref); s.Ref == "" {
		return v, invalid("give the reference or acknowledgement number from the exchange portal")
	}
	if s.FileID != "" {
		f, err := o.App.Ingest.Raw.Get(ctx, tenant, s.FileID)
		if err != nil || f.Source != "DISCLOSURE-EVIDENCE" {
			return v, invalid("the uploaded file %s was not found", s.FileID)
		}
		s.FileName = f.Name
	}
	for _, e := range v.Exchanges {
		if e.Exchange == s.Exchange && e.Submitted != nil {
			return v, fmt.Errorf("%w: already marked submitted to %s on %s (reference %s)", ErrState, s.Exchange, e.Submitted.Date, e.Submitted.Ref)
		}
	}
	now := o.now().Format(time.RFC3339)
	s.By, s.At, s.Late, s.Answers, s.Note = actor, now, s.Date > v.Period.Due, v.Fields, strings.TrimSpace(s.Note)
	id := kind + "|" + v.Period.ID
	rec := o.discRecord(ctx, tenant, kind, v.Period.ID)
	rec.Submissions = append(rec.Submissions, s)
	if err := docs.Upsert(ctx, o.Docs, tenant, cDisclosure, id, rec, actor); err != nil {
		return v, err
	}
	o.log(ctx, tenant, actor, "disclosure.submitted", id, map[string]any{"exchange": s.Exchange, "date": s.Date, "ref": s.Ref, "answer": v.Answer, "late": s.Late})
	// The calendar duty for this exchange and period is done.
	dutyID := discDutyID(kind, s.Exchange)
	run := DutyRun{Period: v.Period.Due, Due: v.Period.Due, At: now, By: actor, Evidence: "Submitted to " + s.Exchange + " on " + s.Date + ", reference " + s.Ref + strings.TrimSpace(" "+s.FileName), Late: s.Late}
	if _, err := docs.Update(ctx, o.Docs, tenant, cObligation, dutyID, actor, func(d *Duty) error {
		for _, r := range d.Done {
			if r.Period == run.Period {
				return nil
			}
		}
		d.Done = append(d.Done, run)
		return nil
	}); err == nil {
		o.closeWork(ctx, tenant, actor, "compliance", dutyID+"|"+run.Period, "done")
	}
	return o.disclosure(ctx, tenant, kind, v.Period.ID)
}

// DisclosureCSV exports a report's answers and its filings.
func (o *Ops) DisclosureCSV(ctx context.Context, tenant, actor, kind, period string) (string, []byte, error) {
	v, err := o.Disclosure(ctx, tenant, actor, kind, period)
	if err != nil {
		return "", nil, err
	}
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	w.Write([]string{"Section", "Question", "Answer", "Edited"})
	for _, f := range v.Fields {
		w.Write([]string{f.Section, f.Label, csvSafe(f.Value), map[bool]string{true: "Edited", false: ""}[f.Edited]})
	}
	for _, e := range v.Exchanges {
		if s := e.Submitted; s != nil {
			w.Write([]string{"Filing", "Submitted to " + e.Exchange, csvSafe(s.Date + " · reference " + s.Ref + " · by " + s.By + strings.TrimSpace(" · "+s.FileName)), ""})
		} else {
			w.Write([]string{"Filing", "Submitted to " + e.Exchange, "Not yet", ""})
		}
	}
	w.Flush()
	o.log(ctx, tenant, actor, "disclosure.exported", kind+"|"+v.Period.ID, nil)
	return fmt.Sprintf("%s_disclosure_%s.csv", kind, v.Period.ID), b.Bytes(), w.Error()
}

// DisclosuresScreen is the Exchange disclosures tab: both reports for
// their latest periods, the details the forms need and the deployment.
type DisclosuresScreen struct {
	AIML       DisclosureView    `json:"aiml"`
	SaaS       DisclosureView    `json:"saas"`
	Profile    DisclosureProfile `json:"profile"`
	Deployment DeploymentInfo    `json:"deployment"`
	Exchanges  []string          `json:"exchanges"`
	Member     []string          `json:"member_of"`
}

// Disclosures returns the screen; aiml and saas pick the periods.
func (o *Ops) Disclosures(ctx context.Context, tenant, actor, aimlPeriod, saasPeriod string) (DisclosuresScreen, error) {
	if err := o.require(ctx, tenant, actor, "Viewing exchange disclosures", RoleCompliance); err != nil {
		return DisclosuresScreen{}, err
	}
	var s DisclosuresScreen
	var err error
	if s.AIML, err = o.disclosure(ctx, tenant, DiscAIML, aimlPeriod); err != nil {
		return s, err
	}
	if s.SaaS, err = o.disclosure(ctx, tenant, DiscSaaS, saasPeriod); err != nil {
		return s, err
	}
	set, _ := o.Settings(ctx, tenant)
	s.Profile, s.Deployment, s.Exchanges = o.discProfile(ctx, tenant), o.deployment(set), discExchangeOrder
	s.Member = o.discExchanges(set, DisclosureProfile{}, DiscSaaS)
	return s, nil
}
