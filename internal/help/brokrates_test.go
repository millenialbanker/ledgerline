package help

import (
	"strings"
	"testing"
)

// Questions about brokerage by scrip and client interest rates find the
// new articles.
func TestSpecificRateQuestions(t *testing.T) {
	k := kb(t)
	for _, c := range []struct{ q, want string }{
		{"brokerage by stock", "scrip-groups"},
		{"special brokerage rate on ETFs", "scrip-groups"},
		{"which brokerage rate applied to this trade", "brokerage-preview"},
		{"give a client interest at 15 percent", "client-interest"},
		{"no interest for a staff client", "client-interest"},
	} {
		h := k.Search(c.q, "", 3)
		if len(h) == 0 || !strings.Contains("|"+c.want+"|", "|"+h[0].Article.ID+"|") {
			var got []string
			for _, x := range h {
				got = append(got, x.Article.ID)
			}
			t.Errorf("%q: want %s, got %v", c.q, c.want, got)
		}
	}
}
