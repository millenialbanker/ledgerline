package help

import (
	"strings"
	"testing"
)

// TestDPControlsQuestions: questions about DIS books, the checks on a
// slip, request timelines, the daily reconciliation, the blank stock, pool
// pay-outs and clients' trading freeze requests find their articles.
func TestDPControlsQuestions(t *testing.T) {
	k := kb(t)
	for _, c := range []struct{ q, view, want string }{
		{"client lost his dis booklet, how do I issue a new one", "", "dis-books"},
		{"new dis book needs the requisition slip", "dp", "dis-books"},
		{"how many loose slips can a client get", "", "dis-books"},
		{"dis from a dormant account needs a recorded call", "", "dis-slip-checks"},
		{"who countersigns a dis above 5 lakh", "", "dis-slip-checks"},
		{"closure with dues outstanding what is the timeline", "", "dp-request-timelines"},
		{"follow up rta 21 days demat request", "", "dp-request-timelines"},
		{"daily dp reconciliation sign off", "", "dp-daily-recon"},
		{"count blank dis stock", "", "dis-stock"},
		{"cdsl charge for pool delay", "", "pool-payouts"},
		{"client wants to freeze trading", "", "trade-freeze"},
		{"re-enable trading after freeze", "rms", "trade-freeze"},
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
