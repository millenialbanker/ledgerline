package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/control"
	"ledgerline/internal/docs"
)

// ---- support requests to the provider ----
//
// Staff write to Ledgerline's support team from inside Ledgerline. A request
// carries the firm, the person, their role, the screen and the version, and
// goes to the provider's Ledgerline Control with the next report (at once,
// when Control is connected). Replies and status come back the same way, and
// Ledgy tells the person.

const cSupport = "supportreq"

// SupportMsg is one message in a request.
type SupportMsg struct {
	ID     string `json:"id"`
	From   string `json:"from"` // firm, support
	Who    string `json:"who"`
	Text   string `json:"text"`
	At     string `json:"at"`
	Synced bool   `json:"synced,omitempty"`
}

// SupportRequest is a request to Ledgerline's support team.
type SupportRequest struct {
	ID       string       `json:"id"`
	User     string       `json:"user"`
	UserName string       `json:"user_name"`
	Role     string       `json:"role"`
	Subject  string       `json:"subject"`
	Priority string       `json:"priority"` // Low, Normal, High, Urgent
	Category string       `json:"category"`
	Screen   string       `json:"screen,omitempty"`
	Status   string       `json:"status"` // Sent, Open, Waiting on you, Resolved, Closed
	Ticket   string       `json:"ticket,omitempty"`
	Messages []SupportMsg `json:"messages"`
	Created  string       `json:"created"`
	Updated  string       `json:"updated"`
	Close    bool         `json:"close,omitempty"` // the firm closed it; not yet sent
}

// SupportPriorities and SupportCategories are the choices on the form.
var (
	SupportPriorities = []string{"Low", "Normal", "High", "Urgent"}
	SupportCategories = []string{"Something is not working", "How do I…", "Data or file problem", "Billing", "Feature request", "Other"}
)

func (o *Ops) supportUser(ctx context.Context, tenant, actor string) (User, error) {
	u, err := o.user(ctx, tenant, actor)
	if err != nil || !u.Active || u.Role == RoleClient {
		return User{}, fmt.Errorf("%w: support requests are for the firm's logins", ErrForbidden)
	}
	return u, nil
}

func (o *Ops) kickControl() {
	if o.ControlKick != nil {
		select {
		case o.ControlKick <- struct{}{}:
		default:
		}
	}
}

// CreateSupportRequest sends a request to Ledgerline's support team.
func (o *Ops) CreateSupportRequest(ctx context.Context, tenant, actor, subject, text, priority, category, screen string) (SupportRequest, error) {
	u, err := o.supportUser(ctx, tenant, actor)
	if err != nil {
		return SupportRequest{}, err
	}
	subject, text = strings.TrimSpace(subject), strings.TrimSpace(text)
	if subject == "" || len(subject) > 150 {
		return SupportRequest{}, invalid("give the request a short subject (up to 150 characters)")
	}
	if text == "" || len(text) > 8000 {
		return SupportRequest{}, invalid("describe what happened (up to 8,000 characters)")
	}
	if !contains(SupportPriorities, priority) {
		priority = "Normal"
	}
	if !contains(SupportCategories, category) {
		category = "Other"
	}
	n, err := o.next(ctx, tenant, "supportreq", actor)
	if err != nil {
		return SupportRequest{}, err
	}
	now := o.now().Format(time.RFC3339)
	r := SupportRequest{ID: fmt.Sprintf("SR-%04d", n), User: actor, UserName: u.Name, Role: u.Role, Subject: subject, Priority: priority, Category: category,
		Screen: trunc(screen, 60), Status: "Sent", Created: now, Updated: now,
		Messages: []SupportMsg{{ID: rpaID("m", 6), From: "firm", Who: u.Name, Text: text, At: now}}}
	if err := docs.Upsert(ctx, o.Docs, tenant, cSupport, r.ID, r, actor); err != nil {
		return r, err
	}
	o.log(ctx, tenant, actor, "support.request", r.ID, map[string]any{"subject": subject, "priority": priority})
	o.kickControl()
	return r, nil
}

// SupportRequests lists the person's requests (all the firm's for Admins).
func (o *Ops) SupportRequests(ctx context.Context, tenant, actor string) ([]SupportRequest, error) {
	u, err := o.supportUser(ctx, tenant, actor)
	if err != nil {
		return nil, err
	}
	rs, err := docs.List[SupportRequest](ctx, o.Docs, tenant, cSupport, "")
	if err != nil {
		return nil, err
	}
	out := []SupportRequest{}
	for _, r := range rs {
		if r.User == actor || u.Role == RoleAdmin {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Updated > out[j].Updated })
	return out, nil
}

// ReplySupportRequest adds to a request, or closes it.
func (o *Ops) ReplySupportRequest(ctx context.Context, tenant, actor, id, text string, closeIt bool) (SupportRequest, error) {
	u, err := o.supportUser(ctx, tenant, actor)
	if err != nil {
		return SupportRequest{}, err
	}
	text = strings.TrimSpace(text)
	if text == "" && !closeIt {
		return SupportRequest{}, invalid("write a reply")
	}
	if len(text) > 8000 {
		return SupportRequest{}, invalid("keep the reply under 8,000 characters")
	}
	r, err := docs.Update(ctx, o.Docs, tenant, cSupport, id, actor, func(x *SupportRequest) error {
		if x.User != actor && u.Role != RoleAdmin {
			return ErrNotFound
		}
		now := o.now().Format(time.RFC3339)
		if text != "" {
			x.Messages = append(x.Messages, SupportMsg{ID: rpaID("m", 6), From: "firm", Who: u.Name, Text: text, At: now})
			if x.Status == "Waiting on you" || x.Status == "Resolved" {
				x.Status = "Open"
			}
		}
		if closeIt {
			x.Status, x.Close = "Closed", true
		}
		x.Updated = now
		return nil
	})
	if err != nil {
		return r, fmt.Errorf("%w: no such request", ErrNotFound)
	}
	o.kickControl()
	return r, nil
}

// SupportPending is what has not yet reached Ledgerline Control.
func (o *Ops) SupportPending(ctx context.Context, tenant string) []control.TicketSync {
	rs, _ := docs.List[SupportRequest](ctx, o.Docs, tenant, cSupport, "")
	var out []control.TicketSync
	for _, r := range rs {
		for i, m := range r.Messages {
			if m.From != "firm" || m.Synced {
				continue
			}
			t := control.TicketSync{LocalID: r.ID, Tenant: tenant, User: r.User, UserName: m.Who, Role: r.Role, Text: m.Text, At: m.At, MsgID: m.ID}
			if i == 0 {
				t.Subject, t.Priority, t.Category, t.Screen = r.Subject, r.Priority, r.Category, r.Screen
			}
			out = append(out, t)
		}
		if r.Close {
			out = append(out, control.TicketSync{LocalID: r.ID, Tenant: tenant, User: r.User, Close: true, MsgID: r.ID + "-close", At: r.Updated})
		}
	}
	return out
}

// SupportSynced marks messages as received by Ledgerline Control.
func (o *Ops) SupportSynced(ctx context.Context, tenant string, sent []control.TicketSync) {
	byReq := map[string]map[string]bool{}
	for _, t := range sent {
		if t.Tenant != tenant {
			continue
		}
		if byReq[t.LocalID] == nil {
			byReq[t.LocalID] = map[string]bool{}
		}
		byReq[t.LocalID][t.MsgID] = true
	}
	for id, ms := range byReq {
		_, _ = docs.Update(ctx, o.Docs, tenant, cSupport, id, "system:control", func(x *SupportRequest) error {
			for i := range x.Messages {
				if ms[x.Messages[i].ID] {
					x.Messages[i].Synced = true
				}
			}
			if ms[x.ID+"-close"] {
				x.Close = false
			}
			if x.Status == "Sent" {
				x.Status = "Open"
			}
			return nil
		})
	}
}

// SupportApply takes replies and status changes from Ledgerline Control.
func (o *Ops) SupportApply(ctx context.Context, tenant string, ups []control.TicketUpdate) {
	for _, u := range ups {
		if u.Tenant != tenant {
			continue
		}
		var tell, who string
		r, err := docs.Update(ctx, o.Docs, tenant, cSupport, u.LocalID, "system:control", func(x *SupportRequest) error {
			for _, m := range x.Messages {
				if m.ID == u.MsgID {
					return errClaimed // already have it
				}
			}
			if u.Ticket != "" {
				x.Ticket = u.Ticket
			}
			if u.Text != "" {
				x.Messages = append(x.Messages, SupportMsg{ID: u.MsgID, From: "support", Who: firstOf(u.From, "Ledgerline support"), Text: u.Text, At: u.At, Synced: true})
				tell = "Ledgerline support replied to your request " + x.ID + " (" + x.Subject + ")."
			} else if u.Status != x.Status && !x.Close {
				x.Messages = append(x.Messages, SupportMsg{ID: u.MsgID, From: "status", Who: "Ledgerline support", Text: "Status: " + u.Status, At: u.At, Synced: true})
			} else {
				// Only the ticket number: keep the update's ID so it is not applied again.
				x.Messages = append(x.Messages, SupportMsg{ID: u.MsgID, From: "info", Synced: true, At: u.At})
			}
			if u.Status != "" && u.Status != x.Status && !x.Close {
				x.Status = u.Status
				if u.Text == "" && (u.Status == "Resolved" || u.Status == "Waiting on you") {
					tell = "Your request " + x.ID + " (" + x.Subject + ") is now: " + u.Status + "."
				}
			}
			x.Updated = o.now().Format(time.RFC3339)
			who = x.User
			return nil
		})
		if err == nil && tell != "" {
			o.supportTell(ctx, tenant, who, tell, r.ID)
		}
	}
}

func (o *Ops) supportTell(ctx context.Context, tenant, user, text, id string) {
	e, err := o.chatEnv(ctx, tenant, LedgyActor)
	if err != nil {
		return
	}
	u, ok := e.users[user]
	if !ok || chatSide(u) == "" {
		return
	}
	cid := "L-" + shortHash(user)
	c, err := o.chatChannel(ctx, tenant, cid)
	if err != nil {
		c = ChatChannel{ID: cid, Kind: ChatLedgy, Scope: chatSide(u), Members: []string{user}, Created: o.now().Format(time.RFC3339), CreatedBy: LedgyActor}
		if docs.Upsert(ctx, o.Docs, tenant, cChatChan, cid, c, LedgyActor) != nil {
			return
		}
	}
	_, _ = o.postChat(ctx, e, c, LedgyActor, text, nil, &ChatLink{View: "support:" + id, Label: "Open the request"})
}
