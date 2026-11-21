package screens

import (
	"os"
	"strings"
	"testing"
)

func TestCatalogue(t *testing.T) {
	if len(All()) < 80 {
		t.Fatalf("only %d screens", len(All()))
	}
	for _, k := range []string{"dashboard", "crm", "brokerage", "money", "roles", "users", "connections"} {
		if _, ok := Get(k); !ok {
			t.Errorf("%s missing", k)
		}
	}
	if !IsAdmin("connections") || !IsAdmin("roles") || IsAdmin("crm") {
		t.Fatal("admin screens")
	}
	if Name("bank") != "Receipts & payouts · Bank reconciliation" || Name("crm") != "Leads & CRM" {
		t.Fatalf("names %q %q", Name("bank"), Name("crm"))
	}
	js := string(JS())
	if !strings.Contains(js, "const GROUPS = {") || !strings.Contains(js, `["money","Receipts & payouts"]`) || !strings.Contains(js, `["@usersg"]`) {
		t.Fatal(js[:400])
	}
	if p := os.Getenv("LL_SCREENS_JS"); p != "" {
		os.WriteFile(p, JS(), 0o644)
	}
}
