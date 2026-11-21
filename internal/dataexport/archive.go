package dataexport

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"path"
	"strings"
	"time"

	"ledgerline/internal/sign"
)

// The archive is one ZIP. Every file in it is listed in manifest.json with
// its size and SHA-256; manifest.json is signed, and the signature is the
// file the manifest names (manifest.json.p7s, a CMS signature with the
// firm's document-signing certificate, or manifest.json.sig, an Ed25519
// signature whose public key is in the manifest and is also shown on the
// export screen and the destruction certificate, so it can be checked
// against a copy kept elsewhere).

// Format is the manifest format this package writes.
const Format = 1

// Kind marks a Ledgerline full export.
const Kind = "ledgerline-full-export"

// ManifestName is the manifest's path in the archive.
const ManifestName = "manifest.json"

// FileEntry is one file in the archive.
type FileEntry struct {
	Path    string `json:"path"`
	Kind    string `json:"kind"` // collection, history, table, file, audit, csv, index, readme, keys
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256"`
	Records int    `json:"records,omitempty"` // lines of a JSON Lines file, rows of a CSV
}

// AuditSummary is the audit trail as checked when the export was made.
type AuditSummary struct {
	Entries  int    `json:"entries"`
	Sealed   int    `json:"sealed"`
	Unsealed int    `json:"unsealed"`
	Checked  bool   `json:"checked"` // the seals were verified at export time
	OK       bool   `json:"ok"`
	Problems int    `json:"problems"`
	LastHash string `json:"last_hash,omitempty"`
	First    string `json:"first,omitempty"`
	Last     string `json:"last,omitempty"`
	// SealKeys says whether audit/seal-keys.json is included (final export
	// only), so the seals can be checked without Ledgerline.
	SealKeys bool `json:"seal_keys"`
}

// SignatureInfo says how the manifest is signed.
type SignatureInfo struct {
	Method            string `json:"method"` // cms, ed25519
	File              string `json:"file"`
	Signer            string `json:"signer"`
	PublicKey         string `json:"public_key,omitempty"`         // ed25519: base64 of the 32-byte public key
	CertificateSHA256 string `json:"certificate_sha256,omitempty"` // cms: the signing certificate
}

// Manifest describes an export.
type Manifest struct {
	Format            int            `json:"format"`
	Kind              string         `json:"kind"`
	ExportID          string         `json:"export_id"`
	Tenant            string         `json:"tenant"`
	Firm              string         `json:"firm"`
	FirmCode          string         `json:"firm_code,omitempty"`
	Created           string         `json:"created"`
	LedgerlineVersion string         `json:"ledgerline_version"`
	Backend           string         `json:"backend"`
	Final             bool           `json:"final"` // made for closing the account
	RequestedBy       string         `json:"requested_by"`
	ApprovedBy        string         `json:"approved_by,omitempty"`
	Collections       map[string]int `json:"collections"` // documents per collection
	Tables            map[string]int `json:"tables"`      // rows per table, as stored
	DocumentVersions  int            `json:"document_versions"`
	StoredFiles       int            `json:"stored_files"`
	StoredBytes       int64          `json:"stored_bytes"`
	Audit             AuditSummary   `json:"audit"`
	Signature         SignatureInfo  `json:"signature"`
	Files             []FileEntry    `json:"files"`
	// Period is set on an archive package of a past period.
	Period *PeriodInfo `json:"period,omitempty"`
}

// PeriodInfo describes the period an archive package holds.
type PeriodInfo struct {
	ArchiveID   string         `json:"archive_id"`
	From        string         `json:"from"`
	To          string         `json:"to"`
	Kinds       []string       `json:"kinds"`
	Rows        map[string]int `json:"rows"` // per table
	RetainUntil string         `json:"retain_until"`
	Clients     int            `json:"clients"`
	Backend     string         `json:"backend"`
}

// Archive writes the ZIP as a stream: each file is written once, in
// order, and hashed as it goes, so nothing is held in memory.
type Archive struct {
	zw    *zip.Writer
	at    time.Time
	files []FileEntry
	seen  map[string]bool
	cur   *FileEntry
	h     hash.Hash
	w     io.Writer
}

// NewArchive starts an archive on w. at is the time stamped on each file.
func NewArchive(w io.Writer, at time.Time) *Archive {
	return &Archive{zw: zip.NewWriter(w), at: at, seen: map[string]bool{}}
}

// CleanPath makes a safe path inside the archive: forward slashes, no
// leading slash, no "..", no control characters.
func CleanPath(p string) string {
	p = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '\\' || r == ':' || r == '*' || r == '?' || r == '"' || r == '<' || r == '>' || r == '|' {
			return '_'
		}
		return r
	}, p)
	p = path.Clean("/" + p)[1:]
	if p == "" || p == "." {
		p = "_"
	}
	return p
}

// Create starts the next file, ending the one before. A path already used
// gets a number added.
func (a *Archive) Create(name, kind string) (io.Writer, error) {
	if err := a.end(); err != nil {
		return nil, err
	}
	name = CleanPath(name)
	if name == ManifestName || strings.HasPrefix(name, ManifestName+".") {
		name = "_" + name
	}
	if a.seen[name] {
		ext := path.Ext(name)
		base := strings.TrimSuffix(name, ext)
		for i := 2; ; i++ {
			if n := fmt.Sprintf("%s-%d%s", base, i, ext); !a.seen[n] {
				name = n
				break
			}
		}
	}
	a.seen[name] = true
	zf, err := a.zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: a.at})
	if err != nil {
		return nil, err
	}
	a.cur = &FileEntry{Path: name, Kind: kind}
	a.h = sha256.New()
	a.w = io.MultiWriter(zf, a.h, countWriter{&a.cur.Size})
	return a.w, nil
}

type countWriter struct{ n *int64 }

func (c countWriter) Write(p []byte) (int, error) { *c.n += int64(len(p)); return len(p), nil }

// Current is the file being written ("" when none).
func (a *Archive) Current() string {
	if a.cur == nil {
		return ""
	}
	return a.cur.Path
}

// Count adds n records to the file being written.
func (a *Archive) Count(n int) {
	if a.cur != nil {
		a.cur.Records += n
	}
}

func (a *Archive) end() error {
	if a.cur == nil {
		return nil
	}
	a.cur.SHA256 = hex.EncodeToString(a.h.Sum(nil))
	a.files = append(a.files, *a.cur)
	a.cur, a.h, a.w = nil, nil, nil
	return nil
}

// Files lists the files written so far (the current one excluded).
func (a *Archive) Files() []FileEntry { return append([]FileEntry(nil), a.files...) }

// Ed25519Signer signs with a new key made for this export. The public key
// goes into the manifest (info.PublicKey) and is recorded with the export.
func Ed25519Signer() (info SignatureInfo, signer func([]byte) ([]byte, error)) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	info = SignatureInfo{Method: "ed25519", File: ManifestName + ".sig", Signer: "Ledgerline export key (Ed25519, made for this export)", PublicKey: base64.StdEncoding.EncodeToString(pub)}
	return info, func(m []byte) ([]byte, error) { return ed25519.Sign(priv, m), nil }
}

// Finish ends the last file, writes the manifest with every file listed,
// and its signature, and closes the ZIP. sigInfo goes into the manifest
// so it says how it is signed; the signer signs exactly the manifest's
// bytes. The manifest is signed before anything is written, so when the
// signer fails Finish can be called again with another one. It returns
// the manifest's bytes.
func (a *Archive) Finish(m Manifest, sigInfo SignatureInfo, signer func([]byte) ([]byte, error)) ([]byte, error) {
	if err := a.end(); err != nil {
		return nil, err
	}
	if m.Kind != PeriodKind {
		m.Kind = Kind
	}
	m.Format, m.Files, m.Signature = Format, a.Files(), sigInfo
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	b = append(b, '\n')
	sig, err := signer(b)
	if err != nil {
		return nil, fmt.Errorf("signing the manifest: %w", err)
	}
	zf, err := a.zw.CreateHeader(&zip.FileHeader{Name: ManifestName, Method: zip.Deflate, Modified: a.at})
	if err != nil {
		return nil, err
	}
	if _, err := zf.Write(b); err != nil {
		return nil, err
	}
	zf, err = a.zw.CreateHeader(&zip.FileHeader{Name: sigInfo.File, Method: zip.Store, Modified: a.at})
	if err != nil {
		return nil, err
	}
	if _, err := zf.Write(sig); err != nil {
		return nil, err
	}
	return b, a.zw.Close()
}

// ManifestSHA256 is the hash recorded for an export: of manifest.json's bytes.
func ManifestSHA256(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// Verified is the result of checking an archive.
type Verified struct {
	Manifest       Manifest
	ManifestSHA256 string
	Signer         string
	Files          int
}

// Verify checks an archive end to end: the manifest's signature, then every
// file listed (present, same size, same SHA-256) and no file unlisted.
// wantKey, when set, is the Ed25519 public key recorded for the export; an
// archive signed with another key fails. A CMS signature is checked
// cryptographically; whether its certificate is trusted is for the reader.
func Verify(r io.ReaderAt, size int64, wantKey string) (Verified, error) {
	return VerifyKind(r, size, wantKey, Kind)
}

// PeriodKind marks an archive package of a past period (ops/archive.go):
// the same ZIP, manifest and signature as a full export, holding one
// period's rows.
const PeriodKind = "ledgerline-period-archive"

// VerifyKind is Verify for an archive whose manifest says it is of kind.
func VerifyKind(r io.ReaderAt, size int64, wantKey, kind string) (Verified, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return Verified{}, fmt.Errorf("not a ZIP file: %w", err)
	}
	by := map[string]*zip.File{}
	for _, f := range zr.File {
		if by[f.Name] != nil {
			return Verified{}, fmt.Errorf("%s appears twice", f.Name)
		}
		by[f.Name] = f
	}
	read := func(name string) ([]byte, error) {
		f := by[name]
		if f == nil {
			return nil, fmt.Errorf("%s is missing", name)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return io.ReadAll(io.LimitReader(rc, 64<<20))
	}
	mb, err := read(ManifestName)
	if err != nil {
		return Verified{}, err
	}
	var m Manifest
	if err := json.Unmarshal(mb, &m); err != nil {
		return Verified{}, fmt.Errorf("manifest.json: %w", err)
	}
	if m.Kind != kind || m.Format != Format {
		if kind == PeriodKind {
			return Verified{}, errors.New("not a Ledgerline archive package")
		}
		return Verified{}, errors.New("not a Ledgerline full export")
	}
	out := Verified{Manifest: m, ManifestSHA256: ManifestSHA256(mb)}
	if m.Signature.File != ManifestName+".sig" && m.Signature.File != ManifestName+".p7s" {
		return out, errors.New("the manifest names no signature file")
	}
	sig, err := read(m.Signature.File)
	if err != nil {
		return out, err
	}
	switch m.Signature.Method {
	case "ed25519":
		pk, err := base64.StdEncoding.DecodeString(m.Signature.PublicKey)
		if err != nil || len(pk) != ed25519.PublicKeySize {
			return out, errors.New("the manifest's public key is not an Ed25519 key")
		}
		if wantKey != "" && wantKey != m.Signature.PublicKey {
			return out, errors.New("the manifest is signed with a different key from the one recorded for this export")
		}
		if !ed25519.Verify(ed25519.PublicKey(pk), mb, sig) {
			return out, errors.New("the manifest's signature does not match")
		}
		out.Signer = m.Signature.Signer
	case "cms":
		v, err := sign.Verify(sig, mb)
		if err != nil {
			return out, fmt.Errorf("the manifest's signature does not check: %w", err)
		}
		if m.Signature.CertificateSHA256 != "" && v.Signer.SHA256 != m.Signature.CertificateSHA256 {
			return out, errors.New("the manifest is signed with a different certificate from the one it names")
		}
		out.Signer = v.Signer.Name
	default:
		return out, fmt.Errorf("unknown signature method %q", m.Signature.Method)
	}
	listed := map[string]bool{ManifestName: true, m.Signature.File: true}
	for _, fe := range m.Files {
		listed[fe.Path] = true
		f := by[fe.Path]
		if f == nil {
			return out, fmt.Errorf("%s is listed but missing", fe.Path)
		}
		rc, err := f.Open()
		if err != nil {
			return out, err
		}
		h := sha256.New()
		n, err := io.Copy(h, rc)
		rc.Close()
		if err != nil {
			return out, fmt.Errorf("%s: %w", fe.Path, err)
		}
		if n != fe.Size || hex.EncodeToString(h.Sum(nil)) != fe.SHA256 {
			return out, fmt.Errorf("%s does not match the manifest", fe.Path)
		}
		out.Files++
	}
	for name := range by {
		if !listed[name] {
			return out, fmt.Errorf("%s is not in the manifest", name)
		}
	}
	return out, nil
}

// ReadFile returns one file of an archive, for tests and checks.
func ReadFile(r io.ReaderAt, size int64, name string) ([]byte, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, err
	}
	for _, f := range zr.File {
		if f.Name == name {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			var b bytes.Buffer
			_, err = io.Copy(&b, rc)
			return b.Bytes(), err
		}
	}
	return nil, fmt.Errorf("%s is not in the archive", name)
}
