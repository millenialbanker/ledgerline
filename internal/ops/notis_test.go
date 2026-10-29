package ops

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// fakeNSE answers like NOTIS with the samples from NSE's specification.
type fakeNSE struct {
	logins  int
	bodies  []map[string]any
	replies []string // CSV payloads, one per inquiry
	code    int
}

func (f *fakeNSE) Token(ctx context.Context, base, key, secret string) (string, int, error) {
	f.logins++
	return "tok-1", 32400, nil
}

func (f *fakeNSE) Inquiry(ctx context.Context, base, path, token string, body []byte) (int, []byte, error) {
	var m map[string]any
	json.Unmarshal(body, &m)
	f.bodies = append(f.bodies, m)
	if f.code != 0 {
		return f.code, []byte(`{}`), nil
	}
	d := m["data"].(map[string]any)
	field := "tradesInquiry"
	if _, ok := d["actionsInquiry"]; ok {
		field = "actionsInquiry"
	}
	csv := f.replies[0]
	f.replies = f.replies[1:]
	b, _ := json.Marshal(map[string]any{"status": "success", "messages": map[string]string{"code": "01010000"}, "data": map[string]string{"msgId": d["msgId"].(string), field: csv}})
	return 200, b, nil
}

func TestNotisTradesFeed(t *testing.T) {
	o, ctx := newOps(t)
	f := &fakeNSE{}
	o.Notis = f
	at := time.Date(2019, 12, 20, 10, 0, 0, 0, ist)
	o.Now = func() time.Time { return at }
	step := func() { at = at.Add(16 * time.Second) }
	if _, _, err := o.NotisFetch(ctx, T, "priya.nair", "FO", ""); err == nil {
		t.Fatal("fetched without a connection")
	}
	if _, err := o.SaveConnection(ctx, T, "admin", connNotisFO, map[string]string{"member": "8081", "env": "UAT", "fetch": "On"}, map[string]string{"api_key": "fo-key", "secret": "s"}); err != nil {
		t.Fatal(err)
	}
	// First call logs in and uses the 15-second slot.
	if _, n, err := o.NotisFetch(ctx, T, "priya.nair", "FO", ""); err != nil || n != 0 || f.logins != 1 {
		t.Fatalf("login: %d %v", n, err)
	}
	if _, _, err := o.NotisFetch(ctx, T, "priya.nair", "FO", ""); err == nil || !strings.Contains(err.Error(), "15 seconds") {
		t.Fatalf("no wait: %v", err)
	}
	step()
	// NSE's F&O sample: a futures trade, a client modification of another
	// (activity 105), a third trade; then the modification of the first.
	f.replies = []string{
		"3,20191220,,,827587,3^827155,1,47964115,82609719241959,49102,800,26705,2,1300000001946000,9,36956,1,SG18,SBISG0000081,,2,6001,1260524280,1,,400051001004000,P,08081,IBULHSGFIN,,FUTSTK,1261785600,-1,XX,,,,,,,,^" +
			"827201,1,89638308,82609719248043,49440,2200,4440,1,1500000001376851,9,8255,1,ERROR,CITI00003543,,105,5445,1260523571,1,,400051001004012,P,08081,YESBANK,,FUTSTK,1261837800,-1,XX,,,,,,,,^" +
			"827587,1,37527876,82609719325776,47859,250,403185,1,1200000001362256,9,36411,1,KSAM12,CITI00003543,,2,6001,1260524281,1,,400051001002012,P,08081,BAJFINANCE,,FUTSTK,1264809600,-1,XX,,,,,,,,^",
		"3,20191220,,,827600,1^827600,1,47964115,82609719241959,49102,800,26705,2,1300000001946000,9,36956,1,SG19,SBISG0000081,,105,5445,1260524280,1,,400051001004000,P,08081,IBULHSGFIN,,FUTSTK,1261785600,-1,XX,,,,,,,,",
		"3,20191220,,,827600,0",
	}
	st, n, err := o.NotisFetch(ctx, T, "priya.nair", "FO", "")
	if err != nil || n != 3 || st.TradeSeq != 827587 || st.Date != "2019-12-20" || st.Trades != 3 {
		t.Fatalf("first page %+v %d %v", st, n, err)
	}
	d := f.bodies[0]["data"].(map[string]any)
	if d["msgId"] != "0808120191220"+"0000001" || d["tradesInquiry"] != "0,ALL,," || d["dataFormat"] != "CSV:CSV" {
		t.Fatalf("request %v", d)
	}
	step()
	if st, n, _ = o.NotisFetch(ctx, T, SystemActor, "FO", ""); n != 1 || st.TradeSeq != 827600 || st.Trades != 3 {
		t.Fatalf("second page %+v %d", st, n)
	}
	if d := f.bodies[1]["data"].(map[string]any); d["tradesInquiry"] != "827587,ALL,," || d["msgId"] != "08081201912200000002" {
		t.Fatalf("cursor %v", d)
	}
	ts, _ := o.NotisTrades(ctx, T, "2019-12-20")
	if len(ts) != 3 {
		t.Fatalf("%d trades", len(ts))
	}
	var first = ts[0]
	for _, x := range ts {
		if x.TradeNo == "47964115" {
			first = x
		}
	}
	if first.UCC != "SG19" || first.Side != "S" || first.Qty != 800 || first.Price.String() != "267.05" || first.InstrumentType != "FUT" || first.Expiry != "2019-12-26" || first.TradeTime != "09:38:00" {
		t.Fatalf("modified trade %+v", first)
	}
	step()
	if _, n, _ := o.NotisFetch(ctx, T, SystemActor, "FO", ""); n != 0 {
		t.Fatalf("empty page gave %d", n)
	}
	// Live risk uses them while there is no end-of-day file.
	if v, err := o.LiveRisk(ctx, T, "2019-12-20", ""); err != nil || !strings.Contains(v.Source, "NSE trade API") {
		t.Fatalf("live %v %v", v.Source, err)
	}
	// An expired token means logging in again next time.
	step()
	f.code = 572
	if _, _, err := o.NotisFetch(ctx, T, SystemActor, "FO", ""); err == nil {
		t.Fatal("572 not reported")
	}
	f.code = 0
	step()
	o.NotisFetch(ctx, T, SystemActor, "FO", "")
	if f.logins != 2 {
		t.Fatalf("logins %d", f.logins)
	}
	// Closed from 05:00 to 06:30.
	at = time.Date(2019, 12, 21, 5, 30, 0, 0, ist)
	if _, _, err := o.NotisFetch(ctx, T, SystemActor, "FO", ""); err == nil || !strings.Contains(err.Error(), "05:00") {
		t.Fatalf("closed window: %v", err)
	}
}

// NSE's cash-market sample row, and the time conversions.
func TestNotisCMRowAndTimes(t *testing.T) {
	row := strings.Split("1565203,N,2019122075170400,82660622204928,13538,9,78250,1,1300000000679330,1,5090,2,90159,90159,1,2,6001,1261300998,1,0,N,,0,122015001001000,1025,N,TECHM,EQ,TECH MAHINDRA LIMITED,EQ,90159,,,,,,,,", ",")
	tr, err := notisTrade("CM", row)
	if err != nil || tr.Symbol != "TECHM" || tr.Price.String() != "782.5" && tr.Price.String() != "782.50" || tr.Side != "B" || !tr.Pro || tr.Time != "09:23:18" || tr.Settlement != "N" {
		t.Fatalf("%+v %v", tr, err)
	}
	// A security name with a comma in it.
	withComma := append(append(append([]string{}, row[:28]...), "TECH", " MAHINDRA LTD"), row[29:]...)
	if tr, err := notisTrade("CM", withComma); err != nil || tr.Instrument != "EQ" {
		t.Fatalf("comma name %+v %v", tr, err)
	}
	if got := from1980(1261785600).Format("2006-01-02 15:04"); got != "2019-12-26 00:00" {
		t.Fatalf("1980 time %s", got)
	}
	n := notisNonce(time.Date(2017, 1, 20, 16, 12, 21, 591e6, ist))
	if b, _ := decodeB64(n); !strings.HasPrefix(b, "20012017161221591:") || len(b) != 24 {
		t.Fatalf("nonce %q", b)
	}
}

func decodeB64(s string) (string, error) {
	b, err := base64Std(s)
	return string(b), err
}

func base64Std(s string) ([]byte, error) { return base64.StdEncoding.DecodeString(s) }
