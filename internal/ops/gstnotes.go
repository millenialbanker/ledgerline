package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// ---- GST credit and debit notes for bills corrected late ----
//
// A corrected bill (notes.go CorrectBill) is reversed and posted again on
// its trade date, so the contract note, which is the tax invoice for the
// brokerage and charges, changes. Within the month, or before the month's
// GSTR-1 is due (the 11th of the next month), the register simply shows
// the corrected invoice. After that the month's return is filed: the
// invoice stays as filed, and the change is a credit note (less brokerage)
// or a debit note (more) of the period the correction was made in (CGST
// Act s.34, rule 53), declared in that period's GSTR-1 (table 9B for
// clients with a GSTIN, netted into table 7 for the others) and GSTR-3B.
//
// GST() leaves such corrections out of the original period and lists them
// as notes of the correction's period; IssueGSTNotes numbers them (one
// series a year each for credit and debit notes) so the register and the
// return carry the note numbers.
//
// To confirm: the cut-off assumes monthly GSTR-1 filed by the 11th; a firm
// that files earlier, or quarterly, sees notes for corrections it could
// still have amended.

const cGSTNote = "gstnote" // group key -> GSTNote

// GSTNote is an issued credit or debit note.
type GSTNote struct {
	Key      string       `json:"key"`
	No       string       `json:"no"`
	Date     string       `json:"date"`
	Kind     string       `json:"kind"` // Credit note, Debit note
	UCC      string       `json:"ucc"`
	Orig     string       `json:"original_invoice"`
	OrigDate string       `json:"original_date"`
	Taxable  money.Amount `json:"taxable"`
	GST      money.Amount `json:"gst"`
	By       string       `json:"by"`
	At       string       `json:"at"`
}

// gstr1Cut is the last day a correction still changes the month's own
// GSTR-1: the 10th of the next month.
func gstr1Cut(valueDate string) string {
	if len(valueDate) < 7 {
		return valueDate
	}
	return addMonths(valueDate[:7]+"-01", 1)[:7] + "-10"
}

func gstDay(t time.Time) string { return t.In(ist).Format("2006-01-02") }

func gstBillKind(k string) bool { return k == "BILL" || k == "MTM" || k == "EXPIRY" }

// gstLateAdjustments finds the reversals of bills made after the month's
// GSTR-1 was due, and the bills posted again after them: each entry ID
// maps to its correction's key (bill source ID and the correction's day).
func gstLateAdjustments(all []ledger.Entry, byID map[string]ledger.Entry) map[string]string {
	type rev struct {
		at  time.Time
		key string
	}
	revs := map[string][]rev{} // bill source ID -> late reversals
	out := map[string]string{}
	for _, e := range all {
		if e.Source.Kind != "REVERSAL" {
			continue
		}
		orig, ok := byID[e.ReversalOf]
		if !ok || !gstBillKind(orig.Source.Kind) || gstDay(e.CreatedAt) <= gstr1Cut(orig.ValueDate) {
			continue
		}
		k := orig.Source.ID + "|" + gstDay(e.CreatedAt)
		revs[orig.Source.ID] = append(revs[orig.Source.ID], rev{e.CreatedAt, k})
		out[e.ID] = k
	}
	for id := range revs {
		sort.Slice(revs[id], func(i, j int) bool { return revs[id][i].at.Before(revs[id][j].at) })
	}
	for _, e := range all {
		rs := revs[e.Source.ID]
		if !gstBillKind(e.Source.Kind) || len(rs) == 0 {
			continue
		}
		for i := len(rs) - 1; i >= 0; i-- {
			if !e.CreatedAt.Before(rs[i].at) {
				out[e.ID] = rs[i].key
				break
			}
		}
	}
	return out
}

func gstNoteID(key string) string { return strings.NewReplacer("/", "_", "|", "_").Replace(key) }

// gstNoteLines are the notes whose corrections were made in from..to.
func (o *Ops) gstNoteLines(ctx context.Context, tenant, from, to string, late map[string]string, all []ledger.Entry, cs map[string]Client, rate money.Dec, pos func(Client) (string, bool), warn *[]string) ([]GSTLine, []GSTLine) {
	type acc struct{ taxable, gst money.Amount }
	groups := map[string]*acc{}
	for _, e := range all {
		k, ok := late[e.ID]
		if !ok {
			continue
		}
		if d := k[strings.LastIndex(k, "|")+1:]; d < from || d > to {
			continue
		}
		a := groups[k]
		if a == nil {
			a = &acc{}
			groups[k] = a
		}
		for _, x := range e.Postings {
			switch {
			case x.Account == ledger.IncomeBrokerage, strings.HasPrefix(x.Account, "PAYABLE:EXCH_TXN"), x.Account == ledger.PayableSEBIFee:
				a.taxable += x.Credit - x.Debit
			case x.Account == ledger.PayableGST:
				a.gst += x.Credit - x.Debit
			}
		}
	}
	notes, cdnr := []GSTLine{}, []GSTLine{}
	pending := 0
	for _, k := range sortedKeys(groups) {
		a := groups[k]
		if a.taxable == 0 && a.gst == 0 {
			continue
		}
		src, day := k[:strings.LastIndex(k, "|")], k[strings.LastIndex(k, "|")+1:]
		p := strings.Split(src, "/")
		if len(p) < 2 {
			continue
		}
		c := cs[p[1]]
		kind := "Debit note"
		if a.taxable < 0 || (a.taxable == 0 && a.gst < 0) {
			kind = "Credit note"
		}
		abs := func(x money.Amount) money.Amount {
			if x < 0 {
				return -x
			}
			return x
		}
		st, inter := pos(c)
		orig := "BILL " + p[0] + " " + p[1]
		if n, _, err := docs.Get[ContractNote](ctx, o.Docs, tenant, cNote, p[0]+"|"+p[1]); err == nil {
			orig = firstOf(n.Replaces, n.No)
		}
		l := GSTLine{Date: day, Kind: kind, UCC: p[1], Name: c.Name, GSTIN: c.GSTIN, POS: st, SAC: SACBroking, Taxable: abs(a.taxable), Rate: rate,
			Total: abs(a.taxable) + abs(a.gst), Orig: orig, OrigDate: p[0], Reason: "Bill corrected after the month's return", Key: k}
		l.IGST, l.CGST, l.SGST = splitGST(abs(a.gst), inter)
		if n, _, err := docs.Get[GSTNote](ctx, o.Docs, tenant, cGSTNote, gstNoteID(k)); err == nil {
			l.Invoice, l.Date = n.No, n.Date
		} else {
			pending++
		}
		notes = append(notes, l)
		if l.GSTIN != "" {
			cdnr = append(cdnr, l)
		}
	}
	if pending > 0 {
		*warn = append(*warn, fmt.Sprintf("%d bill correction(s) made after the month's GSTR-1 was due need a credit or debit note: issue them on this screen before filing.", pending))
	}
	return notes, cdnr
}

// IssueGSTNotes numbers the period's credit and debit notes not issued
// yet. It returns how many it issued.
func (o *Ops) IssueGSTNotes(ctx context.Context, tenant, actor, from, to string) (int, error) {
	if err := o.require(ctx, tenant, actor, "Issuing GST credit notes", RoleChecker, RoleCompliance); err != nil {
		return 0, err
	}
	r, err := o.GST(ctx, tenant, from, to)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, l := range r.Notes {
		if l.Invoice != "" {
			continue
		}
		series := map[string]string{"Credit note": "CRN", "Debit note": "DBN"}[l.Kind]
		seq, err := o.next(ctx, tenant, "gstnote:"+series+":"+fyLabel(l.Date), actor)
		if err != nil {
			return n, err
		}
		g := GSTNote{Key: l.Key, No: fmt.Sprintf("%s/%s/%06d", series, fyLabel(l.Date), seq), Date: l.Date, Kind: l.Kind, UCC: l.UCC, Orig: l.Orig, OrigDate: l.OrigDate,
			Taxable: l.Taxable, GST: l.IGST + l.CGST + l.SGST, By: actor, At: o.now().Format(time.RFC3339)}
		if _, err := docs.Put(ctx, o.Docs, tenant, cGSTNote, gstNoteID(l.Key), g, 0, actor); err != nil {
			return n, err
		}
		o.log(ctx, tenant, actor, "gst.note_issued", g.No, map[string]any{"ucc": g.UCC, "kind": g.Kind, "original": g.Orig, "taxable": g.Taxable.String()})
		n++
	}
	return n, nil
}
