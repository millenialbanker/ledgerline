package ops

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/docs"
)

func baAt(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02T15:04", s, ist)
	if err != nil {
		panic(err)
	}
	return t
}

// baSetup is a private board of the maker (admin), with the checker as a
// member and the risk manager as an observer, and the clock at now.
func baSetup(t *testing.T, now string) (*Ops, context.Context, Board) {
	t.Helper()
	o, ctx := setup(t)
	clock := baAt(now)
	o.Now = func() time.Time { return clock }
	b, err := o.CreateBoard(ctx, T, "priya.nair", BoardInput{Name: "Operations", Visibility: "private"})
	if err != nil {
		t.Fatal(err)
	}
	b, err = o.UpdateBoard(ctx, T, "priya.nair", b.ID, BoardInput{Members: []BoardPerson{{"priya.nair", BoardAdmin}, {"farhan.khan", BoardMember}, {"anita.rao", BoardObserver}}})
	if err != nil {
		t.Fatal(err)
	}
	return o, ctx, b
}

func baCard(t *testing.T, o *Ops, ctx context.Context, b Board, id string) Card {
	t.Helper()
	c, _, err := o.GetCard(ctx, T, "priya.nair", b.ID, id)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestBoardRuleMoveIntoList(t *testing.T) {
	o, ctx, b := baSetup(t, "2026-11-16T10:00")
	maker, checker, risk := "priya.nair", "farhan.khan", "anita.rao"
	todo, doing, done := b.Lists[0].ID, b.Lists[1].ID, b.Lists[2].ID
	lab := b.Labels[0].ID // green
	rule, err := o.SaveBoardAuto(ctx, T, maker, b.ID, BoardAuto{Kind: AutoRule, Name: "Done means complete",
		Trigger: &AutoTrigger{Event: "card_moved_in", List: done},
		Actions: []AutoAction{{Kind: "add_label", Label: lab}, {Kind: "set_due", Days: 2, Time: "17:00"}, {Kind: "mark_done"}, {Kind: "notify", To: []string{risk}, Text: "{card} is done ({list})"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !rule.On || rule.Seq != 1 || rule.Trigger.List != done {
		t.Fatalf("rule %+v", rule)
	}
	c, err := o.AddCard(ctx, T, checker, b.ID, CardInput{List: todo, Title: "Send the margin file"})
	if err != nil {
		t.Fatal(err)
	}
	// Moving into another list does nothing.
	if _, err := o.MoveCard(ctx, T, checker, b.ID, c.ID, CardMove{List: doing}); err != nil {
		t.Fatal(err)
	}
	if c = baCard(t, o, ctx, b, c.ID); len(c.Labels) != 0 || c.Due != "" {
		t.Fatalf("rule ran on a move into Doing: %+v", c)
	}
	if _, err := o.MoveCard(ctx, T, checker, b.ID, c.ID, CardMove{List: done}); err != nil {
		t.Fatal(err)
	}
	c = baCard(t, o, ctx, b, c.ID)
	if !contains(c.Labels, lab) || c.Due != "2026-11-18T17:00" || !c.DueDone {
		t.Fatalf("card after the rule: labels %v due %q done %v", c.Labels, c.Due, c.DueDone)
	}
	ns, _, _ := o.BoardNotices(ctx, T, risk, 0)
	if len(ns) == 0 || ns[0].Text != "Send the margin file is done (Done)" || ns[0].By != AutomationActor {
		t.Fatalf("risk notices %+v", ns)
	}
	// The log (an observer may read it) and the board's activity.
	runs, err := o.BoardAutoLog(ctx, T, risk, b.ID, "", 0, 0)
	if err != nil || len(runs) != 1 {
		t.Fatalf("log %+v %v", runs, err)
	}
	r := runs[0]
	if r.Status != "done" || r.Auto != rule.ID || r.CardNo != c.No || r.By != checker || !strings.Contains(r.Why, "moved into Done by farhan.khan") || len(r.Did) != 4 ||
		r.Did[0] != "added the green label to #1" || !strings.Contains(r.Did[1], "18 Nov 2026 17:00") || r.Did[3] != "notified anita.rao" {
		t.Fatalf("run %+v", r)
	}
	acts, _ := o.BoardActivityList(ctx, T, maker, b.ID, c.ID, 0, 0)
	var ran, labelled bool
	for _, a := range acts {
		ran = ran || (a.What == "automation.ran" && a.By == AutomationActor && strings.Contains(a.Text, "rule “Done means complete” ran on #1"))
		labelled = labelled || (a.What == "card.label_added" && a.By == AutomationActor)
	}
	if !ran || !labelled {
		t.Fatalf("activity %+v", acts)
	}
	v, _ := o.BoardAutomation(ctx, T, maker, b.ID)
	if len(v.Autos) != 1 || v.Autos[0].Runs != 1 || v.Autos[0].LastStatus != "done" || v.RunsToday != 1 || v.Cap != baDailyCap {
		t.Fatalf("view %+v", v)
	}
	// Switched off, it no longer runs.
	if _, err := o.SetBoardAutoOn(ctx, T, checker, b.ID, rule.ID, false); err != nil {
		t.Fatal(err)
	}
	c2, _ := o.AddCard(ctx, T, checker, b.ID, CardInput{List: todo, Title: "Second"})
	if _, err := o.MoveCard(ctx, T, checker, b.ID, c2.ID, CardMove{List: done}); err != nil {
		t.Fatal(err)
	}
	if c2 = baCard(t, o, ctx, b, c2.ID); len(c2.Labels) != 0 {
		t.Fatal("a switched-off rule ran")
	}
}

func TestBoardRuleConditions(t *testing.T) {
	o, ctx, b := baSetup(t, "2026-11-16T10:00")
	maker, checker := "priya.nair", "farhan.khan"
	todo, done := b.Lists[0].ID, b.Lists[2].ID
	l1, l2 := b.Labels[0].ID, b.Labels[1].ID
	save := func(a BoardAuto) BoardAuto {
		t.Helper()
		a, err := o.SaveBoardAuto(ctx, T, maker, b.ID, a)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	save(BoardAuto{Kind: AutoRule, Name: "SEBI filings without a date", Trigger: &AutoTrigger{Event: "card_added", List: todo},
		Conditions: []AutoCond{{Kind: "title_contains", Value: "SEBI"}, {Kind: "no_due"}}, Actions: []AutoAction{{Kind: "add_label", Label: l1}}})
	save(BoardAuto{Kind: AutoRule, Name: "Checker's cards", Trigger: &AutoTrigger{Event: "card_added"},
		Conditions: []AutoCond{{Kind: "has_member", Value: checker}, {Kind: "not_in_list", Value: done}}, Actions: []AutoAction{{Kind: "add_label", Label: l2}}})
	save(BoardAuto{Kind: AutoRule, Name: "Late", Trigger: &AutoTrigger{Event: "card_moved_in", List: done},
		Conditions: []AutoCond{{Kind: "overdue"}, {Kind: "lacks_label", Value: l2}}, Actions: []AutoAction{{Kind: "comment", Text: "{no} {card} was finished late, after {due}"}}})
	add := func(title, due string, members ...string) Card {
		t.Helper()
		c, err := o.AddCard(ctx, T, checker, b.ID, CardInput{List: todo, Title: title, Due: due, Members: members})
		if err != nil {
			t.Fatal(err)
		}
		return baCard(t, o, ctx, b, c.ID)
	}
	a := add("SEBI half-yearly return", "")
	bb := add("SEBI annual return", "2026-11-30")
	c := add("DP bills", "", checker)
	d := add("sebi quarterly", "", checker)
	for _, x := range []struct {
		c    Card
		want []string
	}{{a, []string{l1}}, {bb, nil}, {c, []string{l2}}, {d, []string{l1, l2}}} {
		if strings.Join(x.c.Labels, ",") != strings.Join(x.want, ",") {
			t.Errorf("%s: labels %v, want %v", x.c.Title, x.c.Labels, x.want)
		}
	}
	// Overdue and without l2: commented; overdue with l2, or not overdue: not.
	late := add("Close the October books", "2026-11-10")
	for _, x := range []Card{late, bb, d} {
		if _, err := o.MoveCard(ctx, T, checker, b.ID, x.ID, CardMove{List: done}); err != nil {
			t.Fatal(err)
		}
	}
	if late = baCard(t, o, ctx, b, late.ID); len(late.Comments) != 1 || late.Comments[0].By != AutomationActor || late.Comments[0].Text != "#5 Close the October books was finished late, after 10 Nov 2026" {
		t.Fatalf("late card comments %+v", late.Comments)
	}
	if bb, d = baCard(t, o, ctx, b, bb.ID), baCard(t, o, ctx, b, d.ID); len(bb.Comments)+len(d.Comments) != 0 {
		t.Fatal("commented on a card the conditions left out")
	}
}

func TestBoardRuleLoopGuard(t *testing.T) {
	o, ctx, b := baSetup(t, "2026-11-16T10:00")
	maker, checker := "priya.nair", "farhan.khan"
	todo, doing, done := b.Lists[0].ID, b.Lists[1].ID, b.Lists[2].ID
	ra, _ := o.SaveBoardAuto(ctx, T, maker, b.ID, BoardAuto{Kind: AutoRule, Name: "Doing to Done", Trigger: &AutoTrigger{Event: "card_moved_in", List: doing}, Actions: []AutoAction{{Kind: "move", List: done}}})
	rb, err := o.SaveBoardAuto(ctx, T, maker, b.ID, BoardAuto{Kind: AutoRule, Name: "Done to Doing", Trigger: &AutoTrigger{Event: "card_moved_in", List: done}, Actions: []AutoAction{{Kind: "move", List: doing, Top: true}}})
	if err != nil {
		t.Fatal(err)
	}
	c, _ := o.AddCard(ctx, T, checker, b.ID, CardInput{List: todo, Title: "Ping-pong"})
	if _, err := o.MoveCard(ctx, T, checker, b.ID, c.ID, CardMove{List: doing}); err != nil {
		t.Fatalf("the person's move failed: %v", err)
	}
	// A (depth 1) → B (2) → A (3) → B stopped.
	runs, _ := o.BoardAutoLog(ctx, T, maker, b.ID, "", 0, 0)
	if len(runs) != 4 || runs[0].Status != "stopped" || runs[0].Auto != rb.ID || runs[0].By != checker || !strings.Contains(runs[0].Error, "loop") {
		t.Fatalf("runs %+v", runs)
	}
	for i, r := range runs[1:] {
		want := []string{ra.ID, rb.ID, ra.ID}[i]
		if r.Status != "done" || r.Auto != want || r.By != checker {
			t.Fatalf("run %d %+v", i, r)
		}
	}
	if c = baCard(t, o, ctx, b, c.ID); c.List != done {
		t.Fatalf("card ended in %s", c.List)
	}
	if a, _, _ := docs.Get[BoardAuto](ctx, o.Docs, T, cBoardAuto, baKey(b.ID, rb.ID)); a.Stopped == "" || a.LastStatus != "done" || a.Runs != 1 {
		t.Fatalf("rule B %+v", a)
	}

	// A rule whose action fails leaves the person's change alone and says why.
	o.SetBoardAutoOn(ctx, T, maker, b.ID, ra.ID, false)
	o.SetBoardAutoOn(ctx, T, maker, b.ID, rb.ID, false)
	extra, _ := o.AddList(ctx, T, maker, b.ID, "Parked", nanF())
	bad, err := o.SaveBoardAuto(ctx, T, maker, b.ID, BoardAuto{Kind: AutoRule, Name: "Park it", Trigger: &AutoTrigger{Event: "label_added"}, Actions: []AutoAction{{Kind: "move", List: extra.ID}, {Kind: "mark_done"}}})
	if err != nil {
		t.Fatal(err)
	}
	yes := true
	if _, err := o.UpdateList(ctx, T, maker, b.ID, extra.ID, ListInput{Archived: &yes}); err != nil {
		t.Fatal(err)
	}
	ls := []string{b.Labels[2].ID}
	if c, err = o.UpdateCard(ctx, T, checker, b.ID, c.ID, CardPatch{Labels: &ls}); err != nil || len(c.Labels) != 1 {
		t.Fatalf("the person's change failed with the rule: %v", err)
	}
	runs, _ = o.BoardAutoLog(ctx, T, maker, b.ID, bad.ID, 0, 0)
	if len(runs) != 1 || runs[0].Status != "failed" || !strings.Contains(runs[0].Error, "open list") {
		t.Fatalf("failed run %+v", runs)
	}
	if c = baCard(t, o, ctx, b, c.ID); c.DueDone {
		t.Fatal("actions went on after one failed")
	}
}

func TestBoardCardButton(t *testing.T) {
	o, ctx, b := baSetup(t, "2026-11-16T10:00")
	maker, checker, risk := "priya.nair", "farhan.khan", "anita.rao"
	todo, doing := b.Lists[0].ID, b.Lists[1].ID
	btn, err := o.SaveBoardAuto(ctx, T, checker, b.ID, BoardAuto{Kind: AutoCardButton, Name: "Send to checker", Actions: []AutoAction{
		{Kind: "move", List: doing, Top: true}, {Kind: "add_member", Member: checker}, {Kind: "join"},
		{Kind: "add_checklist", Name: "Checks", Items: []string{"Amount matches the bank", "", "Client's own account"}},
		{Kind: "comment", Text: "Ready for checking, sent by {user}"}}})
	if err != nil {
		t.Fatal(err)
	}
	first, _ := o.AddCard(ctx, T, maker, b.ID, CardInput{List: doing, Title: "Already there"})
	c, _ := o.AddCard(ctx, T, maker, b.ID, CardInput{List: todo, Title: "Payout to RK Traders"})
	if _, err := o.RunBoardAuto(ctx, T, risk, b.ID, btn.ID, c.ID, false); !errors.Is(err, ErrForbidden) {
		t.Fatalf("an observer pressed a card button: %v", err)
	}
	res, err := o.RunBoardAuto(ctx, T, maker, b.ID, btn.ID, c.ID, false)
	if err != nil || res.Run == nil || res.Run.Status != "done" || res.Run.By != maker {
		t.Fatalf("pressed: %+v %v", res.Run, err)
	}
	c = baCard(t, o, ctx, b, c.ID)
	first = baCard(t, o, ctx, b, first.ID)
	if c.List != doing || c.Pos >= first.Pos || strings.Join(c.Members, ",") != "farhan.khan,priya.nair" {
		t.Fatalf("card %+v", c)
	}
	if len(c.Checklists) != 1 || c.Checklists[0].Name != "Checks" || len(c.Checklists[0].Items) != 2 {
		t.Fatalf("checklist %+v", c.Checklists)
	}
	if len(c.Comments) != 1 || c.Comments[0].Text != "Ready for checking, sent by priya.nair" {
		t.Fatalf("comments %+v", c.Comments)
	}
	// A rule is not pressed; a switched-off button is not either.
	rule, _ := o.SaveBoardAuto(ctx, T, maker, b.ID, BoardAuto{Kind: AutoRule, Name: "r", Trigger: &AutoTrigger{Event: "card_done"}, Actions: []AutoAction{{Kind: "archive"}}})
	if _, err := o.RunBoardAuto(ctx, T, maker, b.ID, rule.ID, c.ID, false); !errors.Is(err, ErrInvalid) {
		t.Fatalf("pressed a rule: %v", err)
	}
	o.SetBoardAutoOn(ctx, T, maker, b.ID, btn.ID, false)
	if _, err := o.RunBoardAuto(ctx, T, maker, b.ID, btn.ID, c.ID, false); !errors.Is(err, ErrInvalid) {
		t.Fatalf("pressed a switched-off button: %v", err)
	}
}

func TestBoardBoardButton(t *testing.T) {
	o, ctx, b := baSetup(t, "2026-11-16T10:00")
	maker, checker, risk := "priya.nair", "farhan.khan", "anita.rao"
	todo, done := b.Lists[0].ID, b.Lists[2].ID
	btn, err := o.SaveBoardAuto(ctx, T, maker, b.ID, BoardAuto{Kind: AutoBoardButton, Name: "Clear To do", List: todo,
		Conditions: []AutoCond{{Kind: "not_complete"}},
		Actions:    []AutoAction{{Kind: "move", List: done}, {Kind: "sort_list", List: done, By: "title"}, {Kind: "notify", To: []string{"card_members", risk}}}})
	if err != nil {
		t.Fatal(err)
	}
	titles := "QWERTYUIOPASDFGHJKLZXCVBN" // 25 cards
	var finished Card
	for i, ch := range titles {
		c, err := o.AddCard(ctx, T, checker, b.ID, CardInput{List: todo, Title: "Card " + string(ch), Members: []string{maker}})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			yes := true
			finished, _ = o.UpdateCard(ctx, T, checker, b.ID, c.ID, CardPatch{DueDone: &yes})
		}
	}
	if _, err := o.RunBoardAuto(ctx, T, risk, b.ID, btn.ID, "", true); !errors.Is(err, ErrForbidden) {
		t.Fatalf("an observer pressed a board button: %v", err)
	}
	res, err := o.RunBoardAuto(ctx, T, checker, b.ID, btn.ID, "", false)
	if err != nil || !res.NeedsConfirm || res.Cards != 24 || res.Run != nil {
		t.Fatalf("over 20 cards without confirming: %+v %v", res, err)
	}
	if v, _ := o.GetBoard(ctx, T, maker, b.ID, false); func() int {
		n := 0
		for _, c := range v.Cards {
			if c.List == done {
				n++
			}
		}
		return n
	}() != 0 {
		t.Fatal("ran before it was confirmed")
	}
	res, err = o.RunBoardAuto(ctx, T, checker, b.ID, btn.ID, "", true)
	if err != nil || res.Run.Status != "done" || res.Cards != 24 || res.Run.Cards != 24 {
		t.Fatalf("confirmed: %+v %v", res.Run, err)
	}
	v, _ := o.GetBoard(ctx, T, maker, b.ID, false)
	var inDone []Card
	for _, c := range v.Cards {
		if c.List == done {
			inDone = append(inDone, c)
		} else if c.ID != finished.ID {
			t.Fatalf("%s stayed in %s", c.Title, c.List)
		}
	}
	if len(inDone) != 24 {
		t.Fatalf("%d cards in Done", len(inDone))
	}
	for i := 1; i < len(inDone); i++ { // boardCards orders by list, then position
		if inDone[i-1].Title > inDone[i].Title {
			t.Fatalf("Done not sorted by title: %s before %s", inDone[i-1].Title, inDone[i].Title)
		}
	}
	// One notice each for the card members (the maker) and the risk manager.
	for _, u := range []string{maker, risk} {
		ns, _, _ := o.BoardNotices(ctx, T, u, 200)
		n := 0
		for _, x := range ns {
			if x.By == AutomationActor && strings.Contains(x.Text, "Clear To do ran") {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("%s got %d notices from the button", u, n)
		}
	}
	// Few cards: no question asked.
	if res, err := o.RunBoardAuto(ctx, T, checker, b.ID, btn.ID, "", false); err != nil || res.NeedsConfirm || res.Cards != 0 || res.Run.Did[0] != "no cards matched" {
		t.Fatalf("second press %+v %v", res, err)
	}
}

func TestBoardScheduledCommand(t *testing.T) {
	o, ctx, b := baSetup(t, "2026-11-16T08:00") // a Monday
	maker, checker := "priya.nair", "farhan.khan"
	todo := b.Lists[0].ID
	set, _ := o.Settings(ctx, T)
	for _, d := range []string{"2026-11-16", "2026-11-17", "2026-11-18"} {
		if !set.IsBusinessDay(d) {
			t.Fatalf("%s is not a working day in the sample settings", d)
		}
	}
	cmd, err := o.SaveBoardAuto(ctx, T, maker, b.ID, BoardAuto{Kind: AutoScheduled, Name: "Daily recon card",
		When: &AutoWhen{Every: "working_day", At: "09:00"},
		Actions: []AutoAction{{Kind: "create_card", List: todo, Text: "Daily recon {date}", Labels: []string{b.Labels[4].ID}, Members: []string{checker},
			Due: true, Days: 1, Time: "18:00", Name: "Steps", Items: []string{"Load the trade files", "Run the reconciliation"}}}})
	if err != nil {
		t.Fatal(err)
	}
	cards := func() []Card {
		v, _ := o.GetBoard(ctx, T, maker, b.ID, false)
		return v.Cards
	}
	baTick(ctx, o, T, baAt("2026-11-16T08:59"))
	if n := len(cards()); n != 0 {
		t.Fatalf("ran early: %d cards", n)
	}
	baTick(ctx, o, T, baAt("2026-11-16T09:00"))
	cs := cards()
	if len(cs) != 1 {
		t.Fatalf("at 09:00: %d cards", len(cs))
	}
	c := cs[0]
	if c.Title != "Daily recon 16 Nov 2026" || c.List != todo || c.Due != "2026-11-17T18:00" || strings.Join(c.Members, ",") != checker || len(c.Labels) != 1 ||
		len(c.Checklists) != 1 || len(c.Checklists[0].Items) != 2 || c.CreatedBy != AutomationActor {
		t.Fatalf("card %+v", c)
	}
	baTick(ctx, o, T, baAt("2026-11-16T09:01"))
	baTick(ctx, o, T, baAt("2026-11-16T11:30"))
	// A restart (a new Ops on the same store) remembers the run.
	o2 := New(o.App)
	o2.Now = o.Now
	baTick(ctx, o2, T, baAt("2026-11-16T09:02"))
	if n := len(cards()); n != 1 {
		t.Fatalf("ran again: %d cards", n)
	}
	// A trading holiday on a working-day command: skipped, and the log says why.
	set.Holidays = append(set.Holidays, "2026-11-17")
	if err := docs.Upsert(ctx, o.Docs, T, cSettings, "main", set, "admin"); err != nil {
		t.Fatal(err)
	}
	baTick(ctx, o, T, baAt("2026-11-17T09:00"))
	if n := len(cards()); n != 1 {
		t.Fatalf("ran on a holiday: %d cards", n)
	}
	runs, _ := o.BoardAutoLog(ctx, T, checker, b.ID, cmd.ID, 0, 0)
	if len(runs) != 2 || runs[0].Status != "skipped" || !strings.Contains(runs[0].Error, "trading holiday") || runs[1].Status != "done" {
		t.Fatalf("runs %+v", runs)
	}
	baTick(ctx, o, T, baAt("2026-11-18T09:00"))
	baTick(ctx, o, T, baAt("2026-11-21T09:00")) // Saturday
	if n := len(cards()); n != 2 {
		t.Fatalf("after Wednesday and Saturday: %d cards", n)
	}
	if runs, _ := o.BoardAutoLog(ctx, T, checker, b.ID, cmd.ID, 0, 0); len(runs) != 3 {
		t.Fatalf("a weekend day was logged: %+v", runs)
	}
	// Missed while Ledgerline was down: runs late, within three hours.
	baTick(ctx, o, T, baAt("2026-11-19T10:15"))
	if runs, _ := o.BoardAutoLog(ctx, T, checker, b.ID, cmd.ID, 0, 1); len(runs) != 1 || !strings.Contains(runs[0].Why, "late") {
		t.Fatalf("late run %+v", runs)
	}
	baTick(ctx, o, T, baAt("2026-11-20T12:30"))
	if n := len(cards()); n != 3 {
		t.Fatalf("ran more than three hours late: %d cards", n)
	}

	// Which days each kind of schedule runs on.
	set, _ = o.Settings(ctx, T)
	for _, x := range []struct {
		w    AutoWhen
		date string
		want bool
	}{
		{AutoWhen{Every: "weekday"}, "2026-11-17", true}, // a weekday, even on a holiday
		{AutoWhen{Every: "weekday", SkipHolidays: true}, "2026-11-17", false},
		{AutoWhen{Every: "weekdays", Days: []int{2, 4}}, "2026-11-19", true},
		{AutoWhen{Every: "weekdays", Days: []int{2, 4}}, "2026-11-18", false},
		{AutoWhen{Every: "month_day", Day: 31}, "2026-11-30", true},
		{AutoWhen{Every: "month_day", Day: 16}, "2026-11-16", true},
		{AutoWhen{Every: "first_working"}, "2026-11-02", true}, // the 1st is a Sunday
		{AutoWhen{Every: "first_working"}, "2026-11-03", false},
		{AutoWhen{Every: "last_working"}, "2026-11-30", true},
		{AutoWhen{Every: "day"}, "2026-11-22", true},
	} {
		if got, _ := x.w.runsOn(x.date, set); got != x.want {
			t.Errorf("%+v on %s: %v", x.w, x.date, got)
		}
	}
	// Run now, from the console.
	res, err := o.RunBoardAuto(ctx, T, checker, b.ID, cmd.ID, "", false)
	if err != nil || res.Run.Status != "done" || len(cards()) != 4 {
		t.Fatalf("run now %+v %v", res.Run, err)
	}
	// A scheduled command acts on the board, not on a card.
	if _, err := o.SaveBoardAuto(ctx, T, maker, b.ID, BoardAuto{Kind: AutoScheduled, Name: "x", When: &AutoWhen{Every: "day", At: "09:00"}, Actions: []AutoAction{{Kind: "archive"}}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a scheduled command with a card action: %v", err)
	}
	if _, err := o.SaveBoardAuto(ctx, T, maker, b.ID, BoardAuto{Kind: AutoScheduled, Name: "x", When: &AutoWhen{Every: "day", At: "9am"}, Actions: []AutoAction{{Kind: "sort_list", List: todo}}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a time not HH:MM: %v", err)
	}
}

func TestBoardDueDateCommand(t *testing.T) {
	o, ctx, b := baSetup(t, "2026-11-16T10:00")
	maker, checker, risk := "priya.nair", "farhan.khan", "anita.rao"
	todo, doing := b.Lists[0].ID, b.Lists[1].ID
	lab := b.Labels[3].ID
	cmd, err := o.SaveBoardAuto(ctx, T, maker, b.ID, BoardAuto{Kind: AutoDueDate, Name: "An hour to go",
		Due:     &AutoDueAt{N: 1, Unit: "hours", When: "before", List: doing},
		Actions: []AutoAction{{Kind: "add_label", Label: lab}, {Kind: "notify", To: []string{"card_members"}, Text: "{card} is due at {due}"}}})
	if err != nil {
		t.Fatal(err)
	}
	add := func(list, title, due string, done bool) Card {
		t.Helper()
		c, err := o.AddCard(ctx, T, checker, b.ID, CardInput{List: list, Title: title, Due: due, Members: []string{risk}})
		if err != nil {
			t.Fatal(err)
		}
		if done {
			yes := true
			c, _ = o.UpdateCard(ctx, T, checker, b.ID, c.ID, CardPatch{DueDone: &yes})
		}
		return c
	}
	c1 := add(doing, "File the margin report", "2026-11-16T15:00", false)
	add(todo, "Another list", "2026-11-16T15:00", false)
	add(doing, "Already complete", "2026-11-16T15:00", true)
	add(doing, "Its moment passed before the command existed", "2026-11-16T10:30", false)
	count := func() int {
		rs, _ := o.BoardAutoLog(ctx, T, maker, b.ID, cmd.ID, 0, 0)
		return len(rs)
	}
	baTick(ctx, o, T, baAt("2026-11-16T10:05"))
	baTick(ctx, o, T, baAt("2026-11-16T13:59"))
	if n := count(); n != 0 {
		t.Fatalf("ran early: %d", n)
	}
	baTick(ctx, o, T, baAt("2026-11-16T14:00"))
	baTick(ctx, o, T, baAt("2026-11-16T14:01"))
	baTick(ctx, o, T, baAt("2026-11-16T16:00"))
	runs, _ := o.BoardAutoLog(ctx, T, maker, b.ID, cmd.ID, 0, 0)
	if len(runs) != 1 || runs[0].Card != c1.ID || runs[0].Status != "done" || !strings.Contains(runs[0].Why, "1 hour before its due date (16 Nov 2026 15:00)") {
		t.Fatalf("runs %+v", runs)
	}
	if c := baCard(t, o, ctx, b, c1.ID); !contains(c.Labels, lab) {
		t.Fatalf("not labelled %+v", c.Labels)
	}
	ns, _, _ := o.BoardNotices(ctx, T, risk, 0)
	if len(ns) == 0 || ns[0].Text != "File the margin report is due at 16 Nov 2026 15:00" {
		t.Fatalf("notices %+v", ns)
	}
	// A new due date: it runs once more, for that date.
	nd := "2026-11-17T15:00"
	if _, err := o.UpdateCard(ctx, T, checker, b.ID, c1.ID, CardPatch{Due: &nd}); err != nil {
		t.Fatal(err)
	}
	baTick(ctx, o, T, baAt("2026-11-17T14:00"))
	baTick(ctx, o, T, baAt("2026-11-17T14:30"))
	if n := count(); n != 2 {
		t.Fatalf("after the new due date: %d runs", n)
	}
	if bad := (AutoDueAt{N: 100, Unit: "days", When: "before"}); true {
		if _, err := o.SaveBoardAuto(ctx, T, maker, b.ID, BoardAuto{Kind: AutoDueDate, Name: "x", Due: &bad, Actions: []AutoAction{{Kind: "archive"}}}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("100 days before: %v", err)
		}
	}
}

func TestBoardAutomationPermissionsAndLog(t *testing.T) {
	o, ctx, b := baSetup(t, "2026-11-16T10:00")
	maker, checker, risk := "priya.nair", "farhan.khan", "anita.rao"
	todo := b.Lists[0].ID
	rule := BoardAuto{Kind: AutoRule, Name: "Label new cards", Trigger: &AutoTrigger{Event: "card_added", List: todo}, Actions: []AutoAction{{Kind: "add_label", Label: b.Labels[0].ID}}}
	if _, err := o.SaveBoardAuto(ctx, T, risk, b.ID, rule); !errors.Is(err, ErrForbidden) {
		t.Fatalf("an observer made a rule: %v", err)
	}
	if v, err := o.BoardAutomation(ctx, T, risk, b.ID); err != nil || v.CanEdit || v.Role != BoardObserver {
		t.Fatalf("observer's view %+v %v", v, err)
	}
	if _, err := o.BoardAutomation(ctx, T, "vikram.shah", b.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("someone off a private board read its automation: %v", err)
	}
	a, err := o.SaveBoardAuto(ctx, T, checker, b.ID, rule)
	if err != nil {
		t.Fatalf("a member made a rule: %v", err)
	}
	if v, _ := o.BoardAutomation(ctx, T, checker, b.ID); !v.CanEdit || len(v.Autos) != 1 {
		t.Fatalf("member's view %+v", v)
	}
	// Changing it: the kind stays; bad references are refused.
	a.Name = "Label cards added to To do"
	if a, err = o.SaveBoardAuto(ctx, T, maker, b.ID, a); err != nil || a.Name != "Label cards added to To do" || a.UpdatedBy != maker {
		t.Fatalf("changed %+v %v", a, err)
	}
	kind := a
	kind.Kind = AutoCardButton
	if _, err := o.SaveBoardAuto(ctx, T, maker, b.ID, kind); !errors.Is(err, ErrInvalid) {
		t.Fatalf("changed the kind: %v", err)
	}
	for _, x := range []BoardAuto{
		{Kind: AutoRule, Name: "no trigger", Actions: []AutoAction{{Kind: "archive"}}},
		{Kind: AutoRule, Name: "no actions", Trigger: &AutoTrigger{Event: "card_done"}},
		{Kind: AutoRule, Name: "bad list", Trigger: &AutoTrigger{Event: "card_done"}, Actions: []AutoAction{{Kind: "move", List: "L-nope"}}},
		{Kind: AutoRule, Name: "bad label", Trigger: &AutoTrigger{Event: "card_done"}, Actions: []AutoAction{{Kind: "add_label", Label: "LB-nope"}}},
		{Kind: AutoRule, Name: "off the board", Trigger: &AutoTrigger{Event: "card_done"}, Actions: []AutoAction{{Kind: "add_member", Member: "vikram.shah"}}},
		{Kind: AutoRule, Name: "bad condition", Trigger: &AutoTrigger{Event: "card_done"}, Conditions: []AutoCond{{Kind: "sunny"}}, Actions: []AutoAction{{Kind: "archive"}}},
		{Kind: "macro", Name: "bad kind", Actions: []AutoAction{{Kind: "archive"}}},
	} {
		if _, err := o.SaveBoardAuto(ctx, T, maker, b.ID, x); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", x.Name, err)
		}
	}
	// Runs go to the log, newest first, with paging.
	for i := 0; i < 3; i++ {
		if _, err := o.AddCard(ctx, T, checker, b.ID, CardInput{List: todo, Title: "Card"}); err != nil {
			t.Fatal(err)
		}
	}
	runs, _ := o.BoardAutoLog(ctx, T, risk, b.ID, a.ID, 0, 0)
	if len(runs) != 3 || runs[0].Seq <= runs[2].Seq {
		t.Fatalf("log %+v", runs)
	}
	if older, _ := o.BoardAutoLog(ctx, T, risk, b.ID, "", runs[0].Seq, 1); len(older) != 1 || older[0].Seq != runs[1].Seq {
		t.Fatalf("paging %+v", older)
	}
	// The daily cap: runs stop, the log and the board's admins are told once.
	old := baDailyCap
	baDailyCap = 5
	defer func() { baDailyCap = old }()
	for i := 0; i < 4; i++ {
		o.AddCard(ctx, T, checker, b.ID, CardInput{List: todo, Title: "More"})
	}
	runs, _ = o.BoardAutoLog(ctx, T, risk, b.ID, "", 0, 0)
	if runs[0].Status != "limit" || runs[1].Status != "done" || len(runs) != 6 {
		t.Fatalf("at the cap %+v", runs)
	}
	ns, _, _ := o.BoardNotices(ctx, T, maker, 0)
	if len(ns) == 0 || !strings.Contains(ns[0].Text, "stopped for today") {
		t.Fatalf("admin notices %+v", ns)
	}
	if v, _ := o.BoardAutomation(ctx, T, risk, b.ID); v.RunsToday != 5 {
		t.Fatalf("runs today %d", v.RunsToday)
	}
	// Deleting: members may, observers may not.
	if err := o.DeleteBoardAuto(ctx, T, risk, b.ID, a.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("an observer deleted a rule: %v", err)
	}
	if err := o.DeleteBoardAuto(ctx, T, checker, b.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	if v, _ := o.BoardAutomation(ctx, T, checker, b.ID); len(v.Autos) != 0 {
		t.Fatalf("deleted rule still listed %+v", v.Autos)
	}
}
