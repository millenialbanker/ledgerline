package ops

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// ---- stock SIPs: topping up by UPI AutoPay or e-mandate ----
//
// When the firm switches top-ups on, a client may give a mandate on their
// own registered bank account (Cashfree Subscriptions: an on-demand
// subscription, authorised by UPI AutoPay or e-mandate). Two days before
// an instalment, if the client's free funds will not cover it, Ledgerline
// raises a debit of the shortfall for the next day, at most ₹15,000 (RBI
// allows e-mandate debits without additional authentication up to that,
// with a pre-debit notice at least 24 hours before:
// https://www.rbi.org.in/Scripts/NotificationUser.aspx?Id=12570). Cashfree
// sends the pre-debit notice (it needs 25 hours between the notice and the
// debit) and the client is also told through the firm's messages.
//
// The money is a receipt credited to the client's ledger when Cashfree
// reports the debit, exactly like a payment gateway receipt: against the
// gateway receivable, which Cashfree's settlement into the client money
// bank account clears (Receipts & payouts → gateway settlement). Cashfree
// must therefore settle into the client bank account, never the firm's
// own. A shortfall above the cap is not debited at all: the instalment
// would fail anyway.
//
// Cashfree's subscription API (public):
// create https://www.cashfree.com/docs/api-reference/payments/latest/subscription/mandate/create
// raise a charge https://www.cashfree.com/docs/api-reference/payments/latest/subscription/payment/raise
// notices https://www.cashfree.com/docs/api-reference/payments/latest/subscription/webhooks
// signature https://www.cashfree.com/docs/api-reference/webhooks/payloads-and-signatures
// Tested here only against a fake Cashfree server.
//
// UPI block (SEBI's ASBA-like facility for secondary market trading,
// mandatory for qualified stock brokers to offer from 1 February 2025) is
// another way to fund SIPs and is not built: it is flagged for later.

const connSIPMandate = "stocksip-mandate"

const cashfreeSubsVersion = "2026-01-01" // x-api-version on Cashfree's latest subscription API pages

var cashfreePGBases = [2]string{"https://api.cashfree.com/pg", "https://sandbox.cashfree.com/pg"}

// sipPayHTTP is replaced in tests.
var sipPayHTTP = &http.Client{Timeout: 30 * time.Second}

func init() {
	ConnProviders = append(ConnProviders, ConnProvider{ID: connSIPMandate, Name: "Stock SIP top-ups (UPI AutoPay / e-mandate)", Group: "Money", Test: "https",
		Purpose: "Debits a client's shortfall for the next stock SIP instalment from their own bank account by UPI AutoPay or e-mandate (Cashfree Subscriptions), at most ₹15,000 a debit, with a day's notice. Cashfree must settle into the client money bank account. Give Cashfree the notice address /hooks/<firm>/stocksip/cashfree",
		Fields: []ConnField{fVend("Cashfree"), {Key: "client_id", Label: "Client ID"}, {Key: "api_key", Label: "Client secret (also signs Cashfree's notices)", Secret: true},
			{Key: "mode", Label: "Environment", Options: []string{"Live", verifySandbox}, Optional: true},
			{Key: "methods", Label: "Mandate methods", Options: []string{"upi", "upi, enach", "enach"}, Optional: true},
			{Key: "url", Label: "Address (leave empty for Cashfree's)", Optional: true, Placeholder: "https://…"}, fNotes}})
}

// SIPMandate is a client's top-up mandate.
type SIPMandate struct {
	UCC            string       `json:"ucc"`
	Provider       string       `json:"provider"`
	SubscriptionID string       `json:"subscription_id"`
	CFSubscription string       `json:"cf_subscription_id,omitempty"`
	Session        string       `json:"session,omitempty"` // checkout session (kept for the authorisation page)
	Token          string       `json:"token,omitempty"`   // the authorisation page's address
	Status         string       `json:"status"`            // Awaiting authorisation, Active, Paused, On hold, Cancelled, Ended, Authorisation failed
	MaxAmount      money.Amount `json:"max_amount"`
	Methods        []string     `json:"methods"`
	BankLast4      string       `json:"bank_last4,omitempty"`
	Live           bool         `json:"live"`
	CreatedAt      string       `json:"created_at"`
	CreatedBy      string       `json:"created_by"`
	UpdatedAt      string       `json:"updated_at,omitempty"`
	Note           string       `json:"note,omitempty"`
}

// Public leaves out what opens the checkout.
func (m SIPMandate) Public() SIPMandate { m.Session, m.Token = "", ""; return m }

// SIPTopup is one debit raised for an instalment day.
type SIPTopup struct {
	ID         string       `json:"id"` // trade date|UCC
	UCC        string       `json:"ucc"`
	TradeDate  string       `json:"trade_date"`
	DebitDate  string       `json:"debit_date"`
	Need       money.Amount `json:"need"`   // the day's SIP orders with charges
	Free       money.Amount `json:"free"`   // free funds when raised
	Amount     money.Amount `json:"amount"` // debit asked for
	PaymentID  string       `json:"payment_id,omitempty"`
	CFPayment  string       `json:"cf_payment_id,omitempty"`
	Status     string       `json:"status"` // Scheduled, Success, Failed, Not raised
	Reason     string       `json:"reason,omitempty"`
	Receipt    string       `json:"receipt,omitempty"`
	RaisedAt   string       `json:"raised_at"`
	UpdatedAt  string       `json:"updated_at,omitempty"`
	Scrips     []string     `json:"scrips"`
	NoticeSent bool         `json:"notice_sent,omitempty"`
}

type cashfreeSubs struct {
	base, id, secret string
	live             bool
}

func (o *Ops) sipMandateConn(ctx context.Context, tenant string) (*cashfreeSubs, error) {
	c, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connSIPMandate)
	if err != nil || c.Status == "Not set" || c.Values["client_id"] == "" || c.Secrets["api_key"] == "" {
		return nil, fmt.Errorf("%w: set up Stock SIP top-ups in Settings → Connections", ErrState)
	}
	if c.Values["provider"] != "Cashfree" {
		return nil, fmt.Errorf("%w: only Cashfree is built in for SIP top-ups", ErrState)
	}
	live := c.Values["mode"] != verifySandbox
	base := cashfreePGBases[0]
	if !live {
		base = cashfreePGBases[1]
	}
	if u := strings.TrimRight(c.Values["url"], "/"); u != "" {
		base = u
	}
	return &cashfreeSubs{base: base, id: c.Values["client_id"], secret: c.Secrets["api_key"], live: live}, nil
}

func sipMandateMethods(c Connection) []string {
	var out []string
	for _, m := range strings.Split(firstOf(c.Values["methods"], "upi"), ",") {
		if m = strings.TrimSpace(m); m != "" {
			out = append(out, m)
		}
	}
	return out
}

func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

func (c *cashfreeSubs) post(ctx context.Context, path string, body, out any) (int, error) {
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(b))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-client-id", c.id)
	req.Header.Set("x-client-secret", c.secret)
	req.Header.Set("x-api-version", cashfreeSubsVersion)
	req.Header.Set("x-idempotency-key", newUUID())
	resp, err := sipPayHTTP.Do(req)
	if err != nil {
		return 0, fmt.Errorf("Cashfree did not answer")
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var e struct {
		Message string `json:"message"`
		Code    string `json:"code"`
	}
	_ = json.Unmarshal(rb, &e)
	if resp.StatusCode/100 != 2 {
		why := firstOf(e.Message, trimBody(rb))
		if resp.StatusCode == 401 || resp.StatusCode == 403 {
			why = "Cashfree refused the keys; check Settings → Connections"
		}
		return resp.StatusCode, fmt.Errorf("Cashfree answered %d: %s", resp.StatusCode, why)
	}
	if out != nil {
		_ = json.Unmarshal(rb, out)
	}
	return resp.StatusCode, nil
}

// SIPMandateStart is a new mandate and where the client authorises it.
type SIPMandateStart struct {
	Mandate SIPMandate `json:"mandate"`
	AuthURL string     `json:"auth_url"`
}

// StartStockSIPMandate creates a top-up mandate for a client on their
// registered bank account; the client then authorises it at the address
// returned (base is the server's public address).
func (o *Ops) StartStockSIPMandate(ctx context.Context, tenant, actor, ucc, base string) (SIPMandateStart, error) {
	var res SIPMandateStart
	ucc = strings.ToUpper(strings.TrimSpace(ucc))
	if role, err := o.RoleOf(ctx, tenant, actor); err == nil && role == RoleClient {
		accts, err := o.PortalAccounts(ctx, tenant, actor)
		if err != nil {
			return res, err
		}
		if !contains(accts, ucc) {
			return res, ErrNotFound
		}
	} else if err := o.require(ctx, tenant, actor, "Setting up a stock SIP top-up mandate", RoleMaker, RoleChecker); err != nil {
		return res, err
	}
	cfg := o.sipSettings(ctx, tenant)
	if !cfg.Enabled || !cfg.Topups {
		return res, fmt.Errorf("%w: the firm does not offer SIP top-ups", ErrState)
	}
	cf, err := o.sipMandateConn(ctx, tenant)
	if err != nil {
		return res, err
	}
	c, err := o.Client(ctx, tenant, ucc)
	if err != nil {
		return res, err
	}
	if c.Bank.Account == "" || c.Bank.IFSC == "" {
		return res, invalid("the client has no registered bank account; the mandate must be on it")
	}
	if cur, _, err := docs.Get[SIPMandate](ctx, o.Docs, tenant, cSIPMandate, ucc); err == nil && cur.Status == "Active" {
		return res, fmt.Errorf("%w: %s already has an active mandate (%s)", ErrState, ucc, cur.SubscriptionID)
	}
	conn, _, _ := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connSIPMandate)
	n, err := o.next(ctx, tenant, "stocksip-mandate", actor)
	if err != nil {
		return res, err
	}
	set, _ := o.Settings(ctx, tenant)
	now := o.now()
	m := SIPMandate{UCC: ucc, Provider: "Cashfree", SubscriptionID: fmt.Sprintf("LLSIP-%s-%d", ucc, n), Status: "Awaiting authorisation", MaxAmount: cfg.TopupCap,
		Methods: sipMandateMethods(conn), BankLast4: MaskAccount(c.Bank.Account), Live: cf.live, CreatedAt: now.Format(time.RFC3339), CreatedBy: actor}
	if len(m.BankLast4) > 4 {
		m.BankLast4 = m.BankLast4[len(m.BankLast4)-4:]
	}
	body := map[string]any{
		"subscription_id": m.SubscriptionID,
		"customer_details": map[string]any{"customer_name": c.Name, "customer_email": c.Email, "customer_phone": c.Mobile,
			"customer_bank_account_holder_name": truncate(c.Name, 40), "customer_bank_account_number": c.Bank.Account, "customer_bank_ifsc": c.Bank.IFSC},
		"plan_details": map[string]any{"plan_name": "Stock SIP top-up", "plan_type": "ON_DEMAND", "plan_currency": "INR", "plan_max_amount": cfg.TopupCap.Dec().Float64(),
			"plan_note": "Top-up of " + c.UCC + "'s trading account with " + truncate(set.Firm, 60) + " for stock SIP instalments"},
		"authorization_details":    map[string]any{"authorization_amount": 1, "authorization_amount_refund": true, "payment_methods": m.Methods},
		"subscription_expiry_time": now.AddDate(5, 0, 0).Format("2006-01-02T15:04:05-07:00"),
		"subscription_tags":        map[string]string{"ucc": c.UCC, "psp_note": truncate("SIP top-up "+set.Firm, 60)},
	}
	if b := strings.TrimRight(base, "/"); b != "" {
		body["subscription_meta"] = map[string]any{"return_url": b + "/#psip"}
	}
	var r struct {
		CFID    any    `json:"cf_subscription_id"`
		Session string `json:"subscription_session_id"`
		Status  string `json:"subscription_status"`
	}
	if _, err := cf.post(ctx, "/subscriptions", body, &r); err != nil {
		o.log(ctx, tenant, actor, "stocksip.mandate.failed", ucc, map[string]any{"error": err.Error()})
		return res, fmt.Errorf("%w: %v", ErrState, err)
	}
	if r.Session == "" {
		return res, fmt.Errorf("%w: Cashfree did not return a checkout session", ErrState)
	}
	tok, err := newToken()
	if err != nil {
		return res, err
	}
	m.CFSubscription, m.Session, m.Token = fmt.Sprint(r.CFID), r.Session, tok
	if st := cashfreeMandateStatus(r.Status, ""); st != "" && st != "Active" {
		m.Status = st
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cSIPMandate, ucc, m, actor); err != nil {
		return res, err
	}
	o.log(ctx, tenant, actor, "stocksip.mandate.created", ucc, map[string]any{"subscription": m.SubscriptionID, "max": m.MaxAmount.String(), "methods": m.Methods})
	res.Mandate = m.Public()
	res.AuthURL = strings.TrimRight(base, "/") + "/hooks/" + tenant + "/stocksip/mandate/" + tok
	return res, nil
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// cashfreeMandateStatus maps Cashfree's subscription and authorisation
// statuses to the mandate's.
func cashfreeMandateStatus(sub, auth string) string {
	switch strings.ToUpper(sub) {
	case "ACTIVE":
		return "Active"
	case "INITIALIZED", "BANK_APPROVAL_PENDING":
		if strings.EqualFold(auth, "FAILED") {
			return "Authorisation failed"
		}
		return "Awaiting authorisation"
	case "ON_HOLD":
		return "On hold"
	case "CUSTOMER_PAUSED":
		return "Paused"
	case "CANCELLED", "CUSTOMER_CANCELLED":
		return "Cancelled"
	case "COMPLETED", "EXPIRED", "LINK_EXPIRED", "CARD_EXPIRED":
		return "Ended"
	}
	if strings.EqualFold(auth, "FAILED") {
		return "Authorisation failed"
	}
	return ""
}

// SIPMandatePage is what the authorisation page shows.
type SIPMandatePage struct {
	Firm    string
	Name    string
	Session string
	Mode    string // production or sandbox, for Cashfree's checkout
	Max     string
	Status  string
	Error   string
	Nonce   string // set by the page's handler for its own script
}

// StockSIPMandatePage looks up the authorisation page of a mandate.
func (o *Ops) StockSIPMandatePage(ctx context.Context, tenant, token string) (SIPMandatePage, error) {
	set, _ := o.Settings(ctx, tenant)
	p := SIPMandatePage{Firm: set.Firm}
	ms, _ := docs.List[SIPMandate](ctx, o.Docs, tenant, cSIPMandate, "")
	for _, m := range ms {
		if m.Token == "" || len(token) < 16 || subtle.ConstantTimeCompare([]byte(m.Token), []byte(token)) != 1 {
			continue
		}
		c, _ := o.Client(ctx, tenant, m.UCC)
		p.Name, p.Session, p.Max, p.Status = strings.Fields(c.Name + " x")[0], m.Session, formatINR(m.MaxAmount.Dec()), m.Status
		p.Mode = map[bool]string{true: "production", false: "sandbox"}[m.Live]
		if m.Status != "Awaiting authorisation" {
			p.Error = "This mandate is " + strings.ToLower(m.Status) + "."
			p.Session = ""
		}
		return p, nil
	}
	p.Error = "This link is not valid."
	return p, ErrNotFound
}

// SIPMandateHTML is the page where the client authorises the mandate in
// Cashfree's subscription checkout (Cashfree's JS SDK, subscriptionsCheckout
// with the session: https://www.cashfree.com/docs/payments/subscription/hosted-checkout).
// The SDK address is Cashfree's published v3 SDK; it is not exercised by
// Ledgerline's tests.
var SIPMandateHTML = template.Must(template.New("mandate").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Top-up mandate · {{.Firm}}</title>
<style>body{font:16px/1.5 system-ui,-apple-system,Segoe UI,sans-serif;background:#f3f4f1;color:#1d2420;margin:0;padding:24px 16px}main{max-width:480px;margin:0 auto;background:#fff;border:1px solid #d9ddd6;border-radius:12px;padding:24px}h1{font-size:20px;margin:0 0 12px}button{font:inherit;padding:10px 16px;border-radius:8px;border:1px solid #1f6b45;background:#1f6b45;color:#fff;cursor:pointer}small{color:#5b655e}</style></head><body><main>
{{if .Error}}<h1>{{.Firm}}</h1><p>{{.Error}}</p>{{else}}<h1>Top-up mandate for your stock SIPs</h1>
<p>Dear {{.Name}}, this authorises {{.Firm}} to debit your registered bank account, through Cashfree, for the shortfall in your trading account before a stock SIP instalment: at most ₹{{.Max}} a debit, each with a notice at least 24 hours before. The money is credited to your trading account.</p>
<p><button id="go" data-session="{{.Session}}" data-mode="{{.Mode}}">Authorise with UPI AutoPay or e-mandate</button></p>
<p><small>You can pause or revoke the mandate at any time in your UPI app or bank, or by writing to us.</small></p>
<script src="https://sdk.cashfree.com/js/v3/cashfree.js"></script>
<script nonce="{{.Nonce}}">document.getElementById('go').onclick=function(){var b=this;Cashfree({mode:b.dataset.mode}).subscriptionsCheckout({subsSessionId:b.dataset.session,redirectTarget:'_self'});};</script>{{end}}
</main></body></html>`))

// SIPTopupResult is what raising a day's top-ups did.
type SIPTopupResult struct {
	TradeDate string     `json:"trade_date"`
	DebitDate string     `json:"debit_date"`
	Topups    []SIPTopup `json:"topups"`
	Message   string     `json:"message"`
}

// sipPendingTopups are debits raised for a trade date and not yet
// reported: they count towards the day's free funds. Debits that came in
// are already in the ledger as receipts.
func (o *Ops) sipPendingTopups(ctx context.Context, tenant, date string) map[string]money.Dec {
	out := map[string]money.Dec{}
	ts, _ := docs.List[SIPTopup](ctx, o.Docs, tenant, cSIPTopup, date+"|")
	for _, t := range ts {
		if t.Status == "Scheduled" {
			out[t.UCC] = out[t.UCC].Add(t.Amount.Dec())
		}
	}
	return out
}

// RaiseStockSIPTopups raises tomorrow's debits for the shortfall of the
// instalments on tradeDate (the day after the debit). It is run the day
// before the debit, so the client has at least 24 hours' notice.
func (o *Ops) RaiseStockSIPTopups(ctx context.Context, tenant, actor, tradeDate string) (SIPTopupResult, error) {
	res := SIPTopupResult{TradeDate: tradeDate, Topups: []SIPTopup{}}
	if actor != SystemActor {
		if err := o.require(ctx, tenant, actor, "Raising stock SIP top-up debits", RoleMaker, RoleChecker); err != nil {
			return res, err
		}
	}
	cfg := o.sipSettings(ctx, tenant)
	if !cfg.Enabled || !cfg.Topups {
		return res, fmt.Errorf("%w: SIP top-ups are switched off", ErrState)
	}
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return res, err
	}
	t, err := time.Parse("2006-01-02", tradeDate)
	if err != nil || !set.IsBusinessDay(tradeDate) {
		return res, invalid("give the trading day of the instalments")
	}
	now := o.now()
	today := now.Format("2006-01-02")
	res.DebitDate = t.AddDate(0, 0, -1).Format("2006-01-02")
	if today >= res.DebitDate {
		return res, invalid("too late for %s: a debit on %s must be notified to the client at least 24 hours before, so it is raised by %s", tradeDate, res.DebitDate, t.AddDate(0, 0, -2).Format("2006-01-02"))
	}
	cf, err := o.sipMandateConn(ctx, tenant)
	if err != nil {
		return res, err
	}
	sips, err := o.StockSIPs(ctx, tenant, "")
	if err != nil {
		return res, err
	}
	clients, _ := o.ClientMap(ctx, tenant)
	look, asAt, _ := o.liveLookup(ctx, tenant, today, now.Format("15:04:05"))
	free, _ := o.sipFreeFunds(ctx, tenant, today)
	// What today's own SIP orders still need comes off today's free funds.
	if ts, _ := docs.List[SIPOrder](ctx, o.Docs, tenant, cSIPOrder, today+"|"); len(ts) > 0 {
		for _, x := range ts {
			if x.reserves() {
				free[x.UCC] = free[x.UCC].Sub(x.Estimate.Dec())
			}
		}
	}
	need := map[string]money.Dec{}
	scrips := map[string][]string{}
	huge := money.DecInt(1 << 50)
	for _, s := range sips {
		if s.Status != SIPActive && s.Status != SIPPaused {
			continue
		}
		for _, sched := range s.scheduledFor(set, tradeDate) {
			x := o.sipEvaluate(ctx, tenant, cfg, s, clients[s.UCC], look, asAt, "", tradeDate, sched, huge, money.Dec{})
			if x.Status != SIPOrdPending {
				continue
			}
			need[s.UCC] = need[s.UCC].Add(x.Estimate.Dec())
			scrips[s.UCC] = append(scrips[s.UCC], s.Symbol)
		}
	}
	raised := 0
	for _, u := range sortedKeys(need) {
		id := tradeDate + "|" + u
		if _, _, err := docs.Get[SIPTopup](ctx, o.Docs, tenant, cSIPTopup, id); err == nil {
			continue
		}
		short := need[u].Sub(free[u])
		if short.Sign() <= 0 {
			continue
		}
		tp := SIPTopup{ID: id, UCC: u, TradeDate: tradeDate, DebitDate: res.DebitDate, Need: need[u].Round(money.UnitPaisa), Free: free[u].Round(money.UnitPaisa),
			Amount: short.CeilRupee().Round(money.UnitPaisa), Status: "Not raised", RaisedAt: now.Format(time.RFC3339), Scrips: scrips[u]}
		m, _, err := docs.Get[SIPMandate](ctx, o.Docs, tenant, cSIPMandate, u)
		limit := cfg.TopupCap
		if err == nil && m.MaxAmount > 0 && m.MaxAmount < limit {
			limit = m.MaxAmount
		}
		switch {
		case err != nil || m.Status != "Active":
			continue // no mandate: nothing to raise or record
		case tp.Amount > limit:
			tp.Reason = fmt.Sprintf("the shortfall ₹%s is above the ₹%s a mandate debit may be without additional authentication", formatINR(tp.Amount.Dec()), formatINR(limit.Dec()))
		default:
			tp.PaymentID = "SIPT-" + strings.ReplaceAll(tradeDate, "-", "") + "-" + u
			var r struct {
				CFPayment any    `json:"cf_payment_id"`
				Status    string `json:"payment_status"`
			}
			_, err := cf.post(ctx, "/subscriptions/pay", map[string]any{"subscription_id": m.SubscriptionID, "payment_id": tp.PaymentID, "payment_type": "CHARGE",
				"payment_amount": tp.Amount.Dec().Float64(), "payment_schedule_date": res.DebitDate, "payment_remarks": "Stock SIP top-up for " + tradeDate}, &r)
			if err != nil {
				tp.Status, tp.Reason = "Failed", err.Error()
				break
			}
			tp.Status = "Scheduled"
			if r.CFPayment != nil {
				tp.CFPayment = fmt.Sprint(r.CFPayment)
			}
			if strings.EqualFold(r.Status, "FAILED") {
				tp.Status, tp.Reason = "Failed", "Cashfree did not accept the debit"
			}
		}
		if tp.Status == "Scheduled" {
			raised++
			tp.NoticeSent = true
			o.sipNotify(ctx, tenant, actor, "stocksip.predebit", u, map[string]string{"amount": formatINR(tp.Amount.Dec()), "debit_date": fmtDay(tp.DebitDate),
				"trade_date": fmtDay(tradeDate), "scrips": strings.Join(tp.Scrips, ", ")})
		}
		if _, err := docs.Put(ctx, o.Docs, tenant, cSIPTopup, id, tp, 0, actor); err != nil {
			return res, err
		}
		o.log(ctx, tenant, actor, "stocksip.topup", id, map[string]any{"amount": tp.Amount.String(), "status": tp.Status, "reason": tp.Reason, "debit_date": tp.DebitDate})
		res.Topups = append(res.Topups, tp)
	}
	res.Message = fmt.Sprintf("%d top-up debits raised for %s (debit on %s)", raised, tradeDate, res.DebitDate)
	return res, nil
}

// StockSIPPaymentHook takes Cashfree's notices: a mandate authorised or
// changed, and a top-up debit's result. A successful debit is credited to
// the client's ledger at once as a receipt against the gateway
// receivable, cleared when Cashfree settles into the client bank account.
func (o *Ops) StockSIPPaymentHook(ctx context.Context, tenant string, h http.Header, body []byte) (string, error) {
	c, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connSIPMandate)
	if err != nil || c.Status == "Not set" || c.Secrets["api_key"] == "" {
		return "", fmt.Errorf("%w: SIP top-ups are not set up", ErrForbidden)
	}
	if !cashfreeSigOK(c.Secrets["api_key"], h.Get("x-webhook-timestamp"), h.Get("x-webhook-signature"), body) {
		return "", fmt.Errorf("%w: signature does not match", ErrForbidden)
	}
	v, err := decodeJSON(body)
	if err != nil {
		return "", invalid("%v", err)
	}
	typ := jsonStr(v, "type")
	now := o.now().Format(time.RFC3339)
	switch typ {
	case "SUBSCRIPTION_STATUS_CHANGED", "SUBSCRIPTION_AUTH_STATUS":
		sid := firstOf(jsonStr(v, "data.subscription_details.subscription_id"), jsonStr(v, "data.subscription_id"))
		st := cashfreeMandateStatus(jsonStr(v, "data.subscription_details.subscription_status"), jsonStr(v, "data.authorization_details.authorization_status"))
		ms, _ := docs.List[SIPMandate](ctx, o.Docs, tenant, cSIPMandate, "")
		for _, m := range ms {
			if m.SubscriptionID != sid || st == "" {
				continue
			}
			m.Status, m.UpdatedAt = st, now
			if err := docs.Upsert(ctx, o.Docs, tenant, cSIPMandate, m.UCC, m, "cashfree"); err != nil {
				return "", err
			}
			o.log(ctx, tenant, "cashfree", "stocksip.mandate."+strings.ToLower(strings.ReplaceAll(st, " ", "_")), m.UCC, map[string]any{"subscription": sid})
			return "mandate " + st, nil
		}
		return "ignored: unknown subscription", nil
	case "SUBSCRIPTION_PAYMENT_SUCCESS", "SUBSCRIPTION_PAYMENT_FAILED", "SUBSCRIPTION_PAYMENT_CANCELLED":
	default:
		return "ignored", nil
	}
	if !strings.EqualFold(jsonStr(v, "data.payment_type"), "CHARGE") {
		return "ignored: not a charge", nil
	}
	pid := jsonStr(v, "data.payment_id")
	parts := strings.SplitN(pid, "-", 3) // SIPT-yyyymmdd-UCC
	if len(parts) != 3 || parts[0] != "SIPT" || len(parts[1]) != 8 {
		return "ignored: not a SIP top-up", nil
	}
	id := parts[1][:4] + "-" + parts[1][4:6] + "-" + parts[1][6:] + "|" + parts[2]
	tp, _, err := docs.Get[SIPTopup](ctx, o.Docs, tenant, cSIPTopup, id)
	if err != nil {
		return "", invalid("top-up %s is not on record", pid)
	}
	cfid := jsonStr(v, "data.cf_payment_id")
	if typ != "SUBSCRIPTION_PAYMENT_SUCCESS" {
		if tp.Status == "Success" {
			return "ignored: already credited", nil
		}
		tp.Status, tp.Reason, tp.CFPayment, tp.UpdatedAt = "Failed", firstOf(jsonStr(v, "data.failure_details.failure_reason"), strings.ToLower(strings.TrimPrefix(typ, "SUBSCRIPTION_PAYMENT_"))), firstOf(cfid, tp.CFPayment), now
		if err := docs.Upsert(ctx, o.Docs, tenant, cSIPTopup, id, tp, "cashfree"); err != nil {
			return "", err
		}
		o.log(ctx, tenant, "cashfree", "stocksip.topup.failed", id, map[string]any{"reason": tp.Reason})
		return "top-up failed", nil
	}
	if tp.Receipt != "" {
		return "ignored: already credited as " + tp.Receipt, nil
	}
	amt := parseAmountUnit(jsonStr(v, "data.payment_amount"), false)
	if amt <= 0 || cfid == "" {
		return "", invalid("the notice has no amount or Cashfree payment ID")
	}
	cl, err := o.Client(ctx, tenant, tp.UCC)
	if err != nil {
		return "", err
	}
	date, _ := creditWhen(jsonStr(v, "event_time"), o.now())
	r := Receipt{ID: "GW-CF-" + cfid, Date: date, UCC: cl.UCC, Name: cl.Name, Mode: "Gateway", Ref: cfid, FromAccount: cl.Bank.Account, Amount: amt,
		Status: "Credited", Maker: "gateway", Checker: "gateway", Note: "Stock SIP top-up by mandate (" + pid + ")"}
	if _, _, err := docs.Get[Receipt](ctx, o.Docs, tenant, cReceipt, r.ID); err == nil {
		return "ignored: already credited", nil
	}
	e, err := o.postReceipt(ctx, tenant, "gateway", r, ReceivableGateway)
	if err != nil {
		return "", err
	}
	r.Entry = e.ID
	if _, err := docs.Put(ctx, o.Docs, tenant, cReceipt, r.ID, r, 0, "gateway"); err != nil {
		return "", err
	}
	tp.Status, tp.CFPayment, tp.Receipt, tp.Reason, tp.UpdatedAt = "Success", cfid, r.ID, "", now
	if amt != tp.Amount {
		tp.Reason = fmt.Sprintf("Cashfree debited ₹%s against ₹%s asked", formatINR(amt.Dec()), formatINR(tp.Amount.Dec()))
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cSIPTopup, id, tp, "cashfree"); err != nil {
		return "", err
	}
	o.log(ctx, tenant, "gateway", "receipt.gateway", r.ID, map[string]any{"ucc": r.UCC, "amount": r.Amount.String(), "for": "stock SIP top-up " + pid})
	o.AllocationMoneyIn(ctx, tenant, r.UCC, r.Amount, r.ID)
	o.notify(ctx, tenant, "gateway", "receipt.credited", r.UCC, map[string]string{"amount": formatINR(r.Amount.Dec()), "mode": "SIP top-up mandate", "date": r.Date, "status": "credited"})
	return "credited " + r.ID, nil
}

// StockSIPTopups lists debits raised, newest first.
func (o *Ops) StockSIPTopups(ctx context.Context, tenant string, limit int) []SIPTopup {
	ts, _ := docs.List[SIPTopup](ctx, o.Docs, tenant, cSIPTopup, "")
	sort.SliceStable(ts, func(i, j int) bool { return ts[i].ID > ts[j].ID })
	if limit > 0 && len(ts) > limit {
		ts = ts[:limit]
	}
	return ts
}
