package ops

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"ledgerline/internal/docs"
)

// ---- The card repeater (Trello's Card Repeater Power-Up) ----
//
// A card on a team board can repeat: every day, weekday or working day
// (Monday to Friday except the firm's trading holidays, as scheduled
// commands use them), weekly on chosen days, monthly (on day N, or the
// first or last working day), quarterly or yearly, at a time (IST). Each
// time, automation adds a copy of the card to a list of the same board
// (the card's own list unless another is chosen): checklists unticked,
// the same labels, members and custom fields, and a due date N days (or
// working days) after it is made if asked. The copy does not repeat.
//
// A repeat can end after N copies or on a date. It stops when the card is
// archived, deleted or moved to another board, and skips its times while
// the board is closed (it carries on when the board is reopened). It runs
// from the boards' minute, claims each time before copying (so a restart,
// or a second server, never makes two copies) and catches up at most one
// time missed while Ledgerline was not running. Cards on Ledgerline
// boards, and cards linked to Ledgerline records, do not repeat: their
// records decide what they are.

const cBoardRepeat = "boardrepeat" // "<board>|<card>"

// rpLookAhead is how far ahead the next time is looked for.
const rpLookAhead = 400

func init() {
	boardTickHooks = append(boardTickHooks, rpTick)
	OnBoardEvent(rpOnEvent)
}

// CardRepeat is a card's repeat.
type CardRepeat struct {
	Board string `json:"board"`
	Card  string `json:"card"`
	// Every: day, weekday, working_day, weekly (Days), monthly, quarterly,
	// yearly (Which: day N, first_working or last_working; Month for
	// yearly, 1–12, and for quarterly the month of the quarter, 1–3).
	Every       string `json:"every"`
	Days        []int  `json:"days,omitempty"` // weekly: 0 Sunday … 6 Saturday
	Which       string `json:"which,omitempty"`
	Day         int    `json:"day,omitempty"` // 1–31 (the month's last day when it is shorter)
	Month       int    `json:"month,omitempty"`
	At          string `json:"at"`             // HH:MM IST
	List        string `json:"list,omitempty"` // where copies go ("" the card's own list)
	Due         bool   `json:"due,omitempty"`  // give copies a due date
	DueDays     int    `json:"due_days,omitempty"`
	DueTime     string `json:"due_time,omitempty"` // HH:MM ("" a date alone)
	WorkingDays bool   `json:"working_days,omitempty"`
	EndAfter    int    `json:"end_after,omitempty"` // stop after so many copies (0: no end)
	EndOn       string `json:"end_on,omitempty"`    // YYYY-MM-DD: no copies after this day
	Made        int    `json:"made"`
	Next        string `json:"next,omitempty"`      // the next time, RFC3339 (IST)
	LastSlot    string `json:"last_slot,omitempty"` // YYYY-MM-DDTHH:MM it last ran for
	LastRun     string `json:"last_run,omitempty"`
	LastCard    string `json:"last_card,omitempty"`
	LastNo      int64  `json:"last_no,omitempty"`
	LastStatus  string `json:"last_status,omitempty"` // done, failed, skipped
	LastError   string `json:"last_error,omitempty"`
	Ended       string `json:"ended,omitempty"` // why it stopped ("" it runs)
	CreatedBy   string `json:"created_by"`
	CreatedAt   string `json:"created_at"`
	UpdatedBy   string `json:"updated_by,omitempty"`
	UpdatedAt   string `json:"updated_at"`
	Deleted     bool   `json:"deleted,omitempty"`
}

// CardRepeatView is a repeat with its sentence and next times, for the console.
type CardRepeatView struct {
	CardRepeat
	Text     string   `json:"text"`     // "Repeats every month on the last working day"
	Where    string   `json:"where"`    // "Copies go to Monthly, due 5 days after they are made"
	Upcoming []string `json:"upcoming"` // the next times (RFC3339), at most 3
}

var rpMonths = []string{"January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"}

func rpKey(board, card string) string { return board + "|" + card }

// when is the repeat as a scheduled command's schedule, for one month.
func (r CardRepeat) when() AutoWhen {
	switch r.Every {
	case "day", "weekday", "working_day":
		return AutoWhen{Every: r.Every, At: r.At}
	case "weekly":
		return AutoWhen{Every: "weekdays", Days: r.Days, At: r.At}
	}
	switch r.Which {
	case "first_working", "last_working":
		return AutoWhen{Every: r.Which, At: r.At}
	}
	return AutoWhen{Every: "month_day", Day: r.Day, At: r.At}
}

// runsOn reports whether the repeat makes a copy on date (YYYY-MM-DD).
func (r CardRepeat) runsOn(date string, set Settings) bool {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return false
	}
	switch r.Every {
	case "quarterly":
		if (int(t.Month())-1)%3 != r.Month-1 {
			return false
		}
	case "yearly":
		if int(t.Month()) != r.Month {
			return false
		}
	}
	ok, _ := r.when().runsOn(date, set)
	return ok
}

// after is the repeat's first time after t (false: none within a year or so).
func (r CardRepeat) after(t time.Time, set Settings) (time.Time, bool) {
	t = t.In(ist)
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, ist)
	for i := 0; i < rpLookAhead; i++ {
		d := day.AddDate(0, 0, i)
		date := d.Format("2006-01-02")
		if !r.runsOn(date, set) {
			continue
		}
		s, err := time.ParseInLocation("2006-01-02 15:04", date+" "+r.At, ist)
		if err == nil && s.After(t) {
			return s, true
		}
	}
	return time.Time{}, false
}

// upcoming are the next n times from t, before the repeat ends.
func (r CardRepeat) upcoming(t time.Time, set Settings, n int) []time.Time {
	var out []time.Time
	made := r.Made
	for len(out) < n {
		s, ok := r.after(t, set)
		if !ok || (r.EndOn != "" && s.Format("2006-01-02") > r.EndOn) || (r.EndAfter > 0 && made >= r.EndAfter) {
			break
		}
		out = append(out, s)
		t, made = s, made+1
	}
	return out
}

func rpOrdinal(n int) string {
	switch {
	case n%100 >= 11 && n%100 <= 13:
		return fmt.Sprintf("%dth", n)
	case n%10 == 1:
		return fmt.Sprintf("%dst", n)
	case n%10 == 2:
		return fmt.Sprintf("%dnd", n)
	case n%10 == 3:
		return fmt.Sprintf("%drd", n)
	}
	return fmt.Sprintf("%dth", n)
}

// Sentence says how often the card repeats.
func (r CardRepeat) Sentence() string {
	which := func(of string) string {
		switch r.Which {
		case "first_working":
			return "on the first working day" + of
		case "last_working":
			return "on the last working day" + of
		}
		return fmt.Sprintf("on day %d", r.Day) + of
	}
	switch r.Every {
	case "day":
		return "Repeats every day"
	case "weekday":
		return "Repeats every weekday (Monday to Friday)"
	case "working_day":
		return "Repeats every working day"
	case "weekly":
		var ds []string
		for _, d := range []int{1, 2, 3, 4, 5, 6, 0} {
			if contains(intsAsStrings(r.Days), fmt.Sprint(d)) {
				ds = append(ds, time.Weekday(d).String())
			}
		}
		return "Repeats every week on " + rpAnd(ds)
	case "monthly":
		return "Repeats every month " + which("")
	case "quarterly":
		m := []string{"first", "second", "third"}[min(max(r.Month, 1), 3)-1]
		return "Repeats every quarter " + which(" of its "+m+" month")
	case "yearly":
		mn := rpMonths[min(max(r.Month, 1), 12)-1]
		if r.Which == "day" || r.Which == "" {
			return fmt.Sprintf("Repeats every year on %d %s", r.Day, mn)
		}
		return "Repeats every year " + which(" of "+mn)
	}
	return "Repeats"
}

func intsAsStrings(xs []int) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = fmt.Sprint(x)
	}
	return out
}

func rpAnd(xs []string) string {
	switch len(xs) {
	case 0:
		return "…"
	case 1:
		return xs[0]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}

func (o *Ops) rpView(b Board, r CardRepeat, set Settings) CardRepeatView {
	v := CardRepeatView{CardRepeat: r, Text: r.Sentence(), Upcoming: []string{}}
	where := "Copies go to this card’s list"
	if r.List != "" {
		if l, ok := boardList(b, r.List); ok {
			where = "Copies go to " + l.Name
		} else {
			where = "Copies go to a list that no longer exists"
		}
	}
	if r.Due {
		unit := "day"
		if r.WorkingDays {
			unit = "working day"
		}
		if r.DueDays == 0 {
			where += ", due the day they are made"
		} else {
			where += fmt.Sprintf(", due %d %s%s after they are made", r.DueDays, unit, plural(r.DueDays))
		}
		if r.DueTime != "" {
			where += " at " + r.DueTime
		}
	}
	switch {
	case r.EndAfter > 0:
		where += fmt.Sprintf(" · ends after %d cop%s", r.EndAfter, map[bool]string{true: "y", false: "ies"}[r.EndAfter == 1])
	case r.EndOn != "":
		if t, err := time.Parse("2006-01-02", r.EndOn); err == nil {
			where += " · ends on " + t.Format("2 Jan 2006")
		}
	}
	v.Where = where
	if r.Ended == "" {
		from := o.now()
		for _, t := range r.upcoming(from, set, 3) {
			v.Upcoming = append(v.Upcoming, t.Format(time.RFC3339))
		}
	}
	return v
}

// BoardRepeats are the repeats of a board's cards (everyone who can see it).
func (o *Ops) BoardRepeats(ctx context.Context, tenant, actor, board string) ([]CardRepeatView, error) {
	b, _, err := o.boardFor(ctx, tenant, actor, board)
	if err != nil {
		return nil, err
	}
	rs, err := docs.List[CardRepeat](ctx, o.Docs, tenant, cBoardRepeat, board+"|")
	if err != nil {
		return nil, err
	}
	set, _ := o.Settings(ctx, tenant)
	out := []CardRepeatView{}
	for _, r := range rs {
		if !r.Deleted {
			out = append(out, o.rpView(b, r, set))
		}
	}
	return out, nil
}

// rpCheck checks a repeat as a person saves it, and works out its next time.
func (o *Ops) rpCheck(ctx context.Context, tenant, actor string, b Board, c Card, in *CardRepeat, set Settings) error {
	if b.Kind == BoardLedgerline {
		return invalid("cards on a Ledgerline board follow their records, so they do not repeat")
	}
	if b.Template {
		return invalid("cards on a template board do not repeat; make a board from the template first")
	}
	if b.Kind == BoardInbox {
		return invalid("cards in your Inbox do not repeat; move the card to a board first")
	}
	if c.Link != nil {
		return invalid("#%d is linked to %s: a card that follows a Ledgerline record does not repeat", c.No, c.Link.Label)
	}
	if c.Mirror != nil {
		return invalid("#%d is a mirror of a card on another board: set the repeat on the original", c.No)
	}
	if c.Archived {
		return invalid("#%d is archived; send it back to the board first", c.No)
	}
	in.At = strings.TrimSpace(in.At)
	if !baHHMM.MatchString(in.At) {
		return invalid("give the time as HH:MM (IST), e.g. 10:00")
	}
	switch in.Every {
	case "day", "weekday", "working_day":
		in.Days, in.Which, in.Day, in.Month = nil, "", 0, 0
	case "weekly":
		seen := map[int]bool{}
		var ds []int
		for _, d := range in.Days {
			if d < 0 || d > 6 {
				return invalid("weekdays are 0 (Sunday) to 6 (Saturday)")
			}
			if !seen[d] {
				seen[d] = true
				ds = append(ds, d)
			}
		}
		if len(ds) == 0 {
			return invalid("choose the days of the week")
		}
		in.Days, in.Which, in.Day, in.Month = ds, "", 0, 0
	case "monthly", "quarterly", "yearly":
		in.Days = nil
		switch in.Which {
		case "", "day":
			in.Which = "day"
			if in.Day < 1 || in.Day > 31 {
				return invalid("the day of the month is 1 to 31")
			}
		case "first_working", "last_working":
			in.Day = 0
		default:
			return invalid("choose a day of the month, or its first or last working day")
		}
		switch in.Every {
		case "monthly":
			in.Month = 0
		case "quarterly":
			if in.Month < 1 || in.Month > 3 {
				return invalid("choose the first, second or third month of each quarter")
			}
		case "yearly":
			if in.Month < 1 || in.Month > 12 {
				return invalid("choose the month")
			}
		}
	default:
		return invalid("a card repeats every day, weekday, working day, week, month, quarter or year")
	}
	if in.List != "" {
		if l, ok := boardList(b, in.List); !ok || l.Archived {
			return invalid("choose an open list for the copies")
		}
	}
	if in.Due {
		if in.DueDays < 0 || in.DueDays > 365 {
			return invalid("copies are due 0 to 365 days after they are made")
		}
		if in.DueTime = strings.TrimSpace(in.DueTime); in.DueTime != "" && !baHHMM.MatchString(in.DueTime) {
			return invalid("give the due time as HH:MM, or leave it empty for the date alone")
		}
	} else {
		in.DueDays, in.DueTime, in.WorkingDays = 0, "", false
	}
	if in.EndAfter < 0 || in.EndAfter > 1000 {
		return invalid("a repeat ends after at most 1,000 copies")
	}
	if in.EndOn = strings.TrimSpace(in.EndOn); in.EndOn != "" {
		if in.EndAfter > 0 {
			return invalid("a repeat ends after a number of copies or on a date, not both")
		}
		if _, err := time.Parse("2006-01-02", in.EndOn); err != nil {
			return invalid("give the end date as YYYY-MM-DD")
		}
		if in.EndOn < o.now().In(ist).Format("2006-01-02") {
			return invalid("the end date has passed")
		}
	}
	return nil
}

// SaveCardRepeat sets or changes a card's repeat (board admins and members).
func (o *Ops) SaveCardRepeat(ctx context.Context, tenant, actor, board, card string, in CardRepeat) (CardRepeatView, error) {
	b, _, err := o.boardFor(ctx, tenant, actor, board, BoardAdmin, BoardMember)
	if err != nil {
		return CardRepeatView{}, err
	}
	c, _, err := o.GetCard(ctx, tenant, actor, board, card)
	if err != nil {
		return CardRepeatView{}, err
	}
	set, _ := o.Settings(ctx, tenant)
	if err := o.rpCheck(ctx, tenant, actor, b, c, &in, set); err != nil {
		return CardRepeatView{}, err
	}
	now := o.now()
	key := rpKey(board, card)
	old, _, gerr := docs.Get[CardRepeat](ctx, o.Docs, tenant, cBoardRepeat, key)
	fresh := gerr != nil || old.Deleted || old.Ended != ""
	in.Made = 0
	if !fresh {
		in.Made = old.Made // the count goes on when a running repeat is changed
	}
	ups := in.upcoming(now, set, 1)
	if len(ups) == 0 {
		if in.EndAfter > 0 && in.Made >= in.EndAfter {
			return CardRepeatView{}, invalid("it has already made %d copies; end it later, or not at all", in.Made)
		}
		return CardRepeatView{}, invalid("with these settings it would not make a copy within the next year")
	}
	stamp := now.Format(time.RFC3339)
	if errors.Is(gerr, docs.ErrNotFound) {
		if _, err := docs.Put(ctx, o.Docs, tenant, cBoardRepeat, key, CardRepeat{Board: board, Card: card, CreatedBy: actor, CreatedAt: stamp, Deleted: true}, 0, actor); err != nil && !errors.Is(err, docs.ErrStale) {
			return CardRepeatView{}, err
		}
	}
	r, err := docs.Update(ctx, o.Docs, tenant, cBoardRepeat, key, actor, func(x *CardRepeat) error {
		keep := *x
		fresh = keep.Deleted || keep.Ended != ""
		*x = in
		x.Board, x.Card, x.Deleted, x.Ended = board, card, false, ""
		x.LastSlot = keep.LastSlot // a time already run for is never run again
		if fresh {
			x.Made, x.CreatedBy, x.CreatedAt = 0, actor, stamp
			x.LastRun, x.LastCard, x.LastNo, x.LastStatus, x.LastError = "", "", 0, "", ""
		} else {
			x.Made, x.CreatedBy, x.CreatedAt = keep.Made, keep.CreatedBy, keep.CreatedAt
			x.LastRun, x.LastCard, x.LastNo, x.LastStatus, x.LastError = keep.LastRun, keep.LastCard, keep.LastNo, keep.LastStatus, keep.LastError
		}
		x.Next, x.UpdatedBy, x.UpdatedAt = ups[0].Format(time.RFC3339), actor, stamp
		return nil
	})
	if err != nil {
		return CardRepeatView{}, err
	}
	v := o.rpView(b, r, set)
	what := "set #%d to repeat: %s"
	if !fresh {
		what = "changed the repeat of #%d: %s"
	}
	o.boardLog(ctx, tenant, actor, b, card, "card.repeat_set", fmt.Sprintf(what, c.No, strings.ToLower(v.Text[:1])+v.Text[1:]+" at "+r.At), nil)
	return v, nil
}

// PreviewCardRepeat says how a repeat would read and when it would make
// its next copies, without saving it.
func (o *Ops) PreviewCardRepeat(ctx context.Context, tenant, actor, board, card string, in CardRepeat) (CardRepeatView, error) {
	b, _, err := o.boardFor(ctx, tenant, actor, board)
	if err != nil {
		return CardRepeatView{}, err
	}
	c, _, err := o.GetCard(ctx, tenant, actor, board, card)
	if err != nil {
		return CardRepeatView{}, err
	}
	set, _ := o.Settings(ctx, tenant)
	if err := o.rpCheck(ctx, tenant, actor, b, c, &in, set); err != nil {
		return CardRepeatView{}, err
	}
	in.Board, in.Card = board, card
	if old, _, err := docs.Get[CardRepeat](ctx, o.Docs, tenant, cBoardRepeat, rpKey(board, card)); err == nil && !old.Deleted && old.Ended == "" {
		in.Made = old.Made
	}
	return o.rpView(b, in, set), nil
}

// StopCardRepeat stops a card's repeat (board admins and members). The
// copies already made stay.
func (o *Ops) StopCardRepeat(ctx context.Context, tenant, actor, board, card string) error {
	b, _, err := o.boardFor(ctx, tenant, actor, board, BoardAdmin, BoardMember)
	if err != nil {
		return err
	}
	r, err := docs.Update(ctx, o.Docs, tenant, cBoardRepeat, rpKey(board, card), actor, func(x *CardRepeat) error {
		if x.Deleted {
			return fmt.Errorf("%w: the card does not repeat", ErrNotFound)
		}
		x.Deleted, x.UpdatedBy, x.UpdatedAt = true, actor, o.now().Format(time.RFC3339)
		return nil
	})
	if errors.Is(err, docs.ErrNotFound) {
		return fmt.Errorf("%w: the card does not repeat", ErrNotFound)
	}
	if err != nil {
		return err
	}
	no := ""
	if c, _, err := docs.Get[Card](ctx, o.Docs, tenant, cBoardCard, cardKey(board, card)); err == nil {
		no = fmt.Sprintf(" #%d", c.No)
	}
	o.boardLog(ctx, tenant, actor, b, card, "card.repeat_stopped", fmt.Sprintf("stopped%s repeating (%d cop%s made)", no, r.Made, map[bool]string{true: "y", false: "ies"}[r.Made == 1]), nil)
	return nil
}

// rpEnd stops a repeat for a reason (the card was archived …).
func (o *Ops) rpEnd(ctx context.Context, tenant string, r CardRepeat, why string) {
	_, _ = docs.Update(ctx, o.Docs, tenant, cBoardRepeat, rpKey(r.Board, r.Card), AutomationActor, func(x *CardRepeat) error {
		if x.Deleted || x.Ended != "" {
			return errBANoChange
		}
		x.Ended, x.UpdatedAt = why, o.now().Format(time.RFC3339)
		return nil
	})
}

// rpOnEvent stops a card's repeat when the card is archived.
func rpOnEvent(ctx context.Context, o *Ops, tenant string, ev BoardEvent) {
	if ev.Kind != "card.archived" || ev.Card == "" {
		return
	}
	r, _, err := docs.Get[CardRepeat](ctx, o.Docs, tenant, cBoardRepeat, rpKey(ev.Board, ev.Card))
	if err != nil || r.Deleted || r.Ended != "" {
		return
	}
	o.rpEnd(ctx, tenant, r, "the card was archived")
}

// ---- the boards' minute ----

func rpTick(ctx context.Context, o *Ops, tenant string, now time.Time) {
	defer o.baRecover(ctx, tenant, "")
	rs, err := docs.List[CardRepeat](ctx, o.Docs, tenant, cBoardRepeat, "")
	if err != nil {
		return
	}
	var set *Settings
	now = now.In(ist)
	for _, r := range rs {
		if r.Deleted || r.Ended != "" {
			continue
		}
		next, ok := bnTime(r.Next)
		if !ok || now.Before(next) {
			continue
		}
		if set == nil {
			s, _ := o.Settings(ctx, tenant)
			set = &s
		}
		o.rpRun(ctx, tenant, r, next, *set, now)
	}
}

// rpRun makes the copy that is due (once, for the latest time missed).
func (o *Ops) rpRun(ctx context.Context, tenant string, r CardRepeat, next time.Time, set Settings, now time.Time) {
	b, _, err := docs.Get[Board](ctx, o.Docs, tenant, cBoard, r.Board)
	if err != nil || b.Deleted {
		o.rpEnd(ctx, tenant, r, "the board was deleted")
		return
	}
	src, _, err := docs.Get[Card](ctx, o.Docs, tenant, cBoardCard, cardKey(r.Board, r.Card))
	switch {
	case err != nil || (src.Deleted && src.MovedTo == ""):
		o.rpEnd(ctx, tenant, r, "the card was deleted")
		return
	case src.Deleted:
		o.rpEnd(ctx, tenant, r, "the card moved to another board")
		return
	case src.Archived:
		o.rpEnd(ctx, tenant, r, "the card was archived")
		return
	case b.Kind == BoardLedgerline || src.Link != nil:
		o.rpEnd(ctx, tenant, r, "the card follows a Ledgerline record")
		return
	}
	// The latest time due: from the stored next one (holidays may have
	// changed since it was worked out) up to now.
	slot, ok := r.after(next.Add(-time.Minute), set)
	if !ok || slot.After(now) {
		if ok {
			o.rpSetNext(ctx, tenant, r, slot)
		}
		return
	}
	for i := 0; i < rpLookAhead; i++ {
		s, ok := r.after(slot, set)
		if !ok || s.After(now) {
			break
		}
		slot = s
	}
	newNext, more := r.after(now, set)
	key := slot.Format("2006-01-02T15:04")
	endDay := r.EndOn != "" && slot.Format("2006-01-02") > r.EndOn
	status := "done"
	switch {
	case endDay:
		o.rpEnd(ctx, tenant, r, "it ended on "+r.EndOn)
		return
	case b.Closed || b.Template:
		status = "skipped"
	}
	// Claim the time first: a restart, or a second server, does not copy twice.
	if _, err := docs.Update(ctx, o.Docs, tenant, cBoardRepeat, rpKey(r.Board, r.Card), AutomationActor, func(x *CardRepeat) error {
		if x.Deleted || x.Ended != "" || x.LastSlot >= key || x.Next != r.Next {
			return errBANoChange
		}
		x.LastSlot, x.Next = key, ""
		if more {
			x.Next = newNext.Format(time.RFC3339)
		}
		x.LastRun, x.LastStatus, x.LastError = now.Format(time.RFC3339), status, ""
		if status == "skipped" {
			x.LastError = "the board is closed"
		}
		return nil
	}); err != nil {
		return
	}
	if status == "skipped" {
		return
	}
	nc, err := o.rpCopy(ctx, tenant, b, src, r, set, now)
	late := ""
	if now.Sub(slot) >= 2*time.Minute {
		late = fmt.Sprintf(", %s late: Ledgerline was not running at %s", now.Sub(slot).Truncate(time.Minute), slot.Format("02 Jan 15:04"))
	}
	_, _ = docs.Update(ctx, o.Docs, tenant, cBoardRepeat, rpKey(r.Board, r.Card), AutomationActor, func(x *CardRepeat) error {
		if err != nil {
			x.LastStatus, x.LastError = "failed", baErrText(err)
			return nil
		}
		x.Made++
		x.LastCard, x.LastNo = nc.ID, nc.No
		if x.EndAfter > 0 && x.Made >= x.EndAfter {
			x.Ended = fmt.Sprintf("it made the %d cop%s it was set to make", x.EndAfter, map[bool]string{true: "y", false: "ies"}[x.EndAfter == 1])
		} else if !more || (x.EndOn != "" && newNext.Format("2006-01-02") > x.EndOn) {
			x.Ended = "it has no more times to run"
		}
		return nil
	})
	if err != nil {
		o.boardLog(ctx, tenant, AutomationActor, b, src.ID, "card.repeat_failed", clip(fmt.Sprintf("the card repeater could not copy #%d: %s", src.No, baErrText(err)), 600), nil)
		return
	}
	o.boardLog(ctx, tenant, AutomationActor, b, src.ID, "card.repeated", fmt.Sprintf("the card repeater made #%d from #%d (%s)%s", nc.No, src.No, strings.ToLower(r.Sentence()[:1])+r.Sentence()[1:], late), map[string]string{"copy": nc.ID})
}

func (o *Ops) rpSetNext(ctx context.Context, tenant string, r CardRepeat, next time.Time) {
	_, _ = docs.Update(ctx, o.Docs, tenant, cBoardRepeat, rpKey(r.Board, r.Card), AutomationActor, func(x *CardRepeat) error {
		if x.Deleted || x.Next != r.Next {
			return errBANoChange
		}
		x.Next = next.Format(time.RFC3339)
		return nil
	})
}

// rpCopy adds the copy: checklists unticked (their items' own dates
// dropped), the same labels, members and custom fields, a due date when
// asked. Attachments, comments and image covers stay on the original.
func (o *Ops) rpCopy(ctx context.Context, tenant string, b Board, src Card, r CardRepeat, set Settings, now time.Time) (Card, error) {
	list := firstOf(r.List, src.List)
	if l, ok := boardList(b, list); !ok || l.Archived {
		return Card{}, invalid("the list for the copies is archived or gone; edit the repeat")
	}
	nc := Card{Title: src.Title, Description: src.Description, Location: src.Location, Fields: map[string]string{}}
	for _, l := range src.Labels {
		if _, ok := boardLabel(b, l); ok {
			nc.Labels = append(nc.Labels, l)
		}
	}
	us, _ := o.Users(ctx, tenant)
	for _, m := range src.Members {
		ok := len(us) == 0
		for _, u := range us {
			ok = ok || (u.ID == m && u.Active && u.Role != RoleClient && boardRole(b, u) != "")
		}
		if ok {
			nc.Members = append(nc.Members, m)
		}
	}
	for id, v := range src.Fields {
		if _, ok := boardField(b, id); ok {
			nc.Fields[id] = v
		}
	}
	nc.Checklists = copyChecklists(src.Checklists, true)
	for i := range nc.Checklists {
		for j := range nc.Checklists[i].Items {
			nc.Checklists[i].Items[j].Due = ""
		}
	}
	if src.Cover != nil && src.Cover.Color != "" && src.Cover.File == "" {
		nc.Cover = &CardCover{Color: src.Cover.Color, Full: src.Cover.Full}
	}
	if r.Due {
		d := now.In(ist).Format("2006-01-02")
		if r.WorkingDays {
			for i := 0; i < r.DueDays; i++ {
				d = set.NextBusinessDay(d)
			}
		} else {
			t, _ := time.ParseInLocation("2006-01-02", d, ist)
			d = t.AddDate(0, 0, r.DueDays).Format("2006-01-02")
		}
		if r.DueTime != "" {
			d += "T" + r.DueTime
		}
		nc.Due = d
	}
	return o.addCard(ctx, tenant, AutomationActor, b, list, nc, math.NaN())
}
