package ops

import (
	"context"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/control"
	"ledgerline/internal/docs"
)

// Notices from the provider (sent from Ledgerline Control): planned
// maintenance, releases, payment reminders. Each is shown as a banner to
// every staff login, or to the firm's Admins only, between its dates.

const (
	cNotice = "notice"
	// cEditionCtl holds the edition last set from Ledgerline Control.
	cEditionCtl = "editionctl"
)

// ProviderNotice is a notice as kept by the firm.
type ProviderNotice struct {
	control.Notice
	Received  string `json:"received"`
	Withdrawn string `json:"withdrawn,omitempty"`
}

func (o *Ops) applyNotice(ctx context.Context, tenant string, n *control.Notice) error {
	if n == nil || strings.TrimSpace(n.ID) == "" || strings.TrimSpace(n.Title) == "" {
		return invalid("a notice needs an ID and a title")
	}
	if n.Level != "warn" && n.Level != "bad" {
		n.Level = "info"
	}
	if n.Audience != "admins" {
		n.Audience = "all"
	}
	if len(n.Text) > 2000 {
		n.Text = n.Text[:2000]
	}
	p := ProviderNotice{Notice: *n, Received: o.now().Format(time.RFC3339)}
	if err := docs.Upsert(ctx, o.Docs, tenant, cNotice, n.ID, p, "system:control"); err != nil {
		return err
	}
	o.log(ctx, tenant, "system:control", "notice.received", n.ID, map[string]any{"title": n.Title, "audience": n.Audience})
	return nil
}

func (o *Ops) withdrawNotice(ctx context.Context, tenant, id string) error {
	_, err := docs.Update(ctx, o.Docs, tenant, cNotice, id, "system:control", func(p *ProviderNotice) error {
		if p.Withdrawn == "" {
			p.Withdrawn = o.now().Format(time.RFC3339)
		}
		return nil
	})
	if err != nil && strings.Contains(err.Error(), "not found") {
		return nil // never arrived: nothing to take down
	}
	return err
}

// Notices lists the provider's notices showing now for this user.
func (o *Ops) Notices(ctx context.Context, tenant, actor string) ([]control.Notice, error) {
	role, err := o.RoleOf(ctx, tenant, actor)
	if err != nil {
		return nil, err
	}
	out := []control.Notice{}
	if role == RoleClient {
		return out, nil
	}
	all, err := docs.List[ProviderNotice](ctx, o.Docs, tenant, cNotice, "")
	if err != nil {
		return out, err
	}
	now := o.now()
	for _, p := range all {
		if p.Withdrawn != "" || (p.Audience == "admins" && role != RoleAdmin) {
			continue
		}
		if t, err := time.Parse(time.RFC3339, p.From); err == nil && now.Before(t) {
			continue
		}
		if t, err := time.Parse(time.RFC3339, p.Until); err == nil && now.After(t) {
			continue
		}
		out = append(out, p.Notice)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].From > out[j].From })
	return out, nil
}

// applyEdition changes the firm's edition as the provider set it in Control.
func (o *Ops) applyEdition(ctx context.Context, tenant, edition string) error {
	if !validEdition(edition) {
		return invalid("edition must be manual, autopilot or full")
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cEditionCtl, "current", editionRecord{Edition: edition, Since: o.now().Format(time.RFC3339Nano)}, "system:control"); err != nil {
		return err
	}
	if err := o.SetEdition(tenant, edition); err != nil {
		return err
	}
	o.Edition(ctx, tenant) // records the change and its start
	return nil
}

// LoadControlEditions applies editions set from Control (at start, over the
// configured ones): Control is where the provider changes a firm's plan.
func (o *Ops) LoadControlEditions(ctx context.Context, tenants []string) {
	for _, t := range tenants {
		if r, _, err := docs.Get[editionRecord](ctx, o.Docs, t, cEditionCtl, "current"); err == nil && validEdition(r.Edition) {
			_ = o.SetEdition(t, r.Edition)
		}
	}
}

// setupChecklist is the firm's go-live checklist for Control.
func (o *Ops) setupChecklist(ctx context.Context, tenant string, set Settings) []control.SetupItem {
	var out []control.SetupItem
	add := func(key, label string, ok bool, detail string) {
		out = append(out, control.SetupItem{Key: key, Label: label, OK: ok, Detail: detail})
	}
	add("firm", "Firm name and SEBI registration", set.Firm != "" && set.SEBIReg != "", "")
	missing := []string{}
	for _, ex := range []string{"NSE", "BSE"} {
		if strings.TrimSpace(set.MemberCodes[ex]) == "" {
			missing = append(missing, ex)
		}
	}
	add("member_codes", "Exchange member codes", len(missing) == 0, strings.Join(missing, ", ")+map[bool]string{true: "", false: " missing"}[len(missing) == 0])
	if sv, err := o.Signing(ctx, tenant, SystemActor); err == nil {
		it := control.SetupItem{Key: "signing", Label: "Digital signature certificate", OK: sv.Ready && !sv.Test}
		switch {
		case sv.Test:
			it.Detail = "test certificate only"
		case !sv.Ready:
			it.Detail = firstOf(sv.Problem, "not set up")
		default:
			it.Detail, it.Days = sv.Info.Name, sv.ExpiresIn
		}
		out = append(out, it)
	}
	hasMail := o.mailerFor(ctx, tenant) != nil
	add("mail", "Mail server for contract notes", hasMail, map[bool]string{true: "", false: "not set (Settings → Connections)"}[hasMail])
	// The other connections a firm needs before it goes live.
	if cs, err := docs.List[Connection](ctx, o.Docs, tenant, cConn, ""); err == nil {
		got := map[string]Connection{}
		for _, c := range cs {
			got[c.Provider] = c
		}
		for _, p := range ConnProviders {
			if !p.Required || p.ID == "mail" {
				continue
			}
			c, ok := got[p.ID]
			st := "not set"
			if ok && c.Status != "Not set" {
				st = strings.ToLower(c.Status)
				if c.Problem != "" && c.Status == "Problem" {
					st += ": " + c.Problem
				}
			}
			add("conn_"+p.ID, p.Name, ok && (c.Status == "Working" || c.Status == "Saved"), map[bool]string{true: "", false: st}[ok && (c.Status == "Working" || c.Status == "Saved")])
		}
	}
	if bv, err := o.BackupsStatus(ctx, tenant, SystemActor); err == nil {
		d := ""
		if len(bv.Problems) > 0 {
			d = bv.Problems[0]
		} else if bv.LastGood == nil {
			d = "no backup yet"
		}
		add("backups", "Backups", bv.LastGood != nil && len(bv.Problems) == 0, d)
	}
	add("kyc", "Online KYC (DigiLocker plugin)", o.KYC != nil, map[bool]string{true: "", false: "not connected"}[o.KYC != nil])
	if set.ComplianceEmail == "" {
		add("compliance_email", "Compliance officer email", false, "missing")
	} else {
		add("compliance_email", "Compliance officer email", true, "")
	}
	return out
}
