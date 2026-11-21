package ops

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
)

// ---- Boards: more of Trello's list, card and board features ----
//
// Copying a list (on its board or to another team board) and moving one to
// another board; watching a list or a board; collapsing a list (each
// person's own choice, kept on the server); turning a checklist item into
// a card; and the board's permissions: who may comment, who may add and
// remove members, and whether anyone at head office may join a firm-wide
// board. The core (boards.go) calls in here where a change tells watchers
// or needs a permission.

const cBoardPref = "boardpref" // "<board>|<user>": one person's view of a board

// BoardPerms are a board's permissions. The zero value is what boards did
// before there were any: members and observers comment; admins add and
// remove members; nobody joins on their own.
type BoardPerms struct {
	Comments string `json:"comments,omitempty"`  // off, members, observers (members and observers; the default)
	Invite   string `json:"invite,omitempty"`    // admins (the default) or members: who adds and removes members
	SelfJoin bool   `json:"self_join,omitempty"` // a firm-wide team board: anyone at head office can join as a member
}

// Who may comment, and who may add and remove members.
const (
	CommentOff       = "off"
	CommentMembers   = "members"
	CommentObservers = "observers"
	InviteAdmins     = "admins"
	InviteMembers    = "members"
)

func (p BoardPerms) comments() string { return firstOf(p.Comments, CommentObservers) }
func (p BoardPerms) invite() string   { return firstOf(p.Invite, InviteAdmins) }

var (
	boardCommentWho = map[string]string{CommentOff: "no one", CommentMembers: "members", CommentObservers: "members and observers"}
	boardInviteWho  = map[string]string{InviteAdmins: "admins", InviteMembers: "all members"}
)

// applyBoardPerms sets a board's permissions and says what changed. A
// Ledgerline board's members come from people's roles, so only who may
// comment can change there; joining is for firm-wide team boards.
func applyBoardPerms(x *Board, p BoardPerms) ([]string, error) {
	if _, ok := boardCommentWho[p.comments()]; !ok {
		return nil, invalid("commenting is off, for members, or for members and observers")
	}
	if _, ok := boardInviteWho[p.invite()]; !ok {
		return nil, invalid("members are added and removed by admins, or by all members")
	}
	if x.Kind == BoardLedgerline && (p.invite() != InviteAdmins || p.SelfJoin) {
		return nil, invalid("a Ledgerline board's members come from their roles; only who may comment can be changed")
	}
	if x.Kind != BoardTeam || x.Visibility != "firm" {
		p.SelfJoin = false // only a firm-wide team board can be joined
	}
	var ch []string
	if p.comments() != x.Perms.comments() {
		ch = append(ch, "let "+boardCommentWho[p.comments()]+" comment")
	}
	if p.invite() != x.Perms.invite() {
		ch = append(ch, "let "+boardInviteWho[p.invite()]+" add and remove members")
	}
	if p.SelfJoin != x.Perms.SelfJoin {
		ch = append(ch, map[bool]string{true: "let anyone at head office join the board", false: "stopped people joining the board on their own"}[p.SelfJoin])
	}
	x.Perms = BoardPerms{Comments: p.comments(), Invite: p.invite(), SelfJoin: p.SelfJoin}
	return ch, nil
}

func boardSystem(id string) bool { return strings.HasPrefix(id, "system:") }

// boardCommentErr says why the person may not comment on the board (nil
// when they may). Ledgerline itself always may.
func boardCommentErr(b Board, v boardViewer) error {
	if boardSystem(v.user.ID) {
		return nil
	}
	switch b.Perms.comments() {
	case CommentOff:
		return fmt.Errorf("%w: commenting is switched off on this board", ErrForbidden)
	case CommentMembers:
		if !v.can(BoardAdmin, BoardMember) {
			return fmt.Errorf("%w: only the board's members can comment on it", ErrForbidden)
		}
	}
	return nil
}

// boardCanJoin says why the person may not join the board (nil when they
// may): a firm-wide team board whose admins let anyone at head office join.
func boardCanJoin(b Board, v boardViewer) error {
	switch {
	case b.Kind != BoardTeam || b.Visibility != "firm" || !b.Perms.SelfJoin:
		return fmt.Errorf("%w: this board's admins add its members", ErrForbidden)
	case b.Closed:
		return fmt.Errorf("%w: the board is closed", ErrState)
	case boardSystem(v.user.ID) || v.user.scope() != "":
		return fmt.Errorf("%w: only head-office staff join firm-wide boards", ErrForbidden)
	case v.board == BoardAdmin:
		return invalid("you are already an admin of this board")
	}
	for _, m := range b.Members {
		if m.User == v.user.ID {
			return invalid("you are already on this board as %s", m.Role)
		}
	}
	return nil
}

// JoinBoard makes the person a member of a firm-wide board that anyone at
// head office can join.
func (o *Ops) JoinBoard(ctx context.Context, tenant, actor, id string) (Board, error) {
	b, v, err := o.boardFor(ctx, tenant, actor, id)
	if err != nil {
		return Board{}, err
	}
	if err := boardCanJoin(b, v); err != nil {
		return Board{}, err
	}
	// Head office only (branch and partner staff do not see firm-wide boards).
	if err := o.require(ctx, tenant, actor, "Joining a firm-wide board", RoleMaker, RoleChecker, RoleRisk, RoleCompliance); err != nil {
		return Board{}, err
	}
	b, err = docs.Update(ctx, o.Docs, tenant, cBoard, id, actor, func(x *Board) error {
		for _, m := range x.Members {
			if m.User == actor {
				return invalid("you are already on this board as %s", m.Role)
			}
		}
		x.Members = append(x.Members, BoardPerson{User: actor, Role: BoardMember})
		x.UpdatedAt = o.now().Format(time.RFC3339)
		return nil
	})
	if err != nil {
		return Board{}, err
	}
	o.boardLog(ctx, tenant, actor, b, "", "board.joined", "joined this board", nil)
	o.log(ctx, tenant, actor, "board.joined", b.ID, map[string]any{"name": b.Name})
	return b, nil
}

// SetBoardMembers sets a board's members. Admins may set anything (as
// UpdateBoard does). When the board lets all members add and remove
// members, a member may add people as members or observers and remove
// anyone but an admin; roles stay as they are.
func (o *Ops) SetBoardMembers(ctx context.Context, tenant, actor, id string, members []BoardPerson) (Board, error) {
	b, v, err := o.boardFor(ctx, tenant, actor, id, BoardAdmin, BoardMember)
	if err != nil {
		return Board{}, err
	}
	if v.can(BoardAdmin) {
		return o.UpdateBoard(ctx, tenant, actor, id, BoardInput{Members: members})
	}
	if b.Kind == BoardLedgerline || b.Perms.invite() != InviteMembers {
		return Board{}, fmt.Errorf("%w: only the board's admins add and remove its members", ErrForbidden)
	}
	var added, removed []string
	b, err = docs.Update(ctx, o.Docs, tenant, cBoard, id, actor, func(x *Board) error {
		added, removed = nil, nil
		old := map[string]string{}
		for _, m := range x.Members {
			old[m.User] = m.Role
		}
		seen := map[string]bool{}
		var out []BoardPerson
		for _, m := range members {
			if m.User == "" || seen[m.User] {
				continue
			}
			seen[m.User] = true
			if r, ok := old[m.User]; ok {
				if r != m.Role {
					return fmt.Errorf("%w: only a board admin changes someone's role", ErrForbidden)
				}
				out = append(out, m)
				continue
			}
			if m.Role == BoardAdmin {
				return fmt.Errorf("%w: only a board admin can add an admin", ErrForbidden)
			}
			if err := o.boardMemberOK(ctx, tenant, m); err != nil {
				return err
			}
			out = append(out, m)
			added = append(added, m.User)
		}
		for _, m := range x.Members {
			if !seen[m.User] {
				if m.Role == BoardAdmin {
					return fmt.Errorf("%w: only a board admin can remove an admin", ErrForbidden)
				}
				removed = append(removed, m.User)
			}
		}
		x.Members = out
		x.UpdatedAt = o.now().Format(time.RFC3339)
		return nil
	})
	if err != nil {
		return Board{}, err
	}
	if len(added) > 0 {
		o.boardLog(ctx, tenant, actor, b, "", "board.updated", "added "+strings.Join(added, ", ")+" to the board", nil)
		o.boardNotifyKind(ctx, tenant, actor, b, "", NoticeAdded, added, fmt.Sprintf("%s added you to the board %s", actor, b.Name))
	}
	if len(removed) > 0 {
		o.boardLog(ctx, tenant, actor, b, "", "board.updated", "removed "+strings.Join(removed, ", ")+" from the board", nil)
	}
	return b, nil
}

// ---- watching ----

// boardGroup is who an @board or @card in a comment tells: every admin
// and member of the board (on a Ledgerline board, whose members come from
// their roles, everyone whose role works it), or the card's members and
// watchers, as long as they can still see the board.
func boardGroup(b Board, c Card, g string, us []User) []string {
	var ids []string
	switch g {
	case "board":
		for _, m := range b.Members {
			if m.Role == BoardAdmin || m.Role == BoardMember {
				ids = append(ids, m.User)
			}
		}
		if b.Kind == BoardLedgerline {
			for _, u := range us {
				if r := boardRole(b, u); u.Role != RoleClient && (r == BoardAdmin || r == BoardMember) {
					ids = append(ids, u.ID)
				}
			}
		}
	case "card":
		ids = append(append(ids, c.Members...), c.Watchers...)
	}
	return boardSeers(b, ids, us)
}

// boardSeers keeps the people who are active and can see the board.
func boardSeers(b Board, ids []string, us []User) []string {
	by := map[string]User{}
	for _, u := range us {
		by[u.ID] = u
	}
	var out []string
	for _, id := range ids {
		if u, ok := by[id]; ok && u.Active && boardRole(b, u) != "" && !contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// boardWatchNotify tells the people watching the board, and those watching
// the list when one is given, about a change to a card. The people in skip
// already had a more specific notice about it (as a member, a watcher of
// the card, or mentioned), so they get no second one.
func (o *Ops) boardWatchNotify(ctx context.Context, tenant, actor string, b Board, card, list string, skip []string, text string) {
	var who []string
	if l, ok := boardList(b, list); ok && list != "" {
		who = append(who, l.Watchers...)
	}
	who = append(who, b.Watchers...)
	var to []string
	for _, w := range who {
		if !contains(skip, w) {
			to = append(to, w)
		}
	}
	if len(to) == 0 {
		return
	}
	us, _ := o.Users(ctx, tenant)
	o.boardNotifyKind(ctx, tenant, actor, b, card, NoticeWatch, boardSeers(b, to, us), text)
}

// boardDateText is a card date for people: "20 Nov 2026", "20 Nov 2026, 15:00".
func boardDateText(d string) string {
	if t, err := time.Parse("2006-01-02T15:04", d); err == nil {
		return t.Format("2 Jan 2006, 15:04")
	}
	if t, err := time.Parse("2006-01-02", d); err == nil {
		return t.Format("2 Jan 2006")
	}
	return d
}

// WatchBoard starts or stops the person watching a board: they are told
// when a card on it is added, moved, completed, commented on or re-dated.
func (o *Ops) WatchBoard(ctx context.Context, tenant, actor, id string, on bool) (bool, error) {
	if _, _, err := o.boardFor(ctx, tenant, actor, id); err != nil {
		return false, err
	}
	if boardSystem(actor) {
		return false, invalid("Ledgerline does not watch boards")
	}
	_, err := docs.Update(ctx, o.Docs, tenant, cBoard, id, actor, func(x *Board) error {
		x.Watchers = toggleIn(x.Watchers, actor, on)
		return nil
	})
	return on, err
}

// WatchList starts or stops the person watching a list: they are told when
// a card is added to it or moved into it.
func (o *Ops) WatchList(ctx context.Context, tenant, actor, board, list string, on bool) (BoardList, error) {
	if _, _, err := o.boardFor(ctx, tenant, actor, board); err != nil {
		return BoardList{}, err
	}
	if boardSystem(actor) {
		return BoardList{}, invalid("Ledgerline does not watch lists")
	}
	var out BoardList
	_, err := docs.Update(ctx, o.Docs, tenant, cBoard, board, actor, func(x *Board) error {
		for i := range x.Lists {
			if l := &x.Lists[i]; l.ID == list && !l.Archived {
				l.Watchers = toggleIn(l.Watchers, actor, on)
				out = *l
				return nil
			}
		}
		return fmt.Errorf("%w: no such list", ErrNotFound)
	})
	return out, err
}

// ---- collapsing ----

// boardPref is one person's view of a board, kept so it follows them from
// one device to another.
type boardPref struct {
	Board     string   `json:"board"`
	User      string   `json:"user"`
	Collapsed []string `json:"collapsed,omitempty"` // lists shown as a narrow strip
	UpdatedAt string   `json:"updated_at"`
}

// CollapseList collapses or expands a list for the person; it returns the
// lists they now have collapsed on the board.
func (o *Ops) CollapseList(ctx context.Context, tenant, actor, board, list string, on bool) ([]string, error) {
	b, _, err := o.boardFor(ctx, tenant, actor, board)
	if err != nil {
		return nil, err
	}
	if l, ok := boardList(b, list); !ok || l.Archived {
		return nil, fmt.Errorf("%w: no such list", ErrNotFound)
	}
	key := cardKey(board, actor)
	p, _, err := docs.GetOr(ctx, o.Docs, tenant, cBoardPref, key, boardPref{Board: board, User: actor})
	if err != nil {
		return nil, err
	}
	p.Collapsed, p.UpdatedAt = toggleIn(p.Collapsed, list, on), o.now().Format(time.RFC3339)
	if err := docs.Upsert(ctx, o.Docs, tenant, cBoardPref, key, p, actor); err != nil {
		return nil, err
	}
	return o.boardCollapsed(ctx, tenant, actor, b), nil
}

// boardCollapsed is the open lists the person collapsed on the board.
func (o *Ops) boardCollapsed(ctx context.Context, tenant, actor string, b Board) []string {
	out := []string{}
	p, _, _ := docs.GetOr(ctx, o.Docs, tenant, cBoardPref, cardKey(b.ID, actor), boardPref{})
	for _, id := range p.Collapsed {
		if l, ok := boardList(b, id); ok && !l.Archived {
			out = append(out, id)
		}
	}
	return out
}

// ---- copying and moving lists ----

// ListCopyInput is where a copy of a list goes.
type ListCopyInput struct {
	Board string   `json:"board,omitempty"` // another team board ("": this one)
	Name  string   `json:"name,omitempty"`  // "": the list's own name
	Pos   *float64 `json:"pos,omitempty"`   // nil: just after the list on its own board, last on another
}

// ListCopy is a list made by copying or moving one.
type ListCopy struct {
	Board   string    `json:"board"`
	List    BoardList `json:"list"`
	Cards   int       `json:"cards"`
	Skipped int       `json:"skipped,omitempty"` // linked cards, which stay with their record
}

// boardPos is where a new list goes on a board: at pos, just after the list
// after, or last.
func boardPos(x *Board, pos *float64, after string) float64 {
	if pos != nil && !math.IsNaN(*pos) {
		return *pos
	}
	var open []BoardList
	for _, l := range x.Lists {
		if !l.Archived {
			open = append(open, l)
		}
	}
	sort.Slice(open, func(i, j int) bool { return open[i].Pos < open[j].Pos })
	for i, l := range open {
		if l.ID == after {
			if i+1 < len(open) {
				return (l.Pos + open[i+1].Pos) / 2
			}
			return l.Pos + 1024
		}
	}
	p := 1024.0
	for _, l := range x.Lists {
		p = math.Max(p, l.Pos+1024)
	}
	return p
}

// boardMapOnto finds on board to the labels (same name and colour) and
// custom fields (same name and type) of board from that the cards use,
// adding to it those it lacks. It returns to as it now is, with maps from
// from's IDs to to's. On the same board the maps are the identity.
func (o *Ops) boardMapOnto(ctx context.Context, tenant, actor string, from, to Board, cards []Card) (Board, map[string]string, map[string]string, error) {
	lm, fm := map[string]string{}, map[string]string{}
	if from.ID == to.ID {
		for _, l := range from.Labels {
			lm[l.ID] = l.ID
		}
		for _, f := range from.Fields {
			fm[f.ID] = f.ID
		}
		return to, lm, fm, nil
	}
	usedL, usedF := map[string]bool{}, map[string][]string{}
	for _, c := range cards {
		for _, l := range c.Labels {
			usedL[l] = true
		}
		for f, v := range c.Fields {
			usedF[f] = append(usedF[f], v)
		}
	}
	if len(usedL) == 0 && len(usedF) == 0 {
		return to, lm, fm, nil
	}
	nb, err := docs.Update(ctx, o.Docs, tenant, cBoard, to.ID, actor, func(x *Board) error {
		lm, fm = map[string]string{}, map[string]string{}
		for _, l := range from.Labels {
			if !usedL[l.ID] {
				continue
			}
			for _, y := range x.Labels {
				if strings.EqualFold(y.Name, l.Name) && y.Color == l.Color {
					lm[l.ID] = y.ID
					break
				}
			}
			if lm[l.ID] == "" && len(x.Labels) < 50 {
				nl := BoardLabel{ID: boardID("LB-", 4), Name: l.Name, Color: l.Color}
				x.Labels, lm[l.ID] = append(x.Labels, nl), nl.ID
			}
		}
		for _, f := range from.Fields {
			vals, ok := usedF[f.ID]
			if !ok {
				continue
			}
			i := -1
			for j, y := range x.Fields {
				if strings.EqualFold(y.Name, f.Name) && y.Type == f.Type {
					i = j
					break
				}
			}
			if i < 0 {
				if len(x.Fields) >= 50 {
					continue
				}
				nf := f
				nf.ID, nf.Options = boardID("F-", 4), append([]string(nil), f.Options...)
				x.Fields, i = append(x.Fields, nf), len(x.Fields)
			}
			if x.Fields[i].Type == "list" {
				for _, v := range vals {
					if v != "" && !contains(x.Fields[i].Options, v) {
						x.Fields[i].Options = append(x.Fields[i].Options, v)
					}
				}
			}
			fm[f.ID] = x.Fields[i].ID
		}
		return nil
	})
	return nb, lm, fm, err
}

// boardPeople is everyone who can see the board.
func (o *Ops) boardPeople(ctx context.Context, tenant string, b Board) map[string]bool {
	us, _ := o.Users(ctx, tenant)
	out := map[string]bool{}
	for _, u := range us {
		if u.Active && u.Role != RoleClient && boardRole(b, u) != "" {
			out[u.ID] = true
		}
	}
	return out
}

// listCards are a list's open cards, in order.
func (o *Ops) listCards(ctx context.Context, tenant string, b Board, list string) []Card {
	all, _ := o.boardCards(ctx, tenant, b.ID)
	var out []Card
	for _, c := range all {
		if c.List == list && !c.Archived {
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Pos < out[j].Pos })
	return out
}

// CopyList copies a list and its open cards, on its own board or to another
// team board the person can change. The cards keep their checklists (as
// they are), labels (matched on another board by name and colour, and made
// there if missing), members who are on that board, dates and custom field
// values; not their comments, attachments or activity. Cards linked to a
// Ledgerline record stay with the record and are not copied.
func (o *Ops) CopyList(ctx context.Context, tenant, actor, board, list string, in ListCopyInput) (ListCopy, error) {
	src, _, err := o.boardFor(ctx, tenant, actor, board)
	if err != nil {
		return ListCopy{}, err
	}
	if src.Kind == BoardLedgerline {
		return ListCopy{}, invalid("a Ledgerline board's lists are its records' stages and are not copied; copy its cards to a team board instead")
	}
	l, ok := boardList(src, list)
	if !ok || l.Archived {
		return ListCopy{}, fmt.Errorf("%w: no such list", ErrNotFound)
	}
	to, _, err := o.boardFor(ctx, tenant, actor, firstOf(in.Board, board), BoardAdmin, BoardMember)
	if err != nil {
		return ListCopy{}, err
	}
	if to.Kind == BoardLedgerline {
		return ListCopy{}, invalid("a Ledgerline board's lists are its records' stages; copy the list to a team board")
	}
	name, err := textField("list name", firstOf(strings.TrimSpace(in.Name), l.Name), 120, true)
	if err != nil {
		return ListCopy{}, err
	}
	var cards []Card
	out := ListCopy{Board: to.ID}
	for _, c := range o.listCards(ctx, tenant, src, list) {
		if c.Link != nil {
			out.Skipped++
			continue
		}
		cards = append(cards, c)
	}
	to, lm, fm, err := o.boardMapOnto(ctx, tenant, actor, src, to, cards)
	if err != nil {
		return ListCopy{}, err
	}
	people := o.boardPeople(ctx, tenant, to)
	nl := BoardList{ID: boardID("L-", 4), Name: name, Color: l.Color, Limit: l.Limit}
	to, err = docs.Update(ctx, o.Docs, tenant, cBoard, to.ID, actor, func(x *Board) error {
		if len(x.Lists) >= 100 {
			return invalid("a board can have at most 100 lists")
		}
		after := ""
		if x.ID == src.ID {
			after = list
		}
		nl.Pos = boardPos(x, in.Pos, after)
		x.Lists = append(x.Lists, nl)
		x.UpdatedAt = o.now().Format(time.RFC3339)
		return nil
	})
	if err != nil {
		return ListCopy{}, err
	}
	out.List = nl
	on := map[string]int{} // members: how many cards each is on
	for _, c := range cards {
		nc := Card{Title: c.Title, Description: c.Description, Start: c.Start, Due: c.Due, DueDone: c.DueDone, Reminder: c.Reminder,
			Checklists: copyChecklists(c.Checklists, false), Location: c.Location, Template: c.Template}
		if c.Cover != nil && c.Cover.Color != "" { // an image cover is an attachment, which stays behind
			nc.Cover = &CardCover{Color: c.Cover.Color, Full: c.Cover.Full}
		}
		if c.Mirror != nil {
			m := *c.Mirror
			nc.Mirror = &m
		}
		for _, id := range c.Labels {
			if x, ok := lm[id]; ok {
				nc.Labels = append(nc.Labels, x)
			}
		}
		for id, v := range c.Fields {
			if x, ok := fm[id]; ok {
				if nc.Fields == nil {
					nc.Fields = map[string]string{}
				}
				nc.Fields[x] = v
			}
		}
		for _, m := range c.Members {
			if people[m] {
				nc.Members = append(nc.Members, m)
				on[m]++
			}
		}
		if _, err := o.addCardOpt(ctx, tenant, actor, to, nl.ID, nc, math.NaN(), true); err != nil {
			return out, err
		}
		out.Cards++
	}
	cards1 := fmt.Sprintf("%d card%s", out.Cards, plural(out.Cards))
	if to.ID == src.ID {
		o.boardLog(ctx, tenant, actor, to, "", "list.copied", fmt.Sprintf("copied the list %s as %s (%s)", l.Name, name, cards1), map[string]string{"list": nl.ID, "from_list": list})
	} else {
		o.boardLog(ctx, tenant, actor, to, "", "list.copied", fmt.Sprintf("copied the list %s here from %s (%s)", l.Name, src.Name, cards1), map[string]string{"list": nl.ID})
		o.boardLog(ctx, tenant, actor, src, "", "list.copied_out", fmt.Sprintf("copied the list %s to the board %s", l.Name, to.Name), map[string]string{"list": list})
	}
	var told []string
	for m, k := range on {
		told = append(told, m)
		o.boardNotifyKind(ctx, tenant, actor, to, "", NoticeAdded, []string{m}, fmt.Sprintf("%s copied the list %s to %s, with you on %d card%s", actor, name, to.Name, k, plural(k)))
	}
	o.boardWatchNotify(ctx, tenant, actor, to, "", "", told, fmt.Sprintf("%s copied the list %s to %s (%s)", actor, name, to.Name, cards1))
	return out, nil
}

// ListMoveInput is where a list moves.
type ListMoveInput struct {
	Board string   `json:"board"`         // another team board ("": this one, to pos)
	Pos   *float64 `json:"pos,omitempty"` // nil: last
}

// MoveList moves a list and its open cards to another team board. The
// cards take that board's next numbers and keep their comments,
// attachments, checklists and dates; labels and custom fields are matched
// there (and made if missing); members and watchers who cannot see that
// board are left off. Each old card stays behind as a tombstone that
// points at the new one, so old links still find it. The list stays here,
// archived, for its archived cards.
func (o *Ops) MoveList(ctx context.Context, tenant, actor, board, list string, in ListMoveInput) (ListCopy, error) {
	if in.Board == "" || in.Board == board {
		if in.Pos == nil {
			return ListCopy{}, invalid("choose the board to move the list to")
		}
		l, err := o.UpdateList(ctx, tenant, actor, board, list, ListInput{Pos: in.Pos})
		return ListCopy{Board: board, List: l}, err
	}
	src, _, err := o.boardFor(ctx, tenant, actor, board, BoardAdmin, BoardMember)
	if err != nil {
		return ListCopy{}, err
	}
	to, _, err := o.boardFor(ctx, tenant, actor, in.Board, BoardAdmin, BoardMember)
	if err != nil {
		return ListCopy{}, err
	}
	if src.Kind == BoardLedgerline || to.Kind == BoardLedgerline {
		return ListCopy{}, invalid("a Ledgerline board's lists are its records' stages: they cannot move, and no list can move onto one")
	}
	l, ok := boardList(src, list)
	if !ok || l.Archived {
		return ListCopy{}, fmt.Errorf("%w: no such list", ErrNotFound)
	}
	cards := o.listCards(ctx, tenant, src, list)
	to, lm, fm, err := o.boardMapOnto(ctx, tenant, actor, src, to, cards)
	if err != nil {
		return ListCopy{}, err
	}
	people := o.boardPeople(ctx, tenant, to)
	nl := BoardList{ID: boardID("L-", 4), Name: l.Name, Color: l.Color, Limit: l.Limit}
	for _, w := range l.Watchers {
		if people[w] {
			nl.Watchers = append(nl.Watchers, w)
		}
	}
	to, err = docs.Update(ctx, o.Docs, tenant, cBoard, to.ID, actor, func(x *Board) error {
		if len(x.Lists) >= 100 {
			return invalid("a board can have at most 100 lists")
		}
		nl.Pos = boardPos(x, in.Pos, "")
		x.Lists = append(x.Lists, nl)
		x.UpdatedAt = o.now().Format(time.RFC3339)
		return nil
	})
	if err != nil {
		return ListCopy{}, err
	}
	out := ListCopy{Board: to.ID, List: nl}
	opt := cardMoveOpts{labels: lm, fields: fm, people: people}
	for _, c := range cards {
		moved, err := o.cardOntoBoard(ctx, tenant, actor, src, c, to, nl.ID, math.NaN(), opt)
		if err != nil {
			return out, err
		}
		out.Cards++
		o.boardNotifyKind(ctx, tenant, actor, to, moved.ID, NoticeMoved, moved.Watchers, fmt.Sprintf("%s moved %s with the list %s to %s", actor, clip(c.Title, 80), l.Name, to.Name))
	}
	if _, err := docs.Update(ctx, o.Docs, tenant, cBoard, src.ID, actor, func(x *Board) error {
		for i := range x.Lists {
			if x.Lists[i].ID == list {
				x.Lists[i].Archived, x.Lists[i].MovedTo, x.Lists[i].Watchers = true, to.ID, nil
			}
		}
		x.UpdatedAt = o.now().Format(time.RFC3339)
		return nil
	}); err != nil {
		return out, err
	}
	cards1 := fmt.Sprintf("%d card%s", out.Cards, plural(out.Cards))
	o.boardLog(ctx, tenant, actor, src, "", "list.moved_out", fmt.Sprintf("moved the list %s to the board %s (%s)", l.Name, to.Name, cards1), map[string]string{"list": list, "board": to.ID})
	o.boardLog(ctx, tenant, actor, to, "", "list.moved_in", fmt.Sprintf("moved the list %s here from %s (%s)", l.Name, src.Name, cards1), map[string]string{"list": nl.ID, "board": src.ID})
	o.log(ctx, tenant, actor, "board.list_moved", src.ID, map[string]any{"list": l.Name, "to": to.ID, "cards": out.Cards})
	o.boardWatchNotify(ctx, tenant, actor, to, "", "", nil, fmt.Sprintf("%s moved the list %s to %s from %s (%s)", actor, l.Name, to.Name, src.Name, cards1))
	return out, nil
}

// ---- checklists ----

// ConvertCheckItem turns a checklist item into a card in the same list,
// just below its card, titled with the item's text and given its member and
// due date. The item leaves the checklist. It returns the card the item
// was on, and the new card.
func (o *Ops) ConvertCheckItem(ctx context.Context, tenant, actor, board, card, checklist, item string) (Card, Card, error) {
	b, _, err := o.boardFor(ctx, tenant, actor, board, BoardAdmin, BoardMember)
	if err != nil {
		return Card{}, Card{}, err
	}
	c, _, err := docs.Get[Card](ctx, o.Docs, tenant, cBoardCard, cardKey(board, card))
	if err != nil || c.Deleted {
		return Card{}, Card{}, errCardGone
	}
	var it CheckItem
	for _, cl := range c.Checklists {
		for _, x := range cl.Items {
			if cl.ID == checklist && x.ID == item {
				it = x
			}
		}
	}
	if it.ID == "" {
		return Card{}, Card{}, fmt.Errorf("%w: no such checklist item", ErrNotFound)
	}
	pos := c.Pos + 1024
	for _, x := range o.listCards(ctx, tenant, b, c.List) {
		if x.Pos > c.Pos {
			pos = (c.Pos + x.Pos) / 2
			break
		}
	}
	nc := Card{Title: it.Text, Due: it.Due}
	if it.Member != "" {
		nc.Members = []string{it.Member}
	}
	made, err := o.addCard(ctx, tenant, actor, b, c.List, nc, math.NaN())
	if err != nil {
		return Card{}, Card{}, err
	}
	// addCard reads a position of 0 or less as "at the top"; cards dragged
	// to the top can sit there, so put the new one in place afterwards.
	if m, err := docs.Update(ctx, o.Docs, tenant, cBoardCard, cardKey(board, made.ID), actor, func(x *Card) error { x.Pos = pos; return nil }); err == nil {
		made = m
	}
	allDone := false
	c, err = docs.Update(ctx, o.Docs, tenant, cBoardCard, cardKey(board, card), actor, func(x *Card) error {
		if x.Deleted {
			return errCardGone
		}
		allDone = false
		for i := range x.Checklists {
			cl := &x.Checklists[i]
			if cl.ID != checklist {
				continue
			}
			was := len(cl.Items) > 0
			for _, y := range cl.Items {
				was = was && y.Done
			}
			var keep []CheckItem
			for _, y := range cl.Items {
				if y.ID != item {
					keep = append(keep, y)
				}
			}
			cl.Items = keep
			now := len(keep) > 0
			for _, y := range keep {
				now = now && y.Done
			}
			allDone = now && !was
		}
		x.UpdatedAt = o.now().Format(time.RFC3339)
		return nil
	})
	if err != nil {
		return c, made, err
	}
	o.boardLog(ctx, tenant, actor, b, card, "checklist.converted", fmt.Sprintf("turned the checklist item %s on #%d into the card #%d", clip(it.Text, 80), c.No, made.No), map[string]string{"card": made.ID})
	if allDone {
		o.boardEvent(ctx, tenant, BoardEvent{Board: board, Card: card, Kind: "checklist.done", Actor: actor, Data: map[string]string{"checklist": checklist}})
	}
	return c, made, nil
}
