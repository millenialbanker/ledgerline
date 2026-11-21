package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/app"
	"ledgerline/internal/charges"
	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ops"
	"ledgerline/internal/sample"
	"ledgerline/internal/vault"
)

func mobileServer(t *testing.T, withDay bool) (*httptest.Server, *ops.Ops, context.Context) {
	t.Helper()
	ops.PasswordIterations = 1000
	reg, _ := ingest.LoadDir("../../config/adapters")
	f, _ := os.Open("../../config/charges/sample-schedule.json")
	defer f.Close()
	sched, _ := charges.LoadSchedule(f)
	a := app.New(vault.Protect(app.MemoryStores(), vault.New(vault.Ephemeral())), reg, sched)
	o := ops.New(a)
	ctx := context.Background()
	if err := o.SeedDev(ctx, "demo"); err != nil {
		t.Fatal(err)
	}
	if withDay {
		if _, err := o.LoadSampleDay(ctx, "demo", "priya.nair", sample.Generate("2026-10-16"), false, func(ctx context.Context, tenant, actor, source, name string, data []byte) error {
			_, err := a.IngestFile(ctx, tenant, actor, source, "", name, data)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	o.Now = func() time.Time { return time.Date(2026, 10, 16, 11, 0, 0, 0, time.FixedZone("IST", 19800)) }
	srv := httptest.NewServer(New(a, o, map[string]string{key: "demo"}, slog.New(slog.NewTextHandler(io.Discard, nil)), true))
	t.Cleanup(srv.Close)
	return srv, o, ctx
}

// TestStaffAppScoping: the staff app's calls follow the staff app setting,
// the role and the branch or partner scope exactly as the desktop does.
func TestStaffAppScoping(t *testing.T) {
	srv, o, ctx := mobileServer(t, true)
	if c := ipoCall(t, srv, "GET", "/v1/m/home?date=2026-10-16", "farhan.khan", nil, nil); c != 403 {
		t.Fatalf("staff app off: %d", c)
	}
	set := map[string]any{"staff_on": true, "staff_roles": []string{"Ops checker", "Branch staff", "Authorised person", "Admin"}}
	if c := ipoCall(t, srv, "PUT", "/v1/mobile/settings", "priya.nair", set, nil); c != 403 {
		t.Fatalf("maker changed the settings: %d", c)
	}
	if c := ipoCall(t, srv, "PUT", "/v1/mobile/settings", "admin", set, nil); c != 200 {
		t.Fatalf("settings: %d", c)
	}
	// Clients inside and outside the branch user's scope.
	f := o.ClientScope(ctx, "demo", "meera.iyer")
	cs, _ := o.Clients(ctx, "demo")
	var in, out ops.Client
	for _, c := range cs {
		if c.Status != "Active" || c.PAN == "" {
			continue
		}
		if f(c.UCC) && in.UCC == "" {
			in = c
		}
		if !f(c.UCC) && out.UCC == "" {
			out = c
		}
	}
	if in.UCC == "" || out.UCC == "" {
		t.Fatal("sample has no clients on both sides of the branch")
	}
	put := func(id, role, ucc string, p *ops.Proposal) {
		w := ops.WorkItem{ID: id, Key: "test:" + id, Process: "payouts", Title: "Check " + ucc, Detail: ucc + " asks for ₹12,500.00", UCC: ucc, Role: role, View: "money", Date: "2026-10-16", Due: "2026-10-16 23:59", Status: "Open", Proposed: p, Created: "2026-10-16T09:00:00+05:30"}
		if err := docs.Upsert(ctx, o.Docs, "demo", "work", id, w, "test"); err != nil {
			t.Fatal(err)
		}
	}
	prep := &ops.Proposal{Kind: "risk.set", Label: "Set risk High", Args: map[string]string{"ucc": in.UCC, "risk": "High", "why": "test"}}
	put("W-test-in", "", in.UCC, prep)
	put("W-test-out", "", out.UCC, prep)
	put("W-test-co", "Compliance officer", in.UCC, prep)

	var ap ops.MobileApprovals
	if c := ipoCall(t, srv, "GET", "/v1/m/approvals?date=2026-10-16", "meera.iyer", nil, &ap); c != 200 {
		t.Fatalf("approvals: %d", c)
	}
	ids := map[string]ops.MobileItem{}
	for _, w := range ap.Waiting {
		ids[w.ID] = w
	}
	if _, ok := ids["W-test-in"]; !ok {
		t.Fatalf("branch user does not see her client's item: %+v", ap.Waiting)
	}
	if _, ok := ids["W-test-out"]; ok {
		t.Fatal("branch user sees another branch's client")
	}
	if _, ok := ids["W-test-co"]; ok {
		t.Fatal("branch user sees the compliance officer's item")
	}
	if ids["W-test-in"].Amount != "₹12,500.00" || ids["W-test-in"].Action != "Set risk High" {
		t.Fatalf("item %+v", ids["W-test-in"])
	}
	if c := ipoCall(t, srv, "POST", "/v1/m/approvals/W-test-out", "meera.iyer", map[string]string{"decision": "reject", "reason": "not mine"}, nil); c != 404 {
		t.Fatalf("decided another branch's item: %d", c)
	}
	if c := ipoCall(t, srv, "POST", "/v1/m/approvals/W-test-in", "meera.iyer", map[string]string{"decision": "reject"}, nil); c != 422 {
		t.Fatalf("rejected without a reason: %d", c)
	}
	var it ops.MobileItem
	if c := ipoCall(t, srv, "POST", "/v1/m/approvals/W-test-in", "meera.iyer", map[string]string{"decision": "reject", "reason": "Client withdrew"}, &it); c != 200 || it.Status != "Dismissed" {
		t.Fatalf("reject: %d %+v", c, it)
	}
	// The desktop's role check: a checker cannot approve the compliance
	// officer's prepared action; the Admin can.
	if c := ipoCall(t, srv, "POST", "/v1/m/approvals/W-test-co", "farhan.khan", map[string]string{"decision": "approve"}, nil); c != 403 {
		t.Fatalf("checker approved a compliance item: %d", c)
	}
	if c := ipoCall(t, srv, "POST", "/v1/m/approvals/W-test-co", "admin", map[string]string{"decision": "approve"}, &it); c != 200 || it.Status != "Done" {
		t.Fatalf("admin approve: %d %+v", c, it)
	}
	if c, _ := o.Client(ctx, "demo", in.UCC); c.Risk != "High" {
		t.Fatalf("prepared action not carried out: %+v", c.Risk)
	}
	// The day: firm-wide steps for the head office only.
	var day ops.MobileDay
	if c := ipoCall(t, srv, "GET", "/v1/m/day?date=2026-10-16", "meera.iyer", nil, &day); c != 200 || !day.Scoped || len(day.Steps) != 0 {
		t.Fatalf("branch day: %d %+v", c, day)
	}
	if c := ipoCall(t, srv, "GET", "/v1/m/day?date=2026-10-16", "farhan.khan", nil, &day); c != 200 || day.Scoped || len(day.Steps) < 12 || day.Done == 0 {
		t.Fatalf("head office day: %d %+v", c, day)
	}
	var al struct{ Alerts []ops.MobileAlert }
	if c := ipoCall(t, srv, "GET", "/v1/m/alerts?date=2026-10-16", "farhan.khan", nil, &al); c != 200 {
		t.Fatalf("alerts: %d", c)
	}
	// The client look-up: only the person's clients, PAN masked.
	var cl struct{ Clients []ops.MobileClient }
	var raw []byte
	if c := ipoCall(t, srv, "GET", "/v1/m/clients?date=2026-10-16&q="+out.UCC, "meera.iyer", nil, &cl); c != 200 || len(cl.Clients) != 0 {
		t.Fatalf("branch user found another branch's client: %d %+v", c, cl)
	}
	if c := ipoCall(t, srv, "GET", "/v1/m/clients?date=2026-10-16&q="+in.PAN, "meera.iyer", nil, &raw); c != 200 || !strings.Contains(string(raw), in.UCC) || strings.Contains(string(raw), in.PAN) || !strings.Contains(string(raw), "XXXX") {
		t.Fatalf("look-up by PAN: %d %s", c, raw)
	}
	// Not on the list, or a client login: no staff app.
	if c := ipoCall(t, srv, "GET", "/v1/m/home", "anita.rao", nil, nil); c != 403 {
		t.Fatalf("risk manager not on the list: %d", c)
	}
	id, _, err := o.PortalAccess(ctx, "demo", "farhan.khan", in.UCC, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if c := ipoCall(t, srv, "GET", "/v1/m/approvals", id, nil, nil); c != 403 {
		t.Fatalf("client login opened the staff app: %d", c)
	}
	var home ops.MobileHome
	if c := ipoCall(t, srv, "GET", "/v1/m/home?date=2026-10-16", "sunrise.ap", nil, &home); c != 200 || !home.Scoped {
		t.Fatalf("partner home: %d %+v", c, home)
	}
}

// TestMobileDeviceHTTP: sign in with the password, register the phone,
// unlock with the device secret, revoke.
func TestMobileDeviceHTTP(t *testing.T) {
	srv, o, ctx := mobileServer(t, false)
	if _, err := o.SaveMobileSettings(ctx, "demo", "admin", ops.MobileSettings{StaffOn: true, StaffRoles: []string{"Ops checker"}}); err != nil {
		t.Fatal(err)
	}
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	do := func(method, path string, body any, csrf bool, out any) int {
		t.Helper()
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, srv.URL+path, rd)
		req.Header.Set("Content-Type", "application/json")
		if csrf {
			req.Header.Set("X-Requested-With", "ledgerline")
		}
		res, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if out != nil {
			json.NewDecoder(res.Body).Decode(out)
		}
		return res.StatusCode
	}
	var cfg map[string]any
	if st := do("GET", "/v1/mobile/config?app=staff", nil, false, &cfg); st != 200 || cfg["on"] != true || cfg["tenant"] != "demo" {
		t.Fatalf("config %d %v", st, cfg)
	}
	if st := do("GET", "/v1/mobile/config?app=client&firm=nope", nil, false, nil); st != 404 {
		t.Fatalf("unknown firm: %d", st)
	}
	if st := do("POST", "/v1/auth/login", map[string]string{"user": "farhan.khan", "password": ops.DevPassword}, false, nil); st != 200 {
		t.Fatalf("login %d", st)
	}
	dev := map[string]any{"app": "staff", "platform": "android", "name": "Pixel 8", "push_token": "fcm-token-made-up-00000000000000000000099"}
	if st := do("POST", "/v1/mobile/devices", dev, false, nil); st != 403 {
		t.Fatalf("registered without the CSRF header: %d", st)
	}
	var reg struct {
		Device ops.DeviceView `json:"device"`
		Secret string         `json:"secret"`
	}
	if st := do("POST", "/v1/mobile/devices", dev, true, &reg); st != 201 || reg.Secret == "" || reg.Device.ID == "" {
		t.Fatalf("register %d %+v", st, reg)
	}
	// An integration's API key cannot register phones.
	if st := ipoCall(t, srv, "POST", "/v1/mobile/devices", "farhan.khan", dev, nil); st != 403 {
		t.Fatalf("API key registered a phone: %d", st)
	}
	do("POST", "/v1/auth/logout", nil, true, nil)
	if st := do("GET", "/v1/m/home", nil, false, nil); st != 401 {
		t.Fatalf("after sign-out: %d", st)
	}
	var e map[string]map[string]string
	if st := do("POST", "/v1/mobile/unlock", map[string]string{"device": reg.Device.ID, "secret": "wrong"}, false, &e); st != 401 || e["error"]["code"] != "device_revoked" {
		t.Fatalf("wrong secret: %d %v", st, e)
	}
	if st := do("POST", "/v1/mobile/unlock", map[string]string{"device": reg.Device.ID, "secret": reg.Secret}, false, nil); st != 200 {
		t.Fatalf("unlock %d", st)
	}
	var home ops.MobileHome
	if st := do("GET", "/v1/m/home", nil, false, &home); st != 200 || home.Device != reg.Device.ID || home.User != "farhan.khan" {
		t.Fatalf("home %d %+v", st, home)
	}
	var ds struct {
		Devices []ops.DeviceView `json:"devices"`
		This    string           `json:"this"`
	}
	if st := do("GET", "/v1/mobile/devices", nil, false, &ds); st != 200 || ds.This != reg.Device.ID || len(ds.Devices) != 1 || !ds.Devices[0].Push {
		t.Fatalf("devices %d %+v", st, ds)
	}
	if st := do("PUT", "/v1/mobile/devices/"+reg.Device.ID+"/push", map[string]string{"token": "fcm-token-made-up-00000000000000000000100"}, true, nil); st != 200 {
		t.Fatalf("push token %d", st)
	}
	if st := do("POST", "/v1/mobile/devices/"+reg.Device.ID+"/revoke", map[string]string{}, true, nil); st != 200 {
		t.Fatalf("revoke %d", st)
	}
	if st := do("GET", "/v1/m/home", nil, false, nil); st != 401 {
		t.Fatalf("session of a revoked phone: %d", st)
	}
	// The set-up QR: Admin only, the server and firm, no secret.
	var qr map[string]any
	if st := ipoCall(t, srv, "GET", "/v1/mobile/staff-qr", "admin", nil, &qr); st != 200 || !strings.HasPrefix(qr["link"].(string), StaffAppScheme+"://setup?server=") || !strings.HasPrefix(qr["qr"].(string), "data:image/png;base64,") {
		t.Fatalf("qr %d %v", st, qr)
	}
	if st := ipoCall(t, srv, "GET", "/v1/mobile/staff-qr", "farhan.khan", nil, nil); st != 403 {
		t.Fatalf("checker saw the set-up code: %d", st)
	}
}
