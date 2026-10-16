// Package recon matches exchange trades against the OMS trade book and
// classifies every difference. Exchange records are the source of truth;
// the OMS is reconciled against them, never the reverse.
package recon

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"time"

	"ledgerline/internal/ingest"
	"ledgerline/internal/money"
)

// BreakType classifies a reconciliation difference.
type BreakType string

const (
	MissingInOMS       BreakType = "MISSING_IN_OMS"
	MissingInExchange  BreakType = "MISSING_IN_EXCHANGE"
	TradeNoMismatch    BreakType = "TRADE_NO_MISMATCH"
	UCCMismatch        BreakType = "UCC_MISMATCH"
	SideMismatch       BreakType = "SIDE_MISMATCH"
	InstrumentMismatch BreakType = "INSTRUMENT_MISMATCH"
	QtyMismatch        BreakType = "QTY_MISMATCH"
	PriceMismatch      BreakType = "PRICE_MISMATCH"
	AlgoIDMissing      BreakType = "ALGO_ID_MISSING"
)

// Priority order: the first matching type becomes a break's primary type.
var order = []BreakType{MissingInOMS, MissingInExchange, TradeNoMismatch, UCCMismatch, SideMismatch, InstrumentMismatch, QtyMismatch, PriceMismatch, AlgoIDMissing}

var suggestions = map[BreakType]string{
	MissingInOMS:       "Likely a manual trade from the exchange terminal. Book it into the OMS against the client and confirm with the dealer.",
	MissingInExchange:  "Stale or rejected order still marked filled in the OMS. Confirm rejection with the exchange and cancel the OMS fill.",
	TradeNoMismatch:    "Same trade recorded under a different trade number in the OMS. Correct the OMS reference to the exchange trade number.",
	UCCMismatch:        "Confirm the intended client with the dealer. If the OMS client is correct, raise a client code modification before the exchange cut-off; otherwise correct the OMS.",
	SideMismatch:       "Buy/sell differs. The exchange record is final; correct the OMS and check the client's position and margin.",
	InstrumentMismatch: "Contract differs (symbol, expiry, strike or type). The exchange record is final; correct the OMS.",
	QtyMismatch:        "Exchange quantity is final. Correct the OMS fill and check for a partial fill split across trade numbers.",
	PriceMismatch:      "Exchange price is final. Correct the OMS fill price; contract notes use exchange data.",
	AlgoIDMissing:      "Map the API order to its registered strategy ID. Algo orders without a valid ID must be reported.",
}

// FieldDiff is one differing field.
type FieldDiff struct {
	Field    string `json:"field"`
	Exchange string `json:"exchange"`
	OMS      string `json:"oms"`
}

// Break is one unreconciled trade.
type Break struct {
	ID         string      `json:"id"`
	Type       BreakType   `json:"type"`
	Exchange   string      `json:"exchange"`
	Segment    string      `json:"segment"`
	TradeDate  string      `json:"trade_date"`
	TradeNo    string      `json:"trade_no"`
	OMSTradeNo string      `json:"oms_trade_no,omitempty"`
	UCC        string      `json:"ucc"`
	Instrument string      `json:"instrument"`
	Value      money.Dec   `json:"value"`
	Diffs      []FieldDiff `json:"diffs"`
	Suggestion string      `json:"suggestion"`
}

// Result is the outcome of one reconciliation run.
type Result struct {
	Date          string            `json:"date"`
	ExchangeCount int               `json:"exchange_count"`
	OMSCount      int               `json:"oms_count"`
	Matched       int               `json:"matched"`
	Breaks        []Break           `json:"breaks"`
	ByType        map[BreakType]int `json:"by_type"`
}

// Options tune matching.
type Options struct {
	PriceTolerance money.Dec     // absolute; zero means exact
	FuzzyWindow    time.Duration // for trade-number mismatches; default 2s
}

func breakID(date string, t BreakType, exch, seg, tradeNo, omsNo string) string {
	h := sha256.Sum256([]byte(date + "|" + string(t) + "|" + exch + "|" + seg + "|" + tradeNo + "|" + omsNo))
	return "BRK-" + hex.EncodeToString(h[:])[:12]
}

func diffs(e, o ingest.TradeEvent, tol money.Dec) []FieldDiff {
	var d []FieldDiff
	add := func(f, a, b string) { d = append(d, FieldDiff{f, a, b}) }
	if e.UCC != o.UCC {
		add("ucc", e.UCC, o.UCC)
	}
	if e.Side != o.Side {
		add("side", e.Side, o.Side)
	}
	if e.Instrument() != o.Instrument() {
		add("instrument", e.Instrument(), o.Instrument())
	}
	if e.Qty != o.Qty {
		add("qty", money.DecInt(e.Qty).String(), money.DecInt(o.Qty).String())
	}
	if e.Price.Sub(o.Price).Abs().Cmp(tol) > 0 {
		add("price", e.Price.String(), o.Price.String())
	}
	if e.AlgoID != "" && e.AlgoID != o.AlgoID {
		add("algo_id", e.AlgoID, o.AlgoID)
	}
	return d
}

var fieldType = map[string]BreakType{"ucc": UCCMismatch, "side": SideMismatch, "instrument": InstrumentMismatch, "qty": QtyMismatch, "price": PriceMismatch, "algo_id": AlgoIDMissing}

func primary(ds []FieldDiff) BreakType {
	best := len(order)
	for _, d := range ds {
		for i, t := range order {
			if fieldType[d.Field] == t && i < best {
				best = i
			}
		}
	}
	return order[best]
}

func secs(t string) (int, bool) {
	p, err := time.Parse("15:04:05", t)
	if err != nil {
		return 0, false
	}
	return p.Hour()*3600 + p.Minute()*60 + p.Second(), true
}

// Run reconciles one trade date. Break IDs are deterministic, so re-running
// after corrections yields the same IDs for breaks that persist.
func Run(date string, exch, oms []ingest.TradeEvent, opt Options) Result {
	if opt.FuzzyWindow == 0 {
		opt.FuzzyWindow = 2 * time.Second
	}
	res := Result{Date: date, ByType: map[BreakType]int{}, Breaks: []Break{}}
	filter := func(in []ingest.TradeEvent) []ingest.TradeEvent {
		var out []ingest.TradeEvent
		for _, t := range in {
			if t.TradeDate == date {
				out = append(out, t)
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Key() < out[j].Key() })
		return out
	}
	exch, oms = filter(exch), filter(oms)
	res.ExchangeCount, res.OMSCount = len(exch), len(oms)

	omsBy := map[string]int{}
	for i, o := range oms {
		omsBy[o.Key()] = i
	}
	used := make([]bool, len(oms))
	var unmatched []ingest.TradeEvent
	add := func(b Break) {
		b.ID = breakID(date, b.Type, b.Exchange, b.Segment, b.TradeNo, b.OMSTradeNo)
		b.Suggestion = suggestions[b.Type]
		if b.Diffs == nil {
			b.Diffs = []FieldDiff{}
		}
		res.Breaks = append(res.Breaks, b)
		res.ByType[b.Type]++
	}
	for _, e := range exch {
		i, ok := omsBy[e.Key()]
		if !ok {
			unmatched = append(unmatched, e)
			continue
		}
		used[i] = true
		ds := diffs(e, oms[i], opt.PriceTolerance)
		if len(ds) == 0 {
			res.Matched++
			continue
		}
		add(Break{Type: primary(ds), Exchange: e.Exchange, Segment: e.Segment, TradeDate: date, TradeNo: e.TradeNo, UCC: e.UCC, Instrument: e.Instrument(), Value: e.Value(), Diffs: ds})
	}
	// Fuzzy pass: an unmatched exchange trade whose economics match a leftover
	// OMS fill within the time window is the same trade under another number.
	for _, e := range unmatched {
		es, eok := secs(e.TradeTime)
		found := -1
		for i, o := range oms {
			if used[i] || o.Exchange != e.Exchange || o.Segment != e.Segment || o.UCC != e.UCC || o.Instrument() != e.Instrument() ||
				o.Side != e.Side || o.Qty != e.Qty || o.Price.Cmp(e.Price) != 0 {
				continue
			}
			if os, ook := secs(o.TradeTime); eok && ook {
				dt := os - es
				if dt < 0 {
					dt = -dt
				}
				if time.Duration(dt)*time.Second > opt.FuzzyWindow {
					continue
				}
			}
			found = i
			break
		}
		if found >= 0 {
			used[found] = true
			o := oms[found]
			add(Break{Type: TradeNoMismatch, Exchange: e.Exchange, Segment: e.Segment, TradeDate: date, TradeNo: e.TradeNo, OMSTradeNo: o.TradeNo, UCC: e.UCC, Instrument: e.Instrument(), Value: e.Value(), Diffs: []FieldDiff{{"trade_no", e.TradeNo, o.TradeNo}}})
			continue
		}
		add(Break{Type: MissingInOMS, Exchange: e.Exchange, Segment: e.Segment, TradeDate: date, TradeNo: e.TradeNo, UCC: e.UCC, Instrument: e.Instrument(), Value: e.Value()})
	}
	for i, o := range oms {
		if !used[i] {
			add(Break{Type: MissingInExchange, Exchange: o.Exchange, Segment: o.Segment, TradeDate: date, TradeNo: o.TradeNo, UCC: o.UCC, Instrument: o.Instrument(), Value: o.Value()})
		}
	}
	rank := map[BreakType]int{}
	for i, t := range order {
		rank[t] = i
	}
	sort.SliceStable(res.Breaks, func(i, j int) bool {
		a, b := res.Breaks[i], res.Breaks[j]
		if rank[a.Type] != rank[b.Type] {
			return rank[a.Type] < rank[b.Type]
		}
		return a.TradeNo < b.TradeNo
	})
	return res
}
