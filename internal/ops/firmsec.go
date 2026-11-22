package ops

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// ---- Firm securities: the broker's own securities ----
//
// A broker holds securities of its own in one or both of two portfolios:
//
//   - Investments (INV): held as an inventory of lots at cost, sold first
//     in first out; profit on sale goes to the profit and loss account and
//     to the capital gains register. AS 13 (or Ind AS 109) values them.
//   - Stock-in-trade (SIT): a purchase and sale book (purchases and sales
//     accounts, closing stock valued at the period end), the way a trading
//     business keeps it; ICDS VIII values it for tax.
//
// Everything a firm decides about them is a setting here (FSSettings),
// asked as the "Firm securities" section of Settings → Firm practices
// (firmsecpractices.go), so one firm's answers are only its settings.
// The lots and their entries are in the securities register
// (firmsecregister.go); the reports, valuation and net worth are in
// firmsecreports.go and firmsecvalue.go; the demat check, pledges and
// dividends in firmsecrecon.go.

const (
	cFSSettings = "fssettings"

	PortInv = "INV" // investments
	PortSIT = "SIT" // stock-in-trade

	// FSNone is the portfolios answer of a firm that holds no securities.
	FSNone = "none"
	FSBoth = "both"
)

// PortfolioName names a portfolio for people.
func PortfolioName(p string) string {
	switch p {
	case PortInv:
		return "Investments"
	case PortSIT:
		return "Stock-in-trade"
	}
	return p
}

// FSSettings are the firm's choices for its own securities. The comments
// name the questionnaire item each one answers.
type FSSettings struct {
	// Portfolios (s1): inv, sit, both or none.
	Portfolios string `json:"portfolios"`
	// Demats (s2): "separate" (each portfolio has its own demat account,
	// at the firm's DP or another, CDSL or NSDL) or "one" (one account;
	// the register keeps the portfolios apart).
	Demats   string `json:"demats"`
	InvDemat string `json:"inv_demat,omitempty"`
	SITDemat string `json:"sit_demat,omitempty"`
	// Default (s3): "change" (the trading code gives the default
	// portfolio, changeable until the day is closed) or "fixed".
	Default  string   `json:"default"`
	SITCodes []string `json:"sit_codes"`
	InvCodes []string `json:"inv_codes"`
	// Moves (s4): "journal" (a stock journal with a second person's
	// approval) or "no".
	Moves string `json:"moves"`
	// Instruments (s5): "all" or "listed"; Extra are instrument types of
	// the firm's own (each asked how it is treated: Kinds).
	Instruments string   `json:"instruments"`
	Extra       []string `json:"extra,omitempty"`
	Kinds       []FSKind `json:"kinds,omitempty"` // treatments the firm set for its own types
	// InvMethod (i1) and SITMethod (t3): "fifo" (perpetual first in,
	// first out).
	InvMethod string `json:"inv_method"`
	SITMethod string `json:"sit_method"`
	// InvCost (i2): "full" (price, brokerage, stamp duty, exchange and
	// SEBI charges, GST: no input credit taken), "full-itc" (the same
	// without GST, input credit taken) or "price" (price only).
	InvCost string `json:"inv_cost"`
	// Standard (i3): "as" (Accounting Standards) or "indas" (Ind AS).
	Standard string `json:"standard"`
	// Term (i4): "lot" (each investment lot marked long-term or current
	// when bought) or "period" (by holding period at the reporting date).
	Term string `json:"term"`
	// InvValue (i5): "cost", "lcm" (lower of cost and closing price for
	// current investments, long-term at cost), "lcm-all" or "fair".
	InvValue string `json:"inv_value"`
	// Corp (i6): "engine": the corporate actions engine on firm lots.
	Corp string `json:"corp"`
	// CGRegister (i7): keep the capital gains register for investments.
	CGRegister bool `json:"cg_register"`
	// Income (i8): "auto" (dividends from the corporate actions, TDS as
	// receivable, matched to bank credits) or "manual".
	Income       string `json:"income"`
	TDSPct       string `json:"tds_pct,omitempty"`
	TDSThreshold string `json:"tds_threshold,omitempty"`
	// SITSource (t1): "auto" (the trade files plus manual entries) or
	// "manual".
	SITSource string `json:"sit_source"`
	// Spec (t2): keep intraday (speculative) trades apart.
	Spec bool `json:"spec"`
	// Closing (t4): "cost", "lcm-scrip", "lcm-category" or "fair".
	Closing string `json:"closing"`
	// SITCharges (t5): "expense" (all charges as expenses) or "cost"
	// (charges other than STT added to the purchase cost).
	SITCharges string `json:"sit_charges"`
	// DematCheck (c1): "daily", "monthly" or "off".
	DematCheck string `json:"demat_check"`
	// Pledges (c2): "track" (tracked, not blocked: a warning before a
	// pledged lot is sold).
	Pledges string `json:"pledges"`
	// Disclosure (c3): add the proprietary trading disclosure to the
	// account-opening documents.
	Disclosure     bool   `json:"disclosure"`
	DisclosureText string `json:"disclosure_text,omitempty"`
	// NetWorth (c4): "cost" (securities at cost) or "book" (as carried).
	NetWorth string `json:"net_worth"`
	// Checker (c5): "manual" (a second person approves manual and
	// off-market entries), "all" or "none".
	Checker string `json:"checker"`
	// Accounts (c6): "kind" (one account per portfolio and kind).
	Accounts string `json:"accounts"`
	// Opening (c7): "lots" (lot by lot from the old system) or "average".
	Opening string `json:"opening"`
	By      string `json:"by,omitempty"`
	At      string `json:"at,omitempty"`
}

// DefaultFSSettings are the choices a firm starts with: no securities of
// its own until it says where it keeps them (so nothing is posted for a
// firm that has not answered); then trades under PRO going to
// stock-in-trade, FIFO at full cost, AS, everything at cost, intraday
// apart, a daily demat check.
func DefaultFSSettings() FSSettings {
	return FSSettings{Portfolios: FSNone, Demats: "separate", Default: "change", SITCodes: []string{"PRO"}, InvCodes: []string{}, Moves: "journal",
		Instruments: "all", InvMethod: "fifo", SITMethod: "fifo", InvCost: "full", Standard: "as", Term: "lot", InvValue: "cost", Corp: "engine",
		CGRegister: true, Income: "auto", TDSPct: "10", TDSThreshold: "10000", SITSource: "auto", Spec: true, Closing: "cost", SITCharges: "expense",
		DematCheck: "daily", Pledges: "track", Disclosure: true, NetWorth: "cost", Checker: "manual", Accounts: "kind", Opening: "lots"}
}

// FirmSecSettings returns the firm's settings for its own securities.
func (o *Ops) FirmSecSettings(ctx context.Context, tenant string) FSSettings {
	s, _, _ := docs.GetOr(ctx, o.Docs, tenant, cFSSettings, "settings", DefaultFSSettings())
	if s.SITCodes == nil {
		s.SITCodes = []string{}
	}
	if s.InvCodes == nil {
		s.InvCodes = []string{}
	}
	return s
}

// saveFS changes the settings (the caller checked the actor).
func (o *Ops) saveFS(ctx context.Context, tenant, actor string, fn func(*FSSettings) error) error {
	s := o.FirmSecSettings(ctx, tenant)
	if err := fn(&s); err != nil {
		return err
	}
	s.By, s.At = actor, o.now().Format(time.RFC3339)
	if err := docs.Upsert(ctx, o.Docs, tenant, cFSSettings, "settings", s, actor); err != nil {
		return err
	}
	o.log(ctx, tenant, actor, "firm_securities.settings", "settings", map[string]any{"by": actor})
	o.fsKindsWork(ctx, tenant, actor, s)
	return nil
}

// has says whether the firm keeps a portfolio.
func (s FSSettings) has(p string) bool {
	switch s.Portfolios {
	case FSBoth:
		return p == PortInv || p == PortSIT
	case "inv":
		return p == PortInv
	case "sit":
		return p == PortSIT
	}
	return false
}

// Ports are the portfolios the firm keeps, investments first.
func (s FSSettings) Ports() []string {
	var out []string
	for _, p := range []string{PortInv, PortSIT} {
		if s.has(p) {
			out = append(out, p)
		}
	}
	return out
}

// codePortfolio is the default portfolio of a trade under a trading code
// ("" when the code is not one of the firm's own).
func (s FSSettings) codePortfolio(code string) string {
	code = strings.ToUpper(code)
	if contains(s.InvCodes, code) && s.has(PortInv) {
		return PortInv
	}
	if contains(s.SITCodes, code) {
		if s.has(PortSIT) {
			return PortSIT
		}
		if s.has(PortInv) {
			return PortInv
		}
	}
	return ""
}

// ownCodes are every trading code of the firm's own.
func (s FSSettings) ownCodes() []string {
	var out []string
	for _, c := range append(append([]string{}, s.SITCodes...), s.InvCodes...) {
		if c = strings.ToUpper(strings.TrimSpace(c)); c != "" && !contains(out, c) {
			out = append(out, c)
		}
	}
	return out
}

// needsChecker says whether an entry of a type waits for a second person.
func (s FSSettings) needsChecker(typ string) bool {
	switch s.Checker {
	case "none":
		return false
	case "all":
		return true
	}
	return typ != FSTypeTrade && typ != FSTypeCorp
}

// pctSetting parses a percentage setting.
func pctSetting(s string, def int64) money.Dec {
	if d, err := money.ParseDec(strings.TrimSpace(s)); err == nil && d.Sign() >= 0 {
		return d
	}
	return money.DecInt(def)
}

// ---- instrument kinds ----

// FSKind is a kind of security the firm holds, with how it is treated.
type FSKind struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Category is the ICDS VIII category closing stock is compared in:
	// shares, debt, convertible or other.
	Category string `json:"category"`
	// Tax is the capital gains class: equity (STT paid: 12 months,
	// 111A/112A), listed (other listed securities: 12 months, 112), sgb,
	// unlisted (24 months), mf-other (24 months; specified funds bought
	// from 1 April 2023 always short-term, section 50AA).
	Tax    string `json:"tax"`
	Listed bool   `json:"listed"`
	// Own marks a type the firm added; Asked is false until the firm has
	// said how it is treated.
	Own   bool `json:"own,omitempty"`
	Asked bool `json:"asked"`
}

// fsBaseKinds are the kinds every firm has (questionnaire s5).
var fsBaseKinds = []FSKind{
	{ID: "EQ", Name: "Listed equity shares", Category: "shares", Tax: "equity", Listed: true, Asked: true},
	{ID: "ETF", Name: "Exchange traded funds", Category: "other", Tax: "equity", Listed: true, Asked: true},
	{ID: "MFE", Name: "Equity-oriented mutual fund units", Category: "other", Tax: "equity", Listed: false, Asked: true},
	{ID: "MFO", Name: "Other mutual fund units", Category: "other", Tax: "mf-other", Listed: false, Asked: true},
	{ID: "BOND", Name: "Bonds and debentures", Category: "debt", Tax: "listed", Listed: true, Asked: true},
	{ID: "GSEC", Name: "Government securities and treasury bills", Category: "debt", Tax: "listed", Listed: true, Asked: true},
	{ID: "SGB", Name: "Sovereign gold bonds", Category: "debt", Tax: "sgb", Listed: true, Asked: true},
	{ID: "UNL", Name: "Unlisted shares", Category: "shares", Tax: "unlisted", Listed: false, Asked: true},
}

// FSCategories are ICDS VIII's categories of securities held as
// stock-in-trade (para 8: shares, debt securities, convertible
// securities, any other securities).
var FSCategories = []struct{ ID, Name string }{
	{"shares", "Shares"}, {"debt", "Debt securities"}, {"convertible", "Convertible securities"}, {"other", "Any other securities"},
}

// FSTaxClasses are the capital gains classes a kind can take.
var FSTaxClasses = []struct{ ID, Name string }{
	{"equity", "Listed equity, equity-oriented fund or business trust units (STT paid): long-term after 12 months"},
	{"listed", "Other listed securities: long-term after 12 months"},
	{"sgb", "Sovereign gold bonds: long-term after 12 months; redemption by RBI exempt"},
	{"unlisted", "Unlisted shares and other assets: long-term after 24 months"},
	{"mf-other", "Other fund units: long-term after 24 months; specified funds bought from 1 April 2023 always short-term"},
}

var reKindID = regexp.MustCompile(`[^A-Z0-9]+`)

// ownKindID is the ID of a type the firm added: X and its name's letters.
func ownKindID(name string) string {
	id := strings.Trim(reKindID.ReplaceAllString(strings.ToUpper(name), "_"), "_")
	if len(id) > 16 {
		id = id[:16]
	}
	return "X_" + id
}

// kinds lists the kinds the firm uses: the base ones (listed only, when
// the firm chose listed shares and ETFs only) and its own.
func (s FSSettings) kinds() []FSKind {
	var out []FSKind
	for _, k := range fsBaseKinds {
		if s.Instruments == "listed" && k.ID != "EQ" && k.ID != "ETF" {
			continue
		}
		out = append(out, k)
	}
	for _, n := range s.Extra {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		k := FSKind{ID: ownKindID(n), Name: n, Category: "other", Tax: "unlisted", Own: true}
		for _, x := range s.Kinds {
			if x.ID == k.ID {
				k.Category, k.Tax, k.Listed, k.Asked = x.Category, x.Tax, x.Listed, x.Asked
			}
		}
		out = append(out, k)
	}
	return out
}

func (s FSSettings) kind(id string) FSKind {
	for _, k := range s.kinds() {
		if k.ID == id {
			return k
		}
	}
	for _, k := range fsBaseKinds {
		if k.ID == id {
			return k
		}
	}
	return FSKind{ID: id, Name: id, Category: "other", Tax: "unlisted"}
}

// fsKindsWork asks the Admin how each of the firm's own instrument types
// is treated, until it is said.
func (o *Ops) fsKindsWork(ctx context.Context, tenant, actor string, s FSSettings) {
	for _, k := range s.kinds() {
		if k.Own && !k.Asked {
			o.raiseWork(ctx, tenant, "firmsec", "kind|"+k.ID, "Say how "+k.Name+" is treated",
				fmt.Sprintf("The firm holds %s, an instrument type of its own. Say on Firm securities → Holdings (Instrument types) which ICDS VIII category its closing stock is compared in and how its capital gains are worked out. Until then Ledgerline treats it as an unlisted asset in \"any other securities\".", k.Name),
				"", RoleAdmin, "firmsec", o.now().Format("2006-01-02"))
		}
	}
}

// SetFSKind records how one of the firm's own instrument types is treated.
func (o *Ops) SetFSKind(ctx context.Context, tenant, actor string, k FSKind) (FSSettings, error) {
	if err := o.require(ctx, tenant, actor, "Changing firm securities settings", RoleAdmin); err != nil {
		return FSSettings{}, err
	}
	okCat, okTax := false, false
	for _, c := range FSCategories {
		okCat = okCat || c.ID == k.Category
	}
	for _, c := range FSTaxClasses {
		okTax = okTax || c.ID == k.Tax
	}
	if !okCat || !okTax {
		return FSSettings{}, invalid("choose the ICDS VIII category and the capital gains class")
	}
	err := o.saveFS(ctx, tenant, actor, func(s *FSSettings) error {
		found := false
		for _, x := range s.kinds() {
			found = found || (x.Own && x.ID == k.ID)
		}
		if !found {
			return invalid("%s is not one of the firm's own instrument types", k.ID)
		}
		var keep []FSKind
		for _, x := range s.Kinds {
			if x.ID != k.ID {
				keep = append(keep, x)
			}
		}
		s.Kinds = append(keep, FSKind{ID: k.ID, Category: k.Category, Tax: k.Tax, Listed: k.Listed, Asked: true, Own: true})
		return nil
	})
	if err == nil {
		o.closeWork(ctx, tenant, actor, "firmsec", "kind|"+k.ID, "treatment set")
	}
	return o.FirmSecSettings(ctx, tenant), err
}

// kindOfTrade guesses a security's kind from what the exchange gives:
// the series and the ISIN (IN + issuer type: E companies, F funds, 0 to 9
// the governments) and the company's name.
func kindOfTrade(series, isin, symbol, company string) string {
	series, isin = strings.ToUpper(series), strings.ToUpper(isin)
	up := strings.ToUpper(symbol + " " + company)
	switch {
	case series == "GB" || strings.HasPrefix(strings.ToUpper(symbol), "SGB"):
		return "SGB"
	case series == "GS" || series == "TB" || series == "SG" || (len(isin) > 2 && isin[2] >= '0' && isin[2] <= '9'):
		return "GSEC"
	case strings.HasPrefix(isin, "INF"):
		if strings.Contains(up, "ETF") || strings.Contains(up, "BEES") {
			return "ETF"
		}
		return "MFE"
	case series != "" && series != "EQ" && series != "BE" && series != "BZ" && series != "SM" && series != "ST" && (series[0] == 'N' || series[0] == 'Y' || series[0] == 'Z' || series[0] == 'D'):
		return "BOND"
	}
	return "EQ"
}

// ---- accounts: one per portfolio and kind (questionnaire c6) ----
//
// The accounts sit under the chart's heads (chart.go): investments under
// "Investments (FDs, securities)", the closing stock under
// "Stock-in-trade (securities)", purchases and sales under their own
// heads, the charges on own trades under exchange and clearing charges.
// Scrip and lot detail is in the register, not the chart.

// Heads the securities accounts sit under (added to defaultHeads).
const (
	HeadStock  = "A-STOCK"
	HeadSales  = "I-SALES"
	HeadPurch  = "E-PURCH"
	headInvest = "A-INVEST"
)

func fsInvAcct(kind string) string { return "ASSET:FSINV_" + kind }
func fsSITAcct(kind string) string { return "ASSET:FSSIT_" + kind }
func fsPurAcct(kind string, spec bool) string {
	if spec {
		return "EXPENSE:FSPUR_" + kind + "_SPEC"
	}
	return "EXPENSE:FSPUR_" + kind
}
func fsSalAcct(kind string, spec bool) string {
	if spec {
		return "INCOME:FSSAL_" + kind + "_SPEC"
	}
	return "INCOME:FSSAL_" + kind
}

// The firm's other securities accounts.
const (
	AcctFSChangeInv   = "EXPENSE:FS_CHANGE_INV"
	AcctFSInvProfit   = "INCOME:FS_INV_PROFIT"
	AcctFSDividend    = "INCOME:FS_DIVIDEND"
	AcctFSInterest    = "INCOME:FS_INTEREST"
	AcctFSTransfer    = "INCOME:FS_TRANSFER"
	AcctFSFairValue   = "INCOME:FS_FAIR_VALUE"
	AcctFSBuyBackLoss = "EXPENSE:FS_BUYBACK_LOSS"
	AcctFSProvExp     = "EXPENSE:FS_PROVISION"
	AcctFSProvision   = "ASSET:FS_PROVISION"
	AcctFSFVAdj       = "ASSET:FS_FV_ADJ"
	AcctFSIncomeRecv  = "RECEIVABLE:FS_INCOME"
	AcctFSTDS         = "RECEIVABLE:FS_TDS"
)

// fsCharge heads: each charge on the firm's own trades has an expense
// account of its own, so reports can show each head (questionnaire i2).
var fsChargeHeads = []struct{ ID, Name, Acct string }{
	{"brokerage", "Brokerage", "EXPENSE:FSCHG_BROK"},
	{"stt", "STT", "EXPENSE:FSCHG_STT"},
	{"exchange", "Exchange and SEBI charges", "EXPENSE:FSCHG_EXCH"},
	{"stamp", "Stamp duty", "EXPENSE:FSCHG_STAMP"},
	{"gst", "GST on charges", "EXPENSE:FSCHG_GST"},
	{"other", "Other charges", "EXPENSE:FSCHG_OTHER"},
}

var fsFixedAccounts = []struct{ code, name, head string }{
	{AcctFSChangeInv, "Changes in inventories of stock-in-trade", HeadPurch},
	{AcctFSInvProfit, "Profit / loss on sale of investments", "I-OTHER"},
	{AcctFSDividend, "Dividend income", "I-OTHER"},
	{AcctFSInterest, "Interest on investments", "I-INT"},
	{AcctFSTransfer, "Gain / loss on transfer between portfolios", "I-OTHER"},
	{AcctFSFairValue, "Fair value gain / loss on securities", "I-OTHER"},
	{AcctFSBuyBackLoss, "Loss on buy-back of shares (cost written off)", "E-OTHER"},
	{AcctFSProvExp, "Provision for diminution in value of investments (charge)", "E-OTHER"},
	{AcctFSProvision, "Provision for diminution in value of investments", headInvest},
	{AcctFSFVAdj, "Fair value adjustment on investments", headInvest},
	{AcctFSIncomeRecv, "Dividend and interest receivable", "A-RECV"},
	{AcctFSTDS, "TDS receivable on dividends and interest", "A-RECV"},
}

// fsAccountName names a securities account (and its head), or "" when
// the code is not one.
func (s FSSettings) fsAccountName(code string) (name, head string) {
	for _, a := range fsFixedAccounts {
		if a.code == code {
			return a.name, a.head
		}
	}
	for _, h := range fsChargeHeads {
		if h.Acct == code {
			return h.Name + " on own trades", "E-EXCH"
		}
	}
	for _, p := range []struct{ prefix, name, head string }{
		{"ASSET:FSINV_", "Investments – ", headInvest}, {"ASSET:FSSIT_", "Stock-in-trade – ", HeadStock},
		{"EXPENSE:FSPUR_", "Purchases – ", HeadPurch}, {"INCOME:FSSAL_", "Sales – ", HeadSales},
	} {
		if rest, ok := strings.CutPrefix(code, p.prefix); ok {
			spec := strings.HasSuffix(rest, "_SPEC")
			k := s.kind(strings.TrimSuffix(rest, "_SPEC"))
			n := p.name + strings.ToLower(k.Name[:1]) + k.Name[1:]
			if spec {
				n += " (intraday)"
			}
			return n, p.head
		}
	}
	return "", ""
}

// ensureFSAccounts puts the securities accounts an entry posts to in the
// chart, under their heads, the first time they are used.
func (o *Ops) ensureFSAccounts(ctx context.Context, tenant string, s FSSettings, codes []string) {
	m := o.accountMaster(ctx, tenant)
	changed := false
	for _, c := range codes {
		if _, ok := m[c]; ok {
			continue
		}
		name, head := s.fsAccountName(c)
		if name == "" {
			continue
		}
		a := Account{Code: c, Name: name, Head: head, Type: accountType(c), System: true, Status: accountActive, CreatedBy: SystemActor, Created: o.now().Format(time.RFC3339), AllowedUsers: []string{}}
		a.Group = accountGroup(c)
		if err := docs.Upsert(ctx, o.Docs, tenant, cAccount, c, a, SystemActor); err == nil {
			m[c] = a
			changed = true
		}
	}
	if changed {
		o.indexChanged(tenant)
	}
}
