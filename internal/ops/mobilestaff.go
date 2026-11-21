package ops

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"ledgerline/internal/docs"
)

// ---- the staff app's screens (/m/) ----
//
// Each view follows the desktop's rules exactly: the work items a person
// sees are those of the Autopilot screen for their branch or partner scope
// (an item about a client they may not see is not shown), they act only on
// items for their role (Admin on any), and client data is masked as on the
// desktop (Client.Public). Every call first checks the staff app is open to
// the person (StaffAppAccess).

// MobileItem is a work item on the phone.
type MobileItem struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Detail   string `json:"detail"`
	Area     string `json:"area"`
	Process  string `json:"process"`
	UCC      string `json:"ucc,omitempty"`
	Role     string `json:"role"`
	Due      string `json:"due"`
	Overdue  bool   `json:"overdue"`
	Action   string `json:"action,omitempty"` // the prepared action's label
	Amount   string `json:"amount,omitempty"` // ₹, when the item is about money
	CanAct   bool   `json:"can_act"`
	Status   string `json:"status"`
	Closed   string `json:"closed,omitempty"`
	ClosedBy string `json:"closed_by,omitempty"`
	Note     string `json:"note,omitempty"`
}

// staffCtx is the person and what they may see.
type staffCtx struct {
	user, role string
	scope      func(string) bool
}

func (o *Ops) staffFor(ctx context.Context, tenant, actor string) (staffCtx, error) {
	if err := o.StaffAppAccess(ctx, tenant, actor); err != nil {
		return staffCtx{}, err
	}
	role, err := o.RoleOf(ctx, tenant, actor)
	if err != nil {
		return staffCtx{}, err
	}
	return staffCtx{user: actor, role: role, scope: o.ClientScope(ctx, tenant, actor)}, nil
}

func (s staffCtx) sees(w WorkItem) bool { return w.UCC == "" || s.scope == nil || s.scope(w.UCC) }
func (s staffCtx) mine(w WorkItem) bool {
	return s.role == RoleAdmin || w.Role == "" || w.Role == s.role
}
func (s staffCtx) canAct(w WorkItem) bool {
	return s.role == RoleAdmin || w.Role == "" || w.Role == s.role
}
func (s staffCtx) headOffice() bool {
	return s.role != RoleBranch && s.role != RoleAP
}

var reRupees = regexp.MustCompile(`₹\s?[0-9][0-9,]*(\.[0-9]+)?`)

// itemAmount is the money a work item is about: the payout or the bank
// credit it acts on, else the first rupee amount in its detail.
func (o *Ops) itemAmount(ctx context.Context, tenant string, w WorkItem) string {
	if p := w.Proposed; p != nil {
		switch p.Kind {
		case "payout.approve":
			if x, _, err := docs.Get[PayoutRequest](ctx, o.Docs, tenant, cPayout, p.Args["id"]); err == nil {
				return "₹" + formatINR(x.Amount.Dec())
			}
		case "credit.identify", "credit.return":
			if x, _, err := docs.Get[BankTxn](ctx, o.Docs, tenant, cBankTxn, p.Args["id"]); err == nil {
				return "₹" + formatINR(x.Amount.Dec())
			}
		}
	}
	if m := reRupees.FindString(w.Detail); m != "" {
		return strings.ReplaceAll(m, " ", "")
	}
	return ""
}

func (o *Ops) mobileItem(ctx context.Context, tenant string, s staffCtx, w WorkItem, names map[string]AutoDef) MobileItem {
	d := names[w.Process]
	it := MobileItem{ID: w.ID, Title: w.Title, Detail: w.Detail, Area: firstOf(d.Area, "Other"), Process: firstOf(d.Name, w.Process), UCC: w.UCC, Role: w.Role,
		Due: w.Due, Overdue: w.Overdue, CanAct: s.canAct(w), Status: w.Status, Closed: w.Closed, ClosedBy: w.ClosedBy, Note: w.Note}
	if w.Proposed != nil {
		it.Action = w.Proposed.Label
	}
	it.Amount = o.itemAmount(ctx, tenant, w)
	return it
}

func autoDefNames() map[string]AutoDef {
	m := map[string]AutoDef{}
	for _, d := range AutoDefs {
		m[d.ID] = d
	}
	return m
}

// staffOpen is the open work the person sees and may act on.
func (o *Ops) staffOpen(ctx context.Context, tenant string, s staffCtx) ([]WorkItem, error) {
	ws, err := o.WorkItems(ctx, tenant, "Open")
	if err != nil {
		return nil, err
	}
	out := []WorkItem{}
	for _, w := range ws {
		if s.sees(w) && s.mine(w) {
			out = append(out, w)
		}
	}
	return out, nil
}

// staffWaitingCount is how many prepared actions wait for the person (the
// count a push says).
func (o *Ops) staffWaitingCount(ctx context.Context, tenant, user string) int {
	s, err := o.staffFor(ctx, tenant, user)
	if err != nil {
		return 0
	}
	ws, _ := o.staffOpen(ctx, tenant, s)
	n := 0
	for _, w := range ws {
		if w.Proposed != nil && s.canAct(w) {
			n++
		}
	}
	return n
}

// MobileApprovals is the Approvals tab: prepared actions waiting for the
// person, and what was closed today.
type MobileApprovals struct {
	Waiting []MobileItem `json:"waiting"`
	Done    []MobileItem `json:"done"`
}

// StaffApprovals lists the approvals of the person.
func (o *Ops) StaffApprovals(ctx context.Context, tenant, actor, date string) (MobileApprovals, error) {
	s, err := o.staffFor(ctx, tenant, actor)
	if err != nil {
		return MobileApprovals{}, err
	}
	open, err := o.staffOpen(ctx, tenant, s)
	if err != nil {
		return MobileApprovals{}, err
	}
	names := autoDefNames()
	v := MobileApprovals{Waiting: []MobileItem{}, Done: []MobileItem{}}
	for _, w := range open {
		if w.Proposed != nil && s.canAct(w) {
			v.Waiting = append(v.Waiting, o.mobileItem(ctx, tenant, s, w, names))
		}
	}
	sort.SliceStable(v.Waiting, func(i, j int) bool {
		if v.Waiting[i].Overdue != v.Waiting[j].Overdue {
			return v.Waiting[i].Overdue
		}
		return v.Waiting[i].Due < v.Waiting[j].Due
	})
	all, _ := o.WorkItems(ctx, tenant, "")
	today := o.now().Format("2006-01-02")
	for _, w := range all {
		if w.Status == "Open" || w.Status == "Cleared" || !s.sees(w) || !s.mine(w) || w.Proposed == nil {
			continue
		}
		if strings.HasPrefix(w.Closed, today) || w.Date == date {
			v.Done = append(v.Done, o.mobileItem(ctx, tenant, s, w, names))
		}
	}
	sort.Slice(v.Done, func(i, j int) bool { return v.Done[i].Closed > v.Done[j].Closed })
	if len(v.Done) > 50 {
		v.Done = v.Done[:50]
	}
	return v, nil
}

// StaffWork is the work queue: open items for the person that need doing
// on a screen (no prepared action), overdue first.
func (o *Ops) StaffWork(ctx context.Context, tenant, actor string) ([]MobileItem, error) {
	s, err := o.staffFor(ctx, tenant, actor)
	if err != nil {
		return nil, err
	}
	open, err := o.staffOpen(ctx, tenant, s)
	if err != nil {
		return nil, err
	}
	names := autoDefNames()
	out := []MobileItem{}
	for _, w := range open {
		if w.Proposed == nil {
			out = append(out, o.mobileItem(ctx, tenant, s, w, names))
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Overdue != out[j].Overdue {
			return out[i].Overdue
		}
		return out[i].Due < out[j].Due
	})
	return out, nil
}

// StaffDecide approves an item's prepared action, or rejects (or closes)
// it with a reason. It is the desktop's work-queue action with the same
// role check, plus the person's branch or partner scope.
func (o *Ops) StaffDecide(ctx context.Context, tenant, actor, id, decision, reason string) (MobileItem, error) {
	s, err := o.staffFor(ctx, tenant, actor)
	if err != nil {
		return MobileItem{}, err
	}
	w, _, err := docs.Get[WorkItem](ctx, o.Docs, tenant, cWorkItem, id)
	if err != nil || !s.sees(w) {
		return MobileItem{}, ErrNotFound
	}
	switch decision {
	case "approve":
		if w.Proposed == nil {
			return MobileItem{}, invalid("nothing is prepared for this item; do it on the %s screen on a computer", firstOf(w.View, "work queue"))
		}
		w, err = o.CloseWorkItem(ctx, tenant, actor, id, true, firstOf(strings.TrimSpace(reason), "Approved in the staff app"))
	case "reject", "close":
		if strings.TrimSpace(reason) == "" {
			return MobileItem{}, invalid("say why")
		}
		w, err = o.CloseWorkItem(ctx, tenant, actor, id, false, strings.TrimSpace(reason))
	default:
		return MobileItem{}, invalid("decision is approve, reject or close")
	}
	if err != nil {
		return MobileItem{}, err
	}
	o.log(ctx, tenant, actor, "mobile.decision", id, map[string]any{"decision": decision})
	return o.mobileItem(ctx, tenant, s, w, autoDefNames()), nil
}

// ---- the day ----

// MobileStep is one step of the day, as on the desktop runbook.
type MobileStep struct {
	ID      string `json:"id"`
	Session string `json:"session"`
	At      string `json:"at"`
	Name    string `json:"name"`
	State   string `json:"state"` // done, needs, wait, na
	Note    string `json:"note"`
	Who     string `json:"who"`
}

// MobileDay is the Today tab.
type MobileDay struct {
	Date     string       `json:"date"`
	Scoped   bool         `json:"scoped"` // branch staff and partners: no firm-wide steps
	Steps    []MobileStep `json:"steps"`
	Done     int          `json:"done"`
	All      int          `json:"all"`
	Needs    int          `json:"needs"`
	Glance   [][2]string  `json:"glance,omitempty"`
	Sessions [][3]string  `json:"sessions"`
}

type dayStep struct {
	id, s, at, name, who string
	pol                  []string
	check                func(d Dashboard) (state, note string)
}

func countOf(m map[string]int, ks ...string) int {
	n := 0
	for _, k := range ks {
		n += m[k]
	}
	return n
}

// daySteps follows LY_STEPS and LY_CHECK in web/layouts.js.
var daySteps = []dayStep{
	{"files", "bod", "07:30", "Clearing and depository files", RoleMaker, []string{"intake"}, func(d Dashboard) (string, string) {
		if d.Trades > 0 {
			return "done", fmt.Sprintf("%d trades, %d OMS fills", d.Trades, d.OMSFills)
		}
		return "wait", "No trades loaded yet"
	}},
	{"blocks", "bod", "08:15", "Debit blocks and front-office limits", RoleRisk, []string{"debit_blocks"}, nil},
	{"alloc", "bod", "08:30", "Collateral and clearing deposits", RoleRisk, []string{"deposits"}, nil},
	{"duties", "bod", "08:45", "Circulars, calendar and follow-ups", RoleCompliance, []string{"compliance", "leads"}, nil},
	{"money", "intraday", "11:00", "Receipts, payouts and pledges", RoleChecker, []string{"third_party", "unidentified", "payouts", "pledges"}, func(d Dashboard) (string, string) {
		if n := countOf(d.Bank, "Third-party", "Unidentified") + d.Pledges; n > 0 {
			return "needs", fmt.Sprintf("%s to look at", countWords(n, "credit or pledge", "credits or pledges"))
		}
		if d.Bank["Matched"] > 0 {
			return "done", fmt.Sprintf("%d credits matched", d.Bank["Matched"])
		}
		return "", ""
	}},
	{"margin", "intraday", "13:30", "Margin calls and square-off", RoleRisk, []string{"margin_calls", "square_off", "mtf_liquidation"}, func(d Dashboard) (string, string) {
		if n := d.Risk["Breach"]; n > 0 {
			return "needs", countWords(n, "client", "clients") + " in breach"
		}
		if d.Risk["WithMargin"] > 0 {
			return "done", "No breaches"
		}
		return "", ""
	}},
	{"recon", "eod", "16:00", "Trade import and reconciliation", RoleChecker, []string{"recon"}, func(d Dashboard) (string, string) {
		if d.Recon["total"] == 0 {
			if d.Trades > 0 {
				return "wait", "Not run yet"
			}
			return "wait", "No trades loaded"
		}
		if n := d.Recon["OPEN"]; n > 0 {
			return "needs", countWords(n, "break", "breaks") + " open"
		}
		return "done", fmt.Sprintf("%d found, all cleared", d.Recon["total"])
	}},
	{"billing", "eod", "16:15", "Billing and ledger posting", RoleChecker, []string{"billing", "interest_daily"}, func(d Dashboard) (string, string) {
		if d.Billed {
			return "done", "Bills posted"
		}
		if d.Trades > 0 {
			return "wait", "After reconciliation"
		}
		return "wait", "No trades loaded"
	}},
	{"payfile", "eod", "16:05", "Payout bank file", RoleChecker, []string{"payout_batch", "ras"}, nil},
	{"settle", "eod", "16:30", "Settlement and unpaid securities", RoleChecker, []string{"settlement"}, func(d Dashboard) (string, string) {
		if n := countOf(d.Settlement, "Short delivery", "Funds shortfall", "Unpledge & pay-in"); n > 0 {
			return "needs", countWords(n, "exception", "exceptions")
		}
		if d.Billed {
			return "done", "Ready for pay-in"
		}
		return "", ""
	}},
	{"notes", "eod", "17:00", "Contract notes", RoleMaker, []string{"notes"}, func(d Dashboard) (string, string) {
		t, s := d.Notes["total"], d.Notes["Sent"]
		switch {
		case t == 0:
			return "wait", "Not generated yet"
		case t > s:
			note := fmt.Sprintf("%d of %d sent", s, t)
			if b := d.Notes["Bounced"]; b > 0 {
				note += fmt.Sprintf(" · %d bounced", b)
			}
			return "needs", note
		}
		return "done", fmt.Sprintf("%d sent", t)
	}},
	{"penalty", "eod", "17:15", "Margin penalties", RoleRisk, []string{"penalties"}, func(d Dashboard) (string, string) {
		if d.Penalty.Sign() > 0 {
			return "wait", "₹" + formatINR(d.Penalty) + " estimated · approval on Margin reporting"
		}
		if d.Trades > 0 {
			return "done", "No short collection"
		}
		return "", ""
	}},
	{"reports", "eod", "17:45", "Regulatory reports", RoleCompliance, []string{"reports"}, func(d Dashboard) (string, string) {
		if d.ReportsDue > 0 {
			return "needs", countWords(d.ReportsDue, "report", "reports") + " not uploaded"
		}
		return "", ""
	}},
	{"surv", "eod", "18:00", "Surveillance, KYC and NRI checks", RoleCompliance, []string{"surveillance", "kyc", "nri", "inactive"}, func(d Dashboard) (string, string) {
		if n := d.Alerts["open"]; n > 0 {
			return "needs", fmt.Sprintf("%d open alerts · %d high", n, d.Alerts["high"])
		}
		return "", ""
	}},
	{"close", "eod", "18:30", "Equity close", RoleChecker, []string{"day_close"}, func(d Dashboard) (string, string) {
		for _, p := range d.EOD.Parts {
			if p.ID != "equity" {
				continue
			}
			if p.Closed {
				return "done", "Closed " + clock(p.ClosedAt)
			}
			bad := 0
			for _, c := range p.Checks {
				if !c.OK {
					bad++
				}
			}
			if bad > 0 {
				return "wait", "Waits on " + countWords(bad, "check", "checks")
			}
			return "wait", "Ready to close"
		}
		if d.EOD.Closed {
			return "done", "Closed"
		}
		return "", ""
	}},
	{"mcx", "commodity", "23:45", "Commodity close", RoleChecker, []string{"commodity_close"}, func(d Dashboard) (string, string) {
		for _, p := range d.EOD.Parts {
			if p.ID == "commodity" {
				if !p.Needed {
					return "na", "No commodity session"
				}
				if p.Closed {
					return "done", "Closed"
				}
				return "wait", "After the MCX session"
			}
		}
		return "na", "No commodity session"
	}},
}

func clock(t string) string {
	if len(t) >= 16 {
		return t[11:16]
	}
	return t
}

// StaffDay is the day's progress for the head office; branch staff and
// partners get only their own counts (the firm's steps are not theirs).
func (o *Ops) StaffDay(ctx context.Context, tenant, actor, date string) (MobileDay, error) {
	s, err := o.staffFor(ctx, tenant, actor)
	if err != nil {
		return MobileDay{}, err
	}
	v := MobileDay{Date: date, Steps: []MobileStep{}, Sessions: [][3]string{{"bod", "Before open", "06:55–09:15"}, {"intraday", "Market hours", "09:15–15:30"}, {"eod", "After close", "15:30–18:45"}, {"commodity", "Commodity close", "after the MCX session"}}}
	if !s.headOffice() {
		v.Scoped = true
		return v, nil
	}
	d, err := o.Dashboard(ctx, tenant, date)
	if err != nil {
		return v, err
	}
	open, _ := o.WorkItems(ctx, tenant, "Open")
	runs, _ := docs.List[AutoRun](ctx, o.Docs, tenant, cAutoRun, "")
	for _, st := range daySteps {
		items := 0
		for _, w := range open {
			if contains(st.pol, w.Process) {
				items++
			}
		}
		ran := ""
		for _, r := range runs {
			if r.Date == date && contains(st.pol, r.Policy) && r.At > ran {
				ran = r.At
			}
		}
		state, note := "", ""
		if st.check != nil {
			state, note = st.check(d)
		}
		switch {
		case state == "na":
		case state == "needs" || items > 0:
			if state != "needs" {
				note = countWords(items, "item", "items") + " in the queue"
			}
			state = "needs"
		case state == "done" || (state == "" && ran != ""):
			state = "done"
			if note == "" {
				note = "Done " + clock(ran)
			}
		default:
			state = "wait"
			if note == "" {
				note = "Not yet"
				if st.s == "bod" || st.s == "intraday" {
					note = "Not run yet"
				}
			}
		}
		v.Steps = append(v.Steps, MobileStep{ID: st.id, Session: st.s, At: st.at, Name: st.name, State: state, Note: note, Who: st.who})
		if state != "na" {
			v.All++
		}
		if state == "done" {
			v.Done++
		}
		if state == "needs" {
			v.Needs++
		}
	}
	sort.SliceStable(v.Steps, func(i, j int) bool {
		si, sj := sessRank[v.Steps[i].Session], sessRank[v.Steps[j].Session]
		if si != sj {
			return si < sj
		}
		return v.Steps[i].At < v.Steps[j].At
	})
	cover := "—"
	if d.Funds.Cover != 0 {
		cover = fmt.Sprintf("%.2f×", d.Funds.Cover)
	}
	v.Glance = [][2]string{{"Turnover", "₹" + formatINR(d.Turnover)}, {"Margin collected / required", "₹" + formatINR(d.Collected) + " / ₹" + formatINR(d.Required)}, {"Client funds cover", cover}}
	if n := d.Risk["Breach"]; n > 0 {
		v.Glance = append(v.Glance, [2]string{"Clients in margin breach", fmt.Sprint(n)})
	}
	return v, nil
}

var sessRank = map[string]int{"bod": 0, "intraday": 1, "eod": 2, "commodity": 3}

// ---- alerts and exceptions ----

// MobileAlert is one line of the Alerts tab.
type MobileAlert struct {
	Kind     string `json:"kind"`
	Title    string `json:"title"`
	Detail   string `json:"detail,omitempty"`
	Count    int    `json:"count,omitempty"`
	Severity string `json:"severity"` // High, Medium, Low
	UCC      string `json:"ucc,omitempty"`
	Date     string `json:"date,omitempty"`
	ID       string `json:"id,omitempty"`
}

// StaffAlerts lists the day's exceptions and open surveillance alerts the
// person would see on the desktop: the firm's for the head office (the
// dashboard counts), the person's own clients for branch staff and
// partners, and the alert register for the compliance officer and Admin.
func (o *Ops) StaffAlerts(ctx context.Context, tenant, actor, date string) ([]MobileAlert, error) {
	s, err := o.staffFor(ctx, tenant, actor)
	if err != nil {
		return nil, err
	}
	out := []MobileAlert{}
	add := func(kind, title, sev string, n int) {
		if n > 0 {
			out = append(out, MobileAlert{Kind: kind, Title: title, Severity: sev, Count: n})
		}
	}
	open, _ := o.staffOpen(ctx, tenant, s)
	od := 0
	for _, w := range open {
		if w.Overdue {
			od++
		}
	}
	add("overdue", "Overdue work", "High", od)
	if s.headOffice() {
		d, err := o.Dashboard(ctx, tenant, date)
		if err != nil {
			return nil, err
		}
		add("margin", "Clients in margin breach", "High", d.Risk["Breach"])
		add("recon", "Reconciliation breaks open", "Medium", d.Recon["OPEN"])
		add("bank", "Third-party or unidentified credits", "Medium", countOf(d.Bank, "Third-party", "Unidentified"))
		add("pledges", "Pledge requests to decide", "Low", d.Pledges)
		add("settlement", "Settlement exceptions", "High", countOf(d.Settlement, "Short delivery", "Funds shortfall", "Unpledge & pay-in"))
		add("notes", "Contract notes bounced", "Medium", d.Notes["Bounced"])
		add("reports", "Regulatory reports not uploaded", "Medium", d.ReportsDue)
		add("surv", "Open surveillance alerts", "Medium", d.Alerts["open"])
		for _, w := range d.Warnings {
			out = append(out, MobileAlert{Kind: "warning", Title: w, Severity: "Low"})
		}
	} else {
		rv, err := o.Risk(ctx, tenant, date)
		if err == nil {
			n := 0
			for _, r := range rv.Rows {
				if r.Status == "Breach" && (s.scope == nil || s.scope(r.UCC)) {
					n++
				}
			}
			add("margin", "Your clients in margin breach", "High", n)
		}
	}
	if s.role == RoleCompliance || s.role == RoleAdmin {
		as, _ := o.Alerts(ctx, tenant)
		k := 0
		for _, a := range as {
			if a.Status != "Open" && a.Status != "Under review" {
				continue
			}
			if k++; k > 30 {
				break
			}
			out = append(out, MobileAlert{Kind: "alert", ID: a.ID, Title: a.Type, Detail: a.Detail, Severity: a.Severity, UCC: a.UCC, Date: a.Date})
		}
	}
	return out, nil
}

// ---- client look-up ----

// MobileClient is a client in the look-up, masked as on the desktop.
type MobileClient struct {
	UCC        string   `json:"ucc"`
	Name       string   `json:"name"`
	PAN        string   `json:"pan"` // masked
	Status     string   `json:"status"`
	RiskStatus string   `json:"risk_status,omitempty"`
	Funds      string   `json:"funds"`
	Branch     string   `json:"branch,omitempty"`
	Partner    string   `json:"partner,omitempty"`
	Mobile     string   `json:"mobile,omitempty"`
	Email      string   `json:"email,omitempty"`
	Segments   []string `json:"segments,omitempty"`
	Blocked    bool     `json:"blocked,omitempty"`
}

// StaffClients finds clients by name, UCC or PAN among those the person
// may see (at most 30).
func (o *Ops) StaffClients(ctx context.Context, tenant, actor, date, q string) ([]MobileClient, error) {
	s, err := o.staffFor(ctx, tenant, actor)
	if err != nil {
		return nil, err
	}
	q = strings.ToLower(strings.TrimSpace(q))
	if len(q) < 2 {
		return []MobileClient{}, nil
	}
	cs, err := o.Clients(ctx, tenant)
	if err != nil {
		return nil, err
	}
	funds, _ := o.clientFunds(ctx, tenant, date, "")
	risk := map[string]string{}
	if rv, err := o.Risk(ctx, tenant, date); err == nil {
		for _, r := range rv.Rows {
			risk[r.UCC] = r.Status
		}
	}
	out := []MobileClient{}
	for _, c := range cs {
		if s.scope != nil && !s.scope(c.UCC) {
			continue
		}
		// The full PAN is matched here, on the server, and never sent.
		if !strings.Contains(strings.ToLower(c.UCC+" "+c.Name+" "+c.PAN), q) {
			continue
		}
		p := c.Public()
		out = append(out, MobileClient{UCC: p.UCC, Name: p.Name, PAN: p.PAN, Status: p.Status, RiskStatus: risk[p.UCC], Funds: "₹" + formatINR(funds[p.UCC].Dec()),
			Branch: p.Branch, Partner: p.Partner, Mobile: p.Mobile, Email: p.Email, Segments: p.Segments, Blocked: p.Blocked})
		if len(out) >= 30 {
			break
		}
	}
	o.log(ctx, tenant, actor, "mobile.client_search", "", map[string]any{"found": len(out)}) // not the words searched: they can be a PAN
	return out, nil
}

// MobileHome is the staff app's first screen: counts only.
type MobileHome struct {
	User      string `json:"user"`
	Name      string `json:"name"`
	Role      string `json:"role"`
	Firm      string `json:"firm"`
	Date      string `json:"date"`
	Approvals int    `json:"approvals"`
	Work      int    `json:"work"`
	Overdue   int    `json:"overdue"`
	Scoped    bool   `json:"scoped"`
	Device    string `json:"device,omitempty"`
}

// StaffHome counts what waits for the person.
func (o *Ops) StaffHome(ctx context.Context, tenant, actor, date string) (MobileHome, error) {
	s, err := o.staffFor(ctx, tenant, actor)
	if err != nil {
		return MobileHome{}, err
	}
	u, _ := o.user(ctx, tenant, actor)
	set, _ := o.Settings(ctx, tenant)
	v := MobileHome{User: actor, Name: u.Name, Role: s.role, Firm: set.Firm, Date: date, Scoped: !s.headOffice()}
	open, err := o.staffOpen(ctx, tenant, s)
	if err != nil {
		return v, err
	}
	for _, w := range open {
		if w.Proposed != nil && s.canAct(w) {
			v.Approvals++
		} else if w.Proposed == nil {
			v.Work++
		}
		if w.Overdue {
			v.Overdue++
		}
	}
	return v, nil
}
