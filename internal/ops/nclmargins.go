package ops

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// ---- NSE Clearing margins API (NMASS Margins, FO / CD / CO) ----
//
// NSE Clearing's margin API (protocol v1.2, circular NCL/CMPT/76433) gives a
// clearing member, during the day, the margins NSE Clearing itself is
// charging: for the clearing member, for each trading member under it and
// for each client, plus client positions, member open interest and the
// market-wide position limits. Live RMS uses the client margins in place of
// Ledgerline's own estimate whenever NSE Clearing's figure is higher.
//
// Logging in: Basic consumer key and secret; NSE Clearing answers with the
// access token, an AES key and IV, encrypted (RSA PKCS#1 v1.5) to the
// certificate the member uploaded on NMASS (API Access Management, file
// RSA_<member>.pem). Every later call sends the token, and the request,
// encrypted with that AES-256-CBC key; the reply's data comes back the same
// way. Ledgerline can make the key pair itself (Connections → NSE Clearing
// margins API → Make key), keeping the private key in the vault and giving
// the firm the certificate to upload.
//
// Limits: one login a minute; one call every 10 seconds per endpoint, every
// 300 seconds for the whole-book file requests. A message number
// (member + date + 7-digit daily count) is never used twice.

const (
	connNCLMargins = "ncl-margins"
	cNCLMState     = "nclmstate" // key: segment
	cNCLMSnap      = "nclmsnap"  // key: seg|date|kind (cm, tm, moi, mwpl)
	cNCLMCli       = "nclmcli"   // key: seg|date|ucc
	cNCLMPos       = "nclmpos"   // key: seg|date|ucc

	nclmLive = "https://www.connect2nsccl.com"
	nclmUAT  = "https://uat.connect2nsccl.com"
	// nclmCliMax is the most client codes one cli-margins call may carry.
	nclmCliMax = 50000
)

// NCLMKinds are the data calls, with their endpoints and seconds between calls.
var NCLMKinds = map[string]struct {
	Path, Inquiry, Label string
	Every                time.Duration
}{
	"cm":            {"cm-margins", "", "Clearing member margin", 10 * time.Second},
	"tm":            {"tm-margins", "", "Trading member margins", 10 * time.Second},
	"cli":           {"cli-margins", "", "Client margins", 10 * time.Second},
	"positions":     {"positions", "", "Client positions", 10 * time.Second},
	"moi":           {"moi", "", "Member open interest", 10 * time.Second},
	"mwpl":          {"mwpl", "", "Market-wide position limits", 10 * time.Second},
	"cli-all":       {"cli-margins/generate-all", "cli-margins/inquiry", "All client margins (file)", 300 * time.Second},
	"positions-all": {"positions/generate-all", "positions/inquiry", "All client positions (file)", 300 * time.Second},
}

// nclmColumns name the fields of each record, in NSE Clearing's order
// (fillers left out). The meaning of a few differs by segment, as noted.
var nclmColumns = map[string][]string{
	"cm": {"CM code", "Cash collateral", "Non-cash collateral", "Effective non-cash", "Effective collateral", "Prop margin", "TM margin above 90%", "Total margin",
		"MLNW", "Utilisation %", "Initial margin (prop)", "COBG (prop); CO: ICMTM", "Delivery margin (prop); CO: additional margin", "Extreme loss margin", "MTM profit/loss (prop)"},
	"tm": {"TM/CP code", "TM/CP name", "Cash collateral", "Non-cash collateral", "Effective deposit", "Prop margin", "Client margin above 90%", "Non-cash benefit TM",
		"Non-cash benefit CM", "Total margin", "Utilisation %", "TM margin above 90%", "Intraday uncrystallised MTM loss (FO)", "Initial margin (prop)",
		"COBG (prop); CO: ICMTM", "Delivery margin (prop); CO: additional margin", "Extreme loss margin (prop)"},
	"moi":  {"TM/CP code", "Symbol", "Instrument type", "NSE Clearing open interest", "Limit", "Percentage"},
	"mwpl": {"Symbol", "NSE Clearing FUTEQ OI", "Limit", "FUTEQ OI %"},
}

func init() {
	ConnProviders = append(ConnProviders, ConnProvider{ID: connNCLMargins, Name: "NSE Clearing margins API", Group: "Exchanges", Test: "https",
		Purpose: "Margins NSE Clearing charges during the day (clearing member, trading members, each client), client positions, open interest and MWPL, for F&O, currency and commodity. Register on NMASS → API Access Management (mail risk_ops@nsccl.co.in first)",
		Fields: []ConnField{{Key: "member", Label: "Clearing member code"}, {Key: "api_key", Label: "Consumer key (from NSE Clearing's email, after the certificate is approved)", Secret: true, Optional: true},
			{Key: "secret", Label: "Consumer secret", Secret: true, Optional: true},
			{Key: "private_key", Label: "Private key (PEM) for the certificate uploaded to NMASS; leave empty if Ledgerline made the key", Secret: true, Optional: true},
			{Key: "segments", Label: "Segments", Options: []string{"FO", "FO, CD", "FO, CO", "FO, CD, CO", "CD", "CO", "CD, CO"}},
			{Key: "env", Label: "Environment", Options: []string{"Live", "UAT"}},
			{Key: "fetch", Label: "Fetch during the day", Options: []string{"On", "Off"}},
			{Key: "url", Label: "Address (leave empty for NSE Clearing's)", Optional: true, Placeholder: nclmLive}, fNotes}})
}

// NCLMState is where a segment's margin feed stands.
type NCLMState struct {
	Segment   string            `json:"segment"`
	MsgDate   string            `json:"msg_date"`
	MsgSeq    int               `json:"msg_seq"`
	Token     string            `json:"token,omitempty"`
	Key       string            `json:"key,omitempty"` // base64 AES key and IV for this token
	IV        string            `json:"iv,omitempty"`
	TokenExp  string            `json:"token_exp,omitempty"`
	Pending   map[string]string `json:"pending,omitempty"` // whole-book file asked for: kind -> message number
	Last      map[string]string `json:"last,omitempty"`    // kind -> when it last came back
	Rows      map[string]int    `json:"rows,omitempty"`    // kind -> rows last received
	LastError string            `json:"last_error,omitempty"`
}

// NCLMClient is NSE Clearing's margin for one client in one segment.
type NCLMClient struct {
	Segment         string    `json:"segment"`
	Date            string    `json:"date"`
	At              string    `json:"at"`
	UCC             string    `json:"ucc"`
	TM              string    `json:"tm,omitempty"`
	CashCollateral  money.Dec `json:"cash_collateral"`
	NonCash         money.Dec `json:"non_cash_collateral"`
	Initial         money.Dec `json:"initial_margin"`
	COBG            money.Dec `json:"cobg"`     // CO: ICMTM
	Delivery        money.Dec `json:"delivery"` // CO: additional margin
	ExtremeLoss     money.Dec `json:"extreme_loss"`
	Total           money.Dec `json:"total"`
	MTM             money.Dec `json:"mtm"`
	Excess90        money.Dec `json:"above_90"`
	ExcessNonCash90 money.Dec `json:"non_cash_above_90"`
	EligibleNonCash money.Dec `json:"eligible_non_cash"`
}

// NCLMPosition is one open position NSE Clearing holds for a client.
type NCLMPosition struct {
	TM         string    `json:"tm"`
	Instrument string    `json:"instrument"` // FUTSTK, OPTIDX…
	Symbol     string    `json:"symbol"`
	Expiry     string    `json:"expiry"` // YYYY-MM-DD
	Strike     money.Dec `json:"strike"`
	OptType    string    `json:"opt_type"`
	Qty        money.Dec `json:"qty"`
}

// NCLMPositions are a client's positions in one segment.
type NCLMPositions struct {
	Segment   string         `json:"segment"`
	Date      string         `json:"date"`
	At        string         `json:"at"`
	UCC       string         `json:"ucc"`
	Positions []NCLMPosition `json:"positions"`
}

// NCLMSnapshot is the latest reply of a whole-member call.
type NCLMSnapshot struct {
	Segment string     `json:"segment"`
	Date    string     `json:"date"`
	Kind    string     `json:"kind"`
	At      string     `json:"at"`
	Columns []string   `json:"columns"`
	Rows    [][]string `json:"rows"`
}

// NCLMarginsAPI is NSE Clearing's side, replaced in tests.
type NCLMarginsAPI interface {
	Post(ctx context.Context, url string, h http.Header, body []byte) (int, http.Header, []byte, error)
}

type nclmHTTP struct{ hc *http.Client }

// NSE Clearing wants Accept-Encoding present and blank, so Go's own gzip
// request is switched off.
var nclmClient = &http.Client{Timeout: 2 * time.Minute, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, DisableCompression: true, TLSHandshakeTimeout: 20 * time.Second}}

func (h nclmHTTP) Post(ctx context.Context, u string, hd http.Header, body []byte) (int, http.Header, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return 0, nil, nil, err
	}
	for k, v := range hd {
		req.Header[k] = v
	}
	resp, err := h.hc.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
	return resp.StatusCode, resp.Header, b, err
}

func (o *Ops) nclmAPI() NCLMarginsAPI {
	if o.NCLMargins != nil {
		return o.NCLMargins
	}
	return nclmHTTP{hc: nclmClient}
}

// Calls per consumer key, segment and endpoint, across the whole server.
var (
	nclmMu   sync.Mutex
	nclmLast = map[string]time.Time{}
)

func nclmSlot(key string, every time.Duration, now time.Time) (bool, time.Duration) {
	nclmMu.Lock()
	defer nclmMu.Unlock()
	if t, ok := nclmLast[key]; ok && now.Sub(t) < every {
		return false, every - now.Sub(t)
	}
	nclmLast[key] = now
	return true, 0
}

type nclmConn struct {
	base, member, key, secret string
	priv                      *rsa.PrivateKey
	segments                  []string
	fetch                     bool
}

func nclmSegments(s string) []string {
	var out []string
	for _, p := range strings.Split(firstOf(s, "FO"), ",") {
		if p = strings.ToUpper(strings.TrimSpace(p)); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func parseNCLMConn(c Connection) (nclmConn, error) {
	n := nclmConn{member: strings.ToUpper(strings.TrimSpace(c.Values["member"])), key: c.Secrets["api_key"], secret: c.Secrets["secret"],
		segments: nclmSegments(c.Values["segments"]), fetch: c.Values["fetch"] == "On"}
	if l := len(n.member); l < 4 || l > 5 || n.key == "" || n.secret == "" {
		return n, invalid("the NSE Clearing margins connection needs the 4 or 5 character clearing member code, consumer key and consumer secret")
	}
	if c.Secrets["private_key"] == "" {
		return n, invalid("the NSE Clearing margins connection has no private key: make one with Make key, or paste the key of the certificate uploaded to NMASS")
	}
	priv, err := parseRSAKey(c.Secrets["private_key"])
	if err != nil {
		return n, err
	}
	n.priv = priv
	n.base = nclmLive
	if c.Values["env"] == "UAT" {
		n.base = nclmUAT
	}
	if u := strings.TrimRight(c.Values["url"], "/"); u != "" {
		n.base = u
	}
	return n, nil
}

func parseRSAKey(s string) (*rsa.PrivateKey, error) {
	b, _ := pem.Decode([]byte(pemLines(s)))
	if b == nil {
		return nil, invalid("the private key is not in PEM form (-----BEGIN … PRIVATE KEY-----)")
	}
	rk, err := x509.ParsePKCS1PrivateKey(b.Bytes)
	if err != nil {
		k, err := x509.ParsePKCS8PrivateKey(b.Bytes)
		if err != nil {
			return nil, invalid("the private key could not be read: %v", err)
		}
		var ok bool
		if rk, ok = k.(*rsa.PrivateKey); !ok {
			return nil, invalid("the private key must be an RSA key")
		}
	}
	if rk.N.BitLen() < 2048 {
		return nil, invalid("NSE Clearing needs an RSA key of at least 2048 bits")
	}
	return rk, nil
}

// pemLines puts back the line breaks a pasted key may have lost.
func pemLines(s string) string {
	s = strings.TrimSpace(s)
	i, j := strings.Index(s, "-----BEGIN "), strings.Index(s, "-----END ")
	if i < 0 || j < i || strings.Contains(s, "\n") {
		return s
	}
	h := strings.Index(s[i+11:], "-----")
	if h < 0 {
		return s
	}
	head := s[i : i+11+h+5]
	body := strings.Join(strings.Fields(s[len(head)+i:j]), "")
	var b strings.Builder
	b.WriteString(head + "\n")
	for len(body) > 64 {
		b.WriteString(body[:64] + "\n")
		body = body[64:]
	}
	b.WriteString(body + "\n" + s[j:] + "\n")
	return b.String()
}

// ---- encryption ----

// nclmKeyBytes reads the AES key or IV NSE Clearing sends with the token.
// Its encoding is not given in the protocol, so plain text, base64 and hex
// are each tried for the expected length.
func nclmKeyBytes(s string, size int) ([]byte, error) {
	s = strings.TrimSpace(s)
	if len(s) == size {
		return []byte(s), nil
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding} {
		if b, err := enc.DecodeString(s); err == nil && len(b) == size {
			return b, nil
		}
	}
	if b, err := hex.DecodeString(s); err == nil && len(b) == size {
		return b, nil
	}
	return nil, fmt.Errorf("NSE Clearing's AES value is %d characters; expected %d bytes as text, base64 or hex", len(s), size)
}

func aesCBCEncrypt(key, iv, plain []byte) (string, error) {
	blk, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	p := append(append([]byte{}, plain...), bytes.Repeat([]byte{byte(pad)}, pad)...)
	out := make([]byte, len(p))
	cipher.NewCBCEncrypter(blk, iv).CryptBlocks(out, p)
	return base64.StdEncoding.EncodeToString(out), nil
}

func aesCBCDecrypt(key, iv []byte, b64 string) ([]byte, error) {
	c, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return nil, fmt.Errorf("not base64")
	}
	if len(c) == 0 || len(c)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("not a whole number of AES blocks")
	}
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(c))
	cipher.NewCBCDecrypter(blk, iv).CryptBlocks(out, c)
	pad := int(out[len(out)-1])
	if pad < 1 || pad > aes.BlockSize || pad > len(out) || !bytes.Equal(out[len(out)-pad:], bytes.Repeat([]byte{byte(pad)}, pad)) {
		return nil, fmt.Errorf("wrong AES key or padding")
	}
	return out[:len(out)-pad], nil
}

// ---- the replies ----

// nclmEnvelope reads a reply's status, message code and data, whichever of
// the protocol's several shapes it comes in.
type nclmEnvelope struct {
	Status string
	Code   string
	Text   string
	Data   string // encrypted payload, when a string
}

func parseNCLMEnvelope(b []byte) (nclmEnvelope, bool) {
	var raw map[string]json.RawMessage
	if json.Unmarshal(b, &raw) != nil {
		return nclmEnvelope{}, false
	}
	var e nclmEnvelope
	str := func(m json.RawMessage) string {
		var s string
		if json.Unmarshal(m, &s) == nil {
			return s
		}
		var n json.Number
		if json.Unmarshal(m, &n) == nil {
			return n.String()
		}
		return ""
	}
	for k, v := range raw {
		switch strings.ToLower(k) {
		case "status":
			e.Status = str(v)
		case "code":
			e.Code = firstOf(e.Code, str(v))
		case "message", "messages":
			if s := str(v); s != "" {
				e.Text = s
				continue
			}
			var list []string
			if json.Unmarshal(v, &list) == nil {
				e.Text = strings.Join(list, "; ")
				continue
			}
			var obj map[string]json.RawMessage
			if json.Unmarshal(v, &obj) == nil {
				if c, ok := obj["code"]; ok {
					e.Code = str(c)
				}
				for _, k := range []string{"success", "error", "message"} {
					if s, ok := obj[k]; ok {
						e.Text = strings.TrimSpace(str(s))
					}
				}
			}
		case "data":
			if s := str(v); s != "" {
				e.Data = s
				continue
			}
			var obj map[string]json.RawMessage
			if json.Unmarshal(v, &obj) == nil {
				if c, ok := obj["code"]; ok {
					e.Code = str(c)
				}
			}
		}
	}
	return e, true
}

// nclmCodeText says what a message code means (field part, then check).
func nclmCodeText(code string) string {
	if len(code) < 7 {
		return ""
	}
	field := map[string]string{"0100": "", "0101": "", "0102": "message number: ", "0114": "member code: ", "0115": "client list: "}[code[:4]]
	what := map[string]string{"0000": "accepted", "0001": "duplicate message number", "0200": "control and data records do not match", "0201": "too short",
		"0202": "too long", "0204": "missing", "0206": "wrong format", "0241": "system error at NSE Clearing", "0242": "service unavailable",
		"0243": "request not understood", "0244": "no data", "0245": "file still being made", "0246": "more than 50,000 clients", "0247": "too many calls (rate limit)",
		"400": "encryption or request error", "401": "login refused or expired", "500": "server error at NSE Clearing", "503": "service unavailable"}[code[4:]]
	if what == "" {
		return ""
	}
	return field + what
}

func nclmCheck(code string) string {
	if len(code) >= 7 {
		return code[4:]
	}
	return code
}

// nclmRecords splits decrypted data into records and fields: records are
// separated by "^" (or new lines), fields by ",". A JSON list of record
// strings is accepted too.
func nclmRecords(text string) [][]string {
	text = strings.TrimSpace(strings.TrimPrefix(text, "\ufeff"))
	var parts []string
	if strings.HasPrefix(text, "[") {
		var list []string
		if json.Unmarshal([]byte(text), &list) == nil {
			parts = list
		}
	}
	if parts == nil {
		parts = []string{text}
	}
	var out [][]string
	for _, p := range parts {
		for _, r := range strings.FieldsFunc(p, func(c rune) bool { return c == '^' || c == '\n' }) {
			r = strings.TrimSpace(r)
			if r == "" {
				continue
			}
			f := strings.Split(r, ",")
			for i := range f {
				f[i] = strings.TrimSpace(f[i])
			}
			// Trailing fillers are dropped.
			for len(f) > 0 && f[len(f)-1] == "" {
				f = f[:len(f)-1]
			}
			if len(f) > 0 {
				out = append(out, f)
			}
		}
	}
	return out
}

func nclmDec(s string) money.Dec {
	if d, err := money.ParseDec(strings.TrimSpace(s)); err == nil {
		return d
	}
	return money.Dec{}
}

// nclmClients reads client-margin records. A record is cmCode, cmName,
// tmCode, tmName, clientCode and 11 amounts; the protocol's own sample
// gives later records without the first four, which then repeat.
func nclmClients(seg, date, at string, recs [][]string) []NCLMClient {
	var out []NCLMClient
	tm := ""
	for _, f := range recs {
		switch {
		case len(f) >= 16:
			tm, f = f[2], f[4:16]
		case len(f) >= 12:
			f = f[:12]
		default:
			continue
		}
		if f[0] == "" {
			continue
		}
		d := func(i int) money.Dec { return nclmDec(f[i]) }
		out = append(out, NCLMClient{Segment: seg, Date: date, At: at, UCC: strings.ToUpper(f[0]), TM: tm, CashCollateral: d(1), NonCash: d(2), Initial: d(3), COBG: d(4),
			Delivery: d(5), ExtremeLoss: d(6), Total: d(7), MTM: d(8), Excess90: d(9), ExcessNonCash90: d(10), EligibleNonCash: d(11)})
	}
	return out
}

// nclmPositions reads position records: tmCode, clientCode, instrument,
// symbol, expiry (yyyyMMdd), strike, option type, six fillers, open quantity.
func nclmPositions(recs [][]string) map[string][]NCLMPosition {
	out := map[string][]NCLMPosition{}
	for _, f := range recs {
		if len(f) < 7 {
			continue
		}
		p := NCLMPosition{TM: f[0], Instrument: f[2], Symbol: f[3], Strike: nclmDec(f[5]), OptType: f[6]}
		if e := f[4]; len(e) == 8 {
			p.Expiry = e[:4] + "-" + e[4:6] + "-" + e[6:]
		}
		if len(f) > 13 {
			p.Qty = nclmDec(f[13])
		}
		u := strings.ToUpper(f[1])
		out[u] = append(out[u], p)
	}
	return out
}

// ---- calls ----

// NCLMResult is what one fetch did.
type NCLMResult struct {
	Segment string `json:"segment"`
	Kind    string `json:"kind"`
	Status  string `json:"status"` // Received, Requested, In progress, No data
	Rows    int    `json:"rows"`
	MsgID   string `json:"msg_id,omitempty"`
}

var errNCLMWait = errors.New("NSE Clearing limits how often each call may be made")

type nclmSession struct {
	token    string
	key, iv  []byte
	consumer string
}

// nclmLogin returns the held token, or logs in when it has expired.
func (o *Ops) nclmLogin(ctx context.Context, c nclmConn, st *NCLMState, now time.Time) (nclmSession, error) {
	s := nclmSession{consumer: c.key}
	if exp, err := time.Parse(time.RFC3339, st.TokenExp); err == nil && now.Before(exp) && st.Token != "" {
		k, _ := base64.StdEncoding.DecodeString(st.Key)
		iv, _ := base64.StdEncoding.DecodeString(st.IV)
		if len(k) == 32 && len(iv) == 16 {
			s.token, s.key, s.iv = st.Token, k, iv
			return s, nil
		}
	}
	if ok, wait := nclmSlot(c.key+"|"+st.Segment+"|token", time.Minute, now); !ok {
		return s, fmt.Errorf("%w: %v (next login in %ds)", ErrState, errNCLMWait, int(wait.Seconds())+1)
	}
	h := http.Header{}
	h.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(c.key+":"+c.secret)))
	h.Set("Content-Type", "application/x-www-form-urlencoded")
	h.Set("grant_type", "client_credentials")
	nclmHeaders(h, now)
	code, _, b, err := o.nclmAPI().Post(ctx, c.base+"/Margins-"+st.Segment+"-API/v2/request/token", h, []byte(url.Values{"grant_type": {"client_credentials"}}.Encode()))
	if err != nil {
		return s, fmt.Errorf("could not reach NSE Clearing: %v", err)
	}
	env, _ := parseNCLMEnvelope(b)
	if code != 200 || env.Data == "" {
		return s, fmt.Errorf("NSE Clearing refused the login (%d %s)", code, firstOf(nclmCodeText(env.Code), env.Code))
	}
	ct, err := base64.StdEncoding.DecodeString(env.Data)
	if err != nil {
		return s, fmt.Errorf("NSE Clearing's login reply is not base64")
	}
	plain, err := rsa.DecryptPKCS1v15(nil, c.priv, ct)
	if err != nil {
		return s, fmt.Errorf("the login reply could not be decrypted with the private key: is the certificate on NMASS the one for this key?")
	}
	var t map[string]any
	d := json.NewDecoder(bytes.NewReader(plain))
	d.UseNumber()
	if err := d.Decode(&t); err != nil {
		return s, fmt.Errorf("NSE Clearing's login reply is not JSON")
	}
	get := func(k string) string {
		for kk, v := range t {
			if strings.EqualFold(kk, k) {
				return strings.TrimSpace(fmt.Sprint(v))
			}
		}
		return ""
	}
	s.token = get("access_token")
	if s.token == "" {
		return s, fmt.Errorf("no access token in NSE Clearing's login reply")
	}
	if s.key, err = nclmKeyBytes(get("key"), 32); err != nil {
		return s, err
	}
	if s.iv, err = nclmKeyBytes(get("iv"), 16); err != nil {
		return s, err
	}
	secs, _ := strconv.Atoi(get("expires_in"))
	if secs <= 0 {
		secs = 3600
	}
	st.Token, st.Key, st.IV = s.token, base64.StdEncoding.EncodeToString(s.key), base64.StdEncoding.EncodeToString(s.iv)
	st.TokenExp = now.Add(time.Duration(max(secs-120, 30)) * time.Second).Format(time.RFC3339)
	return s, nil
}

func nclmHeaders(h http.Header, now time.Time) {
	h.Set("nonce", notisNonce(now))
	h.Set("Accept", "application/json")
	h.Set("User-Agent", "/")
	h["Accept-Encoding"] = []string{""}
}

// nclmCall sends one encrypted request and returns the decrypted data text,
// or the raw file for an inquiry.
func (o *Ops) nclmCall(ctx context.Context, c nclmConn, s nclmSession, seg, path string, data map[string]any, now time.Time, file bool) (string, string, error) {
	plain, _ := json.Marshal(map[string]any{"version": "2.0", "data": data})
	enc, err := aesCBCEncrypt(s.key, s.iv, plain)
	if err != nil {
		return "", "", err
	}
	bearer, _ := aesCBCEncrypt(s.key, s.iv, []byte(s.token))
	body, _ := json.Marshal(map[string]string{"data": enc})
	h := http.Header{}
	h.Set("Authorization", "Bearer "+bearer)
	h["consumerKey"] = []string{s.consumer} // exactly as named in the protocol
	h.Set("Content-Type", "application/json")
	nclmHeaders(h, now)
	code, rh, b, err := o.nclmAPI().Post(ctx, c.base+"/Margins-"+seg+"-API/v2/request/"+path, h, body)
	if err != nil {
		return "", "", fmt.Errorf("could not reach NSE Clearing: %v", err)
	}
	env, isJSON := parseNCLMEnvelope(b)
	if code == 401 {
		return "", env.Code, errNCLMLogin
	}
	if code != 200 && len(env.Code) == 8 { // a check on the message, not an HTTP failure
		return "", env.Code, nclmCodeErr{env.Code}
	}
	if code != 200 {
		return "", env.Code, fmt.Errorf("NSE Clearing answered %d: %s", code, firstOf(nclmCodeText(env.Code), firstOf(env.Text, trimBody(b))))
	}
	if file && !isJSON {
		text, err := nclmFile(s, rh.Get("Content-Type"), b)
		return text, "", err
	}
	if !isJSON {
		return "", "", fmt.Errorf("NSE Clearing's reply is not JSON: %s", trimBody(b))
	}
	if chk := nclmCheck(env.Code); chk != "" && chk != "0000" {
		return "", env.Code, nclmCodeErr{env.Code}
	}
	if env.Status != "" && !strings.EqualFold(env.Status, "success") {
		return "", env.Code, fmt.Errorf("NSE Clearing answered %s %s", env.Status, firstOf(nclmCodeText(env.Code), env.Text))
	}
	if env.Data == "" {
		return "", env.Code, nil
	}
	if p, err := aesCBCDecrypt(s.key, s.iv, env.Data); err == nil {
		return string(p), env.Code, nil
	}
	if strings.Contains(env.Data, ",") { // already plain text
		return env.Data, env.Code, nil
	}
	return "", env.Code, fmt.Errorf("NSE Clearing's reply could not be decrypted with this login's key")
}

var errNCLMLogin = errors.New("NSE Clearing did not accept the login token; logging in again next time")

type nclmCodeErr struct{ code string }

func (e nclmCodeErr) Error() string {
	return "NSE Clearing answered " + firstOf(nclmCodeText(e.code), "code "+e.code)
}

// nclmFile reads an inquiry's file: one part of a multipart reply, or the
// body itself, possibly encrypted with the login's key.
func nclmFile(s nclmSession, ctype string, b []byte) (string, error) {
	if mt, params, err := mime.ParseMediaType(ctype); err == nil && strings.HasPrefix(mt, "multipart/") {
		r := multipart.NewReader(bytes.NewReader(b), params["boundary"])
		var all bytes.Buffer
		for {
			p, err := r.NextPart()
			if err != nil {
				break
			}
			x, _ := io.ReadAll(io.LimitReader(p, 256<<20))
			all.Write(x)
			all.WriteByte('\n')
		}
		b = all.Bytes()
	}
	if p, err := aesCBCDecrypt(s.key, s.iv, string(b)); err == nil {
		return string(p), nil
	}
	if bytes.HasPrefix(b, []byte{0x1f, 0x8b}) || bytes.HasPrefix(b, []byte("PK")) {
		return "", fmt.Errorf("NSE Clearing sent a compressed file; its format is not in the protocol yet")
	}
	return string(b), nil
}

// NCLMarginsFetch makes one call for a segment. For cli and positions,
// uccs are the clients to ask about (all active clients in the segment when
// empty). The whole-book kinds (cli-all, positions-all) first ask NSE
// Clearing to make the file, then fetch it on a later call.
func (o *Ops) NCLMarginsFetch(ctx context.Context, tenant, actor, seg, kind string, uccs []string) (NCLMResult, error) {
	seg, kind = strings.ToUpper(strings.TrimSpace(seg)), strings.ToLower(strings.TrimSpace(kind))
	res := NCLMResult{Segment: seg, Kind: kind}
	k, ok := NCLMKinds[kind]
	if !ok {
		return res, invalid("kind is one of cm, tm, cli, positions, moi, mwpl, cli-all, positions-all")
	}
	if kind == "mwpl" && seg != "FO" {
		return res, invalid("market-wide position limits are for F&O only")
	}
	if actor != SystemActor {
		if err := o.require(ctx, tenant, actor, "Fetching margins from NSE Clearing", RoleMaker, RoleChecker, RoleRisk); err != nil {
			return res, err
		}
	}
	cn, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connNCLMargins)
	if err != nil || cn.Status == "Not set" {
		return res, fmt.Errorf("%w: set up the NSE Clearing margins API in Settings → Connections", ErrState)
	}
	c, err := parseNCLMConn(cn)
	if err != nil {
		return res, err
	}
	if !contains(c.segments, seg) {
		return res, invalid("segment %s is not switched on for the NSE Clearing margins connection (%s)", seg, strings.Join(c.segments, ", "))
	}
	now := o.now()
	date := now.In(ist).Format("2006-01-02")
	at := now.Format(time.RFC3339)
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cNCLMState, seg, NCLMState{Segment: seg})
	if st.Pending == nil {
		st.Pending = map[string]string{}
	}
	if st.Last == nil {
		st.Last = map[string]string{}
	}
	if st.Rows == nil {
		st.Rows = map[string]int{}
	}
	save := func() { _ = docs.Upsert(ctx, o.Docs, tenant, cNCLMState, seg, st, SystemActor) }
	fail := func(err error) (NCLMResult, error) {
		st.LastError = err.Error()
		save()
		return res, err
	}
	// A whole-book file already asked for is fetched by inquiry.
	pending := st.Pending[kind]
	path, every := k.Path, k.Every
	if pending != "" {
		path, every = k.Inquiry, 10*time.Second
	}
	if ok, wait := nclmSlot(c.key+"|"+seg+"|"+path, every, now); !ok {
		return res, fmt.Errorf("%w: %v (try again in %ds)", ErrState, errNCLMWait, int(wait.Seconds())+1)
	}
	s, err := o.nclmLogin(ctx, c, &st, now)
	if err != nil {
		return fail(err)
	}
	save()
	data := map[string]any{}
	if pending != "" {
		data["msgId"] = pending
		res.MsgID = pending
	} else {
		day := now.In(ist).Format("20060102")
		if st.MsgDate != day {
			st.MsgDate, st.MsgSeq = day, 0
		}
		st.MsgSeq++
		save() // a message number is spent before it is sent
		res.MsgID = fmt.Sprintf("%s%s%07d", c.member, day, st.MsgSeq)
		data["msgId"] = res.MsgID
		if k.Inquiry == "" {
			data["memCode"] = c.member
		}
	}
	if kind == "cli" || kind == "positions" {
		if len(uccs) == 0 {
			if uccs, err = o.nclmClientList(ctx, tenant, seg); err != nil {
				return fail(err)
			}
		}
		if len(uccs) == 0 {
			return fail(fmt.Errorf("%w: no active clients in %s to ask about", ErrState, seg))
		}
		if len(uccs) > nclmCliMax {
			return fail(invalid("NSE Clearing takes at most %d clients a call; use the whole-book file (%s-all)", nclmCliMax, kind))
		}
		data["cliList"] = uccs
	}
	text, code, err := o.nclmCall(ctx, c, s, seg, path, data, now, pending != "")
	var ce nclmCodeErr
	switch {
	case errors.Is(err, errNCLMLogin):
		st.Token, st.TokenExp, st.Key, st.IV = "", "", "", ""
		return fail(err)
	case errors.As(err, &ce) && nclmCheck(ce.code) == "0245":
		res.Status = "In progress"
		st.LastError = ""
		save()
		return res, nil
	case errors.As(err, &ce) && nclmCheck(ce.code) == "0244":
		res.Status = "No data"
		delete(st.Pending, kind)
		st.Last[kind], st.Rows[kind], st.LastError = at, 0, ""
		save()
		return res, nil
	case err != nil:
		if pending != "" && errors.As(err, &ce) {
			delete(st.Pending, kind) // ask for a new file next time
		}
		return fail(err)
	}
	_ = code
	if k.Inquiry != "" && pending == "" {
		st.Pending[kind] = res.MsgID
		st.LastError = ""
		res.Status = "Requested"
		save()
		o.log(ctx, tenant, actor, "nclmargins.requested", seg+"/"+kind, map[string]any{"msg_id": res.MsgID})
		return res, nil
	}
	recs := nclmRecords(text)
	base := strings.TrimSuffix(kind, "-all")
	switch base {
	case "cli":
		for _, cl := range nclmClients(seg, date, at, recs) {
			_ = docs.Upsert(ctx, o.Docs, tenant, cNCLMCli, seg+"|"+date+"|"+cl.UCC, cl, SystemActor)
			res.Rows++
		}
	case "positions":
		for u, ps := range nclmPositions(recs) {
			_ = docs.Upsert(ctx, o.Docs, tenant, cNCLMPos, seg+"|"+date+"|"+u, NCLMPositions{Segment: seg, Date: date, At: at, UCC: u, Positions: ps}, SystemActor)
			res.Rows++
		}
	default:
		cols := nclmColumns[base]
		snap := NCLMSnapshot{Segment: seg, Date: date, Kind: base, At: at, Columns: cols, Rows: [][]string{}}
		for _, f := range recs {
			if len(f) > len(cols) {
				f = f[:len(cols)]
			}
			snap.Rows = append(snap.Rows, f)
		}
		res.Rows = len(snap.Rows)
		_ = docs.Upsert(ctx, o.Docs, tenant, cNCLMSnap, seg+"|"+date+"|"+base, snap, SystemActor)
	}
	delete(st.Pending, kind)
	st.Last[kind], st.Rows[kind], st.LastError = at, res.Rows, ""
	save()
	res.Status = "Received"
	o.log(ctx, tenant, actor, "nclmargins.fetched", seg+"/"+kind, map[string]any{"rows": res.Rows, "msg_id": res.MsgID})
	return res, nil
}

// nclmClientList is the active clients trading in a segment.
func (o *Ops) nclmClientList(ctx context.Context, tenant, seg string) ([]string, error) {
	cs, err := o.Clients(ctx, tenant)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, c := range cs {
		if c.Status == "Active" && (len(c.Segments) == 0 || contains(c.Segments, seg)) {
			out = append(out, strings.ToUpper(c.UCC))
		}
	}
	sort.Strings(out)
	return out, nil
}

// NCLMClientMargins are NSE Clearing's margins for each client on a date,
// added across segments, and the time of the latest figure.
func (o *Ops) NCLMClientMargins(ctx context.Context, tenant, date string) (map[string]money.Dec, string, error) {
	out := map[string]money.Dec{}
	latest := ""
	for _, seg := range []string{"FO", "CD", "CO"} {
		cs, err := docs.List[NCLMClient](ctx, o.Docs, tenant, cNCLMCli, seg+"|"+date+"|")
		if err != nil {
			return nil, "", err
		}
		for _, c := range cs {
			out[c.UCC] = out[c.UCC].Add(c.Total)
			if c.At > latest {
				latest = c.At
			}
		}
	}
	return out, latest, nil
}

// NCLMView is the screen: each segment's feed and what it holds today.
type NCLMView struct {
	Date     string              `json:"date"`
	Segments []NCLMState         `json:"segments"`
	Kinds    map[string]string   `json:"kinds"`
	Snaps    []NCLMSnapshot      `json:"snapshots"`
	Clients  int                 `json:"clients"`
	Top      []NCLMClient        `json:"top_clients"` // highest margin first
	Errors   map[string]string   `json:"errors,omitempty"`
	Enabled  []string            `json:"enabled"`
	Sizes    map[string][]string `json:"columns"`
}

// NCLMarginsStatus shows where the feed stands and today's figures.
func (o *Ops) NCLMarginsStatus(ctx context.Context, tenant, date string) NCLMView {
	if date == "" {
		date = o.now().In(ist).Format("2006-01-02")
	}
	v := NCLMView{Date: date, Kinds: map[string]string{}, Snaps: []NCLMSnapshot{}, Top: []NCLMClient{}, Sizes: nclmColumns, Enabled: []string{}}
	for k, d := range NCLMKinds {
		v.Kinds[k] = d.Label
	}
	if cn, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connNCLMargins); err == nil && cn.Status != "Not set" {
		v.Enabled = nclmSegments(cn.Values["segments"])
	}
	for _, seg := range []string{"FO", "CD", "CO"} {
		st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cNCLMState, seg, NCLMState{Segment: seg})
		st.Token, st.Key, st.IV = "", "", ""
		v.Segments = append(v.Segments, st)
		snaps, _ := docs.List[NCLMSnapshot](ctx, o.Docs, tenant, cNCLMSnap, seg+"|"+date+"|")
		v.Snaps = append(v.Snaps, snaps...)
		cs, _ := docs.List[NCLMClient](ctx, o.Docs, tenant, cNCLMCli, seg+"|"+date+"|")
		v.Clients += len(cs)
		v.Top = append(v.Top, cs...)
	}
	sort.SliceStable(v.Top, func(i, j int) bool { return v.Top[i].Total.Cmp(v.Top[j].Total) > 0 })
	if len(v.Top) > 50 {
		v.Top = v.Top[:50]
	}
	return v
}

// NCLMClientPositions are a client's positions at NSE Clearing on a date.
func (o *Ops) NCLMClientPositions(ctx context.Context, tenant, date, ucc string) ([]NCLMPositions, error) {
	var out []NCLMPositions
	for _, seg := range []string{"FO", "CD", "CO"} {
		p, _, err := docs.Get[NCLMPositions](ctx, o.Docs, tenant, cNCLMPos, seg+"|"+date+"|"+strings.ToUpper(ucc))
		if err == nil {
			out = append(out, p)
		}
	}
	return out, nil
}

// nclMarginsTick fetches client margins every five minutes, and the
// clearing and trading member figures every fifteen, during market hours
// for each segment switched on (commodity until 23:30).
func (o *Ops) nclMarginsTick(ctx context.Context, tenant string, now time.Time) {
	cn, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connNCLMargins)
	if err != nil || cn.Status == "Not set" || cn.Values["fetch"] != "On" {
		return
	}
	hm := now.In(ist).Format("15:04")
	for _, seg := range nclmSegments(cn.Values["segments"]) {
		closeAt := "15:45"
		if seg == "CO" {
			closeAt = "23:30"
		}
		if hm < "09:15" || hm > closeAt {
			continue
		}
		st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cNCLMState, seg, NCLMState{Segment: seg})
		due := func(kind string, every time.Duration) bool {
			t, err := time.Parse(time.RFC3339, st.Last[kind])
			return err != nil || now.Sub(t) >= every || st.Pending[kind] != ""
		}
		cli := "cli"
		if list, _ := o.nclmClientList(ctx, tenant, seg); len(list) > nclmCliMax {
			cli = "cli-all"
		}
		for _, k := range []struct {
			kind  string
			every time.Duration
		}{{cli, 5 * time.Minute}, {"cm", 15 * time.Minute}, {"tm", 15 * time.Minute}} {
			if due(k.kind, k.every) {
				_, _ = o.NCLMarginsFetch(ctx, tenant, SystemActor, seg, k.kind, nil)
			}
		}
	}
}

// ---- the key pair ----

// NCLMSubject is who the certificate names.
type NCLMSubject struct {
	Country string `json:"country"`
	State   string `json:"state"`
	City    string `json:"city"`
	Org     string `json:"organisation"`
	Unit    string `json:"department"`
	Name    string `json:"common_name"`
}

// NCLMKey is the certificate to upload to NMASS, and a signing request for
// firms that want it signed by a certificate authority (NSE Clearing
// recommends this; a self-signed certificate is not refused).
type NCLMKey struct {
	FileName    string `json:"file_name"`
	Certificate string `json:"certificate"`
	Request     string `json:"signing_request"`
	Expires     string `json:"expires"`
}

// NCLMarginsMakeKey makes a 2048-bit RSA key pair, keeps the private key
// in the connection's vault and returns the certificate (one year) as
// RSA_<member>.pem.
func (o *Ops) NCLMarginsMakeKey(ctx context.Context, tenant, actor string, sub NCLMSubject) (NCLMKey, error) {
	if err := o.require(ctx, tenant, actor, "Making the NSE Clearing key", RoleAdmin); err != nil {
		return NCLMKey{}, err
	}
	cn, _, _ := docs.GetOr(ctx, o.Docs, tenant, cConn, connNCLMargins, Connection{Provider: connNCLMargins, Status: "Not set"})
	member := strings.ToUpper(strings.TrimSpace(cn.Values["member"]))
	if member == "" {
		return NCLMKey{}, invalid("save the clearing member code on the connection first")
	}
	sub.Country = strings.ToUpper(firstOf(strings.TrimSpace(sub.Country), "IN"))
	if sub.Org == "" || sub.Name == "" || sub.State == "" || sub.City == "" {
		return NCLMKey{}, invalid("enter the organisation, state, city and the server's name (common name)")
	}
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return NCLMKey{}, err
	}
	name := pkix.Name{Country: []string{sub.Country}, Province: []string{sub.State}, Locality: []string{sub.City}, Organization: []string{sub.Org}, CommonName: sub.Name}
	if sub.Unit != "" {
		name.OrganizationalUnit = []string{sub.Unit}
	}
	now := o.now()
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	tmpl := &x509.Certificate{SerialNumber: serial, Subject: name, NotBefore: now.Add(-time.Hour), NotAfter: now.AddDate(1, 0, 0),
		KeyUsage: x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature | x509.KeyUsageDataEncipherment, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		return NCLMKey{}, err
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: name}, priv)
	if err != nil {
		return NCLMKey{}, err
	}
	if cn.Secrets == nil {
		cn.Secrets = map[string]string{}
	}
	if cn.Values == nil {
		cn.Values = map[string]string{}
	}
	cn.Secrets["private_key"] = string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)}))
	cn.UpdatedAt, cn.UpdatedBy = now.Format(time.RFC3339), actor
	if err := docs.Upsert(ctx, o.Docs, tenant, cConn, connNCLMargins, cn, actor); err != nil {
		return NCLMKey{}, err
	}
	// A new key means a new login.
	for _, seg := range []string{"FO", "CD", "CO"} {
		if st, _, err := docs.Get[NCLMState](ctx, o.Docs, tenant, cNCLMState, seg); err == nil {
			st.Token, st.TokenExp, st.Key, st.IV = "", "", "", ""
			_ = docs.Upsert(ctx, o.Docs, tenant, cNCLMState, seg, st, SystemActor)
		}
	}
	o.log(ctx, tenant, actor, "nclmargins.key_made", connNCLMargins, map[string]any{"member": member, "expires": tmpl.NotAfter.Format("2006-01-02")})
	return NCLMKey{FileName: "RSA_" + member + ".pem", Expires: tmpl.NotAfter.In(ist).Format("2006-01-02"),
		Certificate: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		Request:     string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csr}))}, nil
}
