package ops

import (
	"context"
	"errors"
	"math/rand"
	"strings"
	"testing"

	"ledgerline/internal/help"
	"ledgerline/internal/money"
)

func meter(t *testing.T, markup, min string) *Meter {
	t.Helper()
	prices, err := LoadAIPrices("")
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewMeter(prices, "88.50", markup, min, "")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestMeterChargesAtLeastTwiceCost(t *testing.T) {
	m := meter(t, "2", "")
	// A typical Ledgy answer on Claude Haiku 5.5: 2,500 tokens in, 400 out.
	// Cost $0.00025 + $0.0002 = $0.00045 = ₹0.0398 at ₹88.50; ×2 = ₹0.0797,
	// rounded up to 8 paise.
	usd, inr, ch, err := m.Charge("anthropic", "claude-haiku-5-5", help.Usage{InputTokens: 2500, OutputTokens: 400})
	if err != nil || usd.Fixed(8) != "0.00045000" || inr.Fixed(6) != "0.039825" || ch != money.Amount(8) {
		t.Fatalf("haiku: %s %s %d %v", usd.Fixed(8), inr.Fixed(6), ch, err)
	}
	// For every priced model and many sizes, the charge is never below
	// twice the cost, and never more than a paisa above it.
	r := rand.New(rand.NewSource(1))
	two := money.MustDec("2")
	for _, p := range m.Prices {
		for i := 0; i < 300; i++ {
			u := help.Usage{InputTokens: r.Intn(200000), OutputTokens: r.Intn(5000)}
			_, inr, ch, err := m.Charge(p.Provider, p.Model, u)
			if err != nil {
				t.Fatal(err)
			}
			floor := inr.Mul(two)
			if ch.Dec().Cmp(floor) < 0 || ch.Dec().Sub(floor).Cmp(money.MustDec("0.01")) >= 0 {
				t.Fatalf("%s %+v: charge %s for cost %s", p.Model, u, ch, inr.Fixed(6))
			}
		}
	}
	// A higher markup and a minimum charge.
	m3 := meter(t, "3", "0.25")
	if _, _, ch, _ := m3.Charge("anthropic", "claude-haiku-5-5", help.Usage{InputTokens: 2500, OutputTokens: 400}); ch != money.Amount(25) {
		t.Fatalf("minimum charge: %d", ch)
	}
	if _, _, ch, _ := m3.Charge("anthropic", "claude-opus-5-5", help.Usage{InputTokens: 2500, OutputTokens: 400}); ch != money.Amount(478) { // $0.018 × 88.5 × 3 = ₹4.779, up to ₹4.78
		t.Fatalf("opus ×3: %d", ch)
	}
}

func TestMeterRefusesBadConfig(t *testing.T) {
	prices, _ := LoadAIPrices("")
	for _, c := range [][3]string{{"88", "1.99", ""}, {"88", "1", ""}, {"", "2", ""}, {"0", "2", ""}, {"88", "two", ""}, {"88", "2", "-1"}} {
		if _, err := NewMeter(prices, c[0], c[1], c[2], ""); err == nil {
			t.Errorf("accepted %v", c)
		}
	}
	m := meter(t, "2.5", "")
	if _, _, _, err := m.Charge("openai", "some-unpriced-model", help.Usage{InputTokens: 1}); err == nil {
		t.Fatal("unpriced model accepted")
	}
}

func TestAICreditAndUsage(t *testing.T) {
	o, ctx := setup(t)
	o.LedgyAI = &fakeWriter{}
	o.Meter = meter(t, "2", "")
	set, _ := o.Settings(ctx, T)
	set.LedgyAI = true
	o.SaveSettings(ctx, T, "admin", set)

	if _, err := o.AddAICredit(ctx, T, "provider", 0, "x"); err == nil {
		t.Fatal("zero credit accepted")
	}
	if _, err := o.AddAICredit(ctx, T, "provider", money.Amount(50), ""); err == nil {
		t.Fatal("credit without a reference accepted")
	}
	// 50 paise: enough for a few answers at about 8 paise each.
	w, err := o.AddAICredit(ctx, T, "provider", money.Amount(50), "INV-42")
	if err != nil || w.Balance != 50 {
		t.Fatalf("%+v %v", w, err)
	}
	answered := 0
	for i := 0; i < 12; i++ {
		a, err := o.Ledgy(ctx, T, "priya.nair", "how do I add an AP", "", D)
		if err != nil {
			t.Fatal(err)
		}
		if a.AI != "" {
			answered++
		} else if !strings.Contains(a.AINote, "credit has run out") {
			t.Fatalf("no AI and no note: %+v", a)
		}
	}
	w2, _, _ := docsGetWallet(o, T)
	if answered == 0 || answered >= 12 || w2.Balance < 0 {
		t.Fatalf("answered %d, balance %d", answered, w2.Balance)
	}
	if w2.Charged != money.Amount(50)-w2.Balance || w2.Answers != answered {
		t.Fatalf("wallet: %+v", w2)
	}

	// The firm sees its charges, not the provider's cost or markup.
	if _, err := o.AIUsage(ctx, T, "priya.nair", ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker saw usage: %v", err)
	}
	v, err := o.AIUsage(ctx, T, "admin", "")
	if err != nil || v.Answers != answered || v.Charged != w2.Charged || len(v.Uses) != answered || len(v.TopUps) != 1 || v.GST == 0 {
		t.Fatalf("usage: %+v %v", v, err)
	}
	for _, u := range v.Uses {
		if u.CostINR != "" || u.CostUSD != "" || u.Markup != "" || u.Rate != "" {
			t.Fatalf("cost shown to the firm: %+v", u)
		}
	}
	// The provider sees cost, charge and margin; the margin is at least the cost.
	rows, err := o.ProviderAIUsage(ctx, []string{T}, "")
	if err != nil || len(rows) != 1 || rows[0].Answers != answered {
		t.Fatalf("provider: %+v %v", rows, err)
	}
	cost, _ := money.ParseDec(rows[0].CostINR)
	margin, _ := money.ParseDec(rows[0].Margin)
	if margin.Cmp(cost) < 0 || rows[0].ToppedUp != 50 {
		t.Fatalf("margin %s below cost %s", rows[0].Margin, rows[0].CostINR)
	}
}

func docsGetWallet(o *Ops, tenant string) (AIWallet, int64, error) {
	v, err := o.aiUsage(context.Background(), tenant, "")
	return v.Wallet, 0, err
}
