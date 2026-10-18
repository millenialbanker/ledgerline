package ops

import (
	"context"
	"strings"
	"testing"

	"ledgerline"
	"ledgerline/internal/app"
	"ledgerline/internal/charges"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
	"ledgerline/internal/vault"
)

func newOps(t *testing.T) (*Ops, context.Context) {
	t.Helper()
	ctx := context.Background()
	reg, _ := ingest.LoadFS(ledgerline.FS, "config/adapters")
	f, _ := ledgerline.FS.Open("config/charges/sample-schedule.json")
	sched, _ := charges.LoadSchedule(f)
	f.Close()
	a := app.New(vault.Protect(app.MemoryStores(), vault.New(vault.Ephemeral())), reg, sched)
	o := New(a)
	o.SimulateOMS = true
	if err := o.SeedDev(ctx, T); err != nil {
		t.Fatal(err)
	}
	return o, ctx
}

func ingester(a *app.App) TradeIngester {
	return func(ctx context.Context, tenant, actor, source, name string, data []byte) error {
		_, err := a.IngestFile(ctx, tenant, actor, source, "", name, data)
		return err
	}
}

// TestSampleWeek works five consecutive trading days end to end and checks
// that positions carry forward, carried futures are marked to market
// against each day's settlement price, weekly options expire on their day,
// and a day cannot be worked while the previous one is open.
func TestSampleWeek(t *testing.T) {
	o, ctx := newOps(t)
	a := o.App
	date := "2026-10-16"
	var days []string
	for i := 0; i < 5; i++ {
		run, err := o.RunSampleDay(ctx, T, "admin", date, ingester(a))
		if err != nil {
			t.Fatalf("day %d %s: %v", i+1, date, err)
		}
		t.Logf("%+v", run)
		if run.Trades < 150 || run.Bills == 0 || run.Rolled == 0 {
			t.Fatalf("thin day: %+v", run)
		}
		if i > 0 && run.MTM == 0 {
			t.Fatalf("%s: carried futures were not marked to market", date)
		}
		tb, _ := a.Ledger.TrialBalance(ctx, T, date)
		if !tb.Balanced {
			t.Fatalf("%s: trial balance off", date)
		}
		days = append(days, date)
		date = run.NextDay
	}
	if strings.Join(days, ",") != "2026-10-16,2026-10-19,2026-10-20,2026-10-21,2026-10-22" {
		t.Fatalf("days %v", days)
	}
	// Carried MTM on each later day = sum of qty x (today's close - previous close).
	for i := 1; i < len(days); i++ {
		d, prev := days[i], days[i-1]
		cf, _ := o.carried(ctx, T, d)
		_, today, _ := o.Market(ctx, T, d)
		_, yday, _ := o.Market(ctx, T, prev)
		want := map[string]money.Dec{}
		for _, c := range cf {
			if !strings.Contains(c.Instrument, "|FUT|") {
				continue
			}
			ti, _ := today.Get(c.Exchange, c.Segment, c.Instrument)
			yi, _ := yday.Get(c.Exchange, c.Segment, c.Instrument)
			if c.AvgPrice.Cmp(yi.Close) != 0 {
				t.Fatalf("%s %s carried at %s, previous close %s", d, c.Instrument, c.AvgPrice, yi.Close)
			}
			k := c.UCC + "/" + c.Exchange + "-" + c.Segment
			want[k] = want[k].Add(ti.Close.Sub(yi.Close).MulInt(c.Qty))
		}
		es, _ := a.Ledger.Store.List(ctx, T, ledger.Filter{From: d, To: d})
		got := map[string]money.Amount{}
		for _, e := range es {
			if e.Source.Kind != "MTM" {
				continue
			}
			k := strings.SplitN(e.Source.ID, "/", 2)[1]
			for _, p := range e.Postings {
				if strings.HasPrefix(p.Account, "CLIENT:") {
					got[k] += p.Credit - p.Debit
				}
			}
		}
		for k, w := range want {
			if w.Round(money.UnitPaisa) != got[k] {
				t.Errorf("%s %s: MTM posted %s, want %s", d, k, got[k].Dec(), w)
			}
		}
	}
	// Weekly NIFTY options expired on 20-Oct: nothing carries past it.
	for _, d := range days[3:] {
		cf, _ := o.carried(ctx, T, d)
		for _, c := range cf {
			if strings.Contains(c.Instrument, "|2026-10-20|") {
				t.Fatalf("%s still carries expired %s", d, c.Instrument)
			}
		}
	}
	// The next day cannot be billed or loaded while the last is open: load
	// 23-Oct without closing it, then try 26-Oct.
	if _, err := o.LoadSampleFor(ctx, T, "admin", "2026-10-23", ingester(a)); err != nil {
		t.Fatal(err)
	}
	if err := o.DayOpenError(ctx, T, "2026-10-26"); err == nil {
		t.Fatal("26-Oct should be blocked while 23-Oct is open")
	}
	if _, err := o.LoadSampleFor(ctx, T, "admin", "2026-10-26", ingester(a)); err == nil {
		t.Fatal("loading 26-Oct before closing 23-Oct should fail")
	}
	// Reports span the week.
	r, err := o.ClientReportFor(ctx, T, "tradebook", "ALL", days[0], days[4])
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("trade book for the week: %v", r.Summary)
}
