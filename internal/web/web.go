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
	"ledgerline/internal/screens"
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
	// The attendance kiosk (HRMS): a tablet at the office the firm's HR
	// registered; it uses the camera for the check-in photo. The kiosk's
	// calls are /kiosk-api/{tenant}/... (internal/api/hrms.go).
	kiosk, err := fs.ReadFile(ledgerline.FS, "web/kiosk.html")
	if err != nil {
		panic("web: kiosk page missing from build: " + err.Error())
	}
	mux.HandleFunc("GET /kiosk/{tenant}", func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Permissions-Policy", "camera=(self), microphone=(), geolocation=()")
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; font-src 'self'; img-src 'self' data: blob:; media-src 'self' blob:; connect-src 'self'; frame-ancestors 'none'")
		h.Set("Content-Type", "text/html; charset=utf-8")
		h.Set("Cache-Control", "no-cache")
		w.Write(kiosk)
	})
	// The support desk: Ledgerline's own support people sign in here, ask a
	// firm for access and open the sessions the firm approved.
	desk, err := fs.ReadFile(ledgerline.FS, "web/support-desk.html")
	if err != nil {
		panic("web: support desk page missing from build: " + err.Error())
	}
	mux.HandleFunc("GET /support-desk", func(w http.ResponseWriter, r *http.Request) {
		secure(w)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(desk)
	})
	// The staff app and the client app: phone pages that the native shells
	// (mobile/staff, mobile/client) open. They call the same API.
	// The staff app's attendance check-in may use the location and the
	// front camera (a selfie when the firm asks); the client app neither.
	for route, file := range map[string]string{"GET /m/{$}": "web/m.html", "GET /app/{$}": "web/mapp.html"} {
		b, err := fs.ReadFile(ledgerline.FS, file)
		if err != nil {
			panic("web: " + file + " missing from build: " + err.Error())
		}
		perm := "camera=(), microphone=(), geolocation=()"
		if file == "web/m.html" {
			perm = "camera=(self), microphone=(), geolocation=(self)"
		}
		mux.HandleFunc(route, func(w http.ResponseWriter, r *http.Request) {
			secure(w)
			w.Header().Set("Permissions-Policy", perm)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			w.Write(b)
		})
	}
	for name, ct := range map[string]string{"compregs.js": "text/javascript; charset=utf-8", "rmslimits.js": "text/javascript; charset=utf-8", "settle.js": "text/javascript; charset=utf-8", "app.js": "text/javascript; charset=utf-8", "acctpick.js": "text/javascript; charset=utf-8", "books.js": "text/javascript; charset=utf-8", "firmsec.js": "text/javascript; charset=utf-8", "books.css": "text/css; charset=utf-8", "ledgy.js": "text/javascript; charset=utf-8", "dash.js": "text/javascript; charset=utf-8", "calendar.js": "text/javascript; charset=utf-8", "chat.js": "text/javascript; charset=utf-8", "chat.css": "text/css; charset=utf-8", "rpa.js": "text/javascript; charset=utf-8", "support.js": "text/javascript; charset=utf-8", "supportaccess.js": "text/javascript; charset=utf-8", "security.js": "text/javascript; charset=utf-8", "dataexit.js": "text/javascript; charset=utf-8", "practices.js": "text/javascript; charset=utf-8", "firmkey.js": "text/javascript; charset=utf-8", "supportdesk.js": "text/javascript; charset=utf-8", "stocksip.js": "text/javascript; charset=utf-8", "mf.js": "text/javascript; charset=utf-8", "ipo.js": "text/javascript; charset=utf-8", "ipoprod.js": "text/javascript; charset=utf-8", "rfq.js": "text/javascript; charset=utf-8", "cbrics.js": "text/javascript; charset=utf-8", "payouthold.js": "text/javascript; charset=utf-8", "bowpush.js": "text/javascript; charset=utf-8", "finspotapi.js": "text/javascript; charset=utf-8", "uccapi.js": "text/javascript; charset=utf-8", "dealers.js": "text/javascript; charset=utf-8", "cdslreq.js": "text/javascript; charset=utf-8", "rpa.css": "text/css; charset=utf-8", "app.css": "text/css; charset=utf-8", "open.js": "text/javascript; charset=utf-8", "open.css": "text/css; charset=utf-8", "cdsltxn.js": "text/javascript; charset=utf-8", "cdslpledge.js": "text/javascript; charset=utf-8", "migrate.js": "text/javascript; charset=utf-8", "exreport.js": "text/javascript; charset=utf-8", "aidisc.js": "text/javascript; charset=utf-8", "boards.js": "text/javascript; charset=utf-8", "boards.css": "text/css; charset=utf-8", "boardviews.js": "text/javascript; charset=utf-8", "boardviews.css": "text/css; charset=utf-8", "boardrules.js": "text/javascript; charset=utf-8", "boardrules.css": "text/css; charset=utf-8", "boardextras.js": "text/javascript; charset=utf-8", "boardextras.css": "text/css; charset=utf-8", "boardsync.js": "text/javascript; charset=utf-8", "boardsync.css": "text/css; charset=utf-8", "boardux.js": "text/javascript; charset=utf-8", "boardux.css": "text/css; charset=utf-8", "boardnotify.js": "text/javascript; charset=utf-8", "boardnotify.css": "text/css; charset=utf-8", "boardplanner.js": "text/javascript; charset=utf-8", "boardplanner.css": "text/css; charset=utf-8", "boardio.js": "text/javascript; charset=utf-8", "boardio.css": "text/css; charset=utf-8", "selfinspect.js": "text/javascript; charset=utf-8", "selfinspect.css": "text/css; charset=utf-8", "inspack.js": "text/javascript; charset=utf-8", "inspack.css": "text/css; charset=utf-8", "circulars.js": "text/javascript; charset=utf-8", "circulars.css": "text/css; charset=utf-8", "compcal.js": "text/javascript; charset=utf-8", "compcal.css": "text/css; charset=utf-8", "clientlists.js": "text/javascript; charset=utf-8", "clientlists.css": "text/css; charset=utf-8", "layouts.js": "text/javascript; charset=utf-8", "layouts.css": "text/css; charset=utf-8", "ly-runbook.js": "text/javascript; charset=utf-8", "ly-runbook.css": "text/css; charset=utf-8", "ly-workspaces.js": "text/javascript; charset=utf-8", "ly-workspaces.css": "text/css; charset=utf-8", "ly-inbox.js": "text/javascript; charset=utf-8", "ly-inbox.css": "text/css; charset=utf-8", "ly-phone.js": "text/javascript; charset=utf-8", "ly-phone.css": "text/css; charset=utf-8", "ly-control.js": "text/javascript; charset=utf-8", "ly-control.css": "text/css; charset=utf-8", "ly-rolehome.js": "text/javascript; charset=utf-8", "ly-rolehome.css": "text/css; charset=utf-8", "ly-pilot.js": "text/javascript; charset=utf-8", "ly-pilot.css": "text/css; charset=utf-8", "ly-grid.js": "text/javascript; charset=utf-8", "ly-grid.css": "text/css; charset=utf-8", "apmap.js": "text/javascript; charset=utf-8", "raia.js": "text/javascript; charset=utf-8", "rasucc.js": "text/javascript; charset=utf-8", "parallel.js": "text/javascript; charset=utf-8", "firmdemats.js": "text/javascript; charset=utf-8", "dpsurv.js": "text/javascript; charset=utf-8", "dpctl.js": "text/javascript; charset=utf-8", "clientmoney.js": "text/javascript; charset=utf-8", "clientmoney.css": "text/css; charset=utf-8", "fatca.js": "text/javascript; charset=utf-8", "sanctions.js": "text/javascript; charset=utf-8", "fatca.css": "text/css; charset=utf-8", "roles.js": "text/javascript; charset=utf-8", "roles.css": "text/css; charset=utf-8", "activity.js": "text/javascript; charset=utf-8", "activity.css": "text/css; charset=utf-8", "steps.js": "text/javascript; charset=utf-8", "mshell.js": "text/javascript; charset=utf-8", "mstaff.js": "text/javascript; charset=utf-8", "mclient.js": "text/javascript; charset=utf-8", "mobile.css": "text/css; charset=utf-8", "mobileapps.js": "text/javascript; charset=utf-8", "hrms.js": "text/javascript; charset=utf-8", "cyber.js": "text/javascript; charset=utf-8", "resilience.js": "text/javascript; charset=utf-8", "kiosk.js": "text/javascript; charset=utf-8", "kiosk.css": "text/css; charset=utf-8"} {
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
	// The staff screens' catalogue: the same list the server checks role
	// permissions against (internal/screens).
	screensJS := screens.JS()
	mux.HandleFunc("GET /screens.js", func(w http.ResponseWriter, r *http.Request) {
		secure(w)
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(screensJS)
	})
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
