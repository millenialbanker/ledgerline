package ops

import (
	"errors"
	"strings"
	"testing"

	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// Margin penalties may reach a client's ledger only for a dishonoured
// cheque or a disclosed hedge break, with evidence, approved by a second
// person (NSE/INSP/64315). Autopilot only prepares them.
func TestPenaltyPassThrough(t *testing.T) {
	o, ctx := setup(t)
	if DefaultSettings().PenaltyPassThrough {
		t.Fatal("pass-through is on by default")
	}
	rep, err := o.Margins(ctx, T, D)
	if err != nil {
		t.Fatal(err)
	}
	var row MarginRow
	for _, r := range rep.Rows {
		if r.Penalty.Round(money.UnitPaisa) > 0 {
			row = r
			break
		}
	}
	if row.UCC == "" {
		t.Fatal("no client with a penalty in the sample day")
	}
	u := row.UCC
	penalties := func() int {
		st, _ := o.App.Ledger.Statement(ctx, T, ledger.ClientAccount(u), "", D)
		n := 0
		for _, l := range st.Lines {
			if strings.Contains(l.Narration, "penalty") {
				n++
			}
		}
		return n
	}
	runAuto := func() {
		t.Helper()
		if _, err := o.RunStage(ctx, T, "admin", D, "eod", "penalties"); err != nil {
			t.Fatal(err)
		}
	}
	// Off (the default): nothing is prepared or posted.
	runAuto()
	if penalties() != 0 {
		t.Fatal("penalty posted with pass-through off")
	}
	if _, err := o.ProposePenaltyPass(ctx, T, "vikram.shah", PenaltyPassRequest{Date: D, UCC: u, Reason: PenaltyHedgeBreak}); !errors.Is(err, ErrState) {
		t.Fatalf("proposed with pass-through off: %v", err)
	}
	// A firm that had it on keeps it, but every event needs a reason.
	set, _ := o.Settings(ctx, T)
	set.PenaltyPassThrough = true
	if err := docs.Upsert(ctx, o.Docs, T, cSettings, "main", set, "test"); err != nil {
		t.Fatal(err)
	}
	// Even on Auto (a saved policy) Autopilot only prepares the item.
	if _, err := o.SetAutoPolicy(ctx, T, "admin", AutoPolicy{ID: "penalties", Mode: ModeAuto}); err == nil {
		t.Fatal("penalties could be set to Auto")
	}
	runAuto()
	if penalties() != 0 {
		t.Fatal("Autopilot posted a penalty to a client")
	}
	open := func(key string) *WorkItem {
		ws, _ := o.WorkItems(ctx, T, "Open")
		for i := range ws {
			if ws[i].Key == wPenaltyPass+":"+key {
				return &ws[i]
			}
		}
		return nil
	}
	if open(penaltyPassID(D, u)) == nil {
		t.Fatal("no decision item prepared")
	}
	runAuto() // a later run does not clear the undecided item
	if open(penaltyPassID(D, u)) == nil {
		t.Fatal("decision item cleared by a run")
	}

	ev, err := o.UploadPenaltyEvidence(ctx, T, "vikram.shah", "cheque-return-memo.pdf", []byte("%PDF-1.4\n% return memo\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.UploadPenaltyEvidence(ctx, T, "vikram.shah", "x.txt", []byte("plain text")); err == nil {
		t.Fatal("text accepted as evidence")
	}
	base := PenaltyPassRequest{Date: D, UCC: u, Evidence: []string{ev.ID}}
	try := func(actor string, mod func(*PenaltyPassRequest)) error {
		q := base
		mod(&q)
		_, err := o.ProposePenaltyPass(ctx, T, actor, q)
		return err
	}
	if err := try("priya.nair", func(q *PenaltyPassRequest) { q.Reason = PenaltyChequeBounce; q.Cheque = "x" }); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker proposed: %v", err)
	}
	if err := try("vikram.shah", func(q *PenaltyPassRequest) { q.Reason = "late_payment" }); err == nil {
		t.Fatal("reason outside the circular accepted")
	}
	if err := try("vikram.shah", func(q *PenaltyPassRequest) { q.Reason = PenaltyHedgeBreak; q.PolicyNotifiedOn = "2026-01-05" }); err == nil {
		t.Fatal("hedge break without the policy clause")
	}
	if err := try("vikram.shah", func(q *PenaltyPassRequest) {
		q.Reason, q.PolicyClause, q.PolicyNotifiedOn = PenaltyHedgeBreak, "RMS 7.3", addDays(D, -3)
	}); err == nil {
		t.Fatal("policy notified less than 7 days before")
	}
	if err := try("vikram.shah", func(q *PenaltyPassRequest) { q.Reason, q.Cheque = PenaltyChequeBounce, "000999" }); err == nil {
		t.Fatal("cheque bounce without a dishonoured cheque")
	}
	if err := try("vikram.shah", func(q *PenaltyPassRequest) { q.Date = "2024-10-31"; q.Reason = PenaltyHedgeBreak }); err == nil {
		t.Fatal("penalty before 1 Nov 2024 passed on")
	}

	// The client's cheque bounced.
	chq, err := o.RecordReceipt(ctx, T, "priya.nair", Receipt{Date: D, UCC: u, Mode: "Cheque", Ref: "004711", DraweeBank: "HDFC Bank", Amount: 1000000})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.DecideReceipt(ctx, T, "farhan.khan", chq.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := o.ClearCheque(ctx, T, "priya.nair", chq.ID, D, true, false, "Funds insufficient"); err != nil {
		t.Fatal(err)
	}
	if err := try("vikram.shah", func(q *PenaltyPassRequest) { q.Reason, q.Cheque, q.Evidence = PenaltyChequeBounce, "004711", nil }); err == nil {
		t.Fatal("proposed without evidence")
	}
	q := base
	q.Reason, q.Cheque = PenaltyChequeBounce, "004711"
	p, err := o.ProposePenaltyPass(ctx, T, "vikram.shah", q)
	if err != nil || p.Status != "Waiting for approval" || p.ChequeReceipt != chq.ID {
		t.Fatalf("propose: %+v %v", p, err)
	}
	if penalties() != 0 {
		t.Fatal("posted before approval")
	}
	if open(penaltyPassID(D, u)) != nil {
		t.Fatal("decision item still open after the reason was given")
	}
	if _, err := o.DecidePenaltyPass(ctx, T, "vikram.shah", p.ID, true, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("proposer approved: %v", err)
	}
	w := open("approve:" + p.ID)
	if w == nil || w.Proposed == nil || w.Proposed.Kind != "penalty.pass" {
		t.Fatalf("no approval item: %+v", w)
	}
	if _, err := o.CloseWorkItem(ctx, T, "farhan.khan", w.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	p, _, _ = docs.Get[PenaltyPass](ctx, o.Docs, T, cPenaltyPass, p.ID)
	if p.Status != "Posted" || p.Checker != "farhan.khan" || p.Entry == "" {
		t.Fatalf("after approval: %+v", p)
	}
	if penalties() != 1 {
		t.Fatalf("%d penalty entries", penalties())
	}
	// The client's margin statement says why and with what evidence.
	ms, err := o.MarginStatement(ctx, T, D, u)
	if err != nil {
		t.Fatal(err)
	}
	all := strings.Join(ms.Notes, " ")
	for _, s := range ms.Sections {
		for _, r := range s.Rows {
			all += " " + strings.Join(r, " ")
		}
	}
	for _, want := range []string{"dishonoured", "004711", "cheque-return-memo.pdf", "charged to your account"} {
		if !strings.Contains(all, want) {
			t.Fatalf("margin statement lacks %q: %s", want, all)
		}
	}
	// The client was sent the letter with the evidence.
	if p.Letter == "" {
		t.Fatal("no letter to the client")
	}
	if d, _, err := docs.Get[Dispatch](ctx, o.Docs, T, cDispatch, p.Letter); err != nil {
		t.Fatal(err)
	} else if d.Status == "Sent" && (len(d.Files) != 2 || d.Files[1].RawID != ev.ID) {
		t.Fatalf("letter files: %+v", d.Files)
	}
	// Settled: a second run neither re-raises nor re-posts.
	runAuto()
	if open(penaltyPassID(D, u)) != nil || penalties() != 1 {
		t.Fatal("penalty raised or posted again")
	}
	if _, err := o.DecidePenaltyPass(ctx, T, "anita.rao", p.ID, true, ""); !errors.Is(err, ErrState) {
		t.Fatalf("decided twice: %v", err)
	}
}
