package ops

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// Onboarding stages, in order.
var Stages = []string{"Application", "KYC verification", "UCC upload", "Active"}

// Segment names.
var SegmentNames = map[string]string{"CM": "Equity cash", "FO": "Equity F&O", "CD": "Currency", "COM": "Commodity"}

// BankAccount is a client's registered bank account. Client.Bank is the
// primary account; Client.Banks, once a client has more than one, lists
// every registered account with its status and verification
// (clientbanks.go).
type BankAccount struct {
	Bank    string `json:"bank"`
	IFSC    string `json:"ifsc"`
	Account string `json:"account"`
	// Branch is the bank branch's name, when known (the HDFC payout file
	// carries it).
	Branch string `json:"branch,omitempty"`
	MICR   string `json:"micr,omitempty"`
	// Type is SB, CA, NRE or NRO.
	Type string `json:"type,omitempty"`
	// In Client.Banks only: the primary (default payout) account, Active or
	// Inactive, how and when the account was verified, when it was added,
	// the proof document and the change request that added or last
	// changed it.
	Primary      bool   `json:"primary,omitempty"`
	Status       string `json:"status,omitempty"`
	VerifiedOn   string `json:"verified_on,omitempty"`
	VerifyMethod string `json:"verify_method,omitempty"`
	AddedOn      string `json:"added_on,omitempty"`
	ProofDoc     string `json:"proof_doc,omitempty"`
	Change       string `json:"change,omitempty"`
	InactiveOn   string `json:"inactive_on,omitempty"`
	InactiveWhy  string `json:"inactive_why,omitempty"`
}

// Demat is a client's demat account. Client.Demat is the primary account;
// Client.Demats, once a client has more than one, lists every account
// with its status (demats.go).
type Demat struct {
	Depository string `json:"depository"`
	ID         string `json:"id"`
	// In Client.Demats only: the primary account (the one the exchanges'
	// UCC records list first and securities pay-outs credit), Active,
	// Inactive or Closed, when it was added, and the change request that
	// added or last changed it.
	Primary     bool   `json:"primary,omitempty"`
	Status      string `json:"status,omitempty"`
	AddedOn     string `json:"added_on,omitempty"`
	InactiveOn  string `json:"inactive_on,omitempty"`
	InactiveWhy string `json:"inactive_why,omitempty"`
	Change      string `json:"change,omitempty"`
	// OwnDP: the account is with the firm's own DP (a BO account Ledgerline
	// keeps), not with another DP. Worked out when the client is read
	// (Ops.withDemats) and set by the DP-ACCOUNTS import.
	OwnDP bool `json:"own_dp,omitempty"`
}

// Client is a client of the firm: a trading client, or (DPOnly) a client
// of the firm's DP with demat accounts and no trading account.
type Client struct {
	UCC            string       `json:"ucc"`
	Name           string       `json:"name"`
	Type           string       `json:"type"`
	PAN            string       `json:"pan"`
	DOB            string       `json:"dob,omitempty"`
	Mobile         string       `json:"mobile"`
	Email          string       `json:"email"`
	City           string       `json:"city"`
	Status         string       `json:"status"`
	Segments       []string     `json:"segments"`
	Risk           string       `json:"risk"`
	Income         string       `json:"income"`
	RAS            string       `json:"ras"`
	Bank           BankAccount  `json:"bank"`
	Demat          Demat        `json:"demat"`
	Nominee        string       `json:"nominee"`
	NomineeName    string       `json:"nominee_name,omitempty"`
	KYCDate        string       `json:"kyc_date"`
	KYCRoute       string       `json:"kyc_route,omitempty"`
	IncomeProof    bool         `json:"income_proof"`
	RiskDisclosure bool         `json:"risk_disclosure"`
	IPVScheduled   bool         `json:"ipv_scheduled"`
	API            bool         `json:"api"`
	UPIBlock       money.Amount `json:"upi_block"`
	Blocked        bool         `json:"blocked"`
	BlockedReason  string       `json:"blocked_reason,omitempty"`
	// TradeFrozen is the client's own request to freeze online trading
	// (tradefreeze.go): its ID while the freeze is on. Kept apart from the
	// RMS kill switch so lifting one does not lift the other.
	TradeFrozen string   `json:"trade_frozen,omitempty"`
	LastTrade   string   `json:"last_trade,omitempty"`
	CreatedBy   string   `json:"created_by,omitempty"`
	ActivatedBy string   `json:"activated_by,omitempty"`
	Tags        []string `json:"tags"` // client groups, e.g. HNI, NRI, ACTIVE-TRADER
	// Inactive is the exchanges' UCC "Inactive" flag: no activity for the
	// inactivity period (24 months under the 2024 exchange circulars).
	Inactive      bool   `json:"inactive,omitempty"`
	InactiveSince string `json:"inactive_since,omitempty"`
	ReactivatedOn string `json:"reactivated_on,omitempty"`
	KYCUpdatedOn  string `json:"kyc_updated_on,omitempty"` // last KYC detail change uploaded to the KRA
	Branch        string `json:"branch,omitempty"`
	State         string `json:"state,omitempty"` // place of supply for GST
	GSTIN         string `json:"gstin,omitempty"`
	WhatsApp      bool   `json:"whatsapp,omitempty"`      // opted in to WhatsApp messages
	DND           bool   `json:"dnd,omitempty"`           // on the do-not-disturb register (no promotional SMS)
	Partner       string `json:"partner,omitempty"`       // authorised person
	RM            string `json:"rm,omitempty"`            // relationship manager
	ReferredBy    string `json:"referred_by,omitempty"`   // the referrer: a client's UCC or a registered AP's code
	ReferredFrom  string `json:"referred_from,omitempty"` // referral period start
	// ReferrerKind is AP (a registered authorised person, who may earn a
	// referral reward) or Client (recorded as the source only); empty on
	// records from before the AP-only rule, read as Client (referrals.go).
	ReferrerKind string `json:"referrer_kind,omitempty"`
	// Private clients are visible only to admins, compliance and VisibleTo.
	Private   bool     `json:"private,omitempty"`
	VisibleTo []string `json:"visible_to,omitempty"`
	// PMLA and KYC registries.
	CSC            []string `json:"csc,omitempty"` // special categories
	RiskReviewedOn string   `json:"risk_reviewed_on,omitempty"`
	KRAStatus      string   `json:"kra_status,omitempty"`
	KRARemark      string   `json:"kra_remark,omitempty"`
	CKYC           string   `json:"ckyc,omitempty"`
	ReKYCOn        string   `json:"rekyc_on,omitempty"`
	ReKYCMode      string   `json:"rekyc_mode,omitempty"`
	// PhysicalNotes: the client did not opt for electronic contract notes
	// and statements; they are printed and sent by courier.
	PhysicalNotes bool `json:"physical_notes,omitempty"`
	// ECNAuth is the client's specific authorisation of electronic
	// contract notes, with how it was given (onboardkit.go).
	ECNAuth *ECNAuthorisation `json:"ecn_auth,omitempty"`
	// FundsLedger is where this client's money goes when it comes in with
	// no other instruction, with a separate commodity ledger
	// (segledger.go): EQ or COM; empty waits for a person to choose.
	FundsLedger string `json:"funds_ledger,omitempty"`
	// NoInsights: the client turned account insights off in the portal.
	NoInsights bool `json:"no_insights,omitempty"`
	// WAConsent records the client's WhatsApp choice: when, how and by whom.
	WAConsent *WAConsent `json:"wa_consent,omitempty"`
	// DataConsent is the consent to collecting sensitive personal data
	// (bank, photograph, signature) recorded on the branch path;
	// KYCSource is where the KYC record came from (CKYC records may be
	// used only to verify identity and address); ErasedOn and ErasedRef
	// mark a closed client whose personal data was erased once the
	// retention period ended (dataprivacy.go).
	DataConsent *DataConsent `json:"data_consent,omitempty"`
	KYCSource   string       `json:"kyc_source,omitempty"`
	ErasedOn    string       `json:"erased_on,omitempty"`
	ErasedRef   string       `json:"erased_ref,omitempty"`
	// Nominees (up to 3 from 1 Sep 2026; see nominees.go) with each one's
	// share and optional identity; NominationOn is when the choice
	// (nominate or opt out) was made and NominationMode how it was
	// declared. NomineesOnStatement: the client chose to have the
	// nominees' names on statements (otherwise Yes/No).
	Nominees            []Nominee `json:"nominees,omitempty"`
	NominationOn        string    `json:"nomination_on,omitempty"`
	NominationMode      string    `json:"nomination_mode,omitempty"`
	NomineesOnStatement bool      `json:"nominees_on_statement,omitempty"`
	// SegStatus is each exchange segment's own status (Active, Suspended,
	// Dormant, Reactivated, Closed) and SegHistory every change to it.
	SegStatus  map[string]SegmentState `json:"seg_status,omitempty"`
	SegHistory []SegmentChange         `json:"seg_history,omitempty"`
	// Banks are all the client's registered bank accounts once there is
	// more than one (empty: Bank is the only one); Bank is always the
	// primary. See clientbanks.go.
	Banks []BankAccount `json:"banks,omitempty"`
	// Demats are all the client's demat accounts once there is more than
	// one (empty: Demat is the only one); Demat is always the primary.
	// PayoutDemat is the account the client chose for pay-outs of
	// securities (empty: the primary). See demats.go.
	Demats      []Demat `json:"demats,omitempty"`
	PayoutDemat string  `json:"payout_demat,omitempty"`
	// DPOnly: a client of the firm's DP only, with no trading account
	// (dponly.go): no segments, no exchange UCC, front-office files,
	// limits, contract notes or running account settlement.
	DPOnly bool `json:"dp_only,omitempty"`
	// FATCA is the client's FATCA/CRS self-certification (fatca.go): tax
	// residency with TINs, place and country of birth, nationality, US
	// person, controlling persons, the account's classification, its
	// validity and date, and where it stands at the KRA. Required for a new
	// client; nil for a client from the previous system until recorded.
	FATCA *FATCADecl `json:"fatca,omitempty"`
	// Incomplete lists, in words ("Mobile", "PAN", "Bank account number",
	// "Address"…), the details the previous system did not have, or had in
	// a form Ledgerline cannot accept, when an inactive client was migrated
	// with them as they were. They are completed before the client may be
	// reactivated; see incomplete.go.
	Incomplete []string `json:"incomplete,omitempty"`
	// MNRL: the client's registered mobile, or a joint holder's, is on
	// DoT's Mobile Number Revocation List; SMS and WhatsApp to the number
	// are held until the match is cleared (mnrl.go).
	MNRL []MNRLFlag `json:"mnrl,omitempty"`
	// Deceased: the KRA reported the client as deceased; debits (payouts,
	// demat debits, orders) are blocked until transmission (deceased.go).
	Deceased *DeceasedFlag `json:"deceased,omitempty"`
	// Sanctions: a sanctions case holds the client (sanctions.go): a
	// possible match being verified, or a confirmed designated person.
	// Trading, payouts, demat debits and other transactions are stopped.
	Sanctions *SanctionsFlag `json:"sanctions,omitempty"`
	// KYCHolds: the client is on an exchange's or CDSL's KYC list (not
	// validated by the KRA, not permitted to trade, KYC on hold); the KYC
	// trading gate holds the client back while any trading list does
	// (kycgate.go).
	KYCHolds []KYCHold `json:"kyc_holds,omitempty"`
	// PROI is the onboarding record of an individual resident outside
	// India (NRI, OCI, foreign national), required from 3 Dec 2026
	// (proi.go); NRIAccount is the NRI's account type (NRE or NRO, from
	// NRI (PIS)), which sets the NSE UCC category (11 or 37).
	PROI       *PROIKYC `json:"proi,omitempty"`
	NRIAccount string   `json:"nri_account,omitempty"`
}

var (
	rePAN    = regexp.MustCompile(`^[A-Z]{5}[0-9]{4}[A-Z]$`)
	reIFSC   = regexp.MustCompile(`^[A-Z]{4}0[A-Z0-9]{6}$`)
	reMobile = regexp.MustCompile(`^[6-9][0-9]{9}$`)
	reEmail  = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
	reAcct   = regexp.MustCompile(`^[0-9]{9,18}$`)
	reUCC    = regexp.MustCompile(`^[A-Z0-9]{3,10}$`)
	reGSTIN  = regexp.MustCompile(`^[0-9]{2}[A-Z]{5}[0-9]{4}[A-Z][1-9A-Z]Z[0-9A-Z]$`)
)

// FieldError is one validation problem on a form field.
type FieldError struct {
	Field string `json:"field"`
	Msg   string `json:"message"`
}

// ValidationError collects field errors.
type ValidationError struct{ Fields []FieldError }

func (v *ValidationError) Error() string {
	var s []string
	for _, f := range v.Fields {
		s = append(s, f.Field+": "+f.Msg)
	}
	return "invalid: " + strings.Join(s, "; ")
}
func (v *ValidationError) Unwrap() error { return ErrInvalid }

// Normalise cleans user input in place.
func (c *Client) Normalise() {
	c.UCC = strings.ToUpper(strings.TrimSpace(c.UCC))
	c.Name = strings.Join(strings.Fields(c.Name), " ")
	c.PAN = strings.ToUpper(strings.TrimSpace(c.PAN))
	c.Bank.IFSC = strings.ToUpper(strings.TrimSpace(c.Bank.IFSC))
	c.Bank.Account = strings.TrimSpace(c.Bank.Account)
	syncBanks(c)
	c.Email = strings.TrimSpace(strings.ToLower(c.Email))
	c.Mobile = strings.TrimSpace(c.Mobile)
	c.City = strings.TrimSpace(c.City)
	if c.Type == "" {
		c.Type = "Individual"
	}
	if c.RAS == "" && !c.DPOnly {
		c.RAS = "Quarterly"
	}
	if c.Risk == "" {
		c.Risk = "Medium"
	}
	normaliseNominees(c)
	if c.Nominee == "" {
		c.Nominee = "Opted out"
	}
	c.Demat.ID = normDematID(c.Demat.ID)
	c.Demat.Depository = strings.ToUpper(strings.TrimSpace(c.Demat.Depository))
	if c.Demat.Depository == "" {
		c.Demat.Depository = "CDSL"
		if strings.HasPrefix(c.Demat.ID, "IN") {
			c.Demat.Depository = "NSDL"
		}
	}
	syncDemats(c)
	c.PayoutDemat = normDematID(c.PayoutDemat)
	seen := map[string]bool{}
	var segs []string
	for _, s := range c.Segments {
		s = strings.ToUpper(strings.TrimSpace(s))
		if SegmentNames[s] != "" && !seen[s] {
			segs = append(segs, s)
			seen[s] = true
		}
	}
	sort.Slice(segs, func(i, j int) bool { return segOrder(segs[i]) < segOrder(segs[j]) })
	c.Segments = segs
}

func segOrder(s string) int { return strings.Index("CM FO CD COM", s) }

// Validate checks a client record.
func (c Client) Validate() error {
	var fe []FieldError
	add := func(f, m string) { fe = append(fe, FieldError{f, m}) }
	if c.UCC != "" && !reUCC.MatchString(c.UCC) {
		add("ucc", "UCC is 3 to 10 letters or digits")
	}
	if strings.EqualFold(c.UCC, SuspenseUCC) {
		// The back-office code for unidentified receipts in SEGMON; never
		// a client, in the exchanges' UCC database or the front end
		// (NCL/CMPL/64088).
		add("ucc", "SUSPE1234N is kept for unidentified receipts in the client collateral report: give the client another code")
	}
	if len(c.Name) < 3 {
		add("name", "Enter the full name as per PAN")
	}
	if !rePAN.MatchString(c.PAN) {
		add("pan", "PAN must be 5 letters, 4 digits, 1 letter")
	} else {
		want := map[string]byte{"Individual": 'P', "NRI": 'P', "HUF": 'H', "Corporate": 'C'}[c.Type]
		if want != 0 && c.PAN[3] != want {
			add("pan", fmt.Sprintf("The 4th character of a %s PAN is %c", strings.ToLower(c.Type), want))
		}
	}
	switch c.Type {
	case "Individual", "HUF", "Corporate", "NRI":
	default:
		add("type", "Type must be Individual, HUF, Corporate or NRI")
	}
	switch {
	case reMobile.MatchString(c.Mobile):
	case c.Type == "NRI" && reIntlMobile.MatchString(c.Mobile):
		// A client resident outside India may give an international
		// mobile (SEBI's PROI KYC circular, para 5.5.7; proi.go).
	case c.Type == "NRI":
		add("mobile", "Enter a 10-digit Indian mobile number, or an international one with + and the country code")
	default:
		add("mobile", "Enter a 10-digit Indian mobile number")
	}
	if !reEmail.MatchString(c.Email) {
		add("email", "Enter a valid email address")
	}
	if c.City == "" {
		add("city", "Enter the city")
	}
	if strings.TrimSpace(c.Bank.Bank) == "" {
		add("bank.bank", "Enter the bank name")
	}
	if !reIFSC.MatchString(c.Bank.IFSC) {
		add("bank.ifsc", "IFSC is 4 letters, 0, then 6 characters")
	}
	if !reAcct.MatchString(c.Bank.Account) {
		add("bank.account", "Account number is 9 to 18 digits")
	}
	fe = append(fe, checkBanks(c.Banks)...)
	fe = append(fe, checkDemats(c.Demats)...)
	if c.PayoutDemat != "" {
		if d, ok := dematOf(c, c.PayoutDemat); !ok || !d.active() {
			add("payout_demat", "The account for pay-outs of securities must be an active demat account of the client")
		}
	}
	switch {
	case c.DPOnly:
		// A client of the DP only: no trading account, so no segments, no
		// running account settlement and no derivatives income proof
		// (dponly.go).
		if len(c.Segments) > 0 {
			add("segments", "A DP-only client has no trading segments; open a trading account to add them")
		}
		if c.RAS != "" && c.RAS != "Monthly" && c.RAS != "Quarterly" {
			add("ras", "Running account settlement must be Monthly or Quarterly")
		}
	default:
		if len(c.Segments) == 0 {
			add("segments", "Choose at least one segment")
		}
		for _, s := range c.Segments {
			if s != "CM" && !c.IncomeProof {
				add("income_proof", "Derivatives segments need income proof")
				break
			}
		}
		if c.RAS != "Monthly" && c.RAS != "Quarterly" {
			add("ras", "Running account settlement must be Monthly or Quarterly")
		}
	}
	if c.Nominee == "Opted in" && strings.TrimSpace(c.NomineeName) == "" {
		add("nominee_name", "Enter the nominee name")
	}
	// A nomination of more than 3 made before 1 Sep 2026 is kept (and
	// flagged) when other details change; new ones are held to 3 where
	// they are made (createApplication, nomination changes).
	fe = append(fe, checkNominees(c.Nominees, time.Now(), legacyMaxNominees)...)
	if len(fe) > 0 {
		return &ValidationError{fe}
	}
	return nil
}

// Clients lists every client.
func (o *Ops) Clients(ctx context.Context, tenant string) ([]Client, error) {
	return docs.List[Client](ctx, o.Docs, tenant, cClient, "")
}

// ClientMap indexes clients by UCC.
func (o *Ops) ClientMap(ctx context.Context, tenant string) (map[string]Client, error) {
	cs, err := o.Clients(ctx, tenant)
	if err != nil {
		return nil, err
	}
	m := make(map[string]Client, len(cs))
	for _, c := range cs {
		m[c.UCC] = c
	}
	return m, nil
}

// Client returns one client.
func (o *Ops) Client(ctx context.Context, tenant, ucc string) (Client, error) {
	c, _, err := docs.Get[Client](ctx, o.Docs, tenant, cClient, strings.ToUpper(ucc))
	return c, err
}

// CreateApplication starts onboarding. A UCC is assigned when none is given.
func (o *Ops) CreateApplication(ctx context.Context, tenant, actor string, c Client) (Client, error) {
	if err := o.require(ctx, tenant, actor, "Creating a client", RoleMaker, RoleChecker); err != nil {
		return Client{}, err
	}
	return o.createApplication(ctx, tenant, actor, c)
}

// createApplication validates and stores a new application; callers check
// who may create it.
func (o *Ops) createApplication(ctx context.Context, tenant, actor string, c Client) (Client, error) {
	c.Normalise()
	if c.Nominee == "" {
		c.Nominee = "Opted out"
	}
	// The risk disclosure document is about trading: a client of the DP
	// only acknowledges it when opening a trading account (dponly.go).
	if !c.RiskDisclosure && !c.DPOnly {
		return Client{}, &ValidationError{[]FieldError{{"risk_disclosure", "Risk disclosure acknowledgement is required"}}}
	}
	if err := c.Validate(); err != nil {
		return Client{}, err
	}
	if len(c.Nominees) > MaxNominees {
		return Client{}, &ValidationError{[]FieldError{{"nominees", fmt.Sprintf("At most %d nominees", MaxNominees)}}}
	}
	all, err := o.Clients(ctx, tenant)
	if err != nil {
		return Client{}, err
	}
	for _, x := range all {
		if x.PAN == c.PAN {
			return Client{}, &ValidationError{[]FieldError{{"pan", "A client with this PAN already exists (" + x.UCC + ")"}}}
		}
	}
	if d, hit := o.IsDebarredPAN(ctx, tenant, c.PAN, o.now().Format("2006-01-02")); hit {
		o.log(ctx, tenant, actor, "onboarding.debarred", c.PAN, map[string]any{"order": d.OrderNo, "regulator": d.Regulator})
		if isSanctionsList(listTypeOf(d)) {
			// A designated person sought an account: a case to report
			// (sanctions.go).
			o.screenApplicant(ctx, tenant, actor, c.Name, c.PAN, c.DOB)
		}
		return Client{}, &ValidationError{[]FieldError{{"pan", "This PAN is debarred from the securities market (" + d.Regulator + " order " + d.OrderNo + "); the account cannot be opened"}}}
	}
	// The FATCA/CRS self-certification is part of the account opening
	// (SEBI/HO/MIRSD/SECFATF/P/CIR/2024/12; SEBI circular of 11 November
	// 2026, para 6); it goes to the KRA with the KYC (fatca.go).
	mode := FATCAModeForm
	if strings.HasPrefix(actor, "applicant:") {
		mode = FATCAModeESign
	}
	if err := requireFATCA(&c, actor, mode, o.now().Format(time.RFC3339)); err != nil {
		return Client{}, err
	}
	if c.UCC == "" {
		// A code never equals the firm's own (onboardx.go): one that would
		// is skipped.
		for try := 0; try < 5 && (c.UCC == "" || o.UCCCollision(ctx, tenant, c.UCC) != ""); try++ {
			n, err := o.next(ctx, tenant, "ucc", actor)
			if err != nil {
				return Client{}, err
			}
			c.UCC = fmt.Sprintf("BH%d", 2000+n)
		}
	}
	if why := o.UCCCollision(ctx, tenant, c.UCC); why != "" {
		return Client{}, &ValidationError{[]FieldError{{"ucc", "This client code cannot be used: " + why}}}
	}
	c.Status = "KYC verification"
	c.KYCDate = o.now().Format("2006-01-02")
	c.CreatedBy = actor
	c.UPIBlock, c.Blocked, c.LastTrade = 0, false, ""
	// The consent to collecting sensitive personal data (IT Rules 2011,
	// rule 5; dataprivacy.go): the e-signed application online; on the
	// branch path, the signed form's reference when staff give it.
	if strings.HasPrefix(actor, "applicant:") {
		c.DataConsent = &DataConsent{At: o.now().Format(time.RFC3339), Source: "e-Sign", Ref: "online application", By: actor}
	} else if c.DataConsent != nil {
		if strings.TrimSpace(c.DataConsent.Ref) == "" {
			return Client{}, &ValidationError{[]FieldError{{"data_consent", "Give the form number of the signed consent"}}}
		}
		c.DataConsent = &DataConsent{At: o.now().Format(time.RFC3339), Source: "Signed form", Ref: trim(strings.TrimSpace(c.DataConsent.Ref), 80), By: actor}
	}
	c.ErasedOn, c.ErasedRef = "", ""
	c.WAConsent = nil
	if c.WhatsApp {
		c.setConsent(true, o.now().Format(time.RFC3339), SrcOnboarding, actor, "application "+c.UCC)
	}
	if c.Partner != "" {
		o.ensureAPBaseline(ctx, tenant) // before the client exists, so it is not taken as an existing mapping
	}
	if _, err := docs.Put(ctx, o.Docs, tenant, cClient, c.UCC, c, 0, actor); err != nil {
		return Client{}, err
	}
	o.log(ctx, tenant, actor, "client.application.created", c.UCC, map[string]any{"pan": MaskPAN(c.PAN), "segments": c.Segments, "dp_only": c.DPOnly})
	// Screened against the UNSC, UAPA and WMD lists (and the exchanges'
	// defaulting clients) before it can be activated (sanctions.go).
	o.screenNewClient(ctx, tenant, actor, c)
	if x, err := o.Client(ctx, tenant, c.UCC); err == nil {
		c = x
	}
	// A new client of an AP, for the weekly report of clients mapped to APs.
	o.recordAPChange(ctx, tenant, actor, c, "", c.Partner, APViaOpening, true)
	return c, nil
}

// AdvanceStage moves a client to the next onboarding stage. Activation
// needs an Ops checker who is not the person who created the application.
func (o *Ops) AdvanceStage(ctx context.Context, tenant, actor, ucc string) (Client, error) {
	c, err := o.Client(ctx, tenant, ucc)
	if err != nil {
		return Client{}, err
	}
	i := -1
	for k, s := range Stages {
		if s == c.Status {
			i = k
		}
	}
	if c.Status == "In-person verification" { // a stage that no longer exists
		i = 1
	}
	if i < 0 || i == len(Stages)-1 {
		return Client{}, fmt.Errorf("%w: %s is %s and cannot be advanced", ErrState, ucc, c.Status)
	}
	next := Stages[i+1]
	if next == "UCC upload" && c.DPOnly {
		next = "Active" // no trading account, so nothing goes to the exchanges' UCC (dponly.go)
	}
	if next == "Active" || next == "UCC upload" {
		// No account for a designated person: an open sanctions case stops
		// the client going to the exchanges or being activated.
		if err := o.sanctionsGate(ctx, tenant, c.UCC); err != nil {
			return Client{}, err
		}
	}
	if next == "Active" {
		if err := o.require(ctx, tenant, actor, "Activating a client", RoleChecker); err != nil {
			return Client{}, err
		}
		if c.CreatedBy == actor {
			return Client{}, fmt.Errorf("%w: the person who created the application cannot activate it", ErrForbidden)
		}
		// The CDD must be complete before the account relationship starts
		// (kycgate.go); the KRA's verification is waited for by the KYC
		// gate on the front ends.
		if p := o.activationProblems(ctx, tenant, c); len(p) > 0 {
			return Client{}, fmt.Errorf("%w: %s cannot be activated: %s", ErrState, c.UCC, strings.Join(p, "; "))
		}
	} else if err := o.require(ctx, tenant, actor, "Advancing onboarding", RoleMaker, RoleChecker); err != nil {
		return Client{}, err
	}
	c, err = docs.Update(ctx, o.Docs, tenant, cClient, c.UCC, actor, func(x *Client) error {
		if x.Status != c.Status {
			return docs.ErrStale
		}
		x.Status = next
		if next == "Active" {
			x.ActivatedBy = actor
		}
		return nil
	})
	if err != nil {
		return Client{}, err
	}
	o.log(ctx, tenant, actor, "client.stage", ucc, map[string]any{"stage": next})
	return c, nil
}

// SetBlocked turns a client's kill switch on or off (Risk manager).
func (o *Ops) SetBlocked(ctx context.Context, tenant, actor, ucc string, blocked bool, reason string) (Client, error) {
	if err := o.require(ctx, tenant, actor, "The kill switch", RoleRisk); err != nil {
		return Client{}, err
	}
	if blocked && strings.TrimSpace(reason) == "" {
		return Client{}, invalid("give a reason for blocking the client")
	}
	c, err := docs.Update(ctx, o.Docs, tenant, cClient, strings.ToUpper(ucc), actor, func(x *Client) error {
		if x.Status != "Active" {
			return fmt.Errorf("%w: only active clients can be blocked", ErrState)
		}
		if x.Deceased != nil && !blocked {
			// Lifted only when the intimation is found false (deceased.go).
			return fmt.Errorf("%w: %s; the block is lifted only if the intimation is found false, on Compliance → Deceased clients", ErrState, deceasedWhy(*x))
		}
		x.Blocked, x.BlockedReason = blocked, reason
		if !blocked {
			x.BlockedReason = ""
		}
		return nil
	})
	if err != nil {
		return Client{}, err
	}
	action := "rms.kill.on"
	if !blocked {
		action = "rms.kill.off"
	}
	o.log(ctx, tenant, actor, action, c.UCC, map[string]any{"reason": reason})
	return c, nil
}

// Public returns a copy safe for display: account numbers and PAN masked.
func (c Client) Public() Client {
	c.Bank.Account = MaskAccount(c.Bank.Account)
	c.Banks = maskBanks(registeredBanks(c))
	c.PAN = MaskPAN(c.PAN)
	c.Nominees = maskNominees(c.Nominees)
	if len(c.MNRL) > 0 {
		fs := make([]MNRLFlag, len(c.MNRL))
		for i, f := range c.MNRL {
			f.Mobile = maskMobile(f.Mobile)
			fs[i] = f
		}
		c.MNRL = fs
	}
	c.FATCA = c.FATCA.masked()
	c.PROI = c.PROI.masked()
	return c
}
