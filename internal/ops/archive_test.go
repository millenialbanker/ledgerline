package ops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/app"
	"ledgerline/internal/dataexport"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// Made-up firm, clients and figures only.

// arcFirm is a firm that went live from a migration (with migrated history
// of 2024-25 and 2025-26) and has its own files, trades and entries of
// 2024-25, some still needed by open items.
func arcFirm(t *testing.T) (*Ops, context.Context, map[string]string) {
	t.Helper()
	o, ctx := migSetup(t)
	o.Now = func() time.Time { return time.Date(2026, 11, 20, 11, 0, 0, 0, ist) }
	o.ArchiveDir, o.ArchiveSync = t.TempDir(), true
	p := checked(t, o, ctx, mkPkg(t, mspec{masters: true, history: true, docs: true, checks: goodChecks("2501.00", "")}), "office.llmig")
	m, err := o.ProposeMigration(ctx, MT, "adm.one", MigProposal{Package: p.ID, Firm: "01"})
	if err != nil {
		t.Fatal(err)
	}
	if m, err = o.DecideMigration(ctx, MT, "chk.one", m.ID, true, mPass, ""); err != nil || m.Status != MigLoaded {
		t.Fatalf("migration %v %s %s", err, m.Status, m.Error)
	}
	ids := map[string]string{}
	// stored files received in 2024-25
	raw := o.App.Snap.(*app.MemSnapshot).Raw
	raw.Now = func() time.Time { return time.Date(2024, 6, 10, 18, 0, 0, 0, ist) }
	put := func(key, src, body string) {
		sum := sha256.Sum256([]byte(body))
		f, err := o.App.Ingest.Raw.Put(ctx, ingest.RawFile{Tenant: MT, Source: src, Name: key + ".txt", SHA256: hex.EncodeToString(sum[:]), Content: []byte(body)})
		if err != nil {
			t.Fatal(err)
		}
		ids[key] = f.ID
	}
	put("F1", "NSE-CM-TRADE", "made-up trade file one")
	put("F2", "NSE-CM-TRADE", "made-up trade file two")
	put("F3", "NSE-BHAV", "made-up price file")
	put("D1", "DISPATCH", "made-up contract note dispatch")
	raw.Now = time.Now
	trade := func(no, ucc, date, file string) ingest.TradeEvent {
		return ingest.TradeEvent{Tenant: MT, Origin: ingest.FromExchange, Exchange: "NSE", Segment: "CM", TradeNo: no, UCC: ucc, Symbol: "TESTCO", InstrumentType: "EQ", Side: "B", Qty: 10, Price: money.MustDec("100"), TradeDate: date, FileID: file}
	}
	if _, _, err := o.App.Trades.Add(ctx, []ingest.TradeEvent{trade("T1", "TB002", "2024-06-10", ids["F1"]), trade("T2", "TA001", "2024-08-01", ids["F2"])}); err != nil {
		t.Fatal(err)
	}
	post := func(key, date string, ps ...ledger.Posting) string {
		e, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: MT, Key: key, ValueDate: date, Narration: "made-up " + key, Source: ledger.SourceRef{Kind: "MANUAL", ID: key}, Postings: ps, CreatedBy: "adm.two"})
		if err != nil {
			t.Fatal(err)
		}
		return e.ID
	}
	dr := func(a, v string) ledger.Posting { return ledger.Posting{Account: a, Debit: money.MustAmount(v)} }
	cr := func(a, v string) ledger.Posting { return ledger.Posting{Account: a, Credit: money.MustAmount(v)} }
	ids["e1"] = post("bill-1", "2024-06-10", dr("CLIENT:TB002", "1000.00"), cr("INCOME:BROKERAGE", "1000.00"))
	ids["e2"] = post("rcpt-1", "2024-07-01", dr("BANK:CLIENT", "1000.00"), cr("CLIENT:TB002", "1000.00"))
	// TA001 owes money (the migration's opening debit): its rows stay live
	ids["e3"] = post("bill-2", "2024-08-01", dr("CLIENT:TA001", "500.00"), cr("INCOME:BROKERAGE", "500.00"))
	// an entry reversed after the period stays live with its reversal
	ids["e4"] = post("rcpt-2", "2024-09-01", dr("BANK:CLIENT", "300.00"), cr("CLIENT:TC003", "300.00"))
	r, _, err := o.App.Ledger.Reverse(ctx, MT, ids["e4"], "adm.two", "wrong client", "2025-05-01")
	if err != nil {
		t.Fatal(err)
	}
	ids["e5"] = r.ID
	ids["e6"] = post("bill-3", "2025-06-01", dr("CLIENT:TB002", "200.00"), cr("INCOME:BROKERAGE", "200.00"))
	ids["e7"] = post("rcpt-3", "2025-06-05", dr("BANK:CLIENT", "200.00"), cr("CLIENT:TB002", "200.00"))
	return o, ctx, ids
}

// books is everything a later period's reports show.
type books struct {
	all, tbEnd, tbNow, stmt, stmtAll, income string
}

func readBooks(t *testing.T, o *Ops, ctx context.Context) books {
	t.Helper()
	var b books
	bal, err := o.App.Ledger.Balances(ctx, MT, "")
	if err != nil {
		t.Fatal(err)
	}
	b.all = fmt.Sprint(bal)
	for _, x := range []struct {
		d string
		p *string
	}{{"2025-03-31", &b.tbEnd}, {"2026-11-20", &b.tbNow}} {
		tb, err := o.App.Ledger.TrialBalance(ctx, MT, x.d)
		if err != nil || !tb.Balanced {
			t.Fatalf("trial balance %s %v", x.d, err)
		}
		*x.p = fmt.Sprint(tb.Rows, tb.TotalDebit, tb.TotalCredit)
	}
	st, err := o.StaffStatement(ctx, MT, "TB002", "2025-04-01", "2026-11-20")
	if err != nil {
		t.Fatal(err)
	}
	lines := []string{}
	for _, l := range st.Lines {
		lines = append(lines, l.EntryID+" "+l.ValueDate+" "+l.Debit.String()+" "+l.Credit.String()+" "+l.Balance.String())
	}
	b.stmt = fmt.Sprint(st.Opening, lines, st.Closing)
	sa, err := o.ClientStatement(ctx, MT, "TB002", "", "2026-11-20")
	if err != nil {
		t.Fatal(err)
	}
	b.stmtAll = sa.Closing.String()
	in, err := o.App.Ledger.Statement(ctx, MT, "INCOME:BROKERAGE", "2025-04-01", "2026-11-20")
	if err != nil {
		t.Fatal(err)
	}
	b.income = fmt.Sprint(in.Opening, len(in.Lines), in.Closing)
	return b
}

func TestArchivePeriodAndRestore(t *testing.T) {
	o, ctx, ids := arcFirm(t)
	before := readBooks(t, o, ctx)
	f1, _ := o.App.Ingest.Raw.Get(ctx, MT, ids["F1"])
	e1, _ := o.App.Ledger.Store.Get(ctx, MT, ids["e1"])

	// the current year, and a period not over, are refused
	if p, err := o.PreviewArchive(ctx, MT, "adm.one", ArchiveAsk{FYs: []string{"2026-27"}}); err != nil || len(p.Blockers) == 0 || !strings.Contains(p.Blockers[0], "current financial year") {
		t.Fatalf("current year: %v %+v", err, p.Blockers)
	}
	if _, err := o.PreviewArchive(ctx, MT, "adm.one", ArchiveAsk{FYs: []string{"2022-23", "2024-25"}}); err == nil {
		t.Fatal("years with a gap accepted")
	}
	if _, err := o.PreviewArchive(ctx, MT, "mkr.one", ArchiveAsk{FYs: []string{"2024-25"}}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker previewed: %v", err)
	}

	p, err := o.PreviewArchive(ctx, MT, "comp.one", ArchiveAsk{FYs: []string{"2024-25"}})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, k := range p.Kinds {
		got[k.Kind] = k.Rows
	}
	if len(p.Blockers) != 0 || p.From != "2024-04-01" || p.To != "2025-03-31" || p.RetainUntil != "2033-03-31" ||
		got[app.ArcLedger] != 2 || got[app.ArcTrades] != 1 || got[app.ArcFiles] != 2 || got[app.ArcDocuments] != 1 || got[app.ArcMigrated] == 0 || got[app.ArcDocHistory] != 0 {
		t.Fatalf("preview %+v", p)
	}
	if p.Bytes <= 0 || p.Saving < 0 || !strings.Contains(strings.Join(p.Kept, ";"), "unpaid debit") {
		t.Fatalf("preview figures %+v", p)
	}

	// asked by one Admin, approved by another person
	if _, err := o.RequestArchive(ctx, MT, "comp.one", ArchiveAsk{FYs: []string{"2024-25"}}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("compliance asked: %v", err)
	}
	r, err := o.RequestArchive(ctx, MT, "adm.one", ArchiveAsk{FYs: []string{"2024-25"}, Reason: "closed year"})
	if err != nil || r.State != app.ArcRequested {
		t.Fatalf("request %v %+v", err, r)
	}
	if _, err := o.DecideArchive(ctx, MT, "adm.one", r.ID, true, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("own archive approved: %v", err)
	}
	if _, err := o.RequestArchive(ctx, MT, "adm.two", ArchiveAsk{From: "2025-01-01", To: "2025-02-28"}); err == nil || !strings.Contains(err.Error(), "overlapping") {
		t.Fatalf("an overlapping request: %v", err)
	}
	r, err = o.DecideArchive(ctx, MT, "comp.one", r.ID, true, "")
	if err != nil || r.State != app.ArcArchived {
		t.Fatalf("archive %v %s %s", err, r.State, r.Error)
	}
	if r.PackageSHA256 == "" || r.VerifiedAt.IsZero() || len(r.CarryEntries) != 1 || r.Rows["journal_entry"] != 2 || r.PublicKey == "" || r.Clients == 0 {
		t.Fatalf("record %+v", r)
	}
	path := filepath.Join(o.ArchiveDir, MT, r.ID+".llar")
	if st, err := os.Stat(path); err != nil || st.Size() != r.PackageBytes {
		t.Fatalf("package on archive storage: %v", err)
	}

	// every later balance, statement and report is the same
	if after := readBooks(t, o, ctx); after != before {
		t.Fatalf("books changed by archiving:\n%+v\n%+v", before, after)
	}
	if _, err := o.App.Ledger.Store.Get(ctx, MT, ids["e1"]); err == nil {
		t.Fatal("an archived entry is still live")
	}
	for _, k := range []string{"e3", "e4", "e5", "e6"} {
		if _, err := o.App.Ledger.Store.Get(ctx, MT, ids[k]); err != nil {
			t.Fatalf("%s should stay live: %v", k, err)
		}
	}
	carry, _ := o.App.Ledger.Store.Get(ctx, MT, r.CarryEntries[0])
	if carry.ValueDate != "2025-03-31" || carry.Source.Kind != app.CarryKind || len(carry.Postings) != 2 {
		t.Fatalf("carried forward %+v", carry)
	}
	// screens that reach into the period say so
	if _, err := o.App.Ingest.Raw.Get(ctx, MT, ids["F1"]); !errors.Is(err, ErrArchived) || !strings.Contains(err.Error(), "restore it") {
		t.Fatalf("archived file: %v", err)
	}
	if _, err := o.App.Ingest.Raw.Get(ctx, MT, ids["F2"]); err != nil {
		t.Fatalf("a file a live trade needs was archived: %v", err)
	}
	if _, err := o.StaffStatement(ctx, MT, "TB002", "2024-06-01", "2024-12-31"); !errors.Is(err, ErrArchived) {
		t.Fatalf("statement of an archived period: %v", err)
	}
	if st, err := o.StaffStatement(ctx, MT, "TC003", "2024-06-01", "2024-12-31"); err != nil || len(st.Lines) != 1 {
		t.Fatalf("a client with nothing archived: %v %+v", err, st)
	}
	if err := o.ArchivedOverlap(ctx, MT, "2024-06-10", "2024-06-10", ""); !errors.Is(err, ErrArchived) {
		t.Fatalf("trades of an archived day: %v", err)
	}
	if _, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: MT, Key: "late", ValueDate: "2024-12-01", Narration: "made-up", Source: ledger.SourceRef{Kind: "MANUAL", ID: "x"},
		Postings: []ledger.Posting{{Account: "BANK:CLIENT", Debit: 100}, {Account: "INCOME:OTHER", Credit: 100}}, CreatedBy: "adm.two"}); !errors.Is(err, ErrArchived) {
		t.Fatalf("an entry posted into an archived period: %v", err)
	}
	if _, err := o.ClientHistoryTable(ctx, MT, "chk.one", "TA001", "2024-25", "LEDGER", false); !errors.Is(err, ErrArchived) {
		t.Fatalf("archived migrated history: %v", err)
	}
	if tb, err := o.ClientHistoryTable(ctx, MT, "chk.one", "TA001", "2025-26", "LEDGER", false); err != nil || len(tb.Rows) == 0 {
		t.Fatalf("2025-26 history should stay: %v", err)
	}
	// TA001 owes money, so its old documents stay live too
	if _, _, err := o.ArchiveFile(ctx, MT, "chk.one", "TA001", "ST-OLD", false); err != nil {
		t.Fatalf("a kept client's migrated document: %v", err)
	}
	v, err := o.ArchiveScreen(ctx, MT, "comp.one")
	if err != nil || len(v.Index) != 1 || v.Index[0].State != app.ArcArchived || !v.StorageOK {
		t.Fatalf("screen %v %+v", err, v.Index)
	}

	// the full export carries the archive package, which verifies
	var buf bytes.Buffer
	ew := &exportWriter{ar: dataexport.NewArchive(&buf, o.now())}
	if n, err := o.exportArchives(ctx, MT, ew); err != nil || n != 1 {
		t.Fatalf("export archives %d %v", n, err)
	}
	si, fn := dataexport.Ed25519Signer()
	if _, err := ew.ar.Finish(dataexport.Manifest{ExportID: "x"}, si, fn); err != nil {
		t.Fatal(err)
	}
	inner, err := dataexport.ReadFile(bytes.NewReader(buf.Bytes()), int64(buf.Len()), "archives/"+r.ID+".zip")
	if err != nil {
		t.Fatal(err)
	}
	if vr, err := dataexport.VerifyKind(bytes.NewReader(inner), int64(len(inner)), r.PublicKey, dataexport.PeriodKind); err != nil || vr.ManifestSHA256 != r.PackageSHA256 {
		t.Fatalf("the exported package does not verify: %v", err)
	}

	// restore: asked by an Admin, approved by a second person
	if _, err := o.RequestRestore(ctx, MT, "adm.one", r.ID, ""); err == nil {
		t.Fatal("a restore without a reason")
	}
	if r, err = o.RequestRestore(ctx, MT, "adm.one", r.ID, "inspection asks for 2024-25"); err != nil || r.State != app.ArcRestoreRequested {
		t.Fatalf("restore request %v %s", err, r.State)
	}
	if _, err := o.DecideRestore(ctx, MT, "adm.one", r.ID, true, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("own restore approved: %v", err)
	}
	if r, err = o.DecideRestore(ctx, MT, "comp.one", r.ID, true, ""); err != nil || r.State != app.ArcRestored {
		t.Fatalf("restore %v %s %s", err, r.State, r.Error)
	}
	if after := readBooks(t, o, ctx); after != before {
		t.Fatalf("books changed by the restore:\n%+v\n%+v", before, after)
	}
	if back, err := o.App.Ledger.Store.Get(ctx, MT, ids["e1"]); err != nil || back.Seq != e1.Seq || back.Fingerprint() != e1.Fingerprint() || !back.CreatedAt.Equal(e1.CreatedAt) {
		t.Fatalf("restored entry %v %+v", err, back)
	}
	if _, err := o.App.Ledger.Store.Get(ctx, MT, r.CarryEntries[0]); err == nil {
		t.Fatal("the carried-forward entry is still there")
	}
	if g, err := o.App.Ingest.Raw.Get(ctx, MT, ids["F1"]); err != nil || !bytes.Equal(g.Content, f1.Content) || g.SHA256 != f1.SHA256 {
		t.Fatalf("restored file %v", err)
	}
	if st, err := o.StaffStatement(ctx, MT, "TB002", "2024-06-01", "2024-12-31"); err != nil || len(st.Lines) != 2 {
		t.Fatalf("restored statement %v %+v", err, st)
	}
	if _, err := o.ClientHistoryTable(ctx, MT, "chk.one", "TA001", "2024-25", "LEDGER", false); err != nil {
		t.Fatalf("restored migrated history: %v", err)
	}
	if _, _, err := o.ArchiveFile(ctx, MT, "chk.one", "TA001", "ST-OLD", false); err != nil {
		t.Fatalf("restored migrated document: %v", err)
	}

	// archive it again, then a damaged package is refused on restore and
	// nothing changes
	again, err := o.ArchiveAgain(ctx, MT, "adm.two", r.ID)
	if err != nil || again.Again != r.ID {
		t.Fatalf("again %v %+v", err, again)
	}
	if again, err = o.DecideArchive(ctx, MT, "comp.one", again.ID, true, ""); err != nil || again.State != app.ArcArchived {
		t.Fatalf("archive again %v %s %s", err, again.State, again.Error)
	}
	if after := readBooks(t, o, ctx); after != before {
		t.Fatal("books changed by archiving again")
	}
	p2 := filepath.Join(o.ArchiveDir, MT, again.ID+".llar")
	b, _ := os.ReadFile(p2)
	b[len(b)/2] ^= 0xff
	if err := os.WriteFile(p2, b, 0o600); err != nil {
		t.Fatal(err)
	}
	o.RequestRestore(ctx, MT, "adm.two", again.ID, "test the check")
	again, err = o.DecideRestore(ctx, MT, "comp.one", again.ID, true, "")
	if err != nil || again.State != app.ArcArchived || !strings.Contains(again.Error, "restore") {
		t.Fatalf("damaged package: %v %s %q", err, again.State, again.Error)
	}
	if _, err := o.App.Ledger.Store.Get(ctx, MT, ids["e1"]); err == nil {
		t.Fatal("a damaged package was restored")
	}
	if after := readBooks(t, o, ctx); after != before {
		t.Fatal("books changed by a refused restore")
	}
}

// TestArchiveStoreRefusals: the store removes nothing without an approved,
// verified record, nothing outside the period, and nothing that would change
// a balance.
func TestArchiveStoreRefusals(t *testing.T) {
	o, ctx, ids := arcFirm(t)
	ps := o.App.Periods
	r := app.ArchiveRecord{Tenant: MT, ID: "AR-20261120-110000-abcd", State: app.ArcRequested, From: "2024-04-01", To: "2025-03-31", RequestedBy: "adm.one", Kinds: app.ArchiveKinds}
	if err := ps.SaveArchive(ctx, r); err != nil {
		t.Fatal(err)
	}
	refs := []app.RowRef{{Table: "journal_entry", Key: ids["e1"]}, {Table: "journal_entry", Key: ids["e2"]}}
	keep := func(map[string]int, []ledger.Entry) (app.ArchiveRecord, error) {
		x := r
		x.State = app.ArcArchived
		return x, nil
	}
	if _, err := ps.RemovePeriod(ctx, MT, r.ID, refs, nil, keep); err == nil {
		t.Fatal("removed without approval")
	}
	r.State, r.ApprovedBy = app.ArcApproved, "comp.one"
	if err := ps.SaveArchive(ctx, r); err != nil {
		t.Fatal(err)
	}
	r.State, r.PackageSHA256, r.VerifiedAt = app.ArcVerified, strings.Repeat("ab", 32), time.Now()
	if err := ps.SaveArchive(ctx, r); err != nil {
		t.Fatal(err)
	}
	// state cannot go back
	back := r
	back.State = app.ArcRequested
	if err := ps.SaveArchive(ctx, back); err == nil {
		t.Fatal("state moved back")
	}
	// an entry outside the period
	if _, err := ps.RemovePeriod(ctx, MT, r.ID, append(refs, app.RowRef{Table: "journal_entry", Key: ids["e6"]}), nil, keep); err == nil {
		t.Fatal("removed an entry outside the period")
	}
	// without the carried-forward entries the balances would change
	if _, err := ps.RemovePeriod(ctx, MT, r.ID, refs, nil, keep); err == nil || !strings.Contains(err.Error(), "balance") {
		t.Fatalf("removed without carrying forward: %v", err)
	}
	if _, err := o.App.Ledger.Store.Get(ctx, MT, ids["e1"]); err != nil {
		t.Fatal("a refused removal removed an entry")
	}
	net := map[string]money.Amount{"INCOME:BROKERAGE": money.MustAmount("-1000.00"), "BANK:CLIENT": money.MustAmount("1000.00")}
	carry := app.CarryEntries(MT, r.ID, r.From, r.To, "comp.one", net)
	out, err := ps.RemovePeriod(ctx, MT, r.ID, refs, carry, keep)
	if err != nil || out.State != app.ArcArchived {
		t.Fatalf("remove %v", err)
	}
	// a restore needs a second person's approval
	if _, err := ps.RestorePeriod(ctx, MT, r.ID, func() (app.Record, bool, error) { return app.Record{}, false, nil }, nil); err == nil {
		t.Fatal("restored without approval")
	}
}

func TestSelectEntriesAndCarry(t *testing.T) {
	sel := app.ArchiveSelection{From: "2024-04-01", To: "2025-03-31", ExcludeUCC: map[string]bool{"X1": true}}
	heads := []app.EntryHead{
		{ID: "A", ValueDate: "2024-05-01", Accounts: []string{"CLIENT:C1", "INCOME:B"}},
		{ID: "B", ValueDate: "2024-05-02", Accounts: []string{"CLIENT:X1", "INCOME:B"}},
		{ID: "C", ValueDate: "2024-05-03", Accounts: []string{"BANK:B", "CLIENT:C2"}},
		{ID: "D", ValueDate: "2024-05-04", Accounts: []string{"BANK:B", "CLIENT:C2"}, ReversalOf: "C"},
		{ID: "E", ValueDate: "2024-05-05", Accounts: []string{"BANK:B", "CLIENT:C3"}},
		{ID: "G", ValueDate: "2025-03-31", SourceKind: app.CarryKind, Accounts: []string{"BANK:B", "INCOME:B"}},
	}
	// E is reversed by F, after the period
	got := app.SelectEntries(heads, map[string]string{"D": "C", "F": "E"}, sel)
	if !got["A"] || got["B"] || !got["C"] || !got["D"] || got["E"] || got["G"] || len(got) != 3 {
		t.Fatalf("selected %v", got)
	}
	net := map[string]money.Amount{}
	for i := 0; i < 12000; i++ {
		net[fmt.Sprintf("CLIENT:C%05d", i)] = money.Amount(100)
	}
	net["BANK:B"] = money.Amount(-1200000)
	es := app.CarryEntries("t", "AR-1", "2024-04-01", "2025-03-31", "a", net)
	if len(es) != 3 {
		t.Fatalf("%d carried-forward entries", len(es))
	}
	var sum money.Amount
	for _, e := range es {
		if err := e.Validate(); err != nil || e.ValueDate != "2025-03-31" {
			t.Fatalf("carried forward %v", err)
		}
		for _, p := range e.Postings {
			if p.Account == app.CarryAccount {
				sum += p.Debit - p.Credit
			}
		}
	}
	if sum != 0 {
		t.Fatalf("the balancing account nets to %s", sum)
	}
}
