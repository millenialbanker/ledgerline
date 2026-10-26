package ops

import (
	"context"
	"fmt"
	"strings"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
	"ledgerline/internal/pdf"
)

// ---- documents sent to clients, as PDF ----

func firmLines(set Settings) []string {
	l := []string{set.Firm}
	reg := "SEBI registration " + firstOf(set.SEBIReg, "-")
	var members []string
	for _, k := range sortedKeys(set.MemberCodes) {
		members = append(members, k+" "+set.MemberCodes[k])
	}
	if len(members) > 0 {
		reg += " · Member codes " + strings.Join(members, ", ")
	}
	l = append(l, reg)
	if set.FirmGSTIN != "" {
		l = append(l, "GSTIN "+set.FirmGSTIN)
	}
	if set.ComplianceEmail != "" {
		l = append(l, "Compliance officer: "+set.ComplianceEmail)
	}
	return l
}

const pdfFooter = "This document is issued electronically and is digitally signed. Check the signature in your PDF reader. If you did not expect it, write to the compliance officer."

func colRight(t string) bool { return t != ColText && t != ColDate && t != "" }

// reportPDF lays out a client report or statement.
func reportPDF(r ClientReport, set Settings) pdf.Report {
	out := pdf.Report{Firm: firmLines(set), Title: r.Title, Footer: pdfFooter}
	out.Meta = []string{r.Period, "Generated " + r.Generated}
	party := r.Name
	if r.UCC != "" && r.UCC != "ALL" {
		party += " (" + r.UCC + ")"
	}
	out.Party = []string{party}
	if r.PAN != "" {
		out.Party = append(out.Party, "PAN "+r.PAN)
	}
	if r.Address != "" {
		out.Party = append(out.Party, r.Address)
	}
	for _, s := range r.Summary {
		out.Summary = append(out.Summary, [2]string{s.Label, s.Value})
	}
	wide := false
	for _, sec := range r.Sections {
		t := pdf.Table{Title: sec.Title, Rows: sec.Rows, Totals: sec.Totals, Empty: sec.Empty, Note: sec.Note}
		for _, c := range sec.Columns {
			w := 1.0
			if c.Type == ColText && (strings.Contains(strings.ToLower(c.Label), "particular") || strings.Contains(strings.ToLower(c.Label), "security") || strings.Contains(strings.ToLower(c.Label), "client") || strings.Contains(strings.ToLower(c.Label), "contract")) {
				w = 2.4
			}
			t.Cols = append(t.Cols, pdf.Col{Label: c.Label, Right: colRight(c.Type), Weight: w})
		}
		if len(sec.Columns) > 8 {
			wide = true
		}
		out.Sections = append(out.Sections, t)
	}
	out.Landscape = wide
	out.Notes = r.Notes
	return out
}

// notePDF lays out a contract note.
func (o *Ops) notePDF(ctx context.Context, tenant, date, ucc, part string) (pdf.Report, noteView, error) {
	v, err := o.noteView(ctx, tenant, date, ucc, part)
	if err != nil {
		return pdf.Report{}, v, err
	}
	n, c, set := v.Note, v.Client, v.Set
	r := pdf.Report{Landscape: true, Firm: firmLines(set), Title: "Contract note", Footer: pdfFooter}
	r.Meta = []string{"No. " + n.No, "Trade date " + n.Date, "Settlement " + set.NextSettlementDay(date)}
	r.Party = []string{c.Name + " (UCC " + c.UCC + ")", "PAN " + MaskPAN(c.PAN) + " · " + c.City, c.Email}
	if c.Demat.ID != "" {
		r.Party = append(r.Party, c.Demat.Depository+" "+c.Demat.ID)
	}
	if n.Revision > 0 {
		r.Banner = fmt.Sprintf("REVISED CONTRACT NOTE (revision %d). It replaces contract note %s for this trade date, which stands cancelled. Reason: %s", n.Revision, n.Replaces, n.Reason)
	}
	if o.App.Schedule.Sample {
		r.Banner = strings.TrimSpace(r.Banner + " Charges use the sample rate schedule " + o.App.Schedule.Version + ". Not valid for real clients.")
	}
	dir := "receivable by"
	if v.Net.Sign() < 0 {
		dir = "payable by"
	}
	r.Summary = [][2]string{{"Net amount " + dir + " client", "₹" + formatINR(v.Net.Abs())}, {"Trades", fmt.Sprint(len(v.Lines))}, {"Contract note", n.No}, {"Trade date", n.Date}}
	t := pdf.Table{Title: "Trades", Cols: []pdf.Col{{Label: "Order no.", Weight: 1.3}, {Label: "Trade no.", Weight: 1.1}, {Label: "Time", Weight: 0.8}, {Label: "Exchange"}, {Label: "Security / contract", Weight: 2.6}, {Label: "B/S", Weight: 0.5}, {Label: "Qty", Right: true, Weight: 0.8}, {Label: "Price", Right: true}, {Label: "Brokerage", Right: true}, {Label: "Net total", Right: true, Weight: 1.2}}}
	for _, l := range v.Lines {
		t.Rows = append(t.Rows, []string{l.OrderNo, l.TradeNo, l.Time, l.Seg, l.Name, l.Side, l.Qty, l.Price, formatINR(l.Brok.Dec()), formatINR(l.Net)})
	}
	g := pdf.Table{Title: "Obligation and charges by segment", Cols: []pdf.Col{{Label: "Segment"}, {Label: "Pay-in / pay-out", Right: true, Weight: 1.3}, {Label: "Brokerage", Right: true}, {Label: "Exchange charges", Right: true}, {Label: "STT / CTT", Right: true}, {Label: "SEBI fee", Right: true}, {Label: "Stamp duty", Right: true}, {Label: "GST", Right: true}, {Label: "Net", Right: true, Weight: 1.2}}}
	for _, x := range v.Groups {
		g.Rows = append(g.Rows, []string{x.Exchange + "-" + x.Segment, formatINR(x.TradeNet.Dec()), formatINR(x.Charges.Brokerage.Dec()), formatINR(x.Charges.ExchTxn.Dec()), formatINR(x.Charges.STT.Dec()), formatINR(x.Charges.SEBIFee.Dec()), formatINR(x.Charges.Stamp.Dec()), formatINR(x.Charges.GST.Dec()), formatINR(x.Net.Dec())})
	}
	g.Totals = []string{"Net " + dir + " client", "", "", "", "", "", "", "", formatINR(v.Net.Abs())}
	r.Sections = []pdf.Table{t, g}
	r.Notes = []string{"Positive amounts are payable to the client; negative amounts are payable by the client. Futures are marked to market at the day's settlement price.",
		"This contract note is issued subject to the rules, bye-laws and regulations of the exchanges and SEBI, and to the arbitration provisions of the exchanges."}
	return r, v, nil
}

// MarginStatement is a client's daily margin statement: margin required
// (peak and end of day) against what was collected, and any shortfall.
func (o *Ops) MarginStatement(ctx context.Context, tenant, date, ucc string) (ClientReport, error) {
	rep, err := o.Margins(ctx, tenant, date)
	if err != nil {
		return ClientReport{}, err
	}
	for _, r := range rep.Rows {
		if r.UCC == strings.ToUpper(ucc) {
			return o.marginStatement(ctx, tenant, date, r)
		}
	}
	return ClientReport{}, fmt.Errorf("%w: no margin for %s on %s", ErrNotFound, ucc, date)
}

func (o *Ops) marginStatement(ctx context.Context, tenant, date string, r MarginRow) (ClientReport, error) {
	c, err := o.Client(ctx, tenant, r.UCC)
	if err != nil {
		return ClientReport{}, err
	}
	rep := ClientReport{Kind: "margin", Title: "Daily margin statement", UCC: c.UCC, Name: c.Name, PAN: MaskPAN(c.PAN), To: date, Period: "Trade date " + date, Generated: o.now().Format("02-Jan-2006 15:04 IST")}
	m := func(d money.Dec) string { return "₹" + formatINR(d) }
	rep.Summary = []SummaryItem{{Label: "Peak margin required", Value: m(r.Peak), Type: ColMoney}, {Label: "End-of-day margin", Value: m(r.EOD), Type: ColMoney},
		{Label: "Margin available", Value: m(r.Collected), Type: ColMoney}, {Label: "Shortfall", Value: m(r.Short), Type: ColMoney}}
	sec := Section{Title: "Margin", Columns: []Column{{"item", "Item", ColText}, {"amount", "Amount", ColMoney}, {"note", "Note", ColText}}}
	sec.Rows = [][]string{
		{"Peak margin required", formatINR(r.Peak), firstOf(r.PeakAt, "end of day")},
		{"End-of-day margin required", formatINR(r.EOD), ""},
		{"Funds (credit balance and UPI blocks)", formatINR(r.Funds), "credit before the day's bills"},
		{"Collateral (pledged securities after haircut)", formatINR(r.Collateral), "counted up to the funds, as the exchanges allow"},
		{"Margin available", formatINR(r.Collected), ""},
		{"Shortfall at peak", formatINR(r.Short), firstOf(r.ShortAt, "-")},
	}
	if r.Penalty.Sign() > 0 {
		set, _ := o.Settings(ctx, tenant)
		note := "levied by the exchange on the firm"
		if set.PenaltyPassThrough {
			note = "charged to your account"
		}
		sec.Rows = append(sec.Rows, []string{"Short-collection penalty (" + r.SlabPct.String() + "%)", formatINR(r.Penalty), note})
	}
	rep.Sections = []Section{sec}
	rep.Notes = []string{"Margins are computed at the exchanges' snapshot times; the peak is the highest of them. Please keep enough funds or collateral to cover the peak margin of your positions."}
	return rep, nil
}

// RASStatement is the statement sent with a running-account settlement:
// funds held, retained against obligations and margin, and paid out.
func (o *Ops) RASStatement(ctx context.Context, tenant, settleDate, ucc string) (ClientReport, error) {
	bs, err := o.RASBatches(ctx, tenant, settleDate)
	if err != nil {
		return ClientReport{}, err
	}
	for _, b := range bs {
		for _, it := range b.Items {
			if it.UCC == strings.ToUpper(ucc) {
				return o.rasStatement(ctx, tenant, b, it)
			}
		}
	}
	return ClientReport{}, fmt.Errorf("%w: %s was not settled on %s", ErrNotFound, ucc, settleDate)
}

func (o *Ops) rasStatement(ctx context.Context, tenant string, b RASBatch, it RASItem) (ClientReport, error) {
	c, err := o.Client(ctx, tenant, it.UCC)
	if err != nil {
		return ClientReport{}, err
	}
	rep := ClientReport{Kind: "ras", Title: "Statement of running account settlement", UCC: c.UCC, Name: c.Name, PAN: MaskPAN(c.PAN), To: b.Date, Period: "Settled on " + b.Date + " (balance as on " + b.AsOf + ")", Generated: o.now().Format("02-Jan-2006 15:04 IST")}
	paid := it.Amount.Dec()
	credit := it.Credit
	if credit.Sign() == 0 {
		credit = paid
	}
	retained := it.Retained
	if retained.Sign() < 0 {
		retained = money.Dec{}
	}
	rep.Summary = []SummaryItem{{Label: "Funds held", Value: "₹" + formatINR(credit), Type: ColMoney}, {Label: "Retained", Value: "₹" + formatINR(retained), Type: ColMoney}, {Label: "Paid to your bank", Value: "₹" + formatINR(paid), Type: ColMoney}, {Label: "Preference", Value: firstOf(c.RAS, "Quarterly"), Type: ColText}}
	rep.Sections = []Section{{Title: "Settlement", Columns: []Column{{"item", "Item", ColText}, {"amount", "Amount", ColMoney}}, Rows: [][]string{
		{"Credit balance as on " + b.AsOf, formatINR(credit)},
		{"Retained for obligations and margin (as allowed by SEBI)", formatINR(retained)},
		{"Paid to " + it.Bank + " A/c " + MaskAccount(it.Account) + " (" + it.IFSC + ")", formatINR(paid)},
	}}}
	rep.Notes = []string{"Under SEBI's running account rules, funds not needed for your obligations and margin are returned to you at each settlement. If you find a difference, tell the firm within 30 days; you can also raise it with the exchange."}
	return rep, nil
}

// DPStatement is a BO account's monthly transaction and holding statement.
func (o *Ops) DPStatement(ctx context.Context, tenant, month, ucc string) (ClientReport, error) {
	c, err := o.Client(ctx, tenant, ucc)
	if err != nil {
		return ClientReport{}, err
	}
	bo, _, err := docs.Get[BOAccount](ctx, o.Docs, tenant, cBO, c.UCC)
	if err != nil {
		return ClientReport{}, fmt.Errorf("%w: %s has no DP account here", ErrNotFound, ucc)
	}
	from := month + "-01"
	to := addDays(addMonths(from, 1), -1)
	rep := ClientReport{Kind: "dp", Title: "Statement of transactions and holdings (DP)", UCC: c.UCC, Name: c.Name, PAN: MaskPAN(c.PAN), From: from, To: to, Period: from + " to " + to, Generated: o.now().Format("02-Jan-2006 15:04 IST")}
	rep.Summary = []SummaryItem{{Label: "BO ID", Value: bo.BOID, Type: ColText}, {Label: "Depository", Value: bo.Depository, Type: ColText}, {Label: "Status", Value: bo.Status, Type: ColText}}
	tx, _ := o.DPTransactions(ctx, tenant, c.UCC)
	sec := Section{Title: "Transactions", Columns: []Column{{"date", "Date", ColDate}, {"isin", "ISIN", ColText}, {"symbol", "Security", ColText}, {"kind", "Transaction", ColText}, {"ref", "Reference", ColText}, {"qty", "Quantity", ColQty}}, Empty: "No transactions in this period."}
	for _, t := range tx {
		if t.Date >= from && t.Date <= to {
			sec.Rows = append(sec.Rows, []string{t.Date, t.ISIN, t.Symbol, t.Kind, t.Ref, fmt.Sprint(t.Qty)})
		}
	}
	rep.Sections = append(rep.Sections, sec)
	if h, err := o.ClientReportFor(ctx, tenant, "holdings", c.UCC, "", to); err == nil {
		for _, s := range h.Sections {
			s.Title = "Holdings as on " + to
			rep.Sections = append(rep.Sections, s)
		}
		rep.Summary = append(rep.Summary, h.Summary...)
	}
	rep.Notes = []string{"Check this statement against your own records and report any difference to the DP within 30 days."}
	return rep, nil
}
