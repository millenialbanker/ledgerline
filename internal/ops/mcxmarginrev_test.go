package ops

import (
	"strings"
	"testing"
)

// MCX/MCXCCL/327/2026 (MCXCCL/TECH/121/2026, 1 Jun 2026) revised fields 9,
// 15 and 16 of INTRASAR, field 9 of EODSAR and fields 18 and 19 of the
// margin file, with the live date still to be announced. Files in both the
// earlier and the revised layout read with no issue, the values Ledgerline
// takes come out the same, and the margin reporting upload carries fields 18
// and 19 as MCXCCL's margin file gave them.
func TestMCXClientMarginReportingRevision(t *testing.T) {
	for _, v := range []struct {
		name                 string
		sar9, sar15, sar16   string // INTRASAR fields 9, 15, 16
		eod9                 string // EODSAR field 9
		peakRef, intrasarRef string // margin file fields 18, 19
	}{
		{"earlier layout", "250.00", "10.00", "", "75.00", "", ""},
		{"revised layout", "", "", "02", "", "03", "05"},
	} {
		t.Run(v.name, func(t *testing.T) {
			o, ctx := setup(t)
			ucc, _, _ := mcxClient(t, o)
			lib, _ := XFiles()

			// Short allocations: client code and amount (fields 4 and 5).
			intra := strings.Join([]string{"16OCT2026", "1234", "56845", ucc, "1500.00", "", "", "", v.sar9, "", "", "", "", "", v.sar15, v.sar16}, ",") + "\n"
			eod := strings.Join([]string{"16OCT2026", "1234", "56845", ucc, "900.00", "", "", "", v.eod9}, ",") + "\n"
			for name, data := range map[string]string{"MCX_INTRASAR_56845_20261016.csv": intra, "MCX_EODSAR_56845_20261016.csv": eod} {
				s, err := lib.Recognise(name, []byte(data))
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				if tb, err := s.Read(name, []byte(data)); err != nil || len(tb.Issues) != 0 || len(tb.Rows) != 1 {
					t.Fatalf("%s: %v %+v", name, err, tb.Issues)
				}
				loadX(t, o, name, []byte(data))
			}
			got := map[string]string{}
			for _, r := range ccSection(t, o, "mcx_short_allocation").Rows {
				if r[1] == ucc {
					got[r[0]] = r[3]
				}
			}
			if got["Intraday"] != "1500.00" || got["End of day"] != "900.00" {
				t.Fatalf("short allocations %v", got)
			}

			// Margin file in, margin reporting upload out.
			mrg := strings.Join([]string{"16OCT2026", "56845", ucc, "1000.00", "200.00", "50.00", "", "", "", "", "", "100.00", "1000.00", "", "", "0.00", "0.00", v.peakRef, v.intrasarRef}, ",") + "\n"
			s, err := lib.Recognise("MCX_MARGIN_56845_20261016.csv", []byte(mrg))
			if err != nil || s.ID != "mcx-client-margin" {
				t.Fatalf("margin file recognised as %v (%v)", s, err)
			}
			if tb, err := s.Read("MCX_MARGIN_56845_20261016.csv", []byte(mrg)); err != nil || len(tb.Issues) != 0 {
				t.Fatalf("margin file: %v %+v", err, tb.Issues)
			}
			loadX(t, o, "MCX_MARGIN_56845_20261016.csv", []byte(mrg))
			name, b, err := o.ExchangeFile(ctx, T, "priya.nair", "mcx-margin-reporting", D, XFileOptions{})
			if err != nil || name != "MCX_MARGIN_20261016_.M01" {
				t.Fatalf("margin reporting %s %v", name, err)
			}
			out, _ := lib.Get("mcx-margin-reporting")
			tb, err := out.Read(name, b)
			if err != nil || len(tb.Issues) != 0 || len(tb.Rows) != 1 {
				t.Fatalf("margin reporting read back: %v %+v %q", err, tb.Issues, b)
			}
			f := strings.Split(strings.TrimRight(string(b), "\r\n"), ",")
			if len(f) != 19 || f[2] != ucc || f[3] != "1000.00" || f[17] != v.peakRef || f[18] != v.intrasarRef || f[8] == "" || f[9] == "" || f[10] == "" {
				t.Fatalf("margin reporting line %q", b)
			}
		})
	}
}
