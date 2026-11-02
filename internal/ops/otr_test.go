package ops

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/money"
)

func TestOTRSlabs(t *testing.T) {
	s := DefaultOTRSettings()
	for _, c := range []struct {
		ratio string
		label string
		paise int
	}{{"49.99", "below 50", 0}, {"50", "50 to below 250", 2}, {"312.5", "250 to below 500", 10}, {"500", "500 to below 1000", 15}, {"1999", "1000 to below 2000", 20}, {"2000", "2000 or more", 25}} {
		sl, label, _ := s.slabFor(money.MustDec(c.ratio))
		if label != c.label || sl.Paise != c.paise {
			t.Errorf("%s: %q %d", c.ratio, label, sl.Paise)
		}
	}
}

func TestOTRAlerts(t *testing.T) {
	o, ctx := setup(t)
	o.Now = func() time.Time { return time.Date(2026, 10, 16, 18, 0, 0, 0, ist) }
	cs := activeIn(t, o, "CM")
	ex, _ := o.App.ExchangeTrades(ctx, T, D)
	traded := map[string]bool{}
	for _, x := range ex {
		if x.Exchange == "BSE" {
			traded[x.UCC] = true
		}
	}
	var quiet string // a client with no BSE trade on the day
	for _, c := range cs {
		if !traded[c.UCC] {
			quiet = c.UCC
			break
		}
	}
	if quiet == "" {
		t.Fatal("every client traded on BSE")
	}
	// 60 order events and no trade: ratio 60, slab 50 to below 250.
	var lines []string
	for i := 0; i < 60; i++ {
		act := "A"
		if i%3 == 2 {
			act = "D"
		}
		lines = append(lines, fmt.Sprintf("B|599901|SYNTH|10|10|10000|0|0|10:15:%02d|16/10/2026|20000000000000%05d|A|%s|EOSESS|%s|ORDER ADDED SUCCESSFULLY|1004|9999000011112222|", i%60, i, quiet, act))
	}
	r := loadX(t, o, "BO161026.9999", []byte(strings.Join(lines, "\n")+"\n"))
	if !strings.Contains(r.Loaded, "over the OTR alert level") {
		t.Fatalf("%+v", r)
	}
	w := openWork(t, o, wOTR)
	cw := w["BSE|"+D+"|Client|"+quiet]
	if cw.Status != "Open" || cw.Role != RoleCompliance || cw.UCC != quiet || !strings.Contains(cw.Title, "60:1") || !strings.Contains(cw.Detail, "50 to below 250, 2 paise") {
		t.Fatalf("client item %+v (all %v)", cw, w)
	}
	if tw := w["BSE|"+D+"|Terminal|9999000011112222"]; tw.Status != "Open" {
		t.Fatalf("terminal item %+v", tw)
	}
	v, err := o.OTRReport(ctx, T, "vikram.shah", D, D)
	if err != nil || len(v.Days) != 1 || v.Days[0].Lines[0].By != "Member" || v.Basis == "" {
		t.Fatalf("%+v %v", v, err)
	}
	var cl OTRLine
	for _, l := range v.Days[0].Lines {
		if l.By == "Client" && l.Key == quiet {
			cl = l
		}
	}
	if cl.Orders != 60 || cl.Trades != 0 || cl.Ratio.Fixed(2) != "60.00" || cl.Slab != "50 to below 250" || !cl.Over || cl.CoolOff || cl.Name == "" {
		t.Fatalf("client line %+v", cl)
	}
	if _, err := o.OTRReport(ctx, T, "priya.nair", "", ""); err == nil {
		t.Fatal("maker saw the OTR report")
	}

	// Raising the alert level and loading again closes the items.
	if _, err := o.SaveOTRSettings(ctx, T, "priya.nair", OTRSettings{AlertFrom: 100}); err == nil {
		t.Fatal("maker changed the OTR rules")
	}
	if _, err := o.SaveOTRSettings(ctx, T, "vikram.shah", OTRSettings{Slabs: []OTRSlab{{50, 2}, {40, 5}}}); err == nil {
		t.Fatal("slabs going down accepted")
	}
	st, err := o.SaveOTRSettings(ctx, T, "vikram.shah", OTRSettings{AlertFrom: 100})
	if err != nil || len(st.Slabs) != 5 || st.CoolOffAt != 500 || st.Count != OTRCountAll {
		t.Fatalf("%+v %v", st, err)
	}
	loadX(t, o, "BO161026.9999", []byte(strings.Join(lines, "\n")+"\n"))
	if cw := openWork(t, o, wOTR)["BSE|"+D+"|Client|"+quiet]; cw.Status != "Done" {
		t.Fatalf("still open below the new level: %+v", cw)
	}

	// Cooling-off at 500, and the algo-only count.
	var big []otrOrder
	for i := 0; i < 520; i++ {
		big = append(big, otrOrder{quiet, "T-NOT-ALGO"})
	}
	day := o.otrCheck(ctx, T, "vikram.shah", "BSE", "2026-10-19", "", big, nil)
	if day.Lines[0].By != "Member" || !day.Lines[0].CoolOff || day.Lines[0].Slab != "500 to below 1000" {
		t.Fatalf("%+v", day.Lines[0])
	}
	if cw := openWork(t, o, wOTR)["BSE|2026-10-19|Client|"+quiet]; !strings.Contains(cw.Detail, "first 15 minutes") {
		t.Fatalf("no cooling-off note: %+v", cw)
	}
	if _, err := o.SaveOTRSettings(ctx, T, "vikram.shah", OTRSettings{Count: OTRCountAlgo}); err != nil {
		t.Fatal(err)
	}
	if day := o.otrCheck(ctx, T, "vikram.shah", "BSE", "2026-10-20", "", big, nil); len(day.Lines) != 0 {
		t.Fatalf("orders of a terminal not marked algo counted: %+v", day.Lines)
	}
}
