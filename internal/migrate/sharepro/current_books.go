package sharepro

import (
	"context"
	"fmt"
	"strings"

	"ledgerline/internal/migrate"
	"ledgerline/internal/migrate/pkg"
	"ledgerline/internal/money"
)

// Opening balances: SharePro's trial balance as of the date, from ACCTOT
// (the daily totals of every head and sub-account, opening included),
// mapped to Ledgerline accounts.

const (
	qTB = `/*sp:trial-balance*/ SELECT RTRIM(co) AS co, RTRIM(accd) AS accd, RTRIM(subcode) AS subcode, SUM(amtdr) AS dr, SUM(amtcr) AS cr FROM ACCTOT WHERE co IN (%s) AND vrdt <= '%s' GROUP BY co, accd, subcode`
	// net movement of each client ledger in the year to the date, then the
	// same day by day (FIFO for the date each debit became due)
	qClientNet   = `/*sp:client-net*/ SELECT RTRIM(x.ACCD) AS accd, RTRIM(x.SUBCODE) AS subcode, SUM(x.AMTDR) AS dr, SUM(x.AMTCR) AS cr FROM (%s) x GROUP BY x.ACCD, x.SUBCODE`
	qClientDays  = `/*sp:client-days*/ SELECT RTRIM(x.ACCD) AS accd, RTRIM(x.SUBCODE) AS subcode, x.VRDT AS vrdt, SUM(x.AMTDR) AS dr, SUM(x.AMTCR) AS cr FROM (%s) x GROUP BY x.SUBCODE, x.ACCD, x.VRDT ORDER BY x.SUBCODE, x.VRDT`
	qClientMoves = `SELECT ACCD, SUBCODE, VRDT, AMTDR, AMTCR FROM JVFILE WHERE CO IN (%[1]s) AND ACCD IN (%[2]s) AND VRDT <= '%[3]s' AND VRDT >= '%[4]s'` +
		` UNION ALL SELECT ACCD, SUBCODE, VRDT, AMTDR, AMTCR FROM BCFILE WHERE co IN (%[1]s) AND ACCD IN (%[2]s) AND VRDT <= '%[3]s' AND VRDT >= '%[4]s'`
	// checks: the same trial balance added up on the server, client
	// ledgers merged over the firm's COs as Ledgerline holds them
	qCheckTB        = `/*sp:check-tb*/ SELECT SUM(CASE WHEN b > 0 THEN b ELSE 0 END) AS dr, SUM(CASE WHEN b < 0 THEN -b ELSE 0 END) AS cr FROM (SELECT %[1]s AS k, SUM(t.amtdr - t.amtcr) AS b FROM ACCTOT t LEFT JOIN (%[2]s) d ON %[3]s WHERE t.co IN (%[4]s) AND t.vrdt <= '%[5]s' GROUP BY %[1]s) x`
	qCheckClientBal = `/*sp:check-client-balances*/ SELECT SUM(CASE WHEN b > 0 THEN b ELSE 0 END) AS dr, SUM(CASE WHEN b < 0 THEN -b ELSE 0 END) AS cr FROM (SELECT d.u AS k, SUM(t.amtdr - t.amtcr) AS b FROM ACCTOT t JOIN (%[1]s) d ON %[2]s WHERE t.co IN (%[3]s) AND t.vrdt <= '%[4]s' GROUP BY d.u) x`
)

type acctBal struct {
	code, name, ucc string
	bal             money.Dec
}

func (c *cur) writeOpening(ctx context.Context) error {
	ch, err := loadChart(ctx, c.d, c.s, c.rep)
	if err != nil {
		return err
	}
	c.chart = ch
	area := "Opening balances"
	if !c.s.has("ACCTOT") {
		c.rep.errorf(area, "", "no-acctot", "Give a backup with the ACCTOT table, or load opening balances by hand", "This company database has no ACCTOT (account totals) table; OPENING-BALANCES is not written")
		return nil
	}
	asOf := day(c.asOf)
	cos := inList(c.f.COs)
	bals := map[string]*acctBal{}
	subPaisa := 0
	err = c.d.query(ctx, fmt.Sprintf(qTB, cos, asOf), func(r migrate.Row) error {
		code, name, ucc := ch.account(str(r, "co"), str(r, "accd"), str(r, "subcode"))
		b := dec(r, "dr").Sub(dec(r, "cr"))
		a := bals[code]
		if a == nil {
			a = &acctBal{code: code, name: name, ucc: ucc}
			bals[code] = a
		}
		a.bal = a.bal.Add(b)
		return nil
	})
	if err != nil {
		return err
	}
	ch.reportUnknown(c.rep, area)
	since, err := c.dueDates(ctx, ch, bals)
	if err != nil {
		return err
	}
	h := headers[srcOpening]
	cw, err := c.w.Master(c.f.Code, srcOpening, h)
	if err != nil {
		return err
	}
	var td, tc money.Amount
	var clientDr, clientCr money.Amount
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
	if err := cw.Close(); err != nil {
		return err
	}
	if subPaisa > 0 {
		c.rep.warnf(area, "", "sub-paisa", "Check the rounded balances", "%d account balances had fractions of a paisa and were rounded to the paisa", subPaisa)
	}
	if td != tc {
		c.rep.errorf(area, "", "unbalanced", "Find the difference in SharePro's trial balance (inter-company accounts of COs not in this firm, or a damaged ACCTOT) before loading",
			"The opening balances do not net to zero: debits %s, credits %s, difference %s", td.String(), tc.String(), (td - tc).String())
	}
	c.rep.infof(area, "", "totals", "Opening balances as of %s: debits %s, credits %s; client debits %s, client credits %s", asOf, td.String(), tc.String(), clientDr.String(), clientCr.String())
	return c.tbChecks(ctx, ch)
}

// dueDates works out, for each client in debit, the date the debit
// became due: the year's movements are taken first in, first out (each
// credit pays the oldest debit), the year's opening balance being the
// oldest.
func (c *cur) dueDates(ctx context.Context, ch *chart, bals map[string]*acctBal) (map[string]string, error) {
	out := map[string]string{}
	heads := ch.clientHeadList()
	if len(heads) == 0 || !c.s.has("JVFILE") || !c.s.has("BCFILE") {
		c.rep.warnf("Opening balances", "", "no-due-dates", "Ageing and interest start from the go-live date", "Client ledgers cannot be told apart in this database; the Since dates are not given")
		return out, nil
	}
	start := fyStart(c.k.FY)
	if start.IsZero() || start.After(c.asOf) {
		start = c.asOf.AddDate(-1, 0, 0)
	}
	moves := fmt.Sprintf(qClientMoves, inList(c.f.COs), inList(heads), day(c.asOf), day(start))
	net := map[string]money.Dec{}
	if err := c.d.query(ctx, fmt.Sprintf(qClientNet, moves), func(r migrate.Row) error {
		if _, _, u := ch.account("", str(r, "accd"), str(r, "subcode")); u != "" {
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
		if _, _, u := ch.account("", str(r, "accd"), str(r, "subcode")); u != "" {
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
		f := get(a.ucc)
		if d := f.oldest(); d != "" {
			out[a.ucc] = d
			if d == day(start) {
				old++
			}
		}
	}
	if old > 0 {
		c.rep.infof("Opening balances", "", "old-debits", "%d client debits were already due at the start of the year (%s); their Since date is the year's first day (earlier years' databases would give the exact date)", old, day(start))
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

// clientJoin is the SQL that maps an ACCTOT row (alias t) to the client
// code (d.u), as chart.account does.
func (c *cur) clientJoin(heads []string) (sub, on string) {
	a, s := "RTRIM(ACC_ACCD)", "RTRIM(ISNULL(NULLIF(RTRIM(ACC_SUBCOD), ''), SUBCD))"
	if !c.s.hasCol("DLMAST", "ACC_ACCD") {
		a = "''"
	}
	if !c.s.hasCol("DLMAST", "ACC_SUBCOD") {
		s = "RTRIM(SUBCD)"
	}
	sub = fmt.Sprintf("SELECT DISTINCT %s AS a, %s AS s, RTRIM(SUBCD) AS u FROM DLMAST", a, s)
	on = fmt.Sprintf("d.s = RTRIM(t.subcode) AND RTRIM(t.subcode) <> '' AND (d.a = RTRIM(t.accd) OR (d.a = '' AND RTRIM(t.accd) IN (%s)))", inList(heads))
	return sub, on
}

func (c *cur) tbChecks(ctx context.Context, ch *chart) error {
	sub, on := c.clientJoin(ch.clientHeadList())
	key := "CASE WHEN d.u IS NOT NULL THEN 'C|' + d.u ELSE RTRIM(t.co) + '|' + RTRIM(t.accd) + '|' + RTRIM(t.subcode) END"
	r, err := c.d.one(ctx, fmt.Sprintf(qCheckTB, key, sub, on, inList(c.f.COs), day(c.asOf)))
	if err != nil {
		return err
	}
	detail := "SharePro trial balance (ACCTOT) as of " + day(c.asOf) + ", client ledgers merged over the firm's companies " + strings.Join(c.f.COs, ", ")
	c.rep.check(pkg.CheckTBDebit, rupees0(dec(r, "dr")), detail)
	c.rep.check(pkg.CheckTBCredit, rupees0(dec(r, "cr")), detail)
	r, err = c.d.one(ctx, fmt.Sprintf(qCheckClientBal, sub, on, inList(c.f.COs), day(c.asOf)))
	if err != nil {
		return err
	}
	c.rep.check(pkg.CheckClientDebit, rupees0(dec(r, "dr")), "Client ledgers in debit, as of "+day(c.asOf))
	c.rep.check(pkg.CheckClientCredit, rupees0(dec(r, "cr")), "Client ledgers in credit, as of "+day(c.asOf))
	return nil
}
