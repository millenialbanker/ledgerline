package help

import (
	"strings"
	"testing"
)

// TestMoneyRulesQuestions: questions about money kept back from the
// clearing corporation, the rules on the firm's bank accounts, cheques to
// clients, client-fund FDRs and journals on client ledgers find their
// articles.
func TestMoneyRulesQuestions(t *testing.T) {
	k := kb(t)
	for _, c := range []struct{ q, view, want string }{
		{"what is reason code rc09 in segmon", "", "retained-funds"},
		{"funds received after cut off how to report", "", "retained-funds"},
		{"where is suspe1234n in the segmon file", "", "retained-funds"},
		{"report a new bank account to the exchange within 7 working days", "", "firm-bank-reporting"},
		{"bank undertaking addendum for the new account", "", "firm-bank-reporting"},
		{"pay the client by cheque because the neft failed", "", "payout-cheque"},
		{"cheque to client not cleared in 5 days", "", "payout-cheque"},
		{"client fdr tenor one year one day", "", "client-fdr"},
		{"journal between two clients", "", "client-journals"},
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
