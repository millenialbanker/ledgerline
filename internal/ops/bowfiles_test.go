package ops

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
	"ledgerline/internal/xfile"
)

// readBOW reads a BOW upload with its layout: the name matches, no header
// or title line, every line CRLF with exactly the layout's fields, and no
// issues.
func readBOW(t *testing.T, id, name string, b []byte) *xfile.Table {
	t.Helper()
	lib, _ := XFiles()
	s, ok := lib.Get(id)
	if !ok {
		t.Fatalf("no layout %s", id)
	}
	if !s.MatchesName(name) {
		t.Fatalf("%s does not match %s's names", name, id)
	}
	if len(b) > 0 && !strings.HasSuffix(string(b), "\r\n") {
		t.Fatalf("%s: no final CRLF", id)
	}
	for i, l := range strings.Split(strings.TrimSuffix(string(b), "\r\n"), "\r\n") {
		if len(b) == 0 {
			break
		}
		if strings.Contains(l, "\n") || strings.Count(l, ",") != len(s.Columns)-1 {
			t.Fatalf("%s line %d: %q, want %d fields", id, i+1, l, len(s.Columns))
		}
	}
	tb, err := s.Read(name, b)
	if err != nil || len(tb.Issues) != 0 {
		t.Fatalf("%s: %v %v", id, err, tb.Issues)
	}
	if same, diff, err := s.RoundTrip(name, b); err != nil || !same {
		t.Fatalf("%s round trip: %s %v", id, diff, err)
	}
	return tb
}

func TestBOWRupees(t *testing.T) {
	for in, want := range map[string]int64{"0": 0, "1234.60": 1234, "1234.99": 1234, "70000": 70000, "-2500": -2500, "-10.40": -11, "-0.01": -1} {
		if got := bowRupees(mustDec(in)); got != want {
			t.Errorf("%s: %d, want %d", in, got, want)
		}
	}
}

// The funds file: every active client, one figure (Securities +
// Commodities) in whole rupees, debits kept, blocked and inactive 0; then
// the file of changed clients only.
func TestBOWFundFile(t *testing.T) {
	o, ctx := newOps(t)
	o.Now = func() time.Time { return time.Date(2026, 10, 16, 8, 13, 39, 0, ist) }
	for _, c := range []Client{
		{UCC: "ON01", Name: "One", Status: "Active", Segments: []string{"CM", "COM"}},
		{UCC: "ON02", Name: "Two", Status: "Active", Segments: []string{"CM"}},
		{UCC: "ON03", Name: "Three", Status: "Active", Segments: []string{"CM"}, Inactive: true},
		{UCC: "ON04", Name: "Four", Status: "Active", Segments: []string{"CM"}, Blocked: true, BlockedReason: "Debit"},
		{UCC: "ON05", Name: "Five", Status: "Active", Segments: []string{"CM"}},
	} {
		if err := docs.Upsert(ctx, o.Docs, T, cClient, c.UCC, c, "t"); err != nil {
			t.Fatal(err)
		}
	}
	csv := "Account,Debit,Credit\nCLIENT:ON01,,70000.00\nCLIENT:ON02,2500.00,\nCLIENT:ON03,,900.00\nCLIENT:ON04,,5000.00\nCLIENT:ON05,,1234.60\nEQUITY:OPENING,74634.60,\n"
	if _, err := o.LoadFile(ctx, T, "admin", "OPENING-BALANCES", "2026-10-15", "opening.csv", []byte(csv)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := o.BOWFundFile(ctx, T, "vikram.shah", D, false); err == nil {
		t.Fatal("compliance may not download the BOW funds file")
	}
	name, b, err := o.BOWFile(ctx, T, "anita.rao", "bow-funds-file", D, false)
	if err != nil {
		t.Fatal(err)
	}
	if name != "BOW_FUND_16-10-26 08_13_39 AM.csv" {
		t.Fatalf("name %q", name)
	}
	tb := readBOW(t, "bow-fund", name, b)
	lims, _ := o.bowLimits(ctx, T, D)
	cs, _ := o.ClientMap(ctx, T)
	active := 0
	for _, c := range cs {
		if c.Status == "Active" {
			active++
		}
	}
	got := map[string]string{}
	for _, r := range tb.Rows {
		v := r.Values
		got[v["ClientCode"]] = v["CashDeposit"]
		want := map[string]string{"Const6": "0", "Const7": "0", "Const8": "0", "Const10": "1", "Const11": "0", "Const16": "1", "Const17": "1", "Const18": "0", "Const19": "1", "Const20": "Y"}
		for k, x := range want {
			if v[k] != x {
				t.Fatalf("%s %s = %q", v["ClientCode"], k, v[k])
			}
		}
		for _, k := range []string{"Blank2", "Blank3", "Blank4", "Blank9", "Blank12", "Blank13", "Blank14", "Blank15"} {
			if v[k] != "" {
				t.Fatalf("%s %s = %q", v["ClientCode"], k, v[k])
			}
		}
		l := lims[v["ClientCode"]]
		if l.Why == "" && l.Securities.Add(l.Commodities).Sign() > 0 && v["CashDeposit"] != strconv.FormatInt(bowRupees(l.Securities.Add(l.Commodities)), 10) {
			t.Fatalf("%s: %s, Securities %s + Commodities %s", v["ClientCode"], v["CashDeposit"], l.Securities, l.Commodities)
		}
	}
	if len(tb.Rows) != active {
		t.Fatalf("%d lines, %d active clients", len(tb.Rows), active)
	}
	for u, w := range map[string]string{"ON01": "70000", "ON02": "-2500", "ON03": "0", "ON04": "0", "ON05": "1234"} {
		if got[u] != w {
			t.Errorf("%s: %q, want %s", u, got[u], w)
		}
	}
	// Nothing changed: the changed file is empty and not on the checklist.
	o.Now = func() time.Time { return time.Date(2026, 10, 16, 10, 5, 2, 0, ist) }
	name2, b2, err := o.BOWFundFile(ctx, T, "anita.rao", D, true)
	if err != nil || len(b2) != 0 || name2 != "BOW_FUND_16-10-26 10_05_02 AM.csv" {
		t.Fatalf("changed file %q %q %v", name2, b2, err)
	}
	// ON01 is blocked and ON05 closed: both go to 0, and only they are in
	// the changed file; ON05 is not in the next full file.
	docs.Update(ctx, o.Docs, T, cClient, "ON01", "t", func(c *Client) error { c.Blocked = true; return nil })
	docs.Update(ctx, o.Docs, T, cClient, "ON05", "t", func(c *Client) error { c.Status = "Closed"; return nil })
	name2, b2, err = o.BOWFile(ctx, T, "priya.nair", "bow-funds-file", D, true)
	if err != nil {
		t.Fatal(err)
	}
	tb = readBOW(t, "bow-fund", name2, b2)
	if len(tb.Rows) != 2 || tb.Rows[0].Values["ClientCode"] != "ON01" || tb.Rows[0].Values["CashDeposit"] != "0" || tb.Rows[1].Values["ClientCode"] != "ON05" || tb.Rows[1].Values["CashDeposit"] != "0" {
		t.Fatalf("changed file %q", b2)
	}
	if _, b2, _ = o.BOWFundFile(ctx, T, "anita.rao", D, true); len(b2) != 0 {
		t.Fatalf("changed again %q", b2)
	}
	_, b, _ = o.BOWFundFile(ctx, T, "anita.rao", D, false)
	if strings.Contains(string(b), "ON05,") || !strings.Contains(string(b), "ON01,,,,0,") || !strings.Contains(string(b), "ON02,,,,-2500,") {
		t.Fatalf("full file %q", b)
	}
}

// With an MCX-only bank account the one figure is Securities + Commodities.
func TestBOWFundFileEarmark(t *testing.T) {
	o, ctx := emSetup(t, true)
	o.Now = func() time.Time { return time.Date(2026, 10, 30, 8, 13, 0, 0, ist) }
	next := "2026-10-30"
	lims, _ := o.bowLimits(ctx, T, next)
	if l := lims["BJ01"]; l.Securities.Cmp(mustDec("10000")) != 0 || l.Commodities.Cmp(mustDec("200000")) != 0 {
		t.Fatalf("limits %+v", l)
	}
	name, b, err := o.BOWFundFile(ctx, T, "anita.rao", next, false)
	if err != nil {
		t.Fatal(err)
	}
	tb := readBOW(t, "bow-fund", name, b)
	got := map[string]string{}
	for _, r := range tb.Rows {
		got[r.Values["ClientCode"]] = r.Values["CashDeposit"]
	}
	if got["BJ01"] != "210000" || got["BJ02"] != "50000" {
		t.Fatalf("funds %v", got)
	}
}

// The holdings file: DP quantity as Finspot's ISIN Holding, pool quantity
// the previous day's delivery buys (as Finspot's T1 file), average prices,
// NSE symbol and series or the BSE code; nothing without a quantity, and
// nothing for a security in neither master.
func TestBOWHoldingFile(t *testing.T) {
	o, ctx := setup(t)
	set, _ := o.Settings(ctx, T)
	next := set.nextBusinessDay(D)
	// Finspot's figures for the same day.
	fn, fb, err := o.FinspotHoldingFile(ctx, T, next, false)
	if err != nil {
		t.Fatal(err)
	}
	dp := map[string]string{}
	for _, r := range readFinspot(t, "finspot-dp-holding", fn, fb).Rows {
		dp[r.Values["AccountId"]+"|"+r.Values["ISIN"]] = r.Values["HoldingsQty"]
	}
	ts, _ := o.exchangeTrades(ctx, T, D)
	_, look, _ := o.Market(ctx, T, D)
	pool := map[string]int64{}
	bought := map[string]int64{}
	val := map[string]money.Dec{}
	for _, tr := range ts {
		i, ok := look.ForTrade(tr)
		if tr.Segment != "CM" || tr.Product != "CNC" || !ok || i.ISIN == "" {
			continue
		}
		k := tr.UCC + "|" + i.ISIN
		if tr.Side == "B" {
			pool[k] += tr.Qty
			bought[k] += tr.Qty
			val[k] = val[k].Add(tr.Price.MulInt(tr.Qty))
		} else {
			pool[k] -= tr.Qty
		}
	}
	// A holding with a known average cost.
	hs, _ := o.Holdings(ctx, T, "")
	var h Holding
	for _, x := range hs {
		if dp[x.UCC+"|"+x.ISIN] != "" && dp[x.UCC+"|"+x.ISIN] != "0" {
			h = x
			break
		}
	}
	if h.UCC == "" {
		t.Fatal("no holding in the sample")
	}
	docs.Update(ctx, o.Docs, T, cHolding, holdingID(h.UCC, h.ISIN), "t", func(x *Holding) error { x.AvgCost = mustDec("1043.255"); return nil })
	// The masters: every ISIN gets a symbol (NSE series EQ) and a BSE code;
	// one has only a BSE code and one is in neither.
	isins := map[string]bool{}
	for k := range dp {
		isins[strings.SplitN(k, "|", 2)[1]] = true
	}
	for k, q := range pool {
		if q > 0 {
			isins[strings.SplitN(k, "|", 2)[1]] = true
		}
	}
	var poolKey string
	for _, k := range sortedKeys(pool) {
		if pool[k] > 0 {
			poolKey = k
			break
		}
	}
	if poolKey == "" {
		t.Fatal("no delivery buys in the sample")
	}
	poolISIN := strings.SplitN(poolKey, "|", 2)[1]
	byCode := map[string]string{} // symbol or BSE code -> ISIN
	var bseOnly, none string
	for n, isin := range sortedKeys(isins) {
		ref := SecRef{ISIN: isin, Symbol: fmt.Sprintf("TSYM%d", n), Series: "EQ", BSECode: fmt.Sprintf("5%05d", n)}
		switch {
		case isin == h.ISIN || isin == poolISIN:
		case bseOnly == "":
			bseOnly, ref.Symbol, ref.Series = isin, "", ""
		case none == "":
			none, ref = isin, SecRef{ISIN: isin, Symbol: "APPLSYM"} // an applicable symbol, no series, no BSE code
		}
		docs.Upsert(ctx, o.Docs, T, cSecRef, isin, ref, "t")
		byCode[firstOf(ref.Symbol, ref.BSECode)] = isin
	}
	if none == "" {
		t.Fatal("too few securities in the sample")
	}
	name, b, err := o.BOWFile(ctx, T, "anita.rao", "bow-holdings", next, false)
	if err != nil {
		t.Fatal(err)
	}
	if name != "BOW_DP_Holding_Combined_Upload_19102026.TXT" {
		t.Fatalf("name %q", name)
	}
	tb := readBOW(t, "bow-dp-holding", name, b)
	seen := map[string]bool{}
	poolLines, costLine, bseLine := 0, false, false
	for _, r := range tb.Rows {
		v := r.Values
		isin := byCode[firstOf(v["Symbol"], v["BSECode"])]
		k := v["ClientCode"] + "|" + isin
		if isin == "" || seen[k] {
			t.Fatalf("line %+v: unknown or repeated", v)
		}
		seen[k] = true
		if isin == none {
			t.Fatalf("a security in neither master is written: %+v", v)
		}
		if (v["Symbol"] == "") != (v["Series"] == "") || v["DPCFSHaircutPct"] != "0" || v["PoolCFSHaircutPct"] != "0" {
			t.Fatalf("%+v", v)
		}
		wantDP := dp[k]
		if wantDP == "" {
			wantDP = "0"
		}
		if v["DPQty"] != wantDP {
			t.Fatalf("%s DP %s, Finspot %s", k, v["DPQty"], wantDP)
		}
		wantPool := max(pool[k], 0)
		if v["PoolQty"] != fmt.Sprintf("%d.00", wantPool) {
			t.Fatalf("%s pool %s, want %d", k, v["PoolQty"], wantPool)
		}
		if v["DPQty"] == "0" && wantPool == 0 {
			t.Fatalf("a line with no quantity: %+v", v)
		}
		if wantPool > 0 {
			poolLines++
			avg, _ := val[k].Quo(money.DecInt(bought[k]))
			if v["PoolAvgPrice"] != avg.Fixed(2) {
				t.Fatalf("%s pool avg %s, want %s", k, v["PoolAvgPrice"], avg.Fixed(2))
			}
		} else if v["PoolAvgPrice"] != "0.00" {
			t.Fatalf("%+v", v)
		}
		if k == h.UCC+"|"+h.ISIN {
			costLine = v["DPAvgPrice"] == "1043.26" && v["Series"] == "EQ"
		}
		if isin == bseOnly {
			bseLine = v["Symbol"] == "" && v["BSECode"] != ""
		}
	}
	if poolLines == 0 || !costLine || !bseLine {
		t.Fatalf("pool lines %d, average cost line %v, BSE-only line %v", poolLines, costLine, bseLine)
	}
	// Every Finspot holding and T1 buy of a security in a master is there.
	for k, q := range dp {
		if q != "0" && byCode != nil && !seen[k] && !strings.HasSuffix(k, "|"+none) {
			t.Fatalf("%s (%s) missing", k, q)
		}
	}
	// The firm's credit-for-sale haircuts.
	if _, err := o.SaveConnection(ctx, T, "admin", connBOWFiles, map[string]string{"dp_haircut": "10", "pool_haircut": "100"}, nil); err != nil {
		t.Fatal(err)
	}
	_, b, _ = o.BOWHoldingFile(ctx, T, next)
	if tb = readBOW(t, "bow-dp-holding", name, b); tb.Rows[0].Values["DPCFSHaircutPct"] != "10" || tb.Rows[0].Values["PoolCFSHaircutPct"] != "100" {
		t.Fatalf("haircuts %+v", tb.Rows[0].Values)
	}
}

// The checklist lists the BOW files only for a firm on BOW.
func TestBOWFilesChecklist(t *testing.T) {
	o, ctx := newOps(t)
	o.Now = func() time.Time { return time.Date(2026, 10, 16, 8, 30, 0, 0, ist) }
	bowItems := func() map[string]string {
		cl, err := o.DailyFileChecklist(ctx, T, D)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]string{}
		for _, it := range cl.Items {
			if it.Party == "BOW" {
				got[it.ID] = it.Status + " " + it.Due
			}
		}
		return got
	}
	if got := bowItems(); len(got) != 0 {
		t.Fatalf("BOW files listed without a BOW connection: %v", got)
	}
	if _, err := o.SaveConnection(ctx, T, "admin", connBOWFiles, map[string]string{"dp_haircut": "0"}, nil); err != nil {
		t.Fatal(err)
	}
	if got := bowItems(); got["bow-fund"] != "Missing "+D+" 08:13" || got["bow-holding"] != "Missing "+D+" 08:17" {
		t.Fatalf("checklist %v", got)
	}
	if _, _, err := o.BOWFundFile(ctx, T, "anita.rao", D, true); err != nil {
		t.Fatal(err)
	}
	if got := bowItems(); got["bow-fund"] == "Written "+D+" 08:13" {
		t.Fatal("the changed-clients file ticks off the morning funds file")
	}
	o.BOWFundFile(ctx, T, "anita.rao", D, false)
	o.BOWHoldingFile(ctx, T, D)
	if got := bowItems(); got["bow-fund"] != "Written "+D+" 08:13" || got["bow-holding"] != "Written "+D+" 08:17" {
		t.Fatalf("checklist %v", got)
	}
	if _, err := o.SaveConnection(ctx, T, "admin", connBOWFiles, map[string]string{"dp_haircut": "120"}, nil); err == nil {
		if _, _, err := o.BOWHoldingFile(ctx, T, D); err == nil {
			t.Fatal("a haircut over 100% was taken")
		}
	}
}
