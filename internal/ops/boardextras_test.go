package ops

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"ledgerline/internal/docs"
)

func TestBoardTemplateData(t *testing.T) {
	ids := map[string]bool{}
	for _, tp := range boardTemplates {
		if !strings.HasPrefix(tp.ID, "bt-") || ids[tp.ID] || tp.Name == "" || tp.About == "" || !boardColorOK(tp.Color) || len(tp.Lists) == 0 {
			t.Fatalf("template %+v", tp.ID)
		}
		ids[tp.ID] = true
		labels := map[string]bool{}
		for _, l := range tp.Labels {
			if !boardColorOK(l.Color) || l.Color == "" {
				t.Errorf("%s: label %s colour %q", tp.ID, l.Name, l.Color)
			}
			labels[l.Name] = true
		}
		for _, f := range tp.Fields {
			if !contains([]string{"text", "number", "date", "checkbox", "list"}, f.Type) || (f.Type == "list") != (len(f.Options) > 0) {
				t.Errorf("%s: field %+v", tp.ID, f)
			}
		}
		for _, c := range tp.Cards {
			if c.List < 0 || c.List >= len(tp.Lists) || c.Title == "" {
				t.Errorf("%s: card %q list %d", tp.ID, c.Title, c.List)
			}
			for _, l := range c.Labels {
				if !labels[l] {
					t.Errorf("%s: card %q has the unknown label %q", tp.ID, c.Title, l)
				}
			}
			for _, cl := range c.Checklists {
				if cl.Name == "" || len(cl.Items) == 0 {
					t.Errorf("%s: card %q checklist %+v", tp.ID, c.Title, cl)
				}
			}
		}
	}
	if len(boardTemplates) < 7 {
		t.Fatalf("only %d templates", len(boardTemplates))
	}
}

func TestBoardTemplatesUse(t *testing.T) {
	o, ctx := setup(t)
	maker, checker := "priya.nair", "farhan.khan"
	g, err := o.BoardTemplates(ctx, T, maker)
	if err != nil || len(g) != len(boardTemplates) || !g[0].Builtin || g[0].Items == 0 {
		t.Fatalf("gallery %d %v", len(g), err)
	}

	// A Ledgerline template, with its starter cards.
	b, err := o.UseBoardTemplate(ctx, T, maker, "bt-month-end", BoardTemplateUse{Name: "Close: November 2026", Visibility: "private", Cards: true})
	if err != nil {
		t.Fatal(err)
	}
	if b.Name != "Close: November 2026" || b.Visibility != "private" || len(b.Lists) != 4 || len(b.Labels) != 6 || len(b.Fields) != 4 || b.Background != "blue" || b.Template {
		t.Fatalf("board from template %+v", b)
	}
	v, _ := o.GetBoard(ctx, T, maker, b.ID, false)
	if len(v.Cards) != 6 {
		t.Fatalf("starter cards %d", len(v.Cards))
	}
	for _, c := range v.Cards {
		if len(c.Checklists) == 0 || len(c.Labels) != 1 {
			t.Fatalf("starter card %+v", c)
		}
		if _, ok := boardLabel(b, c.Labels[0]); !ok {
			t.Fatalf("label %s not on the board", c.Labels[0])
		}
	}
	// Without the cards: the lists, labels and fields only.
	b2, err := o.UseBoardTemplate(ctx, T, maker, "bt-compliance", BoardTemplateUse{Cards: false})
	if err != nil || b2.Name != "Compliance calendar" || b2.Visibility != "firm" {
		t.Fatalf("%+v %v", b2, err)
	}
	if v2, _ := o.GetBoard(ctx, T, maker, b2.ID, false); len(v2.Cards) != 0 || len(v2.Board.Fields) != 4 {
		t.Fatalf("cards %d fields %d", len(v2.Cards), len(v2.Board.Fields))
	}
	if _, err := o.UseBoardTemplate(ctx, T, maker, "bt-nothing", BoardTemplateUse{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown template: %v", err)
	}
	if _, err := o.UseBoardTemplate(ctx, T, maker, b.ID, BoardTemplateUse{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a board that is not a template: %v", err)
	}

	// A card template on the board: a new card from it has its checklists, and is not a template.
	tc := v.Cards[0]
	yes := true
	if tc, err = o.UpdateCard(ctx, T, maker, b.ID, tc.ID, CardPatch{Template: &yes}); err != nil || !tc.Template {
		t.Fatalf("card template %v", err)
	}
	nc, err := o.AddCard(ctx, T, maker, b.ID, CardInput{List: b.Lists[1].ID, Title: "Interest: December", FromCard: tc.ID})
	if err != nil || nc.Template || nc.Title != "Interest: December" || len(nc.Checklists) != 1 || len(nc.Checklists[0].Items) != len(tc.Checklists[0].Items) {
		t.Fatalf("card from template %+v %v", nc, err)
	}

	// The firm's own template: an admin marks the board; others see it in the gallery once it is firm-wide.
	if _, err := o.UpdateBoard(ctx, T, maker, b.ID, BoardInput{Template: &yes}); err != nil {
		t.Fatal(err)
	}
	if g, _ := o.BoardTemplates(ctx, T, checker); len(g) != len(boardTemplates) {
		t.Fatalf("checker sees a private template: %d", len(g))
	}
	if _, err := o.UpdateBoard(ctx, T, maker, b.ID, BoardInput{Visibility: "firm"}); err != nil {
		t.Fatal(err)
	}
	g, _ = o.BoardTemplates(ctx, T, checker)
	last := g[len(g)-1]
	if len(g) != len(boardTemplates)+1 || last.ID != b.ID || last.Builtin || len(last.Cards) != 7 || last.Templates != 1 || last.CanEdit {
		t.Fatalf("firm template in the gallery %+v", last)
	}
	b3, err := o.UseBoardTemplate(ctx, T, checker, b.ID, BoardTemplateUse{Name: "Close: December 2026", Visibility: "private", Cards: true})
	if err != nil || b3.Visibility != "private" || b3.Template || len(b3.Fields) != 4 {
		t.Fatalf("from the firm's template %+v %v", b3, err)
	}
	v3, _ := o.GetBoard(ctx, T, checker, b3.ID, false)
	tpls := 0
	for _, c := range v3.Cards {
		if c.Template {
			tpls++
		}
	}
	if len(v3.Cards) != 7 || tpls != 1 || v3.Role != BoardAdmin {
		t.Fatalf("cards %d templates %d role %s", len(v3.Cards), tpls, v3.Role)
	}
	acts, _ := o.BoardActivityList(ctx, T, checker, b3.ID, "", 0, 0)
	found := false
	for _, a := range acts {
		found = found || a.What == "board.from_template"
	}
	if !found {
		t.Fatal("no activity for the template")
	}
}

func TestBoardMirrors(t *testing.T) {
	o, ctx := setup(t)
	maker, checker, risk := "priya.nair", "farhan.khan", "anita.rao"
	src, _ := o.CreateBoard(ctx, T, maker, BoardInput{Name: "Compliance desk"})
	dst, _ := o.CreateBoard(ctx, T, maker, BoardInput{Name: "Operations desk"})
	dst, err := o.UpdateBoard(ctx, T, maker, dst.ID, BoardInput{Members: []BoardPerson{{maker, BoardAdmin}, {checker, BoardMember}}})
	if err != nil {
		t.Fatal(err)
	}
	c, _ := o.AddCard(ctx, T, maker, src.ID, CardInput{List: src.Lists[0].ID, Title: "File the net worth certificate", Description: "CA has the accounts", Due: "2026-11-30"})

	// Only to boards the person can add cards to.
	if _, err := o.MirrorCard(ctx, T, checker, src.ID, c.ID, CardMove{Board: src.ID, List: src.Lists[0].ID}); err == nil {
		t.Fatal("mirrored onto its own board")
	}
	other, _ := o.CreateBoard(ctx, T, maker, BoardInput{Name: "Risk desk"}) // checker is an observer there
	if _, err := o.MirrorCard(ctx, T, checker, src.ID, c.ID, CardMove{Board: other.ID, List: other.Lists[0].ID}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("mirrored to a board the checker only observes: %v", err)
	}
	ts, _ := o.MirrorTargets(ctx, T, checker, src.ID)
	if len(ts) != 1 || ts[0].ID != dst.ID || len(ts[0].Lists) != 3 {
		t.Fatalf("targets %+v", ts)
	}

	m, err := o.MirrorCard(ctx, T, checker, src.ID, c.ID, CardMove{Board: dst.ID, List: dst.Lists[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	if m.Mirror == nil || m.Mirror.Card != c.ID || m.Title != c.Title || m.Due != c.Due || m.Description != c.Description {
		t.Fatalf("mirror %+v", m)
	}
	if _, err := o.MirrorCard(ctx, T, maker, src.ID, c.ID, CardMove{Board: dst.ID, List: dst.Lists[0].ID}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a second mirror on the same board: %v", err)
	}

	// The original changes; reading the mirror's board brings the mirror up to date.
	title, due, done := "File the half-yearly net worth certificate", "2026-11-29", true
	if _, err := o.UpdateCard(ctx, T, maker, src.ID, c.ID, CardPatch{Title: &title, Due: &due, DueDone: &done}); err != nil {
		t.Fatal(err)
	}
	mirrorOf := func(who string) Card {
		t.Helper()
		v, err := o.GetBoard(ctx, T, who, dst.ID, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, x := range v.Cards {
			if x.ID == m.ID {
				return x
			}
		}
		t.Fatalf("no mirror on the board")
		return Card{}
	}
	if x := mirrorOf(checker); x.Title != title || x.Due != due || !x.DueDone {
		t.Fatalf("mirror not refreshed %+v", x)
	}

	// Changes through the mirror go to the original, with the person's rights there.
	info, err := o.MirrorSource(ctx, T, checker, dst.ID, m.ID)
	if err != nil || !info.Visible || info.BoardName != "Compliance desk" || info.CanEdit || !strings.Contains(info.Why, "only view") {
		t.Fatalf("checker's view of the original %+v %v", info, err)
	}
	nt := "Net worth certificate: half year to September"
	if _, err := o.EditMirrorSource(ctx, T, checker, dst.ID, m.ID, MirrorEdit{Title: &nt}); !errors.Is(err, ErrForbidden) || !strings.Contains(err.Error(), "Compliance desk") {
		t.Fatalf("an observer of the original changed it: %v", err)
	}
	if info, err = o.EditMirrorSource(ctx, T, maker, dst.ID, m.ID, MirrorEdit{Title: &nt}); err != nil || info.Title != nt || !info.CanEdit {
		t.Fatalf("maker's change %+v %v", info, err)
	}
	if x := mirrorOf(maker); x.Title != nt {
		t.Fatalf("mirror after the change %+v", x)
	}
	// A change made on the mirror card itself: forwarded for someone who may change the original ...
	t2 := "Net worth certificate (CA signed)"
	if _, err := o.UpdateCard(ctx, T, maker, dst.ID, m.ID, CardPatch{Title: &t2}); err != nil {
		t.Fatal(err)
	}
	if x := mirrorOf(maker); x.Title != t2 {
		t.Fatalf("forwarded change %+v", x)
	}
	if s, _, _ := o.GetCard(ctx, T, maker, src.ID, c.ID); s.Title != t2 {
		t.Fatalf("original not changed: %q", s.Title)
	}
	// ... undone, with a notice, for someone who may not.
	t3 := "Changed by the checker"
	if _, err := o.UpdateCard(ctx, T, checker, dst.ID, m.ID, CardPatch{Title: &t3}); err != nil {
		t.Fatal(err)
	}
	if x := mirrorOf(maker); x.Title != t2 {
		t.Fatalf("checker's change was kept: %+v", x)
	}
	if ns, _, _ := o.BoardNotices(ctx, T, checker, 0); len(ns) == 0 || !strings.Contains(ns[0].Text, "was undone") {
		t.Fatalf("checker's notices %+v", ns)
	}
	if s, _, _ := o.GetCard(ctx, T, maker, src.ID, c.ID); s.Title != t2 {
		t.Fatalf("original changed by the checker: %q", s.Title)
	}

	// A private original: the mirror shows nothing of it on a firm-wide board.
	priv, _ := o.CreateBoard(ctx, T, maker, BoardInput{Name: "Director's notes", Visibility: "private"})
	pc, _ := o.AddCard(ctx, T, maker, priv.ID, CardInput{List: priv.Lists[0].ID, Title: "Penalty from the inspection", Description: "Do not share", Due: "2026-12-05"})
	pm, err := o.MirrorCard(ctx, T, maker, priv.ID, pc.ID, CardMove{Board: dst.ID, List: dst.Lists[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	if pm.Title != mirrorHiddenTitle || pm.Description != "" || pm.Due != "" {
		t.Fatalf("hidden mirror shows %+v", pm)
	}
	v, _ := o.GetBoard(ctx, T, risk, dst.ID, false)
	for _, x := range v.Cards {
		if strings.Contains(x.Title, "Penalty") || strings.Contains(x.Description, "share") {
			t.Fatalf("risk sees the private card %+v", x)
		}
	}
	acts, _ := o.BoardActivityList(ctx, T, risk, dst.ID, "", 0, 0)
	for _, a := range acts {
		if strings.Contains(a.Text, "Penalty") {
			t.Fatalf("activity shows the private card: %s", a.Text)
		}
	}
	if hi, _ := o.MirrorSource(ctx, T, risk, dst.ID, pm.ID); hi.Visible || hi.Title != "" || hi.BoardName != "" || !hi.Hidden {
		t.Fatalf("risk's view of a private original %+v", hi)
	}
	if hi, _ := o.MirrorSource(ctx, T, maker, dst.ID, pm.ID); !hi.Visible || hi.Title != pc.Title {
		t.Fatalf("maker's view of the private original %+v", hi)
	}
	if acts, _ := o.BoardActivityList(ctx, T, maker, priv.ID, "", 0, 0); !strings.Contains(acts[0].Text, "to the board Operations desk") {
		t.Fatalf("source activity %s", acts[0].Text)
	}
	// A firm-wide card mirrored to a private board: its board does not learn the private board's name.
	if _, err := o.MirrorCard(ctx, T, maker, src.ID, c.ID, CardMove{Board: priv.ID, List: priv.Lists[1].ID}); err != nil {
		t.Fatal(err)
	}
	if acts, _ := o.BoardActivityList(ctx, T, checker, src.ID, "", 0, 0); !strings.Contains(acts[0].Text, "to another board") {
		t.Fatalf("source activity %s", acts[0].Text)
	}

	// The original moves to another board: the mirror follows it.
	if _, err := o.MoveCard(ctx, T, maker, src.ID, c.ID, CardMove{Board: other.ID, List: other.Lists[0].ID}); err != nil {
		t.Fatal(err)
	}
	if x := mirrorOf(maker); x.Mirror.Board != other.ID || x.Title != t2 {
		t.Fatalf("mirror after the move %+v", x.Mirror)
	}
	moved := mirrorOf(maker).Mirror.Card

	// Archiving the original archives its mirrors.
	yes := true
	if _, err := o.UpdateCard(ctx, T, maker, other.ID, moved, CardPatch{Archived: &yes}); err != nil {
		t.Fatal(err)
	}
	if mc, _, _ := o.GetCard(ctx, T, maker, dst.ID, m.ID); !mc.Archived {
		t.Fatal("mirror not archived with its original")
	}
	// A mirror of a deleted original is archived when its board is read.
	if _, err := o.UpdateCard(ctx, T, maker, priv.ID, pc.ID, CardPatch{Archived: &yes}); err != nil {
		t.Fatal(err)
	}
	if v, _ := o.GetBoard(ctx, T, maker, dst.ID, false); len(v.Cards) != 0 {
		t.Fatalf("open cards left %d", len(v.Cards))
	}
}

const sampleMail = "From: =?UTF-8?Q?Ravi_K=C3=BCmar?= <Ravi.Kumar@example.com>\r\n" +
	"To: board@ledgerline.example\r\n" +
	"Subject: =?UTF-8?B?S1lDIGRvY3VtZW50cyDigJMgQkgxMDA0?=\r\n" +
	"Message-ID: <abc123@mail.example.com>\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/mixed; boundary=\"outer\"\r\n" +
	"\r\n" +
	"--outer\r\n" +
	"Content-Type: multipart/alternative; boundary=\"inner\"\r\n" +
	"\r\n" +
	"--inner\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"Content-Transfer-Encoding: quoted-printable\r\n" +
	"\r\n" +
	"Please find the PAN and bank proof attached. Caf=C3=A9 address is the new o=\r\n" +
	"ne.\r\n" +
	"--inner\r\n" +
	"Content-Type: text/html; charset=utf-8\r\n" +
	"\r\n" +
	"<p>Please find the <b>PAN</b> attached.</p>\r\n" +
	"--inner--\r\n" +
	"--outer\r\n" +
	"Content-Type: image/png; name=\"pan.png\"\r\n" +
	"Content-Disposition: attachment; filename=\"pan.png\"\r\n" +
	"Content-Transfer-Encoding: base64\r\n" +
	"\r\n" +
	"%PNG%\r\n" +
	"--outer\r\n" +
	"Content-Type: application/octet-stream; name=\"tool.exe\"\r\n" +
	"Content-Disposition: attachment; filename=\"tool.exe\"\r\n" +
	"Content-Transfer-Encoding: base64\r\n" +
	"\r\n" +
	"TVqQAAMAAAAEAAAA\r\n" +
	"--outer--\r\n"

var testPNG = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x02\x00\x00\x00\x90wS\xde\x00\x00\x00\x0cIDATx\x9cc\xf8\x0f\x00\x00\x01\x01\x00\x05\x18\xd8N\x00\x00\x00\x00IEND\xaeB`\x82")

// sampleBoardMail is a sample inbound email with a PNG and a refused .exe.
func sampleBoardMail() []byte {
	b64 := base64.StdEncoding.EncodeToString(testPNG)
	return []byte(strings.Replace(sampleMail, "%PNG%", b64[:40]+"\r\n"+b64[40:], 1))
}

func TestBoardEmail(t *testing.T) {
	o, ctx := setup(t)
	maker, checker := "priya.nair", "farhan.khan"
	b, _ := o.CreateBoard(ctx, T, maker, BoardInput{Name: "Onboarding inbox"})
	b, _ = o.UpdateBoard(ctx, T, maker, b.ID, BoardInput{Members: []BoardPerson{{maker, BoardAdmin}, {checker, BoardMember}}})
	if _, err := o.SetBoardEmail(ctx, T, checker, b.ID, BoardEmailInput{On: true}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a member switched on email: %v", err)
	}
	in, err := o.SetBoardEmail(ctx, T, maker, b.ID, BoardEmailInput{On: true, List: b.Lists[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	key := strings.TrimPrefix(in.Path, "/inbound/boards/"+T+"/")
	if !in.On || len(key) != 64 || in.List != b.Lists[1].ID || len(in.Lists) != 3 {
		t.Fatalf("email settings %+v", in)
	}
	if v, _ := o.GetBoard(ctx, T, checker, b.ID, false); v.Board.EmailKey != "" {
		t.Fatal("a member sees the board's address")
	}
	if !o.BoardEmailKnown(ctx, T, key) || o.BoardEmailKnown(ctx, T, strings.Repeat("0", 64)) || o.BoardEmailKnown(ctx, "beta", key) {
		t.Fatal("key lookup")
	}

	c, dup, err := o.BoardInboundMail(ctx, T, key, sampleBoardMail())
	if err != nil || dup {
		t.Fatalf("%v %v", err, dup)
	}
	if c.Title != "KYC documents – BH1004" || c.List != b.Lists[1].ID {
		t.Fatalf("title %q list %s", c.Title, c.List)
	}
	lines := strings.Split(c.Description, "\n")
	if lines[0] != "From: Ravi Kümar <ravi.kumar@example.com>" || !strings.Contains(c.Description, "Café address is the new one.") || strings.Contains(c.Description, "<b>") {
		t.Fatalf("description %q", c.Description)
	}
	if len(c.Attachments) != 1 || c.Attachments[0].Name != "pan.png" || !c.Attachments[0].Image || !strings.Contains(c.Description, "Not attached: tool.exe") {
		t.Fatalf("attachments %+v / %q", c.Attachments, c.Description)
	}
	if _, _, _, data, err := o.BoardFile(ctx, T, checker, c.Attachments[0].File); err != nil || string(data) != string(testPNG) {
		t.Fatalf("attachment content %v", err)
	}
	// The provider sends it again: no second card.
	c2, dup, err := o.BoardInboundMail(ctx, T, key, sampleBoardMail())
	if err != nil || !dup || c2.ID != c.ID {
		t.Fatalf("redelivery %v %v %s", err, dup, c2.ID)
	}
	acts, _ := o.BoardActivityList(ctx, T, checker, b.ID, "", 0, 0)
	if acts[0].What != "email.received" || !strings.Contains(acts[0].Text, "ravi.kumar@example.com") {
		t.Fatalf("activity %+v", acts[0])
	}

	// An HTML-only email, without a subject.
	htmlOnly := "From: ops@example.com\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<html><head><style>p{color:red}</style></head><body><p>Client&nbsp;BH1007 wants&amp;needs a call</p><ul><li>PAN</li><li>Bank</li></ul><script>alert(1)</script></body></html>"
	c3, _, err := o.BoardInboundMail(ctx, T, key, []byte(htmlOnly))
	if err != nil {
		t.Fatal(err)
	}
	if c3.Title != "Email from ops@example.com" || !strings.Contains(c3.Description, "Client BH1007 wants&needs a call") || !strings.Contains(c3.Description, "- PAN") ||
		strings.Contains(c3.Description, "color") || strings.Contains(c3.Description, "alert") || strings.Contains(c3.Description, "<") {
		t.Fatalf("html email %q / %q", c3.Title, c3.Description)
	}
	if _, _, err := o.BoardInboundMail(ctx, T, key, []byte("not an email")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("junk: %v", err)
	}

	// A new address: the old one stops working.
	in2, err := o.SetBoardEmail(ctx, T, maker, b.ID, BoardEmailInput{On: true, Rotate: true})
	if err != nil || in2.Path == in.Path || o.BoardEmailKnown(ctx, T, key) {
		t.Fatalf("rotate %+v %v", in2, err)
	}
	key = strings.TrimPrefix(in2.Path, "/inbound/boards/"+T+"/")
	// A closed board takes no mail.
	if _, err := o.SetBoardClosed(ctx, T, maker, b.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := o.BoardInboundMail(ctx, T, key, []byte(htmlOnly)); !errors.Is(err, ErrState) {
		t.Fatalf("mail to a closed board: %v", err)
	}
	if _, err := o.SetBoardClosed(ctx, T, maker, b.ID, false); err != nil {
		t.Fatal(err)
	}
	if in3, err := o.SetBoardEmail(ctx, T, maker, b.ID, BoardEmailInput{On: false}); err != nil || in3.On || in3.Path != "" || o.BoardEmailKnown(ctx, T, key) {
		t.Fatalf("off %+v %v", in3, err)
	}
}

func TestCardStickers(t *testing.T) {
	o, ctx := setup(t)
	maker, observer := "priya.nair", "farhan.khan"
	b, _ := o.CreateBoard(ctx, T, maker, BoardInput{Name: "Stickers"})
	c, _ := o.AddCard(ctx, T, maker, b.ID, CardInput{List: b.Lists[0].ID, Title: "Sticky"})
	if _, err := o.CardSticker(ctx, T, observer, b.ID, c.ID, StickerOp{Add: "star"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("an observer added a sticker: %v", err)
	}
	if _, err := o.CardSticker(ctx, T, maker, b.ID, c.ID, StickerOp{Add: "skull"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown sticker: %v", err)
	}
	if _, err := o.CardSticker(ctx, T, maker, b.ID, c.ID, StickerOp{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nothing to do: %v", err)
	}
	for i := 0; i < MaxCardStickers; i++ {
		var err error
		if c, err = o.CardSticker(ctx, T, maker, b.ID, c.ID, StickerOp{Add: BoardStickers[i]}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := o.CardSticker(ctx, T, maker, b.ID, c.ID, StickerOp{Add: "tick"}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "at most 10") {
		t.Fatalf("an eleventh sticker: %v", err)
	}
	one, bad := 1, 10
	if _, err := o.CardSticker(ctx, T, maker, b.ID, c.ID, StickerOp{Remove: &bad}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("remove out of range: %v", err)
	}
	c, err := o.CardSticker(ctx, T, maker, b.ID, c.ID, StickerOp{Remove: &one})
	if err != nil || len(c.Stickers) != 9 || c.Stickers[0] != "tick" || c.Stickers[1] != "flag" {
		t.Fatalf("removed %v %v", c.Stickers, err)
	}
	if c, err = o.CardSticker(ctx, T, maker, b.ID, c.ID, StickerOp{Add: "tick"}); err != nil || len(c.Stickers) != 10 || c.Stickers[9] != "tick" {
		t.Fatalf("the same sticker twice %v %v", c.Stickers, err)
	}
	if len(BoardStickers) != 24 {
		t.Fatalf("%d stickers", len(BoardStickers))
	}
	acts, _ := o.BoardActivityList(ctx, T, maker, b.ID, c.ID, 0, 1)
	if acts[0].What != "card.sticker" {
		t.Fatalf("activity %+v", acts[0])
	}
	o.SetBoardClosed(ctx, T, maker, b.ID, true)
	if _, err := o.CardSticker(ctx, T, maker, b.ID, c.ID, StickerOp{Remove: &one}); err == nil {
		t.Fatal("a sticker changed on a closed board")
	}
}

func cardMailKeyOf(t *testing.T, in CardEmailInfo) string {
	t.Helper()
	if !in.On || !strings.HasPrefix(in.Path, "/inbound/boards/"+T+"/c") {
		t.Fatalf("card address %+v", in)
	}
	return strings.TrimPrefix(in.Path, "/inbound/boards/"+T+"/")
}

func TestCardEmail(t *testing.T) {
	o, ctx := setup(t)
	maker, checker, observer := "priya.nair", "farhan.khan", "anita.rao"
	b, _ := o.CreateBoard(ctx, T, maker, BoardInput{Name: "Client follow-ups"})
	b, _ = o.UpdateBoard(ctx, T, maker, b.ID, BoardInput{Members: []BoardPerson{{maker, BoardAdmin}, {checker, BoardMember}}})
	c, _ := o.AddCard(ctx, T, maker, b.ID, CardInput{List: b.Lists[0].ID, Title: "Bank proof for BH1004"})
	other, _ := o.AddCard(ctx, T, maker, b.ID, CardInput{List: b.Lists[0].ID, Title: "Another card"})

	// Off until the board's email-to-board is on.
	if in, err := o.CardEmail(ctx, T, checker, b.ID, c.ID); err != nil || in.On || in.Path != "" || !strings.Contains(in.Why, "Email-to-board is off") {
		t.Fatalf("off %+v %v", in, err)
	}
	if _, err := o.SetBoardEmail(ctx, T, maker, b.ID, BoardEmailInput{On: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.CardEmail(ctx, T, observer, b.ID, c.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("an observer saw the card's address: %v", err)
	}
	in, err := o.CardEmail(ctx, T, checker, b.ID, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	key := cardMailKeyOf(t, in)
	board, _ := o.BoardEmail(ctx, T, maker, b.ID)
	bkey := strings.TrimPrefix(board.Path, "/inbound/boards/"+T+"/")
	if strings.Contains(key, bkey[:16]) {
		t.Fatal("the card's address shows the board's")
	}
	oin, _ := o.CardEmail(ctx, T, checker, b.ID, other.ID)
	okey := cardMailKeyOf(t, oin)
	if okey == key || !o.BoardEmailKnown(ctx, T, key) || !o.BoardEmailKnown(ctx, T, okey) {
		t.Fatal("each card has its own known address")
	}
	// A forged key: the right card and board, the wrong signature.
	forged := key[:len(key)-32] + strings.Repeat("0", 32)
	if o.BoardEmailKnown(ctx, T, forged) || o.BoardEmailKnown(ctx, "beta", key) {
		t.Fatal("forged or other firm's key known")
	}
	if _, _, err := o.BoardInboundMail(ctx, T, forged, sampleBoardMail()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("forged key took mail: %v", err)
	}

	// Mail to the card: a comment with the sender and the text, and the
	// files that pass an upload's checks.
	got, dup, err := o.BoardInboundMail(ctx, T, key, sampleBoardMail())
	if err != nil || dup || got.ID != c.ID {
		t.Fatalf("mail %v %v %s", err, dup, got.ID)
	}
	if len(got.Comments) != 1 || got.Comments[0].By != mailActor {
		t.Fatalf("comments %+v", got.Comments)
	}
	text := got.Comments[0].Text
	if !strings.HasPrefix(text, "Email from Ravi Kümar (ravi.kumar@example.com): KYC documents – BH1004\n\n") || !strings.Contains(text, "Café address is the new one.") ||
		!strings.Contains(text, "Attached: pan.png") || !strings.Contains(text, "Not attached: tool.exe") {
		t.Fatalf("comment %q", text)
	}
	if len(got.Attachments) != 1 || got.Attachments[0].Name != "pan.png" || got.Attachments[0].By != mailActor {
		t.Fatalf("attachments %+v", got.Attachments)
	}
	if v, _ := o.GetBoard(ctx, T, maker, b.ID, false); len(v.Cards) != 2 {
		t.Fatalf("mail to a card made a card: %d", len(v.Cards))
	}
	// Sent again: no second comment. The same message to the other card is its own.
	if again, dup, err := o.BoardInboundMail(ctx, T, key, sampleBoardMail()); err != nil || !dup || len(again.Comments) != 1 {
		t.Fatalf("redelivery %v %v %d", err, dup, len(again.Comments))
	}
	if oc, dup, err := o.BoardInboundMail(ctx, T, okey, sampleBoardMail()); err != nil || dup || len(oc.Comments) != 1 || oc.ID != other.ID {
		t.Fatalf("other card %v %v", err, dup)
	}
	acts, _ := o.BoardActivityList(ctx, T, maker, b.ID, c.ID, 0, 0)
	found := false
	for _, a := range acts {
		found = found || (a.What == "email.received" && strings.Contains(a.Text, "on #1") && a.Data["card_mail"] == "1")
	}
	if !found {
		t.Fatalf("activity %+v", acts)
	}

	// An archived card takes no mail.
	arch := true
	o.UpdateCard(ctx, T, maker, b.ID, other.ID, CardPatch{Archived: &arch})
	if _, _, err := o.BoardInboundMail(ctx, T, okey, []byte("From: a@example.com\r\nSubject: hi\r\n\r\ntext")); !errors.Is(err, ErrState) {
		t.Fatalf("archived card took mail: %v", err)
	}
	if in, _ := o.CardEmail(ctx, T, checker, b.ID, other.ID); in.On || !strings.Contains(in.Why, "archived") {
		t.Fatalf("archived card's address %+v", in)
	}
	// A closed board takes no mail.
	o.SetBoardClosed(ctx, T, maker, b.ID, true)
	if _, _, err := o.BoardInboundMail(ctx, T, key, []byte("From: a@example.com\r\nSubject: hi\r\n\r\ntext")); !errors.Is(err, ErrState) {
		t.Fatalf("closed board took mail: %v", err)
	}
	o.SetBoardClosed(ctx, T, maker, b.ID, false)
	// A new board address changes every card's.
	if _, err := o.SetBoardEmail(ctx, T, maker, b.ID, BoardEmailInput{On: true, Rotate: true}); err != nil {
		t.Fatal(err)
	}
	if o.BoardEmailKnown(ctx, T, key) {
		t.Fatal("the old card address still works")
	}
	if in2, _ := o.CardEmail(ctx, T, checker, b.ID, c.ID); in2.Path == in.Path {
		t.Fatal("the card kept its address")
	}
	// Switched off: no card address.
	o.SetBoardEmail(ctx, T, maker, b.ID, BoardEmailInput{On: false})
	if in3, _ := o.CardEmail(ctx, T, checker, b.ID, c.ID); in3.On {
		t.Fatal("card address while the board's email is off")
	}

	// A Ledgerline board's cards take no mail, even with a key.
	lb, _ := o.CreateBoard(ctx, T, "admin", BoardInput{Name: "Work queue (test)"})
	lc, _ := o.AddCard(ctx, T, "admin", lb.ID, CardInput{List: lb.Lists[0].ID, Title: "Linked record"})
	lb, _ = docs.Update(ctx, o.Docs, T, cBoard, lb.ID, "admin", func(x *Board) error { x.Kind, x.EmailKey = BoardLedgerline, boardID("", 32); return nil })
	if in, err := o.CardEmail(ctx, T, "admin", lb.ID, lc.ID); err != nil || in.On {
		t.Fatalf("ledgerline card address %+v %v", in, err)
	}
	if _, _, err := o.BoardInboundMail(ctx, T, cardMailKey(lb, lc), sampleBoardMail()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("ledgerline board took card mail: %v", err)
	}
}
