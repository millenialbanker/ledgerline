package ops

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
)

// ---- onboarding details the client master does not hold ----
//
// One record per client (ClientOnboarding) keeps what the account-opening
// rules ask for beyond the client master and the UCC details:
//
//   - the purpose and intended nature of the relationship (SEBI KYC master
//     circular SEBI/HO/MIRSD/SECFATF/P/CIR/2023/169, para 12);
//   - a video in-person verification (VIPV) of a resident client (the same
//     circular's VIPV conditions: a trained official, informed consent, a
//     live recording with random questions, the OVD shown, the photograph
//     matched, stored tamper-proof with the date, time and location);
//   - a person with disability: the disability as CDSL records it
//     (communiqué DP2026-461: MDP, OTH or NO), a guardian who signs where
//     needed and is KYC-compliant himself, and the liveness check used
//     instead of blinking (NSE/INSP/73976 FAQ on account opening by persons
//     with disability);
//   - an HUF's Karta and coparceners (CDSL DP2026-218 and DP2026-253), and
//     an NPO's NITI Aayog DARPAN ID (CDSL/OPS/DP/POLCY/2026/470 and 509),
//     which go to CDSL with the BO record (cdslbomap.go, cdslharmon.go);
//   - the client's written opt-out from an exchange (SEBI stock broker
//     master circular SEBI/HO/MIRSD/MIRSD-PoD/P/CIR/2025/90, para 21.8):
//     no UCC is sent to that exchange while it stands;
//   - a corporate's board resolution for commodity derivatives before MCX
//     is enabled (MCX/INSP/255/2026 para 1.1 (i));
//   - the approval of a second trading code for the same PAN on the same
//     exchange (NSE inspection master circular, Annexure 1.1 item 96);
//   - the e-mail to MCX surveillance for a client with a Sikkim address
//     (MCX/S&I/251/2026 para 1.2).
//
// The firm's own codes (its trading member codes and proprietary codes,
// with the one designated ERROR account) are kept in UCCControls: no
// client UCC may equal any of them (NSE Clearing NCL/CMPT/68965, circular
// 201/2025, points 2-6; NSE/SURV/74008, circular 313/2026).
//
// Each check is on Compliance → KYC trading gate → Onboarding checks; the
// ones that make an account unfit to open stop activation
// (activationProblems), the rest raise work items daily (onboardDaily) and
// are self-inspection tests.

const (
	cOnboardX     = "onboardx"
	cUCCControls  = "ucccontrols"
	wOnboard      = "onboarding"
	mcxSikkimMail = "surveillance@mcxindia.com"
)

// OnboardPurposes are the purposes a client gives for the relationship.
// The circular asks for the purpose and intended nature, not for a code
// list: these are the firm's choices, with a note for anything else.
var OnboardPurposes = []string{"Long-term investment", "Trading", "Investment and trading", "Hedging", "Other"}

// Disability values as CDSL's BO record takes them (UDiFF field
// MntlDsblty): MDP a person with mental disability (with a guardian), OTH
// other disability, NO not applicable.
var Disabilities = map[string]string{"NO": "No disability", "MDP": "Person with mental disability (guardian operates)", "OTH": "Other disability"}

// LivenessMethods are the ways liveness is checked: blinking, or the
// alternatives NSE's FAQ lists for persons who cannot blink.
var LivenessMethods = []string{"Blinking", "Facial expression", "Head nod", "Reading an OTP shown on screen", "Assisted video capture"}

// Exchanges a client may opt out of.
var OptOutExchanges = []string{"NSE", "BSE", "MCX"}

// ClientOnboarding is one client's record.
type ClientOnboarding struct {
	UCC         string `json:"ucc"`
	Purpose     string `json:"purpose,omitempty"`
	PurposeNote string `json:"purpose_note,omitempty"`

	Disability     string           `json:"disability,omitempty"` // NO, MDP, OTH
	Guardian       *OnboardGuardian `json:"guardian,omitempty"`
	LivenessMethod string           `json:"liveness_method,omitempty"`

	VIPV *VIPVRecord `json:"vipv,omitempty"`

	Karta       *OnboardPerson  `json:"karta,omitempty"`
	Coparceners []OnboardPerson `json:"coparceners,omitempty"`

	DarpanID  string `json:"darpan_id,omitempty"`
	DarpanRef string `json:"darpan_ref,omitempty"` // the registration certificate kept

	OptOuts []ExchangeOptOut `json:"opt_outs,omitempty"`

	BoardResolution *BoardResolution   `json:"board_resolution,omitempty"`
	SecondUCC       *SecondUCCApproval `json:"second_ucc,omitempty"`

	SikkimMailedOn string `json:"sikkim_mailed_on,omitempty"`
	SikkimMailRef  string `json:"sikkim_mail_ref,omitempty"`

	UpdatedBy string `json:"updated_by,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// OnboardGuardian signs for a person with disability where needed; he is
// KYC-compliant himself.
type OnboardGuardian struct {
	Signs    bool   `json:"signs"`
	Name     string `json:"name,omitempty"`
	PAN      string `json:"pan,omitempty"`
	Relation string `json:"relation,omitempty"`
	KYCOK    bool   `json:"kyc_ok"` // the guardian's own KYC is registered at a KRA
}

// OnboardPerson is a Karta or a coparcener of an HUF. Relation is CDSL's
// relationship code (SPO, SON, DAU, FAT, MOT, BRO, SIS…: RltshWthBnfclOwnr).
type OnboardPerson struct {
	Name     string `json:"name"`
	Relation string `json:"relation,omitempty"`
	DOB      string `json:"dob,omitempty"`
	PAN      string `json:"pan,omitempty"`
	Mobile   string `json:"mobile,omitempty"`
	Email    string `json:"email,omitempty"`
}

// ExchangeOptOut is the client's written negative consent for an exchange.
type ExchangeOptOut struct {
	Exchange string `json:"exchange"`
	On       string `json:"on"`
	Document string `json:"document"` // the signed letter kept
	By       string `json:"by,omitempty"`
	At       string `json:"at,omitempty"`
}

// BoardResolution permits a corporate client to trade commodity
// derivatives.
type BoardResolution struct {
	On       string `json:"on"`
	Document string `json:"document"`
}

// SecondUCCApproval allows the client a second active code on an exchange
// (the other code, why, who approved it).
type SecondUCCApproval struct {
	OtherUCC   string `json:"other_ucc"`
	Reason     string `json:"reason"`
	ApprovedBy string `json:"approved_by"`
	On         string `json:"on"`
}

// VIPVRecord is a video in-person verification: who did it, the client's
// consent, the live recording and its fingerprint, the random questions
// asked, the OVD shown, the photograph matched, and when and where.
type VIPVRecord struct {
	Official          string   `json:"official"`            // name and employee code
	OfficialTrainedOn string   `json:"official_trained_on"` // the official's VIPV training
	Consent           bool     `json:"consent"`
	At                string   `json:"at"`                // YYYY-MM-DDTHH:MM, IST
	Geo               string   `json:"geo"`               // latitude,longitude at the time
	GeoCountry        string   `json:"geo_country"`       // ISO 3166-1 alpha-2
	RecordingRef      string   `json:"recording_ref"`     // where the recording is kept
	RecordingSHA256   string   `json:"recording_sha256"`  // its fingerprint when stored
	Questions         []string `json:"questions"`         // random questions asked
	OVDShown          string   `json:"ovd_shown"`         // the OVD the client displayed
	PhotoMatch        bool     `json:"photo_match"`       // with the Aadhaar or OVD photograph
	Liveness          bool     `json:"liveness"`          // liveness checked
	LivenessMethod    string   `json:"liveness_method"`   // how (LivenessMethods)
	Storage           string   `json:"storage,omitempty"` // tamper-proof store
}

var (
	reGeo    = regexp.MustCompile(`^-?[0-9]{1,2}(\.[0-9]+)?\s*,\s*-?[0-9]{1,3}(\.[0-9]+)?$`)
	reSHA256 = regexp.MustCompile(`^[0-9a-f]{64}$`)
	reDarpan = regexp.MustCompile(`^[A-Z]{2}/[0-9]{4}/[0-9]{7}$`)
)

// vipvProblems lists what a VIPV record lacks. resident: the client lives
// in India, so the location is India.
func vipvProblems(v *VIPVRecord, resident bool) []string {
	if v == nil {
		return []string{"video IPV not recorded"}
	}
	var miss []string
	for _, x := range []struct {
		ok   bool
		name string
	}{
		{strings.TrimSpace(v.Official) != "", "the official"}, {validDate(v.OfficialTrainedOn), "the official's VIPV training"},
		{v.Consent, "the client's informed consent"}, {vipvAt(v.At) != "", "the date and time"},
		{reGeo.MatchString(strings.TrimSpace(v.Geo)), "the location (latitude, longitude)"},
		{!resident || v.GeoCountry == "IN", "a location in India"},
		{strings.TrimSpace(v.RecordingRef) != "", "the recording's reference"}, {reSHA256.MatchString(v.RecordingSHA256), "the recording's fingerprint (SHA-256)"},
		{len(v.Questions) > 0, "the random questions asked"}, {strings.TrimSpace(v.OVDShown) != "", "the OVD shown"},
		{v.PhotoMatch, "the photograph matched"}, {v.Liveness && v.LivenessMethod != "", "the liveness check and how it was done"},
	} {
		if !x.ok {
			miss = append(miss, x.name)
		}
	}
	if len(miss) == 0 {
		return nil
	}
	return []string{"video IPV lacks " + strings.Join(miss, ", ")}
}

func vipvAt(s string) string {
	for _, l := range []string{time.RFC3339, "2006-01-02T15:04"} {
		if t, err := time.ParseInLocation(l, strings.TrimSpace(s), ist); err == nil {
			return t.Format(time.RFC3339)
		}
	}
	return ""
}

// normVIPV tidies a record before it is stored.
func normVIPV(v *VIPVRecord) error {
	if v == nil {
		return nil
	}
	v.GeoCountry = strings.ToUpper(strings.TrimSpace(v.GeoCountry))
	v.RecordingSHA256 = strings.ToLower(strings.TrimSpace(v.RecordingSHA256))
	var qs []string
	for _, q := range v.Questions {
		if q = strings.TrimSpace(q); q != "" {
			qs = append(qs, q)
		}
	}
	v.Questions = qs
	if v.At != "" && vipvAt(v.At) == "" {
		return invalid("the VIPV date and time is YYYY-MM-DDTHH:MM")
	}
	if v.OfficialTrainedOn != "" && !validDate(v.OfficialTrainedOn) {
		return invalid("the official's training date is YYYY-MM-DD")
	}
	if v.LivenessMethod != "" && !contains(LivenessMethods, v.LivenessMethod) {
		return invalid("the liveness check is one of %s", strings.Join(LivenessMethods, ", "))
	}
	return nil
}

// kycByVIPV: the client's IPV was done by video.
func kycByVIPV(c Client) bool { return strings.Contains(strings.ToUpper(c.KYCRoute), "VIPV") }

// OnboardingFor returns the client's record (empty when none).
func (o *Ops) OnboardingFor(ctx context.Context, tenant, ucc string) ClientOnboarding {
	ucc = strings.ToUpper(strings.TrimSpace(ucc))
	x, _, _ := docs.GetOr(ctx, o.Docs, tenant, cOnboardX, ucc, ClientOnboarding{UCC: ucc})
	return x
}

func (o *Ops) onboardings(ctx context.Context, tenant string) map[string]ClientOnboarding {
	xs, _ := docs.List[ClientOnboarding](ctx, o.Docs, tenant, cOnboardX, "")
	m := map[string]ClientOnboarding{}
	for _, x := range xs {
		m[x.UCC] = x
	}
	return m
}

// SaveOnboarding stores a client's record (Ops maker or checker, or
// Compliance). The UCC, who and when are set here.
func (o *Ops) SaveOnboarding(ctx context.Context, tenant, actor, ucc string, x ClientOnboarding) (ClientOnboarding, error) {
	if err := o.require(ctx, tenant, actor, "Recording onboarding details", RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return x, err
	}
	c, err := o.Client(ctx, tenant, ucc)
	if err != nil {
		return x, err
	}
	x.UCC = c.UCC
	x.Purpose, x.PurposeNote = strings.TrimSpace(x.Purpose), strings.TrimSpace(x.PurposeNote)
	if x.Purpose != "" && !contains(OnboardPurposes, x.Purpose) {
		return x, invalid("the purpose is one of %s", strings.Join(OnboardPurposes, ", "))
	}
	if x.Purpose == "Other" && x.PurposeNote == "" {
		return x, invalid("say what the purpose is")
	}
	x.Disability = strings.ToUpper(strings.TrimSpace(x.Disability))
	if _, ok := Disabilities[x.Disability]; x.Disability != "" && !ok {
		return x, invalid("the disability is MDP, OTH or NO")
	}
	if x.LivenessMethod != "" && !contains(LivenessMethods, x.LivenessMethod) {
		return x, invalid("the liveness check is one of %s", strings.Join(LivenessMethods, ", "))
	}
	if g := x.Guardian; g != nil {
		g.Name, g.PAN, g.Relation = strings.Join(strings.Fields(g.Name), " "), strings.ToUpper(strings.TrimSpace(g.PAN)), strings.TrimSpace(g.Relation)
		if g.PAN != "" && !rePAN.MatchString(g.PAN) {
			return x, invalid("the guardian's PAN is 5 letters, 4 digits and a letter")
		}
		if !g.Signs && g.Name == "" {
			x.Guardian = nil
		}
	}
	if err := normVIPV(x.VIPV); err != nil {
		return x, err
	}
	people := append([]OnboardPerson{}, x.Coparceners...)
	if x.Karta != nil {
		people = append(people, *x.Karta)
	}
	for _, p := range people {
		if strings.TrimSpace(p.Name) == "" {
			return x, invalid("every Karta and coparcener needs a name")
		}
		if p.PAN != "" && !rePAN.MatchString(strings.ToUpper(strings.TrimSpace(p.PAN))) {
			return x, invalid("the PAN of %s is 5 letters, 4 digits and a letter", p.Name)
		}
		if p.DOB != "" && !validDate(p.DOB) {
			return x, invalid("the date of birth of %s is YYYY-MM-DD", p.Name)
		}
	}
	for i := range x.Coparceners {
		x.Coparceners[i].PAN = strings.ToUpper(strings.TrimSpace(x.Coparceners[i].PAN))
		x.Coparceners[i].Relation = strings.ToUpper(strings.TrimSpace(x.Coparceners[i].Relation))
	}
	if x.Karta != nil {
		x.Karta.PAN = strings.ToUpper(strings.TrimSpace(x.Karta.PAN))
	}
	x.DarpanID = strings.ToUpper(strings.TrimSpace(x.DarpanID))
	if x.DarpanID != "" && !reDarpan.MatchString(x.DarpanID) {
		return x, invalid("the DARPAN ID is the state code, the year and a 7-digit number, as XX/2026/0123456")
	}
	now := o.now().Format(time.RFC3339)
	old := o.OnboardingFor(ctx, tenant, c.UCC)
	seen := map[string]bool{}
	var outs []ExchangeOptOut
	for _, e := range x.OptOuts {
		e.Exchange = strings.ToUpper(strings.TrimSpace(e.Exchange))
		if !contains(OptOutExchanges, e.Exchange) || seen[e.Exchange] {
			return x, invalid("an opt-out is from NSE, BSE or MCX, once each")
		}
		if !validDate(e.On) || strings.TrimSpace(e.Document) == "" {
			return x, invalid("an opt-out from %s needs its date and the signed letter's reference", e.Exchange)
		}
		seen[e.Exchange] = true
		for _, p := range old.OptOuts {
			if p.Exchange == e.Exchange && p.On == e.On {
				e.By, e.At = p.By, p.At
			}
		}
		if e.By == "" {
			e.By, e.At = actor, now
		}
		outs = append(outs, e)
	}
	x.OptOuts = outs
	if b := x.BoardResolution; b != nil && (b.On == "" && b.Document == "") {
		x.BoardResolution = nil
	} else if b != nil && (!validDate(b.On) || strings.TrimSpace(b.Document) == "") {
		return x, invalid("the board resolution needs its date and the document's reference")
	}
	if s := x.SecondUCC; s != nil {
		s.OtherUCC = strings.ToUpper(strings.TrimSpace(s.OtherUCC))
		if s.OtherUCC == "" && s.Reason == "" {
			x.SecondUCC = nil
		} else {
			if s.OtherUCC == "" || strings.TrimSpace(s.Reason) == "" || !validDate(s.On) {
				return x, invalid("the approval of a second code needs the other code, the reason and the date")
			}
			s.ApprovedBy = firstOf(old.secondApprover(s), actor)
			if s.ApprovedBy == actor {
				if err := o.require(ctx, tenant, actor, "Approving a second trading code", RoleCompliance); err != nil {
					return x, err
				}
			}
		}
	}
	if x.SikkimMailedOn != "" && !validDate(x.SikkimMailedOn) {
		return x, invalid("the date the e-mail went to MCX is YYYY-MM-DD")
	}
	x.UpdatedBy, x.UpdatedAt = actor, now
	if err := docs.Upsert(ctx, o.Docs, tenant, cOnboardX, x.UCC, x, actor); err != nil {
		return x, err
	}
	if x.SikkimMailedOn != "" {
		o.closeWork(ctx, tenant, actor, wOnboard, "sikkim:"+x.UCC, "e-mailed on "+x.SikkimMailedOn)
	}
	if x.SecondUCC != nil {
		o.closeWork(ctx, tenant, actor, wOnboard, "second:"+x.UCC, "approved by "+x.SecondUCC.ApprovedBy)
	}
	o.log(ctx, tenant, actor, "onboarding.recorded", x.UCC, map[string]any{"purpose": x.Purpose, "disability": x.Disability, "opt_outs": len(x.OptOuts)})
	return x, nil
}

// secondApprover keeps the approver of an unchanged approval.
func (x ClientOnboarding) secondApprover(s *SecondUCCApproval) string {
	if p := x.SecondUCC; p != nil && p.OtherUCC == s.OtherUCC && p.On == s.On && p.Reason == s.Reason {
		return p.ApprovedBy
	}
	return ""
}

// optedOut: the client has opted out of the exchange.
func (x ClientOnboarding) optedOut(ex string) bool {
	for _, e := range x.OptOuts {
		if e.Exchange == ex {
			return true
		}
	}
	return false
}

// optOutMap is, by UCC, the exchanges each client opted out of.
func (o *Ops) optOutMap(ctx context.Context, tenant string) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for u, x := range o.onboardings(ctx, tenant) {
		for _, e := range x.OptOuts {
			if out[u] == nil {
				out[u] = map[string]bool{}
			}
			out[u][e.Exchange] = true
		}
	}
	return out
}

// ---- the firm's own codes ----

// UCCControls are the firm's proprietary codes and its one designated
// ERROR account.
type UCCControls struct {
	// ErrorAccount is the single designated ERROR account, the
	// proprietary code trades are moved to when no client can take them.
	ErrorAccount string `json:"error_account"`
	// PropCodes are the firm's other proprietary UCCs (OWN when empty).
	PropCodes []string `json:"prop_codes"`
	UpdatedBy string   `json:"updated_by,omitempty"`
	UpdatedAt string   `json:"updated_at,omitempty"`
}

func (o *Ops) UCCControlsNow(ctx context.Context, tenant string) UCCControls {
	c, _, _ := docs.GetOr(ctx, o.Docs, tenant, cUCCControls, "main", UCCControls{})
	if c.PropCodes == nil {
		c.PropCodes = []string{}
	}
	return c
}

// SaveUCCControls stores them (Compliance or Admin).
func (o *Ops) SaveUCCControls(ctx context.Context, tenant, actor string, c UCCControls) (UCCControls, error) {
	if err := o.require(ctx, tenant, actor, "Setting the firm's proprietary codes", RoleCompliance); err != nil {
		return c, err
	}
	c.ErrorAccount = strings.ToUpper(strings.TrimSpace(c.ErrorAccount))
	if c.ErrorAccount != "" && !reUCC.MatchString(c.ErrorAccount) {
		return c, invalid("the ERROR account is a code of 3 to 10 letters and digits")
	}
	var ps []string
	for _, p := range c.PropCodes {
		if p = strings.ToUpper(strings.TrimSpace(p)); p != "" && !contains(ps, p) {
			if !reUCC.MatchString(p) {
				return c, invalid("%s is not a code of 3 to 10 letters and digits", p)
			}
			ps = append(ps, p)
		}
	}
	c.PropCodes = ps
	if cs, err := o.ClientMap(ctx, tenant); err == nil {
		for _, p := range append(append([]string{}, ps...), c.ErrorAccount) {
			if _, ok := cs[p]; ok && p != "" {
				return c, invalid("%s is a client's code; a proprietary code must differ from every client code", p)
			}
		}
	}
	c.UpdatedBy, c.UpdatedAt = actor, o.now().Format(time.RFC3339)
	err := docs.Upsert(ctx, o.Docs, tenant, cUCCControls, "main", c, actor)
	o.log(ctx, tenant, actor, "ucc.controls.saved", "main", map[string]any{"error_account": c.ErrorAccount, "prop_codes": ps})
	return c, err
}

// uccCollision says why a client code may not be used ("" when it may):
// it equals the firm's trading member code on an exchange, or a
// proprietary code (OWN, the ERROR account, the firm's own), or begins
// with ERROR.
func uccCollision(set Settings, ctl UCCControls, ucc string) string {
	u := strings.ToUpper(strings.TrimSpace(ucc))
	if u == "" {
		return ""
	}
	for _, ex := range sortedKeys(set.MemberCodes) {
		if strings.EqualFold(strings.TrimSpace(set.MemberCodes[ex]), u) {
			return "it is the firm's trading member code on " + ex
		}
	}
	switch {
	case u == "OWN":
		return "OWN is the proprietary code"
	case strings.HasPrefix(u, "ERROR"):
		return "codes beginning ERROR are proprietary"
	case u == ctl.ErrorAccount:
		return "it is the firm's ERROR account"
	case contains(ctl.PropCodes, u):
		return "it is one of the firm's proprietary codes"
	}
	return ""
}

// UCCCollision is uccCollision with the firm's settings.
func (o *Ops) UCCCollision(ctx context.Context, tenant, ucc string) string {
	set, _ := o.Settings(ctx, tenant)
	return uccCollision(set, o.UCCControlsNow(ctx, tenant), ucc)
}

// ---- the checks ----

// sikkimAddress: the correspondence or permanent address is in Sikkim
// (by the state, or a PIN code of 737).
func sikkimAddress(c Client, u UCCDetails) bool {
	for _, s := range []string{c.State, u.State, u.PermState} {
		if s = strings.ToUpper(strings.TrimSpace(s)); s == "SIKKIM" || s == "SK" || s == "IN-SK" {
			return true
		}
	}
	for _, p := range []string{u.PIN, u.PermPIN} {
		if strings.HasPrefix(strings.TrimSpace(p), "737") {
			return true
		}
	}
	return false
}

// isNPO: CDSL's BO status and sub-status of a non-profit organisation
// (status 29 sub 256, status 25 sub 265; CDSL/OPS/DP/POLCY/2026/470).
func isNPO(d CDSLBODetail) bool {
	cat, sub := strings.TrimLeft(d.Codes["bo_category"], "0"), strings.TrimLeft(d.Codes["sub_status"], "0")
	return (cat == "29" && sub == "256") || (cat == "25" && sub == "265") || sub == "256" || sub == "265"
}

// onboardEnv is what the checks look at, read once.
type onboardEnv struct {
	set     Settings
	ctl     UCCControls
	clients map[string]Client
	x       map[string]ClientOnboarding
	byPAN   map[string][]string // active trading codes per PAN
	today   string
}

func (o *Ops) onboardEnv(ctx context.Context, tenant string) onboardEnv {
	e := onboardEnv{x: o.onboardings(ctx, tenant), byPAN: map[string][]string{}, today: o.now().In(ist).Format("2006-01-02")}
	e.set, _ = o.Settings(ctx, tenant)
	e.ctl = o.UCCControlsNow(ctx, tenant)
	e.clients, _ = o.ClientMap(ctx, tenant)
	for _, u := range sortedKeys(e.clients) {
		c := e.clients[u]
		if c.DPOnly || c.PAN == "" || c.Status == "Closed" || c.Status == "Rejected" {
			continue
		}
		e.byPAN[c.PAN] = append(e.byPAN[c.PAN], u)
	}
	return e
}

// exchangesOf are the exchanges the client trades on: NSE and BSE for
// equity and F&O (and currency), MCX for commodity, less the ones the
// client opted out of.
func (e onboardEnv) exchangesOf(c Client) []string {
	var out []string
	x := e.x[c.UCC]
	add := func(ex string) {
		if !contains(out, ex) && !x.optedOut(ex) {
			out = append(out, ex)
		}
	}
	for _, s := range c.Segments {
		switch s {
		case "CM", "FO", "CD":
			add("NSE")
			add("BSE")
		case "COM":
			add("MCX")
		}
	}
	return out
}

// secondUCC is the other active code of the same PAN on a shared
// exchange, if any.
func (e onboardEnv) secondUCC(c Client) (string, string) {
	for _, u := range e.byPAN[c.PAN] {
		if u == c.UCC {
			continue
		}
		o := e.clients[u]
		if o.Status != "Active" && o.Status != "UCC upload" {
			continue
		}
		for _, ex := range e.exchangesOf(c) {
			if contains(e.exchangesOf(o), ex) {
				return u, ex
			}
		}
	}
	return "", ""
}

// OnboardIssue is one thing wrong with a client's onboarding.
type OnboardIssue struct {
	ID    string `json:"id"` // the rule
	What  string `json:"what"`
	Block bool   `json:"block"` // stops activation
}

// onboardIssues lists them for one client. u are its UCC details, d its
// CDSL details.
func (e onboardEnv) onboardIssues(c Client, u UCCDetails, d CDSLBODetail) []OnboardIssue {
	x := e.x[c.UCC]
	var out []OnboardIssue
	add := func(id string, block bool, f string, a ...any) {
		out = append(out, OnboardIssue{ID: id, What: fmt.Sprintf(f, a...), Block: block})
	}
	if why := uccCollision(e.set, e.ctl, c.UCC); why != "" {
		add("NCL-BSE-0055", true, "the client code %s may not be used: %s; close it and register a new code", c.UCC, why)
	}
	if x.Purpose == "" && !c.DPOnly {
		add("SEBI-KYCAML-0010", false, "purpose and intended nature of the relationship not recorded")
	}
	if kycByVIPV(c) && !isPROI(c) {
		for _, p := range vipvProblems(x.VIPV, true) {
			add("SEBI-KYCAML-0042", true, "%s", p)
		}
	} else if x.VIPV != nil {
		for _, p := range vipvProblems(x.VIPV, !isPROI(c)) {
			add("SEBI-KYCAML-0042", false, "%s", p)
		}
	}
	if x.Disability == "MDP" && (x.Guardian == nil || !x.Guardian.Signs) {
		add("NSE-0262", true, "a person with mental disability operates through a guardian: record the guardian who signs")
	}
	if g := x.Guardian; g != nil && g.Signs {
		var miss []string
		if g.Name == "" {
			miss = append(miss, "name")
		}
		if !rePAN.MatchString(g.PAN) {
			miss = append(miss, "PAN")
		}
		if !g.KYCOK {
			miss = append(miss, "the guardian's own KYC at a KRA")
		}
		if len(miss) > 0 {
			add("NSE-0262", true, "the guardian who signs lacks %s", strings.Join(miss, ", "))
		}
	}
	if x.Disability != "" && x.Disability != "NO" && x.LivenessMethod == "" {
		add("NSE-0262", false, "record how liveness was checked (blinking, or an alternative)")
	}
	if c.Type == "HUF" && primaryDemat(c).Depository == "CDSL" {
		if x.Karta == nil || len(x.Coparceners) == 0 {
			add("DP-0339", false, "the HUF's Karta and coparceners not recorded (they go to CDSL with the BO record)")
		}
	}
	if isNPO(d) && x.DarpanID == "" {
		add("DP-0342", false, "a non-profit organisation's NITI Aayog DARPAN ID not recorded")
	}
	if contains(c.Segments, "COM") && c.Type == "Corporate" && x.BoardResolution == nil && !x.optedOut("MCX") {
		add("MCX-N0021", true, "a corporate needs its board resolution for commodity derivatives before MCX is enabled")
	}
	if contains(c.Segments, "COM") && !x.optedOut("MCX") && sikkimAddress(c, u) && x.SikkimMailedOn == "" {
		add("MCX-N0014", false, "a Sikkim address: e-mail the client's identity, address proof and KYC to MCX surveillance (%s), or MCX marks the code inactive", mcxSikkimMail)
	}
	if other, ex := e.secondUCC(c); other != "" && (x.SecondUCC == nil || x.SecondUCC.OtherUCC != other) {
		add("NSE2-N0028", true, "the PAN also has the active code %s on %s: record why a second code is allowed, approved by compliance, or close one", other, ex)
	}
	return out
}

// onboardBlocks are the issues that stop activation, in words.
func (o *Ops) onboardBlocks(ctx context.Context, tenant string, c Client) []string {
	e := o.onboardEnv(ctx, tenant)
	var out []string
	for _, i := range e.onboardIssues(c, o.UCCDetailsFor(ctx, tenant, c.UCC), o.CDSLBODetailFor(ctx, tenant, c.UCC)) {
		if i.Block {
			out = append(out, i.What)
		}
	}
	return out
}

// OnboardRow is a client on the Onboarding checks tab.
type OnboardRow struct {
	UCC    string            `json:"ucc"`
	Name   string            `json:"name"`
	Type   string            `json:"type"`
	Status string            `json:"status"`
	Issues []OnboardIssue    `json:"issues"`
	Record *ClientOnboarding `json:"record,omitempty"`
}

// OnboardView is the tab.
type OnboardView struct {
	Rows        []OnboardRow      `json:"rows"`
	Controls    UCCControls       `json:"controls"`
	MemberCodes map[string]string `json:"member_codes"`
	Purposes    []string          `json:"purposes"`
	Disability  map[string]string `json:"disability"`
	Liveness    []string          `json:"liveness"`
	Exchanges   []string          `json:"exchanges"`
	CanEdit     bool              `json:"can_edit"`
	CanControl  bool              `json:"can_control"`
}

// OnboardChecks builds the tab: every open client with an issue, or with
// a record (so it can be edited); one client when ucc is given.
func (o *Ops) OnboardChecks(ctx context.Context, tenant, actor, ucc string) (OnboardView, error) {
	v := OnboardView{Rows: []OnboardRow{}, Purposes: OnboardPurposes, Disability: Disabilities, Liveness: LivenessMethods, Exchanges: OptOutExchanges}
	if err := o.require(ctx, tenant, actor, "Viewing onboarding checks", RoleCompliance, RoleChecker, RoleMaker, RoleRisk); err != nil {
		return v, err
	}
	v.CanEdit = o.require(ctx, tenant, actor, "", RoleMaker, RoleChecker, RoleCompliance) == nil
	v.CanControl = o.require(ctx, tenant, actor, "", RoleCompliance) == nil
	e := o.onboardEnv(ctx, tenant)
	v.Controls, v.MemberCodes = e.ctl, e.set.MemberCodes
	ucc = strings.ToUpper(strings.TrimSpace(ucc))
	for _, u := range sortedKeys(e.clients) {
		c := e.clients[u]
		if ucc != "" && u != ucc || c.Status == "Closed" || c.Status == "Rejected" {
			continue
		}
		is := e.onboardIssues(c, o.UCCDetailsFor(ctx, tenant, u), o.CDSLBODetailFor(ctx, tenant, u))
		x, has := e.x[u]
		// The purpose alone, on a client opened before Ledgerline asked for
		// it, is shown only when looked up.
		if ucc == "" && len(is) == 1 && is[0].ID == "SEBI-KYCAML-0010" && !has {
			continue
		}
		if len(is) == 0 && !has && ucc == "" {
			continue
		}
		r := OnboardRow{UCC: u, Name: c.Name, Type: c.Type, Status: c.Status, Issues: is}
		if is == nil {
			r.Issues = []OnboardIssue{}
		}
		if has {
			r.Record = &x
		}
		v.Rows = append(v.Rows, r)
	}
	sort.SliceStable(v.Rows, func(i, j int) bool { return len(v.Rows[i].Issues) > len(v.Rows[j].Issues) })
	return v, nil
}

// onboardDaily raises a work item for each issue an open client has that
// a person must act on: a code that collides, a second code not approved,
// the MCX e-mail for a Sikkim address. Run by the KYC gate's daily tick.
func (o *Ops) onboardDaily(ctx context.Context, tenant, day string) {
	e := o.onboardEnv(ctx, tenant)
	for _, u := range sortedKeys(e.clients) {
		c := e.clients[u]
		if c.Status != "Active" && c.Status != "UCC upload" {
			continue
		}
		for _, i := range e.onboardIssues(c, o.UCCDetailsFor(ctx, tenant, u), CDSLBODetail{}) {
			switch i.ID {
			case "NCL-BSE-0055":
				o.raiseWork(ctx, tenant, wOnboard, "collide:"+u, "Client code "+u+" collides with the firm's own code", i.What+". NSE Clearing's circular 201/2025 asks for the colliding code to be deactivated and a fresh one registered.", u, RoleCompliance, "kycgate", day)
			case "NSE2-N0028":
				o.raiseWork(ctx, tenant, wOnboard, "second:"+u, "Second active code for the PAN of "+u, i.What+". Record the approval on KYC trading gate → Onboarding checks.", u, RoleCompliance, "kycgate", day)
			case "MCX-N0014":
				o.raiseWork(ctx, tenant, wOnboard, "sikkim:"+u, "E-mail MCX about "+u+" (Sikkim address)", "MCX/S&I/251/2026 para 1.2: for a client with a Sikkim correspondence or permanent address, e-mail the identity proof, address proof and KYC to "+mcxSikkimMail+"; then record the date on KYC trading gate → Onboarding checks.", u, RoleCompliance, "kycgate", day)
			}
		}
	}
	// A proprietary code that is also a client's (the controls were set
	// after the client was opened) is caught above; no ERROR account set is
	// the self-inspection's.
}

// ---- self-inspection ----

func init() {
	siTests = append(siTests,
		SITest{ID: "ucc_code_collision", Title: "No client code equals the firm's trading member code or a proprietary code", Area: "Onboarding", Severity: "high", Owner: RoleCompliance,
			Source: "NSE Clearing NCL/CMPT/68965 (circular 201/2025), points 2-6", Checks: "Every open client's code against the firm's trading member codes on each exchange, OWN, codes beginning ERROR, the ERROR account and the proprietary codes set on Onboarding checks.",
			Fix: "Deactivate the colliding code at the exchanges and register a new one for the client.", Screen: "kycgate", ScreenName: "Compliance → KYC trading gate → Onboarding checks", Real: true, run: siUCCCollision},
		SITest{ID: "error_account", Title: "One designated ERROR account is set", Area: "Trading", Severity: "medium", Owner: RoleCompliance,
			Source: "NSE/SURV/74008 (circular 313/2026)", ToConfirm: "Penalty of Rs 10,000 a month without an ERROR account, as the gap report reads the circular.",
			Checks: "The firm's designated ERROR account is recorded on Onboarding checks, and differs from every client code.",
			Fix:    "Record the ERROR account registered with the exchange.", Screen: "kycgate", ScreenName: "Compliance → KYC trading gate → Onboarding checks", Real: true, run: siErrorAccount},
		SITest{ID: "second_ucc", Title: "No PAN has two active codes on the same exchange without approval", Area: "Onboarding", Severity: "medium", Owner: RoleCompliance,
			Source: "NSE inspection master circular NSE/INSP/73976, Annexure 1.1 item 96", Checks: "Open clients sharing a PAN with another active or pending code on the same exchange: the approval of the second code is recorded.",
			Fix: "Record why a second code is allowed (approved by compliance) or close one of the codes.", Screen: "kycgate", ScreenName: "Compliance → KYC trading gate → Onboarding checks", Real: true, run: siSecondUCC},
		SITest{ID: "onboarding_details", Title: "New clients' onboarding details are complete", Area: "Onboarding", Severity: "medium", Owner: RoleCompliance,
			Source: "SEBI KYC master circular 2023/169 para 12 and VIPV conditions; NSE/INSP/73976 FAQ (persons with disability); CDSL DP2026-218, DP2026-461, 2026/470; MCX/INSP/255/2026; MCX/S&I/251/2026",
			Checks: "Clients opened in the last 30 days: the purpose of the relationship, a complete VIPV record where the IPV was by video, the guardian and liveness check of a person with disability, an HUF's Karta and coparceners, an NPO's DARPAN ID, a corporate's board resolution for MCX, and the MCX e-mail for a Sikkim address.",
			Fix:    "Record what is missing on Onboarding checks.", Screen: "kycgate", ScreenName: "Compliance → KYC trading gate → Onboarding checks", Real: true, run: siOnboardDetails},
		SITest{ID: "exchange_opt_out", Title: "No UCC goes to an exchange the client opted out of", Area: "Onboarding", Severity: "low", Owner: RoleCompliance,
			Source: "SEBI stock broker master circular SEBI/HO/MIRSD/MIRSD-PoD/P/CIR/2025/90, para 21.8", Checks: "Clients with a written opt-out from an exchange: no registration at that exchange made or sent after the opt-out.",
			Fix: "Close the client's code at that exchange, or remove the opt-out if the client withdrew it in writing.", Screen: "kycgate", ScreenName: "Compliance → KYC trading gate → Onboarding checks", Real: true, run: siExchangeOptOut},
	)
}

func siUCCCollision(e *siEnv) (siOut, error) {
	var s siOut
	s.input("Clients", e.date)
	ctl := e.o.UCCControlsNow(e.ctx, e.tenant)
	for _, u := range sortedKeys(e.clients) {
		c := e.clients[u]
		if c.Status == "Closed" || c.Status == "Rejected" || c.DPOnly {
			continue
		}
		s.checked++
		if why := uccCollision(e.set, ctl, u); why != "" {
			s.bad(fmt.Sprintf("%s: %s", siClient(u), why))
		}
	}
	s.summary = fmt.Sprintf("%d client codes checked; %d collide with the firm's own codes.", s.checked, s.found)
	return s, nil
}

func siErrorAccount(e *siEnv) (siOut, error) {
	var s siOut
	s.input("The firm's proprietary codes", e.date)
	ctl := e.o.UCCControlsNow(e.ctx, e.tenant)
	s.checked = 1
	switch _, isClient := e.clients[ctl.ErrorAccount]; {
	case ctl.ErrorAccount == "":
		s.bad("No ERROR account is designated")
	case isClient:
		s.bad("The ERROR account " + ctl.ErrorAccount + " is also a client's code")
	}
	s.summary = map[bool]string{true: "The ERROR account is " + ctl.ErrorAccount + ".", false: "The ERROR account is not set right."}[s.found == 0]
	return s, nil
}

func siSecondUCC(e *siEnv) (siOut, error) {
	var s siOut
	s.input("Clients", e.date)
	env := e.o.onboardEnv(e.ctx, e.tenant)
	for _, u := range sortedKeys(env.clients) {
		c := env.clients[u]
		if c.Status != "Active" && c.Status != "UCC upload" || len(env.byPAN[c.PAN]) < 2 {
			continue
		}
		s.checked++
		if other, ex := env.secondUCC(c); other != "" {
			if x := env.x[u]; x.SecondUCC == nil || x.SecondUCC.OtherUCC != other {
				if x2 := env.x[other]; x2.SecondUCC == nil || x2.SecondUCC.OtherUCC != u {
					s.bad(fmt.Sprintf("%s and %s share a PAN on %s with no approval", siClient(u), siClient(other), ex))
				}
			}
		}
	}
	s.summary = fmt.Sprintf("%d clients sharing a PAN checked; %d without an approved second code.", s.checked, s.found)
	return s, nil
}

func siOnboardDetails(e *siEnv) (siOut, error) {
	var s siOut
	s.input("Clients opened", e.date)
	env := e.o.onboardEnv(e.ctx, e.tenant)
	from := addDays(e.date, -siWindow)
	for _, u := range sortedKeys(env.clients) {
		c := env.clients[u]
		if c.Status != "Active" && c.Status != "UCC upload" || c.KYCDate < from || c.KYCDate > e.date {
			continue
		}
		s.checked++
		var what []string
		for _, i := range env.onboardIssues(c, e.o.UCCDetailsFor(e.ctx, e.tenant, u), e.o.CDSLBODetailFor(e.ctx, e.tenant, u)) {
			if i.ID != "NCL-BSE-0055" && i.ID != "NSE2-N0028" {
				what = append(what, i.What)
			}
		}
		if len(what) > 0 {
			s.bad(siClient(u) + ": " + strings.Join(what, "; "))
		}
	}
	s.summary = fmt.Sprintf("%d clients opened in the last %d days checked; %d with details missing.", s.checked, siWindow, s.found)
	return s, nil
}

func siExchangeOptOut(e *siEnv) (siOut, error) {
	var s siOut
	s.input("UCC registrations", e.date)
	for u, x := range e.o.onboardings(e.ctx, e.tenant) {
		for _, oo := range x.OptOuts {
			s.checked++
			if at := e.o.uccRegSentAt(e.ctx, e.tenant, oo.Exchange, u); at != "" && at[:10] >= oo.On {
				s.bad(fmt.Sprintf("%s opted out of %s on %s, but a UCC record went there on %s", siClient(u), oo.Exchange, fmtDay(oo.On), fmtDay(at[:10])))
			}
		}
	}
	s.summary = fmt.Sprintf("%d opt-outs checked; %d with a UCC sent after them.", s.checked, s.found)
	return s, nil
}

// uccRegSentAt is when a UCC record of the client last went to the
// exchange ("" when never, as far as Ledgerline knows).
func (o *Ops) uccRegSentAt(ctx context.Context, tenant, ex, ucc string) string {
	if ex == "NSE" {
		r, _, err := docs.Get[NSEUCIReg](ctx, o.Docs, tenant, cUCCReg, "NSE|"+ucc)
		if err != nil || len(r.SentAt) < 10 {
			return ""
		}
		return r.SentAt
	}
	r, _, err := docs.Get[BSEUCCReg](ctx, o.Docs, tenant, cBSEUCCReg, ex+"|"+ucc)
	if err != nil || len(r.SentAt) < 10 {
		return ""
	}
	return r.SentAt
}
