package ops

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/docs"
)

// Synthetic clients only: made-up codes, accounts, mobiles and e-mails.

// caSetup makes two clients with their own portal logins, at a fixed time.
func caSetup(t *testing.T) (*Ops, context.Context, *time.Time) {
	t.Helper()
	o, ctx := newOps(t)
	now := time.Date(2026, 11, 20, 11, 0, 0, 0, ist)
	o.Now = func() time.Time { return now }
	for i, u := range []string{"CA101", "CA102"} {
		c := cbClient(t, u)
		c.Mobile = []string{"9811100001", "9811100002"}[i]
		if err := docs.Upsert(ctx, o.Docs, T, cClient, c.UCC, c, "test"); err != nil {
			t.Fatal(err)
		}
		if _, _, err := o.PortalAccess(ctx, T, "farhan.khan", u, nil, false); err != nil {
			t.Fatal(err)
		}
	}
	return o, ctx, &now
}

func caAdd(t *testing.T, o *Ops, ucc, acct string, extra map[string]string) ChangeRequest {
	t.Helper()
	v := map[string]string{"action": "add", "bank": "ICICI Bank", "ifsc": "ICIC0000002", "account": acct}
	for k, x := range extra {
		v[k] = x
	}
	r, err := o.RequestChange(t.Context(), T, "priya.nair", ucc, "Bank account", v, cbProof(t, o, ucc, "Bank proof"))
	if err != nil {
		t.Fatalf("raise %s: %v", ucc, err)
	}
	return r
}

func caAudited(t *testing.T, o *Ops, action, id string) bool {
	t.Helper()
	evs, _ := o.App.Audit.List(t.Context(), T, 0)
	for _, e := range evs {
		if e.Action == action && e.Object == id {
			return true
		}
	}
	return false
}

// TestBankAuthByCode: a bank change staff raise waits for the client, who
// confirms it with the code in the portal; then a checker approves it.
func TestBankAuthByCode(t *testing.T) {
	o, ctx, _ := caSetup(t)
	r := caAdd(t, o, "CA101", "000201234567", nil)
	if r.Status != ChangeWaitClient || r.Auth == nil || r.Auth.Form != AuthClient || len(r.Auth.Sent) != 2 {
		t.Fatalf("raised: %+v %+v", r, r.Auth)
	}
	if !strings.Contains(r.Auth.Shown, "XXXXXXXX4567") || !strings.Contains(r.Auth.Shown, "ICIC0000002") || !strings.Contains(r.Auth.Shown, "ICICI Bank") || strings.Contains(r.Auth.Shown, "01234567") {
		t.Fatalf("shown: %q", r.Auth.Shown)
	}
	for _, s := range r.Auth.Sent {
		if s.CodeHash != "" || s.LinkID != "" || strings.Contains(s.To, "9811100001") || s.Status != "Sent" {
			t.Fatalf("sent: %+v", s)
		}
	}
	mob, mail := r.DevAuth["mobile_code"], r.DevAuth["e-mail_code"]
	if len(mob) != 6 || len(mail) != 6 {
		t.Fatalf("dev codes: %v", r.DevAuth)
	}
	// The messages went to the registered mobile and e-mail with what
	// changes; the copy kept has neither the code nor a working link.
	ms, _ := docs.List[Message](ctx, o.Docs, T, cMessage, "")
	got := map[string]Message{}
	for _, m := range ms {
		if m.Event == "change.bank_confirm" {
			got[m.Channel] = m
		}
	}
	if got[ChSMS].To != "9811100001" || got[ChEmail].To != "ca101@example.in" || !strings.Contains(got[ChEmail].Body, "XXXXXXXX4567") ||
		strings.Contains(got[ChSMS].Body, mob) || strings.Contains(got[ChEmail].Body, mail) || strings.Contains(got[ChEmail].Body, r.DevAuth["e-mail_token"]) {
		t.Fatalf("messages: %+v", got)
	}
	// Not approvable until the client confirms.
	if _, err := o.DecideChange(ctx, T, "farhan.khan", r.ID, true, ""); !errors.Is(err, ErrState) || !strings.Contains(err.Error(), "waiting for the client") {
		t.Fatalf("approved before the client confirmed: %v", err)
	}
	// Another client's code, and another client's login, are refused; so is
	// a family login that sees the account but is not the holder's.
	other := caAdd(t, o, "CA102", "000209999999", nil)
	if _, err := o.ClientAnswerChange(ctx, T, "ca101", r.ID, other.DevAuth["mobile_code"], true); err == nil || !strings.Contains(err.Error(), "not right") {
		t.Fatalf("another client's code: %v", err)
	}
	if _, err := o.ClientAnswerChange(ctx, T, "ca102", r.ID, mob, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another client's login: %v", err)
	}
	if _, _, err := o.PortalAccess(ctx, T, "farhan.khan", "CA102", []string{"CA101"}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := o.ClientAnswerChange(ctx, T, "ca102", r.ID, mob, true); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a family login confirmed: %v", err)
	}
	if _, err := o.ChangeAuthAction(ctx, T, "priya.nair", r.ID, "nonsense", "", ""); err == nil {
		t.Fatal("unknown action")
	}
	// The right code: the request now waits for a checker, with the record.
	v, err := o.ClientAnswerChange(ctx, T, "ca101", r.ID, mail, true)
	if err != nil || v.Status != "Pending" {
		t.Fatalf("confirm: %+v %v", v, err)
	}
	a := v.Auth
	if a.Answer != "Confirmed" || a.Channel != "e-mail" || a.To != "ca•••@example.in" || a.Via != "code in the client portal" || a.At == "" || a.By != "ca101" || a.Tries != 1 {
		t.Fatalf("record: %+v", a)
	}
	if !caAudited(t, o, "change.client_confirmed", r.ID) || !caAudited(t, o, "change.client_asked", r.ID) {
		t.Fatal("not audited")
	}
	// A code used twice is refused.
	if _, err := o.ClientAnswerChange(ctx, T, "ca101", r.ID, mail, true); !errors.Is(err, ErrState) {
		t.Fatalf("code used twice: %v", err)
	}
	if _, err := o.ClientAnswerChange(ctx, T, "ca101", r.ID, mob, true); !errors.Is(err, ErrState) {
		t.Fatalf("the other code after confirming: %v", err)
	}
	// Maker-checker as before.
	if _, err := o.DecideChange(ctx, T, "priya.nair", r.ID, true, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker approved: %v", err)
	}
	v, err = o.DecideChange(ctx, T, "farhan.khan", r.ID, true, "")
	if err != nil || v.Status != "Approved" {
		t.Fatalf("approve: %+v %v", v, err)
	}
	if c, _ := o.Client(ctx, T, "CA101"); len(activeBanks(c)) != 2 {
		t.Fatalf("not applied: %+v", c.Banks)
	}
	var steps []string
	for _, h := range v.History {
		steps = append(steps, h.What)
	}
	hist := strings.Join(steps, " | ")
	for _, w := range []string{"waiting for the client to confirm", "Sent to the client to confirm: mobile", "Confirmed by the client by code in the client portal (sent to e-mail", "Approved by farhan.khan on the client confirmation"} {
		if !strings.Contains(hist, w) {
			t.Fatalf("history lacks %q: %s", w, hist)
		}
	}
	// The client's change log lists the request with the record.
	pv, err := o.Portal(ctx, T, "ca101", "2026-11-20", "CA101")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ch := range pv.Changes {
		found = found || (ch.ID == r.ID && ch.Auth != nil && ch.Auth.Answer == "Confirmed" && len(ch.History) >= 4)
	}
	if !found {
		t.Fatalf("portal change log: %+v", pv.Changes)
	}
	// Wrong codes run out.
	for i := 0; i < bankAuthMaxTries; i++ {
		o.ClientAnswerChange(ctx, T, "ca102", other.ID, "000000", true)
	}
	if _, err := o.ClientAnswerChange(ctx, T, "ca102", other.ID, other.DevAuth["mobile_code"], true); err == nil || !strings.Contains(err.Error(), "too many") {
		t.Fatalf("after too many tries: %v", err)
	}
	// Sending again gives a new code; the old one stops working.
	again, err := o.ChangeAuthAction(ctx, T, "priya.nair", other.ID, "send", "", "")
	if err != nil || again.Auth.Sends != 2 || again.Auth.Tries != 0 || again.DevAuth["mobile_code"] == "" {
		t.Fatalf("send again: %+v %v", again, err)
	}
	if again.DevAuth["mobile_code"] != other.DevAuth["mobile_code"] {
		if _, err := o.ClientAnswerChange(ctx, T, "ca102", other.ID, other.DevAuth["mobile_code"], true); err == nil {
			t.Fatal("the old code still works")
		}
	}
	if p, _ := o.AnswerBankAuthLink(ctx, T, other.DevAuth["mobile_token"], true, "198.51.100.7"); !strings.Contains(p.Error, "newer message") {
		t.Fatalf("the old link: %+v", p)
	}
	if v, err := o.ClientAnswerChange(ctx, T, "ca102", other.ID, again.DevAuth["mobile_code"], true); err != nil || v.Auth.Channel != "mobile" || v.Auth.To != "••••••0002" {
		t.Fatalf("the new code: %+v %v", v, err)
	}
}

// TestBankAuthByLink: the link shows the change and answers only on a
// button press; used once.
func TestBankAuthByLink(t *testing.T) {
	t.Setenv("LEDGERLINE_PUBLIC_URL", "")
	o, ctx, _ := caSetup(t)
	cbChange(t, o, "CA101", map[string]string{"action": "add", "bank": "Axis Bank", "ifsc": "UTIB0000003", "account": "911010000000003"}, cbProof(t, o, "CA101", "Bank proof"))
	r, err := o.RequestChange(ctx, T, "priya.nair", "CA101", "Bank account", map[string]string{"action": "primary", "target": "XXXXXXXXXXX0003", "ifsc": "UTIB0000003"}, "")
	if err != nil || r.Status != ChangeWaitClient {
		t.Fatalf("raise: %+v %v", r, err)
	}
	if !strings.Contains(r.Auth.Shown, "Make Axis Bank account XXXXXXXXXXX0003 (IFSC UTIB0000003) your primary account") || !strings.Contains(r.Auth.Shown, "XXXXXXXXXX5678") {
		t.Fatalf("shown: %q", r.Auth.Shown)
	}
	tok := r.DevAuth["e-mail_token"]
	if r.DevAuth["e-mail_link"] != "" {
		t.Fatalf("a link without a public address: %v", r.DevAuth)
	}
	p, _, _, err := o.OpenBankAuthLink(ctx, T, tok)
	if err != nil || p.UCC != "CA101" || p.ID != r.ID || p.Shown != r.Auth.Shown || p.Answer != "" {
		t.Fatalf("open: %+v %v", p, err)
	}
	if x, _, _ := docs.Get[ChangeRequest](ctx, o.Docs, T, cChange, r.ID); x.Status != ChangeWaitClient {
		t.Fatal("opening the link answered it")
	}
	if p, err := o.AnswerBankAuthLink(ctx, T, "not-a-real-token-at-all", true, ""); err == nil || p.Error == "" {
		t.Fatal("a made-up link worked")
	}
	p, err = o.AnswerBankAuthLink(ctx, T, tok, true, "198.51.100.7")
	if err != nil || p.Answer != "Confirmed" {
		t.Fatalf("confirm: %+v %v", p, err)
	}
	x, _, _ := docs.Get[ChangeRequest](ctx, o.Docs, T, cChange, r.ID)
	if x.Status != "Pending" || x.Auth.Via != "link" || x.Auth.Channel != "e-mail" || x.Auth.From != "198.51.100.7" || x.Auth.By != "client:CA101" {
		t.Fatalf("record: %+v", x.Auth)
	}
	// Used twice: refused; the other channel's link is closed too.
	if p, err := o.AnswerBankAuthLink(ctx, T, tok, true, ""); err == nil || !strings.Contains(p.Error, "already been used") {
		t.Fatalf("link used twice: %+v %v", p, err)
	}
	if p, _ := o.AnswerBankAuthLink(ctx, T, r.DevAuth["mobile_token"], false, ""); !strings.Contains(p.Error, "already been answered") {
		t.Fatalf("other link after the answer: %+v", p)
	}
	if v, err := o.DecideChange(ctx, T, "farhan.khan", r.ID, true, ""); err != nil || v.Status != "Approved" {
		t.Fatalf("approve: %+v %v", v, err)
	}
	if c, _ := o.Client(ctx, T, "CA101"); c.Bank.Account != "911010000000003" {
		t.Fatalf("primary not changed: %+v", c.Bank)
	}
	// With a public address the message carries the link.
	r2, err := o.RequestChange(WithPublicURL(ctx, "https://bo.example.in/"), T, "priya.nair", "CA102", "Bank account", map[string]string{"action": "add", "bank": "SBI", "ifsc": "SBIN0000004", "account": "300000000004"}, cbProof(t, o, "CA102", "Bank proof"))
	if err != nil || r2.DevAuth["mobile_link"] != "https://bo.example.in"+BankAuthPath(T, r2.DevAuth["mobile_token"]) {
		t.Fatalf("link: %v %v", r2.DevAuth, err)
	}
}

// TestBankAuthRefused: the client refuses (by link or in the portal); the
// request closes and the compliance officer is told.
func TestBankAuthRefused(t *testing.T) {
	o, ctx, _ := caSetup(t)
	r := caAdd(t, o, "CA101", "000201234567", nil)
	p, err := o.AnswerBankAuthLink(ctx, T, r.DevAuth["mobile_token"], false, "198.51.100.8")
	if err != nil || p.Answer != "Refused" {
		t.Fatalf("refuse: %+v %v", p, err)
	}
	x, _, _ := docs.Get[ChangeRequest](ctx, o.Docs, T, cChange, r.ID)
	if x.Status != ChangeRefused || x.Auth.Answer != "Refused" || x.Auth.Channel != "mobile" || x.Decided == "" {
		t.Fatalf("refused: %+v %+v", x, x.Auth)
	}
	if _, err := o.DecideChange(ctx, T, "farhan.khan", r.ID, true, ""); !errors.Is(err, ErrState) {
		t.Fatalf("a refused change approved: %v", err)
	}
	if _, err := o.ClientAnswerChange(ctx, T, "ca101", r.ID, r.DevAuth["e-mail_code"], true); !errors.Is(err, ErrState) {
		t.Fatalf("confirmed after refusing: %v", err)
	}
	if w, _, err := docs.Get[WorkItem](ctx, o.Docs, T, cWorkItem, "W-bankauth-"+r.ID+"-refused"); err != nil || w.Role != RoleCompliance {
		t.Fatalf("compliance work item: %+v %v", w, err)
	}
	if !caAudited(t, o, "change.client_refused", r.ID) {
		t.Fatal("refusal not audited")
	}
	// In the portal, no code is needed to refuse.
	r2 := caAdd(t, o, "CA101", "000201234567", nil)
	if v, err := o.ClientAnswerChange(ctx, T, "ca101", r2.ID, "", false); err != nil || v.Status != ChangeRefused || v.Auth.Via != "client portal" {
		t.Fatalf("refuse in the portal: %+v %v", v, err)
	}
}

// TestBankAuthExpiry: unanswered, the request expires after the firm's
// days; the client and the maker are told.
func TestBankAuthExpiry(t *testing.T) {
	o, ctx, now := caSetup(t)
	set, _ := o.Settings(ctx, T)
	set.BankAuthDays = 3
	if err := o.SaveSettings(ctx, T, "admin", set); err != nil {
		t.Fatal(err)
	}
	set.BankAuthDays = 31
	if err := o.SaveSettings(ctx, T, "admin", set); err == nil {
		t.Fatal("31 days accepted")
	}
	r := caAdd(t, o, "CA101", "000201234567", nil)
	if exp, _ := time.Parse(time.RFC3339, r.Auth.Expires); !exp.Equal(now.Add(72 * time.Hour)) {
		t.Fatalf("expires %s", r.Auth.Expires)
	}
	*now = now.Add(48 * time.Hour)
	if n := o.ExpireBankAuths(ctx, T); n != 0 {
		t.Fatalf("expired early: %d", n)
	}
	*now = now.Add(25 * time.Hour)
	// The code no longer works, even before the tick ran.
	if _, err := o.ClientAnswerChange(ctx, T, "ca101", r.ID, r.DevAuth["mobile_code"], true); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("code after expiry: %v", err)
	}
	x, _, _ := docs.Get[ChangeRequest](ctx, o.Docs, T, cChange, r.ID)
	if x.Status != ChangeExpired || x.Auth.State != "Expired" {
		t.Fatalf("expired: %+v", x)
	}
	if p, _ := o.AnswerBankAuthLink(ctx, T, r.DevAuth["e-mail_token"], true, ""); !strings.Contains(p.Error, "expired") {
		t.Fatalf("link after expiry: %+v", p)
	}
	if w, _, err := docs.Get[WorkItem](ctx, o.Docs, T, cWorkItem, "W-bankauth-"+r.ID+"-expired"); err != nil || w.Role != RoleMaker || !strings.Contains(w.Detail, "priya.nair") {
		t.Fatalf("maker's work item: %+v %v", w, err)
	}
	ms, _ := docs.List[Message](ctx, o.Docs, T, cMessage, "")
	told := 0
	for _, m := range ms {
		if m.Event == "change.bank_expired" && m.UCC == "CA101" {
			told++
		}
	}
	if told != 2 {
		t.Fatalf("client told %d times", told)
	}
	// The tick expires the rest.
	r2 := caAdd(t, o, "CA101", "000201234567", nil)
	*now = now.Add(4 * 24 * time.Hour)
	o.bankAuthTick(ctx, T, *now)
	if x, _, _ := docs.Get[ChangeRequest](ctx, o.Docs, T, cChange, r2.ID); x.Status != ChangeExpired || !caAudited(t, o, "change.expired", r2.ID) {
		t.Fatalf("tick: %+v", x)
	}
	if _, err := o.DecideChange(ctx, T, "farhan.khan", r2.ID, true, ""); !errors.Is(err, ErrState) {
		t.Fatalf("an expired change approved: %v", err)
	}
}

// TestBankAuthLetter: the signed letter instead of the client's
// confirmation; the firm's setting chooses which forms are taken.
func TestBankAuthLetter(t *testing.T) {
	o, ctx, _ := caSetup(t)
	letter := cbProof(t, o, "CA101", DocClientAuth)
	r := caAdd(t, o, "CA101", "000201234567", map[string]string{"auth_doc": letter, "auth": "esign"})
	if r.Status != "Pending" || r.Auth.Form != AuthESign || r.Auth.Doc != letter || r.Auth.DocBy != "priya.nair" || len(r.Auth.Sent) != 0 || r.New["auth_doc"] != "" {
		t.Fatalf("with the letter: %+v %+v", r, r.Auth)
	}
	if v, err := o.DecideChange(ctx, T, "farhan.khan", r.ID, true, ""); err != nil || v.Status != "Approved" || !strings.Contains(v.History[len(v.History)-1].What, "e-signed request") {
		t.Fatalf("approve on the letter: %+v %v", v, err)
	}
	// Another client's letter, or a document of another kind, is not one.
	if _, err := o.RequestChange(ctx, T, "priya.nair", "CA102", "Bank account", map[string]string{"action": "add", "bank": "SBI", "ifsc": "SBIN0000004", "account": "300000000004", "auth_doc": letter}, cbProof(t, o, "CA102", "Bank proof")); err == nil || !strings.Contains(err.Error(), DocClientAuth) {
		t.Fatalf("another client's letter: %v", err)
	}
	// A request waiting for the client can take the letter instead; the
	// code sent stops working.
	w := caAdd(t, o, "CA102", "300000000004", nil)
	if _, err := o.ChangeAuthAction(ctx, T, "priya.nair", w.ID, "letter", cbProof(t, o, "CA102", "Other"), ""); err == nil {
		t.Fatal("a document that is not a letter")
	}
	v, err := o.ChangeAuthAction(ctx, T, "priya.nair", w.ID, "letter", cbProof(t, o, "CA102", DocClientAuth), "")
	if err != nil || v.Status != "Pending" || v.Auth.Form != AuthLetter {
		t.Fatalf("letter on a waiting request: %+v %v", v, err)
	}
	if _, err := o.ClientAnswerChange(ctx, T, "ca102", w.ID, w.DevAuth["mobile_code"], true); !errors.Is(err, ErrState) {
		t.Fatalf("code after the letter: %v", err)
	}
	if _, err := o.DecideChange(ctx, T, "farhan.khan", w.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	// Letters only: no request goes to the client; confirmation only: no letter.
	set, _ := o.Settings(ctx, T)
	set.BankAuthForms = BankAuthLetter
	if err := o.SaveSettings(ctx, T, "admin", set); err != nil {
		t.Fatal(err)
	}
	if _, err := o.RequestChange(ctx, T, "priya.nair", "CA101", "Bank account", map[string]string{"action": "deactivate", "target": "XXXXXXXX4567", "ifsc": "ICIC0000002"}, ""); err == nil || !strings.Contains(err.Error(), "signed letter") {
		t.Fatalf("letters only, none attached: %v", err)
	}
	set.BankAuthForms = BankAuthConfirm
	if err := o.SaveSettings(ctx, T, "admin", set); err != nil {
		t.Fatal(err)
	}
	if _, err := o.RequestChange(ctx, T, "priya.nair", "CA101", "Bank account", map[string]string{"action": "deactivate", "target": "XXXXXXXX4567", "ifsc": "ICIC0000002"}, cbProof(t, o, "CA101", DocClientAuth)); err == nil || !strings.Contains(err.Error(), "only when the client confirms") {
		t.Fatalf("confirmation only, letter attached: %v", err)
	}
	set.BankAuthForms = "sometimes"
	if err := o.SaveSettings(ctx, T, "admin", set); err == nil {
		t.Fatal("unknown setting accepted")
	}
}

// TestBankAuthNeeded: a staff bank change with no authorisation (raised
// before it was needed) cannot be approved until it gets one; one the
// client raised in the portal needs none.
func TestBankAuthNeeded(t *testing.T) {
	o, ctx, _ := caSetup(t)
	old := ChangeRequest{ID: "CHG-09001", UCC: "CA101", Name: "Asha Ramesh Kulkarni", Kind: "Bank account", Source: "Head office", Status: "Pending", By: "priya.nair", At: o.now().Format(time.RFC3339),
		New: map[string]string{"action": "add", "bank": "ICICI Bank", "ifsc": "ICIC0000002", "account": "000201234567"}, Old: map[string]string{}}
	if err := docs.Upsert(ctx, o.Docs, T, cChange, old.ID, old, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := o.DecideChange(ctx, T, "farhan.khan", old.ID, true, ""); !errors.Is(err, ErrState) || !strings.Contains(err.Error(), "no client authorisation") {
		t.Fatalf("approved without authorisation: %v", err)
	}
	v, err := o.ChangeAuthAction(ctx, T, "priya.nair", old.ID, "send", "", "")
	if err != nil || v.Status != ChangeWaitClient || v.DevAuth["mobile_code"] == "" {
		t.Fatalf("sent to the client: %+v %v", v, err)
	}
	if _, err := o.ChangeAuthAction(ctx, T, "ca101", old.ID, "send", "", ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("the client sent it: %v", err)
	}
	// A checker may still reject a request waiting for the client.
	if v, err := o.DecideChange(ctx, T, "farhan.khan", old.ID, false, "Raised in error"); err != nil || v.Status != "Rejected" {
		t.Fatalf("reject while waiting: %+v %v", v, err)
	}

	// From the portal: the client's own login is the authorisation.
	d, err := o.AddClientDoc(ctx, T, "ca101", "CA101", "Bank proof", "cheque.png", []byte("\x89PNG\r\n\x1a\n0000000000000000"))
	if err != nil {
		t.Fatal(err)
	}
	r, err := o.RequestChange(ctx, T, "ca101", "CA101", "Bank account", map[string]string{"action": "add", "bank": "ICICI Bank", "ifsc": "ICIC0000002", "account": "000201234567"}, d.ID)
	if err != nil || r.Status != "Pending" || r.Auth == nil || r.Auth.Form != AuthPortal || len(r.DevAuth) != 0 {
		t.Fatalf("portal request: %+v %v", r, err)
	}
	ms, _ := docs.List[Message](ctx, o.Docs, T, cMessage, "")
	for _, m := range ms {
		if m.Event == "change.bank_confirm" && strings.Contains(m.Body, r.ID) {
			t.Fatalf("portal request sent for confirmation: %+v", m)
		}
	}
	if v, err := o.DecideChange(ctx, T, "farhan.khan", r.ID, true, ""); err != nil || v.Status != "Approved" {
		t.Fatalf("portal approve: %+v %v", v, err)
	}
	// A client with no valid registered contact cannot confirm: the letter is needed.
	c, _ := o.Client(ctx, T, "CA102")
	c.Mobile, c.Email, c.Incomplete = "", "nobody.invalid", []string{"Mobile", "Email"}
	docs.Upsert(ctx, o.Docs, T, cClient, c.UCC, c, "test")
	if _, err := o.RequestChange(ctx, T, "priya.nair", "CA102", "Bank account", map[string]string{"action": "add", "bank": "SBI", "ifsc": "SBIN0000004", "account": "300000000004"}, cbProof(t, o, "CA102", "Bank proof")); err == nil || !strings.Contains(err.Error(), "no valid registered mobile or e-mail") {
		t.Fatalf("no contact: %v", err)
	}
}
