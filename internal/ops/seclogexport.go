package ops

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"ledgerline/internal/app"
	"ledgerline/internal/docs"
)

// ---- log export to the firm's security monitoring (SIEM / SOC) ----
//
// SEBI's CSCRF asks for logs to reach the firm's security operations
// centre; CERT-In asks for them to be kept for 180 days in India. Each firm
// chooses one or more of:
//
//   - syslog over TLS to its SIEM: RFC 5424 messages with RFC 5425
//     octet-counting framing ("MSG-LEN SP SYSLOG-MSG") over TLS 1.2 or
//     later, to host:port (6514 by default), checked against the firm's CA
//     certificate when one is given, otherwise the system's;
//   - HTTPS POST of JSON lines to an address, with a bearer token or an
//     HMAC-SHA256 signature over the timestamp and the body;
//   - a download of its logs for a date range (JSON lines, gzip).
//
// What is sent: the security events (log "security"), the sign-in records
// (log "signin") and the audit trail (log "audit", with identifiers masked
// as described at maskAuditDetail). Each line is one JSON object; the
// syslog message carries the same JSON as its MSG.
//
// Delivery is at least once: each destination keeps a cursor per log (the
// last event ID, the last audit sequence) that moves only after the
// destination took the batch, so after a failure the same lines are sent
// again; a SIEM drops duplicates by the line's "id". A failed send is
// retried with a growing wait (1, 2, 4 … up to 60 minutes); after 30
// minutes of failures the export is stuck and a work item is raised for
// the Admin, closed when lines flow again. A new destination starts from
// the time it was set up; history is in the download.
//
// Syslog over TLS has no acknowledgement in RFC 5425: a batch counts as
// delivered when every frame was written and the TLS connection closed
// cleanly. The structured-data part of each message is "-" (no SD-ID is
// registered for Ledgerline) and MSG is plain UTF-8 JSON without a BOM (the
// MSG-ANY form of RFC 5424 section 6.4), which SIEM JSON parsers expect.

const (
	connSIEMSyslog = "siem-syslog"
	connSIEMHTTPS  = "siem-https"
	cLogExport     = "logexport" // key: the connection ID
	exportBatch    = 500
	exportLag      = 10 * time.Second // lines younger than this wait for the next run, so none written a moment late is skipped
)

var logChoices = []string{"Security events, sign-ins and audit trail", "Security events and sign-ins only"}

func init() {
	fLogs := ConnField{Key: "logs", Label: "What to send", Options: logChoices, Optional: true}
	fCA := ConnField{Key: "ca_certificate", Label: "CA certificate of the SIEM (PEM; empty: the usual public CAs)", Secret: true, Optional: true}
	ConnProviders = append(ConnProviders,
		ConnProvider{ID: connSIEMSyslog, Name: "Security monitoring: syslog over TLS", Group: "Security monitoring", Test: "none",
			Purpose: "Sends security events, sign-ins and the audit trail to the firm's SIEM or SOC as syslog over TLS (RFC 5424 messages, RFC 5425 framing), as SEBI's CSCRF asks. The SIEM must be in India for CERT-In's log rule",
			Fields: []ConnField{{Key: "host", Label: "SIEM host name", Placeholder: "siem.firm.in"}, {Key: "port", Label: "Port", Optional: true, Placeholder: "6514"},
				{Key: "server_name", Label: "Name on the SIEM's certificate, if not the host name", Optional: true}, fCA, fLogs, fNotes}},
		ConnProvider{ID: connSIEMHTTPS, Name: "Security monitoring: HTTPS (JSON lines)", Group: "Security monitoring", Test: "none",
			Purpose: "Sends security events, sign-ins and the audit trail to the firm's SIEM or SOC as JSON lines by HTTPS POST, with a bearer token or an HMAC signature. The SIEM must be in India for CERT-In's log rule",
			Fields: []ConnField{{Key: "url", Label: "Address (https)", Placeholder: "https://siem.firm.in/ingest/ledgerline"},
				{Key: "auth", Label: "Sign-in", Options: []string{"Bearer token", "HMAC signature"}},
				{Key: "secret", Label: "Token or HMAC secret", Secret: true}, fCA, fLogs, fNotes}},
	)
}

// checkSIEMConn checks a log export connection before it is saved.
func checkSIEMConn(c Connection) error {
	if c.Provider == connSIEMSyslog {
		h := c.Values["host"]
		if strings.ContainsAny(h, "/: ") || h == "" {
			return invalid("give only the host name or address of the SIEM, without https:// or a port")
		}
		if p := c.Values["port"]; p != "" {
			if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
				return invalid("the port is a number, usually 6514")
			}
		}
	}
	if pem := c.Secrets["ca_certificate"]; pem != "" {
		if !x509.NewCertPool().AppendCertsFromPEM([]byte(pem)) {
			return invalid("the CA certificate is not a PEM certificate (-----BEGIN CERTIFICATE-----)")
		}
	}
	return nil
}

// ExportState is a destination's progress.
type ExportState struct {
	Dest          string `json:"dest"`
	Started       string `json:"started"`
	SecCursor     string `json:"sec_cursor"`   // the last security event sent (or the start time)
	AuditCursor   int64  `json:"audit_cursor"` // the last audit sequence sent
	LastDelivered string `json:"last_delivered,omitempty"`
	LastBatch     int    `json:"last_batch,omitempty"`
	Delivered     int64  `json:"delivered"`
	Failures      int    `json:"failures"`
	FailingSince  string `json:"failing_since,omitempty"`
	LastError     string `json:"last_error,omitempty"`
	LastErrorAt   string `json:"last_error_at,omitempty"`
	NextTry       string `json:"next_try,omitempty"`
	Stuck         bool   `json:"stuck,omitempty"`
	StuckKey      string `json:"stuck_key,omitempty"`
	LastTest      string `json:"last_test,omitempty"`
	LastTestOK    bool   `json:"last_test_ok,omitempty"`
	LastTestText  string `json:"last_test_text,omitempty"`
}

// exportLine is one line to send.
type exportLine struct {
	log  string // security, signin, audit
	at   time.Time
	sev  string
	json []byte
	// where it moves the cursor
	secID string
	seq   int64
}

// syslogPRI is facility * 8 + severity (RFC 5424 section 6.2.1): facility
// 10 (security/authorization) for security and sign-in events, 13 (log
// audit) for the audit trail.
func (l exportLine) pri() int {
	fac := 10
	if l.log == "audit" {
		fac = 13
	}
	sev := map[string]int{SevCritical: 2, SevHigh: 3, SevMedium: 4, SevLow: 5, SevInfo: 6}[l.sev]
	if sev == 0 {
		sev = 6
	}
	return fac*8 + sev
}

var syslogHostRe = regexp.MustCompile(`[^!-~]`)

func syslogHost() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "-"
	}
	h = syslogHostRe.ReplaceAllString(h, "")
	if len(h) > 255 {
		h = h[:255]
	}
	return firstOf(h, "-")
}

// syslogFrame is one RFC 5424 message with RFC 5425 octet counting:
// "<PRI>1 TIMESTAMP HOSTNAME APP-NAME PROCID MSGID STRUCTURED-DATA MSG",
// preceded by its length in octets and a space.
func syslogFrame(l exportLine, host string) []byte {
	msg := fmt.Sprintf("<%d>1 %s %s ledgerline - %s - %s", l.pri(), l.at.In(ist).Format("2006-01-02T15:04:05.000000Z07:00"), host, l.log, l.json)
	return []byte(strconv.Itoa(len(msg)) + " " + msg)
}

// secLine is a security or sign-in event as exported.
func secLine(tenant string, e SecEvent) exportLine {
	b, _ := json.Marshal(map[string]any{"log": e.Log, "firm": tenant, "id": e.ID, "at": e.At, "kind": e.Kind, "severity": e.Severity, "actor": e.Actor, "subject": e.Subject,
		"ip": e.IP, "message": e.Message, "detail": e.Detail, "incident": e.Incident})
	return exportLine{log: firstOf(e.Log, "security"), at: mustTime(e.At), sev: e.Severity, json: b, secID: e.ID}
}

// siemAuditLine is an audit trail entry as exported, its detail masked.
func siemAuditLine(tenant string, a app.AuditEvent) exportLine {
	b, _ := json.Marshal(map[string]any{"log": "audit", "firm": tenant, "id": "audit-" + strconv.FormatInt(a.Seq, 10), "seq": a.Seq, "at": a.At.In(ist).Format(time.RFC3339Nano),
		"actor": a.Actor, "action": a.Action, "object": maskPANs(a.Object), "detail": maskAuditDetail(a.Detail), "seal": a.Hash})
	return exportLine{log: "audit", at: a.At, sev: SevInfo, json: b, seq: a.Seq}
}

// maskAuditDetail masks what the audit trail may hold of a client's
// identity before it leaves Ledgerline: every PAN in a text value has its
// middle hidden (as the console masks PANs), and values under keys that
// name an identifier (PAN, Aadhaar, account, mobile, phone, e-mail, date of
// birth) keep only their last four characters. Audit entries are written
// without secrets, and the console's audit view shows them as stored; the
// export is stricter.
func maskAuditDetail(d map[string]any) map[string]any {
	if len(d) == 0 {
		return nil
	}
	out := make(map[string]any, len(d))
	for k, v := range d {
		out[k] = maskAuditValue(strings.ToLower(k), v)
	}
	return out
}

var reIdentKey = regexp.MustCompile(`pan$|^pan|aadhaar|aadhar|uid$|account|acct|mobile|phone|email|e_mail|dob|birth`)

func maskAuditValue(k string, v any) any {
	switch x := v.(type) {
	case string:
		if reIdentKey.MatchString(k) && len(x) > 4 && !strings.Contains(k, "count") {
			return strings.Repeat("X", min(len(x)-4, 12)) + x[len(x)-4:]
		}
		return maskPANs(x)
	case map[string]any:
		return maskAuditDetail(x)
	case []any:
		ys := make([]any, len(x))
		for i, y := range x {
			ys[i] = maskAuditValue(k, y)
		}
		return ys
	case []string:
		ys := make([]any, len(x))
		for i, y := range x {
			ys[i] = maskAuditValue(k, y)
		}
		return ys
	}
	return v
}

// auditScan reads the firm's audit trail in order after a sequence number.
func (o *Ops) auditScan(ctx context.Context, tenant string, after int64, limit int) ([]app.AuditEvent, error) {
	var s app.AuditStore = o.App.Audit
	if va := o.vaultAudit(); va != nil {
		s = va.Inner
	}
	c, ok := s.(app.ChainedAudit)
	if !ok {
		return nil, errors.New("this server's audit store cannot be read in order")
	}
	return c.Scan(ctx, tenant, after, limit)
}

func (o *Ops) auditLastSeq(ctx context.Context, tenant string) int64 {
	xs, _ := o.App.Audit.List(ctx, tenant, 1)
	if len(xs) == 0 {
		return 0
	}
	return xs[0].Seq
}

// pendingLines are the next lines for a destination after its cursors.
func (o *Ops) pendingLines(ctx context.Context, tenant string, st ExportState, withAudit bool, max int) ([]exportLine, error) {
	var out []exportLine
	until := o.now().Add(-exportLag).UTC().Format("2006-01-02T15:04:05.000000Z")
	cur := st.SecCursor
	start, err := time.Parse("2006-01-02", firstOf(cur, until)[:10])
	if err != nil {
		start = o.now().UTC()
	}
	for d := start; d.Format("2006-01-02") <= until[:10] && len(out) < max; d = d.Add(24 * time.Hour) {
		xs, err := docs.List[SecEvent](ctx, o.Docs, tenant, cSecEvent, d.Format("2006-01-02"))
		if err != nil {
			return nil, err
		}
		for _, x := range xs {
			if x.ID > cur && x.ID <= until && len(out) < max {
				out = append(out, secLine(tenant, x))
			}
		}
	}
	if withAudit && len(out) < max {
		auditUntil := o.App.Now().Add(-exportLag)
		as, err := o.auditScan(ctx, tenant, st.AuditCursor, max-len(out))
		if err != nil {
			return nil, err
		}
		for _, a := range as {
			if a.At.After(auditUntil) {
				break
			}
			out = append(out, siemAuditLine(tenant, a))
		}
	}
	return out, nil
}

// exportTLS is the TLS set-up for a destination.
func exportTLS(c Connection, serverName string) (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: serverName}
	if pem := c.Secrets["ca_certificate"]; pem != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(pem)) {
			return nil, errors.New("the CA certificate is not a PEM certificate")
		}
		cfg.RootCAs = pool
	}
	return cfg, nil
}

// sendSyslog writes the lines to the SIEM over TLS.
func (o *Ops) sendSyslog(ctx context.Context, c Connection, lines []exportLine) error {
	host, port := c.Values["host"], firstOf(c.Values["port"], "6514")
	cfg, err := exportTLS(c, firstOf(c.Values["server_name"], host))
	if err != nil {
		return err
	}
	dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	d := &tls.Dialer{NetDialer: &net.Dialer{Timeout: 15 * time.Second}, Config: cfg}
	nc, err := d.DialContext(dctx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		return fmt.Errorf("can't connect to %s:%s over TLS: %w", host, port, err)
	}
	conn := nc.(*tls.Conn)
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(60 * time.Second))
	hn := syslogHost()
	var buf bytes.Buffer
	for _, l := range lines {
		buf.Write(syslogFrame(l, hn))
	}
	if _, err := conn.Write(buf.Bytes()); err != nil {
		return fmt.Errorf("sending to %s:%s: %w", host, port, err)
	}
	if err := conn.CloseWrite(); err != nil { // TLS close_notify: the end of this batch
		return fmt.Errorf("closing the connection to %s:%s: %w", host, port, err)
	}
	return nil
}

// HMAC signing of HTTPS exports: X-Ledgerline-Signature is "sha256=" and
// the hex HMAC-SHA256, keyed with the secret, of X-Ledgerline-Timestamp
// (Unix seconds), a newline and the body. The receiver recomputes it and
// should refuse a timestamp more than 5 minutes off.
func exportSignature(secret, ts string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(ts + "\n"))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

// sendHTTPS posts the lines as one batch of JSON lines.
func (o *Ops) sendHTTPS(ctx context.Context, tenant string, c Connection, lines []exportLine, batch string) error {
	var body bytes.Buffer
	for _, l := range lines {
		body.Write(l.json)
		body.WriteByte('\n')
	}
	cfg, err := exportTLS(c, "")
	if err != nil {
		return err
	}
	cl := &http.Client{Timeout: 60 * time.Second, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, TLSClientConfig: cfg},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Values["url"], bytes.NewReader(body.Bytes()))
	if err != nil {
		return err
	}
	ts := strconv.FormatInt(o.now().Unix(), 10)
	req.Header.Set("Content-Type", "application/x-ndjson")
	req.Header.Set("User-Agent", "Ledgerline-log-export/1")
	req.Header.Set("X-Ledgerline-Firm", tenant)
	req.Header.Set("X-Ledgerline-Batch", batch)
	req.Header.Set("X-Ledgerline-Timestamp", ts)
	if c.Values["auth"] == "HMAC signature" {
		req.Header.Set("X-Ledgerline-Signature", exportSignature(c.Secrets["secret"], ts, body.Bytes()))
	} else {
		req.Header.Set("Authorization", "Bearer "+c.Secrets["secret"])
	}
	resp, err := cl.Do(req)
	if err != nil {
		msg := err.Error()
		if i := strings.LastIndex(msg, "\": "); i >= 0 {
			msg = msg[i+3:]
		}
		return errors.New("can't reach the SIEM: " + msg)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("the SIEM answered %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

func (o *Ops) sendLines(ctx context.Context, tenant string, c Connection, lines []exportLine, batch string) error {
	if c.Provider == connSIEMSyslog {
		return o.sendSyslog(ctx, c, lines)
	}
	return o.sendHTTPS(ctx, tenant, c, lines, batch)
}

func (o *Ops) exportConn(ctx context.Context, tenant, id string) (Connection, bool) {
	c, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, id)
	if err != nil || c.Status == "Not set" || c.Provider == "" {
		return c, false
	}
	return c, true
}

// logExportTick sends what is due to each destination the firm set up.
func (o *Ops) logExportTick(ctx context.Context, tenant string, now time.Time) {
	for _, id := range []string{connSIEMSyslog, connSIEMHTTPS} {
		if c, ok := o.exportConn(ctx, tenant, id); ok {
			o.exportRun(ctx, tenant, c, now.In(ist))
		}
	}
}

// newExportState starts a destination at now: its cursors are past every
// event and audit entry written so far ("|99999999" sorts after every
// sequence number of the same microsecond).
func (o *Ops) newExportState(ctx context.Context, tenant, id string, now time.Time) ExportState {
	return ExportState{Dest: id, Started: now.Format(time.RFC3339), SecCursor: now.UTC().Format("2006-01-02T15:04:05.000000Z") + "|99999999", AuditCursor: o.auditLastSeq(ctx, tenant)}
}

// exportRun sends up to five batches to one destination, unless it is
// waiting to retry.
func (o *Ops) exportRun(ctx context.Context, tenant string, c Connection, now time.Time) ExportState {
	st, _, err := docs.Get[ExportState](ctx, o.Docs, tenant, cLogExport, c.Provider)
	if err != nil {
		st = o.newExportState(ctx, tenant, c.Provider, now)
		_ = docs.Upsert(ctx, o.Docs, tenant, cLogExport, c.Provider, st, SystemActor)
		return st
	}
	if st.NextTry != "" && now.Before(mustTime(st.NextTry)) {
		return st
	}
	withAudit := c.Values["logs"] != logChoices[1]
	for i := 0; i < 5; i++ {
		lines, err := o.pendingLines(ctx, tenant, st, withAudit, exportBatch)
		if err == nil && len(lines) == 0 {
			break
		}
		if err == nil {
			batch := fmt.Sprintf("%s-%s-%d", c.Provider, now.UTC().Format("20060102T150405"), i)
			err = o.sendLines(ctx, tenant, c, lines, batch)
		}
		if err != nil {
			st = o.exportFailed(ctx, tenant, st, err, now)
			break
		}
		for _, l := range lines {
			if l.secID != "" {
				st.SecCursor = l.secID
			}
			if l.seq > st.AuditCursor {
				st.AuditCursor = l.seq
			}
		}
		st.LastDelivered, st.LastBatch, st.Delivered = now.Format(time.RFC3339), len(lines), st.Delivered+int64(len(lines))
		st.Failures, st.FailingSince, st.NextTry = 0, "", ""
		if st.Stuck {
			st.Stuck = false
			o.closeWork(ctx, tenant, SystemActor, "logexport", st.StuckKey, "lines are being delivered again")
		}
		if len(lines) < exportBatch {
			break
		}
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cLogExport, c.Provider, st, SystemActor)
	return st
}

// exportFailed records a failure and when to try again; after 30 minutes
// of failures the export is stuck and the Admin gets a work item.
func (o *Ops) exportFailed(ctx context.Context, tenant string, st ExportState, err error, now time.Time) ExportState {
	st.Failures++
	if st.FailingSince == "" {
		st.FailingSince = now.Format(time.RFC3339)
	}
	st.LastError, st.LastErrorAt = trim(err.Error(), 300), now.Format(time.RFC3339)
	wait := time.Minute << min(st.Failures-1, 6)
	if wait > time.Hour {
		wait = time.Hour
	}
	st.NextTry = now.Add(wait).Format(time.RFC3339)
	if !st.Stuck && now.Sub(mustTime(st.FailingSince)) >= 30*time.Minute {
		st.Stuck, st.StuckKey = true, st.Dest+"|"+now.Format("2006-01-02T15:04")
		name := map[string]string{connSIEMSyslog: "syslog over TLS", connSIEMHTTPS: "HTTPS"}[st.Dest]
		o.raiseWork(ctx, tenant, "logexport", st.StuckKey, "Log export to the SIEM ("+name+") is stuck",
			"Security logs have not reached the firm's SIEM since "+mustTime(st.FailingSince).In(ist).Format("02 Jan 15:04")+": "+st.LastError+". Nothing is lost: they are sent once it answers. Check the SIEM and the connection under Settings → Connections.", "", RoleAdmin, "logexport", now.Format("2006-01-02"))
		o.SecurityEvent(ctx, tenant, SecEvent{Kind: "logexport.stuck", Actor: SystemActor, Subject: st.Dest, Message: "Log export (" + name + ") failing since " + mustTime(st.FailingSince).In(ist).Format("15:04") + ": " + st.LastError})
	}
	return st
}

// ---- the Log export screen ----

// LogExportDest is one destination on the screen.
type LogExportDest struct {
	ID      string       `json:"id"`
	Name    string       `json:"name"`
	Set     bool         `json:"set"`
	Target  string       `json:"target,omitempty"` // host:port or the address
	Auth    string       `json:"auth,omitempty"`
	Logs    string       `json:"logs,omitempty"`
	Status  string       `json:"status"` // Not set, Waiting to start, Delivering, Retrying, Stuck
	State   *ExportState `json:"state,omitempty"`
	Pending int          `json:"pending"` // lines waiting (up to a batch)
}

// LogExportScreen is the Log export screen.
type LogExportScreen struct {
	Dests     []LogExportDest `json:"dests"`
	Retention string          `json:"retention"`
	Days      int             `json:"retention_days"`
	Fields    string          `json:"fields"`
}

// LogExport shows each destination's state. Compliance and Admin.
func (o *Ops) LogExport(ctx context.Context, tenant, actor string) (LogExportScreen, error) {
	if err := o.require(ctx, tenant, actor, "Seeing and testing the log export", RoleCompliance); err != nil {
		return LogExportScreen{}, err
	}
	set := o.SecuritySettings(ctx, tenant)
	sc := LogExportScreen{Retention: retentionLine(set), Days: set.RetentionDays,
		Fields: "Each line is one JSON object: log (security, signin or audit), firm, id, at (IST), and for events kind, severity, actor, subject, ip, message, detail and incident; for the audit trail seq, actor, action, object, detail (identifiers masked) and seal."}
	for _, id := range []string{connSIEMSyslog, connSIEMHTTPS} {
		p, _ := connProvider(id)
		d := LogExportDest{ID: id, Name: p.Name, Status: "Not set"}
		if c, ok := o.exportConn(ctx, tenant, id); ok {
			d.Set, d.Logs = true, firstOf(c.Values["logs"], logChoices[0])
			if id == connSIEMSyslog {
				d.Target = c.Values["host"] + ":" + firstOf(c.Values["port"], "6514")
			} else {
				d.Target, d.Auth = c.Values["url"], c.Values["auth"]
			}
			d.Status = "Waiting to start"
			if st, _, err := docs.Get[ExportState](ctx, o.Docs, tenant, cLogExport, id); err == nil {
				d.State = &st
				switch {
				case st.Stuck:
					d.Status = "Stuck"
				case st.Failures > 0:
					d.Status = "Retrying"
				default:
					d.Status = "Delivering"
				}
				if ls, err := o.pendingLines(ctx, tenant, st, d.Logs != logChoices[1], exportBatch); err == nil {
					d.Pending = len(ls)
				}
			}
		}
		sc.Dests = append(sc.Dests, d)
	}
	return sc, nil
}

// TestLogExport sends one test line to a destination now, without moving
// its cursor. Compliance and Admin.
func (o *Ops) TestLogExport(ctx context.Context, tenant, actor, id string) (LogExportDest, error) {
	if err := o.require(ctx, tenant, actor, "Seeing and testing the log export", RoleCompliance); err != nil {
		return LogExportDest{}, err
	}
	if id != connSIEMSyslog && id != connSIEMHTTPS {
		return LogExportDest{}, ErrNotFound
	}
	c, ok := o.exportConn(ctx, tenant, id)
	if !ok {
		return LogExportDest{}, invalid("set the destination up under Settings → Connections first")
	}
	e := o.SecurityEvent(ctx, tenant, SecEvent{Kind: "logexport.test", Actor: actor, Subject: id, Message: "Test line from Ledgerline's log export, sent by " + actor})
	now := o.now()
	err := o.sendLines(ctx, tenant, c, []exportLine{secLine(tenant, e)}, id+"-test-"+now.UTC().Format("20060102T150405"))
	st, _, gerr := docs.Get[ExportState](ctx, o.Docs, tenant, cLogExport, id)
	if gerr != nil {
		st = o.newExportState(ctx, tenant, id, now)
	}
	st.LastTest, st.LastTestOK, st.LastTestText = now.Format(time.RFC3339), err == nil, "Delivered"
	if err != nil {
		st.LastTestText = trim(err.Error(), 300)
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cLogExport, id, st, actor)
	o.log(ctx, tenant, actor, "security.logexport.tested", id, map[string]any{"ok": err == nil, "result": st.LastTestText})
	sc, lerr := o.LogExport(ctx, tenant, actor)
	if lerr != nil {
		return LogExportDest{}, lerr
	}
	for _, d := range sc.Dests {
		if d.ID == id {
			return d, nil
		}
	}
	return LogExportDest{}, ErrNotFound
}

// LogDownload is the firm's logs between two IST dates as gzip'd JSON
// lines: security events and sign-ins, and (unless onlyEvents) the audit
// trail. Compliance and Admin; the download is itself a security event.
func (o *Ops) LogDownload(ctx context.Context, tenant, actor, from, to string, onlyEvents bool) ([]byte, string, error) {
	if err := o.require(ctx, tenant, actor, "Downloading security logs", RoleCompliance); err != nil {
		return nil, "", err
	}
	evs, err := o.secEventsBetween(ctx, tenant, from, to)
	if err != nil {
		return nil, "", err
	}
	a, _ := time.ParseInLocation("2006-01-02", from, ist)
	b, _ := time.ParseInLocation("2006-01-02", to, ist)
	end := b.Add(24 * time.Hour)
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	n := 0
	for _, e := range evs {
		zw.Write(secLine(tenant, e).json)
		zw.Write([]byte{'\n'})
		n++
	}
	if !onlyEvents {
		after := int64(0)
		for {
			as, err := o.auditScan(ctx, tenant, after, 5000)
			if err != nil {
				return nil, "", err
			}
			if len(as) == 0 {
				break
			}
			for _, x := range as {
				after = x.Seq
				if !x.At.Before(a) && x.At.Before(end) {
					zw.Write(siemAuditLine(tenant, x).json)
					zw.Write([]byte{'\n'})
					n++
				}
			}
			if len(as) < 5000 || !as[len(as)-1].At.Before(end) {
				break
			}
		}
	}
	if err := zw.Close(); err != nil {
		return nil, "", err
	}
	o.SecurityEvent(ctx, tenant, SecEvent{Kind: "data.logs_downloaded", Actor: actor, Subject: from + " to " + to, Message: fmt.Sprintf("%s downloaded the security logs from %s to %s (%d lines)", actor, from, to, n), Detail: map[string]any{"lines": n, "audit": !onlyEvents}})
	return buf.Bytes(), fmt.Sprintf("ledgerline-logs-%s-%s-to-%s.jsonl.gz", tenant, from, to), nil
}

// siemTest is the Connections test of a log export destination: one test
// line, sent now.
func (o *Ops) siemTest(ctx context.Context, tenant, actor string, c Connection) (string, string) {
	e := o.SecurityEvent(ctx, tenant, SecEvent{Kind: "logexport.test", Actor: actor, Subject: c.Provider, Message: "Test line from Ledgerline's log export, sent by " + actor})
	if err := o.sendLines(ctx, tenant, c, []exportLine{secLine(tenant, e)}, c.Provider+"-test-"+o.now().UTC().Format("20060102T150405")); err != nil {
		return "Problem", trim(err.Error(), 300)
	}
	return "Working", "a test line was delivered"
}
