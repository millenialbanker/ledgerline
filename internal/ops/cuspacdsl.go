package ops

import (
	"context"
	"fmt"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// ---- CUSPA instructions to CDSL by the pledge API (CDSL API Handbook
// §5.20) ----
//
// At pay-out the depository pledges a client's unpaid securities to the
// firm's CUSPA by itself. CDSL's transaction export (COD) shows each such
// pledge (CuspaTxnFlg TM or CM, the pledge created); its transaction ID
// is the pledge sequence number (PSN). Ledgerline keeps every one seen and
// puts its PSN on the client's CUSPA line for the ISIN.
//
// With the PSN on a line and CDSL's pledge API on (Connections → CDSL
// APIs (DP), "Normal pledge, unpledge and confiscate"), the firm's release
// of the line goes to CDSL at once as an unpledge by the pledgee, and an
// invocation as a confiscation with the invocation value; CDSL's answer
// goes on the line. The release or invocation was already approved on the
// CUSPA screen (or made by the daily review under the 2.25× rule), so no
// second approval is asked. Lines without a PSN, or with the API off, stay
// on the day's depository list to be given on CDSL's system, as before.
//
// To confirm in CDSL's mock environment: that the unpledge and
// confiscation APIs take the automatic CUSPA pledges, and that the COD
// transaction ID of the pledge created is the PSN they expect.

const (
	cCUSPAPSN = "cuspapsn" // PSN → CUSPAPledge
	cuspaOf   = "CU"       // PledgeRelease.Of for the depository's CUSPA pledges
)

// CUSPAPledge is an automatic pledge to the firm's CUSPA seen in CDSL's
// transaction export.
type CUSPAPledge struct {
	PSN   string `json:"psn"`
	UCC   string `json:"ucc"`
	BOID  string `json:"bo_id"`
	ISIN  string `json:"isin"`
	Qty   string `json:"qty"`
	Date  string `json:"date"`  // the pledge's execution date (the pay-out day)
	CUSPA string `json:"cuspa"` // the CUSPA line it was put on
	Seen  string `json:"seen"`
}

// cuspaPledgeSeen keeps an automatic CUSPA pledge from CDSL's transaction
// export and puts it on its CUSPA line when there is one.
func (o *Ops) cuspaPledgeSeen(ctx context.Context, tenant, actor string, p CUSPAPledge) {
	p.PSN = strings.TrimLeft(strings.TrimSpace(p.PSN), "0")
	if p.PSN == "" || p.UCC == "" || p.ISIN == "" {
		return
	}
	if _, _, err := docs.Get[CUSPAPledge](ctx, o.Docs, tenant, cCUSPAPSN, p.PSN); err == nil {
		return
	}
	p.Seen = o.now().Format(time.RFC3339)
	if err := docs.Upsert(ctx, o.Docs, tenant, cCUSPAPSN, p.PSN, p, actor); err != nil {
		return
	}
	o.cuspaLinkPSNs(ctx, tenant, actor)
}

// cuspaLinkPSNs puts each pledge not yet on a line on the client's open
// CUSPA line for the ISIN: the one paid out on the pledge's date, else the
// oldest without a PSN. A line made after the export was read gets its
// PSN here too (the daily review, and before anything is sent).
func (o *Ops) cuspaLinkPSNs(ctx context.Context, tenant, actor string) int {
	ps, _ := docs.List[CUSPAPledge](ctx, o.Docs, tenant, cCUSPAPSN, "")
	var items []CUSPAItem
	n := 0
	for _, p := range ps {
		if p.CUSPA != "" {
			continue
		}
		if items == nil {
			items, _ = docs.List[CUSPAItem](ctx, o.Docs, tenant, cCUSPA, "")
		}
		best := -1
		for i, it := range items {
			if it.UCC != p.UCC || !strings.EqualFold(it.ISIN, p.ISIN) || it.PSN != "" || !it.open() {
				continue
			}
			switch {
			case best < 0:
				best = i
			case (it.PayOut == p.Date) != (items[best].PayOut == p.Date):
				if it.PayOut == p.Date {
					best = i
				}
			case it.Date < items[best].Date:
				best = i
			}
		}
		if best < 0 {
			continue
		}
		id := items[best].ID
		_, err := docs.Update(ctx, o.Docs, tenant, cCUSPA, id, actor, func(x *CUSPAItem) error {
			if x.PSN != "" {
				return fmt.Errorf("%w: %s has PSN %s", ErrState, x.ID, x.PSN)
			}
			x.PSN = p.PSN
			x.Events = append(x.Events, o.cuspaEvent(firstOf(p.Date, o.now().Format("2006-01-02")), "PSN", 0, actor, "CDSL's automatic pledge "+p.PSN+" to the CUSPA (transaction export)"))
			return nil
		})
		if err != nil {
			continue
		}
		items[best].PSN = p.PSN
		p.CUSPA = id
		_ = docs.Upsert(ctx, o.Docs, tenant, cCUSPAPSN, p.PSN, p, actor)
		n++
	}
	return n
}

// cuspaToCDSL sends a CUSPA line's release (UN) or invocation (CF) of q
// shares to CDSL when the line has its PSN and the pledge API is on, and
// says how it went ("" when nothing was sent: give it on CDSL's system).
func (o *Ops) cuspaToCDSL(ctx context.Context, tenant, actor, date, id, kind string, q int64, value money.Dec, why string) string {
	if q <= 0 || !o.cdslFamilyOn(ctx, tenant, cdslFamPledge) {
		return ""
	}
	o.cuspaLinkPSNs(ctx, tenant, actor)
	it, _, err := docs.Get[CUSPAItem](ctx, o.Docs, tenant, cCUSPA, id)
	if err != nil || it.PSN == "" {
		return ""
	}
	n, err := o.next(ctx, tenant, map[string]string{"UN": "cdslun", "CF": "cdslcf"}[kind], actor)
	if err != nil {
		return ""
	}
	c, _ := o.Client(ctx, tenant, it.UCC)
	r := PledgeRelease{ID: fmt.Sprintf("%s-%d", kind, n), Kind: kind, UCC: it.UCC, Name: c.Name, Of: cuspaOf, PSN: it.PSN, ISIN: it.ISIN, Symbol: it.Symbol, Qty: q,
		Reason: truncate(firstOf(why, "CUSPA "+map[string]string{"UN": "release", "CF": "invocation"}[kind]), 100), Source: "CUSPA", CUSPA: it.ID,
		Status: map[string]string{"UN": "Pending", "CF": "Notice given"}[kind], RequestedBy: actor, ApprovedBy: actor, At: o.now().Format(time.RFC3339)}
	if kind == "CF" {
		r.Value = value.Fixed(3)
	}
	if _, err := docs.Put(ctx, o.Docs, tenant, cCPLRelease, r.ID, r, 0, actor); err != nil {
		return ""
	}
	sent, err := o.cplSendRelease(ctx, tenant, actor, r)
	if err != nil && (sent.Status == "Pending" || sent.Status == "Notice given") {
		// not sent at all: CDSL never saw it
		sent, _ = docs.Update(ctx, o.Docs, tenant, cCPLRelease, r.ID, SystemActor, func(x *PledgeRelease) error {
			x.Status, x.ErrMsg, x.DoneAt = "Failed", errText(err), o.now().Format(time.RFC3339)
			return nil
		})
	}
	if sent.Status != "Done" { // done is put on the line by cplReleaseDone
		o.cuspaCDSLResult(ctx, tenant, sent)
	}
	return sent.Status
}

// cuspaCDSLResult puts how a CUSPA release or invocation went at CDSL on
// its line (on the event it was sent for), and asks staff to give one CDSL
// did not do on CDSL's system.
func (o *Ops) cuspaCDSLResult(ctx context.Context, tenant string, r PledgeRelease) {
	if r.CUSPA == "" {
		return
	}
	what := map[string]string{"UN": "Released", "CF": "Invoked"}[r.Kind]
	res := r.ID + " " + r.Status
	if r.Status == "Failed" || r.Status == "Rejected" {
		res += ": " + firstOf(strings.TrimSpace(r.ErrCode+" "+r.ErrMsg), "not done by CDSL")
	}
	it, err := docs.Update(ctx, o.Docs, tenant, cCUSPA, r.CUSPA, SystemActor, func(x *CUSPAItem) error {
		for i := len(x.Events) - 1; i >= 0; i-- {
			e := &x.Events[i]
			if e.What == what && e.Qty == r.Qty && (e.CDSL == "" || strings.HasPrefix(e.CDSL, r.ID+" ")) {
				e.CDSL = res
				break
			}
		}
		x.CDSL = res
		return nil
	})
	if err != nil {
		return
	}
	switch r.Status {
	case "Done":
		o.closeWork(ctx, tenant, SystemActor, "cuspa", it.ID+":cdsl", "done at CDSL")
	case "Failed", "Rejected": // a missing reply is checked at CDSL first (cplSendRelease)
		act := map[string]string{"UN": "release", "CF": "invocation"}[r.Kind]
		o.raiseWork(ctx, tenant, "cuspa", it.ID+":cdsl", fmt.Sprintf("Give the CUSPA %s of %s at CDSL", act, it.UCC),
			fmt.Sprintf("CDSL's API did not do the %s of %d %s (CUSPA line %s, PSN %s): %s. Give it on CDSL's system (Settlement → Unpaid securities → Depository list), or record CDSL's result on Pledges (CDSL).", act, r.Qty, it.Symbol, it.ID, it.PSN, res),
			it.UCC, RoleRisk, "settlement", "")
	}
}
