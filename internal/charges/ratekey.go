package charges

import (
	"fmt"
	"regexp"
	"strings"
)

// ---- rate lines: one product, narrowed by exchange, channel, case and scrip ----
//
// A brokerage card holds rate lines keyed "PRODUCT|EXCHANGE|CHANNEL|CASE",
// or "PRODUCT|EXCHANGE|CHANNEL|CASE|TARGET" for a line that applies to one
// scrip, one scrip group or one underlying only, any part after the product
// being "*" for "all". A key of the product alone ("CM-DEL") is the same as
// "CM-DEL|*|*|*". When an order is priced, the most specific line that fits
// wins, in this order:
//
//  1. the target: a line for the scrip itself (symbol or ISIN), then one for
//     a scrip group the scrip is in, then one for the underlying (NIFTY),
//     then lines for no particular scrip;
//  2. the case: auction, rollover, expiry and the other trade types, then
//     index or stock derivatives, then no case;
//  3. the channel: the order's channel, then all channels;
//  4. the exchange: the order's exchange, then all exchanges.
//
// So "options on NIFTY" beats "index options online", which beats "index
// options", which beats "options".

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
	CasePhysical   = "physical"   // stock derivative settled by delivery at expiry
)

// Targets narrow a line to one scrip, one scrip group or one underlying.
const (
	TargetScrip      = "scrip:" // a cash-market scrip by symbol or ISIN
	TargetGroup      = "group:" // a scrip group the firm keeps (ETFs, a special list)
	TargetUnderlying = "und:"   // a derivative's underlying, e.g. NIFTY
)

// CaseNames are display names, in display order.
var CaseNames = []struct{ Case, Name string }{
	{CaseAuction, "Auction"}, {CaseRollover, "Rollover"}, {CaseExpiry, "Futures expiry"},
	{CaseExercise, "Option exercise"}, {CaseAssignment, "Option assignment"},
	{CaseIndex, "Index derivatives"}, {CaseStock, "Stock derivatives"},
	{CasePhysical, "Physical delivery"},
}

// SettlementCases are cases priced only when they happen (at expiry, by the
// settlement run), never by falling back to the product's ordinary rate.
var SettlementCases = map[string]bool{CaseExpiry: true, CaseExercise: true, CaseAssignment: true, CasePhysical: true}

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
	case CaseIndex, CaseStock, CasePhysical:
		return isFut(b) || isOpt(b)
	}
	return false
}

var (
	validScrip = regexp.MustCompile(`^[A-Z0-9][A-Z0-9&._-]{0,29}$`)
	validGroup = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{0,29}$`)
)

// ValidGroupName says whether a scrip group name is usable (upper case
// letters, digits, - or _, up to 30).
func ValidGroupName(g string) bool { return validGroup.MatchString(g) }

// ValidScrip says whether a symbol or ISIN is usable in a line or a group.
func ValidScrip(s string) bool { return validScrip.MatchString(s) }

// NormTarget reads a target as written ("scrip:RELIANCE", "group:ETF",
// "und:NIFTY", also "underlying:NIFTY"; "" or "*" is none) and checks it.
func NormTarget(t string) (string, error) {
	t = strings.TrimSpace(t)
	if t == "" || t == Any || strings.EqualFold(t, "all") {
		return Any, nil
	}
	kind, name, ok := strings.Cut(t, ":")
	bad := fmt.Errorf("target %q: write scrip:<symbol or ISIN>, group:<name> or und:<underlying>", t)
	if !ok {
		return "", bad
	}
	name = strings.ToUpper(strings.TrimSpace(name))
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "scrip", "symbol", "isin":
		if !validScrip.MatchString(name) {
			return "", fmt.Errorf("scrip %q is not a symbol or ISIN", name)
		}
		return TargetScrip + name, nil
	case "group":
		if !validGroup.MatchString(name) {
			return "", fmt.Errorf("scrip group %q: use letters, digits, - or _", name)
		}
		return TargetGroup + name, nil
	case "und", "underlying":
		if !validScrip.MatchString(name) {
			return "", fmt.Errorf("underlying %q is not a symbol", name)
		}
		return TargetUnderlying + name, nil
	}
	return "", bad
}

// TargetLabel is a target for people: "scrip RELIANCE", "group ETF",
// "underlying NIFTY".
func TargetLabel(t string) string {
	switch {
	case strings.HasPrefix(t, TargetScrip):
		return "scrip " + strings.TrimPrefix(t, TargetScrip)
	case strings.HasPrefix(t, TargetGroup):
		return "group " + strings.TrimPrefix(t, TargetGroup)
	case strings.HasPrefix(t, TargetUnderlying):
		return "underlying " + strings.TrimPrefix(t, TargetUnderlying)
	}
	return t
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
	// Target is a scrip, scrip group or underlying ("" or "*" for none).
	Target string `json:"target,omitempty"`
}

func (k RateKey) target() string {
	if k.Target == "" {
		return Any
	}
	return k.Target
}

// String is the canonical key; all-wildcard lines are the product alone,
// and the target part is written only when the line has one.
func (k RateKey) String() string {
	if k.target() != Any {
		return strings.Join([]string{string(k.Bucket), k.Exchange, k.Channel, k.Case, k.target()}, "|")
	}
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
	if k.target() != Any {
		s = append(s, TargetLabel(k.target()))
	}
	return strings.Join(s, " · ")
}

// NewRateKey normalises and checks the parts of a key ("" means all).
func NewRateKey(product, exchange, channel, kase string) (RateKey, error) {
	return NewTargetKey(product, exchange, channel, kase, "")
}

// NewTargetKey is NewRateKey with a target: a scrip, scrip group or
// underlying (see NormTarget).
func NewTargetKey(product, exchange, channel, kase, target string) (RateKey, error) {
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
	t, err := NormTarget(target)
	if err != nil {
		return k, err
	}
	if t != Any {
		k.Target = t
	}
	cash := k.Bucket == CMDelivery || k.Bucket == CMIntraday
	if strings.HasPrefix(t, TargetUnderlying) && cash {
		return k, fmt.Errorf("an underlying applies to derivatives; use scrip:%s for %s", strings.TrimPrefix(t, TargetUnderlying), BucketNames[k.Bucket])
	}
	if strings.HasPrefix(t, TargetScrip) && !cash {
		return k, fmt.Errorf("a scrip applies to the cash market; use und:%s for %s", strings.TrimPrefix(t, TargetScrip), BucketNames[k.Bucket])
	}
	return k, nil
}

// ParseRateKey reads "CM-DEL", "CM-DEL|NSE|online|*" or
// "FO-OPT|*|*|*|und:NIFTY".
func ParseRateKey(s string) (RateKey, error) {
	p := strings.Split(s, "|")
	switch len(p) {
	case 1:
		return NewRateKey(p[0], "", "", "")
	case 4:
		return NewRateKey(p[0], p[1], p[2], p[3])
	case 5:
		return NewTargetKey(p[0], p[1], p[2], p[3], p[4])
	}
	return RateKey{}, fmt.Errorf("rate key %q: want PRODUCT, PRODUCT|EXCHANGE|CHANNEL|CASE or PRODUCT|EXCHANGE|CHANNEL|CASE|TARGET", s)
}

// Targets are the scrip, the scrip groups it is in and the underlying of an
// order, for lines narrowed to them.
type Targets struct {
	Scrips     []string // cash market: symbol and ISIN
	Groups     []string // scrip groups the scrip is in, the one that wins first
	Underlying string   // derivatives: the underlying symbol
}

// list is the target parts that fit, most specific first, ending with none.
func (t Targets) list() []string {
	var out []string
	for _, s := range t.Scrips {
		if s = strings.ToUpper(strings.TrimSpace(s)); s != "" {
			out = append(out, TargetScrip+s)
		}
	}
	for _, g := range t.Groups {
		if g = strings.ToUpper(strings.TrimSpace(g)); g != "" {
			out = append(out, TargetGroup+g)
		}
	}
	if u := strings.ToUpper(strings.TrimSpace(t.Underlying)); u != "" {
		out = append(out, TargetUnderlying+u)
	}
	return append(out, Any)
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
	// Targets, when known, let lines for the scrip, its groups or the
	// underlying apply.
	Targets
}

// Keys lists the keys that fit, most specific first: target, then case,
// then channel, then exchange.
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
	for _, tg := range q.Targets.list() {
		for _, c := range cases {
			for _, ch := range chans {
				for _, x := range exchs {
					out = append(out, RateKey{q.Bucket, x, ch, c, tg}.String())
				}
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
