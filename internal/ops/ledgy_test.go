package ops

import (
	"context"
	"errors"
	"strings"
	"testing"

	"ledgerline/internal/docs"
	"ledgerline/internal/help"
	"ledgerline/internal/money"
)

func TestLedgyAnswersAndChecks(t *testing.T) {
	o, ctx := setup(t)

	// The examples from the brief, as staff would type them.
	for q, want := range map[string]string{
		"how do I change the brokerage for a client": "change-brokerage",
		"how to add an AP":                           "add-ap",
		"how to deal with ucc mismatch":              "break-ucc-mismatch",
	} {
		a, err := o.Ledgy(ctx, T, "priya.nair", q, "", D)
		if err != nil || !a.Confident || a.Article == nil || a.Article.ID != want {
			t.Fatalf("%q: %+v %v", q, a.Article, err)
		}
	}

	// Why can't I close the day: the day-close checks, live.
	a, err := o.Ledgy(ctx, T, "farhan.khan", "why can't I close the day?", "dashboard", D)
	if err != nil || a.Article == nil || a.Article.ID != "eod-close" || len(a.Facts) == 0 || !strings.HasPrefix(a.Facts[0].Title, "Day close") {
		t.Fatalf("eod: %+v %v", a, err)
	}
	if len(a.Facts[0].Lines) < 5 {
		t.Fatalf("eod lines: %+v", a.Facts[0].Lines)
	}

	// A client named in the question is looked up.
	a, _ = o.Ledgy(ctx, T, "farhan.khan", "why is BH1001 blocked?", "", D)
	found := false
	for _, f := range a.Facts {
		found = found || strings.HasPrefix(f.Title, "BH1001")
	}
	if !found || a.Article == nil || a.Article.ID != "client-cant-trade" {
		t.Fatalf("client check: %+v", a)
	}

	// Breaks are listed for a recon question.
	a, _ = o.Ledgy(ctx, T, "priya.nair", "how do I clear the recon breaks", "recon", D)
	if len(a.Facts) == 0 || !strings.HasPrefix(a.Facts[0].Title, "Open breaks") {
		t.Fatalf("breaks: %+v", a.Facts)
	}

	// Who can.
	a, _ = o.Ledgy(ctx, T, "priya.nair", "who can approve payouts?", "", D)
	if len(a.Perms) == 0 || a.Perms[0].What != "Approving payouts" {
		t.Fatalf("perms: %+v", a.Perms)
	}
	// A pasted permission error.
	a, _ = o.Ledgy(ctx, T, "priya.nair", "Approving payouts needs Ops checker or Risk manager (you are Ops maker)", "money", D)
	if a.Article == nil || a.Article.ID != "permission-denied" || len(a.Perms) == 0 {
		t.Fatalf("pasted error: %+v", a)
	}

	// My queue.
	a, _ = o.Ledgy(ctx, T, "farhan.khan", "what's in my queue", "", D)
	if !a.Confident || a.Article == nil || a.Article.ID != "work-queue" {
		t.Fatalf("queue answer: %+v", a.Article)
	}
	if len(a.Facts) == 0 || !strings.HasPrefix(a.Facts[len(a.Facts)-1].Title, "Your work queue") {
		t.Fatalf("queue: %+v", a.Facts)
	}
}

func TestLedgyScopeGapsAndFeedback(t *testing.T) {
	o, ctx := setup(t)
	cs, _ := o.ClientMap(ctx, T)
	var outside string
	for u, c := range cs {
		if c.Branch != "Bengaluru" {
			outside = u
			break
		}
	}
	// Branch staff get answers but no facts about clients outside their
	// branch, and no firm-wide day-close facts.
	a, err := o.Ledgy(ctx, T, "meera.iyer", "why is "+outside+" blocked", "", D)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range a.Facts {
		if strings.HasPrefix(f.Title, outside) {
			t.Fatal("branch user saw another branch's client")
		}
	}
	a, _ = o.Ledgy(ctx, T, "meera.iyer", "why can't I close the day", "", D)
	for _, f := range a.Facts {
		if strings.HasPrefix(f.Title, "Day close") {
			t.Fatal("branch user saw firm-wide day close")
		}
	}

	// Something Ledgy can't answer is kept, masked.
	a, _ = o.Ledgy(ctx, T, "priya.nair", "what is the weather for ABCPI1234K at 9876543210", "", D)
	if a.Confident || a.Gap == "" || len(a.Suggest) == 0 {
		t.Fatalf("not sure: %+v", a)
	}
	if _, err := o.LedgyGaps(ctx, T, "priya.nair"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker saw gaps: %v", err)
	}
	gs, err := o.LedgyGaps(ctx, T, "vikram.shah")
	if err != nil || len(gs) != 1 || strings.Contains(gs[0].Question, "ABCPI1234K") || strings.Contains(gs[0].Question, "9876543210") {
		t.Fatalf("gaps: %+v %v", gs, err)
	}
	// An unhelpful answer joins the list too.
	if err := o.LedgyFeedback(ctx, T, "priya.nair", "how to add an AP", "", "add-ap", false); err != nil {
		t.Fatal(err)
	}
	gs, _ = o.LedgyGaps(ctx, T, "admin")
	if len(gs) != 2 {
		t.Fatalf("after feedback: %+v", gs)
	}
	// Stored encrypted like everything else.
	raw, _ := docs.List[map[string]any](ctx, o.Docs, T, cLedgyQ, "")
	if len(raw) != 2 {
		t.Fatalf("stored: %d", len(raw))
	}
}

type fakeWriter struct {
	q        string
	in, out  int
	provider string
	model    string
}

func (f *fakeWriter) Provider() string {
	if f.provider == "" {
		return "anthropic"
	}
	return f.provider
}
func (f *fakeWriter) Model() string {
	if f.model == "" {
		return "claude-haiku-5-5"
	}
	return f.model
}
func (f *fakeWriter) MaxOutput() int { return help.MaxOutputTokens }
func (f *fakeWriter) Write(_ context.Context, q string, arts []help.Article, _ []help.PermHit) (help.Reply, error) {
	f.q = q
	in, out := f.in, f.out
	if in == 0 {
		in, out = 2500, 400
	}
	return help.Reply{Text: "1. Open **" + arts[0].Title + "**.", Provider: f.Provider(), Model: f.Model(), Usage: help.Usage{InputTokens: in, OutputTokens: out}}, nil
}

func TestLedgyAIOnlyWhenSwitchedOn(t *testing.T) {
	o, ctx := setup(t)
	fw := &fakeWriter{}
	o.LedgyAI = fw
	prices, _ := LoadAIPrices("")
	o.Meter, _ = NewMeter(prices, "88", "2", "", "")
	a, _ := o.Ledgy(ctx, T, "priya.nair", "change brokerage for ABCPI1234K", "", D)
	if a.AI != "" {
		t.Fatal("AI used while the firm has it off")
	}
	set, _ := o.Settings(ctx, T)
	set.LedgyAI = true
	if err := o.SaveSettings(ctx, T, "admin", set); err != nil {
		t.Fatal(err)
	}
	// No credit yet: the standard answer, with a note.
	a, _ = o.Ledgy(ctx, T, "priya.nair", "change brokerage for ABCPI1234K", "", D)
	if a.AI != "" || !strings.Contains(a.AINote, "credit") {
		t.Fatalf("without credit: %q %q", a.AI, a.AINote)
	}
	if _, err := o.AddAICredit(ctx, T, "provider", money.Amount(1000), "INV-1"); err != nil { // ₹10
		t.Fatal(err)
	}
	a, _ = o.Ledgy(ctx, T, "priya.nair", "change brokerage for ABCPI1234K", "", D)
	if a.AI == "" || strings.Contains(fw.q, "ABCPI1234K") || !strings.Contains(fw.q, "[PAN]") {
		t.Fatalf("ai: %q note %q sent %q", a.AI, a.AINote, fw.q)
	}
}
