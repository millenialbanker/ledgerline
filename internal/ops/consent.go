package ops

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"html/template"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
)

// ---- WhatsApp consent ----
//
// WhatsApp's business policy lets a business start a conversation only
// with people who agreed to hear from it on WhatsApp, and the DPDP Act
// expects consent for using a mobile number beyond what it was given for.
// Every opt-in and opt-out is recorded with when, how and by whom, so the
// firm can show it to Meta, an auditor or the client. Messages go on
// WhatsApp only to clients whose consent is on.

const cWAInvite = "wainvite"

// Consent sources.
const (
	SrcOnboarding = "Account opening form"
	SrcPortal     = "Client portal"
	SrcWhatsApp   = "WhatsApp message"
	SrcLink       = "Consent link"
	SrcCall       = "Recorded call"
	SrcForm       = "Signed form"
	SrcMigrated   = "Migrated from previous system"
)

// StaffConsentSources are the ways staff may record a client's choice.
var StaffConsentSources = []string{SrcCall, SrcForm, SrcOnboarding}

// WAConsent is a client's current WhatsApp choice and how it was made.
type WAConsent struct {
	On      bool             `json:"on"`
	At      string           `json:"at"`
	Source  string           `json:"source"`
	By      string           `json:"by"`
	Ref     string           `json:"ref,omitempty"` // call recording, form number, link token, chat ID
	History []WAConsentEvent `json:"history,omitempty"`
}

// WAConsentEvent is one change.
type WAConsentEvent struct {
	On     bool   `json:"on"`
	At     string `json:"at"`
	Source string `json:"source"`
	By     string `json:"by"`
	Ref    string `json:"ref,omitempty"`
}

func (c *Client) setConsent(on bool, at, source, by, ref string) {
	if c.WAConsent == nil {
		c.WAConsent = &WAConsent{}
	}
	w := c.WAConsent
	w.History = append(w.History, WAConsentEvent{On: on, At: at, Source: source, By: by, Ref: ref})
	if len(w.History) > 20 {
		w.History = w.History[len(w.History)-20:]
	}
	w.On, w.At, w.Source, w.By, w.Ref = on, at, source, by, ref
	c.WhatsApp = on
}

func (o *Ops) recordConsent(ctx context.Context, tenant, actor, ucc string, on bool, source, ref string) (Client, error) {
	c, err := docs.Update(ctx, o.Docs, tenant, cClient, strings.ToUpper(ucc), actor, func(c *Client) error {
		c.setConsent(on, o.now().Format(time.RFC3339), source, actor, ref)
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "client.whatsapp", c.UCC, map[string]any{"opt_in": on, "source": source, "ref": ref})
	}
	return c, err
}

// RecordConsent is staff recording a client's WhatsApp choice made on a
// recorded call or a signed form; the reference is required.
func (o *Ops) RecordConsent(ctx context.Context, tenant, actor, ucc string, on bool, source, ref string) (Client, error) {
	if err := o.require(ctx, tenant, actor, "Recording WhatsApp consent", RoleMaker, RoleChecker, RoleCompliance, RoleBranch, RoleAP); err != nil {
		return Client{}, err
	}
	if !contains(StaffConsentSources, source) {
		return Client{}, invalid("source must be one of %s", strings.Join(StaffConsentSources, ", "))
	}
	if strings.TrimSpace(ref) == "" {
		return Client{}, invalid("give the call recording or form reference, so the consent can be shown later")
	}
	if _, err := o.Client(ctx, tenant, ucc); err != nil {
		return Client{}, err
	}
	return o.recordConsent(ctx, tenant, actor, ucc, on, source, strings.TrimSpace(ref))
}

// ConsentRow is one client on the consent screen.
type ConsentRow struct {
	UCC     string `json:"ucc"`
	Name    string `json:"name"`
	Mobile  string `json:"mobile"`
	State   string `json:"state"` // Opted in, Opted out, Invited, Not asked
	At      string `json:"at,omitempty"`
	Source  string `json:"source,omitempty"`
	By      string `json:"by,omitempty"`
	Ref     string `json:"ref,omitempty"`
	Invited string `json:"invited,omitempty"`
}

// ConsentView is the WhatsApp consent register.
type ConsentView struct {
	Rows    []ConsentRow   `json:"rows"`
	Counts  map[string]int `json:"counts"`
	Sources []string       `json:"sources"`
}

// Consents lists every active client's WhatsApp consent.
func (o *Ops) Consents(ctx context.Context, tenant string, scope func(string) bool) (ConsentView, error) {
	v := ConsentView{Rows: []ConsentRow{}, Counts: map[string]int{}, Sources: StaffConsentSources}
	cs, err := o.ClientMap(ctx, tenant)
	if err != nil {
		return v, err
	}
	inv := map[string]string{}
	ins, _ := docs.List[WAInvite](ctx, o.Docs, tenant, cWAInvite, "")
	for _, x := range ins {
		if x.Sent > inv[x.UCC] {
			inv[x.UCC] = x.Sent
		}
	}
	for _, u := range sortedKeys(cs) {
		c := cs[u]
		if c.Status != "Active" || (scope != nil && !scope(u)) {
			continue
		}
		r := ConsentRow{UCC: u, Name: c.Name, Mobile: maskMobile(c.Mobile), State: "Not asked", Invited: inv[u]}
		switch {
		case c.WAConsent != nil:
			w := c.WAConsent
			r.At, r.Source, r.By, r.Ref = w.At, w.Source, w.By, w.Ref
			r.State = map[bool]string{true: "Opted in", false: "Opted out"}[w.On]
		case c.WhatsApp:
			r.State, r.Source = "Opted in", "No record"
		case inv[u] != "":
			r.State = "Invited"
		}
		v.Counts[r.State]++
		v.Rows = append(v.Rows, r)
	}
	sortConsent(v.Rows)
	return v, nil
}

// ---- the consent link ----

// WAInvite is a one-time link sent to a client asking for WhatsApp consent.
type WAInvite struct {
	Token   string `json:"token"`
	UCC     string `json:"ucc"`
	Channel string `json:"channel"`
	Sent    string `json:"sent"`
	By      string `json:"by"`
	Answer  string `json:"answer,omitempty"` // Yes, No
	At      string `json:"at,omitempty"`
}

// CampaignResult reports a consent campaign.
type CampaignResult struct {
	Invited   int      `json:"invited"`
	Skipped   int      `json:"skipped"`
	Reasons   []string `json:"reasons,omitempty"`
	Remaining int      `json:"remaining"`
}

// ConsentCampaign sends a one-tap consent link by email or SMS to active
// clients who have not been asked, or not answered in 30 days. Clients who
// said no or opted out are not asked again. base is the console address.
func (o *Ops) ConsentCampaign(ctx context.Context, tenant, actor, channel, base string, limit int) (CampaignResult, error) {
	var res CampaignResult
	if err := o.require(ctx, tenant, actor, "Asking clients for WhatsApp consent", RoleChecker, RoleCompliance); err != nil {
		return res, err
	}
	if channel != ChEmail && channel != ChSMS {
		return res, invalid("channel must be Email or SMS")
	}
	if limit <= 0 {
		limit = 500
	}
	ts, err := o.templates(ctx, tenant)
	if err != nil {
		return res, err
	}
	tpl := ""
	for _, t := range ts {
		if t.Event == "whatsapp.consent" && t.Channel == channel && t.Active {
			tpl = t.ID
		}
	}
	if tpl == "" {
		return res, invalid("no active %s template for whatsapp.consent; set one up under Messages → Templates", channel)
	}
	v, err := o.Consents(ctx, tenant, nil)
	if err != nil {
		return res, err
	}
	today := o.now().Format("2006-01-02")
	base = strings.TrimRight(base, "/")
	for _, r := range v.Rows {
		if r.State == "Opted in" || r.State == "Opted out" || (r.Invited != "" && daysBetween(r.Invited[:10], today) < 30) {
			continue
		}
		if res.Invited >= limit {
			res.Remaining++
			continue
		}
		tok, err := newToken()
		if err != nil {
			return res, err
		}
		inv := WAInvite{Token: tok, UCC: r.UCC, Channel: channel, Sent: o.now().Format(time.RFC3339), By: actor}
		if err := docs.Upsert(ctx, o.Docs, tenant, cWAInvite, tok, inv, actor); err != nil {
			return res, err
		}
		ms, err := o.sendEvent(ctx, tenant, actor, "whatsapp.consent", r.UCC, map[string]string{"link": base + "/optin/" + tenant + "/" + tok, "mobile": r.Mobile}, tpl)
		if err != nil || len(ms) == 0 || ms[0].Status != "Sent" {
			why := "not sent"
			if err != nil {
				why = err.Error()
			} else if len(ms) > 0 {
				why = ms[0].Status + ": " + ms[0].Note
			}
			res.Skipped++
			if len(res.Reasons) < 10 {
				res.Reasons = append(res.Reasons, r.UCC+" "+why)
			}
			continue
		}
		res.Invited++
	}
	o.log(ctx, tenant, actor, "whatsapp.consent.campaign", channel, map[string]any{"invited": res.Invited, "skipped": res.Skipped})
	return res, nil
}

func newToken() (string, error) {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// ConsentPage is what the consent link shows.
type ConsentPage struct {
	Firm   string
	Name   string
	Mobile string
	Answer string // set once answered
	Error  string
}

// OpenConsent looks up a consent link. Links last 30 days.
func (o *Ops) OpenConsent(ctx context.Context, tenant, token string) (ConsentPage, WAInvite, error) {
	set, _ := o.Settings(ctx, tenant)
	p := ConsentPage{Firm: set.Firm}
	inv, _, err := docs.Get[WAInvite](ctx, o.Docs, tenant, cWAInvite, token)
	if err != nil {
		p.Error = "This link is not valid."
		return p, inv, ErrNotFound
	}
	if inv.Answer != "" {
		p.Answer = inv.Answer
	}
	if t, err := time.Parse(time.RFC3339, inv.Sent); err == nil && o.now().Sub(t) > 30*24*time.Hour && inv.Answer == "" {
		p.Error = "This link has expired. You can turn WhatsApp messages on in the client portal, or message us on WhatsApp."
		return p, inv, ErrState
	}
	c, err := o.Client(ctx, tenant, inv.UCC)
	if err != nil {
		p.Error = "This link is not valid."
		return p, inv, err
	}
	p.Name, p.Mobile = strings.Fields(c.Name + " x")[0], maskMobile(c.Mobile)
	return p, inv, nil
}

// AnswerConsent records the answer given on a consent link (POST only, so
// link scanners that prefetch the page do not answer for the client).
func (o *Ops) AnswerConsent(ctx context.Context, tenant, token string, yes bool) (ConsentPage, error) {
	p, inv, err := o.OpenConsent(ctx, tenant, token)
	if err != nil || inv.Answer != "" {
		return p, err
	}
	ans := map[bool]string{true: "Yes", false: "No"}[yes]
	if _, err := o.recordConsent(ctx, tenant, "client:"+inv.UCC, inv.UCC, yes, SrcLink, "link "+token[:8]+" sent "+inv.Sent[:10]+" by "+inv.Channel); err != nil {
		return p, err
	}
	inv.Answer, inv.At = ans, o.now().Format(time.RFC3339)
	_ = docs.Upsert(ctx, o.Docs, tenant, cWAInvite, token, inv, "client:"+inv.UCC)
	p.Answer = ans
	return p, nil
}

// ConsentHTML renders the consent page.
var ConsentHTML = template.Must(template.New("consent").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>WhatsApp messages from {{.Firm}}</title>
<style>body{font:16px/1.5 system-ui,-apple-system,Segoe UI,sans-serif;background:#f3f4f1;color:#1d2420;margin:0;padding:24px 16px}main{max-width:460px;margin:0 auto;background:#fff;border:1px solid #d9ddd6;border-radius:12px;padding:24px}h1{font-size:20px;margin:0 0 12px}p{margin:0 0 12px}.b{display:flex;gap:10px;flex-wrap:wrap;margin-top:18px}button{font:inherit;padding:10px 16px;border-radius:8px;border:1px solid #9aa39b;background:#fff;cursor:pointer}button.y{background:#1f6b45;border-color:#1f6b45;color:#fff}small{color:#5b655e}</style></head><body><main>
{{if .Error}}<h1>{{.Firm}}</h1><p>{{.Error}}</p>
{{else if eq .Answer "Yes"}}<h1>Thank you, {{.Name}}</h1><p>You will get account messages from {{.Firm}} on WhatsApp at {{.Mobile}}. Reply STOP on WhatsApp at any time to stop them.</p>
{{else if eq .Answer "No"}}<h1>Noted, {{.Name}}</h1><p>We will not send you WhatsApp messages. You will still get contract notes, statements and alerts by email and SMS.</p>
{{else}}<h1>WhatsApp messages from {{.Firm}}</h1>
<p>Dear {{.Name}}, would you like to receive messages about your trading account on WhatsApp at {{.Mobile}}?</p>
<p>These include alerts on funds, margin and contract notes, and information about your own account such as tax on your gains, offers on shares you hold and dates to act by. They are not investment advice.</p>
<form method="post" class="b"><button class="y" name="answer" value="yes">Yes, send me WhatsApp messages</button><button name="answer" value="no">No, thanks</button></form>
<p style="margin-top:16px"><small>You can stop at any time by replying STOP on WhatsApp or from the client portal. Contract notes, statements and regulatory alerts continue by email and SMS either way.</small></p>
{{end}}</main></body></html>`))

// sortConsent orders rows for display: not asked first.
func sortConsent(rows []ConsentRow) {
	rank := map[string]int{"Not asked": 0, "Invited": 1, "Opted out": 2, "Opted in": 3}
	sort.SliceStable(rows, func(i, j int) bool { return rank[rows[i].State] < rank[rows[j].State] })
}
