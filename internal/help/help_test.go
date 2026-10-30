package help

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"ledgerline"
)

func kb(t *testing.T) *KB {
	t.Helper()
	k, err := Load(ledgerline.FS, "config/help")
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// consoleViews reads the view IDs from the console, so links to screens
// that do not exist fail here rather than in front of a user.
func consoleViews(t *testing.T) map[string]bool {
	t.Helper()
	b, err := os.ReadFile("../../web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	nav := string(b)[strings.Index(string(b), "const GROUPS = {"):]
	nav = nav[:strings.Index(nav, "const clientUser")]
	out := map[string]bool{}
	for _, m := range regexp.MustCompile(`\['([a-z]+)', '`).FindAllStringSubmatch(nav, -1) {
		out[m[1]] = true
	}
	return out
}

var linkRe = regexp.MustCompile(`\[\[([a-z]+)\|[^\]]+\]\]`)

func TestArticlesAreConsistent(t *testing.T) {
	k := kb(t)
	views := consoleViews(t)
	if len(k.Articles) < 80 {
		t.Fatalf("only %d articles", len(k.Articles))
	}
	checks := map[string]bool{"": true, "eod": true, "client": true, "breaks": true, "work": true}
	for _, a := range k.Articles {
		if a.Screen != "" && !views[a.Screen] {
			t.Errorf("%s: screen %q is not a console view", a.ID, a.Screen)
		}
		for _, m := range linkRe.FindAllStringSubmatch(a.Body, -1) {
			if !views[m[1]] {
				t.Errorf("%s: link to unknown screen %q", a.ID, m[1])
			}
		}
		for _, r := range a.Related {
			if _, ok := k.Get(r); !ok {
				t.Errorf("%s: related article %q does not exist", a.ID, r)
			}
		}
		if !checks[a.Check] {
			t.Errorf("%s: unknown check %q", a.ID, a.Check)
		}
		if len(a.Asks) == 0 {
			t.Errorf("%s: no ask phrases", a.ID)
		}
	}
}

// TestPermissionsMatchCode re-reads every permission check in internal/ops
// and requires config/help/permissions.md to say the same. Run with
// LL_UPDATE_HELP=1 to rewrite the file.
func TestPermissionsMatchCode(t *testing.T) {
	roleNames := map[string]string{"RoleMaker": "Ops maker", "RoleChecker": "Ops checker", "RoleRisk": "Risk manager", "RoleCompliance": "Compliance officer", "RoleAdmin": "Admin", "RoleBranch": "Branch staff", "RoleAP": "Authorised person"}
	order := []string{"Ops maker", "Ops checker", "Risk manager", "Compliance officer", "Branch staff", "Authorised person", "Admin"}
	re := regexp.MustCompile(`o\.require\(ctx, tenant, actor, "([^"]+)"((?:, Role[A-Za-z]+)*)\)`)
	files, _ := filepath.Glob("../ops/*.go")
	want := map[string]map[string]bool{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, _ := os.ReadFile(f)
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			what := strings.ToUpper(m[1][:1]) + m[1][1:]
			if want[what] == nil {
				want[what] = map[string]bool{}
			}
			for _, r := range strings.Split(m[2], ",") {
				if r = strings.TrimSpace(r); r != "" {
					want[what][roleNames[r]] = true
				}
			}
		}
	}
	var lines []string
	for what, rs := range want {
		var l []string
		for _, r := range order {
			if rs[r] {
				l = append(l, r)
			}
		}
		if len(l) == 0 {
			l = []string{"Admin"}
		}
		lines = append(lines, what+" | "+strings.Join(l, ", "))
	}
	sort.Strings(lines)
	body := "# Who may do what. Generated from the permission checks in internal/ops;\n# TestPermissionsMatchCode fails when they differ (LL_UPDATE_HELP=1 rewrites this file).\n# Admin may do everything. Format: What | Roles\n\n" + strings.Join(lines, "\n") + "\n"
	path := "../../config/help/permissions.md"
	if os.Getenv("LL_UPDATE_HELP") != "" {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := os.ReadFile(path)
	if string(got) != body {
		t.Fatal("config/help/permissions.md is out of date with the permission checks in internal/ops; run: LL_UPDATE_HELP=1 go test ./internal/help")
	}
	if len(kb(t).Perms) != len(lines) {
		t.Fatal("permissions not loaded")
	}
}

// TestQuestions asks in the words staff use and requires the right article
// first.
func TestQuestions(t *testing.T) {
	k := kb(t)
	cases := []struct{ q, view, want string }{
		// The examples from the brief.
		{"how to change the brokerage", "", "change-brokerage"},
		{"how do I add an AP", "", "add-ap"},
		{"how to deal with ucc mismatch", "", "break-ucc-mismatch"},
		{"ucc mismatch break in recon", "recon", "break-ucc-mismatch"},
		// Brokerage.
		{"client wants lower brokerage on delivery", "", "change-brokerage"},
		{"set brokerage for HNI group", "", "change-brokerage"},
		{"create a per lot brokerage plan for options", "", "brokerage-plans"},
		{"client says he was charged wrong brokerage yesterday", "", "correct-bill"},
		// Partners.
		{"register a new sub broker", "", "add-ap"},
		{"move client to a different AP", "", "map-client"},
		{"give the AP a login", "", "ap-login"},
		{"how do we pay AP commission and TDS", "", "ap-payout"},
		{"open a new branch office", "", "add-branch"},
		{"dealer nism certificate expired", "", "dealers-nism"},
		{"who uses terminal 40001", "", "dealers-nism"},
		{"bse dealer registration file", "", "dealers-nism"},
		// CDSL APIs.
		{"open demat account through cdsl api", "", "cdsl-demat-api"},
		{"bo id not received from cdsl", "", "cdsl-demat-api"},
		{"how many demat accounts for this pan", "", "cdsl-demat-api"},
		{"add off market beneficiary", "", "cdsl-demat-api"},
		// Day.
		{"why can't I close the day", "", "eod-close"},
		{"what is the daily process", "", "daily-routine"},
		{"exchange file got rejected", "files", "file-rejected"},
		{"trade punched in wrong client code", "", "ccm|break-ucc-mismatch"},
		{"quantity mismatch between exchange and oms", "", "break-qty-mismatch"},
		{"algo id missing on trades", "", "break-algo-id-missing"},
		{"trade on exchange not in our oms", "", "break-missing-in-oms"},
		{"contract note bounced", "", "contract-notes"},
		{"the books are locked up to 2026-09-30", "", "period-locked"},
		// Clients.
		{"exchange rejected the UCC upload", "", "ucc-rejected"},
		{"client stuck in KYC verification", "", "activate-client"},
		{"client changed his mobile number", "", "change-contact"},
		{"change client bank account", "", "change-contact"},
		{"client forgot portal password", "", "portal-access"},
		{"reactivate a dormant account", "", "inactive"},
		{"kra status on hold", "", "kra-status"},
		{"see the full pan number", "", "reveal-pan"},
		{"why is client blocked", "", "client-cant-trade"},
		{"review an online ekyc application", "", "online-opening"},
		// Money and risk.
		{"money came from client's wife's account", "", "third-party-funds"},
		{"unknown credit in bank statement", "", "unknown-credit"},
		{"cheque bounced", "", "cheque-return"},
		{"payout says only 5000 available", "", "payout"},
		{"client in margin shortfall", "", "margin-shortfall"},
		{"peak margin penalty why", "", "peak-margin"},
		{"branch wants extra limit for client", "", "limit-request"},
		{"accept pledge request", "", "pledge"},
		{"interest on debit balance", "", "debit-interest"},
		// Compliance and admin.
		{"file an STR", "", "surveillance"},
		{"SCORES complaint", "", "complaints"},
		{"add a new user", "", "add-user"},
		{"user locked out", "", "reset-password"},
		{"lost phone authenticator", "", "reset-2fa"},
		{"Approving payouts needs Ops checker or Risk manager (you are Ops maker)", "", "permission-denied"},
		{"whatsapp messages not going to client", "", "whatsapp-consent"},
		{"edit sms template dlt", "", "message-templates"},
		{"stock split holdings", "", "corporate-actions"},
		{"enter a DIS slip", "", "dis"},
		{"tender shares in buyback", "", "buyback"},
		{"what is in my work queue", "", "work-queue"},
		{"is our data stored in India", "", "data-in-india"},
		{"our ai credit ran out", "", "ai-credit"},
		{"how do I add a widget to my dashboard", "", "customize-dashboard"},
		{"did last night's backup fail", "", "backups"},
		{"how do I close MCX after the session", "", "eod-close"},
		{"how do I load next year's NSE holiday list", "", "exchange-holidays"},
		{"pay-in date is wrong because of a bank holiday", "", "exchange-holidays"},
		{"why is the autopilot screen missing", "", "editions"},
		{"what does full autopilot change", "", "editions"},
	}
	fails := 0
	for _, c := range cases {
		h := k.Search(c.q, c.view, 3)
		if len(h) == 0 || !strings.Contains("|"+c.want+"|", "|"+h[0].Article.ID+"|") {
			fails++
			got := []string{}
			for _, x := range h {
				got = append(got, x.Article.ID)
			}
			t.Errorf("%q: want %s, got %v", c.q, c.want, got)
		}
	}
	t.Logf("%d of %d questions answered with the right article first", len(cases)-fails, len(cases))
}

func TestWhoCan(t *testing.T) {
	k := kb(t)
	for q, want := range map[string]string{
		"who can approve payouts":        "Approving payouts",
		"who can activate a client":      "Activating a client",
		"who can close the day":          "Closing the day",
		"who can change brokerage plans": "Changing brokerage plans",
	} {
		h := k.SearchPerms(q, 3)
		if len(h) == 0 || h[0].What != want {
			t.Errorf("%q: got %+v", q, h)
		}
	}
}

// TestProvidersReportUsage runs each provider against a fake API: the right
// endpoint and headers, only the articles and masked question sent, and
// the token counts read back for metering.
func TestProvidersReportUsage(t *testing.T) {
	k := kb(t)
	a, _ := k.Get("change-brokerage")
	cases := []struct {
		name     string
		w        func(url string) Writer
		path     string
		header   string
		response string
		in, out  int
	}{
		{"anthropic", func(u string) Writer { return &Claude{Key: "k-anth", BaseURL: u} }, "/v1/messages", "x-api-key",
			`{"content":[{"type":"text","text":"1. Open **Brokerage plans**."}],"usage":{"input_tokens":2100,"output_tokens":180}}`, 2100, 180},
		{"openai", func(u string) Writer { return &OpenAI{Key: "k-oai", BaseURL: u} }, "/v1/chat/completions", "authorization",
			`{"choices":[{"message":{"content":"1. Open **Brokerage plans**."}}],"usage":{"prompt_tokens":2050,"completion_tokens":170}}`, 2050, 170},
		{"gemini", func(u string) Writer { return &Gemini{Key: "k-gem", BaseURL: u} }, "/v1beta/models/gemini-3.1-flash-lite:generateContent", "x-goog-api-key",
			`{"candidates":[{"content":{"parts":[{"text":"1. Open **Brokerage plans**."}]}}],"usageMetadata":{"promptTokenCount":2000,"candidatesTokenCount":150,"thoughtsTokenCount":20}}`, 2000, 170},
	}
	for _, c := range cases {
		var got map[string]any
		var hdr http.Header
		var path string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hdr, path = r.Header, r.URL.Path
			json.NewDecoder(r.Body).Decode(&got)
			w.Write([]byte(c.response))
		}))
		w := c.w(srv.URL)
		r, err := w.Write(context.Background(), "change brokerage for [PAN]", []Article{a}, nil)
		srv.Close()
		if err != nil || !strings.Contains(r.Text, "Brokerage plans") {
			t.Fatalf("%s: %q %v", c.name, r.Text, err)
		}
		if path != c.path || hdr.Get(c.header) == "" {
			t.Fatalf("%s: path %s header %s", c.name, path, hdr.Get(c.header))
		}
		if r.Usage.InputTokens != c.in || r.Usage.OutputTokens != c.out || r.Provider != c.name || r.Model != w.Model() {
			t.Fatalf("%s: usage %+v %s %s", c.name, r.Usage, r.Provider, r.Model)
		}
		body, _ := json.Marshal(got)
		if !strings.Contains(string(body), `change-brokerage`) || !strings.Contains(string(body), "[PAN]") {
			t.Fatalf("%s: prompt %s", c.name, body)
		}
	}
	// A provider error is reported, not billed.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(529) }))
	defer srv.Close()
	if _, err := (&Claude{Key: "k", BaseURL: srv.URL}).Write(context.Background(), "x", nil, nil); err == nil {
		t.Fatal("API error not reported")
	}
}
