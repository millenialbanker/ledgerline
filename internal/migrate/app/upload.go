package app

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"ledgerline/internal/migrate/run"
)

// ErrOffset means a chunk does not continue the file; the page resends
// from what was received.
var ErrOffset = errors.New("this piece does not follow on from what was received")

var idRe = regexp.MustCompile(`^[A-Za-z0-9_-]{6,40}$`)

var reserved = regexp.MustCompile(`(?i)^(con|prn|aux|nul|com[0-9]|lpt[0-9])(\..*)?$`)

// SafeRel checks a file name sent by the page (a dropped file's name, or
// its path inside a dropped folder, with / or \) and returns it as a
// relative path for this system. Absolute paths, drive letters, .. and
// names Windows cannot hold are refused.
func SafeRel(name string) (string, error) {
	bad := fmt.Errorf("the file name %q cannot be used", name)
	if name == "" || len(name) > 1024 || strings.ContainsRune(name, 0) {
		return "", bad
	}
	if strings.HasPrefix(name, "/") || strings.HasPrefix(name, `\`) {
		return "", bad
	}
	parts := strings.FieldsFunc(name, func(r rune) bool { return r == '/' || r == '\\' })
	if len(parts) == 0 || len(parts) > 20 {
		return "", bad
	}
	for _, p := range parts {
		if p == "." || p == ".." || strings.ContainsAny(p, `:*?"<>|`) || len(p) > 255 || reserved.MatchString(p) || strings.TrimRight(p, ". ") != p {
			return "", bad
		}
		for _, r := range p {
			if r < 32 {
				return "", bad
			}
		}
	}
	return filepath.Join(parts...), nil
}

// Upload stores one piece of a dropped file. A new file starts at offset
// 0; each piece must follow on from what was received.
//
// Files dropped together share a batch, so they land in one folder (the
// parts of a split .7z.001, .7z.002 ... must sit side by side).
func (s *Session) Upload(batch, id, name string, offset, size int64, body io.Reader) (int64, error) {
	if !idRe.MatchString(id) || !idRe.MatchString(batch) {
		return 0, errors.New("bad upload id")
	}
	rel, err := SafeRel(name)
	if err != nil {
		return 0, err
	}
	if size <= 0 || offset < 0 || offset > size {
		return 0, errors.New("bad size")
	}
	s.mu.Lock()
	if s.job != nil && s.job.Running {
		s.mu.Unlock()
		return 0, errBusy
	}
	var f *File
	for _, x := range s.files {
		if x.ID == id {
			f = x
		}
	}
	if f == nil {
		if offset != 0 {
			s.mu.Unlock()
			return 0, ErrOffset
		}
		if free := s.FreeWork(); free >= 0 && size+64<<20 > free {
			s.mu.Unlock()
			return 0, fmt.Errorf("not enough free space: %s needs %s but the work folder's disk has %s free. Free up space, or start the tool with a work folder on a bigger disk (--work)", filepath.Base(rel), run.Size(size), run.Size(free))
		}
		dir := filepath.Join(s.Work, "uploads", batch)
		f = &File{ID: id, Name: filepath.ToSlash(rel), Path: filepath.Join(dir, rel), Size: size, Status: "uploading"}
		if !run.WithinHere(dir, f.Path) {
			s.mu.Unlock()
			return 0, errors.New("bad file name")
		}
		for _, x := range s.files {
			if x.Path == f.Path {
				s.mu.Unlock()
				return 0, errors.New(filepath.Base(rel) + " was already dropped")
			}
		}
		if err := os.MkdirAll(filepath.Dir(f.Path), 0o700); err != nil {
			s.mu.Unlock()
			return 0, err
		}
		s.files = append(s.files, f)
	}
	if f.Local || f.Size != size || f.Name != filepath.ToSlash(rel) {
		s.mu.Unlock()
		return 0, errors.New("this upload id belongs to another file")
	}
	if f.Received != offset {
		got := f.Received
		s.mu.Unlock()
		return got, ErrOffset
	}
	if f.Status != "uploading" {
		got := f.Received
		s.mu.Unlock()
		return got, nil
	}
	path := f.Path
	f.Status = "receiving"
	s.mu.Unlock()

	n, werr := func() (int64, error) {
		fh, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE, 0o600)
		if err != nil {
			return 0, err
		}
		defer fh.Close()
		if _, err := fh.Seek(offset, io.SeekStart); err != nil {
			return 0, err
		}
		n, err := io.Copy(fh, io.LimitReader(body, size-offset))
		if err != nil {
			return n, err
		}
		// a piece cut short leaves the file at what arrived
		return n, fh.Truncate(offset + n)
	}()
	s.mu.Lock()
	defer s.mu.Unlock()
	f.Received = offset + n
	f.Status = "uploading"
	if werr != nil {
		return f.Received, werr
	}
	if f.Received == f.Size {
		f.Status = "ready"
	}
	return f.Received, nil
}
