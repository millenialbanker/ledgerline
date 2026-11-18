package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/docs"
)

// fakeNCMS plays NSE Clearing's NCMS for F&O.
type fakeNCMS struct {
	t       *testing.T
	logins  int
	pages   map[string]string // inquiry from sequence -> CSV
	mods    [][]map[string]any
	expired bool
}

func (f *fakeNCMS) Post(ctx context.Context, u string, h http.Header, body []byte) (int, []byte, error) {
	if strings.HasSuffix(u, "/token") || strings.HasSuffix(u, "/ncms-token") {
		if h.Get("Authorization") != "Basic Y2stbmNtczpjcy1uY21z" || h.Get("nonce") == "" {
			return 401, []byte(`{"error":"invalid_client"}`), nil
		}
		f.logins++
		return 200, []byte(fmt.Sprintf(`{"access_token":"tok-%d","token_type":"bearer","expires_in":"3600"}`, f.logins)), nil
	}
	if h.Get("Authorization") != fmt.Sprintf("Bearer tok-%d", f.logins) || f.expired {
		f.expired = false
		return 401, []byte(`{"messages":{"code":"0100401"},"status":"error"}`), nil
	}
	var req struct {
		Data map[string]any `json:"data"`
	}
	json.Unmarshal(body, &req)
	js := func(v any) []byte { b, _ := json.Marshal(v); return b }
	switch {
	case strings.HasSuffix(u, "/ncms-fo/trd-act-inquiry"):
		from := strings.Split(fmt.Sprint(req.Data["trdactInquiry"]), ",")[0]
		return 200, js(map[string]any{"status": "success", "messages": map[string]string{"code": "01010000"}, "data": map[string]any{"msgId": req.Data["msgId"], "trdactInquiry": f.pages[from]}}), nil
	case strings.HasSuffix(u, "/ncms-fo/cp-modification"):
		var mods []map[string]any
		b, _ := json.Marshal(req.Data["cpModData"])
		json.Unmarshal(b, &mods)
		f.mods = append(f.mods, mods)
		return 200, js(map[string]any{"status": "Success", "messages": map[string]string{"success": " Request submitted successfully."}, "data": map[string]string{"code": "01010000"}}), nil
	}
	return 404, nil, nil
}

// ncmsRec builds an F&O trade record (37 fields) from field numbers.
func ncmsRec(v map[int]string) string {
	f := make([]string, 37)
	for i, x := range v {
		f[i-1] = x
	}
	return strings.Join(f, ",")
}

func TestNCMSCPCodes(t *testing.T) {
	o, ctx := setup(t)
	now := time.Date(2026, 11, 18, 11, 0, 0, 0, ist)
	o.Now = func() time.Time { return now }
	cs, _ := o.Clients(ctx, T)
	nri, other := cs[0].UCC, cs[1].UCC
	_ = docs.Upsert(ctx, o.Docs, T, cNRI, nri, NRIDetails{UCC: nri, Account: "NRE", Bank: "Test Bank", PISAccount: "1", Permission: "P1", PermDate: "2026-01-01", Country: "AE", CPCode: "CITI00005680"}, "admin")
	// The NRI client's trade was punched without the CP code; another
	// client's trade needs nothing.
	t1 := ncmsRec(map[int]string{1: "1", 2: "1", 3: "960025162", 5: "42401", 6: "75", 7: "1295000", 8: "1", 9: "2600000001529687", 12: "2", 13: nri, 17: "6001", 22: "P", 23: "12345", 24: "NIFTY", 26: "OPTIDX", 31: "1377619600251621", 34: "1"})
	t2 := ncmsRec(map[int]string{1: "2", 2: "1", 3: "960025163", 5: "42401", 6: "75", 7: "1300000", 8: "2", 9: "2600000001529688", 12: "2", 13: other, 17: "6001", 22: "P", 23: "12345", 24: "NIFTY", 26: "OPTIDX", 31: "1377619600251622", 34: "1"})
	f := &fakeNCMS{t: t, pages: map[string]string{"0": "2,2^" + t1 + "^" + t2, "2": "2,0"}}
	o.NCMS = f
	if _, err := o.SaveConnection(ctx, T, "admin", connNCMS, map[string]string{"member": "12345", "env": "UAT", "fetch": "On", "fix": "Off"}, map[string]string{"api_key": "ck-ncms", "secret": "cs-ncms"}); err != nil {
		t.Fatal(err)
	}
	run, err := o.NCMSFetch(ctx, T, "admin", false)
	if err != nil || run.Trades != 2 || run.Sent != 0 {
		t.Fatalf("read %+v %v", run, err)
	}
	v := o.NCMSStatus(ctx, T)
	if len(v.Wrong) != 1 || v.Wrong[0].UCC != nri || v.Wrong[0].Price.String() != "12950" || v.State.Seq != 2 {
		t.Fatalf("status %+v", v)
	}
	// Correcting sends the client's CP code once, with the trade's details.
	if run, err = o.NCMSFetch(ctx, T, "admin", true); err != nil || run.Sent != 1 || len(f.mods) != 1 {
		t.Fatalf("fix %+v %v", run, err)
	}
	m := f.mods[0][0]
	if m["newCPCode"] != "CITI00005680" || m["oldCPCode"] != "" || m["uniqId"] != "1377619600251621" || m["orderNo"] != "2600000001529687" || fmt.Sprint(m["trdNo"]) != "9.60025162e+08" {
		t.Fatalf("modification %v", m)
	}
	if run, _ = o.NCMSFetch(ctx, T, "admin", true); run.Sent != 0 {
		t.Fatal("the change was sent twice")
	}
	// NCMS reports the change done (new CP image), then a custodian's
	// rejection of the trade, which goes to Risk.
	now = now.Add(10 * time.Minute)
	f.expired = true // the token ran out: Ledgerline logs in again once
	a1 := ncmsRec(map[int]string{1: "3", 3: "960025162", 5: "42401", 14: "CITI00005680", 17: "9001", 31: "1377619600251621", 32: "0", 34: "8"})
	a2 := ncmsRec(map[int]string{1: "4", 3: "960025162", 5: "42401", 14: "CITI00005680", 17: "9001", 31: "1377619600251621", 32: "0", 34: "14"})
	f.pages["2"], f.pages["4"] = "4,2^"+a1+"^"+a2, "4,0"
	if run, err = o.NCMSFetch(ctx, T, SystemActor, false); err != nil || run.Actions != 2 || f.logins != 2 {
		t.Fatalf("actions %+v %v (logins %d)", run, err, f.logins)
	}
	ch, _, _ := docs.Get[NCMSChange](ctx, o.Docs, T, cNCMSChange, "FO|2026-11-18|1377619600251621")
	tr, _, _ := docs.Get[NCMSTrade](ctx, o.Docs, T, cNCMSTrade, "FO|2026-11-18|1377619600251621")
	if ch.Status != "Done" || tr.CPCode != "CITI00005680" || tr.Status != "R" {
		t.Fatalf("change %+v trade %+v", ch, tr)
	}
	ws, _ := docs.List[WorkItem](ctx, o.Docs, T, cWorkItem, "")
	found := false
	for _, w := range ws {
		found = found || (w.Process == "ncms" && strings.HasPrefix(w.Key, "ncms:rejected:") && w.Role == RoleRisk && w.Status == "Open")
	}
	if !found {
		t.Fatal("no work item for the custodian's rejection")
	}
	if v = o.NCMSStatus(ctx, T); len(v.Wrong) != 0 {
		t.Fatalf("still wrong %+v", v.Wrong)
	}
}
