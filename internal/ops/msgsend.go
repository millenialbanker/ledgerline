package ops

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"ledgerline/internal/docs"
)

// ---- sending SMS and WhatsApp through the firm's provider ----
//
// Every client message is written to the message log first. When the firm
// has connected an SMS gateway or WhatsApp provider (Settings →
// Connections), it is then sent at once, and the provider's reference is
// kept with it. A provider that does not answer leaves the message
// "Queued"; the scheduler tries again, three times in all, then marks it
// Failed. With no provider connected the message stays in the log as "Not
// sent", so nothing is ever claimed as sent that was not (a development
// firm shows "Sent" with a "simulated" note).
//
// SMS: MSG91, Kaleyra, Gupshup Enterprise and Route Mobile are built in,
// each with the DLT entity and template IDs TRAI requires; "Other" takes the
// gateway's own address with {to}, {text}, {sender}, {entity}, {template}
// and {key} in it.
//
// WhatsApp: Meta's Cloud API, Gupshup and Interakt. A message the firm
// starts must use a template Meta has approved (its name is set on the
// Ledgerline template; the values go in the order they appear in the body).
// Within 24 hours of the client writing in, plain text may be sent, which
// is how the WhatsApp bot answers.

// msgHTTP is replaced in tests.
var msgHTTP = &http.Client{Timeout: 15 * time.Second}

const msgMaxAttempts = 3

// sendErr is a provider's refusal; transient ones are tried again.
type sendErr struct {
	msg       string
	transient bool
}

func (e sendErr) Error() string { return e.msg }

func msgRequest(ctx context.Context, method, u string, hdr map[string]string, body io.Reader) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return 0, nil, sendErr{msg: "bad provider address: " + err.Error()}
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := msgHTTP.Do(req)
	if err != nil {
		return 0, nil, sendErr{msg: "no answer from the provider", transient: true}
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode >= 500 || resp.StatusCode == 429 {
		return resp.StatusCode, b, sendErr{msg: fmt.Sprintf("provider answered %d", resp.StatusCode), transient: true}
	}
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return resp.StatusCode, b, sendErr{msg: "the provider refused the key; check Settings → Connections"}
	}
	return resp.StatusCode, b, nil
}

// msisdn is a mobile number with India's country code: 91XXXXXXXXXX.
func msisdn(m string) string {
	d := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, m)
	if len(d) > 10 {
		d = d[len(d)-10:]
	}
	return "91" + d
}

func providerBase(c Connection, def string) string {
	if u := strings.TrimRight(strings.TrimSpace(c.Values["url"]), "/"); u != "" {
		return u
	}
	return def
}

// ---- SMS ----

// SMSProviders are the gateways with a built-in sender.
var SMSProviders = []string{"MSG91", "Kaleyra", "Gupshup", "Route Mobile", "Other"}

func checkSMSConn(c Connection) error {
	v := c.Values
	switch v["provider"] {
	case "MSG91", "Kaleyra", "Gupshup", "Route Mobile":
		if v["sender"] == "" || v["dlt_entity"] == "" {
			return invalid("enter the DLT sender ID (header) and the DLT entity ID")
		}
	}
	switch v["provider"] {
	case "Kaleyra":
		if v["user"] == "" {
			return invalid("enter Kaleyra's account SID")
		}
	case "Gupshup", "Route Mobile":
		if v["user"] == "" {
			return invalid("enter the gateway's user name (user ID)")
		}
	case "Other":
		if u := v["url"]; u != "" && !strings.Contains(u, "{to}") {
			return invalid("the gateway address must contain {to} and {text} where the number and message go")
		}
	}
	return nil
}

// sendSMS sends one SMS with the connected gateway and returns its reference.
func sendSMS(ctx context.Context, c Connection, to, text, dltTemplate, category string) (string, error) {
	v, key := c.Values, c.Secrets["api_key"]
	num := msisdn(to)
	switch v["provider"] {
	case "MSG91":
		q := url.Values{"authkey": {key}, "mobiles": {num}, "message": {text}, "sender": {v["sender"]}, "route": {"4"}, "country": {"91"}, "DLT_TE_ID": {dltTemplate}}
		_, b, err := msgRequest(ctx, http.MethodGet, providerBase(c, "https://api.msg91.com")+"/api/sendhttp.php?"+q.Encode(), nil, nil)
		if err != nil {
			return "", err
		}
		s := strings.TrimSpace(string(b))
		if s == "" || strings.Contains(strings.ToLower(s), "error") || strings.Contains(strings.ToLower(s), "fail") {
			return "", sendErr{msg: "MSG91: " + trimBody(b)}
		}
		return s, nil
	case "Kaleyra":
		typ := "TXN"
		if category == "Promotional" {
			typ = "MKT"
		}
		form := url.Values{"to": {"+" + num}, "sender": {v["sender"]}, "body": {text}, "type": {typ}, "template_id": {dltTemplate}}
		st, b, err := msgRequest(ctx, http.MethodPost, providerBase(c, "https://api.kaleyra.io")+"/v1/"+url.PathEscape(v["user"])+"/messages",
			map[string]string{"api-key": key, "Content-Type": "application/x-www-form-urlencoded"}, strings.NewReader(form.Encode()))
		if err != nil {
			return "", err
		}
		var r struct {
			ID    string `json:"id"`
			Error any    `json:"error"`
		}
		_ = json.Unmarshal(b, &r)
		if st >= 300 || r.ID == "" {
			return "", sendErr{msg: "Kaleyra: " + trimBody(b)}
		}
		return r.ID, nil
	case "Gupshup":
		q := url.Values{"method": {"SendMessage"}, "send_to": {num}, "msg": {text}, "msg_type": {"TEXT"}, "userid": {v["user"]}, "auth_scheme": {"plain"},
			"password": {key}, "v": {"1.1"}, "format": {"text"}, "mask": {v["sender"]}, "principalEntityId": {v["dlt_entity"]}, "dltTemplateId": {dltTemplate}}
		_, b, err := msgRequest(ctx, http.MethodGet, providerBase(c, "https://enterprise.smsgupshup.com")+"/GatewayAPI/rest?"+q.Encode(), nil, nil)
		if err != nil {
			return "", err
		}
		// "success | 919xxxxxxxxx | 1234567890-123" or "error | 105 | …"
		p := strings.Split(string(b), "|")
		if strings.TrimSpace(strings.ToLower(p[0])) != "success" {
			return "", sendErr{msg: "Gupshup: " + trimBody(b)}
		}
		return strings.TrimSpace(p[len(p)-1]), nil
	case "Route Mobile":
		q := url.Values{"username": {v["user"]}, "password": {key}, "type": {"0"}, "dlr": {"1"}, "destination": {num}, "source": {v["sender"]}, "message": {text},
			"entityid": {v["dlt_entity"]}, "tempid": {dltTemplate}}
		_, b, err := msgRequest(ctx, http.MethodGet, providerBase(c, "https://api.rmlconnect.net")+"/bulksms/bulksms?"+q.Encode(), nil, nil)
		if err != nil {
			return "", err
		}
		// "1701|919xxxxxxxxx|<message id>": 1701 is accepted.
		p := strings.Split(strings.TrimSpace(string(b)), "|")
		if p[0] != "1701" {
			return "", sendErr{msg: "Route Mobile answered " + trimBody(b)}
		}
		return p[len(p)-1], nil
	case "Other":
		tpl := v["url"]
		if tpl == "" {
			return "", sendErr{msg: "no gateway address set for this SMS provider"}
		}
		r := strings.NewReplacer("{to}", url.QueryEscape(num), "{text}", url.QueryEscape(text), "{sender}", url.QueryEscape(v["sender"]),
			"{entity}", url.QueryEscape(v["dlt_entity"]), "{template}", url.QueryEscape(dltTemplate), "{key}", url.QueryEscape(key))
		st, b, err := msgRequest(ctx, http.MethodGet, r.Replace(tpl), nil, nil)
		if err != nil {
			return "", err
		}
		if st >= 300 {
			return "", sendErr{msg: fmt.Sprintf("gateway answered %d: %s", st, trimBody(b))}
		}
		return trimBody(b), nil
	}
	return "", sendErr{msg: "no built-in sender for " + firstOf(v["provider"], "this provider")}
}

// ---- WhatsApp ----

// WAProviders are the WhatsApp providers with a built-in sender.
var WAProviders = []string{"Meta Cloud API", "Gupshup", "Interakt", "Other"}

func checkWAConn(c Connection) error {
	v := c.Values
	switch v["provider"] {
	case "Meta Cloud API":
		if v["phone_id"] == "" {
			return invalid("enter the phone number ID from Meta's WhatsApp Manager")
		}
	case "Gupshup":
		if v["app"] == "" || v["number"] == "" {
			return invalid("enter the business number and Gupshup's app name")
		}
	}
	return nil
}

// waText sends plain text, allowed within 24 hours of the client writing.
func waText(ctx context.Context, c Connection, to, text string) (string, error) {
	v, key := c.Values, c.Secrets["api_key"]
	switch v["provider"] {
	case "Meta Cloud API":
		return metaSend(ctx, c, map[string]any{"messaging_product": "whatsapp", "recipient_type": "individual", "to": msisdn(to), "type": "text", "text": map[string]any{"preview_url": false, "body": text}})
	case "Gupshup":
		msg, _ := json.Marshal(map[string]string{"type": "text", "text": text})
		form := url.Values{"channel": {"whatsapp"}, "source": {msisdn(v["number"])}, "destination": {msisdn(to)}, "message": {string(msg)}, "src.name": {v["app"]}}
		return gupshupWA(ctx, c, "/wa/api/v1/msg", form, key)
	case "Interakt":
		return "", sendErr{msg: "Interakt's public API sends approved templates only; set the WhatsApp template name on the Ledgerline template"}
	}
	return "", sendErr{msg: "no built-in sender for " + firstOf(v["provider"], "this WhatsApp provider")}
}

// waTemplate sends an approved template with its values in order.
func waTemplate(ctx context.Context, c Connection, to, name string, params []string) (string, error) {
	v, key := c.Values, c.Secrets["api_key"]
	lang := firstOf(v["lang"], "en")
	switch v["provider"] {
	case "Meta Cloud API":
		ps := []map[string]string{}
		for _, p := range params {
			ps = append(ps, map[string]string{"type": "text", "text": p})
		}
		tpl := map[string]any{"name": name, "language": map[string]string{"code": lang}}
		if len(ps) > 0 {
			tpl["components"] = []map[string]any{{"type": "body", "parameters": ps}}
		}
		return metaSend(ctx, c, map[string]any{"messaging_product": "whatsapp", "to": msisdn(to), "type": "template", "template": tpl})
	case "Gupshup":
		t, _ := json.Marshal(map[string]any{"id": name, "params": params})
		form := url.Values{"source": {msisdn(v["number"])}, "destination": {msisdn(to)}, "template": {string(t)}, "src.name": {v["app"]}}
		return gupshupWA(ctx, c, "/wa/api/v1/template/msg", form, key)
	case "Interakt":
		d := msisdn(to)
		body, _ := json.Marshal(map[string]any{"countryCode": "+91", "phoneNumber": d[2:], "type": "Template",
			"template": map[string]any{"name": name, "languageCode": lang, "bodyValues": params}})
		st, b, err := msgRequest(ctx, http.MethodPost, providerBase(c, "https://api.interakt.ai")+"/v1/public/message/",
			map[string]string{"Authorization": "Basic " + key, "Content-Type": "application/json"}, bytes.NewReader(body))
		if err != nil {
			return "", err
		}
		var r struct {
			Result  bool   `json:"result"`
			ID      string `json:"id"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(b, &r)
		if st >= 300 || !r.Result {
			return "", sendErr{msg: "Interakt: " + firstOf(r.Message, trimBody(b))}
		}
		return r.ID, nil
	}
	return "", sendErr{msg: "no built-in sender for " + firstOf(v["provider"], "this WhatsApp provider")}
}

func metaSend(ctx context.Context, c Connection, payload map[string]any) (string, error) {
	body, _ := json.Marshal(payload)
	st, b, err := msgRequest(ctx, http.MethodPost, providerBase(c, "https://graph.facebook.com/v21.0")+"/"+url.PathEscape(c.Values["phone_id"])+"/messages",
		map[string]string{"Authorization": "Bearer " + c.Secrets["api_key"], "Content-Type": "application/json"}, bytes.NewReader(body))
	var r struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
		Error struct {
			Message string `json:"message"`
			Code    int    `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(b, &r)
	if err != nil {
		if r.Error.Message != "" {
			var se sendErr
			if errors.As(err, &se) {
				se.msg += ": " + r.Error.Message
				return "", se
			}
		}
		return "", err
	}
	if st >= 300 || len(r.Messages) == 0 {
		why := firstOf(r.Error.Message, trimBody(b))
		if r.Error.Code == 131047 {
			why = "more than 24 hours since the client last wrote: an approved template is needed"
		}
		return "", sendErr{msg: "Meta: " + why}
	}
	return r.Messages[0].ID, nil
}

func gupshupWA(ctx context.Context, c Connection, path string, form url.Values, key string) (string, error) {
	st, b, err := msgRequest(ctx, http.MethodPost, providerBase(c, "https://api.gupshup.io")+path,
		map[string]string{"apikey": key, "Content-Type": "application/x-www-form-urlencoded"}, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	var r struct {
		Status    string `json:"status"`
		MessageID string `json:"messageId"`
		Message   string `json:"message"`
	}
	_ = json.Unmarshal(b, &r)
	if st >= 300 || r.Status != "submitted" {
		return "", sendErr{msg: "Gupshup: " + firstOf(r.Message, trimBody(b))}
	}
	return r.MessageID, nil
}

// waParams are a template body's values in the order they appear.
func waParams(body string, vars map[string]string) []string {
	var out []string
	for _, m := range rePlaceholder.FindAllString(body, -1) {
		out = append(out, vars[m[1:len(m)-1]])
	}
	return out
}

// ---- the send step ----

func (o *Ops) msgConn(ctx context.Context, tenant, id string) (Connection, bool) {
	c, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, id)
	if err != nil || c.Status == "Not set" || c.Values["provider"] == "" || c.Secrets["api_key"] == "" {
		return c, false
	}
	return c, true
}

// waSession says whether the client wrote to the firm's number in the last
// 24 hours, when free-form replies are allowed.
func (o *Ops) waSession(ctx context.Context, tenant, mobile string) bool {
	chats, err := docs.List[WAChat](ctx, o.Docs, tenant, cWAChat, "")
	if err != nil {
		return false
	}
	m := msisdn(mobile)
	for i := len(chats) - 1; i >= 0; i-- {
		if msisdn(chats[i].Mobile) != m {
			continue
		}
		t, err := time.Parse(time.RFC3339, chats[i].At)
		return err == nil && o.now().Sub(t) < 24*time.Hour
	}
	return false
}

// transmit sends a message that passed its checks, and records how it went.
// waName and params are for a WhatsApp template (empty for free text).
func (o *Ops) transmit(ctx context.Context, tenant string, m *Message, waName string, params []string) {
	if m.Status != "Sent" && m.Status != "Queued" {
		return
	}
	var (
		ref string
		err error
	)
	switch m.Channel {
	case ChSMS:
		c, ok := o.msgConn(ctx, tenant, "sms")
		if !ok {
			o.notConnected(ctx, tenant, m, "no SMS gateway connected (Settings → Connections)")
			return
		}
		m.Provider = c.Values["provider"]
		ref, err = sendSMS(ctx, c, m.To, m.Body, m.DLT, m.Category)
	case ChWhatsApp:
		c, ok := o.msgConn(ctx, tenant, "whatsapp")
		if !ok {
			o.notConnected(ctx, tenant, m, "no WhatsApp provider connected (Settings → Connections)")
			return
		}
		m.Provider = c.Values["provider"]
		switch {
		case waName != "":
			ref, err = waTemplate(ctx, c, m.To, waName, params)
		case o.waSession(ctx, tenant, m.To):
			ref, err = waText(ctx, c, m.To, m.Body)
		default:
			m.Status, m.Note = "Not sent", "WhatsApp needs a template approved by Meta for messages the firm starts: set its name on this template"
			return
		}
	default:
		return
	}
	m.Attempts++
	var se sendErr
	switch {
	case err == nil:
		m.Status, m.Ref, m.Note = "Sent", ref, ""
	case errors.As(err, &se) && se.transient && m.Attempts < msgMaxAttempts:
		m.Status, m.Note = "Queued", err.Error()+"; trying again"
	default:
		m.Status, m.Note = "Failed", err.Error()
	}
}

func (o *Ops) notConnected(ctx context.Context, tenant string, m *Message, why string) {
	if o.DevCodes || o.devFirm(ctx, tenant) {
		m.Note = "simulated: development firm, " + why
		return
	}
	m.Status, m.Note = "Not sent", why
}

// retryMessages sends queued messages again, a minute or more after the
// last try.
func (o *Ops) retryMessages(ctx context.Context, tenant string, now time.Time) {
	ms, err := docs.List[Message](ctx, o.Docs, tenant, cMessage, "")
	if err != nil {
		return
	}
	tpls, _ := o.templates(ctx, tenant)
	byID := map[string]Template{}
	for _, t := range tpls {
		byID[t.ID] = t
	}
	for _, m := range ms {
		if m.Status != "Queued" {
			continue
		}
		if t, err := time.Parse(time.RFC3339, firstOf(m.TriedAt, m.At)); err == nil && now.Sub(t) < time.Minute {
			continue
		}
		name, params := "", []string(nil)
		if t, ok := byID[m.Template]; ok && m.Channel == ChWhatsApp && t.WAName != "" {
			name, params = t.WAName, m.Params
		}
		o.transmit(ctx, tenant, &m, name, params)
		m.TriedAt = now.Format(time.RFC3339)
		_ = docs.Upsert(ctx, o.Docs, tenant, cMessage, m.ID, m, SystemActor)
	}
}

// ---- Meta's webhook ----

// metaInbound reads the text messages in a Meta Cloud API webhook.
func metaInbound(body []byte) ([][2]string, bool) {
	var p struct {
		Object string `json:"object"`
		Entry  []struct {
			Changes []struct {
				Value struct {
					Messages []struct {
						From string `json:"from"`
						Type string `json:"type"`
						Text struct {
							Body string `json:"body"`
						} `json:"text"`
						Button struct {
							Text string `json:"text"`
						} `json:"button"`
					} `json:"messages"`
				} `json:"value"`
			} `json:"changes"`
		} `json:"entry"`
	}
	if json.Unmarshal(body, &p) != nil || p.Object == "" {
		return nil, false
	}
	var out [][2]string
	for _, e := range p.Entry {
		for _, ch := range e.Changes {
			for _, m := range ch.Value.Messages {
				text := firstOf(m.Text.Body, m.Button.Text)
				if m.From != "" && text != "" {
					out = append(out, [2]string{m.From, text})
				}
			}
		}
	}
	return out, true
}

func validHubSignature(secret string, body []byte, sig string) bool {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal([]byte("sha256="+hex.EncodeToString(mac.Sum(nil))), []byte(strings.ToLower(strings.TrimSpace(sig))))
}

// WhatsAppVerify answers Meta's check when the webhook address is set up:
// the challenge is echoed when the verify token matches the connection's.
func (o *Ops) WhatsAppVerify(ctx context.Context, tenant, mode, token, challenge string) (string, error) {
	c, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, "whatsapp")
	if err != nil || mode != "subscribe" || c.Values["verify_token"] == "" || !hmac.Equal([]byte(token), []byte(c.Values["verify_token"])) {
		return "", ErrForbidden
	}
	return challenge, nil
}

// replyWhatsApp sends the bot's answer through the connected provider; the
// client has just written, so plain text is allowed.
func (o *Ops) replyWhatsApp(ctx context.Context, tenant, to, ucc, text string) {
	c, ok := o.msgConn(ctx, tenant, "whatsapp")
	if !ok || strings.TrimSpace(text) == "" {
		return
	}
	n, err := o.next(ctx, tenant, "message", "whatsapp")
	if err != nil {
		return
	}
	m := Message{ID: fmt.Sprintf("MSG-%07d", n), At: o.now().Format(time.RFC3339), Event: "whatsapp.reply", Channel: ChWhatsApp, Category: "Service", UCC: ucc, To: to, Body: text, Provider: c.Values["provider"]}
	ref, err := waText(ctx, c, to, text)
	m.Attempts = 1
	if err != nil {
		m.Status, m.Note = "Failed", err.Error()
	} else {
		m.Status, m.Ref = "Sent", ref
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cMessage, m.ID, m, "whatsapp")
}
