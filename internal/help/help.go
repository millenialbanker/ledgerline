// Package help is Ledgy's knowledge: short articles on every screen and
// daily task, the reconciliation break playbooks, and who may do what. It
// searches them for a question in the user's own words, preferring
// articles about the screen the user is on.
//
// Articles live in config/help/*.md. Each starts with "## <id>", then
// "key: value" lines (title, screen, roles, ask, check, related), a blank
// line, and the body in a small markdown subset: numbered and bulleted
// lists, **bold**, `code`, and [[screen|Label]] links that open a screen.
package help

import (
	"bufio"
	"fmt"
	"io/fs"
	"math"
	"path"
	"regexp"
	"sort"
	"strings"

	"ledgerline/internal/recon"
)

// Article is one help topic.
type Article struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Area    string   `json:"area"`
	Screen  string   `json:"screen,omitempty"` // console view to open
	Roles   []string `json:"roles,omitempty"`  // who usually does this
	Asks    []string `json:"asks,omitempty"`   // ways people ask for it
	Check   string   `json:"check,omitempty"`  // live check Ledgy can run
	Related []string `json:"related,omitempty"`
	Body    string   `json:"body"`
}

// Permission says which roles may do something (Admin may do everything).
type Permission struct {
	What  string   `json:"what"`
	Roles []string `json:"roles"`
}

// KB is the loaded knowledge base with its search index.
type KB struct {
	Articles []Article
	Perms    []Permission
	byID     map[string]int
	docs     []doc
	df       map[string]int
	avgLen   float64
}

type doc struct {
	terms map[string]float64 // term -> weighted frequency
	n     float64
	asks  [][]string
	title []string
}

// Load reads every *.md file in dir, the permissions table and the
// reconciliation playbooks.
func Load(fsys fs.FS, dir string) (*KB, error) {
	k := &KB{byID: map[string]int{}}
	ents, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, err
	}
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		b, err := fs.ReadFile(fsys, path.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		if e.Name() == "permissions.md" {
			if k.Perms, err = parsePerms(string(b)); err != nil {
				return nil, err
			}
			continue
		}
		as, err := parse(string(b), e.Name())
		if err != nil {
			return nil, err
		}
		for _, a := range as {
			if err := k.add(a); err != nil {
				return nil, err
			}
		}
	}
	for _, a := range playbookArticles() {
		if err := k.add(a); err != nil {
			return nil, err
		}
	}
	k.index()
	return k, nil
}

func (k *KB) add(a Article) error {
	if _, dup := k.byID[a.ID]; dup {
		return fmt.Errorf("help: article %q defined twice", a.ID)
	}
	if a.Title == "" || strings.TrimSpace(a.Body) == "" {
		return fmt.Errorf("help: article %q needs a title and a body", a.ID)
	}
	k.byID[a.ID] = len(k.Articles)
	k.Articles = append(k.Articles, a)
	return nil
}

// Get returns an article by ID.
func (k *KB) Get(id string) (Article, bool) {
	i, ok := k.byID[id]
	if !ok {
		return Article{}, false
	}
	return k.Articles[i], true
}

var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,60}$`)

func parse(src, file string) ([]Article, error) {
	var out []Article
	var cur *Article
	area := ""
	inHead := false
	var body strings.Builder
	flush := func() {
		if cur != nil {
			cur.Body = strings.TrimSpace(body.String())
			out = append(out, *cur)
		}
		cur, inHead = nil, false
		body.Reset()
	}
	sc := bufio.NewScanner(strings.NewReader(src))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	line := 0
	for sc.Scan() {
		line++
		l := sc.Text()
		switch {
		case strings.HasPrefix(l, "# "):
			flush()
			area = strings.TrimSpace(l[2:])
		case strings.HasPrefix(l, "## "):
			flush()
			id := strings.TrimSpace(l[3:])
			if !idRe.MatchString(id) {
				return nil, fmt.Errorf("help: %s:%d: bad article id %q", file, line, id)
			}
			cur, inHead = &Article{ID: id, Area: area}, true
		case cur == nil:
			// text before the first article is a comment
		case inHead && strings.TrimSpace(l) == "":
			inHead = false
		case inHead:
			key, val, ok := strings.Cut(l, ":")
			if !ok {
				return nil, fmt.Errorf("help: %s:%d: expected key: value in the header of %s", file, line, cur.ID)
			}
			val = strings.TrimSpace(val)
			switch strings.TrimSpace(key) {
			case "title":
				cur.Title = val
			case "screen":
				cur.Screen = val
			case "roles":
				cur.Roles = splitList(val, ",")
			case "ask":
				cur.Asks = append(cur.Asks, splitList(val, ";")...)
			case "check":
				cur.Check = val
			case "related":
				cur.Related = splitList(val, ",")
			default:
				return nil, fmt.Errorf("help: %s:%d: unknown key %q in %s", file, line, key, cur.ID)
			}
		default:
			body.WriteString(l)
			body.WriteByte('\n')
		}
	}
	flush()
	return out, sc.Err()
}

func splitList(s, sep string) []string {
	var out []string
	for _, x := range strings.Split(s, sep) {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

// parsePerms reads lines "What | Role, Role".
func parsePerms(src string) ([]Permission, error) {
	var out []Permission
	for i, l := range strings.Split(src, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		what, roles, ok := strings.Cut(l, "|")
		if !ok {
			return nil, fmt.Errorf("help: permissions.md:%d: expected 'What | Roles'", i+1)
		}
		out = append(out, Permission{What: strings.TrimSpace(what), Roles: splitList(roles, ",")})
	}
	return out, nil
}

// ---- break playbooks as articles ----

var breakNames = map[recon.BreakType][2]string{
	recon.MissingInOMS:       {"Trade on the exchange but not in the OMS", "missing in oms; exchange trade not in oms; trade not in our system; extra exchange trade"},
	recon.MissingInExchange:  {"OMS fill not on the exchange", "missing in exchange; oms trade not in exchange file; unconfirmed fill; trade not confirmed by exchange"},
	recon.TradeNoMismatch:    {"Trade numbers differ", "trade number mismatch; trade no mismatch; different trade number"},
	recon.UCCMismatch:        {"UCC mismatch (client code differs)", "ucc mismatch; client code mismatch; wrong client code; trade booked to wrong client; client code modification; punching error"},
	recon.SideMismatch:       {"Buy/sell side differs", "side mismatch; buy sell mismatch; wrong side"},
	recon.InstrumentMismatch: {"Contract or instrument differs", "instrument mismatch; wrong contract; wrong strike; wrong expiry; scrip mismatch"},
	recon.QtyMismatch:        {"Quantity differs", "quantity mismatch; qty mismatch; wrong quantity; partial fill difference"},
	recon.PriceMismatch:      {"Price differs", "price mismatch; wrong price; trade price difference"},
	recon.AlgoIDMissing:      {"Algo ID missing or wrong", "algo mismatch; algo id missing; algo id wrong; algo order tag; api order without algo id"},
}

func playbookArticles() []Article {
	var out []Article
	for _, p := range recon.Playbooks() {
		nm, ok := breakNames[p.Type]
		if !ok {
			continue
		}
		var b strings.Builder
		b.WriteString("Open the break on [[recon|Trade reconciliation]]. The break shows these same steps with the trade's details filled in.\n\n**Check first**\n")
		for i, c := range p.Check {
			fmt.Fprintf(&b, "%d. %s\n", i+1, c)
		}
		for _, o := range p.Outcomes {
			fmt.Fprintf(&b, "\n**If %s**\n", strings.ToLower(o.If[:1])+o.If[1:])
			for i, f := range o.Fix {
				fmt.Fprintf(&b, "%d. %s\n", i+1, f)
			}
			if o.CCM {
				b.WriteString("- This is a client code modification: it counts towards the day's limit and needs evidence.\n")
			}
		}
		b.WriteString("\nResolving needs a note saying what you checked; the break suggests one to fill in.")
		out = append(out, Article{
			ID:      "break-" + strings.ReplaceAll(strings.ToLower(string(p.Type)), "_", "-"),
			Title:   "Break: " + nm[0],
			Area:    "Trade reconciliation",
			Screen:  "recon",
			Roles:   []string{"Ops maker", "Ops checker"},
			Asks:    splitList(nm[1], ";"),
			Check:   "breaks",
			Related: []string{"recon-breaks", "ccm", "run-billing"},
			Body:    strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(b.String(), "{trade}", "the trade"), "{ucc}", "the client"), "{exchange}", "the exchange client"), "{oms}", "the OMS client"), "{order}", "the order"), "{terminal}", "the terminal"),
		})
	}
	return out
}

// ---- search ----

var stop = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`a an the to of in on for and or is are was be it its this that how do i we
	can what why where when which who my our me you your with from at by as if not no get got does did done has have
	there their them they so any some should would could will just please help want need make way there's it's i'm
	am about into out up than then also all one ledgy hi hello`) {
		stop[w] = true
	}
}

// synonyms maps a word to the canonical term it is indexed under.
var synonyms = map[string]string{}

func init() {
	groups := [][]string{
		{"ap", "authorised", "authorized", "subbroker", "sub-broker", "partner", "franchise", "franchisee", "remisier", "introducer"},
		{"ucc", "clientcode", "client-code"},
		{"brokerage", "commission", "slab", "brokrage", "brokerge"},
		{"payout", "withdrawal", "withdraw", "pay-out", "payouts"},
		{"receipt", "payin", "pay-in", "funds-added"},
		{"kra", "cvl", "ndml", "camskra", "kfin"},
		{"2fa", "totp", "authenticator", "two-factor", "twofactor", "mfa"},
		{"password", "pwd", "passcode"},
		{"login", "signin", "sign-in", "logon"},
		{"locked", "lockout", "blocked-login"},
		{"cn", "ecn", "contract-note", "contractnote"},
		{"mismatch", "difference", "differ", "different", "wrong", "incorrect"},
		{"break", "breaks", "exception"},
		{"recon", "reconciliation", "reconcile", "matching"},
		{"eod", "end-of-day", "dayend", "day-end", "close-day"},
		{"bod", "start-of-day", "beginning-of-day"},
		{"dp", "depository", "cdsl", "nsdl", "demat"},
		{"dis", "slip", "delivery-instruction"},
		{"mtf", "margin-trading", "mtf-funding"},
		{"pledge", "repledge", "re-pledge", "collateral"},
		{"gst", "gstr", "gstr-1", "gstr-3b", "gstin"},
		{"whatsapp", "wa", "whats"},
		{"sms", "dlt", "text-message"},
		{"inactive", "dormant", "inoperative"},
		{"reactivate", "reactivation", "re-activate", "revive"},
		{"nri", "pis", "nre", "nro"},
		{"ipo", "asba", "allotment"},
		{"mf", "mutual-fund", "sip"},
		{"str", "suspicious"},
		{"complaint", "grievance", "scores", "escalation"},
		{"voucher", "journal-voucher", "jv"},
		{"user", "staff", "employee", "login-id"},
		{"client", "customer", "investor", "account-holder"},
		{"add", "create", "new", "register", "onboard", "setup", "set-up", "open"},
		{"change", "modify", "update", "edit", "amend", "correct", "fix", "alter"},
		{"remove", "delete", "deactivate", "unassign"},
		{"reduce", "lower", "decrease", "cheaper", "less", "discount", "concession"},
		{"approve", "authorise", "authorize", "checker", "sanction"},
		{"margin", "shortfall", "peak", "span", "exposure"},
		{"squareoff", "square-off", "liquidate", "liquidation", "cut-position"},
		{"limit", "exposure-limit", "trading-limit"},
		{"bank", "ifsc", "account-number"},
		{"cheque", "check", "chq", "bounce", "bounced", "dishonour"},
		{"interest", "dpc", "delayed-payment"},
		{"debit", "debtor", "debit-balance", "dues", "outstanding"},
		{"kyc", "ckyc", "rekyc", "re-kyc"},
		{"pan", "pan-card"},
		{"aadhaar", "aadhar", "digilocker", "ekyc", "e-kyc", "esign", "e-sign"},
		{"report", "reports", "return", "filing", "upload-report"},
		{"statement", "ledger-statement", "account-statement"},
		{"bill", "billing", "invoice", "charges"},
		{"settlement", "obligation", "payin-payout"},
		{"auction", "short-delivery", "shortage", "close-out"},
		{"dividend", "bonus", "split", "corporate-action", "rights"},
		{"buyback", "buy-back", "tender", "open-offer"},
		{"file", "files", "upload", "import", "load"},
		{"rejected", "reject", "failed", "fail", "error", "invalid", "refused"},
		{"autopilot", "automation", "automatic", "auto"},
		{"job", "schedule", "scheduled", "cron"},
		{"group", "tag", "tags", "category"},
		{"branch", "office", "zone", "region"},
		{"rm", "relationship-manager", "dealer"},
		{"oms", "front-office", "odin", "omnesys", "nest", "trading-terminal"},
		{"surveillance", "aml", "alert", "alerts", "pmla"},
		{"permission", "permitted", "allowed", "role", "rights", "access", "forbidden", "authorised-to"},
		{"seal", "tamper", "tampered", "audit-trail"},
	}
	for _, g := range groups {
		for _, w := range g {
			synonyms[w] = g[0]
			synonyms[stem(w)] = g[0]
		}
	}
}

var wordRe = regexp.MustCompile(`[a-z0-9]+(?:-[a-z0-9]+)*`)

// terms turns text into index terms: lower-case words, stop words dropped,
// light stemming, synonyms folded together. Hyphenated words also index
// their parts.
func terms(s string) []string {
	var out []string
	for _, w := range wordRe.FindAllString(strings.ToLower(s), -1) {
		cands := []string{w}
		if strings.Contains(w, "-") {
			cands = append(cands, strings.Split(w, "-")...)
			cands = append(cands, strings.ReplaceAll(w, "-", ""))
		}
		for _, c := range cands {
			if stop[c] || len(c) < 2 && c != "x" {
				continue
			}
			if t, ok := synonyms[c]; ok {
				out = append(out, t)
				continue
			}
			s := stem(c)
			if t, ok := synonyms[s]; ok {
				s = t
			}
			out = append(out, s)
		}
	}
	return out
}

func stem(w string) string {
	if len(w) <= 4 || strings.ContainsAny(w, "0123456789") {
		return w
	}
	for _, suf := range []string{"ations", "ation", "ings", "ing", "ies", "ed", "es", "s"} {
		if strings.HasSuffix(w, suf) && len(w)-len(suf) >= 3 {
			r := w[:len(w)-len(suf)]
			if suf == "ies" {
				r += "y"
			}
			return r
		}
	}
	// "activate" and "activating" meet at "activat".
	if strings.HasSuffix(w, "e") {
		return w[:len(w)-1]
	}
	return w
}

func (k *KB) index() {
	k.df = map[string]int{}
	total := 0.0
	k.docs = make([]doc, len(k.Articles))
	for i, a := range k.Articles {
		d := doc{terms: map[string]float64{}}
		addW := func(text string, w float64) {
			for _, t := range terms(text) {
				d.terms[t] += w
				d.n += w
			}
		}
		addW(a.Title, 4)
		for _, q := range a.Asks {
			addW(q, 3)
			d.asks = append(d.asks, terms(q))
		}
		addW(a.Area, 1)
		addW(a.Body, 1)
		d.title = terms(a.Title)
		for t := range d.terms {
			k.df[t]++
		}
		total += d.n
		k.docs[i] = d
	}
	if len(k.docs) > 0 {
		k.avgLen = total / float64(len(k.docs))
	}
}

// Hit is one search result.
type Hit struct {
	Article Article `json:"article"`
	Score   float64 `json:"score"`
}

// Search returns the best articles for q, highest first. view is the
// screen the user is on; articles about it get a small lift.
func (k *KB) Search(q, view string, n int) []Hit {
	qt := terms(q)
	if len(qt) == 0 {
		return nil
	}
	seen := map[string]bool{}
	uniq := qt[:0:0]
	for _, t := range qt {
		if !seen[t] {
			seen[t] = true
			uniq = append(uniq, t)
		}
	}
	N := float64(len(k.docs))
	const k1, b = 1.2, 0.5
	var hits []Hit
	for i, d := range k.docs {
		s := 0.0
		matched := 0
		for _, t := range uniq {
			f := d.terms[t]
			if f == 0 {
				continue
			}
			matched++
			idf := math.Log(1 + (N-float64(k.df[t])+0.5)/(float64(k.df[t])+0.5))
			s += idf * f * (k1 + 1) / (f + k1*(1-b+b*d.n/k.avgLen))
		}
		if s == 0 {
			continue
		}
		// Reward covering more of the question, and a question that
		// matches one of the ways people ask for this article.
		s *= 0.5 + 0.5*float64(matched)/float64(len(uniq))
		best := 0.0
		for _, a := range d.asks {
			if c := cover(a, seen); c > best {
				best = c
			}
		}
		if c := cover(d.title, seen); c > best {
			best = c
		}
		s *= 1 + best
		if view != "" && k.Articles[i].Screen == view {
			s *= 1.15
		}
		hits = append(hits, Hit{Article: k.Articles[i], Score: s})
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if n > 0 && len(hits) > n {
		hits = hits[:n]
	}
	return hits
}

// cover is the share of phrase terms present in the question.
func cover(phrase []string, q map[string]bool) float64 {
	if len(phrase) == 0 {
		return 0
	}
	n := 0
	for _, t := range phrase {
		if q[t] {
			n++
		}
	}
	return float64(n) / float64(len(phrase))
}

// PermHit is a permission matching a question.
type PermHit struct {
	Permission
	Score float64 `json:"score"`
}

// SearchPerms finds who may do what the question mentions, for questions
// like "who can approve payouts" or a pasted "needs Ops checker" error.
func (k *KB) SearchPerms(q string, n int) []PermHit {
	qs := map[string]bool{}
	for _, t := range terms(q) {
		qs[t] = true
	}
	var out []PermHit
	for _, p := range k.Perms {
		pt := terms(p.What)
		c := cover(pt, qs)
		if c >= 0.6 {
			out = append(out, PermHit{p, c + 0.01*float64(len(pt))})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// Suggestions are starter questions for a screen.
func (k *KB) Suggestions(view string, n int) []string {
	var out []string
	for _, a := range k.Articles {
		if a.Screen == view && len(a.Asks) > 0 {
			out = append(out, Question(a))
		}
		if len(out) >= n {
			return out
		}
	}
	for _, id := range []string{"daily-routine", "eod-close", "change-brokerage", "add-ap", "break-ucc-mismatch", "permission-denied"} {
		if len(out) >= n {
			break
		}
		if a, ok := k.Get(id); ok && !contains(out, Question(a)) {
			out = append(out, Question(a))
		}
	}
	return out
}

// Question phrases an article as something a user would ask.
func Question(a Article) string {
	t := a.Title
	if strings.HasPrefix(t, "Break: ") {
		return "How do I fix a " + strings.ToLower(strings.TrimPrefix(t, "Break: ")) + " break?"
	}
	if strings.HasSuffix(t, "?") {
		return t
	}
	return "How do I " + strings.ToLower(t[:1]) + t[1:] + "?"
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
