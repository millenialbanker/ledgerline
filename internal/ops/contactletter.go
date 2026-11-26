package ops

import (
	"context"
	"fmt"
	"strings"
	"time"

	"ledgerline/internal/docs"
)

// ---- letters and nudges that follow a client's details ----
//
//   - CDSL DPOI 3.4.17: where the e-mail and the mobile of a demat account
//     whose KYC attributes are not compliant are changed together, the DP
//     sends an acknowledgement letter by registered post or courier to the
//     permanent address and keeps the dispatch proof. "Together" is read
//     as both changes approved within 30 days of each other (the window
//     recentContactChange uses); "KYC attributes not compliant" as a KRA
//     status other than Validated or Registered. Ledgerline raises the
//     work item; the letter goes by post (not to the new e-mail), and the
//     work item is closed with the courier or registered post number.
//   - SEBI/HO/OIAE/OIAE_IAD-3/P/CIR/2026/12676 para 10.2: a demat account
//     without a nomination (opt-outs included) is shown a pop-up on the
//     benefits of nominating at the first login of the day on the web or
//     app; accounts with a nomination are not.

const (
	wContactLetter = "contactletter"
	cNomNudge      = "nomnudge" // UCC -> the day the pop-up was last shown
)

// contactChangeLetter raises the registered-post letter when both the
// mobile and the e-mail of a demat client with non-compliant KYC changed.
func (o *Ops) contactChangeLetter(ctx context.Context, tenant string, r ChangeRequest) {
	if v, _ := dutyNeed("dp", o.firmFacts(ctx, tenant)); v == triNo {
		return
	}
	c, err := o.Client(ctx, tenant, r.UCC)
	if err != nil || c.KRAStatus == "Validated" || c.KRAStatus == "Registered" || len(o.boAccountsOf(ctx, tenant, c.UCC)) == 0 {
		return
	}
	other := map[string]string{"Mobile": "Email", "Email": "Mobile"}[r.Kind]
	rs, _ := docs.List[ChangeRequest](ctx, o.Docs, tenant, cChange, "")
	since := o.now().AddDate(0, 0, -30).Format(time.RFC3339)
	var ids []string
	for _, x := range rs {
		if x.UCC == c.UCC && x.Kind == other && x.Status == "Approved" && x.Decided >= since {
			ids = append(ids, x.ID)
		}
	}
	if len(ids) == 0 {
		return
	}
	day := o.now().In(ist).Format("2006-01-02")
	o.raiseWork(ctx, tenant, wContactLetter, c.UCC+":"+r.ID, "Registered-post letter to "+c.UCC+": mobile and e-mail both changed",
		fmt.Sprintf("%s's mobile and e-mail were both changed (%s and %s) and the KYC is not KRA-validated (%s). Send an acknowledgement letter by registered post or courier to the permanent address, not to the new contacts, and close this item with the post or courier number (CDSL DPOI 3.4.17).",
			c.UCC, r.ID, strings.Join(ids, ", "), firstOf(c.KRAStatus, "not known")), c.UCC, RoleMaker, "changes", day)
	o.log(ctx, tenant, SystemActor, "dp.contact_letter_due", c.UCC, map[string]any{"changes": append(ids, r.ID)})
}

// NominationNudgeText is the pop-up on the benefits of nominating.
const NominationNudgeText = "Your demat account has no nominee. With a nominee, your securities pass to the person you choose quickly and simply; without one, your family must produce documents such as a succession certificate or probate. You can name up to 3 nominees, giving only each one's name and relationship. Add one from your profile in the client portal or app."

// nominationNudge is the pop-up text for the client's first login of the
// day, or "" (a nomination given, no demat account with the firm's DP, or
// shown already today). Showing it records the day.
func (o *Ops) nominationNudge(ctx context.Context, tenant string, c Client) string {
	if c.Nominee == "Opted in" || len(o.boAccountsOf(ctx, tenant, c.UCC)) == 0 {
		return ""
	}
	if v, _ := dutyNeed("dp", o.firmFacts(ctx, tenant)); v == triNo {
		return ""
	}
	day := o.now().In(ist).Format("2006-01-02")
	type seen struct {
		Day string `json:"day"`
	}
	if s, _, err := docs.Get[seen](ctx, o.Docs, tenant, cNomNudge, c.UCC); err == nil && s.Day == day {
		return ""
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cNomNudge, c.UCC, seen{Day: day}, SystemActor); err != nil {
		return ""
	}
	return NominationNudgeText
}
