// Package backup writes, checks and keeps firm backups.
//
// A backup is one file holding every row of one firm (see
// app.Snapshotter), compressed and encrypted with the firm's own keys:
//
//	LLBK1\n
//	{"v":1,"id":...,"tenant":...,"created":...}\n      header, in clear
//	frames: type (1 byte) · length (4 bytes) · sealed bytes
//
// Data frames carry up to 1 MiB of the gzip stream of the rows (one JSON
// record per line). The last frame carries the manifest: what was backed up,
// row counts per table and the SHA-256 of the rows. Each frame is sealed
// (AES-256-GCM) bound to the header, its position and its type, so a frame
// that is changed, reordered, swapped from another backup or cut off fails
// to open. A file is only as readable as the database it came from: it
// needs the same data keys.
//
// Next to each file is a small .json sidecar with the manifest and whether
// the file was read back and checked, so backups can be listed without
// decrypting them. The sidecar is a convenience: Verify trusts only the
// sealed manifest inside the file.
package backup

import (
	"bufio"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/app"
)

const (
	magic     = "LLBK1\n"
	chunkSize = 1 << 20
	frameData = 'D'
	frameEnd  = 'E'
	// Ext is a backup file's extension.
	Ext = ".llbak"
)

// Sealer encrypts for a firm (vault.Vault).
type Sealer interface {
	Seal(tenant string, aad, pt []byte) ([]byte, error)
	Open(tenant string, aad, b []byte) ([]byte, error)
}

// Tiers a backup can belong to.
const (
	Daily     = "daily"
	Weekly    = "weekly"
	Quarterly = "quarterly"
	Yearly    = "yearly"
	Manual    = "manual"
)

// Manifest describes a backup.
type Manifest struct {
	Format  int            `json:"v"`
	ID      string         `json:"id"`
	Tenant  string         `json:"tenant"`
	Firm    string         `json:"firm,omitempty"`
	Date    string         `json:"date"`  // the business day backed up
	Tiers   []string       `json:"tiers"` // daily, weekly, quarterly, yearly, manual
	Trigger string         `json:"trigger"`
	By      string         `json:"by"`
	Created string         `json:"created"`
	Backend string         `json:"backend"`
	Rows    map[string]int `json:"rows"`
	Total   int            `json:"total_rows"`
	Bytes   int64          `json:"bytes"` // uncompressed rows
	SHA256  string         `json:"sha256"`
	Millis  int64          `json:"ms"`
}

// Has reports whether the backup is in tier t.
func (m Manifest) Has(t string) bool {
	for _, x := range m.Tiers {
		if x == t {
			return true
		}
	}
	return false
}

type header struct {
	Format  int    `json:"v"`
	ID      string `json:"id"`
	Tenant  string `json:"tenant"`
	Created string `json:"created"`
}

func aad(head []byte, i uint64, typ byte) []byte {
	b := make([]byte, 0, len(head)+16)
	b = append(b, "llbk1\x00"...)
	b = append(b, head...)
	b = binary.BigEndian.AppendUint64(b, i)
	return append(b, typ)
}

// frameWriter seals what is written to it in chunks.
type frameWriter struct {
	w      io.Writer
	s      Sealer
	tenant string
	head   []byte
	buf    []byte
	i      uint64
	n      int64
}

func (f *frameWriter) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		k := min(chunkSize-len(f.buf), len(p))
		f.buf = append(f.buf, p[:k]...)
		p = p[k:]
		if len(f.buf) == chunkSize {
			if err := f.flush(frameData, f.buf); err != nil {
				return 0, err
			}
			f.buf = f.buf[:0]
		}
	}
	return n, nil
}

func (f *frameWriter) flush(typ byte, pt []byte) error {
	ct, err := f.s.Seal(f.tenant, aad(f.head, f.i, typ), pt)
	if err != nil {
		return err
	}
	var h [5]byte
	h[0] = typ
	binary.BigEndian.PutUint32(h[1:], uint32(len(ct)))
	if _, err := f.w.Write(h[:]); err != nil {
		return err
	}
	if _, err := f.w.Write(ct); err != nil {
		return err
	}
	f.i++
	f.n += int64(len(ct)) + 5
	return nil
}

// NewID returns a backup ID: the date, the time and a random suffix.
func NewID(date string, at time.Time) string {
	var r [3]byte
	_, _ = rand.Read(r[:])
	return date + "-" + at.Format("150405") + "-" + hex.EncodeToString(r[:])
}

// Write streams a snapshot of m.Tenant into w and returns the completed
// manifest. m.ID, Tenant, Date, Tiers, Trigger and By must be set.
func Write(ctx context.Context, w io.Writer, snap app.Snapshotter, s Sealer, m Manifest, now time.Time) (Manifest, error) {
	if snap == nil {
		return m, errors.New("backup: this storage cannot be backed up")
	}
	if m.ID == "" || m.Tenant == "" || m.Date == "" {
		return m, errors.New("backup: id, tenant and date are required")
	}
	start := time.Now()
	m.Format, m.Backend, m.Created = 1, snap.Backend(), now.UTC().Format(time.RFC3339)
	head, _ := json.Marshal(header{Format: 1, ID: m.ID, Tenant: m.Tenant, Created: m.Created})
	if _, err := io.WriteString(w, magic); err != nil {
		return m, err
	}
	if _, err := w.Write(append(head, '\n')); err != nil {
		return m, err
	}
	fw := &frameWriter{w: w, s: s, tenant: m.Tenant, head: head}
	gz, _ := gzip.NewWriterLevel(fw, gzip.BestSpeed)
	sum := sha256.New()
	out := io.MultiWriter(gz, sum)
	m.Rows = map[string]int{}
	m.Total, m.Bytes = 0, 0
	err := snap.Snapshot(ctx, m.Tenant, func(r app.Record) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		line, err := json.Marshal(r)
		if err != nil {
			return err
		}
		line = append(line, '\n')
		if _, err := out.Write(line); err != nil {
			return err
		}
		m.Rows[r.Table]++
		m.Total++
		m.Bytes += int64(len(line))
		return nil
	})
	if err != nil {
		return m, fmt.Errorf("backup: reading the data: %w", err)
	}
	if err := gz.Close(); err != nil {
		return m, err
	}
	if len(fw.buf) > 0 {
		if err := fw.flush(frameData, fw.buf); err != nil {
			return m, err
		}
	}
	m.SHA256 = hex.EncodeToString(sum.Sum(nil))
	m.Millis = time.Since(start).Milliseconds()
	mb, _ := json.Marshal(m)
	if err := fw.flush(frameEnd, mb); err != nil {
		return m, err
	}
	return m, nil
}

// frameReader opens frames and yields the data.
type frameReader struct {
	r      *bufio.Reader
	s      Sealer
	tenant string
	head   []byte
	i      uint64
	cur    []byte
	end    []byte
	done   bool
}

func (f *frameReader) Read(p []byte) (int, error) {
	for len(f.cur) == 0 {
		if f.done {
			return 0, io.EOF
		}
		if err := f.next(); err != nil {
			return 0, err
		}
	}
	n := copy(p, f.cur)
	f.cur = f.cur[n:]
	return n, nil
}

func (f *frameReader) next() error {
	var h [5]byte
	if _, err := io.ReadFull(f.r, h[:]); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return errors.New("backup: the file is cut short (its last part is missing)")
		}
		return err
	}
	typ, n := h[0], binary.BigEndian.Uint32(h[1:])
	if typ != frameData && typ != frameEnd {
		return fmt.Errorf("backup: part %d is damaged", f.i+1)
	}
	if n > chunkSize+1<<16 {
		return fmt.Errorf("backup: part %d is damaged (impossible size)", f.i+1)
	}
	ct := make([]byte, n)
	if _, err := io.ReadFull(f.r, ct); err != nil {
		return errors.New("backup: the file is cut short (its last part is missing)")
	}
	pt, err := f.s.Open(f.tenant, aad(f.head, f.i, typ), ct)
	if err != nil {
		return fmt.Errorf("backup: part %d does not open: the file was changed, or these are not the keys it was made with (%v)", f.i+1, err)
	}
	f.i++
	if typ == frameEnd {
		f.end, f.done = pt, true
		if _, err := f.r.ReadByte(); err == nil {
			return errors.New("backup: there is extra data after the end of the backup")
		}
		return nil
	}
	f.cur = pt
	return nil
}

// Read checks a backup from start to end and passes each row to fn (which
// may be nil). It fails unless every part opens with the firm's keys, the
// rows match the sealed manifest's counts and checksum, and nothing follows
// the end.
func Read(r io.Reader, s Sealer, fn func(app.Record) error) (Manifest, error) {
	br := bufio.NewReaderSize(r, 1<<16)
	mg := make([]byte, len(magic))
	if _, err := io.ReadFull(br, mg); err != nil || string(mg) != magic {
		return Manifest{}, errors.New("backup: not a Ledgerline backup file")
	}
	head, err := br.ReadBytes('\n')
	if err != nil {
		return Manifest{}, errors.New("backup: the header is damaged")
	}
	head = head[:len(head)-1]
	var h header
	if err := json.Unmarshal(head, &h); err != nil || h.Format != 1 || h.Tenant == "" {
		return Manifest{}, errors.New("backup: the header is damaged or from a newer version")
	}
	fr := &frameReader{r: br, s: s, tenant: h.Tenant, head: head}
	gz, err := gzip.NewReader(fr)
	if err != nil {
		if strings.HasPrefix(err.Error(), "backup:") {
			return Manifest{}, err
		}
		return Manifest{}, fmt.Errorf("backup: %w", err)
	}
	sum := sha256.New()
	lines := bufio.NewReaderSize(io.TeeReader(gz, sum), 1<<16)
	rows, total := map[string]int{}, 0
	for {
		line, err := lines.ReadBytes('\n')
		if len(line) > 0 {
			var rec app.Record
			if jerr := json.Unmarshal(line, &rec); jerr != nil || rec.Table == "" {
				return Manifest{}, fmt.Errorf("backup: row %d is damaged", total+1)
			}
			rows[rec.Table]++
			total++
			if fn != nil {
				if err := fn(rec); err != nil {
					return Manifest{}, err
				}
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Manifest{}, err
		}
	}
	// Drain to the end frame.
	if _, err := io.Copy(io.Discard, fr); err != nil {
		return Manifest{}, err
	}
	if !fr.done {
		return Manifest{}, errors.New("backup: the file is cut short (its last part is missing)")
	}
	var m Manifest
	if err := json.Unmarshal(fr.end, &m); err != nil {
		return Manifest{}, errors.New("backup: the manifest is damaged")
	}
	if m.ID != h.ID || m.Tenant != h.Tenant {
		return m, errors.New("backup: the manifest does not belong to this file")
	}
	if got := hex.EncodeToString(sum.Sum(nil)); got != m.SHA256 {
		return m, fmt.Errorf("backup: checksum mismatch: rows hash to %s, the manifest says %s", got[:12], short(m.SHA256))
	}
	if total != m.Total {
		return m, fmt.Errorf("backup: %d rows read, the manifest says %d", total, m.Total)
	}
	for t, n := range m.Rows {
		if rows[t] != n {
			return m, fmt.Errorf("backup: %s has %d rows, the manifest says %d", t, rows[t], n)
		}
	}
	return m, nil
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// ---- a folder of backups ----

// Entry is one backup in a folder: the sidecar.
type Entry struct {
	Manifest
	File        string `json:"file"`
	Size        int64  `json:"size"`
	Verified    string `json:"verified,omitempty"`
	VerifyError string `json:"verify_error,omitempty"`
}

// Dir keeps backups under Path/<tenant>/.
type Dir struct {
	Path string
}

var reTenant = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

func (d Dir) folder(tenant string) (string, error) {
	if !reTenant.MatchString(tenant) {
		return "", fmt.Errorf("backup: bad firm id %q", tenant)
	}
	return filepath.Join(d.Path, tenant), nil
}

var reID = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}-\d{6}-[0-9a-f]{6}$`)

func (d Dir) file(tenant, id string) (string, error) {
	if !reID.MatchString(id) {
		return "", fmt.Errorf("backup: bad backup id %q", id)
	}
	f, err := d.folder(tenant)
	if err != nil {
		return "", err
	}
	return filepath.Join(f, "ledgerline-"+tenant+"-"+id+Ext), nil
}

// Create writes a new backup and checks it by reading it back. The file
// appears under its final name only once it is complete and on disk.
func (d Dir) Create(ctx context.Context, snap app.Snapshotter, s Sealer, m Manifest, now time.Time) (Entry, error) {
	path, err := d.file(m.Tenant, m.ID)
	if err != nil {
		return Entry{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return Entry{}, err
	}
	tmp := path + ".partial"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return Entry{}, err
	}
	bw := bufio.NewWriterSize(f, 1<<20)
	m, err = Write(ctx, bw, snap, s, m, now)
	if err == nil {
		err = bw.Flush()
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return Entry{Manifest: m}, err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return Entry{Manifest: m}, err
	}
	e := Entry{Manifest: m, File: filepath.Base(path)}
	if st, err := os.Stat(path); err == nil {
		e.Size = st.Size()
	}
	e, err = d.verify(e, s, now)
	if serr := d.writeSidecar(e); err == nil {
		err = serr
	}
	return e, err
}

// Verify reads a backup end to end and records the result in its sidecar.
func (d Dir) Verify(tenant, id string, s Sealer, now time.Time) (Entry, error) {
	es, err := d.List(tenant)
	if err != nil {
		return Entry{}, err
	}
	for _, e := range es {
		if e.ID == id {
			e, err = d.verify(e, s, now)
			if serr := d.writeSidecar(e); err == nil {
				err = serr
			}
			return e, err
		}
	}
	return Entry{}, fmt.Errorf("backup %s not found", id)
}

func (d Dir) verify(e Entry, s Sealer, now time.Time) (Entry, error) {
	path, err := d.file(e.Tenant, e.ID)
	if err != nil {
		return e, err
	}
	f, err := os.Open(path)
	if err != nil {
		e.Verified, e.VerifyError = "", "file missing: "+err.Error()
		return e, errors.New(e.VerifyError)
	}
	defer f.Close()
	m, err := Read(f, s, nil)
	if err == nil && (m.ID != e.ID || m.SHA256 != e.SHA256 && e.SHA256 != "") {
		err = errors.New("backup: the file does not match its listing")
	}
	if err != nil {
		e.Verified, e.VerifyError = "", err.Error()
		return e, err
	}
	e.Manifest = m
	e.Verified, e.VerifyError = now.UTC().Format(time.RFC3339), ""
	return e, nil
}

func (d Dir) writeSidecar(e Entry) error {
	path, err := d.file(e.Tenant, e.ID)
	if err != nil {
		return err
	}
	b, _ := json.MarshalIndent(e, "", "  ")
	tmp := path + ".json.partial"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path+".json")
}

// List returns a firm's backups, newest first. Sidecars without their
// backup file are listed with an error so a missing file is noticed.
func (d Dir) List(tenant string) ([]Entry, error) {
	dir, err := d.folder(tenant)
	if err != nil {
		return nil, err
	}
	names, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return []Entry{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []Entry{}
	for _, n := range names {
		if !strings.HasSuffix(n.Name(), Ext+".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, n.Name()))
		if err != nil {
			continue
		}
		var e Entry
		if json.Unmarshal(b, &e) != nil || e.Tenant != tenant {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, strings.TrimSuffix(n.Name(), ".json"))); err != nil {
			e.Verified, e.VerifyError = "", "the backup file is missing"
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Date != out[j].Date {
			return out[i].Date > out[j].Date
		}
		return out[i].Created > out[j].Created
	})
	return out, nil
}

// PathOf returns the file of a backup, for restoring or copying.
func (d Dir) PathOf(tenant, id string) (string, error) { return d.file(tenant, id) }

// Delete removes a backup and its sidecar.
func (d Dir) Delete(tenant, id string) error {
	path, err := d.file(tenant, id)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Remove(path + ".json"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Keep is how many backups each tier keeps.
type Keep struct {
	Daily     int `json:"daily"`
	Weekly    int `json:"weekly"`
	Quarterly int `json:"quarterly"`
	Yearly    int `json:"yearly"`
	Manual    int `json:"manual"`
}

// Of returns the count for a tier.
func (k Keep) Of(t string) int {
	switch t {
	case Daily:
		return k.Daily
	case Weekly:
		return k.Weekly
	case Quarterly:
		return k.Quarterly
	case Yearly:
		return k.Yearly
	case Manual:
		return k.Manual
	}
	return 0
}

// Prune decides which backups to delete. Of several good backups of the
// same day only the newest counts. Each tier then keeps its newest days up
// to its count; a backup in several tiers stays while any of them keeps
// it. The newest good backup is never deleted, and nothing is deleted on
// account of a newer backup that failed its check.
func Prune(es []Entry, k Keep) (drop []Entry) {
	good := func(e Entry) bool { return e.Verified != "" && e.VerifyError == "" }
	sorted := append([]Entry(nil), es...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Date != sorted[j].Date {
			return sorted[i].Date > sorted[j].Date
		}
		return sorted[i].Created > sorted[j].Created
	})
	keep := map[string]bool{}
	superseded := map[string]bool{}
	newestOfDay := map[string]bool{} // date+kind seen with a good backup
	for _, e := range sorted {
		kind := "tier"
		if e.Has(Manual) && len(e.Tiers) == 1 {
			kind = "manual"
		}
		key := e.Date + "|" + kind
		if !good(e) {
			continue
		}
		if newestOfDay[key] {
			superseded[e.ID] = true
			continue
		}
		newestOfDay[key] = true
	}
	for _, t := range []string{Daily, Weekly, Quarterly, Yearly, Manual} {
		n := 0
		for _, e := range sorted {
			if !good(e) || superseded[e.ID] || !e.Has(t) {
				continue
			}
			if n < k.Of(t) {
				keep[e.ID] = true
			}
			n++
		}
	}
	for _, e := range sorted {
		if good(e) {
			keep[e.ID] = true // the newest good backup
			break
		}
	}
	for _, e := range sorted {
		switch {
		case keep[e.ID]:
		case !good(e):
			// A failed backup goes once a good one of a later or the same
			// day exists, so failures don't pile up.
			for _, x := range sorted {
				if good(x) && x.Date >= e.Date {
					drop = append(drop, e)
					break
				}
			}
		default:
			drop = append(drop, e)
		}
	}
	return drop
}
