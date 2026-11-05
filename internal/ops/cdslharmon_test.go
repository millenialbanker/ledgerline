package ops

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"ledgerline/internal/docs"
)

// cdslSample is one of CDSL's sample files (public, dummy data).
func cdslSample(t *testing.T, prefix string) (string, []byte) {
	t.Helper()
	ents, err := os.ReadDir("../xfile/testdata/cdsl")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), prefix) {
			b, err := os.ReadFile("../xfile/testdata/cdsl/" + e.Name())
			if err != nil {
				t.Fatal(err)
			}
			return e.Name(), b
		}
	}
	t.Fatalf("no sample %s", prefix)
	return "", nil
}

// cdslLinkBO points a client's demat (and its BO account) at a BO ID.
func cdslLinkBO(t *testing.T, o *Ops, ucc, bo string) {
	t.Helper()
	ctx := t.Context()
	if _, err := docs.Update(ctx, o.Docs, T, cClient, ucc, "admin", func(c *Client) error { c.Demat = Demat{Depository: "CDSL", ID: bo}; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := docs.Upsert(ctx, o.Docs, T, cBO, ucc, BOAccount{UCC: ucc, BOID: bo, Depository: "CDSL", Status: "Active", Opened: "2024-01-01"}, "admin"); err != nil {
		t.Fatal(err)
	}
}

func cdslWork(t *testing.T, o *Ops, process, keyPrefix string) []WorkItem {
	t.Helper()
	ws, _ := docs.List[WorkItem](t.Context(), o.Docs, T, cWorkItem, "")
	var out []WorkItem
	for _, w := range ws {
		if w.Process == process && strings.HasPrefix(strings.TrimPrefix(w.Key, process+":"), keyPrefix) {
			out = append(out, w)
		}
	}
	return out
}

// cdslCMFile is a made-up client master (synthetic BO IDs, names, PANs).
func cdslCMFile(t *testing.T, lines ...map[string]string) []byte {
	t.Helper()
	h := xheader(t, "cdsl-client-master")
	out := []string{strings.Join(h, ",")}
	for i, l := range lines {
		v := map[string]string{"Src": "CDSL", "CntrlSctiesDpstryPtcpt": "81600", "BrnchId": "000000", "RcrdNb": "1", "RcdSRNumber": "1", "PurpCd": "DFT", "AcctSts": "DFT", "BSDAFlg": "DFT", "POAOrDDPITp": "DFT"}
		for k, x := range l {
			v[k] = x
		}
		v["RcdSRNumber"] = firstOf(l["RcdSRNumber"], fmt.Sprint(i+1))
		out = append(out, xline(h, v))
	}
	return []byte(strings.Join(out, "\r\n") + "\r\n")
}

func TestCDSLClientMasterSample(t *testing.T) {
	o, ctx := setup(t)
	cs, _ := o.Clients(ctx, T)
	var uccs []string
	for _, c := range cs {
		if c.Status == "Active" && rePAN.MatchString(c.PAN) && len(uccs) < 3 {
			uccs = append(uccs, c.UCC)
		}
	}
	if len(uccs) < 3 {
		t.Skip("not enough sample clients")
	}
	// The sample's accounts: frozen (FRZ FB) with BO-UCC links; a BSDA; a
	// pending account with POA links (settlement only for the second holder).
	frozen, bsda, pending := uccs[0], uccs[1], uccs[2]
	cdslLinkBO(t, o, frozen, "1304140007417063")
	cdslLinkBO(t, o, bsda, "1304140007965606")
	cdslLinkBO(t, o, pending, "1304140008056337")
	name, data := cdslSample(t, "CLN_MSTR")
	res, err := o.LoadExchangeFile(ctx, T, "priya.nair", name, data, true)
	if err != nil || len(res) != 1 || res[0].Format != "cdsl-client-master" || res[0].Problems != 0 || res[0].Added != 3 || !strings.HasPrefix(res[0].Loaded, "Loaded into the BO accounts") {
		t.Fatalf("%+v %v", res, err)
	}
	if res[0].Skipped != 8 {
		t.Errorf("not linked: %d", res[0].Skipped)
	}
	get := func(u string) BOAccount {
		b, _, err := docs.Get[BOAccount](ctx, o.Docs, T, cBO, u)
		if err != nil || b.CDSL == nil {
			t.Fatalf("%s: %+v %v", u, b, err)
		}
		return b
	}
	b := get(frozen)
	if b.CDSL.Status != "ACT" || b.Status != "Active" || b.CDSLFreeze != "Both" || !b.debitFrozen() || !b.creditFrozen() || b.DDPI {
		t.Errorf("frozen account: %+v %+v", b, b.CDSL)
	}
	if !contains(b.CDSL.CMIDs, "959") || !contains(b.CDSL.CMIDs, "M51340") || len(b.CDSL.UCCLinks) != 4 || b.CDSL.Nominees != 1 || b.CDSL.NoNomination != "NO" || !b.CDSL.PANVerified {
		t.Errorf("links and flags: %+v", b.CDSL)
	}
	if b := get(bsda); !b.BSDA || b.CDSL.BSDA != "BSDA" {
		t.Errorf("BSDA account: %+v", b)
	}
	b = get(pending)
	if b.Status != "Pending activation" || len(b.CDSL.POA) != 6 || b.DDPI || b.CDSL.DDPIBy != "" || b.CDSL.POA[3].Nature != "SET" || b.CDSL.POA[3].Holder != "SRH" || b.CDSL.POA[0].MasterID != "2304140000001584" {
		t.Errorf("pending account: %+v %+v", b, b.CDSL)
	}
	// KYC details are never overwritten: the differences go for review.
	c, _ := o.Client(ctx, T, frozen)
	if c.Demat.ID != "1304140007417063" || strings.Contains(c.Name, "YAMINZE") {
		t.Fatalf("client changed: %+v", c)
	}
	if w := cdslWork(t, o, "cdslcm", "kyc:"+frozen); len(w) != 1 || !strings.Contains(w[0].Detail, "PAN (CDSL") || strings.Contains(w[0].Detail, "EYBPS0159D") {
		t.Errorf("review item: %+v", w)
	}
	if w := cdslWork(t, o, "cdslcm", "unlinked:"); len(w) != 1 {
		t.Errorf("unlinked: %+v", w)
	}
	// A frozen account cannot deliver.
	if _, err := o.IssueDISBook(ctx, T, "priya.nair", frozen, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := o.EnterDIS(ctx, T, "priya.nair", DIS{UCC: frozen, Slip: 1000001, Kind: "Off-market", ToBO: "1208160099999999", Reason: "Gift", ExecDate: D, Received: D, Lines: []DISLine{{ISIN: "INE000X01010", Qty: 1}}}); !errors.Is(err, ErrState) {
		t.Errorf("DIS from a frozen account: %v", err)
	}
	if why := o.payoutRisk(ctx, T, c); !strings.Contains(why, "frozen for debits at CDSL") {
		t.Errorf("payout risk: %q", why)
	}
}

// DDPI from the client master decides how a sale is delivered: with it,
// DDPI; without it, eDIS.
func TestCDSLClientMasterDrivesSellFlow(t *testing.T) {
	o, ctx := setup(t)
	if _, err := o.SaveConnection(ctx, T, "admin", connCDSL, map[string]string{"dp_id": "12081600", "env": cdslEnvMock, "edis_cm_nse": "M1234", cdslFamEDIS: "On"}, map[string]string{cdxKeyEDIS: testEDISKey}); err != nil {
		t.Fatal(err)
	}
	c, _ := cdxTestClient(t, o, ctx)
	bo := o.bo(ctx, T, c).BOID
	if need, _ := o.edisDeliverable(ctx, T, D); !need[c.UCC] {
		t.Fatal("without a DDPI the client should sell through eDIS")
	}
	cfg := o.CDSLPledgeSettings(ctx, T)
	cfg.DDPIPOAID = "1208160000009999"
	if _, err := o.SaveCDSLPledgeSettings(ctx, T, "vikram.shah", cfg); err != nil {
		t.Fatal(err)
	}
	fh := map[string]string{"ClntId": bo, "Purpse": "FH", "PurpCd": "CORAD", "AcctSts": "ACT", "BSDAFlg": "NBSD", "FrstNm": c.Name, "PAN": c.PAN, "PANVrfyFlg": "PANVLD", "FrzSts": "NFZ", "FrznFor": "DFT"}
	poa := func(master, nature, holder, to string) map[string]string {
		return map[string]string{"ClntId": bo, "Purpse": "POALB", "POAOrDDPITp": "DDPI", "POAId": "77001", "POAOrDDPIId": "77001", "POAMstrID": master, "GPABPAFlg": nature, "PoaLnkPurpCd": holder, "FrDt": "2026-01-01", "ToDt": to, "POAToOprtAcct": "YES"}
	}
	load := func(name string, lines ...map[string]string) {
		t.Helper()
		res, err := o.LoadExchangeFile(ctx, T, "priya.nair", name, cdslCMFile(t, lines...), true)
		if err != nil || res[0].Problems != 0 || res[0].Added != 1 {
			t.Fatalf("%+v %v", res, err)
		}
	}
	// A DDPI for margin pledge only, and one to another broker's master, do not count.
	load("CLN_MSTR_081600_500001_I_202610161200_001_P0.csv", fh, poa("1208160000009999", "MRP", "ARH", ""), poa("1208160000001111", "SET", "ARH", ""))
	if b := o.bo(ctx, T, c); b.DDPI || len(b.CDSL.POA) != 2 {
		t.Fatalf("DDPI without a settlement link to the firm: %+v", b.CDSL)
	}
	load("CLN_MSTR_081600_500002_I_202610161300_001_P0.csv", fh, poa("1208160000009999", "SET", "ARH", "2027-12-31"))
	b := o.bo(ctx, T, c)
	if !b.DDPI || !strings.Contains(b.CDSL.DDPIBy, "77001") {
		t.Fatalf("DDPI not recorded: %+v", b.CDSL)
	}
	if need, _ := o.edisDeliverable(ctx, T, D); need[c.UCC] {
		t.Fatal("with a DDPI the client's sales go by DDPI, not eDIS")
	}
	if w := cdslWork(t, o, "cdslcm", "ddpi:"+c.UCC+":on"); len(w) != 1 {
		t.Errorf("DDPI work item: %+v", w)
	}
	// The DDPI ends: back to eDIS.
	load("CLN_MSTR_081600_500003_I_202610161400_001_P0.csv", fh, poa("1208160000009999", "SET", "ARH", "2026-06-30"))
	if b := o.bo(ctx, T, c); b.DDPI {
		t.Fatal("an expired DDPI still counts")
	}
	if need, _ := o.edisDeliverable(ctx, T, D); !need[c.UCC] {
		t.Fatal("after the DDPI ends the client sells through eDIS")
	}
	// A full file lists the DP's accounts it does not have.
	load("CLN_MSTR_081600_500004_F_202610161500_001_P0.csv", fh)
	if w := cdslWork(t, o, "cdslcm", "missing:500004"); len(w) != 1 || strings.Contains(w[0].Detail, c.UCC) {
		t.Errorf("missing accounts: %+v", w)
	}
}

func TestCDSLMaskUID(t *testing.T) {
	in := []byte("ClntId,UID,PAN,VID\r\n1208160000000001,123412341234,ABCDE1234F,9999888877776666\r\n1208160000000002,,ABCDE1234G,\r\n")
	out := string(cdslMaskUID(in))
	if strings.Contains(out, "123412341234") || strings.Contains(out, "9999888877776666") || !strings.Contains(out, "XXXXXXXX1234") || !strings.Contains(out, "XXXXXXXX6666") || !strings.Contains(out, "ABCDE1234F") || !strings.HasSuffix(out, "\r\n") {
		t.Fatalf("%q", out)
	}
}

// dpe6 is a made-up freeze master line (communiqué 2026/621 layout, one
// name field).
func dpe6(id, bo, isin, qtyType, qty, status, forCode, ucrDate, reason, authority string) string {
	f := []string{id, bo, "TEST HOLDER", isin, "", qtyType, qty, qty, status, forCode, "2", "2", "15102026:101500", "1", "15102026:101500", "S", "", "11", reason, "REF" + id, "", ucrDate, "", "", "OPR1", "", authority, "ORDER 1 OF 2026", "ATTACHMENT", ""}
	return strings.Join(f, "~")
}

func TestCDSLFreezeMasterBlocksAndLifts(t *testing.T) {
	o, ctx := setup(t)
	c, h := cdxTestClient(t, o, ctx)
	bo := o.bo(ctx, T, c).BOID
	// Another client for a partial ISIN freeze.
	var c2 Client
	var h2 Holding
	cs, _ := o.ClientMap(ctx, T)
	for _, u := range sortedKeys(cs) {
		x := cs[u]
		if x.UCC == c.UCC || !strings.HasPrefix(o.bo(ctx, T, x).BOID, "12081600") {
			continue
		}
		hs, _ := o.Holdings(ctx, T, x.UCC)
		for _, y := range hs {
			if o.freeBalance(ctx, T, x.UCC, y.ISIN) >= 20 {
				c2, h2 = x, y
			}
		}
		if c2.UCC != "" {
			break
		}
	}
	if c2.UCC == "" {
		t.Skip("no second client with a holding")
	}
	bo2 := o.bo(ctx, T, c2).BOID
	free2 := o.freeBalance(ctx, T, c2.UCC, h2.ISIN)
	file := dpe6("5001", bo, "", "F", "0", "A", "1", "", "ORDER FROM CBI", "CBI MUMBAI") + "\r\n" + dpe6("5002", bo2, h2.ISIN, "P", "10.000", "A", "3", "", "COURT ORDER", "") + "\r\n"
	res, err := o.LoadExchangeFile(ctx, T, "priya.nair", "18DPE6U16102026.001", []byte(file), true)
	if err != nil || res[0].Format != "cdsl-freeze-master" || res[0].Problems != 0 || res[0].Added != 2 {
		t.Fatalf("%+v %v", res, err)
	}
	b := o.bo(ctx, T, c)
	if b.CDSLFreeze != "Debit" || !b.debitFrozen() || b.creditFrozen() || !strings.Contains(b.freezeWhy(), "CBI MUMBAI") {
		t.Fatalf("account freeze: %+v", b)
	}
	// Debits stop: DIS, eDIS client check, pledges, early pay-in; funds payouts wait for a person.
	book, err := o.IssueDISBook(ctx, T, "priya.nair", c.UCC, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.EnterDIS(ctx, T, "priya.nair", DIS{UCC: c.UCC, Slip: book.From, Kind: "Off-market", ToBO: "1208160099999999", Reason: "Gift", ExecDate: D, Received: D, Lines: []DISLine{{ISIN: h.ISIN, Qty: 1}}}); !errors.Is(err, ErrState) {
		t.Errorf("DIS: %v", err)
	}
	if _, err := o.RequestMarginPledge(ctx, T, "priya.nair", c.UCC, "MP", "", []PledgeLine{{ISIN: h.ISIN, Qty: 1}}); err == nil {
		t.Error("pledge from a frozen account")
	}
	if why := o.payoutRisk(ctx, T, c); why == "" {
		t.Error("payout not held")
	}
	// A partial freeze takes its quantity out of the free balance; a credit freeze stops pay-outs.
	if got := o.freeBalance(ctx, T, c2.UCC, h2.ISIN); got != free2-10 {
		t.Errorf("free %d, was %d", got, free2)
	}
	if why := o.settleFrozen(ctx, T, Obligation{UCC: c2.UCC, ISIN: h2.ISIN, Symbol: h2.Symbol}, "payout"); !strings.Contains(why, "frozen at CDSL") {
		t.Errorf("pay-out: %q", why)
	}
	if why := o.settleFrozen(ctx, T, Obligation{UCC: c2.UCC, ISIN: h2.ISIN}, "payin"); why != "" {
		t.Errorf("a partial freeze does not stop the pay-in of the rest: %q", why)
	}
	if w := cdslWork(t, o, "cdslfreeze", "5001:set"); len(w) != 1 || w[0].UCC != c.UCC {
		t.Errorf("freeze work item: %+v", w)
	}
	// The freeze is lifted (unfreeze date and status): the block goes.
	lift := dpe6("5001", bo, "", "F", "0", "U", "1", "16102026:120000", "ORDER FROM CBI", "CBI MUMBAI") + "\r\n"
	res, err = o.LoadExchangeFile(ctx, T, "priya.nair", "18DPE6U16102026.002", []byte(lift), true)
	if err != nil || res[0].Added != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if b := o.bo(ctx, T, c); b.CDSLFreeze != "" || b.debitFrozen() {
		t.Fatalf("still frozen: %+v", b)
	}
	if w := cdslWork(t, o, "cdslfreeze", "5001:lift"); len(w) != 1 {
		t.Errorf("lift work item: %+v", w)
	}
	if w := cdslWork(t, o, "cdslfreeze", "5001:set"); len(w) != 1 || w[0].Status != "Done" {
		t.Errorf("the freeze item should close: %+v", w)
	}
	if _, err := o.EnterDIS(ctx, T, "priya.nair", DIS{UCC: c.UCC, Slip: book.From, Kind: "Off-market", ToBO: "1208160099999999", Reason: "Gift", ExecDate: D, Received: D, Lines: []DISLine{{ISIN: h.ISIN, Qty: 1}}}); err != nil {
		t.Errorf("DIS after the freeze is lifted: %v", err)
	}
	fs, err := o.CDSLFreezes(ctx, T, "priya.nair", "")
	if err != nil || len(fs) != 2 || fs[0].ID != "5002" || fs[1].Lift != "Unfrozen" {
		t.Errorf("freezes: %+v %v", fs, err)
	}
}

func TestFreezeRowReading(t *testing.T) {
	cases := []struct {
		status, typ, ucr, exp, act string
		want                       string
	}{
		{"A", "S", "", "", "", "Active"},
		{"F", "S", "", "", "", "Failed"},
		{"C", "S", "", "", "", "Lifted"},
		{"R", "S", "", "", "", "Lifted"},
		{"A", "U", "", "", "", "Lifted"},
		{"A", "S", "15102026:100000", "", "", "Lifted"},
		{"A", "S", "", "01102026:000000", "", "Expired"},
		{"A", "S", "", "", "20102026:000000", "Pending"},
	}
	for _, x := range cases {
		lib, _ := XFiles()
		s, _ := lib.Get("cdsl-freeze-master")
		line := strings.Join([]string{"1", "1208160000000001", "A", "", "", "F", "0", "0", x.status, "1", "", "3", "", "1", x.act, x.typ, x.exp, "", "", "", "", x.ucr, "", "", "", "", "", "", "", ""}, "~")
		tb, _ := s.Read("18DPE6U.001", []byte(line+"\r\n"))
		f, ok := freezeFromRow(tb.Rows[0], "2026-10-16")
		if !ok || f.State != x.want {
			t.Errorf("%+v: %s", x, f.State)
		}
	}
	if cdslDate("09092026:143005") != "2026-09-09T14:30:05" || cdslDate("09092026") != "2026-09-09" || cdslDate("09092026:000000") != "2026-09-09" {
		t.Error("dates")
	}
	if r := cdslFreezeReason("11"); !strings.Contains(r, "CBI") {
		t.Errorf("reason %q", r)
	}
}
