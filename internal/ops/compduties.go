package ops

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"ledgerline"
	"ledgerline/internal/docs"
)

// ---- the compliance calendar's catalogue ----
//
// config/compliance/duties.json lists the recurring compliance work of a
// stock broker and depository participant (and of a broker that is also a
// research analyst or investment adviser), item by item as compliance
// calendars set it out: the daily, weekly, fortnightly, monthly, quarterly,
// half-yearly and yearly returns and checks, the event-driven intimations
// and the one-time items. Each entry carries:
//
//   - the circulars in force that set it (issuer, number, date, link and
//     what each says), and the deadline as they state it;
//   - the steps to complete it, when a circular lists them (with where they
//     come from), else the usual practice, marked as practice;
//   - how it is done: through an API Ledgerline has, by Ledgerline itself,
//     as a file Ledgerline makes for a portal upload, on a portal, by an
//     independent auditor, or by the firm outside Ledgerline; and whether an
//     official API exists that Ledgerline does not use yet;
//   - what in Ledgerline does it or checks it (a Periodic work task, a
//     self-inspection test, a screen);
//   - who it applies to, read against the firm profile (a CDSL or NSDL
//     participant, a member of an exchange, a clearing member, MTF, algo or
//     internet-based trading, authorised persons, branches, the CSCRF
//     category, a research analyst or investment adviser registration).
//
// The firm's own state of each duty (switched on or off, when it was done
// and with what evidence, the events recorded) is kept per firm; the
// catalogue's text is applied when the calendar is read, so a corrected
// circular reaches every firm with the next build. A duty the compliance
// officer changed (SaveDuty) keeps their title, due rule, owner and evidence.

// Duty groups: the registration a duty belongs to.
const (
	DutyGroupTrading = "Trading"
	DutyGroupDP      = "DP"
	DutyGroupRAIA    = "RA & IA"
)

// How a duty is done.
const (
	HowAPI        = "api"        // through an API Ledgerline has
	HowLedgerline = "ledgerline" // Ledgerline does it (statements, settlements, registers, checks)
	HowUpload     = "upload"     // Ledgerline makes the file or data; a person submits it on the portal
	HowPortal     = "portal"     // a person does it on a regulator's or exchange's portal
	HowAuditor    = "auditor"    // an independent auditor, chartered accountant or CERT-In auditor
	HowFirm       = "firm"       // the firm's own work outside Ledgerline (board, website, policies)
)

var dutyHows = []string{HowAPI, HowLedgerline, HowUpload, HowPortal, HowAuditor, HowFirm}

// API statuses of a duty.
const (
	APIBuilt     = "built"     // Ledgerline has the connection
	APIAvailable = "available" // an official API exists that Ledgerline does not use yet
	APIFile      = "file"      // no API: the portal takes a file upload
	APIPortal    = "portal"    // no API: a form or a check on a portal
	APINone      = "none"      // nothing to submit, or an auditor's work
)

var dutyAPIStatuses = []string{APIBuilt, APIAvailable, APIFile, APIPortal, APINone}

// DutyRef is a circular behind a duty.
type DutyRef struct {
	Issuer string `json:"issuer"`
	No     string `json:"no"`
	Date   string `json:"date,omitempty"`
	Title  string `json:"title,omitempty"`
	URL    string `json:"url,omitempty"`
	Says   string `json:"says,omitempty"` // what it requires for this duty
}

// DutyAPI says whether a duty can be done through an API.
type DutyAPI struct {
	Status string `json:"status"`
	Name   string `json:"name,omitempty"`
	Detail string `json:"detail,omitempty"`
	URL    string `json:"url,omitempty"`
}

// DutyLink is what in Ledgerline does or checks a duty.
type DutyLink struct {
	Task       string `json:"task,omitempty"`  // a Periodic work task (periodic.go)
	Check      string `json:"check,omitempty"` // a self-inspection test (selfinspect_lib.go)
	Screen     string `json:"screen,omitempty"`
	ScreenName string `json:"screen_name,omitempty"`
	What       string `json:"what,omitempty"` // in words
}

// DutyEvent is one occurrence of an event-driven duty (a change of
// directors, a cyber incident …): what happened and by when it must be done.
type DutyEvent struct {
	ID     string   `json:"id"`
	Date   string   `json:"date"`           // when it happened or was found
	Time   string   `json:"time,omitempty"` // HH:MM, for deadlines in hours
	What   string   `json:"what"`
	Due    string   `json:"due"`              // the date it must be done by
	DueAt  string   `json:"due_at,omitempty"` // the time on that date, for deadlines in hours
	By     string   `json:"by"`
	Logged string   `json:"logged"`
	Done   *DutyRun `json:"done,omitempty"`
}

// DutyCheck is the latest result of the self-inspection test that checks a
// duty.
type DutyCheck struct {
	Test    string `json:"test"`
	Title   string `json:"title,omitempty"`
	Status  string `json:"status"` // pass, fail, skipped, muted, error
	Date    string `json:"date"`   // the business date inspected
	Summary string `json:"summary,omitempty"`
	Run     string `json:"run,omitempty"`
}

// DutyDef is one entry of the catalogue.
type DutyDef struct {
	ID          string    `json:"id"`
	Group       string    `json:"group"`
	Title       string    `json:"title"`
	Authority   string    `json:"authority,omitempty"`
	Frequency   string    `json:"frequency"`
	DueText     string    `json:"due_text"`
	Rule        string    `json:"rule"`
	EventWithin string    `json:"event_within,omitempty"`
	Applies     []string  `json:"applies,omitempty"`
	Role        string    `json:"role"`
	FiledWith   string    `json:"filed_with,omitempty"`
	Evidence    string    `json:"evidence"`
	Refs        []DutyRef `json:"refs"`
	Steps       []string  `json:"steps,omitempty"`
	StepsFrom   string    `json:"steps_from,omitempty"`
	Practice    []string  `json:"practice,omitempty"`
	How         string    `json:"how"`
	API         DutyAPI   `json:"api"`
	Link        *DutyLink `json:"ledgerline,omitempty"`
	Changed     string    `json:"changed,omitempty"`
	Items       []string  `json:"items,omitempty"` // the source calendar's items it covers
	Shared      bool      `json:"shared,omitempty"`
	// Extensions: a regulator's one-off extension of an occurrence (the
	// period as the rule dates it) to a later due date.
	Extensions []DutyExtension `json:"extensions,omitempty"`
}

// DutyExtension moves one occurrence's due date.
type DutyExtension struct {
	Period string `json:"period"`
	Due    string `json:"due"`
	By     string `json:"by"` // the circular that extended it
}

// DutyCatalogue is config/compliance/duties.json.
type DutyCatalogue struct {
	Version int       `json:"version"`
	Updated string    `json:"updated"`
	Source  string    `json:"source"`
	Note    string    `json:"note,omitempty"`
	Duties  []DutyDef `json:"duties"`
}

var (
	dutyCatOnce sync.Once
	dutyCat     DutyCatalogue
	dutyCatErr  error
)

// dutyCatalogue is the catalogue shipped with this build.
func dutyCatalogue() (DutyCatalogue, error) {
	dutyCatOnce.Do(func() {
		b, err := fs.ReadFile(ledgerline.FS, "config/compliance/duties.json")
		if err != nil {
			dutyCatErr = err
			return
		}
		dutyCat, dutyCatErr = parseDutyCatalogue(b)
	})
	return dutyCat, dutyCatErr
}

// parseDutyCatalogue reads and checks a catalogue: every entry must be
// valid, and IDs unique.
func parseDutyCatalogue(b []byte) (DutyCatalogue, error) {
	var c DutyCatalogue
	if err := json.Unmarshal(b, &c); err != nil {
		return DutyCatalogue{}, fmt.Errorf("compliance catalogue: %w", err)
	}
	seen := map[string]bool{}
	var errs []error
	for _, d := range c.Duties {
		if seen[d.ID] {
			errs = append(errs, fmt.Errorf("%s: the ID is used twice", d.ID))
		}
		seen[d.ID] = true
		if err := d.validate(); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", d.ID, err))
		}
	}
	if len(errs) > 0 {
		return DutyCatalogue{}, fmt.Errorf("compliance catalogue: %w", errors.Join(errs...))
	}
	return c, nil
}

var reDutyID = regexp.MustCompile(`^(OB|CC)-[A-Z0-9-]{2,40}$`)

// validate checks an entry.
func (d DutyDef) validate() error {
	switch {
	case !reDutyID.MatchString(d.ID):
		return fmt.Errorf("an ID is OB-… or CC-… in capitals")
	case !contains([]string{DutyGroupTrading, DutyGroupDP, DutyGroupRAIA}, d.Group):
		return fmt.Errorf("unknown group %q", d.Group)
	case strings.TrimSpace(d.Title) == "" || strings.TrimSpace(d.Evidence) == "" || strings.TrimSpace(d.DueText) == "":
		return fmt.Errorf("a duty needs a title, the deadline in words and the evidence to keep")
	case !contains([]string{RoleMaker, RoleChecker, RoleRisk, RoleCompliance, RoleAdmin}, d.Role):
		return fmt.Errorf("unknown owner %q", d.Role)
	case !contains(dutyFrequencies, d.Frequency):
		return fmt.Errorf("unknown frequency %q", d.Frequency)
	case !validDutyRule(d.Rule):
		return fmt.Errorf("unknown due rule %q", d.Rule)
	case (d.Rule == "EVENT") != (d.EventWithin != ""):
		return fmt.Errorf("an event duty (and only one) says how soon it is due after the event")
	case d.EventWithin != "" && !reEventWithin.MatchString(d.EventWithin):
		return fmt.Errorf("unknown event deadline %q", d.EventWithin)
	case !contains(dutyHows, d.How):
		return fmt.Errorf("unknown way of doing it %q", d.How)
	case !contains(dutyAPIStatuses, d.API.Status):
		return fmt.Errorf("unknown API status %q", d.API.Status)
	case len(d.Refs) == 0:
		return fmt.Errorf("a duty needs the circular that sets it")
	case len(d.Steps) > 0 && strings.TrimSpace(d.StepsFrom) == "":
		return fmt.Errorf("the steps must say which circular lists them")
	}
	for _, e := range d.Extensions {
		if e.Period == "" || !validDate(e.Due) || strings.TrimSpace(e.By) == "" {
			return fmt.Errorf("an extension needs the period, the new due date and the circular")
		}
	}
	for _, r := range d.Refs {
		if strings.TrimSpace(r.Issuer) == "" || strings.TrimSpace(r.No) == "" {
			return fmt.Errorf("a circular needs its issuer and number")
		}
		if r.Date != "" && !validDate(r.Date) {
			return fmt.Errorf("circular %s: date %q is not YYYY-MM-DD", r.No, r.Date)
		}
		if r.URL != "" && !strings.HasPrefix(r.URL, "https://") && !strings.HasPrefix(r.URL, "http://") {
			return fmt.Errorf("circular %s: the link must be a web address", r.No)
		}
	}
	for _, n := range d.Applies {
		if !validDutyNeed(n) {
			return fmt.Errorf("unknown condition %q", n)
		}
	}
	if d.Link != nil {
		if d.Link.Task != "" {
			if _, ok := periodicTask(d.Link.Task); !ok {
				return fmt.Errorf("unknown Periodic work task %q", d.Link.Task)
			}
		}
		// The self-inspection test it names is checked by the catalogue's
		// test (siTests depends on the calendar, so not here).
	}
	return nil
}

// dutyFrequencies are the frequencies a duty can have.
var dutyFrequencies = []string{"Daily", "Weekly", "Fortnightly", "Monthly", "Quarterly", "Half-yearly", "Yearly", "Every 2 years", "On event", "One time", "As required", "Settlement day", "Monthly / quarterly"}

// ---- due rules ----
//
// On top of the original rules (circulars.go: Dnn, MM-DD dates, RASM,
// RASQ, NETWORTH, SYSPLAN, SYSPRELIM, SYSATR, DAILY):
//
//	TDAY        each trading day, due the same day
//	TDAY+N      each trading day, due N trading days later
//	WEEK+N      each week, due N trading days after its last trading day
//	WEEKEND     each week, due on its last trading day
//	FORTNIGHT   twice a month: due on the 15th and on the last day
//	MEND        each month, due on its last day
//	RAS+N       N trading days after each running account settlement
//	EVENT       when something happens (event_within: 0, 6h, 1wd, 3d …)
//	ONCE:date   once, by that date; ONCE once, no date
//	ASNEEDED    as and when required: nothing to date
//	AUTO        Ledgerline does it by itself all the time (a register it
//	            keeps, a statement it sends): nothing to mark

var (
	reTDay        = regexp.MustCompile(`^TDAY(\+([1-9]|[12][0-9]|30))?$`)
	reWeek        = regexp.MustCompile(`^WEEK\+([0-9]|10)$`)
	reRASPlus     = regexp.MustCompile(`^RAS\+([0-9]|[12][0-9]|30)$`)
	reOnce        = regexp.MustCompile(`^ONCE(:\d{4}-\d{2}-\d{2})?$`)
	reEventWithin = regexp.MustCompile(`^(0|[1-9][0-9]?h|[1-9][0-9]?wd|[1-9][0-9]{0,2}d)$`)
)

// validDutyRule says whether a due rule is one Ledgerline can date.
func validDutyRule(rule string) bool {
	switch {
	case reDutyMonthly.MatchString(rule), reDutyDates.MatchString(rule), contains(dutyRules, rule):
		return true
	case reTDay.MatchString(rule), reWeek.MatchString(rule), reRASPlus.MatchString(rule):
		return true
	case rule == "WEEKEND", rule == "FORTNIGHT", rule == "MEND", rule == "EVENT", rule == "ASNEEDED", rule == "AUTO":
		return true
	case reOnce.MatchString(rule):
		return rule == "ONCE" || validDate(rule[5:])
	}
	return false
}

// dutyOcc is one time a duty falls due.
type dutyOcc struct {
	Period string
	Due    string
}

// dutyLookback is how far back an unmet duty is still chased, in days.
func dutyLookback(rule string) int {
	switch {
	case reTDay.MatchString(rule), rule == "DAILY":
		return 7
	case reWeek.MatchString(rule), rule == "WEEKEND":
		return 21
	case rule == "FORTNIGHT":
		return 45
	}
	return 120
}

func isTradingDay(set Settings, d string) bool {
	t, err := time.Parse("2006-01-02", d)
	if err != nil || t.Weekday() == time.Saturday || t.Weekday() == time.Sunday {
		return false
	}
	h, _ := set.IsHoliday(d)
	return !h
}

func lastOfMonth(y int, m time.Month) string {
	return time.Date(y, m+1, 0, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
}

// dutyOccurrences lists the times a rule falls due between from and to (by
// due date, in order), at most max of them. ONCE ignores from: a one-time
// duty stays due until it is done, however old its date. EVENT and
// ASNEEDED have none (events are dated by Duties).
func dutyOccurrences(set Settings, prof ComplianceProfile, rule, from, to string, max int) []dutyOcc {
	f, err1 := time.Parse("2006-01-02", from)
	t, err2 := time.Parse("2006-01-02", to)
	if err1 != nil || err2 != nil || to < from {
		return nil
	}
	var out []dutyOcc
	add := func(o dutyOcc) bool {
		if o.Due == "" || (o.Due >= from && o.Due <= to) {
			out = append(out, o)
		}
		return len(out) < max
	}
	switch rule = resolveRule(rule, prof); {
	case reOnce.MatchString(rule):
		if rule == "ONCE" {
			return []dutyOcc{{Period: "once"}}
		}
		if d := rule[5:]; d <= to {
			return []dutyOcc{{Period: "once", Due: d}}
		}
		return nil
	case reTDay.MatchString(rule):
		n := 0
		if i := strings.IndexByte(rule, '+'); i > 0 {
			n, _ = strconv.Atoi(rule[i+1:])
		}
		// A day N trading days back can fall due in the window.
		for d := addDays(from, -(n*2 + 7)); d <= to; d = addDays(d, 1) {
			if !isTradingDay(set, d) {
				continue
			}
			if !add(dutyOcc{Period: d, Due: addTradingDays(set, d, n)}) {
				break
			}
		}
	case rule == "DAILY":
		for d := addDays(from, -1); d <= to; d = addDays(d, 1) {
			if tt, _ := time.Parse("2006-01-02", d); tt.Weekday() == time.Sunday {
				continue
			}
			if !add(dutyOcc{Period: d, Due: addDays(d, 1)}) {
				break
			}
		}
	case reWeek.MatchString(rule), rule == "WEEKEND":
		n := 0
		if rule != "WEEKEND" {
			n, _ = strconv.Atoi(rule[5:])
		}
		mon := f.AddDate(0, 0, -21)
		mon = mon.AddDate(0, 0, -((int(mon.Weekday()) + 6) % 7)) // back to Monday
		for ; !mon.After(t); mon = mon.AddDate(0, 0, 7) {
			last := ""
			for d := mon.AddDate(0, 0, 4); !d.Before(mon); d = d.AddDate(0, 0, -1) {
				if ds := d.Format("2006-01-02"); isTradingDay(set, ds) {
					last = ds
					break
				}
			}
			if last == "" {
				continue // no trading day that week
			}
			y, w := mon.ISOWeek()
			if !add(dutyOcc{Period: fmt.Sprintf("%d-W%02d", y, w), Due: addTradingDays(set, last, n)}) {
				break
			}
		}
	case rule == "FORTNIGHT", rule == "MEND":
		for m := time.Date(f.Year(), f.Month()-1, 1, 0, 0, 0, 0, time.UTC); !m.After(t); m = m.AddDate(0, 1, 0) {
			p := m.Format("2006-01")
			if rule == "FORTNIGHT" {
				if !add(dutyOcc{Period: p + " (1–15)", Due: p + "-15"}) {
					break
				}
				if !add(dutyOcc{Period: p + " (16–end)", Due: lastOfMonth(m.Year(), m.Month())}) {
					break
				}
				continue
			}
			if !add(dutyOcc{Period: p, Due: lastOfMonth(m.Year(), m.Month())}) {
				break
			}
		}
	case reRASPlus.MatchString(rule):
		n, _ := strconv.Atoi(rule[4:])
		seen := map[string]bool{}
		rs := append([]RASDate(nil), prof.RAS...)
		sort.Slice(rs, func(i, j int) bool { return rs[i].Last() < rs[j].Last() })
		for _, r := range rs {
			if seen[r.RunOn] {
				continue // a monthly and a quarterly settlement on the same day: one report
			}
			seen[r.RunOn] = true
			if !add(dutyOcc{Period: "RAS of " + r.RunOn, Due: addTradingDays(set, r.Last(), n)}) {
				break
			}
		}
	case reDutyMonthly.MatchString(rule):
		day, _ := strconv.Atoi(rule[1:])
		for m := time.Date(f.Year(), f.Month()-1, 1, 0, 0, 0, 0, time.UTC); !m.After(t.AddDate(0, 1, 0)); m = m.AddDate(0, 1, 0) {
			next := m.AddDate(0, 1, 0)
			last, _ := time.Parse("2006-01-02", lastOfMonth(next.Year(), next.Month()))
			dd := min(day, last.Day())
			due := time.Date(next.Year(), next.Month(), dd, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
			if !add(dutyOcc{Period: m.Format("2006-01"), Due: due}) {
				break
			}
		}
	case reDutyDates.MatchString(rule):
		var ds []string
		for y := f.Year() - 1; y <= t.Year()+1; y++ {
			for _, md := range strings.Split(rule, ",") {
				md = strings.TrimSpace(md)
				d := fmt.Sprintf("%d-%s", y, md)
				if md == "02-29" && !ledgerValid(d) {
					d = fmt.Sprintf("%d-02-28", y)
				}
				if ledgerValid(d) {
					ds = append(ds, d)
				}
			}
		}
		sort.Strings(ds)
		for _, d := range ds {
			if !add(dutyOcc{Period: d, Due: d}) {
				break
			}
		}
	}
	return out
}

func ledgerValid(d string) bool { _, err := time.Parse("2006-01-02", d); return err == nil }

// eventDue is when an event duty is due after the event: the date, and
// the time for a deadline in hours.
func eventDue(set Settings, within, date, hhmm string) (string, string) {
	switch {
	case within == "0":
		return date, ""
	case strings.HasSuffix(within, "wd"):
		n, _ := strconv.Atoi(strings.TrimSuffix(within, "wd"))
		return addTradingDays(set, date, n), ""
	case strings.HasSuffix(within, "h"):
		n, _ := strconv.Atoi(strings.TrimSuffix(within, "h"))
		at, err := time.Parse("2006-01-02 15:04", date+" "+firstOf(hhmm, "00:00"))
		if err != nil {
			return date, ""
		}
		at = at.Add(time.Duration(n) * time.Hour)
		return at.Format("2006-01-02"), at.Format("15:04")
	case strings.HasSuffix(within, "d"):
		n, _ := strconv.Atoi(strings.TrimSuffix(within, "d"))
		return addDays(date, n), ""
	}
	return date, ""
}

// eventWithinWords says an event deadline in words.
func eventWithinWords(within string) string {
	switch {
	case within == "0":
		return "the same day"
	case strings.HasSuffix(within, "wd"):
		n := strings.TrimSuffix(within, "wd")
		if n == "1" {
			return "by the next working day"
		}
		return "within " + n + " working days"
	case strings.HasSuffix(within, "h"):
		return "within " + strings.TrimSuffix(within, "h") + " hours"
	case strings.HasSuffix(within, "d"):
		return "within " + strings.TrimSuffix(within, "d") + " days"
	}
	return within
}

// ---- who a duty applies to ----

var reDutyNeed = regexp.MustCompile(`^(no:)?(broker|clearing|dp|cdsl|nsdl|mtf|algo|ibt|nri|ap|branch|ra|ia|pms|derivatives|member:(NSE|BSE|MCX|MSE|NCDEX)|seg:(CM|FO|CD|COM)|cscrf:(qualified|mid|small|self|exempt)(,(qualified|mid|small|self|exempt))*)$`)

// validDutyNeed checks one condition ("a|b" for either).
func validDutyNeed(n string) bool {
	for _, p := range strings.Split(n, "|") {
		if !reDutyNeed.MatchString(strings.TrimSpace(p)) {
			return false
		}
	}
	return n != ""
}

// dutyNeed reads one condition against the firm profile. "no:x" holds
// when x does not.
func dutyNeed(n string, f firmFacts) (tri, string) {
	if x, ok := strings.CutPrefix(n, "no:"); ok && !strings.Contains(n, "|") {
		switch v, why := dutyNeed(x, f); v {
		case triYes:
			return triNo, why
		case triNo:
			return triYes, why
		default:
			return triUnknown, why
		}
	}
	if strings.Contains(n, "|") {
		var yes, unk, no []string
		for _, p := range strings.Split(n, "|") {
			switch v, why := dutyNeed(strings.TrimSpace(p), f); v {
			case triYes:
				yes = append(yes, why)
			case triNo:
				no = append(no, why)
			default:
				unk = append(unk, why)
			}
		}
		switch {
		case len(yes) > 0:
			return triYes, yes[0]
		case len(unk) > 0:
			return triUnknown, strings.Join(unk, "; or ")
		}
		return triNo, strings.Join(no, ", and ")
	}
	fact := func(v tri, yes, no, unknown string) (tri, string) {
		switch v {
		case triYes:
			return triYes, yes
		case triNo:
			return triNo, no
		}
		return triUnknown, unknown
	}
	switch {
	case n == "broker":
		return fact(triOf(len(f.Member) > 0), "you are a trading member", "you are not a member of any exchange in Settings", "")
	case strings.HasPrefix(n, "member:"):
		x := n[7:]
		return fact(triOf(f.Member[x]), "you are a member of "+x, "you are not a member of "+x, "")
	case strings.HasPrefix(n, "seg:"):
		x := n[4:]
		name := map[string]string{"CM": "the cash market", "FO": "equity derivatives", "CD": "currency derivatives", "COM": "commodity derivatives"}[x]
		return fact(triOf(f.Seg[x]), "you trade "+name, "you don't trade "+name, "")
	case n == "derivatives":
		return fact(triOf(f.Seg["FO"] || f.Seg["CD"] || f.Seg["COM"]), "you trade derivatives", "you trade no derivatives segment", "")
	case n == "clearing":
		return fact(triOf(len(f.SelfAt) > 0), "you clear your own trades", "another clearing member clears your trades", "")
	case n == "dp":
		return fact(f.dp(), "you are a depository participant", "you are not a depository participant", "tell Ledgerline whether the firm is a CDSL or NSDL participant (Settings → Firm profile)")
	case n == "cdsl":
		return fact(f.CDSL, "you are a CDSL participant", "you are not a CDSL participant", "tell Ledgerline whether the firm is a CDSL participant (Settings → Firm profile)")
	case n == "nsdl":
		return fact(f.NSDL, "you are an NSDL participant", "you are not an NSDL participant", "tell Ledgerline whether the firm is an NSDL participant (Settings → Firm profile)")
	case n == "mtf":
		return fact(f.MTF, "you offer MTF", "you don't offer MTF", "")
	case n == "algo":
		return fact(f.Algo, "you offer algo trading", "you don't offer algo trading", "tell Ledgerline whether the firm runs or offers algo trading (Settings → Firm profile)")
	case n == "ibt":
		return fact(f.IBT, "clients trade through your website or app", "clients don't trade through your website or app", "tell Ledgerline whether clients trade through the firm's website or app (Settings → Firm profile)")
	case n == "nri":
		return fact(f.NRI, "you have NRI clients", "you have no NRI clients", "tell Ledgerline whether the firm has NRI clients")
	case n == "ap":
		return fact(f.AP, "you have authorised persons", "you have no authorised persons", "tell Ledgerline whether the firm has authorised persons")
	case n == "branch":
		return fact(f.Branch, "you have branches", "you have no branches", "tell Ledgerline whether the firm has branches")
	case n == "ra", n == "ia", n == "pms":
		name := map[string]string{"ra": "a research analyst", "ia": "an investment adviser", "pms": "a portfolio manager"}[n]
		return fact(triOf(f.Regs[n]), "you are registered as "+name, "you are not registered as "+name+" (Settings → Firm profile)", "")
	case strings.HasPrefix(n, "cscrf:"):
		if f.CSCRF == "" {
			return triUnknown, "tell Ledgerline the firm's CSCRF category (Settings → Firm profile)"
		}
		if contains(strings.Split(n[6:], ","), f.CSCRF) {
			return triYes, "your CSCRF category is " + cscrfName(f.CSCRF)
		}
		return triNo, "it is not for a " + cscrfName(f.CSCRF)
	}
	return triUnknown, "unknown condition " + n
}

// dutyApplies reads every condition: a duty applies when all hold, does
// not when any fails, and is not sure when the profile doesn't say.
func dutyApplies(needs []string, f firmFacts) Applicability {
	a := Applicability{Verdict: ApplyYes, Reasons: []string{}}
	var unsure []string
	for _, n := range needs {
		switch v, why := dutyNeed(n, f); v {
		case triNo:
			return Applicability{Verdict: ApplyNo, Reason: why, Reasons: []string{why}}
		case triUnknown:
			unsure = append(unsure, why)
		default:
			a.Reasons = append(a.Reasons, why)
		}
	}
	if len(unsure) > 0 {
		a.Verdict, a.Reason, a.Reasons = ApplyUnsure, unsure[0], append(unsure, a.Reasons...)
	} else if len(a.Reasons) > 0 {
		a.Reason = a.Reasons[0]
	}
	return a
}

// ---- the catalogue in the calendar ----

// seed is the duty a firm starts with for an entry.
func (d DutyDef) seed(date string) Duty {
	return Duty{ID: d.ID, Title: d.Title, Authority: d.authority(), Frequency: d.Frequency, Rule: d.Rule, Role: d.Role, Evidence: d.Evidence,
		Active: true, Since: date, Done: []DutyRun{}, Group: d.Group, Rev: dutyRev}
}

// authority names the duty's circulars in short.
func (d DutyDef) authority() string {
	if d.Authority != "" {
		return d.Authority
	}
	var xs []string
	for i, r := range d.Refs {
		if i == 3 {
			xs = append(xs, "…")
			break
		}
		x := r.Issuer
		if r.No != "" && !strings.EqualFold(r.No, r.Issuer) {
			x += " " + r.No
		}
		xs = append(xs, x)
	}
	return strings.Join(xs, "; ")
}

// overlay puts the catalogue's text on a firm's duty. A duty the
// compliance officer changed keeps their title, rule, owner and evidence.
func (d DutyDef) overlay(x *Duty) {
	if !x.Custom {
		x.Title, x.Frequency, x.Rule, x.Role, x.Evidence, x.Authority = d.Title, d.Frequency, d.Rule, d.Role, d.Evidence, d.authority()
		x.DueText, x.EventWithin = d.DueText, d.EventWithin
	}
	x.Group, x.Applies, x.FiledWith, x.Refs, x.Steps, x.StepsFrom, x.Practice = d.Group, d.Applies, d.FiledWith, d.Refs, d.Steps, d.StepsFrom, d.Practice
	x.How, x.Changed, x.Items, x.Shared, x.Extensions = d.How, d.Changed, d.Items, x.Shared || d.Shared, d.Extensions
	api := d.API
	x.API = &api
	if d.Link != nil {
		l := *d.Link
		x.Link = &l
		if x.Task == "" && l.Task != "" {
			x.Task = l.Task
		}
	}
}

// siLatest is each test's result in the latest self-inspection on or
// before date.
func (o *Ops) siLatest(ctx context.Context, tenant, date string) map[string]DutyCheck {
	rs, err := o.siRuns(ctx, tenant)
	if err != nil || len(rs) == 0 {
		return nil
	}
	out := map[string]DutyCheck{}
	for _, r := range rs { // oldest first: a later run replaces an earlier one
		if r.Date > date {
			continue
		}
		for _, x := range r.Results {
			out[x.Test] = DutyCheck{Test: x.Test, Title: x.Title, Status: x.Status, Date: r.Date, Summary: x.Summary, Run: r.ID}
		}
	}
	return out
}

// dueDuty dates a duty from its rule (the rules Duties does not date
// itself): the earliest time it falls due that is not done, which may be
// past.
func dueDuty(set Settings, prof ComplianceProfile, x *Duty, date string) {
	x.NextDue, x.Period, x.DueAt = "", "", ""
	switch x.Rule {
	case "ASNEEDED", "AUTO":
	case "EVENT":
		for _, e := range x.Events {
			if e.Done == nil && (x.NextDue == "" || e.Due < x.NextDue) {
				x.NextDue, x.Period, x.DueAt = e.Due, "event "+e.ID, e.DueAt
			}
		}
	default:
		from := addDays(date, -dutyLookback(x.Rule))
		if x.Since > from {
			from = x.Since
		}
		done := map[string]bool{}
		for _, r := range x.Done {
			done[r.Period] = true
		}
		for _, oc := range dutyOccurrences(set, prof, x.Rule, from, addDays(date, 400), 400) {
			if !done[oc.Period] {
				x.NextDue, x.Period = extendedDue(x.Extensions, oc), oc.Period
				break
			}
		}
		// A one-time duty whose deadline passed before the firm's calendar
		// had it: Ledgerline can't know whether it was done, so it asks for
		// confirmation instead of counting it overdue.
		if x.Period == "once" && x.NextDue != "" && x.Since != "" && x.NextDue < x.Since {
			x.Warning = strings.TrimSpace(x.Warning + " The deadline (" + fmtDay(x.NextDue) + ") was before Ledgerline's calendar had this duty: confirm it was done, with the evidence.")
			x.NextDue = ""
		}
	}
	x.Overdue = x.Active && x.NextDue != "" && x.NextDue < date
}

// dutyStatus says where a duty stands, in words for the screen.
func dutyStatus(x Duty, date string) string {
	switch {
	case x.Applicability != nil && x.Applicability.Verdict == ApplyNo:
		return "Doesn't apply"
	case !x.Active:
		return "Switched off"
	case x.Check != nil && x.Check.Status == SIPass && x.NextDue == "":
		return "Done by Ledgerline"
	case x.Check != nil && x.Check.Status == SIFail:
		return "Ledgerline check failing"
	case x.Rule == "AUTO":
		return "Done by Ledgerline"
	case x.Rule == "ASNEEDED":
		return "As required"
	case x.Rule == "EVENT" && x.NextDue == "" && !x.Overdue:
		return "When it happens"
	case x.Period == "once" && x.NextDue == "":
		return "To confirm"
	case x.NextDue == "":
		return "Done"
	case x.Overdue:
		return "Overdue"
	case x.NextDue == date:
		return "Due today"
	case x.NextDue <= addDays(date, 7):
		return "Due this week"
	}
	return "Upcoming"
}

// extendedDue is an occurrence's due date after any extension.
func extendedDue(ext []DutyExtension, oc dutyOcc) string {
	for _, e := range ext {
		if e.Period == oc.Period && e.Due > oc.Due {
			return e.Due
		}
	}
	return oc.Due
}

// dailyRule: a duty that falls due every trading day.
func dailyRule(rule string) bool { return reTDay.MatchString(rule) || rule == "DAILY" }

// ---- events ----

// DutyEventReq records an event of an event-driven duty.
type DutyEventReq struct {
	Date string `json:"date"`           // when it happened or was found
	Time string `json:"time,omitempty"` // HH:MM, for deadlines in hours
	What string `json:"what"`
	// Due: the date the notice itself gives (an exchange's cut-off), in
	// place of the duty's usual deadline.
	Due string `json:"due,omitempty"`
}

// RecordDutyEvent records that something happened that an event-driven duty
// must answer (a change of directors, a cyber incident): it is due within
// the duty's deadline and goes to its owner's work queue at once.
func (o *Ops) RecordDutyEvent(ctx context.Context, tenant, actor, date, id string, req DutyEventReq) (Duty, error) {
	if !validDate(date) {
		date = o.businessDate(ctx, tenant)
	}
	obs, err := o.Duties(ctx, tenant, date)
	if err != nil {
		return Duty{}, err
	}
	var x *Duty
	for i := range obs {
		if obs[i].ID == id {
			x = &obs[i]
		}
	}
	if x == nil {
		return Duty{}, ErrNotFound
	}
	if err := o.require(ctx, tenant, actor, x.Title, x.Role, RoleCompliance); err != nil {
		return Duty{}, err
	}
	if x.Rule != "EVENT" {
		return Duty{}, invalid("%s is not an event-driven duty", x.Title)
	}
	if !validDate(req.Date) || req.Date > date {
		return Duty{}, invalid("give the date it happened, on or before %s", fmtDay(date))
	}
	if req.Time != "" && !reHHMM.MatchString(req.Time) {
		return Duty{}, invalid("the time is HH:MM")
	}
	if strings.HasSuffix(x.EventWithin, "h") && !strings.HasSuffix(x.EventWithin, "wd") && req.Time == "" {
		return Duty{}, invalid("give the time it was noticed: it is due %s", eventWithinWords(x.EventWithin))
	}
	what := clip(strings.TrimSpace(req.What), 300)
	if what == "" {
		return Duty{}, invalid("say what happened")
	}
	set, _ := o.Settings(ctx, tenant)
	due, at := eventDue(set, x.EventWithin, req.Date, req.Time)
	if req.Due != "" {
		if !validDate(req.Due) || req.Due < req.Date {
			return Duty{}, invalid("the due date the notice gives must be on or after %s", fmtDay(req.Date))
		}
		due, at = req.Due, ""
	}
	var ev DutyEvent
	saved, err := docs.Update(ctx, o.Docs, tenant, cObligation, id, actor, func(y *Duty) error {
		ev = DutyEvent{ID: fmt.Sprintf("E%d", len(y.Events)+1), Date: req.Date, Time: req.Time, What: what, Due: due, DueAt: at, By: actor, Logged: o.now().Format(time.RFC3339)}
		y.Events = append(y.Events, ev)
		return nil
	})
	if err != nil {
		return Duty{}, err
	}
	o.log(ctx, tenant, actor, "obligation.event", id, map[string]any{"event": ev.ID, "date": ev.Date, "due": ev.Due, "due_at": ev.DueAt})
	when := ev.Due + " " + firstOf(ev.DueAt, "18:00")
	title := x.Title + " · due " + fmtDay(ev.Due)
	if ev.DueAt != "" {
		title += " " + ev.DueAt
	}
	rc := &runCtx{o: o, ctx: ctx, tenant: tenant, date: date, process: "compliance", keys: map[string]bool{}}
	rc.queue(id+"|event "+ev.ID, title, fmt.Sprintf("%s (%s). Due %s. Keep: %s. Mark it done on Circulars & calendar.", what, fmtDay(ev.Date), eventWithinWords(x.EventWithin), x.Evidence), "", x.Role, "circulars", when, nil)
	ds, _ := o.Duties(ctx, tenant, date)
	for _, d := range ds {
		if d.ID == id {
			return d, nil
		}
	}
	return saved, nil
}

// ---- the month's compliance sheet ----

// DutySheetRow is one duty in the month's sheet.
type DutySheetRow struct {
	ID        string    `json:"id"`
	Group     string    `json:"group"`
	Title     string    `json:"title"`
	Frequency string    `json:"frequency"`
	DueText   string    `json:"due_text"`
	FiledWith string    `json:"filed_with"`
	How       string    `json:"how"`
	API       string    `json:"api"`
	APIName   string    `json:"api_name,omitempty"`
	Role      string    `json:"role"`
	Dues      []string  `json:"dues"`  // due dates in the month
	Every     string    `json:"every"` // in words, for daily duties
	Done      []DutyRun `json:"done"`  // done in the month
	Status    string    `json:"status"`
	Applies   string    `json:"applies"` // applicability in words
	Verdict   string    `json:"verdict"`
	Authority string    `json:"authority"`
	Items     []string  `json:"items,omitempty"`
	Steps     int       `json:"steps"`
}

// DutySheet is the month's compliance sheet: every duty, in the calendar's
// order, with its due dates in the month.
type DutySheet struct {
	Month  string         `json:"month"`
	Label  string         `json:"label"`
	Rows   []DutySheetRow `json:"rows"`
	Counts map[string]int `json:"counts"`
}

// DutySheetFor builds the sheet for a month (YYYY-MM).
func (o *Ops) DutySheetFor(ctx context.Context, tenant, actor, month, date string) (DutySheet, error) {
	if err := o.require(ctx, tenant, actor, "Viewing the compliance calendar", RoleCompliance, RoleChecker, RoleMaker, RoleRisk); err != nil {
		return DutySheet{}, err
	}
	m, err := time.Parse("2006-01", month)
	if err != nil {
		return DutySheet{}, invalid("the month is YYYY-MM")
	}
	if !validDate(date) {
		date = o.businessDate(ctx, tenant)
	}
	from, to := m.Format("2006-01-02"), lastOfMonth(m.Year(), m.Month())
	ds, err := o.Duties(ctx, tenant, date)
	if err != nil {
		return DutySheet{}, err
	}
	set, _ := o.Settings(ctx, tenant)
	prof := o.CompProfile(ctx, tenant)
	sh := DutySheet{Month: month, Label: m.Format("January 2006"), Rows: []DutySheetRow{}, Counts: map[string]int{}}
	for _, d := range ds {
		r := DutySheetRow{ID: d.ID, Group: firstOf(d.Group, DutyGroupTrading), Title: d.Title, Frequency: d.Frequency, DueText: firstOf(d.DueText, d.Frequency), FiledWith: firstOf(d.FiledWith, d.Filing),
			How: d.How, Role: d.Role, Dues: []string{}, Done: []DutyRun{}, Status: d.Status, Authority: d.Authority, Items: d.Items, Steps: len(d.Steps)}
		if d.API != nil {
			r.API, r.APIName = d.API.Status, d.API.Name
		}
		if d.Applicability != nil {
			r.Applies, r.Verdict = d.Applicability.Label(), d.Applicability.Verdict
		}
		switch {
		case d.Rule == "EVENT":
			for _, e := range d.Events {
				if e.Due >= from && e.Due <= to {
					r.Dues = append(r.Dues, e.Due)
				}
			}
			r.Every = "When it happens: " + eventWithinWords(d.EventWithin)
		case d.Rule == "ASNEEDED":
			r.Every = "As and when required"
		case d.Rule == "AUTO":
			r.Every = "Ledgerline does it by itself"
		case d.Rule == "RASM" || d.Rule == "RASQ":
			kind := map[string]string{"RASM": RASKindMonthly, "RASQ": RASKindQuarterly}[d.Rule]
			for _, x := range prof.RAS {
				if x.Kind == kind && x.Last() >= from && x.Last() <= to {
					r.Dues = append(r.Dues, x.Last())
				}
			}
		default:
			occ := dutyOccurrences(set, prof, d.Rule, from, to, 62)
			if d.Rule == "DAILY" {
				r.Every = fmt.Sprintf("Every day but Sunday (%d in the month)", len(occ))
			} else if dailyRule(d.Rule) {
				r.Every = fmt.Sprintf("Each trading day (%d in the month)", len(occ))
			} else {
				for _, x := range occ {
					if x.Due != "" {
						r.Dues = append(r.Dues, extendedDue(d.Extensions, x))
					}
				}
				if len(occ) == 1 && occ[0].Due == "" {
					r.Every = "One time (no date)"
				}
			}
		}
		for _, run := range d.Done {
			if at := run.At; len(at) >= 10 && at[:10] >= from && at[:10] <= to {
				r.Done = append(r.Done, run)
			}
		}
		sh.Rows = append(sh.Rows, r)
		sh.Counts[r.Group]++
		if r.Verdict != ApplyNo {
			sh.Counts["applies"]++
			if len(r.Dues) > 0 || r.Every != "" && dailyRule(d.Rule) {
				sh.Counts["due_in_month"]++
			}
			if r.API == APIBuilt {
				sh.Counts["api_built"]++
			}
			if r.API == APIAvailable {
				sh.Counts["api_available"]++
			}
		}
	}
	order := map[string]int{DutyGroupTrading: 0, DutyGroupDP: 1, DutyGroupRAIA: 2}
	freq := map[string]int{}
	for i, f := range dutyFrequencies {
		freq[f] = i
	}
	sort.SliceStable(sh.Rows, func(i, j int) bool {
		a, b := sh.Rows[i], sh.Rows[j]
		if order[a.Group] != order[b.Group] {
			return order[a.Group] < order[b.Group]
		}
		if freq[a.Frequency] != freq[b.Frequency] {
			return freq[a.Frequency] < freq[b.Frequency]
		}
		return a.ID < b.ID
	})
	return sh, nil
}

// SheetCSV is the sheet as a CSV file.
func (sh DutySheet) CSV() []byte {
	var b strings.Builder
	w := csv.NewWriter(&b)
	_ = w.Write([]string{"Group", "Sl", "Compliance", "Frequency", "Due (as the circular says)", "Due dates in " + sh.Label, "Filed with", "How it is done", "API", "Owner", "Applies", "Status", "Done in the month", "Circulars"})
	n := map[string]int{}
	how := map[string]string{HowAPI: "By API (Ledgerline)", HowLedgerline: "By Ledgerline", HowUpload: "File from Ledgerline, uploaded on the portal", HowPortal: "On the portal", HowAuditor: "Independent auditor", HowFirm: "The firm"}
	for _, r := range sh.Rows {
		n[r.Group]++
		dues := strings.Join(r.Dues, ", ")
		if r.Every != "" {
			dues = strings.TrimSpace(r.Every + " " + dues)
		}
		var done []string
		for _, x := range r.Done {
			done = append(done, x.At[:10]+" "+x.By)
		}
		api := r.API
		if r.APIName != "" {
			api += ": " + r.APIName
		}
		_ = w.Write([]string{r.Group, strconv.Itoa(n[r.Group]), r.Title, r.Frequency, r.DueText, dues, r.FiledWith, firstOf(how[r.How], r.How), api, r.Role, r.Applies, r.Status, strings.Join(done, "; "), r.Authority})
	}
	w.Flush()
	return []byte(b.String())
}
