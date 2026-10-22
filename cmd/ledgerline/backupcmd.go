package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"ledgerline/internal/app"
	"ledgerline/internal/backup"
	"ledgerline/internal/pgstore"
	"ledgerline/internal/vault"
)

// backupCmd checks and restores backups:
//
//	ledgerline backup verify FILE
//	ledgerline backup restore FILE
//
// Both need the data keys the backup was made with (LEDGERLINE_DATA_KEYS or
// LEDGERLINE_DATA_KEYS_FILE). restore writes into the PostgreSQL database
// in LEDGERLINE_DB, connected as the database owner, and only into a firm
// that has no data there: it never overwrites. It then checks the restored
// audit trail's seals.
func backupCmd(args []string) error {
	usage := errors.New("usage: ledgerline backup verify FILE | ledgerline backup restore FILE")
	if len(args) < 2 {
		return usage
	}
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	yes := fs.Bool("yes", false, "restore without asking")
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	kr, err := dataKeys(false, false)
	if err != nil {
		return err
	}
	v := vault.New(kr)
	path := args[1]
	switch args[0] {
	case "verify":
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		m, err := backup.Read(bufio.NewReaderSize(f, 1<<20), v, nil)
		if err != nil {
			return err
		}
		printManifest(m)
		fmt.Println("OK: every part opened with these keys and the rows match the manifest.")
		return nil
	case "restore":
		dsn := os.Getenv("LEDGERLINE_DB")
		if dsn == "" {
			return errors.New("set LEDGERLINE_DB to the database to restore into, connected as its owner")
		}
		if sqlDriver == "" {
			return errors.New("this binary has no PostgreSQL driver; build with -tags pgx")
		}
		// Check the whole file first: nothing is written from a damaged backup.
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		m, err := backup.Read(bufio.NewReaderSize(f, 1<<20), v, nil)
		f.Close()
		if err != nil {
			return err
		}
		printManifest(m)
		if m.Backend != "postgres" {
			return fmt.Errorf("this backup was taken from %s storage and restores only there", m.Backend)
		}
		if !*yes {
			fmt.Printf("Restore firm %s as of %s into the database in LEDGERLINE_DB? Type the firm id to go on: ", m.Tenant, m.Date)
			line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
			if strings.TrimSpace(line) != m.Tenant {
				return errors.New("not restored")
			}
		}
		ctx := context.Background()
		st, err := pgstore.Open(ctx, sqlDriver, dsn)
		if err != nil {
			return err
		}
		defer st.DB.Close()
		f, err = os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		recs := make(chan app.Record, 256)
		errc := make(chan error, 1)
		go func() {
			_, err := backup.Read(bufio.NewReaderSize(f, 1<<20), v, func(r app.Record) error { recs <- r; return nil })
			close(recs)
			errc <- err
		}()
		n, err := st.Restore(ctx, m.Tenant, func() (app.Record, bool, error) {
			r, ok := <-recs
			return r, ok, nil
		})
		for range recs {
		}
		if rerr := <-errc; err == nil && rerr != nil {
			err = rerr
		}
		if err != nil {
			return err
		}
		fmt.Println("Restored rows:")
		for _, t := range sortedKeys(n) {
			fmt.Printf("  %-16s %d\n", t, n[t])
		}
		chk, err := (&vault.Audit{Inner: st.AuditStore(), V: v}).VerifyAudit(ctx, m.Tenant)
		if err != nil {
			return fmt.Errorf("restored, but the audit trail could not be checked: %w", err)
		}
		if !chk.OK {
			return fmt.Errorf("restored, but the audit trail's seals do not check: %+v", chk.Problems)
		}
		fmt.Printf("Audit trail: %d entries, seals intact.\nDone: firm %s is restored as of %s.\n", chk.Entries, m.Tenant, m.Date)
		return nil
	}
	return usage
}

func printManifest(m backup.Manifest) {
	fmt.Printf("Backup %s\n  firm      %s %s\n  day       %s (%s)\n  taken     %s, %s, by %s\n  storage   %s\n  rows      %d (%d bytes before compression)\n  checksum  %s\n",
		m.ID, m.Tenant, m.Firm, m.Date, strings.Join(m.Tiers, ", "), m.Created, m.Trigger, m.By, m.Backend, m.Total, m.Bytes, m.SHA256)
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
