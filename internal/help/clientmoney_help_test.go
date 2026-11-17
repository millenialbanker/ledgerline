package help

import (
	"strings"
	"testing"
)

// TestClientMoneyQuestions: questions about the UCC-wise settlement report,
// the firm's bank accounts and the daily client money reconciliation find
// their articles.
func TestClientMoneyQuestions(t *testing.T) {
	k := kb(t)
	for _, c := range []struct{ q, view, want string }{
		{"how do I submit ucc wise settlement details to nse", "", "ras-ucc-report"},
		{"report the running account settlement client wise", "", "ras-ucc-report"},
		{"ras report due in 10 trading days", "ras", "ras-ucc-report"},
		{"which of our accounts is the dscnba", "", "firm-bank-accounts"},
		{"add the uscnba", "clientmoney", "firm-bank-accounts"},
		{"daily reconciliation statement of funds transferred to own account", "", "client-money-recon"},
		{"money left in dscnba at end of day", "", "client-money-recon"},
		{"sign off the client money reconciliation", "", "client-money-recon"},
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
