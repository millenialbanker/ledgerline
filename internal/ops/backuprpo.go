package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/backup"
	"ledgerline/internal/docs"
)

// ---- recovery point: intraday backups and the RPO actually achieved ----
//
// CSCRF RC.RP.S2 and SEBI 2025/119 para 6.10 ask critical systems to lose
// no more than 15 minutes of data (RPO). Ledgerline's backups run after
// each close, so on their own a failure late in the day loses the day's
// work: the RPO is hours. Two ways to do better:
//
//   - Intraday backups: a full, encrypted, checked backup every 5 to 240
//     minutes in the firm's working window (Settings: intraday backups).
//     Each is a complete copy, so a short interval costs disk and some
//     load; the last 16 are kept. The achievable RPO is the interval plus
//     the time a backup takes, so a 15-minute RPO needs 10 minutes or less.
//   - Continuous archiving of the database (PostgreSQL WAL archiving, set
//     up by the operator on the database server). It covers the whole
//     server, not one firm, and Ledgerline cannot restore from it itself;
//     when the database reports it (pg_stat_archiver), Ledgerline shows
//     that it is on and when it last archived. The memory store has none.
//
// What Ledgerline states is measured, not promised: the worst gap between
// good backups in the last 7 days, and the data at risk right now.

// WALArchiver is a store that can say whether continuous archiving is on
// (pgstore.Store).
type WALArchiver interface {
	WALArchive(ctx context.Context) (on bool, last time.Time, failed int64, err error)
}

// RPOStatus is the recovery point as it stands.
type RPOStatus struct {
	TargetMin   int    `json:"target_min"`
	TargetRTO   int    `json:"target_rto_min"`
	Intraday    int    `json:"intraday_minutes"`
	LastGood    string `json:"last_good,omitempty"`
	AtRiskMin   int    `json:"at_risk_min"`    // since the newest good backup
	WorstGapMin int    `json:"worst_gap_min"`  // the worst gap between good backups, last 7 days
	BackupMin   int    `json:"backup_minutes"` // how long the last backup took
	Achievable  int    `json:"achievable_min"` // what the current setup can give
	Meets       bool   `json:"meets"`
	WAL         string `json:"wal"` // on, off, not available
	WALLast     string `json:"wal_last,omitempty"`
	Line        string `json:"line"`
}

// rpoStatus measures the recovery point from the backups.
func (o *Ops) rpoStatus(ctx context.Context, tenant string) RPOStatus {
	cs := o.CyberSettingsOf(ctx, tenant)
	s := RPOStatus{TargetMin: cs.RPOMin, TargetRTO: cs.RTOMin, Intraday: cs.IntradayMinutes, WAL: "not available"}
	now := o.now()
	var good []time.Time
	if o.Backups != nil {
		es, _ := o.Backups.List(tenant)
		for _, e := range es {
			if e.Verified != "" && e.VerifyError == "" {
				if t, err := time.Parse(time.RFC3339, e.Created); err == nil {
					good = append(good, t)
				}
			}
		}
	}
	sort.Slice(good, func(i, j int) bool { return good[i].After(good[j]) })
	if len(good) > 0 {
		s.LastGood = good[0].In(ist).Format(time.RFC3339)
		s.AtRiskMin = int(now.Sub(good[0]) / time.Minute)
		prev := now
		for _, t := range good {
			if now.Sub(t) > 7*24*time.Hour {
				break
			}
			s.WorstGapMin = max(s.WorstGapMin, int(prev.Sub(t)/time.Minute))
			prev = t
		}
	}
	rs, _ := docs.List[BackupRun](ctx, o.Docs, tenant, cBackupRun, "")
	for i := len(rs) - 1; i >= 0; i-- {
		if rs[i].Status == "Succeeded" {
			s.BackupMin = int((time.Duration(rs[i].Millis)*time.Millisecond + time.Minute - 1) / time.Minute)
			break
		}
	}
	if w, ok := any(o.App.Snap).(WALArchiver); ok && o.App.Snap != nil {
		if on, last, failed, err := w.WALArchive(ctx); err == nil {
			s.WAL = map[bool]string{true: "on", false: "off"}[on]
			if !last.IsZero() {
				s.WALLast = last.In(ist).Format(time.RFC3339)
			}
			if failed > 0 {
				s.WAL += fmt.Sprintf(" (%d failures)", failed)
			}
		}
	}
	switch {
	case o.Backups == nil:
		s.Achievable = -1
		s.Line = "Backups are not set up on this server, so nothing can be restored."
	case cs.IntradayMinutes > 0:
		s.Achievable = cs.IntradayMinutes + s.BackupMin
		s.Line = fmt.Sprintf("Intraday backups every %d minutes between %s and %s: a failure in that window loses up to about %d minutes of work.", cs.IntradayMinutes, cs.IntradayFrom, cs.IntradayTo, s.Achievable)
	default:
		s.Achievable = 24 * 60
		s.Line = "Backups run after each day's close: a failure during the day loses the work since the last close, hours rather than minutes. Switch on intraday backups for a shorter RPO."
	}
	if len(good) > 0 {
		s.Line += fmt.Sprintf(" Measured: the worst gap between good backups in the last 7 days was %s; the newest is %s old.", minutesText(s.WorstGapMin), minutesText(s.AtRiskMin))
	}
	if strings.HasPrefix(s.WAL, "on") {
		s.Line += " The database's continuous archive is on (the operator restores the whole server from it to a point in time)."
	}
	s.Meets = s.Achievable >= 0 && s.Achievable <= s.TargetMin
	return s
}

// intradayTick takes the intraday backup when it is due: on a trading
// day, inside the window, the interval after the last one.
func (o *Ops) intradayTick(ctx context.Context, tenant string, now time.Time) {
	if o.Backups == nil || o.Sealer == nil || o.App.Snap == nil {
		return
	}
	cs := o.CyberSettingsOf(ctx, tenant)
	if cs.IntradayMinutes <= 0 {
		return
	}
	now = now.In(ist)
	today, hm := now.Format("2006-01-02"), now.Format("15:04")
	set, _ := o.Settings(ctx, tenant)
	if !set.IsTradingDay(PartEquity, today) || hm < cs.IntradayFrom || hm > cs.IntradayTo {
		return
	}
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cCyberState, "intraday", cyberState{})
	if t, err := time.Parse(time.RFC3339, st.Last); err == nil && now.Sub(t) < time.Duration(cs.IntradayMinutes)*time.Minute {
		return
	}
	st.Last = now.Format(time.RFC3339)
	_ = docs.Upsert(ctx, o.Docs, tenant, cCyberState, "intraday", st, SystemActor)
	run := func(ctx context.Context) {
		_, _ = o.RunBackup(ctx, tenant, SystemActor, today, "", fmt.Sprintf("intraday (every %d minutes)", cs.IntradayMinutes), []string{backup.Intraday}, false)
	}
	if o.BackupSync {
		run(ctx)
		return
	}
	go run(context.WithoutCancel(ctx))
}

type cyberState struct {
	Last string `json:"last"`
}
