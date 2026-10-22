package ops

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"ledgerline/internal/docs"
)

// ---- exchange holiday calendars ----
//
// Exchanges publish next year's trading holidays, and the clearing
// corporations their settlement (clearing) holidays, each December, and
// add days during the year (an election, a sudden closure). Ledgerline
// fetches NSE's lists for every segment from NSE's holiday API, treats
// BSE's equity, derivative and currency lists as NSE's (the two publish
// matching circulars) and takes MCX's, and anything else, from the
// exchange circular uploaded as a CSV.
//
// Nothing fetched or uploaded applies until an Ops checker or the
// compliance officer approves it, because the calendar moves pay-in dates.
// Approved equity trading holidays decide which days are business days;
// settlement holidays also push pay-in. Holidays typed in Settings still
// apply on top.
//
// The check runs every morning in December until next year's list is
// approved, every Monday for changes to this year's, and on demand.

const (
	cHolCal   = "holcal"
	cHolCheck = "holcheck"
)

// Holiday is one exchange holiday.
type Holiday struct {
	Date     string `json:"date"`
	Exchange string `json:"exchange"` // NSE, BSE, MCX
	Segment  string `json:"segment"`  // CM, FO, CD, COM
	Kind     string `json:"kind"`     // trading, settlement
	Name     string `json:"name"`
	Note     string `json:"note,omitempty"`
}

func (h Holiday) key() string { return h.Exchange + "|" + h.Segment + "|" + h.Kind + "|" + h.Date }

// HolidayCalendar is one revision of a year's exchange holidays.
type HolidayCalendar struct {
	ID        string    `json:"id"` // YYYY-NNN
	Year      int       `json:"year"`
	Rev       int       `json:"rev"`
	Status    string    `json:"status"` // Pending, Approved, Rejected, Superseded
	Holidays  []Holiday `json:"holidays"`
	Sources   []string  `json:"sources"`
	Added     []Holiday `json:"added"`   // against the approved calendar when made
	Removed   []Holiday `json:"removed"` // likewise
	By        string    `json:"by"`
	At        string    `json:"at"`
	DecidedBy string    `json:"decided_by,omitempty"`
	DecidedAt string    `json:"decided_at,omitempty"`
	Note      string    `json:"note,omitempty"`
}

// HolidayCheck is the result of the last check for a year.
type HolidayCheck struct {
	Year    int      `json:"year"`
	At      string   `json:"at"`
	By      string   `json:"by"`
	Result  string   `json:"result"`
	Errors  []string `json:"errors,omitempty"`
	Pending string   `json:"pending,omitempty"` // revision raised
}

type holCheckDoc struct {
	Years   map[string]HolidayCheck `json:"years"`
	LastRun string                  `json:"last_run"` // date of the last automatic run
}

// HolidaySource fetches one exchange's lists for a year. It returns no
// holidays and no error when the year is not published yet.
type HolidaySource interface {
	Name() string
	Fetch(ctx context.Context, year int) ([]Holiday, error)
}

var holSegments = []string{"CM", "FO", "CD", "COM"}
var holExchanges = []string{"NSE", "BSE", "MCX"}

// ---- NSE ----

// NSEHolidays reads NSE's holiday API: trading holidays and clearing
// (settlement) holidays for each segment.
type NSEHolidays struct {
	BaseURL string // default https://www.nseindia.com
	HTTP    *http.Client
}

func (n *NSEHolidays) Name() string { return "NSE" }

func (n *NSEHolidays) Fetch(ctx context.Context, year int) ([]Holiday, error) {
	base := strings.TrimRight(n.BaseURL, "/")
	if base == "" {
		base = "https://www.nseindia.com"
	}
	hc := n.HTTP
	if hc == nil {
		jar, _ := cookiejar.New(nil)
		hc = &http.Client{Timeout: 30 * time.Second, Jar: jar}
	}
	get := func(url string) ([]byte, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		// NSE serves its API to browsers; it sets cookies on the home page.
		req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0 Safari/537.36")
		req.Header.Set("Accept", "application/json, text/plain, */*")
		req.Header.Set("Accept-Language", "en-IN,en;q=0.9")
		req.Header.Set("Referer", base+"/resources/exchange-communication-holidays")
		resp, err := hc.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("NSE returned HTTP %d", resp.StatusCode)
		}
		return b, nil
	}
	_, _ = get(base + "/") // cookies
	var out []Holiday
	for _, typ := range []struct{ api, kind string }{{"trading", "trading"}, {"clearing", "settlement"}} {
		b, err := get(base + "/api/holiday-master?type=" + typ.api)
		if err != nil {
			return nil, fmt.Errorf("NSE %s holidays: %w", typ.kind, err)
		}
		hs, err := parseNSEHolidays(b, typ.kind, year)
		if err != nil {
			return nil, fmt.Errorf("NSE %s holidays: %w", typ.kind, err)
		}
		out = append(out, hs...)
	}
	return out, nil
}

// parseNSEHolidays reads the holiday-master JSON: an object of segment
// code to a list of {tradingDate "26-Jan-2026", description, ...}.
func parseNSEHolidays(b []byte, kind string, year int) ([]Holiday, error) {
	var raw map[string][]struct {
		TradingDate string `json:"tradingDate"`
		Description string `json:"description"`
		Morning     string `json:"morning_session"`
		Evening     string `json:"evening_session"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, errors.New("the reply is not the holiday list NSE usually sends")
	}
	var out []Holiday
	seen := 0
	for seg, rows := range raw {
		if !contains(holSegments, seg) {
			continue
		}
		seen++
		for _, r := range rows {
			d, ok := parseHolidayDate(r.TradingDate)
			if !ok {
				return nil, fmt.Errorf("date %q not understood", r.TradingDate)
			}
			if d[:4] != strconv.Itoa(year) {
				continue
			}
			h := Holiday{Date: d, Exchange: "NSE", Segment: seg, Kind: kind, Name: strings.TrimSpace(r.Description)}
			if ev := strings.TrimSpace(r.Evening); ev != "" && ev != "-" && !strings.EqualFold(ev, "closed") {
				h.Note = "evening session " + ev
			}
			out = append(out, h)
		}
	}
	if seen == 0 {
		return nil, errors.New("the reply has no equity, derivative, currency or commodity lists")
	}
	return out, nil
}

var holDateLayouts = []string{"2006-01-02", "02-Jan-2006", "2-Jan-2006", "January 02, 2006", "January 2, 2006", "Jan 02, 2006", "Jan 2, 2006", "02/01/2006", "2/1/2006", "02-01-2006", "02.01.2006", "02 Jan 2006", "2 Jan 2006", "02-January-2006", "Monday, January 02, 2006", "02-Jan-06"}

func parseHolidayDate(s string) (string, bool) {
	s = strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
	for _, l := range holDateLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.Format("2006-01-02"), true
		}
	}
	return "", false
}

// ---- reading calendars ----

func holID(year, rev int) string { return fmt.Sprintf("%04d-%03d", year, rev) }

func (o *Ops) holidayCalendars(ctx context.Context, tenant string, year int) ([]HolidayCalendar, error) {
	cs, err := docs.List[HolidayCalendar](ctx, o.Docs, tenant, cHolCal, fmt.Sprintf("%04d-", year))
	sort.Slice(cs, func(i, j int) bool { return cs[i].Rev < cs[j].Rev })
	return cs, err
}

// approvedAndPending returns a year's approved calendar and the pending
// revision, if any.
func (o *Ops) approvedAndPending(ctx context.Context, tenant string, year int) (approved, pending *HolidayCalendar, revs int, err error) {
	cs, err := o.holidayCalendars(ctx, tenant, year)
	if err != nil {
		return nil, nil, 0, err
	}
	for i := range cs {
		switch cs[i].Status {
		case "Approved":
			approved = &cs[i]
		case "Pending":
			pending = &cs[i]
		}
		revs = max(revs, cs[i].Rev)
	}
	return approved, pending, revs, nil
}

// calDays is the business calendar from approved exchange calendars.
type calDays struct {
	trading, settlement map[string]string
	// MCX commodity trading holidays (closed all day), and the years an
	// approved MCX list covers; other years follow the equity calendar.
	mcx      map[string]string
	mcxYears map[string]bool
	at       time.Time
}

var (
	calMu    sync.Mutex
	calCache = map[*Ops]map[string]calDays{}
)

func (o *Ops) forgetCalendar(tenant string) {
	calMu.Lock()
	defer calMu.Unlock()
	delete(calCache[o], tenant)
}

// exchangeDays returns the equity trading holidays and the settlement
// holidays of every approved year, cached for half a minute.
func (o *Ops) exchangeDays(ctx context.Context, tenant string) calDays {
	calMu.Lock()
	if c, ok := calCache[o][tenant]; ok && time.Since(c.at) < 30*time.Second {
		calMu.Unlock()
		return c
	}
	calMu.Unlock()
	c := calDays{trading: map[string]string{}, settlement: map[string]string{}, mcx: map[string]string{}, mcxYears: map[string]bool{}, at: time.Now()}
	all, err := docs.List[HolidayCalendar](ctx, o.Docs, tenant, cHolCal, "")
	if err != nil {
		return c
	}
	for _, cal := range all {
		if cal.Status != "Approved" {
			continue
		}
		for _, h := range cal.Holidays {
			if h.Exchange == "MCX" && h.Segment == "COM" && h.Kind == "trading" {
				c.mcxYears[h.Date[:4]] = true
				// A day with the evening session open is still a trading day.
				if n := strings.ToLower(h.Note); !strings.Contains(n, "evening session open") && !strings.Contains(n, "evening open") {
					c.mcx[h.Date] = h.Name
				}
				continue
			}
			if h.Segment != "CM" || (h.Exchange != "NSE" && h.Exchange != "BSE") {
				continue
			}
			if h.Kind == "trading" {
				c.trading[h.Date] = h.Name
			} else {
				c.settlement[h.Date] = h.Name
			}
		}
	}
	calMu.Lock()
	if calCache[o] == nil {
		calCache[o] = map[string]calDays{}
	}
	calCache[o][tenant] = c
	calMu.Unlock()
	return c
}

// ---- making revisions ----

func diffHolidays(old, nw []Holiday) (added, removed []Holiday) {
	om, nm := map[string]Holiday{}, map[string]Holiday{}
	for _, h := range old {
		om[h.key()] = h
	}
	for _, h := range nw {
		nm[h.key()] = h
	}
	for k, h := range nm {
		if _, ok := om[k]; !ok {
			added = append(added, h)
		}
	}
	for k, h := range om {
		if _, ok := nm[k]; !ok {
			removed = append(removed, h)
		}
	}
	sortHolidays(added)
	sortHolidays(removed)
	return added, removed
}

func sortHolidays(hs []Holiday) {
	sort.Slice(hs, func(i, j int) bool {
		if hs[i].Date != hs[j].Date {
			return hs[i].Date < hs[j].Date
		}
		return hs[i].key() < hs[j].key()
	})
}

func sameHolidays(a, b []Holiday) bool {
	ad, rm := diffHolidays(a, b)
	if len(ad)+len(rm) > 0 {
		return false
	}
	names := map[string]string{}
	for _, h := range a {
		names[h.key()] = h.Name + "|" + h.Note
	}
	for _, h := range b {
		if names[h.key()] != h.Name+"|"+h.Note {
			return false
		}
	}
	return true
}

// propose raises a pending revision for year with hs, replacing any
// pending one. It returns nil when hs matches what is already there.
func (o *Ops) propose(ctx context.Context, tenant, actor string, year int, hs []Holiday, sources []string, note string) (*HolidayCalendar, error) {
	approved, pending, revs, err := o.approvedAndPending(ctx, tenant, year)
	if err != nil {
		return nil, err
	}
	var base []Holiday
	if approved != nil {
		base = approved.Holidays
	}
	sortHolidays(hs)
	if pending != nil && sameHolidays(pending.Holidays, hs) {
		return nil, nil
	}
	if pending == nil && approved != nil && sameHolidays(base, hs) {
		return nil, nil
	}
	if pending != nil {
		if _, err := docs.Update(ctx, o.Docs, tenant, cHolCal, pending.ID, actor, func(x *HolidayCalendar) error {
			x.Status, x.DecidedBy, x.DecidedAt, x.Note = "Superseded", actor, o.now().Format(time.RFC3339), "replaced by a newer list before it was approved"
			return nil
		}); err != nil {
			return nil, err
		}
	}
	ad, rm := diffHolidays(base, hs)
	cal := HolidayCalendar{ID: holID(year, revs+1), Year: year, Rev: revs + 1, Status: "Pending", Holidays: hs, Sources: sources,
		Added: ad, Removed: rm, By: actor, At: o.now().Format(time.RFC3339), Note: note}
	if cal.Added == nil {
		cal.Added = []Holiday{}
	}
	if cal.Removed == nil {
		cal.Removed = []Holiday{}
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cHolCal, cal.ID, cal, actor); err != nil {
		return nil, err
	}
	o.log(ctx, tenant, actor, "holidays.proposed", cal.ID, map[string]any{"year": year, "added": len(ad), "removed": len(rm), "sources": strings.Join(sources, "; ")})
	return &cal, nil
}

// replaceLists returns base without the (exchange, kind, segment) lists in
// cover, plus add.
func replaceLists(base []Holiday, cover map[string]bool, add []Holiday) []Holiday {
	out := []Holiday{}
	for _, h := range base {
		if !cover[h.Exchange+"|"+h.Kind+"|"+h.Segment] {
			out = append(out, h)
		}
	}
	return append(out, add...)
}

// CheckHolidays fetches a year's lists from the exchanges and raises a
// revision for approval when they differ from what is approved.
func (o *Ops) CheckHolidays(ctx context.Context, tenant, actor string, year int) (HolidayCheck, error) {
	if actor != SystemActor {
		if err := o.require(ctx, tenant, actor, "Checking exchange holidays", RoleMaker, RoleChecker, RoleCompliance); err != nil {
			return HolidayCheck{}, err
		}
	}
	if year < 2000 || year > 2100 {
		return HolidayCheck{}, invalid("year %d is out of range", year)
	}
	chk := HolidayCheck{Year: year, At: o.now().Format(time.RFC3339), By: actor}
	if len(o.HolidaySources) == 0 {
		chk.Result = "No exchange connection on this server: upload the exchange circulars instead."
		return chk, o.saveCheck(ctx, tenant, chk)
	}
	approved, pending, _, err := o.approvedAndPending(ctx, tenant, year)
	if err != nil {
		return chk, err
	}
	var base []Holiday
	switch {
	case pending != nil:
		base = pending.Holidays
	case approved != nil:
		base = approved.Holidays
	}
	cover := map[string]bool{}
	var got []Holiday
	var sources []string
	for _, src := range o.HolidaySources {
		hs, err := src.Fetch(ctx, year)
		if err != nil {
			chk.Errors = append(chk.Errors, err.Error())
			continue
		}
		if len(hs) == 0 {
			chk.Errors = append(chk.Errors, fmt.Sprintf("%s has not published its %d holidays yet", src.Name(), year))
			continue
		}
		sources = append(sources, fmt.Sprintf("%s holiday API, %s", src.Name(), o.now().Format("02-Jan-2006")))
		for _, h := range hs {
			cover[h.Exchange+"|"+h.Kind+"|"+h.Segment] = true
		}
		got = append(got, hs...)
		// BSE publishes the same equity, derivative and currency holidays.
		if src.Name() == "NSE" {
			for _, h := range hs {
				if h.Segment == "COM" {
					continue
				}
				b := h
				b.Exchange, b.Note = "BSE", strings.TrimSpace("as NSE; check against BSE's circular "+h.Note)
				cover["BSE|"+b.Kind+"|"+b.Segment] = true
				got = append(got, b)
			}
		}
	}
	if len(got) == 0 {
		if len(chk.Errors) > 0 {
			chk.Result = "Could not fetch: " + strings.Join(chk.Errors, "; ")
		} else {
			chk.Result = "Nothing published yet"
		}
		return chk, o.saveCheck(ctx, tenant, chk)
	}
	if pending != nil {
		sources = append(append([]string{}, pending.Sources...), sources...)
	} else if approved != nil {
		sources = append(append([]string{}, approved.Sources...), sources...)
	}
	cal, err := o.propose(ctx, tenant, actor, year, replaceLists(base, cover, got), dedupe(sources), "fetched from the exchanges")
	if err != nil {
		return chk, err
	}
	if cal == nil {
		chk.Result = "Up to date: the exchanges' lists match what is approved"
		if pending != nil {
			chk.Result = "No change since the list waiting for approval"
			chk.Pending = pending.ID
		}
	} else {
		chk.Pending = cal.ID
		chk.Result = fmt.Sprintf("Changes found (%d days added or changed, %d removed): waiting for an Ops checker or the compliance officer to approve", holDays(cal.Added), holDays(cal.Removed))
		if approved == nil && pending == nil {
			chk.Result = fmt.Sprintf("%d holiday days fetched: waiting for an Ops checker or the compliance officer to approve", holDays(cal.Holidays))
		}
	}
	return chk, o.saveCheck(ctx, tenant, chk)
}

// holDays counts distinct dates.
func holDays(hs []Holiday) int {
	m := map[string]bool{}
	for _, h := range hs {
		m[h.Date] = true
	}
	return len(m)
}

func dedupe(ss []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func (o *Ops) saveCheck(ctx context.Context, tenant string, c HolidayCheck) error {
	_, err := docs.Update(ctx, o.Docs, tenant, cHolCheck, "status", SystemActor, func(x *holCheckDoc) error {
		if x.Years == nil {
			x.Years = map[string]HolidayCheck{}
		}
		x.Years[strconv.Itoa(c.Year)] = c
		return nil
	})
	if errors.Is(err, docs.ErrNotFound) {
		err = docs.Upsert(ctx, o.Docs, tenant, cHolCheck, "status", holCheckDoc{Years: map[string]HolidayCheck{strconv.Itoa(c.Year): c}}, SystemActor)
	}
	return err
}

// UploadHolidays reads an exchange circular's holiday table (CSV) for one
// exchange and kind and raises a revision for approval. Columns found by
// their headings: Date and Description (or Holiday); Exchange, Segment and
// Type columns, when present, override the form.
func (o *Ops) UploadHolidays(ctx context.Context, tenant, actor string, year int, exchange, kind string, segments []string, data []byte) (*HolidayCalendar, error) {
	if err := o.require(ctx, tenant, actor, "Loading exchange holidays", RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return nil, err
	}
	exchange, kind = strings.ToUpper(strings.TrimSpace(exchange)), strings.ToLower(strings.TrimSpace(kind))
	if !contains(holExchanges, exchange) {
		return nil, invalid("exchange must be NSE, BSE or MCX")
	}
	if kind != "trading" && kind != "settlement" {
		return nil, invalid("choose trading or settlement holidays")
	}
	if len(segments) == 0 {
		return nil, invalid("choose at least one segment")
	}
	for _, s := range segments {
		if !contains(holSegments, s) {
			return nil, invalid("unknown segment %q", s)
		}
	}
	hs, err := parseHolidayCSV(data, year, exchange, kind, segments)
	if err != nil {
		return nil, err
	}
	approved, pending, _, err := o.approvedAndPending(ctx, tenant, year)
	if err != nil {
		return nil, err
	}
	var base []Holiday
	var sources []string
	switch {
	case pending != nil:
		base, sources = pending.Holidays, pending.Sources
	case approved != nil:
		base, sources = approved.Holidays, approved.Sources
	}
	cover := map[string]bool{}
	for _, h := range hs {
		cover[h.Exchange+"|"+h.Kind+"|"+h.Segment] = true
	}
	for _, s := range segments {
		cover[exchange+"|"+kind+"|"+s] = true
	}
	src := fmt.Sprintf("%s %s holidays circular, uploaded by %s on %s", exchange, kind, actor, o.now().Format("02-Jan-2006"))
	cal, err := o.propose(ctx, tenant, actor, year, replaceLists(base, cover, hs), dedupe(append(append([]string{}, sources...), src)), "uploaded from the exchange circular")
	if err != nil {
		return nil, err
	}
	if cal == nil {
		return nil, invalid("these holidays are already in the calendar; nothing changed")
	}
	return cal, nil
}

func parseHolidayCSV(data []byte, year int, exchange, kind string, segments []string) ([]Holiday, error) {
	r := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))))
	r.FieldsPerRecord, r.TrimLeadingSpace, r.LazyQuotes = -1, true, true
	rows, err := r.ReadAll()
	if err != nil {
		return nil, invalid("the file is not a readable CSV: %v", err)
	}
	col := map[string]int{"date": -1, "name": -1, "exchange": -1, "segment": -1, "kind": -1, "note": -1}
	start := -1
	for i, row := range rows {
		for j, c := range row {
			switch h := strings.ToLower(strings.TrimSpace(c)); {
			case h == "date" || h == "holiday date" || h == "dates":
				col["date"] = j
			case h == "description" || h == "holiday" || h == "holidays" || h == "occasion" || h == "name" || h == "festival":
				col["name"] = j
			case h == "exchange":
				col["exchange"] = j
			case h == "segment":
				col["segment"] = j
			case h == "type" || h == "kind":
				col["kind"] = j
			case h == "note" || h == "remarks" || h == "remark":
				col["note"] = j
			}
		}
		if col["date"] >= 0 {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return nil, invalid("no Date column found: the file needs a heading row with Date and Description")
	}
	cell := func(row []string, k string) string {
		if j := col[k]; j >= 0 && j < len(row) {
			return strings.TrimSpace(row[j])
		}
		return ""
	}
	var out []Holiday
	var bad []string
	for _, row := range rows[start:] {
		ds := cell(row, "date")
		if ds == "" {
			continue
		}
		d, ok := parseHolidayDate(ds)
		if !ok {
			bad = append(bad, ds)
			continue
		}
		if d[:4] != strconv.Itoa(year) {
			return nil, invalid("%s is not in %d", ds, year)
		}
		ex, kd, segs := exchange, kind, segments
		if v := strings.ToUpper(cell(row, "exchange")); v != "" {
			ex = v
		}
		if v := strings.ToLower(cell(row, "kind")); v != "" {
			if strings.HasPrefix(v, "settle") || strings.HasPrefix(v, "clear") {
				kd = "settlement"
			} else if strings.HasPrefix(v, "trad") {
				kd = "trading"
			}
		}
		if v := strings.ToUpper(cell(row, "segment")); v != "" {
			segs = []string{v}
		}
		if !contains(holExchanges, ex) || (kd != "trading" && kd != "settlement") {
			return nil, invalid("row %q: exchange must be NSE, BSE or MCX and type trading or settlement", strings.Join(row, ","))
		}
		for _, s := range segs {
			if !contains(holSegments, s) {
				return nil, invalid("row %q: unknown segment %q", strings.Join(row, ","), s)
			}
			out = append(out, Holiday{Date: d, Exchange: ex, Segment: s, Kind: kd, Name: cell(row, "name"), Note: cell(row, "note")})
		}
	}
	if len(bad) > 0 {
		return nil, invalid("dates not understood: %s (use YYYY-MM-DD or 26-Jan-%d)", strings.Join(bad, ", "), year)
	}
	if len(out) == 0 {
		return nil, invalid("no holidays found in the file")
	}
	return out, nil
}

// DecideHolidays approves or rejects a pending revision. The person who
// uploaded a list cannot approve it.
func (o *Ops) DecideHolidays(ctx context.Context, tenant, actor, id string, approve bool, note string) (HolidayCalendar, error) {
	if err := o.require(ctx, tenant, actor, "Approving exchange holidays", RoleChecker, RoleCompliance); err != nil {
		return HolidayCalendar{}, err
	}
	cal, _, err := docs.Get[HolidayCalendar](ctx, o.Docs, tenant, cHolCal, id)
	if err != nil {
		return cal, err
	}
	if cal.Status != "Pending" {
		return cal, fmt.Errorf("%w: this list is %s", ErrState, strings.ToLower(cal.Status))
	}
	if approve && cal.By == actor {
		return cal, fmt.Errorf("%w: you loaded this list, so someone else must approve it", ErrForbidden)
	}
	if !approve && strings.TrimSpace(note) == "" {
		return cal, invalid("say why the list is rejected")
	}
	now := o.now().Format(time.RFC3339)
	if approve {
		approved, _, _, err := o.approvedAndPending(ctx, tenant, cal.Year)
		if err != nil {
			return cal, err
		}
		if approved != nil {
			if _, err := docs.Update(ctx, o.Docs, tenant, cHolCal, approved.ID, actor, func(x *HolidayCalendar) error {
				x.Status = "Superseded"
				return nil
			}); err != nil {
				return cal, err
			}
		}
	}
	cal, err = docs.Update(ctx, o.Docs, tenant, cHolCal, id, actor, func(x *HolidayCalendar) error {
		x.Status, x.DecidedBy, x.DecidedAt = "Rejected", actor, now
		if approve {
			x.Status = "Approved"
		}
		if note != "" {
			x.Note = strings.TrimSpace(note)
		}
		return nil
	})
	if err != nil {
		return cal, err
	}
	o.forgetCalendar(tenant)
	o.log(ctx, tenant, actor, "holidays."+strings.ToLower(cal.Status), id, map[string]any{"year": cal.Year, "holidays": len(cal.Holidays), "added": len(cal.Added), "removed": len(cal.Removed), "note": note})
	return cal, nil
}

// ---- the screen ----

// HolidayYear is one year on the holidays screen.
type HolidayYear struct {
	Year     int               `json:"year"`
	Approved *HolidayCalendar  `json:"approved"`
	Pending  *HolidayCalendar  `json:"pending"`
	Check    *HolidayCheck     `json:"check"`
	Warnings []string          `json:"warnings"`
	History  []HolidayCalendar `json:"history"`
}

// HolidaysView is the exchange holidays screen.
type HolidaysView struct {
	Years     []HolidayYear `json:"years"`
	Connected []string      `json:"connected"` // sources fetched automatically
	Manual    []string      `json:"manual_holidays"`
}

// Holidays returns this year's and next year's calendars and checks.
func (o *Ops) Holidays(ctx context.Context, tenant string) (HolidaysView, error) {
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return HolidaysView{}, err
	}
	v := HolidaysView{Manual: set.Holidays, Connected: []string{}}
	if v.Manual == nil {
		v.Manual = []string{}
	}
	for _, s := range o.HolidaySources {
		v.Connected = append(v.Connected, s.Name())
	}
	chk, _, _ := docs.GetOr(ctx, o.Docs, tenant, cHolCheck, "status", holCheckDoc{})
	y0 := o.now().Year()
	for _, y := range []int{y0, y0 + 1} {
		hy := HolidayYear{Year: y, Warnings: o.holidayWarnings(ctx, tenant, set, y)}
		cs, err := o.holidayCalendars(ctx, tenant, y)
		if err != nil {
			return v, err
		}
		for i := range cs {
			switch cs[i].Status {
			case "Approved":
				hy.Approved = &cs[i]
			case "Pending":
				hy.Pending = &cs[i]
			}
		}
		hy.History = make([]HolidayCalendar, 0, len(cs))
		for i := len(cs) - 1; i >= 0; i-- {
			h := cs[i]
			h.Holidays = nil // the list itself is shown for approved and pending only
			hy.History = append(hy.History, h)
		}
		if c, ok := chk.Years[strconv.Itoa(y)]; ok {
			hy.Check = &c
		}
		v.Years = append(v.Years, hy)
	}
	return v, nil
}

// holidayWarnings explains what is missing for a year.
func (o *Ops) holidayWarnings(ctx context.Context, tenant string, set Settings, year int) []string {
	out := []string{}
	approved, pending, _, err := o.approvedAndPending(ctx, tenant, year)
	if err != nil {
		return out
	}
	now := o.now()
	switch {
	case approved == nil && pending != nil:
		out = append(out, fmt.Sprintf("The %d list is waiting for approval; until then only weekends and the holidays in Settings count.", year))
	case approved == nil && year <= now.Year():
		out = append(out, fmt.Sprintf("No approved exchange holidays for %d: business days and pay-in dates count only weekends and the holidays in Settings.", year))
	case approved == nil && year == now.Year()+1 && now.Month() == time.December && now.Day() >= 15:
		out = append(out, fmt.Sprintf("Next year's (%d) exchange holidays are not loaded yet. The exchanges usually publish them in December.", year))
	}
	if approved != nil {
		has := map[string]bool{}
		for _, h := range approved.Holidays {
			has[h.Exchange+"|"+h.Kind] = true
		}
		for _, ex := range holExchanges {
			if set.MemberCodes[ex] == "" {
				continue
			}
			for _, k := range []string{"trading", "settlement"} {
				if !has[ex+"|"+k] {
					out = append(out, fmt.Sprintf("No %s %s holidays for %d: upload %s's circular.", ex, k, year, ex))
				}
			}
		}
	}
	return out
}

// holidayTick runs the automatic checks once a day after 08:00 IST: every
// day while a year that matters has no approved list (this year, and next
// year from 1 December), and on Mondays to pick up changes.
func (o *Ops) holidayTick(ctx context.Context, tenant string, now time.Time) error {
	now = now.In(ist)
	if now.Hour() < 8 || len(o.HolidaySources) == 0 {
		return nil
	}
	today := now.Format("2006-01-02")
	chk, _, _ := docs.GetOr(ctx, o.Docs, tenant, cHolCheck, "status", holCheckDoc{})
	if chk.LastRun == today {
		return nil
	}
	years := []int{now.Year()}
	if now.Month() == time.December {
		years = append(years, now.Year()+1)
	}
	for _, y := range years {
		approved, pending, _, err := o.approvedAndPending(ctx, tenant, y)
		if err != nil {
			return err
		}
		if (approved == nil && pending == nil) || now.Weekday() == time.Monday {
			if _, err := o.CheckHolidays(ctx, tenant, SystemActor, y); err != nil {
				return err
			}
		}
	}
	_, err := docs.Update(ctx, o.Docs, tenant, cHolCheck, "status", SystemActor, func(x *holCheckDoc) error { x.LastRun = today; return nil })
	if errors.Is(err, docs.ErrNotFound) {
		err = docs.Upsert(ctx, o.Docs, tenant, cHolCheck, "status", holCheckDoc{LastRun: today, Years: map[string]HolidayCheck{}}, SystemActor)
	}
	return err
}
