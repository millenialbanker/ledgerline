package help

import (
	"strings"
	"testing"
)

// TestBoardIOQuestions: questions about import, export, collections,
// stickers, (no) email to a card and the AI on cards find their articles.
func TestBoardIOQuestions(t *testing.T) {
	k := kb(t)
	for _, c := range []struct{ q, want string }{
		{"how do I import a board from trello", "board-import-trello"},
		{"bring our trello board with its comments", "board-import-trello"},
		{"export the board's cards to excel", "board-export"},
		{"print a board", "board-export"},
		{"print a card", "card-share"},
		{"group boards into a collection", "board-collections"},
		{"recently viewed boards", "boards-recent"},
		{"add a sticker to a card", "card-stickers"},
		{"email for this card", "email-to-board"}, // explains why there is none
		{"summarise this card with ai", "card-ai"},
		{"suggest a checklist", "card-ai"},
	} {
		h := k.Search(c.q, "boards", 3)
		if len(h) == 0 || !strings.Contains("|"+c.want+"|", "|"+h[0].Article.ID+"|") {
			got := []string{}
			for _, x := range h {
				got = append(got, x.Article.ID)
			}
			t.Errorf("%q: want %s, got %v", c.q, c.want, got)
		}
	}
}
