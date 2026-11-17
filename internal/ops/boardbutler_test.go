package ops

import (
	"errors"
	"strings"
	"testing"

	"ledgerline/internal/docs"
)

// Butler parity: the newer triggers, the "by" filters and the checklist
// conditions.
func TestBoardRuleNewTriggers(t *testing.T) {
	o, ctx, b := baSetup(t, "2026-11-16T10:00")
	maker, checker := "priya.nair", "farhan.khan"
	todo, doing := b.Lists[0].ID, b.Lists[1].ID
	b, err := o.UpdateBoard(ctx, T, maker, b.ID, BoardInput{Fields: []BoardField{{Name: "Priority", Type: "list", Options: []string{"Low", "High"}}}})
	if err != nil {
		t.Fatal(err)
	}
	fid := b.Fields[0].ID
	// Each rule comments its name, so which ran shows on the card.
	rule := func(name string, tr AutoTrigger, conds ...AutoCond) {
		t.Helper()
		if _, err := o.SaveBoardAuto(ctx, T, maker, b.ID, BoardAuto{Kind: AutoRule, Name: name, Trigger: &tr, Conditions: conds,
			Actions: []AutoAction{{Kind: "comment", Text: name + " {cardname} on {boardname} in {listname} by {username}"}}}); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	rule("mine", AutoTrigger{Event: "card_added", By: maker})
	rule("others", AutoTrigger{Event: "card_added", By: "!" + maker})
	rule("ticked", AutoTrigger{Event: "item_checked", Words: "bank"})
	rule("unticked", AutoTrigger{Event: "item_unchecked"})
	rule("added", AutoTrigger{Event: "item_added", Words: "sign"})
	rule("attached", AutoTrigger{Event: "attachment_added"})
	rule("high", AutoTrigger{Event: "field_set", Field: fid, Value: "High"})
	rule("renamed", AutoTrigger{Event: "card_renamed"})
	rule("undone", AutoTrigger{Event: "card_undone"})
	rule("top", AutoTrigger{Event: "card_moved_end", Pos: "top", List: doing})
	rule("back", AutoTrigger{Event: "card_unarchived"})
	rule("allDone", AutoTrigger{Event: "card_moved_in", List: todo}, AutoCond{Kind: "items_all_done"})
	rule("someDone", AutoTrigger{Event: "card_moved_in", List: todo}, AutoCond{Kind: "items_some_done"})
	rule("noneDone", AutoTrigger{Event: "card_moved_in", List: todo}, AutoCond{Kind: "items_none_done"})
	said := func(c Card) []string {
		t.Helper()
		c = baCard(t, o, ctx, b, c.ID)
		var out []string
		for _, cm := range c.Comments {
			out = append(out, strings.Fields(cm.Text)[0])
		}
		return out
	}
	mine, _ := o.AddCard(ctx, T, maker, b.ID, CardInput{List: doing, Title: "Mine"})
	theirs, _ := o.AddCard(ctx, T, checker, b.ID, CardInput{List: doing, Title: "Theirs"})
	if got := strings.Join(said(mine), ","); got != "mine" {
		t.Fatalf("by me: %s", got)
	}
	if got := strings.Join(said(theirs), ","); got != "others" {
		t.Fatalf("by someone else: %s", got)
	}
	if c := baCard(t, o, ctx, b, theirs.ID); c.Comments[0].Text != "others Theirs on Operations in Doing by farhan.khan" {
		t.Fatalf("variables: %q", c.Comments[0].Text)
	}
	// Checklist items: ticked (with words), unticked, added (with words).
	c, _ := o.AddCard(ctx, T, checker, b.ID, CardInput{List: doing, Title: "Payout"})
	c, _ = o.EditChecklist(ctx, T, checker, b.ID, c.ID, ChecklistOp{Op: "add_list", Name: "Checks"})
	cl := c.Checklists[0].ID
	c, _ = o.EditChecklist(ctx, T, checker, b.ID, c.ID, ChecklistOp{Op: "add_item", Checklist: cl, Name: "Bank proof"})
	c, _ = o.EditChecklist(ctx, T, checker, b.ID, c.ID, ChecklistOp{Op: "add_item", Checklist: cl, Name: "Signed form"})
	yes, no := true, false
	tick := func(i int, v *bool) {
		t.Helper()
		if _, err := o.EditChecklist(ctx, T, checker, b.ID, c.ID, ChecklistOp{Op: "update_item", Checklist: cl, Item: c.Checklists[0].Items[i].ID, Done: v}); err != nil {
			t.Fatal(err)
		}
	}
	tick(0, &yes)
	tick(1, &yes)
	tick(1, &no)
	if got := strings.Join(said(c), ","); got != "others,added,ticked,unticked" {
		t.Fatalf("checklist triggers: %s", got)
	}
	// Moved into To do with some items done, then with all of them.
	move := func(c Card, list string) {
		t.Helper()
		if _, err := o.MoveCard(ctx, T, checker, b.ID, c.ID, CardMove{List: list}); err != nil {
			t.Fatal(err)
		}
	}
	move(c, todo)
	tick(1, &yes)
	move(c, doing)
	move(c, todo)
	if got := strings.Join(said(c)[4:], ","); got != "someDone,allDone" {
		t.Fatalf("checklist conditions: %s", got)
	}
	n0, _ := o.AddCard(ctx, T, checker, b.ID, CardInput{List: doing, Title: "No checklist"})
	move(n0, todo)
	if got := strings.Join(said(n0), ","); got != "others,noneDone" {
		t.Fatalf("no items done: %s", got)
	}
	// Attachment, custom field (only when set to High), rename, not complete, unarchived.
	d, _ := o.AddCard(ctx, T, checker, b.ID, CardInput{List: todo, Title: "Contract notes", Due: "2026-11-20"})
	patch := func(p CardPatch) {
		t.Helper()
		if _, err := o.UpdateCard(ctx, T, checker, b.ID, d.ID, p); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := o.AttachLink(ctx, T, checker, b.ID, d.ID, "https://example.com/notes", ""); err != nil {
		t.Fatal(err)
	}
	patch(CardPatch{Fields: &map[string]string{fid: "Low"}})
	patch(CardPatch{Fields: &map[string]string{fid: "High"}})
	patch(CardPatch{Fields: &map[string]string{}})
	title := "Contract notes for October"
	patch(CardPatch{Title: &title})
	patch(CardPatch{DueDone: &yes})
	patch(CardPatch{DueDone: &no})
	patch(CardPatch{Archived: &yes})
	patch(CardPatch{Archived: &no})
	if got := strings.Join(said(d), ","); got != "others,attached,high,renamed,undone,back" {
		t.Fatalf("card triggers: %s", got)
	}
	// Moved to the top of Doing (within the list, and from another list); not to its bottom.
	top := 1.0
	if _, err := o.MoveCard(ctx, T, checker, b.ID, theirs.ID, CardMove{List: doing, Pos: &top}); err != nil {
		t.Fatal(err)
	}
	move(d, doing)
	if _, err := o.MoveCard(ctx, T, checker, b.ID, d.ID, CardMove{List: doing, Pos: &top}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(said(theirs), ","); got != "others,top" {
		t.Fatalf("moved to the top: %v", got)
	}
	if got := said(d); got[len(got)-1] != "top" || strings.Count(strings.Join(got, ","), "top") != 1 {
		t.Fatalf("moved to the bottom, then the top: %v", got)
	}
	// Bad triggers are refused.
	for _, tr := range []AutoTrigger{{Event: "card_moved_end", Pos: "middle"}, {Event: "field_set", Field: "F-nope"}, {Event: "field_set", Field: fid, Value: "Medium"},
		{Event: "field_set", Value: "High"}, {Event: "card_added", By: "!vikram.shah"}} {
		if _, err := o.SaveBoardAuto(ctx, T, maker, b.ID, BoardAuto{Kind: AutoRule, Name: "bad", Trigger: &tr, Actions: []AutoAction{{Kind: "archive"}}}); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v: %v", tr, err)
		}
	}
}

// Butler parity: the newer actions, on card buttons.
func TestBoardButtonNewActions(t *testing.T) {
	o, ctx, b := baSetup(t, "2026-11-16T10:00")
	maker, checker, risk := "priya.nair", "farhan.khan", "anita.rao"
	doing := b.Lists[1].ID
	b, _ = o.UpdateBoard(ctx, T, maker, b.ID, BoardInput{Fields: []BoardField{{Name: "Closed by", Type: "text"}, {Name: "Urgent", Type: "checkbox"}}})
	other, err := o.CreateBoard(ctx, T, maker, BoardInput{Name: "Compliance desk", Visibility: "private"})
	if err != nil {
		t.Fatal(err)
	}
	// Only an Admin's address (or the configured one) is taken, without its path.
	o.NoteConsoleAddress(ctx, T, checker, "https://elsewhere.example", false)
	o.NoteConsoleAddress(ctx, T, "admin", "https://bo.bhairav.example/whatever?x=1", false)
	if got := o.BoardCardLink(ctx, T, "B-1", "C-2"); got != "https://bo.bhairav.example/#boards/B-1/C-2" {
		t.Fatalf("card link %q", got)
	}
	if _, err := o.SaveBoardAuto(ctx, T, maker, b.ID, BoardAuto{Kind: AutoCardButton, Name: "Bad", Actions: []AutoAction{{Kind: "set_field", Field: b.Fields[1].ID, Text: "yes"}}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a checkbox set to yes: %v", err)
	}
	btn, err := o.SaveBoardAuto(ctx, T, maker, b.ID, BoardAuto{Kind: AutoCardButton, Name: "Close out", Actions: []AutoAction{
		{Kind: "check_all"}, {Kind: "set_field", Field: b.Fields[0].ID, Text: "{username}"}, {Kind: "set_field", Field: b.Fields[1].ID, Text: "true"},
		{Kind: "remove_labels"}, {Kind: "remove_members"}, {Kind: "add_watcher", Member: risk}, {Kind: "move_top"},
		{Kind: "copy_board", Board: other.ID, List: other.Lists[0].ID},
		{Kind: "comment", Text: "Closed {cardlink} due {duedate}"},
		{Kind: "email", To: []string{"watchers", checker}, Text: "{cardname} is closed"}}})
	if err != nil {
		t.Fatal(err)
	}
	first, _ := o.AddCard(ctx, T, maker, b.ID, CardInput{List: doing, Title: "First"})
	c, _ := o.AddCard(ctx, T, maker, b.ID, CardInput{List: doing, Title: "Client funds check", Due: "2026-11-20", Labels: []string{b.Labels[0].ID, b.Labels[1].ID}, Members: []string{checker}})
	c, _ = o.EditChecklist(ctx, T, maker, b.ID, c.ID, ChecklistOp{Op: "add_list", Name: "Steps"})
	for _, it := range []string{"Bank", "Ledger", "Holdings"} {
		c, _ = o.EditChecklist(ctx, T, maker, b.ID, c.ID, ChecklistOp{Op: "add_item", Checklist: c.Checklists[0].ID, Name: it})
	}
	res, err := o.RunBoardAuto(ctx, T, maker, b.ID, btn.ID, c.ID, false)
	if err != nil || res.Run.Status != "done" {
		t.Fatalf("run %+v %v", res.Run, err)
	}
	c = baCard(t, o, ctx, b, c.ID)
	first = baCard(t, o, ctx, b, first.ID)
	done, all := baItemsDone(c)
	if done != 3 || all != 3 || c.Fields[b.Fields[0].ID] != maker || c.Fields[b.Fields[1].ID] != "true" || len(c.Labels) != 0 || len(c.Members) != 0 ||
		!contains(c.Watchers, risk) || c.Pos >= first.Pos || c.List != doing {
		t.Fatalf("card after the button %+v", c)
	}
	if len(c.Comments) != 1 || c.Comments[0].Text != "Closed https://bo.bhairav.example/#boards/"+b.ID+"/"+c.ID+" due 20 Nov 2026" {
		t.Fatalf("comment %+v", c.Comments)
	}
	cs, _ := o.boardCards(ctx, T, other.ID)
	if len(cs) != 1 || cs[0].Title != c.Title || len(cs[0].Checklists) != 1 || len(cs[0].Labels) != 0 {
		t.Fatalf("copy on the other board %+v", cs)
	}
	// The email action leaves notices to be emailed with the next batch
	// (the watchers: the checker, who was a member, and the risk manager).
	for _, who := range []string{checker, risk} {
		ns, _ := docs.List[bnNotice](ctx, o.Docs, T, cBoardNote, who+"|")
		var got *bnNotice
		for i := range ns {
			if ns[i].Push {
				got = &ns[i]
			}
		}
		if got == nil || got.Kind != NoticeAutomation || got.Text != "Client funds check is closed" || got.Card != c.ID {
			t.Fatalf("%s: email notices %+v", who, ns)
		}
	}
	// Untick all; move to the bottom.
	btn2, err := o.SaveBoardAuto(ctx, T, maker, b.ID, BoardAuto{Kind: AutoCardButton, Name: "Reopen", Actions: []AutoAction{{Kind: "uncheck_all"}, {Kind: "move_bottom"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.RunBoardAuto(ctx, T, maker, b.ID, btn2.ID, c.ID, false); err != nil {
		t.Fatal(err)
	}
	c = baCard(t, o, ctx, b, c.ID)
	if done, _ := baItemsDone(c); done != 0 || c.Pos <= first.Pos {
		t.Fatalf("after reopening %+v", c)
	}
	// Copying to another board needs the saver's access there, when saved and when it runs.
	if _, err := o.SaveBoardAuto(ctx, T, checker, b.ID, BoardAuto{Kind: AutoCardButton, Name: "Copy", Actions: []AutoAction{{Kind: "copy_board", Board: other.ID, List: other.Lists[0].ID}}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("copied to a board the saver cannot see: %v", err)
	}
	if _, err := o.UpdateBoard(ctx, T, maker, other.ID, BoardInput{Members: []BoardPerson{{maker, BoardAdmin}, {checker, BoardMember}}}); err != nil {
		t.Fatal(err)
	}
	cp, err := o.SaveBoardAuto(ctx, T, checker, b.ID, BoardAuto{Kind: AutoCardButton, Name: "Copy", Actions: []AutoAction{{Kind: "copy_board", Board: other.ID, List: other.Lists[0].ID}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.UpdateBoard(ctx, T, maker, other.ID, BoardInput{Members: []BoardPerson{{maker, BoardAdmin}}}); err != nil {
		t.Fatal(err)
	}
	if res, _ := o.RunBoardAuto(ctx, T, maker, b.ID, cp.ID, c.ID, false); res.Run.Status != "failed" || !strings.Contains(res.Run.Error, "farhan.khan can no longer add cards") {
		t.Fatalf("copy after the saver lost access: %+v", res.Run)
	}
	// A Ledgerline board refuses copying to another board; moving up its list is fine.
	ll := Board{ID: "B-ll2", Name: "Breaks", Kind: BoardLedgerline, Visibility: "firm", Lists: []BoardList{{ID: "L-1", Name: "Open", Pos: 1}}, CreatedBy: "system", CreatedAt: "2026-11-01T00:00:00Z"}
	if err := docs.Upsert(ctx, o.Docs, T, cBoard, ll.ID, ll, "system"); err != nil {
		t.Fatal(err)
	}
	if _, err := o.SaveBoardAuto(ctx, T, "admin", ll.ID, BoardAuto{Kind: AutoCardButton, Name: "x", Actions: []AutoAction{{Kind: "copy_board", Board: other.ID, List: other.Lists[0].ID}}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("copy_board on a Ledgerline board: %v", err)
	}
	if _, err := o.SaveBoardAuto(ctx, T, "admin", ll.ID, BoardAuto{Kind: AutoCardButton, Name: "x", Actions: []AutoAction{{Kind: "move_top"}, {Kind: "remove_labels"}, {Kind: "check_all"}}}); err != nil {
		t.Fatalf("reorder and tidy on a Ledgerline board: %v", err)
	}
	// A scheduled command emails people by name, not "the card's members".
	if _, err := o.SaveBoardAuto(ctx, T, maker, b.ID, BoardAuto{Kind: AutoScheduled, Name: "s", When: &AutoWhen{Every: "day", At: "09:00"}, Actions: []AutoAction{{Kind: "email", To: []string{"card_members"}}}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("scheduled email to card members: %v", err)
	}
	if _, err := o.SaveBoardAuto(ctx, T, maker, b.ID, BoardAuto{Kind: AutoScheduled, Name: "s", When: &AutoWhen{Every: "day", At: "09:00"}, Actions: []AutoAction{{Kind: "email", To: []string{checker}, Text: "Morning"}}}); err != nil {
		t.Fatal(err)
	}
}
