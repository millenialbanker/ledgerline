package sharepro

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"ledgerline/internal/migrate"
)

// The chart of accounts.
//
// SharePro posts to a ledger head (ACCD; per company in MSFILE, with its
// group in MASTMSFILE.AgrCode and the ACCTGROUP tree) and, for heads with
// sub-ledgers, a sub-account (SUBCODE, MLFILE). MSFILE and MLFILE have one
// row per branch. Verified against a real database: a client's ledger is
// an MLFILE sub-account whose SUBCODE is the client code (DLMAST.SUBCD),
// under a "client control" head of each company (DLMAST.ACC_ACCD is blank).
// Several heads are client-coded (clients, NRI clients, DP clients, margin
// deposits, per-client STT and stamp duty...). In Ledgerline:
//
//	client-coded head whose name says CLIENT (and not MARGIN, CONTROL or
//	ADDBACK)                         CLIENT:<client code>, one account over the firm's companies
//	other client-coded head          <PREFIX>:<CO>-<ACCD>, one firm account for the head
//	any other head or sub-account    <PREFIX>:<CO>-<ACCD>[-<SUBCODE>]
//
// where PREFIX comes from the head's group: BANK, CASH, CC (exchange and
// clearing accounts), RECEIVABLE, ASSET, PAYABLE, LOAN, EQUITY, INCOME,
// EXPENSE, or SUSPENSE when the group cannot be told.

const (
	qGroups     = `/*sp:acctgroup*/ SELECT RTRIM(AgrCode) AS code, RTRIM(PAgrCode) AS parent, RTRIM(Descr) AS descr FROM ACCTGROUP`
	qHeads      = `/*sp:mastmsfile*/ SELECT %s FROM MASTMSFILE`
	qCOHeads    = `/*sp:msfile*/ SELECT %s FROM MSFILE`
	qSubs       = `/*sp:mlfile*/ SELECT RTRIM(co) AS co, RTRIM(ACCD) AS accd, RTRIM(SUBCODE) AS subcode, MAX(DESCR) AS descr FROM MLFILE GROUP BY co, ACCD, SUBCODE`
	qAcTypes    = `/*sp:msactypes*/ SELECT RTRIM(accttype) AS accttype, lExchange AS lexchange FROM MSACTYPES`
	qClientAcct = `/*sp:client-accounts*/ SELECT RTRIM(SUBCD) AS subcd FROM DLMAST`
)

type head struct {
	accd, descr, group, acType, bankCD, tt string
}

type chart struct {
	groups    map[string][2]string // code -> parent, descr
	heads     map[string]head      // ACCD -> head (MASTMSFILE, or MSFILE)
	coHead    map[string]string    // co|accd -> descr (MSFILE)
	subs      map[string]string    // co|accd|subcode -> descr (MLFILE)
	exchTypes map[string]bool
	isClient  map[string]bool   // client codes
	coded     map[string]bool   // co|accd: client-coded head
	merged    map[string]bool   // co|accd: client-coded head that is the client's ledger
	bookHead  map[string]string // co|TT -> bank or cash head of that book
	unknown   map[string]bool
}

// merges says whether a client-coded head is the client's own ledger.
func merges(descr string) bool {
	d := strings.ToUpper(descr)
	return strings.Contains(d, "CLIENT") && !strings.Contains(d, "MARGIN") && !strings.Contains(d, "CONTROL") && !strings.Contains(d, "ADDBACK")
}

func loadChart(ctx context.Context, d db, s *schema, rep *report) (*chart, error) {
	c := &chart{groups: map[string][2]string{}, heads: map[string]head{}, coHead: map[string]string{}, subs: map[string]string{},
		exchTypes: map[string]bool{}, isClient: map[string]bool{}, coded: map[string]bool{}, merged: map[string]bool{}, bookHead: map[string]string{}, unknown: map[string]bool{}}
	if s.has("ACCTGROUP") {
		if err := d.query(ctx, qGroups, func(r migrate.Row) error {
			c.groups[upper(r, "code")] = [2]string{upper(r, "parent"), upper(r, "descr")}
			return nil
		}); err != nil {
			return nil, err
		}
	} else {
		rep.infof("Trial balance", "", "no-groups", "This database has no ACCTGROUP table; account types are read from the ledger heads' names")
	}
	if s.has("MSACTYPES") {
		_ = d.query(ctx, qAcTypes, func(r migrate.Row) error {
			if flag(r, "lexchange") {
				c.exchTypes[upper(r, "accttype")] = true
			}
			return nil
		})
	}
	if s.has("MASTMSFILE") {
		list, _ := s.sel("MASTMSFILE", "", "ACCD", "DESCR", "AgrCode", "ACCTTYPE", "BANKCD", "TT")
		if err := d.query(ctx, fmt.Sprintf(qHeads, list), func(r migrate.Row) error {
			h := head{accd: upper(r, "accd"), descr: str(r, "descr"), group: upper(r, "agrcode"), acType: upper(r, "accttype"), bankCD: str(r, "bankcd"), tt: upper(r, "tt")}
			c.heads[h.accd] = h
			return nil
		}); err != nil {
			return nil, err
		}
	}
	if s.has("MSFILE") {
		list, _ := s.sel("MSFILE", "", "co", "ACCD", "DESCR", "TT")
		if err := d.query(ctx, fmt.Sprintf(qCOHeads, list), func(r migrate.Row) error {
			co, accd := str(r, "co"), upper(r, "accd")
			if c.coHead[co+"|"+accd] == "" {
				c.coHead[co+"|"+accd] = str(r, "descr")
			}
			h, ok := c.heads[accd]
			if !ok {
				h = head{accd: accd, descr: str(r, "descr"), tt: upper(r, "tt")}
				c.heads[accd] = h
			}
			if tt := firstNonEmpty(upper(r, "tt"), h.tt); tt != "" {
				if _, dup := c.bookHead[co+"|"+tt]; !dup {
					c.bookHead[co+"|"+tt] = accd
				}
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}
	if err := d.query(ctx, qClientAcct, func(r migrate.Row) error {
		c.isClient[upper(r, "subcd")] = true
		return nil
	}); err != nil {
		return nil, err
	}
	tot, hit := map[string]int{}, map[string]int{}
	if s.has("MLFILE") {
		if err := d.query(ctx, qSubs, func(r migrate.Row) error {
			k := str(r, "co") + "|" + upper(r, "accd")
			c.subs[k+"|"+upper(r, "subcode")] = str(r, "descr")
			tot[k]++
			if c.isClient[upper(r, "subcode")] {
				hit[k]++
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}
	// a head is client-coded when most of its sub-accounts are client codes
	for k, n := range tot {
		if hit[k] > 0 && hit[k]*2 > n {
			c.coded[k] = true
			if merges(c.headName(k)) {
				c.merged[k] = true
			}
		}
	}
	return c, nil
}

func (c *chart) headName(coAccd string) string {
	co, accd, _ := strings.Cut(coAccd, "|")
	return firstNonEmpty(c.coHead[co+"|"+accd], c.heads[accd].descr, accd)
}

// kindOf is the Ledgerline prefix of a ledger head.
func (c *chart) kindOf(accd string) string {
	h := c.heads[accd]
	if c.exchTypes[h.acType] {
		return "CC"
	}
	// walk the group tree, nearest first
	seen := map[string]bool{}
	for g := h.group; g != "" && !seen[g]; g = c.groups[g][0] {
		seen[g] = true
		if k := groupKind(g, c.groups[g][1], h); k != "" {
			return k
		}
	}
	if k := groupKind("", strings.ToUpper(h.descr), h); k != "" && k != "ASSET" && k != "PAYABLE" {
		return k
	}
	return "SUSPENSE"
}

func groupKind(code, descr string, h head) string {
	has := func(words ...string) bool {
		for _, w := range words {
			if strings.Contains(code, w) || strings.Contains(descr, w) {
				return true
			}
		}
		return false
	}
	switch {
	case has("CASHINHAND", "CASH AND BANK", "CASH & BANK", "BANK BAL"):
		if h.bankCD != "" || strings.Contains(strings.ToUpper(h.descr), "BANK") || strings.Contains(h.acType, "BANK") {
			return "BANK"
		}
		return "CASH"
	case has("EXCHANGE", "CLEARING"):
		return "CC"
	case has("CAPITAL", "RESERVE", "RESURVE", "SURPLUS", "SHARE HOLDER", "SHAREHOLDER", "PARTNER"):
		return "EQUITY"
	case has("LOAN", "BORROW") && !has("ADVANCE"):
		return "LOAN"
	case has("SDEBTOR", "SUNDRY DEBTOR", "DEBTORS", "RECEIVABLE"):
		return "RECEIVABLE"
	case has("FASSET", "FIXED ASSET", "INVESTMENT", "STOCK", "DEPOSIT", "ADVANCE", "MISCEXP", "MISCELLANEOUS EXP", "CURRENT ASSET", "CASSET", "PREPAID"):
		return "ASSET"
	case has("SCRED", "SUNDRY CREDITOR", "CREDITOR", "DUTIES", "TAXES", "PROVITION", "PROVISION", "CURRENT LIAB", "CLIB", "PAYABLE"):
		return "PAYABLE"
	case has("INCOME", "REVENUE"):
		return "INCOME"
	case has("EXPEN", "EXPENDITURE"):
		return "EXPENSE"
	case has("ASSET", "APPLICATION OF FUND"):
		return "ASSET"
	case has("LIABILIT", "SOURCES OF FUND"):
		return "PAYABLE"
	}
	return ""
}

// account maps a SharePro posting to a Ledgerline account. ucc is set when
// the account is the client's own ledger (CLIENT:<ucc>).
func (c *chart) account(co, accd, sub string) (code, name, ucc string) {
	accd, sub = strings.ToUpper(strings.TrimSpace(accd)), strings.ToUpper(strings.TrimSpace(sub))
	k := co + "|" + accd
	if sub != "" && c.isClient[sub] && c.coded[k] && c.merged[k] {
		return "CLIENT:" + codePart(sub), firstNonEmpty(c.subs[k+"|"+sub], sub), sub
	}
	kind := c.kindOf(accd)
	if kind == "SUSPENSE" {
		c.unknown[accd] = true
	}
	code = kind + ":" + codePart(co) + "-" + codePart(accd)
	name = c.headName(k)
	if sub != "" && !(c.coded[k] && c.isClient[sub]) {
		// a sub-account of its own; a client's amount under a client-coded
		// head that is not the client's ledger stays in the head's account
		code += "-" + codePart(sub)
		name = firstNonEmpty(c.subs[k+"|"+sub], name+" / "+sub)
	}
	return code, name, ""
}

// clientOf is the client a posting is for, whichever account it goes to.
func (c *chart) clientOf(co, accd, sub string) string {
	sub = strings.ToUpper(strings.TrimSpace(sub))
	if sub != "" && c.isClient[sub] && c.coded[co+"|"+strings.ToUpper(strings.TrimSpace(accd))] {
		return sub
	}
	return ""
}

// reportUnknown raises one warning per head whose type could not be told.
func (c *chart) reportUnknown(rep *report, area string) {
	for _, h := range sortedKeys(c.unknown) {
		rep.warnf(area, h, "unknown-head", "Check the account type after loading; move it to the right account in Ledgerline if needed",
			"Ledger head %s (%s) has no recognisable account group; it is migrated as a suspense account", h, c.heads[h].descr)
	}
	c.unknown = map[string]bool{}
}

// headsOf lists the client-coded heads of the given companies (co|accd),
// merged ones or the others.
func (c *chart) headsOf(cos []string, merged bool) []string {
	in := map[string]bool{}
	for _, co := range cos {
		in[co] = true
	}
	var out []string
	for k := range c.coded {
		co, _, _ := strings.Cut(k, "|")
		if in[co] && c.merged[k] == merged {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// accdsOf is the distinct ACCDs of co|accd keys.
func accdsOf(keys []string) []string {
	m := map[string]bool{}
	for _, k := range keys {
		_, a, _ := strings.Cut(k, "|")
		m[a] = true
	}
	return sortedKeys(m)
}
