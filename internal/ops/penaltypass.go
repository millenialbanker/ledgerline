package ops

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// ---- passing a margin penalty to a client ----
//
// A clearing corporation's penalty for short or non-collection of upfront
// margin is the broker's. NSE/INSP/64315 (1 Oct 2024; for penalties levied
// on or after 1 Nov 2024) lets the broker pass it to the client only when
// the shortfall is the client's doing, in two cases:
//
//   - a cheque the client gave the broker was dishonoured; or
//   - the margin went up because the client changed a hedge or a leg of it
//     expired, and the broker's risk management policy, notified to clients
//     at least 7 days before, says this can happen.
//
// The client must get the supporting documents and the broker must keep an
// audit trail. So nothing passes a penalty on by itself: Autopilot only
// prepares a work item per penalty; a Compliance officer or Risk manager
// picks the reason, gives the cheque or the policy clause and attaches the
// evidence; a different person approves it; only then is the client's
// ledger debited and the client sent a signed letter with the evidence.

const (
	cPenaltyPass = "penaltypass"
	srcPenaltyEv = "PENALTY-EVIDENCE"
	// wPenaltyPass is the work queue process of the decisions and
	// approvals. It is not an Autopilot process, so a run never clears an
	// item a person still has to decide.
	wPenaltyPass = "penalty_pass"

	// PenaltyChequeBounce: the client's cheque was dishonoured.
	PenaltyChequeBounce = "cheque_bounce"
	// PenaltyHedgeBreak: a hedge changed or a leg expired, as the risk
	// management policy disclosed.
	PenaltyHedgeBreak = "hedge_break"

	// PenaltyPassFrom is the first penalty date the 2024 rule covers;
	// before it, earlier circulars barred passing penalties on at all.
	PenaltyPassFrom = "2024-11-01"

	// MaxPenaltyEvidence is the largest evidence file accepted.
	MaxPenaltyEvidence = 10 << 20
)

// PenaltyReasons are the only reasons a penalty may be passed on.
var PenaltyReasons = map[string]string{
	PenaltyChequeBounce: "Cheque given by the client was dishonoured",
	PenaltyHedgeBreak:   "Hedge changed or a hedge leg expired, as disclosed in the risk management policy",
}

// PenaltyPassRequest is what Compliance or Risk give for one penalty.
type PenaltyPassRequest struct {
	Date   string `json:"date"`
	UCC    string `json:"ucc"`
	Reason string `json:"reason"` // cheque_bounce or hedge_break
	// Cheque: the bounced receipt (its ID or the cheque number).
	Cheque string `json:"cheque,omitempty"`
	// Hedge break: the clause of the risk management policy and the date
	// clients were notified of it.
	PolicyClause     string   `json:"policy_clause,omitempty"`
	PolicyNotifiedOn string   `json:"policy_notified_on,omitempty"`
	Evidence         []string `json:"evidence"` // uploaded evidence file IDs
	Note             string   `json:"note,omitempty"`
}

// PenaltyPass is one penalty passed (or proposed to be passed) to a client.
type PenaltyPass struct {
	ID               string         `json:"id"` // date|UCC
	Date             string         `json:"date"`
	UCC              string         `json:"ucc"`
	Name             string         `json:"name"`
	Short            money.Dec      `json:"short"`
	SlabPct          money.Dec      `json:"slab_pct"`
	Penalty          money.Dec      `json:"penalty"`
	Reason           string         `json:"reason"`
	ReasonText       string         `json:"reason_text"`
	ChequeReceipt    string         `json:"cheque_receipt,omitempty"`
	ChequeNo         string         `json:"cheque_no,omitempty"`
	PolicyClause     string         `json:"policy_clause,omitempty"`
	PolicyNotifiedOn string         `json:"policy_notified_on,omitempty"`
	Evidence         []PeriodicFile `json:"evidence"`
	Note             string         `json:"note,omitempty"`
	Status           string         `json:"status"` // Waiting for approval, Posted, Rejected
	Maker            string         `json:"maker"`
	MadeAt           string         `json:"made_at"`
	Checker          string         `json:"checker,omitempty"`
	CheckedAt        string         `json:"checked_at,omitempty"`
	CheckerNote      string         `json:"checker_note,omitempty"`
	Entry            string         `json:"entry,omitempty"`
	Letter           string         `json:"letter,omitempty"` // dispatch ID of the letter to the client
}

// Ref is the reference printed for the client: the cheque or the clause.
func (p PenaltyPass) Ref() string {
	if p.Reason == PenaltyChequeBounce {
		return "cheque " + firstOf(p.ChequeNo, p.ChequeReceipt) + " (receipt " + p.ChequeReceipt + ")"
	}
	return "risk management policy " + p.PolicyClause + ", notified " + fmtDay(p.PolicyNotifiedOn)
}

// EvidenceNames lists the evidence files.
func (p PenaltyPass) EvidenceNames() string {
	var n []string
	for _, f := range p.Evidence {
		n = append(n, f.Name+" ("+f.ID+")")
	}
	return strings.Join(n, ", ")
}

func penaltyPassID(date, ucc string) string { return date + "|" + strings.ToUpper(ucc) }

func (o *Ops) penaltyPass(ctx context.Context, tenant, date, ucc string) (PenaltyPass, bool) {
	p, _, err := docs.Get[PenaltyPass](ctx, o.Docs, tenant, cPenaltyPass, penaltyPassID(date, ucc))
	return p, err == nil
}

// PenaltyPasses lists the passes for a date ("" for all), newest first.
func (o *Ops) PenaltyPasses(ctx context.Context, tenant, date string) ([]PenaltyPass, error) {
	ps, err := docs.List[PenaltyPass](ctx, o.Docs, tenant, cPenaltyPass, "")
	if err != nil {
		return nil, err
	}
	out := []PenaltyPass{}
	for _, p := range ps {
		if date == "" || p.Date == date {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

// UploadPenaltyEvidence keeps a document supporting a pass-through: the
// bank's cheque return memo, the risk policy extract and its notice.
func (o *Ops) UploadPenaltyEvidence(ctx context.Context, tenant, actor, name string, data []byte) (PeriodicFile, error) {
	if err := o.require(ctx, tenant, actor, "Passing a margin penalty to a client", RoleCompliance, RoleRisk); err != nil {
		return PeriodicFile{}, err
	}
	if len(data) == 0 || len(data) > MaxPenaltyEvidence {
		return PeriodicFile{}, invalid("the file must be between 1 byte and 10 MB")
	}
	switch ct := http.DetectContentType(data); {
	case ct == "application/pdf", ct == "image/png", ct == "image/jpeg":
	default:
		return PeriodicFile{}, invalid("upload a PDF or a PNG or JPEG image (this looks like %s)", ct)
	}
	name = strings.TrimSpace(strings.NewReplacer("/", "_", "\\", "_").Replace(name))
	if name == "" {
		name = "evidence"
	}
	rf, err := o.App.Ingest.Raw.Put(ctx, ingest.RawFile{Tenant: tenant, Source: srcPenaltyEv, Name: name, SHA256: sha256Hex(data), Size: int64(len(data)), Content: data, ReceivedAt: o.now()})
	if err != nil {
		return PeriodicFile{}, err
	}
	o.log(ctx, tenant, actor, "margin.penalty.evidence", rf.ID, map[string]any{"name": name, "sha256": rf.SHA256})
	return PeriodicFile{ID: rf.ID, Name: name}, nil
}

// PenaltyEvidence returns an evidence file.
func (o *Ops) PenaltyEvidence(ctx context.Context, tenant, actor, id string) (ingest.RawFile, error) {
	if err := o.require(ctx, tenant, actor, "Viewing margin penalty evidence", RoleCompliance, RoleRisk, RoleChecker); err != nil {
		return ingest.RawFile{}, err
	}
	f, err := o.App.Ingest.Raw.Get(ctx, tenant, id)
	if err != nil {
		return f, err
	}
	if f.Source != srcPenaltyEv {
		return ingest.RawFile{}, fmt.Errorf("%w: file %s", ErrNotFound, id)
	}
	return f, nil
}

// ProposePenaltyPass records the reason and evidence for passing one
// penalty to a client and asks a checker to approve it. Nothing is posted.
func (o *Ops) ProposePenaltyPass(ctx context.Context, tenant, actor string, q PenaltyPassRequest) (PenaltyPass, error) {
	if actor == SystemActor {
		return PenaltyPass{}, fmt.Errorf("%w: a person decides whether a penalty is passed on", ErrForbidden)
	}
	if err := o.require(ctx, tenant, actor, "Passing a margin penalty to a client", RoleCompliance, RoleRisk); err != nil {
		return PenaltyPass{}, err
	}
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return PenaltyPass{}, err
	}
	if !set.PenaltyPassThrough {
		return PenaltyPass{}, fmt.Errorf("%w: penalty pass-through is off in Settings: the firm bears the penalty", ErrState)
	}
	if !validDate(q.Date) {
		return PenaltyPass{}, invalid("give the penalty date (YYYY-MM-DD)")
	}
	if q.Date < PenaltyPassFrom {
		return PenaltyPass{}, invalid("penalties before %s cannot be passed to clients (NSE circulars of 2020–2022)", fmtDay(PenaltyPassFrom))
	}
	q.UCC = strings.ToUpper(strings.TrimSpace(q.UCC))
	if old, ok := o.penaltyPass(ctx, tenant, q.Date, q.UCC); ok && old.Status != "Rejected" {
		return old, fmt.Errorf("%w: the penalty of %s on %s is already %s", ErrState, q.UCC, q.Date, strings.ToLower(old.Status))
	}
	rep, err := o.Margins(ctx, tenant, q.Date)
	if err != nil {
		return PenaltyPass{}, err
	}
	var row *MarginRow
	for i := range rep.Rows {
		if rep.Rows[i].UCC == q.UCC {
			row = &rep.Rows[i]
		}
	}
	if row == nil || row.Penalty.Round(money.UnitPaisa) <= 0 {
		return PenaltyPass{}, fmt.Errorf("%w: %s has no short-collection penalty on %s", ErrNotFound, q.UCC, q.Date)
	}
	p := PenaltyPass{ID: penaltyPassID(q.Date, q.UCC), Date: q.Date, UCC: q.UCC, Name: row.Name, Short: row.Short, SlabPct: row.SlabPct, Penalty: row.Penalty,
		Reason: q.Reason, ReasonText: PenaltyReasons[q.Reason], Note: strings.TrimSpace(q.Note), Status: "Waiting for approval", Maker: actor, MadeAt: o.now().Format(time.RFC3339), Evidence: []PeriodicFile{}}
	switch q.Reason {
	case PenaltyChequeBounce:
		ref := strings.TrimSpace(q.Cheque)
		if ref == "" {
			return PenaltyPass{}, invalid("give the dishonoured cheque (its receipt or cheque number)")
		}
		rs, err := o.Receipts(ctx, tenant)
		if err != nil {
			return PenaltyPass{}, err
		}
		for _, r := range rs {
			if r.UCC == q.UCC && r.Status == "Bounced" && (r.ID == ref || r.Ref == ref) && r.Date <= q.Date {
				p.ChequeReceipt, p.ChequeNo = r.ID, r.Ref
			}
		}
		if p.ChequeReceipt == "" {
			return PenaltyPass{}, invalid("no dishonoured cheque %s of %s given on or before %s: record the bounce on Receipts first", ref, q.UCC, fmtDay(q.Date))
		}
	case PenaltyHedgeBreak:
		p.PolicyClause, p.PolicyNotifiedOn = strings.TrimSpace(q.PolicyClause), strings.TrimSpace(q.PolicyNotifiedOn)
		if p.PolicyClause == "" {
			return PenaltyPass{}, invalid("give the clause of the risk management policy that discloses the hedge break")
		}
		if !validDate(p.PolicyNotifiedOn) {
			return PenaltyPass{}, invalid("give the date clients were notified of that clause (YYYY-MM-DD)")
		}
		// Notice of the policy at least 7 days before the rule's start or
		// the penalty date, whichever is later.
		if latest := addDays(q.Date, -7); p.PolicyNotifiedOn > latest {
			return PenaltyPass{}, invalid("the policy clause must have been notified to clients at least 7 days before the penalty date (by %s)", fmtDay(latest))
		}
	default:
		return PenaltyPass{}, invalid("a penalty can be passed on only for a dishonoured cheque (%s) or a disclosed hedge break (%s)", PenaltyChequeBounce, PenaltyHedgeBreak)
	}
	for _, id := range q.Evidence {
		f, err := o.App.Ingest.Raw.Get(ctx, tenant, strings.TrimSpace(id))
		if err != nil || f.Source != srcPenaltyEv {
			return PenaltyPass{}, invalid("evidence file %s is not an uploaded penalty document", id)
		}
		p.Evidence = append(p.Evidence, PeriodicFile{ID: f.ID, Name: f.Name})
	}
	if len(p.Evidence) == 0 {
		return PenaltyPass{}, invalid("attach the supporting documents: they go to the client")
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cPenaltyPass, p.ID, p, actor); err != nil {
		return PenaltyPass{}, err
	}
	o.closeWork(ctx, tenant, actor, wPenaltyPass, p.ID, "reason given by "+actor)
	r := &runCtx{o: o, ctx: ctx, tenant: tenant, date: q.Date, process: wPenaltyPass, keys: map[string]bool{}}
	r.queue("approve:"+p.ID, fmt.Sprintf("Approve passing ₹%s penalty to %s", formatINR(p.Penalty), p.UCC),
		fmt.Sprintf("%s for %s: %s; evidence %s. Proposed by %s.", p.ReasonText, fmtDay(p.Date), p.Ref(), p.EvidenceNames(), actor), p.UCC, RoleChecker, "rms", "",
		&Proposal{Kind: "penalty.pass", Label: "Approve and post", Args: map[string]string{"id": p.ID}})
	o.log(ctx, tenant, actor, "margin.penalty.proposed", p.ID, map[string]any{"reason": p.Reason, "ref": p.Ref(), "evidence": p.EvidenceNames(), "penalty": p.Penalty.String()})
	return p, nil
}

// DecidePenaltyPass approves (posts) or rejects a proposed pass-through.
// The checker must differ from the person who proposed it.
func (o *Ops) DecidePenaltyPass(ctx context.Context, tenant, actor, id string, approve bool, note string) (PenaltyPass, error) {
	if actor == SystemActor {
		return PenaltyPass{}, fmt.Errorf("%w: a person approves passing a penalty on", ErrForbidden)
	}
	if err := o.require(ctx, tenant, actor, "Approving a margin penalty pass-through", RoleChecker, RoleRisk, RoleCompliance); err != nil {
		return PenaltyPass{}, err
	}
	p, _, err := docs.Get[PenaltyPass](ctx, o.Docs, tenant, cPenaltyPass, id)
	if err != nil {
		return p, err
	}
	if p.Status != "Waiting for approval" {
		return p, fmt.Errorf("%w: %s is %s", ErrState, id, strings.ToLower(p.Status))
	}
	if p.Maker == actor {
		return p, fmt.Errorf("%w: the person who proposed it cannot approve or reject it", ErrForbidden)
	}
	set, _ := o.Settings(ctx, tenant)
	if approve && !set.PenaltyPassThrough {
		return p, fmt.Errorf("%w: penalty pass-through was switched off in Settings", ErrState)
	}
	p.Checker, p.CheckedAt, p.CheckerNote = actor, o.now().Format(time.RFC3339), strings.TrimSpace(note)
	if !approve {
		if p.CheckerNote == "" {
			return p, invalid("say why it is rejected")
		}
		p.Status = "Rejected"
	} else {
		amt := p.Penalty.Round(money.UnitPaisa)
		e, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: tenant, Key: "PEN:" + p.Date + ":" + p.UCC, ValueDate: p.Date,
			Narration: fmt.Sprintf("Short margin collection penalty for %s (%s%% on %s) passed on: %s, %s", p.Date, p.SlabPct, p.Short.Fixed(2), p.ReasonText, p.Ref()),
			Source:    ledger.SourceRef{Kind: "PENALTY", ID: p.Date + "/" + p.UCC}, CreatedBy: actor,
			Postings: []ledger.Posting{{Account: ledger.ClientAccount(p.UCC), Debit: amt}, {Account: ledger.PayablePenalty, Credit: amt}}})
		if err != nil {
			return p, err
		}
		p.Status, p.Entry = "Posted", e.ID
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cPenaltyPass, p.ID, p, actor); err != nil {
		return p, err
	}
	o.closeWork(ctx, tenant, actor, wPenaltyPass, "approve:"+p.ID, p.Status+" by "+actor)
	o.log(ctx, tenant, actor, "margin.penalty."+strings.ToLower(p.Status), p.ID, map[string]any{"reason": p.Reason, "ref": p.Ref(), "maker": p.Maker, "entry": p.Entry, "note": p.CheckerNote})
	if p.Status == "Posted" {
		// The client gets the supporting documents with a signed letter.
		paras := []string{
			fmt.Sprintf("The clearing corporation levied a penalty of ₹%s on us for short collection of upfront margin in your account %s on %s (shortfall ₹%s, %s%%).", formatINR(p.Penalty), p.UCC, fmtDay(p.Date), formatINR(p.Short), p.SlabPct),
			fmt.Sprintf("Exchange rules allow this penalty to be charged to you only where the shortfall was caused by the client. It has been debited to your account because: %s (%s).", p.ReasonText, p.Ref()),
			"The supporting documents are attached: " + p.EvidenceNames() + ". If you think this is wrong, please write to us within 30 days.",
		}
		d, err := o.sendLetterWith(ctx, tenant, actor, p.Date, p.UCC, "PEN-"+p.Date, "Margin penalty charged to your account", paras, p.Evidence)
		if err != nil {
			o.log(ctx, tenant, actor, "letter.error", p.UCC, map[string]any{"penalty": p.ID, "error": err.Error()})
		} else {
			p.Letter = d.ID
			_ = docs.Upsert(ctx, o.Docs, tenant, cPenaltyPass, p.ID, p, actor)
		}
	}
	return p, nil
}
