//go:build integration && pgx

package pgstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/app"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// TestIntegrationArchive archives a past period of one firm and restores
// it: the database refuses to remove anything without an approved,
// verified record, or outside the period; the removal replaces the
// period's entries with carried-forward ones and every balance stays the
// same; the restore puts the same rows back (ids, sequence numbers, file
// bytes, document versions and their history) and removes the
// carried-forward entry; the application role still cannot delete.
//
//	LEDGERLINE_TEST_DB               application role
//	LEDGERLINE_TEST_ARCHIVE_TENANT   a firm that exists
func TestIntegrationArchive(t *testing.T) {
	dsn, tn := os.Getenv("LEDGERLINE_TEST_DB"), os.Getenv("LEDGERLINE_TEST_ARCHIVE_TENANT")
	if dsn == "" || tn == "" {
		t.Skip("set LEDGERLINE_TEST_DB and LEDGERLINE_TEST_ARCHIVE_TENANT")
	}
	ctx := context.Background()
	st, err := Open(ctx, "pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer st.DB.Close()
	key := time.Now().Format("150405.000000")
	from, to := "2019-04-01", "2020-03-31"
	post := func(k, date string, ps ...ledger.Posting) ledger.Entry {
		e, _, err := st.Append(ctx, ledger.Entry{Tenant: tn, Key: k + key, ValueDate: date, Narration: "made-up " + k, CreatedBy: "it", Source: ledger.SourceRef{Kind: "T", ID: k}, Postings: ps})
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	amt := func(v string) money.Amount { return money.MustAmount(v) }
	e1 := post("bill", "2019-06-01", ledger.Posting{Account: "CLIENT:IA1", Debit: amt("100.00")}, ledger.Posting{Account: "INCOME:ITBROK", Credit: amt("100.00")})
	post("rcpt", "2019-07-01", ledger.Posting{Account: "BANK:IT", Debit: amt("60.00")}, ledger.Posting{Account: "CLIENT:IA1", Credit: amt("60.00")})
	later := post("later", "2020-05-01", ledger.Posting{Account: "BANK:IT", Debit: amt("40.00")}, ledger.Posting{Account: "CLIENT:IA1", Credit: amt("40.00")})
	body := []byte("archive test file " + key)
	sum := sha256.Sum256(body)
	rf, err := st.Put(ctx, ingest.RawFile{Tenant: tn, Source: "MIG-ARCHIVE", Name: "old.pdf", SHA256: hex.EncodeToString(sum[:]), Content: body})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Add(ctx, []ingest.TradeEvent{{Tenant: tn, Origin: ingest.FromExchange, Exchange: "NSE", Segment: "CM", TradeNo: "IT" + key, UCC: "IA1", Symbol: "TESTCO",
		InstrumentType: "EQ", Side: "B", Qty: 1, Price: money.MustDec("10"), TradeDate: "2019-06-01", FileID: rf.ID}}); err != nil {
		t.Fatal(err)
	}
	ds := st.DocStore()
	docID := "IT" + key + "|2019-20|LEDGER|000000"
	for i := 0; i < 2; i++ { // two versions, so the history has two rows
		if _, err := ds.Put(ctx, tn, "mighistc", docID, json.RawMessage(fmt.Sprintf(`{"gz":"dmVyc2lvbiAl%d"}`, i)), 0, true, "it"); err != nil {
			t.Fatal(err)
		}
	}
	docBefore, _ := ds.Get(ctx, tn, "mighistc", docID)
	histCount := func() int {
		n := 0
		_ = st.tx(ctx, tn, func(tx *sql.Tx) error {
			return tx.QueryRowContext(ctx, `SELECT count(*) FROM ll.doc_history WHERE tenant_id = $1 AND coll = 'mighistc' AND id = $2`, tn, docID).Scan(&n)
		})
		return n
	}
	histBefore := histCount()
	balances := func(asOf string) string {
		var m map[string]money.Amount
		_ = st.tx(ctx, tn, func(tx *sql.Tx) error {
			var err error
			m, err = balancesTx(ctx, tx, tn, asOf)
			return err
		})
		return fmt.Sprint(m)
	}
	allBefore, toBefore := balances(""), balances(to)

	id := "AR-" + time.Now().Format("20060102-150405") + "-" + strings.ReplaceAll(key, ".", "")[:6]
	r := app.ArchiveRecord{Tenant: tn, ID: id, State: app.ArcRequested, From: from, To: to, RequestedBy: "admin", Kinds: app.ArchiveKinds, RequestedAt: time.Now()}
	if err := st.SaveArchive(ctx, r); err != nil {
		t.Fatal(err)
	}
	sel := app.ArchiveSelection{From: from, To: to, Kinds: map[string]bool{app.ArcLedger: true, app.ArcTrades: true, app.ArcMigrated: true},
		MigratedDocs: map[string]bool{"mighistc|" + docID: true}, MigratedRaw: map[string]bool{rf.ID: true}}
	var recs []app.Record
	var refs []app.RowRef
	net := map[string]money.Amount{}
	err = st.PeriodRows(ctx, tn, sel, func(row app.ArchiveRow) error {
		recs = append(recs, row.Record)
		if row.Ref.Table != "" {
			refs = append(refs, row.Ref)
		}
		if row.Table == "posting" {
			var p struct {
				Account string `json:"account_code"`
				Dr      int64  `json:"debit_paise"`
				Cr      int64  `json:"credit_paise"`
			}
			if err := json.Unmarshal(row.Row, &p); err != nil {
				return err
			}
			net[p.Account] += money.Amount(p.Dr - p.Cr)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	mine := 0
	myRefs := refs
	for _, x := range refs {
		if x.Key == e1.ID || x.Key == rf.ID || x.Key == "mighistc|"+docID {
			mine++
		}
	}
	if mine != 3 {
		t.Fatalf("period rows: %v", refs)
	}
	finish := func(_ map[string]int, stored []ledger.Entry) (app.ArchiveRecord, error) {
		x := r
		x.State, x.ArchivedAt = app.ArcArchived, time.Now()
		for _, e := range stored {
			x.CarryEntries = append(x.CarryEntries, e.ID)
		}
		return x, nil
	}
	carry := app.CarryEntries(tn, id, from, to, "co", net)
	if _, err := st.RemovePeriod(ctx, tn, id, myRefs, carry, finish); err == nil {
		t.Fatal("removed without approval")
	}
	r.State, r.ApprovedBy = app.ArcApproved, "co"
	if err := st.SaveArchive(ctx, r); err != nil {
		t.Fatal(err)
	}
	r.State, r.PackageSHA256, r.VerifiedAt = app.ArcVerified, strings.Repeat("ab", 32), time.Now()
	if err := st.SaveArchive(ctx, r); err != nil {
		t.Fatal(err)
	}
	// The state cannot go back, by the database's own check.
	back := r
	back.State = app.ArcRequested
	if err := st.SaveArchive(ctx, back); err == nil {
		t.Fatal("the archive's state moved back")
	}
	// An entry outside the period: the database refuses the whole removal.
	if _, err := st.RemovePeriod(ctx, tn, id, append(append([]app.RowRef{}, myRefs...), app.RowRef{Table: "journal_entry", Key: later.ID}), carry, finish); err == nil {
		t.Fatal("removed an entry outside the period")
	}
	if _, err := st.Get(ctx, tn, e1.ID); err != nil {
		t.Fatal("a refused removal removed an entry")
	}
	// The application role cannot call the deletion itself without a record,
	// nor delete directly.
	if err := st.tx(ctx, tn, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM ll.journal_entry WHERE tenant_id = $1 AND id = $2`, tn, e1.ID)
		return err
	}); err == nil {
		t.Fatal("the application role deleted an entry")
	}
	if err := st.tx(ctx, tn, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `SELECT ll.archive_remove($1, 'AR-20200101-000000-0000', 'journal_entry', ARRAY[$2::text])`, tn, e1.ID)
		return err
	}); err == nil {
		t.Fatal("removed under an archive that does not exist")
	}
	out, err := st.RemovePeriod(ctx, tn, id, myRefs, carry, finish)
	if err != nil {
		t.Fatal(err)
	}
	if out.State != app.ArcArchived || len(out.CarryEntries) != 1 {
		t.Fatalf("state %s carried %v", out.State, out.CarryEntries)
	}
	if _, err := st.Get(ctx, tn, e1.ID); err == nil {
		t.Fatal("the archived entry is still live")
	}
	if _, err := ds.Get(ctx, tn, "mighistc", docID); err == nil || histCount() != 0 {
		t.Fatal("the migrated history is still live")
	}
	if a, b := balances(""), balances(to); a != allBefore || b != toBefore {
		t.Fatalf("balances changed:\n%s\n%s\n%s\n%s", allBefore, a, toBefore, b)
	}

	// restore
	r = out
	r.State, r.RestoreRequestedBy = app.ArcRestoreRequested, "admin"
	if err := st.SaveArchive(ctx, r); err != nil {
		t.Fatal(err)
	}
	i := 0
	next := func() (app.Record, bool, error) {
		if i >= len(recs) {
			return app.Record{}, false, nil
		}
		i++
		return recs[i-1], true, nil
	}
	done := func(n map[string]int) (app.ArchiveRecord, error) {
		x := r
		x.State, x.RestoredAt = app.ArcRestored, time.Now()
		return x, nil
	}
	if _, err := st.RestorePeriod(ctx, tn, id, next, done); err == nil {
		t.Fatal("restored without approval")
	}
	r.State, r.RestoreApprovedBy = app.ArcRestoreApproved, "co"
	if err := st.SaveArchive(ctx, r); err != nil {
		t.Fatal(err)
	}
	i = 0
	if _, err := st.RestorePeriod(ctx, tn, id, next, done); err != nil {
		t.Fatal(err)
	}
	got, err := st.Get(ctx, tn, e1.ID)
	if err != nil || got.Seq != e1.Seq || got.Fingerprint() != e1.Fingerprint() {
		t.Fatalf("restored entry %v %+v", err, got)
	}
	f, err := st.GetRaw(ctx, tn, rf.ID)
	if err != nil || string(f.Content) != string(body) {
		t.Fatalf("restored file %v", err)
	}
	if d, err := ds.Get(ctx, tn, "mighistc", docID); err != nil || d.Version != docBefore.Version || string(d.Body) != string(docBefore.Body) || histCount() != histBefore {
		t.Fatalf("restored document %v %d/%d history %d/%d", err, d.Version, docBefore.Version, histCount(), histBefore)
	}
	if a, b := balances(""), balances(to); a != allBefore || b != toBefore {
		t.Fatal("balances changed by the restore")
	}
	for _, c := range out.CarryEntries {
		if _, err := st.Get(ctx, tn, c); err == nil {
			t.Fatal("the carried-forward entry is still there")
		}
	}
}
