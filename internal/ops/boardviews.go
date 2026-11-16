package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
)

// ---- Boards: views ----
//
// The board's own views (table, calendar, timeline, dashboard) work in
// the console from the board's cards (web/boardviews.js). The server adds:
//
//   - My cards: the cards a person is a member of, on every board they
//     can see, by when they fall due (the boards home).
//   - The dashboard's tiles, kept on the board (Settings["views.tiles"]),
//     and "cards over time": cards added and completed each week, from
//     the board's activity.
//   - All boards: one table and calendar of the cards on every board a
//     person can see, filtered and paged (CrossCards).

// MyCard is a card the person is a member of.
type MyCard struct {
	Board     string       `json:"board"`
	BoardName string       `json:"board_name"`
	Card      string       `json:"card"`
	No        int64        `json:"no"`
	Title     string       `json:"title"`
	List      string       `json:"list"`
	ListName  string       `json:"list_name"`
	Labels    []BoardLabel `json:"labels,omitempty"`
	Start     string       `json:"start,omitempty"`
	Due       string       `json:"due,omitempty"`
	Items     int          `json:"items,omitempty"` // checklist items
	ItemsDone int          `json:"items_done,omitempty"`
	When      string       `json:"when"` // overdue, today, week, later or none
	UpdatedAt string       `json:"updated_at"`
}

// MyCardsView is a person's open cards across boards. Cards marked
// complete are counted, not listed.
type MyCardsView struct {
	Today  string         `json:"today"`   // IST
	WeekTo string         `json:"week_to"` // the Sunday that ends this week
	Cards  []MyCard       `json:"cards"`
	Counts map[string]int `json:"counts"` // by When
	Done   int            `json:"done"`
	More   bool           `json:"more,omitempty"` // more than the cards listed
}

const myCardsMax = 500

// myCardWhen says when a card falls due: overdue (its due time has passed),
// today, this week (to Sunday), later, or none (no due date).
func myCardWhen(due string, now time.Time) string {
	t, ok := boardTime(due)
	if !ok {
		return "none"
	}
	now = now.In(ist)
	today := now.Format("2006-01-02")
	day := t.In(ist).Format("2006-01-02")
	switch {
	case t.Before(now):
		return "overdue"
	case day == today:
		return "today"
	case day <= myWeekEnd(now):
		return "week"
	}
	return "later"
}

// myWeekEnd is the Sunday that ends the week (Monday to Sunday) holding now.
func myWeekEnd(now time.Time) string {
	now = now.In(ist)
	return now.AddDate(0, 0, (7-int(now.Weekday()))%7).Format("2006-01-02")
}

// MyCards lists the open cards a person is a member of, on the boards they
// can see (not closed boards or templates), soonest due first.
func (o *Ops) MyCards(ctx context.Context, tenant, actor string) (MyCardsView, error) {
	bs, err := o.Boards(ctx, tenant, actor)
	if err != nil {
		return MyCardsView{}, err
	}
	now := o.now()
	out := MyCardsView{Today: now.Format("2006-01-02"), WeekTo: myWeekEnd(now), Cards: []MyCard{}, Counts: map[string]int{"overdue": 0, "today": 0, "week": 0, "later": 0, "none": 0}}
	for _, s := range bs {
		if s.Closed || s.Template {
			continue
		}
		b, _, err := docs.Get[Board](ctx, o.Docs, tenant, cBoard, s.ID)
		if err != nil {
			continue
		}
		cs, err := o.boardCards(ctx, tenant, s.ID)
		if err != nil {
			return MyCardsView{}, err
		}
		for _, c := range cs {
			if c.Archived || c.Template || c.Mirror != nil || !contains(c.Members, actor) {
				continue
			}
			l, ok := boardList(b, c.List)
			if !ok || l.Archived {
				continue
			}
			if c.DueDone {
				out.Done++
				continue
			}
			m := MyCard{Board: b.ID, BoardName: b.Name, Card: c.ID, No: c.No, Title: c.Title, List: l.ID, ListName: l.Name,
				Start: c.Start, Due: c.Due, When: myCardWhen(c.Due, now), UpdatedAt: c.UpdatedAt}
			for _, id := range c.Labels {
				if lb, ok := boardLabel(b, id); ok {
					m.Labels = append(m.Labels, lb)
				}
			}
			for _, cl := range c.Checklists {
				for _, it := range cl.Items {
					m.Items++
					if it.Done {
						m.ItemsDone++
					}
				}
			}
			out.Counts[m.When]++
			out.Cards = append(out.Cards, m)
		}
	}
	at := func(c MyCard) time.Time {
		t, ok := boardTime(c.Due)
		if !ok {
			return time.Date(9999, 1, 1, 0, 0, 0, 0, ist)
		}
		return t
	}
	sort.SliceStable(out.Cards, func(i, j int) bool {
		a, b := out.Cards[i], out.Cards[j]
		if ta, tb := at(a), at(b); !ta.Equal(tb) {
			return ta.Before(tb)
		}
		if a.BoardName != b.BoardName {
			return a.BoardName < b.BoardName
		}
		return a.No < b.No
	})
	if len(out.Cards) > myCardsMax {
		out.Cards, out.More = out.Cards[:myCardsMax], true
	}
	return out, nil
}

// ---- dashboard tiles ----

// DashTile is one tile of a board's dashboard: what it counts and how it
// is drawn.
type DashTile struct {
	ID    string `json:"id"`
	Data  string `json:"data"`  // list, label, member, due, checks or time (cards over time)
	Chart string `json:"chart"` // bar or pie; line for cards over time
}

const (
	dashTilesKey = "views.tiles"
	dashMaxTiles = 12
)

// dashCharts are the charts each kind of tile can be drawn as; the first
// is the default.
var dashCharts = map[string][]string{
	"list": {"bar", "pie"}, "label": {"bar", "pie"}, "member": {"bar", "pie"}, "due": {"bar", "pie"}, "checks": {"bar", "pie"}, "time": {"line"},
}

var dashIDRe = regexp.MustCompile(`^[a-z0-9-]{1,24}$`)

// DefaultDashTiles are the tiles a board's dashboard starts with.
func DefaultDashTiles() []DashTile {
	return []DashTile{{"list", "list", "bar"}, {"due", "due", "bar"}, {"label", "label", "bar"}, {"member", "member", "bar"}, {"checks", "checks", "bar"}}
}

// dashTilesCheck checks tiles as a person set them: at most 12, each a
// known kind drawn a way it can be. A missing or repeated id gets a new one.
func dashTilesCheck(ts []DashTile) ([]DashTile, error) {
	if len(ts) > dashMaxTiles {
		return nil, invalid("a dashboard can have at most %d tiles", dashMaxTiles)
	}
	seen := map[string]bool{}
	out := make([]DashTile, 0, len(ts))
	for _, t := range ts {
		t.Data, t.Chart, t.ID = strings.TrimSpace(t.Data), strings.TrimSpace(t.Chart), strings.TrimSpace(t.ID)
		charts, ok := dashCharts[t.Data]
		if !ok {
			return nil, invalid("a tile shows cards per list, label or member, by due status, checklist completion or cards over time")
		}
		if t.Chart == "" {
			t.Chart = charts[0]
		}
		if !contains(charts, t.Chart) {
			return nil, invalid("that tile can be a %s chart", strings.Join(charts, " or "))
		}
		for t.ID == "" || !dashIDRe.MatchString(t.ID) || seen[t.ID] {
			t.ID = boardID("t", 3)
		}
		seen[t.ID] = true
		out = append(out, t)
	}
	return out, nil
}

// DashTilesOf is a board's dashboard tiles (the default set when none
// were saved, or what was saved no longer reads).
func DashTilesOf(b Board) []DashTile {
	raw := b.Settings[dashTilesKey]
	if raw == "" {
		return DefaultDashTiles()
	}
	var ts []DashTile
	if json.Unmarshal([]byte(raw), &ts) != nil {
		return DefaultDashTiles()
	}
	ts, err := dashTilesCheck(ts)
	if err != nil {
		return DefaultDashTiles()
	}
	return ts
}

// SetDashTiles saves a board's dashboard tiles (board admins and members);
// reset goes back to the default set.
func (o *Ops) SetDashTiles(ctx context.Context, tenant, actor, board string, ts []DashTile, reset bool) ([]DashTile, error) {
	b, _, err := o.boardFor(ctx, tenant, actor, board, BoardAdmin, BoardMember)
	if err != nil {
		return nil, err
	}
	raw := ""
	if !reset {
		if ts == nil {
			ts = []DashTile{}
		}
		if ts, err = dashTilesCheck(ts); err != nil {
			return nil, err
		}
		j, _ := json.Marshal(ts)
		raw = string(j)
	}
	b, err = docs.Update(ctx, o.Docs, tenant, cBoard, board, actor, func(x *Board) error {
		if x.Settings == nil {
			x.Settings = map[string]string{}
		}
		if reset {
			delete(x.Settings, dashTilesKey)
		} else {
			x.Settings[dashTilesKey] = raw
		}
		x.UpdatedAt = o.now().Format(time.RFC3339)
		return nil
	})
	if err != nil {
		return nil, err
	}
	o.boardLog(ctx, tenant, actor, b, "", "board.updated", "changed the dashboard", nil)
	return DashTilesOf(b), nil
}

// ---- cards over time ----

// OverTimeWeek is one week (Monday to Sunday, IST) of a board's cards.
type OverTimeWeek struct {
	Week  string `json:"week"`  // its Monday
	Added int    `json:"added"` // cards added (or moved in from another board)
	Done  int    `json:"done"`  // cards marked complete
}

// OverTimeView is the last twelve weeks of a board, this one last.
type OverTimeView struct {
	Weeks  []OverTimeWeek `json:"weeks"`
	Read   int            `json:"read"`             // activity entries read
	Capped bool           `json:"capped,omitempty"` // stopped reading before the twelve weeks' start
}

const overTimeWeeks = 12

// overTimeBuckets: read at most this many thousands of activity entries,
// newest first (a variable so tests can lower it).
var overTimeBuckets = 25

// BoardOverTime counts, for each of the last twelve weeks, the cards
// added to a board and the cards marked complete, from the board's
// activity. It reads the newest activity a thousand entries at a time and
// stops at the twelve weeks' start, or after 25,000 entries.
func (o *Ops) BoardOverTime(ctx context.Context, tenant, actor, board string) (OverTimeView, error) {
	if _, _, err := o.boardFor(ctx, tenant, actor, board); err != nil {
		return OverTimeView{}, err
	}
	now := o.now()
	mon := time.Date(now.Year(), now.Month(), now.Day()-(int(now.Weekday())+6)%7, 0, 0, 0, 0, ist)
	start := mon.AddDate(0, 0, -7*(overTimeWeeks-1))
	v := OverTimeView{Weeks: make([]OverTimeWeek, overTimeWeeks)}
	added, done := make([]map[string]bool, overTimeWeeks), make([]map[string]bool, overTimeWeeks)
	for i := range v.Weeks {
		v.Weeks[i].Week = start.AddDate(0, 0, 7*i).Format("2006-01-02")
		added[i], done[i] = map[string]bool{}, map[string]bool{}
	}
	s, _, err := docs.Get[seqDoc](ctx, o.Docs, tenant, cSeq, "boardact:"+board)
	if errors.Is(err, docs.ErrNotFound) {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	n := 0
	for k := s.N / 1000; k >= 0; k-- {
		if n == overTimeBuckets {
			v.Capped = true
			break
		}
		n++
		// Activity keys are "<board>|<seq, 12 digits>": these are seq k*1000 to k*1000+999.
		as, err := docs.List[BoardActivity](ctx, o.Docs, tenant, cBoardAct, fmt.Sprintf("%s|%09d", board, k))
		if err != nil {
			return v, err
		}
		older := false
		for _, a := range as {
			v.Read++
			t, err := time.Parse(time.RFC3339, a.At)
			if err != nil || a.Card == "" {
				continue
			}
			if t.Before(start) {
				older = true
				continue
			}
			w := int(t.Sub(start) / (7 * 24 * time.Hour))
			if w < 0 || w >= overTimeWeeks {
				continue
			}
			switch a.What {
			case "card.created":
				added[w][a.Card] = true
			case "card.due_done":
				done[w][a.Card] = true
			}
		}
		if older {
			break
		}
	}
	for i := range v.Weeks {
		v.Weeks[i].Added, v.Weeks[i].Done = len(added[i]), len(done[i])
	}
	return v, nil
}

// ---- all boards: cards across boards ----

// XCardsQuery filters the cards across boards. Lists and labels are by
// name (any case), so one filter covers the same list on many boards;
// "_none" in members or labels is a card with none.
type XCardsQuery struct {
	Boards  []string // board ids
	Lists   []string // list names
	Members []string // user ids
	Labels  []string // label names
	Due     []string // overdue, soon, later, none, done
	From    string   // YYYY-MM-DD: only cards whose dates touch From to To
	To      string
	Offset  int
	Limit   int // at most 500
}

// XCard is a card in the table or calendar of all boards.
type XCard struct {
	Board     string       `json:"board"`
	BoardName string       `json:"board_name"`
	Role      string       `json:"role"` // the person's role on the board: admin, member or observer
	Card      string       `json:"card"`
	No        int64        `json:"no"`
	Title     string       `json:"title"`
	List      string       `json:"list"`
	ListName  string       `json:"list_name"`
	Labels    []BoardLabel `json:"labels,omitempty"`
	Members   []string     `json:"members,omitempty"`
	Start     string       `json:"start,omitempty"`
	Due       string       `json:"due,omitempty"`
	DueDone   bool         `json:"due_done,omitempty"`
	Status    string       `json:"status"` // overdue, soon (within 24 hours), later, none or done
	Items     int          `json:"items,omitempty"`
	ItemsDone int          `json:"items_done,omitempty"`
	UpdatedAt string       `json:"updated_at"`
}

// XBoard is a board the person can see, for the boards filter.
type XBoard struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	Role string `json:"role"`
}

// XLabel is a label name in use, with the colour it has first.
type XLabel struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

// XCardsView is one page of the cards across boards, with what can be
// filtered on.
type XCardsView struct {
	Today   string         `json:"today"`
	Cards   []XCard        `json:"cards"`
	Total   int            `json:"total"` // cards that match
	Offset  int            `json:"offset"`
	Limit   int            `json:"limit"`
	More    bool           `json:"more,omitempty"`
	Capped  bool           `json:"capped,omitempty"` // more cards than are read at once: narrow the boards
	Boards  []XBoard       `json:"boards"`
	Lists   []string       `json:"lists"`
	Labels  []XLabel       `json:"labels"`
	Members []BoardUserRef `json:"members"`
}

const xCardsPage = 500

// xCardsScan is the most cards read for one page (a variable so tests can
// lower it).
var xCardsScan = 20000

// cardDueStatus is a card's due status: done, overdue, soon (within 24
// hours), later or none.
func cardDueStatus(c Card, now time.Time) string {
	if c.DueDone {
		return "done"
	}
	t, ok := boardTime(c.Due)
	switch {
	case !ok:
		return "none"
	case t.Before(now):
		return "overdue"
	case t.Sub(now) <= 24*time.Hour:
		return "soon"
	}
	return "later"
}

// labelName is how a label is called: its name, or its colour.
func labelName(l BoardLabel) string {
	if l.Name != "" {
		return l.Name
	}
	if l.Color == "" {
		return "Label"
	}
	return strings.ToUpper(l.Color[:1]) + l.Color[1:]
}

func lowerSet(xs []string) map[string]bool {
	m := map[string]bool{}
	for _, x := range xs {
		if x = strings.ToLower(strings.TrimSpace(x)); x != "" {
			m[x] = true
		}
	}
	return m
}

// CrossCards is the open cards on every board the person can see (the
// same boards as the boards home: Branch and AP staff see only their
// own), filtered, soonest due first, a page at a time. Closed boards,
// templates, archived cards and mirrors are left out. At most 20,000
// cards are read; past that Capped is set.
func (o *Ops) CrossCards(ctx context.Context, tenant, actor string, q XCardsQuery) (XCardsView, error) {
	bs, err := o.Boards(ctx, tenant, actor)
	if err != nil {
		return XCardsView{}, err
	}
	now := o.now()
	v := XCardsView{Today: now.Format("2006-01-02"), Cards: []XCard{}, Boards: []XBoard{}, Lists: []string{}, Labels: []XLabel{}, Members: []BoardUserRef{}}
	v.Limit, v.Offset = q.Limit, q.Offset
	if v.Limit <= 0 || v.Limit > xCardsPage {
		v.Limit = xCardsPage
	}
	if v.Offset < 0 {
		v.Offset = 0
	}
	for _, d := range []string{q.From, q.To} {
		if d != "" {
			if _, err := time.Parse("2006-01-02", d); err != nil {
				return v, invalid("give dates as YYYY-MM-DD")
			}
		}
	}
	from, to := q.From, q.To
	if from != "" && to == "" {
		to = "9999-12-31"
	}
	if to != "" && from == "" {
		from = "0000-01-01"
	}
	onBoards, lists, labels, due, members := map[string]bool{}, lowerSet(q.Lists), lowerSet(q.Labels), map[string]bool{}, map[string]bool{}
	for _, b := range q.Boards {
		onBoards[b] = true
	}
	for _, d := range q.Due {
		due[d] = true
	}
	for _, m := range q.Members {
		members[m] = true
	}
	listSeen, labelSeen, memberSeen := map[string]string{}, map[string]XLabel{}, map[string]bool{}
	var all []XCard
	scanned := 0
	for _, s := range bs {
		if s.Closed || s.Template {
			continue
		}
		v.Boards = append(v.Boards, XBoard{ID: s.ID, Name: s.Name, Kind: s.Kind, Role: s.Role})
		if len(onBoards) > 0 && !onBoards[s.ID] {
			continue
		}
		if scanned >= xCardsScan {
			v.Capped = true
			continue
		}
		b, _, err := docs.Get[Board](ctx, o.Docs, tenant, cBoard, s.ID)
		if err != nil || b.Deleted {
			continue
		}
		cs, err := o.boardCards(ctx, tenant, b.ID)
		if err != nil {
			return v, err
		}
		for _, c := range cs {
			if c.Archived || c.Template || c.Mirror != nil {
				continue
			}
			l, ok := boardList(b, c.List)
			if !ok || l.Archived {
				continue
			}
			if scanned++; scanned > xCardsScan {
				v.Capped = true
				break
			}
			x := XCard{Board: b.ID, BoardName: b.Name, Role: s.Role, Card: c.ID, No: c.No, Title: c.Title, List: l.ID, ListName: l.Name, Members: c.Members,
				Start: c.Start, Due: c.Due, DueDone: c.DueDone, Status: cardDueStatus(c, now), UpdatedAt: c.UpdatedAt}
			for _, id := range c.Labels {
				if lb, ok := boardLabel(b, id); ok {
					x.Labels = append(x.Labels, lb)
				}
			}
			for _, cl := range c.Checklists {
				for _, it := range cl.Items {
					x.Items++
					if it.Done {
						x.ItemsDone++
					}
				}
			}
			// What can be filtered on: every card on the chosen boards.
			if _, ok := listSeen[strings.ToLower(l.Name)]; !ok {
				listSeen[strings.ToLower(l.Name)] = l.Name
			}
			for _, lb := range x.Labels {
				if _, ok := labelSeen[strings.ToLower(labelName(lb))]; !ok {
					labelSeen[strings.ToLower(labelName(lb))] = XLabel{Name: labelName(lb), Color: lb.Color}
				}
			}
			for _, m := range c.Members {
				memberSeen[m] = true
			}
			if len(lists) > 0 && !lists[strings.ToLower(l.Name)] {
				continue
			}
			if len(due) > 0 && !due[x.Status] {
				continue
			}
			if len(members) > 0 {
				hit := members["_none"] && len(c.Members) == 0
				for _, m := range c.Members {
					hit = hit || members[m]
				}
				if !hit {
					continue
				}
			}
			if len(labels) > 0 {
				hit := labels["_none"] && len(x.Labels) == 0
				for _, lb := range x.Labels {
					hit = hit || labels[strings.ToLower(labelName(lb))]
				}
				if !hit {
					continue
				}
			}
			if from != "" && !cardInDays(c, from, to) {
				continue
			}
			all = append(all, x)
		}
	}
	at := func(c XCard) time.Time {
		if t, ok := boardTime(c.Due); ok {
			return t
		}
		if t, ok := boardTime(c.Start); ok && from != "" {
			return t
		}
		return time.Date(9999, 1, 1, 0, 0, 0, 0, ist)
	}
	sort.SliceStable(all, func(i, j int) bool {
		a, b := all[i], all[j]
		if ta, tb := at(a), at(b); !ta.Equal(tb) {
			return ta.Before(tb)
		}
		if a.BoardName != b.BoardName {
			return a.BoardName < b.BoardName
		}
		return a.No < b.No
	})
	v.Total = len(all)
	if v.Offset < len(all) {
		end := v.Offset + v.Limit
		if end > len(all) {
			end = len(all)
		}
		v.Cards = all[v.Offset:end]
		v.More = end < len(all)
	}
	for _, n := range listSeen {
		v.Lists = append(v.Lists, n)
	}
	sort.Slice(v.Lists, func(i, j int) bool { return strings.ToLower(v.Lists[i]) < strings.ToLower(v.Lists[j]) })
	for _, l := range labelSeen {
		v.Labels = append(v.Labels, l)
	}
	sort.Slice(v.Labels, func(i, j int) bool { return strings.ToLower(v.Labels[i].Name) < strings.ToLower(v.Labels[j].Name) })
	us, _ := o.Users(ctx, tenant)
	for _, u := range us {
		if memberSeen[u.ID] {
			v.Members = append(v.Members, BoardUserRef{ID: u.ID, Name: u.Name, Role: u.Role})
			delete(memberSeen, u.ID)
		}
	}
	for id := range memberSeen {
		v.Members = append(v.Members, BoardUserRef{ID: id, Name: id})
	}
	sort.Slice(v.Members, func(i, j int) bool { return strings.ToLower(v.Members[i].Name) < strings.ToLower(v.Members[j].Name) })
	return v, nil
}
