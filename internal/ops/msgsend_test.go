package ops

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/docs"
)

// fakeGateways answers as each SMS gateway and WhatsApp provider's API
// reference describes, and records what it was sent.
type gwCall struct {
	Path, Query, Body string
	Header            http.Header
}

func fakeGateways(t *testing.T) (*httptest.Server, *[]gwCall, *int) {
	var calls []gwCall
	fail := 0 // answer 503 this many times
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		calls = append(calls, gwCall{r.URL.Path, r.URL.RawQuery, string(b), r.Header.Clone()})
		if fail > 0 {
			fail--
			w.WriteHeader(503)
			return
		}
		q := r.URL.Query()
		switch {
		case r.URL.Path == "/api/sendhttp.php":
			if q.Get("authkey") != "KEY" {
				w.Write([]byte(`{"msgType":"error","msg":"Authentication failure"}`))
				return
			}
			w.Write([]byte("3763646c3058373530393831"))
		case strings.HasSuffix(r.URL.Path, "/messages") && strings.HasPrefix(r.URL.Path, "/v1/"):
			w.WriteHeader(202)
			w.Write([]byte(`{"id":"8f2c1a7e-kaleyra","type":"TXN","total_count":1}`))
		case r.URL.Path == "/GatewayAPI/rest":
			w.Write([]byte("success | " + q.Get("send_to") + " | 4357293472394-123"))
		case r.URL.Path == "/bulksms/bulksms":
			w.Write([]byte("1701|" + q.Get("destination") + "|a1b2c3"))
		case r.URL.Path == "/PNID/messages":
			if r.Header.Get("Authorization") != "Bearer KEY" {
				w.WriteHeader(401)
				return
			}
			var p map[string]any
			json.Unmarshal(b, &p)
			if p["type"] == "text" && strings.Contains(string(b), "OLD") {
				w.WriteHeader(400)
				w.Write([]byte(`{"error":{"message":"Re-engagement message","code":131047}}`))
				return
			}
			w.Write([]byte(`{"messaging_product":"whatsapp","contacts":[{"input":"919876543210","wa_id":"919876543210"}],"messages":[{"id":"wamid.HBgM"}]}`))
		case strings.HasPrefix(r.URL.Path, "/wa/api/v1/"):
			w.WriteHeader(202)
			w.Write([]byte(`{"status":"submitted","messageId":"ee4a68a0-gupshup"}`))
		case r.URL.Path == "/v1/public/message/":
			w.WriteHeader(201)
			w.Write([]byte(`{"result":true,"message":"Message created successfully","id":"interakt-1"}`))
		default:
			w.WriteHeader(404)
		}
	}))
	old := msgHTTP
	msgHTTP = srv.Client()
	t.Cleanup(func() { srv.Close(); msgHTTP = old })
	return srv, &calls, &fail
}

func TestSMSGateways(t *testing.T) {
	srv, calls, _ := fakeGateways(t)
	ctx := context.Background()
	conn := func(p, user string) Connection {
		return Connection{Values: map[string]string{"provider": p, "sender": "BHAIRV", "dlt_entity": "1101000000000001", "user": user, "url": srv.URL}, Secrets: map[string]string{"api_key": "KEY"}}
	}
	for _, tc := range []struct{ p, user, ref string }{
		{"MSG91", "", "3763646c3058373530393831"}, {"Kaleyra", "HXIN1234", "8f2c1a7e-kaleyra"}, {"Gupshup", "2000123456", "4357293472394-123"}, {"Route Mobile", "bhairav", "a1b2c3"},
	} {
		ref, err := sendSMS(ctx, conn(tc.p, tc.user), "98765 43210", "Dear client, Rs 500 received.", "1107166000000000011", "Transactional")
		if err != nil || ref != tc.ref {
			t.Errorf("%s: %q %v", tc.p, ref, err)
		}
	}
	// Each carries the number with 91, the DLT template and the sender.
	for _, c := range *calls {
		all := c.Query + c.Body
		if !strings.Contains(all, "919876543210") || !strings.Contains(all, "1107166000000000011") || !strings.Contains(all, "BHAIRV") {
			t.Errorf("%s: %s", c.Path, all)
		}
	}
	if (*calls)[1].Header.Get("api-key") != "KEY" || !strings.Contains((*calls)[1].Body, "type=TXN") {
		t.Errorf("kaleyra call %+v", (*calls)[1])
	}
	bad := conn("MSG91", "")
	bad.Secrets["api_key"] = "WRONG"
	if _, err := sendSMS(ctx, bad, "9876543210", "x", "1", "Transactional"); err == nil {
		t.Error("MSG91 error answer taken as sent")
	}
	other := Connection{Values: map[string]string{"provider": "Other", "url": srv.URL + "/bulksms/bulksms?destination={to}&message={text}&tempid={template}"}, Secrets: map[string]string{"api_key": "KEY"}}
	if _, err := sendSMS(ctx, other, "9876543210", "Hello there", "77", "Transactional"); err != nil {
		t.Error(err)
	}
	if last := (*calls)[len(*calls)-1]; !strings.Contains(last.Query, "message=Hello+there") || !strings.Contains(last.Query, "tempid=77") {
		t.Errorf("other: %s", last.Query)
	}
	if err := checkSMSConn(Connection{Values: map[string]string{"provider": "Other", "url": "https://gw.example/send"}}); err == nil {
		t.Error("Other without {to}")
	}
}

func TestWhatsAppProviders(t *testing.T) {
	srv, calls, _ := fakeGateways(t)
	ctx := context.Background()
	meta := Connection{Values: map[string]string{"provider": "Meta Cloud API", "phone_id": "PNID", "url": srv.URL}, Secrets: map[string]string{"api_key": "KEY"}}
	if ref, err := waTemplate(ctx, meta, "9876543210", "receipt_credited", []string{"Ravi", "25,000.00"}); err != nil || ref != "wamid.HBgM" {
		t.Fatalf("meta template %q %v", ref, err)
	}
	var p struct {
		To       string `json:"to"`
		Template struct {
			Name       string `json:"name"`
			Components []struct {
				Parameters []struct{ Text string } `json:"parameters"`
			} `json:"components"`
		} `json:"template"`
	}
	json.Unmarshal([]byte((*calls)[0].Body), &p)
	if p.To != "919876543210" || p.Template.Name != "receipt_credited" || p.Template.Components[0].Parameters[1].Text != "25,000.00" {
		t.Fatalf("meta body %s", (*calls)[0].Body)
	}
	if _, err := waText(ctx, meta, "9876543210", "OLD conversation"); err == nil || !strings.Contains(err.Error(), "24 hours") {
		t.Fatalf("re-engagement: %v", err)
	}
	gs := Connection{Values: map[string]string{"provider": "Gupshup", "number": "9123456789", "app": "BhairavApp", "url": srv.URL}, Secrets: map[string]string{"api_key": "KEY"}}
	if ref, err := waText(ctx, gs, "9876543210", "Your balance is Rs 500"); err != nil || ref != "ee4a68a0-gupshup" {
		t.Fatalf("gupshup %q %v", ref, err)
	}
	if c := (*calls)[len(*calls)-1]; c.Header.Get("apikey") != "KEY" || !strings.Contains(c.Body, "src.name=BhairavApp") || !strings.Contains(c.Body, "destination=919876543210") {
		t.Fatalf("gupshup call %+v", c)
	}
	it := Connection{Values: map[string]string{"provider": "Interakt", "url": srv.URL}, Secrets: map[string]string{"api_key": "KEY"}}
	if ref, err := waTemplate(ctx, it, "9876543210", "margin_call", []string{"BH1003"}); err != nil || ref != "interakt-1" {
		t.Fatalf("interakt %q %v", ref, err)
	}
	if _, err := waText(ctx, it, "9876543210", "hi"); err == nil {
		t.Fatal("Interakt free text claimed as sent")
	}
	if got := waParams("Dear {name}, {amount} received on {date}.", map[string]string{"name": "Ravi", "amount": "500", "date": "29 Oct"}); strings.Join(got, "|") != "Ravi|500|29 Oct" {
		t.Fatal(got)
	}
}

// anyClient is an active seeded client with a valid mobile number.
func anyClient(t *testing.T, o *Ops, ctx context.Context) Client {
	cs, _ := o.ClientMap(ctx, T)
	for _, u := range sortedKeys(cs) {
		if c := cs[u]; c.Status == "Active" && reMobile.MatchString(c.Mobile) {
			return c
		}
	}
	t.Fatal("no client with a mobile")
	return Client{}
}

func TestMessagesGoThroughProvider(t *testing.T) {
	o, ctx := setup(t)
	cl := anyClient(t, o, ctx).UCC
	srv, calls, fail := fakeGateways(t)
	at := time.Date(2026, 10, 29, 11, 0, 0, 0, ist)
	o.Now = func() time.Time { return at }
	var sms Template
	ts, _ := o.Templates(ctx, T)
	for _, x := range ts {
		if x.Channel == ChSMS && x.DLT != "" && x.Category != "Promotional" {
			sms = x
			break
		}
	}
	if sms.ID == "" {
		t.Fatal("no SMS template")
	}
	// A development firm with no gateway: shown as simulated.
	ms, err := o.TestMessage(ctx, T, "admin", sms.ID, cl)
	if err != nil || ms[0].Status != "Sent" || !strings.Contains(ms[0].Note, "simulated") {
		t.Fatalf("simulated: %+v %v", ms, err)
	}
	// A real firm with no gateway: never claimed as sent.
	m := Message{Channel: ChSMS, Status: "Sent"}
	o.notConnected(ctx, "OTHERFIRM", &m, "no SMS gateway connected")
	if m.Status != "Not sent" {
		t.Fatalf("real firm without gateway: %+v", m)
	}
	if _, err := o.SaveConnection(ctx, T, "admin", "sms", map[string]string{"provider": "MSG91", "sender": "BHAIRV", "dlt_entity": "1101000000000001", "otp_template": "1107000000000099", "url": srv.URL}, map[string]string{"api_key": "KEY"}); err != nil {
		t.Fatal(err)
	}
	ms, err = o.TestMessage(ctx, T, "admin", sms.ID, cl)
	if err != nil || ms[0].Status != "Sent" || ms[0].Ref != "3763646c3058373530393831" || ms[0].Provider != "MSG91" {
		t.Fatalf("sent: %+v %v", ms, err)
	}
	// The gateway down: queued, tried again by the scheduler a minute later.
	*fail = 1
	ms, _ = o.TestMessage(ctx, T, "admin", sms.ID, cl)
	if ms[0].Status != "Queued" {
		t.Fatalf("queued: %+v", ms[0])
	}
	o.retryMessages(ctx, T, at.Add(30*time.Second))
	if got, _, _ := docs.Get[Message](ctx, o.Docs, T, cMessage, ms[0].ID); got.Status != "Queued" {
		t.Fatal("retried too soon")
	}
	o.retryMessages(ctx, T, at.Add(2*time.Minute))
	if got, _, _ := docs.Get[Message](ctx, o.Docs, T, cMessage, ms[0].ID); got.Status != "Sent" || got.Attempts != 2 {
		t.Fatalf("after retry: %+v", got)
	}
	// A one-time code is sent but never kept in the log.
	before := len(*calls)
	o.sendDirectAs(ctx, T, "admin", ChSMS, "9876543210", "BH1003", "change.otp", "", "Your code is 482913.", "Your code is ••••••.")
	if c := (*calls)[before]; !strings.Contains(c.Query, "482913") || !strings.Contains(c.Query, "DLT_TE_ID=1107000000000099") {
		t.Fatalf("otp call %s", c.Query)
	}
	all, _ := docs.List[Message](ctx, o.Docs, T, cMessage, "")
	if last := all[len(all)-1]; strings.Contains(last.Body, "482913") || last.Status != "Sent" {
		t.Fatalf("otp kept: %+v", last)
	}
}

func TestMetaWebhook(t *testing.T) {
	o, ctx := setup(t)
	srv, calls, _ := fakeGateways(t)
	if _, err := o.SaveConnection(ctx, T, "admin", "whatsapp", map[string]string{"provider": "Meta Cloud API", "number": "9123456789", "phone_id": "PNID", "verify_token": "ledgerline-hook", "url": srv.URL}, map[string]string{"api_key": "KEY"}); err != nil {
		t.Fatal(err)
	}
	if c, err := o.WhatsAppVerify(ctx, T, "subscribe", "ledgerline-hook", "1158201444"); err != nil || c != "1158201444" {
		t.Fatalf("verify %q %v", c, err)
	}
	if _, err := o.WhatsAppVerify(ctx, T, "subscribe", "wrong", "1"); err == nil {
		t.Fatal("wrong verify token accepted")
	}
	mobile := anyClient(t, o, ctx).Mobile
	body := []byte(`{"object":"whatsapp_business_account","entry":[{"id":"1","changes":[{"field":"messages","value":{"messaging_product":"whatsapp","messages":[{"from":"91` + mobile + `","id":"wamid.in","timestamp":"1730190000","type":"text","text":{"body":"HELP"}}]}}]}]}`)
	set, _ := o.Settings(ctx, T)
	mac := hmac.New(sha256.New, []byte(set.WhatsAppSecret))
	mac.Write(body)
	if _, err := o.WhatsAppWebhook(ctx, T, body, "sha256=00"); err == nil {
		t.Fatal("bad Meta signature accepted")
	}
	reply, err := o.WhatsAppWebhook(ctx, T, body, "sha256="+hex.EncodeToString(mac.Sum(nil)))
	if err != nil || reply == "" {
		t.Fatalf("webhook %q %v", reply, err)
	}
	last := (*calls)[len(*calls)-1]
	var p struct {
		To   string                `json:"to"`
		Type string                `json:"type"`
		Text struct{ Body string } `json:"text"`
	}
	json.Unmarshal([]byte(last.Body), &p)
	if last.Path != "/PNID/messages" || p.To != "91"+mobile || p.Type != "text" || p.Text.Body != reply {
		t.Fatalf("reply sent %+v", p)
	}
	// Now inside the 24-hour window: a template without a WhatsApp name may
	// go as text.
	if !o.waSession(ctx, T, mobile) {
		t.Fatal("no session after the client wrote")
	}
	_ = url.Values{}
}
