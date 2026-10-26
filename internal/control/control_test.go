package control

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
	"ledgerline/internal/vault"
)

var now = time.Date(2026, 10, 20, 11, 0, 0, 0, IST)

func newC(t *testing.T) (*Control, context.Context) {
	t.Helper()
	c := New(&vault.Docs{Inner: docs.NewMem(), V: vault.New(vault.Ephemeral())})
	c.Now = func() time.Time { return now }
	ctx := context.Background()
	if _, err := c.Bootstrap(ctx, "owner", "Owner", "owner-password-1"); err != nil {
		t.Fatal(err)
	}
	return c, ctx
}

func firm(tenant, name string, staff, traded, kyc, bots int, ed string) FirmReport {
	return FirmReport{Tenant: tenant, Firm: name, SEBIReg: "INZ" + tenant, Edition: ed, Month: "2026-10", StaffActive: staff, TradedMonth: traded, KYCMonth: kyc, Bots: BotUse{Computers: bots}, Users: []UserInfo{}}
}

func TestBillingSlabsGSTAndNumbers(t *testing.T) {
	c, ctx := newC(t)
	in, _, _ := c.NewInstallation(ctx, "owner", "Hosted", "hosted")
	if _, err := c.Receive(ctx, in, Report{Firms: []FirmReport{firm("acme", "Acme", 12, 6000, 40, 2, "autopilot")}}, "1.2.3.4"); err != nil {
		t.Fatal(err)
	}
	fs, _ := c.Firms(ctx, "owner")
	cu, _, _ := docs.Get[Customer](ctx, c.Docs, T, cCustomer, fs[0].Customer)
	if cu.Status != "Trial" {
		t.Fatalf("new customer %+v", cu)
	}
	// Trial customers are not billed.
	if ivs, _ := c.Generate(ctx, "owner", "2026-10", ""); len(ivs) != 0 {
		t.Fatal("trial billed")
	}
	set, _ := c.Settings(ctx)
	set.Vendor = Vendor{Name: "Ledgerline Tech", Address: "Pune", GSTIN: "27AAACL1234A1Z5", Prefix: "LL", DueDays: 15}
	if _, err := c.SaveSettings(ctx, "owner", set); err != nil {
		t.Fatal(err)
	}
	cu.Status, cu.Address, cu.GSTIN, cu.DiscountPct = "Active", "Mumbai", "27AABCA1111B1Z1", "10"
	if _, err := c.SaveCustomer(ctx, "owner", cu); err != nil {
		t.Fatal(err)
	}
	ivs, err := c.Generate(ctx, "owner", "2026-10", "")
	if err != nil || len(ivs) != 1 {
		t.Fatalf("generate %v %d", err, len(ivs))
	}
	iv := ivs[0]
	want := map[string]money.Amount{
		"edition": money.MustAmount("35000"),
		"users":   money.MustAmount("1500"),  // 2 above the 10 included at 750
		"bots":    money.MustAmount("4000"),  // 2 × 2000
		"kyc":     money.MustAmount("1000"),  // 40 × 25
		"clients": money.MustAmount("51000"), // first 500 free, 4,500 × ₹10, 1,000 × ₹6
		"setup":   money.MustAmount("50000"),
	}
	got := map[string]money.Amount{}
	for _, l := range iv.Lines {
		got[l.Kind] += l.Amount
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got %s want %s", k, got[k], v)
		}
	}
	sub := money.MustAmount("142500")
	if iv.Subtotal != sub || iv.Discount != money.MustAmount("14250") || iv.Taxable != money.MustAmount("128250") {
		t.Fatalf("sums %s %s %s", iv.Subtotal, iv.Discount, iv.Taxable)
	}
	if iv.CGST+iv.SGST != money.MustAmount("23085") || iv.IGST != 0 || iv.Total != money.MustAmount("151335") {
		t.Fatalf("gst %s %s %s total %s", iv.CGST, iv.SGST, iv.IGST, iv.Total)
	}
	// Issuing numbers it in the financial year's series and bills the setup fee once.
	iv, err = c.IssueInvoice(ctx, "owner", iv.ID, "")
	if err != nil || iv.Number != "LL/26-27/0001" || iv.Due != "2026-11-04" {
		t.Fatalf("issue %v %+v", err, iv.Number)
	}
	if _, err := c.EditDraft(ctx, "owner", iv.ID, &InvoiceLine{Desc: "x", Qty: 1, Rate: 1}, -1, nil); err == nil {
		t.Fatal("changed an issued invoice")
	}
	pv, _ := c.Preview(ctx, "owner", "2026-11")
	for _, l := range pv[0].Invoice.Lines {
		if l.Kind == "setup" {
			t.Fatal("setup fee billed twice")
		}
	}
	// Another state: IGST.
	cu.GSTIN = "29AABCA1111B1Z1"
	c.SaveCustomer(ctx, "owner", cu)
	pv, _ = c.Preview(ctx, "owner", "2026-10")
	if pv[0].Invoice.IGST == 0 || pv[0].Invoice.CGST != 0 {
		t.Fatalf("inter-state %+v", pv[0].Invoice)
	}
	// Payments.
	if _, err := c.RecordPayment(ctx, "owner", iv.ID, Payment{Amount: iv.Total + 1}); err == nil {
		t.Fatal("overpaid")
	}
	iv, _ = c.RecordPayment(ctx, "owner", iv.ID, Payment{Amount: money.MustAmount("100000"), Date: "2026-10-25"})
	if iv.Status != "Part-paid" {
		t.Fatal(iv.Status)
	}
	iv, _ = c.RecordPayment(ctx, "owner", iv.ID, Payment{Amount: iv.Total - iv.Paid})
	if iv.Status != "Paid" {
		t.Fatal(iv.Status)
	}
	if _, err := c.VoidInvoice(ctx, "owner", iv.ID, "mistake"); err == nil {
		t.Fatal("voided a paid invoice")
	}
}

func TestRolesAndTickets(t *testing.T) {
	c, ctx := newC(t)
	if _, pw, err := c.SaveStaff(ctx, "owner", "asha", "Asha", RoleSupport, true, false); err != nil || pw == "" {
		t.Fatal(err)
	}
	if _, err := c.Preview(ctx, "asha", "2026-10"); err == nil {
		t.Fatal("support saw billing")
	}
	if _, _, err := c.NewInstallation(ctx, "asha", "x", "own"); err == nil {
		t.Fatal("support made an installation")
	}
	in, _, _ := c.NewInstallation(ctx, "owner", "Own", "own")
	rep := Report{Firms: []FirmReport{firm("b1", "Bee", 3, 10, 0, 0, "manual")}, Tickets: []TicketSync{{LocalID: "SR-0001", Tenant: "b1", User: "u1", UserName: "U One", Subject: "Help", Priority: "Urgent", Text: "Broken", MsgID: "m1"}}}
	reply, err := c.Receive(ctx, in, rep, "")
	if err != nil || reply.Received != 1 {
		t.Fatal(err)
	}
	// The firm is told its ticket number (queued during this report, sent with the next reply).
	reply, _ = c.Receive(ctx, in, Report{Firms: rep.Firms, Tickets: rep.Tickets}, "")
	if len(reply.Updates) != 1 || reply.Updates[0].Ticket != "T-1" {
		t.Fatalf("updates %+v", reply.Updates)
	}
	ts, _ := c.Tickets(ctx, "asha")
	if len(ts) != 1 || len(ts[0].Messages) != 2 {
		t.Fatalf("duplicate message kept: %+v", ts)
	}
	// Urgent: first reply due within an hour.
	c.Now = func() time.Time { return now.Add(2 * time.Hour) }
	if !c.Overdue(ts[0]) {
		t.Fatal("not overdue")
	}
	tk, err := c.ReplyTicket(ctx, "asha", ts[0].ID, "Looking", false, "")
	if err != nil || tk.Status != "Waiting on customer" || tk.Assignee != "asha" || c.Overdue(tk) {
		t.Fatalf("%v %+v", err, tk)
	}
	// Another server can't post to this firm's tickets.
	in2, _, _ := c.NewInstallation(ctx, "owner", "Other", "own")
	c.Receive(ctx, in2, Report{Tickets: []TicketSync{{LocalID: "SR-0001", Tenant: "b1", Text: "spoof", MsgID: "x"}}}, "")
	tk, _ = c.Ticket(ctx, "owner", ts[0].ID)
	for _, m := range tk.Messages {
		if m.Text == "spoof" {
			t.Fatal("another installation wrote to the ticket")
		}
	}
	if len(c.mustTickets(t, ctx)) != 1 {
		t.Fatal("spoofed ticket created")
	}
}

func (c *Control) mustTickets(t *testing.T, ctx context.Context) []Ticket {
	ts, err := c.Tickets(ctx, "owner")
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

func TestWordsAndFormat(t *testing.T) {
	for a, w := range map[string]string{
		"151335":     "Rupees One Lakh Fifty-One Thousand Three Hundred Thirty-Five Only",
		"0.50":       "Rupees Zero and Fifty Paise Only",
		"12345678.9": "Rupees One Crore Twenty-Three Lakh Forty-Five Thousand Six Hundred Seventy-Eight and Ninety Paise Only",
	} {
		if got := InWords(money.MustAmount(a)); got != w {
			t.Errorf("%s: %s", a, got)
		}
	}
	if inr(money.MustAmount("12345678.9")) != "1,23,45,678.90" || inr(money.MustAmount("999")) != "999.00" {
		t.Fatal(inr(money.MustAmount("12345678.9")))
	}
	if fy(time.Date(2027, 3, 31, 0, 0, 0, 0, IST)) != "26-27" || fy(time.Date(2027, 4, 1, 0, 0, 0, 0, IST)) != "27-28" {
		t.Fatal("fy")
	}
	if !validGSTIN("27AAACL1234A1Z5") || validGSTIN("27AAACL1234A1Z") {
		t.Fatal("gstin")
	}
}

func TestFileStoreKeepsDataEncrypted(t *testing.T) {
	dir := t.TempDir()
	kr := vault.Ephemeral()
	fs, err := OpenFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	c := New(&vault.Docs{Inner: fs, V: vault.New(kr)})
	ctx := context.Background()
	c.Bootstrap(ctx, "owner", "", "owner-password-1")
	c.SaveCustomer(ctx, "owner", Customer{Name: "Secretive Securities", Status: "Active"})
	c.SaveCustomer(ctx, "owner", Customer{ID: "CUS-0001", Name: "Secretive Securities Ltd", Status: "Active"})
	fs.Close()
	raw, _ := os.ReadFile(filepath.Join(dir, "control.journal"))
	if bytes.Contains(raw, []byte("Secretive")) {
		t.Fatal("customer name in clear on disk")
	}
	fs2, err := OpenFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer fs2.Close()
	c2 := New(&vault.Docs{Inner: fs2, V: vault.New(kr)})
	cs, _ := c2.Customers(ctx, "owner")
	if len(cs) != 1 || cs[0].Name != "Secretive Securities Ltd" {
		t.Fatalf("after restart %+v", cs)
	}
	raw2, _ := os.ReadFile(filepath.Join(dir, "control.journal"))
	if len(raw2) >= len(raw) {
		t.Fatal("journal not compacted")
	}
}

func TestHTTPSignInAndCSRF(t *testing.T) {
	c, _ := newC(t)
	c.Now = time.Now
	srv := httptest.NewServer(NewServer(c, nil))
	defer srv.Close()
	jar, _ := cookiejar.New(nil)
	cl := &http.Client{Jar: jar}
	do := func(method, path, csrf string, body any) (*http.Response, map[string]any) {
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, srv.URL+path, bytes.NewReader(b))
		if csrf != "" {
			req.Header.Set("X-CSRF", csrf)
		}
		r, err := cl.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		json.NewDecoder(r.Body).Decode(&m)
		r.Body.Close()
		return r, m
	}
	if r, _ := do("GET", "/api/overview", "", nil); r.StatusCode != 401 {
		t.Fatal("no sign-in needed")
	}
	if r, _ := do("POST", "/api/login", "", map[string]string{"id": "owner", "password": "nope"}); r.StatusCode != 403 {
		t.Fatal("wrong password accepted")
	}
	r, m := do("POST", "/api/login", "", map[string]string{"id": "owner", "password": "owner-password-1"})
	if r.StatusCode != 200 {
		t.Fatal(m)
	}
	csrf := m["csrf"].(string)
	if r, _ := do("GET", "/api/overview", "", nil); r.StatusCode != 200 {
		t.Fatal("overview", r.StatusCode)
	}
	if r, _ := do("POST", "/api/installations", "", map[string]string{"name": "x"}); r.StatusCode != 403 {
		t.Fatal("no CSRF accepted")
	}
	r, m = do("POST", "/api/installations", csrf, map[string]string{"name": "x", "kind": "own"})
	if r.StatusCode != 200 || !strings.HasPrefix(m["key"].(string), "llc_") {
		t.Fatal(m)
	}
	key := m["key"].(string)
	// Reports need the key.
	req, _ := http.NewRequest("POST", srv.URL+"/api/v1/report", strings.NewReader(`{"firms":[]}`))
	if r, _ := http.DefaultClient.Do(req); r.StatusCode != 401 {
		t.Fatal("report without key")
	}
	req, _ = http.NewRequest("POST", srv.URL+"/api/v1/report", strings.NewReader(`{"firms":[]}`))
	req.Header.Set("Authorization", "Bearer "+key)
	if r, _ := http.DefaultClient.Do(req); r.StatusCode != 200 {
		t.Fatal("report with key", r.StatusCode)
	}
	// A new person must set a password first.
	_, m = do("POST", "/api/staff", csrf, map[string]any{"id": "ben", "name": "Ben", "role": "Billing", "active": true})
	pw := m["password"].(string)
	do("POST", "/api/logout", csrf, nil)
	_, m = do("POST", "/api/login", "", map[string]string{"id": "ben", "password": pw})
	if m["must_reset"] != true {
		t.Fatal("no reset")
	}
	csrf = m["csrf"].(string)
	if r, _ := do("GET", "/api/customers", "", nil); r.StatusCode != 403 {
		t.Fatal("used before reset")
	}
	if r, m := do("POST", "/api/password", csrf, map[string]string{"old": pw, "new": "a-good-long-password"}); r.StatusCode != 200 {
		t.Fatal(m)
	}
	if r, _ := do("GET", "/api/customers", "", nil); r.StatusCode != 200 {
		t.Fatal("after reset")
	}
	// Static console is served.
	if r, _ := http.Get(srv.URL + "/control.js"); r.StatusCode != 200 {
		t.Fatal("console")
	}
}

func TestHalfPct(t *testing.T) {
	for in, want := range map[string]string{"18": "9", "20": "10", "5": "2.5", "": "9"} {
		if got := halfPct(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}
