package ops

import (
	"errors"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/docs"
)

// onboardSetup is a firm with member codes and a few made-up clients.
func onboardSetup(t *testing.T) (*Ops, *testing.T) {
	t.Helper()
	o, ctx := newOps(t)
	siClock(o, siAt("2026-12-04", "11:00"))
	set, _ := o.Settings(ctx, T)
	set.MemberCodes = map[string]string{"NSE": "90311", "BSE": "6127", "MCX": "12345"} // made up
	if err := o.SaveSettings(ctx, T, "admin", set); err != nil {
		t.Fatal(err)
	}
	for _, c := range []Client{
		{UCC: "OBC01", Name: "Sample Corp Pvt Ltd", Type: "Corporate", PAN: "AAACS1234K", Status: "UCC upload", Segments: []string{"CM", "COM"}, KYCDate: "2026-12-01", City: "Pune"},
		{UCC: "OBI01", Name: "Meera Test", Type: "Individual", PAN: "ABCPM1111A", Status: "Active", Segments: []string{"CM"}, KYCDate: "2026-12-01", City: "Gangtok", State: "Sikkim", KYCRoute: "VIPV"},
		{UCC: "OBI02", Name: "Meera Test", Type: "Individual", PAN: "ABCPM1111A", Status: "Active", Segments: []string{"CM", "COM"}, KYCDate: "2026-12-02", City: "Gangtok", State: "Sikkim"},
		{UCC: "OBH01", Name: "Rao HUF", Type: "HUF", PAN: "AAAHR1234B", Status: "Active", Segments: []string{"CM"}, KYCDate: "2026-12-02", City: "Pune", Demat: Demat{Depository: "CDSL", ID: "1209000000000001"}},
	} {
		if err := docs.Upsert(ctx, o.Docs, T, cClient, c.UCC, c, "t"); err != nil {
			t.Fatal(err)
		}
	}
	return o, t
}

func issueIDs(is []OnboardIssue) string {
	var s []string
	for _, i := range is {
		s = append(s, i.ID+map[bool]string{true: "!", false: ""}[i.Block])
	}
	return strings.Join(s, " ")
}

func TestOnboardingChecks(t *testing.T) {
	o, _ := onboardSetup(t)
	ctx := t.Context()
	c, _ := o.Client(ctx, T, "OBC01")
	// A corporate on MCX without its board resolution cannot be activated.
	if ps := o.activationProblems(ctx, T, c); !strings.Contains(strings.Join(ps, "; "), "board resolution") {
		t.Fatalf("no board resolution check: %v", ps)
	}
	if _, err := o.SaveOnboarding(ctx, T, "priya.nair", "OBC01", ClientOnboarding{Purpose: "Hedging", BoardResolution: &BoardResolution{On: "2026-11-20", Document: "BR-2026-11"}}); err != nil {
		t.Fatal(err)
	}
	if ps := o.activationProblems(ctx, T, c); strings.Contains(strings.Join(ps, "; "), "board resolution") {
		t.Fatalf("board resolution recorded: %v", ps)
	}
	if _, err := o.SaveOnboarding(ctx, T, "priya.nair", "OBC01", ClientOnboarding{Purpose: "Gambling"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a purpose not on the list: %v", err)
	}

	// The VIPV of a resident: the master circular's conditions.
	v, _ := o.OnboardChecks(ctx, T, "vikram.shah", "OBI01")
	if got := issueIDs(v.Rows[0].Issues); !strings.Contains(got, "SEBI-KYCAML-0042!") || !strings.Contains(got, "SEBI-KYCAML-0010") || !strings.Contains(got, "NSE2-N0028!") {
		t.Fatalf("OBI01 issues %s", got)
	}
	vp := &VIPVRecord{Official: "E101 Sample Official", OfficialTrainedOn: "2026-06-01", Consent: true, At: "2026-12-01T11:30", Geo: "27.33, 88.61", GeoCountry: "IN",
		RecordingRef: "vipv/OBI01.mp4", RecordingSHA256: strings.Repeat("0a", 32), Questions: []string{"What is the date today?"}, OVDShown: "Passport", PhotoMatch: true, Liveness: true, LivenessMethod: "Head nod"}
	x := ClientOnboarding{Purpose: "Trading", VIPV: vp, Disability: "MDP"}
	if _, err := o.SaveOnboarding(ctx, T, "priya.nair", "OBI01", x); err != nil {
		t.Fatal(err)
	}
	v, _ = o.OnboardChecks(ctx, T, "vikram.shah", "OBI01")
	if got := issueIDs(v.Rows[0].Issues); strings.Contains(got, "SEBI-KYCAML-0042") || !strings.Contains(got, "NSE-0262!") {
		t.Fatalf("after the VIPV: %s", got)
	}
	// A person with mental disability operates through a guardian who is
	// KYC-compliant.
	x.Guardian = &OnboardGuardian{Signs: true, Name: "Asha Test", PAN: "ABCPA2222B", Relation: "Mother", KYCOK: true}
	x.LivenessMethod = "Head nod"
	// The second code for the PAN needs compliance's approval.
	x.SecondUCC = &SecondUCCApproval{OtherUCC: "OBI02", Reason: "Separate code for the client's commodity trading", On: "2026-12-03"}
	if _, err := o.SaveOnboarding(ctx, T, "priya.nair", "OBI01", x); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a maker approved a second code: %v", err)
	}
	if _, err := o.SaveOnboarding(ctx, T, "vikram.shah", "OBI01", x); err != nil {
		t.Fatal(err)
	}
	v, _ = o.OnboardChecks(ctx, T, "vikram.shah", "OBI01")
	if got := issueIDs(v.Rows[0].Issues); got != "" {
		t.Fatalf("OBI01 still has %s", got)
	}
	siWant(t, siOne(t, o, ctx, "second_ucc", "2026-12-04"), SIPass, "second code approved")

	// The Sikkim address of an MCX client: a work item until MCX is e-mailed.
	o.onboardDaily(ctx, T, "2026-12-04")
	if w := workFor(t, o, wOnboard, "sikkim:OBI02"); w.Status != "Open" {
		t.Fatalf("no Sikkim work item: %+v", w)
	}
	if _, err := o.SaveOnboarding(ctx, T, "priya.nair", "OBI02", ClientOnboarding{Purpose: "Trading", SikkimMailedOn: "2026-12-04", SikkimMailRef: "mail 1"}); err != nil {
		t.Fatal(err)
	}
	if w := workFor(t, o, wOnboard, "sikkim:OBI02"); w.Status == "Open" {
		t.Fatal("Sikkim work item not closed")
	}

	// An HUF's Karta and coparceners go to CDSL in the harmonised upload
	// only; the disability flag goes in both.
	h, _ := o.Client(ctx, T, "OBH01")
	in := cdslJointIn()
	in.C, in.D, in.Members, in.BO = h, CDSLBODetail{PANVerified: true}, nil, BOAccount{}
	in.C.State = "Maharashtra"
	in.Def = map[string]string{}
	for k, v := range cdslJointIn().Def {
		in.Def[k] = v
	}
	in.Def["bo_statement_cycle_code"], in.Def["nationality_code"] = "EM", "01"
	in.C.Bank, in.C.Income, in.C.Mobile, in.C.Email, in.C.DOB = in.C.Bank, "10-25 L", "9876500011", "huf@example.test", "1980-01-01"
	in.U.Contacts = []UCCContact{{Name: "Ravi Rao", Designation: "Karta"}}
	in.X = ClientOnboarding{Disability: "OTH", Karta: &OnboardPerson{Name: "Ravi Rao", PAN: "ABCPR3333C"}, Coparceners: []OnboardPerson{{Name: "Anil Rao", Relation: "SON", DOB: "2001-02-03"}}}
	rec, probs, _ := boSetupRecord(in)
	if len(probs) > 0 {
		t.Fatalf("HUF record problems %v", probs)
	}
	rows, p2 := cdslBOUploadRows(boCSVIn{Records: [][]boLine{rec}, DPID: "12090", Operator: "OP1", Batch: 1, At: o.now()})
	if len(p2) > 0 {
		t.Fatal(p2)
	}
	var purposes []string
	var dis string
	for _, r := range rows {
		purposes = append(purposes, r.Values["Purpse"])
		if r.Values["MntlDsblty"] != "" {
			dis = r.Values["MntlDsblty"]
		}
	}
	if j := strings.Join(purposes, ","); !strings.Contains(j, "KR") || !strings.Contains(j, "CP") || dis != "OTH" {
		t.Fatalf("harmonised lines %s, disability %q", j, dis)
	}
	plain, _, _ := boSetupRecord(cdslJointIn())
	plain = append(plain, boLine{Layout: boHarmonisedOnly, Who: "Karta", H: map[string]string{"Purpse": "KR", "FrstNm": "ZEDKARTA"}})
	if b, p := (boFile{Prefix: "08", DPID: "12090", Operator: "OP1", Records: [][]boLine{plain}}).Bytes(); len(p) > 0 || len(b) == 0 || strings.Contains(string(b), "ZEDKARTA") {
		t.Fatalf("the fixed-width file with an HUF: %v", p)
	}
	// An NPO without its DARPAN ID: the BO file is not made.
	in.D.Codes = map[string]string{"bo_category": "29", "sub_status": "0256"}
	if _, probs, _ := boSetupRecord(in); !strings.Contains(strings.Join(probs, ";"), "DARPAN") {
		t.Fatalf("NPO without DARPAN ID: %v", probs)
	}
	if _, err := o.SaveOnboarding(ctx, T, "priya.nair", "OBH01", ClientOnboarding{DarpanID: "12345"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a malformed DARPAN ID: %v", err)
	}
	siWant(t, siOne(t, o, ctx, "onboarding_details", "2026-12-04"), SIFail, "the HUF's coparceners are missing")
}

// No client code may be the firm's own: trading member codes, OWN,
// ERROR..., and the proprietary codes.
func TestUCCCollisions(t *testing.T) {
	o, _ := onboardSetup(t)
	ctx := t.Context()
	c := Client{Name: "Code Test", Type: "Individual", PAN: "ABCPC4444D", Mobile: "9800000123", Email: "code@example.test", City: "Pune", Segments: []string{"CM"}, Income: "5-10L",
		Bank: BankAccount{Bank: "Sample Bank", IFSC: "SBIN0001234", Account: "123456789014"}, RiskDisclosure: true, FATCA: IndiaOnlyFATCA("Pune", "")}
	for _, u := range []string{"90311", "OWN", "ERROR01"} {
		x := c
		x.UCC = u
		if _, err := o.CreateApplication(ctx, T, "priya.nair", x); err == nil || !strings.Contains(err.Error(), "cannot be used") {
			t.Fatalf("%s accepted: %v", u, err)
		}
	}
	siWant(t, siOne(t, o, ctx, "error_account", "2026-12-04"), SIFail, "no ERROR account")
	if _, err := o.SaveUCCControls(ctx, T, "vikram.shah", UCCControls{ErrorAccount: "OBI01"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a client's code as the ERROR account: %v", err)
	}
	if _, err := o.SaveUCCControls(ctx, T, "vikram.shah", UCCControls{ErrorAccount: "ERR9", PropCodes: []string{"prop1"}}); err != nil {
		t.Fatal(err)
	}
	siWant(t, siOne(t, o, ctx, "error_account", "2026-12-04"), SIPass, "ERROR account set")
	x := c
	x.UCC = "PROP1"
	if _, err := o.CreateApplication(ctx, T, "priya.nair", x); err == nil {
		t.Fatal("a proprietary code given to a client")
	}
	// A client already holding a colliding code: found, and activation stops.
	_ = docs.Upsert(ctx, o.Docs, T, cClient, "6127", Client{UCC: "6127", Name: "Old Code", Type: "Individual", PAN: "ABCPO5555E", Status: "Active", Segments: []string{"CM"}, City: "Pune"}, "t")
	siWant(t, siOne(t, o, ctx, "ucc_code_collision", "2026-12-04"), SIFail, "client code 6127 is the BSE member code")
	o.onboardDaily(ctx, T, "2026-12-04")
	if w := workFor(t, o, wOnboard, "collide:6127"); w.Status != "Open" {
		t.Fatalf("no collision work item %+v", w)
	}
}

// A written opt-out keeps a new client's UCC from that exchange.
func TestExchangeOptOut(t *testing.T) {
	o, _ := onboardSetup(t)
	ctx := t.Context()
	if _, err := o.SaveOnboarding(ctx, T, "priya.nair", "OBC01", ClientOnboarding{OptOuts: []ExchangeOptOut{{Exchange: "BSE", On: "2026-12-01"}}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an opt-out without its letter: %v", err)
	}
	if _, err := o.SaveOnboarding(ctx, T, "priya.nair", "OBC01", ClientOnboarding{OptOuts: []ExchangeOptOut{{Exchange: "BSE", On: "2026-12-01", Document: "Letter 7"}}}); err != nil {
		t.Fatal(err)
	}
	items, _, err := o.bseUCCItems(ctx, T)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if it.c.UCC == "OBC01" {
			t.Fatal("the client who opted out of BSE is still sent to BSE")
		}
	}
	siWant(t, siOne(t, o, ctx, "exchange_opt_out", "2026-12-04"), SIPass, "nothing sent after the opt-out")
}

// MCX takes UCC records from 7:00 a.m. to 10:30 p.m. on trading days.
func TestMCXUCCWindow(t *testing.T) {
	set := Settings{}
	for at, open := range map[string]bool{"2026-12-04 11:00": true, "2026-12-04 06:59": false, "2026-12-04 22:30": false, "2026-12-05 11:00": false, "2026-12-04 07:00": true} {
		tm, _ := time.ParseInLocation("2006-01-02 15:04", at, ist)
		if got := mcxUCCWindowWhy(set, tm) == ""; got != open {
			t.Errorf("%s: open %v, want %v", at, got, open)
		}
	}
}

// An NRI applies online by the PROI process: a mobile abroad, the e-mail
// verified, the passport and the overseas address instead of Aadhaar.
func TestOnlinePROIApplication(t *testing.T) {
	o, ctx := newOps(t)
	siClock(o, siAt("2026-12-04", "11:00"))
	o.DevCodes = true
	a, secret, codes, err := o.StartApplication(ctx, T, "+447700900456", "nri@mail.example", "", "", "")
	if err != nil {
		t.Fatalf("a mobile abroad: %v", err)
	}
	if a, err = o.VerifyAppCode(ctx, T, secret, "email", codes["email"]); err != nil || !contains(a.Steps, "contact") {
		t.Fatalf("the e-mail alone confirms a mobile abroad: %v %v", a.Steps, err)
	}
	if _, err := o.SaveAppStep(ctx, T, secret, AppStep{Step: "identity", Application: Application{PAN: "ABCPN6666F", Name: "Nikhil Test", DOB: "1985-05-05", Gender: "Male", Father: "Ravi Test"}}); err == nil || !strings.Contains(err.Error(), "outside India") {
		t.Fatalf("a mobile abroad for a resident: %v", err)
	}
	step := func(s string, d Application) Application {
		t.Helper()
		x, err := o.SaveAppStep(ctx, T, secret, AppStep{Step: s, Application: d})
		if err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		return x
	}
	step("identity", Application{PAN: "ABCPN6666F", Name: "Nikhil Test", DOB: "1985-05-05", Gender: "Male", Father: "Ravi Test", Residence: "NRI"})
	ov := &AppOverseas{Country: "GB", PassportNo: "Z7654321", PassportExpiry: "2032-01-01", Address: "10 Sample Road, Reading", City: "Reading"}
	if _, err := o.SaveAppStep(ctx, T, secret, AppStep{Step: "address", Application: Application{Overseas: ov}}); err == nil || !strings.Contains(err.Error(), "passport") {
		t.Fatalf("no passport uploaded: %v", err)
	}
	for _, k := range []string{AppDocPassport, AppDocOverseas, "Photograph", "Signature", "PAN card"} {
		if _, err := o.AddAppDoc(ctx, T, secret, k, strings.ToLower(k)+".png", png1); err != nil {
			t.Fatal(err)
		}
	}
	a = step("address", Application{Overseas: ov})
	if a.AddrSource != "Overseas (passport and address proof)" || a.City != "Reading" {
		t.Fatalf("address %+v", a)
	}
	step("tax", Application{FATCA: IndiaOnlyFATCA("Kochi", "")})
	step("bank", Application{Bank: "Sample Bank", IFSC: "SBIN0001234", Account: "50100123456780"})
	if _, err := o.SaveAppStep(ctx, T, secret, AppStep{Step: "profile", Application: Application{Segments: []string{"CM"}, Income: "10-25 L", Occupation: "Salaried", RAS: "Quarterly"}}); err == nil {
		t.Fatal("no purpose accepted")
	}
	step("profile", Application{Segments: []string{"CM"}, Income: "10-25 L", Occupation: "Salaried", RAS: "Quarterly", Purpose: "Long-term investment"})
	step("nominee", Application{NomineeOptOut: true})
	step("photo", Application{})
	all := map[string]bool{}
	for k := range o.appDeclarations(ctx, T) {
		all[k] = true
	}
	_, dev, err := o.SignApplication(ctx, T, secret, all, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if a, _, err = o.SignApplication(ctx, T, secret, all, false, dev["esign"]); err != nil || a.UCC == "" {
		t.Fatalf("submit %+v %v", a, err)
	}
	c, _ := o.Client(ctx, T, a.UCC)
	if c.Type != "NRI" || c.PROI == nil || c.PROI.PassportNo != "Z7654321" || c.PROI.Country != "GB" || c.PROI.KRALookupOn == "" || !c.PROI.EmailVerified || c.PROI.MobileVerified {
		t.Fatalf("client %+v PROI %+v", c, c.PROI)
	}
	if x := o.OnboardingFor(ctx, T, c.UCC); x.Purpose != "Long-term investment" {
		t.Fatalf("purpose %+v", x)
	}
	// Staff still record the CKYCRR search, the video IPV and the rest.
	if ps := proiProblems(c, "2026-12-04"); !strings.Contains(strings.Join(ps, ";"), "in-person verification") {
		t.Fatalf("PROI problems %v", ps)
	}
	if !strings.Contains(proiEmailOnly(c), "not verified") {
		t.Fatal("messages to the unverified mobile abroad")
	}
}
