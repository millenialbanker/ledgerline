package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// ---- exclusion criteria shown with a step's reminder ----
//
// Some steps are run by hand today because they have exceptions a person
// wants to see first: the daily interest run leaves out the firm's
// important clients (their own "no interest" terms, an interest-exempt
// group, or a legacy debt; clientinterest.go). Such a step's reminder lists
// who is left out and what is not charged, and the person confirms "run
// with these exemptions" in one click. The list is fingerprinted when it is
// shown, so a confirmation of a list that has changed since is refused.

// StepExceptions are a step's exclusions for a day.
type StepExceptions struct {
	Title   string             `json:"title"`
	Summary string             `json:"summary"`
	From    string             `json:"from,omitempty"`
	To      string             `json:"to,omitempty"`
	Rows    []StepExceptionRow `json:"rows"`
	Amount  money.Amount       `json:"amount"` // not charged, before GST
	GST     money.Amount       `json:"gst"`
	Confirm string             `json:"confirm"` // the button
	// Fingerprint identifies the list shown; ConfirmStep checks it.
	Fingerprint string `json:"fingerprint"`
}

// StepExceptionRow is one exclusion.
type StepExceptionRow struct {
	UCC    string       `json:"ucc"`
	Name   string       `json:"name"`
	Why    string       `json:"why"`
	Debit  money.Amount `json:"debit"` // the overdue debit on the last day
	Days   int          `json:"days"`  // days that would have been charged
	Amount money.Amount `json:"amount"`
}

// stepExceptionFns give a step's exceptions for a day; stepConfirmFns run
// the step with them, as the person confirming.
var stepExceptionFns = map[string]func(o *Ops, ctx context.Context, tenant, date string) (*StepExceptions, error){
	StepAutopilot + ":interest_daily": func(o *Ops, ctx context.Context, tenant, date string) (*StepExceptions, error) {
		return o.InterestExemptions(ctx, tenant, date)
	},
}

var stepConfirmFns = map[string]func(o *Ops, ctx context.Context, tenant, actor, date string) (string, error){
	StepAutopilot + ":interest_daily": func(o *Ops, ctx context.Context, tenant, actor, date string) (string, error) {
		run, err := o.PostDailyInterest(ctx, tenant, actor, date)
		if err != nil {
			return "", err
		}
		// Autopilot on Suggest queued the same posting for approval.
		o.closeWork(ctx, tenant, actor, "interest_daily", "interest:"+date, "posted with the exemptions confirmed")
		return fmt.Sprintf("%d entries for %d clients, interest ₹%s, GST ₹%s", run.Entries, run.Clients, formatINR(run.Interest.Dec()), formatINR(run.GST.Dec())), nil
	},
}

// ---- the daily interest run ----

const cIntDailyMark = "intdailymark" // "last": the latest day daily interest was posted up to

// intDailyMark records how far daily interest has been posted.
type intDailyMark struct {
	Upto string `json:"upto"`
	By   string `json:"by"`
	At   string `json:"at"`
}

// markDailyInterest notes a daily interest run up to a day.
func (o *Ops) markDailyInterest(ctx context.Context, tenant, actor, upto string) {
	m, _, _ := docs.GetOr(ctx, o.Docs, tenant, cIntDailyMark, "last", intDailyMark{})
	if m.Upto >= upto {
		return
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cIntDailyMark, "last", intDailyMark{Upto: upto, By: actor, At: o.now().Format(time.RFC3339)}, actor)
}

// dailyInterestUpto is the latest day daily interest was posted up to.
func (o *Ops) dailyInterestUpto(ctx context.Context, tenant string) intDailyMark {
	m, _, _ := docs.GetOr(ctx, o.Docs, tenant, cIntDailyMark, "last", intDailyMark{})
	return m
}

// InterestExemptions are the clients the daily interest run up to a day
// leaves out although they are in overdue debit past their grace days, with
// the interest not charged: from the day after the last run (a Monday run
// covers the weekend) to the day. A client is left out for a legacy debt, an
// interest-exempt group (Settings) or their own "no interest" terms. The
// amount is at the rate the client would otherwise pay: their own rate,
// else the firm's.
func (o *Ops) InterestExemptions(ctx context.Context, tenant, upto string) (*StepExceptions, error) {
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return nil, err
	}
	if !set.dailyInterest() {
		return nil, invalid("interest is posted monthly")
	}
	// The run covers the days from the one after the last run (or, before
	// any run was recorded, the last day posted, or the day daily posting
	// began).
	from := set.InterestDailyFrom
	if m := o.dailyInterestUpto(ctx, tenant); m.Upto != "" {
		from = max(from, addDays(m.Upto, 1))
	} else if cov, err := o.interestCovered(ctx, tenant); err == nil {
		for _, ps := range cov {
			for _, p := range ps {
				if p[0] == p[1] && p[1] < upto {
					from = max(from, addDays(p[1], 1))
				}
			}
		}
	}
	if from > upto {
		from = upto
	}
	ex := &StepExceptions{Title: "Clients not charged interest", From: from, To: upto, Rows: []StepExceptionRow{}, Confirm: "Run with these exemptions"}
	ib, err := o.interestBook(ctx, tenant)
	if err != nil {
		return nil, err
	}
	cs, err := o.ClientMap(ctx, tenant)
	if err != nil {
		return nil, err
	}
	moves, err := o.clientMoves(ctx, tenant, upto, nil)
	if err != nil {
		return nil, err
	}
	legacy := o.legacyOpen(ctx, tenant)
	gstPct := set.gstRate()
	look := addDays(from, -max(int(set.InterestGraceDays), ib.maxGrace()))
	for _, u := range sortedKeys(moves) {
		c, ok := cs[u]
		if !ok {
			continue
		}
		why := ""
		switch {
		case legacy[u]:
			why = "Legacy debt: no interest"
		default:
			for _, t := range c.Tags {
				if contains(set.InterestExemptTags, t) {
					why = "In the interest-exempt group " + t
					break
				}
			}
		}
		ms := moves[u]
		balance := func(d string) money.Amount {
			var b money.Amount
			for _, m := range ms {
				if m.Date <= d && !(m.Kind == "INTEREST" && m.Date == d) {
					b += m.Amount
				}
			}
			return b
		}
		row := StepExceptionRow{UCC: u, Name: c.Name}
		var gst money.Amount
		streak := 0
		for d := look; d <= upto; d = addDays(d, 1) {
			b := balance(d)
			if b < 0 {
				streak++
			} else {
				streak = 0
			}
			if d < from || b >= 0 {
				continue
			}
			terms := ib.on(u, d)
			w := why
			rate, grace := terms.rate, terms.grace
			if terms.free {
				// No interest on the client's own terms: what the firm's terms
				// would have charged.
				rate, grace = ib.rate, ib.grace
				if w == "" {
					w = "Client's own terms: no interest"
					if h, ok := ib.clients[u]; ok {
						if t, ok := h.At(d); ok && t.Note != "" {
							w += " (" + t.Note + ")"
						}
					}
				}
			}
			if w == "" || streak <= int(grace) {
				continue
			}
			intr := divInt((-b).Dec().Mul(rate), 100*365).Round(money.UnitPaisa)
			if intr <= 0 {
				continue
			}
			row.Why, row.Debit = w, -b
			row.Days++
			row.Amount += intr
			gst += intr.Dec().Pct(gstPct).Round(money.UnitPaisa)
		}
		if row.Days > 0 {
			ex.Rows = append(ex.Rows, row)
			ex.Amount += row.Amount
			ex.GST += gst
		}
	}
	sort.SliceStable(ex.Rows, func(i, j int) bool { return ex.Rows[i].Amount > ex.Rows[j].Amount })
	span := fmtDay(from)
	if from != upto {
		span = fmtDay(from) + " to " + fmtDay(upto)
	}
	switch n := len(ex.Rows); n {
	case 0:
		ex.Summary, ex.Confirm = "No client in overdue debit is exempt for "+span+".", "Run the interest"
	default:
		ex.Summary = fmt.Sprintf("%d client%s exempt for %s: ₹%s interest (and ₹%s GST) not charged.", n, plural(n), span, formatINR(ex.Amount.Dec()), formatINR(ex.GST.Dec()))
	}
	parts := []string{from, upto}
	for _, r := range ex.Rows {
		parts = append(parts, r.UCC+"="+r.Amount.String())
	}
	ex.Fingerprint = shortHash(strings.Join(parts, ";"))
	return ex, nil
}
