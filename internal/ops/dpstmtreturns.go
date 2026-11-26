package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
)

// ---- DP statements returned undelivered ----
//
// SEBI's master circular for depositories (SEBI/HO/MRD/MRD-PoD-1/P/CIR/
// 2024/168, para 1.22): a DP may stop sending a BO's transaction statements
// only after three consecutive statements came back undelivered because
// the address changed. The returns are kept as proof, the client is asked
// to update the address, and once it is updated the withheld statements
// are sent, free of charge.
//
// A printed statement the courier brings back is recorded on the dispatch
// register (Returned undelivered). Each return raises a work item to
// follow up the client's address. After three in a row, the monthly run
// records the statement as Withheld instead of sending it; an approved
// address change after the last return lifts that, and the next run sends
// the withheld statements again.

const (
	dispatchReturned = "Returned undelivered"
	dispatchWithheld = "Withheld"
	// dpReturnsToStop is how many consecutive returns allow stopping.
	dpReturnsToStop = 3
)

// MarkReturned records a printed document the courier brought back
// undelivered.
func (o *Ops) MarkReturned(ctx context.Context, tenant, actor, id, reason string) (Dispatch, error) {
	if err := o.require(ctx, tenant, actor, "Recording a returned document", RoleMaker, RoleChecker, RoleAdmin); err != nil {
		return Dispatch{}, err
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return Dispatch{}, invalid("say why it came back (for example: addressee moved, door locked)")
	}
	d, err := docs.Update(ctx, o.Docs, tenant, cDispatch, id, actor, func(x *Dispatch) error {
		if x.Status != "Physical sent" {
			return invalid("only a printed copy sent by courier can come back undelivered")
		}
		x.Status, x.Reason, x.BouncedAt = dispatchReturned, reason, o.now().Format(time.RFC3339)
		return nil
	})
	if err != nil {
		return d, err
	}
	n := o.dpReturnStreak(ctx, tenant, d.UCC)
	detail := fmt.Sprintf("%s %s sent by %s came back undelivered (%s). Ask the client to update the address (a change request of kind Address) by phone, email or SMS, and keep the returned cover as proof.", DocKindNames[d.Kind], d.Ref, firstOf(d.Courier, "courier"), reason)
	if d.Kind == DocDP {
		detail += fmt.Sprintf(" DP statements returned in a row: %d. After %d, further statements are withheld until the address is updated; they are then sent free of charge.", n, dpReturnsToStop)
	}
	o.raiseWork(ctx, tenant, "dispatch", "address:"+d.UCC+":"+d.ID, "Returned undelivered: update "+d.Name+"'s address", detail, d.UCC, RoleMaker, "dispatch:"+d.ID, d.Date)
	o.log(ctx, tenant, actor, "dispatch.returned", d.ID, map[string]any{"ucc": d.UCC, "reason": reason, "in_a_row": n})
	return d, nil
}

// dpReturnStreak counts the client's latest DP statements that came back
// undelivered, in a row (withheld ones do not break the run).
func (o *Ops) dpReturnStreak(ctx context.Context, tenant, ucc string) int {
	n, _ := o.dpReturnState(ctx, tenant, ucc)
	return n
}

// dpReturnState is the run of returns and when the last one came back.
func (o *Ops) dpReturnState(ctx context.Context, tenant, ucc string) (int, string) {
	all, _ := docs.List[Dispatch](ctx, o.Docs, tenant, cDispatch, "")
	// The latest attempt of each month's statement.
	last := map[string]Dispatch{}
	for _, d := range all {
		if d.Kind != DocDP || d.UCC != ucc {
			continue
		}
		if p, ok := last[d.Ref]; !ok || d.ID > p.ID {
			last[d.Ref] = d
		}
	}
	months := sortedKeys(last)
	sort.Sort(sort.Reverse(sort.StringSlice(months)))
	n, at := 0, ""
	for _, m := range months {
		d := last[m]
		if d.Status == dispatchWithheld {
			continue
		}
		if d.Status != dispatchReturned {
			break
		}
		if at == "" || d.BouncedAt > at {
			at = d.BouncedAt
		}
		n++
	}
	return n, at
}

// addressUpdatedSince says whether an address change was approved after
// the time.
func (o *Ops) addressUpdatedSince(ctx context.Context, tenant, ucc, at string) bool {
	cr, _ := docs.List[ChangeRequest](ctx, o.Docs, tenant, cChange, "")
	for _, c := range cr {
		if c.UCC == ucc && c.Kind == "Address" && c.Status == "Approved" && firstOf(c.Decided, c.At) > at {
			return true
		}
	}
	return false
}

// dpStatementStopped says whether the month's DP statement is withheld:
// three returns in a row and no address update since.
func (o *Ops) dpStatementStopped(ctx context.Context, tenant, ucc string) bool {
	n, at := o.dpReturnState(ctx, tenant, ucc)
	return n >= dpReturnsToStop && !o.addressUpdatedSince(ctx, tenant, ucc, at)
}

// withholdDP records the month's statement as withheld.
func (o *Ops) withholdDP(ctx context.Context, tenant, actor, month, ucc string) (Dispatch, error) {
	c, err := o.Client(ctx, tenant, ucc)
	if err != nil {
		return Dispatch{}, err
	}
	n, err := o.next(ctx, tenant, "dispatch", actor)
	if err != nil {
		return Dispatch{}, err
	}
	d := Dispatch{ID: fmt.Sprintf("DSP-%07d", n), Key: DocDP + "|" + month + "|" + c.UCC, Kind: DocDP, Ref: month, Date: monthEnd(month), UCC: c.UCC, Name: c.Name, Channel: "physical",
		Subject: DocKindNames[DocDP] + " " + month, Status: dispatchWithheld, Reason: fmt.Sprintf("%d statements in a row came back undelivered; sent free of charge once the address is updated", dpReturnsToStop),
		Attempt: 1, Created: o.now().Format(time.RFC3339), By: actor, Files: []DispatchFile{}}
	return d, docs.Upsert(ctx, o.Docs, tenant, cDispatch, d.ID, d, actor)
}

// resendWithheldDP sends the client's withheld DP statements once the
// address is updated.
func (o *Ops) resendWithheldDP(ctx context.Context, tenant, actor, ucc string, res *SendStatementsResult) {
	all, _ := docs.List[Dispatch](ctx, o.Docs, tenant, cDispatch, "")
	for _, d := range all {
		if d.Kind == DocDP && d.UCC == ucc && d.Status == dispatchWithheld && !o.delivered(ctx, tenant, DocDP, d.Ref, ucc) {
			res.add(o.sendDPStatement(ctx, tenant, actor, d.Ref, ucc))
			_, _ = docs.Update(ctx, o.Docs, tenant, cDispatch, d.ID, actor, func(x *Dispatch) error {
				x.Status, x.Reason = "Superseded", "sent again after the address was updated"
				return nil
			})
		}
	}
}
