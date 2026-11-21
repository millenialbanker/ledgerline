package ops

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"ledgerline/internal/docs"
)

// ---- leave ----
//
// Leave types and their rules are the firm's (HRPolicy.LeaveTypes): days
// granted at the start of the leave year (prorated for joiners), days
// accrued at the end of each month of service, a cap, carry forward and
// encashment at year end (the rest lapses). A request counts working days
// only (weekly offs and holidays are not leave), is approved by the
// person's manager (or HR) and names a backup. Who is on leave on a day,
// and their backup, is exposed for reminders and hand-offs (OnLeave).

const (
	cLeaveReq    = "hr_leave"
	cLeaveLedger = "hr_leaveledger"
)

func parseDays(s string) (float64, error) {
	if strings.TrimSpace(s) == "" {
		return 0, nil
	}
	return strconv.ParseFloat(strings.TrimSpace(s), 64)
}

func days(s string) float64 { d, _ := parseDays(s); return d }

func halfRound(x float64) float64 { return math.Round(x*2) / 2 }

// LeaveRequest is a request for leave.
type LeaveRequest struct {
	ID        string  `json:"id"`
	Employee  string  `json:"employee"`
	Name      string  `json:"name"`
	Type      string  `json:"type"`
	From      string  `json:"from"`
	To        string  `json:"to"`
	Half      bool    `json:"half"` // a half day (From = To)
	Days      float64 `json:"days"`
	Reason    string  `json:"reason"`
	Backup    string  `json:"backup,omitempty"` // employee code covering
	Status    string  `json:"status"`           // Pending, Approved, Rejected, Cancelled
	DecidedBy string  `json:"decided_by,omitempty"`
	Note      string  `json:"note,omitempty"`
	At        string  `json:"at"`
}

// LeaveEntry is a movement in the leave ledger: carried in, encashed,
// lapsed, or adjusted by HR.
type LeaveEntry struct {
	ID       string  `json:"id"`
	Employee string  `json:"employee"`
	Type     string  `json:"type"`
	Year     string  `json:"year"` // the leave year it belongs to
	Kind     string  `json:"kind"` // carry, encash, lapse, adjust
	Days     float64 `json:"days"` // carry and adjust add; encash and lapse are recorded as days taken out
	Note     string  `json:"note,omitempty"`
	PaidIn   string  `json:"paid_in,omitempty"` // encashment: the payroll month that paid it
	By       string  `json:"by"`
	At       string  `json:"at"`
}

// leaveYear is the leave year containing day: its first and last days and
// its label.
func leaveYear(p HRPolicy, day string) (string, string, string) {
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		return "", "", ""
	}
	if p.LeaveYear == "financial" {
		y := t.Year()
		if t.Month() < time.April {
			y--
		}
		return fmt.Sprintf("%d-04-01", y), fmt.Sprintf("%d-03-31", y+1), fmt.Sprintf("%d-%02d", y, (y+1)%100)
	}
	return fmt.Sprintf("%d-01-01", t.Year()), fmt.Sprintf("%d-12-31", t.Year()), fmt.Sprint(t.Year())
}

func leaveType(p HRPolicy, code string) (LeaveType, bool) {
	for _, l := range p.LeaveTypes {
		if strings.EqualFold(l.Code, code) {
			return l, true
		}
	}
	return LeaveType{}, false
}

// workingDays counts a person's working days from..to.
func (o *Ops) workingDays(p HRPolicy, set Settings, e Employee, from, to string) float64 {
	n := 0.0
	t, _ := time.Parse("2006-01-02", from)
	for d := t; d.Format("2006-01-02") <= to; d = d.AddDate(0, 0, 1) {
		if o.dayKind(p, set, e, d.Format("2006-01-02")) == "working" {
			n++
		}
	}
	return n
}

// Balance is one leave type's balance.
type Balance struct {
	Type     string  `json:"type"`
	Name     string  `json:"name"`
	Paid     bool    `json:"paid"`
	Carried  float64 `json:"carried"`
	Granted  float64 `json:"granted"`
	Accrued  float64 `json:"accrued"`
	Taken    float64 `json:"taken"`
	Pending  float64 `json:"pending"`
	Encashed float64 `json:"encashed"`
	Adjusted float64 `json:"adjusted"`
	Balance  float64 `json:"balance"`
	Year     string  `json:"year"`
}

// leaveBalance works out a balance as at asOf.
func (o *Ops) leaveBalance(ctx context.Context, tenant string, p HRPolicy, set Settings, e Employee, lt LeaveType, asOf string, reqs []LeaveRequest, ledger []LeaveEntry) Balance {
	start, end, label := leaveYear(p, asOf)
	b := Balance{Type: lt.Code, Name: lt.Name, Paid: lt.Paid, Year: label}
	if lt.Gender != "" && e.Gender != lt.Gender {
		return b
	}
	for _, x := range ledger {
		if x.Employee != e.Code || x.Type != lt.Code || x.Year != label {
			continue
		}
		switch x.Kind {
		case "carry":
			b.Carried += x.Days
		case "encash", "lapse":
			b.Encashed += x.Days
		case "adjust":
			b.Adjusted += x.Days
		}
	}
	// Granted at the start of the year; a joiner gets the months left,
	// counting the joining month when they joined by the 15th.
	if a := days(lt.Annual); a > 0 {
		if e.JoinedOn <= start {
			b.Granted = a
		} else if e.JoinedOn <= end {
			j, _ := time.Parse("2006-01-02", e.JoinedOn)
			s, _ := time.Parse("2006-01-02", start)
			months := 12 - ((j.Year()-s.Year())*12 + int(j.Month()) - int(s.Month()))
			if j.Day() > 15 {
				months--
			}
			b.Granted = halfRound(a * float64(months) / 12)
		}
	}
	// Accrued at the end of each month of service within the year, up
	// to asOf (the joining month counts when joined by the 15th).
	if m := days(lt.Monthly); m > 0 {
		s, _ := time.Parse("2006-01-02", start)
		for mo := s; mo.Format("2006-01-02") <= end; mo = mo.AddDate(0, 1, 0) {
			last := mo.AddDate(0, 1, -1).Format("2006-01-02")
			if last > asOf {
				break
			}
			first := mo.Format("2006-01-02")
			joinedBy := e.JoinedOn <= first || (e.JoinedOn[:7] == first[:7] && e.JoinedOn[8:] <= "15")
			if joinedBy && (e.LeftOn == "" || e.LeftOn >= last) {
				b.Accrued += m
			}
		}
	}
	credit := b.Carried + b.Granted + b.Accrued + b.Adjusted
	if mx := days(lt.MaxBalance); mx > 0 && credit > mx {
		credit = mx
	}
	for _, r := range reqs {
		if r.Employee != e.Code || !strings.EqualFold(r.Type, lt.Code) || r.From > end || r.To < start {
			continue
		}
		switch r.Status {
		case "Approved":
			b.Taken += r.Days
		case "Pending":
			b.Pending += r.Days
		}
	}
	b.Balance = credit - b.Taken - b.Encashed
	return b
}

// Balances are a person's balances on a day.
func (o *Ops) balances(ctx context.Context, tenant string, e Employee, asOf string) []Balance {
	p := o.hrPolicy(ctx, tenant)
	set, _ := o.Settings(ctx, tenant)
	reqs, _ := docs.List[LeaveRequest](ctx, o.Docs, tenant, cLeaveReq, "")
	led, _ := docs.List[LeaveEntry](ctx, o.Docs, tenant, cLeaveLedger, e.Code+"|")
	out := []Balance{}
	for _, lt := range p.LeaveTypes {
		if lt.Gender != "" && lt.Gender != e.Gender {
			continue
		}
		out = append(out, o.leaveBalance(ctx, tenant, p, set, e, lt, asOf, reqs, led))
	}
	return out
}

// ApplyLeave is a person's request for leave.
func (o *Ops) ApplyLeave(ctx context.Context, tenant, actor string, r LeaveRequest) (LeaveRequest, error) {
	if err := o.needModule(ctx, tenant, ModHRMS); err != nil {
		return r, err
	}
	e, ok := o.employeeOf(ctx, tenant, actor)
	if !ok {
		return r, fmt.Errorf("%w: your login is not linked to an employee", ErrForbidden)
	}
	p := o.hrPolicy(ctx, tenant)
	set, _ := o.Settings(ctx, tenant)
	lt, ok := leaveType(p, r.Type)
	if !ok {
		return r, invalid("choose a leave type")
	}
	if lt.Gender != "" && lt.Gender != e.Gender {
		return r, invalid("%s is not available to you", lt.Name)
	}
	if r.To == "" {
		r.To = r.From
	}
	if !validDay(r.From) || !validDay(r.To) || r.To < r.From {
		return r, invalid("give the first and last day (YYYY-MM-DD)")
	}
	if r.Half && r.From != r.To {
		return r, invalid("a half day is one day")
	}
	if leaveYearLabel(p, r.From) != leaveYearLabel(p, r.To) {
		return r, invalid("split leave that runs into the next leave year into two requests")
	}
	r.Days = o.workingDays(p, set, e, r.From, r.To)
	if r.Half {
		r.Days = 0.5 * r.Days
	}
	if r.Days <= 0 {
		return r, invalid("those days are holidays or weekly offs: no leave is needed")
	}
	reqs, _ := docs.List[LeaveRequest](ctx, o.Docs, tenant, cLeaveReq, "")
	for _, x := range reqs {
		if x.Employee == e.Code && (x.Status == "Pending" || x.Status == "Approved") && x.From <= r.To && r.From <= x.To {
			return r, invalid("you already have leave %s for some of these days", x.ID)
		}
	}
	if !lt.Negative {
		led, _ := docs.List[LeaveEntry](ctx, o.Docs, tenant, cLeaveLedger, e.Code+"|")
		_, end, _ := leaveYear(p, r.From)
		b := o.leaveBalance(ctx, tenant, p, set, e, lt, min(end, max(r.To, o.today())), reqs, led)
		if b.Balance-b.Pending < r.Days {
			return r, invalid("%s balance is %.1f day(s) (%.1f waiting for approval); this needs %.1f", lt.Name, b.Balance, b.Pending, r.Days)
		}
	}
	r.Backup = strings.ToUpper(strings.TrimSpace(r.Backup))
	if r.Backup != "" {
		b, err := o.employee(ctx, tenant, r.Backup)
		if err != nil || b.Status == EmpLeft || b.Code == e.Code {
			return r, invalid("the backup must be another person on the staff")
		}
	}
	n, err := o.next(ctx, tenant, "hr-leave", actor)
	if err != nil {
		return r, err
	}
	r.ID, r.Employee, r.Name, r.Type, r.Status, r.At, r.Reason = fmt.Sprintf("LV-%05d", n), e.Code, e.Name, lt.Code, "Pending", o.now().Format(time.RFC3339), trim(strings.TrimSpace(r.Reason), 300)
	r.DecidedBy, r.Note = "", ""
	if _, err := docs.Put(ctx, o.Docs, tenant, cLeaveReq, r.ID, r, 0, actor); err != nil {
		return r, err
	}
	o.log(ctx, tenant, actor, "hr.leave.applied", r.ID, map[string]any{"type": r.Type, "from": r.From, "to": r.To, "days": r.Days})
	o.notifyManager(ctx, tenant, e, "leave:"+r.ID, fmt.Sprintf("Leave %s: %s (%s), %s %s to %s", r.ID, e.Name, e.Code, lt.Name, fmtDay(r.From), fmtDay(r.To)), "Approve or reject on People → My team.")
	return r, nil
}

func leaveYearLabel(p HRPolicy, d string) string { _, _, l := leaveYear(p, d); return l }

// DecideLeave approves or rejects: the person's manager, or HR; never
// one's own.
func (o *Ops) DecideLeave(ctx context.Context, tenant, actor, id string, approve bool, note string) (LeaveRequest, error) {
	if err := o.needModule(ctx, tenant, ModHRMS); err != nil {
		return LeaveRequest{}, err
	}
	sc, err := o.scopeFor(ctx, tenant, actor)
	if err != nil {
		return LeaveRequest{}, err
	}
	r, err := docs.Update(ctx, o.Docs, tenant, cLeaveReq, id, actor, func(r *LeaveRequest) error {
		if r.Employee == sc.self {
			return fmt.Errorf("%w: someone else approves your own leave", ErrForbidden)
		}
		if !(sc.team[r.Employee] || sc.role == RoleHR || sc.role == RoleAdmin) {
			return fmt.Errorf("%w: leave is approved by the person's manager or HR", ErrForbidden)
		}
		if r.Status != "Pending" {
			return fmt.Errorf("%w: %s is %s", ErrState, r.ID, strings.ToLower(r.Status))
		}
		if !approve && strings.TrimSpace(note) == "" {
			return invalid("give the reason for rejecting")
		}
		r.Status, r.DecidedBy, r.Note = map[bool]string{true: "Approved", false: "Rejected"}[approve], actor, trim(note, 300)
		return nil
	})
	if err != nil {
		return r, err
	}
	o.closeWork(ctx, tenant, actor, wHR, "leave:"+r.ID, strings.ToLower(r.Status))
	o.log(ctx, tenant, actor, "hr.leave."+strings.ToLower(r.Status), r.ID, map[string]any{"employee": r.Employee, "days": r.Days})
	return r, nil
}

// CancelLeave withdraws a request: the person (while pending or before it
// starts) or HR.
func (o *Ops) CancelLeave(ctx context.Context, tenant, actor, id string) (LeaveRequest, error) {
	if err := o.needModule(ctx, tenant, ModHRMS); err != nil {
		return LeaveRequest{}, err
	}
	sc, err := o.scopeFor(ctx, tenant, actor)
	if err != nil {
		return LeaveRequest{}, err
	}
	today := o.today()
	r, err := docs.Update(ctx, o.Docs, tenant, cLeaveReq, id, actor, func(r *LeaveRequest) error {
		hr := sc.role == RoleHR || sc.role == RoleAdmin
		if r.Employee != sc.self && !hr {
			return ErrNotFound
		}
		if r.Status != "Pending" && !(r.Status == "Approved" && (r.From > today || hr)) {
			return fmt.Errorf("%w: %s cannot be cancelled now", ErrState, r.ID)
		}
		r.Status = "Cancelled"
		return nil
	})
	if err == nil {
		o.closeWork(ctx, tenant, actor, wHR, "leave:"+r.ID, "cancelled")
		o.log(ctx, tenant, actor, "hr.leave.cancelled", r.ID, nil)
	}
	return r, err
}

// AdjustLeave adds (or takes away) days by hand, with a reason. HR.
func (o *Ops) AdjustLeave(ctx context.Context, tenant, actor, code, typ string, d float64, note string) (LeaveEntry, error) {
	if err := o.needModule(ctx, tenant, ModHRMS); err != nil {
		return LeaveEntry{}, err
	}
	if err := o.require(ctx, tenant, actor, "Adjusting leave balances", RoleHR); err != nil {
		return LeaveEntry{}, err
	}
	p := o.hrPolicy(ctx, tenant)
	if _, ok := leaveType(p, typ); !ok || d == 0 || strings.TrimSpace(note) == "" {
		return LeaveEntry{}, invalid("give the leave type, days (not zero) and the reason")
	}
	e, err := o.employee(ctx, tenant, code)
	if err != nil {
		return LeaveEntry{}, err
	}
	n, err := o.next(ctx, tenant, "hr-leaveledger", actor)
	if err != nil {
		return LeaveEntry{}, err
	}
	x := LeaveEntry{ID: fmt.Sprintf("%s|%06d", e.Code, n), Employee: e.Code, Type: strings.ToUpper(typ), Year: leaveYearLabel(p, o.today()), Kind: "adjust", Days: d, Note: trim(note, 300), By: actor, At: o.now().Format(time.RFC3339)}
	if err := docs.Upsert(ctx, o.Docs, tenant, cLeaveLedger, x.ID, x, actor); err != nil {
		return x, err
	}
	o.log(ctx, tenant, actor, "hr.leave.adjusted", e.Code, map[string]any{"type": x.Type, "days": d, "note": x.Note})
	return x, nil
}

// YearCloseRow is what closing the leave year did for a person and type.
type YearCloseRow struct {
	Employee string  `json:"employee"`
	Type     string  `json:"type"`
	Closing  float64 `json:"closing"`
	Carried  float64 `json:"carried"`
	Encashed float64 `json:"encashed"`
	Lapsed   float64 `json:"lapsed"`
}

// CloseLeaveYear closes the leave year that ends on yearEnd: each balance
// is carried forward up to the type's limit, encashed up to its limit (paid
// in the next payroll) and the rest lapses. Once per year. HR.
func (o *Ops) CloseLeaveYear(ctx context.Context, tenant, actor, yearEnd string) ([]YearCloseRow, error) {
	if err := o.needModule(ctx, tenant, ModHRMS); err != nil {
		return nil, err
	}
	if err := o.require(ctx, tenant, actor, "Closing the leave year", RoleHR); err != nil {
		return nil, err
	}
	p := o.hrPolicy(ctx, tenant)
	_, end, label := leaveYear(p, yearEnd)
	if end != yearEnd {
		return nil, invalid("give the last day of a leave year (%s)", end)
	}
	if yearEnd > o.today() {
		return nil, invalid("the leave year has not ended")
	}
	next := leaveYearLabel(p, shiftDate(end, 0, 0, 1))
	if _, _, err := docs.Get[hrTickDoc](ctx, o.Docs, tenant, cHRTick, "leaveclose-"+label); err == nil {
		return nil, fmt.Errorf("%w: the leave year %s is already closed", ErrState, label)
	}
	set, _ := o.Settings(ctx, tenant)
	es, _ := o.employees(ctx, tenant)
	reqs, _ := docs.List[LeaveRequest](ctx, o.Docs, tenant, cLeaveReq, "")
	out := []YearCloseRow{}
	put := func(e Employee, typ, year, kind string, d float64, note string) error {
		n, err := o.next(ctx, tenant, "hr-leaveledger", actor)
		if err != nil {
			return err
		}
		x := LeaveEntry{ID: fmt.Sprintf("%s|%06d", e.Code, n), Employee: e.Code, Type: typ, Year: year, Kind: kind, Days: d, Note: note, By: actor, At: o.now().Format(time.RFC3339)}
		return docs.Upsert(ctx, o.Docs, tenant, cLeaveLedger, x.ID, x, actor)
	}
	for _, e := range es {
		if !e.inService(end) && e.Status != EmpLeaving {
			continue
		}
		led, _ := docs.List[LeaveEntry](ctx, o.Docs, tenant, cLeaveLedger, e.Code+"|")
		for _, lt := range p.LeaveTypes {
			if lt.Negative || (lt.Gender != "" && lt.Gender != e.Gender) {
				continue
			}
			b := o.leaveBalance(ctx, tenant, p, set, e, lt, end, reqs, led)
			if b.Balance <= 0 {
				continue
			}
			row := YearCloseRow{Employee: e.Code, Type: lt.Code, Closing: b.Balance}
			row.Carried = math.Min(b.Balance, days(lt.CarryMax))
			if lt.Paid {
				row.Encashed = math.Min(b.Balance-row.Carried, days(lt.EncashMax))
			}
			row.Lapsed = b.Balance - row.Carried - row.Encashed
			if row.Carried > 0 {
				if err := put(e, lt.Code, next, "carry", row.Carried, "carried from "+label); err != nil {
					return out, err
				}
			}
			if row.Encashed > 0 {
				if err := put(e, lt.Code, label, "encash", row.Encashed, "encashed at year end"); err != nil {
					return out, err
				}
			}
			if row.Lapsed > 0 {
				if err := put(e, lt.Code, label, "lapse", row.Lapsed, "lapsed at year end"); err != nil {
					return out, err
				}
			}
			out = append(out, row)
		}
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cHRTick, "leaveclose-"+label, hrTickDoc{Date: o.today()}, actor)
	o.log(ctx, tenant, actor, "hr.leave.year_closed", label, map[string]any{"rows": len(out)})
	return out, nil
}

// LeaveView is a person's leave (self-service) or a team's.
type LeaveView struct {
	Balances []Balance      `json:"balances"`
	Requests []LeaveRequest `json:"requests"`
	Types    []LeaveType    `json:"types"`
	Year     string         `json:"year"`
}

// MyLeave is the signed-in person's balances and requests.
func (o *Ops) MyLeave(ctx context.Context, tenant, actor string) (LeaveView, error) {
	if err := o.needModule(ctx, tenant, ModHRMS); err != nil {
		return LeaveView{}, err
	}
	e, ok := o.employeeOf(ctx, tenant, actor)
	if !ok {
		return LeaveView{}, fmt.Errorf("%w: your login is not linked to an employee", ErrForbidden)
	}
	p := o.hrPolicy(ctx, tenant)
	v := LeaveView{Balances: o.balances(ctx, tenant, e, o.today()), Requests: []LeaveRequest{}, Types: p.LeaveTypes, Year: leaveYearLabel(p, o.today())}
	rs, _ := docs.List[LeaveRequest](ctx, o.Docs, tenant, cLeaveReq, "")
	for i := len(rs) - 1; i >= 0; i-- {
		if rs[i].Employee == e.Code {
			v.Requests = append(v.Requests, rs[i])
		}
	}
	return v, nil
}

// EmployeeBalances is a person's balances (themselves, their manager, HR).
func (o *Ops) EmployeeBalances(ctx context.Context, tenant, actor, code, asOf string) ([]Balance, error) {
	if err := o.needModule(ctx, tenant, ModHRMS); err != nil {
		return nil, err
	}
	sc, err := o.scopeFor(ctx, tenant, actor)
	if err != nil {
		return nil, err
	}
	code = strings.ToUpper(code)
	if !(sc.sees(code) && (sc.all || sc.team[code] || code == sc.self)) || (sc.role == RoleCompliance && code != sc.self) {
		return nil, ErrNotFound
	}
	if asOf == "" {
		asOf = o.today()
	}
	e, err := o.employee(ctx, tenant, code)
	if err != nil {
		return nil, err
	}
	return o.balances(ctx, tenant, e, asOf), nil
}

// TeamCalendar is the team's leave and the holidays in a month.
type TeamCalendar struct {
	Month    string         `json:"month"`
	People   []OrgNode      `json:"people"` // flat: code, name, designation
	Leave    []LeaveRequest `json:"leave"`
	Pending  []LeaveRequest `json:"pending"` // waiting for this manager
	WFH      []WFHRequest   `json:"wfh_pending"`
	Holidays []HRHoliday    `json:"holidays"`
}

// Team is the actor's team calendar (managers; HR sees everyone).
func (o *Ops) Team(ctx context.Context, tenant, actor, month string) (TeamCalendar, error) {
	if err := o.needModule(ctx, tenant, ModHRMS); err != nil {
		return TeamCalendar{}, err
	}
	sc, err := o.scopeFor(ctx, tenant, actor)
	if err != nil {
		return TeamCalendar{}, err
	}
	if month == "" {
		month = o.now().Format("2006-01")
	}
	if _, err := time.Parse("2006-01", month); err != nil {
		return TeamCalendar{}, invalid("month is YYYY-MM")
	}
	hrAll := sc.role == RoleHR || sc.role == RoleAdmin
	in := func(c string) bool { return sc.team[c] || hrAll }
	v := TeamCalendar{Month: month, People: []OrgNode{}, Leave: []LeaveRequest{}, Pending: []LeaveRequest{}, WFH: []WFHRequest{}, Holidays: []HRHoliday{}}
	es, _ := o.employees(ctx, tenant)
	for _, e := range es {
		if in(e.Code) && e.Status != EmpLeft {
			v.People = append(v.People, OrgNode{Code: e.Code, Name: e.Name, Designation: e.Designation, Department: e.Department, Branch: e.Branch, Reports: []OrgNode{}})
		}
	}
	first, last := month+"-01", monthDays(month)[len(monthDays(month))-1]
	rs, _ := docs.List[LeaveRequest](ctx, o.Docs, tenant, cLeaveReq, "")
	for _, r := range rs {
		if !in(r.Employee) {
			continue
		}
		if r.Status == "Pending" {
			v.Pending = append(v.Pending, r)
		}
		if (r.Status == "Approved" || r.Status == "Pending") && r.From <= last && r.To >= first {
			v.Leave = append(v.Leave, r)
		}
	}
	ws, _ := docs.List[WFHRequest](ctx, o.Docs, tenant, cWFH, "")
	for _, w := range ws {
		if in(w.Employee) && w.Status == "Pending" {
			v.WFH = append(v.WFH, w)
		}
	}
	p := o.hrPolicy(ctx, tenant)
	for _, h := range p.Holidays {
		if h.Date >= first && h.Date <= last {
			v.Holidays = append(v.Holidays, h)
		}
	}
	if p.ExchangeHols {
		set, _ := o.Settings(ctx, tenant)
		for _, d := range monthDays(month) {
			if ok, n := set.IsHoliday(d); ok {
				v.Holidays = append(v.Holidays, HRHoliday{Date: d, Name: firstOf(n, "Exchange holiday")})
			}
		}
	}
	sort.Slice(v.Holidays, func(i, j int) bool { return v.Holidays[i].Date < v.Holidays[j].Date })
	return v, nil
}

// OnLeaveRow is a person away on a day and who covers.
type OnLeaveRow struct {
	Employee   string `json:"employee"`
	Name       string `json:"name"`
	User       string `json:"user,omitempty"`
	Type       string `json:"type"`
	Half       bool   `json:"half"`
	Until      string `json:"until"`
	Backup     string `json:"backup,omitempty"`
	BackupName string `json:"backup_name,omitempty"`
	BackupUser string `json:"backup_user,omitempty"`
}

// OnLeave lists who is on approved leave on a day, with their backup, for
// reminders and hand-offs (a person on leave has a backup). Any staff
// login with the module on may read it: it shows no reason or balance.
func (o *Ops) OnLeave(ctx context.Context, tenant, day string) ([]OnLeaveRow, error) {
	if err := o.needModule(ctx, tenant, ModHRMS); err != nil {
		return nil, err
	}
	if day == "" {
		day = o.today()
	}
	rs, err := docs.List[LeaveRequest](ctx, o.Docs, tenant, cLeaveReq, "")
	if err != nil {
		return nil, err
	}
	es, _ := o.employees(ctx, tenant)
	out := []OnLeaveRow{}
	for _, r := range rs {
		if r.Status != "Approved" || r.From > day || r.To < day {
			continue
		}
		e, _ := find(es, r.Employee)
		row := OnLeaveRow{Employee: r.Employee, Name: r.Name, User: e.User, Type: r.Type, Half: r.Half, Until: r.To, Backup: r.Backup}
		if b, ok := find(es, r.Backup); ok {
			row.BackupName, row.BackupUser = b.Name, b.User
		}
		out = append(out, row)
	}
	return out, nil
}

// BackupFor returns the login covering a user who is on leave on day ("",
// false when the user is not on leave or has no backup with a login).
// Reminders and hand-offs elsewhere call it.
func (o *Ops) BackupFor(ctx context.Context, tenant, user, day string) (string, bool) {
	rows, err := o.OnLeave(ctx, tenant, day)
	if err != nil {
		return "", false
	}
	for _, r := range rows {
		if r.User == user && r.BackupUser != "" {
			return r.BackupUser, true
		}
	}
	return "", false
}
