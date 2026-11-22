package ops

import (
	"context"
	"fmt"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// ---- loading opening lots and 31 January 2018 values (c7, i7) ----
//
// FIRM-LOTS brings the firm's own holdings in from the old system lot by
// lot, as at the day before go-live: each lot with its portfolio, the
// date it was acquired, its cost and (optionally) its kind, whether it is
// a long-term or current investment, and its fair market value on 31
// January 2018. The lots count in the holdings at once; their entry (Dr
// the securities accounts, Cr the account the opening balances were
// carried in) is posted when a second person agrees (PostFirmSecOpening).
// Loading again replaces lots not yet posted. The migration tool writes
// the same file where the old system keeps the firm's own lots; any
// reading of a particular system stays in that system's mapper.
//
// FMV-2018 gives the fair market value per unit on 31 January 2018 for
// the grandfathering of long-term gains (cgtax.go), for the firm and its
// clients.

func firmSecSources() []masterSrc {
	return []masterSrc{
		{"FIRM-LOTS", "The firm's own securities, lot by lot, from the previous back office (as at the day before go-live). Portfolio is INV (investments) or SIT (stock-in-trade); AcqDate the date the lot was acquired; Cost the lot's total cost. Optional columns: Kind (EQ, ETF, MFE, MFO, BOND, GSEC, SGB, UNL; guessed from the ISIN when blank), Term (LT or CUR, investments only), FMV2018 (fair market value per unit on 31 January 2018), Ref. Loading again replaces lots not yet posted; a second person posts them on Firm securities → Holdings", []string{"Portfolio", "ISIN", "Symbol", "Qty", "AcqDate", "Cost"}, true, "Once at go-live (as at the day before)"},
		{"FMV-2018", "Fair market value per unit on 31 January 2018 (the highest price quoted that day on a recognised stock exchange; the NAV for fund units), for the grandfathering of long-term gains on shares and equity fund units bought before 1 February 2018 (section 55(2)(ac)). Rows replace the value of the same ISIN", []string{"ISIN", "FMV"}, false, "Once"},
	}
}

func (o *Ops) prepareFirmSec(ctx context.Context, tenant, actor, source, date string, rows []row) (func() (int, int, error), []ingest.ParseError) {
	var errs []ingest.ParseError
	bad := func(l int, f, m string, a ...any) {
		errs = append(errs, ingest.ParseError{Line: l, Field: f, Msg: fmt.Sprintf(m, a...)})
	}
	if source == "FMV-2018" {
		var xs []FMV2018
		for _, r := range rows {
			x := FMV2018{ISIN: strings.ToUpper(r.get("ISIN")), By: actor}
			v, err := money.ParseDec(r.get("FMV"))
			if err != nil || v.Sign() <= 0 {
				bad(r.line, "FMV", "must be a price above zero, got %q", r.get("FMV"))
			}
			if len(x.ISIN) != 12 {
				bad(r.line, "ISIN", "must be 12 characters")
			}
			x.FMV = v
			xs = append(xs, x)
		}
		return func() (int, int, error) {
			for _, x := range xs {
				if err := docs.Upsert(ctx, o.Docs, tenant, cFMV2018, x.ISIN, x, actor); err != nil {
					return 0, 0, err
				}
			}
			return len(xs), 0, nil
		}, errs
	}
	s := o.FirmSecSettings(ctx, tenant)
	if s.Portfolios == FSNone {
		bad(0, "Portfolio", "the firm keeps no securities of its own yet: answer Settings → Firm practices → Firm securities first")
		return nil, errs
	}
	old := o.fsTxns(ctx, tenant)
	for _, t := range old {
		if t.Type == FSTypeOpening && t.Status == FSPosted {
			bad(0, "", "opening lots were already posted (%s): enter further lots as manual entries", t.ID)
			return nil, errs
		}
	}
	now := o.now().Format(time.RFC3339)
	var ts []FSTxn
	for i, r := range rows {
		p := strings.ToUpper(r.get("Portfolio"))
		switch p {
		case "INVESTMENTS", "INVESTMENT":
			p = PortInv
		case "STOCK-IN-TRADE", "STOCK IN TRADE", "STOCK":
			p = PortSIT
		}
		if !s.has(p) {
			bad(r.line, "Portfolio", "must be one of the firm's portfolios (INV or SIT), got %q", r.get("Portfolio"))
		}
		t := FSTxn{ID: fmt.Sprintf("OL-%s-%05d", strings.ReplaceAll(date, "-", ""), i+1), Date: date, AcqDate: r.get("AcqDate"), PayDate: date, Portfolio: p, Side: "B", Type: FSTypeOpening,
			ISIN: strings.ToUpper(r.get("ISIN")), Symbol: strings.ToUpper(r.get("Symbol")), Ref: r.get("Ref"), Status: FSLoaded, Maker: actor, Created: now, Note: "Loaded from FIRM-LOTS"}
		if len(t.ISIN) != 12 {
			bad(r.line, "ISIN", "must be 12 characters")
		}
		if !ledger.ValidDate(t.AcqDate) || t.AcqDate > date {
			bad(r.line, "AcqDate", "must be a date (YYYY-MM-DD) on or before %s", date)
		}
		q, err := parseQty(r.get("Qty"))
		if err != nil || q.Sign() <= 0 {
			bad(r.line, "Qty", "must be above zero")
		}
		c, err := money.ParseAmount(r.get("Cost"))
		if err != nil || c < 0 {
			bad(r.line, "Cost", "must be an amount in rupees")
		}
		t.Qty, t.Value = q, c
		k := strings.ToUpper(r.get("Kind"))
		if k == "" {
			k = kindOfTrade("", t.ISIN, t.Symbol, "")
		}
		t.Kind = s.kind(k).ID
		if t.Portfolio == PortInv {
			t.Term = TermLong
			switch strings.ToUpper(r.get("Term")) {
			case "", "LT":
			case "CUR", "CURRENT":
				t.Term = TermCurrent
			default:
				bad(r.line, "Term", "must be LT or CUR")
			}
		}
		if f := r.get("FMV2018"); f != "" {
			v, err := money.ParseDec(f)
			if err != nil || v.Sign() <= 0 {
				bad(r.line, "FMV2018", "must be a price per unit above zero")
			}
			t.FMV2018 = v
		}
		ts = append(ts, t)
	}
	return func() (int, int, error) {
		for _, t := range old {
			if t.Type == FSTypeOpening && t.Status == FSLoaded {
				t.Status, t.Reason = FSCancelled, "replaced by a later FIRM-LOTS load"
				if err := docs.Upsert(ctx, o.Docs, tenant, cFSTxn, t.ID, t, actor); err != nil {
					return 0, 0, err
				}
			}
		}
		for _, t := range ts {
			if err := docs.Upsert(ctx, o.Docs, tenant, cFSTxn, t.ID, t, actor); err != nil {
				return 0, 0, err
			}
		}
		o.raiseWork(ctx, tenant, "firmsec", "opening|"+date, "Firm's opening lots to post",
			fmt.Sprintf("%s loaded %d opening lots of the firm's own securities as at %s. A second person checks them on Firm securities → Holdings and posts them to the books.", actor, len(ts), date), "", RoleChecker, "firmsec", date)
		o.log(ctx, tenant, actor, "firm_securities.opening_loaded", date, map[string]any{"lots": len(ts)})
		return len(ts), 0, nil
	}, errs
}
