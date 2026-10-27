package ops

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"ledgerline/internal/docs"
)

// ---- client groups (tags) ----

var validTag = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{0,23}$`)

// NormaliseTag upper-cases a tag and turns spaces into dashes.
func NormaliseTag(t string) string {
	return strings.ToUpper(strings.Join(strings.Fields(t), "-"))
}

// TagCount is a group with its size.
type TagCount struct {
	Tag     string `json:"tag"`
	Clients int    `json:"clients"`
}

// Tags lists every group in use with its client count.
func (o *Ops) Tags(ctx context.Context, tenant string) ([]TagCount, error) {
	cs, err := o.Clients(ctx, tenant)
	if err != nil {
		return nil, err
	}
	n := map[string]int{}
	for _, c := range cs {
		for _, t := range c.Tags {
			n[t]++
		}
	}
	// Groups that have a plan assigned exist even when empty.
	as, _ := docs.List[PlanAssignment](ctx, o.Docs, tenant, cBrokAssign, "")
	for _, a := range as {
		if t, ok := strings.CutPrefix(a.Target, "tag:"); ok {
			if _, seen := n[t]; !seen {
				n[t] = 0
			}
		}
	}
	out := []TagCount{}
	for _, t := range sortedKeys(n) {
		out = append(out, TagCount{t, n[t]})
	}
	return out, nil
}

// TagClients adds and removes groups on a set of clients.
func (o *Ops) TagClients(ctx context.Context, tenant, actor string, uccs, add, remove []string) (int, error) {
	if err := o.require(ctx, tenant, actor, "Changing client groups", RoleMaker, RoleChecker); err != nil {
		return 0, err
	}
	norm := func(in []string) ([]string, error) {
		var out []string
		for _, t := range in {
			t = NormaliseTag(t)
			if t == "" {
				continue
			}
			if !validTag.MatchString(t) {
				return nil, invalid("group %q: use letters, digits, - or _, up to 24 characters", t)
			}
			out = append(out, t)
		}
		return out, nil
	}
	a, err := norm(add)
	if err != nil {
		return 0, err
	}
	r, err := norm(remove)
	if err != nil {
		return 0, err
	}
	if len(a)+len(r) == 0 {
		return 0, invalid("name at least one group to add or remove")
	}
	if len(uccs) == 0 {
		return 0, invalid("choose at least one client")
	}
	n := 0
	for _, u := range uccs {
		u = strings.ToUpper(strings.TrimSpace(u))
		var before []string
		c, err := docs.Update(ctx, o.Docs, tenant, cClient, u, actor, func(c *Client) error {
			before = append([]string(nil), c.Tags...)
			set := map[string]bool{}
			for _, t := range c.Tags {
				set[t] = true
			}
			for _, t := range a {
				set[t] = true
			}
			for _, t := range r {
				delete(set, t)
			}
			c.Tags = sortedKeys(set)
			return nil
		})
		if errors.Is(err, docs.ErrNotFound) {
			return n, fmt.Errorf("%w: client %s", ErrNotFound, u)
		}
		if err != nil {
			return n, err
		}
		if strings.Join(before, ",") != strings.Join(c.Tags, ",") {
			o.log(ctx, tenant, actor, "client.groups", u, map[string]any{"from": before, "to": c.Tags})
			n++
		}
	}
	return n, nil
}
