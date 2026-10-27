package ops

import (
	"context"
	"fmt"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/mail"
)

// Mail, attachments and the mail server are in package mail; these names
// keep the ops code readable.
type (
	Mail       = mail.Message
	MailFile   = mail.File
	Mailer     = mail.Sender
	Rejected   = mail.Rejected
	SMTPMailer = mail.SMTP
)

// ParseSMTP reads a mail server address (smtp:// or smtps://).
var ParseSMTP = mail.ParseSMTP

func newMessageID(domain string) string { return mail.NewMessageID(domain) }

// mailStaff emails the firm's own people (management, compliance): backup
// alerts and scheduled reports. Each is kept in the outbox with what really
// happened, so "emailed" is never claimed when no mail server is set.
func (o *Ops) mailStaff(ctx context.Context, tenant, to, subject, text, ref string, attach []MailFile) string {
	set, _ := o.Settings(ctx, tenant)
	result := "Not sent: no mail server (set one in Connections)"
	if m := o.mailerFor(ctx, tenant); m != nil {
		if _, err := m.Send(ctx, Mail{From: mailFrom(m, set), To: to, Subject: subject, Text: text, Attach: attach}); err != nil {
			result = "Failed: " + err.Error()
		} else {
			result = "Sent"
		}
	} else if o.DevCodes || o.devFirm(ctx, tenant) {
		result = "Sent (simulated)"
	}
	if n, err := o.next(ctx, tenant, "outbox", SystemActor); err == nil {
		_ = docs.Upsert(ctx, o.Docs, tenant, cOutbox, fmt.Sprintf("%08d", n), OutboxItem{ID: fmt.Sprintf("%08d", n), To: to, Subject: subject, Ref: ref, At: o.now().Format(time.RFC3339), Result: result}, SystemActor)
	}
	return result
}

// Outbox lists the mail sent to the firm's own people, newest first.
func (o *Ops) Outbox(ctx context.Context, tenant, actor string) ([]OutboxItem, error) {
	if err := o.require(ctx, tenant, actor, "Seeing mail sent to staff", RoleAdmin, RoleCompliance); err != nil {
		return nil, err
	}
	xs, err := docs.List[OutboxItem](ctx, o.Docs, tenant, cOutbox, "")
	for i, j := 0, len(xs)-1; i < j; i, j = i+1, j-1 {
		xs[i], xs[j] = xs[j], xs[i]
	}
	if len(xs) > 500 {
		xs = xs[:500]
	}
	return xs, err
}
