package docs

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
)

// Overlay reads through to a base store and keeps every write in memory,
// so work can be tried out against real data without changing it: a dry
// run of an import sees the firm's settings and users, and the documents
// earlier files of the same run would have written, and nothing reaches
// the base store.
type Overlay struct {
	Base Store
	top  *Mem
}

// NewOverlay starts an empty overlay on base.
func NewOverlay(base Store) *Overlay { return &Overlay{Base: base, top: NewMem()} }

func (o *Overlay) Get(ctx context.Context, t, c, id string) (Doc, error) {
	d, err := o.top.Get(ctx, t, c, id)
	if err == nil {
		return d, nil
	}
	return o.Base.Get(ctx, t, c, id)
}

func (o *Overlay) List(ctx context.Context, t, c, prefix string) ([]Doc, error) {
	base, err := o.Base.List(ctx, t, c, prefix)
	if err != nil {
		return nil, err
	}
	top, _ := o.top.List(ctx, t, c, prefix)
	if len(top) == 0 {
		return base, nil
	}
	by := map[string]Doc{}
	for _, d := range base {
		by[d.ID] = d
	}
	for _, d := range top {
		by[d.ID] = d
	}
	out := make([]Doc, 0, len(by))
	for _, d := range by {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (o *Overlay) Put(ctx context.Context, t, c, id string, body json.RawMessage, expect int64, upsert bool, actor string) (Doc, error) {
	if _, err := o.top.Get(ctx, t, c, id); errors.Is(err, ErrNotFound) {
		// Bring the base document up first so versions carry on from it.
		if d, err := o.Base.Get(ctx, t, c, id); err == nil {
			o.top.mu.Lock()
			o.top.docs[key(t, c, id)] = d
			o.top.mu.Unlock()
		}
	}
	return o.top.Put(ctx, t, c, id, body, expect, upsert, actor)
}
