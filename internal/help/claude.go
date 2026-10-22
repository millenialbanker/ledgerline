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

// AI providers phrase Ledgy's answers. Each receives only the user's
// question (identifiers already masked) and the help articles that
// matched, never client data, and is told to answer only from them. Each
// reports the tokens it used so the call can be metered.

// Usage is what a call consumed, as reported by the provider.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Reply is a phrased answer.
type Reply struct {
	Text     string `json:"text"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Usage    Usage  `json:"usage"`
}

// Writer is an AI provider.
type Writer interface {
	Write(ctx context.Context, question string, articles []Article, perms []PermHit) (Reply, error)
	Provider() string
	Model() string
	MaxOutput() int
}

// MaxOutputTokens caps every answer.
const MaxOutputTokens = 700

const systemPrompt = `You are Ledgy, the helper inside Ledgerline, back-office software for Indian stockbrokers.
Answer the staff member's question using ONLY the help articles provided. Do not invent screens, buttons, rules or numbers that are not in them.
Be short and practical: a sentence of context, then numbered steps naming the exact screen and button. Mention who must do a step when the article says so (for example an Ops checker).
If the articles do not answer the question, say so in one sentence and suggest the closest article by its title.
Never give investment advice. Plain text only; use "1." for steps and **bold** for screen and button names.`

// Prompt builds the user message: the articles, the permissions and the
// question. Exported so the cost of a call can be estimated beforehand.
func Prompt(question string, articles []Article, perms []PermHit) string {
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
	return b.String()
}

// EstimateInputTokens is a generous estimate (about 3 characters a token)
// used to check a firm has enough credit before calling.
func EstimateInputTokens(question string, articles []Article, perms []PermHit) int {
	return (len(systemPrompt)+len(Prompt(question, articles, perms)))/3 + 50
}

func postJSON(ctx context.Context, hc *http.Client, url string, hdr map[string]string, body any, out any) error {
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("content-type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	if hc == nil {
		hc = &http.Client{Timeout: 25 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("help: the AI provider returned HTTP %d", resp.StatusCode)
	}
	return json.Unmarshal(raw, out)
}

func base(u, def string) string {
	if u == "" {
		u = def
	}
	return strings.TrimRight(u, "/")
}

// ---- Anthropic ----

// Claude uses Anthropic's Messages API.
type Claude struct {
	Key     string
	ModelID string // default claude-haiku-5-5
	BaseURL string // default https://api.anthropic.com
	HTTP    *http.Client
}

func (c *Claude) Provider() string { return "anthropic" }
func (c *Claude) MaxOutput() int   { return MaxOutputTokens }
func (c *Claude) Model() string {
	if c.ModelID != "" {
		return c.ModelID
	}
	return "claude-haiku-5-5"
}

func (c *Claude) Write(ctx context.Context, question string, articles []Article, perms []PermHit) (Reply, error) {
	if c.Key == "" {
		return Reply{}, fmt.Errorf("help: no Anthropic API key")
	}
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage struct {
			In  int `json:"input_tokens"`
			Out int `json:"output_tokens"`
		} `json:"usage"`
	}
	err := postJSON(ctx, c.HTTP, base(c.BaseURL, "https://api.anthropic.com")+"/v1/messages",
		map[string]string{"x-api-key": c.Key, "anthropic-version": "2023-06-01"},
		map[string]any{"model": c.Model(), "max_tokens": MaxOutputTokens, "system": systemPrompt,
			"messages": []map[string]any{{"role": "user", "content": Prompt(question, articles, perms)}}}, &out)
	if err != nil {
		return Reply{}, err
	}
	var t strings.Builder
	for _, x := range out.Content {
		if x.Type == "text" {
			t.WriteString(x.Text)
		}
	}
	return Reply{Text: t.String(), Provider: c.Provider(), Model: c.Model(), Usage: Usage{out.Usage.In, out.Usage.Out}}, nil
}

// ---- OpenAI ----

// OpenAI uses OpenAI's Chat Completions API.
type OpenAI struct {
	Key     string
	ModelID string // default gpt-5.6-luna
	BaseURL string // default https://api.openai.com
	HTTP    *http.Client
}

func (c *OpenAI) Provider() string { return "openai" }
func (c *OpenAI) MaxOutput() int   { return MaxOutputTokens }
func (c *OpenAI) Model() string {
	if c.ModelID != "" {
		return c.ModelID
	}
	return "gpt-5.6-luna"
}

func (c *OpenAI) Write(ctx context.Context, question string, articles []Article, perms []PermHit) (Reply, error) {
	if c.Key == "" {
		return Reply{}, fmt.Errorf("help: no OpenAI API key")
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			In  int `json:"prompt_tokens"`
			Out int `json:"completion_tokens"`
		} `json:"usage"`
	}
	err := postJSON(ctx, c.HTTP, base(c.BaseURL, "https://api.openai.com")+"/v1/chat/completions",
		map[string]string{"authorization": "Bearer " + c.Key},
		map[string]any{"model": c.Model(), "max_completion_tokens": MaxOutputTokens, "messages": []map[string]any{
			{"role": "system", "content": systemPrompt}, {"role": "user", "content": Prompt(question, articles, perms)}}}, &out)
	if err != nil {
		return Reply{}, err
	}
	text := ""
	if len(out.Choices) > 0 {
		text = out.Choices[0].Message.Content
	}
	return Reply{Text: text, Provider: c.Provider(), Model: c.Model(), Usage: Usage{out.Usage.In, out.Usage.Out}}, nil
}

// ---- Google Gemini ----

// Gemini uses Google's Gemini API (generateContent).
type Gemini struct {
	Key     string
	ModelID string // default gemini-3.1-flash-lite
	BaseURL string // default https://generativelanguage.googleapis.com
	HTTP    *http.Client
}

func (c *Gemini) Provider() string { return "gemini" }
func (c *Gemini) MaxOutput() int   { return MaxOutputTokens }
func (c *Gemini) Model() string {
	if c.ModelID != "" {
		return c.ModelID
	}
	return "gemini-3.1-flash-lite"
}

func (c *Gemini) Write(ctx context.Context, question string, articles []Article, perms []PermHit) (Reply, error) {
	if c.Key == "" {
		return Reply{}, fmt.Errorf("help: no Gemini API key")
	}
	var out struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
		Usage struct {
			In       int `json:"promptTokenCount"`
			Out      int `json:"candidatesTokenCount"`
			Thoughts int `json:"thoughtsTokenCount"`
		} `json:"usageMetadata"`
	}
	err := postJSON(ctx, c.HTTP, base(c.BaseURL, "https://generativelanguage.googleapis.com")+"/v1beta/models/"+c.Model()+":generateContent",
		map[string]string{"x-goog-api-key": c.Key},
		map[string]any{
			"systemInstruction": map[string]any{"parts": []map[string]string{{"text": systemPrompt}}},
			"contents":          []map[string]any{{"role": "user", "parts": []map[string]string{{"text": Prompt(question, articles, perms)}}}},
			"generationConfig":  map[string]any{"maxOutputTokens": MaxOutputTokens},
		}, &out)
	if err != nil {
		return Reply{}, err
	}
	var t strings.Builder
	if len(out.Candidates) > 0 {
		for _, p := range out.Candidates[0].Content.Parts {
			t.WriteString(p.Text)
		}
	}
	// Thinking tokens are billed as output.
	return Reply{Text: t.String(), Provider: c.Provider(), Model: c.Model(), Usage: Usage{out.Usage.In, out.Usage.Out + out.Usage.Thoughts}}, nil
}
