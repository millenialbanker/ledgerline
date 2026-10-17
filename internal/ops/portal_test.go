package ops

import (
	"errors"
	"strings"
	"testing"

	"ledgerline/internal/money"
)

func TestClientPortal(t *testing.T) {
	o, ctx := setup(t)
	cs, _ := o.ClientMap(ctx, T)
	// Only a checker gives portal access; unlinked family needs a declaration.
	if _, _, err := o.PortalAccess(ctx, T, "priya.nair", "BH1001", nil, false); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker granted access: %v", err)
	}
	if _, _, err := o.PortalAccess(ctx, T, "farhan.khan", "BH1001", []string{"BH1002"}, false); err == nil || !strings.Contains(err.Error(), "family declaration") {
		t.Fatalf("family without declaration: %v", err)
	}
	id, pw, err := o.PortalAccess(ctx, T, "farhan.khan", "BH1001", []string{"BH1002"}, true)
	if err != nil || id != "bh1001" || pw == "" {
		t.Fatalf("grant: %s %q %v", id, pw, err)
	}
	scope := o.ClientScope(ctx, T, "bh1001")
	if scope == nil || !scope("BH1001") || !scope("bh1002") || scope("BH1003") {
		t.Fatal("client scope wrong")
	}
	v, err := o.Portal(ctx, T, "bh1001", D, "BH1002")
	if err != nil || v.Account.UCC != "BH1002" || len(v.Accounts) != 2 || strings.Contains(v.Account.PAN, cs["BH1002"].PAN) {
		t.Fatalf("portal: %+v %v", v.Account, err)
	}
	if _, err := o.Portal(ctx, T, "bh1001", D, "BH1003"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other client's portal: %v", err)
	}
	if _, err := o.Portal(ctx, T, "priya.nair", D, "BH1001"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("staff portal: %v", err)
	}

	// Change requests: from the portal, approved by a checker who did not raise it.
	r, err := o.RequestChange(ctx, T, "bh1001", "BH1001", "Email", map[string]string{"email": "aarav.new@mail.example"}, "")
	if err != nil || r.Source != "Portal" || r.Old["email"] != cs["BH1001"].Email {
		t.Fatalf("request: %+v %v", r, err)
	}
	if _, err := o.RequestChange(ctx, T, "bh1001", "BH1003", "Email", map[string]string{"email": "x@y.in"}, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("change for another client: %v", err)
	}
	if _, err := o.RequestChange(ctx, T, "bh1001", "BH1001", "Bank account", map[string]string{"bank": "ICICI Bank", "ifsc": "ICIC0001234", "account": "123412341234"}, ""); err == nil || !strings.Contains(err.Error(), "bank proof") {
		t.Fatalf("bank change without proof: %v", err)
	}
	if _, err := o.DecideChange(ctx, T, "priya.nair", r.ID, true, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker approved: %v", err)
	}
	if _, err := o.DecideChange(ctx, T, "farhan.khan", r.ID, true, ""); err == nil {
		t.Fatal("approved before the client verified the code")
	}
	if _, err := o.VerifyChange(ctx, T, "bh1001", r.ID, "000000x"); err == nil {
		t.Fatal("wrong code accepted")
	}
	// Verified by code: applied straight away (automation on by default).
	if v, err := o.VerifyChange(ctx, T, "bh1001", r.ID, r.DevCode); err != nil || v.Status != "Approved" {
		t.Fatalf("verify: %+v %v", v, err)
	}
	c, _ := o.Client(ctx, T, "BH1001")
	if c.Email != "aarav.new@mail.example" || c.KYCUpdatedOn == "" {
		t.Fatalf("change not applied: %s %s", c.Email, c.KYCUpdatedOn)
	}
	// A bank change with a bank proof attached.
	png := []byte("\x89PNG\r\n\x1a\n0000000000000000")
	doc, err := o.AddClientDoc(ctx, T, "bh1001", "BH1001", "Bank proof", "cheque.png", png)
	if err != nil {
		t.Fatal(err)
	}
	br, err := o.RequestChange(ctx, T, "bh1001", "BH1001", "Bank account", map[string]string{"bank": "ICICI Bank", "ifsc": "ICIC0001234", "account": "123412341234"}, doc.ID)
	if err != nil || strings.Contains(br.New["account"], "12341234") {
		t.Fatalf("bank request: %+v %v", br, err)
	}
	if _, err := o.DecideChange(ctx, T, "farhan.khan", br.ID, false, ""); err == nil {
		t.Fatal("reject without reason")
	}
	if _, err := o.DecideChange(ctx, T, "farhan.khan", br.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	if c, _ := o.Client(ctx, T, "BH1001"); c.Bank.Account != "123412341234" {
		t.Fatalf("bank not changed: %+v", c.Bank)
	}

	// Complaints: raised in the portal, worked by staff, disclosed.
	tk, err := o.OpenTicket(ctx, T, "bh1001", Ticket{UCC: "BH1002", Category: "Non-receipt of funds", Subject: "Payout not received"}, "I asked for ₹10,000 on Monday.")
	if err != nil || tk.Source != "Directly from investors" || tk.Due != addDays(tk.OpenedOn, 21) {
		t.Fatalf("ticket: %+v %v", tk, err)
	}
	if _, err := o.OpenTicket(ctx, T, "priya.nair", Ticket{UCC: "BH1004", Category: "Unauthorised trades", Subject: "Trade not placed by me", Source: "SEBI (SCORES)"}, "SCORES complaint"); err == nil {
		t.Fatal("SCORES complaint without reference")
	}
	sc, err := o.OpenTicket(ctx, T, "priya.nair", Ticket{UCC: "BH1004", Category: "Unauthorised trades", Subject: "Trade not placed by me", Source: "SEBI (SCORES)", Ref: "SEBIE/MH26/0001234/1", OpenedOn: "2026-06-20"}, "SCORES complaint")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.TicketUpdate(ctx, T, "bh1001", tk.ID, "", true, "", ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("client internal note: %v", err)
	}
	if _, err := o.TicketUpdate(ctx, T, "priya.nair", tk.ID, "", false, "Resolved", ""); err == nil {
		t.Fatal("resolved without a reply")
	}
	tk, err = o.TicketUpdate(ctx, T, "priya.nair", tk.ID, "Checked with the bank", true, "", "farhan.khan")
	if err != nil || len(tk.Internal) != 1 || tk.Assignee != "farhan.khan" {
		t.Fatalf("internal note: %+v %v", tk, err)
	}
	tk, err = o.TicketUpdate(ctx, T, "farhan.khan", tk.ID, "Paid by NEFT, UTR N2026101612345", false, "Resolved", "")
	if err != nil || tk.Status != "Resolved" || tk.ResolvedOn == "" {
		t.Fatalf("resolve: %+v %v", tk, err)
	}
	pv, _ := o.Portal(ctx, T, "bh1001", D, "BH1001")
	if len(pv.Tickets) != 1 || pv.Tickets[0].Internal != nil {
		t.Fatalf("portal tickets: %+v", pv.Tickets)
	}
	tk, _ = o.TicketUpdate(ctx, T, "bh1001", tk.ID, "Not received yet", false, "", "")
	if tk.Status != "Open" {
		t.Fatalf("client reply did not reopen: %s", tk.Status)
	}
	month := o.now().Format("2006-01")
	ds, err := o.ComplaintsDisclosure(ctx, T, month)
	if err != nil {
		t.Fatal(err)
	}
	if ds.Total.Received != 1 || ds.Total.Pending != 2 || ds.Rows[1].Over3Months != 1 || ds.Rows[1].PendingAtOpen != 1 {
		t.Fatalf("disclosure: %+v", ds.Rows)
	}
	_ = sc

	// Outside trades feed capital gains.
	ext, err := o.AddExternalTrade(ctx, T, "bh1001", ExternalTrade{UCC: "BH1001", Date: D, Symbol: "ZZTEST", ISIN: "INE000Z01011", Side: "B", Qty: 10, Price: money.MustDec("100"), Broker: "Other Broker Ltd"})
	if err != nil {
		t.Fatal(err)
	}
	open, _, _, err := o.equityBook(ctx, T, "BH1001", D)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, l := range open {
		found = found || (l.ISIN == "INE000Z01011" && l.Qty == 10)
	}
	if !found {
		t.Fatal("outside buy not in lots")
	}
	if err := o.DeleteExternalTrade(ctx, T, "bh1001", "BH1001", ext.ID); err != nil {
		t.Fatal(err)
	}
	if xs, _ := o.ExternalTrades(ctx, T, "BH1001"); len(xs) != 0 {
		t.Fatalf("deleted trade still listed: %v", xs)
	}
}

func TestLeads(t *testing.T) {
	o, ctx := setup(t)
	l, err := o.SaveLead(ctx, T, "priya.nair", Lead{Name: "Kavya Iyer", Mobile: "9812345678", Source: "Website", Interest: []string{"Equity"}})
	if err != nil || l.Stage != "New" || !strings.HasPrefix(l.Mobile, "•") {
		t.Fatalf("lead: %+v %v", l, err)
	}
	if _, err := o.SaveLead(ctx, T, "priya.nair", Lead{Name: "K Iyer", Mobile: "9812345678"}); err == nil {
		t.Fatal("duplicate open lead")
	}
	l, msg, err := o.ActOnLead(ctx, T, "priya.nair", l.ID, LeadAction{Kind: "call", Outcome: "Interested", Text: "Wants F&O"})
	if err != nil || l.Stage != "Contacted" || !strings.Contains(msg, "5678") {
		t.Fatalf("call: %+v %q %v", l.Stage, msg, err)
	}
	l, _, err = o.ActOnLead(ctx, T, "priya.nair", l.ID, LeadAction{Kind: "appointment", At: D + "T11:30", Text: "Account opening"})
	if err != nil || len(l.Appts) != 1 {
		t.Fatalf("appointment: %v", err)
	}
	if _, _, err := o.ActOnLead(ctx, T, "priya.nair", l.ID, LeadAction{Kind: "stage", Stage: "Converted"}); err == nil {
		t.Fatal("converted without UCC")
	}
	if _, _, err := o.ActOnLead(ctx, T, "priya.nair", l.ID, LeadAction{Kind: "stage", Stage: "Converted", UCC: "BH1003"}); err != nil {
		t.Fatal(err)
	}
	v, err := o.CRM(ctx, T, "farhan.khan", D, D[:8]+"01", o.now().Format("2006-01-02"))
	if err != nil || len(v.Today) != 1 || len(v.Incentives) != 1 || v.Incentives[0].Activated != 1 || v.Incentives[0].Incentive != v.PerClient || v.Leads[0].Masked {
		t.Fatalf("crm: %+v %v", v.Incentives, err)
	}
	if _, err := o.CRM(ctx, T, "anita.rao", D, D, D); !errors.Is(err, ErrForbidden) {
		t.Fatalf("risk manager saw leads: %v", err)
	}
}
