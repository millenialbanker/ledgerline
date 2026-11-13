package ops

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
	"ledgerline/internal/migrate/pkg"
	"ledgerline/internal/money"
	"ledgerline/internal/pdf"
)

// ---- migration: Ledgerline's figures against the old system's ----
//
// The package carries totals the old system shows (Manifest.Checks): the
// trial balance, client counts and balances, DP holdings, documents. After
// a load Ledgerline works out the same figures from what it now holds and
// shows each side by side. A failed line lists the rows behind it where it
// can: the accounts, clients or holdings whose figure in the loaded file
// differs from Ledgerline's.

// MigRecon is a reconciliation of a migration.
type MigRecon struct {
	At      time.Time     `json:"at"`
	By      string        `json:"by"`
	AsOf    string        `json:"as_of"`
	Rows    []MigReconRow `json:"rows"`
	Passed  int           `json:"passed"`
	Failed  int           `json:"failed"`
	Unknown int           `json:"unknown"`
}

// MigReconRow is one check.
type MigReconRow struct {
	Name       string         `json:"name"`
	Label      string         `json:"label"`
	FY         string         `json:"fy,omitempty"`
	Detail     string         `json:"detail,omitempty"`
	Expected   string         `json:"expected"`
	Ledgerline string         `json:"ledgerline"`
	Difference string         `json:"difference"`
	Status     string         `json:"status"` // Pass, Fail, Not checked
	Behind     []MigReconDiff `json:"behind,omitempty"`
	More       int            `json:"more,omitempty"` // rows behind not listed
}

// MigReconDiff is one row behind a failed check.
type MigReconDiff struct {
	Key        string `json:"key"`
	Expected   string `json:"expected"`
	Ledgerline string `json:"ledgerline"`
}

var reconLabels = map[string]string{
	pkg.CheckTBDebit: "Trial balance: total debits", pkg.CheckTBCredit: "Trial balance: total credits", pkg.CheckClients: "Clients",
	pkg.CheckClientDebit: "Clients in debit: total", pkg.CheckClientCredit: "Clients in credit: total", pkg.CheckDPAccounts: "Demat accounts",
	pkg.CheckDPQuantity: "Demat holdings: total quantity", pkg.CheckDocuments: "Archived documents", pkg.CheckBankAccounts: "Client bank accounts",
	pkg.CheckPositions: "Open positions", pkg.CheckOpeningDifference: "Difference in opening balances (to be confirmed by the accountant)",
	pkg.CheckDPTariffVersions: "DP tariff plan versions", pkg.CheckDPTariffAccounts: "DP accounts on a tariff plan", pkg.CheckDPAMCPaidAhead: "DP accounts with maintenance paid ahead",
}

var reconMoney = map[string]bool{pkg.CheckTBDebit: true, pkg.CheckTBCredit: true, pkg.CheckClientDebit: true, pkg.CheckClientCredit: true, pkg.CheckOpeningDifference: true}

const reconBehindMax = 200

// openingDiffAccount holds a difference the old system's opening
// balances carried, until the firm's accountant confirms it.
const openingDiffAccount = "SUSPENSE:OPENING-DIFFERENCE"

// ReconcileMigration works the figures out again and stores the result.
func (o *Ops) ReconcileMigration(ctx context.Context, tenant, actor, id string) (Migration, error) {
	if err := o.require(ctx, tenant, actor, "Reconciling a migration", RoleChecker, RoleCompliance); err != nil {
		return Migration{}, err
	}
	m, _, err := docs.Get[Migration](ctx, o.Docs, tenant, cMig, id)
	if err != nil {
		return m, fmt.Errorf("%w: no such migration", ErrNotFound)
	}
	if m.Status != MigLoaded && m.Status != MigUndoProposed {
		return m, fmt.Errorf("%w: migration %s is %s; only a loaded migration is reconciled", ErrState, id, strings.ToLower(m.Status))
	}
	rc, err := o.reconcile(ctx, tenant, actor, &m)
	if err != nil {
		return m, err
	}
	m.Recon = rc
	if err := o.saveMig(ctx, tenant, actor, &m); err != nil {
		return m, err
	}
	o.log(ctx, tenant, actor, "migration.reconciled", id, map[string]any{"passed": rc.Passed, "failed": rc.Failed})
	return m, nil
}

// rawRows reads back a go-live file the migration loaded.
func (o *Ops) rawRows(ctx context.Context, tenant string, m *Migration, source string, cols []string) []row {
	for _, st := range m.Steps {
		if st.Kind == StepMaster && st.Source == source && st.RawID != "" {
			if rf, err := o.App.Ingest.Raw.Get(ctx, tenant, st.RawID); err == nil {
				rows, _ := parseCSV(rf.Content, cols)
				return rows
			}
		}
	}
	return nil
}

func (o *Ops) stepDate(m *Migration, source string) string {
	for _, st := range m.Steps {
		if st.Kind == StepMaster && st.Source == source {
			return st.Date
		}
	}
	return ""
}

func (o *Ops) reconcile(ctx context.Context, tenant, actor string, m *Migration) (*MigRecon, error) {
	rc := &MigRecon{At: o.now(), By: actor, AsOf: m.AsOf, Rows: []MigReconRow{}}
	var checks []pkg.Check
	seen := map[string]bool{}
	for _, id := range m.Packages {
		p, _, err := docs.Get[MigPackage](ctx, o.Docs, tenant, cMigPkg, id)
		if err != nil || p.Manifest == nil {
			continue
		}
		for _, c := range p.Manifest.Checks {
			k := c.Firm + "|" + c.FY + "|" + c.Name + "|" + c.Detail
			if (c.Firm == "" || c.Firm == m.Firm) && !seen[k] {
				seen[k] = true
				checks = append(checks, c)
			}
		}
	}
	// Ledgerline's side, worked out once.
	bal, err := o.App.Ledger.Balances(ctx, tenant, m.AsOf)
	if err != nil {
		return nil, err
	}
	clients, err := o.Clients(ctx, tenant)
	if err != nil {
		return nil, err
	}
	for _, c := range checks {
		r := MigReconRow{Name: c.Name, Label: firstOf(reconLabels[c.Name], c.Name), FY: c.FY, Detail: c.Detail, Expected: c.Value}
		got, behind, known := o.reconFigure(ctx, tenant, m, c, bal, clients)
		if !known {
			r.Status, r.Ledgerline, r.Difference = "Not checked", "", ""
			rc.Unknown++
			rc.Rows = append(rc.Rows, r)
			continue
		}
		r.Ledgerline = got
		diff, same := reconDiff(c.Name, c.Value, got)
		r.Difference = diff
		if same {
			r.Status = "Pass"
			rc.Passed++
		} else {
			r.Status = "Fail"
			rc.Failed++
			if len(behind) > reconBehindMax {
				r.More = len(behind) - reconBehindMax
				behind = behind[:reconBehindMax]
			}
			r.Behind = behind
		}
		rc.Rows = append(rc.Rows, r)
	}
	return rc, nil
}

func reconDiff(name, exp, got string) (string, bool) {
	clean := func(s string) string { return strings.ReplaceAll(strings.TrimSpace(s), ",", "") }
	if reconMoney[name] {
		e, err1 := money.ParseAmount(clean(exp))
		g, err2 := money.ParseAmount(clean(got))
		if err1 != nil || err2 != nil {
			return "", false
		}
		return (g - e).String(), g == e
	}
	e, err1 := strconv.ParseInt(clean(exp), 10, 64)
	g, err2 := strconv.ParseInt(clean(got), 10, 64)
	if err1 != nil || err2 != nil {
		ed, err1 := money.ParseDec(clean(exp))
		gd, err2 := money.ParseDec(clean(got))
		if err1 != nil || err2 != nil {
			return "", false
		}
		return gd.Sub(ed).String(), gd.Cmp(ed) == 0
	}
	return strconv.FormatInt(g-e, 10), g == e
}

// reconFigure is Ledgerline's figure for one check and, where it can say,
// the rows that differ from the loaded file. known is false for a check
// Ledgerline does not know.
func (o *Ops) reconFigure(ctx context.Context, tenant string, m *Migration, c pkg.Check, bal map[string]money.Amount, clients []Client) (string, []MigReconDiff, bool) {
	var behind []MigReconDiff
	fileBal := func() map[string]money.Amount {
		out := map[string]money.Amount{}
		for _, r := range o.rawRows(ctx, tenant, m, "OPENING-BALANCES", []string{"Account", "Debit", "Credit"}) {
			a := strings.ToUpper(r.get("Account"))
			dr, _ := money.ParseAmount(r.get("Debit"))
			cr, _ := money.ParseAmount(r.get("Credit"))
			out[a] += dr - cr
		}
		return out
	}
	accountDiffs := func(only func(string) bool) {
		fb := fileBal()
		keys := map[string]bool{}
		for k := range fb {
			keys[k] = true
		}
		for k := range bal {
			keys[k] = true
		}
		for _, k := range sortedKeys(keys) {
			if only(k) && fb[k] != bal[k] {
				behind = append(behind, MigReconDiff{Key: k, Expected: drcr(fb[k]), Ledgerline: drcr(bal[k])})
			}
		}
	}
	switch c.Name {
	case pkg.CheckTBDebit, pkg.CheckTBCredit:
		var dr, cr money.Amount
		for _, b := range bal {
			if b > 0 {
				dr += b
			} else {
				cr -= b
			}
		}
		accountDiffs(func(string) bool { return true })
		if c.Name == pkg.CheckTBDebit {
			return dr.String(), behind, true
		}
		return cr.String(), behind, true
	case pkg.CheckClientDebit, pkg.CheckClientCredit:
		var dr, cr money.Amount
		for a, b := range bal {
			if ledger.IsClientAccount(a) {
				if b > 0 {
					dr += b
				} else {
					cr -= b
				}
			}
		}
		accountDiffs(ledger.IsClientAccount)
		if c.Name == pkg.CheckClientDebit {
			return dr.String(), behind, true
		}
		return cr.String(), behind, true
	case pkg.CheckOpeningDifference:
		// the balance of the old system's unexplained opening difference,
		// debit positive, as the migration tool wrote it
		return bal[openingDiffAccount].String(), nil, true
	case pkg.CheckClients:
		have := map[string]bool{}
		for _, cl := range clients {
			have[cl.UCC] = true
		}
		file := map[string]bool{}
		for _, r := range o.rawRows(ctx, tenant, m, "CLIENT-MASTER", []string{"UCC"}) {
			u := strings.ToUpper(r.get("UCC"))
			file[u] = true
			if !have[u] {
				behind = append(behind, MigReconDiff{Key: u, Expected: "in the client master", Ledgerline: "missing"})
			}
		}
		for _, u := range sortedKeys(have) {
			if len(file) > 0 && !file[u] {
				behind = append(behind, MigReconDiff{Key: u, Expected: "not in the client master", Ledgerline: "present"})
			}
		}
		return strconv.Itoa(len(clients)), behind, true
	case pkg.CheckDPAccounts:
		// The DP's own records when DP-ACCOUNTS was loaded: every BO
		// account of every client (trading or DP-only), whatever its status,
		// several per client where a client has them; else the clients with
		// a demat account in the client master.
		if bos := o.boAccounts(ctx, tenant); len(bos) > 0 {
			have := map[string]string{}
			for _, b := range bos {
				have[normDematID(b.BOID)] = b.UCC
			}
			for _, r := range o.rawRows(ctx, tenant, m, "DP-ACCOUNTS", []string{"UCC", "BOID"}) {
				u, bo := strings.ToUpper(r.get("UCC")), normDematID(r.get("BOID"))
				switch owner, ok := have[bo]; {
				case !ok:
					behind = append(behind, MigReconDiff{Key: u + " " + bo, Expected: "in DP-ACCOUNTS", Ledgerline: "no DP record (not a client here)"})
				case owner != u:
					behind = append(behind, MigReconDiff{Key: u + " " + bo, Expected: u, Ledgerline: "the account of " + owner})
				}
			}
			return strconv.Itoa(len(bos)), behind, true
		}
		n := 0
		have := map[string]bool{}
		for _, cl := range clients {
			if ds := demats(cl); len(ds) > 0 {
				n += len(ds)
				have[cl.UCC] = true
			}
		}
		for _, r := range o.rawRows(ctx, tenant, m, "CLIENT-MASTER", []string{"UCC", "DematID"}) {
			if u := strings.ToUpper(r.get("UCC")); r.get("DematID") != "" && !have[u] {
				behind = append(behind, MigReconDiff{Key: u, Expected: r.get("DematID"), Ledgerline: "no demat account"})
			}
		}
		return strconv.Itoa(n), behind, true
	case pkg.CheckDPQuantity:
		hs, err := docs.List[Holding](ctx, o.Docs, tenant, cHolding, "")
		if err != nil {
			return "", nil, false
		}
		// Every holding of every account, exactly (units may be fractional).
		var q money.Dec
		got := map[string]money.Dec{}
		for _, h := range hs {
			q = q.Add(h.Qty)
			k := h.UCC + " " + h.ISIN
			got[k] = got[k].Add(h.Qty)
		}
		want := map[string]money.Dec{}
		for _, r := range o.rawRows(ctx, tenant, m, "DP-HOLDINGS", []string{"UCC", "ISIN", "Qty"}) {
			n, _ := money.ParseDec(firstOf(strings.TrimSpace(r.get("Qty")), "0"))
			k := strings.ToUpper(r.get("UCC")) + " " + strings.ToUpper(r.get("ISIN"))
			want[k] = want[k].Add(n)
		}
		if len(want) > 0 {
			keys := map[string]bool{}
			for k := range want {
				keys[k] = true
			}
			for k := range got {
				keys[k] = true
			}
			for _, k := range sortedKeys(keys) {
				if want[k].Cmp(got[k]) != 0 {
					behind = append(behind, MigReconDiff{Key: k, Expected: groupINDec(want[k]), Ledgerline: groupINDec(got[k])})
				}
			}
		}
		return q.String(), behind, true
	case pkg.CheckDocuments:
		n := 0
		per := map[string]int{}
		for _, st := range m.Steps {
			for fy, k := range st.Counts {
				per[fy] += k
				if c.FY == "" || c.FY == fy {
					n += k
				}
			}
		}
		for _, id := range m.Packages {
			if p, _, err := docs.Get[MigPackage](ctx, o.Docs, tenant, cMigPkg, id); err == nil && p.Manifest != nil {
				for _, f := range p.Manifest.Files {
					if f.Kind == pkg.KindDocuments && f.Firm == m.Firm && (c.FY == "" || c.FY == f.FY) && f.Rows != per[f.FY] && contains(m.Files, f.Path) {
						behind = append(behind, MigReconDiff{Key: "Documents " + f.FY, Expected: groupIN(int64(f.Rows)), Ledgerline: groupIN(int64(per[f.FY]))})
					}
				}
			}
		}
		return strconv.Itoa(n), behind, true
	case pkg.CheckBankAccounts:
		n := 0
		got := map[string]int{}
		// Every registered account counts, active or not, as the old
		// system's figure does: accounts can load inactive (closed in the
		// old system, over the firm's limit, or with an invalid number).
		for _, cl := range clients {
			n += len(cl.Banks)
			got[cl.UCC] += len(cl.Banks)
		}
		want := map[string]int{}
		for _, r := range o.rawRows(ctx, tenant, m, "CLIENT-BANKS", []string{"UCC"}) {
			want[strings.ToUpper(r.get("UCC"))]++
		}
		if len(want) > 0 {
			for _, u := range sortedKeys(want) {
				if want[u] != got[u] {
					behind = append(behind, MigReconDiff{Key: u, Expected: strconv.Itoa(want[u]), Ledgerline: strconv.Itoa(got[u])})
				}
			}
		}
		return strconv.Itoa(n), behind, true
	case pkg.CheckPositions:
		d := o.stepDate(m, "POSITIONS")
		if d == "" {
			return "0", nil, true
		}
		ps, err := docs.List[Position](ctx, o.Docs, tenant, cPosition, d+"|")
		if err != nil {
			return "", nil, false
		}
		have := map[string]bool{}
		for _, p := range ps {
			have[p.UCC+" "+p.Exchange+"-"+p.Segment+" "+strings.ToUpper(strings.Split(p.Instrument, "|")[0])] = true
		}
		for _, r := range o.rawRows(ctx, tenant, m, "POSITIONS", []string{"UCC", "Exchange", "Segment", "Symbol"}) {
			k := strings.ToUpper(r.get("UCC")) + " " + strings.ToUpper(r.get("Exchange")) + "-" + strings.ToUpper(r.get("Segment")) + " " + strings.ToUpper(r.get("Symbol"))
			if !have[k] {
				behind = append(behind, MigReconDiff{Key: k, Expected: "open", Ledgerline: "missing"})
			}
		}
		return strconv.Itoa(len(ps)), behind, true
	case pkg.CheckDPTariffVersions:
		ps, err := o.DPPlans(ctx, tenant)
		if err != nil {
			return "", nil, false
		}
		have := map[string]bool{}
		n := 0
		for _, p := range ps {
			for _, v := range p.Versions {
				have[p.Code+" "+v.From] = true
				n++
			}
		}
		file := map[string]bool{}
		for _, r := range o.rawRows(ctx, tenant, m, "DP-TARIFFS", []string{"Plan", "From"}) {
			if k := strings.ToUpper(r.get("Plan")) + " " + r.get("From"); !file[k] {
				file[k] = true
				if !have[k] {
					behind = append(behind, MigReconDiff{Key: k, Expected: "in DP-TARIFFS", Ledgerline: "missing"})
				}
			}
		}
		return strconv.Itoa(n), behind, true
	case pkg.CheckDPTariffAccounts, pkg.CheckDPAMCPaidAhead:
		n := 0
		for _, b := range o.boAccounts(ctx, tenant) {
			if c.Name == pkg.CheckDPTariffAccounts && b.planAt(m.AsOf) != "" || c.Name == pkg.CheckDPAMCPaidAhead && b.AMCPaidUpto > m.AsOf {
				n++
			}
		}
		return strconv.Itoa(n), behind, true
	}
	return "", nil, false
}

func drcr(a money.Amount) string {
	switch {
	case a > 0:
		return a.String() + " Dr"
	case a < 0:
		return (-a).String() + " Cr"
	}
	return "0.00"
}

// MigReconCSV writes a migration's reconciliation, with the rows behind
// each failed check.
func (o *Ops) MigReconCSV(ctx context.Context, tenant, actor, id string, w io.Writer) error {
	m, err := o.GetMigration(ctx, tenant, actor, id)
	if err != nil {
		return err
	}
	if m.Recon == nil {
		return fmt.Errorf("%w: migration %s has not been reconciled", ErrState, id)
	}
	cw := csv.NewWriter(w)
	cw.Write([]string{"Migration " + m.ID + " · " + m.System + " firm " + m.Firm + " · as of " + m.AsOf + " · reconciled " + m.Recon.At.Format("2006-01-02 15:04") + " by " + m.Recon.By})
	cw.Write([]string{"Check", "Year", "Detail", "Expected (old system)", "Ledgerline", "Difference", "Result", "Row", "Row expected", "Row in Ledgerline"})
	for _, r := range m.Recon.Rows {
		cw.Write([]string{r.Label, r.FY, r.Detail, r.Expected, r.Ledgerline, r.Difference, r.Status, "", "", ""})
		for _, b := range r.Behind {
			cw.Write([]string{r.Label, r.FY, "", "", "", "", "", b.Key, b.Expected, b.Ledgerline})
		}
		if r.More > 0 {
			cw.Write([]string{r.Label, r.FY, "", "", "", "", "", fmt.Sprintf("and %d more", r.More), "", ""})
		}
	}
	cw.Flush()
	return cw.Error()
}

// MigReconPDF lays the reconciliation out with the firm's report header.
func (o *Ops) MigReconPDF(ctx context.Context, tenant, actor, id string) ([]byte, error) {
	m, err := o.GetMigration(ctx, tenant, actor, id)
	if err != nil {
		return nil, err
	}
	if m.Recon == nil {
		return nil, fmt.Errorf("%w: migration %s has not been reconciled", ErrState, id)
	}
	set, _ := o.Settings(ctx, tenant)
	rep := pdf.Report{Firm: firmLines(set), Title: "Migration reconciliation", Landscape: true,
		Meta:  []string{"Migration " + m.ID, "Balances as of " + m.AsOf, "Reconciled " + m.Recon.At.In(ist).Format("02-Jan-2006 15:04") + " by " + m.Recon.By},
		Party: []string{"From " + m.System + ", firm " + m.Firm + " " + m.FirmName},
		Summary: [][2]string{{"Checks passed", strconv.Itoa(m.Recon.Passed)}, {"Checks failed", strconv.Itoa(m.Recon.Failed)}, {"Not checked by Ledgerline", strconv.Itoa(m.Recon.Unknown)},
			{"Proposed by", m.ProposedBy}, {"Approved by", m.DecidedBy}},
		Footer: "Expected figures are the old system's, as the migration tool read them. Ledgerline's are worked out from what was loaded."}
	t := pdf.Table{Title: "Checks", Cols: []pdf.Col{{Label: "Check", Weight: 2.4}, {Label: "Year"}, {Label: "Expected", Right: true, Weight: 1.4}, {Label: "Ledgerline", Right: true, Weight: 1.4}, {Label: "Difference", Right: true, Weight: 1.2}, {Label: "Result"}}}
	for _, r := range m.Recon.Rows {
		t.Rows = append(t.Rows, []string{r.Label, r.FY, r.Expected, r.Ledgerline, r.Difference, r.Status})
	}
	rep.Sections = append(rep.Sections, t)
	for _, r := range m.Recon.Rows {
		if len(r.Behind) == 0 {
			continue
		}
		bt := pdf.Table{Title: r.Label + ": rows that differ", Cols: []pdf.Col{{Label: "Account, client or holding", Weight: 2.4}, {Label: "Expected", Right: true}, {Label: "Ledgerline", Right: true}}}
		for _, b := range r.Behind {
			bt.Rows = append(bt.Rows, []string{b.Key, b.Expected, b.Ledgerline})
		}
		if r.More > 0 {
			bt.Note = fmt.Sprintf("and %d more; the CSV download lists %d", r.More, reconBehindMax)
		}
		rep.Sections = append(rep.Sections, bt)
	}
	return pdf.Render(rep).Bytes(pdf.Meta{Title: "Migration reconciliation " + m.ID, Author: set.Firm, Created: o.now()}, nil)
}
