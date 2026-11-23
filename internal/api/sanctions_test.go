package api

import (
	"testing"
	"time"

	"ledgerline/internal/ops"
)

// TestSanctionsRoutes: the sanctions screen is for Compliance; a list loads
// with its version, all clients are screened, settings and a FATF
// statement are saved. Names and references are made up.
func TestSanctionsRoutes(t *testing.T) {
	now := time.Date(2026, 11, 17, 11, 0, 0, 0, time.UTC)
	srv, _ := ipoServer(t, now)
	if c := ipoCall(t, srv, "GET", "/v1/sanctions", "priya.nair", nil, nil); c != 403 {
		t.Fatalf("an ops maker saw sanctions cases: %d", c)
	}
	var l ops.SanctionsLoad
	csv := []byte("Name,Aliases,DOB,Ref\nTarvesh Okonmar,T Okonmaar,1980,UAPA-IV-901\n")
	if c := ipoCall(t, srv, "POST", "/v1/sanctions/lists?list=UAPA&version=2026-11-10&name=uapa.csv", "vikram.shah", csv, &l); c != 200 || l.Version.Version != "2026-11-10" || l.Version.Entries != 1 || !l.Run.All {
		t.Fatalf("load %d %+v", c, l)
	}
	if c := ipoCall(t, srv, "POST", "/v1/sanctions/lists?list=Nowhere", "vikram.shah", csv, nil); c != 422 {
		t.Fatalf("unknown list: %d", c)
	}
	var run ops.SanctionsRun
	if c := ipoCall(t, srv, "POST", "/v1/sanctions/screen", "vikram.shah", map[string]any{}, &run); c != 200 || run.Clients == 0 {
		t.Fatalf("screen %d %+v", c, run)
	}
	var v ops.SanctionsView
	if c := ipoCall(t, srv, "GET", "/v1/sanctions", "vikram.shah", nil, &v); c != 200 || len(v.Lists) == 0 || v.LastRun == nil || v.Config.ReviewAt == 0 {
		t.Fatalf("view %d %+v", c, v)
	}
	cfg := v.Config
	cfg.EveryDays = 3
	if c := ipoCall(t, srv, "PUT", "/v1/sanctions/config", "vikram.shah", cfg, &cfg); c != 200 || cfg.EveryDays != 3 {
		t.Fatalf("config %d %+v", c, cfg)
	}
	var st ops.FATFStatement
	if c := ipoCall(t, srv, "POST", "/v1/sanctions/fatf", "vikram.shah", map[string]any{"plenary": "October 2026", "date": "2026-10-24", "call_for_action": []string{"KP"}}, &st); c != 200 || st.ID == "" {
		t.Fatalf("fatf %d %+v", c, st)
	}
	if c := ipoCall(t, srv, "POST", "/v1/sanctions/cases/SC-99999", "vikram.shah", map[string]any{"action": "confirm", "note": "x"}, nil); c != 404 {
		t.Fatalf("unknown case: %d", c)
	}
}
