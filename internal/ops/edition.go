package ops

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"ledgerline/internal/docs"
)

// ---- editions: how much of the work runs by itself ----
//
// The same software is sold in three editions, set per firm by the
// provider (not by the firm's own admin):
//
//   Manual          nothing runs by itself; every step is a person's click.
//                   Autopilot, the work queue and scheduled runs are off;
//                   jobs can still be run by hand.
//   Autopilot       routine work runs by itself where it is safe and asks
//                   a person whenever judgement or approval is needed
//                   (each automation at its usual setting).
//   Full autopilot  every automation runs at the most the rules allow;
//                   people act only where SEBI, the exchanges, depositories
//                   or the PMLA rules require a person.
//
// Every edition keeps the same rules: maker-checker, the caps on each
// automation, and the steps reserved for people never change.

const (
	EditionManual    = "manual"
	EditionAutopilot = "autopilot"
	EditionFull      = "full"
	cEdition         = "edition"
)

// EditionInfo describes an edition for the screens.
type EditionInfo struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	About string `json:"about"`
}

// Editions lists the editions in order.
var Editions = []EditionInfo{
	{EditionManual, "Manual", "Every step is done by a person. No automation, no work queue and no scheduled runs; jobs can be run by hand. Backups and the exchange holiday check still run."},
	{EditionAutopilot, "Autopilot", "Routine work runs by itself where it is safe; people are asked whenever judgement or approval is needed."},
	{EditionFull, "Full autopilot", "Everything runs automatically at the most the rules allow; people act only where the regulations require a person."},
}

// EditionName returns the display name of an edition.
func EditionName(id string) string {
	for _, e := range Editions {
		if e.ID == id {
			return e.Name
		}
	}
	return id
}

func validEdition(id string) bool {
	for _, e := range Editions {
		if e.ID == id {
			return true
		}
	}
	return false
}

// ParseEditions reads "firm:edition,firm:edition".
func ParseEditions(spec string) (map[string]string, error) {
	out := map[string]string{}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		t, e, ok := strings.Cut(part, ":")
		e = strings.ToLower(strings.TrimSpace(e))
		if !ok || strings.TrimSpace(t) == "" || !validEdition(e) {
			return nil, fmt.Errorf("editions must look like firm:manual, firm:autopilot or firm:full (got %q)", part)
		}
		out[strings.TrimSpace(t)] = e
	}
	return out, nil
}

// editionRecord remembers each firm's edition and since when, so a change
// is recorded in the audit trail and settings saved under an earlier
// edition can be told apart.
type editionRecord struct {
	Edition string `json:"edition"`
	Since   string `json:"since"`
}

type editionCache struct {
	mu   sync.Mutex
	seen map[string]editionRecord
}

// SetEdition sets a firm's edition (the provider's configuration, or the
// development switcher).
func (o *Ops) SetEdition(tenant, edition string) error {
	if !validEdition(edition) {
		return invalid("edition must be manual, autopilot or full")
	}
	o.edMu.Lock()
	defer o.edMu.Unlock()
	if o.EditionOf == nil {
		o.EditionOf = map[string]string{}
	}
	o.EditionOf[tenant] = edition
	return nil
}

func (o *Ops) configuredEdition(tenant string) string {
	o.edMu.Lock()
	defer o.edMu.Unlock()
	if e, ok := o.EditionOf[tenant]; ok {
		return e
	}
	if o.DefaultEdition != "" {
		return o.DefaultEdition
	}
	return EditionAutopilot
}

// Edition returns the firm's edition and when it started.
func (o *Ops) Edition(ctx context.Context, tenant string) (string, string) {
	ed := o.configuredEdition(tenant)
	o.edCache.mu.Lock()
	if o.edCache.seen == nil {
		o.edCache.seen = map[string]editionRecord{}
	}
	r, ok := o.edCache.seen[tenant]
	o.edCache.mu.Unlock()
	if ok && r.Edition == ed {
		return ed, r.Since
	}
	rec, _, err := docs.Get[editionRecord](ctx, o.Docs, tenant, cEdition, "current")
	if err != nil || rec.Edition != ed {
		prev := rec.Edition
		rec = editionRecord{Edition: ed, Since: o.now().Format(time.RFC3339Nano)}
		if docs.Upsert(ctx, o.Docs, tenant, cEdition, "current", rec, SystemActor) == nil {
			if prev != "" || err == nil {
				o.log(ctx, tenant, SystemActor, "edition.changed", "", map[string]any{"from": prev, "to": ed})
			}
			if ed == EditionFull {
				_ = o.setAutopilotJobs(ctx, tenant, true)
			}
		}
	}
	o.edCache.mu.Lock()
	o.edCache.seen[tenant] = rec
	o.edCache.mu.Unlock()
	return rec.Edition, rec.Since
}

func (o *Ops) manual(ctx context.Context, tenant string) bool {
	ed, _ := o.Edition(ctx, tenant)
	return ed == EditionManual
}

var errManualEdition = invalid("automation is not part of the Manual edition; every step is done by hand")

// EditionView is the firm's edition for the screens.
type EditionView struct {
	EditionInfo
	Since     string        `json:"since"`
	Editions  []EditionInfo `json:"editions"`
	HumanOnly []string      `json:"human_only"`
}

// EditionStatus describes the firm's edition.
func (o *Ops) EditionStatus(ctx context.Context, tenant string) EditionView {
	ed, since := o.Edition(ctx, tenant)
	v := EditionView{Since: since, Editions: Editions, HumanOnly: []string{}}
	for _, e := range Editions {
		if e.ID == ed {
			v.EditionInfo = e
		}
	}
	for _, h := range HumanOnly {
		v.HumanOnly = append(v.HumanOnly, h.Process)
	}
	return v
}

// savedBefore reports whether a setting saved at at predates since.
func savedBefore(at, since string) bool {
	a, err1 := time.Parse(time.RFC3339Nano, at)
	b, err2 := time.Parse(time.RFC3339Nano, since)
	if err1 != nil || err2 != nil {
		return true
	}
	return a.Before(b)
}
