package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Recording is a recording Ledgerline asked for.
type Recording struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	StartURL string `json:"start_url"`
}

// Recorder is what recording needs from Ledgerline.
type Recorder interface {
	RecSteps(ctx context.Context, rec string, steps []Step) (stop bool, err error)
	RecDone(ctx context.Context, rec string) (workflow string, err error)
}

type pending struct {
	s  Step
	at time.Time
}

// Record opens the start page and sends what the person does until they
// stop in Ledgerline or close the browser.
func Record(ctx context.Context, b *Browser, srv Recorder, rec Recording, logf func(string)) (string, error) {
	var mu sync.Mutex
	var buf []pending
	last := time.Now()
	files := 0
	add := func(s Step) {
		mu.Lock()
		defer mu.Unlock()
		buf = append(buf, pending{s, time.Now()})
		last = time.Now()
	}
	setup := func(session string) {
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		_ = b.CDP.Call(c, session, "Runtime.addBinding", map[string]any{"name": "__llrec"}, nil)
		_ = b.CDP.Call(c, session, "Page.addScriptToEvaluateOnNewDocument", map[string]any{"source": recorderJS}, nil)
		_ = b.CDP.Call(c, session, "Runtime.evaluate", map[string]any{"expression": recorderJS}, nil)
	}
	b.OnEvent(func(e Event) {
		switch e.Method {
		case "bot.attached":
			go setup(e.SessionID)
		case "Runtime.bindingCalled":
			var p struct {
				Name    string `json:"name"`
				Payload string `json:"payload"`
			}
			if json.Unmarshal(e.Params, &p) != nil || p.Name != "__llrec" {
				return
			}
			var s Step
			if json.Unmarshal([]byte(p.Payload), &s) != nil || s.Kind == "" {
				return
			}
			add(s)
		case "Page.frameNavigated":
			// A page opened by typing an address (not by a click or key) is
			// an "open" step.
			var p struct {
				Frame struct {
					ParentID string `json:"parentId"`
					URL      string `json:"url"`
				} `json:"frame"`
			}
			if json.Unmarshal(e.Params, &p) != nil || p.Frame.ParentID != "" || e.SessionID != b.Session() {
				return
			}
			u := p.Frame.URL
			if !strings.HasPrefix(u, "http") {
				return
			}
			mu.Lock()
			quiet := time.Since(last) > 2*time.Second
			mu.Unlock()
			if quiet {
				add(Step{Kind: "open", URL: u, Label: "Open " + u})
			}
		case "Browser.downloadWillBegin":
			// The click that started a download becomes a download step.
			var p struct {
				SuggestedFilename string `json:"suggestedFilename"`
			}
			_ = json.Unmarshal(e.Params, &p)
			mu.Lock()
			for i := len(buf) - 1; i >= 0; i-- {
				if buf[i].s.Kind == "click" {
					files++
					buf[i].s.Kind, buf[i].s.Var = "download", fmt.Sprintf("file%d", files)
					buf[i].s.Label = "Download " + p.SuggestedFilename
					break
				}
			}
			mu.Unlock()
		}
	})
	setup(b.Session())
	add(Step{Kind: "open", URL: rec.StartURL, Label: "Open " + rec.StartURL})
	nc, cancel := context.WithTimeout(ctx, 60*time.Second)
	if err := b.Navigate(nc, rec.StartURL); err != nil {
		logf("  could not open " + rec.StartURL + ": " + err.Error())
	}
	cancel()
	mu.Lock()
	last = time.Now()
	mu.Unlock()
	logf("Recording. Do the task in the browser window, then click Stop in Ledgerline (or close the window).")
	flush := func(all bool) (bool, error) {
		mu.Lock()
		var out []Step
		keep := buf[:0]
		for _, p := range buf {
			// Hold steps briefly in case a download turns a click into a download step.
			if all || time.Since(p.at) > 2500*time.Millisecond {
				out = append(out, p.s)
			} else {
				keep = append(keep, p)
			}
		}
		buf = keep
		mu.Unlock()
		c, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		return srv.RecSteps(c, rec.ID, out)
	}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-b.Exited():
			logf("The browser was closed: saving the recording.")
			_, _ = flush(true)
			return srv.RecDone(context.Background(), rec.ID)
		case <-tick.C:
			stop, err := flush(false)
			if err != nil {
				logf("  could not reach Ledgerline: " + err.Error())
				continue
			}
			if stop {
				time.Sleep(500 * time.Millisecond)
				_, _ = flush(true)
				logf("Stopped in Ledgerline: saving the recording.")
				return srv.RecDone(ctx, rec.ID)
			}
		}
	}
}
