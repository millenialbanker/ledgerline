package ops

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/app"
	"ledgerline/internal/backup"
	"ledgerline/internal/dataexport"
	"ledgerline/internal/docs"
)

// exitSetup is the sample day with exports built in the foreground, a work
// folder and a backup folder of the test's own, and a clock the test moves.
func exitSetup(t *testing.T) (*Ops, context.Context, *time.Time) {
	t.Helper()
	o, ctx := setup(t)
	// The clock starts now (the development signing certificate is made
	// for the real time) and the test moves it.
	now := time.Now().In(ist).Truncate(time.Second)
	o.Now = func() time.Time { return now }
	o.WorkDir, o.ExportSync = t.TempDir(), true
	o.Backups, o.Sealer, o.BackupSync = &backup.Dir{Path: t.TempDir()}, o.vaultAudit().V, true
	return o, ctx, &now
}

func memSnap(t *testing.T, o *Ops) *app.MemSnapshot {
	t.Helper()
	m, ok := o.App.Snap.(*app.MemSnapshot)
	if !ok {
		t.Fatal("the test needs the in-memory stores")
	}
	return m
}

// download makes a link for actor and reads the whole export.
func download(t *testing.T, o *Ops, ctx context.Context, actor, id string) []byte {
	t.Helper()
	l, err := o.DataExportLink(ctx, T, actor, id)
	if err != nil {
		t.Fatal(err)
	}
	tok := l.URL[strings.Index(l.URL, "?t=")+3:]
	_, rc, err := o.OpenDataExport(ctx, T, actor, id, tok)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	// The link works once.
	if _, _, err := o.OpenDataExport(ctx, T, actor, id, tok); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a used link worked again: %v", err)
	}
	return b
}

func jsonLines(t *testing.T, b []byte) []map[string]json.RawMessage {
	t.Helper()
	var out []map[string]json.RawMessage
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("not JSON Lines: %v", err)
		}
		out = append(out, m)
	}
	return out
}

func TestDataExportApprovalAndContents(t *testing.T) {
	o, ctx, _ := exitSetup(t)
	// Only an Admin asks.
	if _, err := o.RequestDataExport(ctx, T, "vikram.shah", ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("compliance asked for an export: %v", err)
	}
	x, err := o.RequestDataExport(ctx, T, "admin", "moving to another back office")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.RequestDataExport(ctx, T, "admin", ""); !errors.Is(err, ErrState) {
		t.Fatalf("a second export while one waits: %v", err)
	}
	// The one who asked cannot approve; an Ops maker cannot either.
	if _, err := o.DecideDataExport(ctx, T, "admin", x.ID, true, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("the requester approved: %v", err)
	}
	if _, err := o.DecideDataExport(ctx, T, "priya.nair", x.ID, true, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("an ops maker approved: %v", err)
	}
	if _, err := o.DecideDataExport(ctx, T, "vikram.shah", x.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	x, _, _ = docs.Get[DataExport](ctx, o.Docs, T, cDataExport, x.ID)
	if x.State != DXReady || x.ManifestSHA256 == "" || x.Size == 0 {
		t.Fatalf("export not ready: %+v", x)
	}
	// Only an Admin downloads.
	if _, err := o.DataExportLink(ctx, T, "vikram.shah", x.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("compliance made a download link: %v", err)
	}
	// The file at rest is encrypted.
	p, _ := o.exportPath(T, x.ID)
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("manifest.json")) || bytes.Contains(raw, []byte("PK\x03\x04")) {
		t.Fatal("the export is stored in clear")
	}
	b := download(t, o, ctx, "admin", x.ID)
	if int64(len(b)) != x.Size {
		t.Fatalf("downloaded %d bytes, recorded %d", len(b), x.Size)
	}
	v, err := dataexport.Verify(bytes.NewReader(b), int64(len(b)), x.PublicKey)
	if err != nil {
		t.Fatalf("the export does not verify: %v", err)
	}
	if v.ManifestSHA256 != x.ManifestSHA256 {
		t.Fatal("manifest hash differs from the one recorded")
	}
	m := v.Manifest
	if m.Signature.Method != "cms" || m.Tenant != T || m.Final {
		t.Fatalf("manifest: %+v", m.Signature)
	}
	file := func(name string) []byte {
		t.Helper()
		f, err := dataexport.ReadFile(bytes.NewReader(b), int64(len(b)), name)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	// Every collection, every document, decrypted.
	snap := memSnap(t, o)
	want := map[string]int{}
	for _, d := range snap.Docs.ExportTenant(T) {
		want[d.Collection]++
	}
	for coll, n := range m.Collections {
		if n > want[coll] {
			t.Errorf("collection %s: %d exported, %d stored", coll, n, want[coll])
		}
		lines := jsonLines(t, file("collections/"+coll+".jsonl"))
		if len(lines) != n {
			t.Errorf("collection %s: %d lines, manifest says %d", coll, len(lines), n)
		}
	}
	for coll := range want {
		if _, ok := m.Collections[coll]; !ok && coll != cDataExport && coll != cWorkItem && coll != cSeq {
			t.Errorf("collection %s is missing from the export", coll)
		}
	}
	us := jsonLines(t, file("collections/"+cUser+".jsonl"))
	found := false
	for _, l := range us {
		var u User
		if err := json.Unmarshal(l["doc"], &u); err != nil {
			t.Fatal(err)
		}
		if u.ID == "vikram.shah" && u.Role == RoleCompliance {
			found = true
		}
	}
	if !found {
		t.Fatal("users are not decrypted in the export")
	}
	// Every stored file with its original bytes.
	idx := jsonLines(t, file("files/index.jsonl"))
	raws := snap.Raw.ExportTenant(T)
	if len(raws) == 0 || len(idx) != len(raws) || m.StoredFiles != len(raws) {
		t.Fatalf("files: %d stored, %d in the index", len(raws), len(idx))
	}
	for _, l := range idx {
		var e struct{ ID, Path, SHA256 string }
		json.Unmarshal(l["id"], &e.ID)
		json.Unmarshal(l["path"], &e.Path)
		json.Unmarshal(l["sha256"], &e.SHA256)
		f, err := o.App.Ingest.Raw.Get(ctx, T, e.ID)
		if err != nil {
			t.Fatal(err)
		}
		got := file(e.Path)
		s := sha256.Sum256(got)
		if !bytes.Equal(got, f.Content) || hex.EncodeToString(s[:]) != f.SHA256 || e.SHA256 != f.SHA256 {
			t.Fatalf("file %s differs from the stored one", e.ID)
		}
	}
	// The audit trail with its seals; the seal keys only in a final export.
	al := jsonLines(t, file("audit/audit-trail.jsonl"))
	if len(al) != m.Tables["audit_log"] || !m.Audit.Checked || !m.Audit.OK || m.Audit.SealKeys {
		t.Fatalf("audit: %d lines, %+v", len(al), m.Audit)
	}
	if _, err := dataexport.ReadFile(bytes.NewReader(b), int64(len(b)), "audit/seal-keys.json"); err == nil {
		t.Fatal("seal keys in an ordinary export")
	}
	// CSV for the main tables.
	cl, err := csv.NewReader(bytes.NewReader(file("csv/clients.csv"))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(cl)-1 != m.Collections[cClient] || cl[0][0] != "id" {
		t.Fatalf("clients.csv has %d rows for %d clients", len(cl)-1, m.Collections[cClient])
	}
	le, err := csv.NewReader(bytes.NewReader(file("csv/ledger-entries.csv"))).ReadAll()
	if err != nil || len(le) < 2 {
		t.Fatalf("ledger-entries.csv: %d rows, %v", len(le), err)
	}
	if _, err := csv.NewReader(bytes.NewReader(file("csv/trades.csv"))).ReadAll(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(file("README.txt")), "CHECKING THE EXPORT") {
		t.Fatal("no README")
	}
	// A changed file fails the check.
	zr, _ := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	for _, name := range []string{"README.txt", "manifest.json.p7s"} {
		for _, f := range zr.File {
			if f.Name != name {
				continue
			}
			off, _ := f.DataOffset()
			bad := append([]byte(nil), b...)
			bad[off+int64(f.CompressedSize64)/2] ^= 0x55
			if _, err := dataexport.Verify(bytes.NewReader(bad), int64(len(bad)), x.PublicKey); err == nil {
				t.Fatalf("an archive with %s changed verified", name)
			}
		}
	}
	// The download is in the audit trail.
	ev, _ := o.App.Audit.List(ctx, T, 50)
	seen := false
	for _, e := range ev {
		if e.Action == "dataexport.downloaded" && e.Object == x.ID && e.Actor == "admin" {
			seen = true
		}
	}
	if !seen {
		t.Fatal("download not audited")
	}
}

func TestDataExportAnotherAdminApproves(t *testing.T) {
	o, ctx, now := exitSetup(t)
	if err := o.PutUser(ctx, T, "admin", User{ID: "admin.two", Name: "Second Admin", Role: RoleAdmin, Active: true}); err != nil {
		t.Fatal(err)
	}
	x, err := o.RequestDataExport(ctx, T, "admin", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.DecideDataExport(ctx, T, "admin.two", x.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	// A link expires.
	l, err := o.DataExportLink(ctx, T, "admin", x.ID)
	if err != nil {
		t.Fatal(err)
	}
	*now = now.Add(16 * time.Minute)
	if _, _, err := o.OpenDataExport(ctx, T, "admin", x.ID, l.URL[strings.Index(l.URL, "?t=")+3:]); !errors.Is(err, ErrForbidden) {
		t.Fatalf("an expired link worked: %v", err)
	}
	// Another Admin cannot use a link made for someone else.
	l, _ = o.DataExportLink(ctx, T, "admin", x.ID)
	if _, _, err := o.OpenDataExport(ctx, T, "admin.two", x.ID, l.URL[strings.Index(l.URL, "?t=")+3:]); !errors.Is(err, ErrForbidden) {
		t.Fatalf("another Admin used the link: %v", err)
	}
	// After the keep period the file is deleted.
	*now = now.AddDate(0, 0, DataExportKeepDays+1)
	o.exitTick(ctx, T, *now)
	x, _, _ = docs.Get[DataExport](ctx, o.Docs, T, cDataExport, x.ID)
	p, _ := o.exportPath(T, x.ID)
	if _, err := os.Stat(p); x.State != DXDeleted || err == nil {
		t.Fatalf("expired export kept: %s", x.State)
	}
}

func TestPurgeRefusedWithoutApprovedExit(t *testing.T) {
	o, ctx, now := exitSetup(t)
	finish := func(n map[string]int) (app.ExitRecord, error) { t.Fatal("purged"); return app.ExitRecord{}, nil }
	if _, err := o.App.Purge.PurgeTenant(ctx, T, "EX-none", *now, finish); err == nil {
		t.Fatal("purged without an exit record")
	}
	base := app.ExitRecord{Tenant: T, ID: "EX-1", State: app.ExitApproved, RequestedBy: "admin", ApprovedBy: "vikram.shah", CoolingOffEnds: now.Add(-time.Hour),
		ExportID: "DX-1", ExportSHA256: "ab", ExportDownloadedAt: now.Add(-time.Minute)}
	for name, r := range map[string]app.ExitRecord{
		"requested only":       func(r app.ExitRecord) app.ExitRecord { r.State = app.ExitRequested; return r }(base),
		"cancelled":            func(r app.ExitRecord) app.ExitRecord { r.State = app.ExitCancelled; return r }(base),
		"approved by the same": func(r app.ExitRecord) app.ExitRecord { r.ApprovedBy = r.RequestedBy; return r }(base),
		"in cooling-off":       func(r app.ExitRecord) app.ExitRecord { r.CoolingOffEnds = now.Add(time.Hour); return r }(base),
		"export not taken":     func(r app.ExitRecord) app.ExitRecord { r.ExportDownloadedAt = time.Time{}; return r }(base),
	} {
		if err := o.App.Exits.SaveExit(ctx, r); err != nil {
			t.Fatal(err)
		}
		if _, err := o.App.Purge.PurgeTenant(ctx, T, r.ID, *now, finish); !errors.Is(err, app.ErrState) {
			t.Errorf("%s: purge not refused: %v", name, err)
		}
	}
	// Another firm's approved exit does not let this firm be purged.
	other := base
	other.Tenant = "other"
	_ = o.App.Exits.SaveExit(ctx, other)
	base.State = app.ExitCancelled
	_ = o.App.Exits.SaveExit(ctx, base)
	if _, err := o.App.Purge.PurgeTenant(ctx, T, "EX-1", *now, finish); err == nil {
		t.Fatal("purged on another firm's exit")
	}
	if len(memSnap(t, o).Docs.ExportTenant(T)) == 0 {
		t.Fatal("data gone")
	}
}

func TestExitCancelDuringCoolingOff(t *testing.T) {
	o, ctx, now := exitSetup(t)
	r, err := o.RequestExit(ctx, T, "admin", T, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.DecideExit(ctx, T, "vikram.shah", r.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(3 * 24 * time.Hour)
	if _, err := o.CancelExit(ctx, T, "priya.nair", r.ID, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("an ops maker cancelled: %v", err)
	}
	if _, err := o.CancelExit(ctx, T, "admin", r.ID, "staying"); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(10 * 24 * time.Hour)
	if o.exitTick(ctx, T, *now) {
		t.Fatal("a cancelled exit still holds the firm's work")
	}
	xs, _ := docs.List[DataExport](ctx, o.Docs, T, cDataExport, "")
	if len(xs) != 0 {
		t.Fatal("a final export was made for a cancelled exit")
	}
	if _, err := o.PurgeFirm(ctx, T, "admin", r.ID, T); !errors.Is(err, app.ErrState) {
		t.Fatalf("purged a cancelled exit: %v", err)
	}
	// A new request can be made after a cancel.
	if _, err := o.RequestExit(ctx, T, "admin", T, ""); err != nil {
		t.Fatal(err)
	}
}

func TestExitPurgeAndCertificate(t *testing.T) {
	o, ctx, now := exitSetup(t)
	if err := o.SeedDev(ctx, "other"); err != nil {
		t.Fatal(err)
	}
	snap := memSnap(t, o)
	otherBefore := len(snap.Docs.ExportTenant("other")) + len(snap.Audit.ExportTenant("other"))
	if _, err := o.BackupNow(ctx, T, "admin"); err != nil {
		t.Fatal(err)
	}
	if es, _ := o.Backups.List(T); len(es) != 1 {
		t.Fatal("no backup")
	}
	if _, err := o.RequestExit(ctx, T, "admin", "wrong", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("closing without typing the firm's code: %v", err)
	}
	if _, err := o.RequestExit(ctx, T, "vikram.shah", T, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("compliance asked to close: %v", err)
	}
	r, err := o.RequestExit(ctx, T, "admin", T, "the firm is merging")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.DecideExit(ctx, T, "admin", r.ID, true, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("the requester approved: %v", err)
	}
	if _, err := o.DecideExit(ctx, T, "farhan.khan", r.ID, true, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("an ops checker approved: %v", err)
	}
	r, err = o.DecideExit(ctx, T, "vikram.shah", r.ID, true, "")
	if err != nil {
		t.Fatal(err)
	}
	if want := now.UTC().Add(7 * 24 * time.Hour); !r.CoolingOffEnds.Equal(want) {
		t.Fatalf("cooling-off ends %v, want %v", r.CoolingOffEnds, want)
	}
	if _, err := o.PurgeFirm(ctx, T, "admin", r.ID, T); !errors.Is(err, app.ErrState) {
		t.Fatalf("purged in the cooling-off: %v", err)
	}
	if o.exitTick(ctx, T, *now) {
		t.Fatal("the firm's work stopped during the cooling-off")
	}
	// The cooling-off ends: the final export is made and nothing else runs.
	*now = now.Add(7*24*time.Hour + time.Minute)
	if !o.exitTick(ctx, T, *now) {
		t.Fatal("the firm's work goes on after the cooling-off")
	}
	r, _ = o.App.Exits.GetExit(ctx, T, r.ID)
	x, _, err := docs.Get[DataExport](ctx, o.Docs, T, cDataExport, r.ExportID)
	if err != nil || x.State != DXReady || !x.Final || r.ExportSHA256 != x.ManifestSHA256 {
		t.Fatalf("final export: %+v %v", x, err)
	}
	if _, err := o.PurgeFirm(ctx, T, "admin", r.ID, T); !errors.Is(err, app.ErrState) {
		t.Fatalf("purged before the final export was downloaded: %v", err)
	}
	b := download(t, o, ctx, "admin", x.ID)
	if _, err := dataexport.Verify(bytes.NewReader(b), int64(len(b)), ""); err != nil {
		t.Fatal(err)
	}
	// The final export carries the seal keys: every seal checks.
	kb, err := dataexport.ReadFile(bytes.NewReader(b), int64(len(b)), "audit/seal-keys.json")
	if err != nil {
		t.Fatal(err)
	}
	var keys struct{ Keys map[string]string }
	json.Unmarshal(kb, &keys)
	ab, _ := dataexport.ReadFile(bytes.NewReader(b), int64(len(b)), "audit/audit-trail.jsonl")
	prev, n := "", 0
	for _, l := range jsonLines(t, ab) {
		var e struct {
			At, Actor, Action, Object, Prev, Hash string
			Detail                                map[string]any
		}
		raw, _ := json.Marshal(l)
		json.Unmarshal(raw, &e)
		at, _ := time.Parse(time.RFC3339Nano, e.At)
		c, _ := app.AuditCanonical(app.AuditEvent{Tenant: T, At: at, Actor: e.Actor, Action: e.Action, Object: e.Object, Detail: e.Detail, Prev: e.Prev})
		ver, mac, _ := strings.Cut(e.Hash, ":")
		k, _ := hex.DecodeString(keys.Keys[ver])
		h := hmac.New(sha256.New, k)
		h.Write(c)
		if hex.EncodeToString(h.Sum(nil)) != mac || e.Prev != prev {
			t.Fatalf("seal %d does not check with the exported key", n)
		}
		prev = e.Hash
		n++
	}
	if n == 0 {
		t.Fatal("no audit trail")
	}
	r, _ = o.App.Exits.GetExit(ctx, T, r.ID)
	if r.ExportDownloadedAt.IsZero() {
		t.Fatal("the download is not on the exit")
	}
	// Admin only, with the firm's code typed.
	if _, err := o.PurgeFirm(ctx, T, "vikram.shah", r.ID, T); !errors.Is(err, ErrForbidden) {
		t.Fatalf("compliance deleted the data: %v", err)
	}
	if _, err := o.PurgeFirm(ctx, T, "admin", r.ID, "nope"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("deleted without the firm's code: %v", err)
	}
	want := map[string]int{"doc": len(snap.Docs.ExportTenant(T)), "raw_file": len(snap.Raw.ExportTenant(T)), "audit_log": len(snap.Audit.ExportTenant(T)),
		"journal_entry": len(snap.Ledger.ExportTenant(T)), "trade_event": len(snap.Trades.ExportTenant(T))}
	done, err := o.PurgeFirm(ctx, T, "admin", r.ID, T)
	if err != nil {
		t.Fatal(err)
	}
	for k, n := range want {
		if done.Exit.Deleted[k] != n || n == 0 && k != "trade_event" {
			t.Errorf("%s: certificate says %d deleted, there were %d", k, done.Exit.Deleted[k], n)
		}
	}
	// Everything of the firm is gone; the other firm is untouched.
	if n := len(snap.Docs.ExportTenant(T)) + len(snap.Raw.ExportTenant(T)) + len(snap.Audit.ExportTenant(T)) + len(snap.Ledger.ExportTenant(T)) +
		len(snap.Trades.ExportTenant(T)) + len(snap.Breaks.ExportTenant(T)); n != 0 {
		t.Fatalf("%d rows of the firm left", n)
	}
	if after := len(snap.Docs.ExportTenant("other")) + len(snap.Audit.ExportTenant("other")); after != otherBefore {
		t.Fatalf("the other firm changed: %d rows, was %d", after, otherBefore)
	}
	if es, _ := o.Backups.List(T); len(es) != 0 {
		t.Fatal("the firm's backups are still on disk")
	}
	if d, _ := o.firmWorkDir(T); func() bool { _, err := os.Stat(d); return err == nil }() {
		t.Fatal("the firm's work folder is still there")
	}
	if _, err := os.Stat(filepath.Join(o.Backups.Path, T)); err == nil {
		t.Fatal("the firm's backup folder is still there")
	}
	// The certificate: signed, recorded with its hash outside the firm,
	// downloadable from the firm's link.
	if _, whole, err := VerifyPDF(done.Certificate); err != nil || !whole {
		t.Fatalf("certificate signature: %v", err)
	}
	s := sha256.Sum256(done.Certificate)
	recs, _ := o.ExitRecords(ctx)
	if len(recs) != 1 || recs[0].State != app.ExitPurged || recs[0].CertificateSHA256 != hex.EncodeToString(s[:]) || recs[0].Certificate != nil || recs[0].BackupsGoneBy == "" {
		t.Fatalf("operator record: %+v", recs)
	}
	if !strings.Contains(recs[0].BackupsGoneBy, "confirmed by the operator") {
		t.Fatalf("backups age out by %q with no retention set", recs[0].BackupsGoneBy)
	}
	tok := done.CertificateURL[strings.Index(done.CertificateURL, "?t=")+3:]
	if c, err := o.ExitCertificate(ctx, T, r.ID, tok); err != nil || !bytes.Equal(c, done.Certificate) {
		t.Fatalf("certificate link: %v", err)
	}
	if _, err := o.ExitCertificate(ctx, T, r.ID, tok+"0"); err == nil {
		t.Fatal("a wrong link gave the certificate")
	}
	if c, err := o.OperatorCertificate(ctx, T, r.ID); err != nil || !bytes.Equal(c, done.Certificate) {
		t.Fatal("the operator cannot issue the certificate again")
	}
	// Nothing runs or is written for the firm any more, and it cannot be
	// purged twice.
	if !o.FirmClosed(T) || !o.exitTick(ctx, T, *now) {
		t.Fatal("the purged firm still runs")
	}
	if n, err := o.Tick(ctx, T, *now); n != 0 || err != nil || len(snap.Docs.ExportTenant(T)) != 0 {
		t.Fatal("the scheduler wrote to a purged firm")
	}
	if _, err := o.App.Purge.PurgeTenant(ctx, T, r.ID, *now, func(map[string]int) (app.ExitRecord, error) { return app.ExitRecord{}, nil }); !errors.Is(err, app.ErrState) {
		t.Fatalf("purged twice: %v", err)
	}
	r.State = app.ExitApproved
	if err := o.App.Exits.SaveExit(ctx, r); !errors.Is(err, app.ErrState) {
		t.Fatalf("a purged exit was reopened: %v", err)
	}
}

func TestExitCertificateBackupDate(t *testing.T) {
	o, _, now := exitSetup(t)
	o.BackupCopiesDays = 35
	if got := o.backupsGoneBy(*now); got != now.AddDate(0, 0, 35).Format("02 Jan 2006") {
		t.Fatalf("backups age out by %s", got)
	}
}
