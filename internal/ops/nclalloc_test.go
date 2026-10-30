package ops

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// fakeNCL stands in for NSE Clearing: it keeps every request and answers
// status inquiries with the codes the test sets per client.
type fakeNCL struct {
	mu      sync.Mutex
	sent    []map[string]any
	codes   map[string]string // cliCode -> result code
	failNow int               // fail the next N sends as unavailable
}

func (f *fakeNCL) Send(ctx context.Context, c NCLConn, body []byte) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNow > 0 {
		f.failNow--
		return "", "", ErrNCLTransient
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return "", "", err
	}
	f.sent = append(f.sent, m)
	return "Request accepted", "01010000", nil
}

func (f *fakeNCL) Inquire(ctx context.Context, c NCLConn, msgID string) (string, []NCLResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.sent {
		d := m["data"].(map[string]any)
		if d["msgId"] != msgID {
			continue
		}
		var out []NCLResult
		for _, r := range d["allocationRequest"].([]any) {
			x := r.(map[string]any)
			code := f.codes[x["cliCode"].(string)]
			if code == "" {
				code = "0100"
			}
			out = append(out, NCLResult{Segment: x["segment"].(string), CliCode: x["cliCode"].(string), AccType: x["accType"].(string), Amt: x["amt"], Action: x["action"].(string), ErrCd: "0105" + code})
		}
		return "01010000", out, nil
	}
	return "01010102", nil, nil
}

func (f *fakeNCL) last(t *testing.T) (string, string, []map[string]any) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) == 0 {
		t.Fatal("nothing sent")
	}
	d := f.sent[len(f.sent)-1]["data"].(map[string]any)
	var recs []map[string]any
	for _, r := range d["allocationRequest"].([]any) {
		recs = append(recs, r.(map[string]any))
	}
	return d["msgId"].(string), d["requestType"].(string), recs
}

func (f *fakeNCL) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.sent) }

func allocSetup(t *testing.T) (*Ops, context.Context, *fakeNCL) {
	t.Helper()
	o, ctx := newOps(t)
	f := &fakeNCL{codes: map[string]string{}}
	o.NCL = f
	// A fixed working-day clock: NSE Clearing's 20:00 cut-off must not
	// depend on when the tests run.
	o.Now = func() time.Time { return time.Date(2026, 10, 16, 11, 0, 0, 0, ist) }
	for _, u := range []string{"AL1", "AL2", "AL3"} {
		docs.Upsert(ctx, o.Docs, T, cClient, u, Client{UCC: u, Name: "Client " + u, Status: "Active", Email: u + "@x.example"}, "t")
	}
	set, _ := o.Settings(ctx, T)
	set.AllocAPILive, set.AllocAutoPush = true, true
	if err := o.SaveSettings(ctx, T, "admin", set); err == nil {
		t.Fatal("sending switched on without the key and secret")
	}
	if _, err := o.SaveConnection(ctx, T, "admin", connNCLAlloc, map[string]string{"member": "90311"}, map[string]string{"api_key": "k", "secret": "s"}); err != nil {
		t.Fatal(err)
	}
	if err := o.SaveSettings(ctx, T, "admin", set); err != nil {
		t.Fatal(err)
	}
	return o, ctx, f
}

func level(t *testing.T, o *Ops, ucc string) AllocLevel {
	t.Helper()
	l, _, err := docs.Get[AllocLevel](context.Background(), o.Docs, T, cAllocLevel, allocKey("CM", ucc))
	if err != nil {
		t.Fatalf("no level for %s", ucc)
	}
	return l
}

// Money in during the day is sent as the client's new total on top of the
// confirmed level, one request at a time per client; a partly accepted
// request stops further pushes until a checker confirms the level.
func TestAllocationMoneyIn(t *testing.T) {
	o, ctx, f := allocSetup(t)

	// No confirmed level yet: held, nothing sent.
	o.AllocationMoneyIn(ctx, T, "AL1", money.MustAmount("20000"), "RC-1")
	if f.count() != 0 {
		t.Fatal("sent without a confirmed level")
	}
	if p, _, _ := docs.Get[AllocPending](ctx, o.Docs, T, cAllocPending, allocKey("CM", "AL1")); !strings.Contains(p.Held, "No confirmed allocation") {
		t.Fatalf("pending %+v", p)
	}
	// The N-MASS file gives the level; the held money then goes as 50,000 + 20,000.
	o.levelsFromNMASS(ctx, T, "t", "2026-10-15", "CM", []map[string]string{{"CLIENT CODE": "AL1", "CASH(A)": "50000"}})
	if _, err := o.AllocationCheck(ctx, T, "priya.nair"); err == nil {
		t.Fatal("a maker ran the allocation check")
	}
	if _, err := o.AllocationCheck(ctx, T, "anita.rao"); err != nil {
		t.Fatal(err)
	}
	id, rt, recs := f.last(t)
	if !regexp.MustCompile(`^90311\d{8}\d{7}$`).MatchString(id) || rt != "I" || len(recs) != 1 || recs[0]["amt"].(float64) != 70000 || recs[0]["action"] != "U" || recs[0]["cliCode"] != "AL1" || recs[0]["tmCode"] != "90311" {
		t.Fatalf("request %s %s %v", id, rt, recs)
	}
	// More money while that request awaits its result: waits, not sent.
	o.AllocationMoneyIn(ctx, T, "AL1", money.MustAmount("5000"), "RC-2")
	if f.count() != 1 {
		t.Fatal("a second request went out before the first one's result")
	}
	// The result arrives: level 70,000; the waiting 5,000 goes as 75,000.
	o.AllocationCheck(ctx, T, SystemActor)
	if l := level(t, o, "AL1"); l.Amount.Cmp(money.MustDec("70000")) != 0 || !strings.HasPrefix(l.Source, "Accepted") {
		t.Fatalf("level %+v", l)
	}
	_, _, recs = f.last(t)
	if f.count() != 2 || recs[0]["amt"].(float64) != 75000 {
		t.Fatalf("second request %v", recs)
	}
	// NSE Clearing accepts only part of it: the level is unconfirmed and
	// money in is held until a checker confirms the figure.
	f.codes["AL1"] = "0104"
	o.AllocationCheck(ctx, T, SystemActor)
	if l := level(t, o, "AL1"); !l.Unconfirmed || l.Amount.Cmp(money.MustDec("70000")) != 0 {
		t.Fatalf("after partial %+v", l)
	}
	o.AllocationMoneyIn(ctx, T, "AL1", money.MustAmount("1000"), "RC-3")
	if f.count() != 2 {
		t.Fatal("sent on an unconfirmed level")
	}
	f.codes["AL1"] = ""
	if _, err := o.ConfirmAllocationLevel(ctx, T, "farhan.khan", "CM", "AL1", money.MustDec("72000"), ""); err == nil {
		t.Fatal("confirmed without saying where the figure comes from")
	}
	if _, err := o.ConfirmAllocationLevel(ctx, T, "farhan.khan", "CM", "AL1", money.MustDec("72000"), "N-MASS screen 11:40"); err != nil {
		t.Fatal(err)
	}
	if _, _, recs = f.last(t); f.count() != 3 || recs[0]["amt"].(float64) != 73000 {
		t.Fatalf("after confirmation %v", recs)
	}
	// A temporary failure is resent under a new message ID.
	o.AllocationCheck(ctx, T, SystemActor)
	f.failNow = 1
	o.AllocationMoneyIn(ctx, T, "AL1", money.MustAmount("500"), "RC-4")
	ms, _ := docs.List[AllocMessage](ctx, o.Docs, T, cAllocMsg, "")
	sup, ready := 0, 0
	for _, m := range ms {
		if m.Status == "Superseded" {
			sup++
		}
		if m.Status == "Ready" && m.Retry != "" {
			ready++
		}
	}
	if sup != 1 || ready != 1 {
		t.Fatalf("retry: %d superseded, %d ready", sup, ready)
	}
	o.AllocationCheck(ctx, T, SystemActor) // sends the retry
	if _, _, recs = f.last(t); recs[0]["amt"].(float64) != 73500 {
		t.Fatalf("retry %v", recs)
	}
}

// Start- and end-of-day batches: only changed clients, reductions first,
// prepared by one person and approved by another; end of day goes as E.
func TestAllocationBatch(t *testing.T) {
	o, ctx, f := allocSetup(t)
	// A client allocated before who has nothing now is brought down.
	o.levelsFromNMASS(ctx, T, "t", "2026-10-15", "CM", []map[string]string{{"CLIENT CODE": "AL2", "CASH(A)": "40000"}})
	ms, err := o.PrepareAllocationBatch(ctx, T, "priya.nair", "2026-10-16", AllocEOD)
	if err != nil {
		t.Fatal(err)
	}
	m := ms[0]
	if m.RequestType != "E" || m.Status != "Awaiting approval" || m.Records[0].Action != "D" {
		t.Fatalf("batch %+v", m)
	}
	for i := 1; i < len(m.Records); i++ {
		if m.Records[i].Action == "D" && m.Records[i-1].Action == "U" {
			t.Fatal("a reduction after an increase")
		}
	}
	if _, err := o.PrepareAllocationBatch(ctx, T, "priya.nair", "2026-10-16", AllocEOD); err == nil {
		t.Fatal("a second batch while one awaits approval")
	}
	if _, err := o.DecideAllocation(ctx, T, "priya.nair", m.ID, true, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("the preparer approved: %v", err)
	}
	if f.count() != 0 {
		t.Fatal("sent before approval")
	}
	got, err := o.DecideAllocation(ctx, T, "farhan.khan", m.ID, true, "")
	if err != nil || got.Status != "Sent" {
		t.Fatalf("approve %+v %v", got, err)
	}
	_, rt, recs := f.last(t)
	if rt != "E" || recs[0]["action"] != "D" || recs[0]["cliCode"] != "AL2" || recs[0]["amt"].(float64) != 0 {
		t.Fatalf("sent %s %v", rt, recs[:1])
	}
	o.AllocationCheck(ctx, T, SystemActor)
	if l := level(t, o, "AL2"); !l.Amount.IsZero() {
		t.Fatalf("AL2 level %+v", l)
	}
	v, _ := o.AllocationScreen(ctx, T, "2026-10-16")
	if !v.Live || v.Connection == "Not set" || len(v.Messages) == 0 {
		t.Fatalf("screen %+v", v)
	}
	if name, b, err := o.AllocationRequestFile(ctx, T, m.ID); err != nil || !strings.HasPrefix(name, "NSCCL_") || !strings.Contains(string(b), `"requestType":"E"`) {
		t.Fatalf("file %s %v", name, err)
	}
}

// A receipt approved by a checker allocates the money (cheques once cleared).
func TestReceiptAllocates(t *testing.T) {
	o, ctx, f := allocSetup(t)
	o.levelsFromNMASS(ctx, T, "t", "2026-10-15", "CM", []map[string]string{{"CLIENT CODE": "AL3", "CASH(A)": "1000"}})
	docs.Update(ctx, o.Docs, T, cClient, "AL3", "t", func(c *Client) error { c.Bank.Account = "123456789"; return nil })
	r, err := o.RecordReceipt(ctx, T, "priya.nair", Receipt{UCC: "AL3", Mode: "NEFT", Ref: "UTR1", Amount: money.MustAmount("2500"), Date: "2026-10-16"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.DecideReceipt(ctx, T, "farhan.khan", r.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	if _, _, recs := f.last(t); recs[0]["cliCode"] != "AL3" || recs[0]["amt"].(float64) != 3500 {
		t.Fatalf("sent %v", recs)
	}
}
