package help

import (
	"strings"
	"testing"
)

// TestRound6Questions: questions about AMC refunds, direct death
// intimations, CDSL KYC deficiencies, OTP closures, reason-27 unfreezes,
// opt-out declarations, PoA terms, blocked clients' orders, bulk deals,
// wash trades and BSE's CCM penalty file find their articles.
func TestRound6Questions(t *testing.T) {
	k := kb(t)
	for _, c := range []struct{ q, view, want string }{
		{"refund amc on closure pro rata", "", "amc-refund"},
		{"nominee informed death of holder freeze demat", "", "death-intimation-direct"},
		{"cdsl kyc deficiency freeze reason code 20", "", "cdsl-kyc-deficiency"},
		{"otp transfer cum closure pending in cdas", "", "otp-closure"},
		{"unfreeze reason 27 kyc validated", "", "kyc-unfreeze-27"},
		{"client does not want nominee signed opt out form", "", "nomination-optout"},
		{"poa terms checklist stamped not assignable", "", "poa-terms"},
		{"cancel pending orders when client blocked", "", "order-cancel"},
		{"bulk deal 0.5 percent of shares", "", "bulk-deal-check"},
		{"wash trade same client buy and sell", "", "wash-trades"},
		{"bse ccm penalty file", "", "bse-ccm-penalty"},
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
