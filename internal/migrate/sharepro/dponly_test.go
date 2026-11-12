package sharepro

import (
	"context"
	"strings"
	"testing"

	"ledgerline/internal/migrate"
	"ledgerline/internal/migrate/pkg"
)

func rowsBy(rows []map[string]string, col string) map[string]map[string]string {
	out := map[string]map[string]string{}
	for _, r := range rows {
		out[r[col]] = r
	}
	return out
}

func wantCols(t *testing.T, what string, got, want map[string]string) {
	t.Helper()
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s %s = %q, want %q", what, k, got[k], v)
		}
	}
}

// A demat account holder with no trading account is a client of its own
// (DPOnly = Y) under the DP client code, with its details, nominees, bank
// and KYC; a closed account comes across inactive.
func TestDPOnlyClients(t *testing.T) {
	r, _ := runCurrent(t, migrate.Plan{})
	_, cl := readCSV(t, r, "firms/02/master/CLIENT-MASTER.csv")
	by := rowsBy(cl, "UCC")
	wantCols(t, "00000009", by["00000009"], map[string]string{"DPOnly": "Y", "Name": "Gita Test", "Type": "Individual", "PAN": "AAAPT0009G", "DOB": "1985-07-01",
		"Mobile": "9800000009", "Email": "gita@example.com", "City": "KOLKATA", "Segments": "", "RAS": "", "Risk": "High", "Income": "", "Bank": "TEST BANK",
		"IFSC": "TEST0000001", "Account": "123456789009", "Depository": "CDSL", "DematID": "1234567800000009", "Nominee": "Opted in", "NomineeName": "Raju Test",
		"NominationDate": "2021-02-01", "KYCDate": "2021-02-01", "Status": "Active", "API": "N", "Inactive": "", "InactiveSince": "", "Branch": "HO",
		"State": "West Bengal", "Groups": "DPG", "ECN": "", "LastTrade": ""})
	// a closed account: inactive since its closing date
	wantCols(t, "00000010", by["00000010"], map[string]string{"DPOnly": "Y", "Name": "Kiran Test", "Inactive": "Y", "InactiveSince": "2023-03-31",
		"DematID": "1234567800000010", "Segments": "", "Status": "Active"})
	// frozen is not closed; a trading code that is no client makes a DP-only client
	wantCols(t, "00000011", by["00000011"], map[string]string{"DPOnly": "Y", "Name": "Lal Test", "Inactive": ""})
	for _, u := range []string{"TEST001", "TEST002", "TEST005"} {
		if by[u]["DPOnly"] != "N" {
			t.Errorf("%s DPOnly %q", u, by[u]["DPOnly"])
		}
	}
	if _, ok := by["OLD999"]; ok {
		t.Error("a trading code that is not a client became a client")
	}
	// the trading clients' checks: no segment is not a fault of a DP-only
	// client; a closed one's gaps are warnings
	for _, i := range r.Manifest.Issues {
		dpOnly := strings.HasPrefix(i.Key, "000000")
		if dpOnly && i.Severity == "Error" {
			t.Errorf("DP-only client error: %+v", i)
		}
		if dpOnly && strings.Contains(i.Message, "No segment") {
			t.Errorf("DP-only client without segments reported: %+v", i)
		}
	}
	found := false
	for _, i := range issues(r, "Client master", "10-digit") {
		found = found || (i.Key == "00000010" && i.Severity == "Warning")
	}
	if !found {
		t.Errorf("closed DP-only client's missing mobile not a warning: %+v", r.Manifest.Issues)
	}
	if len(issues(r, "Client master", "3 DP-only clients")) != 1 || len(issues(r, "Client master", "1 of them are closed accounts")) != 1 {
		t.Errorf("DP-only clients not counted: %+v", r.Manifest.Issues)
	}
	if len(issues(r, "Client master", "GROSSINCOME")) != 1 {
		t.Error("unmapped income codes not reported")
	}
	// the dormant-client count is of trading clients only
	if len(issues(r, "Client master", "2 clients with no active segment")) != 1 {
		t.Errorf("inactive trading clients: %+v", issues(r, "Client master", "no active segment"))
	}

	// nominees from CDNOMREG (never their Aadhaar numbers)
	_, nm := readCSV(t, r, "firms/02/master/NOMINEES.csv")
	var gn, kn []map[string]string
	for _, n := range nm {
		switch n["UCC"] {
		case "00000009":
			gn = append(gn, n)
		case "00000010":
			kn = append(kn, n)
		case "TEST001":
			if n["Name"] == "Not Used" {
				t.Error("a trading client's DP nomination used")
			}
		}
	}
	if len(gn) != 2 {
		t.Fatalf("nominees %v", gn)
	}
	wantCols(t, "nominee 1", gn[0], map[string]string{"Name": "Raju Test", "Relation": "Brother", "Share": "60", "DOB": "2010-01-01", "Guardian": "Hari Test",
		"Mobile": "9800000019", "Address": "9 DP Road KOLKATA 700009", "DeclaredOn": "2021-02-01", "IDType": "", "ID": ""})
	wantCols(t, "nominee 2", gn[1], map[string]string{"Name": "Lata Test", "Relation": "Spouse", "Share": "40", "IDType": "PAN", "ID": "AAAPT0019R", "Mobile": "", "DeclaredOn": "2021-02-01"})
	if len(kn) != 1 || kn[0]["Relation"] != "Father" || kn[0]["ID"] != "" {
		t.Errorf("closed account's nominee %v", kn)
	}
	if len(issues(r, "Nominees", "not a valid PAN")) != 1 {
		t.Error("nominee with a bad PAN not counted")
	}

	// UCC details from CLLIST and CLLIST_DET
	_, ud := readCSV(t, r, "firms/02/master/UCC-DETAILS.csv")
	du := rowsBy(ud, "UCC")
	wantCols(t, "UCC details 00000009", du["00000009"], map[string]string{"Address1": "9 DP Road", "Address2": "Block B", "Address3": "Near Park Ward 5", "City": "KOLKATA",
		"PIN": "700009", "State": "West Bengal", "Country": "India", "PermAddress1": "9 Village Road", "PermCity": "HOWRAH", "PermState": "West Bengal", "PermPIN": "711109",
		"Gender": "Female", "Guardian": "Hari Test", "Marital": "", "Nationality": "Indian", "Occupation": "1", "PEP": "N", "IPV": "Y", "AgreementDate": "2021-02-01",
		"BankType": "SB", "POASecurities": "N"})
	wantCols(t, "UCC details 00000010", du["00000010"], map[string]string{"Address1": "10 Old Lane", "PIN": "700010", "Occupation": "6", "Gender": ""})

	// the DP bank account
	_, bk := readCSV(t, r, "firms/02/master/CLIENT-BANKS.csv")
	bb := rowsBy(bk, "UCC")
	wantCols(t, "bank", bb["00000009"], map[string]string{"Bank": "TEST BANK", "IFSC": "TEST0000001", "Account": "123456789009", "Primary": "Y", "Status": "Active", "MICR": "700000009", "Type": "SB"})
	if w := issues(r, "Client bank accounts", "IFSC"); len(w) == 0 {
		t.Error("closed DP-only client's bank without IFSC not reported")
	} else {
		for _, i := range w {
			if i.Key == "00000010" && i.Severity != "Warning" {
				t.Errorf("closed client's bank %+v", i)
			}
		}
	}

	// KRA status and CKYC number recorded by the DP
	_, kr := readCSV(t, r, "firms/02/master/KRA-STATUS.csv")
	_, ck := readCSV(t, r, "firms/02/master/CKYC-NUMBERS.csv")
	if k := rowsBy(kr, "PAN")["AAAPT0009G"]; k["Status"] != "Validated" || !strings.Contains(k["Remarks"], "CLLIST_DET") {
		t.Errorf("DP-only KRA status %v", kr)
	}
	if rowsBy(ck, "PAN")["AAAPT0009G"]["CKYCNumber"] != "98765432109876" {
		t.Errorf("DP-only CKYC %v", ck)
	}
}

// DP-only clients belong to the firm whose company holds the DP.
func TestDPOnlyFirm(t *testing.T) {
	m := New()
	ctx := context.Background()
	co := companyFake("TT2627")
	if _, err := m.Firms(ctx, adminFake(), co); err != nil {
		t.Fatal(err)
	}
	k, _ := m.Classify(ctx, co)
	r := build(t, func(w *pkg.Writer) {
		if err := m.Current(ctx, co, k, migrate.Plan{Firms: []string{"02", "05"}}, w, nil); err != nil {
			t.Fatal(err)
		}
	})
	_, sec := readCSV(t, r, "firms/02/master/CLIENT-MASTER.csv")
	_, met := readCSV(t, r, "firms/05/master/CLIENT-MASTER.csv")
	if rowsBy(sec, "UCC")["00000009"] == nil {
		t.Error("DP-only client not in the DP's firm")
	}
	if len(met) != 1 || met[0]["UCC"] != "TEST006" {
		t.Errorf("metals firm's clients %v", met)
	}
}

// Every account of a client at the firm's DP is in DP-ACCOUNTS, one marked
// primary, with the joint holders; a primary demat account at another DP
// stays the client master's only.
func TestSeveralDPAccounts(t *testing.T) {
	r, _ := runCurrent(t, migrate.Plan{})
	_, dp := readCSV(t, r, "firms/02/master/DP-ACCOUNTS.csv")
	by := rowsBy(dp, "BOID")
	wantCols(t, "first account", by["1234567800000001"], map[string]string{"UCC": "TEST001", "Primary": "Y", "Holder2Name": "", "Holder2PAN": ""})
	wantCols(t, "joint account", by["1234567800000002"], map[string]string{"UCC": "TEST001", "Primary": "N", "Opened": "2022-06-01", "Holder2Name": "Ravi Test",
		"Holder2PAN": "AAAPT0011K", "Holder3Name": "Mira Test", "Holder3PAN": "BADPAN3"})
	if w := issues(r, "DP accounts", "BADPAN3"); len(w) != 1 || w[0].Key != "TEST001" || w[0].Severity != "Warning" {
		t.Errorf("joint holder's bad PAN %+v", w)
	}
	primaries := map[string]int{}
	for _, x := range dp {
		if x["Primary"] == "Y" {
			primaries[x["UCC"]]++
		}
	}
	for u, n := range primaries {
		if n != 1 {
			t.Errorf("%s has %d primary accounts", u, n)
		}
	}
	if len(primaries) != 7 {
		t.Errorf("clients with a primary account %v", primaries)
	}
	// TEST005's primary demat account is at another DP (CDMAST); its account
	// at this DP (linked by TRADINGID) is the DP's primary
	_, cl := readCSV(t, r, "firms/02/master/CLIENT-MASTER.csv")
	cm := rowsBy(cl, "UCC")
	if cm["TEST005"]["DematID"] != "8765432100001234" || cm["TEST005"]["Depository"] != "CDSL" || cm["TEST001"]["DematID"] != "1234567800000001" {
		t.Errorf("client master demat accounts %v %v", cm["TEST005"], cm["TEST001"])
	}
	for _, x := range dp {
		if strings.HasPrefix(x["BOID"], "87654321") || strings.HasPrefix(x["BOID"], "IN") {
			t.Errorf("another DP's account in DP-ACCOUNTS: %v", x)
		}
	}
	if by["1234567800000012"]["UCC"] != "TEST005" || by["1234567800000012"]["Primary"] != "Y" {
		t.Errorf("TEST005's account by TRADINGID %v", by["1234567800000012"])
	}
	if len(issues(r, "Client master", "1 trading clients' primary demat account in SharePro's client demat master (CDMAST) is at another DP")) != 1 ||
		len(issues(r, "Client master", "1 of them also have")) != 1 {
		t.Errorf("other-DP accounts not reported: %+v", r.Manifest.Issues)
	}

	// CDMAST names the second account: it is the primary
	co := companyFake("TT2627")
	co.on("client-demat", migrate.Row{"subcd": "TEST001", "type": "Primary", "dpid": "12345678", "clientid": "00000002"})
	r, _ = runCurrentOn(t, migrate.Plan{}, co)
	_, dp = readCSV(t, r, "firms/02/master/DP-ACCOUNTS.csv")
	by = rowsBy(dp, "BOID")
	_, cl = readCSV(t, r, "firms/02/master/CLIENT-MASTER.csv")
	if by["1234567800000002"]["Primary"] != "Y" || by["1234567800000001"]["Primary"] != "N" || rowsBy(cl, "UCC")["TEST001"]["DematID"] != "1234567800000002" {
		t.Errorf("CDMAST's primary not the primary: %v / %v", by["1234567800000001"], by["1234567800000002"])
	}

	// without CDMAST: the oldest open account
	co = companyFake("TT2627")
	delete(co.tables, "CDMAST")
	for _, x := range co.rows["dp-accounts"]("") {
		if x["benacnum"] == "00000001" {
			x["activedt"] = d("2023-01-01")
		}
	}
	r, _ = runCurrentOn(t, migrate.Plan{}, co)
	_, dp = readCSV(t, r, "firms/02/master/DP-ACCOUNTS.csv")
	by = rowsBy(dp, "BOID")
	_, cl = readCSV(t, r, "firms/02/master/CLIENT-MASTER.csv")
	if by["1234567800000002"]["Primary"] != "Y" || rowsBy(cl, "UCC")["TEST001"]["DematID"] != "1234567800000002" || rowsBy(cl, "UCC")["TEST005"]["DematID"] != "1234567800000012" {
		t.Errorf("oldest open account not the primary: %v", dp)
	}
	if _, rows := readCSV(t, r, "firms/02/master/CLIENT-DEMATS.csv"); len(rows) != 0 {
		t.Errorf("client demat accounts without CDMAST %v", rows)
	}
	if len(co.asked("client-demat")) != 0 {
		t.Error("CDMAST read when the database has none")
	}
}

// A DP-only client's code is the DP client code, unless a trading client
// has it.
func TestDPOnlyCode(t *testing.T) {
	used := map[string]bool{"00000009": true}
	for _, c := range []struct {
		a          dpAcct
		code, note string
	}{
		{dpAcct{co: "06", ben: "00000010"}, "00000010", ""},
		{dpAcct{co: "06", ben: "00000009"}, "DP00000009", "already a client code"},
		{dpAcct{co: "06", boid: "1234567800000042"}, "00000042", ""},
		{dpAcct{co: "06", ben: "AB"}, "DPAB", "not 3 to 10"},
		{dpAcct{co: "06", ben: "A-B"}, "", "no other code"},
	} {
		code, note := dpOnlyCode(&c.a, used)
		if code != c.code || (c.note == "") != (note == "") || !strings.Contains(note, c.note) {
			t.Errorf("%+v: %q %q", c.a, code, note)
		}
	}
	co := companyFake("TT2627")
	clients := co.rows["clients"]("")
	co.on("clients", append(clients, migrate.Row{"subcd": "00000009", "descr": "Numbered Test", "pan_no": "AAAPT0099N"})...)
	r, _ := runCurrentOn(t, migrate.Plan{}, co)
	_, dp := readCSV(t, r, "firms/02/master/DP-ACCOUNTS.csv")
	if rowsBy(dp, "BOID")["1234567800000009"]["UCC"] != "DP00000009" {
		t.Errorf("DP-only code taken by a trading client: %v", dp)
	}
	if w := issues(r, "Client master", "already a client code"); len(w) != 1 || w[0].Key != "DP00000009" {
		t.Errorf("code change not reported: %+v", w)
	}
}

// The history of a DP-only client's account carries its client code, and
// fractional quantities keep their decimals.
func TestDPOnlyHistory(t *testing.T) {
	m := New()
	ctx := context.Background()
	co := companyFake("TT2627")
	if _, err := m.Firms(ctx, adminFake(), co); err != nil {
		t.Fatal(err)
	}
	ck, _ := m.Classify(ctx, co)
	h := companyFake("TT2526")
	h.on("hist-dp", migrate.Row{"co": "06", "trdt": d("2025-06-01"), "benacnum": "00000010", "isincd": "INF000A01015", "drcr": "D", "qtybooked": "12.3450", "trancode": "ODTA", "npk": int64(4)})
	h.on("hist-hold-date", migrate.Row{"d": d("2026-03-31")})
	h.on("hist-holdings", migrate.Row{"co": "06", "benacnum": "00000009", "isincd": "INF000A01015", "balance": "125.4320", "curbal": "100.4320", "pldgbal": "25.000"})
	hk, _ := m.Classify(ctx, h)
	plan := migrate.Plan{Firms: []string{"02"}}
	r := build(t, func(w *pkg.Writer) {
		if err := m.Current(ctx, co, ck, plan, w, nil); err != nil {
			t.Fatal(err)
		}
		if err := m.History(ctx, h, hk, plan, w, nil); err != nil {
			t.Fatal(err)
		}
	})
	_, tx := readCSV(t, r, "firms/02/history/2025-26/DP-TRANSACTIONS.csv")
	if len(tx) != 1 || tx[0]["UCC"] != "00000010" || tx[0]["BOID"] != "1234567800000010" || tx[0]["Quantity"] != "-12.345" {
		t.Errorf("DP transactions %v", tx)
	}
	_, ye := readCSV(t, r, "firms/02/history/2025-26/DP-HOLDINGS-YEAR-END.csv")
	if len(ye) != 1 || ye[0]["UCC"] != "00000009" || ye[0]["BOID"] != "1234567800000009" || ye[0]["Total"] != "125.432" || ye[0]["Free"] != "100.432" || ye[0]["Locked"] != "0" {
		t.Errorf("year-end holdings %v", ye)
	}
}
