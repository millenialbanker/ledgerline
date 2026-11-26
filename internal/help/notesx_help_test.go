package help

import (
	"strings"
	"testing"
)

// TestNotesKitQuestions: questions about the contract note format and
// remarks, exchange-cancelled trades, ECN authorisation, the kit and the
// client notices of 49-notes-kit.md find their articles.
func TestNotesKitQuestions(t *testing.T) {
	k := kb(t)
	for _, c := range []struct{ q, view, want string }{
		{"weighted average price on the contract note", "", "contract-note-format"},
		{"common contract note across nse and bse", "", "contract-note-format"},
		{"mcx last trading date on contract note", "", "contract-note-format"},
		{"remark on contract note when margin call not met", "", "contract-note-remarks"},
		{"rights entitlement disclaimer", "", "contract-note-remarks"},
		{"exchange cancelled a trade rtcm", "", "trade-cancelled-by-exchange"},
		{"client authorisation for electronic contract notes", "", "ecn-authorisation"},
		{"mitc and guidance note in account opening kit", "", "account-kit"},
		{"client master to bo on account opening", "", "client-master-letter"},
		{"mobile and email changed together registered post", "", "contact-change-letter"},
		{"nomination pop up at login", "", "nomination-popup"},
		{"shares credited to a closed demat account", "", "closed-account-balances"},
		{"transmission missing documents told to claimant", "", "transmission-deficiency"},
	} {
		h := k.Search(c.q, c.view, 3)
		if len(h) == 0 || h[0].Article.ID != c.want {
			got := []string{}
			for _, x := range h {
				got = append(got, x.Article.ID)
			}
			t.Errorf("%q: want %s, got %s", c.q, c.want, strings.Join(got, ", "))
		}
	}
}
