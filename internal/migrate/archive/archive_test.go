package archive

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash/crc32"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf16"
)

func write(t *testing.T, p string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func sum(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func makeZip(t *testing.T, p string, entries map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for n, d := range entries {
		w, _ := zw.Create(n)
		w.Write(d)
	}
	zw.Close()
	write(t, p, buf.Bytes())
	return buf.Bytes()
}

// make7z writes a minimal unencrypted 7z archive holding one file stored
// with the Copy method (no 7-Zip program is needed to build the fixture).
func make7z(name string, data []byte) []byte {
	if len(data) >= 0x80 || len(name) > 50 {
		panic("keep fixtures small: one-byte 7z numbers only")
	}
	var h bytes.Buffer
	h.Write([]byte{0x01})                        // Header
	h.Write([]byte{0x04})                        // MainStreamsInfo
	h.Write([]byte{0x06, 0x00, 0x01})            // PackInfo: pos 0, 1 stream
	h.Write([]byte{0x09, byte(len(data)), 0x00}) // sizes, end
	h.Write([]byte{0x07, 0x0B, 0x01, 0x00})      // UnpackInfo, Folder, 1 folder, not external
	h.Write([]byte{0x01, 0x01, 0x00})            // 1 coder: id size 1, id 0x00 (Copy)
	h.Write([]byte{0x0C, byte(len(data))})       // unpack size
	h.Write([]byte{0x00})                        // end UnpackInfo
	h.Write([]byte{0x08})                        // SubStreamsInfo
	h.Write([]byte{0x0A, 0x01})                  // CRC, all defined
	binary.Write(&h, binary.LittleEndian, crc32.ChecksumIEEE(data))
	h.Write([]byte{0x00}) // end SubStreamsInfo
	h.Write([]byte{0x00}) // end MainStreamsInfo
	h.Write([]byte{0x05, 0x01})
	u := utf16.Encode([]rune(name))
	h.Write([]byte{0x11, byte(1 + 2*(len(u)+1)), 0x00})
	for _, c := range u {
		binary.Write(&h, binary.LittleEndian, c)
	}
	h.Write([]byte{0, 0})
	h.Write([]byte{0x00}) // end FilesInfo
	h.Write([]byte{0x00}) // end Header
	hdr := h.Bytes()

	var start bytes.Buffer
	binary.Write(&start, binary.LittleEndian, uint64(len(data)))
	binary.Write(&start, binary.LittleEndian, uint64(len(hdr)))
	binary.Write(&start, binary.LittleEndian, crc32.ChecksumIEEE(hdr))
	var out bytes.Buffer
	out.Write([]byte{'7', 'z', 0xBC, 0xAF, 0x27, 0x1C, 0, 4})
	binary.Write(&out, binary.LittleEndian, crc32.ChecksumIEEE(start.Bytes()))
	out.Write(start.Bytes())
	out.Write(data)
	out.Write(hdr)
	return out.Bytes()
}

func TestPlainBak(t *testing.T) {
	d := t.TempDir()
	data := []byte("TAPE made-up backup bytes")
	p := filepath.Join(d, "NC2627 backup.BAK")
	write(t, p, data)
	items, err := Unpack(context.Background(), p, Options{Work: filepath.Join(d, "work")})
	if err != nil || len(items) != 1 {
		t.Fatal(items, err)
	}
	it := items[0]
	if it.Path != p || it.Extracted || it.SHA256 != sum(data) || it.Bytes != int64(len(data)) || it.Name != "NC2627 backup.BAK" {
		t.Errorf("%+v", it)
	}
}

func TestZip(t *testing.T) {
	d := t.TempDir()
	work := filepath.Join(d, "work")
	bak := []byte("made-up NC2526 backup")
	zb := makeZip(t, filepath.Join(d, "in", "NC2526.zip"), map[string][]byte{
		"../../evil/NC2526.bak": bak,
		"readme.txt":            []byte("ignored"),
	})
	var prog []string
	items, err := Unpack(context.Background(), filepath.Join(d, "in", "NC2526.zip"), Options{Work: work, Progress: func(w string, done, total int64) { prog = append(prog, w) }})
	if err != nil || len(items) != 1 {
		t.Fatal(items, err)
	}
	it := items[0]
	if !it.Extracted || it.Inner != "NC2526.bak" || it.SHA256 != sum(zb) || it.Bytes != int64(len(bak)) {
		t.Errorf("%+v", it)
	}
	// written flat inside the work folder, never where ../ pointed
	rel, err := filepath.Rel(work, it.Path)
	if err != nil || strings.HasPrefix(rel, "..") {
		t.Errorf("extracted outside the work folder: %s", it.Path)
	}
	if _, err := os.Stat(filepath.Join(d, "evil")); err == nil {
		t.Error("path traversal")
	}
	got, _ := os.ReadFile(it.Path)
	if !bytes.Equal(got, bak) {
		t.Error("content")
	}
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(it.Path); st.Mode().Perm() != 0o600 {
			t.Errorf("mode %v", st.Mode())
		}
	}
	if len(prog) == 0 {
		t.Error("no progress")
	}
	// a zip with no backup
	makeZip(t, filepath.Join(d, "in", "empty.zip"), map[string][]byte{"a.txt": []byte("x")})
	_, err = Unpack(context.Background(), filepath.Join(d, "in", "empty.zip"), Options{Work: work})
	var se *SkipError
	if !errors.As(err, &se) || !strings.Contains(se.Reason, "no SQL Server backup") {
		t.Errorf("empty zip: %v", err)
	}
}

func TestSevenZip(t *testing.T) {
	d := t.TempDir()
	work := filepath.Join(d, "work")
	bak := []byte("made-up NCMAIL2627 backup")
	raw := make7z("Backup/NCMAIL2627.bak", bak)
	p := filepath.Join(d, "NCMAIL2627_202611112200.7z")
	write(t, p, raw)
	items, err := Unpack(context.Background(), p, Options{Work: work})
	if err != nil || len(items) != 1 {
		t.Fatal(items, err)
	}
	if items[0].Inner != "NCMAIL2627.bak" || items[0].SHA256 != sum(raw) {
		t.Errorf("%+v", items[0])
	}
	got, _ := os.ReadFile(items[0].Path)
	if !bytes.Equal(got, bak) {
		t.Errorf("content %q", got)
	}

	// a 7z inside a zip
	zp := filepath.Join(d, "nested.zip")
	makeZip(t, zp, map[string][]byte{"inner.7z": raw})
	items, err = Unpack(context.Background(), zp, Options{Work: work})
	if err != nil || len(items) != 1 || items[0].Inner != "inner.7z/NCMAIL2627.bak" {
		t.Fatal(items, err)
	}
}

// The encrypted fixtures are sevenzip's own test archives (BSD licence),
// password "password"; they hold no .bak, so a right password ends in
// "no SQL Server backup inside".
func TestSevenZipPassword(t *testing.T) {
	work := t.TempDir()
	for _, f := range []string{"t2.7z", "t5.7z"} {
		p := filepath.Join("testdata", f)
		_, err := Unpack(context.Background(), p, Options{Work: work})
		var np *NeedPassword
		if f == "t2.7z" {
			// encrypted headers: asked for before anything is read
			if !errors.As(err, &np) || np.Wrong {
				t.Errorf("%s without password: %v", f, err)
			}
			_, err = Unpack(context.Background(), p, Options{Work: work, Password: "wrong one"})
			if !errors.As(err, &np) || !np.Wrong {
				t.Errorf("%s wrong password: %v", f, err)
			}
		}
		_, err = Unpack(context.Background(), p, Options{Work: work, Password: "password"})
		var se *SkipError
		if !errors.As(err, &se) || !strings.Contains(se.Reason, "no SQL Server backup") {
			t.Errorf("%s right password: %v", f, err)
		}
	}
}

func TestExpandFolders(t *testing.T) {
	d := t.TempDir()
	write(t, filepath.Join(d, "SharePro Backups", "2026", "NC2627.7z"), make7z("NC2627.bak", []byte("x")))
	write(t, filepath.Join(d, "SharePro Backups", "2025", "NC2526.bak"), []byte("y"))
	write(t, filepath.Join(d, "SharePro Backups", "old.rar"), []byte("r"))
	write(t, filepath.Join(d, "SharePro Backups", "notes.txt"), []byte("n"))
	write(t, filepath.Join(d, "SharePro Backups", "NC1920.mdf"), []byte("m"))
	write(t, filepath.Join(d, "SharePro Backups", "split.7z.001"), []byte("1"))
	write(t, filepath.Join(d, "SharePro Backups", "split.7z.002"), []byte("2"))
	write(t, filepath.Join(d, "SharePro Backups", ".hidden", "x.bak"), []byte("h"))
	write(t, filepath.Join(d, "photo.jpg"), []byte("j"))
	files, skipped := Expand([]string{filepath.Join(d, "SharePro Backups"), filepath.Join(d, "photo.jpg"), filepath.Join(d, "missing.bak")})
	var names []string
	for _, f := range files {
		names = append(names, filepath.Base(f))
	}
	if strings.Join(names, ",") != "NC2526.bak,NC2627.7z,split.7z.001" {
		t.Errorf("files %v", names)
	}
	why := map[string]string{}
	for _, s := range skipped {
		why[s.Name] = s.Reason
	}
	if !strings.Contains(why["old.rar"], "unpack it with WinRAR or 7-Zip first") || !strings.Contains(why["NC1920.mdf"], "detached") || !strings.Contains(why["photo.jpg"], "Not a SQL Server backup") || !strings.Contains(why["missing.bak"], "not there") {
		t.Errorf("skipped %v", why)
	}
	if _, ok := why["notes.txt"]; ok {
		t.Error("ordinary files in a folder reported")
	}
	if len(skipped) != 4 {
		t.Errorf("skipped %v", skipped)
	}
}

func TestNames(t *testing.T) {
	for in, want := range map[string]string{
		`..\..\Windows\NC2627.bak`:   "NC2627.bak",
		"a/b/../../c/NC2627.bak":     "NC2627.bak",
		`C:\SharePro\Bkp\SSMAIL.bak`: "SSMAIL.bak",
		"/etc/passwd":                "passwd",
	} {
		if g := entryName(in); g != want {
			t.Errorf("entryName(%q) = %q", in, g)
		}
	}
	if safeName("NC:26*27?.bak") != "NC_26_27_.bak" || safeName("...") != "file" {
		t.Error(safeName("NC:26*27?.bak"))
	}
	for name, k := range map[string]string{"X.BAK": "bak", "x.7Z": "7z", "x.7z.001": "7z", "x.Zip": "zip", "x.rar": "", "x.7z.003": ""} {
		if g, _ := kind(name); g != k {
			t.Errorf("kind(%s) = %s", name, g)
		}
	}
	// Windows-style paths are classified the same on any system
	if g, _ := kind(`D:\SharePro Backup\NC2627_20261111.7z`); g != "7z" {
		t.Error("windows path")
	}
}

func TestCancel(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "a.bak")
	write(t, p, bytes.Repeat([]byte("x"), 3<<20))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Unpack(ctx, p, Options{Work: d}); !errors.Is(err, context.Canceled) {
		t.Errorf("got %v", err)
	}
}
