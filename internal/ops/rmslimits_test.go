package ops

import (
	"context"
	"strings"
	"testing"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// rlInput is a risk input with made-up contracts on NSE and BSE.
func rlInput(gross bool) riskInput {
	lim := RiskLimitConfig{GrossAcrossExchanges: gross}
	lim.fill()
	ins := []Instrument{
		{Exchange: "NSE", Segment: "FO", Symbol: "ACME", Type: "FUT", Expiry: "2026-12-31", Lot: 100, LTP: money.DecInt(100), SPANPct: money.DecInt(10), ExpoPct: money.DecInt(5)},
		{Exchange: "BSE", Segment: "FO", Symbol: "ACME", Type: "FUT", Expiry: "2026-12-31", Lot: 100, LTP: money.DecInt(100), SPANPct: money.DecInt(10), ExpoPct: money.DecInt(5)},
		{Exchange: "NSE", Segment: "CM", Symbol: "ACME", Type: "EQ", ISIN: "INE000Z01011", LTP: money.DecInt(50), VaRPct: money.DecInt(15), ELMPct: money.DecInt(5)},
		{Exchange: "BSE", Segment: "CM", Symbol: "ACMEB", Type: "EQ", ISIN: "INE000Z01011", LTP: money.DecInt(50), VaRPct: money.DecInt(15), ELMPct: money.DecInt(5)},
		{Exchange: "NSE", Segment: "CM", Symbol: "WILD", Type: "EQ", ISIN: "INE000Z02019", LTP: money.DecInt(10), VaRPct: money.DecInt(20), ELMPct: money.DecInt(5)},
	}
	tr := func(ex, seg, sym, typ, exp, side string, q int64, px int64, at, prod string) ingest.TradeEvent {
		return ingest.TradeEvent{Exchange: ex, Segment: seg, UCC: "ZZ01", Symbol: sym, InstrumentType: typ, Expiry: exp, Side: side, Qty: q, Price: money.DecInt(px), TradeDate: "2026-11-02", TradeTime: at, Product: prod}
	}
	return riskInput{date: "2026-11-02", set: DefaultSettings(), lim: lim, look: newLookup(Market{Instruments: ins}), clients: map[string]Client{"ZZ01": {UCC: "ZZ01"}},
		trades: []ingest.TradeEvent{
			tr("NSE", "FO", "ACME", "FUT", "2026-12-31", "B", 100, 100, "10:00:00", "NRML"),
			tr("BSE", "FO", "ACME", "FUT", "2026-12-31", "S", 60, 100, "10:05:00", "NRML"),
			tr("NSE", "CM", "ACME", "EQ", "", "B", 10, 50, "10:10:00", "MIS"),
			tr("BSE", "CM", "ACMEB", "EQ", "", "S", 10, 50, "10:11:00", "MIS"),
		}}
}

// The same contract on NSE and BSE, both cleared at NSE Clearing, is
// margined on the net; margined apart when the firm says so.
func TestMarginNettedAcrossExchanges(t *testing.T) {
	in := rlInput(false)
	pos, cm := in.positions("23:59:59")
	var nse, bse PositionView
	for _, p := range pos["ZZ01"] {
		if p.Exchange == "NSE" {
			nse = p
		} else {
			bse = p
		}
	}
	// Net long 40 × 100 × 15% = 600, all on the NSE leg.
	if nse.Margin.Cmp(money.DecInt(600)) != 0 || !bse.Margin.IsZero() || nse.Netted != "NCL" || bse.Netted != "NCL" {
		t.Fatalf("netted: nse %s bse %s (%s)", nse.Margin, bse.Margin, nse.Netted)
	}
	if !cm["ZZ01"].IsZero() {
		t.Fatalf("intraday cash netted by ISIN across exchanges: %s", cm["ZZ01"])
	}
	in = rlInput(true)
	pos, cm = in.positions("23:59:59")
	var tot money.Dec
	for _, p := range pos["ZZ01"] {
		tot = tot.Add(p.Margin)
	}
	if tot.Cmp(money.DecInt(2400)) != 0 || cm["ZZ01"].Cmp(money.DecInt(100)) != 0 {
		t.Fatalf("gross: %s cash %s", tot, cm["ZZ01"])
	}
	// BSE cleared at ICCL: no netting.
	in = rlInput(false)
	in.set.ClearingFor = map[string]string{"BSE-FO": "ICCL"}
	pos, _ = in.positions("")
	tot = money.Dec{}
	for _, p := range pos["ZZ01"] {
		tot = tot.Add(p.Margin)
	}
	if tot.Cmp(money.DecInt(2400)) != 0 {
		t.Fatalf("different clearing corporations: %s", tot)
	}
}

// A security under a surveillance measure is margined at the stage's rate.
func TestSurveillanceMargin(t *testing.T) {
	in := rlInput(false)
	in.trades = append(in.trades, ingest.TradeEvent{Exchange: "NSE", Segment: "CM", UCC: "ZZ01", Symbol: "WILD", InstrumentType: "EQ", Side: "B", Qty: 100, Price: money.DecInt(10), TradeDate: "2026-11-02", TradeTime: "11:00:00", Product: "CNC"})
	_, cm := in.positions("23:59:59")
	if cm["ZZ01"].Cmp(money.DecInt(250)) != 0 {
		t.Fatalf("VaR + ELM: %s", cm["ZZ01"])
	}
	in.surv = survIndex{}
	in.surv.add(SurvMeasure{Exchange: "NSE", Symbol: "WILD", ISIN: "INE000Z02019", Measure: "ASM", Stage: "LT-ASM IV", MarginPct: money.DecInt(100), TradeForTrade: true})
	_, cm = in.positions("23:59:59")
	if cm["ZZ01"].Cmp(money.DecInt(1000)) != 0 {
		t.Fatalf("surveillance margin: %s", cm["ZZ01"])
	}
}

// Expiry day: a stock spread that involves the expiring contract.
func TestExpirySpreads(t *testing.T) {
	pos := map[string][]PositionView{"ZZ01": {
		{Segment: "FO", Instrument: "ACME|FUT|2026-11-26|", Name: "ACME 26NOV26 FUT", FutEqQty: money.DecInt(500)},
		{Segment: "FO", Instrument: "ACME|FUT|2026-12-31|", Name: "ACME 31DEC26 FUT", FutEqQty: money.DecInt(-500)},
		{Segment: "FO", Instrument: "NIFTY|FUT|2026-11-26|", Index: true, FutEqQty: money.DecInt(75)},
		{Segment: "FO", Instrument: "NIFTY|FUT|2026-12-31|", Index: true, FutEqQty: money.DecInt(-75)},
	}, "ZZ02": {
		{Segment: "FO", Instrument: "ACME|FUT|2026-11-26|", FutEqQty: money.DecInt(500)},
		{Segment: "FO", Instrument: "ACME|FUT|2026-12-31|", FutEqQty: money.DecInt(500)},
	}}
	sp := expirySpreads(pos, "2026-11-26")
	if len(sp) != 1 || sp[0].UCC != "ZZ01" || sp[0].Symbol != "ACME" || len(sp[0].Expiring) != 1 {
		t.Fatalf("%+v", sp)
	}
	if len(expirySpreads(pos, "2026-11-25")) != 0 {
		t.Fatal("not expiry day")
	}
}

// Position limits, the ban period, the index cover report and the work
// items on the sample day.
func TestRiskLimitsSample(t *testing.T) {
	o, ctx := setup(t)
	v, err := o.RiskLimits(ctx, T, D)
	if err != nil || len(v.Limits) != 0 || !contains(v.Missing, "SBIN (NCL FO)") {
		t.Fatalf("before the file: %v %d %v", err, len(v.Limits), v.Missing)
	}
	// Made-up limits: SBIN's MWPL small enough that 1,500 shares breach
	// 10%; CRUDEOIL's client limit from the file.
	csv := "Exchange,Segment,Symbol,MWPL,ClientLimit,MemberLimit,OIValue\nNSE,FO,SBIN,12000,,,\nMCX,COM,CRUDEOIL,,150,250,\nNSE,FO,NIFTY,,,,1000000000\n"
	if _, err := o.LoadFile(ctx, T, "anita.rao", "POSITION-LIMITS", D, "limits.csv", []byte(csv)); err != nil {
		t.Fatal(err)
	}
	if _, err := o.LoadFile(ctx, T, "anita.rao", "FO-BAN", D, "ban.csv", []byte("Symbol\nRELIANCE\n")); err != nil {
		t.Fatal(err)
	}
	v, err = o.RiskLimits(ctx, T, D)
	if err != nil {
		t.Fatal(err)
	}
	var sbin, member, crude *PositionLimitRow
	for i, r := range v.Limits {
		switch {
		case r.Symbol == "SBIN" && r.Level == "Client" && r.Open.Cmp(money.DecInt(1500)) == 0:
			sbin = &v.Limits[i]
		case r.Symbol == "SBIN" && r.Level == "Member":
			member = &v.Limits[i]
		case r.Symbol == "CRUDEOIL" && r.Level == "Member":
			crude = &v.Limits[i]
		}
	}
	if sbin == nil || sbin.Status != "Breach" || sbin.Limit.Cmp(money.DecInt(1200)) != 0 || !strings.Contains(sbin.Basis, "10% of MWPL") {
		t.Fatalf("client SBIN: %+v", sbin)
	}
	if member == nil || member.Limit.Cmp(money.DecInt(3600)) != 0 || member.Status != "Breach" {
		t.Fatalf("member SBIN: %+v", member)
	}
	// MCX: member level is the higher of summed longs and summed shorts.
	if crude == nil || crude.Open.Cmp(money.Max(crude.Long, crude.Short)) != 0 || crude.Limit.Cmp(money.DecInt(250)) != 0 {
		t.Fatalf("member CRUDEOIL: %+v", crude)
	}
	// The firm's own code gets the client-level (or proprietary) limit.
	if _, err := o.SaveRiskLimitConfig(ctx, T, "anita.rao", RiskLimitConfig{ProprietaryUCCs: []string{sbin.UCCs[0]}}); err != nil {
		t.Fatal(err)
	}
	v, _ = o.RiskLimits(ctx, T, D)
	found := false
	for _, r := range v.Limits {
		if r.Level == "Proprietary" && r.Symbol == "SBIN" {
			found = r.Limit.Cmp(money.DecInt(2400)) == 0 && r.Entity == "Own account"
		}
	}
	if !found {
		t.Fatalf("proprietary row: %+v", v.Limits)
	}
	if len(v.Banned) != 1 || v.Banned[0] != "RELIANCE" {
		t.Fatalf("banned %v", v.Banned)
	}
	// Index futures limit from the open interest: the ₹500 crore floor is higher here.
	for _, r := range v.Index {
		if r.Kind == "Index futures gross" && r.Index == "NIFTY" && (r.Limit.Cmp(money.DecInt(5_000_000_000)) != 0 || r.Status != "OK") {
			t.Fatalf("index futures: %+v", r)
		}
	}
	// A made-up short call big enough to pass ₹1,500 crore net FutEq.
	big := ingest.TradeEvent{Tenant: T, Origin: ingest.FromExchange, Exchange: "NSE", Segment: "FO", TradeNo: "ZZ9000001", OrderNo: "ZZO1", UCC: sbin.UCCs[0], Symbol: "NIFTY", InstrumentType: "CE",
		Expiry: "2026-10-20", Strike: "25900", Side: "S", Qty: 1_500_000, Price: money.DecInt(100), TradeDate: D, TradeTime: "15:00:00", Product: "NRML"}
	if _, _, err := o.App.Trades.Add(ctx, []ingest.TradeEvent{big}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.SaveRiskLimitConfig(ctx, T, "priya.nair", RiskLimitConfig{}); err == nil {
		t.Fatal("an ops maker changed RMS limit settings")
	}
	run, err := o.RunRiskLimitChecks(ctx, T, "anita.rao", D)
	if err != nil || run.Raised == 0 {
		t.Fatalf("run: %v %d", err, run.Raised)
	}
	ws, _ := docs.List[WorkItem](ctx, o.Docs, T, cWorkItem, "")
	got := map[string]int{}
	for _, w := range ws {
		got[w.Process]++
	}
	if got[wPosLimits] == 0 || got[wIndexReport] != 1 || got[wIndexLimits] == 0 {
		t.Fatalf("work items: %v", got)
	}
	b, n, err := o.IndexCoverReport(ctx, T, "anita.rao", D)
	if err != nil || n == 0 || !strings.HasPrefix(string(b), "Date,PAN,ClientCodes,Index,Side") {
		t.Fatalf("cover report: %v %d", err, n)
	}
	if _, _, err := o.IndexCoverReport(ctx, T, "priya.nair", D); err == nil {
		t.Fatal("an ops maker downloaded the cover report")
	}
	// The self-inspection test finds the breaches.
	set, _ := o.Settings(ctx, T)
	e := &siEnv{o: o, ctx: ctx, tenant: T, date: D, now: o.now(), set: set}
	if s, err := siPositionLimits(e); err != nil || s.skip != "" || s.found == 0 {
		t.Fatalf("si: %+v %v", s, err)
	}
	if s, err := siFOBan(e); err != nil || s.skip != "" {
		t.Fatalf("si ban: %+v %v", s, err)
	}
}

// The surveillance list loads, gives the front-end file, and finds
// intraday trading in a trade-for-trade security.
func TestSurveillanceList(t *testing.T) {
	o, ctx := setup(t)
	ts, _ := o.exchangeTrades(ctx, T, D)
	sym := ""
	for _, x := range ts {
		if x.Segment == "CM" && x.Product == "MIS" && x.Exchange == "NSE" {
			sym = x.Symbol
			break
		}
	}
	if sym == "" {
		t.Fatal("no intraday trade in the sample")
	}
	csv := "Exchange,Symbol,Measure,Stage,MarginPct,TradeForTrade,From\nNSE," + sym + ",ASM,LT-ASM IV,100,Y,2026-01-01\nNSE,OLDONE,GSM,II,100,N,2020-01-01\n"
	if _, err := o.LoadFile(ctx, T, "anita.rao", "SURV-MEASURES", "", "surv.csv", []byte(csv)); err != nil {
		t.Fatal(err)
	}
	if _, err := o.LoadFile(ctx, T, "anita.rao", "SURV-MEASURES", "", "bad.csv", []byte("Exchange,Symbol,Measure,MarginPct\nNSE,X,ASM,150\n")); err == nil {
		t.Fatal("a margin above 100% loaded")
	}
	f := string(o.SurvMarginFile(ctx, T, D))
	if !strings.Contains(f, sym+",,ASM,LT-ASM IV,100,Y") {
		t.Fatalf("file: %s", f)
	}
	v, _ := o.RiskLimits(ctx, T, D)
	if len(v.Surveillance) == 0 || v.Surveillance[0].Symbol != sym {
		t.Fatalf("hits: %+v", v.Surveillance)
	}
	// Margin reporting takes the measure's margin for intraday positions.
	if v.SurvCount == 0 {
		t.Fatal("no measure in force")
	}
}

// One pay-in to NSE Clearing is shared across CC:NSE-CM and CC:BSE-CM by
// what each owes; a firm that clears BSE at ICCL keeps it on one.
func TestCCSplit(t *testing.T) {
	o, ctx := setup(t)
	day := "2020-01-06"
	post := func(key, acct string, amt money.Amount) {
		if _, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: T, Key: key, ValueDate: day, CreatedBy: "test", Narration: "made-up obligation",
			Postings: []ledger.Posting{{Account: "SUSPENSE:TEST", Debit: amt}, {Account: acct, Credit: amt}}}); err != nil {
			t.Fatal(err)
		}
	}
	post("T1", "CC:NSE-CM", 70000)
	post("T2", "CC:BSE-CM", 30000)
	x := FirmTransfer{Date: day, Purpose: "payin", Segment: "NSE-CM", Amount: 120000}
	sp := o.ccSplit(ctx, T, x)
	if len(sp) != 2 || sp[0].Account != "CC:NSE-CM" || sp[0].Amount != 90000 || sp[1].Account != "CC:BSE-CM" || sp[1].Amount != 30000 {
		t.Fatalf("split %+v", sp)
	}
	x.Split = sp
	ps := x.postings()
	var dr, cr money.Amount
	for _, p := range ps {
		dr, cr = dr+p.Debit, cr+p.Credit
	}
	if len(ps) != 3 || dr != cr || dr != 120000 {
		t.Fatalf("postings %+v", ps)
	}
	set, _ := o.Settings(ctx, T)
	set.ClearingFor = map[string]string{"BSE-CM": "ICCL"}
	if err := o.SaveSettings(ctx, T, "admin", set); err != nil {
		t.Fatal(err)
	}
	if sp := o.ccSplit(ctx, T, x); sp != nil {
		t.Fatalf("ICCL: %+v", sp)
	}
}

// Pay-in reminders on the settlement day of the sample trades.
func TestPayInReminders(t *testing.T) {
	o, ctx := setup(t)
	set, _ := o.Settings(ctx, T)
	lim := o.RiskLimitConfig(ctx, T)
	payIn := set.NextSettlementDay(D)
	if n := o.RaisePayInReminders(ctx, T, set, lim, payIn, "07:00"); n < 2 {
		t.Fatalf("raised %d", n)
	}
	ws, _ := docs.List[WorkItem](ctx, o.Docs, T, cWorkItem, "")
	var funds, sec *WorkItem
	for i, w := range ws {
		if w.Process != wPayInCutoff {
			continue
		}
		if strings.HasPrefix(w.Key, wPayInCutoff+":funds|NCL|") {
			funds = &ws[i]
		}
		if strings.HasPrefix(w.Key, wPayInCutoff+":securities|NCL|") {
			sec = &ws[i]
		}
	}
	if funds == nil || funds.Due != payIn+" 11:00" || sec == nil || sec.Due != payIn+" 10:30" {
		t.Fatalf("funds %+v securities %+v", funds, sec)
	}
	// After the cut-offs nothing more is raised.
	if n := o.RaisePayInReminders(ctx, T, set, lim, payIn, "11:30"); n != 0 {
		t.Fatalf("after the cut-off: %d", n)
	}
}

// The early pay-in rule: from the effective date a client in breach who
// sold with early pay-in and bought again is flagged.
func TestEPIMarginRule(t *testing.T) {
	o, ctx := setup(t)
	lv, _ := o.LiveRisk(ctx, T, D, "")
	ts, _ := o.exchangeTrades(ctx, T, D)
	ucc, sym := "", ""
	for _, r := range lv.Rows {
		if r.Free.Sign() >= 0 {
			continue
		}
		for _, x := range ts {
			if x.UCC == r.UCC && x.Segment == "CM" && x.Side == "B" {
				ucc, sym = r.UCC, x.Symbol
			}
		}
		if ucc != "" {
			break
		}
	}
	if ucc == "" {
		t.Skip("no client short of margin with a cash buy in the sample")
	}
	if err := docs.Upsert(ctx, o.Docs, T, cEPI, D+"|ZZ-1", EPIRecord{ID: D + "|ZZ-1", Date: D, UCC: ucc, Symbol: sym, Qty: 1, Status: "Accepted"}, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := o.SaveRiskLimitConfig(ctx, T, "anita.rao", RiskLimitConfig{EPIMarginFrom: "2099-01-01"}); err != nil {
		t.Fatal(err)
	}
	for _, r := range mustLive(t, o, ctx).Rows {
		if r.EPIRule != "" {
			t.Fatalf("before the effective date: %s", r.EPIRule)
		}
	}
	if _, err := o.SaveRiskLimitConfig(ctx, T, "anita.rao", RiskLimitConfig{EPIMarginFrom: D}); err != nil {
		t.Fatal(err)
	}
	hit := false
	for _, r := range mustLive(t, o, ctx).Rows {
		hit = hit || r.UCC == ucc && strings.Contains(r.EPIRule, "squared off early pay-in sales in "+sym)
	}
	if !hit {
		t.Fatal("rule not applied")
	}
}

func mustLive(t *testing.T, o *Ops, ctx context.Context) LiveView {
	t.Helper()
	v, err := o.LiveRisk(ctx, T, D, "")
	if err != nil {
		t.Fatal(err)
	}
	return v
}
