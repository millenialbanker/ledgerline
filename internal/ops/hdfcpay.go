package ops

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/hdfcapi"
	"ledgerline/internal/money"
)

// ---- Payout batches to HDFC Bank by API (CBX batch payments) ----
//
// Instead of the ENet file (hdfcenet.go), an approved payout batch can go
// to HDFC Bank's "Corporate Banking Experience - Batch Payments v1" API,
// with the bank's JOSE High security (internal/hdfcapi). The API is the
// maker in the firm's CBX domain: the payments then wait in CBX for the
// firm's own verifier and authorisers, who approve them there as they do
// an uploaded file. The API cannot approve.
//
// Money: each client was debited when the batch was made (payments.go),
// so the money cannot be paid out twice while CBX waits. A payment is
// credited back only when the bank reports it rejected (or an RBI return);
// never because time has passed. One the bank has not settled after its
// inquiry schedule goes to the work queue for a person to check in CBX.
//
// Sending once: the file reference (6 digits) is stored before the batch
// is sent. If the bank's reply is lost, the batch number is looked up by
// that reference before anything may be sent again; the bank also refuses
// a second batch with the same reference on the same day.
//
// Inquiries follow the bank's published rhythm for bulk batches (Inquiry
// Frequency Interval documentation): a set of three 15 minutes after the
// send, 5 minutes apart (the bank answers "accepted" and "under process"
// before the real state); a second set 45 minutes later; then once at
// the end of the day for the next 3 days; at most 6 a day for a batch.
// Then the bank asks for the case to be raised with it.

const (
	connHDFCPay = "hdfc-cbx-pay"
	cHDFCSend   = "hdfc_api_send"

	hdfcEODInquiry = "19:30"
	hdfcMaxDaily   = 6
)

// Send states.
const (
	HSSending     = "Sending"
	HSUnclear     = "Unclear"      // no clear answer: being looked up by file reference
	HSAccepted    = "Accepted"     // with the bank; lines being inquired
	HSRefused     = "Refused"      // the bank did not take it: may be sent again
	HSNotReceived = "Not received" // the bank has no record: may be sent again
	HSDone        = "Done"         // every payment paid or failed
)

func init() {
	ConnProviders = append(ConnProviders, ConnProvider{ID: connHDFCPay, Name: "HDFC Bank CBX payments (API)", Group: "Money", Test: "token",
		Purpose: "Payout batches sent to HDFC Bank by API instead of the ENet file. The API is the maker in CBX: your verifier and authorisers still approve every payment in CBX. Results and UTRs come back by themselves; rejected payments are credited back to the client",
		Fields: []ConnField{
			{Key: "send", Label: "Send payout batches by API", Options: []string{"On", "Off"}},
			{Key: "mode", Label: "Environment", Options: []string{"Production", "UAT"}},
			{Key: "url", Label: "Gateway address (leave empty for the bank's own)", Optional: true, Placeholder: hdfcapi.ProdBase},
			{Key: "client_code", Label: "CBX client code for client payouts"},
			{Key: "group_id", Label: "CBX domain ID (group ID)"},
			{Key: "maker_id", Label: "CBX user ID of the API maker"},
			{Key: "scope", Label: "Scope (from the bank's developer portal)"},
			{Key: "app_type", Label: "App type (only if the bank gave one)", Optional: true},
			{Key: "api_key", Label: "Consumer key (client ID)", Secret: true},
			{Key: "secret", Label: "Consumer secret", Secret: true},
			{Key: "bank_certificate", Label: "HDFC Bank's certificate (PEM, from the developer portal)", Secret: true},
			{Key: "certificate", Label: "The firm's CA-signed certificate with its chain (PEM, as uploaded to the portal), for mutual TLS", Secret: true, Optional: true},
			{Key: "private_key", Label: "The firm's private key (PEM); leave empty if Ledgerline made the key", Secret: true, Optional: true},
			{Key: "notes", Label: "Notes (IP addresses whitelisted, RM contact, key renewal)", Optional: true},
		}})
}

// checkHDFCPayConn is run when the connection is saved.
func checkHDFCPayConn(c Connection) error {
	for _, k := range []string{"client_code", "group_id", "maker_id"} {
		v := c.Values[k]
		if len(v) > 50 || strings.ContainsAny(v, " ,\"'") {
			return invalid("the %s has spaces or other characters CBX does not use", strings.ReplaceAll(k, "_", " "))
		}
	}
	if len(c.Values["maker_id"]) > 32 {
		return invalid("the CBX maker user ID is at most 32 characters")
	}
	if _, err := hdfcapi.BankPublicKey(c.Secrets["bank_certificate"]); err != nil {
		return invalid("HDFC Bank's certificate: %v", err)
	}
	if k := c.Secrets["private_key"]; k != "" {
		key, err := hdfcapi.ParsePrivateKey(k)
		if err != nil {
			return invalid("%v", err)
		}
		if key.N.BitLen() < 2048 {
			return invalid("the bank needs an RSA key of at least 2048 bits")
		}
		if ch := c.Secrets["certificate"]; ch != "" {
			if _, err := hdfcapi.TLSCertificate(ch, key); err != nil {
				return invalid("%v", err)
			}
		}
	}
	return nil
}

// hdfcPayHTTP replaces the HTTP client (tests).
var hdfcPayHTTP *http.Client

// One client per firm and connection version, so the OAuth token is kept.
var hdfcPayClients sync.Map // tenant -> hdfcPayCached

type hdfcPayCached struct {
	stamp string
	cl    *hdfcapi.Client
}

type hdfcPayConn struct {
	cl                   *hdfcapi.Client
	client, group, maker string
	on                   bool
}

func (o *Ops) hdfcPay(ctx context.Context, tenant string) (hdfcPayConn, error) {
	c, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connHDFCPay)
	if err != nil || c.Status == "Not set" {
		return hdfcPayConn{}, invalid("set up HDFC Bank CBX payments (API) in Settings → Connections")
	}
	x := hdfcPayConn{client: c.Values["client_code"], group: c.Values["group_id"], maker: c.Values["maker_id"], on: c.Values["send"] == "On"}
	sum := sha256.Sum256([]byte(fmt.Sprint(c.Values, c.Secrets)))
	stamp := hex.EncodeToString(sum[:])
	if v, ok := hdfcPayClients.Load(tenant); ok && v.(hdfcPayCached).stamp == stamp {
		x.cl = v.(hdfcPayCached).cl
		return x, nil
	}
	if c.Secrets["private_key"] == "" {
		return x, invalid("the connection has no private key: make one (Make key) or paste the firm's key")
	}
	key, err := hdfcapi.ParsePrivateKey(c.Secrets["private_key"])
	if err != nil {
		return x, invalid("%v", err)
	}
	bank, err := hdfcapi.BankPublicKey(c.Secrets["bank_certificate"])
	if err != nil {
		return x, invalid("HDFC Bank's certificate: %v", err)
	}
	base := c.Values["url"]
	if base == "" {
		base = hdfcapi.ProdBase
		if c.Values["mode"] == "UAT" {
			base = hdfcapi.UATBase
		}
	}
	cfg := hdfcapi.Config{Base: base, APIKey: c.Secrets["api_key"], Secret: c.Secrets["secret"], Scope: c.Values["scope"], AppType: c.Values["app_type"],
		Key: key, BankKey: bank, HTTP: hdfcPayHTTP, Now: o.now}
	if ch := c.Secrets["certificate"]; ch != "" {
		tc, err := hdfcapi.TLSCertificate(ch, key)
		if err != nil {
			return x, invalid("%v", err)
		}
		cfg.TLSCerts = []tls.Certificate{tc}
	} else if c.Values["mode"] != "UAT" && hdfcPayHTTP == nil {
		return x, invalid("the bank's production gateway needs mutual TLS: add the firm's CA-signed certificate to the connection")
	}
	if x.cl, err = hdfcapi.New(cfg); err != nil {
		return x, invalid("%v", err)
	}
	hdfcPayClients.Store(tenant, hdfcPayCached{stamp: stamp, cl: x.cl})
	return x, nil
}

// hdfcPayTest gets an OAuth token: it checks the address, the mutual TLS,
// the consumer key, secret and scope, and changes nothing.
func (o *Ops) hdfcPayTest(ctx context.Context, tenant string) (string, string) {
	x, err := o.hdfcPay(ctx, tenant)
	if err != nil {
		return "Problem", strings.TrimPrefix(err.Error(), "invalid: ")
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := x.cl.Token(cctx); err != nil {
		return "Problem", err.Error()
	}
	mine, bank := x.cl.Kids()
	return "Working", fmt.Sprintf("token received; the firm's key ID %s, the bank's %s", mine, bank)
}

// ---- the send record ----

// HDFCSendLine is one payment's state at the bank.
type HDFCSendLine struct {
	Request string       `json:"request"`
	UCC     string       `json:"ucc"`
	Ref     string       `json:"ref"` // custrefno: the instruction reference
	Amount  money.Amount `json:"amount"`
	Type    string       `json:"type"`
	Status  string       `json:"status"` // Waiting, Paid, Failed
	UTR     string       `json:"utr,omitempty"`
	Note    string       `json:"note,omitempty"`
}

// HDFCCheck is one call made after the send.
type HDFCCheck struct {
	At     string `json:"at"`
	Kind   string `json:"kind"` // inquiry, batch number
	Result string `json:"result"`
	By     string `json:"by"`
}

// HDFCSend is a payout batch sent by API.
type HDFCSend struct {
	ID        string         `json:"id"` // the payout batch
	FileRef   string         `json:"file_ref"`
	Day       string         `json:"day"` // the day it was sent (IST)
	State     string         `json:"state"`
	BatchNum  string         `json:"batch_num,omitempty"`
	Message   string         `json:"message,omitempty"`
	SentAt    string         `json:"sent_at"`
	By        string         `json:"by"`
	Tries     int            `json:"tries"`
	Total     money.Amount   `json:"total"`
	Lines     []HDFCSendLine `json:"lines"`
	Checks    []HDFCCheck    `json:"checks"`
	Escalated bool           `json:"escalated,omitempty"`
}

func (s HDFCSend) open() bool {
	return s.State == HSUnclear || s.State == HSAccepted || s.State == HSSending
}

// resendable: the bank surely does not have it.
func (s HDFCSend) resendable() bool { return s.State == HSRefused || s.State == HSNotReceived }

// slashDate turns 2026-11-19 into 19/11/2026, the bank's date form.
func slashDate(iso string) string {
	if len(iso) != 10 {
		return iso
	}
	return iso[8:10] + "/" + iso[5:7] + "/" + iso[0:4]
}

// hdfcSendOf is a batch's API send, if any.
func (o *Ops) hdfcSendOf(ctx context.Context, tenant, batch string) (HDFCSend, bool) {
	s, _, err := docs.Get[HDFCSend](ctx, o.Docs, tenant, cHDFCSend, batch)
	return s, err == nil
}

// HDFCSendView is a batch's API send for the screen.
type HDFCSendView struct {
	Set         bool      `json:"set"` // connection saved and sending on
	Problem     string    `json:"problem,omitempty"`
	Send        *HDFCSend `json:"send,omitempty"`
	CanSend     bool      `json:"can_send"`
	Why         string    `json:"why,omitempty"` // why it cannot be sent
	NextCheck   string    `json:"next_check,omitempty"`
	ChecksToday int       `json:"checks_today"`
	Blocked     []string  `json:"blocked,omitempty"` // checks that stop the send
}

// HDFCPayView is the API state of a batch.
func (o *Ops) HDFCPayView(ctx context.Context, tenant, batch string) (HDFCSendView, error) {
	b, _, err := docs.Get[PayoutBatch](ctx, o.Docs, tenant, cPOBatch, batch)
	if err != nil {
		return HDFCSendView{}, err
	}
	v := HDFCSendView{}
	x, err := o.hdfcPay(ctx, tenant)
	switch {
	case err != nil:
		v.Problem = strings.TrimPrefix(err.Error(), "invalid: ")
	case !x.on:
		v.Problem = "sending by API is off in the connection"
	default:
		v.Set = true
	}
	if s, ok := o.hdfcSendOf(ctx, tenant, batch); ok {
		v.Send = &s
		v.ChecksToday = checksOn(s, o.now().In(ist).Format("2006-01-02"))
		if t, ok := o.hdfcWhenNext(s, o.now()); ok && s.State == HSAccepted {
			v.NextCheck = t.In(ist).Format("2006-01-02 15:04")
		}
	}
	if v.Set {
		// The send checks matter only while the batch can still be sent.
		if v.Why = o.hdfcSendBlock(ctx, tenant, b, v.Send); v.Why == "" {
			_, probs, err := o.hdfcPayments(ctx, tenant, b, false)
			if err != nil {
				return v, err
			}
			v.Blocked = probs
			v.CanSend = len(probs) == 0
		}
	}
	return v, nil
}

// hdfcSendBlock says why a batch cannot be sent now ("" if it can).
func (o *Ops) hdfcSendBlock(ctx context.Context, tenant string, b PayoutBatch, s *HDFCSend) string {
	if b.Status == "Confirmed" || b.Status == "Part confirmed" {
		return "the batch already has the bank's result"
	}
	if s != nil && !s.resendable() {
		return "already sent to HDFC (" + strings.ToLower(s.State) + ")"
	}
	if fs, _ := o.hdfcFiles(ctx, tenant, b.ID); len(fs) > 0 {
		for _, f := range fs {
			if f.Status == "Active" {
				return "the ENet file " + f.Name + " was made for this batch; cancel it first if it was not uploaded"
			}
		}
	}
	return ""
}

// hdfcPayments builds the API lines, with the ENet file's checks (the
// same bank, the same rules). With assign, instruction references are
// given (once per payment, kept); without, they are only checked.
func (o *Ops) hdfcPayments(ctx context.Context, tenant string, b PayoutBatch, assign bool) ([]hdfcapi.Payment, []string, error) {
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return nil, nil, err
	}
	refs := placeholderRefs(set, b)
	if assign {
		if b, err = o.assignInstrRefs(ctx, tenant, SystemActor, set, b.ID); err != nil {
			return nil, nil, err
		}
		refs = map[string]string{}
		for _, it := range b.Items {
			refs[it.Request] = it.InstrRef
		}
	}
	rows, _, probs := o.hdfcRows(ctx, tenant, set, b, refs)
	for _, it := range b.Items {
		// Only a payout from another firm account (an MCX-only account)
		// cannot go under this CBX client code. MCX money paid from the
		// usual payout account (one bank account for all exchanges) has no
		// DebitAccount, or the payout account itself, and goes in the same
		// batch as the equity payouts.
		if it.DebitAccount != "" && normAcctNo(it.DebitAccount) != normAcctNo(set.PayoutDebitAccount) {
			probs = append(probs, it.Request+" "+it.UCC+": paid from "+it.DebitAccount+", not the account of the API's CBX client code; make the ENet file for it")
		}
	}
	if len(b.Items) > hdfcapi.MaxBatch {
		probs = append(probs, fmt.Sprintf("the bank takes at most %d payments a batch; this one has %d", hdfcapi.MaxBatch, len(b.Items)))
	}
	day := slashDate(o.now().In(ist).Format("2006-01-02"))
	var out []hdfcapi.Payment
	for _, r := range rows {
		v := r.Values
		out = append(out, hdfcapi.Payment{Type: v["txn_type"], BeneCode: v["bene_code"], Account: v["account"], Amount: v["amount"], Name: v["name"],
			InstrRef: v["instruction_ref"], CustRef: v["instruction_ref"], Info1: v["customer_ref"], Date: day, IFSC: v["ifsc"], BankName: v["bank_name"], Branch: v["branch_name"], Email: v["email"]})
	}
	return out, probs, nil
}

// SendPayoutBatchHDFC sends an approved payout batch to HDFC Bank by API.
func (o *Ops) SendPayoutBatchHDFC(ctx context.Context, tenant, actor, batch string) (HDFCSend, error) {
	if err := o.require(ctx, tenant, actor, "Sending payouts to the bank", RoleMaker, RoleChecker); err != nil {
		return HDFCSend{}, err
	}
	x, err := o.hdfcPay(ctx, tenant)
	if err != nil {
		return HDFCSend{}, err
	}
	if !x.on {
		return HDFCSend{}, invalid("sending payout batches by API is off in Settings → Connections → HDFC Bank CBX payments")
	}
	b, _, err := docs.Get[PayoutBatch](ctx, o.Docs, tenant, cPOBatch, batch)
	if err != nil {
		return HDFCSend{}, err
	}
	prev, had := o.hdfcSendOf(ctx, tenant, batch)
	var pp *HDFCSend
	if had {
		pp = &prev
	}
	if why := o.hdfcSendBlock(ctx, tenant, b, pp); why != "" {
		return prev, fmt.Errorf("%w: %s", ErrState, why)
	}
	pays, probs, err := o.hdfcPayments(ctx, tenant, b, true)
	if err != nil {
		return HDFCSend{}, err
	}
	if len(probs) > 0 {
		return HDFCSend{}, &PayoutFileBlocked{Batch: b.ID, Problems: probs}
	}
	b, _, _ = docs.Get[PayoutBatch](ctx, o.Docs, tenant, cPOBatch, batch) // with the references
	now := o.now()
	today := now.In(ist).Format("2006-01-02")
	s := HDFCSend{ID: b.ID, Day: today, State: HSSending, SentAt: now.Format(time.RFC3339), By: actor, Tries: 1, Total: b.Total, Checks: []HDFCCheck{}}
	byRef := map[string]PayoutItem{}
	for _, it := range b.Items {
		byRef[it.InstrRef] = it
	}
	for _, p := range pays {
		it := byRef[p.CustRef]
		s.Lines = append(s.Lines, HDFCSendLine{Request: it.Request, UCC: it.UCC, Ref: p.CustRef, Amount: it.Amount, Type: p.Type, Status: "Waiting"})
	}
	// The file reference: kept from an earlier try the same day, so the
	// bank refuses a duplicate if the first did land after all.
	if had && prev.Day == today && prev.FileRef != "" {
		s.FileRef, s.Tries, s.Checks = prev.FileRef, prev.Tries+1, prev.Checks
	} else {
		n, err := o.next(ctx, tenant, "hdfc-api-fileref", actor)
		if err != nil {
			return HDFCSend{}, err
		}
		s.FileRef = fmt.Sprintf("%06d", n%1000000)
		if had {
			s.Tries, s.Checks = prev.Tries+1, prev.Checks
		}
	}
	// Claim the send before calling the bank: a second click, or another
	// person at the same time, finds it Sending.
	if had {
		if _, err := docs.Update(ctx, o.Docs, tenant, cHDFCSend, b.ID, actor, func(y *HDFCSend) error {
			if !y.resendable() {
				return fmt.Errorf("%w: already sent to HDFC (%s)", ErrState, strings.ToLower(y.State))
			}
			*y = s
			return nil
		}); err != nil {
			return prev, err
		}
	} else if _, err := docs.Put(ctx, o.Docs, tenant, cHDFCSend, b.ID, s, 0, actor); err != nil {
		if errors.Is(err, docs.ErrStale) {
			cur, _ := o.hdfcSendOf(ctx, tenant, b.ID)
			return cur, fmt.Errorf("%w: someone is sending this batch now", ErrState)
		}
		return HDFCSend{}, err
	}
	req := hdfcapi.BatchRequest{ClientCode: x.client, GroupID: x.group, Maker: x.maker, Count: fmt.Sprint(len(pays)), FileRef: s.FileRef, Payments: pays}
	cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	rep, err := x.cl.SendBatch(cctx, "", req)
	cancel()
	var ae *hdfcapi.APIError
	switch {
	case err == nil && rep.Accepted():
		s.State, s.BatchNum, s.Message = HSAccepted, string(rep.BatchNum), firstOf(string(rep.Text), "Accepted")
	case err == nil:
		s.State, s.Message = HSRefused, fmt.Sprintf("the bank did not accept the batch (%s %s)", rep.Code, rep.Text)
	case errors.As(err, &ae) && ae.Sure() && !strings.Contains(strings.ToLower(ae.Reason), "duplicate"):
		s.State, s.Message = HSRefused, err.Error()
	default:
		// No clear answer (no reply, a gateway failure, an answer that
		// could not be read, or "duplicate"): the bank may have it.
		s.State, s.Message = HSUnclear, err.Error()
	}
	s, err = o.saveSend(ctx, tenant, actor, s)
	if err != nil {
		return s, err
	}
	o.log(ctx, tenant, actor, "payout.hdfc_api_sent", b.ID, map[string]any{"state": s.State, "file_ref": s.FileRef, "batch_num": s.BatchNum, "lines": len(s.Lines), "total": s.Total.String(), "try": s.Tries, "message": s.Message})
	switch s.State {
	case HSUnclear:
		o.raiseWork(ctx, tenant, "payouts", "hdfcapi-unclear|"+b.ID, "Payout batch "+b.ID+": HDFC's answer not received",
			"The batch was sent to HDFC Bank by API but no clear answer came back ("+s.Message+"). Ledgerline is asking HDFC for it by its file reference "+s.FileRef+"; do not upload an ENet file for it. If HDFC has no record after 30 minutes, it can be sent again.", "", RoleChecker, "money", today)
		s, _ = o.hdfcLookup(ctx, tenant, actor, s, x)
	case HSRefused:
		o.raiseWork(ctx, tenant, "payouts", "hdfcapi-refused|"+b.ID, "Payout batch "+b.ID+" refused by HDFC",
			"HDFC Bank did not take the batch: "+s.Message+". Nothing was paid; the clients' payouts are still debited and waiting. Fix the cause and send it again, or make the ENet file.", "", RoleChecker, "money", today)
	}
	return s, nil
}

// saveSend stores a send's new state over whatever is stored.
func (o *Ops) saveSend(ctx context.Context, tenant, actor string, s HDFCSend) (HDFCSend, error) {
	if len(s.Checks) > 60 {
		s.Checks = s.Checks[len(s.Checks)-60:]
	}
	return docs.Update(ctx, o.Docs, tenant, cHDFCSend, s.ID, actor, func(y *HDFCSend) error { *y = s; return nil })
}

// hdfcLookup asks the bank for the batch number of an unclear send.
func (o *Ops) hdfcLookup(ctx context.Context, tenant, actor string, s HDFCSend, x hdfcPayConn) (HDFCSend, error) {
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	rep, err := x.cl.BatchNumber(cctx, hdfcapi.BatchNumberRequest{GroupID: x.group, Maker: x.maker, Date: slashDate(s.Day), FileRef: s.FileRef})
	cancel()
	now := o.now()
	ck := HDFCCheck{At: now.Format(time.RFC3339), Kind: "batch number", By: actor}
	switch {
	case err != nil:
		ck.Result = err.Error()
	case rep.BatchNum != "":
		ck.Result = "found " + string(rep.BatchNum)
		s.State, s.BatchNum, s.Message = HSAccepted, string(rep.BatchNum), "found by its file reference"
		o.closeWork(ctx, tenant, actor, "payouts", "hdfcapi-unclear|"+s.ID, "HDFC has the batch: "+s.BatchNum)
	default:
		ck.Result = firstOf(string(rep.Message), "no record")
		if t, e := time.Parse(time.RFC3339, s.SentAt); e == nil && now.Sub(t) >= 30*time.Minute {
			s.State, s.Message = HSNotReceived, "HDFC has no record of file reference "+s.FileRef+": "+ck.Result
			o.closeWork(ctx, tenant, actor, "payouts", "hdfcapi-unclear|"+s.ID, "HDFC has no record")
			o.raiseWork(ctx, tenant, "payouts", "hdfcapi-notreceived|"+s.ID, "Payout batch "+s.ID+" not received by HDFC",
				"HDFC Bank has no record of the batch (file reference "+s.FileRef+") 30 minutes after it was sent. Nothing was paid. Send it again from Receipts & payouts, or make the ENet file.", "", RoleChecker, "money", s.Day)
		}
	}
	s.Checks = append(s.Checks, ck)
	out, err := o.saveSend(ctx, tenant, actor, s)
	o.log(ctx, tenant, actor, "payout.hdfc_api_lookup", s.ID, map[string]any{"file_ref": s.FileRef, "result": ck.Result, "state": out.State})
	return out, err
}

// hdfcSlots are the inquiry times after a send, as the bank publishes.
func (o *Ops) hdfcSlots(s HDFCSend) []time.Time {
	t, err := time.Parse(time.RFC3339, s.SentAt)
	if err != nil {
		return nil
	}
	var out []time.Time
	for _, m := range []int{15, 20, 25, 70, 75, 80} {
		out = append(out, t.Add(time.Duration(m)*time.Minute))
	}
	day := t.In(ist)
	for d := 0; d <= 3; d++ {
		e, _ := time.ParseInLocation("2006-01-02 15:04", day.AddDate(0, 0, d).Format("2006-01-02")+" "+hdfcEODInquiry, ist)
		if e.After(out[len(out)-1]) {
			out = append(out, e)
		}
	}
	return out
}

func inquiries(s HDFCSend) int {
	n := 0
	for _, c := range s.Checks {
		if c.Kind == "inquiry" {
			n++
		}
	}
	return n
}

func checksOn(s HDFCSend, day string) int {
	n := 0
	for _, c := range s.Checks {
		if t, err := time.Parse(time.RFC3339, c.At); err == nil && c.Kind == "inquiry" && t.In(ist).Format("2006-01-02") == day {
			n++
		}
	}
	return n
}

// hdfcNextInquiry is when the next scheduled inquiry is due.
func (o *Ops) hdfcNextInquiry(s HDFCSend) (time.Time, bool) {
	slots := o.hdfcSlots(s)
	if k := inquiries(s); k < len(slots) {
		return slots[k], true
	}
	return time.Time{}, false
}

// hdfcWhenNext is when the tick will actually ask next: the scheduled
// slot, but after the two sets only at the end of a day, and not on a day
// whose 6 inquiries are used.
func (o *Ops) hdfcWhenNext(s HDFCSend, now time.Time) (time.Time, bool) {
	due, ok := o.hdfcNextInquiry(s)
	if !ok {
		return due, false
	}
	if due.Before(now) {
		due = now
	}
	for i := 0; i < 5; i++ {
		day := due.In(ist).Format("2006-01-02")
		eod, _ := time.ParseInLocation("2006-01-02 15:04", day+" "+hdfcEODInquiry, ist)
		if checksOn(s, day) >= hdfcMaxDaily {
			due, _ = time.ParseInLocation("2006-01-02 15:04", due.In(ist).AddDate(0, 0, 1).Format("2006-01-02")+" "+hdfcEODInquiry, ist)
			continue
		}
		if inquiries(s) >= 6 && due.Before(eod) {
			due = eod
		}
		break
	}
	return due, true
}

// CheckPayoutBatchHDFC asks the bank now (within its daily limit).
func (o *Ops) CheckPayoutBatchHDFC(ctx context.Context, tenant, actor, batch string) (HDFCSend, error) {
	if err := o.require(ctx, tenant, actor, "Checking payouts with the bank", RoleMaker, RoleChecker); err != nil {
		return HDFCSend{}, err
	}
	s, ok := o.hdfcSendOf(ctx, tenant, batch)
	if !ok {
		return HDFCSend{}, fmt.Errorf("%w: batch %s was not sent by API", ErrNotFound, batch)
	}
	x, err := o.hdfcPay(ctx, tenant)
	if err != nil {
		return s, err
	}
	switch s.State {
	case HSUnclear:
		return o.hdfcLookup(ctx, tenant, actor, s, x)
	case HSAccepted:
	default:
		return s, fmt.Errorf("%w: the batch is %s; nothing to ask the bank", ErrState, strings.ToLower(s.State))
	}
	now := o.now()
	if checksOn(s, now.In(ist).Format("2006-01-02")) >= hdfcMaxDaily {
		return s, fmt.Errorf("%w: HDFC allows %d inquiries a day for a batch; this one has had them. Check in CBX, or ask again tomorrow", ErrState, hdfcMaxDaily)
	}
	if n := len(s.Checks); n > 0 {
		if t, err := time.Parse(time.RFC3339, s.Checks[n-1].At); err == nil && now.Sub(t) < 5*time.Minute {
			return s, fmt.Errorf("%w: the bank was asked at %s; HDFC asks for 5 minutes between inquiries", ErrState, t.In(ist).Format("15:04"))
		}
	}
	return o.hdfcInquire(ctx, tenant, actor, s, x)
}

// hdfcInquire asks for the batch's state and applies what is final.
func (o *Ops) hdfcInquire(ctx context.Context, tenant, actor string, s HDFCSend, x hdfcPayConn) (HDFCSend, error) {
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	rep, err := x.cl.Inquire(cctx, hdfcapi.InquiryRequest{GroupID: x.group, Maker: x.maker, BatchNum: s.BatchNum, Date: slashDate(s.Day), FileRef: s.FileRef})
	cancel()
	o.syncSend(ctx, tenant, &s)
	ck := HDFCCheck{At: o.now().Format(time.RFC3339), Kind: "inquiry", By: actor}
	paid, failed := map[string]string{}, map[string]string{}
	if err != nil {
		ck.Result = err.Error()
	} else {
		ck.Result = firstOf(string(rep.Message), string(rep.Code))
		if len(rep.Lines) > 0 {
			ck.Result = fmt.Sprintf("%d lines · %s", len(rep.Lines), ck.Result)
		}
		byRef := map[string]int{}
		for i, l := range s.Lines {
			byRef[l.Ref] = i
		}
		for _, l := range rep.Lines {
			i, ok := byRef[strings.TrimSpace(string(l.CustRef))]
			if !ok || s.Lines[i].Status != "Waiting" {
				continue
			}
			ln := &s.Lines[i]
			if a, e := money.ParseAmount(string(l.Amount)); e == nil && a != ln.Amount {
				ln.Note = fmt.Sprintf("the bank reports ₹%s, not ₹%s: check in CBX", formatINR(a.Dec()), formatINR(ln.Amount.Dec()))
				continue
			}
			st, rbi := strings.ToUpper(string(l.Status)), strings.ToUpper(string(l.RBIStatus))
			switch {
			case st == "R":
				ln.Status, ln.Note = "Failed", firstOf(string(l.Reason), "rejected by the bank")
				failed[ln.Request] = "rejected by HDFC: " + ln.Note
			case rbi == "TXREJE":
				ln.Status, ln.Note = "Failed", firstOf(string(l.RBIReason), "returned by the beneficiary's bank")
				failed[ln.Request] = "returned (RBI): " + ln.Note
			case st == "C":
				utr := strings.TrimSpace(firstOf(string(l.UTR), string(l.BankRef)))
				if utr == "" {
					ln.Note = "complete at the bank; waiting for the UTR"
					continue
				}
				ln.Status, ln.UTR, ln.Note = "Paid", utr, map[string]string{"TXSETT": "settled", "TXDSETT": "deemed settled", "TXSIP": "settlement in progress"}[rbi]
				paid[ln.Request] = utr
			default:
				ln.Note = firstOf(string(l.Reason), "processing (with your CBX approvers, or at the bank)")
			}
		}
	}
	s.Checks = append(s.Checks, ck)
	if len(paid)+len(failed) > 0 {
		if _, e := o.settlePayouts(ctx, tenant, SystemActor, s.ID, paid, failed, false, "HDFC API"); e != nil {
			// The batch did not take the result: try again at the next
			// inquiry; the lines stay waiting.
			for i := range s.Lines {
				if _, ok := paid[s.Lines[i].Request]; ok {
					s.Lines[i].Status, s.Lines[i].UTR = "Waiting", ""
				}
				if _, ok := failed[s.Lines[i].Request]; ok {
					s.Lines[i].Status = "Waiting"
				}
			}
			ck.Result += " · not applied: " + e.Error()
			s.Checks[len(s.Checks)-1] = ck
		}
	}
	done := true
	for _, l := range s.Lines {
		if l.Status == "Waiting" {
			done = false
		}
	}
	if done {
		s.State, s.Message = HSDone, "every payment paid or failed"
		o.closeWork(ctx, tenant, actor, "payouts", "hdfcapi-stuck|"+s.ID, "all settled")
	}
	out, err := o.saveSend(ctx, tenant, actor, s)
	o.log(ctx, tenant, actor, "payout.hdfc_api_inquiry", s.ID, map[string]any{"result": ck.Result, "paid": len(paid), "failed": len(failed), "state": out.State})
	return out, err
}

// syncSend takes results entered on the batch by hand into the send; it
// reports whether every line is now final.
func (o *Ops) syncSend(ctx context.Context, tenant string, s *HDFCSend) bool {
	b, _, err := docs.Get[PayoutBatch](ctx, o.Docs, tenant, cPOBatch, s.ID)
	if err != nil {
		return false
	}
	st := map[string]PayoutItem{}
	for _, it := range b.Items {
		st[it.Request] = it
	}
	done := true
	for i := range s.Lines {
		l := &s.Lines[i]
		if it, ok := st[l.Request]; ok && l.Status == "Waiting" && (it.Status == "Paid" || it.Status == "Failed") {
			l.Status, l.UTR, l.Note = it.Status, it.UTR, "result entered on the batch"
		}
		if l.Status == "Waiting" {
			done = false
		}
	}
	return done
}

// hdfcPayTick runs the inquiries and lookups that are due.
func (o *Ops) hdfcPayTick(ctx context.Context, tenant string, now time.Time) {
	ss, err := docs.List[HDFCSend](ctx, o.Docs, tenant, cHDFCSend, "")
	if err != nil {
		return
	}
	var open []HDFCSend
	for _, s := range ss {
		if s.open() {
			open = append(open, s)
		}
	}
	if len(open) == 0 {
		return
	}
	x, err := o.hdfcPay(ctx, tenant)
	if err != nil {
		return
	}
	sort.Slice(open, func(i, j int) bool { return open[i].SentAt < open[j].SentAt })
	for _, s := range open {
		last := time.Time{}
		if n := len(s.Checks); n > 0 {
			last, _ = time.Parse(time.RFC3339, s.Checks[n-1].At)
		}
		switch s.State {
		case HSUnclear:
			if now.Sub(last) >= 10*time.Minute {
				_, _ = o.hdfcLookup(ctx, tenant, SystemActor, s, x)
			}
		case HSAccepted:
			if o.syncSend(ctx, tenant, &s) {
				s.State, s.Message = HSDone, "every payment paid or failed"
				_, _ = o.saveSend(ctx, tenant, SystemActor, s)
				o.closeWork(ctx, tenant, SystemActor, "payouts", "hdfcapi-stuck|"+s.ID, "all settled")
				continue
			}
			due, ok := o.hdfcNextInquiry(s)
			switch {
			case ok && !now.Before(due) && now.Sub(last) >= 5*time.Minute && checksOn(s, now.In(ist).Format("2006-01-02")) < hdfcMaxDaily &&
				// after the two sets: only at the end of a day
				(inquiries(s) < 6 || now.In(ist).Format("15:04") >= hdfcEODInquiry):
				_, _ = o.hdfcInquire(ctx, tenant, SystemActor, s, x)
			case !ok && !s.Escalated:
				var waiting []string
				for _, l := range s.Lines {
					if l.Status == "Waiting" {
						waiting = append(waiting, l.Request+" "+l.UCC+" ₹"+formatINR(l.Amount.Dec()))
					}
				}
				o.raiseWork(ctx, tenant, "payouts", "hdfcapi-stuck|"+s.ID, fmt.Sprintf("Payout batch %s: %d payments with no final state from HDFC", s.ID, len(waiting)),
					"HDFC's inquiry schedule is over and these payments have no final state: "+strings.Join(waiting, "; ")+". Check each in CBX (batch "+s.BatchNum+"); raise any not settled with the bank; then enter the result on the batch (UTR, or failed). The clients stay debited until then.", "", RoleChecker, "money", now.In(ist).Format("2006-01-02"))
				s.Escalated = true
				_, _ = o.saveSend(ctx, tenant, SystemActor, s)
			}
		}
	}
}

// HDFCPayMakeKey makes the firm's 2048-bit RSA key pair for the bank,
// keeps the private key in the connection's vault and returns a signing
// request for a certificate authority (the bank needs a CA-signed
// certificate) and a self-signed certificate for UAT.
func (o *Ops) HDFCPayMakeKey(ctx context.Context, tenant, actor string, sub NCLMSubject) (NCLMKey, error) {
	if err := o.require(ctx, tenant, actor, "Making the HDFC Bank API key", RoleAdmin); err != nil {
		return NCLMKey{}, err
	}
	cn, _, _ := docs.GetOr(ctx, o.Docs, tenant, cConn, connHDFCPay, Connection{Provider: connHDFCPay, Status: "Not set"})
	if cn.Status == "Not set" {
		return NCLMKey{}, invalid("save the connection first")
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
	tmpl := &x509.Certificate{SerialNumber: serial, Subject: name, NotBefore: now.Add(-time.Hour), NotAfter: now.AddDate(1, 0, 0), DNSNames: []string{sub.Name},
		KeyUsage: x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		return NCLMKey{}, err
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: name, DNSNames: []string{sub.Name}}, priv)
	if err != nil {
		return NCLMKey{}, err
	}
	if cn.Secrets == nil {
		cn.Secrets = map[string]string{}
	}
	cn.Secrets["private_key"] = string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)}))
	delete(cn.Secrets, "certificate") // the old certificate is for the old key
	cn.Status, cn.Problem = "Saved", "new key: upload its certificate to the bank's developer portal and paste the CA-signed certificate here"
	cn.UpdatedAt, cn.UpdatedBy = now.Format(time.RFC3339), actor
	if err := docs.Upsert(ctx, o.Docs, tenant, cConn, connHDFCPay, cn, actor); err != nil {
		return NCLMKey{}, err
	}
	kid, _ := hdfcapi.KID(&priv.PublicKey)
	o.log(ctx, tenant, actor, "hdfcpay.key_made", connHDFCPay, map[string]any{"kid": kid, "expires": tmpl.NotAfter.Format("2006-01-02")})
	code := strings.ToUpper(firstOf(cn.Values["client_code"], "FIRM"))
	return NCLMKey{FileName: "HDFC_" + code + ".pem", Expires: tmpl.NotAfter.In(ist).Format("2006-01-02"),
		Certificate: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		Request:     string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csr}))}, nil
}

// BankPayoutReturn is a credit that returned a paid payout.
const BankPayoutReturn = "Payout returned"

// payoutReturned finds the paid payout a bank credit returns: the credit's
// narration, reference or remitter names the payout's UTR (at least 10
// characters) and the amount is the payout's, paid in the last 15 days.
// The payout's entry is reversed (the money is the client's again), the
// payout becomes Failed and the client is told.
func (o *Ops) payoutReturned(ctx context.Context, tenant, actor string, t BankTxn) (BankTxn, bool, error) {
	text := strings.ToUpper(t.Narration + " " + t.Ref + " " + t.Remitter)
	if t.Amount <= 0 || strings.TrimSpace(text) == "" {
		return t, false, nil
	}
	prs, err := docs.List[PayoutRequest](ctx, o.Docs, tenant, cPayout, "")
	if err != nil {
		return t, false, err
	}
	from := o.now().In(ist).AddDate(0, 0, -15).Format("2006-01-02")
	var hit *PayoutRequest
	for i := range prs {
		p := &prs[i]
		u := strings.ToUpper(strings.TrimSpace(p.UTR))
		if p.Status != "Paid" || len(u) < 10 || p.Amount != t.Amount || p.Date < from || p.Entry == "" || !strings.Contains(text, u) {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(t.Ref), u) && !strings.Contains(strings.ToUpper(t.Narration), "RET") {
			continue // the payout's own debit line, not a return
		}
		if hit != nil {
			return t, false, nil // two payouts could be meant: left to a person
		}
		hit = p
	}
	if hit == nil {
		return t, false, nil
	}
	why := "returned by the bank after payment: " + firstOf(strings.TrimSpace(t.Narration), t.Ref)
	rev, _, err := o.App.Ledger.Reverse(ctx, tenant, hit.Entry, actor, "payout "+why, t.Date)
	if err != nil {
		return t, false, err
	}
	if _, err := docs.Update(ctx, o.Docs, tenant, cPayout, hit.ID, actor, func(x *PayoutRequest) error {
		x.Status, x.Reason = "Failed", why
		return nil
	}); err != nil {
		return t, false, err
	}
	if hit.Batch != "" {
		_, _ = docs.Update(ctx, o.Docs, tenant, cPOBatch, hit.Batch, actor, func(b *PayoutBatch) error {
			for i := range b.Items {
				if b.Items[i].Request == hit.ID {
					b.Items[i].Status = "Failed"
				}
			}
			if b.Results != nil {
				b.Results["Paid"]--
				b.Results["Failed"]++
			}
			return nil
		})
	}
	t.Status, t.UCC, t.EntryID = BankPayoutReturn, hit.UCC, rev.ID
	t.Note = fmt.Sprintf("Return of payout %s (UTR %s) · reversed, the money is the client's again", hit.ID, hit.UTR)
	o.notify(ctx, tenant, actor, "payout.failed", hit.UCC, map[string]string{"amount": formatINR(hit.Amount.Dec()), "reason": why})
	o.raiseWork(ctx, tenant, "payouts", "payout-returned|"+hit.ID, "Payout "+hit.ID+" to "+hit.UCC+" returned by the bank",
		fmt.Sprintf("₹%s paid on %s (UTR %s) came back: %s. It is reversed to the client's account. Check the client's bank details before paying again.", formatINR(hit.Amount.Dec()), fmtDay(hit.Date), hit.UTR, firstOf(strings.TrimSpace(t.Narration), t.Ref)), hit.UCC, RoleChecker, "money", t.Date)
	o.log(ctx, tenant, actor, "payout.returned", hit.ID, map[string]any{"credit": t.Ref, "utr": hit.UTR, "amount": hit.Amount.String(), "reversal": rev.ID})
	return t, true, nil
}
