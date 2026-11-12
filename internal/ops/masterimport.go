package ops

import (
	"context"
	"fmt"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/money"
)

// ---- go-live imports: offices, partners, exchange UCC details, BO accounts ----
//
// Data the back office holds that another system (SharePro first) also
// keeps, brought across at migration in plain CSV files with fixed columns:
//
//   BRANCHES      the firm's offices and their tree (before the client master)
//   PARTNERS      authorised persons and relationship managers (after BRANCHES)
//   UCC-DETAILS   each client's exchange UCC details (after the client master)
//   UCC-CONTACTS  contact persons of non-individual clients
//   DP-ACCOUNTS   the DP's record of each client's demat accounts
//
// Each is checked with the same rules as its screen (SaveBranch,
// SavePartner, SaveUCCDetails, the DP desk), every row's problems are
// reported at once, and loading a file twice changes nothing.

func importSources() []masterSrc {
	return []masterSrc{
		{"BRANCHES", "The firm's offices (migration: the previous back office's branch master). Code and Name are required; optional City, Manager, Kind (Zone, Region, Branch or Franchise; Branch when blank), Parent (the office it reports to, which may be further down the file) and Active (Y or N, Y when blank). Rows replace the offices with the same code. Load before the partners and the client master", []string{"Code", "Name"}, false, "Once at go-live, then on change"},
		{"PARTNERS", "Authorised persons and relationship managers (migration: the previous back office's sub-broker or AP master). Code, Name and Kind (Authorised person or Relationship manager) are required; optional Branch, Parent (the partner it reports to), PAN, GSTIN, RegNo (exchange AP registration), SharePct (its share of brokerage, 0 to 100), Bank, Account, IFSC, Email and Active (Y or N, Y when blank). Load after the branches and before the client master, whose AP and RM columns name them", []string{"Code", "Name", "Kind"}, false, "Once at go-live, then on change"},
		{"UCC-DETAILS", "Each client's details for the exchanges' UCC that the client master does not hold (migration): address in lines with PIN (6 digits in India), permanent address, gender, father's or spouse's name, marital status, nationality, occupation, PEP, IPV, POA, UPI, agreement date, CIN, bank account type, and whose the mobile and e-mail are. Required columns UCC, Address1, City, State, PIN; optional Category, Address2, Address3, Country, PermAddress1, PermAddress2, PermAddress3, PermCity, PermState, PermCountry, PermPIN, Gender, Guardian, Marital, Nationality, Occupation, OccupationDetail, IncomeAsOn, PEP, IPV, POAFunds, POASecurities, UPI, AgreementDate, CIN, BankType, MobileRelation, EmailRelation. A blank cell leaves what is stored; the client must exist (load after the client master)", []string{"UCC", "Address1", "City", "State", "PIN"}, false, "Once at go-live, then on change"},
		{"UCC-CONTACTS", "Contact persons of non-individual clients for the exchanges' UCC: partners, karta, directors, trustees (migration). UCC, Name and Designation are required; optional PAN, Address, Phone, Email and DIN. A client's rows replace their contact persons, at most 5; the client must exist", []string{"UCC", "Name", "Designation"}, false, "Once at go-live, then on change"},
		{"DP-ACCOUNTS", "The DP's record of each client's demat accounts (migration: the previous back office's DP client master), one row per account: a client may have several, and a DP-only client (DPOnly Y in the client master) has only these. UCC, BOID, Depository (CDSL or NSDL) and Status (Active, Frozen, Closed or Not opened) are required; optional Opened and ClosedOn (dates), BSDA and DDPI (Y or N), Freeze (Debit, Credit or Both) and FreezeReason, both needed for a frozen account; Primary (Y or N: the client's primary demat account; when no row of a client says Y, the client master's demat account if it is one of them and open, else the client's first open row); Holder2Name, Holder2PAN, Holder3Name and Holder3PAN for the second and third holders of a joint account. The UCC must be a client (trading or DP-only; load after the client master) and a BO ID belongs to one client", []string{"UCC", "BOID", "Depository", "Status"}, false, "Once at go-live, then on change"},
	}
}

// ynCell reads an optional Y/N cell (def when blank).
func ynCell(r row, col string, def bool, bad func(l int, f, m string, a ...any)) bool {
	switch v := strings.ToUpper(strings.TrimSpace(r.get(col))); v {
	case "":
		return def
	case "Y", "YES":
		return true
	case "N", "NO":
		return false
	default:
		bad(r.line, col, "must be Y or N, got %q", v)
		return def
	}
}

// inIndia says whether a country cell means India (blank is India).
func inIndia(c string) bool {
	switch strings.ToUpper(strings.TrimSpace(c)) {
	case "", "IN", "IND", "INDIA", "85", "101":
		return true
	}
	return false
}

func (o *Ops) prepareImport(ctx context.Context, tenant, actor, source string, rows []row) (func() (int, int, error), []ingest.ParseError) {
	var errs []ingest.ParseError
	bad := func(l int, f, m string, a ...any) {
		errs = append(errs, ingest.ParseError{Line: l, Field: f, Msg: fmt.Sprintf(m, a...)})
	}
	warn := func(l int, f, m string, a ...any) {
		errs = append(errs, ingest.ParseError{Line: l, Field: f, Msg: fmt.Sprintf(m, a...), Warning: true})
	}
	switch source {
	case "BRANCHES":
		if err := o.require(ctx, tenant, actor, "Managing branches", RoleAdmin, RoleChecker); err != nil {
			bad(1, "", "%v", err)
			return nil, errs
		}
		have, err := o.Branches(ctx, tenant)
		if err != nil {
			bad(1, "", "%v", err)
			return nil, errs
		}
		var bs []Branch
		lines := map[string]int{}
		for _, r := range rows {
			b := Branch{Code: strings.TrimSpace(r.get("Code")), Name: strings.Join(strings.Fields(r.get("Name")), " "), City: r.get("City"), Manager: r.get("Manager"),
				Parent: strings.TrimSpace(r.get("Parent")), Kind: strings.TrimSpace(r.get("Kind")), Active: ynCell(r, "Active", true, bad)}
			for _, k := range BranchKinds {
				if strings.EqualFold(k, b.Kind) {
					b.Kind = k
				}
			}
			if l, dup := lines[b.Code]; dup && b.Code != "" {
				bad(r.line, "Code", "%s is also on line %d", b.Code, l)
			}
			lines[b.Code] = r.line
			bs = append(bs, b)
		}
		// Every office as it will be: the stored ones with the file's on top,
		// so a parent further down the file is found.
		all := append([]Branch{}, bs...)
		for _, h := range have {
			if _, ok := lines[h.Code]; !ok {
				all = append(all, h)
			}
		}
		for i := range bs {
			if err := checkBranch(&bs[i], all); err != nil {
				field := "Code"
				if strings.Contains(err.Error(), "parent") || strings.Contains(err.Error(), "report") {
					field = "Parent"
				} else if strings.Contains(err.Error(), "kind") {
					field = "Kind"
				}
				bad(rows[i].line, field, "%s", strings.TrimPrefix(err.Error(), "invalid: "))
			}
		}
		return func() (int, int, error) {
			n, same := 0, 0
			for _, b := range bs {
				if old, _, err := docs.Get[Branch](ctx, o.Docs, tenant, cBranch, b.Code); err == nil && old == b {
					same++
					continue
				}
				if err := docs.Upsert(ctx, o.Docs, tenant, cBranch, b.Code, b, actor); err != nil {
					return n, same, err
				}
				n++
			}
			if n > 0 {
				o.log(ctx, tenant, actor, "branch.imported", fmt.Sprint(n), map[string]any{"offices": n})
			}
			return n, same, nil
		}, errs

	case "PARTNERS":
		if err := o.require(ctx, tenant, actor, "Managing partners", RoleAdmin, RoleChecker); err != nil {
			bad(1, "", "%v", err)
			return nil, errs
		}
		have, err := docs.List[Partner](ctx, o.Docs, tenant, cPartner, "")
		if err != nil {
			bad(1, "", "%v", err)
			return nil, errs
		}
		var ps []Partner
		lines := map[string]int{}
		parent := map[string]string{}
		for _, h := range have {
			parent[h.Code] = h.Parent
		}
		for _, r := range rows {
			p := Partner{Code: strings.ToUpper(strings.TrimSpace(r.get("Code"))), Name: strings.Join(strings.Fields(r.get("Name")), " "), Branch: strings.TrimSpace(r.get("Branch")),
				Parent: strings.ToUpper(strings.TrimSpace(r.get("Parent"))), PAN: strings.TrimSpace(r.get("PAN")), GSTIN: strings.TrimSpace(r.get("GSTIN")), RegNo: r.get("RegNo"),
				Bank: r.get("Bank"), Account: r.get("Account"), IFSC: strings.ToUpper(r.get("IFSC")), Email: r.get("Email"), Active: ynCell(r, "Active", true, bad)}
			for _, k := range PartnerKinds {
				if strings.EqualFold(k, strings.TrimSpace(r.get("Kind"))) {
					p.Kind = k
				}
			}
			if p.Kind == "" {
				p.Kind = strings.TrimSpace(r.get("Kind"))
			}
			if v := strings.TrimSpace(r.get("SharePct")); v != "" {
				d, err := money.ParseDec(strings.TrimSuffix(v, "%"))
				if err != nil {
					bad(r.line, "SharePct", "must be a number from 0 to 100, got %q", v)
				}
				p.SharePct = d
			}
			if p.IFSC != "" && !reIFSC.MatchString(p.IFSC) {
				bad(r.line, "IFSC", "IFSC is 4 letters, 0, then 6 characters")
			}
			if p.Account != "" && !reAcct.MatchString(p.Account) {
				bad(r.line, "Account", "account number is 9 to 18 digits")
			}
			if p.Email != "" && !reEmail.MatchString(p.Email) {
				bad(r.line, "Email", "is not a valid email address")
			}
			if l, dup := lines[p.Code]; dup && p.Code != "" {
				bad(r.line, "Code", "%s is also on line %d", p.Code, l)
			}
			lines[p.Code] = r.line
			parent[p.Code] = p.Parent
			ps = append(ps, p)
		}
		for i := range ps {
			for _, pr := range checkPartner(&ps[i], parent) {
				bad(rows[i].line, pr[0], "%s", pr[1])
			}
		}
		return func() (int, int, error) {
			n, same := 0, 0
			for _, p := range ps {
				if old, _, err := docs.Get[Partner](ctx, o.Docs, tenant, cPartner, p.Code); err == nil {
					old.Clients, old.Downline, old.Brokerage, old.Team = 0, nil, 0, 0
					if sameJSON(old, p) {
						same++
						continue
					}
				}
				if err := docs.Upsert(ctx, o.Docs, tenant, cPartner, p.Code, p, actor); err != nil {
					return n, same, err
				}
				n++
			}
			if n > 0 {
				o.log(ctx, tenant, actor, "partner.imported", fmt.Sprint(n), map[string]any{"partners": n})
			}
			return n, same, nil
		}, errs

	case "UCC-DETAILS":
		if err := o.require(ctx, tenant, actor, "Editing exchange UCC details", RoleMaker, RoleChecker); err != nil {
			bad(1, "", "%v", err)
			return nil, errs
		}
		cs, err := o.ClientMap(ctx, tenant)
		if err != nil {
			bad(1, "", "%v", err)
			return nil, errs
		}
		type upd struct {
			ucc  string
			cols map[string]string
		}
		var us []upd
		lines := map[string]int{}
		var cols []string
		for c := range (&UCCDetails{}).uccColumns() {
			cols = append(cols, c)
		}
		gaps := map[string][]string{} // inactive clients' details kept incomplete
		for _, r := range rows {
			u := strings.ToUpper(strings.TrimSpace(r.get("UCC")))
			cl, ok := cs[u]
			if !ok {
				bad(r.line, "UCC", "%s is not a client; load the client master first", u)
				continue
			}
			if l, dup := lines[u]; dup {
				bad(r.line, "UCC", "%s is also on line %d", u, l)
				continue
			}
			lines[u] = r.line
			x := upd{ucc: u, cols: map[string]string{}}
			for _, c := range cols {
				if v := strings.TrimSpace(r.get(c)); v != "" {
					x.cols[c] = v
				}
			}
			// The details as they will be, checked as the screen checks them.
			d := o.UCCDetailsFor(ctx, tenant, u)
			fs := d.uccColumns()
			for c, v := range x.cols {
				*fs[c] = v
			}
			d.normalise()
			// An inactive client's details load as the previous system had
			// them, each problem a warning (incomplete.go).
			for _, pr := range d.issues() {
				if cl.Inactive {
					warn(r.line, pr[0], "%s%s", pr[1], incompleteNote)
					gaps[u] = append(gaps[u], uccGapLabel(pr[0]))
					continue
				}
				bad(r.line, pr[0], "%s", pr[1])
			}
			us = append(us, x)
		}
		return func() (int, int, error) {
			n := 0
			for _, x := range us {
				d := o.UCCDetailsFor(ctx, tenant, x.ucc)
				fs := d.uccColumns()
				for c, v := range x.cols {
					*fs[c] = v
				}
				d.normalise()
				d.UCC, d.UpdatedBy, d.UpdatedAt = x.ucc, actor, o.now().Format(time.RFC3339)
				if err := docs.Upsert(ctx, o.Docs, tenant, cUCCDetails, x.ucc, d, actor); err != nil {
					return n, 0, err
				}
				if len(gaps[x.ucc]) > 0 || len(cs[x.ucc].Incomplete) > 0 {
					if err := o.noteIncomplete(ctx, tenant, actor, x.ucc, &d, gaps[x.ucc]...); err != nil {
						return n, 0, err
					}
				}
				n++
			}
			return n, 0, nil
		}, errs

	case "UCC-CONTACTS":
		if err := o.require(ctx, tenant, actor, "Editing exchange UCC details", RoleMaker, RoleChecker); err != nil {
			bad(1, "", "%v", err)
			return nil, errs
		}
		cs, err := o.ClientMap(ctx, tenant)
		if err != nil {
			bad(1, "", "%v", err)
			return nil, errs
		}
		by := map[string][]UCCContact{}
		var order []string
		gaps := map[string]bool{} // inactive clients' contacts kept incomplete
		for _, r := range rows {
			u := strings.ToUpper(strings.TrimSpace(r.get("UCC")))
			cl, ok := cs[u]
			if !ok {
				bad(r.line, "UCC", "%s is not a client; load the client master first", u)
				continue
			}
			c := UCCContact{Name: r.get("Name"), Designation: r.get("Designation"), PAN: strings.ToUpper(r.get("PAN")), Address: r.get("Address"), Phone: r.get("Phone"), Email: r.get("Email"), DIN: strings.ToUpper(r.get("DIN"))}
			if cl.Inactive && strings.TrimSpace(c.Name+c.Designation+c.PAN) == "" {
				// Nothing to keep a contact person by (incomplete.go).
				warn(r.line, "Name", "%s: the row has no name, designation or PAN; it is left out", u)
				continue
			}
			for _, pr := range contactIssues(c) {
				if cl.Inactive {
					warn(r.line, pr[0], "%s%s", pr[1], incompleteNote)
					gaps[u] = true
					continue
				}
				bad(r.line, pr[0], "%s", pr[1])
			}
			if by[u] == nil {
				order = append(order, u)
			}
			by[u] = append(by[u], c)
			if len(by[u]) == maxUCCContacts+1 {
				bad(r.line, "UCC", "%s has more than %d contact persons; the exchanges take at most %d", u, maxUCCContacts, maxUCCContacts)
			}
		}
		return func() (int, int, error) {
			n := 0
			for _, u := range order {
				d := o.UCCDetailsFor(ctx, tenant, u)
				d.Contacts = by[u]
				d.normalise()
				d.UCC, d.UpdatedBy, d.UpdatedAt = u, actor, o.now().Format(time.RFC3339)
				if err := docs.Upsert(ctx, o.Docs, tenant, cUCCDetails, u, d, actor); err != nil {
					return n, 0, err
				}
				if gaps[u] || len(cs[u].Incomplete) > 0 {
					var add []string
					if gaps[u] {
						add = []string{"Contact persons"}
					}
					if err := o.noteIncomplete(ctx, tenant, actor, u, &d, add...); err != nil {
						return n, 0, err
					}
				}
				n += len(by[u])
			}
			return n, 0, nil
		}, errs

	case "DP-ACCOUNTS":
		return o.prepareDPAccounts(ctx, tenant, actor, rows)
	}
	bad(1, "", "no loader for %s", source)
	return nil, errs
}
