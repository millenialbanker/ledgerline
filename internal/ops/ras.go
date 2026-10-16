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
// requirement plus the configured buffer). Monthly clients, and anyone who
// has not traded for the inactivity period, settle on the monthly date;
// quarterly clients on the quarterly date.
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
	for _, b := range batches {
		for _, it := range b.Items {
			done[b.Date+"|"+it.UCC] = true
		}
	}
	v := RASView{AsOf: asOf, Monthly: set.RASMonthly, Quarterly: set.RASQuarterly, BufferPct: set.RASBufferPct, Rows: []RASRow{}, Batches: batches}
	inactiveDays := set.InactiveDays
	if inactiveDays <= 0 {
		inactiveDays = 30
	}
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
		row.Inactive = lt == "" || int64(daysBetween(lt, asOf)) > inactiveDays
		row.Retention = r.Required.Pct(money.DecInt(100 + set.RASBufferPct)).CeilRupee()
		row.Payout = money.Max(row.Credit.Sub(row.Retention), money.Dec{})
		if row.Inactive || c.RAS == "Monthly" {
			row.SettlesOn = set.RASMonthly
		} else {
			row.SettlesOn = set.RASQuarterly
		}
		row.Settled = done[row.SettlesOn+"|"+r.UCC]
		v.Rows = append(v.Rows, row)
	}
	return v, nil
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
		if r.SettlesOn != settleDate || r.Settled {
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
		b.Items = append(b.Items, RASItem{UCC: r.UCC, Name: c.Name, Bank: c.Bank.Bank, IFSC: c.Bank.IFSC, Account: c.Bank.Account, Amount: amt})
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
