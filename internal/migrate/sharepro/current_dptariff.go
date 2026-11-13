package sharepro

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/migrate"
	"ledgerline/internal/migrate/pkg"
	"ledgerline/internal/money"
)

// DP tariffs: SharePro's tariff baskets (DPCLBSKT) with their dated rates
// (DPCHARGE, the basket code in BENACNUM), each account's basket history
// (DPCLBASKET) and the date its annual maintenance is paid up to (DPAMCDB)
// become DP-TARIFFS and DP-TARIFF-ACCOUNTS. See MAPPING.md.
const (
	qDPBaskets  = `/*sp:dp-baskets*/ SELECT %s FROM DPCLBSKT WHERE CO IN (%s)`
	qDPCharges  = `/*sp:dp-charges*/ SELECT %s FROM DPCHARGE WHERE CO IN (%s)`
	qDPBskAccts = `/*sp:dp-basket-accounts*/ SELECT %s FROM DPCLBASKET WHERE CO IN (%s)`
	qDPAMCPaid  = `/*sp:dp-amc-paid*/ SELECT RTRIM(BENACNUM) AS benacnum, MAX(AMDATETO) AS upto FROM DPAMCDB WHERE CO IN (%s) AND AMTDR > 0 GROUP BY RTRIM(BENACNUM)`
	// the checks: tariff versions, accounts on a basket at the as-of date,
	// accounts with maintenance paid beyond it
	qCheckDPTariffVersions = `/*sp:check-dp-tariff-versions*/ SELECT COUNT(*) AS n FROM (SELECT DISTINCT RTRIM(c.BENACNUM) AS b, c.EFFECTDATE AS e FROM DPCHARGE c
		WHERE c.CO IN (%[1]s) AND c.EFFECTDATE IS NOT NULL AND RTRIM(c.TRANCODE) <> ''
			AND EXISTS (SELECT 1 FROM DPCLBSKT k WHERE k.CO IN (%[1]s) AND RTRIM(k.BASKETCD) = RTRIM(c.BENACNUM))) v`
	qCheckDPTariffAccounts = `/*sp:check-dp-tariff-accounts*/ SELECT COUNT(*) AS n FROM CLLIST l WHERE l.CO IN (%[1]s)%[2]s
		AND EXISTS (SELECT 1 FROM DPCLBASKET a WHERE a.CO IN (%[1]s) AND RTRIM(a.BENACNUM) = RTRIM(l.BENACNUM) AND a.EffectDate <= '%[3]s'
			AND EXISTS (SELECT 1 FROM DPCLBSKT k WHERE k.CO IN (%[1]s) AND RTRIM(k.BASKETCD) = RTRIM(a.BASKETCD))
			AND EXISTS (SELECT 1 FROM DPCHARGE ch WHERE ch.CO IN (%[1]s) AND RTRIM(ch.BENACNUM) = RTRIM(a.BASKETCD) AND ch.EFFECTDATE IS NOT NULL))`
	qCheckDPAMCAhead = `/*sp:check-dp-amc-ahead*/ SELECT COUNT(*) AS n FROM CLLIST l WHERE l.CO IN (%[1]s)%[2]s
		AND EXISTS (SELECT 1 FROM DPCLBASKET a WHERE a.CO IN (%[1]s) AND RTRIM(a.BENACNUM) = RTRIM(l.BENACNUM) AND a.EffectDate IS NOT NULL
			AND EXISTS (SELECT 1 FROM DPCLBSKT k WHERE k.CO IN (%[1]s) AND RTRIM(k.BASKETCD) = RTRIM(a.BASKETCD))
			AND EXISTS (SELECT 1 FROM DPCHARGE ch WHERE ch.CO IN (%[1]s) AND RTRIM(ch.BENACNUM) = RTRIM(a.BASKETCD) AND ch.EFFECTDATE IS NOT NULL))
		AND (SELECT MAX(d.AMDATETO) FROM DPAMCDB d WHERE d.CO IN (%[1]s) AND RTRIM(d.BENACNUM) = RTRIM(l.BENACNUM) AND d.AMTDR > 0) > '%[3]s'`
)

// dpRateCodes are SharePro's transaction codes whose RATE is a charge per
// certificate (demat, remat); on every other code RATE is a percentage of
// the value [verified: 2201 RATE 3 = ₹3 per certificate, 2230 RATE 0.1 =
// 0.1% with a ₹25 minimum, in Baljit's tariff note].
var dpRateCodes = map[string]bool{"2201": true, "2205": true, "901": true, "902": true}

// nsdlCodes are NSDL's transaction codes in DPTRTYPE, as the CDSL codes
// Ledgerline's plan lines use.
var nsdlCodes = map[string]string{"1908": "MPGD", "1911": "MPUS", "2908": "MPGD", "2911": "MPUS", "901": "2201", "902": "2205", "908": "2230",
	"911": "2280", "IDEL": "ODTR", "IREC": "ORTR", "ODEL": "ODTA", "OREC": "ORTA", "PIN": "DCM", "POUT": "RCCH", "INST": "SETL"}

func (c *cur) writeDPTariffs(ctx context.Context) error {
	area := "DP tariffs"
	dpc := c.f.dpCOs()
	if len(dpc) == 0 || !c.s.has("DPCLBSKT") || !c.s.has("DPCHARGE") {
		return nil
	}
	cos := inList(dpc)
	type basket struct{ code, name, typ, period string }
	baskets := map[string]basket{}
	bl, _ := c.s.sel("DPCLBSKT", "", "BASKETCD", "DESCR", "TYPE", "PERIOD")
	if err := c.d.query(ctx, fmt.Sprintf(qDPBaskets, bl, cos), func(r migrate.Row) error {
		b := basket{code: upper(r, "basketcd"), name: str(r, "descr"), typ: upper(r, "type"), period: strings.ToLower(str(r, "period"))}
		if b.code != "" {
			baskets[b.code] = b
		}
		return nil
	}); err != nil {
		return err
	}
	// the rates of each basket version; rows keyed by an account (an
	// account-specific tariff) are counted, not migrated
	cl, _ := c.s.sel("DPCHARGE", "", "BENACNUM", "EFFECTDATE", "TRANCODE", "RATE", "MIN", "MAX", "FLAT", "MAILCHG",
		"SPRATE", "SPMIN", "SPMAX", "SPFLAT", "CMRATE", "CMMIN", "CMMAX", "CMFLAT")
	type line struct {
		plan, from, code string
		v                map[string]string
	}
	var lines []line
	accountTariffs := map[string]bool{}
	dupes := 0
	seen := map[string]bool{}
	if err := c.d.query(ctx, fmt.Sprintf(qDPCharges, cl, cos), func(r migrate.Row) error {
		plan, code := upper(r, "benacnum"), upper(r, "trancode")
		if _, ok := baskets[plan]; !ok {
			accountTariffs[plan] = true
			return nil
		}
		from := sinceDate(r, "effectdate")
		if mapped, ok := nsdlCodes[code]; ok {
			code = mapped
		}
		if from == "" || code == "" {
			return nil
		}
		if k := plan + "|" + from + "|" + code; seen[k] {
			dupes++ // NSDL and CDSL codes of the same charge: the first stays
			return nil
		} else {
			seen[k] = true
		}
		rate := func(pre string) (pct, perUnit string) {
			v := dec(r, pre+"rate")
			if dpRateCodes[code] {
				return "", rupees(v)
			}
			return decStr(v), ""
		}
		pct, unit := rate("")
		spPct, spUnit := rate("sp")
		cmPct, cmUnit := rate("cm")
		lines = append(lines, line{plan, from, code, map[string]string{
			"Flat": rupees(dec(r, "flat")), "Pct": pct, "PerUnit": unit, "Min": rupees(dec(r, "min")), "Max": rupees(dec(r, "max")), "Mail": rupees(dec(r, "mailchg")),
			"OwnFlat": rupees(dec(r, "cmflat")), "OwnPct": cmPct, "OwnPerUnit": cmUnit, "OwnMin": rupees(dec(r, "cmmin")), "OwnMax": rupees(dec(r, "cmmax")),
			"SpecialFlat": rupees(dec(r, "spflat")), "SpecialPct": spPct, "SpecialPerUnit": spUnit, "SpecialMin": rupees(dec(r, "spmin")), "SpecialMax": rupees(dec(r, "spmax")),
		}})
		return nil
	}); err != nil {
		return err
	}
	sort.SliceStable(lines, func(i, j int) bool {
		a, b := lines[i], lines[j]
		if a.plan != b.plan {
			return a.plan < b.plan
		}
		if a.from != b.from {
			return a.from < b.from
		}
		return a.code < b.code
	})
	h := headers[srcDPTariffs]
	cw, err := c.w.Master(c.f.Code, srcDPTariffs, h)
	if err != nil {
		return err
	}
	written, special := map[string]bool{}, 0
	versions := map[string]bool{}
	for _, l := range lines {
		b := baskets[l.plan]
		v := l.v
		v["Plan"], v["From"], v["Code"], v["Name"] = l.plan, l.from, l.code, b.name
		v["Pool"] = yn(b.typ == "CM")
		v["AMCBilling"] = map[bool]string{true: "Yearly", false: "Monthly"}[b.period == "yearly"]
		if v["SpecialFlat"] != v["Flat"] || v["SpecialPct"] != v["Pct"] || v["SpecialPerUnit"] != v["PerUnit"] || v["SpecialMin"] != v["Min"] || v["SpecialMax"] != v["Max"] {
			special++
		}
		if err := cw.Write(rec(h, v)); err != nil {
			cw.Close()
			return err
		}
		written[l.plan] = true
		versions[l.plan+"|"+l.from] = true
	}
	if err := cw.Close(); err != nil {
		return err
	}
	for code := range baskets {
		if !written[code] {
			c.rep.warnf(area, code, "empty-basket", "Set the plan's rates in Ledgerline if accounts are to use it", "Tariff basket %s has no rates in DPCHARGE; it is not migrated", code)
		}
	}
	if len(accountTariffs) > 0 {
		c.rep.infof(area, "", "account-tariffs", "%d tariffs in DPCHARGE are keyed by an account, not a basket (account-specific rates); they are not migrated: the accounts are not in the DP master or pay their basket's rates", len(accountTariffs))
	}
	if special > 0 {
		c.rep.warnf(area, "", "sp-rates", "Ask the office what SharePro's SP rates are for; Ledgerline bills the normal and the CM (own trades) rates",
			"%d charges have a different SP rate in SharePro (DPCHARGE.SP*); it is kept on the plan (Special*) for the record and not billed", special)
	}
	if dupes > 0 {
		c.rep.infof(area, "", "nsdl-codes", "%d NSDL charge codes are the same charge as a CDSL code of the version; the first is kept", dupes)
	}
	c.rep.infof(area, "", "plans", "%d tariff plans (SharePro baskets) with %d dated versions; AMC (AM) and corporate AMC (0015) are each version's maintenance, billed a year in advance on yearly baskets", len(written), len(versions))

	// Each account's basket history and the date its AMC is paid up to.
	paid := map[string]string{}
	if c.s.has("DPAMCDB") {
		if err := c.d.query(ctx, fmt.Sprintf(qDPAMCPaid, cos), func(r migrate.Row) error {
			if u := date(r, "upto"); u != "" {
				paid[upper(r, "benacnum")] = u
			}
			return nil
		}); err != nil {
			return err
		}
	}
	byBen := map[string]*dpAcct{}
	for i := range c.cm.dp {
		a := &c.cm.dp[i]
		if contains(dpc, a.co) && c.dpWritten[a.boid] {
			byBen[a.ben] = a
		}
	}
	h2 := headers[srcDPPlanAcct]
	cw2, err := c.w.Master(c.f.Code, srcDPPlanAcct, h2)
	if err != nil {
		return err
	}
	type asg struct{ ben, from, plan string }
	var as []asg
	unknown, empty, notDP, undated := map[string]int{}, map[string]int{}, 0, 0
	if c.s.has("DPCLBASKET") {
		al, _ := c.s.sel("DPCLBASKET", "", "BENACNUM", "EffectDate", "BASKETCD")
		if err := c.d.query(ctx, fmt.Sprintf(qDPBskAccts, al, cos), func(r migrate.Row) error {
			x := asg{upper(r, "benacnum"), sinceDate(r, "effectdate"), upper(r, "basketcd")}
			switch {
			case byBen[x.ben] == nil:
				notDP++
			case baskets[x.plan].code != "" && !written[x.plan]:
				empty[x.plan]++
			case !written[x.plan]:
				unknown[firstNonEmpty(x.plan, "(blank)")]++
			case x.from == "":
				undated++
			default:
				as = append(as, x)
			}
			return nil
		}); err != nil {
			cw2.Close()
			return err
		}
	}
	sort.Slice(as, func(i, j int) bool {
		if as[i].ben != as[j].ben {
			return byBen[as[i].ben].boid < byBen[as[j].ben].boid
		}
		return as[i].from < as[j].from
	})
	onPlan := map[string]bool{}
	for _, x := range as {
		a := byBen[x.ben]
		v := map[string]string{"BOID": a.boid, "Plan": x.plan, "From": x.from}
		if !onPlan[x.ben] {
			v["AMCPaidUpto"] = paid[x.ben]
		}
		onPlan[x.ben] = true
		if err := cw2.Write(rec(h2, v)); err != nil {
			cw2.Close()
			return err
		}
	}
	if err := cw2.Close(); err != nil {
		return err
	}
	for _, code := range sortedKeys(empty) {
		c.rep.warnf(area, code, "empty-basket-accounts", "Put the accounts on a plan in Ledgerline (DP desk, Tariff → change)", "%d basket assignments in DPCLBASKET name basket %s, which has no rates; those rows are left out", empty[code], code)
	}
	if undated > 0 {
		c.rep.warnf(area, "", "undated-basket", "Put the accounts on a plan in Ledgerline (DP desk, Tariff → change)", "%d basket assignments in DPCLBASKET have no effective date; they are left out", undated)
	}
	for _, code := range sortedKeys(unknown) {
		c.rep.warnf(area, code, "unknown-basket", "Put the accounts on a plan in Ledgerline (DP desk, Tariff → change)", "%d basket assignments in DPCLBASKET name basket %q, which is not in the basket master (DPCLBSKT); those rows are left out", unknown[code], code)
	}
	if n := len(byBen) - len(onPlan); n > 0 {
		c.rep.infof(area, "", "no-basket", "%d DP accounts have no basket in SharePro (DPCLBASKET); they pay the firm tariff in Ledgerline until put on a plan", n)
	}
	if notDP > 0 {
		c.rep.infof(area, "", "not-dp", "%d basket assignments are for accounts not migrated as DP accounts; they are left out", notDP)
	}
	// The checks Ledgerline verifies after the load.
	r, err := c.d.one(ctx, fmt.Sprintf(qCheckDPTariffVersions, cos))
	if err != nil {
		return err
	}
	c.rep.check(pkg.CheckDPTariffVersions, fmt.Sprint(i64(r, "n")), "DP tariff versions in SharePro (basket and effective date in DPCHARGE)")
	asOf := day(c.asOf)
	if r, err = c.d.one(ctx, fmt.Sprintf(qCheckDPTariffAccounts, cos, c.dpExcl("l"), asOf)); err != nil {
		return err
	}
	c.rep.check(pkg.CheckDPTariffAccounts, fmt.Sprint(i64(r, "n")), "DP accounts on a tariff basket on "+asOf+" (DPCLBASKET)")
	if c.s.has("DPAMCDB") {
		if r, err = c.d.one(ctx, fmt.Sprintf(qCheckDPAMCAhead, cos, c.dpExcl("l"), asOf)); err != nil {
			return err
		}
		c.rep.check(pkg.CheckDPAMCPaidAhead, fmt.Sprint(i64(r, "n")), "DP accounts with annual maintenance paid beyond "+asOf+" (DPAMCDB)")
	}
	return nil
}

// decStr is a decimal as written, "" when zero.
// fromStart is the From of a tariff version or basket assignment SharePro
// dates 1900-01-01: in force from the start.
const fromStart = "1900-01-01"

// sinceDate reads an effective date where SharePro's "no date" (1900-01-01)
// means from the start; only a NULL is no date.
func sinceDate(r migrate.Row, col string) string {
	if s := date(r, col); s != "" {
		return s
	}
	switch v := r[col].(type) {
	case time.Time:
		return fromStart
	case string:
		if s := strings.TrimSpace(v); strings.HasPrefix(s, "1900-") || strings.HasPrefix(s, "1899-") {
			return fromStart
		}
	}
	return ""
}

func decStr(d money.Dec) string {
	if d.IsZero() {
		return ""
	}
	return d.String()
}
