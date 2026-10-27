package ops

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestConnections(t *testing.T) {
	o, ctx := setup(t)
	v, err := o.Connections(ctx, T, "admin")
	if err != nil || len(v.Connections) != len(ConnProviders) || v.Required < 3 || v.Ready != 0 {
		t.Fatalf("view %+v %v", v, err)
	}
	if _, err := o.Connections(ctx, T, "priya.nair"); err == nil {
		t.Fatal("maker saw connections")
	}
	if _, err := o.SaveConnection(ctx, T, "vikram.shah", "kra", nil, nil); err == nil {
		t.Fatal("compliance changed a connection")
	}
	if _, err := o.SaveConnection(ctx, T, "admin", "kra", map[string]string{"provider": "CVL KRA", "code": "1234", "user": "bhairav"}, nil); err == nil {
		t.Fatal("saved without the password")
	}
	if _, err := o.SaveConnection(ctx, T, "admin", "kra", map[string]string{"provider": "Some KRA", "code": "1234", "user": "bhairav"}, map[string]string{"password": "s3cret"}); err == nil {
		t.Fatal("unknown provider accepted")
	}
	if _, err := o.SaveConnection(ctx, T, "admin", "kra", map[string]string{"provider": "CVL KRA", "code": "1234", "user": "bhairav", "url": "http://kra.example"}, map[string]string{"password": "s3cret"}); err == nil {
		t.Fatal("plain http accepted")
	}
	// A provider's address that asks for sign-in (401) is reachable.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }))
	defer srv.Close()
	old := connTestClient
	connTestClient = srv.Client()
	connTestClient.Timeout = 5 * time.Second
	defer func() { connTestClient = old }()
	cv, err := o.SaveConnection(ctx, T, "admin", "kra", map[string]string{"provider": "CVL KRA", "code": "1234", "user": "bhairav", "url": srv.URL}, map[string]string{"password": "s3cret"})
	if err != nil || cv.Status != "Saved" || !cv.SecretSet["password"] {
		t.Fatalf("save %+v %v", cv, err)
	}
	cv, err = o.TestConnection(ctx, T, "admin", "kra")
	if err != nil || cv.Status != "Working" || !strings.Contains(cv.Problem, "401") {
		t.Fatalf("test %+v %v", cv, err)
	}
	// Saving again without the password keeps it.
	if cv, err = o.SaveConnection(ctx, T, "admin", "kra", map[string]string{"provider": "CVL KRA", "code": "5678", "user": "bhairav", "url": srv.URL}, nil); err != nil || !cv.SecretSet["password"] || cv.Values["code"] != "5678" {
		t.Fatalf("resave %+v %v", cv, err)
	}
	if s, err := o.ConnSecret(ctx, T, "test", "kra", "password"); err != nil || s != "s3cret" {
		t.Fatalf("secret %q %v", s, err)
	}
	// The secret never leaves in the view, the audit trail or the firm's report.
	v, _ = o.Connections(ctx, T, "admin")
	for _, c := range v.Connections {
		for _, x := range c.Values {
			if strings.Contains(x, "s3cret") {
				t.Fatal("secret in the view")
			}
		}
	}
	evs, _ := o.App.Audit.List(ctx, T, 200)
	for _, e := range evs {
		if strings.Contains(string(mustJSON(e)), "s3cret") {
			t.Fatalf("secret in the audit trail: %s", e.Action)
		}
	}
	r, _ := o.FirmReport(ctx, T)
	if strings.Contains(string(mustJSON(r)), "s3cret") {
		t.Fatal("secret in the report to Control")
	}
	ok := false
	for _, s := range r.Setup {
		if s.Key == "conn_kra" && s.OK {
			ok = true
		}
	}
	if !ok {
		t.Fatalf("KRA not in the go-live checklist: %+v", r.Setup)
	}
	// An address that doesn't answer is a problem.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	o.SaveConnection(ctx, T, "admin", "kra", map[string]string{"provider": "CVL KRA", "code": "1", "user": "u", "url": "https://" + addr}, nil)
	if cv, _ := o.TestConnection(ctx, T, "admin", "kra"); cv.Status != "Problem" {
		t.Fatalf("dead address: %+v", cv)
	}
	// The firm's own mail server replaces the server-wide one.
	if o.mailerFor(ctx, T) != nil {
		t.Fatal("mailer without set-up")
	}
	if _, err := o.SaveConnection(ctx, T, "admin", "mail", map[string]string{"host": "smtp.firm.test", "port": "587", "from": "notes@firm.test", "user": "notes@firm.test"}, map[string]string{"password": "pw"}); err != nil {
		t.Fatal(err)
	}
	if m, ok := o.mailerFor(ctx, T).(*SMTPMailer); !ok || m.Host != "smtp.firm.test" || m.Pass != "pw" {
		t.Fatalf("firm mailer %+v", m)
	}
	if err := o.RemoveConnection(ctx, T, "admin", "mail"); err != nil || o.mailerFor(ctx, T) != nil {
		t.Fatalf("remove %v", err)
	}
	if _, err := o.SaveConnection(ctx, T, "admin", "signing", nil, nil); err == nil {
		t.Fatal("system connection saved here")
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
