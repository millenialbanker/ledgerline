package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/money"
)

// ---- RMS limits beyond margin: position limits, index limits, the F&O
// ban period, surveillance measures, the expiry-day spread rule and the
// early pay-in margin rule ----
//
// Sources, as the firm's compliance gap report gives them (the protocol
// texts are not in the repo):
//
//   - NSE Clearing NCL/CMPT/73997 (consolidated circular, F&O), items
//     11.1-11.3.2; NSE/COM/73930 items 1.10-1.11 (commodity derivatives):
//     client and member open positions in derivatives within the clearing
//     corporation's position limits (by default a share of the market-wide
//     position limit, MWPL: client 10%, proprietary 20%, member 30%, as
//     the gap report reads them; each is a setting), measured on positions
//     across exchanges; on a breach no fresh position and the excess
//     reduced.
//   - SEBI/HO/MRD/TPD-1/P/CIR/2025/79, para 5.5 and Annexure-1: index
//     futures gross position of a client or of the firm's own account
//     within the higher of 15% of the index futures open interest and
//     ₹500 crore (5.5.2); each PAN's end-of-day index options FutEq within
//     ₹1,500 crore net and ₹10,000 crore gross on each side (5.5.1), more
//     only when backed by stock holdings (net short) or cash, G-secs and
//     T-bills (net long), reported by the clearing member to the clearing
//     corporation (5.5.4.2-5.5.4.3); in the F&O ban period an order only
//     if it reduces the entity's FutEq open interest (net delta) by the end
//     of the day.
//   - SEBI/HO/MRD/MRD-PoD-1/P/CIR/2023/136 (commodity derivatives master
//     circular) paras 3.4.1(iii)-(iv) and 3.4.2: client-level limits apply
//     to the firm's own (proprietary) positions; the member-level open
//     position adds all clients' and proprietary longs, and separately all
//     shorts, and takes the higher; no splitting of positions across codes
//     of clients acting in concert.
//   - NSE/SURV/74008 (circular 313/2026): the exchanges' surveillance
//     measures (GSM, ESM, LT-ASM, ST-ASM, IBC, ICA) applied in RMS: stage
//     margins and trade-for-trade.
//   - SEBI HO/47/15/11(2)2025-MRD-TPD1/I/4226/2026: no calendar-spread
//     benefit on expiry day for single-stock spreads that involve the
//     contract expiring that day.
//   - SEBI HO/(320)2026-MRD-POD2/I/24141/2026: from four months after
//     22 Oct 2026, a client may square off a sale backed by early pay-in,
//     or take a new cash position, only with the required margin actually
//     available (the date is a setting).
//
// Where a text is ambiguous Ledgerline takes the stricter reading and says
// so here: a client's (and a connected group's) open position in an
// underlying is its long FutEq plus its short FutEq, never netted across
// contracts; the member level in commodities is the higher of the summed
// longs and the summed shorts (SEBI para 3.4.1(iv)), and in equity and
// currency derivatives the sum of the clients' and the firm's open
// positions. An option with no delta known counts at its whole quantity.

const (
	cRiskLimCfg = "risklimcfg" // "config"
	cPosLimits  = "poslimits"  // date: the exchange's or clearing corporation's limit file
	cFOBan      = "foban"      // date: securities in the F&O ban period
	cSurvList   = "survlist"   // "current": surveillance measures in force
	cRiskLimRun = "risklimrun" // date: when the end-of-day checks last ran

	wPosLimits   = "position_limits"
	wIndexLimits = "index_limits"
	wIndexReport = "index_cover_report"
	wFOBan       = "fo_ban"
	wExpirySprd  = "expiry_spread"
	wSurvT2T     = "surv_t2t"
	wEPIMargin   = "epi_margin"
)

// RiskLimitConfig is the firm's RMS limit settings (RMS → Position
// limits). Zero values take the defaults (fill).
type RiskLimitConfig struct {
	// PositionLimitsOff stops the position-limit, index-limit and ban
	// checks (a firm that trades no derivatives).
	PositionLimitsOff bool `json:"position_limits_off,omitempty"`
	// Shares of the MWPL, in %, used when the limit file gives no
	// client or member limit of its own.
	ClientMWPLPct int64 `json:"client_mwpl_pct"`
	PropMWPLPct   int64 `json:"prop_mwpl_pct"`
	MemberMWPLPct int64 `json:"member_mwpl_pct"`
	// WarnPct of a limit puts a position on the watch list.
	WarnPct int64 `json:"warn_pct"`
	// BanPct of the MWPL puts a security in the ban period from the next
	// trading day, when no ban file is loaded for the date.
	BanPct int64 `json:"ban_pct"`
	// Index futures: the higher of IndexFutOIPct of the index futures open
	// interest and IndexFutFloorCr crore.
	IndexFutOIPct   int64 `json:"index_fut_oi_pct"`
	IndexFutFloorCr int64 `json:"index_fut_floor_cr"`
	// Index options per PAN, crore: net FutEq, and gross on each side.
	IndexOptNetCr   int64 `json:"index_opt_net_cr"`
	IndexOptGrossCr int64 `json:"index_opt_gross_cr"`
	// ProprietaryUCCs are the firm's own trading codes; a client whose PAN
	// is the firm's PAN (Settings) is proprietary too.
	ProprietaryUCCs []string `json:"proprietary_uccs,omitempty"`
	// GrossAcrossExchanges margins each exchange apart even where one
	// clearing corporation clears both (the stricter, not the clearing
	// corporation's, figure).
	GrossAcrossExchanges bool `json:"gross_across_exchanges,omitempty"`
	// SurvMarginsOff stops applying surveillance stage margins.
	SurvMarginsOff bool `json:"surv_margins_off,omitempty"`
	// EPIMarginFrom is when a square-off of a sale backed by early pay-in,
	// or a new cash position, needs the margin actually available.
	EPIMarginFrom string `json:"epi_margin_from"`
	// ExpirySpreadFrom is when the expiry-day spread rule applies.
	ExpirySpreadFrom string `json:"expiry_spread_from"`
	// Pay-in cut-offs (HH:MM, IST) on the settlement day; the auction
	// pay-in is the business day after the auction (rmspayin.go).
	PayInRemindersOff bool   `json:"payin_reminders_off,omitempty"`
	FundsPayInBy      string `json:"funds_payin_by"`
	SecuritiesPayInBy string `json:"securities_payin_by"`
	AuctionPayInBy    string `json:"auction_payin_by"`
	// ICCL's own cut-offs (BSECL cash master circular chapter 4 part A):
	// the normal pay-in, funds and securities, by 10:30 on T+1; the auction
	// pay-in by 08:00 with its securities instructions by 07:50
	// (icclsettle.go). Used only for obligations cleared at ICCL.
	ICCLFundsPayInBy      string `json:"iccl_funds_payin_by,omitempty"`
	ICCLSecuritiesPayInBy string `json:"iccl_securities_payin_by,omitempty"`
	ICCLAuctionPayInBy    string `json:"iccl_auction_payin_by,omitempty"`
	ICCLAuctionInstrBy    string `json:"iccl_auction_instr_by,omitempty"`
	// CCSplitOff stops splitting one pay-in or pay-out of a clearing
	// corporation across the exchanges it clears (rmspayin.go).
	CCSplitOff bool   `json:"cc_split_off,omitempty"`
	By         string `json:"by,omitempty"`
	At         string `json:"at,omitempty"`
}

// fill sets the defaults of unset values.
func (c *RiskLimitConfig) fill() {
	def := func(p *int64, v int64) {
		if *p <= 0 {
			*p = v
		}
	}
	def(&c.ClientMWPLPct, 10)
	def(&c.PropMWPLPct, 20)
	def(&c.MemberMWPLPct, 30)
	def(&c.WarnPct, 90)
	def(&c.BanPct, 95)
	def(&c.IndexFutOIPct, 15)
	def(&c.IndexFutFloorCr, 500)
	def(&c.IndexOptNetCr, 1500)
	def(&c.IndexOptGrossCr, 10000)
	c.EPIMarginFrom = firstOf(c.EPIMarginFrom, "2027-02-22")
	c.ExpirySpreadFrom = firstOf(c.ExpirySpreadFrom, "2026-05-05")
	c.FundsPayInBy = firstOf(c.FundsPayInBy, "11:00")
	c.SecuritiesPayInBy = firstOf(c.SecuritiesPayInBy, "10:30")
	c.AuctionPayInBy = firstOf(c.AuctionPayInBy, "08:00")
	c.ICCLFundsPayInBy = firstOf(c.ICCLFundsPayInBy, "10:30")
	c.ICCLSecuritiesPayInBy = firstOf(c.ICCLSecuritiesPayInBy, "10:30")
	c.ICCLAuctionPayInBy = firstOf(c.ICCLAuctionPayInBy, "08:00")
	c.ICCLAuctionInstrBy = firstOf(c.ICCLAuctionInstrBy, "07:50")
}

// RiskLimitConfig is the firm's RMS limit settings, defaults filled.
func (o *Ops) RiskLimitConfig(ctx context.Context, tenant string) RiskLimitConfig {
	c, _, _ := docs.GetOr(ctx, o.Docs, tenant, cRiskLimCfg, "config", RiskLimitConfig{})
	c.fill()
	return c
}

// SaveRiskLimitConfig stores the settings (Risk manager or Admin).
func (o *Ops) SaveRiskLimitConfig(ctx context.Context, tenant, actor string, c RiskLimitConfig) (RiskLimitConfig, error) {
	if err := o.require(ctx, tenant, actor, "Changing RMS limit settings", RoleRisk); err != nil {
		return c, err
	}
	for _, p := range []struct {
		v    int64
		name string
	}{{c.ClientMWPLPct, "client share of MWPL"}, {c.PropMWPLPct, "proprietary share of MWPL"}, {c.MemberMWPLPct, "member share of MWPL"}, {c.WarnPct, "watch level"}, {c.BanPct, "ban level"}, {c.IndexFutOIPct, "share of index futures open interest"}} {
		if p.v < 0 || p.v > 100 {
			return c, invalid("the %s is a percentage from 1 to 100", p.name)
		}
	}
	for _, p := range []int64{c.IndexFutFloorCr, c.IndexOptNetCr, c.IndexOptGrossCr} {
		if p < 0 {
			return c, invalid("index limits are crore amounts above zero")
		}
	}
	for _, d := range []string{c.EPIMarginFrom, c.ExpirySpreadFrom} {
		if d != "" && !validDate(d) {
			return c, invalid("dates are YYYY-MM-DD, got %q", d)
		}
	}
	for _, t := range []string{c.FundsPayInBy, c.SecuritiesPayInBy, c.AuctionPayInBy, c.ICCLFundsPayInBy, c.ICCLSecuritiesPayInBy, c.ICCLAuctionPayInBy, c.ICCLAuctionInstrBy} {
		if _, err := time.Parse("15:04", t); t != "" && err != nil {
			return c, invalid("cut-off times are HH:MM, got %q", t)
		}
	}
	var codes []string
	for _, u := range c.ProprietaryUCCs {
		if u = strings.ToUpper(strings.TrimSpace(u)); u != "" && !contains(codes, u) {
			codes = append(codes, u)
		}
	}
	c.ProprietaryUCCs, c.By, c.At = codes, actor, o.now().Format(time.RFC3339)
	if err := docs.Upsert(ctx, o.Docs, tenant, cRiskLimCfg, "config", c, actor); err != nil {
		return c, err
	}
	o.log(ctx, tenant, actor, "rms.limits.settings", "config", map[string]any{"client_mwpl_pct": c.ClientMWPLPct, "prop_mwpl_pct": c.PropMWPLPct, "member_mwpl_pct": c.MemberMWPLPct,
		"epi_margin_from": c.EPIMarginFrom, "gross_across_exchanges": c.GrossAcrossExchanges, "proprietary": c.ProprietaryUCCs, "off": c.PositionLimitsOff})
	c.fill()
	return c, nil
}

// ---- the files ----

// PositionLimit is one underlying's limits from the exchange's or the
// clearing corporation's limit file, in units of the underlying (the
// trade quantity), with the open interest of an index in rupees.
type PositionLimit struct {
	Exchange    string    `json:"exchange"`
	Segment     string    `json:"segment"`
	Symbol      string    `json:"symbol"`
	MWPL        money.Dec `json:"mwpl"`
	ClientLimit money.Dec `json:"client_limit"`
	MemberLimit money.Dec `json:"member_limit"`
	OIValue     money.Dec `json:"oi_value"`
}

type posLimitDoc struct {
	Date  string          `json:"date"`
	Items []PositionLimit `json:"items"`
}

type foBanDoc struct {
	Date    string   `json:"date"`
	Symbols []string `json:"symbols"`
}

// SurvMeasure is a security under an exchange surveillance measure.
type SurvMeasure struct {
	Exchange      string    `json:"exchange"`
	Symbol        string    `json:"symbol"`
	ISIN          string    `json:"isin,omitempty"`
	Measure       string    `json:"measure"` // GSM, ESM, LT-ASM, ST-ASM, IBC, ICA …
	Stage         string    `json:"stage,omitempty"`
	MarginPct     money.Dec `json:"margin_pct"`
	TradeForTrade bool      `json:"trade_for_trade,omitempty"`
	From          string    `json:"from,omitempty"`
	To            string    `json:"to,omitempty"`
}

func (m SurvMeasure) activeOn(date string) bool {
	return (m.From == "" || m.From <= date) && (m.To == "" || date <= m.To)
}

type survListDoc struct {
	AsOf  string        `json:"as_of"`
	Items []SurvMeasure `json:"items"`
}

// survIndex finds the measure in force on a security (the one with the
// highest margin when there are several).
type survIndex map[string]SurvMeasure

func (s survIndex) add(m SurvMeasure) {
	for _, k := range []string{m.Exchange + "|" + m.Symbol, m.ISIN} {
		if k == "" || k == "|" || strings.HasSuffix(k, "|") {
			continue
		}
		if old, ok := s[k]; !ok || m.MarginPct.Cmp(old.MarginPct) > 0 || m.TradeForTrade && !old.TradeForTrade {
			if ok && old.TradeForTrade {
				m.TradeForTrade = true
			}
			s[k] = m
		}
	}
}

func (s survIndex) find(exch, sym, isin string) (SurvMeasure, bool) {
	if s == nil {
		return SurvMeasure{}, false
	}
	if isin != "" {
		if m, ok := s[isin]; ok {
			return m, true
		}
	}
	m, ok := s[exch+"|"+sym]
	return m, ok
}

// survMeasuresOn is the surveillance list's measures in force on a date.
func (o *Ops) survMeasuresOn(ctx context.Context, tenant, date string) survIndex {
	d, _, err := docs.Get[survListDoc](ctx, o.Docs, tenant, cSurvList, "current")
	if err != nil {
		return nil
	}
	out := survIndex{}
	for _, m := range d.Items {
		if m.activeOn(date) {
			out.add(m)
		}
	}
	return out
}

// SurvMeasures is the loaded surveillance list.
func (o *Ops) SurvMeasures(ctx context.Context, tenant string) (string, []SurvMeasure) {
	d, _, _ := docs.Get[survListDoc](ctx, o.Docs, tenant, cSurvList, "current")
	if d.Items == nil {
		d.Items = []SurvMeasure{}
	}
	return d.AsOf, d.Items
}

func rmsLimitSources() []masterSrc {
	return []masterSrc{
		{"POSITION-LIMITS", "Position limits of derivatives underlyings for the date, from the exchange's or clearing corporation's limit files, in units of the underlying (the trade quantity). Optional columns: MWPL (market-wide position limit), ClientLimit and MemberLimit (when the exchange gives its own numbers, as in commodities; else the firm's shares of the MWPL apply), OIValue (an index's futures open interest in rupees, for the index futures limit). Replaces the date's list", []string{"Exchange", "Segment", "Symbol"}, true, "Daily, before trading"},
		{"FO-BAN", "Securities in the F&O ban period for the date (the exchange's ban list). Replaces the date's list", []string{"Symbol"}, true, "Daily, before trading"},
		{"SURV-MEASURES", "Securities under exchange surveillance measures (GSM, ESM, LT-ASM, ST-ASM, IBC, ICA) with the margin of their stage. Optional columns: ISIN, Stage, MarginPct, TradeForTrade (Y or N), From and To (YYYY-MM-DD). Replaces the list", []string{"Exchange", "Symbol", "Measure"}, false, "On every exchange update"},
	}
}

// numCell reads a number that may carry thousands separators.
func numCell(s string) (money.Dec, error) {
	s = strings.TrimSpace(strings.ReplaceAll(s, ",", ""))
	if s == "" {
		return money.Dec{}, nil
	}
	return money.ParseDec(s)
}

// prepareRMSLimits reads the three files.
func (o *Ops) prepareRMSLimits(ctx context.Context, tenant, actor, source, date string, rows []row) (func() (int, int, error), []ingest.ParseError) {
	var errs []ingest.ParseError
	bad := func(l int, f, m string) { errs = append(errs, ingest.ParseError{Line: l, Field: f, Msg: m}) }
	switch source {
	case "POSITION-LIMITS":
		var items []PositionLimit
		for _, r := range rows {
			l := PositionLimit{Exchange: strings.ToUpper(r.get("Exchange")), Segment: strings.ToUpper(r.get("Segment")), Symbol: strings.ToUpper(r.get("Symbol"))}
			if l.Exchange == "" || l.Segment == "" || l.Symbol == "" {
				bad(r.line, "Symbol", "give the exchange, segment and symbol")
				continue
			}
			ok := true
			for _, f := range []struct {
				col string
				to  *money.Dec
			}{{"MWPL", &l.MWPL}, {"ClientLimit", &l.ClientLimit}, {"MemberLimit", &l.MemberLimit}, {"OIValue", &l.OIValue}} {
				v, err := numCell(r.get(f.col))
				if err != nil || v.Sign() < 0 {
					bad(r.line, f.col, "must be a number of zero or more")
					ok = false
				}
				*f.to = v
			}
			if ok && l.MWPL.IsZero() && l.ClientLimit.IsZero() && l.MemberLimit.IsZero() && l.OIValue.IsZero() {
				bad(r.line, "MWPL", "give at least one of MWPL, ClientLimit, MemberLimit or OIValue")
				ok = false
			}
			if ok {
				items = append(items, l)
			}
		}
		return func() (int, int, error) {
			return len(items), 0, docs.Upsert(ctx, o.Docs, tenant, cPosLimits, date, posLimitDoc{Date: date, Items: items}, actor)
		}, errs
	case "FO-BAN":
		var syms []string
		for _, r := range rows {
			if s := strings.ToUpper(r.get("Symbol")); s != "" && !contains(syms, s) {
				syms = append(syms, s)
			}
		}
		return func() (int, int, error) {
			return len(syms), 0, docs.Upsert(ctx, o.Docs, tenant, cFOBan, date, foBanDoc{Date: date, Symbols: syms}, actor)
		}, errs
	}
	var items []SurvMeasure
	for _, r := range rows {
		m := SurvMeasure{Exchange: strings.ToUpper(r.get("Exchange")), Symbol: strings.ToUpper(r.get("Symbol")), ISIN: strings.ToUpper(r.get("ISIN")),
			Measure: strings.ToUpper(r.get("Measure")), Stage: r.get("Stage"), From: r.get("From"), To: r.get("To")}
		if m.Symbol == "" || m.Measure == "" {
			bad(r.line, "Measure", "give the symbol and the measure")
			continue
		}
		v, err := numCell(r.get("MarginPct"))
		if err != nil || v.Sign() < 0 || v.Cmp(money.DecInt(100)) > 0 {
			bad(r.line, "MarginPct", "must be a percentage from 0 to 100")
			continue
		}
		m.MarginPct = v
		switch strings.ToUpper(r.get("TradeForTrade")) {
		case "Y", "YES", "TRUE", "1":
			m.TradeForTrade = true
		case "", "N", "NO", "FALSE", "0":
		default:
			bad(r.line, "TradeForTrade", "must be Y or N")
			continue
		}
		if (m.From != "" && !validDate(m.From)) || (m.To != "" && !validDate(m.To)) {
			bad(r.line, "From", "From and To are YYYY-MM-DD")
			continue
		}
		items = append(items, m)
	}
	asOf := firstOf(date, o.now().In(ist).Format("2006-01-02"))
	return func() (int, int, error) {
		return len(items), 0, docs.Upsert(ctx, o.Docs, tenant, cSurvList, "current", survListDoc{AsOf: asOf, Items: items}, actor)
	}, errs
}

// SurvMarginFile is the surveillance list in force on a date for the
// trading front end: each security's measure, stage, margin and whether it
// trades for trade (no intraday).
func (o *Ops) SurvMarginFile(ctx context.Context, tenant, date string) []byte {
	_, items := o.SurvMeasures(ctx, tenant)
	rows := [][]string{{"Date", "Exchange", "Symbol", "ISIN", "Measure", "Stage", "MarginPct", "TradeForTrade"}}
	for _, m := range items {
		if !m.activeOn(date) {
			continue
		}
		t2t := "N"
		if m.TradeForTrade {
			t2t = "Y"
		}
		rows = append(rows, []string{date, m.Exchange, m.Symbol, m.ISIN, m.Measure, m.Stage, m.MarginPct.String(), t2t})
	}
	return csvBytesOf(rows)
}

// positionLimitsOn is each underlying's limits by EXCH|SEG|SYMBOL: the
// latest limit file on or before the date, with the MWPL from NSE Clearing's
// margins API (mwpl) where the file has none.
func (o *Ops) positionLimitsOn(ctx context.Context, tenant string, set Settings, date string) (map[string]PositionLimit, []string) {
	out := map[string]PositionLimit{}
	var src []string
	ds, _ := docs.List[posLimitDoc](ctx, o.Docs, tenant, cPosLimits, "")
	best := -1
	for i, d := range ds {
		if d.Date <= date && (best < 0 || d.Date > ds[best].Date) {
			best = i
		}
	}
	if best >= 0 {
		for _, l := range ds[best].Items {
			out[l.Exchange+"|"+l.Segment+"|"+l.Symbol] = l
		}
		src = append(src, "Position limit file of "+fmtDay(ds[best].Date))
	}
	for _, d := range []string{date, set.PrevBusinessDay(date)} {
		snap, _, err := docs.Get[NCLMSnapshot](ctx, o.Docs, tenant, cNCLMSnap, "FO|"+d+"|mwpl")
		if err != nil {
			continue
		}
		for _, r := range snap.Rows {
			if len(r) < 3 {
				continue
			}
			lim, err := numCell(r[2])
			if err != nil || lim.Sign() <= 0 {
				continue
			}
			k := "NSE|FO|" + strings.ToUpper(strings.TrimSpace(r[0]))
			l := out[k]
			if l.MWPL.IsZero() {
				l.Exchange, l.Segment, l.Symbol, l.MWPL = "NSE", "FO", strings.ToUpper(strings.TrimSpace(r[0])), lim
				out[k] = l
			}
		}
		src = append(src, "NSE Clearing's MWPL ("+fmtDay(d)+" "+snap.At+")")
		break
	}
	return out, src
}

// bannedOn is the securities in the F&O ban period on a date: the ban
// file of the date, and those whose open interest NSE Clearing put at or
// above the ban level of the MWPL on the business day before.
func (o *Ops) bannedOn(ctx context.Context, tenant string, set Settings, lim RiskLimitConfig, date string) (map[string]string, string) {
	out := map[string]string{}
	src := ""
	if d, _, err := docs.Get[foBanDoc](ctx, o.Docs, tenant, cFOBan, date); err == nil {
		for _, s := range d.Symbols {
			out[s] = "ban list of " + fmtDay(date)
		}
		src = "F&O ban list of " + fmtDay(date)
	}
	prev := set.PrevBusinessDay(date)
	if snap, _, err := docs.Get[NCLMSnapshot](ctx, o.Docs, tenant, cNCLMSnap, "FO|"+prev+"|mwpl"); err == nil {
		for _, r := range snap.Rows {
			if len(r) < 4 {
				continue
			}
			pct, err := numCell(strings.TrimSuffix(strings.TrimSpace(r[3]), "%"))
			if err != nil || pct.Cmp(money.DecInt(lim.BanPct)) < 0 {
				continue
			}
			s := strings.ToUpper(strings.TrimSpace(r[0]))
			if _, ok := out[s]; !ok {
				out[s] = fmt.Sprintf("open interest %s%% of MWPL on %s", pct.Fixed(2), fmtDay(prev))
			}
		}
		src = strings.TrimPrefix(src+"; NSE Clearing's MWPL of "+fmtDay(prev), "; ")
	}
	return out, src
}

// ---- the view ----

// PositionLimitRow is one entity's open position in one underlying
// against its limit.
type PositionLimitRow struct {
	Level   string    `json:"level"`  // Client, Proprietary, Member, Connected group
	Entity  string    `json:"entity"` // a UCC, a masked PAN, the group's ID, "Member"
	UCCs    []string  `json:"uccs"`
	Market  string    `json:"market"` // the clearing corporation (or exchange) the positions are taken across
	Segment string    `json:"segment"`
	Symbol  string    `json:"symbol"`
	Long    money.Dec `json:"long"` // FutEq units
	Short   money.Dec `json:"short"`
	Open    money.Dec `json:"open"`
	Limit   money.Dec `json:"limit"`
	Basis   string    `json:"basis"`
	Pct     float64   `json:"pct"`
	Status  string    `json:"status"` // OK, Watch, Breach
	Note    string    `json:"note,omitempty"`
}

// IndexLimitRow is one index limit of a client, PAN or the firm's own
// account.
type IndexLimitRow struct {
	Kind   string    `json:"kind"` // Index futures gross, Index options net, Index options long side, Index options short side
	Level  string    `json:"level"`
	Entity string    `json:"entity"`
	PAN    string    `json:"pan,omitempty"` // masked
	UCCs   []string  `json:"uccs"`
	Index  string    `json:"index"`
	Value  money.Dec `json:"value"` // ₹
	Limit  money.Dec `json:"limit"`
	Basis  string    `json:"basis"`
	Status string    `json:"status"` // OK, Watch, Breach, Justified
	// For a net beyond the limit: what may back it (stock holdings for a
	// net short, cash and cash equivalents for a net long) and its value.
	CoverKind string    `json:"cover_kind,omitempty"`
	Cover     money.Dec `json:"cover"`
	Excess    money.Dec `json:"excess"`
	pan       string
}

// BanRow is a client's position in a security in the F&O ban period.
type BanRow struct {
	UCC       string    `json:"ucc"`
	Symbol    string    `json:"symbol"`
	Why       string    `json:"why"`
	Start     money.Dec `json:"start"` // net FutEq units at the start of the day
	End       money.Dec `json:"end"`
	Trades    int       `json:"trades"`
	Increased bool      `json:"increased"`
}

// SpreadRow is a client's single-stock calendar spread that involves the
// contract expiring on the date.
type SpreadRow struct {
	UCC      string   `json:"ucc"`
	Symbol   string   `json:"symbol"`
	Expiring []string `json:"expiring"`
	Other    []string `json:"other"`
}

// SurvHit is a client's trade in a security under a surveillance measure
// that the measure does not allow (intraday in a trade-for-trade security).
type SurvHit struct {
	UCC      string `json:"ucc"`
	Exchange string `json:"exchange"`
	Symbol   string `json:"symbol"`
	Measure  string `json:"measure"`
	Why      string `json:"why"`
}

// MemberOIRow is NSE Clearing's member open interest against its limit
// (margins API, moi).
type MemberOIRow struct {
	TM     string    `json:"tm"`
	Symbol string    `json:"symbol"`
	Type   string    `json:"type"`
	OI     money.Dec `json:"oi"`
	Limit  money.Dec `json:"limit"`
	Pct    float64   `json:"pct"`
	Status string    `json:"status"`
}

// RiskLimitsView is RMS → Position limits for a date.
type RiskLimitsView struct {
	Date         string             `json:"date"`
	Config       RiskLimitConfig    `json:"config"`
	Sources      []string           `json:"sources"`
	Missing      []string           `json:"missing"` // underlyings with positions and no limit known
	Limits       []PositionLimitRow `json:"limits"`
	Index        []IndexLimitRow    `json:"index"`
	Banned       []string           `json:"banned"`
	Ban          []BanRow           `json:"ban"`
	Spreads      []SpreadRow        `json:"spreads"`
	Surveillance []SurvHit          `json:"surveillance"`
	SurvAsOf     string             `json:"surv_as_of,omitempty"`
	SurvCount    int                `json:"surv_count"`
	MemberOI     []MemberOIRow      `json:"member_oi"`
	Breaches     int                `json:"breaches"`
	Notes        []string           `json:"notes"`
}

func limitStatus(pct float64, warn int64) string {
	switch {
	case pct >= 100:
		return "Breach"
	case pct >= float64(warn):
		return "Watch"
	}
	return "OK"
}

func limPct(v, lim money.Dec) float64 {
	if lim.Sign() <= 0 {
		return 0
	}
	return v.Float64() / lim.Float64() * 100
}

// proprietary says which trading codes are the firm's own.
func (in riskInput) proprietary() map[string]bool {
	out := map[string]bool{}
	for _, u := range in.lim.ProprietaryUCCs {
		out[u] = true
	}
	if pan := strings.TrimSpace(in.set.FirmPAN); pan != "" {
		for u, c := range in.clients {
			if c.PAN == pan {
				out[u] = true
			}
		}
	}
	return out
}

// RiskLimits works out the date's end-of-day position limits, index
// limits, ban-period trades, expiry-day spreads and surveillance hits.
// It reads only; RunRiskLimitChecks raises the work items.
func (o *Ops) RiskLimits(ctx context.Context, tenant, date string) (RiskLimitsView, error) {
	if !validDate(date) {
		return RiskLimitsView{}, invalid("give a date (YYYY-MM-DD)")
	}
	in, err := o.riskInput(ctx, tenant, date)
	if err != nil {
		return RiskLimitsView{}, err
	}
	v := RiskLimitsView{Date: date, Config: in.lim, Sources: []string{}, Missing: []string{}, Limits: []PositionLimitRow{}, Index: []IndexLimitRow{}, Banned: []string{},
		Ban: []BanRow{}, Spreads: []SpreadRow{}, Surveillance: []SurvHit{}, MemberOI: []MemberOIRow{}, Notes: []string{}}
	v.SurvAsOf, _ = o.SurvMeasures(ctx, tenant)
	v.SurvCount = len(in.surv)
	o.survHits(in, &v)
	if in.lim.PositionLimitsOff {
		v.Notes = append(v.Notes, "Position limit checks are switched off in RMS → Position limits → Settings.")
		return v, nil
	}
	pos, _ := in.positions("")
	limits, src := o.positionLimitsOn(ctx, tenant, in.set, date)
	v.Sources = append(v.Sources, src...)
	prop := in.proprietary()
	o.positionLimitRows(ctx, tenant, in, pos, limits, prop, &v)
	o.indexLimitRows(ctx, tenant, in, pos, limits, prop, &v)
	banned, bsrc := o.bannedOn(ctx, tenant, in.set, in.lim, date)
	if bsrc != "" {
		v.Sources = append(v.Sources, bsrc)
	}
	v.Banned = sortedKeys(banned)
	banRows(in, pos, banned, &v)
	if date >= in.lim.ExpirySpreadFrom {
		v.Spreads = expirySpreads(pos, date)
	}
	o.memberOI(ctx, tenant, in.lim, date, &v)
	for _, r := range v.Limits {
		if r.Status == "Breach" {
			v.Breaches++
		}
	}
	for _, r := range v.Index {
		if r.Status == "Breach" {
			v.Breaches++
		}
	}
	for _, b := range v.Ban {
		if b.Increased {
			v.Breaches++
		}
	}
	return v, nil
}

// positionLimitRows fills the client, proprietary, member and connected
// group rows of the date's derivative positions.
func (o *Ops) positionLimitRows(ctx context.Context, tenant string, in riskInput, pos map[string][]PositionView, limits map[string]PositionLimit, prop map[string]bool, v *RiskLimitsView) {
	type side struct {
		long, short money.Dec
		uccs        []string
	}
	// The limit of an underlying taken across a market: the first of the
	// market's exchanges whose file has it.
	limitOf := func(exchs []string, seg, sym string) (PositionLimit, bool) {
		sort.Strings(exchs)
		for _, e := range exchs {
			if l, ok := limits[e+"|"+seg+"|"+sym]; ok {
				return l, true
			}
		}
		return PositionLimit{}, false
	}
	ent := map[string]*side{}    // entity|market|seg|sym
	perUCC := map[string]*side{} // ucc|market|seg|sym
	member := map[string]*side{}
	exchsOf := map[string][]string{} // market|seg|sym -> exchanges
	idx := map[string]bool{}
	add := func(m map[string]*side, k, u string, q money.Dec) {
		s := m[k]
		if s == nil {
			s = &side{}
			m[k] = s
		}
		if q.Sign() > 0 {
			s.long = s.long.Add(q)
		} else {
			s.short = s.short.Add(q.Neg())
		}
		if u != "" && !contains(s.uccs, u) {
			s.uccs = append(s.uccs, u)
		}
	}
	entityOf := func(u string) (string, string) {
		if prop[u] {
			return "Proprietary", "Own account"
		}
		if c := in.clients[u]; c.PAN != "" {
			return "Client", "PAN:" + c.PAN
		}
		return "Client", u
	}
	for _, u := range sortedKeys(pos) {
		for _, p := range pos[u] {
			if p.Segment == "CM" {
				continue
			}
			mk := in.marginGroup(p.Exchange, p.Segment)
			sym := strings.SplitN(p.Instrument, "|", 2)[0]
			key := mk + "|" + p.Segment + "|" + sym
			if !contains(exchsOf[key], p.Exchange) {
				exchsOf[key] = append(exchsOf[key], p.Exchange)
			}
			if p.Index {
				idx[key] = true
			}
			lvl, e := entityOf(u)
			add(ent, lvl+"|"+e+"|"+key, u, p.FutEqQty)
			add(perUCC, u+"|"+key, u, p.FutEqQty)
			add(member, key, u, p.FutEqQty)
		}
	}
	pct := func(p int64, mwpl money.Dec) money.Dec { return mwpl.Pct(money.DecInt(p)) }
	missing := map[string]bool{}
	row := func(level, entity string, uccs []string, key string, s *side, open money.Dec) {
		parts := strings.SplitN(key, "|", 3)
		l, ok := limitOf(exchsOf[key], parts[1], parts[2])
		if !ok {
			if !idx[key] {
				missing[parts[2]+" ("+parts[0]+" "+parts[1]+")"] = true
			}
			return
		}
		var lim money.Dec
		var basis string
		switch level {
		case "Member":
			lim, basis = l.MemberLimit, "member limit in the limit file"
			if lim.IsZero() && !idx[key] {
				lim, basis = pct(in.lim.MemberMWPLPct, l.MWPL), fmt.Sprintf("%d%% of MWPL %s", in.lim.MemberMWPLPct, l.MWPL.Fixed(0))
			}
		case "Proprietary":
			lim, basis = l.ClientLimit, "client-level limit in the limit file (applies to the firm's own positions)"
			if lim.IsZero() && !idx[key] {
				lim, basis = pct(in.lim.PropMWPLPct, l.MWPL), fmt.Sprintf("%d%% of MWPL %s", in.lim.PropMWPLPct, l.MWPL.Fixed(0))
			}
		default:
			lim, basis = l.ClientLimit, "client limit in the limit file"
			if lim.IsZero() && !idx[key] {
				lim, basis = pct(in.lim.ClientMWPLPct, l.MWPL), fmt.Sprintf("%d%% of MWPL %s", in.lim.ClientMWPLPct, l.MWPL.Fixed(0))
			}
		}
		if lim.Sign() <= 0 {
			if !idx[key] {
				missing[parts[2]+" ("+parts[0]+" "+parts[1]+")"] = true
			}
			return
		}
		pc := limPct(open, lim)
		sort.Strings(uccs)
		v.Limits = append(v.Limits, PositionLimitRow{Level: level, Entity: entity, UCCs: uccs, Market: parts[0], Segment: parts[1], Symbol: parts[2],
			Long: s.long, Short: s.short, Open: open, Limit: lim, Basis: basis, Pct: pc, Status: limitStatus(pc, in.lim.WarnPct)})
	}
	for _, k := range sortedKeys(ent) {
		s := ent[k]
		parts := strings.SplitN(k, "|", 3)
		e := parts[1]
		if strings.HasPrefix(e, "PAN:") {
			e = MaskPAN(strings.TrimPrefix(e, "PAN:"))
		}
		row(parts[0], e, s.uccs, parts[2], s, s.long.Add(s.short))
	}
	for _, k := range sortedKeys(member) {
		s := member[k]
		open := s.long.Add(s.short)
		if strings.SplitN(k, "|", 3)[1] == "COM" {
			// Commodities: longs and shorts added separately, the higher
			// (SEBI master circular para 3.4.1(iv)).
			open = money.Max(s.long, s.short)
		}
		row("Member", "Member", nil, k, s, open)
	}
	// Connected clients (dpsurv.go) whose positions together pass the
	// client limit while each code is within it: a possible split across
	// codes (para 3.4.2).
	for _, g := range o.connectedGroupsSys(ctx, tenant) {
		comb := map[string]*side{}
		for _, u := range g.Members {
			for k, s := range perUCC {
				if strings.HasPrefix(k, u+"|") {
					key := strings.TrimPrefix(k, u+"|")
					c := comb[key]
					if c == nil {
						c = &side{}
						comb[key] = c
					}
					c.long, c.short = c.long.Add(s.long), c.short.Add(s.short)
					c.uccs = append(c.uccs, u)
				}
			}
		}
		for _, key := range sortedKeys(comb) {
			c := comb[key]
			if len(c.uccs) < 2 {
				continue
			}
			before := len(v.Limits)
			row("Connected group", g.ID, c.uccs, key, c, c.long.Add(c.short))
			if len(v.Limits) == before {
				continue
			}
			r := &v.Limits[len(v.Limits)-1]
			alone := true
			for _, x := range v.Limits[:before] {
				if (x.Level == "Client" || x.Level == "Proprietary") && x.Status == "Breach" && x.Symbol == r.Symbol && x.Market == r.Market && overlaps(x.UCCs, c.uccs) {
					alone = false
				}
			}
			switch {
			case r.Status == "OK":
				v.Limits = v.Limits[:before] // only groups at or near the limit are shown
			case alone:
				r.Note = "Each code is within the client limit but together they are not: a possible split of one position across codes (" + strings.Join(g.Links, "; ") + ")."
			default:
				r.Note = "A member of the group is over the client limit on its own."
			}
		}
	}
	v.Missing = sortedKeys(missing)
	sort.SliceStable(v.Limits, func(i, j int) bool { return v.Limits[i].Pct > v.Limits[j].Pct })
}

func overlaps(a, b []string) bool {
	for _, x := range a {
		if contains(b, x) {
			return true
		}
	}
	return false
}

// indexLimitRows fills the index futures and index options rows.
func (o *Ops) indexLimitRows(ctx context.Context, tenant string, in riskInput, pos map[string][]PositionView, limits map[string]PositionLimit, prop map[string]bool, v *RiskLimitsView) {
	crore := money.DecInt(10_000_000)
	type acc struct {
		fut, net, long, short money.Dec
		uccs                  []string
		level, entity, pan    string
	}
	futs := map[string]*acc{} // entity|index (futures: each client code, and the firm's own)
	opts := map[string]*acc{} // PAN|index (options)
	get := func(m map[string]*acc, k, level, entity, pan, u string) *acc {
		a := m[k]
		if a == nil {
			a = &acc{level: level, entity: entity, pan: pan}
			m[k] = a
		}
		if !contains(a.uccs, u) {
			a.uccs = append(a.uccs, u)
		}
		return a
	}
	exchs := map[string][]string{}
	for _, u := range sortedKeys(pos) {
		c := in.clients[u]
		level, entity, pan := "Client", u, c.PAN
		if prop[u] {
			level, entity = "Proprietary", "Own account"
		}
		for _, p := range pos[u] {
			if !p.Index || p.Segment == "CM" {
				continue
			}
			sym := strings.SplitN(p.Instrument, "|", 2)[0]
			if !contains(exchs[sym], p.Exchange) {
				exchs[sym] = append(exchs[sym], p.Exchange)
			}
			if p.Type == "FUT" {
				a := get(futs, level+"|"+entity+"|"+sym, level, entity, pan, u)
				a.fut = a.fut.Add(p.FutEq.Abs())
				continue
			}
			pk := firstOf(pan, u)
			if level == "Proprietary" {
				pk = "OWN"
			}
			a := get(opts, pk+"|"+sym, level, firstOf(pan, u), pan, u)
			if level == "Proprietary" {
				a.entity = "Own account"
			}
			a.net = a.net.Add(p.FutEq)
			if p.FutEq.Sign() > 0 {
				a.long = a.long.Add(p.FutEq)
			} else {
				a.short = a.short.Add(p.FutEq.Neg())
			}
		}
	}
	oiOf := func(sym string) money.Dec {
		for _, e := range exchs[sym] {
			if l, ok := limits[e+"|FO|"+sym]; ok && l.OIValue.Sign() > 0 {
				return l.OIValue
			}
		}
		return money.Dec{}
	}
	maskEnt := func(a *acc) string {
		if a.level == "Proprietary" {
			return "Own account"
		}
		if a.pan != "" && a.entity == a.pan {
			return MaskPAN(a.pan)
		}
		return a.entity
	}
	for _, k := range sortedKeys(futs) {
		a := futs[k]
		sym := k[strings.LastIndex(k, "|")+1:]
		floor := crore.MulInt(in.lim.IndexFutFloorCr)
		lim, basis := floor, fmt.Sprintf("₹%d crore", in.lim.IndexFutFloorCr)
		if oi := oiOf(sym); oi.Sign() > 0 {
			if x := oi.Pct(money.DecInt(in.lim.IndexFutOIPct)); x.Cmp(floor) > 0 {
				lim, basis = x, fmt.Sprintf("%d%% of index futures open interest ₹%s", in.lim.IndexFutOIPct, formatINR(oi))
			}
		} else {
			basis += " (no open interest loaded: the floor, the lower of the two)"
		}
		pc := limPct(a.fut, lim)
		v.Index = append(v.Index, IndexLimitRow{Kind: "Index futures gross", Level: a.level, Entity: maskEnt(a), PAN: MaskPAN(a.pan), UCCs: a.uccs, Index: sym, Value: a.fut, Limit: lim, Basis: basis, Status: limitStatus(pc, in.lim.WarnPct), pan: a.pan})
	}
	if len(opts) == 0 {
		return
	}
	// What may back a net beyond the limit, by PAN: cash and cash
	// equivalents (the ledger credit and cash-equivalent collateral) for a
	// net long; stock holdings at the close for a net short.
	funds, _ := o.clientFunds(ctx, tenant, in.date, "")
	coll := in.collateral()
	stocks := map[string]money.Dec{}
	for _, h := range in.holdings {
		if inst, ok := in.look.ByISIN(h.ISIN); ok && !in.cashEq[h.ISIN] {
			px := inst.Close
			if px.IsZero() {
				px = inst.LTP
			}
			stocks[h.UCC] = stocks[h.UCC].Add(h.Value(px))
		}
	}
	netLim, grossLim := crore.MulInt(in.lim.IndexOptNetCr), crore.MulInt(in.lim.IndexOptGrossCr)
	for _, k := range sortedKeys(opts) {
		a := opts[k]
		sym := k[strings.LastIndex(k, "|")+1:]
		base := IndexLimitRow{Level: a.level, Entity: maskEnt(a), PAN: MaskPAN(a.pan), UCCs: a.uccs, Index: sym, pan: a.pan}
		n := base
		n.Kind, n.Value, n.Limit, n.Basis = "Index options net", a.net.Abs(), netLim, fmt.Sprintf("₹%d crore net FutEq per PAN", in.lim.IndexOptNetCr)
		n.Status = limitStatus(limPct(n.Value, netLim), in.lim.WarnPct)
		if n.Status == "Breach" {
			n.Excess = n.Value.Sub(netLim)
			for _, u := range a.uccs {
				if a.net.Sign() > 0 {
					n.CoverKind = "Cash and cash equivalents"
					n.Cover = n.Cover.Add(money.Max(funds[u].Dec(), money.Dec{})).Add(coll[u].CashEq)
				} else {
					n.CoverKind = "Stock holdings"
					n.Cover = n.Cover.Add(stocks[u])
				}
			}
			if n.Cover.Cmp(n.Excess) >= 0 {
				n.Status = "Justified"
			}
		}
		v.Index = append(v.Index, n)
		for _, sd := range []struct {
			kind string
			val  money.Dec
		}{{"Index options long side", a.long}, {"Index options short side", a.short}} {
			g := base
			g.Kind, g.Value, g.Limit, g.Basis = sd.kind, sd.val, grossLim, fmt.Sprintf("₹%d crore gross FutEq on each side per PAN", in.lim.IndexOptGrossCr)
			g.Status = limitStatus(limPct(g.Value, grossLim), in.lim.WarnPct)
			v.Index = append(v.Index, g)
		}
	}
}

// banRows: each client's net FutEq in a banned security at the start and
// the end of the day, both at the day's deltas, for clients who traded it.
func banRows(in riskInput, pos map[string][]PositionView, banned map[string]string, v *RiskLimitsView) {
	if len(banned) == 0 {
		return
	}
	traded := map[string]int{}
	for _, t := range in.trades {
		if t.Segment != "CM" && banned[t.Symbol] != "" {
			traded[t.UCC+"|"+t.Symbol]++
		}
	}
	if len(traded) == 0 {
		return
	}
	start := in
	start.trades = nil
	sod, _ := start.positions("")
	net := func(m map[string][]PositionView, u, sym string) money.Dec {
		var x money.Dec
		for _, p := range m[u] {
			if p.Segment != "CM" && strings.SplitN(p.Instrument, "|", 2)[0] == sym {
				x = x.Add(p.FutEqQty)
			}
		}
		return x
	}
	for _, k := range sortedKeys(traded) {
		u, sym, _ := strings.Cut(k, "|")
		b := BanRow{UCC: u, Symbol: sym, Why: banned[sym], Start: net(sod, u, sym), End: net(pos, u, sym), Trades: traded[k]}
		b.Increased = b.End.Abs().Cmp(b.Start.Abs()) > 0
		v.Ban = append(v.Ban, b)
	}
}

// expirySpreads finds single-stock calendar spreads that involve a
// contract expiring on the date: opposite FutEq in the expiring contract
// and a later one of the same stock.
func expirySpreads(pos map[string][]PositionView, date string) []SpreadRow {
	out := []SpreadRow{}
	for _, u := range sortedKeys(pos) {
		type legs struct{ exp, later []PositionView }
		by := map[string]*legs{}
		for _, p := range pos[u] {
			if p.Index || p.Segment == "CM" {
				continue
			}
			parts := strings.Split(p.Instrument, "|")
			if len(parts) < 3 || parts[2] < date {
				continue
			}
			l := by[parts[0]]
			if l == nil {
				l = &legs{}
				by[parts[0]] = l
			}
			if parts[2] == date {
				l.exp = append(l.exp, p)
			} else {
				l.later = append(l.later, p)
			}
		}
		for _, sym := range sortedKeys(by) {
			l := by[sym]
			var e, f money.Dec
			for _, p := range l.exp {
				e = e.Add(p.FutEqQty)
			}
			for _, p := range l.later {
				f = f.Add(p.FutEqQty)
			}
			if len(l.exp) == 0 || len(l.later) == 0 || e.Sign() == 0 || f.Sign() == 0 || e.Sign() == f.Sign() {
				continue
			}
			r := SpreadRow{UCC: u, Symbol: sym}
			for _, p := range l.exp {
				r.Expiring = append(r.Expiring, p.Name)
			}
			for _, p := range l.later {
				r.Other = append(r.Other, p.Name)
			}
			out = append(out, r)
		}
	}
	return out
}

// survHits: intraday trades in trade-for-trade securities (bought and
// sold the same day, or an intraday product).
func (o *Ops) survHits(in riskInput, v *RiskLimitsView) {
	if len(in.surv) == 0 {
		return
	}
	type x struct {
		b, s int64
		mis  bool
		m    SurvMeasure
		ex   string
		sym  string
	}
	acc := map[string]*x{}
	for _, t := range in.trades {
		if t.Segment != "CM" {
			continue
		}
		isin := ""
		if i, ok := in.look.ForTrade(t); ok {
			isin = i.ISIN
		}
		m, ok := in.surv.find(t.Exchange, t.Symbol, isin)
		if !ok || !m.TradeForTrade {
			continue
		}
		k := t.UCC + "|" + t.Exchange + "|" + t.Symbol
		a := acc[k]
		if a == nil {
			a = &x{m: m, ex: t.Exchange, sym: t.Symbol}
			acc[k] = a
		}
		if t.Side == "B" {
			a.b += t.Qty
		} else {
			a.s += t.Qty
		}
		a.mis = a.mis || t.Product == "MIS"
	}
	for _, k := range sortedKeys(acc) {
		a := acc[k]
		why := ""
		switch {
		case a.mis:
			why = "intraday (MIS) trade in a trade-for-trade security"
		case a.b > 0 && a.s > 0:
			why = fmt.Sprintf("bought %d and sold %d the same day in a trade-for-trade security", a.b, a.s)
		default:
			continue
		}
		v.Surveillance = append(v.Surveillance, SurvHit{UCC: strings.SplitN(k, "|", 2)[0], Exchange: a.ex, Symbol: a.sym, Measure: strings.TrimSpace(a.m.Measure + " " + a.m.Stage), Why: why})
	}
}

// memberOI reads NSE Clearing's member open interest against its limits.
func (o *Ops) memberOI(ctx context.Context, tenant string, lim RiskLimitConfig, date string, v *RiskLimitsView) {
	snap, _, err := docs.Get[NCLMSnapshot](ctx, o.Docs, tenant, cNCLMSnap, "FO|"+date+"|moi")
	if err != nil {
		return
	}
	v.Sources = append(v.Sources, "NSE Clearing's member open interest ("+snap.At+")")
	for _, r := range snap.Rows {
		if len(r) < 5 {
			continue
		}
		oi, e1 := numCell(r[3])
		l, e2 := numCell(r[4])
		if e1 != nil || e2 != nil || l.Sign() <= 0 {
			continue
		}
		pc := limPct(oi, l)
		m := MemberOIRow{TM: r[0], Symbol: strings.ToUpper(strings.TrimSpace(r[1])), Type: r[2], OI: oi, Limit: l, Pct: pc, Status: limitStatus(pc, lim.WarnPct)}
		v.MemberOI = append(v.MemberOI, m)
		if m.Status == "Breach" {
			v.Breaches++
		}
	}
	sort.SliceStable(v.MemberOI, func(i, j int) bool { return v.MemberOI[i].Pct > v.MemberOI[j].Pct })
}

// ---- the checks and their work items ----

// RiskLimitRun is what a run of the end-of-day checks raised.
type RiskLimitRun struct {
	Date   string `json:"date"`
	Raised int    `json:"raised"`
	View   RiskLimitsView
}

// RunRiskLimitChecks raises the date's work items: each breach of a
// position or index limit, each ban-period trade that increased open
// interest, the index options report to the clearing corporation, each
// client to warn about the expiry-day spread rule, each trade-for-trade
// breach and each early pay-in square-off without margin.
func (o *Ops) RunRiskLimitChecks(ctx context.Context, tenant, actor, date string) (RiskLimitRun, error) {
	if actor != SystemActor {
		if err := o.require(ctx, tenant, actor, "Running the RMS limit checks", RoleRisk, RoleCompliance); err != nil {
			return RiskLimitRun{}, err
		}
	}
	v, err := o.RiskLimits(ctx, tenant, date)
	if err != nil {
		return RiskLimitRun{}, err
	}
	run := RiskLimitRun{Date: date, View: v}
	raise := func(process, key, title, detail, ucc string) {
		o.raiseWork(ctx, tenant, process, date+"|"+key, title, detail, ucc, RoleRisk, "rms", date)
		run.Raised++
	}
	for _, r := range v.Limits {
		if r.Status != "Breach" {
			continue
		}
		ucc := ""
		if len(r.UCCs) == 1 {
			ucc = r.UCCs[0]
		}
		what := "Stop fresh positions in " + r.Symbol + " on the trading front end (square-off only) and have the position reduced within the limit."
		if r.Level == "Member" {
			what = "The firm as a whole is over the limit: allow no fresh positions in " + r.Symbol + " for any client or the firm's own account, and reduce the open position."
		}
		raise(wPosLimits, r.Level+"|"+r.Entity+"|"+r.Market+"|"+r.Segment+"|"+r.Symbol,
			fmt.Sprintf("%s position limit breached: %s %s %s (%.0f%%)", r.Level, r.Entity, r.Symbol, r.Segment, r.Pct),
			fmt.Sprintf("Open position %s against a limit of %s (%s), across %s; codes %s. %s %s", r.Open.Fixed(0), r.Limit.Fixed(0), r.Basis, r.Market, strings.Join(r.UCCs, ", "), what, r.Note), ucc)
	}
	for _, r := range v.MemberOI {
		if r.Status == "Breach" {
			raise(wPosLimits, "NCL|"+r.TM+"|"+r.Symbol+"|"+r.Type, fmt.Sprintf("NSE Clearing member open interest over its limit: %s %s (%.0f%%)", r.Symbol, r.Type, r.Pct),
				fmt.Sprintf("Member %s has open interest %s against NSE Clearing's limit %s. Allow no fresh positions and reduce the open interest.", r.TM, r.OI.Fixed(0), r.Limit.Fixed(0)), "")
		}
	}
	report := false
	for _, r := range v.Index {
		switch r.Status {
		case "Breach":
			ucc := ""
			if len(r.UCCs) == 1 {
				ucc = r.UCCs[0]
			}
			raise(wIndexLimits, r.Kind+"|"+firstOf(r.pan, r.Entity)+"|"+r.Index, fmt.Sprintf("%s over the limit: %s %s", r.Kind, r.Entity, r.Index),
				fmt.Sprintf("₹%s against ₹%s (%s); codes %s. Stop fresh positions that add to it and have it reduced. %s", formatINR(r.Value), formatINR(r.Limit), r.Basis, strings.Join(r.UCCs, ", "), coverNote(r)), ucc)
			if r.Kind == "Index options net" {
				report = true
			}
		case "Justified":
			report = true
		}
	}
	if report {
		raise(wIndexReport, "report", "Report the holdings backing index option positions beyond the limit to the clearing corporation",
			"Some PANs' end-of-day index options FutEq is beyond the net limit. Download the index options cover report on RMS → Position limits and submit it to the clearing corporation within its timeline; without the backing, the clearing corporation levies its penalty or surveillance deposit.", "")
	}
	for _, b := range v.Ban {
		if b.Increased {
			raise(wFOBan, b.UCC+"|"+b.Symbol, fmt.Sprintf("Ban period: %s increased open interest in %s", b.UCC, b.Symbol),
				fmt.Sprintf("%s is in the F&O ban period (%s). The client's net FutEq went from %s to %s with %d trades today: in the ban period only trades that reduce it are allowed. Find how the orders passed the front end, square off the increase and record the action; the exchange may levy a penalty.", b.Symbol, b.Why, b.Start.Fixed(0), b.End.Fixed(0), b.Trades), b.UCC)
		}
	}
	for _, s := range v.Spreads {
		raise(wExpirySprd, s.UCC+"|"+s.Symbol, fmt.Sprintf("Expiry day: tell %s there is no spread benefit on %s", s.UCC, s.Symbol),
			fmt.Sprintf("On expiry day a single-stock calendar spread that includes the expiring contract gets no margin offset: %s against %s. Ledgerline margins each leg in full; check the trading front end does too, collect the full margin and tell the client.", strings.Join(s.Expiring, ", "), strings.Join(s.Other, ", ")), s.UCC)
	}
	for _, h := range v.Surveillance {
		raise(wSurvT2T, h.UCC+"|"+h.Exchange+"|"+h.Symbol, fmt.Sprintf("Trade-for-trade breach: %s in %s (%s)", h.UCC, h.Symbol, h.Measure),
			fmt.Sprintf("%s is under %s on %s: %s. Block intraday trading in it on the trading front end and review the trades.", h.Symbol, h.Measure, h.Exchange, h.Why), h.UCC)
	}
	if live, err := o.LiveRisk(ctx, tenant, date, ""); err == nil {
		for _, r := range live.Rows {
			if r.EPIRule != "" {
				raise(wEPIMargin, r.UCC, "Early pay-in sale squared off or new position without margin: "+r.UCC, r.EPIRule, r.UCC)
			}
		}
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cRiskLimRun, date, struct{ At string }{o.now().Format(time.RFC3339)}, actor)
	o.log(ctx, tenant, actor, "rms.limits.checked", date, map[string]any{"raised": run.Raised, "breaches": v.Breaches})
	return run, nil
}

func coverNote(r IndexLimitRow) string {
	if r.CoverKind == "" {
		return ""
	}
	return fmt.Sprintf("%s on record: ₹%s against an excess of ₹%s.", r.CoverKind, formatINR(r.Cover), formatINR(r.Excess))
}

// IndexCoverReport is the clearing-member report of the PANs whose index
// options net FutEq is beyond the limit, with the holdings that back it.
// No layout is published to Ledgerline: a plain CSV with what the
// circular asks for (para 5.5.4.2-5.5.4.3), for the clearing
// corporation's upload.
func (o *Ops) IndexCoverReport(ctx context.Context, tenant, actor, date string) ([]byte, int, error) {
	if err := o.require(ctx, tenant, actor, "Downloading the index options cover report", RoleRisk, RoleCompliance); err != nil {
		return nil, 0, err
	}
	v, err := o.RiskLimits(ctx, tenant, date)
	if err != nil {
		return nil, 0, err
	}
	rows := [][]string{{"Date", "PAN", "ClientCodes", "Index", "Side", "NetFutEq", "Limit", "Excess", "BackedBy", "BackingValue", "FullyBacked"}}
	n := 0
	for _, r := range v.Index {
		if r.Kind != "Index options net" || (r.Status != "Breach" && r.Status != "Justified") {
			continue
		}
		sd := "Long"
		if r.CoverKind == "Stock holdings" {
			sd = "Short"
		}
		full := "N"
		if r.Status == "Justified" {
			full = "Y"
		}
		rows = append(rows, []string{date, firstOf(r.pan, r.Entity), strings.Join(r.UCCs, " "), r.Index, sd, r.Value.Fixed(2), r.Limit.Fixed(2), r.Excess.Fixed(2), r.CoverKind, r.Cover.Fixed(2), full})
		n++
	}
	o.log(ctx, tenant, actor, "rms.index.report", date, map[string]any{"rows": n})
	return csvBytesOf(rows), n, nil
}

// riskLimitsTick runs the end-of-day checks once a business day after
// 18:00 IST (the day's trade files are in by then); RMS → Position limits
// runs them again on demand.
func (o *Ops) riskLimitsTick(ctx context.Context, tenant string, now time.Time) {
	now = now.In(ist)
	today := now.Format("2006-01-02")
	if now.Format("15:04") < "18:00" {
		return
	}
	set, err := o.Settings(ctx, tenant)
	if err != nil || !set.IsBusinessDay(today) {
		return
	}
	if _, _, err := docs.Get[struct{ At string }](ctx, o.Docs, tenant, cRiskLimRun, today); err == nil {
		return
	}
	_, _ = o.RunRiskLimitChecks(ctx, tenant, SystemActor, today)
}
