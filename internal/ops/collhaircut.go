package ops

import (
	"strings"

	"ledgerline/internal/money"
)

// ---- valuing securities pledged as margin ----
//
// NSE/INSP/66840 (paras 1-4, Annexure A) and the NSE inspection master
// circular NSE/INSP/73976 (Exhibit 17, Q14-15): a client's collateral
// securities are valued, for collecting and reporting margin on day T, at
// the closing price of T-1 less a haircut of at least the VaR margin that
// applies at the beginning of T. For the kinds Annexure A names:
//
//   - Government securities and T-bills: the clearing corporation's
//     haircut, or 10% when it gives none (valued at the T-1 CCIL/NDS
//     closing price, which the CCIL price file gives Ledgerline).
//   - Units of mutual funds not listed on an exchange: the T-1 NAV less
//     10% (the NAV comes from the ISIN price files).
//   - Listed mutual fund units and ETFs, and shares: the haircut, but never
//     less than the day's VaR margin.
//
// The clearing corporation's haircut is the market data's HaircutPct
// (filled from NSE Clearing's VaR/haircut file where the market data has
// none, ccdata.go addRefData). A security with neither a haircut nor a VaR
// known keeps the old reading (haircut nil): Ledgerline has nothing to
// value it by, and a security that is not on the approved list is refused
// when the pledge is asked for (cdslpledge.go), not here.

var (
	hcFloorGsec       = money.DecInt(10) // G-sec / T-bill with no CC haircut
	hcFloorUnlistedMF = money.DecInt(10) // unlisted mutual fund units
	hcFull            = money.DecInt(100)
)

// isGovtSecurity: a G-sec, T-bill or SDL (by ISIN, or CCIL's price file).
func isGovtSecurity(i Instrument) bool {
	return isGovtISIN(i.ISIN) || i.Type == "GSEC" || govtSeries[strings.ToUpper(i.Segment)]
}

// isUnlistedMF: units of a mutual fund (ISIN INF...) that are not traded on
// an exchange's cash market, so priced from the NAV file only.
func isUnlistedMF(i Instrument) bool {
	return strings.HasPrefix(strings.ToUpper(i.ISIN), "INF") && i.Segment != "CM"
}

// pledgeHaircut is the haircut (per cent) for a security pledged as
// margin, with the floors above. It never exceeds 100.
func pledgeHaircut(i Instrument) money.Dec {
	hc := i.HaircutPct
	switch {
	case isGovtSecurity(i):
		if hc.Sign() <= 0 {
			hc = hcFloorGsec
		}
	case isUnlistedMF(i):
		hc = money.Max(hc, hcFloorUnlistedMF)
	default:
		hc = money.Max(hc, i.VaRPct)
	}
	if hc.Sign() < 0 {
		hc = money.Dec{}
	}
	return money.Min(hc, hcFull)
}

// pledgePrice is the price a pledged security is valued at on a date: the
// previous trading day's close (prev: the market data as of T-1), else the
// day's own price when T-1 has none (a security first priced today).
func pledgePrice(cur, prev Lookup, isin string) (money.Dec, bool) {
	if i, ok := prev.ByISIN(isin); ok {
		if i.Close.Sign() > 0 {
			return i.Close, true
		}
		if i.LTP.Sign() > 0 {
			return i.LTP, true
		}
	}
	if i, ok := cur.ByISIN(isin); ok && i.LTP.Sign() > 0 {
		return i.LTP, true
	}
	return money.Dec{}, false
}

// pledgeValueOf is qty of a pledged security at the T-1 close less the
// haircut (the day's haircut, VaR and kind from cur).
func pledgeValueOf(cur, prev Lookup, isin string, qty money.Dec) (money.Dec, bool) {
	i, ok := cur.ByISIN(isin)
	if !ok {
		if i, ok = prev.ByISIN(isin); !ok {
			return money.Dec{}, false
		}
	}
	px, ok := pledgePrice(cur, prev, isin)
	if !ok {
		return money.Dec{}, false
	}
	return px.Mul(qty).Pct(hcFull.Sub(pledgeHaircut(i))), true
}
