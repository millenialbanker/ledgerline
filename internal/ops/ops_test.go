package ops

import (
	"context"
	"os"
	"strings"
	"testing"

	"ledgerline"
	"ledgerline/internal/app"
	"ledgerline/internal/charges"
	"ledgerline/internal/ingest"
	"ledgerline/internal/money"
	"ledgerline/internal/sample"
)

const T, D = "demo", "2026-10-16"

func setup(t *testing.T) (*Ops, context.Context) {
	t.Helper()
	ctx := context.Background()
	reg, err := ingest.LoadFS(ledgerline.FS, "config/adapters")
	if err != nil {
		t.Fatal(err)
	}
	f, _ := ledgerline.FS.Open("config/charges/sample-schedule.json")
	sched, err := charges.LoadSchedule(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	a := app.New(app.MemoryStores(), reg, sched)
	o := New(a)
	o.SimulateOMS = true
	if err := o.SeedDev(ctx, T); err != nil {
		t.Fatal(err)
	}
	day := sample.Generate(D)
	if os.Getenv("LL_DUMP_SAMPLE") != "" {
		for _, f := range day.Files {
			os.WriteFile(os.Getenv("LL_DUMP_SAMPLE")+"/"+f.Name, f.Data, 0o644)
		}
	}
	n, err := o.LoadSampleDay(ctx, T, "priya.nair", day, false, func(ctx context.Context, tenant, actor, source, name string, data []byte) error {
		r, err := a.IngestFile(ctx, tenant, actor, source, "", name, data)
		if err != nil {
			t.Logf("%s errors: %v", name, r.Errors)
		}
		return err
	})
	if err != nil {
		t.Fatalf("loaded %d files: %v", n, err)
	}
	return o, ctx
}

func TestSampleDay(t *testing.T) {
	o, ctx := setup(t)
	a := o.App
	cs, _ := o.Clients(ctx, T)
	ex, _ := a.ExchangeTrades(ctx, T, D)
	t.Logf("clients %d, exchange trades %d", len(cs), len(ex))
	if len(cs) != 64 || len(ex) < 250 {
		t.Fatalf("clients %d trades %d", len(cs), len(ex))
	}
	sum, err := a.RunRecon(ctx, T, "priya.nair", D)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("recon: %d matched, breaks %v", sum.Result.Matched, sum.Result.ByType)
	if len(sum.Result.Breaks) < 15 {
		t.Fatalf("expected the injected breaks, got %v", sum.Result.ByType)
	}
	m, _, err := o.Market(ctx, T, D)
	if err != nil {
		t.Fatal(err)
	}
	br, err := a.RunBilling(ctx, T, "priya.nair", D, m.SettlementPrices())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("billing: posted %d", len(br.Posted))
	tb, _ := a.Ledger.TrialBalance(ctx, T, D)
	if !tb.Balanced {
		t.Fatal("trial balance off")
	}
	rv, err := o.Risk(ctx, T, D)
	if err != nil {
		t.Fatal(err)
	}
	st := map[string]int{}
	for _, r := range rv.Rows {
		st[r.Status]++
	}
	t.Logf("risk: %v warning=%q", st, rv.Warning)
	if st["Breach"] == 0 || st["OK"] == 0 {
		t.Fatalf("risk mix %v", st)
	}
	mr, err := o.Margins(ctx, T, D)
	if err != nil {
		t.Fatal(err)
	}
	short := 0
	for _, r := range mr.Rows {
		if r.Short.Sign() > 0 {
			short++
		}
	}
	t.Logf("margin rows %d, short %d", len(mr.Rows), short)
	se, err := o.Obligations(ctx, T, D)
	if err != nil {
		t.Fatal(err)
	}
	ss := map[string]int{}
	for _, ob := range se.Obligations {
		ss[ob.Status]++
	}
	t.Logf("settlement: %v pay-in %s totals %v mtm rows %d", ss, se.PayIn, se.Totals, len(se.MTM))
	if se.PayIn != "2026-10-19" {
		t.Fatalf("pay-in %s", se.PayIn)
	}
	// Settlement actions.
	for steps := 0; steps < 500; steps++ {
		cur, _ := o.Obligations(ctx, T, D)
		var next *Obligation
		for i := range cur.Obligations {
			if cur.Obligations[i].Action != "" {
				next = &cur.Obligations[i]
				break
			}
		}
		if next == nil {
			break
		}
		if _, err := o.SettleAction(ctx, T, "priya.nair", D, next.Key, next.Action); err != nil {
			t.Fatalf("settle %s %s: %v", next.Key, next.Action, err)
		}
	}
	// Notes.
	n, err := o.GenerateNotes(ctx, T, "priya.nair", D)
	if err != nil || n == 0 {
		t.Fatalf("notes %d %v", n, err)
	}
	sent, bounced, err := o.SendNotes(ctx, T, "priya.nair", D, "")
	t.Logf("notes %d sent %d bounced %d", n, sent, bounced)
	if err != nil || sent == 0 {
		t.Fatal(err)
	}
	notes, _ := o.Notes(ctx, T, D)
	html, err := o.RenderNote(ctx, T, D, notes[0].UCC)
	if err != nil || !strings.Contains(string(html), notes[0].No) {
		t.Fatalf("render: %v", err)
	}
	// Bank.
	bt, _ := o.BankTxns(ctx, T)
	bs := map[string]int{}
	for _, b := range bt {
		bs[b.Status]++
	}
	t.Logf("bank %v", bs)
	if bs["Matched"] == 0 || bs["Third-party"] == 0 || bs["Unidentified"] == 0 {
		t.Fatalf("bank mix %v", bs)
	}
	for _, b := range bt {
		if b.Status == "Third-party" {
			if _, err := o.ReturnCredit(ctx, T, "priya.nair", b.ID, "not registered"); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	for _, b := range bt {
		if b.Status == "Unidentified" {
			if _, err := o.IdentifyCredit(ctx, T, "priya.nair", b.ID, "BH1001", true); err == nil {
				t.Fatal("maker should not identify")
			}
			if _, err := o.IdentifyCredit(ctx, T, "farhan.khan", b.ID, "BH1001", true); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	// RMS overrides and square-off.
	var breach string
	for _, r := range rv.Rows {
		if r.Status == "Breach" && r.Positions > 0 {
			breach = r.UCC
			break
		}
	}
	ov, err := o.CreateOverride(ctx, T, "priya.nair", D, breach, money.MustDec("50000"), "Funds in transit")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.DecideOverride(ctx, T, "priya.nair", D, ov.ID, true); err == nil {
		t.Fatal("maker approved own override")
	}
	if _, err := o.DecideOverride(ctx, T, "farhan.khan", D, ov.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := o.SquareOffClients(ctx, T, "priya.nair", D, nil); err == nil {
		t.Fatal("maker squared off")
	}
	sq, err := o.SquareOffClients(ctx, T, "anita.rao", D, nil)
	if err != nil {
		t.Fatal(err)
	}
	rv2, _ := o.Risk(ctx, T, D)
	left := 0
	for _, r := range rv2.Rows {
		if r.Status == "Breach" && r.Positions > 0 {
			left++
		}
	}
	t.Logf("square-off: %d instructions, %d filled, breaches with positions left %d", len(sq.Instructions), sq.Filled, left)
	if left != 0 {
		t.Fatal("breached positions remain after simulated square-off")
	}
	rb, err := a.RunBillingOpts(ctx, T, "farhan.khan", D, m.SettlementPrices(), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("rebill: posted %d reversed %d existing %d", len(rb.Posted), len(rb.Reversed), len(rb.Existing))
	if len(rb.Reversed) == 0 || len(rb.Posted) == 0 {
		t.Fatal("rebill did nothing")
	}
	if tb, _ := a.Ledger.TrialBalance(ctx, T, D); !tb.Balanced {
		t.Fatal("trial balance off after rebill")
	}
	// CCM.
	open, _ := a.Breaks.List(ctx, T, D, "OPEN")
	for _, b := range open {
		if string(b.Type) == "UCC_MISMATCH" {
			if _, err := o.RaiseCCM(ctx, T, "priya.nair", b.ID, ""); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	// RAS.
	rv3, err := o.RAS(ctx, T, D)
	if err != nil {
		t.Fatal(err)
	}
	due := 0
	for _, r := range rv3.Rows {
		if r.SettlesOn == "2026-11-06" && r.Payout.Sign() > 0 {
			due++
		}
	}
	b, err := o.RunRAS(ctx, T, "farhan.khan", D, "2026-11-06")
	if err != nil || len(b.Items) != due {
		t.Fatalf("ras %d of %d: %v", len(b.Items), due, err)
	}
	// Reports and EOD.
	for _, d := range ReportDefs() {
		if _, err := o.ReportStep(ctx, T, "priya.nair", D, d.Code, "validate", ""); err != nil {
			t.Fatalf("%s: %v", d.Code, err)
		}
	}
	if _, err := o.ReportStep(ctx, T, "vikram.shah", D, "funds_balance", "upload", "ACK123"); err != nil {
		t.Fatal(err)
	}
	if n, err := o.RunRules(ctx, T, "vikram.shah", D); err != nil {
		t.Fatal(err)
	} else {
		t.Logf("rules raised %d", n)
	}
	dash, err := o.Dashboard(ctx, T, D)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("dashboard: trades %d turnover %s funds cover %.2f eod checks %d", dash.Trades, dash.Turnover.Fixed(0), dash.Funds.Cover, len(dash.EOD.Checks))
	if _, err := o.CloseDay(ctx, T, "farhan.khan", D, ""); err == nil {
		t.Log("closed clean")
	} else if _, err := o.CloseDay(ctx, T, "farhan.khan", D, "test close"); err != nil {
		t.Fatal(err)
	}
	if tb, _ := a.Ledger.TrialBalance(ctx, T, D); !tb.Balanced {
		t.Fatal("trial balance off at end")
	}
	// IPO allotment from the later file.
	if _, err := o.LoadSampleDay(ctx, T, "priya.nair", sample.Generate(D), true, nil); err != nil {
		t.Fatal(err)
	}
	ipos, _ := o.IPOApps(ctx, T)
	for _, x := range ipos {
		if x.Allotment == "Awaiting" {
			t.Fatalf("allotment not applied: %+v", x)
		}
	}
}
