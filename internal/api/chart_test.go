package api

import (
	neturl "net/url"
	"testing"
)

// TestChartRoutesAndRoles: the chart of accounts routes follow the books
// screen for roles a firm makes, and the built-in roles' checks: a maker
// searches and makes a pending account; only a checker changes heads, and
// a second checker approves.
func TestChartRoutesAndRoles(t *testing.T) {
	h := newRolesHarness(t)
	h.approve("/v1/roles", `{"role":{"name":"Books viewer","about":"Reads the books","screens":{"books":{"view":true},"dashboard":{"view":true}},"limits":{"scope":"firm","brokerage":true}}}`)
	h.approve("/v1/users/lata.crm/roles", `{"roles":["Books viewer"]}`)
	if c, v := h.call("GET", "/v1/accounts/search?q=gst&date=2026-10-16", "lata.crm", ""); c != 200 || v["hits"] == nil {
		t.Fatalf("viewer search: %d %v", c, v)
	}
	if c, _ := h.call("GET", "/v1/account-heads", "lata.crm", ""); c != 200 {
		t.Fatalf("viewer heads: %d", c)
	}
	if c, _ := h.call("GET", "/v1/accounts/trial-balance?as_of=2026-10-16", "lata.crm", ""); c != 200 {
		t.Fatalf("viewer trial balance: %d", c)
	}
	for _, p := range []string{"/v1/accounts/new", "/v1/account-heads"} {
		if c, _ := h.call("POST", p, "lata.crm", `{"name":"X","head":"E-OTHER"}`); c != 403 {
			t.Errorf("viewer POST %s: %d", p, c)
		}
	}
	// Built-in roles.
	c, a := h.call("POST", "/v1/accounts/new", "priya.nair", `{"name":"Courier charges","head":"E-OTHER","alias":"cour"}`)
	if c != 201 || a["status"] != "Pending" || a["head_name"] != "Other expenses" {
		t.Fatalf("maker new account: %d %v", c, a)
	}
	code := neturl.PathEscape(a["code"].(string))
	if c, _ := h.call("POST", "/v1/account-heads", "priya.nair", `{"name":"Branch rent","parent":"E-RENT"}`); c != 403 {
		t.Fatalf("maker added a head: %d", c)
	}
	if c, v := h.call("POST", "/v1/account-heads", "farhan.khan", `{"name":"Branch rent","parent":"E-RENT"}`); c != 200 || v["level1"] != "Expenditure" {
		t.Fatalf("checker head: %d %v", c, v)
	}
	if c, _ := h.call("POST", "/v1/accounts/"+code+"/decide", "priya.nair", `{"action":"approve"}`); c != 403 {
		t.Fatalf("maker approved an account: %d", c)
	}
	if c, v := h.call("POST", "/v1/accounts/"+code+"/decide", "farhan.khan", `{"action":"approve"}`); c != 200 || v["status"] != "Active" {
		t.Fatalf("checker approves: %d %v", c, v)
	}
	c, mv := h.call("POST", "/v1/accounts/"+code+"/move", "priya.nair", `{"head":"E-RENT","reason":"it is office running cost"}`)
	if c != 200 || mv["status"] != "Pending" {
		t.Fatalf("move: %d %v", c, mv)
	}
	if c, _ := h.call("POST", "/v1/account-moves/"+mv["id"].(string)+"/approve", "priya.nair", `{}`); c != 403 {
		t.Fatalf("maker approved a move: %d", c)
	}
	if c, v := h.call("POST", "/v1/account-moves/"+mv["id"].(string)+"/approve", "farhan.khan", `{}`); c != 200 || v["status"] != "Approved" {
		t.Fatalf("approve move: %d %v", c, v)
	}
	if c, v := h.call("GET", "/v1/accounts/search?q=cour&date=2026-10-16", "farhan.khan", ""); c != 200 || len(v["hits"].([]any)) == 0 || v["hits"].([]any)[0].(map[string]any)["head"] != "Rent and office" {
		t.Fatalf("after move: %d %v", c, v)
	}
}
