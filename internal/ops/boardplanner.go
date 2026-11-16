package ops

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"ledgerline/internal/docs"
)

// ---- Boards: Inbox, Planner and calendar feeds ----
//
// Inbox (Trello's personal capture): each person has a private board of
// kind "inbox" with one list, made the first time they use it. No one
// else can see it, firm Admins and Ledgerline's own actors included
// (boardRole), it is left off the boards home (Boards), and its notices
// go to its owner only (boardNotifyKind). Its cards are full cards; one
// is sorted by moving it to a board.
//
// Planner (Trello's planner): a person's week of the cards they are on
// that have dates, and their plan blocks: times set aside for a card.
// Blocks belong to the person (boardplan, "<user>|<id>") and leave the
// card's dates alone unless the person chooses "Set as due date".
// Ledgerline's own calendar (Calendar & circulars) has no per-person API,
// so its items are not shown here.
//
// Calendar feeds: an iCalendar (RFC 5545) feed of "my cards" or of one
// board, for Google Calendar or Outlook, at a long random address. Only
// the address's hash is kept. Feeds are off until the firm's Admin
// switches them on, for the firm or for one board: card titles may name
// clients, and a feed takes them out of Ledgerline.

const (
	BoardInbox = "inbox"

	cBoardPlan    = "boardplan"    // "<user>|<block id>"
	cBoardFeed    = "boardfeed"    // "<user>|<feed id>"
	cBoardFeedKey = "boardfeedkey" // "<sha256 of the feed's token, hex>"
	cBoardFeedCfg = "boardfeedcfg" // "firm"

	// boardFeedSetting is "on" in Board.Settings when the firm's Admin
	// allows a feed of that board.
	boardFeedSetting = "views.feed"
)

// ---- Inbox ----

// inboxID is a person's Inbox board: one per person, the same each time.
func inboxID(user string) string {
	h := sha256.Sum256([]byte("inbox|" + user))
	return "BI-" + hex.EncodeToString(h[:6])
}

func boardActorSystem(id string) bool {
	return id == "" || id == SystemActor || id == LedgyActor || strings.HasPrefix(id, "system:")
}

// inboxOwner is the person whose Inbox b is ("" for other boards).
func inboxOwner(b Board) string {
	if b.Kind != BoardInbox {
		return ""
	}
	return b.CreatedBy
}

// inboxRole: the owner is the Inbox's admin; no one else can see it.
func inboxRole(b Board, u User) string {
	if !boardActorSystem(u.ID) && u.ID == b.CreatedBy {
		return BoardAdmin
	}
	return ""
}

// inboxOnly keeps the people an Inbox's notices may go to: its owner.
func inboxOnly(b Board, users []string) []string {
	if b.Kind != BoardInbox {
		return users
	}
	var out []string
	for _, u := range users {
		if u == b.CreatedBy {
			out = append(out, u)
		}
	}
	return out
}

// inboxFixed refuses the board settings an Inbox does not have.
func inboxFixed(b Board, in BoardInput) error {
	if b.Kind != BoardInbox {
		return nil
	}
	if in.Members != nil || in.Visibility == "firm" || (in.Template != nil && *in.Template) {
		return invalid("your Inbox is yours alone: it has no other members, is never firm-wide and is not a template")
	}
	return nil
}

// inboxList is the Inbox's open list.
func inboxList(b Board) string {
	id, at := "", 0.0
	for _, l := range b.Lists {
		if !l.Archived && (id == "" || l.Pos < at) {
			id, at = l.ID, l.Pos
		}
	}
	return id
}

// inboxSummary is the person's Inbox as a board summary, if they have one.
func (o *Ops) inboxSummary(ctx context.Context, tenant, actor string) []BoardSummary {
	if boardActorSystem(actor) {
		return nil
	}
	b, _, err := docs.Get[Board](ctx, o.Docs, tenant, cBoard, inboxID(actor))
	if err != nil || b.Deleted || b.Kind != BoardInbox || b.CreatedBy != actor {
		return nil
	}
	return []BoardSummary{{ID: b.ID, Name: b.Name, Kind: b.Kind, Visibility: b.Visibility, Background: b.Background, Role: BoardAdmin, Lists: 1, Members: 1, UpdatedAt: b.UpdatedAt}}
}

// EnsureInbox is the person's Inbox, made the first time.
func (o *Ops) EnsureInbox(ctx context.Context, tenant, actor string) (Board, error) {
	u, err := o.boardUser(ctx, tenant, actor)
	if err != nil {
		return Board{}, err
	}
	if boardActorSystem(u.ID) {
		return Board{}, fmt.Errorf("%w: an Inbox belongs to a person", ErrForbidden)
	}
	id := inboxID(u.ID)
	b, _, err := docs.Get[Board](ctx, o.Docs, tenant, cBoard, id)
	if errors.Is(err, docs.ErrNotFound) {
		now := o.now().Format(time.RFC3339)
		nb := Board{ID: id, Name: "Inbox", Kind: BoardInbox, Visibility: "private", Members: []BoardPerson{{User: u.ID, Role: BoardAdmin}},
			Lists: []BoardList{{ID: boardID("L-", 4), Name: "Inbox", Pos: 1024}}, Labels: defaultLabels(), Background: "blue", CoversOn: true,
			CreatedBy: u.ID, CreatedAt: now, UpdatedAt: now}
		if _, err := docs.Put(ctx, o.Docs, tenant, cBoard, id, nb, 0, actor); err != nil {
			if !errors.Is(err, docs.ErrStale) {
				return Board{}, err
			}
			b, _, err = docs.Get[Board](ctx, o.Docs, tenant, cBoard, id) // made just now by another request
			return b, err
		}
		o.boardLog(ctx, tenant, actor, nb, "", "board.created", "started this Inbox", nil)
		return nb, nil
	}
	if err != nil {
		return Board{}, err
	}
	if b.Kind != BoardInbox || b.CreatedBy != u.ID {
		return Board{}, fmt.Errorf("%w: no such board", ErrNotFound)
	}
	if b.Closed || b.Deleted || inboxList(b) == "" { // keep it usable
		b, err = docs.Update(ctx, o.Docs, tenant, cBoard, id, actor, func(x *Board) error {
			x.Closed, x.Deleted = false, false
			if inboxList(*x) == "" {
				x.Lists = append(x.Lists, BoardList{ID: boardID("L-", 4), Name: "Inbox", Pos: float64(len(x.Lists)+1) * 1024})
			}
			return nil
		})
	}
	return b, err
}

// InboxView is a person's Inbox: its open cards, top first.
type InboxView struct {
	Board string `json:"board"`
	List  string `json:"list"`
	Cards []Card `json:"cards"`
}

// Inbox is the person's Inbox, made the first time.
func (o *Ops) Inbox(ctx context.Context, tenant, actor string) (InboxView, error) {
	b, err := o.EnsureInbox(ctx, tenant, actor)
	if err != nil {
		return InboxView{}, err
	}
	cs, err := o.boardCards(ctx, tenant, b.ID)
	if err != nil {
		return InboxView{}, err
	}
	out := InboxView{Board: b.ID, List: inboxList(b), Cards: []Card{}}
	for _, c := range cs {
		if l, ok := boardList(b, c.List); ok && !l.Archived && !c.Archived {
			out.Cards = append(out.Cards, c)
		}
	}
	sort.SliceStable(out.Cards, func(i, j int) bool { return out.Cards[i].Pos < out.Cards[j].Pos })
	return out, nil
}

// AddInboxCard puts a card at the top of the person's Inbox.
func (o *Ops) AddInboxCard(ctx context.Context, tenant, actor string, in CardInput) (Card, error) {
	b, err := o.EnsureInbox(ctx, tenant, actor)
	if err != nil {
		return Card{}, err
	}
	top := 0.0
	return o.AddCard(ctx, tenant, actor, b.ID, CardInput{List: inboxList(b), Title: in.Title, Description: in.Description, Due: in.Due, Pos: &top})
}

// ---- Planner ----

// PlanBlock is time a person set aside for a card.
type PlanBlock struct {
	ID        string `json:"id"`
	User      string `json:"user"`
	Board     string `json:"board"`
	Card      string `json:"card"`
	Start     string `json:"start"`   // YYYY-MM-DDTHH:MM, IST
	Minutes   int    `json:"minutes"` // 15 to 720
	Deleted   bool   `json:"deleted,omitempty"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// PlanBlockInput makes or changes a plan block.
type PlanBlockInput struct {
	Board   string  `json:"board,omitempty"`
	Card    string  `json:"card,omitempty"`
	Start   *string `json:"start,omitempty"`
	Minutes *int    `json:"minutes,omitempty"`
}

const (
	planMaxBlocks  = 2000
	planMinMinutes = 15
	planMaxMinutes = 720
	planDefault    = 30
	planMaxDays    = 42
	planMaxCards   = 300
)

func planKey(user, id string) string { return user + "|" + id }

// planTimes checks a block's start and length: it starts and ends on the
// same day (it may end at midnight).
func planTimes(start string, minutes int) (string, error) {
	t, err := time.ParseInLocation("2006-01-02T15:04", strings.TrimSpace(start), ist)
	if err != nil {
		return "", invalid("give the start as YYYY-MM-DDTHH:MM")
	}
	if minutes < planMinMinutes || minutes > planMaxMinutes {
		return "", invalid("a plan block is %d minutes to %d hours long", planMinMinutes, planMaxMinutes/60)
	}
	if end := t.Add(time.Duration(minutes) * time.Minute); end.Format("2006-01-02") != t.Format("2006-01-02") && !end.Equal(time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, ist)) {
		return "", invalid("a plan block ends on the day it starts")
	}
	return t.Format("2006-01-02T15:04"), nil
}

// planEnd is when a block ends, as a card date.
func planEnd(p PlanBlock) string {
	t, err := time.ParseInLocation("2006-01-02T15:04", p.Start, ist)
	if err != nil {
		return ""
	}
	return t.Add(time.Duration(p.Minutes) * time.Minute).Format("2006-01-02T15:04")
}

// CreatePlanBlock sets time aside for a card the person can see (30
// minutes unless they say).
func (o *Ops) CreatePlanBlock(ctx context.Context, tenant, actor string, in PlanBlockInput) (PlanBlock, error) {
	if boardActorSystem(actor) {
		return PlanBlock{}, fmt.Errorf("%w: plans belong to a person", ErrForbidden)
	}
	c, _, err := o.GetCard(ctx, tenant, actor, in.Board, in.Card)
	if err != nil {
		return PlanBlock{}, err
	}
	if c.Archived {
		return PlanBlock{}, invalid("restore #%d before planning it", c.No)
	}
	if in.Start == nil {
		return PlanBlock{}, invalid("give the start as YYYY-MM-DDTHH:MM")
	}
	mins := planDefault
	if in.Minutes != nil {
		mins = *in.Minutes
	}
	start, err := planTimes(*in.Start, mins)
	if err != nil {
		return PlanBlock{}, err
	}
	all, err := docs.List[PlanBlock](ctx, o.Docs, tenant, cBoardPlan, actor+"|")
	if err != nil {
		return PlanBlock{}, err
	}
	n := 0
	for _, p := range all {
		if !p.Deleted {
			n++
		}
	}
	if n >= planMaxBlocks {
		return PlanBlock{}, invalid("you have %d plan blocks; remove old ones first", n)
	}
	now := o.now().Format(time.RFC3339)
	p := PlanBlock{ID: boardID("P-", 5), User: actor, Board: in.Board, Card: in.Card, Start: start, Minutes: mins, CreatedAt: now, UpdatedAt: now}
	if _, err := docs.Put(ctx, o.Docs, tenant, cBoardPlan, planKey(actor, p.ID), p, 0, actor); err != nil {
		return PlanBlock{}, err
	}
	return p, nil
}

var errPlanGone = fmt.Errorf("%w: no such plan block", ErrNotFound)

// UpdatePlanBlock moves or resizes one of the person's own blocks.
func (o *Ops) UpdatePlanBlock(ctx context.Context, tenant, actor, id string, in PlanBlockInput) (PlanBlock, error) {
	if boardActorSystem(actor) || id == "" || strings.Contains(id, "|") {
		return PlanBlock{}, errPlanGone
	}
	p, err := docs.Update(ctx, o.Docs, tenant, cBoardPlan, planKey(actor, id), actor, func(x *PlanBlock) error {
		if x.Deleted || x.User != actor {
			return errPlanGone
		}
		start, mins := x.Start, x.Minutes
		if in.Start != nil {
			start = *in.Start
		}
		if in.Minutes != nil {
			mins = *in.Minutes
		}
		s, err := planTimes(start, mins)
		if err != nil {
			return err
		}
		x.Start, x.Minutes, x.UpdatedAt = s, mins, o.now().Format(time.RFC3339)
		return nil
	})
	if errors.Is(err, docs.ErrNotFound) {
		return PlanBlock{}, errPlanGone
	}
	return p, err
}

// DeletePlanBlock takes a block off the person's plan.
func (o *Ops) DeletePlanBlock(ctx context.Context, tenant, actor, id string) error {
	if boardActorSystem(actor) || id == "" || strings.Contains(id, "|") {
		return errPlanGone
	}
	_, err := docs.Update(ctx, o.Docs, tenant, cBoardPlan, planKey(actor, id), actor, func(x *PlanBlock) error {
		if x.Deleted || x.User != actor {
			return errPlanGone
		}
		x.Deleted, x.UpdatedAt = true, o.now().Format(time.RFC3339)
		return nil
	})
	if errors.Is(err, docs.ErrNotFound) {
		return errPlanGone
	}
	return err
}

// PlanSetDue makes the end of a block its card's due date (the person
// must be able to change the card).
func (o *Ops) PlanSetDue(ctx context.Context, tenant, actor, id string) (Card, error) {
	if boardActorSystem(actor) || id == "" || strings.Contains(id, "|") {
		return Card{}, errPlanGone
	}
	p, _, err := docs.Get[PlanBlock](ctx, o.Docs, tenant, cBoardPlan, planKey(actor, id))
	if err != nil || p.Deleted || p.User != actor {
		return Card{}, errPlanGone
	}
	board, card, ok := o.planFollow(ctx, tenant, actor, p)
	if !ok {
		return Card{}, errCardGone
	}
	due := planEnd(p)
	return o.UpdateCard(ctx, tenant, actor, board, card, CardPatch{Due: &due})
}

// planFollow finds a block's card where it is now: a card moved to
// another board (from the Inbox, say) is found there and the block
// follows it.
func (o *Ops) planFollow(ctx context.Context, tenant, actor string, p PlanBlock) (string, string, bool) {
	board, card := p.Board, p.Card
	for i := 0; i < 4; i++ {
		c, _, err := docs.Get[Card](ctx, o.Docs, tenant, cBoardCard, cardKey(board, card))
		if err != nil {
			return "", "", false
		}
		if !c.Deleted {
			if board != p.Board || card != p.Card {
				_, _ = docs.Update(ctx, o.Docs, tenant, cBoardPlan, planKey(actor, p.ID), actor, func(x *PlanBlock) error { x.Board, x.Card = board, card; return nil })
			}
			return board, card, true
		}
		b, c2, ok := strings.Cut(c.MovedTo, "|")
		if !ok {
			return "", "", false
		}
		board, card = b, c2
	}
	return "", "", false
}

// PlanCard is a card as the Planner shows it.
type PlanCard struct {
	Board     string       `json:"board"`
	BoardName string       `json:"board_name"`
	Card      string       `json:"card"`
	No        int64        `json:"no"`
	Title     string       `json:"title"`
	ListName  string       `json:"list_name"`
	Labels    []BoardLabel `json:"labels,omitempty"`
	Start     string       `json:"start,omitempty"`
	Due       string       `json:"due,omitempty"`
	DueDone   bool         `json:"due_done,omitempty"`
	Items     int          `json:"items,omitempty"`
	ItemsDone int          `json:"items_done,omitempty"`
	Inbox     bool         `json:"inbox,omitempty"`
	Planned   bool         `json:"planned,omitempty"`  // has a block in the days shown
	CanEdit   bool         `json:"can_edit,omitempty"` // the person may change its dates
}

// PlanBlockView is a block with its card.
type PlanBlockView struct {
	ID      string    `json:"id"`
	Start   string    `json:"start"`
	Minutes int       `json:"minutes"`
	Card    *PlanCard `json:"card,omitempty"` // nil: the card is gone or no longer visible
}

// PlannerView is a person's planner for some days.
type PlannerView struct {
	From       string          `json:"from"`
	To         string          `json:"to"` // the last day shown
	Today      string          `json:"today"`
	Blocks     []PlanBlockView `json:"blocks"`
	Dated      []PlanCard      `json:"dated"` // my cards with a start or due date in the days shown
	Cards      []PlanCard      `json:"cards"` // my open cards with no block in the days shown
	More       bool            `json:"more,omitempty"`
	Inbox      []PlanCard      `json:"inbox"`
	InboxBoard string          `json:"inbox_board"`
	InboxList  string          `json:"inbox_list"`
}

func planCardOf(b Board, c Card, role string) PlanCard {
	l, _ := boardList(b, c.List)
	pc := PlanCard{Board: b.ID, BoardName: b.Name, Card: c.ID, No: c.No, Title: c.Title, ListName: l.Name, Start: c.Start, Due: c.Due, DueDone: c.DueDone,
		Inbox: b.Kind == BoardInbox, CanEdit: (role == BoardAdmin || role == BoardMember) && !b.Closed && c.Mirror == nil}
	for _, id := range c.Labels {
		if lb, ok := boardLabel(b, id); ok {
			pc.Labels = append(pc.Labels, lb)
		}
	}
	for _, cl := range c.Checklists {
		for _, it := range cl.Items {
			pc.Items++
			if it.Done {
				pc.ItemsDone++
			}
		}
	}
	return pc
}

// cardInDays reports whether a card's start-to-due falls on any of the days.
func cardInDays(c Card, from, to string) bool {
	s, d := c.Start, c.Due
	if s == "" && d == "" {
		return false
	}
	if s == "" {
		s = d
	}
	if d == "" {
		d = s
	}
	if len(s) > 10 {
		s = s[:10]
	}
	if len(d) > 10 {
		d = d[:10]
	}
	if s > d {
		s, d = d, s
	}
	return s <= to && d >= from
}

// Planner is the person's plan for days from from (a Monday by default):
// their blocks, their cards with dates, the cards they have not planned
// and their Inbox.
func (o *Ops) Planner(ctx context.Context, tenant, actor, from string, days int) (PlannerView, error) {
	u, err := o.boardUser(ctx, tenant, actor)
	if err != nil {
		return PlannerView{}, err
	}
	ib, err := o.EnsureInbox(ctx, tenant, actor)
	if err != nil {
		return PlannerView{}, err
	}
	now := o.now()
	f, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(from), ist)
	if from == "" {
		f = time.Date(now.Year(), now.Month(), now.Day()-(int(now.Weekday())+6)%7, 0, 0, 0, 0, ist)
	} else if err != nil {
		return PlannerView{}, invalid("give the first day as YYYY-MM-DD")
	}
	if days <= 0 {
		days = 7
	}
	if days > planMaxDays {
		days = planMaxDays
	}
	v := PlannerView{From: f.Format("2006-01-02"), To: f.AddDate(0, 0, days-1).Format("2006-01-02"), Today: now.Format("2006-01-02"),
		Blocks: []PlanBlockView{}, Dated: []PlanCard{}, Cards: []PlanCard{}, Inbox: []PlanCard{}, InboxBoard: ib.ID, InboxList: inboxList(ib)}

	all, err := docs.List[PlanBlock](ctx, o.Docs, tenant, cBoardPlan, actor+"|")
	if err != nil {
		return v, err
	}
	planned := map[string]bool{}
	var blocks []PlanBlock
	for _, p := range all {
		if p.Deleted || p.User != actor || len(p.Start) < 10 || p.Start[:10] < v.From || p.Start[:10] > v.To {
			continue
		}
		if b, c, ok := o.planFollow(ctx, tenant, actor, p); ok {
			p.Board, p.Card = b, c
		}
		blocks = append(blocks, p)
		planned[cardKey(p.Board, p.Card)] = true
	}

	bs, err := o.Boards(ctx, tenant, actor)
	if err != nil {
		return v, err
	}
	bs = append(bs, o.inboxSummary(ctx, tenant, actor)...)
	seen := map[string]PlanCard{}
	for _, s := range bs {
		if s.Closed || s.Template {
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
		inbox := b.Kind == BoardInbox
		for _, c := range cs {
			if c.Archived || c.Template {
				continue
			}
			if l, ok := boardList(b, c.List); !ok || l.Archived {
				continue
			}
			k := cardKey(b.ID, c.ID)
			mine := inbox || (contains(c.Members, actor) && c.Mirror == nil)
			if !mine && !planned[k] {
				continue
			}
			pc := planCardOf(b, c, s.Role)
			pc.Planned = planned[k]
			seen[k] = pc
			if !mine {
				continue
			}
			if cardInDays(c, v.From, v.To) {
				v.Dated = append(v.Dated, pc)
			}
			if inbox {
				v.Inbox = append(v.Inbox, pc)
			} else if !c.DueDone && !pc.Planned {
				v.Cards = append(v.Cards, pc)
			}
		}
	}
	for _, p := range blocks {
		bv := PlanBlockView{ID: p.ID, Start: p.Start, Minutes: p.Minutes}
		if pc, ok := seen[cardKey(p.Board, p.Card)]; ok {
			bv.Card = &pc
		} else if c, b, err := o.GetCard(ctx, tenant, u.ID, p.Board, p.Card); err == nil && !c.Archived && !b.Closed {
			pc := planCardOf(b, c, boardRole(b, u))
			pc.Planned = true
			bv.Card = &pc
		}
		v.Blocks = append(v.Blocks, bv)
	}
	sort.SliceStable(v.Blocks, func(i, j int) bool { return v.Blocks[i].Start < v.Blocks[j].Start })
	due := func(c PlanCard) string {
		if c.Due == "" {
			return "9999"
		}
		return c.Due
	}
	sort.SliceStable(v.Cards, func(i, j int) bool {
		if a, b := due(v.Cards[i]), due(v.Cards[j]); a != b {
			return a < b
		}
		return v.Cards[i].Title < v.Cards[j].Title
	})
	sort.SliceStable(v.Dated, func(i, j int) bool {
		return firstOf(v.Dated[i].Start, v.Dated[i].Due) < firstOf(v.Dated[j].Start, v.Dated[j].Due)
	})
	if len(v.Cards) > planMaxCards {
		v.Cards, v.More = v.Cards[:planMaxCards], true
	}
	if len(v.Dated) > planMaxCards {
		v.Dated, v.More = v.Dated[:planMaxCards], true
	}
	return v, nil
}

// ---- calendar feeds ----

type boardFeedDoc struct {
	ID        string `json:"id"`
	User      string `json:"user"`
	Scope     string `json:"scope"` // mine or board
	Board     string `json:"board,omitempty"`
	Hash      string `json:"hash"` // sha256 of the token
	CreatedAt string `json:"created_at"`
	UsedAt    string `json:"used_at,omitempty"`
	Revoked   bool   `json:"revoked,omitempty"`
	RevokedAt string `json:"revoked_at,omitempty"`
}

type boardFeedKeyDoc struct {
	User string `json:"user"`
	ID   string `json:"id"`
}

type boardFeedCfg struct {
	On bool   `json:"on"`
	By string `json:"by,omitempty"`
	At string `json:"at,omitempty"`
}

// BoardFeed is one of a person's calendar feeds (its address is shown
// once, when it is made).
type BoardFeed struct {
	ID        string `json:"id"`
	Scope     string `json:"scope"`
	Board     string `json:"board,omitempty"`
	BoardName string `json:"board_name,omitempty"`
	CreatedAt string `json:"created_at"`
	UsedAt    string `json:"used_at,omitempty"`
	Allowed   bool   `json:"allowed"` // feeds are on for it now
}

// BoardFeedBoard is a board the person can see, and whether its feed is allowed.
type BoardFeedBoard struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	On      bool   `json:"on"` // the board's own switch
	Allowed bool   `json:"allowed"`
}

// BoardFeedsView is the calendar feed settings a person sees.
type BoardFeedsView struct {
	FirmOn    bool             `json:"firm_on"`
	FirmBy    string           `json:"firm_by,omitempty"`
	FirmAt    string           `json:"firm_at,omitempty"`
	CanSwitch bool             `json:"can_switch"` // the firm's Admin
	Feeds     []BoardFeed      `json:"feeds"`
	Boards    []BoardFeedBoard `json:"boards"`
}

const (
	feedMaxPerUser = 50
	feedMaxEvents  = 1000
)

var feedTokenRe = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

func feedHash(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

func (o *Ops) feedCfg(ctx context.Context, tenant string) boardFeedCfg {
	c, _, _ := docs.GetOr(ctx, o.Docs, tenant, cBoardFeedCfg, "firm", boardFeedCfg{})
	return c
}

func boardFeedOn(b Board) bool { return b.Settings[boardFeedSetting] == "on" }

// BoardFeeds is the person's calendar feeds and whether feeds are on.
func (o *Ops) BoardFeeds(ctx context.Context, tenant, actor string) (BoardFeedsView, error) {
	u, err := o.boardUser(ctx, tenant, actor)
	if err != nil {
		return BoardFeedsView{}, err
	}
	cfg := o.feedCfg(ctx, tenant)
	v := BoardFeedsView{FirmOn: cfg.On, FirmBy: cfg.By, FirmAt: cfg.At, CanSwitch: u.Role == RoleAdmin && !boardActorSystem(u.ID), Feeds: []BoardFeed{}, Boards: []BoardFeedBoard{}}
	bs, err := o.Boards(ctx, tenant, actor)
	if err != nil {
		return v, err
	}
	names := map[string]BoardFeedBoard{}
	for _, s := range bs {
		if s.Template {
			continue
		}
		b, _, err := docs.Get[Board](ctx, o.Docs, tenant, cBoard, s.ID)
		if err != nil {
			continue
		}
		fb := BoardFeedBoard{ID: b.ID, Name: b.Name, On: boardFeedOn(b), Allowed: cfg.On || boardFeedOn(b)}
		names[b.ID] = fb
		if !s.Closed {
			v.Boards = append(v.Boards, fb)
		}
	}
	sort.SliceStable(v.Boards, func(i, j int) bool { return strings.ToLower(v.Boards[i].Name) < strings.ToLower(v.Boards[j].Name) })
	fs, err := docs.List[boardFeedDoc](ctx, o.Docs, tenant, cBoardFeed, actor+"|")
	if err != nil {
		return v, err
	}
	for _, f := range fs {
		if f.Revoked || f.User != actor {
			continue
		}
		bf := BoardFeed{ID: f.ID, Scope: f.Scope, Board: f.Board, CreatedAt: f.CreatedAt, UsedAt: f.UsedAt, Allowed: cfg.On}
		if f.Scope == "board" {
			nb, ok := names[f.Board]
			bf.BoardName, bf.Allowed = nb.Name, ok && nb.Allowed
		}
		v.Feeds = append(v.Feeds, bf)
	}
	return v, nil
}

// CreateBoardFeed makes a feed of the person's cards (scope "mine") or of
// a board, and returns its secret token, shown this once. An existing
// feed of the same cards is revoked: this is also how a feed's address is
// changed.
func (o *Ops) CreateBoardFeed(ctx context.Context, tenant, actor, scope, board string) (BoardFeed, string, error) {
	u, err := o.boardUser(ctx, tenant, actor)
	if err != nil {
		return BoardFeed{}, "", err
	}
	if boardActorSystem(u.ID) {
		return BoardFeed{}, "", fmt.Errorf("%w: a calendar feed belongs to a person", ErrForbidden)
	}
	cfg := o.feedCfg(ctx, tenant)
	off := fmt.Errorf("%w: calendar feeds are off. The firm's Admin can switch them on, for the firm or for a board", ErrForbidden)
	out := BoardFeed{Scope: scope, Allowed: true}
	switch scope {
	case "mine":
		board = ""
		if !cfg.On {
			return BoardFeed{}, "", off
		}
	case "board":
		b, _, err := o.boardFor(ctx, tenant, actor, board)
		if err != nil {
			return BoardFeed{}, "", err
		}
		if b.Kind == BoardInbox {
			return BoardFeed{}, "", invalid("your Inbox's cards are in your My cards feed")
		}
		if !cfg.On && !boardFeedOn(b) {
			return BoardFeed{}, "", off
		}
		out.BoardName = b.Name
	default:
		return BoardFeed{}, "", invalid("a feed is of your cards or of a board")
	}
	fs, err := docs.List[boardFeedDoc](ctx, o.Docs, tenant, cBoardFeed, actor+"|")
	if err != nil {
		return BoardFeed{}, "", err
	}
	now := o.now().Format(time.RFC3339)
	n := 0
	for _, f := range fs {
		if f.Revoked {
			continue
		}
		if f.Scope == scope && f.Board == board {
			_, _ = docs.Update(ctx, o.Docs, tenant, cBoardFeed, planKey(actor, f.ID), actor, func(x *boardFeedDoc) error { x.Revoked, x.RevokedAt = true, now; return nil })
			continue
		}
		n++
	}
	if n >= feedMaxPerUser {
		return BoardFeed{}, "", invalid("you have %d calendar feeds; stop some first", n)
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return BoardFeed{}, "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	f := boardFeedDoc{ID: boardID("CF-", 5), User: actor, Scope: scope, Board: board, Hash: feedHash(token), CreatedAt: now}
	if _, err := docs.Put(ctx, o.Docs, tenant, cBoardFeedKey, f.Hash, boardFeedKeyDoc{User: actor, ID: f.ID}, 0, actor); err != nil {
		return BoardFeed{}, "", err
	}
	if _, err := docs.Put(ctx, o.Docs, tenant, cBoardFeed, planKey(actor, f.ID), f, 0, actor); err != nil {
		return BoardFeed{}, "", err
	}
	o.log(ctx, tenant, actor, "board.feed_created", f.ID, map[string]any{"scope": scope, "board": board})
	out.ID, out.Board, out.CreatedAt = f.ID, board, now
	return out, token, nil
}

// RevokeBoardFeed stops one of the person's feeds: its address no longer works.
func (o *Ops) RevokeBoardFeed(ctx context.Context, tenant, actor, id string) error {
	if boardActorSystem(actor) || id == "" || strings.Contains(id, "|") {
		return fmt.Errorf("%w: no such feed", ErrNotFound)
	}
	_, err := docs.Update(ctx, o.Docs, tenant, cBoardFeed, planKey(actor, id), actor, func(x *boardFeedDoc) error {
		if x.Revoked || x.User != actor {
			return fmt.Errorf("%w: no such feed", ErrNotFound)
		}
		x.Revoked, x.RevokedAt = true, o.now().Format(time.RFC3339)
		return nil
	})
	if errors.Is(err, docs.ErrNotFound) {
		return fmt.Errorf("%w: no such feed", ErrNotFound)
	}
	if err == nil {
		o.log(ctx, tenant, actor, "board.feed_revoked", id, nil)
	}
	return err
}

// SetFirmFeeds switches calendar feeds on or off for the whole firm (the
// firm's Admin).
func (o *Ops) SetFirmFeeds(ctx context.Context, tenant, actor string, on bool) error {
	if err := o.require(ctx, tenant, actor, "Switching calendar feeds on or off", RoleAdmin); err != nil {
		return err
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cBoardFeedCfg, "firm", boardFeedCfg{On: on, By: actor, At: o.now().Format(time.RFC3339)}, actor); err != nil {
		return err
	}
	o.log(ctx, tenant, actor, "board.feeds", "firm", map[string]any{"on": on})
	return nil
}

// SetBoardFeed allows or stops feeds of one board (the firm's Admin).
func (o *Ops) SetBoardFeed(ctx context.Context, tenant, actor, board string, on bool) (Board, error) {
	if err := o.require(ctx, tenant, actor, "Switching a board's calendar feed on or off", RoleAdmin); err != nil {
		return Board{}, err
	}
	b, _, err := o.boardFor(ctx, tenant, actor, board)
	if err != nil {
		return Board{}, err
	}
	if b.Kind == BoardInbox {
		return Board{}, invalid("an Inbox has no feed of its own")
	}
	b, err = docs.Update(ctx, o.Docs, tenant, cBoard, board, actor, func(x *Board) error {
		if x.Settings == nil {
			x.Settings = map[string]string{}
		}
		if on {
			x.Settings[boardFeedSetting] = "on"
		} else {
			delete(x.Settings, boardFeedSetting)
		}
		return nil
	})
	if err != nil {
		return b, err
	}
	o.boardLog(ctx, tenant, actor, b, "", "board.updated", map[bool]string{true: "allowed calendar feeds of this board", false: "stopped calendar feeds of this board"}[on], nil)
	o.log(ctx, tenant, actor, "board.feeds", board, map[string]any{"on": on})
	return b, nil
}

// FeedEvent is one card due date in a feed.
type FeedEvent struct {
	Board     string
	BoardName string
	Card      string
	No        int64
	Title     string
	ListName  string
	Due       string
	Done      bool
	Updated   string
}

// BoardFeedICS is the iCalendar for a feed's token (base is the address
// people open Ledgerline at, for the links back). A token that is not a
// live feed is not found; a feed whose cards are not allowed out (feeds
// switched off, or the board no longer visible) is an empty calendar, so
// a calendar that subscribed drops what it had.
func (o *Ops) BoardFeedICS(ctx context.Context, tenant, token, base string) ([]byte, error) {
	gone := fmt.Errorf("%w: no such calendar feed", ErrNotFound)
	if !feedTokenRe.MatchString(token) {
		return nil, gone
	}
	k, _, err := docs.Get[boardFeedKeyDoc](ctx, o.Docs, tenant, cBoardFeedKey, feedHash(token))
	if err != nil {
		return nil, gone
	}
	f, _, err := docs.Get[boardFeedDoc](ctx, o.Docs, tenant, cBoardFeed, planKey(k.User, k.ID))
	if err != nil || f.Revoked || f.Hash != feedHash(token) {
		return nil, gone
	}
	u, err := o.boardUser(ctx, tenant, f.User)
	if err != nil || boardActorSystem(u.ID) {
		return nil, gone
	}
	now := o.now()
	if t, err := time.Parse(time.RFC3339, f.UsedAt); err != nil || now.Sub(t) > time.Hour {
		_, _ = docs.Update(ctx, o.Docs, tenant, cBoardFeed, planKey(f.User, f.ID), f.User, func(x *boardFeedDoc) error { x.UsedAt = now.Format(time.RFC3339); return nil })
	}
	cfg := o.feedCfg(ctx, tenant)
	name := "Ledgerline: My cards"
	var evs []FeedEvent
	add := func(b Board, c Card) {
		if c.Archived || c.Template || c.Deleted || c.Due == "" {
			return
		}
		t, ok := boardTime(c.Due)
		if !ok || t.Before(now.AddDate(0, 0, -120)) || t.After(now.AddDate(0, 0, 400)) {
			return
		}
		l, ok := boardList(b, c.List)
		if !ok || l.Archived {
			return
		}
		evs = append(evs, FeedEvent{Board: b.ID, BoardName: b.Name, Card: c.ID, No: c.No, Title: c.Title, ListName: l.Name, Due: c.Due, Done: c.DueDone, Updated: c.UpdatedAt})
	}
	switch f.Scope {
	case "mine":
		if cfg.On {
			bs, _ := o.Boards(ctx, tenant, u.ID)
			bs = append(bs, o.inboxSummary(ctx, tenant, u.ID)...)
			for _, s := range bs {
				if s.Closed || s.Template {
					continue
				}
				b, _, err := docs.Get[Board](ctx, o.Docs, tenant, cBoard, s.ID)
				if err != nil || b.Deleted {
					continue
				}
				cs, _ := o.boardCards(ctx, tenant, b.ID)
				for _, c := range cs {
					if b.Kind == BoardInbox || (contains(c.Members, u.ID) && c.Mirror == nil) {
						add(b, c)
					}
				}
			}
		}
	case "board":
		b, _, err := o.boardFor(ctx, tenant, u.ID, f.Board)
		if err == nil {
			name = "Ledgerline: " + b.Name
		}
		if err == nil && b.Kind != BoardInbox && !b.Closed && !b.Template && (cfg.On || boardFeedOn(b)) {
			cs, _ := o.boardCards(ctx, tenant, b.ID)
			for _, c := range cs {
				if c.Mirror == nil {
					add(b, c)
				}
			}
		}
	}
	sort.SliceStable(evs, func(i, j int) bool {
		a, _ := boardTime(evs[i].Due)
		b, _ := boardTime(evs[j].Due)
		return a.Before(b)
	})
	if len(evs) > feedMaxEvents {
		evs = evs[:feedMaxEvents]
	}
	return feedICS(name, evs, strings.TrimRight(base, "/"), now), nil
}

// ---- iCalendar (RFC 5545) ----

// icsLines writes content lines: CRLF after each, folded so no line is
// longer than 75 octets, never inside a UTF-8 character.
type icsLines struct{ b strings.Builder }

func (w *icsLines) add(line string) {
	max := 75
	for len(line) > max {
		cut := max
		for cut > 0 && !utf8.RuneStart(line[cut]) {
			cut--
		}
		w.b.WriteString(line[:cut])
		w.b.WriteString("\r\n ")
		line = line[cut:]
		max = 74 // the space that starts a folded line counts
	}
	w.b.WriteString(line)
	w.b.WriteString("\r\n")
}

// icsText escapes a TEXT value: backslash, semicolon, comma and newline.
func icsText(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case r == ';':
			b.WriteString(`\;`)
		case r == ',':
			b.WriteString(`\,`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
		case r < 0x20 || r == 0x7f:
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func icsUTC(t time.Time) string { return t.UTC().Format("20060102T150405Z") }

// feedICS is the calendar: each card's due date as an event, all day when
// it has no time, with the card's title, its board and list, and a link
// back. Descriptions and comments stay in Ledgerline.
func feedICS(name string, evs []FeedEvent, base string, now time.Time) []byte {
	var w icsLines
	w.add("BEGIN:VCALENDAR")
	w.add("VERSION:2.0")
	w.add("PRODID:-//Ledgerline//Boards calendar feed//EN")
	w.add("CALSCALE:GREGORIAN")
	w.add("X-WR-CALNAME:" + icsText(name))
	w.add("X-WR-CALDESC:" + icsText("Due dates of cards in Ledgerline. Card details stay in Ledgerline."))
	w.add("REFRESH-INTERVAL;VALUE=DURATION:PT1H")
	w.add("X-PUBLISHED-TTL:PT1H")
	for _, e := range evs {
		stamp := now
		if t, err := time.Parse(time.RFC3339, e.Updated); err == nil {
			stamp = t
		}
		link := base + "/#boards/" + e.Board + "/" + e.Card
		title := e.Title
		if e.Done {
			title = "✓ " + title
		}
		w.add("BEGIN:VEVENT")
		w.add("UID:" + e.Board + "." + e.Card + "@boards.ledgerline")
		w.add("DTSTAMP:" + icsUTC(stamp))
		w.add("LAST-MODIFIED:" + icsUTC(stamp))
		if len(e.Due) > 10 {
			t, _ := boardTime(e.Due)
			w.add("DTSTART:" + icsUTC(t))
			w.add("DTEND:" + icsUTC(t.Add(30*time.Minute)))
		} else {
			d, _ := time.Parse("2006-01-02", e.Due)
			w.add("DTSTART;VALUE=DATE:" + d.Format("20060102"))
			w.add("DTEND;VALUE=DATE:" + d.AddDate(0, 0, 1).Format("20060102"))
		}
		w.add("SUMMARY:" + icsText(title))
		w.add("DESCRIPTION:" + icsText(fmt.Sprintf("Board: %s\nList: %s\nCard #%d\nOpen it in Ledgerline: %s", e.BoardName, e.ListName, e.No, link)))
		w.add("URL:" + link)
		w.add("TRANSP:TRANSPARENT")
		w.add("END:VEVENT")
	}
	w.add("END:VCALENDAR")
	return []byte(w.b.String())
}
