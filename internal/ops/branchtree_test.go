package ops

import (
	"errors"
	"testing"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

func TestBranchTreeAndLimitRequests(t *testing.T) {
	o, ctx := setup(t)
	if err := o.seedSampleBrokerage(ctx, T, D); err != nil {
		t.Fatal(err)
	}
	if err := o.seedSamplePartners(ctx, T); err != nil {
		t.Fatal(err)
	}
	cs, _ := o.ClientMap(ctx, T)
	var blr, other string
	for _, u := range sortedKeys(cs) {
		switch {
		case cs[u].Branch == "Bengaluru" && blr == "":
			blr = u
		case cs[u].Branch != "Bengaluru" && cs[u].Branch != "" && other == "":
			other = u
		}
	}
	if blr == "" || other == "" {
		t.Skip("sample has no Bengaluru client")
	}
	// A zone head sees every office under the zone.
	if err := docs.Upsert(ctx, o.Docs, T, cUser, "zone.south", User{ID: "zone.south", Name: "South zone head", Role: RoleBranch, Active: true, Branch: "SOUTH"}, "test"); err != nil {
		t.Fatal(err)
	}
	f := o.ClientScope(ctx, T, "zone.south")
	if f == nil || !f(blr) || f(other) {
		t.Fatal("zone scope wrong")
	}
	// Mysuru franchise under Bengaluru: Bengaluru staff see its clients.
	c := cs[other]
	c.Branch = "Mysuru"
	_ = docs.Upsert(ctx, o.Docs, T, cClient, c.UCC, c, "test")
	if f := o.ClientScope(ctx, T, "meera.iyer"); !f(other) || !o.ClientScope(ctx, T, "zone.south")(other) {
		t.Fatal("franchise clients not visible up the tree")
	}
	if _, err := o.SaveBranch(ctx, T, "admin", Branch{Code: "SOUTH", Name: "South zone", Kind: "Zone", Parent: "Mysuru", Active: true}); err == nil {
		t.Fatal("cycle accepted")
	}
	if _, err := o.SaveBranch(ctx, T, "admin", Branch{Code: "X1", Name: "X", Kind: "Branch", Parent: "NOPE"}); err == nil {
		t.Fatal("missing parent accepted")
	}

	// Branch limit request: own client only; decided by head-office risk.
	c.Branch = "Delhi"
	_ = docs.Upsert(ctx, o.Docs, T, cClient, c.UCC, c, "test")
	if _, err := o.CreateOverride(ctx, T, "meera.iyer", D, other, money.MustDec("50000"), "Client adding funds"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("request for another branch's client: %v", err)
	}
	ov, err := o.CreateOverride(ctx, T, "meera.iyer", D, blr, money.MustDec("50000"), "Client's cheque deposited at branch")
	if err != nil || !ov.ByBranch || ov.Branch != "Bengaluru" {
		t.Fatalf("request: %+v %v", ov, err)
	}
	ws, _ := o.WorkItems(ctx, T, "Open")
	queued := false
	for _, w := range ws {
		queued = queued || (w.Process == "limit_requests" && w.Role == RoleRisk && w.UCC == blr)
	}
	if !queued {
		t.Fatal("request not queued for risk")
	}
	if _, err := o.DecideOverride(ctx, T, "farhan.khan", D, ov.ID, true); err == nil {
		t.Fatal("ops checker decided a branch request")
	}
	if ov, err = o.DecideOverride(ctx, T, "anita.rao", D, ov.ID, true); err != nil || ov.Status != "Approved" {
		t.Fatalf("decide: %+v %v", ov, err)
	}
	ws, _ = o.WorkItems(ctx, T, "Done")
	done := false
	for _, w := range ws {
		done = done || w.Process == "limit_requests"
	}
	if !done {
		t.Fatal("work item not closed")
	}
	rv, _ := o.Risk(ctx, T, D)
	for _, r := range rv.Rows {
		if r.UCC == blr && r.Overrides.Cmp(money.MustDec("50000")) != 0 {
			t.Fatalf("limit not raised: %s", r.Overrides)
		}
	}
}
