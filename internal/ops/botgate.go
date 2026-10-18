package ops

import (
	"context"
	"crypto/hmac"
	"fmt"
	"regexp"
	"strings"
	"time"

	"ledgerline/internal/docs"
)

// ---- one-time code for account information on WhatsApp ----
//
// Anyone holding the client's phone can write from the registered number,
// so answers that show balances, holdings, the ledger, contract notes,
// margin or tax need a one-time code sent to the client's registered email.
// A verified session lasts for the hours set in Settings (24 by default).

const cWASession = "wasession"

// botSensitive are the commands that show account information.
var botSensitive = map[string]bool{"BAL": true, "BALANCE": true, "HOLD": true, "HOLDINGS": true, "LEDGER": true, "CN": true, "MARGIN": true, "TAX": true, "OFFERS": true}

var reBotCode = regexp.MustCompile(`^\d{6}$`)

type waSession struct {
	Mobile     string `json:"mobile"`
	Hash       string `json:"hash,omitempty"`
	Expires    string `json:"expires,omitempty"`
	Tries      int    `json:"tries,omitempty"`
	PendingCmd string `json:"pending_cmd,omitempty"`
	PendingUCC string `json:"pending_ucc,omitempty"`
	Verified   string `json:"verified,omitempty"`
}

func (o *Ops) botSessionHours(set Settings) int64 {
	if set.WABotSessionHours > 0 {
		return set.WABotSessionHours
	}
	return 24
}

// botGate decides whether a message may be answered now. It returns a reply
// to send instead (stop), or the command and account to answer, which may
// be a command held back until the code was given.
func (o *Ops) botGate(ctx context.Context, tenant, mobile string, c Client, text, cmd string, set Settings) (reply string, stop bool, runCmd, runUCC string) {
	now := o.now()
	s, _, _ := docs.Get[waSession](ctx, o.Docs, tenant, cWASession, mobile)
	s.Mobile = mobile
	verified := false
	if t, err := time.Parse(time.RFC3339, s.Verified); err == nil && now.Sub(t) < time.Duration(o.botSessionHours(set))*time.Hour {
		verified = true
	}
	code := strings.ReplaceAll(strings.TrimSpace(text), " ", "")
	if reBotCode.MatchString(code) && s.Hash != "" {
		exp, _ := time.Parse(time.RFC3339, s.Expires)
		switch {
		case now.After(exp):
			s.Hash = ""
			reply = "That code has expired. Send your request again and we will send a new code."
		case s.Tries >= 3:
			s.Hash = ""
			reply = "Too many wrong codes. Send your request again for a new code."
		case !hmac.Equal([]byte(hashOTP(mobile, code)), []byte(s.Hash)):
			s.Tries++
			reply = "That code is not right. Please check the email and try again."
		default:
			s.Hash, s.Tries, s.Verified = "", 0, now.Format(time.RFC3339)
			runCmd, runUCC = s.PendingCmd, s.PendingUCC
			s.PendingCmd, s.PendingUCC = "", ""
			_ = docs.Upsert(ctx, o.Docs, tenant, cWASession, mobile, s, "whatsapp")
			o.log(ctx, tenant, "whatsapp", "whatsapp.verified", maskMobile(mobile), nil)
			return "", false, firstOf(runCmd, "HELP"), firstOf(runUCC, c.UCC)
		}
		_ = docs.Upsert(ctx, o.Docs, tenant, cWASession, mobile, s, "whatsapp")
		return reply, true, "", ""
	}
	if !botSensitive[cmd] || verified {
		return "", false, cmd, c.UCC
	}
	if !reEmail.MatchString(c.Email) || strings.HasSuffix(c.Email, ".invalid") {
		return "We need to confirm it is you, but there is no valid email address on your account. Please update it in the client portal or contact us.", true, "", ""
	}
	otp := otpCode()
	s.Hash, s.Tries, s.Expires = hashOTP(mobile, otp), 0, now.Add(10*time.Minute).Format(time.RFC3339)
	s.PendingCmd, s.PendingUCC = cmd, c.UCC
	_ = docs.Upsert(ctx, o.Docs, tenant, cWASession, mobile, s, "whatsapp")
	o.sendDirect(ctx, tenant, "whatsapp", ChEmail, c.Email, c.UCC, "whatsapp.code", "Your code for WhatsApp: "+otp,
		fmt.Sprintf("Dear %s,\n\nYour code to see your account on WhatsApp is %s. It is valid for 10 minutes. Do not share it with anyone; we will never ask for it on a call.\n\nIf you did not ask for it, someone may have your phone: please call us.\n\n%s", c.Name, otp, set.Firm))
	reply = fmt.Sprintf("To keep your account safe, we have sent a 6-digit code to your registered email %s. Reply with the code to continue.", maskEmail(c.Email))
	if o.DevCodes {
		reply += "\n(Development server: the code is " + otp + ".)"
	}
	return reply, true, "", ""
}
