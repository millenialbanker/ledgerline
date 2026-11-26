package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/money"
)

// ---- the weekly statement of funds and securities: what stands open ----
//
// NSE/INSP/73976 (the Inspection master circular), item 4.5, sets the
// weekly statement: the week Monday to Saturday, balances as on the
// Saturday after the open bills, uncleared cheques and posted margins, the
// pending pay-in and pay-out obligations and the uncleared cheques listed
// separately, with the clauses on the daily margin statement and the
// running account authorisation, sent within four trading days. Every
// active client gets it, a zero balance too; only a client with nothing
// (zero balance, no movement, nothing with the broker) who is not active
// may be left out.
//
// The circular's twelve sub-items are applied as the gap report lists
// them; the full text was not at hand, so where it may ask for more (for
// example a particular order of the sections) the statement keeps
// Ledgerline's order.

// fsWeek is what the week's statements share: the pending obligations,
// the uncleared cheques and the margin of every client as on the Saturday,
// worked out once for a bulk send.
type fsWeek struct {
	tenant, asOn string
	marginOn     string
	obligations  map[string][][]string
	cheques      map[string][][]string
	chequeTotal  map[string]money.Dec
	margin       map[string]MarginRow
}

type fsWeekKey struct{}

// withFSWeek keeps the week's shared figures in the context, so a bulk
// send works them out once rather than once per client.
func (o *Ops) withFSWeek(ctx context.Context, tenant, asOn string) context.Context {
	w := o.fsWeekFor(ctx, tenant, asOn)
	return context.WithValue(ctx, fsWeekKey{}, w)
}

func (o *Ops) fsWeekOf(ctx context.Context, tenant, asOn string) *fsWeek {
	if w, ok := ctx.Value(fsWeekKey{}).(*fsWeek); ok && w.tenant == tenant && w.asOn == asOn {
		return w
	}
	return o.fsWeekFor(ctx, tenant, asOn)
}

func (o *Ops) fsWeekFor(ctx context.Context, tenant, asOn string) *fsWeek {
	w := &fsWeek{tenant: tenant, asOn: asOn, obligations: map[string][][]string{}, cheques: map[string][][]string{}, chequeTotal: map[string]money.Dec{}, margin: map[string]MarginRow{}}
	set, _ := o.Settings(ctx, tenant)
	// Pending obligations: the trades of the week's last trading days
	// whose pay-in or pay-out falls after the Saturday.
	d := asOn
	if !set.IsBusinessDay(d) {
		d = set.PrevBusinessDay(d)
	}
	w.marginOn = d
	for k := 0; k < 2 && d > addDays(asOn, -6); k++ {
		st, err := o.Obligations(ctx, tenant, d)
		if err == nil && st.PayIn > asOn {
			for _, ob := range st.Obligations {
				if ob.NetQty == 0 && ob.Funds.Sign() == 0 {
					continue
				}
				what, side := "Pay-out to you", ob.Funds
				if ob.Funds.Sign() < 0 {
					what, side = "Pay-in by you", ob.Funds.Neg()
				}
				sec := "Receive"
				q := ob.NetQty
				if q < 0 {
					sec, q = "Deliver", -q
				}
				w.obligations[ob.UCC] = append(w.obligations[ob.UCC], []string{d, st.PayIn, ob.Exchange + " " + ob.Symbol, fmt.Sprintf("%s %d", sec, q), what, d2s(side)})
			}
			for _, m := range st.MTM {
				if m.Amount.Sign() == 0 {
					continue
				}
				what, side := "Pay-out to you", m.Amount
				if m.Amount.Sign() < 0 {
					what, side = "Pay-in by you", m.Amount.Neg()
				}
				w.obligations[m.UCC] = append(w.obligations[m.UCC], []string{d, st.PayIn, m.Segment + " mark-to-market", "", what, d2s(side)})
			}
		}
		d = set.PrevBusinessDay(d)
	}
	// Cheques received by the Saturday and not cleared by it.
	rs, _ := docs.List[Receipt](ctx, o.Docs, tenant, cReceipt, "")
	sort.Slice(rs, func(i, j int) bool { return rs[i].Date < rs[j].Date })
	for _, r := range rs {
		if r.Mode != "Cheque" || r.Date > asOn {
			continue
		}
		open := r.Status == "Uncleared" || ((r.Status == "Credited" || r.Status == "Bounced") && r.ClearedOn > asOn)
		if !open {
			continue
		}
		w.cheques[r.UCC] = append(w.cheques[r.UCC], []string{r.Date, firstOf(r.Ref, "-"), firstOf(r.DraweeBank, "-"), d2s(r.Amount.Dec())})
		w.chequeTotal[r.UCC] = w.chequeTotal[r.UCC].Add(r.Amount.Dec())
	}
	// Margin posted at the end of the week's last trading day.
	if rep, err := o.Margins(ctx, tenant, w.marginOn); err == nil {
		for _, r := range rep.Rows {
			w.margin[r.UCC] = r
		}
	}
	return w
}

// fsWeekSections adds the week's open items to the statement: the funds
// balance after uncleared cheques and margin, and the pending obligations
// and uncleared cheques as sections of their own.
func (o *Ops) fsWeekSections(ctx context.Context, tenant string, r *ClientReport, closing money.Amount) []Section {
	w := o.fsWeekOf(ctx, tenant, r.To)
	c, _ := o.Client(ctx, tenant, r.UCC)
	held := closing.Dec().Neg() // + is money held for the client
	chq := w.chequeTotal[r.UCC]
	m := w.margin[r.UCC]
	fromFunds := money.Max(m.EOD.Sub(m.Collateral), money.Dec{})
	free := money.Max(held.Sub(chq).Sub(fromFunds), money.Dec{})
	bal := Section{Title: "Balance as on " + fmtDay(r.To), Columns: []Column{{"item", "", ColText}, {"amount", "Amount", ColMoney}},
		Rows: [][]string{
			{"Ledger balance after open bills (" + fsDrCr(closing) + ")", d2s(closing.Dec().Abs())},
			{"Less: cheques received and not yet cleared", d2s(chq)},
			{"Margin posted at the end of " + fmtDay(w.marginOn), d2s(m.EOD)},
			{"of which covered by pledged securities (after haircut)", d2s(money.Min(m.Collateral, m.EOD))},
			{"Funds free of obligations, uncleared cheques and margin", d2s(free)},
		},
		Note: "Open bills are the trades of the week already billed; their pay-in or pay-out may fall after the Saturday and is listed under Pending obligations."}
	ob := Section{Title: "Pending obligations (pay-in and pay-out after " + fmtDay(r.To) + ")", Columns: []Column{{"trade", "Trade date", ColDate}, {"settle", "Settles on", ColDate}, {"security", "Security / segment", ColText}, {"qty", "Securities", ColText}, {"what", "Funds", ColText}, {"amount", "Amount", ColMoney}},
		Rows: w.obligations[r.UCC], Empty: "No pay-in or pay-out is pending."}
	ch := Section{Title: "Uncleared cheques", Columns: []Column{{"date", "Received on", ColDate}, {"ref", "Cheque no.", ColText}, {"bank", "Drawn on", ColText}, {"amount", "Amount", ColMoney}},
		Rows: w.cheques[r.UCC], Empty: "No cheque is waiting to clear."}
	if len(ch.Rows) > 0 {
		ch.Totals = []string{"Total", "", "", d2s(chq)}
	}
	r.Notes = append(r.Notes, fsClauses(c)...)
	return []Section{bal, ob, ch}
}

// fsDrCr says whether a ledger balance is a debit (owed by the client) or a
// credit (held for the client).
func fsDrCr(a money.Amount) string {
	if a > 0 {
		return "Dr, payable by you"
	}
	if a < 0 {
		return "Cr, held for you"
	}
	return "nil"
}

// fsClauses are the clauses the weekly statement carries (item 4.5).
func fsClauses(c Client) []string {
	ras := firstOf(c.RAS, "Quarterly")
	return []string{
		"Daily margin statement: for each day you had a margin, we sent you a daily margin statement showing the margin required, collected and any shortfall. If you did not receive one, tell us.",
		"Running account: you have authorised us to keep your account on a running basis, settled " + strings.ToLower(ras) + " on the exchanges' settlement dates. You may change or revoke this authorisation at any time by writing to us.",
		"Pending obligations and uncleared cheques are shown separately and are not yet settled; cheques count as paid only when cleared.",
		// SEBI master circular for stock brokers, para 50.1.4: clients are
		// told the voluntary freeze facility is there and how to use it.
		"Freezing your account: if you suspect misuse, you can ask us at any time to freeze (block) your trading account, from your registered email or mobile number or from the client portal's Help desk. We acknowledge the request, block trading, cancel pending orders and confirm it to you; trading is re-enabled only after we verify you.",
	}
}

// siList names up to n items, with how many more.
func siList(xs []string, n int) string {
	if len(xs) <= n {
		return strings.Join(xs, ", ")
	}
	return strings.Join(xs[:n], ", ") + fmt.Sprintf(" and %d more", len(xs)-n)
}

// tradedBy says whether Ledgerline has any exchange trade on or before
// the date.
func (o *Ops) tradedBy(ctx context.Context, tenant, date string) bool {
	ts, _ := o.App.Trades.List(ctx, tenant, ingest.FromExchange, "")
	for _, t := range ts {
		if t.TradeDate <= date {
			return true
		}
	}
	return false
}

// fsRecentTrader: the client traded in the 12 months to the date, so gets
// the weekly statement whatever its balance.
func fsRecentTrader(c Client, date string) bool {
	return c.LastTrade != "" && c.LastTrade >= addDays(date, -365)
}

// siMaskAll masks client codes for examples.
func siMaskAll(us []string) []string {
	out := make([]string, len(us))
	for i, u := range us {
		out[i] = siMask(u)
	}
	return out
}
