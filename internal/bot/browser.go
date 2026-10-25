package bot

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

// FindBrowser returns Chrome, Edge or Chromium on this computer
// (LLBOT_BROWSER overrides).
func FindBrowser() (string, error) {
	if p := os.Getenv("LLBOT_BROWSER"); p != "" {
		return p, nil
	}
	var cands []string
	switch runtime.GOOS {
	case "darwin":
		cands = []string{"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", "/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"/Applications/Chromium.app/Contents/MacOS/Chromium", "/Applications/Brave Browser.app/Contents/MacOS/Brave Browser"}
		if h, err := os.UserHomeDir(); err == nil {
			cands = append(cands, filepath.Join(h, "Applications/Google Chrome.app/Contents/MacOS/Google Chrome"))
		}
	case "windows":
		for _, base := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), os.Getenv("LocalAppData")} {
			if base != "" {
				cands = append(cands, filepath.Join(base, `Google\Chrome\Application\chrome.exe`), filepath.Join(base, `Microsoft\Edge\Application\msedge.exe`))
			}
		}
	default:
		for _, n := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "microsoft-edge"} {
			if p, err := exec.LookPath(n); err == nil {
				cands = append(cands, p)
			}
		}
		if m, _ := filepath.Glob("/opt/pw-browsers/chromium-*/chrome-linux/chrome"); len(m) > 0 {
			cands = append(cands, m...)
		}
		cands = append(cands, "/opt/pw-browsers/chromium")
	}
	for _, c := range cands {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c, nil
		}
	}
	return "", errors.New("no Chrome or Edge found; install Google Chrome, or set LLBOT_BROWSER to the browser's program file")
}

// Browser is a browser the bot started, with the tab it is working in.
type Browser struct {
	CDP     *CDP
	cmd     *exec.Cmd
	mu      sync.Mutex
	session string // the current tab's session
	target  string
	tabs    []tab // newest last
	loads   int   // page loads finished in the current tab
	loading bool
	dl      map[string]*download
	dlDir   string
	events  []func(Event)
}

type tab struct{ target, session string }

type download struct {
	name, path string
	state      string // inProgress, completed, canceled
}

var reDevtools = regexp.MustCompile(`DevTools listening on (ws://\S+)`)

// Launch starts the browser with its own profile (so the bot's logins stay
// apart from the person's own browser) and opens startURL.
func Launch(ctx context.Context, exe, profile, downloads string, headless bool) (*Browser, error) {
	if err := os.MkdirAll(profile, 0o700); err != nil {
		return nil, err
	}
	args := []string{"--remote-debugging-port=0", "--user-data-dir=" + profile, "--no-first-run", "--no-default-browser-check",
		"--disable-features=Translate,DownloadBubble,DownloadBubbleV2", "--disable-popup-blocking", "--window-size=1280,900", "about:blank"}
	if headless {
		args = append([]string{"--headless=new"}, args...)
	}
	if os.Getuid() == 0 && runtime.GOOS == "linux" {
		args = append([]string{"--no-sandbox"}, args...)
	}
	cmd := exec.Command(exe, args...)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("could not start the browser: %w", err)
	}
	found := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			if m := reDevtools.FindStringSubmatch(sc.Text()); m != nil {
				found <- m[1]
			}
		}
	}()
	var ws string
	select {
	case ws = <-found:
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		return nil, errors.New("the browser did not start in 30 seconds (close other windows of it using the bot's profile and try again)")
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		return nil, ctx.Err()
	}
	c, err := DialCDP(ctx, ws)
	if err != nil {
		_ = cmd.Process.Kill()
		return nil, err
	}
	b := &Browser{CDP: c, cmd: cmd, dl: map[string]*download{}, dlDir: downloads}
	c.On(b.onEvent)
	if err := b.init(ctx); err != nil {
		b.Close()
		return nil, err
	}
	return b, nil
}

func (b *Browser) init(ctx context.Context) error {
	if err := b.CDP.Call(ctx, "", "Target.setDiscoverTargets", map[string]any{"discover": true}, nil); err != nil {
		return err
	}
	if b.dlDir != "" {
		if err := os.MkdirAll(b.dlDir, 0o700); err != nil {
			return err
		}
		if err := b.CDP.Call(ctx, "", "Browser.setDownloadBehavior", map[string]any{"behavior": "allowAndName", "downloadPath": b.dlDir, "eventsEnabled": true}, nil); err != nil {
			return err
		}
	}
	var ts struct {
		TargetInfos []struct {
			TargetID string `json:"targetId"`
			Type     string `json:"type"`
		} `json:"targetInfos"`
	}
	if err := b.CDP.Call(ctx, "", "Target.getTargets", nil, &ts); err != nil {
		return err
	}
	for _, t := range ts.TargetInfos {
		if t.Type == "page" {
			return b.attach(ctx, t.TargetID)
		}
	}
	var nt struct {
		TargetID string `json:"targetId"`
	}
	if err := b.CDP.Call(ctx, "", "Target.createTarget", map[string]any{"url": "about:blank"}, &nt); err != nil {
		return err
	}
	return b.attach(ctx, nt.TargetID)
}

// attach makes a tab the current one.
func (b *Browser) attach(ctx context.Context, target string) error {
	var r struct {
		SessionID string `json:"sessionId"`
	}
	if err := b.CDP.Call(ctx, "", "Target.attachToTarget", map[string]any{"targetId": target, "flatten": true}, &r); err != nil {
		return err
	}
	for _, m := range []string{"Page.enable", "Runtime.enable", "DOM.enable"} {
		if err := b.CDP.Call(ctx, r.SessionID, m, nil, nil); err != nil {
			return err
		}
	}
	b.mu.Lock()
	b.tabs = append(b.tabs, tab{target, r.SessionID})
	b.session, b.target = r.SessionID, target
	b.mu.Unlock()
	for _, f := range b.hooks() {
		f(Event{Method: "bot.attached", SessionID: r.SessionID})
	}
	return nil
}

func (b *Browser) hooks() []func(Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]func(Event){}, b.events...)
}

// OnEvent adds a listener (it must not block).
func (b *Browser) OnEvent(f func(Event)) {
	b.mu.Lock()
	b.events = append(b.events, f)
	b.mu.Unlock()
}

func (b *Browser) onEvent(e Event) {
	var p struct {
		TargetInfo struct {
			TargetID string `json:"targetId"`
			Type     string `json:"type"`
			OpenerID string `json:"openerId"`
		} `json:"targetInfo"`
		TargetID          string `json:"targetId"`
		GUID              string `json:"guid"`
		SuggestedFilename string `json:"suggestedFilename"`
		State             string `json:"state"`
		Frame             struct {
			ParentID string `json:"parentId"`
		} `json:"frame"`
	}
	_ = json.Unmarshal(e.Params, &p)
	switch e.Method {
	case "Target.targetCreated":
		// A link or script opened a new tab: work in it, as a person would.
		if p.TargetInfo.Type == "page" && p.TargetInfo.OpenerID != "" {
			go func(id string) {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				_ = b.attach(ctx, id)
			}(p.TargetInfo.TargetID)
		}
	case "Target.targetDestroyed":
		b.mu.Lock()
		for i, t := range b.tabs {
			if t.target == p.TargetID {
				b.tabs = append(b.tabs[:i], b.tabs[i+1:]...)
				if b.target == p.TargetID && len(b.tabs) > 0 {
					last := b.tabs[len(b.tabs)-1]
					b.session, b.target = last.session, last.target
				}
				break
			}
		}
		b.mu.Unlock()
	case "Page.frameStartedLoading":
		if e.SessionID == b.Session() {
			b.mu.Lock()
			b.loading = true
			b.mu.Unlock()
		}
	case "Page.loadEventFired":
		if e.SessionID == b.Session() {
			b.mu.Lock()
			b.loading, b.loads = false, b.loads+1
			b.mu.Unlock()
		}
	case "Page.frameStoppedLoading":
		// The main frame's id is its tab's target id. A link that turns
		// into a download stops loading without a load event.
		var f struct {
			FrameID string `json:"frameId"`
		}
		_ = json.Unmarshal(e.Params, &f)
		b.mu.Lock()
		if e.SessionID == b.session && f.FrameID == b.target {
			b.loading = false
		}
		b.mu.Unlock()
	case "Browser.downloadWillBegin":
		b.mu.Lock()
		b.loading = false
		b.dl[p.GUID] = &download{name: p.SuggestedFilename, path: filepath.Join(b.dlDir, p.GUID), state: "inProgress"}
		b.mu.Unlock()
	case "Browser.downloadProgress":
		b.mu.Lock()
		if d := b.dl[p.GUID]; d != nil && p.State != "" {
			d.state = p.State
		}
		b.mu.Unlock()
	}
	for _, f := range b.hooks() {
		f(e)
	}
}

// Session is the current tab's session.
func (b *Browser) Session() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.session
}

// Tabs is how many tabs are open.
func (b *Browser) Tabs() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.tabs)
}

// Call sends a command to the current tab.
func (b *Browser) Call(ctx context.Context, method string, params, out any) error {
	return b.CDP.Call(ctx, b.Session(), method, params, out)
}

// Navigate opens a page in the current tab and waits for it to load.
func (b *Browser) Navigate(ctx context.Context, u string) error {
	b.mu.Lock()
	n := b.loads
	b.mu.Unlock()
	var r struct {
		ErrorText string `json:"errorText"`
	}
	if err := b.Call(ctx, "Page.navigate", map[string]any{"url": u}, &r); err != nil {
		return err
	}
	if r.ErrorText != "" {
		return fmt.Errorf("could not open %s: %s", u, r.ErrorText)
	}
	return b.waitLoad(ctx, n, 60*time.Second)
}

func (b *Browser) waitLoad(ctx context.Context, after int, d time.Duration) error {
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		b.mu.Lock()
		done := b.loads > after
		b.mu.Unlock()
		if done {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return errors.New("the page did not finish loading")
}

// Settle waits after an action for any page load it started to finish.
func (b *Browser) Settle(ctx context.Context, before int) {
	time.Sleep(300 * time.Millisecond)
	end := time.Now().Add(45 * time.Second)
	for time.Now().Before(end) {
		b.mu.Lock()
		busy := b.loading
		b.mu.Unlock()
		var st struct {
			Result struct {
				Value string `json:"value"`
			} `json:"result"`
		}
		c, cancel := timeout(ctx, 5*time.Second)
		err := b.Call(c, "Runtime.evaluate", map[string]any{"expression": "document.readyState", "returnByValue": true}, &st)
		cancel()
		if !busy && err == nil && st.Result.Value == "complete" {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(150 * time.Millisecond):
		}
	}
}

// Loads is the count of finished page loads in the current tab.
func (b *Browser) Loads() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.loads
}

// Downloads lists downloads by id.
func (b *Browser) downloads() map[string]download {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := map[string]download{}
	for k, v := range b.dl {
		out[k] = *v
	}
	return out
}

// Screenshot is a PNG of the current tab.
func (b *Browser) Screenshot(ctx context.Context) []byte {
	var r struct {
		Data []byte `json:"data"`
	}
	c, cancel := timeout(ctx, 10*time.Second)
	defer cancel()
	if b.Call(c, "Page.captureScreenshot", map[string]any{"format": "png"}, &r) != nil {
		return nil
	}
	return r.Data
}

// Close quits the browser.
func (b *Browser) Close() {
	c, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	_ = b.CDP.Call(c, "", "Browser.close", nil, nil)
	cancel()
	b.CDP.Close()
	done := make(chan struct{})
	go func() { _ = b.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = b.cmd.Process.Kill()
	}
}

// Exited is closed when the person closes the browser.
func (b *Browser) Exited() <-chan struct{} { return b.CDP.Done() }

func cleanName(s string) string {
	s = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`/\:*?"<>|`, r) || r < 32 {
			return '_'
		}
		return r
	}, s)
	if s == "" || s == "." || s == ".." {
		s = "download"
	}
	return s
}
