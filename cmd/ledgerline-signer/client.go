package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// The agent protocol (Ledgerline's internal/ops/signagent.go), each call
// with the agent key as "Authorization: Bearer llsign.…":
//
//	POST /v1/signing/agent/hello  {cert_der_b64, agent_version, host} → {status, message, certificate}
//	POST /v1/signing/agent/poll   {max, agent_version, host}          → {status, message, jobs: [{id, digest_b64, hash}]}
//	POST /v1/signing/agent/result {id, signature_b64 | error, agent_version, host}
//
// Poll waits up to about 25 seconds for work. Jobs come only while the
// certificate here is the one approved in Ledgerline (status "approved").

type agentStatus struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

type agentJob struct {
	ID     string `json:"id"`
	Digest string `json:"digest_b64"`
	Hash   string `json:"hash"`
}

type pollAnswer struct {
	agentStatus
	Jobs []agentJob `json:"jobs"`
}

// httpError is an answer other than 200 from Ledgerline.
type httpError struct {
	Code int
	Msg  string
}

func (e *httpError) Error() string { return fmt.Sprintf("Ledgerline answered %d: %s", e.Code, e.Msg) }

// errKeyRefused: the agent key was not accepted; trying again will not help.
var errKeyRefused = errors.New("the agent key was refused")

type client struct {
	server, key, host string
	be                backend
	hc                *http.Client
	log               func(string)
	// Waits (shorter in tests): after a network error (doubling up to
	// maxRetry), between polls while the certificate waits for approval,
	// and between tries to open the token again after it failed.
	retry, maxRetry, waitApproval, tokenRetry time.Duration
	max                                       int
}

func newClient(server, key, host string, be backend, log func(string)) *client {
	return &client{server: strings.TrimRight(server, "/"), key: key, host: host, be: be, log: log,
		hc:    &http.Client{Timeout: 60 * time.Second},
		retry: 2 * time.Second, maxRetry: time.Minute, waitApproval: 30 * time.Second, tokenRetry: 30 * time.Second, max: 10}
}

// checkServer accepts https:// addresses, and http:// to this computer
// (for tests).
func checkServer(raw string) (string, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(raw), "/"))
	if err != nil || u.Host == "" {
		return "", errors.New("give Ledgerline's address with -server, such as https://bo.example.in")
	}
	switch h := u.Hostname(); {
	case u.Scheme == "https":
	case u.Scheme == "http" && (h == "localhost" || h == "127.0.0.1" || h == "::1"):
	default:
		return "", errors.New("Ledgerline's address must start with https://")
	}
	return u.String(), nil
}

func (c *client) call(ctx context.Context, path string, body, out any) error {
	if _, err := checkServer(c.server); err != nil {
		return err
	}
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.server+"/v1/signing/agent/"+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("User-Agent", "ledgerline-signer/"+version)
	res, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != 200 {
		var e struct {
			Message string `json:"message"`
			Error   struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		he := &httpError{Code: res.StatusCode, Msg: firstOf(e.Error.Message, e.Message, http.StatusText(res.StatusCode))}
		if res.StatusCode == 401 {
			return fmt.Errorf("%w: %s", errKeyRefused, he.Msg)
		}
		return he
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

func (c *client) hello(ctx context.Context) (agentStatus, error) {
	var st agentStatus
	err := c.call(ctx, "hello", map[string]string{"cert_der_b64": base64.StdEncoding.EncodeToString(c.be.Certificate().Raw), "agent_version": version, "host": c.host}, &st)
	return st, err
}

func (c *client) poll(ctx context.Context) (pollAnswer, error) {
	var p pollAnswer
	err := c.call(ctx, "poll", map[string]any{"max": c.max, "agent_version": version, "host": c.host}, &p)
	return p, err
}

func (c *client) result(ctx context.Context, id string, sig []byte, failed error) error {
	b := map[string]string{"id": id, "agent_version": version, "host": c.host}
	if failed != nil {
		b["error"] = failed.Error()
	} else {
		b["signature_b64"] = base64.StdEncoding.EncodeToString(sig)
	}
	return c.call(ctx, "result", b, nil)
}

// sleep waits d or until ctx ends; false when ctx ended.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func isNetErr(err error) bool {
	var ne net.Error
	var he *httpError
	return errors.As(err, &ne) || (errors.As(err, &he) && (he.Code == 429 || he.Code >= 500)) || errors.Is(err, io.EOF) || strings.Contains(err.Error(), "connection")
}

// run says hello, then polls, signs and answers until ctx ends. Network
// errors are retried with a growing wait. When the token fails (pulled
// out), the jobs in hand are answered with the error, polling stops and
// the token is tried again every 30 seconds.
func (c *client) run(ctx context.Context) error {
	wait := c.retry
	backoff := func(err error) bool {
		c.log(fmt.Sprintf("Cannot reach Ledgerline (%s); trying again in %s.", short(err), wait.Round(time.Second)))
		ok := sleep(ctx, wait)
		wait = min(2*wait, c.maxRetry)
		return ok
	}
	hello, tokenBroken, last := true, false, ""
	for ctx.Err() == nil {
		if tokenBroken {
			if err := c.be.Reopen(); err != nil {
				c.log(fmt.Sprintf("The USB token is not available (%s); trying again in %s.", short(err), c.tokenRetry.Round(time.Second)))
				sleep(ctx, c.tokenRetry)
				continue
			}
			c.log("The USB token is available again.")
			tokenBroken, hello = false, true
		}
		if hello {
			st, err := c.hello(ctx)
			switch {
			case ctx.Err() != nil:
				return nil
			case errors.Is(err, errKeyRefused):
				return err
			case err != nil:
				if !backoff(err) {
					return nil
				}
				continue
			}
			hello, wait = false, c.retry
			if st.Status != last {
				c.log(st.Message)
				last = st.Status
			}
		}
		p, err := c.poll(ctx)
		switch {
		case ctx.Err() != nil:
			return nil
		case errors.Is(err, errKeyRefused):
			return err
		case err != nil:
			if !isNetErr(err) {
				c.log("Ledgerline did not take the poll: " + short(err))
			}
			hello = true
			if !backoff(err) {
				return nil
			}
			continue
		}
		wait = c.retry
		if p.Status != last {
			c.log(p.Message)
			last = p.Status
		}
		if p.Status != "approved" {
			sleep(ctx, c.waitApproval)
			hello = true // the certificate here may have been changed meanwhile
			continue
		}
		if len(p.Jobs) == 0 {
			continue
		}
		start, signed, failed := time.Now(), 0, 0
		var tokenErr error
		for _, j := range p.Jobs {
			var sig []byte
			var serr error
			dg, err := base64.StdEncoding.DecodeString(j.Digest)
			switch {
			case tokenErr != nil:
				serr = fmt.Errorf("the USB token is not available: %s", short(tokenErr))
			case err != nil || j.Hash != "SHA256" || len(dg) != 32:
				serr = errors.New("not a SHA-256 digest")
			default:
				if sig, serr = signDigest(c.be, dg); serr != nil {
					tokenErr = serr
					serr = fmt.Errorf("the USB token did not sign: %s", short(serr))
				}
			}
			if err := c.result(ctx, j.ID, sig, serr); err != nil || serr != nil {
				failed++
				if err != nil && serr == nil {
					c.log("Ledgerline did not take a signature: " + short(err))
				}
				continue
			}
			signed++
		}
		line := fmt.Sprintf("Signed %d document%s in %.1f s", signed, plural(signed), time.Since(start).Seconds())
		if failed > 0 {
			line += fmt.Sprintf("; %d not signed", failed)
		}
		c.log(line + ".")
		if tokenErr != nil {
			c.log("The USB token stopped signing: " + short(tokenErr) + ". Check it is plugged in.")
			tokenBroken = true
		}
	}
	return nil
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// short keeps a message to one line.
func short(err error) string {
	s := strings.Join(strings.Fields(err.Error()), " ")
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
