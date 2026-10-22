package ops

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeNSE serves what NSE's holiday API returns.
type fakeHolidays struct {
	trading, clearing map[string][]Holiday // segment -> holidays
	fail              error
	calls             int
}

func (f *fakeHolidays) Name() string { return "NSE" }
func (f *fakeHolidays) Fetch(_ context.Context, year int) ([]Holiday, error) {
	f.calls++
	if f.fail != nil {
		return nil, f.fail
	}
	var out []Holiday
	for kind, m := range map[string]map[string][]Holiday{"trading": f.trading, "settlement": f.clearing} {
		for seg, hs := range m {
			for _, h := range hs {
				if strings.HasPrefix(h.Date, fmt.Sprint(year)) {
					h.Exchange, h.Segment, h.Kind = "NSE", seg, kind
					out = append(out, h)
				}
			}
		}
	}
	return out, nil
}

func TestHolidayCalendar(t *testing.T) {
	o, ctx := setup(t)
	now := time.Date(2026, 10, 16, 9, 0, 0, 0, ist)
	o.Now = func() time.Time { return now }
	src := &fakeHolidays{
		trading: map[string][]Holiday{
			"CM": {{Date: "2026-10-20", Name: "Diwali Laxmi Pujan"}, {Date: "2026-11-24", Name: "Guru Nanak Jayanti"}},
			"FO": {{Date: "2026-10-20", Name: "Diwali Laxmi Pujan"}, {Date: "2026-11-24", Name: "Guru Nanak Jayanti"}},
		},
		clearing: map[string][]Holiday{
			"CM": {{Date: "2026-10-20", Name: "Diwali Laxmi Pujan"}, {Date: "2026-11-24", Name: "Guru Nanak Jayanti"}, {Date: "2026-10-19", Name: "Bank holiday"}},
		},
	}
	o.HolidaySources = []HolidaySource{src}

	set, _ := o.Settings(ctx, T)
	if !set.IsBusinessDay("2026-10-20") || set.NextSettlementDay("2026-10-16") != "2026-10-19" {
		t.Fatal("calendar applied before anything was approved")
	}

	// The check raises a list for approval; nothing changes until approved.
	chk, err := o.CheckHolidays(ctx, T, SystemActor, 2026)
	if err != nil || chk.Pending == "" {
		t.Fatalf("check %+v %v", chk, err)
	}
	v, _ := o.Holidays(ctx, T)
	p := v.Years[0].Pending
	if p == nil || len(p.Holidays) != 7+7 { // NSE 7, and BSE copies of the same 7
		t.Fatalf("pending %+v", p)
	}
	if set, _ := o.Settings(ctx, T); !set.IsBusinessDay("2026-10-20") {
		t.Fatal("pending list already applied")
	}
	if _, err := o.DecideHolidays(ctx, T, "priya.nair", p.ID, true, ""); err == nil {
		t.Fatal("an Ops maker approved the holidays")
	}
	if _, err := o.DecideHolidays(ctx, T, "farhan.khan", p.ID, false, ""); err == nil {
		t.Fatal("rejected without a reason")
	}
	if _, err := o.DecideHolidays(ctx, T, "farhan.khan", p.ID, true, ""); err != nil {
		t.Fatal(err)
	}

	// Approved: Diwali is no business day; pay-in also skips the bank holiday.
	set, _ = o.Settings(ctx, T)
	if set.IsBusinessDay("2026-10-20") || !set.IsBusinessDay("2026-10-19") {
		t.Fatal("trading holidays not applied")
	}
	if got := set.NextSettlementDay("2026-10-16"); got != "2026-10-21" {
		t.Fatalf("pay-in for 16-Oct is %s, want 21-Oct (19th settlement holiday, 20th trading holiday)", got)
	}
	if got := set.NextBusinessDay("2026-10-16"); got != "2026-10-19" {
		t.Fatalf("next business day %s", got)
	}
	if got := set.PrevBusinessDay("2026-10-21"); got != "2026-10-19" {
		t.Fatalf("previous business day %s", got)
	}
	if st, err := o.Obligations(ctx, T, D); err != nil || st.PayIn != "2026-10-21" {
		t.Fatalf("settlement pay-in %s %v", st.PayIn, err)
	}
	// MCX is a member exchange here but has no list: the screen says so.
	v, _ = o.Holidays(ctx, T)
	if w := strings.Join(v.Years[0].Warnings, " "); !strings.Contains(w, "MCX trading") {
		t.Fatalf("warnings %q", w)
	}

	// Same lists again: nothing to approve.
	if chk, _ := o.CheckHolidays(ctx, T, SystemActor, 2026); chk.Pending != "" || !strings.HasPrefix(chk.Result, "Up to date") {
		t.Fatalf("recheck %+v", chk)
	}

	// The exchange adds a holiday mid-year (an election): a new list with
	// just that change waits for approval.
	src.trading["CM"] = append(src.trading["CM"], Holiday{Date: "2026-11-05", Name: "Election"})
	chk, _ = o.CheckHolidays(ctx, T, SystemActor, 2026)
	v, _ = o.Holidays(ctx, T)
	p = v.Years[0].Pending
	if p == nil || len(p.Added) != 2 || p.Added[0].Date != "2026-11-05" || p.Added[1].Date != "2026-11-05" || len(p.Removed) != 0 {
		t.Fatalf("change %+v %+v", chk, p)
	}

	// MCX circular uploaded by the maker (the exchange's own table layout);
	// it builds on the pending list. The maker cannot approve it.
	csv := "Sr. No.,Date,Day,Description\n1,\"October 20, 2026\",Tuesday,Diwali Laxmi Pujan\n2,25-Dec-2026,Friday,Christmas\n"
	up, err := o.UploadHolidays(ctx, T, "priya.nair", 2026, "MCX", "trading", []string{"COM"}, []byte(csv))
	if err != nil {
		t.Fatal(err)
	}
	if len(up.Added) != 4 { // the election (NSE and BSE) and 2 MCX holidays
		t.Fatalf("upload added %+v", up.Added)
	}
	v, _ = o.Holidays(ctx, T)
	if len(v.Years[0].History) < 3 || v.Years[0].History[1].Status != "Superseded" {
		t.Fatalf("history %+v", v.Years[0].History)
	}
	if _, err := o.DecideHolidays(ctx, T, "vikram.shah", up.ID, true, "matches MCX circular"); err != nil {
		t.Fatal(err)
	}
	set, _ = o.Settings(ctx, T)
	if set.IsBusinessDay("2026-11-05") {
		t.Fatal("election holiday not applied")
	}
	// MCX's own holidays don't change equity business days.
	if set.IsBusinessDay("2026-12-24") == false {
		t.Fatal("an MCX-only list changed equity days")
	}

	// Bad uploads are refused with a reason.
	for name, c := range map[string]string{
		"no date column": "Holiday\nDiwali\n",
		"other year":     "Date,Description\n2027-01-26,Republic Day\n",
		"bad date":       "Date,Description\n31/31/2026,Nonsense\n",
		"empty":          "Date,Description\n",
	} {
		if _, err := o.UploadHolidays(ctx, T, "priya.nair", 2026, "MCX", "settlement", []string{"COM"}, []byte(c)); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}

	// The exchange is unreachable: the check says so and changes nothing.
	src.fail = errors.New("NSE returned HTTP 403")
	chk, _ = o.CheckHolidays(ctx, T, SystemActor, 2026)
	if !strings.Contains(chk.Result, "403") || chk.Pending != "" {
		t.Fatalf("failed check %+v", chk)
	}
	src.fail = nil
}

func TestHolidayTick(t *testing.T) {
	o, ctx := setup(t)
	src := &fakeHolidays{trading: map[string][]Holiday{"CM": {{Date: "2027-01-26", Name: "Republic Day"}}}, clearing: map[string][]Holiday{"CM": {{Date: "2027-01-26", Name: "Republic Day"}}}}
	o.HolidaySources = []HolidaySource{src}
	// 7 December 2026, a Monday, 07:00: too early.
	at := time.Date(2026, 12, 7, 7, 0, 0, 0, ist)
	if err := o.holidayTick(ctx, T, at); err != nil || src.calls != 0 {
		t.Fatalf("ran before 08:00: %d %v", src.calls, err)
	}
	// 08:05: checks this year (none approved) and next year (December).
	at = at.Add(65 * time.Minute)
	if err := o.holidayTick(ctx, T, at); err != nil || src.calls != 2 {
		t.Fatalf("calls %d %v", src.calls, err)
	}
	// Once a day only.
	_ = o.holidayTick(ctx, T, at.Add(time.Hour))
	if src.calls != 2 {
		t.Fatalf("ran twice in a day: %d", src.calls)
	}
	o.Now = func() time.Time { return at }
	v, _ := o.Holidays(ctx, T)
	if v.Years[1].Year != 2027 || v.Years[1].Pending == nil {
		t.Fatalf("2027 not raised: %+v", v.Years[1])
	}
	// Tuesday: next year is pending, this year has nothing published, so
	// only this year is checked again.
	_ = o.holidayTick(ctx, T, at.AddDate(0, 0, 1))
	if src.calls != 3 {
		t.Fatalf("Tuesday calls %d", src.calls)
	}
	// It runs from the scheduler too, in every edition.
	o.SetEdition(T, EditionManual)
	if _, err := o.Tick(ctx, T, at.AddDate(0, 0, 7)); err != nil || src.calls < 5 {
		t.Fatalf("manual edition tick: calls %d %v", src.calls, err)
	}
}

func TestNSEHolidayAPI(t *testing.T) {
	trading := `{"CM":[{"tradingDate":"26-Jan-2026","weekDay":"Monday","description":"Republic Day","Sr_no":1},{"tradingDate":"03-Mar-2026","weekDay":"Tuesday","description":"Holi","Sr_no":2}],
	 "FO":[{"tradingDate":"26-Jan-2026","weekDay":"Monday","description":"Republic Day","Sr_no":1}],
	 "COM":[{"tradingDate":"26-Jan-2026","weekDay":"Monday","description":"Republic Day","morning_session":"Closed","evening_session":"Open","Sr_no":1}],
	 "MF":[{"tradingDate":"26-Jan-2026","description":"Republic Day"}],
	 "CM2025":[{"tradingDate":"25-Dec-2025","description":"Christmas"}]}`
	clearing := `{"CM":[{"tradingDate":"26-Jan-2026","description":"Republic Day"},{"tradingDate":"01-Apr-2026","description":"Annual bank closing"},{"tradingDate":"25-Dec-2025","description":"Christmas"}]}`
	cookie := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/":
			http.SetCookie(w, &http.Cookie{Name: "nsit", Value: "x"})
		case r.URL.Path == "/api/holiday-master":
			if _, err := r.Cookie("nsit"); err == nil {
				cookie = true
			}
			if r.Header.Get("User-Agent") == "" || !strings.Contains(r.Header.Get("Accept"), "json") {
				w.WriteHeader(403)
				return
			}
			if r.URL.Query().Get("type") == "trading" {
				fmt.Fprint(w, trading)
			} else {
				fmt.Fprint(w, clearing)
			}
		}
	}))
	defer srv.Close()
	hs, err := (&NSEHolidays{BaseURL: srv.URL}).Fetch(context.Background(), 2026)
	if err != nil {
		t.Fatal(err)
	}
	if !cookie {
		t.Fatal("did not carry NSE's cookies")
	}
	n := map[string]int{}
	for _, h := range hs {
		n[h.Kind+" "+h.Segment]++
		if h.Segment == "COM" && h.Note != "evening session Open" {
			t.Fatalf("COM note %q", h.Note)
		}
		if h.Date < "2026" {
			t.Fatalf("other year included: %+v", h)
		}
	}
	if n["trading CM"] != 2 || n["trading FO"] != 1 || n["trading COM"] != 1 || n["settlement CM"] != 2 || len(hs) != 6 {
		t.Fatalf("parsed %v", n)
	}
	if _, err := parseNSEHolidays([]byte(`<html>Access denied</html>`), "trading", 2026); err == nil {
		t.Fatal("HTML accepted")
	}
	if _, err := parseNSEHolidays([]byte(`{"XX":[]}`), "trading", 2026); err == nil {
		t.Fatal("no segments accepted")
	}
}
