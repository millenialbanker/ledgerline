package ops

import (
	"context"
	"fmt"
	"strings"

	"ledgerline/internal/xfile"
)

// ---- CDSL report files for compliance (DPF7, DPG1, DPD1) ----
//
// CDSL sends a DP three daily reports that need a person's review rather
// than a posting: debits in dormant accounts (DPF7), high-value transactions
// (DPG1) and changes CDSL recorded on its demat accounts (DPD1, for example a
// freeze). Each line becomes an alert on Compliance → Alerts, raised once.
// The layouts were read from a DP's own files; CDSL has published no format
// note for them, so fields without a known meaning are kept as they are.

func (o *Ops) boClients(ctx context.Context, tenant string) map[string]string {
	cs, _ := o.ClientMap(ctx, tenant)
	by := map[string]string{}
	for _, c := range cs {
		if c.Demat.ID != "" {
			by[normDematID(c.Demat.ID)] = c.UCC
		}
	}
	return by
}

// ddmmyyyy turns 16102026 into 2026-10-16 ("" if it is not a date).
func ddmmyyyy(s string) string {
	s = strings.TrimSpace(s)
	if len(s) != 8 || strings.Trim(s, "0123456789") != "" {
		return ""
	}
	return s[4:] + "-" + s[2:4] + "-" + s[:2]
}

func (o *Ops) loadCDSLReport(ctx context.Context, tenant, actor string, s *xfile.Spec, rows []xfile.Row) (int, int) {
	by := o.boClients(ctx, tenant)
	added, dup := 0, 0
	raise := func(a Alert, key string) {
		a.Source = "CDSL"
		if o.raiseAlert(ctx, tenant, actor, a, key) {
			added++
		} else {
			dup++
		}
	}
	switch s.ID {
	case "cdsl-dormant-txn":
		for _, r := range rows {
			if r.Blank() || r.Get("BOID") == "" {
				continue
			}
			bo := normDematID(r.Get("BOID"))
			raise(Alert{Type: "Debit in a dormant demat account", UCC: by[bo], Severity: "Medium", Date: ddmmyyyy(r.Get("TxnDate")),
				Detail: fmt.Sprintf("BO %s: %s of %s (%s) on %s; %s. Check the instruction was genuinely the client's.", MaskAccount(bo), r.Get("Quantity"), r.Get("Security"), r.Get("ISIN"), r.Get("TxnDate"), r.Get("Narration"))},
				"DPF7:"+bo+":"+r.Get("TxnDate")+":"+r.Get("ISIN")+":"+r.Get("Narration"))
		}
	case "cdsl-high-value":
		cat, date := "", ""
		for _, r := range rows {
			switch r.Record {
			case "01":
				date = ddmmyyyy(r.Get("ReportDate"))
			case "02":
				cat = r.Get("Category")
			case "03":
				bo := normDematID(r.Get("BOID"))
				raise(Alert{Type: "High-value demat transaction" + map[bool]string{true: " (" + cat + ")", false: ""}[cat != ""], UCC: by[bo], Severity: "Medium", Date: date,
					Detail: fmt.Sprintf("BO %s: %s of %s (%s). Check it fits the client's income and trading profile.", MaskAccount(bo), r.Get("Quantity"), r.Get("Security"), r.Get("ISIN"))},
					"DPG1:"+date+":"+cat+":"+bo+":"+r.Get("Field2")+":"+r.Get("ISIN")+":"+r.Get("Quantity"))
			}
		}
	case "cdsl-bo-modification":
		bo := ""
		for _, r := range rows {
			if r.Record == "#1" {
				f := strings.Fields(r.Get("BOLine"))
				bo = ""
				if len(f) > 0 {
					bo = normDematID(f[len(f)-1])
				}
				continue
			}
			if bo == "" || r.Get("Description") == "" {
				continue
			}
			desc := strings.TrimSpace(r.Get("Description"))
			sev := "Low"
			if strings.Contains(strings.ToUpper(desc), "FROZEN") || strings.Contains(strings.ToUpper(desc), "FREEZE") {
				sev = "High"
			}
			raise(Alert{Type: "Demat account changed at CDSL: " + desc, UCC: by[bo], Severity: sev,
				Detail: fmt.Sprintf("BO %s: %s (%s) changed from %q to %q. Bring the client's record in line; a frozen account must not be used for deliveries or pledges.", MaskAccount(bo), desc, r.Get("FieldCode"), r.Get("OldValue"), r.Get("NewValue"))},
				"DPD1:"+o.now().Format("20060102")+":"+bo+":"+r.Get("FieldCode")+":"+r.Get("OldValue")+">"+r.Get("NewValue"))
		}
	}
	return added, dup
}
