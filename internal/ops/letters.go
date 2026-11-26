package ops

import (
	"context"
	"fmt"
	"strings"
)

// ---- letters to clients on account decisions ----
//
// When a reactivation, closure or modification request is accepted or
// rejected, the client gets a signed letter saying so (and why, when it is
// rejected). It goes out like every other document: signed, kept as sent
// and recorded in the dispatch register.

// DocLetter is a letter to a client about a decision on the account.
const DocLetter = "client_letter"

func init() { DocKindNames[DocLetter] = "Letter on an account request" }

// sendLetter sends a signed letter; ref identifies the request.
func (o *Ops) sendLetter(ctx context.Context, tenant, actor, date, ucc, ref, title string, paras []string) (Dispatch, error) {
	return o.sendLetterWith(ctx, tenant, actor, date, ucc, ref, title, paras, nil)
}

// sendLetterWith sends a signed letter with kept files attached as they are.
func (o *Ops) sendLetterWith(ctx context.Context, tenant, actor, date, ucc, ref, title string, paras []string, extra []PeriodicFile) (Dispatch, error) {
	c, err := o.Client(ctx, tenant, ucc)
	if err != nil {
		return Dispatch{}, err
	}
	set, _ := o.Settings(ctx, tenant)
	rep := ClientReport{Kind: "letter", Title: title, UCC: c.UCC, Name: c.Name, PAN: MaskPAN(c.PAN), Address: c.City, To: date, Period: fmtDay(date),
		Generated: o.now().Format("02-Jan-2006 15:04 IST"), Notes: append([]string{"Dear " + c.Name + ","}, append(paras, "Yours faithfully,", firstOf(set.Firm, "The broker"))...)}
	rep.normalise()
	return o.dispatchDoc(ctx, tenant, actor, docRequest{Kind: DocLetter, Ref: ref, Date: date, UCC: c.UCC, Report: reportPDF(rep, set), Title: title,
		FileName: strings.NewReplacer(" ", "", "/", "-").Replace(title) + "_" + c.UCC + "_" + date + ".pdf",
		Text:     fmt.Sprintf("Dear %s,\n\n%s\n\nThe signed letter is attached.", c.Name, strings.Join(paras, "\n\n")), Extra: extra})
}

func (o *Ops) reactivationLetter(ctx context.Context, tenant, actor string, r Reactivation) {
	paras := []string{fmt.Sprintf("Your request %s to reactivate your trading account %s has been accepted on %s. You can trade again from the next trading day.", r.ID, r.UCC, fmtDay(r.Decided))}
	title := "Account reactivation accepted"
	if r.Status == "Rejected" {
		title = "Account reactivation not accepted"
		paras = []string{fmt.Sprintf("We could not accept your request %s to reactivate your trading account %s, for this reason: %s.", r.ID, r.UCC, r.Reason),
			"Please send us what is needed and we will look at the request again. You can also write to us if you think this is a mistake."}
	}
	if _, err := o.sendLetter(ctx, tenant, actor, r.Decided, r.UCC, r.ID, title, paras); err != nil {
		o.log(ctx, tenant, actor, "letter.error", r.UCC, map[string]any{"request": r.ID, "error": err.Error()})
	}
}

// closureLetter writes to the client when a segment is closed. It says the
// funds and securities were paid out only when the books show it; while a
// credit balance or a pledge remains, the letter is held and a work item
// asks for the pay-out first (NSE/INSP/73976 item 1.15(vi); a closure
// asked for by the client goes through its own request, acctclose.go).
func (o *Ops) closureLetter(ctx context.Context, tenant, actor string, c Client, ch SegmentChange) {
	if ch.To != "Closed" || c.Deceased != nil {
		return // a deceased client's balance goes through the transmission (deceased.go)
	}
	paras := []string{fmt.Sprintf("Your trading account %s has been closed in the %s segment from %s (%s).", c.UCC, ch.Segment, fmtDay(ch.On), ch.Reason)}
	if len(c.OpenSegments()) == 0 {
		ok, why := o.closurePaidOut(ctx, tenant, c.UCC, ch.On)
		if !ok {
			o.raiseWork(ctx, tenant, "closure", "letter|"+c.UCC+"|"+ch.On, "Closure letter held · "+c.UCC,
				fmt.Sprintf("%s's last open segment was closed on %s, but the books show %s. Pay out and release first; the closure letter goes once that is done (close it through a closure request on Settlement → Account closures).", c.UCC, fmtDay(ch.On), why), c.UCC, RoleChecker, "settlement", ch.On)
			o.log(ctx, tenant, actor, "letter.held", c.UCC, map[string]any{"segment": ch.Segment, "why": why})
			return
		}
		paras = append(paras, "Your ledger shows no funds due to you and no securities pledged with us. Your statement of account to the date of closure is available on request.")
	} else {
		paras = append(paras, fmt.Sprintf("Your account stays open in %s. Your statement of account is available on request.", strings.Join(c.OpenSegments(), ", ")))
	}
	if _, err := o.sendLetter(ctx, tenant, actor, ch.On, c.UCC, "CLOSE-"+ch.Segment+"-"+ch.On, "Account closure", paras); err != nil {
		o.log(ctx, tenant, actor, "letter.error", c.UCC, map[string]any{"segment": ch.Segment, "error": err.Error()})
	}
}
