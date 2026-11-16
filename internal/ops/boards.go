package ops

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
)

// ---- Boards: Trello-style boards, lists and cards ----
//
// Two kinds of board:
//
//   - Team boards (Kind "team"): free-form boards staff make for anything:
//     lists of cards with labels, members, dates, checklists, comments,
//     attachments and custom fields.
//   - Ledgerline boards (Kind "ledgerline"): kept in step with Ledgerline's
//     own records (the work queue, reconciliation breaks, unpaid securities,
//     onboarding, complaints, leads) by boardsync.go. Their cards carry a
//     Link to the record; people add comments, checklists, labels and
//     members to them like any card.
//
// Who sees what: staff only (never a client login). A "firm" board is
// open to every head-office user; a "private" board to its members.
// Board roles: admin (settings, members, close and delete), member
// (lists and cards) and observer (read and comment). A firm Admin is an
// admin of every firm board.
//
// Other files plug in through hooks so this file stays the core:
// boardEventHooks (automation rules react to what happened),
// boardTickHooks (scheduled and due-date work, once a minute),
// boardReadHooks (Ledgerline boards bring their cards up to date before a
// board is read) and boardMoveHooks (a move of a linked card does the
// matching work in Ledgerline, or is refused).

const (
	cBoard     = "board"
	cBoardCard = "boardcard" // "<board>|<card>"
	cBoardAct  = "boardact"  // "<board>|<seq, 12 digits>"
	cBoardFile = "boardfile"
	cBoardNote = "boardnote" // "<user>|<seq, 12 digits>"

	BoardTeam       = "team"
	BoardLedgerline = "ledgerline"

	BoardAdmin    = "admin"
	BoardMember   = "member"
	BoardObserver = "observer"

	boardMaxFile = 15 << 20
)

// Board is one board: its lists, labels, custom fields and members. Cards
// are separate documents.
type Board struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Kind        string            `json:"kind"`             // team or ledgerline
	Source      string            `json:"source,omitempty"` // a Ledgerline board's records (boardsync.go)
	Visibility  string            `json:"visibility"`       // firm or private
	Members     []BoardPerson     `json:"members"`
	Lists       []BoardList       `json:"lists"`
	Labels      []BoardLabel      `json:"labels"`
	Fields      []BoardField      `json:"fields,omitempty"`
	Background  string            `json:"background,omitempty"` // a colour name, or "file:<id>" for an uploaded image
	CoversOn    bool              `json:"covers_on"`
	Template    bool              `json:"template,omitempty"`
	Closed      bool              `json:"closed,omitempty"`
	Starred     []string          `json:"starred,omitempty"`
	EmailKey    string            `json:"email_key,omitempty"` // email-to-board (boardextras.go)
	EmailList   string            `json:"email_list,omitempty"`
	CardSeq     int64             `json:"card_seq"`
	Settings    map[string]string `json:"settings,omitempty"` // for the other board files
	Watchers    []string          `json:"watchers,omitempty"` // told about cards added, moved, completed, commented on, re-dated (boardlists.go)
	Perms       BoardPerms        `json:"perms"`              // who may comment, add members, join (boardlists.go)
	Deleted     bool              `json:"deleted,omitempty"`
	CreatedBy   string            `json:"created_by"`
	CreatedAt   string            `json:"created_at"`
	UpdatedAt   string            `json:"updated_at"`
}

// BoardPerson is a member of a board and their role on it.
type BoardPerson struct {
	User string `json:"user"`
	Role string `json:"role"`
}

// BoardList is a list (column) of cards.
type BoardList struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Pos      float64 `json:"pos"`
	Color    string  `json:"color,omitempty"`
	Limit    int     `json:"limit,omitempty"` // most cards it should hold (0: no limit)
	Archived bool    `json:"archived,omitempty"`
	Source   string  `json:"source,omitempty"` // the record status a Ledgerline board's list stands for
	// Watchers are told when a card is added to the list or moved into it.
	Watchers []string `json:"watchers,omitempty"`
	MovedTo  string   `json:"moved_to,omitempty"` // the board the list moved to (it stays here, archived, for its archived cards)
}

// BoardLabel is a coloured label, named or not.
type BoardLabel struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color"`
}

// BoardField is a custom field: text, number, date, checkbox or list.
type BoardField struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	Options []string `json:"options,omitempty"`  // for list
	OnFront bool     `json:"on_front,omitempty"` // shown on the card in the list
}

// Card is a card on a board.
type Card struct {
	Board       string            `json:"board"`
	ID          string            `json:"id"`
	No          int64             `json:"no"` // the card's number on its board
	List        string            `json:"list"`
	Pos         float64           `json:"pos"`
	Title       string            `json:"title"`
	Description string            `json:"description,omitempty"`
	Labels      []string          `json:"labels,omitempty"`
	Members     []string          `json:"members,omitempty"`
	Watchers    []string          `json:"watchers,omitempty"`
	Votes       []string          `json:"votes,omitempty"`
	Start       string            `json:"start,omitempty"` // YYYY-MM-DD or YYYY-MM-DDTHH:MM (IST)
	Due         string            `json:"due,omitempty"`
	DueDone     bool              `json:"due_done,omitempty"`
	Reminder    int               `json:"reminder,omitempty"` // minutes before due (0: none)
	Reminded    string            `json:"reminded,omitempty"` // the due the reminder went out for
	Checklists  []Checklist       `json:"checklists,omitempty"`
	Attachments []CardAttachment  `json:"attachments,omitempty"`
	Cover       *CardCover        `json:"cover,omitempty"`
	Fields      map[string]string `json:"fields,omitempty"`
	Location    string            `json:"location,omitempty"`
	Comments    []CardComment     `json:"comments,omitempty"`
	Link        *CardLink         `json:"link,omitempty"`   // a Ledgerline record (boardsync.go)
	Mirror      *CardRef          `json:"mirror,omitempty"` // shows another card (boardextras.go)
	Template    bool              `json:"template,omitempty"`
	Archived    bool              `json:"archived,omitempty"`
	// Deleted cards are kept (the store keeps every record) but no longer shown.
	Deleted   bool   `json:"deleted,omitempty"`
	MovedTo   string `json:"moved_to,omitempty"` // "<board>|<card>" when it went to another board
	CreatedBy string `json:"created_by"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// Checklist is a list of items to tick on a card.
type Checklist struct {
	ID    string      `json:"id"`
	Name  string      `json:"name"`
	Items []CheckItem `json:"items"`
}

// CheckItem is one item of a checklist.
type CheckItem struct {
	ID     string `json:"id"`
	Text   string `json:"text"`
	Done   bool   `json:"done,omitempty"`
	Member string `json:"member,omitempty"`
	Due    string `json:"due,omitempty"`
}

// CardAttachment is a file uploaded to a card, or a link.
type CardAttachment struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	File  string `json:"file,omitempty"` // board file id
	URL   string `json:"url,omitempty"`  // a link
	Type  string `json:"type,omitempty"`
	Size  int64  `json:"size,omitempty"`
	Image bool   `json:"image,omitempty"`
	By    string `json:"by"`
	At    string `json:"at"`
}

// CardCover is a card's cover: a colour or one of its images.
type CardCover struct {
	Color string `json:"color,omitempty"`
	File  string `json:"file,omitempty"`
	Full  bool   `json:"full,omitempty"` // the cover fills the card
}

// CardComment is a comment on a card.
type CardComment struct {
	ID        string              `json:"id"`
	By        string              `json:"by"`
	At        string              `json:"at"`
	Text      string              `json:"text"`
	Edited    string              `json:"edited,omitempty"`
	Mentions  []string            `json:"mentions,omitempty"`
	Groups    []string            `json:"groups,omitempty"` // "board", "card": the @board and @card that notified people
	Reactions map[string][]string `json:"reactions,omitempty"`
}

// CardLink ties a card to a Ledgerline record.
type CardLink struct {
	Kind   string `json:"kind"` // work, break, cuspa, application, ticket, lead, client …
	ID     string `json:"id"`
	Label  string `json:"label"`
	View   string `json:"view,omitempty"` // the console screen that works it
	Status string `json:"status,omitempty"`
	UCC    string `json:"ucc,omitempty"`
}

// CardRef names a card on a board.
type CardRef struct {
	Board string `json:"board"`
	Card  string `json:"card"`
}

// BoardActivity is one entry of a board's activity.
type BoardActivity struct {
	Board string            `json:"board"`
	Seq   int64             `json:"seq"`
	Card  string            `json:"card,omitempty"`
	By    string            `json:"by"`
	At    string            `json:"at"`
	What  string            `json:"what"` // a code, e.g. card.moved
	Text  string            `json:"text"` // for people
	Data  map[string]string `json:"data,omitempty"`
}

// BoardNotice is a notification for one person.
type BoardNotice struct {
	ID    string `json:"id"`
	User  string `json:"user"`
	At    string `json:"at"`
	Board string `json:"board"`
	Card  string `json:"card,omitempty"`
	By    string `json:"by,omitempty"`
	Text  string `json:"text"`
	Read  bool   `json:"read,omitempty"`
	// Kind says what the notice is about (NoticeAdded …), so people can
	// choose which kinds reach them by email. Older notices have none.
	Kind string `json:"kind,omitempty"`
}

// Kinds of board notice.
const (
	NoticeAdded      = "added"      // added to a board, a card or a checklist item
	NoticeMention    = "mention"    // @mentioned in a comment
	NoticeComment    = "comment"    // a comment on a card one watches or is on
	NoticeMoved      = "moved"      // a card one watches moved
	NoticeDue        = "due"        // a due date reminder
	NoticeWatch      = "watch"      // anything else on a card, list or board one watches
	NoticeAutomation = "automation" // sent by an automation rule or about one
	NoticeOther      = "other"
)

type boardFileDoc struct {
	ID    string `json:"id"`
	Board string `json:"board"`
	Name  string `json:"name"`
	Type  string `json:"type"`
	Size  int64  `json:"size"`
	Image bool   `json:"image"`
	RawID string `json:"raw_id"`
	By    string `json:"by"`
	At    string `json:"at"`
}

// BoardEvent is something that happened on a board, for the automation
// rules (boardrules.go) and anything else that listens.
type BoardEvent struct {
	Board string            `json:"board"`
	Card  string            `json:"card,omitempty"`
	Kind  string            `json:"kind"` // card.created, card.moved, card.label_added, card.member_added, card.due_set, card.due_done, checklist.done, card.archived, comment.added, card.updated
	Actor string            `json:"actor"`
	Data  map[string]string `json:"data,omitempty"` // e.g. from_list, to_list, label, member, checklist
}

var (
	// boardEventHooks run after each change, in order. A hook must not
	// fail the change; it logs its own errors.
	boardEventHooks []func(ctx context.Context, o *Ops, tenant string, ev BoardEvent)
	// boardTickHooks run once a minute from the scheduler.
	boardTickHooks []func(ctx context.Context, o *Ops, tenant string, now time.Time)
	// boardReadHooks run before a board's cards are read (Ledgerline
	// boards sync their cards).
	boardReadHooks []func(ctx context.Context, o *Ops, tenant string, b Board) error
	// boardMoveHooks run when a card with a Link changes list: a hook
	// that knows the link's kind does the matching work, or returns an
	// error to refuse the move. handled reports whether any hook knew it.
	boardMoveHooks []func(ctx context.Context, o *Ops, tenant, actor string, b Board, c Card, to BoardList) (handled bool, err error)
)

// OnBoardEvent registers a hook for board events.
func OnBoardEvent(f func(ctx context.Context, o *Ops, tenant string, ev BoardEvent)) {
	boardEventHooks = append(boardEventHooks, f)
}

func (o *Ops) boardEvent(ctx context.Context, tenant string, ev BoardEvent) {
	for _, h := range boardEventHooks {
		h(ctx, o, tenant, ev)
	}
}

// boardsTick is the boards' minute: due reminders, then the hooks.
func (o *Ops) boardsTick(ctx context.Context, tenant string, now time.Time) {
	o.boardDueReminders(ctx, tenant, now)
	for _, h := range boardTickHooks {
		h(ctx, o, tenant, now)
	}
}

// ---- small helpers ----

var errCardGone = fmt.Errorf("%w: no such card", ErrNotFound)

func boardID(prefix string, n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

func cardKey(board, card string) string { return board + "|" + card }

var boardColors = []string{"green", "yellow", "orange", "red", "purple", "blue", "sky", "lime", "pink", "black"}

func boardColorOK(c string) bool { return c == "" || contains(boardColors, c) }

// Labels have 30 colours, as in Trello: each of the ten in a subtle, normal
// and bold shade ("green_subtle", "green", "green_bold"). Lists, covers and
// backgrounds keep the ten.
func boardLabelColorOK(c string) bool {
	h, shade, _ := strings.Cut(c, "_")
	return c == "" || contains(boardColors, h) && (shade == "" && !strings.Contains(c, "_") || shade == "subtle" || shade == "bold")
}

// labelColorName is a label colour for people: "green", "subtle green".
func labelColorName(c string) string {
	if h, shade, ok := strings.Cut(c, "_"); ok {
		return shade + " " + h
	}
	return c
}

// boardDate accepts YYYY-MM-DD or YYYY-MM-DDTHH:MM, or "".
func boardDate(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	if _, err := time.Parse("2006-01-02", s); err == nil {
		return s, nil
	}
	if _, err := time.Parse("2006-01-02T15:04", s); err == nil {
		return s, nil
	}
	return "", invalid("give a date as YYYY-MM-DD or YYYY-MM-DDTHH:MM")
}

// boardTime is a card date as a time in IST (a date alone is the end of
// that day).
func boardTime(s string) (time.Time, bool) {
	if t, err := time.ParseInLocation("2006-01-02T15:04", s, ist); err == nil {
		return t, true
	}
	if t, err := time.ParseInLocation("2006-01-02", s, ist); err == nil {
		return t.Add(23*time.Hour + 59*time.Minute), true
	}
	return time.Time{}, false
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func textField(what, s string, max int, required bool) (string, error) {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n"))
	if required && s == "" {
		return "", invalid("give the %s", what)
	}
	if utf8.RuneCountInString(s) > max {
		return "", invalid("the %s can be at most %d characters", what, max)
	}
	return s, nil
}

// ---- who may do what ----

type boardViewer struct {
	user  User
	role  string // firm role
	board string // board role: admin, member, observer or "" (cannot see)
}

func (v boardViewer) can(roles ...string) bool { return contains(roles, v.board) }

func (o *Ops) boardUser(ctx context.Context, tenant, actor string) (User, error) {
	if actor == SystemActor || actor == LedgyActor || strings.HasPrefix(actor, "system:") {
		return User{ID: actor, Name: "Ledgerline", Role: RoleAdmin, Active: true}, nil
	}
	role, err := o.RoleOf(ctx, tenant, actor)
	if err != nil {
		return User{}, err
	}
	if role == RoleClient {
		return User{}, fmt.Errorf("%w: boards are for staff", ErrForbidden)
	}
	us, _ := o.Users(ctx, tenant)
	for _, u := range us {
		if u.ID == actor {
			return u, nil
		}
	}
	return User{ID: actor, Name: actor, Role: role, Active: true}, nil
}

// boardRole is a person's role on a board ("" when they cannot see it).
func boardRole(b Board, u User) string {
	if b.Kind == BoardLedgerline && (u.Role == RoleBranch || u.Role == RoleAP) {
		return "" // Ledgerline boards hold head-office records
	}
	for _, m := range b.Members {
		if m.User == u.ID {
			return m.Role
		}
	}
	if u.Role == RoleAdmin {
		return BoardAdmin
	}
	if b.Visibility == "firm" && u.Role != RoleBranch && u.Role != RoleAP {
		if b.Kind == BoardLedgerline {
			return BoardMember
		}
		return BoardObserver
	}
	return ""
}

func (o *Ops) boardFor(ctx context.Context, tenant, actor, id string, need ...string) (Board, boardViewer, error) {
	u, err := o.boardUser(ctx, tenant, actor)
	if err != nil {
		return Board{}, boardViewer{}, err
	}
	b, _, err := docs.Get[Board](ctx, o.Docs, tenant, cBoard, id)
	if err != nil || b.Deleted {
		return Board{}, boardViewer{}, fmt.Errorf("%w: no such board", ErrNotFound)
	}
	v := boardViewer{user: u, role: u.Role, board: boardRole(b, u)}
	if v.board == "" {
		return Board{}, v, fmt.Errorf("%w: no such board", ErrNotFound)
	}
	if len(need) > 0 && !v.can(need...) {
		what := "change this board"
		if contains(need, BoardMember) {
			what = "change cards and lists on this board"
		}
		return b, v, fmt.Errorf("%w: you cannot %s (your role on it: %s)", ErrForbidden, what, v.board)
	}
	if b.Closed && len(need) > 0 && !(len(need) == 1 && need[0] == BoardAdmin) {
		return b, v, fmt.Errorf("%w: the board is closed; reopen it first", ErrState)
	}
	return b, v, nil
}

// ---- activity and notices ----

func (o *Ops) boardLog(ctx context.Context, tenant, actor string, b Board, card, what, text string, data map[string]string) {
	n, err := o.next(ctx, tenant, "boardact:"+b.ID, actor)
	if err != nil {
		return
	}
	a := BoardActivity{Board: b.ID, Seq: n, Card: card, By: actor, At: o.now().Format(time.RFC3339), What: what, Text: text, Data: data}
	_ = docs.Upsert(ctx, o.Docs, tenant, cBoardAct, fmt.Sprintf("%s|%012d", b.ID, n), a, actor)
}

// BoardRev is a board's change counter: the console polls it and reloads
// the board when it moves.
func (o *Ops) BoardRev(ctx context.Context, tenant, actor, id string) (int64, error) {
	if _, _, err := o.boardFor(ctx, tenant, actor, id); err != nil {
		return 0, err
	}
	s, _, err := docs.Get[seqDoc](ctx, o.Docs, tenant, cSeq, "boardact:"+id)
	if errors.Is(err, docs.ErrNotFound) {
		return 0, nil
	}
	return s.N, err
}

func (o *Ops) boardNotify(ctx context.Context, tenant, actor string, b Board, card string, users []string, text string) {
	o.boardNotifyKind(ctx, tenant, actor, b, card, NoticeOther, users, text)
}

// boardNotifyKind leaves a notice of the given kind for each person in
// users, except the one who acted.
func (o *Ops) boardNotifyKind(ctx context.Context, tenant, actor string, b Board, card, kind string, users []string, text string) {
	seen := map[string]bool{}
	for _, u := range users {
		if u == "" || u == actor || seen[u] || strings.HasPrefix(u, "system:") {
			continue
		}
		seen[u] = true
		n, err := o.next(ctx, tenant, "boardnote:"+u, actor)
		if err != nil {
			continue
		}
		id := fmt.Sprintf("%s|%012d", u, n)
		_ = docs.Upsert(ctx, o.Docs, tenant, cBoardNote, id, BoardNotice{ID: id, User: u, At: o.now().Format(time.RFC3339), Board: b.ID, Card: card, By: actor, Text: text, Kind: kind}, actor)
	}
}

// BoardNotices are a person's notifications, newest first.
func (o *Ops) BoardNotices(ctx context.Context, tenant, actor string, limit int) ([]BoardNotice, int, error) {
	if _, err := o.boardUser(ctx, tenant, actor); err != nil {
		return nil, 0, err
	}
	ns, err := docs.List[BoardNotice](ctx, o.Docs, tenant, cBoardNote, actor+"|")
	if err != nil {
		return nil, 0, err
	}
	unread := 0
	for _, n := range ns {
		if !n.Read {
			unread++
		}
	}
	sort.Slice(ns, func(i, j int) bool { return ns[i].ID > ns[j].ID })
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if len(ns) > limit {
		ns = ns[:limit]
	}
	if ns == nil {
		ns = []BoardNotice{}
	}
	return ns, unread, nil
}

// ReadBoardNotices marks a person's notices read ("" for all).
func (o *Ops) ReadBoardNotices(ctx context.Context, tenant, actor, id string) (int, error) {
	ns, _, err := o.BoardNotices(ctx, tenant, actor, 200)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, x := range ns {
		if x.Read || (id != "" && x.ID != id) {
			continue
		}
		if _, err := docs.Update(ctx, o.Docs, tenant, cBoardNote, x.ID, actor, func(d *BoardNotice) error { d.Read = true; return nil }); err == nil {
			n++
		}
	}
	return n, nil
}

// ---- boards ----

// BoardSummary is a board in the list of boards.
type BoardSummary struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Source     string `json:"source,omitempty"`
	Visibility string `json:"visibility"`
	Background string `json:"background,omitempty"`
	Template   bool   `json:"template,omitempty"`
	Closed     bool   `json:"closed,omitempty"`
	Starred    bool   `json:"starred"`
	Role       string `json:"role"`
	Lists      int    `json:"lists"`
	Members    int    `json:"members"`
	UpdatedAt  string `json:"updated_at"`
}

// Boards lists the boards a person can see.
func (o *Ops) Boards(ctx context.Context, tenant, actor string) ([]BoardSummary, error) {
	u, err := o.boardUser(ctx, tenant, actor)
	if err != nil {
		return nil, err
	}
	bs, err := docs.List[Board](ctx, o.Docs, tenant, cBoard, "")
	if err != nil {
		return nil, err
	}
	out := []BoardSummary{}
	for _, b := range bs {
		r := boardRole(b, u)
		if r == "" || b.Deleted {
			continue
		}
		lists := 0
		for _, l := range b.Lists {
			if !l.Archived {
				lists++
			}
		}
		out = append(out, BoardSummary{ID: b.ID, Name: b.Name, Kind: b.Kind, Source: b.Source, Visibility: b.Visibility, Background: b.Background, Template: b.Template,
			Closed: b.Closed, Starred: contains(b.Starred, u.ID), Role: r, Lists: lists, Members: len(b.Members), UpdatedAt: b.UpdatedAt})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Starred != out[j].Starred {
			return out[i].Starred
		}
		return out[i].UpdatedAt > out[j].UpdatedAt
	})
	return out, nil
}

// BoardInput is what a person sets on a board.
type BoardInput struct {
	Name        string        `json:"name"`
	Description *string       `json:"description,omitempty"` // nil leaves it as it is
	Visibility  string        `json:"visibility"`
	Background  string        `json:"background"`
	CoversOn    *bool         `json:"covers_on,omitempty"`
	Labels      []BoardLabel  `json:"labels,omitempty"`
	Fields      []BoardField  `json:"fields,omitempty"`
	Members     []BoardPerson `json:"members,omitempty"`
	Template    *bool         `json:"template,omitempty"`
	Lists       []string      `json:"lists,omitempty"` // a new board's lists (default To do, Doing, Done)
	Perms       *BoardPerms   `json:"perms,omitempty"` // nil leaves them as they are
}

func defaultLabels() []BoardLabel {
	var ls []BoardLabel
	for _, c := range []string{"green", "yellow", "orange", "red", "purple", "blue"} {
		ls = append(ls, BoardLabel{ID: boardID("LB-", 4), Color: c})
	}
	return ls
}

// CreateBoard makes a team board; the maker is its admin.
func (o *Ops) CreateBoard(ctx context.Context, tenant, actor string, in BoardInput) (Board, error) {
	u, err := o.boardUser(ctx, tenant, actor)
	if err != nil {
		return Board{}, err
	}
	name, err := textField("board name", in.Name, 120, true)
	if err != nil {
		return Board{}, err
	}
	desc := ""
	if in.Description != nil {
		desc = *in.Description
	}
	desc, err = textField("description", desc, 4000, false)
	if err != nil {
		return Board{}, err
	}
	vis := firstOf(in.Visibility, "firm")
	if vis != "firm" && vis != "private" {
		return Board{}, invalid("a board is firm-wide or private")
	}
	if u.Role == RoleBranch || u.Role == RoleAP {
		vis = "private" // branch and partner staff see only their own boards
	}
	now := o.now().Format(time.RFC3339)
	b := Board{ID: boardID("B-", 6), Name: name, Description: desc, Kind: BoardTeam, Visibility: vis, Members: []BoardPerson{{User: u.ID, Role: BoardAdmin}},
		Labels: defaultLabels(), Background: firstOf(in.Background, "blue"), CoversOn: true, CreatedBy: actor, CreatedAt: now, UpdatedAt: now}
	if !boardColorOK(b.Background) && !strings.HasPrefix(b.Background, "file:") {
		return Board{}, invalid("unknown background %q", b.Background)
	}
	names := in.Lists
	if len(names) == 0 {
		names = []string{"To do", "Doing", "Done"}
	}
	for i, n := range names {
		n, err := textField("list name", n, 120, true)
		if err != nil {
			return Board{}, err
		}
		b.Lists = append(b.Lists, BoardList{ID: boardID("L-", 4), Name: n, Pos: float64((i + 1) * 1024)})
	}
	for _, m := range in.Members {
		if m.User != u.ID {
			if err := o.boardMemberOK(ctx, tenant, m); err != nil {
				return Board{}, err
			}
			b.Members = append(b.Members, m)
		}
	}
	if _, err := docs.Put(ctx, o.Docs, tenant, cBoard, b.ID, b, 0, actor); err != nil {
		return Board{}, err
	}
	o.boardLog(ctx, tenant, actor, b, "", "board.created", "created this board", nil)
	o.log(ctx, tenant, actor, "board.created", b.ID, map[string]any{"name": b.Name, "visibility": b.Visibility})
	return b, nil
}

func (o *Ops) boardMemberOK(ctx context.Context, tenant string, m BoardPerson) error {
	if !contains([]string{BoardAdmin, BoardMember, BoardObserver}, m.Role) {
		return invalid("a board member is an admin, member or observer")
	}
	r, err := o.RoleOf(ctx, tenant, m.User)
	if err != nil || r == RoleClient {
		return invalid("%s is not a staff user", m.User)
	}
	return nil
}

// BoardView is a board with its open cards, for the console.
type BoardView struct {
	Board Board          `json:"board"`
	Cards []Card         `json:"cards"`
	Role  string         `json:"role"`
	Rev   int64          `json:"rev"`
	Users []BoardUserRef `json:"users"` // people who can be added or mentioned
	// For the person reading it (boardlists.go): the lists they collapsed,
	// and whether they may join the board as a member.
	Collapsed []string `json:"collapsed"`
	CanJoin   bool     `json:"can_join,omitempty"`
}

// BoardUserRef is a person shown on a board.
type BoardUserRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
}

// GetBoard is a board and its open cards (archived ones with archived).
func (o *Ops) GetBoard(ctx context.Context, tenant, actor, id string, archived bool) (BoardView, error) {
	b, v, err := o.boardFor(ctx, tenant, actor, id)
	if err != nil {
		return BoardView{}, err
	}
	for _, h := range boardReadHooks {
		if err := h(ctx, o, tenant, b); err != nil {
			o.log(ctx, tenant, actor, "board.sync_failed", b.ID, map[string]any{"error": err.Error()})
		}
	}
	if b, _, err = docs.Get[Board](ctx, o.Docs, tenant, cBoard, id); err != nil {
		return BoardView{}, err
	}
	cs, err := o.boardCards(ctx, tenant, id)
	if err != nil {
		return BoardView{}, err
	}
	out := BoardView{Board: b, Role: v.board, Cards: []Card{}}
	for _, c := range cs {
		if c.Archived == archived {
			out.Cards = append(out.Cards, c)
		}
	}
	out.Rev, _ = o.BoardRev(ctx, tenant, actor, id)
	us, _ := o.Users(ctx, tenant)
	for _, u := range us {
		if u.Active && u.Role != RoleClient && boardRole(b, u) != "" {
			out.Users = append(out.Users, BoardUserRef{ID: u.ID, Name: u.Name, Role: boardRole(b, u)})
		}
	}
	if !v.can(BoardAdmin) {
		out.Board.EmailKey = "" // the email address is for the board's admins
	}
	out.Collapsed = o.boardCollapsed(ctx, tenant, actor, b)
	out.CanJoin = boardCanJoin(b, v) == nil
	return out, nil
}

func (o *Ops) boardCards(ctx context.Context, tenant, board string) ([]Card, error) {
	all, err := docs.List[Card](ctx, o.Docs, tenant, cBoardCard, board+"|")
	if err != nil {
		return nil, err
	}
	cs := all[:0]
	for _, c := range all {
		if !c.Deleted {
			cs = append(cs, c)
		}
	}
	sort.SliceStable(cs, func(i, j int) bool {
		if cs[i].List != cs[j].List {
			return cs[i].List < cs[j].List
		}
		return cs[i].Pos < cs[j].Pos
	})
	return cs, nil
}

// UpdateBoard changes a board's settings (board admins).
func (o *Ops) UpdateBoard(ctx context.Context, tenant, actor, id string, in BoardInput) (Board, error) {
	b, v, err := o.boardFor(ctx, tenant, actor, id, BoardAdmin)
	if err != nil {
		return b, err
	}
	var changes []string
	b, err = docs.Update(ctx, o.Docs, tenant, cBoard, id, actor, func(x *Board) error {
		changes = nil
		if in.Name != "" && in.Name != x.Name {
			n, err := textField("board name", in.Name, 120, true)
			if err != nil {
				return err
			}
			x.Name = n
			changes = append(changes, "renamed it to "+n)
		}
		if in.Description != nil && *in.Description != x.Description {
			d, err := textField("description", *in.Description, 4000, false)
			if err != nil {
				return err
			}
			x.Description = d
		}
		if in.Visibility != "" && in.Visibility != x.Visibility {
			if in.Visibility != "firm" && in.Visibility != "private" {
				return invalid("a board is firm-wide or private")
			}
			if x.Kind == BoardLedgerline && in.Visibility == "private" {
				return invalid("a Ledgerline board stays firm-wide; its work belongs to the roles that do it")
			}
			if (v.role == RoleBranch || v.role == RoleAP) && in.Visibility == "firm" {
				return fmt.Errorf("%w: branch and partner staff keep their boards private", ErrForbidden)
			}
			x.Visibility = in.Visibility
			changes = append(changes, "made it "+map[string]string{"firm": "firm-wide", "private": "private"}[in.Visibility])
		}
		if in.Background != "" && in.Background != x.Background {
			if !boardColorOK(in.Background) && !strings.HasPrefix(in.Background, "file:") {
				return invalid("unknown background %q", in.Background)
			}
			x.Background = in.Background
		}
		if in.CoversOn != nil {
			x.CoversOn = *in.CoversOn
		}
		if in.Perms != nil {
			ch, err := applyBoardPerms(x, *in.Perms)
			if err != nil {
				return err
			}
			changes = append(changes, ch...)
		}
		if in.Template != nil && *in.Template != x.Template {
			if x.Kind == BoardLedgerline {
				return invalid("a Ledgerline board cannot be a template")
			}
			x.Template = *in.Template
		}
		if in.Labels != nil {
			if len(in.Labels) > 50 {
				return invalid("a board can have at most 50 labels")
			}
			for i := range in.Labels {
				l := &in.Labels[i]
				if !boardLabelColorOK(l.Color) {
					return invalid("unknown label colour %q", l.Color)
				}
				n, err := textField("label name", l.Name, 60, false)
				if err != nil {
					return err
				}
				l.Name = n
				if l.ID == "" {
					l.ID = boardID("LB-", 4)
				}
			}
			x.Labels = in.Labels
		}
		if in.Fields != nil {
			if len(in.Fields) > 50 {
				return invalid("a board can have at most 50 custom fields")
			}
			for i := range in.Fields {
				f := &in.Fields[i]
				n, err := textField("field name", f.Name, 60, true)
				if err != nil {
					return err
				}
				f.Name = n
				if !contains([]string{"text", "number", "date", "checkbox", "list"}, f.Type) {
					return invalid("a custom field is text, number, date, checkbox or list")
				}
				if f.Type == "list" && len(f.Options) == 0 {
					return invalid("give the options of %s", f.Name)
				}
				if f.ID == "" {
					f.ID = boardID("F-", 4)
				}
			}
			x.Fields = in.Fields
		}
		if in.Members != nil {
			admins := 0
			for _, m := range in.Members {
				if err := o.boardMemberOK(ctx, tenant, m); err != nil {
					return err
				}
				if m.Role == BoardAdmin {
					admins++
				}
			}
			if admins == 0 && x.Kind == BoardTeam {
				return invalid("a board needs at least one admin")
			}
			old := map[string]bool{}
			for _, m := range x.Members {
				old[m.User] = true
			}
			var added []string
			for _, m := range in.Members {
				if !old[m.User] {
					added = append(added, m.User)
				}
			}
			x.Members = in.Members
			if len(added) > 0 {
				changes = append(changes, "added "+strings.Join(added, ", ")+" to the board")
				defer o.boardNotifyKind(ctx, tenant, actor, *x, "", NoticeAdded, added, fmt.Sprintf("%s added you to the board %s", actor, x.Name))
			}
		}
		x.UpdatedAt = o.now().Format(time.RFC3339)
		return nil
	})
	if err != nil {
		return b, err
	}
	for _, c := range changes {
		o.boardLog(ctx, tenant, actor, b, "", "board.updated", c, nil)
	}
	if len(changes) == 0 {
		o.boardLog(ctx, tenant, actor, b, "", "board.updated", "changed the board's settings", nil)
	}
	return b, nil
}

// SetBoardClosed closes or reopens a board (board admins).
func (o *Ops) SetBoardClosed(ctx context.Context, tenant, actor, id string, closed bool) (Board, error) {
	if _, _, err := o.boardFor(ctx, tenant, actor, id, BoardAdmin); err != nil {
		return Board{}, err
	}
	b, err := docs.Update(ctx, o.Docs, tenant, cBoard, id, actor, func(x *Board) error {
		x.Closed, x.UpdatedAt = closed, o.now().Format(time.RFC3339)
		return nil
	})
	if err == nil {
		o.boardLog(ctx, tenant, actor, b, "", "board.closed", map[bool]string{true: "closed this board", false: "reopened this board"}[closed], nil)
	}
	return b, err
}

// DeleteBoard deletes a closed team board and its cards (board admins).
func (o *Ops) DeleteBoard(ctx context.Context, tenant, actor, id string) error {
	b, _, err := o.boardFor(ctx, tenant, actor, id, BoardAdmin)
	if err != nil {
		return err
	}
	if !b.Closed {
		return invalid("close the board before deleting it")
	}
	if b.Kind == BoardLedgerline {
		return invalid("a Ledgerline board is closed, not deleted")
	}
	cs, _ := o.boardCards(ctx, tenant, id)
	for _, c := range cs {
		_, _ = docs.Update(ctx, o.Docs, tenant, cBoardCard, cardKey(id, c.ID), actor, func(x *Card) error { x.Deleted = true; return nil })
	}
	if _, err := docs.Update(ctx, o.Docs, tenant, cBoard, id, actor, func(x *Board) error { x.Deleted = true; return nil }); err != nil {
		return err
	}
	o.log(ctx, tenant, actor, "board.deleted", id, map[string]any{"name": b.Name, "cards": len(cs)})
	return nil
}

// StarBoard stars or unstars a board for the person.
func (o *Ops) StarBoard(ctx context.Context, tenant, actor, id string, on bool) error {
	if _, _, err := o.boardFor(ctx, tenant, actor, id); err != nil {
		return err
	}
	_, err := docs.Update(ctx, o.Docs, tenant, cBoard, id, actor, func(x *Board) error {
		x.Starred = toggleIn(x.Starred, actor, on)
		return nil
	})
	return err
}

func toggleIn(xs []string, x string, on bool) []string {
	var out []string
	for _, y := range xs {
		if y != x {
			out = append(out, y)
		}
	}
	if on {
		out = append(out, x)
	}
	return out
}

// CopyBoard copies a board's lists, labels and fields, and its cards when
// asked (templates make boards this way).
func (o *Ops) CopyBoard(ctx context.Context, tenant, actor, id, name string, withCards bool) (Board, error) {
	src, _, err := o.boardFor(ctx, tenant, actor, id)
	if err != nil {
		return Board{}, err
	}
	if src.Kind == BoardLedgerline {
		return Board{}, invalid("a Ledgerline board's cards come from its records; make a team board instead")
	}
	var names []string
	var lists []BoardList
	for _, l := range src.Lists {
		if !l.Archived {
			names = append(names, l.Name)
			lists = append(lists, l)
		}
	}
	b, err := o.CreateBoard(ctx, tenant, actor, BoardInput{Name: firstOf(name, src.Name+" (copy)"), Description: &src.Description, Visibility: src.Visibility, Background: src.Background, Lists: names})
	if err != nil {
		return Board{}, err
	}
	listMap, labelMap := map[string]string{}, map[string]string{}
	b, err = docs.Update(ctx, o.Docs, tenant, cBoard, b.ID, actor, func(x *Board) error {
		for i := range x.Lists {
			listMap[lists[i].ID] = x.Lists[i].ID
			x.Lists[i].Color, x.Lists[i].Limit = lists[i].Color, lists[i].Limit
		}
		x.Labels = nil
		for _, l := range src.Labels {
			nl := BoardLabel{ID: boardID("LB-", 4), Name: l.Name, Color: l.Color}
			labelMap[l.ID] = nl.ID
			x.Labels = append(x.Labels, nl)
		}
		x.Fields = append([]BoardField(nil), src.Fields...)
		x.CoversOn = src.CoversOn
		return nil
	})
	if err != nil || !withCards {
		return b, err
	}
	cs, _ := o.boardCards(ctx, tenant, src.ID)
	for _, c := range cs {
		to, ok := listMap[c.List]
		if c.Archived || !ok {
			continue
		}
		var labels []string
		for _, l := range c.Labels {
			if nl, ok := labelMap[l]; ok {
				labels = append(labels, nl)
			}
		}
		nc := Card{Title: c.Title, Description: c.Description, Labels: labels, Checklists: copyChecklists(c.Checklists, true), Fields: copyMap(c.Fields), Cover: c.Cover, Location: c.Location, Template: c.Template}
		if _, err := o.addCard(ctx, tenant, actor, b, to, nc, math.NaN()); err != nil {
			return b, err
		}
	}
	return b, nil
}

func copyChecklists(cl []Checklist, reset bool) []Checklist {
	var out []Checklist
	for _, c := range cl {
		n := Checklist{ID: boardID("CL-", 4), Name: c.Name}
		for _, it := range c.Items {
			ni := CheckItem{ID: boardID("I-", 4), Text: it.Text, Member: it.Member, Due: it.Due, Done: it.Done && !reset}
			n.Items = append(n.Items, ni)
		}
		out = append(out, n)
	}
	return out
}

func copyMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// ---- lists ----

// ListInput changes a list.
type ListInput struct {
	Name     *string  `json:"name,omitempty"`
	Pos      *float64 `json:"pos,omitempty"`
	Color    *string  `json:"color,omitempty"`
	Limit    *int     `json:"limit,omitempty"`
	Archived *bool    `json:"archived,omitempty"`
}

// AddList adds a list at pos (NaN: at the end).
func (o *Ops) AddList(ctx context.Context, tenant, actor, board, name string, pos float64) (BoardList, error) {
	b, _, err := o.boardFor(ctx, tenant, actor, board, BoardAdmin, BoardMember)
	if err != nil {
		return BoardList{}, err
	}
	if b.Kind == BoardLedgerline {
		return BoardList{}, invalid("a Ledgerline board's lists are its records' stages")
	}
	n, err := textField("list name", name, 120, true)
	if err != nil {
		return BoardList{}, err
	}
	l := BoardList{ID: boardID("L-", 4), Name: n}
	b, err = docs.Update(ctx, o.Docs, tenant, cBoard, board, actor, func(x *Board) error {
		if len(x.Lists) >= 100 {
			return invalid("a board can have at most 100 lists")
		}
		l.Pos = pos
		if math.IsNaN(pos) {
			l.Pos = 1024
			for _, y := range x.Lists {
				l.Pos = math.Max(l.Pos, y.Pos+1024)
			}
		}
		x.Lists = append(x.Lists, l)
		x.UpdatedAt = o.now().Format(time.RFC3339)
		return nil
	})
	if err == nil {
		o.boardLog(ctx, tenant, actor, b, "", "list.created", "added the list "+l.Name, map[string]string{"list": l.ID})
	}
	return l, err
}

// UpdateList renames, moves, colours, limits, archives or restores a list.
func (o *Ops) UpdateList(ctx context.Context, tenant, actor, board, list string, in ListInput) (BoardList, error) {
	b, _, err := o.boardFor(ctx, tenant, actor, board, BoardAdmin, BoardMember)
	if err != nil {
		return BoardList{}, err
	}
	var out BoardList
	var what string
	b, err = docs.Update(ctx, o.Docs, tenant, cBoard, board, actor, func(x *Board) error {
		for i := range x.Lists {
			l := &x.Lists[i]
			if l.ID != list {
				continue
			}
			if in.Name != nil {
				if x.Kind == BoardLedgerline {
					return invalid("a Ledgerline board's lists are its records' stages")
				}
				n, err := textField("list name", *in.Name, 120, true)
				if err != nil {
					return err
				}
				what = "renamed the list " + l.Name + " to " + n
				l.Name = n
			}
			if in.Pos != nil {
				l.Pos = *in.Pos
				what = firstOf(what, "moved the list "+l.Name)
			}
			if in.Color != nil {
				if !boardColorOK(*in.Color) {
					return invalid("unknown colour %q", *in.Color)
				}
				l.Color = *in.Color
			}
			if in.Limit != nil {
				if *in.Limit < 0 || *in.Limit > 1000 {
					return invalid("a list limit is 0 (none) to 1000 cards")
				}
				l.Limit = *in.Limit
			}
			if in.Archived != nil {
				if x.Kind == BoardLedgerline && *in.Archived {
					return invalid("a Ledgerline board's lists are its records' stages")
				}
				if l.MovedTo != "" && !*in.Archived {
					return invalid("the list %s moved to another board; only its archived cards stayed here", l.Name)
				}
				l.Archived = *in.Archived
				what = map[bool]string{true: "archived the list ", false: "restored the list "}[l.Archived] + l.Name
			}
			out = *l
			x.UpdatedAt = o.now().Format(time.RFC3339)
			return nil
		}
		return fmt.Errorf("%w: no such list", ErrNotFound)
	})
	if err == nil {
		o.boardLog(ctx, tenant, actor, b, "", "list.updated", firstOf(what, "changed the list "+out.Name), map[string]string{"list": list})
	}
	return out, err
}

// MoveListCards moves every open card of a list to another list.
func (o *Ops) MoveListCards(ctx context.Context, tenant, actor, board, from, to string) (int, error) {
	b, _, err := o.boardFor(ctx, tenant, actor, board, BoardAdmin, BoardMember)
	if err != nil {
		return 0, err
	}
	cs, _ := o.boardCards(ctx, tenant, board)
	n := 0
	for _, c := range cs {
		if c.List == from && !c.Archived {
			if _, err := o.MoveCard(ctx, tenant, actor, board, c.ID, CardMove{List: to}); err != nil {
				return n, err
			}
			n++
		}
	}
	_ = b
	return n, nil
}

// ArchiveListCards archives every open card of a list.
func (o *Ops) ArchiveListCards(ctx context.Context, tenant, actor, board, list string) (int, error) {
	if _, _, err := o.boardFor(ctx, tenant, actor, board, BoardAdmin, BoardMember); err != nil {
		return 0, err
	}
	cs, _ := o.boardCards(ctx, tenant, board)
	n := 0
	t := true
	for _, c := range cs {
		if c.List == list && !c.Archived {
			if _, err := o.UpdateCard(ctx, tenant, actor, board, c.ID, CardPatch{Archived: &t}); err != nil {
				return n, err
			}
			n++
		}
	}
	return n, nil
}

// SortList orders a list's cards by due date, title, number or newest.
func (o *Ops) SortList(ctx context.Context, tenant, actor, board, list, by string) error {
	b, _, err := o.boardFor(ctx, tenant, actor, board, BoardAdmin, BoardMember)
	if err != nil {
		return err
	}
	cs, _ := o.boardCards(ctx, tenant, board)
	var in []Card
	for _, c := range cs {
		if c.List == list && !c.Archived {
			in = append(in, c)
		}
	}
	less := map[string]func(a, b Card) bool{
		"due":    func(a, b Card) bool { return firstOf(a.Due, "9999") < firstOf(b.Due, "9999") },
		"title":  func(a, b Card) bool { return strings.ToLower(a.Title) < strings.ToLower(b.Title) },
		"newest": func(a, b Card) bool { return a.No > b.No },
		"oldest": func(a, b Card) bool { return a.No < b.No },
	}[by]
	if less == nil {
		return invalid("sort by due, title, newest or oldest")
	}
	sort.SliceStable(in, func(i, j int) bool { return less(in[i], in[j]) })
	for i, c := range in {
		p := float64((i + 1) * 1024)
		_, _ = docs.Update(ctx, o.Docs, tenant, cBoardCard, cardKey(board, c.ID), actor, func(x *Card) error { x.Pos = p; return nil })
	}
	o.boardLog(ctx, tenant, actor, b, "", "list.sorted", "sorted a list by "+by, map[string]string{"list": list})
	return nil
}

// ---- cards ----

func boardList(b Board, id string) (BoardList, bool) {
	for _, l := range b.Lists {
		if l.ID == id {
			return l, true
		}
	}
	return BoardList{}, false
}

func boardLabel(b Board, id string) (BoardLabel, bool) {
	for _, l := range b.Labels {
		if l.ID == id {
			return l, true
		}
	}
	return BoardLabel{}, false
}

// CardInput is a new card.
type CardInput struct {
	List        string            `json:"list"`
	Title       string            `json:"title"`
	Description string            `json:"description,omitempty"`
	Pos         *float64          `json:"pos,omitempty"` // nil: at the bottom; 0 or less: at the top
	Labels      []string          `json:"labels,omitempty"`
	Members     []string          `json:"members,omitempty"`
	Due         string            `json:"due,omitempty"`
	Fields      map[string]string `json:"fields,omitempty"`
	Link        *CardLink         `json:"link,omitempty"`
	FromCard    string            `json:"from_card,omitempty"` // a card template on this board
}

// AddCard adds a card to a list.
func (o *Ops) AddCard(ctx context.Context, tenant, actor, board string, in CardInput) (Card, error) {
	b, _, err := o.boardFor(ctx, tenant, actor, board, BoardAdmin, BoardMember)
	if err != nil {
		return Card{}, err
	}
	c := Card{Title: in.Title, Description: in.Description, Labels: in.Labels, Members: in.Members, Due: in.Due, Fields: in.Fields, Link: in.Link}
	if in.FromCard != "" {
		t, _, err := docs.Get[Card](ctx, o.Docs, tenant, cBoardCard, cardKey(board, in.FromCard))
		if err != nil {
			return Card{}, fmt.Errorf("%w: no such card template", ErrNotFound)
		}
		c = Card{Title: firstOf(strings.TrimSpace(in.Title), t.Title), Description: t.Description, Labels: t.Labels, Members: t.Members, Checklists: copyChecklists(t.Checklists, true),
			Fields: copyMap(t.Fields), Cover: t.Cover, Location: t.Location}
	}
	// Members are staff, as UpdateCard checks.
	c.Members = nonEmptyUniq(c.Members)
	for _, m := range c.Members {
		if r, err := o.RoleOf(ctx, tenant, m); err != nil || r == RoleClient {
			return Card{}, invalid("%s is not a staff user", m)
		}
	}
	pos := math.NaN()
	if in.Pos != nil {
		pos = *in.Pos
	}
	return o.addCard(ctx, tenant, actor, b, in.List, c, pos)
}

// addCard writes a new card (callers checked the person may).
func (o *Ops) addCard(ctx context.Context, tenant, actor string, b Board, list string, c Card, pos float64) (Card, error) {
	return o.addCardOpt(ctx, tenant, actor, b, list, c, pos, false)
}

// addCardOpt is addCard; quiet leaves the notices (to the card's members,
// and to the people watching the list or board) to the caller, which tells
// them once about many cards.
func (o *Ops) addCardOpt(ctx context.Context, tenant, actor string, b Board, list string, c Card, pos float64, quiet bool) (Card, error) {
	l, ok := boardList(b, list)
	if !ok || l.Archived {
		return Card{}, invalid("choose an open list for the card")
	}
	var err error
	if c.Title, err = textField("card title", c.Title, 500, true); err != nil {
		return Card{}, err
	}
	if c.Description, err = textField("description", c.Description, 20000, false); err != nil {
		return Card{}, err
	}
	if c.Due, err = boardDate(c.Due); err != nil {
		return Card{}, err
	}
	if c.Start, err = boardDate(c.Start); err != nil {
		return Card{}, err
	}
	for _, id := range c.Labels {
		if _, ok := boardLabel(b, id); !ok {
			return Card{}, invalid("no such label on this board")
		}
	}
	b, err = docs.Update(ctx, o.Docs, tenant, cBoard, b.ID, actor, func(x *Board) error { x.CardSeq++; x.UpdatedAt = o.now().Format(time.RFC3339); return nil })
	if err != nil {
		return Card{}, err
	}
	cs, _ := o.boardCards(ctx, tenant, b.ID)
	top, bottom := math.Inf(1), math.Inf(-1)
	for _, x := range cs {
		if x.List == list && !x.Archived {
			top, bottom = math.Min(top, x.Pos), math.Max(bottom, x.Pos)
		}
	}
	switch {
	case math.IsNaN(pos) && math.IsInf(bottom, -1):
		pos = 1024
	case math.IsNaN(pos):
		pos = bottom + 1024
	case pos <= 0 && math.IsInf(top, 1):
		pos = 1024
	case pos <= 0:
		pos = top / 2
	}
	now := o.now().Format(time.RFC3339)
	c.Board, c.ID, c.No, c.List, c.Pos = b.ID, boardID("C-", 6), b.CardSeq, list, pos
	c.CreatedBy, c.CreatedAt, c.UpdatedAt = actor, now, now
	if !strings.HasPrefix(actor, "system:") && !contains(c.Watchers, actor) {
		c.Watchers = append(c.Watchers, actor)
	}
	if _, err := docs.Put(ctx, o.Docs, tenant, cBoardCard, cardKey(b.ID, c.ID), c, 0, actor); err != nil {
		return Card{}, err
	}
	o.boardLog(ctx, tenant, actor, b, c.ID, "card.created", fmt.Sprintf("added #%d %s to %s", c.No, clip(c.Title, 80), l.Name), map[string]string{"list": list})
	if len(c.Members) > 0 && !quiet {
		o.boardNotifyKind(ctx, tenant, actor, b, c.ID, NoticeAdded, c.Members, fmt.Sprintf("%s added you to the card %s on %s", actor, clip(c.Title, 80), b.Name))
	}
	if !quiet {
		o.boardWatchNotify(ctx, tenant, actor, b, c.ID, list, c.Members, fmt.Sprintf("%s added %s to %s on %s", actor, clip(c.Title, 80), l.Name, b.Name))
	}
	o.boardEvent(ctx, tenant, BoardEvent{Board: b.ID, Card: c.ID, Kind: "card.created", Actor: actor, Data: map[string]string{"list": list}})
	return c, nil
}

// GetCard is one card (open or archived) with its board.
func (o *Ops) GetCard(ctx context.Context, tenant, actor, board, card string) (Card, Board, error) {
	b, _, err := o.boardFor(ctx, tenant, actor, board)
	if err != nil {
		return Card{}, Board{}, err
	}
	c, _, err := docs.Get[Card](ctx, o.Docs, tenant, cBoardCard, cardKey(board, card))
	if err != nil || (c.Deleted && c.MovedTo == "") {
		return Card{}, b, fmt.Errorf("%w: no such card", ErrNotFound)
	}
	if c.Deleted {
		to, id, _ := strings.Cut(c.MovedTo, "|")
		return Card{}, b, &CardMovedError{Board: to, Card: id}
	}
	return c, b, nil
}

// CardMovedError says a card went to another board, and where, so old
// links can follow it. It is a not-found error.
type CardMovedError struct{ Board, Card string }

func (e *CardMovedError) Error() string {
	return fmt.Sprintf("%v: the card moved to another board (%s|%s)", ErrNotFound, e.Board, e.Card)
}
func (e *CardMovedError) Unwrap() error { return ErrNotFound }

// CardPatch changes some of a card's fields; nil leaves a field as it is.
type CardPatch struct {
	Title       *string            `json:"title,omitempty"`
	Description *string            `json:"description,omitempty"`
	Labels      *[]string          `json:"labels,omitempty"`
	Members     *[]string          `json:"members,omitempty"`
	Start       *string            `json:"start,omitempty"`
	Due         *string            `json:"due,omitempty"`
	DueDone     *bool              `json:"due_done,omitempty"`
	Reminder    *int               `json:"reminder,omitempty"`
	Cover       *CardCover         `json:"cover,omitempty"` // an empty cover removes it
	Fields      *map[string]string `json:"fields,omitempty"`
	Location    *string            `json:"location,omitempty"`
	Link        *CardLink          `json:"link,omitempty"` // an empty Kind removes it
	Template    *bool              `json:"template,omitempty"`
	Archived    *bool              `json:"archived,omitempty"`
}

// UpdateCard applies a patch to a card.
func (o *Ops) UpdateCard(ctx context.Context, tenant, actor, board, card string, p CardPatch) (Card, error) {
	b, _, err := o.boardFor(ctx, tenant, actor, board, BoardAdmin, BoardMember)
	if err != nil {
		return Card{}, err
	}
	var logs [][2]string
	var events []BoardEvent
	var newMembers []string
	var watchNews []string // for the people watching the board
	ev := func(kind string, data map[string]string) {
		events = append(events, BoardEvent{Board: board, Card: card, Kind: kind, Actor: actor, Data: data})
	}
	c, err := docs.Update(ctx, o.Docs, tenant, cBoardCard, cardKey(board, card), actor, func(x *Card) error {
		if x.Deleted {
			return errCardGone
		}
		logs, events, newMembers, watchNews = nil, nil, nil, nil
		if p.Title != nil {
			t, err := textField("card title", *p.Title, 500, true)
			if err != nil {
				return err
			}
			if t != x.Title {
				logs = append(logs, [2]string{"card.renamed", "renamed #" + fmt.Sprint(x.No) + " to " + clip(t, 80)})
				x.Title = t
			}
		}
		if p.Description != nil {
			d, err := textField("description", *p.Description, 20000, false)
			if err != nil {
				return err
			}
			if d != x.Description {
				logs = append(logs, [2]string{"card.described", "changed the description of #" + fmt.Sprint(x.No)})
				x.Description = d
			}
		}
		if p.Labels != nil {
			for _, id := range *p.Labels {
				if _, ok := boardLabel(b, id); !ok {
					return invalid("no such label on this board")
				}
			}
			for _, id := range *p.Labels {
				if !contains(x.Labels, id) {
					l, _ := boardLabel(b, id)
					logs = append(logs, [2]string{"card.label_added", fmt.Sprintf("added the %s label %s to #%d", labelColorName(l.Color), l.Name, x.No)})
					ev("card.label_added", map[string]string{"label": id})
				}
			}
			for _, id := range x.Labels {
				if !contains(*p.Labels, id) {
					ev("card.label_removed", map[string]string{"label": id})
				}
			}
			x.Labels = nonEmptyUniq(*p.Labels)
		}
		if p.Members != nil {
			for _, m := range *p.Members {
				r, err := o.RoleOf(ctx, tenant, m)
				if err != nil || r == RoleClient {
					return invalid("%s is not a staff user", m)
				}
				if !contains(x.Members, m) {
					newMembers = append(newMembers, m)
					logs = append(logs, [2]string{"card.member_added", fmt.Sprintf("added %s to #%d", m, x.No)})
					ev("card.member_added", map[string]string{"member": m})
				}
			}
			x.Members = nonEmptyUniq(*p.Members)
			for _, m := range newMembers {
				if !contains(x.Watchers, m) {
					x.Watchers = append(x.Watchers, m)
				}
			}
		}
		if p.Start != nil {
			s, err := boardDate(*p.Start)
			if err != nil {
				return err
			}
			x.Start = s
		}
		if p.Due != nil {
			d, err := boardDate(*p.Due)
			if err != nil {
				return err
			}
			if d != x.Due {
				x.Due, x.Reminded = d, ""
				if d == "" {
					logs = append(logs, [2]string{"card.due_removed", fmt.Sprintf("removed the due date of #%d", x.No)})
					watchNews = append(watchNews, fmt.Sprintf("%s removed the due date of %s on %s", actor, clip(x.Title, 80), b.Name))
				} else {
					logs = append(logs, [2]string{"card.due_set", fmt.Sprintf("set #%d due %s", x.No, d)})
					ev("card.due_set", map[string]string{"due": d})
					watchNews = append(watchNews, fmt.Sprintf("%s set %s due %s on %s", actor, clip(x.Title, 80), boardDateText(d), b.Name))
				}
			}
		}
		if x.Start != "" && x.Due != "" && x.Start > x.Due {
			return invalid("the start date is after the due date")
		}
		if p.DueDone != nil && *p.DueDone != x.DueDone {
			x.DueDone = *p.DueDone
			if x.DueDone {
				logs = append(logs, [2]string{"card.due_done", fmt.Sprintf("marked #%d complete", x.No)})
				ev("card.due_done", nil)
				watchNews = append(watchNews, fmt.Sprintf("%s marked %s complete on %s", actor, clip(x.Title, 80), b.Name))
			}
		}
		if p.Reminder != nil {
			if *p.Reminder < 0 || *p.Reminder > 60*24*14 {
				return invalid("a reminder is up to 14 days before the due date")
			}
			x.Reminder, x.Reminded = *p.Reminder, ""
		}
		if p.Cover != nil {
			if p.Cover.Color == "" && p.Cover.File == "" {
				x.Cover = nil
			} else {
				if !boardColorOK(p.Cover.Color) {
					return invalid("unknown cover colour %q", p.Cover.Color)
				}
				if p.Cover.File != "" && !hasAttachment(*x, p.Cover.File) {
					return invalid("a cover image is one of the card's own images")
				}
				cv := *p.Cover
				x.Cover = &cv
			}
		}
		if p.Fields != nil {
			vals := map[string]string{}
			for id, v := range *p.Fields {
				f, ok := boardField(b, id)
				if !ok {
					return invalid("no such custom field on this board")
				}
				v, err := fieldValue(f, v)
				if err != nil {
					return err
				}
				if v != "" {
					vals[id] = v
				}
			}
			x.Fields = vals
		}
		if p.Location != nil {
			l, err := textField("location", *p.Location, 300, false)
			if err != nil {
				return err
			}
			x.Location = l
		}
		if p.Link != nil {
			if p.Link.Kind == "" {
				x.Link = nil
			} else {
				lk := *p.Link
				x.Link = &lk
				logs = append(logs, [2]string{"card.linked", fmt.Sprintf("linked #%d to %s", x.No, lk.Label)})
			}
		}
		if p.Template != nil {
			x.Template = *p.Template
		}
		if p.Archived != nil && *p.Archived != x.Archived {
			if l, ok := boardList(b, x.List); !*p.Archived && (!ok || l.Archived) {
				if l.MovedTo != "" {
					return invalid("the list %s moved to another board; move the card to an open list instead", l.Name)
				}
				return invalid("the list %s is archived; restore the list first, or move the card to an open list", firstOf(l.Name, "it was in"))
			}
			x.Archived = *p.Archived
			if x.Archived {
				logs = append(logs, [2]string{"card.archived", fmt.Sprintf("archived #%d", x.No)})
				ev("card.archived", nil)
			} else {
				logs = append(logs, [2]string{"card.restored", fmt.Sprintf("restored #%d", x.No)})
			}
		}
		x.UpdatedAt = o.now().Format(time.RFC3339)
		return nil
	})
	if err != nil {
		if errors.Is(err, docs.ErrNotFound) {
			return c, fmt.Errorf("%w: no such card", ErrNotFound)
		}
		return c, err
	}
	for _, l := range logs {
		o.boardLog(ctx, tenant, actor, b, card, l[0], l[1], nil)
	}
	if len(newMembers) > 0 {
		o.boardNotifyKind(ctx, tenant, actor, b, card, NoticeAdded, newMembers, fmt.Sprintf("%s added you to the card %s on %s", actor, clip(c.Title, 80), b.Name))
	}
	for _, t := range watchNews {
		o.boardWatchNotify(ctx, tenant, actor, b, card, "", newMembers, t)
	}
	if len(events) == 0 && len(logs) == 0 {
		ev("card.updated", nil)
	}
	for _, e := range events {
		o.boardEvent(ctx, tenant, e)
	}
	return c, nil
}

func nonEmptyUniq(xs []string) []string {
	var out []string
	for _, x := range xs {
		if x != "" && !contains(out, x) {
			out = append(out, x)
		}
	}
	return out
}

func hasAttachment(c Card, file string) bool {
	for _, a := range c.Attachments {
		if a.File == file && a.Image {
			return true
		}
	}
	return false
}

func boardField(b Board, id string) (BoardField, bool) {
	for _, f := range b.Fields {
		if f.ID == id {
			return f, true
		}
	}
	return BoardField{}, false
}

var numRe = regexp.MustCompile(`^-?\d+(\.\d+)?$`)

func fieldValue(f BoardField, v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", nil
	}
	switch f.Type {
	case "number":
		if !numRe.MatchString(v) {
			return "", invalid("%s is a number", f.Name)
		}
	case "date":
		d, err := boardDate(v)
		if err != nil {
			return "", invalid("%s is a date (YYYY-MM-DD)", f.Name)
		}
		v = d
	case "checkbox":
		if v != "true" && v != "false" {
			return "", invalid("%s is ticked or not", f.Name)
		}
		if v == "false" {
			return "", nil
		}
	case "list":
		if !contains(f.Options, v) {
			return "", invalid("%s is one of %s", f.Name, strings.Join(f.Options, ", "))
		}
	default:
		if utf8.RuneCountInString(v) > 500 {
			return "", invalid("%s is at most 500 characters", f.Name)
		}
	}
	return v, nil
}

// CardMove is where a card goes: a list (and board), and a position.
type CardMove struct {
	Board string   `json:"board,omitempty"` // another board ("": this one)
	List  string   `json:"list"`
	Pos   *float64 `json:"pos,omitempty"` // nil: at the bottom
}

// MoveCard moves a card within its board or to another board.
func (o *Ops) MoveCard(ctx context.Context, tenant, actor, board, card string, m CardMove) (Card, error) {
	b, _, err := o.boardFor(ctx, tenant, actor, board, BoardAdmin, BoardMember)
	if err != nil {
		return Card{}, err
	}
	c, _, err := docs.Get[Card](ctx, o.Docs, tenant, cBoardCard, cardKey(board, card))
	if err != nil || c.Deleted {
		return Card{}, errCardGone
	}
	if m.Board != "" && m.Board != board {
		return o.moveCardToBoard(ctx, tenant, actor, b, c, m)
	}
	to, ok := boardList(b, m.List)
	if !ok || to.Archived {
		return Card{}, invalid("choose an open list")
	}
	from, _ := boardList(b, c.List)
	if c.List != m.List && c.Link != nil {
		handled := false
		for _, h := range boardMoveHooks {
			ok, err := h(ctx, o, tenant, actor, b, c, to)
			if err != nil {
				return c, err
			}
			handled = handled || ok
		}
		if !handled && b.Kind == BoardLedgerline {
			return c, invalid("%s moves when its record changes: open it in Ledgerline", c.Link.Label)
		}
	}
	pos := math.NaN()
	if m.Pos != nil {
		pos = *m.Pos
	}
	if math.IsNaN(pos) {
		cs, _ := o.boardCards(ctx, tenant, board)
		pos = 1024
		for _, x := range cs {
			if x.List == m.List && x.ID != card && !x.Archived {
				pos = math.Max(pos, x.Pos+1024)
			}
		}
	}
	moved := c.List != m.List
	c, err = docs.Update(ctx, o.Docs, tenant, cBoardCard, cardKey(board, card), actor, func(x *Card) error {
		if x.Deleted {
			return errCardGone
		}
		x.List, x.Pos, x.UpdatedAt = m.List, pos, o.now().Format(time.RFC3339)
		return nil
	})
	if err != nil {
		return c, err
	}
	if moved {
		o.boardLog(ctx, tenant, actor, b, card, "card.moved", fmt.Sprintf("moved #%d from %s to %s", c.No, from.Name, to.Name), map[string]string{"from_list": from.ID, "to_list": to.ID})
		o.boardNotifyKind(ctx, tenant, actor, b, card, NoticeMoved, c.Watchers, fmt.Sprintf("%s moved %s to %s on %s", actor, clip(c.Title, 80), to.Name, b.Name))
		o.boardWatchNotify(ctx, tenant, actor, b, card, to.ID, c.Watchers, fmt.Sprintf("%s moved %s to %s on %s", actor, clip(c.Title, 80), to.Name, b.Name))
		o.boardEvent(ctx, tenant, BoardEvent{Board: board, Card: card, Kind: "card.moved", Actor: actor, Data: map[string]string{"from_list": from.ID, "to_list": to.ID}})
	} else {
		o.boardLog(ctx, tenant, actor, b, card, "card.reordered", fmt.Sprintf("moved #%d within %s", c.No, to.Name), nil)
	}
	return c, nil
}

func (o *Ops) moveCardToBoard(ctx context.Context, tenant, actor string, from Board, c Card, m CardMove) (Card, error) {
	to, _, err := o.boardFor(ctx, tenant, actor, m.Board, BoardAdmin, BoardMember)
	if err != nil {
		return Card{}, err
	}
	if from.Kind == BoardLedgerline || to.Kind == BoardLedgerline {
		return Card{}, invalid("a card moves between team boards only; mirror or copy it instead")
	}
	pos := math.NaN()
	if m.Pos != nil {
		pos = *m.Pos
	}
	moved, err := o.cardOntoBoard(ctx, tenant, actor, from, c, to, m.List, pos, cardMoveOpts{})
	if err != nil {
		return moved, err
	}
	o.boardLog(ctx, tenant, actor, from, "", "card.moved_out", fmt.Sprintf("moved #%d %s to the board %s", c.No, clip(c.Title, 80), to.Name), nil)
	l, _ := boardList(to, m.List)
	text := fmt.Sprintf("%s moved %s to %s on %s", actor, clip(c.Title, 80), l.Name, to.Name)
	o.boardNotifyKind(ctx, tenant, actor, to, moved.ID, NoticeMoved, c.Watchers, text)
	o.boardWatchNotify(ctx, tenant, actor, to, moved.ID, m.List, c.Watchers, text)
	return moved, nil
}

// cardMoveOpts are what goes with a card to another board.
type cardMoveOpts struct {
	labels, fields map[string]string // this board's label and field IDs to the other's (nil: they stay behind)
	people         map[string]bool   // who can see the other board (nil: every member and watcher goes)
}

// cardOntoBoard moves a card to a list on another team board: a new card
// there, with that board's next number, keeps the comments, attachments,
// checklists, members and watchers; the old one becomes a tombstone that
// points at it, so old links still find it. It tells no one; callers do.
func (o *Ops) cardOntoBoard(ctx context.Context, tenant, actor string, from Board, c Card, to Board, list string, pos float64, opt cardMoveOpts) (Card, error) {
	nc := c
	nc.Labels, nc.Fields = nil, nil // labels and fields belong to their board
	for _, id := range c.Labels {
		if nl, ok := opt.labels[id]; ok {
			nc.Labels = append(nc.Labels, nl)
		}
	}
	for id, v := range c.Fields {
		if nf, ok := opt.fields[id]; ok {
			if nc.Fields == nil {
				nc.Fields = map[string]string{}
			}
			nc.Fields[nf] = v
		}
	}
	people := func(xs []string) []string {
		if opt.people == nil {
			return xs
		}
		var out []string
		for _, x := range xs {
			if opt.people[x] {
				out = append(out, x)
			}
		}
		return out
	}
	nc.Members, nc.Watchers = people(c.Members), people(c.Watchers)
	nc.Comments = append([]CardComment(nil), c.Comments...)
	moved, err := o.addCardOpt(ctx, tenant, actor, to, list, nc, pos, true)
	if err != nil {
		return Card{}, err
	}
	// The attachments' files belong to the old board: point them at the new one.
	for _, a := range c.Attachments {
		if a.File != "" {
			_, _ = docs.Update(ctx, o.Docs, tenant, cBoardFile, a.File, actor, func(f *boardFileDoc) error { f.Board = to.ID; return nil })
		}
	}
	_, _ = docs.Update(ctx, o.Docs, tenant, cBoardCard, cardKey(to.ID, moved.ID), actor, func(x *Card) error {
		if x.Deleted {
			return errCardGone
		}
		x.Attachments, x.Comments, x.Checklists, x.Members, x.Watchers, x.Cover, x.CreatedBy, x.CreatedAt = c.Attachments, c.Comments, c.Checklists, nc.Members, nc.Watchers, c.Cover, c.CreatedBy, c.CreatedAt
		return nil
	})
	if _, err := docs.Update(ctx, o.Docs, tenant, cBoardCard, cardKey(from.ID, c.ID), actor, func(x *Card) error {
		if x.Deleted {
			return errCardGone
		}
		x.Deleted, x.MovedTo = true, cardKey(to.ID, moved.ID)
		return nil
	}); err != nil {
		return moved, err
	}
	moved, _, err = docs.Get[Card](ctx, o.Docs, tenant, cBoardCard, cardKey(to.ID, moved.ID))
	return moved, err
}

// CopyCard copies a card to a list on this or another board.
func (o *Ops) CopyCard(ctx context.Context, tenant, actor, board, card string, m CardMove, title string, keep []string) (Card, error) {
	c, _, err := o.GetCard(ctx, tenant, actor, board, card)
	if err != nil {
		return Card{}, err
	}
	toID := firstOf(m.Board, board)
	to, _, err := o.boardFor(ctx, tenant, actor, toID, BoardAdmin, BoardMember)
	if err != nil {
		return Card{}, err
	}
	if to.Kind == BoardLedgerline {
		return Card{}, invalid("cards cannot be copied onto a Ledgerline board")
	}
	nc := Card{Title: firstOf(strings.TrimSpace(title), c.Title), Description: c.Description, Due: c.Due, Start: c.Start, Location: c.Location, Cover: c.Cover, Link: c.Link}
	if contains(keep, "checklists") {
		nc.Checklists = copyChecklists(c.Checklists, false)
	}
	if contains(keep, "labels") && toID == board {
		nc.Labels = c.Labels
	}
	if contains(keep, "members") {
		nc.Members = c.Members
	}
	if contains(keep, "fields") && toID == board {
		nc.Fields = copyMap(c.Fields)
	}
	pos := math.NaN()
	if m.Pos != nil {
		pos = *m.Pos
	}
	return o.addCard(ctx, tenant, actor, to, m.List, nc, pos)
}

// DeleteCard deletes an archived card for good (board admins).
func (o *Ops) DeleteCard(ctx context.Context, tenant, actor, board, card string) error {
	b, _, err := o.boardFor(ctx, tenant, actor, board, BoardAdmin)
	if err != nil {
		return err
	}
	c, _, err := docs.Get[Card](ctx, o.Docs, tenant, cBoardCard, cardKey(board, card))
	if err != nil {
		return fmt.Errorf("%w: no such card", ErrNotFound)
	}
	if !c.Archived {
		return invalid("archive the card before deleting it")
	}
	if _, err := docs.Update(ctx, o.Docs, tenant, cBoardCard, cardKey(board, card), actor, func(x *Card) error { x.Deleted = true; return nil }); err != nil {
		return err
	}
	o.boardLog(ctx, tenant, actor, b, "", "card.deleted", fmt.Sprintf("deleted #%d %s", c.No, clip(c.Title, 80)), nil)
	return nil
}

// ToggleCardPerson adds or removes the person as watcher, member or voter.
func (o *Ops) ToggleCardPerson(ctx context.Context, tenant, actor, board, card, what string, on bool) (Card, error) {
	need := []string{BoardAdmin, BoardMember, BoardObserver}
	if what == "member" {
		need = []string{BoardAdmin, BoardMember}
	}
	b, _, err := o.boardFor(ctx, tenant, actor, board, need...)
	if err != nil {
		return Card{}, err
	}
	if what == "member" {
		c, _, err := o.GetCard(ctx, tenant, actor, board, card)
		if err != nil {
			return c, err
		}
		ms := toggleIn(c.Members, actor, on)
		return o.UpdateCard(ctx, tenant, actor, board, card, CardPatch{Members: &ms})
	}
	c, err := docs.Update(ctx, o.Docs, tenant, cBoardCard, cardKey(board, card), actor, func(x *Card) error {
		if x.Deleted {
			return errCardGone
		}
		switch what {
		case "watch":
			x.Watchers = toggleIn(x.Watchers, actor, on)
		case "vote":
			x.Votes = toggleIn(x.Votes, actor, on)
		default:
			return invalid("watch, vote or member")
		}
		return nil
	})
	if err == nil && what == "vote" {
		o.boardLog(ctx, tenant, actor, b, card, "card.voted", map[bool]string{true: "voted on #", false: "took back a vote on #"}[on]+fmt.Sprint(c.No), nil)
	}
	return c, err
}

// ---- checklists ----

// ChecklistOp is one change to a card's checklists.
type ChecklistOp struct {
	Op        string  `json:"op"` // add_list, rename_list, delete_list, add_item, update_item, delete_item, move_item
	Checklist string  `json:"checklist,omitempty"`
	Item      string  `json:"item,omitempty"`
	Name      string  `json:"name,omitempty"` // checklist name or item text
	Done      *bool   `json:"done,omitempty"`
	Member    *string `json:"member,omitempty"`
	Due       *string `json:"due,omitempty"`
	Index     *int    `json:"index,omitempty"`     // move_item: new place in the list
	CopyFrom  string  `json:"copy_from,omitempty"` // add_list: copy the items of this checklist …
	CopyCard  string  `json:"copy_card,omitempty"` // … on this card of the board ("": this card)
}

// EditChecklist applies one checklist change.
func (o *Ops) EditChecklist(ctx context.Context, tenant, actor, board, card string, op ChecklistOp) (Card, error) {
	b, _, err := o.boardFor(ctx, tenant, actor, board, BoardAdmin, BoardMember)
	if err != nil {
		return Card{}, err
	}
	var done []string // checklists completed by this change
	var logText string
	var from []Checklist // add_list copying from another card: that card's checklists
	if op.Op == "add_list" && op.CopyFrom != "" && op.CopyCard != "" && op.CopyCard != card {
		src, _, err := docs.Get[Card](ctx, o.Docs, tenant, cBoardCard, cardKey(board, op.CopyCard))
		if err != nil || src.Deleted {
			return Card{}, fmt.Errorf("%w: no such card to copy the checklist from", ErrNotFound)
		}
		if from = src.Checklists; from == nil {
			from = []Checklist{}
		}
	}
	c, err := docs.Update(ctx, o.Docs, tenant, cBoardCard, cardKey(board, card), actor, func(x *Card) error {
		if x.Deleted {
			return errCardGone
		}
		done, logText = nil, ""
		find := func() (*Checklist, error) {
			for i := range x.Checklists {
				if x.Checklists[i].ID == op.Checklist {
					return &x.Checklists[i], nil
				}
			}
			return nil, fmt.Errorf("%w: no such checklist", ErrNotFound)
		}
		switch op.Op {
		case "add_list":
			if len(x.Checklists) >= 20 {
				return invalid("a card can have at most 20 checklists")
			}
			n, err := textField("checklist name", firstOf(op.Name, "Checklist"), 200, true)
			if err != nil {
				return err
			}
			cl := Checklist{ID: boardID("CL-", 4), Name: n}
			if op.CopyFrom != "" {
				src, found := x.Checklists, false
				if from != nil {
					src = from
				}
				for _, y := range src {
					if y.ID == op.CopyFrom {
						cl.Items, found = copyChecklists([]Checklist{y}, true)[0].Items, true
					}
				}
				if !found {
					return fmt.Errorf("%w: no such checklist to copy", ErrNotFound)
				}
			}
			x.Checklists = append(x.Checklists, cl)
			logText = "added the checklist " + n
		case "rename_list":
			cl, err := find()
			if err != nil {
				return err
			}
			if cl.Name, err = textField("checklist name", op.Name, 200, true); err != nil {
				return err
			}
		case "delete_list":
			var keep []Checklist
			for _, y := range x.Checklists {
				if y.ID != op.Checklist {
					keep = append(keep, y)
				}
			}
			x.Checklists = keep
		case "add_item":
			cl, err := find()
			if err != nil {
				return err
			}
			if len(cl.Items) >= 200 {
				return invalid("a checklist can have at most 200 items")
			}
			t, err := textField("item", op.Name, 500, true)
			if err != nil {
				return err
			}
			cl.Items = append(cl.Items, CheckItem{ID: boardID("I-", 4), Text: t})
		case "update_item", "delete_item", "move_item":
			cl, err := find()
			if err != nil {
				return err
			}
			idx := -1
			for i := range cl.Items {
				if cl.Items[i].ID == op.Item {
					idx = i
				}
			}
			if idx < 0 {
				return fmt.Errorf("%w: no such checklist item", ErrNotFound)
			}
			switch op.Op {
			case "delete_item":
				cl.Items = append(cl.Items[:idx], cl.Items[idx+1:]...)
			case "move_item":
				if op.Index == nil || *op.Index < 0 || *op.Index >= len(cl.Items) {
					return invalid("give the item's new place")
				}
				it := cl.Items[idx]
				rest := append(append([]CheckItem(nil), cl.Items[:idx]...), cl.Items[idx+1:]...)
				cl.Items = append(rest[:*op.Index], append([]CheckItem{it}, rest[*op.Index:]...)...)
			default:
				it := &cl.Items[idx]
				if op.Name != "" {
					if it.Text, err = textField("item", op.Name, 500, true); err != nil {
						return err
					}
				}
				if op.Member != nil {
					it.Member = *op.Member
				}
				if op.Due != nil {
					if it.Due, err = boardDate(*op.Due); err != nil {
						return err
					}
				}
				if op.Done != nil && *op.Done != it.Done {
					it.Done = *op.Done
					if it.Done {
						logText = fmt.Sprintf("completed %s on #%d", clip(it.Text, 80), x.No)
						all := true
						for _, y := range cl.Items {
							all = all && y.Done
						}
						if all {
							done = append(done, cl.ID)
						}
					}
				}
			}
		default:
			return invalid("unknown checklist change %q", op.Op)
		}
		x.UpdatedAt = o.now().Format(time.RFC3339)
		return nil
	})
	if err != nil {
		return c, err
	}
	if logText != "" {
		o.boardLog(ctx, tenant, actor, b, card, "checklist."+op.Op, logText, nil)
	}
	for _, id := range done {
		o.boardEvent(ctx, tenant, BoardEvent{Board: board, Card: card, Kind: "checklist.done", Actor: actor, Data: map[string]string{"checklist": id}})
	}
	if op.Op == "update_item" && op.Member != nil && *op.Member != "" {
		o.boardNotifyKind(ctx, tenant, actor, b, card, NoticeAdded, []string{*op.Member}, fmt.Sprintf("%s gave you a checklist item on %s", actor, clip(c.Title, 80)))
	}
	return c, nil
}

// ---- comments ----

var mentionRe = regexp.MustCompile(`@([a-z0-9][a-z0-9._-]*)`)

// AddComment comments on a card; @user mentions notify those people,
// @card the card's members and watchers, and @board (from the board's
// admins and members) every admin and member of the board.
func (o *Ops) AddComment(ctx context.Context, tenant, actor, board, card, text string) (Card, error) {
	b, v, err := o.boardFor(ctx, tenant, actor, board, BoardAdmin, BoardMember, BoardObserver)
	if err != nil {
		return Card{}, err
	}
	if b.Closed {
		return Card{}, fmt.Errorf("%w: the board is closed", ErrState)
	}
	if err := boardCommentErr(b, v); err != nil {
		return Card{}, err
	}
	t, err := textField("comment", text, 5000, true)
	if err != nil {
		return Card{}, err
	}
	var ments, groups []string
	us, _ := o.Users(ctx, tenant)
	for _, m := range mentionRe.FindAllStringSubmatch(t, -1) {
		m[1] = strings.TrimRight(m[1], "._-") // "@board." ends a sentence
		switch g := m[1]; {
		case g == "card" && !contains(groups, g):
			groups = append(groups, g)
		case g == "board" && !contains(groups, g) && v.can(BoardAdmin, BoardMember): // an observer's @board is plain text
			groups = append(groups, g)
		}
		for _, u := range us {
			if u.ID == m[1] && u.Active && boardRole(b, u) != "" && !contains(ments, u.ID) {
				ments = append(ments, u.ID)
			}
		}
	}
	cm := CardComment{ID: boardID("CM-", 5), By: actor, At: o.now().Format(time.RFC3339), Text: t, Mentions: ments, Groups: groups}
	c, err := docs.Update(ctx, o.Docs, tenant, cBoardCard, cardKey(board, card), actor, func(x *Card) error {
		if x.Deleted {
			return errCardGone
		}
		if len(x.Comments) >= 2000 {
			return invalid("this card has too many comments; start another card")
		}
		x.Comments = append(x.Comments, cm)
		if !strings.HasPrefix(actor, "system:") && !contains(x.Watchers, actor) {
			x.Watchers = append(x.Watchers, actor)
		}
		x.UpdatedAt = cm.At
		return nil
	})
	if err != nil {
		return c, err
	}
	o.boardLog(ctx, tenant, actor, b, card, "comment.added", fmt.Sprintf("commented on #%d: %s", c.No, clip(t, 120)), map[string]string{"comment": cm.ID})
	o.boardNotifyKind(ctx, tenant, actor, b, card, NoticeMention, ments, fmt.Sprintf("%s mentioned you on %s: %s", actor, clip(c.Title, 60), clip(t, 120)))
	told := append([]string(nil), ments...) // one notice each: the most specific
	for _, g := range groups {
		var to []string
		for _, u := range boardGroup(b, c, g, us) {
			if !contains(told, u) {
				to, told = append(to, u), append(told, u)
			}
		}
		o.boardNotifyKind(ctx, tenant, actor, b, card, NoticeMention, to, fmt.Sprintf("%s mentioned @%s on %s: %s", actor, g, clip(c.Title, 60), clip(t, 120)))
	}
	var others []string
	for _, w := range append(append([]string(nil), c.Watchers...), c.Members...) {
		if !contains(told, w) {
			others = append(others, w)
		}
	}
	o.boardNotifyKind(ctx, tenant, actor, b, card, NoticeComment, others, fmt.Sprintf("%s commented on %s: %s", actor, clip(c.Title, 60), clip(t, 120)))
	o.boardWatchNotify(ctx, tenant, actor, b, card, "", append(told, others...), fmt.Sprintf("%s commented on %s on %s: %s", actor, clip(c.Title, 60), b.Name, clip(t, 120)))
	o.boardEvent(ctx, tenant, BoardEvent{Board: board, Card: card, Kind: "comment.added", Actor: actor, Data: map[string]string{"comment": cm.ID, "text": t}})
	return c, nil
}

// EditComment changes or deletes ("" text) the person's own comment.
func (o *Ops) EditComment(ctx context.Context, tenant, actor, board, card, id, text string, del bool) (Card, error) {
	b, v, err := o.boardFor(ctx, tenant, actor, board, BoardAdmin, BoardMember, BoardObserver)
	if err != nil {
		return Card{}, err
	}
	if err := boardCommentErr(b, v); err != nil && !del {
		return Card{}, err
	}
	return docs.Update(ctx, o.Docs, tenant, cBoardCard, cardKey(board, card), actor, func(x *Card) error {
		if x.Deleted {
			return errCardGone
		}
		for i, cm := range x.Comments {
			if cm.ID != id {
				continue
			}
			if cm.By != actor && !(del && v.can(BoardAdmin)) {
				return fmt.Errorf("%w: only the writer can change a comment", ErrForbidden)
			}
			if del {
				x.Comments = append(x.Comments[:i], x.Comments[i+1:]...)
				return nil
			}
			t, err := textField("comment", text, 5000, true)
			if err != nil {
				return err
			}
			x.Comments[i].Text, x.Comments[i].Edited = t, o.now().Format(time.RFC3339)
			return nil
		}
		return fmt.Errorf("%w: no such comment", ErrNotFound)
	})
}

var reactions = []string{"👍", "👀", "✅", "🎉", "❤️", "😂", "🙏", "❗"}

// ReactComment adds or removes the person's reaction to a comment.
func (o *Ops) ReactComment(ctx context.Context, tenant, actor, board, card, id, emoji string) (Card, error) {
	if !contains(reactions, emoji) {
		return Card{}, invalid("react with one of %s", strings.Join(reactions, " "))
	}
	b, v, err := o.boardFor(ctx, tenant, actor, board, BoardAdmin, BoardMember, BoardObserver)
	if err != nil {
		return Card{}, err
	}
	if err := boardCommentErr(b, v); err != nil {
		return Card{}, err
	}
	return docs.Update(ctx, o.Docs, tenant, cBoardCard, cardKey(board, card), actor, func(x *Card) error {
		if x.Deleted {
			return errCardGone
		}
		for i := range x.Comments {
			if x.Comments[i].ID == id {
				if x.Comments[i].Reactions == nil {
					x.Comments[i].Reactions = map[string][]string{}
				}
				r := x.Comments[i].Reactions[emoji]
				x.Comments[i].Reactions[emoji] = toggleIn(r, actor, !contains(r, actor))
				if len(x.Comments[i].Reactions[emoji]) == 0 {
					delete(x.Comments[i].Reactions, emoji)
				}
				return nil
			}
		}
		return fmt.Errorf("%w: no such comment", ErrNotFound)
	})
}

// ---- attachments ----

// AttachFile stores a file on a card.
func (o *Ops) AttachFile(ctx context.Context, tenant, actor, board, card, name string, data []byte) (Card, error) {
	b, _, err := o.boardFor(ctx, tenant, actor, board, BoardAdmin, BoardMember)
	if err != nil {
		return Card{}, err
	}
	name = strings.TrimSpace(path.Base(strings.ReplaceAll(name, "\\", "/")))
	if name == "" || name == "." || len(name) > 120 {
		return Card{}, invalid("the file needs a name of up to 120 characters")
	}
	if len(data) == 0 || len(data) > boardMaxFile {
		return Card{}, invalid("files can be up to %d MB", boardMaxFile>>20)
	}
	ct, img, err := chatFileType(name, data)
	if err != nil {
		return Card{}, err
	}
	if _, _, err := docs.Get[Card](ctx, o.Docs, tenant, cBoardCard, cardKey(board, card)); err != nil {
		return Card{}, fmt.Errorf("%w: no such card", ErrNotFound)
	}
	sum := sha256.Sum256(data)
	rf, err := o.App.Ingest.Raw.Put(ctx, ingest.RawFile{Tenant: tenant, Source: "BOARD", Name: board + " " + name, SHA256: hex.EncodeToString(sum[:]), Content: data})
	if err != nil {
		return Card{}, err
	}
	now := o.now().Format(time.RFC3339)
	f := boardFileDoc{ID: boardID("BF-", 9), Board: board, Name: name, Type: ct, Size: int64(len(data)), Image: img, RawID: rf.ID, By: actor, At: now}
	if err := docs.Upsert(ctx, o.Docs, tenant, cBoardFile, f.ID, f, actor); err != nil {
		return Card{}, err
	}
	c, err := docs.Update(ctx, o.Docs, tenant, cBoardCard, cardKey(board, card), actor, func(x *Card) error {
		if x.Deleted {
			return errCardGone
		}
		if len(x.Attachments) >= 100 {
			return invalid("a card can have at most 100 attachments")
		}
		x.Attachments = append(x.Attachments, CardAttachment{ID: boardID("A-", 4), Name: name, File: f.ID, Type: ct, Size: f.Size, Image: img, By: actor, At: now})
		if img && x.Cover == nil && b.CoversOn {
			x.Cover = &CardCover{File: f.ID}
		}
		x.UpdatedAt = now
		return nil
	})
	if err == nil {
		o.boardLog(ctx, tenant, actor, b, card, "card.attached", fmt.Sprintf("attached %s to #%d", name, c.No), nil)
	}
	return c, err
}

// AttachLink adds a link to a card.
func (o *Ops) AttachLink(ctx context.Context, tenant, actor, board, card, url, name string) (Card, error) {
	b, _, err := o.boardFor(ctx, tenant, actor, board, BoardAdmin, BoardMember)
	if err != nil {
		return Card{}, err
	}
	url = strings.TrimSpace(url)
	if !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "http://") || len(url) > 2000 {
		return Card{}, invalid("give a web address starting https://")
	}
	n, err := textField("link name", firstOf(name, url), 200, true)
	if err != nil {
		return Card{}, err
	}
	now := o.now().Format(time.RFC3339)
	c, err := docs.Update(ctx, o.Docs, tenant, cBoardCard, cardKey(board, card), actor, func(x *Card) error {
		if x.Deleted {
			return errCardGone
		}
		x.Attachments = append(x.Attachments, CardAttachment{ID: boardID("A-", 4), Name: n, URL: url, By: actor, At: now})
		x.UpdatedAt = now
		return nil
	})
	if err == nil {
		o.boardLog(ctx, tenant, actor, b, card, "card.attached", fmt.Sprintf("attached a link to #%d", c.No), nil)
	}
	return c, err
}

// RemoveAttachment removes an attachment from a card.
func (o *Ops) RemoveAttachment(ctx context.Context, tenant, actor, board, card, id string) (Card, error) {
	if _, _, err := o.boardFor(ctx, tenant, actor, board, BoardAdmin, BoardMember); err != nil {
		return Card{}, err
	}
	return docs.Update(ctx, o.Docs, tenant, cBoardCard, cardKey(board, card), actor, func(x *Card) error {
		if x.Deleted {
			return errCardGone
		}
		var keep []CardAttachment
		for _, a := range x.Attachments {
			if a.ID == id {
				if x.Cover != nil && x.Cover.File == a.File && a.File != "" {
					x.Cover = nil
				}
				continue
			}
			keep = append(keep, a)
		}
		if len(keep) == len(x.Attachments) {
			return fmt.Errorf("%w: no such attachment", ErrNotFound)
		}
		x.Attachments = keep
		return nil
	})
}

// UploadBoardFile stores a file for a board itself (its background
// image), without a card.
func (o *Ops) UploadBoardFile(ctx context.Context, tenant, actor, board, name string, data []byte) (string, error) {
	if _, _, err := o.boardFor(ctx, tenant, actor, board, BoardAdmin); err != nil {
		return "", err
	}
	ct, img, err := chatFileType(name, data)
	if err != nil {
		return "", err
	}
	if !img {
		return "", invalid("a board background is a JPEG, PNG, GIF or WebP image")
	}
	if len(data) > boardMaxFile {
		return "", invalid("files can be up to %d MB", boardMaxFile>>20)
	}
	sum := sha256.Sum256(data)
	rf, err := o.App.Ingest.Raw.Put(ctx, ingest.RawFile{Tenant: tenant, Source: "BOARD", Name: board + " " + name, SHA256: hex.EncodeToString(sum[:]), Content: data})
	if err != nil {
		return "", err
	}
	f := boardFileDoc{ID: boardID("BF-", 9), Board: board, Name: name, Type: ct, Size: int64(len(data)), Image: true, RawID: rf.ID, By: actor, At: o.now().Format(time.RFC3339)}
	return f.ID, docs.Upsert(ctx, o.Docs, tenant, cBoardFile, f.ID, f, actor)
}

// BoardFile returns a board file to someone who can see its board.
func (o *Ops) BoardFile(ctx context.Context, tenant, actor, id string) (name, ctype string, image bool, data []byte, err error) {
	f, _, err := docs.Get[boardFileDoc](ctx, o.Docs, tenant, cBoardFile, id)
	if err != nil {
		return "", "", false, nil, fmt.Errorf("%w: no such file", ErrNotFound)
	}
	if _, _, err := o.boardFor(ctx, tenant, actor, f.Board); err != nil {
		return "", "", false, nil, fmt.Errorf("%w: no such file", ErrNotFound)
	}
	rf, err := o.App.Ingest.Raw.Get(ctx, tenant, f.RawID)
	if err != nil {
		return "", "", false, nil, err
	}
	return f.Name, f.Type, f.Image, rf.Content, nil
}

// ---- activity, search, reminders ----

// BoardActivityList is a board's activity, newest first (one card's with
// card), before a sequence number for paging.
func (o *Ops) BoardActivityList(ctx context.Context, tenant, actor, board, card string, before int64, limit int) ([]BoardActivity, error) {
	if _, _, err := o.boardFor(ctx, tenant, actor, board); err != nil {
		return nil, err
	}
	as, err := docs.List[BoardActivity](ctx, o.Docs, tenant, cBoardAct, board+"|")
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	out := []BoardActivity{}
	for i := len(as) - 1; i >= 0 && len(out) < limit; i-- {
		a := as[i]
		if (card == "" || a.Card == card) && (before <= 0 || a.Seq < before) {
			out = append(out, a)
		}
	}
	return out, nil
}

// CardHit is a card found by search.
type CardHit struct {
	Board     string `json:"board"`
	BoardName string `json:"board_name"`
	Card      string `json:"card"`
	No        int64  `json:"no"`
	Title     string `json:"title"`
	List      string `json:"list"`
	Due       string `json:"due,omitempty"`
	Archived  bool   `json:"archived,omitempty"`
	Where     string `json:"where"` // title, description, comment, checklist, number
}

// SearchCards finds cards on the boards the person can see, by words in
// the title, description, comments or checklists, or "#12" for a number.
func (o *Ops) SearchCards(ctx context.Context, tenant, actor, q string) ([]CardHit, error) {
	q = strings.ToLower(strings.TrimSpace(q))
	if len([]rune(q)) < 2 {
		return []CardHit{}, nil
	}
	bs, err := o.Boards(ctx, tenant, actor)
	if err != nil {
		return nil, err
	}
	out := []CardHit{}
	for _, s := range bs {
		b, _, err := docs.Get[Board](ctx, o.Docs, tenant, cBoard, s.ID)
		if err != nil {
			continue
		}
		cs, _ := o.boardCards(ctx, tenant, s.ID)
		for _, c := range cs {
			where := ""
			switch {
			case strings.HasPrefix(q, "#") && fmt.Sprintf("#%d", c.No) == q:
				where = "number"
			case strings.Contains(strings.ToLower(c.Title), q):
				where = "title"
			case strings.Contains(strings.ToLower(c.Description), q):
				where = "description"
			}
			for _, cm := range c.Comments {
				if where == "" && strings.Contains(strings.ToLower(cm.Text), q) {
					where = "comment"
				}
			}
			for _, cl := range c.Checklists {
				for _, it := range cl.Items {
					if where == "" && strings.Contains(strings.ToLower(it.Text), q) {
						where = "checklist"
					}
				}
			}
			if where == "" {
				continue
			}
			l, _ := boardList(b, c.List)
			out = append(out, CardHit{Board: b.ID, BoardName: b.Name, Card: c.ID, No: c.No, Title: c.Title, List: l.Name, Due: c.Due, Archived: c.Archived, Where: where})
			if len(out) >= 100 {
				return out, nil
			}
		}
	}
	return out, nil
}

// boardDueReminders tells a card's members and watchers when it falls due
// within its reminder time, once per due date.
func (o *Ops) boardDueReminders(ctx context.Context, tenant string, now time.Time) {
	bs, err := docs.List[Board](ctx, o.Docs, tenant, cBoard, "")
	if err != nil {
		return
	}
	for _, b := range bs {
		if b.Closed || b.Template {
			continue
		}
		cs, _ := o.boardCards(ctx, tenant, b.ID)
		for _, c := range cs {
			if c.Archived || c.DueDone || c.Due == "" || c.Reminder <= 0 || c.Reminded == c.Due {
				continue
			}
			due, ok := boardTime(c.Due)
			if !ok || now.Before(due.Add(-time.Duration(c.Reminder)*time.Minute)) {
				continue
			}
			when := "is due " + due.In(ist).Format("02 Jan 15:04")
			if now.After(due) {
				when = "is overdue"
			}
			if _, err := docs.Update(ctx, o.Docs, tenant, cBoardCard, cardKey(b.ID, c.ID), SystemActor, func(x *Card) error { x.Reminded = x.Due; return nil }); err != nil {
				continue
			}
			o.boardNotifyKind(ctx, tenant, SystemActor, b, c.ID, NoticeDue, append(append([]string(nil), c.Members...), c.Watchers...), fmt.Sprintf("%s on %s %s", clip(c.Title, 80), b.Name, when))
		}
	}
}
