package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// ---- daily interest: posted every day, so it earns interest itself ----
//
// With Settings → Interest posting "daily", each day's interest on a
// client's overdue debit (counted from the pay-in date, as always) is
// posted to the ledger as its own entry, with GST. The next day's debit
// includes it, so from the second day the client pays interest on the
// principal and on the interest and GST already charged. Days are calendar
// days: a weekend is charged on Friday's balance when the next run comes.
// The tax invoice is one per client per month, issued at month end.
//
// Monthly posting (the default) charges simple interest once a month.

const (
	InterestMonthly = "monthly"
	InterestDaily   = "daily"
	cIntInvoice     = "intinvoice" // month|ucc: daily interest already invoiced
)

func (s Settings) dailyInterest() bool {
	return s.InterestPosting == InterestDaily && validDate(s.InterestDailyFrom)
}

// interestCovered lists, per client, the periods interest has been posted
// for (from/to, inclusive), ignoring reversed entries.
func (o *Ops) interestCovered(ctx context.Context, tenant string) (map[string][][2]string, error) {
	posted, err := o.postedInterest(ctx, tenant)
	if err != nil {
		return nil, err
	}
	out := map[string][][2]string{}
	for u, ps := range posted {
		for per := range ps {
			f, t, _ := strings.Cut(per, "/")
			out[u] = append(out[u], [2]string{f, t})
		}
	}
	return out, nil
}

func covered(ps [][2]string, d string) bool {
	for _, p := range ps {
		if p[0] <= d && d <= p[1] {
			return true
		}
	}
	return false
}

// DailyInterestRun is what one daily posting did.
type DailyInterestRun struct {
	From     string       `json:"from"`
	To       string       `json:"to"`
	Entries  int          `json:"entries"`
	Clients  int          `json:"clients"`
	Interest money.Amount `json:"interest"`
	GST      money.Amount `json:"gst"`
}

// PostDailyInterest posts each day's interest from the day after the last
// one charged (or the date daily posting began) up to `upto`, one entry per
// client per day, each on that day's overdue debit including the interest
// already posted. Running it again posts nothing new.
func (o *Ops) PostDailyInterest(ctx context.Context, tenant, actor, upto string) (DailyInterestRun, error) {
	if actor != SystemActor {
		if err := o.require(ctx, tenant, actor, "Posting interest", RoleChecker, RoleRisk); err != nil {
			return DailyInterestRun{}, err
		}
	}
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return DailyInterestRun{}, err
	}
	if !set.dailyInterest() {
		return DailyInterestRun{}, invalid("interest is posted monthly: choose daily posting and the date it starts in Settings")
	}
	if !validDate(upto) {
		return DailyInterestRun{}, invalid("give the date (YYYY-MM-DD)")
	}
	run := DailyInterestRun{From: set.InterestDailyFrom, To: upto}
	if upto < set.InterestDailyFrom {
		return run, nil
	}
	rate, gstPct, grace := set.interestRate(), set.gstRate(), int(set.InterestGraceDays)
	cs, err := o.ClientMap(ctx, tenant)
	if err != nil {
		return run, err
	}
	moves, err := o.clientMoves(ctx, tenant, upto, nil)
	if err != nil {
		return run, err
	}
	cov, err := o.interestCovered(ctx, tenant)
	if err != nil {
		return run, err
	}
	legacy := o.legacyOpen(ctx, tenant)
	look := addDays(set.InterestDailyFrom, -grace)
	charged := map[string]bool{}
	for _, u := range sortedKeys(moves) {
		c, ok := cs[u]
		if !ok || legacy[u] || hasAnyTag(c.Tags, set.InterestExemptTags) {
			continue
		}
		ms := moves[u]
		// Balance at the end of day d, counting interest posted for earlier
		// days but not d's own (it is worked out from this balance).
		balance := func(d string) money.Amount {
			var b money.Amount
			for _, m := range ms {
				if m.Date > d || (m.Kind == "INTEREST" && m.Date == d) {
					continue
				}
				b += m.Amount
			}
			return b
		}
		streak := 0
		for d := look; d <= upto; d = addDays(d, 1) {
			b := balance(d)
			if b < 0 {
				streak++
			} else {
				streak = 0
			}
			if d < set.InterestDailyFrom || b >= 0 || streak <= grace || covered(cov[u], d) {
				continue
			}
			if err := o.checkLock(ctx, tenant, d); err != nil {
				return run, err
			}
			intr := divInt((-b).Dec().Mul(rate), 100*365).Round(money.UnitPaisa)
			if intr <= 0 {
				continue
			}
			gst := intr.Dec().Pct(gstPct).Round(money.UnitPaisa)
			desc := fmt.Sprintf("Interest on debit balance for %s @ %s%% p.a. (debit ₹%s)", fmtShort(d), rate, (-b).String())
			_, created, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: tenant, Key: "INT:" + d + ":" + d + ":" + u, ValueDate: d,
				Narration: desc, Source: ledger.SourceRef{Kind: "INTEREST", ID: d + "/" + d + "/" + u}, CreatedBy: actor,
				Postings: []ledger.Posting{{Account: ledger.ClientAccount(u), Debit: intr + gst, Memo: "Interest and GST"},
					{Account: IncomeInterest, Credit: intr, Memo: "Interest"},
					{Account: ledger.PayableGST, Credit: gst, Memo: "GST on interest"}}})
			if err != nil {
				return run, err
			}
			// The charge is part of the next day's balance.
			ms = append(ms, clientMove{Date: d, Amount: -(intr + gst), Kind: "INTEREST"})
			sort.SliceStable(ms, func(i, j int) bool { return ms[i].Date < ms[j].Date })
			if created {
				run.Entries++
				run.Interest += intr
				run.GST += gst
				charged[u] = true
			}
		}
	}
	run.Clients = len(charged)
	if run.Entries > 0 {
		o.log(ctx, tenant, actor, "interest.daily", upto, map[string]any{"entries": run.Entries, "clients": run.Clients, "interest": run.Interest.String()})
	}
	return run, nil
}

// dailyInterestEntries returns, per client, the daily interest entries of
// from..to (not reversed): interest, GST and the number of days.
type dailySum struct {
	Days          int
	Interest, GST money.Amount
	First         string
	MaxDebit      money.Amount
}

func (o *Ops) dailyInterestSums(ctx context.Context, tenant, from, to string) (map[string]*dailySum, error) {
	es, err := o.App.Ledger.Store.List(ctx, tenant, ledger.Filter{From: from, To: to})
	if err != nil {
		return nil, err
	}
	all, err := o.App.Ledger.Store.List(ctx, tenant, ledger.Filter{})
	if err != nil {
		return nil, err
	}
	rev := map[string]bool{}
	for _, e := range all {
		if e.ReversalOf != "" {
			rev[e.ReversalOf] = true
		}
	}
	out := map[string]*dailySum{}
	for _, e := range es {
		if e.Source.Kind != "INTEREST" || rev[e.ID] {
			continue
		}
		p := strings.Split(e.Source.ID, "/")
		if len(p) != 3 || p[0] != p[1] || p[0] < from || p[0] > to {
			continue
		}
		s := out[p[2]]
		if s == nil {
			s = &dailySum{First: e.ID}
			out[p[2]] = s
		}
		s.Days++
		for _, x := range e.Postings {
			switch x.Account {
			case IncomeInterest:
				s.Interest += x.Credit
			case ledger.PayableGST:
				s.GST += x.Credit
			}
		}
	}
	return out, nil
}

// interestDailyView is the interest screen when interest is posted daily:
// what was posted for each client in the period.
func (o *Ops) interestDailyView(ctx context.Context, tenant string, v InterestView) (InterestView, error) {
	sums, err := o.dailyInterestSums(ctx, tenant, v.From, v.To)
	if err != nil {
		return v, err
	}
	cs, _ := o.ClientMap(ctx, tenant)
	invs, _ := docs.List[Invoice](ctx, o.Docs, tenant, cInvoice, "")
	for _, u := range sortedKeys(sums) {
		s := sums[u]
		r := InterestRow{UCC: u, Name: cs[u].Name, Tags: cs[u].Tags, DebitDays: s.Days, Interest: s.Interest, GST: s.GST, Total: s.Interest + s.GST, Status: "Posted daily", Entry: s.First}
		if r.Tags == nil {
			r.Tags = []string{}
		}
		for _, i := range invs {
			if i.UCC == u && i.Kind == "Interest" && i.Date >= v.From && i.Date <= v.To && strings.HasPrefix(i.Description, "Daily interest") {
				r.Invoice = i.No
			}
		}
		v.Posted += r.Total
		v.Rows = append(v.Rows, r)
	}
	sort.SliceStable(v.Rows, func(i, j int) bool { return v.Rows[i].Interest > v.Rows[j].Interest })
	return v, nil
}

// IssueDailyInterestInvoices issues each client one tax invoice for the
// month's daily interest. Running it again issues nothing new.
func (o *Ops) IssueDailyInterestInvoices(ctx context.Context, tenant, actor, month string) (int, error) {
	if len(month) != 7 || !validDate(month+"-01") {
		return 0, invalid("give the month (YYYY-MM)")
	}
	from, to := month+"-01", monthEnd(month)
	sums, err := o.dailyInterestSums(ctx, tenant, from, to)
	if err != nil {
		return 0, err
	}
	set, _ := o.Settings(ctx, tenant)
	cs, _ := o.ClientMap(ctx, tenant)
	n := 0
	for _, u := range sortedKeys(sums) {
		s := sums[u]
		key := month + "|" + u
		if _, _, err := docs.Get[map[string]string](ctx, o.Docs, tenant, cIntInvoice, key); err == nil {
			continue
		}
		desc := fmt.Sprintf("Daily interest on debit balance, %s to %s @ %s%% p.a. (%d days, posted daily)", fmtShort(from), fmtShort(to), set.interestRate(), s.Days)
		inv, err := o.issueInvoice(ctx, tenant, actor, Invoice{Date: to, UCC: u, Name: cs[u].Name, Kind: "Interest", Description: desc, SAC: "997119",
			Taxable: s.Interest, GSTPct: set.gstRate(), GST: s.GST, Total: s.Interest + s.GST, Entry: s.First})
		if err != nil {
			return n, err
		}
		if err := docs.Upsert(ctx, o.Docs, tenant, cIntInvoice, key, map[string]string{"invoice": inv.No}, actor); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func autoInterestDaily(r *runCtx) error {
	set, _ := r.o.Settings(r.ctx, r.tenant)
	if !set.dailyInterest() {
		r.note("interest is posted monthly")
		return nil
	}
	if !r.auto() {
		r.queue("interest:"+r.date, "Post daily interest up to "+fmtDay(r.date), "Each day's interest on overdue debits, on the balance including interest already posted.", "", RoleChecker, "debits", "",
			&Proposal{Kind: "interest.daily", Label: "Post", Args: map[string]string{"date": r.date}})
		return nil
	}
	run, err := r.o.PostDailyInterest(r.ctx, r.tenant, SystemActor, r.date)
	if err != nil {
		return err
	}
	r.done += run.Clients
	r.note("%d entries for %d clients, interest ₹%s", run.Entries, run.Clients, run.Interest)
	return nil
}
