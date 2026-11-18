package ops

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// ---- NSE Clearing UPIX (UPI block mechanism) ----
//
// A client who trades on money blocked in their own bank account by UPI
// (the UPI block mechanism) never pays the firm: NSE Clearing debits the
// block for the client's obligations and releases what is left. UPIX is
// NSE Clearing's system for those blocks; its API (protocol "UPIX System")
// tells the member which clients are registered, how much is still blocked
// for each in each segment, and every debit NSE Clearing made on a block.
//
// Ledgerline uses it so that nobody types these in:
//   - Registrations, once a day (and as NSE Clearing notifies them).
//   - Balances every 15 minutes: risk and live RMS use the blocked amount
//     NSE Clearing reports in place of the amount on the client record.
//   - Debits every 15 minutes (and as notified): each is posted to the
//     client's ledger as money received from the client and paid straight
//     to NSE Clearing (debit the clearing account, credit the client), keyed
//     by NSE Clearing's reference so it is posted once.
//
// Security: every call carries a JWT the caller makes (HS256 with a 32-byte
// key shared with NSE Clearing): subject <CM|TM>_<member>, audience NCL,
// issued-at in milliseconds, and the API called ("POST_/v1/inquire/…").
// NSE Clearing's notifications carry one the other way round (subject NCL),
// checked within a minute of the server's time.
//
// NSE Clearing has not yet published the addresses; the Admin enters the one
// it gives. Releasing a block and moving blocked money between segments are
// for the client's own request and are not sent by Ledgerline yet.

const (
	connUPIX   = "ncl-upix"
	cUPIXReg   = "upixreg"   // key: seg|ucc
	cUPIXBal   = "upixbal"   // key: seg|ucc
	cUPIXDebit = "upixdebit" // key: ccRefId
	cUPIXState = "upixstate" // one document
	upixBatch  = 1000
	upixTime   = "02-Jan-2006 15:04:05"
)

var upixSegments = []string{"CM", "FO", "CD", "SL", "CO"}

func init() {
	ConnProviders = append(ConnProviders, ConnProvider{ID: connUPIX, Name: "NSE Clearing UPIX (UPI block)", Group: "Money", Test: "none",
		Purpose: "Clients who trade on money blocked by UPI: their registration, the amount still blocked in each segment (used in risk) and each debit NSE Clearing makes on a block (posted to the client's ledger). NSE Clearing gives the address and the shared key",
		Fields: []ConnField{{Key: "member_type", Label: "Member type", Options: []string{"CM", "TM"}},
			{Key: "member", Label: "Member code (5 characters)"},
			{Key: "cm", Label: "Clearing member code, if not the member code", Optional: true},
			{Key: "tm", Label: "Trading member code, if not the member code", Optional: true},
			{Key: "secret", Label: "Shared key (32 bytes, base64, from NSE Clearing)", Secret: true},
			{Key: "url", Label: "Address (from NSE Clearing)", Optional: true, Placeholder: "https://…"},
			{Key: "version", Label: "API version", Optional: true, Placeholder: "v1"},
			{Key: "segments", Label: "Segments (any of CM, FO, CD, SL, CO, comma separated)", Placeholder: "CM, FO"},
			{Key: "auto", Label: "Check registrations, balances and debits automatically", Options: []string{"On", "Off"}},
			{Key: "post", Label: "Post each block debit to the client's ledger", Options: []string{"On", "Off"}}, fNotes}})
}

// UPIXAPI is NSE Clearing's side, replaced in tests.
type UPIXAPI interface {
	Post(ctx context.Context, u string, h http.Header, body []byte) (int, []byte, error)
}

func (o *Ops) upixAPI() UPIXAPI {
	if o.UPIX != nil {
		return o.UPIX
	}
	return ncmsHTTP{c: &http.Client{Timeout: 60 * time.Second}}
}

type upixConn struct {
	base, version, sub, cm, tm string
	key                        []byte
	segs                       []string
	auto, post                 bool
}

func parseUPIXConn(c Connection) (upixConn, error) {
	mt := firstOf(c.Values["member_type"], "CM")
	m := strings.ToUpper(strings.TrimSpace(c.Values["member"]))
	x := upixConn{base: strings.TrimRight(c.Values["url"], "/"), version: firstOf(strings.Trim(c.Values["version"], "/ "), "v1"), sub: mt + "_" + m,
		cm: firstOf(strings.ToUpper(strings.TrimSpace(c.Values["cm"])), m), tm: firstOf(strings.ToUpper(strings.TrimSpace(c.Values["tm"])), m),
		auto: c.Values["auto"] != "Off", post: c.Values["post"] != "Off"}
	if len(m) != 5 {
		return x, invalid("the UPIX connection needs the 5 character member code")
	}
	k, err := base64.StdEncoding.DecodeString(strings.TrimSpace(c.Secrets["secret"]))
	if err != nil || len(k) != 32 {
		return x, invalid("the UPIX shared key is 32 bytes, base64 encoded, as NSE Clearing gives it")
	}
	x.key = k
	for _, s := range nclmSegments(firstOf(c.Values["segments"], "CM, FO")) {
		if !contains(upixSegments, s) {
			return x, invalid("the UPIX segments are %s; %s is not one of them", strings.Join(upixSegments, ", "), s)
		}
		x.segs = append(x.segs, s)
	}
	return x, nil
}

// upixJWT makes the token for one call.
func upixJWT(sub, aud, api string, key []byte, now time.Time) string {
	enc := base64.RawURLEncoding
	p, _ := json.Marshal(struct { // in the protocol's order
		Sub     string `json:"sub"`
		Aud     string `json:"aud"`
		Iat     int64  `json:"iat"`
		UpixAPI string `json:"upixApi"`
	}{sub, aud, now.UnixMilli(), api})
	in := enc.EncodeToString([]byte(`{"typ":"JWT","alg":"HS256"}`)) + "." + enc.EncodeToString(p)
	m := hmac.New(sha256.New, key)
	m.Write([]byte(in))
	return in + "." + enc.EncodeToString(m.Sum(nil))
}

// upixVerify checks a token NSE Clearing sent with a notification.
func upixVerify(tok, aud, apiSuffix string, key []byte, now time.Time) error {
	parts := strings.Split(strings.TrimSpace(tok), ".")
	if len(parts) != 3 {
		return invalid("not a JWT")
	}
	dec := func(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "=")) }
	hb, err1 := dec(parts[0])
	pb, err2 := dec(parts[1])
	sig, err3 := dec(parts[2])
	if err1 != nil || err2 != nil || err3 != nil {
		return invalid("the JWT is not base64url")
	}
	var h struct{ Typ, Alg string }
	var p struct {
		Sub, Aud string
		Iat      json.Number
		UpixAPI  string `json:"upixApi"`
	}
	if json.Unmarshal(hb, &h) != nil || json.Unmarshal(pb, &p) != nil || h.Typ != "JWT" || h.Alg != "HS256" {
		return invalid("the JWT header is not typ JWT, alg HS256")
	}
	m := hmac.New(sha256.New, key)
	m.Write([]byte(parts[0] + "." + parts[1]))
	if !hmac.Equal(m.Sum(nil), sig) {
		return invalid("the JWT signature does not match the shared key")
	}
	iat, err := p.Iat.Int64()
	if d := now.UnixMilli() - iat; err != nil || d > 60000 || d < -60000 {
		return invalid("the JWT was issued more than a minute from this server's time")
	}
	if p.Sub != "NCL" || p.Aud != aud || !strings.HasPrefix(p.UpixAPI, "POST_") || !strings.HasSuffix(p.UpixAPI, apiSuffix) {
		return invalid("the JWT is not NSE Clearing's for this member and notification")
	}
	return nil
}

// upixPost calls one of NSE Clearing's APIs: path is "/inquire/segblockbal",
// or "/rest/inquire/blockdebitlist" for those under /rest/.
func (o *Ops) upixPost(ctx context.Context, x upixConn, path string, body, out any, now time.Time) error {
	if x.base == "" {
		return fmt.Errorf("%w: NSE Clearing has not given the UPIX address yet; enter it in Connections → NSE Clearing UPIX", ErrState)
	}
	full := "/" + x.version + path
	if rest, ok := strings.CutPrefix(path, "/rest"); ok {
		full = "/rest/" + x.version + rest
	}
	b, _ := json.Marshal(body)
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("Accept", "application/json")
	h.Set("User-Agent", "/")
	h["Accept-Encoding"] = []string{""}
	h.Set("Authorization", "Bearer "+upixJWT(x.sub, "NCL", "POST_"+full, x.key, now))
	code, rb, err := o.upixAPI().Post(ctx, x.base+full, h, b)
	if err != nil {
		return fmt.Errorf("could not reach NSE Clearing's UPIX: %v", err)
	}
	if code != 200 {
		var e struct{ Messages []string }
		_ = json.Unmarshal(rb, &e)
		return fmt.Errorf("NSE Clearing's UPIX answered %d: %s", code, firstOf(strings.Join(e.Messages, "; "), trimBody(rb)))
	}
	if err := json.Unmarshal(rb, out); err != nil {
		return fmt.Errorf("NSE Clearing's UPIX reply is not what the protocol gives: %s", trimBody(rb))
	}
	return nil
}

// UPIXReg is a client's registration for the UPI block in a segment.
type UPIXReg struct {
	Seg      string `json:"seg"`
	UCC      string `json:"ucc"`
	Status   string `json:"status"` // R registered, D de-registered
	Exchange string `json:"exchange,omitempty"`
	Banks    int    `json:"banks"`
	RegTime  string `json:"reg_time,omitempty"`
	Last     string `json:"last_activity,omitempty"`
	At       string `json:"at"`
}

// UPIXBal is the amount still blocked for a client in a segment.
type UPIXBal struct {
	Seg    string    `json:"seg"`
	UCC    string    `json:"ucc"`
	Amount money.Dec `json:"amount"`
	At     string    `json:"at"`
}

// UPIXDebit is one debit NSE Clearing made on a client's block.
type UPIXDebit struct {
	CCRefID string       `json:"cc_ref_id"`
	Seg     string       `json:"seg"`
	UCC     string       `json:"ucc"`
	UMN     string       `json:"umn"`
	TMRef   string       `json:"tm_ref_id"`
	Amount  money.Amount `json:"amount"`
	Balance money.Amount `json:"balance"` // left on the block after it
	DrTime  string       `json:"dr_time"` // as NSE Clearing gives it
	Date    string       `json:"date"`    // YYYY-MM-DD
	Desc    string       `json:"desc,omitempty"`
	Posted  string       `json:"posted,omitempty"` // the ledger entry, once posted
	Problem string       `json:"problem,omitempty"`
}

// UPIXState is where the feed stands.
type UPIXState struct {
	RegDate   string   `json:"reg_date"`   // the day registrations were last read
	BalAt     string   `json:"bal_at"`     // when balances were last read
	DebitTo   string   `json:"debit_to"`   // debits are read up to this time (RFC 3339)
	Refresh   []string `json:"refresh"`    // seg|ucc whose balance changed by notification
	LastRun   string   `json:"last_run"`   // RFC 3339
	LastError string   `json:"last_error"` // the last run's first problem
	Notices   int      `json:"notices"`    // notifications received
}

// UPIXRun is what one check did.
type UPIXRun struct {
	Registered int      `json:"registered"`
	Balances   int      `json:"balances"`
	Debits     int      `json:"debits"`
	Posted     int      `json:"posted"`
	Problems   []string `json:"problems,omitempty"`
}

var upixMu sync.Mutex

func (o *Ops) upixConnection(ctx context.Context, tenant string) (upixConn, error) {
	cn, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connUPIX)
	if err != nil || cn.Status == "Not set" {
		return upixConn{}, fmt.Errorf("%w: set up NSE Clearing UPIX in Settings → Connections", ErrState)
	}
	return parseUPIXConn(cn)
}

// upixClientSeg: Ledgerline's segment name for UPIX's.
func upixClientSeg(s string) string {
	if s == "SL" {
		return "SLB"
	}
	return s
}

// UPIXSync reads registrations (once a day, or when full), balances and the
// debits since the last check.
func (o *Ops) UPIXSync(ctx context.Context, tenant, actor string, full bool) (UPIXRun, error) {
	var run UPIXRun
	if actor != SystemActor {
		if err := o.require(ctx, tenant, actor, "Checking UPI blocks with NSE Clearing", RoleMaker, RoleChecker, RoleRisk); err != nil {
			return run, err
		}
	}
	x, err := o.upixConnection(ctx, tenant)
	if err != nil {
		return run, err
	}
	upixMu.Lock()
	defer upixMu.Unlock()
	now := o.now()
	today := now.In(ist).Format("2006-01-02")
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cUPIXState, "state", UPIXState{})
	st.LastRun = now.Format(time.RFC3339)
	problem := func(err error) { run.Problems = append(run.Problems, err.Error()) }
	if full || st.RegDate != today {
		n, err := o.upixRegistrations(ctx, tenant, x, now)
		run.Registered = n
		if err != nil {
			problem(err)
		} else {
			st.RegDate = today
		}
	}
	if n, err := o.upixBalances(ctx, tenant, x, now, nil); err != nil {
		problem(err)
	} else {
		run.Balances, st.BalAt, st.Refresh = n, now.Format(time.RFC3339), nil
	}
	// From the last check, with a little overlap (debits are keyed by
	// reference), or from midnight.
	d := now.In(ist)
	from := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, ist)
	if t, err := time.Parse(time.RFC3339, st.DebitTo); err == nil && t.After(now.Add(-72*time.Hour)) {
		from = t.Add(-5 * time.Minute)
	}
	if d, p, err := o.upixDebits(ctx, tenant, x, from, now); err != nil {
		problem(err)
	} else {
		run.Debits, run.Posted, st.DebitTo = d, p, now.Format(time.RFC3339)
	}
	st.LastError = ""
	if len(run.Problems) > 0 {
		st.LastError = run.Problems[0]
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cUPIXState, "state", st, SystemActor)
	o.log(ctx, tenant, actor, "upix.checked", today, map[string]any{"registered": run.Registered, "balances": run.Balances, "debits": run.Debits, "posted": run.Posted})
	return run, nil
}

type upixKey struct {
	Seg  string `json:"seg"`
	CmCd string `json:"cmCd"`
	TmCd string `json:"tmCd"`
	UCC  string `json:"ucc"`
}

// upixRegistrations asks for every active client in each segment.
func (o *Ops) upixRegistrations(ctx context.Context, tenant string, x upixConn, now time.Time) (int, error) {
	cs, err := o.tradingClients(ctx, tenant)
	if err != nil {
		return 0, err
	}
	reg := map[string]bool{}
	for _, seg := range x.segs {
		var keys []upixKey
		for _, c := range cs {
			if c.Status == "Active" && (len(c.Segments) == 0 || contains(c.Segments, upixClientSeg(seg))) {
				keys = append(keys, upixKey{seg, x.cm, x.tm, strings.ToUpper(c.UCC)})
			}
		}
		for i := 0; i < len(keys); i += upixBatch {
			var out []struct {
				Seg, UCC, Status, RegStatus, RegTime, LastActivityTime, Exch string
				BankList                                                     []json.RawMessage
			}
			if err := o.upixPost(ctx, x, "/inquire/regdereg", keys[i:min(i+upixBatch, len(keys))], &out, now); err != nil {
				return len(reg), err
			}
			for _, r := range out {
				key := firstOf(r.Seg, seg) + "|" + strings.ToUpper(r.UCC)
				if nclmCheck(r.Status) != "0100" || (r.RegStatus != "R" && r.RegStatus != "D") {
					if _, _, err := docs.Get[UPIXReg](ctx, o.Docs, tenant, cUPIXReg, key); err == nil {
						_ = docs.Upsert(ctx, o.Docs, tenant, cUPIXReg, key, UPIXReg{Seg: firstOf(r.Seg, seg), UCC: strings.ToUpper(r.UCC), Status: "N", At: now.Format(time.RFC3339)}, SystemActor)
					}
					continue
				}
				_ = docs.Upsert(ctx, o.Docs, tenant, cUPIXReg, key, UPIXReg{Seg: firstOf(r.Seg, seg), UCC: strings.ToUpper(r.UCC), Status: r.RegStatus, Exchange: r.Exch,
					Banks: len(r.BankList), RegTime: r.RegTime, Last: r.LastActivityTime, At: now.Format(time.RFC3339)}, SystemActor)
				if r.RegStatus == "R" {
					reg[key] = true
				}
			}
		}
	}
	return len(reg), nil
}

// upixBalances reads the blocked amount of every registered client, or of
// only those given (seg|ucc).
func (o *Ops) upixBalances(ctx context.Context, tenant string, x upixConn, now time.Time, only []string) (int, error) {
	var keys []upixKey
	asked := map[string]bool{}
	if len(only) > 0 {
		for _, k := range only {
			seg, ucc, _ := strings.Cut(k, "|")
			keys = append(keys, upixKey{seg, x.cm, x.tm, ucc})
		}
	} else {
		regs, err := docs.List[UPIXReg](ctx, o.Docs, tenant, cUPIXReg, "")
		if err != nil {
			return 0, err
		}
		for _, r := range regs {
			if r.Status == "R" {
				keys = append(keys, upixKey{r.Seg, x.cm, x.tm, r.UCC})
				asked[r.Seg+"|"+r.UCC] = true
			}
		}
	}
	n := 0
	for i := 0; i < len(keys); i += upixBatch {
		var out []struct {
			Seg, UCC, Status string
			BalAmount        json.Number
		}
		if err := o.upixPost(ctx, x, "/inquire/segblockbal", keys[i:min(i+upixBatch, len(keys))], &out, now); err != nil {
			return n, err
		}
		for _, r := range out {
			amt := money.Dec{} // not found: nothing blocked
			if nclmCheck(r.Status) == "0100" {
				amt, _ = money.ParseDec(firstOf(r.BalAmount.String(), "0"))
			}
			_ = docs.Upsert(ctx, o.Docs, tenant, cUPIXBal, r.Seg+"|"+strings.ToUpper(r.UCC), UPIXBal{Seg: r.Seg, UCC: strings.ToUpper(r.UCC), Amount: amt, At: now.Format(time.RFC3339)}, SystemActor)
			n++
		}
	}
	if len(only) == 0 { // clients no longer registered have nothing blocked
		bs, _ := docs.List[UPIXBal](ctx, o.Docs, tenant, cUPIXBal, "")
		for _, b := range bs {
			if !asked[b.Seg+"|"+b.UCC] && b.Amount.Sign() != 0 {
				b.Amount, b.At = money.Dec{}, now.Format(time.RFC3339)
				_ = docs.Upsert(ctx, o.Docs, tenant, cUPIXBal, b.Seg+"|"+b.UCC, b, SystemActor)
			}
		}
	}
	return n, nil
}

type upixDebitMsg struct {
	Seg, UCC, TmRefID, Umn, CcRefID, DrTime, Desc string
	DrAmt, BalAmount                              json.Number
}

// upixDebits reads the debits made from from to to, and posts new ones.
func (o *Ops) upixDebits(ctx context.Context, tenant string, x upixConn, from, to time.Time) (int, int, error) {
	var refs []string
	if err := o.upixPost(ctx, x, "/rest/inquire/blockdebitlist", map[string]string{"fromTxnTime": strings.ToUpper(from.In(ist).Format(upixTime)), "toTxnTime": strings.ToUpper(to.In(ist).Format(upixTime))}, &refs, to); err != nil {
		return 0, 0, err
	}
	var want []string
	for _, r := range refs {
		if _, _, err := docs.Get[UPIXDebit](ctx, o.Docs, tenant, cUPIXDebit, r); err != nil {
			want = append(want, r)
		}
	}
	n, posted := 0, 0
	for i := 0; i < len(want); i += upixBatch {
		var out []upixDebitMsg
		if err := o.upixPost(ctx, x, "/rest/inquire/blockdebit", want[i:min(i+upixBatch, len(want))], &out, to); err != nil {
			return n, posted, err
		}
		for _, m := range out {
			ok, p := o.upixRecordDebit(ctx, tenant, x, m)
			if ok {
				n++
			}
			if p {
				posted++
			}
		}
	}
	return n, posted, nil
}

// upixRecordDebit keeps a debit once and posts it when switched on (new,
// posted).
func (o *Ops) upixRecordDebit(ctx context.Context, tenant string, x upixConn, m upixDebitMsg) (bool, bool) {
	if m.CcRefID == "" || m.UCC == "" {
		return false, false
	}
	if old, _, err := docs.Get[UPIXDebit](ctx, o.Docs, tenant, cUPIXDebit, m.CcRefID); err == nil && (old.Posted != "" || !x.post) {
		return false, false
	}
	amt, _ := money.ParseAmount(firstOf(m.DrAmt.String(), "0"))
	bal, _ := money.ParseAmount(firstOf(m.BalAmount.String(), "0"))
	d := UPIXDebit{CCRefID: m.CcRefID, Seg: m.Seg, UCC: strings.ToUpper(m.UCC), UMN: m.Umn, TMRef: m.TmRefID, Amount: amt, Balance: bal, DrTime: m.DrTime, Desc: m.Desc}
	if t, err := time.ParseInLocation(upixTime, strings.TrimSpace(m.DrTime), ist); err == nil {
		d.Date = t.Format("2006-01-02")
	} else {
		d.Date = o.now().In(ist).Format("2006-01-02")
	}
	posted := false
	if x.post && amt > 0 {
		if _, err := o.Client(ctx, tenant, d.UCC); err != nil {
			d.Problem = "client " + d.UCC + " is not in Ledgerline"
		} else {
			kind := ""
			if d.Seg == "CO" && o.sepCom(ctx, tenant) {
				kind = ledger.LedgerCommodity
			}
			e, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: tenant, Key: "UPIX:" + d.CCRefID, ValueDate: d.Date, CreatedBy: SystemActor,
				Narration: "UPI block debited by NSE Clearing · " + d.CCRefID, Source: ledger.SourceRef{Kind: "UPIX", ID: d.CCRefID},
				Postings: []ledger.Posting{{Account: ledger.ClearingAccount("NSE", upixClientSeg(d.Seg)), Debit: amt},
					{Account: ledger.ClientLedgerAccount(d.UCC, kind), Credit: amt, Memo: "UPI block " + d.UMN}}})
			if err != nil {
				d.Problem = err.Error()
			} else {
				d.Posted, posted = e.ID, true
			}
		}
		if d.Problem != "" {
			o.raiseWork(ctx, tenant, "upix", "debit:"+d.CCRefID, "UPI block debit not posted for "+d.UCC,
				fmt.Sprintf("NSE Clearing debited %s from %s's UPI block (%s, reference %s), but Ledgerline could not post it: %s. Post it to the client's ledger by hand.", d.Amount, d.UCC, d.Seg, d.CCRefID, d.Problem),
				d.UCC, RoleChecker, "ledger", d.Date)
		}
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cUPIXDebit, d.CCRefID, d, SystemActor)
	return true, posted
}

// UPIXHook takes one of NSE Clearing's notifications (kind is blockdebit,
// blockcreate, blockrelease, regdereg or transfer).
func (o *Ops) UPIXHook(ctx context.Context, tenant, version, kind string, auth string, body []byte) (int, error) {
	x, err := o.upixConnection(ctx, tenant)
	if err != nil {
		return 0, err
	}
	if version != x.version {
		return 0, invalid("this server takes UPIX notifications of version %s", x.version)
	}
	now := o.now()
	if err := upixVerify(strings.TrimPrefix(strings.TrimSpace(auth), "Bearer "), x.sub, "/notify/"+kind, x.key, now); err != nil {
		return 0, err
	}
	upixMu.Lock()
	defer upixMu.Unlock()
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cUPIXState, "state", UPIXState{})
	st.Notices++
	n := 0
	switch kind {
	case "blockdebit":
		var ms []upixDebitMsg
		if err := json.Unmarshal(body, &ms); err != nil {
			return 0, invalid("the notification is not a list of debits")
		}
		for _, m := range ms {
			if ok, _ := o.upixRecordDebit(ctx, tenant, x, m); ok {
				n++
			}
			st.Refresh = appendNew(st.Refresh, m.Seg+"|"+strings.ToUpper(m.UCC))
		}
	case "blockcreate", "blockrelease":
		var ms []struct{ Seg, UCC string }
		if err := json.Unmarshal(body, &ms); err != nil {
			return 0, invalid("the notification is not a list of blocks")
		}
		for _, m := range ms {
			st.Refresh = appendNew(st.Refresh, m.Seg+"|"+strings.ToUpper(m.UCC))
			n++
		}
	case "regdereg":
		var ms []struct {
			Seg, UCC, RegStatus, RegTime, LastActivityTime, Exch string
			BankList                                             []json.RawMessage
		}
		if err := json.Unmarshal(body, &ms); err != nil {
			return 0, invalid("the notification is not a list of registrations")
		}
		for _, m := range ms {
			if m.RegStatus != "R" && m.RegStatus != "D" {
				continue
			}
			_ = docs.Upsert(ctx, o.Docs, tenant, cUPIXReg, m.Seg+"|"+strings.ToUpper(m.UCC), UPIXReg{Seg: m.Seg, UCC: strings.ToUpper(m.UCC), Status: m.RegStatus, Exchange: m.Exch,
				Banks: len(m.BankList), RegTime: m.RegTime, Last: m.LastActivityTime, At: now.Format(time.RFC3339)}, SystemActor)
			st.Refresh = appendNew(st.Refresh, m.Seg+"|"+strings.ToUpper(m.UCC))
			n++
		}
	case "transfer":
		n = 1 // Ledgerline sends no transfers yet; balances follow
	default:
		return 0, invalid("no such UPIX notification: %s", kind)
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cUPIXState, "state", st, SystemActor)
	o.log(ctx, tenant, SystemActor, "upix.notified", kind, map[string]any{"records": n})
	return n, nil
}

func appendNew(xs []string, s string) []string {
	if contains(xs, s) {
		return xs
	}
	return append(xs, s)
}

// upixBlocked is each client's amount still blocked, added across segments,
// as NSE Clearing last reported it.
func (o *Ops) upixBlocked(ctx context.Context, tenant string) map[string]money.Dec {
	bs, err := docs.List[UPIXBal](ctx, o.Docs, tenant, cUPIXBal, "")
	if err != nil || len(bs) == 0 {
		return nil
	}
	out := map[string]money.Dec{}
	for _, b := range bs {
		out[b.UCC] = out[b.UCC].Add(b.Amount)
	}
	return out
}

// upiBlock is the money blocked for a client: NSE Clearing's figure when
// UPIX reports one, else the amount on the client record.
func upiBlock(c Client, ub map[string]money.Dec) money.Dec {
	if v, ok := ub[strings.ToUpper(c.UCC)]; ok {
		return v
	}
	return c.UPIBlock.Dec()
}

// UPIXView is the feed for the screen.
type UPIXView struct {
	Set        bool        `json:"set"`
	Address    bool        `json:"address"` // NSE Clearing's address entered
	State      UPIXState   `json:"state"`
	Registered int         `json:"registered"`
	Blocked    money.Dec   `json:"blocked"`
	Debits     []UPIXDebit `json:"debits"` // today's, newest first
}

// UPIXStatus shows the feed.
func (o *Ops) UPIXStatus(ctx context.Context, tenant string) UPIXView {
	v := UPIXView{Debits: []UPIXDebit{}}
	cn, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connUPIX)
	if err != nil || cn.Status == "Not set" {
		return v
	}
	v.Set, v.Address = true, cn.Values["url"] != ""
	v.State, _, _ = docs.GetOr(ctx, o.Docs, tenant, cUPIXState, "state", UPIXState{})
	regs, _ := docs.List[UPIXReg](ctx, o.Docs, tenant, cUPIXReg, "")
	for _, r := range regs {
		if r.Status == "R" {
			v.Registered++
		}
	}
	for _, b := range o.upixBlocked(ctx, tenant) {
		v.Blocked = v.Blocked.Add(b)
	}
	today := o.now().In(ist).Format("2006-01-02")
	ds, _ := docs.List[UPIXDebit](ctx, o.Docs, tenant, cUPIXDebit, "")
	for _, d := range ds {
		if d.Date == today {
			v.Debits = append(v.Debits, d)
		}
	}
	sort.SliceStable(v.Debits, func(i, j int) bool { return v.Debits[i].DrTime > v.Debits[j].DrTime })
	return v
}

// upixTick checks every 15 minutes from 09:00 to 23:45, and within a
// minute of a notification that changed a balance.
func (o *Ops) upixTick(ctx context.Context, tenant string, now time.Time) {
	cn, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connUPIX)
	if err != nil || cn.Status == "Not set" || cn.Values["auto"] == "Off" || cn.Values["url"] == "" {
		return
	}
	if hm := now.In(ist).Format("15:04"); hm < "09:00" || hm > "23:45" {
		return
	}
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cUPIXState, "state", UPIXState{})
	if len(st.Refresh) > 0 {
		if x, err := parseUPIXConn(cn); err == nil {
			upixMu.Lock()
			if _, err := o.upixBalances(ctx, tenant, x, now, st.Refresh); err == nil {
				st.Refresh = nil
				_ = docs.Upsert(ctx, o.Docs, tenant, cUPIXState, "state", st, SystemActor)
			}
			upixMu.Unlock()
		}
	}
	if t, err := time.Parse(time.RFC3339, st.LastRun); err == nil && now.Sub(t) < 15*time.Minute {
		return
	}
	_, _ = o.UPIXSync(ctx, tenant, SystemActor, false)
}
