// Package mail sends email with attachments through a mail server.
package mail

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"net/textproto"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Message is one email with attachments.
type Message struct {
	From    string
	To      string
	CC      []string
	Subject string
	Text    string
	Attach  []File
}

// File is an attachment.
type File struct {
	Name, Type string
	Data       []byte
}

// Sender sends mail. A *Rejected error means the address or message was
// refused for good (a bounce); any other error is worth retrying.
type Sender interface {
	Send(ctx context.Context, m Message) (messageID string, err error)
}

var reEmail = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

// ValidAddress reports whether s looks like an email address.
func ValidAddress(s string) bool { return reEmail.MatchString(s) }

// Rejected is a permanent refusal by the receiving server.
type Rejected struct{ Reason string }

func (r *Rejected) Error() string { return "rejected: " + r.Reason }

// SMTP sends through a mail server: smtp://user:pass@host:587 (STARTTLS)
// or smtps://user:pass@host:465 (TLS), ?from=notes@firm.in.
type SMTP struct {
	Host, Port, User, Pass, From string
	Implicit                     bool
	Timeout                      time.Duration
	// InsecureSkipVerify is for test servers only.
	InsecureSkipVerify bool
}

// ParseSMTP reads a mail server address.
func ParseSMTP(raw string) (*SMTP, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil, errors.New("mail server must look like smtp://user:password@host:587?from=notes@firm.in")
	}
	m := &SMTP{Host: u.Hostname(), Port: u.Port(), From: u.Query().Get("from"), Timeout: 60 * time.Second}
	switch u.Scheme {
	case "smtp":
		if m.Port == "" {
			m.Port = "587"
		}
	case "smtps":
		m.Implicit = true
		if m.Port == "" {
			m.Port = "465"
		}
	default:
		return nil, errors.New("mail server scheme must be smtp or smtps")
	}
	if u.User != nil {
		m.User = u.User.Username()
		m.Pass, _ = u.User.Password()
	}
	if !reEmail.MatchString(m.From) {
		return nil, errors.New("mail server needs ?from=<the sending address>")
	}
	return m, nil
}

// NewMessageID makes a Message-ID for a domain.
func NewMessageID(domain string) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return fmt.Sprintf("<%x@%s>", b, domain)
}

// Build makes the MIME message.
func (m Message) Build(id string) []byte {
	var b bytes.Buffer
	h := func(k, v string) { fmt.Fprintf(&b, "%s: %s\r\n", k, v) }
	h("From", m.From)
	h("To", m.To)
	if len(m.CC) > 0 {
		h("Cc", strings.Join(m.CC, ", "))
	}
	h("Subject", mime.QEncoding.Encode("utf-8", m.Subject))
	h("Date", time.Now().Format(time.RFC1123Z))
	h("Message-ID", id)
	h("MIME-Version", "1.0")
	bnd := fmt.Sprintf("LL-%x", time.Now().UnixNano())
	h("Content-Type", `multipart/mixed; boundary="`+bnd+`"`)
	b.WriteString("\r\n")
	fmt.Fprintf(&b, "--%s\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n%s\r\n", bnd, strings.ReplaceAll(m.Text, "\n", "\r\n"))
	for _, a := range m.Attach {
		fmt.Fprintf(&b, "--%s\r\nContent-Type: %s; name=%q\r\nContent-Disposition: attachment; filename=%q\r\nContent-Transfer-Encoding: base64\r\n\r\n", bnd, contentType(a.Type), a.Name, a.Name)
		enc := base64.StdEncoding.EncodeToString(a.Data)
		for len(enc) > 76 {
			b.WriteString(enc[:76] + "\r\n")
			enc = enc[76:]
		}
		b.WriteString(enc + "\r\n")
	}
	fmt.Fprintf(&b, "--%s--\r\n", bnd)
	return b.Bytes()
}

// Check connects, starts TLS and signs in without sending anything: it
// proves the address, the certificate and the login.
func (s *SMTP) Check(ctx context.Context) error {
	c, conn, err := s.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	defer c.Close()
	return c.Quit()
}

func (s *SMTP) dial(ctx context.Context) (*smtp.Client, net.Conn, error) {
	addr := net.JoinHostPort(s.Host, s.Port)
	d := net.Dialer{Timeout: s.timeout()}
	tlsCfg := &tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12, InsecureSkipVerify: s.InsecureSkipVerify}
	var conn net.Conn
	var err error
	if s.Implicit {
		conn, err = tls.DialWithDialer(&d, "tcp", addr, tlsCfg)
	} else {
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("mail server: %w", err)
	}
	_ = conn.SetDeadline(time.Now().Add(s.timeout()))
	c, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("mail server: %w", err)
	}
	if !s.Implicit {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			c.Close()
			return nil, nil, errors.New("mail server does not offer STARTTLS; refusing to send in clear")
		}
		if err := c.StartTLS(tlsCfg); err != nil {
			c.Close()
			return nil, nil, fmt.Errorf("mail server TLS: %w", err)
		}
	}
	if s.User != "" {
		if err := c.Auth(smtp.PlainAuth("", s.User, s.Pass, s.Host)); err != nil {
			c.Close()
			return nil, nil, fmt.Errorf("mail server login: %w", err)
		}
	}
	return c, conn, nil
}

func (s *SMTP) timeout() time.Duration {
	if s.Timeout <= 0 {
		return 60 * time.Second
	}
	return s.Timeout
}

// Send delivers one message.
func (s *SMTP) Send(ctx context.Context, m Message) (string, error) {
	if m.From == "" {
		m.From = s.From
	}
	c, conn, err := s.dial(ctx)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	defer c.Close()
	if err := c.Mail(m.From); err != nil {
		return "", classify(err)
	}
	for _, r := range append([]string{m.To}, m.CC...) {
		if err := c.Rcpt(r); err != nil {
			return "", classify(err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return "", classify(err)
	}
	domain := s.From[strings.LastIndex(s.From, "@")+1:]
	id := NewMessageID(domain)
	if _, err := w.Write(m.Build(id)); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", classify(err)
	}
	_ = c.Quit()
	return id, nil
}

// classify turns a 5xx reply into a bounce; 4xx and others can be retried.
func classify(err error) error {
	var te *textproto.Error
	if errors.As(err, &te) && te.Code >= 500 && te.Code < 600 {
		return &Rejected{Reason: fmt.Sprintf("%d %s", te.Code, te.Msg)}
	}
	return err
}

func contentType(t string) string {
	if t == "" {
		return "application/octet-stream"
	}
	return t
}
