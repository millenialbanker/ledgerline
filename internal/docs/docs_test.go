package docs

import (
	"context"
	"errors"
	"testing"
)

type client struct {
	UCC  string `json:"ucc"`
	Name string `json:"name"`
}

func TestVersioning(t *testing.T) {
	ctx := context.Background()
	s := NewMem()
	if _, err := Put(ctx, s, "t1", "client", "BH1", client{"BH1", "A"}, 0, "ops"); err != nil {
		t.Fatal(err)
	}
	if _, err := Put(ctx, s, "t1", "client", "BH1", client{"BH1", "B"}, 0, "ops"); !errors.Is(err, ErrStale) {
		t.Fatalf("create twice: %v", err)
	}
	v, ver, _ := Get[client](ctx, s, "t1", "client", "BH1")
	if v.Name != "A" || ver != 1 {
		t.Fatalf("%+v %d", v, ver)
	}
	if _, err := Put(ctx, s, "t1", "client", "BH1", client{"BH1", "C"}, 1, "ops"); err != nil {
		t.Fatal(err)
	}
	if _, err := Put(ctx, s, "t1", "client", "BH1", client{"BH1", "D"}, 1, "ops"); !errors.Is(err, ErrStale) {
		t.Fatalf("stale write accepted: %v", err)
	}
	got, err := Update(ctx, s, "t1", "client", "BH1", "ops", func(c *client) error { c.Name += "!"; return nil })
	if err != nil || got.Name != "C!" {
		t.Fatalf("update %+v %v", got, err)
	}
	if _, _, err := Get[client](ctx, s, "t2", "client", "BH1"); !errors.Is(err, ErrNotFound) {
		t.Fatal("tenant isolation broken")
	}
	Upsert(ctx, s, "t1", "client", "BH2", client{"BH2", "Z"}, "ops")
	l, _ := List[client](ctx, s, "t1", "client", "")
	if len(l) != 2 || l[0].UCC != "BH1" {
		t.Fatalf("list %+v", l)
	}
	l, _ = List[client](ctx, s, "t1", "client", "BH2")
	if len(l) != 1 {
		t.Fatalf("prefix list %+v", l)
	}
}
