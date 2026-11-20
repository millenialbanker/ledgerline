package ops

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/vault"
	"ledgerline/internal/vault/kmsfake"
)

// Made-up AWS credentials for the fake KMS.
const (
	fkAKID   = "AKIAFAKEFIRMKEY00042"
	fkSecret = "madeUpSecretAccessKey/forTests+0000000000"
)

type fkClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fkClock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fkClock) Add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

func firmKeySetup(t *testing.T) (*Ops, context.Context, *kmsfake.KMS, string, *fkClock) {
	t.Helper()
	o, ctx := setup(t)
	clk := &fkClock{t: time.Date(2026, 11, 20, 11, 0, 0, 0, time.UTC)}
	o.Now = clk.Now
	f := kmsfake.New("ap-south-1", "111122223333")
	t.Cleanup(f.Close)
	f.Now = clk.Now
	f.AddCreds(fkAKID, fkSecret)
	arn := f.AddKey()
	o.FirmKeyClient = f.Client()
	return o, ctx, f, arn, clk
}

func saveKMSConn(o *Ops, ctx context.Context, f *kmsfake.KMS, arn, secret string) (ConnectionView, error) {
	return o.SaveConnection(ctx, T, "admin", connFirmKeyAWS,
		map[string]string{"region": "ap-south-1", "key_arn": arn, "access_key_id": fkAKID, "url": f.URL(), "sts_url": f.URL()},
		map[string]string{"secret_access_key": secret})
}

func firmKeyAudit(t *testing.T, o *Ops, ctx context.Context, action string) []string {
	t.Helper()
	evs, err := o.App.Audit.List(ctx, T, 5000)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range evs {
		if e.Action == action {
			b, _ := json.Marshal(e)
			out = append(out, string(b))
		}
	}
	return out
}

func TestFirmKeyConnectionChecks(t *testing.T) {
	o, ctx, f, arn, _ := firmKeySetup(t)
	bad := []map[string]string{
		{"region": "ap-south-1", "key_arn": strings.Replace(arn, "ap-south-1", "us-east-1", 1), "access_key_id": fkAKID},
		{"region": "ap-south-2", "key_arn": arn, "access_key_id": fkAKID},
		{"region": "ap-south-1", "key_arn": arn, "access_key_id": "not a key"},
		{"region": "ap-south-1", "key_arn": arn, "access_key_id": fkAKID, "role_arn": "role/x"},
		{"region": "ap-south-1", "key_arn": arn, "access_key_id": fkAKID, "external_id": "abc"},
		{"region": "eu-west-1", "key_arn": arn, "access_key_id": fkAKID},
	}
	for _, v := range bad {
		if _, err := o.SaveConnection(ctx, T, "admin", connFirmKeyAWS, v, map[string]string{"secret_access_key": fkSecret}); err == nil {
			t.Errorf("accepted %v", v)
		}
	}
	if _, err := o.SaveConnection(ctx, T, "admin", connFirmKeyRemote, map[string]string{"url": "https://keys.firm.example"}, map[string]string{"secret": "short"}); err == nil {
		t.Error("short shared secret accepted")
	}
	if _, err := o.SaveConnection(ctx, T, "admin", connFirmKeyRemote, map[string]string{"url": "https://keys.firm.example"}, nil); err == nil {
		t.Error("key service with neither secret nor certificate accepted")
	}
	// Region empty: Mumbai.
	if cv, err := o.SaveConnection(ctx, T, "admin", connFirmKeyAWS, map[string]string{"key_arn": arn, "access_key_id": fkAKID, "url": f.URL()}, map[string]string{"secret_access_key": fkSecret}); err != nil || cv.Values["region"] != "ap-south-1" {
		t.Fatalf("%+v %v", cv, err)
	}
	// The test wraps and unwraps with the firm's encryption context.
	cv, err := o.TestConnection(ctx, T, "admin", connFirmKeyAWS)
	if err != nil || cv.Status != "Working" {
		t.Fatalf("test: %+v %v", cv, err)
	}
	var enc, dec bool
	for _, c := range f.Calls() {
		if c.Context[vault.CtxTenant] != T || c.Context[vault.CtxPurpose] != vault.PurposeRoot {
			t.Fatalf("context %+v", c.Context)
		}
		enc, dec = enc || c.Target == "Encrypt", dec || c.Target == "Decrypt"
	}
	if !enc || !dec {
		t.Fatalf("calls %+v", f.Calls())
	}
	// A wrong secret shows as a problem.
	saveKMSConn(o, ctx, f, arn, "wrongSecretAccessKey/forTests+00000000000")
	if cv, _ := o.TestConnection(ctx, T, "admin", connFirmKeyAWS); cv.Status != "Problem" || !strings.Contains(cv.Problem, "signature") {
		t.Fatalf("wrong secret: %+v", cv)
	}
}

func TestFirmKeyMoveRevokeRestoreRotate(t *testing.T) {
	o, ctx, f, arn, clk := firmKeySetup(t)
	cs, _ := o.Clients(ctx, T)
	ucc := cs[0].UCC

	v, err := o.FirmKey(ctx, T, "vikram.shah")
	if err != nil || v.Mode != "ledgerline" || !v.Encrypted {
		t.Fatalf("view: %+v %v", v, err)
	}
	if _, err := o.FirmKey(ctx, T, "priya.nair"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker saw keys: %v", err)
	}
	if _, err := o.UseFirmKey(ctx, T, "admin", connFirmKeyAWS, 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("moved without a connection: %v", err)
	}
	if _, err := saveKMSConn(o, ctx, f, arn, fkSecret); err != nil {
		t.Fatal(err)
	}
	if _, err := o.UseFirmKey(ctx, T, "vikram.shah", connFirmKeyAWS, 0); !errors.Is(err, ErrForbidden) {
		t.Fatalf("compliance moved the key: %v", err)
	}
	v, err = o.UseFirmKey(ctx, T, "admin", connFirmKeyAWS, 10)
	if err != nil || v.Mode != "firm" || !v.Available || v.Current != 1 || v.CacheMinutes != 10 || v.Region != "ap-south-1" {
		t.Fatalf("moved: %+v %v", v, err)
	}
	if strings.Contains(v.Key, "111122223333") || !strings.HasSuffix(v.Key, "6666") || v.LastOK == "" || v.CachedUntil == "" {
		t.Fatalf("key not masked or status missing: %+v", v)
	}
	if n := len(firmKeyAudit(t, o, ctx, "firmkey.moved")); n != 1 {
		t.Fatalf("moved audited %d times", n)
	}
	s, _ := o.Security(ctx, T, "vikram.shah", false)
	if !s.FirmKey || !strings.Contains(s.KeySource, "AWS KMS") {
		t.Fatalf("security page: %+v", s)
	}
	for _, l := range s.Residency {
		if l.What == "Encryption keys" && (l.Status != "india" || !strings.Contains(l.Where, "ap-south-1")) {
			t.Fatalf("residency line for keys: %+v", l)
		}
	}
	// Business as usual: old and new records.
	if _, err := o.Client(ctx, T, ucc); err != nil {
		t.Fatal(err)
	}
	if err := docs.Upsert(ctx, o.Docs, T, "note", "N1", map[string]string{"n": "after the move"}, "admin"); err != nil {
		t.Fatal(err)
	}

	// The firm disables its key. After the cache window nothing is served.
	f.SetEnabled(arn, false)
	clk.Add(11 * time.Minute)
	if _, err := o.Client(ctx, T, ucc); !errors.Is(err, vault.ErrKeyUnavailable) {
		t.Fatalf("served while the key is disabled: %v", err)
	}
	if _, err := o.FirmKey(ctx, T, "admin"); !errors.Is(err, vault.ErrKeyUnavailable) {
		t.Fatalf("even the role check needs the firm's key: %v", err)
	}
	for i := 0; i < 3; i++ {
		clk.Add(31 * time.Second)
		o.Client(ctx, T, ucc)
	}
	if n := len(firmKeyAudit(t, o, ctx, "firmkey.unavailable")); n != 1 {
		t.Fatalf("outage audited %d times", n)
	}
	ws, err := docs.List[WorkItem](ctx, o.App.Docs, T, cWorkItem, "W-firmkey-")
	if err != nil || len(ws) != 1 || ws[0].Status != "Open" || ws[0].Role != RoleAdmin || !strings.Contains(ws[0].Detail, "DisabledException") {
		t.Fatalf("work item: %+v %v", ws, err)
	}
	// The vault's own view still answers (it reads only the key record).
	if s, _ := o.vault().FirmKey(ctx, T); s.Available || s.Outage == nil {
		t.Fatalf("vault status: %+v", s)
	}

	// The key comes back: service resumes, the work item is done.
	f.SetEnabled(arn, true)
	clk.Add(31 * time.Second)
	if _, err := o.Client(ctx, T, ucc); err != nil {
		t.Fatalf("not restored: %v", err)
	}
	if n := len(firmKeyAudit(t, o, ctx, "firmkey.restored")); n != 1 {
		t.Fatalf("restore audited %d times", n)
	}
	ws, _ = docs.List[WorkItem](ctx, o.App.Docs, T, cWorkItem, "W-firmkey-")
	if len(ws) != 1 || ws[0].Status != "Done" {
		t.Fatalf("work item after restore: %+v", ws)
	}

	// Access revoked (a grant or key policy statement removed): a new
	// outage, a new work item.
	f.Revoke(fkAKID, true)
	clk.Add(11 * time.Minute)
	if _, err := o.Client(ctx, T, ucc); !errors.Is(err, vault.ErrKeyUnavailable) {
		t.Fatalf("served after access was revoked: %v", err)
	}
	f.Revoke(fkAKID, false)
	clk.Add(31 * time.Second)
	if _, err := o.Client(ctx, T, ucc); err != nil {
		t.Fatal(err)
	}
	ws, _ = docs.List[WorkItem](ctx, o.App.Docs, T, cWorkItem, "W-firmkey-")
	if len(ws) != 2 || ws[0].Status != "Done" || ws[1].Status != "Done" {
		t.Fatalf("second outage: %+v", ws)
	}

	// Changing the connection so that it can no longer unwrap is refused,
	// and so is removing it.
	if _, err := saveKMSConn(o, ctx, f, arn, "wrongSecretAccessKey/forTests+00000000000"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a locking-out change was saved: %v", err)
	}
	if err := o.RemoveConnection(ctx, T, "admin", connFirmKeyAWS); !errors.Is(err, ErrState) {
		t.Fatalf("removed the key in use: %v", err)
	}

	// Rotation onto a new KMS key: history stays readable, the old key can go.
	arn2 := f.AddKey()
	if _, err := saveKMSConn(o, ctx, f, arn2, ""); err != nil {
		t.Fatalf("new ARN (the old roots still unwrap with their own key): %v", err)
	}
	v, err = o.RotateFirmKey(ctx, T, "admin")
	if err != nil || v.Current != 2 || len(v.Roots) != 2 || !v.Roots[0].Current || v.RotatedBy != "admin" {
		t.Fatalf("rotated: %+v %v", v, err)
	}
	f.SetEnabled(arn, false)
	clk.Add(11 * time.Minute)
	if _, err := o.Client(ctx, T, ucc); err != nil {
		t.Fatalf("old record after rotation: %v", err)
	}
	if n, _, err := docs.Get[map[string]string](ctx, o.Docs, T, "note", "N1"); err != nil || n["n"] != "after the move" {
		t.Fatalf("root 1 record after rotation: %v %v", n, err)
	}
	if v, err := o.SetFirmKeyCache(ctx, T, "admin", 5); err != nil || v.CacheMinutes != 5 {
		t.Fatalf("cache: %+v %v", v, err)
	}
	if _, err := o.SetFirmKeyCache(ctx, T, "admin", 120); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cache 120: %v", err)
	}

	// No credential in the audit trail, work items, views or the key record.
	var texts []string
	evs, _ := o.App.Audit.List(ctx, T, 5000)
	for _, e := range evs {
		b, _ := json.Marshal(e)
		texts = append(texts, string(b))
	}
	for _, w := range ws {
		texts = append(texts, w.Title+w.Detail)
	}
	fv, _ := o.FirmKey(ctx, T, "admin")
	cv, _ := o.Connections(ctx, T, "admin")
	b1, _ := json.Marshal(fv)
	b2, _ := json.Marshal(cv)
	texts = append(texts, string(b1), string(b2))
	for _, s := range texts {
		if strings.Contains(s, fkSecret) {
			t.Fatalf("the secret access key leaked: %s", s)
		}
	}
}

func TestFirmKeyServerMasterCannotRead(t *testing.T) {
	o, ctx, f, arn, _ := firmKeySetup(t)
	saveKMSConn(o, ctx, f, arn, fkSecret)
	if _, err := o.UseFirmKey(ctx, T, "admin", connFirmKeyAWS, 0); err != nil {
		t.Fatal(err)
	}
	if err := docs.Upsert(ctx, o.Docs, T, "note", "N1", map[string]string{"pan": "ABCDE1234F"}, "admin"); err != nil {
		t.Fatal(err)
	}
	// The same server keys without the firm's key record (the database read
	// with Ledgerline's master key alone) cannot open it.
	inner := o.App.Docs.(*vault.Docs).Inner
	bare := &vault.Docs{Inner: inner, V: vault.New(o.vault().Keys())}
	if _, err := bare.Get(ctx, T, "note", "N1"); !errors.Is(err, vault.ErrDecrypt) {
		t.Fatalf("server master key opened the firm's record: %v", err)
	}
}
