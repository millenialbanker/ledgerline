package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// ---- BSDA review ----
//
// A Basic Services Demat Account (BSDA) is the low-cost demat account for
// individuals with one demat account and holdings within the limit (₹10
// lakh under SEBI's 2024 circular). The DP reviews eligibility every
// quarter: eligible regular accounts are converted to BSDA unless the
// client opted out, and BSDAs whose holdings went above the limit, or
// whose holder now has another demat account, become regular accounts.
// The changes go to the depository as BO modifications and each client is
// told. At CDSL each account is a BO modify request sent by the BO modify
// API when it is on (cdslbo.go), with the reason CDSL's code list gives
// (VHCPL, BOHMA); with the API off the review keeps one BO modify file in
// CDSL's layout for upload in CDAS.

const cBSDAReview = "bsdareview"

// BSDALine is one account the review proposes to change.
type BSDALine struct {
	UCC    string    `json:"ucc"`
	Name   string    `json:"name"`
	BOID   string    `json:"bo_id"`
	Value  money.Dec `json:"value"`
	From   string    `json:"from"` // BSDA, Regular
	To     string    `json:"to"`
	Reason string    `json:"reason"`
	Code   string    `json:"code"` // LIMIT (above the limit), MULTI (another demat account), ELIG (eligible)
	// CDSLReq is the BO modify request that took the change to CDSL;
	// CDSLStatus is how it stands (filled when reviews are read), or why
	// nothing was sent.
	CDSLReq    string `json:"cdsl_req,omitempty"`
	CDSLStatus string `json:"cdsl_status,omitempty"`
}

// BSDAReview is one quarter's review.
type BSDAReview struct {
	ID        string     `json:"id"` // YYYY-Qn
	AsOf      string     `json:"as_of"`
	Limit     money.Dec  `json:"limit"`
	Accounts  int        `json:"accounts"`
	BSDA      int        `json:"bsda"`
	Lines     []BSDALine `json:"lines"`
	Kept      []BSDALine `json:"kept"`   // eligible but not converted, and why
	Status    string     `json:"status"` // Proposed, Applied
	By        string     `json:"by"`
	At        string     `json:"at"`
	AppliedBy string     `json:"applied_by,omitempty"`
	AppliedAt string     `json:"applied_at,omitempty"`
	FileID    string     `json:"file_id,omitempty"`
	FileName  string     `json:"file_name,omitempty"`
	// CDSLNote says how the changes went to CDSL: by the BO modify API, or
	// in the file kept here for upload in CDAS (FileCDSL).
	CDSLNote string `json:"cdsl_note,omitempty"`
	FileCDSL bool   `json:"file_cdsl,omitempty"`
}

// quarterID names the calendar quarter of a date (2026-Q3 = Jul–Sep).
func quarterID(date string) string {
	m := 1
	fmt.Sscanf(date[5:7], "%d", &m)
	return fmt.Sprintf("%s-Q%d", date[:4], (m-1)/3+1)
}

// quarterEnd is the last day of the quarter before the one date is in.
func prevQuarterEnd(date string) string {
	m := 1
	fmt.Sscanf(date[5:7], "%d", &m)
	first := fmt.Sprintf("%s-%02d-01", date[:4], (m-1)/3*3+1)
	return addDays(first, -1)
}

// ReviewBSDA proposes the quarter's conversions as at asOf (normally the
// last day of the quarter just ended). It changes nothing until applied.
func (o *Ops) ReviewBSDA(ctx context.Context, tenant, actor, asOf string) (BSDAReview, error) {
	if actor != SystemActor {
		if err := o.require(ctx, tenant, actor, "Reviewing BSDA eligibility", RoleMaker, RoleChecker); err != nil {
			return BSDAReview{}, err
		}
	}
	if !validDate(asOf) {
		return BSDAReview{}, invalid("give the review date (YYYY-MM-DD)")
	}
	id := quarterID(asOf)
	if old, _, err := docs.Get[BSDAReview](ctx, o.Docs, tenant, cBSDAReview, id); err == nil && old.Status == "Applied" {
		return old, fmt.Errorf("%w: the BSDA review for %s was applied on %s", ErrState, id, old.AppliedAt)
	}
	// Holdings are valued at the closing prices of the review date (or the
	// latest before it); without prices every account would look eligible.
	if _, _, err := o.Market(ctx, tenant, asOf); err != nil {
		return BSDAReview{}, fmt.Errorf("cannot value holdings for the BSDA review: %w", err)
	}
	v, err := o.DP(ctx, tenant, asOf)
	if err != nil {
		return BSDAReview{}, err
	}
	cs, err := o.ClientMap(ctx, tenant)
	if err != nil {
		return BSDAReview{}, err
	}
	// One demat account per PAN at this DP.
	perPAN := map[string]int{}
	for _, r := range v.Rows {
		if r.Status != "Closed" {
			perPAN[strings.ToUpper(cs[r.UCC].PAN)]++
		}
	}
	rev := BSDAReview{ID: id, AsOf: asOf, Limit: v.Tariff.BSDALimit.Dec(), Lines: []BSDALine{}, Kept: []BSDALine{}, Status: "Proposed", By: actor, At: o.now().Format("2006-01-02 15:04")}
	for _, r := range v.Rows {
		if r.Status == "Closed" || r.Status == "Not opened" {
			continue
		}
		rev.Accounts++
		c := cs[r.UCC]
		l := BSDALine{UCC: r.UCC, Name: r.Name, BOID: r.BOID, Value: r.Value}
		over := r.Value.Cmp(v.Tariff.BSDALimit.Dec()) > 0
		pan := strings.ToUpper(strings.TrimSpace(c.PAN))
		multi := pan != "" && perPAN[pan] > 1
		switch {
		case r.BSDA && over:
			l.From, l.To, l.Code = "BSDA", "Regular", "LIMIT"
			l.Reason = fmt.Sprintf("Holdings of ₹%s are above the BSDA limit of ₹%s", formatINR(r.Value), formatINR(rev.Limit))
			rev.Lines = append(rev.Lines, l)
		case r.BSDA && (multi || r.OtherDemat):
			// A BSDA is the holder's only demat account (CDSL's reason
			// BOHMA: multiple accounts across the depositories).
			l.From, l.To, l.Code = "BSDA", "Regular", "MULTI"
			l.Reason = "More than one demat account for the PAN"
			if !multi {
				l.Reason = "Has a demat account with another DP"
			}
			rev.Lines = append(rev.Lines, l)
		case r.BSDA:
			rev.BSDA++
		case over:
		case c.Type != "Individual":
		case multi:
			l.Reason = "More than one demat account for the PAN"
			rev.Kept = append(rev.Kept, l)
		case r.BSDAOptOut:
			l.Reason = "The client chose a regular account"
			rev.Kept = append(rev.Kept, l)
		case r.OtherDemat:
			l.Reason = "Has a demat account with another DP"
			rev.Kept = append(rev.Kept, l)
		default:
			l.From, l.To, l.Code = "Regular", "BSDA", "ELIG"
			l.Reason = fmt.Sprintf("Individual with one demat account and holdings of ₹%s, within ₹%s", formatINR(r.Value), formatINR(rev.Limit))
			rev.Lines = append(rev.Lines, l)
		}
	}
	sort.Slice(rev.Lines, func(i, j int) bool { return rev.Lines[i].UCC < rev.Lines[j].UCC })
	if err := docs.Upsert(ctx, o.Docs, tenant, cBSDAReview, rev.ID, rev, actor); err != nil {
		return rev, err
	}
	o.log(ctx, tenant, actor, "dp.bsda_review", rev.ID, map[string]any{"as_of": asOf, "changes": len(rev.Lines)})
	return rev, nil
}

// BSDAReviews lists the reviews, latest first.
// Each change sent to CDSL shows how its BO modify request stands.
func (o *Ops) BSDAReviews(ctx context.Context, tenant string) ([]BSDAReview, error) {
	rs, err := docs.List[BSDAReview](ctx, o.Docs, tenant, cBSDAReview, "")
	sort.Slice(rs, func(i, j int) bool { return rs[i].ID > rs[j].ID })
	if rs == nil {
		rs = []BSDAReview{}
	}
	var reqs map[string]CDSLRequest
	for i := range rs {
		for k, l := range rs[i].Lines {
			if l.CDSLReq == "" {
				continue
			}
			if reqs == nil {
				reqs = map[string]CDSLRequest{}
				all, _ := docs.List[CDSLRequest](ctx, o.Docs, tenant, cCDSLReq, "")
				for _, r := range all {
					reqs[r.ID] = r
				}
			}
			if r, ok := reqs[l.CDSLReq]; ok {
				rs[i].Lines[k].CDSLStatus = r.Status
			}
		}
	}
	return rs, err
}

// ApplyBSDAReview converts the accounts, keeps the BO modification file
// for the depository and tells each client. A checker applies it; the one
// who proposed it cannot, unless the system proposed it.
func (o *Ops) ApplyBSDAReview(ctx context.Context, tenant, actor, id string) (BSDAReview, error) {
	if err := o.require(ctx, tenant, actor, "Applying the BSDA review", RoleChecker); err != nil {
		return BSDAReview{}, err
	}
	rev, _, err := docs.Get[BSDAReview](ctx, o.Docs, tenant, cBSDAReview, id)
	if err != nil {
		return rev, fmt.Errorf("%w: BSDA review %s", ErrNotFound, id)
	}
	if rev.Status == "Applied" {
		return rev, fmt.Errorf("%w: already applied", ErrState)
	}
	if rev.By == actor && actor != SystemActor {
		return rev, fmt.Errorf("%w: the review must be applied by a different checker", ErrForbidden)
	}
	now := o.now()
	x, conn := o.cdslConnFor(ctx, tenant)
	api := conn && x.On(cdslFamBO)
	rows := [][]string{{"BOID", "ClientCode", "Name", "BSDAFlag", "ReasonCode", "HoldingValue", "AsOn"}}
	var recs [][]boLine
	var toCDSL []int // the lines the records are for
	for i, l := range rev.Lines {
		c, err := o.Client(ctx, tenant, l.UCC)
		if err != nil {
			rev.Lines[i].CDSLStatus = "Client not found"
			continue
		}
		b := o.boFor(ctx, tenant, c, l.BOID)
		b.BSDA = l.To == "BSDA"
		if err := o.putBO(ctx, tenant, actor, b); err != nil {
			return rev, err
		}
		flag, reason := bsdaForCDSL(l.Code)
		rows = append(rows, []string{l.BOID, l.UCC, l.Name, flag, reason, l.Value.Fixed(2), fmtDDMMYYYY(rev.AsOf)})
		what := "converted to a Basic Services Demat Account (lower charges)"
		switch {
		case b.BSDA:
		case l.Code == "MULTI":
			what = "changed to a regular demat account because a Basic Services Demat Account must be your only demat account"
		default:
			what = "changed to a regular demat account because the holdings went above the BSDA limit"
		}
		o.notify(ctx, tenant, actor, "dp.bsda", l.UCC, map[string]string{"what": what, "value": formatINR(l.Value), "limit": formatINR(rev.Limit), "date": rev.AsOf})
		if b.Depository != "CDSL" || !reBOIDCDSL.MatchString(b.BOID) {
			rev.Lines[i].CDSLStatus = "Not a CDSL account: change it at the depository"
			continue
		}
		recs = append(recs, bsdaModifyRecord(b.BOID, flag, reason, now))
		toCDSL = append(toCDSL, i)
	}
	summary := func() error { // Ledgerline's own record of the changes
		data := csvBytesOf(rows)
		name := fmt.Sprintf("BSDA_MOD_%s.csv", now.Format("20060102"))
		f, err := o.keepFile(ctx, tenant, name, data)
		rev.FileID, rev.FileName, rev.FileCDSL = f.ID, f.Name, false
		return err
	}
	switch {
	case len(recs) == 0:
		if err := summary(); err != nil {
			return rev, err
		}
	case api:
		// Each account is its own BO modify request; the checker applying
		// the review approves it, so it goes to CDSL at once.
		sent, held := 0, 0
		for _, i := range toCDSL {
			l := rev.Lines[i]
			flag, reason := bsdaForCDSL(l.Code)
			r := CDSLRequest{Kind: CDSLModify, UCC: l.UCC, Name: l.Name, BOID: l.BOID, Source: "BSDA review " + rev.ID + " (" + l.UCC + ")",
				Fields: map[string]string{"bsda": flag, "bsda_reason": reason, "bsda_review": rev.ID}, Maker: rev.By}
			o.cdslBSDAFill(ctx, tenant, actor, x, &r)
			r, err := o.cdslNewRequest(ctx, tenant, actor, r)
			if err != nil {
				return rev, err
			}
			rev.Lines[i].CDSLReq = r.ID
			if r.Status != crChecker { // the file could not be made
				held++
				o.raiseWork(ctx, tenant, "cdslbo", r.ID, fmt.Sprintf("Send %s's BSDA change to CDSL", l.UCC),
					fmt.Sprintf("The BSDA review %s changed %s to %s; its BO modify file could not be made: %s. Fix it and make the file again on the CDSL requests tab, or change it in CDAS.", rev.ID, cdslMaskBO(l.BOID), strings.ToLower(l.To), strings.Join(r.Problems, "; ")), l.UCC, RoleMaker, "cdslreq", "")
				continue
			}
			r.Checker, r.Status = actor, crApproved
			r.event(o.now().Format(time.RFC3339), actor, "Approved with the BSDA review "+rev.ID)
			if _, err := o.cdslSend(ctx, tenant, actor, x, &r); err != nil {
				held++
			} else {
				sent++
			}
			if err := o.cdslSave(ctx, tenant, actor, &r); err != nil {
				return rev, err
			}
		}
		rev.CDSLNote = fmt.Sprintf("Sent to CDSL by the BO modify API: %d account%s", sent, plural(sent))
		if held > 0 {
			rev.CDSLNote = fmt.Sprintf("Sent to CDSL by the BO modify API: %d of %d accounts; %d not sent, on the work queue and the CDSL requests tab", sent, sent+held, held)
		}
		if err := summary(); err != nil {
			return rev, err
		}
	default:
		// The API is off: one BO modify file in CDSL's layout (the firm's
		// fixed-width or harmonised format) for upload in CDAS.
		fx := x
		if !conn {
			fx = cdslConn{DPID: cdxBODP(recs[0][0].V["bo_id"])}
		}
		name, data, probs, _, err := o.cdslWriteModifies(ctx, tenant, actor, fx, o.CDSLBODefaultsNow(ctx, tenant).Values, recs, nil, nil)
		if err != nil {
			return rev, err
		}
		what := fmt.Sprintf("%d account%s", len(recs), plural(len(recs)))
		if len(probs) > 0 {
			rev.CDSLNote = "CDSL's BO modify file could not be made (" + strings.Join(probs, "; ") + "): change the " + what + " in CDAS"
			if err := summary(); err != nil {
				return rev, err
			}
		} else {
			f, err := o.keepFile(ctx, tenant, name, data)
			if err != nil {
				return rev, err
			}
			rev.FileID, rev.FileName, rev.FileCDSL = f.ID, f.Name, true
			rev.CDSLNote = "Upload " + name + " in CDAS (BO modify upload) for the " + what + "; the CDSL BO API is off in Connections"
		}
		for _, i := range toCDSL {
			rev.Lines[i].CDSLStatus = "In the file for CDAS"
		}
		o.raiseWork(ctx, tenant, "periodic", "bsdacdsl:"+rev.ID, "Send the BSDA review "+rev.ID+" to CDSL", rev.CDSLNote+".", "", RoleMaker, "dp", "")
	}
	rev.Status, rev.AppliedBy, rev.AppliedAt = "Applied", actor, now.Format("2006-01-02 15:04")
	if err := docs.Upsert(ctx, o.Docs, tenant, cBSDAReview, rev.ID, rev, actor); err != nil {
		return rev, err
	}
	o.log(ctx, tenant, actor, "dp.bsda_applied", rev.ID, map[string]any{"changes": len(rev.Lines), "file": rev.FileName, "cdsl": rev.CDSLNote})
	return rev, nil
}

// bsdaForCDSL is what CDSL is told for a review line: the BSDA flag as
// the fixed-width file writes it (Y, N; the harmonised upload BSDA, NBSD)
// and CDSL's reason for the change (harmonised upload only).
func bsdaForCDSL(code string) (flag, reason string) {
	switch code {
	case "ELIG":
		return "Y", "DFT"
	case "MULTI":
		return "N", "BOHMA"
	}
	return "N", "VHCPL"
}

// bsdaModifyRecord is the BO modify record that changes an account's BSDA
// flag: line 01 and a line 05 with only the flag (blank fields are left
// as they are at CDSL).
func bsdaModifyRecord(boid, flag, reason string, at time.Time) []boLine {
	return []boLine{
		{Layout: "m01", No: 1, Who: "Record", V: map[string]string{"bo_id": boid, "request_date": boDateTime(at)}},
		{Layout: "m05", No: 5, Who: "Account", V: map[string]string{"bsda": flag}, H: map[string]string{"RsnBSDAMod": reason}},
	}
}

// cdslBSDAFill makes the BO modify file of a request raised by a BSDA
// review (and again when staff ask).
func (o *Ops) cdslBSDAFill(ctx context.Context, tenant, actor string, x cdslConn, r *CDSLRequest) {
	var probs []string
	if !reBOIDCDSL.MatchString(r.BOID) {
		probs = append(probs, "the account has no 16-digit CDSL BO ID")
	}
	rec := bsdaModifyRecord(r.BOID, r.Fields["bsda"], r.Fields["bsda_reason"], o.now())
	name, data, probs, warns, err := o.cdslWriteModify(ctx, tenant, actor, x, o.CDSLBODefaultsNow(ctx, tenant).Values, rec, probs, nil)
	o.cdslPutFile(ctx, tenant, actor, r, "BO modify (BSDA)", name, data, probs, warns, err)
}

func fmtDDMMYYYY(d string) string {
	t, err := time.Parse("2006-01-02", d)
	if err != nil {
		return d
	}
	return t.Format("02012006")
}

// SetBSDAChoice records a client's choice to stay on a regular account and
// whether the client has a demat account with another DP; the review uses
// both.
func (o *Ops) SetBSDAChoice(ctx context.Context, tenant, actor, ucc string, optOut, otherDemat bool) (BOAccount, error) {
	if err := o.require(ctx, tenant, actor, "Recording the BSDA choice", RoleMaker, RoleChecker); err != nil {
		return BOAccount{}, err
	}
	c, err := o.Client(ctx, tenant, ucc)
	if err != nil {
		return BOAccount{}, err
	}
	b := o.bo(ctx, tenant, c)
	b.BSDAOptOut, b.OtherDemat = optOut, otherDemat
	if err := o.putBO(ctx, tenant, actor, b); err != nil {
		return b, err
	}
	o.log(ctx, tenant, actor, "dp.bsda_choice", b.UCC, map[string]any{"opt_out": optOut, "other_demat": otherDemat})
	return b, nil
}
