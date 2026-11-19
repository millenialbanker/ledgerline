package sharepro

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"ledgerline/internal/charges"
	"ledgerline/internal/migrate"
	"ledgerline/internal/migrate/pkg"
	"ledgerline/internal/money"
)

// Brokerage. SharePro keeps rate rows per CO (exchange segment) with an
// effective date: NSQBRKG for square-off (intraday, and futures and options
// trades), NSDBRKG for delivery (and, in derivatives, expiry, exercise and
// assignment), PRODUCTCD the trading channel. OTHBRKG adds minimums per
// order and per contract note, and the client's basket (brkgbasket).
// Baskets are not a table of their own: a basket is a dummy client code
// with its own rate rows and OTHBRKG rows, absent from the client master.
// Ledgerline gets:
//   - BROKERAGE-CARDS: every basket a client points at, as a rate card;
//   - BROKERAGE-CLIENTS: which basket each client is on, per exchange
//     segment, from the dates OTHBRKG gives;
//   - BROKERAGE-CLIENT-RATES: a client's own rows, which win over the
//     basket line by line (as in SharePro).
// Cards and clients' own rates are versioned the same way: for every date
// on which any row changes, a version with every line in force from then.

const (
	qBrokQ   = `/*sp:brokerage-sqr*/ SELECT %s FROM NSQBRKG ORDER BY SUBCD, EffectDate`
	qBrokD   = `/*sp:brokerage-del*/ SELECT %s FROM NSDBRKG ORDER BY SUBCD, EffectDate`
	qBrokOth = `/*sp:brokerage-other*/ SELECT %s FROM OTHBRKG ORDER BY SUBCD, EffectDate`
	qItems   = `/*sp:items*/ SELECT RTRIM(i.ITEMCD) AS itemcd, %s FROM ITC i`
	qItemISN = `/*sp:item-isin*/ SELECT RTRIM(ITEMCD) AS itemcd, RTRIM(ISINCD) AS isin FROM ISINMAST`
	qIntDtl  = `/*sp:interest*/ SELECT %s FROM INTERESTDTL ORDER BY Subcd, EffectDate`
	qTermCh  = `/*sp:terminal-channels*/ SELECT RTRIM(CTCLID) AS ctcl, RTRIM(PRODUCTCD) AS productcd, COUNT(*) AS n FROM TRFILE WHERE CO IN (%s) GROUP BY RTRIM(CTCLID), RTRIM(PRODUCTCD)`
)

// termShare is the share of a terminal's trades that must carry one
// channel for the terminal to be listed as that channel.
const termShare = 0.9

const qProducts = `/*sp:products*/ SELECT RTRIM(PRODUCTCD) AS productcd, RTRIM(DESCR) AS descr FROM PRDMAST`

// spProduct is what a SharePro product code (PRODUCTCD on rate rows and
// trades) means: a trading channel, or futures / options settled by
// delivery of shares. The codes are the firm's own, from SharePro's
// product master (PRDMAST), read by their description, so a firm with
// other codes needs no change here:
//   - "internet" → online, "mobile" → mobile, "dealer" → dealer;
//   - "physical delivery" with "future" or "option" → physical (P-F, P-O);
//   - any other code becomes a channel of its own, named after the code
//     (e.g. "dir" for DIR, "direct trading by client"), so its rates are
//     kept apart as SharePro kept them.
//
// Blank is every channel: a channel's own line wins over it. A code not
// in PRDMAST (or a backup without PRDMAST) is read the same way from the
// code alone (IBT, MBT, P-F, P-O as SharePro ships them).
type spProduct struct {
	channel  string
	physical string // "FUT" or "OPT" for delivery of futures / options
}

func readSPProduct(code, descr string) spProduct {
	d := strings.ToUpper(descr + " " + code)
	switch {
	case code == "":
		return spProduct{}
	case strings.Contains(d, "DELIVERY") && (strings.Contains(d, "PHYSICAL") || strings.Contains(d, "PHISICAL")) || code == "P-F" || code == "P-O":
		if strings.Contains(d, "OPTION") || code == "P-O" {
			return spProduct{physical: "OPT"}
		}
		return spProduct{physical: "FUT"}
	case strings.Contains(d, "INTERNET") || code == "IBT":
		return spProduct{channel: "online"}
	case strings.Contains(d, "MOBILE") || code == "MBT":
		return spProduct{channel: "mobile"}
	case strings.Contains(d, "DEALER"):
		return spProduct{channel: "dealer"}
	}
	ch := strings.Trim(reNonSlug.ReplaceAllString(strings.ToLower(code), "-"), "-")
	if len(ch) < 2 {
		ch = "ch-" + ch
	}
	return spProduct{channel: ch}
}

// products is SharePro's product master, read once.
func (c *cur) products(ctx context.Context) map[string]spProduct {
	if c.prods != nil {
		return c.prods
	}
	c.prods = map[string]spProduct{}
	if c.s.has("PRDMAST") {
		_ = c.d.query(ctx, qProducts, func(r migrate.Row) error {
			code := upper(r, "productcd")
			c.prods[code] = readSPProduct(code, str(r, "descr"))
			return nil
		})
	}
	return c.prods
}

func (c *cur) product(ctx context.Context, code string) spProduct {
	if p, ok := c.products(ctx)[code]; ok {
		return p
	}
	return readSPProduct(code, "")
}

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

// othRow is one OTHBRKG row: a client's (or basket's) minimums and basket
// for a CO from a date.
type othRow struct {
	co, from        string
	minOrd, minNote money.Dec
	minLot          money.Dec
	template        string
	basket          string
}

// rateVersion is one dated version of a card or of a client's own rates.
type rateVersion struct {
	from string
	rows []map[string]string
}

// rateVersions turns rate lines and the OTHBRKG minimums into versions:
// for every date on which a line or a minimum changes, the latest row of
// each line in force on that date.
func (c *cur) rateVersions(lines []rateLine, os []othRow) []rateVersion {
	dates := map[string]bool{}
	for _, l := range lines {
		dates[l.from] = true
	}
	for _, o := range os {
		if contains(c.f.COs, o.co) {
			dates[o.from] = true
		}
	}
	var out []rateVersion
	for _, d := range sortedKeys(dates) {
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
		minOrd := map[string]othRow{}
		for _, o := range os {
			if o.from <= d && contains(c.f.COs, o.co) && o.from >= minOrd[o.co].from {
				minOrd[o.co] = o
			}
		}
		v := rateVersion{from: d}
		for _, k := range sortedKeys(cur) {
			l := cur[k]
			row := map[string]string{}
			for a, b := range l.v {
				row[a] = b
			}
			row["From"] = d
			if o, ok := minOrd[row["_co"]]; ok && row["Mode"] != charges.PlanZero {
				if o.minOrd.Sign() > 0 && row["Min"] == "" {
					row["Min"] = o.minOrd.String()
				}
				if o.minNote.Sign() > 0 {
					row["MinPerNote"] = o.minNote.String()
				}
				if o.minLot.Sign() > 0 && row["MinPerLot"] == "" && !strings.HasPrefix(row["Product"], "CM") {
					row["MinPerLot"] = o.minLot.String()
				}
			}
			v.rows = append(v.rows, row)
		}
		out = append(out, v)
	}
	return out
}

// convertRows turns a client's or basket's rate rows into lines for the
// firm's COs, warning on each row that cannot be converted.
func (c *cur) convertRows(ctx context.Context, area, who string, rs []rateRow, items map[string][2]string) []rateLine {
	var lines []rateLine
	for _, x := range rs {
		if !contains(c.f.COs, x.co) {
			continue
		}
		ls, err := c.convertRate(ctx, x, items)
		if err != nil {
			c.rep.warnf(area, who, "convert", "Set this rate by hand in Ledgerline", "Rate row of %s from %s (CO %s, item %s): %v", x.kind, x.from, x.co, x.item, err)
			continue
		}
		lines = append(lines, ls...)
	}
	return lines
}

// cardSlug is the plan ID Ledgerline makes from a card's code (ops.cardID).
func cardSlug(code string) string {
	id := strings.Trim(reNonSlug.ReplaceAllString(strings.ToLower(strings.TrimSpace(code)), "-"), "-")
	if len(id) < 2 || !reSlugOK.MatchString(id) {
		id = "card-" + id
	}
	if len(id) > 41 {
		id = strings.TrimRight(id[:41], "-")
	}
	return id
}

var (
	reNonSlug = regexp.MustCompile(`[^a-z0-9]+`)
	reSlugOK  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,40}$`)
)

func (c *cur) writeBrokerage(ctx context.Context) error {
	area := "Brokerage"
	open := func(src string) (*pkg.CSV, error) { return c.w.Master(c.f.Code, src, headers[src]) }
	if !c.s.has("NSQBRKG") && !c.s.has("NSDBRKG") {
		for _, src := range []string{srcBrokCards, srcBrokClient, srcBrokRates, srcTermChan} {
			cw, err := open(src)
			if err != nil {
				return err
			}
			if err := cw.Close(); err != nil {
				return err
			}
		}
		c.rep.infof(area, "", "no-tables", "This database has no brokerage tables (NSQBRKG, NSDBRKG); no brokerage is migrated")
		return nil
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
		for _, k := range []string{"SUBCD", "EffectDate", "ITEMCD", "UNDITEMCD", "CO", "PRODUCTCD", "ATWHAT", cols["pct"], cols["min"], cols["minper"], cols["of"], cols["flat"], cols["flatper"], cols["slab"], cols["extra"], cols["event"]} {
			if k != "" {
				names = append(names, k)
			}
		}
		list, _ := c.s.sel(table, "", names...)
		return c.d.query(ctx, fmt.Sprintf(q, list), func(r migrate.Row) error {
			lc := func(k string) string { return strings.ToLower(cols[k]) }
			x := rateRow{kind: kind, co: str(r, "co"), ucc: upper(r, "subcd"), from: date(r, "effectdate"), item: upper(r, "itemcd"), und: upper(r, "unditemcd"),
				prod: upper(r, "productcd"), atwhat: strings.ToUpper(strings.Join(strings.Fields(str(r, "atwhat")+" "+str(r, lc("extra"))+" "+str(r, lc("event"))), " ")),
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
	if err := read(qBrokD, "NSDBRKG", "D", map[string]string{"pct": "DLBRPTGE", "min": "DLBRKGMIN", "minper": "DLBRMNPER", "flat": "DLBRKGFLT", "flatper": "DLFLATPER", "slab": "DLSLABCD", "extra": "ONWHICH", "event": "ONCAT"}); err != nil {
		return err
	}
	others := map[string][]othRow{}
	baskets := map[string]bool{} // basket codes clients point at
	if c.s.has("OTHBRKG") {
		list, _ := c.s.sel("OTHBRKG", "", "SUBCD", "CO", "EffectDate", "ORD_MINBRK", "CNT_MINBRK", "MINLOTBRKG", "DEFA_BRKG", "brkgbasket")
		if err := c.d.query(ctx, fmt.Sprintf(qBrokOth, list), func(r migrate.Row) error {
			u := upper(r, "subcd")
			o := othRow{co: str(r, "co"), from: firstNonEmpty(date(r, "effectdate"), "2000-01-01"), minOrd: dec(r, "ord_minbrk"), minNote: dec(r, "cnt_minbrk"),
				minLot: dec(r, "minlotbrkg"), template: upper(r, "defa_brkg"), basket: upper(r, "brkgbasket")}
			others[u] = append(others[u], o)
			if o.basket != "" {
				baskets[o.basket] = true
			}
			return nil
		}); err != nil {
			return err
		}
	}

	// The cards: every basket a client points at (clients of any status:
	// a reactivated client comes back on their basket).
	cw, err := open(srcBrokCards)
	if err != nil {
		return err
	}
	cardOf := map[string]string{} // basket -> the Card value written
	slugs := map[string]bool{}
	used := map[string]bool{} // channels the converted lines price apart
	noRates := 0
	for _, b := range sortedKeys(baskets) {
		if _, ok := c.cm.clients[b]; ok {
			c.rep.warnf(area, "", "basket-is-client", "Check the basket in SharePro", "Basket %s is also a client code; its rows are taken as the client's own rates, not as a card", b)
			continue
		}
		lines := c.convertRows(ctx, area, "", rows[b], items)
		if len(lines) == 0 {
			noRates++
			continue
		}
		card := b
		for i := 2; slugs[cardSlug(card)]; i++ {
			card = fmt.Sprintf("%s~%d", b, i)
		}
		slugs[cardSlug(card)] = true
		cardOf[b] = card
		for _, v := range c.rateVersions(lines, others[b]) {
			for _, row := range v.rows {
				row["Card"], row["Name"] = card, b
				used[row["Channel"]] = row["Channel"] != ""
				if err := cw.Write(rec(headers[srcBrokCards], row)); err != nil {
					cw.Close()
					return err
				}
			}
		}
	}
	if err := cw.Close(); err != nil {
		return err
	}
	if noRates > 0 {
		c.rep.infof(area, "", "basket-no-rates", "%d brokerage baskets that clients point at have no rates in this firm's segments; they are not made into cards, and their clients pay the firm default card there", noRates)
	}
	c.rep.infof(area, "", "cards", "%d brokerage baskets made into rate cards (OTHBRKG.brkgbasket, with their own rate rows)", len(cardOf))

	// Which basket each client is on, per exchange segment.
	cw, err = open(srcBrokClient)
	if err != nil {
		return err
	}
	onCard, uncarded := map[string]bool{}, 0
	for _, u := range c.clientList() {
		if !c.written[u] {
			continue
		}
		os := append([]othRow(nil), others[u]...)
		sort.SliceStable(os, func(i, j int) bool { return os[i].from < os[j].from })
		last := map[string]string{} // co -> card in force
		for _, o := range os {
			seg := c.f.segOf(o.co)
			if seg == "" || !contains(c.f.COs, o.co) {
				continue
			}
			card := cardOf[o.basket]
			if o.basket != "" && card == "" {
				uncarded++
			}
			if card == last[o.co] {
				continue
			}
			last[o.co] = card
			if card != "" {
				onCard[u] = true
			}
			if err := cw.Write(rec(headers[srcBrokClient], map[string]string{"UCC": u, "Card": card, "From": o.from, "Segment": seg, "Exchange": c.f.exchOf(o.co)})); err != nil {
				cw.Close()
				return err
			}
		}
	}
	if err := cw.Close(); err != nil {
		return err
	}
	if uncarded > 0 {
		c.rep.infof(area, "", "basket-uncarded", "%d basket assignments name a basket with no rates in the segment; those clients pay the firm default card there", uncarded)
	}

	// Clients' own rates.
	cw, err = open(srcBrokRates)
	if err != nil {
		return err
	}
	clients, neither, neitherTraded := 0, 0, 0
	for _, u := range c.clientList() {
		if !c.written[u] {
			continue
		}
		rs := rows[u]
		os := others[u]
		// a client whose rates are another client's (OTHBRKG.DEFA_BRKG)
		if len(rs) == 0 {
			for _, o := range os {
				if o.template != "" && o.template != u && len(rows[o.template]) > 0 && !baskets[o.template] {
					for _, x := range rows[o.template] {
						x.ucc = u
						rs = append(rs, x)
					}
					c.rep.infof(area, u, "template", "Rates copied from client %s, whose rates this client follows in SharePro (OTHBRKG.DEFA_BRKG)", o.template)
					break
				}
			}
		}
		lines := c.convertRows(ctx, area, u, rs, items)
		if len(lines) == 0 {
			if !onCard[u] {
				neither++
				if c.cm.lastTrade[u] >= day(fyStart(c.k.FY)) {
					neitherTraded++
				}
			}
			continue
		}
		clients++
		for _, v := range c.rateVersions(lines, os) {
			for _, row := range v.rows {
				row["UCC"] = u
				used[row["Channel"]] = row["Channel"] != ""
				if err := cw.Write(rec(headers[srcBrokRates], row)); err != nil {
					cw.Close()
					return err
				}
			}
		}
	}
	if err := cw.Close(); err != nil {
		return err
	}
	c.rep.infof(area, "", "summary", "%d clients have their own brokerage rates from SharePro, which win over their card line by line", clients)
	if neither > 0 {
		c.rep.ask(pkg.FillBrokerageDefault, fmt.Sprintf("%d clients (%d of them traded this year) are on no brokerage basket and have no rates of their own in SharePro. They pay the firm default card, which SharePro does not record: set it on Brokerage before the first bills.", neither, neitherTraded))
	}
	delete(used, "")
	delete(used, "dealer") // the default channel needs no terminals
	for k, v := range used {
		if !v {
			delete(used, k)
		}
	}
	terms, err := c.writeTerminalChannels(ctx, used)
	if err != nil {
		return err
	}
	if len(used) > 0 && terms == 0 {
		c.rep.ask(pkg.FillTerminalChannels, "SharePro's rates differ by trading channel (dealer, internet, mobile), but its trades do not show which terminal (CTCL ID) is which channel. Until the list is filled in, every trade is billed at the dealer rate.")
	}
	return nil
}

// writeTerminalChannels lists the terminals whose trades SharePro gave a
// channel the cards price apart (e.g. internet, mobile): SharePro has no
// terminal table, but each trade carries its product code
// (TRFILE.PRODUCTCD, read as for the rates) and CTCL ID. A terminal is
// listed when at least termShare of its trades carry one such channel;
// every other terminal is the default channel (dealer). The current
// year's trades are the sample.
func (c *cur) writeTerminalChannels(ctx context.Context, used map[string]bool) (int, error) {
	cw, err := c.w.Master(c.f.Code, srcTermChan, headers[srcTermChan])
	if err != nil {
		return 0, err
	}
	ts := map[string]map[string]int{} // terminal -> channel -> trades ("" = total)
	if len(used) > 0 && c.s.has("TRFILE") && c.s.hasCol("TRFILE", "CTCLID") && c.s.hasCol("TRFILE", "PRODUCTCD") {
		if err := c.d.query(ctx, fmt.Sprintf(qTermCh, inList(c.f.COs)), func(r migrate.Row) error {
			id := strings.TrimSpace(str(r, "ctcl"))
			if id == "" {
				return nil
			}
			if ts[id] == nil {
				ts[id] = map[string]int{}
			}
			n, _ := strconv.Atoi(num(dec(r, "n")))
			ts[id][""] += n
			if ch := c.product(ctx, upper(r, "productcd")).channel; used[ch] {
				ts[id][ch] += n
			}
			return nil
		}); err != nil {
			cw.Close()
			return 0, err
		}
	}
	listed, mixed := 0, 0
	for _, id := range sortedKeys(ts) {
		t, best, most, some := ts[id], "", 0, 0
		for _, ch := range sortedKeys(t) {
			if ch != "" {
				some += t[ch]
				if t[ch] > most {
					best, most = ch, t[ch]
				}
			}
		}
		switch {
		case some == 0:
			continue
		case float64(most) < termShare*float64(t[""]):
			mixed++
			continue
		}
		if err := cw.Write(rec(headers[srcTermChan], map[string]string{"Terminal": id, "Channel": best})); err != nil {
			cw.Close()
			return 0, err
		}
		listed++
	}
	if err := cw.Close(); err != nil {
		return 0, err
	}
	if listed > 0 {
		c.rep.infof("Brokerage", "", "terminals", "%d terminals (CTCL IDs) listed by the channel SharePro gave their trades this year (%s); other terminals bill as dealer", listed, strings.Join(sortedKeys(used), ", "))
	}
	if mixed > 0 {
		c.rep.infof("Brokerage", "", "terminals-mixed", "%d terminals carried %s trades but mostly others; they bill as dealer. Check them on Brokerage plans → Trading channels", mixed, strings.Join(sortedKeys(used), " or "))
	}
	return listed, nil
}

// convertRate turns one SharePro rate row into Ledgerline rate lines.
func (c *cur) convertRate(ctx context.Context, x rateRow, items map[string][2]string) ([]rateLine, error) {
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
		// delivery rows of derivatives: settlement at expiry; the event is
		// in ONCAT (Expiry, Excercise, Assignm, Delivery)
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
		// futures and options settled by delivery of shares (P-F, P-O)
		switch c.product(ctx, x.prod).physical {
		case "FUT":
			products = []string{fut}
		case "OPT":
			products = []string{opt}
		}
		if c.product(ctx, x.prod).physical != "" && kase != charges.CaseExercise && kase != charges.CaseAssignment {
			kase = charges.CasePhysical
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
	channel := c.product(ctx, x.prod).channel
	rule := charges.BrokerageRule{}
	v := map[string]string{"Exchange": c.f.exchOf(x.co), "Channel": channel, "_co": x.co}
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
		v["Note"] = "SharePro channel " + x.prod
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
		key, err := charges.NewTargetKey(p, v["Exchange"], channel, k, target)
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
				if c.plan.ClientInterest == "firm" {
					// the firm chose to move everyone to its own rate
					c.rep.infof("Client interest", upper(r, "subcd"), "own-rate", "SharePro's own interest rate of %s%% from %s is not migrated: every client moves to the firm's rate, as chosen for the migration", p.String(), t.from)
					t.rate = ""
				}
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
	seen, hasCKYC := map[string]bool{}, map[string]bool{}
	ckyc := func(pan, no string) {
		if n := reDigits.ReplaceAllString(no, ""); len(n) == 14 && n == strings.TrimSpace(no) && !hasCKYC[pan] {
			ckycs = append(ckycs, ck{pan, n})
			hasCKYC[pan] = true
		}
	}
	for _, u := range c.clientList() {
		if !c.written[u] {
			continue
		}
		cl := c.cm.clients[u]
		pan := cl.pan
		k, ok := c.cm.kycByPAN[pan]
		var det dpDetail
		if cl.dpOnly {
			det = c.cm.dpDet[cl.dp.co+"|"+cl.dp.ben]
		}
		if cl.dpOnly && !seen[pan] && rePAN.MatchString(pan) {
			// a DP-only client: the KYC register (by PAN) if it has the PAN,
			// else the KRA status and CKYC number the DP recorded (CLLIST_DET)
			if !ok {
				seen[pan] = true
				if st := dpKRAStatus(det.kraDesc); st != "" {
					if err := cw.Write(rec(h, map[string]string{"PAN": pan, "Status": st, "Remarks": "SharePro DP account details (CLLIST_DET): " + strings.ToLower(det.kraDesc)})); err != nil {
						cw.Close()
						return err
					}
				} else if det.kraDesc != "" {
					c.rep.infof("KRA status", u, "kra-status", "KRA status %q of the DP account details is not one Ledgerline knows; not migrated (Ledgerline asks the KRA)", det.kraDesc)
				}
				ckyc(pan, det.ckyc)
				continue
			}
		}
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
		ckyc(pan, k.kycno)
		ckyc(pan, det.ckyc) // a DP-only client's, when the register has none
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
