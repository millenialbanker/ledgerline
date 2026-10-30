package ops

import (
	"archive/zip"
	"bytes"
	"os"
	"strings"
	"testing"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

func zipOf(t *testing.T, name string, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create(name)
	w.Write(b)
	zw.Close()
	return buf.Bytes()
}

func openWork(t *testing.T, o *Ops, process string) map[string]WorkItem {
	t.Helper()
	ws, _ := docs.List[WorkItem](t.Context(), o.Docs, T, cWorkItem, "")
	out := map[string]WorkItem{}
	for _, w := range ws {
		if w.Process == process {
			out[strings.TrimPrefix(w.Key, process+":")] = w
		}
	}
	return out
}

func activeIn(t *testing.T, o *Ops, seg string) []Client {
	t.Helper()
	cs, _ := o.Clients(t.Context(), T)
	var out []Client
	for _, c := range cs {
		if c.Status == "Active" && contains(c.Segments, seg) {
			out = append(out, c)
		}
	}
	if len(out) < 3 {
		t.Fatalf("only %d active %s clients", len(out), seg)
	}
	return out
}

const npHeader = "UCC Code|IsActiveStatus|Approved|PAN Validation Status|PAN Status|KRA Status|PrimaryOrSecondaryDP1Status|PrimaryOrSecondaryDP2Status|PrimaryOrSecondaryDP3Status|PrimaryOrSecondaryDP4Status|PrimaryOrSecondaryDP5Status"

func TestBSENotPermittedToTrade(t *testing.T) {
	o, ctx := setup(t)
	cs := activeIn(t, o, "CM")
	a, b := cs[0].UCC, cs[1].UCC
	run1 := strings.Join([]string{npHeader,
		a + "|||PAN Validation Status is Invalid.|||||||",
		b + "|||||||Primary Or Secondary DP Status is Pending/Invalid.|||"}, "\r\n") + "\r\n"
	r := loadX(t, o, "EQX_9999_UCC_NotPermittedToTrade_16102026_01.csv", []byte(run1))
	if r.Format != "bse-ucc-not-permitted" || r.Added != 2 {
		t.Fatalf("%+v", r)
	}
	bl := o.BSEUCCBlocked(ctx, T)
	if len(bl) != 2 || !strings.Contains(bl[a][0], "Income Tax") || !strings.Contains(bl[b][0], "demat account 2") {
		t.Fatalf("blocked %v", bl)
	}
	w := openWork(t, o, wBSEUCCBlocked)
	if w[a].Status != "Open" || w[b].Status != "Open" || w[a].Role != RoleMaker || w[a].View != "clients" {
		t.Fatalf("work %+v", w)
	}
	// The evening run replaces the first: b is no longer listed.
	run2 := npHeader + "\r\n" + a + "|||PAN Validation Status is Invalid.|||||||\r\n"
	loadX(t, o, "EQX_9999_UCC_NotPermittedToTrade_16102026_02.csv", []byte(run2))
	if w := openWork(t, o, wBSEUCCBlocked); w[a].Status != "Open" || w[b].Status != "Done" {
		t.Fatalf("after run 2 %+v", w)
	}
	res, _ := o.LoadExchangeFile(ctx, T, "priya.nair", "EQX_9999_UCC_NotPermittedToTrade_16102026_01.csv", []byte(run1), true)
	if !strings.HasPrefix(res[0].Loaded, "Not used") {
		t.Fatalf("older run loaded: %+v", res[0])
	}
	// Derivatives list adds to the cash one.
	loadX(t, o, "EQD_9999_UCC_NotPermittedToTrade_16102026_01.csv", []byte(npHeader+"\r\n"+cs[2].UCC+"||||PAN is Invalid by KRA.||||||\r\n"))
	if bl := o.BSEUCCBlocked(ctx, T); len(bl) != 2 || !strings.HasPrefix(bl[cs[2].UCC][0], "Derivatives: the KRA") {
		t.Fatalf("with EQD %v", bl)
	}
}

func TestBSETradingStatus(t *testing.T) {
	o, ctx := setup(t)
	cs := activeIn(t, o, "CM")
	var lines []string
	for _, c := range cs[1:] {
		lines = append(lines, c.UCC)
	}
	lines = append(lines, "ZZUNK001")
	r := loadX(t, o, "EQX_9999_UCC_Trading_Status_16102026_01.csv", []byte(strings.Join(lines, "\r\n")+"\r\n"))
	if r.Added != len(lines) {
		t.Fatalf("%+v", r)
	}
	st, _, _ := docs.Get[BSETradingStatus](ctx, o.Docs, T, cBSEUCCList, D+"|CM")
	if !contains(st.Missing, cs[0].UCC) || !contains(st.Unknown, "ZZUNK001") || contains(st.Missing, cs[1].UCC) {
		t.Fatalf("%+v", st)
	}
	if w := openWork(t, o, wBSEUCCStatus); w["CM"].Status != "Open" {
		t.Fatalf("work %+v", w)
	}
}

func TestBSEChargesFiles(t *testing.T) {
	o, ctx := setup(t)
	m, _, _ := o.Market(ctx, T, D)
	if _, err := o.App.RunBilling(ctx, T, "priya.nair", D, m.SettlementPrices()); err != nil {
		t.Fatal(err)
	}
	oc := "Member Code,Trade Date,Daily Number of Orders,Daily Charges,Daily Cumulative Charges\n9999,2026-10-15,120,12.00,12.00\n9999,2026-10-16,80,8.00,20.00\n"
	if r := loadX(t, o, "Equity_ OrderCharges_20261016_9999.csv", []byte(oc)); r.Format != "bse-order-charges" || r.Added != 2 {
		t.Fatalf("%+v", r)
	}
	if x, ok := o.BSEMonthOrderCharges(ctx, T, "2026-10"); !ok || x.Total().String() != "20" {
		t.Fatalf("order charges %+v", x)
	}

	billed := o.billedTxn(ctx, T, "BSE", D)
	if billed.Sign() <= 0 {
		t.Fatal("no transaction charges billed on BSE trades")
	}
	hdr := "Member Code,Group,BSE Exclusive/Non-Exclusive,Settlement Number,Daily Turnover,Trade Date,Daily Cumulative Turnover,Monthly Cumulative Turnover,Transaction Charges,Filler,Amount Payable,Filler1,Filler2,Filler3"
	trc := func(extra string) []byte {
		half := billed.Sub(money.MustDec("0.10"))
		rest := money.MustDec("0.10").Add(money.MustDec(extra))
		return []byte(strings.Join([]string{hdr,
			"9999,A,N,2627145,1000000.00,16/10/2026,1000000.00,5000000.00," + half.Fixed(2) + ",," + half.Fixed(2) + ",,,",
			"9999,B,N,2627145,500000.00,16/10/2026,500000.00,2000000.00," + rest.Fixed(2) + ",," + rest.Fixed(2) + ",,,"}, "\r\n") + "\r\n")
	}
	loadX(t, o, "Turnover_TRC_20261016_9999.csv", trc("0.40"))
	if w := openWork(t, o, wBSETxn); w[D].Status == "Open" {
		t.Fatalf("break within tolerance %+v", w)
	}
	loadX(t, o, "Turnover_TRC_20261016_9999.csv", trc("50"))
	if w := openWork(t, o, wBSETxn); w[D].Status != "Open" || w[D].Role != RoleChecker {
		t.Fatalf("no break %+v", w)
	}
	secs, _ := o.ClearingData(ctx, T, D)
	for _, s := range secs {
		if s.ID == "bse_txn" && (s.Diffs != 1 || len(s.Rows) != 3) {
			t.Fatalf("%+v", s)
		}
	}
}

func TestBSEClearingStatement(t *testing.T) {
	o, ctx := newOps(t)
	b, err := os.ReadFile("testdata/VA1610.9999")
	if err != nil {
		t.Fatal(err)
	}
	r := loadX(t, o, "VA1610.9999", b)
	if r.Format != "bse-clearing-statement" || r.Added != 3 {
		t.Fatalf("%+v", r)
	}
	x, _, _ := docs.Get[BSEClearingStmt](ctx, o.Docs, T, cBSEClrStmt, D)
	e := x.Entries
	if e[0].Code != "PAY" || e[0].Particulars != "FUNDS PAYIN SETT 2627145" || e[0].DocNo != "100001" || e[0].Debit.String() != "12345.5" || e[0].Credit.Sign() != 0 ||
		e[2].Credit.String() != "12000" || e[2].Debit.Sign() != 0 || e[2].Balance.String() != "595.5" || e[2].BalanceDrCr != "DR" || e[2].AsOf != D {
		t.Fatalf("%+v", e)
	}
	if x.TotalDebit.String() != "12595.5" || x.ReportBy != "2026-10-31" {
		t.Fatalf("%+v", x)
	}
	if w := openWork(t, o, wBSEClrStmt); w[D].Status != "Open" || w[D].Role != RoleChecker || w[D].Due != "2026-10-31 17:00" {
		t.Fatalf("%+v", w)
	}
	// Totals that disagree with the entries are not loaded.
	bad := strings.Replace(string(b), "12,000.00\r\n", "12,100.00\r\n", 1)
	res, _ := o.LoadExchangeFile(ctx, T, "priya.nair", "VA1610.9999", []byte(bad), true)
	if !strings.Contains(res[0].Loaded, "totals") {
		t.Fatalf("%+v", res[0])
	}
}

func TestBSEDebarredPANs(t *testing.T) {
	o, ctx := setup(t)
	cs := activeIn(t, o, "CM")
	one, two := cs[0], cs[1]
	b, err := os.ReadFile("testdata/DEBR161026.9999")
	if err != nil {
		t.Fatal(err)
	}
	f := strings.NewReplacer("{{UCC1}}", one.UCC, "{{PAN1}}", one.PAN, "{{UCC2}}", two.UCC, "{{PAN2}}", two.PAN).Replace(string(b))
	r := loadX(t, o, "DEBR161026.9999", []byte(f))
	if r.Format != "bse-debarred-pan" || r.Added != 3 || !strings.Contains(r.Loaded, "1 active clients blocked, 1 flagged") {
		t.Fatalf("%+v", r)
	}
	c1, _ := o.Client(ctx, T, one.UCC)
	c2, _ := o.Client(ctx, T, two.UCC)
	if !c1.Blocked || c1.BlockedReason != DebarredReason || c2.Blocked {
		t.Fatalf("blocked %v %q, %v", c1.Blocked, c1.BlockedReason, c2.Blocked)
	}
	if _, hit := o.IsDebarredPAN(ctx, T, one.PAN, D); !hit {
		t.Fatal("PAN not on the debarred list")
	}
	if _, hit := o.IsDebarredPAN(ctx, T, two.PAN, D); hit {
		t.Fatal("scrip-specific order put on the debarred list")
	}
	w := openWork(t, o, wBSEDebarred)
	if w[one.UCC].Role != RoleCompliance || w[two.UCC+":599901"].Status != "Open" {
		t.Fatalf("%+v", w)
	}
	as, _ := o.Alerts(ctx, T)
	n := 0
	for _, a := range as {
		if a.Source == "BSE" && strings.HasPrefix(a.Type, "Debarred") && a.Severity == "High" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("%d alerts", n)
	}
}

func TestBSEContactBulkOrdersDemat(t *testing.T) {
	o, ctx := setup(t)
	cs := activeIn(t, o, "CM")
	// NOME: one mobile that differs from Ledgerline's, one email.
	nome := cs[0].UCC + "|9000000001||\r\n" + cs[1].UCC + "||" + cs[1].Email + "|\r\n"
	if r := loadX(t, o, "NOME161026.9999", []byte(nome)); r.Added != 2 {
		t.Fatalf("%+v", r)
	}
	w := openWork(t, o, wBSEContact)
	if w[D].Status != "Open" || strings.Contains(w[D].Detail, "9000000001") || strings.Contains(w[D].Detail, cs[1].Email) {
		t.Fatalf("NOME work item %+v", w[D])
	}

	// BULK: a deal by a client found by name.
	pad := func(s string, n int) string { return s + strings.Repeat(" ", max(0, n-len(s))) }
	bulk := "9999|599901|" + pad("", 12) + "|" + pad(strings.ToUpper(cs[2].Name), 64) + "|B|    500000|   101.25|16/10/2026|\r\n"
	if r := loadX(t, o, "BULK161026.9999", []byte(bulk)); r.Added != 1 || !strings.Contains(r.Loaded, "1 deals") {
		t.Fatalf("%+v", r)
	}

	// BO: an order log priced in paise against a BSE trade of the day.
	ex, _ := o.App.ExchangeTrades(ctx, T, D)
	var bt struct{ ucc, sym string }
	var px money.Dec
	for _, x := range ex {
		if x.Exchange == "BSE" {
			bt.ucc, bt.sym, px = x.UCC, x.Symbol, x.Price
			break
		}
	}
	if bt.ucc == "" {
		t.Fatal("no BSE trade in the sample day")
	}
	paise := px.MulInt(100).Fixed(0)
	row := func(id, act string) string {
		return "B|599901|" + bt.sym + "|10|10|" + paise + "|0|0|10:15:00|16/10/2026|" + id + "|A|" + bt.ucc + "|EOSESS|" + act + "|ORDER ADDED SUCCESSFULLY|1004|1111222233334444|"
	}
	bo := row("1000000000000000001", "A") + "\n" + row("1000000000000000002", "A") + "\n" + row("1000000000000000002", "D") + "\n"
	if r := loadX(t, o, "BO161026.9999.zip", zipOf(t, "BO161026.9999", []byte(bo))); r.Format != "bse-order-log" || r.Added != 3 || !strings.Contains(r.Loaded, "paise") {
		t.Fatalf("%+v", r)
	}
	ol := o.BSEOrderToTrade(ctx, T, D)
	if ol == nil || ol.Orders[0].Price.Cmp(px) != 0 || ol.ByTerminal[0].Key != "1111222233334444" || ol.ByClient[0].Orders != 3 {
		t.Fatalf("%+v", ol)
	}

	// Demat check responses, PAN masked.
	f := make([]string, 18)
	f[0], f[1], f[2], f[3], f[4], f[5], f[6], f[16], f[17] = "02", "9999", cs[0].UCC, "01", cs[0].PAN, "12081600", "9999", "01", "A"
	if r := loadX(t, o, "CDSL_UCCMSTR_02_16102026_1.RES.9999", []byte(strings.Join(f, "#")+"\r\n")); r.Added != 1 {
		t.Fatalf("%+v", r)
	}
	rs := o.BSEUCCDematResponses(ctx, T, cs[0].UCC)
	if len(rs) != 1 || rs[0].Status != "A" || rs[0].Fields[4] == cs[0].PAN || rs[0].DPID != "12081600" {
		t.Fatalf("%+v", rs)
	}
}

func TestBSEReportsOnChecklist(t *testing.T) {
	o, _ := newOps(t)
	if err := saveClearing(t, o, map[string]string{"BSE-CM": "ICCL", "BSE-FO": "ICCL"}, nil); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile("testdata/VA1610.9999")
	loadX(t, o, "VA1610.9999", b)
	_, cl := checklistParties(t, o)
	got := map[string]ChecklistItem{}
	for _, it := range cl.Items {
		got[it.ID] = it
	}
	for _, id := range []string{"bse-ucc-np-1", "bse-ucc-np-2", "bse-ucc-ts-2", "bse-ordchg", "bse-trc", "bse-va", "bse-debr", "bse-nome", "bse-bulk", "bse-orders"} {
		if got[id].Party != "BSE" {
			t.Fatalf("%s missing: %+v", id, got[id])
		}
	}
	if got["bse-va"].Status != "Received" || got["bse-orders"].Due != D+" 17:40" {
		t.Fatalf("%+v %+v", got["bse-va"], got["bse-orders"])
	}
}
