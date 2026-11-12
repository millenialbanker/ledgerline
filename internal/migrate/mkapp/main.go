// Command mkapp packs the macOS builds of the migration tool into
// "Ledgerline Migration.app" (a minimal bundle: Info.plist and the
// program, no Cocoa code) and zips it. The arm64 and amd64 programs are
// joined into one universal (fat) Mach-O file here, so no lipo or Xcode is
// needed and the bundle can be made on any system.
//
//	go run ./internal/migrate/mkapp -arm64 bin/x-darwin-arm64 -amd64 bin/x-darwin-amd64 \
//	    -out "bin/Ledgerline Migration.app" -zip bin/Ledgerline-Migration-macOS.zip -version 1.0
//
// The bundle is not signed (see the Makefile's TODO); each slice keeps the
// ad-hoc signature the Go linker gives darwin/arm64 programs.
package main

import (
	"archive/zip"
	"debug/macho"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Exe is the program's name inside the bundle.
const Exe = "ledgerline-migrate"

// Slice is one architecture of a universal binary.
type Slice struct {
	CPU, Sub uint32
	Align    uint32 // as a power of two
	Data     []byte
}

// Fat writes a universal Mach-O file (big-endian fat header, then each
// slice at an offset aligned to its Align).
func Fat(w io.Writer, slices []Slice) error {
	if len(slices) == 0 {
		return errors.New("no slices")
	}
	hdr := 8 + 20*len(slices)
	off := uint32(hdr)
	type ent struct{ cpu, sub, off, size, align uint32 }
	var ents []ent
	for _, s := range slices {
		a := uint32(1) << s.Align
		off = (off + a - 1) &^ (a - 1)
		ents = append(ents, ent{s.CPU, s.Sub, off, uint32(len(s.Data)), s.Align})
		off += uint32(len(s.Data))
	}
	buf := make([]byte, 0, hdr)
	buf = binary.BigEndian.AppendUint32(buf, macho.MagicFat)
	buf = binary.BigEndian.AppendUint32(buf, uint32(len(slices)))
	for _, e := range ents {
		for _, v := range []uint32{e.cpu, e.sub, e.off, e.size, e.align} {
			buf = binary.BigEndian.AppendUint32(buf, v)
		}
	}
	if _, err := w.Write(buf); err != nil {
		return err
	}
	pos := uint32(len(buf))
	for i, s := range slices {
		if pad := ents[i].off - pos; pad > 0 {
			if _, err := w.Write(make([]byte, pad)); err != nil {
				return err
			}
		}
		if _, err := w.Write(s.Data); err != nil {
			return err
		}
		pos = ents[i].off + ents[i].size
	}
	return nil
}

func slice(path string) (Slice, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Slice{}, err
	}
	f, err := macho.NewFile(strings.NewReader(string(data)))
	if err != nil {
		return Slice{}, fmt.Errorf("%s is not a macOS program: %w", path, err)
	}
	return Slice{CPU: uint32(f.Cpu), Sub: f.SubCpu, Align: 14, Data: data}, nil
}

// Plist is the bundle's Info.plist.
func Plist(version string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleName</key><string>Ledgerline Migration</string>
	<key>CFBundleDisplayName</key><string>Ledgerline Migration</string>
	<key>CFBundleIdentifier</key><string>app.ledgerline.migration</string>
	<key>CFBundleExecutable</key><string>` + Exe + `</string>
	<key>CFBundlePackageType</key><string>APPL</string>
	<key>CFBundleShortVersionString</key><string>` + version + `</string>
	<key>CFBundleVersion</key><string>` + version + `</string>
	<key>CFBundleInfoDictionaryVersion</key><string>6.0</string>
	<key>LSMinimumSystemVersion</key><string>11.0</string>
	<key>LSUIElement</key><true/>
	<key>NSHighResolutionCapable</key><true/>
</dict>
</plist>
`
}

func main() {
	arm := flag.String("arm64", "", "darwin/arm64 program")
	amd := flag.String("amd64", "", "darwin/amd64 program")
	out := flag.String("out", "Ledgerline Migration.app", "the bundle to make")
	zipPath := flag.String("zip", "", "also zip the bundle here")
	version := flag.String("version", "1.0", "version")
	flag.Parse()
	if err := build(*arm, *amd, *out, *zipPath, *version); err != nil {
		fmt.Fprintln(os.Stderr, "mkapp:", err)
		os.Exit(1)
	}
}

func build(arm, amd, out, zipPath, version string) error {
	var slices []Slice
	for _, p := range []string{arm, amd} {
		if p == "" {
			continue
		}
		s, err := slice(p)
		if err != nil {
			return err
		}
		slices = append(slices, s)
	}
	if len(slices) == 0 {
		return errors.New("give -arm64 and/or -amd64")
	}
	if err := os.RemoveAll(out); err != nil {
		return err
	}
	macos := filepath.Join(out, "Contents", "MacOS")
	if err := os.MkdirAll(macos, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "Contents", "Info.plist"), []byte(Plist(version)), 0o644); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(macos, Exe), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if err := Fat(f, slices); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if zipPath != "" {
		return zipDir(out, zipPath)
	}
	return nil
}

// zipDir zips the bundle keeping the program executable.
func zipDir(dir, dst string) error {
	zf, err := os.Create(dst)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(zf)
	base := filepath.Dir(dir)
	err = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(base, p)
		h := &zip.FileHeader{Name: filepath.ToSlash(rel), Method: zip.Deflate, Modified: time.Now()}
		if info.IsDir() {
			h.Name += "/"
			h.SetMode(os.ModeDir | 0o755)
			_, err := zw.CreateHeader(h)
			return err
		}
		h.SetMode(info.Mode().Perm())
		w, err := zw.CreateHeader(h)
		if err != nil {
			return err
		}
		src, err := os.Open(p)
		if err != nil {
			return err
		}
		defer src.Close()
		_, err = io.Copy(w, src)
		return err
	})
	if cerr := zw.Close(); err == nil {
		err = cerr
	}
	if cerr := zf.Close(); err == nil {
		err = cerr
	}
	return err
}
