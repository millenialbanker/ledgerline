// Package migratetest has a fake Querier and a fake Mapper for testing the
// migration tool without SQL Server. All data is made up.
package migratetest

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"ledgerline/internal/migrate"
	"ledgerline/internal/migrate/pkg"
)

// DB is an in-memory database: tables of rows.
type DB struct {
	Name   string
	Tables map[string][]migrate.Row
}

// Query understands "SELECT * FROM <table>" only.
func (d *DB) Query(ctx context.Context, q string, fn func(migrate.Row) error, args ...any) error {
	f := strings.Fields(q)
	if len(f) != 4 || !strings.EqualFold(f[0], "SELECT") || !strings.EqualFold(f[2], "FROM") {
		return fmt.Errorf("fake querier: cannot run %q", q)
	}
	for _, r := range d.Tables[strings.ToLower(f[3])] {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := fn(r); err != nil {
			return err
		}
	}
	return nil
}

func (d *DB) Database() string { return d.Name }

// Company makes a company database for financial year fy ("2026-27") with
// n clients per firm.
func Company(name, fy string, firms []string, n int) *DB {
	d := &DB{Name: name, Tables: map[string][]migrate.Row{"kind": {{"role": "company", "fy": fy}}}}
	for _, f := range firms {
		d.Tables["firms"] = append(d.Tables["firms"], migrate.Row{"code": f, "name": "Made-up Securities " + f})
		for i := 1; i <= n; i++ {
			d.Tables["clients"] = append(d.Tables["clients"], migrate.Row{"firm": f, "ucc": fmt.Sprintf("Z%s%04d", f, i), "name": fmt.Sprintf("Test Client %d", i), "balance": fmt.Sprintf("%d.50", i*1000)})
		}
	}
	return d
}

// Mail makes a mail database with docs documents of size bytes per firm.
func Mail(name, fy string, firms []string, docs, size int) *DB {
	d := &DB{Name: name, Tables: map[string][]migrate.Row{"kind": {{"role": "mail", "fy": fy}}}}
	for _, f := range firms {
		for i := 0; i < docs; i++ {
			typ := pkg.DocContractNote
			if i%3 == 2 {
				typ = pkg.DocStatement
			}
			d.Tables["docs"] = append(d.Tables["docs"], migrate.Row{"firm": f, "id": fmt.Sprintf("D%s-%s-%d", strings.ReplaceAll(fy, "-", ""), f, i), "type": typ, "size": int64(size), "ucc": fmt.Sprintf("Z%s%04d", f, i%5+1)})
		}
	}
	return d
}

// Admin makes an admin database listing the firms.
func Admin(firms []string) *DB {
	d := &DB{Name: "shadmin", Tables: map[string][]migrate.Row{"kind": {{"role": "admin"}}}}
	for _, f := range firms {
		d.Tables["firms"] = append(d.Tables["firms"], migrate.Row{"code": f, "name": "Made-up Securities " + f})
	}
	return d
}

// Mapper is a fake back office.
type Mapper struct {
	FailCurrent  bool
	FailHistory  map[string]bool // by FY
	FailDocs     map[string]bool
	LeaveOpen    map[string]bool // History of this FY returns nil with a file still open
	Block        chan struct{}   // when set, Documents waits for it or the context
	BlockStarted chan struct{}   // closed when Documents starts waiting

	mu     sync.Mutex
	Called []string
}

func (m *Mapper) Name() string { return "Fake" }

func (m *Mapper) call(s string) {
	m.mu.Lock()
	m.Called = append(m.Called, s)
	m.mu.Unlock()
}

func str(r migrate.Row, k string) string {
	if s, ok := r[k].(string); ok {
		return s
	}
	return ""
}

func fyStart(fy string) int {
	n, _ := strconv.Atoi(strings.SplitN(fy, "-", 2)[0])
	return n
}

func (m *Mapper) Classify(ctx context.Context, q migrate.Querier) (migrate.Kind, error) {
	var k migrate.Kind
	found := false
	err := q.Query(ctx, "SELECT * FROM kind", func(r migrate.Row) error {
		found = true
		k.Role, k.FY = str(r, "role"), str(r, "fy")
		return nil
	})
	if err != nil {
		return k, err
	}
	if !found {
		return migrate.Kind{Role: "other", Note: "Not a back office database", Skip: true}, nil
	}
	switch k.Role {
	case "admin":
		k.Note, k.Order = "Administration (firms and users)", 0
	case "company":
		k.Note, k.Order = "Company accounts "+k.FY, 10000-fyStart(k.FY)
	case "mail":
		k.Note, k.Order = "Mail archive "+k.FY, 20000-fyStart(k.FY)
	}
	return k, nil
}

func (m *Mapper) Firms(ctx context.Context, admin, company migrate.Querier) ([]pkg.Firm, error) {
	m.call("Firms")
	src := company
	if admin != nil {
		src = admin
	}
	var out []pkg.Firm
	err := src.Query(ctx, "SELECT * FROM firms", func(r migrate.Row) error {
		out = append(out, pkg.Firm{Code: str(r, "code"), Name: str(r, "name"), Segments: []string{"NSE-CM", "BSE-CM"}})
		return nil
	})
	return out, err
}

func want(plan migrate.Plan, firm string) bool {
	if len(plan.Firms) == 0 {
		return true
	}
	for _, f := range plan.Firms {
		if f == firm {
			return true
		}
	}
	return false
}

func (m *Mapper) Current(ctx context.Context, q migrate.Querier, k migrate.Kind, plan migrate.Plan, w *pkg.Writer, p migrate.Progress) error {
	m.call("Current " + k.FY)
	if m.FailCurrent {
		return errors.New("the client master table is missing")
	}
	p.Step("Client master")
	// as the real mappers do: the firm's balances are as of the newest year's last day
	for _, f := range plan.Firms {
		w.Manifest().Firms = append(w.Manifest().Firms, pkg.Firm{Code: f, AsOf: "2026-10-30"})
	}
	if len(plan.Firms) == 0 {
		w.Manifest().Firms = append(w.Manifest().Firms, pkg.Firm{Code: "01", AsOf: "2026-10-30"}, pkg.Firm{Code: "02", AsOf: "2026-10-30"})
	}
	byFirm := map[string][][]string{}
	var order []string
	total := map[string]int64{}
	err := q.Query(ctx, "SELECT * FROM clients", func(r migrate.Row) error {
		f := str(r, "firm")
		if !want(plan, f) {
			return nil
		}
		if byFirm[f] == nil {
			order = append(order, f)
		}
		byFirm[f] = append(byFirm[f], []string{str(r, "ucc"), str(r, "name"), str(r, "balance")})
		b, _ := strconv.ParseFloat(str(r, "balance"), 64)
		total[f] += int64(b * 100)
		return nil
	})
	if err != nil {
		return err
	}
	for _, f := range order {
		c, err := w.Master(f, "CLIENT-MASTER", []string{"UCC", "Name", "Balance"})
		if err != nil {
			return err
		}
		for i, r := range byFirm[f] {
			if err := c.Write(r); err != nil {
				return err
			}
			p.Count(i+1, len(byFirm[f]))
		}
		if err := c.Close(); err != nil {
			return err
		}
		w.AddCheck(pkg.Check{Firm: f, Name: pkg.CheckClients, Value: strconv.Itoa(len(byFirm[f]))})
		w.AddCheck(pkg.Check{Firm: f, Name: pkg.CheckClientDebit, Value: fmt.Sprintf("%d.%02d", total[f]/100, total[f]%100)})
		w.AddDropped(f, "Client password", "Passwords are never migrated", len(byFirm[f]))
		w.AddIssue(pkg.Issue{Firm: f, Severity: "Warning", Area: "Client master", Key: byFirm[f][0][0], Message: "No PAN on record", Action: "Add the PAN in Ledgerline after loading"})
	}
	return nil
}

func (m *Mapper) History(ctx context.Context, q migrate.Querier, k migrate.Kind, plan migrate.Plan, w *pkg.Writer, p migrate.Progress) error {
	m.call("History " + k.FY)
	if m.FailHistory[k.FY] {
		return errors.New("the ledger table of " + k.FY + " is damaged")
	}
	firms := map[string]bool{}
	var order []string
	q.Query(ctx, "SELECT * FROM clients", func(r migrate.Row) error {
		f := str(r, "firm")
		if want(plan, f) && !firms[f] {
			firms[f] = true
			order = append(order, f)
		}
		return nil
	})
	for _, f := range order {
		c, err := w.History(f, k.FY, pkg.HistoryLedger, pkg.HistoryColumns[pkg.HistoryLedger])
		if err != nil {
			return err
		}
		start := fyStart(k.FY)
		c.Write([]string{fmt.Sprintf("%d-04-01", start), "JV", "1", "CLIENT:Z" + f + "0001", "Test Client 1", "Z" + f + "0001", "NSE-CM", "Opening", "100.00", "", "", "", "made-up"})
		if m.LeaveOpen[k.FY] {
			return nil
		}
		if err := c.Close(); err != nil {
			return err
		}
	}
	return nil
}

// DocData is the made-up content of a document: not compressible, so
// package sizes follow document sizes.
func DocData(id string, size int) []byte {
	b := make([]byte, size)
	x := uint32(2166136261)
	for _, c := range id {
		x = (x ^ uint32(c)) * 16777619
	}
	for i := range b {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		b[i] = byte(x)
	}
	return b
}

func (m *Mapper) Documents(ctx context.Context, q migrate.Querier, k migrate.Kind, plan migrate.Plan, w *pkg.Writer, p migrate.Progress) error {
	m.call("Documents " + k.FY)
	if m.FailDocs[k.FY] {
		return errors.New("the mail table of " + k.FY + " cannot be read")
	}
	if m.Block != nil {
		if m.BlockStarted != nil {
			close(m.BlockStarted)
		}
		select {
		case <-m.Block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	n := 0
	start := fyStart(k.FY)
	err := q.Query(ctx, "SELECT * FROM docs", func(r migrate.Row) error {
		f := str(r, "firm")
		if !want(plan, f) {
			return nil
		}
		size := int(r["size"].(int64))
		n++
		p.Count(n, 0)
		return w.AddDocument(f, pkg.DocMeta{ID: str(r, "id"), UCC: str(r, "ucc"), Type: str(r, "type"), FY: k.FY, Date: time.Date(start, 6, 1, 0, 0, 0, 0, time.UTC), Format: "pdf", Signed: true, Subject: "Contract note", SourceRef: "mail:" + str(r, "id")}, DocData(str(r, "id"), size))
	})
	if err != nil {
		return err
	}
	w.AddCheck(pkg.Check{FY: k.FY, Name: pkg.CheckDocuments, Value: strconv.Itoa(n)})
	return nil
}
