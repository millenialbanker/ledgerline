package ops

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
)

// ---- automations (robotic process automation) ----
//
// Staff record a task once, in a browser window that the Ledgerline bot
// opens on their own computer: logging in to an exchange or depository
// portal, downloading the day's files, uploading them into Ledgerline,
// filling a form. Ledgerline keeps the recording as a workflow of steps
// they can edit, share, run again on any of their computers, or schedule.
//
// Passwords are kept in an encrypted vault, never inside a workflow: a step
// refers to a saved password, and the bot gets it from Ledgerline only
// while a run is at that step, with each use in the audit trail. Captchas
// and one-time passwords sent by SMS are left to a person: the run pauses
// and asks them, through Ledgy, to finish that step.

const (
	cRPAFlow  = "rpaflow"
	cRPARun   = "rparun"
	cRPARec   = "rparec"
	cRPAAgent = "rpaagent"
	cRPACred  = "rpacred"
	cRPAPair  = "rpapair"

	rpaMaxSteps    = 300
	rpaPairMinutes = 10
	// rpaOnline is how recently a computer must have called in to count as on.
	rpaOnline = 90 * time.Second
	// rpaStall is how long a running workflow may go without word from its
	// computer before it is marked failed.
	rpaStall = 15 * time.Minute
)

// RPAStepKinds are the steps a workflow can have.
var RPAStepKinds = []struct {
	Kind, Name, About string
}{
	{"open", "Open a page", "Go to a web address"},
	{"click", "Click", "Click a button, link or anything on the page"},
	{"type", "Type", "Type into a box; can use a saved password, a date or a value from an earlier step"},
	{"select", "Choose from a list", "Pick an option in a drop-down"},
	{"check", "Tick or untick", "Set a tick box"},
	{"press", "Press a key", "Enter, Tab, Escape, arrow keys…"},
	{"wait_for", "Wait for", "Wait until something appears on the page"},
	{"wait", "Wait", "Wait a number of seconds"},
	{"extract", "Read text", "Read text from the page into a value for later steps"},
	{"assert", "Check the page", "Stop the run unless the page shows something"},
	{"download", "Download a file", "Click something that downloads a file, and keep the file for later steps"},
	{"upload", "Upload a file", "Put a file kept earlier into a file box on the page"},
	{"ledgerline_upload", "Load into Ledgerline", "Load a downloaded file into Ledgerline as a file type (trades, obligations, bhavcopy…)"},
	{"pause", "Ask me", "Pause until a person does something (a captcha, an SMS one-time password) and clicks Continue"},
	{"notify", "Message me", "Send a message through Ledgy"},
}

// RPATarget says which element on a page a step acts on. The bot tries the
// CSS selector first and then the visible text, label and role, so small
// changes to a page do not break the workflow.
type RPATarget struct {
	CSS         string `json:"css,omitempty"`
	Text        string `json:"text,omitempty"`
	Role        string `json:"role,omitempty"`
	Name        string `json:"name,omitempty"` // accessible name or name attribute
	Tag         string `json:"tag,omitempty"`
	Label       string `json:"label,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
	Type        string `json:"type,omitempty"` // input type
}

// RPAStep is one step of a workflow.
type RPAStep struct {
	ID       string     `json:"id"`
	Kind     string     `json:"kind"`
	Label    string     `json:"label,omitempty"`
	URL      string     `json:"url,omitempty"`
	Target   *RPATarget `json:"target,omitempty"`
	Value    string     `json:"value,omitempty"` // may use {{name}}, {{tradedate:DD-MM-YYYY}}, {{secret:<id>.password}}
	Key      string     `json:"key,omitempty"`
	Var      string     `json:"var,omitempty"` // value or file kept by extract/download; file used by upload/ledgerline_upload
	Source   string     `json:"source,omitempty"`
	Seconds  int        `json:"seconds,omitempty"`
	Timeout  int        `json:"timeout,omitempty"` // seconds to find the element (default 20)
	Optional bool       `json:"optional,omitempty"`
	Message  string     `json:"message,omitempty"`
	// NeedsSecret marks a recorded password or one-time-password box that
	// still needs a saved password chosen for it.
	NeedsSecret string `json:"needs_secret,omitempty"` // password, username, otp
}

// RPASchedule runs a workflow by itself.
type RPASchedule struct {
	Active bool   `json:"active"`
	At     string `json:"at"`    // HH:MM, IST
	When   string `json:"when"`  // business, daily, weekly:Mon, monthly:1
	Agent  string `json:"agent"` // the owner's computer to run on
}

// RPAFlow is a saved workflow.
type RPAFlow struct {
	ID         string       `json:"id"`
	Name       string       `json:"name"`
	About      string       `json:"about,omitempty"`
	Owner      string       `json:"owner"`
	Shared     bool         `json:"shared"` // others can see and run it (with their own computer and passwords)
	Steps      []RPAStep    `json:"steps"`
	Schedule   *RPASchedule `json:"schedule,omitempty"`
	Version    int          `json:"version"`
	Created    string       `json:"created"`
	Updated    string       `json:"updated"`
	LastRun    string       `json:"last_run,omitempty"`
	LastStatus string       `json:"last_status,omitempty"`
	LastDate   string       `json:"last_date,omitempty"` // the day the schedule last ran it
}

// RPAStepResult is what happened at one step of a run.
type RPAStepResult struct {
	Step    int    `json:"step"` // index into the workflow's steps
	ID      string `json:"id"`
	Status  string `json:"status"` // Done, Skipped, Failed, Paused
	Message string `json:"message,omitempty"`
	Ms      int64  `json:"ms,omitempty"`
	At      string `json:"at"`
}

// RPARun is one run of a workflow on a computer.
type RPARun struct {
	ID       string            `json:"id"`
	Flow     string            `json:"flow"`
	FlowName string            `json:"flow_name"`
	Version  int               `json:"version"`
	Steps    []RPAStep         `json:"steps"` // the workflow as it was when the run started
	User     string            `json:"user"`  // whose passwords and permissions it uses
	Agent    string            `json:"agent"`
	Trigger  string            `json:"trigger"` // manual, schedule
	Status   string            `json:"status"`  // Queued, Running, Paused, Succeeded, Failed, Cancelled
	Results  []RPAStepResult   `json:"results"`
	Pause    string            `json:"pause,omitempty"` // what the person is asked to do
	Resume   bool              `json:"resume,omitempty"`
	Cancel   bool              `json:"cancel,omitempty"`
	Error    string            `json:"error,omitempty"`
	Shot     string            `json:"shot,omitempty"` // raw file of the screen when it failed
	Context  map[string]string `json:"context"`        // dates for templates
	Queued   string            `json:"queued"`
	Started  string            `json:"started,omitempty"`
	Ended    string            `json:"ended,omitempty"`
	Seen     string            `json:"seen,omitempty"` // last word from the computer
}

// RPARecording is a recording in progress.
type RPARecording struct {
	ID       string    `json:"id"`
	User     string    `json:"user"`
	Agent    string    `json:"agent"`
	Flow     string    `json:"flow,omitempty"` // add to this workflow, or a new one
	Name     string    `json:"name"`
	StartURL string    `json:"start_url"`
	Status   string    `json:"status"` // Waiting, Recording, Done, Cancelled
	Steps    []RPAStep `json:"steps"`
	Stop     bool      `json:"stop,omitempty"`
	Saved    string    `json:"saved,omitempty"` // the workflow it became
	Created  string    `json:"created"`
	Seen     string    `json:"seen,omitempty"`
}

// RPAAgent is a computer paired with Ledgerline to run workflows.
type RPAAgent struct {
	ID        string `json:"id"`
	User      string `json:"user"`
	Name      string `json:"name"`
	OS        string `json:"os"`
	Version   string `json:"version"`
	TokenHash string `json:"token_hash"`
	Paired    string `json:"paired"`
	Seen      string `json:"seen,omitempty"`
	Revoked   bool   `json:"revoked,omitempty"`
}

type rpaPair struct {
	Code    string `json:"code_hash"`
	User    string `json:"user"`
	Expires string `json:"expires"`
}

// RPACred is a saved login for another site or app. Password and TOTP seed
// are write-only: they leave Ledgerline only to a paired computer running a
// workflow step that uses them.
type RPACred struct {
	ID       string   `json:"id"`
	Owner    string   `json:"owner"` // a user ID, or "*firm" for the firm's own logins
	Name     string   `json:"name"`
	Site     string   `json:"site"` // web address or app
	Username string   `json:"username"`
	Password string   `json:"password,omitempty"`
	TOTP     string   `json:"totp,omitempty"` // base32 seed for authenticator-app codes
	Notes    string   `json:"notes,omitempty"`
	Users    []string `json:"users,omitempty"` // firm logins: who may use them in workflows
	Created  string   `json:"created"`
	Updated  string   `json:"updated"`
	UsedAt   string   `json:"used_at,omitempty"`
	Uses     int      `json:"uses"`
}

const rpaFirmOwner = "*firm"

// RPACredView is a saved login as screens see it, without its secrets.
type RPACredView struct {
	ID          string   `json:"id"`
	Owner       string   `json:"owner"`
	Name        string   `json:"name"`
	Site        string   `json:"site"`
	Username    string   `json:"username"`
	HasPassword bool     `json:"has_password"`
	HasTOTP     bool     `json:"has_totp"`
	Notes       string   `json:"notes,omitempty"`
	Users       []string `json:"users,omitempty"`
	Updated     string   `json:"updated"`
	UsedAt      string   `json:"used_at,omitempty"`
	Uses        int      `json:"uses"`
	CanEdit     bool     `json:"can_edit"`
}

func (c RPACred) view(canEdit bool) RPACredView {
	return RPACredView{ID: c.ID, Owner: c.Owner, Name: c.Name, Site: c.Site, Username: c.Username, HasPassword: c.Password != "", HasTOTP: c.TOTP != "",
		Notes: c.Notes, Users: c.Users, Updated: c.Updated, UsedAt: c.UsedAt, Uses: c.Uses, CanEdit: canEdit}
}

// ---- who may ----

// rpaUser is a user who may use automations: head office staff and branch
// staff (not authorised persons or clients).
func (o *Ops) rpaUser(ctx context.Context, tenant, actor string) (User, error) {
	if err := o.require(ctx, tenant, actor, "Using automations", RoleMaker, RoleChecker, RoleRisk, RoleCompliance, RoleBranch); err != nil {
		return User{}, err
	}
	u, err := o.user(ctx, tenant, actor)
	if err != nil || !u.Active {
		return User{}, fmt.Errorf("%w: automations need an active login", ErrForbidden)
	}
	return u, nil
}

func (f RPAFlow) canSee(u User) bool  { return f.Owner == u.ID || f.Shared || u.Role == RoleAdmin }
func (f RPAFlow) canEdit(u User) bool { return f.Owner == u.ID || u.Role == RoleAdmin }

func (c RPACred) canUse(user string) bool {
	if c.Owner == rpaFirmOwner {
		return contains(c.Users, user)
	}
	return c.Owner == user
}

func (c RPACred) canEdit(u User) bool {
	if c.Owner == rpaFirmOwner {
		return u.Role == RoleAdmin
	}
	return c.Owner == u.ID
}

func rpaID(prefix string, n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

func (o *Ops) rpaNow() string { return o.now().Format(time.RFC3339) }

// ---- checking workflows ----

var (
	reRPAVar    = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)
	reRPATmpl   = regexp.MustCompile(`\{\{\s*([^{}]+?)\s*\}\}`)
	reRPASecret = regexp.MustCompile(`^secret:([A-Za-z0-9_-]+)\.(username|password|totp)$`)
	reRPADate   = regexp.MustCompile(`^(today|tradedate|prevtradedate|nexttradedate)(?::([A-Za-z\-/ .]{1,20}))?$`)
	reRPAKey    = regexp.MustCompile(`^(Enter|Tab|Escape|Backspace|Delete|Space|ArrowUp|ArrowDown|ArrowLeft|ArrowRight|Home|End|PageUp|PageDown|F[1-9]|F1[0-2])$`)
	reRPAHM     = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)
)

// rpaSecretRefs lists the saved logins a workflow's steps use.
func rpaSecretRefs(steps []RPAStep) map[string]bool {
	out := map[string]bool{}
	for _, s := range steps {
		for _, m := range reRPATmpl.FindAllStringSubmatch(s.Value, -1) {
			if sm := reRPASecret.FindStringSubmatch(m[1]); sm != nil {
				out[sm[1]] = true
			}
		}
	}
	return out
}

// checkRPASteps validates steps and gives each an ID. Variables must be set
// by an earlier step before they are used.
func checkRPASteps(steps []RPAStep) ([]RPAStep, error) {
	if len(steps) > rpaMaxSteps {
		return nil, invalid("a workflow can have at most %d steps", rpaMaxSteps)
	}
	known := map[string]bool{}
	files := map[string]bool{}
	var out []RPAStep
	for i, s := range steps {
		n := i + 1
		s.Label = strings.TrimSpace(s.Label)
		if len(s.Label) > 120 || len(s.Value) > 2000 || len(s.Message) > 500 {
			return nil, invalid("step %d is too long", n)
		}
		if s.ID == "" {
			s.ID = rpaID("s", 5)
		}
		needT := func() error {
			if s.Target == nil || (s.Target.CSS == "" && s.Target.Text == "" && s.Target.Name == "" && s.Target.Label == "" && s.Target.Placeholder == "") {
				return invalid("step %d (%s) needs something on the page to act on", n, s.Kind)
			}
			return nil
		}
		if s.Timeout < 0 || s.Timeout > 600 {
			return nil, invalid("step %d: wait at most 600 seconds for the page", n)
		}
		switch s.Kind {
		case "open":
			u, err := url.Parse(reRPATmpl.ReplaceAllString(s.URL, "x"))
			if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
				return nil, invalid("step %d: open needs a web address starting with https://", n)
			}
		case "click", "check":
			if err := needT(); err != nil {
				return nil, err
			}
		case "type", "select":
			if err := needT(); err != nil {
				return nil, err
			}
		case "press":
			if !reRPAKey.MatchString(s.Key) {
				return nil, invalid("step %d: unknown key %q", n, s.Key)
			}
		case "wait_for", "assert":
			if s.Target == nil && strings.TrimSpace(s.Value) == "" {
				return nil, invalid("step %d: say what to wait for (something on the page, or text)", n)
			}
		case "wait":
			if s.Seconds < 1 || s.Seconds > 300 {
				return nil, invalid("step %d: wait 1 to 300 seconds", n)
			}
		case "extract":
			if err := needT(); err != nil {
				return nil, err
			}
			if !reRPAVar.MatchString(s.Var) {
				return nil, invalid("step %d: name the value read (lower-case letters, digits, _)", n)
			}
			known[s.Var] = true
		case "download":
			if err := needT(); err != nil {
				return nil, err
			}
			if !reRPAVar.MatchString(s.Var) {
				return nil, invalid("step %d: name the downloaded file (lower-case letters, digits, _)", n)
			}
			files[s.Var] = true
		case "upload":
			if err := needT(); err != nil {
				return nil, err
			}
			if !files[s.Var] {
				return nil, invalid("step %d: upload a file downloaded by an earlier step", n)
			}
		case "ledgerline_upload":
			if !files[s.Var] {
				return nil, invalid("step %d: load a file downloaded by an earlier step", n)
			}
			if strings.TrimSpace(s.Source) == "" {
				return nil, invalid("step %d: choose the Ledgerline file type", n)
			}
		case "pause":
			if strings.TrimSpace(s.Message) == "" {
				s.Message = "Finish this step in the browser, then click Continue."
			}
		case "notify":
			if strings.TrimSpace(s.Message) == "" {
				return nil, invalid("step %d: write the message", n)
			}
		default:
			return nil, invalid("step %d: unknown kind %q", n, s.Kind)
		}
		for _, f := range []string{s.Value, s.URL, s.Message} {
			for _, m := range reRPATmpl.FindAllStringSubmatch(f, -1) {
				t := m[1]
				switch {
				case reRPASecret.MatchString(t), reRPADate.MatchString(t):
				case t == "secret:?":
					return nil, invalid("step %d: choose the saved password to use", n)
				case known[t]:
				default:
					return nil, invalid("step %d uses {{%s}}, which no earlier step reads", n, t)
				}
			}
		}
		if reRPASecret.MatchString(strings.Trim(s.URL, "{} ")) {
			return nil, invalid("step %d: passwords cannot go into a web address", n)
		}
		if s.Kind == "type" && s.NeedsSecret != "" && !strings.Contains(s.Value, "{{secret:") && s.NeedsSecret != "otp" {
			return nil, invalid("step %d: choose the saved password to type here", n)
		}
		out = append(out, s)
	}
	return out, nil
}

// ---- workflows ----

// RPAFlowInput is a workflow as saved from the screen.
type RPAFlowInput struct {
	Name     string       `json:"name"`
	About    string       `json:"about"`
	Shared   bool         `json:"shared"`
	Steps    []RPAStep    `json:"steps"`
	Schedule *RPASchedule `json:"schedule"`
}

// SaveRPAFlow creates (id empty) or changes a workflow.
func (o *Ops) SaveRPAFlow(ctx context.Context, tenant, actor, id string, in RPAFlowInput) (RPAFlow, error) {
	u, err := o.rpaUser(ctx, tenant, actor)
	if err != nil {
		return RPAFlow{}, err
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 80 {
		return RPAFlow{}, invalid("give the workflow a name (up to 80 characters)")
	}
	steps, err := checkRPASteps(in.Steps)
	if err != nil {
		return RPAFlow{}, err
	}
	owner := actor
	var old RPAFlow
	if id != "" {
		old, _, err = docs.Get[RPAFlow](ctx, o.Docs, tenant, cRPAFlow, id)
		if err != nil || !old.canSee(u) {
			return RPAFlow{}, fmt.Errorf("%w: no such workflow", ErrNotFound)
		}
		if !old.canEdit(u) {
			return RPAFlow{}, fmt.Errorf("%w: only %s can change this workflow; make a copy to change it", ErrForbidden, old.Owner)
		}
		owner = old.Owner
	}
	// The passwords it uses must be the owner's or firm logins the owner may use.
	for cid := range rpaSecretRefs(steps) {
		c, _, err := docs.Get[RPACred](ctx, o.Docs, tenant, cRPACred, cid)
		if err != nil || !c.canUse(owner) {
			return RPAFlow{}, invalid("a step uses a saved password that is not %s's", map[bool]string{true: "yours", false: owner}[owner == actor])
		}
	}
	if s := in.Schedule; s != nil && s.Active {
		if !reRPAHM.MatchString(s.At) {
			return RPAFlow{}, invalid("schedule time is HH:MM")
		}
		if !rpaWhen.MatchString(s.When) {
			return RPAFlow{}, invalid("schedule days are business, daily, weekly:Mon or monthly:1")
		}
		a, _, err := docs.Get[RPAAgent](ctx, o.Docs, tenant, cRPAAgent, s.Agent)
		if err != nil || a.User != owner || a.Revoked {
			return RPAFlow{}, invalid("choose one of %s computers for the schedule", map[bool]string{true: "your", false: owner + "'s"}[owner == actor])
		}
	}
	now := o.rpaNow()
	f := RPAFlow{ID: id, Name: in.Name, About: strings.TrimSpace(in.About), Owner: owner, Shared: in.Shared, Steps: steps, Schedule: in.Schedule, Version: old.Version + 1,
		Created: firstOf(old.Created, now), Updated: now, LastRun: old.LastRun, LastStatus: old.LastStatus, LastDate: old.LastDate}
	if f.ID == "" {
		n, err := o.next(ctx, tenant, "rpaflow", actor)
		if err != nil {
			return f, err
		}
		f.ID = fmt.Sprintf("WF-%04d", n)
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cRPAFlow, f.ID, f, actor); err != nil {
		return f, err
	}
	o.log(ctx, tenant, actor, "rpa.flow.save", f.ID, map[string]any{"name": f.Name, "version": f.Version, "steps": len(steps), "shared": f.Shared, "scheduled": f.Schedule != nil && f.Schedule.Active})
	return f, nil
}

// DeleteRPAFlow removes a workflow (its runs stay in the history).
func (o *Ops) DeleteRPAFlow(ctx context.Context, tenant, actor, id string) error {
	u, err := o.rpaUser(ctx, tenant, actor)
	if err != nil {
		return err
	}
	f, _, err := docs.Get[RPAFlow](ctx, o.Docs, tenant, cRPAFlow, id)
	if err != nil || !f.canEdit(u) {
		return fmt.Errorf("%w: no such workflow", ErrNotFound)
	}
	// The document store keeps history; a deleted workflow is emptied and hidden.
	f.Steps, f.Schedule, f.Shared, f.Name = nil, nil, false, "(deleted) "+f.Name
	f.Owner = "*deleted:" + f.Owner
	if err := docs.Upsert(ctx, o.Docs, tenant, cRPAFlow, id, f, actor); err != nil {
		return err
	}
	o.log(ctx, tenant, actor, "rpa.flow.delete", id, nil)
	return nil
}

// ---- the screen ----

// RPAAgentView is a computer as screens see it.
type RPAAgentView struct {
	ID      string `json:"id"`
	User    string `json:"user"`
	Name    string `json:"name"`
	OS      string `json:"os"`
	Version string `json:"version"`
	Paired  string `json:"paired"`
	Seen    string `json:"seen,omitempty"`
	Online  bool   `json:"online"`
	Mine    bool   `json:"mine"`
}

// RPAHome is the Automations screen.
type RPAHome struct {
	Flows   []RPAFlow      `json:"flows"`
	Runs    []RPARun       `json:"runs"`
	Creds   []RPACredView  `json:"creds"`
	Agents  []RPAAgentView `json:"agents"`
	Kinds   any            `json:"kinds"`
	Sources []string       `json:"sources"`
	Users   []ChatPerson   `json:"users,omitempty"` // Admins: who firm logins can be given to
	Admin   bool           `json:"admin"`
	Me      string         `json:"me"`
}

func (o *Ops) agentView(a RPAAgent, me string) RPAAgentView {
	v := RPAAgentView{ID: a.ID, User: a.User, Name: a.Name, OS: a.OS, Version: a.Version, Paired: a.Paired, Seen: a.Seen, Mine: a.User == me}
	if t, err := time.Parse(time.RFC3339, a.Seen); err == nil {
		v.Online = o.now().Sub(t) < rpaOnline
	}
	return v
}

// RPAHome returns the person's workflows (and shared ones), recent runs,
// saved logins and computers.
func (o *Ops) RPAHome(ctx context.Context, tenant, actor string) (RPAHome, error) {
	u, err := o.rpaUser(ctx, tenant, actor)
	if err != nil {
		return RPAHome{}, err
	}
	h := RPAHome{Flows: []RPAFlow{}, Runs: []RPARun{}, Creds: []RPACredView{}, Agents: []RPAAgentView{}, Kinds: RPAStepKinds, Admin: u.Role == RoleAdmin, Me: actor}
	fs, _ := docs.List[RPAFlow](ctx, o.Docs, tenant, cRPAFlow, "")
	for _, f := range fs {
		if !strings.HasPrefix(f.Owner, "*deleted:") && f.canSee(u) {
			h.Flows = append(h.Flows, f)
		}
	}
	sort.Slice(h.Flows, func(i, j int) bool { return h.Flows[i].Updated > h.Flows[j].Updated })
	rs, _ := docs.List[RPARun](ctx, o.Docs, tenant, cRPARun, "")
	for _, r := range rs {
		if r.User == actor || u.Role == RoleAdmin {
			r.Steps = nil
			h.Runs = append(h.Runs, r)
		}
	}
	sort.Slice(h.Runs, func(i, j int) bool { return h.Runs[i].Queued > h.Runs[j].Queued })
	if len(h.Runs) > 100 {
		h.Runs = h.Runs[:100]
	}
	cs, _ := docs.List[RPACred](ctx, o.Docs, tenant, cRPACred, "")
	for _, c := range cs {
		if c.Owner == actor || c.canUse(actor) || (c.Owner == rpaFirmOwner && u.Role == RoleAdmin) {
			h.Creds = append(h.Creds, c.view(c.canEdit(u)))
		}
	}
	sort.Slice(h.Creds, func(i, j int) bool { return h.Creds[i].Name < h.Creds[j].Name })
	as, _ := docs.List[RPAAgent](ctx, o.Docs, tenant, cRPAAgent, "")
	for _, a := range as {
		if !a.Revoked && (a.User == actor || u.Role == RoleAdmin) {
			h.Agents = append(h.Agents, o.agentView(a, actor))
		}
	}
	sort.Slice(h.Agents, func(i, j int) bool { return h.Agents[i].Name < h.Agents[j].Name })
	for _, s := range MasterSources() {
		h.Sources = append(h.Sources, s.Name)
	}
	seen := map[string]bool{}
	for _, a := range o.App.Ingest.Registry.List() {
		if !seen[a.Name] {
			seen[a.Name] = true
			h.Sources = append(h.Sources, a.Name)
		}
	}
	sort.Strings(h.Sources)
	if u.Role == RoleAdmin {
		us, _ := o.Users(ctx, tenant)
		for _, x := range us {
			if x.Active && x.Role != RoleClient && x.Role != RoleAP {
				h.Users = append(h.Users, ChatPerson{ID: x.ID, Name: x.Name, Role: x.Role})
			}
		}
		sort.Slice(h.Users, func(i, j int) bool { return h.Users[i].Name < h.Users[j].Name })
	}
	return h, nil
}

// RPAFlowGet returns one workflow.
func (o *Ops) RPAFlowGet(ctx context.Context, tenant, actor, id string) (RPAFlow, error) {
	u, err := o.rpaUser(ctx, tenant, actor)
	if err != nil {
		return RPAFlow{}, err
	}
	f, _, err := docs.Get[RPAFlow](ctx, o.Docs, tenant, cRPAFlow, id)
	if err != nil || !f.canSee(u) || strings.HasPrefix(f.Owner, "*deleted:") {
		return RPAFlow{}, fmt.Errorf("%w: no such workflow", ErrNotFound)
	}
	return f, nil
}

// ---- computers ----

// RPAPairCode makes a one-time code to pair a computer, valid ten minutes.
func (o *Ops) RPAPairCode(ctx context.Context, tenant, actor string) (string, string, error) {
	if _, err := o.rpaUser(ctx, tenant, actor); err != nil {
		return "", "", err
	}
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	code := base32.StdEncoding.EncodeToString(b) // 8 letters and digits
	code = code[:4] + "-" + code[4:]
	exp := o.now().Add(rpaPairMinutes * time.Minute)
	p := rpaPair{Code: sha(code), User: actor, Expires: exp.Format(time.RFC3339)}
	if err := docs.Upsert(ctx, o.Docs, tenant, cRPAPair, p.Code, p, actor); err != nil {
		return "", "", err
	}
	o.log(ctx, tenant, actor, "rpa.pair.code", actor, nil)
	return code, p.Expires, nil
}

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

var rpaWhen = regexp.MustCompile(`^(business|daily|weekly:(Mon|Tue|Wed|Thu|Fri|Sat|Sun)|monthly:([1-9]|[12][0-9]|3[01]))$`)

// RPAPair turns a pairing code into a computer and its token (shown once).
func (o *Ops) RPAPair(ctx context.Context, tenant, code, name, osName, version string) (RPAAgent, string, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	p, _, err := docs.Get[rpaPair](ctx, o.Docs, tenant, cRPAPair, sha(code))
	if err != nil || p.User == "" {
		return RPAAgent{}, "", fmt.Errorf("%w: that code is not valid; make a new one in Ledgerline", ErrForbidden)
	}
	if t, err := time.Parse(time.RFC3339, p.Expires); err != nil || o.now().After(t) {
		return RPAAgent{}, "", fmt.Errorf("%w: that code has expired; make a new one in Ledgerline", ErrForbidden)
	}
	// One use only.
	_ = docs.Upsert(ctx, o.Docs, tenant, cRPAPair, p.Code, rpaPair{Code: p.Code, Expires: p.Expires}, p.User)
	if _, err := o.rpaUser(ctx, tenant, p.User); err != nil {
		return RPAAgent{}, "", err
	}
	tok := "llbot_" + rpaID("", 24)
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 60 {
		name = "My computer"
	}
	a := RPAAgent{ID: rpaID("PC-", 5), User: p.User, Name: name, OS: trunc(osName, 40), Version: trunc(version, 20), TokenHash: sha(tok), Paired: o.rpaNow(), Seen: o.rpaNow()}
	if err := docs.Upsert(ctx, o.Docs, tenant, cRPAAgent, a.ID, a, p.User); err != nil {
		return a, "", err
	}
	o.log(ctx, tenant, p.User, "rpa.agent.pair", a.ID, map[string]any{"name": a.Name, "os": a.OS})
	return a, tok, nil
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// RevokeRPAAgent unpairs a computer: its token stops working at once.
func (o *Ops) RevokeRPAAgent(ctx context.Context, tenant, actor, id string) error {
	u, err := o.rpaUser(ctx, tenant, actor)
	if err != nil {
		return err
	}
	a, _, err := docs.Get[RPAAgent](ctx, o.Docs, tenant, cRPAAgent, id)
	if err != nil || (a.User != actor && u.Role != RoleAdmin) {
		return fmt.Errorf("%w: no such computer", ErrNotFound)
	}
	a.Revoked, a.TokenHash = true, ""
	if err := docs.Upsert(ctx, o.Docs, tenant, cRPAAgent, id, a, actor); err != nil {
		return err
	}
	o.log(ctx, tenant, actor, "rpa.agent.revoke", id, map[string]any{"name": a.Name, "user": a.User})
	return nil
}

// RPAAgentAuth finds the computer a token belongs to and notes it called in.
func (o *Ops) RPAAgentAuth(ctx context.Context, tenant, token string) (RPAAgent, error) {
	if !strings.HasPrefix(token, "llbot_") || len(token) < 40 {
		return RPAAgent{}, ErrForbidden
	}
	h := sha(token)
	as, err := docs.List[RPAAgent](ctx, o.Docs, tenant, cRPAAgent, "")
	if err != nil {
		return RPAAgent{}, err
	}
	for _, a := range as {
		if !a.Revoked && a.TokenHash != "" && subtle.ConstantTimeCompare([]byte(a.TokenHash), []byte(h)) == 1 {
			if u, err := o.user(ctx, tenant, a.User); err != nil || !u.Active {
				return RPAAgent{}, ErrForbidden
			}
			if t, err := time.Parse(time.RFC3339, a.Seen); err != nil || o.now().Sub(t) > 20*time.Second {
				a.Seen = o.rpaNow()
				_ = docs.Upsert(ctx, o.Docs, tenant, cRPAAgent, a.ID, a, a.User)
			}
			return a, nil
		}
	}
	return RPAAgent{}, ErrForbidden
}

// ---- recording ----

// StartRPARecording asks one of the person's computers to open a browser
// and record. flow adds the steps to an existing workflow.
func (o *Ops) StartRPARecording(ctx context.Context, tenant, actor, agent, flow, name, startURL string) (RPARecording, error) {
	u, err := o.rpaUser(ctx, tenant, actor)
	if err != nil {
		return RPARecording{}, err
	}
	a, _, err := docs.Get[RPAAgent](ctx, o.Docs, tenant, cRPAAgent, agent)
	if err != nil || a.User != actor || a.Revoked {
		return RPARecording{}, invalid("choose one of your computers")
	}
	if flow != "" {
		f, _, err := docs.Get[RPAFlow](ctx, o.Docs, tenant, cRPAFlow, flow)
		if err != nil || !f.canEdit(u) {
			return RPARecording{}, fmt.Errorf("%w: no such workflow", ErrNotFound)
		}
		name = f.Name
	}
	pu, err := url.Parse(strings.TrimSpace(startURL))
	if err != nil || (pu.Scheme != "https" && pu.Scheme != "http") || pu.Host == "" {
		return RPARecording{}, invalid("start from a web address beginning with https://")
	}
	if strings.TrimSpace(name) == "" {
		name = "Recorded " + o.now().Format("2 Jan 15:04")
	}
	r := RPARecording{ID: rpaID("REC-", 5), User: actor, Agent: agent, Flow: flow, Name: trunc(strings.TrimSpace(name), 80), StartURL: pu.String(), Status: "Waiting", Steps: []RPAStep{}, Created: o.rpaNow()}
	if err := docs.Upsert(ctx, o.Docs, tenant, cRPARec, r.ID, r, actor); err != nil {
		return r, err
	}
	return r, nil
}

// RPARecordingGet returns a recording (the screen shows steps as they come).
func (o *Ops) RPARecordingGet(ctx context.Context, tenant, actor, id string) (RPARecording, error) {
	if _, err := o.rpaUser(ctx, tenant, actor); err != nil {
		return RPARecording{}, err
	}
	r, _, err := docs.Get[RPARecording](ctx, o.Docs, tenant, cRPARec, id)
	if err != nil || r.User != actor {
		return RPARecording{}, fmt.Errorf("%w: no such recording", ErrNotFound)
	}
	return r, nil
}

// StopRPARecording asks the computer to stop; cancel discards the steps.
func (o *Ops) StopRPARecording(ctx context.Context, tenant, actor, id string, cancel bool) (RPARecording, error) {
	r, err := o.RPARecordingGet(ctx, tenant, actor, id)
	if err != nil {
		return r, err
	}
	r, err = docs.Update(ctx, o.Docs, tenant, cRPARec, id, actor, func(x *RPARecording) error {
		x.Stop = true
		if cancel || x.Status == "Waiting" {
			x.Status = "Cancelled"
		}
		return nil
	})
	return r, err
}

// ---- runs ----

func (o *Ops) runContext(ctx context.Context, tenant string) map[string]string {
	set, _ := o.Settings(ctx, tenant)
	today := o.now().Format("2006-01-02")
	td := o.businessDate(ctx, tenant)
	step := func(from string, dir int) string {
		t, _ := time.Parse("2006-01-02", from)
		for i := 0; i < 15; i++ {
			t = t.AddDate(0, 0, dir)
			if d := t.Format("2006-01-02"); set.IsBusinessDay(d) {
				return d
			}
		}
		return from
	}
	return map[string]string{"today": today, "tradedate": td, "prevtradedate": step(td, -1), "nexttradedate": step(td, 1)}
}

// StartRPARun runs a workflow on one of the person's computers.
func (o *Ops) StartRPARun(ctx context.Context, tenant, actor, flow, agent string) (RPARun, error) {
	u, err := o.rpaUser(ctx, tenant, actor)
	if err != nil {
		return RPARun{}, err
	}
	f, _, err := docs.Get[RPAFlow](ctx, o.Docs, tenant, cRPAFlow, flow)
	if err != nil || !f.canSee(u) || strings.HasPrefix(f.Owner, "*deleted:") {
		return RPARun{}, fmt.Errorf("%w: no such workflow", ErrNotFound)
	}
	return o.queueRun(ctx, tenant, actor, f, agent, "manual")
}

func (o *Ops) queueRun(ctx context.Context, tenant, user string, f RPAFlow, agent, trigger string) (RPARun, error) {
	a, _, err := docs.Get[RPAAgent](ctx, o.Docs, tenant, cRPAAgent, agent)
	if err != nil || a.User != user || a.Revoked {
		return RPARun{}, invalid("choose one of your computers")
	}
	if len(f.Steps) == 0 {
		return RPARun{}, invalid("this workflow has no steps yet")
	}
	for cid := range rpaSecretRefs(f.Steps) {
		c, _, err := docs.Get[RPACred](ctx, o.Docs, tenant, cRPACred, cid)
		if err != nil || !c.canUse(user) {
			return RPARun{}, invalid("this workflow uses a saved password you cannot use; the owner's passwords stay theirs (make a copy and choose your own)")
		}
	}
	n, err := o.next(ctx, tenant, "rparun", user)
	if err != nil {
		return RPARun{}, err
	}
	r := RPARun{ID: fmt.Sprintf("RUN-%05d", n), Flow: f.ID, FlowName: f.Name, Version: f.Version, Steps: f.Steps, User: user, Agent: agent, Trigger: trigger,
		Status: "Queued", Results: []RPAStepResult{}, Context: o.runContext(ctx, tenant), Queued: o.rpaNow()}
	if err := docs.Upsert(ctx, o.Docs, tenant, cRPARun, r.ID, r, user); err != nil {
		return r, err
	}
	o.log(ctx, tenant, user, "rpa.run.start", r.ID, map[string]any{"flow": f.ID, "version": f.Version, "agent": agent, "trigger": trigger})
	return r, nil
}

// RPARunGet returns a run with its steps and results.
func (o *Ops) RPARunGet(ctx context.Context, tenant, actor, id string) (RPARun, error) {
	u, err := o.rpaUser(ctx, tenant, actor)
	if err != nil {
		return RPARun{}, err
	}
	r, _, err := docs.Get[RPARun](ctx, o.Docs, tenant, cRPARun, id)
	if err != nil || (r.User != actor && u.Role != RoleAdmin) {
		return RPARun{}, fmt.Errorf("%w: no such run", ErrNotFound)
	}
	return r, nil
}

// ControlRPARun continues a paused run or cancels one.
func (o *Ops) ControlRPARun(ctx context.Context, tenant, actor, id, what string) (RPARun, error) {
	r, err := o.RPARunGet(ctx, tenant, actor, id)
	if err != nil {
		return r, err
	}
	if r.User != actor {
		return r, fmt.Errorf("%w: only %s can control this run", ErrForbidden, r.User)
	}
	r, err = docs.Update(ctx, o.Docs, tenant, cRPARun, id, actor, func(x *RPARun) error {
		switch what {
		case "continue":
			if x.Status != "Paused" {
				return invalid("the run is not waiting for you")
			}
			x.Resume = true
		case "cancel":
			if x.Status == "Succeeded" || x.Status == "Failed" || x.Status == "Cancelled" {
				return invalid("the run has finished")
			}
			x.Cancel = true
			if x.Status == "Queued" {
				x.Status, x.Ended = "Cancelled", o.rpaNow()
			}
		default:
			return invalid("continue or cancel")
		}
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "rpa.run."+what, id, nil)
	}
	return r, err
}

// ---- the computer's side ----

// RPAJob is what the computer is asked to do next.
type RPAJob struct {
	Kind      string        `json:"kind"` // record, run
	Recording *RPARecording `json:"recording,omitempty"`
	Run       *RPARun       `json:"run,omitempty"`
}

// RPANextJob claims the computer's next recording or run, if any.
func (o *Ops) RPANextJob(ctx context.Context, tenant string, a RPAAgent) (*RPAJob, error) {
	recs, err := docs.List[RPARecording](ctx, o.Docs, tenant, cRPARec, "")
	if err != nil {
		return nil, err
	}
	for _, r := range recs {
		if r.Agent == a.ID && r.Status == "Waiting" {
			r, err := docs.Update(ctx, o.Docs, tenant, cRPARec, r.ID, a.User, func(x *RPARecording) error {
				if x.Status != "Waiting" {
					return errClaimed
				}
				x.Status, x.Seen = "Recording", o.rpaNow()
				return nil
			})
			if err == nil {
				return &RPAJob{Kind: "record", Recording: &r}, nil
			}
		}
	}
	runs, err := docs.List[RPARun](ctx, o.Docs, tenant, cRPARun, "")
	if err != nil {
		return nil, err
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].Queued < runs[j].Queued })
	for _, r := range runs {
		if r.Agent == a.ID && r.Status == "Queued" {
			r, err := docs.Update(ctx, o.Docs, tenant, cRPARun, r.ID, a.User, func(x *RPARun) error {
				if x.Status != "Queued" {
					return errClaimed
				}
				x.Status, x.Started, x.Seen = "Running", o.rpaNow(), o.rpaNow()
				return nil
			})
			if err == nil {
				return &RPAJob{Kind: "run", Run: &r}, nil
			}
		}
	}
	return nil, nil
}

var errClaimed = errors.New("already claimed")

// RPARecordSteps adds steps the computer recorded. It returns whether to
// stop. Typed passwords never reach Ledgerline: the computer sends a
// placeholder, and Ledgerline links the step to a saved login for the site
// when one matches.
func (o *Ops) RPARecordSteps(ctx context.Context, tenant string, a RPAAgent, id string, steps []RPAStep) (bool, error) {
	r, _, err := docs.Get[RPARecording](ctx, o.Docs, tenant, cRPARec, id)
	if err != nil || r.Agent != a.ID {
		return true, fmt.Errorf("%w: no such recording", ErrNotFound)
	}
	if r.Status != "Recording" {
		return true, nil
	}
	if len(r.Steps)+len(steps) > rpaMaxSteps {
		return true, invalid("recordings can have at most %d steps", rpaMaxSteps)
	}
	creds, _ := docs.List[RPACred](ctx, o.Docs, tenant, cRPACred, "")
	site := ""
	for _, s := range r.Steps {
		if s.Kind == "open" {
			site = s.URL
		}
	}
	for i := range steps {
		s := &steps[i]
		s.ID = rpaID("s", 5)
		s.Label, s.Value, s.Message = trunc(s.Label, 120), trunc(s.Value, 2000), trunc(s.Message, 500)
		if s.Kind == "open" {
			site = s.URL
		}
		if s.Kind == "type" && s.NeedsSecret != "" {
			o.bindSecret(s, creds, r.User, site)
		}
	}
	r, err = docs.Update(ctx, o.Docs, tenant, cRPARec, id, a.User, func(x *RPARecording) error {
		x.Steps = append(x.Steps, steps...)
		x.Seen = o.rpaNow()
		return nil
	})
	return r.Stop, err
}

func rpaHost(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		u, err = url.Parse("https://" + strings.TrimSpace(raw))
		if err != nil {
			return ""
		}
	}
	return strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
}

// bindSecret links a recorded password, username or one-time-password box
// to the person's saved login for the site, when there is one.
func (o *Ops) bindSecret(s *RPAStep, creds []RPACred, user, site string) {
	host := rpaHost(site)
	var match *RPACred
	for i := range creds {
		c := &creds[i]
		if c.canUse(user) && host != "" && (rpaHost(c.Site) == host || strings.HasSuffix(host, "."+rpaHost(c.Site))) {
			if match == nil || c.Owner == user {
				match = c
			}
		}
	}
	switch s.NeedsSecret {
	case "password":
		s.Value = "{{secret:?}}"
		if match != nil && match.Password != "" {
			s.Value, s.NeedsSecret = "{{secret:"+match.ID+".password}}", ""
		}
		s.Label = firstOf(s.Label, "Type the password")
	case "username":
		// The computer sends the typed username so it can be matched; it is
		// kept only when no saved login has it.
		if match != nil && strings.EqualFold(strings.TrimSpace(s.Value), match.Username) {
			s.Value, s.NeedsSecret = "{{secret:"+match.ID+".username}}", ""
		} else {
			s.NeedsSecret = ""
		}
	case "otp":
		if match != nil && match.TOTP != "" {
			s.Value, s.NeedsSecret = "{{secret:"+match.ID+".totp}}", ""
			s.Label = firstOf(s.Label, "Type the authenticator code")
		} else {
			// A code sent by SMS or email: a person types it.
			*s = RPAStep{ID: s.ID, Kind: "pause", Label: "Enter the one-time password", Message: "Type the one-time password you received into the page, then click Continue."}
		}
	}
}

// RPARecordDone ends a recording and saves it as a workflow.
func (o *Ops) RPARecordDone(ctx context.Context, tenant string, a RPAAgent, id string) (RPARecording, error) {
	r, _, err := docs.Get[RPARecording](ctx, o.Docs, tenant, cRPARec, id)
	if err != nil || r.Agent != a.ID {
		return r, fmt.Errorf("%w: no such recording", ErrNotFound)
	}
	if r.Status == "Cancelled" || r.Status == "Done" {
		return r, nil
	}
	steps := rpaTidy(r.Steps)
	f := RPAFlow{Name: r.Name, Owner: r.User}
	if r.Flow != "" {
		if old, _, err := docs.Get[RPAFlow](ctx, o.Docs, tenant, cRPAFlow, r.Flow); err == nil {
			f = old
			steps = append(append([]RPAStep{}, old.Steps...), steps...)
		}
	}
	// Steps still needing a password are saved as they are; the editor asks
	// for them before the workflow can run.
	now := o.rpaNow()
	f.Steps, f.Updated, f.Version = steps, now, f.Version+1
	if f.ID == "" {
		n, err := o.next(ctx, tenant, "rpaflow", r.User)
		if err != nil {
			return r, err
		}
		f.ID, f.Created = fmt.Sprintf("WF-%04d", n), now
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cRPAFlow, f.ID, f, r.User); err != nil {
		return r, err
	}
	r, err = docs.Update(ctx, o.Docs, tenant, cRPARec, id, a.User, func(x *RPARecording) error {
		x.Status, x.Saved, x.Seen = "Done", f.ID, now
		return nil
	})
	o.log(ctx, tenant, r.User, "rpa.flow.record", f.ID, map[string]any{"recording": id, "steps": len(steps), "agent": a.ID})
	return r, err
}

// rpaTidy merges repeated typing into one box and drops clicks that only
// focused the box typed into next.
func rpaTidy(in []RPAStep) []RPAStep {
	var out []RPAStep
	for _, s := range in {
		if n := len(out); n > 0 {
			p := &out[n-1]
			same := func(a, b *RPATarget) bool { return a != nil && b != nil && a.CSS != "" && a.CSS == b.CSS }
			if s.Kind == "type" && p.Kind == "type" && same(p.Target, s.Target) {
				*p = s
				continue
			}
			if s.Kind == "type" && p.Kind == "click" && same(p.Target, s.Target) {
				*p = s
				continue
			}
			if s.Kind == "open" && p.Kind == "open" {
				*p = s
				continue
			}
		}
		out = append(out, s)
	}
	return out
}

// RPAEvent is the computer reporting a step of a run.
type RPAEvent struct {
	Step    int    `json:"step"`
	Status  string `json:"status"` // Running, Done, Skipped, Failed, Paused
	Message string `json:"message"`
	Ms      int64  `json:"ms"`
}

// RPAControl tells the computer whether to carry on.
type RPAControl struct {
	Cancel bool `json:"cancel"`
	Resume bool `json:"resume"`
}

// RPARunEvent records a step's progress and answers with any control.
func (o *Ops) RPARunEvent(ctx context.Context, tenant string, a RPAAgent, id string, ev RPAEvent) (RPAControl, error) {
	var notify bool
	r, err := docs.Update(ctx, o.Docs, tenant, cRPARun, id, a.User, func(x *RPARun) error {
		if x.Agent != a.ID {
			return ErrNotFound
		}
		if x.Status != "Running" && x.Status != "Paused" {
			return nil
		}
		x.Seen = o.rpaNow()
		if ev.Step < 0 || ev.Step >= len(x.Steps) {
			return nil // a heartbeat
		}
		res := RPAStepResult{Step: ev.Step, ID: x.Steps[ev.Step].ID, Status: ev.Status, Message: trunc(ev.Message, 1000), Ms: ev.Ms, At: o.rpaNow()}
		switch ev.Status {
		case "Paused":
			// The computer checks in while it waits; Continue reaches it in
			// the answer to one of these.
			if x.Status != "Paused" {
				notify = true
				x.Status, x.Pause, x.Resume = "Paused", firstOf(res.Message, "Finish this step in the browser, then click Continue."), false
			}
			return nil
		case "Running":
			if x.Status == "Paused" {
				x.Status, x.Pause, x.Resume = "Running", "", false
			}
			return nil
		}
		x.Results = append(x.Results, res)
		if len(x.Results) > 2*rpaMaxSteps {
			x.Results = x.Results[len(x.Results)-2*rpaMaxSteps:]
		}
		return nil
	})
	if err != nil {
		return RPAControl{}, fmt.Errorf("%w: no such run", ErrNotFound)
	}
	if notify {
		o.rpaTell(ctx, tenant, r.User, fmt.Sprintf("Your workflow %s needs you on %s: %s", r.FlowName, o.agentName(ctx, tenant, r.Agent), r.Pause), r.ID)
	}
	return RPAControl{Cancel: r.Cancel, Resume: r.Resume}, nil
}

func (o *Ops) agentName(ctx context.Context, tenant, id string) string {
	a, _, err := docs.Get[RPAAgent](ctx, o.Docs, tenant, cRPAAgent, id)
	if err != nil {
		return "your computer"
	}
	return a.Name
}

// rpaTell has Ledgy message the person about their run.
func (o *Ops) rpaTell(ctx context.Context, tenant, user, text, run string) {
	e, err := o.chatEnv(ctx, tenant, LedgyActor)
	if err != nil {
		return
	}
	u, ok := e.users[user]
	if !ok || chatSide(u) == "" {
		return
	}
	id := "L-" + shortHash(user)
	c, err := o.chatChannel(ctx, tenant, id)
	if err != nil {
		c = ChatChannel{ID: id, Kind: ChatLedgy, Scope: chatSide(u), Members: []string{user}, Created: o.now().Format(time.RFC3339), CreatedBy: LedgyActor}
		if docs.Upsert(ctx, o.Docs, tenant, cChatChan, id, c, LedgyActor) != nil {
			return
		}
	}
	_, _ = o.postChat(ctx, e, c, LedgyActor, text, nil, &ChatLink{View: "rpa:" + run, Label: "Open the run"})
}

// RPARunDone finishes a run.
func (o *Ops) RPARunDone(ctx context.Context, tenant string, a RPAAgent, id, status, msg string, shot []byte) error {
	if status != "Succeeded" && status != "Failed" && status != "Cancelled" {
		return invalid("status is Succeeded, Failed or Cancelled")
	}
	shotID := ""
	if len(shot) > 0 && len(shot) < 8<<20 {
		sum := sha256.Sum256(shot)
		if rf, err := o.App.Ingest.Raw.Put(ctx, ingest.RawFile{Tenant: tenant, Source: "RPA-SHOT", Name: id + ".png", SHA256: hex.EncodeToString(sum[:]), Content: shot}); err == nil {
			shotID = rf.ID
		}
	}
	r, err := docs.Update(ctx, o.Docs, tenant, cRPARun, id, a.User, func(x *RPARun) error {
		if x.Agent != a.ID {
			return ErrNotFound
		}
		if x.Status != "Running" && x.Status != "Paused" {
			return nil
		}
		x.Status, x.Error, x.Ended, x.Pause, x.Shot = status, trunc(msg, 1000), o.rpaNow(), "", shotID
		return nil
	})
	if err != nil {
		return fmt.Errorf("%w: no such run", ErrNotFound)
	}
	_, _ = docs.Update(ctx, o.Docs, tenant, cRPAFlow, r.Flow, a.User, func(f *RPAFlow) error {
		f.LastRun, f.LastStatus = r.Ended, r.Status
		return nil
	})
	o.log(ctx, tenant, r.User, "rpa.run.end", id, map[string]any{"status": status, "error": r.Error})
	if status == "Failed" {
		o.rpaTell(ctx, tenant, r.User, fmt.Sprintf("Your workflow %s stopped: %s", r.FlowName, firstOf(r.Error, "a step failed")), r.ID)
	}
	return nil
}

// RPARunShot returns the screenshot of a failed run.
func (o *Ops) RPARunShot(ctx context.Context, tenant, actor, id string) ([]byte, error) {
	r, err := o.RPARunGet(ctx, tenant, actor, id)
	if err != nil || r.Shot == "" {
		return nil, fmt.Errorf("%w: no picture", ErrNotFound)
	}
	f, err := o.App.Ingest.Raw.Get(ctx, tenant, r.Shot)
	return f.Content, err
}

// RPASecret gives the computer one value from a saved login, only for the
// step of an active run that uses it, and records the use.
func (o *Ops) RPASecret(ctx context.Context, tenant string, a RPAAgent, run string, step int, cred, field string) (string, error) {
	r, _, err := docs.Get[RPARun](ctx, o.Docs, tenant, cRPARun, run)
	if err != nil || r.Agent != a.ID || (r.Status != "Running" && r.Status != "Paused") || step < 0 || step >= len(r.Steps) {
		return "", fmt.Errorf("%w: not available", ErrForbidden)
	}
	want := "{{secret:" + cred + "." + field + "}}"
	if !strings.Contains(strings.ReplaceAll(r.Steps[step].Value, " ", ""), want) {
		return "", fmt.Errorf("%w: that step does not use this login", ErrForbidden)
	}
	c, _, err := docs.Get[RPACred](ctx, o.Docs, tenant, cRPACred, cred)
	if err != nil || !c.canUse(r.User) {
		return "", fmt.Errorf("%w: not available", ErrForbidden)
	}
	var v string
	switch field {
	case "username":
		v = c.Username
	case "password":
		v = c.Password
	case "totp":
		v, err = totpCode(c.TOTP, o.now())
		if err != nil {
			return "", invalid("the saved authenticator key for %s is not valid", c.Name)
		}
	default:
		return "", ErrForbidden
	}
	_, _ = docs.Update(ctx, o.Docs, tenant, cRPACred, cred, r.User, func(x *RPACred) error { x.UsedAt, x.Uses = o.rpaNow(), x.Uses+1; return nil })
	o.log(ctx, tenant, r.User, "rpa.secret.use", cred, map[string]any{"run": run, "step": step, "field": field, "agent": a.ID})
	return v, nil
}

// RPALoadFile loads a downloaded file into Ledgerline for a run's step, as
// the person who runs the workflow.
func (o *Ops) RPALoadFile(ctx context.Context, tenant string, a RPAAgent, run string, step int, name string, data []byte) (ingest.Result, error) {
	r, _, err := docs.Get[RPARun](ctx, o.Docs, tenant, cRPARun, run)
	if err != nil || r.Agent != a.ID || r.Status != "Running" || step < 0 || step >= len(r.Steps) || r.Steps[step].Kind != "ledgerline_upload" {
		return ingest.Result{}, fmt.Errorf("%w: not available", ErrForbidden)
	}
	source := r.Steps[step].Source
	if IsMasterSource(source) {
		return o.LoadFile(ctx, tenant, r.User, source, r.Context["tradedate"], name, data)
	}
	if err := o.require(ctx, tenant, r.User, "Loading files", RoleMaker, RoleChecker, RoleRisk, RoleCompliance); err != nil {
		return ingest.Result{}, err
	}
	return o.App.IngestFile(ctx, tenant, r.User, source, "", name, data)
}

// RPANotify is a run's "Message me" step.
func (o *Ops) RPANotify(ctx context.Context, tenant string, a RPAAgent, run string, step int, text string) error {
	r, _, err := docs.Get[RPARun](ctx, o.Docs, tenant, cRPARun, run)
	if err != nil || r.Agent != a.ID || r.Status != "Running" || step < 0 || step >= len(r.Steps) || r.Steps[step].Kind != "notify" {
		return fmt.Errorf("%w: not available", ErrForbidden)
	}
	o.rpaTell(ctx, tenant, r.User, r.FlowName+": "+trunc(text, 1000), r.ID)
	return nil
}

// rpaTick starts scheduled workflows and closes runs whose computer went
// quiet.
func (o *Ops) rpaTick(ctx context.Context, tenant string, now time.Time, schedules bool) error {
	now = now.In(ist)
	runs, err := docs.List[RPARun](ctx, o.Docs, tenant, cRPARun, "")
	if err != nil {
		return err
	}
	for _, r := range runs {
		if r.Status != "Running" && r.Status != "Paused" {
			continue
		}
		seen, err := time.Parse(time.RFC3339, firstOf(r.Seen, r.Started))
		if err == nil && now.Sub(seen) > rpaStall {
			_, _ = docs.Update(ctx, o.Docs, tenant, cRPARun, r.ID, SystemActor, func(x *RPARun) error {
				x.Status, x.Error, x.Ended = "Failed", "the computer stopped responding (switched off, asleep or offline)", now.Format(time.RFC3339)
				return nil
			})
			o.rpaTell(ctx, tenant, r.User, fmt.Sprintf("Your workflow %s stopped: the computer stopped responding.", r.FlowName), r.ID)
		}
	}
	if !schedules {
		return nil
	}
	set, _ := o.Settings(ctx, tenant)
	today, hm := now.Format("2006-01-02"), now.Format("15:04")
	fs, err := docs.List[RPAFlow](ctx, o.Docs, tenant, cRPAFlow, "")
	if err != nil {
		return err
	}
	for _, f := range fs {
		s := f.Schedule
		if s == nil || !s.Active || f.LastDate == today || hm < s.At || !(Job{When: s.When}).runsOn(set, today) || strings.HasPrefix(f.Owner, "*deleted:") {
			continue
		}
		_, _ = docs.Update(ctx, o.Docs, tenant, cRPAFlow, f.ID, SystemActor, func(x *RPAFlow) error { x.LastDate = today; return nil })
		if _, err := o.queueRun(ctx, tenant, f.Owner, f, s.Agent, "schedule"); err != nil {
			o.rpaTell(ctx, tenant, f.Owner, fmt.Sprintf("Your scheduled workflow %s could not start: %v", f.Name, err), "")
		}
	}
	return nil
}

// ---- saved logins ----

// RPACredInput is a saved login as entered. Empty Password or TOTP keeps
// the saved one; ClearTOTP removes it.
type RPACredInput struct {
	Name      string   `json:"name"`
	Site      string   `json:"site"`
	Username  string   `json:"username"`
	Password  string   `json:"password"`
	TOTP      string   `json:"totp"`
	ClearTOTP bool     `json:"clear_totp"`
	Notes     string   `json:"notes"`
	Firm      bool     `json:"firm"`  // a firm login (Admins)
	Users     []string `json:"users"` // firm logins: who may use them
}

// SaveRPACred creates (id empty) or changes a saved login.
func (o *Ops) SaveRPACred(ctx context.Context, tenant, actor, id string, in RPACredInput) (RPACredView, error) {
	u, err := o.rpaUser(ctx, tenant, actor)
	if err != nil {
		return RPACredView{}, err
	}
	var c RPACred
	if id != "" {
		c, _, err = docs.Get[RPACred](ctx, o.Docs, tenant, cRPACred, id)
		if err != nil || !c.canEdit(u) {
			return RPACredView{}, fmt.Errorf("%w: no such saved login", ErrNotFound)
		}
	} else {
		c = RPACred{ID: rpaID("CR-", 5), Owner: actor, Created: o.rpaNow()}
		if in.Firm {
			if err := o.require(ctx, tenant, actor, "Managing the firm's saved logins"); err != nil {
				return RPACredView{}, err
			}
			c.Owner = rpaFirmOwner
		}
	}
	in.Name, in.Site, in.Username = strings.TrimSpace(in.Name), strings.TrimSpace(in.Site), strings.TrimSpace(in.Username)
	if in.Name == "" || len(in.Name) > 60 || len(in.Site) > 200 || len(in.Username) > 200 || len(in.Password) > 500 || len(in.Notes) > 500 {
		return RPACredView{}, invalid("give the login a name (up to 60 characters); keep the other fields short")
	}
	if id == "" && in.Password == "" {
		return RPACredView{}, invalid("type the password to save")
	}
	if t := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(in.TOTP), " ", "")); t != "" {
		if _, err := totpCode(t, o.now()); err != nil {
			return RPACredView{}, invalid("the authenticator key is the code shown under the QR code when setting up an authenticator app (letters A-Z and 2-7)")
		}
		c.TOTP = t
	} else if in.ClearTOTP {
		c.TOTP = ""
	}
	if in.Password != "" {
		c.Password = in.Password
	}
	c.Name, c.Site, c.Username, c.Notes, c.Updated = in.Name, in.Site, in.Username, strings.TrimSpace(in.Notes), o.rpaNow()
	if c.Owner == rpaFirmOwner {
		c.Users = nil
		for _, x := range in.Users {
			if xu, err := o.user(ctx, tenant, x); err == nil && xu.Active && !contains(c.Users, x) {
				c.Users = append(c.Users, x)
			}
		}
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cRPACred, c.ID, c, actor); err != nil {
		return RPACredView{}, err
	}
	o.log(ctx, tenant, actor, "rpa.cred.save", c.ID, map[string]any{"name": c.Name, "site": c.Site, "owner": c.Owner, "password_changed": in.Password != "", "users": c.Users})
	return c.view(true), nil
}

// DeleteRPACred removes a saved login.
func (o *Ops) DeleteRPACred(ctx context.Context, tenant, actor, id string) error {
	u, err := o.rpaUser(ctx, tenant, actor)
	if err != nil {
		return err
	}
	c, _, err := docs.Get[RPACred](ctx, o.Docs, tenant, cRPACred, id)
	if err != nil || !c.canEdit(u) {
		return fmt.Errorf("%w: no such saved login", ErrNotFound)
	}
	// Overwrite the secrets: the record stays (history is kept) but holds
	// nothing usable, and no one can use it.
	gone := RPACred{ID: c.ID, Owner: "*deleted", Name: c.Name, Created: c.Created, Updated: o.rpaNow()}
	if err := docs.Upsert(ctx, o.Docs, tenant, cRPACred, id, gone, actor); err != nil {
		return err
	}
	o.log(ctx, tenant, actor, "rpa.cred.delete", id, map[string]any{"name": c.Name})
	return nil
}

// totpCode is the current authenticator-app code (RFC 6238: 30 seconds,
// six digits, HMAC-SHA1).
func totpCode(seed string, now time.Time) (string, error) {
	seed = strings.TrimRight(strings.ToUpper(strings.ReplaceAll(seed, " ", "")), "=")
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(seed)
	if err != nil || len(key) < 10 {
		return "", errors.New("bad seed")
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(now.Unix()/30))
	m := hmac.New(sha1.New, key)
	m.Write(msg[:])
	s := m.Sum(nil)
	off := s[len(s)-1] & 0x0f
	n := binary.BigEndian.Uint32(s[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", n%1000000), nil
}

// ScheduledAutomation is a browser automation's schedule, listed with the
// scheduled jobs so every timed run is in one place.
type ScheduledAutomation struct {
	Flow       string `json:"flow"`
	Name       string `json:"name"`
	Owner      string `json:"owner"`
	At         string `json:"at"`
	When       string `json:"when"`
	Active     bool   `json:"active"`
	LastDate   string `json:"last_date,omitempty"`
	LastStatus string `json:"last_status,omitempty"`
}

// ScheduledAutomations lists the browser automations that run on a schedule.
func (o *Ops) ScheduledAutomations(ctx context.Context, tenant string) []ScheduledAutomation {
	fs, _ := docs.List[RPAFlow](ctx, o.Docs, tenant, cRPAFlow, "")
	out := []ScheduledAutomation{}
	for _, f := range fs {
		if f.Schedule == nil {
			continue
		}
		out = append(out, ScheduledAutomation{Flow: f.ID, Name: f.Name, Owner: f.Owner, At: f.Schedule.At, When: f.Schedule.When, Active: f.Schedule.Active, LastDate: f.LastDate, LastStatus: f.LastStatus})
	}
	return out
}
