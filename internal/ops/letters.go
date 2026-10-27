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
		Text: fmt.Sprintf("Dear %s,\n\n%s\n\nThe signed letter is attached.", c.Name, strings.Join(paras, "\n\n"))})
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

func (o *Ops) closureLetter(ctx context.Context, tenant, actor string, c Client, ch SegmentChange) {
	if ch.To != "Closed" {
		return
	}
	paras := []string{fmt.Sprintf("Your trading account %s has been closed in the %s segment from %s (%s).", c.UCC, ch.Segment, fmtDay(ch.On), ch.Reason),
		"Any funds and securities due to you are paid out to your registered bank and demat accounts. Your statement of account to the date of closure is available on request."}
	if _, err := o.sendLetter(ctx, tenant, actor, ch.On, c.UCC, "CLOSE-"+ch.Segment+"-"+ch.On, "Account closure", paras); err != nil {
		o.log(ctx, tenant, actor, "letter.error", c.UCC, map[string]any{"segment": ch.Segment, "error": err.Error()})
	}
}
