package ops

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"ledgerline/internal/app"
	"ledgerline/internal/docs"
	"ledgerline/internal/recon"
)

// ---- Ledgerline boards: boards whose cards are Ledgerline's own records ----
//
// An Admin sets up one board per source: the work queue, reconciliation
// breaks, unpaid securities (CUSPA), onboarding, complaints and leads. Each
// board's lists are its records' stages. The sync (on every read of the
// board, and once a minute from the scheduler) makes a card for each record
// in scope, keeps its title, labels, due date and link status up to date,
// moves it to the list of the record's status and archives it when the
// record leaves scope. People's additions to a card (comments, checklists,
// members, their own labels, attachments) are never touched.
//
// In scope: what is open, and what finished in the last week (breaks:
// resolved or cleared since the start of the business day). A record of a
// private client never goes on a board: only the people named on the
// client may see it.
//
// Moving a card is the record's own action, done as the person with their
// permissions and maker-checker rules, where that is safe and well defined:
// a work item to Done closes it; a client one stage on advances its
// onboarding; a ticket to In progress, Awaiting client, Open or (once
// resolved) Closed; a lead to New, Contacted, Interested or Documents
// pending. Everything else is refused with the screen that does it.
//
// Any card (team boards too) can be linked to a client or record the
// person may see (BoardLinkSearch); the card window shows the record live
// (BoardLinkRecord).
//
// Cards never carry PAN, bank or demat numbers, Aadhaar or phone numbers:
// titles are scrubbed (bsScrub), and record details are shown live, never
// stored on the card.

const (
	cBoardSync = "boardsync" // a Ledgerline board's sync state, by board ID

	bsActor    = "system:ledgerline"
	bsLabelKey = "bs.label." // Board.Settings: a managed label's category → label ID
	bsKeep     = 7 * 24 * time.Hour
	bsReadGap  = 5 * time.Second
	bsTickGap  = time.Minute
	bsLease    = 2 * time.Minute
)

// bsState is a Ledgerline board's sync state.
type bsState struct {
	Board  string `json:"board"`
	Synced string `json:"synced,omitempty"` // last brought up to date (RFC3339)
	Lock   string `json:"lock,omitempty"`   // a sync holds the board until then
	// Closed: when the sync saw a record finish, for records that do not
	// say when they finished (an activated client, a cleared break).
	Closed map[string]string `json:"closed,omitempty"`
	Error  string            `json:"error,omitempty"` // the last sync's error
}

type bsList struct{ Status, Name, Color string }

// bsLabel is a label category: Key is stable, Name is what the board shows.
type bsLabel struct{ Key, Name, Color string }

// bsRec is one record as a card.
type bsRec struct {
	Key      string // kind|id
	Link     CardLink
	Title    string
	Status   string // the Source of the list it belongs in
	Labels   []bsLabel
	Due      string
	Closed   bool
	ClosedAt time.Time // zero: not known; the sync remembers when it saw it
	Order    string
	// New, when set, gives a new card its description and checklists;
	// after that they are people's (circboard.go).
	New func() (string, []Checklist)
}

// bsLinkKinds show the records of link kinds other files add (the card
// window's BoardLinkRecord), for a person who may see them.
var bsLinkKinds = map[string]func(ctx context.Context, o *Ops, tenant, actor string, s bsSeer, id string) (LinkHit, error){}

type bsEnv struct {
	now     time.Time
	date    string // the business date
	clients map[string]Client
}

func (e *bsEnv) hidden(ucc string) bool {
	c, ok := e.clients[strings.ToUpper(ucc)]
	return ok && c.Private
}

func (e *bsEnv) name(ucc string) string {
	c, ok := e.clients[strings.ToUpper(ucc)]
	if !ok || c.Private {
		return ""
	}
	return c.Name
}

// who is "UCC Name" (the code alone for a private or unknown client).
func (e *bsEnv) who(ucc string) string {
	if ucc == "" {
		return ""
	}
	if n := e.name(ucc); n != "" {
		return ucc + " " + n
	}
	return ucc
}

// bsSource is what a Ledgerline board shows and how its cards move.
type bsSource struct {
	ID, Name, About, Background string
	Screen                      string   // where its records are worked, for messages
	Kinds                       []string // link kinds its cards carry
	Lists                       []bsList
	Closed                      []string  // list statuses that are finished
	Labels                      []bsLabel // made with the board; others as records need them
	Roles                       []string  // who may see its records (nil: all head-office staff)
	since                       func(env *bsEnv) time.Time
	list                        func(ctx context.Context, o *Ops, tenant string, env *bsEnv) ([]bsRec, error)
	// move does the record's action for a card moved to a list, as the
	// person; it returns the record's new status (for the card's link).
	move func(ctx context.Context, o *Ops, tenant, actor string, c Card, to BoardList) (string, error)
}

var bsSources []*bsSource

func bsSourceOf(id string) (*bsSource, bool) {
	for _, s := range bsSources {
		if s.ID == id {
			return s, true
		}
	}
	return nil, false
}

func init() {
	weekAgo := func(env *bsEnv) time.Time { return env.now.Add(-bsKeep) }
	var areas []bsLabel
	seen := map[string]bool{}
	for _, d := range AutoDefs {
		if !seen[d.Area] {
			seen[d.Area] = true
			areas = append(areas, bsAreaLabel(d.Area))
		}
	}
	var breakLabels []bsLabel
	for _, t := range bsBreakTypes {
		breakLabels = append(breakLabels, bsLabel{"type:" + string(t.T), t.Name, t.Color})
	}
	var leadLabels []bsLabel
	leadColor := []string{"blue", "green", "yellow", "orange", "purple", "pink", "black"}
	for i, s := range LeadSources {
		leadLabels = append(leadLabels, bsLabel{"source:" + s, s, leadColor[i%len(leadColor)]})
	}
	bsSources = append([]*bsSource{
		{ID: "work", Name: "Work queue", Background: "blue", Screen: "Automation → Autopilot & work queue", Kinds: []string{"work"},
			About: "Autopilot's work queue: what only a person can finish, with the role who should act and when it is due. Cards follow the queue; move one to Done to close its item.",
			Lists: []bsList{{"Open", "Open", ""}, {"Done", "Done", "green"}, {"Cleared", "Cleared", ""}}, Closed: []string{"Done", "Cleared"},
			Labels: append(append([]bsLabel{}, bsRoleLabels...), append(areas, bsPrepared)...), since: weekAgo, list: bsWork, move: bsWorkMove},
		{ID: "breaks", Name: "Reconciliation breaks", Background: "orange", Screen: "Trade reconciliation", Kinds: []string{"break"},
			About: "Differences between the exchange trade files and the OMS: open breaks, and those resolved or cleared today. Breaks are resolved with a note on Trade reconciliation.",
			Lists: []bsList{{"Open", "Open", ""}, {"Resolved", "Resolved", "green"}, {"Cleared", "Cleared", ""}}, Closed: []string{"Resolved", "Cleared"},
			Labels: breakLabels, since: func(env *bsEnv) time.Time {
				t, err := time.ParseInLocation("2006-01-02", env.date, ist)
				if err != nil {
					return env.now.Add(-24 * time.Hour)
				}
				return t
			}, list: bsBreaks, move: bsBreakMove},
		{ID: "cuspa", Name: "Unpaid securities (CUSPA)", Background: "red", Screen: "Settlement → Unpaid securities", Kinds: []string{"cuspa"},
			About: "Securities clients have not paid for, held in the firm's CUSPA, by where they are in SEBI's timeline. Release, notice, invocation and sale are done on Settlement → Unpaid securities.",
			Lists: []bsList{{"period", "In payment period", ""}, {"due", "Pay by today", "yellow"}, {"overdue", "Unpaid: notice or invoke", "orange"}, {"last", "Last day to invoke", "red"},
				{"auto", "Depository releases", ""}, {"invoked", "Invoked: to sell", "purple"}, {"closed", "Closed", "green"}}, Closed: []string{"closed"},
			Labels: []bsLabel{bsCUSPANotice, bsCUSPAExcess, bsCUSPAPaid}, since: weekAgo, list: bsCUSPA, move: bsCUSPAMove},
		{ID: "onboarding", Name: "Onboarding", Background: "green", Screen: "Clients & onboarding", Kinds: []string{"client", "application"},
			About:  "New clients from application to active, with online applications not yet signed. Move a card one stage on to advance the client; activation needs an Ops checker who did not create the application.",
			Lists:  []bsList{{"Application", "Application", ""}, {"KYC verification", "KYC verification", ""}, {"UCC upload", "UCC upload", ""}, {"Active", "Active", "green"}, {"Rejected", "Rejected", ""}},
			Closed: []string{"Active", "Rejected"}, Labels: []bsLabel{bsOnline, bsDPOnly}, since: weekAgo, list: bsOnboarding, move: bsOnboardingMove},
		{ID: "complaints", Name: "Complaints and requests", Background: "purple", Screen: "Investor complaints", Kinds: []string{"ticket"},
			About:  "Investor complaints, service requests and queries that are open or were closed in the last week, with their due dates (21 days for complaints, 7 for the rest). Move a card to work it; resolve with a reply on Investor complaints.",
			Lists:  []bsList{{"Open", "Open", ""}, {"In progress", "In progress", ""}, {"Awaiting client", "Awaiting client", "yellow"}, {"Resolved", "Resolved", "green"}, {"Closed", "Closed", ""}},
			Closed: []string{"Resolved", "Closed"}, Labels: bsTicketLabels, since: weekAgo, list: bsTickets, move: bsTicketMove},
		{ID: "leads", Name: "Leads", Background: "sky", Screen: "Leads & CRM", Kinds: []string{"lead"},
			About:  "Open leads, and those converted or lost in the last week, by stage, with the next follow-up as the due date. Move a card to change the stage; convert or mark lost on Leads & CRM.",
			Lists:  []bsList{{"New", "New", ""}, {"Contacted", "Contacted", ""}, {"Interested", "Interested", ""}, {"Documents pending", "Documents pending", "yellow"}, {"Converted", "Converted", "green"}, {"Lost", "Lost", ""}},
			Closed: []string{"Converted", "Lost"}, Labels: leadLabels, Roles: []string{RoleMaker, RoleChecker, RoleCompliance}, since: weekAgo, list: bsLeads, move: bsLeadMove},
	}, bsSources...) // and any other file's (circboard.go), whichever init runs first
	boardReadHooks = append(boardReadHooks, func(ctx context.Context, o *Ops, tenant string, b Board) error {
		if b.Kind == BoardLedgerline {
			o.bsSync(ctx, tenant, b, o.now(), bsReadGap)
		}
		return nil
	})
	boardTickHooks = append(boardTickHooks, func(ctx context.Context, o *Ops, tenant string, now time.Time) { o.bsTick(ctx, tenant, now) })
	boardMoveHooks = append(boardMoveHooks, bsMove)
}

// ---- labels ----

var (
	bsRoleLabels = []bsLabel{{"role:" + RoleMaker, RoleMaker, "blue"}, {"role:" + RoleChecker, RoleChecker, "purple"}, {"role:" + RoleRisk, RoleRisk, "red"},
		{"role:" + RoleCompliance, RoleCompliance, "orange"}, {"role:" + RoleAdmin, RoleAdmin, "black"}, {"role:", "Anyone", "sky"}}
	bsPrepared     = bsLabel{"work:prepared", "Action prepared", "green"}
	bsCUSPANotice  = bsLabel{"cuspa:notice", "Notice given", "yellow"}
	bsCUSPAExcess  = bsLabel{"cuspa:excess", "Over the 2.25× limit", "red"}
	bsCUSPAPaid    = bsLabel{"cuspa:paid", "Client paid", "green"}
	bsOnline       = bsLabel{"online", "Online", "blue"}
	bsDPOnly       = bsLabel{"dponly", "DP only", "purple"}
	bsTicketLabels = []bsLabel{{"kind:" + KindComplaint, KindComplaint, "red"}, {"kind:" + KindRequest, KindRequest, "blue"}, {"kind:" + KindQuery, KindQuery, "sky"},
		{"source:SEBI (SCORES)", "SCORES", "orange"}, {"source:Stock exchanges", "Exchange", "purple"}}
	bsBreakTypes = []struct {
		T           recon.BreakType
		Name, Color string
	}{
		{recon.MissingInOMS, "Missing in OMS", "red"}, {recon.MissingInExchange, "Missing in exchange", "orange"}, {recon.TradeNoMismatch, "Trade no. mismatch", "yellow"},
		{recon.UCCMismatch, "UCC mismatch", "purple"}, {recon.SideMismatch, "Side mismatch", "pink"}, {recon.InstrumentMismatch, "Contract mismatch", "blue"},
		{recon.QtyMismatch, "Qty mismatch", "sky"}, {recon.PriceMismatch, "Price mismatch", "lime"}, {recon.AlgoIDMissing, "Algo ID missing", "black"},
	}
)

var bsAreaColor = map[string]string{"Operations": "green", "Risk": "pink", "Money": "yellow", "Compliance": "orange", "Clients": "lime", "Accounts": "sky", "Client insights": "blue"}

func bsAreaLabel(area string) bsLabel {
	return bsLabel{"area:" + area, area, firstOf(bsAreaColor[area], "green")}
}

func bsRoleLabel(role string) bsLabel {
	for _, l := range bsRoleLabels {
		if l.Key == "role:"+role {
			return l
		}
	}
	return bsLabel{"role:" + role, role, "sky"}
}

func bsBreakType(t recon.BreakType) (string, string) {
	for _, x := range bsBreakTypes {
		if x.T == t {
			return x.Name, x.Color
		}
	}
	s := strings.ToLower(strings.ReplaceAll(string(t), "_", " "))
	if s == "" {
		return "Break", "black"
	}
	return strings.ToUpper(s[:1]) + s[1:], "black"
}

// ---- privacy ----

var reBSDemat = regexp.MustCompile(`\bIN[0-9]{14}\b`)

func bsDigits(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
}

// bsScrub masks PAN, Aadhaar, phone, bank and demat numbers in text that
// goes on a card or a link.
func bsScrub(s string) string {
	s = reMaskPAN2.ReplaceAllStringFunc(s, func(p string) string { return MaskPAN(strings.ToUpper(p)) })
	s = reBSDemat.ReplaceAllStringFunc(s, MaskAccount)
	s = reMaskAadhaar.ReplaceAllStringFunc(s, func(a string) string { d := bsDigits(a); return "XXXX XXXX " + d[len(d)-4:] })
	s = reMaskPhone.ReplaceAllStringFunc(s, func(p string) string { d := bsDigits(p); return "XXXXXX" + d[len(d)-4:] })
	return reMaskAccount.ReplaceAllStringFunc(s, MaskAccount)
}

// ---- small helpers ----

// bsTime reads an RFC3339 time or a date (the start of the day, IST).
func bsTime(s string) time.Time {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	if t, err := time.ParseInLocation("2006-01-02", s, ist); err == nil {
		return t
	}
	return time.Time{}
}

// bsDue turns "2026-10-16 23:59" or "2026-10-16" into a card due date.
func bsDue(s string) string {
	s = strings.TrimSpace(s)
	if t, err := time.Parse("2006-01-02 15:04", s); err == nil {
		return t.Format("2006-01-02T15:04")
	}
	if d, err := boardDate(s); err == nil {
		return d
	}
	return ""
}

// bsRunTime is when a reconciliation run happened, from its ID
// ("RUN-20261016-<milliseconds>").
func bsRunTime(run string) time.Time {
	i := strings.LastIndex(run, "-")
	if i < 0 {
		return time.Time{}
	}
	ms, err := strconv.ParseInt(run[i+1:], 10, 64)
	if err != nil || ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

// bsInstrument is "SYMBOL|TYPE|EXPIRY|STRIKE" as people read it.
func bsInstrument(k string) string {
	p := strings.Split(k, "|")
	for len(p) < 4 {
		p = append(p, "")
	}
	if p[1] == "" || p[1] == "EQ" {
		return p[0]
	}
	ex := p[2]
	if t, err := time.Parse("2006-01-02", p[2]); err == nil {
		ex = strings.ToUpper(t.Format("02Jan06"))
	}
	if p[1] == "FUT" {
		return p[0] + " " + ex + " FUT"
	}
	return strings.TrimSpace(p[0] + " " + ex + " " + p[3] + " " + p[1])
}

func bsJoin(xs ...string) string {
	var out []string
	for _, x := range xs {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return strings.Join(out, " · ")
}

// ---- sources ----

func bsWork(ctx context.Context, o *Ops, tenant string, env *bsEnv) ([]bsRec, error) {
	ws, err := o.WorkItems(ctx, tenant, "")
	if err != nil {
		return nil, err
	}
	out := make([]bsRec, 0, len(ws))
	for _, w := range ws {
		if env.hidden(w.UCC) {
			continue
		}
		list := w.Status
		if list == "Dismissed" { // closed by a person ("done elsewhere")
			list = "Done"
		}
		r := bsRec{Key: "work|" + w.ID, Status: list, Closed: w.Status != "Open", ClosedAt: bsTime(w.Closed), Due: bsDue(w.Due), Order: w.Due + "|" + w.ID, Title: w.Title}
		if w.UCC != "" {
			if !strings.Contains(w.Title, w.UCC) {
				r.Title += " · " + env.who(w.UCC)
			} else if n := env.name(w.UCC); n != "" && !strings.Contains(w.Title, n) {
				r.Title += " · " + n
			}
		}
		r.Labels = append(r.Labels, bsRoleLabel(w.Role))
		if d, ok := autoDef(w.Process); ok {
			r.Labels = append(r.Labels, bsAreaLabel(d.Area))
		}
		if w.Proposed != nil && w.Status == "Open" {
			r.Labels = append(r.Labels, bsPrepared)
		}
		r.Link = CardLink{Kind: "work", ID: w.ID, Label: clip(bsScrub(w.Title), 120), View: w.View, Status: w.Status, UCC: w.UCC}
		out = append(out, r)
	}
	return out, nil
}

var bsBreakStatus = map[string]string{app.BreakOpen: "Open", app.BreakResolved: "Resolved", app.BreakCleared: "Cleared"}

func bsBreakTitle(env *bsEnv, x app.BreakRecord) string {
	t, _ := bsBreakType(x.Type)
	return bsJoin(t, x.Exchange+" "+bsInstrument(x.Instrument), env.who(x.UCC))
}

func bsBreaks(ctx context.Context, o *Ops, tenant string, env *bsEnv) ([]bsRec, error) {
	bs, err := o.App.Breaks.List(ctx, tenant, "", "")
	if err != nil {
		return nil, err
	}
	out := make([]bsRec, 0, len(bs))
	for _, x := range bs {
		st := bsBreakStatus[x.Status]
		if st == "" || env.hidden(x.UCC) {
			continue
		}
		r := bsRec{Key: "break|" + x.ID, Status: st, Closed: x.Status != app.BreakOpen, Order: x.TradeDate + "|" + string(x.Type) + "|" + x.TradeNo, Title: bsBreakTitle(env, x)}
		if x.ResolvedAt != nil {
			r.ClosedAt = *x.ResolvedAt
		} else if x.Status == app.BreakCleared {
			r.ClosedAt = bsRunTime(x.LastRun)
		}
		name, color := bsBreakType(x.Type)
		r.Labels = []bsLabel{{"type:" + string(x.Type), name, color}}
		if seg := strings.TrimSpace(x.Exchange + " " + x.Segment); seg != "" {
			r.Labels = append(r.Labels, bsLabel{"seg:" + seg, seg, "green"})
		}
		r.Link = CardLink{Kind: "break", ID: x.ID, Label: clip(bsScrub(r.Title), 120), View: "recon", Status: st, UCC: x.UCC}
		out = append(out, r)
	}
	return out, nil
}

var bsCUSPABucket = map[string]string{"period": "In payment period", "due": "Pay by today", "overdue": "Unpaid: notice or invoke", "last": "Last day to invoke",
	"auto": "Depository releases", "invoked": "Invoked: to sell", "closed": "Closed"}

func bsCUSPATitle(env *bsEnv, x CUSPARow) string {
	q := x.Qty + x.unsold()
	if x.Bucket == "closed" {
		q = x.total()
	}
	return bsJoin(fmt.Sprintf("%s %d", x.Symbol, q), env.who(x.UCC))
}

func bsCUSPA(ctx context.Context, o *Ops, tenant string, env *bsEnv) ([]bsRec, error) {
	v, err := o.CUSPAAgeing(ctx, tenant, env.date)
	if err != nil {
		return nil, err
	}
	out := make([]bsRec, 0, len(v.Rows))
	for _, x := range v.Rows {
		if env.hidden(x.UCC) || bsCUSPABucket[x.Bucket] == "" {
			continue
		}
		closed := x.Bucket == "closed"
		r := bsRec{Key: "cuspa|" + x.ID, Status: x.Bucket, Closed: closed, Order: x.PayOutDay + "|" + x.ID, Title: bsCUSPATitle(env, x)}
		if closed {
			r.ClosedAt = bsTime(cuspaLastDate(x.CUSPAItem))
		}
		if x.Exchange != "" {
			r.Labels = append(r.Labels, bsLabel{"exch:" + x.Exchange, x.Exchange, "sky"})
		}
		if !closed && x.NoticeOn != "" {
			r.Labels = append(r.Labels, bsCUSPANotice)
		}
		if x.ReleaseQty > 0 {
			r.Labels = append(r.Labels, bsCUSPAExcess)
		}
		if x.Qty > 0 && x.Ledger.Sign() >= 0 {
			r.Labels = append(r.Labels, bsCUSPAPaid)
		}
		switch x.Bucket {
		case "period", "due":
			r.Due = x.PayBy
		case "overdue", "last":
			r.Due = x.LastInvoke
		case "auto":
			r.Due = x.AutoRelease
		}
		st := bsCUSPABucket[x.Bucket]
		if closed {
			st = x.CUSPAItem.Status
		}
		r.Link = CardLink{Kind: "cuspa", ID: x.ID, Label: clip(bsScrub(r.Title), 120), View: "settlement", Status: st, UCC: x.UCC}
		out = append(out, r)
	}
	return out, nil
}

var bsOnboardingStatus = []string{"Application", "KYC verification", "UCC upload", "Active", "Rejected"}

func bsOnboarding(ctx context.Context, o *Ops, tenant string, env *bsEnv) ([]bsRec, error) {
	apps, err := o.Applications(ctx, tenant)
	if err != nil {
		return nil, err
	}
	byUCC := map[string]Application{}
	var out []bsRec
	for _, a := range apps { // newest first
		if a.UCC != "" {
			if _, ok := byUCC[a.UCC]; !ok {
				byUCC[a.UCC] = a
			}
			continue
		}
		name := firstOf(strings.TrimSpace(a.Name), a.ID)
		r := bsRec{Key: "application|" + a.ID, Order: a.Created + "|" + a.ID, Title: "Online application · " + name, Labels: []bsLabel{bsOnline}}
		switch a.Status {
		case "Draft":
			if t := bsTime(a.Updated); !t.IsZero() && env.now.Sub(t) > 30*24*time.Hour {
				continue // abandoned
			}
			r.Status = "Application"
		case "Rejected":
			r.Status, r.Closed, r.ClosedAt = "Rejected", true, bsTime(a.Updated)
		default:
			continue
		}
		r.Link = CardLink{Kind: "application", ID: a.ID, Label: clip(bsScrub(a.ID+" "+name), 120), View: "apps", Status: a.Status}
		out = append(out, r)
	}
	for _, ucc := range sortedKeys(env.clients) {
		c := env.clients[ucc]
		if c.Private || !contains(bsOnboardingStatus, c.Status) {
			continue
		}
		r := bsRec{Status: c.Status, Closed: c.Status == "Active" || c.Status == "Rejected", Order: c.KYCDate + "|" + c.UCC, Title: bsJoin(c.UCC+" "+c.Name, c.City)}
		if c.DPOnly {
			r.Labels = append(r.Labels, bsDPOnly)
		}
		label := clip(bsScrub(c.UCC+" "+c.Name), 120)
		if a, ok := byUCC[c.UCC]; ok {
			r.Key = "application|" + a.ID
			r.Labels = append(r.Labels, bsOnline)
			if a.Status == "Rejected" || a.Activated {
				r.ClosedAt = bsTime(a.Updated)
			}
			r.Link = CardLink{Kind: "application", ID: a.ID, Label: label, View: "apps", Status: c.Status, UCC: c.UCC}
		} else {
			r.Key = "client|" + c.UCC
			r.Link = CardLink{Kind: "client", ID: c.UCC, Label: label, View: "clients", Status: c.Status, UCC: c.UCC}
		}
		out = append(out, r)
	}
	return out, nil
}

func bsTickets(ctx context.Context, o *Ops, tenant string, env *bsEnv) ([]bsRec, error) {
	ts, err := o.Tickets(ctx, tenant, env.date)
	if err != nil {
		return nil, err
	}
	out := make([]bsRec, 0, len(ts))
	for _, t := range ts {
		if env.hidden(t.UCC) || !contains(TicketStatuses, t.Status) {
			continue
		}
		closed := t.Status == "Resolved" || t.Status == "Closed"
		r := bsRec{Key: "ticket|" + t.ID, Status: t.Status, Closed: closed, ClosedAt: bsTime(t.ResolvedOn), Due: bsDue(t.Due), Order: t.Due + "|" + t.ID,
			Title: bsJoin(t.Subject, env.who(t.UCC))}
		for _, l := range bsTicketLabels {
			if l.Key == "kind:"+t.Kind || l.Key == "source:"+t.Source {
				r.Labels = append(r.Labels, l)
			}
		}
		if t.Category != "" {
			r.Labels = append(r.Labels, bsLabel{"category:" + t.Category, t.Category, "yellow"})
		}
		r.Link = CardLink{Kind: "ticket", ID: t.ID, Label: clip(bsScrub(t.ID+" · "+t.Subject), 120), View: "helpdesk", Status: t.Status, UCC: t.UCC}
		out = append(out, r)
	}
	return out, nil
}

func bsLeadClosedAt(l Lead) time.Time {
	if l.Stage == "Converted" && l.Converted != "" {
		return bsTime(l.Converted)
	}
	for i := len(l.Notes) - 1; i >= 0; i-- {
		if l.Notes[i].Kind == "Stage" {
			return bsTime(l.Notes[i].At)
		}
	}
	return time.Time{}
}

func bsLeads(ctx context.Context, o *Ops, tenant string, env *bsEnv) ([]bsRec, error) {
	v, err := o.CRM(ctx, tenant, SystemActor, env.date, env.date, env.date)
	if err != nil {
		return nil, err
	}
	out := make([]bsRec, 0, len(v.Leads))
	for _, l := range v.Leads {
		if (l.UCC != "" && env.hidden(l.UCC)) || !contains(LeadStages, l.Stage) {
			continue
		}
		closed := l.Stage == "Converted" || l.Stage == "Lost"
		r := bsRec{Key: "lead|" + l.ID, Status: l.Stage, Closed: closed, Order: firstOf(l.Next, "9999") + "|" + l.ID, Title: bsJoin(l.Name, l.City)}
		if closed {
			r.ClosedAt = bsLeadClosedAt(l)
		} else {
			r.Due = bsDue(l.Next)
		}
		if l.Source != "" {
			r.Labels = []bsLabel{{"source:" + l.Source, l.Source, "blue"}}
		}
		r.Link = CardLink{Kind: "lead", ID: l.ID, Label: clip(bsScrub(l.ID+" "+l.Name), 120), View: "crm", Status: l.Stage, UCC: l.UCC}
		out = append(out, r)
	}
	return out, nil
}

// ---- setting up ----

// LedgerlineSetup is one Ledgerline board, set up now or already there.
type LedgerlineSetup struct {
	Source  string `json:"source"`
	Board   string `json:"board"`
	Name    string `json:"name"`
	Created bool   `json:"created"`
	Cards   int    `json:"cards"`
}

func bsSourceIDs() []string {
	var ids []string
	for _, s := range bsSources {
		ids = append(ids, s.ID)
	}
	return ids
}

// SetUpLedgerlineBoards makes the Ledgerline boards for the sources asked
// (all when none), each once: a board already there is kept (and given
// any list it lacks) rather than made again.
func (o *Ops) SetUpLedgerlineBoards(ctx context.Context, tenant, actor string, sources []string) ([]LedgerlineSetup, error) {
	if err := o.require(ctx, tenant, actor, "Setting up Ledgerline boards", RoleAdmin); err != nil {
		return nil, err
	}
	sources = nonEmptyUniq(sources)
	if len(sources) == 0 {
		sources = bsSourceIDs()
	}
	var srcs []*bsSource
	for _, id := range sources {
		s, ok := bsSourceOf(id)
		if !ok {
			return nil, invalid("there is no Ledgerline board %q; choose from %s", id, strings.Join(bsSourceIDs(), ", "))
		}
		srcs = append(srcs, s)
	}
	all, err := docs.List[Board](ctx, o.Docs, tenant, cBoard, "")
	if err != nil {
		return nil, err
	}
	out := []LedgerlineSetup{}
	for _, s := range srcs {
		b, found := bsBoardFor(all, s.ID)
		created := false
		if !found {
			nb := o.bsNewBoard(ctx, tenant, actor, s)
			if _, err := docs.Put(ctx, o.Docs, tenant, cBoard, nb.ID, nb, 0, actor); err != nil {
				if !errors.Is(err, docs.ErrStale) {
					return out, err
				}
				if b, _, err = docs.Get[Board](ctx, o.Docs, tenant, cBoard, nb.ID); err != nil { // set up by someone else just now
					return out, err
				}
			} else {
				b, created = nb, true
				o.boardLog(ctx, tenant, actor, b, "", "board.created", "set up this board from "+s.Screen, nil)
				o.log(ctx, tenant, actor, "board.ledgerline", b.ID, map[string]any{"source": s.ID})
			}
		} else if b, err = o.bsRepair(ctx, tenant, actor, b, s); err != nil {
			return out, err
		}
		o.bsSync(ctx, tenant, b, o.now(), 0)
		n := 0
		if cs, err := o.boardCards(ctx, tenant, b.ID); err == nil {
			for _, c := range cs {
				if !c.Archived {
					n++
				}
			}
		}
		out = append(out, LedgerlineSetup{Source: s.ID, Board: b.ID, Name: b.Name, Created: created, Cards: n})
	}
	return out, nil
}

// bsBoardFor is the board of a source: the one set up here first, else any
// Ledgerline board of that source.
func bsBoardFor(all []Board, source string) (Board, bool) {
	var found *Board
	for i := range all {
		b := &all[i]
		if b.Kind != BoardLedgerline || b.Source != source || b.Deleted {
			continue
		}
		if b.ID == "B-ledgerline-"+source {
			return *b, true
		}
		if found == nil {
			found = b
		}
	}
	if found == nil {
		return Board{}, false
	}
	return *found, true
}

func (o *Ops) bsNewBoard(ctx context.Context, tenant, actor string, s *bsSource) Board {
	now := o.now().Format(time.RFC3339)
	b := Board{ID: "B-ledgerline-" + s.ID, Name: s.Name, Description: s.About, Kind: BoardLedgerline, Source: s.ID, Visibility: "firm", Members: []BoardPerson{},
		Labels: []BoardLabel{}, Background: s.Background, CoversOn: true, Settings: map[string]string{}, CreatedBy: actor, CreatedAt: now, UpdatedAt: now}
	for i, l := range s.Lists {
		b.Lists = append(b.Lists, BoardList{ID: boardID("L-", 4), Name: l.Name, Pos: float64((i + 1) * 1024), Color: l.Color, Source: l.Status})
	}
	for _, l := range s.Labels {
		nl := BoardLabel{ID: boardID("LB-", 4), Name: l.Name, Color: l.Color}
		b.Labels = append(b.Labels, nl)
		b.Settings[bsLabelKey+l.Key] = nl.ID
	}
	if len(s.Roles) > 0 { // only the roles that may see these records
		b.Visibility = "private"
		b.Members = o.bsRoleMembers(ctx, tenant, s, nil)
	}
	return b
}

// bsRepair gives a board the lists and labels of its source it lacks.
func (o *Ops) bsRepair(ctx context.Context, tenant, actor string, b Board, s *bsSource) (Board, error) {
	var added []string
	nb, err := docs.Update(ctx, o.Docs, tenant, cBoard, b.ID, actor, func(x *Board) error {
		added = nil
		has := map[string]bool{}
		top := 0.0
		for i := range x.Lists {
			if x.Lists[i].Source != "" {
				has[x.Lists[i].Source] = true
				x.Lists[i].Archived = false
			}
			top = math.Max(top, x.Lists[i].Pos)
		}
		for _, l := range s.Lists {
			if !has[l.Status] {
				top += 1024
				x.Lists = append(x.Lists, BoardList{ID: boardID("L-", 4), Name: l.Name, Pos: top, Color: l.Color, Source: l.Status})
				added = append(added, l.Name)
			}
		}
		if x.Settings == nil {
			x.Settings = map[string]string{}
		}
		ids := map[string]bool{}
		for _, l := range x.Labels {
			ids[l.ID] = true
		}
		for _, l := range s.Labels {
			if id := x.Settings[bsLabelKey+l.Key]; (id == "" || !ids[id]) && len(x.Labels) < 50 {
				nl := BoardLabel{ID: boardID("LB-", 4), Name: l.Name, Color: l.Color}
				x.Labels = append(x.Labels, nl)
				x.Settings[bsLabelKey+l.Key] = nl.ID
			}
		}
		x.UpdatedAt = o.now().Format(time.RFC3339)
		return nil
	})
	if err != nil {
		return b, err
	}
	for _, n := range added {
		o.boardLog(ctx, tenant, actor, nb, "", "list.created", "added the list "+n, nil)
	}
	return nb, nil
}

// bsRoleMembers is a role-limited board's members: everyone active with
// one of the roles (members already there keep their board role).
func (o *Ops) bsRoleMembers(ctx context.Context, tenant string, s *bsSource, cur []BoardPerson) []BoardPerson {
	have := map[string]string{}
	for _, m := range cur {
		have[m.User] = m.Role
	}
	out := []BoardPerson{}
	us, _ := o.Users(ctx, tenant)
	for _, u := range us {
		if !u.Active || !contains(s.Roles, u.Role) {
			continue
		}
		out = append(out, BoardPerson{User: u.ID, Role: firstOf(have[u.ID], BoardMember)})
	}
	return out
}

// bsGuard keeps a Ledgerline board to head-office staff, and a
// role-limited one to its roles (the core makes every head-office user a
// member of a firm-wide board).
func (o *Ops) bsGuard(ctx context.Context, tenant string, b Board, s *bsSource) Board {
	us, _ := o.Users(ctx, tenant)
	role := map[string]string{}
	for _, u := range us {
		if u.Active {
			role[u.ID] = u.Role
		}
	}
	var keep []BoardPerson
	var dropped []string
	for _, m := range b.Members {
		r := role[m.User]
		ok := r != "" && r != RoleBranch && r != RoleAP && r != RoleClient
		if len(s.Roles) > 0 && r != RoleAdmin {
			ok = contains(s.Roles, r)
		}
		if ok {
			keep = append(keep, m)
		} else {
			dropped = append(dropped, m.User)
		}
	}
	want := keep
	vis := b.Visibility
	if len(s.Roles) > 0 {
		want, vis = o.bsRoleMembers(ctx, tenant, s, keep), "private"
		for _, m := range keep {
			if role[m.User] == RoleAdmin {
				want = append(want, m)
			}
		}
	}
	same := len(want) == len(b.Members) && vis == b.Visibility
	if same {
		old := map[string]string{}
		for _, m := range b.Members {
			old[m.User] = m.Role
		}
		for _, m := range want {
			same = same && old[m.User] == m.Role
		}
	}
	if same {
		return b
	}
	if want == nil {
		want = []BoardPerson{}
	}
	nb, err := docs.Update(ctx, o.Docs, tenant, cBoard, b.ID, bsActor, func(x *Board) error {
		x.Members, x.Visibility = want, vis
		return nil
	})
	if err != nil {
		return b
	}
	if len(dropped) > 0 {
		why := "Ledgerline boards are for head-office staff"
		if len(s.Roles) > 0 {
			why = "only " + strings.Join(s.Roles, ", ") + " and Admin see these records"
		}
		o.boardLog(ctx, tenant, bsActor, nb, "", "board.updated", fmt.Sprintf("took %s off this board: %s", strings.Join(dropped, ", "), why), nil)
	}
	return nb
}

// ---- keeping a board in step ----

func (o *Ops) bsTick(ctx context.Context, tenant string, now time.Time) {
	bs, err := docs.List[Board](ctx, o.Docs, tenant, cBoard, "")
	if err != nil {
		return
	}
	for _, b := range bs {
		if b.Kind == BoardLedgerline && !b.Closed && !b.Deleted {
			o.bsSync(ctx, tenant, b, now, bsTickGap)
		}
	}
}

// bsSync brings a Ledgerline board up to date with its records, unless it
// was within gap, or another sync holds it. It reports whether it ran; a
// failure is kept in the sync state and logged once.
func (o *Ops) bsSync(ctx context.Context, tenant string, b Board, now time.Time, gap time.Duration) (bool, error) {
	src, ok := bsSourceOf(b.Source)
	if !ok || b.Kind != BoardLedgerline || b.Closed || b.Deleted {
		return false, nil
	}
	st, ver, err := docs.Get[bsState](ctx, o.Docs, tenant, cBoardSync, b.ID)
	if errors.Is(err, docs.ErrNotFound) {
		st, ver, err = bsState{Board: b.ID}, 0, nil
	}
	if err != nil {
		return false, err
	}
	if t := bsTime(st.Lock); !t.IsZero() && now.Before(t) {
		return false, nil
	}
	if t := bsTime(st.Synced); gap > 0 && !t.IsZero() && now.Sub(t) >= 0 && now.Sub(t) < gap {
		return false, nil
	}
	prevErr := st.Error
	st.Lock = now.Add(bsLease).Format(time.RFC3339)
	if _, err := docs.Put(ctx, o.Docs, tenant, cBoardSync, b.ID, st, ver, bsActor); err != nil {
		if errors.Is(err, docs.ErrStale) {
			return false, nil
		}
		return false, err
	}
	closed, err := o.bsApply(ctx, tenant, b, src, st.Closed, now)
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	_, uerr := docs.Update(ctx, o.Docs, tenant, cBoardSync, b.ID, bsActor, func(x *bsState) error {
		x.Lock, x.Synced, x.Error = "", now.Format(time.RFC3339), msg
		if err == nil {
			x.Closed = closed
		}
		return nil
	})
	if msg != "" && msg != prevErr {
		o.log(ctx, tenant, bsActor, "board.sync_failed", b.ID, map[string]any{"source": src.ID, "error": msg})
	}
	if err == nil {
		err = uerr
	}
	return true, err
}

// bsBoard is what one sync knows about the board.
type bsBoard struct {
	b       Board
	src     *bsSource
	lists   map[string]BoardList // by status
	source  map[string]string    // list ID → status
	ids     map[string]string    // label category → label ID
	managed map[string]bool      // label IDs the sync owns
	bottom  map[string]float64   // list ID → the last open card's position
	now     time.Time
}

type bsPlan struct {
	c       *Card
	r       *bsRec // nil: the record is gone
	archive bool   // the record left the board's scope
}

type bsChange struct {
	from, to       string // lists, when it moved
	restored       bool
	archived       bool
	added, removed []string // managed labels
	due            string
	dueDone        bool
	status         string // the record's new status, when it changed
	other          bool
}

func (ch bsChange) any() bool {
	return ch.to != "" || ch.restored || ch.archived || len(ch.added) > 0 || len(ch.removed) > 0 || ch.due != "" || ch.dueDone || ch.status != "" || ch.other
}

var errBSSame = errors.New("unchanged")

func (o *Ops) bsApply(ctx context.Context, tenant string, b Board, src *bsSource, closedSeen map[string]string, now time.Time) (map[string]string, error) {
	cl, err := o.ClientMap(ctx, tenant)
	if err != nil {
		return closedSeen, err
	}
	env := &bsEnv{now: now, date: o.businessDate(ctx, tenant), clients: cl}
	recs, err := src.list(ctx, o, tenant, env)
	if err != nil {
		return closedSeen, err
	}
	sort.SliceStable(recs, func(i, j int) bool { return recs[i].Order < recs[j].Order })
	b = o.bsGuard(ctx, tenant, b, src)
	cards, err := o.boardCards(ctx, tenant, b.ID)
	if err != nil {
		return closedSeen, err
	}
	bb := &bsBoard{b: b, src: src, lists: map[string]BoardList{}, source: map[string]string{}, bottom: map[string]float64{}, now: now}
	for _, l := range b.Lists {
		if l.Source != "" && !l.Archived {
			if _, ok := bb.lists[l.Source]; !ok {
				bb.lists[l.Source] = l
			}
			bb.source[l.ID] = l.Source
		}
	}
	byKey := map[string]*Card{}
	for i := range cards {
		c := &cards[i]
		if !c.Archived {
			bb.bottom[c.List] = math.Max(bb.bottom[c.List], c.Pos)
		}
		if c.Link == nil || c.Template || !contains(src.Kinds, c.Link.Kind) {
			continue
		}
		k := c.Link.Kind + "|" + c.Link.ID
		if p := byKey[k]; p != nil && bsPrefer(*p, *c) {
			continue
		}
		byKey[k] = c
	}

	since := src.since(env)
	seen := map[string]string{}
	keys := map[string]bool{}
	var creates []bsRec
	var plans []bsPlan
	for i := range recs {
		r := &recs[i]
		if keys[r.Key] {
			continue
		}
		keys[r.Key] = true
		c := byKey[r.Key]
		at := r.ClosedAt
		if r.Closed && at.IsZero() {
			if t := bsTime(closedSeen[r.Key]); !t.IsZero() {
				at = t
			} else if c != nil && !c.Archived {
				at = now // the sync sees it finish now
			}
			if !at.IsZero() && !at.Before(since) { // remembered while it is on the board
				seen[r.Key] = at.Format(time.RFC3339)
			}
		}
		_, placed := bb.lists[r.Status]
		in := placed && (!r.Closed || (!at.IsZero() && !at.Before(since)))
		switch {
		case c == nil && in:
			creates = append(creates, *r)
		case c == nil:
		case in:
			plans = append(plans, bsPlan{c: c, r: r})
		case !c.Archived:
			plans = append(plans, bsPlan{c: c, r: r, archive: true})
		}
	}
	for k, c := range byKey {
		if !keys[k] && !c.Archived {
			plans = append(plans, bsPlan{c: c})
		}
	}
	sort.Slice(plans, func(i, j int) bool { return plans[i].c.No < plans[j].c.No })

	var need []bsLabel
	for _, r := range creates {
		need = append(need, r.Labels...)
	}
	for _, p := range plans {
		if p.r != nil {
			need = append(need, p.r.Labels...)
		}
	}
	if bb.b, bb.ids, err = o.bsLabels(ctx, tenant, bb.b, need); err != nil {
		return closedSeen, err
	}
	bb.managed = map[string]bool{}
	for k, id := range bb.b.Settings {
		if strings.HasPrefix(k, bsLabelKey) {
			bb.managed[id] = true
		}
	}
	for _, p := range plans {
		if err := o.bsUpdate(ctx, tenant, bb, p); err != nil && !errors.Is(err, errCardGone) {
			return seen, err
		}
	}
	return seen, o.bsCreate(ctx, tenant, bb, creates)
}

// bsPrefer reports whether p is the record's card rather than c: the one
// the sync made, then an open one.
func bsPrefer(p, c Card) bool {
	if (p.CreatedBy == bsActor) != (c.CreatedBy == bsActor) {
		return p.CreatedBy == bsActor
	}
	if p.Archived != c.Archived {
		return !p.Archived
	}
	return p.No < c.No
}

// bsLabels finds the board's label for each category, adding the missing
// ones in one change to the board.
func (o *Ops) bsLabels(ctx context.Context, tenant string, b Board, need []bsLabel) (Board, map[string]string, error) {
	ids := map[string]string{}
	have := map[string]bool{}
	for _, l := range b.Labels {
		have[l.ID] = true
	}
	var missing []bsLabel
	asked := map[string]bool{}
	for _, l := range need {
		if asked[l.Key] {
			continue
		}
		asked[l.Key] = true
		if id := b.Settings[bsLabelKey+l.Key]; id != "" && have[id] {
			ids[l.Key] = id
		} else {
			missing = append(missing, l)
		}
	}
	if len(missing) == 0 {
		return b, ids, nil
	}
	nb, err := docs.Update(ctx, o.Docs, tenant, cBoard, b.ID, bsActor, func(x *Board) error {
		if x.Settings == nil {
			x.Settings = map[string]string{}
		}
		have := map[string]bool{}
		for _, l := range x.Labels {
			have[l.ID] = true
		}
		for _, l := range missing {
			if id := x.Settings[bsLabelKey+l.Key]; id != "" && have[id] {
				ids[l.Key] = id
				continue
			}
			if len(x.Labels) >= 50 {
				continue
			}
			nl := BoardLabel{ID: boardID("LB-", 4), Name: clip(l.Name, 59), Color: firstOf(l.Color, "green")}
			x.Labels = append(x.Labels, nl)
			x.Settings[bsLabelKey+l.Key] = nl.ID
			ids[l.Key] = nl.ID
		}
		return nil
	})
	if err != nil {
		return b, ids, err
	}
	return nb, ids, nil
}

func (bb *bsBoard) labelIDs(r bsRec) []string {
	var out []string
	for _, l := range r.Labels {
		if id := bb.ids[l.Key]; id != "" && !contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// patch brings card x up to date with its plan and says what changed. It
// replaces slices rather than changing them, so it can try a copy first.
func (bb *bsBoard) patch(x *Card, p bsPlan, pos float64) bsChange {
	var ch bsChange
	if p.r == nil { // the record is gone
		if !x.Archived {
			x.Archived, ch.archived = true, true
		}
		return ch
	}
	r := p.r
	if x.Archived && !p.archive {
		if x.Link == nil || x.Link.Status == r.Link.Status {
			return ch // archived by a person, and the record has not changed since
		}
		x.Archived, ch.restored = false, true
	}
	if t := clip(firstOf(strings.TrimSpace(bsScrub(r.Title)), r.Link.ID), 500); x.Title != t {
		x.Title, ch.other = t, true
	}
	mine := bb.labelIDs(*r)
	var keep, cur []string
	for _, id := range x.Labels {
		if bb.managed[id] {
			cur = append(cur, id)
		} else {
			keep = append(keep, id)
		}
	}
	for _, id := range mine {
		if !contains(cur, id) {
			ch.added = append(ch.added, id)
		}
	}
	for _, id := range cur {
		if !contains(mine, id) {
			ch.removed = append(ch.removed, id)
		}
	}
	if len(ch.added) > 0 || len(ch.removed) > 0 {
		x.Labels = append(append([]string{}, mine...), keep...)
	}
	if r.Due != "" && x.Due != r.Due {
		x.Due, x.Reminded, ch.due = r.Due, "", r.Due
	}
	wasDone := contains(bb.src.Closed, bb.source[x.List])
	switch {
	case r.Closed && x.Due != "" && !x.DueDone:
		x.DueDone, ch.dueDone = true, true
	case !r.Closed && x.DueDone && wasDone:
		x.DueDone, ch.other = false, true // reopened
	}
	if x.Link == nil || *x.Link != r.Link {
		if x.Link == nil || x.Link.Status != r.Link.Status {
			ch.status = r.Link.Status
		} else {
			ch.other = true
		}
		l := r.Link
		x.Link = &l
	}
	if to, ok := bb.lists[r.Status]; ok && x.List != to.ID {
		ch.from, ch.to = x.List, to.ID
		x.List, x.Pos = to.ID, pos
	} else if ch.restored {
		x.Pos = pos
	}
	if p.archive && !x.Archived {
		x.Archived, ch.archived = true, true
	}
	return ch
}

func (o *Ops) bsUpdate(ctx context.Context, tenant string, bb *bsBoard, p bsPlan) error {
	pos := 1024.0
	if p.r != nil {
		if to, ok := bb.lists[p.r.Status]; ok {
			pos = bb.bottom[to.ID] + 1024
		}
	}
	try := *p.c
	if !bb.patch(&try, p, pos).any() {
		return nil // nothing to write
	}
	var ch bsChange
	c, err := docs.Update(ctx, o.Docs, tenant, cBoardCard, cardKey(bb.b.ID, p.c.ID), bsActor, func(x *Card) error {
		if x.Deleted {
			return errCardGone
		}
		if ch = bb.patch(x, p, pos); !ch.any() {
			return errBSSame
		}
		x.UpdatedAt = bb.now.Format(time.RFC3339)
		return nil
	})
	if errors.Is(err, errBSSame) {
		return nil
	}
	if err != nil {
		return err
	}
	if ch.to != "" || ch.restored {
		bb.bottom[c.List] = math.Max(bb.bottom[c.List], c.Pos)
	}
	b := bb.b
	ev := func(kind string, data map[string]string) {
		o.boardEvent(ctx, tenant, BoardEvent{Board: b.ID, Card: c.ID, Kind: kind, Actor: bsActor, Data: data})
	}
	name := func(id string) string { l, _ := boardList(b, id); return l.Name }
	switch {
	case ch.archived && p.r == nil:
		o.boardLog(ctx, tenant, bsActor, b, c.ID, "card.archived", fmt.Sprintf("archived #%d: its record is no longer in Ledgerline", c.No), nil)
	case ch.archived:
		o.boardLog(ctx, tenant, bsActor, b, c.ID, "card.archived", fmt.Sprintf("archived #%d: its record finished and has left the board", c.No), nil)
	case ch.to != "":
		o.boardLog(ctx, tenant, bsActor, b, c.ID, "card.moved", fmt.Sprintf("moved #%d from %s to %s", c.No, name(ch.from), name(ch.to)), map[string]string{"from_list": ch.from, "to_list": ch.to})
	case ch.restored:
		o.boardLog(ctx, tenant, bsActor, b, c.ID, "card.restored", fmt.Sprintf("restored #%d: its record changed", c.No), nil)
	default:
		var what []string
		if ch.status != "" {
			what = append(what, "status "+ch.status)
		}
		if ch.due != "" {
			what = append(what, "due "+ch.due)
		}
		if len(ch.added)+len(ch.removed) > 0 {
			what = append(what, "labels")
		}
		o.boardLog(ctx, tenant, bsActor, b, c.ID, "card.updated", fmt.Sprintf("brought #%d up to date%s", c.No, map[bool]string{true: ": " + strings.Join(what, ", "), false: ""}[len(what) > 0]), nil)
	}
	if ch.to != "" {
		o.boardNotifyKind(ctx, tenant, bsActor, b, c.ID, NoticeMoved, append(append([]string(nil), c.Watchers...), c.Members...), fmt.Sprintf("%s moved to %s on %s", clip(c.Title, 80), name(ch.to), b.Name))
		ev("card.moved", map[string]string{"from_list": ch.from, "to_list": ch.to})
	}
	for _, id := range ch.added {
		ev("card.label_added", map[string]string{"label": id})
	}
	for _, id := range ch.removed {
		ev("card.label_removed", map[string]string{"label": id})
	}
	if ch.due != "" {
		ev("card.due_set", map[string]string{"due": ch.due})
	}
	if ch.dueDone {
		ev("card.due_done", nil)
	}
	if ch.archived {
		ev("card.archived", nil)
	}
	if ch.to == "" && !ch.archived && len(ch.added)+len(ch.removed) == 0 && ch.due == "" && !ch.dueDone {
		ev("card.updated", nil)
	}
	return nil
}

// bsCreate adds the cards for new records, numbering them in one change to
// the board.
func (o *Ops) bsCreate(ctx context.Context, tenant string, bb *bsBoard, recs []bsRec) error {
	if len(recs) == 0 {
		return nil
	}
	var start int64
	b, err := docs.Update(ctx, o.Docs, tenant, cBoard, bb.b.ID, bsActor, func(x *Board) error {
		start = x.CardSeq
		x.CardSeq += int64(len(recs))
		x.UpdatedAt = bb.now.Format(time.RFC3339)
		return nil
	})
	if err != nil {
		return err
	}
	bb.b = b
	ts := bb.now.Format(time.RFC3339)
	for i, r := range recs {
		l := bb.lists[r.Status]
		bb.bottom[l.ID] += 1024
		link := r.Link
		c := Card{Board: b.ID, ID: boardID("C-", 6), No: start + int64(i) + 1, List: l.ID, Pos: bb.bottom[l.ID], Title: clip(firstOf(strings.TrimSpace(bsScrub(r.Title)), r.Link.ID), 500),
			Labels: bb.labelIDs(r), Due: r.Due, DueDone: r.Closed && r.Due != "", Link: &link, CreatedBy: bsActor, CreatedAt: ts, UpdatedAt: ts}
		if r.New != nil {
			c.Description, c.Checklists = r.New()
			c.Description = clip(bsScrub(c.Description), 20000)
		}
		if _, err := docs.Put(ctx, o.Docs, tenant, cBoardCard, cardKey(b.ID, c.ID), c, 0, bsActor); err != nil {
			return err
		}
		if len(recs) <= 10 {
			o.boardLog(ctx, tenant, bsActor, b, c.ID, "card.created", fmt.Sprintf("added #%d %s to %s", c.No, clip(c.Title, 80), l.Name), map[string]string{"list": l.ID})
		}
		o.boardEvent(ctx, tenant, BoardEvent{Board: b.ID, Card: c.ID, Kind: "card.created", Actor: bsActor, Data: map[string]string{"list": l.ID}})
	}
	if len(recs) > 10 {
		o.boardLog(ctx, tenant, bsActor, b, "", "card.created", fmt.Sprintf("added %d cards from %s", len(recs), bb.src.Screen), nil)
	}
	return nil
}

// ---- moving a card does the record's work ----

func bsMove(ctx context.Context, o *Ops, tenant, actor string, b Board, c Card, to BoardList) (bool, error) {
	if c.Link == nil || b.Kind != BoardLedgerline {
		return false, nil // a team board's lists are its own
	}
	src, ok := bsSourceOf(b.Source)
	if !ok || !contains(src.Kinds, c.Link.Kind) {
		return false, nil
	}
	if actor == bsActor {
		return true, nil // the sync keeping the card in step
	}
	if strings.HasPrefix(actor, "system:") {
		return true, invalid("automation does not change Ledgerline records: a person moves the card, or works it on %s", src.Screen)
	}
	if to.Source == "" {
		return true, invalid("%s moves when its record changes: open it on %s", c.Link.Label, src.Screen)
	}
	st, err := src.move(ctx, o, tenant, actor, c, to)
	if err != nil {
		return true, err
	}
	if st != "" { // the card shows the record's new status straight away
		_, _ = docs.Update(ctx, o.Docs, tenant, cBoardCard, cardKey(b.ID, c.ID), actor, func(x *Card) error {
			if x.Deleted || x.Link == nil {
				return errBSSame
			}
			x.Link.Status = st
			return nil
		})
	}
	return true, nil
}

func bsWorkMove(ctx context.Context, o *Ops, tenant, actor string, c Card, to BoardList) (string, error) {
	switch to.Source {
	case "Done":
		w, err := o.CloseWorkItem(ctx, tenant, actor, c.Link.ID, false, "Moved to Done on the board")
		return w.Status, err
	case "Cleared":
		return "", invalid("Ledgerline clears a work item itself when it no longer applies; to close it yourself, move it to Done")
	}
	return "", invalid("a closed work item does not reopen; Autopilot raises it again if it is still needed (Automation → Autopilot & work queue)")
}

func bsBreakMove(ctx context.Context, o *Ops, tenant, actor string, c Card, to BoardList) (string, error) {
	switch to.Source {
	case "Resolved":
		return "", invalid("resolve a break with a note saying what was corrected, on Trade reconciliation")
	case "Cleared":
		return "", invalid("a break clears itself when a later reconciliation no longer finds it: correct the OMS and run reconciliation again on Trade reconciliation")
	}
	return "", invalid("a resolved break does not reopen; run reconciliation again on Trade reconciliation")
}

func bsCUSPAMove(ctx context.Context, o *Ops, tenant, actor string, c Card, to BoardList) (string, error) {
	return "", invalid("release or invoke unpaid securities on Settlement → Unpaid securities; the card moves when the line does")
}

// bsNextStage is the stage AdvanceStage moves a client to ("" for none).
func bsNextStage(c Client) string {
	st := c.Status
	if st == "In-person verification" {
		st = "KYC verification"
	}
	for i, s := range Stages {
		if s == st && i < len(Stages)-1 {
			if Stages[i+1] == "UCC upload" && c.DPOnly {
				return "Active"
			}
			return Stages[i+1]
		}
	}
	return ""
}

func bsOnboardingMove(ctx context.Context, o *Ops, tenant, actor string, c Card, to BoardList) (string, error) {
	ucc := c.Link.UCC
	if c.Link.Kind == "client" {
		ucc = firstOf(ucc, c.Link.ID)
	}
	if to.Source == "Rejected" {
		return "", invalid("reject an online application with the reason the applicant will see, on Clients & onboarding → Online applications")
	}
	if ucc == "" {
		return "", invalid("the applicant finishes the online application; it moves to KYC verification when they sign it")
	}
	cl, err := o.Client(ctx, tenant, ucc)
	if err != nil {
		return "", fmt.Errorf("%w: client %s", ErrNotFound, ucc)
	}
	next := bsNextStage(cl)
	if next == "" {
		return "", invalid("%s is %s; onboarding does not move it from there", ucc, cl.Status)
	}
	if to.Source != next {
		return "", invalid("onboarding moves one stage at a time and never back: %s is at %s, so its next stage is %s", ucc, cl.Status, next)
	}
	cl, err = o.AdvanceStage(ctx, tenant, actor, ucc)
	return cl.Status, err
}

func bsTicketMove(ctx context.Context, o *Ops, tenant, actor string, c Card, to BoardList) (string, error) {
	switch to.Source {
	case "Resolved":
		return "", invalid("resolve with the reply to the client, on Investor complaints")
	case "Closed":
		ts, err := o.Tickets(ctx, tenant, o.businessDate(ctx, tenant))
		if err != nil {
			return "", err
		}
		for _, t := range ts {
			if t.ID == c.Link.ID && t.Status != "Resolved" {
				return "", invalid("resolve %s with the reply to the client on Investor complaints before closing it", t.ID)
			}
		}
	}
	t, err := o.TicketUpdate(ctx, tenant, actor, c.Link.ID, "Moved to "+to.Name+" on the board", true, to.Source, "")
	return t.Status, err
}

func bsLeadMove(ctx context.Context, o *Ops, tenant, actor string, c Card, to BoardList) (string, error) {
	switch to.Source {
	case "Converted":
		return "", invalid("convert a lead with the client code it was opened as, on Leads & CRM")
	case "Lost":
		return "", invalid("mark a lead lost with the reason, on Leads & CRM")
	}
	l, _, err := o.ActOnLead(ctx, tenant, actor, c.Link.ID, LeadAction{Kind: "stage", Stage: to.Source, Text: "moved on the board"})
	return l.Stage, err
}

// ---- the boards home ----

// LedgerlineBoardsView is the Ledgerline boards a person sees, with live
// counts, and the sources that can be set up.
type LedgerlineBoardsView struct {
	Boards   []LedgerlineBoardInfo `json:"boards"`
	Sources  []LedgerlineSource    `json:"sources"`
	CanSetUp bool                  `json:"can_set_up"`
}

// LedgerlineBoardInfo is one Ledgerline board with its cards per list.
type LedgerlineBoardInfo struct {
	ID         string               `json:"id"`
	Name       string               `json:"name"`
	Source     string               `json:"source"`
	Background string               `json:"background,omitempty"`
	Closed     bool                 `json:"closed,omitempty"`
	Role       string               `json:"role"`
	Cards      int                  `json:"cards"`
	Overdue    int                  `json:"overdue"`
	Lists      []LedgerlineListInfo `json:"lists"`
	Synced     string               `json:"synced,omitempty"`
	Error      string               `json:"error,omitempty"`
}

// LedgerlineListInfo is a list of a Ledgerline board and its open cards.
type LedgerlineListInfo struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Done   bool   `json:"done,omitempty"` // a finished stage
	Count  int    `json:"count"`
}

// LedgerlineSource is a kind of Ledgerline board.
type LedgerlineSource struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	About  string   `json:"about"`
	Screen string   `json:"screen"`
	Lists  []string `json:"lists"`
	Board  string   `json:"board,omitempty"` // when set up
}

// LedgerlineBoards lists the Ledgerline boards the person sees, brought up
// to date when they were not in the last minute.
func (o *Ops) LedgerlineBoards(ctx context.Context, tenant, actor string) (LedgerlineBoardsView, error) {
	v := LedgerlineBoardsView{Boards: []LedgerlineBoardInfo{}, Sources: []LedgerlineSource{}}
	u, err := o.boardUser(ctx, tenant, actor)
	if err != nil {
		return v, err
	}
	v.CanSetUp = u.Role == RoleAdmin
	all, err := docs.List[Board](ctx, o.Docs, tenant, cBoard, "")
	if err != nil {
		return v, err
	}
	now := o.now()
	for _, s := range bsSources {
		ls := LedgerlineSource{ID: s.ID, Name: s.Name, About: s.About, Screen: s.Screen}
		for _, l := range s.Lists {
			ls.Lists = append(ls.Lists, l.Name)
		}
		b, ok := bsBoardFor(all, s.ID)
		if ok {
			ls.Board = b.ID
		}
		v.Sources = append(v.Sources, ls)
		if !ok {
			continue
		}
		role := boardRole(b, u)
		if role == "" {
			continue
		}
		if !b.Closed {
			o.bsSync(ctx, tenant, b, now, bsTickGap)
			if nb, _, err := docs.Get[Board](ctx, o.Docs, tenant, cBoard, b.ID); err == nil {
				b = nb
			}
		}
		info := LedgerlineBoardInfo{ID: b.ID, Name: b.Name, Source: b.Source, Background: b.Background, Closed: b.Closed, Role: role, Lists: []LedgerlineListInfo{}}
		if st, _, err := docs.Get[bsState](ctx, o.Docs, tenant, cBoardSync, b.ID); err == nil {
			info.Synced, info.Error = st.Synced, st.Error
		}
		count := map[string]int{}
		cs, _ := o.boardCards(ctx, tenant, b.ID)
		done := map[string]bool{}
		for _, l := range b.Lists {
			done[l.ID] = contains(s.Closed, l.Source)
		}
		for _, c := range cs {
			if c.Archived || c.Template {
				continue
			}
			count[c.List]++
			info.Cards++
			if t, ok := boardTime(c.Due); ok && c.Due != "" && !c.DueDone && !done[c.List] && t.Before(now) {
				info.Overdue++
			}
		}
		lists := append([]BoardList(nil), b.Lists...)
		sort.SliceStable(lists, func(i, j int) bool { return lists[i].Pos < lists[j].Pos })
		for _, l := range lists {
			if !l.Archived {
				info.Lists = append(info.Lists, LedgerlineListInfo{ID: l.ID, Name: l.Name, Status: l.Source, Done: done[l.ID], Count: count[l.ID]})
			}
		}
		v.Boards = append(v.Boards, info)
	}
	return v, nil
}

// ---- linking any card to a record ----

// LinkHit is a client or Ledgerline record a card can be linked to, as the
// person may see it.
type LinkHit struct {
	CardLink
	Sub   string   `json:"sub,omitempty"`   // one line about it
	Lines []string `json:"lines,omitempty"` // details, for the card window
	Date  string   `json:"date,omitempty"`  // the record's business date, where its screen is by date
}

// bsSeer is what one person may see.
type bsSeer struct {
	scope func(string) bool // nil: every client
	head  bool              // head-office staff (not branch or partner)
}

func (s bsSeer) client(ucc string) bool { return s.scope == nil || ucc == "" || s.scope(ucc) }

func (o *Ops) bsSeer(ctx context.Context, tenant, actor string) (bsSeer, error) {
	u, err := o.boardUser(ctx, tenant, actor)
	if err != nil {
		return bsSeer{}, err
	}
	return bsSeer{scope: o.ClientScope(ctx, tenant, actor), head: u.Role != RoleBranch && u.Role != RoleAP}, nil
}

func (o *Ops) bsEnvNow(ctx context.Context, tenant string) *bsEnv {
	cl, _ := o.ClientMap(ctx, tenant)
	return &bsEnv{now: o.now(), date: o.businessDate(ctx, tenant), clients: cl}
}

func bsClientHit(env *bsEnv, c Client) LinkHit {
	h := LinkHit{CardLink: CardLink{Kind: "client", ID: c.UCC, Label: clip(bsScrub(env.who(c.UCC)), 120), View: "clients", Status: c.Status, UCC: c.UCC}}
	pan := ""
	if c.PAN != "" {
		pan = "PAN " + MaskPAN(strings.ToUpper(c.PAN))
	}
	h.Sub = bsJoin(pan, c.City, c.Branch)
	return h
}

func bsWorkHit(env *bsEnv, w WorkItem) LinkHit {
	t := w.Title
	if w.UCC != "" && !strings.Contains(t, w.UCC) {
		t += " · " + env.who(w.UCC)
	}
	h := LinkHit{CardLink: CardLink{Kind: "work", ID: w.ID, Label: clip(bsScrub(t), 120), View: w.View, Status: w.Status, UCC: w.UCC}}
	due := ""
	if w.Status == "Open" {
		due = "due " + w.Due
		if w.Overdue {
			due = "overdue since " + w.Due
		}
	}
	h.Sub = bsJoin(w.Status, firstOf(w.Role, "Anyone"), due)
	return h
}

func bsBreakHit(env *bsEnv, x app.BreakRecord) LinkHit {
	st := bsBreakStatus[x.Status]
	return LinkHit{CardLink: CardLink{Kind: "break", ID: x.ID, Label: clip(bsScrub(bsBreakTitle(env, x)), 120), View: "recon", Status: st, UCC: x.UCC},
		Sub: bsJoin(st, strings.TrimSpace(x.Exchange+" "+x.Segment), "trade date "+x.TradeDate), Date: x.TradeDate}
}

func bsCUSPAHit(env *bsEnv, x CUSPARow) LinkHit {
	st := bsCUSPABucket[x.Bucket]
	if x.Bucket == "closed" {
		st = x.CUSPAItem.Status
	}
	return LinkHit{CardLink: CardLink{Kind: "cuspa", ID: x.ID, Label: clip(bsScrub(bsCUSPATitle(env, x)), 120), View: "settlement", Status: st, UCC: x.UCC},
		Sub: bsJoin(st, "day "+strconv.Itoa(x.Day)+" after pay-out", "pay by "+x.PayBy)}
}

func bsTicketHit(env *bsEnv, t Ticket) LinkHit {
	due := ""
	if t.Status != "Resolved" && t.Status != "Closed" {
		due = "due " + t.Due
	}
	return LinkHit{CardLink: CardLink{Kind: "ticket", ID: t.ID, Label: clip(bsScrub(bsJoin(t.ID, t.Subject, env.who(t.UCC))), 120), View: "helpdesk", Status: t.Status, UCC: t.UCC},
		Sub: bsJoin(t.Kind, t.Status, due)}
}

func bsLeadHit(l Lead) LinkHit {
	next := ""
	if l.Next != "" && l.Stage != "Converted" && l.Stage != "Lost" {
		next = "next follow-up " + l.Next
	}
	return LinkHit{CardLink: CardLink{Kind: "lead", ID: l.ID, Label: clip(bsScrub(bsJoin(l.ID+" "+l.Name, l.City)), 120), View: "crm", Status: l.Stage, UCC: l.UCC},
		Sub: bsScrub(bsJoin(l.Stage, l.Source, "owner "+l.Owner, next))}
}

// BoardLinkSearch finds clients (by code, name or full PAN) and records
// (work items, breaks, unpaid securities, tickets, leads) the person may
// see on their own screens, to link a card to. kind narrows it to one.
func (o *Ops) BoardLinkSearch(ctx context.Context, tenant, actor, q, kind string) ([]LinkHit, error) {
	out := []LinkHit{}
	q = strings.TrimSpace(q)
	s, err := o.bsSeer(ctx, tenant, actor)
	if err != nil || utf8.RuneCountInString(q) < 2 {
		return out, err
	}
	if kind != "" && !contains([]string{"client", "work", "break", "cuspa", "ticket", "lead"}, kind) {
		return out, invalid("search clients, work items, breaks, CUSPA lines, tickets or leads")
	}
	lq := strings.ToLower(q)
	match := func(xs ...string) bool {
		for _, x := range xs {
			if x != "" && strings.Contains(strings.ToLower(x), lq) {
				return true
			}
		}
		return false
	}
	want := func(k string) bool { return kind == "" || kind == k }
	per := 8
	if kind != "" {
		per = 30
	}
	env := o.bsEnvNow(ctx, tenant)
	pan := reMaskPAN2.MatchString(q) && len(q) == 10
	if want("client") {
		n := 0
		for _, ucc := range sortedKeys(env.clients) {
			c := env.clients[ucc]
			if n >= per || !s.client(c.UCC) {
				continue
			}
			if match(c.UCC, env.name(c.UCC)) || (pan && strings.EqualFold(c.PAN, q)) {
				out = append(out, bsClientHit(env, c))
				n++
			}
		}
	}
	if want("work") {
		ws, err := o.WorkItems(ctx, tenant, "")
		if err != nil {
			return out, err
		}
		n := 0
		for _, w := range ws { // open first
			if n < per && s.client(w.UCC) && match(w.ID, w.Title, w.UCC, env.name(w.UCC)) {
				out = append(out, bsWorkHit(env, w))
				n++
			}
		}
	}
	if want("break") && s.head {
		bs, err := o.App.Breaks.List(ctx, tenant, "", "")
		if err != nil {
			return out, err
		}
		sort.SliceStable(bs, func(i, j int) bool { return (bs[i].Status == app.BreakOpen) && bs[j].Status != app.BreakOpen })
		n := 0
		for _, x := range bs {
			t, _ := bsBreakType(x.Type)
			if n < per && s.client(x.UCC) && match(x.ID, x.UCC, env.name(x.UCC), x.TradeNo, bsInstrument(x.Instrument), t) {
				out = append(out, bsBreakHit(env, x))
				n++
			}
		}
	}
	if want("cuspa") && s.head {
		v, err := o.CUSPAAgeing(ctx, tenant, env.date)
		if err != nil {
			return out, err
		}
		n := 0
		for _, x := range v.Rows {
			if n < per && s.client(x.UCC) && match(x.ID, x.UCC, env.name(x.UCC), x.Symbol) {
				out = append(out, bsCUSPAHit(env, x))
				n++
			}
		}
	}
	if want("ticket") {
		ts, err := o.Tickets(ctx, tenant, env.date)
		if err != nil {
			return out, err
		}
		n := 0
		for _, t := range ts {
			if n < per && s.client(t.UCC) && match(t.ID, t.Subject, t.UCC, env.name(t.UCC)) {
				out = append(out, bsTicketHit(env, t))
				n++
			}
		}
	}
	if want("lead") {
		if v, err := o.CRM(ctx, tenant, actor, env.date, env.date, env.date); err == nil { // only the leads the person may see
			n := 0
			for _, l := range v.Leads {
				if n < per && s.client(l.UCC) && match(l.ID, l.Name, l.City) {
					out = append(out, bsLeadHit(l))
					n++
				}
			}
		}
	}
	return out, nil
}

var errBSHidden = fmt.Errorf("%w: no such record, or you cannot see it", ErrNotFound)

// BoardLinkRecord is a linked record as it is now, for the card window,
// if the person may see it on its own screen.
func (o *Ops) BoardLinkRecord(ctx context.Context, tenant, actor, kind, id string) (LinkHit, error) {
	s, err := o.bsSeer(ctx, tenant, actor)
	if err != nil {
		return LinkHit{}, err
	}
	env := o.bsEnvNow(ctx, tenant)
	var h LinkHit
	switch kind {
	case "client":
		c, err := o.Client(ctx, tenant, id)
		if err != nil || !s.client(c.UCC) {
			return h, errBSHidden
		}
		h = bsClientHit(env, c)
		h.Lines = []string{bsJoin(c.Type, strings.Join(c.Segments, ", ")), bsJoin(c.Branch, c.Partner)}
		if c.Blocked {
			h.Lines = append(h.Lines, "Blocked by risk")
		}
	case "work":
		ws, err := o.WorkItems(ctx, tenant, "")
		if err != nil {
			return h, err
		}
		for _, w := range ws {
			if w.ID == id && s.client(w.UCC) {
				h = bsWorkHit(env, w)
				h.Lines = []string{w.Detail}
				if d, ok := autoDef(w.Process); ok {
					h.Lines = append(h.Lines, d.Name)
				}
				if w.Proposed != nil && w.Status == "Open" {
					h.Lines = append(h.Lines, "Prepared: "+w.Proposed.Label)
				}
				if w.ClosedBy != "" {
					h.Lines = append(h.Lines, bsJoin("Closed by "+w.ClosedBy, w.Note))
				}
			}
		}
	case "break":
		if !s.head {
			return h, errBSHidden
		}
		bs, err := o.App.Breaks.List(ctx, tenant, "", "")
		if err != nil {
			return h, err
		}
		for _, x := range bs {
			if x.ID == id && s.client(x.UCC) {
				h = bsBreakHit(env, x)
				h.Lines = []string{"Exchange trade " + x.TradeNo + map[bool]string{true: ", OMS " + x.OMSTradeNo, false: ""}[x.OMSTradeNo != ""]}
				for _, d := range x.Diffs {
					h.Lines = append(h.Lines, fmt.Sprintf("%s: exchange %s, OMS %s", d.Field, d.Exchange, d.OMS))
				}
				h.Lines = append(h.Lines, x.Suggestion)
				if x.Resolution != "" {
					h.Lines = append(h.Lines, bsJoin("Resolved by "+x.ResolvedBy, x.Resolution))
				}
			}
		}
	case "cuspa":
		if !s.head {
			return h, errBSHidden
		}
		v, err := o.CUSPAAgeing(ctx, tenant, env.date)
		if err != nil {
			return h, err
		}
		for _, x := range v.Rows {
			if x.ID == id && s.client(x.UCC) {
				h = bsCUSPAHit(env, x)
				h.Lines = []string{x.Next, fmt.Sprintf("Held %d, released %d, invoked %d, sold %d", x.Qty, x.Released, x.Invoked, x.Sold), bsJoin("invoke by "+x.LastInvoke, "the depository releases at the end of "+x.AutoRelease)}
				if x.NoticeOn != "" {
					h.Lines = append(h.Lines, "Notice given on "+x.NoticeOn)
				}
			}
		}
	case "application":
		if !s.head {
			return h, errBSHidden
		}
		as, err := o.Applications(ctx, tenant)
		if err != nil {
			return h, err
		}
		for _, a := range as {
			if a.ID != id || !s.client(a.UCC) || env.hidden(a.UCC) {
				continue
			}
			st := a.Status
			if c, ok := env.clients[a.UCC]; ok && a.UCC != "" {
				st = c.Status
			}
			h = LinkHit{CardLink: CardLink{Kind: "application", ID: a.ID, Label: clip(bsScrub(bsJoin(a.ID, firstOf(env.who(a.UCC), a.Name))), 120), View: "apps", Status: st, UCC: a.UCC},
				Sub: bsJoin("Online application", a.Status)}
			h.Lines = []string{fmt.Sprintf("%d checks passed, %d to check, %d failed", a.count("Passed"), a.count("Check manually"), a.count("Failed"))}
			if a.Reason != "" {
				h.Lines = append(h.Lines, "Rejected: "+a.Reason)
			}
		}
	case "ticket":
		ts, err := o.Tickets(ctx, tenant, env.date)
		if err != nil {
			return h, err
		}
		for _, t := range ts {
			if t.ID == id && s.client(t.UCC) {
				h = bsTicketHit(env, t)
				h.Lines = []string{bsJoin(t.Category, t.Source, t.Ref), bsJoin("assigned to "+firstOf(t.Assignee, "nobody yet"), fmt.Sprintf("%d days old", t.AgeDays))}
				if t.Overdue {
					h.Lines = append(h.Lines, "Overdue")
				}
			}
		}
	case "lead":
		v, err := o.CRM(ctx, tenant, actor, env.date, env.date, env.date)
		if err != nil {
			return h, errBSHidden
		}
		for _, l := range v.Leads {
			if l.ID == id && s.client(l.UCC) {
				h = bsLeadHit(l)
				h.Lines = []string{strings.Join(l.Interest, ", ")}
				if l.LostReason != "" {
					h.Lines = append(h.Lines, "Lost: "+l.LostReason)
				}
			}
		}
	default:
		f, ok := bsLinkKinds[kind]
		if !ok {
			return h, invalid("unknown link kind %q", kind)
		}
		if h, err = f(ctx, o, tenant, actor, s, id); err != nil {
			return h, err
		}
	}
	if h.ID == "" {
		return h, errBSHidden
	}
	var lines []string
	for _, l := range h.Lines {
		if l = strings.TrimSpace(bsScrub(l)); l != "" {
			lines = append(lines, l)
		}
	}
	h.Lines = lines
	h.Sub = bsScrub(h.Sub)
	return h, nil
}
