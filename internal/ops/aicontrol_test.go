package ops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/help"
	"ledgerline/internal/money"
)

// aiFirm is a firm with AI phrasing on and credit, its writer counting calls.
func aiFirm(t *testing.T) (*Ops, context.Context, *countingWriter) {
	t.Helper()
	o, ctx := setup(t)
	o.Now = func() time.Time { return time.Date(2026, 10, 20, 11, 0, 0, 0, ist) }
	w := &countingWriter{}
	o.LedgyAI, o.Meter = w, meter(t, "2", "")
	set, _ := o.Settings(ctx, T)
	set.LedgyAI = true
	if err := o.SaveSettings(ctx, T, "admin", set); err != nil {
		t.Fatal(err)
	}
	if _, err := o.AddAICredit(ctx, T, "provider", money.Amount(10000), "INV-T"); err != nil {
		t.Fatal(err)
	}
	return o, ctx, w
}

// countingWriter is an AI model that echoes the question it was sent.
type countingWriter struct {
	fakeReader
	calls int
	sent  []string
	fail  bool
}

func (c *countingWriter) Write(ctx context.Context, q string, arts []help.Article, perms []help.PermHit) (help.Reply, error) {
	c.calls++
	c.sent = append(c.sent, q)
	if c.fail {
		return help.Reply{}, errors.New("HTTP 529")
	}
	r, err := c.fakeReader.Write(ctx, q, arts, perms)
	r.Text += " You asked: " + q
	return r, err
}

const aiQ = "how do I add an AP"

func TestAIKillSwitch(t *testing.T) {
	o, ctx, w := aiFirm(t)
	a, err := o.Ledgy(ctx, T, "priya.nair", aiQ, "", D)
	if err != nil || a.AI == "" || w.calls != 1 {
		t.Fatalf("AI on: %q %q %d %v", a.AI, a.AINote, w.calls, err)
	}

	// Only an Admin or the compliance officer, and only with a reason.
	if _, err := o.SetAIOff(ctx, T, "priya.nair", true, "testing"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker switched AI off: %v", err)
	}
	if _, err := o.SetAIOff(ctx, T, "vikram.shah", true, " "); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no reason accepted: %v", err)
	}
	if _, err := o.SetAIOff(ctx, T, "vikram.shah", true, "Vendor review pending"); err != nil {
		t.Fatal(err)
	}
	// Takes effect on the next question: Ledgy's standard answer, no call.
	a, _ = o.Ledgy(ctx, T, "priya.nair", aiQ, "", D)
	if a.AI != "" || a.Article == nil || w.calls != 1 || !strings.Contains(a.AINote, "Vendor review pending") {
		t.Fatalf("firm off: %q %q calls %d", a.AI, a.AINote, w.calls)
	}
	evs, _ := o.App.Audit.List(ctx, T, 0)
	found := false
	for _, e := range evs {
		found = found || (e.Action == "ai.switched_off" && fmt.Sprint(e.Detail["reason"]) == "Vendor review pending" && e.Actor == "vikram.shah")
	}
	if !found {
		t.Fatal("switching off not audited with the reason")
	}
	if _, err := o.SetAIOff(ctx, T, "admin", false, "Review done"); err != nil {
		t.Fatal(err)
	}
	if a, _ = o.Ledgy(ctx, T, "priya.nair", aiQ, "", D); a.AI == "" || w.calls != 2 {
		t.Fatalf("back on: %q calls %d", a.AI, w.calls)
	}

	// One provider off: the connected one is refused, another is not.
	if _, err := o.SetAIProviderOff(ctx, T, "vikram.shah", "openai", true, "not approved"); err != nil {
		t.Fatal(err)
	}
	if a, _ = o.Ledgy(ctx, T, "priya.nair", aiQ, "", D); a.AI == "" || w.calls != 3 {
		t.Fatalf("other provider off stopped anthropic: %q", a.AINote)
	}
	if _, err := o.SetAIProviderOff(ctx, T, "vikram.shah", "anthropic", true, "data transfer review"); err != nil {
		t.Fatal(err)
	}
	if a, _ = o.Ledgy(ctx, T, "priya.nair", aiQ, "", D); a.AI != "" || w.calls != 3 || !strings.Contains(a.AINote, "Anthropic Claude") {
		t.Fatalf("provider off: %q %q", a.AI, a.AINote)
	}
	if _, err := o.SetAIProviderOff(ctx, T, "vikram.shah", "skynet", true, "x"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown provider: %v", err)
	}
	o.SetAIProviderOff(ctx, T, "vikram.shah", "anthropic", false, "approved")

	// The server-wide switch (LEDGERLINE_AI_OFF) stops every firm.
	o.AIOff = true
	if a, _ = o.Ledgy(ctx, T, "priya.nair", aiQ, "", D); a.AI != "" || w.calls != 3 || !strings.Contains(a.AINote, "LEDGERLINE_AI_OFF") {
		t.Fatalf("server off: %q %q", a.AI, a.AINote)
	}
	o.AIOff = false

	// Bill reading goes through the same gate.
	set, _ := o.Settings(ctx, T)
	set.BillAI = true
	o.SaveSettings(ctx, T, "admin", set)
	jpeg := append([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0x10, 'J', 'F', 'I', 'F', 0}, make([]byte, 200)...)
	sc, err := o.ScanBill(ctx, T, "priya.nair", "photo.jpg", jpeg, false)
	if err != nil || !sc.AIOffer {
		t.Fatalf("scan: %+v %v", sc, err)
	}
	o.SetAIOff(ctx, T, "vikram.shah", true, "incident 42")
	if _, err := o.ReadBillWithAI(ctx, T, "priya.nair", sc.ID); err == nil || !strings.Contains(err.Error(), "incident 42") || w.fakeReader.calls != 0 {
		t.Fatalf("bill read while off: %v (%d calls)", err, w.fakeReader.calls)
	}
	v, err := o.AIControlsScreen(ctx, T, "vikram.shah")
	if err != nil || !v.Controls.Off || v.Killed == "" || len(v.Controls.Changes) < 5 {
		t.Fatalf("screen %+v %v", v.Controls, err)
	}
	if _, err := o.AIControlsScreen(ctx, T, "priya.nair"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker saw AI controls: %v", err)
	}
}

func TestAIInventory(t *testing.T) {
	o, ctx, _ := aiFirm(t)
	inv, err := o.AIInventory(ctx, T, "vikram.shah")
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]AISystem{}
	for _, s := range inv {
		by[s.ID] = s
	}
	// Every AI feature in the code has an entry, with the facts filled.
	for _, f := range aiFeatures {
		s, ok := by[f]
		if !ok {
			t.Fatalf("no inventory entry for %s", f)
		}
		if s.Provider != "anthropic" || s.Model == "" || s.DataSent == "" || s.WhoCanUse == "" || len(s.Controls) < 5 || s.Purpose == "" {
			t.Fatalf("%s incomplete: %+v", f, s)
		}
		if s.OrderExecution != "N" || s.Discretionary != "N" || s.Advice != "N" || s.CyberSecurity != "N" || !strings.Contains(s.Implemented, "Vendor (Ledgerline)") {
			t.Fatalf("%s answers: %+v", f, s)
		}
	}
	if !by[AIFeatureLedgy].On || by[AIFeatureBillScan].On || !strings.Contains(by[AIFeatureBillScan].OffWhy, "Settings") {
		t.Fatalf("on/off: ledgy %v bill %v %q", by[AIFeatureLedgy].On, by[AIFeatureBillScan].On, by[AIFeatureBillScan].OffWhy)
	}
	// A tool the firm uses outside Ledgerline.
	tool := AISystem{Name: "Surveillance alert ranker", Purpose: "Ranks exchange alerts", Vendor: "Example Analytics", OrderExecution: "N", Discretionary: "N", Advice: "N", CyberSecurity: "N", SaaS: true, HostedAt: "Mumbai", Active: true, InUseFrom: "2026-01-01"}
	if _, err := o.SaveAITool(ctx, T, "priya.nair", tool); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker added a tool: %v", err)
	}
	bad := tool
	bad.Advice = "maybe"
	if _, err := o.SaveAITool(ctx, T, "vikram.shah", bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad answer accepted: %v", err)
	}
	saved, err := o.SaveAITool(ctx, T, "vikram.shah", tool)
	if err != nil || saved.ID == "" || saved.InSystemAudit != "NA" {
		t.Fatalf("%+v %v", saved, err)
	}
	inv, _ = o.AIInventory(ctx, T, "vikram.shah")
	if len(inv) != len(aiFeatures)+1 || inv[len(inv)-1].Source != "firm" || !inv[len(inv)-1].On {
		t.Fatalf("firm tool: %+v", inv[len(inv)-1])
	}
}

// TestAICallsGoThroughTheGate: the only calls to a model are in ailog.go,
// and each passes the gate for a feature that has an inventory entry.
func TestAICallsGoThroughTheGate(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	call := regexp.MustCompile(`\.(Write\(cctx|ReadDocument\()|LedgyAI\.Write\(`)
	gate := regexp.MustCompile(`aiGate\(ctx, tenant, ([A-Za-z]+)\)`)
	feats := map[string]bool{}
	for _, f := range aiFeatures {
		feats[f] = true
	}
	consts := map[string]string{"AIFeatureLedgy": AIFeatureLedgy, "AIFeatureBillScan": AIFeatureBillScan}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, _ := os.ReadFile(f)
		if f != "ailog.go" && call.Match(b) {
			t.Errorf("%s calls an AI model directly: go through aiAsk or aiReadDocument (ailog.go)", f)
		}
		for _, m := range gate.FindAllStringSubmatch(string(b), -1) {
			if m[1] == "feature" {
				continue
			}
			if !feats[consts[m[1]]] {
				t.Errorf("%s: gate for %s, which is not in aiFeatures", f, m[1])
			}
		}
	}
	b, _ := os.ReadFile("ailog.go")
	if n := len(call.FindAll(b, -1)); n != 2 {
		t.Errorf("ailog.go: %d model calls, want 2 (Write and ReadDocument)", n)
	}
	if n := len(regexp.MustCompile(`aiGate\(`).FindAll(b, -1)); n != 2 {
		t.Errorf("ailog.go: %d gate checks, want one per call", n)
	}
}

func TestAIMasking(t *testing.T) {
	l := MaskLists{
		ClientNames: []string{"Rohan Mehta", "Asha Devi Kulkarni", "Sunrise Investments"},
		ClientCodes: []string{"ZZ1001", "QX42"},
		StaffNames:  []string{"Kiran Bose", "Administrator"},
		StaffIDs:    []string{"kiran.bose", "admin"},
	}
	in := "Kiran here (kiran.bose). Client Rohan Mehta, UCC zz1001, PAN abcde1234f, Aadhaar 2345 6789 0123, " +
		"mobile +91 98765 43210, alt 9123456789, email rohan.m@example.in, bank a/c 001234567890123 and asha kulkarni. " +
		"Again: Rohan Mehta, ABCDE1234F, ZZ1001. Ask the administrator. QX42. Rohan Mehta's ledger."
	m := NewMasker(l, nil)
	out := m.Mask(in)
	for _, raw := range []string{"Rohan", "Mehta", "zz1001", "ZZ1001", "abcde1234f", "ABCDE1234F", "2345 6789 0123", "98765 43210", "9123456789", "rohan.m@example.in", "001234567890123", "asha kulkarni", "Kiran", "kiran.bose", "QX42"} {
		if strings.Contains(out, raw) {
			t.Errorf("%q not masked: %s", raw, out)
		}
	}
	for _, tok := range []string{"[PAN-1]", "[AADHAAR-1]", "[PHONE-1]", "[PHONE-2]", "[EMAIL-1]", "[ACCOUNT-1]", "[CLIENT-CODE-1]", "[CLIENT-CODE-2]", "[CLIENT-1]", "[CLIENT-2]", "[STAFF-1]", "[STAFF-2]"} {
		if !strings.Contains(out, tok) {
			t.Errorf("no %s in %s", tok, out)
		}
	}
	// Stable: the same value, the same token.
	if !strings.Contains(out, "[CLIENT-1]'s ledger") {
		t.Errorf("possessive not masked: %s", out)
	}
	if strings.Count(out, "[CLIENT-1]") != 3 || strings.Count(out, "[PAN-1]") != 2 || strings.Count(out, "[CLIENT-CODE-1]") != 2 || strings.Contains(out, "[PAN-2]") {
		t.Errorf("tokens not stable: %s", out)
	}
	// Ordinary words stay: one-word names are not masked.
	if !strings.Contains(out, "administrator") {
		t.Errorf("an ordinary word was masked: %s", out)
	}
	// The answer gets the values back.
	ans := "[CLIENT-1] ([CLIENT-CODE-1], [PAN-1]) can be helped by [STAFF-1]; unknown [PAN-9] stays."
	if got := m.Restore(ans); got != "Rohan Mehta (ZZ1001, ABCDE1234F) can be helped by Kiran; unknown [PAN-9] stays." {
		t.Errorf("restore: %s", got)
	}
	if !strings.Contains(m.Summary(), "pan ×2") {
		t.Errorf("summary %s", m.Summary())
	}
	// Names off: names pass, PAN, Aadhaar and accounts never do.
	m2 := NewMasker(l, []string{AIMaskClientName, AIMaskPAN, AIMaskAadhaar, AIMaskAccount})
	out2 := m2.Mask(in)
	if !strings.Contains(out2, "Rohan Mehta") || strings.Contains(out2, "ABCDE1234F") || strings.Contains(out2, "2345 6789 0123") || strings.Contains(out2, "001234567890123") {
		t.Errorf("policy: %s", out2)
	}
	// Short numbers and amounts are left alone.
	if s := NewMasker(l, nil).Mask("pay ₹15,000 on 2026-10-15 for 120 shares"); s != "pay ₹15,000 on 2026-10-15 for 120 shares" {
		t.Errorf("ordinary numbers masked: %s", s)
	}
}

func TestAIMaskingPolicy(t *testing.T) {
	o, ctx, w := aiFirm(t)
	if _, err := o.SetAIMasking(ctx, T, "vikram.shah", AIMaskClientName, false); !errors.Is(err, ErrForbidden) {
		t.Fatalf("compliance changed masking: %v", err)
	}
	for _, c := range []string{AIMaskPAN, AIMaskAadhaar, AIMaskAccount} {
		if _, err := o.SetAIMasking(ctx, T, "admin", c, false); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s turned off: %v", c, err)
		}
	}
	if _, err := o.SetAIMasking(ctx, T, "admin", AIMaskEmail, false); err != nil {
		t.Fatal(err)
	}
	v, _ := o.AIControlsScreen(ctx, T, "vikram.shah")
	for _, c := range v.Masking {
		if c.On != (c.ID != AIMaskEmail) || c.Locked != maskLocked[c.ID] {
			t.Fatalf("masking view: %+v", v.Masking)
		}
	}
	// A real client's details in a question are masked before the call
	// and come back in the answer.
	cs, _ := o.Clients(ctx, T)
	c := cs[0]
	q := fmt.Sprintf("how do I add an AP for %s %s PAN %s", c.Name, c.UCC, c.PAN)
	a, _ := o.Ledgy(ctx, T, "priya.nair", q, "", D)
	sent := w.sent[len(w.sent)-1]
	if strings.Contains(sent, c.PAN) || strings.Contains(sent, c.Name) || strings.Contains(sent, c.UCC) {
		t.Fatalf("sent unmasked: %s", sent)
	}
	if !strings.Contains(a.AI, c.PAN) || !strings.Contains(a.AI, c.Name) {
		t.Fatalf("answer not restored: %s", a.AI)
	}
}

func TestAIAnswerLog(t *testing.T) {
	o, ctx, w := aiFirm(t)
	cs, _ := o.Clients(ctx, T)
	c := cs[0]
	o.Ledgy(ctx, T, "priya.nair", "how do I add an AP for "+c.Name+" PAN "+c.PAN+" email someone@example.in", "", D)
	// Even with names let through to the model, the log masks them.
	o.SetAIMasking(ctx, T, "admin", AIMaskClientName, false)
	o.Ledgy(ctx, T, "priya.nair", "how do I add an AP for "+c.Name, "", D)
	w.fail = true
	o.Ledgy(ctx, T, "farhan.khan", aiQ, "", D)
	w.fail = false

	if _, err := o.AILog(ctx, T, "priya.nair", "", ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker read the log: %v", err)
	}
	v, err := o.AILog(ctx, T, "vikram.shah", "", "")
	if err != nil || v.Calls != 3 || v.Outcomes[AIAnswered] != 2 || v.Outcomes[AIError] != 1 || v.Charged <= 0 {
		t.Fatalf("log %+v %v", v, err)
	}
	for _, e := range v.Entries {
		all := e.Prompt + e.Answer + e.Note + e.Context
		if strings.Contains(all, c.PAN) || strings.Contains(all, c.Name) || strings.Contains(all, "someone@example.in") {
			t.Fatalf("log holds an identifier: %+v", e)
		}
		if e.Actor == "" || e.Provider != "anthropic" || e.Model == "" || e.Feature != AIFeatureLedgy || e.At == "" {
			t.Fatalf("entry %+v", e)
		}
	}
	if e := v.Entries[len(v.Entries)-1]; e.In == 0 || e.Out == 0 || !strings.Contains(e.Prompt, "[PAN-1]") || !strings.Contains(e.Context, "add-ap") || e.Masked == "" {
		t.Fatalf("first entry %+v", e)
	}
	b, err := o.AILogCSV(ctx, T, "vikram.shah", "", "")
	if err != nil || strings.Count(string(b), "\n") != 4 || strings.Contains(string(b), c.PAN) {
		t.Fatalf("csv %s %v", b, err)
	}

	// Retention: older entries lose their prompt and answer.
	if _, err := o.SetAILogRetention(ctx, T, "vikram.shah", 30); !errors.Is(err, ErrInvalid) {
		t.Fatalf("30 days accepted: %v", err)
	}
	if _, err := o.SetAILogRetention(ctx, T, "vikram.shah", 400); err != nil {
		t.Fatal(err)
	}
	o.Now = func() time.Time { return time.Date(2027, 12, 1, 9, 0, 0, 0, ist) }
	o.aiControlsTick(ctx, T, o.Now())
	v, _ = o.AILog(ctx, T, "vikram.shah", "2026-01-01", "2027-12-31")
	for _, e := range v.Entries {
		if !e.Cleared || e.Prompt != "" || e.Answer != "" || e.In == 0 && e.Outcome == AIAnswered {
			t.Fatalf("not pruned: %+v", e)
		}
	}
}

func TestAIOnDuring(t *testing.T) {
	o, ctx, _ := aiFirm(t) // AI switched on 2026-10-20
	if on, _ := o.aiOnDuring(ctx, T, AIFeatureLedgy, "2026-04-01", "2026-09-30"); on {
		t.Fatal("on before it was switched on")
	}
	if on, _ := o.aiOnDuring(ctx, T, AIFeatureLedgy, "2026-10-01", "2027-03-31"); !on {
		t.Fatal("not on in the period it was switched on")
	}
	o.Now = func() time.Time { return time.Date(2026, 11, 2, 9, 0, 0, 0, ist) }
	o.SetAIOff(ctx, T, "vikram.shah", true, "policy")
	if on, _ := o.aiOnDuring(ctx, T, AIFeatureLedgy, "2027-04-01", "2027-09-30"); on {
		t.Fatal("on after it was switched off")
	}
	if on, _ := o.aiOnDuring(ctx, T, AIFeatureBillScan, "2026-10-01", "2027-03-31"); on {
		t.Fatal("bill reading was never on")
	}
}
