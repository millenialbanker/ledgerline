package ops

import (
	"strings"
	"testing"
)

var png1 = []byte("\x89PNG\r\n\x1a\n0000000000000000")

func TestOnlineAccountOpening(t *testing.T) {
	o, ctx := setup(t)
	o.DevCodes = true
	if err := o.seedSampleBrokerage(ctx, T, D); err != nil {
		t.Fatal(err)
	}
	if err := o.seedSamplePartners(ctx, T); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := o.StartApplication(ctx, T, "12345", "x", "", "", ""); err == nil {
		t.Fatal("bad contact accepted")
	}
	a, secret, codes, err := o.StartApplication(ctx, T, "9876500011", "kavya@mail.example", "AP001", "", "10.0.0.1")
	if err != nil || codes["mobile"] == "" || !strings.HasPrefix(secret, a.ID+".") {
		t.Fatalf("start: %+v %v", a, err)
	}
	if _, err := o.Applicant(ctx, T, a.ID+".wrong"); err == nil {
		t.Fatal("wrong secret accepted")
	}
	if _, err := o.SaveAppStep(ctx, T, secret, AppStep{Step: "identity"}); err == nil {
		t.Fatal("step before contact confirmed")
	}
	if _, err := o.VerifyAppCode(ctx, T, secret, "mobile", "000000x"); err == nil {
		t.Fatal("wrong code accepted")
	}
	if _, err := o.VerifyAppCode(ctx, T, secret, "mobile", codes["mobile"]); err != nil {
		t.Fatal(err)
	}
	if a, err = o.VerifyAppCode(ctx, T, secret, "email", codes["email"]); err != nil || !contains(a.Steps, "contact") {
		t.Fatalf("contact: %+v %v", a.Steps, err)
	}
	step := func(s string, d Application, dl bool) Application {
		t.Helper()
		x, err := o.SaveAppStep(ctx, T, secret, AppStep{Step: s, Application: d, DigiLocker: dl})
		if err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		return x
	}
	cs, _ := o.Clients(ctx, T)
	if _, err := o.SaveAppStep(ctx, T, secret, AppStep{Step: "identity", Application: Application{PAN: cs[0].PAN, Name: "Kavya Iyer", DOB: "1994-03-02", Gender: "Female", Father: "Ramesh Iyer"}}); err == nil {
		t.Fatal("existing client's PAN accepted")
	}
	if _, err := o.SaveAppStep(ctx, T, secret, AppStep{Step: "identity", Application: Application{PAN: "ABCPI1234K", Name: "Kavya Iyer", DOB: "2012-03-02", Gender: "Female", Father: "Ramesh Iyer"}}); err == nil {
		t.Fatal("minor accepted")
	}
	a = step("identity", Application{PAN: "abcpi1234k", Name: "Kavya  Iyer", DOB: "1994-03-02", Gender: "Female", Father: "Ramesh Iyer", Marital: "Single"}, false)
	if a.PAN != "ABCPI1234K" || a.Name != "Kavya Iyer" || len(a.Checks) != 2 || a.IPVCode == "" {
		t.Fatalf("identity: %+v", a)
	}
	a = step("address", Application{}, true)
	if a.City != "Bengaluru" || a.AddrSource != "DigiLocker" {
		t.Fatalf("digilocker: %+v", a)
	}
	if _, err := o.SaveAppStep(ctx, T, secret, AppStep{Step: "bank", Application: Application{Bank: "HDFC Bank", IFSC: "HDFC0001234", Account: "123456780000"}}); err == nil {
		t.Fatal("failed penny drop accepted")
	}
	step("bank", Application{Bank: "HDFC Bank", IFSC: "hdfc0001234", Account: "50100123456789"}, false)
	if _, err := o.SaveAppStep(ctx, T, secret, AppStep{Step: "profile", Application: Application{Segments: []string{"CM", "FO"}, Income: "10-25 L", Occupation: "Salaried", RAS: "Quarterly"}}); err == nil {
		t.Fatal("F&O without income proof")
	}
	for _, k := range []string{"Income proof", "Photograph", "Signature", "PAN card"} {
		if _, err := o.AddAppDoc(ctx, T, secret, k, strings.ToLower(k)+".png", png1); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := o.AddAppDoc(ctx, T, secret, "Photograph", "x.exe", []byte("MZ\x90\x00 not an image")); err == nil {
		t.Fatal("executable accepted")
	}
	step("profile", Application{Segments: []string{"CM", "FO"}, Income: "10-25 L", Occupation: "Salaried", Experience: "1–5 years", RAS: "Quarterly"}, false)
	step("nominee", Application{NomineeName: "Ramesh Iyer", NomineeRel: "Father"}, false)
	a = step("photo", Application{}, false)
	if _, err := o.SaveAppStep(ctx, T, secret, AppStep{Step: "ipv"}); err == nil {
		t.Fatal("IPV without video")
	}
	webm := append([]byte{0x1a, 0x45, 0xdf, 0xa3}, []byte(strings.Repeat("webm", 50))...)
	if _, err := o.AddAppDoc(ctx, T, secret, "IPV / VIPV record", "ipv.webm", webm); err != nil {
		t.Fatal(err)
	}
	step("ipv", Application{}, false)
	all := map[string]bool{}
	for k := range AppDeclarations {
		all[k] = true
	}
	if _, _, err := o.SignApplication(ctx, T, secret, map[string]bool{"rdd": true}, false, ""); err == nil {
		t.Fatal("signed without every declaration")
	}
	_, dev, err := o.SignApplication(ctx, T, secret, all, true, "")
	if err != nil || dev["esign"] == "" {
		t.Fatalf("sign code: %v", err)
	}
	a, _, err = o.SignApplication(ctx, T, secret, all, true, dev["esign"])
	if err != nil || a.Status != "Submitted" || a.UCC == "" || a.FormHash == "" {
		t.Fatalf("submit: %+v %v", a, err)
	}
	c, _ := o.Client(ctx, T, a.UCC)
	if c.Status != "KYC verification" || c.KYCRoute != "Online (e-KYC)" || c.Partner != "AP001" || !c.WhatsApp || c.WAConsent == nil || !c.IncomeProof {
		t.Fatalf("client: %+v", c)
	}
	ds, _ := o.ClientDocs(ctx, T, a.UCC)
	if len(ds) != 6 { // income, photo, signature, PAN, video, signed form
		t.Fatalf("documents: %d", len(ds))
	}
	if _, err := o.SaveAppStep(ctx, T, secret, AppStep{Step: "nominee", Application: Application{NomineeOptOut: true}}); err == nil {
		t.Fatal("changed after submitting")
	}
	// The creator of record is the applicant, so any checker may activate.
	for c.Status != "Active" {
		if c, err = o.AdvanceStage(ctx, T, "farhan.khan", a.UCC); err != nil {
			t.Fatal(err)
		}
	}
	// Rejecting another application tells the applicant.
	b, s2, codes2, _ := o.StartApplication(ctx, T, "9876500022", "x@mail.example", "", "", "")
	_, _ = o.VerifyAppCode(ctx, T, s2, "mobile", codes2["mobile"])
	if _, err := o.RejectApplication(ctx, T, "priya.nair", b.ID, "x"); err == nil {
		t.Fatal("maker rejected")
	}
	if b, err = o.RejectApplication(ctx, T, "farhan.khan", b.ID, "Incomplete documents"); err != nil || b.Status != "Rejected" {
		t.Fatalf("reject: %v", err)
	}
	// Without a provider, checks are left to staff.
	o.DevCodes = false
	if got := o.kyc().PAN("ABCPI1234K", "x", "y"); got.Status != "Check manually" {
		t.Fatalf("manual provider: %+v", got)
	}
}
