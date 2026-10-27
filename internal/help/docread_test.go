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

func TestReadDocument(t *testing.T) {
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
			io.WriteString(w, `{"content":[{"type":"text","text":"{\"total\":1}"}],"usage":{"input_tokens":1500,"output_tokens":40}}`)
		case strings.HasSuffix(path, "/chat/completions"):
			io.WriteString(w, `{"choices":[{"message":{"content":"{\"total\":1}"}}],"usage":{"prompt_tokens":1500,"completion_tokens":40}}`)
		default:
			io.WriteString(w, `{"candidates":[{"content":{"parts":[{"text":"{\"total\":1}"}]}}],"usageMetadata":{"promptTokenCount":1500,"candidatesTokenCount":40,"thoughtsTokenCount":10}}`)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	body := func() string { b, _ := json.Marshal(got); return string(b) }
	for _, c := range []struct {
		r    DocumentReader
		mime string
		want []string
		out  int
	}{
		{&Claude{Key: "k1", BaseURL: srv.URL}, "application/pdf", []string{`"type":"document"`, `"media_type":"application/pdf"`, `"max_tokens":1500`}, 40},
		{&Claude{Key: "k1", BaseURL: srv.URL}, "image/png", []string{`"type":"image"`, `"media_type":"image/png"`}, 40},
		{&OpenAI{Key: "k2", BaseURL: srv.URL}, "application/pdf", []string{`"file_data":"data:application/pdf;base64,`, `"max_completion_tokens":1500`}, 40},
		{&OpenAI{Key: "k2", BaseURL: srv.URL}, "image/jpeg", []string{`"image_url":{"url":"data:image/jpeg;base64,`}, 40},
		{&Gemini{Key: "k3", BaseURL: srv.URL}, "image/jpeg", []string{`"inline_data":{"data":"`, `"responseMimeType":"application/json"`}, 50},
	} {
		r, err := c.r.ReadDocument(ctx, []byte("BILL"), c.mime, "read it")
		if err != nil || r.Text != `{"total":1}` || r.Usage.InputTokens != 1500 || r.Usage.OutputTokens != c.out || key == "" {
			t.Fatalf("%T %s: %+v %v", c.r, c.mime, r, err)
		}
		for _, w := range c.want {
			if !strings.Contains(body(), w) {
				t.Errorf("%T %s: request lacks %s: %s", c.r, c.mime, w, body())
			}
		}
		if !strings.Contains(body(), "QklMTA==") || !strings.Contains(body(), "read it") {
			t.Errorf("%T: document or instruction missing", c.r)
		}
	}
	if _, err := (&Claude{}).ReadDocument(ctx, nil, "image/png", ""); err == nil {
		t.Error("no key accepted")
	}
}
