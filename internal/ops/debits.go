package ops

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// ---- debit balances: ageing, continuous debit, debtors' stock, interest ----
//
// A client's balance is "overdue" once its pay-in date has passed: a bill
// (or futures MTM, option exercise, or a reversal of one) dated T is due
// for pay-in on the next day, so it counts against the client from the day
// after its value date. Every other entry counts from its value date.
//
// Interest (delayed payment charges) is simple interest on the overdue
// debit, day by day, at the firm's annual rate, after an optional grace
// period at the start of each debit spell. Clients in exempt groups are not
// charged. GST is added and a tax invoice is issued for each posting.

const (
	cInvoice = "invoice"
	// IncomeInterest is interest and delayed payment charges earned from clients.
	IncomeInterest = "INCOME:INTEREST"
	debitReason    = "Continuous debit"
)

// settlesNextDay are the entry kinds whose money is due on the next day.
var settlesNextDay = map[string]bool{"BILL": true, "MTM": true, "EXPIRY": true, "REVERSAL": true}

// AgeBuckets are the debit ageing buckets, in days.
var AgeBuckets = []struct {
	Label  string
	MaxDay int
}{{"0–7 days", 7}, {"8–30 days", 30}, {"31–90 days", 90}, {"91–180 days", 180}, {"Over 180 days", 1 << 30}}

func (s Settings) interestRate() money.Dec {
	if d, err := money.ParseDec(s.InterestPctPA); err == nil && s.InterestPctPA != "" {
		return d
	}
	return money.DecInt(18)
}

func (s Settings) gstRate() money.Dec {
	if d, err := money.ParseDec(s.GSTPct); err == nil && s.GSTPct != "" {
		return d
	}
	return money.DecInt(18)
}

func (s Settings) interestMinimum() money.Amount {
	if a, err := money.ParseAmount(s.InterestMinimum); err == nil && s.InterestMinimum != "" {
		return a
	}
	return 100 // ₹1
}

func (s Settings) debitBlockDays() int {
	if s.DebitBlockDays > 0 {
		return int(s.DebitBlockDays)
	}
	return 7
}

// finYear is the Indian financial year of a date, e.g. "2026-27".
func finYear(d string) string {
	t, err := time.Parse("2006-01-02", d)
	if err != nil {
		return ""
	}
	y := t.Year()
	if t.Month() < time.April {
		y--
	}
	return fmt.Sprintf("%d-%02d", y, (y+1)%100)
}

// clientMove is one change to a client's overdue balance.
type clientMove struct {
	Date   string // date from which it counts
	Amount money.Amount
	Kind   string
	Entry  string
}

// clientMoves returns each client's balance movements up to date, by the
// date they become overdue (credit positive). Entries of the kinds in skip
// are left out.
func (o *Ops) clientMoves(ctx context.Context, tenant, date string, skip map[string]bool) (map[string][]clientMove, error) {
	es, err := o.App.Ledger.Store.List(ctx, tenant, ledger.Filter{To: date})
	if err != nil {
		return nil, err
	}
	out := map[string][]clientMove{}
	for _, e := range es {
		if skip[e.Source.Kind] {
			continue
		}
		eff := e.ValueDate
		if settlesNextDay[e.Source.Kind] {
			eff = addDays(eff, 1)
		}
		if eff > date {
			continue
		}
		for _, p := range e.Postings {
			if u, ok := strings.CutPrefix(p.Account, "CLIENT:"); ok && p.Credit != p.Debit {
				out[u] = append(out[u], clientMove{eff, p.Credit - p.Debit, e.Source.Kind, e.ID})
			}
		}
	}
	for _, ms := range out {
		sort.SliceStable(ms, func(i, j int) bool { return ms[i].Date < ms[j].Date })
	}
	return out, nil
}

// dailyBalances turns movements into the balance at the end of each day
// from..to inclusive.
func dailyBalances(ms []clientMove, from, to string) []money.Amount {
	n := daysBetween(from, to) + 1
	if n <= 0 {
		return nil
	}
	out := make([]money.Amount, n)
	var bal money.Amount
	i := 0
	for d := 0; d < n; d++ {
		day := addDays(from, d)
		for i < len(ms) && ms[i].Date <= day {
			bal += ms[i].Amount
			i++
		}
		out[d] = bal
	}
	return out
}

// ---- interest ----

// InterestRow is one client's interest for a period.
type InterestRow struct {
	UCC       string       `json:"ucc"`
	Name      string       `json:"name"`
	Tags      []string     `json:"tags"`
	DebitDays int          `json:"debit_days"` // days charged
	MaxDebit  money.Amount `json:"max_debit"`
	AvgDebit  money.Amount `json:"avg_debit"` // over charged days
	Product   money.Amount `json:"product"`   // sum of daily debits (rupee-days)
	Interest  money.Amount `json:"interest"`
	GST       money.Amount `json:"gst"`
	Total     money.Amount `json:"total"`
	Status    string       `json:"status"` // To post, Posted, Exempt, Below minimum, Legacy debt
	Entry     string       `json:"entry,omitempty"`
	Invoice   string       `json:"invoice,omitempty"`
}

// InterestView is the interest run for a period.
type InterestView struct {
	From      string        `json:"from"`
	To        string        `json:"to"`
	RatePct   money.Dec     `json:"rate_pct"`
	GSTPct    money.Dec     `json:"gst_pct"`
	GraceDays int64         `json:"grace_days"`
	Minimum   money.Amount  `json:"minimum"`
	Exempt    []string      `json:"exempt_tags"`
	Rows      []InterestRow `json:"rows"`
	ToPost    money.Amount  `json:"to_post"`
	Posted    money.Amount  `json:"posted"`
}

// Invoice is a tax invoice for a charge outside the contract note
// (interest, DP charges, penalties). Contract notes are the tax invoices for
// brokerage.
type Invoice struct {
	No          string       `json:"no"`
	Date        string       `json:"date"`
	UCC         string       `json:"ucc"`
	Name        string       `json:"name"`
	Kind        string       `json:"kind"` // Interest, ...
	Description string       `json:"description"`
	SAC         string       `json:"sac"`
	Taxable     money.Amount `json:"taxable"`
	GSTPct      money.Dec    `json:"gst_pct"`
	GST         money.Amount `json:"gst"`
	Total       money.Amount `json:"total"`
	Entry       string       `json:"entry"`
	Status      string       `json:"status"` // Issued, Cancelled
}

func hasAnyTag(tags, set []string) bool {
	for _, t := range tags {
		for _, s := range set {
			if strings.EqualFold(t, s) {
				return true
			}
		}
	}
	return false
}

// postedInterest returns, per client, the periods already charged (entry
// ID by "from/to"), ignoring reversed entries.
func (o *Ops) postedInterest(ctx context.Context, tenant string) (map[string]map[string]string, error) {
	es, err := o.App.Ledger.Store.List(ctx, tenant, ledger.Filter{})
	if err != nil {
		return nil, err
	}
	rev := map[string]bool{}
	for _, e := range es {
		if e.ReversalOf != "" {
			rev[e.ReversalOf] = true
		}
	}
	out := map[string]map[string]string{}
	for _, e := range es {
		if e.Source.Kind != "INTEREST" || rev[e.ID] {
			continue
		}
		p := strings.Split(e.Source.ID, "/") // from/to/ucc
		if len(p) != 3 {
			continue
		}
		if out[p[2]] == nil {
			out[p[2]] = map[string]string{}
		}
		out[p[2]][p[0]+"/"+p[1]] = e.ID
	}
	return out, nil
}

// Interest computes interest on overdue debit balances for from..to.
func (o *Ops) Interest(ctx context.Context, tenant, from, to string) (InterestView, error) {
	if !ledger.ValidDate(from) || !ledger.ValidDate(to) || from > to {
		return InterestView{}, invalid("give a period: from on or before to, as YYYY-MM-DD")
	}
	if daysBetween(from, to) > 370 {
		return InterestView{}, invalid("the interest period can be at most a year")
	}
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return InterestView{}, err
	}
	v := InterestView{From: from, To: to, RatePct: set.interestRate(), GSTPct: set.gstRate(), GraceDays: set.InterestGraceDays, Minimum: set.interestMinimum(), Exempt: set.InterestExemptTags, Rows: []InterestRow{}}
	if v.Exempt == nil {
		v.Exempt = []string{}
	}
	cs, err := o.ClientMap(ctx, tenant)
	if err != nil {
		return v, err
	}
	// Interest is simple: earlier interest postings do not earn interest.
	moves, err := o.clientMoves(ctx, tenant, to, map[string]bool{"INTEREST": true})
	if err != nil {
		return v, err
	}
	posted, err := o.postedInterest(ctx, tenant)
	if err != nil {
		return v, err
	}
	legacy := o.legacyOpen(ctx, tenant)
	invs, _ := docs.List[Invoice](ctx, o.Docs, tenant, cInvoice, "")
	invByEntry := map[string]string{}
	for _, i := range invs {
		invByEntry[i.Entry] = i.No
	}
	// Look back far enough to know how long each spell has run for grace.
	look := addDays(from, -int(set.InterestGraceDays))
	for _, u := range sortedKeys(moves) {
		bals := dailyBalances(moves[u], look, to)
		lead := daysBetween(look, from)
		r := InterestRow{UCC: u, Tags: []string{}}
		if c, ok := cs[u]; ok {
			r.Name, r.Tags = c.Name, c.Tags
			if r.Tags == nil {
				r.Tags = []string{}
			}
		}
		streak := 0
		for i, b := range bals {
			if b < 0 {
				streak++
			} else {
				streak = 0
			}
			if i < lead || b >= 0 || streak <= int(set.InterestGraceDays) {
				continue
			}
			r.DebitDays++
			r.Product += -b
			if -b > r.MaxDebit {
				r.MaxDebit = -b
			}
		}
		if r.DebitDays == 0 {
			continue
		}
		r.AvgDebit = divInt(r.Product.Dec(), int64(r.DebitDays)).Round(money.UnitPaisa)
		r.Interest = divInt(r.Product.Dec().Mul(v.RatePct), 100*365).Round(money.UnitPaisa)
		r.GST = r.Interest.Dec().Pct(v.GSTPct).Round(money.UnitPaisa)
		r.Total = r.Interest + r.GST
		for per, id := range posted[u] {
			pf, pt, _ := strings.Cut(per, "/")
			if pf <= to && pt >= from {
				r.Status, r.Entry, r.Invoice = "Posted", id, invByEntry[id]
				if pf != from || pt != to {
					r.Status = "Posted for " + fmtShort(pf) + " – " + fmtShort(pt)
				}
			}
		}
		switch {
		case r.Status != "":
			if r.Entry != "" {
				v.Posted += r.Total
			}
		case legacy[u]:
			r.Status = "Legacy debt" // not charged: see legacy.go
		case hasAnyTag(r.Tags, v.Exempt):
			r.Status = "Exempt"
		case r.Interest < v.Minimum:
			r.Status = "Below minimum"
		default:
			r.Status = "To post"
			v.ToPost += r.Total
		}
		v.Rows = append(v.Rows, r)
	}
	sort.SliceStable(v.Rows, func(i, j int) bool { return v.Rows[i].Interest > v.Rows[j].Interest })
	return v, nil
}

func fmtShort(d string) string {
	t, err := time.Parse("2006-01-02", d)
	if err != nil {
		return d
	}
	return t.Format("02-Jan")
}

// PostInterest charges the period's interest to every client marked "To
// post": debit the client, credit interest income and GST, and issue a tax
// invoice. Running it again posts nothing new.
func (o *Ops) PostInterest(ctx context.Context, tenant, actor, from, to string) (InterestView, int, error) {
	if err := o.require(ctx, tenant, actor, "Posting interest", RoleChecker, RoleRisk); err != nil {
		return InterestView{}, 0, err
	}
	if err := o.checkLock(ctx, tenant, to); err != nil {
		return InterestView{}, 0, err
	}
	v, err := o.Interest(ctx, tenant, from, to)
	if err != nil {
		return v, 0, err
	}
	n := 0
	for _, r := range v.Rows {
		if r.Status != "To post" {
			continue
		}
		desc := fmt.Sprintf("Interest on debit balance %s to %s @ %s%% p.a. (%d days, average debit ₹%s)", fmtShort(from), fmtShort(to), v.RatePct, r.DebitDays, r.AvgDebit)
		e, created, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: tenant, Key: "INT:" + from + ":" + to + ":" + r.UCC, ValueDate: to,
			Narration: desc, Source: ledger.SourceRef{Kind: "INTEREST", ID: from + "/" + to + "/" + r.UCC}, CreatedBy: actor,
			Postings: []ledger.Posting{{Account: ledger.ClientAccount(r.UCC), Debit: r.Total, Memo: "Interest and GST"},
				{Account: IncomeInterest, Credit: r.Interest, Memo: "Interest"},
				{Account: ledger.PayableGST, Credit: r.GST, Memo: "GST on interest"}}})
		if err != nil {
			return v, n, err
		}
		if !created {
			continue
		}
		n++
		if _, err := o.issueInvoice(ctx, tenant, actor, Invoice{Date: to, UCC: r.UCC, Name: r.Name, Kind: "Interest", Description: desc, SAC: "997119",
			Taxable: r.Interest, GSTPct: v.GSTPct, GST: r.GST, Total: r.Total, Entry: e.ID}); err != nil {
			return v, n, err
		}
	}
	o.log(ctx, tenant, actor, "interest.posted", from+"/"+to, map[string]any{"clients": n, "amount": v.ToPost.String()})
	v, err = o.Interest(ctx, tenant, from, to)
	return v, n, err
}

// issueInvoice numbers and stores a tax invoice (series KIND/FY/NNNNNN).
func (o *Ops) issueInvoice(ctx context.Context, tenant, actor string, inv Invoice) (Invoice, error) {
	series := strings.ToUpper(inv.Kind)
	if len(series) > 3 {
		series = series[:3]
	}
	fy := finYear(inv.Date)
	n, err := o.next(ctx, tenant, "inv-"+series+"-"+fy, actor)
	if err != nil {
		return inv, err
	}
	inv.No = fmt.Sprintf("%s/%s/%06d", series, fy, n)
	inv.Status = "Issued"
	if _, err := docs.Put(ctx, o.Docs, tenant, cInvoice, strings.ReplaceAll(inv.No, "/", "-"), inv, 0, actor); err != nil {
		return inv, err
	}
	o.log(ctx, tenant, actor, "invoice.issued", inv.No, map[string]any{"ucc": inv.UCC, "total": inv.Total.String()})
	return inv, nil
}

// Invoices lists tax invoices, optionally for one client.
func (o *Ops) Invoices(ctx context.Context, tenant, ucc string) ([]Invoice, error) {
	all, err := docs.List[Invoice](ctx, o.Docs, tenant, cInvoice, "")
	if err != nil {
		return nil, err
	}
	out := []Invoice{}
	for _, i := range all {
		if ucc == "" || i.UCC == ucc {
			out = append(out, i)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].No > out[j].No })
	return out, nil
}

// ---- continuous debit, ageing and debtors' stock ----

// DebitRow is one client in debit (or blocked for debit) on a date.
type DebitRow struct {
	UCC        string         `json:"ucc"`
	Name       string         `json:"name"`
	Tags       []string       `json:"tags"`
	Debit      money.Amount   `json:"debit"`  // overdue debit, positive
	Ledger     money.Amount   `json:"ledger"` // ledger balance incl. today's bills, credit positive
	Since      string         `json:"since,omitempty"`
	Days       int            `json:"days"`
	Ageing     []money.Amount `json:"ageing"` // per AgeBuckets
	Pledged    money.Dec      `json:"pledged"`
	Unpledged  money.Dec      `json:"unpledged"`
	Collateral money.Dec      `json:"collateral"` // all holdings after haircut
	CoverPct   money.Dec      `json:"cover_pct"`
	Shortfall  money.Dec      `json:"shortfall"`
	Accrued    money.Amount   `json:"accrued"` // interest this month, not yet posted
	Blocked    bool           `json:"blocked"`
	BlockedBy  string         `json:"blocked_by,omitempty"`
	Action     string         `json:"action"`           // Block, Unblock or ""
	Legacy     bool           `json:"legacy,omitempty"` // an open legacy debt: kept out of the totals and reminders, still blocked
}

// DebitView is the debtors screen.
type DebitView struct {
	Date      string         `json:"date"`
	BlockDays int            `json:"block_days"`
	Buckets   []string       `json:"buckets"`
	Rows      []DebitRow     `json:"rows"`
	Totals    []money.Amount `json:"totals"`
	Debit     money.Amount   `json:"debit"`
	Shortfall money.Dec      `json:"shortfall"`
	ToBlock   int            `json:"to_block"`
	ToUnblock int            `json:"to_unblock"`
	Accrued   money.Amount   `json:"accrued"`
	// Legacy debts (old debits marked on Legacy debts) are listed with
	// Legacy set but left out of Debit, Totals and Shortfall.
	LegacyCount int          `json:"legacy_count"`
	LegacyDebit money.Amount `json:"legacy_debit"`
}

// Debits lists clients with overdue debit balances on date: how long
// they have been in debit, ageing of the debit (oldest first in, first out),
// the securities that cover it, and whether the front end should block or
// unblock them.
func (o *Ops) Debits(ctx context.Context, tenant, date string) (DebitView, error) {
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return DebitView{}, err
	}
	v := DebitView{Date: date, BlockDays: set.debitBlockDays(), Rows: []DebitRow{}, Totals: make([]money.Amount, len(AgeBuckets))}
	for _, b := range AgeBuckets {
		v.Buckets = append(v.Buckets, b.Label)
	}
	cs, err := o.ClientMap(ctx, tenant)
	if err != nil {
		return v, err
	}
	moves, err := o.clientMoves(ctx, tenant, date, nil)
	if err != nil {
		return v, err
	}
	funds, err := o.clientFunds(ctx, tenant, date, "")
	if err != nil {
		return v, err
	}
	hs, err := o.Holdings(ctx, tenant, "")
	if err != nil {
		return v, err
	}
	_, look, _ := o.Market(ctx, tenant, date)
	type cover struct{ pl, un, coll money.Dec }
	cov := map[string]*cover{}
	for _, h := range hs {
		i, ok := look.ByISIN(h.ISIN)
		if !ok {
			continue
		}
		c := cov[h.UCC]
		if c == nil {
			c = &cover{}
			cov[h.UCC] = c
		}
		pv, uv := i.LTP.MulInt(h.Pledged), i.LTP.MulInt(h.Qty-h.Pledged)
		c.pl, c.un = c.pl.Add(pv), c.un.Add(uv)
		c.coll = c.coll.Add(pv.Add(uv).Pct(money.DecInt(100).Sub(i.HaircutPct)))
	}
	var accrued map[string]money.Amount
	if iv, err := o.Interest(ctx, tenant, date[:8]+"01", date); err == nil {
		accrued = map[string]money.Amount{}
		for _, r := range iv.Rows {
			if r.Status == "To post" || r.Status == "Below minimum" {
				accrued[r.UCC] = r.Interest
			}
		}
	}
	legacy := o.legacyOpen(ctx, tenant)
	for _, u := range sortedKeys(cs) {
		c := cs[u]
		ms := moves[u]
		var bal money.Amount
		for _, m := range ms {
			bal += m.Amount
		}
		blockedByUs := c.Blocked && strings.HasPrefix(c.BlockedReason, debitReason)
		if bal >= 0 && !blockedByUs {
			continue
		}
		r := DebitRow{UCC: u, Name: c.Name, Tags: c.Tags, Ledger: funds[u], Blocked: c.Blocked, Ageing: make([]money.Amount, len(AgeBuckets)), Legacy: legacy[u] && bal < 0}
		totals := v.Totals
		if r.Legacy {
			totals = make([]money.Amount, len(AgeBuckets)) // not in the firm's ageing
		}
		if r.Tags == nil {
			r.Tags = []string{}
		}
		if c.Blocked {
			r.BlockedBy = c.BlockedReason
		}
		if bal < 0 {
			r.Debit = -bal
			// Spell start: walk back over the days the balance stayed negative.
			if len(ms) > 0 {
				bals := dailyBalances(ms, ms[0].Date, date)
				i := len(bals) - 1
				for i > 0 && bals[i-1] < 0 {
					i--
				}
				r.Since = addDays(ms[0].Date, i)
				r.Days = len(bals) - i
			}
			// FIFO ageing: debits form lots, credits clear the oldest first.
			type lot struct {
				d string
				a money.Amount
			}
			var lots []lot
			var credit money.Amount
			for _, m := range ms {
				if m.Amount < 0 {
					amt := -m.Amount
					if credit > 0 {
						use := min(credit, amt)
						credit -= use
						amt -= use
					}
					if amt > 0 {
						lots = append(lots, lot{m.Date, amt})
					}
					continue
				}
				cr := m.Amount
				for cr > 0 && len(lots) > 0 {
					use := min(cr, lots[0].a)
					lots[0].a -= use
					cr -= use
					if lots[0].a == 0 {
						lots = lots[1:]
					}
				}
				credit += cr
			}
			for _, l := range lots {
				age := daysBetween(l.d, date)
				for b, bk := range AgeBuckets {
					if age <= bk.MaxDay {
						r.Ageing[b] += l.a
						totals[b] += l.a
						break
					}
				}
			}
		}
		if cv := cov[u]; cv != nil {
			r.Pledged, r.Unpledged, r.Collateral = cv.pl, cv.un, cv.coll
		}
		if r.Debit > 0 {
			if q, err := r.Collateral.MulInt(100).Quo(r.Debit.Dec()); err == nil {
				r.CoverPct = money.DecInt(q.Round(money.UnitRupee).Paise() / 100)
			}
			if sf := r.Debit.Dec().Sub(r.Collateral); sf.Sign() > 0 {
				r.Shortfall = sf
				if !r.Legacy {
					v.Shortfall = v.Shortfall.Add(sf)
				}
			}
		}
		r.Accrued = accrued[u]
		v.Accrued += r.Accrued
		switch {
		case r.Debit > 0 && r.Days >= v.BlockDays && !c.Blocked && c.Status == "Active":
			r.Action = "Block"
			v.ToBlock++
		case r.Debit == 0 && blockedByUs:
			r.Action = "Unblock"
			v.ToUnblock++
		}
		if r.Legacy {
			v.LegacyCount++
			v.LegacyDebit += r.Debit
		} else {
			v.Debit += r.Debit
		}
		v.Rows = append(v.Rows, r)
	}
	sort.SliceStable(v.Rows, func(i, j int) bool {
		if v.Rows[i].Days != v.Rows[j].Days {
			return v.Rows[i].Days > v.Rows[j].Days
		}
		return v.Rows[i].Debit > v.Rows[j].Debit
	})
	return v, nil
}

// ApplyDebitBlocks blocks buying for clients in continuous debit for the
// firm's limit and unblocks clients this rule blocked who are back in
// credit. Other blocks (the RMS kill switch) are left alone.
func (o *Ops) ApplyDebitBlocks(ctx context.Context, tenant, actor, date string) (blocked, unblocked int, err error) {
	if err := o.require(ctx, tenant, actor, "Blocking clients in continuous debit", RoleRisk); err != nil {
		return 0, 0, err
	}
	v, err := o.Debits(ctx, tenant, date)
	if err != nil {
		return 0, 0, err
	}
	for _, r := range v.Rows {
		switch r.Action {
		case "Block":
			reason := fmt.Sprintf("%s since %s (%d days, ₹%s)", debitReason, r.Since, r.Days, r.Debit)
			if _, err := docs.Update(ctx, o.Docs, tenant, cClient, r.UCC, actor, func(c *Client) error {
				c.Blocked, c.BlockedReason = true, reason
				return nil
			}); err != nil {
				return blocked, unblocked, err
			}
			o.log(ctx, tenant, actor, "debit.blocked", r.UCC, map[string]any{"days": r.Days, "debit": r.Debit.String()})
			blocked++
		case "Unblock":
			if _, err := docs.Update(ctx, o.Docs, tenant, cClient, r.UCC, actor, func(c *Client) error {
				if !strings.HasPrefix(c.BlockedReason, debitReason) {
					return errors.New("blocked for another reason")
				}
				c.Blocked, c.BlockedReason = false, ""
				return nil
			}); err != nil {
				continue
			}
			o.log(ctx, tenant, actor, "debit.unblocked", r.UCC, nil)
			unblocked++
		}
	}
	return blocked, unblocked, nil
}

// DebitBlockFile is the block/unblock file for the trading front end: one
// row per client whose buying must be blocked (in continuous debit) or
// released (back in credit after a debit block).
func (o *Ops) DebitBlockFile(ctx context.Context, tenant, date string) ([]byte, error) {
	v, err := o.Debits(ctx, tenant, date)
	if err != nil {
		return nil, err
	}
	rows := [][]string{{"UCC", "Action", "Segments", "Days in debit", "Debit", "Reason"}}
	for _, r := range v.Rows {
		act := r.Action
		if act == "" && r.Blocked && strings.HasPrefix(r.BlockedBy, debitReason) {
			act = "Block" // already blocked here: resend so the front end agrees
		}
		switch act {
		case "Block":
			rows = append(rows, []string{r.UCC, "BLOCK_BUY", "ALL", fmt.Sprint(r.Days), r.Debit.String(), "Continuous debit"})
		case "Unblock":
			rows = append(rows, []string{r.UCC, "UNBLOCK", "ALL", "0", "0.00", "Debit cleared"})
		}
	}
	return CSV(rows), nil
}

func divInt(d money.Dec, n int64) money.Dec {
	q, err := d.Quo(money.DecInt(n))
	if err != nil {
		return money.Dec{}
	}
	return q
}

// checkLock refuses changes dated on or before the locked period.
func (o *Ops) checkLock(ctx context.Context, tenant, date string) error {
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return err
	}
	if l := set.lockedUpTo(o.now().Format("2006-01-02")); l != "" && date <= l {
		return fmt.Errorf("%w: the books are locked up to %s; ask an admin to unlock the period", ErrState, l)
	}
	return nil
}
