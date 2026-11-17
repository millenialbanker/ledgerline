package ops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"ledgerline/internal/docs"
)

// ---- Boards: Inbox and Planner ----
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
// Calendar feeds (iCalendar for Google Calendar or Outlook) were taken out:
// they keep card titles, which can name clients, on servers outside India,
// against SEBI's cloud framework (SEBI/HO/ITD/ITD_VAPT/P/CIR/2023/033).
//
// An Inbox is private in normal use, but it is part of the firm's records:
// the compliance officer or an Admin may open one read-only, giving a
// reason that goes to the sealed audit trail; its owner is told
// (OpenInboxForReview).

const (
	BoardInbox = "inbox"

	cBoardPlan = "boardplan" // "<user>|<block id>"
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

// InboxReview is a person's Inbox as the compliance officer or an Admin
// sees it: every card, archived ones too, and what happened to them. It is
// read-only: the reviewer gets no role on the Inbox.
type InboxReview struct {
	Owner     string          `json:"owner"`
	OwnerName string          `json:"owner_name"`
	Board     string          `json:"board"`
	Cards     []Card          `json:"cards"`
	Activity  []BoardActivity `json:"activity"` // newest first, the last 300
	At        string          `json:"at"`
}

// OpenInboxForReview lets the compliance officer or an Admin read a
// person's Inbox, for a stated reason: an Inbox is part of the firm's
// records (an inspection may ask for it). The opening and its reason go to
// the sealed audit trail, and the owner is told who opened it and why.
// Nothing can be changed through it.
func (o *Ops) OpenInboxForReview(ctx context.Context, tenant, actor, owner, reason string) (InboxReview, error) {
	if err := o.require(ctx, tenant, actor, "Opening a person's Inbox (read-only, with a reason)", RoleCompliance); err != nil {
		return InboxReview{}, err
	}
	if boardActorSystem(actor) {
		return InboxReview{}, fmt.Errorf("%w: a person opens an Inbox, giving a reason", ErrForbidden)
	}
	reason = strings.TrimSpace(reason)
	if utf8.RuneCountInString(reason) < 5 {
		return InboxReview{}, invalid("give the reason for opening the Inbox (it is recorded, and its owner is told)")
	}
	if owner == actor {
		return InboxReview{}, invalid("this is your own Inbox: open it from the boards home")
	}
	us, _ := o.Users(ctx, tenant)
	var who, me User
	for _, u := range us {
		switch u.ID {
		case owner:
			who = u
		case actor:
			me = u
		}
	}
	if who.ID == "" || who.Role == RoleClient {
		return InboxReview{}, fmt.Errorf("%w: %s is not a staff user", ErrNotFound, owner)
	}
	b, _, err := docs.Get[Board](ctx, o.Docs, tenant, cBoard, inboxID(owner))
	if err != nil || b.Kind != BoardInbox || b.CreatedBy != owner {
		return InboxReview{}, fmt.Errorf("%w: %s has not used an Inbox", ErrNotFound, firstOf(who.Name, owner))
	}
	cs, err := o.boardCards(ctx, tenant, b.ID)
	if err != nil {
		return InboxReview{}, err
	}
	as, err := docs.List[BoardActivity](ctx, o.Docs, tenant, cBoardAct, b.ID+"|")
	if err != nil {
		return InboxReview{}, err
	}
	v := InboxReview{Owner: owner, OwnerName: firstOf(who.Name, owner), Board: b.ID, Cards: cs, Activity: []BoardActivity{}, At: o.now().Format(time.RFC3339)}
	if v.Cards == nil {
		v.Cards = []Card{}
	}
	for i := len(as) - 1; i >= 0 && len(v.Activity) < 300; i-- {
		v.Activity = append(v.Activity, as[i])
	}
	o.log(ctx, tenant, actor, "board.inbox_opened", b.ID, map[string]any{"owner": owner, "reason": trim(reason, 500), "cards": len(cs)})
	role := map[string]string{RoleCompliance: "Compliance", RoleAdmin: "Admin"}[me.Role]
	o.boardNotifyKind(ctx, tenant, actor, b, "", NoticeOther, []string{owner}, fmt.Sprintf("%s (%s) opened your Inbox: %s", firstOf(me.Name, actor), firstOf(role, me.Role), clip(reason, 300)))
	return v, nil
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
