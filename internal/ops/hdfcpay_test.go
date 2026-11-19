package ops

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/hdfcapi/hdfctest"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// Synthetic clients and a fake bank only (hdfctest): no real codes,
// accounts or keys.

type hdfcAPIRig struct {
	o     *Ops
	ctx   context.Context
	bank  *hdfctest.Bank
	clock time.Time
}

func (r *hdfcAPIRig) at(d time.Duration) time.Time { r.clock = r.clock.Add(d); return r.clock }

func (r *hdfcAPIRig) tick(d time.Duration) { r.o.hdfcPayTick(r.ctx, T, r.at(d)) }

func hdfcAPISetup(t *testing.T) *hdfcAPIRig {
	t.Helper()
	o, ctx := hdfcSetup(t, nil)
	r := &hdfcAPIRig{o: o, ctx: ctx, clock: time.Date(2026, 11, 19, 11, 0, 0, 0, ist)}
	o.Now = func() time.Time { return r.clock }
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	r.bank = hdfctest.New(&key.PublicKey)
	t.Cleanup(r.bank.Close)
	old := hdfcPayHTTP
	hdfcPayHTTP = r.bank.Server.Client()
	t.Cleanup(func() { hdfcPayHTTP = old; hdfcPayClients.Delete(T) })
	priv := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	_, err = o.SaveConnection(ctx, T, "admin", connHDFCPay,
		map[string]string{"send": "On", "mode": "UAT", "url": r.bank.Base(), "client_code": "TESTCLI", "group_id": "TESTGRP", "maker_id": "APIMAKER", "scope": r.bank.Scope},
		map[string]string{"api_key": r.bank.APIKey, "secret": r.bank.Secret, "bank_certificate": r.bank.CertPEM, "private_key": priv})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func clientBal(t *testing.T, o *Ops, ctx context.Context, ucc string) money.Amount {
	t.Helper()
	b, err := o.App.Ledger.Balances(ctx, T, "")
	if err != nil {
		t.Fatal(err)
	}
	return b[ledger.ClientAccount(ucc)]
}

var hdfcAPIMix = []hdfcCase{
	{"ZQ01", "TEST CLIENT ONE", "SBIN0001234", "123456789012", "STATE BANK OF INDIA", "TEST BRANCH", "one@example.invalid", 1500000, false},
	{"ZQ02", "TEST CLIENT TWO", "ICIC0000002", "987654321098", "ICICI BANK", "", "two@example.invalid", 25000000, true},
	{"ZQ03", "TEST CLIENT THREE", "HDFC0000003", "50100000000003", "HDFC BANK", "", "three@example.invalid", 30000000, false},
}

func TestHDFCAPIPayoutBatch(t *testing.T) {
	r := hdfcAPISetup(t)
	o, ctx := r.o, r.ctx
	if v, err := o.TestConnection(ctx, T, "admin", connHDFCPay); err != nil || v.Status != "Working" || !strings.Contains(v.Problem, "token received") {
		t.Fatalf("connection test %+v %v", v, err)
	}
	b := hdfcBatch(t, o, ctx, "2026-11-19", hdfcAPIMix)
	before := map[string]money.Amount{}
	for _, c := range hdfcAPIMix {
		before[c.ucc] = clientBal(t, o, ctx, c.ucc)
	}
	v, err := o.HDFCPayView(ctx, T, b.ID)
	if err != nil || !v.Set || !v.CanSend || len(v.Blocked) > 0 {
		t.Fatalf("view %+v %v", v, err)
	}

	s, err := o.SendPayoutBatchHDFC(ctx, T, "priya.nair", b.ID)
	if err != nil || s.State != HSAccepted || s.BatchNum == "" || len(s.Lines) != 3 || s.FileRef != "000001" {
		t.Fatalf("send %+v %v", s, err)
	}
	bs := r.bank.Batches()
	if len(bs) != 1 || bs[0].Client != "TESTCLI" || bs[0].Group != "TESTGRP" || bs[0].Maker != "APIMAKER" || bs[0].Date != "19/11/2026" || len(bs[0].Lines) != 3 {
		t.Fatalf("at the bank %+v", bs)
	}
	got, _, _ := docsGetBatch(o, ctx, b.ID)
	for i, l := range bs[0].Lines {
		it := got.Items[i]
		if l.CustRef != it.InstrRef || l.InstrRef != it.InstrRef || l.Account != it.Account || l.Amount != it.Amount.String() || l.Info1 != it.UCC+"-PAY" {
			t.Fatalf("line %d %+v for %+v", i, l.Payment, it)
		}
	}
	if typ := []string{bs[0].Lines[0].Type, bs[0].Lines[1].Type, bs[0].Lines[2].Type}; strings.Join(typ, "") != "NRI" {
		t.Fatalf("types %v", typ)
	}
	// Sent once only, and no ENet file for it.
	if _, err := o.SendPayoutBatchHDFC(ctx, T, "priya.nair", b.ID); !errors.Is(err, ErrState) {
		t.Fatalf("sent twice: %v", err)
	}
	var blocked *PayoutFileBlocked
	if _, err := o.MakeHDFCPayoutFile(ctx, T, "priya.nair", b.ID, ""); !errors.As(err, &blocked) || !strings.Contains(strings.Join(blocked.Problems, ";"), "pay it twice") {
		t.Fatalf("ENet file allowed for a batch with the bank: %v", err)
	}

	// The bank's rhythm: nothing before 15 minutes; then the loop of 3.
	r.tick(10 * time.Minute)
	if n := inquiriesOf(t, o, ctx, b.ID); n != 0 {
		t.Fatalf("asked after 10 minutes (%d)", n)
	}
	r.tick(5 * time.Minute)
	r.tick(5 * time.Minute)
	r.tick(5 * time.Minute)
	if n := inquiriesOf(t, o, ctx, b.ID); n != 3 {
		t.Fatalf("%d inquiries in the first set", n)
	}
	// Asked again within 5 minutes by hand: refused.
	if _, err := o.CheckPayoutBatchHDFC(ctx, T, "priya.nair", b.ID); err == nil || !strings.Contains(err.Error(), "5 minutes") {
		t.Fatalf("manual check too soon: %v", err)
	}
	// Still processing at the bank (with the CBX approvers): nothing moves.
	if g, _, _ := docsGetBatch(o, ctx, b.ID); g.Status != "Sent to bank" || clientBal(t, o, ctx, "ZQ02") != before["ZQ02"] {
		t.Fatalf("moved while processing: %s", g.Status)
	}

	// One paid, one rejected, one still with the approvers.
	ref := func(i int) string { return got.Items[i].InstrRef }
	r.bank.Set(ref(0), "C", "SBINN52026111900001", "", "TXSETT")
	r.bank.Set(ref(1), "R", "", "Beneficiary account closed", "")
	r.tick(45 * time.Minute) // 11:70
	r.tick(5 * time.Minute)
	r.tick(5 * time.Minute)
	g, _, _ := docsGetBatch(o, ctx, b.ID)
	if g.Status != "Part confirmed" || g.Items[0].Status != "Paid" || g.Items[0].UTR != "SBINN52026111900001" || g.Items[1].Status != "Failed" || g.Items[2].Status != "Sent" {
		t.Fatalf("after the second set %s %+v", g.Status, g.Items)
	}
	if clientBal(t, o, ctx, "ZQ02") != before["ZQ02"]-hdfcAPIMix[1].amount || clientBal(t, o, ctx, "ZQ01") != before["ZQ01"] {
		t.Fatalf("balances: ZQ02 %v (was %v), ZQ01 %v (was %v)", clientBal(t, o, ctx, "ZQ02"), before["ZQ02"], clientBal(t, o, ctx, "ZQ01"), before["ZQ01"])
	}
	if p := payoutOf(t, o, ctx, g.Items[1].Request); p.Status != "Failed" || !strings.Contains(p.Reason, "account closed") {
		t.Fatalf("rejected payout %+v", p)
	}
	if dir := os.Getenv("HDFCPAY_DUMP"); dir != "" { // the screen's data, for a browser check
		v, _ := o.HDFCPayView(ctx, T, b.ID)
		bs, _ := o.PayoutBatches(ctx, T)
		raw, _ := json.Marshal(map[string]any{"view": v, "batches": bs})
		_ = os.WriteFile(dir+"/hdfcpay_view.json", raw, 0o600)
	}
	// 6 used today: the next is tomorrow evening.
	if v, _ := o.HDFCPayView(ctx, T, b.ID); v.NextCheck != "2026-11-20 19:30" || v.ChecksToday != 6 {
		t.Fatalf("next check %q, %d today", v.NextCheck, v.ChecksToday)
	}
	// A manual result now only needs the open line.
	if _, err := o.ConfirmPayouts(ctx, T, "priya.nair", b.ID, map[string]string{}, map[string]string{}); err == nil || !strings.Contains(err.Error(), g.Items[2].Request) {
		t.Fatalf("manual confirm without the open line: %v", err)
	}

	// The HDFC-to-HDFC transfer completes; the end-of-day inquiry finds it.
	r.bank.Set(ref(2), "C", "FT26323000123", "", "")
	r.tick(2 * time.Hour) // 14:15: nothing due until 19:30
	if n := inquiriesOf(t, o, ctx, b.ID); n != 6 {
		t.Fatalf("%d inquiries before the end of the day", n)
	}
	r.clock = time.Date(2026, 11, 19, 19, 30, 0, 0, ist)
	r.tick(0)
	s, _ = o.hdfcSendOf(ctx, T, b.ID)
	g, _, _ = docsGetBatch(o, ctx, b.ID)
	// The 7th inquiry of the day is not made: HDFC allows 6.
	if s.State != HSAccepted || inquiriesOf(t, o, ctx, b.ID) != 6 {
		t.Fatalf("asked a 7th time in a day: %s %d", s.State, inquiriesOf(t, o, ctx, b.ID))
	}
	r.clock = time.Date(2026, 11, 20, 19, 30, 0, 0, ist)
	r.tick(0) // the next day's end of day: the bank's loop starts again (1st of 3)
	r.clock = time.Date(2026, 11, 21, 19, 30, 0, 0, ist)
	r.tick(0)
	r.clock = time.Date(2026, 11, 22, 19, 30, 0, 0, ist)
	r.tick(0)
	s, _ = o.hdfcSendOf(ctx, T, b.ID)
	g, _, _ = docsGetBatch(o, ctx, b.ID)
	if s.State != HSDone || g.Status != "Confirmed" || g.Items[2].Status != "Paid" || g.Items[2].UTR != "FT26323000123" || g.Results["Paid"] != 2 || g.Results["Failed"] != 1 {
		t.Fatalf("end %s %s %+v %v", s.State, g.Status, g.Items, g.Results)
	}

	// A late NEFT return of the paid one: reversed, not a new receipt.
	paidBal := clientBal(t, o, ctx, "ZQ01")
	ret, err := o.matchCredit(ctx, T, "bank", BankTxn{ID: "RET1", Date: "2026-11-23", Ref: "N327260012345", Mode: "NEFT", Remitter: "TEST CLIENT ONE", Account: "123456789012",
		Amount: hdfcAPIMix[0].amount, Narration: "NEFT RETURN SBINN52026111900001 ACCOUNT FROZEN"}, nil)
	if err != nil || ret.Status != BankPayoutReturn || ret.UCC != "ZQ01" || ret.EntryID == "" {
		t.Fatalf("return %+v %v", ret, err)
	}
	if clientBal(t, o, ctx, "ZQ01") != paidBal-hdfcAPIMix[0].amount {
		t.Fatalf("ZQ01 after the return %v (was %v)", clientBal(t, o, ctx, "ZQ01"), paidBal)
	}
	if p := payoutOf(t, o, ctx, g.Items[0].Request); p.Status != "Failed" || !strings.Contains(p.Reason, "after payment") {
		t.Fatalf("returned payout %+v", p)
	}
	if !hasOpenWork(t, o, "payouts", "payout-returned|"+g.Items[0].Request) {
		t.Fatal("no work item for the return")
	}
	// A different amount, or no UTR: an ordinary credit.
	other, _ := o.matchCredit(ctx, T, "bank", BankTxn{ID: "RET2", Date: "2026-11-23", Ref: "N327260099999", Mode: "NEFT", Remitter: "SOMEONE", Account: "111111111111", Amount: 999, Narration: "NEFT RETURN FT26323000123"}, nil)
	if other.Status == BankPayoutReturn {
		t.Fatal("a credit of another amount taken as a return")
	}
}

func TestHDFCAPIUnclearAndRefused(t *testing.T) {
	r := hdfcAPISetup(t)
	o, ctx := r.o, r.ctx

	// The reply is lost but the bank has it: found by the file reference.
	b := hdfcBatch(t, o, ctx, "2026-11-19", hdfcAPIMix[:1])
	r.bank.LoseNextReply = true
	s, err := o.SendPayoutBatchHDFC(ctx, T, "priya.nair", b.ID)
	if err != nil || s.State != HSAccepted || s.BatchNum == "" || len(r.bank.Batches()) != 1 {
		t.Fatalf("lost reply %+v %v", s, err)
	}
	if hasOpenWork(t, o, "payouts", "hdfcapi-unclear|"+b.ID) {
		t.Fatal("work item left open once found")
	}

	// The request never arrived: unclear, then not received after 30 minutes,
	// then sent again with the same file reference.
	hdfcClients(t, o, ctx, hdfcAPIMix[1:2])
	hdfcApproved(t, o, ctx, "2026-11-19", hdfcAPIMix[1:2])
	b2, err := o.CreatePayoutBatch(ctx, T, "farhan.khan", "2026-11-19")
	if err != nil {
		t.Fatal(err)
	}
	r.bank.DropNext = true
	s, err = o.SendPayoutBatchHDFC(ctx, T, "priya.nair", b2.ID)
	if err != nil || s.State != HSUnclear || !hasOpenWork(t, o, "payouts", "hdfcapi-unclear|"+b2.ID) {
		t.Fatalf("dropped %+v %v", s, err)
	}
	if _, err := o.SendPayoutBatchHDFC(ctx, T, "priya.nair", b2.ID); !errors.Is(err, ErrState) {
		t.Fatalf("resent while unclear: %v", err)
	}
	if _, err := o.ConfirmPayouts(ctx, T, "priya.nair", b2.ID, map[string]string{}, map[string]string{"x": "y"}); err == nil || !strings.Contains(err.Error(), "looked up") {
		t.Fatalf("manual result while unclear: %v", err)
	}
	r.tick(10 * time.Minute)
	if s, _ = o.hdfcSendOf(ctx, T, b2.ID); s.State != HSUnclear {
		t.Fatalf("given up before 30 minutes: %s", s.State)
	}
	r.tick(10 * time.Minute)
	r.tick(10 * time.Minute)
	if s, _ = o.hdfcSendOf(ctx, T, b2.ID); s.State != HSNotReceived || !hasOpenWork(t, o, "payouts", "hdfcapi-notreceived|"+b2.ID) {
		t.Fatalf("after 30 minutes %s", s.State)
	}
	ref := s.FileRef
	s, err = o.SendPayoutBatchHDFC(ctx, T, "priya.nair", b2.ID)
	if err != nil || s.State != HSAccepted || s.FileRef != ref || s.Tries != 2 {
		t.Fatalf("sent again %+v %v", s, err)
	}

	// Refused by the bank: nothing paid, the clients stay debited, it can
	// be sent again.
	hdfcClients(t, o, ctx, hdfcAPIMix[2:3])
	hdfcApproved(t, o, ctx, "2026-11-19", hdfcAPIMix[2:3])
	b3, _ := o.CreatePayoutBatch(ctx, T, "farhan.khan", "2026-11-19")
	r.bank.RejectNext = "Invalid domain"
	s, err = o.SendPayoutBatchHDFC(ctx, T, "priya.nair", b3.ID)
	if err != nil || s.State != HSRefused || !strings.Contains(s.Message, "Invalid domain") || !hasOpenWork(t, o, "payouts", "hdfcapi-refused|"+b3.ID) {
		t.Fatalf("refused %+v %v", s, err)
	}
	if s, err = o.SendPayoutBatchHDFC(ctx, T, "priya.nair", b3.ID); err != nil || s.State != HSAccepted {
		t.Fatalf("sent again after refusal %+v %v", s, err)
	}
}

func TestHDFCAPIStuckGoesToPeople(t *testing.T) {
	r := hdfcAPISetup(t)
	o, ctx := r.o, r.ctx
	b := hdfcBatch(t, o, ctx, "2026-11-19", hdfcAPIMix[:1])
	bal := clientBal(t, o, ctx, "ZQ01")
	if _, err := o.SendPayoutBatchHDFC(ctx, T, "priya.nair", b.ID); err != nil {
		t.Fatal(err)
	}
	for d := 0; d < 6; d++ { // every minute for four days and a bit, in hops
		for h := 0; h < 24*12; h++ {
			r.tick(5 * time.Minute)
		}
	}
	s, _ := o.hdfcSendOf(ctx, T, b.ID)
	if s.State != HSAccepted || !s.Escalated || !hasOpenWork(t, o, "payouts", "hdfcapi-stuck|"+b.ID) {
		t.Fatalf("not escalated %+v", s)
	}
	// Never credited back on time alone.
	if g, _, _ := docsGetBatch(o, ctx, b.ID); g.Items[0].Status != "Sent" || clientBal(t, o, ctx, "ZQ01") != bal {
		t.Fatalf("credited back without the bank saying so: %+v", g.Items[0])
	}
	// After the first day's two sets, only at the end of a day.
	for _, c := range s.Checks[6:] {
		if tm, _ := time.Parse(time.RFC3339, c.At); c.Kind == "inquiry" && tm.In(ist).Format("15:04") < hdfcEODInquiry {
			t.Fatalf("an inquiry at %s, before the end of the day", tm.In(ist).Format("2006-01-02 15:04"))
		}
	}
	// At most 6 inquiries on any day.
	per := map[string]int{}
	for _, c := range s.Checks {
		if tm, err := time.Parse(time.RFC3339, c.At); err == nil && c.Kind == "inquiry" {
			per[tm.In(ist).Format("2006-01-02")]++
		}
	}
	for d, n := range per {
		if n > hdfcMaxDaily {
			t.Fatalf("%d inquiries on %s", n, d)
		}
	}
	// A person enters the result from CBX.
	if g, err := o.ConfirmPayouts(ctx, T, "priya.nair", b.ID, map[string]string{b.Items[0].Request: "SBINN52026111900777"}, nil); err != nil || g.Status != "Confirmed" {
		t.Fatalf("manual %+v %v", g, err)
	}
	// The API's record follows, and the escalation closes.
	r.tick(5 * time.Minute)
	if s, _ = o.hdfcSendOf(ctx, T, b.ID); s.State != HSDone || s.Lines[0].Status != "Paid" || s.Lines[0].UTR != "SBINN52026111900777" || hasOpenWork(t, o, "payouts", "hdfcapi-stuck|"+b.ID) {
		t.Fatalf("after the manual result %+v", s)
	}
}

func TestHDFCAPIConnectionChecks(t *testing.T) {
	r := hdfcAPISetup(t)
	o, ctx := r.o, r.ctx
	if _, err := o.SaveConnection(ctx, T, "admin", connHDFCPay, map[string]string{"send": "On", "mode": "UAT", "client_code": "TEST CLI", "group_id": "G", "maker_id": "M", "scope": "S"}, nil); err == nil || !strings.Contains(err.Error(), "spaces") {
		t.Fatalf("a client code with a space saved: %v", err)
	}
	if _, err := o.SaveConnection(ctx, T, "admin", connHDFCPay, map[string]string{"send": "On", "mode": "UAT", "client_code": "C", "group_id": "G", "maker_id": "M", "scope": "S"}, map[string]string{"bank_certificate": "not a certificate"}); err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("a bad bank certificate saved: %v", err)
	}
	// A new key: the connection asks for the certificate to be uploaded.
	k, err := o.HDFCPayMakeKey(ctx, T, "admin", NCLMSubject{Org: "Test Firm", State: "West Bengal", City: "Kolkata", Name: "backoffice.example.invalid"})
	if err != nil || !strings.Contains(k.Request, "CERTIFICATE REQUEST") || k.FileName != "HDFC_TESTCLI.pem" {
		t.Fatalf("key %+v %v", k, err)
	}
	// Sending off: nothing goes.
	c, _ := o.Connections(ctx, T, "admin")
	_ = c
	if _, err := o.SaveConnection(ctx, T, "admin", connHDFCPay, map[string]string{"send": "Off", "mode": "UAT", "url": r.bank.Base(), "client_code": "TESTCLI", "group_id": "TESTGRP", "maker_id": "APIMAKER", "scope": r.bank.Scope}, nil); err != nil {
		t.Fatal(err)
	}
	b := hdfcBatch(t, o, ctx, "2026-11-19", hdfcAPIMix[:1])
	if _, err := o.SendPayoutBatchHDFC(ctx, T, "priya.nair", b.ID); err == nil || !strings.Contains(err.Error(), "off") {
		t.Fatalf("sent with sending off: %v", err)
	}
	// A batch with an ENet file cannot also go by API.
	if _, err := o.SaveConnection(ctx, T, "admin", connHDFCPay, map[string]string{"send": "On", "mode": "UAT", "url": r.bank.Base(), "client_code": "TESTCLI", "group_id": "TESTGRP", "maker_id": "APIMAKER", "scope": r.bank.Scope}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := o.MakeHDFCPayoutFile(ctx, T, "priya.nair", b.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := o.SendPayoutBatchHDFC(ctx, T, "priya.nair", b.ID); !errors.Is(err, ErrState) || !strings.Contains(err.Error(), "ENet file") {
		t.Fatalf("sent by API after an ENet file: %v", err)
	}
}

func docsGetBatch(o *Ops, ctx context.Context, id string) (PayoutBatch, int64, error) {
	bs, err := o.PayoutBatches(ctx, T)
	for _, b := range bs {
		if b.ID == id {
			return b, 0, nil
		}
	}
	return PayoutBatch{}, 0, err
}

func payoutOf(t *testing.T, o *Ops, ctx context.Context, id string) PayoutRequest {
	t.Helper()
	ps, _ := docs.List[PayoutRequest](ctx, o.Docs, T, cPayout, "")
	for _, p := range ps {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("payout %s not found", id)
	return PayoutRequest{}
}

func inquiriesOf(t *testing.T, o *Ops, ctx context.Context, batch string) int {
	t.Helper()
	s, ok := o.hdfcSendOf(ctx, T, batch)
	if !ok {
		t.Fatal("no send")
	}
	return inquiries(s)
}
