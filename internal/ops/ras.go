package ops

import (
	"context"
	"fmt"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// RASRow is one client's running account settlement position.
type RASRow struct {
	UCC        string    `json:"ucc"`
	Name       string    `json:"name"`
	Preference string    `json:"preference"`
	LastTrade  string    `json:"last_trade"`
	Inactive   bool      `json:"inactive"`
	Credit     money.Dec `json:"credit"`
	Retention  money.Dec `json:"retention"`
	Payout     money.Dec `json:"payout"`
	SettlesOn  string    `json:"settles_on"`
	Settled    bool      `json:"settled"`
	Bank       string    `json:"bank"`
	// Idle30: a credit balance with no transaction for 30 calendar days
	// (NSE/INSP/53820, FAQ 2–4). It is returned within three working days
	// of IdleFrom (by IdleDue), not on the monthly or quarterly date.
	// LastActivity is the later of the last trade and the last funds
	// received; a client with open positions or margin is not idle.
	Idle30       bool   `json:"idle_30,omitempty"`
	LastActivity string `json:"last_activity,omitempty"`
	IdleFrom     string `json:"idle_from,omitempty"`
	IdleDue      string `json:"idle_due,omitempty"`
}

// rasIdleDays is the rule's period without a transaction: 30 calendar days.
const rasIdleDays = 30

// rasIdleWorkingDays is how many working days the broker then has.
const rasIdleWorkingDays = 3

// addWorkingDays is the n-th business day after date.
func addWorkingDays(set Settings, date string, n int) string {
	d := date
	for i := 0; i < n; i++ {
		d = set.NextBusinessDay(d)
	}
	return d
}

// lastFundsIn is each client's last receipt of funds: fresh funds restart
// the 30 days (NSE/INSP/53820, FAQ 2).
func (o *Ops) lastFundsIn(ctx context.Context, tenant, asOf string) map[string]string {
	out := map[string]string{}
	rs, _ := o.Receipts(ctx, tenant)
	for _, r := range rs {
		if (r.Status == "Credited" || r.Status == "Uncleared") && r.Date <= asOf && r.Date > out[r.UCC] {
			out[r.UCC] = r.Date
		}
	}
	return out
}

// RASBatch is one posted settlement run.
type RASBatch struct {
	Date    string       `json:"date"`
	AsOf    string       `json:"as_of"`
	Items   []RASItem    `json:"items"`
	Total   money.Amount `json:"total"`
	By      string       `json:"by"`
	At      string       `json:"at"`
	Entries []string     `json:"entries"`
}

// RASItem is one payout in a batch.
type RASItem struct {
	UCC     string       `json:"ucc"`
	Name    string       `json:"name"`
	Bank    string       `json:"bank"`
	IFSC    string       `json:"ifsc"`
	Account string       `json:"account"`
	Amount  money.Amount `json:"amount"`
	// Credit and Retained are the balance and the amount kept back at the
	// time of settlement, for the client's statement.
	Credit   money.Dec `json:"credit"`
	Retained money.Dec `json:"retained"`
}

// RASView is the running account page.
type RASView struct {
	AsOf      string     `json:"as_of"`
	Monthly   string     `json:"monthly_date"`
	Quarterly string     `json:"quarterly_date"`
	BufferPct int64      `json:"buffer_pct"`
	Rows      []RASRow   `json:"rows"`
	Batches   []RASBatch `json:"batches"`
}

func (o *Ops) lastTrades(ctx context.Context, tenant string) (map[string]string, error) {
	all, err := o.App.Trades.List(ctx, tenant, ingest.FromExchange, "")
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, t := range all {
		if t.TradeDate > out[t.UCC] {
			out[t.UCC] = t.TradeDate
		}
	}
	if o.App.Adjust != nil {
		if adj, err := o.App.Adjust(ctx, tenant, all); err == nil {
			for _, t := range adj {
				if t.TradeDate > out[t.UCC] {
					out[t.UCC] = t.TradeDate
				}
			}
		}
	}
	return out, nil
}

// RAS computes running account settlement as of a date. Clients with a
// credit balance are paid out the excess over their retention (margin
// requirement plus the configured buffer). Monthly clients settle on the
// monthly date and quarterly clients on the quarterly date. A client with
// a credit balance and no transaction for 30 calendar days is settled
// within three working days, whatever their choice (NSE/INSP/53820): such
// a row is included in any settlement run from IdleFrom on.
func (o *Ops) RAS(ctx context.Context, tenant, asOf string) (RASView, error) {
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return RASView{}, err
	}
	risk, err := o.Risk(ctx, tenant, asOf)
	if err != nil {
		return RASView{}, err
	}
	clients, err := o.ClientMap(ctx, tenant)
	if err != nil {
		return RASView{}, err
	}
	last, err := o.lastTrades(ctx, tenant)
	if err != nil {
		return RASView{}, err
	}
	batches, err := docs.List[RASBatch](ctx, o.Docs, tenant, cRASBatch, "")
	if err != nil {
		return RASView{}, err
	}
	done := map[string]bool{}
	lastDone := map[string]string{}
	for _, b := range batches {
		for _, it := range b.Items {
			done[b.Date+"|"+it.UCC] = true
			if b.Date > lastDone[it.UCC] {
				lastDone[it.UCC] = b.Date
			}
		}
	}
	v := RASView{AsOf: asOf, Monthly: set.RASMonthly, Quarterly: set.RASQuarterly, BufferPct: set.RASBufferPct, Rows: []RASRow{}, Batches: batches}
	// The rule's 30 days; a firm may use a shorter period, never a longer.
	idleDays := int64(rasIdleDays)
	if set.InactiveDays > 0 && set.InactiveDays < idleDays {
		idleDays = set.InactiveDays
	}
	funds := o.lastFundsIn(ctx, tenant, asOf)
	for _, r := range risk.Rows {
		if r.Funds.Sign() <= 0 {
			continue
		}
		c := clients[r.UCC]
		lt := last[r.UCC]
		if c.LastTrade > lt {
			lt = c.LastTrade
		}
		row := RASRow{UCC: r.UCC, Name: c.Name, Preference: c.RAS, LastTrade: lt, Credit: r.Funds, Bank: c.Bank.Bank + " " + MaskAccount(c.Bank.Account)}
		row.LastActivity = lt
		if funds[r.UCC] > row.LastActivity {
			row.LastActivity = funds[r.UCC]
		}
		// Open positions (margin required) count as a transaction until
		// they expire or are closed.
		if r.Required.Sign() <= 0 && row.LastActivity != "" && int64(daysBetween(row.LastActivity, asOf)) >= idleDays {
			row.Idle30, row.Inactive = true, true
			row.IdleFrom = addDays(row.LastActivity, int(idleDays))
			row.IdleDue = addWorkingDays(set, row.IdleFrom, rasIdleWorkingDays)
		}
		row.Retention = r.Required.Pct(money.DecInt(100 + set.RASBufferPct)).CeilRupee()
		row.Payout = money.Max(row.Credit.Sub(row.Retention), money.Dec{})
		// No trade or receipt on record (such as a migrated balance): the
		// 30 days cannot be counted, so it settles on the monthly date.
		row.Inactive = row.Inactive || row.LastActivity == ""
		switch {
		case row.Idle30:
			row.SettlesOn = row.IdleDue
			row.Settled = lastDone[r.UCC] >= row.IdleFrom
		case row.Inactive || c.RAS == "Monthly":
			row.SettlesOn = set.RASMonthly
		default:
			row.SettlesOn = set.RASQuarterly
		}
		if !row.Idle30 {
			row.Settled = done[row.SettlesOn+"|"+r.UCC]
		}
		v.Rows = append(v.Rows, row)
	}
	return v, nil
}

// dueIn says whether a row is settled by a run for settleDate: its own
// settlement date, or, for an idle credit balance, any run from the day
// the 30 days are up (the earlier the better; the deadline is IdleDue).
func (r RASRow) dueIn(settleDate string) bool {
	if r.Settled {
		return false
	}
	if r.Idle30 {
		return settleDate >= r.IdleFrom
	}
	return r.SettlesOn == settleDate
}

// IdleRASDue lists the idle credit balances to return as of date, and
// those past their three-working-day deadline.
func (o *Ops) IdleRASDue(ctx context.Context, tenant, date string) (due, overdue []RASRow, err error) {
	v, err := o.RAS(ctx, tenant, date)
	if err != nil {
		return nil, nil, err
	}
	for _, r := range v.Rows {
		if r.Idle30 && r.dueIn(date) && r.Payout.Round(money.UnitPaisa) > 0 {
			due = append(due, r)
			if date > r.IdleDue {
				overdue = append(overdue, r)
			}
		}
	}
	return due, overdue, nil
}

// queueIdleRAS asks a checker to approve returning the idle balances due
// on date (the same "ras.settle" approval as a settlement date).
func queueIdleRAS(r *runCtx, date string, due []RASRow) {
	var total money.Dec
	first := ""
	for _, x := range due {
		total = total.Add(x.Payout)
		if first == "" || x.IdleDue < first {
			first = x.IdleDue
		}
	}
	r.queue("ras30:"+date, fmt.Sprintf("Return idle credit balances of %d clients (no transaction for 30 days)", len(due)),
		fmt.Sprintf("₹%s to %d clients who have not transacted for 30 days must be returned within three working days (the first by %s), not on the next settlement date (NSE/INSP/53820). Approving settles them and sends each the settlement statement.", formatINR(total), len(due), fmtDay(first)),
		"", RoleChecker, "ras", first+" 17:00", &Proposal{Kind: "ras.settle", Label: "Settle and pay out", Args: map[string]string{"date": date}})
}

// RunRAS posts payouts for every unsettled client due on the settlement
// date. Each payout is idempotent per client and settlement date.
func (o *Ops) RunRAS(ctx context.Context, tenant, actor, asOf, settleDate string) (RASBatch, error) {
	if err := o.require(ctx, tenant, actor, "Running account settlement", RoleChecker); err != nil {
		return RASBatch{}, err
	}
	if !ledger.ValidDate(settleDate) {
		return RASBatch{}, invalid("settlement date must be YYYY-MM-DD")
	}
	v, err := o.RAS(ctx, tenant, asOf)
	if err != nil {
		return RASBatch{}, err
	}
	clients, err := o.ClientMap(ctx, tenant)
	if err != nil {
		return RASBatch{}, err
	}
	b := RASBatch{Date: settleDate, AsOf: asOf, Items: []RASItem{}, Entries: []string{}, By: actor, At: o.now().Format("2006-01-02 15:04")}
	for _, r := range v.Rows {
		if !r.dueIn(settleDate) {
			continue
		}
		amt := r.Payout.Round(money.UnitPaisa)
		if amt <= 0 {
			continue
		}
		c := clients[r.UCC]
		e, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: tenant, Key: "RAS:" + settleDate + ":" + r.UCC, ValueDate: asOf,
			Narration: fmt.Sprintf("Running account settlement for %s · payout to %s %s", settleDate, c.Bank.Bank, MaskAccount(c.Bank.Account)),
			Source:    ledger.SourceRef{Kind: "RAS", ID: settleDate + "/" + r.UCC}, CreatedBy: actor,
			Postings: []ledger.Posting{{Account: ledger.ClientAccount(r.UCC), Debit: amt, Memo: "Running account settlement payout"}, {Account: ledger.BankClient, Credit: amt}}})
		if err != nil {
			return b, err
		}
		b.Items = append(b.Items, RASItem{UCC: r.UCC, Name: c.Name, Bank: c.Bank.Bank, IFSC: c.Bank.IFSC, Account: c.Bank.Account, Amount: amt, Credit: r.Credit, Retained: r.Credit.Sub(amt.Dec())})
		b.Total += amt
		b.Entries = append(b.Entries, e.ID)
	}
	if len(b.Items) == 0 {
		return b, fmt.Errorf("%w: no unsettled payouts are due on %s", ErrState, settleDate)
	}
	id := settleDate + "|" + o.now().Format("150405.000")
	if err := docs.Upsert(ctx, o.Docs, tenant, cRASBatch, id, b, actor); err != nil {
		return b, err
	}
	o.log(ctx, tenant, actor, "ras.run", settleDate, map[string]any{"clients": len(b.Items), "total": b.Total.String()})
	// Each client gets the statement of the settlement (SEBI requires it).
	if res, err := o.SendStatements(ctx, tenant, SystemActor, DocRAS, settleDate); err != nil || len(res.Errors) > 0 {
		o.log(ctx, tenant, SystemActor, "statements.error", "ras "+settleDate, map[string]any{"error": fmt.Sprint(err, res.Errors)})
	}
	return b, nil
}

// RASBatches lists posted batches for a settlement date ("" for all).
func (o *Ops) RASBatches(ctx context.Context, tenant, settleDate string) ([]RASBatch, error) {
	p := ""
	if settleDate != "" {
		p = settleDate + "|"
	}
	return docs.List[RASBatch](ctx, o.Docs, tenant, cRASBatch, p)
}
