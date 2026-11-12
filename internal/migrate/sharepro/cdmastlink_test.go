package sharepro

import (
	"strings"
	"testing"
	"time"

	"ledgerline/internal/migrate"
)

// A DP account with no trading code that a trading client of the first
// holder's PAN lists as its demat account (CDMAST) is that client's, not a
// DP-only client; closed at the DP, it is no primary and inactive in the
// client's list, and an open account is picked for the client master.
func TestCDMASTLinksDPAccount(t *testing.T) {
	co := companyFake("TT2627")
	d := func(s string) time.Time { t, _ := time.Parse("2006-01-02", s); return t }
	acct := migrate.Row{"co": "06", "dpid": "12345678", "benacnum": "00000020", "fbenacnum": "1234567800000020", "subcode": "", "activedt": d("2016-01-01"),
		"benclosedt": d("2022-05-31"), "benstat": "3", "ben1holder": "Bina Test", "itpan": "AAAPT0002B", "branchid": "HO", "bentype": int64(1)}
	prevDP, prevCD := co.rows["dp-accounts"], co.rows["client-demat"]
	co.rows["dp-accounts"] = func(q string) []migrate.Row { return append(prevDP(q), acct) }
	co.rows["client-demat"] = func(q string) []migrate.Row {
		return append(prevCD(q),
			// TEST002's primary: the closed account at the firm's DP
			migrate.Row{"subcd": "TEST002", "type": "Primary", "dpcode": "D1", "dpid": "12345678", "dpname": "TT SECURITIES (CDSL)", "clientid": "00000020", "closed": false},
			migrate.Row{"subcd": "TEST002", "type": "Secondary", "dpcode": "D2", "dpid": "87654321", "dpname": "OTHER DP", "clientid": "00002222", "closed": false})
	}
	r, _ := runCurrentOn(t, migrate.Plan{}, co)
	_, dp := readCSV(t, r, "firms/02/master/DP-ACCOUNTS.csv")
	b := rowsBy(dp, "BOID")["1234567800000020"]
	if b["UCC"] != "TEST002" || b["Status"] != "Closed" || b["Primary"] != "N" {
		t.Fatalf("DP account %v", b)
	}
	_, cl := readCSV(t, r, "firms/02/master/CLIENT-MASTER.csv")
	cm := rowsBy(cl, "UCC")
	if _, ok := cm["00000020"]; ok {
		t.Error("a DP-only client made of a trading client's account")
	}
	if cm["TEST002"]["DematID"] != "8765432100002222" {
		t.Errorf("TEST002's demat account %q: want its open one", cm["TEST002"]["DematID"])
	}
	_, ds := readCSV(t, r, "firms/02/master/CLIENT-DEMATS.csv")
	st := map[string]string{}
	for _, x := range ds {
		if x["UCC"] == "TEST002" {
			st[x["DematID"]] = x["Status"] + "/" + x["Primary"]
		}
	}
	if st["1234567800000020"] != "Inactive/N" || st["8765432100002222"] != "Active/N" {
		t.Errorf("TEST002's demat accounts %v", st)
	}
	if len(issues(r, "DP accounts", "listed as a demat account in the client demat master")) != 1 {
		t.Errorf("the CDMAST link not reported: %+v", r.Manifest.Issues)
	}
	// the checks' SQL takes the same link
	link := false
	for _, q := range co.queries {
		if strings.Contains(q, "/*sp:check-dp-only-clients*/") && strings.Contains(q, "FROM CDMAST cd") {
			link = true
		}
	}
	if !link {
		t.Error("the DP-only clients' check does not follow the CDMAST link")
	}
}
