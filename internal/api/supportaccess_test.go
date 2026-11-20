package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"ledgerline/internal/app"
	"ledgerline/internal/charges"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ops"
	"ledgerline/internal/vault"
)

// saBrowser is one browser (cookie jar) against the test server.
type saBrowser struct {
	t   *testing.T
	url string
	c   *http.Client
}

func (b *saBrowser) do(method, path string, body any) (int, map[string]any, string) {
	b.t.Helper()
	var rd io.Reader
	if body != nil {
		j, _ := json.Marshal(body)
		rd = bytes.NewReader(j)
	}
	req, _ := http.NewRequest(method, b.url+path, rd)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Requested-With", "ledgerline")
	res, err := b.c.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var out map[string]any
	json.Unmarshal(raw, &out)
	return res.StatusCode, out, string(raw)
}

func newBrowser(t *testing.T, url string) *saBrowser {
	jar, _ := cookiejar.New(nil)
	return &saBrowser{t: t, url: url, c: &http.Client{Jar: jar}}
}

// saServer is a firm (the sample users) and a Ledgerline support operator
// signed in at the support desk, on a clock the test moves.
func saServer(t *testing.T) (*ops.Ops, *saBrowser, *saBrowser, func(time.Duration)) {
	t.Helper()
	ops.PasswordIterations = 1000
	reg, _ := ingest.LoadDir("../../config/adapters")
	f, _ := os.Open("../../config/charges/sample-schedule.json")
	defer f.Close()
	sched, _ := charges.LoadSchedule(f)
	a := app.New(vault.Protect(app.MemoryStores(), vault.New(vault.Ephemeral())), reg, sched)
	o := ops.New(a)
	var mu sync.Mutex
	now := time.Now()
	o.Now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	step := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }
	ctx := t.Context()
	if err := o.SeedDev(ctx, "demo"); err != nil {
		t.Fatal(err)
	}
	set, _ := o.Settings(ctx, "demo")
	set.FirmPAN = "AAACB1234C"
	if err := o.SaveSettings(ctx, "demo", "admin", set); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(a, o, map[string]string{key: "demo"}, slog.New(slog.NewTextHandler(io.Discard, nil)), true))
	t.Cleanup(srv.Close)

	// The firm's Admin.
	adm := newBrowser(t, srv.URL)
	if st, out, _ := adm.do("POST", "/v1/auth/login", map[string]string{"user": "admin", "password": ops.DevPassword}); st != 200 {
		t.Fatalf("admin login %d %v", st, out)
	}
	// The operator: a one-time password, a new password, two-factor set up.
	_, pw, err := o.SaveSupportOperator(ctx, "provider", "ravi.k", "Ravi K", true, false)
	if err != nil {
		t.Fatal(err)
	}
	sup := newBrowser(t, srv.URL)
	if st, out, _ := sup.do("POST", "/v1/operator/login", map[string]string{"user": "ravi.k", "password": pw}); st != 200 {
		t.Fatalf("operator login %d %v", st, out)
	}
	if st, _, _ := sup.do("GET", "/v1/operator/requests", nil); st != 403 {
		t.Fatalf("requests before set-up: %d", st)
	}
	if st, out, _ := sup.do("POST", "/v1/operator/password", map[string]string{"current": pw, "new": "a long support passphrase"}); st != 200 {
		t.Fatalf("password %d %v", st, out)
	}
	_, out, _ := sup.do("POST", "/v1/operator/2fa/setup", map[string]string{})
	secret, _ := out["secret"].(string)
	if st, out, _ := sup.do("POST", "/v1/operator/2fa/enable", map[string]string{"code": ops.TOTPCode(secret, o.Now())}); st != 200 {
		t.Fatalf("2fa %d %v", st, out)
	}
	if st, out, _ := sup.do("GET", "/v1/operator/me", nil); st != 200 || len(out["firms"].([]any)) != 1 {
		t.Fatalf("me %d %v", st, out)
	}
	// An operator login is no firm login, and the other way round.
	if st, _, _ := newBrowser(t, srv.URL).do("POST", "/v1/auth/login", map[string]string{"tenant": "demo", "user": "ravi.k", "password": "a long support passphrase"}); st != 401 {
		t.Fatalf("operator signed in to the firm: %d", st)
	}
	if st, _, _ := newBrowser(t, srv.URL).do("POST", "/v1/operator/login", map[string]string{"user": "admin", "password": ops.DevPassword}); st != 401 {
		t.Fatalf("firm admin signed in to the support desk: %d", st)
	}
	return o, adm, sup, step
}

func saAsk(t *testing.T, sup *saBrowser, areas []string, write bool) string {
	t.Helper()
	st, out, _ := sup.do("POST", "/v1/operator/requests", map[string]any{"tenant": "demo", "reason": "Settings page shows an error", "ticket": "SR-0042", "areas": areas, "write": write, "hours": 2})
	if st != 200 || out["status"] != "Pending" {
		t.Fatalf("request %d %v", st, out)
	}
	return out["id"].(string)
}

func TestSupportAccessHTTP(t *testing.T) {
	o, adm, sup, step := saServer(t)

	id := saAsk(t, sup, []string{"settings"}, false)
	// No access without approval: no session, and the firm console is closed.
	if st, _, _ := sup.do("POST", "/v1/operator/requests/demo/"+id+"/start", map[string]string{}); st != 403 {
		t.Fatalf("start before approval: %d", st)
	}
	if st, _, _ := sup.do("GET", "/v1/settings", nil); st != 401 {
		t.Fatalf("firm data before approval: %d", st)
	}
	// The Admin sees it waiting; an Ops maker may not approve.
	st, out, _ := adm.do("GET", "/v1/support-access", nil)
	if st != 200 || len(out["pending"].([]any)) != 1 || out["can_approve"] != true {
		t.Fatalf("screen %d %v", st, out)
	}
	maker := newBrowser(t, adm.url)
	maker.do("POST", "/v1/auth/login", map[string]string{"user": "priya.nair", "password": ops.DevPassword})
	if st, _, _ := maker.do("POST", "/v1/support-access/"+id+"/approve", map[string]any{}); st != 403 {
		t.Fatalf("Ops maker approved: %d", st)
	}
	if st, out, _ := adm.do("POST", "/v1/support-access/"+id+"/approve", map[string]any{"note": "ok"}); st != 200 || out["status"] != "Approved" {
		t.Fatalf("approve %d %v", st, out)
	}
	if st, out, _ := sup.do("POST", "/v1/operator/requests/demo/"+id+"/start", map[string]string{}); st != 200 {
		t.Fatalf("start %d %v", st, out)
	}

	// The support session: marked, masked, scoped, read-only.
	st, out, _ = sup.do("GET", "/v1/me", nil)
	auth, _ := out["auth"].(map[string]any)
	if st != 200 || out["actor"] != "support:ravi.k@demo" || auth["support"] == nil {
		t.Fatalf("me %d %v", st, out)
	}
	_, fullOut, _ := adm.do("GET", "/v1/settings", nil)
	_, maskedOut, raw := sup.do("GET", "/v1/settings", nil)
	full, _ := fullOut["settings"].(map[string]any)
	masked, _ := maskedOut["settings"].(map[string]any)
	if full["payout_debit_account"] != "50200012345678" || full["firm_pan"] != "AAACB1234C" {
		t.Fatalf("the firm's own Admin sees masked values: %v %v", full["payout_debit_account"], full["firm_pan"])
	}
	if masked["payout_debit_account"] != "XXXXXXXXXX5678" || masked["firm_pan"] != "AAACBXXXXC" || strings.Contains(raw, "50200012345678") || strings.Contains(raw, "AAACB1234C\"") {
		t.Fatalf("support sees in full: %v %v", masked["payout_debit_account"], masked["firm_pan"])
	}
	if st, _, _ := sup.do("PUT", "/v1/settings", full); st != 403 {
		t.Fatalf("read-only session wrote: %d", st)
	}
	if st, _, _ := sup.do("GET", "/v1/clients", nil); st != 403 {
		t.Fatalf("outside the areas: %d", st)
	}
	if st, _, _ := sup.do("POST", "/v1/clients/X1/reveal", map[string]string{"field": "pan", "reason": "need it"}); st != 403 {
		t.Fatalf("reveal: %d", st)
	}
	if st, _, _ := sup.do("POST", "/v1/support-access/"+id+"/end", map[string]any{}); st != 403 {
		t.Fatalf("support used the firm's End: %d", st)
	}
	if st, _, _ := sup.do("GET", "/v1/support-access", nil); st != 403 {
		t.Fatalf("support opened the support access screen: %d", st)
	}

	// End now: the next request is refused.
	if st, _, _ := adm.do("POST", "/v1/support-access/"+id+"/end", map[string]any{}); st != 200 {
		t.Fatalf("end %d", st)
	}
	if st, _, _ := sup.do("GET", "/v1/settings", nil); st != 401 {
		t.Fatalf("after End now: %d", st)
	}

	// Every support action is in the log, under the support actor.
	st, out, _ = adm.do("GET", "/v1/support-access/log", nil)
	if st != 200 {
		t.Fatalf("log %d", st)
	}
	var reads, refused int
	for _, l := range out["lines"].([]any) {
		m := l.(map[string]any)
		if strings.HasPrefix(m["action"].(string), "support_access.action") && m["actor"] != "support:ravi.k@demo" {
			t.Fatalf("action under %v", m["actor"])
		}
		if m["action"] == "support_access.action" {
			reads++
		}
		if m["action"] == "support_access.action_refused" {
			refused++
		}
	}
	if reads < 2 || refused < 4 {
		t.Fatalf("log: %d actions, %d refused: %v", reads, refused, out["lines"])
	}
	req, _ := http.NewRequest("GET", adm.url+"/v1/support-access/log.csv", nil)
	res, err := adm.c.Do(req)
	if err != nil || res.StatusCode != 200 || !strings.Contains(res.Header.Get("Content-Type"), "csv") {
		t.Fatalf("csv %v %v", err, res)
	}
	res.Body.Close()

	// Write approved: a change goes through and is recorded under support.
	id = saAsk(t, sup, []string{"settings"}, true)
	adm.do("POST", "/v1/support-access/"+id+"/approve", map[string]any{})
	sup.do("POST", "/v1/operator/requests/demo/"+id+"/start", map[string]string{})
	_, curOut, _ := adm.do("GET", "/v1/settings", nil)
	cur := curOut["settings"].(map[string]any)
	cur["lead_incentive"] = "750"
	if st, out, _ := sup.do("PUT", "/v1/settings", cur); st != 200 {
		t.Fatalf("write session: %d %v", st, out)
	}
	evs, _ := o.App.Audit.List(t.Context(), "demo", 50)
	found := false
	for _, e := range evs {
		found = found || e.Action == "settings.saved" && e.Actor == "support:ravi.k@demo"
	}
	if !found {
		for _, e := range evs[:10] {
			t.Logf("%s %s", e.Actor, e.Action)
		}
		t.Fatal("the change is not recorded under the support actor")
	}

	// Block: the session ends and new requests are refused.
	_, pol, _ := adm.do("GET", "/v1/support-access", nil)
	p := pol["policy"].(map[string]any)
	p["blocked"] = true
	if st, out, _ := adm.do("PUT", "/v1/support-access/policy", p); st != 200 {
		t.Fatalf("block %d %v", st, out)
	}
	if st, _, _ := sup.do("GET", "/v1/settings", nil); st != 401 {
		t.Fatalf("after block: %d", st)
	}
	if st, _, _ := sup.do("POST", "/v1/operator/requests", map[string]any{"tenant": "demo", "reason": "need to look again", "ticket": "SR-1", "areas": []string{"clients"}}); st != 403 {
		t.Fatalf("request while blocked: %d", st)
	}
	p["blocked"] = false
	adm.do("PUT", "/v1/support-access/policy", p)

	// Expiry: the approved hours run out.
	id = saAsk(t, sup, []string{"settings"}, false)
	adm.do("POST", "/v1/support-access/"+id+"/approve", map[string]any{"hours": 1})
	sup.do("POST", "/v1/operator/requests/demo/"+id+"/start", map[string]string{})
	if st, _, _ := sup.do("GET", "/v1/settings", nil); st != 200 {
		t.Fatalf("before expiry: %d", st)
	}
	step(61 * time.Minute)
	if st, _, _ := sup.do("GET", "/v1/settings", nil); st != 401 {
		t.Fatalf("after expiry: %d", st)
	}

	// The firm's own users are unaffected: nothing masked, nothing logged as support.
	maker.do("POST", "/v1/auth/login", map[string]string{"user": "priya.nair", "password": ops.DevPassword}) // idle through the hour above
	if _, m, _ := maker.do("GET", "/v1/settings", nil); m["settings"].(map[string]any)["payout_debit_account"] != "50200012345678" {
		t.Fatalf("Ops maker sees %v", m)
	}
	// Support's ID can't be put in X-Actor with an API key.
	r2, _ := http.NewRequest("POST", adm.url+"/v1/support-access/"+id+"/end", nil)
	r2.Header.Set("Authorization", "Bearer "+key)
	r2.Header.Set("X-Actor", "support:ravi.k@demo")
	if res, _ := http.DefaultClient.Do(r2); res.StatusCode != 400 {
		t.Fatalf("support actor through an API key: %d", res.StatusCode)
	}
}

func TestSupportWriterRefusesFiles(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := &supportWriter{h: http.Header{}}
	sw.Header().Set("Content-Type", "application/pdf")
	sw.Write([]byte("%PDF-1.7 PAN ABCDE1234F"))
	if st, why := sw.finish(rec); st != 403 || why == "" || strings.Contains(rec.Body.String(), "ABCDE1234F") {
		t.Fatalf("pdf: %d %q %s", st, why, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	sw = &supportWriter{h: http.Header{}}
	sw.Header().Set("Content-Type", "text/csv")
	sw.Write([]byte("UCC,PAN,Aadhaar,Account\nX1,ABCDE1234F,2345 6789 0123,000123456789\n"))
	if st, _ := sw.finish(rec); st != 200 || strings.Contains(rec.Body.String(), "ABCDE1234F") || strings.Contains(rec.Body.String(), "6789 0123") || strings.Contains(rec.Body.String(), "000123456789") {
		t.Fatalf("csv: %d %s", st, rec.Body.String())
	}
	if _, err := ParseKeys("0123456789abcdefXYZ:_support"); err == nil {
		t.Fatal("a key opened the support namespace")
	}
}
