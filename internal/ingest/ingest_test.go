package ingest

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

func registry(t *testing.T) *Registry {
	t.Helper()
	r, err := LoadDir("../../config/adapters")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func read(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("../../testdata/sample/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseSampleFiles(t *testing.T) {
	r := registry(t)
	cases := []struct {
		source, file string
		want         int
	}{
		{"NSE-CM-TRADE", "nse_cm_trades.csv", 10},
		{"NSE-FO-TRADE", "nse_fo_trades.csv", 5},
		{"MCX-TRADE", "mcx_trades.txt", 2},
		{"OMS-TRADE", "oms_trades.csv", 17},
	}
	for _, c := range cases {
		spec, err := r.Get(c.source, "")
		if err != nil {
			t.Fatal(err)
		}
		evs, errs, err := spec.Parse(strings.NewReader(string(read(t, c.file))))
		if err != nil || len(errs) > 0 {
			t.Fatalf("%s: err=%v rowErrors=%v", c.source, err, errs)
		}
		if len(evs) != c.want {
			t.Fatalf("%s: parsed %d, want %d", c.source, len(evs), c.want)
		}
	}
	spec, _ := r.Get("NSE-FO-TRADE", "")
	evs, _, _ := spec.Parse(strings.NewReader(string(read(t, "nse_fo_trades.csv"))))
	ce := evs[1]
	if ce.InstrumentType != "CE" || ce.Strike != "25900" || ce.Expiry != "2026-10-20" || ce.Side != "S" || ce.Instrument() != "NIFTY|CE|2026-10-20|25900" {
		t.Fatalf("option parsed as %+v", ce)
	}
	if evs[0].InstrumentType != "FUT" || evs[0].Strike != "" || evs[0].TradeDate != "2026-10-16" || evs[0].Line != 2 {
		t.Fatalf("future parsed as %+v", evs[0])
	}
	mspec, _ := r.Get("MCX-TRADE", "")
	m, _, _ := mspec.Parse(strings.NewReader(string(read(t, "mcx_trades.txt"))))
	if m[0].Expiry != "2026-11-19" || m[0].Exchange != "MCX" || m[0].Segment != "COM" || m[0].Line != 1 {
		t.Fatalf("mcx parsed as %+v", m[0])
	}
}

func TestStrictRejectsBadRows(t *testing.T) {
	spec, _ := registry(t).Get("NSE-CM-TRADE", "")
	bad := "TradeNo,OrderNo,ClientCode,Symbol,Series,BuySell,Qty,Price,TradeDate,TradeTime,Product,AlgoId,TerminalId\n" +
		"1,2,BH1001,RELIANCE,EQ,1,10,1412,16-Oct-2026,09:21:14,CNC,,T\n" +
		"3,4,BH1002,INFY,EQ,9,-5,abc,2026/10/16,09:21:14,CNC,,T\n"
	evs, errs, err := spec.Parse(strings.NewReader(bad))
	if !errors.Is(err, ErrRejected) || evs != nil {
		t.Fatalf("want rejection, got evs=%d err=%v", len(evs), err)
	}
	fields := map[string]bool{}
	for _, e := range errs {
		if e.Line != 3 {
			t.Errorf("error on line %d, want 3", e.Line)
		}
		fields[e.Field] = true
	}
	for _, f := range []string{"side", "qty", "price", "trade_date"} {
		if !fields[f] {
			t.Errorf("missing error for %s; got %v", f, errs)
		}
	}
}

func TestWrongFileVersion(t *testing.T) {
	spec, _ := registry(t).Get("NSE-CM-TRADE", "")
	_, _, err := spec.Parse(strings.NewReader("Trade,Client\n1,BH1\n"))
	if err == nil || !strings.Contains(err.Error(), "right file version") {
		t.Fatalf("expected header mismatch error, got %v", err)
	}
}

func TestServiceIdempotentReupload(t *testing.T) {
	ctx := context.Background()
	svc := &Service{Raw: NewMemRawStore(), Trades: NewMemTradeStore(), Registry: registry(t)}
	data := read(t, "nse_cm_trades.csv")
	r1, err := svc.Ingest(ctx, "t1", "NSE-CM-TRADE", "", "trades.csv", data)
	if err != nil || r1.Added != 10 || r1.File.PriorID != "" {
		t.Fatalf("first upload %+v err %v", r1, err)
	}
	r2, err := svc.Ingest(ctx, "t1", "NSE-CM-TRADE", "", "trades-again.csv", data)
	if err != nil || r2.Added != 0 || r2.DuplicateRows != 10 || r2.File.PriorID != r1.File.ID {
		t.Fatalf("re-upload %+v err %v", r2, err)
	}
	// A changed price under an existing trade number is a conflict, and nothing is stored.
	changed := strings.Replace(string(data), "1412.00", "1413.00", 1)
	if _, err := svc.Ingest(ctx, "t1", "NSE-CM-TRADE", "", "x.csv", []byte(changed)); !errors.Is(err, ErrTradeConflict) {
		t.Fatalf("want conflict, got %v", err)
	}
	evs, _ := svc.Trades.List(ctx, "t1", FromExchange, "2026-10-16")
	if len(evs) != 10 || evs[0].FileID != r1.File.ID {
		t.Fatalf("stored %d events, first from %s", len(evs), evs[0].FileID)
	}
	other, _ := svc.Trades.List(ctx, "t2", "", "")
	if len(other) != 0 {
		t.Fatal("tenant isolation broken")
	}
}

func TestVersionSelection(t *testing.T) {
	if compareVersions("0.10.0", "0.9.1") <= 0 || compareVersions("1.0.0", "1.0.0") != 0 || compareVersions("0.1.0-sample", "0.2.0") >= 0 {
		t.Fatal("version comparison is wrong")
	}
	r := registry(t)
	s, _ := r.Get("OMS-TRADE", "")
	s.Version = "0.2.0"
	if err := r.Add(s); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.Get("OMS-TRADE", ""); got.Version != "0.2.0" {
		t.Fatalf("latest = %s", got.Version)
	}
	if got, _ := r.Get("OMS-TRADE", "0.1.0"); got.Version != "0.1.0" {
		t.Fatal("pinned version not returned")
	}
}

func TestLoadDirReportsWhy(t *testing.T) {
	if _, err := LoadDir("does/not/exist"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing folder: %v", err)
	}
	if os.Getuid() == 0 {
		t.Skip("root ignores permissions")
	}
	d := t.TempDir()
	os.Chmod(d, 0)
	defer os.Chmod(d, 0o755)
	if _, err := LoadDir(d); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("unreadable folder: %v", err)
	}
}
