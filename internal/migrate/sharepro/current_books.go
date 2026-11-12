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
	qBalACCTOT  = `SELECT RTRIM(co) AS co, RTRIM(accd) AS accd, RTRIM(ISNULL(subcode, '')) AS subcode, amtdr - amtcr AS b FROM ACCTOT WHERE co IN (%s) AND vrdt <= '%s'`
	qBalMLFILE  = `SELECT RTRIM(co) AS co, RTRIM(ACCD) AS accd, RTRIM(ISNULL(SUBCODE, '')) AS subcode, AMTDROB - AMTCROB AS b FROM MLFILE WHERE co IN (%s)`
	qBalMSFILE  = `SELECT RTRIM(m.co) AS co, RTRIM(m.ACCD) AS accd, '' AS subcode, m.AMTDROB - m.AMTCROB AS b FROM MSFILE m WHERE m.co IN (%s)%s`
	qNotInSubs  = ` AND NOT EXISTS (SELECT 1 FROM MLFILE l WHERE l.co = m.co AND l.ACCD = m.ACCD)`
	qTB         = `/*sp:trial-balance*/ SELECT t.co, t.accd, t.subcode, SUM(t.b) AS b FROM (%s) t GROUP BY t.co, t.accd, t.subcode`
	qClientNet  = `/*sp:client-net*/ SELECT RTRIM(x.CO) AS co, RTRIM(x.ACCD) AS accd, RTRIM(x.SUBCODE) AS subcode, SUM(x.AMTDR) AS dr, SUM(x.AMTCR) AS cr FROM (%s) x GROUP BY x.CO, x.ACCD, x.SUBCODE`
	qClientDays = `/*sp:client-days*/ SELECT RTRIM(x.CO) AS co, RTRIM(x.ACCD) AS accd, RTRIM(x.SUBCODE) AS subcode, x.VRDT AS vrdt, SUM(x.AMTDR) AS dr, SUM(x.AMTCR) AS cr FROM (%s) x GROUP BY x.SUBCODE, x.CO, x.ACCD, x.VRDT ORDER BY x.SUBCODE, x.VRDT`
	// the year's client ledger lines (FIFO for the date each debit became due)
	qClientMoves = `SELECT CO, ACCD, SUBCODE, VRDT, AMTDR, AMTCR FROM JVFILE WHERE CO IN (%[1]s) AND ACCD IN (%[2]s) AND VRDT <= '%[3]s' AND VRDT >= '%[4]s'` +
		` UNION ALL SELECT co AS CO, ACCD, SUBCODE, VRDT, AMTDR, AMTCR FROM BCFILE WHERE co IN (%[1]s) AND ACCD IN (%[2]s) AND VRDT <= '%[3]s' AND VRDT >= '%[4]s'`
	// checks: the same trial balance added up on the server, client
	// ledgers merged as Ledgerline holds them
	qClientSet      = `SELECT DISTINCT RTRIM(SUBCD) AS u FROM DLMAST`
	qCheckTB        = `/*sp:check-tb*/ SELECT SUM(CASE WHEN b > 0 THEN b ELSE 0 END) AS dr, SUM(CASE WHEN b < 0 THEN -b ELSE 0 END) AS cr FROM (SELECT %[1]s AS k, SUM(t.b) AS b FROM (%[2]s) t LEFT JOIN (%[3]s) d ON d.u = t.subcode AND t.subcode <> '' GROUP BY %[1]s) x`
	qCheckClientBal = `/*sp:check-client-balances*/ SELECT SUM(CASE WHEN b > 0 THEN b ELSE 0 END) AS dr, SUM(CASE WHEN b < 0 THEN -b ELSE 0 END) AS cr FROM (SELECT d.u AS k, SUM(t.b) AS b FROM (%[1]s) t JOIN (%[2]s) d ON d.u = t.subcode WHERE (t.co + '|' + t.accd) IN (%[3]s) GROUP BY d.u) x`
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

func (c *cur) writeOpening(ctx context.Context) error {
	ch, err := loadChart(ctx, c.d, c.s, c.rep)
	if err != nil {
		return err
	}
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
	err = c.d.query(ctx, fmt.Sprintf(qTB, src), func(r migrate.Row) error {
		co, accd, sub := str(r, "co"), upper(r, "accd"), upper(r, "subcode")
		code, name, ucc := ch.account(co, accd, sub)
		b := dec(r, "b")
		if u := ch.clientOf(co, accd, sub); u != "" && !b.IsZero() {
			st := stats[co+"|"+accd]
			if st == nil {
				st = &headStat{}
				stats[co+"|"+accd] = st
			}
			st.clients++
			if b.Sign() > 0 {
				st.dr = st.dr.Add(b)
			} else {
				st.cr = st.cr.Add(b.Neg())
			}
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

// dueDates works out, for each client in debit, the date the debit
// became due: the year's movements are taken first in, first out (each
// credit pays the oldest debit), the year's opening balance being the
// oldest. Earlier years' bill dates are not used: LY_ACCTOT is incomplete.
func (c *cur) dueDates(ctx context.Context, ch *chart, bals map[string]*acctBal) (map[string]string, error) {
	out := map[string]string{}
	heads := accdsOf(ch.headsOf(c.f.COs, true))
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
	key := fmt.Sprintf("CASE WHEN d.u IS NOT NULL AND (t.co + '|' + t.accd) IN (%s) THEN 'C|' + d.u WHEN d.u IS NOT NULL AND (t.co + '|' + t.accd) IN (%s) THEN t.co + '|' + t.accd ELSE t.co + '|' + t.accd + '|' + t.subcode END",
		pairList(merged), pairList(other))
	r, err := c.d.one(ctx, fmt.Sprintf(qCheckTB, key, src, qClientSet))
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
	r, err = c.d.one(ctx, fmt.Sprintf(qCheckClientBal, src, qClientSet, pairList(merged)))
	if err != nil {
		return err
	}
	c.rep.check(pkg.CheckClientDebit, rupees0(dec(r, "dr")), "Client ledgers in debit, as of "+day(c.asOf)+" (client-coded heads named CLIENT only)")
	c.rep.check(pkg.CheckClientCredit, rupees0(dec(r, "cr")), "Client ledgers in credit, as of "+day(c.asOf)+" (client-coded heads named CLIENT only)")
	return nil
}
