package ops

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"ledgerline/internal/docs"
)

// ---- missing daily files: work items and one email a run ----
//
// On business days (the exchange holiday calendar and the firm's own
// holidays, Settings.IsBusinessDay) dailyFileAlertTick looks at the daily
// file checklist of today and of the previous business day (for files due
// the next morning, and late files such as MCX's that run past midnight).
// A file still not received or written GraceMinutes after its usual time
// gets one work item a day for the role that deals with it, and the run
// sends one digest of every file newly missing to the recipients. The
// checklist only lists the files of the clearing corporations chosen in
// Settings → Cleared through, so a clearing corporation that is not chosen
// is never chased. Intraday files and files Ledgerline cannot read yet
// ("Check by hand") are never alerted. When the file arrives,
// noteDailyFile closes its work item.

const (
	cFileAlertSet  = "xfilealertset" // main
	wFileMissing   = "xfile-missing" // work item process; key date|file id
	fileAlertGrace = 30              // minutes, default
)

// FileAlertSettings are the missing-file alert rules.
type FileAlertSettings struct {
	// Off stops the alerts (they are on by default).
	Off bool `json:"off"`
	// GraceMinutes after a file's usual time before it is chased (default 30).
	GraceMinutes int `json:"grace_minutes"`
	// To are the digest's recipients: email addresses, or "management"
	// (Settings → Management emails) and "compliance" (the compliance
	// email); the role names Admin and Compliance officer mean the same.
	// Empty: the management emails, else the compliance email.
	To []string `json:"to"`
	// UpdatedBy and UpdatedAt record the last change.
	UpdatedBy string `json:"updated_by,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// DefaultFileAlertSettings: on, 30 minutes' grace, to management.
func DefaultFileAlertSettings() FileAlertSettings {
	return FileAlertSettings{GraceMinutes: fileAlertGrace, To: []string{}}
}

// FileAlertSettings returns the firm's rules (defaults until saved).
func (o *Ops) FileAlertSettings(ctx context.Context, tenant string) FileAlertSettings {
	s, _, err := docs.Get[FileAlertSettings](ctx, o.Docs, tenant, cFileAlertSet, "main")
	if err != nil {
		return DefaultFileAlertSettings()
	}
	if s.GraceMinutes <= 0 {
		s.GraceMinutes = fileAlertGrace
	}
	if s.To == nil {
		s.To = []string{}
	}
	return s
}

// SaveFileAlertSettings checks and stores the rules.
func (o *Ops) SaveFileAlertSettings(ctx context.Context, tenant, actor string, s FileAlertSettings) (FileAlertSettings, error) {
	if err := o.require(ctx, tenant, actor, "Changing missing-file alerts", RoleCompliance, RoleRisk); err != nil {
		return s, err
	}
	if s.GraceMinutes == 0 {
		s.GraceMinutes = fileAlertGrace
	}
	if s.GraceMinutes < 0 || s.GraceMinutes > 600 {
		return s, invalid("the grace period is 1 to 600 minutes")
	}
	to := []string{}
	for _, e := range s.To {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if _, ok := fileAlertKeyword(e); !ok && !reEmail.MatchString(e) {
			return s, invalid("%q is not an email address, management or compliance", e)
		}
		to = append(to, e)
	}
	s.To, s.UpdatedBy, s.UpdatedAt = to, actor, o.now().Format(time.RFC3339)
	if err := docs.Upsert(ctx, o.Docs, tenant, cFileAlertSet, "main", s, actor); err != nil {
		return s, err
	}
	o.log(ctx, tenant, actor, "xfile.alert_settings", "main", map[string]any{"off": s.Off, "grace_minutes": s.GraceMinutes, "to": strings.Join(s.To, ", ")})
	return s, nil
}

// fileAlertKeyword maps a recipient word or role to where its emails are.
func fileAlertKeyword(s string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "management", strings.ToLower(RoleAdmin):
		return "management", true
	case "compliance", strings.ToLower(RoleCompliance):
		return "compliance", true
	}
	return "", false
}

// fileAlertRecipients are the email addresses of the digest.
func fileAlertRecipients(set Settings, fs FileAlertSettings) []string {
	seen := map[string]bool{}
	var out []string
	add := func(e string) {
		e = strings.TrimSpace(e)
		if e != "" && !seen[strings.ToLower(e)] {
			seen[strings.ToLower(e)] = true
			out = append(out, e)
		}
	}
	to := fs.To
	if len(to) == 0 {
		to = []string{"management"}
		if len(set.ManagementEmails) == 0 {
			to = []string{"compliance"}
		}
	}
	for _, e := range to {
		switch k, _ := fileAlertKeyword(e); k {
		case "management":
			for _, m := range set.ManagementEmails {
				add(m)
			}
		case "compliance":
			add(set.ComplianceEmail)
		default:
			add(e)
		}
	}
	return out
}

// dailyFileRole is the role that chases a missing file.
func dailyFileRole(f DailyFile) string {
	switch f.ID {
	case "bse-debr", "bse-nome", "bse-orders", "bse-bulk", "ncl-segmon":
		return RoleCompliance
	}
	if f.Exchange == "" { // clearing corporation files: margins, collateral, allocation
		return RoleRisk
	}
	return RoleMaker
}

// FileAlertRun is what one run of the missing-file alerts did.
type FileAlertRun struct {
	At       string          `json:"at"`
	Alerted  []ChecklistItem `json:"alerted"`  // newly missing this run
	Mailed   []string        `json:"mailed"`   // recipient (result)
	Skipped  string          `json:"skipped"`  // why nothing was checked
	Checking []string        `json:"checking"` // checklist dates looked at
}

// dailyFileAlertTick chases the expected files that are late (scheduler).
func (o *Ops) dailyFileAlertTick(ctx context.Context, tenant string, now time.Time) {
	_, _ = o.RunFileAlerts(ctx, tenant, SystemActor, now)
}

// CheckMissingFilesNow runs the alerts at once (the checklist's button).
func (o *Ops) CheckMissingFilesNow(ctx context.Context, tenant, actor string) (FileAlertRun, error) {
	if err := o.require(ctx, tenant, actor, "Chasing missing daily files", RoleMaker, RoleChecker, RoleRisk, RoleCompliance); err != nil {
		return FileAlertRun{}, err
	}
	return o.RunFileAlerts(ctx, tenant, actor, o.now())
}

// RunFileAlerts raises a work item for each expected file still missing
// its grace period after its time, once a file a day, and emails one
// digest of those newly missing.
func (o *Ops) RunFileAlerts(ctx context.Context, tenant, actor string, now time.Time) (FileAlertRun, error) {
	now = now.In(ist)
	run := FileAlertRun{At: now.Format(time.DateTime), Alerted: []ChecklistItem{}, Mailed: []string{}, Checking: []string{}}
	fs := o.FileAlertSettings(ctx, tenant)
	if fs.Off {
		run.Skipped = "missing-file alerts are switched off"
		return run, nil
	}
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return run, err
	}
	today := now.Format("2006-01-02")
	if !set.IsBusinessDay(today) {
		run.Skipped = "not a business day"
		return run, nil
	}
	cutoff := now.Add(-time.Duration(fs.GraceMinutes) * time.Minute).Format("2006-01-02 15:04")
	for _, date := range []string{set.PrevBusinessDay(today), today} {
		run.Checking = append(run.Checking, date)
		xs, err := o.chaseMissing(ctx, tenant, actor, date, cutoff)
		if err != nil {
			return run, err
		}
		run.Alerted = append(run.Alerted, xs...)
	}
	if len(run.Alerted) == 0 {
		return run, nil
	}
	to := fileAlertRecipients(set, fs)
	subject := fmt.Sprintf("[%s] %d expected file%s missing (%s)", set.Firm, len(run.Alerted), plural(len(run.Alerted)), fmtDay(today))
	var b strings.Builder
	fmt.Fprintf(&b, "These files were due and have not been received (or, for files the firm sends, written) %d minutes after their usual time:\n\n", fs.GraceMinutes)
	for _, it := range run.Alerted {
		verb := "from"
		if it.Direction == "out" {
			verb = "to"
		}
		fmt.Fprintf(&b, "- %s of %s (%s %s, for %s): due %s IST; with %s\n", it.Title, fmtDay(it.Date), verb, it.Party, strings.Join(it.For, ", "), it.Due, dailyFileRole(it.DailyFile))
	}
	b.WriteString("\nEach has a work item. It closes by itself when the file is loaded on Exchange files in Ledgerline.\n")
	for _, e := range to {
		run.Mailed = append(run.Mailed, e+" ("+o.mailStaff(ctx, tenant, e, subject, b.String(), "xfile-missing:"+today, nil)+")")
	}
	if len(to) == 0 {
		run.Mailed = append(run.Mailed, "nobody: no management or compliance email in Settings")
	}
	o.log(ctx, tenant, actor, "xfile.missing_digest", today, map[string]any{"files": len(run.Alerted), "to": strings.Join(run.Mailed, ", ")})
	return run, nil
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// chaseMissing alerts the files of a checklist date due before cutoff
// (YYYY-MM-DD HH:MM, IST) and not received: an alert record and an audit
// entry, and a work item for the file's role, once a day each. It returns
// the files alerted now.
func (o *Ops) chaseMissing(ctx context.Context, tenant, actor, date, cutoff string) ([]ChecklistItem, error) {
	cl, err := o.DailyFileChecklist(ctx, tenant, date)
	if err != nil {
		return nil, err
	}
	var out []ChecklistItem
	for _, it := range cl.Items {
		if it.Format == "" || it.Intraday || len(it.Files) > 0 || it.Due > cutoff {
			continue
		}
		id := date + "|alert|" + it.ID
		if _, _, err := docs.Get[DailyFileRecv](ctx, o.Docs, tenant, cDailyRecv, id); err == nil {
			continue
		}
		at := o.now().In(ist).Format("2006-01-02 15:04")
		if err := docs.Upsert(ctx, o.Docs, tenant, cDailyRecv, id, DailyFileRecv{Date: date, Format: "alert", Name: it.ID, At: at, By: actor}, actor); err != nil {
			return out, err
		}
		o.log(ctx, tenant, actor, "xfile.missing", it.ID, map[string]any{"date": date, "title": it.Title, "party": it.Party, "due": it.Due, "for": strings.Join(it.For, ", ")})
		what, then := "received from", "load it on Exchange files when it comes"
		if it.Direction == "out" {
			what, then = "sent to", "make it on Exchange files and send it"
		}
		o.workDue(ctx, tenant, wFileMissing, date+"|"+it.ID, "Missing: "+it.Title+" for "+fmtDay(date),
			fmt.Sprintf("%s was due to be %s %s by %s IST (for %s) and is not in Ledgerline. Check the extranet or the portal and chase it; %s, and this item closes by itself.", it.Title, what, it.Party, it.Due, strings.Join(it.For, ", "), then),
			"", dailyFileRole(it.DailyFile), "xfiles", date, it.Due)
		it.AlertedAt, it.Status = at, "Missing"
		out = append(out, it)
	}
	return out, nil
}

// closeMissingWork closes the missing-file items a received file answers.
func (o *Ops) closeMissingWork(ctx context.Context, tenant, actor, format, name, date string) {
	for _, f := range DailyFiles {
		if f.Format == "" || f.Format != format {
			continue
		}
		if f.Name != "" && !regexp.MustCompile("(?i)"+f.Name).MatchString(name) {
			continue
		}
		o.closeWork(ctx, tenant, actor, wFileMissing, date+"|"+f.ID, "Received "+name)
	}
}

// alertTimes are the alert times of a checklist date's files (file id ->
// YYYY-MM-DD HH:MM).
func alertTimes(recv []DailyFileRecv) map[string]string {
	out := map[string]string{}
	for _, r := range recv {
		if r.Format == "alert" {
			out[r.Name] = r.At
		}
	}
	return out
}
