package api

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/ops"
)

func TestFATCARoutes(t *testing.T) {
	now := time.Date(2026, 11, 17, 11, 0, 0, 0, time.UTC)
	srv, o := ipoServer(t, now)
	if c := ipoCall(t, srv, "GET", "/v1/fatca", "meera.iyer", nil, nil); c != 403 {
		t.Fatalf("branch staff saw the FATCA/CRS tab: %d", c)
	}
	var codes ops.FATCACodes
	if c := ipoCall(t, srv, "GET", "/v1/fatca/codes", "priya.nair", nil, &codes); c != 200 || len(codes.Countries) != 249 || codes.Countries[0][0] != "IN" {
		t.Fatalf("codes %d %d", c, len(codes.Countries))
	}
	nc := map[string]any{"name": "Tara Joshi", "type": "Individual", "pan": "ABCPJ7001J", "mobile": "9876500701", "email": "tara@example.test", "city": "Pune",
		"segments": []string{"CM"}, "income": "5-10 L", "ras": "Quarterly", "bank": map[string]string{"bank": "Sample Bank", "ifsc": "SBIN0001234", "account": "123456789701"},
		"risk_disclosure": true}
	if c := ipoCall(t, srv, "POST", "/v1/clients", "priya.nair", nc, nil); c < 300 {
		t.Fatal("a new client without a self-certification")
	}
	nc["fatca"] = map[string]string{"other_than_india": "N", "place_of_birth": "Nashik", "country_of_birth": "IN", "nationality": "IN", "us_person": "N"}
	var cl ops.Client
	if c := ipoCall(t, srv, "POST", "/v1/clients", "priya.nair", nc, &cl); c != 201 || cl.FATCA == nil || cl.FATCA.DeclaredOn != "2026-11-17" {
		t.Fatalf("create %d %+v", c, cl.FATCA)
	}
	var v ops.FATCAView
	if c := ipoCall(t, srv, "GET", "/v1/fatca?date=2026-11-17", "priya.nair", nil, &v); c != 200 || len(v.Rows) == 0 || v.Layout == "" {
		t.Fatalf("tab %d %d", c, len(v.Rows))
	}
	// The KRA upload file is kept as a batch to mark uploaded.
	req, _ := http.NewRequest("GET", srv.URL+"/v1/compliance/kra-upload?since=2026-11-17", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("X-Actor", "priya.nair")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	batch := res.Header.Get("X-Batch")
	if res.StatusCode != 200 || batch == "" || !strings.Contains(string(body), "Tax Residency other than India?") || !strings.Contains(res.Header.Get("Content-Disposition"), batch) {
		t.Fatalf("KRA file %d %q %s", res.StatusCode, batch, body)
	}
	if c := ipoCall(t, srv, "POST", "/v1/compliance/kra-batches/"+batch+"/uploaded", "priya.nair", map[string]string{"date": "2026-11-17", "ref": "ACK-77"}, nil); c != 200 {
		t.Fatalf("mark uploaded %d", c)
	}
	var cp []byte
	if c := ipoCall(t, srv, "GET", "/v1/compliance/kra-batches/"+batch+"/controlling-persons", "priya.nair", nil, &cp); c != 200 || !strings.HasPrefix(string(cp), "PAN,UCC,Name,Serial,Controlling Person Name") {
		t.Fatalf("controlling persons file %d %s", c, cp)
	}
	if c := ipoCall(t, srv, "GET", "/v1/compliance/fatca-legacy", "priya.nair", nil, nil); c < 300 {
		t.Fatal("a legacy file with no certification waiting")
	}
	if c := ipoCall(t, srv, "POST", "/v1/fatca/"+cl.UCC+"/resolve", "priya.nair", map[string]string{"code": "x", "note": "checked the passport"}, nil); c != 403 {
		t.Fatalf("a maker resolved a warning: %d", c)
	}
	if c := ipoCall(t, srv, "POST", "/v1/bank/NOPE/move-fee", "priya.nair", map[string]string{"ref": "NEFT"}, nil); c != 403 {
		t.Fatalf("a maker moved a fee: %d", c)
	}
	var upi ops.UPICollectView
	if c := ipoCall(t, srv, "GET", "/v1/settings/upi-collect", "vikram.shah", nil, &upi); c != 200 || len(upi.Categories) != 10 {
		t.Fatalf("UPI categories %d %+v", c, upi.Categories)
	}
	_ = o
}
