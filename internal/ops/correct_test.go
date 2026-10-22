package ops

import (
	"strings"
	"testing"

	"ledgerline/internal/charges"
	"ledgerline/internal/money"
)

func TestCorrectBillAndReviseNote(t *testing.T) {
	o, ctx := setup(t)
	m, _, _ := o.Market(ctx, T, D)
	if _, err := o.App.RunBilling(ctx, T, "farhan.khan", D, m.SettlementPrices()); err != nil {
		t.Fatal(err)
	}
	if _, err := o.GenerateNotes(ctx, T, "priya.nair", D); err != nil {
		t.Fatal(err)
	}
	o.SendNotes(ctx, T, "priya.nair", D, "")
	ns, _ := o.Notes(ctx, T, D)
	var before ContractNote
	for _, n := range ns {
		if n.Status == "Sent" {
			before = n
			break
		}
	}
	u := before.UCC
	if _, err := o.SavePlan(ctx, T, "farhan.khan", PlanInput{ID: "waiver", Name: "Waiver", From: "2026-01-01", Rules: map[string]charges.BrokerageRule{
		"CM-DEL": {Mode: charges.PlanZero}, "CM-INTRA": {Mode: charges.PlanZero}, "FO-FUT": {Mode: charges.PlanZero}, "FO-OPT": {Mode: charges.PlanZero},
		"CD-FUT": {Mode: charges.PlanZero}, "CD-OPT": {Mode: charges.PlanZero}, "COM-FUT": {Mode: charges.PlanZero}, "COM-OPT": {Mode: charges.PlanZero}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.CorrectBill(ctx, T, "priya.nair", D, u, "x", "waiver", "day", true); err == nil {
		t.Fatal("a maker corrected a bill")
	}
	if _, err := o.CorrectBill(ctx, T, "farhan.khan", D, u, "", "waiver", "day", true); err == nil {
		t.Fatal("correction without a reason")
	}
	res, err := o.CorrectBill(ctx, T, "farhan.khan", D, u, "Brokerage waived as agreed with the client", "waiver", "day", true)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("posted %d reversed %d note %s replaces %s sent %s", len(res.Billing.Posted), len(res.Billing.Reversed), res.Note.No, res.Note.Replaces, res.Sent)
	for _, g := range res.Billing.Groups {
		if g.UCC != u {
			t.Fatalf("rebilled another client %s", g.UCC)
		}
		if g.Charges.Brokerage != 0 {
			t.Fatalf("brokerage not waived: %v", g.Charges.Brokerage)
		}
	}
	if len(res.Billing.Reversed) == 0 || len(res.Billing.Reversed) != len(res.Billing.Posted) {
		t.Fatalf("expected each old bill reversed and reposted: %+v", res.Billing)
	}
	if res.Note == nil || res.Note.Replaces != before.No || res.Note.No == before.No || res.Note.Revision != 1 || res.Sent != "Sent" {
		t.Fatalf("note: %+v", res.Note)
	}
	html, _ := o.RenderNote(ctx, T, D, u, "")
	if !strings.Contains(string(html), "REVISED CONTRACT NOTE") || !strings.Contains(string(html), before.No) {
		t.Fatal("revised note does not say what it replaces")
	}
	// Running it again changes nothing.
	if _, err := o.CorrectBill(ctx, T, "farhan.khan", D, u, "again", "", "", false); err == nil {
		t.Fatal("expected 'did not change'")
	}
	// The waiver was for one day: the next trading day is back on the old plan.
	if ep, _, _ := o.ClientBrokerage(ctx, T, u, "2026-10-19"); ep.PlanID == "waiver" {
		t.Fatalf("one-day waiver still in force: %+v", ep)
	}
	// Trial balance still agrees.
	if tb, _ := o.App.Ledger.TrialBalance(ctx, T, D); !tb.Balanced {
		t.Fatal("trial balance off")
	}
	_ = money.Dec{}
}
