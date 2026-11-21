package ops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	"ledgerline/internal/money"
)

// ---- Finspot by API (Noren WC Admin) ----
//
// Finspot runs on Kambala Solutions' Noren OMS, whose admin API
// (NorenWCAdmin) takes what the Finspot upload files carry (finspot.go),
// one client at a time. This pushes them from Ledgerline: the limits at the
// start of the day and again during the day when money comes in or goes
// out, the ISIN and T1 holdings at the start of the day, and, when the firm
// switches them on, blocks, new and changed client logins and the margin
// baskets. The files stay: NFO_COST_PRICE, the dealer-client positions and
// the dealer order types have no API, and any file can still be uploaded
// by hand.
//
// What the API document gives (only what the code needs):
//   - Every request is POSTed to <base>/<Name> with the body
//     jData=<json>&jKey=<session token>; every value in the JSON is a
//     string ("One or more input parameters are not in string format"), and
//     an "&" inside a value is sent as %26. Answers are JSON: an object with
//     stat Ok or Not_Ok and emsg on failure, or an array (Holdings).
//   - AdminLogin: uid, pwd (SHA-256 of the password), vc (the vendor code
//     from the Noren team), appkey (SHA-256 of "uid|vendor key"), imei (the
//     machine's MAC address), apkversion. The answer's susertoken is the
//     jKey of later requests; brkname is the broker ID (src_bid). The
//     server's IP must be allowed (401 "Ip does not match"); passwords run
//     out ("Password Expired", "Change Password") and wrong ones lock the
//     login ("User Blocked due to multiple wrong attempts"); during EOD and
//     BOD the OMS answers "unknown request" or "Server Timeout".
//   - SetLimits (actid, cash, brkcollamt, …, all in rupees) for the RMS
//     Limits line; UpdateAbsHoldingsISIN (isin, hold_qty, colqty, col_mode:
//     0 close price, 1 LTP, 2 WC, hair_cut, close_prc, upload_prc, …) for
//     the ISIN Holding line, NpoaISINHoldings (with dp_acct_num) for the
//     holdings of clients without a POA; UpdateBTSTAbsHoldings (by exchange
//     token, exch_tks) for the T1 line; BlockAcct / UnBlockAcct and
//     FreezeAccount / DeFreezeAccount; AddInvestorClient, ModifyClient,
//     InvClientSegEnabled, InvClientProductEnabled; SetMarginBasket
//     (token_array, margin_perc_array); Limits and Holdings to read back.
//   - The document gives no rate, no session lifetime and no address: the
//     Noren team gives the address with the vendor code.
//
// Choices where the document is not explicit:
//   - The limit is SetLimits with cash the client's equity money as things
//     stand (liveFunds: the start-of-day balance as the RMS Limits line has
//     it, plus money in and out posted since, less pay-out holds), signed:
//     a debit is sent as is, as the file's column 5 is. The file's notional
//     cash (column 25) has no field of that name: it goes in the field the
//     connection names (prevalued collateral, brkcollamt, unless Finspot
//     says otherwise). A client who may not take new positions gets the
//     lower of the money and 0 and no notional cash, as in the file.
//   - Holdings are absolute: every line of the day's file is sent, and an
//     ISIN Ledgerline sent before that the client no longer holds is sent
//     with quantity 0, so Finspot never keeps a stale holding whether or not
//     it clears holdings at its BOD. T1 lines need the ISIN's NSE or BSE
//     token, from the day's common bhavcopy (Instrument.Token) or the BSE
//     scrip code (SecRef); a line without one is not sent and Risk is told.
//   - Blocks go by the block file unless the connection says otherwise: the
//     API blocks a whole account (or all fresh orders), where the file
//     blocks MIS and NRML and only CNC buys, so a blocked client can still
//     sell holdings. The block list and its state are the file's
//     (finspotBlockPlan), so file and API never disagree.
//   - New clients go by AddInvestorClient without a password (Finspot sets
//     it, as with the file); changed ones by ModifyClient, which needs no
//     password (ModifyInvestorClient asks for one), and their segments and
//     products again. Passwords, the password hash, the vendor key and the
//     session token are never stored, logged or kept in a call record; the
//     token lives in memory for the IST day. A refused sign-in stops
//     automatic pushes (the connection shows Problem) so a locked login is
//     never tried again by itself.
//   - Requests go a few at a time (the connection's parallel requests,
//     default 4), each worker spaced by finspotGap, as the document sets no
//     rate: a morning's holdings are one request per client and ISIN.
//   - Each morning part can go by its file instead (start-of-day limits,
//     ISIN holdings, T1 holdings), with only the day's limit changes by
//     API. With the start-of-day limits by the RMS Limits file, the
//     start-of-day push leaves limits out; a client's limit goes by API
//     during the day only when it differs from the line the day's file
//     gave (or from the last push of the day), and not before the day's
//     RMS Limits file is made, so a later upload of the morning file never
//     overwrites a change sent before it. The parts by file stay on the
//     daily file checklist (finspotChecklist) and the pay-out holds' BOD
//     gate waits for the limit file (payoutBODState).

const (
	connFinspotAPI = "finspot-api"
	cFNClient      = "finspotapiclient" // UCC -> FinspotAPIClient (limits)
	cFNHold        = "finspotapihold"   // UCC -> FinspotAPIHolding
	cFNQueue       = "finspotapiqueue"  // UCC -> bowQueued
	cFNRun         = "finspotapirun"    // run id -> FinspotAPIRun
	cFNState       = "finspotapistate"  // "state" -> fnStateDoc
	cFNSent        = "finspotapisent"   // client|UCC -> finspotSentState
	wFinspotAPI    = "finspotapi"
	fnDayStart     = "09:00"
	fnDayEnd       = "20:00"
	fnMaxRunCall   = 300 // calls kept on a run's record (failures beyond, up to fnMaxFailCall)
	fnMaxFailCall  = 200
	fnMaxFailures  = 100

	FNPartLimits   = "limits"
	FNPartHoldings = "holdings"
	FNPartT1       = "t1"
	FNPartBlocks   = "blocks"
	FNPartClients  = "clients"
	FNPartBaskets  = "baskets"

	FNPushed    = "Pushed"
	FNUnchanged = "Unchanged"
	FNFailed    = "Failed"

	fnNotionalColl = "Prevalued collateral (brkcollamt)"
	fnNotionalDay  = "Day cash (daycash)"
	fnNotionalNone = "Not sent"
	fnBlocksFile   = "By the block file"
	fnBlocksAcct   = "Block the account (BlockAcct)"
	fnBlocksFreeze = "Block fresh orders (FreezeAccount)"
	fnNPOASame     = "Same call as other holdings"
	fnNPOACall     = "NPOA ISIN Holdings call"
	fnBODByAPI     = "By API"
	fnBODByFile    = "By the RMS Limits file"
	fnT1Same       = "Same as ISIN holdings"
	fnT1ByAPI      = "By API"
	fnT1ByFile     = "By the T1 file"
)

// fnPartFile is the daily file checklist item of a morning part sent by
// its file (dailyfiles.go).
var fnPartFile = map[string]string{FNPartLimits: "finspot-limit", FNPartHoldings: "finspot-holding", FNPartT1: "finspot-t1"}

// fnParts are the parts of a push, in the order they go.
var fnParts = []string{FNPartBlocks, FNPartLimits, FNPartHoldings, FNPartT1, FNPartBaskets, FNPartClients}

var fnPartName = map[string]string{FNPartBlocks: "Blocks", FNPartLimits: "Limits", FNPartHoldings: "ISIN holdings", FNPartT1: "T1 holdings", FNPartBaskets: "Margin baskets", FNPartClients: "Client logins"}

func init() {
	ConnProviders = append(ConnProviders, ConnProvider{ID: connFinspotAPI, Name: "Finspot by API (Noren WC Admin)", Group: "Trading", Test: "https",
		Purpose: "Sends Finspot what its upload files carry, by the admin API of Noren (Kambala), the OMS Finspot runs on: limits at the start of the day and again when money comes in or goes out, ISIN and T1 holdings at the start of the day, and, when switched on here, blocks, new and changed client logins and the margin baskets. Needs an admin login enabled for the API, the vendor code and vendor key the Noren team gives with the address, and this server's IP allowed by Finspot. The firm's Finspot values (account ID suffix, exchange users, order types, baskets) stay in Finspot uploads",
		Fields: []ConnField{
			{Key: "env", Label: "Environment", Options: []string{"UAT", "Live"}},
			{Key: "url", Label: "API address from Finspot, ending /NorenWCAdmin (https)"},
			{Key: "user", Label: "Admin login ID (uid)"},
			{Key: "password", Label: "Password", Secret: true},
			{Key: "vendor_code", Label: "Vendor code (vc, from the Noren team)"},
			{Key: "vendor_key", Label: "Vendor key (for the app key)", Secret: true},
			{Key: "broker_id", Label: "Broker ID (src_bid; default the one the sign-in gives)", Optional: true},
			{Key: "mac", Label: "This server's MAC address as registered with Finspot (imei; default LEDGERLINE_SERVER_MAC)", Optional: true},
			{Key: "app_version", Label: "App version (apkversion, default 1.0.0)", Optional: true, Placeholder: "1.0.0"},
			{Key: "send", Label: "Push to Finspot automatically (start of day, then changes to 20:00)", Options: []string{"Off", "On"}},
			{Key: "bod_at", Label: "Start-of-day push at (HH:MM before 09:00, default 08:40)", Optional: true, Placeholder: "08:40"},
			{Key: "every", Label: "Check for changed limits every (minutes, default 5)", Optional: true, Placeholder: "5"},
			{Key: "parallel", Label: "Requests at a time (1 to 8, default 4)", Optional: true, Placeholder: "4"},
			{Key: "notional", Label: "Notional cash (RMS Limits column 25) goes in (confirm with Finspot)", Options: []string{fnNotionalColl, fnNotionalDay, fnNotionalNone}, Optional: true},
			{Key: "bod_limits", Label: "Start-of-day limits (default by API; by the file, only the day's changes go by API)", Options: []string{fnBODByAPI, fnBODByFile}, Optional: true},
			{Key: "holdings", Label: "ISIN holdings by API (Off: the ISIN Holding file)", Options: []string{"On", "Off"}, Optional: true},
			{Key: "t1_holdings", Label: "T1 holdings (default the same as ISIN holdings)", Options: []string{fnT1Same, fnT1ByAPI, fnT1ByFile}, Optional: true},
			{Key: "npoa", Label: "Holdings of clients without a POA or DDPI", Options: []string{fnNPOASame, fnNPOACall}, Optional: true},
			{Key: "blocks", Label: "Blocks", Options: []string{fnBlocksFile, fnBlocksAcct, fnBlocksFreeze}, Optional: true},
			{Key: "clients", Label: "New and changed client logins by API", Options: []string{"Off", "On"}, Optional: true},
			{Key: "access_types", Label: "Client access types for new logins (default WEB,MOB)", Optional: true, Placeholder: "WEB,MOB"},
			{Key: "baskets", Label: "Margin baskets by API at the start of the day", Options: []string{"Off", "On"}, Optional: true},
			fNotes}})
}

// ---- the connection ----

type fnConn struct {
	base, user, pass, vendor, vendorKey, broker, mac, version string
	live, auto, holdings, npoaCall, clients, baskets          bool
	t1, bodLimitsFile                                         bool
	bodAt, notional, blocks                                   string
	access                                                    []string
	every                                                     time.Duration
	parallel                                                  int
}

func (x fnConn) fp() string {
	s := sha256.Sum256([]byte(x.base + "\x00" + x.user + "\x00" + x.pass + "\x00" + x.vendor + "\x00" + x.vendorKey))
	return hex.EncodeToString(s[:])
}

// enabled says whether a part goes by API on this connection.
func (x fnConn) enabled(part string) bool {
	switch part {
	case FNPartLimits:
		return true
	case FNPartHoldings:
		return x.holdings
	case FNPartT1:
		return x.t1
	case FNPartBlocks:
		return x.blocks != fnBlocksFile
	case FNPartClients:
		return x.clients
	case FNPartBaskets:
		return x.baskets
	}
	return false
}

// byFile are the morning parts the firm sends by their files rather than
// by API: start-of-day limits, ISIN holdings, T1 holdings.
func (x fnConn) byFile() []string {
	var out []string
	if x.bodLimitsFile {
		out = append(out, FNPartLimits)
	}
	if !x.holdings {
		out = append(out, FNPartHoldings)
	}
	if !x.t1 {
		out = append(out, FNPartT1)
	}
	return out
}

// notionalField is the SetLimits field notional cash goes in ("" none).
func (x fnConn) notionalField() string {
	switch x.notional {
	case fnNotionalDay:
		return "daycash"
	case fnNotionalNone:
		return ""
	}
	return "brkcollamt"
}

func parseFinspotAPIConn(c Connection) (fnConn, error) {
	v := c.Values
	x := fnConn{base: strings.TrimRight(strings.TrimSpace(v["url"]), "/"), user: strings.TrimSpace(v["user"]), pass: c.Secrets["password"],
		vendor: strings.TrimSpace(v["vendor_code"]), vendorKey: c.Secrets["vendor_key"], broker: strings.TrimSpace(v["broker_id"]),
		mac: firstOf(strings.TrimSpace(v["mac"]), firstOf(os.Getenv("LEDGERLINE_SERVER_MAC"), "LEDGERLINE")), version: firstOf(strings.TrimSpace(v["app_version"]), "1.0.0"),
		live: v["env"] == "Live", auto: v["send"] == "On", holdings: v["holdings"] != "Off", npoaCall: v["npoa"] == fnNPOACall,
		clients: v["clients"] == "On", baskets: v["baskets"] == "On", bodAt: firstOf(strings.TrimSpace(v["bod_at"]), "08:40"),
		notional: firstOf(v["notional"], fnNotionalColl), blocks: firstOf(v["blocks"], fnBlocksFile), every: 5 * time.Minute, parallel: 4}
	switch v["t1_holdings"] {
	case "", fnT1Same:
		x.t1 = x.holdings
	case fnT1ByAPI:
		x.t1 = true
	case fnT1ByFile:
	default:
		return x, invalid("T1 holdings go %s, %s or %s", fnT1Same, fnT1ByAPI, fnT1ByFile)
	}
	switch v["bod_limits"] {
	case "", fnBODByAPI:
	case fnBODByFile:
		x.bodLimitsFile = true
	default:
		return x, invalid("start-of-day limits go %s or %s", fnBODByAPI, fnBODByFile)
	}
	if x.base == "" || x.user == "" || x.pass == "" || x.vendor == "" || x.vendorKey == "" {
		return x, invalid("the Finspot API connection needs the address, the admin login ID and password, the vendor code and the vendor key")
	}
	if u, err := url.Parse(x.base); err != nil || u.Scheme != "https" || u.Host == "" {
		return x, invalid("the Finspot API address starts with https://")
	}
	if !reBOWHHMM.MatchString(x.bodAt) || x.bodAt >= fnDayStart {
		return x, invalid("the start-of-day push time is HH:MM before %s", fnDayStart)
	}
	if e := strings.TrimSpace(v["every"]); e != "" {
		n, err := strconv.Atoi(e)
		if err != nil || n < 1 || n > 60 {
			return x, invalid("check for changed limits every 1 to 60 minutes")
		}
		x.every = time.Duration(n) * time.Minute
	}
	if p := strings.TrimSpace(v["parallel"]); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 8 {
			return x, invalid("requests at a time is 1 to 8")
		}
		x.parallel = n
	}
	for _, a := range strings.FieldsFunc(firstOf(v["access_types"], "WEB,MOB"), func(r rune) bool { return r == ',' || r == ' ' || r == ';' }) {
		a = strings.ToUpper(a)
		if !contains([]string{"WEB", "MOB", "TT", "API"}, a) {
			return x, invalid("client access types are WEB, MOB, TT or API (Finspot's TWS is TT)")
		}
		x.access = append(x.access, a)
	}
	return x, nil
}

// checkFinspotAPIConn is SaveConnection's check of the Finspot API fields.
func checkFinspotAPIConn(c Connection) error {
	_, err := parseFinspotAPIConn(c)
	return err
}

func (o *Ops) finspotAPIConnection(ctx context.Context, tenant string) (Connection, fnConn, error) {
	cn, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connFinspotAPI)
	if err != nil || cn.Status == "Not set" || cn.Status == "" {
		return cn, fnConn{}, fmt.Errorf("%w: set up Finspot by API in Settings → Connections", ErrState)
	}
	x, err := parseFinspotAPIConn(cn)
	return cn, x, err
}

func (o *Ops) fnConnMark(ctx context.Context, tenant, status, problem string) {
	_, _ = docs.Update(ctx, o.Docs, tenant, cConn, connFinspotAPI, SystemActor, func(c *Connection) error {
		if c.Status == "Not set" {
			return docs.ErrStale
		}
		c.Status, c.Problem, c.TestedAt, c.TestedBy = status, problem, o.now().Format(time.RFC3339), SystemActor
		return nil
	})
}

// ---- talking to Finspot ----

// finspotHTTP talks to Finspot; tests replace it.
var finspotHTTP = &http.Client{Timeout: 30 * time.Second}

// finspotGap spaces each worker's requests (the document gives no rate);
// tests set it to 0.
var finspotGap = 20 * time.Millisecond

// fnSessions holds each tenant's session token for the IST day, in memory only.
var fnSessions sync.Map // tenant -> fnSession

type fnSession struct {
	key, broker, fp, day string
	at                   time.Time
}

// fnStop ends a run: auth when Finspot refused the sign-in (automatic
// pushes stop until the connection is saved again); otherwise Finspot is
// not taking requests now and the next check tries again.
type fnStop struct {
	msg  string
	auth bool
}

func (e *fnStop) Error() string { return e.msg }

// fnAnswer is a Finspot answer: an object, or an array of objects.
type fnAnswer struct {
	obj map[string]any
	arr []map[string]any
}

func parseFN(b []byte) (fnAnswer, error) {
	var a fnAnswer
	t := strings.TrimSpace(string(b))
	switch {
	case strings.HasPrefix(t, "["):
		if err := json.Unmarshal([]byte(t), &a.arr); err != nil {
			return a, err
		}
	case strings.HasPrefix(t, "{"):
		if err := json.Unmarshal([]byte(t), &a.obj); err != nil {
			return a, err
		}
	default:
		return a, errors.New("Finspot's answer is not JSON")
	}
	return a, nil
}

// fnStr is a JSON value as text.
func fnStr(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	}
	return fmt.Sprint(v)
}

func (a fnAnswer) get(k string) string { return fnStr(a.obj[k]) }

// ok is whether Finspot did it, with its message when not.
func (a fnAnswer) ok() (bool, string) {
	if a.arr != nil {
		return true, ""
	}
	if strings.EqualFold(a.get("stat"), "Ok") || strings.EqualFold(a.get("status"), "Ok") {
		return true, ""
	}
	return false, firstOf(a.get("emsg"), "Finspot refused the request without a reason")
}

var (
	reFNSession = regexp.MustCompile(`(?i)session expired|invalid session`)
	reFNIP      = regexp.MustCompile(`(?i)ip does not match`)
	reFNDown    = regexp.MustCompile(`(?i)unknown request|server timeout|request_under_process|request under process`)
	reFNAuth    = regexp.MustCompile(`(?i)wrong password|invalid user|deactivated|user blocked|password expired|change password|invalid vendor|invalid app ?key|not allowed|version blocked|latest app|not enabled on|wrong user id`)
)

type fnCall struct {
	ctx      context.Context
	tenant   string
	x        fnConn
	now      func() time.Time
	mu       sync.Mutex // calls, more, failCalls
	calls    []BSEUCCCall
	more     int
	failKept int
	loginMu  sync.Mutex
	loginErr error // a sign-in that failed: never tried again in this run
}

// fnBody is a request body: jData, and jKey when signed in. An "&" in a
// value would end jData early, so it is sent as %26 as the document asks.
func fnBody(jd map[string]any, key string) (string, error) {
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(jd); err != nil {
		return "", err
	}
	s := "jData=" + strings.ReplaceAll(strings.TrimSpace(buf.String()), "&", "%26")
	if key != "" {
		s += "&jKey=" + key
	}
	return s, nil
}

// do sends one request; the answer comes back as is.
func (c *fnCall) do(name, body string) (int, []byte, error) {
	if finspotGap > 0 {
		select {
		case <-c.ctx.Done():
			return 0, nil, c.ctx.Err()
		case <-time.After(finspotGap):
		}
	}
	req, err := http.NewRequestWithContext(c.ctx, http.MethodPost, c.x.base+"/"+name, strings.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	resp, err := finspotHTTP.Do(req)
	if err != nil {
		msg := err.Error()
		if i := strings.LastIndex(msg, "\": "); i >= 0 {
			msg = msg[i+3:]
		}
		return 0, nil, fmt.Errorf("could not reach Finspot: %s", msg)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	return resp.StatusCode, b, err
}

// record keeps a call on the run's record: the first fnMaxRunCall, then
// failures up to fnMaxFailCall; the rest are counted.
func (c *fnCall) record(call BSEUCCCall) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case len(c.calls) < fnMaxRunCall:
		c.calls = append(c.calls, call)
	case call.Error != "" && c.failKept < fnMaxFailCall:
		c.calls = append(c.calls, call)
		c.failKept++
	default:
		c.more++
	}
}

// login signs in: the session token and the broker ID.
func (c *fnCall) login() (key, broker string, err error) {
	jd := map[string]any{"apkversion": c.x.version, "uid": c.x.user, "pwd": sha256Hex([]byte(c.x.pass)), "vc": c.x.vendor,
		"appkey": sha256Hex([]byte(c.x.user + "|" + c.x.vendorKey)), "imei": c.x.mac}
	if ip := os.Getenv("LEDGERLINE_SERVER_IP"); ip != "" {
		jd["ipaddr"] = ip
	}
	body, _ := fnBody(jd, "")
	code, b, err := c.do("AdminLogin", body)
	call := BSEUCCCall{Function: "AdminLogin", Request: "sign in " + c.x.user + " (password not recorded)", Code: code}
	defer func() { c.record(call) }()
	if err != nil {
		call.Error = err.Error()
		return "", "", &fnStop{msg: err.Error()}
	}
	a, perr := parseFN(b)
	msg := a.get("emsg")
	if code == 401 || reFNIP.MatchString(msg) {
		call.Error = firstOf(msg, fmt.Sprintf("HTTP %d", code))
		return "", "", &fnStop{msg: "Finspot refused this server (" + call.Error + "): ask Finspot to allow this server's IP address for the API", auth: true}
	}
	if code == 504 || reFNDown.MatchString(msg) {
		call.Error = firstOf(msg, "HTTP 504")
		return "", "", &fnStop{msg: "Finspot is not taking requests now (" + call.Error + "): the next check tries again"}
	}
	if perr != nil || code/100 != 2 {
		call.Error = fmt.Sprintf("HTTP %d: %s", code, trimBody(b))
		return "", "", &fnStop{msg: fmt.Sprintf("Finspot's server answered %d to the sign-in", code)}
	}
	if ok, m := a.ok(); !ok {
		call.Response, call.Error = "refused", m
		return "", "", &fnStop{msg: "Finspot refused the sign-in (" + m + "): check the admin login, password, vendor code and vendor key in Connections. Wrong passwords lock the login, so automatic pushes stop until the connection is saved again", auth: true}
	}
	if strings.EqualFold(a.get("spasswordreset"), "Y") {
		call.Response = "password change asked"
		return "", "", &fnStop{msg: "Finspot asks for the admin password to be changed: change it on Finspot and save the new one in Connections", auth: true}
	}
	key = a.get("susertoken")
	if key == "" {
		call.Error = "no session token"
		return "", "", &fnStop{msg: "Finspot's sign-in gave no session token", auth: true}
	}
	call.Response = "signed in" // never the token
	return key, firstOf(c.x.broker, a.get("brkname")), nil
}

// session gives the day's session: the one held, or a new sign-in when
// force or stale (the key a request found expired) says so.
func (c *fnCall) session(stale string) (fnSession, error) {
	c.loginMu.Lock()
	defer c.loginMu.Unlock()
	if c.loginErr != nil {
		return fnSession{}, c.loginErr // one refused sign-in a run: wrong passwords lock the login
	}
	day := c.now().In(ist).Format("2006-01-02")
	if v, ok := fnSessions.Load(c.tenant); ok {
		if s := v.(fnSession); s.fp == c.x.fp() && s.day == day && s.key != "" && s.key != stale {
			return s, nil
		}
	}
	fnSessions.Delete(c.tenant)
	key, broker, err := c.login()
	if err != nil {
		c.loginErr = err
		return fnSession{}, err
	}
	s := fnSession{key: key, broker: broker, fp: c.x.fp(), day: day, at: c.now()}
	fnSessions.Store(c.tenant, s)
	return s, nil
}

// The source fields a request carries besides uid, as the document lists
// them per request.
const (
	fnSrcUID   = 1 << iota // src_uid: the admin login
	fnSrcBID               // src_bid: the broker ID
	fnBrokerID             // broker_id: the broker ID
)

// req sends a request with the session, signing in again once when Finspot
// says the session ended. uid, and the source fields src names, are filled
// in. ucc and summary go on the record, never the request itself. A
// *fnStop error ends the run; any other error is this request's refusal.
func (c *fnCall) req(name, ucc, summary string, jd map[string]any, src int) (fnAnswer, error) {
	call := BSEUCCCall{Function: name, UCC: ucc, Request: summary}
	stale := ""
	for attempt := 0; ; attempt++ {
		s, err := c.session(stale)
		if err != nil {
			return fnAnswer{}, err // the sign-in's own record says why
		}
		jd["uid"] = c.x.user
		if src&fnSrcUID != 0 {
			jd["src_uid"] = c.x.user
		}
		if src&fnSrcBID != 0 {
			jd["src_bid"] = s.broker
		}
		if src&fnBrokerID != 0 && s.broker != "" {
			jd["broker_id"] = s.broker
		}
		body, err := fnBody(jd, s.key)
		if err != nil {
			return fnAnswer{}, err
		}
		code, b, err := c.do(name, body)
		call.Code = code
		if err != nil {
			call.Error = err.Error()
			c.record(call)
			return fnAnswer{}, &fnStop{msg: err.Error()}
		}
		a, perr := parseFN(b)
		msg := a.get("emsg")
		switch {
		case reFNIP.MatchString(msg):
			call.Error = msg
			c.record(call)
			return a, &fnStop{msg: "Finspot refused this server (" + msg + "): ask Finspot to allow this server's IP address for the API", auth: true}
		case reFNSession.MatchString(msg) || code == 401:
			fnSessions.Delete(c.tenant)
			if attempt == 0 {
				stale = s.key
				continue
			}
			call.Error = firstOf(msg, "HTTP 401")
			c.record(call)
			return a, &fnStop{msg: "Finspot ended the session again right after a new sign-in (" + call.Error + ")"}
		case code == 504 || reFNDown.MatchString(msg):
			call.Error = firstOf(msg, "HTTP 504")
			c.record(call)
			return a, &fnStop{msg: "Finspot is not taking requests now (" + call.Error + "): the next check tries again"}
		case perr != nil || code/100 != 2:
			call.Error = fmt.Sprintf("HTTP %d: %s", code, trimBody(b))
			c.record(call)
			return a, &fnStop{msg: fmt.Sprintf("Finspot's server answered %d", code)}
		}
		ok, m := a.ok()
		if !ok && reFNAuth.MatchString(m) {
			call.Error = m
			c.record(call)
			return a, &fnStop{msg: "Finspot refused the admin login (" + m + "): check it on Finspot and in Connections", auth: true}
		}
		if ok {
			call.Response = "Ok"
		} else {
			call.Response, call.Error = "Not_Ok", m
		}
		c.record(call)
		if !ok {
			return a, errors.New(m)
		}
		return a, nil
	}
}

// parallel runs fn(0..n-1) on the connection's workers; the first *fnStop
// stops them all and is returned. fn records its own results by index.
func (c *fnCall) parallel(n int, fn func(i int) error) error {
	workers := max(min(c.x.parallel, n), 1)
	var (
		mu   sync.Mutex
		next int
		stop error
		wg   sync.WaitGroup
	)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				mu.Lock()
				if stop != nil || next >= n || c.ctx.Err() != nil {
					mu.Unlock()
					return
				}
				i := next
				next++
				mu.Unlock()
				if err := fn(i); err != nil {
					var st *fnStop
					if errors.As(err, &st) {
						mu.Lock()
						if stop == nil {
							stop = err
						}
						mu.Unlock()
						return
					}
				}
			}
		}()
	}
	wg.Wait()
	if stop == nil && c.ctx.Err() != nil {
		return &fnStop{msg: "the push took too long; the rest go on the next check"}
	}
	return stop
}

// FinspotAPISession is the day's sign-in as the panel shows it.
type FinspotAPISession struct {
	SignedIn bool   `json:"signed_in"`
	At       string `json:"at,omitempty"`
	Broker   string `json:"broker,omitempty"`
}

func (o *Ops) fnSessionState(tenant string, x fnConn) FinspotAPISession {
	if v, ok := fnSessions.Load(tenant); ok {
		if s := v.(fnSession); s.fp == x.fp() && s.day == o.now().In(ist).Format("2006-01-02") {
			return FinspotAPISession{SignedIn: true, At: s.at.Format(time.RFC3339), Broker: s.broker}
		}
	}
	return FinspotAPISession{}
}

// finspotAPITest is the connection test: a sign-in.
func (o *Ops) finspotAPITest(ctx context.Context, tenant string, cn Connection) (string, string) {
	x, err := parseFinspotAPIConn(cn)
	if err != nil {
		return "Problem", strings.TrimPrefix(err.Error(), "invalid: ")
	}
	cctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	c := &fnCall{ctx: cctx, tenant: tenant, x: x, now: o.now}
	fnSessions.Delete(tenant)
	s, err := c.session("")
	if err != nil {
		return "Problem", err.Error()
	}
	env := map[bool]string{true: "live", false: "UAT"}[x.live]
	return "Working", fmt.Sprintf("Finspot (%s) accepted the admin login %s (broker %s)", env, x.user, firstOf(s.broker, "not given"))
}

// ---- states, runs and the queue ----

// FinspotAPIClient is a client's last limit push.
type FinspotAPIClient struct {
	UCC      string    `json:"ucc"`
	ActID    string    `json:"act_id"`
	Cash     money.Dec `json:"cash"`     // as Finspot has it after the push
	Notional money.Dec `json:"notional"` // as Finspot has it after the push
	Status   string    `json:"status"`
	At       string    `json:"at"`
	Run      string    `json:"run,omitempty"`
	Error    string    `json:"error,omitempty"`
	Why      string    `json:"why,omitempty"` // why at most 0
}

// FinspotAPIHolding is what a client's holdings were last pushed as:
// ISIN -> quantity, for the ISIN Holding and the T1 lines.
type FinspotAPIHolding struct {
	UCC    string           `json:"ucc"`
	Hold   map[string]int64 `json:"hold,omitempty"`
	HoldOn string           `json:"hold_on,omitempty"`
	T1     map[string]int64 `json:"t1,omitempty"`
	T1On   string           `json:"t1_on,omitempty"`
}

// FinspotAPIPart is how one part of a push went.
type FinspotAPIPart struct {
	Part      string `json:"part"`
	Name      string `json:"name"`
	Lines     int    `json:"lines"` // requests meant
	Pushed    int    `json:"pushed"`
	Unchanged int    `json:"unchanged,omitempty"`
	Cleared   int    `json:"cleared,omitempty"` // holdings sent at 0
	Failed    int    `json:"failed"`
	Skipped   int    `json:"skipped,omitempty"` // not sent (no token …)
	Note      string `json:"note,omitempty"`
	Stopped   string `json:"stopped,omitempty"`
}

// FinspotAPIRun is one push.
type FinspotAPIRun struct {
	ID           string           `json:"id"`
	Date         string           `json:"date"`
	Kind         string           `json:"kind"` // Start of day, Intraday, By hand
	By           string           `json:"by"`
	At           string           `json:"at"`
	Finished     string           `json:"finished,omitempty"`
	Parts        []FinspotAPIPart `json:"parts"`
	Failures     []string         `json:"failures,omitempty"` // client (and ISIN): Finspot's words
	MoreFailures int              `json:"more_failures,omitempty"`
	Stopped      string           `json:"stopped,omitempty"`
	Calls        []BSEUCCCall     `json:"calls,omitempty"`
	MoreCalls    int              `json:"more_calls,omitempty"`
}

func (r *FinspotAPIRun) fail(s string) {
	if len(r.Failures) < fnMaxFailures {
		r.Failures = append(r.Failures, s)
	} else {
		r.MoreFailures++
	}
}

type fnStateDoc struct {
	BOD       map[string]string `json:"bod,omitempty"`       // part -> date its start-of-day push went
	BODTried  string            `json:"bod_tried,omitempty"` // last attempt (RFC3339)
	LastSweep string            `json:"last_sweep,omitempty"`
}

// finspotAPILimitChanged notes that a client's Finspot limit may have
// changed: the next scheduler tick recomputes it and pushes it if it did.
// Nothing when automatic Finspot pushes are off.
func (o *Ops) finspotAPILimitChanged(ctx context.Context, tenant, ucc, why string) {
	if ucc == "" {
		return
	}
	cn, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connFinspotAPI)
	if err != nil || cn.Values["send"] != "On" {
		return
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cFNQueue, strings.ToUpper(ucc), bowQueued{UCC: strings.ToUpper(ucc), Pending: true, Why: why, At: o.now().Format(time.RFC3339)}, SystemActor)
}

// frontEndLimitChanged tells every front end pushed by API that a client's
// limit may have changed (BOW, bowapi.go; Finspot, here).
func (o *Ops) frontEndLimitChanged(ctx context.Context, tenant, ucc, why string) {
	o.bowLimitChanged(ctx, tenant, ucc, why)
	o.finspotAPILimitChanged(ctx, tenant, ucc, why)
}

func (o *Ops) fnQueuedUCCs(ctx context.Context, tenant string) []string {
	qs, _ := docs.List[bowQueued](ctx, o.Docs, tenant, cFNQueue, "")
	var out []string
	for _, q := range qs {
		if q.Pending {
			out = append(out, q.UCC)
		}
	}
	sort.Strings(out)
	return out
}

func (o *Ops) fnDequeue(ctx context.Context, tenant, ucc, since string) {
	_, _ = docs.Update(ctx, o.Docs, tenant, cFNQueue, ucc, SystemActor, func(q *bowQueued) error {
		if !q.Pending || q.At > since { // changed again during the push: keep it
			return docs.ErrStale
		}
		q.Pending = false
		return nil
	})
}

// ---- the push ----

// FinspotAPIPush pushes to Finspot now (Risk): the parts named (every part
// the connection sends by API when none is), for the clients named or all.
// A push by hand sends every line, even one Finspot was last given.
func (o *Ops) FinspotAPIPush(ctx context.Context, tenant, actor, date string, parts, uccs []string) (FinspotAPIRun, error) {
	if err := o.require(ctx, tenant, actor, "Pushing to Finspot", RoleRisk); err != nil {
		return FinspotAPIRun{}, err
	}
	date = firstOf(date, o.now().In(ist).Format("2006-01-02"))
	if !validDate(date) {
		return FinspotAPIRun{}, invalid("give a date (YYYY-MM-DD)")
	}
	_, x, err := o.finspotAPIConnection(ctx, tenant)
	if err != nil {
		return FinspotAPIRun{}, err
	}
	for _, p := range parts {
		if fnPartName[p] == "" {
			return FinspotAPIRun{}, invalid("no part %q: limits, holdings, t1, blocks, clients or baskets", p)
		}
		if !x.enabled(p) {
			return FinspotAPIRun{}, invalid("%s do not go by API on this connection (Settings → Connections → Finspot by API)", strings.ToLower(fnPartName[p]))
		}
	}
	return o.finspotAPIPush(ctx, tenant, actor, date, "By hand", parts, uccs, true)
}

// fnPush is one push's shared state.
type fnPush struct {
	o             *Ops
	ctx           context.Context
	tenant, actor string
	date          string
	force         bool
	want          map[string]bool // clients named; empty: all
	cfg           finspotCfg
	cs            map[string]Client
	c             *fnCall
	run           *FinspotAPIRun
	started       string
	fieldNotional string
}

func (p *fnPush) wanted(u string) bool { return len(p.want) == 0 || p.want[u] }

// finspotAPIPush pushes the parts named (every enabled part when none is).
// force sends every line; otherwise limits, blocks and clients go only
// when they differ from the last push (or it failed).
func (o *Ops) finspotAPIPush(ctx context.Context, tenant, actor, date, kind string, parts, uccs []string, force bool) (FinspotAPIRun, error) {
	cn, x, err := o.finspotAPIConnection(ctx, tenant)
	if err != nil {
		return FinspotAPIRun{}, err
	}
	if cn.Status == "Problem" && actor == SystemActor {
		return FinspotAPIRun{}, fmt.Errorf("%w: the Finspot API connection has a problem: %s", ErrState, cn.Problem)
	}
	if len(parts) == 0 {
		for _, p := range fnParts {
			if x.enabled(p) {
				parts = append(parts, p)
			}
		}
	}
	cfg, err := o.finspotConfig(ctx, tenant)
	if err != nil {
		return FinspotAPIRun{}, err
	}
	n, err := o.next(ctx, tenant, "finspotapirun-"+date, actor)
	if err != nil {
		return FinspotAPIRun{}, err
	}
	run := FinspotAPIRun{ID: fmt.Sprintf("%s-%04d", date, n), Date: date, Kind: kind, By: actor, At: o.now().Format(time.RFC3339)}
	cs, err := o.tradingClientMap(ctx, tenant)
	if err != nil {
		return run, err
	}
	p := &fnPush{o: o, ctx: ctx, tenant: tenant, actor: actor, date: date, force: force, want: map[string]bool{}, cfg: cfg, cs: cs, run: &run,
		started: run.At, fieldNotional: x.notionalField()}
	for _, u := range uccs {
		u = strings.ToUpper(strings.TrimSpace(u))
		if _, ok := cs[u]; !ok {
			return run, invalid("client %s not found", u)
		}
		p.want[u] = true
	}
	cctx, cancel := context.WithTimeout(ctx, 40*time.Minute)
	defer cancel()
	p.c = &fnCall{ctx: cctx, tenant: tenant, x: x, now: o.now}
	defer o.fnFinishRun(ctx, tenant, actor, &run, p.c)
	// Sign in once before the requests go a few at a time.
	if _, err := p.c.session(""); err != nil {
		var st *fnStop
		if errors.As(err, &st) {
			return run, o.fnStopped(ctx, tenant, &run, st)
		}
		return run, err
	}
	for _, part := range fnParts {
		if !contains(parts, part) {
			continue
		}
		var res FinspotAPIPart
		var perr error
		switch part {
		case FNPartBlocks:
			res, perr = p.blocks()
		case FNPartLimits:
			res, perr = p.limits()
		case FNPartHoldings:
			res, perr = p.holdings(false)
		case FNPartT1:
			res, perr = p.holdings(true)
		case FNPartBaskets:
			res, perr = p.baskets()
		case FNPartClients:
			res, perr = p.clients()
		}
		res.Part, res.Name = part, fnPartName[part]
		var st *fnStop
		if errors.As(perr, &st) {
			res.Stopped = st.msg
			run.Parts = append(run.Parts, res)
			return run, o.fnStopped(ctx, tenant, &run, st)
		}
		if perr != nil {
			res.Stopped = strings.TrimPrefix(perr.Error(), "invalid: ")
		}
		run.Parts = append(run.Parts, res)
		if res.Failed > 0 {
			o.raiseWork(ctx, tenant, wFinspotAPI, part, fmt.Sprintf("Finspot: %d %s not taken", res.Failed, strings.ToLower(res.Name)),
				fmt.Sprintf("The %s push of %s to Finspot had %d refusal(s): see the run %s on Live RMS → Finspot by API for Finspot's words, fix them on Finspot or in Ledgerline, and push again.", strings.ToLower(kind), strings.ToLower(res.Name), res.Failed, run.ID),
				"", RoleRisk, "live", date)
		} else if res.Stopped == "" {
			o.closeWork(ctx, tenant, actor, wFinspotAPI, part, "pushed")
		}
		if kind != "Intraday" && res.Stopped == "" && len(p.want) == 0 {
			o.fnNoteDailyFile(ctx, tenant, part, date)
		}
	}
	o.closeWork(ctx, tenant, actor, wFinspotAPI, "run", "pushed")
	return run, nil
}

// fnNoteDailyFile ticks the day's file checklist for a part sent in full:
// the API took the place of the file.
func (o *Ops) fnNoteDailyFile(ctx context.Context, tenant, part, date string) {
	switch part {
	case FNPartLimits:
		o.noteDailyFile(ctx, tenant, SystemActor, payoutBODLimitFile, "BOD limits by the Finspot API", date)
		o.noteDailyFile(ctx, tenant, SystemActor, "finspot-rms-limit", "by the Finspot API", date)
	case FNPartHoldings:
		o.noteDailyFile(ctx, tenant, SystemActor, "finspot-dp-holding", "by the Finspot API", date)
	case FNPartT1:
		o.noteDailyFile(ctx, tenant, SystemActor, "finspot-t1-holding", "by the Finspot API", date)
	case FNPartBlocks:
		o.noteDailyFile(ctx, tenant, SystemActor, "finspot-rms-block", "by the Finspot API", date)
	}
}

// fnStopped records a run that could not go on and asks Risk to act.
func (o *Ops) fnStopped(ctx context.Context, tenant string, run *FinspotAPIRun, st *fnStop) error {
	run.Stopped = st.msg
	if st.auth {
		o.fnConnMark(ctx, tenant, "Problem", st.msg)
	}
	o.raiseWork(ctx, tenant, wFinspotAPI, "run", "Finspot push stopped",
		"The "+strings.ToLower(run.Kind)+" push to Finspot stopped: "+st.msg+". What was not sent goes on the next check once this is fixed; Push to Finspot on Live RMS sends it now.", "", RoleRisk, "live", run.Date)
	return fmt.Errorf("%w: %s", ErrState, st.msg)
}

func (o *Ops) fnFinishRun(ctx context.Context, tenant, actor string, run *FinspotAPIRun, c *fnCall) {
	run.Finished = o.now().Format(time.RFC3339)
	lines := 0
	for _, p := range run.Parts {
		lines += p.Pushed + p.Failed + p.Cleared
	}
	if run.Kind == "Intraday" && lines == 0 && run.Stopped == "" {
		return // nothing changed: no record
	}
	c.mu.Lock()
	run.Calls, run.MoreCalls = c.calls, c.more
	c.mu.Unlock()
	_ = docs.Upsert(ctx, o.Docs, tenant, cFNRun, run.ID, *run, actor)
	sum := map[string]any{"kind": run.Kind, "stopped": run.Stopped}
	for _, p := range run.Parts {
		sum[p.Part] = fmt.Sprintf("%d pushed, %d failed", p.Pushed, p.Failed)
	}
	o.log(ctx, tenant, actor, "finspot_api.push", run.ID, sum)
}

// ---- limits ----

// FinspotAPILimit is what a client's Finspot limit is to be.
type FinspotAPILimit struct {
	ActID    string    `json:"act_id"`
	Cash     money.Dec `json:"cash"`
	Notional money.Dec `json:"notional"`
	Why      string    `json:"why,omitempty"` // why at most 0 and no notional cash
}

// finspotAPILimits are every client's Finspot limits for a trading date as
// things stand now: the RMS Limits values (finspotLimits: who gets a line,
// the account ID, the notional cash, the cap for clients who may not take
// new positions) with the money as of now (liveFunds) in place of the
// start-of-day balance.
func (o *Ops) finspotAPILimits(ctx context.Context, tenant, date string) (map[string]FinspotAPILimit, error) {
	_, lims, err := o.finspotLimits(ctx, tenant, date)
	if err != nil {
		return nil, err
	}
	eq, _, err := o.liveFunds(ctx, tenant, date)
	if err != nil {
		return nil, err
	}
	cs, _ := o.tradingClientMap(ctx, tenant)
	set, _ := o.Settings(ctx, tenant)
	out := make(map[string]FinspotAPILimit, len(lims))
	for u, l := range lims {
		x := FinspotAPILimit{ActID: l.ID, Cash: eq[u], Notional: l.NotionalCash}
		if why := set.frontEndBlockWhy(cs[u]); why != "" || len(cs[u].OpenSegments()) == 0 {
			x.Cash, x.Notional, x.Why = money.Min(x.Cash, money.Dec{}), money.Dec{}, firstOf(why, "No segment active")
		}
		out[u] = x
	}
	return out, nil
}

func (p *fnPush) limits() (FinspotAPIPart, error) {
	var res FinspotAPIPart
	lims, err := p.o.finspotAPILimits(p.ctx, p.tenant, p.date)
	if err != nil {
		return res, err
	}
	states := map[string]FinspotAPIClient{}
	all, _ := docs.List[FinspotAPIClient](p.ctx, p.o.Docs, p.tenant, cFNClient, "")
	for _, s := range all {
		states[s.UCC] = s
	}
	fileLines := p.o.fnFileLines(p.ctx, p.tenant, p.c.x, p.date)
	var todo []string
	for _, u := range sortedKeys(lims) {
		if !p.wanted(u) {
			continue
		}
		l, st := lims[u], states[u]
		if !p.force && fnLimitSame(st, fileLines, u, p.date, l) {
			res.Unchanged++
			p.o.fnDequeue(p.ctx, p.tenant, u, p.started)
			continue
		}
		todo = append(todo, u)
	}
	res.Lines = len(todo)
	out := make([]FinspotAPIClient, len(todo))
	stop := p.c.parallel(len(todo), func(i int) error {
		u := todo[i]
		l := lims[u]
		jd := map[string]any{"actid": l.ActID, "cash": l.Cash.Fixed(2)}
		sum := "set limits " + l.ActID + ": cash " + l.Cash.Fixed(2)
		if p.fieldNotional != "" {
			jd[p.fieldNotional] = l.Notional.Fixed(2)
			sum += ", " + p.fieldNotional + " " + l.Notional.Fixed(2)
		}
		_, err := p.c.req("SetLimits", u, sum, jd, fnSrcUID)
		st := FinspotAPIClient{UCC: u, ActID: l.ActID, At: p.o.now().Format(time.RFC3339), Run: p.run.ID, Why: l.Why, Status: FNPushed, Cash: l.Cash, Notional: l.Notional}
		var stp *fnStop
		switch {
		case errors.As(err, &stp):
			return err
		case err != nil:
			prev := states[u]
			st.Status, st.Error, st.Cash, st.Notional = FNFailed, err.Error(), prev.Cash, prev.Notional
		}
		out[i] = st
		return nil
	})
	for _, st := range out {
		if st.UCC == "" {
			continue // not reached
		}
		if st.Status == FNFailed {
			res.Failed++
			p.run.fail(st.UCC + " limit: " + st.Error)
		} else {
			res.Pushed++
		}
		_ = docs.Upsert(p.ctx, p.o.Docs, p.tenant, cFNClient, st.UCC, st, p.actor)
		p.o.fnDequeue(p.ctx, p.tenant, st.UCC, p.started)
	}
	if p.fieldNotional == "" {
		res.Note = "notional cash not sent (Connections)"
	}
	return res, stop
}

// fnFileLines are the day's RMS Limits lines when the start-of-day limits
// go by the file (nil otherwise): what Finspot has for a client the API
// has not pushed today.
func (o *Ops) fnFileLines(ctx context.Context, tenant string, x fnConn, date string) map[string]finspotLimit {
	if !x.bodLimitsFile {
		return nil
	}
	_, ls, err := o.finspotLimits(ctx, tenant, date)
	if err != nil {
		return nil
	}
	return ls
}

// fnLimitSame says whether Finspot already has a client's limit: the last
// push, when it went; with the start-of-day limits by the file (fileLines
// set) and no push yet that day, the file's line (its balance as cash,
// its notional cash), as the morning upload gave it.
func fnLimitSame(st FinspotAPIClient, fileLines map[string]finspotLimit, u, date string, l FinspotAPILimit) bool {
	if fileLines != nil && fnDay(st.At) != date {
		f, ok := fileLines[u]
		return ok && f.Margin.Cmp(l.Cash) == 0 && f.NotionalCash.Cmp(l.Notional) == 0
	}
	return st.Status == FNPushed && st.Cash.Cmp(l.Cash) == 0 && st.Notional.Cmp(l.Notional) == 0
}

// fnDay is an RFC 3339 time's IST date ("" when there is none).
func fnDay(at string) string {
	t, err := time.Parse(time.RFC3339, at)
	if err != nil {
		return ""
	}
	return t.In(ist).Format("2006-01-02")
}

// fnBODFileMade says whether the day's RMS Limits file has been made (or
// the limits pushed in full by hand), for the intraday pushes to wait for
// when the start-of-day limits go by the file.
func (o *Ops) fnBODFileMade(ctx context.Context, tenant, date string) bool {
	recv, _ := docs.List[DailyFileRecv](ctx, o.Docs, tenant, cDailyRecv, date+"|finspot-rms-limit|")
	return len(recv) > 0
}

// ---- holdings ----

// fnHoldLine is one holdings request.
type fnHoldLine struct {
	UCC, ISIN string
	Qty       int64
	jd        map[string]any
	call, sum string
}

// holdings sends the ISIN Holding lines (t1 false) or the T1 lines, and 0
// for each ISIN sent before that the client no longer holds.
func (p *fnPush) holdings(t1 bool) (FinspotAPIPart, error) {
	var res FinspotAPIPart
	rows, err := p.o.finspotHoldingRows(p.ctx, p.tenant, p.date, t1)
	if err != nil {
		return res, err
	}
	tokens := p.o.finspotTokens(p.ctx, p.tenant, p.date)
	prior := map[string]FinspotAPIHolding{}
	all, _ := docs.List[FinspotAPIHolding](p.ctx, p.o.Docs, p.tenant, cFNHold, "")
	for _, h := range all {
		prior[h.UCC] = h
	}
	var lines []fnHoldLine
	today := map[string]map[string]int64{}
	noToken := map[string]bool{}
	addLine := func(u, isin string, v map[string]string, qty int64, poa string) {
		actID := p.cfg.ID(u)
		if t1 {
			tk := tokens(isin)
			if len(tk) == 0 {
				noToken[isin] = true
				res.Skipped++
				return
			}
			jd := map[string]any{"actid": actID, "prod": "C", "exch_tks": tk, "hold_qty": strconv.FormatInt(qty, 10), "colqty": "0", "col_mode": "1",
				"hair_cut": firstOf(v["Haircut"], "1"), "close_prc": firstOf(v["ClosePrice"], "0"), "is_collateral": "false", "upload_prc": firstOf(v["ActualPrice"], firstOf(v["ClosePrice"], "0"))}
			lines = append(lines, fnHoldLine{UCC: u, ISIN: isin, Qty: qty, jd: jd, call: "UpdateBTSTAbsHoldings", sum: fmt.Sprintf("T1 holding %s %s: %d", actID, isin, qty)})
			return
		}
		coll := v["IsCollateral"] == "1"
		jd := map[string]any{"actid": actID, "prod": "C", "isin": isin, "hold_qty": strconv.FormatInt(qty, 10), "colqty": firstOf(v["CollateralQty"], "0"),
			"col_mode": firstOf(v["CollateralMode"], "1"), "hair_cut": firstOf(v["Haircut"], "1"), "close_prc": firstOf(v["ClosePrice"], "0"),
			"benqty": "0", "upload_prc": firstOf(v["ActualPrice"], firstOf(v["ClosePrice"], "0"))}
		call := "UpdateAbsHoldingsISIN"
		if p.c.x.npoaCall && poa == "N" {
			call = "NpoaISINHoldings"
			delete(jd, "prod")
			jd["is_collateral"] = strconv.FormatBool(coll)
			if d := v["DPAccount"]; d != "" {
				jd["dp_acct_num"] = d
			}
		} else {
			jd["brkcollqty"], jd["dpqty"] = firstOf(v["BrokerCollateralQty"], "0"), firstOf(v["DPHoldingsQty"], "0")
		}
		lines = append(lines, fnHoldLine{UCC: u, ISIN: isin, Qty: qty, jd: jd, call: call, sum: fmt.Sprintf("holding %s %s: %d, collateral %s", actID, isin, qty, jd["colqty"])})
	}
	for _, r := range rows {
		v := r.Values
		u := strings.TrimSuffix(v["AccountId"], p.cfg.Suffix)
		if !p.wanted(u) {
			continue
		}
		q, _ := strconv.ParseInt(v["HoldingsQty"], 10, 64)
		if today[u] == nil {
			today[u] = map[string]int64{}
		}
		today[u][v["ISIN"]] = q
		addLine(u, v["ISIN"], v, q, v["POAFlag"])
	}
	// What was sent before and is no longer held goes at 0.
	for _, u := range sortedKeys(prior) {
		if !p.wanted(u) {
			continue
		}
		was := prior[u].Hold
		if t1 {
			was = prior[u].T1
		}
		for _, isin := range sortedKeys(was) {
			if _, still := today[u][isin]; still || was[isin] == 0 {
				continue
			}
			if today[u] == nil {
				today[u] = map[string]int64{}
			}
			today[u][isin] = 0
			addLine(u, isin, map[string]string{"CollateralQty": "0", "IsCollateral": "0"}, 0, "")
		}
	}
	res.Lines = len(lines)
	errs := make([]string, len(lines))
	done := make([]bool, len(lines))
	stop := p.c.parallel(len(lines), func(i int) error {
		l := lines[i]
		_, err := p.c.req(l.call, l.UCC, l.sum, l.jd, fnSrcUID|fnSrcBID)
		var stp *fnStop
		if errors.As(err, &stp) {
			return err
		}
		done[i] = true
		if err != nil {
			errs[i] = err.Error()
		}
		return nil
	})
	sent := map[string]map[string]int64{}
	for i, l := range lines {
		if !done[i] {
			continue
		}
		if errs[i] != "" {
			res.Failed++
			p.run.fail(l.UCC + " " + l.ISIN + ": " + errs[i])
			continue
		}
		if l.Qty == 0 {
			res.Cleared++
		} else {
			res.Pushed++
		}
		if sent[l.UCC] == nil {
			sent[l.UCC] = map[string]int64{}
		}
		sent[l.UCC][l.ISIN] = l.Qty
	}
	// Keep what Finspot now has: the lines it took; a refused or unsent
	// line keeps its earlier quantity, so it is tried again.
	for _, u := range sortedKeys(today) {
		h := prior[u]
		h.UCC = u
		was := h.Hold
		if t1 {
			was = h.T1
		}
		now := map[string]int64{}
		for isin, q := range was {
			now[isin] = q
		}
		for isin, q := range sent[u] {
			if q == 0 {
				delete(now, isin)
			} else {
				now[isin] = q
			}
		}
		if t1 {
			h.T1, h.T1On = now, p.date
		} else {
			h.Hold, h.HoldOn = now, p.date
		}
		_ = docs.Upsert(p.ctx, p.o.Docs, p.tenant, cFNHold, u, h, p.actor)
	}
	if len(noToken) > 0 {
		list := sortedKeys(noToken)
		res.Note = fmt.Sprintf("%d line(s) not sent: no NSE or BSE token for %d ISIN(s) (load the day's common bhavcopy)", res.Skipped, len(list))
		if len(list) > 10 {
			list = append(list[:10], fmt.Sprintf("and %d more", len(noToken)-10))
		}
		p.o.raiseWork(p.ctx, p.tenant, wFinspotAPI, "token", "Finspot T1 holdings without a token",
			"Finspot takes T1 holdings by the exchange token, and these ISINs have none in the day's market data: "+strings.Join(list, ", ")+". Load the NSE or BSE common bhavcopy (it carries the token), then push the T1 holdings again; or upload the T1 file by hand.", "", RoleRisk, "live", p.date)
	} else if t1 {
		p.o.closeWork(p.ctx, p.tenant, p.actor, wFinspotAPI, "token", "every line had a token")
	}
	return res, stop
}

// finspotTokens gives an ISIN's exchange tokens as Finspot's exch_tks: NSE's
// and BSE's from the day's market data (the common bhavcopy), BSE's scrip
// code from the security reference when the bhavcopy had none.
func (o *Ops) finspotTokens(ctx context.Context, tenant, date string) func(isin string) []map[string]string {
	set, _ := o.Settings(ctx, tenant)
	_, look, _ := o.Market(ctx, tenant, set.PrevBusinessDay(date))
	var mu sync.Mutex
	memo := map[string][]map[string]string{}
	return func(isin string) []map[string]string {
		mu.Lock()
		defer mu.Unlock()
		if v, ok := memo[isin]; ok {
			return v
		}
		t := look.Tokens(isin)
		var out []map[string]string
		if n := t["NSE"]; n != "" {
			out = append(out, map[string]string{"exch": "NSE", "token": n})
		}
		b := t["BSE"]
		if b == "" {
			ref, _, _ := docs.Get[SecRef](ctx, o.Docs, tenant, cSecRef, isin)
			b = ref.BSECode
		}
		if b != "" {
			out = append(out, map[string]string{"exch": "BSE", "token": b})
		}
		memo[isin] = out
		return out
	}
}

// ---- blocks ----

func (p *fnPush) blocks() (FinspotAPIPart, error) {
	var res FinspotAPIPart
	finspotMu.Lock()
	defer finspotMu.Unlock()
	items, changed, err := p.o.finspotBlockPlan(p.ctx, p.tenant, p.date)
	if err != nil {
		return res, err
	}
	var todo []finspotBlockItem
	for _, it := range items {
		if !p.wanted(it.UCC) {
			continue
		}
		if !p.force && !it.Changed {
			res.Unchanged++
			continue
		}
		todo = append(todo, it)
	}
	res.Lines = len(todo)
	errs := make([]string, len(todo))
	done := make([]bool, len(todo))
	freeze := p.c.x.blocks == fnBlocksFreeze
	stop := p.c.parallel(len(todo), func(i int) error {
		it := todo[i]
		act := p.cfg.ID(it.UCC)
		name, jd, sum := "UnBlockAcct", map[string]any{"actid": act}, "unblock "+act
		switch {
		case it.Block && freeze:
			name, sum = "FreezeAccount", "block fresh orders "+act+": "+it.Why
			jd["type"] = "2"
		case it.Block:
			name, sum = "BlockAcct", "block "+act+": "+it.Why
		case freeze:
			name, sum = "DeFreezeAccount", "allow fresh orders "+act
		}
		_, err := p.c.req(name, it.UCC, sum, jd, 0)
		var stp *fnStop
		if errors.As(err, &stp) {
			return err
		}
		done[i] = true
		if err != nil {
			errs[i] = err.Error()
		}
		return nil
	})
	ok := map[string]bool{}
	for i, it := range todo {
		switch {
		case !done[i]:
		case errs[i] != "":
			res.Failed++
			p.run.fail(it.UCC + " block: " + errs[i])
		default:
			res.Pushed++
			ok[it.UCC] = true
		}
	}
	var keep []FinspotBlockState
	for _, x := range changed {
		if ok[x.UCC] {
			keep = append(keep, x)
		}
	}
	if err := p.o.saveFinspotBlocks(p.ctx, p.tenant, p.actor, keep); err != nil {
		return res, err
	}
	return res, stop
}

// ---- client logins ----

// fnProducts are Finspot's product names for the file's codes.
var fnProducts = map[string]string{"I": "MIS", "M": "NRML", "C": "CNC", "H": "CO", "B": "BO"}

func fnProductList(codes string) []string {
	var out []string
	for _, c := range strings.Split(codes, ",") {
		if p := fnProducts[strings.TrimSpace(c)]; p != "" {
			out = append(out, p)
		}
	}
	return out
}

// fnDate turns YYYY-MM-DD into Finspot's dd-mm-yyyy ("" stays "").
func fnDate(d string) string {
	if t, err := time.Parse("2006-01-02", d); err == nil {
		return t.Format("02-01-2006")
	}
	return ""
}

// fnAcctType is Finspot's account type for a client's type.
func fnAcctType(c Client) string {
	switch c.Type {
	case "NRI":
		return "NRI"
	case "Corporate":
		return "Institutional"
	}
	return "Retail"
}

// fnClientPlan is a client's login requests: the account (AddInvestorClient
// for a new client, ModifyClient for a changed one) and its segments and
// products. No password is ever sent.
type fnClientPlan struct {
	UCC      string
	New      bool
	add, mod map[string]any
	segs     []map[string]any
	prods    map[string]any
	hash     string
	problem  string
}

func (p *fnPush) clientPlan(c Client, firm map[string]bool) fnClientPlan {
	v := p.o.finspotClientRow(p.ctx, p.tenant, c, p.cfg).Values
	act := v["AccountId"]
	pl := fnClientPlan{UCC: c.UCC}
	switch {
	case len(act) > 10:
		pl.problem = "the account ID " + act + " is longer than Finspot's 10 characters"
	case v["Email"] == "" || v["Mobile"] == "":
		pl.problem = "Finspot needs the e-mail and the mobile number"
	case v["PAN"] == "":
		pl.problem = "Finspot needs the PAN"
	}
	common := map[string]any{"actid": act, "acct_name": v["AccountName"], "region": v["BranchId"], "email": v["Email"], "addr": v["ResidenceAddress"],
		"mob_num": v["Mobile"], "family_id": v["ClientGroup"], "pan": v["PAN"], "acct_type": fnAcctType(c), "acct_status": "Activated"}
	if d := v["DPAccount"]; d != "" {
		common["dp_array"] = []string{d}
	}
	if c.Bank.Account != "" {
		common["bank_det"] = []map[string]string{{"acctnum": finspotText(c.Bank.Account), "ifsc_code": finspotText(c.Bank.IFSC), "bank_name": v["BankName"], "bank_addr": v["BankAddress"]}}
	}
	pl.mod = map[string]any{}
	for k, x := range common {
		pl.mod[k] = x
	}
	pl.add = map[string]any{}
	for k, x := range common {
		pl.add[k] = x
	}
	prods := fnProductList(finspotProducts(c))
	pl.add["user_id"], pl.add["uname"], pl.add["disp_prec"] = act, v["AccountName"], firstOf(v["DecimalPreference"], "2")
	pl.add["user_access_grp"], pl.add["user_access_type"], pl.add["user_status"] = p.cfg.Role, p.c.x.access, "Active"
	pl.add["dob"] = fnDate(v["DOB"])
	if pl.add["dob"] == "" && pl.problem == "" {
		pl.problem = "Finspot needs the date of birth (or of incorporation)"
	}
	if len(prods) > 0 {
		pl.add["prarr"] = prods
		pl.prods = map[string]any{"actid": act, "user_id": act, "prarr": prods}
	}
	if ot := strings.Split(p.cfg.OrderTypes, ","); len(ot) > 0 && ot[0] != "" {
		pl.add["ord_type_det"] = ot
	}
	for _, fs := range finspotSegments {
		if !firm[fs.Key] || !contains(c.Segments, fs.Seg) {
			continue
		}
		x, ok := p.cfg.Users[fs.Code]
		if !ok || x.ID == "" || x.Info == "" {
			if pl.problem == "" {
				pl.problem = "set the exchange user ID and user info for " + fs.Code + " in Connections → Finspot uploads"
			}
			continue
		}
		en := "N"
		if c.SegmentOpen(fs.Seg) {
			en = "Y"
		}
		seg := map[string]any{"actid": act, "user_id": act, "seg": fs.Code, "exch_uid": x.ID, "exch_usr_info": x.Info, "enable_flag": en}
		if t, err := time.Parse("02/01/2006", x.Expiry); err == nil {
			seg["expirydate"] = t.Format("Jan 02 2006")
		}
		pl.segs = append(pl.segs, seg)
	}
	h := sha256.New()
	for _, part := range []any{pl.add, pl.segs, pl.prods} {
		b, _ := json.Marshal(part)
		h.Write(b)
	}
	pl.hash = hex.EncodeToString(h.Sum(nil))[:16]
	return pl
}

func (p *fnPush) clients() (FinspotAPIPart, error) {
	var res FinspotAPIPart
	set, _ := p.o.Settings(p.ctx, p.tenant)
	firm := map[string]bool{}
	for _, c := range set.activeSegments() {
		firm[c.Key] = true
	}
	prior, _ := docs.List[finspotSentState](p.ctx, p.o.Docs, p.tenant, cFNSent, "client|")
	last := map[string]finspotSentState{}
	for _, x := range prior {
		last[x.UCC] = x
	}
	// A client Finspot could not take (data missing, or refused) is tried
	// again when its details change, or on a push by hand.
	probs, _ := docs.List[finspotSentState](p.ctx, p.o.Docs, p.tenant, cFNSent, "problem|")
	lastProblem := map[string]finspotSentState{}
	for _, x := range probs {
		lastProblem[x.UCC] = x
	}
	byFile, _ := docs.List[finspotSentState](p.ctx, p.o.Docs, p.tenant, cFinspotSent, "clients|")
	onFinspot := map[string]bool{}
	for _, x := range byFile {
		onFinspot[x.UCC] = true
	}
	// Who creates logins (Settings → Firm practices, finspotlogin.go): a
	// client with a login made in the console is changed, never created
	// again; a new client waits for the console, or for someone to click
	// Create Finspot login when the firm does both.
	mode := p.o.finspotLoginMode(p.ctx, p.tenant)
	logins := p.o.finspotLoginRecords(p.ctx, p.tenant)
	waiting := 0
	var plans []fnClientPlan
	for _, u := range sortedKeys(p.cs) {
		c := p.cs[u]
		if c.Status != "Active" || !p.wanted(u) {
			continue
		}
		pl := p.clientPlan(c, firm)
		was, known := last[u]
		if !p.force && known && was.Hash == pl.hash {
			res.Unchanged++
			continue
		}
		if !p.force && lastProblem[u].Hash == pl.hash {
			res.Skipped++
			continue
		}
		pl.New = !known && !onFinspot[u] && !logins[u].Has
		if pl.New && (mode == FinspotLoginsConsole || mode == FinspotLoginsBoth && !logins[u].Requested) {
			waiting++
			continue
		}
		plans = append(plans, pl)
	}
	if waiting > 0 {
		res.Skipped += waiting
		res.Note = fmt.Sprintf("%d new client(s) have no Finspot login yet: they are made in Finspot's admin console", waiting)
		if mode == FinspotLoginsBoth {
			res.Note += ", or by Create Finspot login on the client"
		}
	}
	res.Lines = len(plans)
	errs := make([]string, len(plans))
	done := make([]bool, len(plans))
	created := make([]bool, len(plans))
	found := make([]bool, len(plans))
	stop := p.c.parallel(len(plans), func(i int) error {
		pl := plans[i]
		if pl.problem != "" {
			done[i], errs[i] = true, pl.problem
			return nil
		}
		act := pl.add["actid"].(string)
		var err error
		if pl.New {
			_, err = p.c.req("AddInvestorClient", pl.UCC, "add client login "+act+" (no password)", pl.add, fnSrcUID|fnBrokerID)
			switch {
			case err != nil && strings.Contains(strings.ToLower(err.Error()), "exist"):
				// Made in Finspot's admin console: changed, never added twice.
				err = nil
				pl.New = false
				found[i] = true
			case err == nil:
				created[i] = true
			}
		}
		if err == nil && !pl.New {
			_, err = p.c.req("ModifyClient", pl.UCC, "modify client "+act, pl.mod, fnSrcUID|fnBrokerID)
		}
		for _, s := range pl.segs {
			if err != nil {
				break
			}
			_, err = p.c.req("InvClientSegEnabled", pl.UCC, fmt.Sprintf("segment %s %s: %s", act, s["seg"], s["enable_flag"]), s, fnSrcUID)
		}
		if err == nil && pl.prods != nil {
			_, err = p.c.req("InvClientProductEnabled", pl.UCC, "products "+act+": "+strings.Join(pl.prods["prarr"].([]string), ","), pl.prods, 0)
		}
		var stp *fnStop
		if errors.As(err, &stp) {
			return err
		}
		done[i] = true
		if err != nil {
			errs[i] = err.Error()
		}
		return nil
	})
	for i, pl := range plans {
		switch {
		case !done[i]:
		case errs[i] != "":
			res.Failed++
			p.run.fail(pl.UCC + " login: " + errs[i])
			_ = docs.Upsert(p.ctx, p.o.Docs, p.tenant, cFNSent, "problem|"+pl.UCC, finspotSentState{Kind: "problem", UCC: pl.UCC, Hash: pl.hash, On: p.date}, p.actor)
		default:
			res.Pushed++
			_ = docs.Upsert(p.ctx, p.o.Docs, p.tenant, cFNSent, "client|"+pl.UCC, finspotSentState{Kind: "client", UCC: pl.UCC, Hash: pl.hash, On: p.date}, p.actor)
			_ = docs.Upsert(p.ctx, p.o.Docs, p.tenant, cFNSent, "problem|"+pl.UCC, finspotSentState{Kind: "problem", UCC: pl.UCC, On: p.date}, p.actor)
		}
		switch {
		case !done[i] || errs[i] != "":
		case created[i]:
			p.o.noteFinspotLogin(p.ctx, p.tenant, p.actor, pl.UCC, FinspotLoginByAPI, "")
		case found[i]:
			p.o.noteFinspotLogin(p.ctx, p.tenant, p.actor, pl.UCC, FinspotLoginFound, "AddInvestorClient: the client exists on Finspot; its details were changed instead")
		}
	}
	return res, stop
}

// ---- margin baskets ----

func (p *fnPush) baskets() (FinspotAPIPart, error) {
	var res FinspotAPIPart
	for _, exch := range []string{"NSE", "BSE"} {
		cfg, basket, lines, err := p.o.finspotBasketLines(p.ctx, p.tenant, exch, p.date)
		if err != nil {
			return res, err
		}
		var toks, pcts []string
		for _, l := range lines {
			if l.Token == "" {
				res.Skipped++
				continue
			}
			toks, pcts = append(toks, l.Token), append(pcts, trimDec(l.Pct.Fixed(2)))
		}
		if len(toks) == 0 {
			continue
		}
		res.Lines++
		jd := map[string]any{"exch": exch, "prod": cfg.BasketProduct, "basket_name": basket, "token_array": toks, "margin_perc_array": pcts}
		_, err = p.c.req("SetMarginBasket", "", fmt.Sprintf("margin basket %s %s: %d scrips", exch, basket, len(toks)), jd, fnSrcUID|fnSrcBID)
		var stp *fnStop
		switch {
		case errors.As(err, &stp):
			return res, err
		case err != nil:
			res.Failed++
			p.run.fail(exch + " margin basket: " + err.Error())
		default:
			res.Pushed++
		}
	}
	if res.Skipped > 0 {
		res.Note = fmt.Sprintf("%d scrip(s) left out: no token in the day's market data", res.Skipped)
	}
	return res, nil
}

// ---- the scheduler ----

// fnBODParts are the parts the start-of-day push sends: everything enabled
// but client logins, which go with the changes, and the limits when the
// start-of-day limits go by the file.
func (x fnConn) bodParts() []string {
	var out []string
	for _, p := range fnParts {
		if p != FNPartClients && x.enabled(p) && !(p == FNPartLimits && x.bodLimitsFile) {
			out = append(out, p)
		}
	}
	return out
}

// finspotAPITick pushes when Finspot pushes are on: the start-of-day parts
// once a business day at the firm's time (tried again every few minutes
// until each goes), then from 09:00 to 20:00 the clients whose limit may
// have changed (finspotAPILimitChanged) at once, and every few minutes any
// limit, block or client login that differs from the last push.
func (o *Ops) finspotAPITick(ctx context.Context, tenant string, now time.Time) {
	cn, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connFinspotAPI)
	if err != nil || cn.Status == "Not set" || cn.Status == "" || cn.Status == "Problem" || cn.Values["send"] != "On" {
		return
	}
	x, err := parseFinspotAPIConn(cn)
	if err != nil {
		return
	}
	now = now.In(ist)
	date, hm := now.Format("2006-01-02"), now.Format("15:04")
	set, _ := o.Settings(ctx, tenant)
	if !set.IsBusinessDay(date) || hm < x.bodAt || hm >= fnDayEnd {
		return
	}
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cFNState, "state", fnStateDoc{})
	if st.BOD == nil {
		st.BOD = map[string]string{}
	}
	var pending []string
	for _, p := range x.bodParts() {
		if st.BOD[p] != date {
			pending = append(pending, p)
		}
	}
	if len(pending) > 0 {
		if t, err := time.Parse(time.RFC3339, st.BODTried); err == nil && t.In(ist).Format("2006-01-02") == date && now.Sub(t) < x.every {
			return
		}
		st.BODTried = now.Format(time.RFC3339)
		_ = docs.Upsert(ctx, o.Docs, tenant, cFNState, "state", st, SystemActor)
		run, _ := o.finspotAPIPush(ctx, tenant, SystemActor, date, "Start of day", pending, nil, true)
		for _, p := range run.Parts {
			if p.Stopped == "" {
				st.BOD[p.Part] = date
			}
		}
		st.LastSweep = now.Format(time.RFC3339)
		_ = docs.Upsert(ctx, o.Docs, tenant, cFNState, "state", st, SystemActor)
		return
	}
	if hm < fnDayStart {
		return
	}
	sweep := true
	if t, err := time.Parse(time.RFC3339, st.LastSweep); err == nil && now.Sub(t) < x.every {
		sweep = false
	}
	q := o.fnQueuedUCCs(ctx, tenant)
	if !sweep && len(q) == 0 {
		return
	}
	// With the start-of-day limits by the file, limit changes wait for the
	// day's RMS Limits file (the queue keeps them).
	var parts []string
	if !x.bodLimitsFile || o.fnBODFileMade(ctx, tenant, date) {
		parts = append(parts, FNPartLimits)
	}
	if sweep {
		st.LastSweep = now.Format(time.RFC3339)
		_ = docs.Upsert(ctx, o.Docs, tenant, cFNState, "state", st, SystemActor)
		q = nil // every client, the queued ones among them
		for _, p := range []string{FNPartBlocks, FNPartClients} {
			if x.enabled(p) {
				parts = append(parts, p)
			}
		}
	}
	if len(parts) == 0 {
		return
	}
	_, _ = o.finspotAPIPush(ctx, tenant, SystemActor, date, "Intraday", parts, q, false)
}

// ---- the screen ----

// FinspotAPIRow is a client on the Finspot API panel.
type FinspotAPIRow struct {
	UCC      string           `json:"ucc"`
	Name     string           `json:"name"`
	Limit    FinspotAPILimit  `json:"limit"` // as computed now
	Last     FinspotAPIClient `json:"last"`
	Holdings int              `json:"holdings"` // ISINs Finspot was last given
	HoldOn   string           `json:"hold_on,omitempty"`
	T1       int              `json:"t1"`
	Queued   bool             `json:"queued,omitempty"`
	Differs  bool             `json:"differs,omitempty"` // the limit differs from the last push
	// Login is how Ledgerline knows the client has a Finspot login ("":
	// not known; finspotlogin.go).
	Login string `json:"login,omitempty"`
}

// FinspotAPIView is the Finspot API panel on Live RMS.
type FinspotAPIView struct {
	Date     string            `json:"date"`
	Status   string            `json:"status"` // the connection's
	Problem  string            `json:"problem,omitempty"`
	Env      string            `json:"env"`
	Auto     bool              `json:"auto"`
	BODAt    string            `json:"bod_at"`
	Every    int               `json:"every"`
	Parts    []string          `json:"parts"`     // sent by API
	BODParts []string          `json:"bod_parts"` // sent by the start-of-day push
	ByFile   []string          `json:"by_file"`   // morning parts sent by their files
	BODDone  map[string]bool   `json:"bod_done"`
	Session  FinspotAPISession `json:"session"`
	Notional string            `json:"notional"`
	Blocks   string            `json:"blocks"`
	Rows     []FinspotAPIRow   `json:"rows"`
	Runs     []FinspotAPIRun   `json:"runs"` // latest first, without calls
	Notes    []string          `json:"notes,omitempty"`
	// LoginMode is who creates clients' Finspot logins (Settings → Firm
	// practices): console, ledgerline or both.
	LoginMode string `json:"login_mode"`
}

// FinspotAPIPanel is the panel: each client's limit now and last push, the
// start-of-day parts done, and the latest runs.
func (o *Ops) FinspotAPIPanel(ctx context.Context, tenant, actor, date string) (FinspotAPIView, error) {
	if err := o.require(ctx, tenant, actor, "Viewing Finspot pushes", RoleRisk, RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return FinspotAPIView{}, err
	}
	cn, x, err := o.finspotAPIConnection(ctx, tenant)
	if err != nil {
		return FinspotAPIView{}, err
	}
	date = firstOf(date, o.now().In(ist).Format("2006-01-02"))
	v := FinspotAPIView{Date: date, Status: cn.Status, Problem: cn.Problem, Env: map[bool]string{true: "Live", false: "UAT"}[x.live], Auto: x.auto,
		BODAt: x.bodAt, Every: int(x.every / time.Minute), BODDone: map[string]bool{}, Session: o.fnSessionState(tenant, x), Notional: x.notional, Blocks: x.blocks}
	for _, p := range fnParts {
		if x.enabled(p) {
			v.Parts = append(v.Parts, p)
		}
	}
	v.BODParts, v.ByFile = x.bodParts(), x.byFile()
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cFNState, "state", fnStateDoc{})
	for p, d := range st.BOD {
		v.BODDone[p] = d == date
	}
	if len(v.ByFile) > 0 {
		var names []string
		for _, p := range v.ByFile {
			names = append(names, strings.ToLower(fnPartName[p]))
		}
		v.Notes = append(v.Notes, "By their files each morning (Front-office files, on the daily file checklist): "+strings.Join(names, ", ")+".")
	}
	if x.bodLimitsFile {
		n := "Start-of-day limits go by the RMS Limits file: during the day only a client whose limit differs from the file's line (or the day's last push) is sent by API"
		if !o.fnBODFileMade(ctx, tenant, date) {
			n += ", and nothing is sent until the day's RMS Limits file is made"
		}
		v.Notes = append(v.Notes, n+".")
	}
	v.Notes = append(v.Notes, "NFO cost price, dealer-client positions and dealer order types have no Finspot API: they stay as files (Front-office files).")
	if x.notional == fnNotionalNone {
		v.Notes = append(v.Notes, "Notional cash (cash-equivalent collateral) is not sent: clients get no limit for it until a field is chosen in Connections.")
	}
	lims, err := o.finspotAPILimits(ctx, tenant, date)
	if err != nil {
		return v, err
	}
	cs, _ := o.tradingClientMap(ctx, tenant)
	queued := map[string]bool{}
	for _, u := range o.fnQueuedUCCs(ctx, tenant) {
		queued[u] = true
	}
	states := map[string]FinspotAPIClient{}
	all, _ := docs.List[FinspotAPIClient](ctx, o.Docs, tenant, cFNClient, "")
	for _, s := range all {
		states[s.UCC] = s
	}
	fileLines := o.fnFileLines(ctx, tenant, x, date)
	holds := map[string]FinspotAPIHolding{}
	hs, _ := docs.List[FinspotAPIHolding](ctx, o.Docs, tenant, cFNHold, "")
	for _, h := range hs {
		holds[h.UCC] = h
	}
	v.LoginMode = o.finspotLoginMode(ctx, tenant)
	logins := o.finspotLoginsKnown(ctx, tenant)
	for _, u := range sortedKeys(lims) {
		l, s := lims[u], states[u]
		h := holds[u]
		r := FinspotAPIRow{UCC: u, Name: cs[u].Name, Limit: l, Last: s, Holdings: len(h.Hold), HoldOn: h.HoldOn, T1: len(h.T1), Queued: queued[u], Login: logins[u].How}
		r.Differs = !fnLimitSame(s, fileLines, u, date, l)
		v.Rows = append(v.Rows, r)
	}
	runs, _ := docs.List[FinspotAPIRun](ctx, o.Docs, tenant, cFNRun, "")
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

// FinspotAPIRunGet is one push with its calls.
func (o *Ops) FinspotAPIRunGet(ctx context.Context, tenant, actor, id string) (FinspotAPIRun, error) {
	if err := o.require(ctx, tenant, actor, "Viewing Finspot pushes", RoleRisk, RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return FinspotAPIRun{}, err
	}
	r, _, err := docs.Get[FinspotAPIRun](ctx, o.Docs, tenant, cFNRun, id)
	return r, err
}

// ---- reading back ----

// FinspotAPIHoldCheck is one ISIN as Finspot has it and as last pushed.
type FinspotAPIHoldCheck struct {
	ISIN    string `json:"isin"`
	Finspot int64  `json:"finspot"`
	Sent    int64  `json:"sent"`
	Match   bool   `json:"match"`
}

// FinspotAPICheck is a client's limit and holdings read back from Finspot.
type FinspotAPICheck struct {
	UCC          string                `json:"ucc"`
	ActID        string                `json:"act_id"`
	At           string                `json:"at"`
	Cash         string                `json:"cash"`      // Finspot's
	Notional     string                `json:"notional"`  // Finspot's, in the connection's field
	SentCash     string                `json:"sent_cash"` // last pushed
	SentNotional string                `json:"sent_notional"`
	Payin        string                `json:"payin,omitempty"`
	Payout       string                `json:"payout,omitempty"`
	MarginUsed   string                `json:"margin_used,omitempty"`
	Collateral   string                `json:"collateral,omitempty"`
	LimitMatch   bool                  `json:"limit_match"`
	Holdings     []FinspotAPIHoldCheck `json:"holdings"`
	HoldMatch    bool                  `json:"hold_match"`
}

// FinspotAPICheckClient reads a client's limit (Limits) and holdings
// (Holdings, CNC) back from Finspot and compares them with the last push
// (Risk). Nothing read is stored.
func (o *Ops) FinspotAPICheckClient(ctx context.Context, tenant, actor, ucc string) (FinspotAPICheck, error) {
	if err := o.require(ctx, tenant, actor, "Checking Finspot", RoleRisk); err != nil {
		return FinspotAPICheck{}, err
	}
	_, x, err := o.finspotAPIConnection(ctx, tenant)
	if err != nil {
		return FinspotAPICheck{}, err
	}
	cfg, err := o.finspotConfig(ctx, tenant)
	if err != nil {
		return FinspotAPICheck{}, err
	}
	ucc = strings.ToUpper(strings.TrimSpace(ucc))
	if _, _, err := docs.Get[Client](ctx, o.Docs, tenant, cClient, ucc); err != nil {
		return FinspotAPICheck{}, invalid("client %s not found", ucc)
	}
	act := cfg.ID(ucc)
	cctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	c := &fnCall{ctx: cctx, tenant: tenant, x: x, now: o.now}
	out := FinspotAPICheck{UCC: ucc, ActID: act, At: o.now().Format(time.RFC3339)}
	la, err := c.req("Limits", ucc, "read limits "+act, map[string]any{"actid": act}, fnSrcUID)
	if err != nil {
		return out, fmt.Errorf("%w: Finspot did not give the limits: %s", ErrState, err.Error())
	}
	if la.obj == nil && len(la.arr) > 0 {
		la.obj = la.arr[0]
	}
	out.Cash, out.Payin, out.Payout, out.MarginUsed, out.Collateral = la.get("cash"), la.get("payin"), la.get("payout"), la.get("marginused"), la.get("collateral")
	if f := x.notionalField(); f != "" {
		out.Notional = la.get(f)
	}
	st, _, _ := docs.Get[FinspotAPIClient](ctx, o.Docs, tenant, cFNClient, ucc)
	out.SentCash, out.SentNotional = st.Cash.Fixed(2), st.Notional.Fixed(2)
	same := func(a, b string) bool {
		x, e1 := money.ParseDec(firstOf(a, "0"))
		y, e2 := money.ParseDec(firstOf(b, "0"))
		return e1 == nil && e2 == nil && x.Cmp(y) == 0
	}
	out.LimitMatch = same(out.Cash, out.SentCash) && (x.notionalField() == "" || same(out.Notional, out.SentNotional))
	ha, err := c.req("Holdings", ucc, "read holdings "+act, map[string]any{"actid": act, "prd": "C"}, 0)
	have := map[string]int64{}
	if err == nil {
		for _, h := range ha.arr {
			isin := fnStr(h["isin"])
			q, _ := strconv.ParseInt(firstOf(fnStr(h["hold_qty"]), firstOf(fnStr(h["holdqty"]), "0")), 10, 64)
			if isin != "" {
				have[isin] += q
			}
		}
	} else if !strings.Contains(strings.ToLower(err.Error()), "no data") {
		return out, fmt.Errorf("%w: Finspot did not give the holdings: %s", ErrState, err.Error())
	}
	hs, _, _ := docs.Get[FinspotAPIHolding](ctx, o.Docs, tenant, cFNHold, ucc)
	isins := map[string]bool{}
	for k := range have {
		isins[k] = true
	}
	for k := range hs.Hold {
		isins[k] = true
	}
	out.HoldMatch = true
	for _, k := range sortedKeys(isins) {
		hc := FinspotAPIHoldCheck{ISIN: k, Finspot: have[k], Sent: hs.Hold[k], Match: have[k] == hs.Hold[k]}
		out.HoldMatch = out.HoldMatch && hc.Match
		out.Holdings = append(out.Holdings, hc)
	}
	o.log(ctx, tenant, actor, "finspot_api.check", ucc, map[string]any{"limit_match": out.LimitMatch, "hold_match": out.HoldMatch})
	return out, nil
}
