package help

import (
	"strings"
	"testing"
)

// TestCyberPrivacyQuestions: questions about unlocking, incident
// follow-ups, cyber controls, clients' data rights, fraud reports,
// erasure, the insider trading code and staff controls find their
// articles.
func TestCyberPrivacyQuestions(t *testing.T) {
	k := kb(t)
	for _, c := range []struct{ q, screen, want string }{
		{"how do I unlock a user who typed the wrong password five times", "", "signin-lock-unlock"},
		{"when is the root cause analysis of an incident due", "", "incident-follow-ups"},
		{"record a restore drill", "", "cyber-controls"},
		{"where is ledgerline's sbom", "", "cyber-controls"},
		{"client wants to know what data we hold about him", "", "portal-terms-data-rights"},
		{"client says a trade is not his, he reported fraud", "", "client-fraud-report"},
		{"erase a closed client's data after retention", "", "data-erasure"},
		{"post trade report after pre-clearance", "", "insider-trading-code"},
		{"dp compliance officer eligibility", "", "staff-controls"},
	} {
		h := k.Search(c.q, c.screen, 3)
		if len(h) == 0 || h[0].Article.ID != c.want {
			got := []string{}
			for _, x := range h {
				got = append(got, x.Article.ID)
			}
			t.Errorf("%q: want %s, got %v", c.q, c.want, strings.Join(got, ", "))
		}
	}
}
