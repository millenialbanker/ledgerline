package ops

import (
	"context"
	crand "crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"ledgerline/internal/app"
	"ledgerline/internal/docs"
	"ledgerline/internal/pdf"
	"ledgerline/internal/sign"
)

// ---- closing the account: exit, purge and destruction certificate ----
//
// SEBI's cloud framework asks for a clear expunging clause: when a firm
// leaves, its data is handed back and then destroyed, with a certificate.
// The flow is deliberate and slow on purpose:
//
//  1. An Admin asks to close the account, typing the firm's code to confirm.
//  2. A second person (the compliance officer or another Admin) approves.
//  3. A cooling-off period follows (7 days unless the firm changes it),
//     during which either of them can cancel.
//  4. When it ends, Ledgerline stops the firm's scheduled work and builds
//     the final export (with the audit seal keys). An Admin downloads it.
//  5. Only then can an Admin delete the data, again typing the firm's code.
//     The store deletes every row of the firm (documents, their history,
//     files, ledger, trades, audit trail) in one transaction, and only for
//     a firm whose exit record says all of the above happened; the local
//     backups and work files of the firm are deleted from disk after it.
//  6. The destruction certificate (a signed PDF: what was deleted, when, who
//     approved, the final export's manifest hash, and the date by which
//     copies of backups held elsewhere age out) is kept in the operator's
//     exit record, outside the firm's data, given to the Admin on the spot,
//     emailed to the compliance address when mail is set up, and can be
//     downloaded again from a link only the firm was given.
//
// Once purged, the firm's API calls and scheduled work stop: nothing may
// write the firm's data again.

const (
	cExitCfg = "dataexitcfg"
	// DefaultCoolingOffDays is the waiting time unless the firm sets another.
	DefaultCoolingOffDays = 7
)

// ExitSettings are the firm's choices for closing its account.
type ExitSettings struct {
	CoolingOffDays int    `json:"cooling_off_days"`
	UpdatedBy      string `json:"updated_by,omitempty"`
	UpdatedAt      string `json:"updated_at,omitempty"`
}

func (o *Ops) exitSettings(ctx context.Context, tenant string) ExitSettings {
	s, _, _ := docs.GetOr(ctx, o.Docs, tenant, cExitCfg, "main", ExitSettings{})
	if s.CoolingOffDays <= 0 {
		s.CoolingOffDays = DefaultCoolingOffDays
	}
	return s
}

// SetExitSettings changes the cooling-off period (1 to 90 days). It cannot
// change while an exit is open.
func (o *Ops) SetExitSettings(ctx context.Context, tenant, actor string, days int) (ExitSettings, error) {
	if err := o.require(ctx, tenant, actor, "Changing the cooling-off period", RoleAdmin); err != nil {
		return ExitSettings{}, err
	}
	if days < 1 || days > 90 {
		return ExitSettings{}, invalid("the cooling-off period is 1 to 90 days")
	}
	if r, _ := o.openExit(ctx, tenant); r != nil {
		return ExitSettings{}, fmt.Errorf("%w: an account closing is open; cancel it first", ErrState)
	}
	s := ExitSettings{CoolingOffDays: days, UpdatedBy: actor, UpdatedAt: o.now().Format(time.RFC3339)}
	if err := docs.Upsert(ctx, o.Docs, tenant, cExitCfg, "main", s, actor); err != nil {
		return s, err
	}
	o.log(ctx, tenant, actor, "exit.settings", "cooling-off", map[string]any{"days": days})
	return s, nil
}

// FirmClosed reports whether the firm's data was deleted after it closed
// its account (in this server's run, or found by LoadClosedFirms).
func (o *Ops) FirmClosed(tenant string) bool {
	o.closedMu.Lock()
	defer o.closedMu.Unlock()
	return o.closed[tenant]
}

func (o *Ops) markClosed(tenant string) {
	o.closedMu.Lock()
	defer o.closedMu.Unlock()
	if o.closed == nil {
		o.closed = map[string]bool{}
	}
	o.closed[tenant] = true
}

// LoadClosedFirms reads the operator's exit records at start-up, so firms
// already purged are refused from the first request.
func (o *Ops) LoadClosedFirms(ctx context.Context) error {
	if o.App.Exits == nil {
		return nil
	}
	rs, err := o.App.Exits.ListExits(ctx, "")
	if err != nil {
		return err
	}
	for _, r := range rs {
		if r.State == app.ExitPurged {
			o.markClosed(r.Tenant)
		}
	}
	return nil
}

// openExit is the firm's exit that is asked for or approved, if any.
func (o *Ops) openExit(ctx context.Context, tenant string) (*app.ExitRecord, error) {
	if o.App.Exits == nil {
		return nil, nil
	}
	rs, err := o.App.Exits.ListExits(ctx, tenant)
	if err != nil {
		return nil, err
	}
	for i := len(rs) - 1; i >= 0; i-- {
		if rs[i].State == app.ExitRequested || rs[i].State == app.ExitApproved {
			r := rs[i]
			return &r, nil
		}
	}
	return nil, nil
}

func (o *Ops) exitRecord(ctx context.Context, tenant, id string) (app.ExitRecord, error) {
	if o.App.Exits == nil {
		return app.ExitRecord{}, fmt.Errorf("%w: closing the account is not available on this storage", ErrState)
	}
	r, err := o.App.Exits.GetExit(ctx, tenant, id)
	if errors.Is(err, app.ErrExitNotFound) {
		return r, fmt.Errorf("%w: no account closing %s", ErrNotFound, id)
	}
	return r, err
}

// confirmCode checks the firm's code typed to confirm.
func confirmCode(tenant, typed string) error {
	if !strings.EqualFold(strings.TrimSpace(typed), tenant) {
		return invalid("type the firm's code, %s, to confirm", tenant)
	}
	return nil
}

// RequestExit asks to close the firm's account and delete its data.
func (o *Ops) RequestExit(ctx context.Context, tenant, actor, confirm, reason string) (app.ExitRecord, error) {
	if err := o.require(ctx, tenant, actor, "Asking to close the firm's account", RoleAdmin); err != nil {
		return app.ExitRecord{}, err
	}
	if o.App.Exits == nil || o.App.Purge == nil {
		return app.ExitRecord{}, fmt.Errorf("%w: closing the account is not available on this storage", ErrState)
	}
	if err := confirmCode(tenant, confirm); err != nil {
		return app.ExitRecord{}, err
	}
	if r, err := o.openExit(ctx, tenant); err != nil {
		return app.ExitRecord{}, err
	} else if r != nil {
		return r.Public(), fmt.Errorf("%w: closing the account is already %s (%s)", ErrState, strings.ToLower(r.State), r.ID)
	}
	set, _ := o.Settings(ctx, tenant)
	now := o.now()
	b := make([]byte, 3)
	_, _ = crand.Read(b)
	r := app.ExitRecord{Tenant: tenant, ID: "EX-" + now.Format("20060102-150405") + "-" + hex.EncodeToString(b), Firm: set.Firm, FirmCode: firmCode(set), State: app.ExitRequested,
		RequestedBy: actor, RequestedAt: now.UTC(), CoolingOffDays: o.exitSettings(ctx, tenant).CoolingOffDays}
	if reason = clip(strings.TrimSpace(reason), 300); reason != "" {
		r.Notes = append(r.Notes, "Reason given: "+reason)
	}
	if err := o.App.Exits.SaveExit(ctx, r); err != nil {
		return r, err
	}
	o.log(ctx, tenant, actor, "exit.requested", r.ID, map[string]any{"cooling_off_days": r.CoolingOffDays, "reason": reason})
	o.raiseWork(ctx, tenant, "exit", r.ID, "Approve or refuse closing the firm's Ledgerline account",
		fmt.Sprintf("%s asked to close the firm's Ledgerline account and delete all its data. A second person, the compliance officer or another Admin, approves or refuses under Admin → Data & exit. After approval there are %d days to change your mind.", actor, r.CoolingOffDays),
		"", RoleCompliance, "dataexit", "")
	return r.Public(), nil
}

// DecideExit approves (starting the cooling-off) or refuses closing the
// account. The person who asked cannot approve.
func (o *Ops) DecideExit(ctx context.Context, tenant, actor, id string, approve bool, note string) (app.ExitRecord, error) {
	if err := o.require(ctx, tenant, actor, "Approving closing the firm's account", RoleCompliance); err != nil {
		return app.ExitRecord{}, err
	}
	r, err := o.exitRecord(ctx, tenant, id)
	if err != nil {
		return r, err
	}
	if r.State != app.ExitRequested {
		return r.Public(), fmt.Errorf("%w: closing %s is %s", ErrState, id, strings.ToLower(r.State))
	}
	if r.RequestedBy == actor {
		return r.Public(), fmt.Errorf("%w: a second person approves closing the account, not the one who asked", ErrForbidden)
	}
	note = clip(strings.TrimSpace(note), 300)
	now := o.now()
	if approve {
		role, _ := o.RoleOf(ctx, tenant, actor)
		r.State, r.ApprovedBy, r.ApprovedRole, r.ApprovedAt = app.ExitApproved, actor, role, now.UTC()
		r.CoolingOffEnds = now.UTC().Add(time.Duration(r.CoolingOffDays) * 24 * time.Hour)
	} else {
		if note == "" {
			return r.Public(), invalid("give the reason for refusing")
		}
		r.State, r.CancelledBy, r.CancelledAt = app.ExitCancelled, actor, now.UTC()
	}
	if note != "" {
		r.Notes = append(r.Notes, map[bool]string{true: "Approved: ", false: "Refused: "}[approve]+note)
	}
	if err := o.App.Exits.SaveExit(ctx, r); err != nil {
		return r, err
	}
	o.closeWork(ctx, tenant, actor, "exit", id, map[bool]string{true: "approved", false: "refused"}[approve])
	if !approve {
		o.log(ctx, tenant, actor, "exit.refused", id, map[string]any{"note": note})
		return r.Public(), nil
	}
	o.log(ctx, tenant, actor, "exit.approved", id, map[string]any{"requested_by": r.RequestedBy, "cooling_off_ends": r.CoolingOffEnds.Format(time.RFC3339)})
	set, _ := o.Settings(ctx, tenant)
	o.tellManagement(ctx, tenant, set, fmt.Sprintf("[%s] Closing the Ledgerline account was approved; the data is deleted after %s unless cancelled", set.Firm, r.CoolingOffEnds.In(ist).Format("02 Jan 2006 15:04")), id)
	return r.Public(), nil
}

// CancelExit stops closing the account, any time before the data is
// deleted.
func (o *Ops) CancelExit(ctx context.Context, tenant, actor, id, note string) (app.ExitRecord, error) {
	if err := o.require(ctx, tenant, actor, "Cancelling closing the firm's account", RoleCompliance); err != nil {
		return app.ExitRecord{}, err
	}
	r, err := o.exitRecord(ctx, tenant, id)
	if err != nil {
		return r, err
	}
	if r.State != app.ExitRequested && r.State != app.ExitApproved {
		return r.Public(), fmt.Errorf("%w: closing %s is %s", ErrState, id, strings.ToLower(r.State))
	}
	r.State, r.CancelledBy, r.CancelledAt = app.ExitCancelled, actor, o.now().UTC()
	if note = clip(strings.TrimSpace(note), 300); note != "" {
		r.Notes = append(r.Notes, "Cancelled: "+note)
	}
	if err := o.App.Exits.SaveExit(ctx, r); err != nil {
		return r, err
	}
	o.closeWork(ctx, tenant, actor, "exit", id, "cancelled")
	o.closeWork(ctx, tenant, actor, "exit", id+":purge", "cancelled")
	o.log(ctx, tenant, actor, "exit.cancelled", id, map[string]any{"note": note})
	return r.Public(), nil
}

// exitTick runs first in every tick. A purged firm runs nothing. Once the
// cooling-off of an approved exit has ended, only the exit's own work runs
// (the final export), so the data stops changing under it.
func (o *Ops) exitTick(ctx context.Context, tenant string, now time.Time) bool {
	if o.FirmClosed(tenant) {
		return true
	}
	r, _ := o.openExit(ctx, tenant)
	keep := ""
	if r != nil {
		keep = r.ExportID
	}
	o.exportHousekeeping(ctx, tenant, keep)
	if r == nil || r.State != app.ExitApproved || now.Before(r.CoolingOffEnds) {
		return false
	}
	if r.ExportID != "" {
		x, _, err := docs.Get[DataExport](ctx, o.Docs, tenant, cDataExport, r.ExportID)
		switch {
		case err == nil && x.State == DXBuilding && !o.exportRunning(tenant, x.ID):
			o.startExport(ctx, tenant, x.ID) // the server restarted while it was being built
			return true
		case err == nil && x.State != DXFailed && x.State != DXDeleted:
			return true
		}
		// A failed final export is tried again after an hour.
		if t, perr := time.Parse(time.RFC3339, x.FinishedAt); perr == nil && now.Sub(t) < time.Hour {
			return true
		}
	}
	at := o.now()
	x := DataExport{ID: newExportID(at), State: DXBuilding, Final: true, Exit: r.ID, RequestedBy: r.RequestedBy, RequestedAt: at.Format(time.RFC3339),
		ApprovedBy: r.ApprovedBy, ApprovedAt: r.ApprovedAt.In(ist).Format(time.RFC3339), Reason: "Final export before closing the account (" + r.ID + ")"}
	if err := docs.Upsert(ctx, o.Docs, tenant, cDataExport, x.ID, x, SystemActor); err != nil {
		return true
	}
	r.ExportID, r.ExportSHA256, r.ExportDownloadedAt = x.ID, "", time.Time{}
	if err := o.App.Exits.SaveExit(ctx, *r); err != nil {
		return true
	}
	o.log(ctx, tenant, SystemActor, "exit.final_export", r.ID, map[string]any{"export": x.ID})
	o.startExport(ctx, tenant, x.ID)
	return true
}

// exitExportReady records the final export's hash on the exit.
func (o *Ops) exitExportReady(ctx context.Context, tenant string, x DataExport) {
	r, err := o.exitRecord(ctx, tenant, x.Exit)
	if err != nil || r.ExportID != x.ID || r.State != app.ExitApproved {
		return
	}
	r.ExportSHA256 = x.ManifestSHA256
	_ = o.App.Exits.SaveExit(ctx, r)
}

// exitExportDownloaded records that the final export was downloaded,
// which lets an Admin delete the data.
func (o *Ops) exitExportDownloaded(ctx context.Context, tenant, actor string, x DataExport) {
	r, err := o.exitRecord(ctx, tenant, x.Exit)
	if err != nil || r.ExportID != x.ID || r.State != app.ExitApproved || !r.ExportDownloadedAt.IsZero() {
		return
	}
	r.ExportDownloadedAt = o.now().UTC()
	if r.ExportSHA256 == "" {
		r.ExportSHA256 = x.ManifestSHA256
	}
	if err := o.App.Exits.SaveExit(ctx, r); err != nil {
		return
	}
	o.log(ctx, tenant, actor, "exit.export_downloaded", r.ID, map[string]any{"export": x.ID, "manifest_sha256": r.ExportSHA256})
	o.raiseWork(ctx, tenant, "exit", r.ID+":purge", "Delete the firm's data and get the destruction certificate",
		"The final export "+x.ID+" was downloaded by "+actor+". Check it opens and its signature verifies, then an Admin deletes the firm's data under Admin → Data & exit. This cannot be undone.",
		"", RoleAdmin, "dataexit", "")
}

// purgeKinds name what the certificate lists, by table.
var purgeKinds = []struct{ table, label string }{
	{"doc", "Documents: clients, users, settings and every workflow record"},
	{"doc_history", "Earlier versions of documents"},
	{"raw_file", "Stored files: KYC documents, exchange, depository and bank files, contract notes, reports"},
	{"journal_entry", "Ledger entries"},
	{"posting", "Ledger postings"},
	{"account", "Ledger accounts"},
	{"trade_event", "Trades"},
	{"recon_run", "Trade reconciliation runs"},
	{"recon_break", "Trade reconciliation breaks"},
	{"charge_schedule", "Charge schedules"},
	{"audit_log", "Audit trail entries"},
	{"tenant", "The firm's own record in the database"},
}

// ExitDone is what deleting the data gives back.
type ExitDone struct {
	Exit           app.ExitRecord `json:"exit"`
	Certificate    []byte         `json:"certificate"` // the PDF
	CertificateURL string         `json:"certificate_url"`
	Mailed         string         `json:"mailed"`
}

// backupsGoneBy is the date copies of backups held elsewhere age out, from
// the operator's setting, or a statement that the operator confirms it.
func (o *Ops) backupsGoneBy(at time.Time) string {
	if o.BackupCopiesDays > 0 {
		return at.In(ist).AddDate(0, 0, o.BackupCopiesDays).Format("02 Jan 2006")
	}
	return "To be confirmed by the operator"
}

// PurgeFirm deletes the firm's data for an approved exit whose cooling-off
// has ended and whose final export was downloaded, and issues the
// destruction certificate.
func (o *Ops) PurgeFirm(ctx context.Context, tenant, actor, id, confirm string) (ExitDone, error) {
	if err := o.require(ctx, tenant, actor, "Deleting the firm's data", RoleAdmin); err != nil {
		return ExitDone{}, err
	}
	if err := confirmCode(tenant, confirm); err != nil {
		return ExitDone{}, err
	}
	if o.App.Purge == nil {
		return ExitDone{}, fmt.Errorf("%w: this storage cannot delete a firm", ErrState)
	}
	r, err := o.exitRecord(ctx, tenant, id)
	if err != nil {
		return ExitDone{}, err
	}
	now := o.now()
	if err := r.PurgeAllowed(now); err != nil {
		return ExitDone{}, err
	}
	// Everything the certificate and the mail need is read now: once the
	// purge starts nothing of the firm can be read, and nothing may be
	// written.
	set, _ := o.Settings(ctx, tenant)
	sc := o.signingConfig(ctx, tenant)
	var signFn func([]byte, time.Time) ([]byte, error)
	var info sign.Info
	signNote := ""
	if sc.Mode == "agent" {
		signNote = "Not digitally signed: the firm signs with a USB token signer, which cannot sign once the firm's data is gone. The certificate's SHA-256 is kept in Ledgerline's record of the exit."
	} else if fn, inf, err := o.docSigner(ctx, tenant); err == nil {
		signFn, info = fn, inf
	} else {
		signNote = "Not digitally signed: " + err.Error() + ". The certificate's SHA-256 is kept in Ledgerline's record of the exit."
	}
	mailer := o.mailerFor(ctx, tenant)
	var backups []string
	if o.Backups != nil {
		if es, err := o.Backups.List(tenant); err == nil {
			for _, e := range es {
				backups = append(backups, e.ID)
			}
		}
	}
	tok := make([]byte, 24)
	_, _ = crand.Read(tok)
	token := hex.EncodeToString(tok)
	th := sha256.Sum256([]byte(token))
	gone := o.backupsGoneBy(now)

	var cert []byte
	out, err := o.App.Purge.PurgeTenant(ctx, tenant, id, now, func(deleted map[string]int) (app.ExitRecord, error) {
		done := r
		done.State, done.PurgedAt, done.Deleted, done.BackupsGoneBy = app.ExitPurged, now.UTC(), deleted, gone
		done.CertificateTokenHash = hex.EncodeToString(th[:])
		b, err := exitCertificatePDF(done, set, len(backups), sc, signFn, info, signNote, now)
		if err != nil {
			return done, fmt.Errorf("the destruction certificate: %w", err)
		}
		s := sha256.Sum256(b)
		done.Certificate, done.CertificateSHA256 = b, hex.EncodeToString(s[:])
		cert = b
		return done, nil
	})
	if err != nil {
		return ExitDone{}, err
	}
	o.markClosed(tenant)
	// The firm's files outside the database: its backups and its work
	// folder (exports, migration uploads).
	var notes []string
	for _, b := range backups {
		if err := o.Backups.Delete(tenant, b); err != nil {
			notes = append(notes, "Backup "+b+" could not be deleted: "+err.Error())
		}
	}
	if d, err := o.firmWorkDir(tenant); err == nil {
		if err := os.RemoveAll(d); err != nil {
			notes = append(notes, "The firm's work folder could not be deleted: "+err.Error())
		}
	}
	if o.Backups != nil && reTenantDir.MatchString(tenant) {
		_ = os.Remove(filepath.Join(o.Backups.Path, tenant)) // the firm's backup folder, now empty
	}
	link := publicURL(ctx) + "/exit-certificate/" + tenant + "/" + id + "?t=" + token
	mailed := "Not sent: no mail server"
	if set.ComplianceEmail == "" {
		mailed = "Not sent: no compliance email in Settings"
	} else if mailer != nil {
		_, err := mailer.Send(ctx, Mail{From: mailFrom(mailer, set), To: set.ComplianceEmail, Subject: "[" + set.Firm + "] Ledgerline destruction certificate " + id,
			Text: fmt.Sprintf("The firm's data was deleted from Ledgerline on %s.\n\nThe destruction certificate is attached (SHA-256 %s). Keep this mail: you can download the certificate again from\n%s\n\nCopies of backups held elsewhere age out by: %s.\n",
				now.Format("02 Jan 2006 15:04 MST"), out.CertificateSHA256, link, gone),
			Attach: []MailFile{{Name: "destruction-certificate-" + id + ".pdf", Type: "application/pdf", Data: cert}}})
		if err != nil {
			mailed = "Failed: " + err.Error()
		} else {
			mailed = "Sent to " + set.ComplianceEmail
		}
	}
	notes = append(notes, "Certificate mail: "+mailed)
	out.Notes = append(out.Notes, notes...)
	_ = o.App.Exits.SaveExit(ctx, out)
	return ExitDone{Exit: out.Public(), Certificate: cert, CertificateURL: link, Mailed: mailed}, nil
}

// exitCertificatePDF renders the destruction certificate, signed with the
// firm's certificate when signFn is set.
func exitCertificatePDF(r app.ExitRecord, set Settings, backups int, sc SigningConfig, signFn func([]byte, time.Time) ([]byte, error), info sign.Info, signNote string, at time.Time) ([]byte, error) {
	ts := func(t time.Time) string {
		if t.IsZero() {
			return "-"
		}
		return t.In(ist).Format("02 Jan 2006 15:04 MST")
	}
	firm := []string{firstOf(r.Firm, r.Tenant)}
	if r.FirmCode != "" {
		firm = append(firm, r.FirmCode)
	}
	rep := pdf.Report{
		Firm:  firm,
		Title: "Certificate of data destruction",
		Meta:  []string{"Certificate " + r.ID, "Issued " + ts(at)},
		Summary: [][2]string{
			{"Firm", firstOf(r.Firm, r.Tenant)},
			{"Firm code in Ledgerline", r.Tenant},
			{"Registration and member codes", firstOf(r.FirmCode, "-")},
			{"Asked for by", r.RequestedBy + ", " + ts(r.RequestedAt)},
			{"Approved by", strings.TrimSpace(r.ApprovedBy+" ("+r.ApprovedRole+")") + ", " + ts(r.ApprovedAt)},
			{"Cooling-off", strconv.Itoa(r.CoolingOffDays) + " days, ended " + ts(r.CoolingOffEnds)},
			{"Final export", r.ExportID + ", downloaded " + ts(r.ExportDownloadedAt)},
			{"Final export's manifest SHA-256", r.ExportSHA256},
			{"Data deleted", ts(r.PurgedAt)},
			{"Backup copies held elsewhere age out by", r.BackupsGoneBy},
		},
	}
	rows := [][]string{}
	total := 0
	for _, k := range purgeKinds {
		if n, ok := r.Deleted[k.table]; ok {
			rows = append(rows, []string{k.label, strconv.Itoa(n)})
			total += n
		}
	}
	rows = append(rows, []string{"Backup files in Ledgerline's backup folder", strconv.Itoa(backups)})
	rep.Sections = []pdf.Table{{Title: "What was deleted", Cols: []pdf.Col{{Label: "Kind", Weight: 5}, {Label: "Count", Right: true, Weight: 1}}, Rows: rows,
		Totals: []string{"Rows deleted from the live database", strconv.Itoa(total)}}}
	rep.Notes = []string{
		"Every record of the firm listed above was deleted from Ledgerline's live database in one transaction at the time shown, and the firm's backups, exports and work files were deleted from the server's disk after it.",
		"Before the deletion the firm received its final export. The SHA-256 of its signed manifest is shown above, so the firm can show which data it was given.",
		"Copies of Ledgerline's backups held elsewhere (off-site copies, database snapshots) are not changed; they age out by the date shown. Where it says to be confirmed, the operator confirms the date in writing.",
		"Not covered: copies outside Ledgerline, such as email and files already sent or downloaded, and records held by exchanges, clearing corporations, depositories, banks and KRAs.",
		"Ledgerline keeps only its own record of this exit: the firm's name and code, the dates, who asked and approved, the counts above and this certificate.",
	}
	if signNote != "" {
		rep.Notes = append(rep.Notes, signNote)
	}
	rep.Footer = "Destruction certificate " + r.ID + " · " + firstOf(r.Firm, r.Tenant)
	meta := pdf.Meta{Title: "Certificate of data destruction " + r.ID, Author: "Ledgerline", Subject: "Data destruction", Created: at}
	if signFn == nil {
		return pdf.Render(rep).Bytes(meta, nil)
	}
	reason := "Certificate of data destruction"
	rep.Stamp = []string{"Digitally signed by " + info.Name, "Date: " + at.In(ist).Format("02-Jan-2006 15:04 MST"), "Reason: " + reason, "Certificate " + info.Serial + " issued by " + info.Issuer}
	if sc.Mode == "test" {
		rep.Banner = "TEST SIGNATURE: this development server signs with a test certificate."
	}
	return pdf.Render(rep).Bytes(meta, &pdf.SignatureSpec{Name: info.Name, Reason: reason, Location: firstOf(sc.Location, "India"), Contact: set.ComplianceEmail, At: at,
		Sign: func(content []byte) ([]byte, error) { return signFn(content, at) }})
}

// ExitCertificate gives the certificate to whoever holds the link the
// firm was given (no sign-in: the firm's users are gone).
func (o *Ops) ExitCertificate(ctx context.Context, tenant, id, token string) ([]byte, error) {
	if o.App.Exits == nil || token == "" {
		return nil, ErrNotFound
	}
	r, err := o.App.Exits.GetExit(ctx, tenant, id)
	if err != nil || r.State != app.ExitPurged || r.CertificateTokenHash == "" {
		return nil, fmt.Errorf("%w: no such certificate", ErrNotFound)
	}
	h := sha256.Sum256([]byte(token))
	if subtle.ConstantTimeCompare([]byte(hex.EncodeToString(h[:])), []byte(r.CertificateTokenHash)) != 1 {
		return nil, fmt.Errorf("%w: no such certificate", ErrNotFound)
	}
	return r.Certificate, nil
}

// ExitRecords is the operator's list of exits, every firm's.
func (o *Ops) ExitRecords(ctx context.Context) ([]app.ExitRecord, error) {
	if o.App.Exits == nil {
		return []app.ExitRecord{}, nil
	}
	rs, err := o.App.Exits.ListExits(ctx, "")
	for i := range rs {
		rs[i] = rs[i].Public()
	}
	return rs, err
}

// OperatorCertificate is the stored certificate of a purged firm, for the
// operator to issue again.
func (o *Ops) OperatorCertificate(ctx context.Context, tenant, id string) ([]byte, error) {
	r, err := o.exitRecord(ctx, tenant, id)
	if err != nil {
		return nil, err
	}
	if r.State != app.ExitPurged || len(r.Certificate) == 0 {
		return nil, fmt.Errorf("%w: exit %s has no certificate", ErrState, id)
	}
	return r.Certificate, nil
}

// ---- the screen ----

// DataExitView is Admin → Data & exit.
type DataExitView struct {
	Exports        []DataExport      `json:"exports"`
	Exit           *app.ExitRecord   `json:"exit"`
	Past           []app.ExitRecord  `json:"past"`
	CoolingOffDays int               `json:"cooling_off_days"`
	Now            string            `json:"now"`
	KeepDays       int               `json:"keep_days"`
	BackupsGoneBy  string            `json:"backups_gone_by"`
	Signing        string            `json:"signing"`
	SigningNote    string            `json:"signing_note,omitempty"`
	ComplianceMail string            `json:"compliance_email"`
	MailSet        bool              `json:"mail_set"`
	Available      bool              `json:"available"`
	Problem        string            `json:"problem,omitempty"`
	Steps          []DataExitStep    `json:"steps"`
	Counts         map[string]string `json:"counts,omitempty"`
}

// DataExitStep is one step of closing the account, for the screen.
type DataExitStep struct {
	Label string `json:"label"`
	State string `json:"state"` // done, now, todo
	Note  string `json:"note,omitempty"`
}

// DataExit is the screen. Admin and the compliance officer (who approves).
func (o *Ops) DataExit(ctx context.Context, tenant, actor string) (DataExitView, error) {
	if err := o.require(ctx, tenant, actor, "Viewing data export and account closing", RoleCompliance); err != nil {
		return DataExitView{}, err
	}
	now := o.now()
	set, _ := o.Settings(ctx, tenant)
	v := DataExitView{Exports: []DataExport{}, Past: []app.ExitRecord{}, CoolingOffDays: o.exitSettings(ctx, tenant).CoolingOffDays, Now: now.Format(time.RFC3339),
		KeepDays: DataExportKeepDays, BackupsGoneBy: o.backupsGoneBy(now), ComplianceMail: set.ComplianceEmail, MailSet: o.mailerFor(ctx, tenant) != nil,
		Available: o.App.Exits != nil && o.App.Purge != nil && o.exportSealer() != nil && o.App.Snap != nil}
	if !v.Available {
		v.Problem = "Full exports and closing the account need the server's data keys and a store that can be read and purged whole."
	}
	sc := o.signingConfig(ctx, tenant)
	v.Signing = sc.Mode
	switch sc.Mode {
	case "agent":
		v.SigningNote = "Your documents are signed by a USB token signer. Exports are signed with it when it is online. The destruction certificate cannot be signed with it, because it is made as the data is deleted; switch to a certificate file or signing service first if you need it signed."
	case "off":
		v.SigningNote = "No digital signature is set up. Exports are signed with a key made for each export; the destruction certificate is not digitally signed."
	}
	xs, _ := docs.List[DataExport](ctx, o.Docs, tenant, cDataExport, "")
	sortExports(xs)
	for _, x := range xs {
		if x.State == DXBuilding && !o.exportRunning(tenant, x.ID) && !x.Final {
			x.State, x.Error = DXFailed, "interrupted: the server restarted while it was being built; ask for a new export"
		}
		v.Exports = append(v.Exports, x.view())
	}
	if o.App.Exits != nil {
		rs, _ := o.App.Exits.ListExits(ctx, tenant)
		for i := len(rs) - 1; i >= 0; i-- {
			r := rs[i].Public()
			if (r.State == app.ExitRequested || r.State == app.ExitApproved) && v.Exit == nil {
				v.Exit = &r
				continue
			}
			v.Past = append(v.Past, r)
		}
	}
	v.Steps = exitSteps(v.Exit, now)
	return v, nil
}

func exitSteps(r *app.ExitRecord, now time.Time) []DataExitStep {
	st := func(done, cur bool) string {
		if done {
			return "done"
		}
		if cur {
			return "now"
		}
		return "todo"
	}
	var asked, approved, cooled, exported, downloaded bool
	if r != nil {
		asked = true
		approved = r.State == app.ExitApproved
		cooled = approved && !now.Before(r.CoolingOffEnds)
		exported = r.ExportSHA256 != ""
		downloaded = !r.ExportDownloadedAt.IsZero()
	}
	s := []DataExitStep{
		{Label: "An Admin asks to close the account", State: st(asked, !asked)},
		{Label: "A second person approves", State: st(approved, asked && !approved)},
		{Label: "Cooling-off period (can be cancelled)", State: st(cooled, approved && !cooled)},
		{Label: "Ledgerline builds the final export", State: st(exported, cooled && !exported)},
		{Label: "An Admin downloads the final export", State: st(downloaded, exported && !downloaded)},
		{Label: "An Admin deletes the data; the certificate is issued", State: st(false, downloaded)},
	}
	if r != nil && approved && !cooled {
		s[2].Note = "ends " + r.CoolingOffEnds.In(ist).Format("02 Jan 2006 15:04")
	}
	return s
}
