package ops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// ---- BOW (BOLTPlusOnWeb) client limits by API ----
//
// BOW is BSE's front end run by MarketPlace Tech Infra Services, which the
// pilot broker uses for internet equity trading and for MCX. Its limits are
// "user deposits" an admin login sets per client; this pushes them from the
// ledger by the BOW API (v1.0.5), Securities and Commodities separately.
//
// What the API document gives (only what the code needs):
//   - HTTP requests named by message (LoginServlet, EditUserDeposit, …)
//     with named fields; repeating rows carry the row number at the end of
//     each field name (UDUSID0, UDUSID1, … with rowcount). Answers are
//     ASCII: records separated by "~", columns by "|", sub-keys by "^".
//     The first record's first column is the status (0 done, 1 failed),
//     the second the message.
//   - Login: LoginServlet (USLOGINID, USPassword, VenderCode, Version,
//     enable2FA …). With two-factor on, AuthenticationMode (verificationMode
//     1) sends an OTP to the user's registered mobile and e-mail, and
//     AuthenticationLogin (OTP) answers the session key; without it the
//     login answer's second record holds the internal user ID and the
//     session key. Every later request carries SessionKey and ThickClient=Y.
//     Three wrong passwords lock the login; passwords run out after 14 days.
//   - EditUserDeposit adds (UDROWSTATE 1) or removes (0) a client's
//     deposit rows, at most 25 a request, one client a request (USLOGINID
//     and USBackOfficeId name the client; UDUSID is BOW's internal ID).
//     Row 0 is the user level (UDDepositLevel 0, no category) and must be
//     there; level 11 rows are User → Securities (UDCategory 1) or
//     Commodities (2). Amounts are sent × 10,000: UDAmount1 the cash
//     deposit, UDAmount2 the multiplier, UDDeposit their product (the
//     effective deposit), UDADHOCMARGIN the adhoc deposit. MTM factor -1
//     means MTM is not checked; UDFIELD5 is the MTM limit (-1 likewise),
//     UDFIELD6/7 whether adhoc counts for margin / MTM.
//   - ListUserDeposit lists a client's rows (ID, Category, Exchange,
//     Market, Product, Cash Deposit, Effective Deposit, Multiplier, …, USID)
//     in rupees; PreEditUserDeposit (UDID) gives the rows to modify by
//     their internal IDs; the modify is EditUserDeposit with the IDs.
//   - UserSearchServlet (MAXUSID 0, userstatus 1) downloads the client
//     master: USID, names, LoginId, BackOfficeId, status, type.
//   - The document gives no request rate, no session lifetime and no URL
//     path for the servlets; only the hosts (membersim.bseindia.com for
//     UAT, bow1mobile.bseindia.com live, under /stocks).
//
// Choices where the document is not explicit:
//   - The address is the base (default https://<host>/stocks) and a
//     message goes to <base>/<message name>, as a form POST (GET can be
//     chosen on the connection). Both are on the connection so the broker
//     can follow what MarketPlace says.
//   - A client is pushed as three rows: the user level (cash the sum of
//     the two, so the overall limit is never below the categories), and
//     Securities and Commodities at level 11. Existing rows are listed
//     first: rows BOW has are modified by their IDs (PreEdit, then Edit),
//     missing ones are added; rows already at the values are left alone.
//     A closed client's rows are removed (UDROWSTATE 0); a blocked,
//     inactive or not active client gets 0 on both. A debit balance is
//     sent as 0 (BOW has no use for a negative deposit).
//   - The client's BOW login is found in the client master by its
//     BackOfficeId or LoginId: the UCC itself, or the UCC before the last
//     "-" (BOW writes CLIENT-T<member>). The master is fetched once a day;
//     Risk can set a client's BOW user by hand.
//   - Passwords, OTPs and session keys are never stored, logged or kept in
//     a call record; the session key lives in memory for the IST day. A
//     refused password stops automatic pushes (the connection shows
//     Problem) so a fourth attempt never locks the login. With two-factor
//     on, Risk signs in each morning with the OTP (Live RMS); until then
//     pushes wait and a work item asks for the sign-in.
//   - Requests are spaced (bowGap) as the document sets no rate.

const (
	connBOW       = "bow-api"
	cBOWClient    = "bowclient"   // ucc -> BOWClientState
	cBOWQueue     = "bowqueue"    // ucc -> bowQueued
	cBOWRun       = "bowrun"      // run id -> BOWPushRun
	cBOWMaster    = "bowmaster"   // "master" -> bowMaster
	cBOWOverride  = "bowoverride" // ucc -> BOWOverride
	cBOWState     = "bowstate"    // "state" -> bowStateDoc
	wBOW          = "bowpush"
	bowUAT        = "https://membersim.bseindia.com/stocks"
	bowLive       = "https://bow1mobile.bseindia.com/stocks"
	bowScale      = 10000
	bowMaxRunCall = 400 // calls kept on a run's record
	bowDayStart   = "09:00"
	bowDayEnd     = "23:30" // MCX runs late
	bowOTPOn      = "OTP (Risk signs in each day)"
	bowOTPOff     = "Off"

	BOWPushed    = "Pushed"
	BOWUnchanged = "Unchanged" // BOW already had the values
	BOWFailed    = "Failed"
	BOWRemoved   = "Removed"
	BOWNotMapped = "Not in BOW"
)

func init() {
	ConnProviders = append(ConnProviders, ConnProvider{ID: connBOW, Name: "BOW (BOLTPlusOnWeb) limits API", Group: "Trading", Test: "https",
		Purpose: "Sets each client's limits (user deposits) in BOW, BSE's front end from MarketPlace Tech, for Securities and Commodities separately: at the start of the day and again during the day when money comes in or goes out. Needs a BOW admin login enabled for the API, the vendor code BOW allots, and this server's address allowed by MarketPlace",
		Fields: []ConnField{
			{Key: "env", Label: "Environment", Options: []string{"UAT", "Live"}},
			{Key: "url", Label: "Address (leave empty for BOW's, e.g. https://bow1mobile.bseindia.com/stocks)", Optional: true, Placeholder: bowUAT},
			{Key: "user", Label: "BOW admin login ID (e.g. ADMIN-T9065)"},
			{Key: "password", Label: "Password", Secret: true},
			{Key: "vendor_code", Label: "Vendor code (from the BOW team)"},
			{Key: "version", Label: "Front-end version with vendor code, as BOW allotted (default LEDGERLINE_1.0)", Optional: true},
			{Key: "two_factor", Label: "Two-factor sign-in", Options: []string{bowOTPOn, bowOTPOff}},
			{Key: "sec_mult", Label: "Securities multiplier (default 1)", Optional: true, Placeholder: "1"},
			{Key: "com_mult", Label: "Commodities multiplier (default 1)", Optional: true, Placeholder: "1"},
			{Key: "mtm", Label: "MTM limit multiplier (empty: MTM not checked)", Optional: true},
			{Key: "send", Label: "Push to BOW automatically (start of day, then 09:00 to 23:30)", Options: []string{"Off", "On"}},
			{Key: "bod_at", Label: "Start-of-day push at (HH:MM, default 08:30)", Optional: true, Placeholder: "08:30"},
			{Key: "every", Label: "Check for changed limits every (minutes, default 5)", Optional: true, Placeholder: "5"},
			{Key: "method", Label: "HTTP method (default POST)", Optional: true, Options: []string{"POST", "GET"}},
			fNotes}})
}

// ---- the connection ----

type bowConn struct {
	base, user, pass, vendor, version, method string
	otp, auto, live                           bool
	secMult, comMult                          money.Dec // as given (1 = 1×)
	mtm                                       money.Dec // MTM multiplier; zero: not checked
	bodAt                                     string
	every                                     time.Duration
}

func (x bowConn) fp() string {
	s := sha256.Sum256([]byte(x.base + "\x00" + x.user + "\x00" + x.pass + "\x00" + x.vendor))
	return hex.EncodeToString(s[:])
}

var reBOWHHMM = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)

func bowMult(s, what string) (money.Dec, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return money.DecInt(1), nil
	}
	d, err := money.ParseDec(s)
	if err != nil || d.Sign() <= 0 || d.Cmp(money.DecInt(100)) > 0 {
		return money.Dec{}, invalid("the %s multiplier must be a number above 0 and at most 100", what)
	}
	return d, nil
}

func parseBOWConn(c Connection) (bowConn, error) {
	x := bowConn{user: strings.TrimSpace(c.Values["user"]), pass: c.Secrets["password"], vendor: strings.TrimSpace(c.Values["vendor_code"]),
		version: firstOf(strings.TrimSpace(c.Values["version"]), "LEDGERLINE_1.0"), method: firstOf(c.Values["method"], http.MethodPost),
		otp: c.Values["two_factor"] != bowOTPOff, auto: c.Values["send"] == "On", bodAt: firstOf(strings.TrimSpace(c.Values["bod_at"]), "08:30"), every: 5 * time.Minute}
	if x.user == "" || x.pass == "" || x.vendor == "" {
		return x, invalid("the BOW connection needs the admin login ID, its password and the vendor code")
	}
	var err error
	if x.secMult, err = bowMult(c.Values["sec_mult"], "Securities"); err != nil {
		return x, err
	}
	if x.comMult, err = bowMult(c.Values["com_mult"], "Commodities"); err != nil {
		return x, err
	}
	if m := strings.TrimSpace(c.Values["mtm"]); m != "" {
		if x.mtm, err = bowMult(m, "MTM limit"); err != nil {
			return x, err
		}
	}
	if !reBOWHHMM.MatchString(x.bodAt) || x.bodAt >= bowDayStart {
		return x, invalid("the start-of-day push time is HH:MM before %s", bowDayStart)
	}
	if e := strings.TrimSpace(c.Values["every"]); e != "" {
		n, err := strconv.Atoi(e)
		if err != nil || n < 1 || n > 60 {
			return x, invalid("check for changed limits every 1 to 60 minutes")
		}
		x.every = time.Duration(n) * time.Minute
	}
	x.base = bowUAT
	if c.Values["env"] == "Live" {
		x.base = bowLive
	}
	if u := strings.TrimRight(strings.TrimSpace(c.Values["url"]), "/"); u != "" {
		x.base = u
	}
	x.live = x.base == bowLive || c.Values["env"] == "Live"
	return x, nil
}

// checkBOWConn is SaveConnection's check of the BOW fields.
func checkBOWConn(c Connection) error {
	_, err := parseBOWConn(c)
	return err
}

func (o *Ops) bowConnection(ctx context.Context, tenant string) (Connection, bowConn, error) {
	cn, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connBOW)
	if err != nil || cn.Status == "Not set" || cn.Status == "" {
		return cn, bowConn{}, fmt.Errorf("%w: set up the BOW limits API in Settings → Connections", ErrState)
	}
	x, err := parseBOWConn(cn)
	return cn, x, err
}

func (o *Ops) bowConnMark(ctx context.Context, tenant, status, problem string) {
	_, _ = docs.Update(ctx, o.Docs, tenant, cConn, connBOW, SystemActor, func(c *Connection) error {
		if c.Status == "Not set" {
			return docs.ErrStale
		}
		c.Status, c.Problem, c.TestedAt, c.TestedBy = status, problem, o.now().Format(time.RFC3339), SystemActor
		return nil
	})
}

// ---- talking to BOW ----

// bowHTTP talks to BOW; tests replace it.
var bowHTTP = &http.Client{Timeout: 30 * time.Second}

// bowGap spaces requests (the document gives no rate); tests set it to 0.
var bowGap = 100 * time.Millisecond

// Sessions and OTP sign-ins in progress, in memory only.
var (
	bowSessions sync.Map // tenant -> bowSession
	bowPending  sync.Map // tenant -> bowPendingLogin
)

type bowSession struct {
	key, usid, fp, day string
	at                 time.Time
}

type bowPendingLogin struct {
	pre, fp string
	at      time.Time
}

// bowStop ends a run: auth when BOW refused the login (automatic pushes
// stop until the connection is saved again), signIn when a person must
// sign in with an OTP.
type bowStop struct {
	msg          string
	auth, signIn bool
}

func (e *bowStop) Error() string { return e.msg }

type bowCall struct {
	ctx    context.Context
	tenant string
	x      bowConn
	now    func() time.Time
	calls  []BSEUCCCall
	sent   int
}

// bowRecords splits an answer into records and columns.
func bowRecords(s string) [][]string {
	s = strings.TrimSpace(s)
	var out [][]string
	for _, r := range strings.Split(s, "~") {
		r = strings.Trim(r, "\r\n")
		if strings.TrimSpace(r) == "" {
			continue
		}
		out = append(out, strings.Split(r, "|"))
	}
	return out
}

func col(r []string, i int) string {
	if i < len(r) {
		return strings.TrimSpace(r[i])
	}
	return ""
}

// bowStatus is an answer's status: done unless the first column is 1; msg
// is BOW's message.
func bowStatus(recs [][]string) (bool, string) {
	if len(recs) == 0 {
		return false, "BOW gave an empty answer"
	}
	if col(recs[0], 0) == "1" {
		for _, c := range recs[0][1:] {
			if c = strings.TrimSpace(c); c != "" {
				return false, c
			}
		}
		return false, "BOW refused the request without a reason"
	}
	return true, col(recs[0], 1)
}

var reBOWSession = regexp.MustCompile(`(?i)session|log ?in again|not logged|relogin|re-login`)

// do sends one message; form holds its fields. The answer text is returned
// as is.
func (c *bowCall) do(name string, form url.Values) (int, string, error) {
	if c.sent > 0 && bowGap > 0 {
		select {
		case <-c.ctx.Done():
			return 0, "", c.ctx.Err()
		case <-time.After(bowGap):
		}
	}
	c.sent++
	u := c.x.base + "/" + name
	var req *http.Request
	var err error
	if c.x.method == http.MethodGet {
		req, err = http.NewRequestWithContext(c.ctx, http.MethodGet, u+"?"+form.Encode(), nil)
	} else {
		req, err = http.NewRequestWithContext(c.ctx, http.MethodPost, u, strings.NewReader(form.Encode()))
		if req != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
	}
	if err != nil {
		return 0, "", err
	}
	resp, err := bowHTTP.Do(req)
	if err != nil {
		msg := err.Error()
		if i := strings.LastIndex(msg, "\": "); i >= 0 {
			msg = msg[i+3:] // never the URL: a GET carries the session key
		}
		return 0, "", fmt.Errorf("could not reach BOW: %s", msg)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	return resp.StatusCode, string(b), err
}

func (c *bowCall) record(call BSEUCCCall) { c.calls = append(c.calls, call) }

// login signs in with the password: the internal ID and the session key
// (blank with two-factor on until the OTP is verified).
func (c *bowCall) login() (usid, key string, err error) {
	ip := os.Getenv("LEDGERLINE_SERVER_IP")
	f := url.Values{"USBackOfficeId": {""}, "USLOGINID": {c.x.user}, "USPassword": {c.x.pass}, "USTransactionPassword": {""}, "Version": {c.x.version},
		"SecuritiesMaxSequenceId": {"0"}, "NSEContractsMaxSequenceId": {"0"}, "NcdexContractsMaxSequenceId": {"0"}, "MCXContractsMaxSequenceId": {"0"},
		"BSEContractsMaxSequenceId": {"0"}, "BSECurrencyContractsMaxSequenceId": {"0"}, "NSECurrencyContractsMaxSequenceId": {"0"},
		"lLoginLogIPAddress": {ip}, "enable2FA": {map[bool]string{true: "Y", false: "N"}[c.x.otp]}, "VenderCode": {c.x.vendor}, "SessionKey": {""}, "ThickClient": {"Y"}}
	code, body, err := c.do("LoginServlet", f)
	call := BSEUCCCall{Function: "LoginServlet", Request: "login " + c.x.user + " (password not recorded)", Code: code}
	defer func() { c.record(call) }()
	if err != nil {
		call.Error = err.Error()
		return "", "", &bowStop{msg: err.Error()}
	}
	if code/100 != 2 {
		call.Error = fmt.Sprintf("HTTP %d", code)
		if code == 401 || code == 403 {
			return "", "", &bowStop{msg: fmt.Sprintf("BOW refused the connection (%d): ask MarketPlace to allow this server's address", code)}
		}
		return "", "", &bowStop{msg: fmt.Sprintf("BOW's server answered %d to the sign-in: %s", code, trimBody([]byte(body)))}
	}
	recs := bowRecords(body)
	ok, msg := bowStatus(recs)
	if !ok {
		call.Response, call.Error = "refused", msg
		return "", "", &bowStop{msg: "BOW refused the sign-in (" + msg + "): check the admin login ID and password in Connections. BOW locks the login after three wrong passwords, so automatic pushes stop until the connection is saved again", auth: true}
	}
	for _, r := range recs {
		if (col(r, 0) == "14" || col(r, 0) == "16") && strings.Contains(strings.ToLower(col(r, 1)), "password") {
			call.Response = "password change asked"
			return "", "", &bowStop{msg: "BOW asks for the admin password to be changed (" + col(r, 1) + "): change it in the BOW Admin Terminal and save the new one in Connections", auth: true}
		}
	}
	if len(recs) > 1 {
		usid, key = col(recs[1], 0), col(recs[1], 1)
	}
	call.Response = "signed in as user " + usid // never the session key
	return usid, key, nil
}

// session gives the day's session key: the one held, else a sign-in when
// two-factor is off. With two-factor on a person signs in (BOWSignIn).
func (c *bowCall) session(force bool) (string, error) {
	day := c.now().In(ist).Format("2006-01-02")
	if v, ok := bowSessions.Load(c.tenant); ok && !force {
		if s := v.(bowSession); s.fp == c.x.fp() && s.day == day && s.key != "" {
			return s.key, nil
		}
	}
	bowSessions.Delete(c.tenant)
	if c.x.otp {
		return "", &bowStop{msg: "BOW needs today's sign-in with the OTP: Live RMS → BOW limits → Sign in to BOW", signIn: true}
	}
	usid, key, err := c.login()
	if err != nil {
		return "", err
	}
	if key == "" {
		return "", &bowStop{msg: "BOW's sign-in gave no session key: if the login has two-factor sign-in, set Two-factor sign-in to OTP in Connections", auth: true}
	}
	bowSessions.Store(c.tenant, bowSession{key: key, usid: usid, fp: c.x.fp(), day: day, at: c.now()})
	return key, nil
}

// req sends a message with the session, signing in again once (two-factor
// off) when BOW says the session ended. ucc and summary go on the record;
// the form itself is never recorded.
func (c *bowCall) req(name, ucc, summary string, f url.Values) ([][]string, error) {
	call := BSEUCCCall{Function: name, UCC: ucc, Request: summary}
	for attempt := 0; ; attempt++ {
		key, err := c.session(attempt > 0)
		if err != nil {
			call.Error = err.Error()
			c.record(call)
			return nil, err
		}
		f.Set("SessionKey", key)
		f.Set("ThickClient", "Y")
		code, body, err := c.do(name, f)
		call.Code = code
		if err != nil {
			call.Error = err.Error()
			c.record(call)
			return nil, &bowStop{msg: err.Error()}
		}
		if code == 401 || code == 403 {
			call.Error = fmt.Sprintf("HTTP %d", code)
			c.record(call)
			return nil, &bowStop{msg: fmt.Sprintf("BOW refused the request (%d): ask MarketPlace to allow this server's address and the API for the admin login", code)}
		}
		if code/100 != 2 {
			call.Error = fmt.Sprintf("HTTP %d: %s", code, trimBody([]byte(body)))
			c.record(call)
			return nil, &bowStop{msg: fmt.Sprintf("BOW's server answered %d", code)}
		}
		recs := bowRecords(body)
		ok, msg := bowStatus(recs)
		if !ok && reBOWSession.MatchString(msg) {
			bowSessions.Delete(c.tenant)
			if attempt == 0 && !c.x.otp {
				continue
			}
			call.Error = msg
			c.record(call)
			return nil, &bowStop{msg: "BOW ended the session (" + msg + "): sign in to BOW again", signIn: c.x.otp}
		}
		call.Response = trimTo(strings.TrimSpace(body), 2000)
		if !ok {
			call.Error = msg
		}
		c.record(call)
		return recs, nil
	}
}

// ---- signing in (two-factor) ----

// BOWSession is the state of the firm's BOW sign-in.
type BOWSession struct {
	SignedIn  bool   `json:"signed_in"`
	At        string `json:"at,omitempty"`
	OTP       bool   `json:"otp"` // two-factor on
	OTPSentAt string `json:"otp_sent_at,omitempty"`
	Message   string `json:"message,omitempty"` // BOW's words
}

func (o *Ops) bowSessionState(tenant string, x bowConn) BOWSession {
	s := BOWSession{OTP: x.otp}
	day := o.now().Format("2006-01-02")
	if v, ok := bowSessions.Load(tenant); ok {
		if bs := v.(bowSession); bs.fp == x.fp() && bs.day == day {
			s.SignedIn, s.At = true, bs.at.In(ist).Format(time.RFC3339)
		}
	}
	if v, ok := bowPending.Load(tenant); ok {
		if p := v.(bowPendingLogin); p.fp == x.fp() && o.now().Sub(p.at) < 10*time.Minute {
			s.OTPSentAt = p.at.In(ist).Format(time.RFC3339)
		}
	}
	return s
}

// BOWSignIn signs the firm in to BOW. Without an OTP it signs in with the
// password and, with two-factor on, asks BOW to send the OTP to the admin
// login's registered mobile and e-mail; with the OTP it finishes the
// sign-in. The session key is kept in memory for the day only.
func (o *Ops) BOWSignIn(ctx context.Context, tenant, actor, otp string) (BOWSession, error) {
	if err := o.require(ctx, tenant, actor, "Signing in to BOW", RoleRisk); err != nil {
		return BOWSession{}, err
	}
	_, x, err := o.bowConnection(ctx, tenant)
	if err != nil {
		return BOWSession{}, err
	}
	otp = strings.TrimSpace(otp)
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	c := &bowCall{ctx: cctx, tenant: tenant, x: x, now: o.now}
	defer func() {
		o.log(ctx, tenant, actor, "bow.signin", connBOW, map[string]any{"step": map[bool]string{true: "otp", false: "password"}[otp != ""], "calls": len(c.calls)})
	}()
	fail := func(err error) (BOWSession, error) {
		var st *bowStop
		if errors.As(err, &st) && st.auth {
			o.bowConnMark(ctx, tenant, "Problem", st.msg)
		}
		return o.bowSessionState(tenant, x), fmt.Errorf("%w: %s", ErrState, err.Error())
	}
	day := o.now().Format("2006-01-02")
	if otp == "" {
		usid, key, err := c.login()
		if err != nil {
			return fail(err)
		}
		if !x.otp {
			if key == "" {
				return fail(&bowStop{msg: "BOW's sign-in gave no session key: if the login has two-factor sign-in, set Two-factor sign-in to OTP in Connections"})
			}
			bowSessions.Store(tenant, bowSession{key: key, usid: usid, fp: x.fp(), day: day, at: o.now()})
			o.bowConnMark(ctx, tenant, "Working", "signed in to BOW")
			o.closeWork(ctx, tenant, actor, wBOW, "signin", "signed in")
			return o.bowSessionState(tenant, x), nil
		}
		code, body, err := c.do("AuthenticationMode", url.Values{"verificationMode": {"1"}, "USLOGINID": {x.user}, "SessionKey": {key}, "ThickClient": {"Y"}})
		call := BSEUCCCall{Function: "AuthenticationMode", Request: "OTP for " + x.user, Code: code}
		if err == nil && code/100 != 2 {
			err = fmt.Errorf("BOW's server answered %d", code)
		}
		if err != nil {
			call.Error = err.Error()
			c.record(call)
			return fail(&bowStop{msg: err.Error()})
		}
		ok, msg := bowStatus(bowRecords(body))
		call.Response = msg
		c.record(call)
		if !ok {
			return fail(&bowStop{msg: "BOW did not send the OTP: " + msg})
		}
		bowPending.Store(tenant, bowPendingLogin{pre: key, fp: x.fp(), at: o.now()})
		s := o.bowSessionState(tenant, x)
		s.Message = firstOf(msg, "OTP sent to the admin login's registered mobile and e-mail")
		return s, nil
	}
	if !x.otp {
		return BOWSession{}, invalid("two-factor sign-in is off on the BOW connection: sign in without an OTP")
	}
	v, ok := bowPending.Load(tenant)
	p, _ := v.(bowPendingLogin)
	if !ok || p.fp != x.fp() || o.now().Sub(p.at) >= 10*time.Minute {
		return BOWSession{}, invalid("ask BOW for an OTP first (it is valid for a few minutes)")
	}
	if strings.Trim(otp, "0123456789") != "" || len(otp) < 4 || len(otp) > 10 {
		return BOWSession{}, invalid("the OTP is the digits BOW sent")
	}
	code, body, err := c.do("AuthenticationLogin", url.Values{"OTP": {otp}, "USLOGINID": {x.user}, "SessionKey": {p.pre}, "ThickClient": {"Y"}})
	call := BSEUCCCall{Function: "AuthenticationLogin", Request: "OTP check for " + x.user + " (OTP not recorded)", Code: code}
	if err == nil && code/100 != 2 {
		err = fmt.Errorf("BOW's server answered %d", code)
	}
	if err != nil {
		call.Error = err.Error()
		c.record(call)
		return fail(&bowStop{msg: err.Error()})
	}
	recs := bowRecords(body)
	ok, msg := bowStatus(recs)
	if !ok || msg == "" {
		call.Response = "refused"
		c.record(call)
		return fail(&bowStop{msg: "BOW did not accept the OTP: " + firstOf(msg, "no session key in the answer")})
	}
	call.Response = "OTP accepted" // never the session key
	c.record(call)
	bowPending.Delete(tenant)
	bowSessions.Store(tenant, bowSession{key: msg, fp: x.fp(), day: day, at: o.now()})
	o.bowConnMark(ctx, tenant, "Working", "signed in to BOW with the OTP")
	o.closeWork(ctx, tenant, actor, wBOW, "signin", "signed in")
	return o.bowSessionState(tenant, x), nil
}

// bowTest is the connection's test: the sign-in (or the day's session)
// and a harmless list (exchange status).
func (o *Ops) bowTest(ctx context.Context, tenant string, cn Connection) (string, string) {
	x, err := parseBOWConn(cn)
	if err != nil {
		return "Problem", strings.TrimPrefix(err.Error(), "invalid: ")
	}
	cctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	c := &bowCall{ctx: cctx, tenant: tenant, x: x, now: o.now}
	env := map[bool]string{true: "live", false: "UAT"}[x.live]
	if x.otp && !o.bowSessionState(tenant, x).SignedIn {
		if _, _, err := c.login(); err != nil {
			return "Problem", err.Error()
		}
		return "Working", "BOW (" + env + ") accepted the admin login and password; Risk signs in with the OTP on Live RMS to push limits"
	}
	recs, err := c.req("ExchangeStatus", "", "exchange status", url.Values{})
	if err != nil {
		return "Problem", err.Error()
	}
	if ok, msg := bowStatus(recs); !ok {
		return "Problem", "BOW signed in but refused the exchange status: " + msg
	}
	return "Working", fmt.Sprintf("BOW (%s) signed in and answered the exchange status (%d records)", env, len(recs))
}

// ---- amounts ----

// bowAmt is an amount as BOW takes it: rupees × 10,000.
func bowAmt(d money.Dec) int64 { return d.Round(money.UnitPaisa).Paise() * (bowScale / 100) }

func bowMultAmt(d money.Dec) int64 { return d.MulInt(bowScale).Round(money.UnitRupee).Paise() / 100 }

func bowStr(n int64) string { return strconv.FormatInt(n, 10) }

// ---- the limits ----

// BOWLimit is what a client's BOW deposits are to be.
type BOWLimit struct {
	Securities  money.Dec `json:"securities"`
	Commodities money.Dec `json:"commodities"`
	Close       bool      `json:"close,omitempty"` // closed: remove the rows
	Why         string    `json:"why,omitempty"`   // why 0 (blocked, inactive …)
	// Net is the signed funds of the BOW funds file (bowfiles.go): the
	// equity and commodity money of the open segments, debits kept; 0
	// whenever the limits are 0 (blocked, inactive …).
	Net money.Dec `json:"-"`
	// NetSec and NetCom are Net's two parts (the equity money of the open
	// equity segments, the commodity money of an open commodity segment),
	// for the funds file's securities and commodities lines.
	NetSec money.Dec `json:"-"`
	NetCom money.Dec `json:"-"`
}

// bowMoneyKinds are the ledger entries that move a client's money during
// the day (money in, payouts, allocations and transfers between ledgers).
var bowMoneyKinds = map[string]bool{"RECEIPT": true, "BANK": true, "GATEWAY": true, "PAYOUT": true, srcAllocation: true, srcLedgerXfer: true}

// bowLimits are every client's BOW deposits for a trading date as things
// stand now: the start-of-day balance (the same basis as Finspot's RMS
// limit, finspotFunds: the ledger at the end of the previous business day
// less cheques not yet cleared, and the BOW commodity funds file, bowFunds),
// plus money in and out posted since (the money kinds above), less pay-out
// holds in effect (equity only).
//
// Securities is equity money and Commodities MCX money, kept apart as the
// firm keeps them (Settings.segApart): with a separate commodity ledger
// (segledger.go) the equity and commodity ledgers; with one ledger and an
// MCX-only bank account (mcxearmark.go) the ledger less the MCX earmark and
// the earmark (today's money counts as MCX money when it is an MCX entry,
// mcxEntries, and approved RMS moves dated today shift it); otherwise the
// whole ledger is Securities and Commodities is 0.
//
// A blocked, inactive or not active client gets 0 on both, a client without
// an open commodity segment 0 Commodities, one without an open equity
// segment 0 Securities; a closed client's rows are removed. Debit balances
// are 0.
func (o *Ops) bowLimits(ctx context.Context, tenant, date string) (map[string]BOWLimit, error) {
	if !validDate(date) {
		return nil, invalid("give a date (YYYY-MM-DD)")
	}
	set, _ := o.Settings(ctx, tenant)
	apart, earmark := set.segApart(), set.mcxEarmark()
	eq, err := o.finspotFunds(ctx, tenant, date)
	if err != nil {
		return nil, err
	}
	com := map[string]money.Dec{}
	if apart {
		if com, err = o.bowFunds(ctx, tenant, date); err != nil {
			return nil, err
		}
	}
	prev := set.PrevBusinessDay(date)
	es, err := o.App.Ledger.Store.List(ctx, tenant, ledger.Filter{From: addDays(prev, 1), To: date})
	if err != nil {
		return nil, err
	}
	var mcxIDs map[string]bool
	if earmark {
		if mcxIDs, err = o.mcxEntries(ctx, tenant); err != nil {
			return nil, err
		}
		ms, _ := docs.List[MCXMove](ctx, o.Docs, tenant, cMCXMove, "")
		for _, m := range ms {
			if m.Status != "Approved" || m.Date <= prev || m.Date > date {
				continue
			}
			d := m.Amount.Dec()
			if m.To != EarmarkMCX {
				d = d.Neg()
			}
			com[m.UCC], eq[m.UCC] = com[m.UCC].Add(d), eq[m.UCC].Sub(d)
		}
	}
	for _, e := range es {
		if !bowMoneyKinds[e.Source.Kind] {
			continue
		}
		mcx := earmark && (mcxIDs[e.ID] || (e.ReversalOf != "" && mcxIDs[e.ReversalOf]))
		for _, p := range e.Postings {
			u, kind, ok := ledger.ClientCode(p.Account)
			if !ok {
				continue
			}
			d := (p.Credit - p.Debit).Dec()
			switch {
			case kind == ledger.LedgerCommodity && apart, kind == ledger.LedgerEquity && mcx:
				com[u] = com[u].Add(d)
			case kind == ledger.LedgerEquity:
				eq[u] = eq[u].Add(d)
			}
		}
	}
	for u, segs := range o.payoutHeld(ctx, tenant, date) {
		for _, v := range segs {
			eq[u] = eq[u].Sub(v)
		}
	}
	cs, err := o.ClientMap(ctx, tenant)
	if err != nil {
		return nil, err
	}
	zero := money.Dec{}
	out := make(map[string]BOWLimit, len(cs))
	for u, c := range cs {
		l := BOWLimit{Securities: money.Max(eq[u], zero), Commodities: money.Max(com[u], zero)}
		equityOpen, comOpen := false, false
		for _, s := range c.OpenSegments() {
			if strings.EqualFold(s, "COM") {
				comOpen = true
			} else {
				equityOpen = true
			}
		}
		switch {
		case c.Status == "Closed":
			l = BOWLimit{Close: true, Why: "account closed"}
		case c.Status != "Active":
			l = BOWLimit{Why: "client is " + strings.ToLower(firstOf(c.Status, "not active"))}
		case c.Blocked:
			l = BOWLimit{Why: "blocked" + firstOf(map[bool]string{true: ": " + c.BlockedReason}[c.BlockedReason != ""], "")}
		case c.Inactive:
			l = BOWLimit{Why: "inactive (no trades for the inactivity period)"}
		case !equityOpen && !comOpen:
			l = BOWLimit{Why: "no segment open"}
		default:
			if equityOpen {
				l.NetSec = eq[u]
			} else {
				l.Securities = zero
			}
			if comOpen {
				l.NetCom = com[u]
			} else {
				l.Commodities = zero
			}
			l.Net = l.NetSec.Add(l.NetCom)
		}
		out[u] = l
	}
	return out, nil
}

// ---- client mapping ----

// BOWUser is a client's login in BOW.
type BOWUser struct {
	USID       string `json:"usid"`
	LoginID    string `json:"login_id"`
	BackOffice string `json:"back_office_id,omitempty"`
	Name       string `json:"name,omitempty"`
}

// BOWOverride is a client's BOW user set by hand.
type BOWOverride struct {
	UCC   string  `json:"ucc"`
	User  BOWUser `json:"user"`
	By    string  `json:"by"`
	At    string  `json:"at"`
	Clear bool    `json:"clear,omitempty"` // removed: the master decides again
}

type bowMaster struct {
	Date    string             `json:"date"`
	At      string             `json:"at"`
	Users   map[string]BOWUser `json:"users"` // UCC -> user
	Total   int                `json:"total"`
	Unknown []string           `json:"unknown,omitempty"` // BOW logins matching no UCC (first 50)
}

// bowMatch is the UCC a BOW login stands for: the ID itself, or the part
// before the last "-" (CLIENT-T9065).
func bowMatch(id string, cs map[string]Client) string {
	id = strings.ToUpper(strings.TrimSpace(id))
	if id == "" {
		return ""
	}
	if _, ok := cs[id]; ok {
		return id
	}
	if i := strings.LastIndex(id, "-"); i > 0 {
		if _, ok := cs[id[:i]]; ok {
			return id[:i]
		}
	}
	return ""
}

// bowFetchMaster downloads BOW's client master and matches it to clients.
func (o *Ops) bowFetchMaster(c *bowCall, cs map[string]Client) (bowMaster, error) {
	recs, err := c.req("UserSearchServlet", "", "client master (active users)", url.Values{"MAXUSID": {"0"}, "userstatus": {"1"}})
	if err != nil {
		return bowMaster{}, err
	}
	if ok, msg := bowStatus(recs); !ok {
		return bowMaster{}, &bowStop{msg: "BOW refused the client master: " + msg}
	}
	m := bowMaster{Date: c.now().In(ist).Format("2006-01-02"), At: c.now().Format(time.RFC3339), Users: map[string]BOWUser{}}
	for _, r := range recs[1:] {
		if len(r) < 6 || strings.Trim(col(r, 0), "0123456789") != "" || col(r, 0) == "" {
			continue
		}
		if t := col(r, 6); t != "" && t != "1" && t != "4" { // normal and NRI clients only
			continue
		}
		m.Total++
		u := BOWUser{USID: col(r, 0), LoginID: col(r, 3), BackOffice: col(r, 4), Name: strings.TrimSpace(col(r, 1) + " " + col(r, 2))}
		ucc := firstOf(bowMatch(u.BackOffice, cs), bowMatch(u.LoginID, cs))
		if ucc == "" {
			if len(m.Unknown) < 50 {
				m.Unknown = append(m.Unknown, firstOf(u.LoginID, u.BackOffice))
			}
			continue
		}
		m.Users[ucc] = u
	}
	return m, nil
}

// bowUsers are the clients' BOW logins: overrides first, then the day's
// client master (fetched when it is not today's or refresh is set).
func (o *Ops) bowUsers(ctx context.Context, tenant string, c *bowCall, cs map[string]Client, refresh bool) (map[string]BOWUser, bowMaster, error) {
	m, _, _ := docs.GetOr(ctx, o.Docs, tenant, cBOWMaster, "master", bowMaster{})
	if c != nil && (refresh || m.Date != o.now().Format("2006-01-02")) {
		nm, err := o.bowFetchMaster(c, cs)
		if err != nil {
			return nil, m, err
		}
		m = nm
		_ = docs.Upsert(ctx, o.Docs, tenant, cBOWMaster, "master", m, SystemActor)
	}
	out := map[string]BOWUser{}
	for u, x := range m.Users {
		out[u] = x
	}
	ovs, _ := docs.List[BOWOverride](ctx, o.Docs, tenant, cBOWOverride, "")
	for _, ov := range ovs {
		if !ov.Clear {
			out[ov.UCC] = ov.User
		}
	}
	return out, m, nil
}

// SetBOWUser sets a client's BOW user by hand (empty USID: back to the
// client master).
func (o *Ops) SetBOWUser(ctx context.Context, tenant, actor, ucc string, u BOWUser) (BOWOverride, error) {
	if err := o.require(ctx, tenant, actor, "Mapping clients to BOW", RoleRisk); err != nil {
		return BOWOverride{}, err
	}
	ucc = strings.ToUpper(strings.TrimSpace(ucc))
	if _, err := o.Client(ctx, tenant, ucc); err != nil {
		return BOWOverride{}, invalid("client %s not found", ucc)
	}
	u.USID, u.LoginID, u.BackOffice = strings.TrimSpace(u.USID), strings.TrimSpace(u.LoginID), strings.TrimSpace(u.BackOffice)
	ov := BOWOverride{UCC: ucc, By: actor, At: o.now().Format(time.RFC3339)}
	if u.USID == "" {
		ov.Clear = true
	} else {
		if strings.Trim(u.USID, "0123456789") != "" {
			return ov, invalid("BOW's internal user ID is a number (USID in BOW's client master)")
		}
		if u.LoginID == "" {
			return ov, invalid("give the client's BOW login ID (e.g. %s-T9065)", ucc)
		}
		u.BackOffice = firstOf(u.BackOffice, u.LoginID)
		ov.User = u
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cBOWOverride, ucc, ov, actor); err != nil {
		return ov, err
	}
	o.log(ctx, tenant, actor, "bow.map", ucc, map[string]any{"usid": u.USID, "login": u.LoginID, "clear": ov.Clear})
	o.bowLimitChanged(ctx, tenant, ucc, "BOW user set")
	return ov, nil
}

// ---- deposit rows ----

// bowRow is one deposit row as sent.
type bowRow struct {
	ID       string // UDID: blank to add
	Level    int    // 0 user, 11 category
	Category string // "", "1" Securities, "2" Commodities
	Cash     money.Dec
	Mult     money.Dec
	State    int // 1 add/keep, 0 remove
}

// bowExisting is a client's rows at BOW (rupees, as listed).
type bowExisting struct {
	ID       string
	Category string // "", Securities, Commodities
	Cash     money.Dec
	Mult     money.Dec
}

func (x bowConn) mtmFactor() int64 {
	if x.mtm.IsZero() {
		return -1
	}
	return bowMultAmt(x.mtm)
}

// bowForm is EditUserDeposit for a client's rows.
func bowForm(x bowConn, u BOWUser, rows []bowRow) url.Values {
	f := url.Values{}
	for i, r := range rows {
		n := strconv.Itoa(i)
		cash, mult := bowAmt(r.Cash), bowMultAmt(r.Mult)
		eff := bowAmt(r.Cash.Mul(r.Mult))
		mtm, mtmLimit := x.mtmFactor(), int64(-1)
		if mtm > 0 {
			mtmLimit = bowAmt(r.Cash.Mul(r.Mult).Mul(x.mtm))
		}
		f.Set("UDID"+n, r.ID)
		f.Set("UDUSID"+n, u.USID)
		f.Set("UDDepositLevel"+n, strconv.Itoa(r.Level))
		f.Set("UDOrderType"+n, "1,2")
		f.Set("UDCategory"+n, r.Category)
		f.Set("UDExchange"+n, "")
		f.Set("UDMarket"+n, "")
		f.Set("UDSEGMENT"+n, "")
		f.Set("UDDeposit"+n, bowStr(eff))
		f.Set("UDROWSTATE"+n, strconv.Itoa(r.State))
		f.Set("UDAmount1"+n, bowStr(cash))
		f.Set("UDAmount2"+n, bowStr(mult))
		f.Set("UDADHOCMARGIN"+n, "0")
		f.Set("UDMarkToMarketFactor"+n, bowStr(mtm))
		f.Set("UDValidTill"+n, "")
		f.Set("UDFIELD5"+n, bowStr(mtmLimit))
		f.Set("UDFIELD6"+n, "1")
		f.Set("UDFIELD7"+n, "0")
	}
	f.Set("rowcount", strconv.Itoa(len(rows)))
	f.Set("USLOGINID", u.LoginID)
	f.Set("USBackOfficeId", firstOf(u.BackOffice, u.LoginID))
	return f
}

// bowSummary is the request as recorded: what each row sets.
func bowSummary(u BOWUser, rows []bowRow) string {
	var p []string
	for _, r := range rows {
		what := map[string]string{"": "user level", "1": "Securities", "2": "Commodities"}[r.Category]
		act := map[bool]string{true: "add", false: "modify " + r.ID}[r.ID == ""]
		if r.State == 0 {
			act = "remove " + r.ID
		}
		p = append(p, fmt.Sprintf("%s %s cash ₹%s × %s", act, what, r.Cash.Fixed(2), r.Mult.Fixed(4)))
	}
	return "EditUserDeposit user " + u.USID + " (" + u.LoginID + "): " + strings.Join(p, "; ")
}

// bowRowsFor are the three rows a limit gives: the user level, Securities
// and Commodities. The user level is the sum; with one multiplier for
// both it is cash × that multiplier, otherwise the effective sum × 1.
func bowRowsFor(x bowConn, l BOWLimit) []bowRow {
	sec := bowRow{Level: 11, Category: "1", Cash: l.Securities, Mult: x.secMult, State: 1}
	com := bowRow{Level: 11, Category: "2", Cash: l.Commodities, Mult: x.comMult, State: 1}
	user := bowRow{Level: 0, Cash: l.Securities.Add(l.Commodities), Mult: x.secMult, State: 1}
	if x.secMult.Cmp(x.comMult) != 0 {
		user.Cash, user.Mult = l.Securities.Mul(x.secMult).Add(l.Commodities.Mul(x.comMult)), money.DecInt(1)
	}
	return []bowRow{user, sec, com}
}

func bowDec(s string) money.Dec {
	d, err := money.ParseDec(strings.ReplaceAll(strings.TrimSpace(s), ",", ""))
	if err != nil {
		return money.Dec{}
	}
	return d
}

// bowListRows reads ListUserDeposit's answer: the overall rows (no
// exchange, market or product) by category.
func bowListRows(recs [][]string) map[string]bowExisting {
	out := map[string]bowExisting{}
	hi := -1
	for i, r := range recs {
		if col(r, 0) == "ID" {
			hi = i
			break
		}
	}
	if hi < 0 {
		return out
	}
	h := map[string]int{}
	for i, c := range recs[hi] {
		h[strings.ToLower(strings.TrimSpace(c))] = i
	}
	get := func(r []string, k string) string {
		if i, ok := h[k]; ok {
			return col(r, i)
		}
		return ""
	}
	// After the headers: column types, widths and positions, then the data.
	if hi+4 > len(recs) {
		return out
	}
	for _, r := range recs[hi+4:] {
		id := col(r, 0)
		if id == "" || strings.Trim(id, "0123456789") != "" {
			continue
		}
		if get(r, "exchange") != "" || get(r, "market") != "" || get(r, "product") != "" {
			continue // an exchange, market or product level row: not ours
		}
		cat := get(r, "category")
		if _, dup := out[cat]; dup {
			continue
		}
		out[cat] = bowExisting{ID: id, Category: cat, Cash: bowDec(get(r, "cash deposit")), Mult: bowDec(get(r, "multiplier"))}
	}
	return out
}

// bowPreEditIDs reads PreEditUserDeposit's answer: the UDIDs of the user
// level and the two level-11 categories ("", "1", "2").
func bowPreEditIDs(recs [][]string) map[string]string {
	out := map[string]string{}
	hi := -1
	for i, r := range recs {
		if col(r, 0) == "UDID" {
			hi = i
			break
		}
	}
	if hi < 0 {
		return out
	}
	h := map[string]int{}
	for i, c := range recs[hi] {
		h[strings.TrimSpace(c)] = i
	}
	at := func(r []string, k string) string {
		if i, ok := h[k]; ok {
			return col(r, i)
		}
		return ""
	}
	for _, r := range recs[hi+1:] {
		id := col(r, 0)
		if id == "" || strings.Trim(id, "0123456789") != "" || at(r, "UDExchange") != "" || at(r, "UDMarket") != "" || at(r, "UDSEGMENT") != "" {
			continue
		}
		switch lv := at(r, "UDDepositLevel"); {
		case lv == "0" || lv == "":
			if _, ok := out[""]; !ok {
				out[""] = id
			}
		case lv == "11" && (at(r, "UDCategory") == "1" || at(r, "UDCategory") == "2"):
			if _, ok := out[at(r, "UDCategory")]; !ok {
				out[at(r, "UDCategory")] = id
			}
		}
	}
	return out
}

var bowCatName = map[string]string{"": "", "1": "Securities", "2": "Commodities"}

// ---- pushing ----

// BOWClientState is a client's last push.
type BOWClientState struct {
	UCC         string    `json:"ucc"`
	USID        string    `json:"usid,omitempty"`
	LoginID     string    `json:"login_id,omitempty"`
	Securities  money.Dec `json:"securities"`  // as BOW has it after the push
	Commodities money.Dec `json:"commodities"` // as BOW has it after the push
	Status      string    `json:"status"`
	At          string    `json:"at"`
	Run         string    `json:"run,omitempty"`
	Error       string    `json:"error,omitempty"`
	Request     string    `json:"request,omitempty"`  // summary, no secrets
	Response    string    `json:"response,omitempty"` // BOW's words
	Why         string    `json:"why,omitempty"`      // why 0
}

type bowQueued struct {
	UCC     string `json:"ucc"`
	Pending bool   `json:"pending"`
	Why     string `json:"why,omitempty"`
	At      string `json:"at"`
}

type bowStateDoc struct {
	BODDate   string `json:"bod_date,omitempty"`  // start-of-day push done for
	BODTried  string `json:"bod_tried,omitempty"` // last attempt (RFC3339)
	LastSweep string `json:"last_sweep,omitempty"`
}

// BOWPushRun is one push.
type BOWPushRun struct {
	ID        string       `json:"id"`
	Date      string       `json:"date"`
	Kind      string       `json:"kind"` // Start of day, Intraday, By hand
	By        string       `json:"by"`
	At        string       `json:"at"`
	Finished  string       `json:"finished,omitempty"`
	Clients   int          `json:"clients"`
	Pushed    int          `json:"pushed"`
	Unchanged int          `json:"unchanged"`
	Removed   int          `json:"removed"`
	Failed    int          `json:"failed"`
	NotMapped []string     `json:"not_mapped,omitempty"`
	Stopped   string       `json:"stopped,omitempty"`
	Calls     []BSEUCCCall `json:"calls,omitempty"`
	MoreCalls int          `json:"more_calls,omitempty"`
}

// bowLimitChanged notes that a client's limit may have changed: the next
// scheduler tick recomputes it and pushes it if it did. Cheap: one read
// and one write, and nothing when BOW pushes are off.
func (o *Ops) bowLimitChanged(ctx context.Context, tenant, ucc, why string) {
	if ucc == "" {
		return
	}
	cn, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connBOW)
	if err != nil || cn.Values["send"] != "On" {
		return
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cBOWQueue, strings.ToUpper(ucc), bowQueued{UCC: strings.ToUpper(ucc), Pending: true, Why: why, At: o.now().Format(time.RFC3339)}, SystemActor)
}

func (o *Ops) bowQueuedUCCs(ctx context.Context, tenant string) []string {
	qs, _ := docs.List[bowQueued](ctx, o.Docs, tenant, cBOWQueue, "")
	var out []string
	for _, q := range qs {
		if q.Pending {
			out = append(out, q.UCC)
		}
	}
	sort.Strings(out)
	return out
}

func (o *Ops) bowDequeue(ctx context.Context, tenant, ucc, since string) {
	_, _ = docs.Update(ctx, o.Docs, tenant, cBOWQueue, ucc, SystemActor, func(q *bowQueued) error {
		if !q.Pending || q.At > since { // changed again during the push: keep it
			return docs.ErrStale
		}
		q.Pending = false
		return nil
	})
}

// BOWPush pushes clients' limits to BOW now (Risk): those named, or every
// client when none is.
func (o *Ops) BOWPush(ctx context.Context, tenant, actor, date string, uccs []string) (BOWPushRun, error) {
	if err := o.require(ctx, tenant, actor, "Pushing limits to BOW", RoleRisk); err != nil {
		return BOWPushRun{}, err
	}
	return o.bowPush(ctx, tenant, actor, firstOf(date, o.now().Format("2006-01-02")), "By hand", uccs, true)
}

// bowPush pushes limits. force pushes every client named (or all) even
// when BOW was last given the same values; otherwise only clients whose
// limit differs from the last push (or whose last push failed) go.
func (o *Ops) bowPush(ctx context.Context, tenant, actor, date, kind string, uccs []string, force bool) (BOWPushRun, error) {
	cn, x, err := o.bowConnection(ctx, tenant)
	if err != nil {
		return BOWPushRun{}, err
	}
	if cn.Status == "Problem" && actor == SystemActor {
		return BOWPushRun{}, fmt.Errorf("%w: the BOW connection has a problem: %s", ErrState, cn.Problem)
	}
	started := o.now().Format(time.RFC3339)
	n, err := o.next(ctx, tenant, "bowrun-"+date, actor)
	if err != nil {
		return BOWPushRun{}, err
	}
	run := BOWPushRun{ID: fmt.Sprintf("%s-%04d", date, n), Date: date, Kind: kind, By: actor, At: started}
	lims, err := o.bowLimits(ctx, tenant, date)
	if err != nil {
		return run, err
	}
	cs, _ := o.ClientMap(ctx, tenant)
	cctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	c := &bowCall{ctx: cctx, tenant: tenant, x: x, now: o.now}
	defer func() { o.bowFinishRun(ctx, tenant, actor, &run, c) }()

	want := map[string]bool{}
	for _, u := range uccs {
		want[strings.ToUpper(strings.TrimSpace(u))] = true
	}
	states := map[string]BOWClientState{}
	if all, _ := docs.List[BOWClientState](ctx, o.Docs, tenant, cBOWClient, ""); all != nil {
		for _, s := range all {
			states[s.UCC] = s
		}
	}
	var todo []string
	for _, u := range sortedKeys(lims) {
		if len(want) > 0 && !want[u] {
			continue
		}
		l, st := lims[u], states[u]
		had := st.Status != "" && st.Status != BOWNotMapped && st.Status != BOWRemoved
		switch {
		case l.Close && !had:
			continue // never given anything: nothing to remove
		case !l.Close && cs[u].Status != "Active" && !had:
			continue // not trading and never given anything
		case !force && st.Status != BOWFailed && st.Status != "" && st.Status != BOWNotMapped &&
			(l.Close == (st.Status == BOWRemoved)) && st.Securities.Cmp(l.Securities) == 0 && st.Commodities.Cmp(l.Commodities) == 0:
			continue // BOW already has it
		case !force && st.Status == BOWNotMapped && l.Securities.IsZero() && l.Commodities.IsZero():
			continue
		}
		todo = append(todo, u)
	}
	for u := range want {
		if _, ok := lims[u]; !ok {
			return run, invalid("client %s not found", u)
		}
	}
	run.Clients = len(todo)
	if len(todo) == 0 {
		return run, nil
	}
	users, _, err := o.bowUsers(ctx, tenant, c, cs, false)
	if err != nil {
		return run, o.bowStopped(ctx, tenant, &run, err)
	}
	for _, u := range todo {
		if cctx.Err() != nil {
			run.Stopped = "the run took too long; the rest go on the next check"
			break
		}
		l := lims[u]
		st := BOWClientState{UCC: u, At: o.now().Format(time.RFC3339), Run: run.ID, Why: l.Why, Securities: states[u].Securities, Commodities: states[u].Commodities}
		usr, ok := users[u]
		if !ok {
			st.Status = BOWNotMapped
			st.Error = "no BOW login for this client in BOW's client master: create the client in BOW, or set the BOW user by hand"
			run.NotMapped = append(run.NotMapped, u)
			_ = docs.Upsert(ctx, o.Docs, tenant, cBOWClient, u, st, actor)
			o.bowDequeue(ctx, tenant, u, started)
			continue
		}
		st.USID, st.LoginID = usr.USID, usr.LoginID
		status, req, resp, perr := o.bowPushClient(c, u, usr, l)
		st.Status, st.Request, st.Response = status, req, trimTo(resp, 500)
		var stop *bowStop
		if errors.As(perr, &stop) {
			return run, o.bowStopped(ctx, tenant, &run, perr)
		}
		if perr != nil {
			st.Status, st.Error = BOWFailed, perr.Error()
			run.Failed++
			o.raiseWork(ctx, tenant, wBOW, u, "BOW limit not set for "+u,
				fmt.Sprintf("BOW did not take client %s's limits (Securities ₹%s, Commodities ₹%s): %s. Check the client in the BOW Admin Terminal, then click Push to BOW on Live RMS.", u, l.Securities.Fixed(2), l.Commodities.Fixed(2), perr.Error()),
				u, RoleRisk, "live", date)
		} else {
			if l.Close {
				st.Securities, st.Commodities = money.Dec{}, money.Dec{}
			} else {
				st.Securities, st.Commodities = l.Securities, l.Commodities
			}
			switch status {
			case BOWUnchanged:
				run.Unchanged++
			case BOWRemoved:
				run.Removed++
			default:
				run.Pushed++
			}
			o.closeWork(ctx, tenant, actor, wBOW, u, "pushed")
		}
		_ = docs.Upsert(ctx, o.Docs, tenant, cBOWClient, u, st, actor)
		o.bowDequeue(ctx, tenant, u, started)
	}
	if len(run.NotMapped) > 0 && kind == "Start of day" {
		list := run.NotMapped
		if len(list) > 10 {
			list = append(append([]string{}, list[:10]...), fmt.Sprintf("and %d more", len(run.NotMapped)-10))
		}
		o.raiseWork(ctx, tenant, wBOW, "notmapped", fmt.Sprintf("%d client(s) not found in BOW", len(run.NotMapped)),
			"These clients have a limit but no login in BOW's client master, so BOW has no limit for them: "+strings.Join(list, ", ")+". Create them in BOW, or set each one's BOW user on Live RMS → BOW limits.", "", RoleRisk, "live", date)
	}
	o.closeWork(ctx, tenant, actor, wBOW, "run", "pushed")
	o.closeWork(ctx, tenant, actor, wBOW, "signin", "signed in")
	return run, nil
}

// bowStopped records a run that could not go on and asks Risk to act.
func (o *Ops) bowStopped(ctx context.Context, tenant string, run *BOWPushRun, err error) error {
	run.Stopped = err.Error()
	var st *bowStop
	if errors.As(err, &st) {
		switch {
		case st.auth:
			o.bowConnMark(ctx, tenant, "Problem", st.msg)
		case st.signIn:
			o.raiseWork(ctx, tenant, wBOW, "signin", "Sign in to BOW with today's OTP",
				"BOW limits wait for today's sign-in: on Live RMS → BOW limits click Sign in to BOW, enter the OTP BOW sends to the admin login's mobile, and the limits go out on the next check.", "", RoleRisk, "live", run.Date)
			return fmt.Errorf("%w: %s", ErrState, err.Error())
		}
	}
	o.raiseWork(ctx, tenant, wBOW, "run", "BOW limits not pushed",
		"The "+strings.ToLower(run.Kind)+" push to BOW stopped: "+err.Error()+". Limits not yet pushed go on the next check; Push to BOW on Live RMS sends them now.", "", RoleRisk, "live", run.Date)
	return fmt.Errorf("%w: %s", ErrState, err.Error())
}

func (o *Ops) bowFinishRun(ctx context.Context, tenant, actor string, run *BOWPushRun, c *bowCall) {
	run.Finished = o.now().Format(time.RFC3339)
	if run.Clients == 0 && len(c.calls) == 0 && run.Stopped == "" && run.Kind == "Intraday" {
		return // nothing changed: no record
	}
	run.Calls = c.calls
	if len(run.Calls) > bowMaxRunCall {
		run.MoreCalls = len(run.Calls) - bowMaxRunCall
		run.Calls = run.Calls[:bowMaxRunCall]
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cBOWRun, run.ID, *run, actor)
	o.log(ctx, tenant, actor, "bow.push", run.ID, map[string]any{"kind": run.Kind, "clients": run.Clients, "pushed": run.Pushed, "unchanged": run.Unchanged, "failed": run.Failed, "not_mapped": len(run.NotMapped), "stopped": run.Stopped})
}

// bowPushClient sets one client's rows: list what BOW has, then add the
// missing rows and modify (PreEdit, Edit) those at other values, or remove
// them for a closed client. A *bowStop error ends the run.
func (o *Ops) bowPushClient(c *bowCall, ucc string, u BOWUser, l BOWLimit) (status, req, resp string, err error) {
	recs, err := c.req("ListUserDeposit", ucc, "list deposits of "+u.LoginID, url.Values{"Exchange": {""}, "market": {""}, "segment": {""},
		"USBackOfficeId": {firstOf(u.BackOffice, u.LoginID)}, "group": {"Y"}, "APIDefaultScreenField": {"Y"}})
	if err != nil {
		return "", "", "", err
	}
	ok, msg := bowStatus(recs)
	if !ok {
		return "", "", msg, errors.New("BOW refused to list the client's deposits: " + msg)
	}
	have := bowListRows(recs)
	rows := bowRowsFor(c.x, l)
	if l.Close {
		var rm []bowRow
		for _, cat := range []string{"", "1", "2"} {
			if e, ok := have[bowCatName[cat]]; ok {
				rm = append(rm, bowRow{ID: e.ID, Level: map[bool]int{true: 0, false: 11}[cat == ""], Category: cat, Cash: e.Cash, Mult: e.Mult, State: 0})
			}
		}
		if len(rm) == 0 {
			return BOWRemoved, "nothing at BOW to remove", "", nil
		}
		if rm[0].Category != "" { // the user level goes first
			return "", "", "", errors.New("BOW has category rows but no user level row: remove them in the BOW Admin Terminal")
		}
		return o.bowEdit(c, ucc, u, rm, BOWRemoved)
	}
	same, any := true, false
	for _, r := range rows {
		e, ok := have[bowCatName[r.Category]]
		any = any || ok
		if !ok || bowAmt(e.Cash) != bowAmt(r.Cash) || bowMultAmt(e.Mult) != bowMultAmt(r.Mult) {
			same = false
		}
	}
	if same {
		return BOWUnchanged, "BOW already has these values", "", nil
	}
	if any {
		first := have[""].ID
		if first == "" {
			first = firstOf(have["Securities"].ID, have["Commodities"].ID)
		}
		precs, err := c.req("PreEditUserDeposit", ucc, "pre-edit deposit "+first, url.Values{"UDID": {first}})
		if err != nil {
			return "", "", "", err
		}
		if ok, msg := bowStatus(precs); !ok {
			return "", "", msg, errors.New("BOW refused to open the deposits for change: " + msg)
		}
		ids := bowPreEditIDs(precs)
		for i := range rows {
			if id := ids[rows[i].Category]; id != "" {
				rows[i].ID = id
			} else if e, ok := have[bowCatName[rows[i].Category]]; ok {
				rows[i].ID = e.ID
			}
		}
	}
	return o.bowEdit(c, ucc, u, rows, BOWPushed)
}

func (o *Ops) bowEdit(c *bowCall, ucc string, u BOWUser, rows []bowRow, done string) (status, req, resp string, err error) {
	sum := bowSummary(u, rows)
	recs, err := c.req("EditUserDeposit", ucc, sum, bowForm(c.x, u, rows))
	if err != nil {
		return "", sum, "", err
	}
	ok, msg := bowStatus(recs)
	if !ok {
		return "", sum, msg, errors.New("BOW refused the deposit: " + msg)
	}
	return done, sum, firstOf(msg, "done"), nil
}

// ---- the scheduler ----

// bowTick pushes when BOW pushes are on: the start-of-day push for every
// client once a business day at the firm's time (tried again every few
// minutes until it goes, e.g. after the OTP sign-in), then from 09:00 to
// 23:30 the clients whose limit may have changed (bowLimitChanged) at once,
// and every few minutes any client whose limit differs from the last push.
func (o *Ops) bowTick(ctx context.Context, tenant string, now time.Time) {
	cn, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connBOW)
	if err != nil || cn.Status == "Not set" || cn.Status == "" || cn.Status == "Problem" || cn.Values["send"] != "On" {
		return
	}
	x, err := parseBOWConn(cn)
	if err != nil {
		return
	}
	now = now.In(ist)
	date, hm := now.Format("2006-01-02"), now.Format("15:04")
	set, _ := o.Settings(ctx, tenant)
	if !set.IsBusinessDay(date) || hm < x.bodAt || hm >= bowDayEnd {
		return
	}
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cBOWState, "state", bowStateDoc{})
	if st.BODDate != date {
		if t, err := time.Parse(time.RFC3339, st.BODTried); err == nil && t.In(ist).Format("2006-01-02") == date && now.Sub(t) < x.every {
			return
		}
		st.BODTried = now.Format(time.RFC3339)
		_ = docs.Upsert(ctx, o.Docs, tenant, cBOWState, "state", st, SystemActor)
		run, err := o.bowPush(ctx, tenant, SystemActor, date, "Start of day", nil, true)
		if err == nil && run.Stopped == "" {
			st.BODDate, st.LastSweep = date, now.Format(time.RFC3339)
			_ = docs.Upsert(ctx, o.Docs, tenant, cBOWState, "state", st, SystemActor)
		}
		return
	}
	if hm < bowDayStart {
		return
	}
	sweep := true
	if t, err := time.Parse(time.RFC3339, st.LastSweep); err == nil && now.Sub(t) < x.every {
		sweep = false
	}
	q := o.bowQueuedUCCs(ctx, tenant)
	if !sweep && len(q) == 0 {
		return
	}
	if sweep {
		st.LastSweep = now.Format(time.RFC3339)
		_ = docs.Upsert(ctx, o.Docs, tenant, cBOWState, "state", st, SystemActor)
		q = nil // every client, the queued ones among them
	}
	_, _ = o.bowPush(ctx, tenant, SystemActor, date, "Intraday", q, false)
}

// ---- the screen ----

// BOWRow is a client on the BOW panel.
type BOWRow struct {
	UCC     string         `json:"ucc"`
	Name    string         `json:"name"`
	Limit   BOWLimit       `json:"limit"` // as computed now
	Last    BOWClientState `json:"last"`
	User    *BOWUser       `json:"user,omitempty"`
	ByHand  bool           `json:"by_hand,omitempty"` // BOW user set by hand
	Queued  bool           `json:"queued,omitempty"`
	Differs bool           `json:"differs,omitempty"` // the limit differs from the last push
}

// BOWView is the BOW panel on Live RMS.
type BOWView struct {
	Date       string       `json:"date"`
	Status     string       `json:"status"` // the connection's
	Problem    string       `json:"problem,omitempty"`
	Env        string       `json:"env"`
	Auto       bool         `json:"auto"`
	BODAt      string       `json:"bod_at"`
	Every      int          `json:"every"`
	BODDone    bool         `json:"bod_done"`
	Session    BOWSession   `json:"session"`
	SecMult    string       `json:"sec_mult"`
	ComMult    string       `json:"com_mult"`
	MasterDate string       `json:"master_date,omitempty"`
	MasterSize int          `json:"master_size"`
	Rows       []BOWRow     `json:"rows"`
	Runs       []BOWPushRun `json:"runs"` // latest first, without calls
	Note       string       `json:"note,omitempty"`
}

// BOWLimitsView is the BOW panel: each client's limit now and last push.
func (o *Ops) BOWLimitsView(ctx context.Context, tenant, actor, date string) (BOWView, error) {
	if err := o.require(ctx, tenant, actor, "Viewing BOW limits", RoleRisk, RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return BOWView{}, err
	}
	cn, x, err := o.bowConnection(ctx, tenant)
	if err != nil {
		return BOWView{}, err
	}
	date = firstOf(date, o.now().Format("2006-01-02"))
	v := BOWView{Date: date, Status: cn.Status, Problem: cn.Problem, Env: map[bool]string{true: "Live", false: "UAT"}[x.live], Auto: x.auto, BODAt: x.bodAt,
		Every: int(x.every / time.Minute), Session: o.bowSessionState(tenant, x), SecMult: x.secMult.String(), ComMult: x.comMult.String()}
	if set, _ := o.Settings(ctx, tenant); !set.segApart() {
		v.Note = "One client ledger and no MCX-only bank account: the whole balance goes to Securities and Commodities is 0, so BOW gives no MCX limit. Set the MCX-only bank account (or a separate commodity ledger) in Settings for MCX money to go to Commodities."
	}
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cBOWState, "state", bowStateDoc{})
	v.BODDone = st.BODDate == date
	lims, err := o.bowLimits(ctx, tenant, date)
	if err != nil {
		return v, err
	}
	cs, _ := o.ClientMap(ctx, tenant)
	users, m, _ := o.bowUsers(ctx, tenant, nil, cs, false)
	v.MasterDate, v.MasterSize = m.Date, m.Total
	ovs, _ := docs.List[BOWOverride](ctx, o.Docs, tenant, cBOWOverride, "")
	byHand := map[string]bool{}
	for _, ov := range ovs {
		byHand[ov.UCC] = !ov.Clear
	}
	queued := map[string]bool{}
	for _, u := range o.bowQueuedUCCs(ctx, tenant) {
		queued[u] = true
	}
	states := map[string]BOWClientState{}
	all, _ := docs.List[BOWClientState](ctx, o.Docs, tenant, cBOWClient, "")
	for _, s := range all {
		states[s.UCC] = s
	}
	for _, u := range sortedKeys(lims) {
		l, s := lims[u], states[u]
		if cs[u].Status != "Active" && s.Status == "" {
			continue
		}
		r := BOWRow{UCC: u, Name: cs[u].Name, Limit: l, Last: s, ByHand: byHand[u], Queued: queued[u]}
		if usr, ok := users[u]; ok {
			r.User = &usr
		}
		r.Differs = s.Status == "" || s.Status == BOWFailed || s.Securities.Cmp(l.Securities) != 0 || s.Commodities.Cmp(l.Commodities) != 0
		v.Rows = append(v.Rows, r)
	}
	runs, _ := docs.List[BOWPushRun](ctx, o.Docs, tenant, cBOWRun, "")
	sort.Slice(runs, func(i, j int) bool {
		return runs[i].At > runs[j].At || (runs[i].At == runs[j].At && runs[i].ID > runs[j].ID)
	})
	if len(runs) > 10 {
		runs = runs[:10]
	}
	for i := range runs {
		runs[i].Calls = nil
	}
	v.Runs = runs
	return v, nil
}

// BOWRun is one push with its calls.
func (o *Ops) BOWRun(ctx context.Context, tenant, actor, id string) (BOWPushRun, error) {
	if err := o.require(ctx, tenant, actor, "Viewing BOW limits", RoleRisk, RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return BOWPushRun{}, err
	}
	r, _, err := docs.Get[BOWPushRun](ctx, o.Docs, tenant, cBOWRun, id)
	return r, err
}

// BOWRefreshMaster fetches BOW's client master again (Risk).
func (o *Ops) BOWRefreshMaster(ctx context.Context, tenant, actor string) (int, int, error) {
	if err := o.require(ctx, tenant, actor, "Mapping clients to BOW", RoleRisk); err != nil {
		return 0, 0, err
	}
	_, x, err := o.bowConnection(ctx, tenant)
	if err != nil {
		return 0, 0, err
	}
	cs, _ := o.ClientMap(ctx, tenant)
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	c := &bowCall{ctx: cctx, tenant: tenant, x: x, now: o.now}
	_, m, err := o.bowUsers(ctx, tenant, c, cs, true)
	if err != nil {
		return 0, 0, fmt.Errorf("%w: %s", ErrState, err.Error())
	}
	o.log(ctx, tenant, actor, "bow.master", connBOW, map[string]any{"users": m.Total, "matched": len(m.Users)})
	return m.Total, len(m.Users), nil
}
