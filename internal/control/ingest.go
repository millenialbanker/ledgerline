package control

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// ---- installations ----

// NewInstallation registers a server and returns its key, shown once.
func (c *Control) NewInstallation(ctx context.Context, by, name, kind string) (Installation, string, error) {
	if err := c.need(ctx, by, RoleBilling); err != nil {
		return Installation{}, "", err
	}
	name = clean(name, 80)
	if name == "" {
		return Installation{}, "", invalid("name the installation, e.g. “Hosted (Mumbai)” or “Acme Securities’ own server”")
	}
	if kind != "hosted" && kind != "own" {
		kind = "own"
	}
	n, err := c.next(ctx, "install")
	if err != nil {
		return Installation{}, "", err
	}
	key := "llc_" + randHex(24)
	in := Installation{ID: fmt.Sprintf("INS-%03d", n), Name: name, Kind: kind, KeyHash: sha(key), KeyHint: key[len(key)-4:], Created: c.stamp(), Firms: []string{}}
	if err := docs.Upsert(ctx, c.Docs, T, cInstall, in.ID, in, by); err != nil {
		return in, "", err
	}
	c.log(ctx, by, "install.create", in.ID, name)
	return in, key, nil
}

// UpdateInstallation renames, revokes or re-keys a server (returning a new key).
func (c *Control) UpdateInstallation(ctx context.Context, by, id, name, kind string, revoke, rekey bool) (Installation, string, error) {
	if err := c.need(ctx, by, RoleBilling); err != nil {
		return Installation{}, "", err
	}
	key := ""
	in, err := docs.Update(ctx, c.Docs, T, cInstall, id, by, func(x *Installation) error {
		if n := clean(name, 80); n != "" {
			x.Name = n
		}
		if kind == "hosted" || kind == "own" {
			x.Kind = kind
		}
		x.Revoked = revoke
		if rekey {
			key = "llc_" + randHex(24)
			x.KeyHash, x.KeyHint, x.Revoked = sha(key), key[len(key)-4:], false
		}
		return nil
	})
	if err != nil {
		return in, "", ErrNotFound
	}
	c.log(ctx, by, "install.update", id, fmt.Sprintf("revoked=%v rekey=%v", revoke, rekey))
	return in, key, nil
}

// InstallationForKey finds the server a key belongs to.
func (c *Control) InstallationForKey(ctx context.Context, key string) (Installation, error) {
	if !strings.HasPrefix(key, "llc_") {
		return Installation{}, ErrForbidden
	}
	h := sha(key)
	all, err := docs.List[Installation](ctx, c.Docs, T, cInstall, "")
	if err != nil {
		return Installation{}, err
	}
	for _, in := range all {
		if in.KeyHash == h && !in.Revoked {
			return in, nil
		}
	}
	return Installation{}, ErrForbidden
}

// Online says whether a server reported recently.
func (c *Control) Online(in Installation) bool {
	t, err := time.Parse(time.RFC3339, in.LastReport)
	return err == nil && c.now().Sub(t) < c.OnlineWithin
}

var editionRank = map[string]int{"manual": 1, "autopilot": 2, "full": 3}

// Receive takes a server's report and returns what it should do.
func (c *Control) Receive(ctx context.Context, in Installation, rep Report, from string) (ReportReply, error) {
	now := c.stamp()
	month := c.month()
	if len(rep.Firms) > 500 || len(rep.Tickets) > 2000 {
		return ReportReply{}, invalid("report too large")
	}
	var tenants []string
	for _, f := range rep.Firms {
		if f.Tenant == "" || len(f.Tenant) > 64 {
			continue
		}
		tenants = append(tenants, f.Tenant)
		fid := in.ID + "~" + f.Tenant
		fm, _, err := docs.Get[Firm](ctx, c.Docs, T, cFirm, fid)
		if errors.Is(err, docs.ErrNotFound) {
			cust, err := c.customerForNewFirm(ctx, f)
			if err != nil {
				return ReportReply{}, err
			}
			fm = Firm{ID: fid, Installation: in.ID, Tenant: f.Tenant, Customer: cust, FirstSeen: now}
		} else if err != nil {
			return ReportReply{}, err
		}
		fm.Latest, fm.LastReport = f, now
		if err := docs.Upsert(ctx, c.Docs, T, cFirm, fid, fm, "install:"+in.ID); err != nil {
			return ReportReply{}, err
		}
		m := firstOf(f.Month, month)
		uid := fid + "|" + m
		u, _, _ := docs.Get[UsageMonth](ctx, c.Docs, T, cUsage, uid)
		u.Firm, u.Month, u.Report = fid, m, f
		u.PeakStaff = max(u.PeakStaff, f.StaffActive)
		u.PeakBots = max(u.PeakBots, f.Bots.Computers)
		if editionRank[f.Edition] > editionRank[u.Edition] {
			u.Edition = f.Edition
		}
		if err := docs.Upsert(ctx, c.Docs, T, cUsage, uid, u, "install:"+in.ID); err != nil {
			return ReportReply{}, err
		}
	}
	sort.Strings(tenants)

	// Support requests and replies.
	for _, t := range rep.Tickets {
		if err := c.receiveTicket(ctx, in, rep.Installation.Version, t); err != nil {
			return ReportReply{}, err
		}
	}
	// What the server says it received and carried out.
	for _, id := range rep.Delivered {
		_, _ = docs.Update(ctx, c.Docs, T, cOutbox, id, "install:"+in.ID, func(o *Outbox) error {
			if o.Installation != in.ID {
				return ErrForbidden
			}
			if o.Delivered == "" {
				o.Delivered = now
			}
			return nil
		})
	}
	for _, a := range rep.Acks {
		_, _ = docs.Update(ctx, c.Docs, T, cCommand, a.ID, "install:"+in.ID, func(x *CommandRec) error {
			if x.Installation != in.ID || x.Status != "Queued" {
				return ErrForbidden
			}
			x.Done = now
			if a.OK {
				x.Status = "Done"
			} else {
				x.Status, x.Error = "Failed", clean(a.Error, 300)
			}
			return nil
		})
	}

	_, err := docs.Update(ctx, c.Docs, T, cInstall, in.ID, "install:"+in.ID, func(x *Installation) error {
		x.LastReport, x.LastFrom, x.Info, x.Firms, x.Reports = now, from, rep.Installation, tenants, x.Reports+1
		if x.Firms == nil {
			x.Firms = []string{}
		}
		return nil
	})
	if err != nil {
		return ReportReply{}, err
	}

	reply := ReportReply{Updates: []TicketUpdate{}, Commands: []Command{}, Received: len(rep.Tickets)}
	obs, _ := docs.List[Outbox](ctx, c.Docs, T, cOutbox, "")
	for _, o := range obs {
		if o.Installation == in.ID && o.Delivered == "" {
			reply.Updates = append(reply.Updates, o.Update)
		}
	}
	cmds, _ := docs.List[CommandRec](ctx, c.Docs, T, cCommand, "")
	for _, x := range cmds {
		if x.Installation == in.ID && x.Status == "Queued" {
			reply.Commands = append(reply.Commands, x.Command)
			if x.Sent == "" {
				_, _ = docs.Update(ctx, c.Docs, T, cCommand, x.ID, "install:"+in.ID, func(y *CommandRec) error { y.Sent = now; return nil })
			}
		}
	}
	return reply, nil
}

// A firm seen for the first time gets its own customer, unless a customer
// already has its SEBI registration (the same broker on a second server).
func (c *Control) customerForNewFirm(ctx context.Context, f FirmReport) (string, error) {
	if f.SEBIReg != "" {
		fs, _ := docs.List[Firm](ctx, c.Docs, T, cFirm, "")
		for _, x := range fs {
			if strings.EqualFold(x.Latest.SEBIReg, f.SEBIReg) && x.Customer != "" {
				return x.Customer, nil
			}
		}
	}
	set, _ := c.Settings(ctx)
	n, err := c.next(ctx, "customer")
	if err != nil {
		return "", err
	}
	cu := Customer{ID: fmt.Sprintf("CUS-%04d", n), Name: firstOf(f.Firm, f.Tenant), LegalName: f.Firm, Status: "Trial", SetupFee: set.Prices.SetupFee, DiscountPct: "0",
		BillFrom: c.month(), Created: c.stamp()}
	return cu.ID, docs.Upsert(ctx, c.Docs, T, cCustomer, cu.ID, cu, "system")
}

// ---- support ----

func toLocalStatus(s string) string {
	switch s {
	case "New", "Open":
		return "Open"
	case "Waiting on customer":
		return "Waiting on you"
	}
	return s
}

func ticketID(in, tenant, local string) string { return in + "~" + tenant + "~" + local }

func (c *Control) receiveTicket(ctx context.Context, in Installation, version string, t TicketSync) error {
	if t.LocalID == "" || t.Tenant == "" || !has(in.Firms, t.Tenant) && !c.firmKnown(ctx, in.ID, t.Tenant) {
		return nil
	}
	id := ticketID(in.ID, t.Tenant, t.LocalID)
	now := c.stamp()
	tk, _, err := docs.Get[Ticket](ctx, c.Docs, T, cTicket, id)
	isNew := errors.Is(err, docs.ErrNotFound)
	if err != nil && !isNew {
		return err
	}
	if isNew {
		if t.Close && t.Text == "" {
			return nil
		}
		fm, _, _ := docs.Get[Firm](ctx, c.Docs, T, cFirm, in.ID+"~"+t.Tenant)
		pri := firstOf(t.Priority, "Normal")
		if _, ok := SLA[pri]; !ok {
			pri = "Normal"
		}
		n, err := c.next(ctx, "ticket")
		if err != nil {
			return err
		}
		tk = Ticket{ID: id, Installation: in.ID, Tenant: t.Tenant, Firm: fm.ID, FirmName: firstOf(fm.Latest.Firm, t.Tenant), Customer: fm.Customer, LocalID: t.LocalID,
			Subject: clean(firstOf(t.Subject, "(no subject)"), 150), Category: clean(t.Category, 60), Priority: pri, Screen: clean(t.Screen, 60), Version: clean(version, 30),
			User: clean(t.User, 80), UserName: clean(t.UserName, 80), Role: clean(t.Role, 40), Status: "New", Messages: []TicketMsg{}, Created: now,
			RespondBy: c.now().Add(SLA[pri]).Format(time.RFC3339)}
		tk.Messages = append(tk.Messages, TicketMsg{ID: "#" + fmt.Sprint(n), From: "status", Who: "Control", Text: "Ticket T-" + fmt.Sprint(n), At: now})
	}
	for _, m := range tk.Messages {
		if m.ID == t.MsgID {
			return nil // already have it
		}
	}
	if t.Close {
		tk.Messages = append(tk.Messages, TicketMsg{ID: t.MsgID, From: "status", Who: firstOf(t.UserName, tk.UserName), Text: "Closed by the customer", At: now})
		tk.Status = "Closed"
		if tk.ResolvedAt == "" {
			tk.ResolvedAt = now
		}
	} else {
		tk.Messages = append(tk.Messages, TicketMsg{ID: t.MsgID, From: "customer", Who: firstOf(clean(t.UserName, 80), tk.UserName), Text: clean(t.Text, 8000), At: firstOf(t.At, now)})
		if tk.Status == "Waiting on customer" || tk.Status == "Resolved" {
			tk.Status, tk.ResolvedAt = "Open", ""
		}
	}
	tk.Updated = now
	if err := docs.Upsert(ctx, c.Docs, T, cTicket, id, tk, "install:"+in.ID); err != nil {
		return err
	}
	if isNew {
		// Tell the firm its ticket number.
		return c.queueUpdate(ctx, tk, "", "Open", "")
	}
	return nil
}

func (c *Control) firmKnown(ctx context.Context, in, tenant string) bool {
	_, _, err := docs.Get[Firm](ctx, c.Docs, T, cFirm, in+"~"+tenant)
	return err == nil
}

// TicketNo is the short number shown to people.
func TicketNo(tk Ticket) string {
	for _, m := range tk.Messages {
		if strings.HasPrefix(m.ID, "#") {
			return "T-" + m.ID[1:]
		}
	}
	return tk.LocalID
}

func (c *Control) queueUpdate(ctx context.Context, tk Ticket, text, status, from string) error {
	id := "U-" + c.now().Format("20060102150405") + "-" + randHex(4)
	u := TicketUpdate{LocalID: tk.LocalID, Tenant: tk.Tenant, Ticket: TicketNo(tk), Status: toLocalStatus(status), Text: text, From: from, At: c.stamp(), MsgID: id}
	return docs.Upsert(ctx, c.Docs, T, cOutbox, id, Outbox{ID: id, Installation: tk.Installation, Update: u, Created: c.stamp()}, "system")
}

// Tickets lists support requests.
func (c *Control) Tickets(ctx context.Context, by string) ([]Ticket, error) {
	if err := c.need(ctx, by); err != nil {
		return nil, err
	}
	all, err := docs.List[Ticket](ctx, c.Docs, T, cTicket, "")
	sort.Slice(all, func(i, j int) bool { return all[i].Updated > all[j].Updated })
	return all, err
}

// Ticket is one request.
func (c *Control) Ticket(ctx context.Context, by, id string) (Ticket, error) {
	if err := c.need(ctx, by); err != nil {
		return Ticket{}, err
	}
	tk, _, err := docs.Get[Ticket](ctx, c.Docs, T, cTicket, id)
	if err != nil {
		return tk, ErrNotFound
	}
	return tk, nil
}

// ReplyTicket answers a request (sent to the firm), or adds an internal
// note, and can change its status.
func (c *Control) ReplyTicket(ctx context.Context, by, id, text string, internal bool, status string) (Ticket, error) {
	if err := c.need(ctx, by, RoleSupport); err != nil {
		return Ticket{}, err
	}
	st, _ := c.staff(ctx, by)
	text = clean(text, 8000)
	if text == "" && status == "" {
		return Ticket{}, invalid("write a reply")
	}
	if status != "" && !has(TicketStatuses, status) {
		return Ticket{}, invalid("unknown status")
	}
	now := c.stamp()
	send, newStatus := false, ""
	tk, err := docs.Update(ctx, c.Docs, T, cTicket, id, by, func(x *Ticket) error {
		if text != "" {
			from := "support"
			if internal {
				from = "note"
			}
			x.Messages = append(x.Messages, TicketMsg{ID: "S-" + randHex(5), From: from, Who: st.Name, Text: text, At: now})
			if !internal {
				send = true
				if x.FirstResponse == "" {
					x.FirstResponse = now
				}
				if status == "" && (x.Status == "New" || x.Status == "Open") {
					status = "Waiting on customer"
				}
			}
		}
		if status != "" && status != x.Status {
			x.Status, newStatus = status, status
			x.Messages = append(x.Messages, TicketMsg{ID: "S-" + randHex(5), From: "status", Who: st.Name, Text: "Status: " + status, At: now})
			if status == "Resolved" || status == "Closed" {
				x.ResolvedAt = now
			} else {
				x.ResolvedAt = ""
			}
		}
		if x.Assignee == "" && !internal && text != "" {
			x.Assignee = by
		}
		x.Updated = now
		return nil
	})
	if err != nil {
		return tk, ErrNotFound
	}
	if send {
		err = c.queueUpdate(ctx, tk, text, tk.Status, st.Name)
	} else if newStatus != "" {
		err = c.queueUpdate(ctx, tk, "", newStatus, st.Name)
	}
	c.log(ctx, by, "ticket.reply", id, map[bool]string{true: "note", false: "reply"}[internal])
	return tk, err
}

// SetTicket changes who a request is with, and its priority.
func (c *Control) SetTicket(ctx context.Context, by, id, assignee, priority string) (Ticket, error) {
	if err := c.need(ctx, by, RoleSupport); err != nil {
		return Ticket{}, err
	}
	if assignee != "" && assignee != "-" {
		if _, err := c.staff(ctx, assignee); err != nil {
			return Ticket{}, invalid("no such person")
		}
	}
	tk, err := docs.Update(ctx, c.Docs, T, cTicket, id, by, func(x *Ticket) error {
		if assignee == "-" {
			x.Assignee = ""
		} else if assignee != "" {
			x.Assignee = assignee
		}
		if _, ok := SLA[priority]; ok && priority != x.Priority {
			x.Priority = priority
			if x.FirstResponse == "" {
				cr, _ := time.Parse(time.RFC3339, x.Created)
				x.RespondBy = cr.Add(SLA[priority]).Format(time.RFC3339)
			}
		}
		x.Updated = c.stamp()
		return nil
	})
	if err != nil {
		return tk, ErrNotFound
	}
	return tk, nil
}

// Overdue says a request has had no first reply within its target.
func (c *Control) Overdue(tk Ticket) bool {
	if tk.FirstResponse != "" || tk.Status == "Resolved" || tk.Status == "Closed" {
		return false
	}
	t, err := time.Parse(time.RFC3339, tk.RespondBy)
	return err == nil && c.now().After(t)
}

// ---- AI credit ----

// SendAICredit queues a top-up of a firm's AI credit, applied at its next
// report; bill says whether it goes on the customer's next invoice.
func (c *Control) SendAICredit(ctx context.Context, by, firm string, amount money.Amount, bill bool, note string) (CommandRec, error) {
	if err := c.need(ctx, by, RoleBilling); err != nil {
		return CommandRec{}, err
	}
	if amount <= 0 || amount > money.MustAmount("1000000") {
		return CommandRec{}, invalid("top up between ₹0.01 and ₹10,00,000")
	}
	fm, _, err := docs.Get[Firm](ctx, c.Docs, T, cFirm, firm)
	if err != nil {
		return CommandRec{}, ErrNotFound
	}
	n, err := c.next(ctx, "command")
	if err != nil {
		return CommandRec{}, err
	}
	x := CommandRec{Command: Command{ID: fmt.Sprintf("CMD-%05d", n), Kind: "ai_credit", Tenant: fm.Tenant, Amount: int64(amount), Ref: clean(note, 100)},
		Installation: fm.Installation, Firm: fm.ID, Customer: fm.Customer, Status: "Queued", Created: c.stamp(), By: by, Bill: bill}
	if err := docs.Upsert(ctx, c.Docs, T, cCommand, x.ID, x, by); err != nil {
		return x, err
	}
	c.log(ctx, by, "ai.credit", fm.ID, amount.String())
	return x, nil
}

// CancelCommand withdraws a command not yet carried out.
func (c *Control) CancelCommand(ctx context.Context, by, id string) error {
	if err := c.need(ctx, by, RoleBilling); err != nil {
		return err
	}
	_, err := docs.Update(ctx, c.Docs, T, cCommand, id, by, func(x *CommandRec) error {
		if x.Status != "Queued" || x.Sent != "" {
			return invalid("it has already gone to the server")
		}
		x.Status, x.Error, x.Done = "Cancelled", "cancelled by "+by, c.stamp()
		return nil
	})
	return err
}
