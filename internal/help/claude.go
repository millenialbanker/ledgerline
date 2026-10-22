package help

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Claude phrases Ledgy's answers with Anthropic's Claude API. It receives
// only the user's question (identifiers already masked) and the help
// articles that matched, never client data, and is told to answer only
// from those articles.
type Claude struct {
	Key     string
	Model   string // default claude-haiku-5-5
	BaseURL string // default https://api.anthropic.com
	HTTP    *http.Client
}

// Name identifies the writer on screen.
func (c *Claude) Name() string { return "Claude (" + c.model() + ")" }

func (c *Claude) model() string {
	if c.Model != "" {
		return c.Model
	}
	return "claude-haiku-5-5"
}

const claudeSystem = `You are Ledgy, the helper inside Ledgerline, back-office software for Indian stockbrokers.
Answer the staff member's question using ONLY the help articles provided. Do not invent screens, buttons, rules or numbers that are not in them.
Be short and practical: a sentence of context, then numbered steps naming the exact screen and button. Mention who must do a step when the article says so (for example an Ops checker).
If the articles do not answer the question, say so in one sentence and suggest the closest article by its title.
Never give investment advice. Plain text only; use "1." for steps and **bold** for screen and button names.`

// Write returns the phrased answer.
func (c *Claude) Write(ctx context.Context, question string, articles []Article, perms []PermHit) (string, error) {
	if c.Key == "" {
		return "", fmt.Errorf("help: no Claude API key")
	}
	var b strings.Builder
	for _, a := range articles {
		fmt.Fprintf(&b, "<article id=%q title=%q screen=%q roles=%q>\n%s\n</article>\n", a.ID, a.Title, a.Screen, strings.Join(a.Roles, ", "), a.Body)
	}
	if len(perms) > 0 {
		b.WriteString("<permissions note=\"Admin may do everything\">\n")
		for _, p := range perms {
			fmt.Fprintf(&b, "%s: %s\n", p.What, strings.Join(p.Roles, ", "))
		}
		b.WriteString("</permissions>\n")
	}
	fmt.Fprintf(&b, "\nQuestion: %s", question)
	body, _ := json.Marshal(map[string]any{
		"model":      c.model(),
		"max_tokens": 700,
		"system":     claudeSystem,
		"messages":   []map[string]any{{"role": "user", "content": b.String()}},
	})
	base := c.BaseURL
	if base == "" {
		base = "https://api.anthropic.com"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-api-key", c.Key)
	req.Header.Set("anthropic-version", "2023-06-01")
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 25 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("help: Claude API returned %d", resp.StatusCode)
	}
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	var t strings.Builder
	for _, c := range out.Content {
		if c.Type == "text" {
			t.WriteString(c.Text)
		}
	}
	return t.String(), nil
}
