package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"ledgerline/internal/app"
	"ledgerline/internal/charges"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ops"
	"ledgerline/internal/vault"
)

const bxMail = "From: Ravi Kumar <ravi@example.com>\r\n" +
	"Subject: Bank proof for BH1004\r\n" +
	"Message-ID: <%ID%@example.com>\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/alternative; boundary=\"b1\"\r\n" +
	"\r\n" +
	"--b1\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"Cancelled cheque attached.\r\n" +
	"--b1--\r\n"

func TestBoardExtrasAPI(t *testing.T) {
	reg, _ := ingest.LoadDir("../../config/adapters")
	f, _ := os.Open("../../config/charges/sample-schedule.json")
	defer f.Close()
	sched, _ := charges.LoadSchedule(f)
	a := app.New(vault.Protect(app.MemoryStores(), vault.New(vault.Ephemeral())), reg, sched)
	o := ops.New(a)
	ctx := context.Background()
	if err := o.SeedDev(ctx, "demo"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(a, o, map[string]string{key: "demo"}, slog.New(slog.NewTextHandler(io.Discard, nil)), false))
	defer srv.Close()
	call := func(method, path, who, ctype string, body []byte, out any) int {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, bytes.NewReader(body))
		if who != "" {
			req.Header.Set("Authorization", "Bearer "+key)
			req.Header.Set("X-Actor", who)
		}
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		if out != nil {
			if err := json.Unmarshal(b, out); err != nil {
				t.Fatalf("%s %s: %v\n%s", method, path, err, b)
			}
		}
		return res.StatusCode
	}

	// The template gallery and a board made from one.
	var g []ops.BoardTemplateInfo
	if code := call("GET", "/v1/board-templates", "priya.nair", "", nil, &g); code != 200 || len(g) < 7 {
		t.Fatalf("gallery %d %d", code, len(g))
	}
	var b ops.Board
	if code := call("POST", "/v1/board-templates/bt-onboarding/use", "priya.nair", "application/json", []byte(`{"name":"Onboarding: Mumbai","visibility":"firm","cards":true}`), &b); code != 200 || b.Name != "Onboarding: Mumbai" {
		t.Fatalf("use %d %+v", code, b)
	}

	// Email-to-board: the board's admin switches it on.
	var em struct {
		On   bool   `json:"on"`
		Path string `json:"path"`
		List string `json:"list"`
	}
	if code := call("POST", "/v1/boards/"+b.ID+"/email", "farhan.khan", "application/json", []byte(`{"on":true}`), nil); code != 403 {
		t.Fatalf("an observer switched email on: %d", code)
	}
	if code := call("POST", "/v1/boards/"+b.ID+"/email", "priya.nair", "application/json", []byte(`{"on":true,"list":"`+b.Lists[0].ID+`"}`), &em); code != 200 || !em.On || !strings.HasPrefix(em.Path, "/inbound/boards/demo/") {
		t.Fatalf("email on %d %+v", code, em)
	}
	mail := func(id string) []byte { return []byte(strings.Replace(bxMail, "%ID%", id, 1)) }
	var res struct {
		Card      string `json:"card"`
		No        int64  `json:"no"`
		Duplicate bool   `json:"duplicate"`
	}
	if code := call("POST", em.Path, "", "message/rfc822", mail("m1"), &res); code != 200 || res.Card == "" || res.Duplicate {
		t.Fatalf("inbound %d %+v", code, res)
	}
	c, _, err := o.GetCard(ctx, "demo", "priya.nair", b.ID, res.Card)
	if err != nil || c.Title != "Bank proof for BH1004" || !strings.HasPrefix(c.Description, "From: Ravi Kumar <ravi@example.com>\n\nCancelled cheque attached.") {
		t.Fatalf("card %+v %v", c, err)
	}
	if code := call("POST", em.Path, "", "message/rfc822", mail("m1"), &res); code != 200 || !res.Duplicate {
		t.Fatalf("redelivery %d %+v", code, res)
	}
	// SendGrid's raw form and Postmark's JSON.
	var form bytes.Buffer
	mw := multipart.NewWriter(&form)
	_ = mw.WriteField("to", "board@example.com")
	_ = mw.WriteField("email", string(mail("m2")))
	mw.Close()
	if code := call("POST", em.Path, "", mw.FormDataContentType(), form.Bytes(), &res); code != 200 || res.Duplicate {
		t.Fatalf("sendgrid %d %+v", code, res)
	}
	pj, _ := json.Marshal(map[string]string{"From": "ravi@example.com", "RawEmail": string(mail("m3"))})
	if code := call("POST", em.Path, "", "application/json", pj, &res); code != 200 || res.Duplicate {
		t.Fatalf("postmark %d %+v", code, res)
	}
	// A wrong address, an email too large, too many emails.
	if code := call("POST", "/inbound/boards/demo/"+strings.Repeat("ab", 32), "", "message/rfc822", mail("m4"), nil); code != 404 {
		t.Fatalf("unknown address %d", code)
	}
	if code := call("POST", em.Path, "", "message/rfc822", append(mail("m5"), bytes.Repeat([]byte("x"), ops.BoardMaxMail)...), nil); code != 413 {
		t.Fatalf("too large %d", code)
	}
	setMax := func(n int) int {
		bxMailLimit.mu.Lock()
		defer bxMailLimit.mu.Unlock()
		old := bxMailLimit.max
		bxMailLimit.max = n
		return old
	}
	old := setMax(5) // five emails so far
	defer setMax(old)
	if code := call("POST", em.Path, "", "message/rfc822", mail("m6"), nil); code != 429 {
		t.Fatalf("rate limit %d", code)
	}
	// Closed boards take no mail.
	setMax(100)
	if _, err := o.SetBoardClosed(ctx, "demo", "priya.nair", b.ID, true); err != nil {
		t.Fatal(err)
	}
	if code := call("POST", em.Path, "", "message/rfc822", mail("m7"), nil); code != 409 {
		t.Fatalf("closed board %d", code)
	}
}
