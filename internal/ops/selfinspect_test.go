package ops

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// All data here is made up: the sample firm and records added by the tests.

const siCO = "vikram.shah" // the compliance officer of the sample firm

// siClock sets the firm's clock; the function it returns moves it.
func siClock(o *Ops, at time.Time) func(time.Time) {
	now := func() time.Time { return at }
	o.Now, o.App.Now = now, now
	return func(t time.Time) { at = t }
}

func siAt(date, hm string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", date+" "+hm, ist)
	if err != nil {
		panic(err)
	}
	return t
}

// siOne runs one test for date and says what it found.
func siOne(t *testing.T, o *Ops, ctx context.Context, id, date string) siOut {
	t.Helper()
	e, err := o.siEnvFor(ctx, T, date)
	if err != nil {
		t.Fatal(err)
	}
	tt, ok := siTest(id)
	if !ok {
		t.Fatalf("no test %s", id)
	}
	out, err := tt.run(e)
	if err != nil {
		t.Fatalf("%s: %v", id, err)
	}
	return out
}

func siState(s siOut) string {
	switch {
	case s.skip != "":
		return SISkipped
	case s.found > 0:
		return SIFail
	}
	return SIPass
}

func siWant(t *testing.T, s siOut, want, what string) {
	t.Helper()
	if got := siState(s); got != want {
		t.Fatalf("%s: %s, want %s (%d of %d; %s%s; %v)", what, got, want, s.found, s.checked, s.summary, s.skip, s.examples)
	}
}

func siPut[V any](t *testing.T, o *Ops, ctx context.Context, coll, key string, v V) {
	t.Helper()
	if err := docs.Upsert(ctx, o.Docs, T, coll, key, v, "test"); err != nil {
		t.Fatal(err)
	}
}

// siTrader is a client who traded on the sample day.
func siTrader(t *testing.T, o *Ops, ctx context.Context) Client {
	t.Helper()
	ex, err := o.App.ExchangeTrades(ctx, T, D)
	if err != nil || len(ex) == 0 {
		t.Fatalf("no trades: %v", err)
	}
	c, err := o.Client(ctx, T, ex[0].UCC)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSelfInspectLibrary(t *testing.T) {
	seen := map[string]bool{}
	waiting := 0
	for _, x := range SelfInspectionTests() {
		if x.ID == "" || seen[x.ID] || x.Title == "" || x.Source == "" || x.Fix == "" || x.Screen == "" || x.ScreenName == "" || x.Checks == "" || x.Area == "" {
			t.Fatalf("incomplete test %+v", x)
		}
		seen[x.ID] = true
		if siWeight[x.Severity] == 0 || siDueDays[x.Severity] == 0 || !contains(Roles, x.Owner) {
			t.Fatalf("%s: severity %q, owner %q", x.ID, x.Severity, x.Owner)
		}
		if x.Real == (x.run == nil) || x.Real == (x.Waiting != "") {
			t.Fatalf("%s: a real test runs, one waiting for data says what for: %+v", x.ID, x)
		}
		if !x.Real {
			waiting++
		}
		if strings.Contains(strings.ToLower(x.Source+x.ToConfirm), "xxx") {
			t.Fatalf("%s: placeholder in the source", x.ID)
		}
	}
	for _, id := range []string{"funds_segregation", "ras_settlement", "cuspa_limit", "cuspa_notice", "cuspa_day6", "margin_penalties", "contract_notes", "complaints_21",
		"payouts_registered", "third_party_receipts", "kyc_complete", "inactive_accounts", "ddpi_purpose", "alerts_45", "holding_statement", "segmon", "weekly_statement",
		"debarred_trades", "ucc_match"} {
		if !seen[id] {
			t.Fatalf("the library lacks %s", id)
		}
	}
	if waiting != 1 || len(siTestIDs()) != len(seen) {
		t.Fatalf("%d waiting for data", waiting)
	}
	s, ok := bsSourceOf(siSource)
	if !ok || len(s.Lists) != 5 || s.Closed[0] != SIExcClosed || bsRecordKinds[siSource] == nil {
		t.Fatalf("the Self-inspection board source is not registered: %+v", s)
	}
	if siMask("BH0014") != "BH**14" || siMask("AB12") != "AB**" || siMask("Z9") != "Z*" || siMask("") != "" {
		t.Fatal("client codes are not masked")
	}
}

// Each implemented test, passing and failing on made-up data.
func TestSelfInspectTests(t *testing.T) {
	t.Run("funds_segregation", func(t *testing.T) {
		o, ctx := setup(t)
		siClock(o, siAt(D, "23:00"))
		siWant(t, siOne(t, o, ctx, "funds_segregation", D), SIPass, "the sample firm's client money")
		amt := money.Amount(200_000_000_00) // ₹2 crore of client money moved to the firm's own account
		if _, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: T, Key: "TEST:misuse", ValueDate: D, Narration: "made-up transfer", Source: ledger.SourceRef{Kind: "TEST", ID: "1"}, CreatedBy: "farhan.khan",
			Postings: []ledger.Posting{{Account: ledger.BankOwn, Debit: amt}, {Account: ledger.BankClient, Credit: amt}}}); err != nil {
			t.Fatal(err)
		}
		s := siOne(t, o, ctx, "funds_segregation", D)
		siWant(t, s, SIFail, "client money used by the firm")
		if !strings.Contains(s.examples[0], "Short by ₹") || strings.Contains(s.summary, "BH0") {
			t.Fatalf("firm-level totals only: %v %s", s.examples, s.summary)
		}
	})
	t.Run("ras_settlement", func(t *testing.T) {
		o, ctx := setup(t)
		siClock(o, siAt(D, "23:00"))
		s := siOne(t, o, ctx, "ras_settlement", D)
		siWant(t, s, SIFail, "the sample's idle credit balances")
		if !strings.Contains(s.examples[0], "no transaction for 30 days") || !strings.Contains(s.examples[0], "**") {
			t.Fatalf("example %v", s.examples)
		}
		if _, err := o.RunRAS(ctx, T, "farhan.khan", D, D); err != nil {
			t.Fatal(err)
		}
		siWant(t, siOne(t, o, ctx, "ras_settlement", D), SIPass, "after the settlement run")
	})
	t.Run("cuspa", func(t *testing.T) {
		o, ctx := setup(t)
		siClock(o, siAt(D, "23:00"))
		for _, id := range []string{"cuspa_limit", "cuspa_notice", "cuspa_day6"} {
			siWant(t, siOne(t, o, ctx, id, D), SIPass, id+" with no CUSPA lines")
		}
		ras, err := o.RAS(ctx, T, D)
		if err != nil || len(ras.Rows) == 0 {
			t.Fatal("no client in credit", err)
		}
		u := ras.Rows[0].UCC // in credit with no margin: everything over the limit
		siPut(t, o, ctx, cCUSPA, "CU-T1", CUSPAItem{ID: "CU-T1", Date: "2026-10-12", UCC: u, ISIN: "INE000T00011", Symbol: "MADEUP", Qty: 10, Value: money.DecInt(10000), Status: CUSPAHeld, PayOut: "2026-10-13"})
		siWant(t, siOne(t, o, ctx, "cuspa_limit", D), SIFail, "over the limit two days running")
		siWant(t, siOne(t, o, ctx, "cuspa_day6", D), SIPass, "day 3 after pay-out")
		siPut(t, o, ctx, cCUSPA, "CU-T1", CUSPAItem{ID: "CU-T1", Date: "2026-10-01", UCC: u, ISIN: "INE000T00011", Symbol: "MADEUP", Qty: 10, Value: money.DecInt(10000), Status: CUSPAHeld, PayOut: "2026-10-05"})
		siWant(t, siOne(t, o, ctx, "cuspa_day6", D), SIFail, "held past day 6")
		siPut(t, o, ctx, cCUSPA, "CU-T1", CUSPAItem{ID: "CU-T1", Date: "2026-10-12", UCC: u, ISIN: "INE000T00011", Symbol: "MADEUP", Released: 10, Value: money.DecInt(10000), Status: CUSPAReleased, PayOut: "2026-10-13"})
		siWant(t, siOne(t, o, ctx, "cuspa_limit", D), SIPass, "released")
		inv := CUSPAItem{ID: "CU-T2", Date: "2026-10-09", UCC: u, ISIN: "INE000T00011", Symbol: "MADEUP", Invoked: 5, Sold: 5, InvokedOn: "2026-10-15", Value: money.DecInt(5000), Status: CUSPASold, PayOut: "2026-10-12"}
		siPut(t, o, ctx, cCUSPA, inv.ID, inv)
		s := siOne(t, o, ctx, "cuspa_notice", D)
		siWant(t, s, SIFail, "invoked with no notice")
		if !strings.Contains(s.examples[0], "CU-T2") || strings.Contains(s.examples[0], u) {
			t.Fatalf("example %v", s.examples)
		}
		inv.NoticeOn = "2026-10-16" // after the invocation
		siPut(t, o, ctx, cCUSPA, inv.ID, inv)
		siWant(t, siOne(t, o, ctx, "cuspa_notice", D), SIFail, "notice after the invocation")
		inv.NoticeOn = "2026-10-14"
		siPut(t, o, ctx, cCUSPA, inv.ID, inv)
		siWant(t, siOne(t, o, ctx, "cuspa_notice", D), SIPass, "notice before the invocation")
	})
	t.Run("margin_penalties", func(t *testing.T) {
		o, ctx := setup(t)
		siClock(o, siAt(D, "23:00"))
		siWant(t, siOne(t, o, ctx, "margin_penalties", D), SIPass, "no penalties")
		c := siTrader(t, o, ctx)
		e, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: T, Key: "PEN:TEST", ValueDate: "2026-10-15", Narration: "made-up penalty", Source: ledger.SourceRef{Kind: "PENALTY", ID: "2026-10-15/" + c.UCC}, CreatedBy: "anita.rao",
			Postings: []ledger.Posting{{Account: ledger.ClientAccount(c.UCC), Debit: 50000}, {Account: ledger.BankOwn, Credit: 50000}}})
		if err != nil {
			t.Fatal(err)
		}
		s := siOne(t, o, ctx, "margin_penalties", D)
		siWant(t, s, SIFail, "a penalty debited without a pass")
		if !strings.Contains(s.examples[0], "no approved pass") {
			t.Fatalf("example %v", s.examples)
		}
		p := PenaltyPass{ID: penaltyPassID("2026-10-15", c.UCC), Date: "2026-10-15", UCC: c.UCC, Status: "Posted", Reason: PenaltyChequeBounce, Maker: "anita.rao", Checker: "farhan.khan",
			Evidence: []PeriodicFile{{ID: "F-1", Name: "return-memo.pdf"}}, Entry: e.ID}
		siPut(t, o, ctx, cPenaltyPass, p.ID, p)
		siWant(t, siOne(t, o, ctx, "margin_penalties", D), SIPass, "an allowed, approved pass")
		p.Reason, p.Checker, p.Evidence = "client asked", "anita.rao", nil
		siPut(t, o, ctx, cPenaltyPass, p.ID, p)
		s = siOne(t, o, ctx, "margin_penalties", D)
		siWant(t, s, SIFail, "a pass with no allowed reason")
		if x := s.examples[0]; !strings.Contains(x, "not one the rule allows") || !strings.Contains(x, "no evidence") || !strings.Contains(x, "second person") {
			t.Fatalf("example %v", s.examples)
		}
	})
	t.Run("contract_notes", func(t *testing.T) {
		o, ctx := setup(t)
		move := siClock(o, siAt("2026-10-19", "18:00"))
		s := siOne(t, o, ctx, "contract_notes", "2026-10-19")
		siWant(t, s, SIFail, "no notes for the sample day")
		if !strings.Contains(s.examples[0], "no contract note") {
			t.Fatalf("example %v", s.examples)
		}
		move(siAt(D, "20:00"))
		if _, err := o.GenerateNotes(ctx, T, "priya.nair", D); err != nil {
			t.Fatal(err)
		}
		notes, _ := o.Notes(ctx, T, D)
		for _, n := range notes { // every note delivered that evening
			if _, err := docs.Update(ctx, o.Docs, T, cNote, noteID(D, n.UCC, n.Part), "test", func(x *ContractNote) error {
				x.Status, x.SentAt = "Sent", siAt(D, "20:05").Format(time.RFC3339)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		}
		move(siAt("2026-10-19", "18:00"))
		siWant(t, siOne(t, o, ctx, "contract_notes", "2026-10-19"), SIPass, "notes sent the same evening")
		late := notes[0]
		if _, err := docs.Update(ctx, o.Docs, T, cNote, noteID(D, late.UCC, late.Part), "test", func(x *ContractNote) error { x.SentAt = siAt("2026-10-19", "10:00").Format(time.RFC3339); return nil }); err != nil {
			t.Fatal(err)
		}
		s = siOne(t, o, ctx, "contract_notes", "2026-10-19")
		siWant(t, s, SIFail, "a note sent after 24 hours")
		if s.found != 1 || !strings.Contains(s.examples[0], "after") {
			t.Fatalf("late note %d %v", s.found, s.examples)
		}
	})
	t.Run("complaints_21", func(t *testing.T) {
		o, ctx := setup(t)
		siClock(o, siAt(D, "23:00"))
		c := siTrader(t, o, ctx)
		tk := Ticket{ID: "TKT-T1", Kind: KindComplaint, UCC: c.UCC, Name: c.Name, Subject: "Made-up complaint", Source: "SEBI (SCORES)", Ref: "SCORES-T1", Status: "Open", OpenedOn: "2026-10-06", Due: "2026-10-27", Messages: []TicketMsg{}}
		siPut(t, o, ctx, cTicket, tk.ID, tk)
		siWant(t, siOne(t, o, ctx, "complaints_21", D), SIPass, "a complaint 10 days old")
		tk.OpenedOn, tk.Due = "2026-09-10", "2026-10-01"
		siPut(t, o, ctx, cTicket, tk.ID, tk)
		s := siOne(t, o, ctx, "complaints_21", D)
		siWant(t, s, SIFail, "a complaint 36 days old")
		if !strings.Contains(s.examples[0], "TKT-T1") || !strings.Contains(s.examples[0], "36 days") || strings.Contains(s.examples[0], c.Name) || strings.Contains(s.examples[0], c.UCC) {
			t.Fatalf("example %v", s.examples)
		}
		tk.Status, tk.ResolvedOn = "Resolved", "2026-10-15"
		siPut(t, o, ctx, cTicket, tk.ID, tk)
		siWant(t, siOne(t, o, ctx, "complaints_21", D), SIPass, "resolved")
	})
	t.Run("payouts_registered", func(t *testing.T) {
		o, ctx := setup(t)
		siClock(o, siAt(D, "23:00"))
		c := siTrader(t, o, ctx)
		pb := primaryBank(c)
		b := PayoutBatch{ID: "PB-T1", Date: D, Status: "Sent to bank", Items: []PayoutItem{{Request: "PR-T1", UCC: c.UCC, Bank: pb.Bank, IFSC: pb.IFSC, Account: pb.Account, Amount: 100000}}}
		siPut(t, o, ctx, cPOBatch, b.ID, b)
		siWant(t, siOne(t, o, ctx, "payouts_registered", D), SIPass, "paid to the primary account")
		b.Items[0].Account = "000099998888"
		siPut(t, o, ctx, cPOBatch, b.ID, b)
		s := siOne(t, o, ctx, "payouts_registered", D)
		siWant(t, s, SIFail, "paid to an account not registered")
		if !strings.Contains(s.examples[0], "XXXXXXXX8888") || strings.Contains(s.examples[0], "000099998888") {
			t.Fatalf("the account is not masked: %v", s.examples)
		}
	})
	t.Run("third_party_receipts", func(t *testing.T) {
		o, ctx := setup(t)
		siClock(o, siAt(D, "23:00"))
		siWant(t, siOne(t, o, ctx, "third_party_receipts", D), SIPass, "the sample's credits parked today")
		siPut(t, o, ctx, cBankTxn, "BT-T1", BankTxn{ID: "BT-T1", Date: "2026-10-07", Mode: "NEFT", Ref: "UTRMADEUP1", Remitter: "A MADE UP NAME", Account: "123456789012", Amount: 500000, Status: "Third-party"})
		s := siOne(t, o, ctx, "third_party_receipts", D)
		siWant(t, s, SIFail, "parked seven working days")
		if strings.Contains(s.examples[0], "MADE UP") || strings.Contains(s.examples[0], "123456789012") {
			t.Fatalf("the remitter shows: %v", s.examples)
		}
		siPut(t, o, ctx, cBankTxn, "BT-T1", BankTxn{ID: "BT-T1", Date: "2026-10-07", Mode: "NEFT", Ref: "UTRMADEUP1", Account: "123456789012", Amount: 500000, Status: "Returned"})
		siWant(t, siOne(t, o, ctx, "third_party_receipts", D), SIPass, "returned")
	})
	t.Run("kyc_complete", func(t *testing.T) {
		o, ctx := setup(t)
		siClock(o, siAt(D, "23:00"))
		s := siOne(t, o, ctx, "kyc_complete", D)
		siWant(t, s, SIFail, "KRA on hold in the sample")
		if !strings.Contains(s.examples[0], "KRA on hold") {
			t.Fatalf("example %v", s.examples)
		}
		siFixKYC(t, o, ctx)
		siWant(t, siOne(t, o, ctx, "kyc_complete", D), SIPass, "KRA validated")
	})
	t.Run("inactive_accounts", func(t *testing.T) {
		o, ctx := setup(t)
		siClock(o, siAt(D, "23:00"))
		v, _ := o.Dormancy(ctx, T, D)
		var due []string
		for _, r := range v.Rows {
			if r.State == "Due" {
				due = append(due, r.UCC)
			}
		}
		siWant(t, siOne(t, o, ctx, "inactive_accounts", D), SIFail, "due and not flagged")
		if _, err := o.FlagInactive(ctx, T, siCO, D, due); err != nil {
			t.Fatal(err)
		}
		siWant(t, siOne(t, o, ctx, "inactive_accounts", D), SIPass, "flagged")
		c := siTrader(t, o, ctx)
		if _, err := docs.Update(ctx, o.Docs, T, cClient, c.UCC, "test", func(x *Client) error { x.Inactive, x.InactiveSince = true, "2026-10-01"; return nil }); err != nil {
			t.Fatal(err)
		}
		s := siOne(t, o, ctx, "inactive_accounts", D)
		siWant(t, s, SIFail, "traded while inactive")
		if !strings.Contains(s.examples[0], "while inactive") {
			t.Fatalf("example %v", s.examples)
		}
	})
	t.Run("alerts_45", func(t *testing.T) {
		o, ctx := setup(t)
		siClock(o, siAt(D, "23:00"))
		siWant(t, siOne(t, o, ctx, "alerts_45", D), SIPass, "no old alerts")
		c := siTrader(t, o, ctx)
		siPut(t, o, ctx, cAlert, "A-TEST-1", Alert{ID: "A-TEST-1", Date: "2026-08-01", Source: "NSE", Type: "Made-up alert", UCC: c.UCC, Severity: "High", Status: "Under review", Notes: []string{}})
		s := siOne(t, o, ctx, "alerts_45", D)
		siWant(t, s, SIFail, "an alert 76 days old")
		if !strings.Contains(s.examples[0], "76 days") {
			t.Fatalf("example %v", s.examples)
		}
		siPut(t, o, ctx, cAlertLife, "A-TEST-1", AlertLife{AlertID: "A-TEST-1", ReferredOn: "2026-09-01", Observation: "referred"})
		siWant(t, siOne(t, o, ctx, "alerts_45", D), SIPass, "referred to the exchange")
	})
	t.Run("holding_statement", func(t *testing.T) {
		o, ctx := setup(t)
		siClock(o, siAt("2026-10-19", "23:00"))
		s := siOne(t, o, ctx, "holding_statement", "2026-10-19")
		siWant(t, s, SIFail, "the 16th and 17th not sent")
		if s.checked != 2 || s.found != 2 {
			t.Fatalf("days %d found %d (%v)", s.checked, s.found, s.examples)
		}
		siPut(t, o, ctx, cNSEHSub, "2026-10-16|1", NSEHoldingSubmission{ID: "2026-10-16|1", Date: "2026-10-16", Seq: 1, Status: "Accepted", Errors: []NSEHoldingError{}})
		// A nil day without the NIL Holding declaration is not sent.
		siPut(t, o, ctx, cNSEHSub, "2026-10-17|NIL", NSEHoldingSubmission{ID: "2026-10-17|NIL", Date: "2026-10-17", Status: hsNil, Errors: []NSEHoldingError{}})
		if s := siOne(t, o, ctx, "holding_statement", "2026-10-19"); s.found != 1 || !strings.Contains(strings.Join(s.examples, " "), "NIL Holding declaration is not recorded") {
			t.Fatalf("nil day %v", s.examples)
		}
		siPut(t, o, ctx, cNSEHSub, "2026-10-17|NIL", NSEHoldingSubmission{ID: "2026-10-17|NIL", Date: "2026-10-17", Status: hsNilDecl, Errors: []NSEHoldingError{}})
		siWant(t, siOne(t, o, ctx, "holding_statement", "2026-10-19"), SIPass, "accepted by API, and nil declared")
		// A Sunday special session is a data day.
		set, _ := o.Settings(ctx, T)
		set.SpecialSessions = []string{"2026-10-18"}
		if err := o.SaveSettings(ctx, T, "admin", set); err != nil {
			t.Fatal(err)
		}
		if s := siOne(t, o, ctx, "holding_statement", "2026-10-19"); s.checked != 3 || s.found != 1 {
			t.Fatalf("special session %d %d %v", s.checked, s.found, s.examples)
		}
		set.SpecialSessions = nil
		if err := o.SaveSettings(ctx, T, "admin", set); err != nil {
			t.Fatal(err)
		}
		siPut(t, o, ctx, cNSEHSub, "2026-10-17|NIL", NSEHoldingSubmission{ID: "2026-10-17|NIL", Date: "2026-10-17", Status: "Superseded", Errors: []NSEHoldingError{}})
		siPut(t, o, ctx, cReport, "2026-10-17|holdings", ReportStatus{Date: "2026-10-17", Status: "Uploaded", Ack: "ACK-T1"})
		siWant(t, siOne(t, o, ctx, "holding_statement", "2026-10-19"), SIPass, "accepted by API, and uploaded with an acknowledgement")
	})
	t.Run("segmon", func(t *testing.T) {
		o, ctx := setup(t)
		siClock(o, siAt("2026-10-19", "23:00"))
		s := siOne(t, o, ctx, "segmon", "2026-10-19")
		siWant(t, s, SIFail, "CC01 and SEGMON of the 16th not made")
		o.noteDailyFile(ctx, T, "priya.nair", "segmon-client-funds", "SEGMON_TEST_16102026.csv", D)
		o.noteDailyFile(ctx, T, "priya.nair", "ncl-client-collateral", "CC01_TEST_16102026.csv", D)
		siWant(t, siOne(t, o, ctx, "segmon", "2026-10-19"), SIPass, "both made")
		if _, err := o.SEGMONResponseFile(ctx, T, "priya.nair", D, "resp.csv", []byte("UCC Code,Error Description\nZZ999,Client not registered\n")); err != nil {
			t.Fatal(err)
		}
		siWant(t, siOne(t, o, ctx, "segmon", "2026-10-19"), SIFail, "a record rejected by NSE Clearing")
	})
	t.Run("weekly_statement", func(t *testing.T) {
		o, ctx := setup(t)
		siClock(o, siAt("2026-10-23", "23:00"))
		siWant(t, siOne(t, o, ctx, "weekly_statement", D), SISkipped, "the first week is not yet due")
		s := siOne(t, o, ctx, "weekly_statement", "2026-10-23")
		siWant(t, s, SIFail, "the week to the 17th not sent")
		c := siTrader(t, o, ctx)
		siPut(t, o, ctx, cDispatch, "DSP-T1", Dispatch{ID: "DSP-T1", Kind: DocFundsSec, Ref: "2026-10-12 to 2026-10-17", Date: "2026-10-17", UCC: c.UCC, Status: "Sent"})
		cs, _ := o.Clients(ctx, T)
		missed := 0
		for _, x := range cs {
			if x.Status == "Active" && fsRecentTrader(x, "2026-10-17") && x.UCC != c.UCC {
				missed++
			}
		}
		if missed > 0 {
			// Every active client gets the statement, a zero balance too.
			siWant(t, siOne(t, o, ctx, "weekly_statement", "2026-10-23"), SIFail, "active clients not sent")
			for i, x := range cs {
				siPut(t, o, ctx, cDispatch, fmt.Sprintf("DSP-T%d", i+2), Dispatch{ID: fmt.Sprintf("DSP-T%d", i+2), Kind: DocFundsSec, Ref: "2026-10-12 to 2026-10-17", Date: "2026-10-17", UCC: x.UCC, Status: "Sent"})
			}
		}
		siWant(t, siOne(t, o, ctx, "weekly_statement", "2026-10-23"), SIPass, "sent")
	})
	t.Run("debarred_trades", func(t *testing.T) {
		o, ctx := setup(t)
		siClock(o, siAt(D, "23:00"))
		siWant(t, siOne(t, o, ctx, "debarred_trades", D), SIPass, "the sample's lists")
		c := siTrader(t, o, ctx)
		siPut(t, o, ctx, cDebarred, "DEB-T1", Debarred{ID: "DEB-T1", PAN: c.PAN, Name: "A MADE UP ENTITY", Regulator: "SEBI", OrderNo: "WTM/MADEUP/2026", OrderDate: "2026-10-01"})
		s := siOne(t, o, ctx, "debarred_trades", D)
		siWant(t, s, SIFail, "traded while debarred")
		if s.found != 2 || strings.Contains(strings.Join(s.examples, " "), c.PAN) {
			t.Fatalf("found %d %v", s.found, s.examples)
		}
		siPut(t, o, ctx, cDebarred, "DEB-T1", Debarred{ID: "DEB-T1", PAN: c.PAN, Name: "A MADE UP ENTITY", Regulator: "SEBI", OrderNo: "WTM/MADEUP/2026", OrderDate: "2026-10-01", Till: "2026-10-10"})
		siWant(t, siOne(t, o, ctx, "debarred_trades", D), SIPass, "the debarment ended before the trade")
	})
	t.Run("ucc_match", func(t *testing.T) {
		o, ctx := setup(t)
		siClock(o, siAt(D, "23:00"))
		siWant(t, siOne(t, o, ctx, "ucc_match", D), SISkipped, "no exchange answers")
		c := siTrader(t, o, ctx)
		siPut(t, o, ctx, cUCCReg, "NSE|"+c.UCC, NSEUCIReg{Exchange: "NSE", UCC: c.UCC, Status: nseUCIAccepted, PANStatus: "A"})
		siWant(t, siOne(t, o, ctx, "ucc_match", D), SIPass, "accepted")
		siPut(t, o, ctx, cUCCReg, "BSE|"+c.UCC, BSEUCCReg{Exchange: "BSE", UCC: c.UCC, Status: "Rejected", Reason: "Made-up reason", PANStatus: "Invalid"})
		s := siOne(t, o, ctx, "ucc_match", D)
		siWant(t, s, SIFail, "rejected by BSE")
		if !strings.Contains(s.examples[0], "BSE") || !strings.Contains(s.examples[0], "PAN not verified") {
			t.Fatalf("example %v", s.examples)
		}
	})
	t.Run("bsda_review", func(t *testing.T) {
		o, ctx := setup(t)
		siClock(o, siAt(D, "23:00"))
		siWant(t, siOne(t, o, ctx, "bsda_review", D), SISkipped, "not a DP")
		c := siTrader(t, o, ctx)
		siPut(t, o, ctx, cBO, "1208000000000001", BOAccount{UCC: c.UCC, BOID: "1208000000000001", Depository: "CDSL", Status: "Active"})
		siWant(t, siOne(t, o, ctx, "bsda_review", D), SIFail, "the July–September review not applied by the 10th")
		siPut(t, o, ctx, cBSDAReview, "2026-Q3", BSDAReview{ID: "2026-Q3", AsOf: "2026-09-30", Status: "Applied"})
		siWant(t, siOne(t, o, ctx, "bsda_review", D), SIPass, "applied")
	})
	t.Run("daily_files", func(t *testing.T) {
		o, ctx := setup(t)
		siClock(o, siAt("2026-10-19", "23:00"))
		siWant(t, siOne(t, o, ctx, "daily_files", "2026-10-19"), SIFail, "the 19th's files never came")
		for _, d := range []string{D, "2026-10-19"} {
			cl, _ := o.DailyFileChecklist(ctx, T, d)
			for _, it := range cl.Items {
				if it.Status == "Missing" {
					o.noteDailyFile(ctx, T, "priya.nair", it.Format, siFileName(it.ID, it.Name), d)
				}
			}
		}
		siWant(t, siOne(t, o, ctx, "daily_files", "2026-10-19"), SIPass, "every file in")
	})
	t.Run("duties_overdue", func(t *testing.T) {
		o, ctx := setup(t)
		siClock(o, siAt(D, "23:00"))
		siWant(t, siOne(t, o, ctx, "duties_overdue", D), SIPass, "the sample's calendar")
		siPut(t, o, ctx, cObligation, "OB-MADEUP", Duty{ID: "OB-MADEUP", Title: "A made-up monthly return", Authority: "Test", Frequency: "Monthly", Rule: "D05", Role: RoleCompliance, Evidence: "—", Active: true, Since: "2026-09-01", Done: []DutyRun{}, Rev: dutyRev})
		s := siOne(t, o, ctx, "duties_overdue", D)
		siWant(t, s, SIFail, "a duty overdue since 5 September")
		if !strings.Contains(s.examples[0], "OB-MADEUP") {
			t.Fatalf("example %v", s.examples)
		}
	})
	t.Run("ddpi_purpose", func(t *testing.T) {
		o, ctx := setup(t)
		siClock(o, siAt(D, "23:00"))
		run, err := o.RunSelfInspection(ctx, T, siCO, D)
		if err != nil {
			t.Fatal(err)
		}
		r := siResultOf(t, run, "ddpi_purpose")
		if r.Status != SISkipped || !strings.Contains(r.Summary, "doesn't have the data") {
			t.Fatalf("ddpi %+v", r)
		}
	})
}

// siFileName is a made-up file name that the checklist's name pattern
// takes.
func siFileName(id, pattern string) string {
	if pattern == "" {
		return id + "_made_up.csv"
	}
	p := strings.NewReplacer(`\d{8}`, "20261016", `\d+`, "1", `(0[2-9]|[1-9]\d)`, "02", `\.`, ".", "$", "").Replace(pattern)
	if strings.HasPrefix(p, "^") {
		return p[1:]
	}
	if strings.HasSuffix(pattern, "$") {
		return id + p
	}
	return id + p + "x.csv"
}

// siFixKYC completes the KYC of every active client of the sample.
func siFixKYC(t *testing.T, o *Ops, ctx context.Context) {
	t.Helper()
	cs, _ := o.Clients(ctx, T)
	for _, c := range cs {
		if c.Status != "Active" {
			continue
		}
		if _, err := docs.Update(ctx, o.Docs, T, cClient, c.UCC, "test", func(x *Client) error {
			x.KRAStatus, x.IncomeProof, x.Incomplete = "Validated", true, nil
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func siResultOf(t *testing.T, run SIRun, id string) SIResult {
	t.Helper()
	for _, r := range run.Results {
		if r.Test == id {
			return r
		}
	}
	t.Fatalf("run %s has no result for %s", run.ID, id)
	return SIResult{}
}

func siExc(t *testing.T, o *Ops, ctx context.Context, id string) SIException {
	t.Helper()
	x, _, err := docs.Get[SIException](ctx, o.Docs, T, cSIExc, id)
	if err != nil {
		t.Fatal(err)
	}
	return x
}

// siCard is the board's card for an exception, with its list's status.
func siCard(t *testing.T, o *Ops, ctx context.Context, board, exc string) (Card, string) {
	t.Helper()
	v, err := o.GetBoard(ctx, T, "admin", board, false)
	if err != nil {
		t.Fatal(err)
	}
	c, ok := bsCardFor(v, siSource, exc)
	if !ok {
		t.Fatalf("no open card for %s", exc)
	}
	l, _ := boardList(v.Board, c.List)
	return c, l.Source
}

func siAudited(t *testing.T, o *Ops, ctx context.Context, action, object string, check func(map[string]any) bool) {
	t.Helper()
	evs, _ := o.App.Audit.List(ctx, T, 0)
	for _, e := range evs {
		if e.Action == action && e.Object == object && check(e.Detail) {
			return
		}
	}
	t.Fatalf("no %s of %s in the audit trail", action, object)
}

// Runs are stored as they were; exceptions become cards that move and
// close by themselves; accepting a risk is audited.
func TestSelfInspectRunsAndCards(t *testing.T) {
	o, ctx := setup(t)
	move := siClock(o, siAt(D, "23:00"))
	setUp, err := o.SetUpLedgerlineBoards(ctx, T, "admin", []string{siSource})
	if err != nil || len(setUp) != 1 || setUp[0].Name != "Self-inspection" {
		t.Fatalf("set up %+v %v", setUp, err)
	}
	board := setUp[0].Board
	run, err := o.RunSelfInspection(ctx, T, siCO, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Results) != len(siTests) || run.Date != D || run.Trigger != "run now" || run.Failed == 0 || run.Passed == 0 || run.New != run.Failed {
		t.Fatalf("run %+v", run)
	}
	if run.Score <= 0 || run.Score >= 100 || run.TotalWeight == 0 {
		t.Fatalf("score %.1f of %d", run.Score, run.TotalWeight)
	}
	for _, r := range run.Results {
		if (r.Status == SIPass || r.Status == SIFail) && len(r.Inputs) == 0 {
			t.Fatalf("%s: no input dates", r.Test)
		}
		for _, x := range r.Examples {
			for _, c := range []string{"BH00", "BH01"} { // the sample's codes are BH0001…
				if strings.Contains(x, c) {
					t.Fatalf("%s: a client code shows in %q", r.Test, x)
				}
			}
		}
	}
	got, err := o.SelfInspectionRun(ctx, T, "priya.nair", run.ID)
	if err != nil || got.ID != run.ID || len(got.Results) != len(run.Results) || got.Score != run.Score || got.Results[0].Summary != run.Results[0].Summary {
		t.Fatalf("stored run %+v %v", got.ID, err)
	}
	kyc := siResultOf(t, run, "kyc_complete")
	if kyc.Status != SIFail || kyc.Exception == "" {
		t.Fatalf("kyc %+v", kyc)
	}
	x := siExc(t, o, ctx, kyc.Exception)
	if x.Status != SIExcNew || x.Due != addDays(D, siDueDays["medium"]) || x.Found != kyc.Found || x.Owner != RoleCompliance {
		t.Fatalf("exception %+v", x)
	}
	c, st := siCard(t, o, ctx, board, x.ID)
	if st != SIExcNew || !strings.Contains(c.Title, "KYC complete for active clients · 3 found") || c.Due == "" || c.CreatedBy != bsActor || len(c.Labels) < 3 {
		t.Fatalf("card %+v in %s", c, st)
	}
	// The card window shows the exception live, for head-office staff only.
	h, err := o.BoardLinkRecord(ctx, T, "priya.nair", siSource, x.ID)
	if err != nil || !strings.Contains(strings.Join(h.Lines, "|"), "How to fix:") || !strings.Contains(h.Sub, "owner Compliance officer") {
		t.Fatalf("link record %+v %v", h, err)
	}
	if _, err := o.BoardLinkRecord(ctx, T, "meera.iyer", siSource, x.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("branch staff saw an exception: %v", err)
	}

	// Marked fixed, but still failing on the next run: back to Being fixed.
	if _, err := o.SetSelfInspectionException(ctx, T, siCO, x.ID, SIExcFixed, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("fixed with no note: %v", err)
	}
	if _, err := o.SetSelfInspectionException(ctx, T, siCO, x.ID, SIExcFixed, "KRA statuses updated"); err != nil {
		t.Fatal(err)
	}
	if _, st := siCard(t, o, ctx, board, x.ID); st != SIExcFixed {
		t.Fatalf("card in %s after fixed", st)
	}
	move(siAt("2026-10-19", "23:00"))
	run2, err := o.RunSelfInspection(ctx, T, siCO, "")
	if err != nil {
		t.Fatal(err)
	}
	if x = siExc(t, o, ctx, x.ID); x.Status != SIExcFixing || x.LastRun != run2.ID || x.History[len(x.History)-1].What != "still failing" {
		t.Fatalf("still failing %+v", x)
	}
	if _, st := siCard(t, o, ctx, board, x.ID); st != SIExcFixing {
		t.Fatalf("card in %s after still failing", st)
	}
	// Fixed for real: the next run closes it, and the card moves to Closed.
	siFixKYC(t, o, ctx)
	run3, err := o.RunSelfInspection(ctx, T, siCO, "")
	if err != nil {
		t.Fatal(err)
	}
	if r := siResultOf(t, run3, "kyc_complete"); r.Status != SIPass || r.Exception != x.ID || run3.Closed == 0 {
		t.Fatalf("passing %+v", r)
	}
	x = siExc(t, o, ctx, x.ID)
	if x.Status != SIExcClosed || x.ClosedRun != run3.ID || !strings.Contains(siDescribe(x).Handled, "Closed by itself") {
		t.Fatalf("closed %+v", x)
	}
	if c, st := siCard(t, o, ctx, board, x.ID); st != SIExcClosed || !c.DueDone || !strings.Contains(c.Title, "passed") {
		t.Fatalf("card %+v in %s", c, st)
	}

	// Moving cards: Being fixed works the exception; Accepted risk and
	// Closed are refused.
	files := siResultOf(t, run3, "daily_files")
	if files.Status != SIFail {
		t.Fatalf("daily files %+v", files)
	}
	fc, _ := siCard(t, o, ctx, board, files.Exception)
	v, _ := o.GetBoard(ctx, T, "admin", board, false)
	if _, err := o.MoveCard(ctx, T, "priya.nair", board, fc.ID, CardMove{List: bsListBy(t, v.Board, SIExcAccepted).ID}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("moved to Accepted risk: %v", err)
	}
	if _, err := o.MoveCard(ctx, T, "priya.nair", board, fc.ID, CardMove{List: bsListBy(t, v.Board, SIExcClosed).ID}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("moved to Closed: %v", err)
	}
	if _, err := o.MoveCard(ctx, T, "anita.rao", board, fc.ID, CardMove{List: bsListBy(t, v.Board, SIExcFixing).ID}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a risk manager worked an Ops maker's exception: %v", err)
	}
	if _, err := o.MoveCard(ctx, T, "priya.nair", board, fc.ID, CardMove{List: bsListBy(t, v.Board, SIExcFixing).ID}); err != nil {
		t.Fatal(err)
	}
	if fx := siExc(t, o, ctx, files.Exception); fx.Status != SIExcFixing || fx.History[len(fx.History)-1].By != "priya.nair" {
		t.Fatalf("moved card's exception %+v", fx)
	}

	// Accepting a risk: the compliance officer's own decision, audited.
	seg := siResultOf(t, run3, "segmon")
	if seg.Status != SIFail {
		t.Fatalf("segmon %+v", seg)
	}
	until := "2026-12-31"
	if _, err := o.AcceptSelfInspectionRisk(ctx, T, "priya.nair", seg.Exception, "Made-up reason for the test", until); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a maker accepted a risk: %v", err)
	}
	if _, err := o.AcceptSelfInspectionRisk(ctx, T, "admin", seg.Exception, "Made-up reason for the test", until); !errors.Is(err, ErrForbidden) {
		t.Fatalf("an admin accepted a risk: %v", err)
	}
	if _, err := o.AcceptSelfInspectionRisk(ctx, T, siCO, seg.Exception, "ok", until); !errors.Is(err, ErrInvalid) {
		t.Fatalf("accepted with no reason: %v", err)
	}
	if _, err := o.AcceptSelfInspectionRisk(ctx, T, siCO, seg.Exception, "NSE Clearing's portal is down; files kept to upload", "2026-10-01"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("accepted until a past date: %v", err)
	}
	ax, err := o.AcceptSelfInspectionRisk(ctx, T, siCO, seg.Exception, "NSE Clearing's portal is down; files kept to upload", until)
	if err != nil || ax.Status != SIExcAccepted || ax.Accepted.Role != RoleCompliance || ax.Accepted.By != siCO {
		t.Fatalf("accepted %+v %v", ax, err)
	}
	siAudited(t, o, ctx, "selfinspect.risk_accepted", seg.Exception, func(d map[string]any) bool {
		return fmt.Sprint(d["reason"]) == "NSE Clearing's portal is down; files kept to upload" && fmt.Sprint(d["until"]) == until && fmt.Sprint(d["role"]) == RoleCompliance
	})
	ac, st := siCard(t, o, ctx, board, seg.Exception)
	if st != SIExcAccepted || !strings.HasPrefix(ac.Due, until) {
		t.Fatalf("accepted card %+v in %s", ac, st)
	}
	run4, err := o.RunSelfInspection(ctx, T, siCO, "")
	if err != nil {
		t.Fatal(err)
	}
	if x := siExc(t, o, ctx, seg.Exception); x.Status != SIExcAccepted || x.LastRun != run4.ID {
		t.Fatalf("accepted risk after a run %+v", x)
	}
	// The acceptance ends: still failing, it is New again.
	move(siAt("2027-01-04", "23:00"))
	if _, err := o.RunSelfInspection(ctx, T, siCO, "2027-01-04"); err != nil {
		t.Fatal(err)
	}
	if x := siExc(t, o, ctx, seg.Exception); x.Status == SIExcAccepted {
		t.Fatalf("acceptance did not end %+v", x)
	}
	if _, err := o.RunSelfInspection(ctx, T, siCO, "2027-02-01"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("ran for a date to come: %v", err)
	}
	siAudited(t, o, ctx, "selfinspect.run", run.ID, func(d map[string]any) bool { return fmt.Sprint(d["trigger"]) == "run now" })
}

func TestSelfInspectMute(t *testing.T) {
	o, ctx := setup(t)
	siClock(o, siAt(D, "23:00"))
	if _, err := o.MuteSelfInspectionTest(ctx, T, "admin", "kyc_complete", "Made-up reason for the test", "2026-10-26"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("an admin muted a test: %v", err)
	}
	if _, err := o.MuteSelfInspectionTest(ctx, T, "priya.nair", "kyc_complete", "Made-up reason for the test", "2026-10-26"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a maker muted a test: %v", err)
	}
	if _, err := o.MuteSelfInspectionTest(ctx, T, siCO, "kyc_complete", "short", "2026-10-26"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("muted with no reason: %v", err)
	}
	if _, err := o.MuteSelfInspectionTest(ctx, T, siCO, "kyc_complete", "KRA re-validation drive under way", "2027-12-31"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("muted for more than 180 days: %v", err)
	}
	if _, err := o.MuteSelfInspectionTest(ctx, T, siCO, "no_such_test", "KRA re-validation drive under way", "2026-10-26"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("muted an unknown test: %v", err)
	}
	m, err := o.MuteSelfInspectionTest(ctx, T, siCO, "kyc_complete", "KRA re-validation drive under way", "2026-10-26")
	if err != nil {
		t.Fatal(err)
	}
	siAudited(t, o, ctx, "selfinspect.muted", "kyc_complete", func(d map[string]any) bool {
		return fmt.Sprint(d["reason"]) == "KRA re-validation drive under way" && fmt.Sprint(d["until"]) == "2026-10-26"
	})
	if _, err := o.MuteSelfInspectionTest(ctx, T, siCO, "kyc_complete", "KRA re-validation drive under way", "2026-10-30"); !errors.Is(err, ErrState) {
		t.Fatalf("muted twice: %v", err)
	}
	run, err := o.RunSelfInspection(ctx, T, siCO, "")
	if err != nil {
		t.Fatal(err)
	}
	r := siResultOf(t, run, "kyc_complete")
	if r.Status != SIMuted || r.MutedAs != SIFail || r.Mute != m.ID || r.Exception != "" || r.Found == 0 {
		t.Fatalf("muted result %+v", r)
	}
	if run.Muted != 1 {
		t.Fatalf("muted count %d", run.Muted)
	}
	v, _ := o.SelfInspection(ctx, T, siCO)
	for _, tv := range v.Tests {
		if tv.ID == "kyc_complete" && (tv.Mute == nil || tv.Mute.Until != "2026-10-26") {
			t.Fatalf("the screen does not show the mute %+v", tv.Mute)
		}
	}
	if _, err := o.UnmuteSelfInspectionTest(ctx, T, siCO, "kyc_complete", "Drive finished early"); err != nil {
		t.Fatal(err)
	}
	siAudited(t, o, ctx, "selfinspect.unmuted", "kyc_complete", func(d map[string]any) bool { return fmt.Sprint(d["reason"]) == "Drive finished early" })
	if _, err := o.UnmuteSelfInspectionTest(ctx, T, siCO, "kyc_complete", "again"); !errors.Is(err, ErrState) {
		t.Fatalf("unmuted twice: %v", err)
	}
	run, _ = o.RunSelfInspection(ctx, T, siCO, "")
	if r := siResultOf(t, run, "kyc_complete"); r.Status != SIFail || r.Exception == "" {
		t.Fatalf("after the mute %+v", r)
	}
}

func TestSelfInspectSummary(t *testing.T) {
	o, ctx := setup(t)
	move := siClock(o, siAt(D, "23:00"))
	r1, err := o.RunSelfInspection(ctx, T, siCO, "")
	if err != nil {
		t.Fatal(err)
	}
	seg := siResultOf(t, r1, "kyc_complete")
	if _, err := o.AcceptSelfInspectionRisk(ctx, T, siCO, seg.Exception, "Clients told; KRA re-validation drive under way", "2026-11-30"); err != nil {
		t.Fatal(err)
	}
	if _, err := o.MuteSelfInspectionTest(ctx, T, siCO, "inactive_accounts", "Flagging batch with the exchanges this week", "2026-10-23"); err != nil {
		t.Fatal(err)
	}
	move(siAt("2026-10-19", "23:00"))
	r2, err := o.RunSelfInspection(ctx, T, siCO, "")
	if err != nil {
		t.Fatal(err)
	}
	rep, err := o.SelfInspectionSummary(ctx, T, D, "2026-10-19")
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Runs) != 2 || rep.Runs[0].ID != r1.ID || rep.Runs[1].ID != r2.ID || rep.Totals.Runs != 2 || rep.Totals.FirstScore != r1.Score || rep.Totals.LastScore != r2.Score {
		t.Fatalf("runs %+v", rep.Totals)
	}
	if len(rep.Tests) != len(siTests) || rep.Totals.Waiting != 1 || len(rep.Mutes) != 1 || rep.Mutes[0].Test != "inactive_accounts" {
		t.Fatalf("tests %d mutes %+v", len(rep.Tests), rep.Mutes)
	}
	found := false
	for _, x := range rep.Exceptions {
		if x.Handled == "" || x.StatusName == "" {
			t.Fatalf("not described %+v", x)
		}
		if x.ID == seg.Exception {
			found = strings.Contains(x.Handled, "Risk accepted by "+siCO+" (Compliance officer)") && strings.Contains(x.Handled, "KRA re-validation")
		}
	}
	if !found || rep.Totals.Accepted != 1 || rep.Totals.Exceptions != len(rep.Exceptions) {
		t.Fatalf("exceptions %+v", rep.Totals)
	}
	// Only the period asked.
	if one, _ := o.SelfInspectionSummary(ctx, T, "2026-10-17", "2026-10-31"); len(one.Runs) != 1 || one.Runs[0].ID != r2.ID {
		t.Fatalf("one run expected, got %d", len(one.Runs))
	}
	if none, _ := o.SelfInspectionSummary(ctx, T, "2026-01-01", "2026-01-31"); len(none.Runs) != 0 || len(none.Exceptions) != 0 || len(none.Mutes) != 0 {
		t.Fatalf("January %+v", none.Totals)
	}
	if _, err := o.SelfInspectionSummary(ctx, T, "2026-10-19", D); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a period backwards: %v", err)
	}
}

func TestSelfInspectRoles(t *testing.T) {
	o, ctx := setup(t)
	siClock(o, siAt(D, "23:00"))
	for _, u := range []string{"meera.iyer", "sunrise.ap"} {
		if _, err := o.SelfInspection(ctx, T, u); !errors.Is(err, ErrForbidden) {
			t.Fatalf("%s opened the self-inspection: %v", u, err)
		}
	}
	for _, u := range []string{"priya.nair", "farhan.khan", "anita.rao"} {
		v, err := o.SelfInspection(ctx, T, u)
		if err != nil || v.Can["run"] || v.Can["accept"] || v.Can["mute"] {
			t.Fatalf("%s: %+v %v", u, v.Can, err)
		}
		if _, err := o.RunSelfInspection(ctx, T, u, ""); !errors.Is(err, ErrForbidden) {
			t.Fatalf("%s ran the self-inspection: %v", u, err)
		}
		if _, err := o.SaveSelfInspectionSettings(ctx, T, u, SISettings{At: "23:00"}); !errors.Is(err, ErrForbidden) {
			t.Fatalf("%s changed the schedule: %v", u, err)
		}
	}
	if v, _ := o.SelfInspection(ctx, T, siCO); !v.Can["run"] || !v.Can["accept"] || !v.Can["mute"] || v.Can["set_up_board"] {
		t.Fatalf("compliance %+v", v.Can)
	}
	if v, _ := o.SelfInspection(ctx, T, "admin"); !v.Can["run"] || v.Can["accept"] || v.Can["mute"] || !v.Can["set_up_board"] {
		t.Fatalf("admin %+v", v.Can)
	}
	run, err := o.RunSelfInspection(ctx, T, "admin", "")
	if err != nil {
		t.Fatal(err)
	}
	kyc := siResultOf(t, run, "kyc_complete").Exception // owned by the compliance officer
	if _, err := o.SetSelfInspectionException(ctx, T, "anita.rao", kyc, SIExcFixing, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a risk manager worked a compliance exception: %v", err)
	}
	if _, err := o.SetSelfInspectionException(ctx, T, "meera.iyer", kyc, SIExcFixing, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("branch staff worked an exception: %v", err)
	}
	if _, err := o.SetSelfInspectionException(ctx, T, siCO, kyc, SIExcClosed, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("closed by hand: %v", err)
	}
	if x, err := o.SetSelfInspectionException(ctx, T, siCO, kyc, SIExcFixing, ""); err != nil || x.Status != SIExcFixing {
		t.Fatalf("being fixed %+v %v", x, err)
	}
	if _, err := o.SelfInspectionTest(ctx, T, "meera.iyer", "kyc_complete"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("branch staff read a test: %v", err)
	}
	d, err := o.SelfInspectionTest(ctx, T, "priya.nair", "kyc_complete")
	if err != nil || len(d.Results) != 1 || len(d.Exceptions) != 1 || d.Test.Last == nil {
		t.Fatalf("test detail %+v %v", d, err)
	}
	if _, err := o.SelfInspectionRun(ctx, T, "sunrise.ap", run.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a partner read a run: %v", err)
	}
}

// The nightly run: once a business day at the firm's time, with an email
// to the compliance officer that carries counts only.
func TestSelfInspectNightly(t *testing.T) {
	o, ctx := setup(t)
	siClock(o, siAt(D, "23:00"))
	o.selfInspectTick(ctx, T, siAt(D, "22:00"))
	if rs, _ := o.siRuns(ctx, T); len(rs) != 0 {
		t.Fatalf("ran before its time: %d", len(rs))
	}
	o.selfInspectTick(ctx, T, siAt(D, "22:31"))
	o.selfInspectTick(ctx, T, siAt(D, "22:32"))
	rs, _ := o.siRuns(ctx, T)
	if len(rs) != 1 || rs[0].Trigger != "nightly" || rs[0].By != SystemActor || rs[0].Date != D {
		t.Fatalf("nightly runs %+v", rs)
	}
	if !strings.HasPrefix(rs[0].Mailed, "Sent") {
		t.Fatalf("compliance not told: %q", rs[0].Mailed)
	}
	box, _ := o.Outbox(ctx, T, "admin")
	mailed := false
	for _, m := range box {
		if m.Ref == "selfinspect:"+rs[0].ID {
			mailed = true
			for _, x := range rs[0].Results {
				if strings.Contains(m.Subject, x.Title) {
					t.Fatalf("a test's title in the email: %q", m.Subject)
				}
			}
			if !strings.Contains(m.Subject, fmt.Sprintf("%d of %d tests failed", rs[0].Failed, rs[0].Passed+rs[0].Failed)) || strings.Contains(m.Subject, "BH0") {
				t.Fatalf("subject %q", m.Subject)
			}
		}
	}
	if !mailed {
		t.Fatal("no email in the outbox")
	}
	// Switched off: no run the next business day.
	if _, err := o.SaveSelfInspectionSettings(ctx, T, siCO, SISettings{Off: true, At: "21:00"}); err != nil {
		t.Fatal(err)
	}
	o.selfInspectTick(ctx, T, siAt("2026-10-19", "23:00"))
	if rs, _ := o.siRuns(ctx, T); len(rs) != 1 {
		t.Fatalf("ran while switched off: %d", len(rs))
	}
	if _, err := o.SaveSelfInspectionSettings(ctx, T, siCO, SISettings{At: "9pm"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a bad time: %v", err)
	}
	if _, err := o.SaveSelfInspectionSettings(ctx, T, siCO, SISettings{At: "21:00"}); err != nil {
		t.Fatal(err)
	}
	o.selfInspectTick(ctx, T, siAt("2026-10-18", "23:00")) // a Sunday
	if rs, _ := o.siRuns(ctx, T); len(rs) != 1 {
		t.Fatalf("ran on a Sunday: %d", len(rs))
	}
	o.selfInspectTick(ctx, T, siAt("2026-10-19", "21:05"))
	rs, _ = o.siRuns(ctx, T)
	if len(rs) != 2 || rs[1].Date != "2026-10-19" {
		t.Fatalf("runs %d", len(rs))
	}
	v, err := o.SelfInspection(ctx, T, siCO)
	if err != nil || len(v.Trend) != 2 || v.Trend[1].Score != rs[1].Score || v.Last.ID != rs[1].ID || v.Real != len(siTests)-1 || v.Waiting != 1 {
		t.Fatalf("screen %+v %v", v.Trend, err)
	}
}

// The self-inspection's results go into an inspection pack as CSV files.
func TestSelfInspectionInPack(t *testing.T) {
	o, ctx := setup(t)
	siClock(o, siAt(D, "23:00"))
	if _, err := o.RunSelfInspection(ctx, T, siCO, D); err != nil {
		t.Fatal(err)
	}
	set, _ := o.Settings(ctx, T)
	b := &PackBuild{O: o, Tenant: T, Actor: siCO, From: D, To: D, Set: set, Today: D, Watermark: "Prepared for a test"}
	sec, ok := packSection("self_inspection")
	if !ok {
		t.Fatal("no self_inspection section")
	}
	if why := sec.Ready(ctx, o, T, PackItem{Section: "self_inspection"}); why != "" {
		t.Fatalf("not ready: %s", why)
	}
	p, err := sec.Build(ctx, b, PackItem{Section: "self_inspection", Period: "period"})
	if err != nil || p.NotAvailable != "" || p.Records == 0 {
		t.Fatalf("part %+v %v", p, err)
	}
	names := map[string]string{}
	for _, f := range p.Files {
		names[f.Name] = string(f.Data)
	}
	for _, n := range []string{"self_inspection_runs.csv", "self_inspection_results.csv", "self_inspection_exceptions.csv"} {
		if !strings.HasPrefix(names[n], "Prepared for a test") {
			t.Fatalf("%s missing or without the watermark: %q", n, names[n])
		}
	}
	if !strings.Contains(names["self_inspection_results.csv"], "ddpi_purpose") || !strings.Contains(p.Note, "1 run;") {
		t.Fatalf("results %q note %q", names["self_inspection_results.csv"][:200], p.Note)
	}
	// A period with no run says so.
	b.From, b.To = "2026-09-01", "2026-09-02"
	if p, _ := sec.Build(ctx, b, PackItem{Section: "self_inspection", Period: "period"}); !strings.Contains(p.NotAvailable, "did not run") {
		t.Fatalf("no run %+v", p)
	}
}
