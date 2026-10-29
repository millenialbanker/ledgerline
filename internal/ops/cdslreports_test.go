package ops

import (
	"strings"
	"testing"

	"ledgerline/internal/docs"
)

// The three CDSL review reports become alerts once each, matched to the
// client by demat account. Sample lines follow the layout of real files,
// with made-up values.
func TestCDSLReportsRaiseAlerts(t *testing.T) {
	o, ctx := setup(t)
	cs, _ := o.Clients(ctx, T)
	ucc := cs[0].UCC
	docs.Update(ctx, o.Docs, T, cClient, ucc, "t", func(c *Client) error { c.Demat = Demat{Depository: "CDSL", ID: "1204320000000496"}; return nil })
	crlf := func(l ...string) []byte { return []byte(strings.Join(l, "\r\n") + "\r\n") }
	files := map[string][]byte{
		"11DPF7UX": crlf("1204320000000496~TEST CLIENT  .~43200~TEST SECURITIES PRIVATE LIMITED~INE000A01010~TEST LIMITED - EQUITY SHARES~10~EP-DR Txn:12345678 CtBo:1100001100012345 1234567890123~16102026"),
		"11DPG1U.19102026": crlf("01~43200~TEST SECURITIES PRIVATE LIMITED~19102026", "02~EARLYPAYIN",
			"03~00000001~00000002~TEST CLIENT  .~12043200~00000496~INE000A01010~TEST LIMITED # EQUITY SHARES~00000003~~2000.000~1204320000000496", "04~2000.000"),
		"08DPD1UX": crlf("BOID 1204320000000496", "1~~C1248~BO FROZEN FOR FLAG~0~1"),
	}
	for name, b := range files {
		res, err := o.LoadExchangeFile(ctx, T, "priya.nair", name, b, true)
		if err != nil || len(res) != 1 || res[0].Added != 1 || res[0].Problems != 0 {
			t.Fatalf("%s: %+v %v", name, res, err)
		}
		// Loading the same file again raises nothing new.
		if res, _ := o.LoadExchangeFile(ctx, T, "priya.nair", name, b, true); res[0].Added != 0 {
			t.Fatalf("%s reloaded %+v", name, res)
		}
	}
	as, _ := o.Alerts(ctx, T)
	got := map[string]Alert{}
	for _, a := range as {
		if a.Source == "CDSL" {
			got[strings.SplitN(a.Type, " (", 2)[0]] = a
		}
	}
	if a := got["Debit in a dormant demat account"]; a.UCC != ucc || a.Date != "2026-10-16" || !strings.Contains(a.Detail, "INE000A01010") {
		t.Fatalf("dormant %+v", a)
	}
	if a := got["High-value demat transaction"]; a.UCC != ucc || a.Date != "2026-10-19" || !strings.Contains(a.Type, "EARLYPAYIN") {
		t.Fatalf("high value %+v", a)
	}
	if a := got["Demat account changed at CDSL: BO FROZEN FOR FLAG"]; a.UCC != ucc || a.Severity != "High" {
		t.Fatalf("modification %+v", got)
	}
}
