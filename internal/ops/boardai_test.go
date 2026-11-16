package ops

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"ledgerline/internal/docs"
	"ledgerline/internal/help"
	"ledgerline/internal/money"
)

// fakeComposer is an AI model that writes what it is told to, and keeps
// what it was sent. No network.
type fakeComposer struct {
	fakeWriter
	reply string
	sent  []string
	instr []string
	fail  bool
}

func (f *fakeComposer) Compose(_ context.Context, instruction, text string, maxOut int) (help.Reply, error) {
	f.sent, f.instr = append(f.sent, text), append(f.instr, instruction)
	if f.fail {
		return help.Reply{}, errors.New("HTTP 529")
	}
	return help.Reply{Text: f.reply, Provider: f.Provider(), Model: f.Model(), Usage: help.Usage{InputTokens: 900, OutputTokens: 120}}, nil
}

func TestCardAIGate(t *testing.T) {
	o, ctx := setup(t)
	maker, checker, observer := "priya.nair", "farhan.khan", "anita.rao"
	var client Client
	cs, _ := o.Clients(ctx, T)
	for _, c := range cs {
		if strings.Contains(c.Name, " ") && c.UCC != "" {
			client = c
			break
		}
	}
	if client.Name == "" {
		t.Fatal("no sample client with a full name")
	}
	b, _ := o.CreateBoard(ctx, T, maker, BoardInput{Name: "Client follow-ups"})
	b, _ = o.UpdateBoard(ctx, T, maker, b.ID, BoardInput{Members: []BoardPerson{{maker, BoardAdmin}, {checker, BoardMember}}})
	c, _ := o.AddCard(ctx, T, maker, b.ID, CardInput{List: b.Lists[0].ID, Title: "Bank proof for " + client.UCC,
		Description: client.Name + " (PAN ABCDE1234F) has to send a cancelled cheque before the payout."})
	o.EditChecklist(ctx, T, maker, b.ID, c.ID, ChecklistOp{Op: "add_list", Name: "Documents"})
	c, _, _ = o.GetCard(ctx, T, maker, b.ID, c.ID)
	o.EditChecklist(ctx, T, maker, b.ID, c.ID, ChecklistOp{Op: "add_item", Checklist: c.Checklists[0].ID, Name: "Cancelled cheque"})
	for i := 0; i < 35; i++ {
		if _, err := o.AddComment(ctx, T, checker, b.ID, c.ID, fmt.Sprintf("Note-%02d: called the client", i)); err != nil {
			t.Fatal(err)
		}
	}

	// No model on the server: off, with the reason, and nothing is sent.
	st, err := o.CardAI(ctx, T, maker, b.ID, c.ID)
	if err != nil || st.On || !strings.Contains(st.Why, "No AI model") {
		t.Fatalf("no model %+v %v", st, err)
	}
	if _, err := o.SummariseCard(ctx, T, maker, b.ID, c.ID); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "No AI model") {
		t.Fatalf("summary with no model: %v", err)
	}

	// A model, but the feature is off in Settings.
	fc := &fakeComposer{reply: "[CLIENT-1] still owes the cancelled cheque; [STAFF-1] has called them."}
	o.LedgyAI, o.Meter = fc, meter(t, "2", "")
	if st, _ := o.CardAI(ctx, T, maker, b.ID, c.ID); st.On || !strings.Contains(st.Why, "Settings") {
		t.Fatalf("feature off %+v", st)
	}
	if _, err := o.SummariseCard(ctx, T, maker, b.ID, c.ID); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "AI on board cards") || len(fc.sent) != 0 {
		t.Fatalf("summary while off: %v (%d sent)", err, len(fc.sent))
	}
	set, _ := o.Settings(ctx, T)
	set.BoardAI = true
	if err := o.SaveSettings(ctx, T, "admin", set); err != nil {
		t.Fatal(err)
	}
	// On, but no credit.
	if _, err := o.SummariseCard(ctx, T, maker, b.ID, c.ID); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "credit") || len(fc.sent) != 0 {
		t.Fatalf("summary without credit: %v", err)
	}
	if _, err := o.AddAICredit(ctx, T, "provider", money.Amount(10000), "INV-T"); err != nil {
		t.Fatal(err)
	}
	st, _ = o.CardAI(ctx, T, observer, b.ID, c.ID)
	if !st.On || st.CanSuggest {
		t.Fatalf("observer %+v", st)
	}
	if st, _ = o.CardAI(ctx, T, checker, b.ID, c.ID); !st.On || !st.CanSuggest {
		t.Fatalf("member %+v", st)
	}

	// On: the masked card text goes out; the reply comes back with the
	// values put back. Nothing is posted.
	r, err := o.SummariseCard(ctx, T, observer, b.ID, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(fc.sent) != 1 || fc.instr[0] != cardAISummaryInstr {
		t.Fatalf("sent %d", len(fc.sent))
	}
	sent := fc.sent[0]
	for _, raw := range []string{client.Name, client.UCC, "ABCDE1234F", "Farhan Khan", "Priya Nair"} {
		if strings.Contains(sent, raw) {
			t.Errorf("%q sent to the model unmasked", raw)
		}
	}
	for _, tok := range []string{"[CLIENT-1]", "[CLIENT-CODE-1]", "[PAN-1]", "Cancelled cheque", "Note-34", "Note-05"} {
		if !strings.Contains(sent, tok) {
			t.Errorf("prompt lacks %q:\n%s", tok, sent)
		}
	}
	if strings.Contains(sent, "Note-04") || !strings.Contains(sent, "30 of 35") {
		t.Errorf("more than the last 30 comments sent:\n%s", sent)
	}
	if !strings.Contains(r.Text, client.Name) || strings.Contains(r.Text, "[CLIENT-1]") || r.By == "" || r.Note == "" {
		t.Fatalf("reply %+v", r)
	}
	if after, _, _ := o.GetCard(ctx, T, maker, b.ID, c.ID); len(after.Comments) != 35 {
		t.Fatal("the summary was posted")
	}
	log, _ := o.AILog(ctx, T, "vikram.shah", "", "")
	if len(log.Entries) != 1 || log.Entries[0].Feature != AIFeatureBoards || log.Entries[0].Outcome != AIAnswered || log.Entries[0].Charge <= 0 ||
		strings.Contains(log.Entries[0].Prompt, client.Name) || strings.Contains(log.Entries[0].Answer, client.Name) {
		t.Fatalf("AI log %+v", log.Entries)
	}

	// Suggestions: for members, read from the model's lines.
	fc.reply = "1. Ask [CLIENT-1] for the cheque\n- Verify the bank proof\n* [ ] Update the client master\nSteps:\n\nVerify the bank proof\n" +
		"Step 5\nStep 6\nStep 7\nStep 8\nStep 9\nStep 10\nStep 11\nStep 12"
	if _, err := o.SuggestChecklist(ctx, T, observer, b.ID, c.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("observer asked for a checklist: %v", err)
	}
	sg, err := o.SuggestChecklist(ctx, T, checker, b.ID, c.ID)
	if err != nil || len(sg.Items) != 10 || sg.Items[0] != "Ask "+client.Name+" for the cheque" || sg.Items[1] != "Verify the bank proof" || sg.Items[2] != "Update the client master" || sg.Items[3] != "Step 5" {
		t.Fatalf("suggestions %q %v", sg.Items, err)
	}
	if fc.instr[1] != cardAIChecklistInstr || strings.Contains(fc.sent[1], client.Name) || strings.Contains(fc.sent[1], "Note-") || !strings.Contains(fc.sent[1], "Cancelled cheque") {
		t.Fatalf("checklist prompt %q", fc.sent[1])
	}
	if after, _, _ := o.GetCard(ctx, T, maker, b.ID, c.ID); len(after.Checklists) != 1 {
		t.Fatal("suggestions were added")
	}
	if got := suggestedItems("NONE"); len(got) != 0 {
		t.Fatalf("NONE read as %q", got)
	}

	// The provider fails: the person is told, the call is logged.
	fc.fail = true
	if _, err := o.SummariseCard(ctx, T, maker, b.ID, c.ID); !errors.Is(err, ErrState) {
		t.Fatalf("provider error: %v", err)
	}
	fc.fail = false
	// The kill switch takes effect on the next call.
	if _, err := o.SetAIOff(ctx, T, "vikram.shah", true, "Vendor review"); err != nil {
		t.Fatal(err)
	}
	n := len(fc.sent)
	if _, err := o.SummariseCard(ctx, T, maker, b.ID, c.ID); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "Vendor review") || len(fc.sent) != n {
		t.Fatalf("kill switch: %v", err)
	}
	if st, _ := o.CardAI(ctx, T, maker, b.ID, c.ID); st.On || !strings.Contains(st.Why, "Vendor review") {
		t.Fatalf("state with the kill switch %+v", st)
	}
	o.SetAIOff(ctx, T, "vikram.shah", false, "Review done")

	// Never on a Ledgerline board.
	lb, _ := o.CreateBoard(ctx, T, "admin", BoardInput{Name: "Breaks (test)"})
	lc, _ := o.AddCard(ctx, T, "admin", lb.ID, CardInput{List: lb.Lists[0].ID, Title: "A break for " + client.UCC})
	docs.Update(ctx, o.Docs, T, cBoard, lb.ID, "admin", func(x *Board) error { x.Kind = BoardLedgerline; return nil })
	if st, err := o.CardAI(ctx, T, "admin", lb.ID, lc.ID); err != nil || st.On || !strings.Contains(st.Why, "Ledgerline boards") {
		t.Fatalf("ledgerline board %+v %v", st, err)
	}
	if _, err := o.SummariseCard(ctx, T, "admin", lb.ID, lc.ID); !errors.Is(err, ErrInvalid) || len(fc.sent) != n {
		t.Fatalf("summary on a Ledgerline board: %v", err)
	}
	// Someone who cannot see the board learns nothing.
	if _, err := o.CardAI(ctx, T, "meera.iyer", b.ID, c.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("branch: %v", err)
	}
	// The inventory has the feature, on while Settings has it on.
	inv, _ := o.AIInventory(ctx, T, "vikram.shah")
	found := false
	for _, s := range inv {
		if s.ID == AIFeatureBoards {
			found = s.On && strings.Contains(s.DataSent, "replaced by tokens") && strings.Contains(s.Switch, "AI on board cards")
			if !found {
				t.Fatalf("inventory entry %+v", s)
			}
		}
	}
	if !found {
		t.Fatal("no inventory entry for AI on board cards")
	}
}
