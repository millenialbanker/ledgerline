package ops

import (
	"testing"
	"time"
)

func TestBoardMyCards(t *testing.T) {
	o, ctx := setup(t)
	// Wednesday 18 November 2026, 11:00 IST: the week ends on Sunday the 22nd.
	o.Now = func() time.Time { return time.Date(2026, 11, 18, 11, 0, 0, 0, ist) }
	me, other := "priya.nair", "farhan.khan"

	b, err := o.CreateBoard(ctx, T, me, BoardInput{Name: "Month-end close", Visibility: "private"})
	if err != nil {
		t.Fatal(err)
	}
	todo := b.Lists[0].ID
	mine := []string{me}
	add := func(b Board, title, due string, members []string) Card {
		t.Helper()
		c, err := o.AddCard(ctx, T, b.CreatedBy, b.ID, CardInput{List: b.Lists[0].ID, Title: title})
		if err != nil {
			t.Fatal(err)
		}
		p := CardPatch{Members: &members}
		if due != "" {
			p.Due = &due
		}
		if c, err = o.UpdateCard(ctx, T, b.CreatedBy, b.ID, c.ID, p); err != nil {
			t.Fatal(err)
		}
		return c
	}
	yesterday := add(b, "Post interest on debit balances", "2026-11-17", mine)
	thisMorning := add(b, "Upload the UCC file", "2026-11-18T09:00", mine)
	today := add(b, "DP bills with GST", "2026-11-18", mine)
	tonight := add(b, "Reconcile the bank", "2026-11-18T17:00", mine)
	sunday := add(b, "AP commission", "2026-11-22T10:00", mine)
	monday := add(b, "Half-yearly return", "2026-11-23", mine)
	none := add(b, "Tidy the master", "", mine)
	add(b, "Someone else's card", "2026-11-18", []string{other})

	// A completed card is counted, not listed; an archived one is neither.
	done := add(b, "Filed the margin report", "2026-11-10", mine)
	yes := true
	if _, err := o.UpdateCard(ctx, T, me, b.ID, done.ID, CardPatch{DueDone: &yes}); err != nil {
		t.Fatal(err)
	}
	gone := add(b, "Old card", "2026-11-01", mine)
	if _, err := o.UpdateCard(ctx, T, me, b.ID, gone.ID, CardPatch{Archived: &yes}); err != nil {
		t.Fatal(err)
	}
	// A label and a checklist come along.
	labels := []string{b.Labels[3].ID}
	if _, err := o.UpdateCard(ctx, T, me, b.ID, yesterday.ID, CardPatch{Labels: &labels}); err != nil {
		t.Fatal(err)
	}
	c, _ := o.EditChecklist(ctx, T, me, b.ID, yesterday.ID, ChecklistOp{Op: "add_list", Name: "Steps"})
	for _, it := range []string{"Run interest", "Check the rates"} {
		c, _ = o.EditChecklist(ctx, T, me, b.ID, yesterday.ID, ChecklistOp{Op: "add_item", Checklist: c.Checklists[0].ID, Name: it})
	}
	if _, err := o.EditChecklist(ctx, T, me, b.ID, yesterday.ID, ChecklistOp{Op: "update_item", Checklist: c.Checklists[0].ID, Item: c.Checklists[0].Items[0].ID, Done: &yes}); err != nil {
		t.Fatal(err)
	}

	// A private board of someone else's, where a card names me: I cannot
	// see the board, so the card is not mine to list.
	hidden, _ := o.CreateBoard(ctx, T, other, BoardInput{Name: "Farhan's private board", Visibility: "private"})
	add(hidden, "Not for Priya's eyes", "2026-11-18", mine)
	// A closed board's cards are left out.
	closed, _ := o.CreateBoard(ctx, T, me, BoardInput{Name: "Last year", Visibility: "private"})
	add(closed, "On a closed board", "2026-11-18", mine)
	if _, err := o.SetBoardClosed(ctx, T, me, closed.ID, true); err != nil {
		t.Fatal(err)
	}

	v, err := o.MyCards(ctx, T, me)
	if err != nil {
		t.Fatal(err)
	}
	if v.Today != "2026-11-18" || v.WeekTo != "2026-11-22" || v.Done != 1 || v.More {
		t.Fatalf("view %+v", v)
	}
	want := []struct {
		id, when string
	}{{yesterday.ID, "overdue"}, {thisMorning.ID, "overdue"}, {tonight.ID, "today"}, {today.ID, "today"}, {sunday.ID, "week"}, {monday.ID, "later"}, {none.ID, "none"}}
	if len(v.Cards) != len(want) {
		t.Fatalf("%d cards, want %d: %+v", len(v.Cards), len(want), v.Cards)
	}
	for i, w := range want {
		if v.Cards[i].Card != w.id || v.Cards[i].When != w.when {
			t.Errorf("card %d: %s %s (%s), want %s %s", i, v.Cards[i].Card, v.Cards[i].When, v.Cards[i].Title, w.id, w.when)
		}
	}
	if n := v.Counts; n["overdue"] != 2 || n["today"] != 2 || n["week"] != 1 || n["later"] != 1 || n["none"] != 1 {
		t.Fatalf("counts %+v", n)
	}
	first := v.Cards[0]
	if first.BoardName != b.Name || first.List != todo || first.ListName != "To do" || len(first.Labels) != 1 || first.Labels[0].Color != b.Labels[3].Color || first.Items != 2 || first.ItemsDone != 1 {
		t.Fatalf("first card %+v", first)
	}

	// Farhan sees his own card on his private board, not Priya's.
	fv, _ := o.MyCards(ctx, T, other)
	if len(fv.Cards) != 0 {
		t.Fatalf("farhan's cards %+v", fv.Cards)
	}
	if _, err := o.UpdateBoard(ctx, T, me, b.ID, BoardInput{Members: []BoardPerson{{me, BoardAdmin}, {other, BoardObserver}}}); err != nil {
		t.Fatal(err)
	}
	if fv, _ = o.MyCards(ctx, T, other); len(fv.Cards) != 1 || fv.Cards[0].When != "today" || fv.Cards[0].BoardName != b.Name {
		t.Fatalf("farhan's cards once on the board %+v", fv.Cards)
	}

	// A Sunday: the week is that day only.
	if got := myWeekEnd(time.Date(2026, 11, 22, 20, 0, 0, 0, ist)); got != "2026-11-22" {
		t.Fatalf("week end on a Sunday %s", got)
	}
	if got := myCardWhen("2026-11-23T09:00", time.Date(2026, 11, 22, 20, 0, 0, 0, ist)); got != "later" {
		t.Fatalf("Monday seen from Sunday %s", got)
	}
}
