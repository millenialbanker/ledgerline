//go:build integration && pgx

package pgstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"ledgerline"
	"ledgerline/internal/app"
	"ledgerline/internal/charges"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ops"
	"ledgerline/internal/sample"
	"ledgerline/internal/vault"
)

// TestIntegrationOpsParity runs the generated sample day through every
// operations module twice, once on in-memory stores and once on PostgreSQL,
// and requires the two to agree on every figure. The PostgreSQL tenant
// (LEDGERLINE_TEST_OPS_TENANT) must exist and be empty.
func TestIntegrationOpsParity(t *testing.T) {
	dsn := os.Getenv("LEDGERLINE_TEST_DB")
	tenant := os.Getenv("LEDGERLINE_TEST_OPS_TENANT")
	if dsn == "" || tenant == "" {
		t.Skip("set LEDGERLINE_TEST_DB and LEDGERLINE_TEST_OPS_TENANT")
	}
	ctx := context.Background()
	st, err := Open(ctx, "pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer st.DB.Close()
	// PostgreSQL runs encrypted, memory in clear: the figures must still agree.
	v := vault.New(vault.Ephemeral())
	pgStores := vault.Protect(st.Stores(), v)
	mem := opsDay(t, ctx, app.MemoryStores(), tenant)
	pg := opsDay(t, ctx, pgStores, tenant)
	defer checkEncryptedAtRest(t, ctx, st, pgStores, tenant)
	if len(mem) != len(pg) {
		t.Fatalf("memory produced %d facts, PostgreSQL %d", len(mem), len(pg))
	}
	diffs := 0
	for i := range mem {
		if mem[i] != pg[i] {
			diffs++
			if diffs <= 20 {
				t.Errorf("differs:\n  memory:   %s\n  postgres: %s", mem[i], pg[i])
			}
		}
	}
	t.Logf("%d facts compared, %d differ", len(mem), diffs)
}

func opsDay(t *testing.T, ctx context.Context, stores app.Stores, tenant string) []string {
	t.Helper()
	const d = "2026-10-16"
	reg, err := ingest.LoadFS(ledgerline.FS, "config/adapters")
	if err != nil {
		t.Fatal(err)
	}
	f, _ := ledgerline.FS.Open("config/charges/sample-schedule.json")
	sched, err := charges.LoadSchedule(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	a := app.New(stores, reg, sched)
	o := ops.New(a)
	o.SimulateOMS = true
	must := func(step string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", step, err)
		}
	}
	must("seed", o.SeedDev(ctx, tenant))
	_, err = o.LoadSampleDay(ctx, tenant, "priya.nair", sample.Generate(d), false, func(ctx context.Context, tenant, actor, source, name string, data []byte) error {
		_, err := a.IngestFile(ctx, tenant, actor, source, "", name, data)
		return err
	})
	must("load", err)
	var facts []string
	add := func(k string, v any) {
		b, _ := json.Marshal(v)
		facts = append(facts, k+" = "+string(b))
	}
	rec, err := a.RunRecon(ctx, tenant, "priya.nair", d)
	must("recon", err)
	add("recon", rec.Result.ByType)
	m, _, err := o.Market(ctx, tenant, d)
	must("market", err)
	bill, err := a.RunBilling(ctx, tenant, "priya.nair", d, m.SettlementPrices())
	must("billing", err)
	add("bills", len(bill.Posted))
	risk := func(label string) {
		rv, err := o.Risk(ctx, tenant, d)
		must("risk", err)
		for _, r := range rv.Rows {
			add(label+" "+r.UCC, []string{r.Status, r.Funds.String(), r.Required.String(), r.Free.String(), r.Limit.String()})
		}
	}
	risk("risk")
	mr, err := o.Margins(ctx, tenant, d)
	must("margins", err)
	for _, r := range mr.Rows {
		add("margin "+r.UCC, []string{r.Peak.String(), r.Collected.String(), r.Penalty.String()})
	}
	for i := 0; i < 500; i++ {
		cur, err := o.Obligations(ctx, tenant, d)
		must("obligations", err)
		var next *ops.Obligation
		for j := range cur.Obligations {
			if cur.Obligations[j].Action != "" {
				next = &cur.Obligations[j]
				break
			}
		}
		if next == nil {
			break
		}
		_, err = o.SettleAction(ctx, tenant, "priya.nair", d, next.Key, next.Action)
		must("settle "+next.Key, err)
	}
	se, err := o.Obligations(ctx, tenant, d)
	must("obligations", err)
	for _, ob := range se.Obligations {
		add("obligation "+ob.Key, ob.Status)
	}
	n, err := o.GenerateNotes(ctx, tenant, "priya.nair", d)
	must("notes", err)
	sent, bounced, err := o.SendNotes(ctx, tenant, "priya.nair", d, "")
	must("send notes", err)
	add("notes", []int{n, sent, bounced})
	sq, err := o.SquareOffClients(ctx, tenant, "anita.rao", d, nil)
	must("square-off", err)
	add("square-off", len(sq.Instructions))
	rb, err := a.RunBillingOpts(ctx, tenant, "farhan.khan", d, m.SettlementPrices(), true)
	must("rebill", err)
	add("rebill", []int{len(rb.Posted), len(rb.Reversed)})
	pen, err := o.PostPenalties(ctx, tenant, "anita.rao", d)
	must("penalties", err)
	add("penalties", pen)
	bt, err := o.BankTxns(ctx, tenant)
	must("bank", err)
	for _, b := range bt {
		add("bank "+b.ID, []string{b.Status, b.UCC})
	}
	set, err := o.Settings(ctx, tenant)
	must("settings", err)
	batch, err := o.RunRAS(ctx, tenant, "farhan.khan", d, set.RASMonthly)
	must("ras", err)
	add("ras", len(batch.Items))
	raised, err := o.RunRules(ctx, tenant, "vikram.shah", d)
	must("rules", err)
	add("rules", raised)
	risk("risk after")
	tb, err := a.Ledger.TrialBalance(ctx, tenant, d)
	must("trial balance", err)
	if !tb.Balanced {
		t.Fatal("trial balance does not balance")
	}
	for _, r := range tb.Rows {
		add("tb "+r.Account, []string{r.Debit.String(), r.Credit.String()})
	}
	fu, err := o.Funds(ctx, tenant, d)
	must("funds", err)
	add("funds", fu)
	dash, err := o.Dashboard(ctx, tenant, d)
	must("dashboard", err)
	db, _ := json.Marshal(dash)
	var dm map[string]any
	json.Unmarshal(db, &dm)
	delete(dm, "eod") // carries timestamps
	add("dashboard", dm)
	sort.Strings(facts)
	t.Logf("%s: %s", fmt.Sprintf("%T", stores.Ledger), strings.Join(facts[:3], "; "))
	return facts
}

// TestIntegrationWeekParity works three consecutive sample days end to end
// (positions rolled at each close, carried futures marked to market, sign-in
// credentials seeded) on memory and on PostgreSQL and requires the same
// outcome. Tenant LEDGERLINE_TEST_WEEK_TENANT must exist and be empty.
func TestIntegrationWeekParity(t *testing.T) {
	dsn := os.Getenv("LEDGERLINE_TEST_DB")
	tenant := os.Getenv("LEDGERLINE_TEST_WEEK_TENANT")
	if dsn == "" || tenant == "" {
		t.Skip("set LEDGERLINE_TEST_DB and LEDGERLINE_TEST_WEEK_TENANT")
	}
	ctx := context.Background()
	st, err := Open(ctx, "pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer st.DB.Close()
	ops.PasswordIterations = 1000
	run := func(stores app.Stores) []string {
		reg, _ := ingest.LoadFS(ledgerline.FS, "config/adapters")
		f, _ := ledgerline.FS.Open("config/charges/sample-schedule.json")
		sched, _ := charges.LoadSchedule(f)
		f.Close()
		a := app.New(stores, reg, sched)
		o := ops.New(a)
		o.SimulateOMS = true
		if err := o.SeedDev(ctx, tenant); err != nil {
			t.Fatal(err)
		}
		if _, _, err := o.Login(ctx, tenant, "admin", ops.DevPassword, "", "", ""); err != nil {
			t.Fatalf("login: %v", err)
		}
		var out []string
		date := "2026-10-16"
		for i := 0; i < 3; i++ {
			r, err := o.RunSampleDay(ctx, tenant, "admin", date, func(ctx context.Context, tn, actor, source, name string, data []byte) error {
				_, err := a.IngestFile(ctx, tn, actor, source, "", name, data)
				return err
			})
			if err != nil {
				t.Fatalf("%T %s: %v", stores.Ledger, date, err)
			}
			b, _ := json.Marshal(r)
			out = append(out, string(b))
			tb, _ := a.Ledger.TrialBalance(ctx, tenant, date)
			for _, row := range tb.Rows {
				out = append(out, date+" "+row.Account+" "+row.Debit.String()+" "+row.Credit.String())
			}
			date = r.NextDay
		}
		return out
	}
	mem, pg := run(app.MemoryStores()), run(vault.Protect(st.Stores(), vault.New(vault.Ephemeral())))
	if len(mem) != len(pg) {
		t.Fatalf("memory %d lines, PostgreSQL %d", len(mem), len(pg))
	}
	diffs := 0
	for i := range mem {
		if mem[i] != pg[i] {
			diffs++
			if diffs < 10 {
				t.Errorf("memory %s\npostgres %s", mem[i], pg[i])
			}
		}
	}
	t.Logf("three days: %d facts compared, %d differ", len(mem), diffs)
}

// checkEncryptedAtRest reads the tenant's rows straight from PostgreSQL and
// requires that no client PAN, bank account or email appears in clear in
// documents, document history or stored files, and that the audit chain
// verifies.
func checkEncryptedAtRest(t *testing.T, ctx context.Context, st *Store, stores app.Stores, tenant string) {
	t.Helper()
	tx, err := st.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, Q["set_tenant"], tenant); err != nil {
		t.Fatal(err)
	}
	var docsN, clear, histClear, files, filesClear int
	needle := "%ABCPI1234K%"
	tx.QueryRowContext(ctx, `SELECT count(*), count(*) FILTER (WHERE body::text ILIKE $2 OR body::text ILIKE '%"pan"%') FROM ll.doc WHERE tenant_id = $1`, tenant, needle).Scan(&docsN, &clear)
	tx.QueryRowContext(ctx, `SELECT count(*) FILTER (WHERE body::text ILIKE $2 OR body::text ILIKE '%"pan"%') FROM ll.doc_history WHERE tenant_id = $1`, tenant, needle).Scan(&histClear)
	tx.QueryRowContext(ctx, `SELECT count(*), count(*) FILTER (WHERE position('ABCPI1234K'::bytea in content) > 0 OR substring(content from 1 for 4) <> 'LLV1'::bytea) FROM ll.raw_file WHERE tenant_id = $1`, tenant).Scan(&files, &filesClear)
	t.Logf("at rest: %d documents (%d in clear), %d history rows in clear, %d files (%d in clear)", docsN, clear, histClear, files, filesClear)
	if docsN == 0 || files == 0 {
		t.Fatal("nothing stored; the check did not run")
	}
	if clear+histClear+filesClear > 0 {
		t.Errorf("client data stored in clear: %d documents, %d history rows, %d files", clear, histClear, filesClear)
	}
	// A record put straight into the database in clear is refused.
	if _, err := tx.ExecContext(ctx, `INSERT INTO ll.doc (tenant_id, coll, id, version, body, updated_by) VALUES ($1, 'client', 'PLANTED', 1, '{"pan":"PLNTD1234X"}', 'db')`, tenant); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := stores.Docs.Get(ctx, tenant, "client", "PLANTED"); !errors.Is(err, vault.ErrUnencrypted) {
		t.Errorf("planted plaintext record accepted: %v", err)
	}
	r, err := stores.Audit.(*vault.Audit).VerifyAudit(ctx, tenant)
	if err != nil || !r.OK || r.Sealed == 0 {
		t.Errorf("audit chain: %+v %v", r, err)
	}
	t.Logf("audit chain: %d sealed entries verified", r.Sealed)
}
