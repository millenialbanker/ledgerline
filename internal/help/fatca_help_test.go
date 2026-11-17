package help

import (
	"strings"
	"testing"
)

// TestFATCAAndUPIQuestions: questions about FATCA/CRS self-certifications
// and validated UPI IDs find their articles, from the screen and from
// anywhere.
func TestFATCAAndUPIQuestions(t *testing.T) {
	k := kb(t)
	for _, c := range []struct{ q, view, want string }{
		{"how do I record a client's fatca declaration", "", "fatca-crs"},
		{"client is a tax resident of the uk", "", "fatca-crs"},
		{"client has no tin for dubai", "", "fatca-crs"},
		{"fatca columns in the kra upload file", "kyc", "fatca-crs"},
		{"controlling persons of a company for crs", "", "fatca-crs"},
		{"fatca for clients onboarded before august 2024", "", "fatca-legacy"},
		{"load fatca declarations from the old system", "", "fatca-legacy"},
		{"upi id for research analyst fees", "", "upi-collection"},
		{"advisory fee paid to the ia upi id on the bank statement", "bank", "upi-fee-credit"},
		{"kra status on hold", "", "kra-status"},
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
