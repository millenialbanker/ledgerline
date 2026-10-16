package app

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"ledgerline/internal/charges"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
	"ledgerline/internal/recon"
)

// newSampleApp loads the repo's adapters and sample schedule.
func newSampleApp(t *testing.T) *App {
	t.Helper()
	reg, err := ingest.LoadDir("../../config/adapters")
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open("../../config/charges/sample-schedule.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sched, err := charges.LoadSchedule(f)
	if err != nil {
		t.Fatal(err)
	}
	return New(MemoryStores(), reg, sched)
}

func load(t *testing.T, a *App, source, file string) ingest.Result {
	t.Helper()
	b, err := os.ReadFile("../../testdata/sample/" + file)
	if err != nil {
		t.Fatal(err)
	}
	r, err := a.IngestFile(context.Background(), "bhairav", "ops.priya", source, "", file, b)
	if err != nil {
		t.Fatalf("%s: %v (%v)", file, err, r.Errors)
	}
	return r
}

func prices(t *testing.T) map[string]money.Dec {
	b, err := os.ReadFile("../../testdata/sample/settlement_prices.json")
	if err != nil {
		t.Fatal(err)
	}
	var p map[string]money.Dec
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestEndToEnd runs a full sample day: four files in, reconciliation,
// break resolution, billing, and checks on the resulting ledger.
func TestEndToEnd(t *testing.T) {
	ctx := context.Background()
	a := newSampleApp(t)
	const tenant, date = "bhairav", "2026-10-16"

	if r := load(t, a, "NSE-CM-TRADE", "nse_cm_trades.csv"); r.Added != 10 {
		t.Fatalf("CM added %d", r.Added)
	}
	load(t, a, "NSE-FO-TRADE", "nse_fo_trades.csv")
	load(t, a, "MCX-TRADE", "mcx_trades.txt")
	load(t, a, "OMS-TRADE", "oms_trades.csv")

	sum, err := a.RunRecon(ctx, tenant, "ops.priya", date)
	if err != nil {
		t.Fatal(err)
	}
	r := sum.Result
	if r.ExchangeCount != 17 || r.OMSCount != 17 || r.Matched != 11 {
		t.Fatalf("recon counts: exch %d oms %d matched %d", r.ExchangeCount, r.OMSCount, r.Matched)
	}
	want := map[recon.BreakType]int{recon.MissingInOMS: 1, recon.MissingInExchange: 1, recon.TradeNoMismatch: 1, recon.UCCMismatch: 1, recon.QtyMismatch: 1, recon.PriceMismatch: 1, recon.AlgoIDMissing: 1}
	for k, v := range want {
		if r.ByType[k] != v {
			t.Errorf("%s: got %d want %d", k, r.ByType[k], v)
		}
	}
	if sum.Open != 7 {
		t.Fatalf("open breaks %d", sum.Open)
	}

	// Resolve one break; a re-run keeps it resolved and the rest open.
	open, _ := a.Breaks.List(ctx, tenant, date, BreakOpen)
	if _, err := a.ResolveBreak(ctx, tenant, "ops.priya", open[0].ID, "Confirmed with dealer"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ResolveBreak(ctx, tenant, "ops.priya", open[0].ID, "again"); err == nil {
		t.Fatal("resolving twice should fail")
	}
	sum2, _ := a.RunRecon(ctx, tenant, "ops.priya", date)
	if sum2.Open != 6 {
		t.Fatalf("after resolve + rerun, open = %d", sum2.Open)
	}

	// Billing.
	if _, err := a.RunBilling(ctx, tenant, "eod", date, nil); err == nil {
		t.Fatal("billing without settlement prices should fail")
	}
	br, err := a.RunBilling(ctx, tenant, "eod", date, prices(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(br.Posted) == 0 || len(br.Conflicts) != 0 || len(br.Warnings) < 2 {
		t.Fatalf("billing %+v", br)
	}
	again, err := a.RunBilling(ctx, tenant, "eod", date, prices(t))
	if err != nil || len(again.Posted) != 0 || len(again.Existing) != len(br.Posted) {
		t.Fatalf("re-run should be idempotent: %+v %v", again, err)
	}

	tb, err := a.Ledger.TrialBalance(ctx, tenant, date)
	if err != nil || !tb.Balanced {
		t.Fatalf("trial balance not balanced: %+v %v", tb, err)
	}
	bal, _ := a.Ledger.Balances(ctx, tenant, date)
	// BH1001: CM delivery buy 14,136.75 Dr; FO futures MTM 1,982.50 Cr and PE premium 5,590.00 Dr
	// with FO charges 93.38 + 26.09. Net 17,863.72 Dr.
	if got := bal[ledger.ClientAccount("BH1001")]; got != money.MustAmount("17863.72") {
		t.Fatalf("BH1001 balance %s, want 17863.72 Dr", got)
	}
	// Every client account plus clearing and statutory accounts sum to zero.
	var total money.Amount
	for _, v := range bal {
		total += v
	}
	if total != 0 {
		t.Fatalf("ledger does not sum to zero: %s", total)
	}

	// Statement for one client and the audit trail.
	st, _ := a.Ledger.Statement(ctx, tenant, ledger.ClientAccount("BH1001"), date, date)
	if len(st.Lines) != 4 || st.Closing != money.MustAmount("17863.72") {
		t.Fatalf("statement %+v", st)
	}
	ev, _ := a.Audit.List(ctx, tenant, 0)
	if len(ev) < 8 || ev[0].Action != "billing.run" {
		t.Fatalf("audit trail %d events, latest %+v", len(ev), ev[0])
	}

	// Reverse a bill, then re-billing reports a conflict on that key (rebill is explicit).
	rev, _, err := a.Reverse(ctx, tenant, "checker", br.Posted[0], "CCM moved trade to another client")
	if err != nil || rev.ReversalOf != br.Posted[0] {
		t.Fatalf("reverse: %v", err)
	}
}

func TestBreakClearsAfterCorrection(t *testing.T) {
	ctx := context.Background()
	a := newSampleApp(t)
	load(t, a, "NSE-CM-TRADE", "nse_cm_trades.csv")
	b, _ := os.ReadFile("../../testdata/sample/oms_trades.csv")
	a.IngestFile(ctx, "bhairav", "ops", "OMS-TRADE", "", "oms.csv", b)
	s1, _ := a.RunRecon(ctx, "bhairav", "ops", "2026-10-16")
	// The OMS file contains FO/MCX trades with no exchange file loaded yet, so they show as missing in exchange.
	before := s1.Open
	load(t, a, "NSE-FO-TRADE", "nse_fo_trades.csv")
	load(t, a, "MCX-TRADE", "mcx_trades.txt")
	s2, _ := a.RunRecon(ctx, "bhairav", "ops", "2026-10-16")
	cleared, _ := a.Breaks.List(ctx, "bhairav", "2026-10-16", BreakCleared)
	if s2.Open >= before || len(cleared) == 0 {
		t.Fatalf("open before %d after %d, cleared %d", before, s2.Open, len(cleared))
	}
}
