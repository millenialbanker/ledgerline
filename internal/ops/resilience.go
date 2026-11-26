package ops

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
)

// ---- cyber resilience records: restore drills, vendor exit plans, the
// SBOM, the clock source, the review of privileged activity ----
//
//   - Restore drills. CSCRF PR.IP.S7-S8 (documented backup and recovery,
//     periodic restoration drills), the cloud framework s.6.2.12 (backup
//     and recovery tested at least twice a year), RC.RP.S1 (isolated or
//     immutable backups, ransomware drills) and RC.RP.S2 with
//     SEBI/HO/ITD-1/ITD_CSC_EXT/P/CIR/2025/119 para 6.10 (RTO 2 hours, RPO
//     15 minutes for critical systems). The firm records each drill: what
//     was restored and from where, when it started and was back, how
//     recent the restored data was. Ledgerline works out the achieved RTO
//     and RPO and flags a drill more than six months old.
//   - Vendor exit plans. The cloud framework s.9 (lock-in and
//     concentration risk, an exit strategy with data portability) and the
//     stock brokers' master circular 2025/90 para 61.2-61.3 (a workable
//     plan to switch the trading, RMS or back office software vendor;
//     source code escrow, more than one vendor, contract terms on
//     documentation, training, penalties and audit cooperation). One
//     record per technology provider, reviewed every year. Ledgerline's
//     own way out (full export, data exit) is pre-filled for it.
//   - SBOM. CSCRF GV.SC.S5 and NSE/INSP/76768 Annex B 2: a software bill
//     of materials for critical systems, SaaS included. Ledgerline gives
//     its own, in CycloneDX 1.5 JSON, from the modules built into the
//     running server.
//   - Clock source. CERT-In directions of 28 April 2022, direction (i):
//     ICT clocks synchronised to the NIC or NPL time servers. Ledgerline
//     reads the server's time service configuration (chrony, ntpd,
//     systemd-timesyncd) or the operator's LEDGERLINE_NTP_SERVERS, and
//     says which servers it uses. The firm's own systems are its duty.
//   - Privileged activity. CSCRF PR.AA.S11: review privileged users'
//     activity periodically, and privileged users may not review their
//     own. A half-yearly review lists every Admin's actions in the period
//     (from the audit trail); someone other than each Admin signs it off.

const (
	cDrill      = "restoredrill"
	cTechVendor = "techvendor"
	cPrivReview = "privreview"
)

// RestoreDrill is one restoration or DR drill.
type RestoreDrill struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`   // Restore test, DR drill, Ransomware scenario
	System    string `json:"system"` // what was restored (Ledgerline, the trading front end …)
	Backup    string `json:"backup,omitempty"`
	From      string `json:"from"` // where the copy came from: Ledgerline backup, off-site copy, immutable copy …
	Isolated  bool   `json:"isolated"`
	Started   string `json:"started"`
	Restored  string `json:"restored"`
	DataUpTo  string `json:"data_up_to"` // the newest data in the restored copy
	Disrupted string `json:"disrupted,omitempty"`
	RTOMin    int    `json:"rto_min"`
	RPOMin    int    `json:"rpo_min"`
	Passed    bool   `json:"passed"`
	Note      string `json:"note,omitempty"`
	By        string `json:"by"`
	At        string `json:"at"`
}

// DrillKinds are the kinds of drill.
var DrillKinds = []string{"Restore test", "DR drill", "Ransomware scenario"}

// Targets for critical systems (2025/119 para 6.10).
const (
	RTOTargetMin = 120
	RPOTargetMin = 15
)

// RecordRestoreDrill adds a drill. Admin and compliance.
func (o *Ops) RecordRestoreDrill(ctx context.Context, tenant, actor string, d RestoreDrill) (RestoreDrill, error) {
	if err := o.require(ctx, tenant, actor, "Recording restore drills", RoleCompliance); err != nil {
		return d, err
	}
	if !contains(DrillKinds, d.Kind) {
		return d, invalid("the drill is one of %s", strings.Join(DrillKinds, ", "))
	}
	d.System, d.From = strings.TrimSpace(d.System), strings.TrimSpace(d.From)
	if d.System == "" || d.From == "" {
		return d, invalid("say what was restored and from which copy")
	}
	st, e1 := parseISTTime(d.Started)
	rs, e2 := parseISTTime(d.Restored)
	up, e3 := parseISTTime(d.DataUpTo)
	if e1 != nil || e2 != nil || e3 != nil {
		return d, invalid("give when the drill started, when the system was back and how recent the restored data was, as dates and times")
	}
	now := o.now()
	if rs.Before(st) || rs.After(now.Add(time.Minute)) || up.After(rs) {
		return d, invalid("the times are out of order or in the future")
	}
	// The disruption is the start of the drill unless the firm gives the
	// simulated time of failure; RPO is from the newest data restored to
	// the failure.
	fail := st
	if d.Disrupted != "" {
		x, err := parseISTTime(d.Disrupted)
		if err != nil || x.After(rs) {
			return d, invalid("give when the simulated failure was, before the system was back")
		}
		fail = x
	}
	d.RTOMin, d.RPOMin = int(rs.Sub(fail)/time.Minute), max(int(fail.Sub(up)/time.Minute), 0)
	d.Passed = d.RTOMin <= RTOTargetMin && d.RPOMin <= RPOTargetMin
	d.Started, d.Restored, d.DataUpTo, d.Disrupted = st.In(ist).Format(time.RFC3339), rs.In(ist).Format(time.RFC3339), up.In(ist).Format(time.RFC3339), fail.In(ist).Format(time.RFC3339)
	n, err := o.next(ctx, tenant, "restoredrill", actor)
	if err != nil {
		return d, err
	}
	d.ID, d.By, d.At, d.Note = fmt.Sprintf("DRL-%05d", n), actor, now.Format(time.RFC3339), trim(strings.TrimSpace(d.Note), 2000)
	if _, err := docs.Put(ctx, o.Docs, tenant, cDrill, d.ID, d, 0, actor); err != nil {
		return d, err
	}
	o.closeWork(ctx, tenant, actor, "security", "restore-drill", "drill recorded")
	o.log(ctx, tenant, actor, "security.restore_drill", d.ID, map[string]any{"kind": d.Kind, "system": d.System, "rto_min": d.RTOMin, "rpo_min": d.RPOMin, "passed": d.Passed, "isolated": d.Isolated})
	return d, nil
}

func (o *Ops) restoreDrills(ctx context.Context, tenant string) []RestoreDrill {
	xs, _ := docs.List[RestoreDrill](ctx, o.Docs, tenant, cDrill, "")
	sort.Slice(xs, func(i, j int) bool { return xs[i].Started > xs[j].Started })
	if xs == nil {
		xs = []RestoreDrill{}
	}
	return xs
}

// ---- technology vendors and their exit plans ----

// TechVendor is a technology provider and the plan to leave it.
type TechVendor struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Service  string `json:"service"` // Trading front end, RMS, Back office, Cloud hosting, Other
	Critical bool   `json:"critical"`
	// The assessment: lock-in and concentration (s.9).
	LockIn        string `json:"lock_in"`       // what makes leaving hard
	Concentration string `json:"concentration"` // what else depends on the same provider
	// The exit plan (para 61.2-61.3).
	Alternative string `json:"alternative"` // who could take over
	DataOut     string `json:"data_out"`    // how the data comes out, in what format
	NoticeDays  int    `json:"notice_days"`
	Steps       string `json:"steps"`
	Escrow      bool   `json:"escrow"` // source code escrow
	DocsClause  bool   `json:"docs_clause"`
	Training    bool   `json:"training_clause"`
	Penalties   bool   `json:"penalties_clause"`
	AuditCoop   bool   `json:"audit_clause"`
	ReviewedOn  string `json:"reviewed_on"`
	ApprovedBy  string `json:"approved_by,omitempty"` // board or proprietor approval, if any
	By          string `json:"by"`
	At          string `json:"at"`
	// Due: the plan is missing parts or more than a year old (on reading).
	Gaps []string `json:"gaps,omitempty"`
}

// TechServices are the kinds of service.
var TechServices = []string{"Trading front end", "RMS", "Back office", "Cloud hosting", "Depository system", "Other"}

func (v TechVendor) gaps(today string) []string {
	var g []string
	if strings.TrimSpace(v.Alternative) == "" {
		g = append(g, "no alternative provider named")
	}
	if strings.TrimSpace(v.DataOut) == "" {
		g = append(g, "no way to get the data out")
	}
	if strings.TrimSpace(v.Steps) == "" {
		g = append(g, "no exit steps")
	}
	if strings.TrimSpace(v.LockIn) == "" || strings.TrimSpace(v.Concentration) == "" {
		g = append(g, "lock-in and concentration risk not assessed")
	}
	if v.ReviewedOn == "" || daysBetween(v.ReviewedOn, today) > 366 {
		g = append(g, "not reviewed in the last year")
	}
	return g
}

// ledgerlineVendor is Ledgerline's own record, filled from what it offers.
func ledgerlineVendor() TechVendor {
	return TechVendor{ID: "ledgerline", Name: "Ledgerline", Service: "Back office", Critical: true,
		LockIn:  "The firm's records are in Ledgerline's database.",
		DataOut: "Full export (every table, signed, in JSON and CSV) at any time from Settings → Data & exit, and a data exit with a destruction certificate when the firm leaves; the migration tool reads other back offices' files.",
		Steps:   "1. Take a verified full export. 2. Load it into the new back office and run both side by side for a settlement cycle. 3. Close the account from Data & exit."}
}

// SaveTechVendor adds or changes a vendor's record. Admin and compliance.
func (o *Ops) SaveTechVendor(ctx context.Context, tenant, actor string, v TechVendor) (TechVendor, error) {
	if err := o.require(ctx, tenant, actor, "Keeping technology vendors' exit plans", RoleCompliance); err != nil {
		return v, err
	}
	v.Name = strings.TrimSpace(v.Name)
	if v.Name == "" || !contains(TechServices, v.Service) {
		return v, invalid("give the vendor's name and the service (%s)", strings.Join(TechServices, ", "))
	}
	if v.ReviewedOn != "" && (!validDay(v.ReviewedOn) || v.ReviewedOn > o.today()) {
		return v, invalid("the review date is YYYY-MM-DD and not in the future")
	}
	if v.NoticeDays < 0 || v.NoticeDays > 1000 {
		return v, invalid("give the notice period in days")
	}
	if v.ID == "" {
		n, err := o.next(ctx, tenant, "techvendor", actor)
		if err != nil {
			return v, err
		}
		v.ID = fmt.Sprintf("TV-%04d", n)
	}
	for _, s := range []*string{&v.LockIn, &v.Concentration, &v.Alternative, &v.DataOut, &v.Steps, &v.ApprovedBy} {
		*s = trim(strings.TrimSpace(*s), 2000)
	}
	v.By, v.At, v.Gaps = actor, o.now().Format(time.RFC3339), nil
	if err := docs.Upsert(ctx, o.Docs, tenant, cTechVendor, v.ID, v, actor); err != nil {
		return v, err
	}
	o.log(ctx, tenant, actor, "security.vendor_exit_plan", v.ID, map[string]any{"name": v.Name, "service": v.Service, "critical": v.Critical, "reviewed_on": v.ReviewedOn})
	v.Gaps = v.gaps(o.today())
	return v, nil
}

// techVendors lists the vendors with their gaps; Ledgerline is first,
// from its stored record when the firm saved one.
func (o *Ops) techVendors(ctx context.Context, tenant string) []TechVendor {
	xs, _ := docs.List[TechVendor](ctx, o.Docs, tenant, cTechVendor, "")
	have := false
	for _, x := range xs {
		if x.ID == "ledgerline" {
			have = true
		}
	}
	if !have {
		xs = append(xs, ledgerlineVendor())
	}
	today := o.today()
	for i := range xs {
		xs[i].Gaps = xs[i].gaps(today)
	}
	sort.SliceStable(xs, func(i, j int) bool {
		return xs[i].ID == "ledgerline" || xs[j].ID != "ledgerline" && xs[i].Name < xs[j].Name
	})
	return xs
}

// ---- SBOM ----

// SBOM returns Ledgerline's software bill of materials in CycloneDX 1.5
// JSON, from the modules built into this server. Admin and compliance.
func (o *Ops) SBOM(ctx context.Context, tenant, actor string) ([]byte, error) {
	if err := o.require(ctx, tenant, actor, "Downloading Ledgerline's software bill of materials", RoleCompliance); err != nil {
		return nil, err
	}
	o.log(ctx, tenant, actor, "security.sbom.downloaded", "ledgerline", nil)
	return buildSBOM(o.now())
}

func buildSBOM(now time.Time) ([]byte, error) {
	type comp struct {
		Type    string              `json:"type"`
		Name    string              `json:"name"`
		Version string              `json:"version"`
		PURL    string              `json:"purl,omitempty"`
		Hashes  []map[string]string `json:"hashes,omitempty"`
	}
	bi, ok := debug.ReadBuildInfo()
	main := comp{Type: "application", Name: "ledgerline", Version: "devel"}
	comps := []comp{{Type: "platform", Name: "go", Version: strings.TrimPrefix(runtime.Version(), "go"), PURL: "pkg:golang/std@" + strings.TrimPrefix(runtime.Version(), "go")}}
	if ok {
		if bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			main.Version = bi.Main.Version
		}
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" && main.Version == "devel" {
				main.Version = "git-" + s.Value
			}
		}
		for _, d := range bi.Deps {
			m := d
			if d.Replace != nil {
				m = d.Replace
			}
			c := comp{Type: "library", Name: m.Path, Version: m.Version, PURL: "pkg:golang/" + m.Path + "@" + m.Version}
			if strings.HasPrefix(m.Sum, "h1:") {
				c.Hashes = []map[string]string{{"alg": "SHA-256", "content": m.Sum[3:]}} // the Go module hash (base64 of a SHA-256 tree hash)
			}
			if m.Version == "" || strings.HasPrefix(m.Path, ".") {
				c.Version, c.PURL = "local", ""
			}
			comps = append(comps, c)
		}
	}
	sort.SliceStable(comps[1:], func(i, j int) bool { return comps[1+i].Name < comps[1+j].Name })
	doc := map[string]any{
		"bomFormat": "CycloneDX", "specVersion": "1.5", "version": 1,
		"metadata": map[string]any{"timestamp": now.UTC().Format(time.RFC3339), "component": main,
			"tools": []map[string]string{{"name": "ledgerline", "version": main.Version}}},
		"components": comps,
	}
	return json.MarshalIndent(doc, "", "  ")
}

// ---- the clock source ----

// NTPStatus is what the server's clock is synchronised to.
type NTPStatus struct {
	Servers []string `json:"servers"`
	From    string   `json:"from"`  // the file or setting read
	State   string   `json:"state"` // NIC/NPL, Other, Not found
	Line    string   `json:"line"`
}

// NTPConfigFiles are the time service files read, in order.
var NTPConfigFiles = []string{"/etc/chrony/chrony.conf", "/etc/chrony.conf", "/etc/ntp.conf", "/etc/ntpsec/ntp.conf", "/etc/systemd/timesyncd.conf"}

// indianTimeServers are NIC's and NPL's public servers.
var indianTimeServers = []string{"samay1.nic.in", "samay2.nic.in", "time.nplindia.org", "time.nplindia.in", "ntp.nplindia.in"}

// ntpStatus reads the operator's setting or the files.
func ntpStatus(env string, files []string) NTPStatus {
	s := NTPStatus{Servers: []string{}}
	if env = strings.TrimSpace(env); env != "" {
		s.From = "LEDGERLINE_NTP_SERVERS"
		for _, x := range strings.FieldsFunc(env, func(r rune) bool { return r == ',' || r == ' ' }) {
			s.Servers = append(s.Servers, strings.ToLower(x))
		}
	} else {
		for _, f := range files {
			fh, err := os.Open(f)
			if err != nil {
				continue
			}
			sc := bufio.NewScanner(fh)
			for sc.Scan() {
				l := strings.TrimSpace(sc.Text())
				fs := strings.Fields(l)
				if len(fs) >= 2 && (fs[0] == "server" || fs[0] == "pool" || fs[0] == "peer") {
					s.Servers = append(s.Servers, strings.ToLower(fs[1]))
				}
				if k, v, ok := strings.Cut(l, "="); ok && (strings.TrimSpace(k) == "NTP" || strings.TrimSpace(k) == "FallbackNTP") {
					for _, x := range strings.Fields(v) {
						s.Servers = append(s.Servers, strings.ToLower(x))
					}
				}
			}
			fh.Close()
			if len(s.Servers) > 0 {
				s.From = f
				break
			}
		}
	}
	indian := 0
	for _, x := range s.Servers {
		if contains(indianTimeServers, x) {
			indian++
		}
	}
	switch {
	case len(s.Servers) == 0:
		s.State, s.Line = "Not found", "No time server is set on this server (or Ledgerline cannot read it). The operator sets NIC's or NPL's servers (samay1.nic.in, samay2.nic.in, time.nplindia.org) and LEDGERLINE_NTP_SERVERS."
	case indian == len(s.Servers):
		s.State, s.Line = "NIC/NPL", "The server's clock follows "+strings.Join(s.Servers, ", ")+" (CERT-In direction (i))."
	case indian > 0:
		s.State, s.Line = "NIC/NPL", "The server's clock follows "+strings.Join(s.Servers, ", ")+"; the NIC or NPL servers among them meet CERT-In direction (i)."
	default:
		s.State, s.Line = "Other", "The server's clock follows "+strings.Join(s.Servers, ", ")+", not NIC's or NPL's servers that CERT-In direction (i) names."
	}
	return s
}

func (o *Ops) ntp() NTPStatus {
	return ntpStatus(os.Getenv("LEDGERLINE_NTP_SERVERS"), NTPConfigFiles)
}

// ---- privileged activity review ----

// PrivReview is the half-yearly review of the Admins' actions.
type PrivReview struct {
	ID       string           `json:"id"` // period, e.g. 2026-H2
	From     string           `json:"from"`
	To       string           `json:"to"`
	Admins   []PrivReviewLine `json:"admins"`
	Status   string           `json:"status"` // Open, Done
	Created  string           `json:"created"`
	Finished string           `json:"finished,omitempty"`
}

// PrivReviewLine is one Admin's actions in the period.
type PrivReviewLine struct {
	User       string         `json:"user"`
	Actions    int            `json:"actions"`
	ByKind     map[string]int `json:"by_kind"`
	Sensitive  []string       `json:"sensitive"` // the first 50 sensitive actions, in words
	ReviewedBy string         `json:"reviewed_by,omitempty"`
	ReviewedAt string         `json:"reviewed_at,omitempty"`
	Note       string         `json:"note,omitempty"`
}

// privSensitive are the actions a review looks at closely.
var privSensitive = []string{"user.", "role.", "security.settings", "settings.", "connection.", "client.reveal", "support.", "firmkey.", "dataexit.", "export.", "backup.", "privacy.erased", "security.incident.updated"}

// privPeriod is the half year a day is in.
func privPeriod(day string) (id, from, to string) {
	y, m := day[:4], day[5:7]
	if m <= "06" {
		return y + "-H1", y + "-01-01", y + "-06-30"
	}
	return y + "-H2", y + "-07-01", y + "-12-31"
}

// BuildPrivReview makes (or rebuilds while open) the review of the half
// year before the given day's half. Compliance and Admin.
func (o *Ops) BuildPrivReview(ctx context.Context, tenant, actor, day string) (PrivReview, error) {
	if err := o.require(ctx, tenant, actor, "Reviewing privileged users' activity", RoleCompliance); err != nil {
		return PrivReview{}, err
	}
	if !validDay(day) {
		day = o.today()
	}
	_, from, _ := privPeriod(day)
	prev := addDays(from, -1)
	id, from, to := privPeriod(prev)
	if r, _, err := docs.Get[PrivReview](ctx, o.Docs, tenant, cPrivReview, id); err == nil && r.Status == "Done" {
		return r, nil
	}
	us, _ := o.Users(ctx, tenant)
	admins := map[string]*PrivReviewLine{}
	for _, u := range us {
		if a, err := o.Access(ctx, tenant, u.ID); err == nil && a.Admin && u.Role != RoleClient {
			admins[u.ID] = &PrivReviewLine{User: u.ID, ByKind: map[string]int{}, Sensitive: []string{}}
		}
	}
	f, _ := time.ParseInLocation("2006-01-02", from, ist)
	t, _ := time.ParseInLocation("2006-01-02", to, ist)
	evs, err := o.auditWindow(ctx, tenant, f, t.Add(24*time.Hour), 0, 200000)
	if err != nil {
		return PrivReview{}, err
	}
	for _, e := range evs {
		l := admins[e.Actor]
		if l == nil {
			continue
		}
		l.Actions++
		k, _, _ := strings.Cut(e.Action, ".")
		l.ByKind[k]++
		for _, p := range privSensitive {
			if strings.HasPrefix(e.Action, p) && len(l.Sensitive) < 50 {
				l.Sensitive = append(l.Sensitive, e.At.In(ist).Format("02 Jan 15:04")+" "+e.Action+" "+trim(e.Object, 60))
				break
			}
		}
	}
	r := PrivReview{ID: id, From: from, To: to, Status: "Open", Created: o.now().Format(time.RFC3339), Admins: []PrivReviewLine{}}
	for _, l := range admins {
		r.Admins = append(r.Admins, *l)
	}
	sort.Slice(r.Admins, func(i, j int) bool { return r.Admins[i].User < r.Admins[j].User })
	if err := docs.Upsert(ctx, o.Docs, tenant, cPrivReview, id, r, actor); err != nil {
		return r, err
	}
	o.log(ctx, tenant, actor, "security.priv_review.built", id, map[string]any{"admins": len(r.Admins)})
	return r, nil
}

// SignPrivReview signs off one Admin's line. Not one's own line.
func (o *Ops) SignPrivReview(ctx context.Context, tenant, actor, id, user, note string) (PrivReview, error) {
	if err := o.require(ctx, tenant, actor, "Reviewing privileged users' activity", RoleCompliance); err != nil {
		return PrivReview{}, err
	}
	if actor == user {
		return PrivReview{}, fmt.Errorf("%w: someone else reviews your own privileged activity", ErrForbidden)
	}
	if len(strings.TrimSpace(note)) < 5 {
		return PrivReview{}, invalid("say what you checked and found")
	}
	r, err := docs.Update(ctx, o.Docs, tenant, cPrivReview, id, actor, func(r *PrivReview) error {
		found := false
		for i := range r.Admins {
			if r.Admins[i].User == user {
				r.Admins[i].ReviewedBy, r.Admins[i].ReviewedAt, r.Admins[i].Note, found = actor, o.now().Format(time.RFC3339), trim(strings.TrimSpace(note), 1000), true
			}
		}
		if !found {
			return ErrNotFound
		}
		done := true
		for _, a := range r.Admins {
			done = done && a.ReviewedBy != ""
		}
		if done {
			r.Status, r.Finished = "Done", o.now().Format(time.RFC3339)
		}
		return nil
	})
	if err != nil {
		return r, err
	}
	if r.Status == "Done" {
		o.closeWork(ctx, tenant, actor, "security", "priv-review:"+r.ID, "reviewed")
	}
	o.log(ctx, tenant, actor, "security.priv_review.signed", id, map[string]any{"user": user})
	return r, nil
}

// resilienceTick, once a day: a work item when the last restore drill is
// more than six months old, when the half-yearly review of privileged
// activity is not done a month into the next half, and when a critical
// vendor's exit plan has gaps.
func (o *Ops) resilienceTick(ctx context.Context, tenant string, now time.Time) {
	today := now.In(ist).Format("2006-01-02")
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cSecState, "resilience", secState{})
	if st.LastPurge >= today {
		return
	}
	st.LastPurge = today
	_ = docs.Upsert(ctx, o.Docs, tenant, cSecState, "resilience", st, SystemActor)
	ds := o.restoreDrills(ctx, tenant)
	if len(ds) == 0 || daysBetween(ds[0].Started[:10], today) > 183 {
		o.raiseWork(ctx, tenant, "security", "restore-drill", "Run and record a restore drill",
			"Backups are to be restored and tested at least twice a year (SEBI cloud framework s.6.2.12; CSCRF PR.IP.S8), with the time to recover and the data lost measured against RTO 2 hours and RPO 15 minutes. Record it on Security → Cyber controls.", "", RoleAdmin, "cyber", today)
	}
	_, from, _ := privPeriod(today)
	if daysBetween(from, today) >= 30 {
		pid, _, _ := privPeriod(addDays(from, -1))
		if r, _, err := docs.Get[PrivReview](ctx, o.Docs, tenant, cPrivReview, pid); err != nil || r.Status != "Done" {
			o.raiseWork(ctx, tenant, "security", "priv-review:"+pid, "Review the Admins' activity of "+pid,
				"Privileged users' activity is reviewed every half year, each Admin by someone else (CSCRF PR.AA.S11). Build the review on Security → Cyber controls and sign off each Admin.", "", RoleCompliance, "cyber", today)
		}
	}
	// From 1 April: the year's check of the technical glitch framework.
	if today[5:] >= "04-01" && today[5:] <= "04-30" {
		if g := o.glitchCheck(ctx, tenant, today); g.Over {
			o.raiseWork(ctx, tenant, "security", "glitch:"+g.AsOf, "Technical glitch framework: "+fmt.Sprint(g.Clients)+" registered clients on 31 March",
				g.Line+" (SEBI HO/38/44/12(1)2026-MIRSD-TPD1 para 6.1-6.2).", "", RoleCompliance, "cyber", today)
		}
	}
	for _, v := range o.techVendors(ctx, tenant) {
		if v.Critical && len(v.Gaps) > 0 {
			o.raiseWork(ctx, tenant, "security", "exit-plan:"+v.ID+":"+today[:4], "Exit plan for "+v.Name+" is incomplete",
				"The plan "+strings.Join(v.Gaps, "; ")+" (SEBI cloud framework s.9; stock brokers' master circular para 61.2-61.3).", "", RoleCompliance, "cyber", today)
		}
	}
}

// ---- the technical glitch framework: does it apply? ----

// GlitchCheck is the yearly test of SEBI's technical glitch framework
// (SEBI HO/38/44/12(1)2026-MIRSD-TPD1 para 6.1-6.2): a broker offering
// internet or app trading with more than 10,000 registered clients
// (closed accounts left out) as at 31 March is covered, as is one the
// exchanges name. Ledgerline counts the clients; the exchange list is the
// firm's to check.
type GlitchCheck struct {
	AsOf      string `json:"as_of"`
	Clients   int    `json:"clients"`
	Threshold int    `json:"threshold"`
	IBT       string `json:"ibt"` // yes, no, not known (Firm profile)
	Over      bool   `json:"over"`
	Line      string `json:"line"`
}

func (o *Ops) glitchCheck(ctx context.Context, tenant, day string) GlitchCheck {
	y := atoiSafe(day[:4])
	if day[5:] < "03-31" {
		y--
	}
	g := GlitchCheck{AsOf: fmt.Sprintf("%04d-03-31", y), Threshold: 10000}
	cs, _ := o.Clients(ctx, tenant)
	for _, c := range cs {
		if c.Status == "Closed" || c.DPOnly || c.KYCDate > g.AsOf || c.UCC == "" {
			continue
		}
		g.Clients++
	}
	switch o.firmFacts(ctx, tenant).IBT {
	case triYes:
		g.IBT = "yes"
	case triNo:
		g.IBT = "no"
	default:
		g.IBT = "not known"
	}
	g.Over = g.Clients > g.Threshold
	switch {
	case g.Over && g.IBT != "no":
		g.Line = fmt.Sprintf("%d registered clients on %s, more than 10,000: SEBI's technical glitch framework applies if clients trade online. Check the exchanges' list of specified brokers too.", g.Clients, fmtDay(g.AsOf))
	case g.Over:
		g.Line = fmt.Sprintf("%d registered clients on %s, but the firm offers no internet or app trading: the framework applies only if an exchange names the firm.", g.Clients, fmtDay(g.AsOf))
	default:
		g.Line = fmt.Sprintf("%d registered clients on %s, not more than 10,000: the framework applies only if an exchange names the firm.", g.Clients, fmtDay(g.AsOf))
	}
	return g
}

// CyberControls is the screen of these records.
type CyberControls struct {
	Drills     []RestoreDrill     `json:"drills"`
	DrillKinds []string           `json:"drill_kinds"`
	LastDrill  string             `json:"last_drill,omitempty"`
	DrillDue   bool               `json:"drill_due"`
	Vendors    []TechVendor       `json:"vendors"`
	Services   []string           `json:"services"`
	NTP        NTPStatus          `json:"ntp"`
	Review     *PrivReview        `json:"review,omitempty"`
	Retention  string             `json:"retention_warn,omitempty"`
	SignIn     map[string]any     `json:"sign_in"`
	Fraud      []FraudReport      `json:"fraud_reports"`
	Requests   []DataRequest      `json:"data_requests"`
	Erasure    []ErasureCandidate `json:"erasure"`
	Privacy    PrivacySettings    `json:"privacy"`
	Glitch     GlitchCheck        `json:"glitch"`
}

// CyberControlsView is the screen. Compliance and Admin.
func (o *Ops) CyberControlsView(ctx context.Context, tenant, actor string) (CyberControls, error) {
	if err := o.require(ctx, tenant, actor, "Seeing cyber controls", RoleCompliance); err != nil {
		return CyberControls{}, err
	}
	today := o.today()
	set := o.SecuritySettings(ctx, tenant)
	v := CyberControls{Drills: o.restoreDrills(ctx, tenant), DrillKinds: DrillKinds, Vendors: o.techVendors(ctx, tenant), Services: TechServices, NTP: o.ntp(), Retention: retentionWarn(set),
		SignIn: map[string]any{"out_of_band_unlock": !set.TimedUnlock, "password_days": set.passwordDays(), "password_history": set.passwordHistory(), "rate_limit": "30 tries per address in 15 minutes", "lock_after": maxFailures}}
	if len(v.Drills) > 0 {
		v.LastDrill = v.Drills[0].Started[:10]
	}
	v.DrillDue = v.LastDrill == "" || daysBetween(v.LastDrill, today) > 183
	_, from, _ := privPeriod(today)
	pid, _, _ := privPeriod(addDays(from, -1))
	if r, _, err := docs.Get[PrivReview](ctx, o.Docs, tenant, cPrivReview, pid); err == nil {
		v.Review = &r
	}
	v.Fraud, _ = o.FraudReports(ctx, tenant, actor)
	v.Requests, _ = o.DataRequests(ctx, tenant, actor)
	v.Erasure = o.erasureList(ctx, tenant)
	v.Privacy = o.privacySettings(ctx, tenant)
	v.Glitch = o.glitchCheck(ctx, tenant, today)
	return v, nil
}
