package ops

import (
	"context"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
	"ledgerline/internal/xfile"
)

// ---- stamp duty on off-market transfers, from CDSL's transaction export ----
//
// Stamp duty on a transfer of securities other than through an exchange
// is collected by the depository (Indian Stamp Act, 2019 amendments).
// CDSL's transaction export (COD) gives each transfer's stamp duty
// (StmpDty) and who pays it (StmpDtyInd): DP when it is paid through the
// DP, which recovers it from the client, CL when the client paid it
// directly. Each DP-paid duty goes on the client's next DP bill as a line
// of its own: not part of the taxable charges (no GST), credited to the
// payable to the depository for stamp duty, which CDSL bills the DP. This
// replaces loading CDSL's monthly stamp duty file. One duty is billed once
// per CDSL transaction: to the transferor when the export shows both sides.

const (
	cDPStamp       = "dpstamp"                  // CDSL transaction ID → DPStamp
	PayableDPStamp = "PAYABLE:DEPOSITORY_STAMP" // stamp duty collected for the depository
)

// DPStamp is the stamp duty of one transfer to recover from a client.
type DPStamp struct {
	ID     string    `json:"id"` // CDSL's transaction ID
	UCC    string    `json:"ucc"`
	BOID   string    `json:"bo_id"`
	ISIN   string    `json:"isin"`
	Date   string    `json:"date"`
	Amount money.Dec `json:"amount"`
	Debit  bool      `json:"debit"`            // the transferor's row
	Billed string    `json:"billed,omitempty"` // the DP bill it is on
	Seen   string    `json:"seen"`
}

// dpStampOf reads a transaction export row's stamp duty paid through the
// DP, if any.
func dpStampOf(r xfile.Row) (money.Dec, bool) {
	if !strings.EqualFold(strings.TrimSpace(r.Get("StmpDtyInd")), "DP") {
		return money.Dec{}, false
	}
	v, err := money.ParseDec(strings.TrimSpace(r.Get("StmpDty")))
	if err != nil || v.Sign() <= 0 {
		return money.Dec{}, false
	}
	return v, true
}

// dpStampSeen keeps a transfer's stamp duty. A transaction is kept once;
// the transferor's row takes the place of the other side's until billed.
func (o *Ops) dpStampSeen(ctx context.Context, tenant, actor string, s DPStamp) {
	s.ID = strings.TrimSpace(s.ID)
	if s.ID == "" || s.UCC == "" {
		return
	}
	if old, _, err := docs.Get[DPStamp](ctx, o.Docs, tenant, cDPStamp, s.ID); err == nil && (old.Billed != "" || old.Debit || !s.Debit) {
		return
	}
	s.Seen = o.now().Format(time.RFC3339)
	_ = docs.Upsert(ctx, o.Docs, tenant, cDPStamp, s.ID, s, actor)
}

// dpStampsDue are a client's unbilled stamp duties to the end of a month,
// on one of the client's accounts (acct as holdingAcct gives it).
func dpStampsDue(c Client, acct, end string, all []DPStamp) []DPStamp {
	var out []DPStamp
	for _, s := range all {
		if s.UCC == c.UCC && s.Billed == "" && s.Date <= end && holdingAcct(c, s.BOID) == acct {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Date < out[j].Date || (out[i].Date == out[j].Date && out[i].ID < out[j].ID)
	})
	return out
}
