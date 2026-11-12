package ops

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/migrate/pkg"
	"ledgerline/internal/money"
	"ledgerline/internal/xfile"
)

// A client with two accounts with the firm's DP and one with another DP:
// holdings per account and added up, a delivery instruction from the
// second account, the front-office holding lines, the exchanges' UCC
// records, CDSL's transaction file and the DP bill.
func TestSeveralDematAccounts(t *testing.T) {
	o, ctx := setup(t)
	c, _ := o.Client(ctx, T, "BH1001")
	prim, second, other := c.Demat.ID, "1208160000000777", "1304140000000999"
	for _, id := range []string{prim, second} {
		if err := o.putBO(ctx, T, "test", BOAccount{UCC: "BH1001", BOID: id, Depository: "CDSL", Status: "Active", Opened: "2024-01-01"}); err != nil {
			t.Fatal(err)
		}
	}
	// The other DP's account: the client's request with its proof, approved
	// by a second person.
	doc, err := o.AddClientDoc(ctx, T, "priya.nair", "BH1001", "Demat proof", "cmr.pdf", []byte("%PDF-1.4 made-up client master report"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.RequestChange(ctx, T, "priya.nair", "BH1001", "Demat account", map[string]string{"action": "add", "id": prim}, doc.ID); err == nil {
		t.Fatal("the same account added twice")
	}
	if _, err := o.RequestChange(ctx, T, "priya.nair", "BH1002", "Demat account", map[string]string{"action": "add", "id": second}, ""); err == nil || !strings.Contains(err.Error(), "whose PAN is different") {
		t.Fatalf("another client's BO ID: %v", err)
	}
	ch, err := o.RequestChange(ctx, T, "priya.nair", "BH1001", "Demat account", map[string]string{"action": "add", "id": other}, doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.DecideChange(ctx, T, "priya.nair", ch.ID, true, ""); err == nil {
		t.Fatal("the maker approved")
	}
	if _, err := o.DecideChange(ctx, T, "farhan.khan", ch.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	c, _ = o.Client(ctx, T, "BH1001")
	ds := o.withDemats(ctx, T, c).Demats
	if len(ds) != 3 || !ds[0].Primary || ds[0].ID != prim || !ds[0].OwnDP || ds[1].ID != second || !ds[1].OwnDP || ds[2].ID != other || ds[2].OwnDP {
		t.Fatalf("demats %+v", ds)
	}

	// Holdings in the second account, loaded with its BO ID.
	hs, _ := o.Holdings(ctx, T, "BH1001")
	var h Holding
	for _, x := range hs {
		if wq(x.Free()) >= 10 {
			h = x
			break
		}
	}
	if h.ISIN == "" {
		t.Fatal("no free holding")
	}
	cs, _ := o.tradingClientMap(ctx, T)
	before, _ := o.sellableDemat(ctx, T, D, cs)
	if _, err := o.LoadFile(ctx, T, "priya.nair", "DP-HOLDINGS", "", "dph.csv", csvOf([]string{"UCC", "ISIN", "Symbol", "Qty", "Pledged", "AvgCost", "BOID"},
		[]string{"BH1001", h.ISIN, h.Symbol, "40", "0", "100", second})); err != nil {
		t.Fatal(err)
	}
	if _, err := o.LoadFile(ctx, T, "priya.nair", "DP-HOLDINGS", "", "dph2.csv", csvOf([]string{"UCC", "ISIN", "Symbol", "Qty", "Pledged", "AvgCost", "BOID"},
		[]string{"BH1001", h.ISIN, h.Symbol, "1", "0", "100", "1208160000000778"})); err == nil {
		t.Fatal("a holding in an account that is not the client's loaded")
	}
	in2, _, _ := docs.Get[Holding](ctx, o.Docs, T, cHolding, holdingIDAt("BH1001", h.ISIN, second))
	in1, _, _ := docs.Get[Holding](ctx, o.Docs, T, cHolding, holdingID("BH1001", h.ISIN))
	if wq(in2.Qty) != 40 || in2.BOID != second || in1.Qty.Cmp(h.Qty) != 0 {
		t.Fatalf("holdings by account %+v / %+v", in1, in2)
	}
	tot, _ := o.clientHoldings(ctx, T, "BH1001")
	for _, x := range tot {
		if x.ISIN == h.ISIN && x.Qty.Cmp(h.Qty.Add(dq(40))) != 0 {
			t.Fatalf("summed %v, want %v + 40", x.Qty, h.Qty)
		}
	}
	d, err := o.ClientDetail(ctx, T, "BH1001", D)
	if err != nil {
		t.Fatal(err)
	}
	var rows, totals int
	for _, x := range d.Holdings {
		if x.ISIN == h.ISIN {
			rows++
		}
	}
	for _, x := range d.HoldingTotals {
		if x.ISIN == h.ISIN && x.Qty.Cmp(h.Qty.Add(dq(40))) == 0 {
			totals++
		}
	}
	if rows != 2 || totals != 1 || len(d.Demats) != 3 {
		t.Fatalf("client screen: %d rows, %d totals, %d accounts", rows, totals, len(d.Demats))
	}
	// Finspot's and BOW's holding lines add the accounts up.
	after, _ := o.sellableDemat(ctx, T, D, cs)
	k := "BH1001|" + h.ISIN
	if after[k] == nil || before[k] == nil || after[k].Demat != before[k].Demat+40 {
		t.Fatalf("front-office holding %+v -> %+v", before[k], after[k])
	}
	bh, _, err := o.bowHoldings(ctx, T, D)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range bh {
		if x.UCC == "BH1001" && x.ISIN == h.ISIN && x.DP != after[k].Demat {
			t.Fatalf("BOW line %+v, want %d", x, after[k].Demat)
		}
	}

	// A delivery instruction from the second account debits it alone.
	book, err := o.IssueDISBookFor(ctx, T, "priya.nair", "BH1001", second, 5)
	if err != nil || book.BOID != second {
		t.Fatalf("book %+v %v", book, err)
	}
	dis := DIS{UCC: "BH1001", BOID: second, Slip: book.From, Kind: "Off-market", ToBO: "1208160099999999", Reason: "Gift", ExecDate: D, Received: D, Lines: []DISLine{{ISIN: h.ISIN, Qty: dq(10)}}}
	over := dis
	over.Lines = []DISLine{{ISIN: h.ISIN, Qty: dq(41)}}
	if _, err := o.EnterDIS(ctx, T, "priya.nair", over); err == nil || !strings.Contains(err.Error(), "free") {
		t.Fatalf("more than the second account holds: %v", err)
	}
	wrong := dis
	wrong.BOID = ""
	if _, err := o.EnterDIS(ctx, T, "priya.nair", wrong); err == nil || !strings.Contains(err.Error(), "not issued") {
		t.Fatalf("the second account's slip on the primary: %v", err)
	}
	x, err := o.EnterDIS(ctx, T, "priya.nair", dis)
	if err != nil {
		t.Fatal(err)
	}
	if x, err = o.VerifyDIS(ctx, T, "farhan.khan", x.ID, true, "", "Call ref C-7"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := o.DISBatch(ctx, T, "priya.nair", D); err != nil {
		t.Fatal(err)
	}
	if _, err := o.ExecuteDIS(ctx, T, "priya.nair", x.ID, true, "", D); err != nil {
		t.Fatal(err)
	}
	in2, _, _ = docs.Get[Holding](ctx, o.Docs, T, cHolding, holdingIDAt("BH1001", h.ISIN, second))
	in1, _, _ = docs.Get[Holding](ctx, o.Docs, T, cHolding, holdingID("BH1001", h.ISIN))
	if wq(in2.Qty) != 30 || in1.Qty.Cmp(h.Qty) != 0 {
		t.Fatalf("after the slip: primary %v (was %v), second %v", in1.Qty, h.Qty, in2.Qty)
	}
	ts, _ := o.DPTransactions(ctx, T, "BH1001")
	if len(ts) == 0 || ts[len(ts)-1].BOID != second {
		t.Fatalf("transaction %+v", ts)
	}

	// The exchanges' UCC records carry every active account, primary first.
	set, _ := o.Settings(ctx, T)
	m := nseUCIMap(c, o.UCCDetailsFor(ctx, T, c.UCC), nil, set, "M1", time.Now(), NSEUCIReg{})
	if len(m.Records) == 0 {
		t.Fatalf("no NSE record: %v", m.Problems)
	}
	b := m.Records[0].Body
	if b["ccdBenAcctNo"] != prim || b["ccdPriSecDp"] != "P" || b["ccdBenAcctNo2"] != second || b["ccdPriSecDp2"] != "S" || b["ccdBenAcctNo3"] != other || len(m.Warnings) != 0 {
		t.Fatalf("NSE demat slots %q %q %q %q %q %v", b["ccdBenAcctNo"], b["ccdPriSecDp"], b["ccdBenAcctNo2"], b["ccdPriSecDp2"], b["ccdBenAcctNo3"], m.Warnings)
	}
	rec := bseUCCMap(c, o.bseUCCLoadSources(ctx, T, set), "Add", nil, time.Now())
	if rec.Values["BENEFICIALOWNACNTNO1"] != prim || rec.Values["BENEFICIALOWNACNTNO2"] != second || rec.Values["PRIMARYORSECONDARYDP2"] != "S" || rec.Values["BENEFICIALOWNACNTNO3"] != other {
		t.Fatalf("BSE demat slots %+v", rec.Values)
	}
	four := c
	four.Demats = append(demats(c), Demat{Depository: "CDSL", ID: "1304140000000998", Status: DematActive})
	var warns []string
	if got := uccDemats(four, "MCX", func(f string, a ...any) { warns = append(warns, f) }); len(got) != 3 || len(warns) != 1 {
		t.Fatalf("MCX takes 3: %d accounts, warnings %v", len(got), warns)
	}

	// CDSL's transaction file: the row's BO ID names the client and account.
	if n, _, err := o.loadCDSLTxns(ctx, T, "test", []xfile.Row{{Values: map[string]string{"ClntId": second, "TxnCd": "CRD", "Qty": "5.5", "ISIN": h.ISIN, "TxnID": "T900", "LineNb": "1", "ExecDt": "16102026", "TxnTyp": "OFF"}}}); err != nil || n != 1 {
		t.Fatalf("CDSL transactions: %d %v", n, err)
	}
	ts, _ = o.DPTransactions(ctx, T, "BH1001")
	got := ts[len(ts)-1]
	if got.BOID != second || got.Qty.Cmp(money.MustDec("5.5")) != 0 {
		t.Fatalf("CDSL transaction %+v", got)
	}

	// One DP bill for the client, with each account's lines.
	bills, err := o.RunDPBilling(ctx, T, "farhan.khan", D[:7])
	if err != nil {
		t.Fatal(err)
	}
	var bill DPBill
	for _, x := range bills {
		if x.UCC == "BH1001" {
			bill = x
		}
	}
	per := map[string]int{}
	for _, l := range bill.Lines {
		per[l.BOID]++
	}
	if bill.ID != D[:7]+"|BH1001" || per[prim] == 0 || per[second] == 0 || per[other] != 0 {
		t.Fatalf("bill %+v", bill)
	}

	// Making the second account the primary moves the holdings with it.
	ch, err = o.RequestChange(ctx, T, "priya.nair", "BH1001", "Demat account", map[string]string{"action": "primary", "id": second}, doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.DecideChange(ctx, T, "farhan.khan", ch.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	c, _ = o.Client(ctx, T, "BH1001")
	n1, _, _ := docs.Get[Holding](ctx, o.Docs, T, cHolding, holdingID("BH1001", h.ISIN))
	n2, _, _ := docs.Get[Holding](ctx, o.Docs, T, cHolding, holdingIDAt("BH1001", h.ISIN, prim))
	if c.Demat.ID != second || wq(n1.Qty) != 30 || n2.Qty.Cmp(h.Qty) != 0 || n2.BOID != prim {
		t.Fatalf("after the primary change: %s, primary key %v, old primary %v", c.Demat.ID, n1.Qty, n2.Qty)
	}
}

// BO accounts kept by UCC before several were allowed are re-keyed by BO
// ID, once.
func TestBOAccountsRekeyedByBOID(t *testing.T) {
	o, ctx := setup(t)
	old := BOAccount{UCC: "BH1002", BOID: "1208160000000555", Depository: "CDSL", Status: "Active", BSDA: true, Opened: "2023-04-01"}
	if err := docs.Upsert(ctx, o.Docs, T, cBO, "BH1002", old, "test"); err != nil {
		t.Fatal(err)
	}
	o.boKeyed.Delete(T) // as when the server starts
	b, ok := o.boAccount(ctx, T, old.BOID)
	if !ok || b.UCC != "BH1002" || !b.BSDA {
		t.Fatalf("re-keyed %+v %v", b, ok)
	}
	if m, _, _ := docs.Get[BOAccount](ctx, o.Docs, T, cBO, "BH1002"); m.MovedTo != old.BOID {
		t.Fatalf("the old record %+v", m)
	}
	o.boKeyed.Delete(T)
	if err := o.rekeyBO(ctx, T); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, x := range o.boAccounts(ctx, T) {
		if x.UCC == "BH1002" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d accounts after a second run", n)
	}
}

// The go-live imports: several DP accounts per client with the primary
// and joint holders, accounts with other DPs, holdings by account with
// fractional units, and a client of the DP only from loading to opening
// a trading account.
func TestDPOnlyClientsAndDematImports(t *testing.T) {
	o, ctx := migSetup(t)
	const date, isin1, isin2 = "2026-10-30", "INE000A01010", "INF000Z01012"
	load := func(src string, data []byte) []string {
		t.Helper()
		r, err := o.LoadFile(ctx, MT, "chk.one", src, "", strings.ToLower(src)+".csv", data)
		if err != nil {
			t.Fatalf("%s: %v %+v", src, err, r.Errors)
		}
		var w []string
		for _, e := range r.Warnings {
			w = append(w, e.Msg)
		}
		return w
	}
	cols := append(append([]string{}, cmCols...), "DPOnly")
	dpc := cmRow("TD004", "Test DP Client D", "ABCPD1234D", "9876500004")
	dpc[8], dpc[11] = "", "" // no segments, no running account settlement
	load("CLIENT-MASTER", csvOf(cols, append(cmRow("TA001", "Test Client A", "ABCPA1234A", "9876500001"), "N"), append(dpc, "Y")))
	d, _ := o.Client(ctx, MT, "TD004")
	if !d.DPOnly || len(d.Segments) != 0 || d.RAS != "" {
		t.Fatalf("DP-only client %+v", d)
	}
	bad := append(cmRow("TE005", "Test Client E", "ABCPE1234E", "9876500005"), "N")
	bad[8] = ""
	if r, err := o.LoadFile(ctx, MT, "chk.one", "CLIENT-MASTER", "", "bad.csv", csvOf(cols, bad)); err == nil || len(r.Errors) == 0 {
		t.Fatal("a trading client without segments loaded")
	}
	// The DP's accounts: two for TA001 (the second the primary, a joint
	// account), a closed and an open one for TD004.
	dpCols := []string{"UCC", "BOID", "Depository", "Status", "ClosedOn", "Primary", "Holder2Name", "Holder2PAN"}
	if w := load("DP-ACCOUNTS", csvOf(dpCols, []string{"TA001", "1234567800000001", "CDSL", "Active", "", "N", "", ""}, []string{"TA001", "1234567800000021", "CDSL", "Active", "", "Y", "Test Joint Holder", "ABCPJ1234J"},
		[]string{"TD004", "1234567800000004", "CDSL", "Closed", "2025-01-01", "", "", ""}, []string{"TD004", "1234567800000014", "CDSL", "Active", "", "", "", ""})); len(w) != 0 {
		t.Fatalf("DP-ACCOUNTS warnings %v", w)
	}
	a, _ := o.Client(ctx, MT, "TA001")
	d, _ = o.Client(ctx, MT, "TD004")
	if a.Demat.ID != "1234567800000021" || len(a.Demats) != 2 || d.Demat.ID != "1234567800000014" || len(d.Demats) != 2 {
		t.Fatalf("primaries %s %+v / %s %+v", a.Demat.ID, a.Demats, d.Demat.ID, d.Demats)
	}
	if b, _ := o.boAccount(ctx, MT, "1234567800000021"); len(b.JointHolders) != 1 || dematHolderNames(a.Name, b.JointHolders) != "Test Client A & Test Joint Holder" {
		t.Fatalf("joint holders %+v", b.JointHolders)
	}
	// Two primaries, or a BO ID of another client, are refused.
	if r, err := o.LoadFile(ctx, MT, "chk.one", "DP-ACCOUNTS", "", "dp2.csv", csvOf(dpCols, []string{"TA001", "1234567800000001", "CDSL", "Active", "", "Y", "", ""}, []string{"TA001", "1234567800000021", "CDSL", "Active", "", "Y", "", ""},
		[]string{"TD004", "1234567800000001", "CDSL", "Active", "", "", "", ""})); err == nil || len(r.Errors) < 2 {
		t.Fatalf("bad DP accounts loaded: %+v", r.Errors)
	}
	// An account with another DP.
	load("CLIENT-DEMATS", csvOf([]string{"UCC", "Depository", "DematID", "Primary", "Status"}, []string{"TA001", "NSDL", "IN30000011119999", "", "Active"}))
	a, _ = o.Client(ctx, MT, "TA001")
	ds := o.withDemats(ctx, MT, a).Demats
	own := map[string]bool{}
	for _, x := range ds {
		own[x.ID] = x.OwnDP
	}
	if a.Demat.ID != "1234567800000021" || len(ds) != 3 || own["IN30000011119999"] || !own["1234567800000001"] || !own["1234567800000021"] {
		t.Fatalf("after CLIENT-DEMATS %+v", ds)
	}
	// Holdings by account, fractional units exact.
	hCols := []string{"UCC", "ISIN", "Symbol", "Qty", "Pledged", "AvgCost", "BOID"}
	load("DP-HOLDINGS", csvOf(hCols, []string{"TA001", isin1, "TESTCO", "100", "0", "10", "1234567800000001"}, []string{"TA001", isin1, "TESTCO", "50", "0", "10", ""},
		[]string{"TA001", isin2, "TESTLIQ", "7.5", "0", "1000", ""}, []string{"TD004", isin2, "TESTLIQ", "125.432", "0", "1000", "1234567800000014"}))
	if r, err := o.LoadFile(ctx, MT, "chk.one", "DP-HOLDINGS", "", "dph.csv", csvOf(hCols, []string{"TA001", isin1, "TESTCO", "1.23456", "0", "10", ""})); err == nil || len(r.Errors) == 0 {
		t.Fatal("five decimal places loaded")
	}
	h1, _, _ := docs.Get[Holding](ctx, o.Docs, MT, cHolding, holdingIDAt("TA001", isin1, "1234567800000001"))
	h0, _, _ := docs.Get[Holding](ctx, o.Docs, MT, cHolding, holdingID("TA001", isin1))
	hd, _, _ := docs.Get[Holding](ctx, o.Docs, MT, cHolding, holdingID("TD004", isin2))
	if wq(h1.Qty) != 100 || wq(h0.Qty) != 50 || hd.Qty.String() != "125.432" {
		t.Fatalf("holdings %v / %v / %v", h1.Qty, h0.Qty, hd.Qty)
	}
	if v := hd.Value(money.MustDec("10.25")); v.Cmp(money.MustDec("1285.678")) != 0 {
		t.Fatalf("value %v", v)
	}

	// The DP-only client is no trading client.
	cs, _ := o.tradingClientMap(ctx, MT)
	if _, in := cs["TD004"]; in {
		t.Fatal("a DP-only client among the trading clients")
	}
	dl, _ := o.sellableDemat(ctx, MT, date, cs)
	if dl["TD004|"+isin2] != nil || dl["TA001|"+isin2] == nil || dl["TA001|"+isin2].Demat != 7 {
		t.Fatalf("front-office holdings %+v", dl["TA001|"+isin2]) // 7.5 units: 7 whole ones sellable
	}
	if items, err := o.nseUCIItems(ctx, MT, "M1", nil); err != nil {
		t.Fatal(err)
	} else {
		for _, it := range items {
			if it.c.UCC == "TD004" {
				t.Fatal("a DP-only client in NSE's UCC")
			}
		}
	}
	if items, _, err := o.bseUCCItems(ctx, MT); err != nil {
		t.Fatal(err)
	} else {
		for _, it := range items {
			if it.c.UCC == "TD004" {
				t.Fatal("a DP-only client in BSE's UCC")
			}
		}
	}
	if items, _, err := o.mcxUCCItems(ctx, MT, date); err != nil {
		t.Fatal(err)
	} else {
		for _, it := range items {
			if it.c.UCC == "TD004" {
				t.Fatal("a DP-only client in MCX's UCC")
			}
		}
	}
	// A credit balance is the client's, but there is no running account
	// settlement or risk row for it; and no contract note.
	if _, err := o.LoadFile(ctx, MT, "chk.one", "OPENING-BALANCES", date, "ob.csv", csvOf([]string{"Account", "Debit", "Credit"}, []string{"BANK:TESTBANK", "700.00", ""}, []string{"CLIENT:TD004", "", "500.00"}, []string{"CLIENT:TA001", "", "200.00"})); err != nil {
		t.Fatal(err)
	}
	rv, err := o.RAS(ctx, MT, date)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rv.Rows {
		if r.UCC == "TD004" {
			t.Fatal("a DP-only client in the running account settlement")
		}
	}
	if _, err := o.generateNotes(ctx, MT, "chk.one", date, nil); err != nil {
		t.Fatal(err)
	}
	if ns, _ := o.Notes(ctx, MT, date); len(ns) != 0 {
		t.Fatalf("contract notes %+v", ns)
	}
	if _, err := o.Assign(ctx, MT, "adm.one", "ucc:TD004", "STD", "2026-10-01"); err == nil {
		t.Fatal("a brokerage plan for a DP-only client")
	}

	// The DP bill posts to the client's ledger.
	month := date[:7]
	bills, err := o.RunDPBilling(ctx, MT, "chk.one", month)
	if err != nil {
		t.Fatal(err)
	}
	var bill DPBill
	for _, b := range bills {
		if b.UCC == "TD004" {
			bill = b
		}
	}
	if bill.Entry == "" || bill.Total <= 0 {
		t.Fatalf("DP bill %+v", bills)
	}
	funds, _ := o.clientFunds(ctx, MT, monthEnd(month), "")
	if funds["TD004"] != money.MustAmount("500.00")-bill.Total {
		t.Fatalf("ledger %s after a bill of %s", funds["TD004"], bill.Total)
	}

	// The DP statement and the portal show the fractional holding.
	st, err := o.DPStatement(ctx, MT, month, "TD004")
	if err != nil {
		t.Fatal(err)
	}
	if !reportHas(st, "125.432") {
		t.Fatalf("DP statement %+v", st.Sections)
	}
	if _, _, err := o.PortalAccess(ctx, MT, "chk.one", "TD004", nil, false); err != nil {
		t.Fatal(err)
	}
	pv, err := o.Portal(ctx, MT, "td004", date, "TD004")
	if err != nil {
		t.Fatal(err)
	}
	if !pv.Account.DPOnly || len(pv.Holdings) != 1 || pv.Holdings[0].Account != "1234567800000014" || pv.Holdings[0].Qty.String() != "125.432" || len(pv.DPBills) != 1 || len(pv.Account.Demats) != 2 {
		t.Fatalf("portal %+v %+v", pv.Account, pv.Holdings)
	}

	// A slip of 10.5 units, then a sale delivery of 100 whole units.
	book, err := o.IssueDISBook(ctx, MT, "mkr.one", "TD004", 5)
	if err != nil {
		t.Fatal(err)
	}
	x, err := o.EnterDIS(ctx, MT, "mkr.one", DIS{UCC: "TD004", Slip: book.From, Kind: "Off-market", ToBO: "1208160099999999", Reason: "Gift", ExecDate: date, Received: date, Lines: []DISLine{{ISIN: isin2, Qty: money.MustDec("10.5")}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.VerifyDIS(ctx, MT, "chk.one", x.ID, true, "", "Call ref C-1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := o.DISBatch(ctx, MT, "mkr.one", date); err != nil {
		t.Fatal(err)
	}
	if _, err := o.ExecuteDIS(ctx, MT, "mkr.one", x.ID, true, "", date); err != nil {
		t.Fatal(err)
	}
	hd, _, _ = docs.Get[Holding](ctx, o.Docs, MT, cHolding, holdingID("TD004", isin2))
	if hd.Qty.String() != "114.932" {
		t.Fatalf("after 10.5 out: %v", hd.Qty)
	}
	d, _ = o.Client(ctx, MT, "TD004")
	if err := o.debitAcross(ctx, MT, "mkr.one", d, isin2, "TESTLIQ", 100); err != nil {
		t.Fatal(err)
	}
	hd, _, _ = docs.Get[Holding](ctx, o.Docs, MT, cHolding, holdingID("TD004", isin2))
	if hd.Qty.String() != "14.932" {
		t.Fatalf("after 100 delivered: %v", hd.Qty)
	}

	// Opening a trading account: the segments and RAS choice, the UCC
	// upload stage, a different checker activates.
	_, _ = docs.Update(ctx, o.Docs, MT, cClient, "TD004", "test", func(c *Client) error { c.IncomeProof = false; return nil })
	if _, err := o.OpenTradingAccount(ctx, MT, "mkr.one", "TD004", TradingAccount{Segments: []string{"CM"}, RAS: "Quarterly"}); err == nil {
		t.Fatal("opened without the risk disclosure")
	}
	if _, err := o.OpenTradingAccount(ctx, MT, "mkr.one", "TD004", TradingAccount{Segments: []string{"CM", "FO"}, RAS: "Quarterly", RiskDisclosure: true}); err == nil {
		t.Fatal("derivatives without income proof")
	}
	d, err = o.OpenTradingAccount(ctx, MT, "mkr.one", "TD004", TradingAccount{Segments: []string{"CM"}, RAS: "Quarterly", RiskDisclosure: true})
	if err != nil {
		t.Fatal(err)
	}
	if d.DPOnly || d.Status != "UCC upload" || !nseUCIInScope(d, false) {
		t.Fatalf("after opening %+v", d)
	}
	if _, err := o.AdvanceStage(ctx, MT, "mkr.one", "TD004"); err == nil {
		t.Fatal("the maker activated")
	}
	if d, err = o.AdvanceStage(ctx, MT, "chk.one", "TD004"); err != nil || d.Status != "Active" {
		t.Fatalf("activation %+v %v", d, err)
	}
	if cs, _ := o.tradingClientMap(ctx, MT); cs["TD004"].UCC == "" {
		t.Fatal("not a trading client after opening")
	}
}

func reportHas(r ClientReport, s string) bool {
	for _, sec := range r.Sections {
		for _, row := range sec.Rows {
			for _, v := range row {
				if v == s {
					return true
				}
			}
		}
	}
	return false
}

// A migration with clients of the DP only, clients with two accounts with
// the DP and fractional units reconciles: every BO account is counted and
// the quantity matches exactly.
func TestMigrationDPAccountsReconcile(t *testing.T) {
	o, ctx := migSetup(t)
	var buf bytes.Buffer
	w, err := pkg.Create(&buf, mPass, "test tool")
	if err != nil {
		t.Fatal(err)
	}
	m := w.Manifest()
	m.Source = pkg.Source{System: "SharePro", Backups: []pkg.Backup{{Database: "TEST2627", Role: "company", FY: "2026-27", Used: true}}}
	m.Firms = []pkg.Firm{{Code: "01", Name: "Test Broking", Years: []string{"2026-27"}, AsOf: mAsOf}}
	cols := append(append([]string{}, cmCols...), "DPOnly")
	dpc := cmRow("TD004", "Test DP Client D", "ABCPD1234D", "9876500004")
	dpc[8], dpc[11] = "", ""
	writeCSV(t, w, "01", "CLIENT-MASTER", [][]string{cols, append(cmRow("TA001", "Test Client A", "ABCPA1234A", "9876500001"), "N"), append(cmRow("TB002", "Test Client B", "ABCPB1234B", "9876500002"), "N"), append(dpc, "Y")})
	writeCSV(t, w, "01", "DP-ACCOUNTS", [][]string{{"UCC", "BOID", "Depository", "Status", "ClosedOn"}, {"TA001", "1234567800000001", "CDSL", "Active", ""}, {"TA001", "1234567800000021", "CDSL", "Active", ""},
		{"TB002", "1234567800000002", "CDSL", "Active", ""}, {"TD004", "1234567800000004", "CDSL", "Closed", "2025-01-01"}, {"TD004", "1234567800000014", "CDSL", "Active", ""}})
	writeCSV(t, w, "01", "DP-HOLDINGS", [][]string{{"UCC", "ISIN", "Symbol", "Qty", "Pledged", "AvgCost", "BOID"}, {"TA001", "INE000A01010", "TESTCO", "100", "0", "10.50", "1234567800000001"},
		{"TA001", "INE000A01010", "TESTCO", "20", "0", "10.50", "1234567800000021"}, {"TB002", "INE000B01010", "TESTB", "50", "0", "20", ""}, {"TD004", "INF000Z01012", "TESTLIQ", "125.432", "0", "1000", ""}})
	w.AddCheck(pkg.Check{Firm: "01", Name: pkg.CheckClients, Value: "3"})
	w.AddCheck(pkg.Check{Firm: "01", Name: pkg.CheckDPAccounts, Value: "5"})
	w.AddCheck(pkg.Check{Firm: "01", Name: pkg.CheckDPQuantity, Value: "295.432"})
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	p := checked(t, o, ctx, buf.Bytes(), "dp.llmig")
	mg, err := o.ProposeMigration(ctx, MT, "adm.one", MigProposal{Package: p.ID, Firm: "01"})
	if err != nil {
		t.Fatal(err)
	}
	if mg, err = o.DecideMigration(ctx, MT, "chk.one", mg.ID, true, mPass, "checked"); err != nil || mg.Status != MigLoaded {
		t.Fatalf("load %v %s %s", err, mg.Status, mg.Error)
	}
	want := map[string]bool{pkg.CheckClients: true, pkg.CheckDPAccounts: true, pkg.CheckDPQuantity: true}
	for _, r := range mg.Recon.Rows {
		if want[r.Name] {
			if r.Status != "Pass" {
				t.Errorf("%s: %s, want %s (%+v)", r.Name, r.Ledgerline, r.Expected, r.Behind)
			}
			delete(want, r.Name)
		}
	}
	if len(want) > 0 {
		t.Fatalf("checks not run: %v", want)
	}
}

// A client of the DP only opened from the new-client flow: no segments,
// running account settlement or risk disclosure, and no UCC upload stage.
func TestDPOnlyNewClient(t *testing.T) {
	o, ctx := newOps(t)
	c := Client{Name: "Test Depository Only", Type: "Individual", PAN: "ABCPQ1234Q", DOB: "1985-02-03", Mobile: "9876500077", Email: "dponly@example.test", City: "Pune", State: "Maharashtra",
		Income: "5-10L", Bank: BankAccount{Bank: "Sample Bank", IFSC: "SBIN0001234", Account: "123456789077"}, DPOnly: true}
	trading := c
	trading.DPOnly = false
	if _, err := o.CreateApplication(ctx, T, "priya.nair", trading); err == nil {
		t.Fatal("a trading client without segments or risk disclosure")
	}
	n, err := o.CreateApplication(ctx, T, "priya.nair", c)
	if err != nil {
		t.Fatal(err)
	}
	if !n.DPOnly || len(n.Segments) != 0 || n.RAS != "" {
		t.Fatalf("created %+v", n)
	}
	for n.Status != "Active" {
		was := n.Status
		if n, err = o.AdvanceStage(ctx, T, "farhan.khan", n.UCC); err != nil {
			t.Fatal(err)
		}
		if n.Status == "UCC upload" {
			t.Fatalf("a DP-only client at the UCC upload stage (from %s)", was)
		}
	}
	if cs, _ := o.tradingClientMap(ctx, T); cs[n.UCC].UCC != "" {
		t.Fatal("a DP-only client among the trading clients")
	}
}
