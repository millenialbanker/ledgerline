package ops

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"ledgerline/internal/docs"
)

// ---- the Circulars board: circulars become cards ----
//
// A Ledgerline board (boardsync.go) whose cards are the obligations of the
// circulars that apply, or may apply, to the firm: one card for each
// action (rule-based, added by the compliance officer, or an accepted AI
// proposal) and for each AI proposal waiting to be accepted. Its lists:
//
//	To review              proposals, and the actions of a circular not yet reviewed
//	Accepted / to do       accepted, not started
//	In progress            the owner is working on it
//	Waiting for evidence   done, the evidence is being gathered
//	Done                   closed with its evidence
//	Not applicable         dropped, rejected, or the circular does not apply
//
// Each card is made with the circular's number, date and issuer, what to
// do, by when, the owner, the evidence needed and the Ledgerline area it
// may change in its description, a "Steps" checklist and an "Evidence"
// checklist; the card window links to the circular in Ledgerline. A card
// whose obligation touches a Ledgerline setting or rule carries the label
// "Ledgerline may need a change" (the board's "Ledgerline updates"
// filter), so the vendor or an Admin sees what the software needs.
//
// Both ways: moving a card does the circular's own action as the person
// (accepting needs the compliance officer; moving on and finishing, the
// owner role or the compliance officer), and a change made on Calendar &
// circulars moves the card. Moving a card to Done closes its action with
// the card's evidence: every item of its Evidence checklist ticked and at
// least one file or link attached, unless the action needs no evidence;
// otherwise the move is refused with the reason. Nothing goes back to To
// review, and nothing is marked not applicable without a reason: those
// are done, with the reason, on Calendar & circulars.
//
// The compliance officer gets a notice in the bell once a day when new
// circulars apply ("3 new circulars apply to you, 2 obligations need
// review"); it carries counts only.

const (
	cCircState     = "circstate" // the daily notice and AI reading, by tenant: "main"
	circNoticeAt   = "09:30"     // IST: the daily notice goes after this
	circAIPerTick  = 2           // new circulars read by AI each minute, at most
	circAINewFor   = 72 * time.Hour
	circCheckEvery = 30 * time.Minute
)

// Lists of the Circulars board: the status key and the name.
var circLists = []bsList{
	{"review", "To review", "purple"}, {"todo", "Accepted / to do", ""}, {"doing", "In progress", "blue"},
	{"evidence", "Waiting for evidence", "yellow"}, {"done", "Done", "green"}, {"na", "Not applicable", ""},
}

var circListName = map[string]string{"review": "To review", "todo": "Accepted", "doing": "In progress", "evidence": "Waiting for evidence", "done": "Done", "na": "Not applicable"}

var (
	bsCircUpdate    = bsLabel{"circ:update", "Ledgerline may need a change", "red"}
	bsCircUnsure    = bsLabel{"circ:unsure", "Applies? Not sure", "yellow"}
	bsCircAI        = bsLabel{"circ:ai", "From AI reading", "purple"}
	bsCircHigh      = bsLabel{"circ:high", "High priority", "orange"}
	circIssuerColor = map[string]string{"SEBI": "purple", "NSE": "blue", "BSE": "sky", "MCX": "orange", "NCDEX": "lime", "MSE": "pink",
		"NCL": "black", "ICCL": "black", "MCXCCL": "black", "NCCL": "black", "MCCIL": "black", "CDSL": "green", "NSDL": "yellow"}
)

func circIssuerLabel(source string) bsLabel {
	_, code := circIssuer(source)
	code = firstOf(code, "Other")
	return bsLabel{"issuer:" + code, code, firstOf(circIssuerColor[code], "black")}
}

func init() {
	bsSources = append(bsSources, &bsSource{ID: "circulars", Name: "Circulars", Background: "lime", Screen: "Calendar & circulars", Kinds: []string{"circular"},
		About:  "What new SEBI, exchange, clearing and depository circulars require of the firm: one card for each obligation of a circular that applies, with its owner, due date, steps and the evidence to keep. Move a card to work it; it is done when its evidence is attached.",
		Lists:  circLists,
		Closed: []string{"done", "na"},
		Labels: append(append([]bsLabel{}, bsRoleLabels...), bsCircUpdate, bsCircUnsure, bsCircAI, bsCircHigh),
		since:  func(env *bsEnv) time.Time { return env.now.Add(-bsKeep) },
		list:   bsCirculars,
		move:   circBoardMove,
	})
	bsLinkKinds["circular"] = circLinkRecord
	boardTickHooks = append(boardTickHooks, func(ctx context.Context, o *Ops, tenant string, now time.Time) { o.circTick(ctx, tenant, now) })
}

// circOb is an action or a proposal, as a card shows it.
type circOb struct {
	ID, Title, Role, Due, DueRule, Needs, Area, Source, Accepted string
	NoEvidence                                                   bool
	Steps                                                        []string
	Proposed                                                     bool
}

func circObOfTask(t CircularTask) circOb {
	return circOb{ID: t.ID, Title: t.Title, Role: t.Role, Due: t.Due, DueRule: t.DueRule, Needs: t.Needs, Area: t.Area, Source: t.Source, Accepted: t.Accepted, NoEvidence: t.NoEvidence, Steps: t.Steps}
}

func circObOfProposal(c Circular, p CircObligation) circOb {
	due, rule := p.due(c)
	return circOb{ID: p.ID, Title: p.What, Role: p.Role, Due: due, DueRule: rule, Needs: p.Evidence, Area: p.Area, Source: "ai", Steps: p.Steps, Proposed: true}
}

// circObFor finds an action or a proposal of a circular.
func circObFor(c Circular, id string) (circOb, *CircularTask, *CircObligation, bool) {
	for i := range c.Tasks {
		if c.Tasks[i].ID == id {
			return circObOfTask(c.Tasks[i]), &c.Tasks[i], nil, true
		}
	}
	for i := range c.Proposals {
		if c.Proposals[i].ID == id {
			return circObOfProposal(c, c.Proposals[i]), nil, &c.Proposals[i], true
		}
	}
	return circOb{}, nil, nil, false
}

// circCardText is a new card's description.
func circCardText(c Circular, a Applicability, ob circOb) string {
	var s strings.Builder
	fmt.Fprintf(&s, "**%s circular %s of %s**", c.Source, c.Ref, fmtDay(c.Date))
	if c.Dept != "" {
		fmt.Fprintf(&s, " (%s)", c.Dept)
	}
	fmt.Fprintf(&s, "\n%s\n\n", c.Subject)
	fmt.Fprintf(&s, "**What to do:** %s\n", ob.Title)
	if ob.Due != "" {
		fmt.Fprintf(&s, "**By when:** %s", fmtDay(ob.Due))
		if ob.DueRule != "" {
			fmt.Fprintf(&s, " (%s)", ob.DueRule)
		}
		s.WriteString("\n")
	}
	fmt.Fprintf(&s, "**Owner:** %s\n", firstOf(ob.Role, RoleCompliance))
	if ob.NoEvidence {
		s.WriteString("**Evidence needed:** none\n")
	} else {
		fmt.Fprintf(&s, "**Evidence needed:** %s\n", firstOf(ob.Needs, "a note or file showing it was done"))
	}
	if circSoftware(ob.Area) {
		ar, _ := circArea(ob.Area)
		fmt.Fprintf(&s, "**Ledgerline may need a change:** %s", ar.Name)
		if ar.Where != "" {
			fmt.Fprintf(&s, " (%s)", ar.Where)
		}
		s.WriteString("\n")
	}
	fmt.Fprintf(&s, "\n%s.\n", a.Label())
	switch {
	case ob.Proposed:
		s.WriteString("Proposed by the AI model from the circular's text: the compliance officer accepts, changes or rejects it on Calendar & circulars.\n")
	case ob.Source == "ai":
		s.WriteString("From the AI model's reading of the circular, accepted by " + firstOf(ob.Accepted, "the compliance officer") + ".\n")
	case ob.Source == "person":
		s.WriteString("Added by " + firstOf(ob.Accepted, "the compliance officer") + ".\n")
	default:
		s.WriteString("From Ledgerline's rules for circulars on this subject.\n")
	}
	s.WriteString("Open the circular in Ledgerline from this card (Open in Ledgerline).")
	if c.URL != "" {
		s.WriteString(" The issuer's copy: " + c.URL)
	}
	return s.String()
}

// circCardSteps and circCardEvidence are a new card's checklist items.
func circCardSteps(ob circOb) []string {
	if len(ob.Steps) > 0 {
		return ob.Steps
	}
	return []string{"Read the circular", clip(ob.Title, 200), "Record what was done and where the evidence is"}
}

func circCardEvidenceItems(ob circOb) []string {
	if ob.NoEvidence {
		return nil
	}
	var out []string
	for _, x := range strings.Split(firstOf(ob.Needs, "A note or file showing it was done"), ";") {
		if x = strings.TrimSpace(x); x != "" && len(out) < 8 {
			out = append(out, clip(x, 200))
		}
	}
	return out
}

func circCardChecklists(ob circOb) []Checklist {
	mk := func(name string, items []string) Checklist {
		cl := Checklist{ID: boardID("CL-", 4), Name: name, Items: []CheckItem{}}
		for _, t := range items {
			cl.Items = append(cl.Items, CheckItem{ID: boardID("I-", 4), Text: t})
		}
		return cl
	}
	out := []Checklist{mk("Steps", circCardSteps(ob))}
	if ev := circCardEvidenceItems(ob); len(ev) > 0 {
		out = append(out, mk("Evidence", ev))
	}
	return out
}

// bsCirculars are the circulars' obligations as cards.
func bsCirculars(ctx context.Context, o *Ops, tenant string, env *bsEnv) ([]bsRec, error) {
	cs, err := o.Circulars(ctx, tenant)
	if err != nil {
		return nil, err
	}
	var out []bsRec
	for _, c := range cs {
		c := c
		a := Applicability{Verdict: ApplyUnsure}
		if c.Applies != nil {
			a = *c.Applies
		}
		na := c.Status == "Not applicable" || a.Verdict == ApplyNo
		naAt := ""
		if c.Override != nil && c.Override.Verdict == ApplyNo {
			naAt = c.Override.At
		}
		rec := func(ob circOb, status string, closed bool, at string, link string) {
			r := bsRec{Key: "circular|" + c.ID + "|" + ob.ID, Status: status, Closed: closed, ClosedAt: bsTime(at), Due: bsDue(ob.Due), Order: ob.Due + "|" + c.ID + "|" + ob.ID,
				Title: c.Ref + ": " + ob.Title}
			r.Labels = []bsLabel{circIssuerLabel(c.Source), bsRoleLabel(firstOf(ob.Role, RoleCompliance))}
			if circSoftware(ob.Area) {
				r.Labels = append(r.Labels, bsCircUpdate)
			}
			if a.Verdict == ApplyUnsure {
				r.Labels = append(r.Labels, bsCircUnsure)
			}
			if ob.Source == "ai" {
				r.Labels = append(r.Labels, bsCircAI)
			}
			if c.Priority == "High" {
				r.Labels = append(r.Labels, bsCircHigh)
			}
			r.Link = CardLink{Kind: "circular", ID: c.ID + "|" + ob.ID, Label: clip(bsScrub(c.Source+" "+c.Ref+" · "+ob.Title), 120), View: "circulars", Status: link}
			ob, aa := ob, a
			r.New = func() (string, []Checklist) { return circCardText(c, aa, ob), circCardChecklists(ob) }
			out = append(out, r)
		}
		for _, t := range c.Tasks {
			ob := circObOfTask(t)
			switch {
			case t.Status == "Done":
				rec(ob, "done", true, t.At, "Done")
			case t.Status == "Dropped":
				rec(ob, "na", true, t.At, "Not applicable")
			case na:
				rec(ob, "na", true, naAt, "Not applicable")
			case !c.taskAccepted(t):
				rec(ob, "review", false, "", "To review")
			case t.Stage == StageInProgress:
				rec(ob, "doing", false, "", "In progress")
			case t.Stage == StageEvidence:
				rec(ob, "evidence", false, "", "Waiting for evidence")
			default:
				rec(ob, "todo", false, "", "Accepted")
			}
		}
		for _, p := range c.Proposals {
			ob := circObOfProposal(c, p)
			switch {
			case p.Status == ObRejected:
				rec(ob, "na", true, p.At, "Not applicable")
			case p.Status != ObProposed:
			case na:
				rec(ob, "na", true, naAt, "Not applicable")
			default:
				rec(ob, "review", false, "", "To review")
			}
		}
	}
	return out, nil
}

// circSplit reads a card's link: the circular and its obligation.
func circSplit(id string) (string, string, bool) {
	i := strings.LastIndex(id, "|")
	if i <= 0 || i == len(id)-1 {
		return "", "", false
	}
	return id[:i], id[i+1:], true
}

// circBoardMove does a card's move on the circular.
func circBoardMove(ctx context.Context, o *Ops, tenant, actor string, cd Card, to BoardList) (string, error) {
	cid, obID, ok := circSplit(cd.Link.ID)
	if !ok {
		return "", fmt.Errorf("%w: no such obligation", ErrNotFound)
	}
	c, _, err := docs.Get[Circular](ctx, o.Docs, tenant, cCircular, cid)
	if err != nil {
		return "", fmt.Errorf("%w: the circular is no longer in Ledgerline", ErrNotFound)
	}
	_, t, p, ok := circObFor(c, obID)
	if !ok {
		return "", fmt.Errorf("%w: no such obligation on %s", ErrNotFound, c.Ref)
	}
	act := func(a CircularAction) (string, error) {
		a.Task = obID
		_, err := o.ActOnCircular(ctx, tenant, actor, cid, a)
		return circListName[to.Source], err
	}
	switch to.Source {
	case "review":
		return "", invalid("an obligation does not go back to review from the board: on Calendar & circulars, drop it as not needed with the reason")
	case "na":
		return "", invalid("say why it is not needed: on Calendar & circulars, use Not needed for an action, Reject for a proposal, or decide that the circular does not apply")
	}
	if p != nil {
		if p.Status != ObProposed {
			return "", fmt.Errorf("%w: this proposal was %s", ErrState, strings.ToLower(p.Status))
		}
		if to.Source != "todo" {
			return "", invalid("accept the proposed obligation first: move it to Accepted / to do (the compliance officer), or change it before accepting on Calendar & circulars")
		}
		_, err := o.ActOnCircular(ctx, tenant, actor, cid, CircularAction{Kind: "accept", Proposal: obID})
		return circListName["todo"], err
	}
	if t.Status != "Open" {
		return "", invalid("a %s action does not reopen from the board; reopen the circular on Calendar & circulars", strings.ToLower(t.Status))
	}
	if !c.taskAccepted(*t) {
		if to.Source != "todo" {
			return "", invalid("the compliance officer accepts this action first: move it to Accepted / to do, or mark the circular reviewed on Calendar & circulars")
		}
		return act(CircularAction{Kind: "accept_task"})
	}
	switch to.Source {
	case "todo":
		return act(CircularAction{Kind: "stage", Stage: ""})
	case "doing":
		return act(CircularAction{Kind: "stage", Stage: StageInProgress})
	case "evidence":
		return act(CircularAction{Kind: "stage", Stage: StageEvidence})
	case "done":
		text, files, err := circCardEvidence(cd, *t)
		if err != nil {
			return "", err
		}
		return act(CircularAction{Kind: "done_task", Evidence: text, files: files, card: cd.Board + "|" + cd.ID})
	}
	return "", invalid("unknown list")
}

// circCardEvidence is the evidence on a card, or why it is not enough to
// finish its action.
func circCardEvidence(cd Card, t CircularTask) (string, []EvidenceFile, error) {
	var files []EvidenceFile
	var names []string
	for _, at := range cd.Attachments {
		files = append(files, EvidenceFile{Name: at.Name, File: at.File, URL: at.URL})
		names = append(names, at.Name)
	}
	var ticked, open []string
	for _, cl := range cd.Checklists {
		if !strings.EqualFold(strings.TrimSpace(cl.Name), "Evidence") {
			continue
		}
		for _, it := range cl.Items {
			if it.Done {
				ticked = append(ticked, it.Text)
			} else {
				open = append(open, "“"+clip(it.Text, 60)+"”")
			}
		}
	}
	if !t.NoEvidence {
		if len(files) == 0 {
			return "", nil, invalid("attach the evidence to the card before moving it to Done (%s): the action is closed with it", strings.TrimRight(firstOf(t.Needs, "a note or file showing it was done"), "."))
		}
		if len(open) > 0 {
			return "", nil, invalid("tick the Evidence checklist before moving the card to Done: %s %s not ticked", joinAnd(open), map[bool]string{true: "is", false: "are"}[len(open) == 1])
		}
	}
	text := fmt.Sprintf("Done on the Circulars board, card #%d", cd.No)
	if len(ticked) > 0 {
		text += ". Evidence: " + strings.Join(ticked, "; ")
	}
	if len(names) > 0 {
		text += ". Files: " + strings.Join(names, ", ")
	}
	return text, files, nil
}

// circLinkRecord is an obligation as the card window shows it.
func circLinkRecord(ctx context.Context, o *Ops, tenant, actor string, s bsSeer, id string) (LinkHit, error) {
	if !s.head {
		return LinkHit{}, errBSHidden
	}
	cid, obID, ok := circSplit(id)
	if !ok {
		return LinkHit{}, errBSHidden
	}
	c, _, err := docs.Get[Circular](ctx, o.Docs, tenant, cCircular, cid)
	if err != nil {
		return LinkHit{}, errBSHidden
	}
	c.annotate(o.firmFacts(ctx, tenant))
	ob, t, p, ok := circObFor(c, obID)
	if !ok {
		return LinkHit{}, errBSHidden
	}
	st := "To review"
	switch {
	case t != nil && t.Status == "Done":
		st = "Done"
	case t != nil && t.Status == "Dropped", p != nil && p.Status == ObRejected, c.Status == "Not applicable":
		st = "Not applicable"
	case t != nil && c.taskAccepted(*t):
		st = firstOf(t.Stage, "Accepted")
	}
	h := LinkHit{CardLink: CardLink{Kind: "circular", ID: id, Label: clip(c.Source+" "+c.Ref+" · "+ob.Title, 120), View: "circulars", Status: st},
		Sub: bsJoin(st, firstOf(ob.Role, RoleCompliance), map[bool]string{true: "due " + fmtDay(ob.Due), false: ""}[ob.Due != ""])}
	h.Lines = []string{c.Subject, c.Applies.Label()}
	if ob.NoEvidence {
		h.Lines = append(h.Lines, "No evidence needed")
	} else if ob.Needs != "" {
		h.Lines = append(h.Lines, "Evidence needed: "+ob.Needs)
	}
	if circSoftware(ob.Area) {
		ar, _ := circArea(ob.Area)
		h.Lines = append(h.Lines, "Ledgerline may need a change: "+ar.Name)
	}
	if t != nil && t.Evidence != "" {
		h.Lines = append(h.Lines, bsJoin(t.By, t.Evidence))
	}
	if p != nil && p.Note != "" {
		h.Lines = append(h.Lines, "Rejected: "+p.Note)
	}
	return h, nil
}

// circBoard is the firm's Circulars board, when an Admin has set it up.
func (o *Ops) circBoard(ctx context.Context, tenant string) (Board, bool) {
	all, err := docs.List[Board](ctx, o.Docs, tenant, cBoard, "")
	if err != nil {
		return Board{}, false
	}
	b, ok := bsBoardFor(all, "circulars")
	return b, ok && !b.Closed
}

// circCardRefresh gives an accepted proposal's card what the compliance
// officer changed, where the card still has what Ledgerline wrote: the
// description, and the Steps and Evidence checklists with nothing ticked.
func (o *Ops) circCardRefresh(ctx context.Context, tenant string, before, after Circular, obID string) {
	b, ok := o.circBoard(ctx, tenant)
	if !ok {
		return
	}
	f := o.firmFacts(ctx, tenant)
	before.annotate(f)
	after.annotate(f)
	old, _, _, ok1 := circObFor(before, obID)
	nu, _, _, ok2 := circObFor(after, obID)
	if !ok1 || !ok2 {
		return
	}
	cards, err := o.boardCards(ctx, tenant, b.ID)
	if err != nil {
		return
	}
	oldText, newText := circCardText(before, *before.Applies, old), circCardText(after, *after.Applies, nu)
	same := func(cl Checklist, items []string) bool {
		if len(cl.Items) != len(items) {
			return false
		}
		for i, it := range cl.Items {
			if it.Done || it.Text != items[i] {
				return false
			}
		}
		return true
	}
	redo := func(cl *Checklist, items []string) {
		cl.Items = []CheckItem{}
		for _, t := range items {
			cl.Items = append(cl.Items, CheckItem{ID: boardID("I-", 4), Text: t})
		}
	}
	for _, cd := range cards {
		if cd.Link == nil || cd.Link.Kind != "circular" || cd.Link.ID != after.ID+"|"+obID {
			continue
		}
		changed := false
		_, err := docs.Update(ctx, o.Docs, tenant, cBoardCard, cardKey(b.ID, cd.ID), bsActor, func(x *Card) error {
			if x.Deleted {
				return errCardGone
			}
			changed = false
			if x.Description == oldText && oldText != newText {
				x.Description, changed = newText, true
			}
			hasEv := false
			for i := range x.Checklists {
				cl := &x.Checklists[i]
				switch strings.TrimSpace(cl.Name) {
				case "Steps":
					if same(*cl, circCardSteps(old)) && !same(*cl, circCardSteps(nu)) {
						redo(cl, circCardSteps(nu))
						changed = true
					}
				case "Evidence":
					hasEv = true
					if same(*cl, circCardEvidenceItems(old)) && !same(*cl, circCardEvidenceItems(nu)) {
						redo(cl, circCardEvidenceItems(nu))
						changed = true
					}
				}
			}
			if !hasEv && old.NoEvidence && !nu.NoEvidence {
				cl := Checklist{ID: boardID("CL-", 4), Name: "Evidence"}
				redo(&cl, circCardEvidenceItems(nu))
				x.Checklists, changed = append(x.Checklists, cl), true
			}
			if !changed {
				return errBSSame
			}
			x.UpdatedAt = o.now().Format(time.RFC3339)
			return nil
		})
		if err == nil && changed {
			o.boardLog(ctx, tenant, bsActor, b, cd.ID, "card.updated", fmt.Sprintf("brought #%d up to date: the obligation as accepted", cd.No), nil)
		}
	}
}

// ---- the minute: AI reading of new circulars, the daily notice ----

type circState struct {
	Noticed string `json:"noticed,omitempty"` // the day the daily notice went
	Checked string `json:"checked,omitempty"` // when it was last worked out (RFC3339)
}

func (o *Ops) circTick(ctx context.Context, tenant string, now time.Time) {
	o.circAIQueue(ctx, tenant, now)
	o.circDailyNotice(ctx, tenant, now)
}

// circAIQueue has the AI model read new circulars that may apply, a few
// a minute, once each.
func (o *Ops) circAIQueue(ctx context.Context, tenant string, now time.Time) {
	if on, _ := o.aiFeatureState(ctx, tenant, AIFeatureCirculars); !on {
		return
	}
	cs, err := o.Circulars(ctx, tenant)
	if err != nil {
		return
	}
	n := 0
	for _, c := range cs {
		if n >= circAIPerTick {
			return
		}
		loaded := bsTime(c.Loaded)
		if c.AI != nil || c.Status != "New" || (c.Priority != "High" && c.Priority != "Medium") || len(strings.TrimSpace(c.Text)) < 200 ||
			loaded.IsZero() || now.Sub(loaded) > circAINewFor || c.Applies == nil || c.Applies.Verdict == ApplyNo {
			continue
		}
		n++
		if _, err := o.ReadCircularAI(ctx, tenant, circAIActor, c.ID); err != nil {
			if _, refused := aiRefusal(err); refused || errors.Is(err, ErrInvalid) {
				return // the gate said no: try again next minute, as the gate decides
			}
		}
	}
}

// circNoticeText is the daily notice: counts only.
func circNoticeText(apply, unsure, no, review int) string {
	pl := func(n int, one, many string) string {
		if n == 1 {
			return fmt.Sprintf("%d %s", n, one)
		}
		return fmt.Sprintf("%d %s", n, many)
	}
	var parts []string
	switch {
	case apply > 0:
		parts = append(parts, pl(apply, "new circular applies to you", "new circulars apply to you"))
		if unsure > 0 {
			parts = append(parts, pl(unsure, "may apply", "may apply"))
		}
	case unsure > 0:
		parts = append(parts, pl(unsure, "new circular may apply to you", "new circulars may apply to you"))
	}
	if review > 0 {
		parts = append(parts, pl(review, "obligation needs review", "obligations need review"))
	}
	if no > 0 {
		parts = append(parts, pl(no, "looks not to apply: confirm it", "look not to apply: confirm them"))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, ", ") + " (Calendar & circulars)"
}

// circInbox counts the circulars not yet reviewed, by whether they apply,
// and their obligations waiting to be accepted.
func circInbox(cs []Circular) (apply, unsure, no, review int) {
	for _, c := range cs {
		if c.Status != "New" || c.Applies == nil {
			continue
		}
		switch c.Applies.Verdict {
		case ApplyYes:
			apply++
			review += c.Review
		case ApplyUnsure:
			unsure++
			review += c.Review
		default:
			no++
		}
	}
	return
}

// circDailyNotice tells the compliance officer, once a day, what new
// circulars need, in the bell on the Circulars board.
func (o *Ops) circDailyNotice(ctx context.Context, tenant string, now time.Time) {
	loc := now.In(ist)
	today := loc.Format("2006-01-02")
	if loc.Format("15:04") < circNoticeAt {
		return
	}
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cCircState, "main", circState{})
	if st.Noticed == today {
		return
	}
	if t := bsTime(st.Checked); !t.IsZero() && now.Sub(t) >= 0 && now.Sub(t) < circCheckEvery {
		return
	}
	st.Checked = now.Format(time.RFC3339)
	defer func() { _ = docs.Upsert(ctx, o.Docs, tenant, cCircState, "main", st, SystemActor) }()
	b, ok := o.circBoard(ctx, tenant)
	if !ok {
		return
	}
	cs, err := o.Circulars(ctx, tenant)
	if err != nil {
		return
	}
	text := circNoticeText(circInbox(cs))
	if text == "" {
		return
	}
	us, _ := o.Users(ctx, tenant)
	var to []string
	for _, role := range []string{RoleCompliance, RoleAdmin} {
		for _, u := range us {
			if u.Active && u.Role == role {
				to = append(to, u.ID)
			}
		}
		if len(to) > 0 {
			break
		}
	}
	if len(to) == 0 {
		return
	}
	o.boardNotifyKind(ctx, tenant, circAIActor, b, "", NoticeOther, to, text)
	st.Noticed = today
}
