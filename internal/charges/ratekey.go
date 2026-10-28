package charges

import (
	"fmt"
	"regexp"
	"strings"
)

// ---- rate lines: one product, narrowed by exchange, channel and case ----
//
// A brokerage card holds rate lines keyed "PRODUCT|EXCHANGE|CHANNEL|CASE",
// any part after the product being "*" for "all". A key of the product alone
// ("CM-DEL") is the same as "CM-DEL|*|*|*". When an order is priced, the
// most specific line that fits wins: a matching case first, then a matching
// channel, then a matching exchange.

// Any is the wildcard part of a rate key.
const Any = "*"

// Cases are the special kinds of trade a card can price separately.
const (
	CaseAuction    = "auction"    // cash-market auction (short delivery close-out)
	CaseRollover   = "rollover"   // futures position moved to a later expiry the same day
	CaseExpiry     = "expiry"     // futures settled at expiry
	CaseExercise   = "exercise"   // long option exercised at expiry
	CaseAssignment = "assignment" // short option assigned at expiry
	CaseIndex      = "index"      // index derivatives
	CaseStock      = "stock"      // stock derivatives
)

// CaseNames are display names, in display order.
var CaseNames = []struct{ Case, Name string }{
	{CaseAuction, "Auction"}, {CaseRollover, "Rollover"}, {CaseExpiry, "Futures expiry"},
	{CaseExercise, "Option exercise"}, {CaseAssignment, "Option assignment"},
	{CaseIndex, "Index derivatives"}, {CaseStock, "Stock derivatives"},
}

// SettlementCases are cases priced only when they happen (at expiry, by the
// settlement run), never by falling back to the product's ordinary rate.
var SettlementCases = map[string]bool{CaseExpiry: true, CaseExercise: true, CaseAssignment: true}

func isFut(b Bucket) bool { return b == FOFut || b == CDFut || b == COMFut }
func isOpt(b Bucket) bool { return b == FOOpt || b == CDOpt || b == COMOpt }

// CaseFits says whether a case applies to a product.
func CaseFits(c string, b Bucket) bool {
	switch c {
	case Any:
		return true
	case CaseAuction:
		return b == CMDelivery || b == CMIntraday
	case CaseRollover, CaseExpiry:
		return isFut(b)
	case CaseExercise, CaseAssignment:
		return isOpt(b)
	case CaseIndex, CaseStock:
		return isFut(b) || isOpt(b)
	}
	return false
}

var (
	validExchange = regexp.MustCompile(`^[A-Z]{2,8}$`)
	validChannel  = regexp.MustCompile(`^[a-z][a-z0-9-]{1,19}$`)
)

// ValidChannel says whether a channel name is usable (lower case, 2 to 20
// letters, digits or dashes, such as dealer, online, mobile or algo).
func ValidChannel(c string) bool { return validChannel.MatchString(c) }

// RateKey is a parsed rate-line key.
type RateKey struct {
	Bucket   Bucket `json:"product"`
	Exchange string `json:"exchange"`
	Channel  string `json:"channel"`
	Case     string `json:"case"`
}

// String is the canonical key; all-wildcard lines are the product alone.
func (k RateKey) String() string {
	if k.Exchange == Any && k.Channel == Any && k.Case == Any {
		return string(k.Bucket)
	}
	return strings.Join([]string{string(k.Bucket), k.Exchange, k.Channel, k.Case}, "|")
}

// Describe is a short label such as "Equity options · NSE · online · Index derivatives".
func (k RateKey) Describe() string {
	s := []string{BucketNames[k.Bucket]}
	if k.Exchange != Any {
		s = append(s, k.Exchange)
	}
	if k.Channel != Any {
		s = append(s, k.Channel)
	}
	if k.Case != Any {
		for _, c := range CaseNames {
			if c.Case == k.Case {
				s = append(s, c.Name)
			}
		}
	}
	return strings.Join(s, " · ")
}

// NewRateKey normalises and checks the parts of a key ("" means all).
func NewRateKey(product, exchange, channel, kase string) (RateKey, error) {
	norm := func(s string) string {
		s = strings.TrimSpace(s)
		if s == "" || strings.EqualFold(s, "all") {
			return Any
		}
		return s
	}
	k := RateKey{Bucket: Bucket(strings.ToUpper(strings.TrimSpace(product))), Exchange: strings.ToUpper(norm(exchange)), Channel: strings.ToLower(norm(channel)), Case: strings.ToLower(norm(kase))}
	if BucketNames[k.Bucket] == "" {
		return k, fmt.Errorf("unknown product %q", product)
	}
	if k.Exchange != Any && !validExchange.MatchString(k.Exchange) {
		return k, fmt.Errorf("unknown exchange %q", exchange)
	}
	if k.Channel != Any && !ValidChannel(k.Channel) {
		return k, fmt.Errorf("channel %q: use 2 to 20 lower-case letters, digits or dashes", channel)
	}
	known := k.Case == Any
	for _, c := range CaseNames {
		known = known || c.Case == k.Case
	}
	if !known {
		return k, fmt.Errorf("unknown case %q", kase)
	}
	if !CaseFits(k.Case, k.Bucket) {
		return k, fmt.Errorf("%s does not apply to %s", k.Case, BucketNames[k.Bucket])
	}
	return k, nil
}

// ParseRateKey reads "CM-DEL" or "CM-DEL|NSE|online|*".
func ParseRateKey(s string) (RateKey, error) {
	p := strings.Split(s, "|")
	switch len(p) {
	case 1:
		return NewRateKey(p[0], "", "", "")
	case 4:
		return NewRateKey(p[0], p[1], p[2], p[3])
	}
	return RateKey{}, fmt.Errorf("rate key %q: want PRODUCT or PRODUCT|EXCHANGE|CHANNEL|CASE", s)
}

// RateQuery is what an order looks like to a card.
type RateQuery struct {
	Bucket   Bucket
	Exchange string
	Channel  string   // "" when unknown
	Cases    []string // most specific first, e.g. [rollover index]
	// Strict prices only lines for one of Cases: a settlement charge that
	// has no line of its own is not charged at the ordinary rate.
	Strict bool
}

// Keys lists the keys that fit, most specific first: case, then channel,
// then exchange.
func (q RateQuery) Keys() []string {
	cases := append([]string{}, q.Cases...)
	if !q.Strict {
		cases = append(cases, Any)
	}
	chans := []string{Any}
	if q.Channel != "" && q.Channel != Any {
		chans = []string{q.Channel, Any}
	}
	exchs := []string{Any}
	if q.Exchange != "" && q.Exchange != Any {
		exchs = []string{strings.ToUpper(q.Exchange), Any}
	}
	var out []string
	for _, c := range cases {
		for _, ch := range chans {
			for _, x := range exchs {
				out = append(out, RateKey{q.Bucket, x, ch, c}.String())
			}
		}
	}
	return out
}

// Lookup finds the most specific line in rules for q.
func Lookup(rules map[string]BrokerageRule, q RateQuery) (BrokerageRule, string, bool) {
	for _, k := range q.Keys() {
		if r, ok := rules[k]; ok {
			return r, k, true
		}
	}
	return BrokerageRule{}, "", false
}

// IndexUnderlyings are the index derivative underlyings recognised without
// configuration; a firm can add more in its settings.
var IndexUnderlyings = []string{"NIFTY", "BANKNIFTY", "FINNIFTY", "MIDCPNIFTY", "NIFTYNXT50", "SENSEX", "BANKEX", "SENSEX50"}
