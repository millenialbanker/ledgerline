package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/help"
)

// ---- AI controls: the kill switch ----
//
// Every call Ledgerline makes to an AI model goes through one gate
// (aiGate). It refuses the call when:
//
//   - the server's AI is off (LEDGERLINE_AI_OFF=1, for every firm);
//   - no AI model is connected, or the server is in India-only mode
//     where the feature would send data abroad;
//   - the firm switched AI off (an Admin or the compliance officer, with
//     the reason, in the audit trail);
//   - the firm switched the connected provider off;
//   - the feature itself is off in Settings (Ledgy phrasing, bill reading).
//
// The gate reads the switches on every call, so a change takes effect on
// the next question. Refused features fall back to what they do without AI
// (Ledgy's article answer, typing the bill's details).

const cAIControl = "aicontrol"

// AI features: each code path that can call a model.
const (
	AIFeatureLedgy    = "ledgy"
	AIFeatureBillScan = "billscan"
)

// aiFeatures lists every AI feature, in the order the inventory shows them.
var aiFeatures = []string{AIFeatureLedgy, AIFeatureBillScan}

// AIProviders are the providers Ledgerline can connect to.
var AIProviders = []string{"anthropic", "openai", "gemini"}

// aiProviderInfo describes each provider for people and for disclosures.
var aiProviderInfo = map[string]struct{ Name, Company, Where string }{
	"anthropic": {"Anthropic Claude", "Anthropic PBC", "USA"},
	"openai":    {"OpenAI", "OpenAI OpCo LLC", "USA"},
	"gemini":    {"Google Gemini API", "Google LLC", "Outside India (Google's global regions)"},
}

// AIControls are the firm's AI switches and settings.
type AIControls struct {
	Off       bool   `json:"off"`
	OffReason string `json:"off_reason,omitempty"`
	OffBy     string `json:"off_by,omitempty"`
	OffAt     string `json:"off_at,omitempty"`
	// ProvidersOff lists providers the firm refuses, with why.
	ProvidersOff map[string]AISwitch `json:"providers_off"`
	// Unmasked are the masking categories an Admin turned off (aimask.go);
	// PAN, Aadhaar and bank accounts can never be.
	Unmasked []string `json:"unmasked"`
	// LogRetentionDays: how long the AI answer log keeps prompts and answers.
	LogRetentionDays int `json:"log_retention_days"`
	// Changes are the last switch changes, newest last.
	Changes []AIControlChange `json:"changes"`
}

// AISwitch is one provider switched off.
type AISwitch struct {
	Reason string `json:"reason"`
	By     string `json:"by"`
	At     string `json:"at"`
}

// AIControlChange is one change to the switches.
type AIControlChange struct {
	At     string `json:"at"`
	By     string `json:"by"`
	What   string `json:"what"`
	Reason string `json:"reason,omitempty"`
}

func (o *Ops) aiControls(ctx context.Context, tenant string) AIControls {
	c, _, _ := docs.GetOr(ctx, o.Docs, tenant, cAIControl, "main", AIControls{})
	if c.ProvidersOff == nil {
		c.ProvidersOff = map[string]AISwitch{}
	}
	if c.Unmasked == nil {
		c.Unmasked = []string{}
	}
	if c.Changes == nil {
		c.Changes = []AIControlChange{}
	}
	if c.LogRetentionDays == 0 {
		c.LogRetentionDays = DefaultAILogRetentionDays
	}
	return c
}

func (o *Ops) saveAIControls(ctx context.Context, tenant, actor string, fn func(*AIControls) error) (AIControls, error) {
	c := o.aiControls(ctx, tenant)
	if err := fn(&c); err != nil {
		return c, err
	}
	if len(c.Changes) > 200 {
		c.Changes = c.Changes[len(c.Changes)-200:]
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cAIControl, "main", c, actor); err != nil {
		return c, err
	}
	o.aiMarkState(ctx, tenant)
	return c, nil
}

// aiKilled says why AI may not be used by this firm at all right now, or "".
func (o *Ops) aiKilled(ctx context.Context, tenant string) string {
	if o.AIOff {
		return "AI is switched off on this server for every firm (LEDGERLINE_AI_OFF)."
	}
	c := o.aiControls(ctx, tenant)
	if c.Off {
		return "Your firm switched AI off" + reasonSuffix(c.OffReason) + "."
	}
	if o.LedgyAI != nil {
		if s, ok := c.ProvidersOff[o.LedgyAI.Provider()]; ok {
			return "Your firm switched " + aiProviderInfo[o.LedgyAI.Provider()].Name + " off" + reasonSuffix(s.Reason) + "."
		}
	}
	return ""
}

func reasonSuffix(r string) string {
	if r = strings.TrimSpace(r); r != "" {
		return ": " + strings.TrimRight(r, ".")
	}
	return ""
}

// aiFeatureState says whether a feature may call the AI model now, and if
// not, why. Every AI call path asks this first (aiGate).
func (o *Ops) aiFeatureState(ctx context.Context, tenant, feature string) (bool, string) {
	if o.LedgyAI == nil || o.Meter == nil {
		return false, "No AI model is connected on this server."
	}
	if why := o.aiKilled(ctx, tenant); why != "" {
		return false, why
	}
	set, _ := o.Settings(ctx, tenant)
	switch feature {
	case AIFeatureLedgy:
		if o.Residency.IndiaOnly {
			return false, "AI phrasing is not allowed in India-only mode."
		}
		if !set.LedgyAI {
			return false, "An admin can switch on “Let Ledgy write answers with AI” in Settings."
		}
	case AIFeatureBillScan:
		if o.Residency.IndiaOnly {
			return false, "Reading bills with AI is not allowed in India-only mode."
		}
		if _, ok := o.LedgyAI.(help.DocumentReader); !ok {
			return false, "The connected AI model cannot read documents."
		}
		if !set.BillAI {
			return false, "An admin can switch on “Read bills with AI” in Settings (the bill is then sent to the AI provider)."
		}
	default:
		return false, "Unknown AI feature " + feature + "."
	}
	return true, ""
}

// aiGate is the one check before any AI call: the writer to use, or why not.
func (o *Ops) aiGate(ctx context.Context, tenant, feature string) (help.Writer, string) {
	if ok, why := o.aiFeatureState(ctx, tenant, feature); !ok {
		return nil, why
	}
	return o.LedgyAI, ""
}

// SetAIOff switches all of the firm's AI use off (or back on), with the reason.
func (o *Ops) SetAIOff(ctx context.Context, tenant, actor string, off bool, reason string) (AIControls, error) {
	if err := o.require(ctx, tenant, actor, "Switching AI off or on", RoleCompliance); err != nil {
		return AIControls{}, err
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return AIControls{}, invalid("give the reason for switching AI %s", map[bool]string{true: "off", false: "on"}[off])
	}
	now := o.now().Format(time.RFC3339)
	c, err := o.saveAIControls(ctx, tenant, actor, func(c *AIControls) error {
		c.Off = off
		if off {
			c.OffReason, c.OffBy, c.OffAt = reason, actor, now
		} else {
			c.OffReason, c.OffBy, c.OffAt = "", "", ""
		}
		c.Changes = append(c.Changes, AIControlChange{At: now, By: actor, What: map[bool]string{true: "AI switched off", false: "AI switched on"}[off], Reason: reason})
		return nil
	})
	if err != nil {
		return c, err
	}
	o.log(ctx, tenant, actor, map[bool]string{true: "ai.switched_off", false: "ai.switched_on"}[off], "firm", map[string]any{"reason": reason})
	return c, nil
}

// SetAIProviderOff refuses (or allows again) one AI provider, with the reason.
func (o *Ops) SetAIProviderOff(ctx context.Context, tenant, actor, provider string, off bool, reason string) (AIControls, error) {
	if err := o.require(ctx, tenant, actor, "Switching AI off or on", RoleCompliance); err != nil {
		return AIControls{}, err
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	if _, ok := aiProviderInfo[provider]; !ok {
		return AIControls{}, invalid("provider must be one of %s", strings.Join(AIProviders, ", "))
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return AIControls{}, invalid("give the reason")
	}
	now := o.now().Format(time.RFC3339)
	c, err := o.saveAIControls(ctx, tenant, actor, func(c *AIControls) error {
		if off {
			c.ProvidersOff[provider] = AISwitch{Reason: reason, By: actor, At: now}
		} else {
			delete(c.ProvidersOff, provider)
		}
		c.Changes = append(c.Changes, AIControlChange{At: now, By: actor, What: aiProviderInfo[provider].Name + map[bool]string{true: " switched off", false: " switched on"}[off], Reason: reason})
		return nil
	})
	if err != nil {
		return c, err
	}
	o.log(ctx, tenant, actor, map[bool]string{true: "ai.provider_off", false: "ai.provider_on"}[off], provider, map[string]any{"reason": reason})
	return c, nil
}

// AIControlsView is the AI controls screen.
type AIControlsView struct {
	Controls     AIControls         `json:"controls"`
	ServerOff    bool               `json:"server_off"`
	Connected    bool               `json:"connected"`
	Provider     string             `json:"provider,omitempty"`
	ProviderName string             `json:"provider_name,omitempty"`
	Model        string             `json:"model,omitempty"`
	IndiaOnly    bool               `json:"india_only"`
	Killed       string             `json:"killed,omitempty"`
	Providers    []AIProviderSwitch `json:"providers"`
	Masking      []AIMaskCategory   `json:"masking"`
	Systems      []AISystem         `json:"systems"`
}

// AIProviderSwitch is one provider's state for the firm.
type AIProviderSwitch struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Where     string    `json:"where"`
	Connected bool      `json:"connected"`
	Off       *AISwitch `json:"off,omitempty"`
}

// AIControlsScreen shows the switches, masking, and the AI inventory.
func (o *Ops) AIControlsScreen(ctx context.Context, tenant, actor string) (AIControlsView, error) {
	if err := o.require(ctx, tenant, actor, "Viewing AI controls", RoleCompliance); err != nil {
		return AIControlsView{}, err
	}
	c := o.aiControls(ctx, tenant)
	v := AIControlsView{Controls: c, ServerOff: o.AIOff, Connected: o.LedgyAI != nil && o.Meter != nil, IndiaOnly: o.Residency.IndiaOnly, Killed: o.aiKilled(ctx, tenant), Masking: aiMaskCategories(c)}
	if o.LedgyAI != nil {
		v.Provider, v.Model, v.ProviderName = o.LedgyAI.Provider(), o.LedgyAI.Model(), aiProviderInfo[o.LedgyAI.Provider()].Name
	}
	for _, p := range AIProviders {
		s := AIProviderSwitch{ID: p, Name: aiProviderInfo[p].Name, Where: aiProviderInfo[p].Where, Connected: v.Provider == p}
		if x, ok := c.ProvidersOff[p]; ok {
			x := x
			s.Off = &x
		}
		v.Providers = append(v.Providers, s)
	}
	var err error
	v.Systems, err = o.aiInventory(ctx, tenant)
	return v, err
}

// ---- on/off history, for the disclosures ----

// AIStateMark is which AI features could be used from a moment on.
type AIStateMark struct {
	At string          `json:"at"`
	On map[string]bool `json:"on"`
}

type aiTimeline struct {
	Marks  []AIStateMark `json:"marks"`
	Pruned string        `json:"pruned,omitempty"` // the day the answer log was last pruned
}

func (o *Ops) aiTimelineDoc(ctx context.Context, tenant string) aiTimeline {
	t, _, _ := docs.GetOr(ctx, o.Docs, tenant, cAIControl, "timeline", aiTimeline{})
	return t
}

func (o *Ops) aiStatesNow(ctx context.Context, tenant string) map[string]bool {
	m := map[string]bool{}
	for _, f := range aiFeatures {
		m[f], _ = o.aiFeatureState(ctx, tenant, f)
	}
	return m
}

// aiMarkState records the features' state when it changed since the last mark.
func (o *Ops) aiMarkState(ctx context.Context, tenant string) {
	now := o.aiStatesNow(ctx, tenant)
	t := o.aiTimelineDoc(ctx, tenant)
	if n := len(t.Marks); n > 0 && sameStates(t.Marks[n-1].On, now) {
		return
	}
	t.Marks = append(t.Marks, AIStateMark{At: o.now().Format(time.RFC3339), On: now})
	_ = docs.Upsert(ctx, o.Docs, tenant, cAIControl, "timeline", t, SystemActor)
}

func sameStates(a, b map[string]bool) bool {
	for _, f := range aiFeatures {
		if a[f] != b[f] {
			return false
		}
	}
	return true
}

// aiOnDuring says whether a feature could be used at any time from `from`
// to `to` (dates, inclusive), from the recorded history and the answer log.
func (o *Ops) aiOnDuring(ctx context.Context, tenant, feature, from, to string) (bool, string) {
	if n := o.aiLogCount(ctx, tenant, feature, from, to); n > 0 {
		return true, fmt.Sprintf("%d AI call(s) in the period", n)
	}
	t := o.aiTimelineDoc(ctx, tenant)
	if len(t.Marks) == 0 {
		on, _ := o.aiFeatureState(ctx, tenant, feature)
		return on, "no change recorded: taken as it is today"
	}
	sort.SliceStable(t.Marks, func(i, j int) bool { return t.Marks[i].At < t.Marks[j].At })
	var atStart *AIStateMark
	for i := range t.Marks {
		m := &t.Marks[i]
		d := m.At[:10]
		if d < from {
			atStart = m
			continue
		}
		if d <= to && m.On[feature] {
			return true, "switched on during the period"
		}
	}
	switch {
	case atStart != nil && atStart.On[feature]:
		return true, "on at the start of the period"
	case atStart == nil && t.Marks[0].At[:10] > from:
		return false, "off for the whole period as recorded from " + t.Marks[0].At[:10]
	}
	return false, "off for the whole period"
}

// aiControlsTick keeps the on/off history up to date (the server's own
// switches change only at a restart) and prunes the answer log once a day.
// It runs in every edition, from the scheduler's Tick.
func (o *Ops) aiControlsTick(ctx context.Context, tenant string, now time.Time) {
	o.aiMarkState(ctx, tenant)
	today := now.In(ist).Format("2006-01-02")
	t := o.aiTimelineDoc(ctx, tenant)
	if t.Pruned == today {
		return
	}
	o.pruneAILog(ctx, tenant, today)
	t = o.aiTimelineDoc(ctx, tenant)
	t.Pruned = today
	_ = docs.Upsert(ctx, o.Docs, tenant, cAIControl, "timeline", t, SystemActor)
}
