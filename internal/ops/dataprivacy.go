package ops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
)

// ---- clients' data rights: terms and privacy notice at first access,
// the data summary, requests, the data-rights nominee, erasure ----
//
//   - CSCRF Part II PR.AT.S3 2: app access only on the customer's request
//     with acknowledgement of terms. A client accepts the firm's portal
//     terms and privacy notice at the first sign-in, and again when the
//     firm changes either; until then the portal opens only the terms.
//   - DPDP Act 2023 s.5 and Rule 3: an itemised notice of the personal
//     data, the purposes, how to withdraw consent, exercise rights and
//     complain to the Data Protection Board; to existing clients too. The
//     default notice is written from what Ledgerline holds; the firm edits
//     it. (The Act's duties apply from 13 May 2027.)
//   - s.6(4)-(6): withdrawing consent as easy as giving it; s.11-12 and
//     Rule 14: a summary of the personal data processed and of the
//     processors and fiduciaries it is shared with, and correction,
//     completion, updating and erasure on request; s.14: a nominee to use
//     the rights on death or incapacity. Requests are answered within 90
//     days (Rule 14(3)).
//   - s.8(7)-(8) and Rule 8: erase once the purpose is served, unless the
//     law needs it kept. A closed client's personal details can be erased
//     only after the firm's record retention (the archive's years, 10 by
//     default, at least SEBI's 8, PMLA's 5 after the relationship ends),
//     and not while a legal hold is on the client.
//   - IT (Reasonable Security Practices) Rules 2011, rule 5: written
//     consent before collecting sensitive personal data (bank details,
//     photograph, signature) on the branch path: the application records
//     the signed form's consent.
//   - PML (Maintenance of Records) Rules 2005, rule 9(1-F): KYC records
//     obtained from the CKYC registry are used only to verify identity
//     and address and not passed on without the client's, SEBI's or the
//     FIU Director's authority. A client whose KYC came from CKYC is
//     tagged, and opening such a client's KYC document asks for the
//     purpose.

const (
	cPrivacySet = "privacyset"
	cTermsAck   = "termsack"
	cDataReq    = "datareq"
	cDataNom    = "datanominee"
	// DataRequestDays is Rule 14(3)'s time to answer.
	DataRequestDays = 90
)

// PrivacySettings are the firm's portal terms and privacy notice.
type PrivacySettings struct {
	Terms         string `json:"terms"`
	TermsVersion  int    `json:"terms_version"`
	Notice        string `json:"notice"`
	NoticeVersion int    `json:"notice_version"`
	// Officer is who answers data requests and grievances (the DPO or the
	// compliance officer); empty: the compliance e-mail.
	Officer   string `json:"officer,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
	UpdatedBy string `json:"updated_by,omitempty"`
}

// TermsAck is a client login's acceptance.
type TermsAck struct {
	User          string   `json:"user"`
	TermsVersion  int      `json:"terms_version"`
	NoticeVersion int      `json:"notice_version"`
	At            string   `json:"at"`
	IP            string   `json:"ip,omitempty"`
	History       []string `json:"history,omitempty"` // earlier acceptances: "terms v1, notice v1 at …"
}

// DataConsent is the client's consent to collecting sensitive personal
// data, on the branch path (the signed form) or online (e-sign).
type DataConsent struct {
	At     string `json:"at"`
	Source string `json:"source"` // Signed form, e-Sign, Migrated
	Ref    string `json:"ref"`    // form number
	By     string `json:"by"`
}

// DataRequest is a client's request under the DPDP Act.
type DataRequest struct {
	ID       string `json:"id"`
	UCC      string `json:"ucc"`
	Kind     string `json:"kind"` // Summary, Correction, Erasure, Withdraw consent, Grievance
	Detail   string `json:"detail"`
	By       string `json:"by"` // the client login, or the data-rights nominee recorded by staff
	At       string `json:"at"`
	Due      string `json:"due"`
	Status   string `json:"status"` // Open, Done, Refused
	Answer   string `json:"answer,omitempty"`
	Answered string `json:"answered,omitempty"`
	AnswerBy string `json:"answer_by,omitempty"`
}

// DataRequestKinds are the requests a client can make.
var DataRequestKinds = []string{"Summary", "Correction", "Erasure", "Withdraw consent", "Grievance"}

// DataNominee is the person who may use the client's data rights on
// death or incapacity.
type DataNominee struct {
	UCC      string `json:"ucc"`
	Name     string `json:"name"`
	Relation string `json:"relation"`
	Email    string `json:"email,omitempty"`
	Mobile   string `json:"mobile,omitempty"`
	At       string `json:"at"`
	By       string `json:"by"`
}

// defaultTerms are the portal terms the firm starts from.
func defaultTerms(firm string) string {
	return fmt.Sprintf(`Terms of use of the %s client portal

1. The portal is for your own accounts only. Keep your password and two-factor codes to yourself; we will never ask for them.
2. Use the portal from devices you trust. Sign out when you are done.
3. What the portal shows is for information. Your contract notes and statements of account are the record of your transactions.
4. Tell us at once if you see a transaction you did not make: use "Report a fraudulent transaction" in the portal, or call us.
5. We record your sign-ins and what you do in the portal, to keep your account safe.
6. These terms may change; you will be asked to accept the new terms at your next sign-in.`, firstOf(firm, "firm's"))
}

// defaultNotice is an itemised notice written from what Ledgerline holds.
func defaultNotice(firm, officer string) string {
	return fmt.Sprintf(`Privacy notice (Digital Personal Data Protection Act, 2023)

%s holds and uses your personal data as a stock broker and depository participant registered with SEBI.

What we hold: your name, PAN, date of birth, address, e-mail and mobile number; your bank and demat accounts; your photograph and signature; your income range, occupation and tax residency; your nominees; your trades, holdings, ledger and statements; and records of your sign-ins and requests.

Why: to open and keep your account (KYC under SEBI's and the PMLA rules), to carry out your trades and settle them, to send contract notes and statements, to report to SEBI, the exchanges, the depositories and the tax authorities as the law requires, and to answer your requests and complaints.

Who we share it with: the KYC registration agency and the CKYC registry, the stock exchanges and clearing corporations, the depository, our banks, and the service providers who process it for us (our back office software, messaging and e-mail providers). We do not sell your data.

Your rights: you can ask for a summary of your data and whom it is shared with, have it corrected or completed, have it erased once the law no longer needs us to keep it, withdraw a consent you gave (for example WhatsApp messages) as easily as you gave it, and name a person to use these rights for you on death or incapacity. Use "My data" in the portal, or write to %s. We answer within 90 days.

Complaints: write to %s. If you are not satisfied, you can complain to the Data Protection Board of India.

Records the law needs us to keep (for example for 8 years under SEBI's rules and 5 years after the account closes under the PMLA) are kept for that time even if you withdraw consent.`, firstOf(firm, "We"), firstOf(officer, "our compliance officer"), firstOf(officer, "our compliance officer"))
}

// privacySettings returns the firm's terms and notice, with the defaults.
func (o *Ops) privacySettings(ctx context.Context, tenant string) PrivacySettings {
	p, _, _ := docs.GetOr(ctx, o.Docs, tenant, cPrivacySet, "main", PrivacySettings{})
	set, _ := o.Settings(ctx, tenant)
	if p.Officer == "" {
		p.Officer = set.ComplianceEmail
	}
	if strings.TrimSpace(p.Terms) == "" {
		p.Terms = defaultTerms(set.Firm)
	}
	if strings.TrimSpace(p.Notice) == "" {
		p.Notice = defaultNotice(set.Firm, p.Officer)
	}
	if p.TermsVersion <= 0 {
		p.TermsVersion = 1
	}
	if p.NoticeVersion <= 0 {
		p.NoticeVersion = 1
	}
	return p
}

// PrivacySettingsView returns them. Compliance and Admin.
func (o *Ops) PrivacySettingsView(ctx context.Context, tenant, actor string) (PrivacySettings, error) {
	if err := o.require(ctx, tenant, actor, "Seeing the portal terms and privacy notice", RoleCompliance); err != nil {
		return PrivacySettings{}, err
	}
	return o.privacySettings(ctx, tenant), nil
}

// SavePrivacySettings changes the terms or the notice; a change of either
// is a new version every client accepts at their next sign-in.
// Compliance and Admin.
func (o *Ops) SavePrivacySettings(ctx context.Context, tenant, actor string, p PrivacySettings) (PrivacySettings, error) {
	if err := o.require(ctx, tenant, actor, "Changing the portal terms and privacy notice", RoleCompliance); err != nil {
		return PrivacySettings{}, err
	}
	old := o.privacySettings(ctx, tenant)
	p.Terms, p.Notice, p.Officer = strings.TrimSpace(p.Terms), strings.TrimSpace(p.Notice), strings.TrimSpace(p.Officer)
	if len(p.Terms) < 100 || len(p.Notice) < 200 {
		return PrivacySettings{}, invalid("write the terms and the privacy notice in full")
	}
	if p.Officer != "" && !reEmail.MatchString(p.Officer) {
		return PrivacySettings{}, invalid("the contact for data requests is an e-mail address")
	}
	p.TermsVersion, p.NoticeVersion = old.TermsVersion, old.NoticeVersion
	if p.Terms != strings.TrimSpace(old.Terms) {
		p.TermsVersion++
	}
	if p.Notice != strings.TrimSpace(old.Notice) {
		p.NoticeVersion++
	}
	p.UpdatedAt, p.UpdatedBy = o.now().Format(time.RFC3339), actor
	if err := docs.Upsert(ctx, o.Docs, tenant, cPrivacySet, "main", p, actor); err != nil {
		return PrivacySettings{}, err
	}
	o.log(ctx, tenant, actor, "privacy.settings.saved", "privacy", map[string]any{"terms_version": p.TermsVersion, "notice_version": p.NoticeVersion})
	return o.privacySettings(ctx, tenant), nil
}

// PortalTerms is what a client is asked to accept.
type PortalTerms struct {
	Due           bool   `json:"due"`
	Terms         string `json:"terms"`
	TermsVersion  int    `json:"terms_version"`
	Notice        string `json:"notice"`
	NoticeVersion int    `json:"notice_version"`
	AcceptedAt    string `json:"accepted_at,omitempty"`
}

// TermsDue reports whether a client login still has to accept the
// current terms and notice. Staff logins never do.
func (o *Ops) TermsDue(ctx context.Context, tenant, user string) bool {
	u, err := o.user(ctx, tenant, user)
	if err != nil || u.Role != RoleClient {
		return false
	}
	p := o.privacySettings(ctx, tenant)
	a, _, err := docs.Get[TermsAck](ctx, o.Docs, tenant, cTermsAck, user)
	return err != nil || a.TermsVersion < p.TermsVersion || a.NoticeVersion < p.NoticeVersion
}

// PortalTermsFor returns the terms for the client login.
func (o *Ops) PortalTermsFor(ctx context.Context, tenant, actor string) (PortalTerms, error) {
	if _, err := o.PortalAccounts(ctx, tenant, actor); err != nil {
		return PortalTerms{}, err
	}
	p := o.privacySettings(ctx, tenant)
	v := PortalTerms{Due: o.TermsDue(ctx, tenant, actor), Terms: p.Terms, TermsVersion: p.TermsVersion, Notice: p.Notice, NoticeVersion: p.NoticeVersion}
	if a, _, err := docs.Get[TermsAck](ctx, o.Docs, tenant, cTermsAck, actor); err == nil {
		v.AcceptedAt = a.At
	}
	return v, nil
}

// AcceptTerms records the client's acceptance of the versions shown.
func (o *Ops) AcceptTerms(ctx context.Context, tenant, actor string, terms, notice int, ip string) (PortalTerms, error) {
	if _, err := o.PortalAccounts(ctx, tenant, actor); err != nil {
		return PortalTerms{}, err
	}
	p := o.privacySettings(ctx, tenant)
	if terms != p.TermsVersion || notice != p.NoticeVersion {
		return PortalTerms{}, fmt.Errorf("%w: the terms changed while you were reading them; read the new version", ErrState)
	}
	now := o.now().Format(time.RFC3339)
	_, err := upd(ctx, o.Docs, tenant, cTermsAck, actor, actor, &TermsAck{User: actor}, func(a *TermsAck) error {
		if a.At != "" {
			a.History = append(a.History, fmt.Sprintf("terms v%d, notice v%d at %s", a.TermsVersion, a.NoticeVersion, a.At))
		}
		a.TermsVersion, a.NoticeVersion, a.At, a.IP = terms, notice, now, trim(ip, 60)
		return nil
	})
	if err != nil {
		return PortalTerms{}, err
	}
	o.log(ctx, tenant, actor, "portal.terms_accepted", actor, map[string]any{"terms_version": terms, "notice_version": notice, "ip": ip})
	return o.PortalTermsFor(ctx, tenant, actor)
}

// ---- the data summary ----

// DataItem is one kind of personal data held, with what is on record.
type DataItem struct {
	What    string `json:"what"`
	Value   string `json:"value"` // masked
	Purpose string `json:"purpose"`
}

// DataRecipient is a fiduciary or processor the data goes to.
type DataRecipient struct {
	Name string `json:"name"`
	Role string `json:"role"` // Fiduciary (has its own duty), Processor (for the firm)
	What string `json:"what"`
}

// DataSummary is what the firm holds about one account and whom it
// shares it with.
type DataSummary struct {
	UCC        string          `json:"ucc"`
	Name       string          `json:"name"`
	At         string          `json:"at"`
	Items      []DataItem      `json:"items"`
	Recipients []DataRecipient `json:"recipients"`
	Consents   []string        `json:"consents"`
	Nominee    *DataNominee    `json:"nominee,omitempty"`
	Requests   []DataRequest   `json:"requests"`
	Officer    string          `json:"officer"`
	KYCSource  string          `json:"kyc_source,omitempty"`
}

// dataSummary builds the summary from the client's record.
func (o *Ops) dataSummary(ctx context.Context, tenant string, c Client) DataSummary {
	p := o.privacySettings(ctx, tenant)
	v := DataSummary{UCC: c.UCC, Name: c.Name, At: o.now().Format(time.RFC3339), Items: []DataItem{}, Recipients: []DataRecipient{}, Consents: []string{}, Requests: []DataRequest{}, Officer: p.Officer, KYCSource: c.KYCSource}
	add := func(what, val, why string) {
		if strings.TrimSpace(val) != "" {
			v.Items = append(v.Items, DataItem{What: what, Value: val, Purpose: why})
		}
	}
	kyc := "KYC under SEBI's rules and the PMLA"
	add("Name", c.Name, kyc)
	add("PAN", MaskPAN(c.PAN), kyc+"; tax reporting")
	add("Date of birth", map[bool]string{true: "on record"}[c.DOB != ""], kyc)
	add("Address", c.City, kyc+"; letters and statements")
	add("E-mail", maskEmail(c.Email), "contract notes, statements, alerts")
	add("Mobile", maskMobile(c.Mobile), "alerts and one-time codes")
	for _, b := range registeredBanks(c) {
		add("Bank account", b.Bank+" "+MaskAccount(b.Account), "pay-in and pay-out of your money")
	}
	for _, d := range append([]Demat{c.Demat}, c.Demats...) {
		if d.ID != "" {
			add("Demat account", d.Depository+" "+MaskAccount(d.ID), "delivery of your securities")
			break
		}
	}
	if len(c.Nominees) > 0 {
		add("Nominees", fmt.Sprintf("%d on record", len(c.Nominees)), "nomination under SEBI's rules")
	}
	add("Income range", c.Income, kyc+"; suitability for derivatives")
	if c.FATCA != nil {
		add("Tax residency (FATCA/CRS)", "declared", "reporting under the Income-tax Rules (rule 114F-114H)")
	}
	add("Photograph and signature", "on the account opening form", kyc)
	add("Trades, holdings, ledger and statements", "kept for your account", "carrying out and settling your trades; SEBI's record keeping")
	add("Sign-ins and requests", "kept with the time and address", "keeping your account safe (CERT-In directions)")
	// Whom it goes to: the market's institutions always; the connections
	// the firm has set up, by name.
	v.Recipients = append(v.Recipients,
		DataRecipient{"KYC registration agency and CKYC registry", "Fiduciary", "KYC details"},
		DataRecipient{"Stock exchanges and clearing corporations", "Fiduciary", "client details (UCC), trades and holdings reporting"},
		DataRecipient{"Depository (CDSL or NSDL)", "Fiduciary", "demat account and its holders"},
		DataRecipient{"Income-tax department", "Fiduciary", "tax reports where the law requires"},
		DataRecipient{"Ledgerline (back office software)", "Processor", "everything above, to run the back office for the firm"})
	cs, _ := docs.List[Connection](ctx, o.Docs, tenant, cConn, "")
	for _, x := range cs {
		pr, ok := connProvider(x.Provider)
		if !ok || x.Status == "" {
			continue
		}
		switch pr.Group {
		case "Messages", "Money":
			name := pr.Name
			if vend := x.Values["vendor"]; vend != "" {
				name += " (" + vend + ")"
			}
			v.Recipients = append(v.Recipients, DataRecipient{name, "Processor", pr.Purpose})
		}
	}
	if c.WAConsent != nil && c.WAConsent.On {
		v.Consents = append(v.Consents, "WhatsApp messages: on since "+fmtDay(c.WAConsent.At[:10])+" ("+c.WAConsent.Source+")")
	}
	if c.DataConsent != nil {
		v.Consents = append(v.Consents, "Sensitive data at account opening: "+c.DataConsent.Source+" "+c.DataConsent.Ref)
	}
	if !c.NoInsights {
		v.Consents = append(v.Consents, "Account insights in the portal: on")
	}
	if n, _, err := docs.Get[DataNominee](ctx, o.Docs, tenant, cDataNom, c.UCC); err == nil && n.Name != "" {
		v.Nominee = &n
	}
	rs, _ := docs.List[DataRequest](ctx, o.Docs, tenant, cDataReq, "")
	for _, r := range rs {
		if r.UCC == c.UCC {
			v.Requests = append(v.Requests, r)
		}
	}
	sort.Slice(v.Requests, func(i, j int) bool { return v.Requests[i].ID > v.Requests[j].ID })
	return v
}

// PortalDataSummary is the client's own summary.
func (o *Ops) PortalDataSummary(ctx context.Context, tenant, actor, ucc string) (DataSummary, error) {
	c, err := o.portalClient(ctx, tenant, actor, ucc)
	if err != nil {
		return DataSummary{}, err
	}
	o.log(ctx, tenant, actor, "privacy.summary.viewed", c.UCC, nil)
	return o.dataSummary(ctx, tenant, c), nil
}

// portalClient is one of the login's accounts.
func (o *Ops) portalClient(ctx context.Context, tenant, actor, ucc string) (Client, error) {
	accts, err := o.PortalAccounts(ctx, tenant, actor)
	if err != nil {
		return Client{}, err
	}
	ucc = strings.ToUpper(strings.TrimSpace(ucc))
	if ucc == "" && len(accts) > 0 {
		ucc = accts[0]
	}
	if !contains(accts, ucc) {
		return Client{}, ErrNotFound
	}
	return o.Client(ctx, tenant, ucc)
}

// RequestData records a client's request. A consent withdrawal for
// WhatsApp takes effect at once; the others go to the compliance officer.
func (o *Ops) RequestData(ctx context.Context, tenant, actor, ucc, kind, detail string) (DataRequest, error) {
	c, err := o.portalClient(ctx, tenant, actor, ucc)
	if err != nil {
		return DataRequest{}, err
	}
	return o.addDataRequest(ctx, tenant, actor, c, kind, detail)
}

func (o *Ops) addDataRequest(ctx context.Context, tenant, actor string, c Client, kind, detail string) (DataRequest, error) {
	if !contains(DataRequestKinds, kind) {
		return DataRequest{}, invalid("the request is one of %s", strings.Join(DataRequestKinds, ", "))
	}
	detail = strings.TrimSpace(detail)
	if kind != "Summary" && len(detail) < 5 {
		return DataRequest{}, invalid("say what you want changed, erased or withdrawn")
	}
	n, err := o.next(ctx, tenant, "datareq", actor)
	if err != nil {
		return DataRequest{}, err
	}
	now := o.now()
	r := DataRequest{ID: fmt.Sprintf("DR-%06d", n), UCC: c.UCC, Kind: kind, Detail: trim(detail, 2000), By: actor, At: now.Format(time.RFC3339), Due: now.AddDate(0, 0, DataRequestDays).In(ist).Format("2006-01-02"), Status: "Open"}
	if kind == "Withdraw consent" && strings.Contains(strings.ToLower(detail), "whatsapp") && c.WAConsent != nil && c.WAConsent.On {
		if _, err := docs.Update(ctx, o.Docs, tenant, cClient, c.UCC, actor, func(x *Client) error {
			x.WhatsApp = false
			x.setConsent(false, now.Format(time.RFC3339), SrcPortal, actor, r.ID)
			return nil
		}); err == nil {
			r.Status, r.Answer, r.Answered, r.AnswerBy = "Done", "WhatsApp messages turned off at once.", now.Format(time.RFC3339), SystemActor
		}
	}
	if _, err := docs.Put(ctx, o.Docs, tenant, cDataReq, r.ID, r, 0, actor); err != nil {
		return DataRequest{}, err
	}
	o.log(ctx, tenant, actor, "privacy.request", c.UCC, map[string]any{"id": r.ID, "kind": kind})
	if r.Status == "Open" {
		o.raiseWork(ctx, tenant, "privacy", r.ID, fmt.Sprintf("Data request %s from %s: %s", r.ID, c.UCC, strings.ToLower(kind)),
			fmt.Sprintf("Answer by %s (DPDP Rules 2025, rule 14(3): 90 days). %s", fmtDay(r.Due), trim(detail, 300)), c.UCC, RoleCompliance, "cyber", now.In(ist).Format("2006-01-02"))
	}
	return r, nil
}

// AnswerDataRequest closes a request: done, or refused with the reason
// (for example, records the law needs kept). Compliance and Admin.
func (o *Ops) AnswerDataRequest(ctx context.Context, tenant, actor, id string, done bool, answer string) (DataRequest, error) {
	if err := o.require(ctx, tenant, actor, "Answering clients' data requests", RoleCompliance); err != nil {
		return DataRequest{}, err
	}
	answer = strings.TrimSpace(answer)
	if len(answer) < 5 {
		return DataRequest{}, invalid("write the answer the client will see")
	}
	r, err := docs.Update(ctx, o.Docs, tenant, cDataReq, id, actor, func(x *DataRequest) error {
		if x.Status != "Open" {
			return fmt.Errorf("%w: %s is %s", ErrState, x.ID, strings.ToLower(x.Status))
		}
		x.Status, x.Answer, x.Answered, x.AnswerBy = map[bool]string{true: "Done", false: "Refused"}[done], trim(answer, 2000), o.now().Format(time.RFC3339), actor
		return nil
	})
	if errors.Is(err, docs.ErrNotFound) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	o.closeWork(ctx, tenant, actor, "privacy", r.ID, strings.ToLower(r.Status))
	if c, err := o.Client(ctx, tenant, r.UCC); err == nil && reEmail.MatchString(c.Email) {
		o.sendDirect(ctx, tenant, actor, ChEmail, c.Email, c.UCC, "privacy.answer", "Your data request "+r.ID,
			fmt.Sprintf("Dear %s,\n\nYour request %s (%s) of %s: %s\n\n%s", c.Name, r.ID, strings.ToLower(r.Kind), fmtDay(r.At[:10]), r.Answer, "If you are not satisfied, you can complain to the Data Protection Board of India."))
	}
	o.log(ctx, tenant, actor, "privacy.answered", r.UCC, map[string]any{"id": r.ID, "status": r.Status})
	return r, nil
}

// DataRequests lists the register. Compliance and Admin.
func (o *Ops) DataRequests(ctx context.Context, tenant, actor string) ([]DataRequest, error) {
	if err := o.require(ctx, tenant, actor, "Seeing clients' data requests", RoleCompliance); err != nil {
		return nil, err
	}
	rs, err := docs.List[DataRequest](ctx, o.Docs, tenant, cDataReq, "")
	sort.Slice(rs, func(i, j int) bool { return rs[i].ID > rs[j].ID })
	if rs == nil {
		rs = []DataRequest{}
	}
	return rs, err
}

// SetDataNominee records the person who may use the client's data rights
// on death or incapacity (s.14). From the portal.
func (o *Ops) SetDataNominee(ctx context.Context, tenant, actor, ucc string, n DataNominee) (DataNominee, error) {
	c, err := o.portalClient(ctx, tenant, actor, ucc)
	if err != nil {
		return DataNominee{}, err
	}
	n.Name, n.Relation, n.Email, n.Mobile = strings.TrimSpace(n.Name), strings.TrimSpace(n.Relation), strings.TrimSpace(n.Email), strings.TrimSpace(n.Mobile)
	if n.Name == "" {
		// Clearing the nominee.
		n = DataNominee{}
	} else {
		if n.Relation == "" {
			return DataNominee{}, invalid("give the person's relation to you")
		}
		if n.Email == "" && n.Mobile == "" {
			return DataNominee{}, invalid("give an e-mail address or mobile number to reach the person")
		}
		if n.Email != "" && !reEmail.MatchString(n.Email) {
			return DataNominee{}, invalid("%q is not an e-mail address", n.Email)
		}
	}
	n.UCC, n.At, n.By = c.UCC, o.now().Format(time.RFC3339), actor
	if err := docs.Upsert(ctx, o.Docs, tenant, cDataNom, c.UCC, n, actor); err != nil {
		return DataNominee{}, err
	}
	o.log(ctx, tenant, actor, "privacy.nominee", c.UCC, map[string]any{"set": n.Name != ""})
	return n, nil
}

// ---- erasure once retention ends ----

// ErasureCandidate is a closed client whose records may now be erased.
type ErasureCandidate struct {
	UCC       string `json:"ucc"`
	Name      string `json:"name"`
	ClosedOn  string `json:"closed_on"`
	KeepUntil string `json:"keep_until"`
	Due       bool   `json:"due"` // retention over
	Hold      bool   `json:"hold"`
	Erased    string `json:"erased_on,omitempty"`
}

// closedOn is when a client's account was closed: the closure request's
// date, or failing that the last segment closure.
func (o *Ops) closedOnMap(ctx context.Context, tenant string) map[string]string {
	out := map[string]string{}
	xs, _ := docs.List[AccountClosure](ctx, o.Docs, tenant, cClosure, "")
	for _, x := range xs {
		if x.Status == "Closed" && x.ClosedOn > out[x.UCC] {
			out[x.UCC] = x.ClosedOn
		}
	}
	return out
}

// ErasureList lists closed clients with the day their records may be
// erased. Compliance and Admin.
func (o *Ops) ErasureList(ctx context.Context, tenant, actor string) ([]ErasureCandidate, error) {
	if err := o.require(ctx, tenant, actor, "Seeing records due for erasure", RoleCompliance); err != nil {
		return nil, err
	}
	return o.erasureList(ctx, tenant), nil
}

func (o *Ops) erasureList(ctx context.Context, tenant string) []ErasureCandidate {
	set, _ := o.Settings(ctx, tenant)
	years := o.archiveSettings(ctx, tenant).norm(archiveRetainMin(set)).RetainYears
	closed := o.closedOnMap(ctx, tenant)
	cs, _ := o.Clients(ctx, tenant)
	today := o.today()
	out := []ErasureCandidate{}
	for _, c := range cs {
		on := closed[c.UCC]
		if on == "" {
			continue
		}
		t, err := time.Parse("2006-01-02", on)
		if err != nil {
			continue
		}
		keep := t.AddDate(years, 0, 0).Format("2006-01-02")
		e := ErasureCandidate{UCC: c.UCC, Name: c.Name, ClosedOn: on, KeepUntil: keep, Due: today > keep && c.ErasedOn == "", Hold: o.onLegalHold(ctx, tenant, c.UCC), Erased: c.ErasedOn}
		if e.Hold {
			e.Due = false
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].KeepUntil < out[j].KeepUntil })
	return out
}

// EraseClient erases a closed client's personal details once the
// retention period is over: name, PAN, contacts, address, bank and demat
// numbers, nominees, tax residency and the documents' names are replaced;
// the UCC, the ledger and trade records stay, without the person. The
// document files themselves are cleared from the index; the operator's
// storage keeps no copy past its backup cycle. Compliance; reason needed.
func (o *Ops) EraseClient(ctx context.Context, tenant, actor, ucc, reason string) (Client, error) {
	if err := o.require(ctx, tenant, actor, "Erasing a client's personal data", RoleCompliance); err != nil {
		return Client{}, err
	}
	ucc = strings.ToUpper(strings.TrimSpace(ucc))
	var cand *ErasureCandidate
	for _, e := range o.erasureList(ctx, tenant) {
		if e.UCC == ucc {
			e := e
			cand = &e
		}
	}
	switch {
	case cand == nil:
		return Client{}, fmt.Errorf("%w: %s is not a closed account", ErrState, ucc)
	case cand.Erased != "":
		return Client{}, fmt.Errorf("%w: %s was erased on %s", ErrState, ucc, fmtDay(cand.Erased))
	case cand.Hold:
		return Client{}, fmt.Errorf("%w: %s is under a legal hold", ErrState, ucc)
	case !cand.Due:
		return Client{}, fmt.Errorf("%w: %s's records are kept until %s (the firm's record retention)", ErrState, ucc, fmtDay(cand.KeepUntil))
	}
	if len(strings.TrimSpace(reason)) < 5 {
		return Client{}, invalid("say why (for example: retention over, the client asked)")
	}
	today := o.today()
	h := sha256.Sum256([]byte(tenant + "|" + ucc))
	tag := "ERASED-" + strings.ToUpper(hex.EncodeToString(h[:4]))
	c, err := docs.Update(ctx, o.Docs, tenant, cClient, ucc, actor, func(x *Client) error {
		x.Name, x.PAN, x.DOB, x.Email, x.Mobile = "Erased client "+ucc, "", "", "", ""
		x.City = ""
		x.Bank = BankAccount{Account: MaskAccount(x.Bank.Account)}
		x.Banks = nil
		x.Demat.ID = MaskAccount(x.Demat.ID)
		for i := range x.Demats {
			x.Demats[i].ID = MaskAccount(x.Demats[i].ID)
		}
		x.Nominees, x.Nominee, x.NomineeName = nil, "Erased", ""
		x.FATCA, x.WAConsent, x.DataConsent = nil, nil, nil
		x.Income = ""
		x.ErasedOn, x.ErasedRef = today, tag
		return nil
	})
	if err != nil {
		return c, err
	}
	ds, _ := o.ClientDocs(ctx, tenant, ucc)
	for _, d := range ds {
		_ = docs.Upsert(ctx, o.Docs, tenant, cClientDoc, ucc+"|"+d.ID, ClientDoc{ID: d.ID, UCC: ucc, Kind: d.Kind, Name: "erased", By: actor, At: o.now().Format(time.RFC3339)}, actor)
	}
	o.closeWork(ctx, tenant, actor, "privacy", "erase:"+ucc, "erased")
	o.log(ctx, tenant, actor, "privacy.erased", ucc, map[string]any{"reason": trim(reason, 300), "ref": tag, "kept_until": cand.KeepUntil, "documents": len(ds)})
	return c, nil
}

// erasureTick, once a day: a closed client whose retention ended goes to
// the compliance officer to erase.
func (o *Ops) erasureTick(ctx context.Context, tenant string, now time.Time) {
	today := now.In(ist).Format("2006-01-02")
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cSecState, "erasure", secState{})
	if st.LastPurge >= today {
		return
	}
	st.LastPurge = today
	_ = docs.Upsert(ctx, o.Docs, tenant, cSecState, "erasure", st, SystemActor)
	for _, e := range o.erasureList(ctx, tenant) {
		if e.Due {
			o.raiseWork(ctx, tenant, "privacy", "erase:"+e.UCC, "Erase the personal data of closed account "+e.UCC,
				fmt.Sprintf("Closed %s; kept until %s. Erase it on Compliance → Data protection (DPDP Act s.8(7)), or record why it must be kept longer (a legal hold).", fmtDay(e.ClosedOn), fmtDay(e.KeepUntil)), e.UCC, RoleCompliance, "cyber", today)
		}
	}
}

// LegalHoldTag on a client keeps its records from erasure (an
// investigation, a dispute, an arbitration).
const LegalHoldTag = "LEGAL-HOLD"

func (o *Ops) onLegalHold(ctx context.Context, tenant, ucc string) bool {
	c, err := o.Client(ctx, tenant, ucc)
	if err != nil {
		return true
	}
	for _, t := range c.Tags {
		if strings.EqualFold(strings.ReplaceAll(t, " ", "-"), LegalHoldTag) {
			return true
		}
	}
	return c.Sanctions != nil
}

// ---- KYC from CKYC, consent on the branch path ----

// KYCSources are where a client's KYC record came from.
var KYCSources = []string{"", "Own (in person or online)", "KRA", "CKYC", "DigiLocker"}

// ckycPurposes are the only uses of a CKYC-sourced KYC record (rule 9(1-F)).
var CKYCPurposes = []string{"Verify identity or address", "The client authorised it", "SEBI asked for it", "FIU-IND Director asked for it"}

// kycDocKinds are the documents that hold the KYC record.
var kycDocKinds = map[string]bool{"PAN card": true, "Address proof": true, "Photograph": true, "Signature": true, "KYC form": true}

// CheckKYCDocUse says whether opening a document needs a purpose: a
// KYC document of a client whose KYC came from CKYC. It returns the
// purpose to record, or an error naming the allowed ones.
func (o *Ops) CheckKYCDocUse(ctx context.Context, tenant, actor, ucc, id, purpose string) error {
	c, err := o.Client(ctx, tenant, ucc)
	if err != nil || c.KYCSource != "CKYC" {
		return nil
	}
	if role, _ := o.RoleOf(ctx, tenant, actor); role == RoleClient {
		return nil
	}
	d, _, err := docs.Get[ClientDoc](ctx, o.Docs, tenant, cClientDoc, strings.ToUpper(ucc)+"|"+id)
	if err != nil || !kycDocKinds[d.Kind] {
		return nil
	}
	if !contains(CKYCPurposes, purpose) {
		return fmt.Errorf("%w: this KYC record came from the CKYC registry: it may be used only to %s (PML Rules, rule 9(1-F)). Choose the purpose", ErrInvalid, strings.ToLower(strings.Join(CKYCPurposes, "; or ")))
	}
	o.log(ctx, tenant, actor, "client.ckyc_record.used", ucc, map[string]any{"doc": id, "purpose": purpose})
	return nil
}

// SetKYCSource tags where a client's KYC record came from. Ops checker and
// compliance.
func (o *Ops) SetKYCSource(ctx context.Context, tenant, actor, ucc, src string) (Client, error) {
	if err := o.require(ctx, tenant, actor, "Recording where a client's KYC came from", RoleChecker, RoleCompliance); err != nil {
		return Client{}, err
	}
	if !contains(KYCSources, src) {
		return Client{}, invalid("the source is one of %s", strings.Join(KYCSources[1:], ", "))
	}
	c, err := docs.Update(ctx, o.Docs, tenant, cClient, strings.ToUpper(ucc), actor, func(x *Client) error {
		x.KYCSource = src
		return nil
	})
	if err != nil {
		return c, err
	}
	o.log(ctx, tenant, actor, "client.kyc_source", c.UCC, map[string]any{"source": src})
	return c, nil
}

// RecordDataConsent records the signed form's consent to collect
// sensitive personal data, for an application made at a branch or head
// office. Ops maker, checker and branch staff.
func (o *Ops) RecordDataConsent(ctx context.Context, tenant, actor, ucc, source, ref string) (Client, error) {
	if err := o.require(ctx, tenant, actor, "Recording a client's consent", RoleMaker, RoleChecker, RoleBranch); err != nil {
		return Client{}, err
	}
	if source != "Signed form" && source != "e-Sign" && source != "Migrated" {
		return Client{}, invalid("the consent is from the signed form, an e-sign or migrated records")
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Client{}, invalid("give the form number or reference")
	}
	c, err := docs.Update(ctx, o.Docs, tenant, cClient, strings.ToUpper(ucc), actor, func(x *Client) error {
		x.DataConsent = &DataConsent{At: o.now().Format(time.RFC3339), Source: source, Ref: trim(ref, 80), By: actor}
		return nil
	})
	if err != nil {
		return c, err
	}
	o.log(ctx, tenant, actor, "client.data_consent", c.UCC, map[string]any{"source": source, "ref": ref})
	return c, nil
}
