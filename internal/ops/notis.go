package ops

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/money"
)

// ---- NSE trade API (NOTIS) ----
//
// NSE's Online Trade Inquiry Service gives a member its trades during the
// day (NOTIS CM and F&O API, version 2.1). Ledgerline asks for everything
// after the last sequence number it holds, at most once every 15 seconds
// per consumer key and never between 05:00 and 06:30, as NSE requires
// (breaking either rule gets the IP removed from NSE's list). Each event —
// an original trade, a client-code modification, a cancellation — arrives
// as a new row with a new sequence number; Ledgerline keeps the latest state
// of each trade. Live risk uses these trades until the end-of-day trade
// file arrives, which stays the record for contract notes and bills.
//
// Each segment has its own consumer key and secret (Settings →
// Connections: "NSE trade API (NOTIS), cash" and "… F&O"), issued by NSE
// to the member's registered static IP.

const (
	connNotisCM = "notis-cm"
	connNotisFO = "notis-fo"
	cNotisState = "notisstate"
	cNotisTrade = "notistrade"
	cNotisAct   = "notisaction"

	notisLive = "https://www.connect2nse.com"
	notisUAT  = "https://www.devconnect2nse.com"
	// nseEpoch is 1980-01-01 00:00:00 IST in Unix seconds: NSE's times are
	// seconds from it, and trade times are "jiffies" (1/65536 s) from it.
	nseEpoch = 315513000
)

var notisPaths = map[string][2]string{ // segment -> trades, actions
	"CM": {"/notis-cm/trades-inquiry", "/notis-cm/actions-inquiry"},
	"FO": {"/inquiry-fo/trades-inquiry", "/inquiry-fo/actions-inquiry"},
}

func init() {
	f := func() []ConnField {
		return []ConnField{{Key: "member", Label: "Member code (5 digits)"}, {Key: "api_key", Label: "Consumer key", Secret: true}, {Key: "secret", Label: "Consumer secret", Secret: true},
			{Key: "env", Label: "Environment", Options: []string{"Live", "UAT"}}, {Key: "filter", Label: "Trades to fetch", Options: []string{"ALL", "TMTRADES"}, Optional: true},
			{Key: "fetch", Label: "Fetch during the day", Options: []string{"On", "Off"}}, {Key: "url", Label: "Address (leave empty for NSE's)", Optional: true, Placeholder: notisLive}, fNotes}
	}
	ConnProviders = append(ConnProviders,
		ConnProvider{ID: connNotisCM, Name: "NSE trade API (NOTIS), cash market", Group: "Exchanges", Test: "https", Fields: f(),
			Purpose: "Cash-market trades from NSE during the day, for live risk. Credentials from NSE (mail msm@nse.co.in) for the server's static IP"},
		ConnProvider{ID: connNotisFO, Name: "NSE trade API (NOTIS), F&O", Group: "Exchanges", Test: "https", Fields: f(),
			Purpose: "F&O trades from NSE during the day, for live risk. Credentials from NSE (Member Portal → API Management) for the server's static IP"},
	)
}

// NotisState is where a segment's feed stands.
type NotisState struct {
	Segment   string `json:"segment"`
	Date      string `json:"date"` // NSE's current trade date (YYYY-MM-DD)
	TradeSeq  int64  `json:"trade_seq"`
	ActionSeq int64  `json:"action_seq"`
	MsgDate   string `json:"msg_date"`
	MsgSeq    int    `json:"msg_seq"`
	Token     string `json:"token,omitempty"`
	TokenExp  string `json:"token_exp,omitempty"`
	LastCall  string `json:"last_call,omitempty"`
	LastOK    string `json:"last_ok,omitempty"`
	LastError string `json:"last_error,omitempty"`
	Trades    int    `json:"trades"`  // trades held for Date
	Events    int    `json:"events"`  // rows received for Date
	Actions   int    `json:"actions"` // action rows received for Date
	Next      string `json:"next"`    // trades or actions, for the next call
}

// NotisTrade is the latest state of one trade side.
type NotisTrade struct {
	Segment    string    `json:"segment"`
	Date       string    `json:"date"`
	TradeNo    string    `json:"trade_no"`
	Side       string    `json:"side"` // B or S
	Seq        int64     `json:"seq"`
	Time       string    `json:"time"`
	OrderNo    string    `json:"order_no"`
	Pro        bool      `json:"pro"`
	Client     string    `json:"client"`
	CP         string    `json:"cp,omitempty"`
	Symbol     string    `json:"symbol"`
	Series     string    `json:"series,omitempty"`
	Instrument string    `json:"instrument"` // EQ, FUT, CE, PE
	Expiry     string    `json:"expiry,omitempty"`
	Strike     string    `json:"strike,omitempty"`
	Qty        int64     `json:"qty"`
	Price      money.Dec `json:"price"`
	Terminal   string    `json:"terminal,omitempty"`
	Settlement string    `json:"settlement,omitempty"`
	TxnCode    string    `json:"txn_code"`
	Activity   string    `json:"activity"`
	Status     string    `json:"status,omitempty"` // FO: P, A, R
	Cancelled  bool      `json:"cancelled"`
	Events     int       `json:"events"`
}

// NotisAction is one action row (CP or client-code change, approval).
type NotisAction struct {
	Segment string   `json:"segment"`
	Date    string   `json:"date"`
	Seq     int64    `json:"seq"`
	TradeNo string   `json:"trade_no"`
	Fields  []string `json:"fields"`
}

// NotisAPI is NSE's side, replaced in tests.
type NotisAPI interface {
	Token(ctx context.Context, base, key, secret string) (string, int, error)
	Inquiry(ctx context.Context, base, path, token string, body []byte) (int, []byte, error)
}

type notisHTTP struct{ hc *http.Client }

var notisClient = &http.Client{Timeout: 60 * time.Second}

// notisNonce is base64 of ddMMyyyyHHmmssSSS:<6 random digits>, fresh for
// every request.
func notisNonce(now time.Time) string {
	n, _ := rand.Int(rand.Reader, big.NewInt(1000000))
	t := now.In(ist)
	return base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%s%03d:%06d", t.Format("02012006150405"), t.Nanosecond()/1e6, n.Int64())))
}

func (h notisHTTP) Token(ctx context.Context, base, key, secret string) (string, int, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/token", strings.NewReader(url.Values{"grant_type": {"client_credentials"}}.Encode()))
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(key+":"+secret)))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("nonce", notisNonce(time.Now()))
	resp, err := h.hc.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != 200 {
		return "", resp.StatusCode, fmt.Errorf("NSE refused the login (%d): %s", resp.StatusCode, trimBody(b))
	}
	var r struct {
		Token   string      `json:"access_token"`
		Expires json.Number `json:"expires_in"` // a string in one sample, a number in another
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if err := d.Decode(&r); err != nil || r.Token == "" {
		return "", 200, fmt.Errorf("no token in NSE's reply")
	}
	exp, _ := strconv.Atoi(strings.Trim(r.Expires.String(), `"`))
	if exp <= 0 {
		exp = 3600
	}
	return r.Token, exp, nil
}

func (h notisHTTP) Inquiry(ctx context.Context, base, path, token string, body []byte) (int, []byte, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("nonce", notisNonce(time.Now()))
	resp, err := h.hc.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	return resp.StatusCode, b, nil
}

func trimBody(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

func (o *Ops) notisAPI() NotisAPI {
	if o.Notis != nil {
		return o.Notis
	}
	return notisHTTP{hc: notisClient}
}

// One call per consumer key every 15 seconds, across the whole server.
var (
	notisMu   sync.Mutex
	notisLast = map[string]time.Time{}
)

func notisSlot(key string, now time.Time) bool {
	notisMu.Lock()
	defer notisMu.Unlock()
	if t, ok := notisLast[key]; ok && now.Sub(t) < 15*time.Second {
		return false
	}
	notisLast[key] = now
	return true
}

// notisOpen says whether NSE's service is up: 06:30 to 05:00 the next day.
func notisOpen(now time.Time) bool {
	hm := now.In(ist).Format("15:04")
	return !(hm >= "05:00" && hm < "06:30")
}

func notisConn(c Connection) (base, member, key, secret, filter string, err error) {
	member, key, secret = strings.TrimSpace(c.Values["member"]), c.Secrets["api_key"], c.Secrets["secret"]
	if len(member) > 5 || member == "" || key == "" || secret == "" {
		return "", "", "", "", "", invalid("the NOTIS connection needs the 5-digit member code, consumer key and consumer secret")
	}
	member = fmt.Sprintf("%05s", member)
	base = notisLive
	if c.Values["env"] == "UAT" {
		base = notisUAT
	}
	if u := strings.TrimRight(c.Values["url"], "/"); u != "" {
		base = u
	}
	return base, member, key, secret, firstOf(c.Values["filter"], "ALL"), nil
}

var errNotisWait = errors.New("NSE allows one call every 15 seconds; try again shortly")

// NotisFetch makes one call for a segment: trades, or actions when kind is
// "actions" (or when the state says actions are due). It returns how many
// rows came back.
func (o *Ops) NotisFetch(ctx context.Context, tenant, actor, seg, kind string) (NotisState, int, error) {
	seg = strings.ToUpper(seg)
	id := map[string]string{"CM": connNotisCM, "FO": connNotisFO}[seg]
	if id == "" {
		return NotisState{}, 0, invalid("segment is CM or FO")
	}
	if actor != SystemActor {
		if err := o.require(ctx, tenant, actor, "Fetching trades from NSE", RoleMaker, RoleChecker, RoleRisk); err != nil {
			return NotisState{}, 0, err
		}
	}
	c, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, id)
	if err != nil || c.Status == "Not set" {
		return NotisState{}, 0, fmt.Errorf("%w: set up %s in Settings → Connections", ErrState, id)
	}
	base, member, key, secret, filter, err := notisConn(c)
	if err != nil {
		return NotisState{}, 0, err
	}
	now := o.now()
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cNotisState, seg, NotisState{Segment: seg})
	if !notisOpen(now) {
		return st, 0, fmt.Errorf("%w: NSE's trade API is closed from 05:00 to 06:30", ErrState)
	}
	if !notisSlot(key, now) {
		return st, 0, fmt.Errorf("%w: %v", ErrState, errNotisWait)
	}
	if kind == "" {
		kind = firstOf(st.Next, "trades")
	}
	api := o.notisAPI()
	save := func() { _ = docs.Upsert(ctx, o.Docs, tenant, cNotisState, seg, st, SystemActor) }
	st.LastCall = now.Format(time.RFC3339)
	// The token lasts hours, and NSE allows only one per consumer key:
	// a new one is asked for only when the held one has expired.
	if exp, err := time.Parse(time.RFC3339, st.TokenExp); st.Token == "" || err != nil || now.After(exp) {
		tok, secs, err := api.Token(ctx, base, key, secret)
		if err != nil {
			st.LastError = err.Error()
			save()
			return st, 0, err
		}
		st.Token, st.TokenExp = tok, now.Add(time.Duration(secs-60)*time.Second).Format(time.RFC3339)
		save()
		return st, 0, nil // the login used this 15-second slot
	}
	day := now.In(ist).Format("20060102")
	if st.MsgDate != day {
		st.MsgDate, st.MsgSeq = day, 0
	}
	st.MsgSeq++
	msgID := fmt.Sprintf("%s%s%07d", member, day, st.MsgSeq)
	cursor, field, path := st.TradeSeq, "tradesInquiry", notisPaths[seg][0]
	if kind == "actions" {
		f := "ALL"
		if filter == "TMTRADES" && seg == "CM" {
			f = "TMACTIONS"
		}
		filter, cursor, field, path = f, st.ActionSeq, "actionsInquiry", notisPaths[seg][1]
	}
	body, _ := json.Marshal(map[string]any{"version": "1.0", "data": map[string]string{"msgId": msgID, "dataFormat": "CSV:CSV", field: fmt.Sprintf("%d,%s,,", cursor, filter)}})
	code, resp, err := api.Inquiry(ctx, base, path, st.Token, body)
	if err == nil && (code == 572 || code == 401) {
		st.Token, st.TokenExp = "", "" // log in again on the next call
		err = fmt.Errorf("NSE answered %d (%s); logging in again next time", code, map[int]string{572: "token expired", 401: "token or IP not accepted"}[code])
	} else if err == nil && code != 200 {
		err = fmt.Errorf("NSE answered %d: %s", code, trimBody(resp))
	}
	if err != nil {
		st.LastError = err.Error()
		save()
		return st, 0, err
	}
	ctl, recs, err := notisParse(resp, msgID, field)
	if err != nil {
		st.LastError = err.Error()
		save()
		return st, 0, err
	}
	date := ctl.Date
	if date != "" && date != st.Date {
		// A new trade date: both cursors start again from zero, and this
		// reply (asked with yesterday's cursor) is discarded.
		if st.Date != "" && cursor > 0 {
			st.Date, st.TradeSeq, st.ActionSeq, st.Trades, st.Events, st.Actions, st.LastError = date, 0, 0, 0, 0, 0, ""
			save()
			return st, 0, nil
		}
		st.Date = date
	}
	n := 0
	if kind == "actions" {
		for _, f := range recs {
			if len(f) < 3 {
				continue
			}
			seq, _ := strconv.ParseInt(f[1], 10, 64)
			a := NotisAction{Segment: seg, Date: st.Date, Seq: seq, TradeNo: f[2], Fields: f}
			_ = docs.Upsert(ctx, o.Docs, tenant, cNotisAct, fmt.Sprintf("%s|%s|%d|%d", seg, st.Date, ctl.MaxSeq, n), a, SystemActor)
			n++
		}
		st.Actions += n
		if ctl.MaxSeq > st.ActionSeq {
			st.ActionSeq = ctl.MaxSeq
		}
		st.Next = "trades"
	} else {
		maxSeq := ctl.MaxSeq
		for _, f := range recs {
			t, err := notisTrade(seg, f)
			if err != nil {
				continue
			}
			if t.Seq > maxSeq {
				maxSeq = t.Seq
			}
			k := strings.Join([]string{seg, st.Date, t.TradeNo, t.Side}, "|")
			old, _, gerr := docs.Get[NotisTrade](ctx, o.Docs, tenant, cNotisTrade, k)
			if gerr == nil {
				if t.Seq <= old.Seq {
					continue // an event already applied
				}
				t.Events = old.Events + 1
				if t.Activity == "5" { // CM pre-image of a client modification: keep the trade as it was
					old.Seq, old.Events = t.Seq, t.Events
					t = old
				}
			} else {
				t.Events = 1
				st.Trades++
			}
			t.Date = st.Date
			_ = docs.Upsert(ctx, o.Docs, tenant, cNotisTrade, k, t, SystemActor)
			n++
		}
		st.Events += n
		if maxSeq > st.TradeSeq {
			st.TradeSeq = maxSeq
		}
		// Actions are asked for every fifth call.
		if st.MsgSeq%5 == 0 {
			st.Next = "actions"
		} else {
			st.Next = "trades"
		}
	}
	st.LastOK, st.LastError = now.Format(time.RFC3339), ""
	save()
	o.log(ctx, tenant, actor, "notis.fetched", seg+"/"+kind, map[string]any{"rows": n, "cursor": cursor, "msg_id": msgID})
	return st, n, nil
}

type notisControl struct {
	Date   string
	MaxSeq int64
	Count  int
}

// notisParse reads NSE's reply: a JSON envelope whose data.<field> is CSV
// records separated by "^", the first one a control record.
func notisParse(b []byte, msgID, field string) (notisControl, [][]string, error) {
	var env struct {
		Status   string `json:"status"`
		Messages struct {
			Code string `json:"code"`
		} `json:"messages"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(b, &env); err != nil {
		return notisControl{}, nil, fmt.Errorf("NSE's reply is not JSON: %s", trimBody(b))
	}
	if !strings.EqualFold(env.Status, "success") || env.Messages.Code != "01010000" {
		return notisControl{}, nil, fmt.Errorf("NSE answered %s (code %s)", firstOf(env.Status, "an error"), env.Messages.Code)
	}
	if got, _ := env.Data["msgId"].(string); got != "" && strings.TrimSpace(got) != msgID {
		return notisControl{}, nil, fmt.Errorf("NSE's reply is for message %s, not %s", got, msgID)
	}
	csv, _ := env.Data[field].(string)
	var ctl notisControl
	var recs [][]string
	for i, r := range strings.Split(csv, "^") {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		f := strings.Split(r, ",")
		for j := range f {
			f[j] = strings.TrimSpace(f[j])
		}
		if i == 0 {
			if len(f) < 6 {
				return ctl, nil, fmt.Errorf("NSE's control record has %d fields, not 6", len(f))
			}
			if len(f[1]) == 8 {
				ctl.Date = f[1][:4] + "-" + f[1][4:6] + "-" + f[1][6:]
			}
			ctl.MaxSeq, _ = strconv.ParseInt(f[4], 10, 64)
			ctl.Count, _ = strconv.Atoi(f[5])
			continue
		}
		recs = append(recs, f)
	}
	return ctl, recs, nil
}

func fromJiffy(j int64) time.Time { return time.Unix(j/65536+nseEpoch, (j%65536)*1e9/65536).In(ist) }
func from1980(s int64) time.Time  { return time.Unix(s+nseEpoch, 0).In(ist) }

var notisCancel = map[string]bool{"5440": true, "5520": true, "5560": true}
var notisCancelAct = map[string]bool{"7": true, "109": true, "110": true, "111": true}

// notisTrade reads one trade row: 39 fields for cash, 37 for F&O. A cash
// security name containing commas is put back together.
func notisTrade(seg string, f []string) (NotisTrade, error) {
	want := map[string]int{"CM": 39, "FO": 37}[seg]
	if seg == "CM" && len(f) > want {
		extra := len(f) - want
		name := strings.Join(f[28:29+extra], ",")
		f = append(append(append([]string{}, f[:28]...), name), f[29+extra:]...)
	}
	if len(f) != want {
		return NotisTrade{}, fmt.Errorf("trade row has %d fields, not %d", len(f), want)
	}
	n := func(i int) int64 { v, _ := strconv.ParseInt(f[i], 10, 64); return v }
	paise := func(i int) money.Dec { return money.DecInt(n(i)).Mul(money.MustDec("0.01")) }
	t := NotisTrade{Segment: seg, TradeNo: f[2], Qty: n(5), Price: paise(6), OrderNo: f[8], Pro: f[11] == "2", Client: f[12], CP: f[13],
		Activity: f[15], TxnCode: f[16]}
	t.Seq, _ = strconv.ParseInt(f[0], 10, 64)
	t.Side = map[string]string{"1": "B", "2": "S"}[f[7]]
	if t.Side == "" || t.TradeNo == "" || t.Qty <= 0 {
		return t, fmt.Errorf("incomplete trade row")
	}
	t.Time = fromJiffy(n(3)).Format("15:04:05")
	switch seg {
	case "CM":
		t.Settlement, t.Terminal, t.Symbol, t.Series, t.Instrument = f[20], f[23], f[26], f[27], "EQ"
	case "FO":
		t.Terminal, t.Status, t.Symbol, t.Series = f[20], f[21], f[23], f[24]
		inst := strings.ToUpper(f[25])
		t.Expiry = from1980(n(26)).Format("2006-01-02")
		switch {
		case strings.HasPrefix(inst, "FUT"):
			t.Instrument = "FUT"
		case strings.HasPrefix(inst, "OPT"):
			t.Instrument = map[string]string{"CE": "CE", "CA": "CE", "PE": "PE", "PA": "PE"}[strings.ToUpper(f[28])]
			if k := n(27); k > 0 {
				t.Strike = paise(27).String()
			}
		}
		if t.Instrument == "" {
			return t, fmt.Errorf("unknown instrument %s %s", inst, f[28])
		}
	}
	t.Cancelled = notisCancel[t.TxnCode] || notisCancelAct[t.Activity]
	return t, nil
}

// NotisTrades are the held trades of a segment and date, as trade events
// (cancelled ones left out).
func (o *Ops) NotisTrades(ctx context.Context, tenant, date string) ([]ingest.TradeEvent, error) {
	var out []ingest.TradeEvent
	for _, seg := range []string{"CM", "FO"} {
		ts, err := docs.List[NotisTrade](ctx, o.Docs, tenant, cNotisTrade, seg+"|"+date+"|")
		if err != nil {
			return nil, err
		}
		for _, t := range ts {
			if t.Cancelled || t.Status == "R" {
				continue
			}
			ucc := strings.ToUpper(t.Client)
			if t.Pro {
				ucc = "PRO"
			}
			out = append(out, ingest.TradeEvent{Tenant: tenant, Origin: ingest.FromExchange, Exchange: "NSE", Segment: seg, TradeNo: t.TradeNo, OrderNo: t.OrderNo,
				UCC: ucc, Symbol: t.Symbol, InstrumentType: t.Instrument, Expiry: t.Expiry, Strike: t.Strike, Side: t.Side, Qty: t.Qty, Price: t.Price,
				TradeDate: date, TradeTime: t.Time, Terminal: t.Terminal, Settlement: t.Settlement})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].TradeTime < out[j].TradeTime })
	return out, nil
}

// NotisView is the feed's state for the screen.
type NotisView struct {
	Segments []NotisState `json:"segments"`
}

// NotisStatus shows where each segment's feed stands.
func (o *Ops) NotisStatus(ctx context.Context, tenant string) NotisView {
	v := NotisView{}
	for _, seg := range []string{"CM", "FO"} {
		st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cNotisState, seg, NotisState{Segment: seg})
		st.Token = ""
		v.Segments = append(v.Segments, st)
	}
	return v
}

// notisTick makes the next due call for each segment whose fetching is on,
// during the day.
func (o *Ops) notisTick(ctx context.Context, tenant string, now time.Time) {
	if hm := now.In(ist).Format("15:04"); hm < "09:00" || hm > "16:30" {
		return
	}
	for seg, id := range map[string]string{"CM": connNotisCM, "FO": connNotisFO} {
		if c, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, id); err == nil && c.Values["fetch"] == "On" && c.Status != "Not set" {
			_, _, _ = o.NotisFetch(ctx, tenant, SystemActor, seg, "")
		}
	}
}
