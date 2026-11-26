package ops

import (
	"strings"
	"time"
)

// ---- online account opening for persons resident outside India ----
//
// An NRI or an OCI cardholder applies on the same page (eapply.go) by the
// PROI process (proi.go; SEBI circular HO/38/30/12(3)2026-MIRSD-POD2/I/
// 25175/2026): the mobile may be outside India (verified where feasible;
// the e-mail is verified by a one-time code, para 5.5.7); the PAN is
// checked and the KRA searched in the identity step, before any document
// is collected (para 5.2); the address step takes the passport, the OCI
// card for an OCI, and the overseas address with the document that proves
// it, instead of Aadhaar (paras 5.3-5.5). The client is created as an NRI
// with the PROI record filled from the application; staff then record the
// CKYCRR search, the video IPV and the rest on KYC trading gate → PROI
// onboarding, and the client is not activated until it is complete.

// appOverseas checks and keeps the address step of a PROI applicant.
func (o *Ops) appOverseas(a *Application, d *AppOverseas) error {
	if d == nil {
		return invalid("give your country of residence, passport and overseas address")
	}
	x := *d
	x.Country = strings.ToUpper(strings.TrimSpace(x.Country))
	x.PassportNo = strings.ToUpper(strings.TrimSpace(x.PassportNo))
	x.OCICard = strings.ToUpper(strings.TrimSpace(x.OCICard))
	x.Address = strings.Join(strings.Fields(x.Address), " ")
	x.City = strings.Join(strings.Fields(x.City), " ")
	var fe []FieldError
	add := func(f, m string) { fe = append(fe, FieldError{f, m}) }
	if !reCountry.MatchString(x.Country) || x.Country == "IN" {
		add("country", "Choose the country you live in")
	}
	if len(x.PassportNo) < 6 {
		add("passport_no", "Enter your passport number")
	}
	today := o.now().In(ist).Format("2006-01-02")
	if !validDate(x.PassportExpiry) || x.PassportExpiry < today {
		add("passport_expiry", "Enter your passport's expiry date; it must still be valid")
	}
	if a.Residence == "OCI" && x.OCICard == "" {
		add("oci_card", "Enter your OCI card number")
	}
	if len(x.Address) < 10 {
		add("address", "Enter your address where you live now, in full")
	}
	if x.City == "" {
		add("city", "Enter your city")
	}
	if !a.hasDoc(AppDocPassport) {
		add("f-pp", "Upload your passport (the pages with your photograph and address)")
	}
	if a.Residence == "OCI" && !a.hasDoc(AppDocOCI) {
		add("f-oci", "Upload your OCI card")
	}
	if !a.hasDoc(AppDocOverseas) {
		add("f-ovd", "Upload a document that proves your overseas address (driving licence, residence permit, national ID)")
	}
	if len(fe) > 0 {
		return &ValidationError{fe}
	}
	a.Overseas, a.AddrSource = &x, "Overseas (passport and address proof)"
	a.Address, a.City, a.State, a.PIN = x.Address, x.City, "", ""
	return nil
}

// appPROI is the PROI record a PROI application gives the new client.
func (o *Ops) appPROI(a Application) *PROIKYC {
	day := func(name string) string {
		for _, c := range a.Checks {
			if c.Name == name && len(c.At) >= 10 {
				return c.At[:10]
			}
		}
		return ""
	}
	docsOn := ""
	for _, d := range a.Docs {
		if (d.Kind == AppDocPassport || d.Kind == AppDocOCI || d.Kind == AppDocOverseas) && len(d.At) >= 10 && (docsOn == "" || d.At[:10] < docsOn) {
			docsOn = d.At[:10]
		}
	}
	p := &PROIKYC{Category: a.Residence, KRALookupOn: day("KRA search"), DocsOn: docsOn, EmailVerified: a.EmailOK, MobileVerified: a.MobileOK,
		PoI: "Passport (uploaded online)", PoA: "Overseas address proof (uploaded online)", PassportCopy: "Online application " + a.ID,
		By: "applicant:" + a.ID, At: o.now().Format(time.RFC3339)}
	for _, c := range a.Checks {
		if c.Name == "PAN" && c.Status == "Passed" && len(c.At) >= 10 {
			p.PANVerifiedOn = c.At[:10]
		}
	}
	if x := a.Overseas; x != nil {
		p.Country, p.PassportNo, p.PassportExpiry, p.OCICard, p.OverseasAddress, p.OverseasOVD = x.Country, x.PassportNo, x.PassportExpiry, x.OCICard, x.Address, "Uploaded online ("+AppDocOverseas+")"
	}
	return p
}
