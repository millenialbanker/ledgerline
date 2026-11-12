package sharepro

import (
	"context"
	"fmt"
	"strings"

	"ledgerline/internal/migrate"
	"ledgerline/internal/migrate/pkg"
	"ledgerline/internal/money"
)

const (
	qHoldDate = `/*sp:hold-date*/ SELECT MAX(HOLDDATE) AS d FROM HOLD WHERE CO IN (%s) AND HOLDDATE < '%s'`
	qHold     = `/*sp:holdings*/ SELECT RTRIM(h.CO) AS co, RTRIM(h.BENACNUM) AS ben, RTRIM(h.ISINCD) AS isin, %s FROM HOLD h WHERE h.CO IN (%s) AND h.HOLDDATE >= '%s' AND h.HOLDDATE < '%s' GROUP BY h.CO, h.BENACNUM, h.ISINCD ORDER BY h.CO, h.BENACNUM, h.ISINCD`
	qSymbols  = `/*sp:isin-symbols*/ SELECT RTRIM(x.ISINCD) AS isin, %s FROM ISINMAST x JOIN ITC i ON i.ITEMCD = x.ITEMCD`
	// checks
	qCheckHold = `/*sp:check-holdings*/ SELECT SUM(h.BALANCE) AS q FROM HOLD h JOIN CLLIST l ON l.CO = h.CO AND l.BENACNUM = h.BENACNUM WHERE h.CO IN (%s) AND h.HOLDDATE >= '%s' AND h.HOLDDATE < '%s' AND RTRIM(l.SUBCODE) IN (%s)`
	qCheckDP   = `/*sp:check-dp-accounts*/ SELECT COUNT(*) AS n FROM CLLIST WHERE CO IN (%s) AND RTRIM(SUBCODE) IN (%s) AND NOT (BENCLOSEDT > '1901-01-01')`
	// derivative trades of contracts not yet expired
	qPositions = `/*sp:positions*/ SELECT RTRIM(t.CO) AS co, RTRIM(t.SUBCD) AS subcd, RTRIM(t.ITEMCD) AS itemcd, t.TRDT AS trdt, t.QTY AS qty, CASE WHEN t.ACT_RATE > 0 THEN t.ACT_RATE ELSE t.RATE END AS price, %s FROM TRFILE t %s WHERE t.CO IN (%s) AND t.TRDT < '%s' AND i.exp_date >= '%s' ORDER BY t.SUBCD, t.CO, t.ITEMCD, %s`
	qCheckPos  = `/*sp:check-positions*/ SELECT COUNT(*) AS n FROM (SELECT t.SUBCD, t.CO, t.ITEMCD FROM TRFILE t %s WHERE t.CO IN (%s) AND t.TRDT < '%s' AND i.exp_date >= '%s' GROUP BY t.SUBCD, t.CO, t.ITEMCD HAVING SUM(t.QTY) <> 0) x`
)

func (c *cur) symbols(ctx context.Context) map[string]string {
	out := map[string]string{}
	if !c.s.has("ISINMAST") || !c.s.has("ITC") {
		return out
	}
	list, _ := c.s.sel("ITC", "i", "NSE_ITEMCD", "BSE_ITEMCD")
	_ = c.d.query(ctx, fmt.Sprintf(qSymbols, list), func(r migrate.Row) error {
		isin := upper(r, "isin")
		if out[isin] == "" {
			out[isin] = firstNonEmpty(upper(r, "nse_itemcd"), upper(r, "bse_itemcd"))
		}
		return nil
	})
	return out
}

type holding struct {
	ucc, isin  string
	qty, pledg money.Dec
}

func (c *cur) writeHoldings(ctx context.Context) error {
	area := "DP holdings"
	dpc := c.f.dpCOs()
	h := headers[srcHoldings]
	if len(dpc) == 0 || !c.s.has("HOLD") || !c.s.has("CLLIST") {
		cw, err := c.w.Master(c.f.Code, srcHoldings, h)
		if err != nil {
			return err
		}
		if len(dpc) > 0 {
			c.rep.infof(area, "", "no-hold", "This database has no HOLD table; DP holdings are not migrated")
		}
		return cw.Close()
	}
	next := day(c.asOf.AddDate(0, 0, 1))
	r, err := c.d.one(ctx, fmt.Sprintf(qHoldDate, inList(dpc), next))
	if err != nil {
		return err
	}
	hd := tm(r, "d")
	cw, err := c.w.Master(c.f.Code, srcHoldings, h)
	if err != nil {
		return err
	}
	if hd.IsZero() {
		c.rep.warnf(area, "", "no-hold-date", "Load the depository's holding file at go-live", "No DP holdings on or before %s", day(c.asOf))
		return cw.Close()
	}
	if day(hd) != day(c.asOf) {
		c.rep.warnf(area, "", "hold-date", "Load the depository's holding file for the go-live date to bring them up to date", "The latest DP holdings in SharePro are of %s, not %s", day(hd), day(c.asOf))
	}
	ucc := map[string]string{}
	for _, a := range c.cm.dp {
		if a.ucc != "" {
			ucc[a.co+"|"+a.ben] = a.ucc
		}
	}
	syms := c.symbols(ctx)
	hs := map[string]*holding{}
	unlinked, unlinkedQty := map[string]bool{}, money.Dec{}
	list := "SUM(h.BALANCE) AS bal, SUM(h.PLDGBAL) AS pldg"
	if !c.s.hasCol("HOLD", "PLDGBAL") {
		list = "SUM(h.BALANCE) AS bal, 0 AS pldg"
	}
	err = c.d.query(ctx, fmt.Sprintf(qHold, list, inList(dpc), day(hd), day(hd.AddDate(0, 0, 1))), func(r migrate.Row) error {
		key := str(r, "co") + "|" + upper(r, "ben")
		u := ucc[key]
		q := dec(r, "bal")
		if q.IsZero() {
			return nil
		}
		if u == "" {
			unlinked[key] = true
			unlinkedQty = unlinkedQty.Add(q)
			return nil
		}
		if c.skipped[u] {
			c.rep.errorf(area, u, "skipped-holding", "Migrate dormant clients as inactive (dormant mode: inactive)", "Dormant client left out of the client master holds %s of %s", q.String(), upper(r, "isin"))
			return nil
		}
		k := u + "|" + upper(r, "isin")
		x := hs[k]
		if x == nil {
			x = &holding{ucc: u, isin: upper(r, "isin")}
			hs[k] = x
		}
		x.qty = x.qty.Add(q)
		x.pledg = x.pledg.Add(dec(r, "pldg"))
		return nil
	})
	if err != nil {
		cw.Close()
		return err
	}
	for _, k := range sortedKeys(hs) {
		x := hs[k]
		if !c.written[x.ucc] {
			c.rep.warnf(area, x.ucc, "not-client", "Check the trading code on the DP account (CLLIST.SUBCODE)", "DP account's trading code is not in the client master; its holdings are listed under it anyway")
		}
		pl := money.Min(x.pledg, x.qty)
		if err := cw.Write(rec(h, map[string]string{"UCC": x.ucc, "ISIN": x.isin, "Symbol": syms[x.isin], "Qty": qtyInt(x.qty, c.rep, x.ucc), "Pledged": qtyInt(pl, c.rep, x.ucc)})); err != nil {
			cw.Close()
			return err
		}
	}
	if err := cw.Close(); err != nil {
		return err
	}
	if len(unlinked) > 0 {
		c.rep.warnf(area, "", "unlinked", "These are DP-only clients: load them when Ledgerline's DP master is set up", "%d DP accounts with holdings (%s shares and units) have no trading code in SharePro and are not in DP-HOLDINGS", len(unlinked), unlinkedQty.String())
	}
	clients := inList(sortedKeys(c.written))
	r, err = c.d.one(ctx, fmt.Sprintf(qCheckHold, inList(dpc), day(hd), day(hd.AddDate(0, 0, 1)), clients))
	if err != nil {
		return err
	}
	c.rep.check(pkg.CheckDPQuantity, dec(r, "q").String(), "Total DP holding (HOLD.BALANCE) on "+day(hd)+" of the DP accounts of migrated clients")
	if c.s.hasCol("CLLIST", "BENCLOSEDT") {
		r, err = c.d.one(ctx, fmt.Sprintf(qCheckDP, inList(dpc), clients))
		if err != nil {
			return err
		}
		c.rep.check(pkg.CheckDPAccounts, fmt.Sprint(i64(r, "n")), "Open DP accounts (CLLIST) of migrated clients; CLIENT-MASTER carries one demat account per client")
	}
	return nil
}

// qtyInt writes a quantity as a whole number (fractional units are
// reported).
func qtyInt(q money.Dec, rep *report, key string) string {
	a := q.Round(money.UnitRupee)
	if a.Dec().Cmp(q) != 0 {
		rep.warnf("DP holdings", key, "fraction", "Check fractional units (mutual fund units) after loading", "Quantity %s is not a whole number; rounded to %d", q.String(), a.Paise()/100)
	}
	return fmt.Sprint(a.Paise() / 100)
}

// ---- open positions ----

type posLot struct {
	qty, price money.Dec // qty signed
}

func (c *cur) derivCOs() []string {
	var out []string
	for _, co := range c.f.COs {
		switch c.f.segOf(co) {
		case "FO", "CD", "COM":
			out = append(out, co)
		}
	}
	return out
}

// itcApply is the item master lookup for a trade (alias i), and the
// contract master for the option type (alias k).
func (c *cur) itcApply() (cols, apply string) {
	il, _ := c.s.sel("ITC", "ITC", "INSTR_TYPE", "EXP_DATE", "CONT_RATE", "NSE_ITEMCD", "MCX_ITEMCD", "DESCR")
	apply = " OUTER APPLY (SELECT TOP 1 " + il + " FROM ITC WHERE ITC.ITEMCD = t.ITEMCD) i"
	cols = "i.instr_type, i.exp_date, i.cont_rate, i.nse_itemcd, i.mcx_itemcd, i.descr"
	if c.s.has("ICCLCONT") {
		kl, _ := c.s.sel("ICCLCONT", "ICCLCONT", "Cont_type", "AssetCd")
		apply += " OUTER APPLY (SELECT TOP 1 " + kl + " FROM ICCLCONT WHERE ICCLCONT.Nse_itemcd = t.ITEMCD OR ICCLCONT.Bse_itemcd = t.ITEMCD) k"
		cols += ", k.cont_type, k.assetcd"
	} else {
		cols += ", NULL AS cont_type, NULL AS assetcd"
	}
	return cols, apply
}

// instrument reads FUT, CE or PE and the underlying from the item master.
func instrument(r migrate.Row) (typ, symbol string) {
	it := upper(r, "instr_type")
	descr := upper(r, "descr")
	switch {
	case strings.HasPrefix(it, "FUT"):
		typ = "FUT"
	case strings.HasPrefix(it, "OPT"):
		switch ct := upper(r, "cont_type"); {
		case strings.HasPrefix(ct, "C"):
			typ = "CE"
		case strings.HasPrefix(ct, "P"):
			typ = "PE"
		default:
			for _, w := range strings.Fields(descr) {
				switch w {
				case "CE", "CA", "CALL":
					typ = "CE"
				case "PE", "PA", "PUT":
					typ = "PE"
				}
			}
		}
	}
	symbol = firstNonEmpty(upper(r, "assetcd"), upper(r, "nse_itemcd"), upper(r, "mcx_itemcd"))
	if symbol == "" && descr != "" {
		symbol = strings.Fields(descr)[0]
	}
	return typ, symbol
}

func (c *cur) writePositions(ctx context.Context) error {
	area := "Open positions"
	h := headers[srcPositions]
	cos := c.derivCOs()
	cw, err := c.w.Master(c.f.Code, srcPositions, h)
	if err != nil {
		return err
	}
	if len(cos) == 0 || !c.s.has("TRFILE") || !c.s.has("ITC") {
		return cw.Close()
	}
	cols, apply := c.itcApply()
	next, asOf := day(c.asOf.AddDate(0, 0, 1)), day(c.asOf)
	type posKey struct{ ucc, co, item string }
	var curKey posKey
	var lots []posLot
	var meta migrate.Row
	n := 0
	var werr error
	flushPos := func() {
		if meta == nil || werr != nil {
			return
		}
		net, val := money.Dec{}, money.Dec{}
		for _, l := range lots {
			net = net.Add(l.qty)
			val = val.Add(l.qty.Abs().Mul(l.price))
		}
		if net.IsZero() {
			return
		}
		u := curKey.ucc
		if c.skipped[u] {
			c.rep.errorf(area, u, "skipped-position", "Migrate dormant clients as inactive (dormant mode: inactive)", "Dormant client left out of the client master has an open position in %s", curKey.item)
			return
		}
		typ, sym := instrument(meta)
		if typ == "" {
			c.rep.warnf(area, u, "instrument", "Add the position by hand or load the clearing corporation's position file", "Instrument type of item %s (%s) cannot be told; the position is left out", curKey.item, upper(meta, "instr_type"))
			return
		}
		avg, _ := val.Quo(net.Abs())
		strike := ""
		if typ != "FUT" {
			strike = num(dec(meta, "cont_rate"))
		}
		q := net.Round(money.UnitRupee)
		werr = cw.Write(rec(h, map[string]string{"UCC": u, "Exchange": c.f.exchOf(curKey.co), "Segment": c.f.segOf(curKey.co), "Symbol": sym, "Type": typ,
			"Expiry": date(meta, "exp_date"), "Strike": strike, "Qty": fmt.Sprint(q.Paise() / 100), "AvgPrice": avg.Fixed(4)}))
		n++
	}
	err = c.d.query(ctx, fmt.Sprintf(qPositions, cols, apply, inList(cos), next, asOf, c.s.order("TRFILE", "t", "TRDT", "NPK")), func(r migrate.Row) error {
		k := posKey{upper(r, "subcd"), str(r, "co"), upper(r, "itemcd")}
		if k != curKey {
			flushPos()
			curKey, lots, meta = k, nil, r
		}
		addFIFO(&lots, posLot{dec(r, "qty"), dec(r, "price")})
		return werr
	})
	flushPos()
	if err == nil {
		err = werr
	}
	if err != nil {
		cw.Close()
		return err
	}
	if err := cw.Close(); err != nil {
		return err
	}
	r, err := c.d.one(ctx, fmt.Sprintf(qCheckPos, apply, inList(cos), next, asOf))
	if err != nil {
		return err
	}
	c.rep.check(pkg.CheckPositions, fmt.Sprint(i64(r, "n")), "Open derivative positions (net quantity of unexpired contracts in TRFILE) as of "+asOf)
	c.rep.infof(area, "", "positions", "%d open positions from SharePro's trades; check them against the clearing corporation's position file on the go-live day", n)
	return nil
}

// addFIFO adds a trade to a position's open lots: a trade on the other
// side closes the oldest lots first.
func addFIFO(lots *[]posLot, t posLot) {
	q := t.qty
	for q.Sign() != 0 && len(*lots) > 0 && (*lots)[0].qty.Sign() != q.Sign() {
		l := &(*lots)[0]
		if l.qty.Abs().Cmp(q.Abs()) <= 0 {
			q = q.Add(l.qty)
			*lots = (*lots)[1:]
		} else {
			l.qty = l.qty.Add(q)
			q = money.Dec{}
		}
	}
	if q.Sign() != 0 {
		*lots = append(*lots, posLot{q, t.price})
	}
}
