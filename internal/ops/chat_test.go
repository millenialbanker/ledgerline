package ops

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func chatSetup(t *testing.T) (*Ops, context.Context) {
	t.Helper()
	o, ctx := setup(t)
	for _, u := range []User{
		{ID: "ravi.k", Name: "Ravi K", Role: RoleBranch, Branch: "Bengaluru", Active: true},
		{ID: "dev.delhi", Name: "Dev Malhotra", Role: RoleBranch, Branch: "Delhi", Active: true},
		{ID: "malabar.ap", Name: "Malabar Capital", Role: RoleAP, Partner: "AP002", Active: true},
	} {
		if err := o.PutUser(ctx, T, "admin", u); err != nil {
			t.Fatal(err)
		}
	}
	return o, ctx
}

func sees(t *testing.T, o *Ops, ctx context.Context, user, ch string) bool {
	t.Helper()
	_, err := o.ChatMessages(ctx, T, user, ch, 0)
	return err == nil
}

func listed(t *testing.T, o *Ops, ctx context.Context, user, ch string) bool {
	t.Helper()
	h, err := o.ChatHome(ctx, T, user)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range h.Conversations {
		if c.ID == ch {
			return true
		}
	}
	return false
}

func TestChatChannelsAndDMs(t *testing.T) {
	o, ctx := chatSetup(t)
	// Only the Admin creates channels, and only for one side.
	if _, err := o.SaveChatChannel(ctx, T, "priya.nair", "", ChatChannelInput{Name: "ops", Scope: "ho"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker created a channel: %v", err)
	}
	if _, err := o.SaveChatChannel(ctx, T, "admin", "", ChatChannelInput{Name: "ops", Scope: "ho", Members: []string{"priya.nair", "meera.iyer"}}); err == nil {
		t.Fatal("branch staff added to a head office channel")
	}
	c, err := o.SaveChatChannel(ctx, T, "admin", "", ChatChannelInput{Name: "Ops Team", Scope: "ho", Members: []string{"priya.nair", "farhan.khan"}})
	if err != nil || c.Name != "ops-team" {
		t.Fatalf("create: %v %+v", err, c)
	}
	if !sees(t, o, ctx, "priya.nair", c.ID) || !sees(t, o, ctx, "farhan.khan", c.ID) {
		t.Fatal("members cannot read")
	}
	for _, u := range []string{"anita.rao", "admin", "meera.iyer", "sunrise.ap"} {
		if sees(t, o, ctx, u, c.ID) {
			t.Errorf("%s reads a channel they are not in", u)
		}
	}
	h, _ := o.ChatHome(ctx, T, "admin")
	if h.Manage == nil || len(h.Manage.Channels) != 1 || h.Manage.Channels[0].LastText != "" {
		t.Fatal("admin manage list")
	}
	// Branch channel: that branch's staff only.
	if _, err := o.SaveChatChannel(ctx, T, "admin", "", ChatChannelInput{Name: "blr", Scope: "branch:Bengaluru", Members: []string{"meera.iyer", "dev.delhi"}}); err == nil {
		t.Fatal("Delhi staff added to a Bengaluru channel")
	}
	bc, err := o.SaveChatChannel(ctx, T, "admin", "", ChatChannelInput{Name: "blr", Scope: "branch:Bengaluru", Members: []string{"meera.iyer", "ravi.k"}})
	if err != nil {
		t.Fatal(err)
	}
	if !sees(t, o, ctx, "ravi.k", bc.ID) || sees(t, o, ctx, "dev.delhi", bc.ID) || sees(t, o, ctx, "priya.nair", bc.ID) {
		t.Fatal("branch channel visibility")
	}
	// Direct messages: within head office or within one branch.
	dm, err := o.OpenDM(ctx, T, "priya.nair", "anita.rao")
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := o.OpenDM(ctx, T, "anita.rao", "priya.nair"); again.ID != dm.ID {
		t.Error("the same pair got two conversations")
	}
	if _, err := o.PostChat(ctx, T, "priya.nair", dm.ID, "hello", nil); err != nil {
		t.Fatal(err)
	}
	if sees(t, o, ctx, "farhan.khan", dm.ID) || !listed(t, o, ctx, "anita.rao", dm.ID) {
		t.Fatal("direct message visibility")
	}
	for _, p := range [][2]string{{"priya.nair", "meera.iyer"}, {"meera.iyer", "dev.delhi"}, {"sunrise.ap", "malabar.ap"}, {"meera.iyer", "sunrise.ap"}} {
		if _, err := o.OpenDM(ctx, T, p[0], p[1]); err == nil {
			t.Errorf("%s could message %s directly", p[0], p[1])
		}
	}
	if _, err := o.OpenDM(ctx, T, "meera.iyer", "ravi.k"); err != nil {
		t.Errorf("same-branch DM: %v", err)
	}
	// Archived channels are read-only.
	if _, err := o.SaveChatChannel(ctx, T, "admin", c.ID, ChatChannelInput{Name: "ops-team", Scope: "ho", Members: []string{"priya.nair", "farhan.khan"}, Archived: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.PostChat(ctx, T, "priya.nair", c.ID, "still there?", nil); err == nil {
		t.Error("posted to an archived channel")
	}
	// Removing a member removes their access.
	if _, err := o.SaveChatChannel(ctx, T, "admin", bc.ID, ChatChannelInput{Name: "blr", Scope: "branch:Bengaluru", Members: []string{"meera.iyer"}}); err != nil {
		t.Fatal(err)
	}
	if sees(t, o, ctx, "ravi.k", bc.ID) {
		t.Error("removed member still reads")
	}
	if _, err := o.ChatHome(ctx, T, "c00001"); err == nil {
		// clients have no team messages (unknown user here, but the rule is the same)
		t.Error("non-staff opened messages")
	}
}

func TestChatDesks(t *testing.T) {
	o, ctx := chatSetup(t)
	h, err := o.ChatHome(ctx, T, "meera.iyer")
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Desks) != 3 || len(h.Parties) != 0 {
		t.Fatalf("branch sees desks %+v parties %d", h.Desks, len(h.Parties))
	}
	// A branch writes to the Risk desk; the party is always its own branch.
	th, err := o.OpenThread(ctx, T, "meera.iyer", "risk", "branch:Delhi")
	if err != nil || th.Scope != "branch:Bengaluru" {
		t.Fatalf("thread %v %+v", err, th)
	}
	if _, err := o.PostChat(ctx, T, "meera.iyer", th.ID, "Client BLR0007 asks for a limit increase", nil); err != nil {
		t.Fatal(err)
	}
	for u, want := range map[string]bool{"meera.iyer": true, "ravi.k": true, "anita.rao": true, "dev.delhi": false, "sunrise.ap": false, "vikram.shah": false, "priya.nair": false, "admin": false} {
		if got := sees(t, o, ctx, u, th.ID); got != want {
			t.Errorf("%s sees Bengaluru's Risk thread: %v, want %v", u, got, want)
		}
	}
	if !listed(t, o, ctx, "anita.rao", th.ID) {
		t.Error("risk desk does not see the new thread")
	}
	// The desk writes to an AP; only that AP sees it.
	rh, _ := o.ChatHome(ctx, T, "anita.rao")
	if len(rh.Parties) == 0 {
		t.Fatal("desk staff get no branches or APs to write to")
	}
	at, err := o.OpenThread(ctx, T, "anita.rao", "risk", "ap:AP001")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.PostChat(ctx, T, "anita.rao", at.ID, "@all please collect margin from your clients by 3 pm", nil); err != nil {
		t.Fatal(err)
	}
	if !sees(t, o, ctx, "sunrise.ap", at.ID) || sees(t, o, ctx, "malabar.ap", at.ID) || sees(t, o, ctx, "meera.iyer", at.ID) {
		t.Fatal("AP thread visibility")
	}
	// The AP writes back (vice versa) in the same thread.
	if at2, _ := o.OpenThread(ctx, T, "sunrise.ap", "risk", ""); at2.ID != at.ID {
		t.Fatal("AP opened a different thread")
	}
	if _, err := o.PostChat(ctx, T, "sunrise.ap", at.ID, "Done", nil); err != nil {
		t.Fatal(err)
	}
	// Head office staff not on a desk cannot write as it.
	if _, err := o.OpenThread(ctx, T, "vikram.shah", "risk", "branch:Delhi"); !errors.Is(err, ErrForbidden) {
		t.Errorf("compliance wrote as the risk desk: %v", err)
	}
	// The Admin staffs desks with head office people only, and can add desks.
	if _, err := o.SaveChatDesk(ctx, T, "admin", "", ChatDeskInput{Name: "Accounts desk", Members: []string{"meera.iyer"}}); err == nil {
		t.Error("branch staff put on a desk")
	}
	d, err := o.SaveChatDesk(ctx, T, "admin", "", ChatDeskInput{Name: "Accounts desk", Members: []string{"farhan.khan"}})
	if err != nil || d.ID != "accounts-desk" {
		t.Fatalf("desk %v %+v", err, d)
	}
	// A desk with nobody on it falls to the Admins.
	if _, err := o.SaveChatDesk(ctx, T, "admin", "compliance", ChatDeskInput{Name: "Compliance desk"}); err != nil {
		t.Fatal(err)
	}
	ct, _ := o.OpenThread(ctx, T, "dev.delhi", "compliance", "")
	if !sees(t, o, ctx, "admin", ct.ID) || sees(t, o, ctx, "vikram.shah", ct.ID) {
		t.Error("empty desk fallback")
	}
}

func TestChatMentionsFilesPulse(t *testing.T) {
	o, ctx := chatSetup(t)
	c, _ := o.SaveChatChannel(ctx, T, "admin", "", ChatChannelInput{Name: "ops", Scope: "ho", Members: []string{"priya.nair", "farhan.khan", "anita.rao"}})
	p0, _ := o.ChatPulse(ctx, T, "farhan.khan", "")
	m, err := o.PostChat(ctx, T, "priya.nair", c.ID, "@farhan.khan please check, cc @vikram.shah (not here) and email a@b.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Mentions) != 1 || m.Mentions[0] != "farhan.khan" || m.All {
		t.Fatalf("mentions %+v", m.Mentions)
	}
	p, _ := o.ChatPulse(ctx, T, "farhan.khan", p0.Now)
	if p.Unread != 1 || p.Mentions != 1 || len(p.Items) != 1 || !p.Items[0].Mention || p.Items[0].From != "Priya Nair" {
		t.Fatalf("pulse %+v", p)
	}
	if pa, _ := o.ChatPulse(ctx, T, "anita.rao", p0.Now); pa.Unread != 1 || pa.Mentions != 0 || pa.Items[0].Mention {
		t.Fatalf("anita pulse %+v", pa)
	}
	if pp, _ := o.ChatPulse(ctx, T, "priya.nair", p0.Now); pp.Unread != 0 || len(pp.Items) != 0 {
		t.Fatalf("sender notified of own message %+v", pp)
	}
	if _, err := o.PostChat(ctx, T, "anita.rao", c.ID, "@all EOD margin file is late", nil); err != nil {
		t.Fatal(err)
	}
	if h, _ := o.ChatHome(ctx, T, "priya.nair"); h.Mentions != 1 {
		t.Errorf("@all did not reach priya: %d", h.Mentions)
	}
	if err := o.MarkChatRead(ctx, T, "farhan.khan", c.ID, 0); err != nil {
		t.Fatal(err)
	}
	if p, _ := o.ChatPulse(ctx, T, "farhan.khan", ""); p.Unread != 0 || p.Mentions != 0 {
		t.Errorf("after reading %+v", p)
	}
	// @all is not a thing in a direct message.
	dm, _ := o.OpenDM(ctx, T, "priya.nair", "anita.rao")
	if m, _ := o.PostChat(ctx, T, "priya.nair", dm.ID, "@all hi", nil); m.All {
		t.Error("@all in a direct message")
	}
	// Files and photos.
	img, err := o.UploadChatFile(ctx, T, "priya.nair", c.ID, "screen.png", png1)
	if err != nil || !img.Image || img.Type != "image/png" {
		t.Fatalf("png: %v %+v", err, img)
	}
	pdf, err := o.UploadChatFile(ctx, T, "priya.nair", c.ID, "note.pdf", []byte("%PDF-1.4\n1 0 obj<<>>endobj\ntrailer<<>>\n%%EOF"))
	if err != nil || pdf.Image {
		t.Fatalf("pdf: %v", err)
	}
	for name, data := range map[string][]byte{"x.html": []byte("<script>alert(1)</script>"), "x.svg": []byte("<svg/>"), "fake.png": []byte("<html>"), "a.exe": []byte("MZ")} {
		if _, err := o.UploadChatFile(ctx, T, "priya.nair", c.ID, name, data); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := o.UploadChatFile(ctx, T, "meera.iyer", c.ID, "x.png", png1); err == nil {
		t.Error("non-member uploaded")
	}
	// Someone else cannot attach my upload, nor read it before it is sent.
	if _, err := o.PostChat(ctx, T, "farhan.khan", c.ID, "", []string{img.ID}); err == nil {
		t.Error("attached another person's upload")
	}
	if _, _, err := o.ChatFile(ctx, T, "farhan.khan", img.ID); err == nil {
		t.Error("read an unsent upload")
	}
	fm, err := o.PostChat(ctx, T, "priya.nair", c.ID, "", []string{img.ID, pdf.ID})
	if err != nil || len(fm.Files) != 2 {
		t.Fatal(err)
	}
	if _, err := o.PostChat(ctx, T, "priya.nair", c.ID, "again", []string{img.ID}); err == nil {
		t.Error("file attached twice")
	}
	if f, b, err := o.ChatFile(ctx, T, "farhan.khan", img.ID); err != nil || len(b) != len(png1) || f.Name != "screen.png" {
		t.Fatalf("member download: %v", err)
	}
	if _, _, err := o.ChatFile(ctx, T, "vikram.shah", img.ID); err == nil {
		t.Error("non-member downloaded")
	}
	ch, _ := o.chatChannel(ctx, T, c.ID)
	if !strings.HasPrefix(ch.LastText, "📎 screen.png and 1 more") {
		t.Errorf("preview %q", ch.LastText)
	}
	if _, err := o.PostChat(ctx, T, "priya.nair", c.ID, "  ", nil); err == nil {
		t.Error("empty message")
	}
	// Paging back through history.
	for i := 0; i < 260; i++ {
		if _, err := o.PostChat(ctx, T, "anita.rao", dm.ID, "line", nil); err != nil {
			t.Fatal(err)
		}
	}
	v, _ := o.ChatMessages(ctx, T, "priya.nair", dm.ID, 0)
	if len(v.Messages) != chatPage || !v.More || v.Messages[len(v.Messages)-1].Seq != 261 {
		t.Fatalf("latest page: %d more=%v last=%d", len(v.Messages), v.More, v.Messages[len(v.Messages)-1].Seq)
	}
	old, _ := o.ChatMessages(ctx, T, "priya.nair", dm.ID, v.Messages[0].Seq)
	if len(old.Messages) != chatPage || old.Messages[len(old.Messages)-1].Seq != v.Messages[0].Seq-1 {
		t.Fatalf("older page: %d", len(old.Messages))
	}
	first, _ := o.ChatMessages(ctx, T, "priya.nair", dm.ID, 3)
	if len(first.Messages) != 2 || first.More {
		t.Fatalf("first page: %d %v", len(first.Messages), first.More)
	}
}

func TestLedgyTellsWhoShouldAct(t *testing.T) {
	o, ctx := chatSetup(t)
	before := map[string]int64{}
	for _, u := range []string{"farhan.khan", "priya.nair", "admin"} {
		p, _ := o.ChatPulse(ctx, T, u, "")
		before[u] = p.Unread
	}
	since := o.now().Add(-time.Millisecond).Format(chatTime)
	r := &runCtx{o: o, ctx: ctx, tenant: T, date: D, process: "testproc", keys: map[string]bool{}}
	r.queue("k1", "Approve 3 payouts", "₹1,20,000 to three clients", "", RoleChecker, "money", "", nil)
	r.queue("k1", "Approve 3 payouts", "₹1,20,000 to three clients", "", RoleChecker, "money", "", nil) // the same item again
	p, _ := o.ChatPulse(ctx, T, "farhan.khan", since)
	got := 0
	for _, it := range p.Items {
		if it.Ledgy && strings.Contains(it.Text, "Approve 3 payouts") {
			got++
			if !it.Mention {
				t.Error("Ledgy notice not marked for the person")
			}
		}
	}
	if got != 1 {
		t.Fatalf("checker got %d notices for one item: %+v", got, p.Items)
	}
	if pm, _ := o.ChatPulse(ctx, T, "priya.nair", ""); pm.Unread != before["priya.nair"] {
		t.Error("maker told about a checker's item")
	}
	h, _ := o.ChatHome(ctx, T, "farhan.khan")
	var lc string
	for _, c := range h.Conversations {
		if c.Kind == ChatLedgy {
			lc = c.ID
		}
	}
	v, err := o.ChatMessages(ctx, T, "farhan.khan", lc, 0)
	if err != nil || v.CanPost {
		t.Fatalf("ledgy conversation: %v can_post=%v", err, v.CanPost)
	}
	last := v.Messages[len(v.Messages)-1]
	if last.From != LedgyActor || last.Link == nil || last.Link.View != "money" || !strings.Contains(last.Text, "₹1,20,000") {
		t.Fatalf("notice %+v", last)
	}
	if sees(t, o, ctx, "admin", lc) {
		t.Error("someone else reads another person's Ledgy notices")
	}
	// A role nobody holds goes to the Admins.
	r.queue("k2", "Unassigned thing", "", "", "Nobody", "", "", nil)
	if pa, _ := o.ChatPulse(ctx, T, "admin", ""); pa.Unread <= before["admin"] {
		t.Error("admin not told about an unowned item")
	}
}
