// Package charges computes brokerage, statutory levies and exchange charges
// for a trade from an effective-dated, versioned rate schedule.
//
// Rates are configuration, not code: a SEBI, exchange or budget change ships
// as a new Rule with a new effective_from date, and historical trades keep
// billing at the rates in force on their trade date.
package charges

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"ledgerline/internal/money"
)

// Bucket is a charge product: segment × instrument class × product.
type Bucket string

const (
	CMDelivery Bucket = "CM-DEL"
	CMIntraday Bucket = "CM-INTRA"
	FOFut      Bucket = "FO-FUT"
	FOOpt      Bucket = "FO-OPT"
	CDFut      Bucket = "CD-FUT"
	CDOpt      Bucket = "CD-OPT"
	COMFut     Bucket = "COM-FUT"
	COMOpt     Bucket = "COM-OPT"
)

var knownBuckets = map[Bucket]bool{CMDelivery: true, CMIntraday: true, FOFut: true, FOOpt: true, CDFut: true, CDOpt: true, COMFut: true, COMOpt: true}

// BucketFor maps a trade's segment, instrument type (EQ, FUT, CE, PE) and
// product (CNC, MIS, NRML, MTF) to a charge bucket.
func BucketFor(segment, instrumentType, product string) (Bucket, error) {
	opt := instrumentType == "CE" || instrumentType == "PE"
	fut := instrumentType == "FUT"
	switch segment {
	case "CM":
		if product == "MIS" {
			return CMIntraday, nil
		}
		return CMDelivery, nil
	case "FO":
		if opt {
			return FOOpt, nil
		}
		if fut {
			return FOFut, nil
		}
	case "CD":
		if opt {
			return CDOpt, nil
		}
		if fut {
			return CDFut, nil
		}
	case "COM":
		if opt {
			return COMOpt, nil
		}
		if fut {
			return COMFut, nil
		}
	}
	return "", fmt.Errorf("charges: no bucket for segment %q instrument %q", segment, instrumentType)
}

// Brokerage modes.
const (
	BrokNone       = "none"
	BrokFlat       = "flat"         // fixed rupees per executed order
	BrokPct        = "pct"          // percent of turnover
	BrokMinFlatPct = "min_flat_pct" // lower of flat and percent
)

// Rule holds the rates for one bucket on one exchange ("*" for all) over a
// date range. Percent fields are percent of turnover (premium for options).
type Rule struct {
	Bucket        Bucket    `json:"bucket"`
	Exchange      string    `json:"exchange"`
	EffectiveFrom string    `json:"effective_from"`
	EffectiveTo   string    `json:"effective_to,omitempty"`
	BrokerageMode string    `json:"brokerage_mode"`
	BrokerageFlat money.Dec `json:"brokerage_flat"`
	BrokeragePct  money.Dec `json:"brokerage_pct"`
	STTBuyPct     money.Dec `json:"stt_buy_pct"`
	STTSellPct    money.Dec `json:"stt_sell_pct"`
	TxnPct        money.Dec `json:"txn_pct"`
	StampBuyPct   money.Dec `json:"stamp_buy_pct"`
	STTRound      string    `json:"stt_round,omitempty"`   // paisa (default) or rupee
	StampRound    string    `json:"stamp_round,omitempty"` // paisa (default) or rupee
}

// Schedule is a versioned set of rules plus tenant-wide statutory rates.
type Schedule struct {
	Version    string    `json:"version"`
	Sample     bool      `json:"sample"`
	Note       string    `json:"note,omitempty"`
	SEBIFeePct money.Dec `json:"sebi_fee_pct"`
	GSTPct     money.Dec `json:"gst_pct"`
	Rules      []Rule    `json:"rules"`
}

func validDate(s string) bool {
	if len(s) != 10 || s[4] != '-' || s[7] != '-' {
		return false
	}
	for i, c := range s {
		if i != 4 && i != 7 && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// LoadSchedule decodes and validates a JSON schedule.
func LoadSchedule(r io.Reader) (Schedule, error) {
	var s Schedule
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return Schedule{}, fmt.Errorf("charges: decode schedule: %w", err)
	}
	return s, s.Validate()
}

// Validate checks every rule and rejects overlapping rules for the same
// bucket and exchange.
func (s Schedule) Validate() error {
	if s.Version == "" {
		return errors.New("charges: schedule version is required")
	}
	if s.SEBIFeePct.Sign() < 0 || s.GSTPct.Sign() < 0 {
		return errors.New("charges: statutory rates cannot be negative")
	}
	for i, r := range s.Rules {
		where := fmt.Sprintf("charges: rule %d (%s %s)", i+1, r.Bucket, r.Exchange)
		if !knownBuckets[r.Bucket] {
			return fmt.Errorf("%s: unknown bucket", where)
		}
		if r.Exchange == "" {
			return fmt.Errorf("%s: exchange is required (use * for all)", where)
		}
		if !validDate(r.EffectiveFrom) || (r.EffectiveTo != "" && (!validDate(r.EffectiveTo) || r.EffectiveTo < r.EffectiveFrom)) {
			return fmt.Errorf("%s: invalid effective dates", where)
		}
		switch r.BrokerageMode {
		case BrokNone, BrokFlat, BrokPct, BrokMinFlatPct:
		default:
			return fmt.Errorf("%s: brokerage_mode must be none, flat, pct or min_flat_pct", where)
		}
		for _, d := range []money.Dec{r.BrokerageFlat, r.BrokeragePct, r.STTBuyPct, r.STTSellPct, r.TxnPct, r.StampBuyPct} {
			if d.Sign() < 0 {
				return fmt.Errorf("%s: rates cannot be negative", where)
			}
		}
		if _, err := money.ParseUnit(r.STTRound); err != nil {
			return fmt.Errorf("%s: %v", where, err)
		}
		if _, err := money.ParseUnit(r.StampRound); err != nil {
			return fmt.Errorf("%s: %v", where, err)
		}
		for j := 0; j < i; j++ {
			o := s.Rules[j]
			if o.Bucket == r.Bucket && o.Exchange == r.Exchange && overlaps(o, r) {
				return fmt.Errorf("%s: overlaps rule %d", where, j+1)
			}
		}
	}
	return nil
}

func overlaps(a, b Rule) bool {
	aEnd, bEnd := a.EffectiveTo, b.EffectiveTo
	if aEnd == "" {
		aEnd = "9999-12-31"
	}
	if bEnd == "" {
		bEnd = "9999-12-31"
	}
	return a.EffectiveFrom <= bEnd && b.EffectiveFrom <= aEnd
}

// Rule finds the rule for a bucket, exchange and trade date. An
// exchange-specific rule wins over a "*" rule.
func (s Schedule) Rule(b Bucket, exchange, date string) (Rule, error) {
	var wild *Rule
	for i := range s.Rules {
		r := &s.Rules[i]
		if r.Bucket != b || date < r.EffectiveFrom || (r.EffectiveTo != "" && date > r.EffectiveTo) {
			continue
		}
		if r.Exchange == exchange {
			return *r, nil
		}
		if r.Exchange == "*" {
			wild = r
		}
	}
	if wild != nil {
		return *wild, nil
	}
	return Rule{}, fmt.Errorf("charges: no %s rate for %s on %s in schedule %s", b, exchange, date, s.Version)
}

// Leg is one executed trade (or order) to price.
type Leg struct {
	Bucket   Bucket    `json:"bucket"`
	Exchange string    `json:"exchange"`
	Side     string    `json:"side"` // B or S
	Qty      int64     `json:"qty"`
	Price    money.Dec `json:"price"`
	Date     string    `json:"date"`
	// NoBrokerage leaves brokerage (and the GST on it) out, for callers that
	// charge brokerage per order from a plan.
	NoBrokerage bool `json:"-"`
}

// Breakdown is the charge split for one or more legs.
type Breakdown struct {
	Turnover  money.Dec    `json:"turnover"`
	Brokerage money.Amount `json:"brokerage"`
	STT       money.Amount `json:"stt"`
	ExchTxn   money.Amount `json:"exchange_txn"`
	SEBIFee   money.Amount `json:"sebi_fee"`
	Stamp     money.Amount `json:"stamp_duty"`
	GST       money.Amount `json:"gst"`
	Total     money.Amount `json:"total"`
	Rule      string       `json:"rule,omitempty"`
}

// Add sums two breakdowns.
func (b Breakdown) Add(o Breakdown) Breakdown {
	r := b.Rule
	if r == "" {
		r = o.Rule
	} else if o.Rule != "" && o.Rule != r {
		r = "multiple"
	}
	return Breakdown{
		Turnover: b.Turnover.Add(o.Turnover), Brokerage: b.Brokerage + o.Brokerage, STT: b.STT + o.STT,
		ExchTxn: b.ExchTxn + o.ExchTxn, SEBIFee: b.SEBIFee + o.SEBIFee, Stamp: b.Stamp + o.Stamp,
		GST: b.GST + o.GST, Total: b.Total + o.Total, Rule: r,
	}
}

// Compute prices one leg. Each component is rounded separately (half away
// from zero) and GST is levied on rounded brokerage + exchange charge + SEBI fee.
func (s Schedule) Compute(l Leg) (Breakdown, error) {
	side := strings.ToUpper(l.Side)
	if side != "B" && side != "S" {
		return Breakdown{}, fmt.Errorf("charges: side must be B or S, got %q", l.Side)
	}
	if l.Qty <= 0 {
		return Breakdown{}, fmt.Errorf("charges: quantity must be positive, got %d", l.Qty)
	}
	if l.Price.Sign() < 0 {
		return Breakdown{}, errors.New("charges: price cannot be negative")
	}
	r, err := s.Rule(l.Bucket, l.Exchange, l.Date)
	if err != nil {
		return Breakdown{}, err
	}
	v := l.Price.MulInt(l.Qty)
	var brok money.Dec
	switch mode := r.BrokerageMode; {
	case l.NoBrokerage:
	default:
		switch mode {
		case BrokFlat:
			brok = r.BrokerageFlat
		case BrokPct:
			brok = v.Pct(r.BrokeragePct)
		case BrokMinFlatPct:
			brok = v.Pct(r.BrokeragePct)
			if r.BrokerageFlat.Cmp(brok) < 0 {
				brok = r.BrokerageFlat
			}
		}
	}
	sttUnit, _ := money.ParseUnit(r.STTRound)
	stampUnit, _ := money.ParseUnit(r.StampRound)
	sttPct := r.STTSellPct
	if side == "B" {
		sttPct = r.STTBuyPct
	}
	b := Breakdown{
		Turnover:  v,
		Brokerage: brok.Round(money.UnitPaisa),
		STT:       v.Pct(sttPct).Round(sttUnit),
		ExchTxn:   v.Pct(r.TxnPct).Round(money.UnitPaisa),
		SEBIFee:   v.Pct(s.SEBIFeePct).Round(money.UnitPaisa),
		Rule:      fmt.Sprintf("%s/%s/%s/%s", s.Version, r.Bucket, r.Exchange, r.EffectiveFrom),
	}
	if side == "B" {
		b.Stamp = v.Pct(r.StampBuyPct).Round(stampUnit)
	}
	b.GST = (b.Brokerage + b.ExchTxn + b.SEBIFee).Dec().Pct(s.GSTPct).Round(money.UnitPaisa)
	b.Total = b.Brokerage + b.STT + b.ExchTxn + b.SEBIFee + b.Stamp + b.GST
	return b, nil
}
