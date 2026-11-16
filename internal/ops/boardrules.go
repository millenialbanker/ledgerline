package ops

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
)

// ---- Board automation: rules, buttons, scheduled and due-date commands ----
//
// Trello's Butler for Ledgerline's boards. Each board has its own
// automation, made by the board's admins and members (observers read it):
//
//   - Rules: when something happens to a card (added to the board or a
//     list, moved into or out of a list, a label or member added, a due
//     date set, marked complete, a checklist completed, archived, a comment
//     posted) and the card meets the conditions, do the actions in order.
//   - Card buttons: named buttons in the card window that do the actions
//     on that card.
//   - Board buttons: named buttons in the board header that do the actions
//     on every open card of a list (or of the board) meeting the conditions.
//   - Scheduled commands: every day, weekday, working day, given weekdays,
//     day N of the month, or first or last working day of the month at
//     HH:MM IST, do board actions (create a card, move all cards of a list,
//     sort or archive a list, notify people).
//   - Due-date commands: N minutes, hours or days before or after a card's
//     due date, once per card per due date.
//
// Actions run as AutomationActor, so the board's activity shows automation
// did them, with an entry naming the rule. A rule's actions can set off
// other rules; a chain stops at depth 3 (baMaxDepth) and the log says so.
// A board runs at most baDailyCap automations a day. An automation that
// fails never fails the person's own change: it is logged, with the reason,
// and shown as the automation's last error.

const (
	cBoardAuto    = "boardrule"    // "<board>|<id>": rules, buttons and commands
	cBoardAutoLog = "boardautolog" // "<board>|<seq, 12 digits>": runs
	cBoardAutoDue = "boardautodue" // "<board>|<command>|<card>": the due date a due-date command last ran for
	cBoardAutoDay = "boardautoday" // "<board>|<YYYY-MM-DD>": runs that day

	// AutomationActor does what board automation does.
	AutomationActor = "system:automation"

	AutoRule        = "rule"
	AutoCardButton  = "card_button"
	AutoBoardButton = "board_button"
	AutoScheduled   = "schedule"
	AutoDueDate     = "due"

	baMaxDepth    = 3              // rules setting off rules
	baConfirmOver = 20             // a board button on more cards asks first
	baMaxPerBoard = 200            // automations a board can have
	baLate        = 3 * time.Hour  // a scheduled command missed while Ledgerline was down still runs this late
	baDueWindow   = 24 * time.Hour // likewise for a due-date command
)

// baDailyCap is the most automation runs a board has in a day (IST).
var baDailyCap = 1000

func init() {
	OnBoardEvent(baOnEvent)
	boardTickHooks = append(boardTickHooks, baTick)
}

// BoardAuto is one rule, button or command of a board.
type BoardAuto struct {
	Board      string       `json:"board"`
	ID         string       `json:"id"`
	Seq        int64        `json:"seq"`  // rules run, and buttons show, in the order they were made
	Kind       string       `json:"kind"` // rule, card_button, board_button, schedule, due
	Name       string       `json:"name"`
	On         bool         `json:"on"`
	Trigger    *AutoTrigger `json:"trigger,omitempty"` // rule
	When       *AutoWhen    `json:"when,omitempty"`    // schedule
	Due        *AutoDueAt   `json:"due,omitempty"`     // due
	List       string       `json:"list,omitempty"`    // board button: the list whose cards it acts on ("" every list)
	Conditions []AutoCond   `json:"conditions,omitempty"`
	Actions    []AutoAction `json:"actions"`
	// Since: scheduled and due-date commands run for moments from this
	// time on (when made or last switched on), never for earlier ones.
	Since      string `json:"since,omitempty"`
	LastSlot   string `json:"last_slot,omitempty"` // schedule: the date and time it last ran for
	Runs       int    `json:"runs"`
	LastRun    string `json:"last_run,omitempty"`
	LastStatus string `json:"last_status,omitempty"`
	LastError  string `json:"last_error,omitempty"`
	Stopped    string `json:"stopped,omitempty"` // when the loop guard last stopped it
	CreatedBy  string `json:"created_by"`
	CreatedAt  string `json:"created_at"`
	UpdatedBy  string `json:"updated_by,omitempty"`
	UpdatedAt  string `json:"updated_at"`
	Deleted    bool   `json:"deleted,omitempty"`
}

// AutoTrigger is what sets a rule off.
type AutoTrigger struct {
	// Event: card_added, card_moved_in, card_moved_out, label_added,
	// label_removed, member_added, due_set, card_done, checklist_done,
	// card_archived, comment_posted.
	Event  string `json:"event"`
	List   string `json:"list,omitempty"`   // card_added, card_moved_in, card_moved_out ("" any list)
	Label  string `json:"label,omitempty"`  // label_added, label_removed ("" any label)
	Member string `json:"member,omitempty"` // member_added ("" anyone)
	Words  string `json:"words,omitempty"`  // comment_posted: only comments containing these words
	By     string `json:"by,omitempty"`     // only when this person does it ("" anyone)
}

// AutoCond is a condition the card must meet.
type AutoCond struct {
	// Kind: has_label, lacks_label, has_member, lacks_member, in_list,
	// not_in_list, title_contains, has_due, no_due, overdue, complete,
	// not_complete.
	Kind  string `json:"kind"`
	Value string `json:"value,omitempty"` // a label, person ("" anyone), list or words
}

// AutoAction is one thing automation does.
type AutoAction struct {
	// Kind, on the card: move, copy, add_label, remove_label, add_member,
	// remove_member, join, set_due, clear_due, mark_done, mark_undone,
	// add_checklist, comment, archive. On the board: create_card, move_all,
	// archive_list. Either: sort_list, notify.
	Kind        string   `json:"kind"`
	List        string   `json:"list,omitempty"`  // move, copy, create_card, sort_list, archive_list; move_all: to
	From        string   `json:"from,omitempty"`  // move_all
	Top         bool     `json:"top,omitempty"`   // move, copy, create_card: at the top of the list
	Label       string   `json:"label,omitempty"` // add_label, remove_label
	Member      string   `json:"member,omitempty"`
	Days        int      `json:"days,omitempty"` // set_due, create_card: due in so many days
	Time        string   `json:"time,omitempty"` // HH:MM ("" a date without a time)
	WorkingDays bool     `json:"working_days,omitempty"`
	Due         bool     `json:"due,omitempty"`   // create_card: give it a due date
	Name        string   `json:"name,omitempty"`  // add_checklist, create_card: the checklist's name
	Items       []string `json:"items,omitempty"` // add_checklist, create_card: checklist items
	Text        string   `json:"text,omitempty"`  // comment, notify (the message), create_card (the title)
	To          []string `json:"to,omitempty"`    // notify: people, "card_members", "watchers"
	By          string   `json:"by,omitempty"`    // sort_list: due, title, newest, oldest
	Labels      []string `json:"labels,omitempty"`
	Members     []string `json:"members,omitempty"`
}

// AutoWhen is when a scheduled command runs (IST).
type AutoWhen struct {
	// Every: day, weekday (Monday to Friday), working_day (Monday to
	// Friday except the firm's trading holidays), weekdays (Days),
	// month_day (Day), first_working, last_working (of the month).
	Every        string `json:"every"`
	Days         []int  `json:"days,omitempty"` // weekdays: 0 Sunday … 6 Saturday
	Day          int    `json:"day,omitempty"`  // month_day: 1–31 (the month's last day when it is shorter)
	At           string `json:"at"`             // HH:MM
	SkipHolidays bool   `json:"skip_holidays,omitempty"`
}

// AutoDueAt is when a due-date command runs for a card.
type AutoDueAt struct {
	N     int    `json:"n"`    // 0: at the due time
	Unit  string `json:"unit"` // minutes, hours, days
	When  string `json:"when"` // before, after
	List  string `json:"list,omitempty"`
	Label string `json:"label,omitempty"`
	Done  bool   `json:"done,omitempty"` // complete cards too (otherwise only cards not complete)
}

// BoardAutoRun is one run of an automation, for the automation log.
type BoardAutoRun struct {
	Board  string   `json:"board"`
	Seq    int64    `json:"seq"`
	At     string   `json:"at"`
	Auto   string   `json:"auto"`
	Kind   string   `json:"kind"`
	Name   string   `json:"name"`
	Card   string   `json:"card,omitempty"`
	CardNo int64    `json:"card_no,omitempty"`
	Title  string   `json:"title,omitempty"`
	Cards  int      `json:"cards,omitempty"` // cards a board button acted on
	By     string   `json:"by,omitempty"`    // who set it off
	Why    string   `json:"why"`
	Did    []string `json:"did"`
	Status string   `json:"status"` // done, failed, stopped (loop guard), limit (daily cap), skipped (holiday)
	Error  string   `json:"error,omitempty"`
}

// BoardAutoView is a board's automation, for the console.
type BoardAutoView struct {
	Autos     []BoardAuto `json:"autos"`
	Role      string      `json:"role"`
	CanEdit   bool        `json:"can_edit"`
	RunsToday int         `json:"runs_today"`
	Cap       int         `json:"cap"`
	Confirm   int         `json:"confirm_over"` // board buttons on more cards ask first
}

// BoardAutoResult is what pressing a button (or running a command now) did.
type BoardAutoResult struct {
	NeedsConfirm bool          `json:"needs_confirm,omitempty"`
	Cards        int           `json:"cards"`
	Run          *BoardAutoRun `json:"run,omitempty"`
}

type baDay struct {
	N    int  `json:"n"`
	Told bool `json:"told,omitempty"`
}

type baDueMark struct {
	Card string `json:"card"`
	Due  string `json:"due"`
}

var errBANoChange = errors.New("no change")

var (
	baKinds = map[string]string{AutoRule: "rule", AutoCardButton: "card button", AutoBoardButton: "board button", AutoScheduled: "scheduled command", AutoDueDate: "due-date command"}
	// baEventOf maps a rule's trigger to the board event it listens for.
	baEventOf = map[string]string{
		"card_added": "card.created", "card_moved_in": "card.moved", "card_moved_out": "card.moved",
		"label_added": "card.label_added", "label_removed": "card.label_removed", "member_added": "card.member_added",
		"due_set": "card.due_set", "card_done": "card.due_done", "checklist_done": "checklist.done",
		"card_archived": "card.archived", "comment_posted": "comment.added",
	}
	baConds     = []string{"has_label", "lacks_label", "has_member", "lacks_member", "in_list", "not_in_list", "title_contains", "has_due", "no_due", "overdue", "complete", "not_complete"}
	baCardOnly  = map[string]bool{"move": true, "copy": true, "add_label": true, "remove_label": true, "add_member": true, "remove_member": true, "join": true, "set_due": true, "clear_due": true, "mark_done": true, "mark_undone": true, "add_checklist": true, "comment": true, "archive": true}
	baBoardOnly = map[string]bool{"create_card": true, "move_all": true, "archive_list": true}
	// A Ledgerline board's cards follow their records: automation may not
	// move, copy, archive or create them.
	baNotLedgerline = map[string]bool{"move": true, "copy": true, "archive": true, "create_card": true, "move_all": true, "archive_list": true}
	baSorts         = map[string]string{"due": "due date", "title": "title", "newest": "newest first", "oldest": "oldest first"}
	baUnits         = map[string]time.Duration{"minutes": time.Minute, "hours": time.Hour, "days": 24 * time.Hour}
	baHHMM          = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)
)

// ---- the loop guard: how deep in a chain of automation a change is ----

type baCtxKey struct{}

type baChain struct {
	depth int
	by    string // the person whose change started the chain
}

func baState(ctx context.Context) baChain {
	s, _ := ctx.Value(baCtxKey{}).(baChain)
	return s
}

// ---- reading and changing a board's automation ----

func baKey(board, id string) string { return board + "|" + id }

func (o *Ops) baList(ctx context.Context, tenant, board string) ([]BoardAuto, error) {
	all, err := docs.List[BoardAuto](ctx, o.Docs, tenant, cBoardAuto, board+"|")
	if err != nil {
		return nil, err
	}
	out := []BoardAuto{}
	for _, a := range all {
		if !a.Deleted {
			out = append(out, a)
		}
	}
	order := map[string]int{AutoRule: 0, AutoCardButton: 1, AutoBoardButton: 2, AutoScheduled: 3, AutoDueDate: 4}
	sort.SliceStable(out, func(i, j int) bool {
		if order[out[i].Kind] != order[out[j].Kind] {
			return order[out[i].Kind] < order[out[j].Kind]
		}
		return out[i].Seq < out[j].Seq
	})
	return out, nil
}

// BoardAutomation is a board's rules, buttons and commands, with today's
// run count. Anyone who can see the board can read it.
func (o *Ops) BoardAutomation(ctx context.Context, tenant, actor, board string) (BoardAutoView, error) {
	b, v, err := o.boardFor(ctx, tenant, actor, board)
	if err != nil {
		return BoardAutoView{}, err
	}
	as, err := o.baList(ctx, tenant, board)
	if err != nil {
		return BoardAutoView{}, err
	}
	day, _, _ := docs.GetOr(ctx, o.Docs, tenant, cBoardAutoDay, board+"|"+o.now().Format("2006-01-02"), baDay{})
	return BoardAutoView{Autos: as, Role: v.board, CanEdit: v.can(BoardAdmin, BoardMember) && !b.Closed, RunsToday: day.N, Cap: baDailyCap, Confirm: baConfirmOver}, nil
}

// SaveBoardAuto makes a rule, button or command (no ID) or changes one
// (board admins and members).
func (o *Ops) SaveBoardAuto(ctx context.Context, tenant, actor, board string, in BoardAuto) (BoardAuto, error) {
	b, _, err := o.boardFor(ctx, tenant, actor, board, BoardAdmin, BoardMember)
	if err != nil {
		return BoardAuto{}, err
	}
	if err := o.baCheck(ctx, tenant, b, &in); err != nil {
		return BoardAuto{}, err
	}
	now := o.now().Format(time.RFC3339)
	if in.ID == "" {
		as, err := o.baList(ctx, tenant, board)
		if err != nil {
			return BoardAuto{}, err
		}
		if len(as) >= baMaxPerBoard {
			return BoardAuto{}, invalid("a board can have at most %d rules, buttons and commands", baMaxPerBoard)
		}
		seq, err := o.next(ctx, tenant, "boardauto:"+board, actor)
		if err != nil {
			return BoardAuto{}, err
		}
		a := BoardAuto{Board: board, ID: boardID("BA-", 5), Seq: seq, Kind: in.Kind, Name: in.Name, On: true, Trigger: in.Trigger, When: in.When, Due: in.Due, List: in.List,
			Conditions: in.Conditions, Actions: in.Actions, Since: now, CreatedBy: actor, CreatedAt: now, UpdatedAt: now}
		if _, err := docs.Put(ctx, o.Docs, tenant, cBoardAuto, baKey(board, a.ID), a, 0, actor); err != nil {
			return BoardAuto{}, err
		}
		o.boardLog(ctx, tenant, actor, b, "", "automation.created", fmt.Sprintf("made the %s “%s”", baKinds[a.Kind], a.Name), map[string]string{"automation": a.ID})
		return a, nil
	}
	a, err := docs.Update(ctx, o.Docs, tenant, cBoardAuto, baKey(board, in.ID), actor, func(x *BoardAuto) error {
		if x.Deleted {
			return fmt.Errorf("%w: no such automation", ErrNotFound)
		}
		if x.Kind != in.Kind {
			return invalid("a %s stays a %s; make a new one instead", baKinds[x.Kind], baKinds[x.Kind])
		}
		x.Name, x.Trigger, x.When, x.Due, x.List, x.Conditions, x.Actions = in.Name, in.Trigger, in.When, in.Due, in.List, in.Conditions, in.Actions
		x.UpdatedBy, x.UpdatedAt = actor, now
		return nil
	})
	if errors.Is(err, docs.ErrNotFound) {
		return a, fmt.Errorf("%w: no such automation", ErrNotFound)
	}
	if err == nil {
		o.boardLog(ctx, tenant, actor, b, "", "automation.updated", fmt.Sprintf("changed the %s “%s”", baKinds[a.Kind], a.Name), map[string]string{"automation": a.ID})
	}
	return a, err
}

// SetBoardAutoOn switches an automation on or off. A command switched
// back on runs for moments from then on, not for those it missed.
func (o *Ops) SetBoardAutoOn(ctx context.Context, tenant, actor, board, id string, on bool) (BoardAuto, error) {
	b, _, err := o.boardFor(ctx, tenant, actor, board, BoardAdmin, BoardMember)
	if err != nil {
		return BoardAuto{}, err
	}
	changed := false
	a, err := docs.Update(ctx, o.Docs, tenant, cBoardAuto, baKey(board, id), actor, func(x *BoardAuto) error {
		if x.Deleted {
			return fmt.Errorf("%w: no such automation", ErrNotFound)
		}
		changed = x.On != on
		if on && !x.On {
			x.Since = o.now().Format(time.RFC3339)
		}
		x.On, x.UpdatedBy, x.UpdatedAt = on, actor, o.now().Format(time.RFC3339)
		return nil
	})
	if errors.Is(err, docs.ErrNotFound) {
		return a, fmt.Errorf("%w: no such automation", ErrNotFound)
	}
	if err == nil && changed {
		o.boardLog(ctx, tenant, actor, b, "", "automation.switched", fmt.Sprintf("switched %s the %s “%s”", map[bool]string{true: "on", false: "off"}[on], baKinds[a.Kind], a.Name), map[string]string{"automation": a.ID})
	}
	return a, err
}

// DeleteBoardAuto deletes an automation (its runs stay in the log).
func (o *Ops) DeleteBoardAuto(ctx context.Context, tenant, actor, board, id string) error {
	b, _, err := o.boardFor(ctx, tenant, actor, board, BoardAdmin, BoardMember)
	if err != nil {
		return err
	}
	a, err := docs.Update(ctx, o.Docs, tenant, cBoardAuto, baKey(board, id), actor, func(x *BoardAuto) error {
		if x.Deleted {
			return fmt.Errorf("%w: no such automation", ErrNotFound)
		}
		x.Deleted, x.On, x.UpdatedBy, x.UpdatedAt = true, false, actor, o.now().Format(time.RFC3339)
		return nil
	})
	if errors.Is(err, docs.ErrNotFound) {
		return fmt.Errorf("%w: no such automation", ErrNotFound)
	}
	if err == nil {
		o.boardLog(ctx, tenant, actor, b, "", "automation.deleted", fmt.Sprintf("deleted the %s “%s”", baKinds[a.Kind], a.Name), map[string]string{"automation": a.ID})
	}
	return err
}

// BoardAutoLog is the board's automation runs, newest first (one
// automation's with auto), before a sequence number for paging.
func (o *Ops) BoardAutoLog(ctx context.Context, tenant, actor, board, auto string, before int64, limit int) ([]BoardAutoRun, error) {
	if _, _, err := o.boardFor(ctx, tenant, actor, board); err != nil {
		return nil, err
	}
	rs, err := docs.List[BoardAutoRun](ctx, o.Docs, tenant, cBoardAutoLog, board+"|")
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	out := []BoardAutoRun{}
	for i := len(rs) - 1; i >= 0 && len(out) < limit; i-- {
		if (auto == "" || rs[i].Auto == auto) && (before <= 0 || rs[i].Seq < before) {
			out = append(out, rs[i])
		}
	}
	return out, nil
}

// RunBoardAuto presses a button: a card button on a card, a board button
// on its cards (more than 20 only with confirm), or runs a scheduled
// command now (board admins and members).
func (o *Ops) RunBoardAuto(ctx context.Context, tenant, actor, board, id, card string, confirm bool) (BoardAutoResult, error) {
	b, _, err := o.boardFor(ctx, tenant, actor, board, BoardAdmin, BoardMember)
	if err != nil {
		return BoardAutoResult{}, err
	}
	a, _, err := docs.Get[BoardAuto](ctx, o.Docs, tenant, cBoardAuto, baKey(board, id))
	if err != nil || a.Deleted {
		return BoardAutoResult{}, fmt.Errorf("%w: no such automation", ErrNotFound)
	}
	if !a.On {
		return BoardAutoResult{}, invalid("“%s” is switched off", a.Name)
	}
	now := o.now()
	switch a.Kind {
	case AutoCardButton:
		c, _, err := o.GetCard(ctx, tenant, actor, board, card)
		if err != nil {
			return BoardAutoResult{}, err
		}
		run := o.baRun(ctx, tenant, b, a, c.ID, actor, "pressed by "+actor, now, func(r *baRunner) error { return r.onCard(c.ID, a.Actions) })
		return BoardAutoResult{Cards: 1, Run: &run}, nil
	case AutoBoardButton:
		ids := o.baButtonCards(ctx, tenant, b, a, now)
		if len(ids) > baConfirmOver && !confirm {
			return BoardAutoResult{NeedsConfirm: true, Cards: len(ids)}, nil
		}
		run := o.baRun(ctx, tenant, b, a, "", actor, "pressed by "+actor, now, func(r *baRunner) error { return r.onCards(ids, a.Actions) })
		return BoardAutoResult{Cards: len(ids), Run: &run}, nil
	case AutoScheduled:
		run := o.baRun(ctx, tenant, b, a, "", actor, "run now by "+actor, now, func(r *baRunner) error { return r.onBoard(a.Actions) })
		return BoardAutoResult{Run: &run}, nil
	}
	return BoardAutoResult{}, invalid("only buttons and scheduled commands run when asked; rules and due-date commands run when their moment comes")
}

// baButtonCards are the open cards a board button acts on.
func (o *Ops) baButtonCards(ctx context.Context, tenant string, b Board, a BoardAuto, now time.Time) []string {
	cs, _ := o.boardCards(ctx, tenant, b.ID)
	var ids []string
	for _, c := range cs {
		if c.Archived || c.Template || (a.List != "" && c.List != a.List) {
			continue
		}
		if l, ok := boardList(b, c.List); !ok || l.Archived {
			continue
		}
		if baCondsOK(a.Conditions, c, now) {
			ids = append(ids, c.ID)
		}
	}
	return ids
}

// ---- checking what a person saves ----

func (o *Ops) baCheck(ctx context.Context, tenant string, b Board, a *BoardAuto) error {
	var err error
	if a.Name, err = textField("name", a.Name, 120, true); err != nil {
		return err
	}
	users, _ := o.Users(ctx, tenant)
	person := func(id string) error {
		if len(users) == 0 {
			return nil // a single-user setup
		}
		for _, u := range users {
			if u.ID == id && u.Active && u.Role != RoleClient && boardRole(b, u) != "" {
				return nil
			}
		}
		return invalid("%s is not someone who can see this board", firstOf(id, "(no one)"))
	}
	list := func(id, what string) error {
		if l, ok := boardList(b, id); !ok || l.Archived {
			return invalid("choose an open list %s", what)
		}
		return nil
	}
	label := func(id string) error {
		if _, ok := boardLabel(b, id); !ok {
			return invalid("no such label on this board")
		}
		return nil
	}
	hasCard := true
	switch a.Kind {
	case AutoRule:
		a.When, a.Due, a.List = nil, nil, ""
		t := a.Trigger
		if t == nil || baEventOf[t.Event] == "" {
			return invalid("choose what sets the rule off")
		}
		n := AutoTrigger{Event: t.Event, By: strings.TrimSpace(t.By)}
		switch t.Event {
		case "card_added", "card_moved_in", "card_moved_out":
			if n.List = t.List; n.List != "" {
				if err := list(n.List, "for the rule"); err != nil {
					return err
				}
			}
		case "label_added", "label_removed":
			if n.Label = t.Label; n.Label != "" {
				if err := label(n.Label); err != nil {
					return err
				}
			}
		case "member_added":
			if n.Member = t.Member; n.Member != "" {
				if err := person(n.Member); err != nil {
					return err
				}
			}
		case "comment_posted":
			if n.Words, err = textField("words", t.Words, 200, false); err != nil {
				return err
			}
		}
		if n.By != "" {
			if err := person(n.By); err != nil {
				return err
			}
		}
		a.Trigger = &n
	case AutoCardButton:
		a.Trigger, a.When, a.Due, a.List = nil, nil, nil, ""
		if len(a.Conditions) > 0 {
			return invalid("a card button acts on the card it is pressed on; it has no conditions")
		}
	case AutoBoardButton:
		a.Trigger, a.When, a.Due = nil, nil, nil
		if a.List != "" {
			if err := list(a.List, "for the button's cards"); err != nil {
				return err
			}
		}
	case AutoScheduled:
		hasCard = false
		a.Trigger, a.Due, a.List = nil, nil, ""
		if len(a.Conditions) > 0 {
			return invalid("a scheduled command has no card to check conditions on; a board button's conditions choose cards")
		}
		w := a.When
		if w == nil {
			return invalid("choose when the command runs")
		}
		n := AutoWhen{Every: w.Every, At: strings.TrimSpace(w.At), SkipHolidays: w.SkipHolidays}
		if !baHHMM.MatchString(n.At) {
			return invalid("give the time as HH:MM (IST), e.g. 09:30")
		}
		switch w.Every {
		case "day", "weekday", "working_day", "first_working", "last_working":
		case "weekdays":
			seen := map[int]bool{}
			for _, d := range w.Days {
				if d < 0 || d > 6 {
					return invalid("weekdays are 0 (Sunday) to 6 (Saturday)")
				}
				if !seen[d] {
					seen[d] = true
					n.Days = append(n.Days, d)
				}
			}
			if len(n.Days) == 0 {
				return invalid("choose the days of the week")
			}
			sort.Ints(n.Days)
		case "month_day":
			if w.Day < 1 || w.Day > 31 {
				return invalid("the day of the month is 1 to 31")
			}
			n.Day = w.Day
		default:
			return invalid("a command runs every day, weekday, working day, given weekdays, day of the month, or first or last working day of the month")
		}
		if n.Every == "working_day" || n.Every == "first_working" || n.Every == "last_working" {
			n.SkipHolidays = false // holidays are already left out
		}
		a.When = &n
	case AutoDueDate:
		a.Trigger, a.When, a.List = nil, nil, ""
		d := a.Due
		if d == nil {
			return invalid("choose when, from the due date, the command runs")
		}
		max := map[string]int{"minutes": 60 * 24, "hours": 24 * 30, "days": 90}[d.Unit]
		if max == 0 {
			return invalid("the time from the due date is in minutes, hours or days")
		}
		if d.N < 0 || d.N > max {
			return invalid("at most %d %s from the due date", max, d.Unit)
		}
		if d.When != "before" && d.When != "after" {
			return invalid("the command runs before or after the due date")
		}
		n := *d
		if n.List != "" {
			if err := list(n.List, "for the command"); err != nil {
				return err
			}
		}
		if n.Label != "" {
			if err := label(n.Label); err != nil {
				return err
			}
		}
		a.Due = &n
	default:
		return invalid("an automation is a rule, a card button, a board button, a scheduled command or a due-date command")
	}
	if len(a.Conditions) > 10 {
		return invalid("at most 10 conditions")
	}
	conds := []AutoCond{}
	for _, c := range a.Conditions {
		if !contains(baConds, c.Kind) {
			return invalid("unknown condition %q", c.Kind)
		}
		n := AutoCond{Kind: c.Kind}
		switch c.Kind {
		case "has_label", "lacks_label":
			if err := label(c.Value); err != nil {
				return err
			}
			n.Value = c.Value
		case "has_member", "lacks_member":
			if n.Value = c.Value; n.Value != "" {
				if err := person(n.Value); err != nil {
					return err
				}
			}
		case "in_list", "not_in_list":
			if _, ok := boardList(b, c.Value); !ok {
				return invalid("choose the list for the condition")
			}
			n.Value = c.Value
		case "title_contains":
			if n.Value, err = textField("words to look for in the title", c.Value, 200, true); err != nil {
				return err
			}
		}
		conds = append(conds, n)
	}
	a.Conditions = conds
	if len(a.Actions) == 0 {
		return invalid("add at least one action")
	}
	if len(a.Actions) > 20 {
		return invalid("at most 20 actions")
	}
	acts := []AutoAction{}
	for _, x := range a.Actions {
		if b.Kind == BoardLedgerline && baNotLedgerline[x.Kind] {
			return invalid("on a Ledgerline board cards follow their records: automation can label, assign, date, check off, comment on and sort cards and notify people, but not move, copy, archive or create cards")
		}
		if !hasCard && baCardOnly[x.Kind] {
			return invalid("a scheduled command has no card to act on: it can create a card, move or archive a list's cards, sort a list or notify people")
		}
		n := AutoAction{Kind: x.Kind}
		switch x.Kind {
		case "move", "copy":
			if err := list(x.List, "to "+x.Kind+" the card to"); err != nil {
				return err
			}
			n.List, n.Top = x.List, x.Top
		case "add_label", "remove_label":
			if err := label(x.Label); err != nil {
				return err
			}
			n.Label = x.Label
		case "add_member", "remove_member":
			if err := person(x.Member); err != nil {
				return err
			}
			n.Member = x.Member
		case "join", "clear_due", "mark_done", "mark_undone", "archive":
		case "set_due":
			if err := baDueOK(x); err != nil {
				return err
			}
			n.Days, n.Time, n.WorkingDays = x.Days, x.Time, x.WorkingDays
		case "add_checklist":
			if n.Name, err = textField("checklist name", x.Name, 200, false); err != nil {
				return err
			}
			if n.Items, err = baItems(x.Items, true); err != nil {
				return err
			}
		case "comment":
			if n.Text, err = textField("comment", x.Text, 5000, true); err != nil {
				return err
			}
		case "sort_list":
			n.By = firstOf(x.By, "due")
			if baSorts[n.By] == "" {
				return invalid("sort by due date, title, newest or oldest")
			}
			if x.List != "" {
				if err := list(x.List, "to sort"); err != nil {
					return err
				}
			} else if !hasCard || (a.Kind == AutoBoardButton && a.List == "") {
				return invalid("choose the list to sort")
			}
			n.List = x.List
		case "notify":
			for _, u := range x.To {
				if u == "card_members" || u == "watchers" {
					if !hasCard {
						return invalid("a scheduled command has no card: notify people by name")
					}
				} else if err := person(u); err != nil {
					return err
				}
				if !contains(n.To, u) {
					n.To = append(n.To, u)
				}
			}
			if len(n.To) == 0 {
				return invalid("choose who to notify")
			}
			if n.Text, err = textField("message", x.Text, 1000, false); err != nil {
				return err
			}
		case "create_card":
			if err := list(x.List, "for the new card"); err != nil {
				return err
			}
			if n.Text, err = textField("card title", x.Text, 500, true); err != nil {
				return err
			}
			for _, l := range x.Labels {
				if err := label(l); err != nil {
					return err
				}
			}
			for _, m := range x.Members {
				if err := person(m); err != nil {
					return err
				}
			}
			if x.Due {
				if err := baDueOK(x); err != nil {
					return err
				}
			}
			if n.Name, err = textField("checklist name", x.Name, 200, false); err != nil {
				return err
			}
			if n.Items, err = baItems(x.Items, false); err != nil {
				return err
			}
			n.List, n.Top, n.Labels, n.Members, n.Due = x.List, x.Top, nonEmptyUniq(x.Labels), nonEmptyUniq(x.Members), x.Due
			if x.Due {
				n.Days, n.Time, n.WorkingDays = x.Days, x.Time, x.WorkingDays
			}
		case "move_all":
			if err := list(x.From, "to move cards from"); err != nil {
				return err
			}
			if err := list(x.List, "to move the cards to"); err != nil {
				return err
			}
			if x.From == x.List {
				return invalid("move the cards to a different list")
			}
			n.From, n.List = x.From, x.List
		case "archive_list":
			if err := list(x.List, "whose cards to archive"); err != nil {
				return err
			}
			n.List = x.List
		default:
			return invalid("unknown action %q", x.Kind)
		}
		acts = append(acts, n)
	}
	a.Actions = acts
	return nil
}

func baDueOK(x AutoAction) error {
	if x.Days < 0 || x.Days > 365 {
		return invalid("a due date is 0 to 365 days ahead")
	}
	if x.Time != "" && !baHHMM.MatchString(x.Time) {
		return invalid("give the due time as HH:MM, or leave it empty for the date alone")
	}
	return nil
}

func baItems(in []string, required bool) ([]string, error) {
	var out []string
	for _, it := range in {
		t, err := textField("checklist item", it, 500, false)
		if err != nil {
			return nil, err
		}
		if t != "" {
			out = append(out, t)
		}
	}
	if required && len(out) == 0 {
		return nil, invalid("give the checklist's items, one a line")
	}
	if len(out) > 50 {
		return nil, invalid("a checklist made by automation has at most 50 items")
	}
	return out, nil
}

// ---- matching ----

func baTriggerOK(t *AutoTrigger, ev BoardEvent) bool {
	if t == nil || baEventOf[t.Event] != ev.Kind || (t.By != "" && t.By != ev.Actor) {
		return false
	}
	d := ev.Data
	switch t.Event {
	case "card_added":
		return t.List == "" || d["list"] == t.List
	case "card_moved_in":
		return t.List == "" || d["to_list"] == t.List
	case "card_moved_out":
		return t.List == "" || d["from_list"] == t.List
	case "label_added", "label_removed":
		return t.Label == "" || d["label"] == t.Label
	case "member_added":
		return t.Member == "" || d["member"] == t.Member
	case "comment_posted":
		// A comment automation posts does not set off comment rules.
		return !strings.HasPrefix(ev.Actor, "system:") && (t.Words == "" || strings.Contains(strings.ToLower(d["text"]), strings.ToLower(t.Words)))
	}
	return true
}

func baCondsOK(cs []AutoCond, c Card, now time.Time) bool {
	for _, x := range cs {
		ok := true
		switch x.Kind {
		case "has_label":
			ok = contains(c.Labels, x.Value)
		case "lacks_label":
			ok = !contains(c.Labels, x.Value)
		case "has_member":
			ok = (x.Value == "" && len(c.Members) > 0) || (x.Value != "" && contains(c.Members, x.Value))
		case "lacks_member":
			ok = (x.Value == "" && len(c.Members) == 0) || (x.Value != "" && !contains(c.Members, x.Value))
		case "in_list":
			ok = c.List == x.Value
		case "not_in_list":
			ok = c.List != x.Value
		case "title_contains":
			ok = strings.Contains(strings.ToLower(c.Title), strings.ToLower(x.Value))
		case "has_due":
			ok = c.Due != ""
		case "no_due":
			ok = c.Due == ""
		case "overdue":
			t, has := boardTime(c.Due)
			ok = has && !c.DueDone && now.After(t)
		case "complete":
			ok = c.DueDone
		case "not_complete":
			ok = !c.DueDone
		default:
			ok = false
		}
		if !ok {
			return false
		}
	}
	return true
}

// ---- running ----

type baRunner struct {
	o      *Ops
	ctx    context.Context // carries the chain's depth
	tenant string
	b      Board
	a      BoardAuto
	by     string // the person who set it off ("" none)
	now    time.Time
	set    *Settings
	did    []string
	cards  []Card // a board button's cards, for notify
}

func (r *baRunner) note(f string, args ...any) { r.did = append(r.did, fmt.Sprintf(f, args...)) }

func (r *baRunner) card(id string) (Card, error) {
	c, _, err := docs.Get[Card](r.ctx, r.o.Docs, r.tenant, cBoardCard, cardKey(r.b.ID, id))
	if err != nil || c.Deleted {
		return Card{}, fmt.Errorf("%w: the card is no longer on this board", ErrNotFound)
	}
	return c, nil
}

func (r *baRunner) listName(id string) string {
	if l, ok := boardList(r.b, id); ok {
		return l.Name
	}
	return "a list that no longer exists"
}

func baLabelName(b Board, id string) string {
	l, ok := boardLabel(b, id)
	switch {
	case !ok:
		return "a label that no longer exists"
	case l.Name != "":
		return "label " + l.Name
	}
	return l.Color + " label"
}

func (r *baRunner) settings() Settings {
	if r.set == nil {
		s, _ := r.o.Settings(r.ctx, r.tenant)
		r.set = &s
	}
	return *r.set
}

// dueIn is a due date so many (working) days from today, at a time or not.
func (r *baRunner) dueIn(days int, at string, working bool) string {
	d := r.now.In(ist).Format("2006-01-02")
	if working {
		s := r.settings()
		for i := 0; i < days; i++ {
			d = s.NextBusinessDay(d)
		}
	} else {
		t, _ := time.ParseInLocation("2006-01-02", d, ist)
		d = t.AddDate(0, 0, days).Format("2006-01-02")
	}
	if at != "" {
		d += "T" + at
	}
	return d
}

// expand fills in {card}, {no}, {list}, {due}, {board}, {user}, {date} and {rule}.
func (r *baRunner) expand(s string, c *Card) string {
	rep := []string{"{board}", r.b.Name, "{date}", r.now.In(ist).Format("02 Jan 2006"), "{user}", firstOf(r.by, "Automation"), "{rule}", r.a.Name}
	if c != nil {
		rep = append(rep, "{card}", c.Title, "{no}", fmt.Sprintf("#%d", c.No), "{list}", r.listName(c.List), "{due}", baDueShown(c.Due))
	}
	return strings.NewReplacer(rep...).Replace(s)
}

func baDueShown(due string) string {
	if t, err := time.Parse("2006-01-02T15:04", due); err == nil {
		return t.Format("02 Jan 2006 15:04")
	}
	if t, err := time.Parse("2006-01-02", due); err == nil {
		return t.Format("02 Jan 2006")
	}
	return "no due date"
}

// top is a position above every open card of a list (nil: at the bottom).
func (r *baRunner) top(list string, top bool) *float64 {
	if !top {
		return nil
	}
	cs, _ := r.o.boardCards(r.ctx, r.tenant, r.b.ID)
	p := math.Inf(1)
	for _, c := range cs {
		if c.List == list && !c.Archived {
			p = math.Min(p, c.Pos)
		}
	}
	if math.IsInf(p, 1) {
		return nil
	}
	p /= 2
	return &p
}

// onCard does the actions on one card, in order.
func (r *baRunner) onCard(id string, acts []AutoAction) error {
	for _, a := range acts {
		c, err := r.card(id)
		if err != nil {
			return err
		}
		if err := r.do(a, &c); err != nil {
			return err
		}
	}
	return nil
}

// onCards does a board button's card actions on each card, then its other
// actions once.
func (r *baRunner) onCards(ids []string, acts []AutoAction) error {
	var per, once []AutoAction
	for _, a := range acts {
		if baCardOnly[a.Kind] {
			per = append(per, a)
		} else {
			once = append(once, a)
		}
	}
	if len(ids) == 0 {
		r.note("no cards matched")
	}
	for _, id := range ids {
		if err := r.onCard(id, per); err != nil {
			if errors.Is(err, ErrNotFound) {
				continue // another rule took the card away meanwhile
			}
			return err
		}
		if c, err := r.card(id); err == nil {
			r.cards = append(r.cards, c)
		}
	}
	return r.onBoard(once)
}

// onBoard does actions that need no card.
func (r *baRunner) onBoard(acts []AutoAction) error {
	for _, a := range acts {
		if err := r.do(a, nil); err != nil {
			return err
		}
	}
	return nil
}

func (r *baRunner) do(a AutoAction, c *Card) error {
	o, ctx, t, act := r.o, r.ctx, r.tenant, AutomationActor
	board := r.b.ID
	if c == nil && baCardOnly[a.Kind] {
		return invalid("there is no card to %s", strings.ReplaceAll(a.Kind, "_", " "))
	}
	patch := func(p CardPatch) error {
		_, err := o.UpdateCard(ctx, t, act, board, c.ID, p)
		return err
	}
	switch a.Kind {
	case "move":
		if c.List == a.List {
			r.note("#%d was already in %s", c.No, r.listName(a.List))
			return nil
		}
		if c.Link != nil {
			return invalid("#%d is linked to %s: it moves when its record does, so automation leaves it", c.No, c.Link.Label)
		}
		if _, err := o.MoveCard(ctx, t, act, board, c.ID, CardMove{List: a.List, Pos: r.top(a.List, a.Top)}); err != nil {
			return err
		}
		r.note("moved #%d to %s", c.No, r.listName(a.List))
	case "copy":
		var pos *float64
		if a.Top {
			z := 0.0
			pos = &z
		}
		nc, err := o.CopyCard(ctx, t, act, board, c.ID, CardMove{List: a.List, Pos: pos}, "", []string{"checklists", "labels", "members", "fields"})
		if err != nil {
			return err
		}
		r.note("copied #%d to %s as #%d", c.No, r.listName(a.List), nc.No)
	case "add_label":
		if contains(c.Labels, a.Label) {
			r.note("#%d already had the %s", c.No, baLabelName(r.b, a.Label))
			return nil
		}
		ls := append(append([]string(nil), c.Labels...), a.Label)
		if err := patch(CardPatch{Labels: &ls}); err != nil {
			return err
		}
		r.note("added the %s to #%d", baLabelName(r.b, a.Label), c.No)
	case "remove_label":
		if !contains(c.Labels, a.Label) {
			r.note("#%d did not have the %s", c.No, baLabelName(r.b, a.Label))
			return nil
		}
		ls := toggleIn(c.Labels, a.Label, false)
		if ls == nil {
			ls = []string{}
		}
		if err := patch(CardPatch{Labels: &ls}); err != nil {
			return err
		}
		r.note("removed the %s from #%d", baLabelName(r.b, a.Label), c.No)
	case "add_member", "join":
		m := a.Member
		if a.Kind == "join" {
			if m = r.by; m == "" {
				r.note("no one to add to #%d: no person set it off", c.No)
				return nil
			}
		}
		if contains(c.Members, m) {
			r.note("%s was already on #%d", m, c.No)
			return nil
		}
		ms := append(append([]string(nil), c.Members...), m)
		if err := patch(CardPatch{Members: &ms}); err != nil {
			return err
		}
		r.note("added %s to #%d", m, c.No)
	case "remove_member":
		if !contains(c.Members, a.Member) {
			r.note("%s was not on #%d", a.Member, c.No)
			return nil
		}
		ms := toggleIn(c.Members, a.Member, false)
		if ms == nil {
			ms = []string{}
		}
		if err := patch(CardPatch{Members: &ms}); err != nil {
			return err
		}
		r.note("took %s off #%d", a.Member, c.No)
	case "set_due":
		d := r.dueIn(a.Days, a.Time, a.WorkingDays)
		if err := patch(CardPatch{Due: &d}); err != nil {
			return err
		}
		r.note("set #%d due %s", c.No, baDueShown(d))
	case "clear_due":
		if c.Due == "" {
			r.note("#%d had no due date", c.No)
			return nil
		}
		e := ""
		if err := patch(CardPatch{Due: &e}); err != nil {
			return err
		}
		r.note("removed the due date of #%d", c.No)
	case "mark_done", "mark_undone":
		v := a.Kind == "mark_done"
		word := map[bool]string{true: "complete", false: "not complete"}[v]
		if c.DueDone == v {
			r.note("#%d was already %s", c.No, word)
			return nil
		}
		if err := patch(CardPatch{DueDone: &v}); err != nil {
			return err
		}
		r.note("marked #%d %s", c.No, word)
	case "add_checklist":
		nc, err := o.EditChecklist(ctx, t, act, board, c.ID, ChecklistOp{Op: "add_list", Name: firstOf(r.expand(a.Name, c), "Checklist")})
		if err != nil {
			return err
		}
		cl := nc.Checklists[len(nc.Checklists)-1]
		for _, it := range a.Items {
			if _, err := o.EditChecklist(ctx, t, act, board, c.ID, ChecklistOp{Op: "add_item", Checklist: cl.ID, Name: r.expand(it, c)}); err != nil {
				return err
			}
		}
		r.note("added the checklist %s (%d items) to #%d", cl.Name, len(a.Items), c.No)
	case "comment":
		if _, err := o.AddComment(ctx, t, act, board, c.ID, r.expand(a.Text, c)); err != nil {
			return err
		}
		r.note("commented on #%d", c.No)
	case "archive":
		if c.Archived {
			r.note("#%d was already archived", c.No)
			return nil
		}
		v := true
		if err := patch(CardPatch{Archived: &v}); err != nil {
			return err
		}
		r.note("archived #%d", c.No)
	case "sort_list":
		l := a.List
		if l == "" && c != nil {
			l = c.List
		}
		if l == "" {
			l = r.a.List
		}
		if err := o.SortList(ctx, t, act, board, l, firstOf(a.By, "due")); err != nil {
			return err
		}
		r.note("sorted %s by %s", r.listName(l), baSorts[firstOf(a.By, "due")])
	case "notify":
		return r.notify(a, c)
	case "create_card":
		b, _, err := docs.Get[Board](ctx, o.Docs, t, cBoard, board)
		if err != nil {
			return err
		}
		nc := Card{Title: r.expand(a.Text, c)}
		for _, l := range a.Labels {
			if _, ok := boardLabel(b, l); ok {
				nc.Labels = append(nc.Labels, l)
			}
		}
		for _, m := range a.Members {
			if role, err := o.RoleOf(ctx, t, m); err == nil && role != RoleClient {
				nc.Members = append(nc.Members, m)
			}
		}
		if a.Due {
			nc.Due = r.dueIn(a.Days, a.Time, a.WorkingDays)
		}
		if len(a.Items) > 0 {
			cl := Checklist{ID: boardID("CL-", 4), Name: firstOf(r.expand(a.Name, c), "Checklist")}
			for _, it := range a.Items {
				cl.Items = append(cl.Items, CheckItem{ID: boardID("I-", 4), Text: r.expand(it, c)})
			}
			nc.Checklists = []Checklist{cl}
		}
		pos := math.NaN()
		if a.Top {
			pos = 0
		}
		made, err := o.addCard(ctx, t, act, b, a.List, nc, pos)
		if err != nil {
			return err
		}
		r.note("made #%d %s in %s", made.No, clip(made.Title, 80), r.listName(a.List))
	case "move_all":
		cs, _ := o.boardCards(ctx, t, board)
		n, left := 0, 0
		for _, x := range cs {
			if x.List != a.From || x.Archived {
				continue
			}
			if x.Link != nil {
				left++
				continue
			}
			if _, err := o.MoveCard(ctx, t, act, board, x.ID, CardMove{List: a.List}); err != nil {
				return fmt.Errorf("moving #%d: %w", x.No, err)
			}
			n++
		}
		r.note("moved %d card%s from %s to %s", n, plural(n), r.listName(a.From), r.listName(a.List))
		if left > 0 {
			r.note("left %d card%s linked to Ledgerline records in %s", left, plural(left), r.listName(a.From))
		}
	case "archive_list":
		n, err := o.ArchiveListCards(ctx, t, act, board, a.List)
		if err != nil {
			return err
		}
		r.note("archived %d card%s in %s", n, plural(n), r.listName(a.List))
	default:
		return invalid("unknown action %q", a.Kind)
	}
	return nil
}

// notify tells the people named, and the card's members or watchers when
// asked (except the person who set it off: they know).
func (r *baRunner) notify(a AutoAction, c *Card) error {
	us, _ := r.o.Users(r.ctx, r.tenant)
	sees := func(id string) bool {
		for _, u := range us {
			if u.ID == id {
				return u.Active && u.Role != RoleClient && boardRole(r.b, u) != ""
			}
		}
		return len(us) == 0
	}
	cards := r.cards
	if c != nil {
		cards = []Card{*c}
	}
	var to []string
	add := func(id string, them bool) {
		if id == "" || strings.HasPrefix(id, "system:") || contains(to, id) || !sees(id) || (them && id == r.by) {
			return
		}
		to = append(to, id)
	}
	for _, x := range a.To {
		switch x {
		case "card_members":
			for _, cd := range cards {
				for _, m := range cd.Members {
					add(m, true)
				}
			}
		case "watchers":
			for _, cd := range cards {
				for _, w := range cd.Watchers {
					add(w, true)
				}
			}
		default:
			add(x, false)
		}
	}
	if len(to) == 0 {
		r.note("no one to notify")
		return nil
	}
	text := strings.TrimSpace(r.expand(a.Text, c))
	switch {
	case text == "" && c != nil:
		text = fmt.Sprintf("%s: %s (#%d) on %s", r.a.Name, clip(c.Title, 80), c.No, r.b.Name)
	case text == "":
		text = fmt.Sprintf("%s ran on %s", r.a.Name, r.b.Name)
	}
	card := ""
	if c != nil {
		card = c.ID
	}
	r.o.boardNotify(r.ctx, r.tenant, AutomationActor, r.b, card, to, clip(text, 1000))
	r.note("notified %s", strings.Join(to, ", "))
	return nil
}

// baRun runs an automation and records it. why says what set it off, by
// who (if a person), card the card it ran on ("" none).
func (o *Ops) baRun(ctx context.Context, tenant string, b Board, a BoardAuto, card, by, why string, now time.Time, fn func(r *baRunner) error) BoardAutoRun {
	st := baState(ctx)
	by = firstOf(by, st.by)
	run := BoardAutoRun{Board: b.ID, Auto: a.ID, Kind: a.Kind, Name: a.Name, Card: card, By: by, Why: why, At: now.In(ist).Format(time.RFC3339), Did: []string{}}
	if card != "" {
		if c, _, err := docs.Get[Card](ctx, o.Docs, tenant, cBoardCard, cardKey(b.ID, card)); err == nil {
			run.CardNo, run.Title = c.No, clip(c.Title, 120)
		}
	}
	ok, tell := o.baTake(ctx, tenant, b, now)
	if !ok {
		run.Status, run.Error = "limit", fmt.Sprintf("this board reached %d automation runs today; automation starts again tomorrow", baDailyCap)
		if tell {
			o.baLog(ctx, tenant, &run)
			var admins []string
			for _, m := range b.Members {
				if m.Role == BoardAdmin {
					admins = append(admins, m.User)
				}
			}
			o.boardNotify(ctx, tenant, AutomationActor, b, "", admins, fmt.Sprintf("Automation on %s stopped for today: it reached %d runs. The automation log shows which rule runs most.", b.Name, baDailyCap))
			o.boardLog(ctx, tenant, AutomationActor, b, "", "automation.limit", fmt.Sprintf("stopped automation for today: the board reached %d runs", baDailyCap), nil)
		}
		return run
	}
	// The run's place in the log is where it started: rules it sets off come after it.
	run.Seq, _ = o.next(ctx, tenant, "boardautolog:"+b.ID, AutomationActor)
	r := &baRunner{o: o, ctx: context.WithValue(ctx, baCtxKey{}, baChain{depth: st.depth + 1, by: by}), tenant: tenant, b: b, a: a, by: by, now: now}
	err := func() (err error) {
		defer func() {
			if p := recover(); p != nil {
				err = fmt.Errorf("stopped on an internal error (%v); tell Ledgerline support", p)
			}
		}()
		return fn(r)
	}()
	run.Did, run.Cards = r.did, len(r.cards)
	if run.Did == nil {
		run.Did = []string{}
	}
	run.Status = "done"
	if err != nil {
		run.Status, run.Error = "failed", baErrText(err)
	}
	o.baLog(ctx, tenant, &run)
	o.baStats(ctx, tenant, a, run)
	where := ""
	if run.CardNo > 0 {
		where = fmt.Sprintf(" on #%d", run.CardNo)
	}
	text := fmt.Sprintf("%s “%s” ran%s: %s", baKinds[a.Kind], a.Name, where, strings.Join(firstN(run.Did, 6), "; "))
	if run.Status == "failed" {
		text = fmt.Sprintf("%s “%s” could not finish%s: %s", baKinds[a.Kind], a.Name, where, run.Error)
	}
	o.boardLog(ctx, tenant, AutomationActor, b, card, "automation.ran", clip(text, 600), map[string]string{"automation": a.ID, "status": run.Status})
	return run
}

func firstN(xs []string, n int) []string {
	if len(xs) <= n {
		return xs
	}
	return append(append([]string(nil), xs[:n]...), fmt.Sprintf("and %d more", len(xs)-n))
}

func baErrText(err error) string {
	s := err.Error()
	for _, p := range []string{ErrInvalid.Error() + ": ", ErrNotFound.Error() + ": ", ErrForbidden.Error() + ": "} {
		s = strings.TrimPrefix(s, p)
	}
	return s
}

func (o *Ops) baLog(ctx context.Context, tenant string, run *BoardAutoRun) {
	if run.Seq == 0 {
		n, err := o.next(ctx, tenant, "boardautolog:"+run.Board, AutomationActor)
		if err != nil {
			return
		}
		run.Seq = n
	}
	rec := *run
	rec.Did = firstN(rec.Did, 40)
	_ = docs.Upsert(ctx, o.Docs, tenant, cBoardAutoLog, fmt.Sprintf("%s|%012d", run.Board, run.Seq), rec, AutomationActor)
}

func (o *Ops) baStats(ctx context.Context, tenant string, a BoardAuto, run BoardAutoRun) {
	_, _ = docs.Update(ctx, o.Docs, tenant, cBoardAuto, baKey(a.Board, a.ID), AutomationActor, func(x *BoardAuto) error {
		if run.Status == "stopped" {
			x.Stopped = run.At // the run that set it off goes on to finish
			return nil
		}
		if run.Status == "done" || run.Status == "failed" {
			x.Runs++
		}
		x.LastRun, x.LastStatus, x.LastError = run.At, run.Status, run.Error
		return nil
	})
}

// baTake counts a run against the board's daily cap. ok is false once the
// cap is reached; tell is true the first time, to say so once.
func (o *Ops) baTake(ctx context.Context, tenant string, b Board, now time.Time) (ok, tell bool) {
	id := b.ID + "|" + now.In(ist).Format("2006-01-02")
	if _, _, err := docs.Get[baDay](ctx, o.Docs, tenant, cBoardAutoDay, id); errors.Is(err, docs.ErrNotFound) {
		if _, err := docs.Put(ctx, o.Docs, tenant, cBoardAutoDay, id, baDay{}, 0, AutomationActor); err != nil && !errors.Is(err, docs.ErrStale) {
			return true, false
		}
	}
	_, err := docs.Update(ctx, o.Docs, tenant, cBoardAutoDay, id, AutomationActor, func(d *baDay) error {
		ok, tell = true, false
		if d.N >= baDailyCap {
			ok = false
			if d.Told {
				return errBANoChange
			}
			d.Told, tell = true, true
			return nil
		}
		d.N++
		return nil
	})
	if err != nil && !errors.Is(err, errBANoChange) {
		return true, false // a counting failure does not stop automation
	}
	return ok, tell
}

func (o *Ops) baRecover(ctx context.Context, tenant, board string) {
	if p := recover(); p != nil {
		o.log(ctx, tenant, AutomationActor, "board.automation_failed", board, map[string]any{"error": fmt.Sprint(p)})
	}
}

// ---- rules: board events ----

func baOnEvent(ctx context.Context, o *Ops, tenant string, ev BoardEvent) {
	defer o.baRecover(ctx, tenant, ev.Board)
	if ev.Card == "" || ev.Kind == "card.updated" {
		return
	}
	all, err := o.baList(ctx, tenant, ev.Board)
	if err != nil {
		return
	}
	var rules []BoardAuto
	for _, a := range all {
		if a.Kind == AutoRule && a.On && baTriggerOK(a.Trigger, ev) {
			rules = append(rules, a)
		}
	}
	if len(rules) == 0 {
		return
	}
	b, _, err := docs.Get[Board](ctx, o.Docs, tenant, cBoard, ev.Board)
	if err != nil || b.Deleted || b.Closed || b.Template {
		return
	}
	st := baState(ctx)
	now := o.now()
	by := ""
	if !strings.HasPrefix(ev.Actor, "system:") {
		by = ev.Actor
	}
	for _, a := range rules {
		c, _, err := docs.Get[Card](ctx, o.Docs, tenant, cBoardCard, cardKey(b.ID, ev.Card))
		if err != nil || c.Deleted {
			return
		}
		if !baCondsOK(a.Conditions, c, now) {
			continue
		}
		why := baWhy(b, a.Trigger, ev)
		if st.depth >= baMaxDepth {
			run := BoardAutoRun{Board: b.ID, Auto: a.ID, Kind: a.Kind, Name: a.Name, Card: c.ID, CardNo: c.No, Title: clip(c.Title, 120), By: firstOf(by, st.by), Why: why, At: now.Format(time.RFC3339),
				Did: []string{}, Status: "stopped", Error: fmt.Sprintf("stopped here: rules set each other off %d times in a row; check these rules for a loop", baMaxDepth)}
			o.baLog(ctx, tenant, &run)
			o.baStats(ctx, tenant, a, run)
			continue
		}
		o.baRun(ctx, tenant, b, a, c.ID, by, why, now, func(r *baRunner) error { return r.onCard(c.ID, a.Actions) })
	}
}

func baWhy(b Board, t *AutoTrigger, ev BoardEvent) string {
	list := func(k string) string {
		if l, ok := boardList(b, ev.Data[k]); ok {
			return l.Name
		}
		return "a list"
	}
	var s string
	switch t.Event {
	case "card_added":
		s = "added to " + list("list")
	case "card_moved_in":
		s = "moved into " + list("to_list")
	case "card_moved_out":
		s = "moved out of " + list("from_list")
	case "label_added":
		s = "the " + baLabelName(b, ev.Data["label"]) + " added"
	case "label_removed":
		s = "the " + baLabelName(b, ev.Data["label"]) + " removed"
	case "member_added":
		s = ev.Data["member"] + " added"
	case "due_set":
		s = "due date set to " + baDueShown(ev.Data["due"])
	case "card_done":
		s = "marked complete"
	case "checklist_done":
		s = "checklist completed"
	case "card_archived":
		s = "archived"
	case "comment_posted":
		s = "comment posted"
	}
	who := ev.Actor
	if strings.HasPrefix(who, "system:") {
		who = "automation"
	}
	return s + " by " + who
}

// ---- scheduled and due-date commands: the boards' minute ----

func baTick(ctx context.Context, o *Ops, tenant string, now time.Time) {
	defer o.baRecover(ctx, tenant, "")
	all, err := docs.List[BoardAuto](ctx, o.Docs, tenant, cBoardAuto, "")
	if err != nil {
		return
	}
	by := map[string][]BoardAuto{}
	var boards []string
	for _, a := range all {
		if a.Deleted || !a.On || (a.Kind != AutoScheduled && a.Kind != AutoDueDate) {
			continue
		}
		if by[a.Board] == nil {
			boards = append(boards, a.Board)
		}
		by[a.Board] = append(by[a.Board], a)
	}
	if len(boards) == 0 {
		return
	}
	set, _ := o.Settings(ctx, tenant)
	now = now.In(ist)
	for _, id := range boards {
		b, _, err := docs.Get[Board](ctx, o.Docs, tenant, cBoard, id)
		if err != nil || b.Deleted || b.Closed || b.Template {
			continue
		}
		var cards []Card
		for _, a := range by[id] {
			switch a.Kind {
			case AutoScheduled:
				o.baScheduled(ctx, tenant, b, a, set, now)
			case AutoDueDate:
				if cards == nil {
					if cards, _ = o.boardCards(ctx, tenant, b.ID); cards == nil {
						cards = []Card{}
					}
				}
				o.baDueCommand(ctx, tenant, b, a, cards, now)
			}
		}
	}
}

// runsOn reports whether the command runs on date, or the holiday it
// skips (a date it would run on but for a trading holiday).
func (w AutoWhen) runsOn(date string, set Settings) (bool, string) {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return false, ""
	}
	wd := t.Weekday()
	weekday := wd >= time.Monday && wd <= time.Friday
	var match bool
	switch w.Every {
	case "day":
		match = true
	case "weekday":
		match = weekday
	case "working_day":
		if !weekday {
			return false, ""
		}
		if h, name := set.IsHoliday(date); h {
			return false, name
		}
		return true, ""
	case "weekdays":
		for _, d := range w.Days {
			match = match || d == int(wd)
		}
	case "month_day":
		last := time.Date(t.Year(), t.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
		match = t.Day() == min(w.Day, last)
	case "first_working", "last_working":
		first := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
		days := first.AddDate(0, 1, -1).Day()
		for i := 0; i < days; i++ {
			d := first.AddDate(0, 0, i)
			if w.Every == "last_working" {
				d = first.AddDate(0, 0, days-1-i)
			}
			if set.IsBusinessDay(d.Format("2006-01-02")) {
				return d.Format("2006-01-02") == date, ""
			}
		}
		return false, ""
	}
	if match && w.SkipHolidays {
		if h, name := set.IsHoliday(date); h {
			return false, name
		}
	}
	return match, ""
}

func (o *Ops) baScheduled(ctx context.Context, tenant string, b Board, a BoardAuto, set Settings, now time.Time) {
	w := a.When
	if w == nil {
		return
	}
	date := now.Format("2006-01-02")
	slot, err := time.ParseInLocation("2006-01-02 15:04", date+" "+w.At, ist)
	if err != nil || now.Before(slot) || !now.Before(slot.Add(baLate)) {
		return
	}
	if since, err := time.Parse(time.RFC3339, a.Since); err == nil && slot.Before(since.Truncate(time.Minute)) {
		return
	}
	key := date + "T" + w.At
	if a.LastSlot >= key {
		return
	}
	runs, holiday := w.runsOn(date, set)
	if !runs && holiday == "" {
		return
	}
	// Claim the slot first: a restart, or a second server, does not run it twice.
	if _, err := docs.Update(ctx, o.Docs, tenant, cBoardAuto, baKey(b.ID, a.ID), AutomationActor, func(x *BoardAuto) error {
		if x.Deleted || !x.On || x.LastSlot >= key {
			return errBANoChange
		}
		x.LastSlot = key
		return nil
	}); err != nil {
		return
	}
	why := "scheduled for " + slot.Format("02 Jan 15:04")
	if holiday != "" {
		run := BoardAutoRun{Board: b.ID, Auto: a.ID, Kind: a.Kind, Name: a.Name, Why: why, At: now.Format(time.RFC3339), Did: []string{},
			Status: "skipped", Error: fmt.Sprintf("%s is a trading holiday (%s)", slot.Format("02 Jan 2006"), holiday)}
		o.baLog(ctx, tenant, &run)
		o.baStats(ctx, tenant, a, run)
		return
	}
	if now.Sub(slot) >= 2*time.Minute {
		why += fmt.Sprintf(", ran %s late (Ledgerline was not running)", now.Sub(slot).Truncate(time.Minute))
	}
	o.baRun(ctx, tenant, b, a, "", "", why, now, func(r *baRunner) error { return r.onBoard(a.Actions) })
}

func (o *Ops) baDueCommand(ctx context.Context, tenant string, b Board, a BoardAuto, cards []Card, now time.Time) {
	d := a.Due
	if d == nil {
		return
	}
	off := time.Duration(d.N) * baUnits[d.Unit]
	if d.When == "before" {
		off = -off
	}
	since, _ := time.Parse(time.RFC3339, a.Since)
	var marks map[string]string
	for _, c := range cards {
		if c.Archived || c.Template || c.Due == "" || (c.DueDone && !d.Done) || (d.List != "" && c.List != d.List) || (d.Label != "" && !contains(c.Labels, d.Label)) {
			continue
		}
		due, ok := boardTime(c.Due)
		if !ok {
			continue
		}
		at := due.Add(off)
		if now.Before(at) || !now.Before(at.Add(baDueWindow)) || (!since.IsZero() && at.Before(since.Truncate(time.Minute))) {
			continue
		}
		if marks == nil {
			marks = map[string]string{}
			ms, _ := docs.List[baDueMark](ctx, o.Docs, tenant, cBoardAutoDue, b.ID+"|"+a.ID+"|")
			for _, m := range ms {
				marks[m.Card] = m.Due
			}
		}
		if marks[c.ID] == c.Due {
			continue
		}
		fresh, _, err := docs.Get[Card](ctx, o.Docs, tenant, cBoardCard, cardKey(b.ID, c.ID))
		if err != nil || fresh.Deleted || fresh.Archived || fresh.Due != c.Due || (fresh.DueDone && !d.Done) || !baCondsOK(a.Conditions, fresh, now) {
			continue
		}
		if !o.baClaimDue(ctx, tenant, b.ID+"|"+a.ID+"|"+c.ID, c.ID, c.Due) {
			continue
		}
		why := fmt.Sprintf("%s %s its due date (%s)", baOffset(d), d.When, baDueShown(c.Due))
		if d.N == 0 {
			why = fmt.Sprintf("at its due date (%s)", baDueShown(c.Due))
		}
		id := c.ID
		o.baRun(ctx, tenant, b, a, id, "", why, now, func(r *baRunner) error { return r.onCard(id, a.Actions) })
	}
}

func baOffset(d *AutoDueAt) string {
	u := strings.TrimSuffix(d.Unit, "s")
	if d.N != 1 {
		u = d.Unit
	}
	return fmt.Sprintf("%d %s", d.N, u)
}

// baClaimDue records that a due-date command ran for a card's due date;
// false if it already had.
func (o *Ops) baClaimDue(ctx context.Context, tenant, key, card, due string) bool {
	_, err := docs.Put(ctx, o.Docs, tenant, cBoardAutoDue, key, baDueMark{Card: card, Due: due}, 0, AutomationActor)
	if err == nil {
		return true
	}
	if !errors.Is(err, docs.ErrStale) {
		return false
	}
	_, err = docs.Update(ctx, o.Docs, tenant, cBoardAutoDue, key, AutomationActor, func(m *baDueMark) error {
		if m.Due == due {
			return errBANoChange
		}
		m.Due = due
		return nil
	})
	return err == nil
}
