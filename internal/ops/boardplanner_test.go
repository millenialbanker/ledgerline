package ops

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"ledgerline/internal/docs"
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
	if _, _, err := o.CreateBoardFeed(ctx, T, me, "board", in.Board); err == nil {
		t.Fatal("a feed of the inbox alone")
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

// icsDoc is an iCalendar as a small parser reads it.
type icsDoc struct {
	props  map[string]string
	events []map[string]string
}

// parseICS checks the RFC 5545 rules a calendar app relies on: CRLF line
// endings, lines folded at 75 octets without splitting a character,
// BEGIN and END in step, and each event's UID, DTSTAMP and DTSTART.
func parseICS(t *testing.T, body string) icsDoc {
	t.Helper()
	if !strings.HasSuffix(body, "\r\n") {
		t.Fatal("the calendar does not end with CRLF")
	}
	phys := strings.Split(strings.TrimSuffix(body, "\r\n"), "\r\n")
	var lines []string
	for i, l := range phys {
		if strings.ContainsAny(l, "\r\n") {
			t.Fatalf("line %d has a bare CR or LF: %q", i, l)
		}
		if len(l) > 75 {
			t.Fatalf("line %d is %d octets: %q", i, len(l), l)
		}
		if !utf8.ValidString(l) {
			t.Fatalf("line %d splits a character: %q", i, l)
		}
		if strings.HasPrefix(l, " ") || strings.HasPrefix(l, "\t") {
			if len(lines) == 0 {
				t.Fatal("the calendar starts with a folded line")
			}
			lines[len(lines)-1] += l[1:]
			continue
		}
		lines = append(lines, l)
	}
	unesc := strings.NewReplacer(`\\`, `\`, `\;`, ";", `\,`, ",", `\n`, "\n", `\N`, "\n")
	d := icsDoc{props: map[string]string{}}
	var stack []string
	var ev map[string]string
	for _, l := range lines {
		name, value, ok := strings.Cut(l, ":")
		if !ok {
			t.Fatalf("no value in %q", l)
		}
		key := strings.ToUpper(name)
		switch key {
		case "BEGIN":
			stack = append(stack, value)
			if value == "VEVENT" {
				ev = map[string]string{}
			}
			continue
		case "END":
			if len(stack) == 0 || stack[len(stack)-1] != value {
				t.Fatalf("END:%s out of step with %v", value, stack)
			}
			stack = stack[:len(stack)-1]
			if value == "VEVENT" {
				for _, k := range []string{"UID", "DTSTAMP", "DTSTART", "SUMMARY"} {
					if !hasProp(ev, k) {
						t.Fatalf("event without %s: %v", k, ev)
					}
				}
				d.events = append(d.events, ev)
				ev = nil
			}
			continue
		}
		if strings.ContainsAny(value, "\r\n") {
			t.Fatalf("raw newline in %q", l)
		}
		if ev != nil {
			ev[key] = unesc.Replace(value)
		} else {
			d.props[key] = unesc.Replace(value)
		}
	}
	if len(stack) != 0 || len(lines) == 0 || lines[0] != "BEGIN:VCALENDAR" {
		t.Fatalf("calendar not closed: %v", stack)
	}
	if d.props["VERSION"] != "2.0" || d.props["PRODID"] == "" {
		t.Fatalf("calendar properties %v", d.props)
	}
	uids := map[string]bool{}
	for _, e := range d.events {
		if uids[e["UID"]] {
			t.Fatalf("UID %s twice", e["UID"])
		}
		uids[e["UID"]] = true
	}
	return d
}

func hasProp(ev map[string]string, k string) bool {
	for p := range ev {
		if p == k || strings.HasPrefix(p, k+";") {
			return true
		}
	}
	return false
}

func TestBoardCalendarFeeds(t *testing.T) {
	o, ctx := setup(t)
	o.Now = func() time.Time { return time.Date(2026, 11, 18, 11, 0, 0, 0, ist) }
	me, other, admin := "priya.nair", "farhan.khan", "admin"
	b, _ := o.CreateBoard(ctx, T, me, BoardInput{Name: "Close; Q3, review", Visibility: "private"})
	long := "Reconcile the escrow account with the bank statement — every line, including the GST on DP charges, and the CUSPA ledger ✓ before the evening cut-off"
	timed, _ := o.AddCard(ctx, T, me, b.ID, CardInput{List: b.Lists[0].ID, Title: long, Members: []string{me}, Due: "2026-11-19T17:30"})
	allDay, _ := o.AddCard(ctx, T, me, b.ID, CardInput{List: b.Lists[0].ID, Title: `Back\slash; semi, comma`, Members: []string{me}, Due: "2026-11-20"})
	o.AddCard(ctx, T, me, b.ID, CardInput{List: b.Lists[0].ID, Title: "No due date", Members: []string{me}})
	o.AddCard(ctx, T, me, b.ID, CardInput{List: b.Lists[0].ID, Title: "Not mine", Due: "2026-11-21"})
	desc := "Client Z-9999 called about a dispute"
	if _, err := o.UpdateCard(ctx, T, me, b.ID, timed.ID, CardPatch{Description: &desc}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.AddComment(ctx, T, me, b.ID, allDay.ID, "private comment text"); err != nil {
		t.Fatal(err)
	}

	// Off by default: no feed can be made.
	if _, _, err := o.CreateBoardFeed(ctx, T, me, "mine", ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("feed made while feeds are off: %v", err)
	}
	if _, _, err := o.CreateBoardFeed(ctx, T, me, "board", b.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("board feed made while feeds are off: %v", err)
	}
	// Only the firm's Admin switches them on.
	if err := o.SetFirmFeeds(ctx, T, me, true); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a non-admin switched feeds on: %v", err)
	}
	if _, err := o.SetBoardFeed(ctx, T, me, b.ID, true); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a board admin who is not the firm's Admin allowed a feed: %v", err)
	}
	if err := o.SetFirmFeeds(ctx, T, admin, true); err != nil {
		t.Fatal(err)
	}
	f, token, err := o.CreateBoardFeed(ctx, T, me, "mine", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(token) != 43 || f.ID == "" || f.Scope != "mine" {
		t.Fatalf("feed %+v token %q", f, token)
	}
	// Only the token's hash is kept.
	for _, coll := range []string{cBoardFeed, cBoardFeedKey} {
		ds, _ := o.Docs.List(ctx, T, coll, "")
		for _, d := range ds {
			if strings.Contains(string(d.Body), token) || strings.Contains(d.ID, token) {
				t.Fatalf("the token is stored in %s", coll)
			}
		}
	}
	body, err := o.BoardFeedICS(ctx, T, token, "https://bo.example.test/")
	if err != nil {
		t.Fatal(err)
	}
	cal := parseICS(t, string(body))
	if cal.props["X-WR-CALNAME"] != "Ledgerline: My cards" || len(cal.events) != 2 {
		t.Fatalf("calendar %v with %d events", cal.props, len(cal.events))
	}
	e1, e2 := cal.events[0], cal.events[1]
	if e1["SUMMARY"] != long || e1["DTSTART"] != "20261119T120000Z" || e1["DTEND"] != "20261119T123000Z" || !strings.Contains(e1["UID"], timed.ID) {
		t.Fatalf("timed event %v", e1)
	}
	if e2["SUMMARY"] != `Back\slash; semi, comma` || e2["DTSTART;VALUE=DATE"] != "20261120" || e2["DTEND;VALUE=DATE"] != "20261121" {
		t.Fatalf("all-day event %v", e2)
	}
	link := "https://bo.example.test/#boards/" + b.ID + "/" + allDay.ID
	if e2["URL"] != link || !strings.Contains(e2["DESCRIPTION"], "Board: Close; Q3, review") || !strings.Contains(e2["DESCRIPTION"], link) {
		t.Fatalf("board and link %v", e2)
	}
	if !strings.HasSuffix(e1["DTSTAMP"], "Z") || len(e1["DTSTAMP"]) != 16 {
		t.Fatalf("DTSTAMP %q", e1["DTSTAMP"])
	}
	for _, leak := range []string{"Z-9999", "private comment", "Not mine", "No due date"} {
		if strings.Contains(string(body), leak) {
			t.Fatalf("the feed carries %q", leak)
		}
	}
	// A completed card is marked.
	yes := true
	o.UpdateCard(ctx, T, me, b.ID, allDay.ID, CardPatch{DueDone: &yes})
	body, _ = o.BoardFeedICS(ctx, T, token, "https://bo.example.test")
	if cal = parseICS(t, string(body)); cal.events[1]["SUMMARY"] != `✓ Back\slash; semi, comma` {
		t.Fatalf("done event %v", cal.events[1])
	}

	// Others' tokens, and tokens that are not tokens, find nothing.
	for _, bad := range []string{"", "x", strings.Repeat("A", 43), token + "x", strings.ToUpper(token)} {
		if bad == token {
			continue
		}
		if _, err := o.BoardFeedICS(ctx, T, bad, ""); !errors.Is(err, ErrNotFound) {
			t.Fatalf("token %q: %v", bad, err)
		}
	}
	// The listing names the feed but never its address.
	fv, _ := o.BoardFeeds(ctx, T, me)
	if !fv.FirmOn || fv.CanSwitch || len(fv.Feeds) != 1 || fv.Feeds[0].ID != f.ID || !fv.Feeds[0].Allowed {
		t.Fatalf("feeds view %+v", fv)
	}
	if av, _ := o.BoardFeeds(ctx, T, admin); !av.CanSwitch || len(av.Feeds) != 0 {
		t.Fatalf("admin's feeds view %+v", av)
	}

	// A new address replaces the old; revoking stops it; only the owner revokes.
	_, token2, err := o.CreateBoardFeed(ctx, T, me, "mine", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.BoardFeedICS(ctx, T, token, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the replaced address still works: %v", err)
	}
	fv, _ = o.BoardFeeds(ctx, T, me)
	if len(fv.Feeds) != 1 {
		t.Fatalf("feeds after a new address %+v", fv.Feeds)
	}
	if err := o.RevokeBoardFeed(ctx, T, other, fv.Feeds[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("farhan stopped priya's feed: %v", err)
	}
	if _, err := o.BoardFeedICS(ctx, T, token2, ""); err != nil {
		t.Fatal(err)
	}
	if err := o.RevokeBoardFeed(ctx, T, me, fv.Feeds[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := o.BoardFeedICS(ctx, T, token2, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a stopped feed still works: %v", err)
	}

	// A board feed; the firm switched off empties it, so subscribed
	// calendars drop the cards; the board's own switch lets it through.
	_, bt, err := o.CreateBoardFeed(ctx, T, me, "board", b.ID)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = o.BoardFeedICS(ctx, T, bt, "")
	if cal = parseICS(t, string(body)); len(cal.events) != 3 || cal.props["X-WR-CALNAME"] != "Ledgerline: Close; Q3, review" {
		t.Fatalf("board feed %v %d", cal.props, len(cal.events))
	}
	if err := o.SetFirmFeeds(ctx, T, admin, false); err != nil {
		t.Fatal(err)
	}
	body, err = o.BoardFeedICS(ctx, T, bt, "")
	if err != nil {
		t.Fatal(err)
	}
	if cal = parseICS(t, string(body)); len(cal.events) != 0 {
		t.Fatalf("feeds off, yet %d events", len(cal.events))
	}
	if _, err := o.SetBoardFeed(ctx, T, admin, b.ID, true); err != nil {
		t.Fatal(err)
	}
	body, _ = o.BoardFeedICS(ctx, T, bt, "")
	if cal = parseICS(t, string(body)); len(cal.events) != 3 {
		t.Fatalf("board allowed, %d events", len(cal.events))
	}
	if _, _, err := o.CreateBoardFeed(ctx, T, me, "mine", ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("my cards feed while the firm is off: %v", err)
	}
	// Someone who leaves the board gets an empty calendar.
	if _, err := o.UpdateBoard(ctx, T, me, b.ID, BoardInput{Members: []BoardPerson{{me, BoardAdmin}, {other, BoardMember}}}); err != nil {
		t.Fatal(err)
	}
	_, ft, err := o.CreateBoardFeed(ctx, T, other, "board", b.ID)
	if err != nil {
		t.Fatal(err)
	}
	o.UpdateBoard(ctx, T, me, b.ID, BoardInput{Members: []BoardPerson{{me, BoardAdmin}}})
	body, _ = o.BoardFeedICS(ctx, T, ft, "")
	if cal = parseICS(t, string(body)); len(cal.events) != 0 {
		t.Fatalf("farhan's feed after leaving the board: %d events", len(cal.events))
	}
	// A deactivated person's feeds stop.
	us, _ := o.Users(ctx, T)
	for _, u := range us {
		if u.ID == me {
			u.Active = false
			if err := o.PutUser(ctx, T, admin, u); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := o.BoardFeedICS(ctx, T, bt, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a deactivated person's feed: %v", err)
	}
}

// Folding never splits a character and keeps every line within 75 octets.
func TestBoardICSFolding(t *testing.T) {
	for _, n := range []int{1, 60, 73, 74, 75, 76, 200} {
		for _, r := range []string{"a", "é", "✓", "𝄞"} {
			var w icsLines
			s := "SUMMARY:" + strings.Repeat(r, n)
			w.add(s)
			out := w.b.String()
			for _, l := range strings.Split(strings.TrimSuffix(out, "\r\n"), "\r\n") {
				if len(l) > 75 || !utf8.ValidString(l) {
					t.Fatalf("%d × %q: line %q", n, r, l)
				}
			}
			if got := strings.ReplaceAll(strings.TrimSuffix(out, "\r\n"), "\r\n ", ""); got != s {
				t.Fatalf("%d × %q unfolds to %q", n, r, got)
			}
		}
	}
	if got := icsText("a\\b;c,d\ne\rf\x01g"); got != `a\\b\;c\,d\nef g` {
		t.Fatalf("escaped %q", got)
	}
	_ = fmt.Sprint
	_ = docs.ErrNotFound
}
