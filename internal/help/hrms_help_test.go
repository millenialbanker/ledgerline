package help

import (
	"strings"
	"testing"
)

// TestHRMSQuestions: questions about the HRMS and Payroll modules find
// their articles.
func TestHRMSQuestions(t *testing.T) {
	k := kb(t)
	for _, c := range []struct{ q, view, want string }{
		{"how do I switch on hrms", "", "modules"},
		{"employee is leaving, switch off his login on the last day", "", "leaving-employee"},
		{"check in refused, not on office network", "", "phone-checkin"},
		{"kiosk pin locked", "", "office-kiosk"},
		{"can a dealer work from home", "", "hybrid-work"},
		{"apply for casual leave", "", "leave-requests"},
		{"pf ecr file for epfo", "", "payroll-returns"},
		{"professional tax west bengal slab", "", "payroll-statutory"},
		{"send salary through hdfc cbx", "", "payroll-pay"},
		{"pre-clearance for a staff trade", "", "own-trading"},
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
