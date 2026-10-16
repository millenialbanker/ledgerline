//go:build integration && pgx

// Integration test: runs the full sample day against a real PostgreSQL with
// the migration applied. Needs the pgx driver:
//
//	go get github.com/jackc/pgx/v5
//	LEDGERLINE_TEST_DB=postgres://ll_test_app@localhost:5432/ledgerline?sslmode=disable \
//	  go test -tags 'integration pgx' ./internal/pgstore
//
// The connecting role must be granted ledgerline_app (not a superuser, or
// row-level security is bypassed) and the tenant row must exist.
package pgstore

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"ledgerline/internal/app"
	"ledgerline/internal/charges"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

func TestIntegrationSampleDay(t *testing.T) {
	dsn := os.Getenv("LEDGERLINE_TEST_DB")
	tenant := os.Getenv("LEDGERLINE_TEST_TENANT")
	if dsn == "" || tenant == "" {
		t.Skip("set LEDGERLINE_TEST_DB and LEDGERLINE_TEST_TENANT")
	}
	ctx := context.Background()
	st, err := Open(ctx, "pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer st.DB.Close()
	reg, err := ingest.LoadDir("../../config/adapters")
	if err != nil {
		t.Fatal(err)
	}
	f, _ := os.Open("../../config/charges/sample-schedule.json")
	sched, err := charges.LoadSchedule(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	a := app.New(st.Stores(), reg, sched)
	// A unique date per run keeps reruns independent on an append-only ledger.
	date := "2026-10-16"
	for _, x := range [][2]string{{"NSE-CM-TRADE", "nse_cm_trades.csv"}, {"NSE-FO-TRADE", "nse_fo_trades.csv"}, {"MCX-TRADE", "mcx_trades.txt"}, {"OMS-TRADE", "oms_trades.csv"}} {
		b, _ := os.ReadFile("../../testdata/sample/" + x[1])
		if _, err := a.IngestFile(ctx, tenant, "it", x[0], "", x[1], b); err != nil {
			t.Fatalf("%s: %v", x[1], err)
		}
	}
	sum, err := a.RunRecon(ctx, tenant, "it", date)
	if err != nil || sum.Result.Matched != 11 || len(sum.Result.Breaks) != 7 {
		t.Fatalf("recon %+v %v", sum.Result, err)
	}
	pb, _ := os.ReadFile("../../testdata/sample/settlement_prices.json")
	var prices map[string]money.Dec
	json.Unmarshal(pb, &prices)
	br, err := a.RunBilling(ctx, tenant, "it", date, prices)
	if err != nil || len(br.Conflicts) != 0 {
		t.Fatalf("billing %+v %v", br, err)
	}
	tb, err := a.Ledger.TrialBalance(ctx, tenant, date)
	if err != nil || !tb.Balanced {
		t.Fatalf("trial balance %+v %v", tb, err)
	}
	bal, _ := a.Ledger.Balances(ctx, tenant, date)
	if got := bal[ledger.ClientAccount("BH1001")]; got != money.MustAmount("17863.72") {
		t.Fatalf("BH1001 %s", got)
	}
	// Idempotent rerun.
	again, err := a.RunBilling(ctx, tenant, "it", date, prices)
	if err != nil || len(again.Posted) != 0 {
		t.Fatalf("rerun posted %v %v", again.Posted, err)
	}
	// A manual entry with a unique key round-trips.
	key := fmt.Sprintf("IT:%d", time.Now().UnixNano())
	e, created, err := a.PostManual(ctx, ledger.Entry{Tenant: tenant, Key: key, ValueDate: date, Narration: "integration receipt", CreatedBy: "it",
		Postings: []ledger.Posting{{Account: ledger.BankClient, Debit: 100}, {Account: ledger.ClientAccount("BH1001"), Credit: 100}}})
	if err != nil || !created {
		t.Fatal(err)
	}
	got, err := st.Get(ctx, tenant, e.ID)
	if err != nil || len(got.Postings) != 2 || got.Fingerprint() != e.Fingerprint() {
		t.Fatalf("reload %+v %v", got, err)
	}
}
