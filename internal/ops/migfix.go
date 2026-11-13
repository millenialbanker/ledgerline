package ops

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"ledgerline/internal/docs"
)

// ---- clients to fix ----
//
// The check lists problems by file and row; the office putting them right
// in the old system works client by client. Every problem of a go-live row
// carries its client's code, name and status in the old system (from the
// client master in the package) and whether the value is missing there or
// only not valid, and MigClientsToFixCSV lists the clients one a row with
// each detail to fix in plain words.
//
// The check also warns where one PAN has more than one active trading code
// for an individual client: the exchanges allow one active code per
// individual client with a trading member (NSE/ISC/48573, June 2021; NRI,
// PMS and institutional clients are exempt).

// migClient is a client of the package's client master, for the check.
type migClient struct {
	Name, Status, PAN, Type, LastTrade string
}

// migRows maps a go-live file's rows by line to their client code, with a
// way to read each row's cells.
type migRows struct {
	ucc  map[int]string
	cell map[int]func(string) string
	cols map[string]bool // lower case
}

// migRowClients reads the go-live files' client code by row, and the
// client master's clients.
func migRowClients(dry []DryFile) (map[string]migRows, map[string]migClient) {
	byFile := map[string]migRows{}
	cs := map[string]migClient{}
	for _, d := range dry {
		h, _ := csv.NewReader(bytes.NewReader(d.Data)).Read()
		cols := map[string]bool{}
		for _, c := range h {
			cols[strings.ToLower(strings.TrimSpace(strings.TrimPrefix(c, "\ufeff")))] = true
		}
		if !cols["ucc"] {
			continue
		}
		rows, _ := parseCSV(d.Data, nil)
		m := migRows{ucc: map[int]string{}, cell: map[int]func(string) string{}, cols: cols}
		for _, r := range rows {
			u := strings.ToUpper(r.get("UCC"))
			if u == "" {
				continue
			}
			m.ucc[r.line], m.cell[r.line] = u, r.get
			if d.Source == "CLIENT-MASTER" {
				st := "Active"
				switch {
				case yes(r.get("DPOnly")) && yes(r.get("Inactive")):
					st = "Inactive (DP only)"
				case yes(r.get("DPOnly")):
					st = "Active (DP only)"
				case yes(r.get("Inactive")):
					st = "Inactive"
				}
				cs[u] = migClient{Name: r.get("Name"), Status: st, PAN: strings.ToUpper(r.get("PAN")), Type: r.get("Type"), LastTrade: r.get("LastTrade")}
			}
		}
		byFile[d.Name] = m
	}
	return byFile, cs
}

// migFieldColumn is the file column a problem's field reads ("bank.ifsc"
// and "banks.0.ifsc" read IFSC).
func migFieldColumn(field string) string {
	last := field
	if i := strings.LastIndex(field, "."); i >= 0 {
		last = field[i+1:]
	}
	if c, ok := map[string]string{"mobile": "Mobile", "email": "Email", "pan": "PAN", "account": "Account", "ifsc": "IFSC", "bank": "Bank", "city": "City",
		"state": "State", "pin": "PIN", "dob": "DOB", "relation": "Relation", "name": "Name", "segments": "Segments"}[strings.ToLower(last)]; ok {
		return c
	}
	return last
}

// tagMigError sets the client of a go-live row's problem, and whether the
// value is missing in the old system.
func tagMigError(e *MigError, rows migRows, cs map[string]migClient) {
	u := rows.ucc[e.Line]
	if u == "" {
		return
	}
	c := cs[u]
	e.UCC, e.Client, e.ClientStatus = u, c.Name, c.Status
	if col := migFieldColumn(e.Field); e.Field != "" && rows.cols[strings.ToLower(col)] && rows.cell[e.Line] != nil {
		e.Blank = strings.TrimSpace(rows.cell[e.Line](col)) == ""
	}
}

// migActiveCodesPerPAN warns on every active trading code of an individual
// client whose PAN has another active trading code in the client master.
func migActiveCodesPerPAN(cs map[string]migClient, rows migRows, add func(line int, ucc, other string)) {
	byPAN := map[string][]string{}
	for u, c := range cs {
		if c.Status == "Active" && c.Type == "Individual" && rePAN.MatchString(c.PAN) {
			byPAN[c.PAN] = append(byPAN[c.PAN], u)
		}
	}
	line := map[string]int{}
	for l, u := range rows.ucc {
		line[u] = l
	}
	for _, us := range byPAN {
		if len(us) < 2 {
			continue
		}
		sort.Strings(us)
		for _, u := range us {
			var other []string
			for _, w := range us {
				if w != u {
					other = append(other, w)
				}
			}
			add(line[u], u, strings.Join(other, ", "))
		}
	}
}

// migDetailLabel names the detail a problem is about, in plain words.
func migDetailLabel(source, field string) string {
	f := strings.ToLower(field)
	last, idx := f, ""
	if i := strings.LastIndex(f, "."); i >= 0 {
		last = f[i+1:]
		if p := strings.Split(f, "."); len(p) == 3 {
			if n, err := strconv.Atoi(p[1]); err == nil {
				idx = strconv.Itoa(n + 1)
			}
		}
	}
	word := map[string]string{"mobile": "mobile number", "email": "email", "pan": "PAN", "account": "account number", "ifsc": "IFSC", "bank": "bank name",
		"city": "city", "state": "state", "pin": "PIN", "permpin": "permanent address PIN", "permstate": "permanent address state", "permcity": "permanent address city",
		"address1": "address", "permaddress1": "permanent address", "dob": "date of birth", "relation": "relationship", "name": "name", "segments": "segments",
		"dematid": "demat account", "boid": "BO ID", "ucc": "client code", "share": "share", "guardian": "guardian", "nominees": "nominees"}[last]
	if word == "" {
		word = field
	}
	var s string
	switch {
	case strings.HasPrefix(f, "bank."):
		s = "primary bank " + word
	case strings.HasPrefix(f, "banks."):
		s = "bank account " + idx + " " + strings.TrimPrefix(word, "account ")
	case strings.HasPrefix(f, "nominees.") && idx != "":
		s = "nominee " + idx + " " + word
	case source == "UCC-DETAILS" && !strings.HasPrefix(last, "perm"):
		s = "address " + word
		if last == "address1" {
			s = "address"
		}
	case source == "UCC-CONTACTS":
		s = "contact persons"
	case source == "DP-ACCOUNTS":
		s = "DP account " + word
	case source == "NOMINEES":
		s = "nominee " + word
	default:
		s = word
	}
	s = strings.Join(strings.Fields(s), " ")
	return strings.ToUpper(s[:1]) + s[1:]
}

// migFixColumns are the yes/blank columns of the clients-to-fix list, for
// filtering in a spreadsheet.
var migFixColumns = []string{"Mobile", "Email", "PAN", "Bank account", "IFSC", "Bank name", "Nominee", "Address", "Demat", "Other"}

func migFixColumn(source, field string) string {
	f := strings.ToLower(field)
	last := f
	if i := strings.LastIndex(f, "."); i >= 0 {
		last = f[i+1:]
	}
	switch {
	case source == "NOMINEES" || strings.HasPrefix(f, "nominees"):
		return "Nominee"
	case last == "mobile":
		return "Mobile"
	case last == "email":
		return "Email"
	case last == "pan":
		return "PAN"
	case last == "account":
		return "Bank account"
	case last == "ifsc":
		return "IFSC"
	case last == "bank":
		return "Bank name"
	case source == "UCC-DETAILS" || last == "city" || last == "state" || last == "pin":
		return "Address"
	case source == "DP-ACCOUNTS" || source == "CLIENT-DEMATS" || last == "dematid":
		return "Demat"
	}
	return "Other"
}

// MigClientsToFixCSV writes the clients with a problem in the package's
// check, one a row: active clients first, then by code. Errors stop the
// load; with warnings, the problems that do not (an inactive client's
// gaps, a shared demat account, several active codes for one PAN) are
// listed too.
func (o *Ops) MigClientsToFixCSV(ctx context.Context, tenant, actor, id string, warnings bool, w io.Writer) error {
	if err := o.require(ctx, tenant, actor, "Checking a migration package", RoleAdmin); err != nil {
		return err
	}
	p, _, err := docs.Get[MigPackage](ctx, o.Docs, tenant, cMigPkg, id)
	if err != nil {
		return fmt.Errorf("%w: no such package", ErrNotFound)
	}
	type fix struct {
		ucc, name, status string
		errs, warns       int
		details, actions  []string
		cols              map[string]bool
	}
	by := map[string]*fix{}
	err = o.eachMigError(ctx, tenant, p, func(e MigError) {
		if e.UCC == "" || (e.Severity == "Warning" && !warnings) {
			return
		}
		f := by[e.UCC]
		if f == nil {
			f = &fix{ucc: e.UCC, name: e.Client, status: firstOf(e.ClientStatus, "Not in the client master"), cols: map[string]bool{}}
			by[e.UCC] = f
		}
		if e.Severity == "Warning" {
			f.warns++
		} else {
			f.errs++
		}
		d := migDetailLabel(e.Source, e.Field)
		if e.Blank {
			d += " missing"
		} else {
			d += ": " + strings.TrimPrefix(strings.TrimPrefix(e.Message, e.UCC+": "), e.UCC+" ")
		}
		if e.Severity == "Warning" {
			d += " (warning)"
		}
		if !contains(f.details, d) {
			f.details = append(f.details, d)
		}
		if !contains(f.actions, e.Action) {
			f.actions = append(f.actions, e.Action)
		}
		f.cols[migFixColumn(e.Source, e.Field)] = true
	})
	if err != nil {
		return err
	}
	list := make([]*fix, 0, len(by))
	for _, f := range by {
		list = append(list, f)
	}
	sort.Slice(list, func(i, j int) bool {
		ai, aj := strings.HasPrefix(list[i].status, "Active"), strings.HasPrefix(list[j].status, "Active")
		if ai != aj {
			return ai
		}
		return list[i].ucc < list[j].ucc
	})
	cw := csv.NewWriter(w)
	cw.Write(append([]string{"Client code", "Client name", "Status in old system", "Errors", "Warnings", "Details to fix", "What to do"}, migFixColumns...))
	for _, f := range list {
		row := []string{f.ucc, f.name, f.status, strconv.Itoa(f.errs), strconv.Itoa(f.warns), strings.Join(f.details, "; "), strings.Join(f.actions, " ")}
		for _, c := range migFixColumns {
			row = append(row, map[bool]string{true: "Yes"}[f.cols[c]])
		}
		cw.Write(row)
	}
	cw.Flush()
	return cw.Error()
}
