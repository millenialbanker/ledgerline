package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
)

// ---- pending orders cancelled when a client is blocked ----
//
// When a client is blocked (the RMS kill switch, a deceased client, a
// sanctions case), fresh orders stop at the front end (the Finspot block
// list or API, finspot.go and finspotapi.go), but orders already pending
// in the OMS stay until they are cancelled. Finspot's admin API (Noren WC
// Admin, as the document Ledgerline was given lists its calls: AdminLogin,
// SetLimits, holdings, BlockAcct / UnBlockAcct, FreezeAccount /
// DeFreezeAccount, client and basket calls) has no call to list or cancel
// a client's orders, so Ledgerline cannot cancel them itself. Instead,
// each block opens a cancellation item with the trading-freeze due time
// (15 minutes in trading hours, before the next session otherwise): one
// click records Finspot's confirmation (or "No pending orders"), and a
// second person confirms it (maker-checker). Every step is in the audit
// log. Clients' own freeze requests keep their own steps (tradefreeze.go).
// When Finspot gives an order-book and cancel call in its admin API, the
// first step can call it.

const (
	cOrderCancel = "ordercancel" // OX-nnnnn; state|UCC
	wOrderCancel = "ordercancel"

	OCToCancel  = "To cancel"
	OCConfirmed = "Confirmed"
	OCDone      = "Checked"
	OCLifted    = "Block lifted first"
)

// OrderCancel is the cancellation of a blocked client's pending orders.
type OrderCancel struct {
	ID        string `json:"id"`
	UCC       string `json:"ucc"`
	Name      string `json:"name"`
	Why       string `json:"why"`
	Since     string `json:"since"`
	Due       string `json:"due"`
	Status    string `json:"status"`
	Ref       string `json:"ref,omitempty"` // Finspot's confirmation, or "No pending orders"
	Maker     string `json:"maker,omitempty"`
	MadeAt    string `json:"made_at,omitempty"`
	Checker   string `json:"checker,omitempty"`
	CheckedAt string `json:"checked_at,omitempty"`
}

type orderCancelState struct {
	Why  string `json:"why"`
	Case string `json:"case"`
}

type orderCancelTickDoc struct {
	At string `json:"at"`
}

// blockWhy is why a client may not trade ("" when it may), leaving out
// the client's own freeze request, which has its own steps.
func blockWhy(c Client) string {
	switch {
	case c.Status != "Active" || c.DPOnly || c.TradeFrozen != "":
		return ""
	case c.Sanctions != nil:
		return "Sanctions case " + c.Sanctions.Case
	case c.Blocked:
		return firstOf(strings.TrimSpace(c.BlockedReason), "Blocked by the kill switch")
	}
	return ""
}

// orderCancelTick opens an item for each new block (at most every five
// minutes).
func (o *Ops) orderCancelTick(ctx context.Context, tenant string, now time.Time) {
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cOrderCancel, "tick", orderCancelTickDoc{})
	if t, err := time.Parse(time.RFC3339, st.At); err == nil && now.Sub(t) < 5*time.Minute && !now.Before(t) {
		return
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cOrderCancel, "tick", orderCancelTickDoc{At: now.Format(time.RFC3339)}, SystemActor)
	o.orderCancelScan(ctx, tenant, now)
}

func (o *Ops) orderCancelScan(ctx context.Context, tenant string, now time.Time) {
	if !o.b6Need(ctx, tenant, "broker") {
		return
	}
	cs, err := o.Clients(ctx, tenant)
	if err != nil {
		return
	}
	set, _ := o.Settings(ctx, tenant)
	for _, c := range cs {
		why := blockWhy(c)
		key := "state|" + c.UCC
		s, _, _ := docs.GetOr(ctx, o.Docs, tenant, cOrderCancel, key, orderCancelState{})
		if why == "" {
			if s.Case != "" {
				oc, _, err := docs.Get[OrderCancel](ctx, o.Docs, tenant, cOrderCancel, s.Case)
				if err == nil && oc.Status == OCToCancel {
					oc.Status = OCLifted
					_ = docs.Upsert(ctx, o.Docs, tenant, cOrderCancel, oc.ID, oc, SystemActor)
					o.closeWork(ctx, tenant, SystemActor, wOrderCancel, oc.ID, "block lifted")
				}
				_ = docs.Upsert(ctx, o.Docs, tenant, cOrderCancel, key, orderCancelState{}, SystemActor)
			}
			continue
		}
		if s.Why == why && s.Case != "" {
			continue
		}
		n, err := o.next(ctx, tenant, "ordercancel", SystemActor)
		if err != nil {
			return
		}
		oc := OrderCancel{ID: fmt.Sprintf("OX-%05d", n), UCC: c.UCC, Name: c.Name, Why: why, Since: now.Format(time.RFC3339), Due: freezeDue(set, c, now).Format(time.RFC3339), Status: OCToCancel}
		if err := docs.Upsert(ctx, o.Docs, tenant, cOrderCancel, oc.ID, oc, SystemActor); err != nil {
			return
		}
		_ = docs.Upsert(ctx, o.Docs, tenant, cOrderCancel, key, orderCancelState{Why: why, Case: oc.ID}, SystemActor)
		o.workDue(ctx, tenant, wOrderCancel, oc.ID, "Cancel "+c.UCC+"'s pending orders on Finspot",
			"The client is blocked ("+why+"): fresh orders stop at the front end, but pending orders stay until cancelled. Cancel them on Finspot and record its confirmation (or that there were none) on RMS & limits; a second person confirms.",
			c.UCC, RoleRisk, "rms", now.In(ist).Format("2006-01-02"), oc.Due)
		o.log(ctx, tenant, SystemActor, "rms.order_cancel_due", oc.ID, map[string]any{"ucc": c.UCC, "why": why, "due": oc.Due})
	}
}

// ConfirmOrderCancel records that the client's pending orders were
// cancelled on Finspot (the one click: the reference may be "No pending
// orders").
func (o *Ops) ConfirmOrderCancel(ctx context.Context, tenant, actor, id, ref string) (OrderCancel, error) {
	if err := o.require(ctx, tenant, actor, "Cancelling blocked clients' orders", RoleRisk, RoleMaker, RoleChecker); err != nil {
		return OrderCancel{}, err
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return OrderCancel{}, invalid("give Finspot's confirmation of the cancellation, or \"No pending orders\"")
	}
	now := o.now().Format(time.RFC3339)
	oc, err := docs.Update(ctx, o.Docs, tenant, cOrderCancel, id, actor, func(x *OrderCancel) error {
		if x.Status != OCToCancel {
			return fmt.Errorf("%w: %s is %s", ErrState, id, strings.ToLower(x.Status))
		}
		x.Status, x.Ref, x.Maker, x.MadeAt = OCConfirmed, ref, actor, now
		return nil
	})
	if err != nil {
		return oc, err
	}
	o.closeWork(ctx, tenant, actor, wOrderCancel, id, ref)
	o.raiseWork(ctx, tenant, wOrderCancel, id+":check", "Check the cancellation of "+oc.UCC+"'s orders ("+id+")",
		"Look at the client's order book on Finspot: no pending order should be left. Confirm on RMS & limits.", oc.UCC, RoleChecker, "rms", isoDay(now))
	o.log(ctx, tenant, actor, "rms.orders_cancelled", id, map[string]any{"ucc": oc.UCC, "ref": ref, "late": now > oc.Due})
	return oc, nil
}

// CheckOrderCancel: a second person confirms no order is left.
func (o *Ops) CheckOrderCancel(ctx context.Context, tenant, actor, id string) (OrderCancel, error) {
	if err := o.require(ctx, tenant, actor, "Checking blocked clients' order cancellations", RoleChecker, RoleRisk); err != nil {
		return OrderCancel{}, err
	}
	oc, err := docs.Update(ctx, o.Docs, tenant, cOrderCancel, id, actor, func(x *OrderCancel) error {
		if x.Status != OCConfirmed {
			return fmt.Errorf("%w: %s is %s", ErrState, id, strings.ToLower(x.Status))
		}
		if x.Maker == actor {
			return fmt.Errorf("%w: the person who recorded the cancellation does not check it", ErrForbidden)
		}
		x.Status, x.Checker, x.CheckedAt = OCDone, actor, o.now().Format(time.RFC3339)
		return nil
	})
	if err != nil {
		return oc, err
	}
	o.closeWork(ctx, tenant, actor, wOrderCancel, id+":check", "checked")
	o.log(ctx, tenant, actor, "rms.orders_cancel_checked", id, map[string]any{"ucc": oc.UCC})
	return oc, nil
}

// OrderCancels lists the items, open first, newest first.
func (o *Ops) OrderCancels(ctx context.Context, tenant, actor string) ([]OrderCancel, error) {
	if err := o.require(ctx, tenant, actor, "Viewing blocked clients' order cancellations", RoleRisk, RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return nil, err
	}
	o.orderCancelScan(ctx, tenant, o.now())
	all, err := docs.List[OrderCancel](ctx, o.Docs, tenant, cOrderCancel, "OX-")
	open := func(x OrderCancel) bool { return x.Status == OCToCancel || x.Status == OCConfirmed }
	sort.SliceStable(all, func(i, j int) bool {
		if open(all[i]) != open(all[j]) {
			return open(all[i])
		}
		return all[i].ID > all[j].ID
	})
	if all == nil {
		all = []OrderCancel{}
	}
	return all, err
}

func init() {
	siTests = append(siTests, SITest{ID: "order_cancel", Title: "Blocked clients' pending orders cancelled and checked", Area: "Risk", Severity: "medium", Owner: RoleRisk,
		Source:    "NSE/INSP/73976 Exhibit 33 (orders cancelled with the block); SEBI Master Circular for Stock Brokers 2025/90, 50.1.2",
		ToConfirm: "The 15-minute target of a client's own freeze request is used for every block.",
		Checks:    "Blocks of the last 90 days: Finspot's cancellation recorded by the due time, and checked by a second person.",
		Fix:       "Cancel on Finspot and record it on RMS & limits → Blocked clients' orders; have it checked.",
		Screen:    "rms", ScreenName: "Risk → RMS & limits", Real: true, run: siOrderCancel})
}

func siOrderCancel(e *siEnv) (siOut, error) {
	var s siOut
	s.input("Blocked clients' order cancellations", e.date)
	all, err := docs.List[OrderCancel](e.ctx, e.o.Docs, e.tenant, cOrderCancel, "OX-")
	if err != nil {
		return s, err
	}
	now := e.now.Format(time.RFC3339)
	for _, x := range all {
		if isoDay(x.Since) < addDays(e.date, -90) || x.Status == OCLifted {
			continue
		}
		s.checked++
		switch {
		case x.Status == OCToCancel && now > x.Due:
			s.bad(siMask(x.UCC) + ": pending orders' cancellation not recorded (" + x.ID + ")")
		case x.Status != OCToCancel && x.MadeAt > x.Due:
			s.bad(siMask(x.UCC) + ": cancellation recorded after the due time (" + x.ID + ")")
		case x.Status == OCConfirmed && isoDay(x.MadeAt) < e.date:
			s.bad(siMask(x.UCC) + ": cancellation not checked by a second person (" + x.ID + ")")
		}
	}
	return s, nil
}
