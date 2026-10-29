package ops

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// A small NAV file in AMFI's layout. The schemes and ISINs are made up.
const mfNAVSample = "Scheme Code;ISIN Div Payout/ ISIN Growth;ISIN Div Reinvestment;Scheme Name;Net Asset Value;Date\r\n" +
	"\r\n" +
	"Open Ended Schemes(Equity Scheme - Large Cap Fund)\r\n" +
	"\r\n" +
	"Sample Mutual Fund\r\n" +
	"\r\n" +
	"100001;INF000A01011;-;Sample Bluechip Fund - Regular Plan - Growth;52.4100;28-Oct-2026\r\n" +
	"100002;INF000A01029;-;Sample Bluechip Fund - Direct Plan - Growth;58.1200;28-Oct-2026\r\n" +
	"100003;INF000A01037;INF000A01045;Sample Bluechip Fund - Regular Plan - IDCW;21.0500;28-Oct-2026\r\n" +
	"\r\n" +
	"Open Ended Schemes(Debt Scheme - Liquid Fund)\r\n" +
	"\r\n" +
	"Sample Mutual Fund\r\n" +
	"\r\n" +
	"100010;INF000A01102;-;Sample Liquid Fund - Regular Plan - Growth;2650.1234;28-Oct-2026\r\n" +
	"100011;INF000A01110;-;Sample Liquid Fund - Direct Plan - Growth;N.A.;28-Oct-2026\r\n"

const (
	isinBlueReg  = "INF000A01011"
	isinBlueDir  = "INF000A01029"
	isinBlueIDCW = "INF000A01037"
	isinLiquid   = "INF000A01102"
)

type mfEnv struct {
	o        *Ops
	ctx      context.Context
	ucc      string
	ucc2     string
	nse      *nseFake
	bse      *bseFake
	nseURL   string
	bseURL   string
	setNow   func(string)
	maker    string
	checker  string
	complian string
}

func at(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, ist)
	if err != nil {
		panic(err)
	}
	return t
}

// mfClients picks active individual clients with what MF registration
// needs.
func mfClients(t *testing.T, o *Ops, ctx context.Context) []Client {
	cs, err := o.Clients(ctx, T)
	if err != nil {
		t.Fatal(err)
	}
	var out []Client
	for _, c := range cs {
		if c.Status == "Active" && len(c.Demat.ID) == 16 && c.Bank.Account != "" && c.Bank.IFSC != "" && c.PAN != "" && !c.Blocked &&
			!mfKYCBlocked(c.KRAStatus) && reEmail.MatchString(c.Email) && !strings.HasSuffix(c.Email, ".invalid") && reMobile.MatchString(c.Mobile) &&
			strings.EqualFold(c.Type, "Individual") && c.City != "" {
			out = append(out, c)
		}
	}
	if len(out) < 3 {
		t.Fatalf("only %d usable clients in the sample", len(out))
	}
	return out
}

func mfProfileFor(c Client) MFProfileUpdate {
	return MFProfileUpdate{Mode: "Demat", TaxStatus: "01", Occupation: "02", Gender: "F", Address: "12 Sample Street", Pincode: "400001", StateCode: "MA", AccountType: "SB",
		FATCA: true, Nominees: []MFNominee{{Name: "Sample Nominee", Relation: "Spouse", Share: "100", IDType: "1", ID: "ABCDE1234F"}}, NomAuth: "O"}
}

func mfSetup(t *testing.T) *mfEnv {
	t.Helper()
	o, ctx := setup(t)
	e := &mfEnv{o: o, ctx: ctx, maker: "priya.nair", checker: "farhan.khan", complian: "vikram.shah"}
	e.setNow = func(s string) { tm := at(s); o.Now = func() time.Time { return tm } }
	e.setNow("2026-10-29 10:00")
	var nseSrv, bseSrv *httptest.Server
	e.nse, nseSrv = newNSEFake(t)
	e.bse, bseSrv = newBSEFake(t)
	oldN, oldB := nseMFHTTP, bseMFHTTP
	nseMFHTTP, bseMFHTTP = nseSrv.Client(), bseSrv.Client()
	t.Cleanup(func() { nseMFHTTP, bseMFHTTP = oldN, oldB })
	e.nseURL, e.bseURL = nseSrv.URL, bseSrv.URL+"/MFOrderEntry/MFOrder.svc/Secure"
	if _, err := o.SaveConnection(ctx, T, "admin", connNSEMFInvest, map[string]string{"member": nseFakeMember, "user": nseFakeUser, "env": "UAT", "url": e.nseURL, "fetch": "On"},
		map[string]string{"secret": nseFakeSecret, "license_key": nseFakeKey}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.SaveConnection(ctx, T, "admin", connBSEStarMF, map[string]string{"member": "6127", "user": "612701", "env": "Demo", "url": e.bseURL},
		map[string]string{"password": "pw-test", "passkey": "pk1"}); err != nil {
		t.Fatal(err)
	}
	days := 3
	if _, err := o.SaveMFSettings(ctx, T, e.complian, MFSettings{Route: MFBoth, ARN: "ARN-99999", EUINs: []MFEUIN{{User: e.maker, EUIN: "E123456"}},
		DirectBasis: MFDirectBases[0], Platforms: []string{"NSE", "BSE"}, TrailPct: map[string]string{"*": "0.75"}, SIPAlertDays: &days}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.LoadMFNAV(ctx, T, e.maker, "NAVAll.txt", []byte(mfNAVSample)); err != nil {
		t.Fatal(err)
	}
	codes := "Scheme Code,ISIN,AMC Code,Purchase Allowed,Redemption Allowed,SIP Allowed,Minimum Purchase\n" +
		"SBLUE-GR,INF000A01011,SAMPLEMF_MF,Y,Y,Y,500\nSBLUE-DGR,INF000A01029,SAMPLEMF_MF,Y,Y,Y,500\nSBLUE-IDCW,INF000A01037,SAMPLEMF_MF,Y,Y,N,500\nSLIQ-GR,INF000A01102,SAMPLEMF_MF,Y,Y,N,500\n"
	cols := map[string]string{"code": "Scheme Code", "amc_code": "AMC Code", "purchase": "Purchase Allowed", "redeem": "Redemption Allowed", "sip": "SIP Allowed", "min_purchase": "Minimum Purchase"}
	if r, err := o.ImportMFFile(ctx, T, e.maker, MFImportRequest{Kind: "scheme-codes", Platform: "NSE", Columns: cols, SaveAs: "nse-schemes"}, []byte(codes)); err != nil || r.Applied != 4 {
		t.Fatalf("NSE codes %+v %v", r, err)
	}
	bseCodes := strings.NewReplacer("SBLUE-GR", "02-GR", "SBLUE-DGR", "02D-GR", "SBLUE-IDCW", "02-DP", "SLIQ-GR", "L1-GR").Replace(codes)
	if r, err := o.ImportMFFile(ctx, T, e.maker, MFImportRequest{Kind: "scheme-codes", Platform: "BSE", Mapping: "nse-schemes"}, []byte(bseCodes)); err != nil || r.Applied != 4 {
		t.Fatalf("BSE codes %+v %v", r, err)
	}
	cs := mfClients(t, o, ctx)
	e.ucc, e.ucc2 = cs[0].UCC, cs[1].UCC
	for _, c := range cs[:2] {
		// The sample has no dates of birth; the platforms need one.
		if _, err := docs.Update(ctx, o.Docs, T, cClient, c.UCC, "test", func(x *Client) error { x.DOB = "1985-04-12"; return nil }); err != nil {
			t.Fatal(err)
		}
		if _, err := o.SaveMFProfile(ctx, T, e.maker, c.UCC, mfProfileFor(c)); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

func (e *mfEnv) register(t *testing.T, ucc string) {
	t.Helper()
	o, ctx := e.o, e.ctx
	p, err := o.RegisterMFClient(ctx, T, e.maker, ucc, "NSE")
	if err != nil || p.Platforms["NSE"].Status != "Submitted" {
		t.Fatalf("NSE registration %+v %v", p.Platforms, err)
	}
	for _, pl := range []string{"NSE", "BSE"} {
		if _, err := o.MarkMFRegistration(ctx, T, e.maker, ucc, pl, "Registered", "authorised by the investor"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMFSettingsRules(t *testing.T) {
	o, ctx := newOps(t)
	if _, err := o.SaveMFSettings(ctx, T, "priya.nair", MFSettings{Route: MFRegular, ARN: "ARN-1"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker saved MF settings: %v", err)
	}
	for _, c := range []struct {
		s    MFSettings
		want string
	}{
		{MFSettings{Route: MFRegular}, "ARN"},
		{MFSettings{Route: MFDirect}, "basis"},
		{MFSettings{Route: MFBoth, ARN: "ARN-1", DirectBasis: MFDirectBases[0], EUINs: []MFEUIN{{User: "priya.nair", EUIN: "E12"}}}, "E followed by 6 digits"},
		{MFSettings{Route: MFRegular, ARN: "ARN-1", EUINs: []MFEUIN{{User: "nobody", EUIN: "E123456"}}}, "not a staff user"},
		{MFSettings{Route: MFRegular, ARN: "ARN-1", Platforms: []string{"MFU"}}, "BSE or NSE"},
		{MFSettings{Route: MFRegular, ARN: "ARN-1", TrailPct: map[string]string{"*": "5"}}, "between 0 and 3"},
		{MFSettings{Route: "Advice"}, "Regular, Direct or Both"},
	} {
		if _, err := o.SaveMFSettings(ctx, T, "vikram.shah", c.s); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: %v (want %q)", c.s, err, c.want)
		}
	}
	s, err := o.SaveMFSettings(ctx, T, "vikram.shah", MFSettings{Route: MFBoth, ARN: "arn-12345", DirectBasis: MFDirectBases[0], Platforms: []string{"nse", "NSE"}})
	if err != nil || s.ARN != "ARN-12345" || len(s.Platforms) != 1 || s.DirectConfirmedBy != "vikram.shah" {
		t.Fatalf("%+v %v", s, err)
	}
}

func TestMFNAVFile(t *testing.T) {
	rows, date, err := parseAMFINAV([]byte(mfNAVSample))
	if err != nil || date != "2026-10-28" || len(rows) != 4 {
		t.Fatalf("%d rows, %s, %v", len(rows), date, err)
	}
	if rows[0].AMC != "Sample Mutual Fund" || !strings.Contains(rows[0].Category, "Large Cap") || rows[3].Category != "Open Ended Schemes(Debt Scheme - Liquid Fund)" {
		t.Fatalf("headings: %+v", rows)
	}
	if _, _, err := parseAMFINAV([]byte("ISIN,NAV\nX,1\n")); err == nil {
		t.Fatal("a non-AMFI file was read")
	}
	o, ctx := newOps(t)
	st, err := o.LoadMFNAV(ctx, T, "priya.nair", "NAVAll.txt", []byte(mfNAVSample))
	if err != nil || st.Schemes != 5 || st.Changed != 5 || st.NAVDate != "2026-10-28" {
		t.Fatalf("%+v %v", st, err)
	}
	if st, _ := o.LoadMFNAV(ctx, T, "priya.nair", "again", []byte(mfNAVSample)); st.Changed != 0 {
		t.Fatalf("an unchanged file rewrote %d schemes", st.Changed)
	}
	reg, _ := o.mfScheme(ctx, T, isinBlueReg)
	dir, _ := o.mfScheme(ctx, T, isinBlueDir)
	idcw2, _ := o.mfScheme(ctx, T, "INF000A01045")
	liq, _ := o.mfScheme(ctx, T, isinLiquid)
	if reg.Plan != MFRegular || dir.Plan != MFDirect || idcw2.NAV.String() != "21.05" || !liq.Liquid() || reg.Liquid() || reg.MonthEnd["2026-10"] != "52.41" {
		t.Fatalf("%+v %+v %+v %+v", reg, dir, idcw2, liq)
	}
	if _, err := o.mfScheme(ctx, T, "INF000A01110"); err == nil {
		t.Fatal("a scheme without a NAV (N.A.) was loaded")
	}
	ss, _ := o.MFSchemes(ctx, T, "bluechip", MFDirect, "", 10)
	if len(ss) != 1 || ss[0].ISIN != isinBlueDir {
		t.Fatalf("search %+v", ss)
	}

	// Fetched from AMFI on the schedule; a failed fetch asks for an upload.
	calls := 0
	ok := true
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if !ok {
			w.WriteHeader(503)
			return
		}
		w.Write([]byte(strings.ReplaceAll(mfNAVSample, "28-Oct-2026", "29-Oct-2026")))
	}))
	defer srv.Close()
	oldURL, oldHTTP := mfNAVURL, mfNAVHTTP
	mfNAVURL, mfNAVHTTP = srv.URL+"/spages/NAVAll.txt", srv.Client()
	defer func() { mfNAVURL, mfNAVHTTP = oldURL, oldHTTP }()
	if _, err := o.SaveMFSettings(ctx, T, "vikram.shah", MFSettings{NAVFetch: true}); err != nil {
		t.Fatal(err)
	}
	tick := func(when string) { tm := at(when); o.Now = func() time.Time { return tm }; o.mfTick(ctx, T, tm) }
	ok = false
	tick("2026-10-29 22:00")
	st, _, _ = docs.GetOr(ctx, o.Docs, T, cMFState, "nav", MFNAVState{})
	if calls != 1 || !strings.Contains(st.LastError, "upload a copy") || st.NAVDate != "2026-10-28" {
		t.Fatalf("failed fetch: %d calls, %+v", calls, st)
	}
	tick("2026-10-29 22:30") // within the hour: no retry
	ok = true
	tick("2026-10-29 23:05")
	st, _, _ = docs.GetOr(ctx, o.Docs, T, cMFState, "nav", MFNAVState{})
	if calls != 2 || st.NAVDate != "2026-10-29" || st.LastError != "" || st.Source != "AMFI" {
		t.Fatalf("fetch: %d calls, %+v", calls, st)
	}
	tick("2026-10-30 07:30") // the next morning: already in
	tick("2026-10-30 14:00") // during the day: never
	if calls != 2 {
		t.Fatalf("fetched again: %d", calls)
	}
}

func TestMFNSEOrders(t *testing.T) {
	e := mfSetup(t)
	o, ctx, ucc := e.o, e.ctx, e.ucc
	in := MFOrderInput{UCC: ucc, Platform: "NSE", Type: "Purchase", ISIN: isinBlueReg, Amount: "5000"}
	if _, err := o.PlaceMFOrder(ctx, T, e.maker, in); err == nil || !strings.Contains(err.Error(), "not registered on NSE MF Invest") {
		t.Fatalf("ordered before registration: %v", err)
	}
	// Registration through the 183-column API.
	e.register(t, ucc)
	reg := e.nse.last("registration/CLIENTCOMMON183")
	c, _ := o.Client(ctx, T, ucc)
	if reg["client_code"] != ucc || reg["primary_holder_pan"] != c.PAN || reg["tax_status"] != "01" || reg["client_type"] != "D" || reg["holding_nature"] != "SI" ||
		reg["nomination_opt"] != "Y" || reg["nominee_1_name"] != "Sample Nominee" || reg["nominee_1_applicable"] != "100" || reg["account_no_1"] != c.Bank.Account ||
		reg["primary_holder_dob_incorporation"] != c.DOB[8:]+"/"+c.DOB[5:7]+"/"+c.DOB[:4] || reg["state"] != "MA" {
		t.Fatalf("183-column registration: %v", reg)
	}
	if _, err := o.PlaceMFOrder(ctx, T, "anita.rao", in); !errors.Is(err, ErrForbidden) {
		t.Fatalf("risk manager placed an MF order: %v", err)
	}
	if _, err := o.PlaceMFOrder(ctx, T, e.checker, in); err == nil || !strings.Contains(err.Error(), "EUIN") {
		t.Fatalf("Regular order without the employee's EUIN: %v", err)
	}
	entries := func() int { es, _ := o.App.Ledger.Store.List(ctx, T, ledger.Filter{}); return len(es) }
	before := entries()
	ord, err := o.PlaceMFOrder(ctx, T, e.maker, in)
	if err != nil || ord.Status != MFPlaced || ord.OrderNo != "432160000002" || ord.BuyType != "FRESH" || ord.EUIN != "E123456" || ord.ARN != "ARN-99999" || ord.AfterCutoff {
		t.Fatalf("%+v %v", ord, err)
	}
	if !strings.Contains(ord.Payment, "NSE Clearing") {
		t.Fatalf("payment %q", ord.Payment)
	}
	row := e.nse.last("transaction/NORMAL")
	if row["scheme_code"] != "SBLUE-GR" || row["trxn_type"] != "P" || row["buy_sell_type"] != "FRESH" || row["client_code"] != ucc || row["order_amount"] != "5000.00" ||
		row["euin_number"] != "E123456" || row["euin_declaration"] != "Y" || row["member_unique_id"] != ord.Ref || row["demat_physical"] != mfDPTxn(c, MFProfile{Mode: "Demat"}) ||
		row["kyc_flag"] != "Y" || row["dpc_flag"] != "Y" || row["trxn_order_id"] != "" {
		t.Fatalf("order row %v", row)
	}
	// The family is now on Regular plans: Direct is refused, for the
	// investor and for a family member linked in the client portal.
	p, _ := o.MFProfile(ctx, T, ucc)
	if p.Route != MFRegular {
		t.Fatalf("route %q", p.Route)
	}
	direct := in
	direct.ISIN = isinBlueDir
	if _, err := o.PlaceMFOrder(ctx, T, e.maker, direct); err == nil || !strings.Contains(err.Error(), "one family uses one route") {
		t.Fatalf("Direct order in a Regular family: %v", err)
	}
	if _, _, err := o.PortalAccess(ctx, T, e.checker, ucc, []string{e.ucc2}, true); err != nil {
		t.Fatal(err)
	}
	p2, _ := o.MFProfile(ctx, T, e.ucc2)
	set, _ := o.MFSettingsGet(ctx, T)
	if p2.Route != MFRegular || len(p2.Family) != 2 {
		t.Fatalf("family %+v", p2)
	}
	if _, err := o.mfCheckRoute(ctx, T, set, p2, MFDirect); err == nil {
		t.Fatal("a family member could start Direct plans")
	}
	// Compliance changes the route, with a reason.
	if _, err := o.SetMFRoute(ctx, T, e.maker, ucc, MFDirect, "moving to direct"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker changed the route: %v", err)
	}
	if _, err := o.SetMFRoute(ctx, T, e.complian, ucc, MFDirect, ""); err == nil {
		t.Fatal("route changed without a reason")
	}
	if rs, err := o.SetMFRoute(ctx, T, e.complian, ucc, MFDirect, "investor redeemed the regular units and moves to direct plans"); err != nil || len(rs) != 2 {
		t.Fatalf("%+v %v", rs, err)
	}
	d, err := o.PlaceMFOrder(ctx, T, e.maker, direct)
	if err != nil || d.EUIN != "" || d.Plan != MFDirect {
		t.Fatalf("direct order %+v %v", d, err)
	}
	if r := e.nse.last("transaction/NORMAL"); r["euin_declaration"] != "N" || r["euin_number"] != "" {
		t.Fatalf("direct row %v", r)
	}
	if _, err := o.SetMFRoute(ctx, T, e.complian, ucc, MFRegular, "back to the distributor"); err != nil {
		t.Fatal(err)
	}

	// The platform refuses: kept as rejected, with its remark.
	e.nse.fail["transaction/NORMAL"] = "SCHEME NOT AVAILABLE FOR PURCHASE"
	if r, err := o.PlaceMFOrder(ctx, T, e.maker, in); err == nil || !strings.Contains(err.Error(), "SCHEME NOT AVAILABLE") || r.Status != MFRejected {
		t.Fatalf("refused order %+v %v", r, err)
	}

	// Cancellation through ORDER_CAN.
	cx, err := o.PlaceMFOrder(ctx, T, e.maker, in)
	if err != nil {
		t.Fatal(err)
	}
	cx, err = o.CancelMFOrder(ctx, T, e.maker, cx.ID, "client changed mind")
	if err != nil || cx.Status != MFCancelled || e.nse.last("cancellation/ORDER_CAN")["order_no"] != cx.OrderNo {
		t.Fatalf("cancel %+v %v", cx, err)
	}

	// Allotment moves units; a demat redemption goes by units, to the
	// client's own bank account.
	if _, err := o.RecordMFAllotment(ctx, T, e.maker, ord.ID, MFAllotment{Units: "95.402", NAV: "52.41", Date: "2026-10-30"}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.RecordMFAllotment(ctx, T, e.maker, ord.ID, MFAllotment{Units: "95.402", NAV: "52.41"}); !errors.Is(err, ErrState) {
		t.Fatalf("allotted twice: %v", err)
	}
	u, _ := o.MFUnits(ctx, T, ucc)
	if len(u.Holdings) != 1 || u.Holdings[0].Units.String() != "95.402" || u.Holdings[0].Value != money.MustAmount("5000.02") || u.Holdings[0].Cost != money.MustAmount("5000") {
		t.Fatalf("holdings %+v", u.Holdings)
	}
	red := MFOrderInput{UCC: ucc, Platform: "NSE", Type: "Redemption", ISIN: isinBlueReg, Units: "200"}
	if _, err := o.PlaceMFOrder(ctx, T, e.maker, red); err == nil || !strings.Contains(err.Error(), "95.402 held") {
		t.Fatalf("redeemed more than held: %v", err)
	}
	red.Units, red.Amount = "", "1000"
	if _, err := o.PlaceMFOrder(ctx, T, e.maker, red); err == nil || !strings.Contains(err.Error(), "by units") {
		t.Fatalf("demat redemption by amount: %v", err)
	}
	red.Amount, red.Units = "", "10"
	r, err := o.PlaceMFOrder(ctx, T, e.maker, red)
	row = e.nse.last("transaction/NORMAL")
	if err != nil || row["trxn_type"] != "R" || row["redemption_units"] != "10.000" || row["account_no"] != c.Bank.Account || row["order_amount"] != "" {
		t.Fatalf("redemption %+v %v %v", r, err, row)
	}
	if _, err := o.RecordMFAllotment(ctx, T, e.maker, r.ID, MFAllotment{Units: "10", NAV: "53", Date: "2026-10-31", Amount: "530"}); err != nil {
		t.Fatal(err)
	}
	if u, _ := o.MFUnits(ctx, T, ucc); u.Holdings[0].Units.String() != "85.402" {
		t.Fatalf("after redemption %+v", u.Holdings)
	}

	// Switch within the fund house.
	sw, err := o.PlaceMFOrder(ctx, T, e.maker, MFOrderInput{UCC: ucc, Platform: "NSE", Type: "Switch", ISIN: isinBlueReg, ToISIN: isinBlueIDCW, Units: "5"})
	row = e.nse.last("transaction/SWITCH")
	if err != nil || row["from_scheme_code"] != "SBLUE-GR" || row["to_scheme_code"] != "SBLUE-IDCW" || row["units"] != "5.000" || row["all_units"] != "N" || !strings.Contains(sw.Remark, "switch-in order") {
		t.Fatalf("switch %+v %v %v", sw, err, row)
	}
	if _, err := o.PlaceMFOrder(ctx, T, e.maker, MFOrderInput{UCC: ucc, Platform: "NSE", Type: "Switch", ISIN: isinBlueReg, ToISIN: isinBlueDir, Units: "5"}); err == nil {
		t.Fatal("switched Regular to Direct without a route change")
	}
	if _, err := o.RecordMFAllotment(ctx, T, e.maker, sw.ID, MFAllotment{Units: "12.5", OutUnits: "5", NAV: "21.05", Date: "2026-10-31"}); err != nil {
		t.Fatal(err)
	}
	if u, _ := o.MFUnits(ctx, T, ucc); len(u.Holdings) != 2 {
		t.Fatalf("after switch %+v", u.Holdings)
	}
	// Orders, allotments and redemptions never touch the client's ledger.
	if n := entries(); n != before {
		t.Fatalf("MF work posted %d ledger entries", n-before)
	}

	// A fresh folio from 1 September 2026 needs a nomination or opt-out.
	pu := mfProfileFor(c)
	pu.Nominees = nil
	if _, err := o.SaveMFProfile(ctx, T, e.maker, ucc, pu); err != nil {
		t.Fatal(err)
	}
	liq := MFOrderInput{UCC: ucc, Platform: "NSE", Type: "Purchase", ISIN: isinLiquid, Amount: "10000"}
	if _, err := o.PlaceMFOrder(ctx, T, e.maker, liq); err == nil || !strings.Contains(err.Error(), "nominees") {
		t.Fatalf("fresh folio without nomination: %v", err)
	}
	pu.NomOptOut = true
	if _, err := o.SaveMFProfile(ctx, T, e.maker, ucc, pu); err != nil {
		t.Fatal(err)
	}
	e.setNow("2026-10-29 13:30") // after the 13:00 liquid cut-off
	if l, err := o.PlaceMFOrder(ctx, T, e.maker, liq); err != nil || !l.AfterCutoff {
		t.Fatalf("liquid order %+v %v", l, err)
	}
	pu.Nominees = []MFNominee{{Name: "A", Relation: "Son", Share: "50"}, {Name: "B", Relation: "Son", Share: "30"}}
	pu.NomOptOut = false
	if _, err := o.SaveMFProfile(ctx, T, e.maker, ucc, pu); err == nil || !strings.Contains(err.Error(), "add up to 80") {
		t.Fatalf("shares of 80%%: %v", err)
	}
	pu.Nominees = append(pu.Nominees, MFNominee{Name: "C", Relation: "Son", Share: "10"}, MFNominee{Name: "D", Relation: "Son", Share: "10"})
	if _, err := o.SaveMFProfile(ctx, T, e.maker, ucc, pu); err == nil || !strings.Contains(err.Error(), "at most 3") {
		t.Fatalf("4 nominees: %v", err)
	}

	// KYC on hold at the KRA, or no FATCA declaration, stops everything.
	if _, err := docs.Update(ctx, o.Docs, T, cClient, ucc, "test", func(x *Client) error { x.KRAStatus = "On Hold"; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := o.PlaceMFOrder(ctx, T, e.maker, in); err == nil || !strings.Contains(err.Error(), "KRA") {
		t.Fatalf("KYC on hold: %v", err)
	}
	docs.Update(ctx, o.Docs, T, cClient, ucc, "test", func(x *Client) error { x.KRAStatus = "Validated"; return nil })
	pu.FATCA, pu.Nominees, pu.NomOptOut = false, nil, true
	o.SaveMFProfile(ctx, T, e.maker, ucc, pu)
	if _, err := o.PlaceMFOrder(ctx, T, e.maker, in); err == nil || !strings.Contains(err.Error(), "FATCA") {
		t.Fatalf("no FATCA: %v", err)
	}
}

func TestMFPortal(t *testing.T) {
	e := mfSetup(t)
	o, ctx, ucc := e.o, e.ctx, e.ucc
	e.register(t, ucc)
	user, _, err := o.PortalAccess(ctx, T, e.checker, ucc, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	v, err := o.PortalMF(ctx, T, user, "")
	if err != nil || v.UCC != ucc || len(v.Ready) != 2 || len(v.Missing) != 0 || !strings.Contains(v.Profile.PAN, "XXXX") {
		t.Fatalf("portal %+v %v", v, err)
	}
	if !strings.Contains(v.PayNote, "clearing corporation") || !strings.Contains(v.ARNNote, "ARN-99999") {
		t.Fatalf("notes %q %q", v.PayNote, v.ARNNote)
	}
	ord, err := o.PortalMFOrder(ctx, T, user, MFOrderInput{UCC: ucc, Platform: "NSE", Type: "Purchase", ISIN: isinBlueReg, Amount: "1000"})
	if err != nil || ord.Source != "portal" || ord.EUIN != "" || ord.ARN != "ARN-99999" {
		t.Fatalf("portal order %+v %v", ord, err)
	}
	if r := e.nse.last("transaction/NORMAL"); r["euin_declaration"] != "N" {
		t.Fatalf("execution-only order %v", r)
	}
	if _, err := o.PortalMFOrder(ctx, T, user, MFOrderInput{UCC: e.ucc2, Platform: "NSE", Type: "Purchase", ISIN: isinBlueReg, Amount: "1000"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ordered for someone else's account: %v", err)
	}
	if _, err := o.PortalMF(ctx, T, e.maker, ucc); err == nil {
		t.Fatal("staff opened the client portal")
	}
	if _, err := o.SavePortalMFProfile(ctx, T, user, ucc, true, []MFNominee{{Name: "X", Relation: "Mother", Share: "100"}}, false); err != nil {
		t.Fatal(err)
	}
	v, _ = o.PortalMF(ctx, T, user, ucc)
	mine := 0
	for _, x := range v.Orders {
		if x.UCC != ucc {
			t.Fatalf("another client's order in the portal: %+v", x)
		}
		if x.Source == "portal" {
			mine++
		}
	}
	if mine != 1 || len(v.Profile.Nominees) != 1 {
		t.Fatalf("portal after order %+v", v)
	}
}

func TestMFBSE(t *testing.T) {
	e := mfSetup(t)
	o, ctx, ucc := e.o, e.ctx, e.ucc
	if _, err := o.RegisterMFClient(ctx, T, e.maker, ucc, "BSE"); !errors.Is(err, errMFNoAPI) {
		t.Fatalf("BSE registration through an API that could not be read: %v", err)
	}
	e.register(t, ucc)
	in := MFOrderInput{UCC: ucc, Platform: "BSE", Type: "Purchase", ISIN: isinBlueReg, Amount: "2500"}
	ord, err := o.PlaceMFOrder(ctx, T, e.maker, in)
	if err != nil || ord.OrderNo != "7531973" || ord.Status != MFPlaced || !strings.HasPrefix(ord.Ref, "20261029612701") || len(ord.Ref) != 20 {
		t.Fatalf("%+v %v", ord, err)
	}
	if !strings.Contains(ord.Payment, "Indian Clearing Corporation") {
		t.Fatalf("payment %q", ord.Payment)
	}
	call := e.bse.last("orderEntryParam")
	want := []string{"TransCode", "TransNo", "OrderId", "UserID", "MemberId", "ClientCode", "SchemeCd", "BuySell", "BuySellType", "DPTxn", "OrderVal", "Qty", "AllRedeem",
		"FolioNo", "Remarks", "KYCStatus", "RefNo", "SubBrCode", "EUIN", "EUINVal", "MinRedeem", "DPC", "IPAdd", "Password", "PassKey", "Parma1", "Param2", "Param3",
		"MobileNo", "EmailID", "MandateID", "Filler1", "Filler2", "Filler3", "Filler4", "Filler5", "Filler6"}
	if strings.Join(call.Names, ",") != strings.Join(want, ",") {
		t.Fatalf("element order\n got %v\nwant %v", call.Names, want)
	}
	v := call.Vals
	if v["TransCode"] != "NEW" || v["SchemeCd"] != "02-GR" || v["BuySell"] != "P" || v["BuySellType"] != "FRESH" || v["OrderVal"] != "2500.00" || v["EUIN"] != "E123456" ||
		v["EUINVal"] != "Y" || v["DPC"] != "Y" || v["Password"] != bseFakePW || v["PassKey"] != "pk1" || v["MemberId"] != "6127" || v["UserID"] != "612701" {
		t.Fatalf("order values %v", v)
	}
	// The encrypted password is reused within the hour.
	if _, err := o.PlaceMFOrder(ctx, T, e.maker, in); err != nil || e.bse.count("getPassword") != 1 {
		t.Fatalf("second order: %v, %d logins", err, e.bse.count("getPassword"))
	}
	e.bse.failOrder = "SCHEME NOT AVAILABLE"
	if r, err := o.PlaceMFOrder(ctx, T, e.maker, in); err == nil || r.Status != MFRejected || !strings.Contains(r.Remark, "SCHEME NOT AVAILABLE") {
		t.Fatalf("failed order %+v %v", r, err)
	}
	e.bse.failOrder = ""
	if _, err := o.RecordMFAllotment(ctx, T, e.maker, ord.ID, MFAllotment{Units: "47.7", NAV: "52.41", Date: "2026-10-30"}); err != nil {
		t.Fatal(err)
	}
	r, err := o.PlaceMFOrder(ctx, T, e.maker, MFOrderInput{UCC: ucc, Platform: "BSE", Type: "Redemption", ISIN: isinBlueReg, AllUnits: true})
	v = e.bse.last("orderEntryParam").Vals
	if err != nil || v["BuySell"] != "R" || v["Qty"] != "47.700" || v["AllRedeem"] != "N" || v["Param3"] == "" || r.Units.String() != "47.7" {
		t.Fatalf("redeem all (demat, by units) %+v %v %v", r, err, v)
	}
	if _, err := o.PlaceMFOrder(ctx, T, e.maker, MFOrderInput{UCC: ucc, Platform: "BSE", Type: "Switch", ISIN: isinBlueReg, ToISIN: isinBlueIDCW, Units: "1"}); !errors.Is(err, errMFNoAPI) {
		t.Fatalf("BSE switch: %v", err)
	}
	// A wrong password is refused at login.
	bseForget(mfConn{Base: e.bseURL, User: "612701", PassKey: "pk1"})
	o.SaveConnection(ctx, T, "admin", connBSEStarMF, map[string]string{"member": "6127", "user": "612701", "env": "Demo", "url": e.bseURL}, map[string]string{"password": "wrong"})
	if _, err := o.PlaceMFOrder(ctx, T, e.maker, in); err == nil || !strings.Contains(err.Error(), "refused the login") {
		t.Fatalf("bad password: %v", err)
	}
}

func TestMFSIPs(t *testing.T) {
	e := mfSetup(t)
	o, ctx, ucc := e.o, e.ctx, e.ucc
	e.register(t, ucc)
	if _, err := o.AddMFMandate(ctx, T, e.maker, ucc, MFMandateInput{Platform: "NSE", Type: "E-mandate", Limit: "25000"}); !errors.Is(err, errMFNoAPI) {
		t.Fatalf("mandate through an API that could not be read: %v", err)
	}
	if _, err := o.AddMFMandate(ctx, T, e.maker, ucc, MFMandateInput{Platform: "NSE", Type: "E-mandate", Limit: "20000000", PlatformID: "1"}); err == nil || !strings.Contains(err.Error(), "1 crore") {
		t.Fatalf("mandate above ₹1 crore: %v", err)
	}
	mn, err := o.AddMFMandate(ctx, T, e.maker, ucc, MFMandateInput{Platform: "NSE", Type: "E-mandate", Limit: "25000", PlatformID: "123456", Status: "Approved"})
	if err != nil || mn.Favour != "NSE Clearing Ltd" || !strings.Contains(mn.Account, "X") || mn.ExpectedBy != "2026-11-05" {
		t.Fatalf("mandate %+v %v", mn, err)
	}
	mb, err := o.AddMFMandate(ctx, T, e.maker, ucc, MFMandateInput{Platform: "BSE", Type: "Physical NACH", Limit: "10000", PlatformID: "BSE778899"})
	if err != nil || mb.Status != "Pending" || mb.Favour != "Indian Clearing Corporation Ltd" {
		t.Fatalf("BSE mandate %+v %v", mb, err)
	}
	in := MFSIPInput{UCC: ucc, Platform: "NSE", ISIN: isinBlueReg, Amount: "5000", Frequency: "MONTHLY", StartDate: "2026-11-01", Installments: 12, MandateID: mn.ID}
	if _, err := o.StartMFSIP(ctx, T, e.maker, in); err == nil || !strings.Contains(err.Error(), "2026-11-02 at the earliest") {
		t.Fatalf("start before T+2: %v", err)
	}
	in.StartDate = "2026-11-10"
	in.MandateID = mb.ID
	if _, err := o.StartMFSIP(ctx, T, e.maker, in); err == nil || !strings.Contains(err.Error(), "NSE mandates") {
		t.Fatalf("BSE mandate on an NSE SIP: %v", err)
	}
	in.MandateID = mn.ID
	s, err := o.StartMFSIP(ctx, T, e.maker, in)
	row := e.nse.last("registration/product/XSIP")
	if err != nil || s.Status != "Active" || s.RegID == "" || s.NextDue != "2026-11-10" || s.EUIN != "E123456" {
		t.Fatalf("XSIP %+v %v", s, err)
	}
	if row["xsip_mandate_id"] != "123456" || row["amc_code"] != "SAMPLEMF_MF" || row["sch_code"] != "SBLUE-GR" || row["start_date"] != "10/11/2026" || row["frequency_type"] != "MONTHLY" ||
		row["installment_no"] != "12" || row["installment_amount"] != "5000.00" || row["frequency_allowed"] != "1" || row["member_code"] != nseFakeMember || row["trans_mode"] != "D" {
		t.Fatalf("XSIP row %v", row)
	}
	big := in
	big.Amount = "21000"
	if _, err := o.StartMFSIP(ctx, T, e.maker, big); err == nil || !strings.Contains(err.Error(), "above its limit") {
		t.Fatalf("XSIPs above the mandate's limit: %v", err)
	}
	idcw := in
	idcw.ISIN = isinBlueIDCW
	if _, err := o.StartMFSIP(ctx, T, e.maker, idcw); err == nil || !strings.Contains(err.Error(), "does not take SIPs") {
		t.Fatalf("SIP in a scheme without SIPs: %v", err)
	}

	// BSE XSIP: registered, then cancelled (CXL) through the API.
	bin := MFSIPInput{UCC: ucc, Platform: "BSE", ISIN: isinBlueReg, Amount: "2000", Frequency: "QUARTERLY", StartDate: "2026-11-20", Installments: 8, MandateID: mb.ID}
	if _, err := o.StartMFSIP(ctx, T, e.maker, bin); err == nil || !strings.Contains(err.Error(), "2026-11-22 at the earliest") {
		t.Fatal("XSIP within 20 days of a physical mandate still being processed")
	}
	bin.StartDate = "2026-12-15"
	bs, err := o.StartMFSIP(ctx, T, e.maker, bin)
	x := e.bse.last("xsipOrderEntryParam").Vals
	if err != nil || bs.RegID != "582597" || x["TransactionCode"] != "NEW" || x["MandateID"] != "BSE778899" || x["StartDate"] != "15/12/2026" || x["FrequencyType"] != "QUARTERLY" ||
		x["NoOfInstallment"] != "8" || x["SchemeCode"] != "02-GR" {
		t.Fatalf("BSE XSIP %+v %v %v", bs, err, x)
	}
	if _, err := o.SetMFMandateStatus(ctx, T, e.maker, ucc, mb.ID, "Cancelled", "", ""); err == nil {
		t.Fatal("cancelled a mandate with an active SIP")
	}
	bs, err = o.CancelMFSIP(ctx, T, e.maker, bs.ID, "client asked", false)
	x = e.bse.last("xsipOrderEntryParam").Vals
	if err != nil || bs.Status != "Cancelled" || x["TransactionCode"] != "CXL" || x["XsipRegID"] != "582597" {
		t.Fatalf("BSE cancel %+v %v %v", bs, err, x)
	}

	// Pauses: recorded after pausing on the platform, at most twice.
	if _, err := o.PauseMFSIP(ctx, T, e.maker, s.ID, "2026-12-10", 1, false); err == nil {
		t.Fatal("paused without the platform")
	}
	if s, err = o.PauseMFSIP(ctx, T, e.maker, s.ID, "2026-12-10", 1, true); err != nil || len(s.Pauses) != 1 || s.Pauses[0].To != "2026-12-10" {
		t.Fatalf("pause %+v %v", s, err)
	}
	if s, err = o.PauseMFSIP(ctx, T, e.maker, s.ID, "2027-02-10", 2, true); err != nil || s.Pauses[1].To != "2027-03-10" {
		t.Fatalf("second pause %+v %v", s, err)
	}
	if _, err := o.PauseMFSIP(ctx, T, e.maker, s.ID, "2027-06-10", 1, true); err == nil || !strings.Contains(err.Error(), "only twice") {
		t.Fatalf("third pause: %v", err)
	}
	// The reminder goes 3 days ahead, once; nothing while paused.
	o.mfTick(ctx, T, at("2026-11-06 09:30"))
	o.mfTick(ctx, T, at("2026-11-07 09:30"))
	o.mfTick(ctx, T, at("2026-11-08 09:30"))
	if n := mfMessages(t, o, ctx, ucc, "mf.sip.due"); n != 2 { // email and WhatsApp templates; WhatsApp held for consent
		t.Fatalf("%d reminders", n)
	}
	o.mfTick(ctx, T, at("2026-12-08 09:30")) // 2026-12-10 is paused
	if n := mfMessages(t, o, ctx, ucc, "mf.sip.due"); n != 2 {
		t.Fatalf("reminded of a paused instalment: %d", n)
	}
	// Cancellation closes at T-2.
	e.setNow("2027-01-07 10:00")
	if _, err := o.CancelMFSIP(ctx, T, e.maker, s.ID, "client asked", true); err != nil {
		t.Fatalf("cancel recorded from the platform: %v", err)
	}

	// Three failed instalments in a row cancel an XSIP.
	e.setNow("2026-10-29 10:00")
	f, err := o.StartMFSIP(ctx, T, e.maker, in)
	if err != nil {
		t.Fatal(err)
	}
	e.setNow("2026-11-09 10:00")
	if _, err := o.CancelMFSIP(ctx, T, e.maker, f.ID, "client asked", false); err == nil || !strings.Contains(err.Error(), "T-2") {
		t.Fatalf("cancelled inside T-2: %v", err)
	}
	if _, err := o.CancelMFSIP(ctx, T, e.maker, f.ID, "client asked", false); errors.Is(err, errMFNoAPI) {
		t.Fatal("T-2 should be checked first")
	}
	if f, err = o.RecordMFSIPInstalment(ctx, T, e.maker, f.ID, "2026-11-10", true, "432170000001", ""); err != nil || f.Done != 1 {
		t.Fatalf("%+v %v", f, err)
	}
	for _, d := range []string{"2026-12-10", "2027-01-10"} {
		if f, err = o.RecordMFSIPInstalment(ctx, T, e.maker, f.ID, d, false, "", "insufficient funds"); err != nil || f.Status != "Active" {
			t.Fatalf("%s: %+v %v", d, f, err)
		}
	}
	if f, err = o.RecordMFSIPInstalment(ctx, T, e.maker, f.ID, "2027-02-10", false, "", "insufficient funds"); err != nil || f.Status != "Cancelled" || f.Failures != 3 {
		t.Fatalf("third failure %+v %v", f, err)
	}
	if n := mfMessages(t, o, ctx, ucc, "mf.sip.cancelled"); n != 1 {
		t.Fatalf("%d cancellation messages", n)
	}
	// The debited instalment is an order to allot.
	os, _ := o.MFOrders(ctx, T)
	found := false
	for _, x := range os {
		found = found || (x.SIP == f.ID && x.OrderNo == "432170000001" && x.Status == MFPendRTA)
	}
	if !found {
		t.Fatal("instalment order not kept")
	}
	// NSE XSIP cancellation is done on the platform's site.
	e.setNow("2026-10-29 10:00")
	g, _ := o.StartMFSIP(ctx, T, e.maker, in)
	if _, err := o.CancelMFSIP(ctx, T, e.maker, g.ID, "client asked", false); !errors.Is(err, errMFNoAPI) {
		t.Fatalf("NSE XSIP cancellation: %v", err)
	}
	ms, _ := o.MFMandates(ctx, T)
	if len(ms) != 2 || ms[0].Status != "Pending" || ms[1].SIPs != 1 {
		t.Fatalf("mandates %+v", ms)
	}
	e.setNow("2026-12-01 10:00")
	if ms, _ := o.MFMandates(ctx, T); !ms[0].Overdue {
		t.Fatalf("physical mandate past 20 working days not overdue: %+v", ms[0])
	}
}

func mfMessages(t *testing.T, o *Ops, ctx context.Context, ucc, event string) int {
	t.Helper()
	ms, err := o.Messages(ctx, T, ucc, 500)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, m := range ms {
		if m.Event == event {
			n++
		}
	}
	return n
}

func TestMFFilesAndCommission(t *testing.T) {
	e := mfSetup(t)
	o, ctx, ucc := e.o, e.ctx, e.ucc
	e.register(t, ucc)
	ord, err := o.PlaceMFOrder(ctx, T, e.maker, MFOrderInput{UCC: ucc, Platform: "NSE", Type: "Purchase", ISIN: isinBlueReg, Amount: "100000"})
	if err != nil {
		t.Fatal(err)
	}
	sampleOrders, _ := o.MFOrders(ctx, T) // the sample day loads an MF-ORDERS file too
	// The platform's order file (MF-ORDERS on Other uploads) still loads,
	// and a row for an order placed here updates that order.
	file := "OrderNo,UCC,Scheme,Type,Amount,Platform,Status\n" + ord.OrderNo + "," + ucc + ",SBLUE-GR,Purchase,100000,NSE,PENDING FOR RTA\n90000001," + e.ucc2 + ",SLIQ-GR,Purchase,2500,BSE,Payment pending\n"
	if _, err := o.LoadFile(ctx, T, e.maker, "MF-ORDERS", "", "mf_orders.csv", []byte(file)); err != nil {
		t.Fatal(err)
	}
	os, _ := o.MFOrders(ctx, T)
	if len(os) != len(sampleOrders)+1 {
		t.Fatalf("orders %+v", os)
	}
	for _, x := range os {
		if x.ID == ord.ID && (x.Status != MFPendRTA || len(x.FileStatuses) != 1) {
			t.Fatalf("merged %+v", x)
		}
		if x.ID == "90000001" && (x.Status != "Payment pending" || x.Name == "") {
			t.Fatalf("file order %+v", x)
		}
	}
	// Order status and allotment files, through a saved mapping.
	st := "Order No|Order Status|Remarks\n" + ord.OrderNo + "|VALIDATED BY RTA|ok\n"
	if r, err := o.ImportMFFile(ctx, T, e.maker, MFImportRequest{Kind: "order-status", Platform: "NSE", Columns: map[string]string{"order_no": "Order No", "status": "Order Status", "remark": "Remarks"}}, []byte(st)); err != nil || r.Applied != 1 {
		t.Fatalf("status %+v %v", r, err)
	}
	al := "Order No|Allotted Units|Allotted NAV|Allotment Date|Folio\n" + ord.OrderNo + "|1908.0137|52.41|30/10/2026|\n77777|1|1|30/10/2026|\n"
	cols := map[string]string{"order_no": "Order No", "units": "Allotted Units", "nav": "Allotted NAV", "date": "Allotment Date", "folio": "Folio"}
	r, err := o.ImportMFFile(ctx, T, e.maker, MFImportRequest{Kind: "allotments", Platform: "NSE", Columns: cols, SaveAs: "nse-allot"}, []byte(al))
	if err != nil || r.Applied != 1 || len(r.Skipped) != 1 {
		t.Fatalf("allotments %+v %v", r, err)
	}
	if r, _ := o.ImportMFFile(ctx, T, e.maker, MFImportRequest{Kind: "allotments", Mapping: "nse-allot"}, []byte(al)); r.Applied != 0 {
		t.Fatalf("allotted twice %+v", r)
	}
	if _, err := o.ImportMFFile(ctx, T, e.maker, MFImportRequest{Kind: "allotments", Platform: "NSE"}, []byte(al)); err == nil || !strings.Contains(err.Error(), "which column") {
		t.Fatalf("unmapped file: %v", err)
	}
	// RTA transactions in a folio (a physical holding from the RTA's feed).
	c2, _ := o.Client(ctx, T, e.ucc2)
	rta := "FOLIO_NO,PRODUCT_ISIN,TRADDATE,UNITS,TRXNTYPE,PURPRICE,AMOUNT,PAN\n" +
		"9100011/22,INF000A01011,01/10/2026,200,PURCHASE,50,10000," + c2.PAN + "\n" +
		"9100011/22,INF000A01011,15/10/2026,50,REDEMPTION,51,2550,\n" +
		"9100099/11,INF000A01011,15/10/2026,5,PURCHASE,51,255,ZZZZZ9999Z\n"
	rcols := map[string]string{"folio": "FOLIO_NO", "isin": "PRODUCT_ISIN", "date": "TRADDATE", "units": "UNITS", "type": "TRXNTYPE", "nav": "PURPRICE", "amount": "AMOUNT", "pan": "PAN"}
	if r, err := o.ImportMFFile(ctx, T, e.maker, MFImportRequest{Kind: "rta-transactions", Columns: rcols, SaveAs: "kfin-mfsd201"}, []byte(rta)); err != nil || r.Applied != 2 || len(r.Skipped) != 1 {
		t.Fatalf("RTA feed %+v %v", r, err)
	}
	if r, _ := o.ImportMFFile(ctx, T, e.maker, MFImportRequest{Kind: "rta-transactions", Mapping: "kfin-mfsd201"}, []byte(rta)); r.Applied != 0 {
		t.Fatalf("RTA rows loaded twice %+v", r)
	}
	u, _ := o.MFUnits(ctx, T, e.ucc2)
	if len(u.Holdings) != 1 || u.Holdings[0].Units.String() != "150" || u.Holdings[0].Folio != "9100011/22" || u.Holdings[0].Mode != "Physical" {
		t.Fatalf("RTA holdings %+v", u.Holdings)
	}

	// Trail commission for October: 0.75% a year on Regular plan value.
	if _, err := o.AccrueMFTrail(ctx, T, e.maker, "2026-10"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker ran commission: %v", err)
	}
	acc, err := o.AccrueMFTrail(ctx, T, e.checker, "2026-10")
	if err != nil || len(acc) != 2 {
		t.Fatalf("accruals %+v %v", acc, err)
	}
	var expected money.Amount
	for _, a := range acc {
		expected += a.Expected
	}
	// (1908.0137 + 150) × 52.41 = 107,860.79 → 0.75% ÷ 12 = 67.41
	if expected != money.MustAmount("67.42") && expected != money.MustAmount("67.41") {
		t.Fatalf("expected %s", expected)
	}
	brok := "Month,ISIN,Scheme,Brokerage,ARN\nOct-2026,INF000A01011,Sample Bluechip Fund - Regular Plan - Growth,60.00,ARN-99999\nOct-2026,,Unknown scheme,5.00,ARN-99999\n"
	bcols := map[string]string{"month": "Month", "isin": "ISIN", "scheme": "Scheme", "amount": "Brokerage", "arn": "ARN"}
	if r, err := o.ImportMFFile(ctx, T, e.maker, MFImportRequest{Kind: "rta-brokerage", Columns: bcols, Name: "camsbrok.csv"}, []byte(brok)); err != nil || r.Applied != 2 {
		t.Fatalf("brokerage %+v %v", r, err)
	}
	v, err := o.MFCommission(ctx, T, "2026-10")
	if err != nil || len(v.Lines) != 1 || v.Lines[0].Status != "Short" || v.Received != money.MustAmount("65") || v.Unmapped != money.MustAmount("5") || v.Files[0] != "camsbrok.csv" {
		t.Fatalf("reconciliation %+v %v", v, err)
	}
	// Mandate status from the platform's report.
	mn, _ := o.AddMFMandate(ctx, T, e.maker, ucc, MFMandateInput{Platform: "NSE", Type: "E-mandate", Limit: "5000", PlatformID: "55555"})
	ms := "Mandate Id,UMRN,Status\n55555,HDFC7021807230000123,APPROVED\n"
	if r, err := o.ImportMFFile(ctx, T, e.maker, MFImportRequest{Kind: "mandate-status", Platform: "NSE", Columns: map[string]string{"mandate_id": "Mandate Id", "umrn": "UMRN", "status": "Status"}}, []byte(ms)); err != nil || r.Applied != 1 {
		t.Fatalf("mandate status %+v %v", r, err)
	}
	p, _ := o.MFProfile(ctx, T, ucc)
	if m, _ := mfMandateOf(p, mn.ID); m.Status != "Approved" || m.UMRN != "HDFC7021807230000123" {
		t.Fatalf("mandate %+v", m)
	}
}

// fakePlatform answers the calls the public specifications could not
// give, to test what Ledgerline does with their answers.
type fakePlatform struct {
	mfPlatform
	status []mfStatusRow
	allot  []mfAllotRow
	mand   []mfMandateRow
}

func (f fakePlatform) OrderStatus(context.Context, mfConn, string, string) ([]mfStatusRow, error) {
	return f.status, nil
}
func (f fakePlatform) Allotments(context.Context, mfConn, string, string) ([]mfAllotRow, error) {
	return f.allot, nil
}
func (f fakePlatform) MandateStatus(context.Context, mfConn, string, string) ([]mfMandateRow, error) {
	return f.mand, nil
}

func TestMFPolling(t *testing.T) {
	e := mfSetup(t)
	o, ctx, ucc := e.o, e.ctx, e.ucc
	e.register(t, ucc)
	a, _ := o.PlaceMFOrder(ctx, T, e.maker, MFOrderInput{UCC: ucc, Platform: "NSE", Type: "Purchase", ISIN: isinBlueReg, Amount: "3000"})
	b, _ := o.PlaceMFOrder(ctx, T, e.maker, MFOrderInput{UCC: ucc, Platform: "NSE", Type: "Purchase", ISIN: isinBlueReg, Amount: "4000"})
	// The real connectors say what they cannot do, once per call.
	o.mfTick(ctx, T, at("2026-10-29 10:00"))
	st, _, _ := docs.Get[MFPollState](ctx, o.Docs, T, cMFState, "poll-NSE")
	if len(st.NoAPI) != 3 || st.LastError != "" {
		t.Fatalf("poll state %+v", st)
	}
	old := mfPlatforms["NSE"]
	defer func() { mfPlatforms["NSE"] = old }()
	mfPlatforms["NSE"] = fakePlatform{mfPlatform: old, status: []mfStatusRow{{OrderNo: a.OrderNo, Status: "PAYMENT CONFIRMATION PENDING"}, {OrderNo: "nobody", Status: "X"}},
		allot: []mfAllotRow{{OrderNo: b.OrderNo, Units: "76.32", NAV: "52.41", Date: "2026-10-30"}}}
	o.mfTick(ctx, T, at("2026-10-29 10:20")) // within 30 minutes: nothing
	o.mfTick(ctx, T, at("2026-10-29 10:31"))
	x, _, _ := docs.Get[MFOrder](ctx, o.Docs, T, cMF, a.ID)
	y, _, _ := docs.Get[MFOrder](ctx, o.Docs, T, cMF, b.ID)
	if x.PlatformStat != "PAYMENT CONFIRMATION PENDING" || x.Status != MFPlaced || y.Status != MFAllotted || y.Allotted.String() != "76.32" {
		t.Fatalf("after polling %+v %+v", x, y)
	}
	st, _, _ = docs.Get[MFPollState](ctx, o.Docs, T, cMFState, "poll-NSE")
	if len(st.NoAPI) != 0 || st.Applied != 2 {
		t.Fatalf("poll state %+v", st)
	}
	v, err := o.MFStatus(ctx, T)
	if err != nil || len(v.Platforms) != 2 || v.Schemes != 5 || v.NAV.NAVDate != "2026-10-28" || v.Platforms[1].Connection != "Saved" {
		t.Fatalf("status %+v %v", v, err)
	}
}
