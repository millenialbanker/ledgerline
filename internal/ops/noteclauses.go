package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"ledgerline/internal/billing"
	"ledgerline/internal/ingest"
	"ledgerline/internal/money"
)

// ---- what a contract note says besides the trades ----
//
//   - Tax invoice particulars (CGST Act s.31, CGST Rules rule 46): the
//     contract note serves as the tax invoice for the brokerage and
//     charges, so it carries the supplier's GSTIN, a serial number (the
//     note number), the SAC, the recipient's GSTIN when registered, the
//     place of supply with its state, and the tax split as IGST or CGST
//     and SGST.
//   - MCX bye-law on jurisdiction (MCX/LEGAL/241/2026, bye-law 3.2): an
//     MCX contract note states that only the courts at Mumbai have
//     jurisdiction over disputes arising from it.
//   - SEBI master circular for stock brokers, para 36.8.1.9: a client who
//     gave a POA (DDPI) gets a daily consolidated scrip-wise summary of the
//     day's buys and sells with average rates; it is printed with the
//     contract note, which is emailed the same day.

// noteClauses are the notes a contract note prints for its trades.
func noteClauses(set Settings, c Client, no string, groups []billing.Group) []string {
	var gst money.Amount
	mcx := false
	for _, g := range groups {
		gst += g.Charges.GST
		mcx = mcx || g.Exchange == "MCX"
	}
	pos := strings.TrimSpace(c.State)
	if pos == "" {
		pos = set.FirmState
	}
	inter := set.FirmState != "" && pos != "" && !strings.EqualFold(pos, set.FirmState)
	i, cg, sg := splitGST(gst, inter)
	tax := fmt.Sprintf("CGST ₹%s and SGST ₹%s (9%% each)", formatINR(cg.Dec()), formatINR(sg.Dec()))
	if inter {
		tax = fmt.Sprintf("IGST ₹%s (18%%)", formatINR(i.Dec()))
	}
	recip := "unregistered"
	if c.GSTIN != "" {
		recip = c.GSTIN
	}
	out := []string{fmt.Sprintf("Tax invoice (section 31 of the CGST Act and rule 46): invoice no. %s · supplier GSTIN %s · SAC %s (stock broking services) · recipient GSTIN %s · place of supply %s · GST on brokerage and charges: %s.",
		no, firstOf(set.FirmGSTIN, "not set"), SACBroking, recip, firstOf(pos, "not recorded"), tax)}
	if mcx {
		out = append(out, "Trades on MCX: only the courts at Mumbai have exclusive jurisdiction over any dispute arising from this contract note (MCX bye-laws, jurisdiction).")
	}
	return out
}

// scripAvg is one security's buys and sells on the day, at average rates.
type scripAvg struct {
	Security        string
	BuyQty, SellQty int64
	BuyAvg, SellAvg string
}

// scripSummary averages the day's trades security by security.
func scripSummary(trades []ingest.TradeEvent) []scripAvg {
	type acc struct {
		bq, sq int64
		bv, sv money.Dec
	}
	by := map[string]*acc{}
	for _, t := range trades {
		k := InstrumentName(t.Symbol, t.InstrumentType, t.Expiry, t.Strike) + " (" + t.Exchange + ")"
		a := by[k]
		if a == nil {
			a = &acc{}
			by[k] = a
		}
		if t.Side == "B" {
			a.bq, a.bv = a.bq+t.Qty, a.bv.Add(t.Value())
		} else {
			a.sq, a.sv = a.sq+t.Qty, a.sv.Add(t.Value())
		}
	}
	keys := make([]string, 0, len(by))
	for k := range by {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []scripAvg
	for _, k := range keys {
		a := by[k]
		r := scripAvg{Security: k, BuyQty: a.bq, SellQty: a.sq}
		if a.bq > 0 {
			p, _ := a.bv.Quo(money.DecInt(a.bq))
			r.BuyAvg = p.Fixed(4)
		}
		if a.sq > 0 {
			p, _ := a.sv.Quo(money.DecInt(a.sq))
			r.SellAvg = p.Fixed(4)
		}
		out = append(out, r)
	}
	return out
}

// hasDDPI says whether the client gave the firm a DDPI or POA on any of
// its demat accounts with the firm's DP.
func (o *Ops) hasDDPI(ctx context.Context, tenant, ucc string) bool {
	for _, b := range o.boAccountsOf(ctx, tenant, ucc) {
		if b.DDPI && b.Status != "Closed" {
			return true
		}
	}
	return false
}
