package bot

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client talks to Ledgerline as a paired computer.
type Client struct {
	Server string // https://bo.example.in
	Tenant string
	Token  string
	HTTP   *http.Client
}

// CheckServer refuses plain http except on this computer.
func CheckServer(raw string) (string, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(raw), "/"))
	if err != nil || u.Host == "" {
		return "", errors.New("give Ledgerline's address, like https://bo.example.in")
	}
	local := u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"
	if u.Scheme != "https" && !(u.Scheme == "http" && local) {
		return "", errors.New("Ledgerline's address must start with https:// (passwords travel over it)")
	}
	return u.String(), nil
}

func (c *Client) hc() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 60 * time.Second}
}

type apiError struct {
	Status  int
	Message string
}

func (e *apiError) Error() string { return e.Message }

func (c *Client) do(ctx context.Context, method, path string, body io.Reader, ct string, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.Server+"/agent/v1/"+url.PathEscape(c.Tenant)+path, body)
	if err != nil {
		return 0, err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	resp, err := c.hc().Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if resp.StatusCode >= 300 {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(b, &e)
		return resp.StatusCode, &apiError{resp.StatusCode, firstNon(e.Error.Message, resp.Status)}
	}
	if out != nil && resp.StatusCode != http.StatusNoContent {
		return resp.StatusCode, json.Unmarshal(b, out)
	}
	return resp.StatusCode, nil
}

func (c *Client) post(ctx context.Context, path string, in, out any) error {
	b, _ := json.Marshal(in)
	_, err := c.do(ctx, http.MethodPost, path, bytes.NewReader(b), "application/json", out)
	return err
}

// Pair exchanges a code from Ledgerline for this computer's token.
func (c *Client) Pair(ctx context.Context, code, name, osName, version string) (agent, user string, err error) {
	var r struct{ Agent, User, Token string }
	if err := c.post(ctx, "/pair", map[string]string{"code": code, "name": name, "os": osName, "version": version}, &r); err != nil {
		return "", "", err
	}
	c.Token = r.Token
	return r.Agent, r.User, nil
}

// Job is something to do.
type Job struct {
	Kind      string     `json:"kind"`
	Recording *Recording `json:"recording"`
	Run       *Run       `json:"run"`
}

// Next waits up to 25 seconds for a job.
func (c *Client) Next(ctx context.Context) (*Job, error) {
	var j Job
	st, err := c.do(ctx, http.MethodGet, "/next?wait=25", nil, "", &j)
	if err != nil || st == http.StatusNoContent || j.Kind == "" {
		return nil, err
	}
	return &j, nil
}

func (c *Client) RecSteps(ctx context.Context, rec string, steps []Step) (bool, error) {
	var r struct {
		Stop bool `json:"stop"`
	}
	if steps == nil {
		steps = []Step{}
	}
	err := c.post(ctx, "/recordings/"+url.PathEscape(rec)+"/steps", map[string]any{"steps": steps}, &r)
	return r.Stop, err
}

func (c *Client) RecDone(ctx context.Context, rec string) (string, error) {
	var r struct {
		Workflow string `json:"workflow"`
	}
	err := c.post(ctx, "/recordings/"+url.PathEscape(rec)+"/done", map[string]any{}, &r)
	return r.Workflow, err
}

func (c *Client) Event(ctx context.Context, run string, step int, status, msg string, ms int64) (Control, error) {
	var ctl Control
	err := c.post(ctx, "/runs/"+url.PathEscape(run)+"/event", map[string]any{"step": step, "status": status, "message": msg, "ms": ms}, &ctl)
	return ctl, err
}

func (c *Client) Secret(ctx context.Context, run string, step int, cred, field string) (string, error) {
	var r struct {
		Value string `json:"value"`
	}
	err := c.post(ctx, "/runs/"+url.PathEscape(run)+"/secret", map[string]any{"step": step, "cred": cred, "field": field}, &r)
	return r.Value, err
}

func (c *Client) Load(ctx context.Context, run string, step int, name string, data []byte) error {
	_, err := c.do(ctx, http.MethodPost, "/runs/"+url.PathEscape(run)+"/load?step="+strconv.Itoa(step)+"&name="+url.QueryEscape(name), bytes.NewReader(data), "application/octet-stream", nil)
	if err != nil {
		return fmt.Errorf("Ledgerline did not accept the file: %w", err)
	}
	return nil
}

func (c *Client) Notify(ctx context.Context, run string, step int, text string) error {
	return c.post(ctx, "/runs/"+url.PathEscape(run)+"/notify", map[string]any{"step": step, "text": text}, nil)
}

func (c *Client) Done(ctx context.Context, run, status, msg string, shot []byte) error {
	return c.post(ctx, "/runs/"+url.PathEscape(run)+"/done", map[string]any{"status": status, "error": msg, "shot": base64.StdEncoding.EncodeToString(shot)}, nil)
}
