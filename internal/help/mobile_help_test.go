package help

import (
	"strings"
	"testing"
)

// TestMobileAppQuestions: questions about the staff app, the client app,
// push notifications and lost phones find their articles.
func TestMobileAppQuestions(t *testing.T) {
	k := kb(t)
	for _, c := range []struct{ q, screen, want string }{
		{"how do I give the staff app to our checkers", "", "staff-app-setup"},
		{"where is the qr code for the staff app", "", "staff-app-setup"},
		{"reject an approval on my phone", "", "staff-app-using"},
		{"check in on the staff app", "", "staff-app-using"},
		{"put our sebi registration and investor charter in the client app", "", "client-app-setup"},
		{"connect firebase for push notifications", "", "push-notifications-fcm"},
		{"do notifications contain client data", "", "push-notifications-fcm"},
		{"a staff member lost their phone", "", "mobile-lost-phone"},
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
