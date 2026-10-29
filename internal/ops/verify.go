package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"

	"ledgerline/internal/docs"
)

// ---- live bank account and PAN checks ----
//
// The firm's verification provider, set in Connections → "Bank account and
// PAN checks", answers two questions while a client is taken on or changes
// their bank account: is this bank account open, and in whose name (a penny
// drop); and is this PAN valid, in whose name, and linked to Aadhaar.
// Cashfree's verification API is built in; the others in the list are kept
// as a record of the firm's account until an adapter is written for them.
//
// A provider that cannot be reached never fails an applicant: the check is
// left for a checker ("Check manually"). Only the provider saying the
// account or PAN is invalid fails it.

const connVerify = "pennydrop" // kept from when this was the penny drop only

// Verifier checks bank accounts and PANs with the firm's provider.
type Verifier interface {
	Name() string
	Bank(ctx context.Context, account, ifsc, name string) AppCheck
	PAN(ctx context.Context, pan, name string) AppCheck
}

// verifyHTTP is replaced in tests.
var verifyHTTP = &http.Client{Timeout: 30 * time.Second}

// Provider addresses, from their API references:
// Cashfree Secure ID (verification suite):
// https://www.cashfree.com/docs/api-reference/vrs/v2/bav-v2/bank-account-verification-sync-v2
// https://www.cashfree.com/docs/api-reference/vrs/v2/pan/verify-pan-sync
var verifyBases = map[string][2]string{ // provider -> live, sandbox
	"Cashfree": {"https://api.cashfree.com/verification", "https://sandbox.cashfree.com/verification"},
}

const verifySandbox = "Sandbox (test)"

func verifyBase(c Connection) string {
	if u := strings.TrimRight(c.Values["url"], "/"); u != "" {
		return u
	}
	b := verifyBases[c.Values["provider"]]
	if c.Values["mode"] == verifySandbox {
		return b[1]
	}
	return b[0]
}

// checkVerifyConn checks the fields a built-in provider needs when the
// connection is saved.
func checkVerifyConn(c Connection) error {
	switch c.Values["provider"] {
	case "Cashfree":
		if c.Values["client_id"] == "" {
			return invalid("enter Cashfree's client ID (the client secret goes in the secret field)")
		}
	}
	return nil
}

// verifier is the firm's connected provider, or nil when none is usable.
func (o *Ops) verifier(ctx context.Context, tenant string) Verifier {
	c, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connVerify)
	if err != nil || c.Status == "Not set" || c.Values["client_id"] == "" || c.Secrets["api_key"] == "" {
		return nil
	}
	base := verifyBase(c)
	switch c.Values["provider"] {
	case "Cashfree":
		return &cashfreeVerify{base: base, id: c.Values["client_id"], secret: c.Secrets["api_key"]}
	}
	return nil
}

// checkPAN runs the PAN check with the connected provider, else the KYC
// provider (or a manual check).
func (o *Ops) checkPAN(ctx context.Context, tenant string, p KYCProvider, pan, name, dob string) AppCheck {
	if v := o.verifier(ctx, tenant); v != nil {
		c := v.PAN(ctx, pan, name)
		o.log(ctx, tenant, SystemActor, "verify.pan", maskPAN(pan), map[string]any{"provider": v.Name(), "status": c.Status})
		return c
	}
	return p.PAN(pan, name, dob)
}

// checkBank runs the bank account check the same way.
func (o *Ops) checkBank(ctx context.Context, tenant string, p KYCProvider, account, ifsc, name string) AppCheck {
	if v := o.verifier(ctx, tenant); v != nil {
		c := v.Bank(ctx, account, ifsc, name)
		o.log(ctx, tenant, SystemActor, "verify.bank", MaskAccount(account), map[string]any{"provider": v.Name(), "ifsc": ifsc, "status": c.Status})
		return c
	}
	if p == nil {
		return manualKYC{}.PennyDrop(account, ifsc, name)
	}
	return p.PennyDrop(account, ifsc, name)
}

// kycName is the provider name shown to applicants.
func (o *Ops) kycName(ctx context.Context, tenant string) string {
	n := o.kyc().Name()
	if v := o.verifier(ctx, tenant); v != nil {
		if n == "Not connected" {
			return v.Name()
		}
		return n + " and " + v.Name()
	}
	return n
}

func maskPAN(p string) string {
	if len(p) != 10 {
		return "••••"
	}
	return p[:3] + "••••" + p[7:]
}

// verifyPost sends a JSON request and reads the JSON answer. A non-2xx
// answer is returned with its status and body for the caller to read.
func verifyPost(ctx context.Context, url string, hdr map[string]string, body any, out any) (int, error) {
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := verifyHTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if out != nil && len(rb) > 0 {
		_ = json.Unmarshal(rb, out)
	}
	return resp.StatusCode, nil
}

func unreachable(name, who string, status int, err error, msg string) AppCheck {
	why := msg
	switch {
	case err != nil:
		why = "no answer"
	case status == 401 || status == 403:
		why = "the provider refused the keys; check Settings → Connections"
	case why == "":
		why = fmt.Sprintf("answer %d", status)
	}
	return AppCheck{Name: name, Status: "Check manually", Detail: fmt.Sprintf("Could not be checked with %s just now (%s); verify from the documents.", who, why), Provider: who}
}

// ---- name comparison (for providers that return only the name) ----

func nameTokens(s string) []string {
	f := strings.FieldsFunc(strings.ToUpper(s), func(r rune) bool { return !unicode.IsLetter(r) })
	out := f[:0]
	for _, t := range f {
		switch t {
		case "MR", "MRS", "MS", "DR", "SHRI", "SMT", "KUMARI":
			continue
		}
		out = append(out, t)
	}
	return out
}

// namesMatch says whether two names are the same person's name as banks and
// the income-tax records write them: every word of the shorter name is in
// the longer one, a single letter standing for an initial.
func namesMatch(a, b string) bool {
	x, y := nameTokens(a), nameTokens(b)
	if len(x) == 0 || len(y) == 0 {
		return false
	}
	if len(x) > len(y) {
		x, y = y, x
	}
	for _, t := range x {
		found := false
		for _, u := range y {
			if t == u || (len(t) == 1 && strings.HasPrefix(u, t)) || (len(u) == 1 && strings.HasPrefix(t, u)) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// ---- Cashfree ----

type cashfreeVerify struct{ base, id, secret string }

func (c *cashfreeVerify) Name() string { return "Cashfree" }

func (c *cashfreeVerify) hdr() map[string]string {
	return map[string]string{"x-client-id": c.id, "x-client-secret": c.secret}
}

var cashfreeGoodName = map[string]bool{"DIRECT_MATCH": true, "GOOD_PARTIAL_MATCH": true}

var cashfreeBankFail = map[string]string{
	"INVALID_ACCOUNT_FAIL": "the account number was not found at this bank",
	"ACCOUNT_BLOCKED":      "the account is blocked or frozen",
	"INVALID_IFSC_FAIL":    "the IFSC is not valid",
	"NRE_ACCOUNT_FAIL":     "this is an NRE account",
}

func (c *cashfreeVerify) Bank(ctx context.Context, account, ifsc, name string) AppCheck {
	var r struct {
		Status     string `json:"account_status"`
		Code       string `json:"account_status_code"`
		NameAtBank string `json:"name_at_bank"`
		BankName   string `json:"bank_name"`
		Match      string `json:"name_match_result"`
		Score      any    `json:"name_match_score"`
		Ref        any    `json:"reference_id"`
		Message    string `json:"message"`
	}
	st, err := verifyPost(ctx, c.base+"/bank-account/sync", c.hdr(), map[string]string{"bank_account": account, "ifsc": ifsc, "name": name}, &r)
	if err != nil || st != 200 {
		return unreachable("Bank account", "Cashfree", st, err, r.Message)
	}
	ck := AppCheck{Name: "Bank account", Provider: "Cashfree"}
	if r.Status != "VALID" {
		why := cashfreeBankFail[r.Code]
		if why == "" {
			why = "the bank did not confirm the account (" + firstOf(r.Code, r.Status) + ")"
		}
		ck.Status, ck.Detail = "Failed", "The bank account could not be confirmed: "+why+"."
		return ck
	}
	at := ""
	if r.BankName != "" {
		at = " at " + r.BankName
	}
	if cashfreeGoodName[r.Match] {
		ck.Status, ck.Detail = "Passed", fmt.Sprintf("Account active%s; name at bank %s (%s, score %v; reference %v)", at, r.NameAtBank, strings.ToLower(strings.ReplaceAll(r.Match, "_", " ")), r.Score, r.Ref)
		return ck
	}
	ck.Status, ck.Detail = "Check manually", fmt.Sprintf("Account active%s, but the name at bank %q does not match %q well (%s); confirm from the bank proof that the account is the applicant's own.", at, r.NameAtBank, name, firstOf(r.Match, "no name match result"))
	return ck
}

func (c *cashfreeVerify) PAN(ctx context.Context, pan, name string) AppCheck {
	var r struct {
		Valid      bool   `json:"valid"`
		Registered string `json:"registered_name"`
		Match      string `json:"name_match_result"`
		Seeding    string `json:"aadhaar_seeding_status"`
		PANStatus  string `json:"pan_status"`
		Desc       string `json:"pan_status_desc"`
		Message    string `json:"message"`
		Type       string `json:"type"`
	}
	h := c.hdr()
	h["x-api-version"] = "2022-10-26" // a version after 2022-09-12 returns the Aadhaar link status
	st, err := verifyPost(ctx, c.base+"/pan", h, map[string]string{"pan": pan, "name": name}, &r)
	if err != nil || st != 200 {
		return unreachable("PAN", "Cashfree", st, err, r.Message)
	}
	return panResult("Cashfree", name, panAnswer{
		valid: r.Valid && (r.PANStatus == "" || r.PANStatus == "VALID"), status: firstOf(r.Desc, firstOf(r.PANStatus, r.Message)),
		registered: r.Registered, match: cashfreeGoodName[r.Match], matchKnown: r.Match != "" && r.Match != "-",
		linked: r.Seeding == "Y", notLinked: r.Seeding == "R",
	})
}

// panAnswer is a provider's answer in common terms.
type panAnswer struct {
	valid              bool
	status, registered string
	match, matchKnown  bool
	linked, notLinked  bool
}

func panResult(who, name string, a panAnswer) AppCheck {
	ck := AppCheck{Name: "PAN", Provider: who}
	if !a.valid {
		ck.Status, ck.Detail = "Failed", "This PAN is not valid in the income-tax records ("+firstOf(a.status, "invalid")+")."
		return ck
	}
	if !a.matchKnown {
		a.match = namesMatch(a.registered, name)
	}
	link := ""
	switch {
	case a.linked:
		link = "; Aadhaar linked"
	case a.notLinked:
		ck.Status, ck.Detail = "Check manually", "PAN valid, name as per PAN "+a.registered+", but the PAN is not linked to Aadhaar: an inoperative PAN cannot complete KYC until the client links it."
		return ck
	}
	if !a.match {
		ck.Status, ck.Detail = "Check manually", fmt.Sprintf("PAN valid, but the name as per PAN %q does not match %q; confirm from the PAN card.", a.registered, name)
		return ck
	}
	ck.Status, ck.Detail = "Passed", "PAN valid; name as per PAN "+a.registered+link
	return ck
}
