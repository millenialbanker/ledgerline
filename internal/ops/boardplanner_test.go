package ops

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// No one but its owner can see an Inbox, firm Admins and Ledgerline's own
// actors included; nothing about it reaches anyone else.
func TestBoardInboxPrivacy(t *testing.T) {
	o, ctx := setup(t)
	o.Now = func() time.Time { return time.Date(2026, 11, 18, 11, 0, 0, 0, ist) }
	me, other, admin := "priya.nair", "farhan.khan", "admin"

	in, err := o.Inbox(ctx, T, me)
	if err != nil {
		t.Fatal(err)
	}
	if in.Board != inboxID(me) || in.List == "" || len(in.Cards) != 0 {
		t.Fatalf("new inbox %+v", in)
	}
	again, _ := o.EnsureInbox(ctx, T, me)
	if again.ID != in.Board || again.Kind != BoardInbox || len(again.Lists) != 1 {
		t.Fatalf("inbox made twice %+v", again)
	}
	c, err := o.AddInboxCard(ctx, T, me, CardInput{Title: "Call back about the margin shortfall, client Z-1234", Due: "2026-11-19T10:00"})
	if err != nil {
		t.Fatal(err)
	}
	c2, _ := o.AddInboxCard(ctx, T, me, CardInput{Title: "Second thought"})
	if in, _ = o.Inbox(ctx, T, me); len(in.Cards) != 2 || in.Cards[0].ID != c2.ID {
		t.Fatalf("inbox cards (newest on top) %+v", in.Cards)
	}

	// Nobody else reaches it: not another user, not the firm's Admin, not automation.
	for _, who := range []string{other, admin, AutomationActor, SystemActor, LedgyActor} {
		if _, err := o.GetBoard(ctx, T, who, in.Board, false); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s saw the inbox: %v", who, err)
		}
		if _, _, err := o.GetCard(ctx, T, who, in.Board, c.ID); err == nil {
			t.Fatalf("%s read an inbox card", who)
		}
		if _, err := o.UpdateCard(ctx, T, who, in.Board, c.ID, CardPatch{Title: bpPtr("changed")}); err == nil {
			t.Fatalf("%s changed an inbox card", who)
		}
		if _, err := o.BoardActivityList(ctx, T, who, in.Board, "", 0, 10); err == nil {
			t.Fatalf("%s read the inbox's activity", who)
		}
	}
	if _, err := o.EnsureInbox(ctx, T, AutomationActor); !errors.Is(err, ErrForbidden) {
		t.Fatalf("automation got an inbox: %v", err)
	}
	for _, who := range []string{other, admin} {
		bs, _ := o.Boards(ctx, T, who)
		for _, b := range bs {
			if b.ID == in.Board {
				t.Fatalf("%s lists the inbox", who)
			}
		}
		if hits, _ := o.SearchCards(ctx, T, who, "margin shortfall"); len(hits) != 0 {
			t.Fatalf("%s found the inbox card: %+v", who, hits)
		}
		if x, _ := o.CrossCards(ctx, T, who, XCardsQuery{}); x.Total != 0 {
			t.Fatalf("%s all boards %+v", who, x.Cards)
		}
		if ts, _ := o.BoardTemplates(ctx, T, who); len(ts) > 0 {
			for _, x := range ts {
				if x.ID == in.Board {
					t.Fatalf("inbox offered as a template to %s", who)
				}
			}
		}
	}
	// The owner does not see it on the boards home either, but finds its cards.
	if bs, _ := o.Boards(ctx, T, me); len(bs) != 0 {
		t.Fatalf("the inbox is on the boards home %+v", bs)
	}
	if hits, _ := o.SearchCards(ctx, T, me, "margin shortfall"); len(hits) != 1 || hits[0].Board != in.Board {
		t.Fatalf("owner search %+v", hits)
	}

	// It stays one person's: no members, never firm-wide, not a template,
	// one list, always open; only its owner on its cards.
	yes := true
	for _, bi := range []BoardInput{{Members: []BoardPerson{{me, BoardAdmin}, {other, BoardMember}}}, {Visibility: "firm"}, {Template: &yes}} {
		if _, err := o.UpdateBoard(ctx, T, me, in.Board, bi); err == nil {
			t.Fatalf("inbox changed with %+v", bi)
		}
	}
	if _, err := o.SetBoardMembers(ctx, T, me, in.Board, []BoardPerson{{me, BoardAdmin}, {other, BoardObserver}}); err == nil {
		t.Fatal("someone else put on the inbox")
	}
	if _, err := o.JoinBoard(ctx, T, other, in.Board); !errors.Is(err, ErrNotFound) {
		t.Fatalf("farhan joined the inbox: %v", err)
	}
	if _, err := o.WatchBoard(ctx, T, other, in.Board, true); err == nil {
		t.Fatal("farhan watches the inbox")
	}
	if _, err := o.AddList(ctx, T, me, in.Board, "Later", nanF()); err == nil {
		t.Fatal("a second list on the inbox")
	}
	if _, err := o.SetBoardClosed(ctx, T, me, in.Board, true); err == nil {
		t.Fatal("the inbox was closed")
	}
	if _, err := o.UpdateCard(ctx, T, me, in.Board, c.ID, CardPatch{Members: &[]string{me, other}}); err == nil {
		t.Fatal("someone else put on an inbox card")
	}
	if _, err := o.UpdateCard(ctx, T, me, in.Board, c.ID, CardPatch{Members: &[]string{me}}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.SaveBoardAuto(ctx, T, me, in.Board, BoardAuto{Kind: AutoRule, Name: "x"}); err == nil {
		t.Fatal("automation saved on the inbox")
	}

	// A mention on an inbox card tells no one; nor does a checklist item.
	before, _, _ := o.BoardNotices(ctx, T, other, 0)
	if _, err := o.AddComment(ctx, T, me, in.Board, c.ID, "ask @farhan.khan about this"); err != nil {
		t.Fatal(err)
	}
	cl, _ := o.EditChecklist(ctx, T, me, in.Board, c.ID, ChecklistOp{Op: "add_list", Name: "Steps"})
	cl, _ = o.EditChecklist(ctx, T, me, in.Board, c.ID, ChecklistOp{Op: "add_item", Checklist: cl.Checklists[0].ID, Name: "Ring"})
	_, _ = o.EditChecklist(ctx, T, me, in.Board, c.ID, ChecklistOp{Op: "update_item", Checklist: cl.Checklists[0].ID, Item: cl.Checklists[0].Items[0].ID, Member: &other})
	if after, _, _ := o.BoardNotices(ctx, T, other, 0); len(after) != len(before) {
		t.Fatalf("someone else was told about an inbox card: %+v", after)
	}
	// Due reminders reach the owner only.
	rem := 60
	if _, err := o.UpdateCard(ctx, T, me, in.Board, c.ID, CardPatch{Reminder: &rem}); err != nil {
		t.Fatal(err)
	}
	o.boardDueReminders(ctx, T, time.Date(2026, 11, 19, 9, 30, 0, 0, ist))
	if ns, _, _ := o.BoardNotices(ctx, T, me, 0); len(ns) == 0 || ns[0].Kind != NoticeDue {
		t.Fatalf("owner's reminder %+v", ns)
	}
	if after, _, _ := o.BoardNotices(ctx, T, other, 0); len(after) != len(before) {
		t.Fatalf("reminder to someone else %+v", after)
	}

	// Moving a card to a board takes it out of the inbox, with its checklist.
	b, _ := o.CreateBoard(ctx, T, me, BoardInput{Name: "Follow-ups", Visibility: "private"})
	moved, err := o.MoveCard(ctx, T, me, in.Board, c.ID, CardMove{Board: b.ID, List: b.Lists[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	if moved.Board != b.ID || moved.List != b.Lists[1].ID || len(moved.Checklists) != 1 || moved.Due != "2026-11-19T10:00" {
		t.Fatalf("moved card %+v", moved)
	}
	if in, _ = o.Inbox(ctx, T, me); len(in.Cards) != 1 || in.Cards[0].ID != c2.ID {
		t.Fatalf("inbox after the move %+v", in.Cards)
	}
	// Another person's inbox is theirs: same rules the other way.
	theirs, _ := o.Inbox(ctx, T, other)
	if theirs.Board == in.Board {
		t.Fatal("two people share an inbox")
	}
	if _, err := o.GetBoard(ctx, T, me, theirs.Board, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("priya saw farhan's inbox: %v", err)
	}
}

func bpPtr[T any](v T) *T { return &v }

// Plan blocks: made for a card the person can see, moved, resized and
// removed by their owner only, and following a card moved out of the Inbox.
func TestBoardPlanBlocks(t *testing.T) {
	o, ctx := setup(t)
	o.Now = func() time.Time { return time.Date(2026, 11, 18, 11, 0, 0, 0, ist) } // a Wednesday
	me, other := "priya.nair", "farhan.khan"
	b, _ := o.CreateBoard(ctx, T, me, BoardInput{Name: "Month-end close", Visibility: "private"})
	todo := b.Lists[0].ID
	c, _ := o.AddCard(ctx, T, me, b.ID, CardInput{List: todo, Title: "Post interest", Members: []string{me}})
	dated, _ := o.AddCard(ctx, T, me, b.ID, CardInput{List: todo, Title: "Bank reconciliation", Members: []string{me}, Due: "2026-11-20"})
	o.AddCard(ctx, T, me, b.ID, CardInput{List: todo, Title: "Not mine"})

	start := "2026-11-18T14:00"
	p, err := o.CreatePlanBlock(ctx, T, me, PlanBlockInput{Board: b.ID, Card: c.ID, Start: &start})
	if err != nil {
		t.Fatal(err)
	}
	if p.Minutes != 30 || p.Start != start || p.User != me {
		t.Fatalf("new block %+v", p)
	}
	for _, bad := range []struct {
		start string
		mins  int
	}{{"2026-11-18", 30}, {"2026-11-18T14:00", 10}, {"2026-11-18T14:00", 721}, {"2026-11-18T23:30", 60}} {
		if _, err := o.CreatePlanBlock(ctx, T, me, PlanBlockInput{Board: b.ID, Card: c.ID, Start: &bad.start, Minutes: &bad.mins}); err == nil {
			t.Fatalf("block %+v accepted", bad)
		}
	}
	late, mins := "2026-11-18T23:30", 30 // ends at midnight
	if _, err := o.CreatePlanBlock(ctx, T, me, PlanBlockInput{Board: b.ID, Card: c.ID, Start: &late, Minutes: &mins}); err != nil {
		t.Fatal(err)
	}
	// Someone who cannot see the card cannot plan it.
	if _, err := o.CreatePlanBlock(ctx, T, other, PlanBlockInput{Board: b.ID, Card: c.ID, Start: &start}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("farhan planned a card he cannot see: %v", err)
	}
	// Move and resize.
	ns, nm := "2026-11-19T09:30", 90
	if p, err = o.UpdatePlanBlock(ctx, T, me, p.ID, PlanBlockInput{Start: &ns}); err != nil || p.Start != ns || p.Minutes != 30 {
		t.Fatalf("moved %+v %v", p, err)
	}
	if p, err = o.UpdatePlanBlock(ctx, T, me, p.ID, PlanBlockInput{Minutes: &nm}); err != nil || p.Minutes != 90 {
		t.Fatalf("resized %+v %v", p, err)
	}
	// Only its owner: to anyone else it does not exist.
	if _, err := o.UpdatePlanBlock(ctx, T, other, p.ID, PlanBlockInput{Start: &start}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("farhan moved priya's block: %v", err)
	}
	if err := o.DeletePlanBlock(ctx, T, other, p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("farhan removed priya's block: %v", err)
	}
	if _, err := o.PlanSetDue(ctx, T, other, p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("farhan used priya's block: %v", err)
	}
	if _, err := o.UpdatePlanBlock(ctx, T, other, me+"|"+p.ID, PlanBlockInput{Start: &start}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a key with a user in it: %v", err)
	}
	if v, _ := o.Planner(ctx, T, other, "2026-11-16", 7); len(v.Blocks) != 0 {
		t.Fatalf("farhan's planner shows %+v", v.Blocks)
	}
	// The card's dates are left alone until "Set as due date".
	got, _, _ := o.GetCard(ctx, T, me, b.ID, c.ID)
	if got.Due != "" || got.Start != "" {
		t.Fatalf("planning changed the card's dates %+v", got)
	}
	if got, err = o.PlanSetDue(ctx, T, me, p.ID); err != nil || got.Due != "2026-11-19T11:00" {
		t.Fatalf("set due %+v %v", got, err)
	}

	// The planner: the week from Monday, blocks with their cards, my dated
	// cards, my cards with no block this week, and my inbox.
	inc, _ := o.AddInboxCard(ctx, T, me, CardInput{Title: "Idea for the AP meeting"})
	v, err := o.Planner(ctx, T, me, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if v.From != "2026-11-16" || v.To != "2026-11-22" || v.Today != "2026-11-18" || len(v.Blocks) != 2 {
		t.Fatalf("planner %+v", v)
	}
	if bl := v.Blocks[1]; bl.ID != p.ID || bl.Card == nil || bl.Card.Title != "Post interest" || bl.Card.BoardName != b.Name || !bl.Card.CanEdit {
		t.Fatalf("block view %+v", v.Blocks[1])
	}
	if len(v.Dated) != 2 || len(v.Cards) != 1 || v.Cards[0].Card != dated.ID || len(v.Inbox) != 1 || v.Inbox[0].Card != inc.ID || !v.Inbox[0].Inbox {
		t.Fatalf("dated %+v cards %+v inbox %+v", v.Dated, v.Cards, v.Inbox)
	}
	// Next week nothing is planned, so the card is in the side panel.
	if nx, _ := o.Planner(ctx, T, me, "2026-11-23", 7); len(nx.Blocks) != 0 || len(nx.Cards) != 2 {
		t.Fatalf("next week %+v", nx)
	}

	// A planned inbox card moved to a board keeps its block.
	is := "2026-11-20T10:00"
	ip, err := o.CreatePlanBlock(ctx, T, me, PlanBlockInput{Board: v.InboxBoard, Card: inc.ID, Start: &is})
	if err != nil {
		t.Fatal(err)
	}
	moved, err := o.MoveCard(ctx, T, me, v.InboxBoard, inc.ID, CardMove{Board: b.ID, List: todo})
	if err != nil {
		t.Fatal(err)
	}
	v, _ = o.Planner(ctx, T, me, "2026-11-16", 7)
	found := false
	for _, bl := range v.Blocks {
		if bl.ID == ip.ID {
			found = bl.Card != nil && bl.Card.Board == b.ID && bl.Card.Card == moved.ID && !bl.Card.Inbox
		}
	}
	if !found {
		t.Fatalf("the block did not follow the card: %+v", v.Blocks)
	}
	if err := o.DeletePlanBlock(ctx, T, me, ip.ID); err != nil {
		t.Fatal(err)
	}
	if err := o.DeletePlanBlock(ctx, T, me, ip.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removed twice: %v", err)
	}
	if v, _ = o.Planner(ctx, T, me, "2026-11-16", 7); len(v.Blocks) != 2 {
		t.Fatalf("blocks after removing %+v", v.Blocks)
	}
}

// The compliance officer or an Admin may open a person's Inbox, read-only,
// giving a reason that is audited; the owner is told. No one else may.
func TestBoardInboxReview(t *testing.T) {
	o, ctx := setup(t)
	o.Now = func() time.Time { return time.Date(2026, 11, 18, 11, 0, 0, 0, ist) }
	me, comp, admin := "priya.nair", "vikram.shah", "admin"
	c, err := o.AddInboxCard(ctx, T, me, CardInput{Title: "Client Z-1234 asked to sell 100 shares tomorrow"})
	if err != nil {
		t.Fatal(err)
	}
	ib := inboxID(me)
	o.AddComment(ctx, T, me, ib, c.ID, "called back at 4 pm")
	yes := true
	old, _ := o.AddInboxCard(ctx, T, me, CardInput{Title: "Old note"})
	o.UpdateCard(ctx, T, me, ib, old.ID, CardPatch{Archived: &yes})
	reason := "NSE inspection: produce staff notes of client instructions"

	// Other roles may not, Branch and AP staff included, whatever the reason.
	for _, who := range []string{"farhan.khan", "anita.rao", "meera.iyer", "sunrise.ap", AutomationActor} {
		if _, err := o.OpenInboxForReview(ctx, T, who, me, reason); err == nil {
			t.Fatalf("%s opened priya's Inbox", who)
		}
	}
	// Not without a reason.
	for _, r := range []string{"", "  ", "why"} {
		if _, err := o.OpenInboxForReview(ctx, T, comp, me, r); err == nil {
			t.Fatalf("opened with the reason %q", r)
		}
	}
	if _, err := o.OpenInboxForReview(ctx, T, comp, "farhan.khan", reason); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an Inbox never used: %v", err)
	}
	before, _ := o.App.Audit.List(ctx, T, 0)
	for _, e := range before {
		if e.Action == "board.inbox_opened" {
			t.Fatal("a refused opening was recorded as an opening")
		}
	}
	notes0, _, _ := o.BoardNotices(ctx, T, me, 0)

	v, err := o.OpenInboxForReview(ctx, T, comp, me, reason)
	if err != nil {
		t.Fatal(err)
	}
	if v.Owner != me || v.OwnerName != "Priya Nair" || v.Board != ib || len(v.Cards) != 2 || len(v.Activity) == 0 {
		t.Fatalf("review %+v", v)
	}
	var got Card
	for _, x := range v.Cards {
		if x.ID == c.ID {
			got = x
		}
	}
	if got.Title != c.Title || len(got.Comments) != 1 {
		t.Fatalf("card in review %+v", got)
	}
	// The sealed audit trail has who, whose and why.
	evs, _ := o.App.Audit.List(ctx, T, 0)
	found := false
	for _, e := range evs {
		found = found || (e.Action == "board.inbox_opened" && e.Actor == comp && e.Object == ib && fmt.Sprint(e.Detail["owner"]) == me && fmt.Sprint(e.Detail["reason"]) == reason)
	}
	if !found {
		t.Fatal("opening the Inbox not audited with the reason")
	}
	// The owner is told who and why.
	notes, _, _ := o.BoardNotices(ctx, T, me, 0)
	if len(notes) != len(notes0)+1 || notes[0].Text != "Vikram Shah (Compliance) opened your Inbox: "+reason || notes[0].By != comp {
		t.Fatalf("owner's notice %+v", notes)
	}
	if _, err := o.OpenInboxForReview(ctx, T, admin, me, "Internal audit of the AP desk"); err != nil {
		t.Fatal(err)
	}
	if notes, _, _ = o.BoardNotices(ctx, T, me, 0); !strings.HasPrefix(notes[0].Text, "Administrator (Admin) opened your Inbox: ") {
		t.Fatalf("admin's notice %q", notes[0].Text)
	}
	// Read-only: the reviewer still has no way in to change anything.
	if _, err := o.GetBoard(ctx, T, comp, ib, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the reviewer opened the Inbox as a board: %v", err)
	}
	if _, err := o.UpdateCard(ctx, T, comp, ib, c.ID, CardPatch{Title: bpPtr("changed")}); err == nil {
		t.Fatal("the reviewer changed a card")
	}
	if _, err := o.AddComment(ctx, T, comp, ib, c.ID, "seen"); err == nil {
		t.Fatal("the reviewer commented")
	}
	b, _ := o.CreateBoard(ctx, T, comp, BoardInput{Name: "Review", Visibility: "private"})
	if _, err := o.MoveCard(ctx, T, comp, ib, c.ID, CardMove{Board: b.ID, List: b.Lists[0].ID}); err == nil {
		t.Fatal("the reviewer moved a card out")
	}
	if _, _, err := o.GetCard(ctx, T, comp, ib, c.ID); err == nil {
		t.Fatal("the reviewer read a card through the board")
	}
	// The reviewer sees nothing of the Inbox elsewhere either.
	if hits, _ := o.SearchCards(ctx, T, comp, "Z-1234"); len(hits) != 0 {
		t.Fatalf("the reviewer's search %+v", hits)
	}
	// Not one's own.
	if _, err := o.OpenInboxForReview(ctx, T, comp, comp, reason); err == nil {
		t.Fatal("opened one's own Inbox for review")
	}
}
