package ops

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html/template"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
)

// ---- online account opening (e-KYC) ----
//
// An individual opens an account from a link, on a phone, without visiting
// the office: mobile and email confirmed by one-time codes; PAN checked and
// the KRA searched; address from DigiLocker or a proof; bank account
// confirmed by a penny drop; trading profile, nominee, photograph,
// signature and a short video for in-person verification; then the
// account-opening form signed by Aadhaar e-Sign. The application lands in
// Clients & onboarding at KYC verification with every check and document
// attached, and goes through the same stages, with a checker activating it.
//
// The checks run through a KYC provider. Until one is connected each check
// is marked "Check manually" and staff verify it on the application; on a
// development server a simulated provider answers.

const cApplication = "eapply"

// Application is one online account-opening application.
type Application struct {
	ID      string `json:"id"`
	Token   string `json:"token_hash,omitempty"` // stored hash; never returned
	Status  string `json:"status"`               // Draft, Submitted, Converted, Rejected
	UCC     string `json:"ucc,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Created string `json:"created"`
	Updated string `json:"updated"`
	IP      string `json:"ip,omitempty"`
	Partner string `json:"partner,omitempty"` // authorised person code from the link
	Lead    string `json:"lead,omitempty"`
	// Activated is set when Autopilot opened the account on submission.
	Activated bool `json:"activated,omitempty"`

	Mobile   string             `json:"mobile"`
	Email    string             `json:"email"`
	MobileOK bool               `json:"mobile_ok"`
	EmailOK  bool               `json:"email_ok"`
	Codes    map[string]appCode `json:"codes,omitempty"`

	Name       string `json:"name,omitempty"`
	PAN        string `json:"pan,omitempty"`
	DOB        string `json:"dob,omitempty"`
	Gender     string `json:"gender,omitempty"`
	Father     string `json:"father,omitempty"` // father's or spouse's name
	Marital    string `json:"marital,omitempty"`
	Address    string `json:"address,omitempty"`
	City       string `json:"city,omitempty"`
	State      string `json:"state,omitempty"`
	PIN        string `json:"pin,omitempty"`
	AddrSource string `json:"addr_source,omitempty"` // DigiLocker, Uploaded proof

	Bank    string `json:"bank,omitempty"`
	IFSC    string `json:"ifsc,omitempty"`
	Account string `json:"account,omitempty"`

	Segments   []string `json:"segments,omitempty"`
	Income     string   `json:"income,omitempty"`
	Occupation string   `json:"occupation,omitempty"`
	Experience string   `json:"experience,omitempty"`
	RAS        string   `json:"ras,omitempty"`
	Depository string   `json:"depository,omitempty"`
	DematID    string   `json:"demat_id,omitempty"`

	NomineeOptOut bool   `json:"nominee_opt_out,omitempty"`
	NomineeName   string `json:"nominee_name,omitempty"`
	NomineeRel    string `json:"nominee_relation,omitempty"`
	// Nominees: up to 10, each with share and identity (SEBI 2025 rules).
	Nominees []Nominee `json:"nominees,omitempty"`

	Declarations map[string]bool `json:"declarations,omitempty"`
	WhatsApp     bool            `json:"whatsapp,omitempty"`

	Docs   []AppDoc   `json:"docs"`
	Checks []AppCheck `json:"checks"`
	Steps  []string   `json:"steps"` // steps completed
	// DLSession is the DigiLocker session at the KYC plugin, while the
	// applicant is away giving consent.
	DLSession string `json:"dl_session,omitempty"`

	FormHash string `json:"form_hash,omitempty"`
	ESigned  string `json:"esigned,omitempty"`
	ESignRef string `json:"esign_ref,omitempty"`
}

type appCode struct {
	Hash    string `json:"hash"`
	Expires string `json:"expires"`
	Tries   int    `json:"tries"`
}

// AppDoc is a file the applicant gave.
type AppDoc struct {
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
	FileID      string `json:"file_id"`
	At          string `json:"at"`
}

// AppCheck is one verification and its result.
type AppCheck struct {
	Name     string `json:"name"`
	Status   string `json:"status"` // Passed, Failed, Check manually
	Detail   string `json:"detail"`
	Provider string `json:"provider"`
	At       string `json:"at"`
}

// Application steps in order.
// There is no in-person verification step: Aadhaar and PAN come from
// DigiLocker with the applicant's consent, and SEBI's KYC circular
// (SEBI/HO/MIRSD/DOP/CIR/P/2020/73) does not require IPV or VIPV when the
// KYC is completed through Aadhaar authentication or documents fetched
// from DigiLocker.
var AppSteps = []string{"contact", "identity", "address", "bank", "profile", "nominee", "photo", "review"}

// AppDeclarations must all be accepted before signing.
var AppDeclarations = map[string]string{
	"rdd":     "I have read the risk disclosure document and, for derivatives, the risk statement.",
	"rights":  "I have read the rights and obligations of the stock broker and the client.",
	"tariff":  "I accept the tariff sheet.",
	"ras":     "I authorise settlement of my running account at the frequency I chose.",
	"kra":     "I consent to my KYC records being fetched from and uploaded to the KRA and CKYC registry.",
	"correct": "The information I have given is true and complete.",
}

// ---- providers ----

// KYCProvider runs the checks of an online application.
type KYCProvider interface {
	Name() string
	PAN(pan, name, dob string) AppCheck
	KRA(pan string) AppCheck
	DigiLocker(a *Application) (AppCheck, bool)
	PennyDrop(account, ifsc, name string) AppCheck
	FaceMatch(photo, panImage []byte) AppCheck
	ESign(formHash, name string) AppCheck
}

type manualKYC struct{}

func (manualKYC) Name() string { return "Not connected" }
func (manualKYC) manual(what string) AppCheck {
	return AppCheck{Name: what, Status: "Check manually", Detail: "No KYC provider is connected; verify from the documents.", Provider: "Not connected"}
}
func (m manualKYC) PAN(string, string, string) AppCheck { return m.manual("PAN") }
func (m manualKYC) KRA(string) AppCheck                 { return m.manual("KRA search") }
func (m manualKYC) DigiLocker(*Application) (AppCheck, bool) {
	return m.manual("Aadhaar (DigiLocker)"), false
}
func (m manualKYC) PennyDrop(string, string, string) AppCheck { return m.manual("Bank account") }
func (m manualKYC) FaceMatch([]byte, []byte) AppCheck         { return m.manual("Face match") }
func (m manualKYC) ESign(string, string) AppCheck {
	return AppCheck{Name: "e-Sign", Status: "Check manually", Detail: "Signed with a one-time code to the verified mobile; Aadhaar e-Sign needs a provider.", Provider: "Not connected"}
}

// simKYC answers like a provider would, for development and demos.
type simKYC struct{}

func (simKYC) Name() string { return "Simulated" }
func (simKYC) PAN(pan, name, dob string) AppCheck {
	c := AppCheck{Name: "PAN", Provider: "Simulated"}
	if strings.HasPrefix(pan, "ZZ") {
		c.Status, c.Detail = "Failed", "PAN not found in the income-tax database"
		return c
	}
	c.Status, c.Detail = "Passed", fmt.Sprintf("PAN active; name as per PAN %s; date of birth matches; Aadhaar linked", strings.ToUpper(name))
	return c
}
func (simKYC) KRA(pan string) AppCheck {
	if pan[len(pan)-2] < 'M' {
		return AppCheck{Name: "KRA search", Status: "Passed", Detail: "KYC registered with a KRA (status Validated); details will be fetched and compared", Provider: "Simulated"}
	}
	return AppCheck{Name: "KRA search", Status: "Passed", Detail: "No KYC on record; a new KYC will be uploaded to the KRA", Provider: "Simulated"}
}
func (simKYC) DigiLocker(a *Application) (AppCheck, bool) {
	a.Address, a.City, a.State, a.PIN = "12, 4th Cross, Indiranagar", "Bengaluru", "Karnataka", "560038"
	return AppCheck{Name: "Aadhaar (DigiLocker)", Status: "Passed", Detail: "e-Aadhaar fetched with consent: name and date of birth match; address taken from Aadhaar; no in-person verification needed", Provider: "Simulated"}, true
}
func (simKYC) PennyDrop(account, ifsc, name string) AppCheck {
	if strings.HasSuffix(account, "0000") {
		return AppCheck{Name: "Bank account", Status: "Failed", Detail: "₹1 credit failed: account closed or invalid", Provider: "Simulated"}
	}
	return AppCheck{Name: "Bank account", Status: "Passed", Detail: fmt.Sprintf("₹1 credited; name at bank %s (match 96%%)", strings.ToUpper(name)), Provider: "Simulated"}
}
func (simKYC) FaceMatch(photo, pan []byte) AppCheck {
	if len(photo) == 0 {
		return AppCheck{Name: "Face match", Status: "Failed", Detail: "No photograph", Provider: "Simulated"}
	}
	return AppCheck{Name: "Face match", Status: "Passed", Detail: "Live photograph matches the PAN image (score 91); liveness passed", Provider: "Simulated"}
}
func (simKYC) ESign(h, name string) AppCheck {
	return AppCheck{Name: "e-Sign", Status: "Passed", Detail: "Aadhaar e-Sign completed; signer name matches; document hash " + h[:16], Provider: "Simulated"}
}

func (o *Ops) kyc() KYCProvider {
	if o.KYC != nil {
		return o.KYC
	}
	if o.DevCodes {
		return simKYC{}
	}
	return manualKYC{}
}

// ---- the applicant's side ----

// AppConfig is what the public page needs before starting.
type AppConfig struct {
	Firm     string            `json:"firm"`
	SEBIReg  string            `json:"sebi_reg"`
	Email    string            `json:"compliance_email,omitempty"`
	Provider string            `json:"provider"`
	Dev      bool              `json:"dev"`
	Segments map[string]string `json:"segments"`
	Declare  map[string]string `json:"declarations"`
	Steps    []string          `json:"steps"`
	DocKinds []string          `json:"doc_kinds"`
	// DigiLockerRedirect: the applicant is sent to DigiLocker and back
	// (the KYC plugin is connected).
	DigiLockerRedirect bool `json:"digilocker_redirect"`
	// AadhaarMethods are the ways the applicant can share Aadhaar:
	// "digilocker", and "aadhaar_offline" when the KYC plugin offers it.
	AadhaarMethods []string `json:"aadhaar_methods"`
}

// ApplyConfig describes the firm for the public page.
func (o *Ops) ApplyConfig(ctx context.Context, tenant string) AppConfig {
	set, _ := o.Settings(ctx, tenant)
	return AppConfig{Firm: set.Firm, SEBIReg: set.SEBIReg, Email: set.ComplianceEmail, Provider: o.kycName(ctx, tenant), Dev: o.DevCodes, Segments: SegmentNames, Declare: AppDeclarations, Steps: AppSteps,
		DocKinds: []string{"PAN card", "Photograph", "Signature", "Income proof", "Bank proof"}, DigiLockerRedirect: isFlow(o.kyc()), AadhaarMethods: o.aadhaarMethods(ctx)}
}

// aadhaarMethods asks the KYC plugin which ways it offers; other
// providers have DigiLocker only.
func (o *Ops) aadhaarMethods(ctx context.Context) []string {
	if m, ok := o.kyc().(interface {
		Methods(context.Context) []string
	}); ok {
		if ms := m.Methods(ctx); len(ms) > 0 {
			return ms
		}
	}
	return []string{AadhaarDigiLocker}
}

func isFlow(p KYCProvider) bool { _, ok := p.(DigiLockerFlow); return ok }

func tokenHash(id, tok string) string {
	h := sha256.Sum256([]byte(id + "|" + tok))
	return hex.EncodeToString(h[:])
}

// StartApplication begins an application and sends codes to the mobile and
// email. It returns the application and the secret the page keeps.
func (o *Ops) StartApplication(ctx context.Context, tenant, mobile, email, partner, lead, ip string) (Application, string, map[string]string, error) {
	mobile = strings.TrimPrefix(strings.TrimSpace(mobile), "+91")
	email = strings.ToLower(strings.TrimSpace(email))
	if !reMobile.MatchString(mobile) || !reEmail.MatchString(email) {
		return Application{}, "", nil, invalid("enter a 10-digit Indian mobile number and a valid email address")
	}
	today := o.now().Format("2006-01-02")
	all, _ := docs.List[Application](ctx, o.Docs, tenant, cApplication, "")
	n := 0
	for _, a := range all {
		if a.Mobile == mobile && strings.HasPrefix(a.Created, today) {
			n++
		}
	}
	if n >= 3 {
		return Application{}, "", nil, invalid("too many applications from this mobile number today; continue the one already started or try tomorrow")
	}
	tok, err := newToken()
	if err != nil {
		return Application{}, "", nil, err
	}
	seq, err := o.next(ctx, tenant, "eapply", "applicant")
	if err != nil {
		return Application{}, "", nil, err
	}
	now := o.now().Format(time.RFC3339)
	a := Application{ID: fmt.Sprintf("APP-%06d", seq), Status: "Draft", Created: now, Updated: now, IP: ip, Mobile: mobile, Email: email, Docs: []AppDoc{}, Checks: []AppCheck{}, Steps: []string{}, Declarations: map[string]bool{}, Codes: map[string]appCode{}}
	a.Token = tokenHash(a.ID, tok)
	if p, _, err := docs.Get[Partner](ctx, o.Docs, tenant, cPartner, strings.ToUpper(partner)); err == nil && p.Active {
		a.Partner = p.Code
	}
	if lead != "" {
		if _, _, err := docs.Get[Lead](ctx, o.Docs, tenant, cLead, lead); err == nil {
			a.Lead = lead
		}
	}
	dev := map[string]string{}
	for _, ch := range []string{"mobile", "email"} {
		dev[ch] = o.sendAppCode(ctx, tenant, &a, ch)
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cApplication, a.ID, a, "applicant"); err != nil {
		return a, "", nil, err
	}
	o.log(ctx, tenant, "applicant", "eapply.started", a.ID, map[string]any{"mobile": maskMobile(mobile), "partner": a.Partner})
	if !o.DevCodes {
		dev = nil
	}
	return a, a.ID + "." + tok, dev, nil
}

func (o *Ops) sendAppCode(ctx context.Context, tenant string, a *Application, ch string) string {
	code := otpCode()
	if a.Codes == nil {
		a.Codes = map[string]appCode{}
	}
	a.Codes[ch] = appCode{Hash: hashOTP(a.ID+ch, code), Expires: o.now().Add(10 * time.Minute).Format(time.RFC3339)}
	set, _ := o.Settings(ctx, tenant)
	if ch == "mobile" {
		msg := "%s is your code to open an account with %s. Valid for 10 minutes. Do not share it."
		o.sendDirectAs(ctx, tenant, "applicant", ChSMS, a.Mobile, "", "eapply.code", "", fmt.Sprintf(msg, code, set.Firm), fmt.Sprintf(msg, "••••••", set.Firm))
	} else {
		o.sendDirect(ctx, tenant, "applicant", ChEmail, a.Email, "", "eapply.code", "Your code to open an account: "+code, fmt.Sprintf("Your code to open an account with %s is %s. It is valid for 10 minutes.\n\nIf you did not ask for it, please ignore this email.\n\n%s", set.Firm, code, set.Firm))
	}
	return code
}

// Applicant finds the application a page's secret belongs to.
func (o *Ops) Applicant(ctx context.Context, tenant, secret string) (Application, error) {
	id, tok, ok := strings.Cut(secret, ".")
	if !ok {
		return Application{}, ErrForbidden
	}
	a, _, err := docs.Get[Application](ctx, o.Docs, tenant, cApplication, id)
	if err != nil || !hmac.Equal([]byte(a.Token), []byte(tokenHash(id, tok))) {
		return Application{}, ErrForbidden
	}
	if t, err := time.Parse(time.RFC3339, a.Created); err == nil && o.now().Sub(t) > 30*24*time.Hour && a.Status == "Draft" {
		return Application{}, fmt.Errorf("%w: this application has expired; please start again", ErrState)
	}
	return a, nil
}

func (a *Application) done(step string) {
	if !contains(a.Steps, step) {
		a.Steps = append(a.Steps, step)
	}
}

func (a *Application) setCheck(c AppCheck, at string) {
	c.At = at
	for i := range a.Checks {
		if a.Checks[i].Name == c.Name {
			a.Checks[i] = c
			return
		}
	}
	a.Checks = append(a.Checks, c)
}

func (a *Application) hasDoc(kind string) bool {
	for _, d := range a.Docs {
		if d.Kind == kind {
			return true
		}
	}
	return false
}

// Public is the application as the applicant's page sees it.
func (a Application) Public() Application {
	a.Token, a.Codes, a.IP = "", nil, ""
	return a
}

// VerifyAppCode confirms the mobile or email code.
func (o *Ops) VerifyAppCode(ctx context.Context, tenant, secret, ch, code string) (Application, error) {
	a, err := o.Applicant(ctx, tenant, secret)
	if err != nil {
		return a, err
	}
	if a.Status != "Draft" {
		return a, fmt.Errorf("%w: application already %s", ErrState, strings.ToLower(a.Status))
	}
	if ch == "resend" {
		for _, c := range []string{"mobile", "email"} {
			if (c == "mobile" && !a.MobileOK) || (c == "email" && !a.EmailOK) {
				o.sendAppCode(ctx, tenant, &a, c)
			}
		}
		return a, docs.Upsert(ctx, o.Docs, tenant, cApplication, a.ID, a, "applicant")
	}
	c, ok := a.Codes[ch]
	if !ok {
		return a, invalid("ask for a new code")
	}
	exp, _ := time.Parse(time.RFC3339, c.Expires)
	switch {
	case o.now().After(exp) || c.Tries >= 5:
		delete(a.Codes, ch)
		err = invalid("that code has expired; ask for a new one")
	case !hmac.Equal([]byte(hashOTP(a.ID+ch, strings.TrimSpace(code))), []byte(c.Hash)):
		c.Tries++
		a.Codes[ch] = c
		err = invalid("that code is not right")
	default:
		delete(a.Codes, ch)
		if ch == "mobile" {
			a.MobileOK = true
		} else {
			a.EmailOK = true
		}
		if a.MobileOK && a.EmailOK {
			a.done("contact")
		}
	}
	a.Updated = o.now().Format(time.RFC3339)
	if uerr := docs.Upsert(ctx, o.Docs, tenant, cApplication, a.ID, a, "applicant"); uerr != nil {
		return a, uerr
	}
	return a, err
}

// AppStep is the data of one step.
type AppStep struct {
	Step string `json:"step"`
	Application
	DigiLocker bool `json:"digilocker,omitempty"`
}

var rePIN = regexp.MustCompile(`^[1-9][0-9]{5}$`)

// SaveAppStep records a step and runs its checks.
func (o *Ops) SaveAppStep(ctx context.Context, tenant, secret string, s AppStep) (Application, error) {
	a, err := o.Applicant(ctx, tenant, secret)
	if err != nil {
		return a, err
	}
	if a.Status != "Draft" {
		return a, fmt.Errorf("%w: application already %s", ErrState, strings.ToLower(a.Status))
	}
	if !a.MobileOK || !a.EmailOK {
		return a, invalid("confirm your mobile and email first")
	}
	p, now := o.kyc(), o.now().Format(time.RFC3339)
	d := s.Application
	var fe []FieldError
	add := func(f, m string) { fe = append(fe, FieldError{f, m}) }
	switch s.Step {
	case "identity":
		d.PAN, d.Name = strings.ToUpper(strings.TrimSpace(d.PAN)), strings.Join(strings.Fields(d.Name), " ")
		if !rePAN.MatchString(d.PAN) || d.PAN[3] != 'P' {
			add("pan", "Enter your individual PAN: 5 letters, 4 digits, 1 letter, with P as the 4th")
		}
		if len(d.Name) < 3 {
			add("name", "Enter your name exactly as on your PAN")
		}
		if t, err := time.Parse("2006-01-02", d.DOB); err != nil || o.now().Sub(t) < 18*365*24*time.Hour {
			add("dob", "You must be 18 or older to open an account online")
		}
		if !contains([]string{"Male", "Female", "Transgender"}, d.Gender) {
			add("gender", "Choose a gender")
		}
		if len(strings.TrimSpace(d.Father)) < 3 {
			add("father", "Enter your father's or spouse's name")
		}
		if len(fe) > 0 {
			return a, &ValidationError{fe}
		}
		cs, _ := o.Clients(ctx, tenant)
		for _, c := range cs {
			if c.PAN == d.PAN {
				return a, &ValidationError{[]FieldError{{"pan", "An account with this PAN already exists. Please contact us."}}}
			}
		}
		if _, hit := o.IsDebarredPAN(ctx, tenant, d.PAN, now[:10]); hit {
			o.log(ctx, tenant, "applicant", "eapply.debarred", a.ID, nil)
			return a, &ValidationError{[]FieldError{{"pan", "We cannot open an account for this PAN. Please contact us."}}}
		}
		a.PAN, a.Name, a.DOB, a.Gender, a.Father, a.Marital = d.PAN, d.Name, d.DOB, d.Gender, strings.TrimSpace(d.Father), d.Marital
		pc := o.checkPAN(ctx, tenant, p, a.PAN, a.Name, a.DOB)
		a.setCheck(pc, now)
		a.setCheck(p.KRA(a.PAN), now)
		if pc.Status == "Failed" {
			_ = docs.Upsert(ctx, o.Docs, tenant, cApplication, a.ID, a, "applicant")
			return a, &ValidationError{[]FieldError{{"pan", pc.Detail}}}
		}
	case "address":
		// Aadhaar (with the address) comes from DigiLocker, with the PAN
		// record, or from an Aadhaar offline e-KYC file: through the KYC
		// plugin when it is connected (the applicant has been there and back;
		// see StartDigiLocker), or from the provider directly.
		if fl, ok := p.(DigiLockerFlow); ok {
			if a.DLSession == "" {
				return a, invalid("share your Aadhaar first: choose DigiLocker or the Aadhaar offline e-KYC file")
			}
			r, err := fl.DigiLockerResult(ctx, a.DLSession)
			if err != nil {
				return a, invalid("your Aadhaar could not be read: %v", err)
			}
			if err := o.applyDigiLocker(ctx, tenant, &a, r, now); err != nil {
				_ = docs.Upsert(ctx, o.Docs, tenant, cApplication, a.ID, a, "applicant")
				return a, err
			}
			break
		}
		c, ok := p.DigiLocker(&a)
		a.setCheck(c, now)
		if !ok {
			return a, invalid("online account opening needs DigiLocker, which is not connected yet. Please contact us to open your account")
		}
		a.AddrSource = "DigiLocker"
	case "bank":
		d.IFSC = strings.ToUpper(strings.TrimSpace(d.IFSC))
		d.Account = strings.TrimSpace(d.Account)
		if strings.TrimSpace(d.Bank) == "" {
			add("bank", "Enter the bank name")
		}
		if !reIFSC.MatchString(d.IFSC) {
			add("ifsc", "IFSC is 4 letters, 0, then 6 characters")
		}
		if !reAcct.MatchString(d.Account) {
			add("account", "Account number is 9 to 18 digits")
		}
		if len(fe) > 0 {
			return a, &ValidationError{fe}
		}
		a.Bank, a.IFSC, a.Account = strings.TrimSpace(d.Bank), d.IFSC, d.Account
		c := o.checkBank(ctx, tenant, p, a.Account, a.IFSC, a.Name)
		a.setCheck(c, now)
		if c.Status == "Failed" {
			_ = docs.Upsert(ctx, o.Docs, tenant, cApplication, a.ID, a, "applicant")
			return a, &ValidationError{[]FieldError{{"account", c.Detail}}}
		}
	case "profile":
		c := Client{Segments: d.Segments}
		c.Normalise()
		if len(c.Segments) == 0 {
			add("segments", "Choose at least one segment")
		}
		if d.Income == "" || d.Occupation == "" {
			add("income", "Choose your income range and occupation")
		}
		if d.RAS != "Monthly" && d.RAS != "Quarterly" {
			add("ras", "Choose monthly or quarterly settlement")
		}
		for _, sg := range c.Segments {
			if sg != "CM" && !a.hasDoc("Income proof") {
				add("income_proof", "Derivatives need an income proof: a recent salary slip, ITR acknowledgement or 6-month bank statement")
				break
			}
		}
		if d.DematID != "" && !regexp.MustCompile(`^(IN[0-9A-Z]{14}|[0-9]{16})$`).MatchString(strings.ToUpper(d.DematID)) {
			add("demat_id", "Demat account is 16 characters: IN followed by 14 for NSDL, or 16 digits for CDSL")
		}
		if len(fe) > 0 {
			return a, &ValidationError{fe}
		}
		a.Segments, a.Income, a.Occupation, a.Experience, a.RAS, a.DematID = c.Segments, d.Income, d.Occupation, d.Experience, d.RAS, strings.ToUpper(d.DematID)
		a.Depository = "CDSL"
		if strings.HasPrefix(a.DematID, "IN") {
			a.Depository = "NSDL"
		}
	case "nominee":
		if d.NomineeOptOut {
			a.NomineeOptOut, a.NomineeName, a.NomineeRel, a.Nominees = true, "", "", nil
			break
		}
		if len(d.Nominees) == 0 {
			return a, invalid("add a nominee (name, relationship and one identity detail), or choose not to nominate")
		}
		c := Client{Nominees: d.Nominees}
		normaliseNominees(&c)
		if fe := validateNominees(c.Nominees, o.now()); len(fe) > 0 {
			return a, &ValidationError{fe}
		}
		a.NomineeOptOut, a.Nominees, a.NomineeName, a.NomineeRel = false, c.Nominees, c.NomineeName, c.Nominees[0].Relation
	case "photo":
		panDL := a.hasDoc("PAN (DigiLocker)")
		if !a.hasDoc("Photograph") || !a.hasDoc("Signature") || (!panDL && !a.hasDoc("PAN card")) {
			if panDL {
				return a, invalid("take a photograph and sign")
			}
			return a, invalid("take a photograph, sign, and upload your PAN card")
		}
		// The live photograph is compared with the Aadhaar photograph (from
		// DigiLocker or the offline e-KYC file), or the PAN card image.
		ph, _ := o.appDocBytes(ctx, tenant, a, "Photograph")
		var ref []byte
		for _, k := range []string{"Aadhaar photo (DigiLocker)", "Aadhaar photo (offline e-KYC)", "PAN card"} {
			if ref, _ = o.appDocBytes(ctx, tenant, a, k); len(ref) > 0 {
				break
			}
		}
		a.setCheck(p.FaceMatch(ph, ref), now)
	default:
		return a, invalid("unknown step")
	}
	a.done(s.Step)
	a.Updated = now
	return a, docs.Upsert(ctx, o.Docs, tenant, cApplication, a.ID, a, "applicant")
}

// Allowed file types for applicant uploads.
var appDocTypes = map[string]bool{"application/pdf": true, "image/jpeg": true, "image/png": true}

// AddAppDoc stores a file the applicant gives. A newer file of the same
// kind replaces the earlier one.
func (o *Ops) AddAppDoc(ctx context.Context, tenant, secret, kind, name string, data []byte) (Application, error) {
	a, err := o.Applicant(ctx, tenant, secret)
	if err != nil {
		return a, err
	}
	if a.Status != "Draft" || !a.MobileOK || !a.EmailOK {
		return a, fmt.Errorf("%w: confirm your mobile and email first", ErrState)
	}
	if !contains(o.ApplyConfig(ctx, tenant).DocKinds, kind) {
		return a, invalid("unknown document kind")
	}
	limit := MaxDocBytes
	ct := http.DetectContentType(data)
	if len(data) == 0 || len(data) > limit {
		return a, invalid("files must be under %d MB", limit>>20)
	}
	if !appDocTypes[ct] {
		return a, invalid("upload a PDF, JPEG or PNG (this looks like %s)", ct)
	}
	sum := sha256.Sum256(data)
	rf, err := o.App.Ingest.Raw.Put(ctx, ingest.RawFile{Tenant: tenant, Source: "CLIENT-DOC", Name: a.ID + " " + name, SHA256: hex.EncodeToString(sum[:]), Content: data})
	if err != nil {
		return a, err
	}
	var keep []AppDoc
	for _, d := range a.Docs {
		if d.Kind != kind {
			keep = append(keep, d)
		}
	}
	a.Docs = append(keep, AppDoc{Kind: kind, Name: name, ContentType: ct, Size: int64(len(data)), SHA256: rf.SHA256, FileID: rf.ID, At: o.now().Format(time.RFC3339)})
	a.Updated = o.now().Format(time.RFC3339)
	return a, docs.Upsert(ctx, o.Docs, tenant, cApplication, a.ID, a, "applicant")
}

func (o *Ops) appDocBytes(ctx context.Context, tenant string, a Application, kind string) ([]byte, error) {
	for _, d := range a.Docs {
		if d.Kind == kind {
			rf, err := o.App.Ingest.Raw.Get(ctx, tenant, d.FileID)
			return rf.Content, err
		}
	}
	return nil, ErrNotFound
}

// ---- signing and submitting ----

// AppForm renders the account-opening form being signed.
func (o *Ops) AppForm(ctx context.Context, tenant string, a Application) ([]byte, error) {
	set, _ := o.Settings(ctx, tenant)
	segs := []string{}
	for _, s := range a.Segments {
		segs = append(segs, SegmentNames[s])
	}
	decl := []string{}
	for _, k := range sortedKeys(AppDeclarations) {
		if a.Declarations[k] {
			decl = append(decl, AppDeclarations[k])
		}
	}
	var b bytes.Buffer
	err := appFormTmpl.Execute(&b, map[string]any{"A": a, "Firm": set.Firm, "SEBI": set.SEBIReg, "Segments": strings.Join(segs, ", "), "Decl": decl, "PANMasked": MaskPAN(a.PAN), "AcctMasked": MaskAccount(a.Account)})
	return b.Bytes(), err
}

var appFormTmpl = template.Must(template.New("form").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><title>Account opening form {{.A.ID}}</title>
<style>body{font:13px/1.5 system-ui,sans-serif;color:#16201B;margin:28px;max-width:820px}h1{font-size:18px;margin:0}h2{font-size:14px;margin:20px 0 6px}table{width:100%;border-collapse:collapse}td{padding:5px 6px;border-bottom:1px solid #ccc;vertical-align:top}td:first-child{width:38%;color:#525d57}.small{font-size:11px;color:#525d57}</style></head><body>
<h1>Account opening form · {{.Firm}}</h1><p class="small">SEBI registration {{.SEBI}} · application {{.A.ID}} · individual, opened online</p>
<h2>Identity</h2><table><tr><td>Name as per PAN</td><td>{{.A.Name}}</td></tr><tr><td>PAN</td><td>{{.A.PAN}}</td></tr><tr><td>Date of birth</td><td>{{.A.DOB}}</td></tr><tr><td>Gender</td><td>{{.A.Gender}}</td></tr><tr><td>Father's / spouse's name</td><td>{{.A.Father}}</td></tr><tr><td>Marital status</td><td>{{.A.Marital}}</td></tr></table>
<h2>Contact and address</h2><table><tr><td>Mobile (verified)</td><td>{{.A.Mobile}}</td></tr><tr><td>Email (verified)</td><td>{{.A.Email}}</td></tr><tr><td>Address ({{.A.AddrSource}})</td><td>{{.A.Address}}, {{.A.City}}, {{.A.State}} {{.A.PIN}}</td></tr></table>
<h2>Bank and demat</h2><table><tr><td>Bank</td><td>{{.A.Bank}} · {{.A.IFSC}} · {{.AcctMasked}}</td></tr><tr><td>Demat account</td><td>{{if .A.DematID}}{{.A.Depository}} {{.A.DematID}}{{else}}To be opened with us{{end}}</td></tr></table>
<h2>Trading</h2><table><tr><td>Segments</td><td>{{.Segments}}</td></tr><tr><td>Annual income</td><td>{{.A.Income}}</td></tr><tr><td>Occupation</td><td>{{.A.Occupation}}</td></tr><tr><td>Trading experience</td><td>{{.A.Experience}}</td></tr><tr><td>Running account settlement</td><td>{{.A.RAS}}</td></tr></table>
<h2>Nomination</h2><table>{{if .A.NomineeOptOut}}<tr><td>Nominee</td><td>I do not wish to nominate</td></tr>{{else}}{{range $n := .A.Nominees}}<tr><td>Nominee</td><td>{{$n.Name}} ({{$n.Relation}}), {{$n.Share}}% · {{$n.IDType}} {{$n.MaskedID}}{{if $n.Guardian}} · minor, guardian {{$n.Guardian}} ({{$n.GuardianRelation}}){{end}}</td></tr>{{end}}{{end}}</table>
<h2>Declarations</h2><ul>{{range .Decl}}<li>{{.}}</li>{{end}}</ul>
<p class="small">Signed electronically by {{.A.Name}}. The document hash is recorded with the signature.</p></body></html>`))

// SignApplication accepts the declarations and signs the form. The applicant
// confirms with a code sent to the verified mobile (the Aadhaar e-Sign OTP
// when a provider is connected).
func (o *Ops) SignApplication(ctx context.Context, tenant, secret string, declarations map[string]bool, whatsapp bool, code string) (Application, map[string]string, error) {
	a, err := o.Applicant(ctx, tenant, secret)
	if err != nil {
		return a, nil, err
	}
	if a.Status != "Draft" {
		return a, nil, fmt.Errorf("%w: application already %s", ErrState, strings.ToLower(a.Status))
	}
	var missing []string
	for _, s := range AppSteps[:len(AppSteps)-1] {
		if !contains(a.Steps, s) {
			missing = append(missing, s)
		}
	}
	if len(missing) > 0 {
		return a, nil, invalid("finish these steps first: %s", strings.Join(missing, ", "))
	}
	for k := range AppDeclarations {
		if !declarations[k] {
			return a, nil, invalid("accept every declaration to sign")
		}
	}
	a.Declarations, a.WhatsApp = declarations, whatsapp
	if code == "" {
		dev := map[string]string{"esign": o.sendAppCode(ctx, tenant, &a, "esign")}
		if !o.DevCodes {
			dev = nil
		}
		return a, dev, docs.Upsert(ctx, o.Docs, tenant, cApplication, a.ID, a, "applicant")
	}
	c, ok := a.Codes["esign"]
	exp, _ := time.Parse(time.RFC3339, c.Expires)
	if !ok || o.now().After(exp) || c.Tries >= 5 {
		return a, nil, invalid("ask for a new signing code")
	}
	if !hmac.Equal([]byte(hashOTP(a.ID+"esign", strings.TrimSpace(code))), []byte(c.Hash)) {
		c.Tries++
		a.Codes["esign"] = c
		_ = docs.Upsert(ctx, o.Docs, tenant, cApplication, a.ID, a, "applicant")
		return a, nil, invalid("that code is not right")
	}
	delete(a.Codes, "esign")
	form, err := o.AppForm(ctx, tenant, a)
	if err != nil {
		return a, nil, err
	}
	h := sha256.Sum256(form)
	a.FormHash = hex.EncodeToString(h[:])
	now := o.now().Format(time.RFC3339)
	sc := o.kyc().ESign(a.FormHash, a.Name)
	a.setCheck(sc, now)
	a.ESigned, a.ESignRef = now, "ESIGN-"+a.FormHash[:12]
	rf, err := o.App.Ingest.Raw.Put(ctx, ingest.RawFile{Tenant: tenant, Source: "CLIENT-DOC", Name: a.ID + " account opening form.html", SHA256: a.FormHash, Content: form})
	if err != nil {
		return a, nil, err
	}
	a.Docs = append(a.Docs, AppDoc{Kind: "Account opening agreement", Name: "Account opening form (signed).html", ContentType: "text/html; charset=utf-8", Size: int64(len(form)), SHA256: a.FormHash, FileID: rf.ID, At: now})
	a.done("review")
	return o.submitApplication(ctx, tenant, a)
}

// submitApplication creates the client at KYC verification and attaches
// the documents and checks.
func (o *Ops) submitApplication(ctx context.Context, tenant string, a Application) (Application, map[string]string, error) {
	actor := "applicant:" + a.ID
	nom := "Opted in"
	if a.NomineeOptOut {
		nom = "Opted out"
	}
	c := Client{Name: a.Name, Type: "Individual", PAN: a.PAN, DOB: a.DOB, Mobile: a.Mobile, Email: a.Email, City: a.City, State: a.State, Segments: a.Segments,
		Risk: "Low", Income: a.Income, RAS: a.RAS, Bank: BankAccount{a.Bank, a.IFSC, a.Account}, Demat: Demat{a.Depository, a.DematID},
		Nominee: nom, NomineeName: a.NomineeName, Nominees: a.Nominees, NominationOn: o.now().Format("2006-01-02"), NominationMode: "Online with e-Sign", KYCRoute: kycRoute(a), IncomeProof: a.hasDoc("Income proof"), RiskDisclosure: true,
		WhatsApp: a.WhatsApp, Partner: a.Partner, Tags: []string{"ONLINE"}}
	if c.Demat.Depository == "" {
		c.Demat.Depository = "CDSL"
	}
	nc, err := o.createApplication(ctx, tenant, actor, c)
	if err != nil {
		o.log(ctx, tenant, actor, "eapply.submit_failed", a.ID, map[string]any{"error": err.Error()})
		return a, nil, err
	}
	for _, d := range a.Docs {
		n, err := o.next(ctx, tenant, "clientdoc", actor)
		if err != nil {
			return a, nil, err
		}
		cd := ClientDoc{ID: fmt.Sprintf("DOC-%06d", n), UCC: nc.UCC, Kind: d.Kind, Name: d.Name, ContentType: d.ContentType, Size: d.Size, SHA256: d.SHA256, FileID: d.FileID, By: actor, At: d.At}
		if _, err := docs.Put(ctx, o.Docs, tenant, cClientDoc, nc.UCC+"|"+cd.ID, cd, 0, actor); err != nil {
			return a, nil, err
		}
	}
	a.Status, a.UCC, a.Updated = "Submitted", nc.UCC, o.now().Format(time.RFC3339)
	if err := docs.Upsert(ctx, o.Docs, tenant, cApplication, a.ID, a, actor); err != nil {
		return a, nil, err
	}
	if a.Lead != "" {
		_, _ = docs.Update(ctx, o.Docs, tenant, cLead, a.Lead, actor, func(l *Lead) error { l.Stage, l.UCC = "Converted", nc.UCC; return nil })
	}
	activated := o.autoActivate(ctx, tenant, a, nc.UCC)
	if activated {
		a.Activated = true
		_ = docs.Upsert(ctx, o.Docs, tenant, cApplication, a.ID, a, actor)
	} else {
		r := &runCtx{o: o, ctx: ctx, tenant: tenant, date: o.businessDate(ctx, tenant), process: "eapply", keys: map[string]bool{}}
		r.queue(a.ID, fmt.Sprintf("Online application %s · %s", a.ID, nc.UCC), fmt.Sprintf("%s applied online; %d checks passed, %d to check manually, %d failed. %s", a.Name, a.count("Passed"), a.count("Check manually"), a.count("Failed"), strings.Join(a.blockers(), " ")), nc.UCC, RoleChecker, "apps", "", nil)
	}
	set, _ := o.Settings(ctx, tenant)
	o.sendDirect(ctx, tenant, actor, ChEmail, a.Email, nc.UCC, "eapply.submitted", "Your account application "+a.ID,
		appReceivedText(a, nc.UCC, set.Firm, activated))
	o.log(ctx, tenant, actor, "eapply.submitted", a.ID, map[string]any{"ucc": nc.UCC})
	return a, nil, nil
}

// kycRoute records how Aadhaar was verified (the KRA upload marks IPV not
// applicable for both).
func kycRoute(a Application) string {
	if a.AddrSource == "Aadhaar offline e-KYC" {
		return "Online (Aadhaar offline e-KYC from UIDAI)"
	}
	return "Online (Aadhaar via DigiLocker)"
}

func (a Application) count(st string) int {
	n := 0
	for _, c := range a.Checks {
		if c.Status == st {
			n++
		}
	}
	return n
}

// ---- staff side ----

// Applications lists online applications, newest first.
func (o *Ops) Applications(ctx context.Context, tenant string) ([]Application, error) {
	as, err := docs.List[Application](ctx, o.Docs, tenant, cApplication, "")
	if as == nil {
		as = []Application{}
	}
	for i := range as {
		as[i].Token, as[i].Codes = "", nil
	}
	sort.SliceStable(as, func(i, j int) bool { return as[i].Created > as[j].Created })
	return as, err
}

// RejectApplication closes a submitted application and tells the applicant.
func (o *Ops) RejectApplication(ctx context.Context, tenant, actor, id, reason string) (Application, error) {
	if err := o.require(ctx, tenant, actor, "Rejecting an application", RoleChecker, RoleCompliance); err != nil {
		return Application{}, err
	}
	if strings.TrimSpace(reason) == "" {
		return Application{}, invalid("give the reason the applicant will see")
	}
	a, err := docs.Update(ctx, o.Docs, tenant, cApplication, id, actor, func(a *Application) error {
		if a.Status == "Rejected" {
			return fmt.Errorf("%w: already rejected", ErrState)
		}
		a.Status, a.Reason, a.Updated = "Rejected", strings.TrimSpace(reason), o.now().Format(time.RFC3339)
		return nil
	})
	if err != nil {
		return a, err
	}
	if a.UCC != "" {
		_, _ = docs.Update(ctx, o.Docs, tenant, cClient, a.UCC, actor, func(c *Client) error {
			if c.Status != "Active" {
				c.Status = "Rejected"
			}
			return nil
		})
	}
	set, _ := o.Settings(ctx, tenant)
	o.sendDirect(ctx, tenant, actor, ChEmail, a.Email, a.UCC, "eapply.rejected", "Your account application "+a.ID,
		fmt.Sprintf("Dear applicant,\n\nWe could not open your account (application %s): %s\n\nYou can reply to this email or start a new application.\n\n%s", a.ID, reason, set.Firm))
	o.closeWork(ctx, tenant, actor, "eapply", a.ID, "rejected")
	o.log(ctx, tenant, actor, "eapply.rejected", id, map[string]any{"reason": reason})
	return a, nil
}

// AppDocFile returns one of an application's files to staff.
func (o *Ops) AppDocFile(ctx context.Context, tenant, actor, id string, n int) (AppDoc, []byte, error) {
	a, _, err := docs.Get[Application](ctx, o.Docs, tenant, cApplication, id)
	if err != nil || n < 0 || n >= len(a.Docs) {
		return AppDoc{}, nil, ErrNotFound
	}
	d := a.Docs[n]
	rf, err := o.App.Ingest.Raw.Get(ctx, tenant, d.FileID)
	if err == nil {
		o.log(ctx, tenant, actor, "eapply.document.viewed", id, map[string]any{"kind": d.Kind})
	}
	return d, rf.Content, err
}
