//go:build integration && pgx

package pgstore

import (
	"bytes"
	"context"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/app"
	"ledgerline/internal/backup"
	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ledger"
	"ledgerline/internal/vault"
)

// TestIntegrationBackupRestore runs the sample day on PostgreSQL, backs the
// firm up, restores it into a second, empty database as that database's
// owner (not a superuser), and requires every row to come back identical,
// the audit seals to hold, and new work to carry on without clashing with
// restored numbers.
//
//	LEDGERLINE_TEST_DB             application role, source database
//	LEDGERLINE_TEST_BACKUP_TENANT  firm that exists and is empty there
//	LEDGERLINE_TEST_RESTORE_DB     owner role, empty target database
func TestIntegrationBackupRestore(t *testing.T) {
	src, dst := os.Getenv("LEDGERLINE_TEST_DB"), os.Getenv("LEDGERLINE_TEST_RESTORE_DB")
	tenant := os.Getenv("LEDGERLINE_TEST_BACKUP_TENANT")
	if src == "" || dst == "" || tenant == "" {
		t.Skip("set LEDGERLINE_TEST_DB, LEDGERLINE_TEST_RESTORE_DB and LEDGERLINE_TEST_BACKUP_TENANT")
	}
	ctx := context.Background()
	a, err := Open(ctx, "pgx", src)
	if err != nil {
		t.Fatal(err)
	}
	defer a.DB.Close()
	v := vault.New(vault.Ephemeral())
	if spec := os.Getenv("LEDGERLINE_DATA_KEYS"); spec != "" {
		kr, err := vault.ParseKeyring(spec, "test")
		if err != nil {
			t.Fatal(err)
		}
		v = vault.New(kr)
	}
	opsDay(t, ctx, vault.Protect(a.Stores(), v), tenant)

	var buf bytes.Buffer
	m, err := backup.Write(ctx, &buf, a, v, backup.Manifest{ID: backup.NewID("2026-10-16", time.Now()), Tenant: tenant, Date: "2026-10-16", Tiers: []string{backup.Daily}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if out := os.Getenv("LL_BACKUP_OUT"); out != "" {
		os.WriteFile(out, buf.Bytes(), 0o600)
	}
	t.Logf("backup: %d rows, %d bytes of rows, %d bytes on disk, %v", m.Total, m.Bytes, buf.Len(), m.Rows)
	for _, tb := range []string{"tenant", "account", "raw_file", "trade_event", "recon_run", "journal_entry", "posting", "audit_log", "doc", "doc_history"} {
		if m.Rows[tb] == 0 {
			t.Errorf("no %s rows backed up", tb)
		}
	}

	b, err := Open(ctx, "pgx", dst)
	if err != nil {
		t.Fatal(err)
	}
	defer b.DB.Close()
	var recs []app.Record
	if _, err := backup.Read(bytes.NewReader(buf.Bytes()), v, func(r app.Record) error { recs = append(recs, r); return nil }); err != nil {
		t.Fatal(err)
	}
	feed := func() func() (app.Record, bool, error) {
		i := 0
		return func() (app.Record, bool, error) {
			if i == len(recs) {
				return app.Record{}, false, nil
			}
			i++
			return recs[i-1], true, nil
		}
	}
	n, err := b.Restore(ctx, tenant, feed())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("restored %v", n)
	// Never over existing data.
	if _, err := b.Restore(ctx, tenant, feed()); err == nil || !strings.Contains(err.Error(), "already has") {
		t.Fatalf("second restore: %v", err)
	}

	// Every row identical.
	dump := func(s *Store) []string {
		var out []string
		if err := s.Snapshot(ctx, tenant, func(r app.Record) error { out = append(out, r.Table+" "+string(r.Row)); return nil }); err != nil {
			t.Fatal(err)
		}
		sort.Strings(out)
		return out
	}
	x, y := dump(a), dump(b)
	if len(x) != len(y) {
		t.Fatalf("source %d rows, restored %d", len(x), len(y))
	}
	for i := range x {
		if x[i] != y[i] {
			t.Fatalf("row differs:\n  source:   %.300s\n  restored: %.300s", x[i], y[i])
		}
	}

	// Seals hold, and the restored data opens with the firm's keys.
	chk, err := (&vault.Audit{Inner: b.AuditStore(), V: v}).VerifyAudit(ctx, tenant)
	if err != nil || !chk.OK || chk.Sealed == 0 {
		t.Fatalf("audit after restore: %+v %v", chk, err)
	}
	rs := vault.Protect(b.Stores(), v)
	ds, err := rs.Docs.List(ctx, tenant, "client", "")
	if err != nil || len(ds) == 0 {
		t.Fatalf("clients after restore: %d %v", len(ds), err)
	}

	// New work carries on: counters were moved past the restored numbers.
	if err := docs.Upsert(ctx, rs.Docs, tenant, "client", ds[0].ID, map[string]any{"name": "changed after restore"}, "admin"); err != nil {
		t.Fatalf("document update after restore: %v", err)
	}
	if _, err := rs.Raw.Put(ctx, ingest.RawFile{Tenant: tenant, Source: "TEST", Name: "after.csv", SHA256: strings.Repeat("b", 64), Content: []byte("x")}); err != nil {
		t.Fatalf("file after restore: %v", err)
	}
	if err := rs.Audit.Log(ctx, app.AuditEvent{Tenant: tenant, At: time.Now(), Actor: "admin", Action: "after.restore"}); err != nil {
		t.Fatalf("audit after restore: %v", err)
	}
	led := ledger.New(rs.Ledger)
	if _, _, err := led.Post(ctx, ledger.Entry{Tenant: tenant, Key: "after-restore", ValueDate: "2026-10-19", Narration: "after restore", Source: ledger.SourceRef{Kind: "TEST", ID: "1"}, CreatedBy: "admin",
		Postings: []ledger.Posting{{Account: ledger.BankClient, Debit: 100}, {Account: ledger.ClientAccount("BH1001"), Credit: 100}}}); err != nil {
		t.Fatalf("journal entry after restore: %v", err)
	}
	if chk, _ := (&vault.Audit{Inner: b.AuditStore(), V: v}).VerifyAudit(ctx, tenant); !chk.OK {
		t.Fatalf("audit chain broken after new entries: %+v", chk.Problems)
	}
}
