package sharepro

import (
	"context"
	"fmt"
	"strings"

	"ledgerline/internal/migrate"
	"ledgerline/internal/migrate/pkg"
	"ledgerline/internal/money"
)

// Opening balances. Verified against a real database: ACCTOT holds daily
// movement totals only; the year's opening balance of a sub-account is
// MLFILE.AMTDROB - AMTCROB and of a head without sub-accounts MSFILE's
// (a head with sub-accounts carries the sum of theirs: never both). Both
// have one row per branch. Balance at the date = opening + ACCTOT movements
// to the date. SharePro's openings do not always net to zero (a year not
// fully closed): the difference goes to SUSPENSE:OPENING-DIFFERENCE, with
// a warning, never hidden.

const (
	// opening and movements, one row per (co, accd, subcode) part
	qBalACCTOT = `SELECT RTRIM(co) AS co, RTRIM(accd) AS accd, RTRIM(ISNULL(subcode, '')) AS subcode, amtdr - amtcr AS b FROM ACCTOT WHERE co IN (%s) AND vrdt <= '%s'`
	qBalMLFILE = `SELECT RTRIM(co) AS co, RTRIM(ACCD) AS accd, RTRIM(ISNULL(SUBCODE, '')) AS subcode, AMTDROB - AMTCROB AS b FROM MLFILE WHERE co IN (%s)`
	qBalMSFILE = `SELECT RTRIM(m.co) AS co, RTRIM(m.ACCD) AS accd, '' AS subcode, m.AMTDROB - m.AMTCROB AS b FROM MSFILE m WHERE m.co IN (%s)%s`
	qNotInSubs = ` AND NOT EXISTS (SELECT 1 FROM MLFILE l WHERE l.co = m.co AND l.ACCD = m.ACCD)`
	qTB        = `/*sp:trial-balance*/ SELECT t.co, t.accd, t.subcode, SUM(t.b) AS b FROM (%s) t GROUP BY t.co, t.accd, t.subcode`
	qClientNet = `/*sp:client-net*/ SELECT RTRIM(x.CO) AS co, RTRIM(x.ACCD) AS accd, RTRIM(x.SUBCODE) AS subcode, SUM(x.AMTDR) AS dr, SUM(x.AMTCR) AS cr FROM (%s) x GROUP BY x.CO, x.ACCD, x.SUBCODE`
	// by date: a client's ledger spans several sub-codes (its DP accounts')
	qClientDays = `/*sp:client-days*/ SELECT RTRIM(x.CO) AS co, RTRIM(x.ACCD) AS accd, RTRIM(x.SUBCODE) AS subcode, x.VRDT AS vrdt, SUM(x.AMTDR) AS dr, SUM(x.AMTCR) AS cr FROM (%s) x GROUP BY x.SUBCODE, x.CO, x.ACCD, x.VRDT ORDER BY x.VRDT, x.SUBCODE, x.CO, x.ACCD`
	// the year's client ledger lines (FIFO for the date each debit became due)
	qClientMoves = `SELECT CO, ACCD, SUBCODE, VRDT, AMTDR, AMTCR FROM JVFILE WHERE CO IN (%[1]s) AND ACCD IN (%[2]s) AND VRDT <= '%[3]s' AND VRDT >= '%[4]s'` +
		` UNION ALL SELECT co AS CO, ACCD, SUBCODE, VRDT, AMTDR, AMTCR FROM BCFILE WHERE co IN (%[1]s) AND ACCD IN (%[2]s) AND VRDT <= '%[3]s' AND VRDT >= '%[4]s'`
	// checks: the same trial balance added up on the server, client
	// ledgers merged as Ledgerline holds them
	qClientSet = `SELECT DISTINCT RTRIM(SUBCD) AS u FROM DLMAST`
	// %[4]s joins the DP client codes' clients (o.ben, o.u), see dpOwnerSQL
	qCheckTB        = `/*sp:check-tb*/ SELECT SUM(CASE WHEN b > 0 THEN b ELSE 0 END) AS dr, SUM(CASE WHEN b < 0 THEN -b ELSE 0 END) AS cr FROM (SELECT %[1]s AS k, SUM(t.b) AS b FROM (%[2]s) t LEFT JOIN (%[3]s) d ON d.u = t.subcode AND t.subcode <> ''%[4]s GROUP BY %[1]s) x`
	qCheckClientBal = `/*sp:check-client-balances*/ SELECT SUM(CASE WHEN b > 0 THEN b ELSE 0 END) AS dr, SUM(CASE WHEN b < 0 THEN -b ELSE 0 END) AS cr FROM (SELECT y.k, SUM(y.b) AS b FROM (SELECT 'C|' + d.u AS k, t.b FROM (%[1]s) t JOIN (%[2]s) d ON d.u = t.subcode WHERE (t.co + '|' + t.accd) IN (%[3]s)%[4]s) y GROUP BY y.k) x`
	qCheckClientDP  = ` UNION ALL SELECT o.u AS k, t.b FROM (%[1]s) t JOIN (%[2]s) o ON o.ben = t.subcode WHERE (t.co + '|' + t.accd) IN (%[3]s)`
)

// DiffAccount takes the difference when SharePro's openings do not balance.
const DiffAccount = "SUSPENSE:OPENING-DIFFERENCE"

type acctBal struct {
	code, name, ucc string
	bal             money.Dec
}

// balSource is the SQL of the balance parts: openings and movements.
func (c *cur) balSource() string {
	cos := inList(c.f.COs)
	var parts []string
	if c.s.has("ACCTOT") {
		parts = append(parts, fmt.Sprintf(qBalACCTOT, cos, day(c.asOf)))
	}
	if c.s.hasCol("MLFILE", "AMTDROB") && c.s.hasCol("MLFILE", "AMTCROB") {
		parts = append(parts, fmt.Sprintf(qBalMLFILE, cos))
	}
	if c.s.hasCol("MSFILE", "AMTDROB") && c.s.hasCol("MSFILE", "AMTCROB") {
		not := ""
		if c.s.has("MLFILE") {
			not = qNotInSubs
		}
		parts = append(parts, fmt.Sprintf(qBalMSFILE, cos, not))
	}
	return strings.Join(parts, " UNION ALL ")
}

type headStat struct {
	clients int
	dr, cr  money.Dec
}

// add counts a sub-ledger's balance (st may be nil).
func (st *headStat) add(b money.Dec) *headStat {
	if st == nil {
		st = &headStat{}
	}
	st.clients++
	if b.Sign() > 0 {
		st.dr = st.dr.Add(b)
	} else {
		st.cr = st.cr.Add(b.Neg())
	}
	return st
}

func (c *cur) writeOpening(ctx context.Context) error {
	ch, err := loadChart(ctx, c.d, c.s, c.rep)
	if err != nil {
		return err
	}
	// the DP accounts' ledgers, by DP client code, go to their clients
	owners := map[string]string{}
	for _, a := range c.cm.dp {
		if contains(c.f.dpCOs(), a.co) {
			addOwner(owners, a.ben, a.client)
		}
	}
	ch.dpLedgers(owners)
	c.chart = ch
	area := "Opening balances"
	src := c.balSource()
	if !c.s.has("ACCTOT") || !c.s.hasCol("MLFILE", "AMTDROB") {
		c.rep.errorf(area, "", "no-balances", "Give a backup with the ACCTOT, MLFILE and MSFILE tables, or load opening balances by hand",
			"This company database lacks ACCTOT or the opening balances of MLFILE; OPENING-BALANCES is not written")
		return nil
	}
	asOf := day(c.asOf)
	bals := map[string]*acctBal{}
	byCO := map[string]money.Dec{}
	stats := map[string]*headStat{} // client-coded head -> clients' balances in it
	unm := map[string]*headStat{}   // DP client head -> balances of sub-ledgers with no client
	err = c.d.query(ctx, fmt.Sprintf(qTB, src), func(r migrate.Row) error {
		co, accd, sub := str(r, "co"), upper(r, "accd"), upper(r, "subcode")
		code, name, ucc := ch.account(co, accd, sub)
		b := dec(r, "b")
		if u := ch.clientOf(co, accd, sub); u != "" && !b.IsZero() {
			stats[co+"|"+accd] = stats[co+"|"+accd].add(b)
		} else if ch.dpCoded[co+"|"+accd] && sub != "" && !b.IsZero() {
			unm[co+"|"+accd] = unm[co+"|"+accd].add(b)
		}
		a := bals[code]
		if a == nil {
			a = &acctBal{code: code, name: name, ucc: ucc}
			bals[code] = a
		}
		a.bal = a.bal.Add(b)
		byCO[co] = byCO[co].Add(b)
		return nil
	})
	if err != nil {
		return err
	}
	ch.reportUnknown(c.rep, area)
	c.reportClientHeads(ch, stats)
	c.reportDPHeads(ch, stats, unm)
	since, err := c.dueDates(ctx, ch, bals)
	if err != nil {
		return err
	}
	h := headers[srcOpening]
	cw, err := c.w.Master(c.f.Code, srcOpening, h)
	if err != nil {
		return err
	}
	var td, tc, clientDr, clientCr money.Amount
	subPaisa := 0
	for _, code := range sortedKeys(bals) {
		a := bals[code]
		amt := a.bal.Round(money.UnitPaisa)
		if amt.Dec().Cmp(a.bal) != 0 {
			subPaisa++
		}
		if amt == 0 {
			continue
		}
		v := map[string]string{"Account": code, "Name": a.name}
		if amt > 0 {
			v["Debit"] = amt.String()
			td += amt
			if a.ucc != "" {
				clientDr += amt
				v["Since"] = since[a.ucc]
			}
		} else {
			v["Credit"] = (-amt).String()
			tc += -amt
			if a.ucc != "" {
				clientCr += -amt
			}
		}
		if a.ucc != "" && c.skipped[a.ucc] {
			c.rep.errorf(area, a.ucc, "skipped-balance", "Migrate dormant clients as inactive (dormant mode: inactive), or settle the balance in SharePro first",
				"Dormant client left out of the client master has a balance of %s; it is kept in the opening balances under %s", amt.String(), code)
		}
		if a.ucc != "" && !c.written[a.ucc] && !c.skipped[a.ucc] {
			c.rep.warnf(area, a.ucc, "no-client", "Check the client code in SharePro", "Client ledger %s has a balance of %s but the client is not in this firm's client master", code, amt.String())
		}
		if err := cw.Write(rec(h, v)); err != nil {
			cw.Close()
			return err
		}
	}
	// SharePro's openings that do not balance: one line, never hidden
	if diff := td - tc; diff != 0 {
		v := map[string]string{"Account": DiffAccount, "Name": "Difference in opening balances carried from SharePro"}
		if diff > 0 {
			v["Credit"] = diff.String()
		} else {
			v["Debit"] = (-diff).String()
		}
		if err := cw.Write(rec(h, v)); err != nil {
			cw.Close()
			return err
		}
		var split []string
		for _, co := range sortedKeys(byCO) {
			if n := byCO[co]; !n.IsZero() {
				split = append(split, fmt.Sprintf("%s %s", co, n.Fixed(2)))
			}
		}
		c.rep.warnf(area, DiffAccount, "difference", "Your accountant must confirm this difference before go-live (often last year's profit or a year-end entry not yet posted in SharePro)",
			"SharePro's balances as of %s do not balance: debits %s, credits %s; the difference of %s is put to %s. By company (debit minus credit): %s",
			asOf, td.String(), tc.String(), money.Amount(abs64(int64(diff))).String(), DiffAccount, strings.Join(split, ", "))
	}
	if err := cw.Close(); err != nil {
		return err
	}
	if subPaisa > 0 {
		c.rep.warnf(area, "", "sub-paisa", "Check the rounded balances", "%d account balances had fractions of a paisa and were rounded to the paisa", subPaisa)
	}
	c.rep.infof(area, "", "totals", "Balances as of %s: debits %s, credits %s; client debits %s, client credits %s", asOf, td.String(), tc.String(), clientDr.String(), clientCr.String())
	return c.tbChecks(ctx, ch, src)
}

func abs64(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

// reportClientHeads lists every client-coded head of the firm, merged into
// the client's ledger or not.
func (c *cur) reportClientHeads(ch *chart, stats map[string]*headStat) {
	area := "Client ledgers"
	for _, merged := range []bool{true, false} {
		for _, k := range ch.headsOf(c.f.COs, merged) {
			st := stats[k]
			if st == nil {
				st = &headStat{}
			}
			co, accd, _ := strings.Cut(k, "|")
			if merged {
				c.rep.infof(area, k, "merged-head", "Head %s of company %s (%s) is a client ledger: %d clients with a balance, debits %s, credits %s; merged into CLIENT:<client code>",
					accd, co, ch.headName(k), st.clients, st.dr.Fixed(2), st.cr.Fixed(2))
				continue
			}
			code, _, _ := ch.account(co, accd, "")
			c.rep.warnf(area, k, "other-head", fmt.Sprintf("Decide with your accountant whether each client's amount in %s belongs in the client's ledger; if so, post a journal after go-live", ch.headName(k)),
				"Head %s of company %s (%s) holds amounts per client: %d clients with a balance, debits %s, credits %s; not merged into the client ledgers, migrated as one account %s",
				accd, co, ch.headName(k), st.clients, st.dr.Fixed(2), st.cr.Fixed(2), code)
		}
	}
}

// reportDPHeads lists every DP client head of the firm (sub-ledgers by DP
// client code): how many sub-ledgers went to their client's ledger and how
// many did not (a warning, with their totals).
func (c *cur) reportDPHeads(ch *chart, stats, unm map[string]*headStat) {
	area := "Client ledgers"
	z := func(st *headStat) *headStat {
		if st == nil {
			return &headStat{}
		}
		return st
	}
	for _, merged := range []bool{true, false} {
		for _, k := range ch.dpHeadsOf(c.f.COs, merged) {
			co, accd, _ := strings.Cut(k, "|")
			n, st, un := ch.dpSubs[k], z(stats[k]), z(unm[k])
			head, _, _ := ch.account(co, accd, "")
			where := "their client's ledger, CLIENT:<client code> (the trading client, or the DP-only client; several accounts of a client add up)"
			if !merged {
				where = "one account " + head + " (the head is not a client ledger by its name)"
			}
			msg := fmt.Sprintf("Head %s of company %s (%s) holds the DP accounts' ledgers by DP client code (CLLIST.BENACNUM): %d sub-ledgers are accounts of the firm's DP and go to %s; %d of them with a balance, debits %s, credits %s.",
				accd, co, ch.headName(k), n[0], where, st.clients, st.dr.Fixed(2), st.cr.Fixed(2))
			if n[1] > 0 {
				msg += fmt.Sprintf(" %d sub-ledgers are not (the code is not in the DP master, or belongs to two clients): %d with a balance, debits %s, credits %s, migrated as sub-accounts %s-<code>",
					n[1], un.clients, un.dr.Fixed(2), un.cr.Fixed(2), head)
			} else {
				msg += " Every sub-ledger is an account of the firm's DP"
			}
			switch {
			case n[1] > 0:
				c.rep.warnf(area, k, "dp-head-unmapped", "Find the clients of these DP client codes in SharePro and move their balances by journal after go-live", "%s", msg)
			case !merged:
				c.rep.warnf(area, k, "dp-head-other", fmt.Sprintf("Decide with your accountant whether each client's amount in %s belongs in the client's ledger; if so, post a journal after go-live", ch.headName(k)), "%s", msg)
			default:
				c.rep.infof(area, k, "dp-head", "%s", msg)
			}
		}
	}
}

// dueDates works out, for each client in debit, the date the debit
// became due: the year's movements are taken first in, first out (each
// credit pays the oldest debit), the year's opening balance being the
// oldest. Earlier years' bill dates are not used: LY_ACCTOT is incomplete.
func (c *cur) dueDates(ctx context.Context, ch *chart, bals map[string]*acctBal) (map[string]string, error) {
	out := map[string]string{}
	// the client ledger heads, the DP client heads among them
	heads := accdsOf(append(ch.headsOf(c.f.COs, true), ch.dpHeadsOf(c.f.COs, true)...))
	if len(heads) == 0 || !c.s.has("JVFILE") || !c.s.has("BCFILE") {
		c.rep.warnf("Opening balances", "", "no-due-dates", "Ageing and interest start from the go-live date", "No client ledger heads were found in this database; the Since dates are not given")
		return out, nil
	}
	start := fyStart(c.k.FY)
	if start.IsZero() || start.After(c.asOf) {
		start = c.asOf.AddDate(-1, 0, 0)
	}
	moves := fmt.Sprintf(qClientMoves, inList(c.f.COs), inList(heads), day(c.asOf), day(start))
	net := map[string]money.Dec{}
	if err := c.d.query(ctx, fmt.Sprintf(qClientNet, moves), func(r migrate.Row) error {
		if _, _, u := ch.account(str(r, "co"), str(r, "accd"), str(r, "subcode")); u != "" {
			net[u] = net[u].Add(dec(r, "dr").Sub(dec(r, "cr")))
		}
		return nil
	}); err != nil {
		return nil, err
	}
	books := map[string]*fifo{}
	bal := func(u string) money.Dec {
		if a := bals["CLIENT:"+codePart(u)]; a != nil {
			return a.bal
		}
		return money.Dec{}
	}
	get := func(u string) *fifo {
		f := books[u]
		if f == nil {
			f = &fifo{}
			f.apply(day(start), bal(u).Sub(net[u])) // the year's opening balance
			books[u] = f
		}
		return f
	}
	if err := c.d.query(ctx, fmt.Sprintf(qClientDays, moves), func(r migrate.Row) error {
		if _, _, u := ch.account(str(r, "co"), str(r, "accd"), str(r, "subcode")); u != "" {
			get(u).apply(date(r, "vrdt"), dec(r, "dr").Sub(dec(r, "cr")))
		}
		return nil
	}); err != nil {
		return nil, err
	}
	old := 0
	for _, code := range sortedKeys(bals) {
		a := bals[code]
		if a.ucc == "" || a.bal.Sign() <= 0 {
			continue
		}
		if d := get(a.ucc).oldest(); d != "" {
			out[a.ucc] = d
			if d == day(start) {
				old++
			}
		}
	}
	if old > 0 {
		c.rep.infof("Opening balances", "", "old-debits", "%d client debits were already due before the year began (%s); their Since date is the year's first day (SharePro's earlier-year totals, LY_ACCTOT, are incomplete)", old, day(start))
	}
	return out, nil
}

// fifo holds a client's unpaid debits, oldest first.
type fifo struct {
	lots   []lot
	credit money.Dec // credit not yet used against a debit
}

type lot struct {
	on  string
	amt money.Dec
}

func (f *fifo) apply(on string, net money.Dec) {
	switch net.Sign() {
	case 1:
		if f.credit.Sign() > 0 {
			x := money.Min(f.credit, net)
			f.credit, net = f.credit.Sub(x), net.Sub(x)
		}
		if net.Sign() > 0 {
			f.lots = append(f.lots, lot{on, net})
		}
	case -1:
		cr := net.Neg()
		for cr.Sign() > 0 && len(f.lots) > 0 {
			if f.lots[0].amt.Cmp(cr) <= 0 {
				cr = cr.Sub(f.lots[0].amt)
				f.lots = f.lots[1:]
			} else {
				f.lots[0].amt = f.lots[0].amt.Sub(cr)
				cr = money.Dec{}
			}
		}
		f.credit = f.credit.Add(cr)
	}
}

func (f *fifo) oldest() string {
	if len(f.lots) == 0 {
		return ""
	}
	return f.lots[0].on
}

func pairList(keys []string) string { return inList(keys) }

func (c *cur) tbChecks(ctx context.Context, ch *chart, src string) error {
	merged, other := ch.headsOf(c.f.COs, true), ch.headsOf(c.f.COs, false)
	dpMerged, dpOther := ch.dpHeadsOf(c.f.COs, true), ch.dpHeadsOf(c.f.COs, false)
	// the DP client heads: a DP account's sub-ledger is its client's (o.u:
	// 'C|' + the trading code, the key of the client's own ledger, or 'D|' +
	// the DP client code for a DP-only client)
	dpKey, dpJoin, dpClient := "", "", ""
	if len(dpMerged)+len(dpOther) > 0 {
		owners := c.dpOwnerSQL()
		dpKey = fmt.Sprintf(" WHEN o.u IS NOT NULL AND (t.co + '|' + t.accd) IN (%s) THEN o.u WHEN o.u IS NOT NULL AND (t.co + '|' + t.accd) IN (%s) THEN t.co + '|' + t.accd",
			pairList(dpMerged), pairList(dpOther))
		dpJoin = " LEFT JOIN (" + owners + ") o ON o.ben = t.subcode AND t.subcode <> ''"
		if len(dpMerged) > 0 {
			dpClient = fmt.Sprintf(qCheckClientDP, src, owners, pairList(dpMerged))
		}
	}
	key := fmt.Sprintf("CASE WHEN d.u IS NOT NULL AND (t.co + '|' + t.accd) IN (%s) THEN 'C|' + d.u WHEN d.u IS NOT NULL AND (t.co + '|' + t.accd) IN (%s) THEN t.co + '|' + t.accd%s ELSE t.co + '|' + t.accd + '|' + t.subcode END",
		pairList(merged), pairList(other), dpKey)
	r, err := c.d.one(ctx, fmt.Sprintf(qCheckTB, key, src, qClientSet, dpJoin))
	if err != nil {
		return err
	}
	dr, cr := dec(r, "dr"), dec(r, "cr")
	diff := cr.Sub(dr) // the suspense line's balance, debit positive
	detail := "SharePro balances (MLFILE/MSFILE openings + ACCTOT) as of " + day(c.asOf) + ", client ledgers merged over the firm's companies " + strings.Join(c.f.COs, ", ")
	if diff.Sign() > 0 {
		dr = dr.Add(diff)
	} else {
		cr = cr.Add(diff.Neg())
	}
	c.rep.check(pkg.CheckTBDebit, rupees0(dr), detail+"; including "+DiffAccount)
	c.rep.check(pkg.CheckTBCredit, rupees0(cr), detail+"; including "+DiffAccount)
	c.rep.check(pkg.CheckOpeningDifference, rupees0(diff), "Balance of "+DiffAccount+" (debit positive): SharePro's debits minus credits do not net to zero by this amount")
	r, err = c.d.one(ctx, fmt.Sprintf(qCheckClientBal, src, qClientSet, pairList(merged), dpClient))
	if err != nil {
		return err
	}
	c.rep.check(pkg.CheckClientDebit, rupees0(dec(r, "dr")), "Client ledgers in debit, as of "+day(c.asOf)+" (client-coded heads named CLIENT, and the DP client heads by DP client code, each DP account's to its client)")
	c.rep.check(pkg.CheckClientCredit, rupees0(dec(r, "cr")), "Client ledgers in credit, as of "+day(c.asOf)+" (client-coded heads named CLIENT, and the DP client heads by DP client code, each DP account's to its client)")
	return nil
}
