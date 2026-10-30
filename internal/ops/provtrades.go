package ops

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/xfile"
)

// ---- provisional (intraday) trade files ----
//
// During the day NSE Clearing, NSE and BSE put out provisional trade files
// (…_<YYYYMMDD>_P_<seq>.csv) in the same SEBI standardised layout as the
// final file (…_F_…). Provisional trades are kept apart from the day's
// exchange trades: they feed live risk only, never billing, contract notes
// or trade reconciliation. Each provisional file adds to (or corrects) the
// trades already held for its exchange, segment and date, matched by trade
// number, so cumulative and incremental files both work. Once the final
// file of an exchange-segment-date is loaded, its provisional trades are
// dropped and later provisional files for it are not used, so a trade is
// never counted twice.

const cProvTrade = "provtrade" // date|EXCH|SEG

var reProvisional = regexp.MustCompile(`(?i)_\d{8}_P_\d+\.csv(\.gz)?$`)

// isProvisionalTradeFile reports whether a trade file name is a provisional one.
func isProvisionalTradeFile(name string) bool { return reProvisional.MatchString(name) }

// ProvTrades are the provisional trades of one exchange, segment and date.
type ProvTrades struct {
	ID       string              `json:"id"`
	Date     string              `json:"date"`
	Exchange string              `json:"exchange"`
	Segment  string              `json:"segment"`
	Files    []string            `json:"files"`
	Trades   []ingest.TradeEvent `json:"trades"`
	Replaced string              `json:"replaced_by,omitempty"` // the final file that superseded them
	LoadedAt string              `json:"loaded_at"`
}

func provID(date, exch, seg string) string { return date + "|" + exch + "|" + seg }

// loadProvisionalTrades parses a provisional trade file with the trade
// adapter and keeps its trades for live risk.
func (o *Ops) loadProvisionalTrades(ctx context.Context, tenant, actor, src string, f xfile.File, r *XFileResult) {
	fail := func(err error) { r.Loaded = "Not loaded: " + strings.TrimPrefix(err.Error(), "invalid: ") }
	spec, err := o.App.Ingest.Registry.Get(src, "")
	if err != nil {
		fail(err)
		return
	}
	evs, perrs, err := spec.Parse(bytes.NewReader(f.Data))
	if err == nil && len(perrs) > 0 {
		err = fmt.Errorf("%d rows could not be read (first: %s)", len(perrs), perrs[0].Error())
	}
	if err != nil {
		fail(err)
		return
	}
	evs, _ = o.applyCCM(ctx, tenant, evs)
	by := map[string][]ingest.TradeEvent{}
	for _, e := range evs {
		e.Tenant = tenant
		by[provID(e.TradeDate, e.Exchange, e.Segment)] = append(by[provID(e.TradeDate, e.Exchange, e.Segment)], e)
	}
	final := map[string]bool{}
	added, skipped := 0, 0
	var late []string
	for _, id := range sortedKeys(by) {
		ts := by[id]
		date, exch, seg := ts[0].TradeDate, ts[0].Exchange, ts[0].Segment
		if _, ok := final[date]; !ok {
			fs, _ := o.App.Trades.List(ctx, tenant, ingest.FromExchange, date)
			for _, t := range fs {
				final[provID(t.TradeDate, t.Exchange, t.Segment)] = true
			}
			final[date] = true
		}
		old, _, _ := docs.Get[ProvTrades](ctx, o.Docs, tenant, cProvTrade, id)
		if final[id] || old.Replaced != "" {
			skipped += len(ts)
			late = append(late, exch+" "+seg+" "+date)
			continue
		}
		p := ProvTrades{ID: id, Date: date, Exchange: exch, Segment: seg, Files: old.Files, LoadedAt: o.now().Format("2006-01-02 15:04:05")}
		idx := map[string]int{}
		for _, t := range old.Trades {
			idx[t.Key()] = len(p.Trades)
			p.Trades = append(p.Trades, t)
		}
		for _, t := range ts {
			if i, ok := idx[t.Key()]; ok {
				p.Trades[i] = t // a later file corrects the trade
				continue
			}
			idx[t.Key()] = len(p.Trades)
			p.Trades = append(p.Trades, t)
			added++
		}
		if !contains(p.Files, f.Name) {
			p.Files = append(p.Files, f.Name)
		}
		if err := docs.Upsert(ctx, o.Docs, tenant, cProvTrade, id, p, actor); err != nil {
			fail(err)
			return
		}
	}
	r.Added, r.Skipped = added, skipped
	switch {
	case len(late) > 0 && added == 0:
		r.Loaded = "Not used: the final trade file is already loaded for " + strings.Join(late, ", ")
	case len(late) > 0:
		r.Loaded = "Loaded as provisional trades for live risk"
		r.Note = "Left out (final file already loaded): " + strings.Join(late, ", ")
	default:
		r.Loaded = "Loaded as provisional trades for live risk (replaced by the final trade file when it is loaded)"
	}
}

// replaceProvisional drops the provisional trades a final trade file
// supersedes: every exchange, segment and date it carries.
func (o *Ops) replaceProvisional(ctx context.Context, tenant, actor, finalName string, rows []xfile.Row) {
	seen := map[string]bool{}
	for _, row := range rows {
		exch := strings.ToUpper(firstNonEmptyStr(row.Get("Xchg"), row.Get("Src")))
		id := provID(anyDate(row.Get("TradDt")), exch, strings.ToUpper(row.Get("Sgmt")))
		if seen[id] {
			continue
		}
		seen[id] = true
		p, _, err := docs.Get[ProvTrades](ctx, o.Docs, tenant, cProvTrade, id)
		if err != nil || p.Replaced != "" {
			continue
		}
		n := len(p.Trades)
		p.Trades, p.Replaced = nil, finalName
		if docs.Upsert(ctx, o.Docs, tenant, cProvTrade, id, p, actor) == nil {
			o.log(ctx, tenant, actor, "trades.provisional_replaced", id, map[string]any{"final": finalName, "provisional_trades": n, "files": len(p.Files)})
		}
	}
}

// provisionalTrades are the day's provisional trades for every exchange
// segment the final trades do not yet cover.
func (o *Ops) provisionalTrades(ctx context.Context, tenant, date string, final []ingest.TradeEvent) []ingest.TradeEvent {
	have := map[string]bool{}
	for _, t := range final {
		have[t.Exchange+"|"+t.Segment] = true
	}
	ps, _ := docs.List[ProvTrades](ctx, o.Docs, tenant, cProvTrade, date+"|")
	var out []ingest.TradeEvent
	for _, p := range ps {
		if p.Replaced != "" || have[p.Exchange+"|"+p.Segment] {
			continue
		}
		out = append(out, p.Trades...)
	}
	out, _ = o.applyCCM(ctx, tenant, out)
	sort.SliceStable(out, func(i, j int) bool { return out[i].TradeTime < out[j].TradeTime })
	return out
}

// IntradayTrades are the day's trades for live risk: the final exchange
// trades, plus provisional trades for exchange segments whose final file
// has not arrived. The source says which were used.
func (o *Ops) IntradayTrades(ctx context.Context, tenant, date string) ([]ingest.TradeEvent, string, error) {
	fs, err := o.exchangeTrades(ctx, tenant, date)
	if err != nil {
		return nil, "", err
	}
	pt := o.provisionalTrades(ctx, tenant, date, fs)
	switch {
	case len(pt) == 0:
		return fs, "Exchange", nil
	case len(fs) == 0:
		return pt, "Exchange (provisional trade files)", nil
	}
	return append(fs, pt...), "Exchange (final and provisional trade files)", nil
}
