package help

import (
	"strings"
	"testing"
)

// TestSecurityQuestions: questions about incidents, the CERT-In report,
// alert rules, Ledgerline's notices, the SIEM export and log retention
// find their articles.
func TestSecurityQuestions(t *testing.T) {
	k := kb(t)
	for _, c := range []struct{ q, screen, want string }{
		{"how do I report a cyber incident to cert-in", "", "security-incidents"},
		{"we have 6 hours to report the incident, where do I record the reference number", "incidents", "security-incidents"},
		{"who gets security alerts", "incidents", "security-alert-rules"},
		{"alert when there are too many wrong passwords", "", "security-alert-rules"},
		{"ledgerline had an outage, where is the notice", "", "provider-incident-notice"},
		{"send our logs to the siem over syslog", "", "log-export-siem"},
		{"the log export to our soc is stuck", "logexport", "log-export-siem"},
		{"how long are sign-in logs kept", "", "security-log-retention"},
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
