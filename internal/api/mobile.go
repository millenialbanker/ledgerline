package api

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	neturl "net/url"
	"strings"

	"ledgerline/internal/ops"
	"ledgerline/internal/qr"
)

// The staff app and the client app (ops/mobile.go, mobilepush.go,
// mobilestaff.go). Both are phone pages served by Ledgerline (/m/ and
// /app/) inside a native shell; these are their calls.
//
//	GET  /v1/mobile/config?app=&firm=      before sign-in: firm, brand, disclosures
//	POST /v1/mobile/unlock                 device ID + secret → an app session
//	POST /v1/mobile/devices                register this phone (after a password sign-in)
//	GET  /v1/mobile/devices[?all=staff|user]
//	POST /v1/mobile/devices/{id}/revoke
//	PUT  /v1/mobile/devices/{id}/push      a new push token
//	GET  /v1/mobile/notifications          the pushes sent to me (words only)
//	GET|PUT /v1/mobile/settings            Settings → Staff app / Client app
//	GET  /v1/mobile/staff-qr               the QR that sets up the staff app
//	POST /v1/mobile/test-push
//	GET  /v1/m/home|approvals|work|day|alerts|clients, POST /v1/m/approvals/{id}

// StaffAppScheme is the staff app's link scheme; the QR code opens the app
// with ledgerline-staff://setup?server=…&firm=… (mobile/staff/app.config.json
// must use the same scheme).
const StaffAppScheme = "ledgerline-staff"

func (s *Server) mobileRoutes() {
	s.mux.HandleFunc("GET /v1/mobile/config", s.mobileConfig)
	s.mux.HandleFunc("POST /v1/mobile/unlock", s.mobileUnlock)
	R := func(p string, h http.HandlerFunc, m bool) { s.route(p, h, m) }
	R("POST /v1/mobile/devices", s.mobileRegister, true)
	R("GET /v1/mobile/devices", s.mobileDevices, false)
	R("POST /v1/mobile/devices/{id}/revoke", s.mobileRevoke, true)
	R("PUT /v1/mobile/devices/{id}/push", s.mobilePushToken, true)
	R("GET /v1/mobile/notifications", s.mobileNotifications, false)
	R("GET /v1/mobile/settings", s.mobileSettings, false)
	R("PUT /v1/mobile/settings", s.mobileSaveSettings, true)
	R("GET /v1/mobile/staff-qr", s.mobileStaffQR, false)
	R("POST /v1/mobile/test-push", s.mobileTestPush, true)
	R("GET /v1/m/home", s.mHome, false)
	R("GET /v1/m/approvals", s.mApprovals, false)
	R("POST /v1/m/approvals/{id}", s.mDecide, true)
	R("GET /v1/m/work", s.mWork, false)
	R("GET /v1/m/day", s.mDay, false)
	R("GET /v1/m/alerts", s.mAlerts, false)
	R("GET /v1/m/clients", s.mClients, false)
}

// The phone calls open to branch staff and partners (each filters to their
// clients as the desktop does) and to client logins (their own phones).
// Kept here, read-only, and merged into the route lists at start-up.
var (
	mobileScoped = []string{"POST /v1/mobile/devices", "GET /v1/mobile/devices", "POST /v1/mobile/devices/{id}/revoke", "PUT /v1/mobile/devices/{id}/push", "GET /v1/mobile/notifications",
		"GET /v1/m/home", "GET /v1/m/approvals", "POST /v1/m/approvals/{id}", "GET /v1/m/work", "GET /v1/m/day", "GET /v1/m/alerts", "GET /v1/m/clients", "POST /v1/mobile/test-push"}
	mobileClient = []string{"POST /v1/mobile/devices", "GET /v1/mobile/devices", "POST /v1/mobile/devices/{id}/revoke", "PUT /v1/mobile/devices/{id}/push", "GET /v1/mobile/notifications"}
)

func init() {
	for _, p := range mobileScoped {
		scopedRoutes[p] = true
	}
	for _, p := range mobileClient {
		clientRoutes[p] = true
	}
}

// firmOf is the firm a public mobile call names, or the only one served.
func (s *Server) firmOf(v string) string {
	t := strings.ToLower(strings.TrimSpace(v))
	if ts := s.Tenants(); t == "" && len(ts) == 1 {
		t = ts[0]
	}
	if !s.knownTenant(t) {
		return ""
	}
	return t
}

func (s *Server) mobileConfig(w http.ResponseWriter, r *http.Request) {
	if s.ops == nil {
		writeErr(w, 404, "not_found", "not available", nil)
		return
	}
	q := r.URL.Query()
	t := s.firmOf(q.Get("firm"))
	if t == "" {
		writeErr(w, 404, "unknown_firm", "Unknown firm code.", nil)
		return
	}
	if s.ops.FirmClosed(t) {
		writeClosed(w)
		return
	}
	app := ops.AppStaff
	if q.Get("app") == ops.AppClient {
		app = ops.AppClient
	}
	v, err := s.ops.MobileAppConfig(context.WithValue(r.Context(), keyTenant, t), t, app)
	w.Header().Set("Cache-Control", "no-store")
	ok(w, v, err)
}

// mobileUnlock opens an app session with the phone's device secret. It
// counts against the same per-address limit as sign-in.
func (s *Server) mobileUnlock(w http.ResponseWriter, r *http.Request) {
	if s.ops == nil {
		writeErr(w, 404, "not_found", "not available", nil)
		return
	}
	var b struct {
		Tenant string `json:"tenant"`
		Device string `json:"device"`
		Secret string `json:"secret"`
	}
	if !decode(w, r, &b) {
		return
	}
	ip := clientIP(r)
	if !s.loginLimit.allow(ip) {
		writeErr(w, 429, "too_many_attempts", "Too many sign-in attempts from this address. Wait 15 minutes and try again.", nil)
		return
	}
	t := s.firmOf(b.Tenant)
	if t == "" {
		writeErr(w, 401, "auth_failed", "Unknown firm code.", nil)
		return
	}
	if s.ops.FirmClosed(t) {
		writeClosed(w)
		return
	}
	ctx := context.WithValue(r.Context(), keyTenant, t)
	tok, st, d, err := s.ops.Unlock(ctx, t, b.Device, b.Secret, ip, r.UserAgent())
	switch {
	case errors.Is(err, ops.ErrDeviceRevoked):
		writeErr(w, 401, "device_revoked", err.Error(), nil)
		return
	case errors.Is(err, ops.ErrReauth):
		writeErr(w, 401, "reauth", err.Error(), nil)
		return
	case err != nil:
		fail(w, err, nil)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: t + "." + tok, Path: "/", HttpOnly: true, Secure: secureRequest(r), SameSite: http.SameSiteStrictMode, MaxAge: 12 * 3600})
	writeJSON(w, 200, map[string]any{"tenant": t, "auth": st, "user": d.User, "app": d.App})
}

func (s *Server) mobileRegister(w http.ResponseWriter, r *http.Request) {
	var b ops.DeviceReq
	if !decode(w, r, &b) {
		return
	}
	_, _, tok, err := s.sessionAuth(r)
	if err != nil || r.Header.Get("Authorization") != "" {
		writeErr(w, 403, "forbidden", "Register a phone from the app, after signing in with your password.", nil)
		return
	}
	d, secret, err := s.ops.RegisterDevice(r.Context(), tenant(r), actor(r), tok, b)
	if err != nil {
		fail(w, err, nil)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 201, map[string]any{"device": d, "secret": secret})
}

func (s *Server) mobileDevices(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.Devices(r.Context(), tenant(r), actor(r), r.URL.Query().Get("all"))
	this := ""
	if _, _, tok, e := s.sessionAuth(r); e == nil {
		this = s.ops.SessionDevice(r.Context(), tenant(r), tok)
	}
	ok(w, map[string]any{"devices": v, "this": this}, err)
}

func (s *Server) mobileRevoke(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Note string `json:"note"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RevokeDevice(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Note)
	ok(w, v, err)
}

func (s *Server) mobilePushToken(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Token string `json:"token"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SetPushToken(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Token)
	ok(w, v, err)
}

func (s *Server) mobileNotifications(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.Notifications(r.Context(), tenant(r), actor(r))
	ok(w, map[string]any{"notifications": v}, err)
}

func (s *Server) mobileSettings(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.MobileSettingsFor(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) mobileSaveSettings(w http.ResponseWriter, r *http.Request) {
	var b ops.MobileSettings
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveMobileSettings(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

// mobileStaffQR is the set-up QR for Settings → Staff app: scanned with
// the phone's camera it opens the staff app with this server's address
// and the firm code. It holds no secret: the person still signs in.
func (s *Server) mobileStaffQR(w http.ResponseWriter, r *http.Request) {
	if !s.allowed(w, r, "Viewing the staff app set-up code", ops.RoleAdmin) {
		return
	}
	base := s.publicURL(r)
	link := StaffAppScheme + "://setup?server=" + neturl.QueryEscape(base) + "&firm=" + neturl.QueryEscape(tenant(r))
	c, err := qr.Encode([]byte(link))
	if err != nil {
		fail(w, err, nil)
		return
	}
	png, err := c.PNG(6)
	if err != nil {
		fail(w, err, nil)
		return
	}
	writeJSON(w, 200, map[string]any{"link": link, "server": base, "firm": tenant(r), "web": base + "/m/?firm=" + neturl.QueryEscape(tenant(r)),
		"qr": "data:image/png;base64," + base64.StdEncoding.EncodeToString(png), "https": strings.HasPrefix(base, "https://")})
}

func (s *Server) mobileTestPush(w http.ResponseWriter, r *http.Request) {
	n, err := s.ops.TestPush(r.Context(), tenant(r), actor(r))
	ok(w, map[string]any{"sent": n}, err)
}

// ---- the staff app's screens ----

// mDate is ?date=, or the firm's business date (today, or the last
// business day on a holiday).
func (s *Server) mDate(w http.ResponseWriter, r *http.Request) (string, bool) {
	d := r.URL.Query().Get("date")
	if d == "" {
		return s.ops.MobileDate(r.Context(), tenant(r)), true
	}
	return dateParam(w, d, "date", true)
}

func (s *Server) mHome(w http.ResponseWriter, r *http.Request) {
	d, okd := s.mDate(w, r)
	if !okd {
		return
	}
	v, err := s.ops.StaffHome(r.Context(), tenant(r), actor(r), d)
	if _, _, tok, e := s.sessionAuth(r); e == nil && err == nil {
		v.Device = s.ops.SessionDevice(r.Context(), tenant(r), tok)
	}
	ok(w, v, err)
}

func (s *Server) mApprovals(w http.ResponseWriter, r *http.Request) {
	d, okd := s.mDate(w, r)
	if !okd {
		return
	}
	v, err := s.ops.StaffApprovals(r.Context(), tenant(r), actor(r), d)
	ok(w, v, err)
}

func (s *Server) mDecide(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.StaffDecide(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Decision, b.Reason)
	ok(w, v, err)
}

func (s *Server) mWork(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.StaffWork(r.Context(), tenant(r), actor(r))
	ok(w, map[string]any{"items": v}, err)
}

func (s *Server) mDay(w http.ResponseWriter, r *http.Request) {
	d, okd := s.mDate(w, r)
	if !okd {
		return
	}
	v, err := s.ops.StaffDay(r.Context(), tenant(r), actor(r), d)
	ok(w, v, err)
}

func (s *Server) mAlerts(w http.ResponseWriter, r *http.Request) {
	d, okd := s.mDate(w, r)
	if !okd {
		return
	}
	v, err := s.ops.StaffAlerts(r.Context(), tenant(r), actor(r), d)
	ok(w, map[string]any{"alerts": v}, err)
}

func (s *Server) mClients(w http.ResponseWriter, r *http.Request) {
	d, okd := s.mDate(w, r)
	if !okd {
		return
	}
	v, err := s.ops.StaffClients(r.Context(), tenant(r), actor(r), d, r.URL.Query().Get("q"))
	ok(w, map[string]any{"clients": v}, err)
}
