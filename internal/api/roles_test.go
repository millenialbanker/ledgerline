package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"ledgerline/internal/app"
	"ledgerline/internal/charges"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ops"
	"ledgerline/internal/screens"
	"ledgerline/internal/vault"
)

// TestEveryRouteHasScreens: each route is linked to the screens it serves
// (or is open to all staff), and every screen named exists, so a role the
// firm makes is never stopped by a route nobody mapped.
func TestEveryRouteHasScreens(t *testing.T) {
	reg, _ := ingest.LoadDir("../../config/adapters")
	f, _ := os.Open("../../config/charges/sample-schedule.json")
	defer f.Close()
	sched, _ := charges.LoadSchedule(f)
	a := app.New(vault.Protect(app.MemoryStores(), vault.New(vault.Ephemeral())), reg, sched)
	s := New(a, ops.New(a), map[string]string{key: "demo"}, slog.New(slog.NewTextHandler(io.Discard, nil)), false)
	if len(s.patterns) < 900 {
		t.Fatalf("only %d routes", len(s.patterns))
	}
	for _, p := range s.patterns {
		areas, common := screensFor(p)
		if !common && len(areas) == 0 {
			t.Errorf("%s: not linked to a screen (add it to routeScreens)", p)
		}
		for _, k := range areas {
			if _, ok := screens.Get(k); !ok {
				t.Errorf("%s: unknown screen %q", p, k)
			}
		}
	}
	for k, v := range routeScreens {
		for _, x := range strings.Fields(v) {
			if _, ok := screens.Get(x); !ok {
				t.Errorf("routeScreens[%q]: unknown screen %q", k, x)
			}
		}
	}
	if a, _ := screensFor("POST /v1/clients/{ucc}/risk"); strings.Join(a, " ") != "kyc clients" {
		t.Fatalf("longest key: %v", a)
	}
	if a, _ := screensFor("GET /v1/clients/{ucc}/documents/{id}"); strings.Join(a, " ") != "clients statements" {
		t.Fatalf("parent key: %v", a)
	}
	if _, c := screensFor("GET /v1/me/ui"); !c {
		t.Fatal("me is common")
	}
}

// rolesServer is a firm with the sample users, a second Admin, and two
// roles of its own: "CRM desk" (leads only) and "Payout approver"
// (approves payouts up to ₹5,000, cannot enter them).
type rolesHarness struct {
	t   *testing.T
	o   *ops.Ops
	srv *httptest.Server
}

func newRolesHarness(t *testing.T) *rolesHarness {
	t.Helper()
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
	for _, u := range []ops.User{{ID: "admin2", Name: "Second Admin", Role: ops.RoleAdmin, Active: true},
		{ID: "lata.crm", Name: "Lata", Role: ops.RoleMaker, Active: true}, {ID: "omar.pay", Name: "Omar", Role: ops.RoleChecker, Active: true}} {
		if err := o.PutUser(ctx, "demo", "admin", u); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(New(a, o, map[string]string{key: "demo"}, slog.New(slog.NewTextHandler(io.Discard, nil)), false))
	t.Cleanup(srv.Close)
	return &rolesHarness{t, o, srv}
}

func (h *rolesHarness) call(method, path, who, body string, hdr ...string) (int, map[string]any) {
	h.t.Helper()
	req, _ := http.NewRequest(method, h.srv.URL+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("X-Actor", who)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	var v map[string]any
	_ = json.Unmarshal(b, &v)
	if v == nil {
		v = map[string]any{"raw": string(b)}
	}
	return res.StatusCode, v
}

// approve proposes with admin and approves with admin2.
func (h *rolesHarness) approve(path, body string) {
	h.t.Helper()
	c, v := h.call("POST", path, "admin", body)
	if c != 200 {
		h.t.Fatalf("propose %s: %d %v", path, c, v)
	}
	id, _ := v["id"].(string)
	if c, v := h.call("POST", "/v1/role-changes/"+id+"/decide", "admin2", `{"approve":true}`); c != 200 || v["status"] != "Approved" {
		h.t.Fatalf("approve %s: %d %v", id, c, v)
	}
}

func TestCustomRolesOverAPI(t *testing.T) {
	h := newRolesHarness(t)
	// A role change needs a second Admin.
	c, v := h.call("POST", "/v1/roles", "admin", `{"role":{"name":"CRM desk","about":"Leads only","screens":{"crm":{"do":true},"dashboard":{"view":true}},"limits":{"scope":"firm"}}}`)
	if c != 200 || v["status"] != "Pending" {
		t.Fatalf("propose: %d %v", c, v)
	}
	id := v["id"].(string)
	if c, _ := h.call("POST", "/v1/role-changes/"+id+"/decide", "admin", `{"approve":true}`); c != 403 {
		t.Fatalf("own approval: %d", c)
	}
	if c, _ := h.call("POST", "/v1/role-changes/"+id+"/decide", "priya.nair", `{"approve":true}`); c != 403 {
		t.Fatalf("a maker approves a role change: %d", c)
	}
	if c, _ := h.call("POST", "/v1/role-changes/"+id+"/decide", "admin2", `{"approve":true}`); c != 200 {
		t.Fatalf("second admin: %d", c)
	}
	// Giving it to someone is a proposal too; until approved, nothing changes.
	c, v = h.call("POST", "/v1/users/lata.crm/roles", "admin", `{"roles":["CRM desk"]}`)
	if c != 200 {
		t.Fatalf("assign: %d %v", c, v)
	}
	if c, _ := h.call("GET", "/v1/receipts?date=2026-10-16", "lata.crm", ""); c != 200 {
		t.Fatalf("before approval lata is still an Ops maker: %d", c)
	}
	if c, _ := h.call("POST", "/v1/role-changes/"+v["id"].(string)+"/decide", "admin2", `{"approve":true}`); c != 200 {
		t.Fatalf("approve assignment: %d", c)
	}
	// Now only CRM and the dashboard: everything else is 403, reads too.
	for _, p := range []string{"/v1/receipts?date=2026-10-16", "/v1/brokerage", "/v1/clients?date=2026-10-16", "/v1/settings"} {
		if c, v := h.call("GET", p, "lata.crm", ""); c != 403 {
			t.Errorf("GET %s: %d %v", p, c, v)
		}
	}
	if c, v := h.call("GET", "/v1/crm?date=2026-10-16", "lata.crm", ""); c != 200 {
		t.Fatalf("crm: %d %v", c, v)
	}
	if c, v := h.call("POST", "/v1/leads", "lata.crm", `{"name":"Test Lead","mobile":"9800000001","source":"Walk-in"}`); c != 200 && c != 201 {
		t.Fatalf("add lead: %d %v", c, v)
	}
	if c, _ := h.call("POST", "/v1/receipts", "lata.crm", `{}`); c != 403 {
		t.Fatalf("receipt: %d", c)
	}
	// /v1/me tells the console which screens to show.
	_, me := h.call("GET", "/v1/me", "lata.crm", "")
	acc, _ := me["access"].(map[string]any)
	if me["role_name"] != "CRM desk" || acc == nil || acc["custom"] != true {
		t.Fatalf("me: %v", me)
	}
	ss := acc["screens"].(map[string]any)
	if ss["crm"] == nil || ss["money"] != nil {
		t.Fatalf("screens: %v", ss)
	}
	// The built-in roles are untouched: Priya still works as an Ops maker.
	if c, _ := h.call("GET", "/v1/brokerage?date=2026-10-16", "priya.nair", ""); c != 200 {
		t.Fatalf("maker reads brokerage: %d", c)
	}
	if _, me := h.call("GET", "/v1/me", "priya.nair", ""); me["access"] != nil {
		t.Fatalf("an unedited built-in role is not gated: %v", me["access"])
	}
}

func TestRolePreviewIsReadOnly(t *testing.T) {
	h := newRolesHarness(t)
	if c, v := h.call("POST", "/v1/roles/preview", "admin", `{"role":{"name":"Draft","screens":{"crm":{"do":true}},"limits":{"scope":"firm"}}}`); c != 200 {
		t.Fatalf("start: %d %v", c, v)
	}
	if c, _ := h.call("POST", "/v1/roles/preview", "priya.nair", `{"role":{"name":"Ops checker"}}`); c != 403 {
		t.Fatalf("a maker previews: %d", c)
	}
	pv := []string{"X-Preview", "1"}
	if c, _ := h.call("GET", "/v1/crm?date=2026-10-16", "admin", "", pv...); c != 200 {
		t.Fatalf("preview reads crm: %d", c)
	}
	if c, _ := h.call("GET", "/v1/settings", "admin", "", pv...); c != 403 {
		t.Fatalf("preview reads settings: %d", c)
	}
	if c, v := h.call("POST", "/v1/leads", "admin", `{"name":"X","mobile":"9800000002","source":"Walk-in"}`, pv...); c != 403 || !strings.Contains(v["error"].(map[string]any)["code"].(string), "preview") {
		t.Fatalf("preview writes: %d %v", c, v)
	}
	if _, me := h.call("GET", "/v1/me", "admin", "", pv...); me["preview"] != "Draft" || me["role"] == ops.RoleAdmin {
		t.Fatalf("me in preview: %v %v", me["preview"], me["role"])
	}
	// Audited.
	if c, _ := h.call("POST", "/v1/roles/preview/end", "admin", `{}`); c != 200 {
		t.Fatal("end")
	}
	if c, _ := h.call("GET", "/v1/crm?date=2026-10-16", "admin", "", pv...); c != 409 {
		t.Fatalf("after the end: %d", c)
	}
	ev, _ := h.o.App.Audit.List(context.Background(), "demo", 50)
	var seen []string
	for _, e := range ev {
		if strings.HasPrefix(e.Action, "role.preview") {
			seen = append(seen, e.Action)
		}
	}
	if len(seen) != 2 {
		t.Fatalf("preview audit: %v", seen)
	}
}

// A role without "may download" gets no files; without "may see brokerage
// totals" no brokerage fields.
func TestRoleLimitsOverAPI(t *testing.T) {
	h := newRolesHarness(t)
	if _, err := h.o.SavePartner(context.Background(), "demo", "admin", ops.Partner{Code: "AP009", Name: "Made-up AP", Kind: "Authorised person", Active: true}); err != nil {
		t.Fatal(err)
	}
	h.approve("/v1/roles", `{"role":{"name":"Dealer desk","screens":{"dealers":{"do":true},"partners":{"view":true}},"limits":{"scope":"firm","brokerage":false,"export":false}}}`)
	h.approve("/v1/users/lata.crm/roles", `{"roles":["Dealer desk"]}`)
	if c, v := h.call("GET", "/v1/dealers/export.csv", "lata.crm", ""); c != 403 || !strings.Contains(v["error"].(map[string]any)["message"].(string), "download") {
		t.Fatalf("export: %d %v", c, v)
	}
	if c, _ := h.call("GET", "/v1/dealers/export.csv", "vikram.shah", ""); c != 200 {
		t.Fatalf("compliance still exports: %d", c)
	}
	c, v := h.call("GET", "/v1/partners?date=2026-10-16", "lata.crm", "")
	if c != 200 {
		t.Fatalf("partners: %d %v", c, v)
	}
	b, _ := json.Marshal(v)
	if strings.Contains(string(b), `brokerage`) {
		t.Fatalf("brokerage shown: %s", b[:min(len(b), 400)])
	}
	if _, v := h.call("GET", "/v1/partners?date=2026-10-16", "admin", ""); !strings.Contains(mustJSON(v), `brokerage`) {
		t.Fatal("admin sees brokerage")
	}
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
