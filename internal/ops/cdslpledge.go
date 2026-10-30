package ops

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// ---- CDSL pledge APIs: margin pledge, re-pledge, normal pledge,
// unpledge and invocation (CDSL API Handbook §5.16–5.20) ----
//
// Ledgerline, as the firm's CDSL DP, sends pledges to CDSL's API gateway
// instead of the depository's own screens:
//
//   - Margin pledge (§5.16): a client asks to pledge shares to the firm as
//     margin (portal or desk); a checker approves; the client confirms on
//     CDSL's own page with the OTP CDSL sends (a browser form post to
//     CDSL), and CDSL posts the result back to Ledgerline's return address.
//     Margin pledge inquiry (§5.17) settles requests whose result did not
//     come back. A successful pledge raises the client's pledged quantity,
//     which collateral and RMS use. The MTF pledge (identifier MF) works
//     the same way and marks the MTF position pledged.
//   - Margin re-pledge (§5.18): the firm re-pledges clients' collateral to
//     the clearing member / clearing corporation; reconciled with NSE
//     Clearing's re-pledge status file (Clearing data).
//   - Normal pledge (§5.19): a pledge to a lender (loan against shares), a
//     DP request with maker and checker.
//   - Unpledge and confiscation (§5.20): release on the client's request
//     (maker, checker), and invocation for an unpaid debit or an MTF
//     position to be liquidated (risk raises, the client gets notice,
//     compliance approves, then it goes once the notice period is over).
//
// Every call is AES-256-CBC with PKCS#7 padding and a zero IV under the API
// key CDSL issues to the DP, carried base64 (handbook "IV factor should be
// null"). The key, passwords and OTPs are never logged; BO IDs are logged
// with the last four digits only.
//
// The connection ("cdsl-api", CDSL APIs (DP)) is shared with the other
// CDSL API screens and is defined in cdslapi.go: this file reads the DP
// ID, environment (Mock or Live) and the API key (secret "secret") from it,
// and the on/off switches "pledge" and "early_payin" when present.

const (
	cplConnID = "cdsl-api"
	cplVia    = "CDSL API"

	cCPLCfg      = "cdslplcfg"    // main: CDSLPledgeConfig
	cCPLTok      = "cdslpltok"    // sha256(token): client confirmation links
	cCPLReq      = "cdslplreq"    // CDSL reqid: what was sent in it
	cCPLRepledge = "cdslrepledge" // RP-n: re-pledge batches
	cCPLNormal   = "cdslnpledge"  // NP-n: normal pledges (loan against shares)
	cCPLRelease  = "cdslunpledge" // UN-n / CF-n: unpledge and invocation

	cplVersion = "2.0"
)

// cplHosts are CDSL's API gateways by environment (handbook §5.16.5 ff.).
// Tests point them at a fake.
var cplHosts = map[string]string{"Mock": "https://mockapigt.cdsl.co.in", "Live": "https://apigt.cdsl.co.in"}

// cplPaths are the endpoints under the gateway.
var cplPaths = map[string]string{
	"MP":         "/PledgeServices/MPSetup",
	"MF":         "/PledgeServices/MPSetup",
	"MPInquiry":  "/PledgeServices/MPInquiry",
	"MR":         "/PledgeServices/MRSetup",
	"EP":         "/PledgeServices/Setup",
	"UN":         "/PledgeServices/UnpledgeSetup",
	"CF":         "/PledgeServices/ConfiscateSetup",
	"EPI":        "/EarlyPayin/v2.0",
	"EPIInquiry": "/EarlyPayin/Inquiry",
}

// cplHTTP calls CDSL; tests swap it. CDSL requires TLS 1.2 or later.
var cplHTTP = &http.Client{Timeout: 60 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, TLSHandshakeTimeout: 20 * time.Second}}

func init() {
	for _, k := range []string{"cdsl.pledge_confirm", "cdsl.pledge_done", "cdsl.pledge_failed", "cdsl.unpledge_done", "cdsl.invocation_notice", "cdsl.invocation_done"} {
		EventVars[k] = []string{"ref", "scrips", "qty", "psn", "reason", "debit", "due"}
	}
	t := func(ev, ch, subj, body, dlt string) Template {
		return Template{Event: ev, Channel: ch, Category: "Transactional", Subject: subj, Body: body, DLT: dlt, Active: true}
	}
	extraTemplates = append(extraTemplates,
		t("cdsl.pledge_confirm", ChEmail, "Confirm your pledge with CDSL", "Dear {name},\n\nYour request {ref} to pledge {scrips} as margin is approved. To complete it, open Pledge shares in the client portal and choose Confirm with CDSL: CDSL shows you the pledge and sends a one-time password to your registered mobile and email.\n\nIf you did not ask for this pledge, do not confirm it and tell us at once.\n\n{firm}", ""),
		t("cdsl.pledge_confirm", ChSMS, "", "Dear {name}, your pledge request {ref} ({scrips}) is approved. Confirm it with the CDSL OTP from Pledge shares in the client portal. Not you? Call us. -{firm}", "1107166000000000061"),
		t("cdsl.pledge_done", ChEmail, "Pledge set up: {scrips}", "Dear {name},\n\nCDSL has set up your pledge {ref}: {scrips}, pledge sequence number {psn}. The shares stay in your demat account and count as margin with us.\n\n{firm}", ""),
		t("cdsl.pledge_failed", ChEmail, "Pledge not set up: {ref}", "Dear {name},\n\nYour pledge request {ref} ({scrips}) was not set up by CDSL: {reason}. You can ask again from the client portal.\n\n{firm}", ""),
		t("cdsl.unpledge_done", ChEmail, "Shares released from pledge: {scrips}", "Dear {name},\n\nThe pledge {psn} is released for {scrips} ({ref}). The shares are free in your demat account.\n\n{firm}", ""),
		t("cdsl.invocation_notice", ChEmail, "Notice: pledged shares to be invoked", "Dear {name},\n\nYour trading account has an overdue debit of ₹{debit}. Unless it is paid by {due}, we will invoke the pledge {psn} and transfer {scrips} from your demat account to recover it, as the rights and obligations document allows. Any surplus after the sale is credited to your account.\n\nPay the debit before {due} to keep your shares. Questions: reply to this email.\n\n{firm}", ""),
		t("cdsl.invocation_notice", ChSMS, "", "Dear {name}, overdue debit Rs {debit}. Unless paid by {due}, pledge {psn} ({scrips}) will be invoked to recover it. -{firm}", "1107166000000000062"),
		t("cdsl.invocation_done", ChEmail, "Pledged shares invoked: {scrips}", "Dear {name},\n\nAs our notice said, the pledge {psn} was invoked for {scrips} ({ref}) to recover the overdue debit. The sale proceeds will be credited to your trading account.\n\n{firm}", ""),
	)
}

// ---- connection, keys, transport ----

// cplConn is what the shared CDSL API connection gives these APIs.
type cplConn struct {
	DPID string // 6-digit DP ID as the APIs carry it
	DP8  string // the 8-digit DP ID BO IDs start with, when known
	Env  string // Mock or Live
	key  []byte
}

func (c cplConn) url(kind string) string { return cplHosts[c.Env] + cplPaths[kind] }

// ErrCDSLNotSet means the CDSL API connection is missing or switched off.
var ErrCDSLNotSet = errors.New("the CDSL APIs connection is not set up (Settings → Connections → CDSL APIs (DP))")

// cplConnection reads the shared connection for an API family ("pledge" or
// "early_payin"). A missing connection, or the family switched off, is
// ErrCDSLNotSet.
func (o *Ops) cplConnection(ctx context.Context, tenant, family string) (cplConn, error) {
	c, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, cplConnID)
	if err != nil || c.Status == "Not set" || c.Values == nil {
		return cplConn{}, ErrCDSLNotSet
	}
	for _, k := range []string{family, "api_" + family} {
		if v := strings.ToLower(strings.TrimSpace(c.Values[k])); v == "off" || v == "no" || v == "false" || v == "disabled" {
			return cplConn{}, fmt.Errorf("%w: the %s APIs are switched off there", ErrCDSLNotSet, strings.ReplaceAll(family, "_", "-"))
		}
	}
	dp := strings.TrimSpace(c.Values["dp_id"])
	cc := cplConn{Env: "Mock"}
	switch {
	case len(dp) == 8 && strings.Trim(dp, "0123456789") == "":
		cc.DP8, cc.DPID = dp, dp[2:]
	case len(dp) >= 1 && len(dp) <= 6 && strings.Trim(dp, "0123456789") == "":
		cc.DPID = dp
	default:
		return cplConn{}, fmt.Errorf("%w: the DP ID should be CDSL's 8-digit DP ID (or its last 6 digits)", ErrCDSLNotSet)
	}
	if e := strings.ToLower(c.Values["env"]); strings.HasPrefix(e, "live") || strings.HasPrefix(e, "prod") {
		cc.Env = "Live"
	}
	key, err := cplKey(c.Secrets["secret"])
	if err != nil {
		return cplConn{}, err
	}
	cc.key = key
	return cc, nil
}

// cplKey turns the API key CDSL issues into the AES-256 key: 32 characters
// used as they are, or 64 hex digits, or base64 of 32 bytes.
func cplKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	switch {
	case s == "":
		return nil, fmt.Errorf("%w: enter the API key CDSL issued (Secret key)", ErrCDSLNotSet)
	case len(s) == 32:
		return []byte(s), nil
	case len(s) == 64:
		if b, err := hex.DecodeString(s); err == nil {
			return b, nil
		}
	}
	if b, err := base64.StdEncoding.DecodeString(s); err == nil && len(b) == 32 {
		return b, nil
	}
	return nil, fmt.Errorf("%w: the API key must be 32 characters for AES-256 (or 64 hex digits)", ErrCDSLNotSet)
}

// cplEncrypt is AES-256-CBC, PKCS#7 padding, zero IV, base64 (§5.16.7).
func cplEncrypt(key, plain []byte) (string, error) {
	b, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	n := aes.BlockSize - len(plain)%aes.BlockSize
	buf := append(append([]byte{}, plain...), bytes.Repeat([]byte{byte(n)}, n)...)
	cipher.NewCBCEncrypter(b, make([]byte, aes.BlockSize)).CryptBlocks(buf, buf)
	return base64.StdEncoding.EncodeToString(buf), nil
}

var errCPLDecrypt = errors.New("the reply could not be decrypted with the CDSL API key")

// cplDecrypt reverses cplEncrypt. A form post may turn '+' into a space.
func cplDecrypt(key []byte, s string) ([]byte, error) {
	s = strings.ReplaceAll(strings.TrimSpace(s), " ", "+")
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		if raw, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(s, "=")); err != nil {
			return nil, errCPLDecrypt
		}
	}
	if len(raw) == 0 || len(raw)%aes.BlockSize != 0 {
		return nil, errCPLDecrypt
	}
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(raw))
	cipher.NewCBCDecrypter(b, make([]byte, aes.BlockSize)).CryptBlocks(out, raw)
	n := int(out[len(out)-1])
	if n == 0 || n > aes.BlockSize || n > len(out) || !bytes.Equal(out[len(out)-n:], bytes.Repeat([]byte{byte(n)}, n)) {
		return nil, errCPLDecrypt
	}
	return out[:len(out)-n], nil
}

// cplNorm re-encodes JSON with every object key trimmed and lower-cased:
// CDSL's samples spell keys loosely (" pledgeresdtls", "partQuantity").
func cplNorm(b []byte) ([]byte, error) {
	var v any
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if err := d.Decode(&v); err != nil {
		return nil, err
	}
	var walk func(any) any
	walk = func(x any) any {
		switch t := x.(type) {
		case map[string]any:
			m := map[string]any{}
			for k, v := range t {
				m[strings.ToLower(strings.TrimSpace(k))] = walk(v)
			}
			return m
		case []any:
			for i := range t {
				t[i] = walk(t[i])
			}
			return t
		case json.Number:
			return t.String() // numbers as strings, as the structs expect
		}
		return x
	}
	return json.Marshal(walk(v))
}

var reHidden = regexp.MustCompile(`(?is)<input[^>]*name\s*=\s*["']?([a-z0-9_]+)["']?[^>]*value\s*=\s*["']([^"']*)["']`)

// cplOpen finds and decrypts the encrypted part of a CDSL reply. Replies
// may be a JSON object carrying the encrypted value (field names), a
// form-encoded or HTML form post, a bare (or quoted) base64 string, or
// already-plain JSON. It returns key-normalised JSON.
func cplOpen(key, body []byte, fields ...string) ([]byte, error) {
	s := strings.TrimSpace(string(body))
	try := func(v string) ([]byte, bool) {
		p, err := cplDecrypt(key, v)
		if err != nil {
			return nil, false
		}
		n, err := cplNorm(p)
		return n, err == nil
	}
	if strings.HasPrefix(s, "{") {
		var m map[string]any
		if err := json.Unmarshal([]byte(s), &m); err == nil {
			lm := map[string]any{}
			for k, v := range m {
				lm[strings.ToLower(strings.TrimSpace(k))] = v
			}
			for _, f := range fields {
				if v, ok := lm[f].(string); ok {
					if p, ok := try(v); ok {
						return p, nil
					}
				}
			}
			for _, k := range []string{"reqid", "resstatus", "reqstatus", "reqseqno", "requestseqno", "dpreqseqno"} {
				if _, ok := lm[k]; ok {
					return cplNorm([]byte(s))
				}
			}
			ks := make([]string, 0, len(lm))
			for k := range lm {
				ks = append(ks, k)
			}
			sort.Strings(ks)
			for _, k := range ks {
				if v, ok := lm[k].(string); ok && len(v) >= 16 {
					if p, ok := try(v); ok {
						return p, nil
					}
				}
			}
		}
	}
	if strings.HasPrefix(s, "\"") {
		var q string
		if json.Unmarshal([]byte(s), &q) == nil {
			s = q
		}
	}
	if strings.Contains(strings.ToLower(s), "<input") {
		for _, m := range reHidden.FindAllStringSubmatch(s, -1) {
			for _, f := range fields {
				if strings.EqualFold(m[1], f) {
					if p, ok := try(template.HTMLEscapeString(m[2])); ok {
						return p, nil
					}
					if p, ok := try(m[2]); ok {
						return p, nil
					}
				}
			}
		}
	}
	if strings.Contains(s, "=") && !strings.ContainsAny(s, "{<") {
		if q, err := url.ParseQuery(s); err == nil {
			for _, f := range fields {
				for k, vs := range q {
					if strings.EqualFold(k, f) && len(vs) > 0 {
						if p, ok := try(vs[0]); ok {
							return p, nil
						}
					}
				}
			}
		}
	}
	if p, ok := try(s); ok {
		return p, nil
	}
	return nil, errCPLDecrypt
}

// cplReply is an HTTP answer from CDSL, kept for the request log.
type cplReply struct {
	Status int
	Body   []byte
}

// cplPostForm sends a form post (the setup APIs).
func (o *Ops) cplPostForm(ctx context.Context, cc cplConn, kind string, form url.Values) (cplReply, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cc.url(kind), strings.NewReader(form.Encode()))
	if err != nil {
		return cplReply{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return cplDo(req)
}

// cplPostJSON sends the JSON APIs (inquiries, early pay-in): dpid, version
// and requestseqno as request headers and {"encryptreqdtl": …} as the body
// (handbook §5.9.6, §5.10.6, §5.17.6 "Request Header/Body").
func (o *Ops) cplPostJSON(ctx context.Context, cc cplConn, kind, seq string, enc string) (cplReply, error) {
	b, _ := json.Marshal(map[string]string{"encryptreqdtl": enc})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cc.url(kind), bytes.NewReader(b))
	if err != nil {
		return cplReply{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("dpid", cc.DPID)
	req.Header.Set("version", cplVersion)
	req.Header.Set("requestseqno", seq)
	return cplDo(req)
}

func cplDo(req *http.Request) (cplReply, error) {
	resp, err := cplHTTP.Do(req)
	if err != nil {
		return cplReply{}, fmt.Errorf("CDSL did not answer: %s", cplErrText(err))
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return cplReply{}, fmt.Errorf("CDSL's answer was cut off: %v", err)
	}
	r := cplReply{Status: resp.StatusCode, Body: b}
	if resp.StatusCode >= 400 {
		return r, fmt.Errorf("CDSL answered %d", resp.StatusCode)
	}
	return r, nil
}

func cplErrText(err error) string {
	m := err.Error()
	if i := strings.LastIndex(m, "\": "); i >= 0 {
		m = m[i+3:]
	}
	return m
}

// maskBO keeps the last four digits of a BO ID for logs.
func maskBO(b string) string {
	if len(b) <= 4 {
		return "****"
	}
	return strings.Repeat("*", len(b)-4) + b[len(b)-4:]
}

func cplTime(t time.Time) string { return t.In(ist).Format("02012006150405") }
func cplDate(d string) string {
	if t, err := time.Parse("2006-01-02", d); err == nil {
		return t.Format("02012006")
	}
	return ""
}
func cplQty(q int64) string { return strconv.FormatInt(q, 10) }

// cplReqID is a reqid unique for the DP for ever: the firm's own counter.
func (o *Ops) cplReqID(ctx context.Context, tenant, prefix string, width int) (string, error) {
	n, err := o.next(ctx, tenant, "cdslreq", SystemActor)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s%0*d", prefix, width-len(prefix), n), nil
}

// ---- configuration ----

// CDSLPledgeConfig is the firm's own details the pledge and early pay-in
// APIs carry: its pledgee accounts and exchange codes as CDSL knows them.
type CDSLPledgeConfig struct {
	// Margin pledge (§5.16): the firm's client securities margin pledge
	// account, and for MTF the client securities under margin funding account.
	PledgeeBOID    string `json:"pledgee_bo_id"`
	MTFPledgeeBOID string `json:"mtf_pledgee_bo_id"`
	Entity         string `json:"entity"`      // TM or CP
	TMID           string `json:"tm_id"`       // trading member ID as CDSL has it
	CMID           string `json:"cm_id"`       // clearing member ID (8)
	ExID           string `json:"ex_id"`       // CDSL exchange ID (2 digits)
	SegmentID      string `json:"segment_id"`  // CDSL segment ID (2)
	ReasonCode     string `json:"reason_code"` // pledge reason code (2 digits)
	ExpiryDays     int    `json:"expiry_days"` // pledge expiry from the day it is set up
	// Re-pledge (§5.18): the clearing member's / clearing corporation's
	// pledgee account, its CM ID, reason code and pledge type of the
	// pledges re-pledged (MP).
	RepledgeeBOID  string `json:"repledgee_bo_id"`
	RepledgeCMID   string `json:"repledge_cm_id"`
	RepledgeReason string `json:"repledge_reason"`
	RepledgeType   string `json:"repledge_type"`
	// Normal pledge (§5.19) reason code.
	NormalReason string `json:"normal_reason"`
	// Invocation: days of notice before pledged shares are invoked.
	NoticeDays int `json:"notice_days"`
	// Early pay-in (§5.9): the counter / early pay-in BO, CM ID, entity
	// (TM, CP, PM or NA), exchange and segment IDs, and the DDPI POA ID.
	EPIBOID    string `json:"epi_bo_id"`
	EPICMID    string `json:"epi_cm_id"`
	EPIEntity  string `json:"epi_entity"`
	EPIExID    string `json:"epi_ex_id"`
	EPISegment string `json:"epi_segment"`
	EPITMCPID  string `json:"epi_tmcp_id"`
	DDPIPOAID  string `json:"ddpi_poa_id"`
	UpdatedBy  string `json:"updated_by,omitempty"`
	UpdatedAt  string `json:"updated_at,omitempty"`
}

func (c *CDSLPledgeConfig) defaults() {
	if c.Entity == "" {
		c.Entity = "TM"
	}
	if c.ExpiryDays == 0 {
		c.ExpiryDays = 365
	}
	if c.ReasonCode == "" {
		c.ReasonCode = "01"
	}
	if c.RepledgeReason == "" {
		c.RepledgeReason = c.ReasonCode
	}
	if c.RepledgeType == "" {
		c.RepledgeType = "MP"
	}
	if c.NormalReason == "" {
		c.NormalReason = "01"
	}
	if c.NoticeDays == 0 {
		c.NoticeDays = 1
	}
	if c.EPIEntity == "" {
		c.EPIEntity = "TM"
	}
}

// CDSLPledgeSettings returns the configuration.
func (o *Ops) CDSLPledgeSettings(ctx context.Context, tenant string) CDSLPledgeConfig {
	c, _, _ := docs.GetOr(ctx, o.Docs, tenant, cCPLCfg, "main", CDSLPledgeConfig{})
	c.defaults()
	return c
}

var (
	reBO16  = regexp.MustCompile(`^[0-9]{16}$`)
	reAlnum = regexp.MustCompile(`^[A-Za-z0-9-]*$`)
	reNum   = regexp.MustCompile(`^[0-9]*$`)
)

// SaveCDSLPledgeSettings checks and stores the configuration.
func (o *Ops) SaveCDSLPledgeSettings(ctx context.Context, tenant, actor string, c CDSLPledgeConfig) (CDSLPledgeConfig, error) {
	if err := o.require(ctx, tenant, actor, "Changing CDSL pledge settings", RoleCompliance, RoleRisk); err != nil {
		return c, err
	}
	for _, x := range []struct{ v, what string }{{c.PledgeeBOID, "margin pledgee BO ID"}, {c.MTFPledgeeBOID, "MTF pledgee BO ID"}, {c.RepledgeeBOID, "re-pledge pledgee BO ID"}, {c.EPIBOID, "early pay-in BO ID"}, {c.DDPIPOAID, "DDPI POA ID"}} {
		if x.v != "" && !reBO16.MatchString(x.v) {
			return c, invalid("the %s must be 16 digits", x.what)
		}
	}
	for _, x := range []struct {
		v, what string
		n       int
		re      *regexp.Regexp
	}{{c.TMID, "TM ID", 12, reAlnum}, {c.CMID, "CM ID", 8, reAlnum}, {c.RepledgeCMID, "re-pledge CM ID", 8, reAlnum}, {c.EPICMID, "early pay-in CM ID", 16, reAlnum}, {c.EPITMCPID, "early pay-in TM/CP ID", 12, reAlnum},
		{c.ExID, "exchange ID", 2, reNum}, {c.EPIExID, "early pay-in exchange ID", 2, reNum}, {c.ReasonCode, "reason code", 2, reNum}, {c.RepledgeReason, "re-pledge reason code", 2, reNum}, {c.NormalReason, "normal pledge reason code", 2, reNum}} {
		if len(x.v) > x.n || !x.re.MatchString(x.v) {
			return c, invalid("the %s is at most %d letters or digits", x.what, x.n)
		}
	}
	if c.Entity != "" && c.Entity != "TM" && c.Entity != "CP" {
		return c, invalid("the entity is TM or CP")
	}
	if c.EPIEntity != "" && !contains([]string{"TM", "CP", "PM", "NA"}, c.EPIEntity) {
		return c, invalid("the early pay-in entity is TM, CP, PM or NA")
	}
	if c.RepledgeType != "" && c.RepledgeType != "MP" && c.RepledgeType != "MR" {
		return c, invalid("the re-pledge pledge type is MP or MR")
	}
	if c.ExpiryDays < 0 || c.ExpiryDays > 3650 || c.NoticeDays < 0 || c.NoticeDays > 30 {
		return c, invalid("expiry is up to 3650 days and notice up to 30 days")
	}
	c.UpdatedBy, c.UpdatedAt = actor, o.now().Format(time.RFC3339)
	c.defaults()
	if err := docs.Upsert(ctx, o.Docs, tenant, cCPLCfg, "main", c, actor); err != nil {
		return c, err
	}
	o.log(ctx, tenant, actor, "cdsl.pledge_settings", "main", nil)
	return c, nil
}

// ---- the CDSL request log ----

// CDSLCall is one request sent to CDSL under a reqid.
type CDSLCall struct {
	ReqID   string   `json:"req_id"`
	Kind    string   `json:"kind"` // MP, MF, MR, EP, UN, CF, EPI
	UCC     string   `json:"ucc,omitempty"`
	IDs     []string `json:"ids"`     // Ledgerline records it carries
	ISINReq []string `json:"isin_req"` // isinreqid per record, same order
	At      string   `json:"at"`
	Status  string   `json:"status"` // Sent, Done, No reply, Abandoned
	Result  string   `json:"result,omitempty"`
	// LastInquiry is when the inquiry API was last asked.
	LastInquiry string `json:"last_inquiry,omitempty"`
	Inquiries   int    `json:"inquiries,omitempty"`
}

// ---- margin pledge (§5.16, §5.17) ----

// PledgeLine is one security in a pledge request.
type PledgeLine struct {
	ISIN string `json:"isin"`
	Qty  int64  `json:"qty"`
}

// cplHolding gives a client's holding and how much is free to pledge:
// quantity less what is pledged to the firm, to lenders, and in pledge
// requests not finished.
func (o *Ops) cplFree(ctx context.Context, tenant, ucc, isin, except string) (Holding, int64) {
	h, _, _ := docs.Get[Holding](ctx, o.Docs, tenant, cHolding, holdingID(ucc, isin))
	free := h.Qty - h.Pledged - o.cplEncumbered(ctx, tenant, ucc)[isin]
	ps, _ := docs.List[PledgeRequest](ctx, o.Docs, tenant, cPledge, "")
	for _, p := range ps {
		if p.ID != except && p.UCC == ucc && p.ISIN == isin && p.Via == cplVia && (p.Status == "Pending" || p.Status == "Awaiting client") {
			free -= p.Qty
		}
	}
	return h, free
}

// RequestMarginPledge records a client's request to pledge shares to the
// firm through CDSL's margin pledge API: from the client portal (the
// client's own login) or the desk. kind is MP (margin) or MF (MTF, with
// the position). A checker then approves each request.
func (o *Ops) RequestMarginPledge(ctx context.Context, tenant, actor, ucc, kind, mtfID string, lines []PledgeLine) ([]PledgeRequest, error) {
	role, err := o.RoleOf(ctx, tenant, actor)
	if err != nil {
		return nil, err
	}
	ucc = strings.ToUpper(strings.TrimSpace(ucc))
	if role == RoleClient {
		accts, err := o.PortalAccounts(ctx, tenant, actor)
		if err != nil {
			return nil, err
		}
		if !contains(accts, ucc) {
			return nil, ErrNotFound
		}
	} else if err := o.require(ctx, tenant, actor, "Entering margin pledge requests", RoleMaker, RoleChecker, RoleRisk); err != nil {
		return nil, err
	}
	if kind == "" {
		kind = "MP"
	}
	if kind != "MP" && kind != "MF" {
		return nil, invalid("the pledge is MP (margin) or MF (margin trading)")
	}
	if len(lines) == 0 || len(lines) > 50 {
		return nil, invalid("give 1 to 50 securities to pledge")
	}
	c, err := o.Client(ctx, tenant, ucc)
	if err != nil {
		return nil, err
	}
	bo := o.bo(ctx, tenant, c)
	if c.Status != "Active" || bo.Status != "Active" || bo.Freeze == "Debit" || bo.Freeze == "Both" {
		return nil, invalid("%s's account or demat account is not active for debits", ucc)
	}
	if !reBO16.MatchString(bo.BOID) || !strings.EqualFold(firstOf(bo.Depository, "CDSL"), "CDSL") {
		return nil, invalid("%s has no CDSL demat account with this DP", ucc)
	}
	var mtf MTFPosition
	if kind == "MF" {
		mtf, _, err = docs.Get[MTFPosition](ctx, o.Docs, tenant, cMTFPos, mtfID)
		if err != nil || mtf.UCC != ucc || mtf.Status != "Open" || mtf.Pledge == "Pledged" {
			return nil, invalid("the MTF position %s is not open and waiting for its pledge", mtfID)
		}
		lines = []PledgeLine{{ISIN: mtf.ISIN, Qty: mtf.Qty}}
	}
	seen := map[string]bool{}
	var out []PledgeRequest
	now := o.now()
	for _, l := range lines {
		l.ISIN = strings.ToUpper(strings.TrimSpace(l.ISIN))
		if len(l.ISIN) != 12 || seen[l.ISIN] {
			return nil, invalid("each ISIN is 12 characters and given once")
		}
		seen[l.ISIN] = true
		if l.Qty <= 0 {
			return nil, invalid("give the number of shares to pledge")
		}
		h, free := o.cplFree(ctx, tenant, ucc, l.ISIN, "")
		if l.Qty > free {
			return nil, invalid("only %d %s are free to pledge", max(free, 0), firstOf(h.Symbol, l.ISIN))
		}
		n, err := o.next(ctx, tenant, "cdslmp", actor)
		if err != nil {
			return nil, err
		}
		p := PledgeRequest{ID: fmt.Sprintf("%s-%d", kind, n), UCC: ucc, ISIN: l.ISIN, Symbol: firstOf(h.Symbol, l.ISIN), Qty: l.Qty, Time: now.Format("2006-01-02 15:04"), Status: "Pending",
			Via: cplVia, Kind: kind, MTFID: mtfID, RequestedBy: actor}
		if _, err := docs.Put(ctx, o.Docs, tenant, cPledge, p.ID, p, 0, actor); err != nil {
			return nil, err
		}
		o.raiseWork(ctx, tenant, "cdslpledge", p.ID, "Approve margin pledge "+p.ID, fmt.Sprintf("%s asks to pledge %d %s to the firm through CDSL. Check and approve; the client then confirms with CDSL's OTP.", ucc, p.Qty, p.Symbol), ucc, RoleChecker, "cdslpledge", now.Format("2006-01-02"))
		o.log(ctx, tenant, actor, "cdsl.pledge.requested", p.ID, map[string]any{"ucc": ucc, "isin": p.ISIN, "qty": p.Qty, "kind": kind})
		out = append(out, p)
	}
	return out, nil
}

// decideAPIPledge is DecidePledge for a request sent through CDSL's API:
// a checker other than the person who entered it approves; the pledge is
// made only once the client confirms it with CDSL's OTP.
func (o *Ops) decideAPIPledge(ctx context.Context, tenant, actor string, p PledgeRequest, accept bool, reason string) (PledgeRequest, error) {
	if err := o.require(ctx, tenant, actor, "Approving margin pledges sent to CDSL", RoleChecker, RoleRisk); err != nil {
		return p, err
	}
	if actor == p.RequestedBy {
		return p, fmt.Errorf("%w: a different person approves the pledge request (maker and checker)", ErrForbidden)
	}
	if accept {
		if _, free := o.cplFree(ctx, tenant, p.UCC, p.ISIN, p.ID); p.Qty > free {
			return p, fmt.Errorf("%w: only %d %s are free to pledge", ErrState, max(free, 0), p.Symbol)
		}
	}
	p, err := docs.Update(ctx, o.Docs, tenant, cPledge, p.ID, actor, func(x *PledgeRequest) error {
		if x.Status != "Pending" {
			return fmt.Errorf("%w: pledge %s is %s", ErrState, x.ID, x.Status)
		}
		x.DecidedBy = actor
		if accept {
			x.Status = "Awaiting client"
		} else {
			x.Status, x.Reason = "Rejected", reason
		}
		return nil
	})
	if err != nil {
		return p, err
	}
	o.closeWork(ctx, tenant, actor, "cdslpledge", p.ID, p.Status)
	if accept {
		o.notify(ctx, tenant, actor, "cdsl.pledge_confirm", p.UCC, map[string]string{"ref": p.ID, "scrips": fmt.Sprintf("%d %s", p.Qty, p.Symbol)})
	}
	o.log(ctx, tenant, actor, "cdsl.pledge."+map[bool]string{true: "approved", false: "rejected"}[accept], p.ID, map[string]any{"ucc": p.UCC, "qty": p.Qty})
	return p, nil
}

// CDSLPledgeLink makes the one-time link with which a client confirms
// their approved pledge requests at CDSL. The client's own login only.
func (o *Ops) CDSLPledgeLink(ctx context.Context, tenant, actor, ucc string) (string, error) {
	accts, err := o.PortalAccounts(ctx, tenant, actor)
	if err != nil {
		return "", err
	}
	ucc = strings.ToUpper(ucc)
	if !contains(accts, ucc) {
		return "", ErrNotFound
	}
	if len(o.cplAwaiting(ctx, tenant, ucc)) == 0 {
		return "", invalid("there is no approved pledge waiting for your confirmation")
	}
	if _, err := o.cplConnection(ctx, tenant, "pledge"); err != nil {
		return "", err
	}
	tok, err := newToken()
	if err != nil {
		return "", err
	}
	now := o.now()
	t := cplToken{UCC: ucc, Created: now.Format(time.RFC3339), Expires: now.Add(15 * time.Minute).Format(time.RFC3339)}
	if err := docs.Upsert(ctx, o.Docs, tenant, cCPLTok, cplTokID(tok), t, actor); err != nil {
		return "", err
	}
	return tok, nil
}

type cplToken struct {
	UCC     string `json:"ucc"`
	Created string `json:"created"`
	Expires string `json:"expires"`
	Used    string `json:"used,omitempty"`
}

func cplTokID(tok string) string { s := sha256.Sum256([]byte(tok)); return hex.EncodeToString(s[:16]) }

func (o *Ops) cplAwaiting(ctx context.Context, tenant, ucc string) []PledgeRequest {
	ps, _ := docs.List[PledgeRequest](ctx, o.Docs, tenant, cPledge, "")
	var out []PledgeRequest
	for _, p := range ps {
		if p.UCC == ucc && p.Via == cplVia && p.Status == "Awaiting client" {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time < out[j].Time })
	return out
}

// CDSLPledgePage is the page that hands the client over to CDSL (a form
// posted to CDSL's margin pledge address), and the page the client sees
// when CDSL sends them back.
type CDSLPledgePage struct {
	Firm   string
	Name   string
	Action string
	Fields []CDSLFormField
	Lines  []string
	Error  string
	Done   string // the result, on the page after CDSL
	Back   string
}

// CDSLFormField is a hidden form value.
type CDSLFormField struct{ Name, Value string }

// CDSLPledgeHTML renders CDSLPledgePage. No script: the client presses the
// button, which posts the form to CDSL.
var CDSLPledgeHTML = template.Must(template.New("cdslpledge").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Pledge · {{.Firm}}</title>
<style>body{font:16px/1.5 system-ui,-apple-system,Segoe UI,sans-serif;background:#f3f4f1;color:#1d2420;margin:0;padding:24px 16px}main{max-width:520px;margin:0 auto;background:#fff;border:1px solid #d9ddd6;border-radius:12px;padding:24px}h1{font-size:20px;margin:0 0 12px}button,a.b{font:inherit;padding:10px 16px;border-radius:8px;border:1px solid #1f6b45;background:#1f6b45;color:#fff;cursor:pointer;text-decoration:none;display:inline-block}ul{padding-left:20px}small{color:#5b655e}@media (prefers-color-scheme:dark){body{background:#151a17;color:#e4e8e3}main{background:#1d2420;border-color:#333b35}small{color:#a5ada6}}</style></head><body><main>
{{if .Error}}<h1>{{.Firm}}</h1><p>{{.Error}}</p>{{if .Back}}<p><a class="b" href="{{.Back}}">Back to the client portal</a></p>{{end}}
{{else if .Done}}<h1>Your pledge</h1><p>{{.Done}}</p><ul>{{range .Lines}}<li>{{.}}</li>{{end}}</ul>{{if .Back}}<p><a class="b" href="{{.Back}}">Back to the client portal</a></p>{{end}}
{{else}}<h1>Confirm your pledge with CDSL</h1><p>Dear {{.Name}}, you asked {{.Firm}} to pledge these shares as margin. CDSL will show you the pledge and send a one-time password to the mobile number and email registered with your demat account.</p><ul>{{range .Lines}}<li>{{.}}</li>{{end}}</ul>
<form method="post" action="{{.Action}}">{{range .Fields}}<input type="hidden" name="{{.Name}}" value="{{.Value}}">{{end}}<button type="submit">Continue to CDSL</button></form>
<p><small>The shares stay in your demat account. Do not confirm a pledge you did not ask for.</small></p>{{end}}
</main></body></html>`))

// CDSLPledgeFormPage builds the hand-over to CDSL from a client's link.
// base is the address browsers reach Ledgerline at (for CDSL's return).
// The link works once.
func (o *Ops) CDSLPledgeFormPage(ctx context.Context, tenant, token, base string) (CDSLPledgePage, error) {
	set, _ := o.Settings(ctx, tenant)
	pg := CDSLPledgePage{Firm: set.Firm, Back: strings.TrimRight(base, "/") + "/#ppledge"}
	fail := func(msg string, err error) (CDSLPledgePage, error) { pg.Error = msg; return pg, err }
	if len(token) < 16 {
		return fail("This link is not valid.", ErrNotFound)
	}
	id := cplTokID(token)
	now := o.now()
	var tk cplToken
	_, err := docs.Update(ctx, o.Docs, tenant, cCPLTok, id, SystemActor, func(t *cplToken) error {
		if t.Used != "" {
			return fmt.Errorf("%w: used", ErrState)
		}
		if exp, err := time.Parse(time.RFC3339, t.Expires); err != nil || now.After(exp) {
			return fmt.Errorf("%w: expired", ErrState)
		}
		t.Used = now.Format(time.RFC3339)
		tk = *t
		return nil
	})
	if err != nil {
		return fail("This link has expired or was already used. Open Pledge shares in the client portal and choose Confirm with CDSL again.", ErrNotFound)
	}
	cc, err := o.cplConnection(ctx, tenant, "pledge")
	if err != nil {
		return fail("Pledges through CDSL are not available just now. Please try later.", err)
	}
	cfg := o.CDSLPledgeSettings(ctx, tenant)
	ps := o.cplAwaiting(ctx, tenant, tk.UCC)
	if len(ps) == 0 {
		return fail("There is no approved pledge waiting for your confirmation.", ErrNotFound)
	}
	kind := firstOf(ps[0].Kind, "MP")
	var batch []PledgeRequest
	for _, p := range ps {
		if firstOf(p.Kind, "MP") == kind && len(batch) < 50 {
			batch = append(batch, p)
		}
	}
	c, err := o.Client(ctx, tenant, tk.UCC)
	if err != nil {
		return fail("Your account could not be found.", err)
	}
	bo := o.bo(ctx, tenant, c)
	pledgee := cfg.PledgeeBOID
	if kind == "MF" {
		pledgee = cfg.MTFPledgeeBOID
	}
	if !reBO16.MatchString(pledgee) || cfg.CMID == "" || cfg.TMID == "" || cfg.ExID == "" || cfg.SegmentID == "" {
		return fail("Pledges through CDSL are not set up yet. Please contact us.", invalid("CDSL pledge settings are incomplete"))
	}
	if cc.DP8 != "" && !strings.HasPrefix(bo.BOID, cc.DP8) {
		return fail("Your demat account is not with this DP.", invalid("BO is not under the DP ID"))
	}
	_, look, _ := o.Market(ctx, tenant, now.Format("2006-01-02"))
	reqID, err := o.cplReqID(ctx, tenant, "LL", 24)
	if err != nil {
		return fail("Something went wrong; please try again.", err)
	}
	today := now.Format("2006-01-02")
	d := cplMPDetails{PledgeIdentifier: kind, ReqTime: cplTime(now), PledgorBOID: bo.BOID, PledgeeBOID: pledgee, ExecutionDate: cplDate(today),
		ExpiryDate: cplDate(addDays(today, cfg.ExpiryDays)), UCCID: truncate(c.UCC, 11), ExID: cfg.ExID, EntityIdentifier: cfg.Entity, TMID: cfg.TMID,
		Remarks: "Margin pledge", ReturnURL: strings.TrimRight(base, "/") + "/hooks/" + tenant + "/cdsl/pledge-return"}
	call := CDSLCall{ReqID: reqID, Kind: kind, UCC: tk.UCC, At: now.Format(time.RFC3339), Status: "Sent"}
	for _, p := range batch {
		isinReq, err := o.cplReqID(ctx, tenant, "LI", 20)
		if err != nil {
			return fail("Something went wrong; please try again.", err)
		}
		px := money.Dec{}
		if i, ok := look.ByISIN(p.ISIN); ok {
			px = firstDec(i.LTP, i.Close)
		}
		if px.Sign() <= 0 {
			return fail("There is no price for "+p.Symbol+" today; please try again after the market opens.", invalid("no price for %s", p.ISIN))
		}
		d.ISINs = append(d.ISINs, cplISIN{PRFNumber: strings.ReplaceAll(p.ID, "-", ""), PledgorIntRef: strings.ReplaceAll(p.ID, "-", ""), ISINReqID: isinReq, ISIN: p.ISIN,
			Quantity: cplQty(p.Qty), Value: px.MulInt(p.Qty).Fixed(2), SegmentID: cfg.SegmentID, CMID: cfg.CMID, ReasonCode: cfg.ReasonCode})
		call.IDs, call.ISINReq = append(call.IDs, p.ID), append(call.ISINReq, isinReq)
		pg.Lines = append(pg.Lines, fmt.Sprintf("%d %s (%s)", p.Qty, p.Symbol, p.ISIN))
	}
	plain, _ := json.Marshal(d)
	enc, err := cplEncrypt(cc.key, plain)
	if err != nil {
		return fail("Something went wrong; please try again.", err)
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cCPLReq, reqID, call, SystemActor); err != nil {
		return fail("Something went wrong; please try again.", err)
	}
	for i, p := range batch {
		_, _ = docs.Update(ctx, o.Docs, tenant, cPledge, p.ID, SystemActor, func(x *PledgeRequest) error {
			x.ReqID, x.ISINReqID, x.SentAt = reqID, call.ISINReq[i], call.At
			return nil
		})
	}
	o.log(ctx, tenant, SystemActor, "cdsl.pledge.sent", reqID, map[string]any{"bo": maskBO(bo.BOID), "kind": kind, "requests": call.IDs})
	pg.Name = strings.Fields(c.Name + " x")[0]
	pg.Action = cc.url(kind)
	pg.Fields = []CDSLFormField{{"dpid", cc.DPID}, {"reqid", reqID}, {"version", cplVersion}, {"pledgedtls", enc}}
	return pg, nil
}

func firstDec(a, b money.Dec) money.Dec {
	if a.Sign() > 0 {
		return a
	}
	return b
}

// cplMPDetails is "pledgedtls" for margin and normal pledge (§5.16, §5.19).
type cplMPDetails struct {
	PledgeIdentifier string    `json:"pledgeidentifier"`
	ReqTime          string    `json:"reqtime"`
	PledgorBOID      string    `json:"pledgorboid"`
	PledgeeBOID      string    `json:"pledgeeboid"`
	ExecutionDate    string    `json:"executiondate"`
	ExpiryDate       string    `json:"expirydate"`
	UCCID            string    `json:"uccid"`
	ExID             string    `json:"exid"`
	EntityIdentifier string    `json:"entityidentifier"`
	TMID             string    `json:"tmid"`
	Remarks          string    `json:"remarks"`
	ReturnURL        string    `json:"returnurl"`
	ISINs            []cplISIN `json:"isindtls"`
}

type cplISIN struct {
	PRFNumber     string `json:"prfnumber"`
	PledgorIntRef string `json:"pledgorintref"`
	ISINReqID     string `json:"isinreqid"`
	ISIN          string `json:"isin"`
	Quantity      string `json:"quantity"`
	Value         string `json:"value"`
	SegmentID     string `json:"segmentid"`
	CMID          string `json:"cmid"`
	ReasonCode    string `json:"reasoncode"`
}

// cplPledgeRes is the pledge reply (setup, inquiry, normal pledge,
// unpledge and confiscate), keys normalised.
type cplPledgeRes struct {
	ReqID        string         `json:"reqid"`
	ResTime      string         `json:"restime"`
	Identifier   string         `json:"pledgeidentifier"`
	PledgorBO    string         `json:"pledgorbo"`
	ReqStatus    string         `json:"reqstatus"`
	ResStatus    string         `json:"resstatus"`
	ResError     string         `json:"reserror"`
	ResErrMsg    string         `json:"reserrmsg"`
	ReqErrorCode string         `json:"reqerrorcode"`
	ReqErrorMsg  string         `json:"reqerrormsg"`
	A            []cplISINReply `json:"pledgeresdtls"`
	B            []cplISINReply `json:"isinresdtls"`
	C            []cplISINReply `json:"isindtls"`
}

type cplISINReply struct {
	ISINReqID string `json:"isinreqid"`
	ISINResID string `json:"isinresid"`
	ISIN      string `json:"isin"`
	ShortName string `json:"isinshortname"`
	Quantity  string `json:"quantity"`
	PSN       string `json:"psn"`
	PSNStatus string `json:"psnstatus"`
	ReqStatus string `json:"reqstatus"`
	ErrorCode string `json:"errorcode"`
	ErrorMsg  string `json:"errormsg"`
}

func (r cplPledgeRes) lines() []cplISINReply { return append(append(append([]cplISINReply{}, r.A...), r.B...), r.C...) }

// failed says whether the whole request failed, and why.
func (r cplPledgeRes) failed() (bool, string, string) {
	st := strings.TrimSpace(firstOf(r.ReqStatus, r.ResStatus))
	code, msg := strings.TrimSpace(firstOf(r.ResError, r.ReqErrorCode)), strings.TrimSpace(firstOf(r.ResErrMsg, r.ReqErrorMsg))
	return st == "1" || (len(r.lines()) == 0 && (st != "0" || code != "")), code, msg
}

func cplParsePledge(p []byte) (cplPledgeRes, error) {
	var r cplPledgeRes
	err := json.Unmarshal(p, &r)
	return r, err
}

// CDSLPledgeReturn takes CDSL's post back after the client's OTP (the
// "returnurl"). The reply is trusted only if it decrypts with the firm's
// API key, its reqid is one Ledgerline sent and is still open, and the
// pledgor BO is the client's.
func (o *Ops) CDSLPledgeReturn(ctx context.Context, tenant, base string, form url.Values) (CDSLPledgePage, error) {
	set, _ := o.Settings(ctx, tenant)
	pg := CDSLPledgePage{Firm: set.Firm, Back: strings.TrimRight(base, "/") + "/#ppledge"}
	reqID := strings.TrimSpace(form.Get("reqid"))
	call, _, err := docs.Get[CDSLCall](ctx, o.Docs, tenant, cCPLReq, reqID)
	if reqID == "" || err != nil || (call.Kind != "MP" && call.Kind != "MF") {
		pg.Error = "We could not match CDSL's reply to a pledge request. If you confirmed the pledge, it will show in the client portal shortly."
		o.log(ctx, tenant, SystemActor, "cdsl.pledge.return_unknown", truncate(reqID, 30), nil)
		return pg, ErrNotFound
	}
	cc, err := o.cplConnection(ctx, tenant, "pledge")
	if err != nil {
		pg.Error = "Your pledge result will show in the client portal shortly."
		return pg, err
	}
	p, err := cplOpen(cc.key, []byte(form.Encode()), "pledgeresdtls")
	if err != nil {
		o.log(ctx, tenant, SystemActor, "cdsl.pledge.return_rejected", reqID, map[string]any{"why": "did not decrypt"})
		pg.Error = "CDSL's reply could not be read. If you confirmed the pledge, it will show in the client portal shortly."
		return pg, err
	}
	res, err := cplParsePledge(p)
	if err != nil || (res.ReqID != "" && res.ReqID != reqID) {
		o.log(ctx, tenant, SystemActor, "cdsl.pledge.return_rejected", reqID, map[string]any{"why": "reqid differs"})
		pg.Error = "CDSL's reply did not match your request."
		return pg, ErrInvalid
	}
	done, failed, why := o.cplApplyMP(ctx, tenant, call, res, "return")
	switch {
	case done > 0 && failed == 0:
		pg.Done = "CDSL has set up your pledge."
	case done > 0:
		pg.Done = fmt.Sprintf("CDSL set up %d of the %d pledges; the rest were not: %s", done, done+failed, why)
	case failed > 0:
		pg.Done = "CDSL did not set up the pledge: " + firstOf(why, "the request was not completed") + "."
	default:
		pg.Done = "Your pledge result will show in the client portal shortly."
	}
	for _, id := range call.IDs {
		if x, _, err := docs.Get[PledgeRequest](ctx, o.Docs, tenant, cPledge, id); err == nil {
			pg.Lines = append(pg.Lines, fmt.Sprintf("%d %s: %s%s", x.Qty, x.Symbol, x.Status, map[bool]string{true: " (PSN " + x.PSN + ")", false: ""}[x.PSN != ""]))
		}
	}
	return pg, nil
}

// cplApplyMP records a margin pledge reply on the requests it carried.
// Requests already settled are left alone, so a reply seen twice (return
// and inquiry) counts once.
func (o *Ops) cplApplyMP(ctx context.Context, tenant string, call CDSLCall, res cplPledgeRes, how string) (done, failed int, why string) {
	allFail, code, msg := res.failed()
	byReq := map[string]cplISINReply{}
	for _, l := range res.lines() {
		byReq[l.ISINReqID] = l
	}
	now := o.now().Format(time.RFC3339)
	for i, id := range call.IDs {
		p, _, err := docs.Get[PledgeRequest](ctx, o.Docs, tenant, cPledge, id)
		if err != nil || p.Status != "Awaiting client" || p.ReqID != call.ReqID {
			continue
		}
		l, have := byReq[call.ISINReq[i]]
		ok := have && strings.TrimSpace(l.ReqStatus) == "0" && strings.TrimSpace(l.PSN) != ""
		if !ok && !allFail && !have {
			continue // not in this reply
		}
		if ok {
			if _, err := o.adjustHolding(ctx, tenant, SystemActor, p.UCC, p.ISIN, p.Symbol, 0, p.Qty); err != nil {
				o.raiseWork(ctx, tenant, "cdslpledge", p.ID+"-holding", "Pledge "+p.ID+" set up at CDSL but not in holdings", fmt.Sprintf("CDSL set up PSN %s for %d %s, but Ledgerline's holdings do not allow it (%v). Load the day's CDSL holding file.", l.PSN, p.Qty, p.Symbol, err), p.UCC, RoleRisk, "cdslpledge", "")
			}
			p, _ = docs.Update(ctx, o.Docs, tenant, cPledge, id, SystemActor, func(x *PledgeRequest) error {
				x.Status, x.PSN, x.PSNStatus, x.DoneAt, x.ErrCode, x.ErrMsg = "Accepted", strings.TrimSpace(l.PSN), strings.TrimSpace(l.PSNStatus), now, "", ""
				return nil
			})
			if p.Kind == "MF" && p.MTFID != "" {
				_, _ = docs.Update(ctx, o.Docs, tenant, cMTFPos, p.MTFID, SystemActor, func(m *MTFPosition) error {
					m.Pledge, m.PledgeRef = "Pledged", p.PSN
					return nil
				})
			}
			o.notify(ctx, tenant, SystemActor, "cdsl.pledge_done", p.UCC, map[string]string{"ref": p.ID, "scrips": fmt.Sprintf("%d %s", p.Qty, p.Symbol), "psn": p.PSN})
			o.log(ctx, tenant, SystemActor, "cdsl.pledge.accepted", p.ID, map[string]any{"psn": p.PSN, "qty": p.Qty, "via": how})
			done++
			continue
		}
		ec, em := firstOf(strings.TrimSpace(l.ErrorCode), code), firstOf(strings.TrimSpace(l.ErrorMsg), msg)
		why = firstOf(em, ec)
		_, _ = docs.Update(ctx, o.Docs, tenant, cPledge, id, SystemActor, func(x *PledgeRequest) error {
			x.Status, x.ErrCode, x.ErrMsg, x.Reason, x.DoneAt = "Failed", ec, em, firstOf(em, "not set up by CDSL"), now
			return nil
		})
		o.notify(ctx, tenant, SystemActor, "cdsl.pledge_failed", p.UCC, map[string]string{"ref": p.ID, "scrips": fmt.Sprintf("%d %s", p.Qty, p.Symbol), "reason": firstOf(em, "the request was not completed")})
		o.log(ctx, tenant, SystemActor, "cdsl.pledge.failed", p.ID, map[string]any{"code": ec, "via": how})
		failed++
	}
	if done+failed > 0 {
		_, _ = docs.Update(ctx, o.Docs, tenant, cCPLReq, call.ReqID, SystemActor, func(c *CDSLCall) error {
			c.Status, c.Result = "Done", fmt.Sprintf("%d set up, %d not", done, failed)
			return nil
		})
	}
	return done, failed, why
}

// InquireMarginPledge asks CDSL about a margin pledge request (§5.17) and
// records what it says.
func (o *Ops) InquireMarginPledge(ctx context.Context, tenant, actor, reqID string) (CDSLCall, error) {
	if actor != SystemActor {
		if err := o.require(ctx, tenant, actor, "Asking CDSL about pledges", RoleMaker, RoleChecker, RoleRisk); err != nil {
			return CDSLCall{}, err
		}
	}
	call, _, err := docs.Get[CDSLCall](ctx, o.Docs, tenant, cCPLReq, reqID)
	if err != nil || (call.Kind != "MP" && call.Kind != "MF") {
		return call, ErrNotFound
	}
	cc, err := o.cplConnection(ctx, tenant, "pledge")
	if err != nil {
		return call, err
	}
	now := o.now()
	seq, err := o.cplReqID(ctx, tenant, "LQ", 24)
	if err != nil {
		return call, err
	}
	body, _ := json.Marshal(struct {
		ReqID       string `json:"reqid"`
		ReqDateTime string `json:"reqdatetime"`
	}{reqID, cplTime(now)})
	enc, err := cplEncrypt(cc.key, body)
	if err != nil {
		return call, err
	}
	call.LastInquiry, call.Inquiries = now.Format(time.RFC3339), call.Inquiries+1
	r, herr := o.cplPostJSON(ctx, cc, "MPInquiry", seq, enc)
	var res cplPledgeRes
	if herr == nil {
		p, err := cplOpen(cc.key, r.Body, "encryptresdtl", "mpsetupresdtls", "encryptreqdtl", "data")
		if err == nil {
			res, err = cplParsePledge(p)
		}
		herr = err
	}
	if herr != nil {
		_ = docs.Upsert(ctx, o.Docs, tenant, cCPLReq, reqID, call, SystemActor)
		o.log(ctx, tenant, actor, "cdsl.pledge.inquiry_error", reqID, map[string]any{"error": herr.Error()})
		return call, fmt.Errorf("%w: %v", ErrState, herr)
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cCPLReq, reqID, call, SystemActor)
	if fail, code, _ := res.failed(); fail && (code == "ERR221" || code == "ERR216") {
		// CDSL has nothing: the client did not finish the OTP. After half
		// an hour the requests wait for the client again.
		if at, err := time.Parse(time.RFC3339, call.At); err == nil && now.Sub(at) > 30*time.Minute {
			o.cplAbandon(ctx, tenant, call)
		}
		return docs.Get[CDSLCall](ctx, o.Docs, tenant, cCPLReq, reqID) //nolint
	}
	o.cplApplyMP(ctx, tenant, call, res, "inquiry")
	c, _, _ := docs.Get[CDSLCall](ctx, o.Docs, tenant, cCPLReq, reqID)
	return c, nil
}

// cplAbandon puts the requests of a hand-over the client never finished
// back to waiting for the client.
func (o *Ops) cplAbandon(ctx context.Context, tenant string, call CDSLCall) {
	for _, id := range call.IDs {
		_, _ = docs.Update(ctx, o.Docs, tenant, cPledge, id, SystemActor, func(x *PledgeRequest) error {
			if x.Status == "Awaiting client" && x.ReqID == call.ReqID {
				x.ReqID, x.ISINReqID, x.SentAt = "", "", ""
			}
			return nil
		})
	}
	_, _ = docs.Update(ctx, o.Docs, tenant, cCPLReq, call.ReqID, SystemActor, func(c *CDSLCall) error {
		c.Status, c.Result = "Abandoned", "the client did not complete CDSL's OTP"
		return nil
	})
}

// ---- screens ----

// CDSLPledgeView is the Pledges (CDSL) screen.
type CDSLPledgeView struct {
	Date        string                `json:"date"`
	Connected   bool                  `json:"connected"`
	Problem     string                `json:"problem,omitempty"`
	Env         string                `json:"env,omitempty"`
	Settings    CDSLPledgeConfig      `json:"settings"`
	Pledges     []PledgeRequest       `json:"pledges"`
	Calls       []CDSLCall            `json:"calls"`
	Repledges   []Repledge            `json:"repledges"`
	Recon       []RepledgeReconRow    `json:"recon"`
	ReconLoaded bool                  `json:"recon_loaded"`
	Normal      []NormalPledge        `json:"normal"`
	Releases    []PledgeRelease       `json:"releases"`
	Candidates  []InvocationCandidate `json:"candidates"`
	Repledgable []PSNAvail            `json:"repledgable"`
}

// CDSLPledges builds the screen.
func (o *Ops) CDSLPledges(ctx context.Context, tenant, date string) (CDSLPledgeView, error) {
	v := CDSLPledgeView{Date: date, Settings: o.CDSLPledgeSettings(ctx, tenant), Pledges: []PledgeRequest{}, Calls: []CDSLCall{}}
	if cc, err := o.cplConnection(ctx, tenant, "pledge"); err != nil {
		v.Problem = err.Error()
	} else {
		v.Connected, v.Env = true, cc.Env
	}
	ps, err := docs.List[PledgeRequest](ctx, o.Docs, tenant, cPledge, "")
	if err != nil {
		return v, err
	}
	for _, p := range ps {
		if p.Via == cplVia {
			v.Pledges = append(v.Pledges, p)
		}
	}
	sort.SliceStable(v.Pledges, func(i, j int) bool { return v.Pledges[i].Time > v.Pledges[j].Time })
	cs, _ := docs.List[CDSLCall](ctx, o.Docs, tenant, cCPLReq, "")
	sort.Slice(cs, func(i, j int) bool { return cs[i].At > cs[j].At })
	if len(cs) > 200 {
		cs = cs[:200]
	}
	v.Calls = append(v.Calls, cs...)
	v.Repledges = o.cplRepledges(ctx, tenant)
	v.Recon, v.ReconLoaded = o.RepledgeRecon(ctx, tenant, date)
	v.Normal = o.cplNormals(ctx, tenant)
	v.Releases = o.cplReleases(ctx, tenant)
	v.Candidates = o.InvocationCandidates(ctx, tenant, date)
	v.Repledgable = o.cplRepledgable(ctx, tenant)
	return v, nil
}

// PortalPledgeView is the client's Pledge shares page.
type PortalPledgeView struct {
	UCC       string          `json:"ucc"`
	Connected bool            `json:"connected"`
	Holdings  []PortalPledgeH `json:"holdings"`
	Requests  []PledgeRequest `json:"requests"`
	Releases  []PledgeRelease `json:"releases"`
	Awaiting  int             `json:"awaiting"`
	PSNs      []PSNAvail      `json:"psns"`
}

// PortalPledgeH is a holding with what can be pledged.
type PortalPledgeH struct {
	ISIN    string `json:"isin"`
	Symbol  string `json:"symbol"`
	Qty     int64  `json:"qty"`
	Pledged int64  `json:"pledged"`
	Free    int64  `json:"free"`
}

// PortalPledges builds the client's page.
func (o *Ops) PortalPledges(ctx context.Context, tenant, actor, ucc string) (PortalPledgeView, error) {
	accts, err := o.PortalAccounts(ctx, tenant, actor)
	if err != nil {
		return PortalPledgeView{}, err
	}
	ucc = strings.ToUpper(strings.TrimSpace(ucc))
	if ucc == "" && len(accts) > 0 {
		ucc = accts[0]
	}
	if !contains(accts, ucc) {
		return PortalPledgeView{}, ErrNotFound
	}
	v := PortalPledgeView{UCC: ucc, Holdings: []PortalPledgeH{}, Requests: []PledgeRequest{}, Releases: []PledgeRelease{}, PSNs: []PSNAvail{}}
	_, err = o.cplConnection(ctx, tenant, "pledge")
	v.Connected = err == nil
	hs, _ := o.Holdings(ctx, tenant, ucc)
	for _, h := range hs {
		if h.Qty <= 0 {
			continue
		}
		_, free := o.cplFree(ctx, tenant, ucc, h.ISIN, "")
		v.Holdings = append(v.Holdings, PortalPledgeH{ISIN: h.ISIN, Symbol: h.Symbol, Qty: h.Qty, Pledged: h.Pledged, Free: max(free, 0)})
	}
	ps, _ := docs.List[PledgeRequest](ctx, o.Docs, tenant, cPledge, "")
	for _, p := range ps {
		if p.UCC == ucc && p.Via == cplVia {
			p.RequestedBy, p.DecidedBy = "", ""
			v.Requests = append(v.Requests, p)
			if p.Status == "Awaiting client" {
				v.Awaiting++
			}
		}
	}
	sort.SliceStable(v.Requests, func(i, j int) bool { return v.Requests[i].Time > v.Requests[j].Time })
	for _, r := range o.cplReleases(ctx, tenant) {
		if r.UCC == ucc && r.Kind == "UN" {
			r.RequestedBy, r.ApprovedBy = "", ""
			v.Releases = append(v.Releases, r)
		}
	}
	for _, a := range o.cplPSNs(ctx, tenant, ucc) {
		if a.Of == "MP" && a.Free > 0 {
			v.PSNs = append(v.PSNs, a)
		}
	}
	return v, nil
}

// ---- the scheduler ----

// cdslPledgeTick follows up the CDSL pledge and early pay-in APIs:
// hand-overs whose result did not come back are asked about; approved
// requests the client never confirmed lapse after 7 days; invocations
// approved and past their notice go to CDSL; early pay-ins with no answer
// are asked about.
func (o *Ops) cdslPledgeTick(ctx context.Context, tenant string, now time.Time) {
	if _, err := o.cplConnection(ctx, tenant, "pledge"); err == nil {
		cs, _ := docs.List[CDSLCall](ctx, o.Docs, tenant, cCPLReq, "")
		for _, c := range cs {
			if (c.Kind != "MP" && c.Kind != "MF") || c.Status != "Sent" {
				continue
			}
			at, err := time.Parse(time.RFC3339, c.At)
			if err != nil || now.Sub(at) < 3*time.Minute {
				continue
			}
			if now.Sub(at) > 72*time.Hour {
				o.cplAbandon(ctx, tenant, c)
				continue
			}
			if li, err := time.Parse(time.RFC3339, c.LastInquiry); err == nil && now.Sub(li) < 5*time.Minute {
				continue
			}
			_, _ = o.InquireMarginPledge(ctx, tenant, SystemActor, c.ReqID)
		}
		ps, _ := docs.List[PledgeRequest](ctx, o.Docs, tenant, cPledge, "")
		for _, p := range ps {
			if p.Via != cplVia || p.Status != "Awaiting client" || p.ReqID != "" {
				continue
			}
			if t, err := time.ParseInLocation("2006-01-02 15:04", p.Time, ist); err == nil && now.Sub(t) > 7*24*time.Hour {
				_, _ = docs.Update(ctx, o.Docs, tenant, cPledge, p.ID, SystemActor, func(x *PledgeRequest) error {
					x.Status, x.Reason = "Failed", "not confirmed by the client within 7 days"
					return nil
				})
			}
		}
		o.cplReleaseTick(ctx, tenant, now)
	}
	if _, err := o.cplConnection(ctx, tenant, "early_payin"); err == nil {
		o.epiTick(ctx, tenant, now)
	}
}
