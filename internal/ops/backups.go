package ops

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"ledgerline/internal/backup"
	"ledgerline/internal/docs"
)

// ---- scheduled backups ----
//
// After a day is closed, Ledgerline backs up the whole firm (see the backup
// package): every table, encrypted with the firm's keys, read back and
// checked before it counts. Each backup is a daily backup; the one taken on
// the last trading day of a week, a quarter or the year is also weekly,
// quarterly or yearly. "Last trading day" uses the approved exchange
// holiday calendar: it is the day whose next business day falls in the
// next week, quarter or year. Each tier keeps its own number of backups,
// so a year-end backup outlives the dailies around it.
//
// If a day is not closed by the fallback time (23:30 by default), it is
// backed up anyway and backed up again when it is closed. A failed backup
// is retried every 15 minutes, up to four times, and management is told.
// Backups run in every edition: they are not automation of the firm's work.

const (
	cBackupRun   = "backuprun"
	cBackupState = "backupstate"
)

// DefaultBackupKeep is how many backups each tier keeps unless the firm
// changes it.
var DefaultBackupKeep = backup.Keep{Daily: 30, Weekly: 13, Quarterly: 12, Yearly: 10, Manual: 10}

// backupKeep returns the firm's counts, defaults filling any unset.
func (s Settings) backupKeep() backup.Keep {
	k := s.BackupKeep
	d := DefaultBackupKeep
	if k.Daily == 0 {
		k.Daily = d.Daily
	}
	if k.Weekly == 0 {
		k.Weekly = d.Weekly
	}
	if k.Quarterly == 0 {
		k.Quarterly = d.Quarterly
	}
	if k.Yearly == 0 {
		k.Yearly = d.Yearly
	}
	if k.Manual == 0 {
		k.Manual = d.Manual
	}
	return k
}

func (s Settings) backupYearEnd() string {
	if s.BackupYearEnd == "december" {
		return "december"
	}
	return "march"
}

func (s Settings) backupFallback() string {
	if _, err := time.Parse("15:04", s.BackupFallback); err == nil {
		return s.BackupFallback
	}
	return "23:30"
}

// backupFallbackCommodity is a time the morning after the trading day.
func (s Settings) backupFallbackCommodity() string {
	if _, err := time.Parse("15:04", s.BackupFallbackCommodity); err == nil {
		return s.BackupFallbackCommodity
	}
	return "00:30"
}

// BackupTiers returns the tiers a backup of date belongs to.
func (s Settings) BackupTiers(date string) []string {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return nil
	}
	tiers := []string{backup.Daily}
	nx, err := time.Parse("2006-01-02", s.NextBusinessDay(date))
	if err != nil {
		return tiers
	}
	if y1, w1 := t.ISOWeek(); true {
		if y2, w2 := nx.ISOWeek(); y1 != y2 || w1 != w2 {
			tiers = append(tiers, backup.Weekly)
		}
	}
	q := func(x time.Time) int { return x.Year()*4 + (int(x.Month())-1)/3 }
	if q(t) != q(nx) {
		tiers = append(tiers, backup.Quarterly)
	}
	yr := func(x time.Time) int { return x.Year() }
	if s.backupYearEnd() == "march" {
		yr = func(x time.Time) int {
			if x.Month() < time.April {
				return x.Year() - 1
			}
			return x.Year()
		}
	}
	if yr(t) != yr(nx) {
		tiers = append(tiers, backup.Yearly)
	}
	return tiers
}

// BackupRun is one attempt, kept for the history.
type BackupRun struct {
	ID      string   `json:"id"`
	Backup  string   `json:"backup,omitempty"`
	Date    string   `json:"date"`
	Part    string   `json:"part,omitempty"`
	Tiers   []string `json:"tiers"`
	Trigger string   `json:"trigger"`
	By      string   `json:"by"`
	Started string   `json:"started"`
	Millis  int64    `json:"ms"`
	Status  string   `json:"status"` // Succeeded, Failed
	Error   string   `json:"error,omitempty"`
	Size    int64    `json:"size,omitempty"`
	Rows    int      `json:"rows,omitempty"`
	Pruned  int      `json:"pruned,omitempty"`
}

// backupDay tracks each business day's backup.
type backupDay struct {
	OK       bool   `json:"ok"`
	Backup   string `json:"backup,omitempty"`
	Closed   bool   `json:"closed"`   // the good backup was taken after the close
	Attempts int    `json:"attempts"` // failures in a row
	Last     string `json:"last,omitempty"`
	Error    string `json:"error,omitempty"`
}

type backupState struct {
	Days map[string]backupDay `json:"days"`
}

var errBackupsOff = errors.New("backups are not set up on this server: set LEDGERLINE_BACKUP_DIR to a folder on a separate disk")

// RunBackup backs up the firm as of date now. It records the attempt,
// prunes old backups when it succeeds and tells management when it fails.
func (o *Ops) RunBackup(ctx context.Context, tenant, actor, date, part, trigger string, tiers []string, closed bool) (BackupRun, error) {
	if o.Backups == nil || o.Sealer == nil || o.App.Snap == nil {
		return BackupRun{}, errBackupsOff
	}
	o.bkMu.Lock()
	if o.bkRunning == nil {
		o.bkRunning = map[string]bool{}
	}
	if o.bkRunning[tenant] {
		o.bkMu.Unlock()
		return BackupRun{}, fmt.Errorf("%w: a backup is already running", ErrState)
	}
	o.bkRunning[tenant] = true
	o.bkMu.Unlock()
	defer func() { o.bkMu.Lock(); delete(o.bkRunning, tenant); o.bkMu.Unlock() }()

	set, _ := o.Settings(ctx, tenant)
	start := o.now()
	run := BackupRun{Date: date, Part: part, Tiers: tiers, Trigger: trigger, By: actor, Started: start.Format(time.RFC3339), Status: "Succeeded"}
	m := backup.Manifest{ID: backup.NewID(date, start), Tenant: tenant, Firm: set.Firm, Date: date, Part: part, Tiers: tiers, Trigger: trigger, By: actor}
	e, err := o.Backups.Create(ctx, o.App.Snap, o.Sealer, m, start)
	run.Backup, run.Millis, run.Size, run.Rows = m.ID, time.Since(start).Milliseconds(), e.Size, e.Total
	if err != nil {
		run.Status, run.Error = "Failed", err.Error()
	} else if es, lerr := o.Backups.List(tenant); lerr == nil {
		for _, x := range backup.Prune(es, set.backupKeep()) {
			if o.Backups.Delete(tenant, x.ID) == nil {
				run.Pruned++
				o.log(ctx, tenant, SystemActor, "backup.deleted", x.ID, map[string]any{"date": x.Date, "tiers": strings.Join(x.Tiers, ",")})
			}
		}
	}
	n, nerr := o.next(ctx, tenant, "backuprun", SystemActor)
	if nerr == nil {
		run.ID = fmt.Sprintf("BR-%06d", n)
		_ = docs.Upsert(ctx, o.Docs, tenant, cBackupRun, run.ID, run, SystemActor)
	}
	if !contains(tiers, backup.Manual) || len(tiers) > 1 {
		o.markBackupDay(ctx, tenant, date+"|"+part, run, closed)
	} else if err == nil {
		// A good manual backup shows the cause of earlier failures is
		// fixed: clear them so the schedule tries again.
		_, _ = docs.Update(ctx, o.Docs, tenant, cBackupState, "main", SystemActor, func(x *backupState) error {
			for k, d := range x.Days {
				d.Attempts, d.Error = 0, ""
				x.Days[k] = d
			}
			return nil
		})
	}
	detail := map[string]any{"date": date, "tiers": strings.Join(tiers, ","), "trigger": trigger, "rows": run.Rows, "bytes": run.Size, "pruned": run.Pruned}
	if err != nil {
		detail["error"] = run.Error
		o.log(ctx, tenant, actor, "backup.failed", m.ID, detail)
		what := date
		if part != "" {
			what += " (" + part + " close)"
		}
		o.tellManagement(ctx, tenant, set, fmt.Sprintf("[%s] Backup FAILED for %s: %s", set.Firm, what, run.Error), m.ID)
		return run, err
	}
	o.log(ctx, tenant, actor, "backup.created", m.ID, detail)
	return run, nil
}

func (o *Ops) tellManagement(ctx context.Context, tenant string, set Settings, subject, ref string) {
	at := o.now().Format("15:04")
	for _, e := range set.ManagementEmails {
		n, err := o.next(ctx, tenant, "outbox", SystemActor)
		if err != nil {
			return
		}
		_ = docs.Upsert(ctx, o.Docs, tenant, cOutbox, fmt.Sprintf("%08d", n), OutboxItem{ID: fmt.Sprintf("%08d", n), To: e, Subject: subject, Ref: ref, At: at, Result: "Sent"}, SystemActor)
	}
}

func (o *Ops) markBackupDay(ctx context.Context, tenant, key string, run BackupRun, closed bool) {
	date := key[:min(10, len(key))]
	upd := func(x *backupState) error {
		if x.Days == nil {
			x.Days = map[string]backupDay{}
		}
		d := x.Days[key]
		d.Attempts++
		d.Last = run.Started
		if run.Status == "Succeeded" {
			// A backup after the close replaces one taken before it.
			if !d.OK || closed || !d.Closed {
				d.OK, d.Backup, d.Closed, d.Error = true, run.Backup, closed, ""
			}
			d.Attempts = 0
		} else {
			d.Error = run.Error
		}
		x.Days[key] = d
		// Forget days older than ten weeks.
		cut := addDays(date, -70)
		for k := range x.Days {
			if k < cut {
				delete(x.Days, k)
			}
		}
		return nil
	}
	if _, err := docs.Update(ctx, o.Docs, tenant, cBackupState, "main", SystemActor, upd); errors.Is(err, docs.ErrNotFound) {
		var s backupState
		_ = upd(&s)
		_ = docs.Upsert(ctx, o.Docs, tenant, cBackupState, "main", s, SystemActor)
	}
}

// backupAfterClose starts the backup that follows a part's close.
func (o *Ops) backupAfterClose(ctx context.Context, tenant, date, part string) {
	if o.Backups == nil {
		return
	}
	set, _ := o.Settings(ctx, tenant)
	tiers := set.BackupTiers(date)
	run := func(ctx context.Context) {
		_, _ = o.RunBackup(ctx, tenant, SystemActor, date, part, "after the "+part+" close", tiers, true)
	}
	if o.BackupSync {
		run(ctx)
		return
	}
	go run(context.WithoutCancel(ctx))
}

// backupTick, every minute, for each part: retries failed backups of
// closed parts, and backs up a part not closed by its fallback time (the
// equity part the same evening, the commodity part the next morning after
// MCX's session).
func (o *Ops) backupTick(ctx context.Context, tenant string, now time.Time) error {
	if o.Backups == nil {
		return nil
	}
	now = now.In(ist)
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return err
	}
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cBackupState, "main", backupState{})
	today, hm := now.Format("2006-01-02"), now.Format("15:04")
	type cand struct {
		date, part string
		fallback   bool // past its fallback time
	}
	var cs []cand
	// Equity: the previous trading day (retries), and today after 23:30.
	cs = append(cs, cand{set.PrevTradingDay(PartEquity, today), PartEquity, false})
	if set.IsTradingDay(PartEquity, today) && hm >= set.backupFallback() {
		cs = append(cs, cand{today, PartEquity, true})
	}
	// Commodity: the session ends near midnight, so the fallback is the
	// next morning, for the last commodity trading day before today.
	cprev := set.PrevTradingDay(PartCommodity, today)
	cs = append(cs, cand{cprev, PartCommodity, hm >= set.backupFallbackCommodity()})
	if pp := set.PrevTradingDay(PartCommodity, cprev); pp != "" {
		cs = append(cs, cand{pp, PartCommodity, false})
	}
	for _, c := range cs {
		if c.date == "" {
			continue
		}
		if need, _ := o.partActive(ctx, tenant, c.date, c.part); !need && c.part == PartCommodity {
			continue
		}
		eod, _ := o.EOD(ctx, tenant, c.date)
		p := eod.part(c.part)
		if p == nil || !p.Needed {
			continue
		}
		bd := st.Days[c.date+"|"+c.part]
		closedNoBackup := p.Closed && (!bd.OK || !bd.Closed)
		failed := bd.Attempts > 0 && !bd.OK
		fallback := c.fallback && !p.Closed && !bd.OK
		if !closedNoBackup && !failed && !fallback {
			continue
		}
		if bd.Attempts >= 4 {
			continue
		}
		if t, err := time.Parse(time.RFC3339, bd.Last); err == nil && now.Sub(t) < 15*time.Minute {
			continue
		}
		trigger := "end of day: the " + c.part + " part was not closed"
		if p.Closed {
			trigger = "after the " + c.part + " close"
			if bd.Attempts > 0 {
				trigger += " (retry)"
			}
		}
		_, _ = o.RunBackup(ctx, tenant, SystemActor, c.date, c.part, trigger, set.BackupTiers(c.date), p.Closed)
	}
	return nil
}

// ---- the screen ----

// BackupsView is the backups screen.
type BackupsView struct {
	Configured bool           `json:"configured"`
	Location   string         `json:"location"`
	Backend    string         `json:"backend"`
	Keep       backup.Keep    `json:"keep"`
	YearEnd    string         `json:"year_end"`
	Fallback   string         `json:"fallback"`
	FallbackC  string         `json:"fallback_commodity"`
	Backups    []backup.Entry `json:"backups"`
	Runs       []BackupRun    `json:"runs"`
	Next       struct {
		Date  string   `json:"date"`
		Tiers []string `json:"tiers"`
	} `json:"next"`
	LastGood *backup.Entry  `json:"last_good"`
	Counts   map[string]int `json:"counts"` // good backups held per tier
	Problems []string       `json:"problems"`
	Running  bool           `json:"running"`
}

// Backups returns the backups screen.
func (o *Ops) BackupsStatus(ctx context.Context, tenant, actor string) (BackupsView, error) {
	if err := o.require(ctx, tenant, actor, "Viewing backups", RoleCompliance, RoleChecker); err != nil {
		return BackupsView{}, err
	}
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return BackupsView{}, err
	}
	v := BackupsView{Keep: set.backupKeep(), YearEnd: set.backupYearEnd(), Fallback: set.backupFallback(), FallbackC: set.backupFallbackCommodity(), Backups: []backup.Entry{}, Runs: []BackupRun{}, Counts: map[string]int{}, Problems: []string{}}
	bd := o.businessDate(ctx, tenant)
	v.Next.Date = bd
	if eod, _, _ := docs.GetOr(ctx, o.Docs, tenant, cEOD, bd, EODState{}); eod.Closed {
		v.Next.Date = set.NextBusinessDay(bd)
	}
	v.Next.Tiers = set.BackupTiers(v.Next.Date)
	if o.Backups == nil || o.App.Snap == nil {
		v.Problems = append(v.Problems, errBackupsOff.Error()+".")
		return v, nil
	}
	v.Configured, v.Location, v.Backend = true, o.Backups.Path, o.App.Snap.Backend()
	o.bkMu.Lock()
	v.Running = o.bkRunning[tenant]
	o.bkMu.Unlock()
	es, err := o.Backups.List(tenant)
	if err != nil {
		v.Problems = append(v.Problems, "The backup folder cannot be read: "+err.Error())
	}
	v.Backups = es
	for i, e := range es {
		if e.Verified == "" || e.VerifyError != "" {
			continue
		}
		if v.LastGood == nil {
			v.LastGood = &es[i]
		}
		for _, t := range e.Tiers {
			v.Counts[t]++
		}
	}
	rs, _ := docs.List[BackupRun](ctx, o.Docs, tenant, cBackupRun, "")
	for i := len(rs) - 1; i >= 0 && len(v.Runs) < 30; i-- {
		v.Runs = append(v.Runs, rs[i])
	}
	v.Problems = append(v.Problems, o.backupProblems(ctx, tenant, set, es)...)
	return v, nil
}

// backupProblems lists what needs attention.
func (o *Ops) backupProblems(ctx context.Context, tenant string, set Settings, es []backup.Entry) []string {
	var out []string
	if o.Backups == nil {
		return out
	}
	var good []backup.Entry
	for _, e := range es {
		if e.VerifyError != "" {
			out = append(out, fmt.Sprintf("The backup of %s (%s) failed its check: %s", e.Date, e.ID, e.VerifyError))
		}
		if e.Verified != "" && e.VerifyError == "" {
			good = append(good, e)
		}
	}
	if len(good) == 0 {
		out = append(out, "There is no good backup yet. The first is taken when a day is closed, or click Back up now.")
		return out
	}
	// Each closed part of the last trading day should be backed up.
	today := o.now().Format("2006-01-02")
	for _, part := range []string{PartEquity, PartCommodity} {
		prev := set.PrevTradingDay(part, today)
		eod, _, _ := docs.GetOr(ctx, o.Docs, tenant, cEOD, prev, EODState{})
		p := eod.part(part)
		if p == nil || !p.Closed {
			continue
		}
		ok := false
		for _, e := range good {
			if e.Date > prev || e.Date == prev && (e.Part == part || e.Part == "") {
				ok = true
				break
			}
		}
		if !ok {
			out = append(out, fmt.Sprintf("The %s close of %s has no good backup; the last good one is of %s.", part, prev, good[0].Date))
		}
	}
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cBackupState, "main", backupState{})
	for _, d := range sortedKeys(st.Days) {
		if x := st.Days[d]; x.Attempts >= 4 {
			out = append(out, fmt.Sprintf("The backup of %s failed four times in a row and is not retried; fix the cause, then click Back up now: %s", strings.Replace(d, "|", " after the ", 1)+" close", x.Error))
		}
	}
	return out
}

// BackupNow takes a backup outside the schedule. It is kept as a manual
// backup and does not count towards the daily, weekly, quarterly or yearly
// backups.
func (o *Ops) BackupNow(ctx context.Context, tenant, actor string) (BackupRun, error) {
	if err := o.require(ctx, tenant, actor, "Taking a backup", RoleAdmin); err != nil {
		return BackupRun{}, err
	}
	return o.RunBackup(ctx, tenant, actor, o.businessDate(ctx, tenant), "", "taken by hand", []string{backup.Manual}, false)
}

// BackupNowAsync checks the user may take a backup and starts it in the
// background; its result appears in the runs.
func (o *Ops) BackupNowAsync(ctx context.Context, tenant, actor string) error {
	if err := o.require(ctx, tenant, actor, "Taking a backup", RoleAdmin); err != nil {
		return err
	}
	if o.Backups == nil || o.Sealer == nil || o.App.Snap == nil {
		return fmt.Errorf("%w: %v", ErrState, errBackupsOff)
	}
	o.bkMu.Lock()
	busy := o.bkRunning[tenant]
	o.bkMu.Unlock()
	if busy {
		return fmt.Errorf("%w: a backup is already running", ErrState)
	}
	go func() { _, _ = o.BackupNow(context.WithoutCancel(ctx), tenant, actor) }()
	return nil
}

// VerifyBackup reads a backup end to end again.
func (o *Ops) VerifyBackup(ctx context.Context, tenant, actor, id string) (backup.Entry, error) {
	if err := o.require(ctx, tenant, actor, "Checking a backup", RoleCompliance, RoleChecker); err != nil {
		return backup.Entry{}, err
	}
	if o.Backups == nil {
		return backup.Entry{}, errBackupsOff
	}
	e, err := o.Backups.Verify(tenant, id, o.Sealer, o.now())
	res := "ok"
	if err != nil {
		res = err.Error()
	}
	o.log(ctx, tenant, actor, "backup.verified", id, map[string]any{"result": res})
	if err != nil {
		return e, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return e, nil
}

// SetBackupPolicy changes how many backups each tier keeps, when the year
// ends for yearly backups, and the fallback time.
func (o *Ops) SetBackupPolicy(ctx context.Context, tenant, actor string, k backup.Keep, yearEnd, fallback, fallbackCommodity string) (Settings, error) {
	if err := o.require(ctx, tenant, actor, "Changing backup settings", RoleAdmin); err != nil {
		return Settings{}, err
	}
	for _, x := range []struct {
		name   string
		v, min int
	}{{"daily", k.Daily, 7}, {"weekly", k.Weekly, 4}, {"quarterly", k.Quarterly, 4}, {"yearly", k.Yearly, 5}, {"manual", k.Manual, 1}} {
		if x.v < x.min || x.v > 400 {
			return Settings{}, invalid("keep between %d and 400 %s backups", x.min, x.name)
		}
	}
	if yearEnd != "march" && yearEnd != "december" {
		return Settings{}, invalid("the year ends in March (financial year) or December")
	}
	if t, err := time.Parse("15:04", fallback); err != nil || t.Hour() < 18 {
		return Settings{}, invalid("the fallback time must be HH:MM, 18:00 or later")
	}
	if fallbackCommodity == "" {
		fallbackCommodity = "00:30"
	}
	if t, err := time.Parse("15:04", fallbackCommodity); err != nil || t.Hour() >= 9 {
		return Settings{}, invalid("the commodity fallback time is the morning after: HH:MM before 09:00")
	}
	s, err := docs.Update(ctx, o.Docs, tenant, cSettings, "main", actor, func(s *Settings) error {
		s.BackupKeep, s.BackupYearEnd, s.BackupFallback, s.BackupFallbackCommodity = k, yearEnd, fallback, fallbackCommodity
		return nil
	})
	if errors.Is(err, docs.ErrNotFound) {
		s = DefaultSettings()
		s.BackupKeep, s.BackupYearEnd, s.BackupFallback, s.BackupFallbackCommodity = k, yearEnd, fallback, fallbackCommodity
		err = docs.Upsert(ctx, o.Docs, tenant, cSettings, "main", s, actor)
	}
	if err != nil {
		return s, err
	}
	o.log(ctx, tenant, actor, "backup.policy", "settings", map[string]any{"daily": k.Daily, "weekly": k.Weekly, "quarterly": k.Quarterly, "yearly": k.Yearly, "manual": k.Manual, "year_end": yearEnd, "fallback": fallback, "fallback_commodity": fallbackCommodity})
	return s, nil
}
