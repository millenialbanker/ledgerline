package ops

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/help"
	"ledgerline/internal/money"
)

// ---- AI calls and the AI answer log ----
//
// The two functions here are the only places Ledgerline calls an AI model
// (a test fails on any other call). Each passes the gate, masks what is
// sent, reserves and meters the charge, restores the tokens in the answer
// and writes one line to the AI answer log: when, who, which feature,
// provider and model, tokens, charge, the masked prompt, the masked
// answer and the outcome. The log is masked again with every category on
// before it is stored, so it never holds a raw PAN, number or name, even
// where the firm let names through to the model. The compliance officer
// reads it and exports it; entries older than the retention period have
// their prompt and answer cleared.

const (
	cAILog = "ailog"
	// DefaultAILogRetentionDays keeps the log five years, like the other
	// records a broker keeps.
	DefaultAILogRetentionDays = 1825
	minAILogRetentionDays     = 365
)

// AILogEntry is one call to an AI model.
type AILogEntry struct {
	ID       string       `json:"id"`
	At       string       `json:"at"`
	Actor    string       `json:"actor"`
	Feature  string       `json:"feature"`
	Provider string       `json:"provider"`
	Model    string       `json:"model"`
	In       int          `json:"input_tokens"`
	Out      int          `json:"output_tokens"`
	Charge   money.Amount `json:"charge"`
	Prompt   string       `json:"prompt"`  // masked
	Context  string       `json:"context"` // what went with it (articles, a document)
	Answer   string       `json:"answer"`  // masked
	Outcome  string       `json:"outcome"` // Answered, Provider error, Not metered, Empty answer, Unreadable answer
	Note     string       `json:"note,omitempty"`
	Masked   string       `json:"masked,omitempty"` // what was masked
	Cleared  bool         `json:"cleared,omitempty"`
}

// AI call outcomes.
const (
	AIAnswered   = "Answered"
	AIError      = "Provider error"
	AINotMetered = "Not metered"
	AIEmpty      = "Empty answer"
	AIUnreadable = "Unreadable answer"
)

func (o *Ops) writeAILog(ctx context.Context, tenant string, e AILogEntry, lists MaskLists) string {
	n, err := o.next(ctx, tenant, "ailog", SystemActor)
	if err != nil {
		return ""
	}
	now := o.now()
	e.ID, e.At = fmt.Sprintf("AIL-%s-%07d", now.Format("20060102"), n), now.Format(time.RFC3339)
	e.Prompt, e.Answer, e.Note = o.logMask(lists, e.Prompt), o.logMask(lists, e.Answer), o.logMask(lists, e.Note)
	if len(e.Answer) > 8000 {
		e.Answer = e.Answer[:8000] + "…"
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cAILog, e.ID, e, SystemActor)
	return e.ID
}

// errAIRefused carries the note for a call that was not made.
type errAIRefused struct{ note string }

func (e *errAIRefused) Error() string { return e.note }

// aiAsk phrases a Ledgy answer: the gate, masking, metering and the log.
// It returns the answer with the tokens put back, the provider's name, or
// a note saying why there is no AI answer ("" when the feature is simply
// off).
func (o *Ops) aiAsk(ctx context.Context, tenant, actor, q string, arts []help.Article, perms []help.PermHit) (text, by, note string) {
	w, why := o.aiGate(ctx, tenant, AIFeatureLedgy)
	if w == nil {
		if o.aiKilled(ctx, tenant) != "" && o.LedgyAI != nil {
			if set, _ := o.Settings(ctx, tenant); set.LedgyAI {
				return "", "", why + " This is Ledgy's standard answer."
			}
		}
		return "", "", ""
	}
	lists := o.aiMaskLists(ctx, tenant)
	m := NewMasker(lists, o.aiControls(ctx, tenant).Unmasked)
	mq := m.Mask(q)
	reserve, err := o.aiReserve(w.Provider(), w.Model(), help.EstimateInputTokens(mq, arts, perms), w.MaxOutput())
	switch {
	case err != nil:
		return "", "", "AI answers are unavailable: " + err.Error()
	case o.aiBalance(ctx, tenant) < reserve:
		return "", "", "Your firm's AI credit has run out, so this is Ledgy's standard answer. Ask your Ledgerline provider to add credit."
	}
	ids := make([]string, 0, len(arts))
	for _, a := range arts {
		ids = append(ids, a.ID)
	}
	entry := AILogEntry{Actor: actor, Feature: AIFeatureLedgy, Provider: w.Provider(), Model: w.Model(), Prompt: mq, Context: "Help articles: " + strings.Join(ids, ", "), Masked: m.Summary()}
	cctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	r, err := w.Write(cctx, mq, arts, perms)
	cancel()
	entry.In, entry.Out, entry.Answer = r.Usage.InputTokens, r.Usage.OutputTokens, r.Text
	switch {
	case err != nil:
		entry.Outcome, entry.Note = AIError, err.Error()
		o.writeAILog(ctx, tenant, entry, lists)
		return "", "", ""
	case strings.TrimSpace(r.Text) == "":
		entry.Outcome = AIEmpty
		if u, merr := o.aiMeter(ctx, tenant, actor, r); merr == nil {
			entry.Charge = u.Charge
		}
		o.writeAILog(ctx, tenant, entry, lists)
		return "", "", ""
	}
	u, merr := o.aiMeter(ctx, tenant, actor, r)
	if merr != nil {
		entry.Outcome, entry.Note = AINotMetered, merr.Error()
		o.writeAILog(ctx, tenant, entry, lists)
		return "", "", "AI answer withheld: it could not be metered (" + merr.Error() + ")."
	}
	entry.Outcome, entry.Charge = AIAnswered, u.Charge
	o.writeAILog(ctx, tenant, entry, lists)
	return strings.TrimSpace(m.Restore(r.Text)), aiName(r), ""
}

// aiReadDocument reads a document with the AI model: the gate, metering
// and the log. A document cannot be masked; its reply is masked in the log.
func (o *Ops) aiReadDocument(ctx context.Context, tenant, actor, feature, docName string, data []byte, mime, instruction string, maxOut int, check func(string) error) (help.Reply, error) {
	w, why := o.aiGate(ctx, tenant, feature)
	if w == nil {
		return help.Reply{}, &errAIRefused{why}
	}
	dr, ok := w.(help.DocumentReader)
	if !ok {
		return help.Reply{}, &errAIRefused{"The connected AI model cannot read documents."}
	}
	reserve, err := o.aiReserve(w.Provider(), w.Model(), 2500+len(data)/400, maxOut)
	if err != nil {
		return help.Reply{}, &errAIRefused{"AI reading is unavailable: " + err.Error()}
	}
	if o.aiBalance(ctx, tenant) < reserve {
		return help.Reply{}, &errAIRefused{"The firm's AI credit has run out. Ask your Ledgerline provider to add credit, or type the details from the bill."}
	}
	lists := o.aiMaskLists(ctx, tenant)
	instr := instruction
	if len(instr) > 400 {
		instr = instr[:400] + "…"
	}
	entry := AILogEntry{Actor: actor, Feature: feature, Provider: w.Provider(), Model: w.Model(), Prompt: instr,
		Context: fmt.Sprintf("Document %s (%s, %d bytes, SHA-256 %s); sent as it is (an image cannot be masked)", docName, mime, len(data), sha256Hex(data)[:16])}
	cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	r, err := dr.ReadDocument(cctx, data, mime, instruction)
	cancel()
	entry.In, entry.Out, entry.Answer = r.Usage.InputTokens, r.Usage.OutputTokens, r.Text
	if err != nil {
		entry.Outcome, entry.Note = AIError, err.Error()
		o.writeAILog(ctx, tenant, entry, lists)
		return r, err
	}
	u, merr := o.aiMeter(ctx, tenant, actor, r)
	if merr != nil {
		entry.Outcome, entry.Note = AINotMetered, merr.Error()
		o.writeAILog(ctx, tenant, entry, lists)
		return r, fmt.Errorf("not metered: %w", merr)
	}
	entry.Charge, entry.Outcome = u.Charge, AIAnswered
	if check != nil {
		if cerr := check(r.Text); cerr != nil {
			entry.Outcome, entry.Note = AIUnreadable, cerr.Error()
		}
	}
	o.writeAILog(ctx, tenant, entry, lists)
	return r, nil
}

func aiRefusal(err error) (string, bool) {
	var r *errAIRefused
	if errors.As(err, &r) {
		return r.note, true
	}
	return "", false
}

// ---- reading the log ----

// AILogView is a page of the AI answer log.
type AILogView struct {
	From      string         `json:"from"`
	To        string         `json:"to"`
	Entries   []AILogEntry   `json:"entries"`
	Calls     int            `json:"calls"`
	Tokens    int            `json:"tokens"`
	Charged   money.Amount   `json:"charged"`
	Outcomes  map[string]int `json:"outcomes"`
	Retention int            `json:"retention_days"`
}

func (o *Ops) aiLogBetween(ctx context.Context, tenant, from, to string) ([]AILogEntry, error) {
	es, err := docs.List[AILogEntry](ctx, o.Docs, tenant, cAILog, "AIL-")
	if err != nil {
		return nil, err
	}
	out := es[:0]
	for _, e := range es {
		d := e.At
		if len(d) >= 10 {
			d = d[:10]
		}
		if (from == "" || d >= from) && (to == "" || d <= to) {
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

func (o *Ops) aiLogCount(ctx context.Context, tenant, feature, from, to string) int {
	es, _ := o.aiLogBetween(ctx, tenant, from, to)
	n := 0
	for _, e := range es {
		if e.Feature == feature {
			n++
		}
	}
	return n
}

// AILog lists the AI calls from one date to another (default: the last 30 days).
func (o *Ops) AILog(ctx context.Context, tenant, actor, from, to string) (AILogView, error) {
	if err := o.require(ctx, tenant, actor, "Viewing the AI answer log", RoleCompliance); err != nil {
		return AILogView{}, err
	}
	if to == "" {
		to = o.now().Format("2006-01-02")
	}
	if from == "" {
		from = addDays(to, -30)
	}
	if !validDate(from) || !validDate(to) || from > to {
		return AILogView{}, invalid("give the period as two dates, from before to")
	}
	es, err := o.aiLogBetween(ctx, tenant, from, to)
	if err != nil {
		return AILogView{}, err
	}
	v := AILogView{From: from, To: to, Entries: []AILogEntry{}, Outcomes: map[string]int{}, Retention: o.aiControls(ctx, tenant).LogRetentionDays}
	for _, e := range es {
		v.Calls++
		v.Tokens += e.In + e.Out
		v.Charged += e.Charge
		v.Outcomes[e.Outcome]++
	}
	v.Entries = append(v.Entries, es...)
	return v, nil
}

// AILogCSV exports the log for a period.
func (o *Ops) AILogCSV(ctx context.Context, tenant, actor, from, to string) ([]byte, error) {
	v, err := o.AILog(ctx, tenant, actor, from, to)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	w.Write([]string{"ID", "Time", "User", "Feature", "Provider", "Model", "Input tokens", "Output tokens", "Charge (Rs)", "Outcome", "Masked", "Prompt (masked)", "Sent with it", "Answer (masked)", "Note"})
	for i := len(v.Entries) - 1; i >= 0; i-- {
		e := v.Entries[i]
		w.Write([]string{e.ID, e.At, e.Actor, e.Feature, e.Provider, e.Model, fmt.Sprint(e.In), fmt.Sprint(e.Out), e.Charge.String(), e.Outcome, e.Masked, csvSafe(e.Prompt), csvSafe(e.Context), csvSafe(e.Answer), csvSafe(e.Note)})
	}
	w.Flush()
	o.log(ctx, tenant, actor, "ai.log_exported", v.From+".."+v.To, map[string]any{"rows": len(v.Entries)})
	return b.Bytes(), w.Error()
}

// csvSafe stops a spreadsheet reading a cell as a formula.
func csvSafe(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

// SetAILogRetention sets how many days the log keeps prompts and answers.
func (o *Ops) SetAILogRetention(ctx context.Context, tenant, actor string, days int) (AIControls, error) {
	if err := o.require(ctx, tenant, actor, "Setting the AI answer log retention", RoleCompliance); err != nil {
		return AIControls{}, err
	}
	if days < minAILogRetentionDays || days > 3650 {
		return AIControls{}, invalid("keep the AI answer log between %d and 3650 days", minAILogRetentionDays)
	}
	now := o.now().Format(time.RFC3339)
	c, err := o.saveAIControls(ctx, tenant, actor, func(c *AIControls) error {
		c.LogRetentionDays = days
		c.Changes = append(c.Changes, AIControlChange{At: now, By: actor, What: fmt.Sprintf("AI answer log kept %d days", days)})
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "ai.log_retention", "firm", map[string]any{"days": days})
	}
	return c, err
}

// pruneAILog clears the prompt and answer of entries older than the
// retention period. The entry stays (when, who, tokens, charge, outcome)
// so the charges still reconcile.
func (o *Ops) pruneAILog(ctx context.Context, tenant, today string) int {
	days := o.aiControls(ctx, tenant).LogRetentionDays
	cut := addDays(today, -days)
	es, err := o.aiLogBetween(ctx, tenant, "", addDays(cut, -1))
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range es {
		if e.Cleared {
			continue
		}
		e.Prompt, e.Answer, e.Note, e.Context, e.Cleared = "", "", "", "", true
		if docs.Upsert(ctx, o.Docs, tenant, cAILog, e.ID, e, SystemActor) == nil {
			n++
		}
	}
	if n > 0 {
		o.log(ctx, tenant, SystemActor, "ai.log_pruned", "firm", map[string]any{"entries": n, "before": cut})
	}
	return n
}
