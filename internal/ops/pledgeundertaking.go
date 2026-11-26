package ops

import (
	"context"
	"fmt"
	"strings"
	"time"

	"ledgerline/internal/docs"
)

// ---- the Annexure-A undertaking with pledges and invocations ----
//
// CDSL's communiqué DP2026-180 (invocation of pledges, under SEBI's
// February 2026 circular) asks, from 6 April 2026, for the undertaking of
// the pledgor and the pledgee in its Annexure-A with each pledge creation
// and invocation request the DP processes on paper or by upload. Ledgerline
// keeps the reference of the signed undertaking (the scan's file name or
// the physical file number) on the request:
//
//   - invocations of margin and MTF pledges (PledgeRelease CF): the firm
//     is the pledgee; compliance is reminded on approving one without it;
//   - pledges to lenders (NormalPledge): the client and the lender sign it.
//
// A margin pledge the client confirms on CDSL's own page with CDSL's OTP is
// created by CDSL online, where the client accepts CDSL's terms; Ledgerline
// treats that confirmation as the record (to confirm with CDSL). The test
// pledge_undertaking finds requests sent without the reference.

const pledgeUndertakingFrom = "2026-04-06"

// RecordPledgeUndertaking records the signed undertaking's reference on an
// invocation (kind CF) or a pledge to a lender (kind NP).
func (o *Ops) RecordPledgeUndertaking(ctx context.Context, tenant, actor, kind, id, ref string) error {
	if err := o.require(ctx, tenant, actor, "Recording pledge undertakings", RoleMaker, RoleChecker, RoleRisk, RoleCompliance); err != nil {
		return err
	}
	ref = strings.TrimSpace(ref)
	if ref == "" || len(ref) > 120 {
		return invalid("give the reference of the signed undertaking (file name or physical file number, up to 120 characters)")
	}
	var err error
	switch kind {
	case "CF":
		_, err = docs.Update(ctx, o.Docs, tenant, cCPLRelease, id, actor, func(x *PledgeRelease) error {
			if x.Kind != "CF" {
				return invalid("%s is not an invocation", id)
			}
			x.Undertaking = ref
			return nil
		})
	case "NP":
		_, err = docs.Update(ctx, o.Docs, tenant, cCPLNormal, id, actor, func(x *NormalPledge) error {
			x.Undertaking = ref
			return nil
		})
	default:
		return invalid("the undertaking is recorded on an invocation (CF) or a pledge to a lender (NP)")
	}
	if err != nil {
		return err
	}
	o.closeWork(ctx, tenant, actor, "pledge-undertaking", kind+":"+id, "recorded: "+ref)
	o.log(ctx, tenant, actor, "cdsl.pledge.undertaking", id, map[string]any{"kind": kind})
	return nil
}

func init() {
	siTests = append(siTests, SITest{ID: "pledge_undertaking", Title: "Signed undertaking kept with each invocation and pledge to a lender", Area: "Depository", Severity: "medium", Owner: RoleCompliance,
		Source:    "CDSL communiqué DP2026-180 (invocation of pledge; Annexure-A undertaking of pledgor and pledgee, from 6 Apr 2026)",
		ToConfirm: "Margin pledges the client confirms with CDSL's OTP are taken as recorded by CDSL itself.",
		Checks:    "Invocations of margin and MTF pledges and pledges to lenders sent to CDSL in the last 30 days (from 6 Apr 2026): the reference of the signed Annexure-A undertaking recorded.",
		Fix:       "Get the signed undertaking, keep it, and record its reference on Collateral → Pledges (CDSL).",
		Screen:    "cdslpledge", ScreenName: "Collateral → Pledges (CDSL)", Real: true, run: siPledgeUndertaking})
}

func siPledgeUndertaking(e *siEnv) (siOut, error) {
	var s siOut
	s.input("Pledges and invocations (CDSL)", e.date)
	from := max(addDays(e.date, -siWindow), pledgeUndertakingFrom)
	day := func(ts string) string {
		t, err := time.Parse(time.RFC3339, ts)
		if err != nil {
			return ""
		}
		return t.In(ist).Format("2006-01-02")
	}
	for _, r := range e.o.cplReleases(e.ctx, e.tenant) {
		d := day(r.SentAt)
		if r.Kind != "CF" || (r.Of != "MP" && r.Of != "MF") || d == "" || d < from || d > e.date {
			continue
		}
		s.checked++
		if r.Undertaking == "" {
			s.bad(fmt.Sprintf("Invocation %s · %s · sent %s without the undertaking on record", r.ID, siClient(r.UCC), fmtDay(d)))
		}
	}
	for _, n := range e.o.cplNormals(e.ctx, e.tenant) {
		d := day(n.SentAt)
		if d == "" || d < from || d > e.date {
			continue
		}
		s.checked++
		if n.Undertaking == "" {
			s.bad(fmt.Sprintf("Pledge %s to %s · %s · sent %s without the undertaking on record", n.ID, n.Lender, siClient(n.UCC), fmtDay(d)))
		}
	}
	s.summary = fmt.Sprintf("%d invocations and pledges to lenders checked; %d without the signed undertaking.", s.checked, s.found)
	return s, nil
}
