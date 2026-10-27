package help

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// DocumentReader reads a document (a scanned bill, a PDF) with an AI
// model and answers an instruction about it. Used only where the firm has
// switched it on: the document leaves the server for the provider.
type DocumentReader interface {
	ReadDocument(ctx context.Context, data []byte, mime, instruction string) (Reply, error)
}

// MaxDocOutputTokens caps a document reading.
const MaxDocOutputTokens = 1500

var docHTTP = &http.Client{Timeout: 90 * time.Second}

func docClient(hc *http.Client) *http.Client {
	if hc != nil {
		return hc
	}
	return docHTTP
}

// ReadDocument sends the document to Claude (images and PDFs).
func (c *Claude) ReadDocument(ctx context.Context, data []byte, mime, instruction string) (Reply, error) {
	if c.Key == "" {
		return Reply{}, fmt.Errorf("help: no Anthropic API key")
	}
	block := map[string]any{"type": "image", "source": map[string]string{"type": "base64", "media_type": mime, "data": base64.StdEncoding.EncodeToString(data)}}
	if mime == "application/pdf" {
		block["type"] = "document"
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
	err := postJSON(ctx, docClient(c.HTTP), base(c.BaseURL, "https://api.anthropic.com")+"/v1/messages",
		map[string]string{"x-api-key": c.Key, "anthropic-version": "2023-06-01"},
		map[string]any{"model": c.Model(), "max_tokens": MaxDocOutputTokens,
			"messages": []map[string]any{{"role": "user", "content": []any{block, map[string]string{"type": "text", "text": instruction}}}}}, &out)
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

// ReadDocument sends the document to OpenAI (images and PDFs).
func (c *OpenAI) ReadDocument(ctx context.Context, data []byte, mime, instruction string) (Reply, error) {
	if c.Key == "" {
		return Reply{}, fmt.Errorf("help: no OpenAI API key")
	}
	url := "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
	part := map[string]any{"type": "image_url", "image_url": map[string]string{"url": url}}
	if mime == "application/pdf" {
		part = map[string]any{"type": "file", "file": map[string]string{"filename": "bill.pdf", "file_data": url}}
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
	err := postJSON(ctx, docClient(c.HTTP), base(c.BaseURL, "https://api.openai.com")+"/v1/chat/completions",
		map[string]string{"authorization": "Bearer " + c.Key},
		map[string]any{"model": c.Model(), "max_completion_tokens": MaxDocOutputTokens, "messages": []map[string]any{
			{"role": "user", "content": []any{part, map[string]string{"type": "text", "text": instruction}}}}}, &out)
	if err != nil {
		return Reply{}, err
	}
	text := ""
	if len(out.Choices) > 0 {
		text = out.Choices[0].Message.Content
	}
	return Reply{Text: text, Provider: c.Provider(), Model: c.Model(), Usage: Usage{out.Usage.In, out.Usage.Out}}, nil
}

// ReadDocument sends the document to Gemini (images and PDFs).
func (c *Gemini) ReadDocument(ctx context.Context, data []byte, mime, instruction string) (Reply, error) {
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
	err := postJSON(ctx, docClient(c.HTTP), base(c.BaseURL, "https://generativelanguage.googleapis.com")+"/v1beta/models/"+c.Model()+":generateContent",
		map[string]string{"x-goog-api-key": c.Key},
		map[string]any{
			"contents": []map[string]any{{"role": "user", "parts": []any{
				map[string]any{"inline_data": map[string]string{"mime_type": mime, "data": base64.StdEncoding.EncodeToString(data)}},
				map[string]string{"text": instruction}}}},
			"generationConfig": map[string]any{"maxOutputTokens": MaxDocOutputTokens, "responseMimeType": "application/json"},
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
