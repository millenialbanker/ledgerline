// Package archive unpacks what the user drops on the migration tool: plain
// SQL Server backups (.bak), .7z files (SharePro's backup job,
// SQLBackupAndFTP, writes one .bak in each, sometimes with a password),
// .zip files, and folders holding any of these.
//
// Everything streams to the work folder; no backup is ever held in memory.
// Each dropped file's SHA-256 is taken for the package's manifest.
package archive

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bodgit/sevenzip"
)

// Item is one SQL Server backup ready to restore.
type Item struct {
	Source      string `json:"source"`          // the file as dropped (full path on this computer)
	Name        string `json:"name"`            // its file name, as shown and written in the manifest
	Inner       string `json:"inner,omitempty"` // the .bak's name inside the archive
	Path        string `json:"path"`            // the .bak on disk
	Bytes       int64  `json:"bytes"`           // size of the .bak
	SourceBytes int64  `json:"source_bytes"`    // size of the dropped file
	SHA256      string `json:"sha256"`          // of the dropped file
	Extracted   bool   `json:"extracted"`       // Path is in the work folder (removed at the end)
}

// Skipped is a dropped file that is not used, with why.
type Skipped struct {
	Source string `json:"source"`
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// NeedPassword means a .7z or .zip is protected: ask for the password and
// unpack it again.
type NeedPassword struct {
	File  string
	Wrong bool // a password was given and did not work
}

func (e *NeedPassword) Error() string {
	if e.Wrong {
		return "the password for " + filepath.Base(e.File) + " is not right"
	}
	return filepath.Base(e.File) + " is protected by a password"
}

// SkipError means the file is not something the tool can use.
type SkipError struct{ Reason string }

func (e *SkipError) Error() string { return e.Reason }

// Options control unpacking.
type Options struct {
	Work     string // extract into this folder
	Password string // for a protected archive
	// Progress is told how far through the file it is (bytes of the
	// dropped file hashed, then bytes extracted).
	Progress func(what string, done, total int64)
}

func ext(name string) string {
	n := strings.ToLower(name)
	if strings.HasSuffix(n, ".7z.001") {
		return ".7z.001"
	}
	return filepath.Ext(n)
}

// kind says what a file is by its name: bak, 7z, zip, or "" (with a reason
// when it is something we know and cannot use).
func kind(name string) (string, string) {
	n := strings.ToLower(name)
	switch e := ext(n); e {
	case ".bak":
		return "bak", ""
	case ".7z", ".7z.001":
		return "7z", ""
	case ".zip":
		return "zip", ""
	case ".rar":
		return "", "RAR files cannot be read by the tool: unpack it with WinRAR or 7-Zip first and drop the .bak inside."
	case ".mdf", ".ndf", ".ldf":
		return "", "This is a detached database file, not a backup. Attach it in SQL Server Management Studio and choose \"Use databases already on this server\", or take a backup (.bak) of it and drop that."
	case ".gz", ".tar", ".tgz", ".bz2", ".xz":
		return "", "This kind of archive cannot be read by the tool: unpack it first and drop the .bak inside."
	}
	if isVolume(n) {
		return "", "" // .7z.002 and later are read with the .001
	}
	return "", "Not a SQL Server backup (.bak) or an archive of one."
}

func isVolume(n string) bool {
	i := strings.LastIndex(n, ".7z.")
	if i < 0 {
		return false
	}
	rest := n[i+4:]
	return len(rest) == 3 && strings.Trim(rest, "0123456789") == "" && rest != "001"
}

// Expand turns dropped paths (files and folders) into the files to unpack,
// sorted, and the files skipped. Inside folders, files that are clearly
// not backups are left out silently; RAR files and detached databases are
// reported.
func Expand(paths []string) (files []string, skipped []Skipped) {
	seen := map[string]bool{}
	add := func(p string, inFolder bool) {
		k, why := kind(p)
		if k == "" {
			if why != "" && (!inFolder || !strings.HasPrefix(why, "Not a SQL Server")) {
				skipped = append(skipped, Skipped{Source: p, Name: filepath.Base(p), Reason: why})
			}
			return
		}
		if !seen[p] {
			seen[p] = true
			files = append(files, p)
		}
	}
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil {
			skipped = append(skipped, Skipped{Source: p, Name: filepath.Base(p), Reason: "The file cannot be opened: " + plainErr(err)})
			continue
		}
		if !st.IsDir() {
			add(p, false)
			continue
		}
		filepath.WalkDir(p, func(q string, d fs.DirEntry, err error) error {
			if err != nil {
				skipped = append(skipped, Skipped{Source: q, Name: filepath.Base(q), Reason: "The folder cannot be read: " + plainErr(err)})
				return nil
			}
			if d.IsDir() {
				if q != p && strings.HasPrefix(d.Name(), ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if d.Type().IsRegular() {
				add(q, true)
			}
			return nil
		})
	}
	sort.Strings(files)
	return files, skipped
}

func plainErr(err error) string {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "it is not there."
	case errors.Is(err, fs.ErrPermission):
		return "this computer's user may not read it."
	}
	return err.Error()
}

// Unpack makes one dropped file ready: a .bak is used where it is; a .7z or
// .zip has every .bak inside it extracted into the work folder.
func Unpack(ctx context.Context, file string, o Options) ([]Item, error) {
	k, why := kind(file)
	if k == "" {
		if why == "" {
			why = "Read together with the first part of the split archive (.7z.001)."
		}
		return nil, &SkipError{Reason: why}
	}
	st, err := os.Stat(file)
	if err != nil {
		return nil, &SkipError{Reason: "The file cannot be opened: " + plainErr(err)}
	}
	sum, err := hashFile(ctx, file, st.Size(), o)
	if err != nil {
		return nil, err
	}
	base := Item{Source: file, Name: filepath.Base(file), SourceBytes: st.Size(), SHA256: sum}
	switch k {
	case "bak":
		base.Path, base.Bytes = file, st.Size()
		return []Item{base}, nil
	case "7z":
		return unpack7z(ctx, file, base, o)
	default:
		return unpackZip(ctx, file, base, o)
	}
}

func hashFile(ctx context.Context, file string, size int64, o Options) (string, error) {
	f, err := os.Open(file)
	if err != nil {
		return "", &SkipError{Reason: "The file cannot be opened: " + plainErr(err)}
	}
	defer f.Close()
	h := sha256.New()
	if _, err := copyCtx(ctx, h, f, func(n int64) { report(o, "Checking "+filepath.Base(file), n, size) }); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func report(o Options, what string, done, total int64) {
	if o.Progress != nil {
		o.Progress(what, done, total)
	}
}

// copyCtx copies in 1 MB pieces, stopping when ctx is cancelled.
func copyCtx(ctx context.Context, w io.Writer, r io.Reader, prog func(int64)) (int64, error) {
	buf := make([]byte, 1<<20)
	var n int64
	for {
		if err := ctx.Err(); err != nil {
			return n, err
		}
		k, err := r.Read(buf)
		if k > 0 {
			if _, werr := w.Write(buf[:k]); werr != nil {
				return n, werr
			}
			n += int64(k)
			if prog != nil {
				prog(n)
			}
		}
		if err == io.EOF {
			return n, nil
		}
		if err != nil {
			return n, err
		}
	}
}

// outDir makes a fresh folder in the work folder for one archive.
func outDir(work, name string) (string, error) {
	if err := os.MkdirAll(filepath.Join(work, "unpacked"), 0o700); err != nil {
		return "", err
	}
	return os.MkdirTemp(filepath.Join(work, "unpacked"), safeName(strings.TrimSuffix(name, filepath.Ext(name)))+"-")
}

// safeName keeps a file name usable on every system: letters, digits and
// a few marks; the rest become _.
func safeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.', r == ' ':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), ". ")
	if out == "" {
		out = "file"
	}
	if len(out) > 80 {
		out = out[:80]
	}
	return out
}

// entryName is the file name of an archive entry, whatever folders (or
// ../ tricks) its path holds: entries are only ever written flat into the
// archive's own folder.
func entryName(p string) string {
	p = strings.ReplaceAll(p, `\`, "/")
	return path.Base(p)
}

// writeEntry streams one entry to dir/name with 0600 permissions.
func writeEntry(ctx context.Context, dir, name string, r io.Reader, size int64, o Options) (string, int64, error) {
	dst := filepath.Join(dir, safeName(name))
	for i := 2; ; i++ {
		if _, err := os.Lstat(dst); errors.Is(err, fs.ErrNotExist) {
			break
		}
		dst = filepath.Join(dir, fmt.Sprintf("%d-%s", i, safeName(name)))
	}
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", 0, err
	}
	n, err := copyCtx(ctx, f, r, func(n int64) { report(o, "Unpacking "+name, n, size) })
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(dst)
		return "", 0, err
	}
	return dst, n, nil
}

func unpack7z(ctx context.Context, file string, base Item, o Options) ([]Item, error) {
	r, err := sevenzip.OpenReaderWithPassword(file, o.Password)
	if err != nil {
		if encrypted(err) {
			return nil, &NeedPassword{File: file, Wrong: o.Password != ""}
		}
		return nil, &SkipError{Reason: "This .7z file cannot be read; it may be damaged or cut short. Copy it again from the SharePro server."}
	}
	defer r.Close()
	dir, err := outDir(o.Work, base.Name)
	if err != nil {
		return nil, err
	}
	var items []Item
	var nested []string
	fail := func(e error) ([]Item, error) {
		os.RemoveAll(dir)
		return nil, e
	}
	for _, f := range r.File {
		if f.FileInfo().IsDir() {
			continue
		}
		name := entryName(f.Name)
		k, _ := kind(name)
		if k == "" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			if encrypted(err) || o.Password != "" {
				return fail(&NeedPassword{File: file, Wrong: o.Password != ""})
			}
			return fail(&SkipError{Reason: "This .7z file cannot be read: " + err.Error()})
		}
		dst, n, err := writeEntry(ctx, dir, name, rc, int64(f.UncompressedSize), o)
		rc.Close()
		if err != nil {
			if ctx.Err() != nil {
				return fail(ctx.Err())
			}
			if encrypted(err) || o.Password != "" {
				return fail(&NeedPassword{File: file, Wrong: o.Password != ""})
			}
			return fail(&SkipError{Reason: "This .7z file is damaged (" + name + " could not be unpacked). Copy it again from the SharePro server."})
		}
		if k == "bak" {
			it := base
			it.Inner, it.Path, it.Bytes, it.Extracted = name, dst, n, true
			items = append(items, it)
		} else {
			nested = append(nested, dst)
		}
	}
	return finish(ctx, dir, base, items, nested, o, fail)
}

// finish unpacks archives found inside an archive (one level) and reports
// an archive with no backup in it.
func finish(ctx context.Context, dir string, base Item, items []Item, nested []string, o Options, fail func(error) ([]Item, error)) ([]Item, error) {
	for _, n := range nested {
		sub, err := Unpack(ctx, n, Options{Work: o.Work, Password: o.Password, Progress: o.Progress})
		os.Remove(n)
		var np *NeedPassword
		if errors.As(err, &np) {
			np.File = base.Source
			return fail(np)
		}
		if err != nil {
			if ctx.Err() != nil {
				return fail(ctx.Err())
			}
			continue
		}
		for _, s := range sub {
			it := base
			it.Inner, it.Path, it.Bytes, it.Extracted = filepath.Base(n)+"/"+s.Inner, s.Path, s.Bytes, true
			items = append(items, it)
		}
	}
	if len(items) == 0 {
		return fail(&SkipError{Reason: "There is no SQL Server backup (.bak) inside this archive."})
	}
	return items, nil
}

func encrypted(err error) bool {
	var re sevenzip.ReadError
	if errors.As(err, &re) && re.Encrypted {
		return true
	}
	var rp *sevenzip.ReadError
	if errors.As(err, &rp) && rp.Encrypted {
		return true
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "password") || strings.Contains(s, "encrypt")
}

func unpackZip(ctx context.Context, file string, base Item, o Options) ([]Item, error) {
	zr, err := zip.OpenReader(file)
	if err != nil {
		return nil, &SkipError{Reason: "This .zip file cannot be read; it may be damaged or cut short."}
	}
	defer zr.Close()
	dir, err := outDir(o.Work, base.Name)
	if err != nil {
		return nil, err
	}
	fail := func(e error) ([]Item, error) {
		os.RemoveAll(dir)
		return nil, e
	}
	var items []Item
	var nested []string
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		name := entryName(f.Name)
		k, _ := kind(name)
		if k == "" {
			continue
		}
		if f.Flags&0x1 != 0 {
			return fail(&SkipError{Reason: "This .zip file is protected by a password, which the tool cannot open. Unpack it with 7-Zip or Windows and drop the .bak inside."})
		}
		rc, err := f.Open()
		if err != nil {
			return fail(&SkipError{Reason: "This .zip file cannot be read: " + err.Error()})
		}
		dst, n, err := writeEntry(ctx, dir, name, rc, int64(f.UncompressedSize64), o)
		rc.Close()
		if err != nil {
			if ctx.Err() != nil {
				return fail(ctx.Err())
			}
			return fail(&SkipError{Reason: "This .zip file is damaged (" + name + " could not be unpacked)."})
		}
		if k == "bak" {
			it := base
			it.Inner, it.Path, it.Bytes, it.Extracted = name, dst, n, true
			items = append(items, it)
		} else {
			nested = append(nested, dst)
		}
	}
	return finish(ctx, dir, base, items, nested, o, fail)
}
