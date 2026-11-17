package ops

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"strings"
	"time"

	"ledgerline/internal/docs"
)

// ---- Board notifications by email ----
//
// Trello emails people their notifications; so do Ledgerline's boards.
// Each person chooses, in the bell's Notification settings:
//
//   - their address, confirmed with a code emailed to it (Ledgerline's
//     user records hold no email address, and a typing mistake must not
//     send card titles to a stranger);
//   - how often: right away (at most one email every 5 minutes), every
//     hour, once a day at a time they choose, or never;
//   - which kinds of notice (BoardNotice.Kind);
//   - a pause of a day or a week (what comes in meanwhile is not emailed
//     later: it stays in the bell).
//
// Delivery runs in the boards' minute (boardTickHooks). It reads the
// person's unread notices that were not yet emailed and sends one digest
// through mailStaff, which keeps each email in the outbox with what really
// happened. An email says only how many notices of each kind are waiting,
// with a link to Ledgerline: never a board's name, a card's title, a
// notice's text or anything else from a card, since a mailbox may be
// outside India (SEBI's cloud framework). Notices
// stay unread. bnState.Seq marks the notices already emailed or passed
// over, and is moved before the email goes, so a restart or a second
// server never sends one twice. Notices from a board the person can no
// longer see are left out. A firm Admin can switch board email off for the
// whole firm.

const (
	cBoardMailPref  = "boardmailpref"  // "<user>": a person's email settings
	cBoardMailState = "boardmailstate" // "<user>": what was emailed (bnState)
	cBoardMailLog   = "boardmaillog"   // "<user>|<seq, 12 digits>": each email sent
	cBoardMailFirm  = "boardmailfirm"  // "firm": the firm's switch and console address

	MailNow    = "now"    // right away, batched: at most one email every 5 minutes
	MailHourly = "hourly" // at the start of each hour
	MailDaily  = "daily"  // once a day at a time (IST)
	MailNever  = "never"

	bnBatch      = 5 * time.Minute
	bnMaxPending = 200 // notices looked at for one email
	bnMaxShown   = 50  // notices written out in one email
	bnCodeTTL    = 30 * time.Minute
	bnRetry      = 15 * time.Minute // after the mail server failed
)

var (
	// NoticeKinds are the kinds people choose from (older notices with no
	// kind, and NoticeOther, count as NoticeWatch).
	NoticeKinds    = []string{NoticeAdded, NoticeMention, NoticeComment, NoticeMoved, NoticeDue, NoticeWatch, NoticeAutomation}
	bnDefaultKinds = []string{NoticeMention, NoticeAdded, NoticeDue}
	bnKindWords    = map[string]string{NoticeAdded: "being added to a card", NoticeMention: "mentions", NoticeComment: "comments on cards you watch",
		NoticeMoved: "cards you watch moving", NoticeDue: "due-date reminders", NoticeWatch: "other activity on cards you watch", NoticeAutomation: "automation messages"}
)

func init() {
	boardTickHooks = append(boardTickHooks, bnTick)
}

// BoardMailPrefs are a person's board email settings.
type BoardMailPrefs struct {
	User        string   `json:"user"`
	Email       string   `json:"email,omitempty"` // the confirmed address
	Since       string   `json:"since,omitempty"` // when it was confirmed: earlier notices are not emailed
	Pending     string   `json:"pending,omitempty"`
	CodeHash    string   `json:"code_hash,omitempty"`
	CodeAt      string   `json:"code_at,omitempty"`
	Tries       int      `json:"tries,omitempty"`
	Every       string   `json:"every,omitempty"` // now, hourly, daily, never ("" daily)
	At          string   `json:"at,omitempty"`    // daily: HH:MM IST ("" 09:00)
	Kinds       []string `json:"kinds,omitempty"`
	KindsSet    bool     `json:"kinds_set,omitempty"` // Kinds was chosen (none is a choice)
	PausedFrom  string   `json:"paused_from,omitempty"`
	PausedUntil string   `json:"paused_until,omitempty"`
	UpdatedAt   string   `json:"updated_at,omitempty"`
}

// BoardMailInput changes how often and what is emailed.
type BoardMailInput struct {
	Every string   `json:"every"`
	At    string   `json:"at"`
	Kinds []string `json:"kinds"`
}

// BoardMailView is a person's settings for the console.
type BoardMailView struct {
	Email       string          `json:"email"`
	Pending     string          `json:"pending,omitempty"` // an address waiting for its code
	Every       string          `json:"every"`
	At          string          `json:"at"`
	Kinds       []string        `json:"kinds"`
	PausedUntil string          `json:"paused_until,omitempty"`
	FirmOff     bool            `json:"firm_off"`
	CanFirm     bool            `json:"can_firm"`    // an Admin, who can switch board email for the firm
	MailServer  bool            `json:"mail_server"` // a mail server is set (or this is a development firm)
	Recent      []BoardMailSent `json:"recent"`
	Sent        string          `json:"sent,omitempty"`     // what happened to the code just sent
	DevCode     string          `json:"dev_code,omitempty"` // development servers only
}

// BoardMailSent is one board email, for the person's record.
type BoardMailSent struct {
	User    string   `json:"user"`
	Seq     int64    `json:"seq"`
	At      string   `json:"at"`
	To      string   `json:"to"`
	Subject string   `json:"subject"`
	Count   int      `json:"count"`
	Result  string   `json:"result"`
	Notices []string `json:"notices"`
}

// bnState is what was emailed to a person.
type bnState struct {
	User     string `json:"user"`
	Seq      int64  `json:"seq"`               // notices up to this one were emailed or passed over
	Looked   int64  `json:"looked,omitempty"`  // the newest notice looked at
	Oldest   string `json:"oldest,omitempty"`  // when the first waiting notice that makes an email due came
	Checked  string `json:"checked,omitempty"` // hourly or daily: the slot already looked at
	LastSent string `json:"last_sent,omitempty"`
	Retry    string `json:"retry,omitempty"` // after a mail server failure, not before
	Sent     int    `json:"sent,omitempty"`
}

// bnFirm is the firm's switch and the address its people reach the console at.
type bnFirm struct {
	Off     bool   `json:"off"`
	Since   string `json:"since,omitempty"` // when it was last switched on: earlier notices are not emailed
	By      string `json:"by,omitempty"`
	At      string `json:"at,omitempty"`
	Console string `json:"console,omitempty"`
}

// bnNotice is a BoardNotice as written by boardNotifyEmail: Push asks for
// it to be emailed with the next 5-minute batch, whatever kinds and how
// often the person chose (but not if they chose never, paused email or
// have no address).
type bnNotice struct {
	BoardNotice
	Push bool `json:"push,omitempty"`
}

func bnEvery(p BoardMailPrefs) string { return firstOf(p.Every, MailDaily) }
func bnAt(p BoardMailPrefs) string    { return firstOf(p.At, "09:00") }
func bnKinds(p BoardMailPrefs) []string {
	if !p.KindsSet {
		return bnDefaultKinds
	}
	if p.Kinds == nil {
		return []string{}
	}
	return p.Kinds
}

// bnCountWords names a kind of notice in a count ("2 mentions").
func bnCountWords(kind string, n int) string {
	w, ok := map[string][2]string{
		NoticeAdded:      {"time you were added to a card or board", "times you were added to a card or board"},
		NoticeMention:    {"mention", "mentions"},
		NoticeComment:    {"comment on a card you watch", "comments on cards you watch"},
		NoticeMoved:      {"card you watch moved", "cards you watch moved"},
		NoticeDue:        {"due-date reminder", "due-date reminders"},
		NoticeWatch:      {"change on something you watch", "changes on things you watch"},
		NoticeAutomation: {"message from an automation rule", "messages from automation rules"},
	}[kind]
	if !ok {
		w = [2]string{"notification", "notifications"}
	}
	if n == 1 {
		return w[0]
	}
	return w[1]
}

func bnKindOf(k string) string {
	if contains(NoticeKinds, k) {
		return k
	}
	return NoticeWatch
}

func bnTime(s string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339, s)
	return t, err == nil
}

// ---- reading and changing a person's settings ----

func (o *Ops) bnPrefs(ctx context.Context, tenant, user string) BoardMailPrefs {
	p, _, _ := docs.GetOr(ctx, o.Docs, tenant, cBoardMailPref, user, BoardMailPrefs{User: user})
	p.User = user
	return p
}

func (o *Ops) bnFirm(ctx context.Context, tenant string) bnFirm {
	f, _, _ := docs.GetOr(ctx, o.Docs, tenant, cBoardMailFirm, "firm", bnFirm{})
	return f
}

// bnUpdate changes a person's settings (made on first use).
func (o *Ops) bnUpdate(ctx context.Context, tenant, user string, fn func(*BoardMailPrefs) error) (BoardMailPrefs, error) {
	if _, _, err := docs.Get[BoardMailPrefs](ctx, o.Docs, tenant, cBoardMailPref, user); errors.Is(err, docs.ErrNotFound) {
		if _, err := docs.Put(ctx, o.Docs, tenant, cBoardMailPref, user, BoardMailPrefs{User: user}, 0, user); err != nil && !errors.Is(err, docs.ErrStale) {
			return BoardMailPrefs{}, err
		}
	}
	return docs.Update(ctx, o.Docs, tenant, cBoardMailPref, user, user, func(p *BoardMailPrefs) error {
		p.User = user
		if err := fn(p); err != nil {
			return err
		}
		p.UpdatedAt = o.now().Format(time.RFC3339)
		return nil
	})
}

// bnStateUpdate changes what was emailed to a person (made on first use).
func (o *Ops) bnStateUpdate(ctx context.Context, tenant, user string, fn func(*bnState) error) (bnState, error) {
	if _, _, err := docs.Get[bnState](ctx, o.Docs, tenant, cBoardMailState, user); errors.Is(err, docs.ErrNotFound) {
		if _, err := docs.Put(ctx, o.Docs, tenant, cBoardMailState, user, bnState{User: user}, 0, SystemActor); err != nil && !errors.Is(err, docs.ErrStale) {
			return bnState{}, err
		}
	}
	return docs.Update(ctx, o.Docs, tenant, cBoardMailState, user, SystemActor, fn)
}

// bnLatest is the number of a person's newest notice.
func (o *Ops) bnLatest(ctx context.Context, tenant, user string) int64 {
	s, _, _ := docs.GetOr(ctx, o.Docs, tenant, cSeq, "boardnote:"+user, seqDoc{})
	return s.N
}

// bnSkipBacklog passes over the person's notices so far: they stay in the
// bell and are not emailed.
func (o *Ops) bnSkipBacklog(ctx context.Context, tenant, user string) {
	latest := o.bnLatest(ctx, tenant, user)
	_, _ = o.bnStateUpdate(ctx, tenant, user, func(s *bnState) error {
		if s.Seq >= latest {
			return errBANoChange
		}
		s.Seq, s.Looked, s.Oldest = latest, latest, ""
		return nil
	})
}

func (o *Ops) bnView(ctx context.Context, tenant, actor string, p BoardMailPrefs) BoardMailView {
	f := o.bnFirm(ctx, tenant)
	role, _ := o.RoleOf(ctx, tenant, actor)
	v := BoardMailView{Email: p.Email, Pending: p.Pending, Every: bnEvery(p), At: bnAt(p), Kinds: bnKinds(p), FirmOff: f.Off, CanFirm: role == RoleAdmin,
		MailServer: o.mailerFor(ctx, tenant) != nil || o.DevCodes || o.devFirm(ctx, tenant), Recent: []BoardMailSent{}}
	if t, ok := bnTime(p.PausedUntil); ok && o.now().Before(t) {
		v.PausedUntil = p.PausedUntil
	}
	ls, _ := docs.List[BoardMailSent](ctx, o.Docs, tenant, cBoardMailLog, actor+"|")
	for i := len(ls) - 1; i >= 0 && len(v.Recent) < 5; i-- {
		v.Recent = append(v.Recent, ls[i])
	}
	return v
}

// BoardMailSettings are the person's board email settings.
func (o *Ops) BoardMailSettings(ctx context.Context, tenant, actor string) (BoardMailView, error) {
	if _, err := o.boardUser(ctx, tenant, actor); err != nil {
		return BoardMailView{}, err
	}
	return o.bnView(ctx, tenant, actor, o.bnPrefs(ctx, tenant, actor)), nil
}

// SaveBoardMail changes how often, and which kinds of notice, are emailed.
func (o *Ops) SaveBoardMail(ctx context.Context, tenant, actor string, in BoardMailInput) (BoardMailView, error) {
	if _, err := o.boardUser(ctx, tenant, actor); err != nil {
		return BoardMailView{}, err
	}
	switch in.Every {
	case MailNow, MailHourly, MailDaily, MailNever:
	default:
		return BoardMailView{}, invalid("email right away, every hour, once a day or never")
	}
	at := strings.TrimSpace(in.At)
	if in.Every == MailDaily && !baHHMM.MatchString(at) {
		return BoardMailView{}, invalid("give the time of the daily email as HH:MM (IST), e.g. 09:00")
	}
	kinds := []string{}
	for _, k := range in.Kinds {
		if !contains(NoticeKinds, k) {
			return BoardMailView{}, invalid("unknown kind of notification %q", k)
		}
		if !contains(kinds, k) {
			kinds = append(kinds, k)
		}
	}
	was := ""
	p, err := o.bnUpdate(ctx, tenant, actor, func(p *BoardMailPrefs) error {
		was = bnEvery(*p)
		p.Every, p.Kinds, p.KindsSet = in.Every, kinds, true
		if in.Every == MailDaily {
			p.At = at
		}
		return nil
	})
	if err != nil {
		return BoardMailView{}, err
	}
	if was == MailNever && in.Every != MailNever {
		o.bnSkipBacklog(ctx, tenant, actor) // what came while it was off stays in the bell
	}
	return o.bnView(ctx, tenant, actor, p), nil
}

func bnCodeHash(tenant, user, code string) string {
	h := sha256.Sum256([]byte("boardmail|" + tenant + "|" + user + "|" + code))
	return hex.EncodeToString(h[:])
}

// SetBoardMailAddress starts using an address ("" stops board email): a
// code goes to it, and ConfirmBoardMail with that code makes it the one.
func (o *Ops) SetBoardMailAddress(ctx context.Context, tenant, actor, email string) (BoardMailView, error) {
	u, err := o.boardUser(ctx, tenant, actor)
	if err != nil {
		return BoardMailView{}, err
	}
	email = strings.ToLower(strings.TrimSpace(email))
	now := o.now()
	if email == "" {
		p, err := o.bnUpdate(ctx, tenant, actor, func(p *BoardMailPrefs) error {
			p.Email, p.Pending, p.CodeHash, p.CodeAt, p.Tries = "", "", "", "", 0
			return nil
		})
		if err != nil {
			return BoardMailView{}, err
		}
		o.log(ctx, tenant, actor, "board.email_removed", actor, nil)
		return o.bnView(ctx, tenant, actor, p), nil
	}
	if len(email) > 200 || !reEmail.MatchString(email) || strings.HasSuffix(email, ".invalid") {
		return BoardMailView{}, invalid("%q is not an email address", email)
	}
	old := o.bnPrefs(ctx, tenant, actor)
	if t, ok := bnTime(old.CodeAt); ok && now.Sub(t) < time.Minute {
		return BoardMailView{}, invalid("a code was sent less than a minute ago; wait a moment before asking for another")
	}
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return BoardMailView{}, err
	}
	code := fmt.Sprintf("%06d", n.Int64())
	p, err := o.bnUpdate(ctx, tenant, actor, func(p *BoardMailPrefs) error {
		p.Pending, p.CodeHash, p.CodeAt, p.Tries = email, bnCodeHash(tenant, actor, code), now.Format(time.RFC3339), 0
		return nil
	})
	if err != nil {
		return BoardMailView{}, err
	}
	set, _ := o.Settings(ctx, tenant)
	text := fmt.Sprintf("Hello %s,\n\nYour code to get board notifications from Ledgerline by email is %s. Enter it in Boards → the bell → Notification settings. It is valid for 30 minutes.\n\nIf you did not ask for this, ignore this email: nothing is sent to this address without the code.\n\n%s",
		firstOf(u.Name, actor), code, set.Firm)
	v := o.bnView(ctx, tenant, actor, p)
	v.Sent = o.mailStaff(ctx, tenant, email, fmt.Sprintf("[%s] Your code for board emails", set.Firm), text, "board-email-code:"+actor, nil)
	if o.DevCodes || o.devFirm(ctx, tenant) {
		v.DevCode = code
	}
	o.log(ctx, tenant, actor, "board.email_code_sent", actor, map[string]any{"to": email, "result": v.Sent})
	return v, nil
}

// ConfirmBoardMail makes the waiting address the person's board email
// address. Notices from before are not emailed.
func (o *Ops) ConfirmBoardMail(ctx context.Context, tenant, actor, code string) (BoardMailView, error) {
	if _, err := o.boardUser(ctx, tenant, actor); err != nil {
		return BoardMailView{}, err
	}
	code = strings.TrimSpace(code)
	now := o.now()
	var bad error
	p, err := o.bnUpdate(ctx, tenant, actor, func(p *BoardMailPrefs) error {
		bad = nil
		at, ok := bnTime(p.CodeAt)
		switch {
		case p.Pending == "" || p.CodeHash == "":
			return invalid("there is no address waiting for a code; give the address first")
		case !ok || now.Sub(at) > bnCodeTTL:
			p.CodeHash = ""
			bad = invalid("the code has expired; ask for a new one")
			return nil
		case p.Tries >= 5:
			p.CodeHash = ""
			bad = invalid("too many wrong codes; ask for a new one")
			return nil
		case bnCodeHash(tenant, actor, code) != p.CodeHash:
			p.Tries++
			bad = invalid("that is not the code sent to %s", p.Pending)
			return nil
		}
		p.Email, p.Since, p.Pending, p.CodeHash, p.CodeAt, p.Tries = p.Pending, now.Format(time.RFC3339), "", "", "", 0
		return nil
	})
	if err != nil {
		return BoardMailView{}, err
	}
	if bad != nil {
		return BoardMailView{}, bad
	}
	o.bnSkipBacklog(ctx, tenant, actor)
	o.log(ctx, tenant, actor, "board.email_confirmed", actor, map[string]any{"email": p.Email})
	return o.bnView(ctx, tenant, actor, p), nil
}

// PauseBoardMail pauses board email for 1 or 7 days (0 starts it again).
// What comes in meanwhile stays in the bell and is not emailed later.
func (o *Ops) PauseBoardMail(ctx context.Context, tenant, actor string, days int) (BoardMailView, error) {
	if _, err := o.boardUser(ctx, tenant, actor); err != nil {
		return BoardMailView{}, err
	}
	if days != 0 && days != 1 && days != 7 {
		return BoardMailView{}, invalid("pause board email for a day or a week")
	}
	now := o.now()
	p, err := o.bnUpdate(ctx, tenant, actor, func(p *BoardMailPrefs) error {
		if days == 0 {
			if t, ok := bnTime(p.PausedUntil); ok && now.Before(t) {
				p.PausedUntil = now.Format(time.RFC3339) // the pause ends now
			}
			return nil
		}
		if t, ok := bnTime(p.PausedUntil); !ok || !now.Before(t) {
			p.PausedFrom = now.Format(time.RFC3339)
		}
		p.PausedUntil = now.Add(time.Duration(days) * 24 * time.Hour).Format(time.RFC3339)
		return nil
	})
	if err != nil {
		return BoardMailView{}, err
	}
	return o.bnView(ctx, tenant, actor, p), nil
}

// SetBoardMailFirm switches board email off (or on) for the whole firm
// (Admins). Switched back on, it emails what comes in from then on.
func (o *Ops) SetBoardMailFirm(ctx context.Context, tenant, actor string, off bool) (BoardMailView, error) {
	if err := o.require(ctx, tenant, actor, "Switching board email for the firm", RoleAdmin); err != nil {
		return BoardMailView{}, err
	}
	now := o.now().Format(time.RFC3339)
	if _, _, err := docs.Get[bnFirm](ctx, o.Docs, tenant, cBoardMailFirm, "firm"); errors.Is(err, docs.ErrNotFound) {
		if _, err := docs.Put(ctx, o.Docs, tenant, cBoardMailFirm, "firm", bnFirm{}, 0, actor); err != nil && !errors.Is(err, docs.ErrStale) {
			return BoardMailView{}, err
		}
	}
	changed := false
	if _, err := docs.Update(ctx, o.Docs, tenant, cBoardMailFirm, "firm", actor, func(f *bnFirm) error {
		changed = f.Off != off
		if !changed {
			return errBANoChange
		}
		f.Off, f.By, f.At = off, actor, now
		if !off {
			f.Since = now
		}
		return nil
	}); err != nil && !errors.Is(err, errBANoChange) {
		return BoardMailView{}, err
	}
	if changed {
		o.log(ctx, tenant, actor, map[bool]string{true: "board.email_off", false: "board.email_on"}[off], "firm", nil)
	}
	return o.bnView(ctx, tenant, actor, o.bnPrefs(ctx, tenant, actor)), nil
}

// NoteConsoleAddress records the address people reach the console at, for
// the links in board emails and {cardlink}. Only the server's configured
// public address, or an address an Admin uses, is taken.
func (o *Ops) NoteConsoleAddress(ctx context.Context, tenant, actor, addr string, configured bool) {
	u, err := url.Parse(strings.TrimSpace(addr))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || strings.ContainsAny(u.Host, " /\\@") {
		return
	}
	if !configured {
		if role, err := o.RoleOf(ctx, tenant, actor); err != nil || role != RoleAdmin || strings.Contains(actor, ":") {
			return
		}
	}
	base := u.Scheme + "://" + u.Host
	if o.bnFirm(ctx, tenant).Console == base {
		return
	}
	if _, _, err := docs.Get[bnFirm](ctx, o.Docs, tenant, cBoardMailFirm, "firm"); errors.Is(err, docs.ErrNotFound) {
		if _, err := docs.Put(ctx, o.Docs, tenant, cBoardMailFirm, "firm", bnFirm{}, 0, SystemActor); err != nil && !errors.Is(err, docs.ErrStale) {
			return
		}
	}
	_, _ = docs.Update(ctx, o.Docs, tenant, cBoardMailFirm, "firm", SystemActor, func(f *bnFirm) error { f.Console = base; return nil })
}

// BoardCardLink is a link to a card in the console: on the firm's console
// address when Ledgerline knows it, otherwise the part after the address.
func (o *Ops) BoardCardLink(ctx context.Context, tenant, board, card string) string {
	p := "#boards/" + url.PathEscape(board)
	if card != "" {
		p += "/" + url.PathEscape(card)
	}
	if base := o.bnFirm(ctx, tenant).Console; base != "" {
		return base + "/" + p
	}
	return p
}

// boardNotifyEmail leaves automation notices that are to be emailed with
// the next 5-minute batch (the "email" automation action).
func (o *Ops) boardNotifyEmail(ctx context.Context, tenant, actor string, b Board, card string, users []string, text string) {
	seen := map[string]bool{}
	for _, u := range users {
		if u == "" || u == actor || seen[u] || strings.HasPrefix(u, "system:") {
			continue
		}
		seen[u] = true
		n, err := o.next(ctx, tenant, "boardnote:"+u, actor)
		if err != nil {
			continue
		}
		id := fmt.Sprintf("%s|%012d", u, n)
		_ = docs.Upsert(ctx, o.Docs, tenant, cBoardNote, id, bnNotice{BoardNotice: BoardNotice{ID: id, User: u, At: o.now().Format(time.RFC3339), Board: b.ID, Card: card, By: actor, Text: text, Kind: NoticeAutomation}, Push: true}, actor)
	}
}

// ---- delivery: the boards' minute ----

type bnItem struct {
	n     bnNotice
	at    time.Time
	board string // the board's name
	card  string // "#12 Card title" ("" none, or the card is gone)
	link  string
}

type bnRun struct {
	o      *Ops
	ctx    context.Context
	tenant string
	now    time.Time
	firm   bnFirm
	users  map[string]User
	single bool // no users configured: everyone is an Admin
	boards map[string]*Board
	cards  map[string]*Card
}

func bnTick(ctx context.Context, o *Ops, tenant string, now time.Time) {
	defer o.baRecover(ctx, tenant, "")
	ps, err := docs.List[BoardMailPrefs](ctx, o.Docs, tenant, cBoardMailPref, "")
	if err != nil || len(ps) == 0 {
		return
	}
	f := o.bnFirm(ctx, tenant)
	if f.Off {
		return
	}
	us, _ := o.Users(ctx, tenant)
	r := &bnRun{o: o, ctx: ctx, tenant: tenant, now: now.In(ist), firm: f, users: map[string]User{}, single: len(us) == 0, boards: map[string]*Board{}, cards: map[string]*Card{}}
	for _, u := range us {
		r.users[u.ID] = u
	}
	for _, p := range ps {
		r.person(p)
	}
}

// slot is the moment the hourly or daily email is due from (the latest
// one not after now), and a key for it.
func (r *bnRun) slot(p BoardMailPrefs) (time.Time, string) {
	switch bnEvery(p) {
	case MailHourly:
		// (Truncate would cut at UTC hours, which are half past in IST.)
		t := time.Date(r.now.Year(), r.now.Month(), r.now.Day(), r.now.Hour(), 0, 0, 0, ist)
		return t, "h" + t.Format("2006-01-02T15")
	case MailDaily:
		t, err := time.ParseInLocation("2006-01-02 15:04", r.now.Format("2006-01-02")+" "+bnAt(p), ist)
		if err != nil {
			t = time.Date(r.now.Year(), r.now.Month(), r.now.Day(), 9, 0, 0, 0, ist)
		}
		if r.now.Before(t) {
			t = t.AddDate(0, 0, -1)
		}
		return t, "d" + t.Format("2006-01-02T15:04")
	}
	return r.now, ""
}

func (r *bnRun) user(id string) (User, bool) {
	if r.single {
		return User{ID: id, Name: id, Role: RoleAdmin, Active: true}, true
	}
	u, ok := r.users[id]
	return u, ok && u.Active && u.Role != RoleClient
}

func (r *bnRun) person(p BoardMailPrefs) {
	if p.Email == "" || bnEvery(p) == MailNever {
		return
	}
	if t, ok := bnTime(p.PausedUntil); ok && r.now.Before(t) {
		return
	}
	u, ok := r.user(p.User)
	if !ok {
		return
	}
	o, ctx, tenant := r.o, r.ctx, r.tenant
	latest := o.bnLatest(ctx, tenant, p.User)
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cBoardMailState, p.User, bnState{User: p.User})
	if latest <= st.Seq {
		return
	}
	if t, ok := bnTime(st.Retry); ok && r.now.Before(t) {
		return
	}
	every := bnEvery(p)
	slot, key := r.slot(p)
	lastSent, _ := bnTime(st.LastSent)
	// Look at the waiting notices only when there are new ones, or the
	// moment an email could be due has come.
	look := latest != st.Looked
	if old, ok := bnTime(st.Oldest); !look && ok && !r.now.Before(old.Add(bnBatch)) {
		look = true
	}
	if !look && every != MailNow && st.Checked != key && lastSent.Before(slot) {
		look = true
	}
	if !look {
		return
	}
	items, upto := r.pending(p, u, st.Seq, latest)
	if len(items) == 0 {
		// Nothing among them to email (read, not chosen, or from a board
		// the person can no longer see): pass them over.
		_, _ = o.bnStateUpdate(ctx, tenant, p.User, func(s *bnState) error {
			if s.Seq != st.Seq {
				return errBANoChange
			}
			s.Seq, s.Looked, s.Oldest = upto, latest, ""
			return nil
		})
		return
	}
	var first, push time.Time
	for _, it := range items {
		if first.IsZero() || it.at.Before(first) {
			first = it.at
		}
		if it.n.Push && (push.IsZero() || it.at.Before(push)) {
			push = it.at
		}
	}
	due := false
	wake := push
	switch every {
	case MailNow:
		due, wake = !r.now.Before(first.Add(bnBatch)), first
	default:
		due = (!push.IsZero() && !r.now.Before(push.Add(bnBatch))) || (first.Before(slot) && lastSent.Before(slot))
	}
	if !due {
		_, _ = o.bnStateUpdate(ctx, tenant, p.User, func(s *bnState) error {
			if s.Seq != st.Seq {
				return errBANoChange
			}
			s.Looked, s.Oldest = latest, ""
			if !wake.IsZero() {
				s.Oldest = wake.Format(time.RFC3339)
			}
			if every != MailNow {
				s.Checked = key
			}
			return nil
		})
		return
	}
	// Claim the notices first: a restart, or a second server, does not send them twice.
	if _, err := o.bnStateUpdate(ctx, tenant, p.User, func(s *bnState) error {
		if s.Seq != st.Seq {
			return errBANoChange
		}
		s.Seq, s.Looked, s.Oldest, s.Checked, s.LastSent, s.Retry = upto, latest, "", key, r.now.Format(time.RFC3339), ""
		s.Sent++
		return nil
	}); err != nil {
		return
	}
	r.send(p, u, items, st.Seq)
}

// pending are the person's notices after seq that are to be emailed, and
// the newest notice they come up to.
func (r *bnRun) pending(p BoardMailPrefs, u User, seq, latest int64) ([]bnItem, int64) {
	o, ctx, tenant := r.o, r.ctx, r.tenant
	start := seq + 1
	if latest-start >= bnMaxPending {
		start = latest - bnMaxPending + 1
	}
	kinds := bnKinds(p)
	since, _ := bnTime(p.Since)
	firmSince, _ := bnTime(r.firm.Since)
	pFrom, _ := bnTime(p.PausedFrom)
	pUntil, _ := bnTime(p.PausedUntil)
	upto := latest
	var out []bnItem
	for n := start; n <= latest; n++ {
		x, _, err := docs.Get[bnNotice](ctx, o.Docs, tenant, cBoardNote, fmt.Sprintf("%s|%012d", p.User, n))
		if err != nil {
			if n > latest-5 {
				upto = n - 1 // numbered but perhaps not written yet: look again next minute
				break
			}
			continue
		}
		at, ok := bnTime(x.At)
		if !ok || x.Read || at.Before(since) || at.Before(firmSince) || (!pFrom.IsZero() && !at.Before(pFrom) && !at.After(pUntil)) {
			continue
		}
		if !x.Push && !contains(kinds, bnKindOf(x.Kind)) {
			continue
		}
		b := r.board(x.Board)
		if b == nil || boardRole(*b, u) == "" {
			continue // the person can no longer see the board
		}
		it := bnItem{n: x, at: at, board: b.Name, link: o.BoardCardLink(ctx, tenant, b.ID, "")}
		if c := r.card(b.ID, x.Card); c != nil {
			it.card, it.link = fmt.Sprintf("#%d %s", c.No, clip(c.Title, 200)), o.BoardCardLink(ctx, tenant, b.ID, c.ID)
		}
		out = append(out, it)
	}
	return out, upto
}

func (r *bnRun) board(id string) *Board {
	if b, ok := r.boards[id]; ok {
		return b
	}
	var bp *Board
	if b, _, err := docs.Get[Board](r.ctx, r.o.Docs, r.tenant, cBoard, id); err == nil && !b.Deleted {
		bp = &b
	}
	r.boards[id] = bp
	return bp
}

func (r *bnRun) card(board, id string) *Card {
	if id == "" {
		return nil
	}
	k := cardKey(board, id)
	if c, ok := r.cards[k]; ok {
		return c
	}
	var cp *Card
	if c, _, err := docs.Get[Card](r.ctx, r.o.Docs, r.tenant, cBoardCard, k); err == nil && !c.Deleted {
		cp = &c
	}
	r.cards[k] = cp
	return cp
}

// send writes the digest and sends it, and records what went.
func (r *bnRun) send(p BoardMailPrefs, u User, items []bnItem, was int64) {
	o, ctx, tenant := r.o, r.ctx, r.tenant
	set, _ := o.Settings(ctx, tenant)
	// The email says only how many notices of each kind are waiting. It
	// never carries a board's name, a card's title, a notice's text or a
	// link to one card: a person's mailbox may be anywhere (Gmail, say),
	// and card titles can name clients. SEBI's cloud framework
	// (SEBI/HO/ITD/ITD_VAPT/P/CIR/2023/033) expects a broker's regulated
	// data to stay in India.
	counts := map[string]int{}
	for _, it := range items {
		counts[bnKindOf(it.n.Kind)]++
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Hello %s,\n\n", firstOf(u.Name, p.User))
	if len(items) == 1 {
		b.WriteString("You have a new notification on Boards in Ledgerline:\n\n")
	} else {
		fmt.Fprintf(&b, "You have %d new notifications on Boards in Ledgerline:\n\n", len(items))
	}
	for _, k := range NoticeKinds {
		if n := counts[k]; n > 0 {
			fmt.Fprintf(&b, "- %d %s\n", n, bnCountWords(k, n))
		}
	}
	open := "Open Ledgerline and choose Boards, then the bell, to read them."
	for _, it := range items {
		if i := strings.Index(it.link, "#boards"); i > 0 {
			open = "Read them in Ledgerline (Boards, then the bell): " + it.link[:i] + "#boards"
			break
		}
	}
	fmt.Fprintf(&b, "\n%s\n\nThese emails never include card titles, board names or comments; they stay in Ledgerline.\n\n", open)
	how := map[string]string{MailNow: "within a few minutes of each notification", MailHourly: "every hour", MailDaily: "once a day at " + bnAt(p) + " IST"}[bnEvery(p)]
	var ws []string
	for _, k := range bnKinds(p) {
		ws = append(ws, bnKindWords[k])
	}
	what := "automation emails sent to you"
	if len(ws) > 0 {
		what = strings.Join(ws, ", ")
	}
	fmt.Fprintf(&b, "You get these emails %s, for %s. Change or pause them in Ledgerline: Boards → the bell → Notification settings.\n\n%s", how, what, set.Firm)
	subject := fmt.Sprintf("[%s] Boards: %d new notifications", set.Firm, len(items))
	if len(items) == 1 {
		subject = fmt.Sprintf("[%s] Boards: a new notification", set.Firm)
	}
	res := o.mailStaff(ctx, tenant, p.Email, subject, b.String(), "board-email:"+p.User, nil)
	ids := make([]string, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.n.ID)
	}
	if strings.HasPrefix(res, "Failed") {
		// The mail server failed: give the notices back, and try again in a while.
		_, _ = o.bnStateUpdate(ctx, tenant, p.User, func(s *bnState) error {
			if s.Seq < was {
				return errBANoChange
			}
			s.Seq, s.Looked, s.Retry = was, 0, r.now.Add(bnRetry).Format(time.RFC3339)
			s.Sent--
			return nil
		})
	}
	if n, err := o.next(ctx, tenant, "boardmaillog:"+p.User, SystemActor); err == nil {
		_ = docs.Upsert(ctx, o.Docs, tenant, cBoardMailLog, fmt.Sprintf("%s|%012d", p.User, n), BoardMailSent{User: p.User, Seq: n, At: r.now.Format(time.RFC3339), To: p.Email, Subject: subject, Count: len(items), Result: res, Notices: ids}, SystemActor)
	}
}
