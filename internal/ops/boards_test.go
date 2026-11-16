package ops

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestBoardsCore(t *testing.T) {
	o, ctx := setup(t)
	maker, checker, risk := "priya.nair", "farhan.khan", "anita.rao"

	b, err := o.CreateBoard(ctx, T, maker, BoardInput{Name: "Month-end close", Visibility: "private"})
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Lists) != 3 || len(b.Labels) != 6 || b.Members[0].User != maker || b.Members[0].Role != BoardAdmin {
		t.Fatalf("new board %+v", b)
	}
	// A private board is invisible to others; a firm Admin sees it.
	if _, err := o.GetBoard(ctx, T, checker, b.ID, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("checker saw a private board: %v", err)
	}
	if bs, _ := o.Boards(ctx, T, "admin"); len(bs) != 1 {
		t.Fatalf("admin boards %+v", bs)
	}
	// Add the checker as a member and the risk manager as an observer.
	b, err = o.UpdateBoard(ctx, T, maker, b.ID, BoardInput{Members: []BoardPerson{{maker, BoardAdmin}, {checker, BoardMember}, {risk, BoardObserver}}})
	if err != nil {
		t.Fatal(err)
	}
	if ns, unread, _ := o.BoardNotices(ctx, T, checker, 0); unread != 1 || !strings.Contains(ns[0].Text, "added you to the board") {
		t.Fatalf("checker notices %+v", ns)
	}
	todo, doing, done := b.Lists[0].ID, b.Lists[1].ID, b.Lists[2].ID

	c1, err := o.AddCard(ctx, T, checker, b.ID, CardInput{List: todo, Title: "Post interest on debit balances", Due: "2026-10-31"})
	if err != nil {
		t.Fatal(err)
	}
	c2, _ := o.AddCard(ctx, T, checker, b.ID, CardInput{List: todo, Title: "DP bills with GST"})
	top := 0.0
	c3, _ := o.AddCard(ctx, T, maker, b.ID, CardInput{List: todo, Title: "AP commission", Pos: &top})
	if c1.No != 1 || c2.No != 2 || c3.No != 3 || !(c3.Pos < c1.Pos && c1.Pos < c2.Pos) {
		t.Fatalf("numbers and order: %v %v %v / %v %v %v", c1.No, c2.No, c3.No, c1.Pos, c2.Pos, c3.Pos)
	}
	if _, err := o.AddCard(ctx, T, risk, b.ID, CardInput{List: todo, Title: "x"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("observer added a card: %v", err)
	}

	// Labels, members, dates, custom fields, cover.
	b, err = o.UpdateBoard(ctx, T, maker, b.ID, BoardInput{Fields: []BoardField{{Name: "Amount", Type: "number"}, {Name: "Segment", Type: "list", Options: []string{"CM", "FO", "COM"}}}})
	if err != nil {
		t.Fatal(err)
	}
	lab := b.Labels[3].ID
	labels, members, due, rem := []string{lab}, []string{risk}, "2026-10-30T17:00", 60
	fields := map[string]string{b.Fields[0].ID: "1250.50", b.Fields[1].ID: "FO"}
	c1, err = o.UpdateCard(ctx, T, checker, b.ID, c1.ID, CardPatch{Labels: &labels, Members: &members, Due: &due, Reminder: &rem, Fields: &fields})
	if err != nil {
		t.Fatal(err)
	}
	if c1.Due != due || len(c1.Fields) != 2 || !contains(c1.Watchers, risk) {
		t.Fatalf("patched card %+v", c1)
	}
	bad := map[string]string{b.Fields[1].ID: "MCX"}
	if _, err := o.UpdateCard(ctx, T, checker, b.ID, c1.ID, CardPatch{Fields: &bad}); err == nil {
		t.Fatal("a list field took a value not in its options")
	}

	// Move, with the watchers told.
	if _, err := o.MoveCard(ctx, T, checker, b.ID, c1.ID, CardMove{List: doing}); err != nil {
		t.Fatal(err)
	}
	if ns, _, _ := o.BoardNotices(ctx, T, risk, 0); len(ns) < 2 || !strings.Contains(ns[0].Text, "moved") {
		t.Fatalf("risk notices %+v", ns)
	}

	// Checklist: completing it fires checklist.done.
	var events []BoardEvent
	boardEventHooks = append(boardEventHooks, func(_ context.Context, _ *Ops, _ string, ev BoardEvent) { events = append(events, ev) })
	defer func() { boardEventHooks = boardEventHooks[:len(boardEventHooks)-1] }()
	c1, err = o.EditChecklist(ctx, T, checker, b.ID, c1.ID, ChecklistOp{Op: "add_list", Name: "Steps"})
	if err != nil {
		t.Fatal(err)
	}
	cl := c1.Checklists[0].ID
	for _, it := range []string{"Run interest", "Check the 4 clients on their own rate"} {
		if c1, err = o.EditChecklist(ctx, T, checker, b.ID, c1.ID, ChecklistOp{Op: "add_item", Checklist: cl, Name: it}); err != nil {
			t.Fatal(err)
		}
	}
	yes := true
	for _, it := range c1.Checklists[0].Items {
		if c1, err = o.EditChecklist(ctx, T, checker, b.ID, c1.ID, ChecklistOp{Op: "update_item", Checklist: cl, Item: it.ID, Done: &yes}); err != nil {
			t.Fatal(err)
		}
	}
	if len(events) == 0 || events[len(events)-1].Kind != "checklist.done" {
		t.Fatalf("events %+v", events)
	}

	// Comments: an observer may comment; @mentions notify.
	if _, err := o.AddComment(ctx, T, risk, b.ID, c1.ID, "@priya.nair please check the FO rate"); err != nil {
		t.Fatal(err)
	}
	if ns, _, _ := o.BoardNotices(ctx, T, maker, 0); !strings.Contains(ns[0].Text, "mentioned you") {
		t.Fatalf("maker notices %+v", ns)
	}
	c1, _, _ = o.GetCard(ctx, T, risk, b.ID, c1.ID)
	cm := c1.Comments[0].ID
	if _, err := o.EditComment(ctx, T, checker, b.ID, c1.ID, cm, "changed", false); !errors.Is(err, ErrForbidden) {
		t.Fatalf("edited someone else's comment: %v", err)
	}
	if c1, err = o.ReactComment(ctx, T, checker, b.ID, c1.ID, cm, "👍"); err != nil || len(c1.Comments[0].Reactions["👍"]) != 1 {
		t.Fatalf("reaction %+v %v", c1.Comments, err)
	}

	// Attachments: an image becomes the cover; a link; removing the image drops the cover.
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x02\x00\x00\x00\x90wS\xde\x00\x00\x00\x0cIDATx\x9cc\xf8\x0f\x00\x00\x01\x01\x00\x05\x18\xd8N\x00\x00\x00\x00IEND\xaeB`\x82")
	c1, err = o.AttachFile(ctx, T, checker, b.ID, c1.ID, "screenshot.png", png)
	if err != nil {
		t.Fatal(err)
	}
	if c1.Cover == nil || c1.Cover.File == "" {
		t.Fatalf("no cover %+v", c1)
	}
	if name, _, img, data, err := o.BoardFile(ctx, T, risk, c1.Cover.File); err != nil || name != "screenshot.png" || !img || len(data) != len(png) {
		t.Fatalf("file %s %v %d %v", name, img, len(data), err)
	}
	if _, _, _, _, err := o.BoardFile(ctx, T, "vikram.shah", c1.Cover.File); err == nil {
		t.Fatal("someone off the board read its file")
	}
	if c1, err = o.AttachLink(ctx, T, checker, b.ID, c1.ID, "https://www.nseindia.com", "NSE"); err != nil || len(c1.Attachments) != 2 {
		t.Fatalf("link %v", err)
	}
	if c1, err = o.RemoveAttachment(ctx, T, checker, b.ID, c1.ID, c1.Attachments[0].ID); err != nil || c1.Cover != nil {
		t.Fatalf("removed image still the cover: %+v %v", c1.Cover, err)
	}

	// Search by words and by number.
	if hs, _ := o.SearchCards(ctx, T, checker, "fo rate"); len(hs) != 1 || hs[0].Where != "comment" {
		t.Fatalf("search %+v", hs)
	}
	if hs, _ := o.SearchCards(ctx, T, checker, "#2"); len(hs) != 1 || hs[0].Card != c2.ID {
		t.Fatalf("search by number %+v", hs)
	}

	// Lists: sort, archive, move all.
	if err := o.SortList(ctx, T, checker, b.ID, todo, "title"); err != nil {
		t.Fatal(err)
	}
	if n, err := o.MoveListCards(ctx, T, checker, b.ID, todo, done); err != nil || n != 2 {
		t.Fatalf("moved %d %v", n, err)
	}
	if n, err := o.ArchiveListCards(ctx, T, checker, b.ID, done); err != nil || n != 2 {
		t.Fatalf("archived %d %v", n, err)
	}
	v, _ := o.GetBoard(ctx, T, checker, b.ID, true)
	if len(v.Cards) != 2 {
		t.Fatalf("archived cards %d", len(v.Cards))
	}
	if err := o.DeleteCard(ctx, T, checker, b.ID, c2.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("member deleted a card: %v", err)
	}
	if err := o.DeleteCard(ctx, T, maker, b.ID, c2.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := o.GetCard(ctx, T, maker, b.ID, c2.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted card still there: %v", err)
	}

	// Copy and move a card to another board.
	b2, _ := o.CreateBoard(ctx, T, checker, BoardInput{Name: "Compliance calendar"})
	cp, err := o.CopyCard(ctx, T, checker, b.ID, c1.ID, CardMove{Board: b2.ID, List: b2.Lists[0].ID}, "", []string{"checklists", "members"})
	if err != nil || cp.Board != b2.ID || len(cp.Checklists) != 1 || cp.Checklists[0].Items[0].Done != true {
		t.Fatalf("copy %+v %v", cp, err)
	}
	mv, err := o.MoveCard(ctx, T, checker, b.ID, c1.ID, CardMove{Board: b2.ID, List: b2.Lists[1].ID})
	if err != nil || mv.Board != b2.ID || len(mv.Comments) != 1 || len(mv.Attachments) != 1 {
		t.Fatalf("move to board %+v %v", mv, err)
	}
	if _, _, err := o.GetCard(ctx, T, checker, b.ID, c1.ID); err == nil || !strings.Contains(err.Error(), "moved") {
		t.Fatalf("old card %v", err)
	}

	// Activity, the change counter, copying a board with cards, close and delete.
	acts, _ := o.BoardActivityList(ctx, T, checker, b.ID, "", 0, 0)
	if len(acts) < 10 || acts[0].Seq < acts[len(acts)-1].Seq {
		t.Fatalf("activity %d", len(acts))
	}
	rev, _ := o.BoardRev(ctx, T, checker, b.ID)
	if rev != acts[0].Seq {
		t.Fatalf("rev %d, last activity %d", rev, acts[0].Seq)
	}
	b3, err := o.CopyBoard(ctx, T, checker, b2.ID, "Compliance calendar 2027", true)
	if err != nil {
		t.Fatal(err)
	}
	if v3, _ := o.GetBoard(ctx, T, checker, b3.ID, false); len(v3.Cards) != 2 {
		t.Fatalf("copied board cards %d", len(v3.Cards))
	}
	if err := o.DeleteBoard(ctx, T, maker, b.ID); err == nil {
		t.Fatal("deleted an open board")
	}
	if _, err := o.SetBoardClosed(ctx, T, maker, b.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := o.AddList(ctx, T, maker, b.ID, "Later", nanF()); !errors.Is(err, ErrState) {
		t.Fatalf("changed a closed board: %v", err)
	}
	if err := o.DeleteBoard(ctx, T, maker, b.ID); err != nil {
		t.Fatal(err)
	}
	for _, x := range func() []BoardSummary { bs, _ := o.Boards(ctx, T, maker); return bs }() {
		if x.ID == b.ID {
			t.Fatalf("maker still sees the deleted board %+v", x)
		}
	}
	ids := map[string]bool{}
	for i := 0; i < 200; i++ {
		ids[boardID("C-", 6)] = true
	}
	if len(ids) != 200 {
		t.Fatalf("ids collide: %d", len(ids))
	}
}

func nanF() float64 { var z float64; return z / z }

func TestBoardDueReminders(t *testing.T) {
	o, ctx := setup(t)
	b, _ := o.CreateBoard(ctx, T, "priya.nair", BoardInput{Name: "Reminders"})
	c, _ := o.AddCard(ctx, T, "priya.nair", b.ID, CardInput{List: b.Lists[0].ID, Title: "File the SEBI half-yearly return"})
	due, rem, ms := "2026-11-20T15:00", 120, []string{"farhan.khan"}
	if _, err := o.UpdateCard(ctx, T, "priya.nair", b.ID, c.ID, CardPatch{Due: &due, Reminder: &rem, Members: &ms}); err != nil {
		t.Fatal(err)
	}
	at := func(s string) time.Time { tm, _ := time.ParseInLocation("2006-01-02T15:04", s, ist); return tm }
	o.boardsTick(ctx, T, at("2026-11-20T12:30"))
	if _, n, _ := o.BoardNotices(ctx, T, "farhan.khan", 0); n != 1 { // added to the card only
		t.Fatalf("before the reminder time: %d", n)
	}
	o.boardsTick(ctx, T, at("2026-11-20T13:05"))
	o.boardsTick(ctx, T, at("2026-11-20T13:06"))
	ns, n, _ := o.BoardNotices(ctx, T, "farhan.khan", 0)
	if n != 2 || !strings.Contains(ns[0].Text, "is due") {
		t.Fatalf("reminders %d %+v", n, ns)
	}
	if ps, _, _ := o.BoardNotices(ctx, T, "priya.nair", 0); len(ps) != 1 {
		t.Fatalf("the watcher who set it %+v", ps)
	}
}

// A change that leaves the description out keeps it.
func TestBoardDescriptionKept(t *testing.T) {
	o, ctx := setup(t)
	d := "Close the month by the 5th"
	b, err := o.CreateBoard(ctx, T, "priya.nair", BoardInput{Name: "Month end", Description: &d})
	if err != nil {
		t.Fatal(err)
	}
	if b, err = o.UpdateBoard(ctx, T, "priya.nair", b.ID, BoardInput{Background: "green"}); err != nil || b.Description != d || b.Background != "green" {
		t.Fatalf("%+v %v", b, err)
	}
	empty := ""
	if b, _ = o.UpdateBoard(ctx, T, "priya.nair", b.ID, BoardInput{Description: &empty}); b.Description != "" {
		t.Fatalf("description not cleared: %q", b.Description)
	}
}
