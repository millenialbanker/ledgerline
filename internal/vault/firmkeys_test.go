package vault

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/vault/kmsfake"
)

// Made-up credentials for the fake KMS.
const (
	testAKID   = "AKIAFAKEFAKEFAKE0001"
	testSecret = "fakeSecretKey/doNotUse+0123456789abcdefgh"
)

// clock is a test clock that only moves when told.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type firmRig struct {
	t      *testing.T
	ctx    context.Context
	mem    *docs.Mem
	raw    *ingest.MemRawStore
	kr     *Keyring
	v      *Vault
	d      *Docs
	r      *Raw
	kms    *kmsfake.KMS
	arn    string
	clk    *clock
	mu     sync.Mutex
	events []KeyEvent
	role   string // assume this role when set
	ext    string
}

func newFirmRig(t *testing.T) *firmRig {
	t.Helper()
	clk := &clock{t: time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)}
	f := kmsfake.New("ap-south-1", "111122223333")
	t.Cleanup(f.Close)
	f.Now = clk.Now
	f.AddCreds(testAKID, testSecret)
	kr, err := ParseKeyring(NewKey(1), "server test key")
	if err != nil {
		t.Fatal(err)
	}
	g := &firmRig{t: t, ctx: context.Background(), mem: docs.NewMem(), raw: ingest.NewMemRawStore(), kr: kr, kms: f, arn: f.AddKey(), clk: clk}
	g.v = g.vault()
	return g
}

// vault is a Vault over the rig's stores, as a fresh server start would
// make it.
func (g *firmRig) vault() *Vault {
	v := New(g.kr)
	v.bindStore(g.mem)
	v.SetFirmKeyHooks(FirmKeyHooks{Now: g.clk.Now, Resolve: func(ctx context.Context, tenant string, rec FirmKeyRecord) (KeyProvider, error) {
		return g.provider(g.arn), nil
	}, Notify: func(tenant string, ev KeyEvent) {
		g.mu.Lock()
		g.events = append(g.events, ev)
		g.mu.Unlock()
	}})
	g.d, g.r = &Docs{Inner: g.mem, V: v}, &Raw{Inner: g.raw, V: v}
	return v
}

func (g *firmRig) provider(arn string) *AWSKMS {
	return &AWSKMS{Region: "ap-south-1", KeyARN: arn, Creds: AWSCredentials{AccessKeyID: testAKID, SecretAccessKey: testSecret},
		RoleARN: g.role, ExternalID: g.ext, SessionName: "ledgerline-alpha",
		Endpoint: g.kms.URL(), STSEndpoint: g.kms.URL(), Client: g.kms.Client(), Now: g.clk.Now}
}

func (g *firmRig) put(id, body string) error {
	_, err := g.d.Put(g.ctx, "alpha", "client", id, json.RawMessage(body), 0, true, "ops")
	return err
}

func (g *firmRig) get(id string) (string, error) {
	x, err := g.d.Get(g.ctx, "alpha", "client", id)
	return string(x.Body), err
}

func (g *firmRig) eventKinds() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []string
	for _, e := range g.events {
		out = append(out, e.Kind)
	}
	return out
}

func storedVersion(t *testing.T, mem *docs.Mem, tenant, id string) int {
	t.Helper()
	x, _ := mem.Get(context.Background(), tenant, "client", id)
	var s string
	if err := json.Unmarshal(x.Body, &s); err != nil {
		t.Fatal(err)
	}
	ct := make([]byte, len(s))
	n, err := base64.StdEncoding.Decode(ct, []byte(strings.TrimPrefix(s, "llv1:")))
	if err != nil {
		t.Fatal(err)
	}
	return int(binary.BigEndian.Uint32(ct[:n][len(magic):]))
}

func TestSigV4KnownAnswers(t *testing.T) {
	// AWS's documented examples: the signing key for the IAM example, and
	// the "get-vanilla" case of the Signature Version 4 test suite.
	if got := fmt.Sprintf("%x", SigningKeyV4("wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", "20150830", "us-east-1", "iam")); got != "c4afb1cc5771d871763a393e44b703571b55cc28424d1a5e86da6ed3c154a4b9" {
		t.Fatalf("signing key %s", got)
	}
	req, _ := http.NewRequest("GET", "https://example.amazonaws.com/", nil)
	SignV4(req, nil, AWSCredentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"}, "us-east-1", "service", time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC))
	want := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, SignedHeaders=host;x-amz-date, Signature=5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31"
	if got := req.Header.Get("Authorization"); got != want {
		t.Fatalf("get-vanilla:\n got %s\nwant %s", got, want)
	}
}

func TestFirmKeyKMSRoundTrip(t *testing.T) {
	g := newFirmRig(t)
	if err := g.put("OLD", `{"pan":"ABCDE1234F"}`); err != nil {
		t.Fatal(err)
	}
	st, err := g.v.UseFirmKey(g.ctx, "alpha", "firmkey-aws", g.provider(g.arn), 0, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if !st.Own || st.Current != 1 || st.CacheMinutes != DefaultCacheMinutes || !st.Available || len(st.Roots) != 1 || st.Roots[0].KeyID != g.arn {
		t.Fatalf("status after the move: %+v", st)
	}
	if err := g.put("NEW", `{"pan":"ZYXWV9876K"}`); err != nil {
		t.Fatal(err)
	}
	if v := storedVersion(t, g.mem, "alpha", "NEW"); v != FirmVersionBase+1 {
		t.Fatalf("new record under key version %d, want the firm's root 1", v)
	}
	if v := storedVersion(t, g.mem, "alpha", "OLD"); v != 1 {
		t.Fatalf("old record was rewritten: version %d", v)
	}
	for id, want := range map[string]string{"OLD": `{"pan":"ABCDE1234F"}`, "NEW": `{"pan":"ZYXWV9876K"}`} {
		if got, err := g.get(id); err != nil || got != want {
			t.Fatalf("%s: %s %v", id, got, err)
		}
	}
	// Files too.
	f, err := g.r.Put(g.ctx, ingest.RawFile{Tenant: "alpha", Source: "CLIENT-DOC", Name: "pan.pdf", SHA256: "ab", Content: []byte("scan")})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := g.r.Get(g.ctx, "alpha", f.ID); err != nil || string(got.Content) != "scan" {
		t.Fatalf("file: %v", err)
	}

	// Every call was signed, scoped to Mumbai and KMS, and carried the
	// encryption context binding firm, purpose and version.
	calls := g.kms.Calls()
	if len(calls) < 2 {
		t.Fatalf("calls: %+v", calls)
	}
	seen := map[string]bool{}
	for _, c := range calls {
		seen[c.Target] = true
		if c.Status != 200 || c.AccessKey != testAKID || !strings.Contains(c.SignedHeaders, "x-amz-target") || !strings.Contains(c.SignedHeaders, "host") {
			t.Fatalf("call: %+v", c)
		}
		if c.Context[CtxTenant] != "alpha" || c.Context[CtxPurpose] != PurposeRoot || c.Context[CtxVersion] != "1" {
			t.Fatalf("encryption context: %+v", c.Context)
		}
	}
	if !seen["Encrypt"] || !seen["Decrypt"] {
		t.Fatalf("calls: %+v", calls)
	}

	// A firm on its own key cannot be read with the server master key.
	plain := &Docs{Inner: g.mem, V: New(g.kr)}
	if _, err := plain.Get(g.ctx, "alpha", "client", "NEW"); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("server master key opened the firm's record: %v", err)
	}
	// Another firm is untouched: still on the server key.
	if _, err := g.d.Put(g.ctx, "beta", "client", "B1", json.RawMessage(`{"n":1}`), 0, false, "ops"); err != nil {
		t.Fatal(err)
	}
	if v := storedVersion(t, g.mem, "beta", "B1"); v != 1 {
		t.Fatalf("beta's record under %d", v)
	}
	// Moving twice is refused.
	if _, err := g.v.UseFirmKey(g.ctx, "alpha", "firmkey-aws", g.provider(g.arn), 0, "admin"); err == nil {
		t.Fatal("moved twice")
	}

	// A restart unwraps from the stored record with no cache.
	g.v = g.vault()
	if got, err := g.get("NEW"); err != nil || got != `{"pan":"ZYXWV9876K"}` {
		t.Fatalf("after restart: %s %v", got, err)
	}
}

func TestFirmKeyRevokedAndRestored(t *testing.T) {
	for _, how := range []string{"disabled", "access revoked"} {
		t.Run(how, func(t *testing.T) {
			g := newFirmRig(t)
			g.put("OLD", `{"n":0}`)
			if _, err := g.v.UseFirmKey(g.ctx, "alpha", "firmkey-aws", g.provider(g.arn), 10, "admin"); err != nil {
				t.Fatal(err)
			}
			g.put("NEW", `{"n":1}`)
			revoke := func(on bool) {
				if how == "disabled" {
					g.kms.SetEnabled(g.arn, !on)
				} else {
					g.kms.Revoke(testAKID, on)
				}
			}
			revoke(true)
			// Within the cache window the keys in memory still work.
			g.clk.Add(9 * time.Minute)
			if _, err := g.get("NEW"); err != nil {
				t.Fatalf("within the window: %v", err)
			}
			// After it, nothing of the firm's is served or written, older
			// records included, and nothing falls back to the server key.
			g.clk.Add(2 * time.Minute)
			for _, id := range []string{"NEW", "OLD"} {
				_, err := g.get(id)
				var ke *KeyError
				if !errors.Is(err, ErrKeyUnavailable) || !errors.As(err, &ke) || !ke.Refused {
					t.Fatalf("%s: %v", id, err)
				}
				if !strings.Contains(err.Error(), "your firm's encryption key is not available (revoked or unreachable)") {
					t.Fatalf("message: %v", err)
				}
			}
			if err := g.put("NEW2", `{"n":2}`); !errors.Is(err, ErrKeyUnavailable) {
				t.Fatalf("write while revoked: %v", err)
			}
			if _, ok := g.mem.Get(g.ctx, "alpha", "client", "NEW2"); ok == nil {
				t.Fatal("a record was written while the key was refused")
			}
			if _, err := g.d.List(g.ctx, "alpha", "client", ""); !errors.Is(err, ErrKeyUnavailable) {
				t.Fatalf("list: %v", err)
			}
			if _, err := g.r.Put(g.ctx, ingest.RawFile{Tenant: "alpha", Source: "X", Name: "x", SHA256: "cd", Content: []byte("x")}); !errors.Is(err, ErrKeyUnavailable) {
				t.Fatalf("file write: %v", err)
			}
			// Requests in the retry interval do not call KMS again.
			n := len(g.kms.Calls())
			g.get("NEW")
			if len(g.kms.Calls()) != n {
				t.Fatal("KMS called again inside the retry interval")
			}
			g.clk.Add(retryDown + time.Second)
			g.get("NEW")
			if len(g.kms.Calls()) != n+1 {
				t.Fatal("KMS not asked again after the retry interval")
			}
			// Announced once, also across a restart.
			if k := g.eventKinds(); len(k) != 1 || k[0] != KeyLost {
				t.Fatalf("events: %v", k)
			}
			g.v = g.vault()
			if _, err := g.get("NEW"); !errors.Is(err, ErrKeyUnavailable) {
				t.Fatalf("after restart: %v", err)
			}
			if k := g.eventKinds(); len(k) != 1 {
				t.Fatalf("announced again after a restart: %v", k)
			}
			st, _ := g.v.FirmKey(g.ctx, "alpha")
			if st.Available || st.Outage == nil || st.Problem == "" {
				t.Fatalf("status while revoked: %+v", st)
			}
			// The key comes back: service resumes and that is announced once.
			revoke(false)
			g.clk.Add(retryDown + time.Second)
			if got, err := g.get("NEW"); err != nil || got != `{"n":1}` {
				t.Fatalf("restored: %s %v", got, err)
			}
			if err := g.put("NEW2", `{"n":2}`); err != nil {
				t.Fatal(err)
			}
			g.get("OLD")
			if k := g.eventKinds(); len(k) != 2 || k[1] != KeyBack {
				t.Fatalf("events: %v", k)
			}
			if st, _ := g.v.FirmKey(g.ctx, "alpha"); !st.Available || st.Outage != nil {
				t.Fatalf("status restored: %+v", st)
			}
		})
	}
}

func TestFirmKeyUnreachable(t *testing.T) {
	g := newFirmRig(t)
	if _, err := g.v.UseFirmKey(g.ctx, "alpha", "firmkey-aws", g.provider(g.arn), 1, "admin"); err != nil {
		t.Fatal(err)
	}
	g.put("A", `{"n":1}`)
	g.kms.Close()
	g.clk.Add(2 * time.Minute)
	_, err := g.get("A")
	var ke *KeyError
	if !errors.As(err, &ke) || ke.Refused || !strings.Contains(ke.Reason, "could not be reached") {
		t.Fatalf("unreachable: %v", err)
	}
}

func TestFirmKeyRotation(t *testing.T) {
	g := newFirmRig(t)
	g.put("PRE", `{"n":0}`)
	if _, err := g.v.UseFirmKey(g.ctx, "alpha", "firmkey-aws", g.provider(g.arn), 0, "admin"); err != nil {
		t.Fatal(err)
	}
	g.put("R1", `{"n":1}`)
	old := g.arn
	g.arn = g.kms.AddKey() // the firm makes a new KMS key and enters it in the connection
	st, err := g.v.RotateFirmKey(g.ctx, "alpha", g.provider(g.arn), "admin")
	if err != nil {
		t.Fatal(err)
	}
	if st.Current != 2 || len(st.Roots) != 2 || st.Roots[0].KeyID != g.arn || st.Roots[1].KeyID != g.arn || st.RotatedBy != "admin" {
		t.Fatalf("after rotation: %+v", st)
	}
	g.put("R2", `{"n":2}`)
	if v := storedVersion(t, g.mem, "alpha", "R2"); v != FirmVersionBase+2 {
		t.Fatalf("R2 under %d", v)
	}
	// Every root is now under the new key: the old one can be retired.
	g.kms.SetEnabled(old, false)
	if _, err := g.v.CheckFirmKey(g.ctx, "alpha"); err != nil {
		t.Fatal(err)
	}
	g.v = g.vault() // and a restart
	for id, want := range map[string]string{"PRE": `{"n":0}`, "R1": `{"n":1}`, "R2": `{"n":2}`} {
		if got, err := g.get(id); err != nil || got != want {
			t.Fatalf("%s after rotation: %s %v", id, got, err)
		}
	}
}

func TestFirmKeyAssumeRole(t *testing.T) {
	g := newFirmRig(t)
	g.role, g.ext = "arn:aws:iam::111122223333:role/LedgerlineKeyUse", "ledgerline-alpha-7f3a"
	g.kms.AddRole(g.role, g.ext)
	if _, err := g.v.UseFirmKey(g.ctx, "alpha", "firmkey-aws", g.provider(g.arn), 0, "admin"); err != nil {
		t.Fatal(err)
	}
	g.put("A", `{"n":1}`)
	sts, kms := 0, 0
	for _, c := range g.kms.Calls() {
		switch {
		case c.Service == "sts" && c.Status == 200:
			sts++
		case c.Service == "kms" && c.Status == 200 && c.SessionToken && strings.Contains(c.SignedHeaders, "x-amz-security-token"):
			kms++
		}
	}
	if sts != 1 || kms < 2 {
		t.Fatalf("sts %d, kms with the role %d: %+v", sts, kms, g.kms.Calls())
	}
	// A wrong external ID is refused.
	g.ext = "guess"
	if _, err := TestFirmKey(g.ctx, g.provider(g.arn), "alpha"); err == nil {
		t.Fatal("wrong external ID accepted")
	}
	// Revoking the role stops the firm at the next unwrap.
	g.ext = "ledgerline-alpha-7f3a"
	g.kms.Revoke(g.role, true)
	g.clk.Add(16 * time.Minute) // the role's credentials and the cache have both expired
	if _, err := g.get("A"); !errors.Is(err, ErrKeyUnavailable) {
		t.Fatalf("role revoked: %v", err)
	}
}

func TestFirmKeyRemoteService(t *testing.T) {
	g := newFirmRig(t)
	ks := kmsfake.NewKeyService("shared-secret-for-test-only-1234")
	defer ks.Close()
	ks.Now = g.clk.Now
	prov := &RemoteKeyService{URL: ks.Server.URL, Secret: ks.Secret, Client: ks.Client(), Now: g.clk.Now}
	g.v.SetFirmKeyHooks(FirmKeyHooks{Now: g.clk.Now, Resolve: func(context.Context, string, FirmKeyRecord) (KeyProvider, error) { return prov, nil },
		Notify: func(string, KeyEvent) {}})
	if _, err := g.v.UseFirmKey(g.ctx, "alpha", "firmkey-remote", prov, 1, "admin"); err != nil {
		t.Fatal(err)
	}
	g.put("A", `{"n":1}`)
	if got, err := g.get("A"); err != nil || got != `{"n":1}` {
		t.Fatalf("%s %v", got, err)
	}
	ks.SetDown(true)
	g.clk.Add(2 * time.Minute)
	var ke *KeyError
	if _, err := g.get("A"); !errors.As(err, &ke) || ke.Refused {
		t.Fatalf("service down: %v", err)
	}
	ks.SetDown(false)
	ks.SetRefused(true)
	g.clk.Add(retryDown + time.Second)
	if _, err := g.get("A"); !errors.As(err, &ke) || !ke.Refused || !strings.Contains(ke.Reason, "withdrawn") {
		t.Fatalf("service refused: %v", err)
	}
	ks.SetRefused(false)
	g.clk.Add(retryDown + time.Second)
	if _, err := g.get("A"); err != nil {
		t.Fatal(err)
	}
	if _, bad := ks.Calls(); bad != 0 {
		t.Fatalf("%d requests with a bad signature", bad)
	}
	// A wrong shared secret is refused by the service.
	wrong := &RemoteKeyService{URL: ks.Server.URL, Secret: "not-the-secret", Client: ks.Client(), Now: g.clk.Now}
	if _, err := TestFirmKey(g.ctx, wrong, "alpha"); err == nil {
		t.Fatal("wrong secret accepted")
	}
	// Context binding: a root wrapped for alpha does not unwrap for beta.
	blob, id, err := prov.Wrap(g.ctx, make([]byte, 32), Bind("alpha", 1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := prov.Unwrap(g.ctx, blob, id, Bind("beta", 1)); err == nil {
		t.Fatal("unwrapped for another firm")
	}
}

func TestFirmKeyRecordMissing(t *testing.T) {
	g := newFirmRig(t)
	g.v.PinFirmKeys("alpha")
	if err := g.put("A", `{"n":1}`); !errors.Is(err, ErrKeyUnavailable) {
		t.Fatalf("pinned firm without a record wrote under the server key: %v", err)
	}
	if err := g.put("A", `{"n":1}`); !errors.Is(err, ErrKeyUnavailable) {
		t.Fatal(err)
	}
	// Unpinned: the record removed in the database after the move.
	g2 := newFirmRig(t)
	if _, err := g2.v.UseFirmKey(g2.ctx, "alpha", "firmkey-aws", g2.provider(g2.arn), 1, "admin"); err != nil {
		t.Fatal(err)
	}
	g2.mem.Put(g2.ctx, "alpha", KeyCollection, keyRecordID, json.RawMessage(`{}`), 0, true, "db")
	g2.clk.Add(2 * time.Minute)
	if err := g2.put("B", `{"n":1}`); err == nil {
		t.Fatal("wrote after the key record was removed")
	}
}

func TestFirmKeyNoSecretsInErrorsOrStatus(t *testing.T) {
	g := newFirmRig(t)
	if _, err := g.v.UseFirmKey(g.ctx, "alpha", "firmkey-aws", g.provider(g.arn), 1, "admin"); err != nil {
		t.Fatal(err)
	}
	g.kms.SetEnabled(g.arn, false)
	g.clk.Add(2 * time.Minute)
	_, err := g.get("A")
	st, _ := g.v.FirmKey(g.ctx, "alpha")
	b, _ := json.Marshal(st)
	p := g.provider(g.arn)
	texts := []string{fmt.Sprint(err), string(b), fmt.Sprintf("%v %+v %#v", p.Creds, p.Creds, p.Creds), p.String()}
	for _, e := range g.events {
		texts = append(texts, fmt.Sprintf("%+v", e))
	}
	rec, _ := g.mem.Get(g.ctx, "alpha", KeyCollection, keyRecordID)
	texts = append(texts, string(rec.Body))
	for _, s := range texts {
		if strings.Contains(s, testSecret) || strings.Contains(s, testAKID) {
			t.Fatalf("a credential leaked: %s", s)
		}
	}
	// The key record is sealed under the server key, not stored in clear.
	if strings.Contains(string(rec.Body), "firmkey-aws") || !strings.HasPrefix(string(rec.Body), envPrefix) {
		t.Fatalf("key record not sealed: %s", rec.Body)
	}
}

func TestFirmKeyCacheWindow(t *testing.T) {
	g := newFirmRig(t)
	if _, err := g.v.SetFirmKeyCache(g.ctx, "alpha", 5, "admin"); err == nil {
		t.Fatal("cache set for a firm on Ledgerline's key")
	}
	if _, err := g.v.UseFirmKey(g.ctx, "alpha", "firmkey-aws", g.provider(g.arn), 99, "admin"); err != nil {
		t.Fatal(err)
	}
	if st, _ := g.v.FirmKey(g.ctx, "alpha"); st.CacheMinutes != MaxCacheMinutes {
		t.Fatalf("cache not bounded: %d", st.CacheMinutes)
	}
	for _, bad := range []int{0, 61} {
		if _, err := g.v.SetFirmKeyCache(g.ctx, "alpha", bad, "admin"); err == nil {
			t.Fatalf("cache %d accepted", bad)
		}
	}
	st, err := g.v.SetFirmKeyCache(g.ctx, "alpha", 2, "admin")
	if err != nil || st.CacheMinutes != 2 || !st.CachedUntil.Equal(g.clk.Now().Add(2*time.Minute)) {
		t.Fatalf("%+v %v", st, err)
	}
	g.kms.SetEnabled(g.arn, false)
	g.clk.Add(3 * time.Minute)
	if err := g.put("A", `{}`); !errors.Is(err, ErrKeyUnavailable) {
		t.Fatalf("revocation not in effect after the window: %v", err)
	}
}

func TestParseKMSKeyARN(t *testing.T) {
	for _, bad := range []string{"", "arn:aws:kms:us-east-1:111122223333:key/abc", "arn:aws:kms:ap-south-1:1111:key/abc", "arn:aws:s3:ap-south-1:111122223333:key/abc", "arn:aws:kms:ap-south-1:111122223333:secret/abc", "arn:aws:kms:eu-west-1:111122223333:alias/x"} {
		if _, _, err := ParseKMSKeyARN(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	for _, good := range []string{"arn:aws:kms:ap-south-1:111122223333:key/1234abcd-12ab-34cd-56ef-1234567890ab", "arn:aws:kms:ap-south-2:111122223333:alias/ledgerline"} {
		if _, _, err := ParseKMSKeyARN(good); err != nil {
			t.Errorf("%s: %v", good, err)
		}
	}
	if _, err := ParseKeyring(fmt.Sprintf("%d:%s", FirmVersionBase, strings.SplitN(NewKey(1), ":", 2)[1]), ""); err == nil {
		t.Error("server key version in the firm range accepted")
	}
}
