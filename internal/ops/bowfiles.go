package ops

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
	"ledgerline/internal/xfile"
)

// ---- BOW upload files ----
//
// The files the firm uploads in BOW (BOLTPlusOnWeb), in the layouts of
// BOW's format workbook (BOW File_Formats.xlsx; config/formats/bow-*.json).
// BOW's file uploader takes them from 17:30 to 08:00, except the user
// deposit and user stock files, which it also takes during market hours.
// No headers; a NOT NULL field in BOW's sheet must have a value.
//
// The two the pilot broker's current back office writes each morning (and
// whose shape Ledgerline copies) are here; every other upload of the
// workbook is in bowuploads.go:
//
//   - BOW_FUND_<dd-mm-yy hh_mm_ss AM>.csv (bow-fund, DEPOSIT_UPLOAD,
//     ~08:13): the clients' deposits in whole rupees. A deposit line has a
//     category, so the file separates equity and commodity money: by default
//     two lines a client, category securities with Securities and category
//     commodities with Commodities as bowLimits computes them for the API
//     (exchange and market blank: every exchange of the category), so
//     commodity money never raises the equity limit. The connection can add
//     a user-level line (category blank, their sum), or write the old back
//     office's single user-level line (Securities + Commodities, signed: a
//     debit in one ledger reduces the other). Debits are kept (negative); a
//     blocked, inactive or not active client 0. Every active client is
//     written, or with "changed" only the clients whose figures differ from
//     the last file (the broker's second, small morning file); what each
//     file said is kept per client (cBOWFundSent). A client no longer active
//     who was last sent a figure other than 0 gets 0 once. The lines stop at
//     20 fields, as the broker's, unless a Product is set (21).
//   - BOW_DP_Holding_Combined_Upload_<DDMMYYYY>.TXT (bow-dp-holding,
//     COMBINED DP UPLOAD WITH T1 POOL, ~08:17): one line per client per
//     security, the only route for holdings (BOW's API cannot write them).
//     DP quantity as Finspot's ISIN Holding (sellableDemat), pool quantity
//     the previous business day's delivery buys less sells (t1Buys, as
//     Finspot's T1 file); the DP average price is the holding's average cost
//     (Holding.AvgCost, from the DP holdings import and corporate actions;
//     0.00 when unknown, and for unpaid purchases in CUSPA alone their value
//     over quantity), the pool average price the average of the day's
//     delivery buys. Symbol and series from the NSE security master, the BSE
//     code from the BSE scrip file (SecRef); a security with neither is left
//     out and logged. The credit-for-sale haircuts (DPCFSHaircut,
//     PoolCFSHaircut) are the firm's values in Connections → BOW upload
//     files, 0 by default as in the broker's file; BOW's API document says a
//     pool haircut of 100 gives no credit for sale (the T1 use).
//
// The files are on Live RMS → Front-office files and on the BOW panel; the
// funds and holdings files are on the daily file checklist when the BOW
// limits API or BOW upload files connection is saved.

const (
	connBOWFiles = "bow-files"
	cBOWFundSent = "bowfundsent" // UCC -> bowFundSent

	bowShapeTwo    = "Two lines: securities and commodities"
	bowShapeThree  = "Two lines and a user-level line (their sum)"
	bowShapeSingle = "Single combined line (as the old back office)"

	bowCatSecurities  = "securities" // DEPOSIT_UPLOAD's category values, as BOW's samples spell them
	bowCatCommodities = "commodities"
)

var bowFileMu sync.Mutex

func init() {
	ConnProviders = append(ConnProviders, ConnProvider{ID: connBOWFiles, Name: "BOW upload files", Group: "Trading", Test: "none",
		Purpose: "The firm-level values BOW's upload files need (BOW File_Formats.xlsx), for a firm that uploads them at https://bow1uploads.bseindia.com or in the BOW Admin terminal: deposits, holdings (BOW's API cannot write them), users, trading rights, limits, brokerage and positions. Download them on Live RMS → Front-office files → BOW uploads. BOW's uploader takes files from 17:30 to 08:00, except deposits and stocks (also during market hours). Saving this connection puts the funds and holdings files on the daily file checklist. Fields are named as in BOW's sheets",
		Fields: []ConnField{
			{Key: "deposit_shape", Label: "Deposit (funds) file: lines per client (default two: securities and commodities, so commodity money never raises the equity limit)", Optional: true, Options: []string{bowShapeTwo, bowShapeThree, bowShapeSingle}},
			{Key: "deposit_product", Label: "Deposit file Product (Delivery, Intraday …; empty: left out, 20 fields as the broker's file)", Optional: true},
			{Key: "dp_haircut", Label: "DPCFSHaircut: credit-for-sale haircut % on DP holdings (0 to 100, default 0)", Optional: true, Placeholder: "0"},
			{Key: "pool_haircut", Label: "PoolCFSHaircut: credit-for-sale haircut % on pool (T1) holdings (0 to 100, default 0; 100 gives no credit for sale)", Optional: true, Placeholder: "0"},
			{Key: "owner_id", Label: "Owner Id for user additions: the admin ID (e.g. T1001) or mini admin ID (miniadmin-T1001)", Optional: true},
			{Key: "offline_groups", Label: "Client groups of offline (CTCL) clients, comma-separated: they go in the offline user file, everyone else in the IBT file", Optional: true, Placeholder: "OFFLINE"},
			{Key: "margin_product_pref", Label: "MarginProductPref for user additions (BOW's value, e.g. 1099511627775; empty: blank)", Optional: true},
			{Key: "delivery_product_pref", Label: "DeliveryProductPref for user additions (empty: blank)", Optional: true},
			{Key: "dealer_ids", Label: "BOW Dealer Id per Ledgerline dealer (DL-0001=DEALER1; …; default the dealer ID without the hyphen)", Optional: true},
			{Key: "client_dealers", Label: "Employee ID/Dealer Id every client is mapped to, comma-separated (besides the dealers of the client's authorised person)", Optional: true},
			{Key: "client_nnf", Label: "Client Trader Id and Location ID per exchange market (BSE EQUITY=123400901:1111111111111012; NSE DERIVATIVE=…)", Optional: true},
			{Key: "risk_preferences", Label: "Risk preferences: the nine Consider … values in BOW's order, 1 margin, 2 MTM, 3 both (e.g. 2,2,1,1,2,1,2,1,3)", Optional: true},
			{Key: "risk_scope", Label: "Risk preferences Exchange, Market, Product (default ALL,ALL,ALL)", Optional: true, Placeholder: "ALL,ALL,ALL"},
			{Key: "contract_margins", Label: "Contract margin lines: Exchange,Market,Segment=Intraday Margin:Delivery Margin (100 / leverage), e.g. BSE,EQUITY,EQUITY=20:100; MCX,COMMODITIES,COMMODITIES-FUTURES=100:100", Optional: true},
			{Key: "right_limits", Label: "Right limit lines: Category,EXCHANGE,MARKET,SEGMENT:Field=value,… e.g. Securities,BSE,EQ,EQ:MaxOrderValue=500000,TurnoverLimit=2500000", Optional: true},
			{Key: "profiles", Label: "Profile names: TR=…,CM=…,RL=…,UCON=…,MTM=…,IP=…,EXC=…", Optional: true},
			{Key: "brokerage_channel", Label: "Brokerage master: the channel BOW orders come through (default online)", Optional: true, Placeholder: "online"},
			{Key: "sell_dp_charge", Label: "Brokerage master: DP charge per delivery sell, ₹ (default 0)", Optional: true, Placeholder: "0"},
			{Key: "disclaimers", Label: "Brokerage master BM_Disclaimer_Message1 to 3, separated by | (50 characters each)", Optional: true},
			{Key: "position_product", Label: "Manual position file Product Type: 1 margin / intraday, 2 CNC / delivery (default 2)", Optional: true, Placeholder: "2"},
			fNotes}})
}

// bowFilesCfg is the BOW upload files connection's values.
type bowFilesCfg struct {
	Set                 bool
	Shape, Product      string
	DPHaircut, PoolHcut money.Dec
	v                   map[string]string // every value, for bowuploads.go
}

func (o *Ops) bowFilesConfig(ctx context.Context, tenant string) (bowFilesCfg, error) {
	c, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connBOWFiles)
	var cfg bowFilesCfg
	if err != nil && !errors.Is(err, docs.ErrNotFound) {
		return cfg, err
	}
	cfg.Set = err == nil && c.Status != "" && c.Status != "Not set"
	cfg.v = map[string]string{}
	for k, x := range c.Values {
		cfg.v[k] = strings.TrimSpace(x)
	}
	pct := func(key, what string) (money.Dec, error) {
		v := cfg.v[key]
		if v == "" {
			return money.Dec{}, nil
		}
		n, err := money.ParseDec(v)
		if err != nil || n.Sign() < 0 || n.Cmp(money.DecInt(100)) > 0 {
			return money.Dec{}, invalid("the %s haircut %% is a number from 0 to 100", what)
		}
		return n, nil
	}
	if cfg.DPHaircut, err = pct("dp_haircut", "DP"); err != nil {
		return cfg, err
	}
	if cfg.PoolHcut, err = pct("pool_haircut", "pool"); err != nil {
		return cfg, err
	}
	cfg.Shape = firstOf(cfg.v["deposit_shape"], bowShapeTwo)
	if cfg.Shape != bowShapeTwo && cfg.Shape != bowShapeThree && cfg.Shape != bowShapeSingle {
		return cfg, invalid("the deposit file shape is one of: %s; %s; %s", bowShapeTwo, bowShapeThree, bowShapeSingle)
	}
	cfg.Product = cfg.v["deposit_product"]
	if strings.ContainsAny(cfg.Product, ",\"\r\n") {
		return cfg, invalid("the deposit Product has no commas or quotes")
	}
	return cfg, nil
}

// bowFilesChecklist says whether the firm uses BOW (the limits API or the
// upload files connection is saved), for the daily file checklist.
func (o *Ops) bowFilesChecklist(ctx context.Context, tenant string) bool {
	if cfg, _ := o.bowFilesConfig(ctx, tenant); cfg.Set {
		return true
	}
	c, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connBOW)
	return err == nil && c.Status != "" && c.Status != "Not set"
}

// BOWKinds are the BOW files on /v1/frontoffice/{kind}: the funds and
// holdings files, then every other upload (bowuploads.go; the kind is the
// layout's id).
var BOWKinds = append([]string{"bow-funds-file", "bow-holdings"}, bowUploadKinds...)

// BOWUpload is one BOW upload file, with a note when something was left
// out or there was nothing to send.
type BOWUpload struct {
	Name  string
	Data  []byte
	Lines int
	Note  string
}

// BOWFile writes one BOW upload for a trading date; changed (funds only)
// writes only the clients whose figure changed since the last file.
func (o *Ops) BOWFile(ctx context.Context, tenant, actor, kind, date string, changed bool) (string, []byte, error) {
	u, err := o.BOWUploadFile(ctx, tenant, actor, kind, date, changed, false)
	return u.Name, u.Data, err
}

// BOWUploadFile writes any BOW upload: changed (funds) only the clients
// whose figures changed; all (the client files of bowuploads.go) every
// client rather than the new and changed.
func (o *Ops) BOWUploadFile(ctx context.Context, tenant, actor, kind, date string, changed, all bool) (BOWUpload, error) {
	switch kind {
	case "bow-funds-file":
		name, b, n, err := o.bowFundFile(ctx, tenant, actor, date, changed)
		u := BOWUpload{Name: name, Data: b, Lines: n}
		if err == nil && n == 0 {
			u.Note = "Nothing to send: no client's deposit changed since the last file"
		}
		return u, err
	case "bow-holdings":
		name, b, err := o.BOWHoldingFile(ctx, tenant, date)
		u := BOWUpload{Name: name, Data: b}
		if err == nil && len(b) == 0 {
			u.Note = "Nothing to send: no active client has a DP or pool quantity"
		}
		return u, err
	}
	if contains(bowUploadKinds, kind) {
		return o.bowUpload(ctx, tenant, actor, kind, date, all)
	}
	return BOWUpload{}, fmt.Errorf("%w: no BOW file %s", ErrNotFound, kind)
}

// bowFundSent is what the last BOW funds file said about a client.
type bowFundSent struct {
	UCC   string `json:"ucc"`
	Funds int64  `json:"funds"` // rupees: the user-level (combined) figure
	// Sec and Com are the securities and commodities lines' figures, and
	// Shape the file's shape, when the file had category lines.
	Sec   int64  `json:"sec,omitempty"`
	Com   int64  `json:"com,omitempty"`
	Shape string `json:"shape,omitempty"`
	On    string `json:"on"` // trading date of the file
	File  string `json:"file"`
}

// same says whether a client's last file said the same.
func (s bowFundSent) same(x bowFundSent) bool {
	if s.Shape == "" {
		s.Shape = bowShapeSingle
	}
	return s.Shape == x.Shape && s.Funds == x.Funds && s.Sec == x.Sec && s.Com == x.Com
}

// zero says whether everything the last file said was 0.
func (s bowFundSent) zero() bool { return s.Funds == 0 && s.Sec == 0 && s.Com == 0 }

// bowRupees is an amount in whole rupees, rounded down (a limit is never
// rounded up; a debit becomes the next whole rupee of debit).
func bowRupees(d money.Dec) int64 {
	p := d.Round(money.UnitPaisa).Paise()
	r := p / 100
	if p%100 < 0 {
		r--
	}
	return r
}

// BOWFundFile is the BOW_FUND upload: every active client's funds (see the
// section comment), or with changedOnly the clients whose figures differ
// from the last file and those never sent.
func (o *Ops) BOWFundFile(ctx context.Context, tenant, actor, date string, changedOnly bool) (string, []byte, error) {
	name, b, _, err := o.bowFundFile(ctx, tenant, actor, date, changedOnly)
	return name, b, err
}

func (o *Ops) bowFundFile(ctx context.Context, tenant, actor, date string, changedOnly bool) (string, []byte, int, error) {
	if err := o.require(ctx, tenant, actor, "Downloading the BOW funds file", RoleMaker, RoleChecker, RoleRisk); err != nil {
		return "", nil, 0, err
	}
	if !validDate(date) {
		return "", nil, 0, invalid("give a date (YYYY-MM-DD)")
	}
	s, err := finspotSpec("bow-fund")
	if err != nil {
		return "", nil, 0, err
	}
	cfg, err := o.bowFilesConfig(ctx, tenant)
	if err != nil {
		return "", nil, 0, err
	}
	bowFileMu.Lock()
	defer bowFileMu.Unlock()
	lims, err := o.bowLimits(ctx, tenant, date)
	if err != nil {
		return "", nil, 0, err
	}
	cs, err := o.ClientMap(ctx, tenant)
	if err != nil {
		return "", nil, 0, err
	}
	prior, err := docs.List[bowFundSent](ctx, o.Docs, tenant, cBOWFundSent, "")
	if err != nil {
		return "", nil, 0, err
	}
	last := map[string]bowFundSent{}
	for _, p := range prior {
		last[p.UCC] = p
	}
	name, err := s.FileName(xfile.Vars{"date": date, "time": o.now().In(ist).Format(finspotTimeFmt)})
	if err != nil {
		return "", nil, 0, err
	}
	fields := 20 // as the broker's file: no Product
	if cfg.Product != "" {
		fields = 21
	}
	line := func(u, cat string, v int64) xfile.Row {
		return xfile.Row{Fields: fields, Values: map[string]string{"ClientID": u, "TotalDeposit": strconv.FormatInt(v, 10), "Category": cat,
			"Filler6": "0", "Filler7": "0", "Filler8": "0", "Factor": "1", "AdhocMargin": "0", "AddIntoEffective": "1", "MTMFactor": "1",
			"EffectiveAmount": "0", "AddIntoMTM": "1", "AddEntry": "Y", "Product": cfg.Product}}
	}
	var rows []xfile.Row
	var sent []bowFundSent
	for _, u := range sortedKeys(cs) {
		c := cs[u]
		was, known := last[u]
		now := bowFundSent{UCC: u, Shape: cfg.Shape, On: date, File: name}
		if c.Status == "Active" {
			l := lims[u]
			if cfg.Shape == bowShapeSingle {
				now.Funds = bowRupees(l.Net)
			} else {
				now.Sec, now.Com = bowRupees(l.NetSec), bowRupees(l.NetCom)
				now.Funds = now.Sec + now.Com
			}
		} else if !known || was.zero() {
			continue // not active, and BOW has nothing from Ledgerline to take back
		}
		if strings.ContainsAny(u, ",\"\r\n") {
			return "", nil, 0, invalid("client code %q cannot go in BOW's comma-separated file", u)
		}
		if changedOnly && known && was.same(now) {
			continue
		}
		if cfg.Shape == bowShapeSingle {
			rows = append(rows, line(u, "", now.Funds))
		} else {
			rows = append(rows, line(u, bowCatSecurities, now.Sec), line(u, bowCatCommodities, now.Com))
			if cfg.Shape == bowShapeThree {
				rows = append(rows, line(u, "", now.Funds))
			}
		}
		if !known || !was.same(now) || was.On != date {
			sent = append(sent, now)
		}
	}
	b, err := s.Write(rows)
	if err != nil {
		return "", nil, 0, err
	}
	for _, x := range sent {
		if err := docs.Upsert(ctx, o.Docs, tenant, cBOWFundSent, x.UCC, x, actor); err != nil {
			return "", nil, 0, err
		}
	}
	o.log(ctx, tenant, actor, "frontoffice.bow_funds_file", name, map[string]any{"date": date, "lines": len(rows), "changed_only": changedOnly, "shape": cfg.Shape})
	if !changedOnly {
		o.noteDailyFile(ctx, tenant, actor, "bow-fund", name, date)
	}
	return name, b, len(rows), nil
}

// bowHold is one client's holding of one security for BOW's stock files.
type bowHold struct {
	UCC, ISIN, Symbol, Series, BSECode string
	DP, Pool                           int64
	DPAvg, PoolAvg                     money.Dec
}

// bowHoldings are the active clients' DP and pool holdings for a trading
// date (see the section comment), sorted by client and ISIN, with the
// ISINs left out for want of a symbol and series or a BSE code.
func (o *Ops) bowHoldings(ctx context.Context, tenant, date string) ([]bowHold, []string, error) {
	set, _ := o.Settings(ctx, tenant)
	prev := set.PrevBusinessDay(date)
	cs, err := o.ClientMap(ctx, tenant)
	if err != nil {
		return nil, nil, err
	}
	_, look, _ := o.Market(ctx, tenant, prev) // no market data: no pool lines
	dl, err := o.sellableDemat(ctx, tenant, date, cs)
	if err != nil {
		return nil, nil, err
	}
	pool, err := o.t1Buys(ctx, tenant, prev, look)
	if err != nil {
		return nil, nil, err
	}
	keys := map[string]bool{}
	for k := range dl {
		keys[k] = true
	}
	for k := range pool {
		keys[k] = true
	}
	refs := map[string]SecRef{}
	ref := func(isin string) SecRef {
		r, ok := refs[isin]
		if !ok {
			r, _, _ = docs.Get[SecRef](ctx, o.Docs, tenant, cSecRef, isin)
			refs[isin] = r
		}
		return r
	}
	var out []bowHold
	var unknown []string
	for _, k := range sortedKeys(keys) {
		u, isin, _ := strings.Cut(k, "|")
		c, ok := cs[u]
		if !ok || c.Status != "Active" {
			continue
		}
		h := bowHold{UCC: u, ISIN: isin}
		if l := dl[k]; l != nil {
			h.DP = max(l.Demat+l.CUSPA, 0)
			h.DPAvg = l.Cost
			if h.DPAvg.Sign() <= 0 && l.Demat <= 0 && l.CUSPA > 0 && l.CUSPAVal.Sign() > 0 {
				h.DPAvg, _ = l.CUSPAVal.Quo(money.DecInt(l.CUSPA))
			}
		}
		if b := pool[k]; b != nil && b.Net > 0 {
			h.Pool = b.Net
			if b.Bought > 0 {
				h.PoolAvg, _ = b.Value.Quo(money.DecInt(b.Bought))
			}
		}
		if h.DP == 0 && h.Pool == 0 {
			continue
		}
		r := ref(isin)
		h.Symbol, h.Series = strings.TrimSpace(r.Symbol), strings.TrimSpace(r.Series)
		if h.Series == "" {
			h.Symbol = "" // a symbol without its NSE series (applicable symbol only) is not one BOW can match
		}
		h.BSECode = strings.TrimSpace(r.BSECode)
		if h.Symbol == "" && h.BSECode == "" {
			if !contains(unknown, isin) {
				unknown = append(unknown, isin)
			}
			continue
		}
		if strings.ContainsAny(u+h.Symbol+h.Series+h.BSECode, ",\"|\r\n") {
			return nil, nil, invalid("%s %s cannot go in BOW's comma-separated file", u, isin)
		}
		if h.DPAvg.Sign() < 0 {
			h.DPAvg = money.Dec{}
		}
		out = append(out, h)
	}
	return out, unknown, nil
}

// BOWHoldingFile is the BOW_DP_Holding_Combined_Upload file (see the
// section comment): one line per active client per security with a DP or
// pool quantity.
func (o *Ops) BOWHoldingFile(ctx context.Context, tenant, date string) (string, []byte, error) {
	if !validDate(date) {
		return "", nil, invalid("give a date (YYYY-MM-DD)")
	}
	s, err := finspotSpec("bow-dp-holding")
	if err != nil {
		return "", nil, err
	}
	cfg, err := o.bowFilesConfig(ctx, tenant)
	if err != nil {
		return "", nil, err
	}
	hs, unknown, err := o.bowHoldings(ctx, tenant, date)
	if err != nil {
		return "", nil, err
	}
	var rows []xfile.Row
	for _, h := range hs {
		rows = append(rows, xfile.Row{Values: map[string]string{
			"BackOfficeID": h.UCC, "NSESymbol": h.Symbol, "Series": h.Series, "BSEScripCode": h.BSECode,
			"DPQuantity": strconv.FormatInt(h.DP, 10), "DPAvePrice": h.DPAvg.Fixed(2), "DPCFSHaircut": cfg.DPHaircut.String(),
			"PoolQuantity": strconv.FormatInt(h.Pool, 10), "PoolAvePrice": h.PoolAvg.Fixed(2), "PoolCFSHaircut": cfg.PoolHcut.String()}})
	}
	b, err := s.Write(rows)
	if err != nil {
		return "", nil, err
	}
	name, err := s.FileName(xfile.Vars{"date": date})
	if err != nil {
		return "", nil, err
	}
	detail := map[string]any{"date": date, "lines": len(rows)}
	if len(unknown) > 0 {
		detail["left_out_isins"] = unknown // no NSE symbol and series nor BSE code: load the security masters
	}
	o.log(ctx, tenant, SystemActor, "frontoffice.bow_holdings_file", name, detail)
	o.noteDailyFile(ctx, tenant, SystemActor, "bow-dp-holding", name, date)
	return name, b, nil
}
