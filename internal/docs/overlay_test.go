package docs

import (
	"context"
	"errors"
	"testing"
)

func TestOverlay(t *testing.T) {
	ctx := context.Background()
	base := NewMem()
	Upsert(ctx, base, "t", "c", "a", map[string]int{"n": 1}, "x")
	Upsert(ctx, base, "t", "c", "b", map[string]int{"n": 2}, "x")
	o := NewOverlay(base)
	// Reads go through; writes stay in the overlay.
	if _, err := Update(ctx, o, "t", "c", "a", "y", func(v *map[string]int) error { (*v)["n"] = 10; return nil }); err != nil {
		t.Fatal(err)
	}
	Upsert(ctx, o, "t", "c", "z", map[string]int{"n": 3}, "y")
	got, _, _ := Get[map[string]int](ctx, o, "t", "c", "a")
	was, _, _ := Get[map[string]int](ctx, base, "t", "c", "a")
	if got["n"] != 10 || was["n"] != 1 {
		t.Fatalf("overlay %v base %v", got, was)
	}
	if _, err := base.Get(ctx, "t", "c", "z"); !errors.Is(err, ErrNotFound) {
		t.Fatal("write reached the base")
	}
	l, _ := o.List(ctx, "t", "c", "")
	if len(l) != 3 || l[0].ID != "a" || l[2].ID != "z" {
		t.Fatalf("list %v", l)
	}
	// Versions carry on from the base, so optimistic writes behave.
	if _, err := Put(ctx, o, "t", "c", "b", map[string]int{"n": 5}, 1, "y"); err != nil {
		t.Fatalf("put at base version: %v", err)
	}
}
