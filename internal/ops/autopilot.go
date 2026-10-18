package ops

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/app"
	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
	"ledgerline/internal/recon"
)

// ---- autopilot: straight-through processing with an exception queue ----
//
// Every routine process has a policy: Auto (the system does it and records
// that it did), Suggest (the system prepares it and a person approves it in
// one click), or Off. Each policy has a cap set by the rules: where SEBI,
// the exchanges, the depositories or the PMLA rules need a person's
// judgement or signature (verifying a physical slip, deciding a suspicious
// transaction, verifying documents, client code modification…) the cap is
// Suggest or Human, and the setting cannot go above it.
//
// What the system cannot finish lands in one work queue, with the role who
// should act, a due time and, where possible, the action already prepared.
// Items close themselves when the condition clears.
//
// Stages run from the scheduler: BOD, intraday, pre-close, EOD, and monthly.

const (
	cAutoPol  = "autopol"
	cAutoRun  = "autorun"
	cWorkItem = "work"
)

// Modes.
const (
	ModeAuto    = "Auto"
	ModeSuggest = "Suggest"
	ModeOff     = "Off"
	ModeHuman   = "Human" // cap only: no automation allowed
)

var modeRank = map[string]int{ModeOff: 0, ModeHuman: 0, ModeSuggest: 1, ModeAuto: 2}

// AutoDef describes an automation.
type AutoDef struct {
	ID      string            `json:"id"`
	Area    string            `json:"area"`
	Name    string            `json:"name"`
	About   string            `json:"about"`
	Stage   string            `json:"stage"` // bod, intraday, preclose, eod, monthly, event
	Cap     string            `json:"cap"`
	CapWhy  string            `json:"cap_why,omitempty"`
	Default string            `json:"default"`
	Params  map[string]string `json:"params,omitempty"` // defaults
	Saves   int               `json:"saves_minutes"`    // rough human minutes saved per run
}

// AutoDefs is every automation, in pipeline order.
var AutoDefs = []AutoDef{
	{"intake", "Operations", "File intake", "Files dropped in the inbox (or pulled from the exchange, depository and bank) are recognised by their columns and loaded in the right order.", "event", ModeAuto, "", ModeAuto, nil, 20},
	{"debit_blocks", "Risk", "Debit blocks", "Blocks buying for clients in debit beyond the firm's limit and unblocks those who paid, before the market opens.", "bod", ModeAuto, "", ModeAuto, nil, 15},
	{"pledges", "Risk", "Margin pledge acceptance", "Accepts margin pledge requests of active, unblocked clients in securities with a haircut; anything else goes to the queue.", "intraday", ModeAuto, "", ModeAuto, nil, 20},
	{"third_party", "Money", "Return third-party credits", "Credits from accounts not registered to the client cannot be used (SEBI); they are returned to source.", "intraday", ModeAuto, "", ModeAuto, nil, 10},
	{"unidentified", "Money", "Identify unknown credits", "Proposes the client for credits with no client code, from the remitter's name and amount; a person confirms with the bank.", "intraday", ModeSuggest, "The bank must confirm the remitter account belongs to the client before funds are credited.", ModeSuggest, nil, 10},
	{"payouts", "Money", "Payout approval", "Approves payout requests up to the limit that the client can withdraw across all segments, if the client is not blocked, high risk or under an open high alert.", "event", ModeAuto, "", ModeAuto, map[string]string{"limit": "200000"}, 30},
	{"payout_batch", "Money", "Payout bank file", "Puts approved payouts in the bank file at the cut-off and asks for the upload.", "eod", ModeAuto, "", ModeAuto, nil, 15},
	{"recon", "Operations", "Reconciliation and break clearing", "Reconciles exchange and OMS trades and clears breaks with no economic difference: trade-number differences, OMS-only fills the exchange rejected, and exchange trades from the firm's own terminals not yet in the OMS feed. Others go to the queue.", "eod", ModeAuto, "", ModeAuto, nil, 45},
	{"billing", "Operations", "Billing", "Bills the day once no reconciliation break affecting money is open; clients with open breaks wait.", "eod", ModeAuto, "", ModeAuto, nil, 20},
	{"settlement", "Operations", "Settlement and derivatives", "Marks carried futures to market, settles expiries, and settles every obligation that is ready; shortfalls go to the queue.", "eod", ModeAuto, "", ModeAuto, nil, 40},
	{"notes", "Operations", "Contract notes", "Generates and emails contract notes within 24 hours; bounced emails go to the queue.", "eod", ModeAuto, "", ModeAuto, nil, 30},
	{"margin_calls", "Risk", "Margin calls", "Messages every client short of margin, and raises MTF margin calls.", "intraday", ModeAuto, "", ModeAuto, nil, 20},
	{"square_off", "Risk", "Square-off of unmet shortfalls", "At the cut-off, closes positions of clients still short of margin, as the risk policy disclosed to clients allows.", "preclose", ModeAuto, "", ModeSuggest, map[string]string{"at": "15:00"}, 20},
	{"mtf_liquidation", "Risk", "MTF liquidation", "Liquidates MTF positions whose margin call was not met or whose pledge was not made by T+1.", "preclose", ModeAuto, "", ModeSuggest, nil, 15},
	{"penalties", "Risk", "Margin penalties", "Posts short-collection penalties at the end of the day.", "eod", ModeAuto, "", ModeAuto, nil, 10},
	{"reports", "Compliance", "Regulatory reports", "Generates and validates every daily report; the upload to the exchange portal is queued until a connection is set up.", "eod", ModeAuto, "", ModeAuto, nil, 40},
	{"surveillance", "Compliance", "Surveillance triage", "Runs the alert rules and gathers evidence on each alert; a compliance officer decides it.", "eod", ModeSuggest, "Alerts are decided, and suspicious transactions reported, by the principal officer under the PMLA rules.", ModeSuggest, nil, 30},
	{"nri", "Compliance", "NRI checks", "Checks NRI trades against the PIS rules and raises alerts.", "eod", ModeAuto, "", ModeAuto, nil, 10},
	{"kyc", "Compliance", "KYC registries and reminders", "Sends periodic-KYC reminders and keeps the KRA upload file; PMLA risk changes the rules suggest go to the queue.", "eod", ModeAuto, "Risk categories are set by the compliance officer.", ModeAuto, nil, 20},
	{"inactive", "Compliance", "Inactive account flagging", "Flags accounts with no activity for the period set by the exchanges and adds them to the UCC upload.", "eod", ModeAuto, "", ModeSuggest, nil, 15},
	{"change_otp", "Clients", "Contact changes verified by OTP", "Email and mobile changes from the portal are applied once the client enters the code sent to the new contact; bank, address and nomination changes need a person.", "event", ModeAuto, "", ModeAuto, nil, 10},
	{"service", "Clients", "Service requests and complaints", "Fulfils 'contract note or statement not received' requests by resending them, assigns complaints in turn, and escalates complaints close to their due date.", "event", ModeAuto, "Complaints are resolved by a person; only service requests are closed automatically.", ModeAuto, nil, 25},
	{"leads", "Clients", "Lead follow-ups", "Queues every lead whose follow-up is due, for its owner.", "bod", ModeAuto, "", ModeAuto, nil, 10},
	{"day_close", "Operations", "Day close", "Closes the day and carries positions forward when every end-of-day check passes; otherwise queues what is blocking.", "eod", ModeAuto, "", ModeAuto, nil, 15},
	{"monthly", "Accounts", "Month-end postings", "Posts interest on debit balances, MTF interest, DP bills and AP payouts for the month just ended.", "monthly", ModeAuto, "", ModeSuggest, nil, 60},
	{"ins_tax_harvest", "Client insights", "Tax-loss harvesting", "From mid-January to the last day that settles in the financial year: clients whose open losses would cut tax on gains already booked this year, or who can still book long-term gains within the ₹1.25 lakh exemption. A reminder goes a week before the last day.", "eod", ModeAuto, "", ModeAuto, map[string]string{"from": "01-15", "min_saving": "1000", "remind_days": "7"}, 20},
	{"ins_buyback", "Client insights", "Buy-back eligibility", "Holders of shares in an open buy-back priced above the market who have not tendered, with their category and entitlement; a reminder two days before it closes.", "eod", ModeAuto, "", ModeAuto, nil, 15},
	{"ins_fo_expiry", "Client insights", "Physical settlement at expiry", "Stock futures, and stock options in the money, expiring within a few business days: they settle by delivery unless closed or rolled over.", "eod", ModeAuto, "", ModeAuto, map[string]string{"days": "3"}, 15},
	{"ins_advance_tax", "Client insights", "Advance tax on gains", "Ten days before each advance-tax instalment, clients with capital gains booked this year.", "eod", ModeAuto, "", ModeAuto, map[string]string{"days": "10", "min_gain": "50000"}, 10},
	{"ins_corp_action", "Client insights", "Dividends and bonuses", "Holders of shares with a dividend, bonus or split whose record date is within ten days.", "eod", ModeAuto, "", ModeAuto, map[string]string{"days": "10"}, 10},
	{"ins_idle_funds", "Client insights", "Idle funds", "Clients with a credit balance, no positions and no trade for a month, who may want to withdraw it.", "eod", ModeAuto, "", ModeSuggest, map[string]string{"min": "25000", "days": "30"}, 10},
	{"ins_pledge_margin", "Client insights", "Margin from holdings", "Derivatives clients whose unpledged holdings could be pledged as margin, after haircuts.", "eod", ModeAuto, "", ModeSuggest, map[string]string{"min": "50000"}, 10},
	{"ins_dormant", "Client insights", "Account turning inactive", "Accounts that the exchanges' rules will flag inactive within 60 days unless there is activity.", "eod", ModeAuto, "", ModeAuto, map[string]string{"days": "60"}, 10},
	{"ins_outcomes", "Client insights", "Insight outcomes", "Marks insights acted on when the client trades, tenders or rolls over within 15 days, and records the turnover.", "eod", ModeAuto, "", ModeAuto, nil, 5},
	{"ras", "Money", "Running account settlement", "On the settlement dates, settles running accounts and sends the payouts.", "eod", ModeAuto, "", ModeAuto, nil, 60},
}

// HumanOnly lists what stays with people, and why.
var HumanOnly = []struct{ Process, Why string }{
	{"Activating a client (KYC verification, in-person verification)", "The KYC regulations and exchange circulars require the intermediary to verify documents and the person."},
	{"Reactivating an inactive account", "The exchange circular of October 2024 requires the client's details to be verified (IPV/VIPV)."},
	{"Deciding surveillance alerts and filing STRs", "The principal officer is responsible under the PMLA rules."},
	{"Verifying physical delivery instruction slips", "The depository rules require the signature to be verified against the record."},
	{"Client code modifications", "Allowed only for genuine errors and reviewed by the exchanges; penalties apply."},
	{"Bill corrections and limit overrides", "Change what a client owes or can trade; need a checker's judgement."},
	{"Bank, address and nomination changes", "Need document verification before the KRA upload."},
	{"MTF enrolment and PMLA risk categories", "Need the signed agreement / the compliance officer's assessment."},
	{"Firm vouchers", "The firm's accounts need a checker under its internal controls."},
}

// AutoPolicy is the firm's setting for one automation.
type AutoPolicy struct {
	ID     string            `json:"id"`
	Mode   string            `json:"mode"`
	Params map[string]string `json:"params"`
	By     string            `json:"by,omitempty"`
	At     string            `json:"at,omitempty"`
}

// AutoPolicyView is a definition with its setting.
type AutoPolicyView struct {
	AutoDef
	Mode     string            `json:"mode"`
	Settings map[string]string `json:"settings"`
	LastRun  *AutoRun          `json:"last_run,omitempty"`
}

func autoDef(id string) (AutoDef, bool) {
	for _, d := range AutoDefs {
		if d.ID == id {
			return d, true
		}
	}
	return AutoDef{}, false
}

func (o *Ops) policy(ctx context.Context, tenant, id string) (AutoDef, string, map[string]string) {
	d, _ := autoDef(id)
	p, _, err := docs.Get[AutoPolicy](ctx, o.Docs, tenant, cAutoPol, id)
	mode := d.Default
	params := map[string]string{}
	for k, v := range d.Params {
		params[k] = v
	}
	if err == nil {
		mode = p.Mode
		for k, v := range p.Params {
			params[k] = v
		}
	}
	if modeRank[mode] > modeRank[d.Cap] {
		mode = d.Cap
	}
	return d, mode, params
}

// SetAutoPolicy changes an automation's mode and settings. The mode cannot
// go above the automation's cap.
func (o *Ops) SetAutoPolicy(ctx context.Context, tenant, actor string, p AutoPolicy) (AutoPolicy, error) {
	if err := o.require(ctx, tenant, actor, "Changing automation", RoleAdmin, RoleCompliance); err != nil {
		return p, err
	}
	d, ok := autoDef(p.ID)
	if !ok {
		return p, invalid("unknown automation %q", p.ID)
	}
	if !contains([]string{ModeAuto, ModeSuggest, ModeOff}, p.Mode) {
		return p, invalid("mode must be Auto, Suggest or Off")
	}
	if modeRank[p.Mode] > modeRank[d.Cap] {
		return p, invalid("%s can be at most %s: %s", d.Name, d.Cap, d.CapWhy)
	}
	if v, ok := p.Params["limit"]; ok {
		if a, err := money.ParseAmount(v); err != nil || a < 0 {
			return p, invalid("limit must be an amount in rupees")
		}
	}
	for _, k := range []string{"from", "to"} {
		if v, ok := p.Params[k]; ok {
			if _, err := time.Parse("01-02", v); err != nil {
				return p, invalid("%s must be a month and day, MM-DD", k)
			}
		}
	}
	for _, k := range []string{"days", "remind_days", "min", "min_saving", "min_gain"} {
		if v, ok := p.Params[k]; ok {
			if n, err := money.ParseDec(strings.ReplaceAll(v, ",", "")); err != nil || n.Sign() < 0 {
				return p, invalid("%s must be a number", k)
			}
		}
	}
	if v, ok := p.Params["at"]; ok {
		if _, err := time.Parse("15:04", v); err != nil {
			return p, invalid("time must be HH:MM")
		}
	}
	p.By, p.At = actor, o.now().Format(time.RFC3339)
	if err := docs.Upsert(ctx, o.Docs, tenant, cAutoPol, p.ID, p, actor); err != nil {
		return p, err
	}
	o.log(ctx, tenant, actor, "autopilot.policy", p.ID, map[string]any{"mode": p.Mode, "params": p.Params})
	return p, nil
}

// ---- work queue ----

// WorkItem is something only a person can finish.
type WorkItem struct {
	ID       string            `json:"id"`
	Key      string            `json:"key"`
	Process  string            `json:"process"`
	Title    string            `json:"title"`
	Detail   string            `json:"detail"`
	UCC      string            `json:"ucc,omitempty"`
	Role     string            `json:"role"`
	View     string            `json:"view"`
	Date     string            `json:"date"`
	Due      string            `json:"due"`
	Status   string            `json:"status"` // Open, Done, Dismissed, Cleared
	Proposed *Proposal         `json:"proposed,omitempty"`
	Created  string            `json:"created"`
	Closed   string            `json:"closed,omitempty"`
	ClosedBy string            `json:"closed_by,omitempty"`
	Note     string            `json:"note,omitempty"`
	Meta     map[string]string `json:"meta,omitempty"`
	Overdue  bool              `json:"overdue"`
}

// Proposal is an action prepared for one-click approval.
type Proposal struct {
	Kind  string            `json:"kind"`
	Label string            `json:"label"`
	Args  map[string]string `json:"args"`
}

type runCtx struct {
	o       *Ops
	ctx     context.Context
	tenant  string
	date    string
	mode    string
	params  map[string]string
	process string
	done    int
	queued  int
	notes   []string
	keys    map[string]bool
}

func (r *runCtx) auto() bool { return r.mode == ModeAuto }

func (r *runCtx) note(f string, a ...any) { r.notes = append(r.notes, fmt.Sprintf(f, a...)) }

// queue raises (or refreshes) a work item.
func (r *runCtx) queue(key, title, detail, ucc, role, view, due string, p *Proposal) {
	r.queued++
	k := r.process + ":" + key
	r.keys[k] = true
	id := "W-" + strings.NewReplacer(":", "-", "|", "-", " ", "", "/", "-").Replace(k)
	if len(id) > 120 {
		id = id[:120]
	}
	now := r.o.now().Format(time.RFC3339)
	if due == "" {
		due = r.date + " 23:59"
	}
	w, _, err := docs.Get[WorkItem](r.ctx, r.o.Docs, r.tenant, cWorkItem, id)
	if err == nil && w.Status != "Open" && w.Status != "Cleared" {
		return // a person already closed it
	}
	w = WorkItem{ID: id, Key: k, Process: r.process, Title: title, Detail: detail, UCC: ucc, Role: role, View: view, Date: r.date, Due: due, Status: "Open", Proposed: p, Created: firstOf(w.Created, now)}
	_ = docs.Upsert(r.ctx, r.o.Docs, r.tenant, cWorkItem, id, w, SystemActor)
}

// closeWork marks a process's item done when it was settled elsewhere.
func (o *Ops) closeWork(ctx context.Context, tenant, actor, process, key, note string) {
	k := process + ":" + key
	id := "W-" + strings.NewReplacer(":", "-", "|", "-", " ", "", "/", "-").Replace(k)
	w, _, err := docs.Get[WorkItem](ctx, o.Docs, tenant, cWorkItem, id)
	if err != nil || w.Status != "Open" {
		return
	}
	w.Status, w.Closed, w.ClosedBy, w.Note = "Done", o.now().Format(time.RFC3339), actor, note
	_ = docs.Upsert(ctx, o.Docs, tenant, cWorkItem, id, w, actor)
}

// clearStale closes this process's open items that no longer apply.
func (r *runCtx) clearStale() {
	ws, _ := docs.List[WorkItem](r.ctx, r.o.Docs, r.tenant, cWorkItem, "")
	for _, w := range ws {
		if w.Process == r.process && w.Status == "Open" && !r.keys[w.Key] && w.Date <= r.date {
			w.Status, w.Closed, w.ClosedBy, w.Note = "Cleared", r.o.now().Format(time.RFC3339), SystemActor, "no longer needed"
			_ = docs.Upsert(r.ctx, r.o.Docs, r.tenant, cWorkItem, w.ID, w, SystemActor)
		}
	}
}

// WorkItems lists the queue, open first.
func (o *Ops) WorkItems(ctx context.Context, tenant, status string) ([]WorkItem, error) {
	ws, err := docs.List[WorkItem](ctx, o.Docs, tenant, cWorkItem, "")
	if err != nil {
		return nil, err
	}
	now := o.now().Format("2006-01-02 15:04")
	out := []WorkItem{}
	for _, w := range ws {
		if status != "" && w.Status != status {
			continue
		}
		w.Overdue = w.Status == "Open" && w.Due < now
		out = append(out, w)
	}
	sort.Slice(out, func(i, j int) bool {
		if (out[i].Status == "Open") != (out[j].Status == "Open") {
			return out[i].Status == "Open"
		}
		return out[i].Due < out[j].Due
	})
	return out, nil
}

// CloseWorkItem dismisses an item, or carries out its proposal.
func (o *Ops) CloseWorkItem(ctx context.Context, tenant, actor, id string, doProposal bool, note string) (WorkItem, error) {
	w, _, err := docs.Get[WorkItem](ctx, o.Docs, tenant, cWorkItem, id)
	if err != nil {
		return w, err
	}
	if w.Status != "Open" {
		return w, fmt.Errorf("%w: %s is %s", ErrState, id, strings.ToLower(w.Status))
	}
	if w.Role != "" {
		if err := o.require(ctx, tenant, actor, w.Title, w.Role); err != nil {
			return w, err
		}
	}
	status := "Dismissed"
	if doProposal {
		if w.Proposed == nil {
			return w, invalid("nothing is prepared for this item; do it on the %s screen", w.View)
		}
		if err := o.carryOut(ctx, tenant, actor, w.Date, *w.Proposed); err != nil {
			return w, err
		}
		status = "Done"
	} else if strings.TrimSpace(note) == "" {
		return w, invalid("say why it is dismissed, or that it was done elsewhere")
	}
	w.Status, w.Closed, w.ClosedBy, w.Note = status, o.now().Format(time.RFC3339), actor, note
	if err := docs.Upsert(ctx, o.Docs, tenant, cWorkItem, w.ID, w, actor); err != nil {
		return w, err
	}
	o.log(ctx, tenant, actor, "work."+strings.ToLower(status), id, map[string]any{"process": w.Process, "note": note})
	return w, nil
}

// carryOut performs a prepared action as the person approving it.
func (o *Ops) carryOut(ctx context.Context, tenant, actor, date string, p Proposal) error {
	a := p.Args
	var err error
	switch p.Kind {
	case "payout.approve":
		_, err = o.DecidePayout(ctx, tenant, actor, date, a["id"], true, "")
	case "pledge.accept":
		_, err = o.DecidePledge(ctx, tenant, actor, a["id"], true, "")
	case "credit.identify":
		_, err = o.IdentifyCredit(ctx, tenant, actor, a["id"], a["ucc"], true)
	case "credit.return":
		_, err = o.ReturnCredit(ctx, tenant, actor, a["id"], "Third-party credit returned to source")
	case "break.resolve":
		_, err = o.App.ResolveBreak(ctx, tenant, actor, a["id"], a["resolution"])
	case "inactive.flag":
		_, err = o.FlagInactive(ctx, tenant, actor, date, strings.Split(a["uccs"], ","))
	case "squareoff":
		_, err = o.SquareOffClients(ctx, tenant, actor, date, strings.Split(a["uccs"], ","))
	case "mtf.liquidate":
		_, err = o.CloseMTF(ctx, tenant, actor, date, a["id"], 0, "liquidated")
	case "report.uploaded":
		_, err = o.ReportStep(ctx, tenant, actor, date, a["code"], "upload", "uploaded by "+actor)
	case "risk.set":
		_, err = o.SetClientRisk(ctx, tenant, actor, a["ucc"], a["risk"], nil, "Raised on the rules' suggestion: "+a["why"])
	case "monthly.post":
		_, err = o.runMonthly(ctx, tenant, actor, a["month"])
	default:
		err = invalid("unknown action %q", p.Kind)
	}
	return err
}

// ---- runs ----

// AutoRun records one automation run.
type AutoRun struct {
	ID      string `json:"id"`
	Policy  string `json:"policy"`
	Stage   string `json:"stage"`
	Date    string `json:"date"`
	At      string `json:"at"`
	Mode    string `json:"mode"`
	Done    int    `json:"done"`
	Queued  int    `json:"queued"`
	Note    string `json:"note"`
	Error   string `json:"error,omitempty"`
	Minutes int    `json:"minutes_saved"`
}

type autoFn func(r *runCtx) error

var autoFns map[string]autoFn

func init() {
	autoFns = map[string]autoFn{
		"debit_blocks": autoDebitBlocks, "pledges": autoPledges, "third_party": autoThirdParty, "unidentified": autoUnidentified,
		"payouts": autoPayouts, "payout_batch": autoPayoutBatch, "recon": autoRecon, "billing": autoBilling, "settlement": autoSettlement,
		"notes": autoNotes, "margin_calls": autoMarginCalls, "square_off": autoSquareOff, "mtf_liquidation": autoMTFLiquidation,
		"penalties": autoPenalties, "reports": autoReports, "surveillance": autoSurveillance, "nri": autoNRI, "kyc": autoKYC,
		"inactive": autoInactive, "service": autoService, "leads": autoLeads, "day_close": autoDayClose, "monthly": autoMonthly, "ras": autoRAS,
		"ins_tax_harvest": autoTaxHarvest, "ins_buyback": autoBuyBack, "ins_fo_expiry": autoFOExpiry, "ins_advance_tax": autoAdvanceTax,
		"ins_corp_action": autoCorpAction, "ins_idle_funds": autoIdleFunds, "ins_pledge_margin": autoPledgeMargin, "ins_dormant": autoDormant,
		"ins_outcomes": autoInsightOutcomes,
	}
}

// RunStage runs every automation of a stage (or one automation by ID) for
// a date. Automations that are Off are skipped.
func (o *Ops) RunStage(ctx context.Context, tenant, actor, date, stage, only string) ([]AutoRun, error) {
	if actor != SystemActor {
		if err := o.require(ctx, tenant, actor, "Running automations", RoleAdmin, RoleChecker); err != nil {
			return nil, err
		}
	}
	if date == "" {
		date = o.businessDate(ctx, tenant)
	}
	var out []AutoRun
	for _, d := range AutoDefs {
		if (only != "" && d.ID != only) || (only == "" && d.Stage != stage) {
			continue
		}
		fn := autoFns[d.ID]
		if fn == nil {
			continue
		}
		_, mode, params := o.policy(ctx, tenant, d.ID)
		if mode == ModeOff {
			continue
		}
		r := &runCtx{o: o, ctx: ctx, tenant: tenant, date: date, mode: mode, params: params, process: d.ID, keys: map[string]bool{}}
		err := fn(r)
		if err == nil {
			r.clearStale()
		}
		run := AutoRun{Policy: d.ID, Stage: d.Stage, Date: date, At: o.now().Format(time.RFC3339), Mode: mode, Done: r.done, Queued: r.queued, Note: strings.Join(r.notes, "; ")}
		if r.done > 0 {
			run.Minutes = d.Saves
		}
		if err != nil {
			run.Error = err.Error()
			r.queue("error", d.Name+" could not run", err.Error(), "", RoleChecker, "autopilot", "", nil)
		}
		n, nerr := o.next(ctx, tenant, "autorun", SystemActor)
		if nerr != nil {
			return out, nerr
		}
		run.ID = fmt.Sprintf("AR-%07d", n)
		_ = docs.Upsert(ctx, o.Docs, tenant, cAutoRun, run.ID, run, SystemActor)
		out = append(out, run)
	}
	o.log(ctx, tenant, actor, "autopilot.stage", stage+":"+date, map[string]any{"runs": len(out), "only": only})
	return out, nil
}

// AutopilotView is the autopilot screen.
type AutopilotView struct {
	Date      string              `json:"date"`
	Policies  []AutoPolicyView    `json:"policies"`
	Work      []WorkItem          `json:"work"`
	Runs      []AutoRun           `json:"runs"`
	HumanOnly []map[string]string `json:"human_only"`
	Done      int                 `json:"done_today"`
	Queued    int                 `json:"queued_today"`
	STP       float64             `json:"stp_pct"`
	Minutes   int                 `json:"minutes_saved_today"`
	Open      int                 `json:"open"`
	Overdue   int                 `json:"overdue"`
	Scheduled bool                `json:"scheduled"`
	Stages    []map[string]string `json:"stages"`
}

// Autopilot builds the screen for a date.
func (o *Ops) Autopilot(ctx context.Context, tenant, date string) (AutopilotView, error) {
	v := AutopilotView{Date: date, Policies: []AutoPolicyView{}, Runs: []AutoRun{}}
	runs, _ := docs.List[AutoRun](ctx, o.Docs, tenant, cAutoRun, "")
	sort.Slice(runs, func(i, j int) bool { return runs[i].ID > runs[j].ID })
	last := map[string]*AutoRun{}
	for i := range runs {
		r := &runs[i]
		if last[r.Policy] == nil {
			last[r.Policy] = r
		}
		if r.Date == date {
			v.Done += r.Done
			v.Minutes += r.Minutes
		}
	}
	if len(runs) > 200 {
		runs = runs[:200]
	}
	v.Runs = runs
	for _, d := range AutoDefs {
		_, mode, params := o.policy(ctx, tenant, d.ID)
		v.Policies = append(v.Policies, AutoPolicyView{AutoDef: d, Mode: mode, Settings: params, LastRun: last[d.ID]})
	}
	ws, err := o.WorkItems(ctx, tenant, "")
	if err != nil {
		return v, err
	}
	v.Work = ws
	for _, w := range ws {
		if w.Status == "Open" {
			v.Open++
			if w.Overdue {
				v.Overdue++
			}
		}
		if w.Date == date {
			v.Queued++
		}
	}
	if v.Done+v.Queued > 0 {
		v.STP = float64(v.Done) * 100 / float64(v.Done+v.Queued)
	}
	v.Scheduled = o.AutopilotScheduled(ctx, tenant)
	js, _ := docs.List[Job](ctx, o.Docs, tenant, cJob, "")
	for _, st := range []struct{ id, stage, name string }{{"JOB-AP-BOD", "bod", "Start of day"}, {"JOB-AP-MID", "intraday", "Intraday"}, {"JOB-AP-PRE", "preclose", "Pre-close"}, {"JOB-AP-EOD", "eod", "End of day"}, {"JOB-AP-MON", "monthly", "Month end"}} {
		row := map[string]string{"stage": st.stage, "name": st.name, "at": ""}
		for _, j := range js {
			if j.ID == st.id {
				row["at"] = j.At
				if j.Active {
					row["at"] += " daily"
				}
			}
		}
		// The latest run of the stage, scheduled or by hand.
		done, queued, failed, last := 0, 0, 0, ""
		for _, r := range runs {
			if r.Stage != st.stage || (st.stage != "monthly" && r.Date != date) {
				continue
			}
			if last == "" {
				last = r.At
			}
			if r.At[:16] != last[:16] {
				continue
			}
			done, queued = done+r.Done, queued+r.Queued
			if r.Error != "" {
				failed++
			}
		}
		if last != "" {
			row["last"], row["status"] = last, "Succeeded"
			row["summary"] = fmt.Sprintf("%d done, %d for people · %s", done, queued, last[11:16])
			if failed > 0 {
				row["status"] = "Failed"
				row["summary"] = fmt.Sprintf("%d failed · %s", failed, row["summary"])
			}
		}
		v.Stages = append(v.Stages, row)
	}
	for _, h := range HumanOnly {
		v.HumanOnly = append(v.HumanOnly, map[string]string{"process": h.Process, "why": h.Why})
	}
	return v, nil
}

// ---- automations ----

func autoDebitBlocks(r *runCtx) error {
	b, u, err := r.o.ApplyDebitBlocks(r.ctx, r.tenant, SystemActor, r.date)
	r.done += b + u
	r.note("%d blocked, %d unblocked", b, u)
	return err
}

func autoPledges(r *runCtx) error {
	ps, err := r.o.PledgeRequests(r.ctx, r.tenant)
	if err != nil {
		return err
	}
	cs, _ := r.o.ClientMap(r.ctx, r.tenant)
	_, look, _ := r.o.Market(r.ctx, r.tenant, r.date)
	for _, p := range ps {
		if p.Status != "Pending" {
			continue
		}
		c := cs[p.UCC]
		i, priced := look.ByISIN(p.ISIN)
		ok := c.Status == "Active" && !c.Blocked && !c.Inactive && priced && i.HaircutPct.Sign() > 0 && i.HaircutPct.Cmp(money.DecInt(100)) < 0
		prop := &Proposal{Kind: "pledge.accept", Label: "Accept pledge", Args: map[string]string{"id": p.ID}}
		if ok && r.auto() {
			if _, err := r.o.DecidePledge(r.ctx, r.tenant, SystemActor, p.ID, true, ""); err != nil {
				return err
			}
			r.done++
			continue
		}
		why := "check the request"
		if !ok {
			why = "client blocked or inactive, or the security has no approved haircut"
			prop = nil
		}
		r.queue(p.ID, "Pledge request "+p.ID+" · "+p.Symbol, fmt.Sprintf("%s pledges %d %s: %s", p.UCC, p.Qty, p.Symbol, why), p.UCC, RoleRisk, "collateral", "", prop)
	}
	return nil
}

func autoThirdParty(r *runCtx) error {
	ts, err := r.o.BankTxns(r.ctx, r.tenant)
	if err != nil {
		return err
	}
	for _, t := range ts {
		if t.Status != "Third-party" {
			continue
		}
		prop := &Proposal{Kind: "credit.return", Label: "Return to source", Args: map[string]string{"id": t.ID}}
		if r.auto() {
			if _, err := r.o.ReturnCredit(r.ctx, r.tenant, SystemActor, t.ID, "Third-party credit returned to source automatically"); err != nil {
				return err
			}
			r.done++
			continue
		}
		r.queue(t.ID, "Third-party credit "+t.Ref, fmt.Sprintf("₹%s from %s (account not registered to %s)", formatINR(t.Amount.Dec()), t.Remitter, t.UCC), t.UCC, RoleChecker, "bank", "", prop)
	}
	return nil
}

func autoUnidentified(r *runCtx) error {
	ts, err := r.o.BankTxns(r.ctx, r.tenant)
	if err != nil {
		return err
	}
	cs, _ := r.o.ClientMap(r.ctx, r.tenant)
	for _, t := range ts {
		if t.Status != "Unidentified" {
			continue
		}
		var guess string
		rem := normName(t.Remitter)
		for _, u := range sortedKeys(cs) {
			if rem != "" && normName(cs[u].Name) == rem {
				guess = u
			}
		}
		var p *Proposal
		detail := fmt.Sprintf("₹%s from %s, ref %s", formatINR(t.Amount.Dec()), t.Remitter, t.Ref)
		if guess != "" {
			p = &Proposal{Kind: "credit.identify", Label: "Credit " + guess + " (bank confirmed)", Args: map[string]string{"id": t.ID, "ucc": guess}}
			detail += "; the remitter's name matches " + guess + " " + cs[guess].Name + ". Confirm the account with the bank first."
		}
		r.queue(t.ID, "Unidentified credit "+t.Ref, detail, guess, RoleChecker, "bank", "", p)
	}
	return nil
}

// payoutRisk says why a payout should not be approved automatically.
func (o *Ops) payoutRisk(ctx context.Context, tenant string, c Client) string {
	switch {
	case c.Blocked:
		return "client is blocked"
	case c.Inactive:
		return "account is inactive"
	case c.Risk == "High":
		return "client is high risk (PMLA)"
	}
	als, _ := o.Alerts(ctx, tenant)
	for _, a := range als {
		if a.UCC == c.UCC && a.Severity == "High" && (a.Status == "Open" || a.Status == "Under review") {
			return "open high alert: " + a.Type
		}
	}
	return ""
}

func autoPayouts(r *runCtx) error {
	limit, _ := money.ParseAmount(r.params["limit"])
	ps, err := r.o.PayoutRequests(r.ctx, r.tenant, r.date)
	if err != nil {
		return err
	}
	cs, _ := r.o.ClientMap(r.ctx, r.tenant)
	for _, p := range ps {
		if p.Status != "Requested" {
			continue
		}
		why := r.o.payoutRisk(r.ctx, r.tenant, cs[p.UCC])
		if why == "" && p.Amount > limit {
			why = fmt.Sprintf("above the automatic limit of ₹%s", formatINR(limit.Dec()))
		}
		if why == "" {
			if w, err := r.o.Withdrawable(r.ctx, r.tenant, r.date, p.UCC); err != nil || p.Amount > w.Available {
				why = "more than the client can withdraw now"
			}
		}
		prop := &Proposal{Kind: "payout.approve", Label: "Approve payout", Args: map[string]string{"id": p.ID}}
		if why == "" && r.auto() {
			if _, err := r.o.DecidePayout(r.ctx, r.tenant, SystemActor, r.date, p.ID, true, ""); err != nil {
				return err
			}
			r.done++
			continue
		}
		if why == "" {
			why = "within the rules"
		}
		r.queue(p.ID, fmt.Sprintf("Payout %s · ₹%s", p.ID, formatINR(p.Amount.Dec())), p.UCC+" "+p.Name+": "+why, p.UCC, RoleChecker, "money", r.date+" 17:00", prop)
	}
	return nil
}

func autoPayoutBatch(r *runCtx) error {
	b, err := r.o.CreatePayoutBatch(r.ctx, r.tenant, SystemActor, r.date)
	if err != nil {
		if errors.Is(err, ErrInvalid) || errors.Is(err, ErrState) {
			r.note("no approved payouts")
			return nil
		}
		return err
	}
	r.done++
	r.note("batch %s: %d payouts, ₹%s", b.ID, len(b.Items), formatINR(b.Total.Dec()))
	r.queue(b.ID, "Upload payout batch "+b.ID+" to the bank", fmt.Sprintf("%d payouts, ₹%s. Upload the file, then enter the UTRs (or connect the bank API to do this automatically).", len(b.Items), formatINR(b.Total.Dec())), "", RoleChecker, "money", r.date+" 18:00", nil)
	return nil
}

func autoRecon(r *runCtx) error {
	ex, err := r.o.exchangeTrades(r.ctx, r.tenant, r.date)
	if err != nil || len(ex) == 0 {
		r.note("no exchange trades loaded yet")
		return err
	}
	if _, err := r.o.App.RunRecon(r.ctx, r.tenant, SystemActor, r.date); err != nil {
		return err
	}
	om, _ := r.o.App.Trades.List(r.ctx, r.tenant, ingest.FromOMS, r.date)
	firm := map[string]bool{"RMS-SQOFF": true}
	for _, t := range om {
		if t.Terminal != "" {
			firm[t.Terminal] = true
		}
	}
	terms := map[string]string{}
	for _, t := range ex {
		terms[t.Exchange+"|"+t.TradeNo] = t.Terminal
	}
	open, err := r.o.App.Breaks.List(r.ctx, r.tenant, r.date, "OPEN")
	if err != nil {
		return err
	}
	for _, b := range open {
		res, role := "", RoleChecker
		switch b.Type {
		case recon.TradeNoMismatch:
			res = "Same trade with a different number in the OMS (matched on time, client, scrip, side, quantity and price); exchange number kept."
		case recon.MissingInExchange:
			res = "OMS fill not confirmed by the exchange (order rejected or cancelled); nothing billed."
		case recon.MissingInOMS:
			if t := terms[b.Exchange+"|"+b.TradeNo]; firm[t] {
				res = "Exchange trade from the firm's own terminal " + t + " not yet in the OMS feed; exchange record is billed."
			}
		case recon.UCCMismatch:
			role = RoleCompliance
		}
		p := &Proposal{Kind: "break.resolve", Label: "Resolve as suggested", Args: map[string]string{"id": b.ID, "resolution": firstOf(res, b.Suggestion)}}
		if res != "" && r.auto() {
			if _, err := r.o.App.ResolveBreak(r.ctx, r.tenant, SystemActor, b.ID, res+" (autopilot)"); err != nil {
				return err
			}
			r.done++
			continue
		}
		if res == "" {
			// Needs checking: the break view has the steps and a note to fill in.
			p = nil
		}
		r.queue(b.ID, fmt.Sprintf("Break %s · %s", b.ID, strings.ReplaceAll(strings.ToLower(string(b.Type)), "_", " ")), b.UCC+" "+b.Instrument+": "+b.Suggestion+" Open it for the steps to check and fix.", b.UCC, role, "recon", r.date+" 18:00", p)
	}
	r.note("%d breaks cleared, %d to review", r.done, r.queued)
	return nil
}

// heldForCCM are clients in an open client-code mismatch: their bills and
// contract notes wait for compliance to decide the modification.
func (o *Ops) heldForCCM(ctx context.Context, tenant, date string) map[string]bool {
	held := map[string]bool{}
	open, _ := o.App.Breaks.List(ctx, tenant, date, "OPEN")
	for _, b := range open {
		if b.Type != recon.UCCMismatch {
			continue
		}
		held[b.UCC] = true
		for _, d := range b.Diffs {
			if strings.EqualFold(d.Field, "ucc") {
				held[d.Exchange], held[d.OMS] = true, true
			}
		}
	}
	return held
}

func autoBilling(r *runCtx) error {
	m, _, err := r.o.Market(r.ctx, r.tenant, r.date)
	if err != nil {
		r.queue("market", "Market data not loaded for "+r.date, "Billing needs settlement prices; load MARKET-DATA.", "", RoleMaker, "files", "", nil)
		return nil
	}
	ex, err := r.o.exchangeTrades(r.ctx, r.tenant, r.date)
	if err != nil || len(ex) == 0 {
		r.note("no exchange trades yet")
		return err
	}
	// Bills follow the exchange record; only clients whose trades may move
	// to another client code wait.
	held := r.o.heldForCCM(r.ctx, r.tenant, r.date)
	var res []app.BillingResult
	if len(held) == 0 {
		b, err := r.o.App.RunBillingOpts(r.ctx, r.tenant, SystemActor, r.date, m.SettlementPrices(), false)
		if err != nil {
			return err
		}
		res = append(res, b)
	} else {
		uccs := map[string]bool{}
		for _, t := range ex {
			if !held[t.UCC] {
				uccs[t.UCC] = true
			}
		}
		for _, u := range sortedKeys(uccs) {
			b, err := r.o.App.RunBillingFor(r.ctx, r.tenant, SystemActor, r.date, m.SettlementPrices(), false, u, "")
			if err != nil {
				return err
			}
			res = append(res, b)
		}
		var hs []string
		for u := range held {
			if u != "" {
				hs = append(hs, u)
			}
		}
		sort.Strings(hs)
		r.note("held for client code decision: %s", strings.Join(hs, ", "))
	}
	posted := 0
	for _, b := range res {
		posted += len(b.Posted)
		for _, c := range b.Conflicts {
			r.queue("conflict:"+c, "Bill conflict "+c, "Trades changed after billing; correct the bill on the Billing page.", "", RoleChecker, "billing", "", nil)
		}
	}
	r.done += posted
	r.note("%d bills posted", posted)
	return nil
}

func autoSettlement(r *runCtx) error {
	st, _ := r.o.EOD(r.ctx, r.tenant, r.date)
	billed := false
	for _, c := range st.Checks {
		billed = billed || (c.Name == "Bills posted" && c.OK)
	}
	if !billed {
		r.note("waiting for billing")
		return nil
	}
	dv, err := r.o.SettleDerivatives(r.ctx, r.tenant, SystemActor, r.date)
	if err != nil {
		return err
	}
	n, err := r.o.SettleAllReady(r.ctx, r.tenant, SystemActor, r.date)
	if err != nil {
		return err
	}
	r.done += len(dv.MTM) + len(dv.Expiry) + n
	ob, err := r.o.Obligations(r.ctx, r.tenant, r.date)
	if err != nil {
		return err
	}
	for _, x := range ob.Obligations {
		if x.Status == "Short delivery" || x.Status == "Funds shortfall" || x.Status == "Unpledge & pay-in" {
			r.queue(x.Key, x.Status+" · "+x.UCC, fmt.Sprintf("%s %s %s: net %d, free %d, funds ₹%s", x.Name, x.Exchange, x.Symbol, x.NetQty, x.Free, formatINR(x.Funds)), x.UCC, RoleChecker, "settlement", "", nil)
		}
	}
	r.note("%d MTM, %d expiry, %d obligations settled", len(dv.MTM), len(dv.Expiry), n)
	return nil
}

func autoNotes(r *runCtx) error {
	st, _ := r.o.EOD(r.ctx, r.tenant, r.date)
	for _, c := range st.Checks {
		if c.Name == "Bills posted" && !c.OK {
			r.note("waiting for billing")
			return nil
		}
	}
	g, err := r.o.generateNotes(r.ctx, r.tenant, SystemActor, r.date, r.o.heldForCCM(r.ctx, r.tenant, r.date))
	if err != nil {
		return err
	}
	s, b, err := r.o.SendNotes(r.ctx, r.tenant, SystemActor, r.date, "")
	if err != nil {
		return err
	}
	r.done += g + s
	ns, _ := r.o.Notes(r.ctx, r.tenant, r.date)
	for _, n := range ns {
		if n.Status == "Bounced" {
			r.queue(n.UCC, "Contract note bounced · "+n.UCC, n.Name+": "+n.Email+" does not accept mail. Get the right email (change request) and resend within 24 hours.", n.UCC, RoleMaker, "notes", addDays(r.date, 1)+" 12:00", nil)
		}
	}
	r.note("%d generated, %d sent, %d bounced", g, s, b)
	return nil
}

func autoMarginCalls(r *runCtx) error {
	rv, err := r.o.Risk(r.ctx, r.tenant, r.date)
	if err != nil {
		return err
	}
	for _, x := range rv.Rows {
		if x.Status != "Breach" {
			continue
		}
		key := "margin.shortfall:" + r.date + ":" + x.UCC
		if _, _, err := docs.Get[AutoRun](r.ctx, r.o.Docs, r.tenant, cAutoRun, "sent:"+key); err == nil {
			continue
		}
		r.o.notify(r.ctx, r.tenant, SystemActor, "margin.shortfall", x.UCC, map[string]string{"shortfall": formatINR(x.Free.Neg()), "date": r.date})
		_ = docs.Upsert(r.ctx, r.o.Docs, r.tenant, cAutoRun, "sent:"+key, AutoRun{ID: "sent:" + key}, SystemActor)
		r.done++
	}
	n, err := r.o.RaiseMTFCalls(r.ctx, r.tenant, SystemActor, r.date)
	r.done += n
	r.note("%d clients called", r.done)
	return err
}

func autoSquareOff(r *runCtx) error {
	at := r.params["at"]
	if at != "" && r.o.now().Format("2006-01-02") == r.date && r.o.now().Format("15:04") < at {
		r.note("runs at %s", at)
		return nil
	}
	lv, err := r.o.LiveRisk(r.ctx, r.tenant, r.date, "")
	if err != nil {
		return err
	}
	var uccs []string
	for _, x := range lv.Rows {
		if x.Status == "Breach" && len(lv.Positions[x.UCC]) > 0 {
			uccs = append(uccs, x.UCC)
		}
	}
	if len(uccs) == 0 {
		r.note("no breaches with open positions")
		return nil
	}
	if r.auto() {
		res, err := r.o.SquareOffClients(r.ctx, r.tenant, SystemActor, r.date, uccs)
		if err != nil {
			return err
		}
		r.done += len(res.Instructions)
		r.note("%d orders for %d clients", len(res.Instructions), len(uccs))
		return nil
	}
	r.queue("sq", fmt.Sprintf("Square off %d clients short of margin", len(uccs)), strings.Join(uccs, ", "), "", RoleRisk, "live", r.date+" "+firstOf(at, "15:00"), &Proposal{Kind: "squareoff", Label: "Square off all", Args: map[string]string{"uccs": strings.Join(uccs, ",")}})
	return nil
}

func autoMTFLiquidation(r *runCtx) error {
	v, err := r.o.MTF(r.ctx, r.tenant, r.date)
	if err != nil {
		return err
	}
	for _, p := range v.Positions {
		if p.State != "Liquidate" {
			continue
		}
		prop := &Proposal{Kind: "mtf.liquidate", Label: "Liquidate and repay", Args: map[string]string{"id": p.ID}}
		if r.auto() {
			// Selling goes to the front end; the repayment follows once sold.
			r.queue(p.ID, "MTF liquidation "+p.ID+" · "+p.UCC, p.Why+". Sell order in the liquidation file; repay once sold.", p.UCC, RoleRisk, "mtf", "", prop)
			r.done++
			continue
		}
		r.queue(p.ID, "MTF liquidation "+p.ID+" · "+p.UCC, p.Why, p.UCC, RoleRisk, "mtf", "", prop)
	}
	return nil
}

func autoPenalties(r *runCtx) error {
	n, err := r.o.PostPenalties(r.ctx, r.tenant, SystemActor, r.date)
	if err != nil && errors.Is(err, ErrState) {
		r.note("already posted")
		return nil
	}
	r.done += n
	r.note("%d penalties posted", n)
	return err
}

func autoReports(r *runCtx) error {
	rs, err := r.o.Reports(r.ctx, r.tenant, r.date)
	if err != nil {
		return err
	}
	for _, x := range rs {
		if x.Freq != "Daily" {
			continue
		}
		if x.Status == "Not generated" || x.Status == "Generated" {
			s, err := r.o.ReportStep(r.ctx, r.tenant, SystemActor, r.date, x.Code, "validate", "")
			if err != nil {
				r.queue(x.Code, "Report "+x.Name+" failed validation", err.Error(), "", RoleCompliance, "reports", "", nil)
				continue
			}
			x = s
			r.done++
		}
		if x.Status == "Validated" {
			r.queue(x.Code, "Upload "+x.Name+" to "+x.To, fmt.Sprintf("%d rows, validated. Due %s.", x.Rows, x.Due), "", RoleCompliance, "reports", r.date+" 23:00",
				&Proposal{Kind: "report.uploaded", Label: "Mark uploaded", Args: map[string]string{"code": x.Code}})
		}
	}
	return nil
}

func autoSurveillance(r *runCtx) error {
	n, err := r.o.RunRules(r.ctx, r.tenant, SystemActor, r.date)
	if err != nil {
		return err
	}
	r.done += n
	als, _ := r.o.Alerts(r.ctx, r.tenant)
	cs, _ := r.o.ClientMap(r.ctx, r.tenant)
	pm, _ := r.o.PMLA(r.ctx, r.tenant, r.date)
	turn := map[string]money.Dec{}
	for _, x := range pm.Rows {
		turn[x.UCC] = x.Turnover
	}
	count := map[string]int{}
	for _, a := range als {
		count[a.UCC]++
	}
	for _, a := range als {
		if a.Status != "Open" && a.Status != "Under review" {
			continue
		}
		c := cs[a.UCC]
		ev := fmt.Sprintf("%s. Evidence: income %s, turnover this FY ₹%s, risk %s, %d alert(s) on this client, KRA %s.", a.Detail, firstOf(c.Income, "not declared"), formatINR(turn[a.UCC]), firstOf(c.Risk, "—"), count[a.UCC], firstOf(c.KRAStatus, "not loaded"))
		due := addDays(firstOf(a.Date, r.date), 30)
		if a.Severity == "High" {
			due = addDays(firstOf(a.Date, r.date), 7)
		}
		r.queue(a.ID, a.Severity+" alert · "+a.Type, ev, a.UCC, RoleCompliance, "surv", due+" 18:00", nil)
	}
	return nil
}

func autoNRI(r *runCtx) error {
	v, err := r.o.NRI(r.ctx, r.tenant, SystemActor, r.date)
	if err != nil {
		return err
	}
	r.done += len(v.Rows)
	for _, x := range v.Rows {
		if x.Missing {
			r.queue(x.UCC, "PIS details missing · "+x.UCC, x.Name+" is an NRI with no PIS bank details on record.", x.UCC, RoleCompliance, "nri", "", nil)
		}
	}
	r.note("%d NRI clients checked, %d rule breaks raised as alerts", len(v.Rows), len(v.Violations))
	return nil
}

func autoKYC(r *runCtx) error {
	v, err := r.o.PMLA(r.ctx, r.tenant, r.date)
	if err != nil {
		return err
	}
	for _, x := range v.Rows {
		if x.Review {
			r.queue("risk:"+x.UCC, "Review PMLA risk · "+x.UCC, fmt.Sprintf("%s is %s; the rules suggest %s: %s", x.Name, x.Risk, x.Suggested, strings.Join(x.Why, "; ")), x.UCC, RoleCompliance, "kyc", "",
				&Proposal{Kind: "risk.set", Label: "Set to " + x.Suggested, Args: map[string]string{"ucc": x.UCC, "risk": x.Suggested, "why": strings.Join(x.Why, "; ")}})
		}
		if len(x.Debarred) > 0 && !x.Blocked {
			r.queue("debarred:"+x.UCC, "Possible debarred entity · "+x.UCC, x.Name+" matches "+strings.Join(x.Debarred, "; ")+" by name. Confirm or clear.", x.UCC, RoleCompliance, "kyc", "", nil)
		}
	}
	// Reminders once a week per client.
	key := "kyc-reminders:" + r.date[:8]
	if _, _, err := docs.Get[AutoRun](r.ctx, r.o.Docs, r.tenant, cAutoRun, key); err != nil {
		for _, x := range v.Rows {
			if x.ReKYCDue != "" && x.ReKYCDue <= addDays(r.date, 30) {
				r.o.notify(r.ctx, r.tenant, SystemActor, "rekyc.due", x.UCC, map[string]string{"due": x.ReKYCDue})
				r.done++
			}
		}
		_ = docs.Upsert(r.ctx, r.o.Docs, r.tenant, cAutoRun, key, AutoRun{ID: key}, SystemActor)
	}
	return nil
}

func autoInactive(r *runCtx) error {
	v, err := r.o.Dormancy(r.ctx, r.tenant, r.date)
	if err != nil {
		return err
	}
	var due []string
	for _, x := range v.Rows {
		if x.State == "Due" {
			due = append(due, x.UCC)
		}
	}
	if len(due) == 0 {
		return nil
	}
	if r.auto() {
		n, err := r.o.FlagInactive(r.ctx, r.tenant, SystemActor, r.date, due)
		r.done += n
		r.note("%d accounts flagged inactive", n)
		return err
	}
	r.queue("due", fmt.Sprintf("Flag %d accounts inactive", len(due)), strings.Join(due, ", "), "", RoleCompliance, "inactive", "", &Proposal{Kind: "inactive.flag", Label: "Flag all", Args: map[string]string{"uccs": strings.Join(due, ",")}})
	return nil
}

func autoService(r *runCtx) error {
	ts, err := r.o.Tickets(r.ctx, r.tenant, r.date)
	if err != nil {
		return err
	}
	staff := []string{}
	us, _ := r.o.Users(r.ctx, r.tenant)
	for _, u := range us {
		if u.Active && (u.Role == RoleMaker || u.Role == RoleChecker) {
			staff = append(staff, u.ID)
		}
	}
	sort.Strings(staff)
	turn := 0
	for _, t := range ts {
		if t.Status == "Resolved" || t.Status == "Closed" {
			continue
		}
		if t.Kind != KindComplaint && t.Category == "Contract note / statement not received" && t.Status == "Open" {
			if err := r.o.fulfilStatementRequest(r.ctx, r.tenant, r.date, t); err != nil {
				return err
			}
			r.done++
			continue
		}
		if t.Assignee == "" && len(staff) > 0 {
			if _, err := r.o.TicketUpdate(r.ctx, r.tenant, SystemActor, t.ID, "", false, "", staff[turn%len(staff)]); err != nil {
				return err
			}
			turn++
			r.done++
		}
		if t.Kind == KindComplaint && addDays(r.date, 3) >= t.Due {
			r.queue(t.ID, "Complaint due "+t.Due+" · "+t.ID, t.Subject+" ("+t.UCC+"). Resolve before the due date.", t.UCC, RoleCompliance, "helpdesk", t.Due+" 18:00", nil)
		}
	}
	return nil
}

// fulfilStatementRequest resends the latest contract note and the month's
// statement, and resolves the request.
func (o *Ops) fulfilStatementRequest(ctx context.Context, tenant, date string, t Ticket) error {
	ns, _ := docs.List[ContractNote](ctx, o.Docs, tenant, cNote, "")
	var last *ContractNote
	for i := range ns {
		if ns[i].UCC == t.UCC && (last == nil || ns[i].Date+ns[i].No > last.Date+last.No) {
			last = &ns[i]
		}
	}
	c, err := o.Client(ctx, tenant, t.UCC)
	if err != nil {
		return err
	}
	msg := ""
	if last != nil {
		if _, _, err := o.SendNotes(ctx, tenant, SystemActor, last.Date, t.UCC); err != nil {
			return err
		}
		msg = fmt.Sprintf("contract note %s for %s", last.No, last.Date)
	}
	from := date[:8] + "01"
	st, err := o.App.Ledger.Statement(ctx, tenant, ledgerClient(t.UCC), from, date)
	if err == nil {
		cl, dc := st.Closing, "Cr"
		if cl > 0 {
			dc = "Dr"
		} else {
			cl = -cl
		}
		o.notify(ctx, tenant, SystemActor, "statement.monthly", t.UCC, map[string]string{"from": from, "to": date, "closing": "₹" + formatINR(cl.Dec()) + " " + dc})
		if msg != "" {
			msg += " and "
		}
		msg += "your statement of account from " + from + " to " + date
	}
	_, err = o.TicketUpdate(ctx, tenant, SystemActor, t.ID, "We have emailed "+msg+" to "+maskEmail(c.Email)+". If they do not arrive, reply here and we will check your email address.", false, "Resolved", "")
	return err
}

func autoLeads(r *runCtx) error {
	ls, err := docs.List[Lead](r.ctx, r.o.Docs, r.tenant, cLead, "")
	if err != nil {
		return err
	}
	for _, l := range ls {
		if l.Stage == "Converted" || l.Stage == "Lost" {
			continue
		}
		if l.Next != "" && l.Next <= r.date {
			r.queue(l.ID, "Follow up "+l.Name, fmt.Sprintf("%s lead from %s, stage %s, due %s", l.ID, l.Source, l.Stage, l.Next), "", "", "crm", l.Next+" 18:00", nil)
		}
	}
	return nil
}

func autoDayClose(r *runCtx) error {
	st, err := r.o.EOD(r.ctx, r.tenant, r.date)
	if err != nil {
		return err
	}
	if st.Closed {
		r.note("already closed")
		return nil
	}
	var bad []string
	for _, c := range st.Checks {
		if !c.OK {
			bad = append(bad, c.Name+": "+c.Detail)
		}
	}
	if len(bad) > 0 || st.Previous != "" {
		if st.Previous != "" {
			bad = append(bad, st.Previous)
		}
		r.queue("close", "Day "+r.date+" cannot close yet", strings.Join(bad, "; "), "", RoleChecker, "dashboard", r.date+" 23:00", nil)
		return nil
	}
	if _, err := r.o.CloseDay(r.ctx, r.tenant, SystemActor, r.date, ""); err != nil {
		return err
	}
	r.done++
	r.note("closed; positions carried forward")
	return nil
}

func autoRAS(r *runCtx) error {
	set, _ := r.o.Settings(r.ctx, r.tenant)
	if r.date != set.RASMonthly && r.date != set.RASQuarterly {
		return nil
	}
	b, err := r.o.RunRAS(r.ctx, r.tenant, SystemActor, r.date, r.date)
	if err != nil {
		if errors.Is(err, ErrState) {
			r.note("already settled")
			return nil
		}
		return err
	}
	r.done += len(b.Items)
	r.note("%d clients settled", len(b.Items))
	return nil
}

func autoMonthly(r *runCtx) error {
	if !strings.HasSuffix(r.date, "-01") && r.date[8:] > "05" {
		return nil
	}
	prev := addDays(r.date[:8]+"01", -1)[:7]
	key := "monthly:" + prev
	if _, _, err := docs.Get[AutoRun](r.ctx, r.o.Docs, r.tenant, cAutoRun, key); err == nil {
		return nil
	}
	if !r.auto() {
		r.queue(prev, "Month-end postings for "+prev, "Interest on debit balances, MTF interest, DP bills and AP payouts.", "", RoleChecker, "autopilot", "", &Proposal{Kind: "monthly.post", Label: "Post all", Args: map[string]string{"month": prev}})
		return nil
	}
	s, err := r.o.runMonthly(r.ctx, r.tenant, SystemActor, prev)
	if err != nil {
		return err
	}
	r.done++
	r.note("%s", s)
	return nil
}

func (o *Ops) runMonthly(ctx context.Context, tenant, actor, month string) (string, error) {
	from, to := month+"-01", monthEnd(month)
	_, n1, err := o.PostInterest(ctx, tenant, actor, from, to)
	if err != nil && !errors.Is(err, ErrInvalid) {
		return "", err
	}
	mi, err := o.PostMTFInterest(ctx, tenant, actor, to, "")
	if err != nil {
		return "", err
	}
	bills, err := o.RunDPBilling(ctx, tenant, actor, month)
	if err != nil {
		return "", err
	}
	n4, err := o.PostAPPayouts(ctx, tenant, actor, from, to)
	if err != nil && !errors.Is(err, ErrInvalid) && !errors.Is(err, ErrState) {
		return "", err
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cAutoRun, "monthly:"+month, AutoRun{ID: "monthly:" + month}, SystemActor)
	return fmt.Sprintf("%s: interest for %d clients, MTF interest ₹%s, %d DP bills, %d AP payouts", month, n1, formatINR(mi.Dec()), len(bills), n4), nil
}

func ledgerClient(ucc string) string { return ledger.ClientAccount(ucc) }
