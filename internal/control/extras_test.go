package control

import (
	"context"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/mail"
	"ledgerline/internal/money"
)

type fakeMail struct{ sent []mail.Message }

func (f *fakeMail) Send(_ context.Context, m mail.Message) (string, error) {
	f.sent = append(f.sent, m)
	return "<x@test>", nil
}

// billed sets up one active customer with an issued October invoice.
func billed(t *testing.T) (*Control, context.Context, Installation, Customer, Invoice) {
	t.Helper()
	c, ctx := newC(t)
	in, _, _ := c.NewInstallation(ctx, "owner", "Hosted", "hosted")
	f := firm("acme", "Acme", 4, 300, 0, 0, "autopilot")
	f.Setup = []SetupItem{{Key: "signing", Label: "Digital signature certificate", OK: true, Days: 12}, {Key: "mail", Label: "Mail server", OK: false, Detail: "not set"}}
	f.Health = Health{EOD: "closed", LastBackup: now.Add(-2 * time.Hour).Format(time.RFC3339)}
	if _, err := c.Receive(ctx, in, Report{Installation: InstallInfo{Version: "1.4.0"}, Firms: []FirmReport{f}}, "1.2.3.4"); err != nil {
		t.Fatal(err)
	}
	set, _ := c.Settings(ctx)
	set.Vendor = Vendor{Name: "Ledgerline Tech", Address: "Pune", GSTIN: "27AAACL1234A1Z5", Prefix: "LL", DueDays: 15, Account: "1234567890", Bank: "HDFC", IFSC: "HDFC0000001", Email: "billing@ledgerline.test"}
	set.Ops = Running{LatestVersion: "1.5.0", RemindEvery: 7, SuspendAfter: 30}
	if _, err := c.SaveSettings(ctx, "owner", set); err != nil {
		t.Fatal(err)
	}
	fs, _ := c.Firms(ctx, "owner")
	cu, _, _ := docs.Get[Customer](ctx, c.Docs, T, cCustomer, fs[0].Customer)
	cu.Status, cu.Address, cu.GSTIN, cu.SetupFee = "Active", "Mumbai", "27AABCA1111B1Z1", 0
	cu.EditionFee = map[string]money.Amount{"autopilot": money.MustAmount("30000")}
	cu.MinMonthly = money.MustAmount("40000")
	cu.BillingEmail, cu.CCEmails = "accounts@acme.test", "cfo@acme.test, bad"
	cu.ContractEnd = "2026-11-10"
	cu, err := c.SaveCustomer(ctx, "owner", cu)
	if err != nil {
		t.Fatal(err)
	}
	ivs, err := c.Generate(ctx, "owner", "2026-10", "")
	if err != nil || len(ivs) != 1 {
		t.Fatalf("generate %v", err)
	}
	iv, err := c.IssueInvoice(ctx, "owner", ivs[0].ID, "2026-10-20")
	if err != nil {
		t.Fatal(err)
	}
	return c, ctx, in, cu, iv
}

func TestContractPricesAndMinimum(t *testing.T) {
	_, _, _, _, iv := billed(t)
	got := map[string]money.Amount{}
	for _, l := range iv.Lines {
		got[l.Kind] += l.Amount
	}
	// Agreed fee 30,000 instead of 35,000; no client fee under 500; topped
	// up to the 40,000 minimum.
	if got["edition"] != money.MustAmount("30000") || got["minimum"] != money.MustAmount("10000") || iv.Taxable != money.MustAmount("40000") {
		t.Fatalf("lines %v taxable %s", got, iv.Taxable)
	}
	if iv.Balance != iv.Total || iv.Status != "Issued" {
		t.Fatalf("balance %s status %s", iv.Balance, iv.Status)
	}
}

func TestCreditNotesTDSStatementAgeing(t *testing.T) {
	c, ctx, _, cu, iv := billed(t)
	if _, err := c.IssueCreditNote(ctx, "owner", iv.ID, money.MustAmount("50000"), "too much"); err == nil {
		t.Fatal("credited more than the invoice")
	}
	if _, err := c.IssueCreditNote(ctx, "owner", iv.ID, money.MustAmount("5000"), ""); err == nil {
		t.Fatal("no reason accepted")
	}
	cn, err := c.IssueCreditNote(ctx, "owner", iv.ID, money.MustAmount("5000"), "Agreed discount for October outage")
	if err != nil || cn.Number != "LL-CN/26-27/0001" || cn.Total != money.MustAmount("5900") || cn.CGST+cn.SGST != money.MustAmount("900") {
		t.Fatalf("credit note %+v %v", cn, err)
	}
	if _, _, err := c.CreditNotePDF(ctx, "owner", cn.ID); err != nil {
		t.Fatal(err)
	}
	iv, _ = c.GetInvoice(ctx, "owner", iv.ID)
	// 47,200 total − 5,900 credit = 41,300 due.
	if iv.Balance != money.MustAmount("41300") || iv.Status != "Part-paid" {
		t.Fatalf("after credit %s %s", iv.Balance, iv.Status)
	}
	if _, err := c.VoidInvoice(ctx, "owner", iv.ID, "x"); err == nil {
		t.Fatal("voided a credited invoice")
	}
	// Customer pays with 10% TDS on the taxable value (40,000 → 4,000).
	if _, err := c.RecordPayment(ctx, "owner", iv.ID, Payment{Date: "2026-10-25", Amount: money.MustAmount("37300"), TDS: money.MustAmount("9000")}); err == nil {
		t.Fatal("TDS above 20% accepted")
	}
	iv, err = c.RecordPayment(ctx, "owner", iv.ID, Payment{Date: "2026-10-25", Amount: money.MustAmount("37300"), TDS: money.MustAmount("4000"), Ref: "UTR123"})
	if err != nil || iv.Status != "Paid" || iv.Balance != 0 {
		t.Fatalf("paid %v %+v", err, iv)
	}
	tds, _ := c.TDSRegister(ctx, "owner")
	if len(tds) != 1 || tds[0].Quarter != "FY 26-27 Q3" || tds[0].Cert != "" {
		t.Fatalf("tds %+v", tds)
	}
	if _, err := c.TDSCertificate(ctx, "owner", iv.ID, 0, "16A/ABC/2026/Q3"); err != nil {
		t.Fatal(err)
	}
	st, err := c.Statement(ctx, "owner", cu.ID, "", "2026-10-31")
	if err != nil || len(st.Lines) != 4 || st.Closing != 0 {
		t.Fatalf("statement %+v %v", st.Lines, err)
	}
	if _, _, err := c.StatementPDF(ctx, "owner", cu.ID, "", ""); err != nil {
		t.Fatal(err)
	}
	// A second, unpaid invoice ages.
	c.Now = func() time.Time { return time.Date(2026, 11, 3, 11, 0, 0, 0, IST) }
	ivs, _ := c.Generate(ctx, "owner", "2026-10", cu.ID) // October already invoiced: nothing new
	if len(ivs) != 0 {
		t.Fatal("billed October twice")
	}
	ivs, _ = c.Generate(ctx, "owner", "2026-11", cu.ID)
	if len(ivs) != 0 {
		t.Fatal("no usage in November yet, still billed")
	}
	n2, _ := c.EditDraft(ctx, "owner", mustDraft(t, c, ctx, cu.ID), &InvoiceLine{Desc: "Custom report", Qty: 1, Rate: money.MustAmount("10000")}, -1, nil)
	n2, err = c.IssueInvoice(ctx, "owner", n2.ID, "2026-11-03")
	if err != nil {
		t.Fatal(err)
	}
	c.Now = func() time.Time { return time.Date(2026, 12, 20, 11, 0, 0, 0, IST) }
	ag, tot, _ := c.Receivables(ctx, "owner")
	if len(ag) != 1 || ag[0].D60 != n2.Total || tot.Total != n2.Total || ag[0].Oldest != 32 {
		t.Fatalf("ageing %+v", ag)
	}
}

func mustDraft(t *testing.T, c *Control, ctx context.Context, cust string) string {
	t.Helper()
	n, _ := c.next(ctx, "invoice")
	iv := Invoice{ID: "INV-T" + itoa(n), Customer: cust, Period: c.month(), Status: "Draft", Payments: []Payment{}, Created: c.stamp(), CreatedBy: "owner"}
	if err := docs.Upsert(ctx, c.Docs, T, cInvoice, iv.ID, iv, "owner"); err != nil {
		t.Fatal(err)
	}
	return iv.ID
}

func TestEmailRemindersAndNotices(t *testing.T) {
	c, ctx, in, _, iv := billed(t)
	if _, err := c.SendInvoice(ctx, "owner", iv.ID); err == nil {
		t.Fatal("sent without a mail server")
	}
	fm := &fakeMail{}
	c.Mailer = fm
	iv, err := c.SendInvoice(ctx, "owner", iv.ID)
	if err != nil || iv.EmailedTo != "accounts@acme.test" || len(fm.sent) != 1 || len(fm.sent[0].CC) != 1 || !strings.HasPrefix(string(fm.sent[0].Attach[0].Data), "%PDF-") {
		t.Fatalf("email %v %+v", err, fm.sent)
	}
	// Not overdue yet: nothing due for a reminder.
	if due, _ := c.RemindersDue(ctx, "owner"); len(due) != 0 {
		t.Fatalf("due early %+v", due)
	}
	c.Now = func() time.Time { return time.Date(2026, 11, 10, 11, 0, 0, 0, IST) } // due 4-Nov
	due, _ := c.RemindersDue(ctx, "owner")
	if len(due) != 1 || due[0].Days != 6 || due[0].Suspend {
		t.Fatalf("due %+v", due)
	}
	iv, err = c.SendReminder(ctx, "owner", iv.ID, true)
	if err != nil || len(iv.Reminders) != 1 || iv.Reminders[0].Via != "email+notice" {
		t.Fatalf("reminder %v %+v", err, iv.Reminders)
	}
	if due, _ := c.RemindersDue(ctx, "owner"); len(due) != 0 {
		t.Fatal("reminded again within the interval")
	}
	// The notice reaches the firm with its next report.
	rep, err := c.Receive(ctx, in, Report{Firms: []FirmReport{firm("acme", "Acme", 4, 300, 0, 0, "autopilot")}}, "1.2.3.4")
	if err != nil || len(rep.Commands) != 1 || rep.Commands[0].Kind != "notice" || rep.Commands[0].Notice.Audience != "admins" {
		t.Fatalf("commands %+v %v", rep.Commands, err)
	}
	// Automatic reminders, once a day, when switched on.
	set, _ := c.Settings(ctx)
	set.Ops.AutoRemind = true
	c.SaveSettings(ctx, "owner", set)
	c.Now = func() time.Time { return time.Date(2026, 11, 20, 9, 0, 0, 0, IST) }
	if n, _ := c.Daily(ctx); n != 0 {
		t.Fatal("ran before 10:00")
	}
	c.Now = func() time.Time { return time.Date(2026, 11, 20, 10, 30, 0, 0, IST) }
	if n, err := c.Daily(ctx); n != 1 || err != nil {
		t.Fatalf("daily %d %v", n, err)
	}
	if n, _ := c.Daily(ctx); n != 0 {
		t.Fatal("ran twice in a day")
	}
	// Broadcast notice, then withdrawn before delivery: cancelled.
	nt, err := c.SendNotice(ctx, "owner", NoticeInput{Title: "Maintenance Sunday 22:00–23:00", Text: "Ledgerline will be unavailable.", Level: "warn", Target: "all", Until: "2026-11-30T23:00"})
	if err != nil || len(nt.Commands) != 1 {
		t.Fatalf("notice %+v %v", nt, err)
	}
	if _, err := c.WithdrawNotice(ctx, "owner", nt.ID); err != nil {
		t.Fatal(err)
	}
	x, _, _ := docs.Get[CommandRec](ctx, c.Docs, T, cCommand, nt.Commands[0])
	if x.Status != "Cancelled" {
		t.Fatalf("not cancelled: %s", x.Status)
	}
	if _, err := c.SendNotice(ctx, "owner", NoticeInput{Title: "x", Target: "customer:NOPE"}); err == nil {
		t.Fatal("notice to nobody")
	}
	// Edition change goes as a command.
	fs, _ := c.Firms(ctx, "owner")
	if _, err := c.SetFirmEdition(ctx, "owner", fs[0].ID, "autopilot"); err == nil {
		t.Fatal("same edition accepted")
	}
	if cmd, err := c.SetFirmEdition(ctx, "owner", fs[0].ID, "full"); err != nil || cmd.Edition != "full" {
		t.Fatalf("edition %v", err)
	}
}

func TestAlertsCannedStatsNotes(t *testing.T) {
	c, ctx, _, cu, _ := billed(t)
	as, err := c.Alerts(ctx, "owner")
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, a := range as {
		kinds[strings.SplitN(a.ID, ":", 2)[0]] = true
	}
	for _, k := range []string{"ver", "setup", "expiry", "renew"} {
		if !kinds[k] {
			t.Errorf("missing %s alert in %v", k, as)
		}
	}
	if err := c.AckAlert(ctx, "owner", as[0].ID); err != nil {
		t.Fatal(err)
	}
	as2, _ := c.Alerts(ctx, "owner")
	if as2[len(as2)-1].ID != as[0].ID || as2[len(as2)-1].Acked == "" {
		t.Fatal("acknowledged alert not set aside")
	}
	// Saved replies need Support.
	if _, _, err := c.SaveStaff(ctx, "owner", "bill", "Bill", RoleBilling, true, false); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SaveCanned(ctx, "bill", Canned{Title: "x", Text: "y"}); err == nil {
		t.Fatal("billing saved a support reply")
	}
	cn, err := c.SaveCanned(ctx, "owner", Canned{Title: "Restart the bot", Text: "Please restart the Ledgerline bot."})
	if err != nil || cn.ID == "" {
		t.Fatal(err)
	}
	st, err := c.SupportNumbers(ctx, "owner", "2026-10")
	if err != nil || st.Opened != 0 {
		t.Fatalf("stats %+v %v", st, err)
	}
	n, err := c.AddCustNote(ctx, "owner", CustNote{Customer: cu.ID, Kind: "Follow-up", Text: "Call about renewal", Due: "2026-11-01"})
	if err != nil {
		t.Fatal(err)
	}
	if fu, _ := c.CustNotes(ctx, "owner", ""); len(fu) != 1 {
		t.Fatal("follow-up not listed")
	}
	if _, err := c.AddCustNote(ctx, "owner", CustNote{ID: n.ID, Done: true}); err != nil {
		t.Fatal(err)
	}
	if fu, _ := c.CustNotes(ctx, "owner", ""); len(fu) != 0 {
		t.Fatal("done follow-up still open")
	}
}

func TestVersionLess(t *testing.T) {
	for _, x := range [][2]string{{"1.4.0", "1.5.0"}, {"1.9", "1.10"}, {"v2.0.1", "2.1"}} {
		if !versionLess(x[0], x[1]) || versionLess(x[1], x[0]) {
			t.Errorf("%v", x)
		}
	}
}
