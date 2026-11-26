package ops

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// loopbackTLS is a self-signed certificate for 127.0.0.1 and its PEM.
func loopbackTLS(t *testing.T) (tls.Certificate, string) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: "siem.test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: k}, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

// fakeSyslog is a TLS syslog receiver: it keeps what each connection sent.
type fakeSyslog struct {
	mu    sync.Mutex
	conns [][]byte
	ln    net.Listener
}

func newFakeSyslog(t *testing.T, cert tls.Certificate) *fakeSyslog {
	t.Helper()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSyslog{ln: ln}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			b, _ := io.ReadAll(c) // to the client's close_notify
			c.Close()
			f.mu.Lock()
			f.conns = append(f.conns, b)
			f.mu.Unlock()
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return f
}

func (f *fakeSyslog) got() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]byte(nil), f.conns...)
}

// syslogFrames splits an RFC 5425 octet-counted stream into messages.
func syslogFrames(t *testing.T, b []byte) []string {
	t.Helper()
	var out []string
	for len(b) > 0 {
		sp := bytes.IndexByte(b, ' ')
		n, err := strconv.Atoi(string(b[:max(sp, 0)]))
		if sp < 1 || err != nil || n <= 0 || sp+1+n > len(b) {
			t.Fatalf("bad frame at %q", b[:min(len(b), 40)])
		}
		out = append(out, string(b[sp+1:sp+1+n]))
		b = b[sp+1+n:]
	}
	return out
}

// TestSyslogTLSExport: lines go to a TLS listener as RFC 5424 messages with
// RFC 5425 framing, starting from when the destination was set up, and the
// cursor stops them being sent twice.
func TestSyslogTLSExport(t *testing.T) {
	o, ctx, _, clock := secSetup(t)
	cert, caPEM := loopbackTLS(t)
	fs := newFakeSyslog(t, cert)
	_, port, _ := net.SplitHostPort(fs.ln.Addr().String())
	if _, err := o.SaveConnection(ctx, T, "admin", connSIEMSyslog, map[string]string{"host": "https://127.0.0.1", "port": port}, nil); err == nil {
		t.Fatal("host with a scheme accepted")
	}
	if _, err := o.SaveConnection(ctx, T, "admin", connSIEMSyslog, map[string]string{"host": "127.0.0.1", "port": port}, map[string]string{"ca_certificate": "not a cert"}); err == nil {
		t.Fatal("bad CA accepted")
	}
	if _, err := o.SaveConnection(ctx, T, "admin", connSIEMSyslog, map[string]string{"host": "127.0.0.1", "port": port}, map[string]string{"ca_certificate": caPEM}); err != nil {
		t.Fatal(err)
	}
	o.SecurityEvent(ctx, T, SecEvent{Kind: "signin.ok", Subject: "before.start"}) // before the export starts: not sent
	*clock = clock.Add(time.Minute)
	o.Tick(ctx, T, *clock) // starts the export from now
	*clock = clock.Add(time.Minute)
	o.Login(ctx, T, "anita.rao", "wrong-password-z", "", "192.0.2.10", "test")
	o.log(ctx, T, "vikram.shah", "client.reveal", "C1001", map[string]any{"field": "PAN", "reason": "checking ABCDE1234F", "pan": "ABCDE1234F"})
	o.Tick(ctx, T, *clock) // too new: waits for the lag
	if n := len(fs.got()); n != 0 {
		t.Fatalf("sent lines younger than the lag: %d", n)
	}
	*clock = clock.Add(time.Minute)
	o.Tick(ctx, T, *clock)
	var got [][]byte
	for i := 0; i < 100 && len(got) == 0; i++ {
		got = fs.got()
		time.Sleep(10 * time.Millisecond)
	}
	if len(got) != 1 {
		t.Fatalf("connections: %d", len(got))
	}
	msgs := syslogFrames(t, got[0])
	logs, kinds := map[string]int{}, map[string]bool{}
	for _, m := range msgs {
		// <PRI>1 TIMESTAMP HOSTNAME APP-NAME PROCID MSGID SD MSG
		p := strings.SplitN(m, " ", 8)
		if len(p) != 8 || !strings.HasPrefix(p[0], "<") || !strings.HasSuffix(p[0], ">1") || p[3] != "ledgerline" || p[4] != "-" || p[6] != "-" || !strings.HasSuffix(p[1], "+05:30") {
			t.Fatalf("not RFC 5424: %q", m)
		}
		var line map[string]any
		if err := json.Unmarshal([]byte(p[7]), &line); err != nil || line["log"] != p[5] || line["firm"] != T {
			t.Fatalf("MSG: %q %v", p[7], err)
		}
		if line["subject"] == "before.start" {
			t.Fatal("sent an event from before the export started")
		}
		if strings.Contains(p[7], "ABCDE1234F") {
			t.Fatalf("PAN not masked: %s", p[7])
		}
		logs[p[5]]++
		if k, _ := line["kind"].(string); k != "" {
			kinds[k] = true
		}
		if p[5] == "signin" && line["kind"] == "signin.failed" && p[0] != "<85>1" { // authpriv (10) * 8 + notice (5)
			t.Fatalf("PRI of a failed sign-in: %s", p[0])
		}
		if p[5] == "audit" && !strings.HasPrefix(p[0], "<110>") { // log audit (13) * 8 + informational (6)
			t.Fatalf("PRI of an audit line: %s", p[0])
		}
	}
	if logs["signin"] != 1 || logs["audit"] < 2 || !kinds["data.reveal"] { // the failure; its audit entry and the reveal; the reveal event
		t.Fatalf("logs: %v %v", logs, kinds)
	}
	sc, err := o.LogExport(ctx, T, "vikram.shah")
	if err != nil || sc.Dests[0].Status != "Delivering" || sc.Dests[0].State.Delivered != int64(len(msgs)) || sc.Dests[0].Pending != 0 || !strings.Contains(sc.Retention, "730 days") {
		t.Fatalf("screen: %+v %v", sc.Dests[0], err)
	}
	*clock = clock.Add(time.Minute)
	o.Tick(ctx, T, *clock)
	time.Sleep(50 * time.Millisecond)
	if n := len(fs.got()); n != 1 {
		t.Fatalf("sent again with nothing new: %d", n)
	}
	// The test button sends one line now and leaves the cursor alone.
	d, err := o.TestLogExport(ctx, T, "vikram.shah", connSIEMSyslog)
	if err != nil || !d.State.LastTestOK {
		t.Fatalf("test: %+v %v", d.State, err)
	}
	if _, err := o.TestLogExport(ctx, T, "priya.nair", connSIEMSyslog); err == nil {
		t.Fatal("maker tested")
	}
}

// TestHTTPSExportHMACRetry: lines are posted as JSON lines with an HMAC
// signature; a refused batch is sent again after the wait (at least once)
// and the cursor moves only when the SIEM took it. A destination failing
// for 30 minutes raises a work item, closed when lines flow again.
func TestHTTPSExportHMACRetry(t *testing.T) {
	o, ctx, _, clock := secSetup(t)
	const secret = "hmac-test-secret-1"
	var mu sync.Mutex
	refuse := 2 // the connection test, then the first batch
	var batches [][]string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		m := hmac.New(sha256.New, []byte(secret))
		m.Write([]byte(r.Header.Get("X-Ledgerline-Timestamp") + "\n"))
		m.Write(body)
		if r.Header.Get("X-Ledgerline-Signature") != "sha256="+hex.EncodeToString(m.Sum(nil)) || r.Header.Get("Content-Type") != "application/x-ndjson" || r.Header.Get("Authorization") != "" {
			http.Error(w, "bad signature", 401)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		var ids []string
		sc := bufio.NewScanner(bytes.NewReader(body))
		for sc.Scan() {
			var l map[string]any
			if json.Unmarshal(sc.Bytes(), &l) != nil {
				http.Error(w, "bad line", 400)
				return
			}
			ids = append(ids, l["id"].(string))
		}
		batches = append(batches, ids)
		if refuse != 0 {
			if refuse > 0 {
				refuse--
			}
			http.Error(w, "busy", 503)
			return
		}
		w.WriteHeader(202)
	}))
	defer srv.Close()
	ca := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))
	if _, err := o.SaveConnection(ctx, T, "admin", connSIEMHTTPS, map[string]string{"url": srv.URL, "auth": "HMAC signature", "logs": logChoices[1]}, map[string]string{"secret": secret, "ca_certificate": ca}); err != nil {
		t.Fatal(err)
	}
	if v, err := o.TestConnection(ctx, T, "admin", connSIEMHTTPS); err == nil && v.Status != "Problem" {
		t.Fatalf("test while refusing: %+v", v)
	}
	o.Tick(ctx, T, *clock) // start
	*clock = clock.Add(time.Minute)
	for i := 0; i < 3; i++ {
		o.SecurityEvent(ctx, T, SecEvent{Kind: "signin.ok", Actor: "u" + strconv.Itoa(i), Subject: "u" + strconv.Itoa(i)})
	}
	*clock = clock.Add(time.Minute)
	o.Tick(ctx, T, *clock) // refused (503)
	sc, _ := o.LogExport(ctx, T, "admin")
	st := sc.Dests[1].State
	if sc.Dests[1].Status != "Retrying" || st.Failures != 1 || st.Delivered != 0 || !strings.Contains(st.LastError, "503") || sc.Dests[1].Pending != 3 {
		t.Fatalf("after refusal: %+v %+v %v", sc.Dests[1], st, batches)
	}
	*clock = clock.Add(30 * time.Second)
	o.Tick(ctx, T, *clock) // still waiting to retry
	mu.Lock()
	if len(batches) != 2 { // the connection test and the refused batch
		t.Fatalf("retried too soon: %d", len(batches))
	}
	mu.Unlock()
	*clock = clock.Add(time.Minute)
	o.Tick(ctx, T, *clock) // retried and taken
	mu.Lock()
	if len(batches) != 3 || strings.Join(batches[2], ",") != strings.Join(batches[1], ",") || len(batches[2]) != 3 {
		t.Fatalf("batches: %v", batches)
	}
	mu.Unlock()
	sc, _ = o.LogExport(ctx, T, "admin")
	if st := sc.Dests[1].State; st.Failures != 0 || st.Delivered != 3 || sc.Dests[1].Pending != 0 || st.SecCursor != batches[2][2] {
		t.Fatalf("after delivery: %+v", st)
	}
	// Only events and sign-ins were asked for: no audit lines.
	for _, id := range batches[2] {
		if strings.HasPrefix(id, "audit-") {
			t.Fatalf("audit line sent: %v", batches[2])
		}
	}
	// Down for good: stuck after 30 minutes, a work item for the Admin.
	mu.Lock()
	refuse = -1
	mu.Unlock()
	o.SecurityEvent(ctx, T, SecEvent{Kind: "signin.ok", Subject: "later"})
	for i := 0; i < 8; i++ {
		*clock = clock.Add(8 * time.Minute)
		o.Tick(ctx, T, *clock)
	}
	stuck := func() bool {
		ws, _ := o.WorkItems(ctx, T, "Open")
		for _, w := range ws {
			if w.Process == "logexport" && w.Role == RoleAdmin {
				return true
			}
		}
		return false
	}
	sc, _ = o.LogExport(ctx, T, "admin")
	if sc.Dests[1].Status != "Stuck" || !stuck() {
		t.Fatalf("not stuck: %+v", sc.Dests[1].State)
	}
	mu.Lock()
	refuse = 0
	mu.Unlock()
	*clock = clock.Add(2 * time.Hour)
	o.Tick(ctx, T, *clock)
	if sc, _ = o.LogExport(ctx, T, "admin"); sc.Dests[1].Status != "Delivering" || stuck() {
		t.Fatalf("still stuck: %+v", sc.Dests[1].State)
	}
}

// TestLogDownloadRange: the download holds the events and audit entries of
// the dates asked for, and no others, with identifiers masked.
func TestLogDownloadRange(t *testing.T) {
	o, ctx, _, clock := secSetup(t)
	o.SecurityEvent(ctx, T, SecEvent{Kind: "signin.ok", Subject: "day.one"})
	o.log(ctx, T, "vikram.shah", "client.reveal", "C1001", map[string]any{"field": "PAN", "reason": "for ABCDE1234F", "mobile": "9800000001"})
	*clock = clock.Add(24 * time.Hour)
	o.SecurityEvent(ctx, T, SecEvent{Kind: "signin.ok", Subject: "day.two"})
	if _, _, err := o.LogDownload(ctx, T, "priya.nair", "2026-11-20", "2026-11-20", false); err == nil {
		t.Fatal("maker downloaded")
	}
	if _, _, err := o.LogDownload(ctx, T, "vikram.shah", "2026-11-21", "2026-11-20", false); err == nil {
		t.Fatal("reversed range accepted")
	}
	b, name, err := o.LogDownload(ctx, T, "vikram.shah", "2026-11-20", "2026-11-20", false)
	if err != nil || !strings.HasSuffix(name, ".jsonl.gz") {
		t.Fatal(name, err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(zr)
	text := string(raw)
	if !strings.Contains(text, `"subject":"day.one"`) || strings.Contains(text, "day.two") || !strings.Contains(text, `"action":"client.reveal"`) || strings.Contains(text, "ABCDE1234F") || strings.Contains(text, "9800000001") || !strings.Contains(text, "ABCDEXXXXF") {
		t.Fatalf("download: %s", text)
	}
	for _, l := range strings.Split(strings.TrimSpace(text), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(l), &m) != nil || m["firm"] != T {
			t.Fatalf("line: %s", l)
		}
		if at, _ := m["at"].(string); !strings.HasPrefix(at, "2026-11-20") {
			t.Fatalf("outside the range: %s", l)
		}
	}
	evs, _ := o.SecEvents(ctx, T, "vikram.shah", "2026-11-21", "2026-11-21", "data.logs_downloaded", 0)
	if len(evs) != 1 {
		t.Fatalf("download not recorded: %+v", evs)
	}
}
