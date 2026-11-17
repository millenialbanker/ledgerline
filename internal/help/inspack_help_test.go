package help

import (
	"strings"
	"testing"
)

// TestInspectionPackQuestions: questions about inspection packs find their
// articles, from the screen and from anywhere.
func TestInspectionPackQuestions(t *testing.T) {
	k := kb(t)
	for _, c := range []struct{ q, view, want string }{
		{"nse inspection data request", "", "inspection-packs"},
		{"prepare documents for the internal auditor", "", "inspection-packs"},
		{"how do I make an inspection pack", "inspack", "inspection-packs"},
		{"draw the same sample again with the seed", "inspack", "inspection-pack-sample"},
		{"who downloaded the inspection pack", "", "inspection-pack-privacy"},
		{"mask personal details for our consultant", "inspack", "inspection-pack-privacy"},
		{"upload the vapt report", "", "inspection-documents"},
		{"which items are to confirm", "inspack", "inspection-pack-templates"},
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
