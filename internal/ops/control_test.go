package ops

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/control"
	"ledgerline/internal/docs"
	"ledgerline/internal/money"
	"ledgerline/internal/vault"
)

// A Ledgerline server reporting to Ledgerline Control: usage, a support
// request and its reply, and AI credit sent from Control.
func TestControlRoundTrip(t *testing.T) {
	o, ctx := setup(t)
	c := control.New(&vault.Docs{Inner: docs.NewMem(), V: vault.New(vault.Ephemeral())})
	if _, err := c.Bootstrap(ctx, "owner", "Owner", "owner-password-1"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(control.NewServer(c, nil))
	defer srv.Close()
	in, key, err := c.NewInstallation(ctx, "owner", "Acme's server", "own")
	if err != nil {
		t.Fatal(err)
	}
	o.ControlKick = make(chan struct{}, 1)
	cc := &ControlClient{Ops: o, URL: srv.URL, Key: key, Tenants: func() []string { return []string{T} }, Info: control.InstallInfo{Version: "test"}}

	if err := cc.ReportNow(ctx); err != nil {
		t.Fatal(err)
	}
	fs, err := c.Firms(ctx, "owner")
	if err != nil || len(fs) != 1 {
		t.Fatalf("firms %v %+v", err, fs)
	}
	f := fs[0]
	if !f.Online || f.Latest.StaffActive == 0 || f.Latest.Clients != 64 || f.Customer == "" || len(f.Latest.Users) == 0 {
		t.Fatalf("firm report: %+v", f.Latest)
	}
	for _, u := range f.Latest.Users {
		if u.Role == RoleClient {
			t.Fatal("client logins reported as staff")
		}
	}
	t.Logf("firm %s: staff %d, clients %d, traded %d, features %v", f.Latest.Firm, f.Latest.StaffActive, f.Latest.Clients, f.Latest.TradedMonth, f.Latest.Features)

	// A support request from inside Ledgerline.
	r, err := o.CreateSupportRequest(ctx, T, "priya.nair", "Trade file won't load", "The NSE file shows an error.", "High", "Data or file problem", "Files")
	if err != nil {
		t.Fatal(err)
	}
	if len(o.SupportPending(ctx, T)) != 1 {
		t.Fatal("not pending")
	}
	if err := cc.ReportNow(ctx); err != nil {
		t.Fatal(err)
	}
	if len(o.SupportPending(ctx, T)) != 0 {
		t.Fatal("still pending after it reached Control")
	}
	ts, _ := c.Tickets(ctx, "owner")
	if len(ts) != 1 || ts[0].Subject != "Trade file won't load" || ts[0].UserName == "" || ts[0].Screen != "Files" || ts[0].Priority != "High" || ts[0].Status != "New" {
		t.Fatalf("ticket %+v", ts)
	}
	tk := ts[0]
	if _, err := c.ReplyTicket(ctx, "owner", tk.ID, "Please send the file name.", false, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ReplyTicket(ctx, "owner", tk.ID, "Internal: likely a header change", true, ""); err != nil {
		t.Fatal(err)
	}
	if err := cc.ReportNow(ctx); err != nil {
		t.Fatal(err)
	}
	rs, _ := o.SupportRequests(ctx, T, "priya.nair")
	got := rs[0]
	if got.Status != "Waiting on you" || got.Ticket == "" {
		t.Fatalf("local status %q ticket %q", got.Status, got.Ticket)
	}
	var support, internal int
	for _, m := range got.Messages {
		if m.From == "support" {
			support++
		}
		if strings.Contains(m.Text, "Internal") {
			internal++
		}
	}
	if support != 1 || internal != 0 {
		t.Fatalf("support msgs %d internal leaked %d: %+v", support, internal, got.Messages)
	}
	// Ledgy told her.
	h, _ := o.ChatHome(ctx, T, "priya.nair")
	told := false
	for _, cv := range h.Conversations {
		if cv.ID == "L-"+shortHash("priya.nair") {
			told = true
		}
	}
	if !told {
		t.Fatal("Ledgy did not tell the person")
	}
	// Delivered once only: another report adds nothing.
	if err := cc.ReportNow(ctx); err != nil {
		t.Fatal(err)
	}
	if err := cc.ReportNow(ctx); err != nil {
		t.Fatal(err)
	}
	rs, _ = o.SupportRequests(ctx, T, "priya.nair")
	if len(rs[0].Messages) != len(got.Messages) {
		t.Fatalf("reply delivered twice: %d vs %d", len(rs[0].Messages), len(got.Messages))
	}
	// She replies; the ticket reopens.
	if _, err := o.ReplySupportRequest(ctx, T, "priya.nair", r.ID, "NSE_FO_20261016.csv", false); err != nil {
		t.Fatal(err)
	}
	cc.ReportNow(ctx)
	tk, _ = c.Ticket(ctx, "owner", tk.ID)
	if tk.Status != "Open" || tk.Messages[len(tk.Messages)-1].Text != "NSE_FO_20261016.csv" {
		t.Fatalf("after customer reply: %s %+v", tk.Status, tk.Messages)
	}
	// Someone else at the firm can't see her request; the Admin can.
	if rs, _ := o.SupportRequests(ctx, T, "farhan.khan"); len(rs) != 0 {
		t.Fatal("another person sees the request")
	}
	if rs, _ := o.SupportRequests(ctx, T, "admin"); len(rs) != 1 {
		t.Fatal("admin doesn't see the firm's requests")
	}
	// Resolved in Control shows in Ledgerline.
	c.ReplyTicket(ctx, "owner", tk.ID, "", false, "Resolved")
	cc.ReportNow(ctx)
	rs, _ = o.SupportRequests(ctx, T, "priya.nair")
	if rs[0].Status != "Resolved" {
		t.Fatalf("status %s", rs[0].Status)
	}

	// AI credit from Control, applied once.
	before, _ := o.aiUsage(ctx, T, "")
	cmd, err := c.SendAICredit(ctx, "owner", f.ID, money.MustAmount("5000"), true, "NEFT 123")
	if err != nil {
		t.Fatal(err)
	}
	cc.ReportNow(ctx) // applied
	cc.ReportNow(ctx) // acked
	after, _ := o.aiUsage(ctx, T, "")
	if after.Wallet.Balance-before.Wallet.Balance != money.MustAmount("5000") {
		t.Fatalf("balance %v -> %v", before.Wallet.Balance, after.Wallet.Balance)
	}
	d, _ := c.GetCustomer(ctx, "owner", f.Customer)
	if len(d.Commands) != 1 || d.Commands[0].Status != "Done" {
		t.Fatalf("command %+v", d.Commands)
	}
	// Even if the same command arrived again, it is not added twice.
	if err := o.ApplyCommand(ctx, cmd.Command); err != nil {
		t.Fatal(err)
	}
	again, _ := o.aiUsage(ctx, T, "")
	if again.Wallet.Balance != after.Wallet.Balance {
		t.Fatal("top-up applied twice")
	}
	// It goes on the customer's bill.
	cu := d.Customer
	cu.Status, cu.Address, cu.LegalName = "Active", "1 Dalal Street, Mumbai", "Acme Securities Pvt Ltd"
	if _, err := c.SaveCustomer(ctx, "owner", cu); err != nil {
		t.Fatal(err)
	}
	pv, _ := c.Preview(ctx, "owner", time.Now().In(control.IST).Format("2006-01"))
	ai := false
	for _, l := range pv[0].Invoice.Lines {
		if l.Kind == "ai" && l.Amount == money.MustAmount("5000") {
			ai = true
		}
	}
	if !ai {
		t.Fatalf("AI credit not billed: %+v", pv[0].Invoice.Lines)
	}

	// A revoked key is refused.
	c.UpdateInstallation(ctx, "owner", in.ID, "", "", true, false)
	if err := cc.ReportNow(ctx); err == nil {
		t.Fatal("revoked key accepted")
	}
	_ = context.Background
}

func TestAuditAreasCoverActions(t *testing.T) {
	o, ctx := setup(t)
	evs, _ := o.App.Audit.List(ctx, T, 5000)
	for _, e := range evs {
		if auditArea(e.Action) == "Other" {
			t.Errorf("no area for %q", e.Action)
		}
	}
}
