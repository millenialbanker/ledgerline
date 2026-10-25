package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Version of the bot.
const Version = "1.0"

// Config is the bot's settings on this computer.
type Config struct {
	Server string `json:"server"`
	Tenant string `json:"tenant"`
	Token  string `json:"token"`
	Agent  string `json:"agent"`
	User   string `json:"user"`
	Name   string `json:"name"`
}

// Home is the bot's folder: settings, its browser profile and downloads.
func Home() string {
	if d := os.Getenv("LLBOT_HOME"); d != "" {
		return d
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return ".ledgerline-bot"
	}
	return filepath.Join(h, ".ledgerline-bot")
}

func configPath() string { return filepath.Join(Home(), "config.json") }

// LoadConfig reads the settings.
func LoadConfig() (Config, error) {
	var c Config
	b, err := os.ReadFile(configPath())
	if errors.Is(err, os.ErrNotExist) {
		return c, errors.New("this computer is not paired yet: in Ledgerline open Automations → Computers → Add this computer, then run the pair command it shows")
	}
	if err != nil {
		return c, err
	}
	return c, json.Unmarshal(b, &c)
}

// SaveConfig writes the settings, readable only by this user.
func SaveConfig(c Config) error {
	if err := os.MkdirAll(Home(), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(configPath(), b, 0o600)
}

// Agent waits for work from Ledgerline and does it.
type Agent struct {
	Cfg      Config
	Client   *Client
	Browser  string // program file
	Headless bool   // runs without a window (recording always shows one)
	Log      func(string)
}

func (a *Agent) logf(f string, v ...any) {
	if a.Log != nil {
		a.Log(fmt.Sprintf(f, v...))
	}
}

// Serve runs until ctx ends.
func (a *Agent) Serve(ctx context.Context) error {
	a.logf("Ready. Waiting for work from %s (stop with Ctrl+C).", a.Cfg.Server)
	backoff := time.Second
	for ctx.Err() == nil {
		j, err := a.Client.Next(ctx)
		if err != nil {
			var ae *apiError
			if errors.As(err, &ae) && ae.Status == 401 {
				return errors.New("Ledgerline no longer accepts this computer (it was removed); pair it again")
			}
			if ctx.Err() != nil {
				return nil
			}
			a.logf("Could not reach Ledgerline (%v); trying again in %s.", err, backoff)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(backoff):
			}
			if backoff < time.Minute {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		if j == nil {
			continue
		}
		switch j.Kind {
		case "record":
			a.record(ctx, *j.Recording)
		case "run":
			a.run(ctx, *j.Run)
		}
	}
	return nil
}

func (a *Agent) record(ctx context.Context, rec Recording) {
	a.logf("Recording %q starting at %s", rec.Name, rec.StartURL)
	// Recording shows the window; LLBOT_HEADLESS_RECORD=1 hides it, for
	// automated tests on machines without a screen.
	b, err := Launch(ctx, a.Browser, filepath.Join(Home(), "browser"), filepath.Join(Home(), "downloads", rec.ID), os.Getenv("LLBOT_HEADLESS_RECORD") == "1")
	if err != nil {
		a.logf("  %v", err)
		_, _ = a.Client.RecDone(ctx, rec.ID)
		return
	}
	defer b.Close()
	wf, err := Record(ctx, b, a.Client, rec, a.Log)
	if err != nil {
		a.logf("  recording ended: %v", err)
		return
	}
	a.logf("Saved as workflow %s. Open it in Ledgerline to check the steps.", wf)
}

func (a *Agent) run(ctx context.Context, r Run) {
	a.logf("Running %q (%s)", r.FlowName, r.ID)
	dir := filepath.Join(Home(), "runs", r.ID)
	_ = os.MkdirAll(dir, 0o700)
	b, err := Launch(ctx, a.Browser, filepath.Join(Home(), "browser"), filepath.Join(dir, ".partial"), a.Headless)
	if err != nil {
		a.logf("  %v", err)
		_ = a.Client.Done(ctx, r.ID, "Failed", err.Error(), nil)
		return
	}
	defer b.Close()
	runner := &Runner{B: b, S: a.Client, Run: r, Dir: dir, Log: a.Log}
	status, msg, shot := runner.Do(ctx)
	a.logf("  %s %s", status, msg)
	c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := a.Client.Done(c, r.ID, status, msg, shot); err != nil {
		a.logf("  could not report the result: %v", err)
	}
}
