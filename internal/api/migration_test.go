package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	"ledgerline/internal/migrate/pkg"
	"ledgerline/internal/ops"
	"ledgerline/internal/vault"
)

// Made-up firm and clients only.
func migTestPackage(t *testing.T, pass string) []byte {
	t.Helper()
	pkg.SetTestCost()
	var buf bytes.Buffer
	w, err := pkg.Create(&buf, pass, "test")
	if err != nil {
		t.Fatal(err)
	}
	w.Manifest().Source = pkg.Source{System: "SharePro"}
	w.Manifest().Firms = []pkg.Firm{{Code: "01", Name: "Test Broking", AsOf: "2026-10-30"}}
	cols := []string{"UCC", "Name", "Type", "PAN", "DOB", "Mobile", "Email", "City", "Segments", "Risk", "Income", "RAS", "Bank", "IFSC", "Account", "Depository", "DematID", "Nominee", "NomineeName", "KYCDate", "Status", "API", "LastTrade", "UPIBlock"}
	c, _ := w.Master("01", "CLIENT-MASTER", cols)
	for i, u := range []string{"TA001", "TB002"} {
		c.Write([]string{u, "Test Client " + u, "Individual", fmt.Sprintf("ABCP%c1234A", 'A'+i), "1980-01-01", fmt.Sprintf("987650000%d", i), strings.ToLower(u) + "@example.com", "Pune", "CM", "Low", "", "Quarterly", "Test Bank", "TEST0123456", "123456789012", "CDSL", "", "", "", "", "Active", "N", "", ""})
	}
	c.Close()
	ob, _ := w.Master("01", "OPENING-BALANCES", []string{"Account", "Debit", "Credit"})
	ob.Write([]string{"CLIENT:TA001", "100.00", ""})
	ob.Write([]string{"CLIENT:TB002", "", "100.00"})
	ob.Close()
	h, _ := w.History("01", "2025-26", pkg.HistoryLedger, pkg.HistoryColumns[pkg.HistoryLedger])
	h.Write([]string{"2025-05-02", "BILL", "B1", "CLIENT:TA001", "", "TA001", "CM", "Bill", "50.00", "", "", "", ""})
	h.Write([]string{"2025-05-02", "BILL", "B2", "CLIENT:TB002", "", "TB002", "CM", "Bill", "70.00", "", "", "", ""})
	h.Close()
	for _, u := range []string{"TA001", "TB002"} {
		w.AddDocument("01", pkg.DocMeta{ID: "CN-" + u, UCC: u, Type: pkg.DocContractNote, Date: time.Date(2025, 5, 2, 0, 0, 0, 0, time.UTC), FY: "2025-26", Signed: true, Format: "pdf"}, []byte("%PDF made-up "+u))
	}
	w.AddCheck(pkg.Check{Firm: "01", Name: pkg.CheckClients, Value: "2"})
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestMigrationAPI(t *testing.T) {
	reg, _ := ingest.LoadDir("../../config/adapters")
	f, _ := os.Open("../../config/charges/sample-schedule.json")
	defer f.Close()
	sched, _ := charges.LoadSchedule(f)
	a := app.New(vault.Protect(app.MemoryStores(), vault.New(vault.Ephemeral())), reg, sched)
	o := ops.New(a)
	o.WorkDir, o.MigrateSync = t.TempDir(), true
	ctx := context.Background()
	for _, u := range []ops.User{{ID: "adm.one", Name: "A", Role: ops.RoleAdmin, Active: true}, {ID: "chk.one", Name: "C", Role: ops.RoleChecker, Active: true},
		{ID: "client.a", Name: "CA", Role: ops.RoleClient, Active: true, Clients: []string{"TA001"}}} {
		o.PutUser(ctx, "mig", "adm.one", u)
	}
	s := New(a, o, map[string]string{key: "mig"}, slog.New(slog.NewTextHandler(io.Discard, nil)), false)
	srv := httptest.NewServer(s)
	defer srv.Close()
	call := func(method, path, who string, body []byte, out any) (int, []byte) {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("X-Actor", who)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if out != nil {
			json.Unmarshal(b, out)
		}
		return resp.StatusCode, b
	}
	const pass = "made-up passphrase 42"
	data := migTestPackage(t, pass)
	var up struct {
		ID     string `json:"id"`
		Chunks int    `json:"chunks"`
	}
	if c, b := call("POST", "/v1/migration/uploads", "chk.one", []byte(fmt.Sprintf(`{"name":"office.llmig","size":%d,"chunk_size":1024}`, len(data))), nil); c != 403 {
		t.Fatalf("checker uploaded: %d %s", c, b)
	}
	if c, b := call("POST", "/v1/migration/uploads", "adm.one", []byte(fmt.Sprintf(`{"name":"office.llmig","size":%d,"chunk_size":1024}`, len(data))), &up); c != 201 {
		t.Fatalf("create: %d %s", c, b)
	}
	// All chunks but the first, then ask what is missing, then the first.
	for n := 1; n < up.Chunks; n++ {
		if c, b := call("PUT", fmt.Sprintf("/v1/migration/uploads/%s/chunks/%d", up.ID, n), "adm.one", data[n*1024:min((n+1)*1024, len(data))], nil); c != 200 {
			t.Fatalf("chunk %d: %d %s", n, c, b)
		}
	}
	var det struct {
		Missing []int `json:"missing"`
	}
	call("GET", "/v1/migration/packages/"+up.ID, "adm.one", nil, &det)
	if len(det.Missing) != 1 || det.Missing[0] != 0 {
		t.Fatalf("missing %v", det.Missing)
	}
	if c, _ := call("POST", "/v1/migration/uploads/"+up.ID+"/complete", "adm.one", nil, nil); c != 409 && c != 422 {
		t.Fatalf("completed with a chunk missing: %d", c)
	}
	call("PUT", fmt.Sprintf("/v1/migration/uploads/%s/chunks/0", up.ID), "adm.one", data[:1024], nil)
	if c, b := call("POST", "/v1/migration/uploads/"+up.ID+"/complete", "adm.one", nil, nil); c != 200 {
		t.Fatalf("complete: %d %s", c, b)
	}
	if c, b := call("POST", "/v1/migration/packages/"+up.ID+"/check", "adm.one", []byte(`{"passphrase":"wrong one entirely"}`), nil); c != 422 || !strings.Contains(string(b), "passphrase is wrong") {
		t.Fatalf("wrong passphrase: %d %s", c, b)
	}
	if c, b := call("POST", "/v1/migration/packages/"+up.ID+"/check", "adm.one", []byte(`{"passphrase":"`+pass+`","firm":"01"}`), nil); c != 202 {
		t.Fatalf("check: %d %s", c, b)
	}
	var m ops.Migration
	if c, b := call("POST", "/v1/migration/proposals", "adm.one", []byte(`{"package":"`+up.ID+`","firm":"01"}`), &m); c != 201 {
		t.Fatalf("propose: %d %s", c, b)
	}
	if c, _ := call("POST", "/v1/migration/runs/"+m.ID+"/decide", "adm.one", []byte(`{"approve":true,"passphrase":"`+pass+`"}`), nil); c != 403 {
		t.Fatalf("maker approved: %d", c)
	}
	if c, b := call("POST", "/v1/migration/runs/"+m.ID+"/decide", "chk.one", []byte(`{"approve":true,"passphrase":"`+pass+`"}`), &m); c != 200 || m.Status != ops.MigLoaded {
		t.Fatalf("approve: %d %s", c, b)
	}
	if c, b := call("GET", "/v1/migration/runs/"+m.ID+"/reconcile.csv", "chk.one", nil, nil); c != 200 || !strings.Contains(string(b), "Clients") {
		t.Fatalf("recon csv %d %s", c, b)
	}
	if c, b := call("GET", "/v1/migration/runs/"+m.ID+"/reconcile.pdf", "chk.one", nil, nil); c != 200 || !bytes.HasPrefix(b, []byte("%PDF")) {
		t.Fatalf("recon pdf %d", c)
	}
	// Staff: history and archive.
	if c, b := call("GET", "/v1/history/TA001/2025-26/statement", "chk.one", nil, nil); c != 200 || !strings.Contains(string(b), "Migrated from SharePro") {
		t.Fatalf("statement %d %s", c, b)
	}
	if c, b := call("GET", "/v1/history-files/2025-26/LEDGER/csv", "chk.one", nil, nil); c != 200 || !strings.Contains(string(b), "TB002") {
		t.Fatalf("export %d %s", c, b)
	}
	if c, _ := call("GET", "/v1/archive/TB002/CN-TB002", "chk.one", nil, nil); c != 200 {
		t.Fatalf("staff download %d", c)
	}
	// Portal: the client's own only; staff routes closed.
	if c, b := call("GET", "/v1/portal/history/TA001/2025-26/statement", "client.a", nil, nil); c != 200 || strings.Contains(string(b), "TB002") {
		t.Fatalf("portal statement %d %s", c, b)
	}
	if c, _ := call("GET", "/v1/portal/history/TB002/2025-26/statement", "client.a", nil, nil); c != 404 {
		t.Fatalf("portal other client: %d", c)
	}
	if c, b := call("GET", "/v1/portal/archive/TA001/CN-TA001", "client.a", nil, nil); c != 200 || !strings.Contains(string(b), "TA001") {
		t.Fatalf("portal own doc %d", c)
	}
	if c, _ := call("GET", "/v1/portal/archive/TB002/CN-TB002", "client.a", nil, nil); c != 404 {
		t.Fatalf("portal other doc: %d", c)
	}
	for _, p := range []string{"/v1/archive/TB002/CN-TB002", "/v1/archive?ucc=TB002", "/v1/history/TB002", "/v1/migration"} {
		if c, _ := call("GET", p, "client.a", nil, nil); c != 403 {
			t.Fatalf("client reached %s: %d", p, c)
		}
	}
}
