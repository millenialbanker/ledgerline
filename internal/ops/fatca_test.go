package ops

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// fatcaClient is a new client for these tests (synthetic data).
func fatcaClient(pan, name, typ string, d *FATCADecl) Client {
	return Client{Name: name, Type: typ, PAN: pan, DOB: "1988-06-01", Mobile: "9876500123", Email: strings.ToLower(strings.Fields(name)[0]) + "@example.test", City: "Pune", State: "Maharashtra",
		Segments: []string{"CM"}, Income: "5-10 L", Bank: BankAccount{Bank: "Sample Bank", IFSC: "SBIN0001234", Account: "123456789055"}, RiskDisclosure: true, FATCA: d}
}

// kraCSV reads a KRA file into its header and rows by column name.
func kraCSV(t *testing.T, b []byte) ([]string, []map[string]string) {
	t.Helper()
	rs, err := csv.NewReader(bytes.NewReader(b)).ReadAll()
	if err != nil || len(rs) == 0 {
		t.Fatalf("csv: %v", err)
	}
	var out []map[string]string
	for _, r := range rs[1:] {
		m := map[string]string{}
		for i, h := range rs[0] {
			m[h] = r[i]
		}
		out = append(out, m)
	}
	return rs[0], out
}

func kraRowFor(t *testing.T, rows []map[string]string, ucc string) map[string]string {
	t.Helper()
	for _, r := range rows {
		if r["UCC"] == ucc {
			return r
		}
	}
	t.Fatalf("%s not in the KRA file", ucc)
	return nil
}

func TestFATCAOnboardingAndKRAFile(t *testing.T) {
	o, ctx := setup(t)
	today := o.now().Format("2006-01-02")
	// A new client needs a self-certification.
	if _, err := o.CreateApplication(ctx, T, "priya.nair", fatcaClient("ABCPF1001F", "Farah Shaikh", "Individual", nil)); err == nil || !strings.Contains(err.Error(), "FATCA") {
		t.Fatalf("no self-certification accepted: %v", err)
	}
	// A foreign residence needs its TIN or a reason; reason B an
	// explanation; a US TIN is 9 digits; a US resident is a US person.
	bad := []*FATCADecl{
		{OtherThanIndia: "Y", PlaceOfBirth: "Pune", CountryOfBirth: "IN", Nationality: "IN", USPerson: "N"},
		{OtherThanIndia: "Y", Residences: []TaxResidence{{Country: "GB"}}, PlaceOfBirth: "Pune", CountryOfBirth: "IN", Nationality: "IN", USPerson: "N"},
		{OtherThanIndia: "Y", Residences: []TaxResidence{{Country: "AE", NoTIN: "B"}}, PlaceOfBirth: "Pune", CountryOfBirth: "IN", Nationality: "IN", USPerson: "N"},
		{OtherThanIndia: "Y", Residences: []TaxResidence{{Country: "US", TIN: "12-345"}}, PlaceOfBirth: "Pune", CountryOfBirth: "IN", Nationality: "IN"},
		{OtherThanIndia: "Y", Residences: []TaxResidence{{Country: "ZZ", TIN: "1"}}, PlaceOfBirth: "Pune", CountryOfBirth: "IN", Nationality: "IN", USPerson: "N"},
		{OtherThanIndia: "N", PlaceOfBirth: "123", CountryOfBirth: "IN", Nationality: "IN", USPerson: "N"},
		{OtherThanIndia: "N", PlaceOfBirth: "Pune", CountryOfBirth: "IN", Nationality: "IN", USPerson: "Y"},
	}
	for i, d := range bad {
		if _, err := o.CreateApplication(ctx, T, "priya.nair", fatcaClient("ABCPF1001F", "Farah Shaikh", "Individual", d)); err == nil {
			t.Fatalf("bad declaration %d accepted", i)
		}
	}
	// India only: the defaults are filled in.
	ind, err := o.CreateApplication(ctx, T, "priya.nair", fatcaClient("ABCPF1001F", "Farah Shaikh", "Individual", IndiaOnlyFATCA("Pune", "")))
	if err != nil {
		t.Fatal(err)
	}
	d := ind.FATCA
	if d.OtherThanIndia != "N" || d.SelfCert != "Y" || d.DeclaredOn != today || d.AccountType != "CRS1102" || d.AccountStatus != AccountNew || d.Mode != FATCAModeForm || d.By != "priya.nair" || d.KRA != "" {
		t.Fatalf("defaults %+v", d)
	}
	// A US person and a UK resident with no TIN (the UK issues them: reason B, explained).
	us, err := o.CreateApplication(ctx, T, "priya.nair", fatcaClient("ABCPG1002G", "George Mathew", "Individual", &FATCADecl{OtherThanIndia: "y",
		Residences:   []TaxResidence{{Country: "United States", TIN: "123-45-6789"}, {Country: "gb", NoTIN: "b", NoTINWhy: "Applied for, not yet issued"}, {Country: "IN"}},
		PlaceOfBirth: "Kochi", CountryOfBirth: "India", Nationality: "IN", USPerson: "N"}))
	if err != nil {
		t.Fatal(err)
	}
	if d := us.FATCA; d.USPerson != "Y" || len(d.Residences) != 2 || d.Residences[0].Country != "US" || d.Residences[0].TIN != "123-45-6789" || d.Residences[1].NoTIN != "B" {
		t.Fatalf("foreign residences %+v", d)
	}
	// What the screens see is masked.
	if p := us.Public(); p.FATCA.Residences[0].TIN != "XXXXXXX6789" || us.FATCA.Residences[0].TIN != "123-45-6789" {
		t.Fatalf("masking %+v", p.FATCA.Residences)
	}
	// A company names controlling persons; an individual cannot.
	if _, err := o.CreateApplication(ctx, T, "priya.nair", fatcaClient("ABCPH1003H", "Hari Menon", "Individual", &FATCADecl{OtherThanIndia: "N", PlaceOfBirth: "Pune", CountryOfBirth: "IN", Nationality: "IN", USPerson: "N",
		ControllingPersons: []ControllingPerson{{Name: "Some One", Type: "CRS801", OtherThanIndia: "N"}}})); err == nil {
		t.Fatal("an individual with controlling persons")
	}
	corp, err := o.CreateApplication(ctx, T, "priya.nair", fatcaClient("ABCCK1004K", "Kestrel Traders Pvt Ltd", "Corporate", &FATCADecl{OtherThanIndia: "N", PlaceOfBirth: "Mumbai", CountryOfBirth: "IN", USPerson: "N",
		ControllingPersons: []ControllingPerson{{Name: "Kiran Desai", Type: "CRS801", PAN: "ABCPD1005D", OtherThanIndia: "N"}, {Name: "Leela Shah", Type: "CRS803", OtherThanIndia: "Y", Residences: []TaxResidence{{Country: "SG", TIN: "S1234567D"}}}}}))
	if err != nil {
		t.Fatal(err)
	}

	// The KRA upload file: the twelve columns as they were, then the
	// FATCA/CRS columns named as the KRA names them.
	f, err := o.KRAUploadBatch(ctx, T, "priya.nair", today)
	if err != nil {
		t.Fatal(err)
	}
	head, rows := kraCSV(t, f.Data)
	if strings.Join(head[:12], ",") != "PAN,Name,DOB,Mobile,Email,City,State,KYCMode,IPVDone,AccountOpened,Change,UCC" {
		t.Fatalf("existing columns changed: %v", head[:12])
	}
	if strings.Join(head[12:18], "|") != "Place of Birth|Country of Birth|Tax Residency other than India?|Country of Tax Residency1|Country of Tax Residency2|Country of Tax Residency3" || head[23] != "Date of Declaration" || head[19] != "Tax Identification Number1" {
		t.Fatalf("FATCA columns %v", head[12:])
	}
	r := kraRowFor(t, rows, us.UCC)
	if r["Change"] != "New" || r["Tax Residency other than India?"] != "Y" || r["Country of Tax Residency1"] != "US" || r["Tax Identification Number1"] != "123-45-6789" ||
		r["Country of Tax Residency2"] != "GB" || r["TIN Exemption Reason2"] != "B" || r["Country of Birth"] != "IN" || r["Place of Birth"] != "Kochi" || r["US Person"] != "Y" ||
		r["Date of Declaration"] != kraDate(today) || r["Self-certification Valid"] != "Y" || r["Account Type"] != "CRS1102" || r["Pre-existing or New Account"] != AccountNew {
		t.Fatalf("US person's row %v", r)
	}
	if r := kraRowFor(t, rows, ind.UCC); r["Tax Residency other than India?"] != "N" || r["Country of Tax Residency1"] != "" {
		t.Fatalf("India-only row %v", r)
	}
	if r := kraRowFor(t, rows, corp.UCC); r["Controlling Persons"] != "2" {
		t.Fatalf("company row %v", r)
	}
	if f.Batch.ID == "" || len(f.Batch.UCCs) != 3 || f.Note != "" {
		t.Fatalf("batch %+v note %q", f.Batch, f.Note)
	}
	// The scheduler's file is the same file, without a batch.
	if b, _ := o.KRAUploadFile(ctx, T, today); !bytes.Equal(b, f.Data) {
		t.Fatal("the nightly file differs")
	}
	// The controlling persons file: one line a person.
	cp, n, err := o.KRAControllingPersonsFile(ctx, T, "priya.nair", f.Batch.ID)
	if err != nil || n != 2 {
		t.Fatalf("controlling persons %d %v", n, err)
	}
	_, cps := kraCSV(t, cp)
	if cps[1]["Controlling Person Name"] != "Leela Shah" || cps[1]["Controlling Person Type"] != "CRS803" || cps[1]["Country of Tax Residency1"] != "SG" || cps[0]["Controlling Person PAN"] != "ABCPD1005D" {
		t.Fatalf("persons %v", cps)
	}
	// Marking the file uploaded puts each certification at the KRA.
	if _, err := o.MarkKRABatchUploaded(ctx, T, "priya.nair", f.Batch.ID, today, ""); err == nil {
		t.Fatal("marked uploaded without the KRA's reference")
	}
	if _, err := o.MarkKRABatchUploaded(ctx, T, "priya.nair", f.Batch.ID, today, "KRA-ACK-1"); err != nil {
		t.Fatal(err)
	}
	if c := mustClient(t, o, us.UCC); c.FATCA.KRA != FATCAKRAUploaded || c.FATCA.KRARef != "KRA-ACK-1" {
		t.Fatalf("after upload %+v", c.FATCA)
	}
	if _, err := o.MarkKRABatchUploaded(ctx, T, "priya.nair", f.Batch.ID, today, "again"); !errors.Is(err, ErrState) {
		t.Fatalf("marked twice: %v", err)
	}
	// The KRA's status file: FATCA Y accepts, N rejects with the remark.
	st := "PAN,Status,Remarks,FATCA\nABCPG1002G,Registered,,Y\nABCPF1001F,On hold,TIN missing,N\n"
	if _, err := o.LoadFile(ctx, T, "vikram.shah", "KRA-STATUS", "", "kra.csv", []byte(st)); err != nil {
		t.Fatal(err)
	}
	if c := mustClient(t, o, us.UCC); c.FATCA.KRA != FATCAKRAAccepted {
		t.Fatalf("accepted %+v", c.FATCA)
	}
	if c := mustClient(t, o, ind.UCC); c.FATCA.KRA != FATCAKRARejected || c.FATCA.KRARemark != "TIN missing" {
		t.Fatalf("rejected %+v", c.FATCA)
	}
	if _, err := o.LoadFile(ctx, T, "vikram.shah", "KRA-STATUS", "", "kra2.csv", []byte("PAN,Status,Remarks,FATCA\nABCPG1002G,Registered,,maybe\n")); err == nil {
		t.Fatal("a FATCA value other than Y or N")
	}
}

func TestFATCAChangeIsMakerChecked(t *testing.T) {
	o, ctx := setup(t)
	today := o.now().Format("2006-01-02")
	c, err := o.CreateApplication(ctx, T, "priya.nair", fatcaClient("ABCPM2001M", "Meena Pillai", "Individual", &FATCADecl{OtherThanIndia: "Y",
		Residences: []TaxResidence{{Country: "GB", TIN: "AB123456C"}}, PlaceOfBirth: "Kollam", CountryOfBirth: "IN", Nationality: "IN", USPerson: "N"}))
	if err != nil {
		t.Fatal(err)
	}
	// The client moves to Canada too: the form sends the UK TIN back masked.
	nd := FATCADecl{OtherThanIndia: "Y", Residences: []TaxResidence{{Country: "GB", TIN: MaskTIN("AB123456C")}, {Country: "CA", TIN: "123456789"}}, PlaceOfBirth: "Kollam", CountryOfBirth: "IN", Nationality: "IN", USPerson: "N", DeclaredOn: today}
	b, _ := json.Marshal(nd)
	vals := map[string]string{"fatca": string(b), "mode": FATCAModeForm}
	if _, err := o.RequestChange(ctx, T, "priya.nair", c.UCC, ChangeFATCA, vals, ""); err == nil || !strings.Contains(err.Error(), DocFATCA) {
		t.Fatalf("no signed self-certification attached: %v", err)
	}
	doc, err := o.AddClientDoc(ctx, T, "priya.nair", c.UCC, DocFATCA, "fatca.png", png1)
	if err != nil {
		t.Fatal(err)
	}
	r, err := o.RequestChange(ctx, T, "priya.nair", c.UCC, ChangeFATCA, vals, doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.New["summary"], "Canada") || !strings.Contains(r.Old["summary"], "United Kingdom") || strings.Contains(r.New["summary"], "AB123456C") {
		t.Fatalf("summaries %v / %v", r.Old, r.New)
	}
	// Nothing changes when the same declaration is sent.
	same, _ := json.Marshal(c.FATCA.masked())
	if _, err := o.RequestChange(ctx, T, "farhan.khan", c.UCC, ChangeFATCA, map[string]string{"fatca": string(same)}, doc.ID); err == nil {
		t.Fatal("an unchanged declaration raised")
	}
	if _, err := o.DecideChange(ctx, T, "priya.nair", r.ID, true, ""); err == nil {
		t.Fatal("the maker approved")
	}
	if _, err := o.DecideChange(ctx, T, "farhan.khan", r.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	got := mustClient(t, o, c.UCC)
	if len(got.FATCA.Residences) != 2 || got.FATCA.Residences[0].TIN != "AB123456C" || got.FATCA.Residences[1].Country != "CA" || got.KYCUpdatedOn != today || got.FATCA.By != "priya.nair" || got.FATCA.KRA != "" {
		t.Fatalf("after approval %+v (updated %s)", got.FATCA, got.KYCUpdatedOn)
	}
	// The change goes to the KRA as a modification.
	_, rows := kraCSV(t, mustKRA(t, o, today))
	if r := kraRowFor(t, rows, c.UCC); r["Country of Tax Residency2"] != "CA" || r["Tax Identification Number1"] != "AB123456C" {
		t.Fatalf("modification %v", r)
	}
	// From the portal, the client's own declaration needs no document.
	fatcaActivate(t, o, c.UCC)
	if _, _, err := o.PortalAccess(ctx, T, "farhan.khan", c.UCC, nil, false); err != nil {
		t.Fatal(err)
	}
	back, _ := json.Marshal(IndiaOnlyFATCA("Kollam", ""))
	pr, err := o.RequestChange(ctx, T, strings.ToLower(c.UCC), c.UCC, ChangeFATCA, map[string]string{"fatca": string(back)}, "")
	if err != nil || pr.Source != "Portal" {
		t.Fatalf("portal change %+v %v", pr, err)
	}
	if _, err := o.DecideChange(ctx, T, "farhan.khan", pr.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	if got := mustClient(t, o, c.UCC); got.FATCA.Mode != FATCAModePortal || !got.FATCA.indiaOnly() {
		t.Fatalf("portal declaration %+v", got.FATCA)
	}
}

// fatcaActivate takes a new client through onboarding: the maker moves it
// on, a checker who did not create it activates it.
func fatcaActivate(t *testing.T, o *Ops, ucc string) {
	t.Helper()
	for c := mustClient(t, o, ucc); c.Status != "Active"; c = mustClient(t, o, ucc) {
		who := "priya.nair"
		if c.Status == "UCC upload" {
			who = "farhan.khan"
		}
		if _, err := o.AdvanceStage(t.Context(), T, who, ucc); err != nil {
			t.Fatal(err)
		}
	}
}

func mustKRA(t *testing.T, o *Ops, since string) []byte {
	t.Helper()
	b, err := o.KRAUploadFile(t.Context(), T, since)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestFATCAReasonableness(t *testing.T) {
	o, ctx := setup(t)
	nri := fatcaClient("ABCPN3001N", "Naveen Rao", "NRI", IndiaOnlyFATCA("Udupi", ""))
	n, err := o.CreateApplication(ctx, T, "priya.nair", nri)
	if err != nil {
		t.Fatal(err)
	}
	corp, err := o.CreateApplication(ctx, T, "priya.nair", fatcaClient("ABCCQ3002Q", "Quill Exports Ltd", "Corporate", &FATCADecl{OtherThanIndia: "N", PlaceOfBirth: "Surat", CountryOfBirth: "IN", USPerson: "N"}))
	if err != nil {
		t.Fatal(err)
	}
	us, err := o.CreateApplication(ctx, T, "priya.nair", fatcaClient("ABCPU3003U", "Uma Iyer", "Individual", &FATCADecl{OtherThanIndia: "N", PlaceOfBirth: "Boston", CountryOfBirth: "US", Nationality: "IN", USPerson: "N"}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.SaveUCCDetails(ctx, T, "priya.nair", us.UCC, UCCDetails{Country: "184", Nationality: "Indian"}); err != nil {
		t.Fatal(err)
	}
	v, err := o.FATCA(ctx, T, D)
	if err != nil {
		t.Fatal(err)
	}
	codes := func(ucc string) []string {
		for _, r := range v.Rows {
			if r.UCC == ucc {
				var cs []string
				for _, w := range r.Warnings {
					cs = append(cs, w.Code)
				}
				return cs
			}
		}
		t.Fatalf("%s not on the tab", ucc)
		return nil
	}
	if cs := codes(n.UCC); !contains(cs, "address-abroad") {
		t.Fatalf("NRI declared India only: %v", cs)
	}
	if cs := codes(corp.UCC); !contains(cs, "no-controlling-persons") {
		t.Fatalf("company without controlling persons: %v", cs)
	}
	if cs := codes(us.UCC); !contains(cs, "address-abroad") || !contains(cs, "us-indicia") {
		t.Fatalf("US-born, address abroad: %v", cs)
	}
	// A sample client from the previous system has none: it is legacy.
	legacy := 0
	for _, r := range v.Rows {
		if r.Legacy && !r.Has && r.LegacyFor == "Collect the self-certification" {
			legacy++
		}
	}
	if legacy == 0 || v.Counts["No self-certification"] == 0 {
		t.Fatalf("legacy rows %d counts %v", legacy, v.Counts)
	}
	// Resolving: a note on what was checked, by a checker or the
	// compliance officer.
	if _, err := o.ResolveFATCAWarning(ctx, T, "priya.nair", corp.UCC, "no-controlling-persons", "A listed company: no controlling persons to report"); err == nil {
		t.Fatal("a maker resolved a warning")
	}
	if _, err := o.ResolveFATCAWarning(ctx, T, "vikram.shah", corp.UCC, "no-controlling-persons", "ok"); err == nil {
		t.Fatal("resolved without saying what was checked")
	}
	c, err := o.ResolveFATCAWarning(ctx, T, "vikram.shah", corp.UCC, "no-controlling-persons", "Listed on NSE: an active NFE, no controlling persons to report")
	if err != nil || len(c.FATCA.Resolved) != 1 {
		t.Fatalf("resolve %+v %v", c.FATCA, err)
	}
	if ws := fatcaWarnings(mustClient(t, o, corp.UCC), mustClient(t, o, corp.UCC).FATCA, UCCDetails{}); openWarnings(ws) != 0 {
		t.Fatalf("still open %+v", ws)
	}
	// An imported declaration with no TIN: cleared only by a correction.
	if _, err := o.LoadFile(ctx, T, "priya.nair", "FATCA-CRS", "", "fatca.csv", []byte("UCC,OtherThanIndia,DeclaredOn,Country1,PlaceOfBirth,CountryOfBirth\nBH1001,Y,2019-04-01,AE,Pune,IN\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := o.ResolveFATCAWarning(ctx, T, "vikram.shah", "BH1001", "no-tin-1", "The client says the UAE issues no TIN to him"); err == nil || !strings.Contains(err.Error(), "corrected") {
		t.Fatalf("a missing TIN resolved by a note: %v", err)
	}
}

func TestFATCALegacyBacklog(t *testing.T) {
	o, ctx := setup(t)
	today := o.now().Format("2006-01-02")
	// Self-certifications from the previous system: rows that repeat the
	// UCC add controlling persons; a client who has one keeps it.
	cs, _ := o.ClientMap(ctx, T)
	var corp string
	for _, u := range sortedKeys(cs) {
		if cs[u].Type == "Corporate" && corp == "" {
			corp = u
		}
	}
	file := "UCC,OtherThanIndia,DeclaredOn,PlaceOfBirth,CountryOfBirth,Nationality,USPerson,Country1,TIN1,SelfCertValid,CPName,CPType,CPOtherThanIndia,AtKRA\n" +
		"BH1001,N,2018-05-10,Pune,India,Indian,N,,,Y,,,,Y\n" +
		"BH1002,Y,2015-03-02,Mumbai,IN,IN,Y,US,123456789,Y,,,,\n" +
		"BH1003,N,2019-01-01,Nagpur,IN,IN,N,,,N,,,,\n" +
		corp + ",N,2020-02-02,Mumbai,IN,,N,,,Y,Asha Bose,CRS801,N,\n" +
		corp + ",N,,,,,,,,,Ravi Bose,CRS802,N,\n"
	if _, err := o.LoadFile(ctx, T, "priya.nair", "FATCA-CRS", "", "fatca.csv", []byte("UCC,OtherThanIndia,DeclaredOn\nBH1001,maybe,2018-05-10\n")); err == nil {
		t.Fatal("a residency flag other than Y or N")
	}
	res, err := o.LoadFile(ctx, T, "priya.nair", "FATCA-CRS", "", "fatca.csv", []byte(file))
	if err != nil || res.Added != 4 {
		t.Fatalf("import %+v %v", res, err)
	}
	b1 := mustClient(t, o, "BH1001").FATCA
	if b1.CountryOfBirth != "IN" || b1.Nationality != "IN" || b1.Mode != FATCAModeImported || b1.KRA != FATCAKRAAccepted || b1.KRARef == "" {
		t.Fatalf("imported %+v", b1)
	}
	if b2 := mustClient(t, o, "BH1002").FATCA; b2.KRA != "" {
		t.Fatalf("imported as at the KRA %+v", b2)
	}
	if c := mustClient(t, o, corp); len(c.FATCA.ControllingPersons) != 2 || c.FATCA.ControllingPersons[1].Type != "CRS802" {
		t.Fatalf("controlling persons %+v", c.FATCA)
	}
	if again, err := o.LoadFile(ctx, T, "priya.nair", "FATCA-CRS", "", "fatca2.csv", []byte("UCC,OtherThanIndia,DeclaredOn\nBH1001,N,2021-01-01\n")); err != nil || again.Added != 0 || mustClient(t, o, "BH1001").FATCA.DeclaredOn != "2018-05-10" {
		t.Fatalf("an import replaced a self-certification: %+v %v", again, err)
	}
	// A client master re-import keeps the self-certification.
	c1 := mustClient(t, o, "BH1001")
	cm := "UCC,Name,Type,PAN,DOB,Mobile,Email,City,Segments,Risk,Income,RAS,Bank,IFSC,Account,Depository,DematID,Nominee,NomineeName,KYCDate,Status,API,LastTrade,UPIBlock\n" +
		strings.Join([]string{c1.UCC, c1.Name, c1.Type, c1.PAN, "", c1.Mobile, c1.Email, c1.City, strings.Join(c1.Segments, ";"), c1.Risk, c1.Income, c1.RAS, c1.Bank.Bank, c1.Bank.IFSC, c1.Bank.Account, c1.Demat.Depository, c1.Demat.ID, "Opted out", "", c1.KYCDate, c1.Status, "N", "", ""}, ",") + "\n"
	if _, err := o.LoadFile(ctx, T, "priya.nair", "CLIENT-MASTER", "", "cm.csv", []byte(cm)); err != nil {
		t.Fatal(err)
	}
	if mustClient(t, o, "BH1001").FATCA == nil {
		t.Fatal("the re-import dropped the self-certification")
	}

	// The legacy bulk file: certifications on record, valid and not at
	// the KRA, as modifications.
	f, err := o.FATCALegacyFile(ctx, T, "priya.nair")
	if err != nil {
		t.Fatal(err)
	}
	_, rows := kraCSV(t, f.Data)
	// BH1001 was opened in 2026 (not legacy); BH1003's is not valid.
	if len(rows) != 2 || kraRowFor(t, rows, "BH1002")["Change"] != "Modify" || kraRowFor(t, rows, "BH1002")["Country of Tax Residency1"] != "US" || kraRowFor(t, rows, corp)["Controlling Persons"] != "2" {
		t.Fatalf("legacy rows %v", rows)
	}
	if b2 := mustClient(t, o, "BH1002").FATCA; b2.AccountStatus != accountStatusFor(mustClient(t, o, "BH1002").KYCDate, today) {
		t.Fatalf("account status %s for an account opened %s", b2.AccountStatus, mustClient(t, o, "BH1002").KYCDate)
	}
	if _, err := o.MarkKRABatchUploaded(ctx, T, "priya.nair", f.Batch.ID, today, "LEGACY-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := o.FATCALegacyFile(ctx, T, "priya.nair"); !errors.Is(err, ErrState) {
		t.Fatalf("a second file with nothing left: %v", err)
	}
	v, _ := o.FATCA(ctx, T, D)
	for _, r := range v.Rows {
		switch r.UCC {
		case "BH1002":
			if r.LegacyFor != "Uploaded, waiting for the KRA's status" {
				t.Fatalf("BH1002 %q", r.LegacyFor)
			}
		case "BH1003":
			if r.LegacyFor != "Collect a valid self-certification" {
				t.Fatalf("BH1003 %q", r.LegacyFor)
			}
		}
	}
	// The nightly self-inspection: the legacy backlog fails until every
	// legacy client's certification is at the KRA.
	run, err := o.RunSelfInspection(ctx, T, siCO, "")
	if err != nil {
		t.Fatal(err)
	}
	active := 0
	for _, c := range mustClients(t, o) {
		if c.Status == "Active" && legacyClient(c) {
			active++
		}
	}
	lg := siResultOf(t, run, "fatca_legacy")
	if lg.Status != SIFail || lg.Checked != active || lg.Found != active-2 {
		t.Fatalf("legacy test %+v (%d legacy clients, 2 at the KRA)", lg, active)
	}
	nw := siResultOf(t, run, "fatca_new")
	if nw.Status == "" {
		t.Fatalf("new clients test %+v", nw)
	}
}

func TestFATCASelfInspectionNewClients(t *testing.T) {
	o, ctx := setup(t)
	c, err := o.CreateApplication(ctx, T, "priya.nair", fatcaClient("ABCCR4001R", "Ridge Holdings Ltd", "Corporate", &FATCADecl{OtherThanIndia: "N", PlaceOfBirth: "Delhi", CountryOfBirth: "IN", USPerson: "N"}))
	if err != nil {
		t.Fatal(err)
	}
	set, _ := o.Settings(ctx, T)
	run := func() siOut {
		t.Helper()
		x := mustClient(t, o, c.UCC)
		x.Status = "Active"
		out, err := siFATCANew(&siEnv{o: o, ctx: ctx, tenant: T, date: o.now().Format("2006-01-02"), set: set, clients: map[string]Client{x.UCC: x}})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	// A company with no controlling persons: a warning to resolve.
	if out := run(); out.found != 1 || len(out.examples) != 1 || !strings.Contains(out.examples[0], "warning") {
		t.Fatalf("a company without controlling persons passed: %+v", out)
	}
	if _, err := o.ResolveFATCAWarning(ctx, T, "vikram.shah", c.UCC, "no-controlling-persons", "Listed company: an active NFE, no controlling persons"); err != nil {
		t.Fatal(err)
	}
	// Recorded today, so within the KRA's three working days.
	if out := run(); out.found != 0 || out.checked != 1 {
		t.Fatalf("after resolving %+v", out)
	}
	// Not at the KRA a week later: found.
	o.Now = func() time.Time { return time.Now().AddDate(0, 0, 8) }
	if out := run(); out.found != 1 || !strings.Contains(out.examples[0], "not at the KRA") {
		t.Fatalf("a week later %+v", out)
	}
}

func TestFATCACodeLists(t *testing.T) {
	cl := FATCACodeLists()
	if len(Countries) != 249 || cl.Countries[0][0] != "IN" || len(cl.Countries) != 249 || len(cl.ControllingPersons) != 13 || cl.MaxCountries != 4 {
		t.Fatalf("countries %d first %v persons %d", len(Countries), cl.Countries[0], len(cl.ControllingPersons))
	}
	for _, s := range []string{"usa", "United Kingdom", "Indian", "AE"} {
		if c, ok := countryByName(upperTrim(s)); !ok || Countries[c] == "" {
			t.Fatalf("%s not read as a country", s)
		}
	}
	if accountStatusFor("2015-12-31", "2026-11-17") != AccountPreexisting || accountStatusFor("2016-01-01", "2026-11-17") != AccountNew || accountStatusFor("", "2026-11-17") != AccountNew {
		t.Fatal("pre-existing or new")
	}
	if MaskTIN("123456789") != "XXXXX6789" || kraDate("2026-11-17") != "17-11-2026" {
		t.Fatal("masking or the KRA's date")
	}
}

func mustClients(t *testing.T, o *Ops) []Client {
	t.Helper()
	cs, err := o.Clients(t.Context(), T)
	if err != nil {
		t.Fatal(err)
	}
	return cs
}
