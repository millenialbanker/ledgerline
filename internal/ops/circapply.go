package ops

import (
	"regexp"
	"strings"
)

// ---- does a circular apply to the firm? ----
//
// Every circular gets one of three answers, with the reason in words:
// applies ("you are a CDSL DP"), does not apply ("you have no MTF") or not
// sure ("tell Ledgerline your CSCRF category"). The rules read who issued
// it, who it is addressed to, the segments and topics its subject names,
// and the firm profile (firmprofile.go):
//
//   - an exchange's circular is for its members; a clearing corporation's
//     for the members who clear there; a depository's for its
//     participants;
//   - a circular addressed to portfolio managers, investment advisers,
//     mutual funds or market infrastructure institutions only is not for a
//     stock broker unless the firm holds that registration;
//   - a subject about a segment the firm does not trade (commodity,
//     currency or equity derivatives), or about MTF, algo trading, NRI
//     clients, authorised persons, branches, BSDA or CSCRF when the firm
//     has none of it, does not apply;
//   - when the answer turns on something the profile does not know, the
//     answer is not sure, and says what to tell Ledgerline.
//
// The rules decide only what is shown and where the cards go; they never
// close a circular. The compliance officer can decide otherwise, with the
// reason, which goes to the audit trail (ActOnCircular "applies"), and a
// circular stays to review until they have looked at it.

// Applicability answers.
const (
	ApplyYes    = "applies"
	ApplyNo     = "no"
	ApplyUnsure = "unsure"
)

// Applicability is whether a circular applies, and why.
type Applicability struct {
	Verdict string   `json:"verdict"` // applies, no, unsure
	Reason  string   `json:"reason"`  // the deciding reason, e.g. "you are a CDSL DP"
	Reasons []string `json:"reasons"` // every reason the rules found
	// The compliance officer's decision, when they made one.
	Overridden bool   `json:"overridden,omitempty"`
	Rules      string `json:"rules,omitempty"`     // what the rules say, when overridden
	RulesWhy   string `json:"rules_why,omitempty"` // and why
	By         string `json:"by,omitempty"`
	At         string `json:"at,omitempty"`
}

// Label is the answer as people read it: "Applies: you are a CDSL DP".
func (a Applicability) Label() string {
	p := map[string]string{ApplyYes: "Applies", ApplyNo: "Doesn't apply", ApplyUnsure: "Not sure"}[a.Verdict]
	if a.Reason == "" {
		return p
	}
	return p + ": " + a.Reason
}

// ApplicabilityOverride is the compliance officer's decision.
type ApplicabilityOverride struct {
	Verdict  string `json:"verdict"` // applies or no
	Reason   string `json:"reason"`
	By       string `json:"by"`
	At       string `json:"at"`
	Rules    string `json:"rules"`               // what the rules said then
	RulesWhy string `json:"rules_why,omitempty"` // and why
}

// circSig is one thing the rules found: +1 applies, -1 does not, 0 not sure.
type circSig struct {
	v      int
	strong bool
	why    string
}

type circGroup struct {
	key, name string
	re        *regexp.Regexp
}

var (
	// The "To" block of a SEBI circular: "To, All Recognised Stock
	// Exchanges ... All Stock Brokers ... Dear Sir / Madam".
	reCircTo     = regexp.MustCompile(`(?s)\bTo(?:\s*[,:]|[ \t]*\r?\n)\s*(.{5,900}?)(?i:\bDear\b|\bMadam\b|\bSir\b|\bSub(?:ject)?\s*[:.\-]|\bRef(?:erence)?\s*[:.\-])`)
	reCircAll    = regexp.MustCompile(`(?i)\ball\s+(?:the\s+)?(?:sebi[- ]registered\s+|registered\s+|recogni[sz]ed\s+)?[a-z&/ ()-]{3,80}`)
	reCircBroker = regexp.MustCompile(`(?i)\b(stock brokers?|trading members?|margin pledge|pledges?|CUSPA)\b`)

	// Who a circular is addressed to.
	circGroups = []circGroup{
		{"broker", "stock brokers", regexp.MustCompile(`(?i)\b(stock[ -]?brokers?|trading members?|members of (?:the )?(?:stock )?exchanges?|all members|registered brokers?|brokers)\b`)},
		{"dp", "depository participants", regexp.MustCompile(`(?i)\b(depository participants?|DPs)\b`)},
		{"cm", "clearing members", regexp.MustCompile(`(?i)\b(clearing members?|professional clearing members?)\b`)},
		{"pms", "portfolio managers", regexp.MustCompile(`(?i)\bportfolio managers?\b`)},
		{"ia", "investment advisers", regexp.MustCompile(`(?i)\binvestment advis[eo]rs?\b`)},
		{"ra", "research analysts", regexp.MustCompile(`(?i)\bresearch analysts?\b`)},
		{"mb", "merchant bankers", regexp.MustCompile(`(?i)\bmerchant bankers?\b`)},
		{"mfd", "mutual fund distributors", regexp.MustCompile(`(?i)\bmutual fund distributors?\b`)},
		{"mf", "mutual funds", regexp.MustCompile(`(?i)\b(asset management compan(?:y|ies)|AMCs?|mutual funds)\b`)},
		{"mii", "stock exchanges, clearing corporations and depositories", regexp.MustCompile(`(?i)\b(stock exchanges?|clearing corporations?|depositories|market infrastructure institutions?|MIIs?)\b`)},
		{"other", "other intermediaries", regexp.MustCompile(`(?i)\b(registrars? to (?:an )?issue|share transfer agents?|credit rating agenc(?:y|ies)|alternative investment funds?|listed (?:entities|companies)|debenture trustees?|bankers to (?:an )?issue|KYC registration agenc(?:y|ies)|custodians?|foreign portfolio investors?|vault managers?)\b`)},
	}

	// Segments a subject can be about.
	circSegs = []struct {
		seg, name string
		re        *regexp.Regexp
	}{
		{"CM", "the cash market", regexp.MustCompile(`(?i)\b(cash (?:market|segment)|capital market segment|equity (?:cash|segment)|CM segment)\b`)},
		{"FO", "equity derivatives", regexp.MustCompile(`(?i)(\bequity derivatives?\b|\bF&O\b|\bfutures (?:&|and) options\b|\bequity F&O\b|\bFO segment\b)`)},
		{"CD", "currency derivatives", regexp.MustCompile(`(?i)\b(currency derivatives?|currency (?:segment|futures|options)|interest rate (?:futures|derivatives))\b`)},
		{"COM", "commodity derivatives", regexp.MustCompile(`(?i)\bcommodit(?:y|ies)\b`)},
	}

	reCircMTF    = regexp.MustCompile(`(?i)(\bmargin trading\b|\bMTF\b)`)
	reCircAlgo   = regexp.MustCompile(`(?i)(\balgo(?:rithmic)?(?:\s+trading)?\b|\balgos\b|\bAPI (?:access|trading|based)\b)`)
	reCircNRI    = regexp.MustCompile(`(?i)(\bNRIs?\b|\bnon[- ]resident)`)
	reCircAP     = regexp.MustCompile(`(?i)(\bauthori[sz]ed persons?\b|\bsub[- ]?brokers?\b)`)
	reCircBranch = regexp.MustCompile(`(?i)\bbranch(?:es)?\b`)
	reCircPMS    = regexp.MustCompile(`(?i)\bportfolio manage(?:r|rs|ment)\b`)
	reCircBSDA   = regexp.MustCompile(`(?i)(\bBSDA\b|\bbasic services? demat)`)
	reCircCSCRF  = regexp.MustCompile(`(?i)(\bCSCRF\b|cyber ?security and cyber resilience|cyber resilience framework)`)
	reCircCats   = map[string]*regexp.Regexp{
		"qualified": regexp.MustCompile(`(?i)\bqualified (?:REs?|regulated entit)`),
		"mid":       regexp.MustCompile(`(?i)\bmid[- ]size (?:REs?|regulated entit)`),
		"small":     regexp.MustCompile(`(?i)\bsmall[- ]size (?:REs?|regulated entit)`),
		"self":      regexp.MustCompile(`(?i)\bself[- ]certification (?:REs?|regulated entit)`),
	}
)

// circIssuer sorts a circular's source: exchange, clearing, depository,
// regulator or other, with its code.
func circIssuer(source string) (kind, code string) {
	s := strings.ToUpper(strings.TrimSpace(source))
	switch s {
	case "NSE", "BSE", "MCX", "NCDEX", "MSE":
		return "exchange", s
	case "MSEI":
		return "exchange", "MSE"
	case "NCL", "NSCCL", "NSE CLEARING", "NSECLEARING":
		return "clearing", "NCL"
	case "ICCL", "MCXCCL", "NCCL", "MCCIL":
		return "clearing", s
	case "CDSL", "NSDL":
		return "depository", s
	case "SEBI":
		return "regulator", s
	}
	return "other", s
}

// circAddressees finds who a circular is addressed to.
func circAddressees(c Circular) []circGroup {
	block := ""
	head := c.Text
	if len(head) > 2500 {
		head = head[:2500]
	}
	if m := reCircTo.FindStringSubmatch(head); m != nil {
		block = m[1]
	} else {
		lead := head
		if len(lead) > 600 {
			lead = lead[:600]
		}
		block = strings.Join(reCircAll.FindAllString(lead, -1), "; ")
	}
	if block == "" {
		return nil
	}
	var out []circGroup
	has := map[string]bool{}
	for _, g := range circGroups {
		if g.re.MatchString(block) {
			out, has[g.key] = append(out, g), true
		}
	}
	if has["mfd"] { // "mutual fund distributors" is not addressed to the funds
		var keep []circGroup
		for _, g := range out {
			if g.key != "mf" {
				keep = append(keep, g)
			}
		}
		out = keep
	}
	return out
}

// circApplicability works out whether a circular applies to the firm.
func circApplicability(c Circular, f firmFacts) Applicability {
	var sigs []circSig
	add := func(v int, strong bool, why string) { sigs = append(sigs, circSig{v, strong, why}) }
	subj := c.Subject + " · " + c.Dept
	all := c.Subject + ". " + c.Text

	// Who issued it.
	kind, code := circIssuer(c.Source)
	switch kind {
	case "exchange":
		if f.Member[code] {
			add(+1, true, "you are a member of "+code)
		} else {
			add(-1, true, "you are not a member of "+code)
		}
	case "clearing":
		name := firstOf(ClearingCorpNames[code], code)
		switch {
		case f.SelfAt[code]:
			add(+1, true, "you clear your own trades at "+name)
		case f.ViaCM[code] != "":
			add(0, true, f.ViaCM[code]+" clears your trades at "+name+" and gets this circular; it may still change what you collect from clients")
		default:
			add(-1, true, "none of your segments is cleared at "+name)
		}
	case "depository":
		dp := map[string]tri{"CDSL": f.CDSL, "NSDL": f.NSDL}[code]
		switch dp {
		case triYes:
			add(+1, true, "you are a "+code+" DP")
		case triUnknown:
			add(0, true, "tell Ledgerline whether you are a "+code+" DP (Settings → Firm profile)")
		default:
			if reCircBroker.MatchString(all) {
				add(0, true, "you are not a "+code+" DP, but it mentions stock brokers or pledges")
			} else {
				add(-1, true, "you are not a "+code+" DP")
			}
		}
	}

	// Who it is addressed to.
	if gs := circAddressees(c); len(gs) > 0 {
		var hit, miss, ask []string
		for _, g := range gs {
			switch g.key {
			case "broker":
				hit = append(hit, "it is addressed to stock brokers")
			case "dp":
				switch f.dp() {
				case triYes:
					hit = append(hit, "it is addressed to depository participants and you are one")
				case triUnknown:
					ask = append(ask, "it is addressed to depository participants: tell Ledgerline whether you are one (Settings → Firm profile)")
				default:
					miss = append(miss, g.name)
				}
			case "cm":
				if len(f.SelfAt) > 0 {
					hit = append(hit, "it is addressed to clearing members and you clear your own trades")
				} else {
					miss = append(miss, g.name)
				}
			case "pms", "ia", "ra", "mb", "mfd":
				if f.Regs[g.key] {
					hit = append(hit, "it is addressed to "+g.name+" and you are one")
				} else {
					miss = append(miss, g.name)
				}
			default:
				miss = append(miss, g.name)
			}
		}
		switch {
		case len(hit) > 0:
			add(+1, true, hit[0])
		case len(ask) > 0:
			add(0, true, ask[0])
		default:
			add(-1, true, "it is addressed to "+joinAnd(miss)+", and the firm is not one of them")
		}
	}

	// Segments the subject names.
	var named, traded []string
	for _, s := range circSegs {
		if s.re.MatchString(subj) {
			named = append(named, s.name)
			if f.Seg[s.seg] {
				traded = append(traded, s.name)
			}
		}
	}
	switch {
	case len(named) > 0 && len(traded) == 0:
		add(-1, true, "you do not trade "+joinAnd(named))
	case len(traded) > 0:
		add(+1, false, "you trade "+joinAnd(traded))
	}

	// Topics the subject is about.
	topic := func(re *regexp.Regexp, have tri, yes, no, ask string) {
		if !re.MatchString(subj) {
			return
		}
		switch have {
		case triYes:
			add(+1, true, yes)
		case triNo:
			add(-1, true, no)
		default:
			if ask != "" {
				add(0, true, ask)
			}
		}
	}
	topic(reCircMTF, f.MTF, "you offer MTF", "you have no MTF", "")
	topic(reCircAlgo, f.Algo, "you run or offer algo trading", "you offer no algo trading", "tell Ledgerline whether you run or offer algo trading (Settings → Firm profile)")
	topic(reCircNRI, f.NRI, "you have NRI clients", "you have no NRI clients", "tell Ledgerline whether you have NRI clients (Settings → Firm profile)")
	topic(reCircAP, f.AP, "you have authorised persons", "you have no authorised persons", "")
	topic(reCircBranch, f.Branch, "you have branch offices", "you have no branch offices", "")
	if reCircPMS.MatchString(subj) {
		if f.Regs["pms"] {
			add(+1, true, "you are a portfolio manager")
		} else {
			add(-1, true, "you are not a portfolio manager")
		}
	}
	topic(reCircBSDA, f.dp(), "you are a depository participant", "you are not a depository participant", "tell Ledgerline whether you are a depository participant (Settings → Firm profile)")
	if reCircCSCRF.MatchString(subj) || reCircCSCRF.MatchString(c.Text) && kind == "regulator" {
		switch {
		case f.CSCRF == "":
			add(0, true, "tell Ledgerline your CSCRF category (Settings → Firm profile)")
		case f.CSCRF == "exempt":
			add(-1, true, "you are below the CSCRF thresholds")
		default:
			var cats []string
			for _, x := range CSCRFCategories {
				if re := reCircCats[x.ID]; re != nil && re.MatchString(all) {
					cats = append(cats, x.ID)
				}
			}
			if len(cats) > 0 && !contains(cats, f.CSCRF) {
				var names []string
				for _, x := range cats {
					names = append(names, cscrfName(x)+"s")
				}
				add(0, true, "it names "+joinAnd(names)+"; you are a "+cscrfName(f.CSCRF))
			} else {
				add(+1, true, "you are a "+cscrfName(f.CSCRF)+" under CSCRF")
			}
		}
	}

	// A SEBI circular with no addressee found: what it is about.
	if kind == "regulator" && !hasStrong(sigs) {
		var about []string
		for _, a := range c.Areas {
			if a != "General" && a != "Market operations" {
				about = append(about, a)
			}
		}
		if len(about) > 0 {
			add(+1, false, "it is about "+strings.ToLower(joinAnd(about)))
		}
	}

	a := Applicability{Reasons: []string{}}
	for _, s := range sigs {
		a.Reasons = append(a.Reasons, s.why)
	}
	pick := func(v int, strong bool) (string, bool) {
		for _, s := range sigs {
			if s.v == v && (!strong || s.strong) {
				return s.why, true
			}
		}
		return "", false
	}
	if why, ok := pick(-1, true); ok {
		a.Verdict, a.Reason = ApplyNo, why
	} else if why, ok := pick(0, true); ok {
		a.Verdict, a.Reason = ApplyUnsure, why
	} else if why, ok := pick(+1, true); ok {
		a.Verdict, a.Reason = ApplyYes, why
	} else {
		a.Verdict, a.Reason = ApplyUnsure, "Ledgerline could not tell who it is for"
		if why, ok := pick(+1, false); ok {
			a.Reason += "; " + why
		}
	}
	if f.Assumed && a.Verdict == ApplyYes && kind == "exchange" {
		a.Verdict, a.Reason = ApplyUnsure, "no member codes in Settings yet, so Ledgerline cannot tell whether you are a member of "+code
	}
	if o := c.Override; o != nil {
		a.Rules, a.RulesWhy = a.Verdict, a.Reason
		a.Verdict, a.Reason, a.Overridden, a.By, a.At = o.Verdict, o.Reason, true, o.By, o.At
	}
	return a
}

func hasStrong(sigs []circSig) bool {
	for _, s := range sigs {
		if s.strong {
			return true
		}
	}
	return false
}

// joinAnd is "a, b and c".
func joinAnd(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}
