package app

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"ledgerline/internal/ingest"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

func TestMemPurgeOneTenantAllOrNothing(t *testing.T) {
	ctx := context.Background()
	s := MemoryStores()
	snap := s.Purge.(*MemSnapshot)
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	for _, tn := range []string{"alpha", "beta"} {
		if _, err := s.Docs.Put(ctx, tn, "client", "C1", json.RawMessage(`{"ucc":"C1"}`), 0, true, "t"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Raw.Put(ctx, ingest.RawFile{Tenant: tn, Source: "X", Name: "f.txt", SHA256: "aa", Content: []byte("x")}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.Ledger.Append(ctx, ledger.Entry{Tenant: tn, Key: "k1", ValueDate: "2026-11-20", Narration: "n", CreatedBy: "t", Source: ledger.SourceRef{Kind: "T", ID: "1"},
			Postings: []ledger.Posting{{Account: "CASH:BANK", Debit: money.Amount(100)}, {Account: "CLIENT:C1", Credit: money.Amount(100)}}}); err != nil {
			t.Fatal(err)
		}
		_ = s.Audit.Log(ctx, AuditEvent{Tenant: tn, At: now, Actor: "t", Action: "a"})
	}
	r := ExitRecord{Tenant: "alpha", ID: "EX-1", State: ExitApproved, RequestedBy: "admin", ApprovedBy: "co", CoolingOffEnds: now.Add(-time.Hour),
		ExportID: "DX-1", ExportSHA256: "ab", ExportDownloadedAt: now}
	if err := s.Exits.SaveExit(ctx, r); err != nil {
		t.Fatal(err)
	}
	// A failing finish puts everything back.
	if _, err := snap.PurgeTenant(ctx, "alpha", "EX-1", now, func(map[string]int) (ExitRecord, error) { return ExitRecord{}, errors.New("no certificate") }); err == nil {
		t.Fatal("purge succeeded with a failed certificate")
	}
	if len(snap.Docs.ExportTenant("alpha")) != 1 || len(snap.Raw.ExportTenant("alpha")) != 1 || len(snap.Ledger.ExportTenant("alpha")) != 1 || len(snap.Audit.ExportTenant("alpha")) != 1 {
		t.Fatal("a failed purge lost data")
	}
	// The record's tenant is the one purged, only.
	if _, err := snap.PurgeTenant(ctx, "beta", "EX-1", now, nil); err == nil {
		t.Fatal("beta purged on alpha's exit")
	}
	var got map[string]int
	out, err := snap.PurgeTenant(ctx, "alpha", "EX-1", now, func(n map[string]int) (ExitRecord, error) {
		got = n
		d := r
		d.State, d.Deleted = ExitPurged, n
		return d, nil
	})
	if err != nil || out.State != ExitPurged {
		t.Fatal(err)
	}
	if got["doc"] != 1 || got["raw_file"] != 1 || got["journal_entry"] != 1 || got["posting"] != 2 || got["audit_log"] != 1 {
		t.Fatalf("counts %v", got)
	}
	if n := len(snap.Docs.ExportTenant("alpha")) + len(snap.Raw.ExportTenant("alpha")) + len(snap.Ledger.ExportTenant("alpha")) + len(snap.Audit.ExportTenant("alpha")); n != 0 {
		t.Fatalf("%d rows left", n)
	}
	if len(snap.Docs.ExportTenant("beta")) != 1 || len(snap.Raw.ExportTenant("beta")) != 1 || len(snap.Ledger.ExportTenant("beta")) != 1 || len(snap.Audit.ExportTenant("beta")) != 1 {
		t.Fatal("beta lost data")
	}
	// Audit sequence numbers are never reused after a purge.
	_ = s.Audit.Log(ctx, AuditEvent{Tenant: "beta", At: now, Actor: "t", Action: "b"})
	evs := snap.Audit.ExportTenant("beta")
	if len(evs) != 2 || evs[1].Seq <= 2 {
		t.Fatalf("sequence reused: %+v", evs)
	}
	if rec, _ := s.Exits.GetExit(ctx, "alpha", "EX-1"); rec.State != ExitPurged {
		t.Fatal("the record is not purged")
	}
}
