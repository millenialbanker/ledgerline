package help

import (
	"context"
	"fmt"
	"strings"
)

// Composer writes a short text for a task other than Ledgy's answers (for
// example summarising a board card or suggesting checklist items), from a
// fixed instruction and the text to work on. The caller masks the text
// before it is sent. Each provider that phrases Ledgy's answers also
// implements Composer; Write and its prompt are unchanged.
type Composer interface {
	Compose(ctx context.Context, instruction, text string, maxOut int) (Reply, error)
}

// MaxComposeTokens caps a composed text.
const MaxComposeTokens = 600

func composeCap(n int) int {
	if n <= 0 || n > MaxComposeTokens {
		return MaxComposeTokens
	}
	return n
}

// EstimateComposeTokens is a generous estimate of a composed call's input
// (about 3 characters a token), to check the firm's credit beforehand.
func EstimateComposeTokens(instruction, text string) int {
	return (len(instruction)+len(text))/3 + 50
}

// Compose sends the instruction and text to Claude.
func (c *Claude) Compose(ctx context.Context, instruction, text string, maxOut int) (Reply, error) {
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
		map[string]any{"model": c.Model(), "max_tokens": composeCap(maxOut), "system": instruction,
			"messages": []map[string]any{{"role": "user", "content": text}}}, &out)
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

// Compose sends the instruction and text to OpenAI.
func (c *OpenAI) Compose(ctx context.Context, instruction, text string, maxOut int) (Reply, error) {
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
		map[string]any{"model": c.Model(), "max_completion_tokens": composeCap(maxOut), "messages": []map[string]any{
			{"role": "system", "content": instruction}, {"role": "user", "content": text}}}, &out)
	if err != nil {
		return Reply{}, err
	}
	t := ""
	if len(out.Choices) > 0 {
		t = out.Choices[0].Message.Content
	}
	return Reply{Text: t, Provider: c.Provider(), Model: c.Model(), Usage: Usage{out.Usage.In, out.Usage.Out}}, nil
}

// Compose sends the instruction and text to Gemini.
func (c *Gemini) Compose(ctx context.Context, instruction, text string, maxOut int) (Reply, error) {
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
			"systemInstruction": map[string]any{"parts": []map[string]string{{"text": instruction}}},
			"contents":          []map[string]any{{"role": "user", "parts": []map[string]string{{"text": text}}}},
			"generationConfig":  map[string]any{"maxOutputTokens": composeCap(maxOut)},
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
	return Reply{Text: t.String(), Provider: c.Provider(), Model: c.Model(), Usage: Usage{out.Usage.In, out.Usage.Out + out.Usage.Thoughts}}, nil
}
