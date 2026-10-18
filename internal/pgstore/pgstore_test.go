package pgstore

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
)

// TestDumpPrepared writes every statement as a PREPARE so it can be
// type-checked against a live schema without a Go driver:
//
//	LL_DUMP_SQL=/tmp/prepared.sql go test ./internal/pgstore -run TestDumpPrepared
//	psql -v ON_ERROR_STOP=1 -f /tmp/prepared.sql
//
// The integration suite (build tag "integration") exercises the same store
// end to end once a driver is available.
func TestDumpPrepared(t *testing.T) {
	names := make([]string, 0, len(Q))
	for k := range Q {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, n := range names {
		if strings.Count(Q[n], "$") == 0 {
			t.Errorf("%s has no parameters; tenant scoping is missing", n)
		}
		if n != "set_tenant" && n != "audit_lock" && !strings.Contains(Q[n], "tenant_id") {
			t.Errorf("%s does not filter or set tenant_id", n)
		}
	}
	path := os.Getenv("LL_DUMP_SQL")
	if path == "" {
		t.Skip("set LL_DUMP_SQL to write the PREPARE script")
	}
	var b strings.Builder
	b.WriteString("\\set ON_ERROR_STOP on\nBEGIN;\nSET ROLE ll_test_app;\nSELECT set_config('app.tenant_id', 'alpha', true);\n")
	for _, n := range names {
		fmt.Fprintf(&b, "PREPARE q_%s AS %s;\n\\echo 'prepared %s'\n", n, Q[n], n)
	}
	b.WriteString("ROLLBACK;\n")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}
