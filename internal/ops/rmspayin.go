package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// ---- pay-in cut-offs and one clearing balance per clearing corporation ----
//
// NSE Clearing NCL/CMPT/73996 (consolidated circular, cash market), items
// 3.2 and 5.1-5.3, as the firm's compliance gap report gives them: on the
// settlement day (T+1) the funds pay-in must be clear in the settlement
// account by 11:00 and the securities instructions within the depository's
// cut-off (10:30); an auction settles the business day after the auction
// by 08:00. One NSE Clearing settlement pay-in covers the trades of every
// exchange it clears (BSE trades under interoperability), so one debit has
// to clear both exchanges' clearing accounts in the books.
//
// The cut-off times are the firm's settings (RMS → Position limits →
// Settings), since they change with the clearing corporation's circulars.

const (
	cPayInRemind = "payinremind" // today: when the reminders last ran
	wPayInCutoff = "payin_cutoff"
)

// raisePayInWork raises a reminder due at the cut-off (YYYY-MM-DD HH:MM).
func (o *Ops) raisePayInWork(ctx context.Context, tenant, key, title, detail, role, date, due string) {
	rc := &runCtx{o: o, ctx: ctx, tenant: tenant, date: date, process: wPayInCutoff, keys: map[string]bool{}}
	rc.queue(key, title, detail, "", role, "settlement", due, nil)
}

// payInCutoffTick raises the day's pay-in reminders from 06:00 IST, every
// half hour until the last cut-off: the funds and securities pay-in of the
// trades of the business day before, and the auction pay-in of the
// auction held the business day before. From 16:00 it raises the next
// morning's auction pay-in when an auction is held today.
func (o *Ops) payInCutoffTick(ctx context.Context, tenant string, now time.Time) {
	now = now.In(ist)
	hm, today := now.Format("15:04"), now.Format("2006-01-02")
	if hm < "06:00" {
		return
	}
	lim := o.RiskLimitConfig(ctx, tenant)
	if lim.PayInRemindersOff {
		return
	}
	set, err := o.Settings(ctx, tenant)
	if err != nil || !set.IsBusinessDay(today) {
		return
	}
	last, _, _ := docs.Get[struct{ At string }](ctx, o.Docs, tenant, cPayInRemind, today)
	if t, err := time.Parse(time.RFC3339, last.At); err == nil && now.Sub(t) < 30*time.Minute {
		return
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cPayInRemind, today, struct{ At string }{now.Format(time.RFC3339)}, SystemActor)
	o.RaisePayInReminders(ctx, tenant, set, lim, today, hm)
}

// RaisePayInReminders raises the reminders due on a settlement day at
// the given time of day; it returns how many it raised.
func (o *Ops) RaisePayInReminders(ctx context.Context, tenant string, set Settings, lim RiskLimitConfig, today, hm string) int {
	n := 0
	// Each clearing corporation has its own cut-offs (ICCL's are BSECL's,
	// icclsettle.go); reminders run until the last of them.
	lastFunds, lastAuction := lim.latestCutoff()
	if hm < lastFunds {
		td := set.PrevBusinessDay(today)
		if set.NextSettlementDay(td) == today {
			n += o.raisePayIn(ctx, tenant, lim, td, today, hm)
		}
	}
	if hm < lastAuction && o.auctionHeld(ctx, tenant, set.PrevBusinessDay(today)) {
		n += o.raiseAuctionPayIn(ctx, tenant, lim, set.PrevBusinessDay(today), today)
	}
	if hm >= "16:00" && o.auctionHeld(ctx, tenant, today) {
		n += o.raiseAuctionPayIn(ctx, tenant, lim, today, set.nextBusinessDay(today))
	}
	return n
}

// raisePayIn raises the funds and securities pay-in reminders of a trade
// date's cash market obligations at each clearing corporation.
func (o *Ops) raisePayIn(ctx context.Context, tenant string, lim RiskLimitConfig, td, payIn, hm string) int {
	st, err := o.Obligations(ctx, tenant, td)
	if err != nil {
		return 0
	}
	type cc struct {
		in, out money.Dec
		sells   int
		isins   map[string]bool
	}
	by := map[string]*cc{}
	for _, ob := range st.Obligations {
		if _, ok := ClearingCorpNames[ob.Clearing]; !ok {
			continue // cleared by another member, whose own cut-offs apply
		}
		c := by[ob.Clearing]
		if c == nil {
			c = &cc{isins: map[string]bool{}}
			by[ob.Clearing] = c
		}
		switch {
		case ob.NetQty > 0:
			c.in = c.in.Add(ob.Funds.Neg())
		case ob.NetQty < 0:
			c.out = c.out.Add(ob.Funds)
			if !strings.HasPrefix(ob.Status, "Early pay-in") {
				c.sells++
				c.isins[ob.ISIN] = true
			}
		}
	}
	n := 0
	for _, k := range sortedKeys(by) {
		c, name, cut := by[k], ClearingCorpNames[k], lim.cutoffs(k)
		extra := ""
		if k == "ICCL" {
			extra = " ICCL takes it from the CM settlement account with its empanelled clearing bank."
		}
		if net := c.in.Sub(c.out); net.Sign() > 0 && hm < cut.Funds {
			o.raisePayInWork(ctx, tenant, "funds|"+k+"|"+payIn, fmt.Sprintf("Funds pay-in to %s: ₹%s clear by %s", name, formatINR(net), cut.Funds),
				fmt.Sprintf("The net cash market pay-in of the trades of %s is ₹%s (purchases ₹%s less sales ₹%s), due in the settlement account by %s on %s. Fund the settlement account and record the transfer on Client money; a late pay-in is a funds shortage at the clearing corporation.",
					fmtDay(td), formatINR(net), formatINR(c.in), formatINR(c.out), cut.Funds, fmtDay(payIn))+extra, RoleChecker, payIn, payIn+" "+cut.Funds)
			n++
		}
		if c.sells > 0 && hm < cut.Securities {
			gross := ""
			if k == "ICCL" {
				gross = icclPayInNote(st.Obligations) // icclsettle.go
			}
			o.raisePayInWork(ctx, tenant, "securities|"+k+"|"+payIn, fmt.Sprintf("Securities pay-in to %s: %d deliveries by %s", name, c.sells, cut.Securities),
				fmt.Sprintf("%d client sales of the trades of %s in %d securities are to be delivered: the pay-in instructions must reach the depository by %s on %s, with the client code, member and ISIN matching the obligation. Run the pay-in on Settlement; a short delivery goes to auction.",
					c.sells, fmtDay(td), len(c.isins), cut.Securities, fmtDay(payIn))+gross, RoleMaker, payIn, payIn+" "+cut.Securities)
			n++
		}
	}
	return n
}

// auctionHeld says whether a clearing corporation auction concerns the
// firm on a date: a short delivery reported for a settlement whose pay-in
// is that day, or NSE Clearing's internal shortage report for the day.
func (o *Ops) auctionHeld(ctx context.Context, tenant, date string) bool {
	if len(o.ccData(ctx, tenant, "intshort", date, "")) > 0 {
		return true
	}
	ss, _ := o.Shortages(ctx, tenant)
	for _, s := range ss {
		if s.PayIn == date {
			return true
		}
	}
	return false
}

func (o *Ops) raiseAuctionPayIn(ctx context.Context, tenant string, lim RiskLimitConfig, auction, payIn string) int {
	if payIn == "" {
		return 0
	}
	if on, _ := o.icclSelf(ctx, tenant); on {
		// ICCL's auction pay-in has its own cut-off and an earlier one for
		// the securities instructions (icclsettle.go).
		cut := lim.cutoffs("ICCL")
		o.raisePayInWork(ctx, tenant, "auction|ICCL|"+payIn, fmt.Sprintf("ICCL auction pay-in by %s on %s", cut.Auction, fmtDay(payIn)),
			fmt.Sprintf("ICCL's auction of %s settles on %s: securities instructions by %s and funds by %s from the CM settlement and pool accounts. See Settlement → Short deliveries.",
				fmtDay(auction), fmtDay(payIn), cut.AuctionInstr, cut.Auction), RoleChecker, payIn, payIn+" "+cut.AuctionInstr)
	}
	o.raisePayInWork(ctx, tenant, "auction|"+payIn, fmt.Sprintf("Auction pay-in by %s on %s", lim.AuctionPayInBy, fmtDay(payIn)),
		fmt.Sprintf("The clearing corporation's auction of %s settles on %s by %s: keep the funds for the auction or close-out in the settlement account, and deliver any securities due, by then. See Settlement → Short deliveries.",
			fmtDay(auction), fmtDay(payIn), lim.AuctionPayInBy), RoleChecker, payIn, payIn+" "+lim.AuctionPayInBy)
	return 1
}

// ---- one pay-in debit across the exchanges a clearing corporation clears ----

// CCSplit is one clearing account's part of a pay-in or pay-out.
type CCSplit struct {
	Account string       `json:"account"`
	Amount  money.Amount `json:"amount"`
}

// ccSplit shares a pay-in or pay-out of a segment across the clearing
// accounts of every exchange whose same segment the same clearing
// corporation clears (CC:NSE-CM and CC:BSE-CM when BSE trades are cleared
// at NSE Clearing), so that one settlement debit or credit clears the
// books: a pay-in goes first to the chosen segment's account and then to
// the others up to what each owes the clearing corporation (its credit
// balance on the transfer's date); a pay-out the same against what the
// clearing corporation owes on each. What is left over stays on the
// chosen segment. It returns nil when there is nothing to split.
func (o *Ops) ccSplit(ctx context.Context, tenant string, t FirmTransfer) []CCSplit {
	if t.Purpose != "payin" && t.Purpose != "payout" {
		return nil
	}
	if o.RiskLimitConfig(ctx, tenant).CCSplitOff {
		return nil
	}
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return nil
	}
	seg, ok := clearingSeg(t.Segment)
	if !ok {
		return nil
	}
	cc := set.ClearedThrough(seg.Key)
	if _, isCC := ClearingCorpNames[cc]; !isCC {
		return nil
	}
	accts := []string{ledger.ClearingAccount(seg.Exchange, seg.Segment)}
	for _, c := range ClearingSegments {
		if c.Key != seg.Key && c.Segment == seg.Segment && set.tradesOn(c.Exchange) && set.ClearedThrough(c.Key) == cc {
			accts = append(accts, ledger.ClearingAccount(c.Exchange, c.Segment))
		}
	}
	if len(accts) < 2 {
		return nil
	}
	bal, err := o.App.Ledger.Balances(ctx, tenant, t.Date)
	if err != nil {
		return nil
	}
	sort.Strings(accts[1:])
	left := t.Amount
	parts := map[string]money.Amount{}
	for _, a := range accts {
		due := -bal[a] // a pay-in clears what the firm owes (a credit balance)
		if t.Purpose == "payout" {
			due = bal[a] // a pay-out what the clearing corporation owes (a debit balance)
		}
		if due <= 0 || left <= 0 {
			continue
		}
		x := min(due, left)
		parts[a] += x
		left -= x
	}
	if left > 0 {
		parts[accts[0]] += left
	}
	if len(parts) < 2 {
		return nil
	}
	var out []CCSplit
	for _, a := range accts {
		if parts[a] > 0 {
			out = append(out, CCSplit{Account: a, Amount: parts[a]})
		}
	}
	return out
}
