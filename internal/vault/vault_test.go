package vault

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/app"
	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
)

func ring(t *testing.T, spec string) *Vault {
	t.Helper()
	k, err := ParseKeyring(spec, "test")
	if err != nil {
		t.Fatal(err)
	}
	return New(k)
}

const pan = "ABCPI4321K"

func TestDocumentsAreEncryptedAndReadable(t *testing.T) {
	ctx := context.Background()
	mem := docs.NewMem()
	v := New(Ephemeral())
	d := &Docs{Inner: mem, V: v}
	body := json.RawMessage(`{"ucc":"BH1001","pan":"` + pan + `","bank":{"account":"50100123456789"}}`)
	if _, err := d.Put(ctx, "alpha", "client", "BH1001", body, 0, false, "ops"); err != nil {
		t.Fatal(err)
	}
	raw, _ := mem.Get(ctx, "alpha", "client", "BH1001")
	if bytes.Contains(raw.Body, []byte(pan)) || bytes.Contains(raw.Body, []byte("50100123456789")) {
		t.Fatalf("plaintext stored: %s", raw.Body)
	}
	if !json.Valid(raw.Body) {
		t.Fatalf("stored body must stay valid JSON for jsonb: %s", raw.Body)
	}
	got, err := d.Get(ctx, "alpha", "client", "BH1001")
	if err != nil || !bytes.Equal(got.Body, body) {
		t.Fatalf("round trip: %s %v", got.Body, err)
	}
	ls, err := d.List(ctx, "alpha", "client", "BH")
	if err != nil || len(ls) != 1 || !bytes.Equal(ls[0].Body, body) {
		t.Fatalf("list: %v %v", ls, err)
	}
	// Typed helpers work unchanged on top.
	type client struct{ PAN string }
	c, _, err := docs.Get[client](ctx, d, "alpha", "client", "BH1001")
	if err != nil || c.PAN != pan {
		t.Fatalf("typed get: %+v %v", c, err)
	}
}

func TestCiphertextCannotBeMovedOrAltered(t *testing.T) {
	ctx := context.Background()
	mem := docs.NewMem()
	d := &Docs{Inner: mem, V: New(Ephemeral())}
	d.Put(ctx, "alpha", "client", "A", json.RawMessage(`{"pan":"AAAAA1111A"}`), 0, false, "x")
	d.Put(ctx, "alpha", "client", "B", json.RawMessage(`{"pan":"BBBBB2222B"}`), 0, false, "x")
	a, _ := mem.Get(ctx, "alpha", "client", "A")
	// Copy A's ciphertext over B (a database-level swap).
	mem.Put(ctx, "alpha", "client", "B", a.Body, 0, true, "attacker")
	if _, err := d.Get(ctx, "alpha", "client", "B"); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("moved ciphertext accepted: %v", err)
	}
	// Flip one character of the ciphertext.
	bad := []byte(string(a.Body))
	bad[20] ^= 1
	if bad[20] == '"' || bad[20] == '\\' {
		bad[20] ^= 3
	}
	mem.Put(ctx, "alpha", "client", "A", bad, 0, true, "attacker")
	if _, err := d.Get(ctx, "alpha", "client", "A"); err == nil {
		t.Fatal("altered ciphertext accepted")
	}
}

func TestFirmsCannotReadEachOther(t *testing.T) {
	v := New(Ephemeral())
	ct, err := v.Seal("alpha", []byte("aad"), []byte(pan))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Open("beta", []byte("aad"), ct); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("beta opened alpha's data: %v", err)
	}
	if pt, err := v.Open("alpha", []byte("aad"), ct); err != nil || string(pt) != pan {
		t.Fatalf("alpha: %q %v", pt, err)
	}
	// A different master key cannot open it either.
	if _, err := New(Ephemeral()).Open("alpha", []byte("aad"), ct); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("other master opened: %v", err)
	}
}

func TestKeyRotation(t *testing.T) {
	ctx := context.Background()
	mem := docs.NewMem()
	k1 := NewKey(1)
	old := &Docs{Inner: mem, V: ring(t, k1)}
	old.Put(ctx, "alpha", "client", "A", json.RawMessage(`{"n":1}`), 0, false, "x")
	k2 := NewKey(2)
	rot := &Docs{Inner: mem, V: ring(t, k2+","+k1)}
	if got, err := rot.Get(ctx, "alpha", "client", "A"); err != nil || string(got.Body) != `{"n":1}` {
		t.Fatalf("old version unreadable after rotation: %v", err)
	}
	rot.Put(ctx, "alpha", "client", "B", json.RawMessage(`{"n":2}`), 0, false, "x")
	// The new record is under version 2, so a keyring without it fails.
	if _, err := old.Get(ctx, "alpha", "client", "B"); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("version 2 record opened with version 1 only: %v", err)
	}
	// Dropping version 1 too early is caught, not silently wrong.
	only2 := &Docs{Inner: mem, V: ring(t, k2)}
	if _, err := only2.Get(ctx, "alpha", "client", "A"); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("expected a clear error without key 1: %v", err)
	}
}

func TestPlaintextWrittenBeforeEncryptionStillReads(t *testing.T) {
	ctx := context.Background()
	mem := docs.NewMem()
	mem.Put(ctx, "alpha", "client", "OLD", json.RawMessage(`{"pan":"OLDPN1234X"}`), 0, false, "x")
	d := &Docs{Inner: mem, V: New(Ephemeral())}
	got, err := d.Get(ctx, "alpha", "client", "OLD")
	if err != nil || !strings.Contains(string(got.Body), "OLDPN1234X") {
		t.Fatalf("legacy record: %s %v", got.Body, err)
	}
	// Updating it encrypts it.
	if _, err := docs.Update(ctx, d, "alpha", "client", "OLD", "x", func(m *map[string]any) error { (*m)["k"] = 1; return nil }); err != nil {
		t.Fatal(err)
	}
	raw, _ := mem.Get(ctx, "alpha", "client", "OLD")
	if bytes.Contains(raw.Body, []byte("OLDPN1234X")) {
		t.Fatal("record stayed plaintext after update")
	}
}

func TestFilesAreEncrypted(t *testing.T) {
	ctx := context.Background()
	mem := ingest.NewMemRawStore()
	r := &Raw{Inner: mem, V: New(Ephemeral())}
	data := []byte("PAN card scan " + pan)
	f, err := r.Put(ctx, ingest.RawFile{Tenant: "alpha", Source: "CLIENT-DOC", Name: "pan.pdf", SHA256: "ab12", Content: data})
	if err != nil || f.Size != int64(len(data)) {
		t.Fatalf("put: %+v %v", f, err)
	}
	stored, _ := mem.Get(ctx, "alpha", f.ID)
	if bytes.Contains(stored.Content, []byte(pan)) || stored.Size != int64(len(data)) {
		t.Fatalf("file stored in clear or wrong size (%d)", stored.Size)
	}
	got, err := r.Get(ctx, "alpha", f.ID)
	if err != nil || !bytes.Equal(got.Content, data) || got.Size != int64(len(data)) {
		t.Fatalf("get: %v", err)
	}
	// Same file again is still recognised as a duplicate.
	f2, _ := r.Put(ctx, ingest.RawFile{Tenant: "alpha", Source: "CLIENT-DOC", Name: "pan again.pdf", SHA256: "ab12", Content: data})
	if f2.PriorID != f.ID {
		t.Fatalf("duplicate not detected: %+v", f2)
	}
}

func TestAuditChain(t *testing.T) {
	ctx := context.Background()
	mem := app.NewMemAuditStore()
	a := &Audit{Inner: mem, V: New(Ephemeral())}
	mem.Log(ctx, app.AuditEvent{Tenant: "alpha", At: time.Now(), Actor: "old", Action: "before.sealing"})
	at := time.Date(2026, 10, 18, 10, 0, 0, 123456789, time.UTC)
	for i, act := range []string{"client.created", "payout.approved", "client.reveal", "bill.corrected"} {
		if err := a.Log(ctx, app.AuditEvent{Tenant: "alpha", At: at.Add(time.Duration(i) * time.Minute), Actor: "farhan", Action: act, Object: "BH1001", Detail: map[string]any{"amount": 125000.5, "n": i, "note": "<ok> & fine"}}); err != nil {
			t.Fatal(err)
		}
		a.Log(ctx, app.AuditEvent{Tenant: "beta", At: at, Actor: "x", Action: "other.firm"})
	}
	r, err := a.VerifyAudit(ctx, "alpha")
	if err != nil || !r.OK || r.Sealed != 4 || r.Unsealed != 1 || r.LastHash == "" {
		t.Fatalf("clean chain: %+v %v", r, err)
	}
	if r2, _ := a.VerifyAudit(ctx, "beta"); !r2.OK || r2.Sealed != 4 {
		t.Fatalf("beta chain: %+v", r2)
	}

	// edit copies alpha's entries, applies a database-level change and
	// verifies the result.
	edit := func(fn func(evs []app.AuditEvent)) AuditCheck {
		t.Helper()
		evs, _ := mem.Scan(ctx, "alpha", 0, 0)
		cp := append([]app.AuditEvent(nil), evs...)
		fn(cp)
		m := app.NewMemAuditStore()
		for _, e := range cp {
			if e.Tenant != "" {
				_ = m.Log(ctx, e) // Log stores Prev and Hash as given, like a raw INSERT
			}
		}
		got, err := (&Audit{Inner: m, V: a.V}).VerifyAudit(ctx, "alpha")
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := edit(func(e []app.AuditEvent) { e[2].Detail = map[string]any{"amount": 9.5, "n": 1, "note": "<ok> & fine"} }); got.OK || !strings.Contains(got.Problems[0].Issue, "changed") {
		t.Fatalf("edited amount not caught: %+v", got)
	}
	if got := edit(func(e []app.AuditEvent) { e[3].Actor = "someone.else" }); got.OK {
		t.Fatal("edited actor not caught")
	}
	if got := edit(func(e []app.AuditEvent) { copy(e[2:], e[3:]); e[len(e)-1] = app.AuditEvent{} }); got.OK || !strings.Contains(got.Problems[0].Issue, "link broken") {
		t.Fatalf("deleted entry not caught: %+v", got)
	}
	if got := edit(func(e []app.AuditEvent) {
		e[len(e)-1] = app.AuditEvent{Tenant: "alpha", At: at, Actor: "intruder", Action: "inserted"}
	}); got.OK {
		t.Fatal("unsealed entry after sealing began not caught")
	}
}

func TestParseKeyring(t *testing.T) {
	for _, bad := range []string{"", "1", "x:abc", "1:notbase64!", "1:" + strings.Repeat("A", 8), NewKey(1) + "," + NewKey(1)[:2] + NewKey(9)[2:]} {
		if _, err := ParseKeyring(bad, ""); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	k, err := ParseKeyring(NewKey(3)+", "+NewKey(2), "")
	if err != nil || k.Current() != 3 || len(k.Versions()) != 2 {
		t.Fatalf("%v %v", k, err)
	}
}
