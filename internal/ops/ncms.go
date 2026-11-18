package ops

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
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

// ---- NSE Clearing NCMS API, F&O (trades, actions and CP codes) ----
//
// NCMS is NSE Clearing's trade management system. Its F&O API (version 2.2,
// 22 Aug 2025) gives the member the day's trades and every action on them
// (CP code changes, a custodian's approval or rejection) and takes CP code
// changes. An NRI client trades derivatives through a custodial participant
// (CP) code (nri.go): a trade punched without the client's CP code, or with
// another, stays in the firm's own books unless the CP code is corrected in
// NCMS the same day. Ledgerline reads the trades every five minutes during
// the day and, when switched on, sends the correction itself; a refused
// correction, or a trade the custodian rejects, goes to a person.
//
// The custodian's own work (approving given-up trades, approve all; for
// cash, OTR allocation and ISO files) is not done here: approving takes on
// the obligation of another member's trades.
//
// Login: OAuth client credentials (Basic consumer key and secret, a nonce),
// answered with a bearer token for an hour. Requests are JSON with the data
// as CSV; records are separated by "^".

const (
	connNCMS    = "ncl-ncms"
	cNCMSState  = "ncmsstate" // key: segment
	cNCMSTrade  = "ncmstrade" // key: seg|date|tradeUniqID
	cNCMSChange = "ncmscp"    // key: seg|date|tradeUniqID: a CP change sent
	ncmsLive    = "https://www.connect2nse.com"
	ncmsUAT     = "https://uat.connect2nsccl.com"
	ncmsModMax  = 15000 // CP changes in one request
)

func init() {
	ConnProviders = append(ConnProviders, ConnProvider{ID: connNCMS, Name: "NSE Clearing NCMS API (F&O trades and CP codes)", Group: "Exchanges", Test: "https",
		Purpose: "The day's F&O trades and actions from NSE Clearing's NCMS, and CP code corrections for NRI clients' trades. Register with NSE Clearing (member code, name, e-mail and this server's address); the consumer key and secret come by e-mail",
		Fields: []ConnField{{Key: "member", Label: "Clearing member code (5 characters)"},
			{Key: "api_key", Label: "Consumer key", Secret: true}, {Key: "secret", Label: "Consumer secret", Secret: true},
			{Key: "env", Label: "Environment", Options: []string{"Live", "UAT"}},
			{Key: "fetch", Label: "Read trades and actions during the day", Options: []string{"On", "Off"}},
			{Key: "fix", Label: "Correct NRI clients' CP codes automatically", Options: []string{"Off", "On"}},
			{Key: "url", Label: "Address (leave empty for NSE Clearing's)", Optional: true, Placeholder: ncmsLive}, fNotes}})
}

// NCMSState is where the segment's trade feed stands.
type NCMSState struct {
	Segment   string `json:"segment"`
	Date      string `json:"date"`     // the trading day the sequence belongs to
	Seq       int64  `json:"seq"`      // the highest sequence number read
	MsgSeq    int    `json:"msg_seq"`  // message numbers spent today
	Token     string `json:"-"`        // held in memory only
	LastRun   string `json:"last_run"` // RFC 3339
	LastOK    string `json:"last_ok,omitempty"`
	LastError string `json:"last_error,omitempty"`
	Trades    int    `json:"trades"`  // original trades read today
	Actions   int    `json:"actions"` // actions read today
	Sent      int    `json:"sent"`    // CP changes sent today
}

// NCMSTrade is one F&O trade as NCMS holds it, with what happened to it.
type NCMSTrade struct {
	Segment    string    `json:"segment"`
	Date       string    `json:"date"`
	Seq        int64     `json:"seq"`
	TradeNo    string    `json:"trade_no"`
	OrderNo    string    `json:"order_no"`
	UniqID     string    `json:"uniq_id"`
	UCC        string    `json:"ucc"`
	CPCode     string    `json:"cp_code"`
	BuySell    string    `json:"buy_sell"` // 1 buy, 2 sell
	Symbol     string    `json:"symbol"`
	Instrument string    `json:"instrument"`
	Qty        int64     `json:"qty"`
	Price      money.Dec `json:"price"`  // rupees
	Status     string    `json:"status"` // P pending, A approved, R rejected, C cancelled
	TM         string    `json:"tm"`
	// The latest action on the trade and its result.
	Action    string `json:"action,omitempty"`
	ActionErr string `json:"action_error,omitempty"`
	ActionAt  string `json:"action_at,omitempty"`
}

// NCMSChange is a CP code correction Ledgerline sent.
type NCMSChange struct {
	Segment string `json:"segment"`
	Date    string `json:"date"`
	UniqID  string `json:"uniq_id"`
	UCC     string `json:"ucc"`
	TradeNo string `json:"trade_no"`
	Old     string `json:"old_cp"`
	New     string `json:"new_cp"`
	MsgID   string `json:"msg_id"`
	At      string `json:"at"`
	Status  string `json:"status"` // Sent, Done, Refused
	Error   string `json:"error,omitempty"`
}

// NCMSAPI is NSE Clearing's side, replaced in tests.
type NCMSAPI interface {
	Post(ctx context.Context, u string, h http.Header, body []byte) (int, []byte, error)
}

type ncmsHTTP struct{ c *http.Client }

func (h ncmsHTTP) Post(ctx context.Context, u string, hd http.Header, body []byte) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header = hd
	resp, err := h.c.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	return resp.StatusCode, b, err
}

func (o *Ops) ncmsAPI() NCMSAPI {
	if o.NCMS != nil {
		return o.NCMS
	}
	return ncmsHTTP{c: &http.Client{Timeout: 60 * time.Second}}
}

type ncmsConn struct {
	base, tokenURL, member, key, secret string
	fetch, fix                          bool
}

func parseNCMSConn(c Connection) (ncmsConn, error) {
	n := ncmsConn{member: strings.ToUpper(strings.TrimSpace(c.Values["member"])), key: c.Secrets["api_key"], secret: c.Secrets["secret"],
		fetch: c.Values["fetch"] != "Off", fix: c.Values["fix"] == "On"}
	if len(n.member) != 5 || n.key == "" || n.secret == "" {
		return n, invalid("the NCMS connection needs the 5 character clearing member code, the consumer key and the consumer secret")
	}
	n.base, n.tokenURL = ncmsLive, ncmsLive+"/ncms-token"
	if c.Values["env"] == "UAT" {
		n.base, n.tokenURL = ncmsUAT, ncmsUAT+"/token"
	}
	if u := strings.TrimRight(c.Values["url"], "/"); u != "" {
		n.base, n.tokenURL = u, u+"/token"
	}
	return n, nil
}

// Tokens are held in memory: an hour each, per tenant.
var ncmsTokens sync.Map // tenant -> ncmsToken

type ncmsToken struct {
	tok string
	exp time.Time
}

var ncmsMu sync.Mutex

func (o *Ops) ncmsLogin(ctx context.Context, tenant string, c ncmsConn, now time.Time, fresh bool) (string, error) {
	if t, ok := ncmsTokens.Load(tenant); ok && !fresh && now.Before(t.(ncmsToken).exp) {
		return t.(ncmsToken).tok, nil
	}
	h := http.Header{}
	h.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(c.key+":"+c.secret)))
	h.Set("Content-Type", "application/x-www-form-urlencoded")
	nclmHeaders(h, now)
	code, b, err := o.ncmsAPI().Post(ctx, c.tokenURL, h, []byte(url.Values{"grant_type": {"client_credentials"}}.Encode()))
	if err != nil {
		return "", fmt.Errorf("could not reach NSE Clearing's NCMS: %v", err)
	}
	var t struct {
		Token   string      `json:"access_token"`
		Expires json.Number `json:"expires_in"`
	}
	if code != 200 || json.Unmarshal(b, &t) != nil || t.Token == "" {
		return "", fmt.Errorf("NSE Clearing's NCMS refused the login (%d %s)", code, trimBody(b))
	}
	secs, _ := strconv.Atoi(t.Expires.String())
	if secs <= 0 || secs > 3600 {
		secs = 3600
	}
	ncmsTokens.Store(tenant, ncmsToken{t.Token, now.Add(time.Duration(max(secs-120, 30)) * time.Second)})
	return t.Token, nil
}

// ncmsCall posts one request and returns its data.
func (o *Ops) ncmsCall(ctx context.Context, tenant string, c ncmsConn, path string, data map[string]any, now time.Time) (map[string]json.RawMessage, error) {
	body, _ := json.Marshal(map[string]any{"version": "1.0", "data": data})
	for try := 0; try < 2; try++ {
		tok, err := o.ncmsLogin(ctx, tenant, c, now, try > 0)
		if err != nil {
			return nil, err
		}
		h := http.Header{}
		h.Set("Authorization", "Bearer "+tok)
		h.Set("Content-Type", "application/json")
		nclmHeaders(h, now)
		code, b, err := o.ncmsAPI().Post(ctx, c.base+path, h, body)
		if err != nil {
			return nil, fmt.Errorf("could not reach NSE Clearing's NCMS: %v", err)
		}
		if code == 401 && try == 0 {
			continue // the token ran out: log in again once
		}
		var env struct {
			Status   string                     `json:"status"`
			Messages json.RawMessage            `json:"messages"`
			Data     map[string]json.RawMessage `json:"data"`
		}
		_ = json.Unmarshal(b, &env)
		mcode := ""
		var m map[string]any
		if json.Unmarshal(env.Messages, &m) == nil {
			mcode = fmt.Sprint(m["code"])
		}
		if code != 200 || (env.Status != "" && !strings.EqualFold(env.Status, "success")) {
			return nil, fmt.Errorf("NSE Clearing's NCMS answered %d %s", code, firstOf(ncmsCodeText(mcode), trimBody(b)))
		}
		return env.Data, nil
	}
	return nil, fmt.Errorf("NSE Clearing's NCMS did not accept a fresh login token")
}

// ncmsCodeText says what a message code means.
func ncmsCodeText(code string) string {
	if len(code) < 8 {
		return code
	}
	return firstOf(map[string]string{"0000": "accepted", "0001": "duplicate message number", "0201": "too short", "0202": "too long", "0204": "missing",
		"0206": "wrong format", "0241": "system error at NSE Clearing", "0242": "service unavailable", "0243": "request not understood", "0244": "no data"}[code[4:]], "code "+code)
}

// ncmsErrText says what an action's error code means.
func ncmsErrText(code string) string {
	return firstOf(map[string]string{"1": "system in wrong state", "2": "invalid contract", "3": "invalid participant", "4": "trade not found", "5": "trade already cancelled",
		"6": "system error", "7": "trade already approved", "8": "trade already rejected", "9": "outstanding alert", "10": "invalid user", "11": "invalid data",
		"12": "the clearing member is in view-only mode", "13": "the clearing member is disabled", "-12": "not the latest trade", "-20": "invalid sequence number",
		"-21": "invalid clearing member", "-22": "invalid CP code", "-23": "invalid buy/sell flag", "-31": "invalid order number", "-32": "invalid trade number",
		"-50": "already submitted", "-51": "already approved", "-52": "already rejected"}[code], "error "+code)
}

// ncmsActions name the action types.
var ncmsActions = map[string]string{"1": "Original trade", "2": "Buy SI generated", "3": "Sell SI generated", "4": "Approved (buy)", "5": "Approved (sell)",
	"6": "CP changed, old CP (buy)", "7": "CP changed, old CP (sell)", "8": "CP changed, new CP (buy)", "9": "CP changed, new CP (sell)", "14": "Rejected (buy)",
	"15": "Rejected (sell)", "16": "Buy SI cancelled", "17": "Sell SI cancelled", "20": "Approval window closed"}

// ncmsRecords reads the inquiry's CSV: the control record (max sequence,
// count) and the trade records (37 fields), separated by "^".
func ncmsRecords(text string) (int64, [][]string) {
	var maxSeq int64
	var out [][]string
	for _, rec := range strings.FieldsFunc(text, func(r rune) bool { return r == '^' || r == '\n' || r == '\r' }) {
		f := strings.Split(rec, ",")
		for i := range f {
			f[i] = strings.TrimSpace(f[i])
		}
		if len(f) >= 30 {
			out = append(out, f)
			if s, err := strconv.ParseInt(f[0], 10, 64); err == nil && s > maxSeq {
				maxSeq = s
			}
			continue
		}
		if len(f) >= 2 { // control record: maxSeqNo, noOfRec
			if s, err := strconv.ParseInt(f[0], 10, 64); err == nil && s > maxSeq {
				maxSeq = s
			}
		}
	}
	return maxSeq, out
}

// NCMSRun is what one read did.
type NCMSRun struct {
	Trades   int      `json:"trades"`
	Actions  int      `json:"actions"`
	Sent     int      `json:"cp_changes_sent"`
	Problems []string `json:"problems,omitempty"`
}

// NCMSFetch reads the F&O trades and actions since the last read, and, when
// switched on (or asked), corrects NRI clients' CP codes.
func (o *Ops) NCMSFetch(ctx context.Context, tenant, actor string, fix bool) (NCMSRun, error) {
	var run NCMSRun
	if actor != SystemActor {
		if err := o.require(ctx, tenant, actor, "Reading trades from NSE Clearing's NCMS", RoleMaker, RoleChecker, RoleRisk); err != nil {
			return run, err
		}
	}
	cn, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connNCMS)
	if err != nil || cn.Status == "Not set" {
		return run, fmt.Errorf("%w: set up the NSE Clearing NCMS API in Settings → Connections", ErrState)
	}
	c, err := parseNCMSConn(cn)
	if err != nil {
		return run, err
	}
	ncmsMu.Lock()
	defer ncmsMu.Unlock()
	now := o.now()
	date := now.In(ist).Format("2006-01-02")
	seg := "FO"
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cNCMSState, seg, NCMSState{Segment: seg})
	if st.Date != date {
		st = NCMSState{Segment: seg, Date: date}
	}
	st.LastRun = now.Format(time.RFC3339)
	save := func() { _ = docs.Upsert(ctx, o.Docs, tenant, cNCMSState, seg, st, SystemActor) }
	msgID := func() string {
		st.MsgSeq++
		return fmt.Sprintf("%s%s%07d", c.member, now.In(ist).Format("20060102"), st.MsgSeq)
	}
	// Read until NSE Clearing has nothing newer (it sends a part at a time).
	for page := 0; page < 50; page++ {
		data, err := o.ncmsCall(ctx, tenant, c, "/ncms-fo/trd-act-inquiry", map[string]any{"msgId": msgID(), "dataFormat": "CSV:CSV", "trdactInquiry": fmt.Sprintf("%d,ALLTRDACT,,", st.Seq)}, now)
		if err != nil {
			st.LastError = err.Error()
			save()
			return run, err
		}
		var text string
		for k, v := range data {
			if strings.EqualFold(k, "trdactInquiry") {
				_ = json.Unmarshal(v, &text)
			}
		}
		maxSeq, recs := ncmsRecords(text)
		for _, f := range recs {
			if o.ncmsApply(ctx, tenant, seg, date, f) {
				run.Actions++
			} else {
				run.Trades++
			}
		}
		if maxSeq <= st.Seq || len(recs) == 0 {
			break
		}
		st.Seq = maxSeq
	}
	st.Trades += run.Trades
	st.Actions += run.Actions
	if fix || c.fix {
		n, problems := o.ncmsFixCP(ctx, tenant, c, seg, date, now, msgID)
		run.Sent, run.Problems = n, problems
		st.Sent += n
	}
	st.LastError = ""
	if len(run.Problems) > 0 {
		st.LastError = run.Problems[0]
	} else {
		st.LastOK = now.Format(time.RFC3339)
	}
	save()
	o.log(ctx, tenant, actor, "ncms.fetched", seg+"/"+date, map[string]any{"trades": run.Trades, "actions": run.Actions, "cp_changes": run.Sent})
	return run, nil
}

// ncmsApply stores an original trade, or applies an action to its trade
// (true for an action).
func (o *Ops) ncmsApply(ctx context.Context, tenant, seg, date string, f []string) bool {
	for len(f) < 37 {
		f = append(f, "")
	}
	uniq := f[30]
	if uniq == "" {
		uniq = f[4] + "-" + f[2] // token and trade number
	}
	key := seg + "|" + date + "|" + uniq
	seq, _ := strconv.ParseInt(f[0], 10, 64)
	if f[16] == "9001" { // an action on a trade
		act, errCd := f[33], strings.TrimSpace(f[31])
		_, _ = docs.Update(ctx, o.Docs, tenant, cNCMSTrade, key, SystemActor, func(t *NCMSTrade) error {
			t.Action, t.ActionAt, t.ActionErr = firstOf(ncmsActions[act], "action "+act), o.now().Format(time.RFC3339), ""
			if errCd != "" && errCd != "0" {
				t.ActionErr = ncmsErrText(errCd)
			}
			switch act {
			case "4", "5":
				t.Status = "A"
			case "14", "15":
				t.Status = "R"
			case "8", "9":
				if t.ActionErr == "" && f[13] != "" {
					t.CPCode = strings.ToUpper(f[13])
				}
			}
			return nil
		})
		o.ncmsActionResult(ctx, tenant, seg, date, uniq, act, errCd)
		return true
	}
	price := money.Dec{}
	if p, err := money.ParseDec(f[6]); err == nil {
		price, _ = p.Quo(money.DecInt(100))
	}
	qty, _ := strconv.ParseInt(f[5], 10, 64)
	t := NCMSTrade{Segment: seg, Date: date, Seq: seq, TradeNo: f[2], OrderNo: f[8], UniqID: uniq, UCC: strings.ToUpper(f[12]), CPCode: strings.ToUpper(f[13]), BuySell: f[7],
		Symbol: f[23], Instrument: f[25], Qty: qty, Price: price, Status: firstOf(f[21], "P"), TM: f[22]}
	if old, _, err := docs.Get[NCMSTrade](ctx, o.Docs, tenant, cNCMSTrade, key); err == nil {
		t.Action, t.ActionErr, t.ActionAt = old.Action, old.ActionErr, old.ActionAt
		if old.CPCode != t.CPCode && old.Action != "" {
			t.CPCode = old.CPCode
		}
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cNCMSTrade, key, t, SystemActor)
	return false
}

// ncmsActionResult settles a CP change Ledgerline sent, and tells a person
// about a refused change or a custodian's rejection.
func (o *Ops) ncmsActionResult(ctx context.Context, tenant, seg, date, uniq, act, errCd string) {
	key := seg + "|" + date + "|" + uniq
	t, _, _ := docs.Get[NCMSTrade](ctx, o.Docs, tenant, cNCMSTrade, key)
	ch, _, chErr := docs.Get[NCMSChange](ctx, o.Docs, tenant, cNCMSChange, key)
	if chErr == nil && ch.Status == "Sent" && (act == "8" || act == "9" || (errCd != "" && errCd != "0")) {
		ch.Status = "Done"
		if errCd != "" && errCd != "0" {
			ch.Status, ch.Error = "Refused", ncmsErrText(errCd)
			o.raiseWork(ctx, tenant, "ncms", "cp:"+uniq, "CP code change refused for "+ch.UCC+"'s F&O trade "+ch.TradeNo,
				fmt.Sprintf("NSE Clearing's NCMS refused changing the CP code of trade %s (%s) from %q to %s: %s. Correct it in NCMS before the day's cut-off, or the trade stays in the firm's books.", ch.TradeNo, ch.UCC, ch.Old, ch.New, ch.Error),
				ch.UCC, RoleMaker, "nri", date)
		} else {
			o.closeWork(ctx, tenant, SystemActor, "ncms", "cp:"+uniq, "NCMS took the CP code")
		}
		_ = docs.Upsert(ctx, o.Docs, tenant, cNCMSChange, key, ch, SystemActor)
	}
	if act == "14" || act == "15" {
		o.raiseWork(ctx, tenant, "ncms", "rejected:"+uniq, "Custodian rejected "+firstOf(t.UCC, "a client")+"'s F&O trade "+t.TradeNo,
			fmt.Sprintf("The custodian of CP code %s rejected trade %s (%s %d %s). The trade now stays in the firm's books: speak to the client and the custodian, and close out or take it on the firm's account as the firm's policy says.", t.CPCode, t.TradeNo, t.Symbol, t.Qty, t.Instrument),
			t.UCC, RoleRisk, "nri", date)
	}
}

// ncmsFixCP sends CP code corrections for NRI clients' trades whose CP code
// is not the client's.
func (o *Ops) ncmsFixCP(ctx context.Context, tenant string, c ncmsConn, seg, date string, now time.Time, msgID func() string) (int, []string) {
	nris, _ := docs.List[NRIDetails](ctx, o.Docs, tenant, cNRI, "")
	cp := map[string]string{}
	for _, n := range nris {
		if n.CPCode != "" {
			cp[strings.ToUpper(n.UCC)] = strings.ToUpper(strings.TrimSpace(n.CPCode))
		}
	}
	if len(cp) == 0 {
		return 0, nil
	}
	trades, _ := docs.List[NCMSTrade](ctx, o.Docs, tenant, cNCMSTrade, seg+"|"+date+"|")
	sort.Slice(trades, func(i, j int) bool { return trades[i].Seq < trades[j].Seq })
	var mods []map[string]any
	var changes []NCMSChange
	for _, t := range trades {
		want, ok := cp[t.UCC]
		if !ok || t.CPCode == want || t.Status == "C" || t.Status == "R" {
			continue
		}
		key := seg + "|" + date + "|" + t.UniqID
		if _, _, err := docs.Get[NCMSChange](ctx, o.Docs, tenant, cNCMSChange, key); err == nil {
			continue // sent once already; a refusal goes to a person
		}
		bs, _ := strconv.Atoi(t.BuySell)
		trd, _ := strconv.ParseInt(t.TradeNo, 10, 64)
		mods = append(mods, map[string]any{"seqNo": t.Seq, "orderNo": t.OrderNo, "trdNo": trd, "bsFlg": bs, "newCPCode": want, "oldCPCode": t.CPCode, "uniqId": t.UniqID})
		changes = append(changes, NCMSChange{Segment: seg, Date: date, UniqID: t.UniqID, UCC: t.UCC, TradeNo: t.TradeNo, Old: t.CPCode, New: want})
	}
	sent := 0
	var problems []string
	for i := 0; i < len(mods); i += ncmsModMax {
		j := min(i+ncmsModMax, len(mods))
		id := msgID()
		if _, err := o.ncmsCall(ctx, tenant, c, "/ncms-fo/cp-modification", map[string]any{"msgId": id, "dataFormat": "", "cpModData": mods[i:j]}, now); err != nil {
			problems = append(problems, "CP code changes: "+err.Error())
			continue
		}
		for _, ch := range changes[i:j] {
			ch.MsgID, ch.At, ch.Status = id, now.Format(time.RFC3339), "Sent"
			_ = docs.Upsert(ctx, o.Docs, tenant, cNCMSChange, seg+"|"+date+"|"+ch.UniqID, ch, SystemActor)
			sent++
		}
	}
	return sent, problems
}

// NCMSView is the day's NCMS feed for the screen.
type NCMSView struct {
	Set     bool         `json:"set"`
	Fix     bool         `json:"fix"`
	State   NCMSState    `json:"state"`
	Changes []NCMSChange `json:"cp_changes"`
	Wrong   []NCMSTrade  `json:"wrong_cp"` // NRI trades whose CP code is still not the client's
}

// NCMSStatus shows the feed and the day's CP code changes.
func (o *Ops) NCMSStatus(ctx context.Context, tenant string) NCMSView {
	date := o.now().In(ist).Format("2006-01-02")
	v := NCMSView{Changes: []NCMSChange{}, Wrong: []NCMSTrade{}}
	cn, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connNCMS)
	if err != nil || cn.Status == "Not set" {
		return v
	}
	v.Set, v.Fix = true, cn.Values["fix"] == "On"
	v.State, _, _ = docs.GetOr(ctx, o.Docs, tenant, cNCMSState, "FO", NCMSState{Segment: "FO"})
	v.Changes, _ = docs.List[NCMSChange](ctx, o.Docs, tenant, cNCMSChange, "FO|"+date+"|")
	nris, _ := docs.List[NRIDetails](ctx, o.Docs, tenant, cNRI, "")
	cp := map[string]string{}
	for _, n := range nris {
		cp[strings.ToUpper(n.UCC)] = strings.ToUpper(strings.TrimSpace(n.CPCode))
	}
	ts, _ := docs.List[NCMSTrade](ctx, o.Docs, tenant, cNCMSTrade, "FO|"+date+"|")
	for _, t := range ts {
		if want, ok := cp[t.UCC]; ok && want != "" && t.CPCode != want && t.Status != "C" {
			v.Wrong = append(v.Wrong, t)
		}
	}
	return v
}

// ncmsTick reads trades every five minutes from 09:15 to 17:30 on weekdays.
func (o *Ops) ncmsTick(ctx context.Context, tenant string, now time.Time) {
	cn, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connNCMS)
	if err != nil || cn.Status == "Not set" || cn.Values["fetch"] == "Off" {
		return
	}
	t := now.In(ist)
	if hm := t.Format("15:04"); hm < "09:15" || hm > "17:30" || t.Weekday() == time.Saturday || t.Weekday() == time.Sunday {
		return
	}
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cNCMSState, "FO", NCMSState{Segment: "FO"})
	if last, err := time.Parse(time.RFC3339, st.LastRun); err == nil && now.Sub(last) < 5*time.Minute {
		return
	}
	_, _ = o.NCMSFetch(ctx, tenant, SystemActor, false)
}
