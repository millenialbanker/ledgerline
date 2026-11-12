package sharepro

import (
	"context"
	"strings"
	"testing"

	"ledgerline/internal/migrate"
	"ledgerline/internal/migrate/pkg"
)

// The DP company's ledgers by DP client code (CLLIST.BENACNUM) go to the
// client of each DP account: the trading client (its several accounts
// adding up with its trading ledger) or the DP-only client.
func TestDPLedgers(t *testing.T) {
	r, co := runCurrent(t, migrate.Plan{})
	_, ob := readCSV(t, r, "firms/02/master/OPENING-BALANCES.csv")
	acct := rowsBy(ob, "Account")
	for code, want := range map[string][3]string{
		"CLIENT:TEST001":             {"1270.00", "", "2026-04-05"}, // 1200 trading + 50 + 20 on its two DP accounts
		"CLIENT:00000009":            {"145.00", "", "2026-08-01"},  // CDSL CLIENTS 120 + CDSL OVERDUE CLIENT 25
		"CLIENT:00000010":            {"", "10.00", ""},             // ADVANCE DP TRAN CHARGE: an advance from the client
		"RECEIVABLE:06-64-00000077":  {"30.00", "", ""},             // a DP client code not in the DP master
		"BANK:06-DPBANK":             {"", "235.00", ""},
		"RECEIVABLE:06-64-00000001":  {"", "", ""},
		"RECEIVABLE:06-2ODCL":        {"", "", ""},
		"PAYABLE:06-2ADVTR-00000010": {"", "", ""},
	} {
		got := acct[code]
		if got["Debit"] != want[0] || got["Credit"] != want[1] || got["Since"] != want[2] {
			t.Errorf("%s = %v, want debit %q credit %q since %q", code, got, want[0], want[1], want[2])
		}
	}
	// per head: sub-ledgers mapped and not (a warning with the totals)
	w := issues(r, "Client ledgers", "Head 64 of company 06 (CDSL CLIENTS)")
	if len(w) != 1 || w[0].Severity != "Warning" || !strings.Contains(w[0].Message, "5 sub-ledgers are accounts of the firm's DP") ||
		!strings.Contains(w[0].Message, "3 of them with a balance, debits 190.00, credits 0.00") || !strings.Contains(w[0].Message, "1 sub-ledgers are not") ||
		!strings.Contains(w[0].Message, "1 with a balance, debits 30.00, credits 0.00") {
		t.Errorf("head 64 %+v", w)
	}
	for head, n := range map[string]string{"2ODCL of company 06 (CDSL OVERDUE CLIENT)": "1 sub-ledgers are accounts", "2ADVTR of company 06 (ADVANCE DP TRAN CHARGE [Adjustable])": "2 sub-ledgers are accounts"} {
		i := issues(r, "Client ledgers", "Head "+head)
		if len(i) != 1 || i[0].Severity != "Info" || !strings.Contains(i[0].Message, n) || !strings.Contains(i[0].Message, "Every sub-ledger is an account of the firm's DP") {
			t.Errorf("head %s %+v", head, i)
		}
	}
	// the server's checks map the DP client codes the same way
	tb, cb := co.asked("check-tb"), co.asked("check-client-balances")
	if len(tb) != 1 || !strings.Contains(tb[0], "FROM CLLIST l WHERE l.CO IN ('06')") || !strings.Contains(tb[0], "'06|2ADVTR','06|2ODCL','06|64'") ||
		!strings.Contains(tb[0], "RTRIM(l.TRADINGID)") || !strings.Contains(tb[0], "HAVING MIN(o.u) = MAX(o.u)") {
		t.Errorf("trial balance check %v", tb)
	}
	if len(cb) != 1 || !strings.Contains(cb[0], "UNION ALL SELECT o.u AS k") || !strings.Contains(cb[0], "'06|2ADVTR','06|2ODCL','06|64'") {
		t.Errorf("client balance check %v", cb)
	}
	if len(issues(r, "Opening balances", "already due before the year began")) != 0 {
		t.Errorf("DP debits dated the year start: %+v", issues(r, "Opening balances", "already due"))
	}
	if q := co.asked("client-days"); len(q) != 1 || !strings.Contains(q[0], "ORDER BY x.VRDT") || !strings.Contains(q[0], "'64'") {
		t.Errorf("client ledger lines %v", q)
	}
}

// The history ledger gives a DP account's lines its client.
func TestDPLedgerHistory(t *testing.T) {
	m := New()
	ctx := context.Background()
	co := companyFake("TT2627")
	if _, err := m.Firms(ctx, adminFake(), co); err != nil {
		t.Fatal(err)
	}
	ck, _ := m.Classify(ctx, co)
	h := companyFake("TT2526")
	line := func(sub, dr, cr string, idn int64) migrate.Row {
		return migrate.Row{"co": "06", "type": "", "tt": "DP", "vrno": "1", "vrdt": d("2025-07-01"), "accd": "64", "subcode": sub, "narr": "DP charges", "amtdr": dr, "amtcr": cr, "idn": idn}
	}
	h.on("hist-jv", line("00000009", "10.00", ".00", 1), line("00000002", "5.00", ".00", 2), line("00000012", "3.00", ".00", 3), line("00000077", "2.00", ".00", 4),
		migrate.Row{"co": "06", "type": "", "tt": "DP", "vrno": "1", "vrdt": d("2025-07-01"), "accd": "DPBANK", "subcode": "", "narr": "DP charges", "amtdr": ".00", "amtcr": "20.00", "idn": int64(5)})
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
	_, led := readCSV(t, r, "firms/02/history/2025-26/LEDGER.csv")
	got := map[string]string{}
	for _, l := range led {
		got[l["SourceRef"]] = l["Account"] + "|" + l["UCC"]
	}
	for ref, want := range map[string]string{"JVFILE:06:DP/1:1": "CLIENT:00000009|00000009", "JVFILE:06:DP/1:2": "CLIENT:TEST001|TEST001",
		"JVFILE:06:DP/1:3": "CLIENT:TEST005|TEST005", "JVFILE:06:DP/1:4": "RECEIVABLE:06-64-00000077|"} {
		if got[ref] != want {
			t.Errorf("ledger line %s = %q, want %q", ref, got[ref], want)
		}
	}
}

// A DP account with no trading code whose TRADINGID is a client is that
// client's; one whose TRADINGID is not stays DP-only.
func TestTradingID(t *testing.T) {
	r, co := runCurrent(t, migrate.Plan{})
	_, dp := readCSV(t, r, "firms/02/master/DP-ACCOUNTS.csv")
	by := rowsBy(dp, "BOID")
	if by["1234567800000012"]["UCC"] != "TEST005" || by["1234567800000010"]["UCC"] != "00000010" {
		t.Errorf("DP accounts %v / %v", by["1234567800000012"], by["1234567800000010"])
	}
	_, cl := readCSV(t, r, "firms/02/master/CLIENT-MASTER.csv")
	if _, ok := rowsBy(cl, "UCC")["00000012"]; ok {
		t.Error("an account linked by TRADINGID became a DP-only client")
	}
	if len(issues(r, "DP accounts", "1 DP accounts with no trading code (CLLIST.SUBCODE) are linked to their trading client by CLLIST.TRADINGID")) != 1 {
		t.Errorf("TRADINGID links not counted: %+v", issues(r, "DP accounts", "TRADINGID"))
	}
	for _, tag := range []string{"check-dp-only-clients", "dp-accounts"} {
		if q := co.asked(tag); len(q) != 1 || !strings.Contains(q[0], "TRADINGID") {
			t.Errorf("%s does not use TRADINGID: %v", tag, q)
		}
	}
}

// CLIENT-DEMATS: every demat account of each client in CDMAST, at this DP
// and others; the client master's is the primary.
func TestClientDemats(t *testing.T) {
	r, _ := runCurrent(t, migrate.Plan{})
	_, rows := readCSV(t, r, "firms/02/master/CLIENT-DEMATS.csv")
	var got []string
	for _, x := range rows {
		got = append(got, strings.Join([]string{x["UCC"], x["Depository"], x["DematID"], x["Primary"], x["Status"]}, "|"))
	}
	want := "TEST001|CDSL|1234567800000001|Y|Active TEST001|CDSL|8765432100000777|N|Active TEST001|NSDL|IN30012310000001|N|Inactive " +
		"TEST005|CDSL|8765432100001234|Y|Active TEST005|NSDL|IN30045620000002|N|Active"
	if strings.Join(got, " ") != want {
		t.Errorf("client demat accounts\n got %s\nwant %s", strings.Join(got, " "), want)
	}
	if e := issues(r, "Client demat accounts", "primary demat account"); len(e) != 1 || e[0].Key != "TEST004" || e[0].Severity != "Error" || !strings.Contains(e[0].Message, `DP code "D7" is not in the DP master`) {
		t.Errorf("active client's bad primary %+v", e)
	}
	if w := issues(r, "Client demat accounts", "not 8 digits"); len(w) != 1 || w[0].Key != "TEST003" || w[0].Severity != "Warning" {
		t.Errorf("inactive client's bad account %+v", w)
	}
	if len(issues(r, "Client demat accounts", "5 demat accounts of 2 clients")) != 1 || len(issues(r, "Client demat accounts", "1 CDMAST rows repeat")) != 1 {
		t.Errorf("client demat counts %+v", r.Manifest.Issues)
	}
	// the client master keeps the primary
	_, cl := readCSV(t, r, "firms/02/master/CLIENT-MASTER.csv")
	if c := rowsBy(cl, "UCC"); c["TEST001"]["DematID"] != "1234567800000001" || c["TEST004"]["DematID"] != "1234567800000004" {
		t.Errorf("client master demat accounts %v %v", c["TEST001"], c["TEST004"])
	}
}

func TestCDMASTDemat(t *testing.T) {
	for _, c := range []struct {
		dpid, clientid, dpname, boid, dep, bad string
	}{
		{"12345678", "00000001", "", "1234567800000001", "CDSL", ""},
		{"IN300123", "10000001", "", "IN30012310000001", "NSDL", ""},
		{"300456", "20000002", "SOME NSDL DP", "IN30045620000002", "NSDL", ""},
		{"", "1234567800000042", "", "1234567800000042", "CDSL", ""},
		{"1234567", "00000001", "", "", "", "do not make a CDSL"},
		{"IN30012", "10000001", "", "", "", "do not make a NSDL"},
		{"", "00000001", "", "", "", "not in the DP master"},
		{"12345678", "123", "", "", "", "not 8 digits"},
	} {
		boid, dep, bad := cdmastDemat(c.dpid, c.clientid, c.dpname, "D1")
		if boid != c.boid || dep != c.dep || (c.bad == "") != (bad == "") || !strings.Contains(bad, c.bad) {
			t.Errorf("%+v: %q %q %q", c, boid, dep, bad)
		}
	}
}

// A DP client head whose name is not a client ledger's stays one firm
// account; its sub-ledgers are not sub-accounts of their own.
func TestDPHeadNotMerged(t *testing.T) {
	ch := &chart{groups: map[string][2]string{}, heads: map[string]head{"DPSTMP": {accd: "DPSTMP", descr: "DP STAMP DUTY", group: "CLIB"}}, coHead: map[string]string{"06|DPSTMP": "DP STAMP DUTY"},
		subs: map[string]string{"06|DPSTMP|00000009": "Gita Test", "06|DPSTMP|00000001": "Asha Test", "06|DPSTMP|00000077": "Old"}, exchTypes: map[string]bool{},
		isClient: map[string]bool{}, coded: map[string]bool{}, merged: map[string]bool{}, bookHead: map[string]string{}, unknown: map[string]bool{}}
	owners := map[string]string{}
	addOwner(owners, "00000009", "00000009")
	addOwner(owners, "00000001", "TEST001")
	addOwner(owners, "00000005", "TEST005")
	addOwner(owners, "00000005", "TEST006") // two clients: none
	ch.dpLedgers(owners)
	if !ch.dpCoded["06|DPSTMP"] || ch.dpMerged["06|DPSTMP"] || ch.dpSubs["06|DPSTMP"] != [2]int{2, 1} {
		t.Fatalf("head %v %v %v", ch.dpCoded, ch.dpMerged, ch.dpSubs)
	}
	if code, _, u := ch.account("06", "DPSTMP", "00000009"); code != "PAYABLE:06-DPSTMP" || u != "" || ch.clientOf("06", "DPSTMP", "00000009") != "00000009" {
		t.Errorf("mapped sub-ledger of a head that is not a client ledger: %s %s", code, u)
	}
	if code, _, _ := ch.account("06", "DPSTMP", "00000077"); code != "PAYABLE:06-DPSTMP-00000077" {
		t.Errorf("unmapped sub-ledger %s", code)
	}
	if owners["00000005"] != "" {
		t.Error("a code of two clients has a client")
	}
}
