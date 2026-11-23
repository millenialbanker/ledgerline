package help

import (
	"strings"
	"testing"
)

// TestSanctionsQuestions: questions about sanctions screening, matches,
// false positives and FATF statements find their articles.
func TestSanctionsQuestions(t *testing.T) {
	k := kb(t)
	for _, c := range []struct{ q, view, want string }{
		{"how do I load the un security council consolidated list", "", "sanctions-screening"},
		{"screen clients against the uapa designated list", "", "sanctions-screening"},
		{"are joint holders screened for sanctions", "kyc", "sanctions-screening"},
		{"client matches a designated person what do I do", "", "sanctions-match"},
		{"report to the central nodal officer within 24 hours", "", "sanctions-match"},
		{"sanctions name match is the wrong person", "", "sanctions-false-positive"},
		{"fatf public statement high risk jurisdiction edd", "", "fatf-statement"},
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
