package ops

import (
	"strings"
	"testing"
)

func TestBSEBlockedList(t *testing.T) {
	o, ctx := setup(t)
	cs := activeIn(t, o, "CM")
	a, b := cs[0].UCC, cs[1].UCC
	loadX(t, o, "EQX_9999_UCC_NotPermittedToTrade_16102026_01.csv", []byte(npHeader+"\r\n"+a+"|||PAN Validation Status is Invalid.|||||||\r\n"))
	loadX(t, o, "EQD_9999_UCC_NotPermittedToTrade_16102026_01.csv", []byte(npHeader+"\r\n"+a+"||||PAN is Invalid by KRA.||||||\r\n"+b+"||||||Primary Or Secondary DP Status is Pending/Invalid.||||\r\n"))

	v, err := o.BSEBlockedList(ctx, T, "priya.nair")
	if err != nil || len(v.Clients) != 2 || v.Lists["CM"] != "2026-10-16" || v.Lists["FO"] != "2026-10-16" {
		t.Fatalf("%+v %v", v, err)
	}
	var ca BSEBlockedClient
	for _, x := range v.Clients {
		if x.UCC == a {
			ca = x
		}
	}
	if !ca.Blocked || strings.Join(ca.Segments, ",") != "CM,FO" || len(ca.Reasons) != 2 || ca.Name != cs[0].Name || !strings.Contains(ca.Reasons[0], "to fix") || len(ca.BSE) != 2 {
		t.Fatalf("client a %+v", ca)
	}
	one, err := o.BSEBlockedFor(ctx, T, "vikram.shah", strings.ToLower(b))
	if err != nil || !one.Blocked || one.Segments[0] != "FO" || !strings.Contains(one.Reasons[0], "demat account 1") {
		t.Fatalf("client b %+v %v", one, err)
	}
	free, err := o.BSEBlockedFor(ctx, T, "priya.nair", cs[2].UCC)
	if err != nil || free.Blocked || len(free.Reasons) != 0 || free.Name == "" {
		t.Fatalf("permitted client %+v %v", free, err)
	}
	if _, err := o.BSEBlockedList(ctx, T, "meera.iyer"); err == nil {
		t.Fatal("branch staff saw the firm-wide list")
	}
}
