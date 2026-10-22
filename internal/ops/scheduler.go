package ops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// ---- scheduled jobs ----
//
// Jobs run on business days, on given weekdays or on a day of the month,
// at a set time (IST). Each run is recorded with its result, and the firm's
// management (or the job's own list) is emailed when a job succeeds or
// fails. Jobs run as the system user, which has no login.

// SystemActor runs scheduled jobs. It cannot sign in: user IDs may not
// contain a colon.
const SystemActor = "system:scheduler"

// JobKind describes one kind of job.
type JobKind struct {
	Kind   string   `json:"kind"`
	Name   string   `json:"name"`
	About  string   `json:"about"`
	Params []string `json:"params,omitempty"`
}

// JobKinds are the jobs the scheduler can run.
var JobKinds = []JobKind{
	{"debit_reminders", "Debit reminders", "Messages clients in debit for at least the given number of days.", []string{"min_days"}},
	{"monthly_statements", "Monthly statements", "Emails every active client their statement of account for the previous month.", nil},
	{"email_report", "Email a report", "Generates a regulatory or inspection report and emails it to the recipients.", []string{"report", "to"}},
	{"kra_upload", "KRA upload file", "Builds the KRA upload file of clients opened or changed since the last run and keeps it with the files.", nil},
	{"autopilot", "Autopilot stage", "Runs every automation of a stage (bod, intraday, preclose, eod, monthly) under the firm's autopilot settings.", []string{"stage"}},
}

// RetiredJobKinds were scheduled jobs that Autopilot now does, with its
// policies, caps and work queue. Saved jobs of these kinds are hidden and
// never run, so the same work cannot happen twice.
var RetiredJobKinds = map[string]string{
	"send_notes":      "Autopilot: Contract notes (also holds notes for clients with an open code change)",
	"margin_alerts":   "Autopilot: Margin calls (one alert per client per day, plus MTF calls)",
	"rekyc_reminders": "Autopilot: KYC registries and reminders",
	"post_interest":   "Autopilot: Month-end postings (a checker approves before posting)",
	"inactive_scan":   "Autopilot: Inactive account flagging",
}

func liveJobs(js []Job) []Job {
	out := js[:0]
	for _, j := range js {
		if _, gone := RetiredJobKinds[j.Kind]; !gone {
			out = append(out, j)
		}
	}
	return out
}

// Job is a scheduled job.
type Job struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Kind        string            `json:"kind"`
	Params      map[string]string `json:"params"`
	At          string            `json:"at"`   // HH:MM, IST
	When        string            `json:"when"` // business, daily, weekly:Mon, monthly:1
	Notify      []string          `json:"notify"`
	OnlyFailure bool              `json:"only_failure"`
	Active      bool              `json:"active"`
	LastRun     string            `json:"last_run,omitempty"`
	LastDate    string            `json:"last_date,omitempty"`
	LastStatus  string            `json:"last_status,omitempty"`
	LastSummary string            `json:"last_summary,omitempty"`
	Next        string            `json:"next,omitempty"`
}

// JobRun is one run of a job.
type JobRun struct {
	ID       string `json:"id"`
	Job      string `json:"job"`
	Name     string `json:"name"`
	Date     string `json:"date"`
	Started  string `json:"started"`
	Millis   int64  `json:"ms"`
	Status   string `json:"status"` // Succeeded, Failed
	Summary  string `json:"summary"`
	By       string `json:"by"`
	Notified string `json:"notified,omitempty"`
}

func validWhen(w string) bool {
	switch {
	case w == "business" || w == "daily":
		return true
	case strings.HasPrefix(w, "weekly:"):
		return contains([]string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"}, strings.TrimPrefix(w, "weekly:"))
	case strings.HasPrefix(w, "monthly:"):
		var d int
		_, err := fmt.Sscanf(strings.TrimPrefix(w, "monthly:"), "%d", &d)
		return err == nil && d >= 1 && d <= 28
	}
	return false
}

// runsOn reports whether a job's schedule includes date.
func (j Job) runsOn(set Settings, date string) bool {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return false
	}
	switch {
	case j.When == "daily":
		return true
	case j.When == "business":
		return set.IsBusinessDay(date)
	case strings.HasPrefix(j.When, "weekly:"):
		return t.Weekday().String()[:3] == strings.TrimPrefix(j.When, "weekly:")
	case strings.HasPrefix(j.When, "monthly:"):
		return fmt.Sprint(t.Day()) == strings.TrimPrefix(j.When, "monthly:")
	}
	return false
}

func (j Job) nextRun(set Settings, now time.Time) string {
	for i := 0; i < 62; i++ {
		d := now.AddDate(0, 0, i).Format("2006-01-02")
		if j.runsOn(set, d) && (i > 0 || (now.Format("15:04") < j.At && j.LastDate != d)) {
			return d + " " + j.At
		}
	}
	return ""
}

// SaveJob creates or updates a job.
func (o *Ops) SaveJob(ctx context.Context, tenant, actor string, j Job) (Job, error) {
	if err := o.require(ctx, tenant, actor, "Scheduling jobs", RoleAdmin); err != nil {
		return j, err
	}
	if o.manual(ctx, tenant) {
		if j.Kind == "autopilot" {
			return j, errManualEdition
		}
		j.Active = false // Manual edition: jobs are run by hand only
	}
	known := false
	for _, k := range JobKinds {
		if k.Kind == j.Kind {
			known = true
			for _, p := range k.Params {
				if strings.TrimSpace(j.Params[p]) == "" {
					return j, invalid("%s needs %s", k.Name, p)
				}
			}
		}
	}
	if !known {
		return j, invalid("unknown job kind %q", j.Kind)
	}
	if _, err := time.Parse("15:04", j.At); err != nil {
		return j, invalid("time must be HH:MM")
	}
	if !validWhen(j.When) {
		return j, invalid("schedule must be business, daily, weekly:Mon…Sun or monthly:1…28")
	}
	for _, e := range j.Notify {
		if !reEmail.MatchString(e) {
			return j, invalid("%q is not an email address", e)
		}
	}
	if strings.TrimSpace(j.Name) == "" {
		return j, invalid("give the job a name")
	}
	if j.Params == nil {
		j.Params = map[string]string{}
	}
	if j.ID == "" {
		n, err := o.next(ctx, tenant, "job", actor)
		if err != nil {
			return j, err
		}
		j.ID = fmt.Sprintf("JOB-%03d", n)
	} else if old, _, err := docs.Get[Job](ctx, o.Docs, tenant, cJob, j.ID); err == nil {
		j.LastRun, j.LastDate, j.LastStatus, j.LastSummary = old.LastRun, old.LastDate, old.LastStatus, old.LastSummary
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cJob, j.ID, j, actor); err != nil {
		return j, err
	}
	o.log(ctx, tenant, actor, "job.saved", j.ID, map[string]any{"kind": j.Kind, "when": j.When, "at": j.At, "active": j.Active})
	return j, nil
}

// Jobs lists jobs with their next run, and recent runs.
func (o *Ops) Jobs(ctx context.Context, tenant string) ([]Job, []JobRun, error) {
	if err := o.SeedJobs(ctx, tenant); err != nil {
		return nil, nil, err
	}
	if err := o.seedAutopilotJobs(ctx, tenant); err != nil {
		return nil, nil, err
	}
	js, err := docs.List[Job](ctx, o.Docs, tenant, cJob, "")
	if err != nil {
		return nil, nil, err
	}
	js = liveJobs(js)
	if o.manual(ctx, tenant) {
		out := js[:0]
		for _, j := range js {
			if j.Kind != "autopilot" {
				j.Active = false
				out = append(out, j)
			}
		}
		js = out
	}
	set, _ := o.Settings(ctx, tenant)
	now := o.now()
	for i := range js {
		if js[i].Active {
			js[i].Next = js[i].nextRun(set, now)
		}
	}
	rs, _ := docs.List[JobRun](ctx, o.Docs, tenant, cJobRun, "")
	sort.Slice(rs, func(i, j int) bool { return rs[i].ID > rs[j].ID })
	if len(rs) > 100 {
		rs = rs[:100]
	}
	if js == nil {
		js = []Job{}
	}
	if rs == nil {
		rs = []JobRun{}
	}
	return js, rs, nil
}

// Tick runs every active job that is due at now and has not run today.
func (o *Ops) Tick(ctx context.Context, tenant string, now time.Time) (int, error) {
	js, err := docs.List[Job](ctx, o.Docs, tenant, cJob, "")
	if err != nil {
		return 0, err
	}
	js = liveJobs(js)
	if o.manual(ctx, tenant) {
		return 0, nil // Manual edition: nothing runs on a timer
	}
	set, _ := o.Settings(ctx, tenant)
	now = now.In(ist)
	today, hm := now.Format("2006-01-02"), now.Format("15:04")
	n := 0
	for _, j := range js {
		if !j.Active || j.LastDate == today || hm < j.At || !j.runsOn(set, today) {
			continue
		}
		if _, err := o.RunJob(ctx, tenant, SystemActor, j.ID, ""); err != nil && !errors.Is(err, errJobFailed) {
			return n, err
		}
		_, _ = docs.Update(ctx, o.Docs, tenant, cJob, j.ID, SystemActor, func(x *Job) error { x.LastDate = today; return nil })
		n++
	}
	return n, nil
}

var errJobFailed = errors.New("job failed")

// RunJob runs a job now for date (default: the current business date) and
// records the run. A job that fails is recorded and reported, and
// errJobFailed is returned with the reason.
func (o *Ops) RunJob(ctx context.Context, tenant, actor, id, date string) (JobRun, error) {
	if actor != SystemActor {
		if err := o.require(ctx, tenant, actor, "Running jobs", RoleAdmin, RoleChecker); err != nil {
			return JobRun{}, err
		}
	}
	j, _, err := docs.Get[Job](ctx, o.Docs, tenant, cJob, id)
	if err != nil {
		return JobRun{}, err
	}
	if date == "" {
		date = o.businessDate(ctx, tenant)
	}
	start := o.now()
	summary, jerr := o.runJob(ctx, tenant, j, date)
	run := JobRun{Job: j.ID, Name: j.Name, Date: date, Started: start.Format(time.RFC3339), Millis: time.Since(start).Milliseconds(), Status: "Succeeded", Summary: summary, By: actor}
	if jerr != nil {
		run.Status, run.Summary = "Failed", jerr.Error()
	}
	set, _ := o.Settings(ctx, tenant)
	to := j.Notify
	if len(to) == 0 {
		to = set.ManagementEmails
	}
	if len(to) > 0 && (run.Status == "Failed" || !j.OnlyFailure) {
		at := o.now().Format("15:04")
		for _, e := range to {
			n, err := o.next(ctx, tenant, "outbox", SystemActor)
			if err != nil {
				break
			}
			_ = docs.Upsert(ctx, o.Docs, tenant, cOutbox, fmt.Sprintf("%08d", n), OutboxItem{ID: fmt.Sprintf("%08d", n), To: e,
				Subject: fmt.Sprintf("[%s] %s %s for %s: %s", set.Firm, j.Name, strings.ToLower(run.Status), date, run.Summary), Ref: j.ID, At: at, Result: "Sent"}, SystemActor)
		}
		run.Notified = strings.Join(to, ", ")
	}
	n, err := o.next(ctx, tenant, "jobrun", SystemActor)
	if err != nil {
		return run, err
	}
	run.ID = fmt.Sprintf("RUN-%07d", n)
	if err := docs.Upsert(ctx, o.Docs, tenant, cJobRun, run.ID, run, SystemActor); err != nil {
		return run, err
	}
	_, _ = docs.Update(ctx, o.Docs, tenant, cJob, j.ID, SystemActor, func(x *Job) error {
		x.LastRun, x.LastDate, x.LastStatus, x.LastSummary = run.Started, start.Format("2006-01-02"), run.Status, run.Summary
		return nil
	})
	o.log(ctx, tenant, actor, "job.run", j.ID, map[string]any{"date": date, "status": run.Status, "summary": run.Summary})
	if jerr != nil {
		return run, fmt.Errorf("%w: %v", errJobFailed, jerr)
	}
	return run, nil
}

func (o *Ops) runJob(ctx context.Context, tenant string, j Job, date string) (string, error) {
	a := SystemActor
	switch j.Kind {
	case "debit_reminders":
		var min int
		fmt.Sscanf(j.Params["min_days"], "%d", &min)
		dv, err := o.Debits(ctx, tenant, date)
		if err != nil {
			return "", err
		}
		n := 0
		for _, r := range dv.Rows {
			if r.Days >= min && r.Debit > 0 {
				o.notify(ctx, tenant, a, "debit.reminder", r.UCC, map[string]string{"debit": formatINR(r.Debit.Dec()), "days": fmt.Sprint(r.Days), "date": date})
				n++
			}
		}
		return fmt.Sprintf("%d clients reminded (in debit %d days or more)", n, min), nil
	case "monthly_statements":
		to := addDays(date[:8]+"01", -1)
		from := to[:8] + "01"
		cs, err := o.ClientMap(ctx, tenant)
		if err != nil {
			return "", err
		}
		n := 0
		for _, u := range sortedKeys(cs) {
			if cs[u].Status != "Active" {
				continue
			}
			st, err := o.App.Ledger.Statement(ctx, tenant, ledger.ClientAccount(u), from, to)
			if err != nil {
				return "", err
			}
			cl := st.Closing
			dc := "Cr"
			if cl > 0 {
				dc = "Dr"
			} else {
				cl = -cl
			}
			o.notify(ctx, tenant, a, "statement.monthly", u, map[string]string{"from": from, "to": to, "closing": "₹" + formatINR(cl.Dec()) + " " + dc})
			n++
		}
		return fmt.Sprintf("%d statements for %s to %s", n, from, to), nil
	case "email_report":
		rows, err := o.ReportRows(ctx, tenant, date, j.Params["report"])
		if err != nil {
			return "", err
		}
		data := csvBytesOf(rows)
		name := fmt.Sprintf("%s_%s.csv", j.Params["report"], date)
		rf, err := o.App.Ingest.Raw.Put(ctx, ingest.RawFile{Tenant: tenant, Source: "REPORT-OUT", Name: name, SHA256: sha256Hex(data), Content: data})
		if err != nil {
			return "", err
		}
		at := o.now().Format("15:04")
		var sent []string
		for _, e := range strings.Split(j.Params["to"], ",") {
			e = strings.TrimSpace(e)
			if !reEmail.MatchString(e) {
				continue
			}
			n, err := o.next(ctx, tenant, "outbox", a)
			if err != nil {
				return "", err
			}
			_ = docs.Upsert(ctx, o.Docs, tenant, cOutbox, fmt.Sprintf("%08d", n), OutboxItem{ID: fmt.Sprintf("%08d", n), To: e, Subject: "Report " + name, Ref: rf.ID, At: at, Result: "Sent"}, a)
			sent = append(sent, e)
		}
		return fmt.Sprintf("%s: %d rows emailed to %s", name, len(rows)-1, strings.Join(sent, ", ")), nil
	case "kra_upload":
		since := j.LastDate
		if since == "" {
			since = date[:8] + "01"
		}
		data, err := o.KRAUploadFile(ctx, tenant, since)
		if err != nil {
			return "", err
		}
		name := fmt.Sprintf("kra_upload_%s_%s.csv", since, date)
		if _, err := o.App.Ingest.Raw.Put(ctx, ingest.RawFile{Tenant: tenant, Source: "KRA-UPLOAD-OUT", Name: name, SHA256: sha256Hex(data), Content: data}); err != nil {
			return "", err
		}
		return fmt.Sprintf("%s with %d clients", name, strings.Count(string(data), "\n")-1), nil
	case "autopilot":
		runs, err := o.RunStage(ctx, tenant, a, date, j.Params["stage"], "")
		if err != nil {
			return "", err
		}
		done, queued, failed := 0, 0, 0
		for _, r := range runs {
			done, queued = done+r.Done, queued+r.Queued
			if r.Error != "" {
				failed++
			}
		}
		s := fmt.Sprintf("%d automations: %d done, %d queued for people", len(runs), done, queued)
		if failed > 0 {
			return s, fmt.Errorf("%s; %d automations failed", s, failed)
		}
		return s, nil
	}
	return "", invalid("unknown job kind %q", j.Kind)
}

// SeedJobs installs the usual schedule, switched off, for a new firm.
func (o *Ops) SeedJobs(ctx context.Context, tenant string) error {
	js, _ := docs.List[Job](ctx, o.Docs, tenant, cJob, "")
	for _, j := range js {
		if !strings.HasPrefix(j.ID, "JOB-AP-") {
			return nil // the firm's schedule exists (the Autopilot jobs alone don't count)
		}
	}
	seed := []Job{
		{Name: "Debit reminders", Kind: "debit_reminders", At: "10:00", When: "business", Params: map[string]string{"min_days": "3"}},
		{Name: "Monthly statements", Kind: "monthly_statements", At: "08:00", When: "monthly:2"},
		{Name: "KRA upload file", Kind: "kra_upload", At: "20:00", When: "business", OnlyFailure: true},
		{Name: "Client ledger balances to management", Kind: "email_report", At: "21:00", When: "business", Params: map[string]string{"report": "ledger_balances", "to": "md@bhairav.example"}},
	}
	for i, j := range seed {
		j.ID = fmt.Sprintf("JOB-%03d", i+1)
		if j.Params == nil {
			j.Params = map[string]string{}
		}
		if err := docs.Upsert(ctx, o.Docs, tenant, cJob, j.ID, j, "system"); err != nil {
			return err
		}
	}
	_, err := o.next(ctx, tenant, "job", "system")
	for i := 1; i < len(seed) && err == nil; i++ {
		_, err = o.next(ctx, tenant, "job", "system")
	}
	return err
}

var _ = money.Amount(0)

func csvBytesOf(rows [][]string) []byte {
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	w.WriteAll(rows)
	return b.Bytes()
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// seedAutopilotJobs adds the autopilot schedule, switched off, so the firm
// turns it on when it is ready.
func (o *Ops) seedAutopilotJobs(ctx context.Context, tenant string) error {
	for _, j := range []Job{
		{ID: "JOB-AP-BOD", Name: "Autopilot: start of day", Kind: "autopilot", At: "08:15", When: "business", Params: map[string]string{"stage": "bod"}},
		{ID: "JOB-AP-MID", Name: "Autopilot: intraday", Kind: "autopilot", At: "12:30", When: "business", Params: map[string]string{"stage": "intraday"}},
		{ID: "JOB-AP-PRE", Name: "Autopilot: pre-close", Kind: "autopilot", At: "15:00", When: "business", Params: map[string]string{"stage": "preclose"}},
		{ID: "JOB-AP-EOD", Name: "Autopilot: end of day", Kind: "autopilot", At: "19:00", When: "business", Params: map[string]string{"stage": "eod"}, OnlyFailure: true},
		{ID: "JOB-AP-MON", Name: "Autopilot: month end", Kind: "autopilot", At: "07:30", When: "daily", Params: map[string]string{"stage": "monthly"}, OnlyFailure: true},
	} {
		if _, _, err := docs.Get[Job](ctx, o.Docs, tenant, cJob, j.ID); err == nil {
			continue
		}
		if err := docs.Upsert(ctx, o.Docs, tenant, cJob, j.ID, j, "system"); err != nil {
			return err
		}
	}
	return nil
}

// SetAutopilotSchedule switches the autopilot jobs on or off together.
func (o *Ops) SetAutopilotSchedule(ctx context.Context, tenant, actor string, on bool) (int, error) {
	if err := o.require(ctx, tenant, actor, "Switching the autopilot schedule", RoleAdmin); err != nil {
		return 0, err
	}
	if o.manual(ctx, tenant) {
		return 0, errManualEdition
	}
	if err := o.setAutopilotJobs(ctx, tenant, on); err != nil {
		return 0, err
	}
	o.log(ctx, tenant, actor, "autopilot.schedule", "", map[string]any{"on": on})
	return 5, nil
}

func (o *Ops) setAutopilotJobs(ctx context.Context, tenant string, on bool) error {
	if err := o.seedAutopilotJobs(ctx, tenant); err != nil {
		return err
	}
	for _, id := range []string{"JOB-AP-BOD", "JOB-AP-MID", "JOB-AP-PRE", "JOB-AP-EOD", "JOB-AP-MON"} {
		if _, err := docs.Update(ctx, o.Docs, tenant, cJob, id, SystemActor, func(j *Job) error { j.Active = on; return nil }); err != nil {
			return err
		}
	}
	return nil
}

// AutopilotScheduled reports whether the autopilot jobs are on.
func (o *Ops) AutopilotScheduled(ctx context.Context, tenant string) bool {
	if o.manual(ctx, tenant) {
		return false
	}
	j, _, err := docs.Get[Job](ctx, o.Docs, tenant, cJob, "JOB-AP-EOD")
	return err == nil && j.Active
}
