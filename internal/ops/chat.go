package ops

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
)

// ---- team messages ----
//
// Staff talk to each other inside Ledgerline:
//
//   - Head office: channels the Admin creates, with the members the Admin
//     adds (only members can read them), and direct messages between any
//     two head office staff.
//   - Each branch: the same, among that branch's staff only.
//   - Official desks (Risk, DP, Compliance, and any the Admin adds): each
//     branch and each authorised person has its own private thread with a
//     desk. The desk's head office members see every thread of their desk;
//     a branch or AP sees only its own. Either side can start the thread.
//
// Messages carry text, files and photographs, and @mentions (@all in a
// channel or desk thread). Everyone in a conversation is notified of every
// message; there is no setting to silence it. Ledgy writes to each person
// when the work queue gives them something new to do.
//
// Messages are kept: they cannot be edited or deleted, so the firm has the
// record.

const (
	cChatChan = "chatchan"
	cChatMsg  = "chatmsg"
	cChatRead = "chatread"
	cChatFile = "chatfile"
	cChatDesk = "chatdesk"

	// LedgyActor writes Ledgy's notices.
	LedgyActor = "system:ledgy"

	ChatKindChannel = "channel" // a named channel (head office or one branch)
	ChatDM          = "dm"      // two people
	ChatThread      = "thread"  // a desk and one branch or AP
	ChatLedgy       = "ledgy"   // Ledgy's notices to one person

	chatMaxText   = 4000
	chatMaxFiles  = 6
	chatMaxFile   = 15 << 20
	chatPage      = 50
	chatBucket    = 200
	chatSideHO    = "ho"
	chatPulseMax  = 20
	chatMaxMember = 500

	// chatTime orders as text: fixed-width, to the microsecond.
	chatTime = "2006-01-02T15:04:05.000000Z07:00"
)

// ChatDesk is an official desk at head office (Risk, DP, Compliance…).
type ChatDesk struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	About     string   `json:"about,omitempty"`
	Members   []string `json:"members"` // head office staff who answer for the desk
	Archived  bool     `json:"archived,omitempty"`
	Created   string   `json:"created"`
	CreatedBy string   `json:"created_by"`
}

// ChatChannel is a conversation.
type ChatChannel struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"` // channel, dm, thread, ledgy
	Name  string `json:"name,omitempty"`
	About string `json:"about,omitempty"`
	// Scope is whose it is: "ho" or "branch:<code>" for channels and direct
	// messages; for a desk thread, the branch or AP ("branch:<code>",
	// "ap:<code>").
	Scope     string   `json:"scope"`
	Desk      string   `json:"desk,omitempty"`
	Members   []string `json:"members,omitempty"` // channels, direct messages, Ledgy
	Seq       int64    `json:"seq"`
	LastAt    string   `json:"last_at,omitempty"`
	LastFrom  string   `json:"last_from,omitempty"`
	LastText  string   `json:"last_text,omitempty"`
	Archived  bool     `json:"archived,omitempty"`
	Created   string   `json:"created"`
	CreatedBy string   `json:"created_by"`
}

// ChatFileRef is a file attached to a message.
type ChatFileRef struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Type  string `json:"type"`
	Size  int64  `json:"size"`
	Image bool   `json:"image"`
}

// ChatLink points a message at a screen (Ledgy's notices).
type ChatLink struct {
	View  string `json:"view"`
	Label string `json:"label"`
}

// ChatMessage is one message.
type ChatMessage struct {
	Channel  string        `json:"channel"`
	Seq      int64         `json:"seq"`
	From     string        `json:"from"`
	Text     string        `json:"text"`
	Mentions []string      `json:"mentions,omitempty"`
	All      bool          `json:"all,omitempty"`
	Files    []ChatFileRef `json:"files,omitempty"`
	Link     *ChatLink     `json:"link,omitempty"`
	At       string        `json:"at"`
}

type chatFileDoc struct {
	ChatFileRef
	Channel string `json:"channel"`
	By      string `json:"by"`
	RawID   string `json:"raw_id"`
	At      string `json:"at"`
	Used    bool   `json:"used"`
}

// chatRead is one person's place in each conversation.
type chatRead struct {
	User     string           `json:"user"`
	Read     map[string]int64 `json:"read"`
	Mentions map[string]int   `json:"mentions"`
}

// ---- who is who ----

// chatSide is where a person sits: "ho", "branch:<code>" or "ap:<code>"
// ("" for clients and incomplete logins, who have no messages).
func chatSide(u User) string {
	switch u.Role {
	case RoleBranch:
		if u.Branch != "" {
			return "branch:" + u.Branch
		}
	case RoleAP:
		if u.Partner != "" {
			return "ap:" + u.Partner
		}
	case RoleClient, "":
	default:
		return chatSideHO
	}
	return ""
}

type chatEnv struct {
	o       *Ops
	ctx     context.Context
	tenant  string
	me      User
	side    string
	users   map[string]User // active users
	desks   map[string]ChatDesk
	parties map[string]string // scope -> display name
}

func (o *Ops) chatEnv(ctx context.Context, tenant, actor string) (*chatEnv, error) {
	us, err := o.Users(ctx, tenant)
	if err != nil {
		return nil, err
	}
	e := &chatEnv{o: o, ctx: ctx, tenant: tenant, users: map[string]User{}, desks: map[string]ChatDesk{}, parties: map[string]string{}}
	for _, u := range us {
		if u.Active {
			e.users[u.ID] = u
		}
	}
	if actor != LedgyActor && actor != SystemActor {
		me, ok := e.users[actor]
		if !ok {
			return nil, fmt.Errorf("%w: messages need an active staff login", ErrForbidden)
		}
		e.me, e.side = me, chatSide(me)
		if e.side == "" {
			return nil, fmt.Errorf("%w: messages are for staff, branches and authorised persons", ErrForbidden)
		}
	}
	if err := o.seedDesks(ctx, tenant, us); err != nil {
		return nil, err
	}
	ds, err := docs.List[ChatDesk](ctx, o.Docs, tenant, cChatDesk, "")
	if err != nil {
		return nil, err
	}
	for _, d := range ds {
		e.desks[d.ID] = d
	}
	// Every branch and AP with a login can be written to, named from the
	// branch and partner records where they exist.
	for _, u := range e.users {
		if sd := chatSide(u); sd != "" && sd != chatSideHO {
			e.parties[sd] = strings.SplitN(sd, ":", 2)[1]
		}
	}
	bs, _ := o.Branches(ctx, tenant)
	for _, b := range bs {
		e.parties["branch:"+b.Code] = firstOf(b.Name, b.Code+" branch")
	}
	ps, _ := docs.List[Partner](ctx, o.Docs, tenant, cPartner, "")
	for _, p := range ps {
		if p.Kind == "" || p.Kind == RoleAP || strings.EqualFold(p.Kind, "Authorised person") {
			e.parties["ap:"+p.Code] = firstOf(p.Name, p.Code) + " (AP " + p.Code + ")"
		}
	}
	for sd, n := range e.parties {
		if strings.HasPrefix(sd, "branch:") && n == strings.TrimPrefix(sd, "branch:") {
			e.parties[sd] = n + " branch"
		} else if strings.HasPrefix(sd, "ap:") && n == strings.TrimPrefix(sd, "ap:") {
			e.parties[sd] = "AP " + n
		}
	}
	return e, nil
}

// seedDesks creates the Risk, DP and Compliance desks the first time,
// staffed by the people in those roles; the Admin changes them later.
func (o *Ops) seedDesks(ctx context.Context, tenant string, us []User) error {
	ds, err := docs.List[ChatDesk](ctx, o.Docs, tenant, cChatDesk, "")
	if err != nil || len(ds) > 0 {
		return err
	}
	now := o.now().Format(time.RFC3339)
	for _, d := range []struct {
		id, name, about string
		roles           []string
	}{
		{"risk", "Risk desk", "Limits, margins, square-offs and exposure questions", []string{RoleRisk}},
		{"dp", "DP desk", "Demat accounts, DIS, pledges and transfers", []string{RoleMaker, RoleChecker}},
		{"compliance", "Compliance desk", "KYC, complaints, circulars and regulatory questions", []string{RoleCompliance}},
	} {
		var ms []string
		for _, u := range us {
			if u.Active && contains(d.roles, u.Role) {
				ms = append(ms, u.ID)
			}
		}
		desk := ChatDesk{ID: d.id, Name: d.name, About: d.about, Members: ms, Created: now, CreatedBy: SystemActor}
		if err := docs.Upsert(ctx, o.Docs, tenant, cChatDesk, d.id, desk, SystemActor); err != nil {
			return err
		}
	}
	return nil
}

func (e *chatEnv) name(id string) string {
	if id == LedgyActor {
		return "Ledgy"
	}
	if u, ok := e.users[id]; ok {
		return u.Name
	}
	return id
}

// deskStaff is who answers for a desk: its members, or the Admins when it
// has none, so nothing goes unread.
func (e *chatEnv) deskStaff(d ChatDesk) []string {
	var out []string
	for _, m := range d.Members {
		if u, ok := e.users[m]; ok && chatSide(u) == chatSideHO {
			out = append(out, m)
		}
	}
	if len(out) == 0 {
		for _, u := range e.users {
			if u.Role == RoleAdmin {
				out = append(out, u.ID)
			}
		}
		sort.Strings(out)
	}
	return out
}

// audience is everyone who can read a conversation.
func (e *chatEnv) audience(c ChatChannel) []string {
	var out []string
	switch c.Kind {
	case ChatThread:
		d, ok := e.desks[c.Desk]
		if !ok {
			return nil
		}
		out = append(out, e.deskStaff(d)...)
		var party []string
		for _, u := range e.users {
			if chatSide(u) == c.Scope {
				party = append(party, u.ID)
			}
		}
		sort.Strings(party)
		out = append(out, party...)
	default:
		for _, m := range c.Members {
			if u, ok := e.users[m]; ok && (c.Kind == ChatLedgy || chatSide(u) == c.Scope) {
				out = append(out, m)
			}
		}
	}
	return out
}

func (e *chatEnv) canSee(c ChatChannel) bool { return contains(e.audience(c), e.me.ID) }

func (e *chatEnv) canPost(c ChatChannel) bool {
	if c.Kind == ChatLedgy || c.Archived || !e.canSee(c) {
		return false
	}
	if c.Kind == ChatThread {
		d := e.desks[c.Desk]
		return !d.Archived
	}
	return true
}

func (e *chatEnv) onDesk(d ChatDesk) bool { return contains(e.deskStaff(d), e.me.ID) }

func (o *Ops) chatChannel(ctx context.Context, tenant, id string) (ChatChannel, error) {
	c, _, err := docs.Get[ChatChannel](ctx, o.Docs, tenant, cChatChan, id)
	if err != nil {
		return c, fmt.Errorf("%w: no such conversation", ErrNotFound)
	}
	return c, nil
}

func shortHash(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(h[:8])
}

// ---- views ----

// ChatPerson is someone in a conversation or to message.
type ChatPerson struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Role  string `json:"role"`
	Side  string `json:"side"`  // Head office, a branch or an AP
	Scope string `json:"scope"` // ho, branch:<code>, ap:<code>
}

// ChatConv is a conversation in the list.
type ChatConv struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Title    string `json:"title"`
	Sub      string `json:"sub,omitempty"`
	Desk     string `json:"desk,omitempty"`
	Scope    string `json:"scope"`
	Unread   int64  `json:"unread"`
	Mentions int    `json:"mentions"`
	LastAt   string `json:"last_at,omitempty"`
	LastFrom string `json:"last_from,omitempty"`
	LastText string `json:"last_text,omitempty"`
	Archived bool   `json:"archived,omitempty"`
}

// ChatDeskView is a desk the person can write to, or answers for.
type ChatDeskView struct {
	ID      string       `json:"id"`
	Name    string       `json:"name"`
	About   string       `json:"about,omitempty"`
	Staff   []ChatPerson `json:"staff"`
	OnDesk  bool         `json:"on_desk"`
	Thread  string       `json:"thread,omitempty"` // a branch or AP's own thread with it
	Members []string     `json:"members,omitempty"`
}

// ChatParty is a branch or AP a desk can write to.
type ChatParty struct {
	Scope string `json:"scope"`
	Name  string `json:"name"`
}

// ChatHome is the messages screen.
type ChatHome struct {
	Me            ChatPerson     `json:"me"`
	Conversations []ChatConv     `json:"conversations"`
	Desks         []ChatDeskView `json:"desks"`
	Parties       []ChatParty    `json:"parties,omitempty"` // for desk staff
	People        []ChatPerson   `json:"people"`            // to message directly
	Manage        *ChatManage    `json:"manage,omitempty"`  // Admins
	Unread        int64          `json:"unread"`
	Mentions      int            `json:"mentions"`
}

// ChatManage is what an Admin manages: every channel and desk (with
// members, not messages) and the people who can be added.
type ChatManage struct {
	Channels []ChatChannel `json:"channels"`
	Desks    []ChatDesk    `json:"desks"`
	Staff    []ChatPerson  `json:"staff"`
	Scopes   []ChatParty   `json:"scopes"` // head office and each branch
}

func (e *chatEnv) person(id string) ChatPerson {
	u := e.users[id]
	return ChatPerson{ID: id, Name: e.name(id), Role: u.Role, Side: e.sideName(chatSide(u)), Scope: chatSide(u)}
}

func (e *chatEnv) sideName(s string) string {
	switch {
	case s == chatSideHO:
		return "Head office"
	case s == "":
		return ""
	}
	if n, ok := e.parties[s]; ok {
		return n
	}
	return strings.SplitN(s, ":", 2)[1]
}

func (e *chatEnv) title(c ChatChannel) (string, string) {
	switch c.Kind {
	case ChatDM:
		for _, m := range c.Members {
			if m != e.me.ID {
				p := e.person(m)
				return p.Name, p.Role
			}
		}
		return "Note to self", ""
	case ChatThread:
		d := e.desks[c.Desk]
		if e.side == c.Scope {
			return d.Name, "Private to " + e.sideName(c.Scope) + " and the desk"
		}
		return e.sideName(c.Scope), d.Name
	case ChatLedgy:
		return "Ledgy", "New work for you"
	}
	sub := "Head office"
	if c.Scope != chatSideHO {
		sub = e.sideName(c.Scope)
	}
	return "#" + c.Name, sub + fmt.Sprintf(" · %d members", len(e.audience(c)))
}

func (o *Ops) chatReads(ctx context.Context, tenant, user string) chatRead {
	r, _, _ := docs.GetOr(ctx, o.Docs, tenant, cChatRead, user, chatRead{User: user})
	if r.Read == nil {
		r.Read = map[string]int64{}
	}
	if r.Mentions == nil {
		r.Mentions = map[string]int{}
	}
	return r
}

func (e *chatEnv) visible() ([]ChatChannel, error) {
	cs, err := docs.List[ChatChannel](e.ctx, e.o.Docs, e.tenant, cChatChan, "")
	if err != nil {
		return nil, err
	}
	var out []ChatChannel
	for _, c := range cs {
		if e.canSee(c) {
			out = append(out, c)
		}
	}
	return out, nil
}

// ChatHome lists the person's conversations with unread counts, the desks
// they can write to, and who they can message.
func (o *Ops) ChatHome(ctx context.Context, tenant, actor string) (ChatHome, error) {
	e, err := o.chatEnv(ctx, tenant, actor)
	if err != nil {
		return ChatHome{}, err
	}
	cs, err := e.visible()
	if err != nil {
		return ChatHome{}, err
	}
	rd := o.chatReads(ctx, tenant, actor)
	h := ChatHome{Me: e.person(actor), Conversations: []ChatConv{}, Desks: []ChatDeskView{}, People: []ChatPerson{}}
	threads := map[string]string{}
	for _, c := range cs {
		if c.Kind == ChatThread && c.Scope == e.side {
			threads[c.Desk] = c.ID
		}
		if c.Kind != ChatKindChannel && c.Seq == 0 && c.CreatedBy != actor {
			continue // an empty thread or direct message someone else opened
		}
		t, sub := e.title(c)
		cv := ChatConv{ID: c.ID, Kind: c.Kind, Title: t, Sub: sub, Desk: c.Desk, Scope: c.Scope, LastAt: c.LastAt, LastText: c.LastText, Archived: c.Archived}
		if c.LastFrom != "" {
			cv.LastFrom = e.name(c.LastFrom)
		}
		if u := c.Seq - rd.Read[c.ID]; u > 0 {
			cv.Unread = u
			h.Unread += u
		}
		cv.Mentions = rd.Mentions[c.ID]
		if cv.Unread == 0 {
			cv.Mentions = 0
		}
		h.Mentions += cv.Mentions
		h.Conversations = append(h.Conversations, cv)
	}
	sort.Slice(h.Conversations, func(i, j int) bool {
		a, b := h.Conversations[i], h.Conversations[j]
		if a.LastAt != b.LastAt {
			return a.LastAt > b.LastAt
		}
		return a.Title < b.Title
	})
	var dids []string
	for id := range e.desks {
		dids = append(dids, id)
	}
	sort.Strings(dids)
	onAny := false
	for _, id := range dids {
		d := e.desks[id]
		if d.Archived {
			continue
		}
		on := e.onDesk(d)
		if e.side == chatSideHO && !on {
			continue // head office staff use their own desks; others talk directly
		}
		onAny = onAny || on
		v := ChatDeskView{ID: d.ID, Name: d.Name, About: d.About, OnDesk: on, Thread: threads[d.ID]}
		for _, m := range e.deskStaff(d) {
			v.Staff = append(v.Staff, e.person(m))
		}
		h.Desks = append(h.Desks, v)
	}
	if onAny {
		for s, n := range e.parties {
			h.Parties = append(h.Parties, ChatParty{s, n})
		}
		sort.Slice(h.Parties, func(i, j int) bool { return h.Parties[i].Name < h.Parties[j].Name })
	}
	if e.side == chatSideHO || strings.HasPrefix(e.side, "branch:") {
		for id, u := range e.users {
			if id != actor && chatSide(u) == e.side {
				h.People = append(h.People, e.person(id))
			}
		}
		sort.Slice(h.People, func(i, j int) bool { return h.People[i].Name < h.People[j].Name })
	}
	if e.me.Role == RoleAdmin {
		m := &ChatManage{Channels: []ChatChannel{}, Desks: []ChatDesk{}, Staff: []ChatPerson{}, Scopes: []ChatParty{{chatSideHO, "Head office"}}}
		all, _ := docs.List[ChatChannel](ctx, o.Docs, tenant, cChatChan, "")
		for _, c := range all {
			if c.Kind == ChatKindChannel {
				c.LastText, c.LastFrom = "", "" // Admins manage channels; they read only those they are in
				m.Channels = append(m.Channels, c)
			}
		}
		sort.Slice(m.Channels, func(i, j int) bool {
			return m.Channels[i].Scope+m.Channels[i].Name < m.Channels[j].Scope+m.Channels[j].Name
		})
		for _, id := range dids {
			m.Desks = append(m.Desks, e.desks[id])
		}
		for id, u := range e.users {
			if s := chatSide(u); s == chatSideHO || strings.HasPrefix(s, "branch:") {
				m.Staff = append(m.Staff, e.person(id))
			}
		}
		sort.Slice(m.Staff, func(i, j int) bool { return m.Staff[i].Side+m.Staff[i].Name < m.Staff[j].Side+m.Staff[j].Name })
		bs, _ := o.Branches(ctx, tenant)
		for _, b := range bs {
			m.Scopes = append(m.Scopes, ChatParty{"branch:" + b.Code, e.parties["branch:"+b.Code]})
		}
		h.Manage = m
	}
	return h, nil
}

// ChatView is one conversation's messages.
type ChatView struct {
	Channel  ChatConv          `json:"channel"`
	About    string            `json:"about,omitempty"`
	Messages []ChatMessage     `json:"messages"`
	More     bool              `json:"more"` // older messages exist
	People   []ChatPerson      `json:"people"`
	CanPost  bool              `json:"can_post"`
	CanAll   bool              `json:"can_all"` // @all is allowed here
	Names    map[string]string `json:"names"`
}

func msgKey(ch string, seq int64) string {
	return fmt.Sprintf("%s|%06d|%012d", ch, seq/chatBucket, seq)
}

// messagesBefore returns up to n messages with seq < before (0: the latest).
func (o *Ops) messagesBefore(ctx context.Context, tenant string, c ChatChannel, before int64, n int) ([]ChatMessage, bool, error) {
	top := c.Seq
	if before > 0 && before-1 < top {
		top = before - 1
	}
	var out []ChatMessage
	for b := top / chatBucket; b >= 0 && len(out) < n; b-- {
		ms, err := docs.List[ChatMessage](ctx, o.Docs, tenant, cChatMsg, fmt.Sprintf("%s|%06d|", c.ID, b))
		if err != nil {
			return nil, false, err
		}
		for _, m := range ms {
			if m.Seq <= top {
				out = append(out, m)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	more := false
	if len(out) > n {
		out, more = out[len(out)-n:], true
	}
	if len(out) > 0 && out[0].Seq > 1 {
		more = true
	}
	return out, more, nil
}

// ChatMessages opens a conversation: its latest messages (or those before
// a point, to scroll back), who is in it, and whether the person may write.
func (o *Ops) ChatMessages(ctx context.Context, tenant, actor, id string, before int64) (ChatView, error) {
	e, err := o.chatEnv(ctx, tenant, actor)
	if err != nil {
		return ChatView{}, err
	}
	c, err := o.chatChannel(ctx, tenant, id)
	if err != nil || !e.canSee(c) {
		return ChatView{}, fmt.Errorf("%w: no such conversation", ErrNotFound)
	}
	ms, more, err := o.messagesBefore(ctx, tenant, c, before, chatPage)
	if err != nil {
		return ChatView{}, err
	}
	t, sub := e.title(c)
	v := ChatView{Channel: ChatConv{ID: c.ID, Kind: c.Kind, Title: t, Sub: sub, Desk: c.Desk, Scope: c.Scope, Archived: c.Archived}, About: c.About,
		Messages: ms, More: more, People: []ChatPerson{}, CanPost: e.canPost(c), CanAll: c.Kind == ChatKindChannel || c.Kind == ChatThread, Names: map[string]string{}}
	if c.Kind == ChatThread {
		v.About = e.desks[c.Desk].About
	}
	for _, p := range e.audience(c) {
		v.People = append(v.People, e.person(p))
		v.Names[p] = e.name(p)
	}
	v.Names[LedgyActor] = "Ledgy"
	for _, m := range ms {
		if _, ok := v.Names[m.From]; !ok {
			v.Names[m.From] = e.name(m.From) // someone who has since left
		}
	}
	if ms == nil {
		v.Messages = []ChatMessage{}
	}
	return v, nil
}

// ---- writing ----

var reMention = regexp.MustCompile(`(^|[^A-Za-z0-9._-])@([a-z0-9][a-z0-9._-]*[a-z0-9]|[a-z0-9])`)

// mentionsIn finds @user and @all.
func mentionsIn(text string, audience []string, allowAll bool) ([]string, bool) {
	var out []string
	all := false
	for _, m := range reMention.FindAllStringSubmatch(text, -1) {
		id := m[2]
		switch {
		case id == "all" && allowAll:
			all = true
		case contains(audience, id) && !contains(out, id):
			out = append(out, id)
		}
	}
	return out, all
}

// PostChat writes a message, with files uploaded for it, and notifies
// everyone in the conversation.
func (o *Ops) PostChat(ctx context.Context, tenant, actor, id, text string, files []string) (ChatMessage, error) {
	e, err := o.chatEnv(ctx, tenant, actor)
	if err != nil {
		return ChatMessage{}, err
	}
	c, err := o.chatChannel(ctx, tenant, id)
	if err != nil || !e.canSee(c) {
		return ChatMessage{}, fmt.Errorf("%w: no such conversation", ErrNotFound)
	}
	if !e.canPost(c) {
		if c.Archived {
			return ChatMessage{}, invalid("this channel is archived: it can be read but not written to")
		}
		return ChatMessage{}, fmt.Errorf("%w: you cannot write here", ErrForbidden)
	}
	text = strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	if utf8.RuneCountInString(text) > chatMaxText {
		return ChatMessage{}, invalid("a message can be at most %d characters", chatMaxText)
	}
	if text == "" && len(files) == 0 {
		return ChatMessage{}, invalid("write a message or attach a file")
	}
	if len(files) > chatMaxFiles {
		return ChatMessage{}, invalid("attach at most %d files to a message", chatMaxFiles)
	}
	var refs []ChatFileRef
	for _, fid := range files {
		f, _, err := docs.Get[chatFileDoc](ctx, o.Docs, tenant, cChatFile, fid)
		if err != nil || f.By != actor || f.Channel != id || f.Used {
			return ChatMessage{}, invalid("an attachment is missing; upload it again")
		}
		refs = append(refs, f.ChatFileRef)
	}
	for _, fid := range files {
		_, _ = docs.Update(ctx, o.Docs, tenant, cChatFile, fid, actor, func(f *chatFileDoc) error { f.Used = true; return nil })
	}
	return o.postChat(ctx, e, c, actor, text, refs, nil)
}

func (o *Ops) postChat(ctx context.Context, e *chatEnv, c ChatChannel, from, text string, files []ChatFileRef, link *ChatLink) (ChatMessage, error) {
	aud := e.audience(c)
	ments, all := mentionsIn(text, aud, c.Kind == ChatKindChannel || c.Kind == ChatThread)
	if c.Kind == ChatLedgy {
		ments = c.Members // Ledgy's notices always count as mentions
	}
	now := o.now().Format(chatTime)
	preview := text
	if preview == "" && len(files) > 0 {
		preview = "📎 " + files[0].Name
		if len(files) > 1 {
			preview += fmt.Sprintf(" and %d more", len(files)-1)
		}
	}
	if r := []rune(preview); len(r) > 140 {
		preview = string(r[:140]) + "…"
	}
	c, err := docs.Update(ctx, o.Docs, e.tenant, cChatChan, c.ID, from, func(x *ChatChannel) error {
		x.Seq++
		x.LastAt, x.LastFrom, x.LastText = now, from, preview
		return nil
	})
	if err != nil {
		return ChatMessage{}, err
	}
	m := ChatMessage{Channel: c.ID, Seq: c.Seq, From: from, Text: text, Mentions: ments, All: all, Files: files, Link: link, At: now}
	if err := docs.Upsert(ctx, o.Docs, e.tenant, cChatMsg, msgKey(c.ID, c.Seq), m, from); err != nil {
		return ChatMessage{}, err
	}
	notify := ments
	if all {
		notify = aud
	}
	for _, u := range notify {
		if u == from {
			continue
		}
		_, _ = upd(ctx, o.Docs, e.tenant, cChatRead, u, from, &chatRead{User: u}, func(r *chatRead) error {
			if r.Mentions == nil {
				r.Mentions = map[string]int{}
			}
			r.Mentions[c.ID]++
			return nil
		})
	}
	if from != LedgyActor {
		_ = o.markRead(ctx, e.tenant, from, c.ID, c.Seq)
	}
	o.log(ctx, e.tenant, from, "chat.message", c.ID, map[string]any{"seq": c.Seq, "kind": c.Kind, "files": len(files), "mentions": len(ments), "all": all})
	return m, nil
}

func (o *Ops) markRead(ctx context.Context, tenant, user, ch string, seq int64) error {
	_, err := upd(ctx, o.Docs, tenant, cChatRead, user, user, &chatRead{User: user}, func(r *chatRead) error {
		if r.Read == nil {
			r.Read = map[string]int64{}
		}
		if seq > r.Read[ch] {
			r.Read[ch] = seq
		}
		if r.Mentions != nil {
			delete(r.Mentions, ch)
		}
		return nil
	})
	return err
}

// MarkChatRead records that the person has read a conversation up to seq.
func (o *Ops) MarkChatRead(ctx context.Context, tenant, actor, id string, seq int64) error {
	e, err := o.chatEnv(ctx, tenant, actor)
	if err != nil {
		return err
	}
	c, err := o.chatChannel(ctx, tenant, id)
	if err != nil || !e.canSee(c) {
		return fmt.Errorf("%w: no such conversation", ErrNotFound)
	}
	if seq <= 0 || seq > c.Seq {
		seq = c.Seq
	}
	return o.markRead(ctx, tenant, actor, id, seq)
}

// ---- starting conversations ----

// OpenDM opens (or creates) a direct conversation with someone on the same
// side: head office with head office, or within one branch.
func (o *Ops) OpenDM(ctx context.Context, tenant, actor, other string) (ChatChannel, error) {
	e, err := o.chatEnv(ctx, tenant, actor)
	if err != nil {
		return ChatChannel{}, err
	}
	u, ok := e.users[other]
	switch {
	case !ok:
		return ChatChannel{}, invalid("no such person")
	case e.side != chatSideHO && !strings.HasPrefix(e.side, "branch:"):
		return ChatChannel{}, fmt.Errorf("%w: authorised persons write to head office through its desks", ErrForbidden)
	case chatSide(u) != e.side:
		return ChatChannel{}, fmt.Errorf("%w: direct messages are within head office or within one branch; use a desk to reach the other side", ErrForbidden)
	}
	ms := []string{actor, other}
	sort.Strings(ms)
	id := "D-" + shortHash(ms...)
	c, err := o.chatChannel(ctx, tenant, id)
	if err == nil {
		return c, nil
	}
	c = ChatChannel{ID: id, Kind: ChatDM, Scope: e.side, Members: ms, Created: o.now().Format(time.RFC3339), CreatedBy: actor}
	if err := docs.Upsert(ctx, o.Docs, tenant, cChatChan, id, c, actor); err != nil {
		return c, err
	}
	o.log(ctx, tenant, actor, "chat.dm", id, map[string]any{"with": other})
	return c, nil
}

// OpenThread opens (or creates) the private thread between a desk and one
// branch or AP. Branch staff and APs open their own; desk staff open any.
func (o *Ops) OpenThread(ctx context.Context, tenant, actor, desk, party string) (ChatChannel, error) {
	e, err := o.chatEnv(ctx, tenant, actor)
	if err != nil {
		return ChatChannel{}, err
	}
	d, ok := e.desks[desk]
	if !ok || d.Archived {
		return ChatChannel{}, invalid("no such desk")
	}
	if e.side != chatSideHO {
		party = e.side // a branch or AP writes only as itself
	} else if !e.onDesk(d) {
		return ChatChannel{}, fmt.Errorf("%w: only %s staff write to branches and APs as the desk", ErrForbidden, d.Name)
	}
	if _, ok := e.parties[party]; !ok {
		return ChatChannel{}, invalid("choose a branch or authorised person")
	}
	id := "T-" + desk + "-" + shortHash(party)
	c, err := o.chatChannel(ctx, tenant, id)
	if err == nil {
		return c, nil
	}
	c = ChatChannel{ID: id, Kind: ChatThread, Desk: desk, Scope: party, Created: o.now().Format(time.RFC3339), CreatedBy: actor}
	if err := docs.Upsert(ctx, o.Docs, tenant, cChatChan, id, c, actor); err != nil {
		return c, err
	}
	o.log(ctx, tenant, actor, "chat.thread", id, map[string]any{"desk": desk, "party": party})
	return c, nil
}

// ---- Admin: channels and desks ----

// ChatChannelInput is a channel as the Admin sets it.
type ChatChannelInput struct {
	Name     string   `json:"name"`
	About    string   `json:"about"`
	Scope    string   `json:"scope"` // "ho" or "branch:<code>"
	Members  []string `json:"members"`
	Archived bool     `json:"archived"`
}

var reChanName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

func (e *chatEnv) checkMembers(scope string, ms []string) ([]string, error) {
	var out []string
	for _, m := range ms {
		u, ok := e.users[m]
		if !ok {
			return nil, invalid("%s is not an active user", m)
		}
		if chatSide(u) != scope {
			return nil, invalid("%s (%s) cannot join: %s channels are for %s staff only", u.Name, e.sideName(chatSide(u)), e.sideName(scope), e.sideName(scope))
		}
		if !contains(out, m) {
			out = append(out, m)
		}
	}
	if len(out) > chatMaxMember {
		return nil, invalid("a channel can have at most %d members", chatMaxMember)
	}
	sort.Strings(out)
	return out, nil
}

// SaveChatChannel creates (id empty) or changes a channel. Only the Admin
// does this, and only the members chosen can read it.
func (o *Ops) SaveChatChannel(ctx context.Context, tenant, actor, id string, in ChatChannelInput) (ChatChannel, error) {
	if err := o.require(ctx, tenant, actor, "Managing message channels and desks"); err != nil {
		return ChatChannel{}, err
	}
	e, err := o.chatEnv(ctx, tenant, actor)
	if err != nil {
		return ChatChannel{}, err
	}
	in.Name = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(in.Name, "#")))
	in.Name = strings.Join(strings.Fields(in.Name), "-")
	if !reChanName.MatchString(in.Name) {
		return ChatChannel{}, invalid("channel names are lower-case letters, digits and dashes, up to 40")
	}
	if in.Scope != chatSideHO {
		if _, ok := e.parties[in.Scope]; !ok || !strings.HasPrefix(in.Scope, "branch:") {
			return ChatChannel{}, invalid("a channel belongs to head office or one branch")
		}
	}
	ms, err := e.checkMembers(in.Scope, in.Members)
	if err != nil {
		return ChatChannel{}, err
	}
	all, _ := docs.List[ChatChannel](ctx, o.Docs, tenant, cChatChan, "")
	for _, c := range all {
		if c.Kind == ChatKindChannel && c.ID != id && c.Scope == in.Scope && c.Name == in.Name {
			return ChatChannel{}, invalid("#%s already exists there", in.Name)
		}
	}
	now := o.now().Format(time.RFC3339)
	var c ChatChannel
	if id == "" {
		n, err := o.next(ctx, tenant, "chatchan", actor)
		if err != nil {
			return c, err
		}
		c = ChatChannel{ID: fmt.Sprintf("C-%04d", n), Kind: ChatKindChannel, Name: in.Name, About: strings.TrimSpace(in.About), Scope: in.Scope, Members: ms, Created: now, CreatedBy: actor}
		if err := docs.Upsert(ctx, o.Docs, tenant, cChatChan, c.ID, c, actor); err != nil {
			return c, err
		}
		o.log(ctx, tenant, actor, "chat.channel.create", c.ID, map[string]any{"name": c.Name, "scope": c.Scope, "members": ms})
		return c, nil
	}
	old, err := o.chatChannel(ctx, tenant, id)
	if err != nil || old.Kind != ChatKindChannel {
		return c, fmt.Errorf("%w: no such channel", ErrNotFound)
	}
	if old.Scope != in.Scope {
		return c, invalid("a channel cannot move between head office and a branch; make a new one")
	}
	c, err = docs.Update(ctx, o.Docs, tenant, cChatChan, id, actor, func(x *ChatChannel) error {
		x.Name, x.About, x.Members, x.Archived = in.Name, strings.TrimSpace(in.About), ms, in.Archived
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "chat.channel.update", id, map[string]any{"name": c.Name, "members": ms, "archived": c.Archived, "was": old.Members})
	}
	return c, err
}

// ChatDeskInput is a desk as the Admin sets it.
type ChatDeskInput struct {
	Name     string   `json:"name"`
	About    string   `json:"about"`
	Members  []string `json:"members"`
	Archived bool     `json:"archived"`
}

// SaveChatDesk creates (id empty) or changes an official desk.
func (o *Ops) SaveChatDesk(ctx context.Context, tenant, actor, id string, in ChatDeskInput) (ChatDesk, error) {
	if err := o.require(ctx, tenant, actor, "Managing message channels and desks"); err != nil {
		return ChatDesk{}, err
	}
	e, err := o.chatEnv(ctx, tenant, actor)
	if err != nil {
		return ChatDesk{}, err
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 40 {
		return ChatDesk{}, invalid("give the desk a name (up to 40 characters)")
	}
	ms, err := e.checkMembers(chatSideHO, in.Members)
	if err != nil {
		return ChatDesk{}, err
	}
	if id == "" {
		slug := strings.Trim(regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(strings.ToLower(in.Name), "-"), "-")
		if slug == "" {
			slug = "desk"
		}
		id = slug
		for i := 2; ; i++ {
			if _, ok := e.desks[id]; !ok {
				break
			}
			id = fmt.Sprintf("%s-%d", slug, i)
		}
		d := ChatDesk{ID: id, Name: in.Name, About: strings.TrimSpace(in.About), Members: ms, Created: o.now().Format(time.RFC3339), CreatedBy: actor}
		if err := docs.Upsert(ctx, o.Docs, tenant, cChatDesk, id, d, actor); err != nil {
			return d, err
		}
		o.log(ctx, tenant, actor, "chat.desk.create", id, map[string]any{"name": d.Name, "members": ms})
		return d, nil
	}
	old, ok := e.desks[id]
	if !ok {
		return ChatDesk{}, fmt.Errorf("%w: no such desk", ErrNotFound)
	}
	d, err := docs.Update(ctx, o.Docs, tenant, cChatDesk, id, actor, func(x *ChatDesk) error {
		x.Name, x.About, x.Members, x.Archived = in.Name, strings.TrimSpace(in.About), ms, in.Archived
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "chat.desk.update", id, map[string]any{"name": d.Name, "members": ms, "archived": d.Archived, "was": old.Members})
	}
	return d, err
}

// ---- files and photographs ----

var chatFileTypes = map[string]string{
	".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".png": "image/png", ".gif": "image/gif", ".webp": "image/webp", ".heic": "image/heic",
	".pdf": "application/pdf", ".txt": "text/plain", ".csv": "text/csv",
	".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", ".xls": "application/vnd.ms-excel",
	".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document", ".doc": "application/msword",
	".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation", ".zip": "application/zip",
}

// chatFileType checks a file's content matches its name, and refuses
// anything a browser could run (HTML, SVG, scripts, programs).
func chatFileType(name string, data []byte) (string, bool, error) {
	ext := strings.ToLower(path.Ext(name))
	want, ok := chatFileTypes[ext]
	if !ok {
		return "", false, invalid("%s files cannot be shared; send photos, PDF, Excel, Word, PowerPoint, CSV, text or ZIP", firstOf(ext, "these"))
	}
	sniff := http.DetectContentType(data)
	match := false
	switch {
	case strings.HasPrefix(want, "image/") && ext != ".heic":
		match = sniff == want
	case ext == ".heic":
		match = len(data) > 12 && string(data[4:8]) == "ftyp"
	case want == "application/pdf":
		match = sniff == "application/pdf"
	case ext == ".txt" || ext == ".csv":
		match = strings.HasPrefix(sniff, "text/plain")
	case ext == ".xls" || ext == ".doc":
		match = sniff == "application/octet-stream" && len(data) > 8 && hex.EncodeToString(data[:8]) == "d0cf11e0a1b11ae1"
	default: // Office Open XML and ZIP are ZIP files
		match = sniff == "application/zip" || sniff == "application/x-zip-compressed"
	}
	if !match {
		return "", false, invalid("%s is not a real %s file", name, strings.TrimPrefix(ext, "."))
	}
	img := strings.HasPrefix(want, "image/") && ext != ".heic"
	return want, img, nil
}

// UploadChatFile stores a file for the person's next message in a
// conversation.
func (o *Ops) UploadChatFile(ctx context.Context, tenant, actor, id, name string, data []byte) (ChatFileRef, error) {
	e, err := o.chatEnv(ctx, tenant, actor)
	if err != nil {
		return ChatFileRef{}, err
	}
	c, err := o.chatChannel(ctx, tenant, id)
	if err != nil || !e.canPost(c) {
		return ChatFileRef{}, fmt.Errorf("%w: you cannot send files here", ErrForbidden)
	}
	name = strings.TrimSpace(path.Base(strings.ReplaceAll(name, "\\", "/")))
	if name == "" || name == "." || len(name) > 120 {
		return ChatFileRef{}, invalid("the file needs a name of up to 120 characters")
	}
	if len(data) == 0 || len(data) > chatMaxFile {
		return ChatFileRef{}, invalid("files can be up to %d MB", chatMaxFile>>20)
	}
	ct, img, err := chatFileType(name, data)
	if err != nil {
		return ChatFileRef{}, err
	}
	sum := sha256.Sum256(data)
	rf, err := o.App.Ingest.Raw.Put(ctx, ingest.RawFile{Tenant: tenant, Source: "CHAT", Name: id + " " + name, SHA256: hex.EncodeToString(sum[:]), Content: data})
	if err != nil {
		return ChatFileRef{}, err
	}
	b := make([]byte, 9)
	_, _ = rand.Read(b)
	f := chatFileDoc{ChatFileRef: ChatFileRef{ID: "F-" + hex.EncodeToString(b), Name: name, Type: ct, Size: int64(len(data)), Image: img}, Channel: id, By: actor, RawID: rf.ID, At: o.now().Format(time.RFC3339)}
	if err := docs.Upsert(ctx, o.Docs, tenant, cChatFile, f.ID, f, actor); err != nil {
		return ChatFileRef{}, err
	}
	return f.ChatFileRef, nil
}

// ChatFile returns a file to someone who can read its conversation.
func (o *Ops) ChatFile(ctx context.Context, tenant, actor, fid string) (ChatFileRef, []byte, error) {
	e, err := o.chatEnv(ctx, tenant, actor)
	if err != nil {
		return ChatFileRef{}, nil, err
	}
	f, _, err := docs.Get[chatFileDoc](ctx, o.Docs, tenant, cChatFile, fid)
	if err != nil {
		return ChatFileRef{}, nil, fmt.Errorf("%w: no such file", ErrNotFound)
	}
	c, err := o.chatChannel(ctx, tenant, f.Channel)
	if err != nil || !e.canSee(c) || (!f.Used && f.By != actor) {
		return ChatFileRef{}, nil, fmt.Errorf("%w: no such file", ErrNotFound)
	}
	rf, err := o.App.Ingest.Raw.Get(ctx, tenant, f.RawID)
	if err != nil {
		return ChatFileRef{}, nil, err
	}
	return f.ChatFileRef, rf.Content, nil
}

// ---- notifications ----

// ChatPulseItem is a new message for the person, to show as it arrives.
type ChatPulseItem struct {
	Channel string `json:"channel"`
	Title   string `json:"title"`
	Seq     int64  `json:"seq"`
	From    string `json:"from"`
	Text    string `json:"text"`
	Mention bool   `json:"mention"`
	Ledgy   bool   `json:"ledgy,omitempty"`
	At      string `json:"at"`
}

// ChatPulse is what the console polls: unread totals, and the messages
// that arrived since the last poll (for notifications).
type ChatPulse struct {
	Unread   int64           `json:"unread"`
	Mentions int             `json:"mentions"`
	Items    []ChatPulseItem `json:"items"`
	Now      string          `json:"now"`
}

// ChatPulse returns unread counts and the messages since `since` (an
// RFC 3339 time from the previous pulse; empty for counts only).
func (o *Ops) ChatPulse(ctx context.Context, tenant, actor, since string) (ChatPulse, error) {
	p := ChatPulse{Items: []ChatPulseItem{}, Now: o.now().Format(chatTime)}
	e, err := o.chatEnv(ctx, tenant, actor)
	if err != nil {
		return p, err
	}
	cs, err := e.visible()
	if err != nil {
		return p, err
	}
	rd := o.chatReads(ctx, tenant, actor)
	st, sinceOK := time.Parse(time.RFC3339, since)
	for _, c := range cs {
		u := c.Seq - rd.Read[c.ID]
		if u <= 0 {
			continue
		}
		p.Unread += u
		p.Mentions += rd.Mentions[c.ID]
		if la, err := time.Parse(time.RFC3339, c.LastAt); sinceOK != nil || err != nil || !la.After(st) {
			continue
		}
		n := u
		if n > 5 {
			n = 5
		}
		ms, _, _ := o.messagesBefore(ctx, tenant, c, 0, int(n))
		title, _ := e.title(c)
		for _, m := range ms {
			at, err := time.Parse(time.RFC3339, m.At)
			if err != nil || !at.After(st) || m.From == actor || m.Seq <= rd.Read[c.ID] {
				continue
			}
			txt := m.Text
			if txt == "" && len(m.Files) > 0 {
				txt = "📎 " + m.Files[0].Name
			}
			if r := []rune(txt); len(r) > 160 {
				txt = string(r[:160]) + "…"
			}
			p.Items = append(p.Items, ChatPulseItem{Channel: c.ID, Title: title, Seq: m.Seq, From: e.name(m.From), Text: txt,
				Mention: m.All || contains(m.Mentions, actor), Ledgy: m.From == LedgyActor, At: m.At})
		}
	}
	sort.Slice(p.Items, func(i, j int) bool { return p.Items[i].At < p.Items[j].At })
	if len(p.Items) > chatPulseMax {
		p.Items = p.Items[len(p.Items)-chatPulseMax:]
	}
	return p, nil
}

// ---- Ledgy's notices ----

// ledgyNotifyWork tells the people who should act on a new work-queue item: those
// in its role (for branch staff, of the client's branch), or the Admins.
func (o *Ops) ledgyNotifyWork(ctx context.Context, tenant string, w WorkItem) {
	e, err := o.chatEnv(ctx, tenant, LedgyActor)
	if err != nil {
		return
	}
	branch := ""
	if w.Role == RoleBranch && w.UCC != "" {
		if c, err := o.Client(ctx, tenant, w.UCC); err == nil {
			branch = c.Branch
		}
	}
	var to []string
	for id, u := range e.users {
		if u.Role == w.Role && (branch == "" || u.Branch == branch) && chatSide(u) != "" {
			to = append(to, id)
		}
	}
	if len(to) == 0 {
		for id, u := range e.users {
			if u.Role == RoleAdmin {
				to = append(to, id)
			}
		}
	}
	sort.Strings(to)
	due := ""
	if w.Due != "" {
		due = " Due " + w.Due + "."
	}
	text := fmt.Sprintf("You have new work: %s.%s", strings.TrimSuffix(w.Title, "."), due)
	if d := strings.TrimSpace(w.Detail); d != "" {
		if r := []rune(d); len(r) > 300 {
			d = string(r[:300]) + "…"
		}
		text += "\n" + d
	}
	view := firstOf(w.View, "autopilot")
	for _, u := range to {
		id := "L-" + shortHash(u)
		c, err := o.chatChannel(ctx, tenant, id)
		if err != nil {
			c = ChatChannel{ID: id, Kind: ChatLedgy, Scope: chatSide(e.users[u]), Members: []string{u}, Created: o.now().Format(time.RFC3339), CreatedBy: LedgyActor}
			if err := docs.Upsert(ctx, o.Docs, tenant, cChatChan, id, c, LedgyActor); err != nil {
				continue
			}
		}
		_, _ = o.postChat(ctx, e, c, LedgyActor, text, nil, &ChatLink{View: view, Label: "Open " + chatViewName(view)})
	}
}

func chatViewName(v string) string {
	if n, ok := map[string]string{"autopilot": "the work queue", "apps": "Online applications", "clients": "Clients & onboarding", "recon": "Trade reconciliation",
		"rms": "RMS & limits", "money": "Receipts & payouts", "kyc": "KYC & PMLA", "surv": "Surveillance & AML", "reports": "Regulatory reports",
		"settlement": "Settlement", "billing": "Billing", "bank": "Bank reconciliation", "notes": "Contract notes", "collateral": "Collateral & pledge"}[v]; ok {
		return n
	}
	return "the screen"
}
