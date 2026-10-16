// Package web serves the browser console. The console is one static page
// that calls the JSON API on the same origin; it holds no data of its own.
package web

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"ledgerline"
)

// Config is what the console learns about the server before sign-in.
type Config struct {
	Dev         bool      `json:"dev"`
	Tenant      string    `json:"tenant,omitempty"` // set when the server serves one firm
	Version     string    `json:"version"`
	DevUsers    []DevUser `json:"dev_users,omitempty"`    // development only: one-click sign-in
	DevPassword string    `json:"dev_password,omitempty"` // development only
}

// DevUser is a development user offered on the sign-in page.
type DevUser struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
}

// Handler serves the console at "/", its config at "/ui/config" and, in
// development mode only, the sample trading-day files at "/sample/".
func Handler(cfg Config) http.Handler {
	mux := http.NewServeMux()
	page, err := fs.ReadFile(ledgerline.FS, "web/index.html")
	if err != nil {
		panic("web: console page missing from build: " + err.Error())
	}
	secure := func(w http.ResponseWriter) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; font-src 'self' https://fonts.gstatic.com; img-src 'self' data: blob:; connect-src 'self'; frame-ancestors 'none'")
	}
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		secure(w)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(page)
	})
	for name, ct := range map[string]string{"app.js": "text/javascript; charset=utf-8", "app.css": "text/css; charset=utf-8"} {
		b, err := fs.ReadFile(ledgerline.FS, "web/"+name)
		if err != nil {
			panic("web: " + name + " missing from build: " + err.Error())
		}
		ct := ct
		mux.HandleFunc("GET /"+name, func(w http.ResponseWriter, r *http.Request) {
			secure(w)
			w.Header().Set("Content-Type", ct)
			w.Header().Set("Cache-Control", "no-cache")
			w.Write(b)
		})
	}
	mux.HandleFunc("GET /ui/config", func(w http.ResponseWriter, r *http.Request) {
		secure(w)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		c := cfg
		if !c.Dev {
			c.DevUsers, c.DevPassword = nil, ""
		}
		json.NewEncoder(w).Encode(c)
	})
	if cfg.Dev {
		mux.HandleFunc("GET /sample/{name}", func(w http.ResponseWriter, r *http.Request) {
			name := r.PathValue("name")
			if name != path.Base(name) || strings.HasPrefix(name, ".") {
				http.NotFound(w, r)
				return
			}
			b, err := fs.ReadFile(ledgerline.FS, "testdata/sample/"+name)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			secure(w)
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Write(b)
		})
	}
	return mux
}
