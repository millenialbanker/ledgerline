package ops

// The activity feed: everything done this week and who did it, as short
// sentences in a panel beside every screen, so an Admin who signs in at
// 10:30 sees what happened from 7:00 without opening the audit trail.
//
// The feed has no store of its own: it reads the audit trail (by tenant and
// time; app.AuditWindow) and turns each entry into a sentence with the
// phrase table in activityphrases.go. The only thing kept per user is
// whether the panel is open and the last entry they looked at
// (activity_state), for the "new since you last looked" marker.
//
// Window: the trading week, Monday 00:00 IST to Saturday 23:59 IST; on
// Sunday the feed still shows the week just ended (and anything done that
// Sunday), and it starts fresh at Monday 00:00. Older weeks stay on the
// Audit trail screen.
//
// Who sees what: an entry is shown only for an area the person can open;
// branch staff and authorised persons see only entries about their own
// clients or made by their own office's people; sign-ins, support access
// and key changes go to the Admin and the Compliance officer only; and
// identifiers in a sentence are masked as the audit export masks them.

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/app"
	"ledgerline/internal/docs"
)

const cActivityState = "activity_state" // per user: panel open, last entry seen

// Activity areas: the console's menu groups that entries belong to.
const (
	AreaOperations  = "Operations"
	AreaRisk        = "Risk"
	AreaAccounts    = "Accounts"
	AreaCompliance  = "Compliance"
	AreaDepository  = "Depository"
	AreaInvestments = "Investments"
	AreaAdmin       = "Admin"
)

// ActivityAreas lists the areas in menu order.
var ActivityAreas = []string{AreaOperations, AreaRisk, AreaAccounts, AreaCompliance, AreaDepository, AreaInvestments, AreaAdmin}

// ActivityWeek is the span the feed shows.
type ActivityWeek struct {
	From  string `json:"from"`  // Monday 00:00 IST
	Ends  string `json:"ends"`  // Saturday 23:59 IST, the end of the trading week
	To    string `json:"to"`    // the next Monday 00:00 IST (exclusive)
	Label string `json:"label"` // "Mon 16 Nov – Sat 21 Nov 2026"
	Ended bool   `json:"ended"` // Sunday: the week has ended and is still shown
	from  time.Time
	to    time.Time
}

// activityWeek is the trading week the feed shows at now: from the Monday
// on or before now (IST) to the next Monday. On Sunday that is the week
// just ended.
func activityWeek(now time.Time) ActivityWeek {
	t := now.In(ist)
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, ist)
	back := (int(day.Weekday()) + 6) % 7 // Monday 0 … Sunday 6
	from := day.AddDate(0, 0, -back)
	to := from.AddDate(0, 0, 7)
	ends := from.AddDate(0, 0, 6).Add(-time.Minute) // Saturday 23:59
	label := from.Format("Mon 2 Jan") + " – " + ends.Format("Mon 2 Jan 2006")
	if from.Year() != ends.Year() {
		label = from.Format("Mon 2 Jan 2006") + " – " + ends.Format("Mon 2 Jan 2006")
	}
	return ActivityWeek{From: from.Format(time.RFC3339), Ends: ends.Format(time.RFC3339), To: to.Format(time.RFC3339), Label: label,
		Ended: !t.Before(from.AddDate(0, 0, 6)), from: from, to: to}
}

// ActivityEntry is one line of the feed.
type ActivityEntry struct {
	Seq    int64  `json:"seq"`
	At     string `json:"at"`  // RFC 3339, IST
	Day    string `json:"day"` // YYYY-MM-DD, IST
	Actor  string `json:"actor"`
	Who    string `json:"who"`           // "Farhan Khan", "Ledgerline"
	Kind   string `json:"kind"`          // "person" or "ledgerline"
	Role   string `json:"role"`          // the person's role (desk), "Ledgerline" for the system
	Via    string `json:"via,omitempty"` // for Ledgerline: autopilot, bank feed, payment gateway…
	What   string `json:"what"`          // the sentence after the name
	Text   string `json:"text"`          // Who + " " + What
	Area   string `json:"area"`
	View   string `json:"view,omitempty"` // console screen to open
	UCC    string `json:"ucc,omitempty"`  // the client it concerns, to open the client
	Action string `json:"action"`
	Object string `json:"object,omitempty"` // masked
	Secure bool   `json:"secure,omitempty"` // security-sensitive (Admin and Compliance only)
}

// ActivityCount is one person's entries today.
type ActivityCount struct {
	Actor string `json:"actor"`
	Who   string `json:"who"`
	Kind  string `json:"kind"`
	Role  string `json:"role"`
	N     int    `json:"n"`
}

// ActivityPerson is a choice in the person filter.
type ActivityPerson struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
}

// ActivityState is what the feed keeps for each user.
type ActivityState struct {
	User   string `json:"user"`
	Open   bool   `json:"open"`
	Seen   int64  `json:"seen"` // the newest entry (audit sequence) the user has looked at
	SeenAt string `json:"seen_at,omitempty"`
}

// ActivityQuery is a request for the feed. After > 0 asks for entries
// newer than that cursor (polling); otherwise the newest entries of the
// week, or those older than Before (paging back).
type ActivityQuery struct {
	After, Before int64
	Limit         int
	Person        string // actor ID
	Role          string // the actor's role, or "Ledgerline"
	Area          string
	Only          string // "people", "ledgerline", "mine"
	Text          string
}

// ActivityFeed is the answer.
type ActivityFeed struct {
	Week    ActivityWeek    `json:"week"`
	Entries []ActivityEntry `json:"entries"` // newest first
	// Cursor is the newest audit entry read; poll with after=Cursor.
	Cursor int64 `json:"cursor"`
	// MoreNew: a poll found more new entries than one answer carries; poll
	// again at once.
	MoreNew bool `json:"more_new,omitempty"`
	// More: older entries of the week may remain; ask with before=Before.
	More   bool  `json:"more"`
	Before int64 `json:"before,omitempty"`
	// Seen is the user's last-seen entry; Unseen counts the newer entries
	// among those returned (99 at most), for the badge on the top bar.
	Seen   int64 `json:"seen"`
	Unseen int   `json:"unseen"`
	Open   bool  `json:"open"`
	// Counts: each person's entries today, for the Admin and head office
	// (not branch staff or authorised persons); first answers only.
	Counts []ActivityCount `json:"counts,omitempty"`
	// Filters the person may use.
	People    []ActivityPerson `json:"people,omitempty"`
	Areas     []string         `json:"areas,omitempty"`
	Roles     []string         `json:"roles,omitempty"`
	AuditLink bool             `json:"audit_link"` // the Audit trail screen is open to this person
}

const (
	activityBatch   = 500   // audit rows read at a time
	activityMaxScan = 20000 // rows looked at in one answer, at most
)

// activityViewer is who is asking and what they may see.
type activityViewer struct {
	actor    string
	role     string
	scoped   bool // branch staff or authorised person
	support  bool
	areas    map[string]bool
	secure   bool                  // may see security-sensitive entries
	scope    func(ucc string) bool // nil: every client
	own      map[string]bool       // for scoped users: the people of their office or downline
	users    map[string]User
	headView bool // sees the per-person counts
	// views, for someone holding a role the firm made or edited (or an
	// Admin previewing a role): the screens their roles may look at; a
	// line about any other screen is not shown. Nil: no such limit.
	views map[string]bool
}

// activityAreasFor is the areas each role can open. Every staff screen is
// open to every head-office role today (the console closes screens only
// for branch and AP logins), so head-office staff see every area; branch
// staff and authorised persons reach only client-level screens (clients,
// contract notes, receipts and payouts, debits, RMS limits, IPOs, investor
// complaints), so the Admin and Depository areas are closed to them, and
// within the others only lines about those screens are shown
// (activityScopedViews).
func activityAreasFor(role string) map[string]bool {
	out := map[string]bool{}
	for _, a := range ActivityAreas {
		out[a] = true
	}
	if role == RoleBranch || role == RoleAP {
		delete(out, AreaAdmin)
		delete(out, AreaDepository)
	}
	return out
}

func (o *Ops) activityViewer(ctx context.Context, tenant, actor string) (*activityViewer, error) {
	role, err := o.RoleOf(ctx, tenant, actor)
	if err != nil {
		return nil, err
	}
	if role == RoleClient {
		return nil, fmt.Errorf("%w: the activity feed is for the firm's staff", ErrForbidden)
	}
	us, err := o.Users(ctx, tenant)
	if err != nil {
		return nil, err
	}
	v := &activityViewer{actor: actor, role: role, users: map[string]User{}, areas: activityAreasFor(role), scope: o.ClientScope(ctx, tenant, actor)}
	for _, u := range us {
		v.users[u.ID] = u
	}
	v.support = IsSupportActor(actor)
	v.scoped = role == RoleBranch || role == RoleAP
	// A support session acts as Admin while approved, but the firm's
	// sign-ins and its own support approvals are not Ledgerline's to read.
	v.secure = !v.support && (role == RoleAdmin || role == RoleCompliance)
	v.headView = !v.scoped && !v.support
	// Roles the firm made or edited (roles.go): the screens they may look
	// at decide the lines, as SCOPED_VIEWS does for branch and AP logins.
	// Security-sensitive lines need the compliance officer's template with
	// the Incidents screen kept.
	if acc, err := o.Access(ctx, tenant, actor); err == nil && !acc.Admin && (acc.Gated() || acc.Preview) {
		v.views = map[string]bool{}
		for k, p := range acc.Screens() {
			if p.View {
				v.views[k] = true
			}
		}
		v.secure = !v.support && contains(acc.Legacy, RoleCompliance)
		for _, d := range acc.Defs {
			if d.Template == RoleCompliance && d.Screens["incidents"].View {
				v.secure = !v.support
			}
		}
		if !v.views["audit"] {
			v.headView = false // the per-person counts are the audit trail's
		}
	}
	if v.scoped {
		me := v.users[actor]
		v.own = map[string]bool{actor: true}
		if role == RoleBranch && me.Branch != "" {
			bs, _ := o.Branches(ctx, tenant)
			offices := map[string]bool{me.Branch: true}
			for _, c := range branchTree(bs)[me.Branch] {
				offices[c] = true
			}
			for _, u := range us {
				if u.scope() == RoleBranch && offices[u.Branch] {
					v.own[u.ID] = true
				}
			}
		}
		if role == RoleAP && me.Partner != "" {
			ps, _ := docs.List[Partner](ctx, o.Docs, tenant, cPartner, "")
			kids := map[string][]string{}
			for _, p := range ps {
				kids[p.Parent] = append(kids[p.Parent], p.Code)
			}
			down := map[string]bool{}
			var walk func(string)
			walk = func(c string) {
				if down[c] {
					return
				}
				down[c] = true
				for _, k := range kids[c] {
					walk(k)
				}
			}
			walk(me.Partner)
			for _, u := range us {
				if u.scope() == RoleAP && down[u.Partner] {
					v.own[u.ID] = true
				}
			}
		}
	}
	return v, nil
}

// sees reports whether the viewer may see the entry.
func (v *activityViewer) sees(e ActivityEntry) bool {
	if !v.areas[e.Area] {
		return false
	}
	if e.Secure && !v.secure {
		return false
	}
	if v.scope != nil && e.UCC != "" && !v.scope(e.UCC) {
		return false
	}
	if v.views != nil && !v.views[e.View] {
		return false // a screen the person's roles do not include
	}
	if v.scoped {
		if !activityScopedViews[e.View] {
			return false // a screen they cannot open (surveillance, billing, reports…)
		}
		return v.own[e.Actor] || (e.UCC != "" && v.scope != nil && v.scope(e.UCC))
	}
	return true
}

// activityScopedViews are the screens branch staff and authorised persons
// can open (the console's SCOPED_VIEWS, web/app.js); a line about any other
// screen is not shown to them, even about their own client, so surveillance
// alerts, billing runs and the like stay at head office.
var activityScopedViews = map[string]bool{"clients": true, "statements": true, "notes": true, "money": true, "debits": true, "rms": true,
	"partners": true, "prepaid": true, "changes": true, "helpdesk": true, "crm": true, "ipo": true, "yearend": true, "insights": true}

// matches applies the person's filters.
func (q ActivityQuery) matches(e ActivityEntry, me string) bool {
	switch q.Only {
	case "people":
		if e.Kind != "person" {
			return false
		}
	case "ledgerline":
		if e.Kind != "ledgerline" {
			return false
		}
	case "mine":
		if e.Actor != me {
			return false
		}
	}
	if q.Person != "" && e.Actor != q.Person {
		return false
	}
	if q.Role != "" && e.Role != q.Role {
		return false
	}
	if q.Area != "" && e.Area != q.Area {
		return false
	}
	if t := strings.ToLower(strings.TrimSpace(q.Text)); t != "" {
		hay := strings.ToLower(e.Text + " " + e.Action + " " + e.Area + " " + e.UCC)
		for _, w := range strings.Fields(t) {
			if !strings.Contains(hay, w) {
				return false
			}
		}
	}
	return true
}

// Activity answers the feed for a user.
func (o *Ops) Activity(ctx context.Context, tenant, actor string, q ActivityQuery) (ActivityFeed, error) {
	v, err := o.activityViewer(ctx, tenant, actor)
	if err != nil {
		return ActivityFeed{}, err
	}
	if q.Only != "" && q.Only != "people" && q.Only != "ledgerline" && q.Only != "mine" {
		return ActivityFeed{}, invalid("only must be people, ledgerline or mine")
	}
	if q.Area != "" && !slices.Contains(ActivityAreas, q.Area) {
		return ActivityFeed{}, invalid("unknown area %q", q.Area)
	}
	if q.Limit <= 0 || q.Limit > 500 {
		q.Limit = 200
	}
	now := o.now()
	wk := activityWeek(now)
	st := o.activityState(ctx, tenant, actor)
	out := ActivityFeed{Week: wk, Entries: []ActivityEntry{}, Seen: st.Seen, Open: st.Open, AuditLink: !v.scoped && (v.views == nil || v.views["audit"])}
	if q.After > 0 {
		err = o.activityPoll(ctx, tenant, v, q, &out)
	} else {
		err = o.activityPage(ctx, tenant, v, q, now, &out)
	}
	if err != nil {
		return ActivityFeed{}, err
	}
	if st.Seen > 0 {
		for _, e := range out.Entries {
			if e.Seq > st.Seen && out.Unseen < 99 {
				out.Unseen++
			}
		}
	}
	if q.After == 0 && q.Before == 0 {
		out.Areas = []string{}
		for _, a := range ActivityAreas {
			if v.areas[a] {
				out.Areas = append(out.Areas, a)
			}
		}
		out.People, out.Roles = v.people(), v.roles()
	}
	return out, nil
}

// activityPoll reads the entries after the cursor, oldest first, and
// returns them newest first.
func (o *Ops) activityPoll(ctx context.Context, tenant string, v *activityViewer, q ActivityQuery, out *ActivityFeed) error {
	rows, err := o.auditScan(ctx, tenant, q.After, activityBatch)
	if err != nil {
		return err
	}
	out.Cursor, out.MoreNew = q.After, len(rows) == activityBatch
	for _, a := range rows {
		out.Cursor = max(out.Cursor, a.Seq)
		if a.At.Before(out.Week.from) || !a.At.Before(out.Week.to) {
			continue
		}
		e, ok := v.entry(a)
		if ok && q.matches(e, v.actor) {
			out.Entries = append(out.Entries, e)
		}
	}
	// Every new line is returned (at most one batch), so the panel never
	// skips any; MoreNew says to ask again at once.
	slices.Reverse(out.Entries)
	return nil
}

// activityPage reads the week newest first until the page is full. The
// first page also counts each person's entries today, so it reads on
// through today even once the page is full.
func (o *Ops) activityPage(ctx context.Context, tenant string, v *activityViewer, q ActivityQuery, now time.Time, out *ActivityFeed) error {
	first := q.Before == 0
	if first {
		out.Cursor = o.auditLastSeq(ctx, tenant)
	}
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, ist)
	counting := first && v.headView
	counts := map[string]*ActivityCount{}
	before, scanned, full := q.Before, 0, false
	for scanned < activityMaxScan {
		rows, err := o.auditWindow(ctx, tenant, out.Week.from, out.Week.to, before, activityBatch)
		if err != nil {
			return err
		}
		for _, a := range rows {
			scanned++
			before = a.Seq
			if first {
				out.Cursor = max(out.Cursor, a.Seq)
			}
			e, ok := v.entry(a)
			if !ok {
				continue
			}
			if counting && !a.At.Before(today) {
				c := counts[e.Actor]
				if c == nil {
					c = &ActivityCount{Actor: e.Actor, Who: e.Who, Kind: e.Kind, Role: e.Role}
					counts[e.Actor] = c
				}
				c.N++
			}
			if full || !q.matches(e, v.actor) {
				continue
			}
			out.Entries = append(out.Entries, e)
			if len(out.Entries) == q.Limit {
				full, out.More, out.Before = true, true, a.Seq
			}
		}
		done := len(rows) < activityBatch
		if !done && len(rows) > 0 && counting && rows[len(rows)-1].At.Before(today) {
			counting = false // today is counted; the rest is earlier in the week
		}
		if done || (full && !counting) {
			break
		}
	}
	if !full && scanned >= activityMaxScan {
		out.More, out.Before = true, before
	}
	if first && v.headView {
		out.Counts = []ActivityCount{}
		for _, c := range counts {
			out.Counts = append(out.Counts, *c)
		}
		sort.Slice(out.Counts, func(i, j int) bool {
			if out.Counts[i].N != out.Counts[j].N {
				return out.Counts[i].N > out.Counts[j].N
			}
			return out.Counts[i].Who < out.Counts[j].Who
		})
	}
	return nil
}

// auditWindow reads a time span of the audit trail newest first, from the
// store underneath the vault (entries are stored in the clear). A store
// that cannot read by time is read whole and filtered.
func (o *Ops) auditWindow(ctx context.Context, tenant string, from, to time.Time, before int64, limit int) ([]app.AuditEvent, error) {
	var s app.AuditStore = o.App.Audit
	if va := o.vaultAudit(); va != nil {
		s = va.Inner
	}
	if w, ok := s.(app.AuditWindow); ok {
		return w.Window(ctx, tenant, from, to, before, limit)
	}
	all, err := s.List(ctx, tenant, 0)
	if err != nil {
		return nil, err
	}
	out := []app.AuditEvent{}
	for _, e := range all {
		if !e.At.Before(from) && e.At.Before(to) && (before <= 0 || e.Seq < before) {
			out = append(out, e)
			if len(out) == limit {
				break
			}
		}
	}
	return out, nil
}

func (v *activityViewer) people() []ActivityPerson {
	out := []ActivityPerson{}
	for _, u := range v.users {
		if u.Role == RoleClient || (v.scoped && !v.own[u.ID]) {
			continue
		}
		out = append(out, ActivityPerson{ID: u.ID, Name: firstOf(u.Name, u.ID), Role: u.Role})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (v *activityViewer) roles() []string {
	if v.scoped {
		return []string{v.role, "Ledgerline"}
	}
	out := []string{}
	for _, r := range Roles {
		out = append(out, r)
	}
	return append(out, "Client", "Ledgerline")
}

// entry turns an audit entry into a feed line; false for entries the feed
// leaves out (too frequent to read, or private to another screen) and
// those the viewer may not see.
func (v *activityViewer) entry(a app.AuditEvent) (ActivityEntry, bool) {
	e, ok := activityLine(a, v.users)
	if !ok || !v.sees(e) {
		return ActivityEntry{}, false
	}
	return e, true
}

// activityLine builds the line for an entry; false when the action is left
// out of the feed.
func activityLine(a app.AuditEvent, users map[string]User) (ActivityEntry, bool) {
	ph, mod := activityPhraseFor(a.Action)
	if ph.quiet {
		return ActivityEntry{}, false
	}
	x := activityData(a)
	at := a.At.In(ist)
	e := ActivityEntry{Seq: a.Seq, At: at.Format(time.RFC3339), Day: at.Format("2006-01-02"), Actor: a.Actor, Action: a.Action,
		Object: x.obj, Area: firstOf(ph.area, mod.area), View: firstOf(ph.view, mod.view), Secure: ph.secure || mod.secure}
	e.Who, e.Kind, e.Role, e.Via = activityWho(a.Actor, users)
	if ph.text != "" || ph.fn != nil {
		e.What = ph.render(x)
	}
	if e.What == "" {
		e.What = activityFallback(a.Action, mod, x)
	}
	e.Text = e.Who + " " + e.What
	switch {
	case ph.objUCC && x.obj != "":
		e.UCC = x.obj
	case ph.uccKey != "" && x.str(ph.uccKey) != "":
		e.UCC = x.str(ph.uccKey)
	case x.str("ucc") != "":
		e.UCC = x.str("ucc")
	case strings.HasPrefix(a.Actor, "client:"):
		e.UCC = strings.TrimPrefix(a.Actor, "client:")
	}
	return e, true
}

// activityWho names the actor: a user by name, Ledgerline for the system
// and its feeds, a client, an applicant or Ledgerline support.
func activityWho(actor string, users map[string]User) (who, kind, role, via string) {
	if u, ok := users[actor]; ok {
		return firstOf(u.Name, u.ID), "person", u.Role, ""
	}
	if v, ok := activitySystemActors[actor]; ok {
		return "Ledgerline", "ledgerline", "Ledgerline", v
	}
	switch {
	case strings.HasPrefix(actor, "system:"):
		return "Ledgerline", "ledgerline", "Ledgerline", strings.ReplaceAll(strings.TrimPrefix(actor, "system:"), "-", " ")
	case strings.HasPrefix(actor, "client:"):
		return "Client " + strings.TrimPrefix(actor, "client:"), "person", "Client", ""
	case strings.HasPrefix(actor, "applicant:"):
		return "An applicant", "person", "Applicant", ""
	case IsSupportActor(actor):
		op, _, _ := strings.Cut(strings.TrimPrefix(actor, SupportActorPrefix), "@")
		return "Ledgerline support (" + op + ")", "person", RoleSupportOperator, ""
	case actor == "":
		return "Someone", "person", "", ""
	}
	return actor, "person", "", ""
}

// activitySystemActors are the actors that are Ledgerline itself or a
// feed it runs, with how the feed labels them.
var activitySystemActors = map[string]string{
	SystemActor:                  "autopilot",
	"system:autopilot":           "autopilot",
	"system:automation":          "automation",
	"system:control":             "",
	"system:ledgerline":          "",
	"system:ledgy":               "Ledgy",
	"system:mirror":              "board mirror",
	"system:rules":               "board rules",
	"system:circulars":           "circulars",
	"system:support-outage-rule": "standing outage rule",
	"system":                     "",
	"ledgerline":                 "",
	"autopilot":                  "autopilot",
	"bank":                       "bank feed",
	"gateway":                    "payment gateway",
	"whatsapp":                   "WhatsApp",
}

// ---- the user's state ----

func (o *Ops) activityState(ctx context.Context, tenant, actor string) ActivityState {
	st, _, err := docs.Get[ActivityState](ctx, o.Docs, tenant, cActivityState, actor)
	if err != nil {
		return ActivityState{User: actor}
	}
	return st
}

// ActivityStatePatch changes the user's state; only the fields sent change.
type ActivityStatePatch struct {
	Open *bool  `json:"open"`
	Seen *int64 `json:"seen"`
}

// SaveActivityState remembers whether the user keeps the panel open and the
// newest entry they have looked at (it only moves forward). Not recorded in
// the audit trail: it is a screen preference, and the feed would otherwise
// report its own reading.
func (o *Ops) SaveActivityState(ctx context.Context, tenant, actor string, p ActivityStatePatch) (ActivityState, error) {
	if _, err := o.activityViewer(ctx, tenant, actor); err != nil {
		return ActivityState{}, err
	}
	if p.Seen != nil && *p.Seen < 0 {
		return ActivityState{}, invalid("seen must not be negative")
	}
	var out ActivityState
	_, err := upd(ctx, o.Docs, tenant, cActivityState, actor, actor, &ActivityState{User: actor}, func(s *ActivityState) error {
		s.User = actor
		if p.Open != nil {
			s.Open = *p.Open
		}
		if p.Seen != nil && *p.Seen > s.Seen {
			s.Seen, s.SeenAt = *p.Seen, o.now().Format(time.RFC3339)
		}
		out = *s
		return nil
	})
	return out, err
}

// ---- turning detail into words ----

// activityText holds an entry's object and detail, masked, with numbers
// read the same from either store (the memory store keeps Go values,
// PostgreSQL gives JSON back).
type activityText struct {
	obj string
	d   map[string]any
}

var reLongDigits = regexp.MustCompile(`\d{11,}`)

// activityMask hides identifiers in free text: PANs as the console shows
// them, and runs of 11 or more digits (Aadhaar, bank account and demat
// numbers) but their last four.
func activityMask(s string) string {
	s = maskPANs(s)
	return reLongDigits.ReplaceAllStringFunc(s, func(m string) string { return strings.Repeat("X", min(len(m)-4, 12)) + m[len(m)-4:] })
}

func activityData(a app.AuditEvent) activityText {
	x := activityText{obj: activityMask(a.Object), d: map[string]any{}}
	if len(a.Detail) > 0 {
		var d map[string]any
		if b, err := json.Marshal(a.Detail); err == nil && json.Unmarshal(b, &d) == nil {
			x.d = maskAuditDetail(d)
		}
	}
	return x
}

func (x activityText) val(k string) (any, bool) {
	if k == "object" {
		return x.obj, x.obj != ""
	}
	v, ok := x.d[k]
	if !ok || v == nil {
		return nil, false
	}
	if s, isStr := v.(string); isStr {
		s = activityMask(strings.TrimSpace(s))
		return s, s != ""
	}
	return v, true
}

func (x activityText) str(k string) string {
	v, ok := x.val(k)
	if !ok {
		return ""
	}
	return activityPlain(v)
}

func (x activityText) num(k string) (float64, bool) {
	v, ok := x.val(k)
	if !ok {
		return 0, false
	}
	return activityNum(v)
}
