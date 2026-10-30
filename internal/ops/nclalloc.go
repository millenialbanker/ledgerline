package ops

import (
	"bytes"
	"compress/gzip"
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
	"strings"
	"sync"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// ---- client collateral allocation to NSE Clearing (CIM Allocation API) ----
//
// NSE Clearing holds each client's collateral as the member allocates it.
// The amount in an allocation request is the client's FINAL allocation for
// that segment: it replaces the earlier level, it does not add to it
// (API documentation v1.3, circulars NCL/CMPT/51657, 52129, 54690). A
// request built from a stale level, or from a receipt amount alone, cuts
// the client's allocation and can cause short allocation. So:
//
//   - Ledgerline keeps each client's CONFIRMED level per segment: what NSE
//     Clearing accepted (status inquiry), or what its N-MASS file shows.
//   - Money that comes in during the day (receipt approved, cheque
//     cleared, online payment) is added to the confirmed level and sent as
//     an upward request. A "money in" request never lowers a level.
//   - One request at a time per client and segment: new money waits until
//     the earlier request's result is known.
//   - Reductions go only through the start- or end-of-day batch, prepared
//     by one person and approved by another; the end-of-day batch is sent
//     as requestType E (applied at the end-of-day release).
//   - A partly accepted request leaves the level unconfirmed: no further
//     money-in requests for the client until a checker confirms it (from
//     N-MASS or NSE Clearing's screen) or the next N-MASS file arrives.
//   - Every request and every reply is kept by message ID.
//
// The consumer key and secret NSE Clearing emails to the member live in
// Settings → Connections (NSE Clearing allocation API); sending is switched
// on in Settings. Until then requests are prepared and can be downloaded,
// but nothing is sent.

const (
	cAllocLevel   = "alloclevel"   // seg|ucc -> AllocLevel
	cAllocMsg     = "allocmsg"     // msgId -> AllocMessage
	cAllocPending = "allocpending" // seg|ucc -> AllocPending (money in, not yet requested)

	AllocMoneyIn = "Money in"
	AllocBOD     = "Start of day"
	AllocEOD     = "End of day"

	allocCutoff  = "20:00" // NSE Clearing's allocation cut-off
	allocMaxRecs = 1000    // records per request
	connNCLAlloc = "ncl-alloc"
	nclAllocBase = "https://www.connect2nse.com"
)

// AllocLevel is a client's allocation NSE Clearing holds for a segment.
type AllocLevel struct {
	Segment     string    `json:"segment"`
	UCC         string    `json:"ucc"`
	Amount      money.Dec `json:"amount"`
	AsOf        string    `json:"as_of"` // when NSE Clearing's figure was true
	Source      string    `json:"source"`
	Unconfirmed bool      `json:"unconfirmed,omitempty"` // a request was partly accepted
	Note        string    `json:"note,omitempty"`
}

// AllocPending is money in for a client not yet in a request.
type AllocPending struct {
	Segment string    `json:"segment"`
	UCC     string    `json:"ucc"`
	Amount  money.Dec `json:"amount"`
	Refs    []string  `json:"refs"`
	Held    string    `json:"held,omitempty"` // why it is not being sent
}

// AllocRecord is one client's line in a request.
type AllocRecord struct {
	Segment  string    `json:"segment"`
	UCC      string    `json:"ucc"`
	AccType  string    `json:"acc_type"` // C client, P proprietary
	Amount   money.Dec `json:"amount"`   // the final allocation requested
	Previous money.Dec `json:"previous"` // the confirmed level it was built on
	Added    money.Dec `json:"added,omitempty"`
	Action   string    `json:"action"` // U or D
	Code     string    `json:"code,omitempty"`
	Result   string    `json:"result,omitempty"`
	// Ref ties the record to what asked for it (a pay-out hold, payouthold.go).
	Ref string `json:"ref,omitempty"`
}

// AllocMessage is one request to NSE Clearing.
type AllocMessage struct {
	ID          string        `json:"id"` // msgId
	Date        string        `json:"date"`
	Kind        string        `json:"kind"`
	RequestType string        `json:"request_type"` // I immediate, E end of day
	Records     []AllocRecord `json:"records"`
	// Status: Awaiting approval, Ready (to send), Sent, Done, Failed,
	// Rejected (by the approver), Superseded (resent under a new ID).
	Status     string `json:"status"`
	Ack        string `json:"ack,omitempty"`
	Error      string `json:"error,omitempty"`
	Attempts   int    `json:"attempts,omitempty"`
	PreparedBy string `json:"prepared_by"`
	ApprovedBy string `json:"approved_by,omitempty"`
	Created    string `json:"created"`
	SentAt     string `json:"sent_at,omitempty"`
	DoneAt     string `json:"done_at,omitempty"`
	CheckedAt  string `json:"checked_at,omitempty"`
	Escalated  bool   `json:"escalated,omitempty"`
	Retry      string `json:"retry_of,omitempty"`
}

// ---- the transport ----

// NCLAllocAPI talks to NSE Clearing's allocation service.
type NCLAllocAPI interface {
	// Send posts an allocation request; ack is NSE Clearing's message-level
	// reply and code its message-level response code.
	Send(ctx context.Context, c NCLConn, body []byte) (ack, code string, err error)
	// Inquire asks for the results of a request: code is the message-level
	// code (0102 while in process) and records the per-record results.
	Inquire(ctx context.Context, c NCLConn, msgID string) (code string, records []NCLResult, err error)
}

// NCLConn is the member's API access.
type NCLConn struct {
	Base, Key, Secret string
	Tenant            string
}

// NCLResult is one record's result from a status inquiry.
type NCLResult struct {
	Segment string `json:"segment"`
	CliCode string `json:"cliCode"`
	TMCode  string `json:"tmCode"`
	AccType string `json:"accType"`
	Amt     any    `json:"amt"`
	Action  string `json:"action"`
	ErrCd   string `json:"errCd"`
}

// ErrNCLTransient marks failures worth retrying (service unavailable,
// system error, network).
var ErrNCLTransient = errors.New("NSE Clearing allocation service unavailable")

// nclHTTP is the real service, per the API documentation: OAuth client
// credentials with a nonce, gzip-compressed JSON over HTTPS.
type nclHTTP struct {
	mu     sync.Mutex
	tokens map[string]nclToken
	client *http.Client
}

type nclToken struct {
	value string
	until time.Time
}

var defaultNCL = &nclHTTP{tokens: map[string]nclToken{}, client: &http.Client{Timeout: 30 * time.Second}}

func nclNonce(now time.Time) string {
	n, _ := rand.Int(rand.Reader, big.NewInt(1000000))
	return base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%s%03d:%06d", now.In(ist).Format("02012006150405"), now.Nanosecond()/1e6, n.Int64())))
}

func gzipBytes(b []byte) []byte {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	w.Write(b)
	w.Close()
	return buf.Bytes()
}

func ungz(b []byte) []byte {
	if len(b) > 2 && b[0] == 0x1f && b[1] == 0x8b {
		if r, err := gzip.NewReader(bytes.NewReader(b)); err == nil {
			if x, err := io.ReadAll(r); err == nil {
				return x
			}
		}
	}
	return b
}

func (h *nclHTTP) token(ctx context.Context, c NCLConn, fresh bool) (string, error) {
	key := c.Tenant + "|" + c.Key
	h.mu.Lock()
	t, ok := h.tokens[key]
	h.mu.Unlock()
	if ok && !fresh && time.Now().Before(t.until) {
		return t.value, nil
	}
	req, _ := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(c.Base, "/")+"/coll-token", strings.NewReader(url.Values{"grant_type": {"client_credentials"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(c.Key+":"+c.Secret)))
	req.Header.Set("nonce", nclNonce(time.Now()))
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := h.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrNCLTransient, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	b = ungz(b)
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("NSE Clearing refused the sign-in (HTTP %d): check the consumer key and secret, and that this server's address is registered with NSE Clearing", resp.StatusCode)
	}
	var tr struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   any    `json:"expires_in"`
	}
	if err := json.Unmarshal(b, &tr); err != nil || tr.AccessToken == "" {
		return "", fmt.Errorf("%w: no token in NSE Clearing's reply", ErrNCLTransient)
	}
	secs := 3600.0
	switch v := tr.ExpiresIn.(type) {
	case float64:
		secs = v
	case string:
		fmt.Sscanf(strings.TrimSpace(v), "%f", &secs)
	}
	// Tokens also lapse after inactivity: renew well before the stated end.
	life := min(time.Duration(secs*float64(time.Second))/2, 20*time.Minute)
	h.mu.Lock()
	h.tokens[key] = nclToken{tr.AccessToken, time.Now().Add(life)}
	h.mu.Unlock()
	return tr.AccessToken, nil
}

func (h *nclHTTP) post(ctx context.Context, c NCLConn, path string, body []byte) ([]byte, error) {
	for attempt := 0; attempt < 2; attempt++ {
		tok, err := h.token(ctx, c, attempt > 0)
		if err != nil {
			return nil, err
		}
		req, _ := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(c.Base, "/")+path, bytes.NewReader(gzipBytes(body)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Content-Encoding", "gzip")
		req.Header.Set("Accept-Encoding", "gzip")
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("nonce", nclNonce(time.Now()))
		resp, err := h.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrNCLTransient, err)
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if resp.StatusCode == 401 && attempt == 0 {
			continue // token lapsed: sign in again
		}
		if resp.StatusCode >= 500 {
			return nil, fmt.Errorf("%w: HTTP %d", ErrNCLTransient, resp.StatusCode)
		}
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("NSE Clearing refused the request (HTTP %d)", resp.StatusCode)
		}
		return ungz(b), nil
	}
	return nil, fmt.Errorf("NSE Clearing refused the sign-in")
}

type nclReply struct {
	Status   string         `json:"status"`
	Messages map[string]any `json:"messages"`
	Data     struct {
		Response        string          `json:"response"`
		InquiryResponse json.RawMessage `json:"inquiryResponse"`
	} `json:"data"`
}

func (r nclReply) code() string {
	if c, ok := r.Messages["code"]; ok {
		return strings.TrimSpace(fmt.Sprint(c))
	}
	return ""
}

func (h *nclHTTP) Send(ctx context.Context, c NCLConn, body []byte) (string, string, error) {
	b, err := h.post(ctx, c, "/coll-allocation/allocation", body)
	if err != nil {
		return "", "", err
	}
	var r nclReply
	if err := json.Unmarshal(b, &r); err != nil {
		return "", "", fmt.Errorf("%w: unreadable reply", ErrNCLTransient)
	}
	return firstOf(r.Data.Response, r.Status), r.code(), nil
}

func (h *nclHTTP) Inquire(ctx context.Context, c NCLConn, msgID string) (string, []NCLResult, error) {
	body, _ := json.Marshal(map[string]any{"version": "1.0", "data": map[string]string{"msgId": msgID, "dataFormat": "JSON"}})
	b, err := h.post(ctx, c, "/coll-allocation/allocation-statusInquiry", body)
	if err != nil {
		return "", nil, err
	}
	var r nclReply
	if err := json.Unmarshal(b, &r); err != nil {
		return "", nil, fmt.Errorf("%w: unreadable reply", ErrNCLTransient)
	}
	var recs []NCLResult
	if len(r.Data.InquiryResponse) > 0 && r.Data.InquiryResponse[0] == '[' {
		_ = json.Unmarshal(r.Data.InquiryResponse, &recs)
	}
	return r.code(), recs, nil
}

func (o *Ops) nclAPI() NCLAllocAPI {
	if o.NCL != nil {
		return o.NCL
	}
	return defaultNCL
}

// nclConn reads the member's API access from Connections; ok is false
// when it is not set.
func (o *Ops) nclConn(ctx context.Context, tenant string) (NCLConn, bool) {
	c, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connNCLAlloc)
	if err != nil || c.Secrets["api_key"] == "" || c.Secrets["secret"] == "" {
		return NCLConn{}, false
	}
	o.log(ctx, tenant, SystemActor, "connection.used", connNCLAlloc, map[string]any{"for": "client allocation"})
	return NCLConn{Base: firstOf(c.Values["url"], nclAllocBase), Key: c.Secrets["api_key"], Secret: c.Secrets["secret"], Tenant: tenant}, true
}

// ---- result codes ----

// nclCodeText explains a result code (the last four digits of errCd).
var nclCodeText = map[string]string{
	"0000": "Submitted", "0100": "Accepted", "0102": "In process", "0103": "Rejected: the client's margin is in use",
	"0104": "Partly accepted: the client's margin is in use", "0122": "Rejected: no margin found", "0123": "Rejected: not enough to allocate in the firm's pool",
	"0201": "Rejected: a field is too short", "0202": "Rejected: a field is too long", "0203": "Rejected: out of range", "0204": "Rejected: a required field is missing",
	"0205": "Rejected: special characters", "0206": "Rejected: wrong format", "0207": "Rejected: below the minimum", "0208": "Rejected: above the maximum",
	"0209": "Rejected: invalid value", "0210": "Rejected: duplicate message ID", "0212": "Service unavailable or outside the time window",
	"0213": "NSE Clearing system error", "0214": "Rejected: too many records", "0217": "Rejected: wrong date", "0218": "Rejected: wrong segment",
	"0219": "Rejected: clearing member code not found", "0220": "Rejected: trading member not linked for the segment", "0221": "Rejected: CP code not linked",
	"0222": "Rejected: wrong account type", "0224": "Rejected: wrong direction (U/D)",
}

func nclResult(code string) (string, string) {
	c := strings.TrimSpace(code)
	if len(c) >= 8 {
		c = c[len(c)-4:]
	}
	if t, ok := nclCodeText[c]; ok {
		return c, t
	}
	return c, "Code " + c
}

// ---- levels and pending money ----

func allocKey(seg, ucc string) string { return seg + "|" + ucc }

// AllocationLevels lists every confirmed level.
func (o *Ops) allocLevels(ctx context.Context, tenant string) map[string]AllocLevel {
	ls, _ := docs.List[AllocLevel](ctx, o.Docs, tenant, cAllocLevel, "")
	out := map[string]AllocLevel{}
	for _, l := range ls {
		out[allocKey(l.Segment, l.UCC)] = l
	}
	return out
}

// setLevel records a confirmed level when it is newer than the one held.
func (o *Ops) setLevel(ctx context.Context, tenant, actor string, l AllocLevel, force bool) error {
	_, err := upd(ctx, o.Docs, tenant, cAllocLevel, allocKey(l.Segment, l.UCC), actor, &AllocLevel{Segment: l.Segment, UCC: l.UCC}, func(x *AllocLevel) error {
		if !force && x.AsOf != "" && x.AsOf > l.AsOf {
			return nil
		}
		*x = l
		return nil
	})
	return err
}

// levelsFromNMASS takes each client's cash allocated from an N-MASS file
// (end of day) as the confirmed level, unless a newer one is held.
func (o *Ops) levelsFromNMASS(ctx context.Context, tenant, actor, date, seg string, rows []map[string]string) {
	for _, r := range rows {
		ucc := strings.ToUpper(strings.TrimSpace(firstOf(r["CLIENT CODE"], r["Client Code"])))
		if ucc == "" {
			continue
		}
		cash := decOr0(firstOf(r["CASH(A)"], r["Cash allocated (b)"]))
		_ = o.setLevel(ctx, tenant, actor, AllocLevel{Segment: firstOf(seg, "CM"), UCC: ucc, Amount: cash, AsOf: date + "T23:59:00+05:30", Source: "N-MASS " + date}, false)
	}
}

// inFlight says whether a request for the client and segment awaits its result.
func inFlight(ms []AllocMessage, seg, ucc string) bool {
	for _, m := range ms {
		if m.Status != "Sent" && m.Status != "Ready" && m.Status != "Awaiting approval" {
			continue
		}
		for _, r := range m.Records {
			if r.Segment == seg && r.UCC == ucc {
				return true
			}
		}
	}
	return false
}

// AllocationMoneyIn records money a client brought in (receipt credited,
// cheque cleared, online payment) for their allocation, when the firm has
// switched on automatic allocation. It never sends a lower level.
func (o *Ops) AllocationMoneyIn(ctx context.Context, tenant, ucc string, amount money.Amount, ref string) {
	set, _ := o.Settings(ctx, tenant)
	if !set.AllocAutoPush || amount <= 0 {
		return
	}
	seg := firstOf(set.AllocPushSegment, "CM")
	_, err := upd(ctx, o.Docs, tenant, cAllocPending, allocKey(seg, ucc), SystemActor, &AllocPending{Segment: seg, UCC: ucc}, func(p *AllocPending) error {
		p.Amount = p.Amount.Add(amount.Dec())
		p.Refs = append(p.Refs, ref)
		return nil
	})
	if err != nil {
		return
	}
	o.log(ctx, tenant, SystemActor, "allocation.money_in", ucc, map[string]any{"segment": seg, "amount": amount.String(), "ref": ref})
	_, _ = o.allocDispatch(ctx, tenant, SystemActor)
}

// ---- building requests ----

func (o *Ops) allocCodes(set Settings) (cm, tm string, err error) {
	tm = strings.TrimSpace(set.MemberCodes["NSE"])
	cm = firstNonEmptyStr(set.MemberCodes["NCL"], tm)
	if tm == "" {
		return "", "", invalid("set the firm's NSE member code in Settings first")
	}
	return cm, tm, nil
}

// newAllocMsgID is <member code (≤5)><YYYYMMDD><7-digit sequence>.
func (o *Ops) newAllocMsgID(ctx context.Context, tenant, actor, cm, date string) (string, error) {
	n, err := o.next(ctx, tenant, "allocmsg-"+date, actor)
	if err != nil {
		return "", err
	}
	code := cm
	if len(code) > 5 {
		code = code[len(code)-5:]
	}
	return fmt.Sprintf("%s%s%07d", code, strings.ReplaceAll(date, "-", ""), n), nil
}

// allocBody is the request as the API expects it.
func allocBody(m AllocMessage, cm, tm string) []byte {
	d, _ := time.Parse("2006-01-02", m.Date)
	type rec struct {
		CurDate string  `json:"curDate"`
		Segment string  `json:"segment"`
		CMCode  string  `json:"cmCode"`
		TMCode  string  `json:"tmCode"`
		CPCode  string  `json:"cpCode"`
		CliCode string  `json:"cliCode"`
		AccType string  `json:"accType"`
		Amt     float64 `json:"amt"`
		Filler1 string  `json:"filler1"`
		Filler2 string  `json:"filler2"`
		Filler3 string  `json:"filler3"`
		Filler4 string  `json:"filler4"`
		Filler5 string  `json:"filler5"`
		Filler6 string  `json:"filler6"`
		Action  string  `json:"action"`
	}
	list := []rec{}
	for _, r := range m.Records {
		seg := r.Segment
		if seg == "COM" {
			seg = "CO"
		}
		cli := r.UCC
		if r.AccType == "P" {
			cli = ""
		}
		list = append(list, rec{CurDate: strings.ToUpper(d.Format("02-Jan-2006")), Segment: seg, CMCode: cm, TMCode: tm, CliCode: cli, AccType: r.AccType, Amt: r.Amount.Float64(), Action: r.Action})
	}
	b, _ := json.Marshal(map[string]any{"version": "1.0", "data": map[string]any{"msgId": m.ID, "requestType": m.RequestType, "allocationRequest": list}})
	return b
}

// allocDispatch turns pending money into money-in requests and sends what
// is ready, when sending is switched on. It returns how many were sent.
func (o *Ops) allocDispatch(ctx context.Context, tenant, actor string) (int, error) {
	set, _ := o.Settings(ctx, tenant)
	cm, tm, err := o.allocCodes(set)
	if err != nil {
		return 0, err
	}
	now := o.now()
	date := now.Format("2006-01-02")
	ms, _ := docs.List[AllocMessage](ctx, o.Docs, tenant, cAllocMsg, "")
	levels := o.allocLevels(ctx, tenant)
	pend, _ := docs.List[AllocPending](ctx, o.Docs, tenant, cAllocPending, "")
	afterCutoff := now.Format("15:04") >= allocCutoff
	var recs []AllocRecord
	for _, p := range pend {
		if p.Amount.Sign() <= 0 {
			continue
		}
		held := ""
		l, ok := levels[allocKey(p.Segment, p.UCC)]
		switch {
		case afterCutoff:
			held = "After NSE Clearing's 20:00 cut-off: goes in the next start-of-day batch"
		case !ok:
			held = "No confirmed allocation for this client yet: load the N-MASS file or send the start-of-day batch first"
		case l.Unconfirmed:
			held = "The last request was partly accepted: confirm the client's allocation first"
		case inFlight(ms, p.Segment, p.UCC):
			held = "Waiting for the result of the request already sent"
		}
		if held != "" {
			if held != p.Held {
				_, _ = upd(ctx, o.Docs, tenant, cAllocPending, allocKey(p.Segment, p.UCC), actor, &p, func(x *AllocPending) error { x.Held = held; return nil })
				if !strings.HasPrefix(held, "Waiting") {
					o.raiseWork(ctx, tenant, "allocation", "held|"+p.Segment+"|"+p.UCC, "Allocation held · "+p.UCC, fmt.Sprintf("₹%s came in for %s (%s) but was not sent to NSE Clearing: %s.", formatINR(p.Amount), p.UCC, p.Segment, held), p.UCC, RoleRisk, "allocation", "")
				}
			}
			continue
		}
		target := l.Amount.Add(p.Amount)
		recs = append(recs, AllocRecord{Segment: p.Segment, UCC: p.UCC, AccType: "C", Amount: target, Previous: l.Amount, Added: p.Amount, Action: "U"})
		_ = docs.Upsert(ctx, o.Docs, tenant, cAllocPending, allocKey(p.Segment, p.UCC), AllocPending{Segment: p.Segment, UCC: p.UCC, Amount: money.Dec{}}, actor)
	}
	for i := 0; i < len(recs); i += allocMaxRecs {
		part := recs[i:min(i+allocMaxRecs, len(recs))]
		id, err := o.newAllocMsgID(ctx, tenant, actor, cm, date)
		if err != nil {
			return 0, err
		}
		m := AllocMessage{ID: id, Date: date, Kind: AllocMoneyIn, RequestType: "I", Records: part, Status: "Ready", PreparedBy: SystemActor, Created: now.Format(time.RFC3339)}
		if err := docs.Upsert(ctx, o.Docs, tenant, cAllocMsg, id, m, actor); err != nil {
			return 0, err
		}
		ms = append(ms, m)
	}
	return o.allocSendReady(ctx, tenant, actor, set, cm, tm, ms)
}

// allocSendReady sends every Ready request, when sending is on and the
// connection is set.
func (o *Ops) allocSendReady(ctx context.Context, tenant, actor string, set Settings, cm, tm string, ms []AllocMessage) (int, error) {
	if !set.AllocAPILive {
		return 0, nil
	}
	conn, ok := o.nclConn(ctx, tenant)
	if !ok {
		return 0, nil
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].ID < ms[j].ID })
	n := 0
	for _, m := range ms {
		if m.Status != "Ready" {
			continue
		}
		if o.now().Format("15:04") >= allocCutoff && m.RequestType == "I" {
			continue
		}
		ack, code, err := o.nclAPI().Send(ctx, conn, allocBody(m, cm, tm))
		c, text := nclResult(code)
		m2, uerr := docs.Update(ctx, o.Docs, tenant, cAllocMsg, m.ID, actor, func(x *AllocMessage) error {
			x.Attempts++
			x.SentAt = o.now().Format(time.RFC3339)
			switch {
			case err != nil:
				x.Status, x.Error = "Failed", err.Error()
			case c == "0212" || c == "0213":
				x.Status, x.Error, x.Ack = "Failed", text, ack
			case c != "" && c != "0000" && c != "0102" && c != "0100":
				x.Status, x.Error, x.Ack = "Failed", text, ack
			default:
				x.Status, x.Ack = "Sent", ack
			}
			return nil
		})
		if uerr != nil {
			return n, uerr
		}
		o.log(ctx, tenant, actor, "allocation.sent", m.ID, map[string]any{"kind": m.Kind, "records": len(m.Records), "status": m2.Status, "code": c})
		if m2.Status == "Sent" {
			n++
		} else {
			o.allocRetryOrRaise(ctx, tenant, actor, m2, errors.Is(err, ErrNCLTransient) || c == "0212" || c == "0213")
		}
	}
	return n, nil
}

// allocRetryOrRaise resends a failed request under a new message ID (a
// message ID is never reused) when the failure is temporary, up to three
// times before the cut-off; otherwise it raises the failure for a person.
func (o *Ops) allocRetryOrRaise(ctx context.Context, tenant, actor string, m AllocMessage, transient bool) {
	if transient && m.Attempts < 3 && o.now().Format("15:04") < allocCutoff {
		set, _ := o.Settings(ctx, tenant)
		cm, _, err := o.allocCodes(set)
		if err == nil {
			if id, err := o.newAllocMsgID(ctx, tenant, actor, cm, o.now().Format("2006-01-02")); err == nil {
				n := m
				n.ID, n.Status, n.Error, n.Ack, n.SentAt, n.Retry = id, "Ready", "", "", "", m.ID
				if docs.Upsert(ctx, o.Docs, tenant, cAllocMsg, id, n, actor) == nil {
					_, _ = docs.Update(ctx, o.Docs, tenant, cAllocMsg, m.ID, actor, func(x *AllocMessage) error { x.Status = "Superseded"; return nil })
					return // the next dispatch sends it
				}
			}
		}
	}
	o.raiseWork(ctx, tenant, "allocation", "failed|"+m.ID, "Allocation request failed · "+m.ID, fmt.Sprintf("%s request %s (%d records) was not accepted by NSE Clearing: %s. Check the connection or NSE Clearing's notice, then send the allocation again from Clearing allocation.", m.Kind, m.ID, len(m.Records), firstOf(m.Error, "no reply")), "", RoleRisk, "allocation", "")
}

// ---- results ----

// AllocationCheck asks NSE Clearing for the results of requests sent and
// applies them, then sends what is ready. Run every few minutes by the
// scheduler and on demand.
func (o *Ops) AllocationCheck(ctx context.Context, tenant, actor string) (int, error) {
	if actor != SystemActor {
		if err := o.require(ctx, tenant, actor, "Allocating collateral", RoleChecker, RoleRisk); err != nil {
			return 0, err
		}
	}
	set, _ := o.Settings(ctx, tenant)
	ms, _ := docs.List[AllocMessage](ctx, o.Docs, tenant, cAllocMsg, "")
	conn, ok := o.nclConn(ctx, tenant)
	done := 0
	if ok && set.AllocAPILive {
		for _, m := range ms {
			if m.Status != "Sent" {
				continue
			}
			code, recs, err := o.nclAPI().Inquire(ctx, conn, m.ID)
			now := o.now()
			if err != nil || len(recs) == 0 {
				_, _ = docs.Update(ctx, o.Docs, tenant, cAllocMsg, m.ID, actor, func(x *AllocMessage) error { x.CheckedAt = now.Format(time.RFC3339); return nil })
				sent, _ := time.Parse(time.RFC3339, m.SentAt)
				if !m.Escalated && now.Sub(sent) > 5*time.Minute {
					_, _ = docs.Update(ctx, o.Docs, tenant, cAllocMsg, m.ID, actor, func(x *AllocMessage) error { x.Escalated = true; return nil })
					why := "still in process"
					if err != nil {
						why = err.Error()
					} else if c, t := nclResult(code); c != "" {
						why = t
					}
					o.raiseWork(ctx, tenant, "allocation", "noreply|"+m.ID, "No result from NSE Clearing · "+m.ID, fmt.Sprintf("Request %s was sent at %s and NSE Clearing has not given its result after 5 minutes (%s). NSE Clearing asks members to email collaterals_ops@nsccl.co.in in this case.", m.ID, sent.In(ist).Format("15:04"), why), "", RoleRisk, "allocation", "")
				}
				continue
			}
			if err := o.applyAllocResults(ctx, tenant, actor, m, recs); err != nil {
				return done, err
			}
			done++
		}
	}
	if _, err := o.allocDispatch(ctx, tenant, actor); err != nil && actor != SystemActor {
		return done, err
	}
	return done, nil
}

func (o *Ops) applyAllocResults(ctx context.Context, tenant, actor string, m AllocMessage, recs []NCLResult) error {
	now := o.now().Format(time.RFC3339)
	res := map[string]string{}
	for _, r := range recs {
		seg := strings.ToUpper(strings.TrimSpace(r.Segment))
		if seg == "CO" {
			seg = "COM"
		}
		k := seg + "|" + strings.ToUpper(strings.TrimSpace(r.CliCode)) + "|" + strings.TrimSpace(r.AccType)
		res[k] = r.ErrCd
	}
	var problems []string
	m2, err := docs.Update(ctx, o.Docs, tenant, cAllocMsg, m.ID, actor, func(x *AllocMessage) error {
		for i := range x.Records {
			r := &x.Records[i]
			cli := r.UCC
			if r.AccType == "P" {
				cli = ""
			}
			code, ok := res[r.Segment+"|"+cli+"|"+r.AccType]
			if !ok && len(recs) == len(x.Records) {
				code = recs[i].ErrCd
			}
			r.Code, r.Result = nclResult(code)
		}
		x.Status, x.DoneAt, x.CheckedAt = "Done", now, now
		return nil
	})
	if err != nil {
		return err
	}
	for _, r := range m2.Records {
		if r.AccType != "C" {
			continue
		}
		switch r.Code {
		case "0100":
			_ = o.setLevel(ctx, tenant, actor, AllocLevel{Segment: r.Segment, UCC: r.UCC, Amount: r.Amount, AsOf: now, Source: "Accepted · " + m.ID}, true)
			o.closeWork(ctx, tenant, actor, "allocation", "held|"+r.Segment+"|"+r.UCC, "allocation accepted")
		case "0104":
			_ = o.setLevel(ctx, tenant, actor, AllocLevel{Segment: r.Segment, UCC: r.UCC, Amount: r.Previous, AsOf: now, Source: "Partly accepted · " + m.ID, Unconfirmed: true,
				Note: "Asked for ₹" + formatINR(r.Amount) + "; NSE Clearing accepted part. Confirm the amount from N-MASS or NSE Clearing's screen."}, true)
			problems = append(problems, r.UCC+" partly accepted")
		default:
			if r.Added.Sign() > 0 {
				// Not allocated: the money is still the client's to allocate.
				_, _ = upd(ctx, o.Docs, tenant, cAllocPending, allocKey(r.Segment, r.UCC), actor, &AllocPending{Segment: r.Segment, UCC: r.UCC}, func(p *AllocPending) error {
					p.Amount = p.Amount.Add(r.Added)
					p.Held = r.Result
					return nil
				})
			}
			problems = append(problems, r.UCC+": "+r.Result)
		}
	}
	o.log(ctx, tenant, actor, "allocation.result", m.ID, map[string]any{"records": len(m2.Records), "problems": len(problems)})
	if m2.Kind == AllocPayoutHold || m2.Kind == AllocPayoutRelease {
		o.payoutHoldResults(ctx, tenant, actor, m2) // payouthold.go
	}
	if len(problems) > 0 {
		if len(problems) > 8 {
			problems = append(problems[:8], fmt.Sprintf("and %d more", len(problems)-8))
		}
		o.raiseWork(ctx, tenant, "allocation", "result|"+m.ID, "Allocation not fully accepted · "+m.ID, "NSE Clearing's result for "+m.ID+": "+strings.Join(problems, "; ")+". Review on Clearing allocation.", "", RoleRisk, "allocation", "")
	}
	return nil
}

// ---- start- and end-of-day batches ----

// PrepareAllocationBatch builds the start- or end-of-day request: each
// client's allocation brought to Ledgerline's figure for the segment
// (cash allocated), only where it differs from the confirmed level,
// reductions first. A second person approves it before it is sent.
func (o *Ops) PrepareAllocationBatch(ctx context.Context, tenant, actor, date, kind string) ([]AllocMessage, error) {
	if err := o.require(ctx, tenant, actor, "Allocating collateral", RoleMaker, RoleChecker, RoleRisk); err != nil {
		return nil, err
	}
	if kind != AllocBOD && kind != AllocEOD {
		return nil, invalid("choose the start-of-day or end-of-day batch")
	}
	if !validDate(date) {
		return nil, invalid("give the date")
	}
	set, _ := o.Settings(ctx, tenant)
	cm, _, err := o.allocCodes(set)
	if err != nil {
		return nil, err
	}
	ms, _ := docs.List[AllocMessage](ctx, o.Docs, tenant, cAllocMsg, "")
	for _, m := range ms {
		if m.Status == "Sent" || m.Status == "Ready" || m.Status == "Awaiting approval" {
			return nil, invalid("request %s (%s) is %s: wait for its result before preparing a batch", m.ID, m.Kind, strings.ToLower(m.Status))
		}
	}
	al, err := o.Allocation(ctx, tenant, date)
	if err != nil {
		return nil, err
	}
	levels := o.allocLevels(ctx, tenant)
	pend, _ := docs.List[AllocPending](ctx, o.Docs, tenant, cAllocPending, "")
	target := map[string]AllocationRow{}
	for _, a := range al {
		if a.Segment == "COM" {
			continue // MCXCCL allocates commodities
		}
		target[allocKey(a.Segment, a.UCC)] = a
	}
	keys := map[string]bool{}
	for k := range target {
		keys[k] = true
	}
	for k, l := range levels {
		if l.Amount.Sign() > 0 {
			keys[k] = true // allocated before, nothing now: brought down
		}
	}
	var down, up []AllocRecord
	for _, k := range sortedKeys(keys) {
		seg, ucc, _ := strings.Cut(k, "|")
		want := target[k].Cash
		have := levels[k].Amount
		if want.Cmp(have) == 0 {
			continue
		}
		r := AllocRecord{Segment: seg, UCC: ucc, AccType: "C", Amount: want, Previous: have, Action: "U"}
		if want.Cmp(have) < 0 {
			r.Action = "D"
			down = append(down, r)
		} else {
			up = append(up, r)
		}
	}
	recs := append(down, up...) // reductions first, as NSE Clearing asks
	if len(recs) == 0 {
		return nil, invalid("every client's allocation already matches: nothing to send")
	}
	rt := "I"
	if kind == AllocEOD {
		rt = "E" // applied at the end-of-day release
	}
	var out []AllocMessage
	for i := 0; i < len(recs); i += allocMaxRecs {
		id, err := o.newAllocMsgID(ctx, tenant, actor, cm, date)
		if err != nil {
			return out, err
		}
		m := AllocMessage{ID: id, Date: date, Kind: kind, RequestType: rt, Records: recs[i:min(i+allocMaxRecs, len(recs))], Status: "Awaiting approval", PreparedBy: actor, Created: o.now().Format(time.RFC3339)}
		if err := docs.Upsert(ctx, o.Docs, tenant, cAllocMsg, id, m, actor); err != nil {
			return out, err
		}
		out = append(out, m)
	}
	// The batch carries any money in that had not been sent.
	for _, p := range pend {
		if p.Amount.Sign() > 0 {
			_ = docs.Upsert(ctx, o.Docs, tenant, cAllocPending, allocKey(p.Segment, p.UCC), AllocPending{Segment: p.Segment, UCC: p.UCC}, actor)
		}
	}
	o.log(ctx, tenant, actor, "allocation.prepared", kind, map[string]any{"date": date, "records": len(recs), "down": len(down), "up": len(up)})
	o.raiseWork(ctx, tenant, "allocation", "approve|"+out[0].ID, "Approve the "+strings.ToLower(kind)+" allocation", fmt.Sprintf("%d clients' allocations to change (%d down, %d up), prepared by %s. A second person approves before it goes to NSE Clearing.", len(recs), len(down), len(up), actor), "", RoleChecker, "allocation", "")
	return out, nil
}

// DecideAllocation approves (and sends) or rejects a prepared batch. The
// person who prepared it cannot approve it.
func (o *Ops) DecideAllocation(ctx context.Context, tenant, actor, id string, approve bool, note string) (AllocMessage, error) {
	if err := o.require(ctx, tenant, actor, "Approving allocation", RoleChecker, RoleRisk); err != nil {
		return AllocMessage{}, err
	}
	m, _, err := docs.Get[AllocMessage](ctx, o.Docs, tenant, cAllocMsg, id)
	if err != nil {
		return m, err
	}
	if m.Status != "Awaiting approval" {
		return m, fmt.Errorf("%w: %s is %s", ErrState, id, strings.ToLower(m.Status))
	}
	if m.PreparedBy == actor {
		return m, fmt.Errorf("%w: the person who prepared an allocation batch cannot approve it", ErrForbidden)
	}
	if !approve && strings.TrimSpace(note) == "" {
		return m, invalid("give the reason for rejecting")
	}
	m, err = docs.Update(ctx, o.Docs, tenant, cAllocMsg, id, actor, func(x *AllocMessage) error {
		x.ApprovedBy, x.Status = actor, map[bool]string{true: "Ready", false: "Rejected"}[approve]
		if !approve {
			x.Error = strings.TrimSpace(note)
		}
		return nil
	})
	if err != nil {
		return m, err
	}
	o.log(ctx, tenant, actor, "allocation."+map[bool]string{true: "approved", false: "rejected"}[approve], id, map[string]any{"records": len(m.Records)})
	o.closeWork(ctx, tenant, actor, "allocation", "approve|"+id, map[bool]string{true: "approved", false: "rejected"}[approve])
	if approve {
		if _, err := o.allocDispatch(ctx, tenant, actor); err != nil {
			return m, err
		}
		m, _, _ = docs.Get[AllocMessage](ctx, o.Docs, tenant, cAllocMsg, id)
	}
	return m, nil
}

// ConfirmAllocationLevel records a client's allocation as NSE Clearing
// shows it (after a partly accepted request, or to start from a known
// figure). Needs a checker or the risk manager, with a note.
func (o *Ops) ConfirmAllocationLevel(ctx context.Context, tenant, actor, seg, ucc string, amount money.Dec, note string) (AllocLevel, error) {
	if err := o.require(ctx, tenant, actor, "Confirming allocation", RoleChecker, RoleRisk); err != nil {
		return AllocLevel{}, err
	}
	seg, ucc = strings.ToUpper(strings.TrimSpace(seg)), strings.ToUpper(strings.TrimSpace(ucc))
	if seg != "CM" && seg != "FO" && seg != "CD" && seg != "SLB" {
		return AllocLevel{}, invalid("segment must be CM, FO, CD or SLB")
	}
	if _, err := o.Client(ctx, tenant, ucc); err != nil {
		return AllocLevel{}, err
	}
	if amount.Sign() < 0 {
		return AllocLevel{}, invalid("the amount cannot be negative")
	}
	if strings.TrimSpace(note) == "" {
		return AllocLevel{}, invalid("say where the figure comes from (N-MASS screen, NSE Clearing's reply)")
	}
	l := AllocLevel{Segment: seg, UCC: ucc, Amount: amount, AsOf: o.now().Format(time.RFC3339), Source: "Confirmed by " + actor, Note: strings.TrimSpace(note)}
	if err := o.setLevel(ctx, tenant, actor, l, true); err != nil {
		return l, err
	}
	o.log(ctx, tenant, actor, "allocation.confirmed", ucc, map[string]any{"segment": seg, "amount": amount.Fixed(2)})
	_, _ = o.allocDispatch(ctx, tenant, actor)
	return l, nil
}

// ---- the screen ----

// AllocationView is the Clearing allocation screen.
type AllocationView struct {
	Date        string         `json:"date"`
	Live        bool           `json:"live"`
	AutoPush    bool           `json:"auto_push"`
	Segment     string         `json:"segment"`
	Connection  string         `json:"connection"` // Not set, Saved, Working, Problem
	Messages    []AllocMessage `json:"messages"`
	Levels      []AllocLevel   `json:"levels"`
	Pending     []AllocPending `json:"pending"`
	Unconfirmed int            `json:"unconfirmed"`
}

// Allocation view for a date: that day's requests, every level, money waiting.
func (o *Ops) AllocationScreen(ctx context.Context, tenant, date string) (AllocationView, error) {
	set, _ := o.Settings(ctx, tenant)
	v := AllocationView{Date: date, Live: set.AllocAPILive, AutoPush: set.AllocAutoPush, Segment: firstOf(set.AllocPushSegment, "CM"), Connection: "Not set", Messages: []AllocMessage{}, Levels: []AllocLevel{}, Pending: []AllocPending{}}
	if c, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connNCLAlloc); err == nil && c.Secrets["api_key"] != "" {
		v.Connection = firstOf(c.Status, "Saved")
	}
	ms, err := docs.List[AllocMessage](ctx, o.Docs, tenant, cAllocMsg, "")
	if err != nil {
		return v, err
	}
	for _, m := range ms {
		if m.Date == date || m.Status == "Sent" || m.Status == "Ready" || m.Status == "Awaiting approval" {
			v.Messages = append(v.Messages, m)
		}
	}
	sort.Slice(v.Messages, func(i, j int) bool { return v.Messages[i].ID > v.Messages[j].ID })
	ls, _ := docs.List[AllocLevel](ctx, o.Docs, tenant, cAllocLevel, "")
	for _, l := range ls {
		v.Levels = append(v.Levels, l)
		if l.Unconfirmed {
			v.Unconfirmed++
		}
	}
	ps, _ := docs.List[AllocPending](ctx, o.Docs, tenant, cAllocPending, "")
	for _, p := range ps {
		if p.Amount.Sign() > 0 {
			v.Pending = append(v.Pending, p)
		}
	}
	return v, nil
}

// AllocationRequestFile is a request as the JSON body NSE Clearing takes,
// for checking or for upload by other means.
func (o *Ops) AllocationRequestFile(ctx context.Context, tenant, id string) (string, []byte, error) {
	m, _, err := docs.Get[AllocMessage](ctx, o.Docs, tenant, cAllocMsg, id)
	if err != nil {
		return "", nil, err
	}
	set, _ := o.Settings(ctx, tenant)
	cm, tm, err := o.allocCodes(set)
	if err != nil {
		return "", nil, err
	}
	return "NSCCL_" + m.ID + ".json", allocBody(m, cm, tm), nil
}
