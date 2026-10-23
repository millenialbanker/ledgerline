package ops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
	"unicode"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
)

// ---- DigiLocker through the KYC plugin ----
//
// With the Ledgerline KYC plugin connected (LEDGERLINE_KYC_URL), the
// applicant is sent to DigiLocker to consent, and comes back to the
// application. The plugin fetches the e-Aadhaar XML and the PAN record
// issued by the Income Tax Department, checks DigiLocker's integrity code
// on each, and Ledgerline compares them with what the applicant typed.

// DigiLockerFlow is a KYC provider that sends the applicant to DigiLocker.
type DigiLockerFlow interface {
	StartDigiLocker(ctx context.Context, ref, returnURL string, hint DigiLockerHint) (url, session string, err error)
	DigiLockerResult(ctx context.Context, session string) (DigiLockerResult, error)
}

// DigiLockerHint is what the applicant said, for the plugin to compare.
type DigiLockerHint struct {
	Name   string `json:"name,omitempty"`
	DOB    string `json:"dob,omitempty"` // YYYY-MM-DD
	PAN    string `json:"pan,omitempty"`
	Mobile string `json:"mobile,omitempty"` // confirmed by code: checked against the Aadhaar's linked mobile (offline e-KYC)
	Email  string `json:"email,omitempty"`
	// Method is how the applicant chose to share Aadhaar: AadhaarDigiLocker,
	// AadhaarOffline, or empty to choose at the plugin.
	Method string `json:"-"`
}

// How the applicant shares Aadhaar through the KYC plugin.
const (
	AadhaarDigiLocker = "digilocker"
	AadhaarOffline    = "aadhaar_offline"
)

// DigiLockerResult is what came back from DigiLocker (the plugin's
// session result).
type DigiLockerResult struct {
	ID      string     `json:"id"`
	Method  string     `json:"method"` // digilocker or aadhaar_offline, once chosen
	Status  string     `json:"status"` // Pending, Completed, Failed, Expired
	Error   string     `json:"error,omitempty"`
	Aadhaar *DLAadhaar `json:"aadhaar,omitempty"`
	PAN     *DLPAN     `json:"pan,omitempty"`
	At      string     `json:"completed_at,omitempty"`
}

// DLAadhaar is the Aadhaar the plugin returns.
type DLAadhaar struct {
	Name     string `json:"name"`
	DOB      string `json:"dob"`
	Gender   string `json:"gender"`
	Last4    string `json:"last4"`
	CareOf   string `json:"care_of"`
	Address  string `json:"address"`
	District string `json:"district"`
	State    string `json:"state"`
	PIN      string `json:"pin"`
	Photo    []byte `json:"photo,omitempty"`
	XML      []byte `json:"xml,omitempty"`
	SHA256   string `json:"sha256"`
	// Source is "DigiLocker" or "Aadhaar offline e-KYC".
	Source      string `json:"source"`
	PhotoType   string `json:"photo_type"`
	Generated   string `json:"generated"`    // when an offline e-KYC file was downloaded from UIDAI
	MobileMatch string `json:"mobile_match"` // the confirmed mobile is the Aadhaar's: Matches, Does not match
	EmailMatch  string `json:"email_match"`
}

// DLPAN is the PAN record the plugin returns.
type DLPAN struct {
	Number string `json:"number"`
	Name   string `json:"name"`
	DOB    string `json:"dob"`
	XML    []byte `json:"xml,omitempty"`
	SHA256 string `json:"sha256"`
}

// StartDigiLocker sends the applicant to share Aadhaar through the KYC
// plugin, by DigiLocker or an Aadhaar offline e-KYC file (method; empty
// lets them choose there), and returns where to send them.
func (o *Ops) StartDigiLocker(ctx context.Context, tenant, secret, returnURL, method string) (string, error) {
	a, err := o.Applicant(ctx, tenant, secret)
	if err != nil {
		return "", err
	}
	if a.Status != "Draft" || !contains(a.Steps, "identity") {
		return "", fmt.Errorf("%w: enter your PAN and identity first", ErrState)
	}
	fl, ok := o.kyc().(DigiLockerFlow)
	if !ok {
		return "", fmt.Errorf("%w: DigiLocker is not connected", ErrState)
	}
	if method != "" && method != AadhaarDigiLocker && method != AadhaarOffline {
		return "", invalid("choose DigiLocker or the Aadhaar offline e-KYC file")
	}
	h := DigiLockerHint{Name: a.Name, DOB: a.DOB, PAN: a.PAN, Method: method}
	if a.MobileOK {
		h.Mobile = a.Mobile
	}
	if a.EmailOK {
		h.Email = a.Email
	}
	url, sess, err := fl.StartDigiLocker(ctx, a.ID, returnURL, h)
	if err != nil {
		return "", fmt.Errorf("Aadhaar verification could not be started: %w", err)
	}
	a.DLSession = sess
	if err := docs.Upsert(ctx, o.Docs, tenant, cApplication, a.ID, a, "applicant"); err != nil {
		return "", err
	}
	o.log(ctx, tenant, "applicant:"+a.ID, "eapply.digilocker.started", a.ID, map[string]any{"method": firstOf(method, "applicant chooses")})
	return url, nil
}

// nameWords folds a name for comparison: upper case, letters only, words
// in order.
func nameWords(s string) []string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		if unicode.IsLetter(r) {
			b.WriteRune(r)
		} else {
			b.WriteRune(' ')
		}
	}
	return strings.Fields(b.String())
}

// NamesMatch compares two names as KYC teams do: the same words in any
// order, allowing initials for middle names ("RAVI K SHARMA" and "RAVI
// KUMAR SHARMA").
func NamesMatch(a, b string) bool {
	x, y := nameWords(a), nameWords(b)
	if len(x) == 0 || len(y) == 0 {
		return false
	}
	if len(x) > len(y) {
		x, y = y, x
	}
	used := make([]bool, len(y))
	for _, w := range x {
		found := false
		for i, v := range y {
			if used[i] {
				continue
			}
			if v == w || (len(w) == 1 && strings.HasPrefix(v, w)) || (len(v) == 1 && strings.HasPrefix(w, v)) {
				used[i], found = true, true
				break
			}
		}
		if !found {
			return false
		}
	}
	// First and last names must be whole words on both sides.
	return len(x) >= 2 || len(y) == 1
}

// Aadhaar checks and documents, by how Aadhaar was shared.
const (
	chkAadhaarDL      = "Aadhaar (DigiLocker)"
	chkAadhaarOffline = "Aadhaar (offline e-KYC)"
)

// applyDigiLocker checks the Aadhaar the applicant shared (from DigiLocker,
// with the PAN record, or an Aadhaar offline e-KYC file) against the
// application and fills the address from Aadhaar.
func (o *Ops) applyDigiLocker(ctx context.Context, tenant string, a *Application, r DigiLockerResult, now string) error {
	chk, prov := chkAadhaarDL, "DigiLocker"
	if r.Method == AadhaarOffline || (r.Aadhaar != nil && r.Aadhaar.Source == "Aadhaar offline e-KYC") {
		chk, prov = chkAadhaarOffline, "UIDAI offline e-KYC"
	}
	switch r.Status {
	case "Completed":
	case "Pending":
		return invalid("Aadhaar has not been shared yet: finish in DigiLocker or upload the offline e-KYC file, then come back")
	default:
		a.setCheck(AppCheck{Name: chk, Status: "Failed", Detail: firstOf(r.Error, "Aadhaar was not shared"), Provider: prov}, now)
		a.DLSession = ""
		return invalid("Aadhaar verification did not complete: %s. Try again", firstOf(r.Error, strings.ToLower(r.Status)))
	}
	if r.Aadhaar == nil {
		a.setCheck(AppCheck{Name: chk, Status: "Failed", Detail: "No Aadhaar in this DigiLocker account", Provider: prov}, now)
		a.DLSession = ""
		return invalid("your DigiLocker account has no Aadhaar. Link Aadhaar in DigiLocker, or use the Aadhaar offline e-KYC file instead")
	}
	ad := r.Aadhaar
	var issues []string
	if !NamesMatch(ad.Name, a.Name) {
		issues = append(issues, fmt.Sprintf("name on Aadhaar %q differs from the name given %q", ad.Name, a.Name))
	}
	if ad.DOB != "" && ad.DOB != a.DOB && !(len(ad.DOB) == 4 && strings.HasPrefix(a.DOB, ad.DOB)) {
		issues = append(issues, fmt.Sprintf("date of birth on Aadhaar %s differs from %s", ad.DOB, a.DOB))
	}
	ac := AppCheck{Name: chk, Status: "Passed", Provider: prov,
		Detail: fmt.Sprintf("e-Aadhaar XXXX XXXX %s fetched with consent; name and date of birth match; address from Aadhaar; no in-person verification needed", ad.Last4)}
	if chk == chkAadhaarOffline {
		// The file can be passed on, so it must be the applicant's: the
		// mobile or email they confirmed must be the one linked to Aadhaar.
		if ad.MobileMatch != "Matches" && ad.EmailMatch != "Matches" {
			issues = append(issues, "mobile and email confirmed are not the ones linked to this Aadhaar (mobile: "+firstOf(ad.MobileMatch, "not in the file")+", email: "+firstOf(ad.EmailMatch, "not in the file")+")")
		}
		dl := ""
		if t, err := time.Parse(time.RFC3339, ad.Generated); err == nil {
			dl = " downloaded " + t.Format("2 Jan 2006 15:04")
		}
		linked := "mobile"
		if ad.MobileMatch != "Matches" {
			linked = "email"
		}
		ac.Detail = fmt.Sprintf("UIDAI-signed offline e-KYC XXXX XXXX %s%s; signature checked; name and date of birth match; confirmed %s is linked to this Aadhaar; address from Aadhaar; no in-person verification needed", ad.Last4, dl, linked)
	}
	if len(issues) > 0 {
		ac.Status, ac.Detail = "Check manually", "Aadhaar XXXX XXXX "+ad.Last4+" received, but the "+strings.Join(issues, "; and the ")
	}
	a.setCheck(ac, now)
	other := chkAadhaarOffline
	if chk == chkAadhaarOffline {
		other = chkAadhaarDL
	}
	a.dropCheck(other) // an earlier attempt the other way no longer counts
	if ad.Address != "" {
		a.Address, a.City, a.State, a.PIN = ad.Address, firstOf(ad.District, a.City), firstOf(ad.State, a.State), firstOf(ad.PIN, a.PIN)
	}
	a.AddrSource = "DigiLocker (Aadhaar)"
	if chk == chkAadhaarOffline {
		a.AddrSource = "Aadhaar offline e-KYC"
	}
	if a.Gender == "" {
		a.Gender = ad.Gender
	}
	for _, k := range []string{"Aadhaar (DigiLocker)", "Aadhaar photo (DigiLocker)", "Aadhaar (offline e-KYC)", "Aadhaar photo (offline e-KYC)"} {
		a.dropDoc(k)
	}
	if len(ad.XML) > 0 {
		name := "e-aadhaar.xml"
		if chk == chkAadhaarOffline {
			name = "aadhaar-offline-ekyc.xml"
		}
		if err := o.addAppFile(ctx, tenant, a, chk, name, "application/xml", ad.XML, now); err != nil {
			return err
		}
	}
	if len(ad.Photo) > 0 {
		ct, name := firstOf(ad.PhotoType, "image/jpeg"), "aadhaar-photo.jpg"
		if ct == "image/jp2" {
			name = "aadhaar-photo.jp2"
		}
		kind := "Aadhaar photo (DigiLocker)"
		if chk == chkAadhaarOffline {
			kind = "Aadhaar photo (offline e-KYC)"
		}
		if err := o.addAppFile(ctx, tenant, a, kind, name, ct, ad.Photo, now); err != nil {
			return err
		}
	}
	// The PAN record issued by the Income Tax Department, when the
	// applicant's DigiLocker has it, replaces the PAN check.
	if pn := r.PAN; pn != nil && pn.Number != "" {
		pc := AppCheck{Name: "PAN", Status: "Passed", Provider: "DigiLocker (Income Tax Department)",
			Detail: fmt.Sprintf("PAN record %s issued by the Income Tax Department; name and date of birth match", MaskPAN(pn.Number))}
		switch {
		case !strings.EqualFold(pn.Number, a.PAN):
			pc.Status, pc.Detail = "Failed", fmt.Sprintf("the PAN in DigiLocker (%s) is not the PAN given (%s)", MaskPAN(pn.Number), MaskPAN(a.PAN))
		case !NamesMatch(pn.Name, a.Name):
			pc.Status, pc.Detail = "Check manually", fmt.Sprintf("name on the PAN record %q differs from the name given %q", pn.Name, a.Name)
		case pn.DOB != "" && pn.DOB != a.DOB:
			pc.Status, pc.Detail = "Check manually", fmt.Sprintf("date of birth on the PAN record %s differs from %s", pn.DOB, a.DOB)
		}
		a.setCheck(pc, now)
		if len(pn.XML) > 0 {
			if err := o.addAppFile(ctx, tenant, a, "PAN (DigiLocker)", "pan-record.xml", "application/xml", pn.XML, now); err != nil {
				return err
			}
		}
		if pc.Status == "Failed" {
			a.DLSession = ""
			return invalid("%s", pc.Detail)
		}
	}
	a.DLSession = ""
	o.log(ctx, tenant, "applicant:"+a.ID, "eapply.digilocker.completed", a.ID, map[string]any{"aadhaar": ac.Status, "via": prov, "pan_from_digilocker": r.PAN != nil})
	return nil
}

// addAppFile stores a file the system obtained for an application.
func (a *Application) dropCheck(name string) {
	out := a.Checks[:0]
	for _, c := range a.Checks {
		if c.Name != name {
			out = append(out, c)
		}
	}
	a.Checks = out
}

func (a *Application) dropDoc(kind string) {
	out := a.Docs[:0]
	for _, d := range a.Docs {
		if d.Kind != kind {
			out = append(out, d)
		}
	}
	a.Docs = out
}

func (o *Ops) addAppFile(ctx context.Context, tenant string, a *Application, kind, name, ct string, data []byte, now string) error {
	h := sha256.Sum256(data)
	sum := hex.EncodeToString(h[:])
	rf, err := o.App.Ingest.Raw.Put(ctx, ingest.RawFile{Tenant: tenant, Source: "CLIENT-DOC", Name: a.ID + " " + name, SHA256: sum, Content: data})
	if err != nil {
		return err
	}
	out := a.Docs[:0]
	for _, d := range a.Docs {
		if d.Kind != kind {
			out = append(out, d)
		}
	}
	a.Docs = append(out, AppDoc{Kind: kind, Name: name, ContentType: ct, Size: int64(len(data)), SHA256: sum, FileID: rf.ID, At: now})
	return nil
}

// ---- activating clean applications ----

// activationNeeds are the checks that must all have passed for an online
// application to be activated without a person.
var activationNeeds = []string{"PAN", "KRA search", "Aadhaar", "Bank account", "e-Sign"}

// blockers lists why an application cannot be activated by itself.
func (a Application) blockers() []string {
	var out []string
	got := map[string]string{}
	for _, c := range a.Checks {
		got[c.Name] = c.Status
		if c.Status != "Passed" {
			out = append(out, c.Name+": "+strings.ToLower(c.Status)+" ("+c.Detail+")")
		}
	}
	for _, n := range activationNeeds {
		_, ok := got[n]
		if n == "Aadhaar" { // through DigiLocker or the offline e-KYC file
			_, dl := got[chkAadhaarDL]
			_, off := got[chkAadhaarOffline]
			ok = dl || off
		}
		if !ok {
			out = append(out, n+": not done")
		}
	}
	return out
}

// autoActivate activates an online application whose every check passed,
// when the firm's activation automation is on Auto. It reports whether it
// did. The person approving is the system, recorded with every check.
func (o *Ops) autoActivate(ctx context.Context, tenant string, a Application, ucc string) bool {
	if o.manual(ctx, tenant) {
		return false
	}
	_, mode, _ := o.policy(ctx, tenant, "activation")
	if mode != ModeAuto || len(a.blockers()) > 0 {
		return false
	}
	actor := "system:autopilot"
	_, err := docs.Update(ctx, o.Docs, tenant, cClient, ucc, actor, func(c *Client) error {
		if c.Status != "KYC verification" {
			return fmt.Errorf("%w: client is %s", ErrState, c.Status)
		}
		c.Status, c.ActivatedBy = "Active", actor
		return nil
	})
	if err != nil {
		return false
	}
	var names []string
	for _, c := range a.Checks {
		names = append(names, c.Name+" ("+c.Provider+")")
	}
	o.log(ctx, tenant, actor, "client.stage", ucc, map[string]any{"stage": "Active", "application": a.ID, "checks": strings.Join(names, "; "), "how": "every online check passed"})
	return true
}

func appReceivedText(a Application, ucc, firm string, active bool) string {
	first := strings.Fields(a.Name)[0]
	if active {
		return fmt.Sprintf("Dear %s,\n\nYour trading account is open. Your client code is %s.\n\nWe verified your Aadhaar, PAN and bank account online, so no further verification is needed. Your details are being registered with the exchanges today; you can trade once that is done, usually by the next working day.\n\nA copy of the form you signed is attached to your application.\n\n%s", first, ucc, firm)
	}
	return fmt.Sprintf("Dear %s,\n\nWe have received your application %s to open a trading account. Your client code will be %s once we have verified your details; this usually takes one working day. We will write to you when the account is active.\n\nA copy of the form you signed is attached to your application.\n\n%s", first, a.ID, ucc, firm)
}
