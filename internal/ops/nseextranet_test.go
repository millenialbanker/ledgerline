package ops

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestExtranetPasswordVector(t *testing.T) {
	// NSE's FAQ (question 37): this key and password give this ciphertext.
	got, err := extranetPassword("w0lWvhdldP5BOQohbJqp9pNHUMiIU+Cc/4gSs06mRIU=", "abcd@1234")
	if err != nil {
		t.Fatal(err)
	}
	if got != "LvRHkSW+8OIMyk51T87KDQ==" {
		t.Fatalf("encrypted %q", got)
	}
	if _, err := extranetPassword("short", "x"); err == nil {
		t.Fatal("bad key accepted")
	}
}

func TestExtranetFetch(t *testing.T) {
	o, ctx := setup(t)
	cs, _ := o.Clients(ctx, T)
	day := time.Date(2026, 10, 29, 19, 0, 0, 0, ist)
	o.Now = func() time.Time { return day }
	ymd := day.Format("20060102")
	// A day's NSE trade file in the layout Ledgerline reads.
	h := xheader(t, "ncl-trade")
	row := xline(h, map[string]string{"TradDt": "2026-10-29", "BizDt": "2026-10-29", "Sgmt": "CM", "Src": "NSE", "Xchg": "NSE", "FinInstrmTp": "STK", "TckrSymb": "XTEST", "SctySrs": "EQ",
		"ClntId": cs[0].UCC, "BuySellInd": "B", "TradQty": "10", "Pric": "101.50", "UnqTradIdr": "770001", "OrdrRef": "1770001", "TradDtTm": "2026-10-29T10:05:00", "CtclId": "111111111111111", "SttlmTp": "N"})
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	zw.Write([]byte(strings.Join(h, ",") + "\r\n" + row + "\r\n"))
	zw.Close()
	tradeName := "Trade_NSE_CM_0_TM_12345_" + ymd + "_F_0000.csv.gz"

	pw, _ := extranetPassword("XBaNb0xmK2TNRIfcHA3F306Oi14HWAeYmtUd0qRheTc=", "Nsejan@123")
	logins, downloads := 0, 0
	var paths []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path+"?"+r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/extranet-api/login/2.0" {
			var b map[string]string
			json.NewDecoder(r.Body).Decode(&b)
			if b["memberCode"] != "12345" || b["loginId"] != "llapi" || b["password"] != pw {
				w.WriteHeader(400)
				w.Write([]byte(`{"token":null,"code":["701"],"status":"error"}`))
				return
			}
			logins++
			w.Write([]byte(`{"token":"tok-1","memberCode":"12345","loginId":"llapi","code":["601"],"status":"success"}`))
			return
		}
		if r.Header.Get("Authorization") != "Bearer tok-1" {
			w.WriteHeader(401)
			w.Write([]byte(`{"code":["803"],"status":"error"}`))
			return
		}
		q := r.URL.Query()
		switch r.URL.Path {
		case "/extranet-api/member/content/2.0":
			switch q.Get("folderPath") {
			case "/Onlinebackup":
				w.Write([]byte(`{"code":["601"],"status":"success","data":[
					{"name":"` + tradeName + `","type":"File","lastUpdated":"29/10/2026 18:40:11","size":812},
					{"name":"CM_ORD_LOG_29102026_12345.CSV.gz","type":"File","lastUpdated":"29/10/2026 18:41:00","size":90},
					{"name":"Trade_NSE_CM_0_TM_12345_20261028_F_0000.csv.gz","type":"File","lastUpdated":"28/10/2026 18:40:11","size":700},
					{"name":"Archive","type":"Folder"}]}`))
			case "/Onlinebackup/Archive":
				w.Write([]byte(`{"code":["601"],"status":"success","data":[]}`))
			default:
				w.WriteHeader(400)
				w.Write([]byte(`{"code":["712"],"status":"error"}`))
			}
		case "/extranet-api/member/file/download/2.0":
			downloads++
			if q.Get("filename") != tradeName || q.Get("segment") != "CM" {
				w.WriteHeader(400)
				w.Write([]byte(`{"code":["715"],"status":"error"}`))
				return
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Write(gz.Bytes())
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	old := extranetHTTP
	extranetHTTP = srv.Client()
	defer func() { extranetHTTP = old }()

	if _, err := o.SaveConnection(ctx, T, "admin", connNSEExtranet, map[string]string{"member": "12345", "login": "llapi", "network": "Internet", "segments": "CM",
		"folders": "member:/Onlinebackup, member:/Reports", "fetch": "On", "url": srv.URL + "/extranet-api"},
		map[string]string{"password": "Nsejan@123", "api_key": "XBaNb0xmK2TNRIfcHA3F306Oi14HWAeYmtUd0qRheTc="}); err != nil {
		t.Fatal(err)
	}
	run, err := o.ExtranetFetch(ctx, T, "priya.nair", "")
	if err != nil {
		t.Fatal(err)
	}
	// Today's trade file is fetched and loaded; yesterday's is left; the
	// order log is not a file Ledgerline reads; /Reports is missing.
	if len(run.Fetched) != 1 || run.Fetched[0].Name != tradeName || run.Fetched[0].Format != "ncl-trade" || !strings.Contains(run.Fetched[0].Loaded, "rade") {
		t.Fatalf("run %+v", run)
	}
	if run.Unknown != 1 || run.Looked != 3 || len(run.Problems) != 1 || !strings.Contains(run.Problems[0], "folder not found") {
		t.Fatalf("run %+v", run)
	}
	trades, _ := o.App.ExchangeTrades(ctx, T, "2026-10-29")
	found := false
	for _, tr := range trades {
		if tr.TradeNo == "770001" {
			found = true
		}
	}
	if !found {
		t.Fatal("the downloaded trade was not loaded")
	}
	// The next run keeps the token and does not fetch the same file again.
	day = day.Add(20 * time.Minute)
	run, err = o.ExtranetFetch(ctx, T, SystemActor, "")
	if err != nil || len(run.Fetched) != 0 || run.Skipped != 1 || logins != 1 || downloads != 1 {
		t.Fatalf("second run %+v %v (logins %d, downloads %d)", run, err, logins, downloads)
	}
	v := o.ExtranetStatus(ctx, T)
	if v.State.Token != "" || len(v.Files) != 1 {
		t.Fatalf("status %+v", v)
	}
	// Closed for maintenance from 05:00 to 06:00.
	day = time.Date(2026, 10, 30, 5, 30, 0, 0, ist)
	if _, err := o.ExtranetFetch(ctx, T, SystemActor, ""); err == nil {
		t.Fatal("fetched during maintenance")
	}
	_ = paths
}

func TestExtranetListShapes(t *testing.T) {
	for _, b := range []string{
		`{"code":["601"],"data":[{"fileName":"a.csv","isFolder":false,"lastModified":"29/10/2026 10:00:00","fileSize":"12"},{"fileName":"Reports","isFolder":true}]}`,
		`[{"name":"a.csv","type":"FILE"},{"name":"Reports","type":"DIRECTORY"}]`,
		`{"status":"success","data":{"folderList":[{"name":"Reports","type":"Folder"}],"fileList":[{"name":"a.csv","type":"File"}]}}`,
	} {
		es := extranetList([]byte(b))
		files, folders := 0, 0
		for _, e := range es {
			if e.Folder {
				folders++
			} else {
				files++
			}
		}
		if files != 1 || folders != 1 {
			t.Errorf("%s: %+v", b, es)
		}
	}
}
