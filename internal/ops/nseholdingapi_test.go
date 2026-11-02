package ops

import (
	"bytes"
	"compress/gzip"
	"crypto/aes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"ledgerline/internal/docs"
)

// fakeNSEHolding is NSE's inspection API as NSE/INSP/55380 describes it.
type fakeNSEHolding struct {
	mu      sync.Mutex
	key     []byte
	logins  int
	uploads []map[string]any
	names   []string
	reply   string // canned upload reply; "" accepts
	logouts int
}

func ecbDecrypt(t *testing.T, key []byte, b64 string) string {
	b, _ := base64.StdEncoding.DecodeString(b64)
	c, err := aes.NewCipher(key)
	if err != nil || len(b)%16 != 0 {
		t.Errorf("password not AES-ECB: %v", err)
		return ""
	}
	out := make([]byte, len(b))
	for i := 0; i < len(b); i += 16 {
		c.Decrypt(out[i:i+16], b[i:i+16])
	}
	n := int(out[len(out)-1])
	return string(out[:len(out)-n])
}

func (f *fakeNSEHolding) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/inspectionapi/login":
			var b map[string]string
			_ = json.NewDecoder(r.Body).Decode(&b)
			if b["memberCode"] != "90311" || b["loginId"] != "ApiUser90311" || ecbDecrypt(t, f.key, b["password"]) != "Hs@Pass12345" {
				w.Write([]byte(`{"code":["701"],"status":"INVALID_MEMBER_CODE_OR_LOGIN_ID_OR_PASSWORD"}`))
				return
			}
			f.logins++
			w.Write([]byte(`{"memberCode":"90311","code":["601"],"loginId":"ApiUser90311","token":"tok123","status":"success"}`))
		case "/inspectionapi/tradingHoldingUpload/1.0":
			if r.Header.Get("Authorization") != "Bearer tok123" {
				w.WriteHeader(401)
				w.Write([]byte(`{"hdrCode":["803"],"hdrStatus":"token wrong"}`))
				return
			}
			file, hd, err := r.FormFile("File")
			if err != nil {
				w.Write([]byte(`{"hdrCode":["527"],"hdrStatus":"PLEASE_UPLOAD_MULTIPART_FILE"}`))
				return
			}
			z, err := gzip.NewReader(file)
			if err != nil {
				w.Write([]byte(`{"hdrCode":["524"]}`))
				return
			}
			raw, _ := io.ReadAll(z)
			var m map[string]any
			if json.Unmarshal(raw, &m) != nil {
				w.Write([]byte(`{"hdrCode":["515"]}`))
				return
			}
			f.uploads = append(f.uploads, m)
			f.names = append(f.names, hd.Filename)
			if f.reply != "" {
				w.Write([]byte(f.reply))
				return
			}
			w.Write([]byte(`{"hdrCode":["601"],"ackId":"ACK1","memberPan":"AAACB1234C","hdrStatus":"success","holdingData":[]}`))
		case "/inspectionapi/logout":
			f.logouts++
			w.Write([]byte(`{"code":["601"],"status":"success"}`))
		default:
			w.WriteHeader(404)
		}
	})
}

func TestNSEHoldingAPI(t *testing.T) {
	o, ctx := newOps(t)
	const W = "2026-10-14"
	now := time.Date(2026, 10, 17, 9, 0, 0, 0, ist) // Saturday morning: Friday's data
	o.Now = func() time.Time { return now }
	key := bytes.Repeat([]byte{7}, 32)
	f := &fakeNSEHolding{key: key}
	srv := httptest.NewTLSServer(f.handler(t))
	t.Cleanup(srv.Close)
	old := nseHoldingHTTP
	nseHoldingHTTP = srv.Client()
	t.Cleanup(func() { nseHoldingHTTP = old })

	if _, err := o.NSEHoldingSubmit(ctx, T, "priya.nair", W); err == nil || !strings.Contains(err.Error(), "Connections") {
		t.Fatalf("submitted without a connection: %v", err)
	}
	for _, c := range []Client{nseUCITestClient("HS001", "ABCPV1234K"), nseUCITestClient("HS002", "ABCPV1235K")} {
		nseUCIAddClient(t, o, c, false)
	}
	_ = docs.Upsert(ctx, o.Docs, T, cHolding, holdingID("HS001", "INE000A01011"), Holding{UCC: "HS001", ISIN: "INE000A01011", Symbol: "AAA", Qty: 100, Pledged: 40}, "test")
	_ = docs.Upsert(ctx, o.Docs, T, cHolding, holdingID("HS002", "INE000B01012"), Holding{UCC: "HS002", ISIN: "INE000B01012", Symbol: "BBB", Qty: 5}, "test")
	if _, err := o.SaveConnection(ctx, T, "admin", connNSEHolding, map[string]string{"member": "90311", "send": "On", "url": srv.URL + "/inspectionapi"},
		map[string]string{"password": "Hs@Pass12345", "secret_key": base64.StdEncoding.EncodeToString(key)}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.NSEHoldingSubmit(ctx, T, "anita.rao", W); err == nil {
		t.Fatal("risk manager submitted")
	}
	sub, err := o.NSEHoldingSubmit(ctx, T, "priya.nair", W)
	if err != nil || sub.Status != "Accepted" || sub.AckID != "ACK1" || sub.Records != 2 || sub.Seq != 1 {
		t.Fatalf("%+v %v", sub, err)
	}
	if f.names[0] != "AAACB1234C_HS_14102026_01.gz" || f.uploads[0]["memberPan"] != "AAACB1234C" || f.logouts != 1 {
		t.Fatalf("upload %v %v", f.names, f.uploads[0]["memberPan"])
	}
	if strings.Contains(sub.File, "AAACB1234C") {
		t.Fatalf("member PAN kept in full: %s", sub.File)
	}
	rows := f.uploads[0]["holdingData"].([]any)
	if r := rows[0].(map[string]any); r["pan"] != "ABCPV1234K" || r["totalPldgQty"] != "40.000" || r["freeBalQty"] != "60.000" || r["dmat"] != "1208160000012345" {
		t.Fatalf("row %v", r)
	}

	// A record NSE rejects: the submission is partly accepted and a work item says why.
	f.reply = `{"hdrCode":["603"],"ackId":"ACK2","hdrStatus":"PARTIAL_SUBMISSION","holdingData":[{"recId":"2","code":["905","917"],"status":"error"}]}`
	sub, err = o.NSEHoldingSubmit(ctx, T, "priya.nair", W)
	if err != nil || sub.Status != "Partly accepted" || sub.Seq != 2 || len(sub.Errors) != 1 || sub.Errors[0].UCC != "HS002" || !strings.Contains(sub.Errors[0].Plain, "client PAN is not right (905)") {
		t.Fatalf("%+v %v", sub, err)
	}
	ws, _ := o.WorkItems(ctx, T, "Open")
	found := false
	for _, w := range ws {
		found = found || (w.Process == "holding-api" && strings.Contains(w.Detail, "HS002 INE000B01012: the client PAN is not right (905)"))
	}
	if !found {
		t.Fatal("no work item for the rejected record")
	}

	// An earlier day missing (805): rejected, with NSE's reason.
	f.reply = `{"hdrCode":["805"],"hdrStatus":"Kindly first submit data for 14-Oct-2026"}`
	sub, _ = o.NSEHoldingSubmit(ctx, T, "priya.nair", "2026-10-15")
	if sub.Status != "Rejected" || !strings.Contains(sub.Reason, "earlier day") || !strings.Contains(sub.Reason, "14-Oct-2026") {
		t.Fatalf("%+v", sub)
	}

	// A client without a PAN is caught before anything is sent.
	bad := nseUCITestClient("HS003", "")
	nseUCIAddClient(t, o, bad, false)
	_ = docs.Upsert(ctx, o.Docs, T, cHolding, holdingID("HS003", "INE000C01013"), Holding{UCC: "HS003", ISIN: "INE000C01013", Symbol: "CCC", Qty: 1}, "test")
	n := len(f.uploads)
	sub, _ = o.NSEHoldingSubmit(ctx, T, "priya.nair", W)
	if sub.Status != "Not sent" || len(f.uploads) != n || len(sub.Errors) != 1 || !strings.Contains(sub.Errors[0].Plain, "HS003") {
		t.Fatalf("%+v", sub)
	}
	_ = docs.Upsert(ctx, o.Docs, T, cHolding, holdingID("HS003", "INE000C01013"), Holding{UCC: "HS003", ISIN: "INE000C01013", Symbol: "CCC"}, "test") // sold

	// The tick: Friday's data waits for Friday's CDSL holding file, then goes once.
	f.reply = ""
	o.nseHoldingTick(ctx, T, now)
	if v, _ := o.NSEHolding(ctx, T, "priya.nair", "2026-10-16"); len(v.Submissions) != 0 { // nothing: the file is not loaded
		t.Fatalf("tick sent before the holding file: %d", len(v.Submissions))
	}
	o.noteDailyFile(ctx, T, "priya.nair", "cdsl-holding", "SOH_16102026.txt", "2026-10-16")
	o.nseHoldingTick(ctx, T, now) // within 30 minutes of the last tick: nothing
	now = now.Add(31 * time.Minute)
	o.nseHoldingTick(ctx, T, now)
	v, _ := o.NSEHolding(ctx, T, "priya.nair", "")
	if v.Submissions[0].By != SystemActor || v.Submissions[0].Status != "Accepted" || v.Submissions[0].Date != "2026-10-16" || v.State.LastOK != "2026-10-16" {
		t.Fatalf("tick %+v %+v", v.Submissions[0], v.State)
	}
	now = now.Add(time.Hour)
	k := len(f.uploads)
	o.nseHoldingTick(ctx, T, now)
	if len(f.uploads) != k {
		t.Fatal("an accepted day was sent again")
	}
}
