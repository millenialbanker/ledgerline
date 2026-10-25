package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// site is a small portal like an exchange's: a login, a form, a download,
// an upload and a frame.
type site struct {
	mu  sync.Mutex
	got map[string]string
	*httptest.Server
}

func newSite(t *testing.T) *site {
	s := &site{got: map[string]string{}}
	m := http.NewServeMux()
	page := func(w http.ResponseWriter, body string) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<!doctype html><html><body>%s</body></html>`, body)
	}
	m.HandleFunc("GET /login", func(w http.ResponseWriter, r *http.Request) {
		page(w, `<h1>Member login</h1><form method="post" action="/login"><label>User ID <input name="user" id="u17263548"></label><label>Password <input type="password" name="pw"></label><button>Login</button></form>`)
	})
	m.HandleFunc("POST /login", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.got["user"], s.got["pw"] = r.FormValue("user"), r.FormValue("pw")
		s.mu.Unlock()
		if r.FormValue("pw") != "s3cret!" {
			page(w, `<p>Wrong password</p>`)
			return
		}
		http.Redirect(w, r, "/home", http.StatusSeeOther)
	})
	m.HandleFunc("GET /home", func(w http.ResponseWriter, r *http.Request) {
		page(w, `<p id="welcome">Welcome BRK001</p>
<form method="post" action="/report"><select id="seg"><option>Equity</option><option>Derivatives</option></select>
<input id="date" placeholder="DD-MM-YYYY"> <label><input type="checkbox" id="agree"> I agree</label>
<button type="button" onclick="document.getElementById('st').textContent='seg='+seg.value+' date='+date.value+' agree='+agree.checked">Apply</button></form>
<p id="st"></p><a href="/trades.csv">Download trades</a>
<form method="post" action="/upload" enctype="multipart/form-data"><input type="file" name="f" style="display:none" id="f"><button>Upload</button></form>
<iframe src="/frame" style="width:300px;height:80px"></iframe>`)
	})
	m.HandleFunc("GET /frame", func(w http.ResponseWriter, r *http.Request) {
		page(w, `<button onclick="this.textContent='frame clicked'">Inside frame</button>`)
	})
	m.HandleFunc("GET /trades.csv", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("Content-Disposition", `attachment; filename="trades_26102026.csv"`)
		io.WriteString(w, "TradeNo,Symbol,Qty\n1,INFY,10\n")
	})
	m.HandleFunc("POST /upload", func(w http.ResponseWriter, r *http.Request) {
		f, h, err := r.FormFile("f")
		if err != nil {
			page(w, "no file")
			return
		}
		b, _ := io.ReadAll(f)
		s.mu.Lock()
		s.got["upload"] = h.Filename + ":" + string(b)
		s.mu.Unlock()
		page(w, fmt.Sprintf(`<p>Uploaded %d bytes</p>`, len(b)))
	})
	s.Server = httptest.NewServer(m)
	t.Cleanup(s.Close)
	return s
}

// fake is Ledgerline's side.
type fake struct {
	mu      sync.Mutex
	secrets []string
	loaded  string
	notes   []string
	events  []string
	steps   []Step
	calls   int
	stopAt  int
}

func (f *fake) Event(_ context.Context, run string, step int, status, msg string, ms int64) (Control, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, fmt.Sprintf("%d:%s:%s", step, status, msg))
	return Control{Resume: true}, nil
}
func (f *fake) Secret(_ context.Context, run string, step int, cred, field string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.secrets = append(f.secrets, fmt.Sprintf("%d:%s.%s", step, cred, field))
	return map[string]string{"username": "BRK001", "password": "s3cret!"}[field], nil
}
func (f *fake) Load(_ context.Context, run string, step int, name string, data []byte) error {
	f.mu.Lock()
	f.loaded = name + ":" + string(data)
	f.mu.Unlock()
	return nil
}
func (f *fake) Notify(_ context.Context, run string, step int, text string) error {
	f.mu.Lock()
	f.notes = append(f.notes, text)
	f.mu.Unlock()
	return nil
}
func (f *fake) RecSteps(_ context.Context, rec string, steps []Step) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.steps = append(f.steps, steps...)
	f.calls++
	return f.stopAt > 0 && f.calls >= f.stopAt, nil
}
func (f *fake) RecDone(context.Context, string) (string, error) { return "WF-0001", nil }

func browser(t *testing.T) (*Browser, string) {
	t.Helper()
	exe, err := FindBrowser()
	if err != nil {
		t.Skip("no browser:", err)
	}
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	b, err := Launch(ctx, exe, filepath.Join(dir, "profile"), filepath.Join(dir, "dl"), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	return b, dir
}

func TestRunWorkflow(t *testing.T) {
	s := newSite(t)
	b, dir := browser(t)
	f := &fake{}
	steps := []Step{
		{Kind: "open", URL: s.URL + "/login"},
		{Kind: "type", Target: &Target{CSS: "#u17263548-gone", Label: "User ID"}, Value: "{{secret:CR-1.username}}"}, // a changed id: found by its label
		{Kind: "type", Target: &Target{CSS: "input[name=\"pw\"]"}, Value: "{{secret:CR-1.password}}"},
		{Kind: "click", Target: &Target{Text: "Login", Tag: "button"}},
		{Kind: "wait_for", Value: "Welcome"},
		{Kind: "extract", Target: &Target{CSS: "#welcome"}, Var: "who"},
		{Kind: "select", Target: &Target{CSS: "#seg"}, Value: "Derivatives"},
		{Kind: "type", Target: &Target{Placeholder: "DD-MM-YYYY"}, Value: "{{tradedate:DD-MM-YYYY}}"},
		{Kind: "check", Target: &Target{CSS: "#agree"}, Value: "on"},
		{Kind: "click", Target: &Target{Text: "Apply"}},
		{Kind: "assert", Value: "seg=Derivatives date=23-10-2026 agree=true"},
		{Kind: "download", Target: &Target{Text: "Download trades"}, Var: "file1"},
		{Kind: "upload", Target: &Target{CSS: "#f"}, Var: "file1"},
		{Kind: "click", Target: &Target{Text: "Upload"}},
		{Kind: "wait_for", Value: "Uploaded 29 bytes"},
		{Kind: "press", Key: "Tab"},
		{Kind: "wait", Seconds: 1},
		{Kind: "ledgerline_upload", Var: "file1", Source: "NSE-CM-TRADE"},
		{Kind: "notify", Message: "Done for {{who}} on {{tradedate:DD-MON-YYYY}}"},
		{Kind: "click", Target: &Target{Text: "Nothing like this"}, Timeout: 1, Optional: true},
	}
	r := &Runner{B: b, S: f, Run: Run{ID: "RUN-1", Steps: steps, Context: map[string]string{"tradedate": "2026-10-23"}}, Dir: dir, Log: func(s string) { t.Log(time.Now().Format("05.000"), s) }}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	status, msg, _ := r.Do(ctx)
	if status != "Succeeded" {
		t.Fatalf("%s: %s\nevents %v", status, msg, f.events)
	}
	if s.got["user"] != "BRK001" || s.got["pw"] != "s3cret!" {
		t.Errorf("login posted %v", s.got)
	}
	if strings.Join(f.secrets, " ") != "1:CR-1.username 2:CR-1.password" {
		t.Errorf("secrets asked %v", f.secrets)
	}
	if s.got["upload"] != "trades_26102026.csv:TradeNo,Symbol,Qty\n1,INFY,10\n" {
		t.Errorf("upload %q", s.got["upload"])
	}
	if f.loaded != "trades_26102026.csv:TradeNo,Symbol,Qty\n1,INFY,10\n" {
		t.Errorf("loaded %q", f.loaded)
	}
	if len(f.notes) != 1 || f.notes[0] != "Done for Welcome BRK001 on 23-OCT-2026" {
		t.Errorf("notes %v", f.notes)
	}
	if last := f.events[len(f.events)-1]; !strings.HasPrefix(last, "19:Skipped") {
		t.Errorf("optional step: %s", last)
	}
	// The frame: a button inside it is found and clicked.
	r2 := &Runner{B: b, S: f, Run: Run{ID: "RUN-2", Steps: []Step{{Kind: "open", URL: s.URL + "/home"}, {Kind: "click", Target: &Target{Text: "Inside frame"}}, {Kind: "wait_for", Target: &Target{Text: "frame clicked"}}}}, Dir: dir}
	if st, msg, _ := r2.Do(ctx); st != "Succeeded" {
		t.Fatalf("frame: %s", msg)
	}
	// A failure gives the reason and a picture.
	r3 := &Runner{B: b, S: f, Run: Run{ID: "RUN-3", Steps: []Step{{Kind: "click", Target: &Target{Text: "Missing button"}, Timeout: 1}}}, Dir: dir}
	if st, msg, shot := r3.Do(ctx); st != "Failed" || !strings.Contains(msg, "Missing button") || len(shot) < 100 {
		t.Fatalf("failure: %s %s %d", st, msg, len(shot))
	}
}

func TestRecordWorkflow(t *testing.T) {
	s := newSite(t)
	b, dir := browser(t)
	f := &fake{}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	done := make(chan string, 1)
	go func() {
		wf, err := Record(ctx, b, f, Recording{ID: "REC-1", StartURL: s.URL + "/login"}, func(string) {})
		if err != nil {
			t.Error(err)
		}
		done <- wf
	}()
	// A person: click the user box, type, click the password box, type,
	// click Login, then download the trades file.
	time.Sleep(2 * time.Second)
	act := &Runner{B: b, S: f, Dir: dir, vars: map[string]string{}, files: map[string]string{}}
	typeLike := func(t0 *Target, text string) {
		if err := act.click(ctx, t0, 10*time.Second); err != nil {
			t.Fatal(err)
		}
		if err := b.Call(ctx, "Input.insertText", map[string]any{"text": text}, nil); err != nil {
			t.Fatal(err)
		}
	}
	typeLike(&Target{Label: "User ID"}, "BRK001")
	typeLike(&Target{CSS: "input[name=\"pw\"]"}, "s3cret!")
	if err := act.click(ctx, &Target{Text: "Login", Tag: "button"}, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := act.waitText(ctx, "Welcome", 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := act.click(ctx, &Target{Text: "Download trades"}, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := act.click(ctx, &Target{CSS: "#agree"}, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	time.Sleep(4 * time.Second)
	f.mu.Lock()
	f.stopAt = f.calls + 1
	f.mu.Unlock()
	select {
	case wf := <-done:
		if wf != "WF-0001" {
			t.Fatalf("workflow %q", wf)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("recording did not stop")
	}
	raw, _ := json.Marshal(f.steps)
	if strings.Contains(string(raw), "s3cret") {
		t.Fatal("the typed password left the page")
	}
	var kinds []string
	for _, st := range f.steps {
		kinds = append(kinds, st.Kind)
	}
	got := strings.Join(kinds, ",")
	t.Logf("recorded: %s", raw)
	want := "open,type,type,click,download,check"
	if got != want {
		t.Fatalf("recorded %s, want %s", got, want)
	}
	u, p := f.steps[1], f.steps[2]
	if u.Value != "BRK001" || u.NeedsSecret != "username" || u.Target.Label != "User ID" || u.Target.CSS == "#u17263548" {
		t.Errorf("username step %+v %+v", u, u.Target)
	}
	if p.Value != "" || p.NeedsSecret != "password" {
		t.Errorf("password step %+v", p)
	}
	if f.steps[4].Var != "file1" || !strings.Contains(f.steps[4].Label, "trades_26102026.csv") {
		t.Errorf("download step %+v", f.steps[4])
	}
	if f.steps[5].Value != "on" {
		t.Errorf("check step %+v", f.steps[5])
	}
	_ = os.RemoveAll(dir)
}
