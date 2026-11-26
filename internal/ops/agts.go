package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"ledgerline/internal/charges"
	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// ---- Annual Global Transaction Statement ----
//
// NSE/INSP/73976 (the Inspection master circular), item 4.17 and Exhibit
// 30: within 30 days of the financial year's end, every client gets an
// Annual Global Transaction Statement, PAN-wise (UCC-wise when a PAN has
// several UCCs): each security or commodity a line, with the exchange and
// segment, the quantity and value bought and sold, close-outs and
// auctions, and the charges (STT, stamp duty, transaction charges, SEBI
// fees). The investor charter (SEBI master circular for stock brokers,
// Annex) lists the Annual Global Statement among the service standards.
//
// SEBI's master circular for stock brokers, para 23.9, also asks for a
// complete statement of funds and securities signed by the broker: the
// annual statement (DocAnnual) is that statement as on 31 March, with the
// AGTS table, signed and kept on the dispatch register. Ledgerline keeps
// one statement per UCC; a client with several UCCs under one PAN gets one
// per UCC, which Exhibit 30 allows.
//
// Exhibit 30's own layout was not at hand; the columns follow the item's
// list and are kept in its order.

// DocAnnual is the year's signed statement of funds, securities and
// transactions (the Annual Global Statement).
const DocAnnual = "annual_statement"

func init() {
	DocKindNames[DocAnnual] = "Annual global statement of funds, securities and transactions"
}

// AGTSRow is one security (or contract) on one exchange and segment.
type AGTSRow struct {
	Exchange  string    `json:"exchange"`
	Segment   string    `json:"segment"`
	Security  string    `json:"security"`
	BuyQty    int64     `json:"buy_qty"`
	BuyValue  money.Dec `json:"buy_value"`
	SellQty   int64     `json:"sell_qty"`
	SellValue money.Dec `json:"sell_value"`
	// CloseOutQty and CloseOutValue are shortages closed out or bought in
	// the exchange's auction for the client (shortage.go).
	CloseOutQty   int64     `json:"close_out_qty,omitempty"`
	CloseOutValue money.Dec `json:"close_out_value"`
	STT           money.Dec `json:"stt"`
	Stamp         money.Dec `json:"stamp"`
	TxnCharges    money.Dec `json:"txn_charges"`
	SEBIFee       money.Dec `json:"sebi_fee"`
}

// agtsSecurity names a trade's security or contract for the statement.
func agtsSecurity(sym, inst, expiry, strike string) string {
	switch inst {
	case "", "EQ":
		return sym
	case "FUT":
		return strings.TrimSpace(sym + " FUT " + expiry)
	}
	return strings.TrimSpace(fmt.Sprintf("%s %s %s %s", sym, expiry, strike, inst))
}

// agtsForYear builds a client's AGTS rows for from..to from the year's
// exchange trades, charged the same way the bills charged them.
func (o *Ops) agtsForYear(ctx context.Context, tenant, ucc, from, to string) ([]AGTSRow, error) {
	by := map[string]*AGTSRow{}
	row := func(ex, seg, sec string) *AGTSRow {
		k := ex + "|" + seg + "|" + sec
		r := by[k]
		if r == nil {
			r = &AGTSRow{Exchange: ex, Segment: seg, Security: sec}
			by[k] = r
		}
		return r
	}
	for d := from; d <= to; d = addDays(d, 1) {
		ts, err := o.exchangeTrades(ctx, tenant, d)
		if err != nil {
			return nil, err
		}
		for _, t := range ts {
			if t.UCC != ucc {
				continue
			}
			r := row(t.Exchange, t.Segment, agtsSecurity(t.Symbol, t.InstrumentType, t.Expiry, t.Strike))
			if t.Side == "B" {
				r.BuyQty += t.Qty
				r.BuyValue = r.BuyValue.Add(t.Value())
			} else {
				r.SellQty += t.Qty
				r.SellValue = r.SellValue.Add(t.Value())
			}
			b, err := charges.BucketFor(t.Segment, t.InstrumentType, t.Product)
			if err != nil {
				continue
			}
			bd, err := o.App.Schedule.Compute(charges.Leg{Bucket: b, Exchange: t.Exchange, Side: t.Side, Qty: t.Qty, Price: t.Price, Date: t.TradeDate, NoBrokerage: true})
			if err != nil {
				continue
			}
			r.STT = r.STT.Add(bd.STT.Dec())
			r.Stamp = r.Stamp.Add(bd.Stamp.Dec())
			r.TxnCharges = r.TxnCharges.Add(bd.ExchTxn.Dec())
			r.SEBIFee = r.SEBIFee.Add(bd.SEBIFee.Dec())
		}
	}
	// Close-outs and auctions of the year's short deliveries.
	sh, _ := docs.List[Shortage](ctx, o.Docs, tenant, cShortage, "")
	for _, s := range sh {
		if s.Date < from || s.Date > to || s.Status != "Settled" {
			continue
		}
		if s.UCC == ucc {
			r := row(s.Exchange, "CM", s.Symbol)
			r.CloseOutQty += s.Qty
			r.CloseOutValue = r.CloseOutValue.Add(s.Price.MulInt(s.Qty))
		}
		for _, b := range s.Buyers {
			if b.UCC == ucc && b.Short > 0 {
				r := row(s.Exchange, "CM", s.Symbol)
				r.CloseOutQty += b.Short
				r.CloseOutValue = r.CloseOutValue.Add(s.Price.MulInt(b.Short))
			}
		}
	}
	out := make([]AGTSRow, 0, len(by))
	for _, r := range by {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Exchange != b.Exchange {
			return a.Exchange < b.Exchange
		}
		if a.Segment != b.Segment {
			return a.Segment < b.Segment
		}
		return a.Security < b.Security
	})
	return out, nil
}

// agtsSection is the AGTS as a statement section.
func agtsSection(rows []AGTSRow) Section {
	sec := Section{Title: "Annual Global Transaction Statement", Columns: []Column{{"ex", "Exchange", ColText}, {"seg", "Segment", ColText}, {"security", "Security / contract", ColText},
		{"bq", "Bought qty", ColQty}, {"bv", "Bought value", ColMoney}, {"sq", "Sold qty", ColQty}, {"sv", "Sold value", ColMoney}, {"cq", "Close-out / auction qty", ColQty}, {"cv", "Close-out value", ColMoney},
		{"stt", "STT / CTT", ColMoney}, {"stamp", "Stamp duty", ColMoney}, {"txn", "Transaction charges", ColMoney}, {"sebi", "SEBI fees", ColMoney}},
		Empty: "No transactions in the year.",
		Note:  "Each security or contract on each exchange and segment, from our records of your trades. Transaction charges and SEBI fees are as charged on your contract notes; brokerage and GST are in the charges summary."}
	var bv, sv, cv, stt, st, tx, sf money.Dec
	for _, r := range rows {
		sec.Rows = append(sec.Rows, []string{r.Exchange, r.Segment, r.Security, qty(r.BuyQty), d2s(r.BuyValue), qty(r.SellQty), d2s(r.SellValue), qty(r.CloseOutQty), d2s(r.CloseOutValue), d2s(r.STT), d2s(r.Stamp), d2s(r.TxnCharges), d2s(r.SEBIFee)})
		bv, sv, cv = bv.Add(r.BuyValue), sv.Add(r.SellValue), cv.Add(r.CloseOutValue)
		stt, st, tx, sf = stt.Add(r.STT), st.Add(r.Stamp), tx.Add(r.TxnCharges), sf.Add(r.SEBIFee)
	}
	if len(rows) > 0 {
		sec.Totals = []string{"Total", "", "", "", d2s(bv), "", d2s(sv), "", d2s(cv), d2s(stt), d2s(st), d2s(tx), d2s(sf)}
	}
	return sec
}

// AnnualStatement is a client's Annual Global Statement for a financial
// year: funds and securities as on its last day and the AGTS.
func (o *Ops) AnnualStatement(ctx context.Context, tenant, fy, ucc string) (ClientReport, error) {
	from, to, err := fyRange(fy)
	if err != nil {
		return ClientReport{}, err
	}
	rep, err := o.ClientReportFor(ctx, tenant, "funds_securities", ucc, from, to)
	if err != nil {
		return rep, err
	}
	rep.Kind, rep.Title = DocAnnual, "Annual global statement · "+map[bool]string{true: "tax year", false: "financial year"}[newTaxAct(from)]+" "+fy
	rows, err := o.agtsForYear(ctx, tenant, rep.UCC, from, to)
	if err != nil {
		return rep, err
	}
	// The year's funds movement is in the monthly statements of account;
	// this statement keeps the balances, the open items, the securities
	// and the year's transactions.
	var keep []Section
	for _, s := range rep.Sections {
		if s.Title != "Funds movement" {
			keep = append(keep, s)
		}
	}
	rep.Sections = append(keep, agtsSection(rows))
	rep.Notes = append(rep.Notes, "This is your statement of funds and securities as on "+fmtDay(to)+" and of your transactions in the year, signed by us. Please report any discrepancy within 30 days.")
	return rep, nil
}

// sendAnnualStatement signs and dispatches the year's statement.
func (o *Ops) sendAnnualStatement(ctx context.Context, tenant, actor, fy, ucc string) (Dispatch, error) {
	rep, err := o.AnnualStatement(ctx, tenant, fy, ucc)
	if err != nil {
		return Dispatch{}, err
	}
	set, _ := o.Settings(ctx, tenant)
	return o.dispatchDoc(ctx, tenant, actor, docRequest{Kind: DocAnnual, Ref: fy, Date: rep.To, UCC: ucc, Report: reportPDF(rep, set), Title: rep.Title,
		FileName: "AnnualStatement_" + rep.UCC + "_" + fy + ".pdf", Text: fmt.Sprintf("Dear %s,\n\nPlease find attached your annual global statement for %s: your funds and securities with us as on %s and your transactions in the year, exchange by exchange. Your year-end tax pack is in the client portal.", rep.Name, fy, fmtDay(rep.To))})
}

// annualDue is the last day for the year's statements: 30 days after the
// year ends.
func annualDue(fy string) string {
	_, to, err := fyRange(fy)
	if err != nil {
		return ""
	}
	return addDays(to, 30)
}

// lastEndedFY is the latest financial year that ended before the date.
func lastEndedFY(date string) string {
	y := date[:4]
	var n int
	fmt.Sscan(y, &n)
	if date[5:] < "04-01" {
		n--
	}
	return fmt.Sprintf("%d-%02d", n-1, n%100)
}

// siAnnualStatement: the last ended year's annual statements reached
// every client who traded in it, within 30 days.
func siAnnualStatement(e *siEnv) (siOut, error) {
	fy := lastEndedFY(e.date)
	from, to, _ := fyRange(fy)
	due := annualDue(fy)
	if e.date <= due {
		return siSkip(fmt.Sprintf("Not due yet: the statements for %s are due by %s.", fy, fmtDay(due)))
	}
	ts, err := siTradesSince(&siEnv{o: e.o, ctx: e.ctx, tenant: e.tenant, date: to, set: e.set}, from)
	if err != nil {
		return siOut{}, err
	}
	traded := map[string]bool{}
	for _, t := range ts {
		traded[t.UCC] = true
	}
	if len(traded) == 0 {
		return siSkip("Not applicable: no trades in Ledgerline in " + fy + ".")
	}
	ds, _ := docs.List[Dispatch](e.ctx, e.o.Docs, e.tenant, cDispatch, "")
	sent := map[string]string{}
	for _, d := range ds {
		if d.Kind == DocAnnual && d.Ref == fy && (d.Status == "Sent" || d.Status == "Physical sent" || d.Status == "Physical pending") {
			if at := firstOf(firstOf(d.SentAt, d.CourierAt), d.Created); at != "" && (sent[d.UCC] == "" || at < sent[d.UCC]) {
				sent[d.UCC] = at
			}
		}
	}
	var s siOut
	s.input("Annual statements "+fy, due)
	var late, missing []string
	for _, u := range sortedKeys(traded) {
		s.checked++
		switch at := sent[u]; {
		case at == "":
			missing = append(missing, u)
		case at[:10] > due:
			late = append(late, u)
		}
	}
	if len(missing) > 0 {
		s.found += len(missing) - 1
		s.bad(fmt.Sprintf("%s · %d clients not sent the annual global statement (due %s): %s", fy, len(missing), fmtDay(due), siList(siMaskAll(missing), 8)))
	}
	if len(late) > 0 {
		s.found += len(late) - 1
		s.bad(fmt.Sprintf("%s · %d sent after %s: %s", fy, len(late), fmtDay(due), siList(siMaskAll(late), 8)))
	}
	s.summary = fmt.Sprintf("%d clients who traded in %s checked; %d problems.", s.checked, fy, s.found)
	return s, nil
}
