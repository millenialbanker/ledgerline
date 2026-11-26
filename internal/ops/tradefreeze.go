package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
)

// ---- clients' requests to freeze their online trading ----
//
// NSE's inspection master circular NSE/INSP/73976, Exhibit 33 (the
// framework for voluntary freezing of online access, under SEBI's
// circular on it): the member offers at least two ways (for example a
// dedicated e-mail address, SMS, the app) for a client to ask that online
// trading be blocked, and publishes the policy. On a request the member
// checks it comes from the client, acknowledges it, blocks online trading
// and cancels the pending orders within 15 minutes during trading hours
// (before the next session starts otherwise), sends the client the open
// positions within an hour of the freeze, and logs it all. Trading is
// enabled again only on the client's request, after due diligence.
//
// Ledgerline keeps the request with its times, blocks fresh orders on the
// front end (the Finspot block list and API, as for the RMS kill switch;
// the client can still close positions), and records what staff confirm
// on Finspot: the pending orders cancelled (Finspot's reference, or that
// there were none) and the positions sent. Ledgerline has no order
// cancellation call of Finspot's: the cancellation is done on Finspot and
// its confirmation recorded here.
//
// Trading hours are 09:00 to 15:30 IST on business days, and to 23:55
// for a client with a commodity segment open (MCX's evening session).

const (
	cTradeFreeze = "tradefreeze"
	wTradeFreeze = "tradefreeze"

	freezeSLAMin     = 15 // minutes, in trading hours
	freezePosMin     = 60 // minutes from the freeze to the positions sent
	freezeOpenHM     = "09:00"
	freezeEquityEnd  = "15:30"
	freezeCommodEnd  = "23:55"
	freezeMinModes   = 2
	freezeReenableBy = "Re-enabled"
)

// FreezeModes are the ways a request can come in.
var FreezeModes = []string{"Email", "SMS", "App", "Website", "Phone (recorded)", "In person", "Letter"}

// TradeFreeze is one client's request to freeze online trading.
type TradeFreeze struct {
	ID       string `json:"id"`
	UCC      string `json:"ucc"`
	Name     string `json:"name"`
	Mode     string `json:"mode"`
	From     string `json:"from"` // the address or number it came from (masked)
	Received string `json:"received_at"`
	Due      string `json:"due"` // the freeze is due by (RFC3339)
	// Validated: the request comes from the client's registered e-mail or
	// mobile, or staff checked it (ValidNote says how).
	Validated   string `json:"validated_at,omitempty"`
	ValidNote   string `json:"valid_note,omitempty"`
	Acked       string `json:"acknowledged_at,omitempty"`
	AckRef      string `json:"ack_ref,omitempty"`
	Frozen      string `json:"frozen_at,omitempty"`
	FrozenBy    string `json:"frozen_by,omitempty"`
	Cancelled   string `json:"orders_cancelled_at,omitempty"`
	CancelRef   string `json:"cancel_ref,omitempty"` // Finspot's confirmation, or "No pending orders"
	Positions   string `json:"positions_sent_at,omitempty"`
	PositionRef string `json:"positions_ref,omitempty"`
	Status      string `json:"status"` // Received, Frozen, Rejected, Re-enabled
	Note        string `json:"note,omitempty"`
	// Reenable is the client's request to trade again, with the due
	// diligence done before.
	Reenable *TradeReenable `json:"reenable,omitempty"`
	By       string         `json:"by"`
}

// TradeReenable is how trading was enabled again.
type TradeReenable struct {
	Requested string `json:"requested_at"`
	Mode      string `json:"mode"`
	Diligence string `json:"due_diligence"`
	By        string `json:"by"`
	At        string `json:"at"`
}

// tradingWindow says whether t is in the client's trading hours, and
// when the next session opens.
func tradingWindow(set Settings, c Client, t time.Time) (bool, time.Time) {
	t = t.In(ist)
	day, hm := t.Format("2006-01-02"), t.Format("15:04")
	end := freezeEquityEnd
	for _, s := range c.OpenSegments() {
		ex, seg, _ := strings.Cut(s, "-")
		if PartOf(ex, seg) == PartCommodity {
			end = freezeCommodEnd
		}
	}
	if set.IsBusinessDay(day) && hm >= freezeOpenHM && hm < end {
		return true, t
	}
	next := day
	if !set.IsBusinessDay(day) || hm >= end {
		next = set.NextBusinessDay(day)
	}
	open, _ := time.ParseInLocation("2006-01-02 15:04", next+" "+freezeOpenHM, ist)
	return false, open
}

// freezeDue is when the freeze is due for a request received at t.
func freezeDue(set Settings, c Client, t time.Time) time.Time {
	in, open := tradingWindow(set, c, t)
	if in {
		return t.Add(freezeSLAMin * time.Minute)
	}
	return open
}

// TradeFreezeIntake is a request as staff record it.
type TradeFreezeIntake struct {
	UCC      string `json:"ucc"`
	Mode     string `json:"mode"`
	From     string `json:"from"`
	Received string `json:"received_at"` // RFC3339 or "YYYY-MM-DD HH:MM" (IST); empty: now
	Note     string `json:"note"`
}

// ReceiveTradeFreeze records a client's request to freeze online trading.
// A request from the client's registered e-mail or mobile is validated at
// once, and frozen at once; another is validated by staff first.
func (o *Ops) ReceiveTradeFreeze(ctx context.Context, tenant, actor string, in TradeFreezeIntake) (TradeFreeze, error) {
	if err := o.require(ctx, tenant, actor, "Clients' trading freeze requests", RoleMaker, RoleChecker, RoleRisk, RoleBranch); err != nil {
		return TradeFreeze{}, err
	}
	c, err := o.Client(ctx, tenant, in.UCC)
	if err != nil {
		return TradeFreeze{}, invalid("client %s not found", in.UCC)
	}
	if !contains(FreezeModes, in.Mode) {
		return TradeFreeze{}, invalid("the request came by one of %s", strings.Join(FreezeModes, ", "))
	}
	now := o.now()
	at := now
	if s := strings.TrimSpace(in.Received); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t, err = time.ParseInLocation("2006-01-02 15:04", s, ist)
		}
		if err != nil || t.After(now) {
			return TradeFreeze{}, invalid("give when the request was received (YYYY-MM-DD HH:MM, not in the future)")
		}
		at = t
	}
	set, _ := o.Settings(ctx, tenant)
	from := strings.TrimSpace(in.From)
	f := TradeFreeze{UCC: c.UCC, Name: c.Name, Mode: in.Mode, Received: at.Format(time.RFC3339), Due: freezeDue(set, c, at).Format(time.RFC3339), Status: "Received", Note: strings.TrimSpace(in.Note), By: actor}
	switch {
	case from != "" && c.Email != "" && strings.EqualFold(from, c.Email):
		f.Validated, f.ValidNote = now.Format(time.RFC3339), "from the registered e-mail"
	case from != "" && c.Mobile != "" && digitsOnly(from) == digitsOnly(c.Mobile):
		f.Validated, f.ValidNote = now.Format(time.RFC3339), "from the registered mobile"
	}
	if strings.Contains(from, "@") {
		f.From = maskEmail(from)
	} else if from != "" {
		f.From = maskMobile(from)
	}
	n, err := o.next(ctx, tenant, "tradefreeze", actor)
	if err != nil {
		return f, err
	}
	f.ID = fmt.Sprintf("TF-%05d", n)
	if _, err := docs.Put(ctx, o.Docs, tenant, cTradeFreeze, f.ID, f, 0, actor); err != nil {
		return f, err
	}
	o.log(ctx, tenant, actor, "tradefreeze.received", f.ID, map[string]any{"ucc": c.UCC, "mode": f.Mode, "received": f.Received, "due": f.Due, "validated": f.Validated != ""})
	if f.Validated != "" {
		return o.applyTradeFreeze(ctx, tenant, actor, f.ID)
	}
	o.tradeFreezeWork(ctx, tenant, f)
	return f, nil
}

// applyTradeFreeze blocks the client's online trading for the request.
func (o *Ops) applyTradeFreeze(ctx context.Context, tenant, actor, id string) (TradeFreeze, error) {
	f, _, err := docs.Get[TradeFreeze](ctx, o.Docs, tenant, cTradeFreeze, id)
	if err != nil {
		return f, err
	}
	if _, err := docs.Update(ctx, o.Docs, tenant, cClient, f.UCC, actor, func(x *Client) error {
		x.TradeFrozen = f.ID
		return nil
	}); err != nil {
		return f, err
	}
	o.frontEndLimitChanged(ctx, tenant, f.UCC, "trading frozen at the client's request "+f.ID)
	now := o.now().Format(time.RFC3339)
	f, err = docs.Update(ctx, o.Docs, tenant, cTradeFreeze, id, actor, func(x *TradeFreeze) error {
		x.Frozen, x.FrozenBy, x.Status = now, actor, "Frozen"
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "tradefreeze.frozen", id, map[string]any{"ucc": f.UCC, "late": f.Frozen > f.Due})
		o.tradeFreezeWork(ctx, tenant, f)
	}
	return f, err
}

// ActOnTradeFreeze moves a request on: validate (staff checked it is the
// client's; then it is frozen), reject (not the client's), ack (the
// acknowledgement sent), cancelled (Finspot's confirmation that the
// pending orders were cancelled, or that there were none), positions (the
// open positions sent).
func (o *Ops) ActOnTradeFreeze(ctx context.Context, tenant, actor, id, step, ref string) (TradeFreeze, error) {
	if err := o.require(ctx, tenant, actor, "Clients' trading freeze requests", RoleMaker, RoleChecker, RoleRisk, RoleBranch); err != nil {
		return TradeFreeze{}, err
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return TradeFreeze{}, invalid("give the reference (how it was checked, the message sent, Finspot's confirmation)")
	}
	now := o.now().Format(time.RFC3339)
	f, err := docs.Update(ctx, o.Docs, tenant, cTradeFreeze, id, actor, func(x *TradeFreeze) error {
		switch step {
		case "validate", "reject":
			if x.Status != "Received" || x.Validated != "" {
				return fmt.Errorf("%w: %s is %s", ErrState, id, strings.ToLower(x.Status))
			}
			if step == "reject" {
				x.Status, x.Note = "Rejected", strings.TrimSpace(x.Note+" Not the client's: "+ref)
				return nil
			}
			x.Validated, x.ValidNote = now, ref
		case "ack":
			x.Acked, x.AckRef = now, ref
		case "cancelled":
			if x.Frozen == "" {
				return fmt.Errorf("%w: freeze the trading first", ErrState)
			}
			x.Cancelled, x.CancelRef = now, ref
		case "positions":
			if x.Frozen == "" {
				return fmt.Errorf("%w: freeze the trading first", ErrState)
			}
			x.Positions, x.PositionRef = now, ref
		default:
			return invalid("step must be validate, reject, ack, cancelled or positions")
		}
		return nil
	})
	if err != nil {
		return f, err
	}
	o.log(ctx, tenant, actor, "tradefreeze."+step, id, map[string]any{"ref": ref})
	if step == "validate" {
		return o.applyTradeFreeze(ctx, tenant, actor, id)
	}
	o.tradeFreezeWork(ctx, tenant, f)
	return f, nil
}

// ReenableTrading lifts a freeze on the client's request, after due
// diligence (Risk manager or Ops checker).
func (o *Ops) ReenableTrading(ctx context.Context, tenant, actor, id string, r TradeReenable) (TradeFreeze, error) {
	if err := o.require(ctx, tenant, actor, "Re-enabling trading after a client's freeze", RoleRisk, RoleChecker); err != nil {
		return TradeFreeze{}, err
	}
	if !contains(FreezeModes, r.Mode) {
		return TradeFreeze{}, invalid("the client's request came by one of %s", strings.Join(FreezeModes, ", "))
	}
	if len(strings.TrimSpace(r.Diligence)) < 10 {
		return TradeFreeze{}, invalid("record the due diligence done: how the request was confirmed to be the client's, and why trading may resume")
	}
	f, _, err := docs.Get[TradeFreeze](ctx, o.Docs, tenant, cTradeFreeze, id)
	if err != nil {
		return f, err
	}
	if f.Status != "Frozen" {
		return f, fmt.Errorf("%w: %s is %s", ErrState, id, strings.ToLower(f.Status))
	}
	now := o.now().Format(time.RFC3339)
	r.Requested = firstOf(strings.TrimSpace(r.Requested), now)
	r.By, r.At, r.Diligence = actor, now, strings.TrimSpace(r.Diligence)
	if _, err := docs.Update(ctx, o.Docs, tenant, cClient, f.UCC, actor, func(x *Client) error {
		if x.TradeFrozen == f.ID {
			x.TradeFrozen = ""
		}
		return nil
	}); err != nil {
		return f, err
	}
	o.frontEndLimitChanged(ctx, tenant, f.UCC, "trading re-enabled after "+f.ID)
	f, err = docs.Update(ctx, o.Docs, tenant, cTradeFreeze, id, actor, func(x *TradeFreeze) error {
		x.Status, x.Reenable = freezeReenableBy, &r
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "tradefreeze.reenabled", id, map[string]any{"ucc": f.UCC, "mode": r.Mode, "due_diligence": r.Diligence})
		for _, k := range []string{"freeze", "cancel", "positions"} {
			o.closeWork(ctx, tenant, actor, wTradeFreeze, k+":"+f.ID, "re-enabled")
		}
	}
	return f, err
}

// TradeFreezes lists the requests, newest first.
func (o *Ops) TradeFreezes(ctx context.Context, tenant, actor string) ([]TradeFreeze, error) {
	if err := o.require(ctx, tenant, actor, "Viewing clients' trading freeze requests", RoleMaker, RoleChecker, RoleRisk, RoleCompliance, RoleBranch); err != nil {
		return nil, err
	}
	fs, err := docs.List[TradeFreeze](ctx, o.Docs, tenant, cTradeFreeze, "")
	sort.SliceStable(fs, func(i, j int) bool { return fs[i].ID > fs[j].ID })
	if fs == nil {
		fs = []TradeFreeze{}
	}
	return fs, err
}

// tradeFreezeWork keeps the request's work items: the freeze while it is
// not done, the order cancellation and the positions once frozen.
func (o *Ops) tradeFreezeWork(ctx context.Context, tenant string, f TradeFreeze) {
	day := isoDay(f.Received)
	hm := func(s string) string {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return s
		}
		return t.In(ist).Format("02-Jan 15:04")
	}
	if f.Status == "Received" {
		what := "Freeze " + f.UCC + "'s online trading"
		if f.Validated == "" {
			what = "Check and freeze " + f.UCC + "'s online trading"
		}
		o.workDue(ctx, tenant, wTradeFreeze, "freeze:"+f.ID, what+" (request "+f.ID+")", fmt.Sprintf("The client asked by %s at %s to freeze online trading. Check it is the client's, acknowledge it and freeze by %s (15 minutes in trading hours, else before the next session).", strings.ToLower(f.Mode), hm(f.Received), hm(f.Due)), f.UCC, RoleRisk, "rms", day, f.Due)
		return
	}
	o.closeWork(ctx, tenant, SystemActor, wTradeFreeze, "freeze:"+f.ID, f.Status)
	if f.Status != "Frozen" {
		return
	}
	if f.Cancelled == "" {
		o.workDue(ctx, tenant, wTradeFreeze, "cancel:"+f.ID, "Cancel "+f.UCC+"'s pending orders on Finspot (request "+f.ID+")", "Trading is frozen: cancel every pending order on Finspot and record Finspot's confirmation (or that there were none).", f.UCC, RoleRisk, "rms", day, f.Due)
	} else {
		o.closeWork(ctx, tenant, SystemActor, wTradeFreeze, "cancel:"+f.ID, f.CancelRef)
	}
	if f.Positions == "" {
		due := f.Frozen
		if t, err := time.Parse(time.RFC3339, f.Frozen); err == nil {
			due = t.Add(freezePosMin * time.Minute).Format(time.RFC3339)
		}
		o.workDue(ctx, tenant, wTradeFreeze, "positions:"+f.ID, "Send "+f.UCC+" the open positions (request "+f.ID+")", "Within an hour of the freeze, send the client the open positions (or that there are none) and record the message.", f.UCC, RoleRisk, "rms", day, due)
	} else {
		o.closeWork(ctx, tenant, SystemActor, wTradeFreeze, "positions:"+f.ID, f.PositionRef)
	}
}

// tradeFreezeTick keeps the open requests' work items (every run).
func (o *Ops) tradeFreezeTick(ctx context.Context, tenant string, now time.Time) {
	fs, _ := docs.List[TradeFreeze](ctx, o.Docs, tenant, cTradeFreeze, "")
	for _, f := range fs {
		if f.Status == "Received" || (f.Status == "Frozen" && (f.Cancelled == "" || f.Positions == "")) {
			o.tradeFreezeWork(ctx, tenant, f)
		}
	}
}

// freezeLate says what of a request was late (empty: nothing).
func freezeLate(f TradeFreeze, now time.Time) []string {
	var out []string
	at := func(s string) time.Time { t, _ := time.Parse(time.RFC3339, s); return t }
	due := at(f.Due)
	switch {
	case f.Status == "Received" && now.After(due):
		out = append(out, "not frozen by "+due.In(ist).Format("02-Jan 15:04"))
	case f.Frozen != "" && at(f.Frozen).After(due):
		out = append(out, "frozen at "+at(f.Frozen).In(ist).Format("02-Jan 15:04")+", due "+due.In(ist).Format("15:04"))
	}
	if f.Frozen != "" {
		if f.Cancelled == "" && now.After(due) {
			out = append(out, "pending orders' cancellation not confirmed")
		}
		pd := at(f.Frozen).Add(freezePosMin * time.Minute)
		switch {
		case f.Positions == "" && now.After(pd):
			out = append(out, "open positions not sent")
		case f.Positions != "" && at(f.Positions).After(pd):
			out = append(out, "open positions sent more than an hour after the freeze")
		}
		if f.Acked == "" && now.After(due) {
			out = append(out, "not acknowledged")
		}
	}
	if f.Reenable != nil && strings.TrimSpace(f.Reenable.Diligence) == "" {
		out = append(out, "re-enabled without due diligence on record")
	}
	return out
}
