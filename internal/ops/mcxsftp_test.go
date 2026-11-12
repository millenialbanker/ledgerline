package ops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"ledgerline/internal/docs"
	"ledgerline/internal/xfile"
)

func TestMCXSFTPFetch(t *testing.T) {
	o, ctx := setup(t)
	o.Now = func() time.Time { return time.Date(2026, 10, 16, 9, 0, 0, 0, ist) }
	_, sym, exp := mcxClient(t, o)
	root := t.TempDir()
	put := func(rel string, b []byte, mod time.Time) {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		if !mod.IsZero() {
			os.Chtimes(p, mod, mod)
		}
	}
	pm := xwrite(t, "mcx-product-master", []xfile.Row{productRow("FUTCOM", sym, "", exp, "", "1001", "0", "100", "N"), productRow("FUTCOM", sym, "", exp, "", "1002", "1001", "100", "N")}...)
	put("Common/MCX_ProductMaster.csv", pm, time.Time{})
	put("Common/MCXRPF-20261016-1100-01-I.spn", []byte("<spn/>"), time.Time{})                       // risk parameters: not read
	put("Common/Bhavcopy_MCX_CO_0_0_0_20260930_F_0000.csv", []byte("x"), o.Now().AddDate(0, 0, -30)) // old, relative to the test date
	mrg := xwrite(t, "mcx-margin-tm",
		xr("10", "Date", "16102026", "CM ID", "1234", "TM ID", "56845", "Number of Detail Records at Summary - End Client Level", "1", "Actual Margin Levied", "1000.00"),
		xr("30", "TM ID", "56845", "Account Type", "C", "Account ID", "MC001", "Actual Margin Levied", "1000.00"))
	put("56845/16102026/MCX_MRG_56845_20261016.csv", mrg, time.Time{})
	put("56845/01102026/MCX_MRG_56845_20261001.csv", mrg, time.Time{}) // a date folder too old
	put("56845/16102026/MCX_Empty_TRD.csv", nil, time.Time{})

	addr, key := sftpTestServer(t, root)
	c := Connection{Provider: connMCXSFTP, Status: "Saved", Values: map[string]string{"host": addr, "user": "DP081600", "member": "56845", "root": root, "fetch": "On"},
		Secrets: map[string]string{"password": "pw"}}
	if err := docs.Upsert(ctx, o.Docs, T, cConn, connMCXSFTP, c, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := o.MCXSFTPFetch(ctx, T, "priya.nair"); err == nil || !strings.Contains(err.Error(), ssh.FingerprintSHA256(key)) {
		t.Fatalf("without a host key: %v", err)
	}
	c.Values["hostkey"] = ssh.FingerprintSHA256(key)
	docs.Upsert(ctx, o.Docs, T, cConn, connMCXSFTP, c, "admin")
	run, err := o.MCXSFTPFetch(ctx, T, "priya.nair")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range run.Fetched {
		names = append(names, f.Name+"="+f.Format)
	}
	got := strings.Join(names, ",")
	if len(run.Fetched) != 2 || !strings.Contains(got, "MCX_ProductMaster.csv=mcx-product-master") || !strings.Contains(got, "MCX_MRG_56845_20261016.csv=mcx-margin-tm") {
		t.Fatalf("fetched %s; run %+v", got, run)
	}
	if run.Unknown < 1 || len(run.Folders) != 3 {
		t.Fatalf("run %+v", run)
	}
	if m, _ := o.MCXContracts(ctx, T, sym); len(m.Contracts) == 0 {
		t.Fatal("product master not loaded")
	}
	// Again: nothing new.
	run, _ = o.MCXSFTPFetch(ctx, T, "priya.nair")
	if len(run.Fetched) != 0 || run.Skipped != 2 {
		t.Fatalf("second run %+v", run)
	}
	if v := o.MCXSFTPStatus(ctx, T); len(v.Files) != 2 || v.State.LastOK == "" {
		t.Fatalf("status %+v", v)
	}
	// The tick waits half an hour between runs, and only runs when on.
	o.mcxSFTPTick(ctx, T, o.Now().Add(10*time.Minute))
	if v := o.MCXSFTPStatus(ctx, T); v.State.LastRun != o.Now().Format(time.RFC3339) {
		t.Fatalf("tick ran too soon: %s", v.State.LastRun)
	}
	if _, err := o.MCXSFTPFetch(ctx, T, "vikram.shah"); err == nil {
		t.Fatal("a viewer fetched")
	}
}

func TestMCXDateFolder(t *testing.T) {
	d := []time.Time{time.Date(2026, 10, 16, 0, 0, 0, 0, ist)}
	for n, want := range map[string]bool{"16102026": true, "20261016": true, "16-10-2026": true, "16Oct2026": true, "16OCT2026": true, "15102026": false, "Reports": false} {
		if mcxDateFolder(n, d) != want {
			t.Errorf("%s", n)
		}
	}
}
