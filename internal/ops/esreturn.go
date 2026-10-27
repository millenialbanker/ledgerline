package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
	"ledgerline/internal/sheet"
)

// ---- enhanced supervision: monitoring of clients' funds ----
//
// Every week the broker reports to the exchanges the client money it holds
// and where: balances in client bank accounts, collateral with the
// clearing corporations, the clients' credit and debit balances, the
// firm's own collateral and bank guarantees, and the margin used by
// clients with credit balances. The exchanges compare the client money
// that should exist with what is held, and the workbook carries the
// client-wise detail behind each figure.

// ESParticulars are the lines of the weekly statement, in the exchanges'
// order and words.
var ESParticulars = []string{
	"Total of day end balance in all Client Bank Accounts ( In Rs.)",
	"Collateral deposited with exchanges in form of Cash and Cash Equivalents (In Rs.)",
	"Collateral deposited with clearing member in form of Cash and Cash Equivalents (In Rs.)",
	"Total Credit Balance of all clients ( In Rs.)",
	"Total debit balance of all clients ( In Rs.)",
	"Value of own securities deposited as collateral with Clearing corporation  ( In Rs.)",
	"Value of Own Securities Deposited as Collateral with Clearing Member  ( In Rs.)",
	"Value of Bank Guarantee  ( In Rs.)",
	"Proprietary margin Obligation  ( In Rs.)",
	"Margin utilized for positions of Credit Balance Clients  ( In Rs.)",
	"Free/unblocked  Collateral deposited with Clearing corporation ( In Rs.)",
	"Free/unblocked  Collateral deposited with Clearing Member ( In Rs.)",
}

// ESReturn is the week's figures.
type ESReturn struct {
	Date    string      `json:"date"`
	Amounts []money.Dec `json:"amounts"` // one per ESParticulars line
	Credits int         `json:"credit_clients"`
	Debits  int         `json:"debit_clients"`
}

// EnhancedSupervision builds the statement as of date and its workbook.
func (o *Ops) EnhancedSupervision(ctx context.Context, tenant, date string) (ESReturn, []byte, error) {
	if !validDate(date) {
		return ESReturn{}, nil, invalid("give the date (YYYY-MM-DD)")
	}
	f, err := o.Funds(ctx, tenant, date)
	if err != nil {
		return ESReturn{}, nil, err
	}
	bal, err := o.App.Ledger.Balances(ctx, tenant, date)
	if err != nil {
		return ESReturn{}, nil, err
	}
	cs, err := o.ClientMap(ctx, tenant)
	if err != nil {
		return ESReturn{}, nil, err
	}
	risk, err := o.Risk(ctx, tenant, date)
	if err != nil {
		return ESReturn{}, nil, err
	}
	dep, _ := docs.List[Deposit](ctx, o.Docs, tenant, cDeposit, "")
	var depCash, depSec, depBG money.Dec
	depRows := [][]string{{"Exchange", "Code", "Account Head", "Account Type", "Account Descr", "Balance", "Total"}}
	sort.Slice(dep, func(i, j int) bool { return dep[i].CC+dep[i].ID < dep[j].CC+dep[j].ID })
	for _, d := range dep {
		if d.Status != "Active" || (d.Issued != "" && d.Issued > date) {
			continue
		}
		switch d.Kind {
		case "Cash", "Fixed deposit":
			depCash = depCash.Add(d.Amount)
		case "Bank guarantee":
			depBG = depBG.Add(d.Amount)
		default:
			depSec = depSec.Add(d.Value)
		}
		depRows = append(depRows, []string{d.CC, d.Ref, d.Purpose, d.Kind, strings.TrimSpace(d.Issuer + " " + d.Segment), d.Amount.Fixed(2), d.Value.Fixed(2)})
	}
	if f.WithCC.Sign() != 0 {
		depRows = append(depRows, []string{"All", "", "Client funds up-streamed", "Cash", "Client money with clearing corporations", f.WithCC.Fixed(2), f.WithCC.Fixed(2)})
	}
	// Uncleared receipts per client (cheques deposited, not yet credited).
	unclr := map[string]money.Dec{}
	if rs, err := o.Receipts(ctx, tenant); err == nil {
		for _, r := range rs {
			if r.Status == "Uncleared" && r.Date <= date {
				unclr[r.UCC] = unclr[r.UCC].Add(r.Amount.Dec())
			}
		}
	}
	riskBy := map[string]RiskRow{}
	for _, r := range risk.Rows {
		riskBy[r.UCC] = r
	}
	credits := [][]string{{"UCC", "Client", "Ledger", "Uncleared Receipts", "Uncleared Payments", "Net Credit"}}
	debits := [][]string{{"UCC", "Client Name", "Ledger", "Uncleared Receipts", "Uncleared Payments", "Net Debit"}}
	util := [][]string{{"Ucc", "Client", "Net Credit Balance (A)", "Total Margin (B)", "Total Repledged (C)", "Total Adjusted Share Margin (D)", "Utilized Margin minimum of (A and (B-D))"}}
	var used money.Dec
	es := ESReturn{Date: date}
	for _, u := range sortedKeys(cs) {
		v := bal["CLIENT:"+u].Dec().Neg() // credit-positive
		if v.Sign() == 0 && unclr[u].Sign() == 0 {
			continue
		}
		net := v.Sub(unclr[u])
		switch {
		case v.Sign() > 0:
			es.Credits++
			credits = append(credits, []string{u, cs[u].Name, v.Fixed(2), unclr[u].Fixed(2), "0.00", net.Fixed(2)})
		case v.Sign() < 0:
			es.Debits++
			debits = append(debits, []string{u, cs[u].Name, v.Neg().Fixed(2), unclr[u].Fixed(2), "0.00", v.Neg().Add(unclr[u]).Fixed(2)})
		}
		if r, ok := riskBy[u]; ok && net.Sign() > 0 && r.Required.Sign() > 0 {
			cash := money.Max(r.Required.Sub(r.CollUsable), money.Dec{})
			u2 := money.Min(net, cash)
			used = used.Add(u2)
			util = append(util, []string{u, cs[u].Name, net.Fixed(2), r.Required.Fixed(2), r.Collateral.Fixed(2), r.CollUsable.Fixed(2), u2.Fixed(2)})
		}
	}
	banks := [][]string{{"Exchange", "Bank Name", "A/C #", "Bank Balance As per Our Books", "Bank Balance As Per Bank Book"}}
	for _, k := range sortedKeys(bal) {
		if strings.HasPrefix(k, "BANK:") {
			banks = append(banks, []string{"All", k, "", bal[k].Dec().Fixed(2), ""})
		}
	}
	withCC := f.WithCC.Add(depCash)
	free := money.Max(withCC.Add(depSec).Add(depBG).Sub(used), money.Dec{})
	es.Amounts = []money.Dec{f.ClientBank, withCC, {}, f.ClientCredits, f.ClientDebits, depSec, {}, depBG, {}, used, free, {}}
	part := [][]string{{"Particulars", "Amount"}}
	for i, l := range ESParticulars {
		part = append(part, []string{l, es.Amounts[i].Fixed(2)})
	}
	wb, err := sheet.WriteXLSX([]sheet.Sheet{{Name: "Weekly Particulars", Rows: part}, {Name: "Client Credits", Rows: credits}, {Name: "Client Debits", Rows: debits},
		{Name: "Bank Balance", Rows: banks}, {Name: "Exch or CM Deposits", Rows: depRows}, {Name: "Utilzed Margins of Clients", Rows: util},
		{Name: "Utilzed Margins of Pro", Rows: [][]string{{"UCC", "Client", "Net Balance", "Total Margin Utilized"}}}})
	return es, wb, err
}

func (o *Ops) runESReturn(ctx context.Context, tenant, date string) (string, []PeriodicFile, error) {
	es, wb, err := o.EnhancedSupervision(ctx, tenant, date)
	if err != nil {
		return "", nil, err
	}
	f, err := o.keepFile(ctx, tenant, "ClientFundsMonitoring_"+fmtDDMMYYYY(date)+".xlsx", wb)
	if err != nil {
		return "", nil, err
	}
	o.raiseWork(ctx, tenant, "periodic", "es:"+date, "Upload the client funds monitoring return for "+fmtDay(date),
		"The workbook is ready on Periodic work; upload it on each exchange's member portal and note the acknowledgement.", "", RoleCompliance, "periodic", date)
	return fmt.Sprintf("Client credits ₹%s (%d clients), debits ₹%s (%d), client bank balance ₹%s", formatINR(es.Amounts[3]), es.Credits, formatINR(es.Amounts[4]), es.Debits, formatINR(es.Amounts[0])), []PeriodicFile{f}, nil
}
