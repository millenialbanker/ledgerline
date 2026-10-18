package ops

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// ---- messaging: templates, event messages, bulk sends, WhatsApp bot ----
//
// Each client-facing event (a receipt credited, a payout paid, a margin
// shortfall, a complaint answered…) is sent through the templates switched
// on for it, by email, SMS and WhatsApp. SMS templates carry their DLT
// template ID (TRAI rules); promotional SMS is held outside 9 am to 9 pm.
// WhatsApp goes only to clients who opted in (messaging the firm's number
// counts as opting in; STOP opts out). Delivery is to the outbox until a
// provider is connected, the same way contract-note emails are.

const (
	cTemplate = "template"
	cMessage  = "message"
	cWAChat   = "wachat"
	cJob      = "job"
	cJobRun   = "jobrun"
)

// Channels.
const (
	ChEmail    = "Email"
	ChSMS      = "SMS"
	ChWhatsApp = "WhatsApp"
)

// Template is a message for one event on one channel. Placeholders are
// written {name}, {ucc}, {amount} and so on; see EventVars.
type Template struct {
	ID       string `json:"id"`
	Event    string `json:"event"`
	Channel  string `json:"channel"`
	Category string `json:"category"` // Transactional, Service, Promotional
	Subject  string `json:"subject,omitempty"`
	Body     string `json:"body"`
	DLT      string `json:"dlt_template_id,omitempty"`
	WAName   string `json:"wa_template,omitempty"` // approved WhatsApp template name
	Active   bool   `json:"active"`
}

// EventVars lists each event and the placeholders it fills.
var EventVars = map[string][]string{
	"receipt.credited":      {"amount", "mode", "date", "status"},
	"payout.paid":           {"amount", "utr"},
	"payout.rejected":       {"amount", "reason"},
	"payout.failed":         {"amount", "reason"},
	"contract_note.sent":    {"no", "date"},
	"margin.shortfall":      {"shortfall", "date"},
	"debit.reminder":        {"debit", "days", "date"},
	"rekyc.due":             {"due"},
	"ticket.opened":         {"id", "subject", "due"},
	"ticket.reply":          {"id", "subject", "status"},
	"change.approved":       {"kind", "id"},
	"change.rejected":       {"kind", "id", "reason"},
	"portal.granted":        {"user"},
	"statement.monthly":     {"from", "to", "closing"},
	"bulk":                  {},
	"insight.tax_harvest":   {"fy", "stcg", "ltcg", "st_loss", "lt_loss", "saving", "exempt_left", "gain_harvest", "last_day", "symbols"},
	"insight.buyback":       {"symbol", "company", "price", "ltp", "premium_pct", "held", "category", "entitlement", "record", "close"},
	"insight.fo_expiry":     {"contract", "expiry", "action", "qty", "symbol", "value"},
	"insight.advance_tax":   {"due", "stcg", "ltcg", "tax", "total"},
	"insight.corp_action":   {"symbol", "what", "record", "ex", "held", "amount"},
	"insight.idle_funds":    {"amount", "since"},
	"insight.pledge_margin": {"margin", "count"},
	"insight.dormant":       {"date", "last"},
}

// Events lists the events in display order.
func Events() []string {
	ks := sortedKeys(EventVars)
	return ks
}

// DefaultTemplates are installed for a new firm.
func DefaultTemplates() []Template {
	t := func(ev, ch, cat, subj, body, dlt string) Template {
		return Template{Event: ev, Channel: ch, Category: cat, Subject: subj, Body: body, DLT: dlt, Active: true}
	}
	ts := []Template{
		t("receipt.credited", ChSMS, "Transactional", "", "Dear {name}, Rs {amount} received by {mode} is {status} in your account {ucc} on {date}. -{firm}", "1107166000000000001"),
		t("receipt.credited", ChWhatsApp, "Transactional", "", "Hi {name}, we have received ₹{amount} ({mode}) for account {ucc}. Status: {status}.", ""),
		t("payout.paid", ChSMS, "Transactional", "", "Dear {name}, Rs {amount} has been paid to your registered bank account. UTR {utr}. -{firm}", "1107166000000000002"),
		t("payout.paid", ChEmail, "Transactional", "Funds paid out: ₹{amount}", "Dear {name},\n\nWe have paid ₹{amount} from your trading account {ucc} to your registered bank account. Bank reference (UTR): {utr}.\n\n{firm}", ""),
		t("payout.rejected", ChSMS, "Transactional", "", "Dear {name}, your payout request of Rs {amount} could not be processed: {reason}. -{firm}", "1107166000000000003"),
		t("payout.failed", ChEmail, "Transactional", "Payout returned by the bank", "Dear {name},\n\nYour payout of ₹{amount} was returned by the bank ({reason}). The amount is back in your trading account. Please check your bank details.\n\n{firm}", ""),
		t("contract_note.sent", ChSMS, "Transactional", "", "Dear {name}, contract note {no} for trades on {date} has been emailed to you. -{firm}", "1107166000000000004"),
		t("margin.shortfall", ChSMS, "Transactional", "", "Dear {name}, your account {ucc} has a margin shortfall of Rs {shortfall} on {date}. Please add funds to avoid square-off. -{firm}", "1107166000000000005"),
		t("margin.shortfall", ChWhatsApp, "Transactional", "", "⚠️ {name}, margin shortfall of ₹{shortfall} in {ucc} on {date}. Add funds to avoid square-off of positions.", ""),
		t("debit.reminder", ChEmail, "Service", "Debit balance in your account", "Dear {name},\n\nYour trading account {ucc} has a debit balance of ₹{debit}, outstanding for {days} days. Interest is charged on overdue debits and buying is blocked after the firm's limit. Please pay at the earliest.\n\n{firm}", ""),
		t("rekyc.due", ChEmail, "Service", "Please update your KYC", "Dear {name},\n\nUnder the KYC rules your details are due for periodic updation on {due}. Sign in to the client portal or reply to this email to confirm or update them.\n\n{firm}", ""),
		t("ticket.opened", ChEmail, "Service", "We have received your request {id}", "Dear {name},\n\nWe have registered your request {id}: {subject}. We will respond by {due}. You can follow it in the client portal under Help desk.\n\n{firm}", ""),
		t("ticket.reply", ChEmail, "Service", "Update on {id}", "Dear {name},\n\nThere is a reply on your request {id} ({subject}). Status: {status}. Please see the Help desk in the client portal.\n\n{firm}", ""),
		t("change.approved", ChEmail, "Service", "Your {kind} has been updated", "Dear {name},\n\nYour request {id} to change your {kind} has been approved and is now in effect.\n\n{firm}", ""),
		t("change.rejected", ChEmail, "Service", "Your change request {id}", "Dear {name},\n\nWe could not process your request {id} to change your {kind}: {reason}.\n\n{firm}", ""),
		t("portal.granted", ChEmail, "Service", "Your client portal login", "Dear {name},\n\nYour client portal user ID is {user}. Your one-time password is being sent separately by SMS.\n\n{firm}", ""),
		t("insight.tax_harvest", ChEmail, "Service", "Your capital gains for FY {fy} and open losses", "Dear {name},\n\nA summary from your account {ucc} for the financial year {fy}:\n\nGains booked so far: short-term ₹{stcg}, long-term ₹{ltcg}.\nUnrealised losses in your holdings: short-term ₹{st_loss}, long-term ₹{lt_loss} ({symbols}).\n\nUnder the income-tax rules, losses booked in the year are set off against gains of the year. Booking these losses would reduce the estimated tax on this year's gains by about ₹{saving}. Long-term gains still within this year's ₹1.25 lakh exemption: ₹{exempt_left}.\n\nFor a sale to count in this financial year it must settle by 31 March: the last trading day is {last_day}. Your holding-wise working is under Tax & insights in the client portal.\n\nThis is information from your account, not tax or investment advice. Please consult your tax adviser before acting. Selling and buying back costs brokerage, STT and other charges.\n\n{firm}", ""),
		t("insight.tax_harvest", ChWhatsApp, "Service", "", "{name}, FY {fy} so far: gains booked short-term ₹{stcg}, long-term ₹{ltcg}. Open losses in your holdings: ₹{st_loss} short-term, ₹{lt_loss} long-term. Booking them by {last_day} could reduce tax on this year's gains by about ₹{saving}. Details: client portal → Tax & insights. Information only, not tax or investment advice.", ""),
		t("insight.buyback", ChEmail, "Service", "{company} buy-back: you hold eligible shares", "Dear {name},\n\n{company} is buying back shares at ₹{price} per share through the stock exchange's tender window (last price ₹{ltp}, {premium_pct}% lower). You held {held} free shares; category: {category}; entitlement: {entitlement}. The record date was {record}.\n\nThe tender period closes on {close}. You can tender in the client portal under Tax & insights, or ask us to tender for you. Shares not accepted are returned to your demat account.\n\nSince 1 October 2024 the buy-back amount is taxed in the shareholder's hands as dividend, and the cost of the shares accepted becomes a capital loss. This is information about an offer on shares you hold, not investment advice.\n\n{firm}", ""),
		t("insight.buyback", ChWhatsApp, "Service", "", "{name}, {company} buy-back at ₹{price} ({premium_pct}% above the last price) is open till {close}. You hold {held} free shares ({category}; entitlement {entitlement}). Tender in the client portal → Tax & insights. Information only, not advice.", ""),
		t("insight.fo_expiry", ChEmail, "Transactional", "{contract} expires on {expiry}: delivery settlement", "Dear {name},\n\nYour position in {contract} expires on {expiry}. Stock derivatives held at expiry are settled by delivery: you would {action} {qty} shares of {symbol}, about ₹{value}. Funds or shares must be in your account for the settlement, and margins rise in the days before expiry.\n\nIf you do not intend to settle by delivery, close or roll over the position before expiry.\n\n{firm}", ""),
		t("insight.fo_expiry", ChSMS, "Transactional", "", "Dear {name}, your {contract} position expires {expiry} and settles by delivery: {action} {qty} {symbol} (approx Rs {value}). Close or roll over to avoid. -{firm}", "1107166000000000010"),
		t("insight.advance_tax", ChEmail, "Service", "Advance tax instalment due {due}", "Dear {name},\n\nThe next advance-tax instalment is due on {due}. Capital gains booked in your account {ucc} this financial year: short-term ₹{stcg}, long-term ₹{ltcg}; estimated tax on them about ₹{tax}. Your capital gains statement is in the client portal under Reports & statements.\n\nThis does not include other income, F&O results or other brokers. It is information, not tax advice; please check with your tax adviser.\n\n{firm}", ""),
		t("insight.corp_action", ChEmail, "Service", "{symbol}: {what}", "Dear {name},\n\n{symbol} has announced a {what}. Record date: {record}; ex-date: {ex}. You hold {held} shares in your demat account.\n\n{firm}", ""),
		t("insight.idle_funds", ChEmail, "Promotional", "₹{amount} in your trading account", "Dear {name},\n\nYour trading account {ucc} has a credit balance of ₹{amount} and no trades since {since}. You can withdraw it at any time from the client portal (Funds & payouts); otherwise it is returned to your bank on the running-account settlement date.\n\n{firm}", ""),
		t("insight.pledge_margin", ChEmail, "Promotional", "Use your holdings as margin", "Dear {name},\n\nYou have {count} holding(s) that are not pledged. Pledged as margin, they would give about ₹{margin} after haircuts, while you keep ownership and corporate benefits. Exchange rules require at least half of the margin for derivatives in cash.\n\nYou can request a pledge from the depository's site or ask us. This is information about your account, not investment advice.\n\n{firm}", ""),
		t("insight.dormant", ChEmail, "Service", "Your trading account will be marked inactive on {date}", "Dear {name},\n\nThere has been no activity in your trading account {ucc} since {last}. Under the stock exchanges' rules it will be marked inactive on {date}, after which you cannot trade until it is reactivated with a fresh verification.\n\nAny trade or an update of your KYC details keeps it active. If you no longer need the account, you can ask us to close it.\n\n{firm}", ""),
		t("insight.fo_expiry", ChWhatsApp, "Transactional", "", "⚠️ {name}, your {contract} position expires on {expiry}. Stock derivatives held at expiry settle by delivery: you would {action} {qty} {symbol} shares, about ₹{value}. Close or roll over before expiry if you do not want delivery.", ""),
		t("insight.advance_tax", ChWhatsApp, "Service", "", "{name}, advance tax instalment due {due}. Capital gains booked this year: short-term ₹{stcg}, long-term ₹{ltcg}; estimated tax about ₹{tax}. Reply TAX for details. Information only, not tax advice.", ""),
		t("insight.corp_action", ChWhatsApp, "Service", "", "{name}, {symbol} has announced a {what}. Record date {record}, ex-date {ex}. You hold {held} shares.", ""),
		t("insight.idle_funds", ChWhatsApp, "Promotional", "", "{name}, ₹{amount} is in your trading account {ucc} with no trades since {since}. You can withdraw it any time from the client portal → Funds & payouts. Reply BAL for your balance.", ""),
		t("insight.pledge_margin", ChWhatsApp, "Promotional", "", "{name}, {count} of your holdings are not pledged. As margin they would give about ₹{margin} after haircuts; you keep ownership and dividends. Ask us or pledge from the depository's site. Information only, not advice.", ""),
		t("insight.dormant", ChWhatsApp, "Service", "", "{name}, there has been no activity in your trading account {ucc} since {last}. Under exchange rules it will be marked inactive on {date}. Any trade or a KYC update keeps it active.", ""),
		t("statement.monthly", ChEmail, "Transactional", "Statement of account {from} to {to}", "Dear {name},\n\nPlease find your statement of account for {from} to {to}. Closing balance: {closing}. Please report any discrepancy within 30 days.\n\n{firm}", ""),
	}
	// WhatsApp messages a business starts must use a template approved by
	// Meta under this name (utility for account information, marketing for
	// promotional ones).
	for i := range ts {
		if ts[i].Channel == ChWhatsApp && ts[i].WAName == "" {
			ts[i].WAName = strings.NewReplacer(".", "_").Replace(ts[i].Event)
		}
	}
	return ts
}

func (o *Ops) templates(ctx context.Context, tenant string) ([]Template, error) {
	ts, err := docs.List[Template](ctx, o.Docs, tenant, cTemplate, "")
	if err != nil {
		return ts, err
	}
	if len(ts) > 0 {
		// Templates for events added since the firm was set up.
		have := map[string]bool{}
		for _, t := range ts {
			have[t.Event+"|"+t.Channel] = true
		}
		for _, t := range DefaultTemplates() {
			if have[t.Event+"|"+t.Channel] {
				continue
			}
			t.ID = "TPL-" + strings.NewReplacer(".", "-", "_", "-").Replace(t.Event) + "-" + strings.ToLower(t.Channel)
			if err := docs.Upsert(ctx, o.Docs, tenant, cTemplate, t.ID, t, "system"); err != nil {
				return nil, err
			}
			ts = append(ts, t)
		}
		return ts, nil
	}
	for i, t := range DefaultTemplates() {
		t.ID = fmt.Sprintf("TPL-%03d", i+1)
		if err := docs.Upsert(ctx, o.Docs, tenant, cTemplate, t.ID, t, "system"); err != nil {
			return nil, err
		}
		ts = append(ts, t)
	}
	return ts, nil
}

var rePlaceholder = regexp.MustCompile(`\{([a-z_]+)\}`)

func render(s string, vars map[string]string) string {
	return rePlaceholder.ReplaceAllStringFunc(s, func(m string) string {
		if v, ok := vars[m[1:len(m)-1]]; ok {
			return v
		}
		return m
	})
}

// Message is one message sent (or held) to a client.
type Message struct {
	ID       string `json:"id"`
	At       string `json:"at"`
	Event    string `json:"event"`
	Template string `json:"template"`
	Channel  string `json:"channel"`
	Category string `json:"category"`
	UCC      string `json:"ucc"`
	To       string `json:"to"`
	Subject  string `json:"subject,omitempty"`
	Body     string `json:"body"`
	Status   string `json:"status"` // Sent, Bounced, Failed, Held, Not opted in
	Note     string `json:"note,omitempty"`
	DLT      string `json:"dlt_template_id,omitempty"`
}

// notify sends an event's messages to a client through every active
// template for it. Errors are logged, never returned: a message failing
// must not undo the business action that raised it.
func (o *Ops) notify(ctx context.Context, tenant, actor, event, ucc string, data map[string]string) {
	if _, err := o.sendEvent(ctx, tenant, actor, event, ucc, data, ""); err != nil {
		o.log(ctx, tenant, actor, "message.error", event, map[string]any{"ucc": ucc, "error": err.Error()})
	}
}

func (o *Ops) sendEvent(ctx context.Context, tenant, actor, event, ucc string, data map[string]string, only string) ([]Message, error) {
	c, err := o.Client(ctx, tenant, ucc)
	if err != nil {
		return nil, err
	}
	ts, err := o.templates(ctx, tenant)
	if err != nil {
		return nil, err
	}
	set, _ := o.Settings(ctx, tenant)
	vars := map[string]string{"name": c.Name, "ucc": c.UCC, "firm": set.Firm}
	for k, v := range data {
		vars[k] = v
	}
	var out []Message
	for _, t := range ts {
		if !t.Active || t.Event != event || (only != "" && t.ID != only) {
			continue
		}
		m, err := o.deliver(ctx, tenant, actor, t, c, vars)
		if err != nil {
			return out, err
		}
		out = append(out, m)
	}
	return out, nil
}

func (o *Ops) deliver(ctx context.Context, tenant, actor string, t Template, c Client, vars map[string]string) (Message, error) {
	now := o.now()
	m := Message{At: now.Format(time.RFC3339), Event: t.Event, Template: t.ID, Channel: t.Channel, Category: t.Category, UCC: c.UCC,
		Subject: render(t.Subject, vars), Body: render(t.Body, vars), Status: "Sent", DLT: t.DLT}
	switch t.Channel {
	case ChEmail:
		m.To = c.Email
		if strings.HasSuffix(c.Email, ".invalid") || !reEmail.MatchString(c.Email) {
			m.Status, m.Note = "Bounced", "mailbox does not accept mail"
		}
	case ChSMS:
		m.To = c.Mobile
		if !reMobile.MatchString(c.Mobile) {
			m.Status, m.Note = "Failed", "no valid mobile number"
		} else if t.DLT == "" {
			m.Status, m.Note = "Failed", "template has no DLT template ID"
		} else if t.Category == "Promotional" && (now.Hour() < 9 || now.Hour() >= 21) {
			m.Status, m.Note = "Held", "promotional SMS is sent between 9 am and 9 pm"
		} else if c.DND && t.Category == "Promotional" {
			m.Status, m.Note = "Failed", "number is on the do-not-disturb register"
		}
	case ChWhatsApp:
		m.To = c.Mobile
		if !c.WhatsApp {
			m.Status, m.Note = "Not opted in", "client has not opted in to WhatsApp"
		}
	}
	n, err := o.next(ctx, tenant, "message", actor)
	if err != nil {
		return m, err
	}
	m.ID = fmt.Sprintf("MSG-%07d", n)
	return m, docs.Upsert(ctx, o.Docs, tenant, cMessage, m.ID, m, actor)
}

// Messages lists messages, newest first, optionally for one client.
func (o *Ops) Messages(ctx context.Context, tenant, ucc string, limit int) ([]Message, error) {
	ms, err := docs.List[Message](ctx, o.Docs, tenant, cMessage, "")
	if err != nil {
		return nil, err
	}
	out := []Message{}
	for i := len(ms) - 1; i >= 0; i-- {
		if ucc == "" || ms[i].UCC == strings.ToUpper(ucc) {
			out = append(out, ms[i])
		}
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

// Templates lists the templates (installing the defaults the first time).
func (o *Ops) Templates(ctx context.Context, tenant string) ([]Template, error) {
	ts, err := o.templates(ctx, tenant)
	sort.Slice(ts, func(i, j int) bool { return ts[i].Event+ts[i].Channel < ts[j].Event+ts[j].Channel })
	return ts, err
}

// SaveTemplate creates or updates a template.
func (o *Ops) SaveTemplate(ctx context.Context, tenant, actor string, t Template) (Template, error) {
	if err := o.require(ctx, tenant, actor, "Editing message templates", RoleChecker, RoleCompliance); err != nil {
		return t, err
	}
	if _, ok := EventVars[t.Event]; !ok {
		return t, invalid("unknown event %q", t.Event)
	}
	if !contains([]string{ChEmail, ChSMS, ChWhatsApp}, t.Channel) {
		return t, invalid("channel must be Email, SMS or WhatsApp")
	}
	if !contains([]string{"Transactional", "Service", "Promotional"}, t.Category) {
		return t, invalid("category must be Transactional, Service or Promotional")
	}
	if strings.TrimSpace(t.Body) == "" {
		return t, invalid("write the message")
	}
	if t.Channel == ChEmail && strings.TrimSpace(t.Subject) == "" {
		return t, invalid("an email needs a subject")
	}
	if t.Channel == ChSMS {
		if !regexp.MustCompile(`^[0-9]{19}$`).MatchString(t.DLT) {
			return t, invalid("SMS templates need the 19-digit DLT template ID registered with the operator")
		}
		if len([]rune(t.Body)) > 480 {
			return t, invalid("SMS text is limited to 480 characters (3 parts)")
		}
	}
	allowed := map[string]bool{"name": true, "ucc": true, "firm": true}
	for _, v := range EventVars[t.Event] {
		allowed[v] = true
	}
	for _, m := range rePlaceholder.FindAllStringSubmatch(t.Subject+" "+t.Body, -1) {
		if !allowed[m[1]] {
			return t, invalid("{%s} is not available for %s; use %s", m[1], t.Event, strings.Join(append([]string{"name", "ucc", "firm"}, EventVars[t.Event]...), ", "))
		}
	}
	if _, err := o.templates(ctx, tenant); err != nil {
		return t, err
	}
	if t.ID == "" {
		n, err := o.next(ctx, tenant, "template", actor)
		if err != nil {
			return t, err
		}
		t.ID = fmt.Sprintf("TPL-%03d", n+int64(len(DefaultTemplates())))
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cTemplate, t.ID, t, actor); err != nil {
		return t, err
	}
	o.log(ctx, tenant, actor, "template.saved", t.ID, map[string]any{"event": t.Event, "channel": t.Channel, "active": t.Active})
	return t, nil
}

// BulkSend sends one template to many clients: everyone, a group (TAG:x)
// or a list of UCCs. Promotional messages skip clients who opted out.
func (o *Ops) BulkSend(ctx context.Context, tenant, actor, templateID, target string, uccs []string, data map[string]string) (map[string]int, error) {
	if err := o.require(ctx, tenant, actor, "Sending bulk messages", RoleChecker, RoleCompliance); err != nil {
		return nil, err
	}
	t, _, err := docs.Get[Template](ctx, o.Docs, tenant, cTemplate, templateID)
	if err != nil {
		return nil, err
	}
	if !t.Active {
		return nil, invalid("template %s is switched off", templateID)
	}
	cs, err := o.ClientMap(ctx, tenant)
	if err != nil {
		return nil, err
	}
	var to []string
	switch {
	case target == "all":
		for _, u := range sortedKeys(cs) {
			if cs[u].Status == "Active" {
				to = append(to, u)
			}
		}
	case strings.HasPrefix(target, "TAG:"):
		tag := strings.TrimPrefix(target, "TAG:")
		for _, u := range sortedKeys(cs) {
			if cs[u].Status == "Active" && contains(cs[u].Tags, tag) {
				to = append(to, u)
			}
		}
	default:
		to = uccs
	}
	if len(to) == 0 {
		return nil, invalid("no clients to send to")
	}
	res := map[string]int{}
	for _, u := range to {
		ms, err := o.sendEvent(ctx, tenant, actor, t.Event, u, data, t.ID)
		if err != nil {
			res["Not found"]++
			continue
		}
		for _, m := range ms {
			res[m.Status]++
		}
	}
	o.log(ctx, tenant, actor, "message.bulk", templateID, map[string]any{"target": target, "results": res})
	return res, nil
}

// ---- WhatsApp bot ----

// WAChat is one exchange with the bot.
type WAChat struct {
	ID     string `json:"id"`
	At     string `json:"at"`
	Mobile string `json:"mobile"`
	UCC    string `json:"ucc,omitempty"`
	In     string `json:"in"`
	Out    string `json:"out"`
}

// WhatsAppInbound answers a message to the firm's WhatsApp number from a
// client's registered mobile: balance, holdings, ledger, latest contract
// note, margin, and opting in or out.
func (o *Ops) WhatsAppInbound(ctx context.Context, tenant, from, text, date string) (string, error) {
	mobile := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(from), "+"), "91")
	if len(mobile) > 10 {
		mobile = mobile[len(mobile)-10:]
	}
	set, _ := o.Settings(ctx, tenant)
	cs, err := o.ClientMap(ctx, tenant)
	if err != nil {
		return "", err
	}
	var mine []Client
	for _, u := range sortedKeys(cs) {
		if c := cs[u]; c.Mobile == mobile && c.Status == "Active" {
			mine = append(mine, c)
		}
	}
	words := strings.Fields(strings.ToUpper(text))
	cmd := ""
	if len(words) > 0 {
		cmd = words[0]
	}
	var reply string
	var ucc string
	if len(mine) == 0 {
		reply = "This number is not registered with " + set.Firm + ". Please write from the mobile number registered with your trading account."
	} else {
		c := mine[0]
		if len(words) > 1 {
			for _, x := range mine {
				if x.UCC == words[1] {
					c = x
				}
			}
		}
		ucc = c.UCC
		if !c.WhatsApp && cmd != "STOP" {
			_, _ = docs.Update(ctx, o.Docs, tenant, cClient, c.UCC, "whatsapp", func(x *Client) error { x.WhatsApp = true; return nil })
		}
		pick := ""
		if len(mine) > 1 {
			var us []string
			for _, x := range mine {
				us = append(us, x.UCC)
			}
			pick = fmt.Sprintf("\n(Showing %s. This number is linked to %s; add the UCC, e.g. BAL %s.)", c.UCC, strings.Join(us, ", "), us[len(us)-1])
		}
		reply = o.botAnswer(ctx, tenant, cmd, c, date, set) + pick
		if cmd == "STOP" {
			_, _ = docs.Update(ctx, o.Docs, tenant, cClient, c.UCC, "whatsapp", func(x *Client) error { x.WhatsApp = false; return nil })
		}
	}
	n, err := o.next(ctx, tenant, "wachat", "whatsapp")
	if err != nil {
		return reply, err
	}
	ch := WAChat{ID: fmt.Sprintf("WA-%07d", n), At: o.now().Format(time.RFC3339), Mobile: mobile, UCC: ucc, In: text, Out: reply}
	return reply, docs.Upsert(ctx, o.Docs, tenant, cWAChat, ch.ID, ch, "whatsapp")
}

const botMenu = "Reply with:\nBAL – balance and funds you can withdraw\nHOLD – your holdings\nLEDGER – last five ledger entries\nCN – latest contract note\nMARGIN – margin used and free\nTAX – capital gains this year and losses you could book\nOFFERS – buy-backs open on your shares\nSTOP – stop WhatsApp messages"

func (o *Ops) botAnswer(ctx context.Context, tenant, cmd string, c Client, date string, set Settings) string {
	first := strings.Fields(c.Name)[0]
	switch cmd {
	case "", "HI", "HELLO", "MENU", "HELP", "START":
		return fmt.Sprintf("Hello %s 👋 This is %s.\n%s", first, set.Firm, botMenu)
	case "BAL", "BALANCE":
		w, err := o.Withdrawable(ctx, tenant, date, c.UCC)
		if err != nil {
			return "Sorry, the balance is not available right now."
		}
		dc := "credit"
		bal := w.Ledger
		if bal < 0 {
			dc, bal = "debit (you owe)", -bal
		}
		return fmt.Sprintf("%s · %s\nLedger: ₹%s %s\nAvailable to withdraw: ₹%s\nAs at %s.", c.UCC, c.Name, formatINR(bal.Dec()), dc, formatINR(w.Available.Dec()), date)
	case "HOLD", "HOLDINGS":
		hs, _ := o.Holdings(ctx, tenant, c.UCC)
		_, look, _ := o.Market(ctx, tenant, date)
		var lines []string
		var total money.Dec
		for _, h := range hs {
			if h.Qty <= 0 {
				continue
			}
			v := money.Dec{}
			if i, ok := look.ByISIN(h.ISIN); ok {
				v = i.LTP.MulInt(h.Qty)
			}
			total = total.Add(v)
			lines = append(lines, fmt.Sprintf("%s %d · ₹%s", h.Symbol, h.Qty, formatINR(v)))
		}
		if len(lines) == 0 {
			return "You have no holdings in your demat account with us."
		}
		if len(lines) > 10 {
			lines = append(lines[:10], fmt.Sprintf("…and %d more", len(lines)-10))
		}
		return fmt.Sprintf("Holdings of %s (%s):\n%s\nTotal ₹%s at the last price.", c.UCC, date, strings.Join(lines, "\n"), formatINR(total))
	case "LEDGER":
		st, err := o.App.Ledger.Statement(ctx, tenant, ledger.ClientAccount(c.UCC), "", date)
		if err != nil || len(st.Lines) == 0 {
			return "No ledger entries yet."
		}
		ls := st.Lines
		if len(ls) > 5 {
			ls = ls[len(ls)-5:]
		}
		var out []string
		for _, l := range ls {
			amt, dc := l.Debit, "Dr"
			if l.Credit > 0 {
				amt, dc = l.Credit, "Cr"
			}
			out = append(out, fmt.Sprintf("%s %s ₹%s %s", l.ValueDate, firstOf(l.Memo, l.Narration), formatINR(amt.Dec()), dc))
		}
		return "Last entries in " + c.UCC + ":\n" + strings.Join(out, "\n")
	case "CN":
		ns, _ := docs.List[ContractNote](ctx, o.Docs, tenant, cNote, "")
		var last *ContractNote
		for i := range ns {
			if ns[i].UCC == c.UCC && (last == nil || ns[i].Date+ns[i].No > last.Date+last.No) {
				last = &ns[i]
			}
		}
		if last == nil {
			return "No contract notes yet."
		}
		return fmt.Sprintf("Latest contract note %s for %s: %d trades, net ₹%s. It was emailed to %s; you can also open it in the client portal.", last.No, last.Date, last.Trades, formatINR(last.Net), maskEmail(c.Email))
	case "MARGIN":
		rv, err := o.Risk(ctx, tenant, date)
		if err == nil {
			for _, r := range rv.Rows {
				if r.UCC == c.UCC {
					return fmt.Sprintf("Margin for %s on %s: required ₹%s, free ₹%s. Status: %s.", c.UCC, date, formatINR(r.Required), formatINR(r.Free), r.Status)
				}
			}
		}
		return "No margin is blocked in your account."
	case "TAX":
		p, err := o.TaxPlan(ctx, tenant, c.UCC, date)
		if err != nil {
			return "Sorry, your tax working is not available right now."
		}
		msg := fmt.Sprintf("FY %s for %s, as at %s:\nGains booked: short-term ₹%s, long-term ₹%s\nOpen losses: short-term ₹%s, long-term ₹%s\nEstimated tax on gains booked: ₹%s", p.FY, c.UCC, date, rupees(p.STCG), rupees(p.LTCG), rupees(p.STLoss), rupees(p.LTLoss), rupees(p.TaxNow))
		if p.Saving.Sign() > 0 {
			msg += fmt.Sprintf("\nBooking the open losses could reduce it by about ₹%s.", rupees(p.Saving))
		}
		if p.GainHarvest.Sign() > 0 {
			msg += fmt.Sprintf("\nLong-term gains still within the ₹1.25 lakh exemption: ₹%s.", rupees(p.GainHarvest))
		}
		if p.DaysLeft >= 0 {
			msg += fmt.Sprintf("\nLast trading day for this year: %s.", fmtDay(p.LastDay))
		}
		return msg + "\nStock-wise working: client portal → Tax & insights. Information only, not tax or investment advice."
	case "OFFERS":
		bs, _ := o.BuyBacks(ctx, tenant)
		_, look, _ := o.Market(ctx, tenant, date)
		var lines []string
		for _, b := range bs {
			if b.Status != "Open" || date > b.Close {
				continue
			}
			h, _, err := docs.Get[Holding](ctx, o.Docs, tenant, cHolding, holdingID(c.UCC, b.ISIN))
			if err != nil || h.Qty-h.Pledged <= 0 {
				continue
			}
			ltp := "—"
			if i, ok := look.ByISIN(b.ISIN); ok {
				ltp = i.LTP.Fixed(2)
			}
			lines = append(lines, fmt.Sprintf("%s buy-back at ₹%s (last price ₹%s), closes %s; you hold %d free", b.Symbol, b.Price.Fixed(2), ltp, fmtDay(b.Close), h.Qty-h.Pledged))
		}
		if len(lines) == 0 {
			return "No buy-back offers are open on shares you hold."
		}
		return strings.Join(lines, "\n") + "\nTender in the client portal → Tax & insights, or reply to ask us to tender for you."
	case "STOP":
		return "You will not get WhatsApp messages from us any more. Send START to subscribe again."
	}
	return "Sorry, I did not understand that.\n" + botMenu
}

func firstOf(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func maskEmail(e string) string {
	u, d, ok := strings.Cut(e, "@")
	if !ok || len(u) < 2 {
		return e
	}
	return u[:2] + strings.Repeat("•", len(u)-2) + "@" + d
}

// WAChats lists recent bot conversations.
func (o *Ops) WAChats(ctx context.Context, tenant string, limit int) ([]WAChat, error) {
	cs, err := docs.List[WAChat](ctx, o.Docs, tenant, cWAChat, "")
	out := []WAChat{}
	for i := len(cs) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, cs[i])
	}
	return out, err
}

// WhatsAppWebhook verifies and answers an inbound webhook. The body is
// {"from": "+9198…", "text": "BAL"}, signed with HMAC-SHA256 of the body
// using the firm's WhatsApp secret.
func (o *Ops) WhatsAppWebhook(ctx context.Context, tenant string, body []byte, sig string) (string, error) {
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return "", err
	}
	if set.WhatsAppSecret == "" {
		return "", fmt.Errorf("%w: WhatsApp is not set up for this firm", ErrState)
	}
	mac := hmac.New(sha256.New, []byte(set.WhatsAppSecret))
	mac.Write(body)
	if !hmac.Equal([]byte(hex.EncodeToString(mac.Sum(nil))), []byte(strings.ToLower(sig))) {
		return "", fmt.Errorf("%w: bad signature", ErrForbidden)
	}
	var m struct{ From, Text string }
	if err := json.Unmarshal(body, &m); err != nil || m.From == "" {
		return "", invalid("body must be {\"from\": …, \"text\": …}")
	}
	return o.WhatsAppInbound(ctx, tenant, m.From, m.Text, o.businessDate(ctx, tenant))
}

// businessDate is today, or the last business day before it.
func (o *Ops) businessDate(ctx context.Context, tenant string) string {
	set, _ := o.Settings(ctx, tenant)
	d := o.now().Format("2006-01-02")
	if !set.IsBusinessDay(d) {
		d = set.PrevBusinessDay(d)
	}
	return d
}

// SetWhatsApp records a client's WhatsApp consent from the portal.
func (o *Ops) SetWhatsApp(ctx context.Context, tenant, actor, ucc string, on bool) (Client, error) {
	accts, err := o.PortalAccounts(ctx, tenant, actor)
	if err != nil {
		return Client{}, err
	}
	ucc = strings.ToUpper(ucc)
	if !contains(accts, ucc) {
		return Client{}, ErrNotFound
	}
	c, err := docs.Update(ctx, o.Docs, tenant, cClient, ucc, actor, func(x *Client) error { x.WhatsApp = on; return nil })
	if err == nil {
		o.log(ctx, tenant, actor, "client.whatsapp", ucc, map[string]any{"opt_in": on})
	}
	return c, err
}

// TestMessage sends one template to one client with sample values.
func (o *Ops) TestMessage(ctx context.Context, tenant, actor, templateID, ucc string) ([]Message, error) {
	if err := o.require(ctx, tenant, actor, "Testing templates", RoleChecker, RoleCompliance); err != nil {
		return nil, err
	}
	t, _, err := docs.Get[Template](ctx, o.Docs, tenant, cTemplate, templateID)
	if err != nil {
		return nil, err
	}
	data := map[string]string{}
	for _, v := range EventVars[t.Event] {
		data[v] = "[" + v + "]"
	}
	t.Active = true
	c, err := o.Client(ctx, tenant, ucc)
	if err != nil {
		return nil, err
	}
	set, _ := o.Settings(ctx, tenant)
	data["name"], data["ucc"], data["firm"] = c.Name, c.UCC, set.Firm
	m, err := o.deliver(ctx, tenant, actor, t, c, data)
	return []Message{m}, err
}
