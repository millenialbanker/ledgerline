package ops

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
)

// ---- CDSL APIs: the demat account (BO) lifecycle ----
//
// Every call the DP makes to CDSL about a demat account is a "CDSL
// request", kept with what was sent, what CDSL answered and CDSL's
// reference, and shown on the DP's "CDSL requests" tab:
//
//   - Open account (CDSL API Handbook §5.4, BO online): a new client's
//     account is opened from the client master (and the online application
//     when there is one). Clients a checker activated with no demat
//     account get one automatically while the BO API is on; staff can
//     raise one too, which a checker approves. CDSL calls back with the BO
//     ID, which goes on the client and the DP's BO record.
//   - Modify account (§5.6): an approved change of email, mobile, address,
//     bank or nomination on a CDSL account is captured with the fields it
//     changes. CDSL's API takes a BO modify file in the CDAS upload layout;
//     staff attach it, a checker approves and it is uploaded, and the
//     upload's status and response file are fetched until CDSL is done.
//   - BO setup upload (§5.5): a BO setup file (several accounts) uploaded
//     the same way.
//   - Transfer cum closure (§5.1) and add beneficiary (§5.2): see
//     cdslredirect.go. Active demat count (§5.3): see cdslcount.go.
//
// With an API family switched off in Connections, the request says "Do in
// CDAS": staff do it in CDSL's own system and record the outcome here.

const (
	cCDSLReq   = "cdslreq"
	cCDSLIndex = "cdslindex" // token hashes and sources → request ID
	cCDSLState = "cdslstate"

	CDSLOpen        = "Open account"
	CDSLSetupUpload = "BO setup upload"
	CDSLModify      = "Modify account"
	CDSLTransfer    = "Transfer cum closure"
	CDSLBeneficiary = "Add beneficiary"

	crNeedsDetails = "Needs details"
	crNeedsFile    = "Needs file"
	crChecker      = "Waiting for checker"
	crApproved     = "Approved"
	crSent         = "Sent to CDSL"
	crClient       = "Waiting for client"
	crConfirmed    = "Confirmed by client"
	crCompleted    = "Completed"
	crRejected     = "Rejected by CDSL"
	crFailed       = "Failed"
	crCancelled    = "Cancelled"
	crManual       = "Do in CDAS"
	crManualDone   = "Done in CDAS"
)

// CDSLKinds are the kinds of request, with the handbook section and the
// API family switch each uses.
var CDSLKinds = []struct{ Kind, Section, Family string }{
	{CDSLOpen, "§5.4", cdslFamBO},
	{CDSLModify, "§5.6", cdslFamBO},
	{CDSLSetupUpload, "§5.5", cdslFamBO},
	{CDSLTransfer, "§5.1", cdslFamTransfer},
	{CDSLBeneficiary, "§5.2", cdslFamBeneficiary},
}

func cdslKindInfo(kind string) (section, family string) {
	for _, k := range CDSLKinds {
		if k.Kind == kind {
			return k.Section, k.Family
		}
	}
	return "", ""
}

// CDSLRequest is one request to CDSL about a demat account.
type CDSLRequest struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Section string `json:"section"`
	UCC     string `json:"ucc,omitempty"`
	Name    string `json:"name,omitempty"`
	BOID    string `json:"bo_id,omitempty"`
	// Source is what the request came from: a change request, a DP
	// request, an online application, or staff.
	Source   string   `json:"source,omitempty"`
	Status   string   `json:"status"`
	Problems []string `json:"problems,omitempty"` // what must be fixed before it can go
	Warnings []string `json:"warnings,omitempty"` // worth knowing; does not stop it
	// Fields are details staff gave for this request (address lines, PIN,
	// codes); they take precedence over the client master.
	Fields  map[string]string `json:"fields,omitempty"`
	Changes []CDSLChange      `json:"changes,omitempty"` // modify: what changes, by handbook field
	Buyers  []CDSLBuyer       `json:"buyers,omitempty"`  // add beneficiary
	File    *CDSLFileRef      `json:"file,omitempty"`    // upload: the file sent
	Reply   *CDSLFileRef      `json:"reply_file,omitempty"`
	// Ref is the reference this side gave CDSL (URN, request ID, upload
	// file name); Refs every one used, as retries need new ones.
	Ref        string   `json:"ref,omitempty"`
	Refs       []string `json:"refs,omitempty"`
	CDSLRef    string   `json:"cdsl_ref,omitempty"` // CRN, acknowledgement, transaction ID
	CDSLStatus string   `json:"cdsl_status,omitempty"`
	Request    string   `json:"request,omitempty"`  // the last request as sent (no keys or passwords)
	Response   string   `json:"response,omitempty"` // CDSL's last answer
	Error      string   `json:"error,omitempty"`
	Tries      int      `json:"tries"`
	Hash       string   `json:"approved_hash,omitempty"` // what the checker approved
	TokenHash  string   `json:"token_hash,omitempty"`    // the link or callback token's hash; never shown
	LinkUntil  string   `json:"link_until,omitempty"`
	Maker      string   `json:"maker"`
	Checker    string   `json:"checker,omitempty"`
	At         string   `json:"at"`
	Updated    string   `json:"updated"`
	SentAt     string   `json:"sent_at,omitempty"`
	DoneAt     string   `json:"done_at,omitempty"`
	// PendingActivation: CDSL opened the account without a signature, so it
	// stays "pending for activation" until one is uploaded in CDAS.
	PendingActivation bool        `json:"pending_activation,omitempty"`
	History           []CDSLEvent `json:"history"`
}

// CDSLChange is one field a modification changes.
type CDSLChange struct {
	Section string `json:"section"` // the BOSetupDetails block, e.g. FirstHolderDetails
	Field   string `json:"field"`
	Label   string `json:"label"`
	Old     string `json:"old,omitempty"`
	New     string `json:"new"`
}

// CDSLFileRef is a file kept with a request.
type CDSLFileRef struct {
	Name   string `json:"name"`
	FileID string `json:"file_id"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// CDSLEvent is one step in a request's life.
type CDSLEvent struct {
	At   string `json:"at"`
	By   string `json:"by"`
	What string `json:"what"`
}

type cdslIndex struct {
	ID  string `json:"id"`
	Use string `json:"use"`
}

func (r CDSLRequest) public() CDSLRequest {
	r.TokenHash = ""
	return r
}

func (r *CDSLRequest) event(at, by, what string) {
	r.History = append(r.History, CDSLEvent{At: at, By: by, What: what})
	if len(r.History) > 60 {
		r.History = r.History[len(r.History)-60:]
	}
}

func (r CDSLRequest) open() bool {
	switch r.Status {
	case crCompleted, crCancelled, crManualDone:
		return false
	}
	return true
}

func cdslHashToken(tok string) string {
	s := sha256.Sum256([]byte("cdsl|" + tok))
	return hex.EncodeToString(s[:])
}

// ---- storage ----

func (o *Ops) cdslNewRequest(ctx context.Context, tenant, actor string, r CDSLRequest) (CDSLRequest, error) {
	n, err := o.next(ctx, tenant, "cdslreq", actor)
	if err != nil {
		return r, err
	}
	now := o.now().Format(time.RFC3339)
	r.ID, r.At, r.Updated = fmt.Sprintf("CR-%05d", n), now, now
	r.Section, _ = cdslKindInfo(r.Kind)
	if r.Maker == "" {
		r.Maker = actor
	}
	r.event(now, actor, "Raised"+map[bool]string{true: " from " + r.Source, false: ""}[r.Source != ""])
	if _, err := docs.Put(ctx, o.Docs, tenant, cCDSLReq, r.ID, r, 0, actor); err != nil {
		return r, err
	}
	if r.Source != "" {
		_ = docs.Upsert(ctx, o.Docs, tenant, cCDSLIndex, "src|"+r.Source, cdslIndex{ID: r.ID, Use: "source"}, actor)
	}
	o.log(ctx, tenant, actor, "cdsl.request", r.ID, map[string]any{"kind": r.Kind, "ucc": r.UCC, "source": r.Source, "status": r.Status})
	return r, nil
}

func (o *Ops) cdslSave(ctx context.Context, tenant, actor string, r *CDSLRequest) error {
	r.Updated = o.now().Format(time.RFC3339)
	return docs.Upsert(ctx, o.Docs, tenant, cCDSLReq, r.ID, *r, actor)
}

func (o *Ops) cdslGet(ctx context.Context, tenant, id string) (CDSLRequest, error) {
	r, _, err := docs.Get[CDSLRequest](ctx, o.Docs, tenant, cCDSLReq, strings.ToUpper(strings.TrimSpace(id)))
	if err != nil {
		return r, fmt.Errorf("%w: CDSL request %s", ErrNotFound, id)
	}
	return r, nil
}

// cdslBySource finds the request raised from a change, DP request or
// application.
func (o *Ops) cdslBySource(ctx context.Context, tenant, src string) (CDSLRequest, bool) {
	ix, _, err := docs.Get[cdslIndex](ctx, o.Docs, tenant, cCDSLIndex, "src|"+src)
	if err != nil {
		return CDSLRequest{}, false
	}
	r, err := o.cdslGet(ctx, tenant, ix.ID)
	return r, err == nil
}

// cdslByToken finds the request a link or callback token belongs to.
func (o *Ops) cdslByToken(ctx context.Context, tenant, tok string) (CDSLRequest, error) {
	if len(tok) < 20 {
		return CDSLRequest{}, ErrNotFound
	}
	h := cdslHashToken(tok)
	ix, _, err := docs.Get[cdslIndex](ctx, o.Docs, tenant, cCDSLIndex, "tok|"+h)
	if err != nil {
		return CDSLRequest{}, ErrNotFound
	}
	r, err := o.cdslGet(ctx, tenant, ix.ID)
	if err != nil || r.TokenHash != h {
		return CDSLRequest{}, ErrNotFound
	}
	return r, nil
}

// cdslNewToken gives a request a fresh token (for its callback address or
// the client's link) and indexes it.
func (o *Ops) cdslNewToken(ctx context.Context, tenant, actor string, r *CDSLRequest, use string) (string, error) {
	tok, err := newToken()
	if err != nil {
		return "", err
	}
	// An earlier token stops working: its index entry no longer matches.
	r.TokenHash = cdslHashToken(tok)
	return tok, docs.Upsert(ctx, o.Docs, tenant, cCDSLIndex, "tok|"+r.TokenHash, cdslIndex{ID: r.ID, Use: use}, actor)
}

// cdslReqID is a request ID that is unique for the DP for ever: the time
// and a sequence, all digits (n digits long, at least 16).
func (o *Ops) cdslReqID(ctx context.Context, tenant, actor string, n int) (string, error) {
	seq, err := o.next(ctx, tenant, "cdslreqid", actor)
	if err != nil {
		return "", err
	}
	ts := o.now().Format("20060102150405")
	if n < 24 {
		ts = o.now().Format("060102")
	}
	w := n - len(ts)
	return ts + fmt.Sprintf("%0*d", w, seq%pow10(w)), nil
}

func pow10(n int) int64 {
	p := int64(1)
	for i := 0; i < n && i < 18; i++ {
		p *= 10
	}
	return p
}

// ---- the screen ----

// CDSLScreen is the "CDSL requests" tab.
type CDSLScreen struct {
	Requests []CDSLRequest    `json:"requests"`
	Counts   map[string]int   `json:"counts"`
	Families []CDSLFamilyView `json:"families"`
	Set      bool             `json:"connection_set"`
	Env      string           `json:"env,omitempty"`
	Defaults CDSLBODefaults   `json:"bo_defaults"`
	Kinds    []string         `json:"kinds"`
	CanMake  bool             `json:"can_make"`
	CanCheck bool             `json:"can_check"`
}

// CDSLFamilyView is an API family and whether it is on.
type CDSLFamilyView struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	On    bool   `json:"on"`
}

// CDSLRequests lists the requests, newest first.
func (o *Ops) CDSLRequests(ctx context.Context, tenant, actor string) (CDSLScreen, error) {
	if err := o.require(ctx, tenant, actor, "Viewing CDSL requests", RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return CDSLScreen{}, err
	}
	rs, err := docs.List[CDSLRequest](ctx, o.Docs, tenant, cCDSLReq, "")
	if err != nil {
		return CDSLScreen{}, err
	}
	sort.Slice(rs, func(i, j int) bool { return rs[i].ID > rs[j].ID })
	out := CDSLScreen{Requests: make([]CDSLRequest, 0, len(rs)), Counts: map[string]int{}}
	for _, r := range rs {
		out.Requests = append(out.Requests, r.public())
		out.Counts[r.Status]++
		if r.open() {
			out.Counts["open"]++
		}
	}
	x, ok := o.cdslConnFor(ctx, tenant)
	out.Set, out.Env = ok, x.Env
	for _, k := range []string{cdslFamBO, cdslFamBOCount, cdslFamTransfer, cdslFamBeneficiary} {
		for _, f := range cdslFamilies {
			if f.Key == k {
				out.Families = append(out.Families, CDSLFamilyView{Key: k, Label: f.Label, On: ok && x.On(k)})
			}
		}
	}
	out.Defaults = o.CDSLBODefaultsNow(ctx, tenant)
	for _, k := range CDSLKinds {
		out.Kinds = append(out.Kinds, k.Kind)
	}
	role, _ := o.RoleOf(ctx, tenant, actor)
	out.CanMake = role == RoleMaker || role == RoleChecker || role == RoleAdmin
	out.CanCheck = role == RoleChecker || role == RoleAdmin
	return out, nil
}

// CDSLRequestGet is one request.
func (o *Ops) CDSLRequestGet(ctx context.Context, tenant, actor, id string) (CDSLRequest, error) {
	if err := o.require(ctx, tenant, actor, "Viewing CDSL requests", RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return CDSLRequest{}, err
	}
	r, err := o.cdslGet(ctx, tenant, id)
	return r.public(), err
}

// ---- BO defaults: CDSL's codes the DP uses for new accounts ----

// CDSLBODefaults are the values the DP uses for every new account where
// the client master has nothing to give: CDSL's product number and code
// values (BO category, sub-status, statement cycle…), keyed by the
// handbook's field names (§5.4 data mapping).
type CDSLBODefaults struct {
	Values map[string]string `json:"values"`
	By     string            `json:"by,omitempty"`
	At     string            `json:"at,omitempty"`
}

// cdslDefaultFields are the defaults the screen offers, with what each is.
var cdslDefaultFields = []struct{ Key, Label, Default string }{
	{"product_number", "Product number (CDSL product set up for the DP's individual accounts)", ""},
	{"bo_category", "BO category code", ""},
	{"bo_sub_status", "BO sub-status code (resident individual)", ""},
	{"mode_of_operation", "Mode of operation code (single holder)", ""},
	{"communication_preference", "Communication preference code", ""},
	{"bo_settlement_planning_flag", "BO settlement planning flag", ""},
	{"bo_statement_cycle_code", "BO statement cycle code", ""},
	{"divnd_bank_acct_type", "Dividend bank account type code (savings)", ""},
	{"divnd_bank_ccy", "Dividend bank currency code", "INR"},
	{"nationality_code", "Nationality code (Indian)", ""},
	{"cust_addr_cntry_code", "Country code of addresses (ISO 3166-1 alpha-2)", "IN"},
	{"primary_mobil_isd_code", "ISD code of mobile numbers", "91"},
	{"bonafide_flag", "Bonafide flag (P for a CDAS BO upload)", "P"},
	{"email_statement_flag", "Statements by email (Y/N)", "Y"},
}

// CDSLBODefaultsNow returns the DP's defaults.
func (o *Ops) CDSLBODefaultsNow(ctx context.Context, tenant string) CDSLBODefaults {
	d, _, _ := docs.GetOr(ctx, o.Docs, tenant, cCDSLState, "bodefaults", CDSLBODefaults{})
	if d.Values == nil {
		d.Values = map[string]string{}
	}
	for _, f := range cdslDefaultFields {
		if _, ok := d.Values[f.Key]; !ok && f.Default != "" {
			d.Values[f.Key] = f.Default
		}
	}
	return d
}

// CDSLDefaultFields lists the defaults with their labels, for the screen.
func CDSLDefaultFields() []map[string]string {
	var out []map[string]string
	for _, f := range cdslDefaultFields {
		out = append(out, map[string]string{"key": f.Key, "label": f.Label})
	}
	return out
}

// SaveCDSLBODefaults saves the DP's defaults (checker or admin).
func (o *Ops) SaveCDSLBODefaults(ctx context.Context, tenant, actor string, vals map[string]string) (CDSLBODefaults, error) {
	if err := o.require(ctx, tenant, actor, "Setting CDSL account defaults", RoleChecker); err != nil {
		return CDSLBODefaults{}, err
	}
	d := CDSLBODefaults{Values: map[string]string{}, By: actor, At: o.now().Format(time.RFC3339)}
	var probs []string
	for _, f := range cdslDefaultFields {
		v := strings.TrimSpace(vals[f.Key])
		if v == "" {
			continue
		}
		if fd, ok := boFieldSpec("ProductDetails", f.Key); ok && utf8.RuneCountInString(v) > fd.Max {
			probs = append(probs, fmt.Sprintf("%s is at most %d characters", f.Label, fd.Max))
		} else if fd, ok := boFieldSpec("FirstHolderDetails", f.Key); ok && utf8.RuneCountInString(v) > fd.Max {
			probs = append(probs, fmt.Sprintf("%s is at most %d characters", f.Label, fd.Max))
		}
		d.Values[f.Key] = v
	}
	if len(probs) > 0 {
		return d, invalid("%s", strings.Join(probs, "; "))
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cCDSLState, "bodefaults", d, actor); err != nil {
		return d, err
	}
	o.log(ctx, tenant, actor, "cdsl.bo_defaults", "bodefaults", map[string]any{"fields": len(d.Values)})
	return d, nil
}

// ---- §5.4 BO online: the field mapping ----

// boField is one field of BOSetupDetails as the handbook's data mapping
// gives it: its JSON name, its length and whether it is mandatory.
type boField struct {
	K   string
	Max int
	M   bool
}

// boSections are the blocks of BOSetupDetails in the handbook's order,
// each with its fields in order (CDSL API Handbook §5.4, data mapping
// rows 1–296; JSON names as in its sample, without the stray spaces).
var boSections = []struct {
	Name   string
	List   bool
	Fields []boField
}{
	{"FirstHolderDetails", false, []boField{{"product_number", 4, true}, {"bo_name", 100, true}, {"bo_middle_name", 20, false}, {"lastsearch_name", 20, true}, {"bo_title", 10, false}, {"bo_suffix", 10, false},
		{"fatherhusband_name", 50, false}, {"address1", 55, true}, {"address2", 55, false}, {"address3", 55, false}, {"cust_addr_cntry_code", 2, true}, {"cust_addr_zip", 10, true}, {"cust_addr_state", 25, false},
		{"cust_addr_city", 25, true}, {"smart_reg_ind", 1, false}, {"primary_mobil_isd_code", 6, false}, {"primary_mobil_no", 17, false}, {"secondary_mobil_isd_code", 6, false}, {"secondary_mobile_telephone_no", 17, false},
		{"secondary_email", 100, false}, {"cust_fax", 17, false}, {"income_tax_pan", 10, true}, {"uid", 16, false}, {"uid_verification_flag", 1, false}, {"poa_type_flag", 1, false}, {"it_circle", 15, false},
		{"primary_email", 100, false}, {"user_text_1", 50, false}, {"user_text_2", 50, false}, {"user_field_3", 4, false}, {"pan_exemption_code", 4, false}, {"pan_verif_flag", 4, false}}},
	{"SecondHolderDetails", false, nil},
	{"ThirdHolderDetails", false, nil},
	{"ProductDetails", false, []boField{{"date_of_maturity", 8, false}, {"dp_internal_ref_number", 10, false}, {"date_of_birth_origin", 8, false}, {"sex_code", 1, false}, {"occupation", 4, false}, {"life_style", 4, false},
		{"geographical_code", 4, false}, {"education_degree", 4, false}, {"annual_income_code", 4, false}, {"nationality_code", 3, false}, {"legal_status_code", 2, false}, {"bo_fee_type", 2, false},
		{"language_code", 2, false}, {"category_4_code", 2, false}, {"bank_option_5", 2, false}, {"staff_relative", 1, false}, {"staff_code", 10, false}, {"alphanumeric_code", 38, false},
		{"bonafide_flag", 1, false}, {"family_account_flag", 1, false}, {"email_statement_flag", 1, false}, {"cas_mode", 2, false}, {"mental_disability", 1, false}, {"rgess_flag", 1, false},
		{"annual_report_flag", 1, false}, {"pledge_standing_instruction_flag", 1, false}, {"email_rta_download_flag", 1, false}, {"bsda_flag", 1, false}, {"byte_alphanum_code", 50, false},
		{"mode_of_operation", 4, true}, {"communication_preference", 4, true}, {"security_access_code", 2, false}, {"bo_category", 2, true}, {"bo_settlement_planning_flag", 1, true},
		{"dividend_bank_ifsc_code", 15, false}, {"rbi_reference_number", 30, false}, {"rbi_approval_date", 8, false}, {"sebi_reg_no", 24, false}, {"beneficiary_tax_deduction_status", 2, false},
		{"smart_card_required", 1, false}, {"smart_card_number", 20, false}, {"smart_card_pin", 10, false}, {"ecs_mandate", 1, false}, {"electronic_confirmation", 1, false}, {"dividend_currency", 6, false},
		{"group_code", 8, false}, {"bo_sub_status", 4, true}, {"clearing_corporation_id", 4, false}, {"clearing_member_id", 8, false}, {"stock_exchange", 2, false}, {"confirmation_waived", 1, false},
		{"trading_id", 8, false}, {"bo_statement_cycle_code", 2, true}, {"custodian_pms_email_id", 50, false}, {"divnd_bank_acct_type", 12, true}, {"divnd_bank_code", 12, true}, {"divnd_acct_numb", 20, true},
		{"divnd_bank_ccy", 6, true}, {"legal_entity_identifier", 20, false}, {"lei_expiry_date", 8, false}, {"print_nominee_details_flag", 1, false}, {"mod_bsda_reason", 1, false},
		{"client_consent_date", 8, false}, {"mode_of_consent_received", 1, false}}},
	{"POALinkDetails", true, nil},
	{"PermanentADDdetails", false, []boField{{"address1", 55, true}, {"address2", 55, false}, {"address3", 55, false}, {"cust_addr_cntry_code", 2, true}, {"cust_addr_zip", 10, true}, {"cust_addr_state", 25, false}, {"city", 25, true}}},
	{"NomineeDetails", true, []boField{{"bo_name", 100, true}, {"bo_middle_name", 20, false}, {"last_search_name", 20, true}, {"bo_title", 10, false}, {"bo_suffix", 10, false}, {"father_husband_name", 50, false},
		{"address1", 55, true}, {"address2", 55, false}, {"address3", 55, false}, {"cust_addr_cntry_code", 2, true}, {"cust_addr_zip", 10, true}, {"cust_addr_state", 25, false}, {"city", 25, true},
		{"dob", 8, false}, {"fax", 17, false}, {"income_tax_pan", 10, false}, {"uid", 16, false}, {"uid_verification_flag", 1, false}, {"it_circle", 15, false}, {"primary_email", 100, false},
		{"user_text_1", 50, false}, {"user_text_2", 50, false}, {"user_field_3", 4, false}, {"user_field_4", 4, false}, {"user_field_5", 4, false}, {"nominee_serial_number", 2, true},
		{"relationship_with_bo", 2, true}, {"percentage_of_shares", 5, true}, {"residual_securities_flag", 1, true}, {"primary_isdcode", 6, false}, {"primary_mobil_no", 17, false},
		{"minor_flag", 4, false}, {"driving_license", 20, false}, {"passport", 20, false}, {"cust_other_relationship", 50, false}}},
	{"NomineeGuardianDetails", true, []boField{{"bo_name", 100, true}, {"bo_middle_name", 20, false}, {"last_search_name", 20, true}, {"bo_title", 10, false}, {"bo_suffix", 10, false},
		{"father_husband_name", 50, false}, {"address1", 55, true}, {"address2", 55, false}, {"address3", 55, false}, {"cust_addr_cntry_code", 2, true}, {"cust_addr_zip", 10, true},
		{"cust_addr_state", 25, false}, {"city", 25, true}, {"dob", 8, false}, {"fax", 17, false}, {"income_tax_pan", 10, false}, {"uid", 16, false}, {"uid_verification_flag", 1, false},
		{"it_circle", 15, false}, {"primary_email", 100, false}, {"user_text_1", 50, false}, {"user_text_2", 50, false}, {"user_field_3", 4, false}, {"user_field_4", 4, false},
		{"user_field_5", 4, false}, {"nominee_serial_number", 2, true}, {"relationship_with_bo", 2, true}, {"primary_isdcode", 6, false}, {"primary_mobil_no", 17, false},
		{"driving_license", 20, false}, {"passport", 20, false}}},
	{"BOGuardianDetails", false, nil},
	{"AuthorizeSignatoryDetails", true, nil},
	{"CMPOALinkDetails", true, nil},
	{"UCCLink", true, nil},
	{"PMSdetails", true, nil},
	{"AOPdetails", true, nil},
	{"CoparcenersDetails", true, nil},
	{"KartaDetails", true, nil},
}

// boLabels name the fields in the problems staff see.
var boLabels = map[string]string{
	"product_number": "product number", "bo_name": "name", "lastsearch_name": "last (search) name", "last_search_name": "last (search) name", "address1": "address line 1",
	"cust_addr_cntry_code": "country code", "cust_addr_zip": "PIN code", "cust_addr_city": "city", "city": "city", "income_tax_pan": "PAN", "mode_of_operation": "mode of operation code",
	"communication_preference": "communication preference code", "bo_category": "BO category code", "bo_settlement_planning_flag": "BO settlement planning flag",
	"bo_sub_status": "BO sub-status code", "bo_statement_cycle_code": "BO statement cycle code", "divnd_bank_acct_type": "dividend bank account type code",
	"divnd_bank_code": "dividend bank code (the bank's 9-digit MICR)", "divnd_acct_numb": "bank account number", "divnd_bank_ccy": "dividend bank currency code",
	"nominee_serial_number": "serial number", "relationship_with_bo": "relationship", "percentage_of_shares": "share", "residual_securities_flag": "residual securities flag",
	"primary_mobil_no": "mobile number", "primary_email": "email", "dividend_bank_ifsc_code": "bank IFSC",
}

func boFieldSpec(section, key string) (boField, bool) {
	for _, s := range boSections {
		if s.Name == section {
			for _, f := range s.Fields {
				if f.K == key {
					return f, true
				}
			}
		}
	}
	return boField{}, false
}

// boBlock is a block's values.
type boBlock map[string]string

// boDetails is BOSetupDetails: one block per section, lists for the
// repeating ones; sections with nothing are sent as null.
type boDetails struct {
	One  map[string]boBlock
	Many map[string][]boBlock
}

// MarshalJSON writes the blocks and their fields in the handbook's order,
// every field of a filled block present (empty when there is no value).
func (d boDetails) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, s := range boSections {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(s.Name)
		b.Write(k)
		b.WriteByte(':')
		obj := func(v boBlock) {
			b.WriteByte('{')
			for j, f := range s.Fields {
				if j > 0 {
					b.WriteByte(',')
				}
				fk, _ := json.Marshal(f.K)
				fv, _ := json.Marshal(v[f.K])
				b.Write(fk)
				b.WriteByte(':')
				b.Write(fv)
			}
			b.WriteByte('}')
		}
		switch {
		case s.List && len(d.Many[s.Name]) > 0:
			b.WriteByte('[')
			for j, v := range d.Many[s.Name] {
				if j > 0 {
					b.WriteByte(',')
				}
				obj(v)
			}
			b.WriteByte(']')
		case !s.List && d.One[s.Name] != nil:
			obj(d.One[s.Name])
		default:
			b.WriteString("null")
		}
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// check validates every filled block against the data mapping: mandatory
// fields, lengths, and the formats the handbook states.
func (d boDetails) check() []string {
	var out []string
	label := func(k string) string { return firstOf(boLabels[k], strings.ReplaceAll(k, "_", " ")) }
	blk := func(sec, who string, v boBlock) {
		for _, s := range boSections {
			if s.Name != sec {
				continue
			}
			for _, f := range s.Fields {
				val := v[f.K]
				switch {
				case f.M && strings.TrimSpace(val) == "":
					out = append(out, fmt.Sprintf("%s: the %s is missing", who, label(f.K)))
				case utf8.RuneCountInString(val) > f.Max:
					out = append(out, fmt.Sprintf("%s: the %s is longer than CDSL's %d characters (%q)", who, label(f.K), f.Max, val))
				}
			}
		}
		if c := v["cust_addr_cntry_code"]; c != "" && c != "IN" && v["cust_addr_state"] == "" {
			out = append(out, who+": an address outside India needs the state")
		}
	}
	for _, s := range boSections {
		if v := d.One[s.Name]; v != nil {
			blk(s.Name, boWho(s.Name, 0), v)
		}
		for i, v := range d.Many[s.Name] {
			blk(s.Name, boWho(s.Name, i), v)
		}
	}
	if f := d.One["FirstHolderDetails"]; f != nil {
		if f["primary_mobil_no"] != "" && f["primary_mobil_isd_code"] == "" {
			out = append(out, "Holder: a mobile number needs its ISD code")
		}
		if f["primary_mobil_no"] != "" && !regexp.MustCompile(`^[0-9]+$`).MatchString(f["primary_mobil_no"]) {
			out = append(out, "Holder: the mobile number must be digits only")
		}
		if f["income_tax_pan"] != "" && !rePAN.MatchString(f["income_tax_pan"]) {
			out = append(out, "Holder: the PAN is not in the PAN format")
		}
		if p := d.One["ProductDetails"]; p != nil && p["email_statement_flag"] == "Y" && f["primary_email"] == "" {
			out = append(out, "Holder: statements by email need the holder's email")
		}
	}
	total := 0
	for i, n := range d.Many["NomineeDetails"] {
		var x int
		if _, err := fmt.Sscanf(n["percentage_of_shares"], "%d", &x); err != nil || len(n["percentage_of_shares"]) != 5 {
			out = append(out, fmt.Sprintf("%s: the share must be five digits with two decimals implied (10000 for 100%%)", boWho("NomineeDetails", i)))
		}
		total += x
		if n["relationship_with_bo"] == "13" && n["cust_other_relationship"] == "" {
			out = append(out, fmt.Sprintf("%s: say what the relationship is (Others)", boWho("NomineeDetails", i)))
		}
	}
	if len(d.Many["NomineeDetails"]) > 0 && total != 10000 {
		out = append(out, fmt.Sprintf("Nominees: the shares add to %d.%02d%%, not 100%%", total/100, total%100))
	}
	return out
}

func boWho(sec string, i int) string {
	switch sec {
	case "FirstHolderDetails":
		return "Holder"
	case "ProductDetails":
		return "Account"
	case "PermanentADDdetails":
		return "Permanent address"
	case "NomineeDetails":
		return fmt.Sprintf("Nominee %d", i+1)
	case "NomineeGuardianDetails":
		return fmt.Sprintf("Guardian of nominee %d", i+1)
	}
	return sec
}

// boRelation is CDSL's relationship code for the relationship as written.
func boRelation(rel string) string {
	r := strings.ToLower(strings.NewReplacer("-", "", " ", "").Replace(rel))
	switch r {
	case "spouse", "wife", "husband":
		return "01"
	case "son":
		return "02"
	case "daughter":
		return "03"
	case "father":
		return "04"
	case "mother":
		return "05"
	case "brother":
		return "06"
	case "sister":
		return "07"
	case "grandson":
		return "08"
	case "granddaughter":
		return "09"
	case "grandfather":
		return "10"
	case "grandmother":
		return "11"
	case "", "notprovided":
		return "12"
	}
	return "13"
}

// boShare writes a percentage as the handbook does: 50 → 05000.
func boShare(s string) string {
	s = strings.TrimSuffix(strings.TrimSpace(s), "%")
	if s == "" {
		return ""
	}
	whole, frac, _ := strings.Cut(s, ".")
	frac = (frac + "00")[:2]
	var w, f int
	if _, err := fmt.Sscanf(whole, "%d", &w); err != nil {
		return ""
	}
	fmt.Sscanf(frac, "%d", &f)
	return fmt.Sprintf("%05d", w*100+f)
}

// boName splits a name into CDSL's name, middle name and last (search)
// name, within their lengths.
func boName(full string) (first, middle, last string) {
	w := strings.Fields(strings.ToUpper(full))
	switch len(w) {
	case 0:
		return "", "", ""
	case 1:
		return w[0], "", w[0]
	}
	first, last = w[0], w[len(w)-1]
	if m := strings.Join(w[1:len(w)-1], " "); utf8.RuneCountInString(m) <= 20 {
		middle = m
	} else {
		first = strings.Join(w[:len(w)-1], " ")
	}
	return
}

// boAddress breaks an address into CDSL's three 55-character lines.
func boAddress(a string) []string {
	a = strings.Join(strings.Fields(a), " ")
	var lines []string
	for a != "" && len(lines) < 3 {
		if utf8.RuneCountInString(a) <= 55 || len(lines) == 2 {
			lines = append(lines, a)
			break
		}
		cut := 55
		r := []rune(a)
		for i := 55; i > 20; i-- {
			if r[i] == ' ' || r[i-1] == ',' {
				cut = i
				break
			}
		}
		lines = append(lines, strings.TrimSpace(string(r[:cut])))
		a = strings.TrimSpace(string(r[cut:]))
	}
	for len(lines) < 3 {
		lines = append(lines, "")
	}
	return lines
}

var rePINIn = regexp.MustCompile(`\b([1-9][0-9]{5})\b`)

// boDate turns 2006-01-02 into 02012006.
func boDate(d string) string {
	if t, err := time.Parse("2006-01-02", d); err == nil {
		return t.Format("02012006")
	}
	return ""
}

// boInput is what an account is opened from.
type boInput struct {
	C    Client
	App  *Application
	F    map[string]string // staff's details for this request
	Def  map[string]string // the DP's defaults
	Sig  *ClientDoc        // the signature image, if any
	BSDA bool
}

// cdslAppFor finds the online application a client came from.
func (o *Ops) cdslAppFor(ctx context.Context, tenant, ucc string) *Application {
	as, _ := docs.List[Application](ctx, o.Docs, tenant, cApplication, "")
	for i := range as {
		if as[i].UCC == ucc && as[i].Status != "Rejected" {
			return &as[i]
		}
	}
	return nil
}

// cdslSignature is the client's latest signature image CDSL accepts.
func (o *Ops) cdslSignature(ctx context.Context, tenant, ucc string) *ClientDoc {
	ds, _ := o.ClientDocs(ctx, tenant, ucc)
	var best *ClientDoc
	for i := range ds {
		if ds[i].Kind == "Signature" && (ds[i].ContentType == "image/jpeg" || ds[i].ContentType == "image/png") {
			if best == nil || ds[i].At > best.At {
				best = &ds[i]
			}
		}
	}
	return best
}

// boBuild maps an account's details to BOSetupDetails, with the problems
// that would stop CDSL taking it.
func boBuild(in boInput) (boDetails, []string, []string) {
	c, f, def := in.C, in.F, in.Def
	if f == nil {
		f = map[string]string{}
	}
	get := func(k string) string { return strings.TrimSpace(f[k]) }
	var probs, warns []string
	d := boDetails{One: map[string]boBlock{}, Many: map[string][]boBlock{}}

	addr, city, state, pin := get("address"), get("city"), get("state"), get("pin")
	if in.App != nil {
		addr, city, state, pin = firstOf(addr, in.App.Address), firstOf(city, in.App.City), firstOf(state, in.App.State), firstOf(pin, in.App.PIN)
	}
	city, state = firstOf(city, c.City), firstOf(state, c.State)
	if addr == "" {
		probs = append(probs, "Enter the client's address (house, street, area) on this request")
	}
	if pin == "" {
		probs = append(probs, "Enter the client's 6-digit PIN code on this request")
	} else if !rePIN.MatchString(pin) {
		probs = append(probs, "The PIN code must be 6 digits and not start with 0")
	}
	lines := boAddress(addr)
	country := firstOf(get("cust_addr_cntry_code"), firstOf(def["cust_addr_cntry_code"], "IN"))
	first, middle, last := boName(c.Name)
	father := get("father")
	if father == "" && in.App != nil {
		father = in.App.Father
	}
	isd := firstOf(def["primary_mobil_isd_code"], "91")
	fh := boBlock{
		"product_number": firstOf(get("product_number"), def["product_number"]), "bo_name": first, "bo_middle_name": middle, "lastsearch_name": last,
		"fatherhusband_name": strings.ToUpper(father), "address1": lines[0], "address2": lines[1], "address3": lines[2],
		"cust_addr_cntry_code": country, "cust_addr_zip": pin, "cust_addr_city": strings.ToUpper(city), "income_tax_pan": c.PAN,
		"primary_mobil_isd_code": isd, "primary_mobil_no": c.Mobile, "primary_email": c.Email,
	}
	if country != "IN" {
		fh["cust_addr_state"] = strings.ToUpper(state)
	}
	if fh["product_number"] == "" {
		probs = append(probs, "Set the CDSL product number in the BO defaults (CDSL requests tab)")
	}
	d.One["FirstHolderDetails"] = fh
	d.One["PermanentADDdetails"] = boBlock{"address1": lines[0], "address2": lines[1], "address3": lines[2], "cust_addr_cntry_code": country, "cust_addr_zip": pin, "cust_addr_state": fh["cust_addr_state"], "city": fh["cust_addr_city"]}

	gender := get("gender")
	if gender == "" && in.App != nil {
		gender = in.App.Gender
	}
	sex := map[string]string{"Male": "M", "Female": "F", "M": "M", "F": "F"}[gender]
	bsda := "N"
	if in.BSDA {
		bsda = "Y"
	}
	pd := boBlock{"date_of_birth_origin": boDate(c.DOB), "sex_code": sex, "bsda_flag": bsda, "dp_internal_ref_number": c.UCC,
		"dividend_bank_ifsc_code": c.Bank.IFSC, "divnd_acct_numb": c.Bank.Account, "divnd_bank_code": get("divnd_bank_code")}
	for _, k := range []string{"mode_of_operation", "communication_preference", "bo_category", "bo_settlement_planning_flag", "bo_sub_status", "bo_statement_cycle_code",
		"divnd_bank_acct_type", "divnd_bank_ccy", "nationality_code", "bonafide_flag", "email_statement_flag", "occupation", "annual_income_code"} {
		if v := firstOf(get(k), def[k]); v != "" {
			pd[k] = v
		}
	}
	if pd["divnd_bank_code"] == "" {
		probs = append(probs, "Enter the bank's 9-digit MICR code (CDSL's dividend bank code) on this request")
	}
	for _, k := range []string{"mode_of_operation", "communication_preference", "bo_category", "bo_settlement_planning_flag", "bo_sub_status", "bo_statement_cycle_code", "divnd_bank_acct_type", "divnd_bank_ccy"} {
		if pd[k] == "" {
			probs = append(probs, fmt.Sprintf("Set the %s in the BO defaults (CDSL requests tab)", boLabels[k]))
		}
	}
	d.One["ProductDetails"] = pd

	now := time.Now()
	for i, n := range c.Nominees {
		nf, nm, nl := boName(n.Name)
		na := n.Address
		npin := firstOf(get(fmt.Sprintf("nominee%d_pin", i+1)), rePINIn.FindString(na))
		ncity := get(fmt.Sprintf("nominee%d_city", i+1))
		if ncity == "" && npin != "" {
			// the word or words just before the PIN, after the last comma
			before := strings.TrimSpace(strings.Split(na, npin)[0])
			before = strings.TrimRight(before, " ,-")
			if k := strings.LastIndex(before, ","); k >= 0 {
				ncity = strings.TrimSpace(before[k+1:])
			}
		}
		if strings.TrimSpace(na) == "" || npin == "" || ncity == "" {
			probs = append(probs, fmt.Sprintf("Nominee %d (%s): CDSL needs the nominee's address with city and PIN code; complete it on the client or give the city and PIN on this request", i+1, n.Name))
		}
		nl1 := boAddress(strings.TrimSpace(strings.Replace(na, npin, "", 1)))
		rel := boRelation(n.Relation)
		nb := boBlock{"bo_name": nf, "bo_middle_name": nm, "last_search_name": nl, "address1": nl1[0], "address2": nl1[1], "address3": nl1[2], "cust_addr_cntry_code": country,
			"cust_addr_zip": npin, "city": strings.ToUpper(ncity), "dob": boDate(n.DOB), "primary_email": n.Email, "nominee_serial_number": fmt.Sprintf("%02d", i+1),
			"relationship_with_bo": rel, "percentage_of_shares": boShare(n.Share), "residual_securities_flag": "N", "minor_flag": "N"}
		if n.Mobile != "" {
			nb["primary_isdcode"], nb["primary_mobil_no"] = isd, n.Mobile
		}
		if rel == "13" {
			nb["cust_other_relationship"] = strings.ToUpper(n.Relation)
		}
		switch n.IDType {
		case "PAN":
			nb["income_tax_pan"] = n.ID
		case "DL":
			nb["driving_license"] = n.ID
		case "PASSPORT":
			nb["passport"] = n.ID
		}
		if i == 0 {
			nb["residual_securities_flag"] = "Y" // the first nominee takes what the shares leave
		}
		if n.Minor(now) {
			nb["minor_flag"] = "Y"
			gf, gm, gl := boName(n.Guardian)
			gp, gc, ga := get(fmt.Sprintf("guardian%d_pin", i+1)), get(fmt.Sprintf("guardian%d_city", i+1)), get(fmt.Sprintf("guardian%d_address", i+1))
			if ga == "" || gp == "" || gc == "" {
				probs = append(probs, fmt.Sprintf("Nominee %d (%s) is a minor: give the guardian's address, city and PIN code on this request", i+1, n.Name))
			}
			gl1 := boAddress(ga)
			d.Many["NomineeGuardianDetails"] = append(d.Many["NomineeGuardianDetails"], boBlock{"bo_name": gf, "bo_middle_name": gm, "last_search_name": gl, "address1": gl1[0], "address2": gl1[1], "address3": gl1[2],
				"cust_addr_cntry_code": country, "cust_addr_zip": gp, "city": strings.ToUpper(gc), "nominee_serial_number": fmt.Sprintf("%02d", i+1), "relationship_with_bo": boRelation(n.GuardianRelation)})
		}
		d.Many["NomineeDetails"] = append(d.Many["NomineeDetails"], nb)
	}
	if len(c.Nominees) > 3 {
		warns = append(warns, fmt.Sprintf("%d nominees: the handbook's serial numbers go to 03; CDSL may refuse more than three until its 10-nominee change is live", len(c.Nominees)))
	}
	if c.Nominee == "Opted in" && len(c.Nominees) == 0 {
		probs = append(probs, "The client opted to nominate but no nominee details are on record")
	}
	if in.Sig == nil {
		warns = append(warns, "No signature image (JPG or PNG) on the client: CDSL opens the account as \"pending for activation\" until a signature is uploaded in CDAS")
	}
	probs = append(probs, d.check()...)
	return d, dedupe(probs), warns
}

// cdslOpenInput gathers what a client's account is opened from.
func (o *Ops) cdslOpenInput(ctx context.Context, tenant string, r CDSLRequest) (boInput, error) {
	c, err := o.Client(ctx, tenant, r.UCC)
	if err != nil {
		return boInput{}, err
	}
	b := o.bo(ctx, tenant, c)
	return boInput{C: c, App: o.cdslAppFor(ctx, tenant, c.UCC), F: r.Fields, Def: o.CDSLBODefaultsNow(ctx, tenant).Values, Sig: o.cdslSignature(ctx, tenant, c.UCC), BSDA: b.BSDA}, nil
}

// cdslOpenCheck rebuilds the details of an open-account request and says
// what is wrong with them.
func (o *Ops) cdslOpenCheck(ctx context.Context, tenant string, r *CDSLRequest) (boDetails, boInput, error) {
	in, err := o.cdslOpenInput(ctx, tenant, *r)
	if err != nil {
		return boDetails{}, in, err
	}
	d, probs, warns := boBuild(in)
	if in.C.Demat.ID != "" {
		probs = append([]string{"The client already has a demat account (" + in.C.Demat.Depository + " " + in.C.Demat.ID + ")"}, probs...)
	}
	if in.C.Demat.Depository != "" && in.C.Demat.Depository != "CDSL" {
		probs = append([]string{"The client's depository is " + in.C.Demat.Depository + ", not CDSL"}, probs...)
	}
	if in.C.Status != "Active" {
		probs = append(probs, "The client is "+in.C.Status+": a checker activates the client first")
	}
	r.Problems, r.Warnings, r.Name = probs, warns, in.C.Name
	return d, in, nil
}

func boHash(d boDetails) string {
	b, _ := json.Marshal(d)
	return sha256Hex(b)
}

// ---- raising requests ----

// CDSLNew is a request staff raise.
type CDSLNew struct {
	Kind   string            `json:"kind"`
	UCC    string            `json:"ucc"`
	Fields map[string]string `json:"fields"`
	Buyers []CDSLBuyer       `json:"buyers"`
}

// NewCDSLRequest raises an open-account or add-beneficiary request; a
// checker approves it before anything goes to CDSL.
func (o *Ops) NewCDSLRequest(ctx context.Context, tenant, actor string, in CDSLNew) (CDSLRequest, error) {
	if err := o.require(ctx, tenant, actor, "Raising CDSL requests", RoleMaker, RoleChecker); err != nil {
		return CDSLRequest{}, err
	}
	c, err := o.Client(ctx, tenant, in.UCC)
	if err != nil {
		return CDSLRequest{}, invalid("client %s not found", in.UCC)
	}
	switch in.Kind {
	case CDSLOpen:
		if c.Demat.ID != "" {
			return CDSLRequest{}, invalid("%s already has a demat account (%s %s)", c.UCC, c.Demat.Depository, c.Demat.ID)
		}
		if old, ok := o.cdslOpenFor(ctx, tenant, c.UCC); ok {
			return CDSLRequest{}, invalid("an account opening for %s is already in hand (%s, %s)", c.UCC, old.ID, strings.ToLower(old.Status))
		}
		r := CDSLRequest{Kind: CDSLOpen, UCC: c.UCC, Name: c.Name, Fields: cleanFields(in.Fields), Source: "staff", Maker: actor}
		if _, _, err := o.cdslOpenCheck(ctx, tenant, &r); err != nil {
			return r, err
		}
		r.Status = crChecker
		if len(r.Problems) > 0 {
			r.Status = crNeedsDetails
		}
		return o.cdslNewRequest(ctx, tenant, actor, r)
	case CDSLBeneficiary:
		return o.newBeneficiaryRequest(ctx, tenant, actor, c, in.Buyers)
	}
	return CDSLRequest{}, invalid("a request raised here is %s or %s; modifications come from approved change requests, closures from the DP's closure requests", CDSLOpen, CDSLBeneficiary)
}

func cleanFields(f map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range f {
		if v = strings.TrimSpace(v); v != "" && len(k) <= 40 && len(v) <= 200 {
			out[k] = v
		}
	}
	return out
}

// cdslOpenFor is the account opening in hand for a client, if any.
func (o *Ops) cdslOpenFor(ctx context.Context, tenant, ucc string) (CDSLRequest, bool) {
	rs, _ := docs.List[CDSLRequest](ctx, o.Docs, tenant, cCDSLReq, "")
	for _, r := range rs {
		if r.Kind == CDSLOpen && r.UCC == ucc && r.Status != crCancelled {
			return r, true
		}
	}
	return CDSLRequest{}, false
}

// SetCDSLFields changes the details staff gave on a request that is not
// yet approved, and checks it again.
func (o *Ops) SetCDSLFields(ctx context.Context, tenant, actor, id string, fields map[string]string) (CDSLRequest, error) {
	if err := o.require(ctx, tenant, actor, "Raising CDSL requests", RoleMaker, RoleChecker); err != nil {
		return CDSLRequest{}, err
	}
	r, err := o.cdslGet(ctx, tenant, id)
	if err != nil {
		return r, err
	}
	if r.Kind != CDSLOpen || (r.Status != crNeedsDetails && r.Status != crChecker && r.Status != crRejected && r.Status != crFailed) {
		return r.public(), fmt.Errorf("%w: %s is %s; its details cannot be changed now", ErrState, r.ID, strings.ToLower(r.Status))
	}
	r.Fields = cleanFields(fields)
	if _, _, err := o.cdslOpenCheck(ctx, tenant, &r); err != nil {
		return r.public(), err
	}
	r.Status = crChecker
	if len(r.Problems) > 0 {
		r.Status = crNeedsDetails
	}
	r.Maker, r.Checker, r.Hash = actor, "", ""
	r.event(o.now().Format(time.RFC3339), actor, "Details changed")
	if err := o.cdslSave(ctx, tenant, actor, &r); err != nil {
		return r.public(), err
	}
	return r.public(), nil
}

// ---- uploads (§5.5, §5.6) ----

// NewCDSLUpload raises a BO setup upload with its file.
func (o *Ops) NewCDSLUpload(ctx context.Context, tenant, actor, name string, data []byte) (CDSLRequest, error) {
	if err := o.require(ctx, tenant, actor, "Raising CDSL requests", RoleMaker, RoleChecker); err != nil {
		return CDSLRequest{}, err
	}
	ref, warn, err := o.cdslKeepFile(ctx, tenant, name, data)
	if err != nil {
		return CDSLRequest{}, err
	}
	r := CDSLRequest{Kind: CDSLSetupUpload, Source: "staff", File: &ref, Status: crChecker, Maker: actor}
	if warn != "" {
		r.Warnings = []string{warn}
	}
	return o.cdslNewRequest(ctx, tenant, actor, r)
}

// AttachCDSLFile attaches the BO modify file to a modification.
func (o *Ops) AttachCDSLFile(ctx context.Context, tenant, actor, id, name string, data []byte) (CDSLRequest, error) {
	if err := o.require(ctx, tenant, actor, "Raising CDSL requests", RoleMaker, RoleChecker); err != nil {
		return CDSLRequest{}, err
	}
	r, err := o.cdslGet(ctx, tenant, id)
	if err != nil {
		return r, err
	}
	if (r.Kind != CDSLModify && r.Kind != CDSLSetupUpload) || !(r.Status == crNeedsFile || r.Status == crChecker || r.Status == crRejected || r.Status == crFailed) {
		return r.public(), fmt.Errorf("%w: a file can be attached to a modification or setup upload not yet sent (%s is %s)", ErrState, r.ID, strings.ToLower(r.Status))
	}
	ref, warn, err := o.cdslKeepFile(ctx, tenant, name, data)
	if err != nil {
		return r.public(), err
	}
	r.File, r.Status, r.Maker, r.Checker, r.Hash = &ref, crChecker, actor, "", ""
	r.Warnings = nil
	if warn != "" {
		r.Warnings = []string{warn}
	}
	r.event(o.now().Format(time.RFC3339), actor, "File attached: "+ref.Name)
	if err := o.cdslSave(ctx, tenant, actor, &r); err != nil {
		return r.public(), err
	}
	return r.public(), nil
}

var reUploadName = regexp.MustCompile(`^[A-Za-z0-9_.\-]{1,120}$`)

// cdslKeepFile checks and stores an upload file. A ZIP must hold the main
// file in a folder named after it, as §5.5 asks; any other file is the
// main file and is zipped that way when sent.
func (o *Ops) cdslKeepFile(ctx context.Context, tenant, name string, data []byte) (CDSLFileRef, string, error) {
	name = path.Base(strings.TrimSpace(name))
	if !reUploadName.MatchString(name) {
		return CDSLFileRef{}, "", invalid("give the file CDAS's upload file name (letters, digits, _ . -), e.g. BO_UPLD_10900_202412171204_1212.csv")
	}
	if len(data) == 0 || len(data) > 25<<20 {
		return CDSLFileRef{}, "", invalid("the file must be between 1 byte and 25 MB")
	}
	warn := ""
	if strings.HasSuffix(strings.ToLower(name), ".zip") {
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return CDSLFileRef{}, "", invalid("%s is not a readable ZIP file", name)
		}
		main := strings.TrimSuffix(name, path.Ext(name))
		found := false
		for _, f := range zr.File {
			found = found || path.Base(f.Name) == main
		}
		if !found {
			return CDSLFileRef{}, "", invalid("the ZIP must hold the main upload file %s (in a folder of that name, with the signature images); CDSL rejects a ZIP of signatures alone", main)
		}
	} else if !strings.HasPrefix(strings.ToUpper(name), "BO_") {
		warn = "CDAS names BO upload files BO_UPLD_<DP ID>_<date and time>_<sequence>.csv; check the file name"
	}
	sum := sha256.Sum256(data)
	rf, err := o.App.Ingest.Raw.Put(ctx, ingest.RawFile{Tenant: tenant, Source: "CDSL-API-OUT", Name: name, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data)), Content: data, ReceivedAt: o.now()})
	if err != nil {
		return CDSLFileRef{}, "", err
	}
	return CDSLFileRef{Name: name, FileID: rf.ID, Size: int64(len(data)), SHA256: rf.SHA256}, warn, nil
}

// cdslZip is the upload ZIP: the main file in a folder of its own name
// (§5.5), or the ZIP as given.
func cdslZip(name string, data []byte) (string, []byte, error) {
	if strings.HasSuffix(strings.ToLower(name), ".zip") {
		return name, data, nil
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(name + "/" + name)
	if err != nil {
		return "", nil, err
	}
	w.Write(data)
	if err := zw.Close(); err != nil {
		return "", nil, err
	}
	return name + ".zip", buf.Bytes(), nil
}

// CDSLRequestFile returns the file sent with a request, or CDSL's
// response file ("reply").
func (o *Ops) CDSLRequestFile(ctx context.Context, tenant, actor, id, which string) (string, []byte, error) {
	if err := o.require(ctx, tenant, actor, "Viewing CDSL requests", RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return "", nil, err
	}
	r, err := o.cdslGet(ctx, tenant, id)
	if err != nil {
		return "", nil, err
	}
	f := r.File
	if which == "reply" {
		f = r.Reply
	}
	if f == nil {
		return "", nil, ErrNotFound
	}
	rf, err := o.App.Ingest.Raw.Get(ctx, tenant, f.FileID)
	if err != nil {
		return "", nil, err
	}
	o.log(ctx, tenant, actor, "cdsl.file_viewed", r.ID, map[string]any{"file": f.Name})
	return f.Name, rf.Content, nil
}

// ---- acting on requests ----

// CDSLActResult is what an action did; Link is the client's link for a
// transfer or beneficiary request, shown once to the staff who sent it.
type CDSLActResult struct {
	Request CDSLRequest `json:"request"`
	Link    string      `json:"link,omitempty"`
}

// ActOnCDSLRequest moves a request on:
//   - approve / reject: the checker (not the maker) decides; an approved
//     request goes to CDSL at once (or says "Do in CDAS" with the API off)
//   - retry: send a failed or rejected request again, with a new reference
//   - status: ask CDSL how an upload is doing
//   - link: send the client a new link (transfer, beneficiary)
//   - done: record that it was done in CDAS (value: the BO ID for an
//     account opening)
//   - activated: the account CDSL opened without a signature is now active
//   - cancel: stop a request that has not completed
func (o *Ops) ActOnCDSLRequest(ctx context.Context, tenant, actor, id, action, note, value string) (CDSLActResult, error) {
	roles := []string{RoleChecker}
	if action == "retry" || action == "status" || action == "link" || action == "cancel" {
		roles = append(roles, RoleMaker)
	}
	if err := o.require(ctx, tenant, actor, "Acting on CDSL requests", roles...); err != nil {
		return CDSLActResult{}, err
	}
	r, err := o.cdslGet(ctx, tenant, id)
	if err != nil {
		return CDSLActResult{}, err
	}
	now := o.now().Format(time.RFC3339)
	res := CDSLActResult{}
	x, set := o.cdslConnFor(ctx, tenant)
	_, fam := cdslKindInfo(r.Kind)
	apiOn := set && x.On(fam)
	switch action {
	case "approve":
		if r.Status != crChecker {
			return res, fmt.Errorf("%w: %s is %s, not waiting for a checker", ErrState, r.ID, strings.ToLower(r.Status))
		}
		if r.Maker == actor {
			return res, fmt.Errorf("%w: the person who raised or last changed a request cannot approve it", ErrForbidden)
		}
		if r.Kind == CDSLOpen {
			d, _, err := o.cdslOpenCheck(ctx, tenant, &r)
			if err != nil {
				return res, err
			}
			if len(r.Problems) > 0 {
				r.Status = crNeedsDetails
				r.event(now, actor, "Not approved: "+strings.Join(r.Problems, "; "))
				_ = o.cdslSave(ctx, tenant, actor, &r)
				return CDSLActResult{Request: r.public()}, invalid("%s", strings.Join(r.Problems, "; "))
			}
			r.Hash = boHash(d)
		}
		r.Checker, r.Status = actor, crApproved
		r.event(now, actor, "Approved"+notePart(note))
		if !apiOn {
			r.Status = crManual
			r.event(now, actor, "The CDSL API for this is off: do it in CDAS and record it here")
			break
		}
		link, err := o.cdslSend(ctx, tenant, actor, x, &r)
		if err != nil {
			o.log(ctx, tenant, actor, "cdsl.send_error", r.ID, map[string]any{"kind": r.Kind, "error": err.Error()})
		}
		res.Link = link
	case "reject":
		if r.Status != crChecker && r.Status != crNeedsDetails && r.Status != crNeedsFile {
			return res, fmt.Errorf("%w: %s is %s", ErrState, r.ID, strings.ToLower(r.Status))
		}
		if strings.TrimSpace(note) == "" {
			return res, invalid("give the reason")
		}
		if r.Maker == actor {
			return res, fmt.Errorf("%w: the person who raised a request cannot reject it; cancel it instead", ErrForbidden)
		}
		r.Status, r.Checker = crCancelled, actor
		r.event(now, actor, "Rejected by the checker: "+note)
		o.cdslSourceDone(ctx, tenant, actor, r, false, "rejected by the checker: "+note)
	case "cancel":
		if !r.open() || r.Status == crSent || r.Status == crConfirmed {
			return res, fmt.Errorf("%w: %s is %s and cannot be cancelled here", ErrState, r.ID, strings.ToLower(r.Status))
		}
		if strings.TrimSpace(note) == "" {
			return res, invalid("give the reason")
		}
		r.Status = crCancelled
		r.event(now, actor, "Cancelled: "+note)
		o.cdslSourceDone(ctx, tenant, actor, r, false, "cancelled: "+note)
	case "retry":
		if r.Status != crFailed && r.Status != crRejected {
			return res, fmt.Errorf("%w: only a failed or rejected request is retried (%s is %s)", ErrState, r.ID, strings.ToLower(r.Status))
		}
		if !apiOn {
			return res, fmt.Errorf("%w: the CDSL API for %s is off in Connections", ErrState, strings.ToLower(r.Kind))
		}
		if r.Kind == CDSLOpen {
			d, _, err := o.cdslOpenCheck(ctx, tenant, &r)
			if err != nil {
				return res, err
			}
			if len(r.Problems) > 0 {
				return CDSLActResult{Request: r.public()}, invalid("%s", strings.Join(r.Problems, "; "))
			}
			if boHash(d) != r.Hash {
				// the client's details changed since the checker approved
				r.Status, r.Checker = crChecker, ""
				r.event(now, actor, "Details changed since approval: back to the checker")
				break
			}
		}
		r.event(now, actor, "Retried"+notePart(note))
		link, err := o.cdslSend(ctx, tenant, actor, x, &r)
		if err != nil {
			o.log(ctx, tenant, actor, "cdsl.send_error", r.ID, map[string]any{"kind": r.Kind, "error": err.Error()})
		}
		res.Link = link
	case "status":
		if (r.Kind != CDSLModify && r.Kind != CDSLSetupUpload) || r.Status != crSent {
			return res, fmt.Errorf("%w: only an upload sent to CDSL has a status to ask for", ErrState)
		}
		if !set {
			return res, fmt.Errorf("%w: the CDSL connection is not set", ErrState)
		}
		o.cdslPoll(ctx, tenant, actor, x, &r)
	case "link":
		if (r.Kind != CDSLTransfer && r.Kind != CDSLBeneficiary) || (r.Status != crClient && r.Status != crFailed && r.Status != crRejected) {
			return res, fmt.Errorf("%w: a new link is sent for a transfer or beneficiary request waiting for the client", ErrState)
		}
		if !apiOn {
			return res, fmt.Errorf("%w: the CDSL API for %s is off in Connections", ErrState, strings.ToLower(r.Kind))
		}
		link, err := o.cdslClientLink(ctx, tenant, actor, x, &r)
		if err != nil {
			return res, err
		}
		res.Link = link
	case "done":
		if r.Status != crManual && r.Status != crFailed && r.Status != crRejected && r.Status != crConfirmed {
			return res, fmt.Errorf("%w: %s is %s; only a request to be done in CDAS (or one that failed) is recorded as done there", ErrState, r.ID, strings.ToLower(r.Status))
		}
		if strings.TrimSpace(note) == "" {
			return res, invalid("say what was done in CDAS (e.g. the CDAS reference)")
		}
		if err := o.cdslManualDone(ctx, tenant, actor, &r, note, value); err != nil {
			return res, err
		}
	case "activated":
		if r.Kind != CDSLOpen || r.Status != crCompleted || !r.PendingActivation {
			return res, fmt.Errorf("%w: only an account CDSL opened pending activation is activated here", ErrState)
		}
		c, err := o.Client(ctx, tenant, r.UCC)
		if err != nil {
			return res, err
		}
		b := o.bo(ctx, tenant, c)
		b.Status = "Active"
		if err := docs.Upsert(ctx, o.Docs, tenant, cBO, b.UCC, b, actor); err != nil {
			return res, err
		}
		r.PendingActivation = false
		r.event(now, actor, "Account activated (signature uploaded in CDAS)"+notePart(note))
	default:
		return res, invalid("action must be approve, reject, retry, status, link, done, activated or cancel")
	}
	if err := o.cdslSave(ctx, tenant, actor, &r); err != nil {
		return res, err
	}
	if !r.open() || r.Status == crSent || r.Status == crClient {
		o.closeWork(ctx, tenant, actor, "cdslbo", r.ID, strings.ToLower(r.Status))
	}
	o.log(ctx, tenant, actor, "cdsl.request_"+action, r.ID, map[string]any{"kind": r.Kind, "status": r.Status, "bo": cdslMaskBO(r.BOID)})
	res.Request = r.public()
	return res, nil
}

func notePart(note string) string {
	if strings.TrimSpace(note) == "" {
		return ""
	}
	return ": " + strings.TrimSpace(note)
}

// cdslSend sends an approved request to CDSL. Errors are recorded on the
// request (Failed, or Rejected by CDSL) as well as returned.
func (o *Ops) cdslSend(ctx context.Context, tenant, actor string, x cdslConn, r *CDSLRequest) (string, error) {
	r.Tries++
	r.Error = ""
	var err error
	link := ""
	switch r.Kind {
	case CDSLOpen:
		err = o.cdslSendOpen(ctx, tenant, actor, x, r)
	case CDSLModify, CDSLSetupUpload:
		err = o.cdslSendUpload(ctx, tenant, actor, x, r)
	case CDSLTransfer, CDSLBeneficiary:
		link, err = o.cdslClientLink(ctx, tenant, actor, x, r)
	}
	now := o.now().Format(time.RFC3339)
	if err != nil {
		r.Error = err.Error()
		if r.Status != crRejected {
			r.Status = crFailed
		}
		r.event(now, actor, r.Status+": "+r.Error)
		o.raiseWork(ctx, tenant, "cdslbo", r.ID, fmt.Sprintf("CDSL %s %s: %s", strings.ToLower(r.Kind), r.ID, strings.ToLower(r.Status)),
			fmt.Sprintf("%s %s: %s. Fix it and retry on the DP's CDSL requests tab.", r.UCC, r.Name, r.Error), r.UCC, RoleMaker, "cdslreq", "")
	}
	return link, err
}

// cdslSendOpen sends an account opening (§5.4).
func (o *Ops) cdslSendOpen(ctx context.Context, tenant, actor string, x cdslConn, r *CDSLRequest) error {
	d, in, err := o.cdslOpenCheck(ctx, tenant, r)
	if err != nil {
		return err
	}
	if len(r.Problems) > 0 {
		return invalid("%s", strings.Join(r.Problems, "; "))
	}
	if x.Callback == "" {
		return invalid("enter Ledgerline's public address in the CDSL APIs connection (callback address): CDSL sends the new BO ID there")
	}
	tok, err := o.cdslNewToken(ctx, tenant, actor, r, "bo")
	if err != nil {
		return err
	}
	urn := strings.ReplaceAll(r.ID, "-", "") + fmt.Sprintf("%02d", r.Tries)
	r.Ref, r.Refs = urn, append(r.Refs, urn)
	at, _ := time.Parse(time.RFC3339, r.At)
	receipt := at.In(ist).Format("02012006150405")
	cb := x.Callback + "/hooks/" + url.PathEscape(tenant) + "/cdsl/bo/" + tok
	det, _ := json.Marshal(d)
	// The AuthSign is over the body as the handbook writes it, with the
	// details as an object; the same values go in the multipart form.
	var body bytes.Buffer
	body.WriteString(`{"receiptdate":`)
	jw := func(s string) { b, _ := json.Marshal(s); body.Write(b) }
	jw(receipt)
	body.WriteString(`,"callbackurl":`)
	jw(cb)
	body.WriteString(`,"urn":`)
	jw(urn)
	body.WriteString(`,"BOSetupDetails":`)
	body.Write(det)
	body.WriteString(`,"Signature":""}`)
	form := url.Values{"receiptdate": {receipt}, "callbackurl": {cb}, "urn": {urn}, "BOSetupDetails": {string(det)}, "Signature": {""}}
	req := cdslRequest{Path: firstOf(x.c.Values["path_bo_online"], cdslPathBOOnline), JSON: body.Bytes(), Form: form, Multipart: true, Header: map[string]string{}}
	r.PendingActivation = in.Sig == nil
	if in.Sig != nil {
		rf, err := o.App.Ingest.Raw.Get(ctx, tenant, in.Sig.FileID)
		if err == nil {
			ext := map[string]string{"image/jpeg": ".jpg", "image/png": ".png"}[in.Sig.ContentType]
			req.Files = []cdslFile{{Field: "file", Name: r.UCC + "_signature" + ext, Data: rf.Content}}
			req.Header["BOSignComputeHash"] = x.FileHash(rf.Content)
		} else {
			r.PendingActivation = true
		}
	}
	// What is kept: the body with the callback token cut out.
	r.Request = strings.Replace(body.String(), tok, "…", 1)
	r.SentAt = o.now().Format(time.RFC3339)
	m, err := x.signed(ctx, tenant, req)
	if err != nil {
		return err
	}
	ans, _ := json.Marshal(m)
	r.Response = string(ans)
	crn := cdslStr(m, "CRN")
	if !strings.EqualFold(cdslStr(m, "success"), "true") {
		id, msg := cdslError(m)
		r.Status = crRejected
		r.CDSLRef = crn
		return fmt.Errorf("CDSL did not take the account opening: %s", cdslExplain(id, msg))
	}
	r.Status, r.CDSLRef = crSent, crn
	r.event(o.now().Format(time.RFC3339), actor, "Sent to CDSL; CRN "+crn+"; CDSL sends the BO ID back when the account is opened")
	o.log(ctx, tenant, actor, "cdsl.bo_open_sent", r.ID, map[string]any{"ucc": r.UCC, "urn": urn, "crn": crn, "signature": !r.PendingActivation})
	return nil
}

// CDSLBOCallback takes CDSL's answer to an account opening (§5.4
// call-back): the BO ID when completed, the error file when rejected.
func (o *Ops) CDSLBOCallback(ctx context.Context, tenant, tok string, body []byte) error {
	r, err := o.cdslByToken(ctx, tenant, tok)
	if err != nil || r.Kind != CDSLOpen {
		return ErrNotFound
	}
	var raw map[string]any
	if json.Unmarshal(cdslCleanJSON(body), &raw) != nil {
		return invalid("the call-back is not JSON")
	}
	m := cdslBody(body)
	urn, crn, boid, st := cdslStr(m, "urn"), cdslStr(m, "crn"), cdslStr(m, "boid"), cdslStr(m, "status")
	if !contains(r.Refs, urn) || (r.CDSLRef != "" && crn != "" && crn != r.CDSLRef) {
		o.log(ctx, tenant, SystemActor, "cdsl.callback_mismatch", r.ID, map[string]any{"urn": urn})
		return invalid("the call-back's URN or CRN does not match the request")
	}
	if r.Status == crCompleted {
		return nil // CDSL sent it again
	}
	actor := "cdsl:callback"
	now := o.now().Format(time.RFC3339)
	fname, fdata := cdslStr(m, "filename"), []byte(nil)
	if s := cdslStr(m, "resfile"); s != "" {
		fdata, _ = base64.StdEncoding.DecodeString(s)
	}
	if fdata != nil {
		name := firstOf(path.Base(fname), r.ID+"-cdsl.txt")
		sum := sha256.Sum256(fdata)
		if rf, err := o.App.Ingest.Raw.Put(ctx, ingest.RawFile{Tenant: tenant, Source: "CDSL-API-IN", Name: name, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(fdata)), Content: fdata, ReceivedAt: o.now()}); err == nil {
			r.Reply = &CDSLFileRef{Name: name, FileID: rf.ID, Size: int64(len(fdata)), SHA256: rf.SHA256}
		}
	}
	r.CDSLRef = firstOf(r.CDSLRef, crn)
	r.CDSLStatus = st
	r.Response = cdslRedactFile(body)
	switch st {
	case "1":
		if !regexp.MustCompile(`^[0-9]{16}$`).MatchString(boid) {
			return invalid("the call-back says completed but has no 16-digit BO ID")
		}
		if err := o.cdslApplyBOID(ctx, tenant, actor, &r, boid, r.Reply); err != nil {
			return err
		}
		r.Status, r.DoneAt = crCompleted, now
		what := "CDSL opened the account: BO ID " + boid
		if r.PendingActivation {
			what += " (pending activation until a signature is uploaded in CDAS)"
		}
		r.event(now, actor, what)
		o.closeWork(ctx, tenant, actor, "cdslbo", r.ID, "completed")
	case "2":
		r.Status = crRejected
		r.Error = "CDSL rejected the account opening" + cdslFileReason(fdata)
		r.event(now, actor, r.Error)
		o.raiseWork(ctx, tenant, "cdslbo", r.ID, "CDSL rejected account opening "+r.ID, r.UCC+" "+r.Name+": "+r.Error+". See CDSL's error file on the request, correct the details and retry.", r.UCC, RoleMaker, "cdslreq", "")
	default:
		return invalid("the call-back's status %q is neither 1 (completed) nor 2 (rejected)", st)
	}
	if err := o.cdslSave(ctx, tenant, actor, &r); err != nil {
		return err
	}
	o.log(ctx, tenant, actor, "cdsl.bo_open_answer", r.ID, map[string]any{"ucc": r.UCC, "status": r.Status, "bo": cdslMaskBO(boid)})
	return nil
}

// cdslRedactFile keeps a call-back without the (large) file in it.
func cdslRedactFile(body []byte) string {
	var m map[string]any
	if json.Unmarshal(cdslCleanJSON(body), &m) != nil {
		if len(body) > 2000 {
			body = body[:2000]
		}
		return string(body)
	}
	for k, v := range m {
		if strings.EqualFold(k, "resfile") {
			if s, ok := v.(string); ok {
				m[k] = fmt.Sprintf("(%d characters of base64, kept as the reply file)", len(s))
			}
		}
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// cdslFileReason is the start of an error file, when it is text.
func cdslFileReason(b []byte) string {
	if len(b) == 0 || !utf8.Valid(b) || strings.ContainsRune(string(b), 0) {
		return ""
	}
	s := strings.Join(strings.Fields(string(b)), " ")
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return ": " + s
}

// cdslApplyBOID puts a new BO ID on the client and the DP's BO record.
func (o *Ops) cdslApplyBOID(ctx context.Context, tenant, actor string, r *CDSLRequest, boid string, ack *CDSLFileRef) error {
	c, err := docs.Update(ctx, o.Docs, tenant, cClient, r.UCC, actor, func(c *Client) error {
		c.Demat = Demat{Depository: "CDSL", ID: boid}
		return nil
	})
	if err != nil {
		return err
	}
	b, _, gerr := docs.Get[BOAccount](ctx, o.Docs, tenant, cBO, c.UCC)
	if gerr != nil {
		b = BOAccount{UCC: c.UCC}
	}
	b.BOID, b.Depository, b.Status, b.Opened, b.ClosedOn = boid, "CDSL", "Active", o.businessDate(ctx, tenant), ""
	if r.PendingActivation {
		b.Status = "Pending activation"
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cBO, c.UCC, b, actor); err != nil {
		return err
	}
	r.BOID = boid
	if ack != nil {
		n, err := o.next(ctx, tenant, "clientdoc", actor)
		if err == nil {
			cd := ClientDoc{ID: fmt.Sprintf("DOC-%06d", n), UCC: c.UCC, Kind: "Other", Name: "CDSL account opening acknowledgement " + ack.Name, ContentType: "application/octet-stream", Size: ack.Size, SHA256: ack.SHA256, FileID: ack.FileID, By: actor, At: o.now().Format(time.RFC3339)}
			_, _ = docs.Put(ctx, o.Docs, tenant, cClientDoc, c.UCC+"|"+cd.ID, cd, 0, actor)
		}
	}
	o.notify(ctx, tenant, actor, "cdsl.bo_opened", c.UCC, map[string]string{"bo_id": boid})
	o.log(ctx, tenant, actor, "dp.bo_opened", c.UCC, map[string]any{"bo": cdslMaskBO(boid), "request": r.ID})
	// The new account's PAN, mobile and email are counted at CDSL (§5.3).
	o.cdslCountSoon(ctx, tenant, c.UCC)
	return nil
}

// cdslManualDone records a request done in CDAS.
func (o *Ops) cdslManualDone(ctx context.Context, tenant, actor string, r *CDSLRequest, note, value string) error {
	now := o.now().Format(time.RFC3339)
	switch r.Kind {
	case CDSLOpen:
		boid := strings.TrimSpace(value)
		if !regexp.MustCompile(`^[0-9]{16}$`).MatchString(boid) {
			return invalid("give the 16-digit BO ID CDAS gave the account")
		}
		r.PendingActivation = false
		if err := o.cdslApplyBOID(ctx, tenant, actor, r, boid, nil); err != nil {
			return err
		}
	case CDSLTransfer:
		if err := o.cdslFinishClosure(ctx, tenant, actor, r, "done in CDAS: "+note); err != nil {
			return err
		}
	}
	r.Status, r.DoneAt = crManualDone, now
	r.event(now, actor, "Done in CDAS: "+note)
	o.cdslSourceDone(ctx, tenant, actor, *r, true, "done in CDAS")
	return nil
}

// cdslSourceDone tells what a request came from how it ended (a closure's
// DP request is completed or rejected with it).
func (o *Ops) cdslSourceDone(ctx context.Context, tenant, actor string, r CDSLRequest, ok bool, note string) {
	if r.Kind != CDSLTransfer || !strings.HasPrefix(r.Source, "DPR-") || ok {
		return
	}
	_, _ = docs.Update(ctx, o.Docs, tenant, cDPReq, r.Source, actor, func(x *DPRequest) error {
		if x.Status == "Sent to CDSL" {
			x.Status, x.Note = "Rejected", "CDSL "+r.ID+" "+note
		}
		return nil
	})
}

// cdslSendUpload uploads a BO setup or modify file (§5.5, §5.6).
func (o *Ops) cdslSendUpload(ctx context.Context, tenant, actor string, x cdslConn, r *CDSLRequest) error {
	if r.File == nil {
		return invalid("attach the BO upload file first")
	}
	rf, err := o.App.Ingest.Raw.Get(ctx, tenant, r.File.FileID)
	if err != nil {
		return err
	}
	zname, zdata, err := cdslZip(r.File.Name, rf.Content)
	if err != nil {
		return err
	}
	pth, kv := cdslPathBOSetupUpload, []string{"UploadId", firstOf(x.c.Values["upload_bo_setup"], "08")}
	if r.Kind == CDSLModify {
		pth, kv = cdslPathBOModUpload, []string{"DPID", x.DPID, "UploadId", firstOf(x.c.Values["upload_bo_modify"], "11")}
	}
	r.Ref, r.Refs = zname, append(r.Refs, zname)
	r.Request = string(cdslJSON(append(kv, "Signature", "")...)) + " + " + zname
	r.SentAt = o.now().Format(time.RFC3339)
	ack, m, err := x.Upload(ctx, tenant, pth, cdslFile{Field: "file", Name: zname, Data: zdata}, kv...)
	if m != nil {
		b, _ := json.Marshal(m)
		r.Response = string(b)
	}
	if err != nil {
		if m != nil {
			r.Status = crRejected
		}
		return err
	}
	r.Status, r.CDSLRef, r.CDSLStatus = crSent, ack, ""
	r.event(o.now().Format(time.RFC3339), actor, "Uploaded "+zname+"; acknowledgement "+ack)
	o.log(ctx, tenant, actor, "cdsl.upload_sent", r.ID, map[string]any{"kind": r.Kind, "file": zname, "ack": ack})
	return nil
}

// cdslPoll asks CDSL how an upload is doing and, once it is processed,
// fetches the response file.
func (o *Ops) cdslPoll(ctx context.Context, tenant, actor string, x cdslConn, r *CDSLRequest) {
	now := o.now().Format(time.RFC3339)
	st, _, err := x.UploadStatus(ctx, tenant, r.CDSLRef)
	if err != nil {
		r.Error = err.Error()
		return
	}
	r.Error = ""
	if st != r.CDSLStatus {
		r.event(now, actor, "CDSL status: "+st)
	}
	r.CDSLStatus = st
	done, failed := cdslUploadDone(st)
	if !done {
		return
	}
	upID := firstOf(x.c.Values["upload_bo_setup"], "08")
	if r.Kind == CDSLModify {
		upID = firstOf(x.c.Values["upload_bo_modify"], "11")
	}
	if name, data, err := x.UploadResponse(ctx, tenant, upID, r.Ref); err == nil {
		sum := sha256.Sum256(data)
		if rf, err := o.App.Ingest.Raw.Put(ctx, ingest.RawFile{Tenant: tenant, Source: "CDSL-API-IN", Name: path.Base(name), SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data)), Content: data, ReceivedAt: o.now()}); err == nil {
			r.Reply = &CDSLFileRef{Name: path.Base(name), FileID: rf.ID, Size: int64(len(data)), SHA256: rf.SHA256}
			r.event(now, actor, "CDSL's response file: "+path.Base(name))
		}
	} else {
		r.event(now, actor, "No response file yet: "+err.Error())
	}
	if failed {
		r.Status = crRejected
		r.Error = "CDSL did not process the file (" + st + "); see its response file"
		o.raiseWork(ctx, tenant, "cdslbo", r.ID, "CDSL did not process "+r.Ref, r.Error, r.UCC, RoleMaker, "cdslreq", "")
		return
	}
	r.Status, r.DoneAt = crCompleted, now
	o.closeWork(ctx, tenant, actor, "cdslbo", r.ID, "processed")
	if r.Kind == CDSLModify {
		o.cdslCountSoon(ctx, tenant, r.UCC)
	}
}

// ---- modifications from approved change requests ----

// cdslModifyKinds are the changes the depository holds too.
var cdslModifyKinds = []string{"Email", "Mobile", "Address", "Bank account", "Nomination"}

// cdslChangesFor maps an approved change to the fields of the BO record
// it changes (§5.4's field names, which the BO modify layout shares).
func cdslChangesFor(ch ChangeRequest, c Client, isd string) []CDSLChange {
	n := ch.New
	var out []CDSLChange
	add := func(sec, f, label, old, new string) {
		if strings.TrimSpace(new) != "" || old != "" {
			out = append(out, CDSLChange{Section: sec, Field: f, Label: label, Old: old, New: new})
		}
	}
	const fh = "FirstHolderDetails"
	switch ch.Kind {
	case "Email":
		add(fh, "primary_email", "Email", ch.Old["email"], n["email"])
	case "Mobile":
		add(fh, "primary_mobil_isd_code", "Mobile ISD code", "", isd)
		add(fh, "primary_mobil_no", "Mobile", ch.Old["mobile"], n["mobile"])
	case "Address":
		if a := n["address"]; a != "" {
			l := boAddress(a)
			add(fh, "address1", "Address line 1", "", l[0])
			add(fh, "address2", "Address line 2", "", l[1])
			add(fh, "address3", "Address line 3", "", l[2])
		}
		add(fh, "cust_addr_city", "City", strings.ToUpper(ch.Old["city"]), strings.ToUpper(n["city"]))
		if n["state"] != "" {
			add(fh, "cust_addr_state", "State", strings.ToUpper(ch.Old["state"]), strings.ToUpper(n["state"]))
		}
		if n["pin"] != "" {
			add(fh, "cust_addr_zip", "PIN code", "", n["pin"])
		}
	case "Bank account":
		add("ProductDetails", "dividend_bank_ifsc_code", "Bank IFSC", ch.Old["ifsc"], strings.ToUpper(n["ifsc"]))
		add("ProductDetails", "divnd_acct_numb", "Bank account", ch.Old["account"], MaskAccount(n["account"]))
	case "Nomination":
		if len(c.Nominees) == 0 {
			add("NomineeDetails", "-", "Nomination", "", "Opted out (no nominee)")
		}
		for i, x := range c.Nominees {
			add("NomineeDetails", fmt.Sprintf("%02d", i+1), fmt.Sprintf("Nominee %d", i+1), "",
				fmt.Sprintf("%s · relationship %s (%s) · share %s", strings.ToUpper(x.Name), boRelation(x.Relation), x.Relation, boShare(x.Share)))
		}
	}
	return out
}

// cdslCaptureModifications raises a modification for every approved change
// on a CDSL account that has none yet. Changes approved before the BO API
// was first on are left alone.
func (o *Ops) cdslCaptureModifications(ctx context.Context, tenant string, x cdslConn, since string) int {
	chs, _ := docs.List[ChangeRequest](ctx, o.Docs, tenant, cChange, "")
	isd := firstOf(o.CDSLBODefaultsNow(ctx, tenant).Values["primary_mobil_isd_code"], "91")
	n := 0
	for _, ch := range chs {
		if ch.Status != "Approved" || !cdslNotBefore(ch.Decided, since) || !contains(cdslModifyKinds, ch.Kind) {
			continue
		}
		if _, ok := o.cdslBySource(ctx, tenant, ch.ID); ok {
			continue
		}
		c, err := o.Client(ctx, tenant, ch.UCC)
		if err != nil {
			continue
		}
		b := o.bo(ctx, tenant, c)
		if b.Depository != "CDSL" || b.BOID == "" || b.Status == "Closed" || b.Status == "Not opened" {
			continue
		}
		r := CDSLRequest{Kind: CDSLModify, UCC: c.UCC, Name: c.Name, BOID: b.BOID, Source: ch.ID, Changes: cdslChangesFor(ch, c, isd), Maker: SystemActor, Status: crNeedsFile}
		if !x.On(cdslFamBO) {
			r.Status = crManual
		}
		r, err = o.cdslNewRequest(ctx, tenant, SystemActor, r)
		if err != nil {
			continue
		}
		n++
		what := "attach the BO modify file (CDAS upload layout) for it; a checker then approves and it is uploaded to CDSL"
		if r.Status == crManual {
			what = "the CDSL BO API is off: make the change in CDAS and mark it done"
		}
		o.raiseWork(ctx, tenant, "cdslbo", r.ID, fmt.Sprintf("Send %s's %s change to CDSL", c.UCC, strings.ToLower(ch.Kind)),
			fmt.Sprintf("%s (%s) was approved; CDSL's record of demat account %s must change too: %s.", ch.ID, ch.Kind, cdslMaskBO(b.BOID), what), c.UCC, RoleMaker, "cdslreq", "")
	}
	return n
}

// ---- automatic account opening ----

// cdslAutoOpen raises an account opening for each active client a checker
// approved whose demat account is to be opened with the DP (CDSL, no BO
// ID): those from an online application and those staff entered. The
// client's activation is the checker's approval, so a complete request
// goes to CDSL at once; one with missing details waits for staff.
func (o *Ops) cdslAutoOpen(ctx context.Context, tenant string, x cdslConn, since string) int {
	cs, _ := o.Clients(ctx, tenant)
	n := 0
	for _, c := range cs {
		if c.Status != "Active" || c.Demat.ID != "" || (c.Demat.Depository != "" && c.Demat.Depository != "CDSL") || c.KYCDate < since {
			continue
		}
		if _, ok := o.cdslOpenFor(ctx, tenant, c.UCC); ok {
			continue
		}
		src := "client " + c.UCC
		if a := o.cdslAppFor(ctx, tenant, c.UCC); a != nil {
			src = "online application " + a.ID
		}
		r := CDSLRequest{Kind: CDSLOpen, UCC: c.UCC, Name: c.Name, Source: src, Maker: firstOf(c.CreatedBy, SystemActor), Checker: c.ActivatedBy}
		d, _, err := o.cdslOpenCheck(ctx, tenant, &r)
		if err != nil {
			continue
		}
		r.Status = crApproved
		if len(r.Problems) > 0 {
			r.Status, r.Checker = crNeedsDetails, ""
		} else {
			r.Hash = boHash(d)
		}
		r, err = o.cdslNewRequest(ctx, tenant, SystemActor, r)
		if err != nil {
			continue
		}
		n++
		if r.Status == crNeedsDetails {
			o.raiseWork(ctx, tenant, "cdslbo", r.ID, "Complete "+c.UCC+"'s details to open the demat account",
				c.Name+" is active with no demat account. Before CDSL can open it: "+strings.Join(r.Problems, "; ")+".", c.UCC, RoleMaker, "cdslreq", "")
			continue
		}
		r.event(o.now().Format(time.RFC3339), SystemActor, "Approved by the client's activation ("+c.ActivatedBy+")")
		_, _ = o.cdslSend(ctx, tenant, SystemActor, x, &r)
		_ = o.cdslSave(ctx, tenant, SystemActor, &r)
	}
	return n
}

// ---- the tick ----

type cdslTickState struct {
	Since     map[string]string `json:"since"` // family → when the tick first saw it on
	LastPoll  string            `json:"last_poll,omitempty"`
	LastCount string            `json:"last_count,omitempty"`
}

// cdslBOTick runs the BO lifecycle: open accounts for newly activated
// clients, capture approved changes, follow uploads, retry calls that did
// not get through, chase requests CDSL or the client have not answered,
// and count active demat accounts for new and changed accounts.
func (o *Ops) cdslBOTick(ctx context.Context, tenant string, now time.Time) {
	x, ok := o.cdslConnFor(ctx, tenant)
	if !ok {
		return
	}
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cCDSLState, "tick", cdslTickState{})
	if st.Since == nil {
		st.Since = map[string]string{}
	}
	day := now.In(ist).Format("2006-01-02")
	for _, f := range []string{cdslFamBO, cdslFamBOCount, cdslFamTransfer, cdslFamBeneficiary} {
		if x.On(f) && st.Since[f] == "" {
			st.Since[f] = now.In(ist).Truncate(time.Second).Format(time.RFC3339)
		}
	}
	if x.On(cdslFamBO) {
		o.cdslAutoOpen(ctx, tenant, x, st.Since[cdslFamBO][:10])
	}
	if since := st.Since[cdslFamBO]; since != "" {
		o.cdslCaptureModifications(ctx, tenant, x, since)
	}
	rs, _ := docs.List[CDSLRequest](ctx, o.Docs, tenant, cCDSLReq, "")
	poll := st.LastPoll == "" || cdslSince(st.LastPoll, now) >= 10*time.Minute
	for _, r := range rs {
		_, fam := cdslKindInfo(r.Kind)
		switch {
		case r.Status == crSent && (r.Kind == CDSLModify || r.Kind == CDSLSetupUpload) && poll && x.On(fam):
			o.cdslPoll(ctx, tenant, SystemActor, x, &r)
			_ = o.cdslSave(ctx, tenant, SystemActor, &r)
		case r.Status == crApproved && x.On(fam):
			_, _ = o.cdslSend(ctx, tenant, SystemActor, x, &r)
			_ = o.cdslSave(ctx, tenant, SystemActor, &r)
		case r.Status == crFailed && r.Tries < 3 && r.Kind == CDSLOpen && x.On(fam) && cdslSince(r.Updated, now) >= 30*time.Minute && cdslNetFailure(r.Error):
			r.event(now.Format(time.RFC3339), SystemActor, "Retried automatically")
			_, _ = o.cdslSend(ctx, tenant, SystemActor, x, &r)
			_ = o.cdslSave(ctx, tenant, SystemActor, &r)
		case r.Status == crSent && r.Kind == CDSLOpen && cdslSince(r.SentAt, now) >= 24*time.Hour:
			o.raiseWork(ctx, tenant, "cdslbo", r.ID, "No BO ID from CDSL for "+r.UCC,
				fmt.Sprintf("Account opening %s (CRN %s) was sent on %s and CDSL has not called back. Check the account in CDAS; if it is open there, record its BO ID on the request.", r.ID, r.CDSLRef, fmtDay(r.SentAt[:10])), r.UCC, RoleMaker, "cdslreq", day)
		case (r.Status == crClient || r.Status == crConfirmed) && cdslSince(r.Updated, now) >= 72*time.Hour:
			o.raiseWork(ctx, tenant, "cdslbo", r.ID, fmt.Sprintf("%s for %s is still %s", r.Kind, r.UCC, strings.ToLower(r.Status)),
				fmt.Sprintf("%s %s has been %s since %s. Remind the client, send a new link, or cancel it.", r.Kind, r.ID, strings.ToLower(r.Status), fmtDay(r.Updated[:10])), r.UCC, RoleMaker, "cdslreq", day)
		}
	}
	if poll {
		st.LastPoll = now.Format(time.RFC3339)
	}
	if x.On(cdslFamBOCount) {
		o.cdslCountTick(ctx, tenant, x, now)
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cCDSLState, "tick", st, SystemActor)
}

// cdslNotBefore says whether time a is at or after time b (RFC 3339, any
// zone).
func cdslNotBefore(a, b string) bool {
	ta, err1 := time.Parse(time.RFC3339, a)
	tb, err2 := time.Parse(time.RFC3339, b)
	return err1 == nil && err2 == nil && !ta.Before(tb.Truncate(time.Second))
}

func cdslSince(at string, now time.Time) time.Duration {
	t, err := time.Parse(time.RFC3339, at)
	if err != nil {
		return 0
	}
	return now.Sub(t)
}

// cdslNetFailure says whether an error was about reaching CDSL (worth
// trying again) rather than about the request.
func cdslNetFailure(e string) bool {
	e = strings.ToLower(e)
	return strings.Contains(e, "could not reach") || strings.Contains(e, "did not answer in time") || strings.Contains(e, "try again later") || strings.Contains(e, "cut off")
}
