package ops

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/mail"
)

// Connections: the firm's own accounts with the outside services
// Ledgerline works with (KRA, CKYC, OMS, exchanges, depository, bank, SMS,
// WhatsApp, e-Sign, mail). The firm's Admin enters each one's address,
// login and keys; secrets are kept encrypted with the firm's data keys,
// are never shown again and never leave in a report. A test checks what
// can be checked without side effects: that the address answers over TLS,
// and for the mail server that the login is accepted.
//
// Moving a firm onto Ledgerline: its Admin enters these weeks before the
// switch, the parallel run proves them, and the switch weekend only loads
// balances.

const cConn = "connection"

// ConnField is one thing a connection needs.
type ConnField struct {
	Key         string   `json:"key"`
	Label       string   `json:"label"`
	Secret      bool     `json:"secret,omitempty"`
	Optional    bool     `json:"optional,omitempty"`
	Placeholder string   `json:"placeholder,omitempty"`
	Options     []string `json:"options,omitempty"`
}

// ConnProvider is a kind of connection.
type ConnProvider struct {
	ID       string      `json:"id"`
	Name     string      `json:"name"`
	Group    string      `json:"group"`
	Purpose  string      `json:"purpose"`
	Fields   []ConnField `json:"fields"`
	Test     string      `json:"test"`               // https (the address answers), smtp (sign in), none
	Required bool        `json:"required,omitempty"` // part of the go-live checklist
	System   string      `json:"system,omitempty"`   // set elsewhere (signing, KYC plugin): where
}

var (
	fURL   = ConnField{Key: "url", Label: "Address (API or upload site)", Optional: true, Placeholder: "https://…"}
	fUser  = ConnField{Key: "user", Label: "User ID"}
	fPass  = ConnField{Key: "password", Label: "Password", Secret: true}
	fKey   = ConnField{Key: "api_key", Label: "API key", Secret: true}
	fVend  = func(opts ...string) ConnField { return ConnField{Key: "provider", Label: "Provider", Options: opts} }
	fNotes = ConnField{Key: "notes", Label: "Notes (IP allowed, contact, renewal)", Optional: true}
)

// ConnProviders is what can be connected.
var ConnProviders = []ConnProvider{
	{ID: "kra", Name: "KRA (KYC registration agency)", Group: "KYC", Purpose: "KYC upload and status for new and changed clients", Required: true, Test: "https",
		Fields: []ConnField{fVend("CVL KRA", "NDML KRA", "CAMS KRA", "KFintech KRA", "DOTEX KRA"), {Key: "code", Label: "Intermediary code"}, fUser, fPass, fURL, fNotes}},
	{ID: "ckyc", Name: "CKYC (CERSAI)", Group: "KYC", Purpose: "Central KYC search and upload", Test: "https",
		Fields: []ConnField{{Key: "code", Label: "FI code"}, fUser, fPass, fURL, fNotes}},
	{ID: "digilocker", Name: "DigiLocker (via Ledgerline KYC)", Group: "KYC", Purpose: "Aadhaar and PAN for online account opening", System: "Set on the server (LEDGERLINE_KYC_URL); the firm's own API Setu registration"},
	{ID: "esign", Name: "e-Sign (Aadhaar e-Sign provider)", Group: "KYC", Purpose: "Signing account opening forms", Test: "https",
		Fields: []ConnField{fVend("eMudhra", "NSDL e-Gov", "Protean", "Leegality", "Digio", "Other"), {Key: "client_id", Label: "Client ID"}, fKey, fURL, fNotes}},
	{ID: "pennydrop", Name: "Bank account check (penny drop)", Group: "KYC", Purpose: "Verifying clients' bank accounts", Test: "https",
		Fields: []ConnField{fVend("Cashfree", "Razorpay", "Digio", "Signzy", "Other"), {Key: "client_id", Label: "Client ID", Optional: true}, fKey, fURL, fNotes}},
	{ID: "oms", Name: "Trading front end (OMS)", Group: "Trading", Purpose: "Trade feed for live risk, start-of-day limits and holdings upload", Required: true, Test: "https",
		Fields: []ConnField{fVend("ODIN (63 moons)", "Omnesys NEST", "Greek", "Symphony", "TradeSmart XTS", "Other"), {Key: "member", Label: "Member / dealer ID"}, fUser, fKey, fURL, fNotes}},
	{ID: "nse", Name: "NSE member portal (ENIT / extranet)", Group: "Exchanges", Purpose: "UCC, reports and file downloads", Test: "https",
		Fields: []ConnField{{Key: "member", Label: "Member code"}, fUser, fPass, fURL, fNotes}},
	{ID: "bse", Name: "BSE member portal", Group: "Exchanges", Purpose: "UCC, reports and file downloads", Test: "https",
		Fields: []ConnField{{Key: "member", Label: "Member code"}, fUser, fPass, fURL, fNotes}},
	{ID: "mcx", Name: "MCX member portal", Group: "Exchanges", Purpose: "UCC, reports and file downloads", Test: "https",
		Fields: []ConnField{{Key: "member", Label: "Member code"}, fUser, fPass, fURL, fNotes}},
	{ID: "depository", Name: "Depository (CDSL / NSDL)", Group: "Depository", Purpose: "DP back office files, pledges, DIS", Test: "https",
		Fields: []ConnField{fVend("CDSL", "NSDL"), {Key: "dp_id", Label: "DP ID"}, fUser, fPass, fURL, fNotes}},
	{ID: "bank", Name: "Client money bank account", Group: "Money", Purpose: "Statements for receipts, and payouts", Required: true, Test: "https",
		Fields: []ConnField{{Key: "bank", Label: "Bank"}, {Key: "account", Label: "Account (last 4)", Placeholder: "e.g. 4321"}, fUser, {Key: "password", Label: "Password or API key", Secret: true}, fURL, fNotes}},
	{ID: "sms", Name: "SMS gateway", Group: "Messages", Purpose: "OTPs and client SMS (DLT registered)", Test: "https",
		Fields: []ConnField{fVend("MSG91", "Gupshup", "Kaleyra", "Route Mobile", "Other"), {Key: "sender", Label: "Sender ID"}, {Key: "dlt_entity", Label: "DLT entity ID"}, fKey, fURL, fNotes}},
	{ID: "whatsapp", Name: "WhatsApp Business", Group: "Messages", Purpose: "Client messages and the WhatsApp bot", Test: "https",
		Fields: []ConnField{fVend("Meta Cloud API", "Gupshup", "Interakt", "Other"), {Key: "number", Label: "Business number"}, fKey, fURL, fNotes}},
	{ID: "mail", Name: "Mail server", Group: "Messages", Purpose: "Signed contract notes and statements to clients", Required: true, Test: "smtp",
		Fields: []ConnField{{Key: "host", Label: "Server", Placeholder: "smtp.firm.in"}, {Key: "port", Label: "Port", Placeholder: "587 (STARTTLS) or 465 (TLS)"}, {Key: "from", Label: "Send as", Placeholder: "notes@firm.in"}, fUser, fPass, fNotes}},
	{ID: "signing", Name: "Digital signature certificate", Group: "Messages", Purpose: "Signing contract notes and statements", System: "Set in Dispatch register → Digital signature"},
}

func connProvider(id string) (ConnProvider, bool) {
	for _, p := range ConnProviders {
		if p.ID == id {
			return p, true
		}
	}
	return ConnProvider{}, false
}

// Connection is one of the firm's connections, as stored.
type Connection struct {
	Provider  string            `json:"provider"`
	Values    map[string]string `json:"values"`
	Secrets   map[string]string `json:"secrets,omitempty"` // never sent to a browser
	Status    string            `json:"status"`            // Saved, Working, Problem
	Problem   string            `json:"problem,omitempty"`
	TestedAt  string            `json:"tested_at,omitempty"`
	TestedBy  string            `json:"tested_by,omitempty"`
	UpdatedAt string            `json:"updated_at"`
	UpdatedBy string            `json:"updated_by"`
}

// ConnectionView is a connection for the screen: secrets shown only as set
// or not.
type ConnectionView struct {
	ConnProvider
	Status    string            `json:"status"` // Not set, Saved, Working, Problem, or the system state
	Problem   string            `json:"problem,omitempty"`
	Values    map[string]string `json:"values"`
	SecretSet map[string]bool   `json:"secret_set"`
	TestedAt  string            `json:"tested_at,omitempty"`
	TestedBy  string            `json:"tested_by,omitempty"`
	UpdatedAt string            `json:"updated_at,omitempty"`
	UpdatedBy string            `json:"updated_by,omitempty"`
}

// ConnectionsView is the Connections page.
type ConnectionsView struct {
	Connections []ConnectionView `json:"connections"`
	ServerIP    string           `json:"server_ip"` // give to providers that allow listed addresses only
	Ready       int              `json:"ready"`
	Required    int              `json:"required"`
}

func (o *Ops) systemConnStatus(ctx context.Context, tenant, id string) (string, string) {
	switch id {
	case "signing":
		sv, err := o.Signing(ctx, tenant, SystemActor)
		switch {
		case err != nil:
			return "Problem", err.Error()
		case sv.Test:
			return "Problem", "test certificate only"
		case !sv.Ready:
			return "Not set", firstOf(sv.Problem, "no certificate")
		}
		return "Working", fmt.Sprintf("%s, %d days left", sv.Info.Name, sv.ExpiresIn)
	case "digilocker":
		if o.KYC != nil {
			return "Working", "connected"
		}
		return "Not set", "the server has no KYC plugin"
	}
	return "Not set", ""
}

// Connections lists every connection and its state.
func (o *Ops) Connections(ctx context.Context, tenant, actor string) (ConnectionsView, error) {
	if err := o.require(ctx, tenant, actor, "Viewing connections", RoleAdmin, RoleCompliance); err != nil {
		return ConnectionsView{}, err
	}
	all, err := docs.List[Connection](ctx, o.Docs, tenant, cConn, "")
	if err != nil {
		return ConnectionsView{}, err
	}
	got := map[string]Connection{}
	for _, c := range all {
		got[c.Provider] = c
	}
	v := ConnectionsView{ServerIP: os.Getenv("LEDGERLINE_SERVER_IP")}
	for _, p := range ConnProviders {
		cv := ConnectionView{ConnProvider: p, Status: "Not set", Values: map[string]string{}, SecretSet: map[string]bool{}}
		if p.System != "" {
			cv.Status, cv.Problem = o.systemConnStatus(ctx, tenant, p.ID)
		} else if c, ok := got[p.ID]; ok {
			cv.Status, cv.Problem, cv.TestedAt, cv.TestedBy, cv.UpdatedAt, cv.UpdatedBy = c.Status, c.Problem, c.TestedAt, c.TestedBy, c.UpdatedAt, c.UpdatedBy
			for k, x := range c.Values {
				cv.Values[k] = x
			}
			for k, x := range c.Secrets {
				cv.SecretSet[k] = x != ""
			}
		}
		if p.Required {
			v.Required++
			if cv.Status == "Working" || cv.Status == "Saved" {
				v.Ready++
			}
		}
		v.Connections = append(v.Connections, cv)
	}
	return v, nil
}

// SaveConnection stores a connection. Secrets left empty keep their value;
// values are checked against the provider's fields.
func (o *Ops) SaveConnection(ctx context.Context, tenant, actor, id string, values, secrets map[string]string) (ConnectionView, error) {
	if err := o.require(ctx, tenant, actor, "Changing connections", RoleAdmin); err != nil {
		return ConnectionView{}, err
	}
	p, ok := connProvider(id)
	if !ok || p.System != "" {
		return ConnectionView{}, invalid("no such connection to set here")
	}
	cur, _, err := docs.GetOr(ctx, o.Docs, tenant, cConn, id, Connection{Provider: id})
	if err != nil {
		return ConnectionView{}, err
	}
	nc := Connection{Provider: id, Values: map[string]string{}, Secrets: map[string]string{}, Status: "Saved", UpdatedAt: o.now().Format(time.RFC3339), UpdatedBy: actor}
	for _, f := range p.Fields {
		if f.Secret {
			s := secrets[f.Key]
			if s == "" {
				s = cur.Secrets[f.Key]
			}
			if s == "" && !f.Optional {
				return ConnectionView{}, invalid("enter the %s", strings.ToLower(f.Label))
			}
			if len(s) > 4000 {
				return ConnectionView{}, invalid("%s is too long", f.Label)
			}
			if s != "" {
				nc.Secrets[f.Key] = s
			}
			continue
		}
		x := strings.TrimSpace(values[f.Key])
		if len(x) > 500 {
			return ConnectionView{}, invalid("%s is too long", f.Label)
		}
		if x == "" && !f.Optional && f.Key != "notes" {
			return ConnectionView{}, invalid("enter the %s", strings.ToLower(f.Label))
		}
		if len(f.Options) > 0 && x != "" && !contains(f.Options, x) {
			return ConnectionView{}, invalid("choose the %s from the list", strings.ToLower(f.Label))
		}
		if f.Key == "url" && x != "" {
			u, err := url.Parse(x)
			if err != nil || u.Scheme != "https" || u.Host == "" {
				return ConnectionView{}, invalid("the address must start with https://")
			}
		}
		if f.Key == "account" && x != "" && (len(x) != 4 || strings.Trim(x, "0123456789") != "") {
			return ConnectionView{}, invalid("give only the last 4 digits of the account")
		}
		if x != "" {
			nc.Values[f.Key] = x
		}
	}
	if id == "mail" {
		if _, err := connSMTP(nc); err != nil {
			return ConnectionView{}, err
		}
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cConn, id, nc, actor); err != nil {
		return ConnectionView{}, err
	}
	changed := []string{}
	for k := range secrets {
		if secrets[k] != "" {
			changed = append(changed, k)
		}
	}
	sort.Strings(changed)
	o.log(ctx, tenant, actor, "connection.saved", id, map[string]any{"secrets_changed": changed}) // never the secrets
	return o.connView(ctx, tenant, actor, id)
}

func (o *Ops) connView(ctx context.Context, tenant, actor, id string) (ConnectionView, error) {
	v, err := o.Connections(ctx, tenant, actor)
	if err != nil {
		return ConnectionView{}, err
	}
	for _, c := range v.Connections {
		if c.ID == id {
			return c, nil
		}
	}
	return ConnectionView{}, ErrNotFound
}

// RemoveConnection deletes a connection's details and secrets.
func (o *Ops) RemoveConnection(ctx context.Context, tenant, actor, id string) error {
	if err := o.require(ctx, tenant, actor, "Changing connections", RoleAdmin); err != nil {
		return err
	}
	if _, ok := connProvider(id); !ok {
		return ErrNotFound
	}
	// The store keeps no deleted rows: overwrite with nothing.
	if err := docs.Upsert(ctx, o.Docs, tenant, cConn, id, Connection{Provider: id, Status: "Not set", UpdatedAt: o.now().Format(time.RFC3339), UpdatedBy: actor}, actor); err != nil {
		return err
	}
	o.log(ctx, tenant, actor, "connection.removed", id, nil)
	return nil
}

// connTestClient is replaced in tests.
var connTestClient = &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// TestConnection checks a connection without side effects and records the
// result.
func (o *Ops) TestConnection(ctx context.Context, tenant, actor, id string) (ConnectionView, error) {
	if err := o.require(ctx, tenant, actor, "Testing connections", RoleAdmin); err != nil {
		return ConnectionView{}, err
	}
	p, ok := connProvider(id)
	if !ok || p.System != "" {
		return ConnectionView{}, invalid("no such connection to test here")
	}
	c, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, id)
	if err != nil || c.Status == "Not set" {
		return ConnectionView{}, invalid("save the connection first")
	}
	status, problem := "Working", ""
	switch {
	case p.Test == "smtp":
		m, err := connSMTP(c)
		if err == nil {
			cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			err = m.Check(cctx)
			cancel()
		}
		if err != nil {
			status, problem = "Problem", err.Error()
		}
	case p.Test == "https" && c.Values["url"] != "":
		status, problem = o.checkAddress(ctx, c.Values["url"])
	default:
		status, problem = "Saved", "nothing to test automatically: the login is checked the first time it is used"
	}
	c.Status, c.Problem, c.TestedAt, c.TestedBy = status, problem, o.now().Format(time.RFC3339), actor
	if err := docs.Upsert(ctx, o.Docs, tenant, cConn, id, c, actor); err != nil {
		return ConnectionView{}, err
	}
	o.log(ctx, tenant, actor, "connection.tested", id, map[string]any{"status": status, "problem": problem})
	return o.connView(ctx, tenant, actor, id)
}

// checkAddress says whether an https address answers. A refusal to sign in
// (401, 403) still shows the address and its certificate are right.
func (o *Ops) checkAddress(ctx context.Context, raw string) (string, string) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" {
		return "Problem", "the address must start with https://"
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	resp, err := connTestClient.Do(req)
	if err != nil {
		var dnsErr *net.DNSError
		switch {
		case errors.As(err, &dnsErr):
			return "Problem", "the address doesn't exist (" + u.Hostname() + ")"
		case strings.Contains(err.Error(), "certificate"):
			return "Problem", "the site's certificate isn't valid"
		case strings.Contains(err.Error(), "timeout") || strings.Contains(err.Error(), "deadline"):
			return "Problem", "no answer in 15 seconds: the provider may need to allow this server's address"
		}
		msg := err.Error()
		if i := strings.LastIndex(msg, "\": "); i >= 0 {
			msg = msg[i+3:] // drop Go's `Get "https://…": ` prefix
		}
		return "Problem", "can't reach it: " + msg
	}
	resp.Body.Close()
	switch {
	case resp.StatusCode >= 500:
		return "Problem", fmt.Sprintf("the provider's server answered with an error (%d)", resp.StatusCode)
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		return "Working", fmt.Sprintf("address answers (%d: sign-in needed, as expected)", resp.StatusCode)
	}
	return "Working", fmt.Sprintf("address answers (%d)", resp.StatusCode)
}

func connSMTP(c Connection) (*mail.SMTP, error) {
	host, port, from := c.Values["host"], firstOf(c.Values["port"], "587"), c.Values["from"]
	if host == "" || !mail.ValidAddress(from) {
		return nil, invalid("give the mail server and the address to send as")
	}
	if port != "587" && port != "465" && port != "25" && port != "2525" {
		return nil, invalid("the port is usually 587 (STARTTLS) or 465 (TLS)")
	}
	return &mail.SMTP{Host: host, Port: port, User: c.Values["user"], Pass: c.Secrets["password"], From: from, Implicit: port == "465", Timeout: 60 * time.Second}, nil
}

// mailerFor is the mail server for a firm: its own from Connections, else
// the server's (LEDGERLINE_SMTP_URL).
func (o *Ops) mailerFor(ctx context.Context, tenant string) Mailer {
	if c, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, "mail"); err == nil && c.Status != "Not set" && c.Status != "Problem" {
		if m, err := connSMTP(c); err == nil {
			return m
		}
	}
	return o.Mailer
}

// ConnSecret gives a connection's secret to code that uses it (a bot step,
// an upload). Every use is recorded.
func (o *Ops) ConnSecret(ctx context.Context, tenant, purpose, id, key string) (string, error) {
	c, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, id)
	if err != nil || c.Secrets[key] == "" {
		return "", fmt.Errorf("%w: the %s connection has no %s; set it in Settings → Connections", ErrNotFound, id, key)
	}
	o.log(ctx, tenant, SystemActor, "connection.used", id, map[string]any{"key": key, "for": purpose})
	return c.Secrets[key], nil
}
