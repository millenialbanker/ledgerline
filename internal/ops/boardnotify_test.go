package ops

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/docs"
)

type failMailer struct{ n int }

func (f *failMailer) Send(_ context.Context, m Mail) (string, error) {
	f.n++
	return "", errors.New("connection refused")
}

// bnSetup is the Operations board (baSetup) with a mail server, a clock
// that can move, and the checker's address confirmed.
func bnSetup(t *testing.T, now string) (*Ops, context.Context, Board, *fakeMailer, func(string)) {
	t.Helper()
	o, ctx, b := baSetup(t, now)
	clock := baAt(now)
	o.Now = func() time.Time { return clock }
	set := func(s string) { clock = baAt(s) }
	m := &fakeMailer{}
	o.Mailer = m
	bnConfirm(t, o, ctx, m, "farhan.khan", "farhan@bhairav.example")
	m.got = nil
	return o, ctx, b, m, set
}

func bnConfirm(t *testing.T, o *Ops, ctx context.Context, m *fakeMailer, who, email string) {
	t.Helper()
	v, err := o.SetBoardMailAddress(ctx, T, who, email)
	if err != nil || v.Pending != email || v.Email != "" || v.Sent != "Sent" {
		t.Fatalf("address: %+v %v", v, err)
	}
	last := m.got[len(m.got)-1]
	code := regexp.MustCompile(`\b\d{6}\b`).FindString(last.Text)
	if last.To != email || code == "" || strings.Contains(last.Subject, code) {
		t.Fatalf("code email %+v", last)
	}
	if _, err := o.ConfirmBoardMail(ctx, T, who, "000000"); err == nil && code != "000000" {
		t.Fatal("a wrong code confirmed the address")
	}
	if v, err = o.ConfirmBoardMail(ctx, T, who, code); err != nil || v.Email != email || v.Pending != "" {
		t.Fatalf("confirmed: %+v %v", v, err)
	}
}

func bnTickAt(o *Ops, ctx context.Context, set func(string), at string) {
	set(at)
	bnTick(ctx, o, T, baAt(at))
}

func TestBoardMailDefaultsAndKinds(t *testing.T) {
	o, ctx, b, m, set := bnSetup(t, "2026-11-16T10:00")
	who := "farhan.khan"
	c, _ := o.AddCard(ctx, T, "priya.nair", b.ID, CardInput{List: b.Lists[0].ID, Title: "Send the margin file", Description: "margin file desc"})
	v, _ := o.BoardMailSettings(ctx, T, who)
	if v.Every != MailDaily || v.At != "09:00" || strings.Join(v.Kinds, ",") != "mention,added,due" {
		t.Fatalf("defaults %+v", v)
	}
	// Defaults: mentions, added and due go; a move does not.
	o.boardNotifyKind(ctx, T, "priya.nair", b, c.ID, NoticeMoved, []string{who}, "priya.nair moved Send the margin file to Doing on Operations")
	set("2026-11-16T10:05")
	o.boardNotifyKind(ctx, T, "priya.nair", b, c.ID, NoticeMention, []string{who}, "priya.nair mentioned you on Send the margin file: please check")
	for _, at := range []string{"2026-11-16T10:30", "2026-11-16T18:00", "2026-11-17T08:59"} {
		bnTickAt(o, ctx, set, at)
	}
	if len(m.got) != 0 {
		t.Fatalf("sent before 09:00: %+v", m.got)
	}
	bnTickAt(o, ctx, set, "2026-11-17T09:00")
	if len(m.got) != 1 {
		t.Fatalf("at 09:00: %d emails", len(m.got))
	}
	e := m.got[0]
	// Ledgerline does not know its address yet: the email says where to look.
	if e.To != "farhan@bhairav.example" || !strings.Contains(e.Subject, "#1 Send the margin file") ||
		!strings.Contains(e.Text, "Operations · #1 Send the margin file\nPriya Nair mentioned you on Send the margin file: please check (16 Nov 10:05)\nIn Ledgerline: Boards → Operations → #1\n") ||
		strings.Contains(e.Text, "moved") || !strings.Contains(e.Text, "once a day at 09:00 IST, for mentions, being added to a card, due-date reminders") {
		t.Fatalf("email %+v", e)
	}
	// Nothing else from the card goes: not its description, nor its comments.
	if strings.Contains(e.Text, "margin file desc") {
		t.Fatal("description in the email")
	}
	// Emailed notices stay unread in the bell.
	if _, unread, _ := o.BoardNotices(ctx, T, who, 0); unread != 3 { // with the one from being added to the board
		t.Fatalf("unread %d", unread)
	}
	// Only comments, right away: the mention is not sent, the comment is.
	if _, err := o.SaveBoardMail(ctx, T, who, BoardMailInput{Every: MailNow, Kinds: []string{NoticeComment}}); err != nil {
		t.Fatal(err)
	}
	set("2026-11-17T10:00")
	o.boardNotifyKind(ctx, T, "priya.nair", b, c.ID, NoticeMention, []string{who}, "priya.nair mentioned you again")
	o.boardNotifyKind(ctx, T, "priya.nair", b, c.ID, NoticeComment, []string{who}, "priya.nair commented on Send the margin file: done")
	o.NoteConsoleAddress(ctx, T, "admin", "https://bo.bhairav.example", false)
	bnTickAt(o, ctx, set, "2026-11-17T10:06")
	if len(m.got) != 2 || strings.Contains(m.got[1].Text, "again") || !strings.Contains(m.got[1].Text, "commented") || !strings.Contains(m.got[1].Text, "\nhttps://bo.bhairav.example/#boards/"+b.ID+"/"+c.ID+"\n") {
		t.Fatalf("kinds: %+v", m.got)
	}
	for _, bad := range []BoardMailInput{{Every: "weekly"}, {Every: MailDaily, At: "9"}, {Every: MailNow, Kinds: []string{"gossip"}}} {
		if _, err := o.SaveBoardMail(ctx, T, who, bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v: %v", bad, err)
		}
	}
	// Recorded: the person's own list, and the outbox.
	if v, _ := o.BoardMailSettings(ctx, T, who); len(v.Recent) != 2 || v.Recent[0].Count != 1 || v.Recent[0].Result != "Sent" {
		t.Fatalf("recent %+v", v.Recent)
	}
	ob, _ := o.Outbox(ctx, T, "admin")
	n := 0
	for _, x := range ob {
		if strings.HasPrefix(x.Ref, "board-email:") {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("outbox has %d board emails", n)
	}
}

func TestBoardMailBatchingAndIdempotence(t *testing.T) {
	o, ctx, b, m, set := bnSetup(t, "2026-11-16T10:00")
	who := "farhan.khan"
	if _, err := o.SaveBoardMail(ctx, T, who, BoardMailInput{Every: MailNow, Kinds: []string{NoticeAdded, NoticeComment}}); err != nil {
		t.Fatal(err)
	}
	note := func(at, text string) {
		set(at)
		o.boardNotifyKind(ctx, T, "priya.nair", b, "", NoticeComment, []string{who}, text)
	}
	note("2026-11-16T10:00", "one")
	bnTickAt(o, ctx, set, "2026-11-16T10:01")
	note("2026-11-16T10:02", "two")
	bnTickAt(o, ctx, set, "2026-11-16T10:03")
	note("2026-11-16T10:04", "three")
	bnTickAt(o, ctx, set, "2026-11-16T10:04")
	if len(m.got) != 0 {
		t.Fatalf("sent before the batch: %d", len(m.got))
	}
	bnTickAt(o, ctx, set, "2026-11-16T10:05")
	if len(m.got) != 1 || !strings.Contains(m.got[0].Subject, "3 notifications") || !strings.Contains(m.got[0].Text, "one") || !strings.Contains(m.got[0].Text, "three") {
		t.Fatalf("the batch: %+v", m.got)
	}
	// Later minutes, and a restart, send nothing more.
	bnTickAt(o, ctx, set, "2026-11-16T10:06")
	o2 := New(o.App)
	o2.Now, o2.Mailer = o.Now, m
	bnTick(ctx, o2, T, baAt("2026-11-16T10:07"))
	if len(m.got) != 1 {
		t.Fatalf("sent again: %d", len(m.got))
	}
	// The next one waits its own 5 minutes; one read meanwhile is left out.
	note("2026-11-16T10:20", "four")
	note("2026-11-16T10:21", "five")
	ns, _, _ := o.BoardNotices(ctx, T, who, 0)
	if _, err := o.ReadBoardNotices(ctx, T, who, ns[0].ID); err != nil {
		t.Fatal(err)
	}
	bnTickAt(o, ctx, set, "2026-11-16T10:24")
	bnTickAt(o, ctx, set, "2026-11-16T10:25")
	if len(m.got) != 2 || !strings.Contains(m.got[1].Text, "four") || strings.Contains(m.got[1].Text, "five") {
		t.Fatalf("second batch: %+v", m.got[1:])
	}
	// The mail server fails: the notices are tried again later, not lost.
	note("2026-11-16T11:00", "six")
	f := &failMailer{}
	o.Mailer = f
	bnTickAt(o, ctx, set, "2026-11-16T11:05")
	bnTickAt(o, ctx, set, "2026-11-16T11:06")
	if f.n != 1 {
		t.Fatalf("failed sends %d", f.n)
	}
	o.Mailer = m
	bnTickAt(o, ctx, set, "2026-11-16T11:10")
	if len(m.got) != 2 {
		t.Fatal("retried before the wait")
	}
	bnTickAt(o, ctx, set, "2026-11-16T11:21")
	if len(m.got) != 3 || !strings.Contains(m.got[2].Text, "six") {
		t.Fatalf("after the retry: %+v", m.got[2:])
	}
}

func TestBoardMailHourlyPushPauseFirm(t *testing.T) {
	o, ctx, b, m, set := bnSetup(t, "2026-11-16T10:10")
	who := "farhan.khan"
	if _, err := o.SaveBoardMail(ctx, T, who, BoardMailInput{Every: MailHourly, Kinds: []string{NoticeMention}}); err != nil {
		t.Fatal(err)
	}
	o.boardNotifyKind(ctx, T, "priya.nair", b, "", NoticeMention, []string{who}, "priya.nair mentioned you: hourly")
	bnTickAt(o, ctx, set, "2026-11-16T10:30")
	bnTickAt(o, ctx, set, "2026-11-16T10:59")
	if len(m.got) != 0 {
		t.Fatalf("hourly sent within the hour: %+v", m.got)
	}
	bnTickAt(o, ctx, set, "2026-11-16T11:00")
	if len(m.got) != 1 {
		t.Fatalf("hourly at 11:00: %d", len(m.got))
	}
	// An automation "email" goes within 5 minutes, whatever the kinds or how often.
	set("2026-11-16T11:10")
	o.boardNotifyEmail(ctx, T, AutomationActor, b, "", []string{who}, "Margin file not sent yet")
	bnTickAt(o, ctx, set, "2026-11-16T11:14")
	bnTickAt(o, ctx, set, "2026-11-16T11:15")
	if len(m.got) != 2 || !strings.Contains(m.got[1].Text, "Margin file not sent yet") {
		t.Fatalf("pushed: %+v", m.got[1:])
	}
	// Paused for a day: nothing then, nor later for what came meanwhile.
	if v, err := o.PauseBoardMail(ctx, T, who, 1); err != nil || v.PausedUntil == "" {
		t.Fatalf("pause %+v %v", v, err)
	}
	set("2026-11-16T12:30")
	o.boardNotifyKind(ctx, T, "priya.nair", b, "", NoticeMention, []string{who}, "priya.nair mentioned you while paused")
	bnTickAt(o, ctx, set, "2026-11-16T13:00")
	bnTickAt(o, ctx, set, "2026-11-17T11:30")
	bnTickAt(o, ctx, set, "2026-11-17T12:00")
	if len(m.got) != 2 {
		t.Fatalf("sent while or after paused: %+v", m.got[2:])
	}
	set("2026-11-17T12:10")
	o.boardNotifyKind(ctx, T, "priya.nair", b, "", NoticeMention, []string{who}, "priya.nair mentioned you after the pause")
	bnTickAt(o, ctx, set, "2026-11-17T13:00")
	if len(m.got) != 3 || strings.Contains(m.got[2].Text, "while paused") {
		t.Fatalf("after the pause: %+v", m.got[2:])
	}
	// The firm switch: only an Admin; off, nothing goes; on again, only what comes after.
	if _, err := o.SetBoardMailFirm(ctx, T, who, true); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a checker switched the firm off: %v", err)
	}
	if v, err := o.SetBoardMailFirm(ctx, T, "admin", true); err != nil || !v.FirmOff {
		t.Fatalf("firm off %+v %v", v, err)
	}
	set("2026-11-17T13:10")
	o.boardNotifyKind(ctx, T, "priya.nair", b, "", NoticeMention, []string{who}, "priya.nair mentioned you while the firm was off")
	bnTickAt(o, ctx, set, "2026-11-17T14:00")
	o.SetBoardMailFirm(ctx, T, "admin", false)
	bnTickAt(o, ctx, set, "2026-11-17T15:00")
	if len(m.got) != 3 {
		t.Fatalf("sent what came while the firm was off: %+v", m.got[3:])
	}
	// Never: nothing at all.
	o.SaveBoardMail(ctx, T, who, BoardMailInput{Every: MailNever, Kinds: []string{NoticeMention}})
	o.boardNotifyEmail(ctx, T, AutomationActor, b, "", []string{who}, "never mind")
	bnTickAt(o, ctx, set, "2026-11-17T16:00")
	if len(m.got) != 3 {
		t.Fatal("emailed someone who chose never")
	}
}

func TestBoardMailAccessAndAddress(t *testing.T) {
	o, ctx, b, m, set := bnSetup(t, "2026-11-16T10:00")
	maker, checker, risk := "priya.nair", "farhan.khan", "anita.rao"
	for _, who := range []string{checker, risk} {
		if who == risk {
			bnConfirm(t, o, ctx, m, risk, "anita@bhairav.example")
			m.got = nil
		}
		if _, err := o.SaveBoardMail(ctx, T, who, BoardMailInput{Every: MailNow, Kinds: NoticeKinds}); err != nil {
			t.Fatal(err)
		}
	}
	other, _ := o.CreateBoard(ctx, T, maker, BoardInput{Name: "Treasury", Visibility: "private"})
	other, _ = o.UpdateBoard(ctx, T, maker, other.ID, BoardInput{Members: []BoardPerson{{maker, BoardAdmin}, {checker, BoardMember}}})
	// A notice from a board the checker is then taken off is left out.
	o.boardNotifyKind(ctx, T, maker, other, "", NoticeAdded, []string{checker}, "priya.nair added you to the board Treasury")
	o.boardNotifyKind(ctx, T, maker, b, "", NoticeAdded, []string{checker}, "priya.nair added you to Operations")
	o.UpdateBoard(ctx, T, maker, other.ID, BoardInput{Members: []BoardPerson{{maker, BoardAdmin}}})
	// The meera.iyer branch user has no address: nothing goes to her.
	o.boardNotifyKind(ctx, T, maker, b, "", NoticeAdded, []string{"meera.iyer"}, "for Meera")
	bnTickAt(o, ctx, set, "2026-11-16T10:05")
	if len(m.got) != 1 || m.got[0].To != "farhan@bhairav.example" || strings.Contains(m.got[0].Text, "Treasury") || !strings.Contains(m.got[0].Text, "Operations") {
		t.Fatalf("lost access: %+v", m.got)
	}
	// Someone whose login is switched off gets nothing.
	us, _ := o.Users(ctx, T)
	for _, u := range us {
		if u.ID == risk {
			u.Active = false
			if err := o.PutUser(ctx, T, "admin", u); err != nil {
				t.Fatal(err)
			}
		}
	}
	set("2026-11-16T11:00")
	o.boardNotifyKind(ctx, T, maker, b, "", NoticeAdded, []string{risk}, "for Anita")
	bnTickAt(o, ctx, set, "2026-11-16T11:10")
	if len(m.got) != 1 {
		t.Fatalf("emailed an inactive user: %+v", m.got[1:])
	}
	// Removing the address stops email.
	if v, err := o.SetBoardMailAddress(ctx, T, checker, ""); err != nil || v.Email != "" {
		t.Fatalf("remove %+v %v", v, err)
	}
	set("2026-11-16T12:00")
	o.boardNotifyKind(ctx, T, maker, b, "", NoticeAdded, []string{checker}, "after removing")
	bnTickAt(o, ctx, set, "2026-11-16T12:10")
	if len(m.got) != 1 {
		t.Fatal("emailed after the address was removed")
	}
	// Addresses: checked, a code a minute at most, expiring codes, five tries.
	if _, err := o.SetBoardMailAddress(ctx, T, checker, "not an address"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad address: %v", err)
	}
	if _, err := o.SetBoardMailAddress(ctx, T, checker, "farhan@bhairav.example"); err != nil {
		t.Fatal(err)
	}
	if _, err := o.SetBoardMailAddress(ctx, T, checker, "farhan@bhairav.example"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("second code within a minute: %v", err)
	}
	code := regexp.MustCompile(`\b\d{6}\b`).FindString(m.got[len(m.got)-1].Text)
	set("2026-11-16T12:45")
	if _, err := o.ConfirmBoardMail(ctx, T, checker, code); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired code: %v", err)
	}
	if _, err := o.SetBoardMailAddress(ctx, T, checker, "farhan@bhairav.example"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		o.ConfirmBoardMail(ctx, T, checker, "12345x")
	}
	code = regexp.MustCompile(`\b\d{6}\b`).FindString(m.got[len(m.got)-1].Text)
	if _, err := o.ConfirmBoardMail(ctx, T, checker, code); err == nil || !strings.Contains(err.Error(), "too many") {
		t.Fatalf("after five wrong codes: %v", err)
	}
	// Notices from before an address is confirmed are not emailed.
	p := o.bnPrefs(ctx, T, checker)
	if p.Email != "" {
		t.Fatalf("address after failed confirmations %+v", p)
	}
	st, _, _ := docs.GetOr(ctx, o.Docs, T, cBoardMailState, checker, bnState{})
	if st.Seq == 0 {
		t.Fatal("no emailed mark kept")
	}
}
