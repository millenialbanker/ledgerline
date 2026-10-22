package backup

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/app"
	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/vault"
)

const T = "acme"

var now = time.Date(2026, 10, 23, 16, 30, 0, 0, time.UTC)

// filled returns encrypted in-memory stores with some data, including a
// file big enough to span several frames.
func filled(t *testing.T, v *vault.Vault) app.Stores {
	t.Helper()
	ctx := context.Background()
	st := vault.Protect(app.MemoryStores(), v)
	for i := 0; i < 50; i++ {
		if err := docs.Upsert(ctx, st.Docs, T, "client", "C"+string(rune('A'+i%26))+string(rune('0'+i/26)), map[string]any{"name": "Client", "n": i}, "maker"); err != nil {
			t.Fatal(err)
		}
	}
	big := make([]byte, 3*chunkSize+123)
	rand.Read(big)
	if _, err := st.Raw.Put(ctx, ingest.RawFile{Tenant: T, Source: "NSE-CM-TRADE", Name: "big.bin", SHA256: strings.Repeat("a", 64), Content: big}); err != nil {
		t.Fatal(err)
	}
	if err := st.Audit.Log(ctx, app.AuditEvent{Tenant: T, At: now, Actor: "maker", Action: "client.saved", Object: "CA0"}); err != nil {
		t.Fatal(err)
	}
	// Another firm's data must stay out of acme's backup.
	if err := docs.Upsert(ctx, st.Docs, "other", "client", "X1", map[string]any{"name": "Other"}, "x"); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestRoundTrip(t *testing.T) {
	v := vault.New(vault.Ephemeral())
	st := filled(t, v)
	var buf bytes.Buffer
	m, err := Write(context.Background(), &buf, st.Snap, v, Manifest{ID: NewID("2026-10-23", now), Tenant: T, Date: "2026-10-23", Tiers: []string{Daily}, Trigger: "test", By: "t"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if m.Rows["doc"] != 50 || m.Rows["raw_file"] != 1 || m.Rows["audit_log"] != 1 || m.Total != 52 {
		t.Fatalf("rows %v total %d", m.Rows, m.Total)
	}
	if bytes.Contains(buf.Bytes(), []byte("Client")) || bytes.Contains(buf.Bytes(), []byte("client.saved")) {
		t.Fatal("backup contains readable data")
	}
	if bytes.Contains(buf.Bytes(), []byte("Other")) {
		t.Fatal("another firm's data is in the backup")
	}

	// Read back and restore into empty stores: the same data comes out.
	fresh := vault.Protect(app.MemoryStores(), v)
	var recs []app.Record
	got, err := Read(bytes.NewReader(buf.Bytes()), v, func(r app.Record) error { recs = append(recs, r); return nil })
	if err != nil || got.SHA256 != m.SHA256 || got.Total != 52 {
		t.Fatalf("read: %v %+v", err, got)
	}
	i := 0
	n, err := fresh.Snap.Restore(context.Background(), T, func() (app.Record, bool, error) {
		if i == len(recs) {
			return app.Record{}, false, nil
		}
		i++
		return recs[i-1], true, nil
	})
	if err != nil || n["doc"] != 50 {
		t.Fatalf("restore %v %v", n, err)
	}
	var c map[string]any
	if c, _, err = docs.Get[map[string]any](context.Background(), fresh.Docs, T, "client", "CB0"); err != nil || c["n"] != float64(1) {
		t.Fatalf("restored client %v %v", c, err)
	}
	f, err := fresh.Raw.Get(context.Background(), T, "RF-00000001")
	if err != nil || len(f.Content) != 3*chunkSize+123 {
		t.Fatalf("restored file %d %v", len(f.Content), err)
	}
	// A second restore into the same firm is refused.
	if _, err := fresh.Snap.Restore(context.Background(), T, func() (app.Record, bool, error) { return app.Record{}, false, nil }); err == nil {
		t.Fatal("restored over existing data")
	}
}

func TestTamperingIsCaught(t *testing.T) {
	v := vault.New(vault.Ephemeral())
	st := filled(t, v)
	mk := func(id string) []byte {
		var b bytes.Buffer
		if _, err := Write(context.Background(), &b, st.Snap, v, Manifest{ID: id, Tenant: T, Date: "2026-10-23", Tiers: []string{Daily}}, now); err != nil {
			t.Fatal(err)
		}
		return b.Bytes()
	}
	a, b := mk("2026-10-23-163000-aaaaaa"), mk("2026-10-23-163000-bbbbbb")
	headEnd := bytes.IndexByte(a[len(magic):], '\n') + len(magic) + 1
	flip := append([]byte(nil), a...)
	flip[len(flip)/2] ^= 1
	swapped := append(append([]byte(nil), b[:headEnd]...), a[headEnd:]...) // a's frames under b's header
	cases := map[string][]byte{
		"flipped byte":    flip,
		"cut short":       a[:len(a)-40],
		"end missing":     a[:len(a)/2],
		"extra data":      append(append([]byte(nil), a...), 'x'),
		"frames swapped":  swapped,
		"not a backup":    []byte("hello"),
		"header replaced": bytes.Replace(a, []byte(`"tenant":"acme"`), []byte(`"tenant":"acmf"`), 1),
	}
	for name, data := range cases {
		if _, err := Read(bytes.NewReader(data), v, nil); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := Read(bytes.NewReader(a), vault.New(vault.Ephemeral()), nil); err == nil || !strings.Contains(err.Error(), "keys") {
		t.Errorf("other keys: %v", err)
	}
	if _, err := Read(bytes.NewReader(a), v, nil); err != nil {
		t.Fatalf("good file: %v", err)
	}
}

func TestDirCreateListVerify(t *testing.T) {
	v := vault.New(vault.Ephemeral())
	st := filled(t, v)
	d := Dir{Path: t.TempDir()}
	e, err := d.Create(context.Background(), st.Snap, v, Manifest{ID: NewID("2026-10-23", now), Tenant: T, Date: "2026-10-23", Tiers: []string{Daily, Weekly}, Trigger: "after EOD close", By: "system"}, now)
	if err != nil || e.Verified == "" || e.Size == 0 {
		t.Fatalf("create %+v %v", e, err)
	}
	es, _ := d.List(T)
	if len(es) != 1 || es[0].ID != e.ID || !es[0].Has(Weekly) {
		t.Fatalf("list %+v", es)
	}
	path, _ := d.PathOf(T, e.ID)
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("file mode %v", st.Mode())
	}
	if m, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "*.partial")); len(m) > 0 {
		t.Fatalf("partial files left: %v", m)
	}
	// Damage the file: verification fails and the listing says so.
	data, _ := os.ReadFile(path)
	data[len(data)-10] ^= 0xff
	os.WriteFile(path, data, 0o600)
	if _, err := d.Verify(T, e.ID, v, now); err == nil {
		t.Fatal("damaged file verified")
	}
	es, _ = d.List(T)
	if es[0].Verified != "" || es[0].VerifyError == "" {
		t.Fatalf("listing after damage %+v", es[0])
	}
	// A missing file shows up too.
	os.Remove(path)
	es, _ = d.List(T)
	if !strings.Contains(es[0].VerifyError, "missing") {
		t.Fatalf("missing file %+v", es[0])
	}
	if _, err := d.List("../etc"); err == nil {
		t.Fatal("bad firm id accepted")
	}
	if err := d.Delete(T, "../../x"); err == nil {
		t.Fatal("bad id accepted")
	}
}

func TestPrune(t *testing.T) {
	var es []Entry
	day := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 400; i++ {
		d := day.AddDate(0, 0, i)
		if d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
			continue
		}
		e := Entry{Manifest: Manifest{ID: d.Format("2006-01-02") + "-200000-000000", Date: d.Format("2006-01-02"), Created: d.Format(time.RFC3339), Tiers: []string{Daily}}, Verified: "yes"}
		if d.Weekday() == time.Friday {
			e.Tiers = append(e.Tiers, Weekly)
		}
		if next := d.AddDate(0, 0, 1); next.Month() != d.Month() && d.Month()%3 == 0 || d.Month()%3 == 0 && d.AddDate(0, 0, 3).Month() != d.Month() && d.Weekday() == time.Friday {
			e.Tiers = append(e.Tiers, Quarterly)
		}
		es = append(es, e)
	}
	// A failed backup today, and a superseded one.
	es = append(es, Entry{Manifest: Manifest{ID: "2027-02-04-210000-ffffff", Date: "2027-02-04", Created: "2027-02-04T21:00:00Z", Tiers: []string{Daily}}, VerifyError: "boom"})
	es = append(es, Entry{Manifest: Manifest{ID: "2027-02-04-190000-eeeeee", Date: "2027-02-04", Created: "2027-02-04T19:00:00Z", Tiers: []string{Daily}}, Verified: "yes"})
	k := Keep{Daily: 20, Weekly: 8, Quarterly: 4, Yearly: 10, Manual: 5}
	drop := Prune(es, k)
	dropped := map[string]bool{}
	for _, e := range drop {
		dropped[e.ID] = true
	}
	kept := map[string]int{}
	for _, e := range es {
		if dropped[e.ID] {
			continue
		}
		for _, t := range e.Tiers {
			kept[t]++
		}
	}
	if kept[Weekly] < 8 || kept[Quarterly] < 4 || kept[Daily] < 20 {
		t.Fatalf("kept %v", kept)
	}
	// Daily kept = 20 newest + older ones kept only for weekly/quarterly.
	total := len(es) - len(drop)
	if total > 20+8+4+1 {
		t.Fatalf("kept %d backups", total)
	}
	if dropped["2027-02-04-190000-eeeeee"] {
		t.Fatal("dropped the good backup of a day whose newer backup failed")
	}
	if !dropped["2027-02-04-210000-ffffff"] {
		t.Fatal("kept a failed backup next to a good one of the same day")
	}
	// Nothing good at all: nothing is deleted.
	if d := Prune([]Entry{{Manifest: Manifest{ID: "x", Date: "2026-01-01", Tiers: []string{Daily}}, VerifyError: "x"}}, k); len(d) != 0 {
		t.Fatalf("dropped the only (failed) backup: %v", d)
	}
	b, _ := json.Marshal(k)
	if !strings.Contains(string(b), `"quarterly":4`) {
		t.Fatal(string(b))
	}
}
