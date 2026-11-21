//go:build integration && pgx

package pgstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
	"time"

	"ledgerline/internal/app"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// TestIntegrationPurge closes one firm and requires every row of it gone,
// the other firm untouched, the purge refused without an approved exit,
// and the application role unable to delete anything itself.
//
//	LEDGERLINE_TEST_DB               application role
//	LEDGERLINE_TEST_EXIT_TENANT      a firm that exists (its row is deleted by the test)
//	LEDGERLINE_TEST_EXIT_OTHER       another firm that exists
func TestIntegrationPurge(t *testing.T) {
	dsn, tn, other := os.Getenv("LEDGERLINE_TEST_DB"), os.Getenv("LEDGERLINE_TEST_EXIT_TENANT"), os.Getenv("LEDGERLINE_TEST_EXIT_OTHER")
	if dsn == "" || tn == "" || other == "" {
		t.Skip("set LEDGERLINE_TEST_DB, LEDGERLINE_TEST_EXIT_TENANT and LEDGERLINE_TEST_EXIT_OTHER")
	}
	ctx := context.Background()
	st, err := Open(ctx, "pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer st.DB.Close()
	ds := st.DocStore()
	key := time.Now().Format("150405.000000")
	for _, x := range []string{tn, other} {
		if _, err := ds.Put(ctx, x, "client", "C"+key, json.RawMessage(`{"ucc":"C1"}`), 0, true, "t"); err != nil {
			t.Fatal(err)
		}
		body := []byte("exit test " + x + " " + key)
		sum := sha256.Sum256(body)
		if _, err := st.Put(ctx, ingest.RawFile{Tenant: x, Source: "X", Name: "f.txt", SHA256: hex.EncodeToString(sum[:]), Content: body}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := st.Append(ctx, ledger.Entry{Tenant: x, Key: "k" + key, ValueDate: "2026-11-20", Narration: "n", CreatedBy: "t", Source: ledger.SourceRef{Kind: "T", ID: "1"},
			Postings: []ledger.Posting{{Account: "CASH:BANK", Debit: money.Amount(100)}, {Account: "CLIENT:C1", Credit: money.Amount(100)}}}); err != nil {
			t.Fatal(err)
		}
		_ = st.Log(ctx, app.AuditEvent{Tenant: x, At: time.Now(), Actor: "t", Action: "a"})
	}
	count := func(x string) int {
		n := 0
		_ = st.Snapshot(ctx, x, func(app.Record) error { n++; return nil })
		return n
	}
	otherBefore := count(other)
	// The application role cannot delete.
	if err := st.tx(ctx, tn, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM ll.doc WHERE tenant_id = $1`, tn)
		return err
	}); err == nil {
		t.Fatal("the application role deleted documents")
	}
	now := time.Now()
	id := "EX-" + key
	r := app.ExitRecord{Tenant: tn, ID: id, State: app.ExitRequested, RequestedBy: "admin", RequestedAt: now}
	if err := st.SaveExit(ctx, r); err != nil {
		t.Fatal(err)
	}
	finish := func(n map[string]int) (app.ExitRecord, error) {
		d := r
		d.State, d.Deleted = app.ExitPurged, n
		return d, nil
	}
	if _, err := st.PurgeTenant(ctx, tn, id, now, finish); err == nil {
		t.Fatal("purged without approval")
	}
	// Approved in Go's eyes but not in the database's (cooling-off ends
	// later by the database clock): the database refuses.
	r.State, r.ApprovedBy, r.CoolingOffEnds, r.ExportID, r.ExportSHA256, r.ExportDownloadedAt = app.ExitApproved, "co", now.Add(time.Hour), "DX-1", "ab", now
	_ = st.SaveExit(ctx, r)
	if _, err := st.PurgeTenant(ctx, tn, id, now.Add(2*time.Hour), finish); err == nil {
		t.Fatal("the database allowed a purge in the cooling-off")
	}
	r.CoolingOffEnds = now.Add(-time.Hour)
	_ = st.SaveExit(ctx, r)
	out, err := st.PurgeTenant(ctx, tn, id, now, finish)
	if err != nil {
		t.Fatal(err)
	}
	if out.Deleted["doc"] < 1 || out.Deleted["raw_file"] < 1 || out.Deleted["posting"] < 2 || out.Deleted["tenant"] != 1 {
		t.Fatalf("deleted %v", out.Deleted)
	}
	if n := count(tn); n != 0 {
		t.Fatalf("%d rows of the purged firm left", n)
	}
	if count(other) != otherBefore {
		t.Fatal("the other firm changed")
	}
	if got, err := st.GetExit(ctx, tn, id); err != nil || got.State != app.ExitPurged {
		t.Fatalf("exit record after the purge: %v", err)
	}
}
