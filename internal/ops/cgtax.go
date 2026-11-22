package ops

import (
	"context"
	"fmt"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// ---- capital gains: the rules both registers use ----
//
// One place for how a sale is taxed, used by the clients' capital gains
// statement (clientreports.go) and the firm's own capital gains register
// (firmsecreports.go). Checked 22 November 2026 against the Income-tax
// Act, 1961 as amended by the Finance (No. 2) Act, 2024 and the Finance
// Act, 2025 (incometaxindia.gov.in):
//
//   - Holding period (section 2(42A), from 23 July 2024): listed
//     securities and units of equity-oriented funds are long-term after
//     12 months; everything else after 24 months. Before 23 July 2024
//     unlisted shares took 24 months and other assets (debt fund units)
//     36 months.
//   - Rates on transfers from 23 July 2024: short-term gains on listed
//     equity, equity-oriented fund units and business trust units with STT
//     paid (section 111A) 20% (15% before); long-term gains on them
//     (section 112A) 12.5% on the gain above ₹1.25 lakh in the year (10%
//     above ₹1 lakh before); other long-term gains (section 112) 12.5%
//     without indexation (20% with indexation before). Other short-term
//     gains are taxed at the normal rates.
//   - Grandfathering (section 55(2)(ac), Finance Act, 2018): for listed
//     equity and equity-oriented fund units bought before 1 February 2018
//     and sold long-term, the cost is the higher of the actual cost and
//     the lower of (a) the fair market value on 31 January 2018 (the
//     highest price quoted on a recognised stock exchange that day, the
//     NAV for unlisted units) and (b) the sale value.
//   - Specified mutual funds (section 50AA, Finance Act, 2023, amended by
//     the Finance (No. 2) Act, 2024): units bought on or after 1 April
//     2023 of a fund investing more than 65% in debt and money market
//     instruments give short-term gains whatever the holding period.
//   - Buy-backs from 1 October 2024: the whole amount is a deemed dividend
//     (section 2(22)(f)) and the consideration for capital gains is nil,
//     so the shares' cost is a capital loss (section 46A).
//
// The Income-tax Act, 2025 replaces the 1961 Act from 1 April 2026 and
// renumbers these sections (111A becomes section 196, 112 section 197,
// 112A section 198; taxlabels.go names them by the year); the rates and
// the ₹1.25 lakh exemption carried over.

const (
	cgNewRegime      = "2024-07-23" // Finance (No. 2) Act, 2024
	cgGrandfatherBuy = "2018-02-01" // bought before this: section 55(2)(ac)
	cgGrandfatherFMV = "2018-01-31"
	cg50AAFrom       = "2023-04-01"
	cFMV2018         = "fmv2018"
)

// CG112AExemption is the year's long-term gains under section 112A not
// taxed, by the date of the transfer.
func CG112AExemption(saleDate string) money.Amount {
	if saleDate >= cgNewRegime {
		return money.MustAmount("125000")
	}
	return money.MustAmount("100000")
}

// cgRate is the rate of a section on a transfer date, in words.
func cgRate(section, saleDate string) string {
	newer := saleDate >= cgNewRegime
	switch section {
	case "111A":
		return map[bool]string{true: "20%", false: "15%"}[newer]
	case "112A":
		return map[bool]string{true: "12.5% above ₹1.25 lakh", false: "10% above ₹1 lakh"}[newer]
	case "112":
		return map[bool]string{true: "12.5% without indexation", false: "20% with indexation"}[newer]
	}
	return "normal rates"
}

// cgSectionName names a section for the year of the sale.
func cgSectionName(section, saleDate string) string {
	if !newTaxAct(saleDate) {
		switch section {
		case "111A", "112A", "112", "50AA":
			return "section " + section
		}
		return "normal rates (short-term)"
	}
	switch section {
	case "111A":
		return "section 196 (was 111A)"
	case "112A":
		return "section 198 (was 112A)"
	case "112":
		return "section 197 (was 112)"
	case "50AA":
		return "specified fund units: short-term (was 50AA)"
	}
	return "normal rates (short-term)"
}

// CGLine is one sale worked out for tax.
type CGLine struct {
	Class    string    `json:"class"` // equity, listed, sgb, unlisted, mf-other, mf-debt
	Term     string    `json:"term"`  // ST or LT
	Section  string    `json:"section"`
	Days     int       `json:"days"`
	Cost     money.Dec `json:"cost"`     // actual cost
	FMV      money.Dec `json:"fmv"`      // fair market value on 31 Jan 2018 (all the units)
	HasFMV   bool      `json:"has_fmv"`  // the FMV is known
	TaxCost  money.Dec `json:"tax_cost"` // cost for capital gains
	Proceeds money.Dec `json:"proceeds"`
	Gain     money.Dec `json:"gain"`
	Grand    bool      `json:"grandfathered"` // bought before 1 February 2018 and long-term (equity)
}

// cgWork works out a sale: class is the capital gains class of the
// security (FSTaxClasses; "mf-debt" a specified fund), acq the date it
// was acquired for tax, fmvUnit its fair market value per unit on 31
// January 2018 (zero when not known).
func cgWork(class, acq, sale string, qty, cost, proceeds, fmvUnit money.Dec) CGLine {
	l := CGLine{Class: class, Cost: cost, TaxCost: cost, Proceeds: proceeds}
	if acq == "" {
		l.Term, l.Section, l.Gain = "ST", "slab", proceeds.Sub(cost)
		return l
	}
	l.Days = daysBetween(acq, sale)
	months := 12
	switch class {
	case "unlisted":
		months = 24
	case "mf-other", "mf-debt":
		months = 24
		if sale < cgNewRegime {
			months = 36
		}
	}
	tb, err := time.Parse("2006-01-02", acq)
	long := err == nil && sale > tb.AddDate(0, months, 0).Format("2006-01-02")
	if class == "mf-debt" && acq >= cg50AAFrom {
		long = false
	}
	l.Term = map[bool]string{true: "LT", false: "ST"}[long]
	switch {
	case class == "equity" && long:
		l.Section = "112A"
	case class == "equity":
		l.Section = "111A"
	case class == "mf-debt" && acq >= cg50AAFrom:
		l.Section = "50AA"
	case long:
		l.Section = "112"
	default:
		l.Section = "slab"
	}
	if class == "equity" && long && acq < cgGrandfatherBuy {
		l.Grand = true
		if fmvUnit.Sign() > 0 {
			l.HasFMV = true
			l.FMV = fmvUnit.Mul(qty)
			// Higher of the cost and the lower of the FMV and the sale value.
			low := money.Min(l.FMV, proceeds)
			l.TaxCost = money.Max(cost, low)
		}
	}
	l.Gain = proceeds.Sub(l.TaxCost)
	return l
}

// cgClassOf is the capital gains class of a security from what is known
// of it: the kind (kindOfTrade) and, for fund units, the scheme's name
// and category.
func cgClassOf(kind, name string) string {
	up := strings.ToUpper(name)
	switch kind {
	case "EQ":
		return "equity"
	case "ETF", "MFE", "MFO":
		for _, w := range []string{"GOLD", "SILVER", "LIQUID", "GILT", "DEBT", "BOND", "MONEY MARKET", "OVERNIGHT", "CORPORATE", "BHARAT BOND", "SDL", "TREASURY", "CREDIT", "DURATION", "FLOATER"} {
			if strings.Contains(up, w) {
				if strings.Contains(up, "GOLD") || strings.Contains(up, "SILVER") {
					return "mf-other"
				}
				return "mf-debt"
			}
		}
		if kind == "MFO" {
			return "mf-other"
		}
		return "equity"
	case "BOND", "GSEC":
		return "listed"
	case "SGB":
		return "sgb"
	case "UNL":
		return "unlisted"
	}
	return "unlisted"
}

// ---- fair market values on 31 January 2018 ----

// FMV2018 is a security's fair market value per unit on 31 January 2018.
type FMV2018 struct {
	ISIN string    `json:"isin"`
	FMV  money.Dec `json:"fmv"`
	By   string    `json:"by,omitempty"`
}

// fmv2018 reads the values loaded (FMV-2018 file), and for the rest the
// day's high in the market data of 31 January 2018 if it was loaded.
func (o *Ops) fmv2018(ctx context.Context, tenant string) map[string]money.Dec {
	out := map[string]money.Dec{}
	xs, _ := docs.List[FMV2018](ctx, o.Docs, tenant, cFMV2018, "")
	for _, x := range xs {
		out[x.ISIN] = x.FMV
	}
	if _, look, err := o.Market(ctx, tenant, cgGrandfatherFMV); err == nil {
		for _, i := range look.byISIN {
			if _, ok := out[i.ISIN]; !ok && i.Close.Sign() > 0 {
				// The close stands in for the day's high when only it is known.
				out[i.ISIN] = i.Close
			}
		}
	}
	return out
}

// fmvNote says how the grandfathered cost was taken.
func fmvNote(missing int) string {
	if missing == 0 {
		return ""
	}
	return fmt.Sprintf("%d long-term sales of shares or units bought before 1 February 2018 have no fair market value for 31 January 2018: their actual cost is used. Load the values (FMV-2018 on Other uploads) to apply section 55(2)(ac).", missing)
}
