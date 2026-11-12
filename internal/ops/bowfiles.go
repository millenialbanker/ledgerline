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
// The two files the pilot broker's current back office writes each morning
// for BOW (BOLTPlusOnWeb) and uploads in the BOW Admin terminal, in the
// layouts read from the broker's real files (structure only):
//
//   - BOW_FUND_<dd-mm-yy hh_mm_ss AM>.csv (bow-fund, ~08:13): one line per
//     client with the funds in whole rupees. It is a user-level deposit, so
//     it carries one figure: Securities + Commodities as bowLimits computes
//     them for the API (signed: a debit is kept, and a debit in one ledger
//     reduces the other), a blocked, inactive or not active client 0. The
//     API push (bowapi.go) is preferred: it sets Securities and Commodities
//     apart, which the file cannot. The file is the fallback when the API is
//     not set up or not working. Every active client is written, or with
//     "changed" only the clients whose figure differs from the last file
//     (the broker's second, small morning file); what each file said is
//     kept per client (cBOWFundSent). A client no longer active who was last
//     sent a figure other than 0 gets 0 once, so BOW does not keep it.
//   - BOW_DP_Holding_Combined_Upload_<DDMMYYYY>.TXT (bow-dp-holding,
//     ~08:17): one line per client per security, the only route for
//     holdings (BOW's API cannot write them). DP quantity as Finspot's ISIN
//     Holding (sellableDemat), pool quantity the previous business day's
//     delivery buys less sells (t1Buys, as Finspot's T1 file); the DP
//     average price is the holding's average cost (Holding.AvgCost, from the
//     DP holdings import and corporate actions; 0.00 when unknown, and for
//     unpaid purchases in CUSPA alone their value over quantity), the pool
//     average price the average of the day's delivery buys. Symbol and
//     series from the NSE security master, the BSE code from the BSE scrip
//     file (SecRef); a security with neither is left out and logged. The
//     credit-for-sale haircuts (fields 10 and 16, probably) are the firm's
//     values in Connections → BOW upload files, 0 by default as in the
//     broker's file.
//
// The files are on Live RMS → Front-office files and on the BOW panel; they
// are on the daily file checklist when the BOW limits API or BOW upload
// files connection is saved.

const (
	connBOWFiles = "bow-files"
	cBOWFundSent = "bowfundsent" // UCC -> bowFundSent
)

var bowFileMu sync.Mutex

func init() {
	ConnProviders = append(ConnProviders, ConnProvider{ID: connBOWFiles, Name: "BOW upload files (funds and holdings)", Group: "Trading", Test: "none",
		Purpose: "For a firm that uploads BOW's BOW_FUND and BOW_DP_Holding files in the BOW Admin terminal (holdings always go this way: BOW's API cannot write them). Download them on Live RMS → Front-office files. Saving this connection puts both files on the daily file checklist",
		Fields: []ConnField{
			{Key: "dp_haircut", Label: "Credit-for-sale haircut % on DP holdings (0 to 100, default 0)", Optional: true, Placeholder: "0"},
			{Key: "pool_haircut", Label: "Credit-for-sale haircut % on pool (T1) holdings (0 to 100, default 0; 100 gives no credit for sale)", Optional: true, Placeholder: "0"},
			fNotes}})
}

// bowFilesCfg is the BOW upload files connection's values.
type bowFilesCfg struct {
	Set                 bool
	DPHaircut, PoolHcut int64
}

func (o *Ops) bowFilesConfig(ctx context.Context, tenant string) (bowFilesCfg, error) {
	c, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connBOWFiles)
	var cfg bowFilesCfg
	if err != nil && !errors.Is(err, docs.ErrNotFound) {
		return cfg, err
	}
	cfg.Set = err == nil && c.Status != "" && c.Status != "Not set"
	pct := func(key, what string) (int64, error) {
		v := strings.TrimSpace(c.Values[key])
		if v == "" {
			return 0, nil
		}
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 || n > 100 {
			return 0, invalid("the %s haircut %% is a whole number from 0 to 100", what)
		}
		return n, nil
	}
	if cfg.DPHaircut, err = pct("dp_haircut", "DP"); err != nil {
		return cfg, err
	}
	if cfg.PoolHcut, err = pct("pool_haircut", "pool"); err != nil {
		return cfg, err
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

// BOWKinds are the BOW files on /v1/frontoffice/{kind}.
var BOWKinds = []string{"bow-funds-file", "bow-holdings"}

// BOWFile writes one BOW upload for a trading date; changed (funds only)
// writes only the clients whose figure changed since the last file.
func (o *Ops) BOWFile(ctx context.Context, tenant, actor, kind, date string, changed bool) (string, []byte, error) {
	switch kind {
	case "bow-funds-file":
		return o.BOWFundFile(ctx, tenant, actor, date, changed)
	case "bow-holdings":
		return o.BOWHoldingFile(ctx, tenant, date)
	}
	return "", nil, fmt.Errorf("%w: no BOW file %s", ErrNotFound, kind)
}

// bowFundSent is what the last BOW funds file said about a client.
type bowFundSent struct {
	UCC   string `json:"ucc"`
	Funds int64  `json:"funds"` // rupees
	On    string `json:"on"`    // trading date of the file
	File  string `json:"file"`
}

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
// section comment), or with changedOnly the clients whose figure differs
// from the last file and those never sent.
func (o *Ops) BOWFundFile(ctx context.Context, tenant, actor, date string, changedOnly bool) (string, []byte, error) {
	if err := o.require(ctx, tenant, actor, "Downloading the BOW funds file", RoleMaker, RoleChecker, RoleRisk); err != nil {
		return "", nil, err
	}
	if !validDate(date) {
		return "", nil, invalid("give a date (YYYY-MM-DD)")
	}
	s, err := finspotSpec("bow-fund")
	if err != nil {
		return "", nil, err
	}
	bowFileMu.Lock()
	defer bowFileMu.Unlock()
	lims, err := o.bowLimits(ctx, tenant, date)
	if err != nil {
		return "", nil, err
	}
	cs, err := o.ClientMap(ctx, tenant)
	if err != nil {
		return "", nil, err
	}
	prior, err := docs.List[bowFundSent](ctx, o.Docs, tenant, cBOWFundSent, "")
	if err != nil {
		return "", nil, err
	}
	last := map[string]bowFundSent{}
	for _, p := range prior {
		last[p.UCC] = p
	}
	name, err := s.FileName(xfile.Vars{"date": date, "time": o.now().In(ist).Format(finspotTimeFmt)})
	if err != nil {
		return "", nil, err
	}
	var rows []xfile.Row
	var sent []bowFundSent
	for _, u := range sortedKeys(cs) {
		c := cs[u]
		was, known := last[u]
		var v int64
		if c.Status == "Active" {
			v = bowRupees(lims[u].Net)
		} else if !known || was.Funds == 0 {
			continue // not active, and BOW has nothing from Ledgerline to take back
		}
		if strings.ContainsAny(u, ",\"\r\n") {
			return "", nil, invalid("client code %q cannot go in BOW's comma-separated file", u)
		}
		if changedOnly && known && was.Funds == v {
			continue
		}
		rows = append(rows, xfile.Row{Values: map[string]string{"ClientCode": u, "CashDeposit": strconv.FormatInt(v, 10)}})
		if !known || was.Funds != v || was.On != date {
			sent = append(sent, bowFundSent{UCC: u, Funds: v, On: date, File: name})
		}
	}
	b, err := s.Write(rows)
	if err != nil {
		return "", nil, err
	}
	for _, x := range sent {
		if err := docs.Upsert(ctx, o.Docs, tenant, cBOWFundSent, x.UCC, x, actor); err != nil {
			return "", nil, err
		}
	}
	o.log(ctx, tenant, actor, "frontoffice.bow_funds_file", name, map[string]any{"date": date, "lines": len(rows), "changed_only": changedOnly})
	if !changedOnly {
		o.noteDailyFile(ctx, tenant, actor, "bow-fund", name, date)
	}
	return name, b, nil
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
	set, _ := o.Settings(ctx, tenant)
	prev := set.PrevBusinessDay(date)
	cs, err := o.ClientMap(ctx, tenant)
	if err != nil {
		return "", nil, err
	}
	_, look, _ := o.Market(ctx, tenant, prev) // no market data: no pool lines
	dl, err := o.sellableDemat(ctx, tenant, date, cs)
	if err != nil {
		return "", nil, err
	}
	pool, err := o.t1Buys(ctx, tenant, prev, look)
	if err != nil {
		return "", nil, err
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
	var rows []xfile.Row
	var unknown []string
	for _, k := range sortedKeys(keys) {
		u, isin, _ := strings.Cut(k, "|")
		c, ok := cs[u]
		if !ok || c.Status != "Active" {
			continue
		}
		var dp, pq int64
		dpAvg, poolAvg := money.Dec{}, money.Dec{}
		if l := dl[k]; l != nil {
			dp = max(l.Demat+l.CUSPA, 0)
			dpAvg = l.Cost
			if dpAvg.Sign() <= 0 && l.Demat <= 0 && l.CUSPA > 0 && l.CUSPAVal.Sign() > 0 {
				dpAvg, _ = l.CUSPAVal.Quo(money.DecInt(l.CUSPA))
			}
		}
		if b := pool[k]; b != nil && b.Net > 0 {
			pq = b.Net
			if b.Bought > 0 {
				poolAvg, _ = b.Value.Quo(money.DecInt(b.Bought))
			}
		}
		if dp == 0 && pq == 0 {
			continue
		}
		r := ref(isin)
		sym, series := strings.TrimSpace(r.Symbol), strings.TrimSpace(r.Series)
		if series == "" {
			sym = "" // a symbol without its NSE series (applicable symbol only) is not one BOW can match
		}
		bse := strings.TrimSpace(r.BSECode)
		if sym == "" && bse == "" {
			if !contains(unknown, isin) {
				unknown = append(unknown, isin)
			}
			continue
		}
		if strings.ContainsAny(u+sym+series+bse, ",\"\r\n") {
			return "", nil, invalid("%s %s cannot go in BOW's comma-separated file", u, isin)
		}
		if dpAvg.Sign() < 0 {
			dpAvg = money.Dec{}
		}
		rows = append(rows, xfile.Row{Values: map[string]string{
			"ClientCode": u, "Symbol": sym, "Series": series, "BSECode": bse,
			"DPQty": strconv.FormatInt(dp, 10), "DPAvgPrice": dpAvg.Fixed(2), "DPCFSHaircutPct": strconv.FormatInt(cfg.DPHaircut, 10),
			"PoolQty": strconv.FormatInt(pq, 10), "PoolAvgPrice": poolAvg.Fixed(2), "PoolCFSHaircutPct": strconv.FormatInt(cfg.PoolHcut, 10)}})
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
