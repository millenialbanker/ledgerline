package ops

import (
	"errors"
	"testing"
)

func TestDashboardLayouts(t *testing.T) {
	o, ctx := setup(t)

	// Nothing saved: both empty, so the console shows its standard layout.
	v, err := o.DashboardLayouts(ctx, T, "priya.nair")
	if err != nil || v.Mine.Widgets != nil || v.Firm.Widgets != nil {
		t.Fatalf("fresh layouts %+v %v", v, err)
	}

	// A user saves their own; another user doesn't see it.
	mine := []DashWidget{{ID: "attention", W: 8, H: 6}, {ID: "kpi-trades", W: 2, H: 2}}
	if _, err := o.SaveDashboardLayout(ctx, T, "priya.nair", mine); err != nil {
		t.Fatal(err)
	}
	v, _ = o.DashboardLayouts(ctx, T, "priya.nair")
	if len(v.Mine.Widgets) != 2 || v.Mine.Widgets[0] != mine[0] || v.Mine.By != "priya.nair" {
		t.Fatalf("saved layout %+v", v.Mine)
	}
	if v, _ := o.DashboardLayouts(ctx, T, "farhan.khan"); v.Mine.Widgets != nil {
		t.Fatalf("another user sees priya's layout: %+v", v.Mine)
	}

	// Bad layouts are refused.
	for name, ws := range map[string][]DashWidget{
		"duplicate": {{ID: "util", W: 4, H: 4}, {ID: "util", W: 4, H: 4}},
		"too wide":  {{ID: "util", W: 13, H: 4}},
		"zero high": {{ID: "util", W: 4, H: 0}},
		"bad id":    {{ID: "<script>", W: 4, H: 4}},
	} {
		if _, err := o.SaveDashboardLayout(ctx, T, "priya.nair", ws); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	many := make([]DashWidget, DashMaxWidgets+1)
	for i := range many {
		many[i] = DashWidget{ID: "w" + string(rune('a'+i%26)) + string(rune('a'+i/26)), W: 1, H: 1}
	}
	if _, err := o.SaveDashboardLayout(ctx, T, "priya.nair", many); !errors.Is(err, ErrInvalid) {
		t.Errorf("too many widgets: %v", err)
	}

	// Only an Admin sets the firm default.
	if _, err := o.SaveFirmDashboard(ctx, T, "priya.nair", mine); err == nil {
		t.Fatal("an Ops maker set the firm default")
	}
	firm := []DashWidget{{ID: "proc", W: 12, H: 2}}
	if _, err := o.SaveFirmDashboard(ctx, T, "admin", firm); err != nil {
		t.Fatal(err)
	}
	v, _ = o.DashboardLayouts(ctx, T, "farhan.khan")
	if len(v.Firm.Widgets) != 1 || v.Firm.Widgets[0].ID != "proc" {
		t.Fatalf("firm default %+v", v.Firm)
	}

	// Reset (nil) clears the user's own layout; the firm default stays.
	if _, err := o.SaveDashboardLayout(ctx, T, "priya.nair", nil); err != nil {
		t.Fatal(err)
	}
	v, _ = o.DashboardLayouts(ctx, T, "priya.nair")
	if v.Mine.Widgets != nil || len(v.Firm.Widgets) != 1 {
		t.Fatalf("after reset %+v", v)
	}

	// Strangers get nothing.
	if _, err := o.DashboardLayouts(ctx, T, "nobody"); err == nil {
		t.Fatal("unknown user read layouts")
	}
}

func TestDashboardWidgetData(t *testing.T) {
	o, ctx := setup(t)
	d, err := o.Dashboard(ctx, T, D)
	if err != nil {
		t.Fatal(err)
	}
	if d.ClientsTraded == 0 || len(d.TopTurnover) == 0 || len(d.TopTurnover) > dashTop || len(d.TopContracts) == 0 {
		t.Fatalf("top lists: clients %d turnover %d contracts %d", d.ClientsTraded, len(d.TopTurnover), len(d.TopContracts))
	}
	for i := 1; i < len(d.TopTurnover); i++ {
		if d.TopTurnover[i].Value.Cmp(d.TopTurnover[i-1].Value) > 0 {
			t.Fatal("top clients not sorted by turnover")
		}
	}
	if len(d.Sides) != 2 || d.Sides[0].Count+d.Sides[1].Count != d.Trades {
		t.Fatalf("buy/sell %+v vs %d trades", d.Sides, d.Trades)
	}
	if sum := d.Sides[0].Value.Add(d.Sides[1].Value); sum.Cmp(d.Turnover) != 0 {
		t.Fatalf("buy+sell %s, turnover %s", sum.Fixed(2), d.Turnover.Fixed(2))
	}
	for i := 1; i < len(d.TopUtil); i++ {
		if d.TopUtil[i].Util > d.TopUtil[i-1].Util {
			t.Fatal("top utilisation not sorted")
		}
	}
}
