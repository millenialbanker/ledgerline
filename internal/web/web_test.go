package web

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"ledgerline"
)

// TestConsoleAssetsServed: every script and stylesheet the console page
// loads is served, as JavaScript or CSS (browsers refuse a script sent as
// plain text).
func TestConsoleAssetsServed(t *testing.T) {
	page, err := fs.ReadFile(ledgerline.FS, "web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	h := Handler(Config{})
	refs := regexp.MustCompile(`(?:src|href)="/([a-z0-9]+\.(?:js|css))"`).FindAllStringSubmatch(string(page), -1)
	if len(refs) < 10 {
		t.Fatalf("only %d assets found", len(refs))
	}
	for _, m := range refs {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/"+m[1], nil))
		want := map[bool]string{true: "text/javascript", false: "text/css"}[strings.HasSuffix(m[1], ".js")]
		if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), want) {
			t.Errorf("/%s: %d %s", m[1], rec.Code, rec.Header().Get("Content-Type"))
		}
	}
}
