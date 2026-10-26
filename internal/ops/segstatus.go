package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
)

// Each exchange segment of a client has its own status, as in the
// exchanges' UCC database: a client can be active in cash, suspended in
// derivatives (for example when income proof lapses) and dormant in
// currency. Every change is kept with its effective date, reason and who
// made it, and the day's changes go to the exchanges with the UCC upload.

// Segment statuses.
const (
	SegActive      = "Active"
	SegReactivated = "Reactivated"
	SegSuspended   = "Suspended"
	SegDormant     = "Dormant"
	SegClosed      = "Closed"
)

// SegmentStatuses lists the statuses in display order.
var SegmentStatuses = []string{SegActive, SegReactivated, SegSuspended, SegDormant, SegClosed}

// SegmentState is a segment's current status.
type SegmentState struct {
	Status string `json:"status"`
	Since  string `json:"since"`
	Reason string `json:"reason,omitempty"`
}

// SegmentChange is one change of a segment's status.
type SegmentChange struct {
	Segment string `json:"segment"`
	From    string `json:"from"`
	To      string `json:"to"`
	On      string `json:"on"` // effective date
	Reason  string `json:"reason,omitempty"`
	By      string `json:"by"`
	At      string `json:"at"`
	Source  string `json:"source,omitempty"` // Ledgerline, Import
}

// SegmentStatus is the status of one segment: Active for a registered
// segment with no status recorded, "Not registered" otherwise.
func (c Client) SegmentStatus(seg string) string {
	if st, ok := c.SegStatus[seg]; ok && st.Status != "" {
		return st.Status
	}
	for _, s := range c.Segments {
		if s == seg {
			return SegActive
		}
	}
	return "Not registered"
}

// SegmentOpen reports whether the client may take new positions in seg.
func (c Client) SegmentOpen(seg string) bool {
	st := c.SegmentStatus(seg)
	return st == SegActive || st == SegReactivated
}

// OpenSegments lists the segments the client may trade in.
func (c Client) OpenSegments() []string {
	var out []string
	for _, s := range c.Segments {
		if c.SegmentOpen(s) {
			out = append(out, s)
		}
	}
	return out
}

func validSegStatus(s string) (string, bool) {
	for _, x := range SegmentStatuses {
		if strings.EqualFold(x, strings.TrimSpace(s)) {
			return x, true
		}
	}
	return "", false
}

// applySegmentStatus records a change on the client (no save).
func applySegmentStatus(c *Client, seg, status, on, reason, by, source string, now time.Time) error {
	seg = strings.ToUpper(strings.TrimSpace(seg))
	if SegmentNames[seg] == "" {
		return invalid("unknown segment %q", seg)
	}
	st, ok := validSegStatus(status)
	if !ok {
		return invalid("status must be one of %s", strings.Join(SegmentStatuses, ", "))
	}
	if !validDate(on) {
		return invalid("give the effective date (YYYY-MM-DD)")
	}
	from := c.SegmentStatus(seg)
	if from == st {
		return nil
	}
	if from == "Not registered" && st != SegActive && source != "Import" {
		return invalid("%s is not one of the client's segments", seg)
	}
	if from == SegClosed && st != SegReactivated && st != SegActive {
		return invalid("a closed segment can only be reopened")
	}
	if c.SegStatus == nil {
		c.SegStatus = map[string]SegmentState{}
	}
	c.SegStatus[seg] = SegmentState{Status: st, Since: on, Reason: reason}
	c.SegHistory = append(c.SegHistory, SegmentChange{Segment: seg, From: from, To: st, On: on, Reason: reason, By: by, At: now.Format(time.RFC3339), Source: source})
	if from == "Not registered" {
		c.Segments = append(c.Segments, seg)
		sort.Slice(c.Segments, func(i, j int) bool { return segOrder(c.Segments[i]) < segOrder(c.Segments[j]) })
	}
	return nil
}

// SetSegmentStatus changes one segment's status for a client.
func (o *Ops) SetSegmentStatus(ctx context.Context, tenant, actor, ucc, seg, status, on, reason string) (Client, error) {
	if err := o.require(ctx, tenant, actor, "Changing a segment's status", RoleChecker, RoleCompliance); err != nil {
		return Client{}, err
	}
	reason = strings.TrimSpace(reason)
	if len(reason) < 4 {
		return Client{}, invalid("give the reason for the change")
	}
	var change SegmentChange
	c, err := docs.Update(ctx, o.Docs, tenant, cClient, strings.ToUpper(ucc), actor, func(c *Client) error {
		n := len(c.SegHistory)
		if err := applySegmentStatus(c, seg, status, on, reason, actor, "Ledgerline", o.now()); err != nil {
			return err
		}
		if len(c.SegHistory) == n {
			return invalid("%s is already %s", strings.ToUpper(seg), c.SegmentStatus(strings.ToUpper(seg)))
		}
		change = c.SegHistory[n]
		return nil
	})
	if err != nil {
		return c, err
	}
	o.log(ctx, tenant, actor, "client.segment_status", c.UCC, map[string]any{"segment": change.Segment, "from": change.From, "to": change.To, "on": change.On, "reason": reason})
	return c, nil
}

// segmentChangesOn lists the changes effective on a date, for the UCC upload.
func segmentChangesOn(c Client, date string) []SegmentChange {
	var out []SegmentChange
	for _, h := range c.SegHistory {
		if h.On == date {
			out = append(out, h)
		}
	}
	return out
}

// SegmentStatusRow is one client on the segment status report.
type SegmentStatusRow struct {
	UCC    string            `json:"ucc"`
	Name   string            `json:"name"`
	Status map[string]string `json:"status"`
	Last   *SegmentChange    `json:"last,omitempty"`
}

// SegmentStatusReport is every client's status in every segment, with the
// counts per segment.
func (o *Ops) SegmentStatusReport(ctx context.Context, tenant, actor string) ([]SegmentStatusRow, map[string]map[string]int, error) {
	if err := o.require(ctx, tenant, actor, "Viewing segment status", RoleMaker, RoleChecker, RoleCompliance, RoleRisk); err != nil {
		return nil, nil, err
	}
	cs, err := o.Clients(ctx, tenant)
	if err != nil {
		return nil, nil, err
	}
	counts := map[string]map[string]int{}
	var out []SegmentStatusRow
	for _, c := range cs {
		r := SegmentStatusRow{UCC: c.UCC, Name: c.Name, Status: map[string]string{}}
		for _, s := range []string{"CM", "FO", "CD", "COM"} {
			st := c.SegmentStatus(s)
			if st == "Not registered" {
				continue
			}
			r.Status[s] = st
			if counts[s] == nil {
				counts[s] = map[string]int{}
			}
			counts[s][st]++
		}
		if n := len(c.SegHistory); n > 0 {
			h := c.SegHistory[n-1]
			r.Last = &h
		}
		out = append(out, r)
	}
	return out, counts, nil
}

// segStatusSummary is a short "CM Active · FO Suspended" for lists.
func segStatusSummary(c Client) string {
	var p []string
	for _, s := range c.Segments {
		p = append(p, fmt.Sprintf("%s %s", s, c.SegmentStatus(s)))
	}
	return strings.Join(p, " · ")
}

// prepareSegmentStatus loads a SEGMENT-STATUS file: each row a change,
// applied in date order per client.
func (o *Ops) prepareSegmentStatus(ctx context.Context, tenant, actor string, rows []row) (func() (int, int, error), []ingest.ParseError) {
	var errs []ingest.ParseError
	type ch struct {
		line                     int
		ucc, seg, st, on, reason string
	}
	var all []ch
	cs, _ := o.ClientMap(ctx, tenant)
	for _, r := range rows {
		c := ch{r.line, strings.ToUpper(r.get("UCC")), strings.ToUpper(r.get("Segment")), r.get("Status"), r.get("EffectiveDate"), r.get("Reason")}
		if _, ok := cs[c.ucc]; !ok {
			errs = append(errs, ingest.ParseError{Line: r.line, Field: "UCC", Msg: c.ucc + " is not a client"})
			continue
		}
		if SegmentNames[c.seg] == "" {
			errs = append(errs, ingest.ParseError{Line: r.line, Field: "Segment", Msg: "must be CM, FO, CD or COM"})
			continue
		}
		if _, ok := validSegStatus(c.st); !ok {
			errs = append(errs, ingest.ParseError{Line: r.line, Field: "Status", Msg: "must be one of " + strings.Join(SegmentStatuses, ", ")})
			continue
		}
		if !validDate(c.on) {
			errs = append(errs, ingest.ParseError{Line: r.line, Field: "EffectiveDate", Msg: "must be YYYY-MM-DD"})
			continue
		}
		all = append(all, c)
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].ucc != all[j].ucc {
			return all[i].ucc < all[j].ucc
		}
		return all[i].on < all[j].on
	})
	return func() (int, int, error) {
		n, skipped := 0, 0
		for i := 0; i < len(all); {
			j := i
			for j < len(all) && all[j].ucc == all[i].ucc {
				j++
			}
			batch := all[i:j]
			if _, err := docs.Update(ctx, o.Docs, tenant, cClient, batch[0].ucc, actor, func(c *Client) error {
				for _, x := range batch {
					before := len(c.SegHistory)
					if err := applySegmentStatus(c, x.seg, x.st, x.on, firstOf(x.reason, "imported"), actor, "Import", o.now()); err != nil {
						skipped++
						continue
					}
					n += len(c.SegHistory) - before
				}
				return nil
			}); err != nil {
				return n, skipped, err
			}
			i = j
		}
		o.log(ctx, tenant, actor, "segment_status.loaded", "", map[string]any{"changes": n, "skipped": skipped})
		return n, skipped, nil
	}, errs
}
