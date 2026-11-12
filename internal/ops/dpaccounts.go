package ops

import (
	"context"
	"fmt"
	"strings"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
)

// ---- DP-ACCOUNTS: the DP's BO accounts, several per client ----

// prepareDPAccounts checks a DP-ACCOUNTS file: one row per BO account, any
// number per client (trading or DP-only), with the primary account and the
// joint holders. Each row is checked as the DP desk checks an account;
// loading the file twice changes nothing.
func (o *Ops) prepareDPAccounts(ctx context.Context, tenant, actor string, rows []row) (func() (int, int, error), []ingest.ParseError) {
	var errs []ingest.ParseError
	bad := func(l int, f, m string, a ...any) {
		errs = append(errs, ingest.ParseError{Line: l, Field: f, Msg: fmt.Sprintf(m, a...)})
	}
	warn := func(l int, f, m string, a ...any) {
		errs = append(errs, ingest.ParseError{Line: l, Field: f, Msg: fmt.Sprintf(m, a...), Warning: true})
	}
	if err := o.require(ctx, tenant, actor, "Loading DP accounts", RoleChecker); err != nil {
		bad(1, "", "%v", err)
		return nil, errs
	}
	cs, err := o.ClientMap(ctx, tenant)
	if err != nil {
		bad(1, "", "%v", err)
		return nil, errs
	}
	// Who each account already belongs to: the DP's records and every
	// client's list (a BO ID belongs to one client).
	owner := o.boClients(ctx, tenant)
	type acct struct {
		b       BOAccount
		line    int
		primary bool
		pgiven  bool
	}
	by := map[string][]*acct{}
	var order []string
	ids := map[string]string{}
	idLine := map[string]int{}
	for _, r := range rows {
		u := strings.ToUpper(strings.TrimSpace(r.get("UCC")))
		b := BOAccount{UCC: u, BOID: normDematID(r.get("BOID")), Depository: strings.ToUpper(strings.TrimSpace(r.get("Depository"))), Opened: r.get("Opened"), ClosedOn: r.get("ClosedOn"),
			FreezeWhy: r.get("FreezeReason")}
		for _, s := range BOStatuses {
			if strings.EqualFold(s, strings.TrimSpace(r.get("Status"))) {
				b.Status = s
			}
		}
		for _, f := range FreezeTypes {
			if strings.EqualFold(f, strings.TrimSpace(r.get("Freeze"))) {
				b.Freeze = f
			}
		}
		b.BSDA, b.DDPI = ynCell(r, "BSDA", false, bad), ynCell(r, "DDPI", false, bad)
		a := &acct{b: b, line: r.line, primary: ynCell(r, "Primary", false, bad), pgiven: strings.TrimSpace(r.get("Primary")) != ""}
		c, isClient := cs[u]
		switch {
		case u == "":
			bad(r.line, "UCC", "is required")
			continue
		case !isClient:
			// A demat account is kept for a client of the firm: a trading
			// client or a DP-only one (DPOnly Y in the client master).
			bad(r.line, "UCC", "%s is not a client; load the client master first (a client of the DP only is loaded there with DPOnly Y)", u)
			continue
		}
		switch b.Depository {
		case "CDSL":
			if !reCDSLBO.MatchString(b.BOID) {
				bad(r.line, "BOID", "a CDSL BO ID is 16 digits, got %q", r.get("BOID"))
			}
		case "NSDL":
			if !reNSDLBO.MatchString(b.BOID) {
				bad(r.line, "BOID", "an NSDL account is IN and 14 digits (DP ID and client ID), got %q", r.get("BOID"))
			}
		default:
			bad(r.line, "Depository", "must be CDSL or NSDL")
		}
		if b.Status == "" {
			bad(r.line, "Status", "must be one of %s", strings.Join(BOStatuses, ", "))
		}
		if v := strings.TrimSpace(r.get("Freeze")); v != "" && b.Freeze == "" {
			bad(r.line, "Freeze", "must be Debit, Credit or Both")
		}
		if b.Status == "Frozen" && (b.Freeze == "" || strings.TrimSpace(b.FreezeWhy) == "") {
			bad(r.line, "Freeze", "a frozen account needs the freeze type (Debit, Credit or Both) and the reason")
		}
		if b.Status != "Frozen" && b.Status != "" && b.Freeze != "" {
			bad(r.line, "Freeze", "only a frozen account has a freeze; the status is %s", b.Status)
		}
		for _, d := range []struct{ v, f string }{{b.Opened, "Opened"}, {b.ClosedOn, "ClosedOn"}} {
			if d.v != "" && !validDate(d.v) {
				bad(r.line, d.f, "must be YYYY-MM-DD, got %q", d.v)
			}
		}
		if b.ClosedOn != "" && b.Status != "Closed" {
			bad(r.line, "ClosedOn", "only a closed account has a closing date")
		}
		if a.primary && (b.Status == "Closed" || b.Status == "Not opened") {
			bad(r.line, "Primary", "the primary demat account must be open; %s is %s", b.BOID, strings.ToLower(b.Status))
		}
		// Joint holders: names, and PANs checked when given (as the
		// previous system had them for an inactive client).
		for _, k := range []string{"2", "3"} {
			n, pan := strings.Join(strings.Fields(r.get("Holder"+k+"Name")), " "), strings.ToUpper(strings.TrimSpace(r.get("Holder"+k+"PAN")))
			if n == "" && pan == "" {
				continue
			}
			if n == "" {
				bad(r.line, "Holder"+k+"Name", "holder %s has a PAN but no name", k)
			}
			if pan != "" && !rePAN.MatchString(pan) {
				if c.Inactive {
					warn(r.line, "Holder"+k+"PAN", "holder %s's PAN %q is not 5 letters, 4 digits, 1 letter%s", k, pan, incompleteNote)
				} else {
					bad(r.line, "Holder"+k+"PAN", "PAN must be 5 letters, 4 digits, 1 letter, got %q", pan)
				}
			}
			if k == "3" && len(a.b.JointHolders) == 0 {
				bad(r.line, "Holder3Name", "give the second holder before the third")
			}
			a.b.JointHolders = append(a.b.JointHolders, JointHolder{Name: n, PAN: pan})
		}
		if b.BOID != "" {
			if o2, dup := ids[b.BOID]; dup {
				if o2 == u {
					bad(r.line, "BOID", "%s is also on line %d", b.BOID, idLine[b.BOID])
				} else {
					bad(r.line, "BOID", "%s is also the account of %s (line %d); a BO ID belongs to one client", b.BOID, o2, idLine[b.BOID])
				}
				continue
			}
			if w, ok := owner[b.BOID]; ok && w != u {
				bad(r.line, "BOID", "%s is already the demat account of %s; a BO ID belongs to one client", b.BOID, w)
			}
			ids[b.BOID], idLine[b.BOID] = u, r.line
		}
		if by[u] == nil {
			order = append(order, u)
		}
		by[u] = append(by[u], a)
	}
	// The primary of each client.
	for _, u := range order {
		as := by[u]
		var marked []*acct
		for _, a := range as {
			if a.primary {
				marked = append(marked, a)
			}
		}
		if len(marked) > 1 {
			bad(marked[1].line, "Primary", "%s has more than one primary demat account (lines %d and %d)", u, marked[0].line, marked[1].line)
			continue
		}
		if len(marked) == 1 {
			continue
		}
		open := func(a *acct) bool { return a.b.Status != "Closed" && a.b.Status != "Not opened" }
		cm := normDematID(cs[u].Demat.ID)
		var pick *acct
		for _, a := range as {
			if open(a) && a.b.BOID == cm {
				pick = a
			}
		}
		for _, a := range as {
			if pick == nil && open(a) {
				pick = a
			}
		}
		if pick != nil {
			pick.primary = true
		}
		if cm != "" && pick != nil && pick.b.BOID != cm {
			if _, inFile := ids[cm]; !inFile {
				warn(pick.line, "BOID", "the client master gives %s's demat account as %s; %s, the DP's account, becomes the primary and %s stays as another account", u, cm, pick.b.BOID, cm)
			}
		}
	}
	// A DP-only client must have an account with the DP: from this file or
	// already on record.
	for _, u := range sortedKeys(cs) {
		c := cs[u]
		if !c.DPOnly || len(by[u]) > 0 {
			continue
		}
		if len(o.boAccountsOf(ctx, tenant, u)) == 0 {
			warn(0, "UCC", "%s is a DP-only client with no demat account with the DP in this file or on record", u)
		}
	}
	as2bo := func(as []*acct) []BOAccount {
		out := make([]BOAccount, len(as))
		for i, a := range as {
			out[i] = a.b
		}
		return out
	}
	return func() (int, int, error) {
		n, same := 0, 0
		for _, u := range order {
			as := by[u]
			changed := false
			var primary string
			for _, a := range as {
				b := a.b
				if a.primary {
					primary = b.BOID
				}
				if old, ok := o.boAccount(ctx, tenant, b.BOID); ok {
					// What CDSL's own files said stays.
					b.CDSL, b.CDSLFreeze, b.CDSLFreezeWhy, b.BSDAOptOut, b.OtherDemat = old.CDSL, old.CDSLFreeze, old.CDSLFreezeWhy, old.BSDAOptOut, old.OtherDemat
					if len(b.JointHolders) == 0 {
						b.JointHolders = old.JointHolders // blank holder cells keep what is stored
					}
					if sameJSON(old, b) {
						same++
						continue
					}
				}
				if err := docs.Upsert(ctx, o.Docs, tenant, cBO, boKey(b), b, actor); err != nil {
					return n, same, err
				}
				changed = true
				n++
			}
			// The client's list: every account of the file, its primary.
			c := cs[u]
			x := c
			x.Demats = append([]Demat(nil), c.Demats...)
			applyDPAccounts(&x, as2bo(as), primary)
			if !changed && x.Demat == c.Demat && sameJSON(x.Demats, c.Demats) {
				continue
			}
			if _, err := o.updateDemats(ctx, tenant, actor, u, func(y *Client) error {
				applyDPAccounts(y, as2bo(as), primary)
				return nil
			}); err != nil {
				return n, same, err
			}
		}
		if n > 0 {
			o.log(ctx, tenant, actor, "dp.accounts_loaded", "", map[string]any{"accounts": n, "clients": len(order)})
		}
		return n, same, nil
	}, errs
}

// applyDPAccounts puts the DP's accounts of a client in its list, with the
// primary given (empty: as the list has it).
func applyDPAccounts(c *Client, bs []BOAccount, primary string) {
	for _, b := range bs {
		ds := demats(*c)
		want := dematStatusOfBO(b.Status)
		if i := indexDemat(ds, b.BOID); i >= 0 {
			ds[i].OwnDP, ds[i].Status = true, want
			if want == DematClosed {
				ds[i].InactiveOn = firstOf(b.ClosedOn, ds[i].InactiveOn)
			}
		} else {
			ds = append(ds, Demat{Depository: firstOf(b.Depository, dematDepository(b.BOID)), ID: normDematID(b.BOID), Status: want, AddedOn: b.Opened, OwnDP: true, InactiveOn: b.ClosedOn})
		}
		c.Demats = ds
	}
	if primary != "" {
		c.Demat = Demat{Depository: dematDepository(primary), ID: normDematID(primary)}
		for _, b := range bs {
			if normDematID(b.BOID) == normDematID(primary) {
				c.Demat.Depository = firstOf(b.Depository, c.Demat.Depository)
			}
		}
	}
	syncDemats(c)
	collapseDemats(c)
}
