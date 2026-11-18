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
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; font-src 'self'; img-src 'self' data: blob:; frame-src 'self' blob:; connect-src 'self'; frame-ancestors 'none'")
	}
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		secure(w)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(page)
	})
	open, err := fs.ReadFile(ledgerline.FS, "web/open.html")
	if err != nil {
		panic("web: account opening page missing from build: " + err.Error())
	}
	// The public account-opening page uses the camera for the photograph.
	mux.HandleFunc("GET /open/{tenant}", func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Permissions-Policy", "camera=(self), microphone=(self), geolocation=()")
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; font-src 'self'; img-src 'self' data: blob:; media-src 'self' blob:; connect-src 'self'; frame-ancestors 'none'")
		h.Set("Content-Type", "text/html; charset=utf-8")
		h.Set("Cache-Control", "no-cache")
		w.Write(open)
	})
	for name, ct := range map[string]string{"app.js": "text/javascript; charset=utf-8", "ledgy.js": "text/javascript; charset=utf-8", "dash.js": "text/javascript; charset=utf-8", "calendar.js": "text/javascript; charset=utf-8", "chat.js": "text/javascript; charset=utf-8", "chat.css": "text/css; charset=utf-8", "rpa.js": "text/javascript; charset=utf-8", "support.js": "text/javascript; charset=utf-8", "stocksip.js": "text/javascript; charset=utf-8", "mf.js": "text/javascript; charset=utf-8", "ipo.js": "text/javascript; charset=utf-8", "ipoprod.js": "text/javascript; charset=utf-8", "payouthold.js": "text/javascript; charset=utf-8", "bowpush.js": "text/javascript; charset=utf-8", "finspotapi.js": "text/javascript; charset=utf-8", "uccapi.js": "text/javascript; charset=utf-8", "dealers.js": "text/javascript; charset=utf-8", "cdslreq.js": "text/javascript; charset=utf-8", "rpa.css": "text/css; charset=utf-8", "app.css": "text/css; charset=utf-8", "open.js": "text/javascript; charset=utf-8", "open.css": "text/css; charset=utf-8", "cdsltxn.js": "text/javascript; charset=utf-8", "cdslpledge.js": "text/javascript; charset=utf-8", "migrate.js": "text/javascript; charset=utf-8", "exreport.js": "text/javascript; charset=utf-8", "aidisc.js": "text/javascript; charset=utf-8", "boards.js": "text/javascript; charset=utf-8", "boards.css": "text/css; charset=utf-8", "boardviews.js": "text/javascript; charset=utf-8", "boardviews.css": "text/css; charset=utf-8", "boardrules.js": "text/javascript; charset=utf-8", "boardrules.css": "text/css; charset=utf-8", "boardextras.js": "text/javascript; charset=utf-8", "boardextras.css": "text/css; charset=utf-8", "boardsync.js": "text/javascript; charset=utf-8", "boardsync.css": "text/css; charset=utf-8", "boardux.js": "text/javascript; charset=utf-8", "boardux.css": "text/css; charset=utf-8", "boardnotify.js": "text/javascript; charset=utf-8", "boardnotify.css": "text/css; charset=utf-8", "boardplanner.js": "text/javascript; charset=utf-8", "boardplanner.css": "text/css; charset=utf-8", "boardio.js": "text/javascript; charset=utf-8", "boardio.css": "text/css; charset=utf-8", "selfinspect.js": "text/javascript; charset=utf-8", "selfinspect.css": "text/css; charset=utf-8", "inspack.js": "text/javascript; charset=utf-8", "inspack.css": "text/css; charset=utf-8", "circulars.js": "text/javascript; charset=utf-8", "circulars.css": "text/css; charset=utf-8", "compcal.js": "text/javascript; charset=utf-8", "compcal.css": "text/css; charset=utf-8", "clientlists.js": "text/javascript; charset=utf-8", "clientlists.css": "text/css; charset=utf-8", "layouts.js": "text/javascript; charset=utf-8", "layouts.css": "text/css; charset=utf-8", "ly-runbook.js": "text/javascript; charset=utf-8", "ly-runbook.css": "text/css; charset=utf-8", "ly-workspaces.js": "text/javascript; charset=utf-8", "ly-workspaces.css": "text/css; charset=utf-8", "ly-inbox.js": "text/javascript; charset=utf-8", "ly-inbox.css": "text/css; charset=utf-8", "ly-phone.js": "text/javascript; charset=utf-8", "ly-phone.css": "text/css; charset=utf-8", "ly-control.js": "text/javascript; charset=utf-8", "ly-control.css": "text/css; charset=utf-8", "ly-rolehome.js": "text/javascript; charset=utf-8", "ly-rolehome.css": "text/css; charset=utf-8", "ly-pilot.js": "text/javascript; charset=utf-8", "ly-pilot.css": "text/css; charset=utf-8", "ly-grid.js": "text/javascript; charset=utf-8", "ly-grid.css": "text/css; charset=utf-8", "apmap.js": "text/javascript; charset=utf-8", "raia.js": "text/javascript; charset=utf-8", "rasucc.js": "text/javascript; charset=utf-8", "clientmoney.js": "text/javascript; charset=utf-8", "clientmoney.css": "text/css; charset=utf-8", "fatca.js": "text/javascript; charset=utf-8", "fatca.css": "text/css; charset=utf-8"} {
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
	// Fonts are served from here, not from a font service, so a browser
	// using Ledgerline talks to no one but this server.
	mux.HandleFunc("GET /fonts/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if name != path.Base(name) || !strings.HasSuffix(name, ".woff2") {
			http.NotFound(w, r)
			return
		}
		b, err := fs.ReadFile(ledgerline.FS, "web/fonts/"+name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		secure(w)
		w.Header().Set("Content-Type", "font/woff2")
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.Write(b)
	})
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
