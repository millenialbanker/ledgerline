package ops

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html/template"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
)

// ---- the client's authorisation of a bank account change ----
//
// A change to a client's registered bank accounts (add, replace, make
// primary, deactivate; clientbanks.go) is made only on the client's own
// authority. The rules followed, as the repo holds them:
//
//   - SEBI Master Circular for Stock Brokers of 17 June 2025, para
//     36.8.1.2: the list of the clients' bank accounts "may be updated /
//     amended by proper communication" (clientbanks.go). "Proper
//     communication" is read as communication from the client: Ledgerline
//     does not take a bank change on a member of staff's word alone.
//   - Paras 28.2 and 29.1 (money only from and to the client's own
//     registered accounts): a wrongly added account is the route by which
//     a client's money is paid to someone else, so the authorisation is
//     kept with the request as evidence for the inspection.
//   - The client's registered mobile and e-mail are the ones in the KYC
//     record and the exchanges' UCC (the contacts the exchanges and SEBI
//     use for the client); a change to them is a change request of its
//     own, confirmed with a code sent to the new contact (portal.go).
//
// So a bank change is authorised in one of three ways:
//
//  1. The client asks for it in the client portal: the signed-in login is
//     the client's authority and nothing more is asked (Source Portal).
//  2. Staff raise it and the client confirms it: Ledgerline sends the
//     client's registered mobile and registered e-mail what exactly will
//     change (account numbers masked, bank and IFSC) with a one-time code
//     and a link. The client opens the link and confirms or refuses, or
//     signs in to the portal and enters the code (or refuses there). The
//     request waits as "Waiting for the client" and cannot be approved
//     until then; if the client does not answer in Settings → Days to wait
//     for the client (7 by default) it expires, and the client and the
//     maker are told.
//  3. Staff attach the client's signed letter or e-signed request, as a
//     document of kind "Client authorisation".
//
// Settings → Client authorisation of bank changes chooses whether 2, 3 or
// both are taken (both by default). Either way a checker other than the
// maker approves the change as before, and a new or replacement account
// still needs its bank proof and, where the firm asks for one, the penny
// drop. The safe readings taken, for the compliance officer to confirm:
//
//   - Only the client's own portal login confirms by code: a family login
//     that also sees the account cannot confirm for the account holder.
//   - Staff never enter a code for the client; the messages say so.
//   - The link page answers only on a button press (POST), so a mail
//     scanner that opens links does not confirm for the client.
//   - A refusal goes to the compliance officer as a work item: a bank
//     change the client did not ask for may be an attempted fraud.
//   - When the client's mobile or e-mail was changed in the last 30 days
//     the checker is warned: a contact change followed by a bank change is
//     the usual route of account takeover.

// Forms of a bank change's authorisation (ChangeAuth.Form).
const (
	AuthClient = "Client confirmation"
	AuthLetter = "Signed letter"
	AuthESign  = "E-signed request"
	AuthPortal = "Client's own portal request"
)

// Statuses of a change request for the client's part.
const (
	ChangeWaitClient = "Waiting for the client"
	ChangeRefused    = "Refused by the client"
	ChangeExpired    = "Expired"
)

// DocClientAuth is the document kind of a client's signed letter or
// e-signed request authorising a change.
const DocClientAuth = "Client authorisation"

// Settings → Client authorisation of bank changes (Settings.BankAuthForms).
const (
	BankAuthBoth    = "both"
	BankAuthConfirm = "confirm"
	BankAuthLetter  = "letter"
)

const (
	cBankAuthLink  = "bankauthlink"  // sha256(token): the client's confirmation links
	cBankAuthState = "bankauthstate" // the expiry tick's last run
)

// bankAuthMaxTries is how many wrong codes end the codes sent.
const bankAuthMaxTries = 5

// bankAuthForms is the forms the firm takes.
func (s Settings) bankAuthForms() string {
	switch s.BankAuthForms {
	case BankAuthConfirm, BankAuthLetter:
		return s.BankAuthForms
	}
	return BankAuthBoth
}

// bankAuthDays is how long a request waits for the client.
func (s Settings) bankAuthDays() int {
	if s.BankAuthDays > 0 {
		return int(s.BankAuthDays)
	}
	return 7
}

func checkBankAuthSettings(s *Settings) error {
	s.BankAuthForms = strings.ToLower(strings.TrimSpace(s.BankAuthForms))
	switch s.BankAuthForms {
	case "", BankAuthBoth, BankAuthConfirm, BankAuthLetter:
	default:
		return invalid("client authorisation of bank changes is both, confirm (code or link) or letter (signed letter or e-sign)")
	}
	if s.BankAuthDays < 0 || s.BankAuthDays > 30 {
		return invalid("days to wait for the client's confirmation must be 1 to 30 (0 for the default of 7)")
	}
	return nil
}

// ChangeAuth is the client's authorisation of a bank account change: how
// it was given, and the record of it.
type ChangeAuth struct {
	Form  string `json:"form"`  // AuthClient, AuthLetter, AuthESign or AuthPortal
	State string `json:"state"` // Waiting for the client, Confirmed, Refused, Expired, Attached, Own request
	// Shown is what the client was told will change, word for word.
	Shown   string     `json:"shown,omitempty"`
	Sent    []AuthSend `json:"sent,omitempty"`
	SentAt  string     `json:"sent_at,omitempty"`
	Sends   int        `json:"sends,omitempty"`
	Expires string     `json:"expires,omitempty"`
	Tries   int        `json:"tries,omitempty"`
	// Doc is the signed letter or e-signed request (kind Client
	// authorisation), with who attached it.
	Doc     string `json:"doc,omitempty"`
	DocBy   string `json:"doc_by,omitempty"`
	DocName string `json:"doc_name,omitempty"`
	// The client's answer: when, through which channel (mobile or e-mail),
	// the masked contact it was sent to, how (code in the portal, or the
	// link) and from where.
	Answer  string `json:"answer,omitempty"` // Confirmed, Refused
	At      string `json:"at,omitempty"`
	Channel string `json:"channel,omitempty"`
	To      string `json:"to,omitempty"`
	Via     string `json:"via,omitempty"`
	By      string `json:"by,omitempty"`
	From    string `json:"from,omitempty"`
	// Warning is for the checker (a recent change of mobile or e-mail).
	Warning string `json:"warning,omitempty"`
	Note    string `json:"note,omitempty"`
}

// AuthSend is one message that asked the client to confirm.
type AuthSend struct {
	Channel  string `json:"channel"` // mobile or e-mail
	To       string `json:"to"`      // masked
	Message  string `json:"message,omitempty"`
	Status   string `json:"status,omitempty"`
	Note     string `json:"note,omitempty"`
	CodeHash string `json:"code_hash,omitempty"`
	LinkID   string `json:"link_id,omitempty"`
}

// ChangeEvent is one step in a change request's history.
type ChangeEvent struct {
	At   string `json:"at"`
	By   string `json:"by"`
	What string `json:"what"`
}

func (r *ChangeRequest) event(at, by, what string) {
	r.History = append(r.History, ChangeEvent{At: at, By: by, What: what})
}

// public strips the codes' hashes.
func (a *ChangeAuth) public() *ChangeAuth {
	if a == nil {
		return nil
	}
	x := *a
	x.Sent = make([]AuthSend, len(a.Sent))
	for i, s := range a.Sent {
		s.CodeHash, s.LinkID = "", ""
		x.Sent[i] = s
	}
	return &x
}

// clientView is the request as the client's portal shows it: without the
// checker's warning or who attached the letter.
func (r ChangeRequest) clientView() ChangeRequest {
	r = r.public()
	if r.Auth != nil {
		r.Auth.Warning, r.Auth.DocBy, r.Auth.Doc = "", "", ""
	}
	return r
}

// bankAuthNeeded: a bank change not raised by the client in the portal.
func bankAuthNeeded(r ChangeRequest) bool { return r.Kind == "Bank account" && r.Source != "Portal" }

// changeAuthOK says whether a bank change may be approved: the portal
// request, the client's confirmation or the letter. A request with none
// cannot be approved.
func changeAuthOK(r ChangeRequest) error {
	if !bankAuthNeeded(r) {
		return nil
	}
	a := r.Auth
	switch {
	case a == nil:
		return fmt.Errorf("%w: %s has no client authorisation: send it to the client to confirm, or attach the client's signed letter or e-signed request", ErrState, r.ID)
	case a.Form == AuthClient && a.Answer == "Confirmed":
		return nil
	case (a.Form == AuthLetter || a.Form == AuthESign) && a.Doc != "":
		return nil
	case a.Form == AuthClient:
		return fmt.Errorf("%w: %s is waiting for the client to confirm it (sent to %s); it can be approved once the client confirms, or a signed letter is attached", ErrState, r.ID, a.sentTo())
	}
	return fmt.Errorf("%w: %s has no client authorisation", ErrState, r.ID)
}

func (a *ChangeAuth) sentTo() string {
	var to []string
	for _, s := range a.Sent {
		to = append(to, s.Channel+" "+s.To)
	}
	if len(to) == 0 {
		return "no contact"
	}
	return strings.Join(to, " and ")
}

// bankChangeShown is what the client is told will change, with account
// numbers masked.
func bankChangeShown(c Client, r ChangeRequest) string {
	n := r.New
	acct := func(bank, account, ifsc string) string {
		return strings.TrimSpace(fmt.Sprintf("%s account %s (IFSC %s)", strings.TrimSpace(bank), MaskAccount(account), strings.ToUpper(strings.TrimSpace(ifsc))))
	}
	p := primaryBank(c)
	cur := acct(p.Bank, p.Account, p.IFSC)
	switch bankChangeAction(r) {
	case BankAdd:
		s := "Add " + acct(n["bank"], n["account"], n["ifsc"]) + " to your registered bank accounts"
		if yes(n["primary"]) {
			s += ", as your primary account (payouts go to it) in place of " + cur
		}
		return s
	case BankReplace:
		return "Replace your primary bank account " + cur + " with " + acct(n["bank"], n["account"], n["ifsc"]) + "; the old account will no longer be registered"
	case BankDeactivate:
		return "Remove " + acct(n["bank"], n["account"], n["ifsc"]) + " from your registered bank accounts"
	case BankPrimary:
		return "Make " + acct(n["bank"], n["account"], n["ifsc"]) + " your primary account (payouts go to it) in place of " + cur
	}
	return "Change your registered bank accounts"
}

// prepBankAuth sets how a bank change is authorised, before it is stored:
// the portal request, the letter attached, or the client's confirmation
// (which the request then waits for). values["auth"] chooses (client,
// letter or esign); values["auth_doc"] (or, for a removal or make-primary,
// the request's document) is the letter.
func (o *Ops) prepBankAuth(ctx context.Context, tenant, actor string, c Client, r *ChangeRequest, doc string) error {
	now := o.now()
	if r.Source == "Portal" {
		r.Auth = &ChangeAuth{Form: AuthPortal, State: "Own request", At: now.Format(time.RFC3339), By: actor}
		return nil
	}
	set, _ := o.Settings(ctx, tenant)
	forms := set.bankAuthForms()
	want := strings.ToLower(strings.TrimSpace(r.New["auth"]))
	authDoc := strings.TrimSpace(r.New["auth_doc"])
	delete(r.New, "auth")
	delete(r.New, "auth_doc")
	if a := bankChangeAction(*r); authDoc == "" && (a == BankDeactivate || a == BankPrimary) {
		authDoc = doc
	}
	switch want {
	case "", "client", "confirm", "letter", "esign", "e-sign":
	default:
		return invalid("the client authorises a bank change by confirming it (code or link) or by a signed letter or e-signed request")
	}
	letterForm := AuthLetter
	if want == "esign" || want == "e-sign" {
		letterForm = AuthESign
	}
	if authDoc != "" && want != "client" && want != "confirm" || want == "letter" || want == "esign" || want == "e-sign" {
		if forms == BankAuthConfirm {
			return invalid("the firm takes a bank account change only when the client confirms it by code or link (Settings → Client authorisation of bank changes): send it to the client instead of attaching a letter")
		}
		d, ok := o.clientDoc(ctx, tenant, c.UCC, authDoc)
		if !ok || d.Kind != DocClientAuth {
			return invalid("attach the client's signed letter or e-signed request as a document of kind %s", DocClientAuth)
		}
		r.Auth = &ChangeAuth{Form: letterForm, State: "Attached", Doc: d.ID, DocBy: actor, DocName: d.Name, Shown: bankChangeShown(c, *r), At: now.Format(time.RFC3339), Warning: o.recentContactChange(ctx, tenant, c.UCC)}
		return nil
	}
	if forms == BankAuthLetter {
		return invalid("the firm takes a bank account change raised by staff only with the client's signed letter or e-signed request (Settings → Client authorisation of bank changes): attach it as a %s document", DocClientAuth)
	}
	if len(authContacts(c)) == 0 {
		if forms == BankAuthConfirm {
			return invalid("%s has no valid registered mobile or e-mail to confirm the change from: correct the client's contact details first", c.UCC)
		}
		return invalid("%s has no valid registered mobile or e-mail to confirm the change from: attach the client's signed letter or e-signed request instead", c.UCC)
	}
	r.Auth = &ChangeAuth{Form: AuthClient, State: ChangeWaitClient, Shown: bankChangeShown(c, *r),
		Expires: now.Add(time.Duration(set.bankAuthDays()) * 24 * time.Hour).Format(time.RFC3339), Warning: o.recentContactChange(ctx, tenant, c.UCC)}
	r.Status = ChangeWaitClient
	return nil
}

func (o *Ops) clientDoc(ctx context.Context, tenant, ucc, id string) (ClientDoc, bool) {
	if strings.TrimSpace(id) == "" {
		return ClientDoc{}, false
	}
	d, _, err := docs.Get[ClientDoc](ctx, o.Docs, tenant, cClientDoc, strings.ToUpper(ucc)+"|"+id)
	return d, err == nil
}

// authContacts are the client's registered contacts the confirmation goes
// to: the mobile (SMS) and the e-mail, where valid.
func authContacts(c Client) [][2]string {
	var out [][2]string
	if reMobile.MatchString(strings.TrimSpace(c.Mobile)) {
		out = append(out, [2]string{"mobile", strings.TrimSpace(c.Mobile)})
	}
	if e := strings.TrimSpace(c.Email); reEmail.MatchString(e) && !strings.HasSuffix(e, ".invalid") {
		out = append(out, [2]string{"e-mail", e})
	}
	return out
}

func maskContact(channel, to string) string {
	if channel == "mobile" {
		return maskMobile(to)
	}
	return maskEmail(to)
}

// recentContactChange warns the checker of a change of the client's mobile
// or e-mail approved in the last 30 days.
func (o *Ops) recentContactChange(ctx context.Context, tenant, ucc string) string {
	rs, _ := docs.List[ChangeRequest](ctx, o.Docs, tenant, cChange, "")
	since := o.now().AddDate(0, 0, -30).Format(time.RFC3339)
	var w []string
	for _, x := range rs {
		if x.UCC == ucc && (x.Kind == "Email" || x.Kind == "Mobile") && x.Status == "Approved" && x.Decided >= since {
			w = append(w, fmt.Sprintf("%s changed on %s (%s)", strings.ToLower(x.Kind), dateOnly(x.Decided), x.ID))
		}
	}
	if len(w) == 0 {
		return ""
	}
	sort.Strings(w)
	return "The client's " + strings.Join(w, " and ") + ": check that the client really asked for this bank change."
}

// bankAuthSend is a code and link to send, before they are sent.
type bankAuthSend struct {
	channel, to, code, token string
}

// armBankAuth makes a fresh code and link for each of the client's
// registered contacts; their hashes go into the request (the codes and
// links themselves are never stored). Earlier codes and links stop
// working.
func (o *Ops) armBankAuth(ctx context.Context, tenant, actor string, c Client, r *ChangeRequest) ([]bankAuthSend, error) {
	var out []bankAuthSend
	r.Auth.Sent, r.Auth.Tries = nil, 0
	for _, ct := range authContacts(c) {
		tok, err := newToken()
		if err != nil {
			return nil, err
		}
		code := otpCode()
		id := bankAuthTokID(tok)
		if err := docs.Upsert(ctx, o.Docs, tenant, cBankAuthLink, id, bankAuthLink{Change: r.ID, Channel: ct[0], Created: o.now().Format(time.RFC3339)}, actor); err != nil {
			return nil, err
		}
		r.Auth.Sent = append(r.Auth.Sent, AuthSend{Channel: ct[0], To: maskContact(ct[0], ct[1]), Status: "To send", CodeHash: hashOTP(r.ID+"|"+ct[0], code), LinkID: id})
		out = append(out, bankAuthSend{channel: ct[0], to: ct[1], code: code, token: tok})
	}
	r.Auth.Sends++
	r.Auth.SentAt = o.now().Format(time.RFC3339)
	return out, nil
}

type bankAuthLink struct {
	Change  string `json:"change"`
	Channel string `json:"channel"`
	Created string `json:"created"`
}

func bankAuthTokID(tok string) string {
	s := sha256.Sum256([]byte("bankauth|" + tok))
	return hex.EncodeToString(s[:16])
}

// publicURLKey carries the address browsers reach Ledgerline at, for the
// client's link (set by the API from LEDGERLINE_PUBLIC_URL or the
// request's host).
type publicURLKey struct{}

// WithPublicURL puts Ledgerline's public address in ctx.
func WithPublicURL(ctx context.Context, base string) context.Context {
	return context.WithValue(ctx, publicURLKey{}, strings.TrimRight(base, "/"))
}

func publicURL(ctx context.Context) string {
	if b, _ := ctx.Value(publicURLKey{}).(string); b != "" {
		return b
	}
	return strings.TrimRight(os.Getenv("LEDGERLINE_PUBLIC_URL"), "/")
}

// BankAuthPath is the link's path under the public address.
func BankAuthPath(tenant, token string) string {
	return "/bank-confirm/" + url.PathEscape(tenant) + "/" + token
}

// sendBankAuth sends the codes and links to the client and records what
// was sent. Returns the dev values (codes and links) for a development
// server and the tests; the API drops them otherwise.
func (o *Ops) sendBankAuth(ctx context.Context, tenant, actor string, c Client, r ChangeRequest, sends []bankAuthSend) map[string]string {
	set, _ := o.Settings(ctx, tenant)
	base := publicURL(ctx)
	until := r.Auth.Expires
	if t, err := time.Parse(time.RFC3339, until); err == nil {
		until = t.In(ist).Format("02 Jan 2006 15:04")
	}
	dev := map[string]string{}
	status := map[string][2]string{}
	msgID := map[string]string{}
	for _, s := range sends {
		link := ""
		if base != "" {
			link = base + BankAuthPath(tenant, s.token)
		}
		dev[s.channel+"_code"], dev[s.channel+"_link"], dev[s.channel+"_token"] = s.code, link, s.token
		body := func(code, link string) (string, string) {
			if s.channel == "mobile" {
				how := "enter code " + code + " in the client portal"
				if link != "" {
					how = "open " + link + " or " + how
				}
				return "", fmt.Sprintf("%s: bank account change asked for %s: %s. To confirm or refuse, %s. Valid till %s. We never ask for this code. Not asked by you? Refuse and call us. -%s",
					set.Firm, c.UCC, r.Auth.Shown, how, until, set.Firm)
			}
			how := "Sign in to the client portal and enter this code under Profile: " + code
			if link != "" {
				how = "To confirm or refuse, open this link:\n\n" + link + "\n\nOr sign in to the client portal and enter this code under Profile: " + code
			}
			return "Confirm the change to your bank accounts (" + c.UCC + ")", fmt.Sprintf("Dear %s,\n\n%s has been asked to make this change to the bank accounts registered for your trading account %s:\n\n%s\n\nRequest %s, raised by our office on %s. It is made only if you confirm it.\n\n%s\n\nThe code and the link work until %s. Do not share the code with anyone, including our staff: we never ask for it.\n\nIf you did not ask for this change, refuse it and tell us at once%s.\n\n%s",
				c.Name, set.Firm, c.UCC, r.Auth.Shown, r.ID, dateOnly(r.At), how, until, map[bool]string{true: " at " + set.ComplianceEmail, false: ""}[set.ComplianceEmail != ""], set.Firm)
		}
		subj, sent := body(s.code, link)
		keptLink := ""
		if link != "" {
			keptLink = base + BankAuthPath(tenant, s.token[:6]+"…")
		}
		_, kept := body("••••••", keptLink)
		ch := ChEmail
		if s.channel == "mobile" {
			ch = ChSMS
		}
		m := o.sendDirectMsg(ctx, tenant, actor, ch, s.to, c.UCC, "change.bank_confirm", subj, sent, kept)
		status[s.channel], msgID[s.channel] = [2]string{m.Status, m.Note}, m.ID
	}
	now := o.now().Format(time.RFC3339)
	_, _ = docs.Update(ctx, o.Docs, tenant, cChange, r.ID, actor, func(x *ChangeRequest) error {
		if x.Auth == nil {
			return nil
		}
		var to []string
		for i, s := range x.Auth.Sent {
			if st, ok := status[s.Channel]; ok {
				x.Auth.Sent[i].Status, x.Auth.Sent[i].Note, x.Auth.Sent[i].Message = st[0], st[1], msgID[s.Channel]
			}
			to = append(to, s.Channel+" "+s.To)
		}
		what := "Sent to the client to confirm"
		if x.Auth.Sends > 1 {
			what = "Sent to the client again (new code and link)"
		}
		x.event(now, actor, what+": "+strings.Join(to, ", ")+"; valid till "+until)
		return nil
	})
	o.log(ctx, tenant, actor, "change.client_asked", r.ID, map[string]any{"ucc": c.UCC, "to": r.Auth.sentTo(), "expires": r.Auth.Expires})
	return dev
}

// sendDirectMsg is sendDirectAs returning the message kept. An e-mail goes
// through the firm's mail server (Connections), so the checker sees whether
// it really went; without one it is "Not sent" (simulated on a
// development firm).
func (o *Ops) sendDirectMsg(ctx context.Context, tenant, actor, channel, to, ucc, event, subject, sent, kept string) Message {
	n, err := o.next(ctx, tenant, "message", actor)
	if err != nil {
		return Message{Status: "Failed", Note: err.Error()}
	}
	m := Message{ID: fmt.Sprintf("MSG-%07d", n), At: o.now().Format(time.RFC3339), Event: event, Channel: channel, Category: "Transactional", UCC: ucc, To: to, Subject: subject, Body: sent, Status: "Sent"}
	if channel == ChEmail {
		if ml := o.mailerFor(ctx, tenant); ml != nil {
			set, _ := o.Settings(ctx, tenant)
			if _, err := ml.Send(ctx, Mail{From: mailFrom(ml, set), To: to, Subject: subject, Text: sent}); err != nil {
				m.Status, m.Note = "Failed", err.Error()
			}
		} else {
			o.notConnected(ctx, tenant, &m, "no mail server connected (Settings → Connections)")
		}
		m.TriedAt = m.At
	}
	if channel == ChSMS {
		if c, ok := o.msgConn(ctx, tenant, "sms"); ok {
			m.DLT = c.Values["otp_template"]
		}
		if id, held := o.mnrlHeld(ctx, tenant, to); held {
			m.Status, m.Note = "Held", mnrlHeldNote(id) // on DoT's revocation list (mnrl.go)
		} else {
			o.transmit(ctx, tenant, &m, "", nil)
			m.TriedAt = m.At
		}
	}
	m.Body = kept
	_ = docs.Upsert(ctx, o.Docs, tenant, cMessage, m.ID, m, actor)
	return m
}

// ---- the client's answer ----

// ClientAnswerChange is the client, signed in to the portal, confirming a
// bank change staff raised with the code sent to them, or refusing it (no
// code needed to refuse). Only the account holder's own login answers.
func (o *Ops) ClientAnswerChange(ctx context.Context, tenant, actor, id, code string, confirm bool) (ChangeRequest, error) {
	accts, err := o.PortalAccounts(ctx, tenant, actor)
	if err != nil {
		return ChangeRequest{}, err
	}
	r, _, err := docs.Get[ChangeRequest](ctx, o.Docs, tenant, cChange, id)
	if err != nil || !contains(accts, r.UCC) {
		return ChangeRequest{}, ErrNotFound
	}
	if len(accts) == 0 || accts[0] != r.UCC {
		return r.public(), fmt.Errorf("%w: only %s's own login can answer a change to %s's bank accounts", ErrForbidden, r.UCC, r.UCC)
	}
	if !confirm {
		return o.recordBankAuth(ctx, tenant, actor, id, "", "", "client portal", "", false, nil)
	}
	code = strings.TrimSpace(code)
	wrong := false
	check := func(x *ChangeRequest) (AuthSend, error) {
		if x.Auth.Tries >= bankAuthMaxTries {
			return AuthSend{}, invalid("too many wrong codes: ask the firm to send a new code")
		}
		for _, s := range x.Auth.Sent {
			if s.CodeHash != "" && code != "" && hmac.Equal([]byte(hashOTP(x.ID+"|"+s.Channel, code)), []byte(s.CodeHash)) {
				return s, nil
			}
		}
		x.Auth.Tries++
		wrong = true
		return AuthSend{}, nil
	}
	out, err := o.recordBankAuth(ctx, tenant, actor, id, "", "", "code in the client portal", "", true, check)
	if err == nil && wrong {
		left := bankAuthMaxTries - out.Auth.Tries
		if left <= 0 {
			return out, invalid("the code is not right, and that was the last try: ask the firm to send a new code")
		}
		return out, invalid("the code is not right (%d tries left)", left)
	}
	return out, err
}

// recordBankAuth records the client's answer on a request waiting for
// them. check, for a code, finds the message the code was sent in (or
// counts a wrong try); for a link, channel and to say which.
func (o *Ops) recordBankAuth(ctx context.Context, tenant, by, id, channel, to, via, from string, confirm bool, check func(*ChangeRequest) (AuthSend, error)) (ChangeRequest, error) {
	now := o.now()
	at := now.Format(time.RFC3339)
	expired, counted := false, false
	r, err := docs.Update(ctx, o.Docs, tenant, cChange, id, by, func(x *ChangeRequest) error {
		if x.Status != ChangeWaitClient || x.Auth == nil || x.Auth.Form != AuthClient {
			return fmt.Errorf("%w: %s is not waiting for the client's answer (%s)", ErrState, id, strings.ToLower(x.Status))
		}
		if at > x.Auth.Expires {
			expired = true
			return fmt.Errorf("%w: expired", ErrState)
		}
		if check != nil {
			s, err := check(x)
			if err != nil {
				return err
			}
			if s.Channel == "" {
				counted = true
				return nil // a wrong code: the try is kept
			}
			channel, to = s.Channel, s.To
		}
		a := x.Auth
		a.Answer, a.At, a.Channel, a.To, a.Via, a.By, a.From = map[bool]string{true: "Confirmed", false: "Refused"}[confirm], at, channel, to, via, by, from
		for i := range a.Sent {
			a.Sent[i].CodeHash, a.Sent[i].LinkID = "", "" // every code and link stops working
		}
		sent := ""
		if channel != "" {
			sent = " (sent to " + channel + " " + to + ")"
		}
		if confirm {
			a.State, x.Status = "Confirmed", "Pending"
			x.event(at, by, "Confirmed by the client by "+via+sent+"; waiting for a checker")
		} else {
			a.State, x.Status, x.Reason, x.Decided = "Refused", ChangeRefused, "Refused by the client", at
			x.event(at, by, "Refused by the client by "+via+sent+"; closed")
		}
		return nil
	})
	if expired {
		o.expireBankAuth(ctx, tenant, id)
		r, _, _ = docs.Get[ChangeRequest](ctx, o.Docs, tenant, cChange, id)
		return r.public(), invalid("the request has expired; nothing was changed")
	}
	if err != nil || counted {
		return r.public(), err
	}
	if confirm {
		o.log(ctx, tenant, by, "change.client_confirmed", id, map[string]any{"ucc": r.UCC, "channel": channel, "to": to, "via": via, "from": from})
	} else {
		o.log(ctx, tenant, by, "change.client_refused", id, map[string]any{"ucc": r.UCC, "channel": channel, "to": to, "via": via, "from": from})
		o.raiseWork(ctx, tenant, "bankauth", id+":refused", fmt.Sprintf("%s refused a bank account change raised by %s", r.UCC, r.By),
			fmt.Sprintf("%s (%s): the client refused %s, raised by %s on %s. Find out with the client who asked for it: a bank change the client did not ask for may be an attempted fraud.", id, r.Name, lowerFirst(r.Auth.Shown), r.By, dateOnly(r.At)),
			r.UCC, RoleCompliance, "changes", "")
	}
	return r.public(), nil
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// ---- the client's link ----

// BankAuthPage is what the client's link shows.
type BankAuthPage struct {
	Firm   string
	Name   string
	UCC    string
	ID     string
	Shown  string
	Raised string
	Until  string
	Answer string // set once answered
	Error  string
}

// OpenBankAuthLink looks up a client's link.
func (o *Ops) OpenBankAuthLink(ctx context.Context, tenant, token string) (BankAuthPage, ChangeRequest, bankAuthLink, error) {
	set, _ := o.Settings(ctx, tenant)
	p := BankAuthPage{Firm: set.Firm}
	var r ChangeRequest
	if len(token) < 16 {
		p.Error = "This link is not valid."
		return p, r, bankAuthLink{}, ErrNotFound
	}
	lk, _, err := docs.Get[bankAuthLink](ctx, o.Docs, tenant, cBankAuthLink, bankAuthTokID(token))
	if err == nil {
		r, _, err = docs.Get[ChangeRequest](ctx, o.Docs, tenant, cChange, lk.Change)
	}
	if err != nil || r.Auth == nil {
		p.Error = "This link is not valid."
		return p, r, lk, ErrNotFound
	}
	p.UCC, p.ID, p.Shown, p.Raised = r.UCC, r.ID, r.Auth.Shown, dateOnly(r.At)
	if c, err := o.Client(ctx, tenant, r.UCC); err == nil {
		p.Name = strings.Fields(c.Name + " x")[0]
	}
	if t, err := time.Parse(time.RFC3339, r.Auth.Expires); err == nil {
		p.Until = t.In(ist).Format("02 Jan 2006 15:04")
	}
	live := false
	for _, s := range r.Auth.Sent {
		live = live || (s.LinkID != "" && s.LinkID == bankAuthTokID(token))
	}
	switch {
	case r.Auth.Answer == "Confirmed" && r.Auth.Via == "link" && r.Auth.Channel == lk.Channel:
		p.Answer = "Confirmed"
	case r.Auth.Answer == "Refused" && r.Auth.Via == "link" && r.Auth.Channel == lk.Channel:
		p.Answer = "Refused"
	case r.Auth.Answer != "":
		p.Error = "This request has already been answered. If that was not you, contact us at once."
		return p, r, lk, ErrState
	case r.Status == ChangeExpired || r.Status == ChangeWaitClient && o.now().Format(time.RFC3339) > r.Auth.Expires:
		p.Error = "This request has expired and nothing was changed. If you still want the change, ask us again."
		return p, r, lk, ErrState
	case r.Status != ChangeWaitClient:
		p.Error = "This request is closed."
		return p, r, lk, ErrState
	case !live:
		p.Error = "This link is no longer valid: a newer message was sent for this request. Use the link in the latest message."
		return p, r, lk, ErrState
	}
	return p, r, lk, nil
}

// AnswerBankAuthLink records the answer given on the link (POST only, so
// link scanners that fetch the page do not answer for the client). from
// is the network address the answer came from, kept in the record.
func (o *Ops) AnswerBankAuthLink(ctx context.Context, tenant, token string, confirm bool, from string) (BankAuthPage, error) {
	p, r, lk, err := o.OpenBankAuthLink(ctx, tenant, token)
	if err != nil || p.Answer != "" {
		if p.Answer != "" {
			p.Error, p.Answer = "This link has already been used.", ""
			return p, fmt.Errorf("%w: the link was used", ErrState)
		}
		return p, err
	}
	to := ""
	for _, s := range r.Auth.Sent {
		if s.Channel == lk.Channel {
			to = s.To
		}
	}
	if _, err := o.recordBankAuth(ctx, tenant, "client:"+r.UCC, r.ID, lk.Channel, to, "link", trim(from, 80), confirm, nil); err != nil {
		p.Error = "This request could not be answered: " + strings.TrimPrefix(err.Error(), ErrState.Error()+": ")
		return p, err
	}
	p.Answer = map[bool]string{true: "Confirmed", false: "Refused"}[confirm]
	return p, nil
}

// BankAuthHTML renders the link's page: the change and two buttons.
var BankAuthHTML = template.Must(template.New("bankauth").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Bank account change · {{.Firm}}</title>
<style>body{font:16px/1.5 system-ui,-apple-system,Segoe UI,sans-serif;background:#f3f4f1;color:#1d2420;margin:0;padding:24px 16px}main{max-width:520px;margin:0 auto;background:#fff;border:1px solid #d9ddd6;border-radius:12px;padding:24px}h1{font-size:20px;margin:0 0 12px}p{margin:0 0 12px}.chg{background:#f3f4f1;border-radius:8px;padding:12px;font-weight:600}.b{display:flex;gap:10px;flex-wrap:wrap;margin-top:18px}button{font:inherit;padding:10px 16px;border-radius:8px;border:1px solid #9aa39b;background:#fff;color:#1d2420;cursor:pointer}button.y{background:#1f6b45;border-color:#1f6b45;color:#fff}button.n{border-color:#a32020;color:#a32020}small{color:#5b655e}@media (prefers-color-scheme:dark){body{background:#151a17;color:#e4e8e3}main{background:#1d2420;border-color:#333b35}.chg{background:#151a17}button{background:#1d2420;color:#e4e8e3}small{color:#a5ada6}}</style></head><body><main>
{{if .Error}}<h1>{{.Firm}}</h1><p>{{.Error}}</p>
{{else if eq .Answer "Confirmed"}}<h1>Thank you{{if .Name}}, {{.Name}}{{end}}</h1><p>You confirmed request {{.ID}}. Our office checks it and then makes the change; you will be told when it is done.</p>
{{else if eq .Answer "Refused"}}<h1>Noted{{if .Name}}, {{.Name}}{{end}}</h1><p>You refused request {{.ID}}. Nothing is changed. Our compliance officer will look into who asked for it. If you did not ask for it, please also call us.</p>
{{else}}<h1>Confirm a change to your bank accounts</h1>
<p>Dear {{.Name}}, {{.Firm}} has been asked to make this change for your trading account {{.UCC}} (request {{.ID}}, raised on {{.Raised}}):</p>
<p class="chg">{{.Shown}}</p>
<p>It is made only if you confirm it. This link works until {{.Until}}.</p>
<form method="post" class="b"><button class="y" name="answer" value="confirm">Yes, I asked for this change</button><button class="n" name="answer" value="refuse">No, I did not ask for it</button></form>
<p style="margin-top:16px"><small>Our staff will never ask you for the code in this message or to press a button for them.</small></p>
{{end}}</main></body></html>`))

// ---- staff: send again, or attach the letter ----

// ChangeAuthAction lets staff send a waiting bank change to the client
// again (a new code and link; the earlier ones stop working), or attach the
// client's signed letter or e-signed request instead (form letter or
// esign). A request raised before client authorisation was needed (no
// record) can be sent or given a letter the same way.
func (o *Ops) ChangeAuthAction(ctx context.Context, tenant, actor, id, action, doc, form string) (ChangeRequest, error) {
	if err := o.require(ctx, tenant, actor, "Asking for a client's authorisation", RoleMaker, RoleChecker, RoleCompliance, RoleBranch, RoleAP); err != nil {
		return ChangeRequest{}, err
	}
	r, _, err := docs.Get[ChangeRequest](ctx, o.Docs, tenant, cChange, id)
	if err != nil {
		return r, err
	}
	if !bankAuthNeeded(r) {
		return r.public(), invalid("%s needs no client authorisation here", id)
	}
	waiting := r.Status == ChangeWaitClient
	legacy := r.Status == "Pending" && r.Auth == nil
	if !waiting && !legacy {
		return r.public(), fmt.Errorf("%w: %s is %s", ErrState, id, strings.ToLower(r.Status))
	}
	if waiting && o.now().Format(time.RFC3339) > r.Auth.Expires {
		o.expireBankAuth(ctx, tenant, id)
		return r.public(), invalid("%s has expired; raise the change again", id)
	}
	c, err := o.Client(ctx, tenant, r.UCC)
	if err != nil {
		return r.public(), err
	}
	set, _ := o.Settings(ctx, tenant)
	at := o.now().Format(time.RFC3339)
	switch action {
	case "send":
		if set.bankAuthForms() == BankAuthLetter {
			return r.public(), invalid("the firm takes only the client's signed letter or e-signed request (Settings → Client authorisation of bank changes)")
		}
		if len(authContacts(c)) == 0 {
			return r.public(), invalid("%s has no valid registered mobile or e-mail", c.UCC)
		}
		if waiting && r.Auth.Sends >= 5 {
			return r.public(), invalid("%s has been sent to the client %d times; call the client, or attach the client's signed letter", id, r.Auth.Sends)
		}
		var sends []bankAuthSend
		r, err = docs.Update(ctx, o.Docs, tenant, cChange, id, actor, func(x *ChangeRequest) error {
			if x.Status != ChangeWaitClient && !(x.Status == "Pending" && x.Auth == nil) {
				return fmt.Errorf("%w: %s is %s", ErrState, id, strings.ToLower(x.Status))
			}
			if x.Auth == nil {
				x.Auth = &ChangeAuth{Form: AuthClient, State: ChangeWaitClient, Shown: bankChangeShown(c, *x), Warning: o.recentContactChange(ctx, tenant, c.UCC),
					Expires: o.now().Add(time.Duration(set.bankAuthDays()) * 24 * time.Hour).Format(time.RFC3339)}
				x.Status = ChangeWaitClient
			}
			var err error
			sends, err = o.armBankAuth(ctx, tenant, actor, c, x)
			return err
		})
		if err != nil {
			return r.public(), err
		}
		out := r.public()
		out.DevAuth = o.sendBankAuth(ctx, tenant, actor, c, r, sends)
		r, _, _ = docs.Get[ChangeRequest](ctx, o.Docs, tenant, cChange, id)
		dev := out.DevAuth
		out = r.public()
		out.DevAuth = dev
		return out, nil
	case "letter":
		if set.bankAuthForms() == BankAuthConfirm {
			return r.public(), invalid("the firm takes a bank change only when the client confirms it by code or link (Settings → Client authorisation of bank changes)")
		}
		d, ok := o.clientDoc(ctx, tenant, r.UCC, doc)
		if !ok || d.Kind != DocClientAuth {
			return r.public(), invalid("attach the client's signed letter or e-signed request as a document of kind %s", DocClientAuth)
		}
		f := AuthLetter
		if strings.EqualFold(form, "esign") || strings.EqualFold(form, "e-sign") {
			f = AuthESign
		}
		r, err = docs.Update(ctx, o.Docs, tenant, cChange, id, actor, func(x *ChangeRequest) error {
			if x.Status != ChangeWaitClient && !(x.Status == "Pending" && x.Auth == nil) {
				return fmt.Errorf("%w: %s is %s", ErrState, id, strings.ToLower(x.Status))
			}
			prev := ""
			if x.Auth != nil && len(x.Auth.Sent) > 0 {
				prev = "; the code and link sent to the client stop working"
			}
			x.Auth = &ChangeAuth{Form: f, State: "Attached", Doc: d.ID, DocBy: actor, DocName: d.Name, Shown: bankChangeShown(c, *x), At: at, Warning: o.recentContactChange(ctx, tenant, c.UCC)}
			x.Status = "Pending"
			x.event(at, actor, strings.ToLower(f[:1])+f[1:]+" attached ("+d.ID+")"+prev+"; waiting for a checker")
			return nil
		})
		if err == nil {
			o.log(ctx, tenant, actor, "change.client_letter", id, map[string]any{"ucc": r.UCC, "doc": d.ID, "form": f})
		}
		return r.public(), err
	}
	return r.public(), invalid("send the request to the client again, or attach the client's letter")
}

// ---- expiry ----

// bankAuthTick expires requests the client did not answer in time, at
// most once every 15 minutes.
func (o *Ops) bankAuthTick(ctx context.Context, tenant string, now time.Time) {
	type state struct {
		Last string `json:"last"`
	}
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cBankAuthState, "tick", state{})
	if t, err := time.Parse(time.RFC3339, st.Last); err == nil && now.Sub(t) < 15*time.Minute && !now.Before(t) {
		return
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cBankAuthState, "tick", state{Last: now.Format(time.RFC3339)}, SystemActor)
	o.ExpireBankAuths(ctx, tenant)
}

// ExpireBankAuths expires every bank change waiting for the client past
// its time, and returns how many.
func (o *Ops) ExpireBankAuths(ctx context.Context, tenant string) int {
	rs, _ := docs.List[ChangeRequest](ctx, o.Docs, tenant, cChange, "")
	now := o.now().Format(time.RFC3339)
	n := 0
	for _, r := range rs {
		if r.Status == ChangeWaitClient && r.Auth != nil && now > r.Auth.Expires {
			if o.expireBankAuth(ctx, tenant, r.ID) {
				n++
			}
		}
	}
	return n
}

// expireBankAuth closes one request as expired and tells the client and
// the maker.
func (o *Ops) expireBankAuth(ctx context.Context, tenant, id string) bool {
	now := o.now().Format(time.RFC3339)
	done := false
	r, err := docs.Update(ctx, o.Docs, tenant, cChange, id, SystemActor, func(x *ChangeRequest) error {
		if x.Status != ChangeWaitClient || x.Auth == nil || now <= x.Auth.Expires {
			return nil
		}
		x.Status, x.Reason, x.Decided = ChangeExpired, "Not confirmed by the client in time", now
		x.Auth.State = "Expired"
		for i := range x.Auth.Sent {
			x.Auth.Sent[i].CodeHash, x.Auth.Sent[i].LinkID = "", ""
		}
		x.event(now, SystemActor, "Expired: the client did not answer by "+dateOnly(x.Auth.Expires)+"; nothing was changed")
		done = true
		return nil
	})
	if err != nil || !done {
		return false
	}
	o.log(ctx, tenant, SystemActor, "change.expired", id, map[string]any{"ucc": r.UCC, "kind": r.Kind, "by": r.By})
	if c, err := o.Client(ctx, tenant, r.UCC); err == nil {
		set, _ := o.Settings(ctx, tenant)
		for _, ct := range authContacts(c) {
			if ct[0] == "mobile" {
				txt := fmt.Sprintf("%s: request %s to change the bank accounts of %s was not confirmed and has expired. Nothing was changed. -%s", set.Firm, r.ID, r.UCC, set.Firm)
				o.sendDirectMsg(ctx, tenant, SystemActor, ChSMS, ct[1], r.UCC, "change.bank_expired", "", txt, txt)
				continue
			}
			txt := fmt.Sprintf("Dear %s,\n\nRequest %s to change the bank accounts registered for your trading account %s was not confirmed in time and has expired. Nothing was changed:\n\n%s\n\nIf you still want this change, please ask us again.\n\n%s", c.Name, r.ID, r.UCC, r.Auth.Shown, set.Firm)
			o.sendDirectMsg(ctx, tenant, SystemActor, ChEmail, ct[1], r.UCC, "change.bank_expired", "Your bank account change request "+r.ID+" has expired", txt, txt)
		}
	}
	o.raiseWork(ctx, tenant, "bankauth", id+":expired", fmt.Sprintf("%s's bank account change %s expired", r.UCC, id),
		fmt.Sprintf("%s (%s), raised by %s on %s: the client did not confirm %s by %s. Nothing was changed and the client has been told. If the client still wants it, raise it again or get the client's signed letter.",
			id, r.Name, r.By, dateOnly(r.At), lowerFirst(r.Auth.Shown), dateOnly(r.Auth.Expires)), r.UCC, RoleMaker, "changes", "")
	return true
}
