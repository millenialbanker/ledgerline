package ops

import "testing"

func TestTaxLabelsByDate(t *testing.T) {
	for _, c := range []struct{ got, want string }{
		{tdsRef("194H", "2026-03-31"), "section 194H"},
		{tdsRef("194H", "2026-04-01"), "section 393(1) [Table: Sl. No. 1(ii)]"},
		{tdsRef("194C", "2026-10-01"), "section 393(1) [Table: Sl. No. 6(i)]"},
		{tdsRef("194J", "2026-10-01"), "section 393(1) [Table: Sl. No. 6(iii)]"},
		{tdsRef("194IPM", "2026-10-01"), "section 393(1) [Table: Sl. No. 2(ii)]"},
		{tdsRef("194Q", "2026-10-01"), "section 393(1) [Table: Sl. No. 8(ii)]"},
		{tdsRef("195", "2026-10-01"), "section 393(2) [Table: Sl. No. 17]"},
		{TDSSectionLabel("194I", "2026-10-01"), "section 393(1) [Table: Sl. No. 2(ii)] (was 194-I)"},
		{cgRef("st", "2025-04-01"), "section 111A"},
		{cgRef("st", "2026-04-01"), "section 196"},
		{cgRef("lt", "2025-04-01"), "section 112A"},
		{cgRef("lt", "2026-04-01"), "section 198"},
		{msmeRef("2026-03-15"), "section 43B(h)"},
		{msmeRef("2026-06-15"), "section 37(2)(g)"},
		{taxYearWord("2026-04-01"), "tax year"},
		{taxYearWord("2025-04-01"), "financial year"},
		{tdsCertForm("2026-04-01"), "Form 131"},
		{tdsCertForm("2026-03-31"), "Form 16A"},
	} {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
	// Every vendor section has a 2025-Act provision; the rates are unchanged.
	rates := map[string]string{"194C": "2", "194J": "10", "194JA": "2", "194I": "10", "194IPM": "2", "194H": "2", "194Q": "0.1"}
	for _, s := range TDSSections[1:] {
		if _, ok := tdsRefs[s.Code]; !ok || rates[s.Code] != s.Rate {
			t.Errorf("section %+v", s)
		}
	}
}
