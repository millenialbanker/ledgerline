package ops

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"ledgerline/internal/docs"
)

func trelloFixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/trello-board.json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBoardTrelloRefusesOtherFiles(t *testing.T) {
	for name, raw := range map[string]string{
		"empty":        "",
		"not json":     "lists,cards\n1,2",
		"cut short":    `{"id":"5f0c1a2b3c4d5e6f7a8b9c0d","name":"x","lists":[`,
		"an array":     `[{"id":"5f0c1a2b3c4d5e6f7a8b9c0d"}]`,
		"other json":   `{"hello":"world","lists":[],"cards":[]}`,
		"no trello id": `{"id":"B-1","name":"Mine","lists":[],"cards":[],"labels":[]}`,
		"no cards":     `{"id":"5f0c1a2b3c4d5e6f7a8b9c0d","name":"x","lists":[],"labels":[]}`,
		"lists as map": `{"id":"5f0c1a2b3c4d5e6f7a8b9c0d","name":"x","lists":{},"cards":[],"labels":[]}`,
		"card export":  `{"id":"5f0c1a2b3c4d5e6f7a8b9c0d","name":"A card","idList":"6b0000000000000000000001","idBoard":"5f0c1a2b3c4d5e6f7a8b9c0e"}`,
	} {
		_, err := parseTrello([]byte(raw))
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
		if name == "card export" && !strings.Contains(err.Error(), "card export") {
			t.Errorf("card export not named: %v", err)
		}
	}
	if _, err := parseTrello(bytes.Repeat([]byte(" "), BoardImportMax+1)); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "20 MB") {
		t.Fatalf("over 20 MB: %v", err)
	}
	// The smallest board export is taken.
	if b, err := parseTrello([]byte(`{"id":"5f0c1a2b3c4d5e6f7a8b9c0d","name":"Tiny","lists":[],"cards":[],"labels":[]}`)); err != nil || b.Name != "Tiny" {
		t.Fatalf("tiny: %+v %v", b, err)
	}
}

func TestBoardTrelloColours(t *testing.T) {
	all := boardLabelColorOK // the 30 label colours
	for in, want := range map[string]string{"green": "green", "green_light": "green_subtle", "red_dark": "red_bold", "sky_light": "sky_subtle", "": "black", "teal": "black", "Pink": "pink"} {
		if got := trelloColor(in, all); got != want {
			t.Errorf("with shades %q: %q, want %q", in, got, want)
		}
	}
	// A palette without the shades keeps their colour.
	only := func(c string) bool { return contains(boardColors, c) }
	for in, want := range map[string]string{"green_light": "green", "red_dark": "red", "lime": "lime"} {
		if got := trelloColor(in, only); got != want {
			t.Errorf("without shades %q: %q, want %q", in, got, want)
		}
	}
	if trelloBackground("grey") != "black" || trelloBackground("orange") != "orange" || trelloBackground("5f0c1a2b3c4d5e6f7a8b9c0d") != "blue" {
		t.Error("backgrounds")
	}
}

func TestBoardTrelloCaps(t *testing.T) {
	var cards, items []string
	for i := 0; i < trelloMaxCards+7; i++ {
		cards = append(cards, fmt.Sprintf(`{"id":"c%d","name":"Card %d","idList":"6b0000000000000000000001","pos":%d,"idShort":%d}`, i, i, i, i+1))
	}
	for i := 0; i < trelloMaxItems+5; i++ {
		items = append(items, fmt.Sprintf(`{"name":"Item %d","state":"incomplete","pos":%d}`, i, i))
	}
	raw := `{"id":"5f0c1a2b3c4d5e6f7a8b9c0d","name":"Big","labels":[],"lists":[{"id":"6b0000000000000000000001","name":"All","pos":1}],"cards":[` + strings.Join(cards, ",") +
		`],"checklists":[{"id":"cl","name":"Long","idCard":"c0","checkItems":[` + strings.Join(items, ",") + `]}]}`
	b, err := parseTrello([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Cards) != trelloMaxCards || len(b.Checklists["c0"]) != 1 || len(b.Checklists["c0"][0].CheckItems.V) != trelloMaxItems {
		t.Fatalf("caps: %d cards, %d items", len(b.Cards), len(b.Checklists["c0"][0].CheckItems.V))
	}
	notes := strings.Join(b.Notes, " ")
	if !strings.Contains(notes, "first 5,000 cards") || !strings.Contains(notes, "5 checklist items beyond 200") {
		t.Fatalf("notes: %s", notes)
	}
}

func TestBoardTrelloImport(t *testing.T) {
	o, ctx := setup(t)
	raw := trelloFixture(t)
	importer := "anita.rao"
	p, err := o.PreviewTrelloImport(ctx, T, importer, raw)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "Demo: settlement follow-ups" || p.Lists != 4 || p.ListsArchived != 1 || p.Cards != 5 || p.CardsArchived != 1 || p.Checklists != 2 || p.Items != 3 ||
		p.Labels != 4 || p.Fields != 5 || p.Comments != 2 || p.Attachments != 1 || p.Covers != 2 {
		t.Fatalf("preview counts %+v", p)
	}
	if p.ListPreview[0].Name != "To do" || p.ListPreview[3].Name != "Old ideas" || !p.ListPreview[3].Archived || p.ListPreview[0].Cards != 2 || p.ListPreview[2].Cards != 0 || p.ListPreview[2].ArchivedCards != 1 {
		t.Fatalf("lists in order %+v", p.ListPreview)
	}
	guess := map[string]TrelloMember{}
	for _, m := range p.Members {
		guess[m.Name] = m
	}
	if g := guess["Priya Nair"]; g.Guess != "priya.nair" || g.Why != "same name" || g.Type != "admin" {
		t.Fatalf("guess by name %+v", g)
	}
	if g := guess["F. Khan"]; g.Guess != "farhan.khan" || g.Why != "same username" {
		t.Fatalf("guess by username %+v", g)
	}
	if g := guess["Outside Consultant"]; g.Guess != "" || g.Type != "observer" {
		t.Fatalf("no guess %+v", g)
	}
	notes := strings.Join(p.Notes, " ")
	if !strings.Contains(notes, "not copied") || !strings.Contains(notes, "1 card in no list") || !strings.Contains(notes, "posted by you") || !strings.Contains(notes, "one notice") {
		t.Fatalf("preview notes %s", notes)
	}
	for _, u := range p.Users {
		if u.Role == RoleClient {
			t.Fatal("a client login offered for mapping")
		}
	}

	// Mapping someone to a login that is not active staff is refused.
	body := func(members map[string]string) TrelloImportInput {
		return TrelloImportInput{Name: "Settlement follow-ups", Members: members, Trello: raw}
	}
	if _, err := o.ImportTrello(ctx, T, importer, body(map[string]string{"6c0000000000000000000001": "nobody.here"})); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown user: %v", err)
	}
	res, err := o.ImportTrello(ctx, T, importer, body(map[string]string{"6c0000000000000000000001": "priya.nair", "6c0000000000000000000002": "farhan.khan", "6c0000000000000000000003": ""}))
	if err != nil {
		t.Fatal(err)
	}
	b := res.Board
	if b.Name != "Settlement follow-ups" || b.Visibility != "private" || b.Kind != BoardTeam || b.Background != "green" || res.Cards != 5 || res.Comments != 2 || res.Attachments != 1 || res.Members != 2 {
		t.Fatalf("result %+v", res)
	}
	roles := map[string]string{}
	for _, m := range b.Members {
		roles[m.User] = m.Role
	}
	if roles[importer] != BoardAdmin || roles["priya.nair"] != BoardAdmin || roles["farhan.khan"] != BoardMember || len(roles) != 3 {
		t.Fatalf("members %v", roles)
	}
	var names []string
	for _, l := range b.Lists {
		names = append(names, l.Name+map[bool]string{true: " (archived)", false: ""}[l.Archived])
	}
	if strings.Join(names, ", ") != "To do, Doing, Done, Old ideas (archived)" {
		t.Fatalf("lists %v", names)
	}
	lab := map[string]string{}
	for _, l := range b.Labels {
		lab[l.Name] = l.Color
	}
	if len(b.Labels) != 4 || lab["Client docs"] != "green" || lab["Blocked"] != "red_bold" || lab["Low priority"] != "sky_subtle" || lab["No colour"] != "black" {
		t.Fatalf("labels %v", lab)
	}
	fields := map[string]BoardField{}
	for _, f := range b.Fields {
		fields[f.Name] = f
	}
	if len(b.Fields) != 5 || b.Fields[0].Name != "Client code" || !fields["Client code"].OnFront || strings.Join(fields["Exchange"].Options, ",") != "NSE,BSE" || fields["Verified"].Type != "checkbox" {
		t.Fatalf("fields %+v", b.Fields)
	}

	v, err := o.GetBoard(ctx, T, "farhan.khan", b.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	arch, _ := o.GetBoard(ctx, T, importer, b.ID, true)
	by := map[int64]Card{}
	for _, c := range append(v.Cards, arch.Cards...) {
		by[c.No] = c
	}
	c1 := by[1]
	if c1.Title != "Collect bank proof" || c1.Description != "Ask for a **cancelled cheque**." || c1.Due != "2026-11-20T12:00" || c1.Start != "2026-11-18" || c1.DueDone {
		t.Fatalf("card 1 %+v", c1)
	}
	if len(c1.Labels) != 2 || strings.Join(c1.Members, ",") != "priya.nair" || c1.Cover == nil || c1.Cover.Color != "yellow" || c1.Cover.Full || c1.Cover.File != "" {
		t.Fatalf("card 1 labels %v members %v cover %+v", c1.Labels, c1.Members, c1.Cover)
	}
	if len(c1.Attachments) != 1 || !strings.HasPrefix(c1.Attachments[0].URL, "https://trello.com/") || c1.Attachments[0].File != "" || c1.Attachments[0].Name != "Scan.pdf" {
		t.Fatalf("attachments %+v", c1.Attachments)
	}
	fv := map[string]string{}
	for _, f := range b.Fields {
		fv[f.Name] = c1.Fields[f.ID]
	}
	if fv["Client code"] != "ZZ9001" || fv["Amount"] != "1250.50" || fv["Exchange"] != "BSE" || fv["Verified"] != "true" || fv["Filed on"] != "2026-11-10" {
		t.Fatalf("field values %v", fv)
	}
	if len(c1.Checklists) != 1 || len(c1.Checklists[0].Items) != 2 {
		t.Fatalf("checklist %+v", c1.Checklists)
	}
	it := c1.Checklists[0].Items
	if it[0].Text != "PAN copy" || !it[0].Done || it[1].Text != "Cancelled cheque" || it[1].Done || it[1].Member != "farhan.khan" || it[1].Due != "2026-11-19T18:00" {
		t.Fatalf("items %+v", it)
	}
	// Comments: oldest first, posted by the importer, saying who and when.
	if len(c1.Comments) != 2 || c1.Comments[0].By != importer || c1.Comments[1].By != importer || len(c1.Comments[0].Mentions) != 0 {
		t.Fatalf("comments %+v", c1.Comments)
	}
	if !strings.HasPrefix(c1.Comments[0].Text, "Imported from Trello: written by Priya Nair (@priyanair) on 11 Nov 2026 at 09:30 IST.\n\nFirst note") ||
		!strings.Contains(c1.Comments[1].Text, "written by F. Khan (@farhan_khan) on 12 Nov 2026 at 10:30 IST") || c1.Comments[0].At >= c1.Comments[1].At {
		t.Fatalf("comment text %q / %q", c1.Comments[0].Text, c1.Comments[1].Text)
	}
	if c2 := by[2]; !c2.DueDone || c2.Cover == nil || !c2.Cover.Full || len(c2.Checklists) != 1 || !c2.Checklists[0].Items[0].Done {
		t.Fatalf("card 2 %+v", c2)
	}
	if !by[3].Archived || by[4].List != b.Lists[3].ID || by[5].ID != "" {
		t.Fatalf("archived %v, card in the archived list %v, orphan %+v", by[3].Archived, by[4].List, by[5])
	}
	// Loosely typed: a number for a name, strings for booleans and positions.
	if c6 := by[6]; c6.Title != "12345" || c6.Archived || c6.List != b.Lists[0].ID || len(c6.Labels) != 0 || len(c6.Fields) != 0 || c6.Pos >= c1.Pos {
		t.Fatalf("card 6 %+v", c6)
	}
	// Nobody was notified; the import is in the activity.
	if ns, _, _ := o.BoardNotices(ctx, T, "priya.nair", 50); len(ns) != 1 || !strings.Contains(ns[0].Text, "imported the Trello board Settlement follow-ups and added you to it") || ns[0].Kind != NoticeAdded {
		t.Fatalf("notices %+v", ns)
	}
	acts, _ := o.BoardActivityList(ctx, T, importer, b.ID, "", 0, 0)
	if acts[0].What != "board.imported" || acts[0].By != importer || !strings.Contains(acts[0].Text, "imported this board from Trello") || !strings.Contains(acts[0].Text, "5 cards") {
		t.Fatalf("activity %+v", acts[0])
	}
	// The card numbers go on from Trello's.
	nc, err := o.AddCard(ctx, T, importer, b.ID, CardInput{List: b.Lists[0].ID, Title: "New after import"})
	if err != nil || nc.No != 7 {
		t.Fatalf("next card %d %v", nc.No, err)
	}
	// The board is private: someone not mapped cannot see it.
	if _, err := o.GetBoard(ctx, T, "vikram.shah", b.ID, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("private: %v", err)
	}
	// Branch staff can import too: their board is private to them.
	if r, err := o.ImportTrello(ctx, T, "meera.iyer", body(nil)); err != nil || r.Board.Visibility != "private" || len(r.Board.Members) != 1 {
		t.Fatalf("branch import %+v %v", r.Board.Members, err)
	}
	if _, err := o.ImportTrello(ctx, T, importer, TrelloImportInput{Trello: []byte(`{"not":"trello"}`)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("not trello: %v", err)
	}
}

func readCSV(t *testing.T, b []byte) [][]string {
	t.Helper()
	if !bytes.HasPrefix(b, []byte("\xef\xbb\xbf")) {
		t.Fatal("no byte order mark")
	}
	rows, err := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(b, []byte("\xef\xbb\xbf")))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestBoardExport(t *testing.T) {
	o, ctx := setup(t)
	admin, member, observer := "priya.nair", "farhan.khan", "anita.rao"
	b, _ := o.CreateBoard(ctx, T, admin, BoardInput{Name: "Export test"})
	b, _ = o.UpdateBoard(ctx, T, admin, b.ID, BoardInput{Members: []BoardPerson{{admin, BoardAdmin}, {member, BoardMember}}, Fields: []BoardField{{Name: "Client code", Type: "text"}, {Name: "Verified", Type: "checkbox"}}})
	if _, err := o.SetBoardEmail(ctx, T, admin, b.ID, BoardEmailInput{On: true}); err != nil {
		t.Fatal(err)
	}
	b, _, _ = docs.Get[Board](ctx, o.Docs, T, cBoard, b.ID)
	f0, f1 := b.Fields[0].ID, b.Fields[1].ID
	c1, _ := o.AddCard(ctx, T, admin, b.ID, CardInput{List: b.Lists[0].ID, Title: "=HYPERLINK(\"http://example.com\",\"Click\")", Description: "line one\nline two", Labels: []string{b.Labels[0].ID}, Members: []string{member}, Due: "2026-11-20", Fields: map[string]string{f0: "@SUM(A1)", f1: "true"}})
	c2, _ := o.AddCard(ctx, T, admin, b.ID, CardInput{List: b.Lists[1].ID, Title: "-5"})
	c3, _ := o.AddCard(ctx, T, admin, b.ID, CardInput{List: b.Lists[2].ID, Title: "+cmd|' /C calc'!A0"})
	o.EditChecklist(ctx, T, admin, b.ID, c1.ID, ChecklistOp{Op: "add_list", Name: "Docs"})
	c1, _, _ = o.GetCard(ctx, T, admin, b.ID, c1.ID)
	o.EditChecklist(ctx, T, admin, b.ID, c1.ID, ChecklistOp{Op: "add_item", Checklist: c1.Checklists[0].ID, Name: "PAN"})
	o.AddComment(ctx, T, member, b.ID, c1.ID, "Checked the PAN")
	archived := true
	o.UpdateCard(ctx, T, admin, b.ID, c3.ID, CardPatch{Archived: &archived})

	// CSV: board members, every card, archived too, no formulas.
	if _, _, err := o.ExportBoardCSV(ctx, T, observer, b.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("observer exported CSV: %v", err)
	}
	if _, _, err := o.ExportBoardCSV(ctx, T, "meera.iyer", b.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("branch exported a board it cannot see: %v", err)
	}
	raw, name, err := o.ExportBoardCSV(ctx, T, member, b.ID)
	if err != nil || name != "Export-test.csv" {
		t.Fatalf("csv %q %v", name, err)
	}
	if !bytes.Contains(raw, []byte("\r\n")) {
		t.Error("CSV lines end CRLF")
	}
	rows := readCSV(t, raw)
	if len(rows) != 4 {
		t.Fatalf("rows %d: %v", len(rows), rows)
	}
	head := rows[0]
	col := func(n string) int {
		for i, h := range head {
			if h == n {
				return i
			}
		}
		t.Fatalf("no column %q in %v", n, head)
		return -1
	}
	cells := map[string][]string{}
	for _, r := range rows[1:] {
		cells[r[col("Card")]] = r
	}
	r1, r2, r3 := cells[fmt.Sprintf("#%d", c1.No)], cells[fmt.Sprintf("#%d", c2.No)], cells[fmt.Sprintf("#%d", c3.No)]
	if r1[col("Title")] != "'=HYPERLINK(\"http://example.com\",\"Click\")" || r1[col("Client code")] != "'@SUM(A1)" || r1[col("Verified")] != "Yes" {
		t.Fatalf("formula not guarded: %q %q", r1[col("Title")], r1[col("Client code")])
	}
	if r2[col("Title")] != "-5" || r3[col("Title")] != "'+cmd|' /C calc'!A0" || r3[col("Archived")] != "Yes" || r1[col("Archived")] != "No" {
		t.Fatalf("numbers and archived: %q %q %q", r2[col("Title")], r3[col("Title")], r3[col("Archived")])
	}
	if r1[col("List")] != "To do" || r1[col("Members")] == "" || r1[col("Due")] != "2026-11-20" || r1[col("Checklist items")] != "1" || r1[col("Checklist items done")] != "0" ||
		r1[col("Description")] != "line one\nline two" || r1[col("Comments")] != "1" || r1[col("Labels")] == "" {
		t.Fatalf("row %v", r1)
	}
	if boardCSVCell("-12.5") != "-12.5" || boardCSVCell("\tx") != "'\tx" || boardCSVCell("plain") != "plain" {
		t.Error("cell guard")
	}

	// JSON: board admins only; everything, archived cards and activity too.
	if _, err := o.ExportBoardJSON(ctx, T, member, b.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("member exported JSON: %v", err)
	}
	ex, err := o.ExportBoardJSON(ctx, T, admin, b.ID)
	if err != nil || ex.Format != "ledgerline-board" || len(ex.Cards) != 3 || len(ex.Activity) < 5 || ex.Board.EmailKey != "" || len(ex.People) < 2 {
		t.Fatalf("json %d cards %d activity key %q %v", len(ex.Cards), len(ex.Activity), ex.Board.EmailKey, err)
	}
	js, _ := json.Marshal(ex)
	if !bytes.Contains(js, []byte("Checked the PAN")) || !bytes.Contains(js, []byte(`"archived":true`)) {
		t.Fatal("json lacks comments or the archived card")
	}
	// A card: anyone who can see the board.
	ce, err := o.ExportCardJSON(ctx, T, observer, b.ID, c1.ID)
	if err != nil || ce.Format != "ledgerline-card" || ce.Card.ID != c1.ID || ce.List != "To do" || len(ce.Labels) != 1 || len(ce.Fields) != 2 || len(ce.Activity) == 0 {
		t.Fatalf("card json %+v %v", ce, err)
	}
	if _, err := o.ExportCardJSON(ctx, T, "sunrise.ap", b.ID, c1.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("AP exported a card: %v", err)
	}
	if err := o.BoardPrinted(ctx, T, observer, b.ID, ""); err != nil {
		t.Fatal(err)
	}
	if err := o.BoardPrinted(ctx, T, observer, b.ID, c2.ID); err != nil {
		t.Fatal(err)
	}
	acts, _ := o.BoardActivityList(ctx, T, admin, b.ID, "", 0, 0)
	var what []string
	for _, a := range acts[:5] {
		what = append(what, a.What+": "+a.Text)
	}
	got := strings.Join(what, " | ")
	for _, w := range []string{"card.printed: printed #2", "board.printed: printed the board", "card.exported: exported #1 as JSON", "board.exported: exported the board as JSON (3 cards)", "board.exported: exported the board's cards as CSV (3 cards)"} {
		if !strings.Contains(got, w) {
			t.Errorf("activity lacks %q: %s", w, got)
		}
	}
}

func TestBoardRecentlyViewed(t *testing.T) {
	o, ctx := setup(t)
	me := "priya.nair"
	var ids []string
	for i := 0; i < 8; i++ {
		b, err := o.CreateBoard(ctx, T, me, BoardInput{Name: fmt.Sprintf("Board %d", i)})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, b.ID)
		if err := o.BoardViewed(ctx, T, me, b.ID); err != nil {
			t.Fatal(err)
		}
	}
	o.BoardViewed(ctx, T, me, ids[2]) // seen again: first
	rs, err := o.RecentBoards(ctx, T, me)
	if err != nil || len(rs) != 6 || rs[0].ID != ids[2] || rs[1].ID != ids[7] || rs[5].ID != ids[3] {
		t.Fatalf("recent %v %v", rs, err)
	}
	// Someone else's list is their own.
	if rs, _ := o.RecentBoards(ctx, T, "farhan.khan"); len(rs) != 0 {
		t.Fatalf("farhan's recent %v", rs)
	}
	// A board the person can no longer see is hidden, and so is a closed one.
	other, _ := o.CreateBoard(ctx, T, "farhan.khan", BoardInput{Name: "Farhan's private", Visibility: "private", Members: []BoardPerson{{me, BoardMember}}})
	if err := o.BoardViewed(ctx, T, me, other.ID); err != nil {
		t.Fatal(err)
	}
	if rs, _ := o.RecentBoards(ctx, T, me); rs[0].ID != other.ID {
		t.Fatalf("private board not first: %v", rs[0])
	}
	o.UpdateBoard(ctx, T, "farhan.khan", other.ID, BoardInput{Members: []BoardPerson{{"farhan.khan", BoardAdmin}}})
	o.SetBoardClosed(ctx, T, me, ids[2], true)
	rs, _ = o.RecentBoards(ctx, T, me)
	for _, r := range rs {
		if r.ID == other.ID || r.ID == ids[2] {
			t.Fatalf("hidden board shown: %v", r)
		}
	}
	if len(rs) != 6 || rs[0].ID != ids[7] {
		t.Fatalf("after hiding %v", rs)
	}
	if err := o.BoardViewed(ctx, T, me, other.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("viewed a board out of reach: %v", err)
	}
}

func TestBoardCollections(t *testing.T) {
	o, ctx := setup(t)
	maker, checker, branch := "priya.nair", "farhan.khan", "meera.iyer"
	firm, _ := o.CreateBoard(ctx, T, maker, BoardInput{Name: "Month-end close"})
	priv, _ := o.CreateBoard(ctx, T, maker, BoardInput{Name: "My private notes", Visibility: "private"})
	other, _ := o.CreateBoard(ctx, T, checker, BoardInput{Name: "Checker's board"})

	if _, err := o.SaveBoardCollection(ctx, T, branch, "", "Branch things"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("branch made a collection: %v", err)
	}
	c, err := o.SaveBoardCollection(ctx, T, maker, "", "  Accounts ")
	if err != nil || c.Name != "Accounts" || c.ID == "" || !c.CanDelete {
		t.Fatalf("create %+v %v", c, err)
	}
	if _, err := o.SaveBoardCollection(ctx, T, checker, "", "accounts"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("same name twice: %v", err)
	}
	if _, err := o.SaveBoardCollection(ctx, T, checker, "", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no name: %v", err)
	}
	// Board admins add their boards; a board can be in several collections.
	if _, err := o.SetBoardInCollection(ctx, T, checker, c.ID, firm.ID, true); !errors.Is(err, ErrForbidden) {
		t.Fatalf("an observer added the board: %v", err)
	}
	for _, id := range []string{firm.ID, priv.ID} {
		if _, err := o.SetBoardInCollection(ctx, T, maker, c.ID, id, true); err != nil {
			t.Fatal(err)
		}
	}
	c2, _ := o.SaveBoardCollection(ctx, T, checker, "", "Compliance")
	o.SetBoardInCollection(ctx, T, checker, c2.ID, other.ID, true)
	if v, err := o.SetBoardInCollection(ctx, T, maker, c2.ID, firm.ID, true); err != nil || len(v.Boards) != 2 {
		t.Fatalf("second collection %+v %v", v, err)
	}
	// Each person sees only the boards they can see; branch staff see none.
	cs, _ := o.BoardCollections(ctx, T, maker)
	if len(cs) != 2 || cs[0].Name != "Accounts" || len(cs[0].Boards) != 2 || !cs[0].CanDelete || cs[1].CanDelete {
		t.Fatalf("maker's collections %+v", cs)
	}
	cs, _ = o.BoardCollections(ctx, T, checker)
	if len(cs[0].Boards) != 1 || cs[0].Boards[0] != firm.ID {
		t.Fatalf("checker sees the private board: %+v", cs[0])
	}
	if cs, err := o.BoardCollections(ctx, T, branch); err != nil || len(cs) != 0 {
		t.Fatalf("branch sees collections: %+v %v", cs, err)
	}
	// Anyone at head office renames; only the maker or an Admin deletes.
	if v, err := o.SaveBoardCollection(ctx, T, checker, c.ID, "Accounts and books"); err != nil || v.Name != "Accounts and books" {
		t.Fatalf("rename %+v %v", v, err)
	}
	if err := o.DeleteBoardCollection(ctx, T, checker, c.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("checker deleted maker's collection: %v", err)
	}
	if v, err := o.SetBoardInCollection(ctx, T, maker, c.ID, priv.ID, false); err != nil || len(v.Boards) != 1 {
		t.Fatalf("take out %+v %v", v, err)
	}
	if err := o.DeleteBoardCollection(ctx, T, "admin", c.ID); err != nil {
		t.Fatal(err)
	}
	if cs, _ := o.BoardCollections(ctx, T, maker); len(cs) != 1 || cs[0].Name != "Compliance" {
		t.Fatalf("after delete %+v", cs)
	}
	if _, err := o.SetBoardInCollection(ctx, T, maker, c.ID, firm.ID, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("added to a deleted collection: %v", err)
	}
	acts, _ := o.BoardActivityList(ctx, T, maker, firm.ID, "", 0, 0)
	if !strings.Contains(acts[0].Text, "added this board to the collection Compliance") {
		t.Fatalf("activity %+v", acts[0])
	}
}
