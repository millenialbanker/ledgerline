package help

import (
	"strings"
	"testing"
)

// TestStatementQuestions: questions about the weekly and annual
// statements, returned DP statements, nomination and complaint notices,
// contract note clauses and referral rewards find their articles.
func TestStatementQuestions(t *testing.T) {
	k := kb(t)
	for _, c := range []struct{ q, view, want string }{
		{"does a zero balance client get the weekly statement", "", "weekly-statement"},
		{"weekly statement monday to saturday", "", "weekly-statement"},
		{"annual global transaction statement for clients", "", "annual-statement"},
		{"courier returned the dp statement undelivered", "", "dp-statement-returned"},
		{"final statement when the dp account is closed", "", "dp-statement-returned"},
		{"acknowledgement of nomination form to the client", "", "nomination-acknowledgement"},
		{"serial number in the register of nominations", "", "nomination-acknowledgement"},
		{"escalation matrix in the complaint acknowledgement", "", "complaint-acknowledgement"},
		{"tell client about smart odr when complaint not resolved", "", "complaint-acknowledgement"},
		{"place of supply and sac on contract note", "", "contract-note-clauses"},
		{"mcx jurisdiction clause courts at mumbai", "", "contract-note-clauses"},
		{"can a client earn a referral reward", "", "referral"},
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
