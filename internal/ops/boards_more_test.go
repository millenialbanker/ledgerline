package ops

import (
	"context"
	"errors"
	"strings"
	"testing"

	"ledgerline/internal/docs"
)

// Made-up boards and cards: no real firm's data.

func bmNotices(t *testing.T, o *Ops, ctx context.Context, who string) []BoardNotice {
	t.Helper()
	ns, _, err := o.BoardNotices(ctx, T, who, 200)
	if err != nil {
		t.Fatal(err)
	}
	return ns
}

// bmSince is the notices someone got after the first n.
func bmSince(t *testing.T, o *Ops, ctx context.Context, who string, n int) []BoardNotice {
	t.Helper()
	ns := bmNotices(t, o, ctx, who)
	return ns[:len(ns)-n]
}

func bmCount(t *testing.T, o *Ops, ctx context.Context, who string) int {
	t.Helper()
	return len(bmNotices(t, o, ctx, who))
}

// bmLedgerline is a Ledgerline board, made directly (its records do not
// matter for what is refused).
func bmLedgerline(t *testing.T, o *Ops, ctx context.Context) Board {
	t.Helper()
	b := Board{ID: boardID("B-", 6), Name: "Work queue", Kind: BoardLedgerline, Visibility: "firm", Lists: []BoardList{{ID: "L-open", Name: "Open", Pos: 1024}, {ID: "L-done", Name: "Done", Pos: 2048}},
		Members: []BoardPerson{}, CreatedBy: "admin", CreatedAt: "2026-11-01T10:00:00+05:30"}
	if _, err := docs.Put(ctx, o.Docs, T, cBoard, b.ID, b, 0, "admin"); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBoardCopyAndMoveList(t *testing.T) {
	o, ctx := setup(t)
	maker, checker, risk := "priya.nair", "farhan.khan", "anita.rao"
	b, _ := o.CreateBoard(ctx, T, maker, BoardInput{Name: "Month-end close", Visibility: "private", Members: []BoardPerson{{checker, BoardMember}, {risk, BoardMember}}})
	b, _ = o.UpdateBoard(ctx, T, maker, b.ID, BoardInput{Fields: []BoardField{{Name: "Segment", Type: "list", Options: []string{"CM", "FO"}}}, Labels: []BoardLabel{{Name: "Urgent", Color: "red_bold"}, {Name: "", Color: "green"}}})
	todo := b.Lists[0].ID
	urgent, green, seg := b.Labels[0].ID, b.Labels[1].ID, b.Fields[0].ID

	c1, _ := o.AddCard(ctx, T, maker, b.ID, CardInput{List: todo, Title: "Post interest on debit balances"})
	labels, members, due, fields := []string{urgent, green}, []string{checker, risk}, "2026-11-30", map[string]string{seg: "FO"}
	done := true
	if _, err := o.UpdateCard(ctx, T, maker, b.ID, c1.ID, CardPatch{Labels: &labels, Members: &members, Due: &due, DueDone: &done, Fields: &fields}); err != nil {
		t.Fatal(err)
	}
	c1, _ = o.EditChecklist(ctx, T, maker, b.ID, c1.ID, ChecklistOp{Op: "add_list", Name: "Steps"})
	cl := c1.Checklists[0].ID
	c1, _ = o.EditChecklist(ctx, T, maker, b.ID, c1.ID, ChecklistOp{Op: "add_item", Checklist: cl, Name: "Run interest"})
	c1, _ = o.EditChecklist(ctx, T, maker, b.ID, c1.ID, ChecklistOp{Op: "update_item", Checklist: cl, Item: c1.Checklists[0].Items[0].ID, Done: &done})
	if _, err := o.AddComment(ctx, T, checker, b.ID, c1.ID, "Rates checked"); err != nil {
		t.Fatal(err)
	}
	c2, _ := o.AddCard(ctx, T, maker, b.ID, CardInput{List: todo, Title: "DP bills"})
	linked, _ := o.AddCard(ctx, T, maker, b.ID, CardInput{List: todo, Title: "Break 41", Link: &CardLink{Kind: "break", ID: "BRK-41", Label: "Break 41"}})

	// On the same board: a new list just after it, cards as they are; the
	// linked card stays with its record.
	cp, err := o.CopyList(ctx, T, checker, b.ID, todo, ListCopyInput{Name: "To do (December)"})
	if err != nil || cp.Cards != 2 || cp.Skipped != 1 || cp.List.Name != "To do (December)" {
		t.Fatalf("copy %+v %v", cp, err)
	}
	v, _ := o.GetBoard(ctx, T, maker, b.ID, false)
	if v.Board.Lists[3].ID != cp.List.ID || !(v.Board.Lists[0].Pos < cp.List.Pos && cp.List.Pos < v.Board.Lists[1].Pos) {
		t.Fatalf("copied list not after the original: %+v", v.Board.Lists)
	}
	var got []Card
	for _, c := range v.Cards {
		if c.List == cp.List.ID {
			got = append(got, c)
		}
	}
	if len(got) != 2 || got[0].Title != c1.Title || got[1].Title != c2.Title {
		t.Fatalf("copied cards %+v", got)
	}
	g := got[0]
	if len(g.Labels) != 2 || g.Labels[0] != urgent || len(g.Members) != 2 || g.Due != due || !g.DueDone || g.Fields[seg] != "FO" ||
		len(g.Checklists) != 1 || !g.Checklists[0].Items[0].Done || g.Checklists[0].ID == cl || len(g.Comments) != 0 || g.No <= linked.No {
		t.Fatalf("copied card %+v", g)
	}
	// The members hear once about the list, not once a card.
	if ns := bmNotices(t, o, ctx, risk); !strings.Contains(ns[0].Text, "copied the list To do (December)") || ns[0].Kind != NoticeAdded {
		t.Fatalf("risk told %+v", ns[0])
	}

	// To another board: labels matched by name and colour (made when
	// missing), fields made, only people on that board kept.
	b2, _ := o.CreateBoard(ctx, T, checker, BoardInput{Name: "December close", Visibility: "private", Members: []BoardPerson{{maker, BoardMember}}})
	b2, _ = o.UpdateBoard(ctx, T, checker, b2.ID, BoardInput{Labels: []BoardLabel{{ID: b2.Labels[0].ID, Name: "", Color: "green"}}})
	cp2, err := o.CopyList(ctx, T, checker, b.ID, todo, ListCopyInput{Board: b2.ID})
	if err != nil || cp2.Cards != 2 || cp2.Board != b2.ID || cp2.List.Name != "To do" {
		t.Fatalf("copy to another board %+v %v", cp2, err)
	}
	v2, _ := o.GetBoard(ctx, T, checker, b2.ID, false)
	if len(v2.Board.Labels) != 2 || v2.Board.Labels[1].Name != "Urgent" || v2.Board.Labels[1].Color != "red_bold" || len(v2.Board.Fields) != 1 {
		t.Fatalf("labels and fields on the other board %+v %+v", v2.Board.Labels, v2.Board.Fields)
	}
	var g2 Card
	for _, c := range v2.Cards {
		if c.Title == c1.Title {
			g2 = c
		}
	}
	if g2.No != 1 || len(g2.Labels) != 2 || g2.Labels[0] != v2.Board.Labels[1].ID || g2.Labels[1] != v2.Board.Labels[0].ID ||
		len(g2.Members) != 1 || g2.Members[0] != checker || g2.Fields[v2.Board.Fields[0].ID] != "FO" {
		t.Fatalf("copied card on the other board %+v", g2)
	}

	// Ledgerline boards: no copying from or onto them.
	ll := bmLedgerline(t, o, ctx)
	if _, err := o.CopyList(ctx, T, "admin", ll.ID, "L-open", ListCopyInput{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("copied a Ledgerline list: %v", err)
	}
	if _, err := o.CopyList(ctx, T, "admin", b.ID, todo, ListCopyInput{Board: ll.ID}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("copied onto a Ledgerline board: %v", err)
	}
	if _, err := o.CopyList(ctx, T, risk, b.ID, todo, ListCopyInput{Board: b2.ID}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("copied onto a board the person cannot see: %v", err)
	}

	// Move the copy's source list to the other board.
	arch := true
	if _, err := o.UpdateCard(ctx, T, maker, b.ID, c2.ID, CardPatch{Archived: &arch}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.MoveList(ctx, T, maker, b.ID, todo, ListMoveInput{Board: ll.ID}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("moved a list onto a Ledgerline board: %v", err)
	}
	if _, err := o.MoveList(ctx, T, "admin", ll.ID, "L-open", ListMoveInput{Board: b.ID}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("moved a Ledgerline list: %v", err)
	}
	mv, err := o.MoveList(ctx, T, maker, b.ID, todo, ListMoveInput{Board: b2.ID})
	if err != nil || mv.Cards != 2 { // c1 and the linked card; the archived c2 stays
		t.Fatalf("move %+v %v", mv, err)
	}
	v, _ = o.GetBoard(ctx, T, maker, b.ID, true)
	if l, _ := boardList(v.Board, todo); !l.Archived || l.MovedTo != b2.ID {
		t.Fatalf("old list %+v", l)
	}
	if len(v.Cards) != 1 || v.Cards[0].ID != c2.ID {
		t.Fatalf("archived cards left behind %+v", v.Cards)
	}
	if _, err := o.UpdateList(ctx, T, maker, b.ID, todo, ListInput{Archived: new(bool)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("restored a moved list: %v", err)
	}
	if _, err := o.UpdateCard(ctx, T, maker, b.ID, c2.ID, CardPatch{Archived: new(bool)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("restored a card into a moved list: %v", err)
	}
	// The old card is a tombstone pointing at the new one, renumbered.
	_, _, err = o.GetCard(ctx, T, maker, b.ID, c1.ID)
	var moved *CardMovedError
	if !errors.As(err, &moved) || moved.Board != b2.ID || !errors.Is(err, ErrNotFound) {
		t.Fatalf("old link %v", err)
	}
	nc, _, err := o.GetCard(ctx, T, maker, moved.Board, moved.Card)
	// Number 3: after the two cards copied there.
	if err != nil || nc.List != mv.List.ID || nc.No != 3 || len(nc.Comments) != 1 || len(nc.Labels) != 2 || len(nc.Members) != 1 || nc.Members[0] != checker {
		t.Fatalf("moved card %+v %v", nc, err)
	}
	if ns := bmNotices(t, o, ctx, checker); !strings.Contains(ns[0].Text, "with the list To do") || ns[0].Kind != NoticeMoved {
		t.Fatalf("watcher told %+v", ns[0])
	}
}

func TestBoardWatching(t *testing.T) {
	o, ctx := setup(t)
	maker, checker, risk, comp := "priya.nair", "farhan.khan", "anita.rao", "vikram.shah"
	b, _ := o.CreateBoard(ctx, T, maker, BoardInput{Name: "Audit", Members: []BoardPerson{{checker, BoardMember}}})
	todo, doing := b.Lists[0].ID, b.Lists[1].ID

	// Risk watches the Doing list; compliance (an observer) the board.
	if l, err := o.WatchList(ctx, T, risk, b.ID, doing, true); err != nil || !contains(l.Watchers, risk) {
		t.Fatalf("watch list %+v %v", l, err)
	}
	if on, err := o.WatchBoard(ctx, T, comp, b.ID, true); err != nil || !on {
		t.Fatal(err)
	}
	if _, err := o.WatchBoard(ctx, T, "meera.iyer", b.ID, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("branch staff watched a board they cannot see: %v", err)
	}
	r0, c0, k0 := bmCount(t, o, ctx, risk), bmCount(t, o, ctx, comp), bmCount(t, o, ctx, checker)

	// A card added to To do: the board's watcher hears; the list's does not.
	c, _ := o.AddCard(ctx, T, maker, b.ID, CardInput{List: todo, Title: "Sample 25 contract notes"})
	if ns := bmSince(t, o, ctx, comp, c0); len(ns) != 1 || ns[0].Kind != NoticeWatch || !strings.Contains(ns[0].Text, "added Sample 25 contract notes to To do") {
		t.Fatalf("board watcher %+v", ns)
	}
	if n := bmCount(t, o, ctx, risk); n != r0 {
		t.Fatalf("list watcher told about another list: %d", n-r0)
	}
	// Moved into Doing: both hear, once each.
	if _, err := o.MoveCard(ctx, T, maker, b.ID, c.ID, CardMove{List: doing}); err != nil {
		t.Fatal(err)
	}
	if ns := bmSince(t, o, ctx, risk, r0); len(ns) != 1 || ns[0].Kind != NoticeWatch || !strings.Contains(ns[0].Text, "moved Sample 25 contract notes to Doing") {
		t.Fatalf("list watcher %+v", ns)
	}
	if ns := bmSince(t, o, ctx, comp, c0); len(ns) != 2 {
		t.Fatalf("board watcher %+v", ns)
	}
	// A card added straight to Doing: the list's watcher hears.
	if _, err := o.AddCard(ctx, T, maker, b.ID, CardInput{List: doing, Title: "Check the AP agreements"}); err != nil {
		t.Fatal(err)
	}
	if ns := bmSince(t, o, ctx, risk, r0); len(ns) != 2 || !strings.Contains(ns[0].Text, "added Check the AP agreements to Doing") {
		t.Fatalf("list watcher %+v", ns)
	}

	// The checker is on the card and also watches the board: a member's
	// notice, not a second one as a board watcher.
	if _, err := o.WatchBoard(ctx, T, checker, b.ID, true); err != nil {
		t.Fatal(err)
	}
	ms := []string{checker}
	if _, err := o.UpdateCard(ctx, T, maker, b.ID, c.ID, CardPatch{Members: &ms}); err != nil {
		t.Fatal(err)
	}
	if ns := bmSince(t, o, ctx, checker, k0); len(ns) != 1 || ns[0].Kind != NoticeAdded {
		t.Fatalf("member told twice %+v", ns)
	}
	k1, c1 := bmCount(t, o, ctx, checker), bmCount(t, o, ctx, comp)
	// A comment: the member (a card watcher) gets the comment notice only;
	// the board watcher gets one watch notice.
	if _, err := o.AddComment(ctx, T, maker, b.ID, c.ID, "Picked the sample"); err != nil {
		t.Fatal(err)
	}
	if ns := bmSince(t, o, ctx, checker, k1); len(ns) != 1 || ns[0].Kind != NoticeComment {
		t.Fatalf("card member %+v", ns)
	}
	if ns := bmSince(t, o, ctx, comp, c1); len(ns) != 1 || ns[0].Kind != NoticeWatch || !strings.Contains(ns[0].Text, "commented on") {
		t.Fatalf("board watcher %+v", ns)
	}
	// Mentioned: the mention only.
	if _, err := o.AddComment(ctx, T, maker, b.ID, c.ID, "@vikram.shah please review"); err != nil {
		t.Fatal(err)
	}
	if ns := bmSince(t, o, ctx, comp, c1); len(ns) != 2 || ns[0].Kind != NoticeMention {
		t.Fatalf("mentioned board watcher %+v", ns)
	}
	// Due date set, and marked complete: board watchers hear.
	due, yes := "2026-11-25T15:00", true
	if _, err := o.UpdateCard(ctx, T, maker, b.ID, c.ID, CardPatch{Due: &due}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.UpdateCard(ctx, T, maker, b.ID, c.ID, CardPatch{DueDone: &yes}); err != nil {
		t.Fatal(err)
	}
	ns := bmSince(t, o, ctx, comp, c1)
	if len(ns) != 4 || !strings.Contains(ns[1].Text, "due 25 Nov 2026, 15:00") || !strings.Contains(ns[0].Text, "marked Sample 25 contract notes complete") || ns[0].Kind != NoticeWatch {
		t.Fatalf("due and complete %+v", ns)
	}
	// The one who acted is never told; unwatching stops it.
	if _, err := o.WatchBoard(ctx, T, comp, b.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := o.AddCard(ctx, T, maker, b.ID, CardInput{List: todo, Title: "Close the audit"}); err != nil {
		t.Fatal(err)
	}
	if n := bmCount(t, o, ctx, comp); n != c1+4 {
		t.Fatalf("unwatched but told: %d", n-c1-4)
	}
	for _, n := range bmNotices(t, o, ctx, maker) {
		if n.Kind == NoticeWatch {
			t.Fatalf("the actor was told %+v", n)
		}
	}

	// Collapsing is each person's own.
	if cs, err := o.CollapseList(ctx, T, risk, b.ID, doing, true); err != nil || len(cs) != 1 || cs[0] != doing {
		t.Fatalf("collapse %v %v", cs, err)
	}
	if v, _ := o.GetBoard(ctx, T, risk, b.ID, false); len(v.Collapsed) != 1 {
		t.Fatalf("risk collapsed %v", v.Collapsed)
	}
	if v, _ := o.GetBoard(ctx, T, maker, b.ID, false); len(v.Collapsed) != 0 {
		t.Fatalf("maker sees risk's collapsed lists %v", v.Collapsed)
	}
	if cs, _ := o.CollapseList(ctx, T, risk, b.ID, doing, false); len(cs) != 0 {
		t.Fatalf("expand %v", cs)
	}
}

func TestBoardConvertCheckItem(t *testing.T) {
	o, ctx := setup(t)
	maker, checker := "priya.nair", "farhan.khan"
	b, _ := o.CreateBoard(ctx, T, maker, BoardInput{Name: "Onboarding", Members: []BoardPerson{{checker, BoardMember}}})
	todo := b.Lists[0].ID
	c, _ := o.AddCard(ctx, T, maker, b.ID, CardInput{List: todo, Title: "Open the Pune branch"})
	next, _ := o.AddCard(ctx, T, maker, b.ID, CardInput{List: todo, Title: "Train the staff"})
	c, _ = o.EditChecklist(ctx, T, maker, b.ID, c.ID, ChecklistOp{Op: "add_list", Name: "Setup"})
	cl := c.Checklists[0].ID
	for _, it := range []string{"Get the SEBI sub-broker letter", "Order the furniture"} {
		c, _ = o.EditChecklist(ctx, T, maker, b.ID, c.ID, ChecklistOp{Op: "add_item", Checklist: cl, Name: it})
	}
	it := c.Checklists[0].Items[0]
	m, d := checker, "2026-12-01"
	c, _ = o.EditChecklist(ctx, T, maker, b.ID, c.ID, ChecklistOp{Op: "update_item", Checklist: cl, Item: it.ID, Member: &m, Due: &d})
	k0 := bmCount(t, o, ctx, checker)

	if _, _, err := o.ConvertCheckItem(ctx, T, "anita.rao", b.ID, c.ID, cl, it.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("an observer converted an item: %v", err)
	}
	src, made, err := o.ConvertCheckItem(ctx, T, maker, b.ID, c.ID, cl, it.ID)
	if err != nil {
		t.Fatal(err)
	}
	if made.Title != it.Text || made.List != todo || len(made.Members) != 1 || made.Members[0] != checker || made.Due != d || !(c.Pos < made.Pos && made.Pos < next.Pos) {
		t.Fatalf("new card %+v (between %v and %v)", made, c.Pos, next.Pos)
	}
	if len(src.Checklists[0].Items) != 1 || src.Checklists[0].Items[0].Text != "Order the furniture" {
		t.Fatalf("item still there %+v", src.Checklists)
	}
	acts, _ := o.BoardActivityList(ctx, T, maker, b.ID, c.ID, 0, 0)
	if acts[0].What != "checklist.converted" || !strings.Contains(acts[0].Text, "into the card #3") {
		t.Fatalf("activity %+v", acts[0])
	}
	if ns := bmSince(t, o, ctx, checker, k0); len(ns) != 1 || ns[0].Kind != NoticeAdded {
		t.Fatalf("item member told %+v", ns)
	}
	if _, _, err := o.ConvertCheckItem(ctx, T, maker, b.ID, c.ID, cl, it.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("converted it twice: %v", err)
	}

	// Copy items from a checklist on another card of the board.
	nc, err := o.EditChecklist(ctx, T, maker, b.ID, next.ID, ChecklistOp{Op: "add_list", Name: "Setup", CopyFrom: cl, CopyCard: c.ID})
	if err != nil || len(nc.Checklists) != 1 || len(nc.Checklists[0].Items) != 1 || nc.Checklists[0].Items[0].Text != "Order the furniture" {
		t.Fatalf("copied items %+v %v", nc.Checklists, err)
	}
	if _, err := o.EditChecklist(ctx, T, maker, b.ID, next.ID, ChecklistOp{Op: "add_list", CopyFrom: "CL-none", CopyCard: c.ID}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("copied a checklist that does not exist: %v", err)
	}
}

func TestBoardMentionGroups(t *testing.T) {
	o, ctx := setup(t)
	maker, checker, risk, comp := "priya.nair", "farhan.khan", "anita.rao", "vikram.shah"
	b, _ := o.CreateBoard(ctx, T, maker, BoardInput{Name: "Close", Visibility: "private", Members: []BoardPerson{{checker, BoardMember}, {risk, BoardObserver}, {comp, BoardMember}}})
	c, _ := o.AddCard(ctx, T, maker, b.ID, CardInput{List: b.Lists[0].ID, Title: "Post interest"})
	k0, r0, v0 := bmCount(t, o, ctx, checker), bmCount(t, o, ctx, risk), bmCount(t, o, ctx, comp)

	// An observer's @board is plain text: nobody is told.
	c, err := o.AddComment(ctx, T, risk, b.ID, c.ID, "@board please look")
	if err != nil || len(c.Comments[0].Groups) != 0 {
		t.Fatalf("observer @board %+v %v", c.Comments, err)
	}
	if bmCount(t, o, ctx, checker) != k0 || bmCount(t, o, ctx, comp) != v0 {
		t.Fatal("an observer's @board told people")
	}
	// A member's @board tells every admin and member, once, even one also
	// mentioned by name; not the observer.
	c, err = o.AddComment(ctx, T, checker, b.ID, c.ID, "@board the rates are in. @vikram.shah over to you.")
	if err != nil || len(c.Comments[1].Groups) != 1 || c.Comments[1].Groups[0] != "board" {
		t.Fatalf("@board %+v %v", c.Comments, err)
	}
	if ns := bmSince(t, o, ctx, comp, v0); len(ns) != 1 || ns[0].Kind != NoticeMention || !strings.Contains(ns[0].Text, "mentioned you") {
		t.Fatalf("named and @board %+v", ns)
	}
	if ns := bmNotices(t, o, ctx, maker); ns[0].Kind != NoticeMention || !strings.Contains(ns[0].Text, "mentioned @board") {
		t.Fatalf("admin %+v", ns[0])
	}
	// The observer watches the card, having commented: a comment notice.
	if ns := bmSince(t, o, ctx, risk, r0); len(ns) != 1 || ns[0].Kind != NoticeComment {
		t.Fatalf("observer told about @board %+v", ns)
	}
	// @card tells the card's members and watchers.
	if c, err = o.AddComment(ctx, T, maker, b.ID, c.ID, "Done, @card."); err != nil || c.Comments[2].Groups[0] != "card" {
		t.Fatalf("@card %+v %v", c.Comments, err)
	}
	if ns := bmSince(t, o, ctx, risk, r0); len(ns) != 2 || ns[0].Kind != NoticeMention || !strings.Contains(ns[0].Text, "mentioned @card") {
		t.Fatalf("watcher @card %+v", ns)
	}
	if ns := bmSince(t, o, ctx, comp, v0); len(ns) != 1 {
		t.Fatalf("not a watcher of the card, but told about @card %+v", ns)
	}

	// On a Ledgerline board the members come from their roles: @board tells
	// head office, not branch staff.
	ll := bmLedgerline(t, o, ctx)
	lc, err := o.AddCard(ctx, T, "admin", ll.ID, CardInput{List: "L-open", Title: "Check the pay-in shortfall"})
	if err != nil {
		t.Fatal(err)
	}
	k1, b1 := bmCount(t, o, ctx, checker), bmCount(t, o, ctx, "meera.iyer")
	if _, err := o.AddComment(ctx, T, maker, ll.ID, lc.ID, "@board this needs someone today"); err != nil {
		t.Fatal(err)
	}
	if ns := bmSince(t, o, ctx, checker, k1); len(ns) != 1 || !strings.Contains(ns[0].Text, "mentioned @board") {
		t.Fatalf("head office on a Ledgerline board %+v", ns)
	}
	if bmCount(t, o, ctx, "meera.iyer") != b1 {
		t.Fatal("branch staff told about a Ledgerline board")
	}
}

func TestBoardPermissions(t *testing.T) {
	o, ctx := setup(t)
	maker, checker, risk, comp := "priya.nair", "farhan.khan", "anita.rao", "vikram.shah"
	b, _ := o.CreateBoard(ctx, T, "admin", BoardInput{Name: "Firm notices", Members: []BoardPerson{{checker, BoardMember}, {risk, BoardObserver}}})
	c, _ := o.AddCard(ctx, T, "admin", b.ID, CardInput{List: b.Lists[0].ID, Title: "Holiday list for 2027"})
	if b.Perms.comments() != CommentObservers || b.Perms.invite() != InviteAdmins || b.Perms.SelfJoin {
		t.Fatalf("defaults %+v", b.Perms)
	}

	// Commenting: by default observers (and everyone at head office on a
	// firm-wide board) may comment, as before.
	if _, err := o.AddComment(ctx, T, maker, b.ID, c.ID, "Diwali is on the 8th"); err != nil {
		t.Fatalf("head office on a firm-wide board: %v", err)
	}
	if _, err := o.UpdateBoard(ctx, T, checker, b.ID, BoardInput{Perms: &BoardPerms{Comments: CommentMembers}}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a member changed the permissions: %v", err)
	}
	if _, err := o.UpdateBoard(ctx, T, "admin", b.ID, BoardInput{Perms: &BoardPerms{Comments: "everyone"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown setting: %v", err)
	}
	if _, err := o.UpdateBoard(ctx, T, "admin", b.ID, BoardInput{Perms: &BoardPerms{Comments: CommentMembers}}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.AddComment(ctx, T, risk, b.ID, c.ID, "Noted"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("an observer commented when only members may: %v", err)
	}
	c, err := o.AddComment(ctx, T, checker, b.ID, c.ID, "Noted")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.ReactComment(ctx, T, risk, b.ID, c.ID, c.Comments[0].ID, "👍"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("an observer reacted: %v", err)
	}
	if _, err := o.UpdateBoard(ctx, T, "admin", b.ID, BoardInput{Perms: &BoardPerms{Comments: CommentOff}}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.AddComment(ctx, T, "admin", b.ID, c.ID, "x"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("commented with commenting off: %v", err)
	}
	if _, err := o.AddComment(ctx, T, "system:automation", b.ID, c.ID, "Moved by a rule"); err != nil {
		t.Fatalf("automation is Ledgerline's own: %v", err)
	}
	// The writer may still delete their own comment.
	if _, err := o.EditComment(ctx, T, checker, b.ID, c.ID, c.Comments[1].ID, "", true); err != nil {
		t.Fatal(err)
	}

	// Adding and removing members: admins only by default.
	add := func(who string, ms ...BoardPerson) error {
		v, _ := o.GetBoard(ctx, T, "admin", b.ID, false)
		_, err := o.SetBoardMembers(ctx, T, who, b.ID, append(append([]BoardPerson(nil), v.Board.Members...), ms...))
		return err
	}
	if err := add(checker, BoardPerson{comp, BoardMember}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a member added someone: %v", err)
	}
	if _, err := o.UpdateBoard(ctx, T, "admin", b.ID, BoardInput{Perms: &BoardPerms{Comments: CommentObservers, Invite: InviteMembers}}); err != nil {
		t.Fatal(err)
	}
	if err := add(checker, BoardPerson{comp, BoardAdmin}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a member added an admin: %v", err)
	}
	if err := add(checker, BoardPerson{comp, BoardMember}); err != nil {
		t.Fatal(err)
	}
	if ns := bmNotices(t, o, ctx, comp); ns[0].Kind != NoticeAdded || !strings.Contains(ns[0].Text, "added you to the board") {
		t.Fatalf("new member %+v", ns[0])
	}
	v, _ := o.GetBoard(ctx, T, checker, b.ID, false)
	var noAdmin, promote []BoardPerson
	for _, m := range v.Board.Members {
		if m.Role != BoardAdmin {
			noAdmin = append(noAdmin, m)
		}
		if m.User == risk {
			m.Role = BoardMember
		}
		promote = append(promote, m)
	}
	if _, err := o.SetBoardMembers(ctx, T, checker, b.ID, noAdmin); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a member removed an admin: %v", err)
	}
	if _, err := o.SetBoardMembers(ctx, T, checker, b.ID, promote); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a member changed a role: %v", err)
	}
	if _, err := o.SetBoardMembers(ctx, T, risk, b.ID, v.Board.Members[:1]); !errors.Is(err, ErrForbidden) {
		t.Fatalf("an observer changed the members: %v", err)
	}

	// Joining: off unless the admins switch it on.
	if v, _ := o.GetBoard(ctx, T, maker, b.ID, false); v.CanJoin || v.Role != BoardObserver {
		t.Fatalf("can join before it was switched on %+v", v.Role)
	}
	if _, err := o.JoinBoard(ctx, T, maker, b.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("joined without the setting: %v", err)
	}
	if _, err := o.UpdateBoard(ctx, T, "admin", b.ID, BoardInput{Perms: &BoardPerms{SelfJoin: true}}); err != nil {
		t.Fatal(err)
	}
	if v, _ := o.GetBoard(ctx, T, maker, b.ID, false); !v.CanJoin || v.Board.Perms.invite() != InviteAdmins {
		t.Fatalf("cannot join %+v", v.Board.Perms)
	}
	if v, _ := o.GetBoard(ctx, T, "admin", b.ID, false); v.CanJoin {
		t.Fatal("an admin was offered to join")
	}
	jb, err := o.JoinBoard(ctx, T, maker, b.ID)
	if err != nil || jb.EmailKey != "" {
		t.Fatal(err)
	}
	if v, _ := o.GetBoard(ctx, T, maker, b.ID, false); v.Role != BoardMember || v.CanJoin {
		t.Fatalf("after joining: %s", v.Role)
	}
	if acts, _ := o.BoardActivityList(ctx, T, maker, b.ID, "", 0, 1); acts[0].What != "board.joined" || acts[0].By != maker {
		t.Fatalf("join not logged %+v", acts)
	}
	if _, err := o.JoinBoard(ctx, T, maker, b.ID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("joined twice: %v", err)
	}
	if _, err := o.JoinBoard(ctx, T, "meera.iyer", b.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("branch staff joined a firm-wide board: %v", err)
	}
	// A private board cannot be joined, whatever it was set to.
	pb, _ := o.UpdateBoard(ctx, T, "admin", b.ID, BoardInput{Visibility: "private", Perms: &BoardPerms{SelfJoin: true}})
	if pb.Perms.SelfJoin {
		t.Fatal("a private board kept joining on")
	}

	// Ledgerline boards keep their role-based members: only commenting can change.
	ll := bmLedgerline(t, o, ctx)
	if _, err := o.UpdateBoard(ctx, T, "admin", ll.ID, BoardInput{Perms: &BoardPerms{Invite: InviteMembers}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("member adding on a Ledgerline board: %v", err)
	}
	if _, err := o.UpdateBoard(ctx, T, "admin", ll.ID, BoardInput{Perms: &BoardPerms{SelfJoin: true}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("joining a Ledgerline board: %v", err)
	}
	if _, err := o.UpdateBoard(ctx, T, "admin", ll.ID, BoardInput{Perms: &BoardPerms{Comments: CommentMembers}}); err != nil {
		t.Fatal(err)
	}
}

// A new card's members are staff, as when they are changed later.
func TestBoardAddCardMembersStaff(t *testing.T) {
	o, ctx := setup(t)
	b, _ := o.CreateBoard(ctx, T, "priya.nair", BoardInput{Name: "Members"})
	if _, err := o.AddCard(ctx, T, "priya.nair", b.ID, CardInput{List: b.Lists[0].ID, Title: "x", Members: []string{"no.such.person"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a card took someone who is not staff: %v", err)
	}
	c, err := o.AddCard(ctx, T, "priya.nair", b.ID, CardInput{List: b.Lists[0].ID, Title: "x", Members: []string{"farhan.khan", "farhan.khan", ""}})
	if err != nil || len(c.Members) != 1 {
		t.Fatalf("%+v %v", c.Members, err)
	}
}

func TestBoardLabelColours(t *testing.T) {
	o, ctx := setup(t)
	b, _ := o.CreateBoard(ctx, T, "priya.nair", BoardInput{Name: "Colours"})
	var ls []BoardLabel
	for _, h := range boardColors {
		for _, c := range []string{h + "_subtle", h, h + "_bold"} {
			ls = append(ls, BoardLabel{Color: c})
		}
	}
	if b, err := o.UpdateBoard(ctx, T, "priya.nair", b.ID, BoardInput{Labels: ls}); err != nil || len(b.Labels) != 30 {
		t.Fatalf("30 colours %v", err)
	}
	for _, bad := range []string{"green_medium", "teal", "_bold", "green_bold_subtle"} {
		if _, err := o.UpdateBoard(ctx, T, "priya.nair", b.ID, BoardInput{Labels: []BoardLabel{{Color: bad}}}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: %v", bad, err)
		}
	}
	shade := "green_bold"
	if _, err := o.UpdateList(ctx, T, "priya.nair", b.ID, b.Lists[0].ID, ListInput{Color: &shade}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a list took a label shade: %v", err)
	}
	if labelColorName("pink_subtle") != "subtle pink" || labelColorName("pink") != "pink" {
		t.Fatal(labelColorName("pink_subtle"))
	}
}
