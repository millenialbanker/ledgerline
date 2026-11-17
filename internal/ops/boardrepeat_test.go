package ops

import (
	"strings"
	"testing"
	"time"

	"ledgerline/internal/docs"
)

func TestBoardCardRepeatSchedules(t *testing.T) {
	o, ctx, _ := baSetup(t, "2026-11-16T08:00") // a Monday
	set, _ := o.Settings(ctx, T)
	set.Holidays = append(set.Holidays, "2026-11-17", "2026-11-30")
	from := baAt("2026-11-16T08:00")
	next := func(r CardRepeat, n int) string {
		t.Helper()
		var out []string
		for _, x := range r.upcoming(from, set, n) {
			out = append(out, x.Format("2006-01-02 15:04"))
		}
		return strings.Join(out, ", ")
	}
	for _, x := range []struct {
		r    CardRepeat
		want string
		says string
	}{
		{CardRepeat{Every: "day", At: "07:30"}, "2026-11-17 07:30, 2026-11-18 07:30", "Repeats every day"},
		{CardRepeat{Every: "weekday", At: "09:00"}, "2026-11-16 09:00, 2026-11-17 09:00", "Repeats every weekday (Monday to Friday)"},
		// Working days leave out the firm's trading holidays.
		{CardRepeat{Every: "working_day", At: "09:00"}, "2026-11-16 09:00, 2026-11-18 09:00", "Repeats every working day"},
		{CardRepeat{Every: "weekly", Days: []int{4, 1}, At: "10:00"}, "2026-11-16 10:00, 2026-11-19 10:00, 2026-11-23 10:00", "Repeats every week on Monday and Thursday"},
		{CardRepeat{Every: "monthly", Which: "day", Day: 31, At: "10:00"}, "2026-11-30 10:00, 2026-12-31 10:00, 2027-01-31 10:00", "Repeats every month on day 31"},
		// The last working day of November 2026 is the 27th once the 30th is a holiday.
		{CardRepeat{Every: "monthly", Which: "last_working", At: "10:00"}, "2026-11-27 10:00, 2026-12-31 10:00", "Repeats every month on the last working day"},
		{CardRepeat{Every: "monthly", Which: "first_working", At: "10:00"}, "2026-12-01 10:00, 2027-01-01 10:00", "Repeats every month on the first working day"},
		{CardRepeat{Every: "quarterly", Which: "day", Day: 15, Month: 1, At: "11:00"}, "2027-01-15 11:00, 2027-04-15 11:00", "Repeats every quarter on day 15 of its first month"},
		{CardRepeat{Every: "quarterly", Which: "last_working", Month: 3, At: "11:00"}, "2026-12-31 11:00, 2027-03-31 11:00", "Repeats every quarter on the last working day of its third month"},
		{CardRepeat{Every: "yearly", Which: "day", Day: 31, Month: 3, At: "12:00"}, "2027-03-31 12:00, 2028-03-31 12:00", "Repeats every year on 31 March"},
		// Ends: after N copies (counting those made), or on a date.
		{CardRepeat{Every: "day", At: "07:30", EndAfter: 3, Made: 2}, "2026-11-17 07:30", "Repeats every day"},
		{CardRepeat{Every: "day", At: "07:30", EndOn: "2026-11-18"}, "2026-11-17 07:30, 2026-11-18 07:30", "Repeats every day"},
	} {
		if got := next(x.r, 3); !strings.HasPrefix(got, x.want) || (x.r.EndAfter+len(x.r.EndOn) > 0 && got != x.want) {
			t.Errorf("%+v: next %s, want %s", x.r, got, x.want)
		}
		if got := x.r.Sentence(); got != x.says {
			t.Errorf("%+v says %q, want %q", x.r, got, x.says)
		}
	}
}

func TestBoardCardRepeat(t *testing.T) {
	o, ctx, b := baSetup(t, "2026-11-16T08:00") // a Monday
	maker, checker, risk := "priya.nair", "farhan.khan", "anita.rao"
	todo, doing := b.Lists[0].ID, b.Lists[1].ID
	lab := b.Labels[0].ID
	b, err := o.UpdateBoard(ctx, T, maker, b.ID, BoardInput{Fields: []BoardField{{Name: "Period", Type: "text"}}})
	if err != nil {
		t.Fatal(err)
	}
	fid := b.Fields[0].ID
	c, err := o.AddCard(ctx, T, maker, b.ID, CardInput{List: todo, Title: "Monthly surveillance report", Description: "Alerts by name", Labels: []string{lab}, Members: []string{checker}, Fields: map[string]string{fid: "Oct 2026"}, Due: "2026-11-10"})
	if err != nil {
		t.Fatal(err)
	}
	c, _ = o.EditChecklist(ctx, T, maker, b.ID, c.ID, ChecklistOp{Op: "add_list", Name: "Steps"})
	cl := c.Checklists[0].ID
	c, _ = o.EditChecklist(ctx, T, maker, b.ID, c.ID, ChecklistOp{Op: "add_item", Checklist: cl, Name: "Alerts reviewed"})
	done := true
	c, _ = o.EditChecklist(ctx, T, maker, b.ID, c.ID, ChecklistOp{Op: "update_item", Checklist: cl, Item: c.Checklists[0].Items[0].ID, Done: &done})

	// Observers read repeats; they do not set them.
	if _, err := o.SaveCardRepeat(ctx, T, risk, b.ID, c.ID, CardRepeat{Every: "day", At: "09:00"}); err == nil {
		t.Fatal("an observer set a repeat")
	}
	for _, bad := range []CardRepeat{{Every: "day", At: "9am"}, {Every: "weekly", At: "09:00"}, {Every: "monthly", Which: "day", Day: 32, At: "09:00"},
		{Every: "quarterly", Which: "day", Day: 1, Month: 4, At: "09:00"}, {Every: "fortnightly", At: "09:00"}, {Every: "day", At: "09:00", List: "L-nope"},
		{Every: "day", At: "09:00", EndOn: "2026-11-01"}, {Every: "day", At: "09:00", EndAfter: 2, EndOn: "2026-12-01"}} {
		if _, err := o.SaveCardRepeat(ctx, T, checker, b.ID, c.ID, bad); err == nil {
			t.Errorf("saved %+v", bad)
		}
	}
	r, err := o.SaveCardRepeat(ctx, T, checker, b.ID, c.ID, CardRepeat{Every: "monthly", Which: "last_working", At: "10:00", List: doing, Due: true, DueDays: 5, DueTime: "17:00", EndAfter: 2})
	if err != nil {
		t.Fatal(err)
	}
	if r.Text != "Repeats every month on the last working day" || r.Next != "2026-11-30T10:00:00+05:30" || len(r.Upcoming) != 2 || !strings.Contains(r.Where, "Copies go to Doing, due 5 days after they are made at 17:00 · ends after 2 copies") {
		t.Fatalf("repeat %+v", r)
	}
	rs, err := o.BoardRepeats(ctx, T, risk, b.ID)
	if err != nil || len(rs) != 1 || rs[0].Card != c.ID {
		t.Fatalf("repeats %+v %v", rs, err)
	}
	cards := func() []Card {
		v, _ := o.GetBoard(ctx, T, maker, b.ID, false)
		return v.Cards
	}
	rpTick(ctx, o, T, baAt("2026-11-30T09:59"))
	if n := len(cards()); n != 1 {
		t.Fatalf("copied early: %d cards", n)
	}
	rpTick(ctx, o, T, baAt("2026-11-30T10:00"))
	cs := cards()
	if len(cs) != 2 {
		t.Fatalf("at 10:00: %d cards", len(cs))
	}
	cp := cs[1]
	if cp.ID == c.ID {
		cp = cs[0]
	}
	if cp.Title != c.Title || cp.List != doing || cp.Due != "2026-12-05T17:00" || cp.DueDone || cp.Description != c.Description || strings.Join(cp.Labels, ",") != lab ||
		strings.Join(cp.Members, ",") != checker || cp.Fields[fid] != "Oct 2026" || len(cp.Checklists) != 1 || cp.Checklists[0].Items[0].Done || cp.CreatedBy != AutomationActor {
		t.Fatalf("copy %+v", cp)
	}
	// The member hears of the copy.
	ns, _, _ := o.BoardNotices(ctx, T, checker, 0)
	if len(ns) == 0 || ns[0].Card != cp.ID || ns[0].Kind != NoticeAdded {
		t.Fatalf("notices %+v", ns)
	}
	// Idempotent: later minutes, and a restart, do not copy again.
	rpTick(ctx, o, T, baAt("2026-11-30T10:01"))
	o2 := New(o.App)
	o2.Now = o.Now
	rpTick(ctx, o2, T, baAt("2026-11-30T10:02"))
	if n := len(cards()); n != 2 {
		t.Fatalf("copied again: %d cards", n)
	}
	got, _, _ := docs.Get[CardRepeat](ctx, o.Docs, T, cBoardRepeat, rpKey(b.ID, c.ID))
	if got.Made != 1 || got.LastNo != cp.No || got.Next != "2026-12-31T10:00:00+05:30" || got.LastSlot != "2026-11-30T10:00" {
		t.Fatalf("after the copy %+v", got)
	}
	// The copy does not repeat.
	if rs, _ := o.BoardRepeats(ctx, T, maker, b.ID); len(rs) != 1 {
		t.Fatalf("repeats after the copy: %d", len(rs))
	}
	// Ledgerline was down from 30 Dec to 5 Feb: one copy, for the latest time missed (30 Jan), and it ends after 2.
	rpTick(ctx, o, T, baAt("2027-02-05T12:00"))
	if n := len(cards()); n != 3 {
		t.Fatalf("after the gap: %d cards, want one more", n)
	}
	got, _, _ = docs.Get[CardRepeat](ctx, o.Docs, T, cBoardRepeat, rpKey(b.ID, c.ID))
	if got.Made != 2 || got.LastSlot != "2027-01-29T10:00" || !strings.Contains(got.Ended, "made the 2 copies") {
		t.Fatalf("after the gap %+v", got)
	}
	acts, _ := o.BoardActivityList(ctx, T, maker, b.ID, c.ID, 0, 50)
	late := false
	for _, a := range acts {
		late = late || (a.What == "card.repeated" && strings.Contains(a.Text, "late: Ledgerline was not running at 29 Jan 10:00"))
	}
	if !late {
		t.Fatalf("activity does not say the copy was late: %+v", acts)
	}
	rpTick(ctx, o, T, baAt("2027-02-26T10:00"))
	if n := len(cards()); n != 3 {
		t.Fatalf("ran after it ended: %d cards", n)
	}
}

func TestBoardCardRepeatStops(t *testing.T) {
	o, ctx, b := baSetup(t, "2026-11-16T08:00")
	maker := "priya.nair"
	todo := b.Lists[0].ID
	add := func(title string) Card {
		t.Helper()
		c, err := o.AddCard(ctx, T, maker, b.ID, CardInput{List: todo, Title: title})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	daily := CardRepeat{Every: "day", At: "09:00"}
	a, bb, c, d := add("Archived later"), add("Deleted later"), add("Board closed"), add("Stopped")
	for _, x := range []Card{a, bb, c, d} {
		if _, err := o.SaveCardRepeat(ctx, T, maker, b.ID, x.ID, daily); err != nil {
			t.Fatal(err)
		}
	}
	count := func(title string) int {
		cs, _ := o.boardCards(ctx, T, b.ID)
		n := 0
		for _, x := range cs {
			if x.Title == title && x.CreatedBy == AutomationActor {
				n++
			}
		}
		return n
	}
	yes := true
	if _, err := o.UpdateCard(ctx, T, maker, b.ID, a.ID, CardPatch{Archived: &yes}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.UpdateCard(ctx, T, maker, b.ID, bb.ID, CardPatch{Archived: &yes}); err != nil {
		t.Fatal(err)
	}
	if err := o.DeleteCard(ctx, T, maker, b.ID, bb.ID); err != nil {
		t.Fatal(err)
	}
	if err := o.StopCardRepeat(ctx, T, maker, b.ID, d.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := o.SetBoardClosed(ctx, T, maker, b.ID, true); err != nil {
		t.Fatal(err)
	}
	rpTick(ctx, o, T, baAt("2026-11-16T09:00"))
	for _, x := range []Card{a, bb, c, d} {
		if n := count(x.Title); n != 0 {
			t.Errorf("%s: %d copies", x.Title, n)
		}
	}
	ra, _, _ := docs.Get[CardRepeat](ctx, o.Docs, T, cBoardRepeat, rpKey(b.ID, a.ID))
	rb, _, _ := docs.Get[CardRepeat](ctx, o.Docs, T, cBoardRepeat, rpKey(b.ID, bb.ID))
	rc, _, _ := docs.Get[CardRepeat](ctx, o.Docs, T, cBoardRepeat, rpKey(b.ID, c.ID))
	if ra.Ended != "the card was archived" || rb.Ended == "" || rc.Ended != "" || rc.LastStatus != "skipped" || rc.Next != "2026-11-17T09:00:00+05:30" {
		t.Fatalf("archived %q, deleted %q, closed board %+v", ra.Ended, rb.Ended, rc)
	}
	// Reopened, the board's repeat carries on from its next time (no catching up on the closed days).
	rpTick(ctx, o, T, baAt("2026-11-17T09:00"))
	if _, err := o.SetBoardClosed(ctx, T, maker, b.ID, false); err != nil {
		t.Fatal(err)
	}
	rpTick(ctx, o, T, baAt("2026-11-18T08:00"))
	if n := count(c.Title); n != 0 {
		t.Fatalf("caught up the closed days: %d copies", n)
	}
	rpTick(ctx, o, T, baAt("2026-11-18T09:00"))
	if n := count(c.Title); n != 1 {
		t.Fatalf("after reopening: %d copies", n)
	}
	// Not on linked cards, and not on Ledgerline boards.
	link := add("Linked")
	if _, err := o.UpdateCard(ctx, T, maker, b.ID, link.ID, CardPatch{Link: &CardLink{Kind: "client", ID: "BH1001", Label: "BH1001"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.SaveCardRepeat(ctx, T, maker, b.ID, link.ID, daily); err == nil || !strings.Contains(err.Error(), "linked") {
		t.Fatalf("repeat on a linked card: %v", err)
	}
	ll := Board{ID: "B-ll", Name: "Work queue", Kind: BoardLedgerline, Visibility: "firm", Lists: []BoardList{{ID: "L-1", Name: "Open", Pos: 1}}, CreatedBy: "system", CreatedAt: time.Now().Format(time.RFC3339)}
	if err := docs.Upsert(ctx, o.Docs, T, cBoard, ll.ID, ll, "system"); err != nil {
		t.Fatal(err)
	}
	lc, err := o.addCard(ctx, T, "admin", ll, "L-1", Card{Title: "A queue item"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.SaveCardRepeat(ctx, T, "admin", ll.ID, lc.ID, daily); err == nil || !strings.Contains(err.Error(), "Ledgerline board") {
		t.Fatalf("repeat on a Ledgerline board: %v", err)
	}
}
