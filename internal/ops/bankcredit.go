package ops

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// ---- live bank credits ----
//
// Money a client sends to the firm's client bank account (NEFT, RTGS, IMPS,
// UPI) can be reported to Ledgerline the moment it lands, instead of waiting
// for the statement. Three kinds of sender are supported, each a connection
// in Settings → Connections:
//
//   - credits-bank: the firm's own bank (HDFC e-CMS, Axis Power eColl, ICICI
//     e-Collection, Kotak, Yes Bank and others). Banks give their message
//     format only to their customers, so the format is described in the
//     connection itself — where in the message the amount, UTR, virtual
//     account and payer account are, and how the bank proves the message is
//     genuine — from the specification the bank sends.
//   - credits-razorpay: Razorpay Smart Collect (virtual accounts), whose
//     "virtual_account.credited" notice is public.
//   - credits-cashfree: Cashfree virtual bank accounts, through its payment
//     notice (PAYMENT_SUCCESS_WEBHOOK with a VBA transfer).
//
// Each credit becomes a statement credit exactly as if it had come in the
// bank statement file, keyed by its UTR, so the statement later finds it
// already there. A virtual account names the client, but the money is still
// a receipt only if it comes from the client's registered bank account;
// otherwise it is held as third-party funds and an alert is raised.

const (
	connCreditsBank     = "credits-bank"
	connCreditsRazorpay = "credits-razorpay"
	connCreditsCashfree = "credits-cashfree"
)

// creditProviders maps the address part (/hooks/{firm}/bank/{name}) to the
// connection.
var creditProviders = map[string]string{"bank": connCreditsBank, "razorpay": connCreditsRazorpay, "cashfree": connCreditsCashfree}

// BankCreditBanks are the banks offered for credits-bank.
var BankCreditBanks = []string{"HDFC Bank e-CMS", "Axis Bank Power eColl", "ICICI Bank e-Collection", "Kotak Mahindra Bank e-Collections",
	"Yes Bank virtual accounts", "IDFC First Bank", "IndusInd Bank", "Federal Bank Collection Hub", "State Bank of India", "Other"}

const (
	authToken   = "Token in a header"
	authHMACHex = "HMAC-SHA256 of the body, hex, in a header"
	authHMACB64 = "HMAC-SHA256 of the body, base64, in a header"
)

// bankCreditFields are where the generic bank adapter finds each value.
var bankCreditFields = []struct{ Key, Label string }{
	{"path_records", "Where the list of credits is (empty if each message is one credit)"},
	{"path_amount", "Amount"},
	{"path_utr", "UTR / bank reference"},
	{"path_virtual_account", "Virtual account number"},
	{"path_payer_account", "Payer account number"},
	{"path_payer_ifsc", "Payer IFSC"},
	{"path_payer_name", "Payer name"},
	{"path_mode", "Mode (NEFT, RTGS, IMPS, UPI)"},
	{"path_time", "Credit date and time"},
}

func bankCreditConnFields() []ConnField {
	f := []ConnField{
		fVend(BankCreditBanks...),
		{Key: "auth", Label: "How the bank proves a message is genuine", Options: []string{authToken, authHMACHex, authHMACB64}},
		{Key: "header", Label: "Header carrying the token or signature", Placeholder: "e.g. X-Signature"},
		{Key: "secret", Label: "Shared token or signing key", Secret: true},
		{Key: "va_prefix", Label: "Virtual account = this prefix + client code", Optional: true, Placeholder: "e.g. BALJ"},
		{Key: "amount_unit", Label: "Amount is in", Options: []string{"Rupees", "Paise"}, Optional: true},
	}
	for _, p := range bankCreditFields {
		f = append(f, ConnField{Key: p.Key, Label: "Field: " + p.Label, Optional: p.Key != "path_amount" && p.Key != "path_utr", Placeholder: "e.g. data.txn.amount"})
	}
	return append(f, ConnField{Key: "ack", Label: "Reply the bank expects (empty: {\"status\":\"ok\"})", Optional: true}, fNotes)
}

func init() {
	ConnProviders = append(ConnProviders,
		ConnProvider{ID: connCreditsBank, Name: "Bank credit notifications (client money account)", Group: "Money", Test: "none",
			Purpose: "Client money credited to the client bank account, reported by the bank as it lands. Fill in where each value is in the bank's message from the specification the bank gives you; the address to give the bank is /hooks/<firm>/bank/bank",
			Fields:  bankCreditConnFields()},
		ConnProvider{ID: connCreditsRazorpay, Name: "Razorpay Smart Collect (virtual accounts)", Group: "Money", Test: "none",
			Purpose: "Bank transfers into Razorpay virtual accounts (virtual_account.credited); address /hooks/<firm>/bank/razorpay",
			Fields:  []ConnField{{Key: "secret", Label: "Webhook secret", Secret: true}, {Key: "va_prefix", Label: "Virtual account = this prefix + client code (or client code in the account's notes.ucc)", Optional: true}, fNotes}},
		ConnProvider{ID: connCreditsCashfree, Name: "Cashfree virtual bank accounts", Group: "Money", Test: "none",
			Purpose: "Transfers into Cashfree virtual bank accounts (PAYMENT_SUCCESS_WEBHOOK); address /hooks/<firm>/bank/cashfree",
			Fields:  []ConnField{{Key: "secret", Label: "Client secret (signs the notices)", Secret: true}, {Key: "va_prefix", Label: "Virtual account = this prefix + client code", Optional: true}, fNotes}},
	)
}

// checkCreditConn is run when a credits-bank connection is saved.
func checkCreditConn(c Connection) error {
	if c.Values["auth"] == "" || c.Values["header"] == "" {
		return invalid("choose how the bank signs its messages and the header it uses")
	}
	for _, k := range []string{"path_records", "path_amount", "path_utr", "path_virtual_account", "path_payer_account", "path_payer_ifsc", "path_payer_name", "path_mode", "path_time"} {
		if p := c.Values[k]; p != "" && !rePath.MatchString(p) {
			return invalid("%s: write the field as names separated by dots, with [0] for list items, e.g. data.credits[0].amount", strings.TrimPrefix(k, "path_"))
		}
	}
	return nil
}

var rePath = regexp.MustCompile(`^[A-Za-z0-9_$@-]+(\[\d+\])?(\.[A-Za-z0-9_$@-]+(\[\d+\])?)*$`)

// BankCredit is one credit as read from a notice.
type BankCredit struct {
	Ref, VirtualAccount, PayerAccount, PayerIFSC, PayerName, Mode, Time, UCC string
	Amount                                                                   money.Amount
}

// BankCreditResult is what a notice did.
type BankCreditResult struct {
	Received  int      `json:"received"`
	Added     int      `json:"added"`
	Duplicate int      `json:"duplicate"`
	Ignored   int      `json:"ignored"`
	Credits   []string `json:"credits"` // statement credit ids
	Ack       string   `json:"-"`
}

// BankCreditHook checks and records a live credit notice from a bank or a
// collections provider.
func (o *Ops) BankCreditHook(ctx context.Context, tenant, provider string, h http.Header, body []byte) (BankCreditResult, error) {
	res := BankCreditResult{Ack: `{"status":"ok"}`}
	id, ok := creditProviders[provider]
	if !ok {
		return res, ErrNotFound
	}
	c, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, id)
	if err != nil || c.Status == "Not set" || c.Secrets["secret"] == "" {
		return res, fmt.Errorf("%w: %s is not set up in Settings → Connections", ErrForbidden, id)
	}
	if a := c.Values["ack"]; a != "" {
		res.Ack = a
	}
	var credits []BankCredit
	switch id {
	case connCreditsRazorpay:
		if !hmac.Equal([]byte(hexHMAC(c.Secrets["secret"], body)), []byte(strings.ToLower(h.Get("X-Razorpay-Signature")))) {
			return res, fmt.Errorf("%w: signature does not match", ErrForbidden)
		}
		credits, res.Ignored, err = parseRazorpayCredit(body)
	case connCreditsCashfree:
		if !cashfreeSigOK(c.Secrets["secret"], h.Get("x-webhook-timestamp"), h.Get("x-webhook-signature"), body) {
			return res, fmt.Errorf("%w: signature does not match", ErrForbidden)
		}
		credits, res.Ignored, err = parseCashfreeCredit(body)
	default:
		if !bankAuthOK(c, h, body) {
			return res, fmt.Errorf("%w: the message's %s does not match", ErrForbidden, c.Values["header"])
		}
		credits, err = parseMappedCredits(c, body)
	}
	if err != nil {
		return res, invalid("%v", err)
	}
	clients, err := o.Clients(ctx, tenant)
	if err != nil {
		return res, err
	}
	known := map[string]bool{}
	for _, cl := range clients {
		known[cl.UCC] = true
	}
	source := firstOf(c.Values["provider"], map[string]string{connCreditsRazorpay: "Razorpay Smart Collect", connCreditsCashfree: "Cashfree"}[id])
	for _, bc := range credits {
		res.Received++
		if bc.Amount <= 0 || bc.Ref == "" {
			res.Ignored++
			continue
		}
		ucc := strings.ToUpper(bc.UCC)
		if ucc == "" && c.Values["va_prefix"] != "" {
			va := strings.ToUpper(strings.TrimSpace(bc.VirtualAccount))
			if p := strings.ToUpper(c.Values["va_prefix"]); strings.HasPrefix(va, p) {
				ucc = strings.TrimLeft(strings.TrimPrefix(va, p), "0")
				if !known[ucc] {
					ucc = strings.TrimPrefix(va, p) // codes with leading zeros
				}
			}
		}
		if !known[ucc] {
			ucc = ""
		}
		date, tm := creditWhen(bc.Time, o.now())
		acct := strings.TrimSpace(bc.PayerAccount)
		if acct == "" && strings.EqualFold(bc.Mode, "UPI") {
			acct = "VPA"
		}
		t := BankTxn{ID: bc.Ref, Date: date, Time: tm, Mode: strings.ToUpper(firstOf(bc.Mode, "BANK")), Ref: bc.Ref, Remitter: bc.PayerName, Account: acct, Amount: bc.Amount,
			Hint: ucc, VirtualAccount: bc.VirtualAccount, PayerIFSC: strings.ToUpper(bc.PayerIFSC), Source: source + " (live)"}
		if _, _, err := docs.Get[BankTxn](ctx, o.Docs, tenant, cBankTxn, t.ID); err == nil {
			res.Duplicate++
			continue
		}
		t, err := o.matchCredit(ctx, tenant, "bank", t, clients)
		if err != nil {
			return res, err
		}
		if _, err := docs.Put(ctx, o.Docs, tenant, cBankTxn, t.ID, t, 0, "bank"); err != nil {
			return res, err
		}
		o.log(ctx, tenant, "bank", "bank.credit.live", t.Ref, map[string]any{"source": source, "status": t.Status, "ucc": t.UCC, "amount": t.Amount.String()})
		res.Added++
		res.Credits = append(res.Credits, t.ID)
	}
	return res, nil
}

func hexHMAC(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

func b64HMAC(secret string, parts ...[]byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	for _, p := range parts {
		m.Write(p)
	}
	return base64.StdEncoding.EncodeToString(m.Sum(nil))
}

// cashfreeSigOK checks Cashfree's x-webhook-signature: base64 HMAC-SHA256 of
// the timestamp and the raw body. Cashfree's pages show it both with and
// without a dot between the two, so either is accepted.
func cashfreeSigOK(secret, ts, sig string, body []byte) bool {
	if ts == "" || sig == "" {
		return false
	}
	return hmac.Equal([]byte(b64HMAC(secret, []byte(ts), body)), []byte(sig)) || hmac.Equal([]byte(b64HMAC(secret, []byte(ts+"."), body)), []byte(sig))
}

func bankAuthOK(c Connection, h http.Header, body []byte) bool {
	got := strings.TrimSpace(h.Get(c.Values["header"]))
	got = strings.TrimSpace(strings.TrimPrefix(got, "Bearer "))
	if got == "" {
		return false
	}
	switch c.Values["auth"] {
	case authHMACHex:
		return hmac.Equal([]byte(hexHMAC(c.Secrets["secret"], body)), []byte(strings.ToLower(got)))
	case authHMACB64:
		return hmac.Equal([]byte(b64HMAC(c.Secrets["secret"], body)), []byte(got))
	default:
		return hmac.Equal([]byte(c.Secrets["secret"]), []byte(got))
	}
}

// ---- reading the notices ----

func decodeJSON(body []byte) (any, error) {
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return nil, fmt.Errorf("the message is not JSON")
	}
	return v, nil
}

// jsonAt reads a value at a path such as data.credits[0].amount.
func jsonAt(v any, path string) any {
	if path == "" {
		return v
	}
	for _, part := range strings.Split(path, ".") {
		idx := -1
		if i := strings.IndexByte(part, '['); i > 0 && strings.HasSuffix(part, "]") {
			idx, _ = strconv.Atoi(part[i+1 : len(part)-1])
			part = part[:i]
		}
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[part]
		if idx >= 0 {
			l, ok := v.([]any)
			if !ok || idx >= len(l) {
				return nil
			}
			v = l[idx]
		}
	}
	return v
}

func jsonStr(v any, path string) string {
	switch x := jsonAt(v, path).(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(x)
	case json.Number:
		return x.String()
	case bool:
		return strconv.FormatBool(x)
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}

func parseAmountUnit(s string, paise bool) money.Amount {
	s = strings.ReplaceAll(strings.TrimSpace(s), ",", "")
	if s == "" {
		return 0
	}
	if paise {
		n, err := strconv.ParseInt(strings.TrimSuffix(s, ".0"), 10, 64)
		if err != nil {
			return 0
		}
		return money.Amount(n)
	}
	a, err := money.ParseAmount(s)
	if err != nil {
		// More than two decimals (e.g. 500.000): round to paise.
		d, err := money.ParseDec(s)
		if err != nil {
			return 0
		}
		return d.Round(money.UnitPaisa)
	}
	return a
}

func parseMappedCredits(c Connection, body []byte) ([]BankCredit, error) {
	v, err := decodeJSON(body)
	if err != nil {
		return nil, err
	}
	recs := []any{v}
	if p := c.Values["path_records"]; p != "" {
		l, ok := jsonAt(v, p).([]any)
		if !ok {
			return nil, fmt.Errorf("no list of credits at %s", p)
		}
		recs = l
	}
	paise := c.Values["amount_unit"] == "Paise"
	var out []BankCredit
	for _, r := range recs {
		out = append(out, BankCredit{
			Amount: parseAmountUnit(jsonStr(r, c.Values["path_amount"]), paise), Ref: jsonStr(r, c.Values["path_utr"]),
			VirtualAccount: jsonStr(r, c.Values["path_virtual_account"]), PayerAccount: jsonStr(r, c.Values["path_payer_account"]),
			PayerIFSC: jsonStr(r, c.Values["path_payer_ifsc"]), PayerName: jsonStr(r, c.Values["path_payer_name"]),
			Mode: jsonStr(r, c.Values["path_mode"]), Time: jsonStr(r, c.Values["path_time"]),
		})
	}
	return out, nil
}

// parseRazorpayCredit reads virtual_account.credited (bank transfers). Other
// events are acknowledged and ignored.
func parseRazorpayCredit(body []byte) ([]BankCredit, int, error) {
	v, err := decodeJSON(body)
	if err != nil {
		return nil, 0, err
	}
	if jsonStr(v, "event") != "virtual_account.credited" {
		return nil, 1, nil
	}
	bt := "payload.bank_transfer.entity"
	if jsonAt(v, bt) == nil {
		return nil, 1, nil // a UPI or other credit: not read yet
	}
	bc := BankCredit{
		Amount: parseAmountUnit(jsonStr(v, bt+".amount"), true), Ref: firstOf(jsonStr(v, bt+".bank_reference"), jsonStr(v, "payload.payment.entity.id")),
		Mode: jsonStr(v, bt+".mode"), PayerAccount: jsonStr(v, bt+".payer_bank_account.account_number"), PayerIFSC: jsonStr(v, bt+".payer_bank_account.ifsc"),
		PayerName: jsonStr(v, bt+".payer_bank_account.name"), VirtualAccount: jsonStr(v, "payload.virtual_account.entity.receivers[0].account_number"),
		UCC: jsonStr(v, "payload.virtual_account.entity.notes.ucc"), Time: jsonStr(v, "payload.payment.entity.created_at"),
	}
	return []BankCredit{bc}, 0, nil
}

// parseCashfreeCredit reads a PAYMENT_SUCCESS_WEBHOOK paid by a transfer to
// a virtual bank account.
func parseCashfreeCredit(body []byte) ([]BankCredit, int, error) {
	v, err := decodeJSON(body)
	if err != nil {
		return nil, 0, err
	}
	vb := "data.payment.payment_method.vba_transfer"
	if jsonStr(v, "type") != "PAYMENT_SUCCESS_WEBHOOK" || jsonAt(v, vb) == nil || !strings.EqualFold(jsonStr(v, "data.payment.payment_status"), "SUCCESS") {
		return nil, 1, nil
	}
	bc := BankCredit{
		Amount: parseAmountUnit(jsonStr(v, "data.payment.payment_amount"), false), Ref: firstOf(jsonStr(v, vb+".utr"), jsonStr(v, vb+".credit_ref_no")),
		PayerAccount: jsonStr(v, vb+".remitter_account"), PayerIFSC: jsonStr(v, vb+".remitter_ifsc"), PayerName: jsonStr(v, vb+".remitter_name"),
		VirtualAccount: jsonStr(v, vb+".vaccount_number"), Time: jsonStr(v, "data.payment.payment_time"), Mode: "BANK",
	}
	return []BankCredit{bc}, 0, nil
}

// creditWhen turns a provider's time (RFC 3339, "2006-01-02 15:04:05",
// "02-01-2006 15:04:05" or seconds since 1970) into an Indian date and time.
func creditWhen(s string, now time.Time) (string, string) {
	t := now
	s = strings.TrimSpace(s)
	if n, err := strconv.ParseInt(s, 10, 64); err == nil && n > 1e9 {
		t = time.Unix(n, 0)
	} else {
		for _, l := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "02-01-2006 15:04:05", "02/01/2006 15:04:05", "2006-01-02"} {
			if x, err := time.ParseInLocation(l, s, ist); err == nil {
				t = x
				break
			}
		}
	}
	t = t.In(ist)
	return t.Format("2006-01-02"), t.Format("15:04:05")
}
