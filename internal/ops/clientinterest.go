package ops

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/money"
)

// ---- a client's own interest terms ----
//
// Interest on debit balances is charged at the firm's rate (Settings) after
// the firm's grace days. A client can have their own terms from a date: their
// own rate, their own grace days, or no interest at all. Terms are dated, so
// each day is charged at the terms in force that day (a change mid-month
// splits the month), and recalculating a past period uses the terms of the
// time. Legacy debts and the exempt client groups in Settings stay out of
// interest whatever the client's terms say.

const cClientInterest = "clientinterest"

// InterestTerms are a client's own interest terms from a date. A version
// with no rate, no grace days and Free unset ends the client's own terms:
// the firm's apply from that date.
type InterestTerms struct {
	From      string `json:"from"`
	RatePct   string `json:"rate_pct,omitempty"`   // % a year; "" is the firm's rate
	GraceDays *int64 `json:"grace_days,omitempty"` // nil is the firm's grace days
	Free      bool   `json:"free,omitempty"`       // no interest
	Note      string `json:"note,omitempty"`
	By        string `json:"by,omitempty"`
	At        string `json:"at,omitempty"`
}

func (t InterestTerms) empty() bool { return t.RatePct == "" && t.GraceDays == nil && !t.Free }

// Describe is a short label such as "15% p.a., 3 grace days".
func (t InterestTerms) Describe() string {
	if t.Free {
		return "No interest"
	}
	var s []string
	if t.RatePct != "" {
		s = append(s, t.RatePct+"% p.a.")
	}
	if t.GraceDays != nil {
		s = append(s, fmt.Sprintf("%d grace days", *t.GraceDays))
	}
	if len(s) == 0 {
		return "Firm's terms"
	}
	return strings.Join(s, ", ")
}

// ClientInterest is the history of a client's own interest terms.
type ClientInterest struct {
	UCC      string          `json:"ucc"`
	Versions []InterestTerms `json:"versions"`
}

// At returns the terms in force on date (ok false: the firm's terms).
func (c ClientInterest) At(date string) (InterestTerms, bool) {
	var best InterestTerms
	ok := false
	for _, v := range c.Versions {
		if v.From <= date && (!ok || v.From > best.From) {
			best, ok = v, true
		}
	}
	if !ok || best.empty() {
		return InterestTerms{}, false
	}
	return best, true
}

// ClientInterests lists every client with their own interest terms.
func (o *Ops) ClientInterests(ctx context.Context, tenant string) ([]ClientInterest, error) {
	cs, err := docs.List[ClientInterest](ctx, o.Docs, tenant, cClientInterest, "")
	sort.Slice(cs, func(i, j int) bool { return cs[i].UCC < cs[j].UCC })
	return cs, err
}

// ClientInterestInput sets a client's own interest terms from a date.
type ClientInterestInput struct {
	UCC string `json:"ucc"`
	InterestTerms
}

// SaveClientInterest records a client's own interest terms from a date
// (replacing terms that start on the same date). Blank terms end them.
func (o *Ops) SaveClientInterest(ctx context.Context, tenant, actor string, in ClientInterestInput) (ClientInterest, error) {
	if err := o.require(ctx, tenant, actor, "Changing client interest rates", RoleChecker); err != nil {
		return ClientInterest{}, err
	}
	c, _, err := o.saveClientInterest(ctx, tenant, actor, in)
	return c, err
}

func cleanTerms(t InterestTerms) (InterestTerms, error) {
	t.RatePct, t.Note = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(t.RatePct), "%")), strings.TrimSpace(t.Note)
	if !validDate(t.From) {
		return t, invalid("effective from must be a date")
	}
	if t.RatePct != "" {
		d, err := money.ParseDec(t.RatePct)
		if err != nil || d.Sign() < 0 || d.Cmp(money.DecInt(100)) > 0 {
			return t, invalid("interest rate %q: give a yearly percentage between 0 and 100", t.RatePct)
		}
		t.RatePct = d.String()
	}
	if t.GraceDays != nil && (*t.GraceDays < 0 || *t.GraceDays > 365) {
		return t, invalid("grace days must be between 0 and 365")
	}
	if t.Free {
		t.RatePct, t.GraceDays = "", nil
	}
	return t, nil
}

func (o *Ops) saveClientInterest(ctx context.Context, tenant, actor string, in ClientInterestInput) (ClientInterest, bool, error) {
	in.UCC = strings.ToUpper(strings.TrimSpace(in.UCC))
	if _, err := o.Client(ctx, tenant, in.UCC); err != nil {
		return ClientInterest{}, false, err
	}
	t, err := cleanTerms(in.InterestTerms)
	if err != nil {
		return ClientInterest{}, false, err
	}
	t.By, t.At = actor, o.now().Format("2006-01-02 15:04")
	changed := true
	c, err := upd(ctx, o.Docs, tenant, cClientInterest, in.UCC, actor, &ClientInterest{UCC: in.UCC}, func(c *ClientInterest) error {
		kept := c.Versions[:0]
		for _, x := range c.Versions {
			if x.From == t.From {
				if x.RatePct == t.RatePct && sameJSON(x.GraceDays, t.GraceDays) && x.Free == t.Free && x.Note == t.Note {
					changed = false
					return nil
				}
				continue
			}
			kept = append(kept, x)
		}
		c.Versions = append(kept, t)
		sort.Slice(c.Versions, func(i, j int) bool { return c.Versions[i].From < c.Versions[j].From })
		return nil
	})
	if err != nil || !changed {
		return c, changed, err
	}
	o.log(ctx, tenant, actor, "interest.client_terms", in.UCC, map[string]any{"from": t.From, "terms": t.Describe()})
	return c, true, nil
}

// ClientInterestView is a client's interest terms on a date, for the client
// page.
type ClientInterestView struct {
	UCC       string          `json:"ucc"`
	Date      string          `json:"date"`
	RatePct   string          `json:"rate_pct"` // in force, "0" when free
	GraceDays int64           `json:"grace_days"`
	Free      bool            `json:"free"`
	Own       bool            `json:"own"` // the client's own terms, not the firm's
	Exempt    string          `json:"exempt,omitempty"`
	Describe  string          `json:"describe"`
	Since     string          `json:"since,omitempty"`
	History   []InterestTerms `json:"history"`
}

// ClientInterestOn is the interest terms a client pays on a date.
func (o *Ops) ClientInterestOn(ctx context.Context, tenant, ucc, date string) (ClientInterestView, error) {
	ucc = strings.ToUpper(strings.TrimSpace(ucc))
	c, err := o.Client(ctx, tenant, ucc)
	if err != nil {
		return ClientInterestView{}, err
	}
	ib, err := o.interestBook(ctx, tenant)
	if err != nil {
		return ClientInterestView{}, err
	}
	d := ib.on(ucc, date)
	v := ClientInterestView{UCC: ucc, Date: date, RatePct: d.rate.String(), GraceDays: d.grace, Free: d.free, Own: d.own, History: []InterestTerms{}}
	if d.free {
		v.RatePct = "0"
	}
	if h, ok := ib.clients[ucc]; ok {
		v.History = append(v.History, h.Versions...)
		if t, ok := h.At(date); ok {
			v.Since = t.From
		}
	}
	set, _ := o.Settings(ctx, tenant)
	switch {
	case o.legacyOpen(ctx, tenant)[ucc]:
		v.Exempt = "Legacy debt: no interest"
	case hasAnyTag(c.Tags, set.InterestExemptTags):
		v.Exempt = "In an interest-exempt group"
	}
	v.Describe = d.describe()
	return v, nil
}

// ---- terms by day ----

// dayTerms are the interest terms that apply to a client on a day.
type dayTerms struct {
	rate  money.Dec
	grace int64
	free  bool
	own   bool
}

func (d dayTerms) describe() string {
	if d.free {
		return "No interest (client's terms)"
	}
	s := d.rate.String() + "% p.a."
	if d.grace > 0 {
		s += fmt.Sprintf(" after %d grace days", d.grace)
	}
	if d.own {
		s += " (client's own)"
	}
	return s
}

// interestBook resolves each client's terms day by day.
type interestBook struct {
	rate    money.Dec
	grace   int64
	clients map[string]ClientInterest
}

func (o *Ops) interestBook(ctx context.Context, tenant string) (interestBook, error) {
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return interestBook{}, err
	}
	cs, err := o.ClientInterests(ctx, tenant)
	if err != nil {
		return interestBook{}, err
	}
	ib := interestBook{rate: set.interestRate(), grace: set.InterestGraceDays, clients: map[string]ClientInterest{}}
	for _, c := range cs {
		ib.clients[c.UCC] = c
	}
	return ib, nil
}

// on is the terms in force for a client on a day.
func (ib interestBook) on(ucc, d string) dayTerms {
	out := dayTerms{rate: ib.rate, grace: ib.grace}
	c, ok := ib.clients[ucc]
	if !ok {
		return out
	}
	t, ok := c.At(d)
	if !ok {
		return out
	}
	out.own = true
	if t.Free {
		out.free, out.rate = true, money.Dec{}
		return out
	}
	if r, err := money.ParseDec(t.RatePct); err == nil && t.RatePct != "" {
		out.rate = r
	}
	if t.GraceDays != nil {
		out.grace = *t.GraceDays
	}
	return out
}

// maxGrace is the longest grace any client has, for how far back a run
// must look to know whether a debit spell is past its grace.
func (ib interestBook) maxGrace() int {
	g := ib.grace
	for _, c := range ib.clients {
		for _, v := range c.Versions {
			if v.GraceDays != nil && *v.GraceDays > g {
				g = *v.GraceDays
			}
		}
	}
	return int(g)
}

// rateSpan collects the rates charged over a period, for narrations: one
// rate reads "15", a change "24 to 14-Nov, 15 from 15-Nov".
type rateSpan struct {
	parts []struct {
		rate     money.Dec
		from, to string
	}
}

func (r *rateSpan) add(d string, rate money.Dec) {
	if n := len(r.parts); n > 0 && r.parts[n-1].rate.Cmp(rate) == 0 {
		r.parts[n-1].to = d
		return
	}
	r.parts = append(r.parts, struct {
		rate     money.Dec
		from, to string
	}{rate, d, d})
}

func (r rateSpan) changed() bool { return len(r.parts) > 1 }

// String is the rate charged: "15", or "24% to 15-Oct, 17% from 16-Oct".
func (r rateSpan) String() string {
	switch len(r.parts) {
	case 0:
		return ""
	case 1:
		return r.parts[0].rate.String()
	}
	var s []string
	for i, p := range r.parts {
		if i == 0 {
			s = append(s, p.rate.String()+"% to "+fmtShort(p.to))
		} else {
			s = append(s, p.rate.String()+"% from "+fmtShort(p.from))
		}
	}
	return strings.Join(s, ", ")
}

// atRate is "@ 24% p.a." or "@ 24% to 14-Nov, 15% from 15-Nov p.a.".
func (r rateSpan) atRate(fallback money.Dec) string {
	if len(r.parts) == 0 {
		return "@ " + fallback.String() + "% p.a."
	}
	if !r.changed() {
		return "@ " + r.String() + "% p.a."
	}
	var s []string
	for i, p := range r.parts {
		if i == 0 {
			s = append(s, p.rate.String()+"% p.a. to "+fmtShort(p.to))
		} else {
			s = append(s, p.rate.String()+"% p.a. from "+fmtShort(p.from))
		}
	}
	return "@ " + strings.Join(s, ", ")
}

// prepareClientInterest loads CLIENT-INTEREST: UCC, From; optional Rate,
// GraceDays, Free (Y) and Note.
func (o *Ops) prepareClientInterest(ctx context.Context, tenant, actor string, rows []row) (func() (int, int, error), []ingest.ParseError) {
	var errs []ingest.ParseError
	bad := func(l int, f, m string, a ...any) {
		errs = append(errs, ingest.ParseError{Line: l, Field: f, Msg: fmt.Sprintf(m, a...)})
	}
	if err := o.require(ctx, tenant, actor, "Changing client interest rates", RoleChecker); err != nil {
		bad(1, "", "%v", err)
		return nil, errs
	}
	cs, err := o.ClientMap(ctx, tenant)
	if err != nil {
		bad(1, "", "%v", err)
		return nil, errs
	}
	var todo []ClientInterestInput
	seen := map[string]int{}
	for _, r := range rows {
		u := strings.ToUpper(strings.TrimSpace(r.get("UCC")))
		if _, ok := cs[u]; !ok {
			bad(r.line, "UCC", "%s is not a client; load the client master first", u)
			continue
		}
		t := InterestTerms{From: strings.TrimSpace(r.get("From")), RatePct: strings.TrimSpace(firstOf(r.get("Rate"), r.get("RatePct"))), Note: r.get("Note")}
		if !validDate(t.From) {
			bad(r.line, "From", "must be YYYY-MM-DD, got %q", t.From)
			continue
		}
		if g := strings.TrimSpace(r.get("GraceDays")); g != "" {
			n, err := strconv.ParseInt(g, 10, 64)
			if err != nil {
				bad(r.line, "GraceDays", "must be a whole number, got %q", g)
				continue
			}
			t.GraceDays = &n
		}
		switch f := strings.ToUpper(strings.TrimSpace(r.get("Free"))); f {
		case "", "N", "NO":
		case "Y", "YES":
			t.Free = true
		default:
			bad(r.line, "Free", "use Y or leave it blank, got %q", f)
			continue
		}
		if t.Free && t.RatePct != "" {
			bad(r.line, "Rate", "a client with no interest (Free Y) cannot also have a rate")
			continue
		}
		ct, err := cleanTerms(t)
		if err != nil {
			bad(r.line, "", "%v", err)
			continue
		}
		if l, dup := seen[u+"|"+ct.From]; dup {
			bad(r.line, "From", "%s from %s is also on line %d", u, ct.From, l)
			continue
		}
		seen[u+"|"+ct.From] = r.line
		todo = append(todo, ClientInterestInput{UCC: u, InterestTerms: ct})
	}
	return func() (int, int, error) {
		n, same := 0, 0
		for _, in := range todo {
			_, changed, err := o.saveClientInterest(ctx, tenant, actor, in)
			if err != nil {
				return n, same, fmt.Errorf("%s from %s: %w", in.UCC, in.From, err)
			}
			if changed {
				n++
			} else {
				same++
			}
		}
		return n, same, nil
	}, errs
}
