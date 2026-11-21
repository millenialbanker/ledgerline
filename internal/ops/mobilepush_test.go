package ops

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// fakeFCM is Google's token endpoint and FCM's send endpoint, made up:
// it checks the signed JWT and the bearer token as Google would, and keeps
// what it was sent.
type fakeFCM struct {
	srv    *httptest.Server
	key    *rsa.PrivateKey
	mu     sync.Mutex
	tokens int
	sent   []fcmMessage
	expire bool // the next send answers 401 once
}

func newFakeFCM(t *testing.T) *fakeFCM {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeFCM{key: k}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/token":
			r.ParseForm()
			parts := strings.Split(r.Form.Get("assertion"), ".")
			if r.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" || len(parts) != 3 {
				w.WriteHeader(400)
				w.Write([]byte(`{"error":"invalid_grant"}`))
				return
			}
			sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
			sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
			var cl map[string]any
			b, _ := base64.RawURLEncoding.DecodeString(parts[1])
			json.Unmarshal(b, &cl)
			if rsa.VerifyPKCS1v15(&k.PublicKey, crypto.SHA256, sum[:], sig) != nil || cl["scope"] != fcmScope || cl["iss"] != "push@demo-firm.iam.gserviceaccount.com" || cl["aud"] != f.srv.URL+"/token" {
				w.WriteHeader(400)
				w.Write([]byte(`{"error":"invalid_grant","error_description":"Invalid JWT Signature."}`))
				return
			}
			f.tokens++
			w.Write([]byte(`{"access_token":"ya29.made-up-` + string(rune('0'+f.tokens)) + `","expires_in":3599,"token_type":"Bearer"}`))
		case r.URL.Path == "/v1/projects/demo-firm/messages:send":
			if f.expire {
				f.expire = false
				w.WriteHeader(401)
				w.Write([]byte(`{"error":{"code":401,"status":"UNAUTHENTICATED","message":"Request had invalid authentication credentials."}}`))
				return
			}
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ya29.made-up-") {
				w.WriteHeader(401)
				return
			}
			var m fcmMessage
			body, _ := io.ReadAll(r.Body)
			if json.Unmarshal(body, &m) != nil || m.Message.Token == "" {
				w.WriteHeader(400)
				return
			}
			f.sent = append(f.sent, m)
			if strings.HasPrefix(m.Message.Token, "gone-") {
				w.WriteHeader(404)
				w.Write([]byte(`{"error":{"code":404,"message":"Requested entity was not found.","status":"NOT_FOUND","details":[{"@type":"type.googleapis.com/google.firebase.fcm.v1.FcmError","errorCode":"UNREGISTERED"}]}}`))
				return
			}
			w.Write([]byte(`{"name":"projects/demo-firm/messages/0:1"}`))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeFCM) serviceAccount() string {
	der, _ := x509.MarshalPKCS8PrivateKey(f.key)
	pk := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	b, _ := json.Marshal(map[string]string{"type": "service_account", "project_id": "demo-firm", "private_key_id": "k1", "private_key": pk,
		"client_email": "push@demo-firm.iam.gserviceaccount.com", "token_uri": f.srv.URL + "/token"})
	return string(b)
}

func (f *fakeFCM) take() []fcmMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.sent
	f.sent = nil
	return out
}

func pushSetup(t *testing.T) (*Ops, context.Context, *fakeFCM) {
	t.Helper()
	o, ctx := setup(t)
	mobileClock(o)
	f := newFakeFCM(t)
	o.FCMClient, o.FCMBase = f.srv.Client(), f.srv.URL
	return o, ctx, f
}

func TestPushConnection(t *testing.T) {
	o, ctx, f := pushSetup(t)
	if _, err := o.SaveConnection(ctx, T, "admin", connFCM, nil, map[string]string{"service_account": `{"type":"authorized_user"}`}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("not a service account: %v", err)
	}
	if _, err := o.SaveConnection(ctx, T, "admin", connFCM, nil, map[string]string{"service_account": "not json"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("not JSON: %v", err)
	}
	v, err := o.SaveConnection(ctx, T, "admin", connFCM, nil, map[string]string{"service_account": f.serviceAccount()})
	if err != nil || !v.SecretSet["service_account"] || v.Status != "Saved" {
		t.Fatalf("save %+v %v", v, err)
	}
	if !strings.Contains(v.Purpose, "India-only mode") || !strings.Contains(v.Purpose, "never carries client data") {
		t.Fatalf("purpose %q", v.Purpose)
	}
	c, ok := o.fcmConn(ctx, T)
	if !ok || c.Project != "demo-firm" {
		t.Fatalf("conn %+v", c)
	}
}

// clientWords are values that must never be in a push: client codes,
// names, PANs, amounts and scrips.
func noClientData(t *testing.T, m fcmMessage, words ...string) {
	t.Helper()
	b, _ := json.Marshal(m)
	s := string(b)
	for _, w := range words {
		if w != "" && strings.Contains(strings.ToLower(s), strings.ToLower(w)) {
			t.Errorf("push carries %q: %s", w, s)
		}
	}
	if strings.Contains(s, "₹") || regexp.MustCompile(`\d[\d,]*\.\d\d`).MatchString(s) {
		t.Errorf("push carries an amount: %s", s)
	}
	for k := range m.Message.Data {
		if k != "kind" && k != "route" && k != "app" && k != "count" {
			t.Errorf("push data key %q", k)
		}
	}
}

func TestPushSenderAndPayload(t *testing.T) {
	o, ctx, f := pushSetup(t)
	staffOn(t, o, ctx, MobileSettings{StaffRoles: []string{RoleChecker, RoleCompliance}})
	reg := func(user, app, token string) DeviceView {
		t.Helper()
		pw := DevPassword
		if app == AppClient {
			pw = "a long client password"
		}
		v, _, err := o.RegisterDevice(ctx, T, user, signIn(t, o, ctx, user, pw), DeviceReq{App: app, Platform: "android", PushToken: token})
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	good := reg("farhan.khan", AppStaff, pushTok)
	gone := reg("farhan.khan", AppStaff, "gone-"+pushTok)
	cs, _ := o.Clients(ctx, T)
	var c Client
	for _, x := range cs {
		if x.Status == "Active" && x.PAN != "" {
			c = x
			break
		}
	}
	// Two payouts prepared for the checker, with the client in the words.
	rc := &runCtx{o: o, ctx: ctx, tenant: T, date: D, process: "payouts", keys: map[string]bool{}}
	for _, id := range []string{"PO-T1", "PO-T2"} {
		rc.queue(id, "Payout "+id+" · "+c.Name, c.UCC+" asks for ₹1,23,456.78 to "+c.PAN, c.UCC, RoleChecker, "money", "", &Proposal{Kind: "payout.approve", Label: "Approve payout", Args: map[string]string{"id": id}})
	}
	// Not connected yet: the push is not sent, and says so.
	if n, err := o.SendPushes(ctx, T); err != nil || n != 0 {
		t.Fatalf("send without a connection: %d %v", n, err)
	}
	ms, _ := o.Notifications(ctx, T, "farhan.khan")
	if len(ms) != 1 || ms[0].Kind != PushApprovals {
		t.Fatalf("notifications %+v", ms)
	}
	if _, err := o.SaveConnection(ctx, T, "admin", connFCM, nil, map[string]string{"service_account": f.serviceAccount()}); err != nil {
		t.Fatal(err)
	}
	rc.queue("PO-T3", "Payout PO-T3 · "+c.Name, c.UCC+" ₹5,000.00", c.UCC, RoleChecker, "money", "", &Proposal{Kind: "payout.approve", Label: "Approve payout", Args: map[string]string{"id": "PO-T3"}})
	rc.queue("PO-T4", "Payout PO-T4 · "+c.Name, c.UCC+" ₹6,000.00", c.UCC, RoleChecker, "money", "", &Proposal{Kind: "payout.approve", Label: "Approve payout", Args: map[string]string{"id": "PO-T4"}})
	n, err := o.SendPushes(ctx, T)
	if err != nil || n != 1 {
		t.Fatalf("send: %d %v", n, err)
	}
	sent := f.take()
	if len(sent) != 2 {
		t.Fatalf("%d pushes sent, want one per phone", len(sent))
	}
	want := o.staffWaitingCount(ctx, T, "farhan.khan")
	for _, m := range sent {
		noClientData(t, m, c.UCC, c.Name, c.PAN, MaskPAN(c.PAN), "PO-T", "1,23,456")
		if m.Message.Notification["title"] != "Approvals waiting" || !strings.HasPrefix(m.Message.Notification["body"], countWords(want, "approval is", "approvals are")) {
			t.Errorf("words %+v (want %d waiting)", m.Message.Notification, want)
		}
		if m.Message.Data["route"] != "approvals" || m.Message.Data["app"] != AppStaff {
			t.Errorf("data %+v", m.Message.Data)
		}
	}
	// The phone FCM no longer knows is forgotten; the other keeps its token.
	if x, _, _ := docsGetDevice(ctx, o, gone.ID); x.PushToken != "" {
		t.Fatal("expired token kept")
	}
	if x, _, _ := docsGetDevice(ctx, o, good.ID); x.PushToken != pushTok {
		t.Fatal("good token dropped")
	}
	// The access token is reused, and fetched again when FCM says it expired.
	f.mu.Lock()
	tokens := f.tokens
	f.expire = true
	f.mu.Unlock()
	if err := o.PushToStaff(ctx, T, []string{"farhan.khan", "priya.nair"}, PushStepOverdue); err != nil {
		t.Fatal(err)
	}
	if err := o.PushToStaff(ctx, T, []string{"farhan.khan"}, "Client "+c.UCC+" owes money"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("free text accepted: %v", err)
	}
	if n, _ := o.SendPushes(ctx, T); n != 1 {
		t.Fatalf("overdue push: %d", n)
	}
	sent = f.take()
	if len(sent) != 1 || sent[0].Message.Notification["body"] != "1 step of today is overdue." { // after one refusal with 401
		t.Fatalf("overdue %+v", sent)
	}
	f.mu.Lock()
	if f.tokens != tokens+1 {
		t.Errorf("token fetched %d times, want once more", f.tokens-tokens)
	}
	f.mu.Unlock()

	// An alert: the compliance officer's phone, no client in the words.
	reg("vikram.shah", AppStaff, pushTok+"-vs")
	if !o.raiseAlert(ctx, T, SystemActor, Alert{Type: "Turnover vs income", UCC: c.UCC, Detail: c.Name + " traded ₹9,99,999.00", Severity: "High"}, "TEST:"+c.UCC) {
		t.Fatal("alert not raised")
	}
	o.SendPushes(ctx, T)
	sent = f.take()
	if len(sent) != 1 || sent[0].Message.Notification["title"] != "Alert raised" {
		t.Fatalf("alert push %+v", sent)
	}
	noClientData(t, sent[0], c.UCC, c.Name, "9,99,999")

	// The client app: a contract note's message becomes "ready", nothing
	// of the note.
	id, _, err := o.PortalAccess(ctx, T, "farhan.khan", c.UCC, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	o.SetPassword(ctx, T, "admin", id, "a long client password", false)
	if _, err := o.SaveMobileSettings(ctx, T, "admin", MobileSettings{StaffOn: true, StaffRoles: []string{RoleChecker, RoleCompliance}, Client: ClientAppBrand{On: true, AppName: "Demo Invest"}}); err != nil {
		t.Fatal(err)
	}
	reg(id, AppClient, pushTok+"-client")
	o.notify(ctx, T, "priya.nair", "contract_note.sent", c.UCC, map[string]string{"no": "CN/2026/000123", "net": "12,345.67"})
	o.notify(ctx, T, "priya.nair", "payout.rejected", c.UCC, map[string]string{"amount": "50,000.00", "reason": "insufficient balance"})
	o.SendPushes(ctx, T)
	sent = f.take()
	if len(sent) != 2 {
		t.Fatalf("client pushes %+v", sent)
	}
	bodies := sent[0].Message.Notification["body"] + "|" + sent[1].Message.Notification["body"]
	if !strings.Contains(bodies, "Your contract note is ready.") || !strings.Contains(bodies, "There is an update on your funds") {
		t.Fatalf("client words %q", bodies)
	}
	for _, m := range sent {
		noClientData(t, m, c.UCC, c.Name, c.PAN, "CN/2026", "12,345", "50,000", "insufficient")
		if m.Message.Data["app"] != AppClient {
			t.Errorf("client push app %+v", m.Message.Data)
		}
	}
	// The test button reaches the person's own phones.
	if n, err := o.TestPush(ctx, T, "farhan.khan"); err != nil || n != 1 {
		t.Fatalf("test push %d %v", n, err)
	}
	if _, err := o.TestPush(ctx, T, "anita.rao"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("test push without a phone: %v", err)
	}
}
