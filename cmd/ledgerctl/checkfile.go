package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"ledgerline/internal/xfile"
)

// checkFile reads exchange and clearing files on this computer against
// Ledgerline's layouts and prints a summary that shows no field value:
// the layout each is recognised as, its rows by record, field counts, the
// header, and the problems by column and kind, with quoted values masked
// and digits shown as 9. Nothing is sent anywhere; zip and gzip files are
// opened in memory. A folder is checked file by file (not its subfolders).
//
//	ledgerctl check-file ~/Downloads/MCX/*.csv
//	ledgerctl check-file -json MCX_NOBL_56845_20261104.csv
func checkFile(args []string, stdout io.Writer) (int, error) {
	fl := flag.NewFlagSet("check-file", flag.ContinueOnError)
	root := fl.String("root", "", "repository folder to read the layouts from (default: built into this binary)")
	asJSON := fl.Bool("json", false, "print the summary as JSON")
	if err := fl.Parse(args); err != nil {
		return 0, err
	}
	if fl.NArg() == 0 {
		return 0, fmt.Errorf("give one or more files (or folders) to check")
	}
	lib, err := xfile.Load(source(*root), "config/formats")
	if err != nil {
		return 0, err
	}
	var paths []string
	for _, p := range fl.Args() {
		st, err := os.Stat(p)
		if err != nil {
			return 0, err
		}
		if !st.IsDir() {
			paths = append(paths, p)
			continue
		}
		ents, err := os.ReadDir(p)
		if err != nil {
			return 0, err
		}
		for _, e := range ents {
			if e.Type().IsRegular() && e.Name()[0] != '.' {
				paths = append(paths, filepath.Join(p, e.Name()))
			}
		}
	}
	sort.Strings(paths)
	var all []xfile.CheckResult
	bad := 0
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil {
			return bad, err
		}
		if st.Size() > xfile.MaxUnpacked {
			all = append(all, xfile.CheckResult{Name: xfile.MaskName(filepath.Base(p)), Note: "larger than the largest file Ledgerline reads"})
			bad++
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return bad, err
		}
		for _, r := range lib.Check(filepath.Base(p), b) {
			if !r.Recognised || r.Issues > 0 || r.Note != "" {
				bad++
			}
			all = append(all, r)
		}
	}
	if *asJSON {
		e := json.NewEncoder(stdout)
		e.SetIndent("", "  ")
		return bad, e.Encode(all)
	}
	for _, r := range all {
		xfile.WriteCheck(stdout, r)
	}
	fmt.Fprintf(stdout, "%d files checked, %d with problems. No field values are shown: quoted values are masked and digits are 9.\n", len(all), bad)
	return bad, nil
}
