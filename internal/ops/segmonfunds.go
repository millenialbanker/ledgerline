package ops

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
	"ledgerline/internal/xfile"
)

// ---- SEGMON: the daily client funds and collateral report (65 columns) ----
//
// Each trading member reports, every day (Monday to Saturday, by the end of
// the next day), every client's funds and collateral: received from the
// client, retained by the trading member, placed with the clearing member,
// retained by the clearing member and placed with the clearing corporation
// (SEBI circular SEBI/HO/MRD2_DCAP/CIR/2021/0598 of 20 July 2021, para 4).
// The layout is config/formats/segmon-client-funds.json, taken from the
// header row of the file; NSE Clearing's reporting-format circular
// (NCL/CMPL/54214 of 27 October 2022, cited by the public NSE circular
// NSE/INSP/57639 of 20 July 2023) holds the column definitions and is not
// reproduced here. What each column is filled from:
//
//	10 A  ledger balance: the client ledger at end of day, credit positive.
//	11 B  clear balance: A less receipts not yet cleared (col 53).
//	12 C  peak clear balance: the highest clear balance during the day,
//	      walking the start-of-day clear balance through the day's postings
//	      in the order they were posted (cheques count when they clear).
//	      Settlement bills due that day do not lower it: the peak is the
//	      higher of the walk with and without the day's bills.
//	16    approved securities, non-cash, received: securities pledged to the
//	      firm, after haircut, other than G-secs and T-bills (54).
//	20    credit in lieu of early pay-in: 100% of the value of the shares
//	      the client paid in early through CDSL that day (sale value, else
//	      closing price).
//	22/26 retained by the trading member: what was received and not placed.
//	30/34 placed with the clearing member: retained with it (38/42, nil in
//	      Ledgerline) plus placed with NSE Clearing.
//	46/50 placed with NSE Clearing: the client's end-of-day allocation to the
//	      segment (cash; non-cash re-pledged), the same split as the
//	      allocation file and CC01.
//	52    "NON MTF" for the client's own ledger in the cash segment, "MTF"
//	      for the margin trading ledger, "NA" in other segments
//	      (NSE/INSP/57639 items 5 and 6).
//	53    uncleared receipts: cheques credited but not cleared at day end.
//	54-58 G-secs and T-bills (ISINs IN00…) pledged, split the same way.
//	63    settlement amount: funds paid out in the running account
//	      settlement, reported in the file of the working day after the
//	      settlement date (NSE/INSP/57639 item 10; NSE/INSP/53115).
//	65    cash collateral for MTF positions, on the NON MTF row.
//
// Bank guarantees and fixed deposits are never taken from clients (client
// funds may not be used for BGs) and the firm's own deposits are not client
// collateral, so 13, 14, 23, 24, 31, 32, 39, 40, 47, 48 and 59-62 are nil;
// so are commodities (reported to MCXCCL), the pool account and other
// collateral, which Ledgerline does not hold per client.
//
// MTF: Ledgerline's books move the funded part of an MTF purchase to the
// MTF loan account, and the client's own margin is spent in the purchase.
// SEGMON reports the margin trading ledger with the cash collateral kept
// out of it and the client's ledger with it in (NSE/INSP/57639 items 5, 6),
// so the client's cash margin on open MTF positions (cost × margin %, never
// more than the client's clear credit would allow) is added to the NON MTF
// row and taken out of the MTF row. The two rows still add up to the books.
//
// Checks before the file is made (SEGMON check): the member and client PANs
// are well formed, the mandatory fields are there, no client, segment and
// indicator is repeated (NSE/INSP/57639 items 1, 7), received = retained
// + placed with the clearing member and placed with the clearing member =
// retained by it + placed with NSE Clearing for every kind of collateral
// (SEBI 2021/0598 para 11), the allocation does not exceed what the client
// gave (para 11), the peak is not below the clear balance, every client with
// a ledger balance or collateral is in the file, and the file's totals agree
// with the ledger and the day's allocation. Errors stop the file; warnings
// are shown.

// segmon column numbers (1-based, as the circulars number them).
const (
	sgDate, sgCMPAN, sgTMPAN, sgCPCode, sgCPPAN, sgClientPAN, sgAccType, sgSegment, sgUCC = 1, 2, 3, 4, 5, 6, 7, 8, 9
	sgA, sgB, sgPeak                                                                      = 10, 11, 12
	sgRecvBG, sgRecvFDR, sgRecvApprCash, sgRecvApprNonCash, sgRecvNonAppr, sgRecvComm     = 13, 14, 15, 16, 17, 18
	sgRecvOther, sgEPI, sgPool, sgRetTMCash                                               = 19, 20, 21, 22
	sgPlCMCash, sgRetCMCash, sgNCLCash, sgMTF, sgUncleared                                = 30, 38, 46, 52, 53
	sgGSecRecv, sgGSecRetTM, sgGSecPlCM, sgGSecRetCM, sgGSecNCL                           = 54, 55, 56, 57, 58
	sgSettle, sgUnclaimed, sgMTFCash                                                      = 63, 64, 65
)

// segmonChains are the collateral kinds as (received, retained by TM,
// placed with CM, retained with CM, placed with NCL); 0 where the layout has
// no such column (commodities and other collateral are not placed with NCL
// in this layout; non-approved securities cannot be).
var segmonChains = []struct {
	what                     string
	recv, retTM, plCM, retCM int
	ncl                      int
}{
	{"bank guarantees", 13, 23, 31, 39, 47},
	{"fixed deposits", 14, 24, 32, 40, 48},
	{"approved securities, cash component", 15, 25, 33, 41, 49},
	{"approved securities, non-cash component", 16, 26, 34, 42, 50},
	{"non-approved securities", 17, 27, 35, 43, 0},
	{"commodities", 18, 28, 36, 44, 51},
	{"other collateral", 19, 29, 37, 45, 0},
	{"G-secs and T-bills", 54, 55, 56, 57, 58},
}

var reSegPAN = regexp.MustCompile(`^[A-Z]{5}[0-9]{4}[A-Z]$`)

// SegmonIssue is one problem the SEGMON check found.
type SegmonIssue struct {
	UCC       string `json:"ucc,omitempty"`
	Segment   string `json:"segment,omitempty"`
	Indicator string `json:"indicator,omitempty"`
	Column    int    `json:"column,omitempty"`
	Field     string `json:"field,omitempty"`
	Message   string `json:"message"`
}

// SegmonTotals are the file's totals set against Ledgerline's books.
type SegmonTotals struct {
	Ledger       money.Dec `json:"ledger"`        // column 10, all rows
	Books        money.Dec `json:"books"`         // client and MTF ledgers in the books
	CashNCL      money.Dec `json:"cash_ncl"`      // column 46
	Allocated    money.Dec `json:"allocated"`     // the day's cash allocation (NSE Clearing segments)
	NonCashNCL   money.Dec `json:"non_cash_ncl"`  // columns 50 + 58
	AllocNonCash money.Dec `json:"alloc_noncash"` // the day's non-cash allocation
	Uncleared    money.Dec `json:"uncleared"`
	EPI          money.Dec `json:"epi"`
	Settlement   money.Dec `json:"settlement"`
	MTFCash      money.Dec `json:"mtf_cash"`
}

// SegmonCheck is the result of the pre-upload check.
type SegmonCheck struct {
	Date     string        `json:"date"`
	File     string        `json:"file"`
	Rows     int           `json:"rows"`
	Clients  int           `json:"clients"`
	Errors   []SegmonIssue `json:"errors"`
	Warnings []SegmonIssue `json:"warnings"`
	Totals   SegmonTotals  `json:"totals"`
	OK       bool          `json:"ok"` // no errors: the file can be made
}

// segBal is one ledger account's day: balance, clear balance and peak clear
// balance, credit positive.
type segBal struct {
	A, B, Peak money.Dec
}

// segmonBalances walks the client and MTF loan ledgers up to date. Keys are
// ledger account names (CLIENT:<ucc>, RECEIVABLE:MTF:<ucc>).
func (o *Ops) segmonBalances(ctx context.Context, tenant, date string) (map[string]*segBal, error) {
	rs, err := docs.List[Receipt](ctx, o.Docs, tenant, cReceipt, "")
	if err != nil {
		return nil, err
	}
	chq := map[string]Receipt{} // receipt entry -> cheque
	for _, r := range rs {
		if r.Mode == "Cheque" && r.Entry != "" && (r.Status == "Uncleared" || r.ClearedOn != "") {
			chq[r.Entry] = r
		}
	}
	es, err := o.App.Ledger.Store.List(ctx, tenant, ledger.Filter{To: date})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(es, func(i, j int) bool { return es[i].Seq < es[j].Seq }) // posting order
	type ev struct {
		amt  money.Dec
		bill bool
	}
	out := map[string]*segBal{}
	open := map[string]money.Dec{}
	day := map[string][]ev{}
	posted := map[string]bool{}
	tracked := func(a string) bool {
		return strings.HasPrefix(a, "CLIENT:") || strings.HasPrefix(a, "RECEIVABLE:MTF:")
	}
	for _, e := range es {
		posted[e.ID] = true
		_, isChq := chq[e.ID]
		bounced := false
		if r, ok := chq[e.ReversalOf]; ok && e.ReversalOf != "" && r.Status == "Bounced" {
			bounced = true
		}
		bill := e.ValueDate == date && (e.Source.Kind == "BILL" || e.Source.Kind == "REVERSAL")
		for _, p := range e.Postings {
			if !tracked(p.Account) {
				continue
			}
			d := (p.Credit - p.Debit).Dec()
			b := out[p.Account]
			if b == nil {
				b = &segBal{}
				out[p.Account] = b
			}
			b.A = b.A.Add(d)
			if isChq || bounced {
				continue // never clear, or clear when the bank clears it (below)
			}
			if e.ValueDate < date {
				open[p.Account] = open[p.Account].Add(d)
			} else {
				day[p.Account] = append(day[p.Account], ev{d, bill})
			}
		}
	}
	// Cheques count from the day the bank cleared them; the time of day is
	// not recorded, so a cheque cleared on the day counts after its postings.
	ids := sortedKeys(chq)
	for _, id := range ids {
		r := chq[id]
		if !posted[id] || r.Status != "Credited" || r.ClearedOn == "" || r.ClearedOn > date {
			continue
		}
		acct := ledger.ClientLedgerAccount(r.UCC, r.Ledger) // a commodity cheque is not in SEGMON
		if r.ClearedOn < date {
			open[acct] = open[acct].Add(r.Amount.Dec())
		} else {
			day[acct] = append(day[acct], ev{r.Amount.Dec(), false})
		}
	}
	for acct, b := range out {
		walk := func(skipBills bool) (end, peak money.Dec) {
			run := open[acct]
			peak = run
			for _, x := range day[acct] {
				if skipBills && x.bill {
					continue
				}
				run = run.Add(x.amt)
				peak = money.Max(peak, run)
			}
			return run, peak
		}
		end, p1 := walk(false)
		_, p2 := walk(true)
		b.B, b.Peak = end, money.Max(p1, p2)
	}
	return out, nil
}

// segmonClient is everything reported for one client.
type segmonClient struct {
	ucc, pan         string
	bal              segBal // client ledger (NON MTF), before the MTF cash
	mtf              *segBal
	mtfCash          money.Dec
	secs, gsec       money.Dec // pledged, after haircut
	epi              money.Dec
	settle           money.Dec
	alloc            map[string]AllocationRow
	segs             []string
	mtfBook, mtfOpen bool
}

// segmonRow is one line of the file, by column number.
type segmonRow struct {
	ucc, seg, ind string
	v             map[int]money.Dec
	s             map[int]string
}

// segmonData gathers the day's figures for every client.
func (o *Ops) segmonData(ctx context.Context, tenant, date string) (map[string]*segmonClient, []string, error) {
	set, _ := o.Settings(ctx, tenant)
	bals, err := o.segmonBalances(ctx, tenant, date)
	if err != nil {
		return nil, nil, err
	}
	cs, _ := o.ClientMap(ctx, tenant)
	al, err := o.Allocation(ctx, tenant, date)
	if err != nil {
		return nil, nil, err
	}
	in, err := o.riskInput(ctx, tenant, date)
	if err != nil {
		return nil, nil, err
	}
	all := map[string]*segmonClient{}
	get := func(u string) *segmonClient {
		c := all[u]
		if c == nil {
			c = &segmonClient{ucc: u, pan: strings.ToUpper(strings.TrimSpace(cs[u].PAN)), alloc: map[string]AllocationRow{}}
			all[u] = c
		}
		return c
	}
	for acct, b := range bals {
		if u, ok := strings.CutPrefix(acct, "CLIENT:"); ok {
			get(u).bal = *b
		}
	}
	for acct, b := range bals {
		if u, ok := strings.CutPrefix(acct, "RECEIVABLE:MTF:"); ok && (b.A.Sign() != 0 || b.Peak.Sign() != 0) {
			x := *b
			get(u).mtf = &x
		}
	}
	// Pledged securities after haircut, G-secs and T-bills apart.
	for _, h := range in.holdings {
		if h.Pledged <= 0 {
			continue
		}
		if i, ok := in.look.ByISIN(h.ISIN); ok {
			v := i.LTP.MulInt(h.Pledged).Pct(money.DecInt(100).Sub(i.HaircutPct)) // as live risk values it
			c := get(h.UCC)
			if strings.HasPrefix(strings.ToUpper(h.ISIN), "IN00") {
				c.gsec = c.gsec.Add(v)
			} else {
				c.secs = c.secs.Add(v)
			}
		}
	}
	for _, a := range al {
		if a.Segment == "COM" {
			continue // commodities are reported to MCXCCL
		}
		c := get(a.UCC)
		x := c.alloc[a.Segment]
		x.UCC, x.Segment = a.UCC, a.Segment
		x.Cash, x.NonCash, x.Required = x.Cash.Add(paise(a.Cash)), x.NonCash.Add(paise(a.NonCash)), x.Required.Add(paise(a.Required))
		c.alloc[a.Segment] = x
	}
	// Early pay-in: 100% of the value of the shares paid in early.
	if epis := o.EPIAccepted(ctx, tenant, date); len(epis) > 0 {
		ts, _ := o.exchangeTrades(ctx, tenant, date)
		_, look, _ := o.Market(ctx, tenant, date)
		type sv struct {
			qty int64
			val money.Dec
		}
		sold := map[string]*sv{}
		for _, t := range ts {
			if t.Segment != "CM" || t.Side != "S" {
				continue
			}
			isin := t.Symbol
			if i, ok := look.ForTrade(t); ok && i.ISIN != "" {
				isin = i.ISIN
			}
			k := t.UCC + "|" + isin
			if sold[k] == nil {
				sold[k] = &sv{}
			}
			sold[k].qty += t.Qty
			sold[k].val = sold[k].val.Add(t.Price.MulInt(t.Qty))
		}
		for _, r := range epis {
			var px money.Dec
			key := r.ISIN
			if key == "" { // a CEP allocation whose ISIN is not in the market data
				key = r.Symbol
			}
			if s := sold[r.UCC+"|"+key]; s != nil && s.qty > 0 {
				px, _ = s.val.Quo(money.DecInt(s.qty))
			} else if i, ok := look.ByISIN(r.ISIN); ok {
				px = i.LTP
			}
			c := get(r.UCC)
			c.epi = c.epi.Add(px.MulInt(r.Qty).Round(money.UnitPaisa).Dec())
		}
	}
	// Running account settlement: in the file of the working day after.
	if bs, err := o.RASBatches(ctx, tenant, set.PrevBusinessDay(date)); err == nil {
		for _, b := range bs {
			for _, it := range b.Items {
				c := get(it.UCC)
				c.settle = c.settle.Add(it.Amount.Dec())
			}
		}
	}
	// MTF: the client's cash margin on open positions.
	ps, _ := docs.List[MTFPosition](ctx, o.Docs, tenant, cMTFPos, "")
	for _, p := range ps {
		if p.Status != "Open" || p.BuyDate > date {
			continue
		}
		c := get(p.UCC)
		c.mtfOpen = true
		c.mtfCash = c.mtfCash.Add(p.Cost.Dec().Pct(p.MarginPct).Round(money.UnitPaisa).Dec())
	}
	var uccs []string
	for u, c := range all {
		c.secs, c.gsec = paise(c.secs), paise(c.gsec)
		// The allocation is made from the unrounded value; rounding both to
		// the paisa must not leave more placed than received.
		var nc money.Dec
		for _, a := range c.alloc {
			nc = nc.Add(a.NonCash)
		}
		if tot := c.secs.Add(c.gsec); nc.Cmp(tot) > 0 && nc.Sub(tot).Cmp(money.MustDec("0.05")) <= 0 {
			c.secs = c.secs.Add(nc.Sub(tot))
		}
		if c.mtf != nil || c.mtfOpen {
			c.mtfBook = true
			if c.mtf == nil {
				c.mtf = &segBal{}
			}
			// Never more cash collateral than the client's clear credit allows.
			c.mtfCash = money.Max(money.Min(c.mtfCash, c.bal.B.Add(c.mtfCash)), money.Dec{})
		}
		active := cs[u].Status == "Active"
		if !active && c.bal.A.Sign() == 0 && c.bal.Peak.Sign() == 0 && len(c.alloc) == 0 && c.secs.Sign() == 0 && c.gsec.Sign() == 0 && !c.mtfBook && c.epi.Sign() == 0 && c.settle.Sign() == 0 {
			continue
		}
		for s := range c.alloc {
			c.segs = append(c.segs, s)
		}
		if (c.epi.Sign() != 0 || c.mtfBook || len(c.segs) == 0) && !contains(c.segs, "CM") {
			c.segs = append(c.segs, "CM")
		}
		sort.SliceStable(c.segs, func(i, j int) bool { return segRank(c.segs[i]) < segRank(c.segs[j]) })
		uccs = append(uccs, u)
	}
	sort.Strings(uccs)
	return all, uccs, nil
}

// segmonRows lays the clients out as the file's lines: one per client and
// segment, and a second cash-segment line for a client with an MTF book.
// The ledger, received and retained figures are on the client's first line.
func segmonRows(all map[string]*segmonClient, uccs []string) []segmonRow {
	var out []segmonRow
	for _, u := range uccs {
		c := all[u]
		var placedCash, placedSecs, placedGSec money.Dec
		nc := map[string][2]money.Dec{} // segment -> securities, G-secs placed
		for _, s := range c.segs {
			a := c.alloc[s]
			placedCash = placedCash.Add(a.Cash)
			// Re-pledged collateral is split between G-secs and other
			// securities in proportion to what the client pledged.
			g := money.Dec{}
			if tot := c.secs.Add(c.gsec); tot.Sign() > 0 && c.gsec.Sign() > 0 {
				q, _ := a.NonCash.Mul(c.gsec).Quo(tot)
				g = q.Round(money.UnitPaisa).Dec()
			}
			nc[s] = [2]money.Dec{a.NonCash.Sub(g), g}
			placedSecs, placedGSec = placedSecs.Add(a.NonCash.Sub(g)), placedGSec.Add(g)
		}
		for i, s := range c.segs {
			r := segmonRow{ucc: u, seg: s, ind: "NA", v: map[int]money.Dec{}, s: map[int]string{}}
			if s == "CM" {
				r.ind = "NON MTF"
			}
			a := c.alloc[s]
			r.v[sgNCLCash], r.v[sgPlCMCash] = a.Cash, a.Cash
			r.v[50], r.v[34] = nc[s][0], nc[s][0]
			r.v[sgGSecNCL], r.v[sgGSecPlCM] = nc[s][1], nc[s][1]
			if i == 0 {
				r.v[sgA], r.v[sgB], r.v[sgPeak] = c.bal.A.Add(c.mtfCash), c.bal.B.Add(c.mtfCash), c.bal.Peak.Add(c.mtfCash)
				r.v[sgUncleared] = c.bal.A.Sub(c.bal.B)
				r.v[sgRecvApprNonCash], r.v[26] = c.secs, c.secs.Sub(placedSecs)
				r.v[sgGSecRecv], r.v[sgGSecRetTM] = c.gsec, c.gsec.Sub(placedGSec)
				r.v[sgRetTMCash] = money.Max(money.Max(r.v[sgB], money.Dec{}).Sub(placedCash), money.Dec{})
				r.v[sgSettle] = c.settle
			}
			if s == "CM" {
				r.v[sgEPI] = c.epi
				r.v[sgMTFCash] = c.mtfCash
			}
			out = append(out, r)
			if s == "CM" && c.mtfBook {
				m := segmonRow{ucc: u, seg: "CM", ind: "MTF", v: map[int]money.Dec{}, s: map[int]string{}}
				m.v[sgA], m.v[sgB], m.v[sgPeak] = c.mtf.A.Sub(c.mtfCash), c.mtf.B.Sub(c.mtfCash), c.mtf.Peak.Sub(c.mtfCash)
				out = append(out, m)
			}
		}
	}
	return out
}

// segmonColKey maps column numbers to the layout's field names.
func segmonColKey(s *xfile.Spec) func(int) string {
	return func(n int) string { return s.Columns[n-1].Key }
}

// SEGMONCheck runs the pre-upload check for a date's file.
func (o *Ops) SEGMONCheck(ctx context.Context, tenant, actor, date string) (SegmonCheck, error) {
	if err := o.require(ctx, tenant, actor, "Downloading exchange files", RoleMaker, RoleChecker, RoleRisk, RoleCompliance); err != nil {
		return SegmonCheck{}, err
	}
	if !validDate(date) {
		return SegmonCheck{}, invalid("give the date the file is for")
	}
	_, _, chk, err := o.segmonBuild(ctx, tenant, date, 1)
	return chk, err
}

// segmonBuild makes the file's rows and checks them.
func (o *Ops) segmonBuild(ctx context.Context, tenant, date string, seq int) ([]xfile.Row, *xfile.Spec, SegmonCheck, error) {
	chk := SegmonCheck{Date: date, Errors: []SegmonIssue{}, Warnings: []SegmonIssue{}}
	lib, err := XFiles()
	if err != nil {
		return nil, nil, chk, err
	}
	s, _ := lib.Get("segmon-client-funds")
	if len(s.Columns) != 65 {
		return nil, nil, chk, fmt.Errorf("the SEGMON layout has %d columns, not 65", len(s.Columns))
	}
	key := segmonColKey(s)
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return nil, nil, chk, err
	}
	pan := firmPAN(set)
	cmPAN := strings.ToUpper(firstNonEmptyStr(set.ClearingMemberPAN, pan))
	errf := func(is SegmonIssue) { chk.Errors = append(chk.Errors, is) }
	warn := func(is SegmonIssue) { chk.Warnings = append(chk.Warnings, is) }
	if !reSegPAN.MatchString(pan) {
		errf(SegmonIssue{Column: sgTMPAN, Field: s.Columns[sgTMPAN-1].Name, Message: "the firm's PAN is missing or not a PAN (5 letters, 4 digits, a letter): set it, or the GSTIN, in Settings"})
	}
	if !reSegPAN.MatchString(cmPAN) {
		errf(SegmonIssue{Column: sgCMPAN, Field: s.Columns[sgCMPAN-1].Name, Message: "the clearing member's PAN is not a PAN: correct it in Settings"})
	}
	all, uccs, err := o.segmonData(ctx, tenant, date)
	if err != nil {
		return nil, nil, chk, err
	}
	rows := segmonRows(all, uccs)
	d, _ := time.Parse("2006-01-02", date)
	var out []xfile.Row
	seen := map[string]bool{}
	tot := &chk.Totals
	for _, r := range rows {
		c := all[r.ucc]
		is := func(col int, msg string, a ...any) SegmonIssue {
			x := SegmonIssue{UCC: r.ucc, Segment: r.seg, Indicator: r.ind, Message: fmt.Sprintf(msg, a...)}
			if col > 0 {
				x.Column, x.Field = col, s.Columns[col-1].Name
			}
			return x
		}
		k := r.ucc + "|" + r.seg + "|" + r.ind
		if seen[k] {
			errf(is(0, "the client is in the file twice for the same segment and indicator"))
		}
		seen[k] = true
		if !reSegPAN.MatchString(c.pan) {
			if c.pan == "" {
				errf(is(sgClientPAN, "the client has no PAN: add it on the client's record"))
			} else {
				errf(is(sgClientPAN, "the client's PAN %s is not a valid PAN: correct it on the client's record", MaskPAN(c.pan)))
			}
		}
		if len(r.ucc) == 0 || len(r.ucc) > 10 {
			errf(is(sgUCC, "the client code must be 1 to 10 characters"))
		}
		for _, ch := range segmonChains {
			v := func(n int) money.Dec { return r.v[n] }
			for _, n := range []int{ch.recv, ch.retTM, ch.plCM, ch.retCM, ch.ncl} {
				if n > 0 && v(n).Sign() < 0 {
					errf(is(n, "%s cannot be negative (%s)", ch.what, v(n).Fixed(2)))
				}
			}
		}
		if r.v[sgPeak].Cmp(r.v[sgB]) < 0 {
			errf(is(sgPeak, "the peak clear balance %s is below the end-of-day clear balance %s", r.v[sgPeak].Fixed(2), r.v[sgB].Fixed(2)))
		}
		if r.v[sgB].Cmp(r.v[sgA]) > 0 && r.ind != "MTF" {
			errf(is(sgB, "the clear balance %s is above the ledger balance %s", r.v[sgB].Fixed(2), r.v[sgA].Fixed(2)))
		}
		for _, n := range []int{sgUncleared, sgEPI, sgSettle, sgMTFCash, sgRetTMCash, sgNCLCash, sgPlCMCash} {
			if r.v[n].Sign() < 0 {
				errf(is(n, "cannot be negative (%s)", r.v[n].Fixed(2)))
			}
		}
		m := map[string]string{}
		for i, col := range s.Columns {
			if col.Type == "dec" {
				m[col.Key] = r.v[i+1].Fixed(2)
			}
		}
		m[key(sgDate)] = d.Format("02-01-2006")
		m[key(sgCMPAN)], m[key(sgTMPAN)], m[key(sgClientPAN)] = cmPAN, pan, c.pan
		m[key(sgCPCode)], m[key(sgCPPAN)] = "", ""
		m[key(sgAccType)], m[key(sgSegment)], m[key(sgUCC)] = "C", r.seg, r.ucc
		m[key(sgMTF)], m[key(sgUnclaimed)] = r.ind, "NA"
		out = append(out, xfile.Row{Values: m})
		tot.Ledger = tot.Ledger.Add(r.v[sgA])
		tot.CashNCL = tot.CashNCL.Add(r.v[sgNCLCash])
		tot.NonCashNCL = tot.NonCashNCL.Add(r.v[50]).Add(r.v[sgGSecNCL])
		tot.Uncleared = tot.Uncleared.Add(r.v[sgUncleared])
		tot.EPI = tot.EPI.Add(r.v[sgEPI])
		tot.Settlement = tot.Settlement.Add(r.v[sgSettle])
		tot.MTFCash = tot.MTFCash.Add(r.v[sgMTFCash])
	}
	// Per client: received = retained + placed; placed with the clearing
	// member = retained by it + placed with NSE Clearing (SEBI 2021/0598
	// para 11), summed over the client's lines.
	byClient := map[string]map[int]money.Dec{}
	for _, r := range rows {
		if byClient[r.ucc] == nil {
			byClient[r.ucc] = map[int]money.Dec{}
		}
		for n, x := range r.v {
			byClient[r.ucc][n] = byClient[r.ucc][n].Add(x)
		}
	}
	for _, u := range uccs {
		v := byClient[u]
		for _, ch := range segmonChains {
			if got := v[ch.retTM].Add(v[ch.plCM]); got.Cmp(v[ch.recv]) != 0 {
				errf(SegmonIssue{UCC: u, Column: ch.recv, Field: s.Columns[ch.recv-1].Name, Message: fmt.Sprintf("%s received (%s) is not what was retained (%s) plus placed with the clearing member (%s)", ch.what, v[ch.recv].Fixed(2), v[ch.retTM].Fixed(2), v[ch.plCM].Fixed(2))})
			}
			if ch.ncl > 0 {
				if got := v[ch.retCM].Add(v[ch.ncl]); got.Cmp(v[ch.plCM]) != 0 {
					errf(SegmonIssue{UCC: u, Column: ch.plCM, Field: s.Columns[ch.plCM-1].Name, Message: fmt.Sprintf("%s placed with the clearing member (%s) is not what it retained (%s) plus placed with NSE Clearing (%s)", ch.what, v[ch.plCM].Fixed(2), v[ch.retCM].Fixed(2), v[ch.ncl].Fixed(2))})
				}
			}
		}
		if got := v[sgRetCMCash].Add(v[sgNCLCash]); got.Cmp(v[sgPlCMCash]) != 0 {
			errf(SegmonIssue{UCC: u, Column: sgPlCMCash, Field: s.Columns[sgPlCMCash-1].Name, Message: "cash placed with the clearing member is not what it retained plus placed with NSE Clearing"})
		}
		c := all[u]
		if avail := money.Max(c.bal.B.Add(c.mtfCash), money.Dec{}).Add(c.epi); v[sgNCLCash].Cmp(avail) > 0 {
			warn(SegmonIssue{UCC: u, Column: sgNCLCash, Field: s.Columns[sgNCLCash-1].Name, Message: fmt.Sprintf("cash allocated to NSE Clearing (%s) is more than the client's clear credit and early pay-in credit (%s); the allocation may not exceed what the client gave", v[sgNCLCash].Fixed(2), avail.Fixed(2))})
		}
		if c.bal.A.Sub(c.bal.B).Sign() > 0 && v[sgUncleared].Sign() == 0 {
			errf(SegmonIssue{UCC: u, Column: sgUncleared, Message: "uncleared receipts are not reported"})
		}
	}
	// Firm totals against the books and the day's allocation.
	if bals, err := o.segmonBalances(ctx, tenant, date); err == nil {
		for acct, b := range bals {
			u, ok := strings.CutPrefix(acct, "CLIENT:")
			if !ok {
				u, ok = strings.CutPrefix(acct, "RECEIVABLE:MTF:")
			}
			if !ok {
				continue
			}
			tot.Books = tot.Books.Add(b.A)
			if b.A.Sign() != 0 && all[u] != nil && !contains(uccs, u) {
				errf(SegmonIssue{UCC: u, Message: "the client has a ledger balance but is not in the file"})
			}
		}
	}
	if tot.Ledger.Cmp(tot.Books) != 0 {
		errf(SegmonIssue{Column: sgA, Field: s.Columns[sgA-1].Name, Message: fmt.Sprintf("the ledger balances in the file add up to %s, the client and MTF ledgers to %s", tot.Ledger.Fixed(2), tot.Books.Fixed(2))})
	}
	if al, err := o.Allocation(ctx, tenant, date); err == nil {
		for _, a := range al {
			if a.Segment != "COM" {
				tot.Allocated, tot.AllocNonCash = tot.Allocated.Add(paise(a.Cash)), tot.AllocNonCash.Add(paise(a.NonCash))
			}
		}
	}
	if tot.CashNCL.Cmp(tot.Allocated) != 0 {
		errf(SegmonIssue{Column: sgNCLCash, Field: s.Columns[sgNCLCash-1].Name, Message: fmt.Sprintf("cash placed with NSE Clearing adds up to %s; the day's allocation is %s", tot.CashNCL.Fixed(2), tot.Allocated.Fixed(2))})
	}
	if tot.NonCashNCL.Cmp(tot.AllocNonCash) != 0 {
		errf(SegmonIssue{Column: 50, Field: s.Columns[49].Name, Message: fmt.Sprintf("securities placed with NSE Clearing add up to %s; the day's allocation is %s", tot.NonCashNCL.Fixed(2), tot.AllocNonCash.Fixed(2))})
	}
	if len(rows) == 0 {
		warn(SegmonIssue{Message: "no client has funds or collateral on this date: the file has only the header"})
	}
	// A separate commodity ledger (segledger.go): SEGMON reports the
	// equity ledger, CLIENT:<ucc>; the commodity ledger's funds are with
	// the commodity clearing member and reported to MCX. Money still
	// awaiting allocation at the end of the day is in neither report.
	if o.sepCom(ctx, tenant) {
		if lf, err := o.clientLedgerFunds(ctx, tenant, date, ""); err == nil {
			for _, u := range sortedKeys(lf) {
				if a := lf[u].Awaiting; a != 0 {
					warn(SegmonIssue{UCC: u, Message: fmt.Sprintf("₹%s received is still awaiting allocation to the equity or commodity ledger and is not in this report; allocate it on RMS and make the file again", formatINR(a.Dec()))})
				}
			}
		}
	}
	chk.Rows, chk.Clients, chk.OK = len(out), len(uccs), len(chk.Errors) == 0
	name, err := s.FileName(xfile.Vars{"date": date, "seq": fmt.Sprint(seq), "member_pan": pan})
	if err == nil {
		chk.File = name
	}
	return out, s, chk, nil
}

// segmonFile is the daily client funds and collateral report. It is not
// made while the check finds errors.
func (o *Ops) segmonFile(ctx context.Context, tenant string, set Settings, date string, vars xfile.Vars) (string, []byte, error) {
	seq := 1
	fmt.Sscan(vars["seq"], &seq)
	rows, s, chk, err := o.segmonBuild(ctx, tenant, date, seq)
	if err != nil {
		return "", nil, err
	}
	if !chk.OK {
		var first []string
		for i, e := range chk.Errors {
			if i == 3 {
				break
			}
			first = append(first, strings.TrimSpace(strings.Join([]string{e.UCC, e.Segment}, " ")+" "+e.Message))
		}
		return "", nil, invalid("the SEGMON file is not made: the check found %d problem(s), first: %s. Run the SEGMON check for the full list", len(chk.Errors), strings.Join(first, "; "))
	}
	b, err := s.Write(rows)
	if err != nil {
		return "", nil, err
	}
	vars["member_pan"] = firmPAN(set)
	name, err := s.FileName(vars)
	if err != nil {
		return "", nil, invalid("%v", err)
	}
	return name, b, nil
}

func paise(d money.Dec) money.Dec { return d.Round(money.UnitPaisa).Dec() }

func segRank(s string) int {
	return map[string]int{"CM": 0, "FO": 1, "CD": 2}[s]
}
