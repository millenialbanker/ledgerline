package ops

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// at sets the clock to a date and time in IST.
func sipAt(o *Ops, date, hm string) {
	t, err := time.ParseInLocation("2006-01-02 15:04", date+" "+hm, ist)
	if err != nil {
		panic(err)
	}
	o.Now = func() time.Time { return t }
}

// sipSetup switches SIPs on with an eligible list of the sample's large caps.
func sipSetup(t *testing.T, mut func(*SIPSettings)) (*Ops, context.Context) {
	t.Helper()
	o, ctx := setup(t)
	sipAt(o, D, "10:00")
	if _, err := o.UploadStockSIPList(ctx, T, "vikram.shah", "eligible", "top250.csv", []byte("Symbol,ISIN,Category\nRELIANCE,INE002A01018,Top 250\nHDFCBANK,INE040A01034,Top 250\nINFY,,Top 250\nTCS,INE467B01029,Top 250\nSBIN,INE062A01020,Top 250\nMARUTI,,Top 250\nITC,,Top 250\n")); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultSIPSettings()
	cfg.Enabled = true
	if mut != nil {
		mut(&cfg)
	}
	if _, err := o.SaveStockSIPSettings(ctx, T, "vikram.shah", cfg); err != nil {
		t.Fatal(err)
	}
	return o, ctx
}

func staffSIP(t *testing.T, o *Ops, ctx context.Context, r SIPRequest) StockSIP {
	t.Helper()
	r.ConsentSource, r.ConsentRef = SrcForm, "SIP-FORM-1"
	s, err := o.CreateStockSIP(ctx, T, "priya.nair", "", r)
	if err != nil {
		t.Fatalf("create %+v: %v", r, err)
	}
	return s
}

func sipOrders(t *testing.T, o *Ops, ctx context.Context, date string) map[string]SIPOrder {
	t.Helper()
	xs, err := docs.List[SIPOrder](ctx, o.Docs, T, cSIPOrder, date+"|")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]SIPOrder{}
	for _, x := range xs {
		out[x.SIP] = x
	}
	return out
}

func hasMessage(t *testing.T, o *Ops, ctx context.Context, ucc, event string) bool {
	t.Helper()
	ms, _ := o.Messages(ctx, T, ucc, 0)
	for _, m := range ms {
		if m.Event == event {
			return true
		}
	}
	return false
}

func TestStockSIPSettings(t *testing.T) {
	o, ctx := setup(t)
	cfg := DefaultSIPSettings()
	cfg.Enabled = true
	if _, err := o.SaveStockSIPSettings(ctx, T, "priya.nair", cfg); !errors.Is(err, ErrForbidden) {
		t.Fatalf("an ops maker set SIP rules: %v", err)
	}
	cfg.AlgoTag, cfg.AlgoID, cfg.OrderType = true, "ALG123", "MARKET"
	if _, err := o.SaveStockSIPSettings(ctx, T, "vikram.shah", cfg); err == nil || !strings.Contains(err.Error(), "market orders") {
		t.Fatalf("algo market orders allowed: %v", err)
	}
	cfg.OrderType = "LIMIT"
	cfg.TopupCap = 2000000
	if _, err := o.SaveStockSIPSettings(ctx, T, "vikram.shah", cfg); err == nil || !strings.Contains(err.Error(), "15,000") {
		t.Fatalf("a top-up above ₹15,000 allowed: %v", err)
	}
	cfg.TopupCap = sipTopupLimit
	cfg.Topups = true
	if _, err := o.SaveStockSIPSettings(ctx, T, "vikram.shah", cfg); err == nil || !strings.Contains(err.Error(), "Connections") {
		t.Fatalf("top-ups on without a mandate provider: %v", err)
	}
	cfg.Topups = false
	s, err := o.SaveStockSIPSettings(ctx, T, "vikram.shah", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if s.DecidedBy != "vikram.shah" || s.ConsentVersion != 1 {
		t.Fatalf("decision not recorded: %+v", s)
	}
	cfg.ConsentText = "I authorise {firm} to buy {scrip} for me."
	s, err = o.SaveStockSIPSettings(ctx, T, "vikram.shah", cfg)
	if err != nil || s.ConsentVersion != 2 {
		t.Fatalf("new wording is not a new version: %d %v", s.ConsentVersion, err)
	}
}

func TestStockSIPSchedule(t *testing.T) {
	set := Settings{Holidays: []string{"2026-12-25"}}
	// Monthly on the 31st: the last day of a shorter month, and a Saturday
	// moves to Monday.
	m := StockSIP{Frequency: SIPMonthly, Day: 31, Start: "2026-01-01"}
	if !m.scheduledOn("2026-11-30", set) || m.scheduledOn("2026-11-29", set) {
		t.Fatal("31st is not the last day of November")
	}
	if got := m.scheduledFor(set, "2026-11-02"); len(got) != 1 || got[0] != "2026-10-31" {
		t.Fatalf("Saturday 31 Oct instalment not on Monday: %v", got)
	}
	if got := m.scheduledFor(set, "2026-10-30"); len(got) != 0 {
		t.Fatalf("instalment placed early: %v", got)
	}
	// Weekly on Friday: Christmas (a holiday) moves to Monday 28 Dec.
	w := StockSIP{Frequency: SIPWeekly, Day: 5, Start: "2026-01-01"}
	if got := w.scheduledFor(set, "2026-12-28"); len(got) != 1 || got[0] != "2026-12-25" {
		t.Fatalf("holiday instalment: %v", got)
	}
	if got := w.scheduledFor(set, "2026-12-25"); got != nil {
		t.Fatalf("instalment on a holiday: %v", got)
	}
	// Daily: every trading day, none at weekends.
	d := StockSIP{Frequency: SIPDaily, Start: "2026-10-16", End: "2026-10-20"}
	if len(d.scheduledFor(set, "2026-10-19")) != 1 || len(d.scheduledFor(set, "2026-10-21")) != 0 {
		t.Fatal("daily schedule or its end date")
	}
	if n := sipNextScheduled(w, set, "2026-12-26"); n != "2027-01-01" || sipTradeDate(set, "2026-12-25") != "2026-12-28" {
		t.Fatalf("next %s", n)
	}
	if floorDiv(money.MustDec("5000"), money.MustDec("1400.85")) != 3 || floorDiv(money.MustDec("2801.70"), money.MustDec("1400.85")) != 2 || floorDiv(money.MustDec("10"), money.MustDec("1400.85")) != 0 {
		t.Fatal("quantity is not rounded down")
	}
}

func TestStockSIPRegister(t *testing.T) {
	o, ctx := sipSetup(t, nil)
	r := SIPRequest{UCC: "BH1001", Symbol: "RELIANCE", Basis: SIPAmount, Amount: 500000, Frequency: SIPMonthly, Day: 5}
	if _, err := o.CreateStockSIP(ctx, T, "priya.nair", "", r); err == nil || !strings.Contains(err.Error(), "standing instruction") {
		t.Fatalf("no consent record: %v", err)
	}
	s := staffSIP(t, o, ctx, r)
	if s.ISIN != "INE002A01018" || s.Exchange != "NSE" || s.Status != SIPActive || s.Consent.Version != 1 || !strings.Contains(s.Consent.Text, "RELIANCE") || !strings.Contains(s.Consent.Text, "₹5,000") {
		t.Fatalf("registered %+v", s)
	}
	// Not on the firm's list; restricted; trade-for-trade.
	if _, err := o.CreateStockSIP(ctx, T, "priya.nair", "", SIPRequest{UCC: "BH1001", Symbol: "LT", Basis: SIPAmount, Amount: 500000, Frequency: SIPDaily, ConsentSource: SrcCall, ConsentRef: "C1"}); err == nil || !strings.Contains(err.Error(), "list of scrips") {
		t.Fatalf("scrip off the list: %v", err)
	}
	if _, err := o.UploadStockSIPList(ctx, T, "vikram.shah", "restricted", "asm.csv", []byte("ISIN,Reason\nINE040A01034,ASM Stage II\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := o.CreateStockSIP(ctx, T, "priya.nair", "", SIPRequest{UCC: "BH1001", Symbol: "HDFCBANK", Basis: SIPQty, Qty: 5, Frequency: SIPDaily, ConsentSource: SrcCall, ConsentRef: "C1"}); err == nil || !strings.Contains(err.Error(), "ASM Stage II") {
		t.Fatalf("restricted scrip: %v", err)
	}
	_ = docs.Upsert(ctx, o.Docs, T, cSecRef, "INE467B01029", SecRef{ISIN: "INE467B01029", Symbol: "TCS", Series: "BE"}, "test")
	if _, err := o.CreateStockSIP(ctx, T, "priya.nair", "", SIPRequest{UCC: "BH1001", Symbol: "TCS", Basis: SIPQty, Qty: 5, Frequency: SIPDaily, ConsentSource: SrcCall, ConsentRef: "C1"}); err == nil || !strings.Contains(err.Error(), "trade-for-trade") {
		t.Fatalf("T2T scrip: %v", err)
	}
	if _, err := o.CreateStockSIP(ctx, T, "priya.nair", "", SIPRequest{UCC: "BH1001", Symbol: "MARUTI", Basis: SIPAmount, Amount: 500000, Frequency: SIPDaily, ConsentSource: SrcCall, ConsentRef: "C1"}); err == nil || !strings.Contains(err.Error(), "one share") {
		t.Fatalf("amount below one share: %v", err)
	}
	// A client in the portal: own accounts only, and must agree.
	if err := o.PutUser(ctx, T, "admin", User{ID: "client.bh1001", Name: "Aarav Sharma", Role: RoleClient, Active: true, Clients: []string{"BH1001"}}); err != nil {
		t.Fatal(err)
	}
	pr := SIPRequest{UCC: "BH1001", Symbol: "SBIN", Basis: SIPAmount, Amount: 300000, Frequency: SIPWeekly, Day: 3}
	pv, err := o.PreviewStockSIP(ctx, T, "client.bh1001", "", pr)
	if err != nil || pv.QtyNow != 3 || pv.Next != "2026-10-21" || !strings.Contains(pv.ConsentText, "every Wednesday") || pv.Estimate < 265000 || pv.Estimate > 270000 {
		t.Fatalf("preview %+v %v", pv, err)
	}
	if _, err := o.CreateStockSIP(ctx, T, "client.bh1001", "", pr); err == nil {
		t.Fatal("registered without the client agreeing")
	}
	pr.Consent, pr.ConsentVersion = true, pv.ConsentVersion
	ps, err := o.CreateStockSIP(ctx, T, "client.bh1001", "", pr)
	if err != nil || ps.Consent.Source != SrcPortal || ps.Consent.By != "client.bh1001" {
		t.Fatalf("portal SIP %+v %v", ps.Consent, err)
	}
	if _, err := o.CreateStockSIP(ctx, T, "client.bh1001", "", SIPRequest{UCC: "BH1002", Symbol: "SBIN", Basis: SIPAmount, Amount: 300000, Frequency: SIPDaily, Consent: true}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SIP on another client's account: %v", err)
	}
	pv2, _ := o.PortalStockSIPs(ctx, T, "client.bh1001", "", D)
	if len(pv2.SIPs) != 2 || pv2.SIPs[0].Next == "" {
		t.Fatalf("portal view %+v", pv2.SIPs)
	}
	// Pause, skip, resume, cancel (revokes the instruction).
	if _, err := o.ChangeStockSIP(ctx, T, "client.bh1001", ps.ID, "skip", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if x, _, _ := docs.Get[StockSIP](ctx, o.Docs, T, cStockSIP, ps.ID); len(x.Skips) != 1 || x.Skips[0] != "2026-10-21" {
		t.Fatalf("skip %v", x.Skips)
	}
	if _, err := o.ChangeStockSIP(ctx, T, "client.bh1001", ps.ID, "pause", "2026-11-30", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := o.ChangeStockSIP(ctx, T, "client.bh1001", ps.ID, "resume", "", "", ""); err != nil {
		t.Fatal(err)
	}
	c, err := o.ChangeStockSIP(ctx, T, "client.bh1001", ps.ID, "cancel", "", "", "no longer needed")
	if err != nil || c.Status != SIPCancelled || c.Consent.RevokedBy != "client.bh1001" {
		t.Fatalf("cancel %+v %v", c, err)
	}
	if _, err := o.ChangeStockSIP(ctx, T, "client.bh1001", ps.ID, "resume", "", "", ""); !errors.Is(err, ErrState) {
		t.Fatalf("resumed a cancelled SIP: %v", err)
	}
}

func TestStockSIPInstalmentsBasketAndFills(t *testing.T) {
	o, ctx := sipSetup(t, func(c *SIPSettings) { c.MaxFailures = 2 })
	// Sample trades of the day: BH1001 bought 36 RELIANCE in one order.
	ok := staffSIP(t, o, ctx, SIPRequest{UCC: "BH1001", Symbol: "RELIANCE", Basis: SIPQty, Qty: 36, Frequency: SIPDaily})
	amt := staffSIP(t, o, ctx, SIPRequest{UCC: "BH1002", Symbol: "INFY", Basis: SIPAmount, Amount: 1000000, Frequency: SIPDaily}) // ₹10,000: 6 at ₹1,500.20
	poor := staffSIP(t, o, ctx, SIPRequest{UCC: "BH1003", Symbol: "MARUTI", Basis: SIPQty, Qty: 50000, Frequency: SIPDaily})
	skip := staffSIP(t, o, ctx, SIPRequest{UCC: "BH1004", Symbol: "ITC", Basis: SIPQty, Qty: 10, Frequency: SIPDaily})
	paused := staffSIP(t, o, ctx, SIPRequest{UCC: "BH1005", Symbol: "SBIN", Basis: SIPQty, Qty: 10, Frequency: SIPDaily})
	if _, err := o.ChangeStockSIP(ctx, T, "priya.nair", skip.ID, "skip", "", D, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := o.ChangeStockSIP(ctx, T, "priya.nair", paused.ID, "pause", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := o.RunStockSIPs(ctx, T, "priya.nair", "2026-10-19"); err == nil {
		t.Fatal("instalments worked out for another day")
	}
	sipAt(o, D, "11:00")
	res, err := o.RunStockSIPs(ctx, T, "priya.nair", D)
	if err != nil {
		t.Fatal(err)
	}
	xs := sipOrders(t, o, ctx, D)
	if x := xs[ok.ID]; x.Status != SIPOrdPending || x.Qty != 36 || x.Limit.String() != "1428.85" || x.Estimate.Dec().Cmp(money.MustDec("51438")) < 0 {
		t.Fatalf("RELIANCE order %+v", x)
	}
	if x := xs[amt.ID]; x.Status != SIPOrdPending || x.Qty != 6 {
		t.Fatalf("INFY order: %d shares, %s %s", x.Qty, x.Status, x.Reason)
	}
	if x := xs[poor.ID]; x.Status != SIPOrdFailed || !strings.Contains(x.Reason, "not enough free funds") {
		t.Fatalf("no funds: %s %s", x.Status, x.Reason)
	}
	if xs[skip.ID].Status != SIPOrdSkipped || xs[paused.ID].Status != SIPOrdSkipped {
		t.Fatalf("skip %s, pause %s", xs[skip.ID].Status, xs[paused.ID].Status)
	}
	if res.Counts[SIPOrdPending] != 2 || !hasMessage(t, o, ctx, "BH1003", "stocksip.failed") {
		t.Fatalf("run %+v", res.Counts)
	}
	// Again: nothing new.
	again, _ := o.RunStockSIPs(ctx, T, "priya.nair", D)
	if len(again.Orders) != 0 {
		t.Fatalf("instalments worked out twice: %d", len(again.Orders))
	}
	// The basket file.
	sr, err := o.SendStockSIPOrders(ctx, T, "farhan.khan", D)
	if err != nil || sr.Placed != 2 || sr.FileName != "SIP_BASKET_20261016_01.csv" {
		t.Fatalf("send %+v %v", sr, err)
	}
	name, file, err := o.StockSIPBasket(ctx, T, "farhan.khan", sr.Batch)
	if err != nil || name != sr.FileName {
		t.Fatal(err)
	}
	spec, _ := sipBasketSpec("stocksip-basket")
	tab, err := spec.Read(name, file)
	if err != nil || len(tab.Issues) > 0 || len(tab.Rows) != 2 {
		t.Fatalf("basket file does not read back: %v %v\n%s", err, tab, file)
	}
	row := tab.Rows[0].Values
	if row["ucc"] != "BH1001" || row["symbol"] != "RELIANCE" || row["qty"] != "36" || row["price"] != "1428.85" || row["order_type"] != "LIMIT" || row["product"] != "CNC" || row["ref"] != xs[ok.ID].Ref {
		t.Fatalf("basket row %v", row)
	}
	if !hasMessage(t, o, ctx, "BH1001", "stocksip.placed") {
		t.Fatal("no placed message")
	}
	// Trades: the sample day's exchange file has BH1001's 36 RELIANCE.
	mr, err := o.MatchStockSIPFills(ctx, T, "farhan.khan", D, true)
	if err != nil {
		t.Fatal(err)
	}
	xs = sipOrders(t, o, ctx, D)
	if x := xs[ok.ID]; x.Status != SIPOrdFilled || x.FilledQty != 36 || x.AvgPrice.Cmp(money.Dec{}) <= 0 || len(x.Trades) == 0 || !strings.Contains(x.FillSrc, "exchange trade files") {
		t.Fatalf("fill %+v (%v)", x, mr.Counts)
	}
	if x := xs[amt.ID]; x.Status != SIPOrdLapsed {
		t.Fatalf("INFY order without trades: %s", x.Status)
	}
	if s, _, _ := docs.Get[StockSIP](ctx, o.Docs, T, cStockSIP, ok.ID); s.Done != 1 || s.Failures != 0 {
		t.Fatalf("bought SIP %+v", s)
	}
	if !hasMessage(t, o, ctx, "BH1001", "stocksip.filled") {
		t.Fatal("no filled message")
	}
	// The next day: BH1003 fails again and the SIP stops (limit 2).
	sipAt(o, "2026-10-19", "11:00")
	if _, err := o.RunStockSIPs(ctx, T, "priya.nair", "2026-10-19"); err != nil {
		t.Fatal(err)
	}
	if s, _, _ := docs.Get[StockSIP](ctx, o.Docs, T, cStockSIP, poor.ID); s.Status != SIPCancelled || s.Failures != 2 || s.Consent.RevokedAt == "" {
		t.Fatalf("SIP not stopped after 2 failures: %+v", s)
	}
	if !hasMessage(t, o, ctx, "BH1003", "stocksip.cancelled") {
		t.Fatal("no stopped message")
	}
	// Report: units, average cost.
	rows, failed, err := o.StockSIPReport(ctx, T, "", "2026-10-19")
	if err != nil || len(failed) < 3 {
		t.Fatalf("report failures %d %v", len(failed), err)
	}
	for _, r := range rows {
		if r.SIP == ok.ID && (r.Units != 36 || r.AvgCost.Cmp(xs[ok.ID].AvgPrice) != 0 || r.Bought != 1) {
			t.Fatalf("report row %+v", r)
		}
	}
	if b, err := o.StockSIPReportCSV(ctx, T, "2026-10-19"); err != nil || !strings.Contains(string(b), "Failed instalments") {
		t.Fatal("report CSV")
	}
	v, err := o.StockSIPView(ctx, T, "2026-10-19")
	if err != nil || len(v.SIPs) != 5 || len(v.Failures) == 0 || v.Lists["eligible"].Count != 7 || !v.RouterReady {
		t.Fatalf("screen %+v %v", v.Lists, err)
	}
}

func TestMatchSIPTrades(t *testing.T) {
	x := SIPOrder{UCC: "C1", Symbol: "ABC", Exchange: "NSE", Qty: 10}
	tr := func(no, ord string, q int64, prod, algo string) ingest.TradeEvent {
		return ingest.TradeEvent{Exchange: "NSE", Segment: "CM", TradeNo: no, OrderNo: ord, UCC: "C1", Symbol: "ABC", Side: "B", Qty: q, Price: money.MustDec("100"), TradeDate: D, Product: prod, AlgoID: algo}
	}
	// Two orders: the client's own 25, and the SIP's 10 in two trades.
	ts := []ingest.TradeEvent{tr("1", "A", 25, "CNC", ""), tr("2", "B", 4, "CNC", ""), tr("3", "B", 6, "", ""), tr("4", "C", 10, "MIS", "")}
	got, _ := matchSIPTrades(x, ts, map[string]bool{})
	if len(got) != 2 || got[0].OrderNo != "B" {
		t.Fatalf("matched %v", got)
	}
	// Taken by another SIP order: nothing fits; the client's 25 is not ours.
	got, note := matchSIPTrades(x, ts, map[string]bool{ts[1].Key(): true, ts[2].Key(): true})
	if len(got) != 0 || !strings.Contains(note, "check") {
		t.Fatalf("ambiguous %v %q", got, note)
	}
	// A single smaller order is a part fill.
	got, _ = matchSIPTrades(x, []ingest.TradeEvent{tr("5", "D", 7, "CNC", "")}, map[string]bool{})
	if len(got) != 1 || got[0].Qty != 7 {
		t.Fatalf("part fill %v", got)
	}
	// The algo ID picks the SIP's trades; the OMS order ID wins over all.
	x.AlgoID = "ALG1"
	got, _ = matchSIPTrades(x, []ingest.TradeEvent{tr("6", "E", 10, "CNC", ""), tr("7", "F", 10, "CNC", "ALG1")}, map[string]bool{})
	if len(got) != 1 || got[0].OrderNo != "F" {
		t.Fatalf("algo %v", got)
	}
	x.OMSOrder = "E"
	got, _ = matchSIPTrades(x, []ingest.TradeEvent{tr("6", "E", 10, "CNC", ""), tr("7", "F", 10, "CNC", "ALG1")}, map[string]bool{})
	if len(got) != 1 || got[0].OrderNo != "E" {
		t.Fatalf("OMS order ID %v", got)
	}
}

func TestXIRR(t *testing.T) {
	d := func(s string) time.Time { x, _ := time.Parse("2006-01-02", s); return x }
	// 10,000 grows to 11,000 in a year: 10%.
	r, ok := xirr([]float64{-10000, 11000}, []time.Time{d("2025-01-01"), d("2026-01-01")})
	if !ok || math.Abs(r-0.1) > 1e-6 {
		t.Fatalf("xirr %v %v", r, ok)
	}
	// Monthly 1,000 for a year, worth 12,700 at the end: about 12.6% a year.
	var fs []float64
	var ws []time.Time
	for m := 0; m < 12; m++ {
		fs, ws = append(fs, -1000), append(ws, time.Date(2025, time.Month(m+1), 5, 0, 0, 0, 0, time.UTC))
	}
	r, ok = xirr(append(fs, 12700), append(ws, d("2026-01-05")))
	if !ok || r < 0.10 || r > 0.15 {
		t.Fatalf("monthly xirr %v", r)
	}
	if _, ok := xirr([]float64{-1000}, []time.Time{d("2025-01-01")}); ok {
		t.Fatal("xirr of one flow")
	}
}

func TestStockSIPXTS(t *testing.T) {
	var mu sync.Mutex
	var orders []map[string]any
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/interactive/user/session":
			if m["appKey"] == "investor-key" {
				io.WriteString(w, `{"type":"success","code":"s-user-0001","description":"Valid User.","result":{"token":"tok-inv","userID":"BH1001","isInvestorClient":true}}`)
				return
			}
			if m["appKey"] != "app-key" || m["secretKey"] != "secret-key" || m["source"] != "WEBAPI" {
				w.WriteHeader(400)
				io.WriteString(w, `{"type":"error","code":"e-user-0001","description":"Invalid credentials"}`)
				return
			}
			io.WriteString(w, `{"type":"success","code":"s-user-0001","description":"Valid User.","result":{"token":"tok-123","userID":"DEALER1","isInvestorClient":false}}`)
		case "/interactive/orders":
			if r.Header.Get("Authorization") != "tok-123" {
				w.WriteHeader(400)
				io.WriteString(w, `{"type":"error","code":"e-session-0002","description":"Invalid Token"}`)
				return
			}
			mu.Lock()
			orders = append(orders, m)
			n := len(orders)
			mu.Unlock()
			io.WriteString(w, `{"type":"success","code":"s-orders-0001","description":"Request sent","result":{"AppOrderID":`+map[int]string{1: "1100001", 2: "1100002"}[n]+`,"OrderUniqueIdentifier":"`+m["orderUniqueIdentifier"].(string)+`"}}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	old := sipXTSHTTP
	sipXTSHTTP = srv.Client()
	defer func() { sipXTSHTTP = old }()

	o, ctx := sipSetup(t, func(c *SIPSettings) { c.Router = "xts" })
	if _, err := o.SaveConnection(ctx, T, "admin", connSIPXTS, map[string]string{"url": srv.URL}, map[string]string{"api_key": "app-key", "secret": "secret-key"}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.UploadStockSIPList(ctx, T, "vikram.shah", "tokens", "xts.csv", []byte("Exchange,Symbol,InstrumentID\nNSE,RELIANCE,2885\n")); err != nil {
		t.Fatal(err)
	}
	a := staffSIP(t, o, ctx, SIPRequest{UCC: "BH1001", Symbol: "RELIANCE", Basis: SIPQty, Qty: 3, Frequency: SIPDaily})
	b := staffSIP(t, o, ctx, SIPRequest{UCC: "BH1002", Symbol: "SBIN", Basis: SIPQty, Qty: 3, Frequency: SIPDaily})
	sipAt(o, D, "11:00")
	res, err := o.RunStockSIPs(ctx, T, "priya.nair", D)
	if err != nil || res.Sent == nil || res.Sent.Placed != 1 || res.Sent.Refused != 1 {
		t.Fatalf("run %+v %v", res, err)
	}
	xs := sipOrders(t, o, ctx, D)
	if x := xs[a.ID]; x.Status != SIPOrdPlaced || x.OMSOrder != "1100001" || x.Router != "Symphony XTS" {
		t.Fatalf("placed %+v", x)
	}
	if x := xs[b.ID]; x.Status != SIPOrdPending || !strings.Contains(x.Note, "no XTS instrument ID") {
		t.Fatalf("no instrument ID: %s %s", x.Status, x.Note)
	}
	m := orders[0]
	if m["exchangeSegment"] != "NSECM" || m["exchangeInstrumentID"] != float64(2885) || m["productType"] != "CNC" || m["orderType"] != "LIMIT" || m["orderSide"] != "BUY" ||
		m["orderQuantity"] != float64(3) || m["limitPrice"] != 1428.85 || m["clientID"] != "BH1001" || m["timeInForce"] != "DAY" || m["orderUniqueIdentifier"] != xs[a.ID].Ref {
		t.Fatalf("order sent %v", m)
	}
	// A refused login, and an investor login (which cannot place for clients).
	if _, err := (&xtsRouter{base: srv.URL, appKey: "bad", secret: "x"}).Route(ctx, nil); err == nil || !strings.Contains(err.Error(), "Invalid credentials") {
		t.Fatalf("bad login: %v", err)
	}
	if _, err := (&xtsRouter{base: srv.URL, appKey: "investor-key", secret: "x"}).Route(ctx, nil); err == nil || !strings.Contains(err.Error(), "dealer login") {
		t.Fatalf("investor login: %v", err)
	}
	// Other OMS: a clear refusal, orders keep waiting.
	if _, err := (otherRouter{vendor: "Finspot"}).Route(ctx, nil); err == nil || !strings.Contains(err.Error(), "not public") {
		t.Fatalf("other OMS: %v", err)
	}
}

// fakeCashfreeSubs is Cashfree's subscription API as its public pages describe it.
type fakeCashfreeSubs struct {
	mu     sync.Mutex
	subs   []map[string]any
	pays   []map[string]any
	header http.Header
}

func (f *fakeCashfreeSubs) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.header = r.Header.Clone()
	w.Header().Set("Content-Type", "application/json")
	if r.Header.Get("x-client-id") != "cf-id" || r.Header.Get("x-client-secret") != "cf-secret" {
		w.WriteHeader(401)
		io.WriteString(w, `{"message":"authentication failed","code":"request_failed"}`)
		return
	}
	switch r.URL.Path {
	case "/subscriptions":
		f.subs = append(f.subs, m)
		io.WriteString(w, `{"cf_subscription_id":"9001","subscription_id":"`+m["subscription_id"].(string)+`","subscription_session_id":"sub_session_abc","subscription_status":"INITIALIZED"}`)
	case "/subscriptions/pay":
		f.pays = append(f.pays, m)
		io.WriteString(w, `{"cf_payment_id":"77001","payment_id":"`+m["payment_id"].(string)+`","payment_status":"PENDING","payment_type":"CHARGE"}`)
	default:
		w.WriteHeader(404)
	}
}

func cfNotice(t *testing.T, o *Ops, ctx context.Context, body string) (string, error) {
	t.Helper()
	ts := "1760000000"
	h := http.Header{}
	h.Set("x-webhook-timestamp", ts)
	h.Set("x-webhook-signature", b64HMAC("cf-secret", []byte(ts), []byte(body)))
	return o.StockSIPPaymentHook(ctx, T, h, []byte(body))
}

func TestStockSIPTopups(t *testing.T) {
	f := &fakeCashfreeSubs{}
	srv := httptest.NewTLSServer(f)
	defer srv.Close()
	old := sipPayHTTP
	sipPayHTTP = srv.Client()
	defer func() { sipPayHTTP = old }()

	o, ctx := sipSetup(t, nil)
	if _, err := o.SaveConnection(ctx, T, "admin", connSIPMandate, map[string]string{"provider": "Cashfree", "client_id": "cf-id", "url": srv.URL}, map[string]string{"api_key": "cf-secret"}); err != nil {
		t.Fatal(err)
	}
	cfg := o.sipSettings(ctx, T)
	cfg.Topups, cfg.ProtectionPct = true, "0.5"
	if _, err := o.SaveStockSIPSettings(ctx, T, "vikram.shah", cfg); err != nil {
		t.Fatal(err)
	}
	// The client gives a mandate on their registered bank account.
	ms, err := o.StartStockSIPMandate(ctx, T, "priya.nair", "BH1005", "https://bo.example")
	if err != nil {
		t.Fatal(err)
	}
	if ms.Mandate.Status != "Awaiting authorisation" || !strings.HasPrefix(ms.AuthURL, "https://bo.example/hooks/demo/stocksip/mandate/") || ms.Mandate.Session != "" {
		t.Fatalf("mandate %+v", ms)
	}
	c, _ := o.Client(ctx, T, "BH1005")
	cd := f.subs[0]["customer_details"].(map[string]any)
	pd := f.subs[0]["plan_details"].(map[string]any)
	if cd["customer_bank_account_number"] != c.Bank.Account || pd["plan_type"] != "ON_DEMAND" || pd["plan_max_amount"] != float64(15000) || f.header.Get("x-api-version") != cashfreeSubsVersion {
		t.Fatalf("subscription request %v %v", f.subs[0], f.header)
	}
	page, err := o.StockSIPMandatePage(ctx, T, strings.TrimPrefix(ms.AuthURL, "https://bo.example/hooks/demo/stocksip/mandate/"))
	if err != nil || page.Session != "sub_session_abc" {
		t.Fatalf("authorisation page %+v %v", page, err)
	}
	m, _, _ := docs.Get[SIPMandate](ctx, o.Docs, T, cSIPMandate, "BH1005")
	if _, err := cfNotice(t, o, ctx, `{"type":"SUBSCRIPTION_STATUS_CHANGED","data":{"subscription_details":{"subscription_id":"`+m.SubscriptionID+`","subscription_status":"ACTIVE"},"authorization_details":{"authorization_status":"ACTIVE"}}}`); err != nil {
		t.Fatal(err)
	}
	if m, _, _ = docs.Get[SIPMandate](ctx, o.Docs, T, cSIPMandate, "BH1005"); m.Status != "Active" {
		t.Fatalf("mandate status %s", m.Status)
	}
	h := http.Header{}
	h.Set("x-webhook-timestamp", "1")
	h.Set("x-webhook-signature", "forged")
	if _, err := o.StockSIPPaymentHook(ctx, T, h, []byte(`{"type":"SUBSCRIPTION_PAYMENT_SUCCESS"}`)); !errors.Is(err, ErrForbidden) {
		t.Fatalf("forged notice accepted: %v", err)
	}
	// SIPs from Monday 19 Oct; BH1005's instalment on Tuesday 20 Oct is a
	// little more than its free funds, BH1006's far more.
	free, _ := o.sipFreeFunds(ctx, T, "2026-10-18")
	short := staffSIP(t, o, ctx, SIPRequest{UCC: "BH1005", Symbol: "RELIANCE", Basis: SIPAmount, Amount: free["BH1005"].Add(money.DecInt(3000)).Round(money.UnitRupee), Frequency: SIPDaily, Start: "2026-10-20"})
	_ = staffSIP(t, o, ctx, SIPRequest{UCC: "BH1006", Symbol: "RELIANCE", Basis: SIPAmount, Amount: free["BH1006"].Add(money.DecInt(90000)).Round(money.UnitRupee), Frequency: SIPDaily, Start: "2026-10-20"})
	if _, err := docs.Put(ctx, o.Docs, T, cSIPMandate, "BH1006", SIPMandate{UCC: "BH1006", SubscriptionID: "LLSIP-BH1006-9", Status: "Active", MaxAmount: sipTopupLimit}, 0, "test"); err != nil {
		t.Fatal(err)
	}
	sipAt(o, "2026-10-19", "10:00")
	if _, err := o.RaiseStockSIPTopups(ctx, T, "priya.nair", "2026-10-20"); err == nil || !strings.Contains(err.Error(), "24 hours") {
		t.Fatalf("debit tomorrow without a day's notice: %v", err)
	}
	// Sunday 18 Oct, 10:00: the scheduler raises Monday's debits for Tuesday.
	sipAt(o, "2026-10-18", "10:00")
	o.stockSIPTick(ctx, T, o.Now())
	tops := o.StockSIPTopups(ctx, T, 0)
	if len(tops) != 2 {
		t.Fatalf("top-ups %+v", tops)
	}
	var tp, big SIPTopup
	for _, x := range tops {
		if x.UCC == "BH1005" {
			tp = x
		} else {
			big = x
		}
	}
	if tp.Status != "Scheduled" || tp.DebitDate != "2026-10-19" || tp.Amount <= 300000 || tp.Amount > sipTopupLimit || tp.CFPayment != "77001" {
		t.Fatalf("top-up %+v", tp)
	}
	if big.Status != "Not raised" || !strings.Contains(big.Reason, "additional authentication") {
		t.Fatalf("over the cap %+v", big)
	}
	p := f.pays[0]
	if len(f.pays) != 1 || p["payment_schedule_date"] != "2026-10-19" || p["payment_type"] != "CHARGE" || p["subscription_id"] != m.SubscriptionID || p["payment_amount"] != tp.Amount.Dec().Float64() {
		t.Fatalf("charge %v", f.pays)
	}
	if !hasMessage(t, o, ctx, "BH1005", "stocksip.predebit") {
		t.Fatal("no pre-debit notice")
	}
	// Tuesday 11:00, before Cashfree reports: the debit on its way counts.
	sipAt(o, "2026-10-20", "11:00")
	if _, err := o.RunStockSIPs(ctx, T, "priya.nair", "2026-10-20"); err != nil {
		t.Fatal(err)
	}
	if x := sipOrders(t, o, ctx, "2026-10-20")[short.ID]; x.Status != SIPOrdPending || x.Topup != tp.Amount {
		t.Fatalf("instalment with a top-up on its way: %s %s %v", x.Status, x.Reason, x.Topup)
	}
	// Cashfree reports the debit: a receipt against the gateway receivable.
	body := `{"type":"SUBSCRIPTION_PAYMENT_SUCCESS","event_time":"2026-10-19T11:30:00+05:30","data":{"payment_id":"` + tp.PaymentID + `","cf_payment_id":"77001","subscription_id":"` + m.SubscriptionID + `","payment_amount":` + tp.Amount.String() + `,"payment_status":"SUCCESS","payment_type":"CHARGE"}}`
	if r, err := cfNotice(t, o, ctx, body); err != nil || !strings.HasPrefix(r, "credited GW-CF-77001") {
		t.Fatalf("debit notice %q %v", r, err)
	}
	if r, _ := cfNotice(t, o, ctx, body); !strings.HasPrefix(r, "ignored") {
		t.Fatalf("credited twice: %q", r)
	}
	rc, _, err := docs.Get[Receipt](ctx, o.Docs, T, cReceipt, "GW-CF-77001")
	if err != nil || rc.Amount != tp.Amount || rc.Date != "2026-10-19" || rc.UCC != "BH1005" {
		t.Fatalf("receipt %+v %v", rc, err)
	}
	e, err := o.App.Ledger.Store.List(ctx, T, ledger.Filter{From: "2026-10-19", To: "2026-10-19"})
	found := false
	for _, x := range e {
		if x.Source.ID == rc.ID {
			for _, p := range x.Postings {
				found = found || (p.Account == ledger.ClientAccount("BH1005") && p.Credit == tp.Amount)
			}
		}
	}
	if err != nil || !found {
		t.Fatal("client not credited")
	}
	if tps := o.StockSIPTopups(ctx, T, 0); tps[0].Status != "Success" && tps[1].Status != "Success" {
		t.Fatal("top-up not marked paid")
	}
	// Gateway settlement into the client bank account clears it.
	if n, _, err := o.SettleGateway(ctx, T, "farhan.khan", "2026-10-21", "CF-SETTLE-1", 0); err != nil || n != 1 {
		t.Fatalf("settlement %d %v", n, err)
	}
}

func TestStockSIPTick(t *testing.T) {
	o, ctx := sipSetup(t, nil)
	staffSIP(t, o, ctx, SIPRequest{UCC: "BH1001", Symbol: "RELIANCE", Basis: SIPQty, Qty: 36, Frequency: SIPDaily})
	sipAt(o, D, "10:59")
	o.stockSIPTick(ctx, T, o.Now())
	if len(sipOrders(t, o, ctx, D)) != 0 {
		t.Fatal("orders before the placement time")
	}
	sipAt(o, D, "11:01")
	o.stockSIPTick(ctx, T, o.Now())
	o.stockSIPTick(ctx, T, o.Now())
	xs := sipOrders(t, o, ctx, D)
	if len(xs) != 1 {
		t.Fatalf("orders %d", len(xs))
	}
	if _, err := o.SendStockSIPOrders(ctx, T, "farhan.khan", D); err != nil {
		t.Fatal(err)
	}
	sipAt(o, D, "17:30")
	o.stockSIPTick(ctx, T, o.Now())
	for _, x := range sipOrders(t, o, ctx, D) {
		if x.Status != SIPOrdFilled {
			t.Fatalf("tick did not match the fill: %s", x.Status)
		}
	}
	od, _, _ := docs.Get[sipOpenDates](ctx, o.Docs, T, cSIPRun, "_open")
	if contains(od.Dates, D) {
		t.Fatal("day still open")
	}
	// Switched off: the tick does nothing.
	cfg := o.sipSettings(ctx, T)
	cfg.Enabled = false
	_, _ = o.SaveStockSIPSettings(ctx, T, "vikram.shah", cfg)
	sipAt(o, "2026-10-19", "11:30")
	o.stockSIPTick(ctx, T, o.Now())
	if len(sipOrders(t, o, ctx, "2026-10-19")) != 0 {
		t.Fatal("orders while switched off")
	}
}

func TestStockSIPWithdrawAndRecordFill(t *testing.T) {
	o, ctx := sipSetup(t, nil)
	a := staffSIP(t, o, ctx, SIPRequest{UCC: "BH1002", Symbol: "INFY", Basis: SIPQty, Qty: 5, Frequency: SIPDaily})
	b := staffSIP(t, o, ctx, SIPRequest{UCC: "BH1004", Symbol: "ITC", Basis: SIPQty, Qty: 50, Frequency: SIPDaily})
	sipAt(o, D, "11:00")
	if _, err := o.RunStockSIPs(ctx, T, "priya.nair", D); err != nil {
		t.Fatal(err)
	}
	s1, err := o.SendStockSIPOrders(ctx, T, "farhan.khan", D)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.WithdrawStockSIPBasket(ctx, T, "farhan.khan", s1.Batch, ""); err == nil {
		t.Fatal("withdrawn without a reason")
	}
	if _, err := o.WithdrawStockSIPBasket(ctx, T, "farhan.khan", s1.Batch, "OMS upload screen down"); err != nil {
		t.Fatal(err)
	}
	if x := sipOrders(t, o, ctx, D)[a.ID]; x.Status != SIPOrdPending || x.Batch != "" {
		t.Fatalf("after withdrawal %s %s", x.Status, x.Batch)
	}
	s2, err := o.SendStockSIPOrders(ctx, T, "farhan.khan", D)
	if err != nil || s2.FileName != "SIP_BASKET_20261016_02.csv" {
		t.Fatalf("second basket %+v %v", s2, err)
	}
	// The dealer confirms from the OMS order book that ITC was bought.
	xb := sipOrders(t, o, ctx, D)[b.ID]
	if _, err := o.RecordStockSIPFill(ctx, T, "farhan.khan", xb.ID, 50, money.MustDec("416.20"), ""); err == nil {
		t.Fatal("fill recorded without saying what was checked")
	}
	x, err := o.RecordStockSIPFill(ctx, T, "farhan.khan", xb.ID, 50, money.MustDec("416.20"), "OMS order book, order 77")
	if err != nil || x.Status != SIPOrdFilled || !x.Counted {
		t.Fatalf("recorded fill %+v %v", x, err)
	}
	if s, _, _ := docs.Get[StockSIP](ctx, o.Docs, T, cStockSIP, b.ID); s.Done != 1 {
		t.Fatalf("bought instalment not counted: %+v", s)
	}
	// INFY has no trades: it lapses at the end of the day and counts once.
	for i := 0; i < 2; i++ {
		if _, err := o.MatchStockSIPFills(ctx, T, "farhan.khan", D, true); err != nil {
			t.Fatal(err)
		}
	}
	if s, _, _ := docs.Get[StockSIP](ctx, o.Docs, T, cStockSIP, a.ID); s.Failures != 1 || s.Failed != 1 {
		t.Fatalf("lapse counted %d times", s.Failed)
	}
}
