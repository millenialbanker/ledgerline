package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"ledgerline"
	"ledgerline/internal/docs"
	"ledgerline/internal/help"
	"ledgerline/internal/money"
)

// ---- AI metering: prepaid credits, charged at a markup on cost ----
//
// Each AI answer is priced from the tokens the provider reports, at the
// provider's list price (config/ai-prices.json), converted to rupees at
// the configured rate and multiplied by the markup (never below 2×), then
// rounded UP to the paisa so the charge is never below the markup. It is
// taken from the firm's prepaid AI credit. A firm with too little credit
// for the largest possible answer gets Ledgy's ordinary answer instead.
// Credits are added by the provider of the software, not by the firm.

const (
	cAIWallet = "aiwallet"
	cAIUse    = "aiuse"
	cAITopUp  = "aitopup"
	// MinMarkup is the lowest markup the meter accepts.
	MinMarkup = "2"
)

// AIPrice is a provider's list price per million tokens, in US dollars.
type AIPrice struct {
	Provider   string `json:"provider"`
	Model      string `json:"model"`
	InputPerM  string `json:"input_per_m"`
	OutputPerM string `json:"output_per_m"`
	Checked    string `json:"checked"`
	Source     string `json:"source"`
}

// Meter is the pricing configuration, set by the provider of the software.
type Meter struct {
	Prices    []AIPrice
	USDINR    money.Dec // rupees per US dollar
	Markup    money.Dec // at least 2
	MinCharge money.Dec // rupees per answer, before GST; 0 for none
	RateNote  string
}

// LoadAIPrices reads the built-in price table, or the file at path.
func LoadAIPrices(path string) ([]AIPrice, error) {
	var b []byte
	var err error
	if path == "" {
		b, err = fs.ReadFile(ledgerline.FS, "config/ai-prices.json")
	} else {
		b, err = os.ReadFile(path)
	}
	if err != nil {
		return nil, err
	}
	var f struct {
		Prices []AIPrice `json:"prices"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("AI price table: %w", err)
	}
	for _, p := range f.Prices {
		for _, v := range []string{p.InputPerM, p.OutputPerM} {
			d, err := money.ParseDec(v)
			if err != nil || d.Sign() < 0 {
				return nil, fmt.Errorf("AI price table: %s %s has a bad price %q", p.Provider, p.Model, v)
			}
		}
	}
	return f.Prices, nil
}

// NewMeter checks the configuration: a rate above zero and a markup of at
// least 2.
func NewMeter(prices []AIPrice, usdinr, markup, minCharge, rateNote string) (*Meter, error) {
	r, err := money.ParseDec(usdinr)
	if err != nil || r.Sign() <= 0 {
		return nil, errors.New("AI metering needs the rupee rate per US dollar (LEDGERLINE_AI_USDINR), for example 88.50")
	}
	if markup == "" {
		markup = MinMarkup
	}
	m, err := money.ParseDec(markup)
	if err != nil {
		return nil, fmt.Errorf("AI markup %q is not a number", markup)
	}
	if m.Cmp(money.MustDec(MinMarkup)) < 0 {
		return nil, fmt.Errorf("AI markup %s is below the minimum of %s×", markup, MinMarkup)
	}
	if minCharge == "" {
		minCharge = "0"
	}
	mc, err := money.ParseDec(minCharge)
	if err != nil || mc.Sign() < 0 {
		return nil, fmt.Errorf("AI minimum charge %q is not an amount", minCharge)
	}
	return &Meter{Prices: prices, USDINR: r, Markup: m, MinCharge: mc, RateNote: rateNote}, nil
}

func (m *Meter) price(provider, model string) (AIPrice, bool) {
	for _, p := range m.Prices {
		if p.Provider == provider && p.Model == model {
			return p, true
		}
	}
	return AIPrice{}, false
}

// Charge prices a call: the provider's cost in dollars and rupees, and the
// charge in rupees (cost × markup, at least the minimum, rounded up to the
// paisa).
func (m *Meter) Charge(provider, model string, u help.Usage) (costUSD, costINR money.Dec, charge money.Amount, err error) {
	p, ok := m.price(provider, model)
	if !ok {
		return money.Dec{}, money.Dec{}, 0, fmt.Errorf("no price for %s %s in the AI price table; Ledgy will not call an unpriced model", provider, model)
	}
	in, out := money.MustDec(p.InputPerM), money.MustDec(p.OutputPerM)
	million := money.MustDec("1000000")
	costUSD, _ = in.MulInt(int64(u.InputTokens)).Add(out.MulInt(int64(u.OutputTokens))).Quo(million)
	costINR = costUSD.Mul(m.USDINR)
	c := costINR.Mul(m.Markup)
	if c.Cmp(m.MinCharge) < 0 {
		c = m.MinCharge
	}
	return costUSD, costINR, ceilPaise(c), nil
}

// ceilPaise rounds a rupee amount up to the next paisa.
func ceilPaise(d money.Dec) money.Amount {
	n, _ := strconv.ParseInt(d.MulInt(100).CeilRupee().Fixed(0), 10, 64)
	return money.Amount(n)
}

// AIWallet is a firm's prepaid AI credit.
type AIWallet struct {
	Balance    money.Amount `json:"balance"`
	Charged    money.Amount `json:"charged"`     // all-time charges
	CostINR    string       `json:"cost_inr"`    // all-time provider cost, rupees
	Answers    int          `json:"answers"`     // all-time metered answers
	ToppedUp   money.Amount `json:"topped_up"`   // all-time credit added
	LowAlerted string       `json:"low_alerted"` // when the low-balance alert last went
}

// AIUse is one metered answer. The question is not stored here.
type AIUse struct {
	ID       string       `json:"id"`
	At       string       `json:"at"`
	Actor    string       `json:"actor"`
	Provider string       `json:"provider"`
	Model    string       `json:"model"`
	In       int          `json:"input_tokens"`
	Out      int          `json:"output_tokens"`
	CostUSD  string       `json:"cost_usd"`
	Rate     string       `json:"usd_inr"`
	CostINR  string       `json:"cost_inr"`
	Markup   string       `json:"markup"`
	Charge   money.Amount `json:"charge"`
	Balance  money.Amount `json:"balance_after"`
}

// AITopUp is credit added by the provider.
type AITopUp struct {
	ID     string       `json:"id"`
	At     string       `json:"at"`
	Amount money.Amount `json:"amount"`
	Ref    string       `json:"ref"` // invoice or payment reference
	By     string       `json:"by"`
}

// aiReserve is the most an answer could cost: the estimated prompt and the
// maximum answer length, priced like a real call.
func (o *Ops) aiReserve(provider, model string, inTokens, maxOut int) (money.Amount, error) {
	_, _, c, err := o.Meter.Charge(provider, model, help.Usage{InputTokens: inTokens, OutputTokens: maxOut})
	return c, err
}

// aiMeter records a call and takes the charge from the firm's credit.
func (o *Ops) aiMeter(ctx context.Context, tenant, actor string, r help.Reply) (AIUse, error) {
	costUSD, costINR, charge, err := o.Meter.Charge(r.Provider, r.Model, r.Usage)
	if err != nil {
		return AIUse{}, err
	}
	now := o.now()
	u := AIUse{ID: fmt.Sprintf("AIU-%s", now.Format("20060102150405.000000")), At: now.Format(time.RFC3339), Actor: actor, Provider: r.Provider, Model: r.Model,
		In: r.Usage.InputTokens, Out: r.Usage.OutputTokens, CostUSD: costUSD.Fixed(8), Rate: o.Meter.USDINR.String(), CostINR: costINR.Fixed(6), Markup: o.Meter.Markup.String(), Charge: charge}
	w, err := docs.Update(ctx, o.Docs, tenant, cAIWallet, "wallet", SystemActor, func(w *AIWallet) error {
		w.Balance -= charge
		w.Charged += charge
		w.Answers++
		prev, _ := money.ParseDec(w.CostINR)
		w.CostINR = prev.Add(costINR).Fixed(6)
		return nil
	})
	if err != nil {
		return u, err
	}
	u.Balance = w.Balance
	if err := docs.Upsert(ctx, o.Docs, tenant, cAIUse, u.ID, u, SystemActor); err != nil {
		return u, err
	}
	o.log(ctx, tenant, actor, "ai.metered", u.ID, map[string]any{"model": u.Model, "tokens": u.In + u.Out, "charge": charge.String()})
	return u, nil
}

// aiBalance returns the firm's credit.
func (o *Ops) aiBalance(ctx context.Context, tenant string) money.Amount {
	w, _, _ := docs.GetOr(ctx, o.Docs, tenant, cAIWallet, "wallet", AIWallet{})
	return w.Balance
}

// AddAICredit adds prepaid credit to a firm, for the provider of the
// software after the firm has paid.
func (o *Ops) AddAICredit(ctx context.Context, tenant, by string, amount money.Amount, ref string) (AIWallet, error) {
	if amount <= 0 {
		return AIWallet{}, invalid("credit must be above zero")
	}
	if strings.TrimSpace(ref) == "" {
		return AIWallet{}, invalid("give the invoice or payment reference")
	}
	now := o.now()
	t := AITopUp{ID: "AIT-" + now.Format("20060102150405.000000"), At: now.Format(time.RFC3339), Amount: amount, Ref: strings.TrimSpace(ref), By: by}
	if err := docs.Upsert(ctx, o.Docs, tenant, cAITopUp, t.ID, t, by); err != nil {
		return AIWallet{}, err
	}
	w, err := docs.Update(ctx, o.Docs, tenant, cAIWallet, "wallet", by, func(w *AIWallet) error {
		w.Balance += amount
		w.ToppedUp += amount
		w.LowAlerted = ""
		return nil
	})
	if errors.Is(err, docs.ErrNotFound) {
		w = AIWallet{Balance: amount, ToppedUp: amount, CostINR: "0"}
		err = docs.Upsert(ctx, o.Docs, tenant, cAIWallet, "wallet", w, by)
	}
	if err != nil {
		return w, err
	}
	o.log(ctx, tenant, by, "ai.credit.added", t.ID, map[string]any{"amount": amount.String(), "ref": t.Ref})
	return w, nil
}

// AIUsageView is a firm's AI credit and use for a month.
type AIUsageView struct {
	Enabled   bool         `json:"enabled"` // AI configured on this server
	Provider  string       `json:"provider,omitempty"`
	Model     string       `json:"model,omitempty"`
	Month     string       `json:"month"`
	Wallet    AIWallet     `json:"wallet"`
	Answers   int          `json:"answers"`
	Tokens    int          `json:"tokens"`
	Charged   money.Amount `json:"charged"`
	GST       money.Amount `json:"gst"`
	PerAnswer money.Amount `json:"per_answer_max"` // most an answer can cost now
	Uses      []AIUse      `json:"uses"`
	TopUps    []AITopUp    `json:"top_ups"`
}

// AIUsage shows a firm its AI credit and charges for a month (YYYY-MM).
// Admins and compliance only. Provider costs are not shown to the firm.
func (o *Ops) AIUsage(ctx context.Context, tenant, actor, month string) (AIUsageView, error) {
	if err := o.require(ctx, tenant, actor, "Viewing AI usage", RoleCompliance); err != nil {
		return AIUsageView{}, err
	}
	v, err := o.aiUsage(ctx, tenant, month)
	for i := range v.Uses {
		v.Uses[i].CostUSD, v.Uses[i].CostINR, v.Uses[i].Rate, v.Uses[i].Markup = "", "", "", ""
	}
	v.Wallet.CostINR = ""
	return v, err
}

func (o *Ops) aiUsage(ctx context.Context, tenant, month string) (AIUsageView, error) {
	if month == "" {
		month = o.now().Format("2006-01")
	}
	if _, err := time.Parse("2006-01", month); err != nil {
		return AIUsageView{}, invalid("month must be YYYY-MM")
	}
	v := AIUsageView{Month: month, Uses: []AIUse{}, TopUps: []AITopUp{}, Enabled: o.LedgyAI != nil && o.Meter != nil}
	if o.LedgyAI != nil {
		v.Provider, v.Model = o.LedgyAI.Provider(), o.LedgyAI.Model()
		if o.Meter != nil {
			v.PerAnswer, _ = o.aiReserve(v.Provider, v.Model, 4000, o.LedgyAI.MaxOutput())
		}
	}
	v.Wallet, _, _ = docs.GetOr(ctx, o.Docs, tenant, cAIWallet, "wallet", AIWallet{})
	us, err := docs.List[AIUse](ctx, o.Docs, tenant, cAIUse, "AIU-"+strings.ReplaceAll(month, "-", ""))
	if err != nil {
		return v, err
	}
	sort.Slice(us, func(i, j int) bool { return us[i].ID > us[j].ID })
	for _, u := range us {
		v.Answers++
		v.Tokens += u.In + u.Out
		v.Charged += u.Charge
	}
	v.Uses = append(v.Uses, us...)
	set, _ := o.Settings(ctx, tenant)
	gst := set.GSTPct
	if gst == "" {
		gst = "18"
	}
	if g, err := money.ParseDec(gst); err == nil {
		v.GST = v.Charged.Dec().Pct(g).Round(money.UnitPaisa)
	}
	ts, _ := docs.List[AITopUp](ctx, o.Docs, tenant, cAITopUp, "")
	sort.Slice(ts, func(i, j int) bool { return ts[i].ID > ts[j].ID })
	v.TopUps = append(v.TopUps, ts...)
	return v, nil
}

// ProviderAIRow is one firm's month for the provider's invoicing.
type ProviderAIRow struct {
	Tenant   string       `json:"tenant"`
	Answers  int          `json:"answers"`
	Tokens   int          `json:"tokens"`
	CostINR  string       `json:"cost_inr"`
	Charged  money.Amount `json:"charged"`
	Margin   string       `json:"margin_inr"`
	Balance  money.Amount `json:"balance"`
	ToppedUp money.Amount `json:"topped_up_in_month"`
}

// ProviderAIUsage summarises a month for the provider: cost, charges and
// margin per firm.
func (o *Ops) ProviderAIUsage(ctx context.Context, tenants []string, month string) ([]ProviderAIRow, error) {
	out := []ProviderAIRow{}
	for _, t := range tenants {
		v, err := o.aiUsage(ctx, t, month)
		if err != nil {
			return nil, err
		}
		cost := money.Dec{}
		for _, u := range v.Uses {
			c, _ := money.ParseDec(u.CostINR)
			cost = cost.Add(c)
		}
		r := ProviderAIRow{Tenant: t, Answers: v.Answers, Tokens: v.Tokens, CostINR: cost.Fixed(2), Charged: v.Charged, Balance: v.Wallet.Balance}
		r.Margin = v.Charged.Dec().Sub(cost).Fixed(2)
		for _, tp := range v.TopUps {
			if strings.HasPrefix(tp.At, month) {
				r.ToppedUp += tp.Amount
			}
		}
		out = append(out, r)
	}
	return out, nil
}
