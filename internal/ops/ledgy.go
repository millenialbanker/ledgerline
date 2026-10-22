package ops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"ledgerline"
	"ledgerline/internal/docs"
	"ledgerline/internal/help"
)

// ---- Ledgy: the in-app helper ----
//
// Ledgy answers "how do I…" and "why can't I…" questions from the help
// articles (config/help), adds live facts from the firm's own data where
// they settle the question (what still blocks the day close, why a client
// can't trade, which breaks are open, what is in your queue), and keeps
// the questions it could not answer so the articles can be improved.

const cLedgyQ = "ledgyq"

var (
	helpOnce sync.Once
	helpKB   *help.KB
	helpErr  error
)

// HelpKB returns the loaded knowledge base.
func HelpKB() (*help.KB, error) {
	helpOnce.Do(func() { helpKB, helpErr = help.Load(ledgerline.FS, "config/help") })
	return helpKB, helpErr
}

// LedgyWriter phrases an answer from the articles (an AI model). Optional.
type LedgyWriter = help.Writer

// LedgyAnswer is Ledgy's reply.
type LedgyAnswer struct {
	Question  string         `json:"question"`
	Confident bool           `json:"confident"`
	Article   *help.Article  `json:"article,omitempty"`
	Others    []LedgyLink    `json:"others"`
	Perms     []help.PermHit `json:"perms"`
	Facts     []LedgyFacts   `json:"facts"`
	AI        string         `json:"ai,omitempty"`
	AIBy      string         `json:"ai_by,omitempty"`
	AINote    string         `json:"ai_note,omitempty"`
	Suggest   []string       `json:"suggest"`
	Related   []LedgyLink    `json:"related"`
	Role      string         `json:"role"`
	Gap       string         `json:"gap_id,omitempty"`
}

// LedgyLink points at another article.
type LedgyLink struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// LedgyFacts is the result of one live check.
type LedgyFacts struct {
	Title string      `json:"title"`
	Lines []LedgyLine `json:"lines"`
}

// LedgyLine is one fact: ok, bad or info, with the screen to fix it.
type LedgyLine struct {
	Text   string `json:"text"`
	State  string `json:"state"`
	Screen string `json:"screen,omitempty"`
	UCC    string `json:"ucc,omitempty"`
}

// LedgyGap is a question Ledgy could not answer well, or an answer marked
// unhelpful. Questions are stored with PAN, numbers and emails masked.
type LedgyGap struct {
	ID        string `json:"id"`
	Question  string `json:"question"`
	Best      string `json:"best,omitempty"`
	Asked     int    `json:"asked"`
	Unhelpful int    `json:"unhelpful"`
	Helpful   int    `json:"helpful"`
	First     string `json:"first"`
	Last      string `json:"last"`
	View      string `json:"view,omitempty"`
}

// confidentScore is the search score above which the top article is taken
// as the answer; below it Ledgy says it is not sure and offers choices.
const confidentScore = 7.0

var (
	reUCCWord = regexp.MustCompile(`\b[A-Za-z]{2,4}[0-9]{3,6}\b`)
	reWhoCan  = regexp.MustCompile(`(?i)\b(who (can|may|is allowed)|which role|what role|permission|not permitted|forbidden|needs (ops|risk|compliance|admin)|you are (ops|risk|compliance|branch|authorised))`)
	reEOD     = regexp.MustCompile(`(?i)(close|closing) (the )?day|\beod\b|end of (the )?day|day close`)
	reQueue   = regexp.MustCompile(`(?i)\b(my (queue|tasks|work|items)|work queue|pending items|to ?do|what do i (need|have) to do)`)
	reBreaks  = regexp.MustCompile(`(?i)\bbreaks?\b|reconcil|\brecon\b|mismatch`)
)

// Ledgy answers a question asked on a screen, for a business date.
func (o *Ops) Ledgy(ctx context.Context, tenant, actor, q, view, date string) (LedgyAnswer, error) {
	q = strings.TrimSpace(q)
	if len(q) > 500 {
		q = q[:500]
	}
	if q == "" {
		return LedgyAnswer{}, invalid("ask a question")
	}
	k, err := HelpKB()
	if err != nil {
		return LedgyAnswer{}, err
	}
	role, err := o.RoleOf(ctx, tenant, actor)
	if err != nil {
		return LedgyAnswer{}, err
	}
	ans := LedgyAnswer{Question: q, Role: role, Others: []LedgyLink{}, Perms: []help.PermHit{}, Facts: []LedgyFacts{}, Suggest: []string{}, Related: []LedgyLink{}}
	// A client code in the question says which client, not what the
	// question is about: search with it replaced by "client".
	ucc := o.ledgyClient(ctx, tenant, q)
	sq := q
	if ucc != "" {
		sq = reUCCWord.ReplaceAllStringFunc(q, func(w string) string {
			if strings.EqualFold(w, ucc) {
				return "client"
			}
			return w
		})
	}
	hits := k.Search(sq, view, 5)
	if reWhoCan.MatchString(q) {
		ans.Perms = k.SearchPerms(sq, 3)
	}
	if len(hits) > 0 && hits[0].Score >= confidentScore {
		ans.Confident = true
		a := hits[0].Article
		ans.Article = &a
		for _, h := range hits[1:] {
			if h.Score >= hits[0].Score*0.6 && len(ans.Others) < 3 {
				ans.Others = append(ans.Others, LedgyLink{h.Article.ID, h.Article.Title})
			}
		}
		for _, id := range a.Related {
			if r, ok := k.Get(id); ok {
				ans.Related = append(ans.Related, LedgyLink{r.ID, r.Title})
			}
		}
	} else {
		for _, h := range hits {
			if len(ans.Others) < 4 {
				ans.Others = append(ans.Others, LedgyLink{h.Article.ID, h.Article.Title})
			}
		}
	}
	if len(ans.Perms) > 0 && !ans.Confident {
		ans.Confident = true // "who can …" answered from the permission table
	}

	// Live checks: those the article asks for, and those the words ask for.
	want := map[string]bool{}
	if ans.Article != nil && ans.Article.Check != "" {
		want[ans.Article.Check] = true
	}
	if reEOD.MatchString(q) {
		want["eod"] = true
	}
	if reQueue.MatchString(q) {
		want["work"] = true
	}
	if ucc != "" {
		want["client"] = true
	}
	if want["breaks"] && !reBreaks.MatchString(q) && ans.Article != nil && !strings.HasPrefix(ans.Article.ID, "break-") && ans.Article.ID != "recon-breaks" && ans.Article.ID != "run-recon" {
		delete(want, "breaks")
	}
	firmWide := role != RoleBranch && role != RoleAP
	if want["eod"] && firmWide {
		if f, ok := o.ledgyEOD(ctx, tenant, date); ok {
			ans.Facts = append(ans.Facts, f)
		}
	}
	if want["breaks"] && firmWide {
		if f, ok := o.ledgyBreaks(ctx, tenant, date); ok {
			ans.Facts = append(ans.Facts, f)
		}
	}
	if want["client"] {
		if ucc == "" {
			ans.Facts = append(ans.Facts, LedgyFacts{Title: "Which client?", Lines: []LedgyLine{{Text: "Tell me the client code (for example \"why is BH1003 blocked?\") and I'll check the account.", State: "info"}}})
		} else if scope := o.ClientScope(ctx, tenant, actor); scope == nil || scope(ucc) {
			if f, ok := o.ledgyClientFacts(ctx, tenant, ucc, date); ok {
				ans.Facts = append(ans.Facts, f)
			}
		}
	}
	if want["work"] {
		ans.Facts = append(ans.Facts, o.ledgyWork(ctx, tenant, role))
	}

	// Manual edition: Autopilot and scheduled runs are not included.
	if ans.Article != nil && (ans.Article.Screen == "autopilot" || ans.Article.Screen == "jobs") && o.manual(ctx, tenant) {
		ans.Facts = append([]LedgyFacts{{Title: "Your edition: Manual", Lines: []LedgyLine{{Text: "Your firm's edition has no Autopilot, work queue or scheduled runs: do each step by hand from its own screen. Jobs can still be run by hand. Ask your provider about the Autopilot editions.", State: "info"}}}}, ans.Facts...)
	}

	// A live check that answers the question is an answer, with the
	// article that goes with it.
	if !ans.Confident && len(ans.Facts) > 0 {
		for _, c := range []struct{ check, article string }{{"eod", "eod-close"}, {"work", "work-queue"}, {"client", "client-cant-trade"}, {"breaks", "recon-breaks"}} {
			if want[c.check] {
				if a, ok := k.Get(c.article); ok {
					ans.Article, ans.Confident = &a, true
					ans.Others = []LedgyLink{}
				}
				break
			}
		}
	}

	// Phrasing by an AI model, when the firm switched it on. Only the
	// question (masked) and the articles are sent, never client data.
	// Every call is metered and paid from the firm's prepaid AI credit.
	if o.LedgyAI != nil && o.Meter != nil && ans.Confident {
		if set, _ := o.Settings(ctx, tenant); set.LedgyAI {
			var arts []help.Article
			if ans.Article != nil {
				arts = append(arts, *ans.Article)
			}
			for _, h := range hits {
				if len(arts) < 3 && h.Score >= confidentScore*0.6 && (ans.Article == nil || h.Article.ID != ans.Article.ID) {
					arts = append(arts, h.Article)
				}
			}
			mq := MaskQuestion(q)
			reserve, err := o.aiReserve(o.LedgyAI.Provider(), o.LedgyAI.Model(), help.EstimateInputTokens(mq, arts, ans.Perms), o.LedgyAI.MaxOutput())
			switch {
			case err != nil:
				ans.AINote = "AI answers are unavailable: " + err.Error()
			case o.aiBalance(ctx, tenant) < reserve:
				ans.AINote = "Your firm's AI credit has run out, so this is Ledgy's standard answer. Ask your Ledgerline provider to add credit."
			default:
				cctx, cancel := context.WithTimeout(ctx, 25*time.Second)
				r, err := o.LedgyAI.Write(cctx, mq, arts, ans.Perms)
				cancel()
				if err == nil && strings.TrimSpace(r.Text) != "" {
					if _, merr := o.aiMeter(ctx, tenant, actor, r); merr == nil {
						ans.AI, ans.AIBy = strings.TrimSpace(r.Text), aiName(r)
					} else {
						ans.AINote = "AI answer withheld: it could not be metered (" + merr.Error() + ")."
					}
				}
			}
		}
	}

	if !ans.Confident {
		ans.Suggest = k.Suggestions(view, 4)
		best := ""
		if len(hits) > 0 {
			best = hits[0].Article.ID
		}
		ans.Gap = o.ledgyGap(ctx, tenant, q, view, best, 1, 0, 0)
	}
	return ans, nil
}

// LedgySuggestions are starter questions for a screen.
func (o *Ops) LedgySuggestions(view string) []string {
	k, err := HelpKB()
	if err != nil {
		return []string{}
	}
	return k.Suggestions(view, 5)
}

// LedgyArticle returns one article.
func (o *Ops) LedgyArticle(id string) (help.Article, error) {
	k, err := HelpKB()
	if err != nil {
		return help.Article{}, err
	}
	a, ok := k.Get(id)
	if !ok {
		return help.Article{}, fmt.Errorf("%w: article %s", ErrNotFound, id)
	}
	return a, nil
}

// LedgyFeedback records whether an answer helped. Unhelpful answers join
// the list of questions to improve.
func (o *Ops) LedgyFeedback(ctx context.Context, tenant, actor, q, view, article string, helpful bool) error {
	if strings.TrimSpace(q) == "" {
		return invalid("question missing")
	}
	h, u := 0, 1
	if helpful {
		h, u = 1, 0
	}
	o.ledgyGap(ctx, tenant, q, view, article, 0, h, u)
	o.log(ctx, tenant, actor, "ledgy.feedback", article, map[string]any{"helpful": helpful})
	return nil
}

// LedgyGaps lists questions Ledgy could not answer or answered unhelpfully,
// most asked first. Admins and compliance use it to improve the articles.
func (o *Ops) LedgyGaps(ctx context.Context, tenant, actor string) ([]LedgyGap, error) {
	if err := o.require(ctx, tenant, actor, "Reviewing Ledgy's unanswered questions", RoleCompliance); err != nil {
		return nil, err
	}
	gs, err := docs.List[LedgyGap](ctx, o.Docs, tenant, cLedgyQ, "")
	if err != nil {
		return nil, err
	}
	out := gs[:0]
	for _, g := range gs {
		if g.Asked > 0 || g.Unhelpful > 0 {
			out = append(out, g)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].Asked+2*out[i].Unhelpful, out[j].Asked+2*out[j].Unhelpful
		if a != b {
			return a > b
		}
		return out[i].Last > out[j].Last
	})
	if out == nil {
		out = []LedgyGap{}
	}
	return out, nil
}

func (o *Ops) ledgyGap(ctx context.Context, tenant, q, view, best string, asked, helpful, unhelpful int) string {
	mq := MaskQuestion(strings.ToLower(strings.Join(strings.Fields(q), " ")))
	sum := sha256.Sum256([]byte(mq))
	id := "Q-" + hex.EncodeToString(sum[:6])
	now := o.now().Format(time.RFC3339)
	_, err := docs.Update(ctx, o.Docs, tenant, cLedgyQ, id, SystemActor, func(g *LedgyGap) error {
		g.Asked += asked
		g.Helpful += helpful
		g.Unhelpful += unhelpful
		g.Last = now
		if best != "" {
			g.Best = best
		}
		return nil
	})
	if err != nil {
		_ = docs.Upsert(ctx, o.Docs, tenant, cLedgyQ, id, LedgyGap{ID: id, Question: mq, Best: best, Asked: asked, Helpful: helpful, Unhelpful: unhelpful, First: now, Last: now, View: view}, SystemActor)
	}
	return id
}

var (
	reMaskPAN   = regexp.MustCompile(`(?i)\b[A-Z]{5}[0-9]{4}[A-Z]\b`)
	reMaskEmail = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
	reLong      = regexp.MustCompile(`\b[0-9]{8,}\b`)
)

// MaskQuestion hides PANs, emails and long numbers (accounts, mobiles)
// before a question is stored or sent anywhere.
func MaskQuestion(q string) string {
	q = reMaskPAN.ReplaceAllString(q, "[PAN]")
	q = reMaskEmail.ReplaceAllString(q, "[email]")
	return reLong.ReplaceAllString(q, "[number]")
}

// ledgyClient finds a client code mentioned in the question.
func (o *Ops) ledgyClient(ctx context.Context, tenant, q string) string {
	for _, w := range reUCCWord.FindAllString(q, 4) {
		if c, err := o.Client(ctx, tenant, strings.ToUpper(w)); err == nil {
			return c.UCC
		}
	}
	return ""
}

func (o *Ops) ledgyEOD(ctx context.Context, tenant, date string) (LedgyFacts, bool) {
	st, err := o.EOD(ctx, tenant, date)
	if err != nil {
		return LedgyFacts{}, false
	}
	f := LedgyFacts{Title: "Day close for " + date}
	if st.Closed {
		f.Lines = append(f.Lines, LedgyLine{Text: "This day is already closed" + strings.TrimSpace(" by "+st.ClosedBy) + ".", State: "ok"})
		return f, true
	}
	bad := 0
	for _, c := range st.Checks {
		s := "ok"
		if !c.OK {
			s, bad = "bad", bad+1
		}
		f.Lines = append(f.Lines, LedgyLine{Text: c.Name + ": " + c.Detail, State: s, Screen: c.View})
	}
	if bad == 0 {
		f.Lines = append(f.Lines, LedgyLine{Text: "Everything passes. An Ops checker can click Run EOD close on the Dashboard.", State: "info", Screen: "dashboard"})
	} else {
		f.Lines = append(f.Lines, LedgyLine{Text: fmt.Sprintf("%d check(s) still open. Fix them, or close with a note explaining why.", bad), State: "info", Screen: "dashboard"})
	}
	return f, true
}

func (o *Ops) ledgyBreaks(ctx context.Context, tenant, date string) (LedgyFacts, bool) {
	open, err := o.App.Breaks.List(ctx, tenant, date, "OPEN")
	if err != nil {
		return LedgyFacts{}, false
	}
	f := LedgyFacts{Title: "Open breaks on " + date}
	if len(open) == 0 {
		all, _ := o.App.Breaks.List(ctx, tenant, date, "")
		if len(all) == 0 {
			f.Lines = append(f.Lines, LedgyLine{Text: "No breaks recorded for this date: reconciliation may not have run yet. Load the files and click Run reconciliation.", State: "info", Screen: "recon"})
		} else {
			f.Lines = append(f.Lines, LedgyLine{Text: fmt.Sprintf("All %d breaks resolved.", len(all)), State: "ok", Screen: "recon"})
		}
		return f, true
	}
	by := map[string]int{}
	for _, b := range open {
		by[string(b.Type)]++
	}
	var types []string
	for t := range by {
		types = append(types, t)
	}
	sort.Strings(types)
	for _, t := range types {
		f.Lines = append(f.Lines, LedgyLine{Text: fmt.Sprintf("%d × %s", by[t], strings.ReplaceAll(strings.ToLower(t), "_", " ")), State: "bad", Screen: "recon"})
	}
	return f, true
}

func (o *Ops) ledgyClientFacts(ctx context.Context, tenant, ucc, date string) (LedgyFacts, bool) {
	c, err := o.Client(ctx, tenant, ucc)
	if err != nil {
		return LedgyFacts{}, false
	}
	f := LedgyFacts{Title: c.UCC + " · " + c.Name}
	add := func(text, state, screen string) {
		f.Lines = append(f.Lines, LedgyLine{Text: text, State: state, Screen: screen, UCC: c.UCC})
	}
	if c.Status != "Active" {
		add("Not active yet: at "+c.Status+". Advance it on Clients & onboarding; an Ops checker activates.", "bad", "clients")
	} else {
		add("Active.", "ok", "clients")
	}
	if c.Blocked {
		why := c.BlockedReason
		if why == "" {
			why = "no reason recorded"
		}
		add("Blocked: "+why+".", "bad", "clients")
	}
	if c.Inactive {
		add("Flagged inactive since "+c.InactiveSince+". Needs reactivation (IPV and a validated KRA status).", "bad", "inactive")
	}
	switch c.KRAStatus {
	case "", "Validated", "Registered":
		if c.KRAStatus != "" {
			add("KRA status "+c.KRAStatus+".", "ok", "kyc")
		}
	default:
		add("KRA status "+c.KRAStatus+strings.TrimSpace(" "+c.KRARemark)+".", "bad", "kyc")
	}
	if rv, err := o.Risk(ctx, tenant, date); err == nil {
		for _, r := range rv.Rows {
			if r.UCC != c.UCC {
				continue
			}
			st := "ok"
			if r.Status == "Breach" || r.Status == "Debit" {
				st = "bad"
			}
			add(fmt.Sprintf("RMS %s: ledger ₹%s, free ₹%s, margin required ₹%s.", r.Status, formatINR(r.Funds), formatINR(r.Free), formatINR(r.Required)), st, "rms")
		}
	}
	return f, true
}

func (o *Ops) ledgyWork(ctx context.Context, tenant, role string) LedgyFacts {
	f := LedgyFacts{Title: "Your work queue (" + role + ")"}
	items, err := o.WorkItems(ctx, tenant, "Open")
	if err != nil {
		return f
	}
	var mine []WorkItem
	for _, w := range items {
		if role == RoleAdmin || w.Role == role {
			mine = append(mine, w)
		}
	}
	sort.SliceStable(mine, func(i, j int) bool { return mine[i].Due < mine[j].Due })
	if len(mine) == 0 {
		f.Lines = append(f.Lines, LedgyLine{Text: "Nothing waiting for your role.", State: "ok", Screen: "autopilot"})
		return f
	}
	for i, w := range mine {
		if i == 5 {
			f.Lines = append(f.Lines, LedgyLine{Text: fmt.Sprintf("…and %d more.", len(mine)-5), State: "info", Screen: "autopilot"})
			break
		}
		due := ""
		if w.Due != "" {
			due = " (due " + w.Due + ")"
		}
		f.Lines = append(f.Lines, LedgyLine{Text: w.Title + due, State: "bad", Screen: w.View, UCC: w.UCC})
	}
	return f
}

func aiName(r help.Reply) string {
	n := map[string]string{"anthropic": "Claude", "openai": "OpenAI", "gemini": "Gemini"}[r.Provider]
	if n == "" {
		n = r.Provider
	}
	return n + " (" + r.Model + ")"
}
