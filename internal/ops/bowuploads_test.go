package ops

import (
	"fmt"
	"strings"
	"testing"

	"ledgerline/internal/charges"
	"ledgerline/internal/docs"
	"ledgerline/internal/xfile"
)

// bowUploadSetup is the sample day with made-up clients and dealers for
// the BOW uploads, and the firm's values in the connection.
func bowUploadSetup(t *testing.T, vals map[string]string) (*Ops, string) {
	t.Helper()
	o, ctx := setup(t)
	for _, c := range []Client{
		{UCC: "BW01", Name: "Meera Kulkarni", Status: "Active", Email: "meera@x.example", Mobile: "9800000001", PAN: "AAAPA1111A", Partner: "AP01", Segments: []string{"CM", "FO"}, KRAStatus: "Validated",
			Bank:  BankAccount{Bank: "HDFC Bank", IFSC: "HDFC0001234", Account: "50100011112222", Branch: "Fort"},
			Banks: []BankAccount{{Bank: "Axis Bank", IFSC: "UTIB0000123", Account: "91902033334444", Status: BankInactive}, {Bank: "HDFC Bank", IFSC: "HDFC0001234", Account: "50100011112222", Branch: "Fort", Primary: true, Status: BankActive}}},
		{UCC: "BW02", Name: "Ravi", Status: "Active", Email: "ravi@x.example", Mobile: "9800000002", PAN: "AAAPA2222B", Segments: []string{"CM", "COM"}, Tags: []string{"OFFLINE"}, KRAStatus: "On hold",
			SegStatus: map[string]SegmentState{"COM": {Status: SegSuspended}}, Bank: BankAccount{Bank: "SBI", IFSC: "SBIN0000001", Account: "12345"}},
		{UCC: "BW03", Name: "No Email", Status: "Active", Mobile: "9800000003", PAN: "AAAPA3333C", Segments: []string{"CM"}},
		{UCC: "BW04", Name: "Closed Client", Status: "Closed", Email: "c@x.example", Mobile: "9800000004", PAN: "AAAPA4444D", Segments: []string{"CM"}},
	} {
		c.Normalise()
		if err := docs.Upsert(ctx, o.Docs, T, cClient, c.UCC, c, "t"); err != nil {
			t.Fatal(err)
		}
	}
	docs.Upsert(ctx, o.Docs, T, cNRI, "BW01", NRIDetails{UCC: "BW01", Account: "NRO", CPCode: "CP1234567", By: "t"}, "t")
	for _, d := range []Dealer{
		{ID: "DL-0901", FirstName: "Amol", LastName: "Desai", PAN: "AAAPD1111A", Mobile: "9811111111", Email: "amol@x.example", Relationship: DealerAP, Partner: "AP01", Status: DealerActive,
			Terminals: []DealerTerminal{{Exchange: "BSE", Segments: []string{"CM", "FO"}, TerminalID: "1234", TradingID: "612700901", LocationID: "4000010001001012", ActiveFrom: "2026-01-01"}}},
		{ID: "DL-0902", FirstName: "Sana", PAN: "AAAPD2222B", Mobile: "9822222222", Email: "sana@x.example", Relationship: DealerEmployee, Status: DealerActive},
		{ID: "DL-0903", FirstName: "Gone", PAN: "AAAPD3333C", Mobile: "9833333333", Email: "gone@x.example", Relationship: DealerEmployee, Status: DealerLeft},
	} {
		docs.Upsert(ctx, o.Docs, T, cDealer, d.ID, d, "t")
	}
	// The security masters (NSE symbol and series, BSE code) for every
	// equity of the day and every holding.
	m, _, _ := o.Market(ctx, T, D)
	isins := map[string]bool{}
	for _, i := range m.Instruments {
		if i.Segment == "CM" && i.ISIN != "" {
			isins[i.ISIN] = true
		}
	}
	hs, _ := o.Holdings(ctx, T, "")
	for _, h := range hs {
		isins[h.ISIN] = true
	}
	for n, isin := range sortedKeys(isins) {
		docs.Upsert(ctx, o.Docs, T, cSecRef, isin, SecRef{ISIN: isin, Symbol: fmt.Sprintf("BWSYM%d", n), Series: "EQ", BSECode: fmt.Sprint(590000 + n)}, "t")
	}
	d := mustDec
	type R = charges.BrokerageRule
	for _, p := range []PlanInput{
		{ID: "bw-retail", Name: "BW Retail, online", From: "2026-04-01", Rules: map[string]R{
			"CM-DEL": {Mode: charges.PlanPct, Pct: d("0.3"), Min: d("20")}, "CM-INTRA": {Mode: charges.PlanLowerOf, Flat: d("20"), Pct: d("0.03")},
			"CM-INTRA|*|online|*": {Mode: charges.PlanPct, Pct: d("0.02")}, "CM-INTRA|*|dealer|*": {Mode: charges.PlanPct, Pct: d("0.05")},
			"FO-FUT": {Mode: charges.PlanLowerOf, Flat: d("20"), Pct: d("0.03")}, "FO-OPT": {Mode: charges.PlanFlat, Flat: d("20")},
			"FO-OPT|NSE|*|index": {Mode: charges.PlanPerLot, PerLot: d("5")}, "COM-FUT": {Mode: charges.PlanFlat, Flat: d("15")},
			"FO-FUT|*|*|expiry": {Mode: charges.PlanZero}, "CD-FUT": {Mode: charges.PlanPct, Pct: d("0.01"), Max: d("15")}}},
		{ID: "bw-staff", Name: "BW Staff", From: "2026-04-01", Rules: map[string]R{"CM-DEL": {Mode: charges.PlanZero}, "CM-INTRA": {Mode: charges.PlanPerUnit, PerUnit: d("0.01")}}},
	} {
		if _, err := o.SavePlan(ctx, T, "admin", p); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := o.Assign(ctx, T, "admin", "default", "bw-retail", "2026-04-01"); err != nil {
		t.Fatal(err)
	}
	if _, err := o.Assign(ctx, T, "admin", "ucc:BW02", "bw-staff", "2026-04-01"); err != nil {
		t.Fatal(err)
	}
	if vals != nil {
		if _, err := o.SaveConnection(ctx, T, "admin", connBOWFiles, vals, nil); err != nil {
			t.Fatal(err)
		}
	}
	set, _ := o.Settings(ctx, T)
	return o, set.nextBusinessDay(D)
}

var bowTestVals = map[string]string{
	"owner_id": "T6127", "dealer_ids": "DL-0901=DEALER1", "client_dealers": "HEAD1",
	"client_nnf":       "BSE EQUITY=612710001:1111111111111012; NSE DERIVATIVE=12266:444444001001116",
	"risk_preferences": "2,2,1,1,2,1,2,1,3", "contract_margins": "BSE,EQUITY,EQUITY=20:100; MCX,COMMODITIES,COMMODITIES-FUTURES=100:100",
	"right_limits": "Securities,BSE,EQ,EQ:MaxOrderValue=500000,TurnoverLimit=2500000; COMMODITIES,MCX,CF,:TotalBuyAmount=2500000",
	"profiles":     "RL=Profile1, EXC=Profile6", "sell_dp_charge": "13.5", "disclaimers": "charges may vary|by stock",
}

// Every BOW upload is written in its layout: the name matches, CRLF, the
// sheet's number of fields, no issues, and it reads back byte for byte.
func TestBOWUploadsRoundTrip(t *testing.T) {
	o, day := bowUploadSetup(t, bowTestVals)
	ctx := t.Context()
	files := map[string]BOWUpload{}
	for _, k := range bowUploadKinds {
		u, err := o.BOWUploadFile(ctx, T, "anita.rao", k, day, false, true)
		if err != nil {
			t.Fatalf("%s: %v", k, err)
		}
		readBOW(t, k, u.Name, u.Data)
		files[k] = u
	}
	lines := func(k string) []xfile.Row {
		lib, _ := XFiles()
		s, _ := lib.Get(k)
		tb, _ := s.Read(files[k].Name, files[k].Data)
		return tb.Rows
	}
	find := func(k, key, val string) []map[string]string {
		var out []map[string]string
		for _, r := range lines(k) {
			if r.Values[key] == val {
				out = append(out, r.Values)
			}
		}
		return out
	}
	one := func(k, key, val string) map[string]string {
		t.Helper()
		rs := find(k, key, val)
		if len(rs) != 1 {
			t.Fatalf("%s: %d lines for %s, want 1: %q", k, len(rs), val, files[k].Data)
		}
		return rs[0]
	}
	// Users: IBT and offline by client group, never a password.
	if v := one("bow-ibt-users", "LoginID", "BW01"); v["OwnerID"] != "T6127" || v["UserID"] != "BW01" || v["UserType"] != "1" || v["FirstName"] != "Meera" || v["LastName"] != "Kulkarni" || v["PAN"] != "AAAPA1111A" || v["DefaultMarketwatch"] != "N" {
		t.Fatalf("IBT %v", v)
	}
	if v := one("bow-offline-users", "LoginID", "BW02"); v["FirstName"] != "Ravi" || v["LastName"] != "Ravi" {
		t.Fatalf("offline %v", v)
	}
	if len(find("bow-ibt-users", "LoginID", "BW02")) != 0 || len(find("bow-ibt-users", "LoginID", "BW03")) != 0 || len(find("bow-ibt-users", "LoginID", "BW04")) != 0 {
		t.Fatal("an offline, incomplete or closed client in the IBT file")
	}
	if n := files["bow-ibt-users"].Note; !strings.Contains(n, "Password left empty") || !strings.Contains(n, "BW03 (e-mail)") {
		t.Fatalf("IBT note %q", n)
	}
	for _, k := range []string{"bow-ibt-users", "bow-offline-users", "bow-dealer-users"} {
		for _, r := range lines(k) {
			if r.Values["Password"] != "" {
				t.Fatalf("%s writes a password", k)
			}
		}
	}
	if v := one("bow-dealer-users", "LoginID", "DEALER1"); v["UserType"] != "2" || v["LastName"] != "Desai" {
		t.Fatalf("dealer %v", v)
	}
	if one("bow-dealer-users", "LoginID", "DL0902")["LastName"] != "Sana" || len(find("bow-dealer-users", "LoginID", "DL0903")) != 0 {
		t.Fatal("dealers")
	}
	// Dealer mapping: the firm's dealer for all, the AP's dealer for BW01.
	if rs := find("bow-dealer-mapping", "ClientID", "BW01"); len(rs) != 2 || rs[0]["DealerID"] != "HEAD1" || rs[1]["DealerID"] != "DEALER1" {
		t.Fatalf("mapping %v", rs)
	}
	// NNF: the dealer's terminal and the clients' IDs for open segments.
	if rs := find("bow-nnf-location", "LoginID", "DEALER1"); len(rs) != 2 || rs[0]["TraderID"] != "612700901" || rs[0]["BrokerID"] != "6127" || rs[0]["Market"] != "EQUITY" || rs[1]["Market"] != "DERIVATIVE" {
		t.Fatalf("NNF dealer %v", rs)
	}
	if rs := find("bow-nnf-location", "LoginID", "BW01"); len(rs) != 2 || rs[1]["Exchange"] != "NSE" || rs[1]["BrokerID"] != "90311" {
		t.Fatalf("NNF client %v", rs)
	}
	if rs := find("bow-nnf-location", "LoginID", "BW02"); len(rs) != 1 {
		t.Fatalf("NNF BW02 (no F&O) %v", rs)
	}
	// Trading rights: open segments allowed, others blocked with the reason.
	rights := map[string]map[string]string{}
	for _, v := range find("bow-trading-rights", "ClientID", "BW02") {
		rights[v["Exchange"]+" "+v["Market"]] = v
	}
	if v := rights["BSE EQUITY"]; v["Permission"] != bowAllow || v["Category"] != "SECURITIES" || v["Segment"] != "EQUITY" || v["DeleteFlag"] != "N" || v["Default2"] != "1" {
		t.Fatalf("rights %v", v)
	}
	if v := rights["MCX COMMODITIES"]; v["Permission"] != bowBlock || v["Category"] != "commodities" || !strings.Contains(v["Remarks"], "suspended") {
		t.Fatalf("rights %v", v)
	}
	if v := rights["NSE DERIVATIVE"]; v["Permission"] != bowBlock || v["Remarks"] != "BLOCKED: segment not registered" {
		t.Fatalf("rights %v", v)
	}
	if len(find("bow-trading-rights", "ClientID", "BW04")) != 0 {
		t.Fatal("a closed client never sent is in the rights file")
	}
	if v := one("bow-risk-preferences", "ClientCode", "BW01"); v["Exchange"] != "ALL" || v["RealisedProfit"] != "2" || v["NetUnrealisedLoss"] != "3" || v["DeleteFlag"] != "" {
		t.Fatalf("risk %v", v)
	}
	if rs := find("bow-contract-margin", "BackofficeID", "BW01"); len(rs) != 1 || rs[0]["IntradayMargin"] != "20" || rs[0]["DeliveryMargin"] != "100" {
		t.Fatalf("contract margin %v", rs)
	}
	if rs := find("bow-contract-margin", "BackofficeID", "BW02"); len(rs) != 1 || rs[0]["Exchange"] != "BSE" {
		t.Fatalf("contract margin BW02 (MCX suspended) %v", rs)
	}
	if rs := find("bow-right-limits", "LoginID", "BW01"); len(rs) != 1 || rs[0]["UserLevel"] != "USER" || rs[0]["MaxOrderValue"] != "500000" || rs[0]["TurnoverLimit"] != "2500000" || rs[0]["TotalBuyAmount"] != "" {
		t.Fatalf("right limits %v", rs)
	}
	if v := one("bow-profile-mapping", "LoginID", "BW01"); v["RLProfile"] != "Profile1" || v["ExcIdeProfile"] != "Profile6" || v["TRProfile"] != "" {
		t.Fatalf("profiles %v", v)
	}
	if one("bow-update-pan", "ClientID", "BW03")["PAN"] != "AAAPA3333C" {
		t.Fatal("PAN")
	}
	// Banks: every registered account, primary first; BW02's short account left out.
	if rs := find("bow-bank", "BackOfficeID", "BW01"); len(rs) != 2 || rs[0]["AccountNo"] != "50100011112222" || rs[0]["Status"] != "active" || rs[0]["BranchName"] != "Fort" || rs[1]["Status"] != "inactive" || rs[1]["BranchName"] != "UTIB0000123" {
		t.Fatalf("banks %v", rs)
	}
	if n := files["bow-bank"].Note; len(find("bow-bank", "BackOfficeID", "BW02")) != 0 || !strings.Contains(n, "BW02 account …2345") || !strings.Contains(n, "IFSC") {
		t.Fatalf("bank note %q", n)
	}
	if one("bow-kra-status", "BackOfficeID", "BW01")["KRAStatus"] != "A" || one("bow-kra-status", "BackOfficeID", "BW02")["KRAStatus"] != "H" || len(find("bow-kra-status", "BackOfficeID", "BW03")) != 0 {
		t.Fatal("KRA")
	}
	if rs := find("bow-cp-code", "BackofficeID", "BW01"); len(rs) == 0 || rs[0]["CPCode"] != "CP1234567" || rs[0]["Market"] != "DERIVATIVE" || rs[0]["Default"] != "Y" {
		t.Fatalf("CP codes %v", rs)
	}
	// Brokerage master: the seeded plans, with the lines BOW cannot express noted.
	bm := lines("bow-brokerage-master")
	if len(bm) == 0 || !strings.Contains(files["bow-brokerage-master"].Note, "a maximum per order") {
		t.Fatalf("brokerage master %d lines, note %q", len(bm), files["bow-brokerage-master"].Note)
	}
	types := map[string]bool{}
	for _, r := range bm {
		v := r.Values
		types[v["BuyType"]] = true
		if v["StampType"] != "1" || v["DeleteFlag"] != "N" || v["GST"] == "" || v["Disclaimer1"] != "charges may vary" || v["Disclaimer2"] != "by stock" {
			t.Fatalf("brokerage line %v", v)
		}
		if v["ProductID"] == "CNC" && (v["SellDPType"] != "2" || v["SellDPValue2"] != "13.5") {
			t.Fatalf("delivery line %v", v)
		}
		if v["Market"] == "DERIVATIVES" && v["ProductID"] != "ALL" {
			t.Fatalf("derivatives line %v", v)
		}
	}
	for _, x := range []string{"1", "2", "3", "4", "5"} {
		if !types[x] {
			t.Fatalf("brokerage type %s missing: %v", x, types)
		}
	}
	got := map[string]map[string]string{}
	for _, r := range bm {
		v := r.Values
		got[v["PlanName"]+"|"+v["Exchange"]+"|"+v["Market"]+"|"+v["Segment"]+"|"+v["ProductID"]+"|"+v["InstrumentSeries"]] = v
	}
	for k, want := range map[string]string{
		"BW Retail online|NSE|EQUITY|EQUITY|CNC|":             "4 0.3 20",
		"BW Retail online|BSE|EQUITY|EQUITY|INTRA|":           "1 0.02 0", // the online channel's line
		"BW Retail online|NSE|DERIVATIVES|FUTURES|ALL|":       "3 0.03 20",
		"BW Retail online|NSE|DERIVATIVES|OPTIONS|ALL|":       "2 0 20",
		"BW Retail online|NSE|DERIVATIVES|OPTIONS|ALL|OPTIDX": "5 0 5",
		"BW Retail online|MCX|COMMODITIES|FUTURES|ALL|":       "2 0 15",
		"BW Staff|BSE|EQUITY|EQUITY|CNC|":                     "1 0 0",
	} {
		v := got[k]
		if v == nil || v["BuyType"]+" "+v["Value1Buy"]+" "+v["Value2Buy"] != want || v["SellType"] != v["BuyType"] {
			t.Fatalf("%s: %v (lines %v)", k, v, sortedKeys(got))
		}
	}
	if got["BW Retail online|NSE|EQUITY|EQUITY|CNC|"]["STTSell"] == "" || got["BW Retail online|NSE|EQUITY|EQUITY|CNC|"]["StampValue1"] == "" {
		t.Fatal("no statutory rates")
	}
	for _, k := range []string{"BW Retail online|NSE|CURRENCY|FUTURES|ALL|", "BW Staff|NSE|EQUITY|EQUITY|INTRA|"} {
		if got[k] != nil {
			t.Fatalf("%s written: %v", k, got[k])
		}
	}
	if n := files["bow-brokerage-master"].Note; !strings.Contains(n, "a maximum per order (1)") || !strings.Contains(n, "per share (1)") || !strings.Contains(n, "the expiry case (1)") || !strings.Contains(n, "1 rate lines for channels other than online") {
		t.Fatalf("note %q", n)
	}
	if one("bow-brokerage-mapping", "BackofficeID", "BW01")["PlanName"] != "BW Retail online" || one("bow-brokerage-mapping", "BackofficeID", "BW02")["PlanName"] != "BW Staff" {
		t.Fatal("plan mapping")
	}
	// Positions: carried derivatives at the previous close.
	mp := lines("bow-manual-position")
	if len(mp) == 0 {
		t.Fatalf("no positions: %q", files["bow-manual-position"].Note)
	}
	for _, r := range mp {
		v := r.Values
		if v["MemberID"] != "6127" || v["ProductType"] != "2" || v["Filler17"] != "1" || (v["BuyQty"] == "") == (v["SellQty"] == "") || strings.HasPrefix(v["InstrumentName"], "FUT") != (v["OptionType"] == "") {
			t.Fatalf("position %v", v)
		}
	}
	// SLB: empty with a note.
	if u := files["bow-slb-holding"]; len(u.Data) != 0 || !strings.Contains(u.Note, "SLB") {
		t.Fatalf("SLB %q %q", u.Data, u.Note)
	}
	// Holdings: the DP-only and pool-only files are the combined file's parts.
	hn, hb, err := o.BOWHoldingFile(ctx, T, day)
	if err != nil {
		t.Fatal(err)
	}
	dp, pool := 0, 0
	for _, r := range readBOW(t, "bow-dp-holding", hn, hb).Rows {
		if r.Values["DPQuantity"] != "0" {
			dp++
		}
		if r.Values["PoolQuantity"] != "0.00" {
			pool++
		}
	}
	if len(lines("bow-only-dp-holding")) != dp || len(lines("bow-dp-stocks")) != dp || len(lines("bow-only-pool-holding")) != pool || dp == 0 || pool == 0 {
		t.Fatalf("DP %d/%d, pool %d/%d", len(lines("bow-only-dp-holding")), dp, len(lines("bow-only-pool-holding")), pool)
	}

	// The client files carry only the new and changed (and, made again the
	// same day, those already written that day): nothing the next day, then
	// BW01 alone once its e-mail changes.
	set, _ := o.Settings(ctx, T)
	day = set.nextBusinessDay(day)
	u, _ := o.BOWUploadFile(ctx, T, "anita.rao", "bow-ibt-users", day, false, false)
	if len(u.Data) != 0 || !strings.Contains(u.Note, "nothing changed") {
		t.Fatalf("again %q %q", u.Data, u.Note)
	}
	docs.Update(ctx, o.Docs, T, cClient, "BW01", "t", func(c *Client) error { c.Email = "meera.k@x.example"; return nil })
	u, _ = o.BOWUploadFile(ctx, T, "anita.rao", "bow-ibt-users", day, false, false)
	if tb := readBOW(t, "bow-ibt-users", u.Name, u.Data); len(tb.Rows) != 1 || tb.Rows[0].Values["Email"] != "meera.k@x.example" {
		t.Fatalf("changed %q", u.Data)
	}
	// A client who closes has their rights taken back once.
	docs.Update(ctx, o.Docs, T, cClient, "BW01", "t", func(c *Client) error { c.Status = "Closed"; return nil })
	u, _ = o.BOWUploadFile(ctx, T, "anita.rao", "bow-trading-rights", day, false, false)
	tb := readBOW(t, "bow-trading-rights", u.Name, u.Data)
	if len(tb.Rows) == 0 {
		t.Fatal("no rights taken back")
	}
	for _, r := range tb.Rows {
		if r.Values["ClientID"] != "BW01" || r.Values["Permission"] != bowBlock || r.Values["Remarks"] != "BLOCKED: client closed" {
			t.Fatalf("rights %v", r.Values)
		}
	}
	if _, err := o.BOWUploadFile(ctx, T, "vikram.shah", "bow-bank", day, false, false); err == nil {
		t.Fatal("compliance may not download BOW files")
	}
}

// A NOT NULL field with no source and no setting refuses the file, naming
// the field and the setting; a file with nothing to send is empty with a note.
func TestBOWUploadsRefuse(t *testing.T) {
	o, day := bowUploadSetup(t, nil)
	ctx := t.Context()
	for kind, want := range map[string]string{
		"bow-ibt-users": "Owner Id", "bow-offline-users": "Owner Id", "bow-dealer-users": "Owner Id",
		"bow-risk-preferences": "Risk preferences", "bow-contract-margin": "Contract margin lines", "bow-right-limits": "Right limit lines",
	} {
		_, err := o.BOWUploadFile(ctx, T, "anita.rao", kind, day, false, false)
		if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "NOT NULL") || !strings.Contains(err.Error(), "Connections → BOW upload files") {
			t.Fatalf("%s: %v", kind, err)
		}
	}
	u, err := o.BOWUploadFile(ctx, T, "anita.rao", "bow-profile-mapping", day, false, false)
	if err != nil || len(u.Data) != 0 || !strings.Contains(u.Note, "no BOW profile names") {
		t.Fatalf("profiles %q %v", u.Note, err)
	}
	for _, bad := range []map[string]string{{"risk_preferences": "2,2,1"}, {"risk_preferences": "2,2,1,1,2,1,2,1,4"}, {"contract_margins": "BSE,EQUITY=20:100"}, {"right_limits": "Securities,BSE,EQ,EQ:Foo=1"}, {"client_nnf": "BSE EQUITY=1"}, {"dp_haircut": "120"}, {"deposit_shape": "Three"}} {
		if _, err := o.SaveConnection(ctx, T, "admin", connBOWFiles, bad, nil); err != nil {
			continue // refused on save
		}
		bad2 := false
		for _, k := range []string{"bow-risk-preferences", "bow-contract-margin", "bow-right-limits", "bow-nnf-location", "bow-only-dp-holding", "bow-funds-file"} {
			if _, err := o.BOWUploadFile(ctx, T, "anita.rao", k, day, false, false); err != nil {
				bad2 = true
			}
		}
		if !bad2 {
			t.Fatalf("%v accepted", bad)
		}
	}
	set, _ := o.Settings(ctx, T)
	delete(set.MemberCodes, "BSE")
	set.MemberCodes["MCX"] = "56204"
	o.SaveSettings(ctx, T, "admin", set)
	o.SaveConnection(ctx, T, "admin", connBOWFiles, map[string]string{}, nil)
	if _, err := o.BOWUploadFile(ctx, T, "anita.rao", "bow-manual-position", day, false, false); err == nil || !strings.Contains(err.Error(), "BSE Member ID") {
		t.Fatalf("positions without the BSE member code: %v", err)
	}
}
