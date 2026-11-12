package sharepro

import (
	"context"
	"fmt"
	"strings"

	"ledgerline/internal/migrate"
)

// The chart of accounts.
//
// SharePro posts to a ledger head (ACCD, per CO in MSFILE, with its group
// in MASTMSFILE.AgrCode and the ACCTGROUP tree) and, for heads with a sub
// ledger, a sub-account (SUBCODE, MLFILE). A client's trading ledger is the
// head DLMAST.ACC_ACCD with the sub-account DLMAST.ACC_SUBCOD (or the
// client code). In Ledgerline:
//
//	client trading ledger          CLIENT:<client code>  (one account across the firm's COs)
//	any other head or sub-account  <PREFIX>:<CO>-<ACCD>[-<SUBCODE>]
//
// where PREFIX comes from the head's group: BANK, CASH, CC (exchange and
// clearing accounts), RECEIVABLE, ASSET, PAYABLE, LOAN, EQUITY, INCOME,
// EXPENSE, or SUSPENSE when the group cannot be told.

const (
	qGroups     = `/*sp:acctgroup*/ SELECT RTRIM(AgrCode) AS code, RTRIM(PAgrCode) AS parent, RTRIM(Descr) AS descr FROM ACCTGROUP`
	qHeads      = `/*sp:mastmsfile*/ SELECT %s FROM MASTMSFILE`
	qCOHeads    = `/*sp:msfile*/ SELECT %s FROM MSFILE`
	qSubs       = `/*sp:mlfile*/ SELECT %s FROM MLFILE`
	qAcTypes    = `/*sp:msactypes*/ SELECT RTRIM(accttype) AS accttype, lExchange AS lexchange FROM MSACTYPES`
	qClientAcct = `/*sp:client-accounts*/ SELECT %s FROM DLMAST`
)

type head struct {
	accd, descr, group, acType, bankCD, tt string
	sub                                    bool
}

type chart struct {
	groups    map[string][2]string // code -> parent, descr
	heads     map[string]head      // ACCD -> head (MASTMSFILE, or MSFILE)
	coHead    map[string]string    // co|accd -> descr (MSFILE)
	subs      map[string]string    // co|accd|subcode -> descr (MLFILE)
	exchTypes map[string]bool
	// client ledgers: head|subcode -> client code
	clientSub   map[string]string
	clientHeads map[string]bool
	bookHead    map[string]string // co|TT -> bank or cash head of that book
	unknown     map[string]bool
}

func loadChart(ctx context.Context, d db, s *schema, rep *report) (*chart, error) {
	c := &chart{groups: map[string][2]string{}, heads: map[string]head{}, coHead: map[string]string{}, subs: map[string]string{},
		exchTypes: map[string]bool{}, clientSub: map[string]string{}, clientHeads: map[string]bool{}, bookHead: map[string]string{}, unknown: map[string]bool{}}
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
		list, _ := s.sel("MASTMSFILE", "", "ACCD", "DESCR", "AgrCode", "ACCTTYPE", "BANKCD", "TT", "SUBLEDGER")
		if err := d.query(ctx, fmt.Sprintf(qHeads, list), func(r migrate.Row) error {
			h := head{accd: upper(r, "accd"), descr: str(r, "descr"), group: upper(r, "agrcode"), acType: upper(r, "accttype"), bankCD: str(r, "bankcd"), tt: upper(r, "tt"), sub: flag(r, "subledger")}
			c.heads[h.accd] = h
			return nil
		}); err != nil {
			return nil, err
		}
	}
	if s.has("MSFILE") {
		list, _ := s.sel("MSFILE", "", "co", "ACCD", "DESCR", "TT", "SUBLEDGER")
		if err := d.query(ctx, fmt.Sprintf(qCOHeads, list), func(r migrate.Row) error {
			co, accd := str(r, "co"), upper(r, "accd")
			c.coHead[co+"|"+accd] = str(r, "descr")
			h, ok := c.heads[accd]
			if !ok {
				h = head{accd: accd, descr: str(r, "descr"), tt: upper(r, "tt"), sub: flag(r, "subledger")}
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
	if s.has("MLFILE") {
		list, _ := s.sel("MLFILE", "", "co", "ACCD", "SUBCODE", "DESCR")
		if err := d.query(ctx, fmt.Sprintf(qSubs, list), func(r migrate.Row) error {
			c.subs[str(r, "co")+"|"+upper(r, "accd")+"|"+upper(r, "subcode")] = str(r, "descr")
			return nil
		}); err != nil {
			return nil, err
		}
	}
	// client ledgers
	list, _ := s.sel("DLMAST", "", "SUBCD", "ACC_ACCD", "ACC_SUBCOD")
	type ca struct{ ucc, accd, sub string }
	var cas []ca
	if err := d.query(ctx, fmt.Sprintf(qClientAcct, list), func(r migrate.Row) error {
		cas = append(cas, ca{upper(r, "subcd"), upper(r, "acc_accd"), upper(r, "acc_subcod")})
		return nil
	}); err != nil {
		return nil, err
	}
	for _, x := range cas {
		if x.accd != "" {
			c.clientHeads[x.accd] = true
		}
	}
	if len(c.clientHeads) == 0 {
		// No client head in the master: the heads most of whose
		// sub-accounts are client codes.
		isClient := map[string]bool{}
		for _, x := range cas {
			isClient[x.ucc] = true
		}
		tot, hit := map[string]int{}, map[string]int{}
		for k := range c.subs {
			p := strings.SplitN(k, "|", 3)
			tot[p[1]]++
			if isClient[p[2]] {
				hit[p[1]]++
			}
		}
		for h, n := range tot {
			if hit[h]*2 > n {
				c.clientHeads[h] = true
			}
		}
		if len(c.clientHeads) > 0 {
			rep.infof("Trial balance", "", "client-heads", "The client master names no ledger head for clients; the heads whose sub-accounts are mostly client codes are taken as client ledgers: %s", strings.Join(sortedKeys(c.clientHeads), ", "))
		}
	}
	for _, x := range cas {
		sub := firstNonEmpty(x.sub, x.ucc)
		if x.accd != "" {
			c.clientSub[x.accd+"|"+sub] = x.ucc
		} else {
			for h := range c.clientHeads {
				c.clientSub[h+"|"+sub] = x.ucc
			}
		}
	}
	return c, nil
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

// account maps a SharePro posting to a Ledgerline account. ucc is set for
// a client's trading ledger.
func (c *chart) account(co, accd, sub string) (code, name, ucc string) {
	accd, sub = strings.ToUpper(strings.TrimSpace(accd)), strings.ToUpper(strings.TrimSpace(sub))
	if c.clientHeads[accd] && sub != "" {
		if u, ok := c.clientSub[accd+"|"+sub]; ok {
			return "CLIENT:" + codePart(u), firstNonEmpty(c.subs[co+"|"+accd+"|"+sub], u), u
		}
	}
	k := c.kindOf(accd)
	if k == "SUSPENSE" {
		c.unknown[accd] = true
	}
	code = k + ":" + codePart(co) + "-" + codePart(accd)
	name = firstNonEmpty(c.coHead[co+"|"+accd], c.heads[accd].descr, accd)
	if sub != "" {
		code += "-" + codePart(sub)
		name = firstNonEmpty(c.subs[co+"|"+accd+"|"+sub], name+" / "+sub)
	}
	return code, name, ""
}

// reportUnknown raises one warning per head whose type could not be told.
func (c *chart) reportUnknown(rep *report, area string) {
	for _, h := range sortedKeys(c.unknown) {
		rep.warnf(area, h, "unknown-head", "Check the account type after loading; move it to the right account in Ledgerline if needed",
			"Ledger head %s (%s) has no recognisable account group; it is migrated as a suspense account", h, c.heads[h].descr)
	}
	c.unknown = map[string]bool{}
}

func (c *chart) clientHeadList() []string { return sortedKeys(c.clientHeads) }
