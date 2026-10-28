package ops

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"ledgerline/internal/charges"
	"ledgerline/internal/ingest"
	"ledgerline/internal/money"
)

// ---- brokerage imports ----
//
// Four plain CSV files bring a firm's brokerage across from another back
// office (a converter for that system writes them):
//
//   BROKERAGE-CARDS          every rate card with its history, one row per rate line
//   BROKERAGE-CLIENTS        which card each client (any status) is on, by segment, with history
//   BROKERAGE-CLIENT-RATES   a client's own rate lines that win over their card
//   TERMINAL-CHANNELS        which terminals are which trading channel
//
// Loading a file twice changes nothing.

// rateColumns are the optional columns that describe a rate line.
var rateColumns = "Exchange, Channel and Case narrow a line (blank = all; Case is auction, rollover, expiry, exercise, assignment, index or stock); Mode is pct, flat, lower_of, per_lot, per_share or zero; Pct, Flat (₹ per order), PerLot, PerShare, MinPerShare, MinPerLot, Min and Max (₹ per order) as the mode needs"

// productCode reads a product as a code (CM-DEL) or its name (Equity delivery).
func productCode(s string) (charges.Bucket, bool) {
	s = strings.TrimSpace(s)
	for _, b := range charges.Buckets {
		if strings.EqualFold(s, string(b)) || strings.EqualFold(s, charges.BucketNames[b]) {
			return b, true
		}
	}
	return "", false
}

// rateLine reads one rate line from a row.
func rateLine(r row) (string, charges.BrokerageRule, error) {
	b, ok := productCode(r.get("Product"))
	if !ok {
		return "", charges.BrokerageRule{}, fmt.Errorf("Product %q: use a product code such as CM-DEL, CM-INTRA, FO-FUT, FO-OPT, CD-FUT, CD-OPT, COM-FUT, COM-OPT", r.get("Product"))
	}
	k, err := charges.NewRateKey(string(b), r.get("Exchange"), r.get("Channel"), r.get("Case"))
	if err != nil {
		return "", charges.BrokerageRule{}, err
	}
	mode := strings.ToLower(strings.TrimSpace(r.get("Mode")))
	switch mode {
	case "per_share", "per-share", "pershare":
		mode = charges.PlanPerUnit
	case "free":
		mode = charges.PlanZero
	case "lower-of", "lowerof":
		mode = charges.PlanLowerOf
	case "per-lot", "perlot":
		mode = charges.PlanPerLot
	}
	rule := charges.BrokerageRule{Mode: mode}
	for _, f := range []struct {
		col string
		d   *money.Dec
	}{{"Pct", &rule.Pct}, {"Flat", &rule.Flat}, {"PerLot", &rule.PerLot}, {"PerShare", &rule.PerUnit},
		{"MinPerShare", &rule.MinPerUnit}, {"MinPerLot", &rule.MinPerLot}, {"Min", &rule.Min}, {"Max", &rule.Max}} {
		v := strings.TrimSpace(r.get(f.col))
		if v == "" {
			continue
		}
		d, err := money.ParseDec(v)
		if err != nil {
			return "", rule, fmt.Errorf("%s must be a number, got %q", f.col, v)
		}
		*f.d = d
	}
	if mode == "" {
		return "", rule, fmt.Errorf("Mode is empty")
	}
	// A single line checked on its own, so the row number points at it.
	if _, err := cleanRules(map[string]charges.BrokerageRule{k.String(): rule}); err != nil {
		return "", rule, err
	}
	return k.String(), rule, nil
}

var reSlug = regexp.MustCompile(`[^a-z0-9]+`)

// cardID turns a card's name in the old system into a plan ID.
func cardID(card string) string {
	id := strings.Trim(reSlug.ReplaceAllString(strings.ToLower(strings.TrimSpace(card)), "-"), "-")
	if len(id) < 2 || !validPlanID.MatchString(id) {
		id = "card-" + id
	}
	if len(id) > 41 {
		id = strings.TrimRight(id[:41], "-")
	}
	return id
}

// findCard finds a card by plan ID, by name, or by the ID its old name maps to.
func findCard(ps map[string]Plan, card string) (string, bool) {
	card = strings.TrimSpace(card)
	if _, ok := ps[card]; ok {
		return card, true
	}
	if _, ok := ps[cardID(card)]; ok {
		return cardID(card), true
	}
	for id, p := range ps {
		if strings.EqualFold(p.Name, card) {
			return id, true
		}
	}
	return "", false
}

func (o *Ops) prepareBrokerage(ctx context.Context, tenant, actor, source string, rows []row) (func() (int, int, error), []ingest.ParseError) {
	var errs []ingest.ParseError
	bad := func(l int, f, m string, a ...any) {
		errs = append(errs, ingest.ParseError{Line: l, Field: f, Msg: fmt.Sprintf(m, a...)})
	}
	if err := o.require(ctx, tenant, actor, "Loading brokerage rates", RoleChecker); err != nil {
		bad(1, "", "%v", err)
		return nil, errs
	}
	switch source {
	case "BROKERAGE-CARDS", "BROKERAGE-CLIENT-RATES":
		cards := source == "BROKERAGE-CARDS"
		type ver struct {
			id, name, note, from string
			rules                map[string]charges.BrokerageRule
		}
		vers := map[string]*ver{}
		for _, r := range rows {
			from := strings.TrimSpace(r.get("From"))
			if !validDate(from) {
				bad(r.line, "From", "must be YYYY-MM-DD, got %q", from)
				continue
			}
			var id, name string
			if cards {
				name = strings.TrimSpace(firstOf(r.get("Name"), r.get("Card")))
				if strings.TrimSpace(r.get("Card")) == "" {
					bad(r.line, "Card", "is empty")
					continue
				}
				id = cardID(r.get("Card"))
			} else {
				id = strings.ToUpper(strings.TrimSpace(r.get("UCC")))
				if _, err := o.Client(ctx, tenant, id); err != nil {
					bad(r.line, "UCC", "%s is not a client; load the client master first", id)
					continue
				}
			}
			k := id + "|" + from
			v := vers[k]
			if v == nil {
				v = &ver{id: id, name: name, from: from, rules: map[string]charges.BrokerageRule{}}
				vers[k] = v
			}
			if n := strings.TrimSpace(r.get("Note")); n != "" {
				v.note = n
			}
			if strings.TrimSpace(r.get("Product")) == "" && strings.TrimSpace(r.get("Mode")) == "" {
				continue // a row that only dates a version with no lines (client rates ending)
			}
			key, rule, err := rateLine(r)
			if err != nil {
				bad(r.line, "", "%v", err)
				continue
			}
			if _, dup := v.rules[key]; dup {
				bad(r.line, "", "%s is given twice for %s from %s", key, id, from)
				continue
			}
			v.rules[key] = rule
		}
		keys := sortedKeys(vers)
		return func() (int, int, error) {
			n, same := 0, 0
			for _, k := range keys {
				v := vers[k]
				var changed bool
				var err error
				if cards {
					_, changed, err = o.savePlanVersion(ctx, tenant, actor, PlanInput{ID: v.id, Name: v.name, Note: v.note, From: v.from}, v.rules)
				} else {
					_, changed, err = o.saveOverrideChanged(ctx, tenant, actor, OverrideInput{UCC: v.id, From: v.from, Rules: v.rules, Note: v.note})
				}
				if err != nil {
					return n, same, fmt.Errorf("%s from %s: %w", v.id, v.from, err)
				}
				if changed {
					n++
				} else {
					same++
				}
			}
			return n, same, nil
		}, errs

	case "BROKERAGE-CLIENTS":
		ps, err := o.Plans(ctx, tenant)
		if err != nil {
			bad(1, "", "%v", err)
			return nil, errs
		}
		plans := map[string]Plan{}
		for _, p := range ps {
			plans[p.ID] = p
		}
		as, _ := o.Assignments(ctx, tenant)
		have := map[string]bool{}
		for _, a := range as {
			have[a.Target+"#"+a.Scope+"#"+a.PlanID+"#"+a.From] = true
		}
		cs, _ := o.ClientMap(ctx, tenant)
		var todo []PlanAssignment
		for _, r := range rows {
			who := strings.TrimSpace(r.get("UCC"))
			var target string
			switch up := strings.ToUpper(who); {
			case up == "DEFAULT" || up == "*":
				target = "default"
			case strings.HasPrefix(up, "GROUP:"):
				t := NormaliseTag(who[6:])
				if !validTag.MatchString(t) {
					bad(r.line, "UCC", "group %q is not a valid group name", who[6:])
					continue
				}
				target = "tag:" + t
			default:
				if _, ok := cs[up]; !ok {
					bad(r.line, "UCC", "%s is not a client; load the client master first", up)
					continue
				}
				target = "ucc:" + up
			}
			scope := strings.TrimSpace(r.get("Segment"))
			if x := strings.TrimSpace(r.get("Exchange")); x != "" && scope != "" && !strings.Contains(scope, "-") {
				scope += "-" + x
			}
			scope, err := normScope(scope)
			if err != nil {
				bad(r.line, "Segment", "%v", err)
				continue
			}
			from := strings.TrimSpace(r.get("From"))
			if !validDate(from) {
				bad(r.line, "From", "must be YYYY-MM-DD, got %q", from)
				continue
			}
			plan := ""
			if c := strings.TrimSpace(r.get("Card")); c != "" {
				id, ok := findCard(plans, c)
				if !ok {
					bad(r.line, "Card", "no card %q; load BROKERAGE-CARDS first", c)
					continue
				}
				plan = id
			} else if target == "default" && scope == "" {
				bad(r.line, "Card", "the firm default needs a card")
				continue
			}
			todo = append(todo, PlanAssignment{Target: target, Scope: scope, PlanID: plan, From: from})
		}
		sort.SliceStable(todo, func(i, j int) bool { return todo[i].From < todo[j].From })
		return func() (int, int, error) {
			n, same := 0, 0
			for _, a := range todo {
				k := a.Target + "#" + a.Scope + "#" + a.PlanID + "#" + a.From
				if have[k] {
					same++
					continue
				}
				if _, err := o.assignAs(ctx, tenant, actor, a); err != nil {
					return n, same, fmt.Errorf("%s: %w", a.Target, err)
				}
				have[k] = true
				n++
			}
			return n, same, nil
		}, errs

	case "TERMINAL-CHANNELS":
		setup, err := o.BrokerageSetup(ctx, tenant)
		if err != nil {
			bad(1, "", "%v", err)
			return nil, errs
		}
		var ts []TerminalChannel
		chans := append([]string{}, setup.Channels...)
		seen := map[string]bool{}
		for _, c := range chans {
			seen[c] = true
		}
		for _, r := range rows {
			t := TerminalChannel{Terminal: strings.TrimSpace(r.get("Terminal")), Channel: strings.ToLower(strings.TrimSpace(r.get("Channel")))}
			if t.Terminal == "" {
				bad(r.line, "Terminal", "is empty")
				continue
			}
			if !charges.ValidChannel(t.Channel) {
				bad(r.line, "Channel", "%q: use 2 to 20 lower-case letters, digits or dashes", t.Channel)
				continue
			}
			if !seen[t.Channel] {
				seen[t.Channel] = true
				chans = append(chans, t.Channel)
			}
			ts = append(ts, t)
		}
		return func() (int, int, error) {
			setup.Channels, setup.Terminals = chans, ts
			if _, err := o.saveBrokerageSetup(ctx, tenant, actor, setup); err != nil {
				return 0, 0, err
			}
			return len(ts), 0, nil
		}, errs
	}
	bad(1, "", "unknown brokerage file %s", source)
	return nil, errs
}

// brokerageSources are the brokerage import formats.
func brokerageSources() []masterSrc {
	return []masterSrc{
		{"BROKERAGE-CARDS", "Rate cards with their history, one row per rate line (all the lines of a card with the same From make that version). Card is the card's code in the old system; optional Name and Note. " + rateColumns, []string{"Card", "From", "Product", "Mode"}, false, "Once at go-live, then on change"},
		{"BROKERAGE-CLIENTS", "Which card each client is on, for every client whatever their status (active, inactive, suspended or closed), with history. UCC is a client code, DEFAULT for the firm default or GROUP:<name> for a client group; optional Segment (CM, FO, CD, COM) and Exchange put the card on one segment or one exchange only; a blank Card ends the earlier card from the date", []string{"UCC", "Card", "From"}, false, "Once at go-live, then on change"},
		{"BROKERAGE-CLIENT-RATES", "A client's own rate lines, which win over their card for the orders they fit; all the lines of a client with the same From make that version, and a row with no Product and Mode ends them from the date. " + rateColumns, []string{"UCC", "From", "Product", "Mode"}, false, "Once at go-live, then on change"},
		{"TERMINAL-CHANNELS", "Which trading channel each terminal's trades come through (replaces the list). Terminal is the terminal or CTCL ID in the exchange trade file, * matching any run of characters; Channel is a channel name such as dealer, online, mobile or algo", []string{"Terminal", "Channel"}, false, "Once, then on change"},
	}
}
