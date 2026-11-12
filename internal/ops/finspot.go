package ops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
	"ledgerline/internal/xfile"
)

// ---- Finspot uploads ----
//
// Finspot (the pilot broker's trading front end: OMS and RMS) takes its
// data as pipe-delimited files whose first line names the file type, in
// the layouts of Finspot's format workbook (config/formats/finspot-*.json):
//
//   - RMS Limits (finspot-rms-limit): the beginning-of-day values Finspot
//     computes the limit from (08:50, FinspotLimitFile in live.go: ledger
//     balance, cash-equivalent collateral as notional cash) and their
//     changes in the day (pay-out holds, payouthold.go);
//   - ISIN Holding (finspot-dp-holding, 08:54) and T1 - ISIN Holdings
//     (finspot-t1-holding): sellable holdings with margin-pledged non-cash
//     collateral, and the previous day's delivery buys not yet received;
//   - RMS Block/UnBlock (finspot-rms-block): clients who may not take new
//     positions;
//   - Investor Client, Investor Segment and Client Products Enabled
//     (finspot-investor-*): client logins, their segments and products;
//   - Client Information - Addition (finspot-client-info): the shorter,
//     older client addition upload, for the same clients;
//   - NFO_COST_PRICE (finspot-nfo-cost-price) and Dealer Client position
//     (finspot-dealer-position): carry-forward F&O positions with their
//     cost (finspot_fo.go);
//   - Dealer – Order Type Enable (finspot-order-types) and the NSE and BSE
//     Margin Basket (finspot-margin-basket-nse, -bse), from firm settings
//     and the day's VaR and ELM (finspot_fo.go).
//
// Finspot has no upload API that Ledgerline knows of: the files are
// downloaded on Live RMS → Front-office files and uploaded on Finspot.
// The firm-level values Finspot needs and Ledgerline does not otherwise
// hold (exchange user IDs per segment, the login role and type, dealer IDs,
// order types, margin basket product, names and minimum) are in Connections → Finspot uploads; a firm with
// that connection saved gets the BOD files on its daily file checklist.
// Passwords and transaction passwords are never written: Finspot sets them.
// Client account and user IDs are the UCC with the firm's account ID
// suffix (finspotCfg.ID).

const (
	connFinspot    = "finspot"
	cFinspotBlock  = "finspotblock" // UCC -> FinspotBlockState
	cFinspotSent   = "finspotsent"  // kind|UCC -> finspotSentState
	finspotTimeFmt = "03_04_05 PM"
)

var finspotMu sync.Mutex

func init() {
	ConnProviders = append(ConnProviders, ConnProvider{ID: connFinspot, Name: "Finspot uploads (trading front end)", Group: "Trading", Test: "none",
		Purpose: "What Finspot's client and segment uploads need from the firm. Finspot takes files (no API): download them on Live RMS → Front-office files. Saving this connection also puts the Finspot BOD limit and holding files on the daily file checklist",
		Fields: []ConnField{
			{Key: "exchange_users", Label: "Exchange user ID and user info per Finspot segment (e.g. NSE=9001:100000000001; NFO=9003:100000000003; BSE=…), optionally :dd/mm/yyyy expiry", Optional: true},
			{Key: "segment_expiry", Label: "Segment expiry date (dd/mm/yyyy), when not given per segment", Optional: true},
			{Key: "user_role", Label: "User role name for client logins (default INVESTOR)", Optional: true, Placeholder: "INVESTOR"},
			{Key: "client_type", Label: "Client type (default Retail)", Optional: true, Placeholder: "Retail"},
			{Key: "client_type2", Label: "Second client type (default Online-Sqroff)", Optional: true, Placeholder: "Online-Sqroff"},
			{Key: "access_type", Label: "User access type (default TWS)", Optional: true, Placeholder: "TWS"},
			{Key: "dealer_ids", Label: "Finspot dealer login IDs, comma-separated (for the dealer order type file)", Optional: true},
			{Key: "order_types", Label: "Order types dealers may place (default LMT,MKT,SL-LMT,SL-MKT,DS)", Optional: true, Placeholder: "LMT,MKT,SL-LMT,SL-MKT,DS"},
			{Key: "position_dealer", Label: "Dealer the client position file puts positions under (default: the first dealer ID)", Optional: true},
			{Key: "basket_product", Label: "Margin basket product: I (MIS), C (CNC), H (CO), M (NRML), B (BO); default I", Optional: true, Placeholder: "I"},
			{Key: "nse_basket", Label: "NSE margin basket name (default NSEMIS)", Optional: true, Placeholder: "NSEMIS"},
			{Key: "bse_basket", Label: "BSE margin basket name (default BSEMIS)", Optional: true, Placeholder: "BSEMIS"},
			{Key: "basket_min_pct", Label: "Minimum margin % in the baskets (default 20)", Optional: true, Placeholder: "20"},
			{Key: "account_suffix", Label: "Account ID suffix: added to the UCC in every Finspot file that carries a client's account or user ID (e.g. -BJT, the broker's code on Finspot)", Optional: true, Placeholder: "-BJT"},
			{Key: "group_validation", Label: "Group wise validation indicator in the RMS limit lines: no or yes (default no)", Optional: true, Placeholder: "no"},
			{Key: "cash_eq_isins", Label: "Cash-equivalent collateral ISINs besides government securities (liquid and overnight fund units, liquid ETFs), comma-separated: their pledged value goes in Notional cash, not in the ISIN Holding collateral", Optional: true},
			fNotes}})
}

// finspotSegUser is the firm's exchange user for one Finspot segment.
type finspotSegUser struct{ ID, Info, Expiry string }

// finspotCfg is the Finspot connection's values.
type finspotCfg struct {
	Set                                 bool
	Users                               map[string]finspotSegUser
	Role, ClientType, Type2, AccessTyp  string
	Dealers                             []string
	OrderTypes, PosDealer               string
	BasketProduct, NSEBasket, BSEBasket string
	BasketMin                           money.Dec
	Suffix                              string // added to the UCC in client account/user IDs
	GroupValidation                     string // RMS limit column 17: no or yes
}

// ID is a client's account / user ID on Finspot: the UCC and the firm's suffix.
func (c finspotCfg) ID(ucc string) string { return ucc + c.Suffix }

func (o *Ops) finspotConfig(ctx context.Context, tenant string) (finspotCfg, error) {
	c, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connFinspot)
	cfg := finspotCfg{Users: map[string]finspotSegUser{}}
	if err != nil && !errors.Is(err, docs.ErrNotFound) {
		return cfg, err
	}
	v := c.Values
	cfg.Set = err == nil && c.Status != "Not set"
	cfg.Role = firstOf(strings.TrimSpace(v["user_role"]), "INVESTOR")
	cfg.ClientType = firstOf(strings.TrimSpace(v["client_type"]), "Retail")
	cfg.Type2 = firstOf(strings.TrimSpace(v["client_type2"]), "Online-Sqroff")
	cfg.AccessTyp = firstOf(strings.TrimSpace(v["access_type"]), "TWS")
	for _, d := range strings.FieldsFunc(v["dealer_ids"], func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '\n' }) {
		cfg.Dealers = append(cfg.Dealers, strings.ToUpper(d))
	}
	cfg.OrderTypes = strings.ReplaceAll(firstOf(strings.TrimSpace(v["order_types"]), "LMT,MKT,SL-LMT,SL-MKT,DS"), " ", "")
	cfg.PosDealer = strings.ToUpper(strings.TrimSpace(v["position_dealer"]))
	if cfg.PosDealer == "" && len(cfg.Dealers) > 0 {
		cfg.PosDealer = cfg.Dealers[0]
	}
	cfg.BasketProduct = strings.ToUpper(firstOf(strings.TrimSpace(v["basket_product"]), "I"))
	if !strings.Contains("ICHMB", cfg.BasketProduct) || len(cfg.BasketProduct) != 1 {
		return cfg, invalid("the margin basket product is I, C, H, M or B")
	}
	cfg.NSEBasket = firstOf(strings.TrimSpace(v["nse_basket"]), "NSEMIS")
	cfg.BSEBasket = firstOf(strings.TrimSpace(v["bse_basket"]), "BSEMIS")
	cfg.BasketMin = money.MustDec("20")
	if x := strings.TrimSpace(v["basket_min_pct"]); x != "" {
		d, err := money.ParseDec(x)
		if err != nil || d.Sign() < 0 || d.Cmp(money.DecInt(100)) > 0 {
			return cfg, invalid("the minimum margin %% is a number from 0 to 100")
		}
		cfg.BasketMin = d
	}
	cfg.Suffix = strings.TrimSpace(v["account_suffix"])
	if strings.ContainsAny(cfg.Suffix, "| \t\r\n") {
		return cfg, invalid("the Finspot account ID suffix has no spaces or pipes")
	}
	cfg.GroupValidation = strings.ToLower(firstOf(strings.TrimSpace(v["group_validation"]), "no"))
	if cfg.GroupValidation != "no" && cfg.GroupValidation != "yes" {
		return cfg, invalid("the group wise validation indicator is no or yes")
	}
	if _, bad := cashEqISINList(v["cash_eq_isins"]); len(bad) > 0 {
		return cfg, invalid("cash-equivalent ISINs: %s is not an ISIN (12 characters)", strings.Join(bad, ", "))
	}
	exp := strings.TrimSpace(v["segment_expiry"])
	for _, part := range strings.FieldsFunc(v["exchange_users"], func(r rune) bool { return r == ';' || r == ',' || r == '\n' }) {
		seg, rest, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			return cfg, invalid("Finspot exchange users: %q is not SEGMENT=user:info", strings.TrimSpace(part))
		}
		f := strings.Split(rest, ":")
		u := finspotSegUser{ID: strings.TrimSpace(f[0]), Expiry: exp}
		if len(f) > 1 {
			u.Info = strings.TrimSpace(f[1])
		}
		if len(f) > 2 && strings.TrimSpace(f[2]) != "" {
			u.Expiry = strings.TrimSpace(f[2])
		}
		cfg.Users[strings.ToUpper(strings.TrimSpace(seg))] = u
	}
	return cfg, nil
}

// finspotSegments maps the firm's exchange segments to Finspot's codes.
var finspotSegments = []struct{ Key, Code, Seg string }{
	{"NSE-CM", "NSE", "CM"}, {"BSE-CM", "BSE", "CM"},
	{"NSE-FO", "NFO", "FO"}, {"BSE-FO", "BFO", "FO"},
	{"NSE-CD", "CDS", "CD"}, {"BSE-CD", "BCD", "CD"},
	{"MCX-COM", "MCX", "COM"}, {"NCDEX-COM", "NCX", "COM"},
}

func finspotSpec(id string) (*xfile.Spec, error) {
	lib, err := XFiles()
	if err != nil {
		return nil, err
	}
	s, ok := lib.Get(id)
	if !ok {
		return nil, invalid("the Finspot layout %s is missing", id)
	}
	return s, nil
}

// finspotText makes a value safe in a pipe-delimited line: Finspot reads
// no quoting, so a pipe or line break becomes a space.
func finspotText(s string) string {
	return strings.Join(strings.Fields(strings.NewReplacer("|", " ", "\r", " ", "\n", " ").Replace(s)), " ")
}

// FinspotKinds are the Finspot files on /v1/frontoffice/{kind}.
var FinspotKinds = []string{"finspot-limits", "finspot-holdings", "finspot-t1-holdings", "finspot-blocks", "finspot-clients", "finspot-client-info", "finspot-client-segments", "finspot-client-products",
	"finspot-nfo-cost", "finspot-dealer-positions", "finspot-order-types", "finspot-margin-basket-nse", "finspot-margin-basket-bse"}

// FinspotFile writes one Finspot upload for a trading date. all (client
// files only) writes every active client instead of the new and changed.
func (o *Ops) FinspotFile(ctx context.Context, tenant, actor, kind, date string, all bool) (string, []byte, error) {
	switch kind {
	case "finspot-limits":
		return o.FinspotLimitFile(ctx, tenant, date)
	case "finspot-holdings":
		return o.FinspotHoldingFile(ctx, tenant, date, false)
	case "finspot-t1-holdings":
		return o.FinspotHoldingFile(ctx, tenant, date, true)
	case "finspot-blocks":
		return o.FinspotBlockFile(ctx, tenant, actor, date)
	case "finspot-clients", "finspot-client-info", "finspot-client-segments", "finspot-client-products":
		return o.FinspotClientFile(ctx, tenant, actor, strings.TrimPrefix(kind, "finspot-"), date, all)
	case "finspot-nfo-cost":
		return o.FinspotCostFile(ctx, tenant, date)
	case "finspot-dealer-positions":
		return o.FinspotDealerPositionFile(ctx, tenant, date)
	case "finspot-order-types":
		return o.FinspotOrderTypeFile(ctx, tenant, date)
	case "finspot-margin-basket-nse":
		return o.FinspotMarginBasketFile(ctx, tenant, "NSE", date)
	case "finspot-margin-basket-bse":
		return o.FinspotMarginBasketFile(ctx, tenant, "BSE", date)
	}
	return "", nil, fmt.Errorf("%w: no Finspot file %s", ErrNotFound, kind)
}

// ---- holdings ----

// FinspotHoldingFile is the beginning-of-day ISIN Holding upload (t1
// false) or the T1 - ISIN Holdings upload (t1 true). Holdings: the demat
// balance (margin-pledged shares included: the pledge is released when
// they are sold), less shares pledged to lenders and shares frozen at
// CDSL, plus unpaid purchases held in CUSPA; nothing for an account frozen
// for debits. T1: the previous business day's delivery buys less sells,
// never below 0. Collateral: a margin-pledged security that is not cash
// equivalent is collateral on its holding line (IS Collateral 1,
// Collateral Qty the pledged quantity, the haircut), so Finspot adds its
// value to the limit; cash-equivalent pledges are in the RMS limit line's
// notional cash already and are not collateral here, and a client who may
// not take new positions has no collateral (as the limit line has no
// notional cash). T1 lines have no collateral.
func (o *Ops) FinspotHoldingFile(ctx context.Context, tenant, date string, t1 bool) (string, []byte, error) {
	id := "finspot-dp-holding"
	if t1 {
		id = "finspot-t1-holding"
	}
	s, err := finspotSpec(id)
	if err != nil {
		return "", nil, err
	}
	set, _ := o.Settings(ctx, tenant)
	prev := set.PrevBusinessDay(date)
	cs, err := o.ClientMap(ctx, tenant)
	if err != nil {
		return "", nil, err
	}
	_, look, _ := o.Market(ctx, tenant, prev) // no market data: haircut 1, price 0
	qty := map[string]int64{}                 // UCC|ISIN
	pledged := map[string]int64{}             // UCC|ISIN: margin-pledged, not cash equivalent
	cfg, err := o.finspotConfig(ctx, tenant)
	if err != nil {
		return "", nil, err
	}
	if t1 {
		buys, err := o.t1Buys(ctx, tenant, prev, look)
		if err != nil {
			return "", nil, err
		}
		for k, b := range buys {
			qty[k] = b.Net
		}
	} else {
		lines, err := o.sellableDemat(ctx, tenant, date, cs)
		if err != nil {
			return "", nil, err
		}
		var pl []string
		for _, l := range lines {
			if l.Pledged > 0 {
				pl = append(pl, l.ISIN)
			}
		}
		cashEq := o.cashEquivalentISINs(ctx, tenant, pl)
		for k, l := range lines {
			qty[k] = l.Demat + l.CUSPA
			if l.Pledged > 0 && !cashEq[l.ISIN] && finspotBlockWhy(cs[l.UCC]) == "" {
				pledged[k] = min(l.Pledged, max(l.Demat, 0))
			}
		}
	}
	poa := map[string]string{}
	var rows []xfile.Row
	for _, k := range sortedKeys(qty) {
		q := qty[k]
		u, isin, _ := strings.Cut(k, "|")
		c, ok := cs[u]
		if q <= 0 || !ok || c.Status != "Active" {
			continue
		}
		if len(u) > 10 {
			return "", nil, invalid("client code %s is longer than Finspot's 10 characters", u)
		}
		if _, ok := poa[u]; !ok {
			poa[u] = o.finspotPOA(ctx, tenant, c)
		}
		hc, px := money.DecInt(1), money.Dec{}
		if i, ok := look.ByISIN(isin); ok {
			px = i.Close
			if px.IsZero() {
				px = i.LTP
			}
			if i.HaircutPct.Sign() > 0 {
				hc, _ = i.HaircutPct.Quo(money.DecInt(100))
				hc = money.Min(hc, money.DecInt(1))
			}
		}
		isColl, cq := "0", min(pledged[k], q)
		if cq > 0 {
			isColl = "1"
		}
		rows = append(rows, xfile.Row{Values: map[string]string{
			"AccountId": cfg.ID(u), "ISIN": isin, "HoldingsQty": fmt.Sprint(q), "IsCollateral": isColl, "CollateralMode": "1", "CollateralQty": fmt.Sprint(cq),
			"Haircut": trimDec(hc.Fixed(4)), "BrokerCollateralQty": "0", "DPHoldingsQty": "0", "ClosePrice": px.Fixed(3), "BeneficiaryHoldings": "0",
			"Product": "CNC", "ActualPrice": px.Fixed(3), "DPAccount": finspotText(normDematID(c.Demat.ID)), "POAFlag": poa[u]}})
	}
	b, err := s.Write(rows)
	if err != nil {
		return "", nil, err
	}
	name, err := s.FileName(xfile.Vars{"date": date})
	if err == nil {
		o.noteDailyFile(ctx, tenant, SystemActor, id, name, date)
	}
	return name, b, err
}

// finspotPOA is Y when the client has a DDPI or a POA for securities.
func (o *Ops) finspotPOA(ctx context.Context, tenant string, c Client) string {
	d := o.CDSLBODetailFor(ctx, tenant, c.UCC)
	if d.DDPIRegNo != "" || len(d.POALinks) > 0 || o.UCCDetailsFor(ctx, tenant, c.UCC).POASecurities == "Y" {
		return "Y"
	}
	return "N"
}

// trimDec drops trailing zeros after the point ("0.1700" → "0.17", "1.0000" → "1").
func trimDec(s string) string {
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return s
}

// dematLine is one client's sellable quantity of one ISIN for the front
// ends' holding files (Finspot's ISIN Holding, BOW's User Stock upload).
type dematLine struct {
	UCC, ISIN string
	Demat     int64     // demat balance (margin-pledged included) less lender pledges and CDSL freezes
	Pledged   int64     // margin-pledged, as the holding has it
	Cost      money.Dec // the holding's average cost (zero when unknown)
	CUSPA     int64     // unpaid purchases held in CUSPA, sellable too
	CUSPAVal  money.Dec // their value
}

// sellableDemat is every client's sellable demat quantity by UCC|ISIN, as
// of the morning of date: the demat balance (margin-pledged shares
// included: the pledge is released when they are sold), less shares
// pledged to lenders and shares frozen at CDSL, nothing for an account
// frozen for debits; and unpaid purchases held in CUSPA before date.
func (o *Ops) sellableDemat(ctx context.Context, tenant, date string, cs map[string]Client) (map[string]*dematLine, error) {
	hs, err := o.Holdings(ctx, tenant, "")
	if err != nil {
		return nil, err
	}
	enc := o.cplEncumberedAll(ctx, tenant)
	out := map[string]*dematLine{}
	line := func(u, isin string) *dematLine {
		k := u + "|" + isin
		if out[k] == nil {
			out[k] = &dematLine{UCC: u, ISIN: isin}
		}
		return out[k]
	}
	frozenBO := map[string]bool{}
	for _, h := range hs {
		c := cs[h.UCC]
		if _, seen := frozenBO[h.UCC]; !seen {
			frozenBO[h.UCC] = o.bo(ctx, tenant, c).debitFrozen()
		}
		if frozenBO[h.UCC] {
			continue
		}
		q := h.Qty - enc[h.UCC][h.ISIN]
		q -= o.cdslFrozenQty(ctx, tenant, c.Demat.ID, h.ISIN, q)
		l := line(h.UCC, h.ISIN)
		l.Demat += q
		l.Pledged += h.Pledged
		l.Cost = h.AvgCost
	}
	cu, _ := docs.List[CUSPAItem](ctx, o.Docs, tenant, cCUSPA, "")
	for _, it := range cu {
		if it.Status == "Held in CUSPA" && it.Date < date {
			l := line(it.UCC, it.ISIN)
			l.CUSPA += it.Qty
			l.CUSPAVal = l.CUSPAVal.Add(it.Value)
		}
	}
	return out, nil
}

// t1Buy is one client's delivery trades of one ISIN on a day.
type t1Buy struct {
	Net    int64     // buys less sells (may be negative)
	Bought int64     // bought
	Value  money.Dec // value bought
}

// t1Buys are a day's delivery (CNC) cash-market trades by UCC|ISIN: the
// T1 quantity the front ends let a client sell before pay-out. Trades whose
// instrument has no ISIN in the market data are left out (the uploads are
// by ISIN).
func (o *Ops) t1Buys(ctx context.Context, tenant, day string, look Lookup) (map[string]*t1Buy, error) {
	ts, err := o.exchangeTrades(ctx, tenant, day)
	if err != nil {
		return nil, err
	}
	out := map[string]*t1Buy{}
	for _, t := range ts {
		if t.Segment != "CM" || t.Product != "CNC" {
			continue
		}
		isin := ""
		if i, ok := look.ForTrade(t); ok {
			isin = i.ISIN
		}
		if isin == "" {
			continue
		}
		k := t.UCC + "|" + isin
		if out[k] == nil {
			out[k] = &t1Buy{}
		}
		b := out[k]
		if t.Side == "B" {
			b.Net += t.Qty
			b.Bought += t.Qty
			b.Value = b.Value.Add(t.Price.MulInt(t.Qty))
		} else {
			b.Net -= t.Qty
		}
	}
	return out, nil
}

// cplEncumberedAll is cplEncumbered for every client: UCC -> ISIN -> qty.
func (o *Ops) cplEncumberedAll(ctx context.Context, tenant string) map[string]map[string]int64 {
	out := map[string]map[string]int64{}
	rel := map[string]int64{}
	for _, r := range o.cplReleases(ctx, tenant) {
		if r.Of == "EP" && r.Status == "Done" {
			rel[r.UCC+"|"+r.PSN] += r.Qty
		}
	}
	for _, n := range o.cplNormals(ctx, tenant) {
		if n.Status == "Rejected" || n.Status == "Failed" {
			continue
		}
		for _, l := range n.Lines {
			if l.Status == "Failed" {
				continue
			}
			if out[n.UCC] == nil {
				out[n.UCC] = map[string]int64{}
			}
			out[n.UCC][l.ISIN] += max(l.Qty-rel[n.UCC+"|"+l.PSN], 0)
		}
	}
	return out
}

// ---- blocks ----

// FinspotBlockState is what the last block file said about a client.
type FinspotBlockState struct {
	UCC     string `json:"ucc"`
	Blocked bool   `json:"blocked"`
	Reason  string `json:"reason,omitempty"`
	On      string `json:"on"` // date of the file that last changed it
}

// finspotBlockWhy says why a client may not take new positions ("" = may).
func finspotBlockWhy(c Client) string {
	switch {
	case c.Status != "Active":
		return "Account not active"
	case c.Inactive:
		return "Inactive account"
	case c.Blocked:
		return firstOf(c.BlockedReason, "Blocked")
	case len(c.OpenSegments()) == 0:
		return "No segment active"
	}
	return ""
}

// FinspotBlockFile is the block / unblock upload: a block line for every
// client blocked now (so the full list is re-sent and a day-only block is
// set again), and an unblock line for every client an earlier file blocked
// who is no longer blocked (in every file of the day the change is first
// written, so a file made again the same day still has it). Clients
// Ledgerline never blocked are never unblocked: a block set by hand on
// Finspot stays. A client who was never active (still onboarding) is not
// in the file; one who was active and no longer is stays blocked.
func (o *Ops) FinspotBlockFile(ctx context.Context, tenant, actor, date string) (string, []byte, error) {
	if err := o.require(ctx, tenant, actor, "Downloading Finspot block and client files", RoleMaker, RoleChecker, RoleRisk); err != nil {
		return "", nil, err
	}
	if !validDate(date) {
		return "", nil, invalid("give a date (YYYY-MM-DD)")
	}
	s, err := finspotSpec("finspot-rms-block")
	if err != nil {
		return "", nil, err
	}
	finspotMu.Lock()
	defer finspotMu.Unlock()
	cs, err := o.ClientMap(ctx, tenant)
	if err != nil {
		return "", nil, err
	}
	prior, err := docs.List[FinspotBlockState](ctx, o.Docs, tenant, cFinspotBlock, "")
	if err != nil {
		return "", nil, err
	}
	st := map[string]FinspotBlockState{}
	for _, p := range prior {
		st[p.UCC] = p
	}
	cfg, err := o.finspotConfig(ctx, tenant)
	if err != nil {
		return "", nil, err
	}
	sent, _ := docs.List[finspotSentState](ctx, o.Docs, tenant, cFinspotSent, "clients|")
	onFinspot := map[string]bool{}
	for _, x := range sent {
		onFinspot[x.UCC] = true
	}
	var rows []xfile.Row
	line := func(u, side, product, block, why string) {
		rows = append(rows, xfile.Row{Fields: 22, Values: map[string]string{"Client": cfg.ID(u), "Side": side, "Products": product, "BlockType": "NON-SQROFF", "Block": block, "Remarks": finspotRemark(why)}})
	}
	lines := func(u, block, why string) {
		line(u, "", "MIS", block, why)
		line(u, "", "NRML", block, why)
		line(u, "B", "CNC", block, why)
	}
	var changed []FinspotBlockState
	for _, u := range sortedKeys(cs) {
		c := cs[u]
		why := finspotBlockWhy(c)
		was, known := st[u]
		if c.Status != "Active" && !known && !onFinspot[u] {
			continue // never on Finspot
		}
		if len(u) > 10 {
			return "", nil, invalid("client code %s is longer than Finspot's 10 characters", u)
		}
		switch {
		case why != "":
			lines(u, "Y", why)
			if !was.Blocked || was.Reason != why {
				changed = append(changed, FinspotBlockState{UCC: u, Blocked: true, Reason: why, On: date})
			}
		case known && was.Blocked:
			lines(u, "N", "Block lifted")
			changed = append(changed, FinspotBlockState{UCC: u, Blocked: false, On: date})
		case known && was.On == date:
			lines(u, "N", "Block lifted") // unblocked earlier today: written again
		}
	}
	b, err := s.Write(rows)
	if err != nil {
		return "", nil, err
	}
	for _, x := range changed {
		if err := docs.Upsert(ctx, o.Docs, tenant, cFinspotBlock, x.UCC, x, actor); err != nil {
			return "", nil, err
		}
	}
	name, err := s.FileName(xfile.Vars{"date": date, "time": o.now().In(ist).Format(finspotTimeFmt)})
	if err == nil {
		o.noteDailyFile(ctx, tenant, actor, "finspot-rms-block", name, date)
	}
	return name, b, err
}

// finspotRemark is a short remark: Finspot's sample remarks are a few words.
func finspotRemark(s string) string {
	s = finspotText(s)
	if r := []rune(s); len(r) > 50 {
		s = string(r[:50])
	}
	return s
}

// ---- client logins ----

type finspotSentState struct {
	Kind string `json:"kind"`
	UCC  string `json:"ucc"`
	Hash string `json:"hash"`
	On   string `json:"on"`
}

// FinspotClientFile writes the client login (kind "clients"), segment
// ("client-segments") or product ("client-products") upload for the
// active clients that are new or changed since the last file of that kind
// (by what the file says about them), and those already written on the
// same date (so a file made again that day has them again); with all,
// every active client.
func (o *Ops) FinspotClientFile(ctx context.Context, tenant, actor, kind, date string, all bool) (string, []byte, error) {
	if err := o.require(ctx, tenant, actor, "Downloading Finspot block and client files", RoleMaker, RoleChecker, RoleRisk); err != nil {
		return "", nil, err
	}
	if !validDate(date) {
		return "", nil, invalid("give a date (YYYY-MM-DD)")
	}
	s, err := finspotSpec(finspotClientLayouts[kind])
	if err != nil {
		return "", nil, err
	}
	cfg, err := o.finspotConfig(ctx, tenant)
	if err != nil {
		return "", nil, err
	}
	set, _ := o.Settings(ctx, tenant)
	firm := map[string]bool{}
	for _, c := range set.activeSegments() {
		firm[c.Key] = true
	}
	finspotMu.Lock()
	defer finspotMu.Unlock()
	cs, err := o.ClientMap(ctx, tenant)
	if err != nil {
		return "", nil, err
	}
	prior, _ := docs.List[finspotSentState](ctx, o.Docs, tenant, cFinspotSent, kind+"|")
	last := map[string]finspotSentState{}
	for _, p := range prior {
		last[p.UCC] = p
	}
	var rows []xfile.Row
	var changed []finspotSentState
	var missing, long []string
	for _, u := range sortedKeys(cs) {
		c := cs[u]
		if c.Status != "Active" {
			continue
		}
		if len(u) > 10 {
			return "", nil, invalid("client code %s is longer than Finspot's 10 characters", u)
		}
		if kind == "client-segments" && len(cfg.ID(u)) > 10 { // Investor Segment: User id Char[10]
			long = append(long, cfg.ID(u))
			continue
		}
		var rs []xfile.Row
		switch kind {
		case "clients":
			rs = []xfile.Row{o.finspotClientRow(ctx, tenant, c, cfg)}
		case "client-info":
			full := o.finspotClientRow(ctx, tenant, c, cfg).Values
			v := map[string]string{}
			for _, k := range []string{"AccountId", "AccountName", "BranchId", "Email", "ResidenceAddress", "OfficeAddress", "Mobile", "ClientGroup", "DecimalPreference", "PAN", "ClientType", "ClientStatus"} {
				v[k] = full[k]
			}
			rs = []xfile.Row{{Values: v}}
		case "client-segments":
			for _, fs := range finspotSegments {
				if !firm[fs.Key] || !contains(c.Segments, fs.Seg) {
					continue
				}
				x, ok := cfg.Users[fs.Code]
				if !ok || x.ID == "" || x.Info == "" || x.Expiry == "" {
					if !contains(missing, fs.Code) {
						missing = append(missing, fs.Code)
					}
					continue
				}
				exp := x.Expiry
				if t, err := time.Parse("02/01/2006", exp); err == nil {
					exp = t.Format("2006-01-02")
				}
				en := "N"
				if c.SegmentOpen(fs.Seg) {
					en = "Y"
				}
				rs = append(rs, xfile.Row{Fields: 6, Values: map[string]string{"UserId": cfg.ID(u), "Segment": fs.Code, "ExchangeUserId": x.ID, "Expiry": exp, "ExchangeUserInfo": x.Info, "Enabled": en}})
			}
		case "client-products":
			if p := finspotProducts(c); p != "" {
				rs = []xfile.Row{{Values: map[string]string{"UserId": cfg.ID(u), "Products": p}}}
			}
		}
		if len(rs) == 0 {
			continue
		}
		h := finspotHash(rs)
		was, known := last[u]
		if !all && known && was.Hash == h && was.On != date {
			continue
		}
		rows = append(rows, rs...)
		if !known || was.Hash != h {
			changed = append(changed, finspotSentState{Kind: kind, UCC: u, Hash: h, On: date})
		}
	}
	if len(long) > 0 {
		return "", nil, invalid("Finspot's segment user ID holds 10 characters; with the account ID suffix %q these are longer: %s", cfg.Suffix, strings.Join(long, ", "))
	}
	if len(missing) > 0 {
		return "", nil, invalid("set the exchange user ID, user info and expiry for %s in Connections → Finspot uploads", strings.Join(missing, ", "))
	}
	b, err := s.Write(rows)
	if err != nil {
		return "", nil, err
	}
	for _, x := range changed {
		if err := docs.Upsert(ctx, o.Docs, tenant, cFinspotSent, kind+"|"+x.UCC, x, actor); err != nil {
			return "", nil, err
		}
	}
	name, err := s.FileName(xfile.Vars{"date": date})
	if err == nil {
		o.log(ctx, tenant, actor, "frontoffice.finspot_clients", name, map[string]any{"kind": kind, "lines": len(rows), "all": all})
	}
	return name, b, err
}

// finspotClientLayouts are the client files' layouts by kind.
var finspotClientLayouts = map[string]string{"clients": "finspot-investor-client", "client-info": "finspot-client-info", "client-segments": "finspot-investor-segment", "client-products": "finspot-investor-products"}

// finspotClientRow is a client's Investor Client line: account and login
// ID are the UCC with the firm's suffix. Passwords are never written.
func (o *Ops) finspotClientRow(ctx context.Context, tenant string, c Client, cfg finspotCfg) xfile.Row {
	d := o.UCCDetailsFor(ctx, tenant, c.UCC)
	var addr []string
	for _, p := range []string{d.Address1, d.Address2, d.Address3, firstOf(d.City, c.City), firstOf(d.State, c.State), d.PIN} {
		if strings.TrimSpace(p) != "" {
			addr = append(addr, strings.TrimSpace(p))
		}
	}
	branch := firstOf(strings.TrimSpace(c.Branch), "HO")
	demat := normDematID(c.Demat.ID)
	dpid := ""
	if len(demat) >= 8 {
		dpid = demat[:8]
	}
	v := map[string]string{
		"AccountId": cfg.ID(c.UCC), "AccountName": c.Name, "LoginId": cfg.ID(c.UCC), "BranchId": branch, "Email": c.Email,
		"ResidenceAddress": strings.Join(addr, ", "), "Mobile": c.Mobile, "ClientGroup": branch, "DPId": dpid, "DPAccount": demat,
		"DecimalPreference": "2", "PAN": c.PAN, "ClientType": cfg.ClientType, "BankAccount": c.Bank.Account, "BankName": c.Bank.Bank,
		"BankAddress": c.Bank.Branch, "ClientStatus": "A", "UserRole": cfg.Role, "ClientType2": cfg.Type2, "AccessType": cfg.AccessTyp,
		"DOB": c.DOB, "UserStatus": "E",
	}
	for k, x := range v {
		v[k] = finspotText(x)
	}
	if !validDate(v["DOB"]) {
		v["DOB"] = ""
	}
	return xfile.Row{Values: v}
}

// finspotProducts are the product codes a client may use: I (MIS) with any
// open segment, M (NRML) with an open derivatives segment, C (CNC) with
// equity cash open.
func finspotProducts(c Client) string {
	open := c.OpenSegments()
	if len(open) == 0 {
		return ""
	}
	p := []string{"I"}
	if contains(open, "FO") || contains(open, "CD") || contains(open, "COM") {
		p = append(p, "M")
	}
	if contains(open, "CM") {
		p = append(p, "C")
	}
	return strings.Join(p, ",")
}

func finspotHash(rs []xfile.Row) string {
	h := sha256.New()
	for _, r := range rs {
		for _, k := range sortedKeys(r.Values) {
			fmt.Fprintf(h, "%s=%s\x00", k, r.Values[k])
		}
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// finspotChecklist says whether the firm sends Finspot files (the
// connection is saved), for the daily file checklist.
func (o *Ops) finspotChecklist(ctx context.Context, tenant string) bool {
	cfg, _ := o.finspotConfig(ctx, tenant)
	return cfg.Set
}
