package ops

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
)

// ---- impersonation of the firm: the register and the monthly reports ----
//
// Someone using the firm's name, an officer's name or the name of its
// research analyst or investment adviser business on a website, an app or a
// social media handle is acted on and reported:
//
//   - research analysts: BSE notice 20250804-59 (4 Aug 2025), updating
//     20241219-40: (a) file an FIR with the police (including the cyber
//     police) and get the app, site or post taken down, enclosing the FIR
//     on SCORES when the case came from a SCORES complaint; (b) take it up
//     with the platform, app store or authority (Apple, Google, MeitY);
//     (c) show it on the website's home page, naming the alleged entity and
//     the police station where the FIR was filed; (d) tell clients how to
//     check an offer is genuine. The details and number of such complaints
//     are reported to BSE monthly, within 7 days of the month's end, on the
//     Membership Portal (RAIA Inspection → Report Impersonation), from 7
//     Aug 2025;
//   - investment advisers: BSE notice 20250804-58 (4 Aug 2025), the same,
//     updating 20241219-41; its user manual takes up to 5 bank accounts and
//     files of up to 5 MB on a case;
//   - the RA and IA master circulars of 6 Feb 2026 (Annexures E and C):
//     the monthly complaints table has a line for complaints received
//     because of impersonation (helpdesk.go);
//   - depository participants: NSDL/POLICY/2024/0048 (18 Apr 2024), on
//     SEBI's strategy against scams impersonating intermediaries: monitor
//     the web and social media, have impersonating apps, sites and posts
//     taken down by police complaints and public notices, caution all
//     clients by email or SMS, and report each instance to NSDL
//     (dp_igreport@nsdl.com) by the 10th of the following month with the
//     DP ID, DP name, particulars, action taken and status; no nil report.
//     CDSL's communiqué DP2024-234 (30 Apr 2024) was not read: its mode is
//     taken to be the same, to confirm.
//
// The notices do not list the fields of BSE's form, and its user manual
// could not be read: the report Ledgerline makes carries everything the
// notices ask for, field by field, to copy into the form.

const (
	cImpCase   = "impcase"   // key: IMP-nnnnnn
	cImpReport = "impreport" // key: BODY|YYYY-MM
	cRAIASet   = "raiaset"   // "main"
	// MaxImpEvidence is the largest file kept with a case: BSE's Report
	// Impersonation form takes files of up to 5 MB.
	MaxImpEvidence = 5 << 20
	// impActDays is how soon Ledgerline expects the steps on a case (its own
	// practice; the notices give no number of days).
	impActDays = 7
)

// Where an impersonation was found, and how.
var (
	ImpWhere            = []string{"Website", "Mobile app", "Social media", "Messaging group", "Other"}
	ImpFoundBy          = []string{"Our own monitoring", "Investor complaint", "SCORES complaint", "Exchange or depository", "Other"}
	ImpTakedownStatuses = []string{"Requested", "Taken down", "Refused", "No reply"}
	ImpAlertBy          = []string{"Email", "SMS", "Email and SMS", "App notification"}
)

// ImpFIR is the police complaint.
type ImpFIR struct {
	PoliceStation string `json:"police_station,omitempty"`
	Number        string `json:"number,omitempty"`
	Date          string `json:"date,omitempty"`
	PortalRef     string `json:"portal_ref,omitempty"` // the cyber crime portal's acknowledgement, if filed there
}

func (f ImpFIR) filed() bool { return f.PoliceStation != "" && f.Number != "" && f.Date != "" }

// ImpTakedown is one request to a platform, app store or authority.
type ImpTakedown struct {
	Platform    string `json:"platform"`
	RequestedOn string `json:"requested_on"`
	Ref         string `json:"ref,omitempty"`
	Status      string `json:"status"`
	StatusOn    string `json:"status_on,omitempty"`
	Note        string `json:"note,omitempty"`
}

// ImpNotice is the notice on the website's home page.
type ImpNotice struct {
	On   string `json:"on,omitempty"`
	URL  string `json:"url,omitempty"`
	Text string `json:"text,omitempty"`
}

// ImpAlert is the caution sent to clients.
type ImpAlert struct {
	On      string `json:"on,omitempty"`
	By      string `json:"by,omitempty"`
	Clients int    `json:"clients,omitempty"`
	Note    string `json:"note,omitempty"`
}

// ImpBank is an account the impersonator took money in.
type ImpBank struct {
	Bank    string `json:"bank"`
	Account string `json:"account"`
	IFSC    string `json:"ifsc,omitempty"`
	Holder  string `json:"holder,omitempty"`
}

// ImpCase is one case on the register.
type ImpCase struct {
	ID string `json:"id"`
	// Businesses are the firm's businesses impersonated (Broking, DP, RA,
	// IA); Who says who or what, in words.
	Businesses   []string       `json:"businesses"`
	Who          string         `json:"who"`
	FoundOn      string         `json:"found_on"`
	FoundBy      string         `json:"found_by"`
	Where        string         `json:"where"`
	Platform     string         `json:"platform,omitempty"`
	Handle       string         `json:"handle"` // the link, handle, app or group name
	Impersonator string         `json:"impersonator,omitempty"`
	Details      string         `json:"details"`
	FIR          ImpFIR         `json:"fir"`
	Takedowns    []ImpTakedown  `json:"takedowns"`
	Notice       ImpNotice      `json:"notice"`
	Alert        ImpAlert       `json:"alert"`
	Banks        []ImpBank      `json:"banks,omitempty"`
	Complaints   []string       `json:"complaints"`
	Evidence     []PeriodicFile `json:"evidence"`
	Status       string         `json:"status"` // Open, Closed
	ClosedOn     string         `json:"closed_on,omitempty"`
	By           string         `json:"by"`
	At           string         `json:"at"`
	UpdatedBy    string         `json:"updated_by,omitempty"`
	UpdatedAt    string         `json:"updated_at,omitempty"`
	Missing      []string       `json:"missing,omitempty"` // worked out when listed
}

// missing lists the steps not yet recorded.
func (c ImpCase) missing() []string {
	var out []string
	if !c.FIR.filed() {
		out = append(out, "the FIR")
	}
	if len(c.Takedowns) == 0 {
		out = append(out, "a takedown request")
	}
	if c.Notice.On == "" {
		out = append(out, "the notice on the website")
	}
	if c.Alert.On == "" {
		out = append(out, "the alert to clients")
	}
	return out
}

func (c ImpCase) concerns(biz string) bool { return contains(c.Businesses, biz) }

// RAIASettings are what the reports need that Ledgerline does not hold
// elsewhere, and the choice on the RA and IA tables.
type RAIASettings struct {
	RANo   string `json:"ra_no,omitempty"`      // SEBI registration as research analyst (INH…)
	IANo   string `json:"ia_no,omitempty"`      // as investment adviser (INA…)
	NSDLDP string `json:"nsdl_dp_id,omitempty"` // the NSDL DP ID (IN3…)
	CDSLDP string `json:"cdsl_dp_id,omitempty"` // the CDSL DP ID; the CDSL connection's when empty
	DPName string `json:"dp_name,omitempty"`    // the firm's name in Settings when empty
	// AdjustImpersonation leaves complaints received because of
	// impersonation out of the RA and IA tables (the annexures' note
	// allows it); the impersonation line still counts them.
	AdjustImpersonation bool   `json:"adjust_impersonation,omitempty"`
	By                  string `json:"by,omitempty"`
	At                  string `json:"at,omitempty"`
}

func (o *Ops) raiaSettings(ctx context.Context, tenant string) RAIASettings {
	s, _, _ := docs.GetOr(ctx, o.Docs, tenant, cRAIASet, "main", RAIASettings{})
	return s
}

// RAIASettingsFor shows the settings.
func (o *Ops) RAIASettingsFor(ctx context.Context, tenant, actor string) (RAIASettings, error) {
	if err := o.require(ctx, tenant, actor, "Viewing the impersonation register", RoleMaker, RoleChecker, RoleCompliance, RoleRisk); err != nil {
		return RAIASettings{}, err
	}
	return o.raiaSettings(ctx, tenant), nil
}

// SaveRAIASettings saves them (compliance officer).
func (o *Ops) SaveRAIASettings(ctx context.Context, tenant, actor string, s RAIASettings) (RAIASettings, error) {
	if err := o.require(ctx, tenant, actor, "Changing the RA and IA reporting settings", RoleCompliance); err != nil {
		return s, err
	}
	up := func(v string) string { return strings.ToUpper(strings.TrimSpace(v)) }
	s.RANo, s.IANo, s.NSDLDP, s.CDSLDP, s.DPName = up(s.RANo), up(s.IANo), up(s.NSDLDP), up(s.CDSLDP), clip(strings.TrimSpace(s.DPName), 120)
	for _, x := range []struct{ v, pre, what string }{{s.RANo, "INH", "research analyst"}, {s.IANo, "INA", "investment adviser"}} {
		if x.v != "" && (!strings.HasPrefix(x.v, x.pre) || len(x.v) < 6 || len(x.v) > 15 || strings.Trim(x.v[3:], "0123456789") != "") {
			return s, invalid("the %s registration number is %s followed by digits", x.what, x.pre)
		}
	}
	if s.NSDLDP != "" && (!strings.HasPrefix(s.NSDLDP, "IN") || len(s.NSDLDP) != 8) {
		return s, invalid("the NSDL DP ID is IN and 6 characters, for example IN300000")
	}
	if s.CDSLDP != "" && (len(s.CDSLDP) != 8 || strings.Trim(s.CDSLDP, "0123456789") != "") {
		return s, invalid("the CDSL DP ID is 8 digits")
	}
	s.By, s.At = actor, o.now().Format(time.RFC3339)
	if err := docs.Upsert(ctx, o.Docs, tenant, cRAIASet, "main", s, actor); err != nil {
		return s, err
	}
	o.log(ctx, tenant, actor, "raia.settings", "main", map[string]any{"ra": s.RANo, "ia": s.IANo, "nsdl_dp": s.NSDLDP, "cdsl_dp": s.CDSLDP, "adjust_impersonation": s.AdjustImpersonation})
	return s, nil
}

// ---- the register ----

// ImpCases lists the register, newest first.
func (o *Ops) ImpCases(ctx context.Context, tenant, actor string) ([]ImpCase, error) {
	if err := o.require(ctx, tenant, actor, "Viewing the impersonation register", RoleMaker, RoleChecker, RoleCompliance, RoleRisk); err != nil {
		return nil, err
	}
	return o.impCases(ctx, tenant)
}

func (o *Ops) impCases(ctx context.Context, tenant string) ([]ImpCase, error) {
	cs, err := docs.List[ImpCase](ctx, o.Docs, tenant, cImpCase, "")
	for i := range cs {
		cs[i].Missing = cs[i].missing()
	}
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].ID > cs[j].ID })
	if cs == nil {
		cs = []ImpCase{}
	}
	return cs, err
}

// SaveImpCase records a case or changes one (with its ID): the FIR, the
// takedown requests, the notice, the alert to clients, closing it.
func (o *Ops) SaveImpCase(ctx context.Context, tenant, actor string, c ImpCase) (ImpCase, error) {
	if err := o.require(ctx, tenant, actor, "Recording impersonation cases", RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return c, err
	}
	today := o.now().Format("2006-01-02")
	date := func(d, what string, need bool) error {
		if d == "" && !need {
			return nil
		}
		if !validDate(d) || d > today {
			return invalid("give the date %s, not after today (YYYY-MM-DD)", what)
		}
		return nil
	}
	tr := func(s string, n int) string { return clip(strings.Join(strings.Fields(s), " "), n) }
	c.Who, c.Platform, c.Handle, c.Impersonator, c.Details = tr(c.Who, 200), tr(c.Platform, 80), strings.TrimSpace(clip(c.Handle, 300)), tr(c.Impersonator, 200), clip(strings.TrimSpace(c.Details), 2000)
	have := o.ticketBusinesses(ctx, tenant)
	var bs []string
	for _, b := range c.Businesses {
		nb, ok := normBiz(b)
		if !ok || nb == "" || !contains(have, nb) {
			return c, invalid("the business impersonated is one of the firm's: %s", strings.Join(have, ", "))
		}
		bs = appendUnique(bs, nb)
	}
	c.Businesses = bs
	switch {
	case len(c.Businesses) == 0:
		return c, invalid("say which of the firm's businesses was impersonated (%s)", strings.Join(have, ", "))
	case c.Who == "":
		return c, invalid("say who or what was impersonated: the firm's name, its research or advisory name, an officer")
	case date(c.FoundOn, "it was found", true) != nil:
		return c, date(c.FoundOn, "it was found", true)
	case !contains(ImpWhere, c.Where):
		return c, invalid("where it was found is one of %s", strings.Join(ImpWhere, ", "))
	case c.FoundBy != "" && !contains(ImpFoundBy, c.FoundBy):
		return c, invalid("how it was found is one of %s", strings.Join(ImpFoundBy, ", "))
	case c.Handle == "":
		return c, invalid("give the link, handle, app or group name")
	case c.Details == "":
		return c, invalid("say what the impersonator did (offers, tips, an app, payments asked for)")
	case len(c.Banks) > 5:
		return c, invalid("give at most 5 bank accounts (as BSE's form takes)")
	}
	c.FoundBy = firstOf(c.FoundBy, "Our own monitoring")
	c.FIR.PoliceStation, c.FIR.Number, c.FIR.PortalRef = tr(c.FIR.PoliceStation, 120), tr(c.FIR.Number, 60), tr(c.FIR.PortalRef, 60)
	if c.FIR.PoliceStation != "" || c.FIR.Number != "" || c.FIR.Date != "" {
		if c.FIR.PoliceStation == "" || c.FIR.Number == "" {
			return c, invalid("give the FIR's police station and number")
		}
		if err := date(c.FIR.Date, "of the FIR", true); err != nil {
			return c, err
		}
	}
	for i := range c.Takedowns {
		t := &c.Takedowns[i]
		t.Platform, t.Ref, t.Note = tr(t.Platform, 80), tr(t.Ref, 80), tr(t.Note, 300)
		t.Status = firstOf(t.Status, "Requested")
		switch {
		case t.Platform == "":
			return c, invalid("each takedown request names the platform, app store or authority")
		case date(t.RequestedOn, "the takedown was requested", true) != nil:
			return c, date(t.RequestedOn, "the takedown was requested", true)
		case !contains(ImpTakedownStatuses, t.Status):
			return c, invalid("a takedown request is %s", strings.Join(ImpTakedownStatuses, ", "))
		case date(t.StatusOn, "of its status", false) != nil:
			return c, date(t.StatusOn, "of its status", false)
		}
	}
	if err := date(c.Notice.On, "the notice went on the website", false); err != nil {
		return c, err
	}
	if c.Notice.On != "" && strings.TrimSpace(c.Notice.URL) == "" {
		return c, invalid("give the web address of the notice")
	}
	if err := date(c.Alert.On, "clients were alerted", false); err != nil {
		return c, err
	}
	if c.Alert.On != "" && !contains(ImpAlertBy, c.Alert.By) {
		return c, invalid("clients were alerted by %s", strings.Join(ImpAlertBy, ", "))
	}
	if c.Alert.Clients < 0 {
		return c, invalid("the number of clients alerted cannot be below zero")
	}
	for i := range c.Banks {
		b := &c.Banks[i]
		b.Bank, b.Account, b.IFSC, b.Holder = tr(b.Bank, 80), strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(b.Account), " ", "")), strings.ToUpper(strings.TrimSpace(b.IFSC)), tr(b.Holder, 120)
		if b.Bank == "" || b.Account == "" {
			return c, invalid("each bank account needs the bank and the account number")
		}
		if b.IFSC != "" && !reIFSC.MatchString(b.IFSC) {
			return c, invalid("IFSC %s is 4 letters, 0, then 6 characters", b.IFSC)
		}
	}
	c.Status = firstOf(c.Status, "Open")
	switch c.Status {
	case "Open":
		c.ClosedOn = ""
	case "Closed":
		if err := date(c.ClosedOn, "it was closed", true); err != nil {
			return c, err
		}
	default:
		return c, invalid("a case is Open or Closed")
	}
	now := o.now().Format(time.RFC3339)
	c.Missing = nil
	if c.ID == "" {
		n, err := o.next(ctx, tenant, "impcase", actor)
		if err != nil {
			return c, err
		}
		c.ID, c.By, c.At = fmt.Sprintf("IMP-%06d", n), actor, now
		c.Complaints, c.Evidence = []string{}, []PeriodicFile{}
		if c.Takedowns == nil {
			c.Takedowns = []ImpTakedown{}
		}
		if _, err := docs.Put(ctx, o.Docs, tenant, cImpCase, c.ID, c, 0, actor); err != nil {
			return c, err
		}
		o.log(ctx, tenant, actor, "impersonation.recorded", c.ID, map[string]any{"businesses": c.Businesses, "where": c.Where, "found_on": c.FoundOn})
	} else {
		saved, err := docs.Update(ctx, o.Docs, tenant, cImpCase, c.ID, actor, func(x *ImpCase) error {
			c.By, c.At, c.Complaints, c.Evidence = x.By, x.At, x.Complaints, x.Evidence
			c.UpdatedBy, c.UpdatedAt = actor, now
			if c.Takedowns == nil {
				c.Takedowns = []ImpTakedown{}
			}
			*x = c
			return nil
		})
		if err != nil {
			return c, err
		}
		c = saved
		o.log(ctx, tenant, actor, "impersonation.updated", c.ID, map[string]any{"status": c.Status, "fir": c.FIR.filed(), "takedowns": len(c.Takedowns), "notice": c.Notice.On != "", "alert": c.Alert.On != ""})
	}
	c.Missing = c.missing()
	return c, nil
}

// linkImpComplaint keeps a complaint with the case it arose from.
func (o *Ops) linkImpComplaint(ctx context.Context, tenant, actor, id, ticket string) {
	_, _ = docs.Update(ctx, o.Docs, tenant, cImpCase, id, actor, func(c *ImpCase) error {
		c.Complaints = appendUnique(c.Complaints, ticket)
		return nil
	})
}

// UploadImpEvidence keeps a file with a case: the FIR copy, screenshots of
// the impersonation, the takedown requests (PDF, PNG or JPEG up to 5 MB).
func (o *Ops) UploadImpEvidence(ctx context.Context, tenant, actor, id, name string, data []byte) (ImpCase, error) {
	if err := o.require(ctx, tenant, actor, "Recording impersonation cases", RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return ImpCase{}, err
	}
	if len(data) == 0 || len(data) > MaxImpEvidence {
		return ImpCase{}, invalid("the file must be between 1 byte and 5 MB (BSE's limit)")
	}
	switch ct := http.DetectContentType(data); ct {
	case "application/pdf", "image/png", "image/jpeg":
	default:
		return ImpCase{}, invalid("upload a PDF or a PNG or JPEG image (this looks like %s)", ct)
	}
	if _, _, err := docs.Get[ImpCase](ctx, o.Docs, tenant, cImpCase, id); err != nil {
		return ImpCase{}, err
	}
	name = strings.TrimSpace(strings.NewReplacer("/", "_", "\\", "_").Replace(name))
	rf, err := o.App.Ingest.Raw.Put(ctx, ingest.RawFile{Tenant: tenant, Source: "IMPERSONATION-EVIDENCE", Name: firstOf(name, "evidence"), SHA256: sha256Hex(data), Size: int64(len(data)), Content: data, ReceivedAt: o.now()})
	if err != nil {
		return ImpCase{}, err
	}
	c, err := docs.Update(ctx, o.Docs, tenant, cImpCase, id, actor, func(c *ImpCase) error {
		c.Evidence = append(c.Evidence, PeriodicFile{ID: rf.ID, Name: rf.Name})
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "impersonation.evidence", id, map[string]any{"file": rf.ID, "name": rf.Name, "sha256": rf.SHA256})
	}
	c.Missing = c.missing()
	return c, err
}

// ImpEvidence returns a file kept with a case.
func (o *Ops) ImpEvidence(ctx context.Context, tenant, actor, file string) (ingest.RawFile, error) {
	if err := o.require(ctx, tenant, actor, "Viewing the impersonation register", RoleMaker, RoleChecker, RoleCompliance, RoleRisk); err != nil {
		return ingest.RawFile{}, err
	}
	f, err := o.App.Ingest.Raw.Get(ctx, tenant, file)
	if err != nil {
		return f, err
	}
	if f.Source != "IMPERSONATION-EVIDENCE" {
		return ingest.RawFile{}, fmt.Errorf("%w: file %s", ErrNotFound, file)
	}
	return f, nil
}

// ---- the monthly reports ----

// The bodies a month's report goes to.
const (
	ImpBSERA = "BSE-RA"
	ImpBSEIA = "BSE-IA"
	ImpNSDL  = "NSDL"
	ImpCDSL  = "CDSL"
)

var (
	impBodiesRAIA = []string{ImpBSERA, ImpBSEIA}
	impBodiesDP   = []string{ImpNSDL, ImpCDSL}
	impBodyBiz    = map[string]string{ImpBSERA: BizRA, ImpBSEIA: BizIA, ImpNSDL: BizDP, ImpCDSL: BizDP}
	impBodyName   = map[string]string{ImpBSERA: "BSE · research analyst", ImpBSEIA: "BSE · investment adviser", ImpNSDL: "NSDL · depository participant", ImpCDSL: "CDSL · depository participant"}
	impBodyWhere  = map[string]string{ImpBSERA: "BSE's IA/RA Membership Portal → RAIA Inspection → Report Impersonation", ImpBSEIA: "BSE's IA/RA Membership Portal → RAIA Inspection → Report Impersonation",
		ImpNSDL: "Email to dp_igreport@nsdl.com", ImpCDSL: "As CDSL's communiqué DP2024-234 directs (to confirm)"}
)

// impDue is when a body's report for a month is due.
func impDue(body, month string) string {
	day := "07" // within 7 days of the month's end (BSE notices 20250804-58, -59)
	if impBodyBiz[body] == BizDP {
		day = "10" // by the 10th (NSDL/POLICY/2024/0048)
	}
	return addMonths(month+"-01", 1)[:8] + day
}

// impNilNeeded: BSE's notices do not say a nil report is needed, but a nil
// check is recorded each month; NSDL needs no nil report (CDSL taken the
// same, to confirm).
func impNilNeeded(body string) bool { return impBodyBiz[body] != BizDP }

// ImpReport is one month's report to one body, or its nil check.
type ImpReport struct {
	ID          string   `json:"id"` // BODY|YYYY-MM
	Body        string   `json:"body"`
	Month       string   `json:"month"`
	Nil         bool     `json:"nil"`
	Cases       []string `json:"cases"`
	Complaints  int      `json:"complaints"`
	SubmittedOn string   `json:"submitted_on,omitempty"`
	Ref         string   `json:"ref,omitempty"`
	Searched    string   `json:"searched,omitempty"`    // the nil check: where the firm looked
	SearchedOn  string   `json:"searched_on,omitempty"` // and when
	NilEntered  bool     `json:"nil_entered,omitempty"` // a nil entry was also made on the portal
	FileID      string   `json:"file_id,omitempty"`
	FileName    string   `json:"file_name,omitempty"`
	Late        bool     `json:"late,omitempty"`
	By          string   `json:"by"`
	At          string   `json:"at"`
}

// ImpBodyView is one body's report for the month.
type ImpBodyView struct {
	Body       string     `json:"body"`
	Name       string     `json:"name"`
	Where      string     `json:"where"`
	Due        string     `json:"due"`
	RegNo      string     `json:"reg_no"`
	Cases      []ImpCase  `json:"cases"`
	Complaints int        `json:"complaints"`
	NilNeeded  bool       `json:"nil_needed"`
	Recorded   *ImpReport `json:"recorded,omitempty"`
	Status     string     `json:"status"` // Done, Nothing to report, Due, Overdue, Upcoming
	Note       string     `json:"note,omitempty"`
}

// ImpReportsView is the month's reports.
type ImpReportsView struct {
	Month    string        `json:"month"`
	Label    string        `json:"label"`
	Bodies   []ImpBodyView `json:"bodies"`
	Settings RAIASettings  `json:"settings"`
	Notes    []string      `json:"notes"`
}

// impBodies are the bodies of a set the firm reports to: BSE for its RA and
// IA registrations; NSDL and CDSL for its DP (both, while the firm
// profile does not say which).
func (o *Ops) impBodies(ctx context.Context, tenant string, set []string) []string {
	f, _ := o.firmProfileFactsOpt(ctx, tenant, false)
	var out []string
	for _, b := range set {
		switch b {
		case ImpBSERA:
			if f.Regs["ra"] {
				out = append(out, b)
			}
		case ImpBSEIA:
			if f.Regs["ia"] {
				out = append(out, b)
			}
		case ImpNSDL:
			if f.NSDL == triYes || (f.NSDL == triUnknown && f.CDSL != triYes && f.dp() != triNo) {
				out = append(out, b)
			}
		case ImpCDSL:
			if f.CDSL == triYes || (f.CDSL == triUnknown && f.NSDL != triYes && f.dp() != triNo) {
				out = append(out, b)
			}
		}
	}
	return out
}

func (o *Ops) impRegNo(ctx context.Context, tenant, body string, s RAIASettings) string {
	switch body {
	case ImpBSERA:
		return s.RANo
	case ImpBSEIA:
		return s.IANo
	case ImpNSDL:
		return s.NSDLDP
	case ImpCDSL:
		if s.CDSLDP != "" {
			return s.CDSLDP
		}
		if c, ok := o.cdslConnFor(ctx, tenant); ok {
			return c.DPID
		}
	}
	return ""
}

func (o *Ops) impReportsView(ctx context.Context, tenant, month string, set []string) (ImpReportsView, error) {
	if _, err := time.Parse("2006-01", month); err != nil {
		return ImpReportsView{}, invalid("the month is YYYY-MM")
	}
	today := o.now().Format("2006-01-02")
	s := o.raiaSettings(ctx, tenant)
	v := ImpReportsView{Month: month, Label: monthLabel(month), Bodies: []ImpBodyView{}, Settings: s, Notes: []string{}}
	cases, err := o.impCases(ctx, tenant)
	if err != nil {
		return v, err
	}
	ts, _ := docs.List[Ticket](ctx, o.Docs, tenant, cTicket, "")
	start, end := month+"-01", monthEnd(month)
	for _, b := range o.impBodies(ctx, tenant, set) {
		biz := impBodyBiz[b]
		bv := ImpBodyView{Body: b, Name: impBodyName[b], Where: impBodyWhere[b], Due: impDue(b, month), RegNo: o.impRegNo(ctx, tenant, b, s), Cases: []ImpCase{}, NilNeeded: impNilNeeded(b)}
		for _, c := range cases {
			if c.concerns(biz) && c.FoundOn >= start && c.FoundOn <= end {
				bv.Cases = append(bv.Cases, c)
			}
		}
		for _, t := range ts {
			if t.Kind == KindComplaint && t.Impersonation && t.biz() == biz && t.OpenedOn >= start && t.OpenedOn <= end {
				bv.Complaints++
			}
		}
		if r, _, err := docs.Get[ImpReport](ctx, o.Docs, tenant, cImpReport, b+"|"+month); err == nil {
			bv.Recorded = &r
		}
		switch {
		case bv.Recorded != nil:
			bv.Status = "Done"
		case len(bv.Cases) == 0 && bv.Complaints == 0 && !bv.NilNeeded:
			bv.Status = "Nothing to report"
		case today > bv.Due:
			bv.Status = "Overdue"
		case today > end:
			bv.Status = "Due"
		default:
			bv.Status = "Upcoming"
		}
		if bv.RegNo == "" {
			bv.Note = "Enter the " + map[string]string{BizRA: "research analyst registration number", BizIA: "investment adviser registration number", BizDP: "DP ID"}[biz] + " under Reporting details."
		}
		if b == ImpCDSL {
			bv.Note = strings.TrimSpace(bv.Note + " CDSL's communiqué DP2024-234 was not read: confirm with CDSL how it takes the report and whether it wants a nil report.")
		}
		v.Bodies = append(v.Bodies, bv)
	}
	if len(v.Bodies) == 0 {
		v.Notes = append(v.Notes, "Nothing to report to: the firm profile has no research analyst or investment adviser registration, and the firm is not a depository participant.")
	}
	return v, nil
}

// ImpReports is the month's reports to BSE and the depositories.
func (o *Ops) ImpReports(ctx context.Context, tenant, actor, month string) (ImpReportsView, error) {
	if err := o.require(ctx, tenant, actor, "Viewing the impersonation register", RoleMaker, RoleChecker, RoleCompliance, RoleRisk); err != nil {
		return ImpReportsView{}, err
	}
	if month == "" {
		month = prevMonth(o.now().Format("2006-01-02"))
	}
	return o.impReportsView(ctx, tenant, month, append(append([]string{}, impBodiesRAIA...), impBodiesDP...))
}

func impTakedownWords(c ImpCase) string {
	var xs []string
	for _, t := range c.Takedowns {
		x := t.Platform + " on " + fmtDay(t.RequestedOn) + " (" + strings.ToLower(t.Status)
		if t.StatusOn != "" {
			x += " " + fmtDay(t.StatusOn)
		}
		x += ")"
		if t.Ref != "" {
			x += " ref " + t.Ref
		}
		xs = append(xs, x)
	}
	return strings.Join(xs, "; ")
}

func impBankWords(c ImpCase) string {
	var xs []string
	for _, b := range c.Banks {
		xs = append(xs, strings.TrimSpace(strings.Join(nonEmpty(b.Bank, b.Account, b.IFSC, b.Holder), " · ")))
	}
	return strings.Join(xs, "; ")
}

// impReportFile writes a body's report for the month: for BSE the case's
// details as the notices ask for them (the form's own fields could not be
// read: to confirm), for the depositories NSDL's five columns.
func (o *Ops) impReportFile(ctx context.Context, tenant string, bv ImpBodyView, month string) (string, []byte) {
	set, _ := o.Settings(ctx, tenant)
	s := o.raiaSettings(ctx, tenant)
	var rows [][]string
	if impBodyBiz[bv.Body] == BizDP {
		name := firstOf(s.DPName, set.Firm)
		rows = append(rows, []string{"DP ID", "DP Name", "Particulars of instance", "Action Taken", "Status"})
		for _, c := range bv.Cases {
			part := fmt.Sprintf("Found on %s (%s) on %s %s %s: %s", fmtDay(c.FoundOn), strings.ToLower(c.FoundBy), strings.ToLower(c.Where), c.Platform, c.Handle, c.Details)
			if c.Impersonator != "" {
				part += "; name used: " + c.Impersonator
			}
			var acts []string
			if c.FIR.filed() {
				acts = append(acts, "Police complaint (FIR "+c.FIR.Number+") at "+c.FIR.PoliceStation+" on "+fmtDay(c.FIR.Date))
			}
			if w := impTakedownWords(c); w != "" {
				acts = append(acts, "Takedown requested: "+w)
			}
			if c.Notice.On != "" {
				acts = append(acts, "Public notice on the website from "+fmtDay(c.Notice.On)+" ("+c.Notice.URL+")")
			}
			if c.Alert.On != "" {
				acts = append(acts, fmt.Sprintf("Clients cautioned by %s on %s", strings.ToLower(c.Alert.By), fmtDay(c.Alert.On)))
			}
			st := c.Status
			if c.Status == "Closed" {
				st += " on " + fmtDay(c.ClosedOn)
			}
			rows = append(rows, []string{bv.RegNo, name, csvSafe(part), csvSafe(strings.Join(acts, ". ")), st})
		}
		return fmt.Sprintf("Impersonation_%s_%s.csv", bv.Body, month), csvBytesOf(rows)
	}
	rows = append(rows, []string{"Report impersonation for " + monthLabel(month) + " · " + bv.Name + " · registration " + firstOf(bv.RegNo, "(not entered)")},
		[]string{"Number of complaints received during the month due to impersonation", fmt.Sprint(bv.Complaints)}, []string{"Number of cases", fmt.Sprint(len(bv.Cases))}, []string{},
		[]string{"Case", "Date found", "How found", "Where (website, app, social media)", "Platform", "Link or handle", "Name used by the impersonator", "Who was impersonated", "What it did", "Complaints it caused",
			"FIR police station", "FIR number", "FIR date", "Takedown requests", "Notice on the website", "Clients alerted", "Impersonator's bank accounts (up to 5)", "Files kept", "Status"})
	for _, c := range bv.Cases {
		notice := ""
		if c.Notice.On != "" {
			notice = fmtDay(c.Notice.On) + " " + c.Notice.URL
		}
		alert := ""
		if c.Alert.On != "" {
			alert = fmt.Sprintf("%s by %s to %d clients", fmtDay(c.Alert.On), strings.ToLower(c.Alert.By), c.Alert.Clients)
		}
		var files []string
		for _, f := range c.Evidence {
			files = append(files, f.Name)
		}
		rows = append(rows, []string{c.ID, fmtDay(c.FoundOn), c.FoundBy, c.Where, c.Platform, csvSafe(c.Handle), csvSafe(c.Impersonator), csvSafe(c.Who), csvSafe(c.Details), strings.Join(c.Complaints, " "),
			csvSafe(c.FIR.PoliceStation), csvSafe(c.FIR.Number), fmtDay(c.FIR.Date), csvSafe(impTakedownWords(c)), csvSafe(notice), alert, csvSafe(impBankWords(c)), csvSafe(strings.Join(files, "; ")), c.Status})
	}
	return fmt.Sprintf("Impersonation_%s_%s.csv", bv.Body, month), csvBytesOf(rows)
}

// ImpReportFile is a body's report for the month, to copy into the portal
// or attach to the email.
func (o *Ops) ImpReportFile(ctx context.Context, tenant, actor, body, month string) (string, []byte, error) {
	v, err := o.ImpReports(ctx, tenant, actor, month)
	if err != nil {
		return "", nil, err
	}
	for _, bv := range v.Bodies {
		if bv.Body == body {
			name, data := o.impReportFile(ctx, tenant, bv, v.Month)
			return name, data, nil
		}
	}
	return "", nil, invalid("the firm does not report to %s", body)
}

// ImpReportReq records a month's report or nil check.
type ImpReportReq struct {
	Body        string `json:"body"`
	Month       string `json:"month"`
	SubmittedOn string `json:"submitted_on"`
	Ref         string `json:"ref"`
	Searched    string `json:"searched"`
	SearchedOn  string `json:"searched_on"`
	NilEntered  bool   `json:"nil_entered"`
}

// MarkImpReport records the month's report to a body with its
// acknowledgement, or, with no case, the nil check: where the firm looked
// for impersonation and when.
func (o *Ops) MarkImpReport(ctx context.Context, tenant, actor string, req ImpReportReq) (ImpReportsView, error) {
	if err := o.require(ctx, tenant, actor, "Reporting impersonation cases", RoleCompliance); err != nil {
		return ImpReportsView{}, err
	}
	v, err := o.impReportsView(ctx, tenant, req.Month, append(append([]string{}, impBodiesRAIA...), impBodiesDP...))
	if err != nil {
		return v, err
	}
	var bv *ImpBodyView
	for i := range v.Bodies {
		if v.Bodies[i].Body == req.Body {
			bv = &v.Bodies[i]
		}
	}
	today := o.now().Format("2006-01-02")
	okDate := func(d string) bool { return validDate(d) && d <= today && d > monthEnd(req.Month) }
	switch {
	case bv == nil:
		return v, invalid("the firm does not report to %s", req.Body)
	case bv.Recorded != nil:
		return v, fmt.Errorf("%w: %s for %s is already recorded", ErrState, bv.Name, v.Label)
	case o.now().Format("2006-01-02") <= monthEnd(req.Month):
		return v, invalid("%s has not ended", v.Label)
	}
	r := ImpReport{ID: req.Body + "|" + req.Month, Body: req.Body, Month: req.Month, Complaints: bv.Complaints, Cases: []string{}, By: actor, At: o.now().Format(time.RFC3339)}
	for _, c := range bv.Cases {
		r.Cases = append(r.Cases, c.ID)
	}
	if len(bv.Cases) > 0 || bv.Complaints > 0 {
		if !okDate(req.SubmittedOn) || strings.TrimSpace(req.Ref) == "" {
			return v, invalid("give the date it was submitted (after the month's end, not after today) and the acknowledgement or reference")
		}
		r.SubmittedOn, r.Ref = req.SubmittedOn, clip(strings.TrimSpace(req.Ref), 120)
		r.Late = r.SubmittedOn > bv.Due
		name, data := o.impReportFile(ctx, tenant, *bv, req.Month)
		f, err := o.keepFile(ctx, tenant, name, data)
		if err != nil {
			return v, err
		}
		r.FileID, r.FileName = f.ID, f.Name
	} else {
		if strings.TrimSpace(req.Searched) == "" || !validDate(req.SearchedOn) || req.SearchedOn > today {
			return v, invalid("for a month with no case, record the nil check: where the firm looked (web, app stores, social media) and when")
		}
		r.Nil, r.Searched, r.SearchedOn, r.NilEntered = true, clip(strings.TrimSpace(req.Searched), 300), req.SearchedOn, req.NilEntered
		if req.NilEntered {
			if !okDate(req.SubmittedOn) {
				return v, invalid("give the date the nil entry was made on the portal")
			}
			r.SubmittedOn, r.Ref = req.SubmittedOn, clip(strings.TrimSpace(req.Ref), 120)
		}
		r.Late = r.SearchedOn > bv.Due
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cImpReport, r.ID, r, actor); err != nil {
		return v, err
	}
	o.log(ctx, tenant, actor, "impersonation.reported", r.ID, map[string]any{"nil": r.Nil, "cases": len(r.Cases), "complaints": r.Complaints, "ref": r.Ref, "late": r.Late})
	o.closeWork(ctx, tenant, actor, "periodic", "imp:"+req.Month+":"+req.Body, "recorded")
	return o.impReportsView(ctx, tenant, req.Month, append(append([]string{}, impBodiesRAIA...), impBodiesDP...))
}

// impReportsDone is whether the month's reports of a set of bodies are done.
func (o *Ops) impReportsDone(ctx context.Context, tenant, month string, set []string) (bool, string) {
	v, err := o.impReportsView(ctx, tenant, month, set)
	if err != nil {
		return false, err.Error()
	}
	done := true
	var parts []string
	for _, b := range v.Bodies {
		ok := b.Status == "Done" || b.Status == "Nothing to report"
		done = done && ok
		x := b.Name + ": " + strings.ToLower(b.Status)
		if b.Recorded != nil && b.Recorded.Nil {
			x = b.Name + ": nil check recorded"
		}
		parts = append(parts, x)
	}
	return done, strings.Join(parts, "; ")
}

// runImpReports is the monthly task: it makes each body's report and
// queues it (or the nil check) for the compliance officer.
func (o *Ops) runImpReports(ctx context.Context, tenant, month, date string, set []string) (string, []PeriodicFile, error) {
	v, err := o.impReportsView(ctx, tenant, month, set)
	if err != nil {
		return "", nil, err
	}
	var files []PeriodicFile
	var open []string
	for _, b := range v.Bodies {
		if b.Status == "Done" || b.Status == "Nothing to report" {
			continue
		}
		open = append(open, b.Name)
		if len(b.Cases) > 0 || b.Complaints > 0 {
			name, data := o.impReportFile(ctx, tenant, b, month)
			f, err := o.keepFile(ctx, tenant, name, data)
			if err != nil {
				return "", files, err
			}
			files = append(files, f)
			o.raiseWork(ctx, tenant, "periodic", "imp:"+month+":"+b.Body, fmt.Sprintf("Report %d impersonation case%s for %s · %s", len(b.Cases), plural(len(b.Cases)), v.Label, b.Name),
				fmt.Sprintf("%s is on Investor complaints → Impersonation. Enter it on %s by %s and record the acknowledgement there.", name, b.Where, fmtDay(b.Due)), "", RoleCompliance, "helpdesk", date)
			continue
		}
		o.raiseWork(ctx, tenant, "periodic", "imp:"+month+":"+b.Body, "Record the nil check of impersonation for "+v.Label+" · "+b.Name,
			"No case of impersonation is on the register for "+v.Label+". Record where the firm looked (web, app stores, social media) and when, on Investor complaints → Impersonation, by "+fmtDay(b.Due)+".", "", RoleCompliance, "helpdesk", date)
	}
	if len(open) == 0 {
		_, note := o.impReportsDone(ctx, tenant, month, set)
		return firstOf(note, "Nothing to report"), nil, nil
	}
	return "", files, &pendingErr{"Waiting for the report or nil check: " + strings.Join(open, ", "), "Waiting for the report"}
}

// ---- self-inspection ----

// siImpActions: every open case acted on (BSE notices 20250804-58 and -59,
// paras (a) to (d); NSDL/POLICY/2024/0048).
func siImpActions(e *siEnv) (siOut, error) {
	cs, err := e.o.impCases(e.ctx, e.tenant)
	if err != nil {
		return siOut{}, err
	}
	if len(cs) == 0 {
		return siSkip("Nothing to check: no case on the impersonation register.")
	}
	var s siOut
	s.input("Impersonation register", e.date)
	open := 0
	for _, c := range cs {
		if c.Status != "Open" {
			continue
		}
		open++
		s.checked++
		if daysBetween(c.FoundOn, e.date) <= impActDays {
			continue
		}
		if m := c.missing(); len(m) > 0 {
			s.bad(fmt.Sprintf("%s · found %s · without %s", c.ID, fmtDay(c.FoundOn), strings.Join(m, ", ")))
		}
	}
	s.summary = fmt.Sprintf("%d cases on the register, %d open; %d open more than %d days without every step recorded.", len(cs), open, s.found, impActDays)
	return s, nil
}

// siImpReports: the latest month whose due date has passed is reported to
// each body (BSE for the RA and IA; the depository for the DP).
func siImpReports(e *siEnv) (siOut, error) {
	bodies := e.o.impBodies(e.ctx, e.tenant, append(append([]string{}, impBodiesRAIA...), impBodiesDP...))
	if len(bodies) == 0 {
		return siSkip("Not applicable: the firm profile has no research analyst or investment adviser registration, and the firm is not a depository participant.")
	}
	var s siOut
	for _, b := range bodies {
		m := prevMonth(e.date)
		if e.date <= impDue(b, m) {
			m = prevMonth(m + "-01")
		}
		v, err := e.o.impReportsView(e.ctx, e.tenant, m, []string{b})
		if err != nil {
			return s, err
		}
		s.input("Impersonation reports", monthEnd(m))
		for _, bv := range v.Bodies {
			s.checked++
			if bv.Status != "Done" && bv.Status != "Nothing to report" {
				what := "the report"
				if len(bv.Cases) == 0 && bv.Complaints == 0 {
					what = "the nil check"
				}
				s.bad(fmt.Sprintf("%s · %s · %s not recorded (due %s)", bv.Name, v.Label, what, fmtDay(bv.Due)))
			}
		}
	}
	s.summary = fmt.Sprintf("%d monthly report%s checked; %d not recorded by the due date.", s.checked, plural(s.checked), s.found)
	return s, nil
}
