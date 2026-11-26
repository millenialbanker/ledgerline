package ops

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"sort"
	"strings"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// ---- FIU-IND cash and non-profit transaction reports (CTR, NTR) ----
//
// SEBI's AML/CFT master circular (SEBI/HO/MIRSD/MIRSDSECFATF/P/CIR/2024/
// 78), paras 73-74, with PML Rules rule 3(1)(A), (B) and (BA): the cash
// transaction report (CTR: cash transactions of more than ₹10 lakh, or
// integrally connected series of them within a month) and the non-profit
// organisation transaction report (NTR: transactions involving receipts by
// non-profit organisations of more than ₹10 lakh) are filed with FIU-IND by
// the 15th of the next month. No NIL report is filed.
//
// Ledgerline takes no cash (ReceiptModes has no cash mode), so it has no
// CTR to make: the monthly run says so. For the NTR it lists every receipt
// from, and every payout to, a client marked "Trust / charity / NGO" of
// more than ₹10 lakh in the month, and the Principal Officer files on
// FINGate what is reportable. Whether a broker's payout to an NPO client is
// a "receipt by the NPO" is not settled in the circular; Ledgerline lists
// both directions, the safe reading, and the Principal Officer decides.
// The worksheet is Ledgerline's own CSV, not FINGate's NTR template, which
// FIU-IND publishes on FINGate.

// npoCSC is the special category that marks a non-profit client.
const npoCSC = "Trust / charity / NGO"

// ntrThreshold is ₹10 lakh: a transaction above it is reportable.
var ntrThreshold = money.Amount(10_00_000 * 100)

// ntrLine is one transaction for the NTR worksheet.
type ntrLine struct {
	Date, UCC, Name, Kind, Mode, Ref string
	Amount                           money.Amount
}

// ntrFor lists the month's NPO transactions above the threshold.
func (o *Ops) ntrFor(ctx context.Context, tenant, month string) ([]ntrLine, error) {
	cs, err := o.ClientMap(ctx, tenant)
	if err != nil {
		return nil, err
	}
	npo := map[string]bool{}
	for u, c := range cs {
		if contains(c.CSC, npoCSC) {
			npo[u] = true
		}
	}
	var out []ntrLine
	if len(npo) == 0 {
		return out, nil
	}
	rs, _ := docs.List[Receipt](ctx, o.Docs, tenant, cReceipt, "")
	for _, r := range rs {
		if npo[r.UCC] && strings.HasPrefix(r.Date, month) && r.Amount > ntrThreshold && (r.Status == "Credited" || r.Status == "Uncleared") {
			out = append(out, ntrLine{r.Date, r.UCC, cs[r.UCC].Name, "Received from the client", r.Mode, r.Ref, r.Amount})
		}
	}
	ps, _ := docs.List[PayoutRequest](ctx, o.Docs, tenant, cPayout, "")
	for _, p := range ps {
		if npo[p.UCC] && strings.HasPrefix(p.Date, month) && p.Amount > ntrThreshold && p.Status == "Paid" {
			out = append(out, ntrLine{p.Date, p.UCC, cs[p.UCC].Name, "Paid to the client", "Bank transfer", p.UTR, p.Amount})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date+out[i].UCC < out[j].Date+out[j].UCC })
	return out, nil
}

// runFIUMonthly prepares the month's CTR and NTR check.
func (o *Ops) runFIUMonthly(ctx context.Context, tenant, month, date string) (string, []PeriodicFile, error) {
	ls, err := o.ntrFor(ctx, tenant, month)
	if err != nil {
		return "", nil, err
	}
	ctr := "CTR: none (Ledgerline takes no cash; no NIL report is filed)"
	if len(ls) == 0 {
		return ctr + "; NTR: no transaction of a non-profit client above ₹10 lakh (no NIL report is filed)", nil, nil
	}
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	_ = w.Write([]string{"Date", "UCC", "Name", "Transaction", "Mode", "Reference", "Amount"})
	var names []string
	for _, l := range ls {
		_ = w.Write([]string{l.Date, l.UCC, l.Name, l.Kind, l.Mode, l.Ref, l.Amount.String()})
		names = append(names, fmt.Sprintf("%s %s ₹%s on %s", l.UCC, strings.ToLower(l.Kind), formatINR(l.Amount.Dec()), fmtDay(l.Date)))
	}
	w.Flush()
	f, err := o.keepFile(ctx, tenant, "NTR_worksheet_"+month+".csv", b.Bytes())
	if err != nil {
		return "", nil, err
	}
	due := addMonths(month+"-01", 1)[:8] + "15"
	o.raiseWork(ctx, tenant, "fiu", "ntr:"+month, fmt.Sprintf("NTR for %s: %d transactions of non-profit clients above ₹10 lakh", monthLabel(month), len(ls)),
		"File the non-profit organisation transaction report on FIU-IND's FINGate 2.0 by "+fmtDay(due)+" for what is reportable (PML Rules rule 3(1)(BA)); the worksheet is in Periodic work. Record the FINGate batch ID here when closing. "+siList(names, 10)+".",
		"", RoleCompliance, "periodic", date)
	return fmt.Sprintf("%s; NTR: %d transactions to review and file by %s", ctr, len(ls), fmtDay(due)), []PeriodicFile{f}, nil
}
