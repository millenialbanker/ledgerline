package help

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCompose runs each provider's Compose against a fake API (no network):
// the instruction goes as the system message, the text as the user's, the
// output is capped and the tokens are read back for metering.
func TestCompose(t *testing.T) {
	var got map[string]any
	var path, key string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = nil
		json.Unmarshal(b, &got)
		path = r.URL.Path
		key = r.Header.Get("x-api-key") + r.Header.Get("authorization") + r.Header.Get("x-goog-api-key")
		switch {
		case strings.HasSuffix(path, "/messages"):
			io.WriteString(w, `{"content":[{"type":"text","text":"A short summary."}],"usage":{"input_tokens":300,"output_tokens":20}}`)
		case strings.HasSuffix(path, "/chat/completions"):
			io.WriteString(w, `{"choices":[{"message":{"content":"A short summary."}}],"usage":{"prompt_tokens":300,"completion_tokens":20}}`)
		default:
			io.WriteString(w, `{"candidates":[{"content":{"parts":[{"text":"A short summary."}]}}],"usageMetadata":{"promptTokenCount":300,"candidatesTokenCount":20,"thoughtsTokenCount":5}}`)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	body := func() string { b, _ := json.Marshal(got); return string(b) }
	for _, c := range []struct {
		c    Composer
		want []string
		out  int
	}{
		{&Claude{Key: "k1", BaseURL: srv.URL}, []string{`"system":"Summarise."`, `"max_tokens":200`, `"content":"Card [CLIENT-1]"`}, 20},
		{&OpenAI{Key: "k2", BaseURL: srv.URL}, []string{`"role":"system"`, `"content":"Summarise."`, `"max_completion_tokens":200`}, 20},
		{&Gemini{Key: "k3", BaseURL: srv.URL}, []string{`"systemInstruction"`, `"maxOutputTokens":200`, `"text":"Card [CLIENT-1]"`}, 25},
	} {
		r, err := c.c.Compose(ctx, "Summarise.", "Card [CLIENT-1]", 200)
		if err != nil || r.Text != "A short summary." || r.Usage.InputTokens != 300 || r.Usage.OutputTokens != c.out || key == "" || r.Provider == "" {
			t.Fatalf("%T: %+v %v", c.c, r, err)
		}
		for _, w := range c.want {
			if !strings.Contains(body(), w) {
				t.Errorf("%T: request lacks %s: %s", c.c, w, body())
			}
		}
	}
	// The cap holds whatever the caller asks for.
	if _, err := (&Claude{Key: "k1", BaseURL: srv.URL}).Compose(ctx, "x", "y", 99999); err != nil || !strings.Contains(body(), `"max_tokens":600`) {
		t.Fatalf("cap: %v %s", err, body())
	}
	if _, err := (&Claude{}).Compose(ctx, "x", "y", 100); err == nil {
		t.Error("no key accepted")
	}
	// Every provider that writes Ledgy's answers can compose.
	for _, w := range []Writer{&Claude{}, &OpenAI{}, &Gemini{}} {
		if _, ok := w.(Composer); !ok {
			t.Errorf("%T is not a Composer", w)
		}
	}
}
