package ops

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
	"ledgerline/internal/xfile"
)

// ccSection finds a section of the Clearing data screen.
func ccSection(t *testing.T, o *Ops, id string) CCSection {
	t.Helper()
	secs, err := o.ClearingData(t.Context(), T, D)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range secs {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("no section %s", id)
	return CCSection{}
}

// mcxClient is a client of the sample day with an MCX trade, and the trade.
func mcxClient(t *testing.T, o *Ops) (string, string, string) {
	t.Helper()
	ts, _ := o.exchangeTrades(t.Context(), T, D)
	for _, x := range ts {
		if x.Exchange == "MCX" && x.InstrumentType == "FUT" {
			return x.UCC, x.Symbol, x.Expiry
		}
	}
	t.Fatal("no MCX trade in the sample day")
	return "", "", ""
}

func productRow(instr, sym, typ, exp, strike, uid, under, lot, deleted string) xfile.Row {
	d, _ := time.Parse("2006-01-02", exp)
	return xr("", "Instrument Name", instr, "Instrument ID", "1", "Unique Identifier", uid, "Underlying Unique Identifier", under, "Symbol", sym, "Underlying Asset", sym,
		"Options Type", typ, "Strike Price", strike, "Expiry Date", strings.ToUpper(d.Format("02Jan2006")), "Product Description", sym+" test contract",
		"Quotation Quantity", "1", "Quotation Unit", "BBL", "Tradable Lot", lot, "Price Tick", "1.0000", "Record Deleted", deleted, "Delivery Unit", "BBL",
		"Delivery Mode", "-1", "Price Numerator", "1", "Price Denominator", "1", "General Numerator", "1", "General Denominator", "1", "Currency Code", "INR")
}

func TestMCXProductMasterAndBhavcopy(t *testing.T) {
	o, ctx := setup(t)
	o.Now = func() time.Time { return time.Date(2026, 10, 16, 9, 0, 0, 0, ist) }
	_, sym, exp := mcxClient(t, o)
	rows := []xfile.Row{
		productRow("FUTCOM", sym, "", exp, "", "1001", "0", "100", "N"), // the underlying asset
		productRow("FUTCOM", sym, "", exp, "", "1002", "1001", "100", "N"),
		productRow("OPTFUT", sym, "CE", exp, "5500.0000", "1003", "1001", "100", "N"),
		productRow("FUTCOM", sym, "", "2026-09-30", "", "1004", "1001", "100", "N"), // expired
		productRow("FUTCOM", "OLDSYM", "", exp, "", "1005", "1001", "100", "Y"),     // deleted
	}
	r := loadX(t, o, "MCX_ProductMaster.csv", xwrite(t, "mcx-product-master", rows...))
	if r.Format != "mcx-product-master" || r.Added != 2 {
		t.Fatalf("%+v", r)
	}
	m, _ := o.MCXContracts(ctx, T, sym)
	if len(m.Contracts) != 2 || m.Contracts[1].Type != "FUT" || m.Contracts[1].Lot != 100 || m.Contracts[1].Settlement != "Cash settled" || m.Contracts[0].Strike != "5500" {
		t.Fatalf("%+v", m.Contracts)
	}
	_, look, _ := o.Market(ctx, T, D)
	if i, ok := look.Get("MCX", "COM", sym+"|CE|"+exp+"|5500"); !ok || i.Lot != 100 {
		t.Fatalf("option contract not in the market data: %+v %v", i, ok)
	}
	// The MCX bhavcopy prices the contract; its market lot of 1 does not
	// replace the product master's lot.
	d1 := strings.Split(exp, "-")
	b := xwrite(t, "mcx-bhavcopy", xr("", "TradDt", "16-10-2026", "BizDt", "16-10-2026", "Sgmt", "CO", "Src", "MCX", "FinInstrmTp", "COF", "TckrSymb", sym,
		"XpryDt", d1[2]+"-"+d1[1]+"-"+d1[0], "ClsPric", "5500.00", "SttlmPric", "5501.00", "LastPric", "5499.00", "NewBrdLotQty", "1"))
	r = loadX(t, o, "Bhavcopy_MCX_CO_0_0_0_20261016_F_0000.csv", b)
	if r.Format != "mcx-bhavcopy" || !strings.Contains(r.Loaded, "MCX COM") {
		t.Fatalf("%+v", r)
	}
	_, look, _ = o.Market(ctx, T, D)
	if i, ok := look.Get("MCX", "COM", sym+"|FUT|"+exp+"|"); !ok || i.Close.String() != "5501" || i.Lot != 100 {
		t.Fatalf("bhavcopy price %+v %v", i, ok)
	}
}

// The product master of a whole day (some 17,000 lines) loads in one pass.
func TestMCXProductMasterSize(t *testing.T) {
	o, _ := setup(t)
	o.Now = func() time.Time { return time.Date(2026, 10, 16, 9, 0, 0, 0, ist) }
	var rows []xfile.Row
	for i := 0; i < 17000; i++ {
		rows = append(rows, productRow("OPTFUT", fmt.Sprintf("SYM%d", i%300), []string{"CE", "PE"}[i%2], "2026-12-17", fmt.Sprintf("%d.0000", 1000+i), fmt.Sprint(10000+i), "1", "10", "N"))
	}
	b := xwrite(t, "mcx-product-master", rows...)
	start := time.Now()
	r := loadX(t, o, "MCX_ProductMaster.csv", b)
	if r.Added != 17000 {
		t.Fatalf("%+v", r)
	}
	if d := time.Since(start); d > 30*time.Second {
		t.Fatalf("17,000 lines took %v", d)
	}
	t.Logf("17,000 product master lines in %v", time.Since(start))
}

func TestMCXObligationMarginsPenalties(t *testing.T) {
	o, ctx := setup(t)
	ucc, sym, _ := mcxClient(t, o)
	mine := money.Dec{}
	if st, err := o.Obligations(ctx, T, D); err == nil {
		for _, m := range st.MTM {
			if m.UCC == ucc && m.Segment == "MCX-COM" {
				mine = mine.Add(m.Amount)
			}
		}
	}
	nobl := func(mtm money.Dec) []byte {
		return xwrite(t, "mcx-obligation-tm", xr("10", "Date", "16102026", "CM ID", "1234", "TM ID", "56845", "Due Date Of Payment", "19102026", "Number of Detail Records at End Client Level", "1",
			"MTM Profit/Loss", mtm.Fixed(2), "Net Pay-In/Pay-Out", mtm.Fixed(2), "Currency Code", "INR"),
			xr("20", "Account Type", "C", "Account ID", ucc, "Instrument Name", "FUTCOM", "Symbol", sym, "MTM Profit/Loss", mtm.Fixed(2), "Currency Code", "INR"))
	}
	r := loadX(t, o, "MCX_NOBL_56845_20261016.csv", nobl(mine))
	if r.Format != "mcx-obligation-tm" {
		t.Fatalf("%+v", r)
	}
	if s := ccSection(t, o, "mcx_obligation"); !s.Loaded || s.Diffs != 0 || len(s.Rows) == 0 {
		t.Fatalf("obligation matched: %+v", s)
	}
	loadX(t, o, "MCX_NOBL_56845_20261016.csv", nobl(mine.Add(money.MustDec("50"))))
	if s := ccSection(t, o, "mcx_obligation"); s.Diffs != 1 {
		t.Fatalf("obligation break not raised: %+v", s)
	}

	// Margin charged (MCX_MRG) and peak snapshots: live RMS and margin reporting.
	loadX(t, o, "MCX_MRG_56845_20261016.csv", xwrite(t, "mcx-margin-tm",
		xr("10", "Date", "16102026", "CM ID", "1234", "TM ID", "56845", "Number of Detail Records at Summary - End Client Level", "1", "Actual Margin Levied", "1000.00"),
		xr("30", "TM ID", "56845", "Account Type", "C", "Account ID", ucc, "Actual Margin Levied", "1000.00", "Net Buy Premium", "100.00", "Extreme Loss Margin", "50.00")))
	loadX(t, o, "MCX_PeakMargin56845_20261016_01.csv", xwrite(t, "mcx-peak-margin-tm",
		xr("", "Business Date", "16OCT2026", "Snapshot Interval", "11:00:00", "CM ID", "1234", "TM ID", "56845", "Client ID", ucc, "Total Upfront Margin", "900000.00", "Short allocation", "10.00"),
		xr("", "Business Date", "16OCT2026", "Snapshot Interval", "11:00:00", "CM ID", "1234", "TM ID", "56845", "Client ID", "*OWN*", "Total Upfront Margin", "5.00")))
	lv, err := o.LiveRisk(ctx, T, D, "")
	if err != nil {
		t.Fatal(err)
	}
	seen := false
	for _, x := range lv.Rows {
		if x.UCC != ucc {
			continue
		}
		seen = true
		if x.ExchangeMargin == nil || x.ExchangeMargin.Cmp(money.MustDec("900000")) < 0 || x.Margin.Cmp(money.MustDec("900000")) < 0 {
			t.Fatalf("live RMS did not take MCXCCL's margin: %+v", x)
		}
	}
	if !seen {
		t.Fatalf("%s not in live RMS", ucc)
	}
	if !strings.HasPrefix(lv.ExchangeMarginAt, D) {
		t.Fatalf("margin time %q", lv.ExchangeMarginAt)
	}

	// The penalty MCXCCL levied (NCMR) is the client's row penalty; with
	// pass-through on it waits for a person, and nothing is debited.
	set, _ := o.Settings(ctx, T)
	set.PenaltyPassThrough = true
	if err := docs.Upsert(ctx, o.Docs, T, cSettings, "main", set, "test"); err != nil {
		t.Fatal(err)
	}
	lib, _ := XFiles()
	ns, _ := lib.Get("mcx-ncmr")
	var h []string
	for _, c := range ns.Columns {
		h = append(h, c.Name)
	}
	line := make([]string, len(h))
	put := func(k, v string) {
		for i, c := range ns.Columns {
			if c.Name == k {
				line[i] = v
			}
		}
	}
	put("Trade Date", "16-Oct-26")
	put("Client Code", ucc)
	put("Shortfall on which penalty has been levied", "5000.00")
	put("Applicable Penalty %", "0.50")
	put("Penalty Amount", "25.00")
	ncmr := "TRADE DATE,CM ID,CM NAME,TM ID,TM NAME,CLIENT CODE" + strings.Repeat(",X", len(h)-6) + "\n" + strings.Join(line, ",") + "\n"
	r = loadX(t, o, "MCX_NCMR_56845_20261016.csv", []byte(ncmr))
	if r.Format != "mcx-ncmr" {
		t.Fatalf("%+v", r)
	}
	rep, err := o.Margins(ctx, T, D)
	if err != nil {
		t.Fatal(err)
	}
	var row *MarginRow
	for i := range rep.Rows {
		if rep.Rows[i].UCC == ucc {
			row = &rep.Rows[i]
		}
	}
	if row == nil || row.Penalty.String() != "25" || !strings.Contains(row.PenaltyBy, "MCXCCL") || row.CCMargin == nil || row.CCMargin.String() != "1150" || row.CCPeak == nil || row.CCPeakAt != "11:00:00" {
		t.Fatalf("margin reporting row %+v", row)
	}
	ws, _ := o.WorkItems(ctx, T, "Open")
	found := false
	for _, w := range ws {
		if w.Process == wPenaltyPass && w.UCC == ucc && strings.Contains(w.Detail, "MCXCCL levied") {
			found = true
		}
	}
	if !found {
		t.Fatal("no pass-through decision for the levied penalty")
	}
	st, _ := o.App.Ledger.Statement(ctx, T, ledger.ClientAccount(ucc), "", D)
	for _, l := range st.Lines {
		if strings.Contains(strings.ToLower(l.Narration), "penalty") {
			t.Fatalf("penalty debited without approval: %+v", l)
		}
	}
	if s := ccSection(t, o, "mcx_penalty"); s.Diffs != 1 || s.Rows[0][5] != "Borne by the firm" {
		t.Fatalf("%+v", s)
	}
	if s := ccSection(t, o, "mcx_margin"); !s.Loaded || len(s.Rows) == 0 {
		t.Fatalf("%+v", s)
	}
}

func TestMCXAllocationTaxesPositionsOrders(t *testing.T) {
	o, ctx := setup(t)
	ucc, sym, exp := mcxClient(t, o)

	// Web allocation: the commodity allocation level.
	loadX(t, o, "MCX_WebAllocationDeallcoation56845_20261016.csv", xwrite(t, "mcx-web-allocation",
		xr("", "CM ID", "1234", "TM ID", "56845", "Allocation at EoD", "100000.00"),
		xr("", "CM ID", "1234", "TM ID", "56845", "End Client/CP Code", ucc, "Allocation at EoD", "3000.00", "OWN Collateral at EoD", "0.00")))
	v, err := o.AllocationScreen(ctx, T, D)
	if err != nil {
		t.Fatal(err)
	}
	ok := false
	for _, l := range v.Levels {
		if l.Segment == "COM" && l.UCC == ucc && l.Amount.String() == "3000" && strings.HasPrefix(l.Source, "MCXCCL web allocation") {
			ok = true
		}
	}
	if !ok {
		t.Fatalf("levels %+v", v.Levels)
	}
	if s := ccSection(t, o, "mcx_collateral"); len(s.Rows) != 1 || s.Rows[0][2] != "3000.00" {
		t.Fatalf("%+v", s)
	}
	// The commodity level never goes into NSE Clearing's allocation request.
	ms, err := o.PrepareAllocationBatch(ctx, T, "vikram.shah", D, AllocEOD)
	if err == nil {
		for _, m := range ms {
			for _, rc := range m.Records {
				if rc.Segment == "COM" || rc.Segment == "CO" {
					t.Fatalf("commodity level in an NSE Clearing request: %+v", rc)
				}
			}
		}
	}

	// CTT and stamp duty (UDiFF): only the per client and contract lines count.
	ctt := xwrite(t, "mcxccl-ctt",
		xr("", "RptHdr", "10", "Sgmt", "CO", "Src", "MCXCCL", "TradDt", D, "TtlTaxs", "100.00"),
		xr("", "RptHdr", "20", "Sgmt", "CO", "Src", "MCXCCL", "TradDt", D, "ClntId", ucc, "TtlTaxs", "100.00"),
		xr("", "RptHdr", "40", "Sgmt", "CO", "Src", "MCXCCL", "TradDt", D, "ClntId", ucc, "TckrSymb", sym, "TtlTaxs", "100.00", "TaxblSellFutrsVal", "548200.00"))
	loadX(t, o, "CTT_MCXCCL_CO_0_TM_56845_20261016_F_0000.csv", ctt)
	rec, _ := o.STTReconcile(ctx, T, D)
	got := false
	for _, x := range rec.Rows {
		if x.Exchange == "MCX" && x.Segment == "COM" {
			got = x.HasStmt && x.ExchSTT.Dec().String() == "100"
		}
	}
	if !got {
		t.Fatalf("CTT %+v", rec.Rows)
	}
	stamp := xwrite(t, "mcxccl-stamp",
		xr("", "RptHdr", "20", "Sgmt", "CO", "Src", "MCXCCL", "ClctnDt", D, "BrkrOrCtdnPtcptId", "56845", "ClntId", ucc, "StmpDtyAmt", "3.00"),
		xr("", "RptHdr", "40", "Sgmt", "CO", "Src", "MCXCCL", "ClctnDt", D, "ClntId", ucc, "TckrSymb", sym, "StmpDtyAmt", "3.00"))
	loadX(t, o, "Stamp_MCXCCL_CO_0_TM_56845_20261016_F_0000.csv", stamp)
	found := false
	for _, x := range ccSection(t, o, "stamp").Rows {
		if strings.HasPrefix(x[0], "MCX") && x[1] == ucc {
			found = x[3] == "3.00"
		}
	}
	if !found {
		t.Fatalf("stamp %+v", ccSection(t, o, "stamp").Rows)
	}

	// Positions (UDiFF), MCXCCL's own sample file.
	b, err := os.ReadFile("../xfile/testdata/mcxccl/Position_MCXCCL_CO_0_TM_33333_20231205_F_0000.csv")
	if err != nil {
		t.Fatal(err)
	}
	r := loadX(t, o, "Position_MCXCCL_CO_0_TM_33333_20231205_F_0000.csv", b)
	if r.Format != "mcxccl-position" || r.Added == 0 || !strings.Contains(r.Loaded, "2023-12-05") {
		t.Fatalf("%+v", r)
	}
	// The UDiFF margin and penalty samples load too.
	for _, n := range []string{"Margin_MCXCCL_CO_0_TM_88888_20231106_F_0000.csv", "Penalty_MCXCCL_CO_0_TM_11111_20240113_F_0000.csv"} {
		b, _ := os.ReadFile("../xfile/testdata/mcxccl/" + n)
		if r := loadX(t, o, n, b); !strings.HasPrefix(r.Format, "mcxccl-") {
			t.Fatalf("%+v", r)
		}
	}

	// The order log: order-to-trade ratio on MCX.
	d, _ := time.Parse("2006-01-02", exp)
	var ords []xfile.Row
	for i := 0; i < 3; i++ {
		ords = append(ords, xr("", "Order Number", fmt.Sprint(9001+i), "Order Trade Status", "5", "Instrument Name", "FUTCOM", "Symbol", sym, "Expiry Date", strings.ToUpper(d.Format("02Jan2006")),
			"User Id", "17", "Buy/Sell Indicator", "1", "Price", "5480.0000", "Account Type", "1", "Account ID", ucc))
	}
	r = loadX(t, o, "MCX_16102026ORD_1.csv", xwrite(t, "mcx-orders", ords...))
	if r.Format != "mcx-orders" || !strings.Contains(r.Loaded, "order-to-trade") {
		t.Fatalf("%+v", r)
	}
	day, _, err := docs.Get[OTRDay](ctx, o.Docs, T, cOTRDay, "MCX|"+D)
	if err != nil || len(day.Lines) == 0 {
		t.Fatalf("OTR %+v %v", day, err)
	}
	// The checklist ticks the files off.
	cl, _ := o.DailyFileChecklist(ctx, T, D)
	st := map[string]string{}
	for _, it := range cl.Items {
		st[it.ID] = it.Status
	}
	for _, id := range []string{"mcx-weballoc", "mcx-ctt", "mcx-stamp", "mcx-orders"} {
		if st[id] != "Received" {
			t.Errorf("%s: %q", id, st[id])
		}
	}
}
