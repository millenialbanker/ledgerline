package ops

import "testing"

// The client master and the freeze master are on the daily checklist of a
// firm that is a CDSL DP, and only then.
func TestCDSLFilesOnDailyChecklist(t *testing.T) {
	o, ctx := newOps(t)
	has := func() (cln, dpe6 *ChecklistItem) {
		cl, err := o.DailyFileChecklist(ctx, T, D)
		if err != nil {
			t.Fatal(err)
		}
		for i := range cl.Items {
			switch cl.Items[i].ID {
			case "cdsl-cln":
				cln = &cl.Items[i]
			case "cdsl-dpe6":
				dpe6 = &cl.Items[i]
			}
		}
		return
	}
	set, _ := o.Settings(ctx, T)
	if !o.cdslDPFirm(ctx, T, set) {
		if c, d := has(); c != nil || d != nil {
			t.Fatal("CDSL files listed for a firm that is not a DP")
		}
		if _, err := o.SaveConnection(ctx, T, "admin", connCDSL, map[string]string{"dp_id": "12081600", "env": cdslEnvMock}, nil); err != nil {
			t.Fatal(err)
		}
	}
	c, d := has()
	if c == nil || d == nil || c.Format != "cdsl-client-master" || c.At != "09:00" || c.Intraday || d.Format != "cdsl-freeze-master" || !d.Intraday || d.Status != "Intraday" {
		t.Fatalf("%+v %+v", c, d)
	}
	o.noteDailyFile(ctx, T, "priya.nair", "cdsl-client-master", "CLN_MSTR_081600_1_I_202610160830_001_P0.csv", "")
	if c, _ := has(); c.Status != "Received" {
		t.Fatalf("client master not ticked off: %+v", c)
	}
}
