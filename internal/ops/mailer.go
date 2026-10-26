package ops

import "ledgerline/internal/mail"

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
