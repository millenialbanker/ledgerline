// Package pkg is the Ledgerline migration package: one encrypted file that
// the migration tool writes at the broker's office and Ledgerline reads at
// go-live.
//
// On disk a package is:
//
//	"LLMIG/1\n"
//	uint32 big-endian length of the header, then the header (JSON)
//	chunks: uint32 big-endian length, then AES-256-GCM ciphertext
//
// The plaintext is a zip archive. The key is scrypt(passphrase, salt). Each
// chunk's nonce is the header's 7-byte prefix, a 4-byte chunk counter and a
// final byte that is 1 on the last chunk only, so chunks cannot be dropped,
// reordered or the file cut short without the reader noticing. The header
// bytes are the additional data of every chunk.
//
// Inside the zip:
//
//	manifest.json                          Manifest
//	report.html                            the tool's report (optional)
//	firms/<firm>/master/<SOURCE>.csv       go-live files in Ledgerline's import layouts
//	                                       (SOURCE is a MasterSources name, e.g. CLIENT-MASTER)
//	firms/<firm>/history/<fy>/<KIND>.csv   read-only history of an earlier year (see History*)
//	firms/<firm>/documents/index.csv       one row per archived document (DocColumns)
//	firms/<firm>/documents/<fy>/<id>.<ext> the documents themselves
//
// A large office may split its documents over several packages; each one
// says which part it is in Manifest.Part.
package pkg

import (
	"archive/zip"
	"bufio"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/scrypt"
)

const (
	Magic     = "LLMIG/1\n"
	chunkSize = 1 << 20
	maxChunk  = chunkSize + 64
	maxHeader = 64 << 10
)

// History kinds: files under firms/<firm>/history/<fy>/.
const (
	HistoryLedger        = "LEDGER"         // every ledger line of the year
	HistoryBills         = "BILLS"          // bills and their charges
	HistoryTrades        = "TRADES"         // trades as billed
	HistoryContractNotes = "CONTRACT-NOTES" // contract note register
	HistoryReceipts      = "RECEIPTS-PAYMENTS"
	HistoryDP            = "DP-TRANSACTIONS"
	HistoryDPBills       = "DP-BILLS"
	HistoryHoldings      = "DP-HOLDINGS-YEAR-END"
)

// Kinds of file in the package.
const (
	KindMaster    = "master"
	KindHistory   = "history"
	KindDocIndex  = "documents-index"
	KindReport    = "report"
	KindDocuments = "documents" // summary entry per firm and year; documents are listed in the index
)

// DocColumns is the header of firms/<firm>/documents/index.csv.
var DocColumns = []string{"ID", "Path", "UCC", "Type", "Date", "FY", "Subject", "To", "Signed", "Format", "Bytes", "SHA256", "SourceRef", "RetainUntil"}

// Document types in the index.
const (
	DocContractNote = "Contract note"
	DocStatement    = "Statement" // ledger, running account, margin, holding statements
	DocDPStatement  = "DP statement"
	DocDPBill       = "DP bill"
	DocBill         = "Bill"
	DocMargin       = "Margin statement"
	DocKYC          = "KYC"
	DocOther        = "Other"
)

// Manifest describes a package.
type Manifest struct {
	Format  int       `json:"format"`
	Created time.Time `json:"created"`
	Tool    string    `json:"tool"`
	Part    *Part     `json:"part,omitempty"`
	Source  Source    `json:"source"`
	Firms   []Firm    `json:"firms"`
	Files   []File    `json:"files"`
	Checks  []Check   `json:"checks"`
	Dropped []Dropped `json:"dropped,omitempty"`
	Issues  []Issue   `json:"issues,omitempty"`
}

// Part says which part of a split package this is.
type Part struct {
	ID    string `json:"id"` // the same in every part of one migration
	Index int    `json:"index"`
	Of    int    `json:"of"`
	Note  string `json:"note,omitempty"` // e.g. "Documents 2019-20 to 2021-22"
}

// Source is what the package was made from.
type Source struct {
	System  string   `json:"system"` // "SharePro"
	Server  string   `json:"server,omitempty"`
	Backups []Backup `json:"backups"`
}

// Backup is one backup or database read.
type Backup struct {
	File       string    `json:"file,omitempty"` // file name as dropped (no folders)
	Database   string    `json:"database"`       // original database name
	Role       string    `json:"role"`           // company, admin, mail, other
	FY         string    `json:"fy,omitempty"`   // "2026-27"
	BackupDate time.Time `json:"backup_date,omitzero"`
	Bytes      int64     `json:"bytes,omitempty"`
	SHA256     string    `json:"sha256,omitempty"`
	Used       bool      `json:"used"`
	Note       string    `json:"note,omitempty"`
}

// Firm is one company in the old system.
type Firm struct {
	Code     string   `json:"code"` // the old system's company code
	Name     string   `json:"name"`
	Segments []string `json:"segments,omitempty"`
	Years    []string `json:"years,omitempty"` // financial years present
	AsOf     string   `json:"as_of,omitempty"` // date the go-live files are as of (YYYY-MM-DD)
}

// File is one file in the package.
type File struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Firm   string `json:"firm,omitempty"`
	FY     string `json:"fy,omitempty"`
	Source string `json:"source,omitempty"` // MasterSources name or History kind
	Rows   int    `json:"rows"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

// Check is a total the old system shows that Ledgerline must reproduce
// after loading, e.g. the trial balance.
type Check struct {
	Firm   string `json:"firm,omitempty"`
	FY     string `json:"fy,omitempty"`
	Name   string `json:"name"`  // see Check* names
	Value  string `json:"value"` // decimal or count as text
	Detail string `json:"detail,omitempty"`
}

// Check names Ledgerline knows how to verify.
const (
	CheckTBDebit      = "trial-balance-debit"
	CheckTBCredit     = "trial-balance-credit"
	CheckClients      = "clients"
	CheckClientDebit  = "client-debit-total"
	CheckClientCredit = "client-credit-total"
	CheckDPAccounts   = "dp-accounts"
	CheckDPQuantity   = "dp-holding-quantity"
	CheckDocuments    = "documents"
	CheckBankAccounts = "client-bank-accounts"
	CheckPositions    = "open-positions"
	// CheckOpeningDifference is the balance (debit positive) of the
	// suspense account that takes the old system's unbalanced openings.
	CheckOpeningDifference = "opening-difference"
)

// Dropped is a field deliberately not carried over.
type Dropped struct {
	Firm   string `json:"firm,omitempty"`
	Field  string `json:"field"`
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}

// Issue is a problem found while reading, with what to do about it.
type Issue struct {
	Firm     string `json:"firm,omitempty"`
	FY       string `json:"fy,omitempty"`
	Severity string `json:"severity"` // Error (blocks loading that file), Warning, Info
	Area     string `json:"area"`     // e.g. Client master
	Key      string `json:"key,omitempty"`
	Message  string `json:"message"`
	Action   string `json:"action,omitempty"`
}

// ---- key and chunks ----

type header struct {
	Format int    `json:"format"`
	KDF    string `json:"kdf"`
	N      int    `json:"n"`
	R      int    `json:"r"`
	P      int    `json:"p"`
	Salt   []byte `json:"salt"`
	Prefix []byte `json:"prefix"`
	Chunk  int    `json:"chunk"`
}

// Scrypt cost; tests lower it.
var scryptN = 1 << 15

func deriveKey(pass string, h header) ([]byte, error) {
	if h.KDF != "scrypt" || h.N < 1<<10 || h.N > 1<<20 || h.R < 1 || h.R > 32 || h.P < 1 || h.P > 16 || len(h.Salt) < 16 {
		return nil, errors.New("not a Ledgerline migration package, or made by a newer tool")
	}
	return scrypt.Key([]byte(pass), h.Salt, h.N, h.R, h.P, 32)
}

func nonce(prefix []byte, n uint32, last bool) []byte {
	b := make([]byte, 12)
	copy(b, prefix[:7])
	binary.BigEndian.PutUint32(b[7:11], n)
	if last {
		b[11] = 1
	}
	return b
}

type sealer struct {
	w      io.Writer
	aead   cipher.AEAD
	prefix []byte
	ad     []byte
	buf    []byte
	n      uint32
	closed bool
}

func (s *sealer) Write(p []byte) (int, error) {
	total := len(p)
	for len(p) > 0 {
		k := min(chunkSize-len(s.buf), len(p))
		s.buf = append(s.buf, p[:k]...)
		p = p[k:]
		if len(s.buf) == chunkSize {
			if err := s.flush(false); err != nil {
				return 0, err
			}
		}
	}
	return total, nil
}

// flush seals the buffered chunk. Close always seals a final chunk with the
// last flag set, even an empty one, so a file cut at a chunk boundary is
// still caught.
func (s *sealer) flush(last bool) error {
	if s.n == ^uint32(0) {
		return errors.New("package too large")
	}
	ct := s.aead.Seal(nil, nonce(s.prefix, s.n, last), s.buf, s.ad)
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], uint32(len(ct)))
	if _, err := s.w.Write(l[:]); err != nil {
		return err
	}
	if _, err := s.w.Write(ct); err != nil {
		return err
	}
	s.n++
	s.buf = s.buf[:0]
	return nil
}

func (s *sealer) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	return s.flush(true)
}

// ---- writer ----

// Writer builds a package.
type Writer struct {
	mu     sync.Mutex
	man    Manifest
	zw     *zip.Writer
	seal   *sealer
	open   bool
	seen   map[string]bool
	docIdx map[string]*docIdx
}

// Create starts a package on w, sealed with the passphrase.
func Create(w io.Writer, passphrase, tool string) (*Writer, error) {
	if len([]rune(passphrase)) < 10 {
		return nil, errors.New("the passphrase must be at least 10 characters")
	}
	h := header{Format: 1, KDF: "scrypt", N: scryptN, R: 8, P: 1, Salt: make([]byte, 16), Prefix: make([]byte, 7), Chunk: chunkSize}
	if _, err := rand.Read(h.Salt); err != nil {
		return nil, err
	}
	if _, err := rand.Read(h.Prefix); err != nil {
		return nil, err
	}
	hb, _ := json.Marshal(h)
	key, err := deriveKey(passphrase, h)
	if err != nil {
		return nil, err
	}
	blk, _ := aes.NewCipher(key)
	aead, _ := cipher.NewGCM(blk)
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], uint32(len(hb)))
	if _, err := io.WriteString(w, Magic); err != nil {
		return nil, err
	}
	if _, err := w.Write(l[:]); err != nil {
		return nil, err
	}
	if _, err := w.Write(hb); err != nil {
		return nil, err
	}
	s := &sealer{w: w, aead: aead, prefix: h.Prefix, ad: hb, buf: make([]byte, 0, chunkSize)}
	pw := &Writer{seal: s, zw: zip.NewWriter(s), seen: map[string]bool{}, docIdx: map[string]*docIdx{}}
	pw.man = Manifest{Format: 1, Created: time.Now().UTC(), Tool: tool}
	return pw, nil
}

// Manifest gives access to the manifest being built (source, firms, part).
// Files, checks, issues and dropped fields are better added through the
// Writer's methods, which are safe for concurrent use.
func (w *Writer) Manifest() *Manifest { return &w.man }

func cleanPath(p string) (string, error) {
	c := path.Clean(strings.ReplaceAll(p, "\\", "/"))
	if c == "." || strings.HasPrefix(c, "/") || strings.HasPrefix(c, "../") || c == ".." {
		return "", fmt.Errorf("bad path in package: %q", p)
	}
	return c, nil
}

// Entry is an open file in the package. Only one may be open at a time.
type Entry struct {
	w    *Writer
	f    File
	zw   io.Writer
	h    hash.Hash
	done bool
}

func (e *Entry) Write(p []byte) (int, error) {
	n, err := e.zw.Write(p)
	e.h.Write(p[:n])
	e.f.Bytes += int64(n)
	return n, err
}

// Close finishes the entry and records it in the manifest.
func (e *Entry) Close() error {
	if e.done {
		return nil
	}
	e.done = true
	e.f.SHA256 = hex.EncodeToString(e.h.Sum(nil))
	e.w.man.Files = append(e.w.man.Files, e.f)
	e.w.open = false
	e.w.mu.Unlock()
	return nil
}

// AddRows lets a caller that writes CSV through Entry record its row count.
func (e *Entry) AddRows(n int) { e.f.Rows += n }

// Begin opens a file. compress is false for files already compressed (PDF,
// images, zip). The Writer stays locked until the entry is closed.
func (w *Writer) Begin(f File, compress bool) (*Entry, error) {
	p, err := cleanPath(f.Path)
	if err != nil {
		return nil, err
	}
	w.mu.Lock()
	if w.seen[p] {
		w.mu.Unlock()
		return nil, fmt.Errorf("%s is already in the package", p)
	}
	w.seen[p] = true
	m := zip.Store
	if compress {
		m = zip.Deflate
	}
	zf, err := w.zw.CreateHeader(&zip.FileHeader{Name: p, Method: m, Modified: time.Now()})
	if err != nil {
		w.mu.Unlock()
		return nil, err
	}
	f.Path = p
	w.open = true
	return &Entry{w: w, f: f, zw: zf, h: sha256.New()}, nil
}

// CSV is a CSV file being written to the package.
type CSV struct {
	e  *Entry
	cw *csv.Writer
	bw *bufio.Writer
}

// Write adds one row.
func (c *CSV) Write(rec []string) error {
	c.e.f.Rows++
	return c.cw.Write(rec)
}

// Close finishes the file.
func (c *CSV) Close() error {
	c.cw.Flush()
	if err := c.cw.Error(); err != nil {
		return err
	}
	if err := c.bw.Flush(); err != nil {
		return err
	}
	return c.e.Close()
}

func (w *Writer) beginCSV(f File, header []string) (*CSV, error) {
	e, err := w.Begin(f, true)
	if err != nil {
		return nil, err
	}
	bw := bufio.NewWriterSize(e, 64<<10)
	c := &CSV{e: e, cw: csv.NewWriter(bw), bw: bw}
	if err := c.cw.Write(header); err != nil {
		return nil, err
	}
	return c, nil
}

// Master starts firms/<firm>/master/<source>.csv. header must be the
// Ledgerline import file's columns.
func (w *Writer) Master(firm, source string, header []string) (*CSV, error) {
	return w.beginCSV(File{Path: "firms/" + firm + "/master/" + source + ".csv", Kind: KindMaster, Firm: firm, Source: source}, header)
}

// History starts firms/<firm>/history/<fy>/<kind>.csv.
func (w *Writer) History(firm, fy, kind string, header []string) (*CSV, error) {
	return w.beginCSV(File{Path: "firms/" + firm + "/history/" + fy + "/" + kind + ".csv", Kind: KindHistory, Firm: firm, FY: fy, Source: kind}, header)
}

// DocMeta describes one archived document.
type DocMeta struct {
	ID          string // unique within the firm; letters, digits, - and _ only
	UCC         string
	Type        string // Doc* constant
	Date        time.Time
	FY          string
	Subject     string
	To          string // the address it was sent to, if it was mailed
	Signed      bool
	Format      string // pdf, html, xlsx, jpg ...
	SourceRef   string // where it came from in the old system (table and key)
	RetainUntil time.Time
}

var docIDOK = func(s string) bool {
	if s == "" || len(s) > 120 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// AddDocument stores one document and lists it in the firm's index. The
// index itself is written by Close, after the documents.
func (w *Writer) AddDocument(firm string, m DocMeta, data []byte) error {
	if !docIDOK(m.ID) {
		return fmt.Errorf("bad document id %q", m.ID)
	}
	ext := strings.ToLower(strings.TrimPrefix(m.Format, "."))
	if ext == "" {
		ext = "bin"
	}
	fy := m.FY
	if fy == "" {
		fy = "unknown"
	}
	p := "firms/" + firm + "/documents/" + fy + "/" + m.ID + "." + ext
	compress := !(ext == "pdf" || ext == "zip" || ext == "jpg" || ext == "jpeg" || ext == "png" || ext == "xlsx" || ext == "docx" || ext == "7z" || ext == "gz")
	e, err := w.Begin(File{Path: p, Kind: "document", Firm: firm, FY: m.FY}, compress)
	if err != nil {
		return err
	}
	if _, err := e.Write(data); err != nil {
		e.Close()
		return err
	}
	sum := e.h.Sum(nil)
	// documents are listed in the index, not one by one in the manifest
	e.done = true
	w.man.Files = w.manAddDocSummary(firm, m.FY, len(data))
	idx := w.docIndex(firm)
	rec := []string{m.ID, p, m.UCC, m.Type, day(m.Date), m.FY, m.Subject, m.To, yn(m.Signed), ext, fmt.Sprint(len(data)), hex.EncodeToString(sum), m.SourceRef, day(m.RetainUntil)}
	idx.rows = append(idx.rows, rec)
	w.open = false
	w.mu.Unlock()
	return nil
}

type docIdx struct{ rows [][]string }

// docIndex is called with w.mu held.
func (w *Writer) docIndex(firm string) *docIdx {
	d := w.docIdx[firm]
	if d == nil {
		d = &docIdx{}
		w.docIdx[firm] = d
	}
	return d
}

func (w *Writer) manAddDocSummary(firm, fy string, n int) []File {
	for i := range w.man.Files {
		f := &w.man.Files[i]
		if f.Kind == KindDocuments && f.Firm == firm && f.FY == fy {
			f.Rows++
			f.Bytes += int64(n)
			return w.man.Files
		}
	}
	return append(w.man.Files, File{Path: "firms/" + firm + "/documents/" + fy + "/", Kind: KindDocuments, Firm: firm, FY: fy, Rows: 1, Bytes: int64(n)})
}

func day(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02")
}

func yn(b bool) string {
	if b {
		return "Y"
	}
	return "N"
}

// AddRaw stores a small file such as report.html.
func (w *Writer) AddRaw(p, kind string, data []byte) error {
	e, err := w.Begin(File{Path: p, Kind: kind}, true)
	if err != nil {
		return err
	}
	if _, err := e.Write(data); err != nil {
		e.Close()
		return err
	}
	return e.Close()
}

func (w *Writer) AddCheck(c Check) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.man.Checks = append(w.man.Checks, c)
}

func (w *Writer) AddIssue(i Issue) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.man.Issues = append(w.man.Issues, i)
}

// AddDropped counts a field left out; repeated calls for the same firm and
// field add up.
func (w *Writer) AddDropped(firm, field, reason string, n int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for i := range w.man.Dropped {
		d := &w.man.Dropped[i]
		if d.Firm == firm && d.Field == field {
			d.Count += n
			return
		}
	}
	w.man.Dropped = append(w.man.Dropped, Dropped{Firm: firm, Field: field, Reason: reason, Count: n})
}

// Close writes the document indexes and the manifest and seals the package.
func (w *Writer) Close() error {
	w.mu.Lock()
	if w.open {
		w.mu.Unlock()
		return errors.New("a file in the package is still open")
	}
	firms := make([]string, 0, len(w.docIdx))
	for f := range w.docIdx {
		firms = append(firms, f)
	}
	sort.Strings(firms)
	w.mu.Unlock()
	for _, f := range firms {
		rows := w.docIdx[f].rows
		c, err := w.beginCSV(File{Path: "firms/" + f + "/documents/index.csv", Kind: KindDocIndex, Firm: f}, DocColumns)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if err := c.Write(r); err != nil {
				return err
			}
		}
		if err := c.Close(); err != nil {
			return err
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	mb, err := json.MarshalIndent(w.man, "", " ")
	if err != nil {
		return err
	}
	zf, err := w.zw.CreateHeader(&zip.FileHeader{Name: "manifest.json", Method: zip.Deflate, Modified: time.Now()})
	if err != nil {
		return err
	}
	if _, err := zf.Write(mb); err != nil {
		return err
	}
	if err := w.zw.Close(); err != nil {
		return err
	}
	return w.seal.Close()
}

// ---- reader ----

// ErrPassphrase means the passphrase is wrong (or the file was changed).
var ErrPassphrase = errors.New("the passphrase is wrong, or the package has been changed or damaged")

// Reader is an opened package. Its plaintext is kept in a temporary file
// that Close removes.
type Reader struct {
	Manifest Manifest
	tmp      *os.File
	zr       *zip.Reader
	byPath   map[string]*zip.File
	files    map[string]File
}

// Open decrypts the package at src into a temporary file in tmpDir and
// reads its manifest. maxBytes (0 for no limit) caps the decrypted size.
func Open(src io.Reader, passphrase, tmpDir string, maxBytes int64) (*Reader, error) {
	br := bufio.NewReaderSize(src, 1<<20)
	mg := make([]byte, len(Magic))
	if _, err := io.ReadFull(br, mg); err != nil || string(mg) != Magic {
		return nil, errors.New("this is not a Ledgerline migration package (.llmig)")
	}
	var l [4]byte
	if _, err := io.ReadFull(br, l[:]); err != nil {
		return nil, errors.New("the package is cut short")
	}
	hl := binary.BigEndian.Uint32(l[:])
	if hl == 0 || hl > maxHeader {
		return nil, errors.New("this is not a Ledgerline migration package (.llmig)")
	}
	hb := make([]byte, hl)
	if _, err := io.ReadFull(br, hb); err != nil {
		return nil, errors.New("the package is cut short")
	}
	var h header
	if err := json.Unmarshal(hb, &h); err != nil || h.Format != 1 || len(h.Prefix) != 7 {
		return nil, errors.New("this package was made by a newer migration tool; update Ledgerline")
	}
	key, err := deriveKey(passphrase, h)
	if err != nil {
		return nil, err
	}
	blk, _ := aes.NewCipher(key)
	aead, _ := cipher.NewGCM(blk)
	tmp, err := os.CreateTemp(tmpDir, "llmig-*.zip")
	if err != nil {
		return nil, err
	}
	fail := func(e error) (*Reader, error) {
		tmp.Close()
		os.Remove(tmp.Name())
		return nil, e
	}
	var n uint32
	var size int64
	var pending []byte
	for {
		_, err := io.ReadFull(br, l[:])
		if err == io.EOF {
			break
		}
		if err != nil {
			return fail(errors.New("the package is cut short"))
		}
		cl := binary.BigEndian.Uint32(l[:])
		if cl < 16 || cl > maxChunk {
			return fail(ErrPassphrase)
		}
		ct := make([]byte, cl)
		if _, err := io.ReadFull(br, ct); err != nil {
			return fail(errors.New("the package is cut short"))
		}
		if pending != nil {
			// the previous chunk was not the last one
			pt, err := aead.Open(nil, nonce(h.Prefix, n, false), pending, hb)
			if err != nil {
				return fail(ErrPassphrase)
			}
			if _, err := tmp.Write(pt); err != nil {
				return fail(err)
			}
			size += int64(len(pt))
			if maxBytes > 0 && size > maxBytes {
				return fail(errors.New("the package is larger than this server accepts"))
			}
			n++
		}
		pending = ct
	}
	if pending == nil {
		return fail(errors.New("the package is empty"))
	}
	pt, err := aead.Open(nil, nonce(h.Prefix, n, true), pending, hb)
	if err != nil {
		return fail(ErrPassphrase)
	}
	if _, err := tmp.Write(pt); err != nil {
		return fail(err)
	}
	size += int64(len(pt))
	zr, err := zip.NewReader(tmp, size)
	if err != nil {
		return fail(fmt.Errorf("the package's contents are damaged: %w", err))
	}
	r := &Reader{tmp: tmp, zr: zr, byPath: map[string]*zip.File{}, files: map[string]File{}}
	for _, f := range zr.File {
		r.byPath[f.Name] = f
	}
	mf := r.byPath["manifest.json"]
	if mf == nil {
		return fail(errors.New("the package has no manifest"))
	}
	rc, err := mf.Open()
	if err != nil {
		return fail(err)
	}
	err = json.NewDecoder(io.LimitReader(rc, 256<<20)).Decode(&r.Manifest)
	rc.Close()
	if err != nil {
		return fail(fmt.Errorf("the manifest is damaged: %w", err))
	}
	for _, f := range r.Manifest.Files {
		r.files[f.Path] = f
	}
	return r, nil
}

// Close removes the temporary plaintext.
func (r *Reader) Close() error {
	r.tmp.Close()
	return os.Remove(r.tmp.Name())
}

// Has says whether the package holds path p.
func (r *Reader) Has(p string) bool { return r.byPath[p] != nil }

// Open opens one file. For files listed in the manifest (and documents,
// which the index lists) the reader checks the SHA-256 at EOF and returns
// an error instead of io.EOF on a mismatch.
func (r *Reader) Open(p string) (io.ReadCloser, error) {
	zf := r.byPath[p]
	if zf == nil {
		return nil, fmt.Errorf("%s is not in the package", p)
	}
	rc, err := zf.Open()
	if err != nil {
		return nil, err
	}
	want := r.files[p].SHA256
	return &checked{rc: rc, h: sha256.New(), want: want, name: p}, nil
}

// OpenDocument opens a document listed in an index, checking its hash.
func (r *Reader) OpenDocument(p, sha string) (io.ReadCloser, error) {
	zf := r.byPath[p]
	if zf == nil {
		return nil, fmt.Errorf("%s is not in the package", p)
	}
	rc, err := zf.Open()
	if err != nil {
		return nil, err
	}
	return &checked{rc: rc, h: sha256.New(), want: sha, name: p}, nil
}

// ReadAll reads a whole file (for small files).
func (r *Reader) ReadAll(p string, max int64) ([]byte, error) {
	rc, err := r.Open(p)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("%s is too large", p)
	}
	return b, nil
}

// CSV reads a CSV file in the package: its header and then each row.
func (r *Reader) CSV(p string, fn func(header, row []string) error) error {
	rc, err := r.Open(p)
	if err != nil {
		return err
	}
	defer rc.Close()
	cr := csv.NewReader(bufio.NewReaderSize(rc, 64<<10))
	cr.FieldsPerRecord = -1
	cr.ReuseRecord = true
	head, err := cr.Read()
	if err != nil {
		return fmt.Errorf("%s: %w", p, err)
	}
	head = append([]string(nil), head...)
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		if err := fn(head, rec); err != nil {
			return err
		}
	}
}

// VerifyAll reads every file listed in the manifest and every document in
// the indexes, checking sizes and hashes. It returns the problems found.
func (r *Reader) VerifyAll() []string {
	var bad []string
	for _, f := range r.Manifest.Files {
		if f.Kind == KindDocuments {
			continue
		}
		rc, err := r.Open(f.Path)
		if err != nil {
			bad = append(bad, err.Error())
			continue
		}
		n, err := io.Copy(io.Discard, rc)
		rc.Close()
		if err != nil {
			bad = append(bad, err.Error())
		} else if n != f.Bytes {
			bad = append(bad, fmt.Sprintf("%s: size %d, manifest says %d", f.Path, n, f.Bytes))
		}
		if f.Kind == KindDocIndex {
			err := r.CSV(f.Path, func(h, row []string) error {
				if len(row) < len(DocColumns) {
					bad = append(bad, f.Path+": short row")
					return nil
				}
				rc, err := r.OpenDocument(row[1], row[11])
				if err != nil {
					bad = append(bad, err.Error())
					return nil
				}
				_, err = io.Copy(io.Discard, rc)
				rc.Close()
				if err != nil {
					bad = append(bad, err.Error())
				}
				return nil
			})
			if err != nil {
				bad = append(bad, err.Error())
			}
		}
	}
	return bad
}

type checked struct {
	rc   io.ReadCloser
	h    hash.Hash
	want string
	name string
}

func (c *checked) Read(p []byte) (int, error) {
	n, err := c.rc.Read(p)
	c.h.Write(p[:n])
	if err == io.EOF && c.want != "" && hex.EncodeToString(c.h.Sum(nil)) != c.want {
		return n, fmt.Errorf("%s does not match its checksum; the package is damaged", c.name)
	}
	return n, err
}

func (c *checked) Close() error { return c.rc.Close() }

// HistoryColumns are the headers of each history file. Amounts are rupees
// with two decimals, dates YYYY-MM-DD, quantities signed where noted. A
// column with nothing to put in it is left empty.
var HistoryColumns = map[string][]string{
	// one row per ledger line; Account is a Ledgerline account (CLIENT:<ucc>,
	// BANK:<code>, or the firm's own account code), UCC set on client lines
	HistoryLedger: {"Date", "VoucherType", "VoucherNo", "Account", "AccountName", "UCC", "Segment", "Narration", "Debit", "Credit", "ChequeNo", "ValueDate", "SourceRef"},
	// one row per bill (client, settlement, segment)
	HistoryBills: {"BillNo", "BillDate", "Exchange", "Segment", "Settlement", "UCC", "Turnover", "Brokerage", "STT", "ExchangeCharges", "SEBIFees", "StampDuty", "GST", "OtherCharges", "NetAmount", "DueDate", "SourceRef"},
	// one row per trade as billed; Side B or S
	HistoryTrades: {"TradeDate", "Exchange", "Segment", "Settlement", "UCC", "TradeNo", "OrderNo", "Symbol", "Series", "ISIN", "Instrument", "Expiry", "Strike", "OptionType", "Side", "Quantity", "Price", "Brokerage", "BillNo", "SourceRef"},
	// contract note register; DocumentID is the document's ID in the index when the note itself is archived
	HistoryContractNotes: {"CNNo", "Date", "UCC", "Exchange", "Segment", "SentOn", "SentTo", "Signed", "DocumentID", "SourceRef"},
	// Type Receipt or Payment
	HistoryReceipts: {"Date", "Type", "VoucherNo", "UCC", "Amount", "Mode", "Bank", "ChequeNo", "Reference", "Narration", "SourceRef"},
	// Quantity signed: + credit to the account, - debit
	HistoryDP:       {"Date", "BOID", "UCC", "ISIN", "Security", "TxnType", "Quantity", "Reference", "Counterparty", "Narration", "SourceRef"},
	HistoryDPBills:  {"BillNo", "BillDate", "BOID", "UCC", "PeriodFrom", "PeriodTo", "Amount", "GST", "Total", "SourceRef"},
	HistoryHoldings: {"Date", "BOID", "UCC", "ISIN", "Security", "Free", "Pledged", "Locked", "Total", "Rate", "Value"},
}
