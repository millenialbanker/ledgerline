package ops

import (
	"strconv"
	"strings"
	"testing"

	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
	"ledgerline/internal/xfile"
)

// readFinspot reads a Finspot file back through its layout: no issues and
// the title line first.
func readFinspot(t *testing.T, id, name string, b []byte) *xfile.Table {
	t.Helper()
	lib, _ := XFiles()
	s, ok := lib.Get(id)
	if !ok {
		t.Fatalf("no layout %s", id)
	}
	if !s.MatchesName(name) {
		t.Fatalf("%s does not match %s's names", name, id)
	}
	tb, err := s.Read(name, b)
	if err != nil || len(tb.Issues) != 0 {
		t.Fatalf("%s: %v %v", id, err, tb.Issues)
	}
	if !strings.HasPrefix(string(b), s.TitleLine+"\r\n") {
		l, _, _ := strings.Cut(string(b), "\r\n")
		t.Fatalf("%s: title line %q", id, l)
	}
	return tb
}

func mustDec(s string) money.Dec { return money.MustDec(s) }

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// SharePro's five-field RMS limit lines read with the 54-column layout
// with no issues and are written back as they came.
func TestFinspotLimitLayoutReadsShareProLines(t *testing.T) {
	lib, _ := XFiles()
	s, _ := lib.Get("finspot-rms-limit")
	if len(s.Columns) != 54 {
		t.Fatalf("%d columns", len(s.Columns))
	}
	f := []byte("RMS Limits\r\nAB123||||25000.00\r\nZX9|NSE|CASH|CNC|0.00\r\n")
	tb, err := s.Read("FINSPOT_RMS_LIMIT_06-11-26 08_50_01 AM.txt", f)
	if err != nil || len(tb.Issues) != 0 || len(tb.Rows) != 2 || tb.Rows[0].Values["CollateralMarginValue"] != "25000.00" || tb.Rows[1].Values["Product"] != "CNC" {
		t.Fatalf("%v %v %+v", err, tb.Issues, tb.Rows)
	}
	if same, diff, err := s.RoundTrip("FINSPOT_RMS_LIMIT_x.txt", f); !same || err != nil {
		t.Fatalf("round trip: %s %v", diff, err)
	}
	// The broker's worked example: ledger -20,000, cash-equivalent
	// collateral 1,50,000 (non-cash 4,75,000 goes in the ISIN Holding file).
	l := finspotLimit{ID: "D224-BJT", Margin: mustDec("-20000"), NotionalCash: mustDec("150000")}
	b, _ := s.Write([]xfile.Row{finspotLimitRow(finspotCfg{Suffix: "-BJT"}, l, l.Margin)})
	if string(b) != "RMS Limits\r\nD224-BJT||||-20000.00||||||||||||no||||||||150000.00\r\n" {
		t.Fatalf("%q", b)
	}
	tb, err = s.Read("FINSPOT_RMS_LIMIT_x.txt", b)
	if err != nil || len(tb.Issues) != 0 || tb.Rows[0].Values["NotionalCash"] != "150000.00" || tb.Rows[0].Values["GroupValidation"] != "no" {
		t.Fatalf("%v %v %+v", err, tb.Issues, tb.Rows)
	}
}

// Government securities (by ISIN or NSE series) and the firm's listed
// ISINs are cash equivalent; equities are not.
func TestCashEquivalentISINs(t *testing.T) {
	o, ctx := setup(t)
	if !isGovtISIN("IN0020990011") || !isGovtISIN("IN2920990019") || isGovtISIN("INE000Z01011") || isGovtISIN("INF000Z01019") {
		t.Fatal("government ISINs")
	}
	if _, bad := cashEqISINList("INF000Z01019, xyz"); len(bad) != 1 || bad[0] != "XYZ" {
		t.Fatalf("bad %v", bad)
	}
	if _, err := o.SaveConnection(ctx, T, "admin", connFinspot, map[string]string{"cash_eq_isins": "INF000Z01019"}, nil); err != nil {
		t.Fatal(err)
	}
	docs.Upsert(ctx, o.Docs, T, cSecRef, "INE000Z01029", SecRef{ISIN: "INE000Z01029", Series: "GS"}, "test")
	got := o.cashEquivalentISINs(ctx, T, []string{"IN0020990011", "INF000Z01019", "INE000Z01029", "INE000Z01011", "INF000Z01027"})
	if !got["IN0020990011"] || !got["INF000Z01019"] || !got["INE000Z01029"] || got["INE000Z01011"] || got["INF000Z01027"] {
		t.Fatalf("%v", got)
	}
	cfg, _ := o.finspotConfig(ctx, T)
	if cfg.GroupValidation != "no" {
		t.Fatal(cfg.GroupValidation)
	}
	for _, v := range []map[string]string{{"cash_eq_isins": "LIQUIDBEES"}, {"group_validation": "maybe"}, {"account_suffix": "-B T"}} {
		o.SaveConnection(ctx, T, "admin", connFinspot, v, nil)
		if _, err := o.finspotConfig(ctx, T); err == nil {
			t.Fatalf("%v accepted", v)
		}
	}
}

// The broker's D224 example end to end: ledger -20,000; pledged G-sec and
// a listed liquid fund worth 1,50,000 after haircut (cash equivalent, in
// Notional cash); pledged shares worth 4,75,000 after haircut (non-cash:
// collateral in the ISIN Holding file, not in the limit line). The suffix
// is on every Finspot file's client IDs.
func TestFinspotD224Example(t *testing.T) {
	o, ctx := setup(t)
	set, _ := o.Settings(ctx, T)
	next := set.nextBusinessDay(D)
	cs, _ := o.Clients(ctx, T)
	var base Client
	for _, c := range cs {
		if c.Status == "Active" && finspotBlockWhy(c) == "" {
			base = c
			break
		}
	}
	c := base
	c.UCC, c.Name = "D224", "Example Client"
	if err := docs.Upsert(ctx, o.Docs, T, cClient, c.UCC, c, "test"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: T, Key: "D224-debit", ValueDate: D, Narration: "debit", Source: ledger.SourceRef{Kind: "MANUAL", ID: "D224"}, CreatedBy: "test",
		Postings: []ledger.Posting{{Account: ledger.ClientAccount("D224"), Debit: money.MustAmount("20000")}, {Account: ledger.BankClient, Credit: money.MustAmount("20000")}}}); err != nil {
		t.Fatal(err)
	}
	const gsec, fund, share = "IN0020990011", "INF000Z01019", "INE000Z01011"
	if _, err := docs.Update(ctx, o.Docs, T, cMarket, D, "test", func(m *Market) error {
		m.Instruments = append(m.Instruments,
			Instrument{Exchange: "NSE", Segment: "CM", Symbol: "TESTGS", Type: "EQ", ISIN: gsec, LTP: mustDec("100"), Close: mustDec("100"), HaircutPct: mustDec("0")},
			Instrument{Exchange: "NSE", Segment: "CM", Symbol: "TESTLIQ", Type: "EQ", ISIN: fund, LTP: mustDec("1000"), Close: mustDec("1000"), HaircutPct: mustDec("10")},
			Instrument{Exchange: "NSE", Segment: "CM", Symbol: "TESTSHR", Type: "EQ", ISIN: share, LTP: mustDec("500"), Close: mustDec("500"), HaircutPct: mustDec("5")})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, h := range []Holding{{UCC: "D224", ISIN: gsec, Symbol: "TESTGS", Qty: dq(1000), Pledged: dq(1000)}, // 1,00,000
		{UCC: "D224", ISIN: fund, Symbol: "TESTLIQ", Qty: dq(60), Pledged: dq(50)},        // 50 x 1000 x 90% = 45,000 ...
		{UCC: "D224", ISIN: share, Symbol: "TESTSHR", Qty: dq(1200), Pledged: dq(1000)}} { // 1000 x 500 x 95% = 4,75,000
		docs.Upsert(ctx, o.Docs, T, cHolding, holdingID(h.UCC, h.ISIN), h, "test")
	}
	// ... and 50 more fund units' worth: 1,00,000 + 50,000 = 1,50,000 cash equivalent.
	docs.Update(ctx, o.Docs, T, cMarket, D, "test", func(m *Market) error {
		for i := range m.Instruments {
			if m.Instruments[i].ISIN == fund {
				m.Instruments[i].HaircutPct = mustDec("0")
			}
		}
		return nil
	})
	if _, err := o.SaveConnection(ctx, T, "admin", connFinspot, map[string]string{"account_suffix": "-BJT", "cash_eq_isins": fund, "dealer_ids": "DLR01",
		"exchange_users": "NSE=9001:100000000001; BSE=9002:1000000000002; NFO=9003:100000000003; BFO=9004:100000000004; CDS=9005:100000000005; BCD=9006:100000000006; MCX=9007:100000000007", "segment_expiry": "30/06/2027"}, nil); err != nil {
		t.Fatal(err)
	}
	rv, _ := o.Risk(ctx, T, D)
	for _, r := range rv.Rows {
		if r.UCC == "D224" && (r.CashEqColl.Cmp(mustDec("150000")) != 0 || r.NonCashColl.Cmp(mustDec("475000")) != 0 || r.Collateral.Cmp(mustDec("625000")) != 0 || r.Funds.Cmp(mustDec("-20000")) != 0) {
			t.Fatalf("risk row %+v", r)
		}
	}
	limitLine := func() string {
		t.Helper()
		name, b, err := o.FinspotLimitFile(ctx, T, next)
		if err != nil {
			t.Fatal(err)
		}
		readFinspot(t, "finspot-rms-limit", name, b)
		var got string
		for _, l := range strings.Split(strings.TrimSpace(string(b)), "\r\n")[1:] {
			if !strings.HasSuffix(strings.Split(l, "|")[0], "-BJT") || len(strings.Split(l, "|")) != 25 {
				t.Fatalf("line %q", l)
			}
			if strings.HasPrefix(l, "D224-BJT|") {
				got = l
			}
		}
		return got
	}
	if l := limitLine(); l != "D224-BJT||||-20000.00||||||||||||no||||||||150000.00" {
		t.Fatalf("limit line %q", l)
	}
	holding := func() map[string]map[string]string {
		t.Helper()
		name, b, err := o.FinspotHoldingFile(ctx, T, next, false)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]map[string]string{}
		for _, r := range readFinspot(t, "finspot-dp-holding", name, b).Rows {
			if !strings.HasSuffix(r.Values["AccountId"], "-BJT") {
				t.Fatalf("%+v", r.Values)
			}
			if r.Values["AccountId"] == "D224-BJT" {
				out[r.Values["ISIN"]] = r.Values
			}
		}
		return out
	}
	hl := holding()
	// The pledged shares are collateral (sellable too); the cash-equivalent
	// pledges are not collateral here: no double counting.
	if v := hl[share]; v["HoldingsQty"] != "1200" || v["IsCollateral"] != "1" || v["CollateralQty"] != "1000" || v["Haircut"] != "0.05" || v["CollateralMode"] != "1" || v["BrokerCollateralQty"] != "0" {
		t.Fatalf("share line %+v", v)
	}
	for _, i := range []string{gsec, fund} {
		if v := hl[i]; v == nil || v["IsCollateral"] != "0" || v["CollateralQty"] != "0" {
			t.Fatalf("cash-equivalent line %s %+v", i, v)
		}
	}
	if hl[fund]["HoldingsQty"] != "60" {
		t.Fatalf("fund %+v", hl[fund])
	}

	// The suffix in the other client files.
	for _, f := range []struct{ kind, layout, col string }{{"finspot-t1-holdings", "finspot-t1-holding", "AccountId"}, {"finspot-clients", "finspot-investor-client", "AccountId"},
		{"finspot-clients", "finspot-investor-client", "LoginId"}, {"finspot-client-info", "finspot-client-info", "AccountId"}, {"finspot-client-segments", "finspot-investor-segment", "UserId"},
		{"finspot-client-products", "finspot-investor-products", "UserId"}, {"finspot-nfo-cost", "finspot-nfo-cost-price", "CLIENT_ID"}, {"finspot-dealer-positions", "finspot-dealer-position", "CLIENT"}} {
		name, b, err := o.FinspotFile(ctx, T, "anita.rao", f.kind, next, true)
		if err != nil {
			t.Fatalf("%s: %v", f.kind, err)
		}
		tb := readFinspot(t, f.layout, name, b)
		if len(tb.Rows) == 0 && f.kind != "finspot-t1-holdings" {
			t.Fatalf("%s: no lines", f.kind)
		}
		for _, r := range tb.Rows {
			if !strings.HasSuffix(r.Values[f.col], "-BJT") {
				t.Fatalf("%s: %s %q", f.kind, f.col, r.Values[f.col])
			}
		}
	}
	if _, err := o.SetBlocked(ctx, T, "anita.rao", "D224", true, "Debit"); err != nil {
		t.Fatal(err)
	}
	name, b, err := o.FinspotBlockFile(ctx, T, "anita.rao", next)
	if err != nil || !strings.Contains(string(b), "\r\n||||D224-BJT||MIS|") {
		t.Fatalf("block file %s %v %q", name, err, b)
	}
	// Blocked: the debit stays, no notional cash, no collateral.
	if l := limitLine(); l != "D224-BJT||||-20000.00||||||||||||no||||||||0.00" {
		t.Fatalf("blocked limit line %q", l)
	}
	if v := holding()[share]; v["IsCollateral"] != "0" || v["CollateralQty"] != "0" || v["HoldingsQty"] != "1200" {
		t.Fatalf("blocked share line %+v", v)
	}
	o.SetBlocked(ctx, T, "anita.rao", "D224", false, "")

	// Finspot's RMS account ID holds 10 characters: UCC + suffix is checked.
	o.SaveConnection(ctx, T, "admin", connFinspot, map[string]string{"account_suffix": "-BROKER"}, nil)
	if _, _, err := o.FinspotLimitFile(ctx, T, next); err == nil || !strings.Contains(err.Error(), "D224-BROKER") || !strings.Contains(err.Error(), "10 characters") {
		t.Fatalf("long IDs: %v", err)
	}
	// The ISIN Holding file has no such limit.
	if _, _, err := o.FinspotHoldingFile(ctx, T, next, false); err != nil {
		t.Fatal(err)
	}
}

// The ISIN Holding file: sellable demat holdings with CUSPA added,
// margin-pledged non-cash securities as collateral, the POA flag from the
// client's DDPI.
func TestFinspotHoldingFile(t *testing.T) {
	o, ctx := setup(t)
	set, _ := o.Settings(ctx, T)
	next := set.nextBusinessDay(D)
	hs, _ := o.Holdings(ctx, T, "")
	var h Holding
	for _, x := range hs {
		if wq(x.Qty) > 0 && len(x.ISIN) == 12 {
			h = x
			break
		}
	}
	if h.UCC == "" {
		t.Fatal("no holding in the sample")
	}
	// An unpaid purchase of the same ISIN in CUSPA can be sold too; a DDPI makes the POA flag Y.
	docs.Put(ctx, o.Docs, T, cCUSPA, "CU-T1", CUSPAItem{ID: "CU-T1", Date: D, UCC: h.UCC, ISIN: h.ISIN, Symbol: h.Symbol, Qty: 7, Status: "Held in CUSPA"}, 0, "test")
	docs.Upsert(ctx, o.Docs, T, cCDSLBODetail, h.UCC, CDSLBODetail{UCC: h.UCC, DDPIRegNo: "DDPI0000000001"}, "test")
	name, b, err := o.FinspotHoldingFile(ctx, T, next, false)
	if err != nil {
		t.Fatal(err)
	}
	tb := readFinspot(t, "finspot-dp-holding", name, b)
	if !strings.HasPrefix(name, "FINSPOT_DP_HOLDING_UPLOAD_") || len(tb.Rows) == 0 {
		t.Fatalf("%s %d rows", name, len(tb.Rows))
	}
	found, pledged := false, 0
	for _, r := range tb.Rows {
		v := r.Values
		cq, _ := strconv.ParseInt(v["CollateralQty"], 10, 64)
		hq, _ := strconv.ParseInt(v["HoldingsQty"], 10, 64)
		if v["Product"] != "CNC" || (v["IsCollateral"] == "1") != (cq > 0) || cq > hq || v["BrokerCollateralQty"] != "0" || v["CollateralMode"] != "1" {
			t.Fatalf("%+v", v)
		}
		if cq > 0 {
			pledged++
		}
		if hc := mustDec(v["Haircut"]); hc.Sign() < 0 || hc.Cmp(mustDec("1")) > 0 {
			t.Fatalf("haircut %s", v["Haircut"])
		}
		if v["AccountId"] == h.UCC && v["ISIN"] == h.ISIN {
			found = true
			if v["HoldingsQty"] != itoa(wq(h.Qty)+7) || v["POAFlag"] != "Y" {
				t.Fatalf("holding %+v, want %d", v, wq(h.Qty)+7)
			}
		}
	}
	if !found || pledged == 0 {
		t.Fatalf("%s %s not in the file, or no margin-pledged collateral line (%d)", h.UCC, h.ISIN, pledged)
	}
	cl, _ := o.DailyFileChecklist(ctx, T, next)
	for _, it := range cl.Items {
		if it.Party == "Finspot" {
			t.Fatalf("Finspot files listed without the connection: %s", it.ID)
		}
	}
	if _, err := o.SaveConnection(ctx, T, "admin", connFinspot, map[string]string{"user_role": "INVESTOR"}, nil); err != nil {
		t.Fatal(err)
	}
	o.FinspotHoldingFile(ctx, T, next, false)
	cl, _ = o.DailyFileChecklist(ctx, T, next)
	got := map[string]string{}
	for _, it := range cl.Items {
		if it.Party == "Finspot" {
			got[it.ID] = it.Status
		}
	}
	if got["finspot-holding"] != "Written" || got["finspot-limit"] == "" || got["finspot-limit"] == "Written" {
		t.Fatalf("checklist %v", got)
	}
	if _, _, err := o.FinspotLimitFile(ctx, T, next); err != nil {
		t.Fatal(err)
	}
	cl, _ = o.DailyFileChecklist(ctx, T, next)
	for _, it := range cl.Items {
		if it.ID == "finspot-limit" && it.Status != "Written" {
			t.Fatalf("limit file %s", it.Status)
		}
	}
}

// The T1 file has the previous day's net delivery buys.
func TestFinspotT1File(t *testing.T) {
	o, ctx := setup(t)
	set, _ := o.Settings(ctx, T)
	next := set.nextBusinessDay(D)
	ts, _ := o.exchangeTrades(ctx, T, D)
	_, look, _ := o.Market(ctx, T, D)
	want := map[string]int64{}
	for _, tr := range ts {
		i, ok := look.ForTrade(tr)
		if tr.Segment != "CM" || tr.Product != "CNC" || !ok || i.ISIN == "" {
			continue
		}
		if tr.Side == "B" {
			want[tr.UCC+"|"+i.ISIN] += tr.Qty
		} else {
			want[tr.UCC+"|"+i.ISIN] -= tr.Qty
		}
	}
	n := 0
	for _, q := range want {
		if q > 0 {
			n++
		}
	}
	name, b, err := o.FinspotHoldingFile(ctx, T, next, true)
	if err != nil {
		t.Fatal(err)
	}
	tb := readFinspot(t, "finspot-t1-holding", name, b)
	if !strings.HasPrefix(name, "FINSPOT_NSEBSE_T1_HOLDING_ISIN_") || n == 0 || len(tb.Rows) != n {
		t.Fatalf("%s: %d rows, want %d", name, len(tb.Rows), n)
	}
	for _, r := range tb.Rows {
		if r.Values["HoldingsQty"] != itoa(want[r.Values["AccountId"]+"|"+r.Values["ISIN"]]) {
			t.Fatalf("%+v want %d", r.Values, want[r.Values["AccountId"]+"|"+r.Values["ISIN"]])
		}
	}
}

// A blocked client gets block lines; when the block is lifted the client
// gets unblock lines that day, and nothing after.
func TestFinspotBlockFile(t *testing.T) {
	o, ctx := setup(t)
	set, _ := o.Settings(ctx, T)
	d1 := set.nextBusinessDay(D)
	d2 := set.nextBusinessDay(d1)
	cs, _ := o.Clients(ctx, T)
	var u string
	for _, c := range cs {
		if c.Status == "Active" && finspotBlockWhy(c) == "" {
			u = c.UCC
			break
		}
	}
	if _, _, err := o.FinspotBlockFile(ctx, T, "vikram.shah", d1); err == nil {
		t.Fatal("compliance may not download the block file")
	}
	if _, err := o.SetBlocked(ctx, T, "anita.rao", u, true, "Debit 5 days"); err != nil {
		t.Fatal(err)
	}
	lines := func(date, block string) []xfile.Row {
		t.Helper()
		name, b, err := o.FinspotBlockFile(ctx, T, "anita.rao", date)
		if err != nil {
			t.Fatal(err)
		}
		tb := readFinspot(t, "finspot-rms-block", name, b)
		for _, l := range strings.Split(strings.TrimSpace(string(b)), "\r\n")[1:] {
			if n := len(strings.Split(l, "|")); n != 22 {
				t.Fatalf("%d fields: %q", n, l)
			}
		}
		var out []xfile.Row
		for _, r := range tb.Rows {
			if r.Values["Client"] == u {
				if r.Values["Block"] != block || r.Values["BlockType"] != "NON-SQROFF" {
					t.Fatalf("%+v want %s", r.Values, block)
				}
				out = append(out, r)
			}
		}
		return out
	}
	got := lines(d1, "Y")
	if len(got) != 3 || got[0].Values["Products"] != "MIS" || got[1].Values["Products"] != "NRML" || got[2].Values["Products"] != "CNC" || got[2].Values["Side"] != "B" || got[0].Values["Remarks"] != "Debit 5 days" {
		t.Fatalf("%+v", got)
	}
	if len(lines(d1, "Y")) != 3 { // made again: the full list again
		t.Fatal("block not repeated")
	}
	o.SetBlocked(ctx, T, "anita.rao", u, false, "")
	if len(lines(d2, "N")) != 3 || len(lines(d2, "N")) != 3 {
		t.Fatal("no unblock lines")
	}
	if len(lines(set.nextBusinessDay(d2), "N")) != 0 {
		t.Fatal("unblock written again the next day")
	}
}

// Client logins: every active client the first time, again on the same
// day, then only the changed; never a password; segments need the
// firm's exchange users.
func TestFinspotClientFiles(t *testing.T) {
	o, ctx := setup(t)
	set, _ := o.Settings(ctx, T)
	d1 := set.nextBusinessDay(D)
	d2 := set.nextBusinessDay(d1)
	cs, _ := o.Clients(ctx, T)
	active := 0
	var u string
	for _, c := range cs {
		if c.Status == "Active" {
			active++
			if u == "" {
				u = c.UCC
			}
		}
	}
	if _, _, err := o.FinspotClientFile(ctx, T, "priya.nair", "client-segments", d1, false); err == nil || !strings.Contains(err.Error(), "exchange user") {
		t.Fatalf("segments without exchange users: %v", err)
	}
	o.SaveConnection(ctx, T, "admin", connFinspot, map[string]string{"exchange_users": "NSE=9001:100000000001; BSE=9002:1000000000002; NFO=9003:100000000003; BFO=9004:100000000004; CDS=9005:100000000005; BCD=9006:100000000006; MCX=9007:100000000007:31/12/2027", "segment_expiry": "30/06/2027"}, nil)
	file := func(kind, date string) (*xfile.Table, []byte) {
		t.Helper()
		ids := map[string]string{"clients": "finspot-investor-client", "client-segments": "finspot-investor-segment", "client-products": "finspot-investor-products"}
		name, b, err := o.FinspotClientFile(ctx, T, "priya.nair", kind, date, false)
		if err != nil {
			t.Fatal(err)
		}
		return readFinspot(t, ids[kind], name, b), b
	}
	tb, b := file("clients", d1)
	if len(tb.Rows) != active {
		t.Fatalf("%d lines for %d active clients", len(tb.Rows), active)
	}
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\r\n")[1:] {
		f := strings.Split(l, "|")
		if len(f) != 29 || f[4] != "" || f[5] != "" || f[0] != f[2] || f[22] != "A" || f[28] != "E" {
			t.Fatalf("line %q", l)
		}
	}
	if tb, _ := file("clients", d1); len(tb.Rows) != active {
		t.Fatal("same-day file differs")
	}
	if tb, _ := file("clients", d2); len(tb.Rows) != 0 {
		t.Fatalf("%d unchanged clients written again", len(tb.Rows))
	}
	docs.Update(ctx, o.Docs, T, cClient, u, "test", func(x *Client) error { x.Email = "new.mail@example.com"; return nil })
	if tb, _ := file("clients", d2); len(tb.Rows) != 1 || tb.Rows[0].Values["AccountId"] != u || tb.Rows[0].Values["Email"] != "new.mail@example.com" {
		t.Fatalf("changed client: %+v", tb.Rows)
	}
	sg, _ := file("client-segments", d1)
	pr, _ := file("client-products", d1)
	if len(sg.Rows) == 0 || len(pr.Rows) == 0 {
		t.Fatalf("%d segment and %d product lines", len(sg.Rows), len(pr.Rows))
	}
	c, _ := o.Client(ctx, T, u)
	for _, r := range sg.Rows {
		if r.Values["Segment"] == "MCX" && r.Values["Expiry"] != "31/12/2027" || r.Values["Segment"] == "NSE" && r.Values["Expiry"] != "30/06/2027" {
			t.Fatalf("%+v", r.Values)
		}
	}
	for _, r := range pr.Rows {
		if r.Values["UserId"] == u && r.Values["Products"] != finspotProducts(c) {
			t.Fatalf("%+v", r.Values)
		}
	}
	if finspotProducts(Client{Segments: []string{"CM", "FO"}}) != "I,M,C" || finspotProducts(Client{Segments: []string{"CM"}}) != "I,C" {
		t.Fatal("products")
	}
	if finspotText("a|b\r\nc") != "a b c" {
		t.Fatal("text")
	}
}

// Carry-forward F&O positions with their cost.
func TestFinspotCostFile(t *testing.T) {
	o, ctx := setup(t)
	set, _ := o.Settings(ctx, T)
	name, b, err := o.FinspotCostFile(ctx, T, set.nextBusinessDay(D))
	if err != nil {
		t.Fatal(err)
	}
	tb := readFinspot(t, "finspot-nfo-cost-price", name, b)
	if !strings.Contains(string(b), "\r\nCLIENT_ID,INSTRUMENT_TYPE,SCRIP_NAME,EXPIRY_DATE,STRIKE_PRICE,OPTION_TYPE,NETQTY,AVG_RATE\r\n") || len(tb.Rows) == 0 {
		t.Fatalf("%d rows: %.300q", len(tb.Rows), b)
	}
	for _, r := range tb.Rows {
		it, ot := r.Values["INSTRUMENT_TYPE"], r.Values["OPTION_TYPE"]
		if strings.HasPrefix(it, "FUT") != (ot == "XX") || r.Values["NETQTY"] == "0" {
			t.Fatalf("%+v", r.Values)
		}
	}
}

// Dealer order types, dealer client positions and margin baskets come
// from firm settings and the day's data; the client information addition
// file has the client lines without passwords.
func TestFinspotDealerAndBasketFiles(t *testing.T) {
	o, ctx := setup(t)
	set, _ := o.Settings(ctx, T)
	d := set.nextBusinessDay(D)
	if _, _, err := o.FinspotOrderTypeFile(ctx, T, d); err == nil {
		t.Fatal("order types without dealer IDs")
	}
	if _, _, err := o.FinspotDealerPositionFile(ctx, T, d); err == nil {
		t.Fatal("positions without a dealer")
	}
	o.SaveConnection(ctx, T, "admin", connFinspot, map[string]string{"dealer_ids": "DLR01, dlr02", "basket_min_pct": "25", "nse_basket": "TESTMIS"}, nil)
	name, b, err := o.FinspotOrderTypeFile(ctx, T, d)
	if err != nil {
		t.Fatal(err)
	}
	tb := readFinspot(t, "finspot-order-types", name, b)
	if len(tb.Rows) != 2 || tb.Rows[1].Values["UserId"] != "DLR02" || tb.Rows[0].Values["OrderType"] != "LMT,MKT,SL-LMT,SL-MKT,DS" {
		t.Fatalf("%+v", tb.Rows)
	}
	name, b, err = o.FinspotDealerPositionFile(ctx, T, d)
	if err != nil {
		t.Fatal(err)
	}
	tb = readFinspot(t, "finspot-dealer-position", name, b)
	rv, _ := o.Risk(ctx, T, D)
	want := 0
	for _, ps := range rv.Positions {
		for _, p := range ps {
			if p.Segment != "CM" && p.Qty != 0 && (p.Exchange == "NSE" || p.Exchange == "BSE" || p.Exchange == "MCX") {
				want++
			}
		}
	}
	if want == 0 || len(tb.Rows) != want {
		t.Fatalf("%d position lines, want %d", len(tb.Rows), want)
	}
	for _, r := range tb.Rows {
		v := r.Values
		if v["USER"] != "DLR01" || v["PRODUCT"] != "NRML" || (v["BUYQTY"] == "0") == (v["SELLQTY"] == "0") || v["EXP"] != strings.ToUpper(v["EXP"]) {
			t.Fatalf("%+v", v)
		}
	}
	name, b, err = o.FinspotMarginBasketFile(ctx, T, "NSE", d)
	if err != nil {
		t.Fatal(err)
	}
	lib, _ := XFiles()
	s, _ := lib.Get("finspot-margin-basket-nse")
	tb, _ = s.Read(name, b)
	if !strings.HasPrefix(string(b), "NSE Margin Basket|I|TESTMIS\r\n") || len(tb.Issues) != 0 || len(tb.Rows) < 2 {
		t.Fatalf("%v %.200q", tb.Issues, b)
	}
	for _, r := range tb.Rows[1:] {
		if mustDec(r.Values["MarginPct"]).Cmp(mustDec("25")) < 0 || r.Values["Series"] == "" {
			t.Fatalf("%+v", r.Values)
		}
	}
	m, _, _ := o.Market(ctx, T, D)
	var isin string
	for _, i := range m.Instruments {
		if i.Segment == "CM" && i.Type == "EQ" && i.ISIN != "" {
			isin = i.ISIN
			break
		}
	}
	docs.Upsert(ctx, o.Docs, T, cSecRef, isin, SecRef{ISIN: isin, BSECode: "599999"}, "test")
	name, b, err = o.FinspotMarginBasketFile(ctx, T, "BSE", d)
	if err != nil {
		t.Fatal(err)
	}
	s, _ = lib.Get("finspot-margin-basket-bse")
	tb, _ = s.Read(name, b)
	if !strings.HasPrefix(string(b), "BSE Margin Basket|I|BSEMIS\r\n599999|") || len(tb.Issues) != 0 || len(tb.Rows) != 2 {
		t.Fatalf("%v %q", tb.Issues, b)
	}
	name, b, err = o.FinspotClientFile(ctx, T, "priya.nair", "client-info", d, false)
	if err != nil {
		t.Fatal(err)
	}
	readFinspot(t, "finspot-client-info", name, b)
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\r\n")[1:] {
		f := strings.Split(l, "|")
		if len(f) != 22 || f[3] != "" || f[4] != "" || f[14] != "2" || f[21] != "A" || f[0] == "" {
			t.Fatalf("line %q", l)
		}
	}
}
