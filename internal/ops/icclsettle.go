package ops

import (
	"context"
	"fmt"
	"strings"

	"ledgerline/internal/money"
)

// ---- settlement at ICCL: cut-offs, accounts and internal shortages ----
//
// BSE2-N0008, ICCL cash segment master circular chapter 4 part A (the
// settlement timetable): pay-in from the clearing member's CM settlement
// account (with an ICCL-empanelled clearing bank) and its CM demat pool
// account; the normal pay-in by 10:30 on T+1; the auction pay-in by 08:00,
// its securities instructions by 07:50. The obligations are already netted
// by clearing corporation (settlement.go nettingGroup: trades of exchanges
// cleared at ICCL net together), and the pay-in reminders are raised per
// clearing corporation (rmspayin.go); here each clearing corporation has
// its own cut-offs (RiskLimitConfig.cutoffs), ICCL's are BSECL's, and the
// settlement and pool accounts are checked to exist.
//
// BSE2-N0007, ICCL notice 20250214-69 (direct pay-out; revised SOP points
// i-v): gross securities pay-in of every client's net sale, and the
// internal-shortage valuation amount (settlement price plus 20%) funded in
// the cash settlement account by 12:00 noon on the settlement day. The
// internal shortages are found per clearing corporation (nclshortage.go);
// ICCL's get their own work item naming BSECL, and the pay-in reminder for
// ICCL gives the gross quantity to deliver, client by client.

// payInCutoffs are one clearing corporation's cut-off times.
type payInCutoffs struct{ Funds, Securities, Auction, AuctionInstr string }

// cutoffs are the pay-in cut-offs at a clearing corporation: ICCL's own
// settings, every other one the firm's general ones.
func (c RiskLimitConfig) cutoffs(cc string) payInCutoffs {
	if cc == "ICCL" {
		return payInCutoffs{c.ICCLFundsPayInBy, c.ICCLSecuritiesPayInBy, c.ICCLAuctionPayInBy, c.ICCLAuctionInstrBy}
	}
	return payInCutoffs{c.FundsPayInBy, c.SecuritiesPayInBy, c.AuctionPayInBy, c.AuctionPayInBy}
}

// latestCutoff is the last cut-off of the day across the clearing
// corporations, so the morning reminders run until it.
func (c RiskLimitConfig) latestCutoff() (funds, auction string) {
	funds, auction = max(c.FundsPayInBy, c.SecuritiesPayInBy), c.AuctionPayInBy
	funds = max(funds, c.ICCLFundsPayInBy, c.ICCLSecuritiesPayInBy)
	auction = max(auction, c.ICCLAuctionPayInBy)
	return funds, auction
}

// icclAccounts says what is missing for pay-in at ICCL: an active
// settlement account serving BSE (Settings → Bank accounts) and a demat
// pool account (Settings → Firm demat accounts, type POOL).
func (o *Ops) icclAccounts(ctx context.Context, tenant string, set Settings) []string {
	var out []string
	bank := false
	for _, b := range o.firmBanks(ctx, tenant) {
		if b.Active && b.Kind == BankSettlement && b.serves("BSE") {
			bank = true
		}
	}
	if !bank {
		out = append(out, "No active settlement account for BSE in Bank accounts: ICCL takes the funds pay-in from the CM settlement account with an ICCL-empanelled clearing bank. Add it, serving BSE.")
	}
	pool := false
	for _, t := range set.FirmDemats {
		if normHSType(t) == "POOL" {
			pool = true
		}
	}
	if !pool {
		out = append(out, "No demat pool account tagged in Firm demat accounts: ICCL takes the securities pay-in from the CM pool account. Tag it as POOL.")
	}
	return out
}

// icclPayInNote is what the ICCL pay-in reminder adds: the gross quantity
// to deliver per client (direct pay-out: no netting across clients).
func icclPayInNote(obs []Obligation) string {
	var lines []string
	for _, ob := range obs {
		if ob.Clearing != "ICCL" || ob.NetQty >= 0 || strings.HasPrefix(ob.Status, "Early pay-in") {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s %s ×%d", ob.UCC, firstOf(ob.Symbol, ob.ISIN), -ob.NetQty))
		if len(lines) == 12 {
			lines = append(lines, "…")
			break
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return " ICCL pays out directly to clients, so deliver each client's net sale in full (gross pay-in, not netted against other clients' purchases) from the CM pool account: " + strings.Join(lines, ", ") + "."
}

// raiseICCLInternalShortage raises the noon funding of ICCL's internal
// shortages; it returns the valuation it covered, which the NSE Clearing
// item leaves out.
func (o *Ops) raiseICCLInternalShortage(ctx context.Context, tenant string, v InternalShortageView) money.Dec {
	var est money.Dec
	var what []string
	for _, r := range v.Rows {
		if r.Clearing != "ICCL" {
			continue
		}
		est = est.Add(r.Valuation)
		what = append(what, fmt.Sprintf("%s ×%d", firstOf(r.Symbol, r.ISIN), r.Internal))
	}
	if est.Sign() <= 0 {
		return est
	}
	o.raiseWork(ctx, tenant, "internal_shortage", "fund|ICCL|"+v.PayIn, fmt.Sprintf("Keep ₹%s in the ICCL settlement account by 12:00 for internal shortages", formatINR(est)),
		fmt.Sprintf("Internal shortages of trades of %s cleared at ICCL (%s), Ledgerline's estimate at the settlement price plus 20%%. BSECL debits the valuation amount by 12:00 noon on %s from the cash settlement account and auctions the shares for the buyers (ICCL notice 20250214-69); where no auction is held the shortage is closed out. Fund the settlement account, then settle the buyers on Settlement → Short deliveries.",
			fmtDay(v.TradeDate), strings.Join(what, ", "), fmtDay(v.PayIn)), "", RoleChecker, "settlement", v.PayIn)
	return est
}
