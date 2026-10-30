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
	{"margin_statements", "Daily margin statements", "Emails every client with margin that day their signed margin statement (run after the day's margins are final).", nil},
	{"email_report", "Email a report", "Generates a regulatory or inspection report and emails it to the recipients.", []string{"report", "to"}},
	{"kra_upload", "KRA upload file", "Builds the KRA upload file of clients opened or changed since the last run and keeps it with the files.", nil},
	{"periodic", "Periodic work", "Runs the weekly, monthly and quarterly work that is due and not done (statements, month-end postings, running account settlement, disclosures) and queues what needs a person (such as the BSDA review).", nil},
	{"autopilot", "Autopilot stage", "Runs every automation of a stage (bod, intraday, preclose, eod, commodity, monthly) under the firm's autopilot settings.", []string{"stage"}},
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
	// The statements below are Periodic work (one place, with due dates).
	"weekly_statements":  "Periodic work: Weekly statement of funds and securities",
	"monthly_statements": "Periodic work: Monthly statement of account (the closing balance is in the email)",
	"dp_statements":      "Periodic work: DP transaction and holding statements",
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
	// Backups and the exchange holiday check run in every edition: they
	// keep the firm's data safe and its calendar right, and change nothing
	// in its work (a fetched holiday list waits for approval).
	if err := o.backupTick(ctx, tenant, now); err != nil {
		return 0, fmt.Errorf("backups: %w", err)
	}
	if err := o.holidayTick(ctx, tenant, now); err != nil {
		return 0, fmt.Errorf("exchange holidays: %w", err)
	}
	// Automations: runs whose computer went quiet are closed in every
	// edition; scheduled workflows start except in the Manual edition.
	if err := o.rpaTick(ctx, tenant, now, !o.manual(ctx, tenant)); err != nil {
		return 0, fmt.Errorf("automations: %w", err)
	}
	// Pay-out holds for big sellers: applied after the start-of-day files,
	// released when the clearing corporation's pay-out lands.
	o.payoutHoldTick(ctx, tenant, now)
	// Client allocation: results of requests sent to NSE Clearing are
	// fetched and applied, and money in is sent, while the allocation
	// window is open. Runs only when the firm switched sending on.
	if hm := now.In(ist).Format("15:04"); hm >= "07:00" && hm <= "21:00" {
		if set, _ := o.Settings(ctx, tenant); set.AllocAPILive {
			_, _ = o.AllocationCheck(ctx, tenant, SystemActor)
		}
	}
	// Trades from NSE's trade API, for live risk, when the firm switched
	// fetching on in Connections.
	o.notisTick(ctx, tenant, now)
	o.nclMarginsTick(ctx, tenant, now)
	o.retryMessages(ctx, tenant, now)
	o.extranetTick(ctx, tenant, now)
	// Client registration with the exchanges by API (UCC), when switched on.
	o.nseUCITick(ctx, tenant, now)
	o.bseUCCTick(ctx, tenant, now)
	// Dealers: NISM certificate expiry and open terminals of dealers who left.
	o.dealersTick(ctx, tenant, now)
	// CDSL (DP): demat account requests, transactions, eDIS and report set-ups.
	o.cdslBOTick(ctx, tenant, now)
	o.cdslTxnTick(ctx, tenant, now)
	// Stock SIP instalments, top-ups and fills: orders still go out only
	// through the dealer's basket upload or the firm's OMS connection.
	o.stockSIPTick(ctx, tenant, now)
	// Mutual funds: the AMFI NAV file, platform status and SIP reminders.
	o.mfTick(ctx, tenant, now)
	// Public issues: 4 pm and 5 pm mandate reminders, response files, allotment checks.
	o.ipoTick(ctx, tenant, now)
	if o.manual(ctx, tenant) {
		return 0, nil // Manual edition: none of the firm's jobs run on a timer
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
		subject := fmt.Sprintf("[%s] %s %s for %s: %s", set.Firm, j.Name, strings.ToLower(run.Status), date, run.Summary)
		var told []string
		for _, e := range to {
			if res := o.mailStaff(ctx, tenant, e, subject, subject+"\n\nSee Scheduled jobs in Ledgerline for the run.", j.ID, nil); !strings.HasPrefix(res, "Sent") {
				told = append(told, e+" ("+res+")")
			} else {
				told = append(told, e)
			}
		}
		to = told
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
			if r.Days >= min && r.Debit > 0 && !r.Legacy { // legacy debts get their own reminders
				o.notify(ctx, tenant, a, "debit.reminder", r.UCC, map[string]string{"debit": formatINR(r.Debit.Dec()), "days": fmt.Sprint(r.Days), "date": date})
				n++
			}
		}
		return fmt.Sprintf("%d clients reminded (in debit %d days or more)", n, min), nil
	case "margin_statements":
		res, err := o.SendStatements(ctx, tenant, a, DocMargin, date)
		if err != nil {
			return "", err
		}
		return statementSummary(res), nil
	case "periodic":
		return o.RunDuePeriodic(ctx, tenant, date)
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
		var sent, failed []string
		for _, e := range strings.Split(j.Params["to"], ",") {
			e = strings.TrimSpace(e)
			if !reEmail.MatchString(e) {
				continue
			}
			res := o.mailStaff(ctx, tenant, e, "Report "+name, fmt.Sprintf("%s for %s is attached (%d rows).", j.Params["report"], date, len(rows)-1), rf.ID, []MailFile{{Name: name, Type: "text/csv", Data: data}})
			if strings.HasPrefix(res, "Sent") {
				sent = append(sent, e)
			} else {
				failed = append(failed, e+": "+res)
			}
		}
		if len(failed) > 0 {
			return "", fmt.Errorf("%s not emailed to %s", name, strings.Join(failed, "; "))
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
		if j.Params["stage"] == "commodity" {
			// The last MCX session before today, closing the morning after.
			set, _ := o.Settings(ctx, tenant)
			date = set.PrevTradingDay(PartCommodity, o.now().Format("2006-01-02"))
		}
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
	// IDs stay as first issued: JOB-002 and JOB-004 were the monthly and DP
	// statement jobs, now Periodic work.
	seed := []Job{
		{ID: "JOB-001", Name: "Debit reminders", Kind: "debit_reminders", At: "10:00", When: "business", Params: map[string]string{"min_days": "3"}},
		{ID: "JOB-003", Name: "Daily margin statements", Kind: "margin_statements", At: "21:30", When: "business"},
		{ID: "JOB-005", Name: "KRA upload file", Kind: "kra_upload", At: "20:00", When: "business", OnlyFailure: true},
		{ID: "JOB-006", Name: "Client ledger balances to management", Kind: "email_report", At: "21:00", When: "business", Params: map[string]string{"report": "ledger_balances", "to": "md@bhairav.example"}},
		{ID: "JOB-007", Name: "Periodic work (weekly, monthly, quarterly)", Kind: "periodic", At: "07:45", When: "daily"},
	}
	for _, j := range seed {
		if j.Params == nil {
			j.Params = map[string]string{}
		}
		if err := docs.Upsert(ctx, o.Docs, tenant, cJob, j.ID, j, "system"); err != nil {
			return err
		}
	}
	var err error
	for i := 0; i < 7 && err == nil; i++ { // the counter as if all seven were issued
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
		{ID: "JOB-AP-COM", Name: "Autopilot: commodity close (last MCX session)", Kind: "autopilot", At: "07:00", When: "daily", Params: map[string]string{"stage": "commodity"}, OnlyFailure: true},
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
	return 6, nil
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

func statementSummary(r SendStatementsResult) string {
	return fmt.Sprintf("%s %s: %d sent, %d bounced, %d on paper, %d held, %d failed, %d already sent", DocKindNames[r.Kind], r.Period, r.Sent, r.Bounced, r.Paper, r.Held, r.Failed, r.Skipped)
}

// lastFriday is the latest Friday on or before date.
func lastFriday(date string) string {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return date
	}
	for t.Weekday() != time.Friday {
		t = t.AddDate(0, 0, -1)
	}
	return t.Format("2006-01-02")
}
