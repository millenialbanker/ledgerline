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

// itemApply joins the item master to trades (alias t): ITC as i, the
// contract master ICCLCONT as k, ISINMAST as x. Verified against a real
// database: ICCLCONT.Cont_type is M or W (not call or put); the option type
// is read from the series and descriptions (optType).
func itemApply(s *schema) (cols, apply string) {
	col := func(table, alias, c, as string) string {
		if s.hasCol(table, c) {
			return alias + ".[" + c + "] AS " + as
		}
		return "NULL AS " + as
	}
	var ic, kc []string
	for _, x := range [][2]string{{"INSTR_TYPE", "instr_type"}, {"EXP_DATE", "exp_date"}, {"CONT_RATE", "cont_rate"}, {"NSE_ITEMCD", "nse_itemcd"},
		{"NSE_SERIES", "nse_series"}, {"MCX_ITEMCD", "mcx_itemcd"}, {"DESCR", "descr"}} {
		ic = append(ic, col("ITC", "ITC", x[0], x[1]))
	}
	for _, x := range [][2]string{{"AssetCd", "assetcd"}, {"Nse_series", "k_series"}, {"ContDescr", "k_descr"}, {"Bfo_SrsCd", "k_srscd"}, {"ProductTp", "k_prod"}} {
		kc = append(kc, col("ICCLCONT", "ICCLCONT", x[0], x[1]))
	}
	cols = "NULL AS instr_type, NULL AS exp_date, NULL AS cont_rate, NULL AS nse_itemcd, NULL AS nse_series, NULL AS mcx_itemcd, NULL AS descr"
	if s.has("ITC") {
		apply = " OUTER APPLY (SELECT TOP 1 " + strings.Join(ic, ", ") + " FROM ITC WHERE ITC.ITEMCD = t.ITEMCD) i"
		cols = "i.instr_type, i.exp_date, i.cont_rate, i.nse_itemcd, i.nse_series, i.mcx_itemcd, i.descr"
	}
	if s.has("ICCLCONT") && s.hasCol("ICCLCONT", "Nse_itemcd") && s.hasCol("ICCLCONT", "Bse_itemcd") {
		apply += " OUTER APPLY (SELECT TOP 1 " + strings.Join(kc, ", ") + " FROM ICCLCONT WHERE ICCLCONT.Nse_itemcd = t.ITEMCD OR ICCLCONT.Bse_itemcd = t.ITEMCD) k"
		cols += ", k.assetcd, k.k_series, k.k_descr, k.k_srscd, k.k_prod"
	} else {
		cols += ", NULL AS assetcd, NULL AS k_series, NULL AS k_descr, NULL AS k_srscd, NULL AS k_prod"
	}
	return cols, apply
}

// optType reads CE or PE from a series ("CE"), a description ("NIFTY
// 29OCT 21000 PE") or a series code ending in CE or PE after a digit.
func optType(vals ...string) string {
	for _, v := range vals {
		v = strings.ToUpper(strings.TrimSpace(v))
		for _, w := range strings.Fields(v) {
			switch w {
			case "CE", "CA", "CALL":
				return "CE"
			case "PE", "PA", "PUT":
				return "PE"
			}
		}
		if n := len(v); n > 3 && v[n-3] >= '0' && v[n-3] <= '9' {
			switch v[n-2:] {
			case "CE":
				return "CE"
			case "PE":
				return "PE"
			}
		}
	}
	return ""
}

// instrument reads FUT, CE or PE (empty when an option's type cannot be
// told) and the underlying from the item master.
func instrument(r migrate.Row) (typ, symbol string) {
	it := upper(r, "instr_type")
	if it == "" {
		// ICCLCONT.ProductTp: SF, IF stock and index futures; SO, IO options
		switch upper(r, "k_prod") {
		case "SF", "IF":
			it = "FUT"
		case "SO", "IO":
			it = "OPT"
		}
	}
	descr := upper(r, "descr")
	switch {
	case strings.HasPrefix(it, "FUT"):
		typ = "FUT"
	case strings.HasPrefix(it, "OPT"):
		typ = optType(str(r, "nse_series"), str(r, "k_series"), str(r, "k_descr"), descr, str(r, "k_srscd"))
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
	cols, apply := itemApply(c.s)
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
			c.rep.warnf(area, u, "instrument", "Add the position by hand or load the clearing corporation's position file", "Instrument type of item %s (%s) cannot be told (an option's call or put is not in its series or description); the position is left out", curKey.item, upper(meta, "instr_type"))
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
