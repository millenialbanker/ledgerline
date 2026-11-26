package ops

import (
	"context"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
)

// ---- capacity monitoring the firm can see ----
//
// CSCRF PR.DS.S3 and DE.CM.S4 ask for real-time capacity monitoring of
// critical systems, threshold alerts and a forecast; a broker under the
// technical glitch framework plans the capacity of its trading
// infrastructure and monitors peak load (MCX/TECH/015/2026: two years'
// growth carried three years forward; CYBER-0293, SEBI-NEW-0023). For the
// firm's Ledgerline, Ledgerline measures:
//
//   - load: requests a quarter hour, the busiest minute, the 95th
//     percentile answer time and server errors, from the API server;
//   - data: the size of the newest good backup, its growth and where that
//     growth leads in three years;
//   - backups: how long the last one took (it bounds the RPO);
//   - the server's memory, which every firm on the server shares.
//
// When a threshold the firm sets is crossed, the Admin gets a work item.
// The trading front end, RMS and network are the firm's or its vendors'
// own systems: their capacity is the firm's to monitor.

const cCapacity = "capacity" // by day: the quarter hours

// capWindow counts the current quarter hour of one firm.
type capWindow struct {
	Quarter  string
	Requests int
	Errors   int
	PerMin   map[string]int
	Millis   []int
}

// CapSample is one quarter hour.
type CapSample struct {
	At         string `json:"at"` // the quarter hour, IST
	Requests   int    `json:"requests"`
	Errors     int    `json:"errors"`
	P95Ms      int    `json:"p95_ms"`
	PeakPerMin int    `json:"peak_per_min"`
}

type capDay struct {
	Samples []CapSample `json:"samples"`
}

func capQuarter(t time.Time) string {
	t = t.In(ist)
	return t.Format("2006-01-02T15:") + fmt.Sprintf("%02d", t.Minute()/15*15)
}

// NoteRequest counts one API request of a firm (the API server calls it).
func (o *Ops) NoteRequest(tenant string, d time.Duration, status int) {
	if tenant == "" {
		return
	}
	now := o.now()
	q := capQuarter(now)
	o.capMu.Lock()
	defer o.capMu.Unlock()
	if o.capLive == nil {
		o.capLive = map[string]*capWindow{}
	}
	w := o.capLive[tenant]
	if w == nil || w.Quarter != q {
		if w != nil && w.Requests > 0 {
			o.capDone(tenant, w)
		}
		w = &capWindow{Quarter: q, PerMin: map[string]int{}}
		o.capLive[tenant] = w
	}
	w.Requests++
	if status >= 500 {
		w.Errors++
	}
	w.PerMin[now.In(ist).Format("15:04")]++
	if len(w.Millis) < 5000 {
		w.Millis = append(w.Millis, int(d/time.Millisecond))
	}
}

func (w *capWindow) sample() CapSample {
	s := CapSample{At: w.Quarter, Requests: w.Requests, Errors: w.Errors}
	for _, n := range w.PerMin {
		s.PeakPerMin = max(s.PeakPerMin, n)
	}
	if len(w.Millis) > 0 {
		ms := append([]int(nil), w.Millis...)
		sort.Ints(ms)
		s.P95Ms = ms[min(len(ms)-1, len(ms)*95/100)]
	}
	return s
}

// capDone keeps a finished window for the tick to write, so a request
// never waits for the store (capMu held).
func (o *Ops) capDone(tenant string, w *capWindow) {
	k := tenant + "|done|" + w.Quarter
	o.capLive[k] = w
}

// capacityTick writes the finished quarter hours and raises the alerts.
func (o *Ops) capacityTick(ctx context.Context, tenant string, now time.Time) {
	q := capQuarter(now)
	var done []*capWindow
	o.capMu.Lock()
	for k, w := range o.capLive {
		if k == tenant && w.Quarter != q && w.Requests > 0 {
			done = append(done, w)
			delete(o.capLive, k)
		} else if strings.HasPrefix(k, tenant+"|done|") {
			done = append(done, w)
			delete(o.capLive, k)
		}
	}
	o.capMu.Unlock()
	if len(done) == 0 {
		return
	}
	cs := o.CyberSettingsOf(ctx, tenant)
	for _, w := range done {
		s := w.sample()
		day := s.At[:10]
		var d capDay
		if _, err := upd(ctx, o.Docs, tenant, cCapacity, day, SystemActor, &d, func(x *capDay) error {
			x.Samples = append(x.Samples, s)
			return nil
		}); err != nil {
			continue
		}
		var why []string
		if s.P95Ms > cs.SlowMs {
			why = append(why, fmt.Sprintf("answers took %d ms at the 95th percentile (threshold %d ms)", s.P95Ms, cs.SlowMs))
		}
		if s.PeakPerMin > cs.BusyPerMin {
			why = append(why, fmt.Sprintf("%d requests in the busiest minute (threshold %d)", s.PeakPerMin, cs.BusyPerMin))
		}
		if s.Requests >= 50 && s.Errors*20 > s.Requests {
			why = append(why, fmt.Sprintf("%d of %d requests failed with a server error", s.Errors, s.Requests))
		}
		if len(why) > 0 {
			o.raiseWork(ctx, tenant, "security", "capacity:"+day, "Capacity threshold crossed on "+fmtDay(day),
				"At "+s.At[11:]+" IST: "+joinAnd(why)+". See Security → Cyber controls → Capacity; tell Ledgerline support if it goes on.", "", RoleAdmin, "cyber", day)
		}
	}
}

// CapacityView is what the firm sees.
type CapacityView struct {
	Days       []CapDaySummary `json:"days"` // the last 30 days, newest first
	Today      []CapSample     `json:"today"`
	DataMB     float64         `json:"data_mb"`
	GrowthMBMo float64         `json:"growth_mb_month"`
	In3YearsMB float64         `json:"in_3_years_mb"`
	BackupMin  int             `json:"backup_minutes"`
	MemoryMB   int             `json:"server_memory_mb"`
	Thresholds CyberSettings   `json:"thresholds"`
	Alerts     []string        `json:"alerts"`
	Line       string          `json:"line"`
}

// CapDaySummary is one day's busiest figures.
type CapDaySummary struct {
	Day        string `json:"day"`
	Requests   int    `json:"requests"`
	Errors     int    `json:"errors"`
	P95Ms      int    `json:"p95_ms"` // the worst quarter hour
	PeakPerMin int    `json:"peak_per_min"`
}

func (o *Ops) capacityView(ctx context.Context, tenant string) CapacityView {
	cs := o.CyberSettingsOf(ctx, tenant)
	v := CapacityView{Days: []CapDaySummary{}, Today: []CapSample{}, Thresholds: cs, Alerts: []string{}}
	today := o.today()
	for i := 0; i < 30; i++ {
		day := addDays(today, -i)
		d, _, err := docs.Get[capDay](ctx, o.Docs, tenant, cCapacity, day)
		if err != nil {
			continue
		}
		sm := CapDaySummary{Day: day}
		for _, s := range d.Samples {
			sm.Requests += s.Requests
			sm.Errors += s.Errors
			sm.P95Ms, sm.PeakPerMin = max(sm.P95Ms, s.P95Ms), max(sm.PeakPerMin, s.PeakPerMin)
		}
		if day == today {
			v.Today = d.Samples
		}
		v.Days = append(v.Days, sm)
	}
	// The data: backup sizes over time.
	if o.Backups != nil {
		es, _ := o.Backups.List(tenant)
		type pt struct {
			t  time.Time
			mb float64
		}
		var ps []pt
		for _, e := range es {
			if e.Verified == "" || e.VerifyError != "" || e.Bytes <= 0 {
				continue
			}
			if t, err := time.Parse(time.RFC3339, e.Created); err == nil {
				ps = append(ps, pt{t, float64(e.Bytes) / (1 << 20)})
			}
		}
		sort.Slice(ps, func(i, j int) bool { return ps[i].t.Before(ps[j].t) })
		if len(ps) > 0 {
			v.DataMB = ps[len(ps)-1].mb
			if len(ps) > 1 {
				first, last := ps[0], ps[len(ps)-1]
				if days := last.t.Sub(first.t).Hours() / 24; days >= 7 {
					v.GrowthMBMo = (last.mb - first.mb) / days * 30
				}
			}
			v.In3YearsMB = v.DataMB + max(v.GrowthMBMo, 0)*36
		}
	}
	v.BackupMin = o.rpoStatus(ctx, tenant).BackupMin
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	v.MemoryMB = int(m.Sys >> 20)
	if cs.DataMB > 0 && v.DataMB > float64(cs.DataMB) {
		v.Alerts = append(v.Alerts, fmt.Sprintf("The firm's data is %.0f MB, above the %d MB threshold.", v.DataMB, cs.DataMB))
	}
	if cs.DataMB > 0 && v.In3YearsMB > float64(cs.DataMB) && v.DataMB <= float64(cs.DataMB) {
		v.Alerts = append(v.Alerts, fmt.Sprintf("At the present growth the data reaches %.0f MB in three years, above the %d MB threshold.", v.In3YearsMB, cs.DataMB))
	}
	if v.BackupMin > cs.BackupMaxMin {
		v.Alerts = append(v.Alerts, fmt.Sprintf("The last backup took %d minutes, more than the %d set.", v.BackupMin, cs.BackupMaxMin))
	}
	for _, d := range v.Days {
		if d.Day >= addDays(today, -7) && (d.P95Ms > cs.SlowMs || d.PeakPerMin > cs.BusyPerMin) {
			v.Alerts = append(v.Alerts, fmt.Sprintf("%s: busiest minute %d requests, slowest quarter hour %d ms at the 95th percentile.", fmtDay(d.Day), d.PeakPerMin, d.P95Ms))
		}
	}
	v.Line = "Measured on Ledgerline's own server for this firm. The trading front end, RMS and network are the firm's or its vendors' systems: plan and monitor their capacity with them."
	return v
}
