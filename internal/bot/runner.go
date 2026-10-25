package bot

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

//go:embed find.js
var findJS string

//go:embed recorder.js
var recorderJS string

// Target, Step and Run mirror Ledgerline's workflow records.
type Target struct {
	CSS         string `json:"css,omitempty"`
	Text        string `json:"text,omitempty"`
	Role        string `json:"role,omitempty"`
	Name        string `json:"name,omitempty"`
	Tag         string `json:"tag,omitempty"`
	Label       string `json:"label,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
	Type        string `json:"type,omitempty"`
	Any         bool   `json:"any,omitempty"` // find even when not visible (file boxes)
}

type Step struct {
	ID          string  `json:"id,omitempty"`
	Kind        string  `json:"kind"`
	Label       string  `json:"label,omitempty"`
	URL         string  `json:"url,omitempty"`
	Target      *Target `json:"target,omitempty"`
	Value       string  `json:"value,omitempty"`
	Key         string  `json:"key,omitempty"`
	Var         string  `json:"var,omitempty"`
	Source      string  `json:"source,omitempty"`
	Seconds     int     `json:"seconds,omitempty"`
	Timeout     int     `json:"timeout,omitempty"`
	Optional    bool    `json:"optional,omitempty"`
	Message     string  `json:"message,omitempty"`
	NeedsSecret string  `json:"needs_secret,omitempty"`
}

type Run struct {
	ID       string            `json:"id"`
	FlowName string            `json:"flow_name"`
	Steps    []Step            `json:"steps"`
	Context  map[string]string `json:"context"`
}

// Control is Ledgerline's answer to a progress report.
type Control struct {
	Cancel bool `json:"cancel"`
	Resume bool `json:"resume"`
}

// Server is what a run needs from Ledgerline.
type Server interface {
	Event(ctx context.Context, run string, step int, status, msg string, ms int64) (Control, error)
	Secret(ctx context.Context, run string, step int, cred, field string) (string, error)
	Load(ctx context.Context, run string, step int, name string, data []byte) error
	Notify(ctx context.Context, run string, step int, text string) error
}

// Runner replays a workflow in a browser.
type Runner struct {
	B     *Browser
	S     Server
	Run   Run
	Dir   string // where the run's downloads go
	Log   func(string)
	vars  map[string]string
	files map[string]string
	step  int
}

var errCancelled = errors.New("cancelled in Ledgerline")

// Do runs every step. It returns Succeeded, Failed or Cancelled, the
// reason, and a picture of the screen when a step failed.
func (r *Runner) Do(ctx context.Context) (string, string, []byte) {
	r.vars, r.files = map[string]string{}, map[string]string{}
	for i, s := range r.Run.Steps {
		r.step = i
		start := time.Now()
		r.logf("Step %d/%d: %s", i+1, len(r.Run.Steps), describe(s))
		err := r.do(ctx, s)
		ms := time.Since(start).Milliseconds()
		if errors.Is(err, errCancelled) || ctx.Err() != nil {
			return "Cancelled", "Cancelled in Ledgerline", nil
		}
		status, msg := "Done", ""
		if err != nil {
			msg = err.Error()
			status = "Failed"
			if s.Optional {
				status = "Skipped"
			}
		}
		ctl, _ := r.S.Event(ctx, r.Run.ID, i, status, msg, ms)
		if ctl.Cancel {
			return "Cancelled", "Cancelled in Ledgerline", nil
		}
		if status == "Failed" {
			r.logf("  failed: %s", msg)
			return "Failed", fmt.Sprintf("Step %d (%s): %s", i+1, describe(s), msg), r.B.Screenshot(ctx)
		}
	}
	return "Succeeded", "", nil
}

func (r *Runner) logf(f string, a ...any) {
	if r.Log != nil {
		r.Log(fmt.Sprintf(f, a...))
	}
}

func describe(s Step) string {
	if s.Label != "" {
		return s.Label
	}
	switch s.Kind {
	case "open":
		return "open " + s.URL
	case "press":
		return "press " + s.Key
	}
	if s.Target != nil {
		return s.Kind + " " + firstNon(s.Target.Text, s.Target.Label, s.Target.Name, s.Target.CSS)
	}
	return s.Kind
}

func firstNon(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

func (r *Runner) do(ctx context.Context, s Step) error {
	wait := time.Duration(s.Timeout) * time.Second
	if wait == 0 {
		wait = 20 * time.Second
	}
	switch s.Kind {
	case "open":
		u, err := r.fill(ctx, s.URL, false)
		if err != nil {
			return err
		}
		return r.B.Navigate(ctx, u)
	case "click":
		return r.click(ctx, s.Target, wait)
	case "check":
		el, err := r.find(ctx, s.Target, wait)
		if err != nil {
			return err
		}
		var on bool
		if err := r.callOn(ctx, el, `function(){ return !!this.checked }`, &on); err != nil {
			return err
		}
		if on != (s.Value != "off") {
			return r.clickEl(ctx, el)
		}
		return nil
	case "type":
		v, err := r.fill(ctx, s.Value, true)
		if err != nil {
			return err
		}
		return r.typeInto(ctx, s.Target, v, wait)
	case "select":
		v, err := r.fill(ctx, s.Value, false)
		if err != nil {
			return err
		}
		el, err := r.find(ctx, s.Target, wait)
		if err != nil {
			return err
		}
		var ok bool
		if err := r.callOn(ctx, el, `function(v){ const n=s=>String(s||'').replace(/\s+/g,' ').trim().toLowerCase(); const o=[...this.options].find(o=>n(o.text)===n(v)||o.value===v); if(!o) return false; this.value=o.value; this.dispatchEvent(new Event('input',{bubbles:true})); this.dispatchEvent(new Event('change',{bubbles:true})); return true }`, &ok, v); err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("no option %q in the list", v)
		}
		r.B.Settle(ctx, 0)
		return nil
	case "press":
		before := r.B.Loads()
		if err := r.press(ctx, s.Key); err != nil {
			return err
		}
		r.B.Settle(ctx, before)
		return nil
	case "wait":
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(s.Seconds) * time.Second):
		}
		return nil
	case "wait_for", "assert":
		if s.Target != nil {
			_, err := r.find(ctx, s.Target, wait)
			if err != nil && s.Kind == "assert" {
				return errors.New("the page does not show what was expected")
			}
			return err
		}
		want, err := r.fill(ctx, s.Value, false)
		if err != nil {
			return err
		}
		return r.waitText(ctx, want, wait)
	case "extract":
		el, err := r.find(ctx, s.Target, wait)
		if err != nil {
			return err
		}
		var v string
		if err := r.callOn(ctx, el, `function(){ return String(this.value !== undefined && this.tagName !== 'LI' ? this.value : this.innerText).trim() }`, &v); err != nil {
			return err
		}
		r.vars[s.Var] = v
		return nil
	case "download":
		return r.download(ctx, s, wait)
	case "upload":
		p := r.files[s.Var]
		if p == "" {
			return fmt.Errorf("no file %q was downloaded earlier", s.Var)
		}
		t := *s.Target
		t.Any = true
		el, err := r.find(ctx, &t, wait)
		if err != nil {
			return err
		}
		return r.B.Call(ctx, "DOM.setFileInputFiles", map[string]any{"files": []string{p}, "objectId": el}, nil)
	case "ledgerline_upload":
		p := r.files[s.Var]
		if p == "" {
			return fmt.Errorf("no file %q was downloaded earlier", s.Var)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return r.S.Load(ctx, r.Run.ID, r.step, filepath.Base(p), data)
	case "pause":
		msg, _ := r.fill(ctx, s.Message, false)
		r.logf("  waiting for you: %s (click Continue in Ledgerline)", msg)
		for {
			ctl, err := r.S.Event(ctx, r.Run.ID, r.step, "Paused", msg, 0)
			if err == nil && ctl.Cancel {
				return errCancelled
			}
			if err == nil && ctl.Resume {
				_, _ = r.S.Event(ctx, r.Run.ID, r.step, "Running", "", 0)
				r.B.Settle(ctx, 0)
				return nil
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-r.B.Exited():
				return errors.New("the browser was closed")
			case <-time.After(3 * time.Second):
			}
		}
	case "notify":
		msg, err := r.fill(ctx, s.Message, false)
		if err != nil {
			return err
		}
		return r.S.Notify(ctx, r.Run.ID, r.step, msg)
	}
	return fmt.Errorf("unknown step %q", s.Kind)
}

// ---- values ----

var reTmpl = regexp.MustCompile(`\{\{\s*([^{}]+?)\s*\}\}`)
var reSecret = regexp.MustCompile(`^secret:([A-Za-z0-9_-]+)\.(username|password|totp)$`)

// fill puts dates, values read earlier and (where allowed) saved passwords
// into text.
func (r *Runner) fill(ctx context.Context, s string, secrets bool) (string, error) {
	var ferr error
	out := reTmpl.ReplaceAllStringFunc(s, func(m string) string {
		t := strings.TrimSpace(m[2 : len(m)-2])
		if sm := reSecret.FindStringSubmatch(t); sm != nil {
			if !secrets {
				ferr = errors.New("passwords can only be typed into boxes")
				return ""
			}
			v, err := r.S.Secret(ctx, r.Run.ID, r.step, sm[1], sm[2])
			if err != nil {
				ferr = fmt.Errorf("could not get the saved login: %w", err)
			}
			return v
		}
		if v, ok := r.vars[t]; ok {
			return v
		}
		name, layout, _ := strings.Cut(t, ":")
		if d, ok := r.Run.Context[name]; ok {
			return formatDate(d, layout)
		}
		ferr = fmt.Errorf("no value for {{%s}}", t)
		return ""
	})
	return out, ferr
}

// formatDate writes YYYY-MM-DD in a layout such as DD-MM-YYYY, DDMMYY or
// DD-MON-YYYY (the default is DD-MM-YYYY).
func formatDate(d, layout string) string {
	t, err := time.Parse("2006-01-02", d)
	if err != nil {
		return d
	}
	if layout == "" {
		layout = "DD-MM-YYYY"
	}
	repl := strings.NewReplacer("YYYY", "2006", "YY", "06", "MON", "\x01", "Mon", "Jan", "MMM", "Jan", "MM", "01", "DD", "02")
	s := t.Format(repl.Replace(layout))
	return strings.ReplaceAll(s, "\x01", strings.ToUpper(t.Format("Jan")))
}

// ---- the page ----

// find waits for the element and returns a handle to it.
func (r *Runner) find(ctx context.Context, t *Target, wait time.Duration) (string, error) {
	if t == nil {
		return "", errors.New("the step does not say what to act on")
	}
	arg, _ := json.Marshal(t)
	end := time.Now().Add(wait)
	for {
		var res struct {
			Result struct {
				ObjectID string `json:"objectId"`
				Subtype  string `json:"subtype"`
			} `json:"result"`
		}
		c, cancel := timeout(ctx, 10*time.Second)
		err := r.B.Call(c, "Runtime.evaluate", map[string]any{"expression": "(" + findJS + ")(" + string(arg) + ")"}, &res)
		cancel()
		if err == nil && res.Result.ObjectID != "" && res.Result.Subtype != "null" {
			return res.Result.ObjectID, nil
		}
		if time.Now().After(end) {
			return "", fmt.Errorf("could not find %q on the page", firstNon(t.Text, t.Label, t.Placeholder, t.Name, t.CSS))
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(400 * time.Millisecond):
		}
	}
}

func (r *Runner) callOn(ctx context.Context, obj, fn string, out any, args ...any) error {
	as := []map[string]any{}
	for _, a := range args {
		as = append(as, map[string]any{"value": a})
	}
	var res struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text string `json:"text"`
		} `json:"exceptionDetails"`
	}
	c, cancel := timeout(ctx, 15*time.Second)
	defer cancel()
	if err := r.B.Call(c, "Runtime.callFunctionOn", map[string]any{"objectId": obj, "functionDeclaration": fn, "arguments": as, "returnByValue": true, "awaitPromise": true}, &res); err != nil {
		return err
	}
	if res.ExceptionDetails != nil {
		return errors.New("the page refused: " + res.ExceptionDetails.Text)
	}
	if out != nil && len(res.Result.Value) > 0 {
		return json.Unmarshal(res.Result.Value, out)
	}
	return nil
}

func (r *Runner) click(ctx context.Context, t *Target, wait time.Duration) error {
	el, err := r.find(ctx, t, wait)
	if err != nil {
		return err
	}
	return r.clickEl(ctx, el)
}

// clickEl clicks the middle of the element with the mouse, as a person would.
func (r *Runner) clickEl(ctx context.Context, el string) error {
	var p struct{ X, Y float64 }
	if err := r.callOn(ctx, el, `function(){ this.scrollIntoView({block:'center',inline:'center'}); const b=this.getBoundingClientRect(); let x=b.left+b.width/2, y=b.top+b.height/2, w=this.ownerDocument.defaultView; while (w && w.frameElement) { const f=w.frameElement.getBoundingClientRect(); x+=f.left; y+=f.top; w=w.parent; } return {X:x,Y:y} }`, &p); err != nil {
		return err
	}
	before := r.B.Loads()
	for _, t := range []string{"mouseMoved", "mousePressed", "mouseReleased"} {
		ev := map[string]any{"type": t, "x": p.X, "y": p.Y, "button": "left", "clickCount": 1}
		if t == "mouseMoved" {
			ev["button"] = "none"
		}
		if err := r.B.Call(ctx, "Input.dispatchMouseEvent", ev, nil); err != nil {
			return err
		}
	}
	r.B.Settle(ctx, before)
	return nil
}

func (r *Runner) typeInto(ctx context.Context, t *Target, v string, wait time.Duration) error {
	el, err := r.find(ctx, t, wait)
	if err != nil {
		return err
	}
	var direct bool
	// Date pickers and similar take their value directly; other boxes are
	// typed into, so the page sees real key input.
	if err := r.callOn(ctx, el, `function(v){ this.scrollIntoView({block:'center'}); this.focus(); const d=['date','time','datetime-local','month','week','color','range'].includes((this.type||'').toLowerCase()); if (d) { const s=Object.getOwnPropertyDescriptor(Object.getPrototypeOf(this),'value'); if (s && s.set) s.set.call(this,v); else this.value=v; this.dispatchEvent(new Event('input',{bubbles:true})); this.dispatchEvent(new Event('change',{bubbles:true})); return true } if (this.select) this.select(); else { const g=this.ownerDocument.getSelection(), rg=this.ownerDocument.createRange(); rg.selectNodeContents(this); g.removeAllRanges(); g.addRange(rg) } return false }`, &direct, v); err != nil {
		return err
	}
	if direct {
		return nil
	}
	if v == "" {
		if err := r.press(ctx, "Backspace"); err != nil {
			return err
		}
	} else if err := r.B.Call(ctx, "Input.insertText", map[string]any{"text": v}, nil); err != nil {
		return err
	}
	return r.callOn(ctx, el, `function(){ this.dispatchEvent(new Event('change',{bubbles:true})) }`, nil)
}

var keyCodes = map[string]int{"Enter": 13, "Tab": 9, "Escape": 27, "Backspace": 8, "Delete": 46, "Space": 32, "ArrowLeft": 37, "ArrowUp": 38, "ArrowRight": 39, "ArrowDown": 40,
	"Home": 36, "End": 35, "PageUp": 33, "PageDown": 34, "F1": 112, "F2": 113, "F3": 114, "F4": 115, "F5": 116, "F6": 117, "F7": 118, "F8": 119, "F9": 120, "F10": 121, "F11": 122, "F12": 123}

func (r *Runner) press(ctx context.Context, key string) error {
	code, ok := keyCodes[key]
	if !ok {
		return fmt.Errorf("unknown key %s", key)
	}
	k := key
	if key == "Space" {
		k = " "
	}
	down := map[string]any{"type": "keyDown", "key": k, "code": key, "windowsVirtualKeyCode": code, "nativeVirtualKeyCode": code}
	switch key {
	case "Enter":
		down["text"] = "\r"
	case "Space":
		down["text"] = " "
	default:
		down["type"] = "rawKeyDown"
	}
	if err := r.B.Call(ctx, "Input.dispatchKeyEvent", down, nil); err != nil {
		return err
	}
	return r.B.Call(ctx, "Input.dispatchKeyEvent", map[string]any{"type": "keyUp", "key": k, "code": key, "windowsVirtualKeyCode": code, "nativeVirtualKeyCode": code}, nil)
}

func (r *Runner) waitText(ctx context.Context, want string, wait time.Duration) error {
	end := time.Now().Add(wait)
	arg, _ := json.Marshal(strings.ToLower(want))
	for {
		var res struct {
			Result struct {
				Value bool `json:"value"`
			} `json:"result"`
		}
		c, cancel := timeout(ctx, 10*time.Second)
		_ = r.B.Call(c, "Runtime.evaluate", map[string]any{"expression": "document.body && document.body.innerText.toLowerCase().includes(" + string(arg) + ")", "returnByValue": true}, &res)
		cancel()
		if res.Result.Value {
			return nil
		}
		if time.Now().After(end) {
			return fmt.Errorf("the page did not show %q", want)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// download clicks and waits for the file, then keeps it under its own name.
func (r *Runner) download(ctx context.Context, s Step, wait time.Duration) error {
	before := r.B.downloads()
	if err := r.click(ctx, s.Target, wait); err != nil {
		return err
	}
	end := time.Now().Add(maxDur(wait, 2*time.Minute))
	for {
		for id, d := range r.B.downloads() {
			if _, old := before[id]; old {
				continue
			}
			switch d.state {
			case "completed":
				dst := filepath.Join(r.Dir, cleanName(d.name))
				if err := os.Rename(d.path, dst); err != nil {
					dst = d.path
				}
				r.files[s.Var] = dst
				r.logf("  downloaded %s", filepath.Base(dst))
				return nil
			case "canceled":
				return errors.New("the download was cancelled")
			}
		}
		if time.Now().After(end) {
			return errors.New("no file was downloaded")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
}

func maxDur(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}
