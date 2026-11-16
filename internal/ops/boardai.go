package ops

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// ---- Boards: AI on cards ----
//
// Two actions in the card window, in the manner of Atlassian
// Intelligence. Both go through the AI gate for the feature
// AIFeatureBoards (switched on in Settings, off by default), with the
// card's text masked, the call metered from the firm's AI credit and
// written to the AI answer log (aiCompose in ailog.go):
//
//   - Summarise this card: from its description, checklists and last 30
//     comments. The summary is shown to the person who asked, who may
//     post it as a comment or copy it; it is never posted by itself.
//   - Suggest a checklist: up to 10 items from the title and description,
//     for the person to pick from before any is added.
//
// Cards on Ledgerline boards are refused: they are the firm's own records
// (clients, breaks, complaints), which Ledgerline does not send to a model.

const (
	cardAIComments = 30   // comments a summary reads, the latest
	cardAIItems    = 10   // most checklist items suggested
	cardAIMaxText  = 24e3 // characters of card text sent at most

	cardAISummaryInstr = `You summarise one card from a task board used by the back-office staff of an Indian stockbroking firm.
Use ONLY the card text provided. Write 2 to 5 short plain sentences, or up to 5 short "- " bullets: what the card is about, where it stands (what is done and what is left), any dates, and who is doing what.
Keep tokens such as [CLIENT-1], [STAFF-2] or [PAN-1] exactly as they are; they stand for values you are not shown.
Do not invent facts, do not give investment advice, do not add a heading. Plain text only.`

	cardAIChecklistInstr = `You suggest checklist items for one card on a task board used by the back-office staff of an Indian stockbroking firm.
From the card's title and description only, list up to 10 short, concrete steps someone would tick off to finish the work, in the order they would be done.
Reply with one step per line and nothing else: no numbering, no heading, no blank lines. Each step under 12 words.
Keep tokens such as [CLIENT-1] or [STAFF-2] exactly as they are. Do not give investment advice. If the card gives too little to go on, reply with the single word NONE.`
)

// CardAIState says whether the AI actions may be offered on a card.
type CardAIState struct {
	On         bool   `json:"on"`
	Why        string `json:"why,omitempty"`         // why not, when off
	CanSuggest bool   `json:"can_suggest,omitempty"` // the person may add checklists to the card
}

// CardAIReply is what the model wrote, for the person to use or not.
type CardAIReply struct {
	Text  string   `json:"text,omitempty"`  // a summary
	Items []string `json:"items,omitempty"` // suggested checklist items
	By    string   `json:"by"`              // the provider and model
	Note  string   `json:"note"`
}

// cardAIFor is the card and board when the AI may be used on them.
func (o *Ops) cardAIFor(ctx context.Context, tenant, actor, board, card string) (Card, Board, boardViewer, error) {
	b, v, err := o.boardFor(ctx, tenant, actor, board)
	if err != nil {
		return Card{}, Board{}, v, err
	}
	c, _, err := o.GetCard(ctx, tenant, actor, board, card)
	if err != nil {
		return Card{}, b, v, err
	}
	if b.Kind != BoardTeam {
		return c, b, v, invalid("AI is not used on Ledgerline boards: their cards are the firm's own records")
	}
	return c, b, v, nil
}

// CardAI is whether the AI actions may be offered on a card now, and why not.
func (o *Ops) CardAI(ctx context.Context, tenant, actor, board, card string) (CardAIState, error) {
	c, b, v, err := o.cardAIFor(ctx, tenant, actor, board, card)
	if err != nil {
		if b.ID != "" && c.ID != "" {
			return CardAIState{Why: strings.TrimPrefix(err.Error(), "invalid: ") + "."}, nil
		}
		return CardAIState{}, err
	}
	on, why := o.aiFeatureState(ctx, tenant, AIFeatureBoards)
	return CardAIState{On: on, Why: why, CanSuggest: on && v.can(BoardAdmin, BoardMember) && !b.Closed && !c.Archived}, nil
}

func cardAIError(err error) error {
	if note, refused := aiRefusal(err); refused {
		return invalid("%s", note)
	}
	return err
}

// cardAIText is the card's text for a summary: title, description,
// checklists with what is done, and the latest comments (oldest first),
// with who wrote them and when.
func (o *Ops) cardAIText(ctx context.Context, tenant string, b Board, c Card) string {
	var s strings.Builder
	l, _ := boardList(b, c.List)
	fmt.Fprintf(&s, "Card #%d: %s\nList: %s\n", c.No, c.Title, l.Name)
	if c.Archived {
		s.WriteString("The card is archived.\n")
	}
	var labels []string
	for _, id := range c.Labels {
		if lb, ok := boardLabel(b, id); ok && lb.Name != "" {
			labels = append(labels, lb.Name)
		}
	}
	if len(labels) > 0 {
		s.WriteString("Labels: " + strings.Join(labels, ", ") + "\n")
	}
	name := o.boardNamer(ctx, tenant)
	if len(c.Members) > 0 {
		var ms []string
		for _, m := range c.Members {
			ms = append(ms, name(m))
		}
		s.WriteString("Members: " + strings.Join(ms, ", ") + "\n")
	}
	if c.Start != "" {
		s.WriteString("Starts: " + c.Start + "\n")
	}
	if c.Due != "" {
		s.WriteString("Due: " + c.Due + map[bool]string{true: " (marked complete)", false: ""}[c.DueDone] + "\n")
	}
	if d := strings.TrimSpace(c.Description); d != "" {
		s.WriteString("\nDescription:\n" + clip(d, 8000) + "\n")
	}
	for _, cl := range c.Checklists {
		done := 0
		for _, it := range cl.Items {
			if it.Done {
				done++
			}
		}
		fmt.Fprintf(&s, "\nChecklist %q (%d of %d done):\n", cl.Name, done, len(cl.Items))
		for i, it := range cl.Items {
			if i >= 60 {
				fmt.Fprintf(&s, "- and %d more items\n", len(cl.Items)-i)
				break
			}
			mark := "[ ]"
			if it.Done {
				mark = "[x]"
			}
			extra := ""
			if it.Member != "" {
				extra += " (" + name(it.Member) + ")"
			}
			if it.Due != "" {
				extra += " due " + it.Due
			}
			fmt.Fprintf(&s, "%s %s%s\n", mark, clip(it.Text, 300), extra)
		}
	}
	cms := c.Comments
	if len(cms) > cardAIComments {
		cms = cms[len(cms)-cardAIComments:]
	}
	if len(cms) > 0 {
		fmt.Fprintf(&s, "\nLatest comments, oldest first (%d of %d):\n", len(cms), len(c.Comments))
		for _, cm := range cms {
			at := cm.At
			if t, err := time.Parse(time.RFC3339, cm.At); err == nil {
				at = t.In(ist).Format("2006-01-02 15:04")
			}
			fmt.Fprintf(&s, "[%s] %s: %s\n", at, name(cm.By), clip(cm.Text, 1500))
		}
	}
	return clip(s.String(), cardAIMaxText)
}

// boardNamer gives people's names (Ledgerline for its own actors).
func (o *Ops) boardNamer(ctx context.Context, tenant string) func(string) string {
	us, _ := o.Users(ctx, tenant)
	names := map[string]string{}
	for _, u := range us {
		names[u.ID] = u.Name
	}
	return func(id string) string {
		if strings.HasPrefix(id, "system:") {
			return "Ledgerline"
		}
		return firstOf(names[id], id)
	}
}

// SummariseCard has the AI model summarise a card for the person.
func (o *Ops) SummariseCard(ctx context.Context, tenant, actor, board, card string) (CardAIReply, error) {
	c, b, _, err := o.cardAIFor(ctx, tenant, actor, board, card)
	if err != nil {
		return CardAIReply{}, err
	}
	text, by, err := o.aiCompose(ctx, tenant, actor, AIFeatureBoards, cardAISummaryInstr, o.cardAIText(ctx, tenant, b, c), fmt.Sprintf("Summary of card #%d on board %s", c.No, b.ID), 400)
	if err != nil {
		return CardAIReply{}, cardAIError(err)
	}
	if text == "" {
		return CardAIReply{}, fmt.Errorf("%w: the AI model wrote nothing; try again", ErrState)
	}
	o.log(ctx, tenant, actor, "board.card_ai_summary", b.ID, map[string]any{"card": c.ID})
	return CardAIReply{Text: clip(text, 3000), By: by, Note: "Written by AI from this card's text, with names and numbers masked. Check it before you post it."}, nil
}

// checkLeadRe is what a line may start with before an item: bullets,
// numbers and tick boxes ("1.", "- [ ]", "•").
var checkLeadRe = regexp.MustCompile(`^\s*(?:(?:[-*•·]+|\(?\d{1,2}[.)]|\[[ xX]?\])\s*)+`)

// suggestedItems reads the model's lines as checklist items.
func suggestedItems(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, l := range strings.Split(strings.ReplaceAll(text, "\r", ""), "\n") {
		l = strings.TrimSpace(checkLeadRe.ReplaceAllString(l, ""))
		l = strings.Trim(l, "*_`\"")
		k := strings.ToLower(l)
		if l == "" || strings.EqualFold(l, "none") || seen[k] || strings.HasSuffix(l, ":") {
			continue
		}
		seen[k] = true
		out = append(out, clip(l, 200))
		if len(out) == cardAIItems {
			break
		}
	}
	return out
}

// SuggestChecklist has the AI model suggest checklist items for a card,
// for the person to pick from (members of the board).
func (o *Ops) SuggestChecklist(ctx context.Context, tenant, actor, board, card string) (CardAIReply, error) {
	c, b, v, err := o.cardAIFor(ctx, tenant, actor, board, card)
	if err != nil {
		return CardAIReply{}, err
	}
	if !v.can(BoardAdmin, BoardMember) {
		return CardAIReply{}, fmt.Errorf("%w: only the board's members can add checklists (your role on it: %s)", ErrForbidden, v.board)
	}
	if b.Closed {
		return CardAIReply{}, fmt.Errorf("%w: the board is closed; reopen it first", ErrState)
	}
	if strings.TrimSpace(c.Description) == "" && len([]rune(strings.TrimSpace(c.Title))) < 8 {
		return CardAIReply{}, invalid("give the card a fuller title or a description first: suggestions come from them")
	}
	text := fmt.Sprintf("Card title: %s\n", c.Title)
	if d := strings.TrimSpace(c.Description); d != "" {
		text += "\nDescription:\n" + clip(d, 8000) + "\n"
	}
	var have []string
	for _, cl := range c.Checklists {
		for _, it := range cl.Items {
			have = append(have, it.Text)
		}
	}
	if len(have) > 0 {
		text += "\nItems the card already has (do not repeat them):\n- " + strings.Join(have[:min(len(have), 40)], "\n- ") + "\n"
	}
	reply, by, err := o.aiCompose(ctx, tenant, actor, AIFeatureBoards, cardAIChecklistInstr, text, fmt.Sprintf("Checklist suggestions for card #%d on board %s", c.No, b.ID), 300)
	if err != nil {
		return CardAIReply{}, cardAIError(err)
	}
	items := suggestedItems(reply)
	o.log(ctx, tenant, actor, "board.card_ai_checklist", b.ID, map[string]any{"card": c.ID, "items": len(items)})
	note := "Suggested by AI from the card's title and description, with names and numbers masked. Tick the ones you want; nothing is added until you choose."
	if len(items) == 0 {
		note = "The AI model had no suggestions for this card. Add more to its description and try again."
	}
	return CardAIReply{Items: items, By: by, Note: note}, nil
}
