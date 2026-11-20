package ops

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"ledgerline/internal/docs"
)

// All keys here are made fresh for the test run; all data is made up.

var eipoTestKeySet struct {
	once               sync.Once
	member, nse, other *rsa.PrivateKey
	err                error
}

// eipoTestKeys are the firm's key pair, NSE's key pair for the firm and a
// third pair standing for a wrong key. Made once; never changed.
func eipoTestKeys(t *testing.T) (member, nse, other *rsa.PrivateKey) {
	t.Helper()
	k := &eipoTestKeySet
	k.once.Do(func() {
		for _, p := range []**rsa.PrivateKey{&k.member, &k.nse, &k.other} {
			if *p, k.err = rsa.GenerateKey(rand.Reader, 2048); k.err != nil {
				return
			}
		}
	})
	if k.err != nil {
		t.Fatal(k.err)
	}
	return k.member, k.nse, k.other
}

func eipoPEMOf(typ string, der []byte) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}))
}

func eipoPEMPriv(t *testing.T, k *rsa.PrivateKey) string {
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return eipoPEMOf("PRIVATE KEY", der)
}

func eipoPEMPub(t *testing.T, k *rsa.PublicKey) string {
	der, err := x509.MarshalPKIXPublicKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return eipoPEMOf("PUBLIC KEY", der)
}

func eipoPEMCert(t *testing.T, k *rsa.PrivateKey) string {
	tpl := &x509.Certificate{SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: "TEST NSE EIPO KEY - MADE UP"},
		NotBefore: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), NotAfter: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	return eipoPEMOf("CERTIFICATE", der)
}

// TestEIPOV2Format checks the sealed message against the standard library
// directly: key1 opens with Go's OAEP-SHA1, the body with Go's AES-GCM (IV
// in front), Sig is SHA-256 of the encoded body, and key3 is OAEP(Sig)
// raised to the private exponent (checked by raising it to the public
// exponent and handing that, encrypted again, to Go's OAEP decryption).
func TestEIPOV2Format(t *testing.T) {
	member, nse, other := eipoTestKeys(t)
	plain := []byte(`{"symbol":"TESTIPO","applicationNumber":"9031100000000001"}`)
	m, err := eipoSeal(&nse.PublicKey, member, plain, true)
	if err != nil {
		t.Fatal(err)
	}
	k1, _ := base64.StdEncoding.DecodeString(m.Key1)
	key, err := rsa.DecryptOAEP(sha1.New(), nil, nse, k1, nil)
	if err != nil || len(key) != 32 {
		t.Fatalf("key1 %d %v", len(key), err)
	}
	ct, _ := base64.StdEncoding.DecodeString(string(m.Body))
	blk, _ := aes.NewCipher(key)
	g, _ := cipher.NewGCM(blk)
	if got, err := g.Open(nil, ct[:12], ct[12:], nil); err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("body %q %v", got, err)
	}
	sum := sha256.Sum256(m.Body)
	if m.Sig != base64.StdEncoding.EncodeToString(sum[:]) {
		t.Fatalf("Sig %s", m.Sig)
	}
	s, _ := base64.StdEncoding.DecodeString(m.Key3)
	e := big.NewInt(int64(member.E))
	em := new(big.Int).Exp(new(big.Int).SetBytes(s), e, member.N)
	c := new(big.Int).Exp(em, e, member.N)
	if got, err := rsa.DecryptOAEP(sha1.New(), nil, member, c.FillBytes(make([]byte, 256)), nil); err != nil || string(got) != m.Sig || len(s) != 256 {
		t.Fatalf("key3 %q %v", got, err)
	}

	h := http.Header{}
	m.set(h)
	if got, err := eipoOpen(nse, &member.PublicKey, h, m.Body, true, true); err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("open %q %v", got, err)
	}
	// A GET: no body, a random Sig of at most 50 letters and digits, signed.
	g2, err := eipoSeal(&nse.PublicKey, member, nil, false)
	if err != nil || g2.Body != nil || len(g2.Sig) == 0 || len(g2.Sig) > 50 || strings.Trim(g2.Sig, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789") != "" || g2.Key1 == "" {
		t.Fatalf("GET seal %+v %v", g2, err)
	}
	hg := http.Header{}
	g2.set(hg)
	if _, err := eipoOpen(nse, &member.PublicKey, hg, nil, false, true); err != nil {
		t.Fatalf("GET open %v", err)
	}

	// Tampering, and wrong keys.
	bad := func(name string, own *rsa.PrivateKey, from *rsa.PublicKey, h http.Header, body []byte, needSig bool, want string) {
		t.Helper()
		if _, err := eipoOpen(own, from, h, body, true, needSig); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	changed := append([]byte(nil), m.Body...)
	// another base64 letter, so the body still decodes (a flipped bit
	// could make it not base64 at all)
	changed[10] = map[bool]byte{true: 'B', false: 'A'}[changed[10] == 'A']
	bad("changed body", nse, &member.PublicKey, h, changed, true, "changed on the way")
	// Sig made again for a changed body: the signature no longer matches.
	h2 := h.Clone()
	h2.Set("Sig", eipoDigest(changed))
	bad("Sig remade", nse, &member.PublicKey, h2, changed, true, "signature (key3) does not match")
	// A reply (no key3) whose Sig was remade: AES-GCM catches it.
	h3 := h.Clone()
	h3.Del("Key3")
	h3.Set("Sig", eipoDigest(changed))
	bad("reply remade", nse, nil, h3, changed, false, "cannot be decrypted")
	bad("signed by another key", nse, &other.PublicKey, h, m.Body, true, "signature (key3) does not match")
	bad("sealed for another key", other, &member.PublicKey, h, m.Body, true, "key1")
	h4 := h.Clone()
	h4.Del("Key3")
	bad("no key3 on a request", nse, &member.PublicKey, h4, m.Body, true, "no signature")
	h5 := h.Clone()
	h5.Set("Key2", "something else")
	bad("Sig and key2 differ", nse, &member.PublicKey, h5, m.Body, true, "differ")
	if _, err := eipoRSAPubDecrypt(&member.PublicKey, []byte("short")); err == nil {
		t.Fatal("a short signature decrypted")
	}
}

// eipoV2Fake is NSE's eIPO speaking v2 as Ledgerline implements it: it
// checks the member's signature, opens the request with NSE's private key,
// has the v3 fake answer it and seals the answer for the member. v3 calls
// (the APIs v2 does not cover) go to the v3 fake as they are.
type eipoV2Fake struct {
	srv    *httptest.Server
	mu     sync.Mutex
	paths  []string
	authV2 int // v2 requests carrying a token
	tamper func(h http.Header, body []byte) []byte
}

func (f *eipoV2Fake) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.paths...)
}

func (f *eipoV2Fake) setTamper(fn func(h http.Header, body []byte) []byte) {
	f.mu.Lock()
	f.tamper = fn
	f.mu.Unlock()
}

func newEIPOV2Fake(t *testing.T, nse *rsa.PrivateKey, member *rsa.PublicKey) *eipoV2Fake {
	t.Helper()
	inner := newEIPOFake(t)
	f := &eipoV2Fake{}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ver, p, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/eipo/"), "/")
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.paths = append(f.paths, ver+"/"+p)
		tamper := f.tamper
		if ver == "v2" && (r.Header.Get("Authorization") != "" || r.Header.Get("Access-Token") != "") {
			f.authV2++
		}
		f.mu.Unlock()
		forward := func(method string, b []byte, auth string) (int, []byte) {
			req, _ := http.NewRequest(method, inner.URL+"/eipo/v3/"+p, bytes.NewReader(b))
			req.Header.Set("Content-Type", "application/json")
			if auth != "" {
				req.Header.Set("Authorization", auth)
			}
			resp, err := inner.Client().Do(req)
			if err != nil {
				return 502, nil
			}
			defer resp.Body.Close()
			out, _ := io.ReadAll(resp.Body)
			return resp.StatusCode, out
		}
		switch ver {
		case "v3":
			if p == "heartbeat" {
				if r.Header.Get("Authorization") != "Bearer tok-1" {
					w.WriteHeader(401)
					return
				}
				w.Write([]byte(`{"status":"success","currentTime":1793167800000}`))
				return
			}
			code, out := forward(r.Method, body, r.Header.Get("Authorization"))
			w.WriteHeader(code)
			w.Write(out)
		case "v2":
			if !eipoV2Covers("/" + p) {
				w.WriteHeader(404)
				return
			}
			if r.Header.Get("member") != "90311" || r.Header.Get("loginId") != "LLAPI01" {
				w.WriteHeader(401)
				w.Write([]byte(`{"status":"failed","reason":"unknown member"}`))
				return
			}
			plain, err := eipoOpen(nse, member, r.Header, body, r.Method == http.MethodPost, true)
			if err != nil {
				w.WriteHeader(401)
				w.Write([]byte(`{"status":"failed","reason":"` + err.Error() + `"}`))
				return
			}
			code, out := forward(r.Method, plain, "Bearer tok-1")
			m, err := eipoSeal(member, nil, out, true)
			if err != nil {
				w.WriteHeader(500)
				return
			}
			m.set(w.Header())
			b := m.Body
			if tamper != nil {
				b = tamper(w.Header(), b)
			}
			w.WriteHeader(code)
			w.Write(b)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func TestIPOEIPOV2HTTPClient(t *testing.T) {
	member, nse, other := eipoTestKeys(t)
	f := newEIPOV2Fake(t, nse, &member.PublicKey)
	c := nseEIPOHTTP{client: f.srv.Client()}
	ctx := context.Background()
	cfg := EIPOConfig{Base: f.srv.URL + "/eipo", Member: "90311", Login: "LLAPI01", Password: "pw-made-up", Version: "v2", MemberKey: member, NSEKey: &nse.PublicKey}
	bid := EIPOBid{Symbol: "APICO", AppNo: "9031100000000001", Category: "IND", Depository: "CDSL", ClientBenID: "1208160000000001", PAN: "AAAAA0000A", AllotmentMode: "demat",
		UPIFlag: "Y", UPI: "x@okicici", LedgerlineID: "IPB-0000001", Bids: []EIPOBidLine{{ActivityType: "new", Quantity: 150, AtCutOff: true, Amount: "15000.00", Remark: "IPB-0000001"}}}
	ups, err := c.SendBids(ctx, cfg, []EIPOBid{bid})
	if err != nil || len(ups) != 1 || ups[0].Status != "success" || len(ups[0].Lines) != 1 || ups[0].Lines[0].BidRef != "2026102800000382" {
		t.Fatalf("v2 send %+v %v", ups, err)
	}
	if ups, err = c.Inquire(ctx, cfg, "APICO", []string{"9031100000000001", "NOSUCHAPP"}); err != nil || len(ups) != 1 || ups[0].PayStatus != "100" {
		t.Fatalf("v2 inquiry %+v %v", ups, err)
	}
	if ms, err := c.Master(ctx, cfg); err != nil || len(ms) != 1 || ms[0].Symbol != "TESTIPO" {
		t.Fatalf("v2 master (a GET) %+v %v", ms, err)
	}
	if al, err := c.Allotment(ctx, cfg, "APICO"); err != nil || len(al) != 1 || al[0].Qty != 150 {
		t.Fatalf("v2 allotment %+v %v", al, err)
	}
	// No login and no token in v2: the signature authenticates each call.
	if got := strings.Join(f.seen(), " "); got != "v2/transactions/add v2/transactions/fetch v2/transactions/fetch v2/ipomaster v2/allotment/fetch" || f.authV2 != 0 {
		t.Fatalf("v2 calls %s (%d with a token)", got, f.authV2)
	}
	// An API v2 does not cover goes by v3, logging in.
	if out, err := c.call(ctx, cfg, http.MethodGet, cfg.path("/heartbeat"), nil); err != nil || !strings.Contains(string(out), "success") {
		t.Fatalf("heartbeat by v3 %s %v", out, err)
	}
	if s := f.seen(); strings.Join(s[len(s)-2:], " ") != "v3/login v3/heartbeat" {
		t.Fatalf("fallback calls %v", s)
	}

	// A reply that fails the check is an error, and nothing in it is used.
	tampers := map[string]func(h http.Header, body []byte) []byte{
		"changed body": func(_ http.Header, b []byte) []byte { b = append([]byte(nil), b...); b[5] ^= 1; return b },
		"other key1": func(h http.Header, b []byte) []byte {
			k, _ := rsa.EncryptOAEP(sha1.New(), rand.Reader, &member.PublicKey, make([]byte, 32), nil)
			h.Set("Key1", base64.StdEncoding.EncodeToString(k))
			return b
		},
		"bad key3": func(h http.Header, b []byte) []byte {
			h.Set("Key3", base64.StdEncoding.EncodeToString(make([]byte, 256)))
			return b
		},
		"no Sig":     func(h http.Header, b []byte) []byte { h.Del("Sig"); return b },
		"plain body": func(h http.Header, _ []byte) []byte { h.Del("Key1"); return []byte(`{"status":"success","data":[]}`) },
	}
	for name, fn := range tampers {
		f.setTamper(fn)
		ms, err := c.Master(ctx, cfg)
		if err == nil || ms != nil || !strings.Contains(err.Error(), "v2 security check") || !strings.Contains(err.Error(), "nothing in it was used") {
			t.Fatalf("%s: %+v %v", name, ms, err)
		}
	}
	f.setTamper(nil)
	// Wrong keys on Ledgerline's side: NSE refuses.
	for name, k := range map[string]EIPOConfig{
		"sealed for another key": {Base: cfg.Base, Member: "90311", Login: "LLAPI01", Password: "pw-made-up", Version: "v2", MemberKey: member, NSEKey: &other.PublicKey},
		"signed by another key":  {Base: cfg.Base, Member: "90311", Login: "LLAPI01", Password: "pw-made-up", Version: "v2", MemberKey: other, NSEKey: &nse.PublicKey},
	} {
		if _, err := c.SendBids(ctx, k, []EIPOBid{bid}); err == nil || !strings.Contains(err.Error(), "refused the v2 request (401)") {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err := c.Master(ctx, EIPOConfig{Base: cfg.Base, Member: "90311", Login: "LLAPI01", Password: "pw-made-up", Version: "v2"}); err == nil || !strings.Contains(err.Error(), "keys are missing") {
		t.Fatalf("no keys: %v", err)
	}
	// v3 is unchanged: login, then the Bearer token, in plain JSON.
	if ups, err := c.SendBids(ctx, EIPOConfig{Base: cfg.Base, Member: "90311", Login: "LLAPI01", Password: "pw-made-up"}, []EIPOBid{bid}); err != nil || len(ups) != 1 || ups[0].Status != "success" {
		t.Fatalf("v3 send %+v %v", ups, err)
	}
	if s := f.seen(); s[len(s)-1] != "v3/transactions/add" {
		t.Fatalf("v3 calls %v", s)
	}
}

func TestIPOEIPOV2Connection(t *testing.T) {
	o, ctx := setup(t)
	o.Now = func() time.Time { return ipoAt(2026, 10, 28, 11, 0) }
	member, nse, _ := eipoTestKeys(t)
	f := newEIPOV2Fake(t, nse, &member.PublicKey)
	old := eipoClient
	eipoClient = nseEIPOHTTP{client: f.srv.Client()}
	t.Cleanup(func() { eipoClient = old })
	vals := map[string]string{"member": "90311", "login": "LLAPI01", "env": "Mock (UAT)", "url": f.srv.URL + "/eipo", "version": eipoV2, "send": "Off", "master": "On"}
	save := func(secrets map[string]string) error {
		_, err := o.SaveConnection(ctx, T, "admin", connNSEEIPO, vals, secrets)
		return err
	}
	small, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	der1 := x509.MarshalPKCS1PrivateKey(member)
	pkcs1 := eipoPEMOf("RSA PRIVATE KEY", der1)
	for _, c := range []struct {
		priv, pub, want string
	}{
		{"", eipoPEMPub(t, &nse.PublicKey), "v2 needs the firm's RSA private key"},
		{pkcs1, "", "v2 needs NSE's public key"},
		{"not a key", eipoPEMPub(t, &nse.PublicKey), "firm's private key could not be read"},
		{eipoPEMPriv(t, small), eipoPEMPub(t, &nse.PublicKey), "1024-bit"},
		{pkcs1, eipoPEMPub(t, &small.PublicKey), "1024-bit"},
		{eipoPEMPub(t, &member.PublicKey), eipoPEMPub(t, &nse.PublicKey), "a public key or certificate goes to NSE"},
		{pkcs1, eipoPEMPriv(t, nse), "a private key was pasted as NSE's public key"},
		{pkcs1, eipoPEMPub(t, &member.PublicKey), "the firm's own public key"},
		{pkcs1, "-----BEGIN PUBLIC KEY-----\nAAAA\n-----END PUBLIC KEY-----", "NSE's public key could not be read"},
	} {
		if err := save(map[string]string{"password": "pw-made-up", "private_key": c.priv, "nse_public_key": c.pub}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("keys %q: %v", c.want, err)
		}
	}
	// A PKCS#1 key pasted into a one-line field (line breaks lost) and
	// NSE's key as a certificate are read.
	if err := save(map[string]string{"password": "pw-made-up", "private_key": strings.ReplaceAll(pkcs1, "\n", " "), "nse_public_key": eipoPEMCert(t, nse)}); err != nil {
		t.Fatal(err)
	}
	if v := o.EIPOStatus(ctx, T); v.Version != "v2" || !v.Usable || v.Needs != "" {
		t.Fatalf("status %+v", v)
	}
	// A reply that fails the check sets nothing up.
	f.setTamper(func(_ http.Header, b []byte) []byte { b = append([]byte(nil), b...); b[8] ^= 1; return b })
	if _, _, err := o.EIPOSyncMaster(ctx, T, "priya.nair"); err == nil || !strings.Contains(err.Error(), "v2 security check") {
		t.Fatalf("tampered master: %v", err)
	}
	if is, _ := docs.List[IPOIssue](ctx, o.Docs, T, cIPOIssue, ""); len(is) != 0 {
		t.Fatalf("issues from a tampered reply: %+v", is)
	}
	if st := o.EIPOStatus(ctx, T).State; !strings.Contains(st.LastError, "v2 security check") {
		t.Fatalf("state %+v", st)
	}
	f.setTamper(nil)
	if added, _, err := o.EIPOSyncMaster(ctx, T, "priya.nair"); err != nil || added != 1 {
		t.Fatalf("v2 master %d %v", added, err)
	}
	if s := f.seen(); s[len(s)-1] != "v2/ipomaster" {
		t.Fatalf("calls %v", s)
	}
	// Back on v3 the keys may stay; they are not used.
	vals["version"] = eipoV3
	if err := save(nil); err != nil {
		t.Fatal(err)
	}
	if v := o.EIPOStatus(ctx, T); v.Version != "v3" || !v.Usable {
		t.Fatalf("v3 status %+v", v)
	}
}

func TestIPOEIPOV2Callback(t *testing.T) {
	o, ctx := setup(t)
	o.Now = func() time.Time { return ipoAt(2026, 10, 28, 11, 0) }
	member, nse, other := eipoTestKeys(t)
	fake := &fakeEIPO{}
	old := eipoClient
	eipoClient = fake
	t.Cleanup(func() { eipoClient = old })
	iss := mainboard(t, o, ctx, "APICO")
	cs := ipoClients(t, o, ctx, 1)
	regUPI(t, o, ctx, cs[0].UCC)
	b, err := o.PlaceIPOBid(ctx, T, "priya.nair", IPOBidInput{Issue: iss.ID, UCC: cs[0].UCC, Category: "RII", Options: opt(1, "", true), UPI: vpaOf(cs[0].UCC), Instruction: staffSaid(o)})
	if err != nil {
		t.Fatal(err)
	}
	const tok = "cb-token-made-up-0123456789"
	vals := map[string]string{"member": "90311", "login": "LLAPI01", "env": "Mock (UAT)", "send": "On", "version": eipoV2}
	if _, err := o.SaveConnection(ctx, T, "admin", connNSEEIPO, vals, map[string]string{"password": "made-up-password", "callback_token": tok,
		"private_key": eipoPEMPriv(t, member), "nse_public_key": eipoPEMPub(t, &nse.PublicKey)}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.EIPOSend(ctx, T, "priya.nair", iss.ID); err != nil || len(fake.sent) != 1 || fake.cfg.Version != "v2" || fake.cfg.MemberKey == nil || fake.cfg.NSEKey == nil {
		t.Fatalf("send %v %+v", err, fake.cfg)
	}
	app := bidOf(t, o, ctx, b.ID).AppNo
	dp := []byte(`{"symbol":"APICO","applicationNumber":"` + app + `","dpVerStatusFlag":"S","dpVerReason":null,"dpVerFailCode":null}`)
	// NSE's callback, sealed for the firm and signed with NSE's key.
	sealed := func(to *rsa.PublicKey, by *rsa.PrivateKey, body []byte) (http.Header, []byte) {
		m, err := eipoSeal(to, by, body, true)
		if err != nil {
			t.Fatal(err)
		}
		h := http.Header{}
		m.set(h)
		h.Set("member", "NSE")
		h.Set("loginId", "NSE")
		return h, m.Body
	}
	h, body := sealed(&member.PublicKey, nse, dp)
	changed := append([]byte(nil), body...)
	changed[3] ^= 1
	hm := h.Clone()
	hm.Set("member", "90311")
	hw := h.Clone()
	hw.Set("Authorization", eipoCallbackAuth("other"))
	hOther, bOther := sealed(&member.PublicKey, other, dp)
	hFor, bFor := sealed(&other.PublicKey, nse, dp)
	for name, c := range map[string]struct {
		h    http.Header
		body []byte
	}{
		"plain JSON":            {http.Header{"Authorization": {eipoCallbackAuth("made-up-password")}}, dp},
		"changed body":          {h, changed},
		"signed by another key": {hOther, bOther},
		"sealed for another":    {hFor, bFor},
		"member not NSE":        {hm, body},
		"wrong Authorization":   {hw, body},
	} {
		if _, err := o.EIPOCallbackHTTP(ctx, T, tok, "appdpstatus", c.h, "203.0.113.9:443", c.body); !errors.Is(err, ErrForbidden) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err := o.EIPOCallback(ctx, T, tok, "appdpstatus", eipoCallbackAuth("made-up-password"), "203.0.113.9:443", dp); !errors.Is(err, ErrForbidden) {
		t.Fatalf("plain callback on v2: %v", err)
	}
	if a := bidOf(t, o, ctx, b.ID); a.DPCheck == "Verified" {
		t.Fatalf("a refused callback was applied %+v", a)
	}
	// Taken without the Authorization (the signature authenticates NSE) and
	// with it.
	if res, err := o.EIPOCallbackHTTP(ctx, T, tok, "appdpstatus", h, "203.0.113.9:443", body); err != nil || res.Applied != 1 {
		t.Fatalf("sealed callback %+v %v", res, err)
	}
	pay := []byte(`{"symbol":"APICO","applicationNumber":"` + app + `","upiPaymentStatusFlag":100,"upiAmtBlocked":15000.00,"upiPayReason":null}`)
	hp, bp := sealed(&member.PublicKey, nse, pay)
	hp.Set("Authorization", eipoCallbackAuth("made-up-password"))
	if res, err := o.EIPOCallbackHTTP(ctx, T, tok, "apppaystatus", hp, "203.0.113.9:443", bp); err != nil || res.Applied != 1 {
		t.Fatalf("sealed callback with Authorization %+v %v", res, err)
	}
	if a := bidOf(t, o, ctx, b.ID); a.DPCheck != "Verified" || a.Status != BidAccepted {
		t.Fatalf("after callback %+v", a)
	}
	// The reply goes back sealed for NSE (key1 and Sig, no key3).
	rh, rb := o.EIPOCallbackReply(ctx, T, tok, map[string]string{"status": "success"})
	if rh.Get("Key1") == "" || rh.Get("Key3") != "" {
		t.Fatalf("reply headers %v", rh)
	}
	if got, err := eipoOpen(nse, nil, rh, rb, true, false); err != nil || string(got) != `{"status":"success"}` {
		t.Fatalf("reply %s %v", got, err)
	}
	if rh, rb := o.EIPOCallbackReply(ctx, T, "wrong", map[string]string{"status": "failed"}); rh.Get("Key1") != "" || string(rb) != `{"status":"failed"}` {
		t.Fatalf("reply to a wrong token %v %s", rh, rb)
	}
}
