package ops

import (
	"context"
	"fmt"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
)

// ---- changing a client's demat accounts (maker-checker) ----
//
// The same path as a bank account change (clientbanks.go, portal.go): the
// client asks from the portal, or staff enter it with the client's signed
// request; a second person approves it; the exchanges' UCC records are then
// sent as a modification (each lists the active accounts, the primary
// first).

// Demat account change actions (ChangeRequest.New["action"]).
const (
	DematAdd        = "add"
	DematDeactivate = "deactivate"
	DematPrimary    = "primary"
	DematPayout     = "payout"
)

func dematChangeAction(r ChangeRequest) string {
	return strings.ToLower(strings.TrimSpace(r.New["action"]))
}

// prepDematChange checks a demat account change as asked and fills in what
// the checker needs: the account in full, its depository, and whether it is
// with the firm's own DP.
func (o *Ops) prepDematChange(ctx context.Context, tenant string, c Client, n map[string]string) error {
	a := strings.ToLower(strings.TrimSpace(n["action"]))
	id := normDematID(n["id"])
	n["action"], n["id"] = a, id
	switch a {
	case DematAdd:
		dep := strings.ToUpper(strings.TrimSpace(n["depository"]))
		if dep == "" {
			dep = dematDepository(id)
		}
		n["depository"] = dep
		if m := dematIDError(dep, id); m != "" {
			return invalid("%s", m)
		}
		if d, ok := dematOf(c, id); ok && d.active() {
			return invalid("%s is already a demat account of %s", id, c.UCC)
		}
		// Another client's account is added only for the same person (the
		// same PAN, the client's other code); the import of the previous
		// system's records is what keeps a joint or family account named
		// by two clients.
		samePAN := func(w string) bool {
			cs, _ := o.ClientMap(ctx, tenant)
			return c.PAN != "" && cs[w].PAN == c.PAN
		}
		if w, ok := o.boClients(ctx, tenant)[id]; ok && w != c.UCC && !samePAN(w) {
			return invalid("%s is the demat account of %s, whose PAN is different; a demat account is added only to its holder's own codes", id, w)
		}
		own := o.firmDP(ctx, tenant).owns(id)
		if b, ok := o.boAccount(ctx, tenant, id); ok {
			// the DP's account of another code of the same person is that
			// code's: here it is a demat account like another DP's
			own = b.UCC == c.UCC
		}
		n["own_dp"] = map[bool]string{true: "Y", false: "N"}[own]
	case DematDeactivate, DematPrimary:
		d, ok := dematOf(c, id)
		switch {
		case !ok:
			return invalid("%s is not a demat account of %s", id, c.UCC)
		case !d.active():
			return invalid("%s is %s: add it again to use it", id, strings.ToLower(d.Status))
		case d.Primary && a == DematDeactivate:
			return invalid("%s is the primary demat account: make another account the primary first", id)
		case d.Primary && a == DematPrimary:
			return invalid("%s is already the primary demat account", id)
		}
	case DematPayout:
		if id != "" {
			if d, ok := dematOf(c, id); !ok || !d.active() {
				return invalid("%s is not an active demat account of %s", id, c.UCC)
			}
		}
	default:
		return invalid("a demat account change is to add or deactivate an account, make one the primary, or choose the account for pay-outs")
	}
	return nil
}

// applyDematChange changes the client's accounts as an approved (or, to
// check it, a pending) demat account change asks.
func applyDematChange(c *Client, r ChangeRequest) error {
	n := r.New
	date := firstOf(dateOnly(r.Decided), dateOnly(r.At))
	if date == "" {
		date = time.Now().Format("2006-01-02")
	}
	id := normDematID(n["id"])
	ds := demats(*c)
	i := indexDemat(ds, id)
	setPrimary := func(k int) {
		for j := range ds {
			ds[j].Primary = j == k
		}
		c.Demat = ds[k].details()
	}
	switch dematChangeAction(r) {
	case DematAdd:
		if i >= 0 && ds[i].active() {
			return invalid("%s is already a demat account of %s", id, c.UCC)
		}
		nd := Demat{Depository: strings.ToUpper(n["depository"]), ID: id, Status: DematActive, AddedOn: date, Change: r.ID, OwnDP: n["own_dp"] == "Y"}
		normDemat(&nd)
		if i >= 0 {
			ds[i] = nd
		} else {
			ds = append(ds, nd)
			i = len(ds) - 1
		}
		if yes(n["primary"]) || primaryDemat(*c).ID == "" {
			setPrimary(i)
		}
	case DematDeactivate:
		switch {
		case i < 0:
			return invalid("%s is not a demat account of %s", id, c.UCC)
		case ds[i].Primary:
			return invalid("%s is the primary demat account: make another account the primary first", id)
		}
		ds[i].Status, ds[i].InactiveOn, ds[i].InactiveWhy, ds[i].Change = DematInactive, date, firstOf(strings.TrimSpace(n["reason"]), "No longer used, at the client's request"), firstOf(r.ID, ds[i].Change)
		if normDematID(c.PayoutDemat) == id {
			c.PayoutDemat = ""
		}
	case DematPrimary:
		if i < 0 || !ds[i].active() {
			return invalid("%s is not an active demat account of %s", id, c.UCC)
		}
		setPrimary(i)
		ds[i].Change = firstOf(r.ID, ds[i].Change)
	case DematPayout:
		c.PayoutDemat = id
	default:
		return invalid("a demat account change is to add or deactivate an account, make one the primary, or choose the account for pay-outs")
	}
	c.Demats = ds
	syncDemats(c)
	collapseDemats(c)
	return nil
}

// dematChangeApproved moves the primary account's holdings when the
// primary changed, and flags the exchanges' UCC records for a modification
// (a DP-only client has none).
func (o *Ops) dematChangeApproved(ctx context.Context, tenant, actor string, r ChangeRequest, before, after Client) []string {
	if b, a := primaryDemat(before).ID, primaryDemat(after).ID; normDematID(b) != normDematID(a) {
		if err := o.moveHoldingsToPrimary(ctx, tenant, actor, after.UCC, b, a); err != nil {
			o.raiseWork(ctx, tenant, "dematchange", r.ID, "Move "+after.UCC+"'s holdings to the new primary demat account", err.Error(), after.UCC, RoleMaker, "clients", "")
		}
	}
	if after.DPOnly || dematChangeAction(r) == DematPayout {
		return nil
	}
	set, _ := o.Settings(ctx, tenant)
	var ex []string
	for _, x := range []string{"NSE", "BSE", "MCX"} {
		if set.tradesOn(x) {
			ex = append(ex, x)
		}
	}
	what := map[string]string{DematAdd: "added a demat account", DematDeactivate: "made a demat account inactive", DematPrimary: "changed the primary demat account"}[dematChangeAction(r)]
	o.raiseWork(ctx, tenant, "uccmod", r.ID, fmt.Sprintf("Send %s's demat account change to the exchanges", after.UCC),
		fmt.Sprintf("%s %s (%s). Upload the UCC modification to %s: the UCC pages list %s as changed; the records carry the %d active demat account(s), primary first.",
			r.ID, what, r.New["id"], strings.Join(ex, ", "), after.UCC, len(activeDemats(after))), after.UCC, RoleMaker, "uccapi", "")
	return ex
}

// ---- migration: CLIENT-DEMATS ----

// prepareClientDemats loads every client's demat accounts, one row per
// account, mostly those with other DPs (the firm's own come with the DP's
// record, DP-ACCOUNTS). The rows replace the client's list, except the
// DP's own accounts of the client, which stay; Primary Y marks the primary
// (none: the client master's demat account stays the primary); Status
// Inactive keeps one no longer used for the record. An inactive client's
// accounts load as the previous system had them, problems as warnings.
func (o *Ops) prepareClientDemats(ctx context.Context, tenant, actor string, rows []row) (func() (int, int, error), []ingest.ParseError) {
	var errs []ingest.ParseError
	bad := func(l int, f, m string, a ...any) {
		errs = append(errs, ingest.ParseError{Line: l, Field: f, Msg: fmt.Sprintf(m, a...)})
	}
	warn := func(l int, f, m string, a ...any) {
		errs = append(errs, ingest.ParseError{Line: l, Field: f, Msg: fmt.Sprintf(m, a...), Warning: true})
	}
	cs, err := o.ClientMap(ctx, tenant)
	if err != nil {
		bad(1, "", "%v", err)
		return nil, errs
	}
	owner := o.boClients(ctx, tenant)
	bos := o.boIndex(ctx, tenant)
	f := o.firmDP(ctx, tenant)
	by := map[string][]Demat{}
	primary := map[string]string{}
	var order []string
	seen, firstIn := map[string]bool{}, map[string]string{}
	for _, r := range rows {
		u := strings.ToUpper(strings.TrimSpace(r.get("UCC")))
		c, ok := cs[u]
		if u == "" {
			bad(r.line, "UCC", "is required")
			continue
		}
		if !ok {
			bad(r.line, "UCC", "%s is not a client; load the client master first", u)
			continue
		}
		lenient := func(field, m string, a ...any) {
			if c.Inactive {
				warn(r.line, field, m+incompleteNote, a...)
				return
			}
			bad(r.line, field, m, a...)
		}
		d := Demat{Depository: strings.ToUpper(strings.TrimSpace(r.get("Depository"))), ID: normDematID(r.get("DematID")), Status: firstOf(strings.TrimSpace(r.get("Status")), DematActive)}
		switch {
		case strings.EqualFold(d.Status, "active"):
			d.Status = DematActive
		case strings.EqualFold(d.Status, "inactive"):
			d.Status = DematInactive
		default:
			lenient("Status", "must be Active or Inactive, got %q", d.Status)
			d.Status = DematInactive
		}
		if d.ID == "" {
			lenient("DematID", "is required")
			continue
		}
		if m := dematIDError(d.Depository, d.ID); m != "" {
			lenient("DematID", "%s, got %q", m, r.get("DematID"))
		}
		// The same account on a row of another client (a joint or family
		// account, or the same person's other code, as the previous back
		// office had it) loads with a warning; the firm's own BO account
		// still has one owner (DP-ACCOUNTS).
		if seen[u+"|"+d.ID] {
			warn(r.line, "DematID", "%s: %s is on an earlier row too; this row is left out", u, d.ID)
			continue
		}
		seen[u+"|"+d.ID] = true
		if w, ok := firstIn[d.ID]; ok && w != u {
			warn(r.line, "DematID", "%s", sharedDematMsg(d.ID, w, cs[w].PAN, c.PAN, true))
		} else if w, ok := owner[d.ID]; ok && w != u {
			warn(r.line, "DematID", "%s", sharedDematMsg(d.ID, w, cs[w].PAN, c.PAN, false))
		}
		if _, ok := firstIn[d.ID]; !ok {
			firstIn[d.ID] = u
		}
		if b, ok := bos.byBOID[d.ID]; ok {
			d.OwnDP = b.UCC == u
		} else if f.owns(d.ID) {
			d.OwnDP = true
			if len(bos.byBOID) > 0 {
				warn(r.line, "DematID", "%s is an account of the firm's DP, but the DP's records (DP-ACCOUNTS) do not have it", d.ID)
			}
		}
		if yn := strings.ToUpper(strings.TrimSpace(r.get("Primary"))); yn == "Y" || yn == "YES" {
			if p, dup := primary[u]; dup {
				bad(r.line, "Primary", "%s already has %s as its primary demat account; a client has one", u, p)
			} else if d.Status != DematActive {
				lenient("Primary", "the primary demat account must be active")
			} else {
				primary[u] = d.ID
			}
		} else if yn != "" && yn != "N" && yn != "NO" {
			bad(r.line, "Primary", "must be Y or N, got %q", yn)
		}
		if by[u] == nil {
			order = append(order, u)
		}
		by[u] = append(by[u], d)
	}
	return func() (int, int, error) {
		n := 0
		for _, u := range order {
			ds := by[u]
			if _, err := o.updateDemats(ctx, tenant, actor, u, func(c *Client) error {
				keepPrimary := primaryDemat(*c).ID
				list := append([]Demat(nil), ds...)
				// The DP's own accounts of the client stay on the list.
				for _, b := range bos.byUCC[u] {
					if indexDemat(list, b.BOID) < 0 {
						list = append(list, Demat{Depository: b.Depository, ID: normDematID(b.BOID), Status: dematStatusOfBO(b.Status), OwnDP: true, AddedOn: b.Opened})
					}
				}
				for i := range list {
					list[i].Primary = false
				}
				c.Demats = list
				switch p := primary[u]; {
				case p != "":
					c.Demat = Demat{Depository: dematDepository(p), ID: p}
				case keepPrimary != "":
					c.Demat = Demat{Depository: dematDepository(keepPrimary), ID: normDematID(keepPrimary)}
					if indexDemat(list, keepPrimary) < 0 {
						// The client master's account, which the file does not
						// list: it stays, as the primary.
						c.Demats = append(c.Demats, Demat{Depository: c.Demat.Depository, ID: c.Demat.ID, Status: DematActive})
					}
				default:
					c.Demat = Demat{}
				}
				for i, d := range c.Demats {
					if normDematID(d.ID) == normDematID(c.Demat.ID) {
						c.Demat.Depository = firstOf(d.Depository, c.Demat.Depository)
						c.Demats[i].Primary = true
					}
				}
				syncDemats(c)
				collapseDemats(c)
				return nil
			}); err != nil {
				return n, 0, err
			}
			n += len(ds)
		}
		if n > 0 {
			o.log(ctx, tenant, actor, "client_demats.loaded", "", map[string]any{"clients": len(order), "accounts": n})
		}
		return n, 0, nil
	}, errs
}

// clientDematDocs lists the demat change requests of a client (for the
// client screen's pending marker).
func (o *Ops) pendingDematChange(ctx context.Context, tenant, ucc string) (ChangeRequest, bool) {
	rs, _ := docs.List[ChangeRequest](ctx, o.Docs, tenant, cChange, "")
	for _, r := range rs {
		if r.UCC == ucc && r.Kind == "Demat account" && r.Status == "Pending" {
			return r.public(), true
		}
	}
	return ChangeRequest{}, false
}
