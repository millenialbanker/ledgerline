package ops

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/billing"
	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/money"
	"ledgerline/internal/xfile"
)

// ---- pay-out hold for big sellers ----
//
// A client who sold on T has the sale money as a ledger credit on T+1, and
// the start-of-day allocation gives it to the client at NSE Clearing. The
// clearing corporation pays the sale money out only around 13:00 on T+1:
// until then that allocation is backed by the broker's own funds, and one
// or two big sellers can tie up most of them.
//
// So, after the start-of-day files are done, a client whose net sale
// proceeds awaiting pay-out are more than a share of the firm's starting
// broker funds (threshold %, 20 by default) has part of them (hold %, 80 by
// default) taken back from the allocation and from the trading front end's
// limit. When the pay-out arrives (a bank credit from the clearing
// corporation, the day's statement, a person, or an "assume paid out"
// time), the allocation and limit are put back.
//
// Allocation changes go through the allocation API code (nclalloc.go) as
// their own kinds: final levels built on the confirmed level, one request
// in flight per client and segment, refs on each record. With the API off,
// they are written as the N-MASS allocation file and marked done by hand.
// The trading front end has no API: hold and release limit files are made.

const (
	cPayoutSet   = "payoutholdset" // "main" -> PayoutHoldSettings
	cPayoutDay   = "payoutday"     // date -> PayoutDay
	cPayoutHold  = "payouthold"    // date_ucc -> PayoutHold
	cPayoutFunds = "payoutfunds"   // date -> PayoutFundsEntry

	AllocPayoutHold    = "Pay-out hold"
	AllocPayoutRelease = "Pay-out release"

	PHProposed      = "Proposed"
	PHApproved      = "Approved" // decided, request not made yet
	PHSending       = "Sending"
	PHToNMASS       = "To do in N-MASS"
	PHApplied       = "Applied"
	PHByHand        = "Done by hand"
	PHFailed        = "Failed"
	PHRejected      = "Rejected"
	PHNotNeeded     = "Not needed"
	PHReleasing     = "Releasing"
	PHReleaseNMASS  = "To release in N-MASS"
	PHReleaseFailed = "Release failed"
	PHPartReleased  = "Partly released"
	PHReleased      = "Released"

	payoutBODLimitFile = "bod-limit-file" // daily file note made by LimitFile
)

// phInEffect: the hold is (or is being) applied at NSE Clearing and in the
// front end's limit.
var phInEffect = map[string]bool{PHSending: true, PHApplied: true, PHByHand: true, PHReleasing: true, PHReleaseNMASS: true, PHReleaseFailed: true, PHPartReleased: true}

// PayoutHoldSettings are the firm's rules (Admin or the risk manager).
type PayoutHoldSettings struct {
	Enabled      bool   `json:"enabled"`
	ThresholdPct string `json:"threshold_pct"` // of starting broker funds; default 20
	HoldPct      string `json:"hold_pct"`      // of net awaiting pay-out; default 80
	// Basis: "net" (sale − purchases − charges, the default) or "gross"
	// (sale − charges).
	Basis    string   `json:"basis"`
	Segments []string `json:"segments"` // whose broker funds count; CM and FO by default
	// Mode: "auto" (holds are sent at once) or "propose" (the risk team
	// approves or edits each hold).
	Mode    string `json:"mode"`
	MinHold string `json:"min_hold"` // ₹; smaller holds are not made
	ApplyAt string `json:"apply_at"` // HH:MM; when not detected from the BOD files (default 08:45)
	// PayoutPattern marks a bank credit as the clearing corporation's funds
	// pay-out (a regular expression on the remitter and narration, case
	// ignored); PayoutAccount (part of an account number on the credit)
	// does too.
	PayoutPattern string `json:"payout_pattern"`
	PayoutAccount string `json:"payout_account"`
	ReleaseAt     string `json:"release_at"` // HH:MM: assume paid out; empty = off
	UpdatedBy     string `json:"updated_by,omitempty"`
	UpdatedAt     string `json:"updated_at,omitempty"`
}

// DefaultPayoutHoldSettings are used until the firm saves its own.
func DefaultPayoutHoldSettings() PayoutHoldSettings {
	return PayoutHoldSettings{ThresholdPct: "20", HoldPct: "80", Basis: "net", Segments: []string{"CM", "FO"}, Mode: "auto", MinHold: "0", ApplyAt: "08:45",
		PayoutPattern: "NSE CLEARING|NSCCL|NATIONAL SECURITIES CLEARING|INDIAN CLEARING|ICCL"}
}

// PayoutFundsSeg is one segment's free broker funds at the start of the day.
type PayoutFundsSeg struct {
	Segment string    `json:"segment"`
	Amount  money.Dec `json:"amount"`
	Source  string    `json:"source"`
	At      string    `json:"at,omitempty"`
}

// PayoutFunds are the starting broker funds and where they came from.
type PayoutFunds struct {
	Total    money.Dec        `json:"total"`
	Segments []PayoutFundsSeg `json:"segments"`
	Missing  []string         `json:"missing,omitempty"` // segments with no figure
}

// PayoutFundsEntry is the figure the risk team typed in for a morning.
type PayoutFundsEntry struct {
	Date     string            `json:"date"`
	Segments map[string]string `json:"segments"`
	Note     string            `json:"note,omitempty"`
	By       string            `json:"by"`
	At       string            `json:"at"`
}

// PayoutDay is where a pay-out day stands.
type PayoutDay struct {
	Date      string      `json:"date"`
	Funds     PayoutFunds `json:"funds"`
	Threshold money.Dec   `json:"threshold"`
	AppliedAt string      `json:"applied_at,omitempty"`
	AppliedBy string      `json:"applied_by,omitempty"`
	Trigger   string      `json:"trigger,omitempty"`
	Credits   []string    `json:"credits,omitempty"`   // bank credits taken as the pay-out
	PaidKeys  []string    `json:"paid_keys,omitempty"` // settlements paid out
	PaidAll   bool        `json:"paid_all,omitempty"`
	Assumed   bool        `json:"assumed,omitempty"` // released at the release time
}

// PayoutPart is one settlement's sale money a hold covers.
type PayoutPart struct {
	Key        string    `json:"key"` // NSE-CM|trade date
	Exchange   string    `json:"exchange"`
	Clearing   string    `json:"clearing"` // NCL, ICCL
	TradeDate  string    `json:"trade_date"`
	SettNo     string    `json:"sett_no,omitempty"`
	PayoutAt   string    `json:"payout_at,omitempty"` // from the settlement master
	Sold       money.Dec `json:"sold"`
	Bought     money.Dec `json:"bought"`
	Charges    money.Dec `json:"charges"`
	Awaiting   money.Dec `json:"awaiting"`
	Hold       money.Dec `json:"hold"`
	Due        bool      `json:"due,omitempty"` // pay-out arrived
	DueWhy     string    `json:"due_why,omitempty"`
	Releasing  bool      `json:"releasing,omitempty"`
	Released   bool      `json:"released,omitempty"`
	ReleasedAt string    `json:"released_at,omitempty"`
}

// PayoutLeg is the hold on one allocation segment.
type PayoutLeg struct {
	Segment       string    `json:"segment"`
	Before        money.Dec `json:"before"`
	Hold          money.Dec `json:"hold"`
	Target        money.Dec `json:"target"`
	Released      money.Dec `json:"released"`
	Releasing     money.Dec `json:"releasing"`
	ReleaseTarget money.Dec `json:"release_target"`
}

// PayoutHold is one client's hold on a pay-out day.
type PayoutHold struct {
	ID           string       `json:"id"` // date_ucc
	Date         string       `json:"date"`
	UCC          string       `json:"ucc"`
	Name         string       `json:"name"`
	Sold         money.Dec    `json:"sold"`
	Bought       money.Dec    `json:"bought"`
	Charges      money.Dec    `json:"charges"`
	Awaiting     money.Dec    `json:"awaiting"`
	Proposed     money.Dec    `json:"proposed"` // by the rule
	Amount       money.Dec    `json:"amount"`   // decided (may be edited)
	Parts        []PayoutPart `json:"parts"`
	Legs         []PayoutLeg  `json:"legs,omitempty"`
	BODLimit     *money.Dec   `json:"bod_limit,omitempty"`
	Status       string       `json:"status"`
	Waiting      string       `json:"waiting,omitempty"` // why a request is not made yet
	MsgID        string       `json:"msg_id,omitempty"`
	ReleaseMsgID string       `json:"release_msg_id,omitempty"`
	Error        string       `json:"error,omitempty"`
	Note         string       `json:"note,omitempty"`
	Created      string       `json:"created"`
	DecidedBy    string       `json:"decided_by,omitempty"`
	AppliedAt    string       `json:"applied_at,omitempty"`
	ReleasedAt   string       `json:"released_at,omitempty"`
	ReleasedBy   string       `json:"released_by,omitempty"`
}

func (h PayoutHold) held() money.Dec {
	var t money.Dec
	for _, l := range h.Legs {
		t = t.Add(l.Hold.Sub(l.Released))
	}
	return t
}

func payoutHoldID(date, ucc string) string { return date + "_" + strings.ToUpper(ucc) }

// ---- settings ----

// PayoutHoldSettings are the firm's pay-out hold rules.
func (o *Ops) PayoutHoldSettings(ctx context.Context, tenant string) PayoutHoldSettings {
	s, _, err := docs.Get[PayoutHoldSettings](ctx, o.Docs, tenant, cPayoutSet, "main")
	if err != nil {
		return DefaultPayoutHoldSettings()
	}
	return s
}

func pctOK(s string, max int64) bool {
	d, err := money.ParseDec(strings.TrimSpace(s))
	return err == nil && d.Sign() > 0 && d.Cmp(money.DecInt(max)) <= 0
}

var rePayoutHHMM = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)

// SavePayoutHoldSettings checks and stores the rules.
func (o *Ops) SavePayoutHoldSettings(ctx context.Context, tenant, actor string, s PayoutHoldSettings) (PayoutHoldSettings, error) {
	if err := o.require(ctx, tenant, actor, "Pay-out hold rules", RoleRisk); err != nil {
		return s, err
	}
	s.ThresholdPct, s.HoldPct, s.MinHold = strings.TrimSpace(s.ThresholdPct), strings.TrimSpace(s.HoldPct), firstOf(strings.TrimSpace(s.MinHold), "0")
	if !pctOK(s.ThresholdPct, 100) {
		return s, invalid("the threshold is a percentage of the starting broker funds, above 0 and at most 100")
	}
	if !pctOK(s.HoldPct, 100) {
		return s, invalid("the hold is a percentage of the sale money awaiting pay-out, above 0 and at most 100")
	}
	if m, err := money.ParseDec(s.MinHold); err != nil || m.Sign() < 0 {
		return s, invalid("the minimum hold is an amount in rupees")
	}
	if s.Basis != "net" && s.Basis != "gross" {
		return s, invalid("the basis is net (sale − purchases − charges) or gross (sale − charges)")
	}
	if s.Mode != "auto" && s.Mode != "propose" {
		return s, invalid("the mode is automatic or propose to the risk team")
	}
	var segs []string
	for _, g := range s.Segments {
		g = strings.ToUpper(strings.TrimSpace(g))
		if g != "CM" && g != "FO" && g != "CD" {
			return s, invalid("broker funds segments are CM, FO and CD")
		}
		if !contains(segs, g) {
			segs = append(segs, g)
		}
	}
	if len(segs) == 0 {
		return s, invalid("choose the segments whose broker funds count")
	}
	s.Segments = segs
	s.ApplyAt = firstOf(strings.TrimSpace(s.ApplyAt), "08:45")
	if !rePayoutHHMM.MatchString(s.ApplyAt) {
		return s, invalid("the apply time is HH:MM")
	}
	s.ReleaseAt = strings.TrimSpace(s.ReleaseAt)
	if s.ReleaseAt != "" && !rePayoutHHMM.MatchString(s.ReleaseAt) {
		return s, invalid("the release time is HH:MM, or empty to wait for the pay-out")
	}
	s.PayoutPattern, s.PayoutAccount = strings.TrimSpace(s.PayoutPattern), strings.TrimSpace(s.PayoutAccount)
	if s.PayoutPattern != "" {
		if _, err := regexp.Compile("(?i)" + s.PayoutPattern); err != nil {
			return s, invalid("the remitter pattern is not a valid pattern: %v", err)
		}
	}
	if s.Enabled && s.PayoutPattern == "" && s.PayoutAccount == "" && s.ReleaseAt == "" {
		return s, invalid("say how the pay-out is recognised: a remitter pattern, the settlement account, or a release time (the Pay-out received button works as well)")
	}
	s.UpdatedBy, s.UpdatedAt = actor, o.now().Format(time.RFC3339)
	if err := docs.Upsert(ctx, o.Docs, tenant, cPayoutSet, "main", s, actor); err != nil {
		return s, err
	}
	o.log(ctx, tenant, actor, "payouthold.settings", "settings", map[string]any{"enabled": s.Enabled, "threshold_pct": s.ThresholdPct, "hold_pct": s.HoldPct, "basis": s.Basis, "mode": s.Mode, "segments": s.Segments, "release_at": s.ReleaseAt})
	return s, nil
}

// ---- starting broker funds ----

// SetPayoutFunds records the free broker funds the risk team read in the
// morning, per segment.
func (o *Ops) SetPayoutFunds(ctx context.Context, tenant, actor, date string, segs map[string]string, note string) (PayoutFundsEntry, error) {
	if err := o.require(ctx, tenant, actor, "Entering broker funds", RoleRisk); err != nil {
		return PayoutFundsEntry{}, err
	}
	if !validDate(date) {
		return PayoutFundsEntry{}, invalid("give the date")
	}
	e := PayoutFundsEntry{Date: date, Segments: map[string]string{}, Note: strings.TrimSpace(note), By: actor, At: o.now().Format(time.RFC3339)}
	for k, v := range segs {
		k = strings.ToUpper(strings.TrimSpace(k))
		if strings.TrimSpace(v) == "" {
			continue
		}
		d, err := money.ParseDec(strings.TrimSpace(v))
		if err != nil || d.Sign() < 0 || (k != "CM" && k != "FO" && k != "CD") {
			return e, invalid("give each segment's (CM, FO, CD) free broker funds in rupees")
		}
		e.Segments[k] = d.Fixed(2)
	}
	if len(e.Segments) == 0 {
		return e, invalid("give at least one segment's figure")
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cPayoutFunds, date, e, actor); err != nil {
		return e, err
	}
	o.log(ctx, tenant, actor, "payouthold.funds", date, map[string]any{"segments": e.Segments, "note": e.Note})
	o.closeWork(ctx, tenant, actor, "payouthold", "funds|"+date, "figure entered")
	return e, nil
}

// nclmFree is a member's free funds from a margins API snapshot row: the
// effective deposit less the total margin.
func nclmFree(snap NCLMSnapshot, tm string) (money.Dec, bool) {
	idx := func(name string) int {
		for i, c := range snap.Columns {
			if c == name {
				return i
			}
		}
		return -1
	}
	code, dep, tot := idx("TM/CP code"), idx("Effective deposit"), idx("Total margin")
	if snap.Kind == "cm" {
		code, dep = idx("CM code"), idx("Effective collateral")
	}
	if dep < 0 || tot < 0 {
		return money.Dec{}, false
	}
	for _, r := range snap.Rows {
		if code >= 0 && code < len(r) && tm != "" && strings.TrimSpace(r[code]) != tm && len(snap.Rows) > 1 {
			continue
		}
		if dep >= len(r) || tot >= len(r) {
			continue
		}
		return money.Max(nclmDec(r[dep]).Sub(nclmDec(r[tot])), money.Dec{}), true
	}
	return money.Dec{}, false
}

// nmassFree is the member's free funds from an N-MASS file: the row without
// a client code (the trading member's own), cash plus non-cash less margins.
func nmassFree(d CCData, tm string) (money.Dec, bool) {
	for _, r := range d.Rows {
		if strings.TrimSpace(firstOf(r["CLIENT CODE"], r["Client Code"])) != "" {
			continue
		}
		if c := strings.TrimSpace(r["TM/CP CODE"]); tm != "" && c != "" && c != tm {
			continue
		}
		cash, nc := decOr0(firstOf(r["CASH(A)"], r["Cash allocated (b)"])), decOr0(r["NON CASH(B)"])
		mg := decOr0(r["TOTAL MARGINS (F=C+D+E)"])
		return money.Max(cash.Add(nc).Sub(mg), money.Dec{}), true
	}
	return money.Dec{}, false
}

// PayoutBrokerFunds finds the starting free broker funds for each segment
// the firm counts: NSE Clearing's margins API (trading member figures),
// then the N-MASS file, then the figure typed in by the risk team.
func (o *Ops) PayoutBrokerFunds(ctx context.Context, tenant, date string) PayoutFunds {
	ps := o.PayoutHoldSettings(ctx, tenant)
	set, _ := o.Settings(ctx, tenant)
	tm := strings.TrimSpace(set.MemberCodes["NSE"])
	prev := set.PrevBusinessDay(date)
	manual, _, merr := docs.Get[PayoutFundsEntry](ctx, o.Docs, tenant, cPayoutFunds, date)
	out := PayoutFunds{Segments: []PayoutFundsSeg{}}
	for _, seg := range ps.Segments {
		var got *PayoutFundsSeg
		// 1. The margins API: the latest trading member snapshot of today
		// (else the previous day's last, the state at the start of today).
		if seg != "CM" {
			for _, d := range []string{date, prev} {
				for _, kind := range []string{"tm", "cm"} {
					snap, _, err := docs.Get[NCLMSnapshot](ctx, o.Docs, tenant, cNCLMSnap, seg+"|"+d+"|"+kind)
					if err != nil {
						continue
					}
					if v, ok := nclmFree(snap, tm); ok && got == nil {
						got = &PayoutFundsSeg{Segment: seg, Amount: v, Source: "NSE Clearing margins API (" + map[string]string{"tm": "trading member", "cm": "clearing member"}[kind] + " figures)", At: snap.At}
					}
				}
				if got != nil {
					break
				}
			}
		}
		// 2. The N-MASS file.
		if got == nil {
			for _, d := range []string{date, prev} {
				for _, cd := range o.ccData(ctx, tenant, "nmass", d, seg) {
					if v, ok := nmassFree(cd, tm); ok {
						got = &PayoutFundsSeg{Segment: seg, Amount: v, Source: "N-MASS file " + cd.File, At: cd.LoadedAt}
						break
					}
				}
				if got != nil {
					break
				}
			}
		}
		// 3. Typed in.
		if got == nil && merr == nil {
			if v, ok := manual.Segments[seg]; ok {
				got = &PayoutFundsSeg{Segment: seg, Amount: decOr0(v), Source: "Entered by " + manual.By, At: manual.At}
			}
		}
		if got == nil {
			out.Missing = append(out.Missing, seg)
			continue
		}
		out.Segments = append(out.Segments, *got)
		out.Total = out.Total.Add(got.Amount)
	}
	return out
}

// ---- sale money awaiting pay-out ----

type payoutClient struct {
	UCC, Name                       string
	Sold, Bought, Charges, Awaiting money.Dec
	Parts                           []PayoutPart
}

// payoutTradeDates are the trade dates whose funds pay-out falls on date.
func payoutTradeDates(set Settings, date string) []string {
	var out []string
	for i := 1; i <= 10; i++ {
		d := addDays(date, -i)
		if set.IsBusinessDay(d) && set.NextSettlementDay(d) == date {
			out = append(out, d)
		}
	}
	sort.Strings(out)
	return out
}

// PayoutAwaiting computes each client's sale money awaiting pay-out on
// date, settlement by settlement (exchange and trade date): sale value,
// purchase value and charges of the cash-market trades whose funds pay-out
// is on date. Charges come from the posted bills, or are worked out from
// the trades when the bill is not posted yet.
func (o *Ops) PayoutAwaiting(ctx context.Context, tenant, date string) (map[string]*payoutClient, error) {
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return nil, err
	}
	ps := o.PayoutHoldSettings(ctx, tenant)
	cs, _ := o.ClientMap(ctx, tenant)
	out := map[string]*payoutClient{}
	for _, td := range payoutTradeDates(set, date) {
		trades, err := o.exchangeTrades(ctx, tenant, td)
		if err != nil {
			return nil, err
		}
		var cm []ingest.TradeEvent
		type grp struct{ sold, bought, charges money.Dec }
		g := map[string]*grp{}
		for _, t := range trades {
			if t.Segment != "CM" {
				continue
			}
			cm = append(cm, t)
			k := strings.ToUpper(t.UCC) + "|" + t.Exchange
			if g[k] == nil {
				g[k] = &grp{}
			}
			if t.Side == "S" {
				g[k].sold = g[k].sold.Add(t.Value())
			} else {
				g[k].bought = g[k].bought.Add(t.Value())
			}
		}
		if len(cm) == 0 {
			continue
		}
		// Charges: posted bills first; worked out for the rest.
		posted := map[string]money.Dec{}
		if es, err := o.App.Ledger.Store.List(ctx, tenant, ledgerFilterDate(td)); err == nil {
			reversed := map[string]bool{}
			for _, e := range es {
				if e.ReversalOf != "" {
					reversed[e.ReversalOf] = true
				}
			}
			for _, e := range es {
				if e.Source.Kind != "BILL" || reversed[e.ID] {
					continue
				}
				p := strings.Split(e.Source.ID, "/")
				if len(p) != 3 || !strings.HasSuffix(p[2], "-CM") {
					continue
				}
				k := strings.ToUpper(p[1]) + "|" + strings.TrimSuffix(p[2], "-CM")
				for _, x := range e.Postings {
					if x.Memo == "Brokerage, taxes and charges" && strings.HasPrefix(x.Account, "CLIENT:") {
						posted[k] = posted[k].Add((x.Debit - x.Credit).Dec())
					}
				}
			}
		}
		if bo, err := o.App.BuildBills(ctx, billing.Input{Tenant: tenant, Date: td, Actor: "payout-hold", Trades: cm, Schedule: o.App.Schedule}); err == nil {
			for _, x := range bo.Groups {
				if x.Segment != "CM" {
					continue
				}
				k := strings.ToUpper(x.UCC) + "|" + x.Exchange
				if _, ok := posted[k]; !ok && g[k] != nil {
					g[k].charges = x.Charges.Total.Dec()
				}
			}
		}
		for k, v := range posted {
			if g[k] != nil {
				g[k].charges = v
			}
		}
		sett, _ := o.SettlementFor(ctx, tenant, "CM", "NORMAL1", td)
		for _, k := range sortedKeys(g) {
			x := g[k]
			ucc, ex, _ := strings.Cut(k, "|")
			c := out[ucc]
			if c == nil {
				c = &payoutClient{UCC: ucc, Name: cs[ucc].Name}
				out[ucc] = c
			}
			net := x.sold.Sub(x.bought).Sub(x.charges)
			if ps.Basis == "gross" {
				net = x.sold.Sub(x.charges)
			}
			p := PayoutPart{Key: ex + "-CM|" + td, Exchange: ex, Clearing: set.ClearedThrough(ex + "-CM"), TradeDate: td, Sold: x.sold, Bought: x.bought, Charges: x.charges, Awaiting: money.Max(net, money.Dec{})}
			if ex == "NSE" && sett.No != "" {
				p.SettNo, p.PayoutAt = sett.No, sett.FundsOut
			}
			c.Sold, c.Bought, c.Charges = c.Sold.Add(x.sold), c.Bought.Add(x.bought), c.Charges.Add(x.charges)
			c.Awaiting = c.Awaiting.Add(p.Awaiting)
			c.Parts = append(c.Parts, p)
		}
	}
	return out, nil
}

// ---- deciding the holds ----

func payoutPct(d money.Dec, pct string) money.Dec {
	p, err := money.ParseDec(pct)
	if err != nil {
		return money.Dec{}
	}
	return d.Pct(p).Round(money.UnitPaisa).Dec()
}

// spreadParts shares a hold across the settlements by their sale money.
func spreadParts(parts []PayoutPart, amount money.Dec) []PayoutPart {
	var tot money.Dec
	idx := []int{}
	for i, p := range parts {
		parts[i].Hold = money.Dec{}
		if p.Awaiting.Sign() > 0 && !p.Released {
			tot = tot.Add(p.Awaiting)
			idx = append(idx, i)
		}
	}
	left := amount
	for n, i := range idx {
		share := left
		if n < len(idx)-1 && tot.Sign() > 0 {
			q, _ := amount.Mul(parts[i].Awaiting).Quo(tot)
			share = q.Round(money.UnitPaisa).Dec()
		}
		parts[i].Hold = share
		left = left.Sub(share)
	}
	return parts
}

// ApplyPayoutHolds works out today's holds and, in automatic mode, makes
// them (the Apply holds now button; also run by the scheduler once the
// start-of-day files are done). Clients already held today are left alone.
func (o *Ops) ApplyPayoutHolds(ctx context.Context, tenant, actor, date string) (PayoutHoldView, error) {
	if actor != SystemActor {
		if err := o.require(ctx, tenant, actor, "Applying pay-out holds", RoleChecker, RoleRisk); err != nil {
			return PayoutHoldView{}, err
		}
	}
	if !validDate(date) {
		return PayoutHoldView{}, invalid("give the date")
	}
	trigger := "Apply holds now (" + actor + ")"
	if actor == SystemActor {
		trigger = "scheduler"
	}
	if err := o.applyPayoutHolds(ctx, tenant, actor, date, trigger); err != nil {
		return PayoutHoldView{}, err
	}
	return o.PayoutHoldScreen(ctx, tenant, date)
}

func (o *Ops) applyPayoutHolds(ctx context.Context, tenant, actor, date, trigger string) error {
	ps := o.PayoutHoldSettings(ctx, tenant)
	if !ps.Enabled {
		return invalid("pay-out holds are switched off: switch them on in the rules first")
	}
	set, _ := o.Settings(ctx, tenant)
	day, _, _ := docs.GetOr(ctx, o.Docs, tenant, cPayoutDay, date, PayoutDay{Date: date})
	if day.PaidAll {
		return invalid("the funds pay-out for %s has already arrived: nothing to hold", date)
	}
	funds := o.PayoutBrokerFunds(ctx, tenant, date)
	if len(funds.Missing) > 0 {
		o.raiseWork(ctx, tenant, "payouthold", "funds|"+date, "Starting broker funds needed · "+date,
			fmt.Sprintf("Ledgerline has no free broker funds figure for %s today (no NSE Clearing margins API figure and no N-MASS file). Enter the figure on Pay-out holds so big sellers' allocations can be checked; until then no holds are made.", strings.Join(funds.Missing, ", ")), "", RoleRisk, "payouthold", date)
		return invalid("no starting broker funds for %s: enter the figure first", strings.Join(funds.Missing, ", "))
	}
	threshold := payoutPct(funds.Total, ps.ThresholdPct)
	clients, err := o.PayoutAwaiting(ctx, tenant, date)
	if err != nil {
		return err
	}
	minHold := decOr0(ps.MinHold)
	existing, _ := docs.List[PayoutHold](ctx, o.Docs, tenant, cPayoutHold, date+"_")
	have := map[string]bool{}
	for _, h := range existing {
		have[h.UCC] = true
	}
	var prev string
	if p := set.PrevBusinessDay(date); p != "" {
		prev = p
	}
	limits := map[string]money.Dec{}
	if rv, err := o.Risk(ctx, tenant, prev); err == nil {
		for _, r := range rv.Rows {
			limits[r.UCC] = r.Limit
		}
	}
	now := o.now().Format(time.RFC3339)
	var made []PayoutHold
	var totalHold, totalAwaiting money.Dec
	for _, u := range sortedKeys(clients) {
		c := clients[u]
		totalAwaiting = totalAwaiting.Add(c.Awaiting)
		if have[u] || c.Awaiting.Cmp(threshold) <= 0 {
			continue
		}
		var parts []PayoutPart
		for _, p := range c.Parts {
			if day.paid(p) {
				continue
			}
			parts = append(parts, p)
		}
		var awaiting money.Dec
		for _, p := range parts {
			awaiting = awaiting.Add(p.Awaiting)
		}
		if awaiting.Cmp(threshold) <= 0 {
			continue
		}
		hold := money.Min(payoutPct(awaiting, ps.HoldPct), awaiting)
		if hold.Sign() <= 0 || hold.Cmp(minHold) < 0 {
			continue
		}
		h := PayoutHold{ID: payoutHoldID(date, u), Date: date, UCC: u, Name: c.Name, Sold: c.Sold, Bought: c.Bought, Charges: c.Charges, Awaiting: awaiting,
			Proposed: hold, Amount: hold, Parts: spreadParts(parts, hold), Status: PHProposed, Created: now}
		if l, ok := limits[u]; ok {
			h.BODLimit = &l
		}
		if ps.Mode == "auto" {
			h.Status, h.DecidedBy = PHApproved, "rule (automatic)"
		}
		if err := docs.Upsert(ctx, o.Docs, tenant, cPayoutHold, h.ID, h, actor); err != nil {
			return err
		}
		o.log(ctx, tenant, actor, "payouthold.proposed", h.ID, map[string]any{"ucc": u, "awaiting": awaiting.Fixed(2), "hold": hold.Fixed(2), "threshold": threshold.Fixed(2), "mode": ps.Mode})
		made = append(made, h)
		totalHold = totalHold.Add(hold)
	}
	day.Funds, day.Threshold = funds, threshold
	day.AppliedAt, day.AppliedBy, day.Trigger = now, actor, trigger
	if err := docs.Upsert(ctx, o.Docs, tenant, cPayoutDay, date, day, actor); err != nil {
		return err
	}
	o.closeWork(ctx, tenant, actor, "payouthold", "funds|"+date, "funds found")
	o.log(ctx, tenant, actor, "payouthold.applied", date, map[string]any{"trigger": trigger, "funds": funds.Total.Fixed(2), "threshold": threshold.Fixed(2), "holds": len(made), "total": totalHold.Fixed(2)})
	if ps.Mode == "propose" && len(made) > 0 {
		o.raiseWork(ctx, tenant, "payouthold", "propose|"+date, fmt.Sprintf("Approve %d pay-out hold(s) · %s", len(made), date),
			fmt.Sprintf("%d client(s) have sale money awaiting pay-out above ₹%s (%s%% of starting broker funds ₹%s). Proposed holds total ₹%s. Approve or edit each on Pay-out holds.", len(made), formatINR(threshold), ps.ThresholdPct, formatINR(funds.Total), formatINR(totalHold)), "", RoleRisk, "payouthold", date)
	}
	// Unpaid sale money left allocated beyond the broker's own funds.
	if left := totalAwaiting.Sub(totalHold); left.Cmp(funds.Total) > 0 || totalHold.Cmp(funds.Total) > 0 {
		o.raiseWork(ctx, tenant, "payouthold", "exceeds|"+date, "Sale money awaiting pay-out exceeds broker funds · "+date,
			fmt.Sprintf("Sale money awaiting pay-out is ₹%s against starting broker funds of ₹%s; the holds take back ₹%s, leaving ₹%s still allocated before pay-out. Consider lowering the threshold, holding more, or adding funds at NSE Clearing.", formatINR(totalAwaiting), formatINR(funds.Total), formatINR(totalHold), formatINR(left)), "", RoleRisk, "payouthold", date)
	}
	for _, h := range made {
		if h.Status == PHApproved {
			o.submitHold(ctx, tenant, actor, h.ID)
		}
	}
	return nil
}

func (d PayoutDay) paid(p PayoutPart) bool { return d.PaidAll || contains(d.PaidKeys, p.Key) }

// DecidePayoutHold approves (optionally with another amount) or rejects a
// proposed hold. The risk team decides.
func (o *Ops) DecidePayoutHold(ctx context.Context, tenant, actor, id string, approve bool, amount, note string) (PayoutHold, error) {
	if err := o.require(ctx, tenant, actor, "Approving pay-out holds", RoleRisk); err != nil {
		return PayoutHold{}, err
	}
	h, _, err := docs.Get[PayoutHold](ctx, o.Docs, tenant, cPayoutHold, id)
	if err != nil {
		return h, err
	}
	if h.Status != PHProposed {
		return h, fmt.Errorf("%w: the hold on %s is %s", ErrState, h.UCC, strings.ToLower(h.Status))
	}
	if !approve && strings.TrimSpace(note) == "" {
		return h, invalid("give the reason for not holding")
	}
	amt := h.Amount
	if approve && strings.TrimSpace(amount) != "" {
		a, err := money.ParseDec(strings.TrimSpace(amount))
		if err != nil || a.Sign() <= 0 {
			return h, invalid("the hold is an amount above 0")
		}
		if a.Cmp(h.Awaiting) > 0 {
			return h, invalid("the hold cannot be more than the sale money awaiting pay-out (₹%s)", formatINR(h.Awaiting))
		}
		amt = a.Round(money.UnitPaisa).Dec()
	}
	h, err = docs.Update(ctx, o.Docs, tenant, cPayoutHold, id, actor, func(x *PayoutHold) error {
		x.DecidedBy, x.Note = actor, strings.TrimSpace(note)
		if !approve {
			x.Status = PHRejected
			return nil
		}
		x.Status, x.Amount = PHApproved, amt
		x.Parts = spreadParts(x.Parts, amt)
		return nil
	})
	if err != nil {
		return h, err
	}
	o.log(ctx, tenant, actor, "payouthold."+map[bool]string{true: "approved", false: "rejected"}[approve], id, map[string]any{"ucc": h.UCC, "proposed": h.Proposed.Fixed(2), "amount": h.Amount.Fixed(2), "note": h.Note})
	if approve {
		o.submitHold(ctx, tenant, actor, id)
		h, _, _ = docs.Get[PayoutHold](ctx, o.Docs, tenant, cPayoutHold, id)
	}
	if hs, _ := docs.List[PayoutHold](ctx, o.Docs, tenant, cPayoutHold, h.Date+"_"); !anyStatus(hs, PHProposed) {
		o.closeWork(ctx, tenant, actor, "payouthold", "propose|"+h.Date, "all decided")
	}
	return h, nil
}

func anyStatus(hs []PayoutHold, st string) bool {
	for _, h := range hs {
		if h.Status == st {
			return true
		}
	}
	return false
}

// ---- making the changes ----

var allocSegOrder = []string{"CM", "FO", "CD"}

// allocAPIOn says whether requests can go to NSE Clearing now.
func (o *Ops) allocAPIOn(ctx context.Context, tenant string, set Settings) bool {
	if !set.AllocAPILive {
		return false
	}
	c, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connNCLAlloc)
	return err == nil && c.Secrets["api_key"] != "" && c.Secrets["secret"] != ""
}

// holdBase is what each segment holds for the client now: the confirmed
// level; with the API off and no level of today, Ledgerline's own
// allocation of the day (what the start-of-day file allocated).
func (o *Ops) holdBase(ctx context.Context, tenant, date, ucc string, api bool) (map[string]money.Dec, string) {
	levels := o.allocLevels(ctx, tenant)
	out := map[string]money.Dec{}
	for _, seg := range allocSegOrder {
		l, ok := levels[allocKey(seg, ucc)]
		if !ok {
			continue
		}
		if l.Unconfirmed {
			return nil, "The client's " + seg + " allocation is unconfirmed (a request was partly accepted): confirm it on Clearing allocation first"
		}
		if api || strings.HasPrefix(l.AsOf, date) {
			out[seg] = l.Amount
		}
	}
	if !api && len(out) == 0 {
		if al, err := o.allocationBase(ctx, tenant, date); err == nil {
			for _, a := range al {
				if a.UCC == ucc && a.Segment != "COM" {
					out[a.Segment] = out[a.Segment].Add(a.Cash)
				}
			}
		}
	}
	return out, ""
}

func segsOf(recs map[string]money.Dec) []string {
	var out []string
	for _, s := range allocSegOrder {
		if _, ok := recs[s]; ok {
			out = append(out, s)
		}
	}
	return out
}

// submitHold makes an approved hold's allocation change: a request to NSE
// Clearing, or the N-MASS file to upload by hand.
func (o *Ops) submitHold(ctx context.Context, tenant, actor, id string) {
	h, _, err := docs.Get[PayoutHold](ctx, o.Docs, tenant, cPayoutHold, id)
	if err != nil || h.Status != PHApproved {
		return
	}
	set, _ := o.Settings(ctx, tenant)
	api := o.allocAPIOn(ctx, tenant, set)
	wait := func(why string) {
		if why != h.Waiting {
			_, _ = docs.Update(ctx, o.Docs, tenant, cPayoutHold, id, actor, func(x *PayoutHold) error { x.Waiting = why; return nil })
		}
	}
	ms, _ := docs.List[AllocMessage](ctx, o.Docs, tenant, cAllocMsg, "")
	for _, seg := range allocSegOrder {
		if inFlight(ms, seg, h.UCC) {
			wait("Waiting for the result of the request already sent for " + seg)
			return
		}
	}
	base, why := o.holdBase(ctx, tenant, h.Date, h.UCC, api)
	if why != "" {
		wait(why)
		return
	}
	left := h.Amount
	var legs []PayoutLeg
	for _, seg := range segsOf(base) {
		have := base[seg]
		if left.Sign() <= 0 || have.Sign() <= 0 {
			continue
		}
		take := money.Min(have, left)
		legs = append(legs, PayoutLeg{Segment: seg, Before: have, Hold: take, Target: have.Sub(take)})
		left = left.Sub(take)
	}
	if len(legs) == 0 {
		h, _ = docs.Update(ctx, o.Docs, tenant, cPayoutHold, id, actor, func(x *PayoutHold) error {
			x.Status, x.Waiting, x.Error = PHFailed, "", "No allocation at NSE Clearing for the client to hold back"
			return nil
		})
		o.raiseWork(ctx, tenant, "payouthold", "failed|"+id, "Pay-out hold not made · "+h.UCC, fmt.Sprintf("The ₹%s hold for %s could not be made: %s. Check the client's allocation on Clearing allocation.", formatINR(h.Amount), h.UCC, h.Error), h.UCC, RoleRisk, "payouthold", h.Date)
		return
	}
	var total money.Dec
	for _, l := range legs {
		total = total.Add(l.Hold)
	}
	save := func(st, msg string) {
		h, _ = docs.Update(ctx, o.Docs, tenant, cPayoutHold, id, actor, func(x *PayoutHold) error {
			x.Legs, x.Status, x.Waiting, x.MsgID, x.AppliedAt = legs, st, "", msg, o.now().Format(time.RFC3339)
			if total.Cmp(x.Amount) < 0 {
				x.Note = strings.TrimSpace(x.Note + fmt.Sprintf(" Held ₹%s, not ₹%s: the client's allocation is only that much.", formatINR(total), formatINR(x.Amount)))
				x.Amount = total
				x.Parts = spreadParts(x.Parts, total)
			}
			return nil
		})
	}
	if !api {
		save(PHToNMASS, "")
		o.log(ctx, tenant, actor, "payouthold.nmass", id, map[string]any{"ucc": h.UCC, "hold": total.Fixed(2), "legs": len(legs)})
		o.raiseWork(ctx, tenant, "payouthold", "nmass|"+h.Date, "Make the pay-out holds in N-MASS · "+h.Date, "The NSE Clearing allocation API is not switched on: download the N-MASS hold file on Pay-out holds, upload it in N-MASS, then click Done in N-MASS. Upload the OMS hold file to the trading front end as well.", "", RoleRisk, "payouthold", h.Date)
		return
	}
	cm, tm, err := o.allocCodes(set)
	if err != nil {
		wait(err.Error())
		return
	}
	mid, err := o.newAllocMsgID(ctx, tenant, actor, cm, o.now().Format("2006-01-02"))
	if err != nil {
		wait(err.Error())
		return
	}
	var recs []AllocRecord
	for _, l := range legs {
		recs = append(recs, AllocRecord{Segment: l.Segment, UCC: h.UCC, AccType: "C", Amount: l.Target, Previous: l.Before, Action: "D", Ref: id})
	}
	m := AllocMessage{ID: mid, Date: o.now().Format("2006-01-02"), Kind: AllocPayoutHold, RequestType: "I", Records: recs, Status: "Ready", PreparedBy: actor, ApprovedBy: firstOf(h.DecidedBy, actor), Created: o.now().Format(time.RFC3339)}
	if err := docs.Upsert(ctx, o.Docs, tenant, cAllocMsg, mid, m, actor); err != nil {
		wait(err.Error())
		return
	}
	save(PHSending, mid)
	o.log(ctx, tenant, actor, "payouthold.sent", id, map[string]any{"ucc": h.UCC, "hold": total.Fixed(2), "msg_id": mid})
	_, _ = o.allocSendReady(ctx, tenant, actor, set, cm, tm, []AllocMessage{m})
	o.payoutSyncMessages(ctx, tenant, actor, h.Date)
}

// ---- release ----

// payoutPartsFor picks the settlements a credit pays out: those whose
// settlement number it names, else those of the clearing corporation it
// names, else all of them.
func payoutPartsFor(text string, keys []PayoutPart) []string {
	t := strings.ToUpper(text)
	var out []string
	for _, p := range keys {
		if len(p.SettNo) >= 4 && strings.Contains(t, strings.ToUpper(p.SettNo)) && !contains(out, p.Key) {
			out = append(out, p.Key)
		}
	}
	if len(out) > 0 {
		return out
	}
	cc := ""
	switch {
	case regexp.MustCompile(`ICCL|INDIAN CLEARING`).MatchString(t):
		cc = "ICCL"
	case regexp.MustCompile(`NSCCL|NSE|NATIONAL SECURITIES`).MatchString(t):
		cc = "NCL"
	}
	for _, p := range keys {
		if (cc == "" || p.Clearing == cc) && !contains(out, p.Key) {
			out = append(out, p.Key)
		}
	}
	return out
}

// PayoutReceived records that the funds pay-out arrived (the Pay-out
// received button) for the settlements given, or all of them, and
// releases the matching holds.
func (o *Ops) PayoutReceived(ctx context.Context, tenant, actor, date string, keys []string, note string) (PayoutHoldView, error) {
	if err := o.require(ctx, tenant, actor, "Recording the funds pay-out", RoleChecker, RoleRisk); err != nil {
		return PayoutHoldView{}, err
	}
	if !validDate(date) {
		return PayoutHoldView{}, invalid("give the date")
	}
	why := "Pay-out received, recorded by " + actor
	if n := strings.TrimSpace(note); n != "" {
		why += ": " + n
	}
	o.releasePayout(ctx, tenant, actor, date, keys, why)
	return o.PayoutHoldScreen(ctx, tenant, date)
}

// releasePayout marks the settlements paid out (all when keys is empty)
// and releases what they hold.
func (o *Ops) releasePayout(ctx context.Context, tenant, actor, date string, keys []string, why string) int {
	_, _ = upd(ctx, o.Docs, tenant, cPayoutDay, date, actor, &PayoutDay{Date: date}, func(d *PayoutDay) error {
		if len(keys) == 0 {
			d.PaidAll = true
		}
		for _, k := range keys {
			if !contains(d.PaidKeys, k) {
				d.PaidKeys = append(d.PaidKeys, k)
			}
		}
		return nil
	})
	hs, _ := docs.List[PayoutHold](ctx, o.Docs, tenant, cPayoutHold, date+"_")
	n := 0
	for _, h := range hs {
		changed := false
		for i := range h.Parts {
			p := &h.Parts[i]
			if p.Released || p.Due || (len(keys) > 0 && !contains(keys, p.Key)) {
				continue
			}
			p.Due, p.DueWhy, changed = true, why, true
		}
		if !changed {
			// Pressed again after a release that failed: resend.
			if h.Status == PHReleaseFailed {
				o.submitRelease(ctx, tenant, actor, h.ID)
				n++
			}
			continue
		}
		switch h.Status {
		case PHProposed, PHApproved, PHToNMASS:
			// Never made: nothing to put back.
			allDue := true
			for _, p := range h.Parts {
				allDue = allDue && (p.Due || p.Released || p.Hold.Sign() == 0)
			}
			if !allDue {
				_ = docs.Upsert(ctx, o.Docs, tenant, cPayoutHold, h.ID, h, actor)
				continue
			}
			was := h.Status
			h.Status, h.Waiting, h.Note = PHNotNeeded, "", strings.TrimSpace(h.Note+" "+why+" before the hold was made.")
			_ = docs.Upsert(ctx, o.Docs, tenant, cPayoutHold, h.ID, h, actor)
			o.log(ctx, tenant, actor, "payouthold.not_needed", h.ID, map[string]any{"ucc": h.UCC, "was": was})
			if was == PHToNMASS {
				o.raiseWork(ctx, tenant, "payouthold", "nmass-undo|"+h.ID, "Check N-MASS for "+h.UCC, fmt.Sprintf("The pay-out arrived before the hold on %s was marked done in N-MASS. If the ₹%s hold was already made in N-MASS, put the client's allocation back there.", h.UCC, formatINR(h.Amount)), h.UCC, RoleRisk, "payouthold", date)
			}
			n++
		default:
			_ = docs.Upsert(ctx, o.Docs, tenant, cPayoutHold, h.ID, h, actor)
			if phInEffect[h.Status] {
				o.submitRelease(ctx, tenant, actor, h.ID)
				n++
			}
		}
	}
	o.log(ctx, tenant, actor, "payouthold.payout", date, map[string]any{"settlements": keys, "why": why, "holds": n})
	return n
}

// submitRelease puts back what the due settlements held: a request to NSE
// Clearing (final level = confirmed level + the amount released), or the
// N-MASS file.
func (o *Ops) submitRelease(ctx context.Context, tenant, actor, id string) {
	h, _, err := docs.Get[PayoutHold](ctx, o.Docs, tenant, cPayoutHold, id)
	if err != nil || !phInEffect[h.Status] || h.Status == PHReleasing || h.Status == PHReleaseNMASS || h.Status == PHSending {
		return // a hold still being sent is released once its result is in
	}
	var amount money.Dec
	var parts []string
	for _, p := range h.Parts {
		if p.Due && !p.Released && !p.Releasing {
			amount = amount.Add(p.Hold)
			parts = append(parts, p.Key)
		}
	}
	if len(parts) == 0 {
		return
	}
	set, _ := o.Settings(ctx, tenant)
	api := o.allocAPIOn(ctx, tenant, set)
	wait := func(why string) {
		_, _ = docs.Update(ctx, o.Docs, tenant, cPayoutHold, id, actor, func(x *PayoutHold) error { x.Waiting = why; return nil })
	}
	ms, _ := docs.List[AllocMessage](ctx, o.Docs, tenant, cAllocMsg, "")
	for _, l := range h.Legs {
		if inFlight(ms, l.Segment, h.UCC) {
			wait("Waiting for the result of the request already sent for " + l.Segment)
			return
		}
	}
	levels := o.allocLevels(ctx, tenant)
	// Put back from the last segment held first.
	legs := append([]PayoutLeg{}, h.Legs...)
	left := amount
	for i := len(legs) - 1; i >= 0; i-- {
		l := &legs[i]
		room := l.Hold.Sub(l.Released)
		if room.Sign() <= 0 || left.Sign() <= 0 {
			continue
		}
		share := money.Min(room, left)
		base := l.Target.Add(l.Released)
		if lv, ok := levels[allocKey(l.Segment, h.UCC)]; ok {
			base = lv.Amount
		}
		l.Releasing, l.ReleaseTarget = share, base.Add(share)
		left = left.Sub(share)
	}
	mark := func(st, msg string) {
		h, _ = docs.Update(ctx, o.Docs, tenant, cPayoutHold, id, actor, func(x *PayoutHold) error {
			x.Legs, x.Status, x.Waiting, x.ReleaseMsgID, x.Error = legs, st, "", msg, ""
			for i := range x.Parts {
				if contains(parts, x.Parts[i].Key) {
					x.Parts[i].Releasing = true
				}
			}
			return nil
		})
	}
	if !api {
		mark(PHReleaseNMASS, "")
		o.log(ctx, tenant, actor, "payouthold.release_nmass", id, map[string]any{"ucc": h.UCC, "release": amount.Fixed(2), "settlements": parts})
		o.raiseWork(ctx, tenant, "payouthold", "nmass-release|"+h.Date, "Put the pay-out holds back in N-MASS · "+h.Date, "The pay-out has arrived. Download the N-MASS release file on Pay-out holds, upload it in N-MASS, then click Done in N-MASS. Upload the OMS release file to the trading front end as well.", "", RoleRisk, "payouthold", h.Date)
		return
	}
	cm, tm, err := o.allocCodes(set)
	if err != nil {
		wait(err.Error())
		return
	}
	mid, err := o.newAllocMsgID(ctx, tenant, actor, cm, o.now().Format("2006-01-02"))
	if err != nil {
		wait(err.Error())
		return
	}
	var recs []AllocRecord
	for _, l := range legs {
		if l.Releasing.Sign() > 0 {
			recs = append(recs, AllocRecord{Segment: l.Segment, UCC: h.UCC, AccType: "C", Amount: l.ReleaseTarget, Previous: l.ReleaseTarget.Sub(l.Releasing), Action: "U", Ref: id})
		}
	}
	m := AllocMessage{ID: mid, Date: o.now().Format("2006-01-02"), Kind: AllocPayoutRelease, RequestType: "I", Records: recs, Status: "Ready", PreparedBy: actor, Created: o.now().Format(time.RFC3339)}
	if err := docs.Upsert(ctx, o.Docs, tenant, cAllocMsg, mid, m, actor); err != nil {
		wait(err.Error())
		return
	}
	mark(PHReleasing, mid)
	o.log(ctx, tenant, actor, "payouthold.release_sent", id, map[string]any{"ucc": h.UCC, "release": amount.Fixed(2), "msg_id": mid, "settlements": parts})
	_, _ = o.allocSendReady(ctx, tenant, actor, set, cm, tm, []AllocMessage{m})
	o.payoutSyncMessages(ctx, tenant, actor, h.Date)
}

// finishRelease records a release as made at NSE Clearing.
func finishRelease(x *PayoutHold, now, actor string) {
	for i := range x.Legs {
		x.Legs[i].Released = x.Legs[i].Released.Add(x.Legs[i].Releasing)
		x.Legs[i].Releasing = money.Dec{}
	}
	all := true
	for i := range x.Parts {
		p := &x.Parts[i]
		if p.Releasing {
			p.Releasing, p.Released, p.ReleasedAt = false, true, now
		}
		if p.Hold.Sign() > 0 && !p.Released {
			all = false
		}
	}
	x.Status, x.Error = PHPartReleased, ""
	if all {
		x.Status, x.ReleasedAt, x.ReleasedBy = PHReleased, now, actor
	}
}

// payoutHoldResults applies NSE Clearing's results of hold and release
// requests (called from applyAllocResults).
func (o *Ops) payoutHoldResults(ctx context.Context, tenant, actor string, m AllocMessage) {
	by := map[string][]AllocRecord{}
	for _, r := range m.Records {
		if r.Ref != "" {
			by[r.Ref] = append(by[r.Ref], r)
		}
	}
	now := o.now().Format(time.RFC3339)
	for _, id := range sortedKeys(by) {
		okAll, why := true, ""
		for _, r := range by[id] {
			if r.Code != "0100" {
				okAll, why = false, r.Segment+": "+r.Result
			}
		}
		h, err := docs.Update(ctx, o.Docs, tenant, cPayoutHold, id, actor, func(x *PayoutHold) error {
			switch {
			case m.Kind == AllocPayoutHold && okAll:
				x.Status, x.Error = PHApplied, ""
			case m.Kind == AllocPayoutHold:
				x.Status, x.Error = PHFailed, why
			case okAll:
				finishRelease(x, now, actor)
			default:
				x.Status, x.Error = PHReleaseFailed, why
				for i := range x.Legs {
					x.Legs[i].Releasing = money.Dec{}
				}
				for i := range x.Parts {
					x.Parts[i].Releasing = false
				}
			}
			return nil
		})
		if err != nil {
			continue
		}
		o.log(ctx, tenant, actor, "payouthold.result", id, map[string]any{"kind": m.Kind, "msg_id": m.ID, "status": h.Status, "ucc": h.UCC})
		switch h.Status {
		case PHFailed:
			o.raiseWork(ctx, tenant, "payouthold", "failed|"+id, "Pay-out hold not accepted · "+h.UCC, fmt.Sprintf("NSE Clearing did not accept the ₹%s hold on %s (%s): %s. The OMS limit should not be cut for this client; check the allocation on Clearing allocation.", formatINR(h.Amount), h.UCC, m.ID, h.Error), h.UCC, RoleRisk, "payouthold", h.Date)
		case PHReleaseFailed:
			o.raiseWork(ctx, tenant, "payouthold", "release-failed|"+id, "Pay-out release not accepted · "+h.UCC, fmt.Sprintf("NSE Clearing did not accept putting back %s's allocation (%s): %s. Press Pay-out received again to resend, or make the change in N-MASS.", h.UCC, m.ID, h.Error), h.UCC, RoleRisk, "payouthold", h.Date)
		}
		if h.Status == PHApplied {
			// A pay-out that arrived while the hold was being sent.
			o.submitRelease(ctx, tenant, actor, id)
		}
	}
}

// payoutSyncMessages marks holds whose request failed outright (not
// accepted at all, or after its retries).
func (o *Ops) payoutSyncMessages(ctx context.Context, tenant, actor, date string) {
	hs, _ := docs.List[PayoutHold](ctx, o.Docs, tenant, cPayoutHold, date+"_")
	var ms []AllocMessage
	for _, h := range hs {
		if h.Status != PHSending && h.Status != PHReleasing {
			continue
		}
		if ms == nil {
			ms, _ = docs.List[AllocMessage](ctx, o.Docs, tenant, cAllocMsg, "")
		}
		kind := map[bool]string{true: AllocPayoutHold, false: AllocPayoutRelease}[h.Status == PHSending]
		var latest *AllocMessage
		for i := range ms {
			if ms[i].Kind != kind {
				continue
			}
			for _, r := range ms[i].Records {
				if r.Ref == h.ID && (latest == nil || ms[i].ID > latest.ID) {
					latest = &ms[i]
				}
			}
		}
		if latest == nil || (latest.Status != "Failed" && latest.Status != "Rejected") {
			continue
		}
		why := firstOf(latest.Error, latest.Status)
		if h.Status == PHSending {
			_, _ = docs.Update(ctx, o.Docs, tenant, cPayoutHold, h.ID, actor, func(x *PayoutHold) error { x.Status, x.Error = PHFailed, why; return nil })
			o.raiseWork(ctx, tenant, "payouthold", "failed|"+h.ID, "Pay-out hold not sent · "+h.UCC, fmt.Sprintf("The ₹%s hold on %s (%s) did not reach NSE Clearing: %s.", formatINR(h.Amount), h.UCC, latest.ID, why), h.UCC, RoleRisk, "payouthold", date)
		} else {
			_, _ = docs.Update(ctx, o.Docs, tenant, cPayoutHold, h.ID, actor, func(x *PayoutHold) error {
				x.Status, x.Error = PHReleaseFailed, why
				for i := range x.Legs {
					x.Legs[i].Releasing = money.Dec{}
				}
				for i := range x.Parts {
					x.Parts[i].Releasing = false
				}
				return nil
			})
			o.raiseWork(ctx, tenant, "payouthold", "release-failed|"+h.ID, "Pay-out release not sent · "+h.UCC, fmt.Sprintf("Putting back %s's allocation (%s) did not reach NSE Clearing: %s. Press Pay-out received again to resend, or make the change in N-MASS.", h.UCC, latest.ID, why), h.UCC, RoleRisk, "payouthold", date)
		}
		o.log(ctx, tenant, actor, "payouthold.send_failed", h.ID, map[string]any{"msg_id": latest.ID, "error": why})
	}
}

// PayoutHoldDoneInNMASS records holds and releases made by hand in N-MASS
// (the API is off): the client's level becomes the figure in the file.
func (o *Ops) PayoutHoldDoneInNMASS(ctx context.Context, tenant, actor, date string, ids []string, note string) (PayoutHoldView, error) {
	if err := o.require(ctx, tenant, actor, "Recording N-MASS changes", RoleChecker, RoleRisk); err != nil {
		return PayoutHoldView{}, err
	}
	hs, _ := docs.List[PayoutHold](ctx, o.Docs, tenant, cPayoutHold, date+"_")
	now := o.now().Format(time.RFC3339)
	n := 0
	for _, h := range hs {
		if len(ids) > 0 && !contains(ids, h.ID) {
			continue
		}
		switch h.Status {
		case PHToNMASS:
			for _, l := range h.Legs {
				_ = o.setLevel(ctx, tenant, actor, AllocLevel{Segment: l.Segment, UCC: h.UCC, Amount: l.Target, AsOf: now, Source: "N-MASS by hand (pay-out hold) · " + actor, Note: strings.TrimSpace(note)}, true)
			}
			_, _ = docs.Update(ctx, o.Docs, tenant, cPayoutHold, h.ID, actor, func(x *PayoutHold) error { x.Status, x.AppliedAt = PHByHand, now; return nil })
			o.log(ctx, tenant, actor, "payouthold.done_by_hand", h.ID, map[string]any{"ucc": h.UCC, "hold": h.Amount.Fixed(2)})
			o.submitRelease(ctx, tenant, actor, h.ID) // pay-out already in
			n++
		case PHReleaseNMASS:
			for _, l := range h.Legs {
				if l.Releasing.Sign() > 0 {
					_ = o.setLevel(ctx, tenant, actor, AllocLevel{Segment: l.Segment, UCC: h.UCC, Amount: l.ReleaseTarget, AsOf: now, Source: "N-MASS by hand (pay-out release) · " + actor, Note: strings.TrimSpace(note)}, true)
				}
			}
			_, _ = docs.Update(ctx, o.Docs, tenant, cPayoutHold, h.ID, actor, func(x *PayoutHold) error { finishRelease(x, now, actor); return nil })
			o.log(ctx, tenant, actor, "payouthold.released_by_hand", h.ID, map[string]any{"ucc": h.UCC})
			n++
		}
	}
	if n == 0 {
		return PayoutHoldView{}, invalid("nothing is waiting to be done in N-MASS")
	}
	o.closeWork(ctx, tenant, actor, "payouthold", "nmass|"+date, "done in N-MASS")
	if hs2, _ := docs.List[PayoutHold](ctx, o.Docs, tenant, cPayoutHold, date+"_"); !anyStatus(hs2, PHReleaseNMASS) {
		o.closeWork(ctx, tenant, actor, "payouthold", "nmass-release|"+date, "done in N-MASS")
	}
	return o.PayoutHoldScreen(ctx, tenant, date)
}

// ---- holds as seen by allocation and live RMS ----

// payoutHeld is what active holds take from each client's allocation on
// date, by client and segment.
func (o *Ops) payoutHeld(ctx context.Context, tenant, date string) map[string]map[string]money.Dec {
	hs, _ := docs.List[PayoutHold](ctx, o.Docs, tenant, cPayoutHold, date+"_")
	out := map[string]map[string]money.Dec{}
	for _, h := range hs {
		if h.Date != date || !phInEffect[h.Status] {
			continue
		}
		for _, l := range h.Legs {
			if v := l.Hold.Sub(l.Released); v.Sign() > 0 {
				if out[h.UCC] == nil {
					out[h.UCC] = map[string]money.Dec{}
				}
				out[h.UCC][l.Segment] = out[h.UCC][l.Segment].Add(v)
			}
		}
	}
	return out
}

// withPayoutHolds lowers each held client's cash allocation by its hold.
func (o *Ops) withPayoutHolds(ctx context.Context, tenant, date string, rows []AllocationRow) []AllocationRow {
	held := o.payoutHeld(ctx, tenant, date)
	if len(held) == 0 {
		return rows
	}
	for i := range rows {
		if v, ok := held[rows[i].UCC][rows[i].Segment]; ok {
			rows[i].Cash = money.Max(rows[i].Cash.Sub(v), money.Dec{})
		}
	}
	return rows
}

// ---- detection and the scheduler ----

// payoutBODState says whether today's start-of-day files are done: the
// start-of-day allocation batch sent or accepted (or the allocation file
// made), and the BOD limit file made.
func (o *Ops) payoutBODState(ctx context.Context, tenant, date string) (done, inFlightBOD bool, why string) {
	ms, _ := docs.List[AllocMessage](ctx, o.Docs, tenant, cAllocMsg, "")
	alloc := ""
	for _, m := range ms {
		if m.Kind != AllocBOD || m.Date != date {
			continue
		}
		switch m.Status {
		case "Sent", "Done":
			alloc = "start-of-day allocation " + strings.ToLower(m.Status)
		case "Awaiting approval", "Ready":
			inFlightBOD = true
		}
	}
	recv, _ := docs.List[DailyFileRecv](ctx, o.Docs, tenant, cDailyRecv, date+"|")
	limit := false
	for _, r := range recv {
		if r.Format == payoutBODLimitFile {
			limit = true
		}
		if alloc == "" && (r.Format == "ncl-allocation" || r.Format == "ncl-allocation-api") {
			alloc = "allocation file made"
		}
	}
	if alloc != "" && limit && !inFlightBOD {
		return true, false, alloc + ", BOD limit file made"
	}
	return false, inFlightBOD, ""
}

// payoutCredit says whether a bank credit is the clearing corporation's
// funds pay-out.
func payoutCredit(ps PayoutHoldSettings, t BankTxn) bool {
	if t.Amount <= 0 {
		return false
	}
	text := t.Remitter + " " + t.Ref + " " + t.Note + " " + t.Mode
	if ps.PayoutPattern != "" {
		if re, err := regexp.Compile("(?i)" + ps.PayoutPattern); err == nil && re.MatchString(text) {
			return true
		}
	}
	if a := strings.TrimSpace(ps.PayoutAccount); a != "" && (strings.Contains(t.Account, a) || strings.Contains(t.VirtualAccount, a)) {
		return true
	}
	return false
}

// payoutDetect looks for today's pay-out credits (live notices and the
// loaded statement) and releases the holds they pay.
func (o *Ops) payoutDetect(ctx context.Context, tenant, date string, ps PayoutHoldSettings) {
	if ps.PayoutPattern == "" && ps.PayoutAccount == "" {
		return
	}
	day, _, _ := docs.GetOr(ctx, o.Docs, tenant, cPayoutDay, date, PayoutDay{Date: date})
	ts, _ := docs.List[BankTxn](ctx, o.Docs, tenant, cBankTxn, "")
	var hits []BankTxn
	for _, t := range ts {
		if t.Date == date && !contains(day.Credits, t.ID) && payoutCredit(ps, t) {
			hits = append(hits, t)
		}
	}
	if len(hits) == 0 {
		return
	}
	hs, _ := docs.List[PayoutHold](ctx, o.Docs, tenant, cPayoutHold, date+"_")
	var parts []PayoutPart
	seen := map[string]bool{}
	for _, h := range hs {
		for _, p := range h.Parts {
			if !seen[p.Key] {
				seen[p.Key] = true
				parts = append(parts, p)
			}
		}
	}
	for _, t := range hits {
		_, _ = upd(ctx, o.Docs, tenant, cPayoutDay, date, SystemActor, &PayoutDay{Date: date}, func(d *PayoutDay) error {
			d.Credits = append(d.Credits, t.ID)
			return nil
		})
		keys := payoutPartsFor(t.Remitter+" "+t.Ref+" "+t.Note, parts)
		src := "live bank credit"
		if !strings.Contains(t.Source, "(live)") {
			src = "bank statement"
		}
		why := fmt.Sprintf("Pay-out seen: %s ₹%s from %s (%s)", src, formatINR(t.Amount.Dec()), firstOf(t.Remitter, "the clearing corporation"), t.Ref)
		o.log(ctx, tenant, "bank", "payouthold.credit", t.ID, map[string]any{"amount": t.Amount.String(), "remitter": t.Remitter, "settlements": keys})
		if len(parts) == 0 {
			keys = nil // nothing held yet: the day is paid out
		}
		o.releasePayout(ctx, tenant, "bank", date, keys, why)
	}
}

// payoutHoldTick runs every minute: it makes the day's holds once the
// start-of-day files are done (or at the firm's time), sends what waits,
// releases holds when the pay-out is seen, and at the release time assumes
// the pay-out and says so.
func (o *Ops) payoutHoldTick(ctx context.Context, tenant string, now time.Time) {
	ps := o.PayoutHoldSettings(ctx, tenant)
	if !ps.Enabled {
		return
	}
	set, _ := o.Settings(ctx, tenant)
	now = now.In(ist)
	date, hm := now.Format("2006-01-02"), now.Format("15:04")
	if !set.IsBusinessDay(date) || hm < "07:00" || hm > "21:00" {
		return
	}
	o.payoutSyncMessages(ctx, tenant, SystemActor, date)
	day, _, _ := docs.GetOr(ctx, o.Docs, tenant, cPayoutDay, date, PayoutDay{Date: date})
	if day.AppliedAt == "" && !day.PaidAll && hm < "15:30" && (ps.ReleaseAt == "" || hm < ps.ReleaseAt) {
		done, inflight, why := o.payoutBODState(ctx, tenant, date)
		switch {
		case done:
			_ = o.applyPayoutHolds(ctx, tenant, SystemActor, date, "BOD files done ("+why+")")
		case hm >= ps.ApplyAt && !inflight:
			_ = o.applyPayoutHolds(ctx, tenant, SystemActor, date, "apply time "+ps.ApplyAt)
		}
	}
	hs, _ := docs.List[PayoutHold](ctx, o.Docs, tenant, cPayoutHold, date+"_")
	for _, h := range hs {
		switch {
		case h.Status == PHApproved:
			o.submitHold(ctx, tenant, SystemActor, h.ID)
		case h.Waiting != "" && phInEffect[h.Status]:
			o.submitRelease(ctx, tenant, SystemActor, h.ID)
		}
	}
	o.payoutDetect(ctx, tenant, date, ps)
	if ps.ReleaseAt != "" && hm >= ps.ReleaseAt {
		day, _, _ = docs.GetOr(ctx, o.Docs, tenant, cPayoutDay, date, PayoutDay{Date: date})
		if day.Assumed {
			return
		}
		hs, _ = docs.List[PayoutHold](ctx, o.Docs, tenant, cPayoutHold, date+"_")
		var open []string
		for _, h := range hs {
			for _, p := range h.Parts {
				if p.Hold.Sign() > 0 && !p.Due && !p.Released && h.Status != PHRejected && h.Status != PHFailed && h.Status != PHNotNeeded {
					open = append(open, h.UCC)
					break
				}
			}
		}
		_, _ = upd(ctx, o.Docs, tenant, cPayoutDay, date, SystemActor, &PayoutDay{Date: date}, func(d *PayoutDay) error { d.Assumed = true; return nil })
		if len(open) == 0 {
			return
		}
		o.releasePayout(ctx, tenant, SystemActor, date, nil, "Assumed paid out at "+ps.ReleaseAt+" (no pay-out credit seen)")
		o.raiseWork(ctx, tenant, "payouthold", "nopayout|"+date, "Funds pay-out not seen by "+ps.ReleaseAt+" · "+date,
			fmt.Sprintf("No credit from the clearing corporation was seen by %s, so the pay-out holds on %s were released as the rules say. Check the settlement account for today's funds pay-out.", ps.ReleaseAt, strings.Join(open, ", ")), "", RoleRisk, "payouthold", date)
	}
}

// ---- the screen ----

// PayoutCandidate is a client with sale money awaiting pay-out.
type PayoutCandidate struct {
	UCC      string    `json:"ucc"`
	Name     string    `json:"name"`
	Sold     money.Dec `json:"sold"`
	Bought   money.Dec `json:"bought"`
	Charges  money.Dec `json:"charges"`
	Awaiting money.Dec `json:"awaiting"`
	Over     bool      `json:"over"` // above the threshold
}

// PayoutHoldView is the Pay-out holds screen.
type PayoutHoldView struct {
	Date        string             `json:"date"`
	Settings    PayoutHoldSettings `json:"settings"`
	Funds       PayoutFunds        `json:"funds"`
	FundsEntry  *PayoutFundsEntry  `json:"funds_entry,omitempty"`
	Threshold   money.Dec          `json:"threshold"`
	TotalHeld   money.Dec          `json:"total_held"`
	Proposed    money.Dec          `json:"proposed"`
	Freed       money.Dec          `json:"freed"`
	FreedPct    float64            `json:"freed_pct"`
	Holds       []PayoutHold       `json:"holds"`
	Candidates  []PayoutCandidate  `json:"candidates"`
	Day         PayoutDay          `json:"day"`
	BODDone     bool               `json:"bod_done"`
	BODWhy      string             `json:"bod_why"`
	APILive     bool               `json:"api_live"`
	TradeDates  []string           `json:"trade_dates"`
	Settlements []PayoutPart       `json:"settlements"`
	OMSFormat   string             `json:"oms_format"`
	OMSNote     string             `json:"oms_note"`
	ToNMASS     int                `json:"to_nmass"`
	ReleaseNM   int                `json:"release_nmass"`
	Error       string             `json:"error,omitempty"`
}

// PayoutHoldScreen shows a pay-out day.
func (o *Ops) PayoutHoldScreen(ctx context.Context, tenant, date string) (PayoutHoldView, error) {
	if !validDate(date) {
		return PayoutHoldView{}, invalid("give the date")
	}
	set, _ := o.Settings(ctx, tenant)
	ps := o.PayoutHoldSettings(ctx, tenant)
	v := PayoutHoldView{Date: date, Settings: ps, Holds: []PayoutHold{}, Candidates: []PayoutCandidate{}, Settlements: []PayoutPart{}, APILive: o.allocAPIOn(ctx, tenant, set), TradeDates: payoutTradeDates(set, date)}
	v.Day, _, _ = docs.GetOr(ctx, o.Docs, tenant, cPayoutDay, date, PayoutDay{Date: date})
	if v.Day.AppliedAt != "" {
		v.Funds, v.Threshold = v.Day.Funds, v.Day.Threshold
	} else {
		v.Funds = o.PayoutBrokerFunds(ctx, tenant, date)
		v.Threshold = payoutPct(v.Funds.Total, ps.ThresholdPct)
	}
	if e, _, err := docs.Get[PayoutFundsEntry](ctx, o.Docs, tenant, cPayoutFunds, date); err == nil {
		v.FundsEntry = &e
	}
	v.BODDone, _, v.BODWhy = o.payoutBODState(ctx, tenant, date)
	hs, err := docs.List[PayoutHold](ctx, o.Docs, tenant, cPayoutHold, date+"_")
	if err != nil {
		return v, err
	}
	sort.SliceStable(hs, func(i, j int) bool { return hs[i].Awaiting.Cmp(hs[j].Awaiting) > 0 })
	v.Holds = append(v.Holds, hs...)
	seen := map[string]bool{}
	for _, h := range hs {
		if phInEffect[h.Status] {
			v.TotalHeld = v.TotalHeld.Add(h.held())
		}
		if h.Status == PHProposed || h.Status == PHApproved {
			v.Proposed = v.Proposed.Add(h.Amount)
		}
		if h.Status == PHToNMASS {
			v.ToNMASS++
		}
		if h.Status == PHReleaseNMASS {
			v.ReleaseNM++
		}
		for _, p := range h.Parts {
			if !seen[p.Key] {
				seen[p.Key] = true
				v.Settlements = append(v.Settlements, PayoutPart{Key: p.Key, Exchange: p.Exchange, Clearing: p.Clearing, TradeDate: p.TradeDate, SettNo: p.SettNo, PayoutAt: p.PayoutAt, Due: v.Day.paid(p)})
			}
		}
	}
	v.Freed = v.TotalHeld
	if v.Funds.Total.Sign() > 0 {
		v.FreedPct = v.Freed.Float64() * 100 / v.Funds.Total.Float64()
	}
	if cs, err := o.PayoutAwaiting(ctx, tenant, date); err == nil {
		for _, u := range sortedKeys(cs) {
			c := cs[u]
			if c.Awaiting.Sign() <= 0 {
				continue
			}
			v.Candidates = append(v.Candidates, PayoutCandidate{UCC: u, Name: c.Name, Sold: c.Sold, Bought: c.Bought, Charges: c.Charges, Awaiting: c.Awaiting, Over: v.Funds.Total.Sign() > 0 && c.Awaiting.Cmp(v.Threshold) > 0})
		}
		sort.SliceStable(v.Candidates, func(i, j int) bool { return v.Candidates[i].Awaiting.Cmp(v.Candidates[j].Awaiting) > 0 })
		if len(v.Candidates) > 50 {
			v.Candidates = v.Candidates[:50]
		}
	} else {
		v.Error = err.Error()
	}
	v.OMSFormat, v.OMSNote = payoutOMSFormat()
	return v, nil
}

// payoutOMSFormat: the Finspot limit layout once confirmed; the generic
// limit-change layout meanwhile.
func payoutOMSFormat() (string, string) {
	if lib, err := XFiles(); err == nil {
		if s, ok := lib.Get("finspot-rms-limit"); ok && s.Confirmed {
			return "finspot-rms-limit", "Finspot limit layout"
		}
	}
	return "generic", "Generic limit-change layout (TradeDate, ClientCode, Segment, Change, NewLimit, BODLimit, Reason): the Finspot limit file layout is not confirmed yet, so map these columns in the front end's limit upload."
}

// ---- files ----

// PayoutHoldFile writes one of the day's files: oms-hold, oms-release
// (limit changes for the trading front end) or nmass-hold, nmass-release
// (the allocation file for N-MASS, when the API is off).
func (o *Ops) PayoutHoldFile(ctx context.Context, tenant, actor, date, kind string) (string, []byte, error) {
	if err := o.require(ctx, tenant, actor, "Downloading pay-out hold files", RoleMaker, RoleChecker, RoleRisk); err != nil {
		return "", nil, err
	}
	if !validDate(date) {
		return "", nil, invalid("give the date")
	}
	hs, _ := docs.List[PayoutHold](ctx, o.Docs, tenant, cPayoutHold, date+"_")
	sort.Slice(hs, func(i, j int) bool { return hs[i].UCC < hs[j].UCC })
	compact := strings.ReplaceAll(date, "-", "")
	var name string
	var b []byte
	var err error
	switch kind {
	case "oms-hold", "oms-release":
		name, b = payoutOMSFile(hs, date, kind == "oms-release")
		name = strings.ToUpper(strings.ReplaceAll(kind, "-", "_")) + "_" + compact + ".csv"
	case "nmass-hold", "nmass-release":
		name, b, err = o.payoutNMASSFile(ctx, tenant, hs, date, kind == "nmass-release")
	default:
		return "", nil, invalid("the file is oms-hold, oms-release, nmass-hold or nmass-release")
	}
	if err != nil {
		return "", nil, err
	}
	o.log(ctx, tenant, actor, "payouthold.file", name, map[string]any{"kind": kind, "date": date, "bytes": len(b)})
	return name, b, nil
}

func payoutOMSFile(hs []PayoutHold, date string, release bool) (string, []byte) {
	rows := [][]string{{"TradeDate", "ClientCode", "Segment", "Change", "NewLimit", "BODLimit", "Reason"}}
	for _, h := range hs {
		if len(h.Legs) == 0 {
			continue
		}
		var total money.Dec
		for _, l := range h.Legs {
			total = total.Add(l.Hold)
		}
		bod, nl := "", func(money.Dec) string { return "" }
		if h.BODLimit != nil {
			bod = h.BODLimit.Fixed(2)
			nl = func(held money.Dec) string { return money.Max(h.BODLimit.Sub(held), money.Dec{}).Fixed(2) }
		}
		if !release {
			if h.Status == PHFailed || h.Status == PHRejected || h.Status == PHNotNeeded {
				continue
			}
			var cum money.Dec
			for _, l := range h.Legs {
				cum = cum.Add(l.Hold)
				rows = append(rows, []string{date, h.UCC, l.Segment, l.Hold.Neg().Fixed(2), nl(cum), bod, "Sale money awaiting pay-out: hold"})
			}
			continue
		}
		left := total
		for _, l := range h.Legs {
			back := l.Released.Add(l.Releasing)
			if back.Sign() <= 0 {
				continue
			}
			left = left.Sub(back)
			rows = append(rows, []string{date, h.UCC, l.Segment, back.Fixed(2), nl(left), bod, "Funds pay-out received: release"})
		}
	}
	return "", csvBytesOf(rows)
}

func (o *Ops) payoutNMASSFile(ctx context.Context, tenant string, hs []PayoutHold, date string, release bool) (string, []byte, error) {
	set, _ := o.Settings(ctx, tenant)
	cm, tm, err := o.allocCodes(set)
	if err != nil {
		return "", nil, err
	}
	lib, err := XFiles()
	if err != nil {
		return "", nil, err
	}
	s, ok := lib.Get("ncl-allocation")
	if !ok {
		return "", nil, invalid("the N-MASS allocation layout is missing")
	}
	var rows []xfile.Row
	for _, h := range hs {
		for _, l := range h.Legs {
			r := map[string]string{"curDate": date, "segment": l.Segment, "cmCode": cm, "tmCode": tm, "cpCode": "", "cliCode": h.UCC, "accType": "C"}
			switch {
			case !release && h.Status == PHToNMASS:
				r["amt"], r["action"] = l.Target.Fixed(2), "D"
			case release && h.Status == PHReleaseNMASS && l.Releasing.Sign() > 0:
				r["amt"], r["action"] = l.ReleaseTarget.Fixed(2), "U"
			default:
				continue
			}
			rows = append(rows, xfile.Row{Values: r})
		}
	}
	if len(rows) == 0 {
		return "", nil, invalid("nothing is waiting to be done in N-MASS")
	}
	seq := "2"
	if release {
		seq = "3"
	}
	name, err := s.FileName(xfile.Vars{"date": date, "seq": seq, "member": tm})
	if err != nil {
		return "", nil, invalid("%v", err)
	}
	b, err := s.Write(rows)
	return name, b, err
}
