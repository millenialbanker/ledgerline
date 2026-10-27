package ops

import (
	"bytes"
	"compress/gzip"
	"encoding/csv"
	"io"
	"strings"
	"testing"
)

func TestClientCollateralFile(t *testing.T) {
	o, ctx := setup(t)
	rows, err := o.ClientCollateral(ctx, T, D)
	if err != nil || len(rows) == 0 {
		t.Fatalf("rows %d %v", len(rows), err)
	}
	segs := map[string]int{}
	for _, r := range rows {
		segs[r.Segment]++
		if r.Total.Cmp(r.Cash.Add(r.GSec).Add(r.NonCash)) != 0 || r.Cash.Sign() < 0 || r.NonCash.Sign() < 0 {
			t.Fatalf("row %+v", r)
		}
	}
	t.Logf("segments %v", segs)
	set, _ := o.Settings(ctx, T)
	set.MemberCodes = map[string]string{}
	if err := o.SaveSettings(ctx, T, "admin", set); err != nil {
		t.Fatal(err)
	}
	if _, _, err := o.ClientCollateralFile(ctx, T, "priya.nair", D, "FO", false); err == nil || !strings.Contains(err.Error(), "member code") {
		t.Fatalf("no member code: %v", err)
	}
	set.MemberCodes = map[string]string{"NSE": "90123"}
	if err := o.SaveSettings(ctx, T, "admin", set); err != nil {
		t.Fatal(err)
	}
	for seg, n := range segs {
		name, gz, err := o.ClientCollateralFile(ctx, T, "priya.nair", D, seg, seg == "CM")
		if err != nil {
			t.Fatal(err)
		}
		if want := CCSegments[seg] + "_CC01_90123_16102026.csv.gz"; name != want {
			t.Fatalf("name %s", name)
		}
		z, err := gzip.NewReader(bytes.NewReader(gz))
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(z)
		recs, err := csv.NewReader(bytes.NewReader(raw)).ReadAll()
		if err != nil {
			t.Fatal(err)
		}
		want, cols := n, 6
		if seg == "CM" {
			want = n + 1 // with the header row asked for
			if recs[0][0] != "Client/UCC Code" {
				t.Fatalf("cm %v", recs[:2])
			}
		}
		if len(recs) != want || len(recs[len(recs)-1]) != cols {
			t.Fatalf("%s: %d lines of %d, want %d of %d", seg, len(recs), len(recs[0]), want, cols)
		}
	}
	if _, _, err := o.ClientCollateralFile(ctx, T, "meera.iyer", D, "FO", false); err == nil {
		t.Fatal("branch user downloaded")
	}
	if rr, err := o.ReportRows(ctx, T, D, "cc01"); err != nil || len(rr) != len(rows)+1 {
		t.Fatalf("report %d %v", len(rr), err)
	}
}
