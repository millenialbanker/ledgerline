package ops

import (
	"strings"
	"testing"
)

// TestDutyCatalogueFirmDuties: the firm duties added from appendix section 8
// of the November 2026 gap report are in the catalogue with their sources
// (each naming the gap report rows it covers), and apply by the firm
// profile: a self-certification broker clearing at NSE Clearing, a member of
// MCX cleared by another clearing member, a CDSL participant without MTF,
// algo or authorised persons gets the right set, and another kind of firm
// gets its own.
func TestDutyCatalogueFirmDuties(t *testing.T) {
	cat, err := dutyCatalogue()
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]DutyDef{}
	for _, d := range cat.Duties {
		by[d.ID] = d
	}
	added := []string{"CC-T-AP-ACTION-ATR", "CC-T-AP-MISCONDUCT", "CC-T-SURV-QUARTERLY", "CC-T-CYBER-INCIDENT-QTR",
		"CC-T-CSCRF-CATEGORY", "CC-T-CSCRF-ACCESS-REVIEW", "CC-T-CSCRF-ACCESS-REVIEW-QTR", "CC-T-CSCRF-SOC", "CC-T-CSCRF-DRILL",
		"CC-T-CSCRF-CONTROLS", "CC-T-THIRD-PARTY-REVIEW", "CC-T-CYBER-OFFICERS", "CC-T-IBT-CONTROLS", "CC-T-PMLA-OFFICERS",
		"CC-T-ML-RISK-ASSESSMENT", "CC-T-AML-TRAINING", "CC-T-ORDER-EVIDENCE", "CC-T-PROHIBITED-ACTIVITIES",
		"CC-T-NSE-QTR-DECLARATIONS", "CC-T-INSURANCE", "CC-T-BO-FACILITY", "CC-T-MCX-SYSAUDIT", "CC-T-MCX-SYSAUDIT-ATR",
		"CC-T-TRADING-SOFTWARE", "CC-T-SMS-TEMPLATES", "CC-T-INVESTOR-AWARENESS", "CC-T-CYBERFRAUD-LEA", "CC-T-ADVERTISEMENT",
		"CC-T-SEBI-INSPECTION-CSR", "CC-T-REGULATOR-LETTERS", "CC-T-NCL-PENALTY-REVIEW", "CC-T-BSE-CRITICAL-FINDING",
		"CC-T-SEBI-CM-FEE", "CC-T-TDS-DEPOSIT", "CC-T-TDS-RETURNS", "CC-T-DPDP-READINESS", "CC-T-PIT-CODE-REVIEW",
		"CC-T-PIT-VIOLATION", "CC-T-RMS-RULES", "CC-T-EPI-MARGIN", "CC-T-CLIENT-CODE-MODS", "CC-T-MATERIAL-CHANGE",
		"CC-D-COMPLIANCE-CERT", "CC-D-VAPT", "CC-D-SEBI-FEES"}
	for _, id := range added {
		d, ok := by[id]
		if !ok {
			t.Errorf("%s not in the catalogue", id)
			continue
		}
		rows := 0
		for _, r := range d.Refs {
			if strings.Contains(r.Says, "Gap report rows: ") {
				rows++
			}
		}
		if rows == 0 || len(d.Applies) == 0 {
			t.Errorf("%s: no gap report rows in its sources, or no applicability (%v)", id, d.Applies)
		}
	}
	// A merged row is cited on the duty it went to.
	for id, row := range map[string]string{"CC-D-CYBER-INCIDENT": "CYBER-0157", "CC-T-FATF": "NSE-0249", "OB-INTAUDIT": "SEBI-SB1-0033", "OB-SYSAUDIT": "NCL-BSE-0102"} {
		found := false
		for _, r := range by[id].Refs {
			found = found || strings.Contains(r.Says, row)
		}
		if !found {
			t.Errorf("%s does not cite %s", id, row)
		}
	}
	if d := by["CC-T-CYBER-INCIDENT-QTR"]; d.Rule != "01-15,04-15,07-15,10-15" || !strings.Contains(d.DueText, "nil") {
		t.Errorf("quarterly cyber incident report %s %s", d.Rule, d.DueText)
	}
	if d := by["CC-T-NCL-PENALTY-REVIEW"]; d.Rule != "EVENT" || d.EventWithin != "7wd" {
		t.Errorf("NCL penalty review %s %s", d.Rule, d.EventWithin)
	}

	firm := firmFacts{Member: map[string]bool{"NSE": true, "BSE": true, "MCX": true}, Seg: map[string]bool{"CM": true, "FO": true, "COM": true},
		SelfAt: map[string]bool{"NCL": true}, ViaCM: map[string]string{"MCXCCL": "another clearing member"}, Regs: map[string]bool{},
		CDSL: triYes, NSDL: triNo, MTF: triNo, Algo: triNo, IBT: triYes, AP: triNo, Branch: triNo, CSCRF: "self"}
	other := firmFacts{Member: map[string]bool{"NSE": true}, Seg: map[string]bool{"CM": true}, SelfAt: map[string]bool{},
		ViaCM: map[string]string{"NCL": "a clearing member"}, Regs: map[string]bool{}, CDSL: triNo, NSDL: triNo, MTF: triNo,
		Algo: triNo, IBT: triNo, AP: triYes, Branch: triYes, CSCRF: "qualified"}
	for _, c := range []struct {
		id          string
		firm, other string
	}{
		{"CC-T-CSCRF-ACCESS-REVIEW", ApplyYes, ApplyNo},
		{"CC-T-CSCRF-ACCESS-REVIEW-QTR", ApplyNo, ApplyYes},
		{"CC-T-MCX-SYSAUDIT", ApplyYes, ApplyNo},
		{"CC-T-NCL-PENALTY-REVIEW", ApplyYes, ApplyNo},
		{"CC-T-SEBI-CM-FEE", ApplyYes, ApplyNo},
		{"CC-T-AP-ACTION-ATR", ApplyNo, ApplyYes},
		{"CC-T-BSE-CRITICAL-FINDING", ApplyYes, ApplyNo},
		{"CC-T-IBT-CONTROLS", ApplyYes, ApplyNo},
		{"CC-D-COMPLIANCE-CERT", ApplyYes, ApplyNo},
		{"CC-D-VAPT", ApplyYes, ApplyNo},
		{"CC-T-ORDER-EVIDENCE", ApplyYes, ApplyYes},
		{"CC-T-PMLA-OFFICERS", ApplyYes, ApplyYes},
	} {
		if a := dutyApplies(by[c.id].Applies, firm); a.Verdict != c.firm {
			t.Errorf("%s for the firm: %+v, want %s", c.id, a, c.firm)
		}
		if a := dutyApplies(by[c.id].Applies, other); a.Verdict != c.other {
			t.Errorf("%s for another broker: %+v, want %s", c.id, a, c.other)
		}
	}
}
