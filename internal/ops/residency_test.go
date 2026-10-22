package ops

import "testing"

func TestResidency(t *testing.T) {
	for r, want := range map[string]bool{"AWS Mumbai (ap-south-1)": true, "ap-south-2": true, "Azure Central India": true, "E2E Networks, Delhi": true, "us-east-1": false, "AWS Singapore": false, "": false} {
		if InIndia(r) != want {
			t.Errorf("%q: %v", r, !want)
		}
	}
	o, ctx := setup(t)
	o.Residency = Residency{IndiaOnly: true, Region: "AWS Mumbai (ap-south-1)", Database: "db.internal"}
	st, err := o.Security(ctx, T, "vikram.shah", false)
	if err != nil || !st.IndiaOnly || len(st.Residency) < 6 {
		t.Fatalf("%+v %v", st, err)
	}
	for _, l := range st.Residency {
		if l.Status == "outside" {
			t.Errorf("outside India with no AI connected: %+v", l)
		}
	}
	if st.Residency[0].Status != "india" {
		t.Fatalf("hosting: %+v", st.Residency[0])
	}
	o.LedgyAI = &fakeWriter{}
	st, _ = o.Security(ctx, T, "vikram.shah", false)
	found := false
	for _, l := range st.Residency {
		found = found || (l.What == "Ledgy AI phrasing" && l.Status == "outside")
	}
	if !found {
		t.Fatal("AI connection not shown as outside India")
	}
}
