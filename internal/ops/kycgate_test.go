package ops

import (
	"context"
	"errors"
	"strings"
	"testing"

	"ledgerline/internal/docs"
	"ledgerline/internal/xfile"
)

// kgClient puts an active client with made-up details.
func kgClient(t *testing.T, o *Ops, ctx context.Context, ucc, pan, kra, kycDate string) Client {
	t.Helper()
	c := Client{UCC: ucc, Name: "Test " + ucc, Type: "Individual", PAN: pan, Status: "Active", Segments: []string{"CM", "FO"}, Mobile: "9800000123", Email: strings.ToLower(ucc) + "@example.test",
		KRAStatus: kra, KYCDate: kycDate, Income: "5-10L", IncomeProof: true}
	if err := docs.Upsert(ctx, o.Docs, T, cClient, ucc, c, "t"); err != nil {
		t.Fatal(err)
	}
	return c
}

func kgBlock(t *testing.T, o *Ops, ctx context.Context, ucc string) finspotBlockItem {
	t.Helper()
	finspotMu.Lock()
	items, _, err := o.finspotBlockPlan(ctx, T, D)
	finspotMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if it.UCC == ucc {
			return it
		}
	}
	return finspotBlockItem{}
}

// The gate is on by default: no KRA status, or one not verified, blocks;
// a client since before April 2024 may still close positions; the gate
// switched off blocks nobody for KYC.
func TestKYCGateBlocks(t *testing.T) {
	o, ctx := setup(t)
	set, _ := o.Settings(ctx, T)
	if set.KYCBlockOff {
		t.Fatal("the gate is off by default")
	}
	cases := []struct {
		ucc, pan, kra, kyc string
		why                string
		all                bool
	}{
		{"KG01", "ABCPK0001A", "", "2025-02-01", "no kra status", true},
		{"KG02", "ABCPK0002A", "On hold", "2023-06-01", "may close positions", false},
		{"KG03", "ABCPK0003A", "KYC Validated", "2025-02-01", "", false},
		{"KG04", "ABCPK0004A", "Under process", "", "under process", true},
		{"KG05", "ABCPK0005A", "Registered", "2025-02-01", "", false},
	}
	for _, c := range cases {
		kgClient(t, o, ctx, c.ucc, c.pan, c.kra, c.kyc)
	}
	for _, c := range cases {
		cl, _ := o.Client(ctx, T, c.ucc)
		k := set.kycGate(cl, D)
		if c.why == "" && k.Why != "" || c.why != "" && (!strings.Contains(strings.ToLower(k.Why), c.why) || k.All != c.all) {
			t.Errorf("%s: %+v", c.ucc, k)
		}
		it := kgBlock(t, o, ctx, c.ucc)
		if (c.why != "") != it.Block || it.All != c.all {
			t.Errorf("%s in the block plan: %+v", c.ucc, it)
		}
	}
	// The block file: ALL for no trading, NON-SQROFF (CNC buys only) for exit only.
	name, b, err := o.FinspotBlockFile(ctx, T, "anita.rao", D)
	if err != nil {
		t.Fatal(err)
	}
	tb := readFinspot(t, "finspot-rms-block", name, b)
	got := map[string][]xfile.Row{}
	for _, r := range tb.Rows {
		got[r.Values["Client"]] = append(got[r.Values["Client"]], r)
	}
	for _, r := range got["KG01"] {
		if r.Values["BlockType"] != "ALL" || r.Values["Block"] != "Y" || r.Values["Side"] != "" {
			t.Fatalf("KG01 %+v", r.Values)
		}
	}
	if len(got["KG02"]) != 3 || got["KG02"][2].Values["BlockType"] != "NON-SQROFF" || got["KG02"][2].Values["Side"] != "B" {
		t.Fatalf("KG02 %+v", got["KG02"])
	}
	// The limits: zero for both kinds.
	ls, err := o.bowLimits(ctx, T, D)
	if err != nil || ls["KG02"].Securities.Sign() != 0 || !strings.Contains(ls["KG02"].Why, "KRA") || ls["KG03"].Why != "" {
		t.Fatalf("limits %+v %+v %v", ls["KG02"], ls["KG03"], err)
	}

	// KG01's status is loaded as validated: the block is lifted.
	kra := "PAN,Status,Remarks\nABCPK0001A,Validated,\n"
	if _, err := o.LoadFile(ctx, T, "vikram.shah", "KRA-STATUS", "", "kra.csv", []byte(kra)); err != nil {
		t.Fatal(err)
	}
	if it := kgBlock(t, o, ctx, "KG01"); it.Block || !it.Changed || !it.WasAll {
		t.Fatalf("KG01 not lifted: %+v", it)
	}

	// The gate off (the firm's practice, recorded): nobody is held for KYC.
	fpAnswer(t, o, ctx, "kyc-blocks", "off", nil)
	set, _ = o.Settings(ctx, T)
	cl, _ := o.Client(ctx, T, "KG04")
	if k := set.kycGate(cl, D); k.Why != "" || kgBlock(t, o, ctx, "KG04").Block {
		t.Fatalf("held with the gate off: %+v", k)
	}
	siWant(t, siOne(t, o, ctx, "kyc_gate", D), SIFail, "the gate off")
}

// The exchanges' lists: matched by PAN or client code, held until a later
// list leaves the client out or Compliance lifts it after the KRA
// verifies; NPTT and MCX's list block square-off too.
func TestKYCGateLists(t *testing.T) {
	o, ctx := setup(t)
	siClock(o, siAt(D, "11:00"))
	kgClient(t, o, ctx, "KL01", "ABCPL0001A", "Validated", "2022-01-10")
	kgClient(t, o, ctx, "KL02", "ABCPL0002A", "On hold", "2022-01-10")

	// NSE's non-validated list with the cycle's NPTT date still ahead.
	csv := "PAN,Client Code,Client Name,Status\nABCPL0002A,KL02,Test,On hold\nZZZPZ9999Z,ZZ9,Nobody,On hold\n,KL01,Test,On hold\n"
	l, err := o.LoadKYCList(ctx, T, "vikram.shah", KYCListLoadReq{List: KYCListNSENonValidated, Name: "90311_Non-Validated_Clients_by_KRA.csv", Date: D, From: "2026-10-21"}, []byte(csv))
	if err != nil || l.Rows != 3 || l.Matched != 2 || l.NotClients != 1 || l.New != 2 {
		t.Fatalf("list %+v %v", l, err)
	}
	if w := workFor(t, o, wKYCGate, "list:"+l.ID); w.Status != "Open" || w.View != "kycgate" || !strings.Contains(w.Title, "21-Oct-2026") {
		t.Fatalf("work item %+v", w)
	}
	set, _ := o.Settings(ctx, T)
	c1, _ := o.Client(ctx, T, "KL01")
	if k := set.kycGate(c1, D); !strings.Contains(k.Why, "non-validated") || k.All {
		t.Fatalf("KL01 before the NPTT date: %+v", k)
	}
	if k := set.kycGate(c1, "2026-10-21"); !k.All {
		t.Fatalf("KL01 from the NPTT date: %+v", k)
	}
	// Only Compliance lifts, and only once the KRA has verified.
	if _, err := o.LiftKYCHold(ctx, T, "priya.nair", "KL01", KYCListNSENonValidated, "validated"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a maker lifted: %v", err)
	}
	if _, err := o.LiftKYCHold(ctx, T, "vikram.shah", "KL02", KYCListNSENonValidated, "validated on CVL"); !errors.Is(err, ErrState) {
		t.Fatalf("lifted while on hold: %v", err)
	}
	if c, err := o.LiftKYCHold(ctx, T, "vikram.shah", "KL01", KYCListNSENonValidated, "KRA shows validated on 15 Oct"); err != nil || len(c.KYCHolds) != 0 {
		t.Fatalf("lift %+v %v", c.KYCHolds, err)
	}

	// MCX's hold list, dropped on Exchange files: square-off barred at once.
	res, err := o.LoadExchangeFile(ctx, T, "vikram.shah", "MCX_KYC_On_Hold_Oct2026.csv", []byte("PAN,Client Code\nABCPL0002A,KL02\n"), true)
	if err != nil || len(res) != 1 || res[0].Format != KYCListMCXHold || res[0].Added != 1 || !strings.Contains(res[0].Loaded, "KYC trading gate") {
		t.Fatalf("MCX list %+v %v", res, err)
	}
	c2, _ := o.Client(ctx, T, "KL02")
	if k := set.kycGate(c2, D); !k.All || len(c2.KYCHolds) != 2 {
		t.Fatalf("KL02 on MCX's list: %+v %+v", k, c2.KYCHolds)
	}
	v, err := o.KYCGate(ctx, T, "vikram.shah", D)
	if err != nil {
		t.Fatal(err)
	}
	var row KYCGateRow
	for _, r := range v.Blocked {
		if r.UCC == "KL02" {
			row = r
		}
	}
	if !row.MCX || row.Mode != "No trading" || len(v.Lists) != 2 {
		t.Fatalf("view row %+v lists %d", row, len(v.Lists))
	}
	// The next NSE list does not have KL02: that hold is lifted, MCX's stays.
	l2, err := o.LoadKYCList(ctx, T, "vikram.shah", KYCListLoadReq{List: KYCListNSENonValidated, Name: "next.csv", Date: "2026-10-17"}, []byte("PAN\nZZZPZ9999Z\n"))
	if err != nil || l2.Lifted != 1 {
		t.Fatalf("next list %+v %v", l2, err)
	}
	c2, _ = o.Client(ctx, T, "KL02")
	if len(c2.KYCHolds) != 1 || c2.KYCHolds[0].List != KYCListMCXHold {
		t.Fatalf("holds %+v", c2.KYCHolds)
	}
	// A list loaded later for an earlier date changes nothing.
	if _, err := o.LoadKYCList(ctx, T, "vikram.shah", KYCListLoadReq{List: KYCListMCXHold, Name: "old.csv", Date: "2026-09-30"}, []byte("PAN\nZZZPZ9999Z\n")); err != nil {
		t.Fatal(err)
	}
	if c2, _ = o.Client(ctx, T, "KL02"); len(c2.KYCHolds) != 1 {
		t.Fatalf("an older list lifted a hold: %+v", c2.KYCHolds)
	}
	// Unknown list, and a file with no header the layout knows.
	if _, err := o.LoadKYCList(ctx, T, "vikram.shah", KYCListLoadReq{List: "nse-other", Name: "x.csv"}, []byte("PAN\n")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown list: %v", err)
	}
	// After 30 days on a list the self-inspection reports the client.
	siWant(t, siOne(t, o, ctx, "kyc_gate", "2026-11-20"), SIFail, "on MCX's list for over 30 days")
}

// The demat side: accounts with a holder not verified need a reason-27
// freeze, joint holders included; CDSL's list is matched by BO ID.
func TestKYCGateDemat(t *testing.T) {
	o, ctx := setup(t)
	siClock(o, siAt(D, "11:00"))
	kgClient(t, o, ctx, "KD01", "ABCPD0001A", "Validated", "2025-01-01")
	kgClient(t, o, ctx, "KD02", "ABCPD0002A", "Validated", "2025-01-01")
	for _, b := range []BOAccount{
		{UCC: "KD01", BOID: "1208160000000101", Depository: "CDSL", Status: "Active", JointHolders: []JointHolder{{Name: "Joint One", PAN: "ABCPJ0001J"}}},
		{UCC: "KD02", BOID: "1208160000000102", Depository: "CDSL", Status: "Active"},
	} {
		if err := o.putBO(ctx, T, "t", b); err != nil {
			t.Fatal(err)
		}
	}
	// The joint holder's status comes from the KRA status file.
	if _, err := o.LoadFile(ctx, T, "vikram.shah", "KRA-STATUS", "", "kra.csv", []byte("PAN,Status,Remarks\nABCPJ0001J,On hold,Mobile not verified\n")); err != nil {
		t.Fatal(err)
	}
	if b, _ := o.boAccount(ctx, T, "1208160000000101"); b.JointHolders[0].KRAStatus != "On hold" {
		t.Fatalf("joint holder %+v", b.JointHolders)
	}
	cs, _ := o.ClientMap(ctx, T)
	var acc []string
	for _, a := range o.dematKYCAccounts(ctx, T, cs, D) {
		acc = append(acc, a.UCC)
	}
	if !contains(acc, "KD01") || contains(acc, "KD02") {
		t.Fatalf("accounts %v", acc)
	}
	siWant(t, siOne(t, o, ctx, "cdsl_kyc_freeze", D), SIFail, "not frozen")
	n, err := o.RaiseKYCFreezes(ctx, T, "priya.nair")
	if err != nil || n == 0 {
		t.Fatalf("raised %d %v", n, err)
	}
	reqs := o.freezeRequests(ctx, T, cdslReasonKYC)
	r, _, _ := docs.Get[DPRequest](ctx, o.Docs, T, cDPReq, reqs["1208160000000101"])
	if r.Kind != "Freeze" || r.Detail["type"] != "Both" || r.Detail["reason_code"] != "27" || !strings.Contains(r.Detail["reason"], "second holder") {
		t.Fatalf("request %+v", r)
	}
	if again, _ := o.RaiseKYCFreezes(ctx, T, "priya.nair"); again != 0 {
		t.Fatalf("raised again: %d", again)
	}
	// CDSL's DP-wise list, matched by BO ID and the holder's PAN.
	l, err := o.LoadKYCList(ctx, T, "vikram.shah", KYCListLoadReq{List: KYCListCDSLDeficient, Name: "KRA_Deficient_BO.csv", Date: D, From: "2026-10-31"}, []byte("BO ID,PAN\n1208160000000101,ABCPJ0001J\n"))
	if err != nil || l.Matched != 1 {
		t.Fatalf("CDSL list %+v %v", l, err)
	}
	c, _ := o.Client(ctx, T, "KD01")
	if len(c.KYCHolds) != 1 || c.KYCHolds[0].Holder != "Second" || c.KYCHolds[0].FreezeOn != "2026-10-31" {
		t.Fatalf("CDSL hold %+v", c.KYCHolds)
	}
	set, _ := o.Settings(ctx, T)
	if k := set.kycGate(c, D); k.Why != "" {
		t.Fatalf("a demat list blocked trading: %+v", k)
	}
	// CDSL shows the reason-27 freeze: the test passes.
	siPut(t, o, ctx, cCDSLFreeze, "1208160000000101|F1", CDSLFreeze{ID: "F1", BOID: "1208160000000101", UCC: "KD01", For: "Both", ReasonCode: "27", State: "Active", ActiveFrom: "2026-10-15"})
	siWant(t, siOne(t, o, ctx, "cdsl_kyc_freeze", D), SIPass, "frozen under 27")
	// A freeze still under code 21 is reported.
	siPut(t, o, ctx, cCDSLFreeze, "1208160000000102|F2", CDSLFreeze{ID: "F2", BOID: "1208160000000102", UCC: "KD02", For: "Debit", ReasonCode: "021", State: "Active", ActiveFrom: "2025-05-01"})
	if v, _ := o.KYCGate(ctx, T, "vikram.shah", D); len(v.Demat.Code21) != 1 || v.Demat.Counts["Frozen (27)"] != 1 {
		t.Fatalf("demat view %+v", v.Demat.Counts)
	}
	siWant(t, siOne(t, o, ctx, "cdsl_mnrl_freeze", D), SIFail, "code 21")
}

// A revoked mobile on a CDSL account: the 30-day notice, then the debit
// freeze under reason 31.
func TestKYCGateMNRLNotice(t *testing.T) {
	o, ctx := setup(t)
	clock := siClock(o, siAt(D, "11:00"))
	kgClient(t, o, ctx, "KM01", "ABCPM0001A", "Validated", "2025-01-01")
	if err := o.putBO(ctx, T, "t", BOAccount{UCC: "KM01", BOID: "1208160000000201", Depository: "CDSL", Status: "Active"}); err != nil {
		t.Fatal(err)
	}
	m := MNRLMatch{ID: "MN-900001", UCC: "KM01", Name: "Test KM01", Holder: "First", Mobile: "9800000123", OnRecord: true, Source: "DoT", Status: MNRLNew, Opened: D, Due: D, History: []MNRLEvent{}}
	siPut(t, o, ctx, cMNRLMatch, m.ID, m)
	if _, err := o.RaiseMNRLFreeze(ctx, T, "priya.nair", m.ID); !errors.Is(err, ErrState) {
		t.Fatalf("freeze before a notice: %v", err)
	}
	if _, err := o.RecordMNRLNotice(ctx, T, "priya.nair", m.ID, D, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a notice without how it was sent: %v", err)
	}
	x, err := o.RecordMNRLNotice(ctx, T, "priya.nair", m.ID, D, "registered e-mail, ref N-1")
	if err != nil || x.NoticeExpiry != "2026-11-15" {
		t.Fatalf("notice %+v %v", x, err)
	}
	siWant(t, siOne(t, o, ctx, "cdsl_mnrl_freeze", D), SIPass, "notice running")
	// The notice runs out: the daily run raises a work item, the test fails
	// until the freeze is requested.
	clock(siAt("2026-11-17", "10:00"))
	o.kycGateDaily(ctx, T, "2026-11-17")
	if w := workFor(t, o, wKYCGate, "mnrl31:"+m.ID); w.Status != "Open" {
		t.Fatalf("no work item: %+v", w)
	}
	siWant(t, siOne(t, o, ctx, "cdsl_mnrl_freeze", "2026-11-17"), SIFail, "notice expired")
	r, err := o.RaiseMNRLFreeze(ctx, T, "priya.nair", m.ID)
	if err != nil || r.Detail["reason_code"] != "31" || r.Detail["type"] != "Debit" || r.BOID != "1208160000000201" {
		t.Fatalf("freeze %+v %v", r, err)
	}
	if w := workFor(t, o, wKYCGate, "mnrl31:"+m.ID); w.Status == "Open" {
		t.Fatal("work item still open")
	}
	siWant(t, siOne(t, o, ctx, "cdsl_mnrl_freeze", "2026-11-17"), SIPass, "freeze requested")
}

// The KRA upload clock: three working days from the KYC or its change.
func TestKRAUploadClock(t *testing.T) {
	o, ctx := setup(t)
	siClock(o, siAt(D, "11:00"))
	kgClient(t, o, ctx, "KU01", "ABCPU0001A", "", "2026-10-09")
	set, _ := o.Settings(ctx, T)
	var it kraUploadItem
	for _, x := range o.kraUploads(ctx, T, set, "2026-10-01", D) {
		if x.UCC == "KU01" {
			it = x
		}
	}
	if it.Due != addWorkDays(set, "2026-10-09", 3) || !it.Late || it.Uploaded != "" {
		t.Fatalf("clock %+v", it)
	}
	siWant(t, siOne(t, o, ctx, "kra_upload_3days", D), SIFail, "not uploaded")
	o.kycGateDaily(ctx, T, D)
	if w := workFor(t, o, wKYCGate, "kra:KU01:New:2026-10-09"); w.Status != "Open" || w.UCC != "KU01" {
		t.Fatalf("work item %+v", w)
	}
	f, err := o.KRAUploadBatch(ctx, T, "vikram.shah", "2026-10-01")
	if err != nil || f.Batch.ID == "" {
		t.Fatalf("batch %+v %v", f.Batch, err)
	}
	if _, err := o.MarkKRABatchUploaded(ctx, T, "vikram.shah", f.Batch.ID, D, "ACK-1"); err != nil {
		t.Fatal(err)
	}
	for _, x := range o.kraUploads(ctx, T, set, "2026-10-01", D) {
		if x.UCC == "KU01" && (x.Uploaded != D || !x.Late) {
			t.Fatalf("after upload %+v", x)
		}
	}
	o.kycGateDaily(ctx, T, D)
	if w := workFor(t, o, wKYCGate, "kra:KU01:New:2026-10-09"); w.Status == "Open" {
		t.Fatal("work item not closed by the upload")
	}
}

// Activation waits for the CDD: details from the previous system, a KYC
// the KRA rejected, and a PROI client's checks from 3 Dec 2026.
func TestActivationAndPROI(t *testing.T) {
	o, ctx := setup(t)
	siClock(o, siAt("2026-12-04", "11:00"))
	c := Client{Name: "Riya Menon", Type: "NRI", PAN: "ABCPR1234Q", DOB: "1990-04-12", Mobile: "+447700900123", Email: "riya@example.test", City: "London", Segments: []string{"CM"}, Income: "10-25L",
		Bank: BankAccount{Bank: "Sample Bank", IFSC: "SBIN0001234", Account: "123456789012"}, RiskDisclosure: true, Nominee: "Opted out", FATCA: IndiaOnlyFATCA("Kochi", "")}
	n, err := o.CreateApplication(ctx, T, "priya.nair", c)
	if err != nil {
		t.Fatalf("an international mobile for an NRI: %v", err)
	}
	if bad := (Client{Type: "Individual", Mobile: "+447700900123"}).Validate(); bad == nil || !strings.Contains(bad.Error(), "10-digit") {
		t.Fatal("an international mobile for a resident")
	}
	for n.Status != "UCC upload" {
		if n, err = o.AdvanceStage(ctx, T, "priya.nair", n.UCC); err != nil {
			t.Fatal(err)
		}
	}
	_, err = o.AdvanceStage(ctx, T, "farhan.khan", n.UCC)
	if !errors.Is(err, ErrState) || !strings.Contains(err.Error(), "PROI: no PROI onboarding record") {
		t.Fatalf("activated without the PROI checks: %v", err)
	}
	siWant(t, siOne(t, o, ctx, "proi_kyc", "2026-12-04"), SIFail, "PROI record missing")
	p := PROIKYC{Category: "NRI", Country: "GB", KRALookupOn: "2026-12-03", CKYCLookupOn: "2026-12-03", CKYCGiven: "None", DocsOn: "2026-12-03", PANVerifiedOn: "2026-12-03",
		PassportNo: "Z1234567", PassportExpiry: "2031-01-01", OverseasAddress: "1 Test Street, London", OverseasOVD: "UK driving licence", EmailVerified: true,
		IPV: "Video (VIPV)", IPVOfficial: "E102 Anil", IPVAt: "2026-12-03T15:04", VIPV: &PROIVIPV{Consent: true, RandomActions: true, GeoTag: "51.50,-0.12", VPNBlocked: true, IPCountry: "GB", FATFCompliant: true,
			Liveness: true, FaceMatch: true, Encrypted: true, OfficialPresent: false, AuditedBy: "Auditor", AuditedOn: "2026-12-03",
			OfficialTrainedOn: "2026-06-01", RecordingRef: "VIPV/2026/12/0001.mp4", RecordingSHA256: strings.Repeat("ab", 32), Questions: []string{"Show your passport's last page"}, OVDShown: "Passport"}, Translations: "Not needed",
		Extra: "No record: full KYC collected", PassportCopy: "PP-scan-0001", PassportCheck: "Verified", Form: "CERSAI template (e-signed scan)", PoI: "Passport", PoA: "UK driving licence", AddressCheck: "Against the OVD", OSV: "Originals seen and verified"}
	if _, err := o.SetPROI(ctx, T, "priya.nair", n.UCC, PROIKYC{Category: "Martian"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad category: %v", err)
	}
	if _, err := o.SetPROI(ctx, T, "priya.nair", n.UCC, p); err != nil {
		t.Fatal(err)
	}
	if _, err = o.AdvanceStage(ctx, T, "farhan.khan", n.UCC); err == nil || !strings.Contains(err.Error(), "authorised official present") {
		t.Fatalf("a VIPV without the official: %v", err)
	}
	p.VIPV.OfficialPresent = true
	saved, err := o.SetPROI(ctx, T, "priya.nair", n.UCC, p)
	if err != nil || saved.Public().PROI.PassportNo != "****4567" {
		t.Fatalf("masked %+v %v", saved.Public().PROI, err)
	}
	// A masked number sent back keeps the one on record.
	p.PassportNo = "****4567"
	if c, _ := o.SetPROI(ctx, T, "priya.nair", n.UCC, p); c.PROI.PassportNo != "Z1234567" {
		t.Fatalf("passport %q", c.PROI.PassportNo)
	}
	if n, err = o.AdvanceStage(ctx, T, "farhan.khan", n.UCC); err != nil || n.Status != "Active" {
		t.Fatalf("activation %v", err)
	}
	// The mobile is not verified: an SMS goes to the e-mail.
	m, err := o.deliver(ctx, T, "t", Template{ID: "T1", Event: "test", Channel: ChSMS, Body: "Hello", DLT: "1", Active: true}, n, map[string]string{})
	if err != nil || m.Channel != ChEmail || m.To != "riya@example.test" || !strings.Contains(m.Note, "not verified") {
		t.Fatalf("message %+v %v", m, err)
	}
	// NRO is NSE category 37.
	if _, err := o.SetNRI(ctx, T, "farhan.khan", NRIDetails{UCC: n.UCC, Account: "NRO", Bank: "Sample Bank", PISAccount: "111", Permission: "P1", PermDate: "2026-12-01", Country: "GB"}); err != nil {
		t.Fatal(err)
	}
	n, _ = o.Client(ctx, T, n.UCC)
	if cat := nseUCICategory(n, UCCDetails{}); cat != "37" {
		t.Fatalf("NRO category %q", cat)
	}
	if cat := nseUCICategory(Client{Type: "NRI", NRIAccount: "NRE"}, UCCDetails{}); cat != "11" {
		t.Fatalf("NRE category %q", cat)
	}

	// A KYC the KRA rejected stops activation.
	r, err := o.CreateApplication(ctx, T, "priya.nair", Client{Name: "Kabir Das", Type: "Individual", PAN: "ABCPD4321K", Mobile: "9800000999", Email: "kabir@example.test", City: "Pune", Segments: []string{"CM"},
		Income: "5-10L", Bank: BankAccount{Bank: "Sample Bank", IFSC: "SBIN0001234", Account: "123456789013"}, RiskDisclosure: true, FATCA: IndiaOnlyFATCA("Pune", "")})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = docs.Update(ctx, o.Docs, T, cClient, r.UCC, "t", func(x *Client) error { x.KRAStatus, x.Status = "Rejected", "UCC upload"; return nil })
	if _, err := o.AdvanceStage(ctx, T, "farhan.khan", r.UCC); err == nil || !strings.Contains(err.Error(), "Rejected at the KRA") {
		t.Fatalf("activated with a rejected KYC: %v", err)
	}
}

// The KRA's daily demise data opens a case like NSE's monthly list.
func TestKRADailyDemise(t *testing.T) {
	o, ctx := setup(t)
	siClock(o, siAt(D, "11:00"))
	kgClient(t, o, ctx, "KX01", "ABCPX0001A", "Validated", "2025-01-01")
	l, err := o.LoadDemiseList(ctx, T, "vikram.shah", DemiseLoadReq{Format: DemiseListKRA, Name: "KRA_Demise_Data_16102026.csv", Date: D}, []byte("PAN,Client Code\nABCPX0001A,KX01\n"))
	if err != nil || l.Matched != 1 || l.Period != "2026-10" || l.Cutoff != "2026-11-21" {
		t.Fatalf("daily demise %+v %v", l, err)
	}
	if c, _ := o.Client(ctx, T, "KX01"); c.Deceased == nil || !c.Blocked {
		t.Fatalf("not blocked: %+v", c.Deceased)
	}
	if _, err := o.LoadDemiseList(ctx, T, "vikram.shah", DemiseLoadReq{Format: "other", Name: "x.csv"}, []byte("PAN\n")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown format: %v", err)
	}
}
