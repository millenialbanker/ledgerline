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
	"time"

	"ledgerline/internal/app"
	"ledgerline/internal/charges"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ops"
	"ledgerline/internal/vault"
)

func TestMCXRoutes(t *testing.T) {
	reg, _ := ingest.LoadDir("../../config/adapters")
	f, _ := os.Open("../../config/charges/sample-schedule.json")
	defer f.Close()
	sched, _ := charges.LoadSchedule(f)
	a := app.New(vault.Protect(app.MemoryStores(), vault.New(vault.Ephemeral())), reg, sched)
	o := ops.New(a)
	o.Now = func() time.Time { return time.Date(2026, 10, 16, 9, 0, 0, 0, time.FixedZone("IST", 19800)) }
	ctx := context.Background()
	if err := o.SeedDev(ctx, "demo"); err != nil {
		t.Fatal(err)
	}
	// A two-contract product master (made up), as MCX sends it.
	line := func(uid, under, sym string) string {
		f := make([]string, 68)
		f[0], f[2], f[3], f[4], f[9], f[12], f[18], f[44], f[67] = "FUTCOM", uid, under, sym, "19NOV2026", "1", "100", "N", "-1"
		return strings.Join(f, ",")
	}
	pm := line("1", "0", "CRUDEOIL") + "\n" + line("2", "1", "CRUDEOIL") + "\n" + line("3", "1", "GOLDM") + "\n"
	if res, err := o.LoadExchangeFile(ctx, "demo", "priya.nair", "MCX_ProductMaster.csv", []byte(pm), true); err != nil || res[0].Added != 2 {
		t.Fatalf("%+v %v", res, err)
	}
	s := New(a, o, map[string]string{key: "demo"}, slog.New(slog.NewTextHandler(io.Discard, nil)), false)
	s.mcxRoutes()
	s.mcxRoutes() // registering twice is harmless
	srv := httptest.NewServer(s)
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL+"/v1/mcx/contracts?symbol=crudeoil", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("X-Actor", "priya.nair")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m ops.MCXContractMaster
	if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&m) != nil || len(m.Contracts) != 1 || m.Contracts[0].Lot != 100 || m.Contracts[0].Expiry != "2026-11-19" {
		t.Fatalf("%d %+v", resp.StatusCode, m)
	}
}
