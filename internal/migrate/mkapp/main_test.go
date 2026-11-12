package main

import (
	"archive/zip"
	"bytes"
	"debug/macho"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFat(t *testing.T) {
	var buf bytes.Buffer
	a := bytes.Repeat([]byte{0xA}, 100)
	b := bytes.Repeat([]byte{0xB}, 50)
	if err := Fat(&buf, []Slice{{CPU: uint32(macho.CpuArm64), Sub: 0, Align: 14, Data: a}, {CPU: uint32(macho.CpuAmd64), Sub: 3, Align: 14, Data: b}}); err != nil {
		t.Fatal(err)
	}
	d := buf.Bytes()
	be := binary.BigEndian
	if be.Uint32(d) != macho.MagicFat || be.Uint32(d[4:]) != 2 {
		t.Fatal("header")
	}
	off1, size1 := be.Uint32(d[16:]), be.Uint32(d[20:])
	off2, size2 := be.Uint32(d[36:]), be.Uint32(d[40:])
	if be.Uint32(d[8:]) != uint32(macho.CpuArm64) || be.Uint32(d[28:]) != uint32(macho.CpuAmd64) || be.Uint32(d[32:]) != 3 {
		t.Error("cpu types")
	}
	if off1 != 1<<14 || off2 != 2<<14 || size1 != 100 || size2 != 50 || be.Uint32(d[24:]) != 14 {
		t.Errorf("offsets %d %d", off1, off2)
	}
	if !bytes.Equal(d[off1:off1+size1], a) || !bytes.Equal(d[off2:off2+size2], b) || len(d) != int(off2+size2) {
		t.Error("slice data")
	}
}

func TestBundle(t *testing.T) {
	// the test binary itself stands in for a Mach-O only on a Mac; check the
	// refusal of a non-Mach-O file and the bundle layout with Fat directly
	d := t.TempDir()
	notMacho := filepath.Join(d, "x")
	os.WriteFile(notMacho, []byte("not a program"), 0o755)
	if err := build(notMacho, "", filepath.Join(d, "A.app"), "", "1.0"); err == nil || !strings.Contains(err.Error(), "not a macOS program") {
		t.Errorf("got %v", err)
	}
	app := filepath.Join(d, "Ledgerline Migration.app")
	os.MkdirAll(filepath.Join(app, "Contents", "MacOS"), 0o755)
	os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), []byte(Plist("1.2")), 0o644)
	os.WriteFile(filepath.Join(app, "Contents", "MacOS", Exe), []byte("prog"), 0o755)
	z := filepath.Join(d, "app.zip")
	if err := zipDir(app, z); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.OpenReader(z)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	names := map[string]os.FileMode{}
	for _, f := range zr.File {
		names[f.Name] = f.Mode()
	}
	if m, ok := names["Ledgerline Migration.app/Contents/MacOS/"+Exe]; !ok || m.Perm()&0o111 == 0 {
		t.Errorf("program missing or not executable: %v", names)
	}
	if _, ok := names["Ledgerline Migration.app/Contents/Info.plist"]; !ok {
		t.Error("no Info.plist")
	}
	p := Plist("1.2")
	for _, want := range []string{"<string>" + Exe + "</string>", "<string>1.2</string>", "APPL"} {
		if !strings.Contains(p, want) {
			t.Errorf("plist lacks %s", want)
		}
	}
}
