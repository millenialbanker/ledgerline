package ops

import (
	"strconv"
	"strings"
	"testing"

	"ledgerline/internal/docs"
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
	b, _ := s.Write([]xfile.Row{finspotLimitRow("AB123", mustDec("1.5"))})
	if string(b) != "RMS Limits\r\nAB123||||1.50\r\n" {
		t.Fatalf("%q", b)
	}
}

// The ISIN Holding file: sellable demat holdings with CUSPA added, no
// collateral benefit, the POA flag from the client's DDPI.
func TestFinspotHoldingFile(t *testing.T) {
	o, ctx := setup(t)
	set, _ := o.Settings(ctx, T)
	next := set.nextBusinessDay(D)
	hs, _ := o.Holdings(ctx, T, "")
	var h Holding
	for _, x := range hs {
		if x.Qty > 0 && len(x.ISIN) == 12 {
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
	found := false
	for _, r := range tb.Rows {
		v := r.Values
		if v["Product"] != "CNC" || v["IsCollateral"] != "0" || v["CollateralQty"] != "0" || v["BrokerCollateralQty"] != "0" || v["CollateralMode"] != "1" {
			t.Fatalf("%+v", v)
		}
		if hc := mustDec(v["Haircut"]); hc.Sign() < 0 || hc.Cmp(mustDec("1")) > 0 {
			t.Fatalf("haircut %s", v["Haircut"])
		}
		if v["AccountId"] == h.UCC && v["ISIN"] == h.ISIN {
			found = true
			if v["HoldingsQty"] != itoa(h.Qty+7) || v["POAFlag"] != "Y" {
				t.Fatalf("holding %+v, want %d", v, h.Qty+7)
			}
		}
	}
	if !found {
		t.Fatalf("%s %s not in the file", h.UCC, h.ISIN)
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
