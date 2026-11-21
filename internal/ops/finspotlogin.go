package ops

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"ledgerline/internal/docs"
)

// ---- a client's user ID (login) on Finspot ----
//
// A client trades on Finspot with a user ID the firm creates: by hand in
// Finspot's admin console, or from Ledgerline (AddInvestorClient with its
// segments and products, finspotapi.go, or Ledgerline's Investor Client
// upload file). The firm says which in Settings → Firm practices
// (Settings.FinspotLogins):
//
//   - console: staff create every login in Finspot's admin console.
//     Ledgerline never creates one.
//   - ledgerline: Ledgerline creates the login of every new active client
//     by API (with client logins by API on), or in its upload file without
//     the API.
//   - both: staff create some in the console; Ledgerline creates one only
//     when someone clicks Create Finspot login for the client.
//
// Whatever the choice, Ledgerline must never create a second login for a
// client who already has one, and should show who has one. It knows a
// client has a login when:
//
//   - Ledgerline created it (by API, or in an upload file it made);
//   - Finspot refused AddInvestorClient because the client exists ("Client
//     exists"): it was made in the console. The login is then changed
//     (ModifyClient) instead, and recorded as found on Finspot;
//   - a read-back (Check Finspot login) found the account: Finspot's
//     Limits call, the read-back Check Finspot already uses, answered for
//     the account. The Noren document does not say what Limits answers for
//     an account that does not exist; Ledgerline takes only an Ok answer as
//     found and never takes a refusal as proof that there is none, so a
//     refusal changes nothing and creation still relies on Finspot's own
//     "Client exists" refusal;
//   - staff marked it as made in the console (without the API).
//
// The records are kept per client (cFNLogin) with who and when.

const (
	cFNLogin = "finspotlogin" // UCC -> FinspotLogin

	FinspotLoginsConsole    = "console"
	FinspotLoginsLedgerline = "ledgerline"
	FinspotLoginsBoth       = "both"

	FinspotLoginByAPI    = "Created by Ledgerline (API)"
	FinspotLoginByFile   = "Sent in Ledgerline's client upload file"
	FinspotLoginFound    = "Found on Finspot (made in its admin console)"
	FinspotLoginChecked  = "Found on Finspot by a check"
	FinspotLoginMarked   = "Marked by staff: made in Finspot's admin console"
	FinspotLoginPrevious = "Sent to Finspot by Ledgerline before"
)

// FinspotLogin is what Ledgerline knows of a client's Finspot login.
type FinspotLogin struct {
	UCC string `json:"ucc"`
	Has bool   `json:"has"`
	How string `json:"how,omitempty"`
	At  string `json:"at,omitempty"`
	By  string `json:"by,omitempty"`
	// Note is the staff's note when they marked it, or Finspot's words at
	// the last check.
	Note      string `json:"note,omitempty"`
	CheckedAt string `json:"checked_at,omitempty"`
	// Requested: someone asked Ledgerline to create it (firm practice
	// "both"); cleared once Finspot has it.
	Requested   bool   `json:"requested,omitempty"`
	RequestedBy string `json:"requested_by,omitempty"`
	RequestedAt string `json:"requested_at,omitempty"`
}

// FinspotLoginView is a client's Finspot login for the screen.
type FinspotLoginView struct {
	FinspotLogin
	ActID string `json:"act_id"`
	Mode  string `json:"mode"`
	// API: Finspot by API is set up, so Ledgerline can check a login;
	// APILogins: with client logins by API on, so it can create one.
	API       bool   `json:"api"`
	APILogins bool   `json:"api_logins"`
	CanCreate bool   `json:"can_create"`
	CreateWhy string `json:"create_why,omitempty"` // why Create is not offered
	// Used: the firm has Finspot set up at all (uploads or API).
	Used bool `json:"used"`
}

// finspotLoginMode is the firm's practice for Finspot logins.
func (o *Ops) finspotLoginMode(ctx context.Context, tenant string) string {
	set, _ := o.Settings(ctx, tenant)
	if set.FinspotLogins != "" {
		return set.FinspotLogins
	}
	if cn, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connFinspotAPI); err == nil && cn.Status != "Not set" && cn.Values["clients"] == "On" {
		return FinspotLoginsLedgerline
	}
	return FinspotLoginsConsole
}

// finspotLoginRecords are the stored login records by UCC.
func (o *Ops) finspotLoginRecords(ctx context.Context, tenant string) map[string]FinspotLogin {
	rs, _ := docs.List[FinspotLogin](ctx, o.Docs, tenant, cFNLogin, "")
	out := make(map[string]FinspotLogin, len(rs))
	for _, r := range rs {
		out[r.UCC] = r
	}
	return out
}

// finspotLoginsKnown is every client Ledgerline knows has a Finspot login:
// the stored records, the clients pushed by API before and those in an
// upload file Ledgerline made.
func (o *Ops) finspotLoginsKnown(ctx context.Context, tenant string) map[string]FinspotLogin {
	out := map[string]FinspotLogin{}
	for u, r := range o.finspotLoginRecords(ctx, tenant) {
		out[u] = r
	}
	add := func(kind, how string, coll string) {
		xs, _ := docs.List[finspotSentState](ctx, o.Docs, tenant, coll, kind+"|")
		for _, x := range xs {
			if r := out[x.UCC]; !r.Has {
				r.UCC, r.Has, r.How, r.At = x.UCC, true, how, x.On
				out[x.UCC] = r
			}
		}
	}
	add("client", FinspotLoginPrevious, cFNSent)
	add("clients", FinspotLoginByFile, cFinspotSent)
	return out
}

func (o *Ops) noteFinspotLogin(ctx context.Context, tenant, actor, ucc, how, note string) {
	at := o.now().Format(time.RFC3339)
	_, err := docs.Update(ctx, o.Docs, tenant, cFNLogin, ucc, actor, func(r *FinspotLogin) error {
		r.Has, r.How, r.At, r.By, r.Note, r.Requested = true, how, at, actor, note, false
		return nil
	})
	if errors.Is(err, docs.ErrNotFound) {
		_ = docs.Upsert(ctx, o.Docs, tenant, cFNLogin, ucc, FinspotLogin{UCC: ucc, Has: true, How: how, At: at, By: actor, Note: note}, actor)
	}
	o.log(ctx, tenant, actor, "finspot.login_known", ucc, map[string]any{"how": how})
}

// FinspotLoginGet is a client's Finspot login as Ledgerline knows it.
func (o *Ops) FinspotLoginGet(ctx context.Context, tenant, actor, ucc string) (FinspotLoginView, error) {
	if err := o.require(ctx, tenant, actor, "Viewing Finspot logins", RoleMaker, RoleChecker, RoleRisk, RoleCompliance); err != nil {
		return FinspotLoginView{}, err
	}
	return o.finspotLoginView(ctx, tenant, ucc)
}

func (o *Ops) finspotLoginView(ctx context.Context, tenant, ucc string) (FinspotLoginView, error) {
	ucc = strings.ToUpper(strings.TrimSpace(ucc))
	c, _, err := docs.Get[Client](ctx, o.Docs, tenant, cClient, ucc)
	if err != nil {
		return FinspotLoginView{}, invalid("client %s not found", ucc)
	}
	cfg, _ := o.finspotConfig(ctx, tenant)
	v := FinspotLoginView{ActID: cfg.ID(ucc), Mode: o.finspotLoginMode(ctx, tenant)}
	v.FinspotLogin = o.finspotLoginsKnown(ctx, tenant)[ucc]
	v.UCC = ucc
	_, x, cerr := o.finspotAPIConnection(ctx, tenant)
	v.API, v.APILogins = cerr == nil, cerr == nil && x.clients
	v.Used = cfg.Set || cerr == nil
	switch {
	case v.Has:
		v.CreateWhy = "the client already has a Finspot login"
	case v.Mode == FinspotLoginsConsole:
		v.CreateWhy = "the firm creates Finspot logins in Finspot's admin console (Settings → Firm practices)"
	case c.Status != "Active":
		v.CreateWhy = "the client is not active"
	case c.DPOnly:
		v.CreateWhy = "a DP-only client has no trading account"
	case !v.APILogins:
		v.CreateWhy = "set up Finspot by API with client logins on, or use the Investor Client upload file (Live RMS → Front-office files)"
	default:
		v.CanCreate = true
	}
	return v, nil
}

// FinspotLoginCheck reads the client's account back from Finspot (its
// Limits call) and, when Finspot answers for it, records that the client
// has a Finspot login. A refusal changes nothing (see the top of the file).
func (o *Ops) FinspotLoginCheck(ctx context.Context, tenant, actor, ucc string) (FinspotLoginView, error) {
	if err := o.require(ctx, tenant, actor, "Checking a Finspot login", RoleMaker, RoleChecker, RoleRisk); err != nil {
		return FinspotLoginView{}, err
	}
	v, err := o.finspotLoginView(ctx, tenant, ucc)
	if err != nil {
		return v, err
	}
	_, x, err := o.finspotAPIConnection(ctx, tenant)
	if err != nil {
		return v, err
	}
	cctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	c := &fnCall{ctx: cctx, tenant: tenant, x: x, now: o.now}
	ans, err := c.req("Limits", v.UCC, "check login "+v.ActID, map[string]any{"actid": v.ActID}, fnSrcUID)
	at := o.now().Format(time.RFC3339)
	var stp *fnStop
	switch {
	case errors.As(err, &stp):
		return v, fmt.Errorf("%w: %s", ErrState, stp.msg)
	case err != nil:
		note := "Finspot did not find the account: " + err.Error()
		_, uerr := docs.Update(ctx, o.Docs, tenant, cFNLogin, v.UCC, actor, func(r *FinspotLogin) error { r.CheckedAt, r.Note = at, note; return nil })
		if errors.Is(uerr, docs.ErrNotFound) {
			_ = docs.Upsert(ctx, o.Docs, tenant, cFNLogin, v.UCC, FinspotLogin{UCC: v.UCC, CheckedAt: at, Note: note}, actor)
		}
		o.log(ctx, tenant, actor, "finspot.login_checked", v.UCC, map[string]any{"found": false})
	default:
		if ans.obj == nil && len(ans.arr) > 0 {
			ans.obj = ans.arr[0]
		}
		if a := ans.get("actid"); a != "" && !strings.EqualFold(a, v.ActID) {
			return v, fmt.Errorf("%w: Finspot answered for another account (%s); nothing was recorded", ErrState, a)
		}
		if !v.Has {
			o.noteFinspotLogin(ctx, tenant, actor, v.UCC, FinspotLoginChecked, "")
		}
		_, _ = docs.Update(ctx, o.Docs, tenant, cFNLogin, v.UCC, actor, func(r *FinspotLogin) error { r.CheckedAt = at; return nil })
		o.log(ctx, tenant, actor, "finspot.login_checked", v.UCC, map[string]any{"found": true})
	}
	return o.finspotLoginView(ctx, tenant, v.UCC)
}

// FinspotLoginMark records that staff made the client's login in Finspot's
// admin console (for a firm without the API, or before a check).
func (o *Ops) FinspotLoginMark(ctx context.Context, tenant, actor, ucc, note string) (FinspotLoginView, error) {
	if err := o.require(ctx, tenant, actor, "Recording a Finspot login", RoleMaker, RoleChecker, RoleRisk); err != nil {
		return FinspotLoginView{}, err
	}
	v, err := o.finspotLoginView(ctx, tenant, ucc)
	if err != nil {
		return v, err
	}
	if v.Has {
		return v, fmt.Errorf("%w: %s already has a Finspot login (%s)", ErrState, v.UCC, v.How)
	}
	if len(note) > 300 {
		return v, invalid("keep the note under 300 characters")
	}
	o.noteFinspotLogin(ctx, tenant, actor, v.UCC, FinspotLoginMarked, strings.TrimSpace(note))
	return o.finspotLoginView(ctx, tenant, v.UCC)
}

// FinspotLoginCreate creates the client's Finspot login from Ledgerline by
// API, when the firm's practice lets Ledgerline create logins and the
// client has none. Finspot's own "Client exists" refusal still guards
// against a login made in the console a moment before: it is then changed
// and recorded as found.
func (o *Ops) FinspotLoginCreate(ctx context.Context, tenant, actor, ucc string) (FinspotLoginView, FinspotAPIRun, error) {
	if err := o.require(ctx, tenant, actor, "Creating a Finspot login", RoleMaker, RoleRisk); err != nil {
		return FinspotLoginView{}, FinspotAPIRun{}, err
	}
	v, err := o.finspotLoginView(ctx, tenant, ucc)
	if err != nil {
		return v, FinspotAPIRun{}, err
	}
	if !v.CanCreate {
		return v, FinspotAPIRun{}, fmt.Errorf("%w: Ledgerline cannot create %s's Finspot login: %s", ErrState, v.UCC, v.CreateWhy)
	}
	at := o.now().Format(time.RFC3339)
	_, uerr := docs.Update(ctx, o.Docs, tenant, cFNLogin, v.UCC, actor, func(r *FinspotLogin) error {
		r.Requested, r.RequestedBy, r.RequestedAt = true, actor, at
		return nil
	})
	if errors.Is(uerr, docs.ErrNotFound) {
		uerr = docs.Upsert(ctx, o.Docs, tenant, cFNLogin, v.UCC, FinspotLogin{UCC: v.UCC, Requested: true, RequestedBy: actor, RequestedAt: at}, actor)
	}
	if uerr != nil {
		return v, FinspotAPIRun{}, uerr
	}
	o.log(ctx, tenant, actor, "finspot.login_requested", v.UCC, nil)
	run, err := o.finspotAPIPush(ctx, tenant, actor, o.now().In(ist).Format("2006-01-02"), "By hand", []string{FNPartClients}, []string{v.UCC}, true)
	if err != nil {
		return v, run, err
	}
	v, err = o.finspotLoginView(ctx, tenant, v.UCC)
	if err == nil && !v.Has && len(run.Failures) > 0 {
		err = fmt.Errorf("%w: Finspot did not take the login: %s", ErrState, run.Failures[0])
	}
	return v, run, err
}
