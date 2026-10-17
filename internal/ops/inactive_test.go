package ops

import (
	"testing"

	"ledgerline/internal/docs"
)

func TestInactiveAccounts(t *testing.T) {
	o, ctx := setup(t)
	v, err := o.Dormancy(ctx, T, D)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("counts %v cutoff %s", v.Counts, v.Cutoff)
	if v.Cutoff != "2024-10-16" || v.Counts["Due"] == 0 || v.Counts["Approaching"] == 0 {
		t.Fatalf("expected due and approaching accounts: %v", v.Counts)
	}
	var due, active string
	for _, r := range v.Rows {
		if r.State == "Due" && due == "" {
			due = r.UCC
		}
		if r.State == "Active" && active == "" {
			active = r.UCC
		}
	}
	if _, err := o.FlagInactive(ctx, T, "priya.nair", D, []string{due}); err == nil {
		t.Fatal("a maker flagged an account")
	}
	if _, err := o.FlagInactive(ctx, T, "farhan.khan", D, []string{active}); err == nil {
		t.Fatal("an active account was flagged")
	}
	if n, err := o.FlagInactive(ctx, T, "farhan.khan", D, []string{due}); err != nil || n != 1 {
		t.Fatalf("flag: %d %v", n, err)
	}
	// Flagged: limit nil, in today's UCC batch, out of the funds report if nil balances.
	rv, _ := o.Risk(ctx, T, D)
	for _, r := range rv.Rows {
		if r.UCC == due && (!r.Inactive || r.Limit.Sign() != 0) {
			t.Fatalf("inactive client still has a limit: %+v", r)
		}
	}
	rows, _ := o.ReportRows(ctx, T, D, "ucc")
	found := false
	for _, r := range rows {
		found = found || (r[0] == due && r[7] == "Inactive")
	}
	if !found {
		t.Fatal("inactive flag missing from the UCC batch")
	}
	// Reactivation: KRA must be validated; maker cannot approve their own.
	req := Reactivation{UCC: due, IPVMode: "VIPV", IPVDate: D, KRAStatus: "On hold"}
	if _, err := o.RequestReactivation(ctx, T, "priya.nair", D, req); err == nil {
		t.Fatal("reactivation with KRA on hold accepted")
	}
	req.KRAStatus, req.DetailsChanged, req.Mobile = "Validated", true, "9811122233"
	r, err := o.RequestReactivation(ctx, T, "priya.nair", D, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.DecideReactivation(ctx, T, "priya.nair", D, r.ID, true, ""); err == nil {
		t.Fatal("maker approved own reactivation")
	}
	if _, err := o.DecideReactivation(ctx, T, "farhan.khan", D, r.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	c, _, _ := docs.Get[Client](ctx, o.Docs, T, cClient, due)
	if c.Inactive || c.ReactivatedOn != D || c.Mobile != "9811122233" {
		t.Fatalf("after reactivation: %+v", c)
	}
	v, _ = o.Dormancy(ctx, T, D)
	for _, r := range v.Rows {
		if r.UCC == due && (r.State != "Active" || r.InactiveFrom != "2028-10-16") {
			t.Fatalf("clock did not restart: %+v", r)
		}
	}
}
