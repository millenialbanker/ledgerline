package sharepro

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"ledgerline/internal/charges"
	"ledgerline/internal/migrate"
	"ledgerline/internal/money"
)

// Brokerage. SharePro keeps each client's own rate rows per CO (exchange
// segment) with an effective date: NSQBRKG for square-off (intraday, and
// futures and options trades), NSDBRKG for delivery (and, in derivatives,
// expiry, exercise and assignment). OTHBRKG adds minimums per order and per
// contract note. Ledgerline gets them as BROKERAGE-CLIENT-RATES: for every
// date on which any of a client's rows changes, a version with every line
// in force from that date.

const (
	qBrokQ   = `/*sp:brokerage-sqr*/ SELECT %s FROM NSQBRKG ORDER BY SUBCD, EffectDate`
	qBrokD   = `/*sp:brokerage-del*/ SELECT %s FROM NSDBRKG ORDER BY SUBCD, EffectDate`
	qBrokOth = `/*sp:brokerage-other*/ SELECT %s FROM OTHBRKG ORDER BY SUBCD, EffectDate`
	qItems   = `/*sp:items*/ SELECT RTRIM(i.ITEMCD) AS itemcd, %s FROM ITC i`
	qItemISN = `/*sp:item-isin*/ SELECT RTRIM(ITEMCD) AS itemcd, RTRIM(ISINCD) AS isin FROM ISINMAST`
	qIntDtl  = `/*sp:interest*/ SELECT %s FROM INTERESTDTL ORDER BY Subcd, EffectDate`
)

type rateRow struct {
	kind                     string // Q or D
	co, ucc, from, item, und string
	prod, atwhat, minPer, of string
	flatPer, slab            string
	pct, minRate, flat       money.Dec
}

type rateLine struct {
	key  string
	v    map[string]string
	from string
}

func (c *cur) writeBrokerage(ctx context.Context) error {
	area := "Brokerage"
	h := headers[srcBrokRates]
	if !c.s.has("NSQBRKG") && !c.s.has("NSDBRKG") {
		cw, err := c.w.Master(c.f.Code, srcBrokRates, h)
		if err != nil {
			return err
		}
		c.rep.infof(area, "", "no-tables", "This database has no brokerage tables (NSQBRKG, NSDBRKG); BROKERAGE-CLIENT-RATES is empty")
		return cw.Close()
	}
	items := map[string][2]string{} // item -> symbol, ISIN
	if c.s.has("ITC") {
		list, _ := c.s.sel("ITC", "i", "NSE_ITEMCD", "BSE_ITEMCD", "MCX_ITEMCD", "DESCR")
		_ = c.d.query(ctx, fmt.Sprintf(qItems, list), func(r migrate.Row) error {
			items[upper(r, "itemcd")] = [2]string{firstNonEmpty(upper(r, "nse_itemcd"), upper(r, "bse_itemcd"), upper(r, "mcx_itemcd")), ""}
			return nil
		})
	}
	if c.s.has("ISINMAST") {
		_ = c.d.query(ctx, qItemISN, func(r migrate.Row) error {
			x := items[upper(r, "itemcd")]
			if x[1] == "" {
				x[1] = upper(r, "isin")
			}
			items[upper(r, "itemcd")] = x
			return nil
		})
	}
	rows := map[string][]rateRow{}
	read := func(q, table, kind string, cols map[string]string) error {
		if !c.s.has(table) {
			return nil
		}
		var names []string
		for _, k := range []string{"SUBCD", "EffectDate", "ITEMCD", "UNDITEMCD", "CO", "PRODUCTCD", "ATWHAT", cols["pct"], cols["min"], cols["minper"], cols["of"], cols["flat"], cols["flatper"], cols["slab"], cols["extra"]} {
			if k != "" {
				names = append(names, k)
			}
		}
		list, _ := c.s.sel(table, "", names...)
		return c.d.query(ctx, fmt.Sprintf(q, list), func(r migrate.Row) error {
			lc := func(k string) string { return strings.ToLower(cols[k]) }
			x := rateRow{kind: kind, co: str(r, "co"), ucc: upper(r, "subcd"), from: date(r, "effectdate"), item: upper(r, "itemcd"), und: upper(r, "unditemcd"),
				prod: upper(r, "productcd"), atwhat: strings.ToUpper(strings.Join(strings.Fields(str(r, "atwhat")+" "+str(r, lc("extra"))), " ")),
				minPer: upper(r, lc("minper")), of: upper(r, lc("of")), flatPer: upper(r, lc("flatper")), slab: str(r, lc("slab")),
				pct: dec(r, lc("pct")), minRate: dec(r, lc("min")), flat: dec(r, lc("flat"))}
			if x.from == "" {
				x.from = "2000-01-01"
			}
			rows[x.ucc] = append(rows[x.ucc], x)
			return nil
		})
	}
	if err := read(qBrokQ, "NSQBRKG", "Q", map[string]string{"pct": "SQBRPTGE", "min": "SQBRMNRT", "minper": "SQBRMNPER", "of": "OF", "flat": "SQBRFLRT", "flatper": "SQFLATPER", "slab": "SLABCD"}); err != nil {
		return err
	}
	if err := read(qBrokD, "NSDBRKG", "D", map[string]string{"pct": "DLBRPTGE", "min": "DLBRKGMIN", "minper": "DLBRMNPER", "flat": "DLBRKGFLT", "flatper": "DLFLATPER", "slab": "DLSLABCD", "extra": "ONWHICH"}); err != nil {
		return err
	}
	type oth struct {
		co, from        string
		minOrd, minNote money.Dec
		minLot          money.Dec
		template        string
	}
	others := map[string][]oth{}
	if c.s.has("OTHBRKG") {
		list, _ := c.s.sel("OTHBRKG", "", "SUBCD", "CO", "EffectDate", "ORD_MINBRK", "CNT_MINBRK", "MINLOTBRKG", "DEFA_BRKG", "brkgbasket")
		if err := c.d.query(ctx, fmt.Sprintf(qBrokOth, list), func(r migrate.Row) error {
			u := upper(r, "subcd")
			others[u] = append(others[u], oth{co: str(r, "co"), from: firstNonEmpty(date(r, "effectdate"), "2000-01-01"), minOrd: dec(r, "ord_minbrk"), minNote: dec(r, "cnt_minbrk"),
				minLot: dec(r, "minlotbrkg"), template: upper(r, "defa_brkg")})
			if b := str(r, "brkgbasket"); b != "" {
				c.rep.warnf(area, u, "basket", "Set the client's brokerage by hand in Ledgerline", "Brokerage basket %s (OTHBRKG.brkgbasket) is not converted", b)
			}
			return nil
		}); err != nil {
			return err
		}
	}
	cw, err := c.w.Master(c.f.Code, srcBrokRates, h)
	if err != nil {
		return err
	}
	clients := 0
	for _, u := range c.clientList() {
		if !c.written[u] {
			continue
		}
		rs := rows[u]
		os := others[u]
		// a client whose rates are another client's (OTHBRKG.DEFA_BRKG)
		if len(rs) == 0 {
			for _, o := range os {
				if o.template != "" && o.template != u && len(rows[o.template]) > 0 {
					for _, x := range rows[o.template] {
						x.ucc = u
						rs = append(rs, x)
					}
					c.rep.infof(area, u, "template", "Rates copied from client %s, whose rates this client follows in SharePro (OTHBRKG.DEFA_BRKG)", o.template)
					break
				}
			}
		}
		if len(rs) == 0 {
			continue
		}
		// every row as Ledgerline lines
		var lines []rateLine
		for _, x := range rs {
			if !contains(c.f.COs, x.co) {
				continue
			}
			ls, err := c.convertRate(x, items)
			if err != nil {
				c.rep.warnf(area, u, "convert", "Set this rate by hand in Ledgerline", "Rate row of %s from %s (CO %s, item %s): %v", x.kind, x.from, x.co, x.item, err)
				continue
			}
			lines = append(lines, ls...)
		}
		if len(lines) == 0 {
			continue
		}
		dates := map[string]bool{}
		for _, l := range lines {
			dates[l.from] = true
		}
		for _, o := range os {
			if contains(c.f.COs, o.co) {
				dates[o.from] = true
			}
		}
		clients++
		for _, d := range sortedKeys(dates) {
			// the latest row of each line in force on d
			cur := map[string]rateLine{}
			for _, l := range lines {
				if l.from <= d {
					if old, ok := cur[l.key]; !ok || l.from >= old.from {
						cur[l.key] = l
					}
				}
			}
			if len(cur) == 0 {
				continue
			}
			minOrd := map[string]oth{}
			for _, o := range os {
				if o.from <= d && contains(c.f.COs, o.co) && o.from >= minOrd[o.co].from {
					minOrd[o.co] = o
				}
			}
			for _, k := range sortedKeys(cur) {
				l := cur[k]
				v := map[string]string{}
				for a, b := range l.v {
					v[a] = b
				}
				v["UCC"], v["From"] = u, d
				if o, ok := minOrd[v["_co"]]; ok && v["Mode"] != charges.PlanZero {
					if o.minOrd.Sign() > 0 && v["Min"] == "" {
						v["Min"] = o.minOrd.String()
					}
					if o.minNote.Sign() > 0 {
						v["MinPerNote"] = o.minNote.String()
					}
					if o.minLot.Sign() > 0 && v["MinPerLot"] == "" && !strings.HasPrefix(v["Product"], "CM") {
						v["MinPerLot"] = o.minLot.String()
					}
				}
				if err := cw.Write(rec(h, v)); err != nil {
					cw.Close()
					return err
				}
			}
		}
	}
	if err := cw.Close(); err != nil {
		return err
	}
	c.rep.infof(area, "", "summary", "%d clients have their own brokerage rates from SharePro; the others take the firm's default card, which is set in Ledgerline", clients)
	return nil
}

// convertRate turns one SharePro rate row into Ledgerline rate lines.
func (c *cur) convertRate(x rateRow, items map[string][2]string) ([]rateLine, error) {
	seg := c.f.segOf(x.co)
	if seg == "" {
		return nil, fmt.Errorf("SharePro company %s is not an exchange segment", x.co)
	}
	if x.slab != "" {
		return nil, fmt.Errorf("slab %s rates are not converted", x.slab)
	}
	var products []string
	kase, scrip, und := "", "", ""
	item := x.item
	cash := seg == "CM"
	fut, opt := seg+"-FUT", seg+"-OPT"
	switch {
	case item == "" || item == "ALL":
	case item == "ALLAUC":
		kase = charges.CaseAuction
	case item == "ALLROL":
		kase = charges.CaseRollover
	case item == "ALLFUT":
		products = []string{fut}
	case item == "ALLOPT":
		products = []string{opt}
	case item == "ALLFID":
		products, kase = []string{fut}, charges.CaseIndex
	case item == "ALLFST":
		products, kase = []string{fut}, charges.CaseStock
	case item == "ALLOID":
		products, kase = []string{opt}, charges.CaseIndex
	case item == "ALLSTK":
		products, kase = []string{opt}, charges.CaseStock
	case strings.HasPrefix(item, "ALL"):
		return nil, fmt.Errorf("item group %s is not known", item)
	default:
		it := items[item]
		if cash {
			scrip = firstNonEmpty(it[1], it[0])
		} else {
			und = it[0]
		}
		if scrip == "" && und == "" {
			return nil, fmt.Errorf("item %s is not in the item master", item)
		}
	}
	if x.und != "" && !cash {
		und = firstNonEmpty(items[x.und][0], x.und)
	}
	if !cash && x.kind == "D" {
		// delivery rows of derivatives: settlement at expiry
		switch {
		case strings.Contains(x.atwhat, "ASSIGN"):
			products, kase = []string{opt}, charges.CaseAssignment
		case strings.Contains(x.atwhat, "EXCER"), strings.Contains(x.atwhat, "EXER"):
			products, kase = []string{opt}, charges.CaseExercise
		case strings.Contains(x.atwhat, "DELIVERY"), strings.Contains(x.atwhat, "PHYSICAL"):
			kase = charges.CasePhysical
		default:
			products, kase = []string{fut}, charges.CaseExpiry
		}
	}
	if len(products) == 0 {
		switch {
		case cash && x.kind == "Q":
			products = []string{"CM-INTRA"}
		case cash:
			products = []string{"CM-DEL"}
		default:
			products = []string{fut, opt}
		}
	}
	rule := charges.BrokerageRule{}
	v := map[string]string{"Exchange": c.f.exchOf(x.co), "_co": x.co}
	perLot := strings.Contains(x.flatPer, "LOT")
	switch {
	case x.pct.Sign() > 0:
		rule.Mode, rule.Pct = charges.PlanPct, x.pct
		switch {
		case x.minRate.Sign() == 0:
		case strings.Contains(x.minPer, "LOT"):
			rule.MinPerLot = x.minRate
		case strings.Contains(x.minPer, "ORDER"):
			rule.Min = x.minRate
		default: // per share (or per unit of the instrument)
			rule.MinPerUnit = x.minRate
		}
		if strings.Contains(x.of, "MKT") {
			rule.Basis = charges.BasisMarket
		}
	case x.flat.Sign() > 0 && perLot:
		rule.Mode, rule.PerLot = charges.PlanPerLot, x.flat
	case x.flat.Sign() > 0 && strings.Contains(x.flatPer, "ORDER"):
		rule.Mode, rule.Flat = charges.PlanFlat, x.flat
	case x.flat.Sign() > 0:
		rule.Mode, rule.PerUnit = charges.PlanPerUnit, x.flat
	default:
		rule.Mode = charges.PlanZero
	}
	if err := rule.Validate(); err != nil {
		return nil, err
	}
	mode := rule.Mode
	if mode == charges.PlanPerUnit {
		mode = "per_share"
	}
	v["Mode"] = mode
	for k, d := range map[string]money.Dec{"Pct": rule.Pct, "Flat": rule.Flat, "PerLot": rule.PerLot, "PerShare": rule.PerUnit, "MinPerShare": rule.MinPerUnit, "MinPerLot": rule.MinPerLot, "Min": rule.Min} {
		if d.Sign() > 0 {
			v[k] = d.String()
		}
	}
	if rule.Basis != "" {
		v["Basis"] = rule.Basis
	}
	if x.prod != "" {
		v["Note"] = "SharePro product " + x.prod
	}
	var out []rateLine
	for _, p := range products {
		k := kase
		if !charges.CaseFits(k, charges.Bucket(p)) && k != "" {
			continue
		}
		target := ""
		switch {
		case scrip != "":
			target = "scrip:" + scrip
		case und != "":
			target = "und:" + und
		}
		key, err := charges.NewTargetKey(p, v["Exchange"], "", k, target)
		if err != nil {
			return nil, err
		}
		lv := map[string]string{}
		for a, b := range v {
			lv[a] = b
		}
		lv["Product"], lv["Case"], lv["Scrip"], lv["Underlying"] = p, k, scrip, und
		out = append(out, rateLine{key: key.String(), v: lv, from: x.from})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("case %s fits none of the products", kase)
	}
	return out, nil
}

// ---- client interest ----

func (c *cur) writeInterest(ctx context.Context) error {
	h := headers[srcInterest]
	cw, err := c.w.Master(c.f.Code, srcInterest, h)
	if err != nil {
		return err
	}
	type term struct {
		from, rate, grace, free, seg string
	}
	by := map[string][]term{}
	if c.s.has("INTERESTDTL") {
		list, _ := c.s.sel("INTERESTDTL", "", "Subcd", "EffectDate", "InterestPer", "NoInterest", "cGraceDays", "SegmentType")
		if err := c.d.query(ctx, fmt.Sprintf(qIntDtl, list), func(r migrate.Row) error {
			t := term{from: firstNonEmpty(date(r, "effectdate"), day(fyStart(c.k.FY))), seg: str(r, "segmenttype")}
			if p := dec(r, "interestper"); p.Sign() > 0 {
				t.rate = p.String()
			}
			if flag(r, "nointerest") {
				t.free, t.rate = "Y", ""
			}
			if g := str(r, "cgracedays"); g != "" {
				if n, err := strconv.Atoi(g); err == nil && n >= 0 {
					t.grace = strconv.Itoa(n)
				}
			}
			by[upper(r, "subcd")] = append(by[upper(r, "subcd")], t)
			return nil
		}); err != nil {
			cw.Close()
			return err
		}
	}
	for _, u := range c.clientList() {
		if !c.written[u] {
			continue
		}
		ts := by[u]
		sort.SliceStable(ts, func(i, j int) bool { return ts[i].from < ts[j].from })
		if len(ts) == 0 && c.cm.clients[u].noInterest {
			ts = []term{{from: day(fyStart(c.k.FY)), free: "Y"}}
		}
		own := false
		for _, t := range ts {
			if t.seg != "" {
				c.rep.infof("Client interest", u, "segment", "SharePro has a segment-wise interest row (%s); Ledgerline's client interest applies to all segments", t.seg)
			}
			has := t.rate != "" || t.free != "" || t.grace != ""
			if !has && !own {
				continue // the firm's terms, as before
			}
			v := map[string]string{"UCC": u, "From": t.from, "Rate": t.rate, "GraceDays": t.grace, "Free": t.free}
			if !has {
				v["Note"] = "Back to the firm's terms"
			}
			if err := cw.Write(rec(h, v)); err != nil {
				cw.Close()
				return err
			}
			own = has
		}
	}
	return cw.Close()
}

// ---- KRA and CKYC ----

// kraStatus maps SharePro's KYC register status (KYCMAST.STATUS) to the
// KRA statuses Ledgerline knows.
func kraStatus(s string) string {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "UPLOADED", "REGISTERED", "VERIFIED", "VALIDATED", "MIGRATED", "KRA VERIFIED":
		if s == "VALIDATED" || s == "KRA VERIFIED" {
			return "Validated"
		}
		return "Registered"
	case "ON HOLD", "HOLD":
		return "On hold"
	case "REJECTED":
		return "Rejected"
	case "PENDING", "UNDER PROCESS", "SUBMITTED":
		return "Under process"
	}
	return ""
}

func (c *cur) writeKYC(ctx context.Context) error {
	h := headers[srcKRA]
	cw, err := c.w.Master(c.f.Code, srcKRA, h)
	if err != nil {
		return err
	}
	type ck struct{ pan, num string }
	var ckycs []ck
	seen := map[string]bool{}
	for _, u := range c.clientList() {
		if !c.written[u] {
			continue
		}
		pan := c.cm.clients[u].pan
		k, ok := c.cm.kycByPAN[pan]
		if !ok || seen[pan] || !rePAN.MatchString(pan) {
			continue
		}
		seen[pan] = true
		if st := kraStatus(k.status); st != "" {
			if err := cw.Write(rec(h, map[string]string{"PAN": pan, "Status": st, "Remarks": "SharePro KYC register: " + strings.ToLower(k.status)})); err != nil {
				cw.Close()
				return err
			}
		} else if k.status != "" {
			c.rep.infof("KRA status", u, "kra-status", "KYC register status %q is not a KRA status; not migrated (Ledgerline asks the KRA)", k.status)
		}
		if n := reDigits.ReplaceAllString(k.kycno, ""); len(n) == 14 && n == strings.TrimSpace(k.kycno) {
			ckycs = append(ckycs, ck{pan, n})
		}
	}
	if err := cw.Close(); err != nil {
		return err
	}
	h = headers[srcCKYC]
	cw, err = c.w.Master(c.f.Code, srcCKYC, h)
	if err != nil {
		return err
	}
	for _, x := range ckycs {
		if err := cw.Write(rec(h, map[string]string{"PAN": x.pan, "CKYCNumber": x.num})); err != nil {
			cw.Close()
			return err
		}
	}
	return cw.Close()
}
