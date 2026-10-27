package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/money"
)

// ---- BSDA review ----
//
// A Basic Services Demat Account (BSDA) is the low-cost demat account for
// individuals with one demat account and holdings within the limit (₹10
// lakh under SEBI's 2024 circular). The DP reviews eligibility every
// quarter: eligible regular accounts are converted to BSDA unless the
// client opted out, and BSDAs whose holdings went above the limit become
// regular accounts. The changes go to the depository as BO modifications
// and each client is told.

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
	Code   string    `json:"code"` // the reason code sent to the depository
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
		switch {
		case r.BSDA && over:
			l.From, l.To, l.Code = "BSDA", "Regular", "LIMIT"
			l.Reason = fmt.Sprintf("Holdings of ₹%s are above the BSDA limit of ₹%s", formatINR(r.Value), formatINR(rev.Limit))
			rev.Lines = append(rev.Lines, l)
		case r.BSDA:
			rev.BSDA++
		case over:
		case c.Type != "Individual":
		case perPAN[strings.ToUpper(c.PAN)] > 1:
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
func (o *Ops) BSDAReviews(ctx context.Context, tenant string) ([]BSDAReview, error) {
	rs, err := docs.List[BSDAReview](ctx, o.Docs, tenant, cBSDAReview, "")
	sort.Slice(rs, func(i, j int) bool { return rs[i].ID > rs[j].ID })
	if rs == nil {
		rs = []BSDAReview{}
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
	rows := [][]string{{"BOID", "ClientCode", "Name", "BSDAFlag", "ReasonCode", "HoldingValue", "AsOn"}}
	for _, l := range rev.Lines {
		c, err := o.Client(ctx, tenant, l.UCC)
		if err != nil {
			continue
		}
		b := o.bo(ctx, tenant, c)
		b.BSDA = l.To == "BSDA"
		if err := docs.Upsert(ctx, o.Docs, tenant, cBO, b.UCC, b, actor); err != nil {
			return rev, err
		}
		flag := "N"
		if b.BSDA {
			flag = "Y"
		}
		rows = append(rows, []string{l.BOID, l.UCC, l.Name, flag, l.Code, l.Value.Fixed(2), fmtDDMMYYYY(rev.AsOf)})
		what := "converted to a Basic Services Demat Account (lower charges)"
		if !b.BSDA {
			what = "changed to a regular demat account because the holdings went above the BSDA limit"
		}
		o.notify(ctx, tenant, actor, "dp.bsda", l.UCC, map[string]string{"what": what, "value": formatINR(l.Value), "limit": formatINR(rev.Limit), "date": rev.AsOf})
	}
	data := csvBytesOf(rows)
	name := fmt.Sprintf("BSDA_MOD_%s.csv", strings.ReplaceAll(o.now().Format("2006-01-02"), "-", ""))
	rf, err := o.App.Ingest.Raw.Put(ctx, ingest.RawFile{Tenant: tenant, Source: "PERIODIC-OUT", Name: name, SHA256: sha256Hex(data), Size: int64(len(data)), Content: data, ReceivedAt: o.now()})
	if err != nil {
		return rev, err
	}
	rev.Status, rev.AppliedBy, rev.AppliedAt, rev.FileID, rev.FileName = "Applied", actor, o.now().Format("2006-01-02 15:04"), rf.ID, name
	if err := docs.Upsert(ctx, o.Docs, tenant, cBSDAReview, rev.ID, rev, actor); err != nil {
		return rev, err
	}
	o.log(ctx, tenant, actor, "dp.bsda_applied", rev.ID, map[string]any{"changes": len(rev.Lines), "file": name})
	return rev, nil
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
	if err := docs.Upsert(ctx, o.Docs, tenant, cBO, b.UCC, b, actor); err != nil {
		return b, err
	}
	o.log(ctx, tenant, actor, "dp.bsda_choice", b.UCC, map[string]any{"opt_out": optOut, "other_demat": otherDemat})
	return b, nil
}
