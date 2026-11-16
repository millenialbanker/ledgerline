package ops

import (
	"errors"
	"fmt"
	"strings"
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

// The dashboard's tiles are checked before they are kept on the board.
func TestBoardDashTiles(t *testing.T) {
	o, ctx := setup(t)
	me, obs := "priya.nair", "anita.rao"
	b, _ := o.CreateBoard(ctx, T, me, BoardInput{Name: "Close", Visibility: "private"})
	if _, err := o.UpdateBoard(ctx, T, me, b.ID, BoardInput{Members: []BoardPerson{{me, BoardAdmin}, {obs, BoardObserver}}}); err != nil {
		t.Fatal(err)
	}
	if ts := DashTilesOf(b); len(ts) != 5 || ts[0].Data != "list" || ts[0].Chart != "bar" {
		t.Fatalf("default tiles %+v", ts)
	}
	ts, err := o.SetDashTiles(ctx, T, me, b.ID, []DashTile{{ID: "a", Data: "member", Chart: "pie"}, {Data: "time"}, {ID: "a", Data: "due"}, {ID: "<b>", Data: "checks", Chart: "pie"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(ts) != 4 || ts[0] != (DashTile{"a", "member", "pie"}) || ts[1].Chart != "line" || ts[1].ID == "" || ts[2].ID == "a" || ts[2].Chart != "bar" || ts[3].ID == "<b>" {
		t.Fatalf("saved tiles %+v", ts)
	}
	got, _ := o.GetBoard(ctx, T, obs, b.ID, false)
	if back := DashTilesOf(got.Board); len(back) != 4 || back[1].Data != "time" {
		t.Fatalf("tiles read back %+v (%q)", back, got.Board.Settings[dashTilesKey])
	}
	for _, bad := range [][]DashTile{
		{{Data: "client"}},
		{{Data: "time", Chart: "pie"}},
		{{Data: "list", Chart: "line"}},
		{{Data: "list", Chart: "3d"}},
		make([]DashTile, 13),
	} {
		if _, err := o.SetDashTiles(ctx, T, me, b.ID, bad, false); err == nil {
			t.Fatalf("tiles %+v accepted", bad)
		}
	}
	// Observers look; they do not change the dashboard.
	if _, err := o.SetDashTiles(ctx, T, obs, b.ID, []DashTile{{Data: "list"}}, false); !errors.Is(err, ErrForbidden) {
		t.Fatalf("observer changed tiles: %v", err)
	}
	// No tiles is allowed; reset goes back to the default set.
	if ts, err = o.SetDashTiles(ctx, T, me, b.ID, nil, false); err != nil || len(ts) != 0 {
		t.Fatalf("no tiles %+v %v", ts, err)
	}
	if ts, err = o.SetDashTiles(ctx, T, me, b.ID, nil, true); err != nil || len(ts) != 5 {
		t.Fatalf("reset %+v %v", ts, err)
	}
	// What no longer reads falls back to the default.
	if ts := DashTilesOf(Board{Settings: map[string]string{dashTilesKey: `[{"data":"nope"}]`}}); len(ts) != 5 {
		t.Fatalf("bad stored tiles %+v", ts)
	}
	if ts := DashTilesOf(Board{Settings: map[string]string{dashTilesKey: `{`}}); len(ts) != 5 {
		t.Fatalf("broken stored tiles %+v", ts)
	}
}

// Cards over time: added and completed each week, for twelve weeks, from
// the board's activity, reading no more than it may.
func TestBoardOverTime(t *testing.T) {
	o, ctx := setup(t)
	me := "priya.nair"
	at := time.Date(2026, 8, 3, 10, 0, 0, 0, ist) // a Monday, fifteen weeks before 16 November
	o.Now = func() time.Time { return at }
	b, _ := o.CreateBoard(ctx, T, me, BoardInput{Name: "Close", Visibility: "private"})
	yes := true
	add := func(n int, done int) {
		for i := 0; i < n; i++ {
			c, err := o.AddCard(ctx, T, me, b.ID, CardInput{List: b.Lists[0].ID, Title: fmt.Sprintf("Card %d", i)})
			if err != nil {
				t.Fatal(err)
			}
			if i < done {
				if _, err := o.UpdateCard(ctx, T, me, b.ID, c.ID, CardPatch{DueDone: &yes}); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	// Before the twelve weeks: not counted.
	add(4, 1)
	at = time.Date(2026, 9, 1, 12, 0, 0, 0, ist) // the week of 31 August, the first shown
	add(3, 2)
	at = time.Date(2026, 11, 18, 9, 0, 0, 0, ist) // this week
	add(2, 0)
	at = time.Date(2026, 11, 22, 23, 0, 0, 0, ist) // Sunday night, still this week
	add(1, 1)

	v, err := o.BoardOverTime(ctx, T, me, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Weeks) != 12 || v.Weeks[0].Week != "2026-08-31" || v.Weeks[11].Week != "2026-11-16" || v.Capped {
		t.Fatalf("weeks %+v", v)
	}
	if w := v.Weeks[0]; w.Added != 3 || w.Done != 2 {
		t.Fatalf("first week %+v", w)
	}
	if w := v.Weeks[11]; w.Added != 3 || w.Done != 1 {
		t.Fatalf("this week %+v", w)
	}
	for _, w := range v.Weeks[1:11] {
		if w.Added != 0 || w.Done != 0 {
			t.Fatalf("empty week %+v", w)
		}
	}
	// Marking the same card complete twice in a week counts it once.
	no := false
	cs, _ := o.boardCards(ctx, T, b.ID)
	last := cs[len(cs)-1]
	o.UpdateCard(ctx, T, me, b.ID, last.ID, CardPatch{DueDone: &no})
	o.UpdateCard(ctx, T, me, b.ID, last.ID, CardPatch{DueDone: &yes})
	if v, _ = o.BoardOverTime(ctx, T, me, b.ID); v.Weeks[11].Done > 2 {
		t.Fatalf("this week counted twice %+v", v.Weeks[11])
	}
	// The work is capped: past the cap it stops and says so.
	old := overTimeBuckets
	overTimeBuckets = 0
	defer func() { overTimeBuckets = old }()
	if v, _ = o.BoardOverTime(ctx, T, me, b.ID); !v.Capped || v.Read != 0 {
		t.Fatalf("capped %+v", v)
	}
	overTimeBuckets = old
	// Only people who can see the board.
	if _, err := o.BoardOverTime(ctx, T, "farhan.khan", b.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("farhan read a private board's history: %v", err)
	}
}

// All boards: the cards of every board a person can see, filtered and
// paged, with the same access as the boards themselves.
func TestBoardCrossCards(t *testing.T) {
	o, ctx := setup(t)
	o.Now = func() time.Time { return time.Date(2026, 11, 18, 11, 0, 0, 0, ist) }
	me, other, branch := "priya.nair", "farhan.khan", "meera.iyer"
	close1, _ := o.CreateBoard(ctx, T, me, BoardInput{Name: "Month-end close"})                           // firm-wide
	priv, _ := o.CreateBoard(ctx, T, other, BoardInput{Name: "Farhan's own", Visibility: "private"})      // only Farhan
	mine, _ := o.CreateBoard(ctx, T, branch, BoardInput{Name: "Bengaluru branch", Visibility: "private"}) // only Meera
	red := close1.Labels[3].ID
	add := func(b Board, by, list, title, due string, members []string, labels []string) Card {
		t.Helper()
		c, err := o.AddCard(ctx, T, by, b.ID, CardInput{List: list, Title: title, Due: due, Members: members, Labels: labels})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	add(close1, me, close1.Lists[0].ID, "Post interest", "2026-11-17", []string{me}, []string{red})
	add(close1, me, close1.Lists[1].ID, "DP bills", "2026-11-18T20:00", nil, nil)
	add(close1, me, close1.Lists[0].ID, "Undated", "", []string{other}, nil)
	add(priv, other, priv.Lists[0].ID, "Farhan's secret", "2026-11-19", []string{other}, nil)
	add(mine, branch, mine.Lists[0].ID, "Branch visit", "2026-11-25", []string{branch}, nil)
	arch := add(close1, me, close1.Lists[0].ID, "Archived", "2026-11-18", nil, nil)
	yes := true
	o.UpdateCard(ctx, T, me, close1.ID, arch.ID, CardPatch{Archived: &yes})

	v, err := o.CrossCards(ctx, T, me, XCardsQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if v.Total != 3 || len(v.Cards) != 3 || v.More || v.Capped || len(v.Boards) != 1 {
		t.Fatalf("priya's cards %+v", v)
	}
	first := v.Cards[0]
	if first.Title != "Post interest" || first.Status != "overdue" || first.BoardName != "Month-end close" || first.ListName != "To do" || first.Role != BoardAdmin || len(first.Labels) != 1 {
		t.Fatalf("first card %+v", first)
	}
	if v.Cards[1].Status != "soon" || v.Cards[2].Status != "none" {
		t.Fatalf("order and status %+v", v.Cards)
	}
	if len(v.Lists) != 2 || len(v.Labels) != 1 || v.Labels[0].Name != "Red" || len(v.Members) != 2 {
		t.Fatalf("filters offered %+v %+v %+v", v.Lists, v.Labels, v.Members)
	}
	// Farhan sees the firm-wide board (as an observer) and his own; not Meera's.
	fv, _ := o.CrossCards(ctx, T, other, XCardsQuery{})
	if fv.Total != 4 || len(fv.Boards) != 2 {
		t.Fatalf("farhan's cards %+v", fv)
	}
	for _, c := range fv.Cards {
		if c.Board == close1.ID && c.Role != BoardObserver {
			t.Fatalf("farhan's role %+v", c)
		}
	}
	// Branch staff see only their own boards.
	bv, _ := o.CrossCards(ctx, T, branch, XCardsQuery{})
	if bv.Total != 1 || bv.Cards[0].Title != "Branch visit" || len(bv.Boards) != 1 {
		t.Fatalf("branch staff %+v", bv)
	}
	// The admin sees every board but no one's Inbox.
	o.AddInboxCard(ctx, T, "admin", CardInput{Title: "Admin's own note", Due: "2026-11-18"})
	if av, _ := o.CrossCards(ctx, T, "admin", XCardsQuery{}); av.Total != 5 {
		t.Fatalf("admin %+v", av)
	}

	// Filters.
	check := func(q XCardsQuery, want ...string) {
		t.Helper()
		v, err := o.CrossCards(ctx, T, other, q)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, c := range v.Cards {
			got = append(got, c.Title)
		}
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Fatalf("%+v: %v, want %v", q, got, want)
		}
	}
	check(XCardsQuery{Boards: []string{priv.ID}}, "Farhan's secret")
	check(XCardsQuery{Boards: []string{mine.ID}}) // not his to see
	check(XCardsQuery{Lists: []string{" to DO "}}, "Post interest", "Farhan's secret", "Undated")
	check(XCardsQuery{Members: []string{other}}, "Farhan's secret", "Undated")
	check(XCardsQuery{Members: []string{"_none"}}, "DP bills")
	check(XCardsQuery{Labels: []string{"red"}}, "Post interest")
	check(XCardsQuery{Labels: []string{"_none"}, Due: []string{"soon", "later"}}, "DP bills", "Farhan's secret")
	check(XCardsQuery{From: "2026-11-18", To: "2026-11-30"}, "DP bills", "Farhan's secret")
	// Labels in the 30 shades are named by shade when they have no name.
	nb, err := o.UpdateBoard(ctx, T, other, priv.ID, BoardInput{Labels: []BoardLabel{{Color: "green_bold"}, {Color: "sky_subtle", Name: "Waiting"}}})
	if err != nil {
		t.Fatal(err)
	}
	cs, _ := o.boardCards(ctx, T, priv.ID)
	if _, err := o.UpdateCard(ctx, T, other, priv.ID, cs[0].ID, CardPatch{Labels: &[]string{nb.Labels[0].ID, nb.Labels[1].ID}}); err != nil {
		t.Fatal(err)
	}
	if lv, _ := o.CrossCards(ctx, T, other, XCardsQuery{Labels: []string{"bold green"}}); lv.Total != 1 || lv.Labels[0].Name != "Bold green" || lv.Labels[0].Color != "green_bold" {
		t.Fatalf("shaded labels %+v %+v", lv.Labels, lv.Cards)
	}
	if _, err := o.CrossCards(ctx, T, other, XCardsQuery{From: "18/11/2026"}); err == nil {
		t.Fatal("a bad date accepted")
	}

	// Paging, and the cap on the cards read.
	p1, _ := o.CrossCards(ctx, T, other, XCardsQuery{Limit: 3})
	p2, _ := o.CrossCards(ctx, T, other, XCardsQuery{Limit: 3, Offset: 3})
	if len(p1.Cards) != 3 || !p1.More || len(p2.Cards) != 1 || p2.More || p2.Cards[0].Title != "Undated" || p1.Total != 4 {
		t.Fatalf("pages %+v / %+v", p1, p2)
	}
	if big, _ := o.CrossCards(ctx, T, other, XCardsQuery{Limit: 5000}); big.Limit != 500 {
		t.Fatalf("limit %d", big.Limit)
	}
	old := xCardsScan
	xCardsScan = 2
	defer func() { xCardsScan = old }()
	if cv, _ := o.CrossCards(ctx, T, other, XCardsQuery{}); !cv.Capped || cv.Total != 2 || len(cv.Boards) != 2 {
		t.Fatalf("capped %+v", cv)
	}
}
