package ops

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/docs"
)

// All data here is made up.

// hsFirm sets the firm's name, PAN and account tags for the holding
// statement tests.
func hsFirm(t *testing.T, o *Ops, ctx context.Context, tags map[string]string) {
	t.Helper()
	set, _ := o.Settings(ctx, T)
	set.Firm, set.FirmPAN = "Test Broking", "AAACB1234C"
	if err := o.SaveSettings(ctx, T, "admin", set); err != nil {
		t.Fatal(err)
	}
	if _, err := o.SaveFirmDemats(ctx, T, "admin", tags, "FIRM01"); err != nil {
		t.Fatal(err)
	}
}

// Account types in the API specification's spelling (CLNTBENEMTF; the master
// circular's CLNTBENE-MTF and the weekly format's TM COLL read as the same type);
// security types from CDSL's ISIN master; commodities with name and unit;
// invoked pledges in the margin pledge account reported, a client's own
// account never; a Saturday made from Friday's balances.
func TestHoldingStatementTypes(t *testing.T) {
	o, ctx := newOps(t)
	const own, mp, mtf, coll, cl = "1208160000012345", "1208160000077777", "1208160000088888", "1208160000066666", "1208160000055555"
	if _, err := o.SaveFirmDemats(ctx, T, "admin", map[string]string{mtf: "clntbene-mtf", coll: "tm coll"}, "FIRM01"); err != nil {
		t.Fatal(err)
	}
	if set, _ := o.Settings(ctx, T); set.FirmDemats[mtf] != "CLNTBENEMTF" || set.FirmDemats[coll] != "TMCOLL" {
		t.Fatalf("aliases %v", set.FirmDemats)
	}
	if !contains(HSAccountTypes, "CLNTBENEMTF") || contains(HSAccountTypes, "CLNTBENE-MTF") || len(HSAccountTypeNotes) != len(HSAccountTypes) {
		t.Fatalf("types %v", HSAccountTypes)
	}
	// The previous system's file in the master circular's spelling.
	old := `{"holdingData":[{"dmat":"` + mtf + `","accountType":"CLNTBENE-MTF"}]}`
	if _, found, err := o.FirmDematsFromStatement(ctx, T, "admin", "old.json", []byte(old)); err != nil || found[MaskAccount(mtf)] != "CLNTBENEMTF" {
		t.Fatalf("old file %v %v", found, err)
	}
	hsFirm(t, o, ctx, map[string]string{own: "OWN", mp: "MRGN PLEDGE"})
	for isin, r := range map[string]SecRef{
		"INE000A01011": {Type: "EQTY"}, "INE000P01011": {Type: "PRSH"}, "INE000D07011": {Type: "DEBTR"}, "INE000G08011": {Type: "GSEC"},
		"INE000W01011": {Type: "WRRNT"}, "INE000E01011": {Type: "EGR", Name: "Electronic Gold Receipt 995"}, "INE000F01011": {Type: "COMDT", Name: "Cotton 29mm Bales"},
		"INE000R01011": {Type: "REIT"}, "INE000Z01011": {Type: "COMDT", Name: "Mystery Commodity"},
	} {
		r.ISIN = isin
		siPut(t, o, ctx, cSecRef, isin, r)
	}
	line := func(isin, tot, pl string) FirmDematLine { return FirmDematLine{ISIN: isin, Total: tot, Pledged: pl} }
	siPut(t, o, ctx, cFirmDematSnap, "cdsl|"+D, FirmDematSnap{Date: D, Accounts: []FirmDematSeen{
		{BOID: own, ISINs: 10, Lines: []FirmDematLine{line("INE000A01011", "100", "40"), line("INE000P01011", "5", "0"), line("INE000D07011", "2", "0"),
			line("INE000G08011", "1", "0"), line("INE000W01011", "3", "0"), line("INE000E01011", "12.5", "0"), line("INE000F01011", "4", "0"),
			line("INE000R01011", "7", "0"), line("INF000M01011", "10.125", "0"), line("IN0020200011", "100", "0")}},
		// Securities from an invoked margin pledge, held in the firm's
		// margin pledge account: reported.
		{BOID: mp, ISINs: 1, Lines: []FirmDematLine{line("INE000A01011", "8", "0")}},
	}})
	set, _ := o.Settings(ctx, T)
	items, probs, notes := o.firmHoldingStatement(ctx, T, set, D)
	if len(probs) != 0 || len(items) != 11 {
		t.Fatalf("statement %d %v", len(items), probs)
	}
	want := map[string][3]string{
		"INE000A01011": {"EQ", "NA", "NA"}, "INE000P01011": {"PREF", "NA", "NA"}, "INE000D07011": {"DEBT", "NA", "NA"}, "INE000G08011": {"BOND", "NA", "NA"},
		"INE000W01011": {"WT", "NA", "NA"}, "INE000E01011": {"COMM", "Electronic Gold Receipt 995", "GMS"}, "INE000F01011": {"COMM", "Cotton 29mm Bales", "BALES"},
		"INE000R01011": {"EQ", "NA", "NA"}, "INF000M01011": {"MF", "NA", "NA"}, "IN0020200011": {"BOND", "NA", "NA"},
	}
	invoked := false
	for _, it := range items {
		if it.Dmat == mp {
			invoked = it.AccountType == "MRGN PLEDGE" && it.TotalQty == "8.000"
			continue
		}
		if w := want[it.ISIN]; it.SecurityType != w[0] || it.NameOfCommodity != w[1] || it.UnitType != w[2] {
			t.Fatalf("%s: %+v", it.ISIN, it)
		}
	}
	if !invoked {
		t.Fatal("securities in the margin pledge account (invoked) not reported")
	}
	all := strings.Join(notes, " | ")
	if len(notes) != 2 || !strings.Contains(all, "INF000M01011 is reported as MF (not in CDSL's ISIN master)") || !strings.Contains(all, "IN0020200011 is reported as BOND") {
		t.Fatalf("notes %v", notes)
	}
	// A commodity without its unit in the name goes as OTHERS, noted.
	siPut(t, o, ctx, cFirmDematSnap, "cdsl|2026-10-15", FirmDematSnap{Date: "2026-10-15", Accounts: []FirmDematSeen{{BOID: own, ISINs: 1, Lines: []FirmDematLine{line("INE000Z01011", "1", "0")}}}})
	if items, _, notes := o.firmHoldingStatement(ctx, T, set, "2026-10-15"); len(items) != 1 || items[0].UnitType != "OTHERS" || len(notes) != 1 {
		t.Fatalf("unit %+v %v", items, notes)
	}
	// The file is made; the security types to check open a work item.
	if _, _, err := o.ExchangeFile(ctx, T, "priya.nair", "holding-statement-api", D, XFileOptions{}); err != nil {
		t.Fatal(err)
	}
	if w, _, err := docs.Get[WorkItem](ctx, o.Docs, T, cWorkItem, "W-holding-api-sectype-"+D); err != nil || w.Status != "Open" || !strings.Contains(w.Detail, "INF000M01011") {
		t.Fatalf("work %+v %v", w, err)
	}
	// Saturday and Sunday: no file of their own, Friday's balances.
	for _, d := range []string{"2026-10-17", "2026-10-18"} {
		if its, ps, _ := o.firmHoldingStatement(ctx, T, set, d); len(ps) != 0 || len(its) != 11 {
			t.Fatalf("%s from Friday: %d %v", d, len(its), ps)
		}
	}
	name, _, err := o.ExchangeFile(ctx, T, "priya.nair", "holding-statement-api", "2026-10-17", XFileOptions{})
	if err != nil || !strings.Contains(name, "_HS_17102026") {
		t.Fatalf("saturday %s %v", name, err)
	}
	// A trading day needs its own file.
	if _, ps, _ := o.firmHoldingStatement(ctx, T, set, "2026-10-19"); len(ps) != 1 || !strings.Contains(ps[0], "19-Oct-2026 is not loaded") {
		t.Fatalf("monday %v", ps)
	}
	// A client's own account tagged as a collateral account would report
	// the client's margin pledge: blocked. OWN on it (the firm's own
	// account opened as a client) is fine.
	siPut(t, o, ctx, cFirmDematSnap, "cdsl|"+D, FirmDematSnap{Date: D, Accounts: []FirmDematSeen{
		{BOID: own, ISINs: 1, Lines: []FirmDematLine{line("INE000A01011", "100", "40")}},
		{BOID: cl, Client: "CL0001", ISINs: 1, Lines: []FirmDematLine{line("INE000A01011", "50", "50")}}}})
	hsFirm(t, o, ctx, map[string]string{own: "OWN", cl: "CLNT COLL"})
	set, _ = o.Settings(ctx, T)
	if its, ps, _ := o.firmHoldingStatement(ctx, T, set, D); len(ps) != 1 || !strings.Contains(ps[0], "is client CL0001's own account") || len(its) != 1 {
		t.Fatalf("client account %v", ps)
	}
	hsFirm(t, o, ctx, map[string]string{own: "OWN", cl: "OWN"})
	set, _ = o.Settings(ctx, T)
	if its, ps, _ := o.firmHoldingStatement(ctx, T, set, D); len(ps) != 0 || len(its) != 2 {
		t.Fatalf("own on a client record %v", ps)
	}
}

// hsFake starts the fake NSE inspection API and the connection to it.
func hsFake(t *testing.T, o *Ops, ctx context.Context, start string) *fakeNSEHolding {
	t.Helper()
	key := bytes.Repeat([]byte{7}, 32)
	f := &fakeNSEHolding{key: key}
	srv := httptest.NewTLSServer(f.handler(t))
	t.Cleanup(srv.Close)
	old := nseHoldingHTTP
	nseHoldingHTTP = srv.Client()
	t.Cleanup(func() { nseHoldingHTTP = old })
	if _, err := o.SaveConnection(ctx, T, "admin", connNSEHolding, map[string]string{"member": "90311", "send": "On", "start": start, "url": srv.URL + "/inspectionapi"},
		map[string]string{"password": "Hs@Pass12345", "secret_key": base64.StdEncoding.EncodeToString(key)}); err != nil {
		t.Fatal(err)
	}
	return f
}

// Nil days (NSE/INSP/60046): nothing goes by API; the one-time NIL
// Holding declaration is asked for and recorded, covers the next nil days,
// and the next day with holdings waits for the bulk nil report.
func TestNSEHoldingNilDays(t *testing.T) {
	o, ctx := newOps(t)
	o.Now = func() time.Time { return time.Date(2026, 10, 22, 9, 0, 0, 0, ist) }
	const own = "1208160000012345"
	hsFirm(t, o, ctx, map[string]string{own: "OWN"})
	f := hsFake(t, o, ctx, "")
	held := FirmDematSnap{Accounts: []FirmDematSeen{{BOID: own, ISINs: 1, Lines: []FirmDematLine{{ISIN: "INE000A01011", Total: "100", Pledged: "0"}}}}}
	empty := FirmDematSnap{Accounts: []FirmDematSeen{{BOID: own, Lines: []FirmDematLine{}}}}
	for d, s := range map[string]FirmDematSnap{"2026-10-16": held, "2026-10-19": empty, "2026-10-20": empty, "2026-10-21": held} {
		s.Date = d
		siPut(t, o, ctx, cFirmDematSnap, "cdsl|"+d, s)
	}
	if sub, err := o.NSEHoldingSubmit(ctx, T, "priya.nair", "2026-10-16"); err != nil || sub.Status != "Accepted" {
		t.Fatalf("%+v %v", sub, err)
	}
	n := len(f.uploads)
	sub, err := o.NSEHoldingSubmit(ctx, T, "priya.nair", "2026-10-19")
	if err != nil || sub.Status != hsNil || len(f.uploads) != n {
		t.Fatalf("nil day %+v %v", sub, err)
	}
	w, _, err := docs.Get[WorkItem](ctx, o.Docs, T, cWorkItem, "W-holding-api-nil-2026-10-19")
	if err != nil || w.Status != "Open" || w.Role != RoleCompliance || !strings.Contains(w.Detail, "NIL Holding Declaration") {
		t.Fatalf("work %+v %v", w, err)
	}
	if v, _ := o.NSEHolding(ctx, T, "priya.nair", "2026-10-19"); len(v.NilUndeclared) != 1 || len(v.Submissions) != 1 || v.Submissions[0].Status != hsNil {
		t.Fatalf("view %+v", v)
	}
	// Recording the declaration: the people who submit, with the portal's
	// acknowledgement, for a day that really held nothing.
	if _, err := o.NSEHoldingNil(ctx, T, "anita.rao", "declaration", []string{"2026-10-19"}, "NIL1"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("risk recorded: %v", err)
	}
	if _, err := o.NSEHoldingNil(ctx, T, "vikram.shah", "declaration", []string{"2026-10-19"}, " "); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no ack: %v", err)
	}
	if _, err := o.NSEHoldingNil(ctx, T, "vikram.shah", "declaration", []string{"2026-10-16"}, "NIL1"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("declared a day with holdings: %v", err)
	}
	if _, err := o.NSEHoldingNil(ctx, T, "vikram.shah", "declaration", []string{"2026-10-19", "2026-10-20"}, "NIL1"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("declaration for two days: %v", err)
	}
	if rs, err := o.NSEHoldingNil(ctx, T, "vikram.shah", "declaration", []string{"2026-10-19"}, "NIL1"); err != nil || len(rs) != 1 || rs[0].Status != hsNilDecl {
		t.Fatalf("declared %+v %v", rs, err)
	}
	if w, _, _ := docs.Get[WorkItem](ctx, o.Docs, T, cWorkItem, "W-holding-api-nil-2026-10-19"); w.Status != "Done" {
		t.Fatalf("work not closed %+v", w)
	}
	// The next nil day is covered by it.
	if sub, _ := o.NSEHoldingSubmit(ctx, T, "priya.nair", "2026-10-20"); sub.Status != hsNilDecl || len(f.uploads) != n {
		t.Fatalf("covered %+v", sub)
	}
	// Holdings are back: not sent before the bulk nil report.
	sub, _ = o.NSEHoldingSubmit(ctx, T, "priya.nair", "2026-10-21")
	if sub.Status != "Not sent" || !strings.Contains(sub.Reason, "Bulk Nil Holding Reporting") || !strings.Contains(sub.Reason, "19-Oct-2026, 20-Oct-2026") || len(f.uploads) != n {
		t.Fatalf("before bulk %+v", sub)
	}
	if v, _ := o.NSEHolding(ctx, T, "priya.nair", ""); len(v.NilForBulk) != 2 || len(v.NilUndeclared) != 0 {
		t.Fatalf("view %+v %+v", v.NilForBulk, v.NilUndeclared)
	}
	if _, err := o.NSEHoldingNil(ctx, T, "priya.nair", "bulk", []string{"2026-10-20", "2026-10-19"}, "BULK7"); err != nil {
		t.Fatal(err)
	}
	sub, _ = o.NSEHoldingSubmit(ctx, T, "priya.nair", "2026-10-21")
	if sub.Status != "Accepted" || len(f.uploads) != n+1 {
		t.Fatalf("after bulk %+v", sub)
	}
	if v, _ := o.NSEHolding(ctx, T, "priya.nair", ""); len(v.NilForBulk) != 0 {
		t.Fatalf("nil days left %+v", v.NilForBulk)
	}
}

// Sunday special sessions are data days; Saturday and the Sunday session
// are made from Friday's balances; a past day is made again from its own
// balances after later files are loaded.
func TestNSEHoldingSpecialSession(t *testing.T) {
	o, ctx := newOps(t)
	now := time.Date(2026, 11, 9, 9, 0, 0, 0, ist)
	o.Now = func() time.Time { return now }
	const own = "1208160000012345"
	hsFirm(t, o, ctx, map[string]string{own: "OWN"})
	if _, err := o.SetSpecialSessions(ctx, T, "priya.nair", []string{"2026-11-08"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker set sessions: %v", err)
	}
	if _, err := o.SetSpecialSessions(ctx, T, "vikram.shah", []string{"8 Nov"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad date: %v", err)
	}
	v, err := o.SetSpecialSessions(ctx, T, "vikram.shah", []string{"2026-11-08", "2026-11-08"})
	if err != nil || len(v.SpecialSessions) != 1 {
		t.Fatalf("sessions %+v %v", v, err)
	}
	set, _ := o.Settings(ctx, T)
	if !o.isHoldingDay(ctx, T, set, "2026-11-08") || o.isHoldingDay(ctx, T, set, "2026-11-01") || !o.isHoldingDay(ctx, T, set, "2026-11-07") {
		t.Fatal("holding days")
	}
	for _, d := range []string{"2026-11-05", "2026-11-06"} {
		siPut(t, o, ctx, cFirmDematSnap, "cdsl|"+d, FirmDematSnap{Date: d, Accounts: []FirmDematSeen{{BOID: own, ISINs: 1, Lines: []FirmDematLine{{ISIN: "INE000A01011", Total: "100", Pledged: "0"}}}}})
	}
	f := hsFake(t, o, ctx, "2026-11-06")
	o.noteDailyFile(ctx, T, "priya.nair", "cdsl-holding", "SOH_06112026.txt", "2026-11-06")
	o.nseHoldingTick(ctx, T, now)
	if strings.Join(f.names, " ") != "AAACB1234C_HS_06112026_01.gz AAACB1234C_HS_07112026_01.gz AAACB1234C_HS_08112026_01.gz" {
		t.Fatalf("sent %v", f.names)
	}
	// A past day is made again from its own balances, not the latest.
	siPut(t, o, ctx, cFirmDematSnap, "cdsl|2026-11-05", FirmDematSnap{Date: "2026-11-05", Accounts: []FirmDematSeen{{BOID: own, ISINs: 1, Lines: []FirmDematLine{{ISIN: "INE000A01011", Total: "7", Pledged: "0"}}}}})
	if sub, err := o.NSEHoldingSubmit(ctx, T, "priya.nair", "2026-11-05"); err != nil || sub.Status != "Accepted" {
		t.Fatalf("%+v %v", sub, err)
	}
	last := f.uploads[len(f.uploads)-1]["holdingData"].([]any)[0].(map[string]any)
	if last["totalQty"] != "7.000" {
		t.Fatalf("past day %v", last)
	}
}

// An older CDSL statement of holding, loaded after a newer one, adds that
// day's balances of the firm's accounts and leaves clients' holdings alone.
func TestOlderStatementOfHolding(t *testing.T) {
	o, ctx := setup(t)
	cs, _ := o.Clients(ctx, T)
	var c Client
	for _, x := range cs {
		if x.Demat.ID != "" && !x.DPOnly {
			c = x
			break
		}
	}
	h := xheader(t, "cdsl-holding")
	const pool = "1208169999000001"
	soh := func(name, day, clientQty, poolQty string) XFileResult {
		t.Helper()
		lines := []string{strings.Join(h, ",")}
		for _, l := range [][2]string{{c.Demat.ID, clientQty}, {pool, poolQty}} {
			lines = append(lines, xline(h, map[string]string{"Src": "CDSL", "BnfclOwnrId": l[0], "BnfcryAcctCtgy": "1", "ISIN": "INE000X01010", "CurBal": l[1], "PldgdBal": "0.000", "StmtDt": day + "T18:00:00"}))
		}
		res, err := o.LoadExchangeFile(ctx, T, "priya.nair", name, []byte(strings.Join(lines, "\r\n")+"\r\n"), true)
		if err != nil {
			t.Fatal(err)
		}
		return res[0]
	}
	soh("SOH_EXP_043200_123_F_202610161831_7_C_0_0_P0.csv", D, "25.000", "40.000")
	r := soh("SOH_EXP_043200_123_F_202610151831_7_C_0_0_P0.csv", "2026-10-15", "99.000", "30.000")
	if !strings.Contains(r.Loaded, "Kept for the holding statement of 15-Oct-2026 only") {
		t.Fatalf("older file %+v", r)
	}
	hs, _ := o.Holdings(ctx, T, c.UCC)
	for _, x := range hs {
		if x.ISIN == "INE000X01010" && x.Qty.String() != "25" {
			t.Fatalf("client holdings replaced by the older file: %s", x.Qty.String())
		}
	}
	if o.latestFirmSnapDate(ctx, T) != D {
		t.Fatal("the latest statement of holding moved back")
	}
	set, _ := o.Settings(ctx, T)
	set.FirmDemats = map[string]string{pool: "POOL"}
	set.FirmUCC = "FIRM01"
	if err := o.SaveSettings(ctx, T, "admin", set); err != nil {
		t.Fatal(err)
	}
	set, _ = o.Settings(ctx, T)
	if its, ps, _ := o.firmHoldingStatement(ctx, T, set, "2026-10-15"); len(ps) != 0 || len(its) != 1 || its[0].TotalQty != "30.000" {
		t.Fatalf("older day %+v %v", its, ps)
	}
}
