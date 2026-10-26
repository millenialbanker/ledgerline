package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"ledgerline/internal/control"
)

// ControlClient reports this installation to the provider's Ledgerline
// Control every quarter hour (and at once when a support request is
// written), and carries out what comes back: support replies and status,
// and AI credit top-ups.
type ControlClient struct {
	Ops     *Ops
	URL     string // e.g. https://control.example.com
	Key     string // the installation key Control issued
	Tenants func() []string
	Info    control.InstallInfo
	Log     *slog.Logger
	Every   time.Duration
	HTTP    *http.Client

	mu        sync.Mutex
	acks      []control.CommandAck
	delivered []string
	LastOK    time.Time
	LastErr   string
}

// Run reports until ctx ends.
func (c *ControlClient) Run(ctx context.Context) {
	if c.Every == 0 {
		c.Every = 15 * time.Minute
	}
	t := time.NewTicker(c.Every)
	defer t.Stop()
	_ = c.ReportNow(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-c.Ops.ControlKick:
			time.Sleep(2 * time.Second) // gather a burst of messages
		}
		if err := c.ReportNow(ctx); err != nil && c.Log != nil {
			c.Log.Warn("ledgerline control", "err", err)
		}
	}
}

// ReportNow sends one report and applies the reply.
func (c *ControlClient) ReportNow(ctx context.Context) error {
	rep := control.Report{Installation: c.Info, Firms: []control.FirmReport{}, SentAt: time.Now().Format(time.RFC3339)}
	tenants := c.Tenants()
	for _, tn := range tenants {
		f, err := c.Ops.FirmReport(ctx, tn)
		if err != nil {
			continue
		}
		rep.Firms = append(rep.Firms, f)
		rep.Tickets = append(rep.Tickets, c.Ops.SupportPending(ctx, tn)...)
	}
	c.mu.Lock()
	rep.Acks, rep.Delivered = c.acks, c.delivered
	c.mu.Unlock()

	body, _ := json.Marshal(rep)
	req, err := http.NewRequestWithContext(ctx, "POST", c.URL+"/api/v1/report", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Key)
	req.Header.Set("Content-Type", "application/json")
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 60 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		c.fail(err.Error())
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != 200 {
		c.fail(fmt.Sprintf("Control answered %d: %.200s", resp.StatusCode, raw))
		return fmt.Errorf("control answered %d", resp.StatusCode)
	}
	var reply control.ReportReply
	if err := json.Unmarshal(raw, &reply); err != nil {
		c.fail("bad reply from Control")
		return err
	}
	// Control has the report: what was sent no longer needs sending.
	c.mu.Lock()
	c.acks, c.delivered = c.acks[len(rep.Acks):], c.delivered[len(rep.Delivered):]
	c.LastOK, c.LastErr = time.Now(), ""
	c.mu.Unlock()
	for _, tn := range tenants {
		c.Ops.SupportSynced(ctx, tn, rep.Tickets)
		c.Ops.SupportApply(ctx, tn, reply.Updates)
	}
	var del []string
	for _, u := range reply.Updates {
		del = append(del, u.MsgID)
	}
	var acks []control.CommandAck
	known := map[string]bool{}
	for _, tn := range tenants {
		known[tn] = true
	}
	for _, cmd := range reply.Commands {
		a := control.CommandAck{ID: cmd.ID, OK: true}
		if !known[cmd.Tenant] {
			a.OK, a.Error = false, "no such firm on this server"
		} else if err := c.Ops.ApplyCommand(ctx, cmd); err != nil {
			a.OK, a.Error = false, err.Error()
		}
		acks = append(acks, a)
	}
	c.mu.Lock()
	c.acks, c.delivered = append(c.acks, acks...), append(c.delivered, del...)
	c.mu.Unlock()
	return nil
}

func (c *ControlClient) fail(msg string) {
	c.mu.Lock()
	c.LastErr = msg
	c.mu.Unlock()
}
