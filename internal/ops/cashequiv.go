package ops

import (
	"context"
	"fmt"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/xfile"
)

// ---- NSE Clearing's cash-equivalent collateral list ----
//
// The clearing corporation publishes, about monthly, the liquid mutual
// fund schemes and ETFs it accepts as cash-equivalent collateral. Its
// approved securities file (ncl-approved-securities: symbol, ISIN, price,
// haircut) and its haircut file (ncl-cb-haircut) carry no cash / non-cash
// category, so they cannot say which ISINs are cash equivalent; this list
// does. The firm loads it on Exchange files (layout ncl-cash-equivalent);
// Ledgerline has no fetch of NSE Clearing's public website files, so it is
// not fetched.
//
// Ledgerline has no sample of the list: the layout reads it by its column
// headings (xfile/headers.go), only the ISIN is needed, and the heading
// texts are to confirm with a sample (add NSE Clearing's text as an alias
// in config/formats/ncl-cash-equivalent.json when it differs). A row whose
// type or category column says non-cash is left out, in case the list
// carries both kinds.
//
// Each load replaces the list as a whole, so a scheme dropped by the
// clearing corporation stops counting. cashEquivalentISINs (rms.go) uses
// it with government securities and the firm's typed list on Connections
// → Finspot uploads, which adds ISINs and can leave one out (-ISIN).

const (
	CashEqListNCL = "ncl-cash-equivalent"
	cCashEqList   = "casheqlist" // "ncl" -> CashEqList
)

// CashEqList is the cash-equivalent list last loaded.
type CashEqList struct {
	ID       string            `json:"id"`
	Date     string            `json:"date"` // the list's date (from its file name), else the day it was loaded
	File     string            `json:"file"`
	FileID   string            `json:"file_id,omitempty"`
	ISINs    map[string]string `json:"isins"` // ISIN -> scheme name (may be "")
	Rows     int               `json:"rows"`
	NonCash  int               `json:"non_cash,omitempty"` // rows left out: their type says non-cash
	Bad      int               `json:"bad,omitempty"`      // rows without an ISIN
	By       string            `json:"by"`
	At       string            `json:"at"`
	Previous int               `json:"previous"` // ISINs on the list it replaced
}

// nclCashEqISINs is the loaded list's ISINs (none before a list is loaded).
func (o *Ops) nclCashEqISINs(ctx context.Context, tenant string) map[string]string {
	l, _, err := docs.Get[CashEqList](ctx, o.Docs, tenant, cCashEqList, "ncl")
	if err != nil {
		return nil
	}
	return l.ISINs
}

// cashEqNonCash says whether a list row's type says it is not cash
// equivalent ("Non-cash", "Non Cash Equivalent").
func cashEqNonCash(typ string) bool {
	return strings.Contains(normHeaderText(typ), "noncash")
}

// normHeaderText is a value compared without case, spaces or punctuation.
func normHeaderText(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// applyCashEqFile replaces the cash-equivalent list with a loaded file's.
// A file with no ISIN is refused, so a wrong file never empties the list.
func (o *Ops) applyCashEqFile(ctx context.Context, tenant, actor string, lf *listFile) (CashEqList, error) {
	l := CashEqList{ID: "ncl", Date: firstOf(ymdFromName(lf.name), lf.date), File: lf.name, FileID: lf.fileID, ISINs: map[string]string{}, By: actor, At: o.now().Format(time.RFC3339)}
	err := lf.feed(func(r xfile.Row) {
		l.Rows++
		isin := strings.ToUpper(strings.TrimSpace(r.Get("isin")))
		if len(isin) != 12 || !strings.HasPrefix(isin, "IN") {
			l.Bad++
			return
		}
		if cashEqNonCash(r.Get("type")) {
			l.NonCash++
			return
		}
		l.ISINs[isin] = strings.TrimSpace(firstOf(r.Get("name"), r.Get("symbol")))
	})
	if err != nil {
		return l, err
	}
	l.Bad += lf.issues
	if len(l.ISINs) == 0 {
		return l, invalid("%s has no cash-equivalent ISIN: the list was not changed", lf.name)
	}
	l.Previous = len(o.nclCashEqISINs(ctx, tenant))
	if err := docs.Upsert(ctx, o.Docs, tenant, cCashEqList, l.ID, l, actor); err != nil {
		return l, err
	}
	o.log(ctx, tenant, actor, "collateral.cash_eq_list", lf.name, map[string]any{"date": l.Date, "isins": len(l.ISINs), "previous": l.Previous, "non_cash": l.NonCash, "bad": l.Bad})
	return l, nil
}

// summary says in a line what a load did.
func (l CashEqList) summary() string {
	s := fmt.Sprintf("%d cash-equivalent ISIN%s (the list before had %d)", len(l.ISINs), plural(len(l.ISINs)), l.Previous)
	if l.NonCash > 0 {
		s += fmt.Sprintf("; %d non-cash row%s left out", l.NonCash, plural(l.NonCash))
	}
	if l.Bad > 0 {
		s += fmt.Sprintf("; %d row%s without an ISIN left out", l.Bad, plural(l.Bad))
	}
	return s
}
