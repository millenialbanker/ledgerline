package api

import (
	"io"
	"net/http"
	neturl "net/url"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/ops"
)

// TestBankAuthRoutes: a bank change staff raise sends the client a link at
// this server's address; opening it does not answer, the button does, and
// the checker then approves.
func TestBankAuthRoutes(t *testing.T) {
	t.Setenv("LEDGERLINE_PUBLIC_URL", "")
	now := time.Date(2026, 11, 20, 11, 0, 0, 0, time.UTC)
	srv, o := ipoServer(t, now)
	ctx := t.Context()
	cs, _ := o.Clients(ctx, "demo")
	var c ops.Client
	for _, x := range cs {
		if x.Status == "Active" && x.Mobile != "" && x.Email != "" && !x.DPOnly && len(x.Incomplete) == 0 {
			c = x
			break
		}
	}
	if c.UCC == "" {
		t.Fatal("no active client in the sample day")
	}
	proof, err := o.AddClientDoc(ctx, "demo", "priya.nair", c.UCC, "Bank proof", "cheque.png", []byte("\x89PNG\r\n\x1a\n0000000000000000"))
	if err != nil {
		t.Fatal(err)
	}
	var r ops.ChangeRequest
	body := map[string]any{"ucc": c.UCC, "kind": "Bank account", "doc": proof.ID, "values": map[string]string{"action": "add", "bank": "Made Up Bank", "ifsc": "MADE0000001", "account": "777700001111"}}
	if code := ipoCall(t, srv, "POST", "/v1/changes", "priya.nair", body, &r); code != 201 || r.Status != ops.ChangeWaitClient {
		t.Fatalf("raise %d %+v", code, r)
	}
	link := r.DevAuth["e-mail_link"]
	if !strings.HasPrefix(link, srv.URL+"/bank-confirm/demo/") {
		t.Fatalf("link %q", link)
	}
	if code := ipoCall(t, srv, "POST", "/v1/changes/"+r.ID+"/decide", "farhan.khan", map[string]any{"approve": true}, nil); code < 400 {
		t.Fatalf("approved before the client confirmed: %d", code)
	}
	res, err := http.Get(link)
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(page), "Confirm a change to your bank accounts") || !strings.Contains(string(page), "XXXXXXXX1111") || strings.Contains(string(page), "777700001111") {
		t.Fatalf("page %d %s", res.StatusCode, page)
	}
	res, err = http.PostForm(link, neturl.Values{"answer": {"confirm"}})
	if err != nil {
		t.Fatal(err)
	}
	page, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(page), "You confirmed request "+r.ID) {
		t.Fatalf("answer page %s", page)
	}
	var list struct {
		Requests []ops.ChangeRequest `json:"requests"`
	}
	if code := ipoCall(t, srv, "GET", "/v1/changes?ucc="+c.UCC, "priya.nair", nil, &list); code != 200 || len(list.Requests) != 1 || list.Requests[0].Auth.Via != "link" || list.Requests[0].Auth.From == "" || list.Requests[0].Status != "Pending" {
		t.Fatalf("change log %d %+v", code, list.Requests)
	}
	// A client login answers only its own account's; staff cannot answer.
	if code := ipoCall(t, srv, "POST", "/v1/changes/"+r.ID+"/answer", "priya.nair", map[string]any{"confirm": true, "code": "123456"}, nil); code < 400 {
		t.Fatalf("staff answered for the client: %d", code)
	}
	if code := ipoCall(t, srv, "POST", "/v1/changes/"+r.ID+"/decide", "farhan.khan", map[string]any{"approve": true}, &r); code != 200 || r.Status != "Approved" {
		t.Fatalf("approve %d %+v", code, r)
	}
}
