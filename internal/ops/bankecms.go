package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"

	"ledgerline/internal/docs"
)

// ---- HDFC Bank e-CMS credit alerts (Client Web Service push) ----
//
// HDFC Bank's "Client Web Service Integration" for CMS transactions pushes
// each credit (and debit) on the firm's account to a web service the firm
// hosts, in near real time. From the bank's integration document:
//
//   - POST of {"GenericCorporateAlertRequest":[{...}]}, field names with
//     spaces: "Alert Sequence No" (unique per transaction, the duplicate
//     key), "Virtual Account", "Account number", "Debit Credit", "Amount",
//     "Remitter Name", "Remitter Account", "Remitter Bank", "Remitter IFSC"
//     (also seen as "IFSC Code"), "Cheque No", "User Reference Number" (UTR),
//     "Mnemonic Code" (NEFT/RTGS/IMPS/FUND TRANS), "Value Date",
//     "Transaction Description", "Transaction Date" (yyyy-mm-dd hh:mm).
//   - The reply is one JSON object, every value a string, within 2 seconds:
//     {"GenericCorporateAlertResponse":{"errorCode":"0","errorMessage":
//     "Success"|"Duplicate","domainReferenceNo":"<Alert Sequence No>"}}, or
//     errorCode "1" with "Technical Reject", which makes the bank push again
//     (it retries three times, then staff can re-push).
//   - The bank allows no user ID, password, token or extra encryption. The
//     only proofs are TLS (a CA-signed, domain-validated, non-EV
//     certificate on the firm's address) and the bank's fixed source
//     addresses, which the firm whitelists. Ledgerline accepts a push only
//     from an address on the connection's list (HDFC's published addresses
//     by default). Behind a reverse proxy, the proxy must set
//     X-Forwarded-For from the real peer address and drop any value the
//     caller sent.
//   - The address must not contain the bank's name, so it is
//     /hooks/<firm>/bank/ecms.
//
// Credits become statement credits exactly as the other live sources do
// (keyed by UTR, so the day's statement finds them already there). Debits,
// and credits to an account that isn't the firm's client money account,
// are acknowledged and not recorded.

const (
	connCreditsECMS = "credits-hdfc-ecms"
	cECMSAlert      = "ecmsalert" // Alert Sequence No -> ecmsAlert
)

// HDFCECMSAddresses are the bank's source addresses in its integration
// document, to be whitelisted.
var HDFCECMSAddresses = []string{"175.100.161.31", "175.100.161.48", "175.100.161.41", "175.100.161.32", "175.100.161.42", "175.100.161.43", "175.100.161.44",
	"175.100.161.45", "103.120.107.41", "103.120.107.48", "103.120.107.31", "103.120.107.42", "103.120.107.43"}

type ecmsAlert struct {
	Seq    string `json:"seq"`
	Credit string `json:"credit,omitempty"` // statement credit id, when one was made
	What   string `json:"what"`             // Credit, Debit ignored, Other account
	At     string `json:"at"`
}

func init() {
	creditProviders["ecms"] = connCreditsECMS
	ConnProviders = append(ConnProviders, ConnProvider{ID: connCreditsECMS, Name: "HDFC Bank e-CMS credit alerts", Group: "Money", Test: "none",
		Purpose: "HDFC Bank pushes every credit on the client money account as it lands (Client Web Service integration for CMS). Give the bank the address /hooks/<firm>/bank/ecms on the firm's own domain, with a CA-signed, domain-validated (not EV) certificate, full chain, as the bank asks; the bank sends no password or signature, so only its addresses below are accepted",
		Fields: []ConnField{
			{Key: "accounts", Label: "Client money account numbers at HDFC (comma-separated); credits to other accounts are ignored"},
			{Key: "va_prefix", Label: "Virtual account = this prefix + client code", Optional: true, Placeholder: "e.g. BALJ"},
			{Key: "addresses", Label: "Bank addresses allowed to push (empty: HDFC's published list)", Optional: true, Placeholder: strings.Join(HDFCECMSAddresses[:2], ", ") + ", …"},
			fNotes,
		}})
}

// checkECMSConn is run when the connection is saved.
func checkECMSConn(c Connection) error {
	if len(splitList(c.Values["accounts"])) == 0 {
		return invalid("give the client money account number the bank will report")
	}
	for _, a := range splitList(c.Values["addresses"]) {
		if net.ParseIP(a) == nil {
			return invalid("%q is not an IP address", a)
		}
	}
	return nil
}

func splitList(s string) []string {
	var out []string
	for _, x := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == ';' || r == '\n' }) {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

func ecmsReply(code, msg, ref string) string {
	b, _ := json.Marshal(map[string]any{"GenericCorporateAlertResponse": map[string]string{"errorCode": code, "errorMessage": msg, "domainReferenceNo": ref}})
	return string(b)
}

// ecmsHook handles one push. Problems the bank can fix by pushing again
// (Ledgerline busy, a storage error) answer "Technical Reject" with the
// error; a push from an address not on the list is refused outright.
func (o *Ops) ecmsHook(ctx context.Context, tenant, ip string, body []byte) (BankCreditResult, error) {
	res := BankCreditResult{}
	c, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connCreditsECMS)
	if err != nil || c.Status == "Not set" || len(splitList(c.Values["accounts"])) == 0 {
		return res, fmt.Errorf("%w: HDFC e-CMS is not set up in Settings → Connections", ErrForbidden)
	}
	allowed := splitList(c.Values["addresses"])
	if len(allowed) == 0 {
		allowed = HDFCECMSAddresses
	}
	if ip == "" || !contains(allowed, ip) {
		o.log(ctx, tenant, "bank", "bank.ecms.refused", ip, map[string]any{"why": "address not allowed"})
		return res, fmt.Errorf("%w: pushes are accepted only from the bank's addresses", ErrForbidden)
	}
	v, err := decodeJSON(body)
	if err != nil {
		res.Ack = ecmsReply("1", "Technical Reject", "")
		return res, invalid("%v", err)
	}
	recs, _ := jsonAt(v, "GenericCorporateAlertRequest").([]any)
	if len(recs) == 0 {
		if m, ok := jsonAt(v, "GenericCorporateAlertRequest").(map[string]any); ok {
			recs = []any{m}
		}
	}
	if len(recs) == 0 {
		res.Ack = ecmsReply("1", "Technical Reject", "")
		return res, invalid("no GenericCorporateAlertRequest in the message")
	}
	ours := map[string]bool{}
	for _, a := range splitList(c.Values["accounts"]) {
		ours[strings.TrimLeft(a, "0")] = true
	}
	get := func(r any, keys ...string) string {
		m, _ := r.(map[string]any)
		for _, k := range keys {
			for mk, mv := range m {
				if strings.EqualFold(strings.TrimSpace(mk), k) {
					if s := jsonStr(mv, ""); s != "" {
						return s
					}
				}
			}
		}
		return ""
	}
	ref, dups := "", 0
	var credits []BankCredit
	var seqs []string
	for _, r := range recs {
		seq := get(r, "Alert Sequence No")
		if ref == "" {
			ref = seq
		}
		if seq == "" {
			res.Ack = ecmsReply("1", "Technical Reject", "")
			return res, invalid("an alert has no Alert Sequence No")
		}
		res.Received++
		if _, _, err := docs.Get[ecmsAlert](ctx, o.Docs, tenant, cECMSAlert, seq); err == nil {
			dups++
			continue
		}
		what := "Credit"
		switch {
		case !strings.HasPrefix(strings.ToUpper(get(r, "Debit Credit")), "C"):
			what = "Debit ignored"
		case !ours[strings.TrimLeft(get(r, "Account number"), "0")]:
			what = "Other account"
		}
		if what != "Credit" {
			res.Ignored++
			_ = docs.Upsert(ctx, o.Docs, tenant, cECMSAlert, seq, ecmsAlert{Seq: seq, What: what, At: o.now().Format("2006-01-02T15:04:05")}, "bank")
			continue
		}
		mode := strings.ToUpper(get(r, "Mnemonic Code"))
		if mode == "FUND TRANS" {
			mode = "TRANSFER"
		}
		credits = append(credits, BankCredit{
			Ref:    firstOf(get(r, "User Reference Number"), seq),
			Amount: parseAmountUnit(get(r, "Amount"), false), VirtualAccount: get(r, "Virtual Account"), PayerAccount: get(r, "Remitter Account"),
			PayerIFSC: get(r, "Remitter IFSC", "IFSC Code"), PayerName: get(r, "Remitter Name"), Mode: mode,
			Time: firstOf(get(r, "Transaction Date"), get(r, "Value Date")),
		})
		seqs = append(seqs, seq)
	}
	if len(credits) > 0 {
		r2, err := o.recordCredits(ctx, tenant, c, "HDFC Bank e-CMS", credits)
		res.Added, res.Duplicate, res.Credits = r2.Added, r2.Duplicate, r2.Credits
		res.Ignored += r2.Ignored
		if err != nil {
			res.Ack = ecmsReply("1", "Technical Reject", ref)
			return res, err
		}
		for i, seq := range seqs {
			_ = docs.Upsert(ctx, o.Docs, tenant, cECMSAlert, seq, ecmsAlert{Seq: seq, Credit: credits[i].Ref, What: "Credit", At: o.now().Format("2006-01-02T15:04:05")}, "bank")
		}
	}
	res.Duplicate += dups
	msg := "Success"
	if dups == res.Received {
		msg = "Duplicate"
	}
	res.Ack = ecmsReply("0", msg, ref)
	return res, nil
}

// BankCreditHookFrom is BankCreditHook with the address the notice came
// from, which the HDFC e-CMS push is checked against.
func (o *Ops) BankCreditHookFrom(ctx context.Context, tenant, provider, ip string, h http.Header, body []byte) (BankCreditResult, error) {
	if creditProviders[provider] == connCreditsECMS {
		return o.ecmsHook(ctx, tenant, ip, body)
	}
	return o.BankCreditHook(ctx, tenant, provider, h, body)
}
