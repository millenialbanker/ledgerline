package ops

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/xfile"
)

// ---- BSE UCC REST API ----
//
// Registers and modifies clients' UCC at BSE without the UCC portal. Built
// from "BSE UCC REST API v0.2.2" (July 2024) and BSE's notice of 23 January
// 2026, "Revised password encryption process for BSE UCC REST API":
//
//   - POST <base>/AddUCCData_V5 (add or modify), /ResetPassword and
//     /PANStatus; live https://ucc.bseindia.com/UCC_REST_API_SERVICE/UCCService.svc,
//     UAT https://uat.bseindia.in/UCC_REST_API_SERVICE/UCCService.svc.
//   - Headers (case-sensitive): MemberCode, UserID, Password, Checksum,
//     Content-Type application/json. The body is one line of JSON.
//   - Checksum: SHA-256 of the body as hex text, RSA-encrypted with the
//     public key BSE gives the member (2048 bits; PKCS#1 v1.5, as the
//     specification's C# RSACryptoServiceProvider.Encrypt(data, false) and
//     Java Cipher "RSA" do), base64.
//   - Password: from the notice, the MD5 of the password, then the SHA-512
//     of that MD5 value, in the header; mandatory in production from the
//     end of March 2026, with no fallback. The notice does not say how the
//     digests are written; Ledgerline writes both as lowercase hex by
//     default (the MD5 hex text is what is hashed by SHA-512), with
//     uppercase hex or base64 as connection options. The password expires
//     every 30 days; the Admin resets it here.
//   - The ADD UCC answer is {"CLIENTCODE","STATUS":"S"|"R","ERRORRESPONSE"}.
//   - PAN STATUS takes a UCC create or modify date, a time window and up to
//     200 characters of client codes; at most one request every two
//     minutes and 2,500 a day.
//
// Choices where the specification is not explicit (the safe reading):
//   - One client per ADD UCC request: the sample answer is one object and
//     the answer to a request of several clients is not specified, so a
//     result could not be tied to its client safely. A list answer is
//     still read, by client code.
//   - The checksum's hex is lowercase by default (the specification's
//     worked example and Java sample); the C# sample writes uppercase, so
//     uppercase is a connection option.
//   - Reset password sends the old and new passwords as given (the
//     specification's sample); the 2026 notice speaks only of the header.
//   - A request that reached BSE without a readable answer leaves the
//     client "Sent" and is sent again on the next run; "already exists" to
//     an Add turns it into a Modify.
//   - After BSE refuses the login or the checksum, the automatic runs stop
//     (the connection shows Problem) until the Admin saves it again, so a
//     wrong password cannot lock the user.
//   - Clients already Active when Ledgerline first sees them are taken as
//     registered at BSE before (baseline); their later changes go as
//     Modify.

const (
	connBSEUCC   = "bse-ucc-api"
	cBSEUCCReg   = "uccreg"      // shared with the NSE UCI API: key "BSE|<UCC>"
	cBSEUCCRun   = "bseuccrun"   // one per run: what was sent and answered
	cBSEUCCState = "bseuccstate" // one document: timings
	bseUCCLive   = "https://ucc.bseindia.com/UCC_REST_API_SERVICE/UCCService.svc"
	bseUCCUAT    = "https://uat.bseindia.in/UCC_REST_API_SERVICE/UCCService.svc"
	bseUCCAdd    = "AddUCCData_V5"
	bseUCCPerRun = 200 // clients sent in one run

	bseHexLower = "hex lower"
	bseHexUpper = "hex upper"
	bseBase64   = "base64"
	bseMD5SHA   = "MD5 then SHA-512"
	bseMD5Only  = "MD5 only (before April 2026)"
)

// BSE UCC registration statuses.
const (
	UCCNotSent     = "Not sent"
	UCCSent        = "Sent"
	UCCAccepted    = "Accepted"
	UCCRejected    = "Rejected"
	UCCNeedsFixing = "Needs fixing"
)

func init() {
	ConnProviders = append(ConnProviders, ConnProvider{ID: connBSEUCC, Name: "BSE UCC API", Group: "Exchanges", Test: "https",
		Purpose: "Registers new clients and sends client changes, segment and inactivity changes to BSE's UCC database (UCC REST API), and reads BSE's PAN verification. Create a separate API user on BSE's UCC portal and ask BSE for the member's RSA public key; BSE may need the server's address listed",
		Fields: []ConnField{{Key: "member", Label: "Member code"}, {Key: "user", Label: "API user ID"},
			{Key: "password", Label: "API user's password (expires every 30 days)", Secret: true},
			{Key: "public_key", Label: "BSE's public key (PEM, base64 or the XML BSE gives)", Secret: true},
			{Key: "env", Label: "Environment", Options: []string{"Live", "UAT"}},
			{Key: "send", Label: "Send to BSE automatically (every 15 minutes, 08:00 to 22:00)", Options: []string{"On", "Off"}},
			{Key: "pw_hash", Label: "Password hashing", Options: []string{bseMD5SHA, bseMD5Only}, Optional: true},
			{Key: "hash_encoding", Label: "How the password digests are written", Options: []string{bseHexLower, bseHexUpper, bseBase64}, Optional: true},
			{Key: "checksum_case", Label: "Checksum SHA-256 letters", Options: []string{"lower", "upper"}, Optional: true},
			{Key: "url", Label: "Address (leave empty for BSE's)", Optional: true, Placeholder: bseUCCLive}, fNotes}})
}

// BSEUCCReg is one client's registration at BSE (key "BSE|<UCC>").
type BSEUCCReg struct {
	Exchange     string            `json:"exchange"`
	UCC          string            `json:"ucc"`
	Name         string            `json:"name"`
	Action       string            `json:"action"` // Add, Modify
	Status       string            `json:"status"` // Not sent, Sent, Accepted, Rejected, Needs fixing
	Reason       string            `json:"reason,omitempty"`
	Problems     []string          `json:"problems,omitempty"`
	Ref          string            `json:"ref,omitempty"`
	Via          string            `json:"via,omitempty"` // API, File, Before Ledgerline
	SentAt       string            `json:"sent_at,omitempty"`
	ResultAt     string            `json:"result_at,omitempty"`
	Hash         string            `json:"payload_hash,omitempty"`  // of the record last sent
	AcceptedHash string            `json:"accepted_hash,omitempty"` // of the record BSE holds
	Summary      map[string]string `json:"summary,omitempty"`       // of the record BSE holds
	PANStatus    string            `json:"pan_status,omitempty"`    // Valid, Invalid, Pending, Not found
	PANName      string            `json:"pan_name,omitempty"`
	AadhaarSeed  string            `json:"aadhaar_seed,omitempty"`
	PANCheckedOn string            `json:"pan_checked_on,omitempty"`
	History      []BSEUCCEvent     `json:"history,omitempty"`
}

// BSEUCCEvent is one step in a registration's history.
type BSEUCCEvent struct {
	At     string `json:"at"`
	By     string `json:"by"`
	Action string `json:"action"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
	Ref    string `json:"ref,omitempty"`
	Hash   string `json:"payload_hash,omitempty"`
}

func (r *BSEUCCReg) event(at, by, status, reason, ref string) {
	r.Status, r.Reason = status, reason
	r.History = append(r.History, BSEUCCEvent{At: at, By: by, Action: r.Action, Status: status, Reason: reason, Ref: ref, Hash: r.Hash})
	if len(r.History) > 50 {
		r.History = r.History[len(r.History)-50:]
	}
}

// BSEUCCCall is one request and answer, as audited: Aadhaar and account
// numbers masked; no password or checksum.
type BSEUCCCall struct {
	Function string `json:"function"`
	UCC      string `json:"ucc,omitempty"`
	Request  string `json:"request"`
	Code     int    `json:"http_status"`
	Response string `json:"response"`
	Error    string `json:"error,omitempty"`
}

// BSEUCCRun is one run.
type BSEUCCRun struct {
	ID       string       `json:"id"`
	At       string       `json:"at"`
	By       string       `json:"by"`
	Kind     string       `json:"kind"` // send, pan-status, reset-password
	Sent     int          `json:"sent"`
	Accepted int          `json:"accepted"`
	Rejected int          `json:"rejected"`
	Note     string       `json:"note"`
	Calls    []BSEUCCCall `json:"calls,omitempty"`
}

type bseUCCStateDoc struct {
	LastSend      string `json:"last_send,omitempty"`
	LastPAN       string `json:"last_pan,omitempty"`
	PANDay        string `json:"pan_day,omitempty"`
	PANToday      int    `json:"pan_today,omitempty"`
	PasswordSetOn string `json:"password_set_on,omitempty"`
}

// BSEUCCResult is what a send or PAN status run did.
type BSEUCCResult struct {
	Message  string   `json:"message"`
	Sent     int      `json:"sent"`
	Accepted int      `json:"accepted"`
	Rejected int      `json:"rejected"`
	Problems []string `json:"problems"`
}

type bseUCCConn struct {
	base, member, user, password string
	key                          *rsa.PublicKey
	pwHash, enc                  string
	upperSum, send, live         bool
}

func parseBSEUCCConn(c Connection) (bseUCCConn, error) {
	x := bseUCCConn{member: strings.TrimSpace(c.Values["member"]), user: strings.TrimSpace(c.Values["user"]), password: c.Secrets["password"],
		pwHash: firstOf(c.Values["pw_hash"], bseMD5SHA), enc: firstOf(c.Values["hash_encoding"], bseHexLower), upperSum: c.Values["checksum_case"] == "upper", send: c.Values["send"] == "On"}
	if x.member == "" || x.user == "" || x.password == "" || c.Secrets["public_key"] == "" {
		return x, invalid("the BSE UCC API connection needs the member code, API user ID, password and BSE's public key")
	}
	k, err := parseBSEPublicKey(c.Secrets["public_key"])
	if err != nil {
		return x, err
	}
	x.key = k
	x.base = bseUCCLive
	if c.Values["env"] == "UAT" {
		x.base = bseUCCUAT
	}
	if u := strings.TrimRight(strings.TrimSpace(c.Values["url"]), "/"); u != "" {
		x.base = u
	}
	x.live = x.base == bseUCCLive
	return x, nil
}

// parseBSEPublicKey reads the RSA public key as BSE may give it: PEM
// (PUBLIC KEY or RSA PUBLIC KEY), the base64 of the DER (what the Java
// sample decodes), or .NET's <RSAKeyValue> XML (what the C# sample uses).
func parseBSEPublicKey(s string) (*rsa.PublicKey, error) {
	s = strings.TrimSpace(s)
	bad := invalid("BSE's public key is not a PEM, base64 or <RSAKeyValue> RSA public key")
	var der []byte
	switch {
	case strings.Contains(s, "<RSAKeyValue"):
		var x struct {
			Modulus  string `xml:"Modulus"`
			Exponent string `xml:"Exponent"`
		}
		if xml.Unmarshal([]byte(s), &x) != nil {
			return nil, bad
		}
		n, err1 := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(x.Modulus), ""))
		e, err2 := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(x.Exponent), ""))
		if err1 != nil || err2 != nil || len(n) == 0 || len(e) == 0 || len(e) > 4 {
			return nil, bad
		}
		k := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
		return checkBSEKey(k, bad)
	case strings.Contains(s, "-----BEGIN"):
		b, _ := pem.Decode([]byte(s))
		if b == nil {
			return nil, bad
		}
		der = b.Bytes
	default:
		d, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(s), ""))
		if err != nil {
			return nil, bad
		}
		der = d
	}
	if k, err := x509.ParsePKIXPublicKey(der); err == nil {
		if rk, ok := k.(*rsa.PublicKey); ok {
			return checkBSEKey(rk, bad)
		}
		return nil, bad
	}
	if k, err := x509.ParsePKCS1PublicKey(der); err == nil {
		return checkBSEKey(k, bad)
	}
	return nil, bad
}

func checkBSEKey(k *rsa.PublicKey, bad error) (*rsa.PublicKey, error) {
	if k.N.BitLen() < 1024 || k.E < 3 {
		return nil, bad
	}
	return k, nil
}

func bseEncode(b []byte, enc string) string {
	switch enc {
	case bseHexUpper:
		return strings.ToUpper(hex.EncodeToString(b))
	case bseBase64:
		return base64.StdEncoding.EncodeToString(b)
	}
	return hex.EncodeToString(b)
}

// bseUCCPassword is the Password header: MD5 of the password, written out,
// then SHA-512 of that text, written out (BSE notice of 23 Jan 2026), or
// the MD5 alone under the earlier rule. Computed at each call from the
// stored password; never stored.
func bseUCCPassword(pw, scheme, enc string) string {
	m := md5.Sum([]byte(pw))
	s := bseEncode(m[:], enc)
	if scheme == bseMD5Only {
		return s
	}
	h := sha512.Sum512([]byte(s))
	return bseEncode(h[:], enc)
}

// bseUCCChecksum is the Checksum header for a body.
func bseUCCChecksum(k *rsa.PublicKey, body []byte, upper bool) (string, error) {
	sum := sha256.Sum256(body)
	h := hex.EncodeToString(sum[:])
	if upper {
		h = strings.ToUpper(h)
	}
	ct, err := rsa.EncryptPKCS1v15(rand.Reader, k, []byte(h))
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(ct), nil
}

// bseUCCHTTP talks to BSE; tests replace it.
var bseUCCHTTP = &http.Client{Timeout: 60 * time.Second}

// One run at a time across the server.
var bseUCCMu sync.Mutex

func (x bseUCCConn) request(ctx context.Context, fn string, body []byte, password string) (*http.Request, error) {
	sum, err := bseUCCChecksum(x.key, body, x.upperSum)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, x.base+"/"+fn, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	// BSE's header names are case-sensitive: set as written, not canonicalised.
	req.Header["Content-Type"] = []string{"application/json"}
	req.Header["MemberCode"] = []string{x.member}
	req.Header["UserID"] = []string{x.user}
	req.Header["Password"] = []string{bseUCCPassword(password, x.pwHash, x.enc)}
	req.Header["Checksum"] = []string{sum}
	return req, nil
}

func (x bseUCCConn) post(ctx context.Context, fn string, body []byte) (int, []byte, error) {
	return x.postAs(ctx, fn, body, x.password)
}

func (x bseUCCConn) postAs(ctx context.Context, fn string, body []byte, password string) (int, []byte, error) {
	req, err := x.request(ctx, fn, body, password)
	if err != nil {
		return 0, nil, err
	}
	resp, err := bseUCCHTTP.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("could not reach BSE's UCC API: %v", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return resp.StatusCode, b, err
}

// bseUCCAnswer is one client's answer.
type bseUCCAnswer struct {
	ClientCode string `json:"CLIENTCODE"`
	Status     string `json:"STATUS"`
	Error      string `json:"ERRORRESPONSE"`
}

// bseUnwrap reads a WCF answer that may be a JSON string holding JSON.
func bseUnwrap(b []byte) []byte {
	b = bytes.TrimSpace(b)
	if len(b) > 0 && b[0] == '"' {
		var s string
		if json.Unmarshal(b, &s) == nil {
			return []byte(s)
		}
	}
	return b
}

// bseUCCAnswers reads the ADD UCC (or reset password) answer: one object,
// a list, or an object holding a list.
func bseUCCAnswers(b []byte) []bseUCCAnswer {
	b = bseUnwrap(b)
	var one bseUCCAnswer
	if json.Unmarshal(b, &one) == nil && one.Status != "" {
		return []bseUCCAnswer{one}
	}
	var list []bseUCCAnswer
	if json.Unmarshal(b, &list) == nil && len(list) > 0 {
		return list
	}
	var wrap map[string]json.RawMessage
	if json.Unmarshal(b, &wrap) == nil {
		keys := sortedKeys(wrap)
		for _, k := range keys {
			if json.Unmarshal(wrap[k], &list) == nil && len(list) > 0 && list[0].Status != "" {
				return list
			}
		}
	}
	return nil
}

// bseUCCErrors turns BSE's messages into English. The specification lists
// only its common errors; the rest are matched on the words in them, and
// BSE's own message is always kept alongside.
var bseUCCErrors = []struct {
	rx   *regexp.Regexp
	text string
	auth bool
}{
	{regexp.MustCompile(`(?i)invalid\s*(user\s*id|userid|user\s*name|username|user)\s*(/|or)?\s*password|password.*(not|in)valid`), "BSE did not accept the member code, API user ID or password: check them in Connections; the password expires every 30 days and the Admin resets it on the BSE UCC screen", true},
	{regexp.MustCompile(`(?i)password.*expire|expire.*password`), "The API user's password has expired: the Admin resets it on the BSE UCC screen", true},
	{regexp.MustCompile(`(?i)request data is invalid`), "BSE could not match the checksum to the request: check BSE's public key and the checksum letters in Connections", true},
	{regexp.MustCompile(`(?i)error\s*code\s*:?\s*203`), "BSE could not decrypt the checksum: the public key in Connections is probably not BSE's key for this member", true},
	{regexp.MustCompile(`(?i)within two minutes`), "BSE allows one PAN status request every two minutes", false},
	{regexp.MustCompile(`(?i)query day limit`), "BSE's limit of 2,500 PAN status requests a day is reached; Ledgerline asks again tomorrow", false},
	{regexp.MustCompile(`(?i)exceeds the limit`), "More than 10,000 records in the time asked: Ledgerline asks for a shorter time", false},
	{regexp.MustCompile(`(?i)already\s*exist`), "The client code is already registered at BSE: Ledgerline sends it as a modification", false},
	{regexp.MustCompile(`(?i)not\s*(exist|found)|does\s*not\s*exist`), "The client code is not registered at BSE: Ledgerline sends it as a new client", false},
	{regexp.MustCompile(`(?i)pan.*name|name.*pan`), "BSE says the name does not match the PAN: correct the name exactly as on the PAN (change request), then it is sent again", false},
	{regexp.MustCompile(`(?i)invalid\s*pan|pan.*invalid|pan.*not\s*valid`), "BSE says the PAN is not valid: check it against the PAN card", false},
	{regexp.MustCompile(`(?i)aadhaar|aadhar|seed`), "BSE says the PAN is not linked with Aadhaar: the client links them on the Income Tax site", false},
	{regexp.MustCompile(`(?i)date\s*of\s*birth|\bdob\b`), "BSE did not accept the date of birth: it must match the PAN", false},
	{regexp.MustCompile(`(?i)mobile`), "BSE did not accept the mobile number (10 digits; one client per number unless family is declared)", false},
	{regexp.MustCompile(`(?i)e-?mail`), "BSE did not accept the email address (one client per address unless family is declared)", false},
	{regexp.MustCompile(`(?i)ifsc`), "BSE did not accept the IFSC", false},
	{regexp.MustCompile(`(?i)bank|account`), "BSE did not accept the bank account details", false},
	{regexp.MustCompile(`(?i)depositor|demat|beneficial|\bdp\b`), "BSE did not accept the demat account details", false},
	{regexp.MustCompile(`(?i)state|city|pin\s*code|pincode`), "BSE did not accept the state, city or PIN code: they must be as in BSE's state-city master", false},
	{regexp.MustCompile(`(?i)income|net\s*worth`), "BSE did not accept the income details (at most a year old)", false},
	{regexp.MustCompile(`(?i)segment`), "BSE did not accept the segments", false},
	{regexp.MustCompile(`(?i)category|status`), "BSE did not accept the client's category or status", false},
	{regexp.MustCompile(`(?i)kra`), "BSE says the KRA status is not validated: see the client's KRA status", false},
}

// BSEUCCError explains a BSE message in English, and whether it is a
// refusal of the login or checksum.
func BSEUCCError(msg string) (string, bool) {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return "BSE gave no reason", false
	}
	for _, e := range bseUCCErrors {
		if e.rx.MatchString(msg) {
			return e.text + " (BSE: " + msg + ")", e.auth
		}
	}
	return "BSE rejected it: " + msg, false
}

func (o *Ops) bseUCCConnection(ctx context.Context, tenant string) (Connection, bseUCCConn, error) {
	cn, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connBSEUCC)
	if err != nil || cn.Status == "Not set" {
		return cn, bseUCCConn{}, fmt.Errorf("%w: set up the BSE UCC API in Settings → Connections", ErrState)
	}
	x, err := parseBSEUCCConn(cn)
	return cn, x, err
}

// bseUCCConnMark records how the connection answered.
func (o *Ops) bseUCCConnMark(ctx context.Context, tenant, status, problem string) {
	_, _ = docs.Update(ctx, o.Docs, tenant, cConn, connBSEUCC, SystemActor, func(c *Connection) error {
		if c.Status == "Not set" {
			return docs.ErrStale
		}
		c.Status, c.Problem, c.TestedAt, c.TestedBy = status, problem, o.now().Format(time.RFC3339), SystemActor
		return nil
	})
}

// bseUCCItem is one client and what to do about it at BSE.
type bseUCCItem struct {
	c       Client
	reg     BSEUCCReg
	exists  bool
	rec     bseUCCRecord
	hash    string
	pending bool
}

func bseRegKey(ucc string) string { return "BSE|" + ucc }

// bseUCCItems maps every client that is or should be registered at BSE.
// Active clients with no registration are left out (baseline: registered
// before Ledgerline).
func (o *Ops) bseUCCItems(ctx context.Context, tenant string) ([]bseUCCItem, []Client, error) {
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return nil, nil, err
	}
	cs, err := o.Clients(ctx, tenant)
	if err != nil {
		return nil, nil, err
	}
	regs, err := docs.List[BSEUCCReg](ctx, o.Docs, tenant, cBSEUCCReg, "BSE|")
	if err != nil {
		return nil, nil, err
	}
	byUCC := map[string]BSEUCCReg{}
	for _, r := range regs {
		byUCC[r.UCC] = r
	}
	src := o.bseUCCLoadSources(ctx, tenant, set)
	now := o.now()
	var out []bseUCCItem
	var baseline []Client
	for _, c := range cs {
		r, ok := byUCC[c.UCC]
		if !ok {
			switch c.Status {
			case "UCC upload":
				r = BSEUCCReg{Exchange: "BSE", UCC: c.UCC, Action: "Add", Status: UCCNotSent}
			case "Active":
				baseline = append(baseline, c)
				continue
			default:
				continue
			}
		}
		if r.AcceptedHash == "" {
			r.Action = "Add"
		} else {
			r.Action = "Modify"
		}
		if r.Ref == "already-exists" { // BSE said an Add already exists
			r.Action = "Modify"
		}
		r.Name = c.Name
		it := bseUCCItem{c: c, reg: r, exists: ok}
		it.rec = bseUCCMap(c, src, r.Action, r.Summary, now)
		it.hash = it.rec.Hash()
		switch r.Status {
		case UCCAccepted:
			it.pending = it.hash != r.AcceptedHash
		case UCCRejected:
			it.pending = it.hash != r.Hash
		case UCCSent:
			it.pending = r.Via != "File" || it.hash != r.Hash
		default: // Not sent, Needs fixing
			it.pending = true
		}
		if c.Status == "Rejected" && r.AcceptedHash == "" {
			it.pending = false
		}
		out = append(out, it)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].c.UCC < out[j].c.UCC })
	return out, baseline, nil
}

// bseUCCBaseline records clients already active as registered before.
func (o *Ops) bseUCCBaseline(ctx context.Context, tenant string, cs []Client) {
	if len(cs) == 0 {
		return
	}
	set, _ := o.Settings(ctx, tenant)
	src := o.bseUCCLoadSources(ctx, tenant, set)
	now := o.now()
	at := now.Format(time.RFC3339)
	for _, c := range cs {
		rec := bseUCCMap(c, src, "Modify", nil, now)
		h := rec.Hash()
		r := BSEUCCReg{Exchange: "BSE", UCC: c.UCC, Name: c.Name, Action: "Modify", Via: "Before Ledgerline", Hash: h, AcceptedHash: h, Summary: rec.Summary}
		r.event(at, SystemActor, UCCAccepted, "Registered at BSE before Ledgerline sent UCC", "")
		if _, err := docs.Put(ctx, o.Docs, tenant, cBSEUCCReg, bseRegKey(c.UCC), r, 0, SystemActor); err != nil && !errors.Is(err, docs.ErrStale) {
			continue
		}
	}
	o.log(ctx, tenant, SystemActor, "bseucc.baseline", "", map[string]any{"clients": len(cs)})
}

func (o *Ops) bseUCCSaveReg(ctx context.Context, tenant string, r BSEUCCReg) {
	_ = docs.Upsert(ctx, o.Docs, tenant, cBSEUCCReg, bseRegKey(r.UCC), r, SystemActor)
}

func (o *Ops) bseUCCSaveRun(ctx context.Context, tenant, actor string, run *BSEUCCRun) {
	n, err := o.next(ctx, tenant, "bseuccrun", actor)
	if err != nil {
		return
	}
	run.ID = fmt.Sprintf("BSEUCC-%06d", n)
	if len(run.Calls) > bseUCCPerRun+5 {
		run.Calls = run.Calls[:bseUCCPerRun+5]
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cBSEUCCRun, run.ID, *run, actor)
	o.log(ctx, tenant, actor, "bseucc."+run.Kind, run.ID, map[string]any{"sent": run.Sent, "accepted": run.Accepted, "rejected": run.Rejected, "note": run.Note})
}

func trimTo(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// BSEUCCSend sends pending clients (or the ones named) to BSE.
func (o *Ops) BSEUCCSend(ctx context.Context, tenant, actor string, uccs []string) (BSEUCCResult, error) {
	res := BSEUCCResult{Problems: []string{}}
	if actor != SystemActor {
		if err := o.require(ctx, tenant, actor, "Sending UCC to BSE", RoleMaker, RoleChecker); err != nil {
			return res, err
		}
	}
	_, x, err := o.bseUCCConnection(ctx, tenant)
	if err != nil {
		return res, err
	}
	bseUCCMu.Lock()
	defer bseUCCMu.Unlock()
	items, baseline, err := o.bseUCCItems(ctx, tenant)
	if err != nil {
		return res, err
	}
	o.bseUCCBaseline(ctx, tenant, baseline)
	want := map[string]bool{}
	for _, u := range uccs {
		want[strings.ToUpper(strings.TrimSpace(u))] = true
	}
	found := map[string]bool{}
	now := o.now()
	at := now.Format(time.RFC3339)
	run := BSEUCCRun{At: at, By: actor, Kind: "send"}
	stopped := ""
	for _, it := range items {
		if len(want) > 0 && !want[it.c.UCC] {
			continue
		}
		found[it.c.UCC] = true
		if !it.pending {
			if len(want) > 0 {
				res.Problems = append(res.Problems, it.c.UCC+": nothing to send (BSE has these details)")
			}
			continue
		}
		r := it.reg
		if len(it.rec.Problems) > 0 {
			r.Problems = it.rec.Problems
			if r.Status != UCCNeedsFixing || r.Reason != it.rec.Problems[0] {
				r.event(at, actor, UCCNeedsFixing, it.rec.Problems[0], "")
			}
			o.bseUCCSaveReg(ctx, tenant, r)
			res.Problems = append(res.Problems, it.c.UCC+": "+strings.Join(it.rec.Problems, "; "))
			continue
		}
		if stopped != "" || run.Sent >= bseUCCPerRun {
			continue
		}
		r.Problems = nil
		r.Hash, r.SentAt, r.Via = it.hash, at, "API"
		body := it.rec.JSON()
		code, b, err := x.post(ctx, bseUCCAdd, body)
		call := BSEUCCCall{Function: bseUCCAdd, UCC: it.c.UCC, Request: string(it.rec.Masked().JSON()), Code: code, Response: trimTo(string(b), 2000)}
		run.Sent++
		var ans *bseUCCAnswer
		for _, a := range bseUCCAnswers(b) {
			if strings.EqualFold(strings.TrimSpace(a.ClientCode), it.c.UCC) || strings.TrimSpace(a.ClientCode) == "" {
				a := a
				ans = &a
				break
			}
		}
		switch {
		case err != nil:
			call.Error = err.Error()
			r.event(at, actor, UCCSent, "No answer from BSE: "+err.Error()+"; sent again on the next run", "")
			stopped = err.Error()
		case ans == nil:
			r.event(at, actor, UCCSent, fmt.Sprintf("BSE's answer (%d) could not be read; sent again on the next run", code), "")
			if code == 401 || code == 403 {
				stopped = fmt.Sprintf("BSE refused the request (%d)", code)
			}
		case strings.EqualFold(strings.TrimSpace(ans.Status), "S"):
			r.ResultAt, r.AcceptedHash, r.Summary, r.Ref = at, it.hash, it.rec.Summary, "BSE "+now.In(ist).Format("02/01/2006 15:04")
			r.PANStatus, r.PANCheckedOn = "", ""
			r.event(at, actor, UCCAccepted, "", r.Ref)
			run.Accepted++
		default:
			text, auth := BSEUCCError(ans.Error)
			r.ResultAt = at
			if auth {
				// Not this client's fault: leave it to go again.
				r.event(at, actor, UCCNotSent, text, "")
				stopped = text
				run.Sent--
				break
			}
			ref := ""
			if regexp.MustCompile(`(?i)already\s*exist`).MatchString(ans.Error) && r.Action == "Add" {
				ref = "already-exists" // the next run sends a Modify
				r.Hash = ""
			}
			r.Ref = ref
			r.event(at, actor, UCCRejected, text, ref)
			run.Rejected++
			res.Problems = append(res.Problems, it.c.UCC+": "+text)
		}
		run.Calls = append(run.Calls, call)
		o.bseUCCSaveReg(ctx, tenant, r)
	}
	for u := range want {
		if !found[u] {
			res.Problems = append(res.Problems, u+": not a client to register at BSE (only clients at UCC upload, or registered before)")
		}
	}
	if stopped != "" {
		res.Problems = append(res.Problems, "Stopped: "+stopped)
		if _, auth := BSEUCCError(stopped); auth || strings.Contains(stopped, "refused") {
			o.bseUCCConnMark(ctx, tenant, "Problem", stopped)
		}
	} else if run.Sent > 0 {
		o.bseUCCConnMark(ctx, tenant, "Working", "")
	}
	res.Sent, res.Accepted, res.Rejected = run.Sent, run.Accepted, run.Rejected
	switch {
	case run.Sent == 0 && stopped == "":
		res.Message = "Nothing to send to BSE"
		if len(res.Problems) > 0 {
			res.Message = fmt.Sprintf("Nothing sent: %d client(s) need fixing first", len(res.Problems))
		}
	default:
		res.Message = fmt.Sprintf("Sent %d to BSE: %d accepted, %d rejected", run.Sent, run.Accepted, run.Rejected)
		if stopped != "" {
			res.Message += "; stopped: " + stopped
		}
	}
	run.Note = res.Message
	if run.Sent > 0 || stopped != "" || actor != SystemActor {
		o.bseUCCSaveRun(ctx, tenant, actor, &run)
	}
	return res, nil
}

// ---- PAN status ----

type bsePANRec struct {
	ClientCode  string `json:"CLIENTCODE"`
	PAN         string `json:"PANNO"`
	CreatedOn   string `json:"CREATEDON"`
	ClientName  string `json:"CLIENTNAME"`
	PANName     string `json:"PANNAME"`
	NameOnCard  string `json:"NAME_ON_PANCARD"`
	Status      string `json:"PANSTATUS"`
	ValidatedOn string `json:"PANVALIDATIONON"`
	Seed        string `json:"AADHAAR_SEED_FLAG"`
}

type bsePANAnswer struct {
	List   []bsePANRec `json:"ResponceListPAN"`
	Errors []struct {
		Status string `json:"STATUS"`
		Error  string `json:"ERRORRESPONSE"`
	} `json:"ErrorResponceListPAN"`
}

// bseUCCPANDate is the BSE create or modify date of a registration: when
// BSE accepted it, or when the batch file was uploaded.
func bseUCCPANDate(r BSEUCCReg) string {
	t := r.ResultAt
	if r.Status == UCCSent && r.Via == "File" {
		t = r.SentAt
	}
	if r.Status != UCCAccepted && !(r.Status == UCCSent && r.Via == "File") {
		return ""
	}
	if r.Via == "Before Ledgerline" {
		return ""
	}
	d, err := time.Parse(time.RFC3339, t)
	if err != nil {
		return ""
	}
	return d.In(ist).Format("2006-01-02")
}

func bsePANName(s string) string {
	return strings.Join(strings.Fields(strings.ToUpper(regexp.MustCompile(`[^A-Za-z ]`).ReplaceAllString(s, " "))), " ")
}

// BSEUCCPANStatus asks BSE for the PAN verification of registered clients
// (the ones named, or those not yet Valid and not asked today). BSE takes
// one request every two minutes, so one run makes one request: for one
// registration date and up to 200 characters of client codes.
func (o *Ops) BSEUCCPANStatus(ctx context.Context, tenant, actor string, uccs []string) (BSEUCCResult, error) {
	res := BSEUCCResult{Problems: []string{}}
	if actor != SystemActor {
		if err := o.require(ctx, tenant, actor, "Checking PAN status at BSE", RoleMaker, RoleChecker); err != nil {
			return res, err
		}
	}
	_, x, err := o.bseUCCConnection(ctx, tenant)
	if err != nil {
		return res, err
	}
	bseUCCMu.Lock()
	defer bseUCCMu.Unlock()
	now := o.now()
	today := now.In(ist).Format("2006-01-02")
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cBSEUCCState, "state", bseUCCStateDoc{})
	if t, err := time.Parse(time.RFC3339, st.LastPAN); err == nil && now.Sub(t) < 2*time.Minute {
		return res, fmt.Errorf("%w: BSE takes one PAN status request every two minutes; ask again after %s", ErrState, t.Add(2*time.Minute).In(ist).Format("15:04:05"))
	}
	if st.PANDay == today && st.PANToday >= 2500 {
		return res, fmt.Errorf("%w: BSE's limit of 2,500 PAN status requests a day is reached", ErrState)
	}
	regs, err := docs.List[BSEUCCReg](ctx, o.Docs, tenant, cBSEUCCReg, "BSE|")
	if err != nil {
		return res, err
	}
	want := map[string]bool{}
	for _, u := range uccs {
		want[strings.ToUpper(strings.TrimSpace(u))] = true
	}
	byDate := map[string][]BSEUCCReg{}
	for _, r := range regs {
		d := bseUCCPANDate(r)
		switch {
		case len(want) > 0:
			if !want[r.UCC] {
				continue
			}
			delete(want, r.UCC)
			if d == "" {
				res.Problems = append(res.Problems, r.UCC+": not registered at BSE by Ledgerline yet, so BSE cannot be asked by date")
				continue
			}
		case d == "" || r.PANStatus == "Valid" || r.PANStatus == "Invalid" || r.PANCheckedOn == today:
			continue
		}
		byDate[d] = append(byDate[d], r)
	}
	for u := range want {
		res.Problems = append(res.Problems, u+": no BSE registration")
	}
	if len(byDate) == 0 {
		res.Message = "No PAN status to ask BSE for"
		return res, nil
	}
	dates := sortedKeys(byDate)
	day := dates[0]
	group := byDate[day]
	sort.Slice(group, func(i, j int) bool { return group[i].UCC < group[j].UCC })
	var list []string
	asked := map[string]BSEUCCReg{}
	n := 0
	for _, r := range group {
		if n+len(r.UCC)+1 > 200 {
			break
		}
		list = append(list, r.UCC)
		asked[r.UCC] = r
		n += len(r.UCC) + 1
	}
	d, _ := time.Parse("2006-01-02", day)
	// UCCList is comma-separated without spaces; TimeFrom/TimeTo cover the day.
	body, _ := json.Marshal(struct {
		CreateDate string `json:"CreateDate"`
		TimeFrom   string `json:"TimeFrom"`
		TimeTo     string `json:"TimeTo"`
		Status     string `json:"Status"`
		UCCList    string `json:"UCCList"`
		RecordType string `json:"RecordType"`
	}{d.Format("02/01/2006"), "00:00", "23:59", "", strings.Join(list, ","), ""})
	st.LastPAN = now.Format(time.RFC3339)
	if st.PANDay != today {
		st.PANDay, st.PANToday = today, 0
	}
	st.PANToday++
	_ = docs.Upsert(ctx, o.Docs, tenant, cBSEUCCState, "state", st, SystemActor)
	code, b, err := x.post(ctx, "PANStatus", body)
	at := now.Format(time.RFC3339)
	run := BSEUCCRun{At: at, By: actor, Kind: "pan-status", Sent: len(list), Calls: []BSEUCCCall{{Function: "PANStatus", Request: string(body), Code: code, Response: trimTo(string(b), 4000)}}}
	if err != nil {
		run.Calls[0].Error = err.Error()
		run.Note = err.Error()
		o.bseUCCSaveRun(ctx, tenant, actor, &run)
		return res, err
	}
	var ans bsePANAnswer
	if json.Unmarshal(bseUnwrap(b), &ans) != nil {
		run.Note = fmt.Sprintf("BSE's answer (%d) could not be read", code)
		o.bseUCCSaveRun(ctx, tenant, actor, &run)
		return res, fmt.Errorf("%w: %s", ErrState, run.Note)
	}
	for _, e := range ans.Errors {
		text, auth := BSEUCCError(e.Error)
		res.Problems = append(res.Problems, text)
		if auth {
			o.bseUCCConnMark(ctx, tenant, "Problem", text)
		}
	}
	if len(ans.Errors) > 0 && len(ans.List) == 0 {
		run.Note = strings.Join(res.Problems, "; ")
		o.bseUCCSaveRun(ctx, tenant, actor, &run)
		res.Message = "BSE did not answer the PAN status: " + run.Note
		return res, nil
	}
	o.bseUCCConnMark(ctx, tenant, "Working", "")
	got := map[string]bsePANRec{}
	for _, p := range ans.List {
		got[strings.ToUpper(strings.TrimSpace(p.ClientCode))] = p
	}
	cs, _ := o.ClientMap(ctx, tenant)
	date := today
	for _, u := range list {
		r := asked[u]
		r.PANCheckedOn = today
		p, ok := got[u]
		if !ok {
			r.PANStatus = "Not found"
			o.bseUCCSaveReg(ctx, tenant, r)
			continue
		}
		r.PANStatus = map[string]string{"V": "Valid", "I": "Invalid", "P": "Pending"}[strings.ToUpper(strings.TrimSpace(p.Status))]
		r.PANName, r.AadhaarSeed = firstOf(strings.TrimSpace(p.PANName), strings.TrimSpace(p.NameOnCard)), strings.ToUpper(strings.TrimSpace(p.Seed))
		if r.Status == UCCSent && r.Via == "File" {
			// BSE knows the client: the uploaded file was taken.
			r.AcceptedHash, r.ResultAt = r.Hash, at
			r.event(at, actor, UCCAccepted, "Found at BSE (PAN status)", r.Ref)
		}
		c := cs[u]
		switch r.PANStatus {
		case "Valid":
			res.Accepted++
			o.closeWork(ctx, tenant, SystemActor, "bseucc", "pan:"+u, "PAN valid at BSE")
		case "Invalid":
			res.Rejected++
			o.raiseWork(ctx, tenant, "bseucc", "pan:"+u, "BSE marks the PAN of "+firstOf(c.Name, u)+" invalid",
				fmt.Sprintf("%s: BSE's PAN verification says the PAN, name or date of birth does not match the Income Tax records. Check the PAN card, correct the name or date of birth by a change request, and the UCC is sent to BSE again.", u), u, RoleMaker, "clients", date)
		}
		if r.PANName != "" && c.Name != "" && bsePANName(r.PANName) != bsePANName(c.Name) {
			o.raiseWork(ctx, tenant, "bseucc", "panname:"+u, "Name of "+u+" differs from the PAN at BSE",
				fmt.Sprintf("The name on record is %q and the Income Tax name BSE has is %q. Correct the name exactly as on the PAN by a change request.", c.Name, r.PANName), u, RoleMaker, "clients", date)
		}
		if (r.AadhaarSeed == "R" || r.AadhaarSeed == "NO" || r.AadhaarSeed == "N") && (c.Type == "Individual" || c.Type == "NRI") {
			o.raiseWork(ctx, tenant, "bseucc", "seed:"+u, "PAN of "+u+" not linked with Aadhaar (BSE)",
				"BSE reports the PAN is not seeded with Aadhaar; an unlinked PAN is inoperative. Ask the client to link PAN and Aadhaar on the Income Tax site.", u, RoleMaker, "clients", date)
		}
		o.bseUCCSaveReg(ctx, tenant, r)
	}
	res.Sent = len(list)
	res.Message = fmt.Sprintf("Asked BSE for %d PAN status(es) of %s: %d valid, %d invalid", len(list), fmtDay(day), res.Accepted, res.Rejected)
	if left := len(group) - len(list) + len(dates) - 1; left > 0 {
		res.Message += "; more are asked every few minutes (BSE takes one request every two minutes)"
	}
	run.Accepted, run.Rejected, run.Note = res.Accepted, res.Rejected, res.Message
	o.bseUCCSaveRun(ctx, tenant, actor, &run)
	return res, nil
}

// ---- password ----

var bsePWSpecial = regexp.MustCompile(`[^A-Za-z0-9]`)

// BSEUCCResetPassword changes the API user's password at BSE and keeps the
// new one as the connection's secret (Admin).
func (o *Ops) BSEUCCResetPassword(ctx context.Context, tenant, actor, newPW string) (string, error) {
	if err := o.require(ctx, tenant, actor, "Resetting the BSE UCC API password", RoleAdmin); err != nil {
		return "", err
	}
	switch {
	case len(newPW) < 8 || len(newPW) > 20:
		return "", invalid("BSE's password is 8 to 20 characters")
	case !regexp.MustCompile(`[A-Z]`).MatchString(newPW) || !regexp.MustCompile(`[a-z]`).MatchString(newPW) || !regexp.MustCompile(`[0-9]`).MatchString(newPW) || !bsePWSpecial.MatchString(newPW):
		return "", invalid("BSE's password needs a capital letter, a small letter, a digit and a special character (! $ @ % …)")
	}
	_, x, err := o.bseUCCConnection(ctx, tenant)
	if err != nil {
		return "", err
	}
	if newPW == x.password {
		return "", invalid("the new password must differ from the current one")
	}
	bseUCCMu.Lock()
	defer bseUCCMu.Unlock()
	body, _ := json.Marshal(struct {
		Old string `json:"OldPassword"`
		New string `json:"NewPassword"`
	}{x.password, newPW})
	code, b, err := x.post(ctx, "ResetPassword", body)
	at := o.now().Format(time.RFC3339)
	run := BSEUCCRun{At: at, By: actor, Kind: "reset-password", Calls: []BSEUCCCall{{Function: "ResetPassword", Request: `{"OldPassword":"***","NewPassword":"***"}`, Code: code, Response: trimTo(string(b), 1000)}}}
	if err != nil {
		run.Calls[0].Error, run.Note = err.Error(), err.Error()
		o.bseUCCSaveRun(ctx, tenant, actor, &run)
		return "", err
	}
	as := bseUCCAnswers(b)
	if len(as) == 0 || !strings.EqualFold(strings.TrimSpace(as[0].Status), "S") {
		msg := fmt.Sprintf("BSE's answer (%d) could not be read", code)
		if len(as) > 0 {
			msg, _ = BSEUCCError(as[0].Error)
		}
		run.Note = "Password not changed: " + msg
		o.bseUCCSaveRun(ctx, tenant, actor, &run)
		return "", fmt.Errorf("%w: %s", ErrState, run.Note)
	}
	if _, err := docs.Update(ctx, o.Docs, tenant, cConn, connBSEUCC, actor, func(c *Connection) error {
		if c.Secrets == nil {
			c.Secrets = map[string]string{}
		}
		c.Secrets["password"] = newPW
		c.Status, c.Problem, c.UpdatedAt, c.UpdatedBy = "Working", "", at, actor
		return nil
	}); err != nil {
		// BSE has the new password and Ledgerline could not keep it: say so.
		return "", fmt.Errorf("BSE changed the password but Ledgerline could not save it (%v): enter the new password in Connections now", err)
	}
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cBSEUCCState, "state", bseUCCStateDoc{})
	st.PasswordSetOn = o.now().Format("2006-01-02")
	_ = docs.Upsert(ctx, o.Docs, tenant, cBSEUCCState, "state", st, SystemActor)
	o.closeWork(ctx, tenant, actor, "bseucc", "password", "password reset")
	run.Note = "Password changed at BSE"
	o.bseUCCSaveRun(ctx, tenant, actor, &run)
	o.log(ctx, tenant, actor, "connection.secret_rotated", connBSEUCC, map[string]any{"key": "password"}) // never the password
	return "BSE changed the API user's password; Ledgerline uses the new one from now on. It expires in 30 days.", nil
}

// ---- the screen ----

// BSEUCCClientRow is one client on the screen.
type BSEUCCClientRow struct {
	UCC       string   `json:"ucc"`
	Name      string   `json:"name"`
	PAN       string   `json:"pan"`
	Action    string   `json:"action"`
	Status    string   `json:"status"`
	Reason    string   `json:"reason"`
	Problems  []string `json:"problems"`
	PANStatus string   `json:"pan_status"`
	SentAt    string   `json:"sent_at"`
	ResultAt  string   `json:"result_at"`
	Ref       string   `json:"ref"`
}

// BSEUCCRunRow is one run on the screen.
type BSEUCCRunRow struct {
	ID       string `json:"id"`
	At       string `json:"at"`
	By       string `json:"by"`
	Sent     int    `json:"sent"`
	Accepted int    `json:"accepted"`
	Rejected int    `json:"rejected"`
	Note     string `json:"note"`
}

// BSEUCCView is GET /v1/ucc-api/bse.
type BSEUCCView struct {
	Exchange   string            `json:"exchange"`
	Connection string            `json:"connection"` // Not set, Saved, Working, Problem
	Live       bool              `json:"live"`
	Counts     map[string]int    `json:"counts"`
	Clients    []BSEUCCClientRow `json:"clients"`
	Runs       []BSEUCCRunRow    `json:"runs"`
}

// bseUCCRowStatus is what the screen shows: a registration with changes
// waiting is Not sent again; problems make it Needs fixing.
func bseUCCRowStatus(it bseUCCItem) (string, string) {
	r := it.reg
	if it.pending && len(it.rec.Problems) > 0 {
		return UCCNeedsFixing, it.rec.Problems[0]
	}
	if it.pending && (r.Status == UCCAccepted || r.Status == UCCRejected) {
		return UCCNotSent, "Changed since BSE's record: sent as a modification"
	}
	if r.Status == UCCRejected && r.Ref == "already-exists" {
		return UCCNotSent, r.Reason
	}
	return r.Status, r.Reason
}

// BSEUCCView is the screen: the connection, every client registered or to
// register, and the latest runs.
func (o *Ops) BSEUCCView(ctx context.Context, tenant, actor string) (BSEUCCView, error) {
	v := BSEUCCView{Exchange: "BSE", Connection: "Not set", Counts: map[string]int{}, Clients: []BSEUCCClientRow{}, Runs: []BSEUCCRunRow{}}
	if err := o.require(ctx, tenant, actor, "Viewing BSE UCC registrations", RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return v, err
	}
	for _, s := range []string{UCCNotSent, UCCSent, UCCAccepted, UCCRejected, UCCNeedsFixing} {
		v.Counts[s] = 0
	}
	if cn, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connBSEUCC); err == nil && cn.Status != "" && cn.Status != "Not set" {
		v.Connection = cn.Status
		if x, err := parseBSEUCCConn(cn); err != nil {
			v.Connection = "Problem"
		} else {
			v.Live = x.live
		}
	}
	items, _, err := o.bseUCCItems(ctx, tenant)
	if err != nil {
		return v, err
	}
	rank := map[string]int{UCCNeedsFixing: 0, UCCRejected: 1, UCCNotSent: 2, UCCSent: 3, UCCAccepted: 4}
	for _, it := range items {
		st, reason := bseUCCRowStatus(it)
		v.Counts[st]++
		probs := []string{}
		if it.pending {
			probs = it.rec.Problems
		}
		ref := it.reg.Ref
		if ref == "already-exists" {
			ref = ""
		}
		v.Clients = append(v.Clients, BSEUCCClientRow{UCC: it.c.UCC, Name: it.c.Name, PAN: MaskPAN(it.c.PAN), Action: it.reg.Action, Status: st, Reason: reason, Problems: probs,
			PANStatus: it.reg.PANStatus, SentAt: it.reg.SentAt, ResultAt: it.reg.ResultAt, Ref: ref})
	}
	sort.SliceStable(v.Clients, func(i, j int) bool { return rank[v.Clients[i].Status] < rank[v.Clients[j].Status] })
	if len(v.Clients) > 1000 {
		v.Clients = v.Clients[:1000]
	}
	runs, _ := docs.List[BSEUCCRun](ctx, o.Docs, tenant, cBSEUCCRun, "")
	sort.Slice(runs, func(i, j int) bool { return runs[i].ID > runs[j].ID })
	for i, r := range runs {
		if i == 30 {
			break
		}
		v.Runs = append(v.Runs, BSEUCCRunRow{ID: r.ID, At: r.At, By: r.By, Sent: r.Sent, Accepted: r.Accepted, Rejected: r.Rejected, Note: firstOf(r.Kind, "send") + ": " + r.Note})
	}
	return v, nil
}

// BSEUCCPreview is one client's record as it would go to BSE.
type BSEUCCPreview struct {
	UCC      string          `json:"ucc"`
	Action   string          `json:"action"`
	Status   string          `json:"status"`
	Pending  bool            `json:"pending"`
	Payload  json.RawMessage `json:"payload"` // account numbers masked
	Problems []string        `json:"problems"`
	Hash     string          `json:"payload_hash"`
}

// BSEUCCPreview maps one client.
func (o *Ops) BSEUCCPreview(ctx context.Context, tenant, actor, ucc string) (BSEUCCPreview, error) {
	if err := o.require(ctx, tenant, actor, "Viewing BSE UCC registrations", RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return BSEUCCPreview{}, err
	}
	c, err := o.Client(ctx, tenant, ucc)
	if err != nil {
		return BSEUCCPreview{}, err
	}
	items, _, err := o.bseUCCItems(ctx, tenant)
	if err != nil {
		return BSEUCCPreview{}, err
	}
	for _, it := range items {
		if it.c.UCC == c.UCC {
			st, _ := bseUCCRowStatus(it)
			return BSEUCCPreview{UCC: c.UCC, Action: it.reg.Action, Status: st, Pending: it.pending, Payload: it.rec.Masked().JSON(), Problems: it.rec.Problems, Hash: it.hash}, nil
		}
	}
	// Not one to register (yet): show what a modification would be.
	set, _ := o.Settings(ctx, tenant)
	rec := bseUCCMap(c, o.bseUCCLoadSources(ctx, tenant, set), "Modify", nil, o.now())
	return BSEUCCPreview{UCC: c.UCC, Action: "Modify", Status: "", Payload: rec.Masked().JSON(), Problems: rec.Problems, Hash: rec.Hash()}, nil
}

// ---- batch file ----

// bseUCCBatchFile is the batch upload file of every pending client whose
// record has no problems, and the clients left out.
func (o *Ops) bseUCCBatchFile(ctx context.Context, tenant string, s *xfile.Spec, vars xfile.Vars) (string, []byte, []string, []string, error) {
	items, _, err := o.bseUCCItems(ctx, tenant)
	if err != nil {
		return "", nil, nil, nil, err
	}
	var recs []bseUCCRecord
	var in, left []string
	for _, it := range items {
		if !it.pending {
			continue
		}
		if len(it.rec.Problems) > 0 {
			left = append(left, it.c.UCC+": "+strings.Join(it.rec.Problems, "; "))
			continue
		}
		recs = append(recs, it.rec)
		in = append(in, it.c.UCC)
	}
	rows, err := bseUCCBatchRows(s, recs)
	if err != nil {
		return "", nil, nil, nil, err
	}
	name, err := s.FileName(vars)
	if err != nil {
		return "", nil, nil, nil, invalid("%v", err)
	}
	b, err := s.Write(rows)
	if err != nil {
		return "", nil, nil, nil, invalid("%v", err)
	}
	return name, b, in, left, nil
}

// BSEUCCBatch is the batch file for download.
type BSEUCCBatch struct {
	Name     string   `json:"name"`
	Content  []byte   `json:"-"`
	Clients  []string `json:"clients"`
	Problems []string `json:"problems"`
}

// BSEUCCBatchFile writes the batch upload file for BSE's UCC portal, the
// fallback when the API is not connected.
func (o *Ops) BSEUCCBatchFile(ctx context.Context, tenant, actor string) (BSEUCCBatch, error) {
	if err := o.require(ctx, tenant, actor, "Downloading exchange files", RoleMaker, RoleChecker, RoleRisk, RoleCompliance); err != nil {
		return BSEUCCBatch{}, err
	}
	lib, err := XFiles()
	if err != nil {
		return BSEUCCBatch{}, err
	}
	s, ok := lib.Get("bse-ucc")
	if !ok {
		return BSEUCCBatch{}, fmt.Errorf("the BSE UCC layout is missing")
	}
	today := o.now().Format("2006-01-02")
	name, b, in, left, err := o.bseUCCBatchFile(ctx, tenant, s, xfile.Vars{"date": today, "seq": "1"})
	if err != nil {
		return BSEUCCBatch{}, err
	}
	o.log(ctx, tenant, actor, "xfile.written", name, map[string]any{"format": "bse-ucc", "date": today, "clients": len(in), "left_out": len(left)})
	if in == nil {
		in = []string{}
	}
	if left == nil {
		left = []string{}
	}
	return BSEUCCBatch{Name: name, Content: b, Clients: in, Problems: left}, nil
}

// BSEUCCMarkUploaded records that the batch file with these clients was
// uploaded on BSE's UCC portal: they are Sent, and become Accepted when
// BSE's PAN status finds them.
func (o *Ops) BSEUCCMarkUploaded(ctx context.Context, tenant, actor, file string, uccs []string) (BSEUCCResult, error) {
	res := BSEUCCResult{Problems: []string{}}
	if err := o.require(ctx, tenant, actor, "Sending UCC to BSE", RoleMaker, RoleChecker); err != nil {
		return res, err
	}
	if len(uccs) == 0 {
		return res, invalid("give the client codes in the uploaded file")
	}
	bseUCCMu.Lock()
	defer bseUCCMu.Unlock()
	items, _, err := o.bseUCCItems(ctx, tenant)
	if err != nil {
		return res, err
	}
	want := map[string]bool{}
	for _, u := range uccs {
		want[strings.ToUpper(strings.TrimSpace(u))] = true
	}
	at := o.now().Format(time.RFC3339)
	for _, it := range items {
		if !want[it.c.UCC] {
			continue
		}
		delete(want, it.c.UCC)
		if !it.pending || len(it.rec.Problems) > 0 {
			res.Problems = append(res.Problems, it.c.UCC+": not in a batch file (nothing to send, or it needs fixing)")
			continue
		}
		r := it.reg
		r.Hash, r.SentAt, r.Via, r.Ref, r.Problems = it.hash, at, "File", "Batch file "+strings.TrimSpace(file), nil
		r.event(at, actor, UCCSent, "Uploaded on BSE's UCC portal", r.Ref)
		o.bseUCCSaveReg(ctx, tenant, r)
		res.Sent++
	}
	for u := range want {
		res.Problems = append(res.Problems, u+": not a client to register at BSE")
	}
	res.Message = fmt.Sprintf("%d client(s) marked as uploaded to BSE", res.Sent)
	o.log(ctx, tenant, actor, "bseucc.file_uploaded", file, map[string]any{"clients": res.Sent})
	return res, nil
}

// ---- scheduler ----

// bseUCCTick sends pending clients every 15 minutes from 08:00 to 22:00
// when sending is switched on, asks BSE's PAN status for registered clients
// once a day each (a request at most every three minutes, under BSE's
// two-minute rule), and reminds the Admin before the password expires.
func (o *Ops) bseUCCTick(ctx context.Context, tenant string, now time.Time) {
	cn, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connBSEUCC)
	if err != nil || cn.Status == "Not set" || cn.Status == "" {
		return
	}
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cBSEUCCState, "state", bseUCCStateDoc{})
	date := now.In(ist).Format("2006-01-02")
	// The password lasts 30 days: remind from day 25.
	since := st.PasswordSetOn
	if since == "" && len(cn.UpdatedAt) >= 10 {
		since = cn.UpdatedAt[:10]
	}
	if t, err := time.ParseInLocation("2006-01-02", since, ist); err == nil {
		if days := int(now.Sub(t).Hours() / 24); days >= 25 {
			o.raiseWork(ctx, tenant, "bseucc", "password", "Reset the BSE UCC API password",
				fmt.Sprintf("BSE's API passwords expire every 30 days; this one was set on %s. Reset it on the BSE UCC screen (Admin).", fmtDay(since)), "", RoleAdmin, "connections", date)
		}
	}
	if cn.Values["send"] != "On" || cn.Status == "Problem" {
		return
	}
	if hm := now.In(ist).Format("15:04"); hm < "08:00" || hm >= "22:00" {
		return
	}
	if t, err := time.Parse(time.RFC3339, st.LastSend); err != nil || now.Sub(t) >= 15*time.Minute {
		st.LastSend = now.Format(time.RFC3339)
		_ = docs.Upsert(ctx, o.Docs, tenant, cBSEUCCState, "state", st, SystemActor)
		_, _ = o.BSEUCCSend(ctx, tenant, SystemActor, nil)
		st, _, _ = docs.GetOr(ctx, o.Docs, tenant, cBSEUCCState, "state", st)
	}
	if t, err := time.Parse(time.RFC3339, st.LastPAN); err != nil || now.Sub(t) >= 3*time.Minute {
		_, _ = o.BSEUCCPANStatus(ctx, tenant, SystemActor, nil)
	}
}
