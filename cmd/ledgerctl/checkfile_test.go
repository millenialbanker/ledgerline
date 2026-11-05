package main

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckFileCommand(t *testing.T) {
	dir := t.TempDir()
	// MCXCCL's own sample margin file (dummy data), zipped, and an unknown file.
	b, err := os.ReadFile("../../internal/xfile/testdata/mcxccl/Margin_MCXCCL_CO_0_TM_88888_20231106_F_0000.csv")
	if err != nil {
		t.Fatal(err)
	}
	var z bytes.Buffer
	zw := zip.NewWriter(&z)
	w, _ := zw.Create("Margin_MCXCCL_CO_0_TM_88888_20231106_F_0000.csv")
	w.Write(b)
	zw.Close()
	os.WriteFile(filepath.Join(dir, "margin.zip"), z.Bytes(), 0o644)
	os.WriteFile(filepath.Join(dir, "notes_1234.txt"), []byte("hello\n"), 0o644)
	var out bytes.Buffer
	bad, err := checkFile([]string{dir}, &out)
	if err != nil || bad != 1 {
		t.Fatalf("%d %v\n%s", bad, err, out.String())
	}
	s := out.String()
	for _, want := range []string{"mcxccl-margin", "issues     none", "written back byte for byte", "notes_9999.txt", "2 files checked, 1 with problems"} {
		if !strings.Contains(s, want) {
			t.Fatalf("%q not in\n%s", want, s)
		}
	}
	if strings.Contains(s, "88888") || strings.Contains(s, "GOLDM") || strings.Contains(s, "1234") {
		t.Fatalf("values shown:\n%s", s)
	}
	if _, err := checkFile(nil, &out); err == nil {
		t.Fatal("no files accepted")
	}
}
