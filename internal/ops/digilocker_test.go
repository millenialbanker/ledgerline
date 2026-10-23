package ops

import (
	"context"
	"strings"
	"testing"
)

func TestNamesMatch(t *testing.T) {
	for _, c := range []struct {
		a, b string
		ok   bool
	}{
		{"Kavya Iyer", "KAVYA IYER", true},
		{"Kavya  Iyer", "Iyer Kavya", true},
		{"Ravi K Sharma", "RAVI KUMAR SHARMA", true},
		{"Ravi Kumar Sharma", "Ravi Sharma", true},
		{"Kavya Iyer", "Kavya Rao", false},
		{"Kavya", "Kavya Iyer", false},
		{"", "Kavya", false},
		{"Mohd. Irfan", "MOHD IRFAN", true},
	} {
		if NamesMatch(c.a, c.b) != c.ok {
			t.Errorf("%q vs %q: want %v", c.a, c.b, c.ok)
		}
	}
}

// dlFake is a KYC provider whose DigiLocker goes through a plugin.
type dlFake struct {
	simKYC
	res     DigiLockerResult
	started DigiLockerHint
}

func (f *dlFake) StartDigiLocker(_ context.Context, ref, ret string, h DigiLockerHint) (string, string, error) {
	f.started = h
	return "https://kyc.example/start/S1", "S1", nil
}
func (f *dlFake) DigiLockerResult(context.Context, string) (DigiLockerResult, error) {
	return f.res, nil
}

// applyMethod is how apply shares Aadhaar through the plugin.
var applyMethod = AadhaarDigiLocker

// walkDigiLocker, when set, takes the applicant through DigiLocker.
var walkDigiLocker func(t *testing.T, url string)

// apply runs an application to signing with the given provider.
func apply(t *testing.T, o *Ops, ctx context.Context, mobile, pan string, viaPlugin bool) (Application, error) {
	t.Helper()
	a, secret, codes, err := o.StartApplication(ctx, T, mobile, "a"+mobile+"@mail.example", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	o.VerifyAppCode(ctx, T, secret, "mobile", codes["mobile"])
	o.VerifyAppCode(ctx, T, secret, "email", codes["email"])
	must := func(x Application, err error) Application {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return x
	}
	must(o.SaveAppStep(ctx, T, secret, AppStep{Step: "identity", Application: Application{PAN: pan, Name: "Kavya Iyer", DOB: "1994-03-02", Gender: "Female", Father: "Ramesh Iyer", Marital: "Single"}}))
	if viaPlugin {
		if _, err := o.SaveAppStep(ctx, T, secret, AppStep{Step: "address"}); err == nil {
			t.Fatal("address before DigiLocker")
		}
		url, err := o.StartDigiLocker(ctx, T, secret, "https://broker.example/open/demo", applyMethod)
		if err != nil || url == "" {
			t.Fatalf("start digilocker %q %v", url, err)
		}
		if walkDigiLocker != nil {
			walkDigiLocker(t, url)
		}
	}
	if a, err = o.SaveAppStep(ctx, T, secret, AppStep{Step: "address", DigiLocker: true}); err != nil {
		return a, err
	}
	must(o.SaveAppStep(ctx, T, secret, AppStep{Step: "bank", Application: Application{Bank: "HDFC Bank", IFSC: "HDFC0001234", Account: "50100123456789"}}))
	for _, k := range []string{"Photograph", "Signature"} {
		must(o.AddAppDoc(ctx, T, secret, k, k+".png", png1))
	}
	must(o.SaveAppStep(ctx, T, secret, AppStep{Step: "profile", Application: Application{Segments: []string{"CM"}, Income: "10-25 L", Occupation: "Salaried", Experience: "None", RAS: "Quarterly"}}))
	must(o.SaveAppStep(ctx, T, secret, AppStep{Step: "nominee", Application: Application{NomineeOptOut: true}}))
	if !viaPlugin || applyMethod == AadhaarOffline {
		must(o.AddAppDoc(ctx, T, secret, "PAN card", "pan.png", png1))
	}
	must(o.SaveAppStep(ctx, T, secret, AppStep{Step: "photo"}))
	all := map[string]bool{}
	for k := range AppDeclarations {
		all[k] = true
	}
	_, dev, _ := o.SignApplication(ctx, T, secret, all, false, "")
	a, _, err = o.SignApplication(ctx, T, secret, all, false, dev["esign"])
	return a, err
}

func TestFullAutopilotActivatesCleanApplications(t *testing.T) {
	o, ctx := setup(t)
	o.DevCodes = true
	o.SetEdition(T, EditionFull)
	a, err := apply(t, o, ctx, "9876500031", "ABCPI1234K", false)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := o.Client(ctx, T, a.UCC)
	if c.Status != "Active" || c.ActivatedBy != "system:autopilot" {
		t.Fatalf("clean application not activated: %s by %s; blockers %v", c.Status, c.ActivatedBy, a.blockers())
	}
	ws, _ := o.WorkItems(ctx, T, "Open")
	for _, w := range ws {
		if w.Key == "eapply:"+a.ID {
			t.Fatal("activated application also queued")
		}
	}

	// Through the plugin, with a name that does not match Aadhaar: a person checks it.
	f := &dlFake{}
	f.res.Status = "Completed"
	f.res.Aadhaar = &DLAadhaar{Name: "Kavya R Iyengar", DOB: "1994-03-02", Gender: "Female", Last4: "4321", Address: "7 MG Road", District: "Pune", State: "Maharashtra", PIN: "411001", Photo: png1, XML: []byte("<Certificate/>")}
	f.res.PAN = &DLPAN{Number: "ABCPI1235K", Name: "Kavya Iyer", DOB: "1994-03-02", XML: []byte("<Certificate/>")}
	o.KYC = f
	a, err = apply(t, o, ctx, "9876500032", "ABCPI1235K", true)
	if err != nil {
		t.Fatal(err)
	}
	if f.started.PAN != "ABCPI1235K" || f.started.Name != "Kavya Iyer" {
		t.Fatalf("hint %+v", f.started)
	}
	if a.City != "Pune" || a.PIN != "411001" || a.AddrSource != "DigiLocker (Aadhaar)" {
		t.Fatalf("address %+v", a)
	}
	var aad, pan AppCheck
	for _, c := range a.Checks {
		switch c.Name {
		case "Aadhaar (DigiLocker)":
			aad = c
		case "PAN":
			pan = c
		}
	}
	if aad.Status != "Check manually" || !strings.Contains(aad.Detail, "Iyengar") || pan.Status != "Passed" || pan.Provider != "DigiLocker (Income Tax Department)" {
		t.Fatalf("checks %+v %+v", aad, pan)
	}
	if !a.hasDoc("Aadhaar (DigiLocker)") || !a.hasDoc("PAN (DigiLocker)") || !a.hasDoc("Aadhaar photo (DigiLocker)") || a.hasDoc("PAN card") {
		t.Fatalf("docs %+v", a.Docs)
	}
	c, _ = o.Client(ctx, T, a.UCC)
	if c.Status != "KYC verification" {
		t.Fatalf("mismatched application activated: %s", c.Status)
	}
	queued := false
	ws, _ = o.WorkItems(ctx, T, "Open")
	for _, w := range ws {
		if w.Key == "eapply:"+a.ID && strings.Contains(w.Detail, "Aadhaar (DigiLocker): check manually") {
			queued = true
		}
	}
	if !queued {
		t.Fatal("not queued with the reason")
	}

	// An offline e-KYC file whose linked mobile and email are not the
	// applicant's (someone else's file): a person checks it.
	f.res.Method, f.res.PAN = AadhaarOffline, nil
	f.res.Aadhaar.Name, f.res.Aadhaar.Source, f.res.Aadhaar.MobileMatch, f.res.Aadhaar.EmailMatch = "Kavya Iyer", "Aadhaar offline e-KYC", "Does not match", "Does not match"
	applyMethod = AadhaarOffline
	a, err = apply(t, o, ctx, "9876500035", "ABCPI1238K", true)
	applyMethod = AadhaarDigiLocker
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range a.Checks {
		if c.Name == "Aadhaar (offline e-KYC)" && (c.Status != "Check manually" || !strings.Contains(c.Detail, "not the ones linked")) {
			t.Fatalf("someone else's file: %+v", c)
		}
	}
	if c, _ := o.Client(ctx, T, a.UCC); c.Status == "Active" {
		t.Fatal("activated with someone else's Aadhaar file")
	}
	f.res.Method = AadhaarDigiLocker
	f.res.Aadhaar.Source = "DigiLocker"
	f.res.PAN = &DLPAN{Number: "ABCPI1236K", Name: "Kavya Iyer", DOB: "1994-03-02", XML: []byte("<Certificate/>")}

	// A PAN in DigiLocker that is not the PAN given stops the application.
	f.res.Aadhaar.Name = "Kavya Iyer"
	f.res.PAN.Number = "ZZZPI9999K"
	if _, err := apply(t, o, ctx, "9876500033", "ABCPI1236K", true); err == nil || !strings.Contains(err.Error(), "is not the PAN given") {
		t.Fatalf("wrong PAN: %v", err)
	}
	// Consent refused.
	f.res.Status, f.res.Error = "Failed", "consent denied in DigiLocker"
	if _, err := apply(t, o, ctx, "9876500034", "ABCPI1237K", true); err == nil || !strings.Contains(err.Error(), "consent denied") {
		t.Fatalf("refused: %v", err)
	}
}
