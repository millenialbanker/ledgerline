package ops

import (
	"context"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/money"
)

// Synthetic pay-out day: trades on Thursday 15 Oct, funds pay-out on
// Friday 16 Oct. Broker funds ₹50 L in CM and ₹12 L in F&O.
const phT, phP = "2026-10-15", "2026-10-16"

func phTrade(ucc, side, no string, qty int64, px string) ingest.TradeEvent {
	return ingest.TradeEvent{Tenant: T, Origin: ingest.FromExchange, Exchange: "NSE", Segment: "CM", TradeNo: no, OrderNo: "O" + no, UCC: ucc, Symbol: "SYNTHA",
		InstrumentType: "EQ", Side: side, Qty: qty, Price: money.MustDec(px), TradeDate: phT, TradeTime: "10:00:00", Product: "CNC"}
}

// phSetup: AL1 sold ₹30 L, AL2 sold ₹5 L, AL3 sold and bought ₹10 L each.
// AL1's start-of-day allocation (CM) is confirmed at ₹30 L.
func phSetup(t *testing.T, api bool) (*Ops, context.Context, *fakeNCL, func(string)) {
	t.Helper()
	o, ctx, f := allocSetup(t)
	at := func(hm string) {
		tm, _ := time.ParseInLocation("2006-01-02 15:04", phP+" "+hm, ist)
		o.Now = func() time.Time { return tm }
	}
	at("08:50")
	if !api {
		set, _ := o.Settings(ctx, T)
		set.AllocAPILive, set.AllocAutoPush = false, false
		if err := o.SaveSettings(ctx, T, "admin", set); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := o.App.Trades.Add(ctx, []ingest.TradeEvent{
		phTrade("AL1", "S", "1", 10000, "300"),
		phTrade("AL2", "S", "2", 1000, "500"),
		phTrade("AL3", "S", "3", 1000, "1000"),
		phTrade("AL3", "B", "4", 1000, "1000"),
	}); err != nil {
		t.Fatal(err)
	}
	for u, amt := range map[string]string{"AL1": "3000000", "AL2": "500000", "AL3": "100000"} {
		if err := o.setLevel(ctx, T, "t", AllocLevel{Segment: "CM", UCC: u, Amount: money.MustDec(amt), AsOf: phP + "T08:30:00+05:30", Source: "Accepted · BOD"}, true); err != nil {
			t.Fatal(err)
		}
	}
	ps := DefaultPayoutHoldSettings()
	ps.Enabled = true
	if _, err := o.SavePayoutHoldSettings(ctx, T, "priya.nair", ps); err == nil {
		t.Fatal("a maker changed the pay-out hold rules")
	}
	if _, err := o.SavePayoutHoldSettings(ctx, T, "anita.rao", ps); err != nil {
		t.Fatal(err)
	}
	if _, err := o.SetPayoutFunds(ctx, T, "anita.rao", phP, map[string]string{"CM": "5000000", "FO": "1200000"}, "NMASS screen 08:40"); err != nil {
		t.Fatal(err)
	}
	return o, ctx, f, at
}

func phHold(t *testing.T, o *Ops, ucc string) PayoutHold {
	t.Helper()
	h, _, err := docs.Get[PayoutHold](context.Background(), o.Docs, T, cPayoutHold, payoutHoldID(phP, ucc))
	if err != nil {
		t.Fatalf("no hold for %s", ucc)
	}
	return h
}

func near(d money.Dec, want string, tol string) bool {
	return d.Sub(money.MustDec(want)).Abs().Cmp(money.MustDec(tol)) <= 0
}

// The rule: ₹62 L of broker funds makes the threshold ₹12.4 L. The ₹30 L
// seller is held at 80% of the net sale money (about ₹24 L); the ₹5 L
// seller and the client who sold and bought the same are not held. The
// request is the final level (₹30 L − hold, about ₹6 L, action D); the
// release puts it back to ₹30 L (action U) when NSE Clearing's pay-out
// credit lands, and Allocation() follows the hold and the release.
func TestPayoutHoldAutomatic(t *testing.T) {
	o, ctx, f, at := phSetup(t, true)
	if _, err := o.App.RunBilling(ctx, T, "priya.nair", phT, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := o.ApplyPayoutHolds(ctx, T, "priya.nair", phP); err == nil {
		t.Fatal("a maker applied pay-out holds")
	}
	v, err := o.ApplyPayoutHolds(ctx, T, "anita.rao", phP)
	if err != nil {
		t.Fatal(err)
	}
	if v.Funds.Total.Cmp(money.MustDec("6200000")) != 0 || v.Threshold.Cmp(money.MustDec("1240000")) != 0 || !strings.HasPrefix(v.Funds.Segments[0].Source, "Entered by anita.rao") {
		t.Fatalf("funds %s threshold %s %+v", v.Funds.Total, v.Threshold, v.Funds.Segments)
	}
	if len(v.Holds) != 1 || v.Holds[0].UCC != "AL1" {
		t.Fatalf("holds %+v", v.Holds)
	}
	h := phHold(t, o, "AL1")
	if !near(h.Awaiting, "3000000", "20000") || h.Awaiting.Cmp(money.MustDec("3000000")) >= 0 || !near(h.Amount, "2400000", "16000") || h.Amount.Cmp(h.Awaiting.Pct(money.DecInt(80)).Round(money.UnitPaisa).Dec()) != 0 {
		t.Fatalf("awaiting %s hold %s", h.Awaiting, h.Amount)
	}
	if h.Status != PHSending || h.MsgID == "" || h.Parts[0].Key != "NSE-CM|"+phT {
		t.Fatalf("hold %+v", h)
	}
	_, rt, recs := f.last(t)
	want := money.MustDec("3000000").Sub(h.Amount)
	if rt != "I" || len(recs) != 1 || recs[0]["action"] != "D" || recs[0]["cliCode"] != "AL1" || money.MustDec(money.DecInt(0).Add(want).Fixed(2)).Cmp(want) != 0 || recs[0]["amt"].(float64) != want.Float64() || !near(want, "600000", "16000") {
		t.Fatalf("request %s %v (want %s)", rt, recs, want)
	}
	// Money in for the client waits for the hold's result.
	o.AllocationMoneyIn(ctx, T, "AL1", money.MustAmount("1000"), "RC-PH")
	n := f.count()
	o.AllocationCheck(ctx, T, SystemActor)
	if h = phHold(t, o, "AL1"); h.Status != PHApplied || level(t, o, "AL1").Amount.Cmp(want) != 0 {
		t.Fatalf("after result %s level %s", h.Status, level(t, o, "AL1").Amount)
	}
	if f.count() != n+1 { // the waiting money-in goes on the held level
		t.Fatalf("money in after hold: %d sends", f.count()-n)
	}
	_, _, recs = f.last(t)
	if recs[0]["amt"].(float64) != want.Add(money.MustDec("1000")).Float64() {
		t.Fatalf("money in on hold %v", recs)
	}
	o.AllocationCheck(ctx, T, SystemActor)

	// Allocation() keeps the hold while it is active.
	base, _ := o.allocationBase(ctx, T, phP)
	al, _ := o.Allocation(ctx, T, phP)
	cash := func(rows []AllocationRow) money.Dec {
		for _, r := range rows {
			if r.UCC == "AL1" && r.Segment == "CM" {
				return r.Cash
			}
		}
		return money.Dec{}
	}
	if cash(base).Sign() <= 0 || cash(al).Cmp(money.Max(cash(base).Sub(h.Amount), money.Dec{})) != 0 {
		t.Fatalf("allocation base %s with hold %s", cash(base), cash(al))
	}
	// Live RMS and the OMS hold file show it too.
	if lv, err := o.LiveRisk(ctx, T, phP, ""); err == nil {
		for _, r := range lv.Rows {
			if r.UCC == "AL1" && (r.PayoutHold == nil || r.PayoutHold.Cmp(h.Amount) != 0) {
				t.Fatalf("live row %+v", r)
			}
		}
	}
	name, b, err := o.PayoutHoldFile(ctx, T, "anita.rao", phP, "oms-hold")
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if err != nil || !strings.HasPrefix(name, "OMS_HOLD_") || len(lines) != 2 || lines[0] != "TradeDate,ClientCode,Segment,Change,NewLimit,BODLimit,Reason" ||
		!strings.HasPrefix(lines[1], phP+",AL1,CM,-"+h.Amount.Fixed(2)+",") {
		t.Fatalf("oms hold file %s %q %v", name, b, err)
	}
	if h.BODLimit != nil {
		if f := strings.Split(lines[1], ","); f[4] != money.Max(h.BODLimit.Sub(h.Amount), money.Dec{}).Fixed(2) {
			t.Fatalf("new limit %q", lines[1])
		}
	}

	// A credit from someone else is not the pay-out; NSE Clearing's is.
	at("13:05")
	if _, err := o.recordCredits(ctx, T, Connection{}, "test bank", []BankCredit{{Ref: "UTR-OTHER", PayerName: "SOME CLIENT", Amount: money.MustAmount("100"), Mode: "NEFT", Time: phP + " 13:00:00"}}); err != nil {
		t.Fatal(err)
	}
	o.payoutHoldTick(ctx, T, o.now())
	if h = phHold(t, o, "AL1"); h.Status != PHApplied {
		t.Fatalf("released on another credit: %s", h.Status)
	}
	if _, err := o.recordCredits(ctx, T, Connection{}, "test bank", []BankCredit{{Ref: "UTR-NCL-1", PayerName: "NSE CLEARING LIMITED", Amount: money.MustAmount("4500000"), Mode: "RTGS", Time: phP + " 13:04:00"}}); err != nil {
		t.Fatal(err)
	}
	o.payoutHoldTick(ctx, T, o.now())
	if h = phHold(t, o, "AL1"); h.Status != PHReleasing {
		t.Fatalf("after pay-out credit %s %s", h.Status, h.Waiting)
	}
	_, rt, recs = f.last(t)
	back := want.Add(money.MustDec("1000")).Add(h.Amount)
	if rt != "I" || recs[0]["action"] != "U" || recs[0]["amt"].(float64) != back.Float64() {
		t.Fatalf("release %v want %s", recs, back)
	}
	o.AllocationCheck(ctx, T, SystemActor)
	if h = phHold(t, o, "AL1"); h.Status != PHReleased || !h.Parts[0].Released || level(t, o, "AL1").Amount.Cmp(money.MustDec("3001000")) != 0 {
		t.Fatalf("released %s level %s", h.Status, level(t, o, "AL1").Amount)
	}
	if al, _ = o.Allocation(ctx, T, phP); cash(al).Cmp(cash(base)) != 0 {
		t.Fatalf("allocation after release %s want %s", cash(al), cash(base))
	}
	_, b, _ = o.PayoutHoldFile(ctx, T, "anita.rao", phP, "oms-release")
	lines = strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[1], phP+",AL1,CM,"+h.Amount.Fixed(2)+",") {
		t.Fatalf("oms release file %q", b)
	}
	if h.BODLimit != nil {
		if f := strings.Split(lines[1], ","); f[4] != h.BODLimit.Fixed(2) {
			t.Fatalf("limit after release %q", lines[1])
		}
	}
}

// Propose mode: the risk team approves or edits each hold; the maker
// cannot. A rejected request does not put the held money back in the
// money-in queue. The manual Pay-out received button releases.
func TestPayoutHoldProposeAndFailure(t *testing.T) {
	o, ctx, f, _ := phSetup(t, true)
	ps := o.PayoutHoldSettings(ctx, T)
	ps.Mode = "propose"
	if _, err := o.SavePayoutHoldSettings(ctx, T, "anita.rao", ps); err != nil {
		t.Fatal(err)
	}
	if _, err := o.ApplyPayoutHolds(ctx, T, "farhan.khan", phP); err != nil {
		t.Fatal(err)
	}
	h := phHold(t, o, "AL1")
	if h.Status != PHProposed || f.count() != 0 {
		t.Fatalf("proposed %s, %d sent", h.Status, f.count())
	}
	if _, err := o.DecidePayoutHold(ctx, T, "farhan.khan", h.ID, true, "", ""); err == nil {
		t.Fatal("a checker approved a hold")
	}
	if _, err := o.DecidePayoutHold(ctx, T, "anita.rao", h.ID, true, "4000000", ""); err == nil {
		t.Fatal("held more than the sale money")
	}
	h, err := o.DecidePayoutHold(ctx, T, "anita.rao", h.ID, true, "2000000", "client is a regular")
	if err != nil || h.Amount.Cmp(money.MustDec("2000000")) != 0 || h.Status != PHSending || h.Parts[0].Hold.Cmp(money.MustDec("2000000")) != 0 {
		t.Fatalf("approved %+v %v", h, err)
	}
	_, _, recs := f.last(t)
	if recs[0]["amt"].(float64) != 1000000 || recs[0]["action"] != "D" {
		t.Fatalf("edited request %v", recs)
	}
	// NSE Clearing rejects it: the hold fails, nothing goes to money in.
	f.codes["AL1"] = "0103"
	o.AllocationCheck(ctx, T, SystemActor)
	if h = phHold(t, o, "AL1"); h.Status != PHFailed || level(t, o, "AL1").Amount.Cmp(money.MustDec("3000000")) != 0 {
		t.Fatalf("after rejection %s level %s", h.Status, level(t, o, "AL1").Amount)
	}
	if p, _, err := docs.Get[AllocPending](ctx, o.Docs, T, cAllocPending, allocKey("CM", "AL1")); err == nil && p.Amount.Sign() != 0 {
		t.Fatalf("held money re-added to pending: %+v", p)
	}
	if _, b, _ := o.PayoutHoldFile(ctx, T, "anita.rao", phP, "oms-hold"); strings.Count(string(b), "\n") != 1 {
		t.Fatalf("failed hold in the OMS file: %q", b)
	}

	// A second day-client: approve AL2 by hand-made proposal after lowering
	// the threshold, then release with the button.
	f.codes["AL1"] = ""
	ps.ThresholdPct = "5"
	if _, err := o.SavePayoutHoldSettings(ctx, T, "anita.rao", ps); err != nil {
		t.Fatal(err)
	}
	if _, err := o.ApplyPayoutHolds(ctx, T, "anita.rao", phP); err != nil {
		t.Fatal(err)
	}
	h2 := phHold(t, o, "AL2")
	if h2.Status != PHProposed {
		t.Fatalf("AL2 %s", h2.Status)
	}
	if _, err := o.DecidePayoutHold(ctx, T, "anita.rao", h2.ID, true, "", ""); err != nil {
		t.Fatal(err)
	}
	o.AllocationCheck(ctx, T, SystemActor)
	if h2 = phHold(t, o, "AL2"); h2.Status != PHApplied {
		t.Fatalf("AL2 %s", h2.Status)
	}
	if _, err := o.PayoutReceived(ctx, T, "priya.nair", phP, nil, ""); err == nil {
		t.Fatal("a maker recorded the pay-out")
	}
	if _, err := o.PayoutReceived(ctx, T, "farhan.khan", phP, nil, "seen on the bank portal"); err != nil {
		t.Fatal(err)
	}
	_, _, recs = f.last(t)
	if recs[0]["action"] != "U" || recs[0]["amt"].(float64) != 500000 {
		t.Fatalf("release %v", recs)
	}
	o.AllocationCheck(ctx, T, SystemActor)
	if h2 = phHold(t, o, "AL2"); h2.Status != PHReleased || h2.ReleasedBy != SystemActor {
		t.Fatalf("AL2 after release %s by %s", h2.Status, h2.ReleasedBy)
	}
}

// With the allocation API off, the hold and release are N-MASS files, then
// Done in N-MASS. The scheduler applies holds once the BOD files are done,
// and at the release time assumes the pay-out with a work item.
func TestPayoutHoldNMASSByHand(t *testing.T) {
	o, ctx, f, at := phSetup(t, false)
	ps := o.PayoutHoldSettings(ctx, T)
	ps.ApplyAt, ps.ReleaseAt = "09:05", "13:30"
	if _, err := o.SavePayoutHoldSettings(ctx, T, "anita.rao", ps); err != nil {
		t.Fatal(err)
	}
	// Not yet: neither the BOD allocation file nor the limit file is made.
	at("08:50")
	o.payoutHoldTick(ctx, T, o.now())
	if v, _ := o.PayoutHoldScreen(ctx, T, phP); v.Day.AppliedAt != "" || v.BODDone {
		t.Fatalf("applied before the BOD files: %+v", v.Day)
	}
	if _, _, err := o.ExchangeFile(ctx, T, "priya.nair", "ncl-allocation", phP, XFileOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.LimitFile(ctx, T, phP); err != nil {
		t.Fatal(err)
	}
	o.payoutHoldTick(ctx, T, o.now())
	v, _ := o.PayoutHoldScreen(ctx, T, phP)
	if !v.BODDone || !strings.HasPrefix(v.Day.Trigger, "BOD files done") || v.ToNMASS != 1 {
		t.Fatalf("after BOD files %+v to N-MASS %d", v.Day, v.ToNMASS)
	}
	h := phHold(t, o, "AL1")
	if h.Status != PHToNMASS || f.count() != 0 {
		t.Fatalf("hold %s, %d sent", h.Status, f.count())
	}
	name, b, err := o.PayoutHoldFile(ctx, T, "anita.rao", phP, "nmass-hold")
	want := money.MustDec("3000000").Sub(h.Amount).Fixed(2)
	if err != nil || !strings.Contains(name, "_ALLOC_16102026.T") || !strings.Contains(string(b), ",AL1,C,"+want+",") || !strings.Contains(string(b), ",D") {
		t.Fatalf("N-MASS hold file %s %q %v", name, b, err)
	}
	if _, err := o.PayoutHoldDoneInNMASS(ctx, T, "priya.nair", phP, nil, ""); err == nil {
		t.Fatal("a maker marked N-MASS done")
	}
	if _, err := o.PayoutHoldDoneInNMASS(ctx, T, "farhan.khan", phP, nil, "uploaded 09:02"); err != nil {
		t.Fatal(err)
	}
	if h = phHold(t, o, "AL1"); h.Status != PHByHand || level(t, o, "AL1").Amount.Fixed(2) != want {
		t.Fatalf("by hand %s level %s", h.Status, level(t, o, "AL1").Amount)
	}
	// No credit by 13:30: assumed paid out, released, and a work item.
	at("13:31")
	o.payoutHoldTick(ctx, T, o.now())
	if h = phHold(t, o, "AL1"); h.Status != PHReleaseNMASS || !h.Parts[0].Due || !strings.HasPrefix(h.Parts[0].DueWhy, "Assumed paid out at 13:30") {
		t.Fatalf("release time %s %+v", h.Status, h.Parts[0])
	}
	w, _, err := docs.Get[WorkItem](ctx, o.Docs, T, cWorkItem, "W-payouthold-nopayout-"+phP)
	if err != nil || w.Status != "Open" || w.Role != RoleRisk {
		t.Fatalf("work item %+v %v", w, err)
	}
	_, b, err = o.PayoutHoldFile(ctx, T, "anita.rao", phP, "nmass-release")
	if err != nil || !strings.Contains(string(b), ",AL1,C,3000000.00,") || !strings.Contains(string(b), ",U") {
		t.Fatalf("N-MASS release file %q %v", b, err)
	}
	if _, err := o.PayoutHoldDoneInNMASS(ctx, T, "anita.rao", phP, nil, ""); err != nil {
		t.Fatal(err)
	}
	if h = phHold(t, o, "AL1"); h.Status != PHReleased || level(t, o, "AL1").Amount.Cmp(money.MustDec("3000000")) != 0 {
		t.Fatalf("released by hand %s level %s", h.Status, level(t, o, "AL1").Amount)
	}
}

// Partial pay-out: a credit naming ICCL releases the BSE settlement's share
// only; with no starting broker funds nothing is held and a work item asks
// for the figure.
func TestPayoutHoldPartialAndNoFunds(t *testing.T) {
	keys := []PayoutPart{{Key: "NSE-CM|" + phT, Clearing: "NCL", SettNo: "2026198"}, {Key: "BSE-CM|" + phT, Clearing: "ICCL"}}
	if got := payoutPartsFor("INDIAN CLEARING CORPORATION LTD", keys); len(got) != 1 || got[0] != "BSE-CM|"+phT {
		t.Fatalf("ICCL credit releases %v", got)
	}
	if got := payoutPartsFor("NSCCL PAYOUT 2026198", keys); len(got) != 1 || got[0] != "NSE-CM|"+phT {
		t.Fatalf("NSCCL credit releases %v", got)
	}
	if got := payoutPartsFor("CLEARING PAYOUT", keys); len(got) != 2 {
		t.Fatalf("unnamed credit releases %v", got)
	}
	o, ctx := newOps(t)
	tm, _ := time.ParseInLocation("2006-01-02 15:04", phP+" 09:00", ist)
	o.Now = func() time.Time { return tm }
	ps := DefaultPayoutHoldSettings()
	ps.Enabled = true
	if _, err := o.SavePayoutHoldSettings(ctx, T, "admin", ps); err != nil {
		t.Fatal(err)
	}
	if _, err := o.ApplyPayoutHolds(ctx, T, "anita.rao", phP); err == nil || !strings.Contains(err.Error(), "no starting broker funds") {
		t.Fatalf("applied without funds: %v", err)
	}
	if w, _, err := docs.Get[WorkItem](ctx, o.Docs, T, cWorkItem, "W-payouthold-funds-"+phP); err != nil || w.Status != "Open" {
		t.Fatalf("funds work item %+v %v", w, err)
	}
	// N-MASS gives the CM figure from the trading member's own row.
	if _, err := o.putCCData(ctx, T, "t", "nmass", "2026-10-15", "FO", "CMMrgnDtlsLevel2_FO_15102026.csv", nil, nil); err != nil {
		t.Fatal(err)
	}
	d := CCData{ID: "nmass|2026-10-15|CM", Kind: "nmass", Date: "2026-10-15", Segment: "CM", File: "CMMrgnDtlsLevel2_CM_15102026.csv",
		Rows: []map[string]string{{"TM/CP CODE": "90311", "CASH(A)": "5500000", "NON CASH(B)": "0", "TOTAL MARGINS (F=C+D+E)": "500000"}, {"CLIENT CODE": "AL1", "CASH(A)": "100"}}}
	if err := docs.Upsert(ctx, o.Docs, T, cCCData, d.ID, d, "t"); err != nil {
		t.Fatal(err)
	}
	fs := o.PayoutBrokerFunds(ctx, T, phP)
	if len(fs.Segments) != 1 || fs.Segments[0].Amount.Cmp(money.MustDec("5000000")) != 0 || !strings.HasPrefix(fs.Segments[0].Source, "N-MASS file") || len(fs.Missing) != 1 || fs.Missing[0] != "FO" {
		t.Fatalf("funds %+v", fs)
	}
}
