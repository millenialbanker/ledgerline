package control

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

//go:embed web
var webFS embed.FS

// Server is Control's web server: the report endpoint for installations,
// and the console for the provider's staff.
type Server struct {
	C      *Control
	Log    *slog.Logger
	Secure bool // cookies only over HTTPS
	mux    *http.ServeMux

	rlMu sync.Mutex
	rl   map[string][]time.Time
}

const cookieName = "llctl"

type ctxKey int

const (
	keyStaff ctxKey = iota
)

// NewServer builds the routes.
func NewServer(c *Control, log *slog.Logger) *Server {
	s := &Server{C: c, Log: log, mux: http.NewServeMux(), rl: map[string][]time.Time{}}
	s.mux.HandleFunc("POST /api/v1/report", s.report)
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") })
	s.mux.HandleFunc("POST /api/login", s.login)
	s.mux.HandleFunc("POST /api/logout", s.logout)

	A := func(p string, h func(http.ResponseWriter, *http.Request, string)) { s.mux.Handle(p, s.authed(h)) }
	A("GET /api/me", s.me)
	A("POST /api/password", s.password)
	A("GET /api/overview", func(w http.ResponseWriter, r *http.Request, by string) { out(w)(s.C.GetOverview(r.Context(), by)) })
	A("GET /api/installations", func(w http.ResponseWriter, r *http.Request, by string) { out(w)(s.C.Installations(r.Context(), by)) })
	A("POST /api/installations", s.newInstall)
	A("POST /api/installations/{id}", s.updInstall)
	A("GET /api/firms", func(w http.ResponseWriter, r *http.Request, by string) { out(w)(s.C.Firms(r.Context(), by)) })
	A("POST /api/firms/{id}/customer", s.linkFirm)
	A("POST /api/firms/{id}/ai-credit", s.aiCredit)
	A("POST /api/commands/{id}/cancel", func(w http.ResponseWriter, r *http.Request, by string) {
		err := s.C.CancelCommand(r.Context(), by, r.PathValue("id"))
		out(w)(map[string]bool{"ok": err == nil}, err)
	})
	A("GET /api/customers", func(w http.ResponseWriter, r *http.Request, by string) { out(w)(s.C.Customers(r.Context(), by)) })
	A("POST /api/customers", s.saveCustomer)
	A("GET /api/customers/{id}", func(w http.ResponseWriter, r *http.Request, by string) {
		out(w)(s.C.GetCustomer(r.Context(), by, r.PathValue("id")))
	})
	A("GET /api/usage", func(w http.ResponseWriter, r *http.Request, by string) {
		out(w)(s.C.Usage(r.Context(), by, firstOf(r.URL.Query().Get("month"), s.C.month())))
	})
	A("GET /api/billing", s.billing)
	A("POST /api/invoices/generate", s.generate)
	A("GET /api/invoices/{id}", func(w http.ResponseWriter, r *http.Request, by string) {
		out(w)(s.C.GetInvoice(r.Context(), by, r.PathValue("id")))
	})
	A("GET /api/invoices/{id}/print", s.printInvoice)
	A("POST /api/invoices/{id}/edit", s.editInvoice)
	A("POST /api/invoices/{id}/issue", s.issue)
	A("POST /api/invoices/{id}/void", s.void)
	A("POST /api/invoices/{id}/payment", s.payment)
	A("GET /api/tickets", func(w http.ResponseWriter, r *http.Request, by string) { out(w)(s.C.Tickets(r.Context(), by)) })
	A("GET /api/tickets/{id}", func(w http.ResponseWriter, r *http.Request, by string) {
		out(w)(s.C.Ticket(r.Context(), by, r.PathValue("id")))
	})
	A("POST /api/tickets/{id}/reply", s.replyTicket)
	A("POST /api/tickets/{id}", s.setTicket)
	A("GET /api/settings", func(w http.ResponseWriter, r *http.Request, by string) { out(w)(s.C.Settings(r.Context())) })
	A("PUT /api/settings", s.saveSettings)
	A("GET /api/staff", func(w http.ResponseWriter, r *http.Request, by string) { out(w)(s.C.StaffList(r.Context(), by)) })
	A("POST /api/staff", s.saveStaff)
	A("GET /api/log", func(w http.ResponseWriter, r *http.Request, by string) { out(w)(s.C.Log(r.Context(), by, 300)) })

	sub, _ := fs.Sub(webFS, "web")
	files := http.FileServer(http.FS(sub))
	s.mux.Handle("GET /", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	}))
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; frame-ancestors 'none'")
	if s.Secure {
		h.Set("Strict-Transport-Security", "max-age=31536000")
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		h.Set("Cache-Control", "no-store")
	}
	s.mux.ServeHTTP(w, r)
}

// ---- helpers ----

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	code, msg := 500, "Something went wrong."
	switch {
	case errors.Is(err, ErrInvalid):
		code, msg = 400, strings.TrimPrefix(err.Error(), "invalid: ")
	case errors.Is(err, ErrForbidden):
		code, msg = 403, strings.TrimPrefix(strings.TrimPrefix(err.Error(), "forbidden"), ": ")
		if msg == "" {
			msg = "You can't do that."
		}
	case errors.Is(err, ErrNotFound), errors.Is(err, docs.ErrNotFound):
		code, msg = 404, "Not found."
	case errors.Is(err, docs.ErrStale):
		code, msg = 409, "Someone else changed this just now; reload and try again."
	}
	writeJSON(w, code, map[string]string{"error": msg})
}

func out(w http.ResponseWriter) func(any, error) {
	return func(v any, err error) {
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, v)
	}
}

func decode(w http.ResponseWriter, r *http.Request, v any, limit int64) bool {
	if err := json.NewDecoder(io.LimitReader(r.Body, limit)).Decode(v); err != nil {
		writeJSON(w, 400, map[string]string{"error": "The request could not be read."})
		return false
	}
	return true
}

func clientIP(r *http.Request) string {
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return h
}

func (s *Server) limited(key string, n int, per time.Duration) bool {
	s.rlMu.Lock()
	defer s.rlMu.Unlock()
	now := time.Now()
	keep := s.rl[key][:0]
	for _, t := range s.rl[key] {
		if now.Sub(t) < per {
			keep = append(keep, t)
		}
	}
	if len(keep) >= n {
		s.rl[key] = keep
		return true
	}
	s.rl[key] = append(keep, now)
	return false
}

// ---- installations report in ----

func (s *Server) report(w http.ResponseWriter, r *http.Request) {
	key, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if s.limited("rep:"+clientIP(r), 120, time.Minute) {
		writeJSON(w, 429, map[string]string{"error": "too many reports"})
		return
	}
	if !ok {
		writeJSON(w, 401, map[string]string{"error": "installation key required"})
		return
	}
	in, err := s.C.InstallationForKey(r.Context(), key)
	if err != nil {
		writeJSON(w, 401, map[string]string{"error": "unknown or revoked installation key"})
		return
	}
	var rep Report
	if !decode(w, r, &rep, 32<<20) {
		return
	}
	reply, err := s.C.Receive(r.Context(), in, rep, clientIP(r))
	if err != nil {
		if s.Log != nil {
			s.Log.Error("report", "installation", in.ID, "err", err)
		}
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, reply)
}

// ---- staff sign-in ----

func (s *Server) authed(h func(http.ResponseWriter, *http.Request, string)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ck, _ := r.Cookie(cookieName)
		tok := ""
		if ck != nil {
			tok = ck.Value
		}
		ss, st, err := s.C.SessionFor(r.Context(), tok)
		if err != nil {
			writeJSON(w, 401, map[string]string{"error": "Please sign in."})
			return
		}
		if r.Method != http.MethodGet && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF")), []byte(ss.CSRF)) != 1 {
			writeJSON(w, 403, map[string]string{"error": "Reload the page and try again."})
			return
		}
		if st.MustReset && r.URL.Path != "/api/password" && r.URL.Path != "/api/me" {
			writeJSON(w, 403, map[string]string{"error": "Set a new password first."})
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), keyStaff, ss)), st.ID)
	})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var b struct{ ID, Password string }
	if !decode(w, r, &b, 4096) {
		return
	}
	tok, ss, st, err := s.C.Login(r.Context(), b.ID, b.Password, clientIP(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: tok, Path: "/", HttpOnly: true, Secure: s.Secure, SameSite: http.SameSiteStrictMode, MaxAge: 12 * 3600})
	writeJSON(w, 200, map[string]any{"csrf": ss.CSRF, "id": st.ID, "name": st.Name, "role": st.Role, "must_reset": st.MustReset})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if ck, _ := r.Cookie(cookieName); ck != nil {
		s.C.Logout(r.Context(), ck.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", HttpOnly: true, Secure: s.Secure, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request, by string) {
	ss := r.Context().Value(keyStaff).(Session)
	st, _ := s.C.staff(r.Context(), by)
	set, _ := s.C.Settings(r.Context())
	writeJSON(w, 200, map[string]any{"csrf": ss.CSRF, "id": st.ID, "name": st.Name, "role": st.Role, "must_reset": st.MustReset,
		"month": s.C.month(), "today": s.C.today(), "vendor": set.Vendor.Name, "roles": Roles, "statuses": TicketStatuses, "customer_statuses": CustomerStatuses, "setup_fee": set.Prices.SetupFee})
}

func (s *Server) password(w http.ResponseWriter, r *http.Request, by string) {
	var b struct{ Old, New string }
	if !decode(w, r, &b, 4096) {
		return
	}
	err := s.C.ChangePassword(r.Context(), by, b.Old, b.New)
	out(w)(map[string]bool{"ok": err == nil}, err)
}

// ---- console actions ----

func (s *Server) newInstall(w http.ResponseWriter, r *http.Request, by string) {
	var b struct{ Name, Kind string }
	if !decode(w, r, &b, 4096) {
		return
	}
	in, key, err := s.C.NewInstallation(r.Context(), by, b.Name, b.Kind)
	in.KeyHash = ""
	out(w)(map[string]any{"installation": in, "key": key}, err)
}

func (s *Server) updInstall(w http.ResponseWriter, r *http.Request, by string) {
	var b struct {
		Name, Kind    string
		Revoke, Rekey bool
	}
	if !decode(w, r, &b, 4096) {
		return
	}
	in, key, err := s.C.UpdateInstallation(r.Context(), by, r.PathValue("id"), b.Name, b.Kind, b.Revoke, b.Rekey)
	in.KeyHash = ""
	out(w)(map[string]any{"installation": in, "key": key}, err)
}

func (s *Server) linkFirm(w http.ResponseWriter, r *http.Request, by string) {
	var b struct{ Customer string }
	if !decode(w, r, &b, 4096) {
		return
	}
	out(w)(s.C.LinkFirm(r.Context(), by, r.PathValue("id"), b.Customer))
}

func (s *Server) aiCredit(w http.ResponseWriter, r *http.Request, by string) {
	var b struct {
		Amount money.Amount `json:"amount"`
		Bill   bool         `json:"bill"`
		Note   string       `json:"note"`
	}
	if !decode(w, r, &b, 4096) {
		return
	}
	out(w)(s.C.SendAICredit(r.Context(), by, r.PathValue("id"), b.Amount, b.Bill, b.Note))
}

func (s *Server) saveCustomer(w http.ResponseWriter, r *http.Request, by string) {
	var cu Customer
	if !decode(w, r, &cu, 64<<10) {
		return
	}
	out(w)(s.C.SaveCustomer(r.Context(), by, cu))
}

func (s *Server) billing(w http.ResponseWriter, r *http.Request, by string) {
	month := firstOf(r.URL.Query().Get("month"), s.C.month())
	pv, err := s.C.Preview(r.Context(), by, month)
	if err != nil {
		writeErr(w, err)
		return
	}
	ivs, err := s.C.Invoices(r.Context(), by)
	if err != nil {
		writeErr(w, err)
		return
	}
	type ivRow struct {
		Invoice
		Overdue  bool   `json:"overdue"`
		CustName string `json:"cust_name"`
	}
	names := map[string]string{}
	for _, b := range pv {
		names[b.Customer.ID] = b.Customer.Name
	}
	rows := []ivRow{}
	for _, iv := range ivs {
		iv.Lines = nil
		rows = append(rows, ivRow{Invoice: iv, Overdue: s.C.InvoiceOverdue(iv), CustName: names[iv.Customer]})
	}
	set, _ := s.C.Settings(r.Context())
	writeJSON(w, 200, map[string]any{"month": month, "preview": pv, "invoices": rows, "prices": set.Prices})
}

func (s *Server) generate(w http.ResponseWriter, r *http.Request, by string) {
	var b struct{ Month, Customer string }
	if !decode(w, r, &b, 4096) {
		return
	}
	out(w)(s.C.Generate(r.Context(), by, b.Month, b.Customer))
}

func (s *Server) editInvoice(w http.ResponseWriter, r *http.Request, by string) {
	var b struct {
		Add    *InvoiceLine `json:"add"`
		Remove *int         `json:"remove"`
		Notes  *string      `json:"notes"`
	}
	if !decode(w, r, &b, 16<<10) {
		return
	}
	rm := -1
	if b.Remove != nil {
		rm = *b.Remove
	}
	out(w)(s.C.EditDraft(r.Context(), by, r.PathValue("id"), b.Add, rm, b.Notes))
}

func (s *Server) issue(w http.ResponseWriter, r *http.Request, by string) {
	var b struct{ Date string }
	if !decode(w, r, &b, 4096) {
		return
	}
	out(w)(s.C.IssueInvoice(r.Context(), by, r.PathValue("id"), b.Date))
}

func (s *Server) void(w http.ResponseWriter, r *http.Request, by string) {
	var b struct{ Reason string }
	if !decode(w, r, &b, 4096) {
		return
	}
	out(w)(s.C.VoidInvoice(r.Context(), by, r.PathValue("id"), b.Reason))
}

func (s *Server) payment(w http.ResponseWriter, r *http.Request, by string) {
	var p Payment
	if !decode(w, r, &p, 4096) {
		return
	}
	out(w)(s.C.RecordPayment(r.Context(), by, r.PathValue("id"), p))
}

func (s *Server) replyTicket(w http.ResponseWriter, r *http.Request, by string) {
	var b struct {
		Text     string `json:"text"`
		Internal bool   `json:"internal"`
		Status   string `json:"status"`
	}
	if !decode(w, r, &b, 64<<10) {
		return
	}
	out(w)(s.C.ReplyTicket(r.Context(), by, r.PathValue("id"), b.Text, b.Internal, b.Status))
}

func (s *Server) setTicket(w http.ResponseWriter, r *http.Request, by string) {
	var b struct{ Assignee, Priority string }
	if !decode(w, r, &b, 4096) {
		return
	}
	out(w)(s.C.SetTicket(r.Context(), by, r.PathValue("id"), b.Assignee, b.Priority))
}

func (s *Server) saveSettings(w http.ResponseWriter, r *http.Request, by string) {
	var b Settings
	if !decode(w, r, &b, 64<<10) {
		return
	}
	out(w)(s.C.SaveSettings(r.Context(), by, b))
}

func (s *Server) saveStaff(w http.ResponseWriter, r *http.Request, by string) {
	var b struct {
		ID, Name, Role string
		Active, Reset  bool
	}
	if !decode(w, r, &b, 4096) {
		return
	}
	st, pw, err := s.C.SaveStaff(r.Context(), by, b.ID, b.Name, b.Role, b.Active, b.Reset)
	out(w)(map[string]any{"staff": st, "password": pw}, err)
}

// ---- printable invoice ----

var invTmpl = template.Must(template.New("inv").Funcs(template.FuncMap{
	"inr":   func(a money.Amount) string { return inr(a) },
	"words": InWords,
	"add":   func(a, b int) int { return a + b },
	"nl":    func(s string) []string { return strings.Split(s, "\n") },
	"itoa":  strconv.Itoa,
}).Parse(invoiceHTML))

func (s *Server) printInvoice(w http.ResponseWriter, r *http.Request, by string) {
	iv, err := s.C.GetInvoice(r.Context(), by, r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'")
	_ = invTmpl.Execute(w, map[string]any{"I": iv, "Intra": iv.IGST == 0, "Draft": iv.Status == "Draft", "Void": iv.Status == "Void",
		"Half": halfPct(iv.GSTPct), "Due": iv.Total - iv.Paid})
}

// inr formats paise in the Indian style: 12,34,567.89.
func inr(a money.Amount) string {
	neg := a < 0
	if neg {
		a = -a
	}
	r, p := int64(a)/100, int64(a)%100
	s := strconv.FormatInt(r, 10)
	if len(s) > 3 {
		head, tail := s[:len(s)-3], s[len(s)-3:]
		var parts []string
		for len(head) > 2 {
			parts = append([]string{head[len(head)-2:]}, parts...)
			head = head[:len(head)-2]
		}
		if head != "" {
			parts = append([]string{head}, parts...)
		}
		s = strings.Join(parts, ",") + "," + tail
	}
	out := s + "." + strconv.FormatInt(100+p, 10)[1:]
	if neg {
		out = "-" + out
	}
	return out
}

const invoiceHTML = `<!doctype html><html lang="en"><head><meta charset="utf-8"><title>{{if .I.Number}}{{.I.Number}}{{else}}Draft {{.I.ID}}{{end}}</title>
<style>
body{font:13px/1.45 -apple-system,"Segoe UI",Roboto,Helvetica,Arial,sans-serif;color:#1b1f1d;margin:0;background:#f2f3ef}
.pg{max-width:820px;margin:24px auto;background:#fff;padding:36px 40px;box-shadow:0 1px 3px rgba(0,0,0,.1);position:relative}
h1{font-size:20px;margin:0 0 2px;letter-spacing:.5px} .muted{color:#5c645f} .r{text-align:right} .mono{font-variant-numeric:tabular-nums}
.top{display:flex;justify-content:space-between;gap:24px;border-bottom:2px solid #1b1f1d;padding-bottom:14px;margin-bottom:16px}
.two{display:grid;grid-template-columns:1fr 1fr;gap:24px;margin-bottom:16px} .box h4{margin:0 0 4px;font-size:11px;text-transform:uppercase;letter-spacing:.6px;color:#5c645f}
table{width:100%;border-collapse:collapse} th,td{padding:7px 8px;border-bottom:1px solid #dadfd5;vertical-align:top} th{font-size:11px;text-transform:uppercase;letter-spacing:.5px;color:#5c645f;text-align:left;background:#f6f7f3}
.tot td{border:0;padding:4px 8px} .tot .g td{border-top:2px solid #1b1f1d;font-weight:700;font-size:15px}
.stamp{position:absolute;top:40px;right:40%;font-size:44px;font-weight:800;color:rgba(190,40,40,.18);transform:rotate(-14deg);letter-spacing:4px}
.foot{margin-top:22px;display:grid;grid-template-columns:1fr 1fr;gap:24px;font-size:12px}
.bar{max-width:820px;margin:16px auto 0;text-align:right} .bar button{font:inherit;padding:6px 14px;border:1px solid #c9cdc3;background:#fff;border-radius:6px;cursor:pointer}
@media print{body{background:#fff}.pg{box-shadow:none;margin:0;max-width:none;padding:0}.bar{display:none}}
</style></head><body>
<div class="bar"><button onclick="print()">Print or save as PDF</button></div>
<div class="pg">{{if .Draft}}<div class="stamp">DRAFT</div>{{end}}{{if .Void}}<div class="stamp">VOID</div>{{end}}
<div class="top"><div><h1>{{.I.Vendor.Name}}</h1><div class="muted">{{range nl .I.Vendor.Address}}{{.}}<br>{{end}}{{if .I.Vendor.GSTIN}}GSTIN {{.I.Vendor.GSTIN}}{{end}}{{if .I.Vendor.PAN}} · PAN {{.I.Vendor.PAN}}{{end}}{{if .I.Vendor.Email}}<br>{{.I.Vendor.Email}}{{end}}{{if .I.Vendor.Phone}} · {{.I.Vendor.Phone}}{{end}}</div></div>
<div class="r"><h1>TAX INVOICE</h1><div>No. <b>{{if .I.Number}}{{.I.Number}}{{else}}— (draft {{.I.ID}}){{end}}</b></div><div>Date {{if .I.Date}}{{.I.Date}}{{else}}—{{end}}</div>{{if .I.Due}}<div>Due {{.I.Due}}</div>{{end}}<div class="muted">For {{.I.Period}}</div></div></div>
<div class="two"><div class="box"><h4>Bill to</h4><b>{{if .I.Bill.LegalName}}{{.I.Bill.LegalName}}{{else}}{{.I.Bill.Name}}{{end}}</b><br>{{range nl .I.Bill.Address}}{{.}}<br>{{end}}{{if .I.Bill.GSTIN}}GSTIN {{.I.Bill.GSTIN}}<br>{{end}}{{if .I.Bill.Contact}}Attn. {{.I.Bill.Contact}}{{end}}</div>
<div class="box"><h4>Place of supply</h4>{{if .I.Bill.State}}State code {{.I.Bill.State}}{{else}}—{{end}}<br><span class="muted">{{if .Intra}}Intra-state: CGST + SGST{{else}}Inter-state: IGST{{end}}</span></div></div>
<table><thead><tr><th>#</th><th>Description</th><th>SAC</th><th class="r">Qty</th><th class="r">Rate ₹</th><th class="r">Amount ₹</th></tr></thead><tbody>
{{range $i, $l := .I.Lines}}<tr><td>{{add $i 1}}</td><td>{{$l.Desc}}</td><td>{{$l.SAC}}</td><td class="r mono">{{$l.Qty}}</td><td class="r mono">{{inr $l.Rate}}</td><td class="r mono">{{inr $l.Amount}}</td></tr>{{end}}
</tbody></table>
<table class="tot" style="width:46%;margin-left:auto;margin-top:8px">
<tr><td>Subtotal</td><td class="r mono">{{inr .I.Subtotal}}</td></tr>
{{if .I.Discount}}<tr><td>Discount</td><td class="r mono">−{{inr .I.Discount}}</td></tr>{{end}}
<tr><td>Taxable value</td><td class="r mono">{{inr .I.Taxable}}</td></tr>
{{if .Intra}}<tr><td>CGST {{.Half}}%</td><td class="r mono">{{inr .I.CGST}}</td></tr><tr><td>SGST {{.Half}}%</td><td class="r mono">{{inr .I.SGST}}</td></tr>{{else}}<tr><td>IGST {{.I.GSTPct}}%</td><td class="r mono">{{inr .I.IGST}}</td></tr>{{end}}
<tr class="g"><td>Total</td><td class="r mono">₹ {{inr .I.Total}}</td></tr>
{{if .I.Paid}}<tr><td>Received</td><td class="r mono">{{inr .I.Paid}}</td></tr><tr><td>Balance due</td><td class="r mono">{{inr .Due}}</td></tr>{{end}}
</table>
<p><b>{{words .I.Total}}</b></p>
{{if .I.Notes}}<p class="muted">{{range nl .I.Notes}}{{.}}<br>{{end}}</p>{{end}}
<div class="foot"><div>{{if .I.Vendor.Account}}<b>Pay to</b><br>{{.I.Vendor.Name}}<br>{{.I.Vendor.Bank}} · A/c {{.I.Vendor.Account}}<br>IFSC {{.I.Vendor.IFSC}}{{end}}{{if .I.Vendor.UPI}}<br>UPI {{.I.Vendor.UPI}}{{end}}</div>
<div class="r">For {{.I.Vendor.Name}}<br><br><br>Authorised signatory</div></div>
{{if .I.Vendor.Footer}}<p class="muted" style="margin-top:18px;font-size:11px">{{.I.Vendor.Footer}}</p>{{end}}
</div></body></html>`

func halfPct(p string) string {
	d, err := money.ParseDec(firstOf(p, "18"))
	if err != nil {
		return ""
	}
	h, _ := d.Quo(money.DecInt(2))
	return h.String() // already without trailing zeros
}
