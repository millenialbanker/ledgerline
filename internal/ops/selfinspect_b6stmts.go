package ops

import (
	"context"
	"fmt"
	"strings"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// ---- self-inspection tests: electronic contract notes, the account
// opening kit, and balances that reach a closed demat account ----
//
// Closed accounts (CDSL DPOI 10.5.3.9-10.5.3.16): securities that reach a
// closed account later (a corporate benefit credited, or a freeze lifted)
// are transferred, on the holder's instruction, to the account the
// holder names, within one year of the closure. Ledgerline sees them in
// the holdings of an account marked Closed: each raises a work item due a
// year from the closure, and the test closed_account_balances fails for
// any still there after the year.

const wClosedBal = "closedbal"

func init() {
	siTests = append(siTests, siB6StmtTests...)
}

var siB6StmtTests = []SITest{
	{ID: "ecn_authorisation", Title: "Electronic contract notes only on the client's specific authorisation, to the client's own e-mail", Area: "Clients", Severity: "high", Owner: RoleMaker,
		Source:    "SEBI master circular for stock brokers SEBI/HO/MIRSD/MIRSD-PoD/P/CIR/2025/90, paras 21.4, 23.2.1 and 52.3.1-52.3.2",
		ToConfirm: "Clients migrated from an earlier system with ECN = Y have their paper authorisation there: record it (attach the signed form) to clear them.",
		Checks:    "Active clients who get contract notes by e-mail: each has an electronic contract note authorisation recorded (the e-Signed form, a signed letter or the client portal), and no registered e-mail is on the firm's own domain.",
		Fix:       "Attach the client's signed authorisation and record it (Contract notes → Electronic contract note authorisation), or switch the client to printed notes; get the client's own e-mail by a signed letter or the portal.",
		Screen:    "notes", ScreenName: "Operations → Contract notes", Real: true, run: siECNAuth},
	{ID: "account_kit", Title: "Account opening kit sent to every new client, with a delivery record", Area: "Clients", Severity: "medium", Owner: RoleMaker,
		Source: "SEBI master circular for stock brokers SEBI/HO/MIRSD/MIRSD-PoD/P/CIR/2025/90, paras 21.1, 21.2 and 21.12",
		Checks: "Clients whose account was opened in the last 180 days: the account opening kit (index of documents, choices, tariff, senior officials and grievance cell) is in the dispatch register, sent or posted.",
		Fix:    "Send the kit from the client's page (Send account opening kit); a bounced e-mail goes to paper from the Dispatch register.",
		Screen: "dispatch", ScreenName: "Operations → Dispatch register", Real: true, run: siAccountKit},
	{ID: "closed_account_balances", Title: "Securities reaching a closed demat account moved within a year of the closure", Area: "Depository", Severity: "medium", Owner: RoleMaker,
		Source:    "CDSL Operating Instructions for DPs, 10.5.3.9-10.5.3.16 (closure; balances later unfrozen or credited)",
		ToConfirm: "Ledgerline reads the one-year window from the operating instructions as the gap report states it; the steps CDSL asks for (reopening for the transfer) are the DP's in CDAS.",
		Checks:    "Demat accounts marked Closed that still show securities: none more than a year after the closure.",
		Fix:       "Get the holder's instruction and transfer the securities to the account the holder names (Depository → DIS), then close the work item.",
		Screen:    "dp", ScreenName: "Depository → Demat accounts & DIS", Real: true, run: siClosedBalances},
}

func siECNAuth(e *siEnv) (siOut, error) {
	var s siOut
	s.input("Clients", e.date)
	own := firmDomains(e.set)
	for _, c := range e.clients {
		if c.Status != "Active" || c.PhysicalNotes {
			continue
		}
		s.checked++
		if c.ECNAuth == nil || !c.ECNAuth.On {
			s.bad(fmt.Sprintf("%s · gets contract notes by e-mail with no electronic contract note authorisation recorded", siMask(c.UCC)))
			continue
		}
		if d := emailDomain(c.Email); own[d] {
			s.bad(fmt.Sprintf("%s · registered e-mail is on the firm's own domain (%s): it must be the client's own", siMask(c.UCC), d))
		}
	}
	if s.checked == 0 {
		return siSkip("Nothing to check: no active client gets contract notes by e-mail.", s.inputs...)
	}
	s.summary = fmt.Sprintf("%d clients on electronic contract notes; %d without an authorisation or with the firm's e-mail.", s.checked, s.found)
	return s, nil
}

func siAccountKit(e *siEnv) (siOut, error) {
	ds, err := docs.List[Dispatch](e.ctx, e.o.Docs, e.tenant, cDispatch, "")
	if err != nil {
		return siOut{}, err
	}
	sent := map[string]bool{}
	for _, d := range ds {
		if d.Kind == DocKit && (d.Status == "Sent" || strings.HasPrefix(d.Status, "Physical")) {
			sent[d.UCC] = true
		}
	}
	var s siOut
	s.input("Clients", e.date)
	s.input("Dispatch register", e.date)
	from := addDays(e.date, -180)
	for _, c := range e.clients {
		if c.KYCDate == "" || c.KYCDate < from || c.KYCDate > e.date || c.Status == "Rejected" {
			continue
		}
		s.checked++
		if !sent[c.UCC] {
			s.bad(fmt.Sprintf("%s · opened %s · no account opening kit delivered", siMask(c.UCC), fmtDay(c.KYCDate)))
		}
	}
	if s.checked == 0 {
		return siSkip("Nothing to check: no account opened in the last 180 days.", s.inputs...)
	}
	s.summary = fmt.Sprintf("%d accounts opened in the last 180 days; %d without a delivered kit.", s.checked, s.found)
	return s, nil
}

// closedBalance is a closed demat account still holding securities.
type closedBalance struct {
	b     BOAccount
	lines []string
	due   string
}

func (o *Ops) closedBalances(ctx context.Context, tenant string) []closedBalance {
	bs, _ := docs.List[BOAccount](ctx, o.Docs, tenant, cBO, "")
	var out []closedBalance
	for _, b := range bs {
		if b.Status != "Closed" || b.MovedTo != "" || b.ClosedOn == "" {
			continue
		}
		c, err := o.Client(ctx, tenant, b.UCC)
		if err != nil {
			continue
		}
		hs, _ := o.Holdings(ctx, tenant, b.UCC)
		acct := holdingAcct(c, b.BOID)
		var ls []string
		for _, h := range hs {
			if h.BOID == acct && h.Qty.Cmp(money.Dec{}) > 0 {
				ls = append(ls, firstOf(h.Symbol, h.ISIN)+" "+fmtQty(h.Qty))
			}
		}
		if len(ls) > 0 {
			out = append(out, closedBalance{b: b, lines: ls, due: addDays(addMonths(b.ClosedOn, 12), -1)})
		}
	}
	return out
}

// closedBalanceDaily raises a work item for each closed account that
// holds securities again, due a year from its closure.
func (o *Ops) closedBalanceDaily(ctx context.Context, tenant, day string) {
	if v, _ := dutyNeed("dp", o.firmFacts(ctx, tenant)); v == triNo {
		return
	}
	for _, x := range o.closedBalances(ctx, tenant) {
		o.workDue(ctx, tenant, wClosedBal, x.b.BOID+":"+x.b.ClosedOn, "Move the securities in closed demat account "+x.b.BOID,
			fmt.Sprintf("%s's demat account %s was closed on %s but now holds %s. Get the holder's instruction and transfer them to the account the holder names by %s (within a year of the closure, CDSL DPOI 10.5.3).", x.b.UCC, x.b.BOID, fmtDay(x.b.ClosedOn), strings.Join(x.lines, ", "), fmtDay(x.due)),
			x.b.UCC, RoleMaker, "dp", day, x.due+" 17:00")
	}
}

func siClosedBalances(e *siEnv) (siOut, error) {
	if v, _ := dutyNeed("dp", e.o.firmFacts(e.ctx, e.tenant)); v == triNo {
		return siSkip("The firm is not a depository participant (Settings → Firm modules).")
	}
	var s siOut
	s.input("Demat accounts and holdings", e.date)
	for _, x := range e.o.closedBalances(e.ctx, e.tenant) {
		s.checked++
		if e.date > x.due {
			s.bad(fmt.Sprintf("%s · %s · closed %s · still holds %s (a year ran out on %s)", siMask(x.b.UCC), x.b.BOID, fmtDay(x.b.ClosedOn), strings.Join(x.lines, ", "), fmtDay(x.due)))
		}
	}
	if s.checked == 0 {
		return siSkip("Nothing to check: no closed demat account holds securities.", s.inputs...)
	}
	s.summary = fmt.Sprintf("%d closed accounts hold securities; %d past the year.", s.checked, s.found)
	return s, nil
}
