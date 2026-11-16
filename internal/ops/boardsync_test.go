package ops

import (
	"errors"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/app"
)

// bsClock sets the firm's clock (and the reconciliation's) to the sample
// day's evening; the function it returns moves it on.
func bsClock(o *Ops) func(time.Duration) {
	at := time.Date(2026, 10, 16, 18, 0, 0, 0, ist)
	now := func() time.Time { return at }
	o.Now, o.App.Now = now, now
	return func(d time.Duration) { at = at.Add(d) }
}

func bsListBy(t *testing.T, b Board, status string) BoardList {
	t.Helper()
	for _, l := range b.Lists {
		if l.Source == status {
			return l
		}
	}
	t.Fatalf("board %s has no list for %q", b.Name, status)
	return BoardList{}
}

func bsCardFor(v BoardView, kind, id string) (Card, bool) {
	for _, c := range v.Cards {
		if c.Link != nil && c.Link.Kind == kind && c.Link.ID == id {
			return c, true
		}
	}
	return Card{}, false
}

func TestLedgerlineBoardSetUp(t *testing.T) {
	o, ctx := setup(t)
	bsClock(o)
	if _, err := o.SetUpLedgerlineBoards(ctx, T, "priya.nair", nil); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a maker set up Ledgerline boards: %v", err)
	}
	if _, err := o.SetUpLedgerlineBoards(ctx, T, "admin", []string{"trades"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown source: %v", err)
	}
	first, err := o.SetUpLedgerlineBoards(ctx, T, "admin", nil)
	if err != nil || len(first) != 6 {
		t.Fatalf("set up %+v %v", first, err)
	}
	for _, s := range first {
		if !s.Created {
			t.Fatalf("not created %+v", s)
		}
	}
	// Running it again finds the boards rather than making them twice.
	again, err := o.SetUpLedgerlineBoards(ctx, T, "admin", []string{"work", "onboarding"})
	if err != nil || len(again) != 2 || again[0].Created || again[0].Board != first[0].Board || again[1].Board != first[3].Board {
		t.Fatalf("again %+v %v", again, err)
	}
	bs, _ := o.Boards(ctx, T, "admin")
	n := 0
	for _, b := range bs {
		if b.Kind == BoardLedgerline {
			n++
		}
	}
	if n != 6 {
		t.Fatalf("%d Ledgerline boards", n)
	}
	board := func(src string) Board {
		for _, s := range first {
			if s.Source == src {
				v, err := o.GetBoard(ctx, T, "admin", s.Board, false)
				if err != nil {
					t.Fatal(err)
				}
				return v.Board
			}
		}
		t.Fatalf("no %s board", src)
		return Board{}
	}
	ob := board("onboarding")
	if ob.Kind != BoardLedgerline || ob.Source != "onboarding" || ob.Visibility != "firm" || len(ob.Lists) != 5 {
		t.Fatalf("onboarding board %+v", ob)
	}
	for _, l := range ob.Lists {
		if l.Source == "" {
			t.Fatalf("list without a stage %+v", l)
		}
	}
	if _, err := o.AddList(ctx, T, "admin", ob.ID, "Later", nanF()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a list was added to a Ledgerline board: %v", err)
	}
	// The sample day's clients not yet active are on the onboarding board.
	v, _ := o.GetBoard(ctx, T, "priya.nair", ob.ID, false)
	per := map[string]int{}
	for _, c := range v.Cards {
		l, _ := boardList(v.Board, c.List)
		per[l.Source]++
		if c.Link == nil || c.Link.Kind != "client" || c.CreatedBy != bsActor {
			t.Fatalf("card %+v", c)
		}
	}
	if per["Application"] != 2 || per["KYC verification"] != 4 || per["UCC upload"] != 2 || per["Active"] != 0 {
		t.Fatalf("onboarding cards %v", per)
	}
	// Leads are not for risk managers (as on Leads & CRM); the work queue is.
	leads := board("leads")
	if _, err := o.GetBoard(ctx, T, "anita.rao", leads.ID, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a risk manager opened the leads board: %v", err)
	}
	if _, err := o.GetBoard(ctx, T, "priya.nair", leads.ID, false); err != nil {
		t.Fatal(err)
	}
	if v, err := o.GetBoard(ctx, T, "anita.rao", board("work").ID, false); err != nil || v.Role != BoardMember {
		t.Fatalf("risk on the work queue board: %v %v", v.Role, err)
	}
	// The boards home: every board with counts per list.
	home, err := o.LedgerlineBoards(ctx, T, "priya.nair")
	if err != nil || len(home.Boards) != 6 || home.CanSetUp || len(home.Sources) != 6 {
		t.Fatalf("home %+v %v", home, err)
	}
	for _, b := range home.Boards {
		if b.Source == "onboarding" && (b.Cards != 8 || b.Lists[1].Name != "KYC verification" || b.Lists[1].Count != 4) {
			t.Fatalf("onboarding counts %+v", b)
		}
	}
	if h, _ := o.LedgerlineBoards(ctx, T, "anita.rao"); len(h.Boards) != 5 {
		t.Fatalf("risk sees %d boards", len(h.Boards))
	}
	if h, _ := o.LedgerlineBoards(ctx, T, "admin"); !h.CanSetUp {
		t.Fatal("admin cannot set up")
	}
}

func TestLedgerlineBoardSync(t *testing.T) {
	o, ctx := setup(t)
	step := bsClock(o)
	if _, err := o.App.RunRecon(ctx, T, "priya.nair", D); err != nil {
		t.Fatal(err)
	}
	r := &runCtx{o: o, ctx: ctx, tenant: T, date: D, process: "kyc", keys: map[string]bool{}}
	r.queue("risk-BH1012", "Call 9876543210 about PAN ABCDE1234F", "Check the client's income", "BH1012", RoleCompliance, "kyc", "", nil)
	r.queue("risk-BH1013", "Review PMLA risk · BH1013", "", "BH1013", RoleCompliance, "kyc", "", nil)
	set, err := o.SetUpLedgerlineBoards(ctx, T, "admin", []string{"work", "breaks"})
	if err != nil {
		t.Fatal(err)
	}
	work, brk := set[0].Board, set[1].Board

	// A card per open work item, scrubbed of PAN and phone numbers.
	v, err := o.GetBoard(ctx, T, "priya.nair", work, false)
	if err != nil || len(v.Cards) != 2 {
		t.Fatalf("work cards %d %v", len(v.Cards), err)
	}
	wc, ok := bsCardFor(v, "work", "W-kyc-risk-BH1012")
	if !ok {
		t.Fatalf("no card for the work item: %+v", v.Cards)
	}
	if strings.Contains(wc.Title, "9876543210") || strings.Contains(wc.Title, "ABCDE1234F") || strings.Contains(wc.Link.Label, "ABCDE1234F") || !strings.Contains(wc.Title, "BH1012") {
		t.Fatalf("title %q label %q", wc.Title, wc.Link.Label)
	}
	if wc.Due != "2026-10-16T23:59" || wc.List != bsListBy(t, v.Board, "Open").ID || wc.Link.Status != "Open" || wc.Link.View != "kyc" {
		t.Fatalf("work card %+v", wc)
	}
	if l, _ := boardLabel(v.Board, wc.Labels[0]); l.Name != RoleCompliance {
		t.Fatalf("role label %+v", l)
	}

	// A card per open break.
	open, _ := o.App.Breaks.List(ctx, T, "", app.BreakOpen)
	v, _ = o.GetBoard(ctx, T, "priya.nair", brk, false)
	if len(open) < 10 || len(v.Cards) != len(open) {
		t.Fatalf("%d break cards for %d open breaks", len(v.Cards), len(open))
	}
	b := v.Board
	x := open[0]
	c, ok := bsCardFor(v, "break", x.ID)
	if !ok || c.List != bsListBy(t, b, "Open").ID || len(c.Labels) != 2 || !strings.Contains(c.Title, x.UCC) {
		t.Fatalf("break card %+v", c)
	}
	// A person's comment, checklist, own label and member.
	if _, err := o.AddComment(ctx, T, "priya.nair", brk, c.ID, "Spoke to the dealer"); err != nil {
		t.Fatal(err)
	}
	if _, err := o.EditChecklist(ctx, T, "priya.nair", brk, c.ID, ChecklistOp{Op: "add_list", Name: "Fix"}); err != nil {
		t.Fatal(err)
	}
	b, err = o.UpdateBoard(ctx, T, "admin", brk, BoardInput{Description: b.Description, Labels: append(append([]BoardLabel{}, b.Labels...), BoardLabel{Name: "Call dealer", Color: "pink"})})
	if err != nil {
		t.Fatal(err)
	}
	mine := b.Labels[len(b.Labels)-1].ID
	labels, members := append(append([]string{}, c.Labels...), mine), []string{"farhan.khan"}
	if _, err := o.UpdateCard(ctx, T, "priya.nair", brk, c.ID, CardPatch{Labels: &labels, Members: &members}); err != nil {
		t.Fatal(err)
	}
	// Nothing changed in Ledgerline: the sync writes nothing.
	step(time.Minute)
	rev, _ := o.BoardRev(ctx, T, "priya.nair", brk)
	other, _ := bsCardFor(v, "break", open[1].ID)
	v, _ = o.GetBoard(ctx, T, "priya.nair", brk, false)
	if r2, _ := o.BoardRev(ctx, T, "priya.nair", brk); r2 != rev {
		t.Fatalf("an unchanged sync changed the board: rev %d → %d", rev, r2)
	}
	if o2, _ := bsCardFor(v, "break", open[1].ID); o2.UpdatedAt != other.UpdatedAt {
		t.Fatal("an unchanged card was rewritten")
	}

	// The break is resolved on Trade reconciliation: its card moves.
	if _, err := o.App.ResolveBreak(ctx, T, "farhan.khan", x.ID, "Corrected the OMS fill"); err != nil {
		t.Fatal(err)
	}
	step(time.Minute)
	v, _ = o.GetBoard(ctx, T, "priya.nair", brk, false)
	c, _ = bsCardFor(v, "break", x.ID)
	if c.List != bsListBy(t, b, "Resolved").ID || c.Link.Status != "Resolved" {
		t.Fatalf("resolved break's card %+v", c)
	}
	if len(c.Comments) != 1 || len(c.Checklists) != 1 || !contains(c.Labels, mine) || len(c.Labels) != 3 || len(c.Members) != 1 {
		t.Fatalf("the person's additions were lost: %+v", c)
	}
	acts, _ := o.BoardActivityList(ctx, T, "priya.nair", brk, c.ID, 0, 0)
	if len(acts) == 0 || acts[0].What != "card.moved" || acts[0].By != bsActor {
		t.Fatalf("activity %+v", acts)
	}
	if ns, _, _ := o.BoardNotices(ctx, T, "farhan.khan", 0); len(ns) == 0 || !strings.Contains(ns[0].Text, "moved to Resolved") {
		t.Fatalf("the card's member was not told: %+v", ns)
	}
	// The next business day the resolved break leaves the board; it is
	// archived, with the person's comment.
	step(3 * 24 * time.Hour) // Monday
	v, _ = o.GetBoard(ctx, T, "priya.nair", brk, false)
	if _, ok := bsCardFor(v, "break", x.ID); ok || len(v.Cards) != len(open)-1 {
		t.Fatalf("resolved break still on the board (%d cards)", len(v.Cards))
	}
	av, _ := o.GetBoard(ctx, T, "priya.nair", brk, true)
	if c, ok := bsCardFor(av, "break", x.ID); !ok || len(c.Comments) != 1 {
		t.Fatalf("archived card %+v", c)
	}
	// Syncing again does not bring it back or make another card.
	step(time.Minute)
	v, _ = o.GetBoard(ctx, T, "priya.nair", brk, false)
	av, _ = o.GetBoard(ctx, T, "priya.nair", brk, true)
	if len(v.Cards) != len(open)-1 || len(av.Cards) != 1 {
		t.Fatalf("after another sync: %d open, %d archived", len(v.Cards), len(av.Cards))
	}

	// A client made private leaves the boards.
	if _, err := o.SetClientPrivacy(ctx, T, "admin", "BH1013", true, nil); err != nil {
		t.Fatal(err)
	}
	step(time.Minute)
	if v, _ = o.GetBoard(ctx, T, "priya.nair", work, false); len(v.Cards) != 1 {
		t.Fatalf("private client's work item still on the board: %d", len(v.Cards))
	}

	// Branch staff added to a Ledgerline board are taken off it.
	if _, err := o.UpdateBoard(ctx, T, "admin", work, BoardInput{Description: v.Board.Description, Members: []BoardPerson{{"meera.iyer", BoardMember}}}); err != nil {
		t.Fatal(err)
	}
	step(time.Minute)
	if _, err := o.GetBoard(ctx, T, "admin", work, false); err != nil {
		t.Fatal(err)
	}
	if _, err := o.GetBoard(ctx, T, "meera.iyer", work, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("branch staff kept on a Ledgerline board: %v", err)
	}

	// The scheduler's minute syncs at most once a minute.
	r.queue("risk-BH1014", "Review PMLA risk · BH1014", "", "BH1014", RoleCompliance, "kyc", "", nil)
	o.boardsTick(ctx, T, o.now())
	all, _ := o.boardCards(ctx, T, work)
	if len(all) != 2 { // synced by the read above, less than a minute ago
		t.Fatalf("tick inside a minute synced: %d cards", len(all))
	}
	step(time.Minute)
	o.boardsTick(ctx, T, o.now())
	if all, _ = o.boardCards(ctx, T, work); len(all) != 3 {
		t.Fatalf("tick did not sync: %d cards", len(all))
	}
}

func TestLedgerlineBoardMoves(t *testing.T) {
	o, ctx := setup(t)
	step := bsClock(o)
	r := &runCtx{o: o, ctx: ctx, tenant: T, date: D, process: "kyc", keys: map[string]bool{}}
	r.queue("risk-BH1012", "Review PMLA risk · BH1012", "", "BH1012", RoleCompliance, "kyc", "", nil)
	st, _ := o.Obligations(ctx, T, D)
	itc := obl(t, st, "BH1010", "NSE", "ITC")
	if _, err := o.SettleAction(ctx, T, "priya.nair", D, itc.Key, "cuspa"); err != nil {
		t.Fatal(err)
	}
	tk, err := o.OpenTicket(ctx, T, "priya.nair", Ticket{UCC: "BH1010", Category: TicketCategories[0], Subject: "Funds not received"}, "My payout of 5000 has not come")
	if err != nil {
		t.Fatal(err)
	}
	ld, err := o.SaveLead(ctx, T, "priya.nair", Lead{Name: "Arjun Menon", Mobile: "9876500011", City: "Pune", Source: "Website"})
	if err != nil {
		t.Fatal(err)
	}
	set, err := o.SetUpLedgerlineBoards(ctx, T, "admin", nil)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, s := range set {
		ids[s.Source] = s.Board
	}
	view := func(src string) BoardView {
		v, err := o.GetBoard(ctx, T, "admin", ids[src], false)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	move := func(actor, src string, c Card, status string) (Card, error) {
		return o.MoveCard(ctx, T, actor, ids[src], c.ID, CardMove{List: bsListBy(t, view(src).Board, status).ID})
	}

	// A work item moved to Done is closed, with the person's role checked.
	v := view("work")
	wc, _ := bsCardFor(v, "work", "W-kyc-risk-BH1012")
	if _, err := move("priya.nair", "work", wc, "Done"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a maker closed a compliance item: %v", err)
	}
	if _, err := move("vikram.shah", "work", wc, "Cleared"); err == nil {
		t.Fatal("moved to Cleared")
	}
	c, err := move("vikram.shah", "work", wc, "Done")
	if err != nil || c.Link.Status != "Dismissed" {
		t.Fatalf("move to Done: %+v %v", c.Link, err)
	}
	ws, _ := o.WorkItems(ctx, T, "")
	if ws[len(ws)-1].ID != wc.Link.ID || ws[len(ws)-1].Status != "Dismissed" || ws[len(ws)-1].Note != "Moved to Done on the board" || ws[len(ws)-1].ClosedBy != "vikram.shah" {
		t.Fatalf("work item %+v", ws[len(ws)-1])
	}
	step(time.Minute)
	v = view("work")
	if c, _ = bsCardFor(v, "work", wc.Link.ID); c.List != bsListBy(t, v.Board, "Done").ID || !c.DueDone {
		t.Fatalf("card after the sync %+v", c)
	}
	if _, err := move("vikram.shah", "work", c, "Open"); err == nil {
		t.Fatal("a closed item reopened")
	}
	step(8 * 24 * time.Hour) // closed more than a week ago
	if v = view("work"); len(v.Cards) != 0 {
		t.Fatalf("closed item still on the board after a week: %d", len(v.Cards))
	}

	// Unpaid securities are never moved from the board.
	step(-8*24*time.Hour - time.Minute)
	v = view("cuspa")
	if len(v.Cards) != 1 {
		t.Fatalf("cuspa cards %d", len(v.Cards))
	}
	cu := v.Cards[0]
	if _, err := move("farhan.khan", "cuspa", cu, "closed"); err == nil || !strings.Contains(err.Error(), "Settlement → Unpaid securities") {
		t.Fatalf("cuspa move: %v", err)
	}
	if v = view("cuspa"); v.Cards[0].List != cu.List {
		t.Fatal("the refused card moved")
	}
	// Breaks are resolved with a note on their screen.
	if _, err := o.App.RunRecon(ctx, T, "priya.nair", D); err != nil {
		t.Fatal(err)
	}
	step(time.Minute)
	v = view("breaks")
	if _, err := move("farhan.khan", "breaks", v.Cards[0], "Resolved"); err == nil || !strings.Contains(err.Error(), "Trade reconciliation") {
		t.Fatalf("break move: %v", err)
	}

	// Onboarding goes one stage at a time, under maker-checker.
	v = view("onboarding")
	var kyc Card
	for _, c := range v.Cards {
		if l, _ := boardList(v.Board, c.List); l.Source == "KYC verification" {
			kyc = c
		}
	}
	if _, err := move("priya.nair", "onboarding", kyc, "Active"); err == nil || !strings.Contains(err.Error(), "one stage at a time") {
		t.Fatalf("skipped a stage: %v", err)
	}
	if c, err = move("priya.nair", "onboarding", kyc, "UCC upload"); err != nil || c.Link.Status != "UCC upload" {
		t.Fatalf("advance: %v", err)
	}
	if cl, _ := o.Client(ctx, T, kyc.Link.ID); cl.Status != "UCC upload" {
		t.Fatalf("client %s is %s", cl.UCC, cl.Status)
	}
	if _, err := move("priya.nair", "onboarding", c, "Active"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a maker activated a client: %v", err)
	}

	// Complaints: worked from the board; resolved with a reply on their screen.
	v = view("complaints")
	tc, ok := bsCardFor(v, "ticket", tk.ID)
	if !ok || tc.Due != tk.Due {
		t.Fatalf("ticket card %+v", tc)
	}
	if _, err := move("priya.nair", "complaints", tc, "Resolved"); err == nil || !strings.Contains(err.Error(), "Investor complaints") {
		t.Fatalf("resolved from the board: %v", err)
	}
	if _, err := move("priya.nair", "complaints", tc, "Closed"); err == nil {
		t.Fatal("closed an unresolved complaint")
	}
	if _, err := move("priya.nair", "complaints", tc, "In progress"); err != nil {
		t.Fatal(err)
	}
	ts, _ := o.Tickets(ctx, T, D)
	if ts[0].Status != "In progress" || len(ts[0].Internal) != 1 {
		t.Fatalf("ticket %+v", ts[0])
	}

	// Leads: stages from the board; never the mobile number on the card.
	v = view("leads")
	lc, ok := bsCardFor(v, "lead", ld.ID)
	if !ok || strings.Contains(lc.Title, "9876500011") || lc.Title != "Arjun Menon · Pune" {
		t.Fatalf("lead card %+v", lc)
	}
	if _, err := move("priya.nair", "leads", lc, "Lost"); err == nil {
		t.Fatal("lost without a reason")
	}
	if _, err := move("priya.nair", "leads", lc, "Contacted"); err != nil {
		t.Fatal(err)
	}
	if l, _, _ := o.ActOnLead(ctx, T, "priya.nair", ld.ID, LeadAction{Kind: "note", Text: "x"}); l.Stage != "Contacted" {
		t.Fatalf("lead stage %s", l.Stage)
	}

	// On a team board a linked card moves freely: its lists are the team's.
	tb, _ := o.CreateBoard(ctx, T, "priya.nair", BoardInput{Name: "Follow-ups"})
	card, _ := o.AddCard(ctx, T, "priya.nair", tb.ID, CardInput{List: tb.Lists[0].ID, Title: "Chase the payout"})
	link := CardLink{Kind: "work", ID: wc.Link.ID, Label: "x"}
	if _, err := o.UpdateCard(ctx, T, "priya.nair", tb.ID, card.ID, CardPatch{Link: &link}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.MoveCard(ctx, T, "priya.nair", tb.ID, card.ID, CardMove{List: tb.Lists[2].ID}); err != nil {
		t.Fatalf("team board move: %v", err)
	}
}

func TestLedgerlineBoardLinkSearch(t *testing.T) {
	o, ctx := setup(t)
	bsClock(o)
	if _, err := o.App.RunRecon(ctx, T, "priya.nair", D); err != nil {
		t.Fatal(err)
	}
	cs, _ := o.ClientMap(ctx, T)
	var blr, del string
	for _, u := range sortedKeys(cs) {
		switch {
		case cs[u].Branch == "Bengaluru" && blr == "":
			blr = u
		case cs[u].Branch == "Delhi" && del == "":
			del = u
		}
	}
	if blr == "" || del == "" {
		t.Fatal("sample has no Bengaluru or Delhi client")
	}
	r := &runCtx{o: o, ctx: ctx, tenant: T, date: D, process: "kyc", keys: map[string]bool{}}
	r.queue("risk-"+blr, "Review PMLA risk · "+blr, "", blr, RoleCompliance, "kyc", "", nil)
	r.queue("risk-"+del, "Review PMLA risk · "+del, "", del, RoleCompliance, "kyc", "", nil)

	// Branch staff find only their branch's clients and records.
	hits, err := o.BoardLinkSearch(ctx, T, "meera.iyer", "BH", "client")
	if err != nil || len(hits) == 0 {
		t.Fatalf("branch client search %v %v", hits, err)
	}
	for _, h := range hits {
		if cs[h.ID].Branch != "Bengaluru" {
			t.Fatalf("meera found %s of %s", h.ID, cs[h.ID].Branch)
		}
		if strings.Contains(h.Sub, cs[h.ID].PAN) || !strings.Contains(h.Sub, "XXXX") {
			t.Fatalf("PAN not masked: %q", h.Sub)
		}
	}
	if hs, _ := o.BoardLinkSearch(ctx, T, "meera.iyer", del, ""); len(hs) != 0 {
		t.Fatalf("meera found another branch's client: %+v", hs)
	}
	if hs, _ := o.BoardLinkSearch(ctx, T, "meera.iyer", "PMLA", "work"); len(hs) != 1 || hs[0].UCC != blr {
		t.Fatalf("meera's work items %+v", hs)
	}
	if hs, _ := o.BoardLinkSearch(ctx, T, "meera.iyer", "BRK", "break"); len(hs) != 0 {
		t.Fatalf("branch staff found breaks: %+v", hs)
	}
	if _, err := o.BoardLinkRecord(ctx, T, "meera.iyer", "client", del); !errors.Is(err, ErrNotFound) {
		t.Fatalf("meera read another branch's client: %v", err)
	}
	if h, err := o.BoardLinkRecord(ctx, T, "meera.iyer", "client", blr); err != nil || h.Status != cs[blr].Status {
		t.Fatalf("own client %+v %v", h, err)
	}

	// Head office finds every branch's, by code, name or full PAN.
	if hs, _ := o.BoardLinkSearch(ctx, T, "priya.nair", del, "client"); len(hs) != 1 || !strings.HasPrefix(hs[0].Label, del+" ") {
		t.Fatalf("head office search %+v", hs)
	}
	if hs, _ := o.BoardLinkSearch(ctx, T, "priya.nair", cs[del].PAN, ""); len(hs) != 1 || hs[0].ID != del || strings.Contains(hs[0].Sub, cs[del].PAN) {
		t.Fatalf("PAN search %+v", hs)
	}
	bh, _ := o.BoardLinkSearch(ctx, T, "priya.nair", "BRK", "break")
	if len(bh) == 0 || bh[0].Status != "Open" {
		t.Fatalf("break search %+v", bh)
	}
	if h, err := o.BoardLinkRecord(ctx, T, "priya.nair", "break", bh[0].ID); err != nil || len(h.Lines) < 2 || h.Date != D {
		t.Fatalf("break record %+v %v", h, err)
	}
	// Leads stay with the roles that see them on Leads & CRM.
	if _, err := o.SaveLead(ctx, T, "priya.nair", Lead{Name: "Kavya Rao", Mobile: "9876500012", Source: "Referral"}); err != nil {
		t.Fatal(err)
	}
	if hs, err := o.BoardLinkSearch(ctx, T, "anita.rao", "Kavya", "lead"); err != nil || len(hs) != 0 {
		t.Fatalf("risk found a lead: %+v %v", hs, err)
	}
	if hs, _ := o.BoardLinkSearch(ctx, T, "priya.nair", "Kavya", "lead"); len(hs) != 1 || strings.Contains(hs[0].Sub+hs[0].Label, "9876500012") {
		t.Fatalf("lead search %+v", hs)
	}
	// A private client is found only by those named on it.
	if _, err := o.SetClientPrivacy(ctx, T, "admin", del, true, []string{"farhan.khan"}); err != nil {
		t.Fatal(err)
	}
	if hs, _ := o.BoardLinkSearch(ctx, T, "priya.nair", del, ""); len(hs) != 0 {
		t.Fatalf("private client found: %+v", hs)
	}
	if hs, _ := o.BoardLinkSearch(ctx, T, "farhan.khan", del, "client"); len(hs) != 1 || hs[0].Label != del {
		t.Fatalf("named user's search %+v", hs)
	}
	if _, err := o.BoardLinkSearch(ctx, T, "priya.nair", "BH", "trades"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown kind: %v", err)
	}
}

func TestLedgerlineBoardScrub(t *testing.T) {
	for in, want := range map[string]string{
		"PAN abcde1234f, mobile +91 98765 43210":                         "PAN ABCDEXXXXF, mobile XXXXXX3210",
		"Aadhaar 2345 6789 0123":                                         "Aadhaar XXXX XXXX 0123",
		"a/c 001234567890123 demat IN30012345678901 BO 1208160012345678": "a/c XXXXXXXXXXX0123 demat XXXXXXXXXXXX8901 BO XXXXXXXXXXXX5678",
		"Break BRK-6aa708995968 · qty mismatch · BH1030":                 "Break BRK-6aa708995968 · qty mismatch · BH1030",
	} {
		if got := bsScrub(in); got != want {
			t.Errorf("bsScrub(%q) = %q, want %q", in, got, want)
		}
	}
	if got := bsInstrument("NIFTY|PE|2026-10-20|25700"); got != "NIFTY 20OCT26 25700 PE" {
		t.Errorf("instrument %q", got)
	}
}
