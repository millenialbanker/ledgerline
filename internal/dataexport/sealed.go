// Package dataexport holds the parts of a firm's full data export that do
// not depend on the firm's records: the encrypted file the export is kept
// in on the server, the ZIP archive with its manifest of every file's size
// and SHA-256, the manifest's signature, and the check a firm (or a test)
// runs on an archive it received.
package dataexport

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Sealer encrypts for a firm (vault.Vault).
type Sealer interface {
	Seal(tenant string, aad, pt []byte) ([]byte, error)
	Open(tenant string, aad, b []byte) ([]byte, error)
}

// A sealed file is how an export (and the scratch files used to build it)
// rests on the server's disk, encrypted with the firm's keys like every
// other stored file:
//
//	LLSX1\n
//	{"v":1,"tenant":...,"label":...}\n             header, in clear
//	frames: type (1 byte) · length (4 bytes) · sealed bytes
//
// Data frames carry up to 1 MiB each. The last frame ('E') carries the
// number of bytes written, so a file cut short fails to open. Every frame
// is bound to the header, its position and its type (AES-256-GCM additional
// data), so frames cannot be reordered, swapped between files or dropped.
const (
	sealedMagic = "LLSX1\n"
	sealedChunk = 1 << 20
	frameData   = 'D'
	frameEnd    = 'E'
)

type sealedHeader struct {
	Format int    `json:"v"`
	Tenant string `json:"tenant"`
	Label  string `json:"label"`
}

func sealedAAD(head []byte, i uint64, typ byte) []byte {
	b := make([]byte, 0, len(head)+16)
	b = append(b, "llsx1\x00"...)
	b = append(b, head...)
	b = binary.BigEndian.AppendUint64(b, i)
	return append(b, typ)
}

// SealedWriter encrypts what is written to it, in frames. Close writes the
// end frame; a file without one does not open.
type SealedWriter struct {
	w      io.Writer
	s      Sealer
	tenant string
	head   []byte
	buf    []byte
	i      uint64
	n      int64 // plaintext bytes
	closed bool
}

// NewSealedWriter starts a sealed file for tenant. label names what the
// file is (an export ID), so one file cannot pass for another.
func NewSealedWriter(w io.Writer, s Sealer, tenant, label string) (*SealedWriter, error) {
	if s == nil {
		return nil, errors.New("dataexport: no encryption keys")
	}
	head, _ := json.Marshal(sealedHeader{Format: 1, Tenant: tenant, Label: label})
	if _, err := io.WriteString(w, sealedMagic); err != nil {
		return nil, err
	}
	if _, err := w.Write(append(head, '\n')); err != nil {
		return nil, err
	}
	return &SealedWriter{w: w, s: s, tenant: tenant, head: head, buf: make([]byte, 0, sealedChunk)}, nil
}

func (f *SealedWriter) Write(p []byte) (int, error) {
	if f.closed {
		return 0, errors.New("dataexport: write after close")
	}
	n := len(p)
	for len(p) > 0 {
		k := min(sealedChunk-len(f.buf), len(p))
		f.buf = append(f.buf, p[:k]...)
		p = p[k:]
		if len(f.buf) == sealedChunk {
			if err := f.frame(frameData, f.buf); err != nil {
				return 0, err
			}
			f.buf = f.buf[:0]
		}
	}
	f.n += int64(n)
	return n, nil
}

func (f *SealedWriter) frame(typ byte, pt []byte) error {
	ct, err := f.s.Seal(f.tenant, sealedAAD(f.head, f.i, typ), pt)
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
	return nil
}

// Size is the number of plaintext bytes written.
func (f *SealedWriter) Size() int64 { return f.n }

// Close writes what is buffered and the end frame. It does not close the
// underlying writer.
func (f *SealedWriter) Close() error {
	if f.closed {
		return nil
	}
	f.closed = true
	if len(f.buf) > 0 {
		if err := f.frame(frameData, f.buf); err != nil {
			return err
		}
		f.buf = f.buf[:0]
	}
	var end [8]byte
	binary.BigEndian.PutUint64(end[:], uint64(f.n))
	return f.frame(frameEnd, end[:])
}

// SealedReader decrypts a sealed file as it is read. It returns an error,
// never a short clean end, when the file is cut off or changed.
type SealedReader struct {
	r      *bufio.Reader
	s      Sealer
	tenant string
	head   []byte
	i      uint64
	buf    []byte
	n      int64
	done   bool
}

// NewSealedReader opens a sealed file written for tenant with label.
func NewSealedReader(r io.Reader, s Sealer, tenant, label string) (*SealedReader, error) {
	if s == nil {
		return nil, errors.New("dataexport: no encryption keys")
	}
	br := bufio.NewReaderSize(r, 1<<16)
	m := make([]byte, len(sealedMagic))
	if _, err := io.ReadFull(br, m); err != nil || string(m) != sealedMagic {
		return nil, errors.New("dataexport: not a sealed Ledgerline file")
	}
	line, err := br.ReadBytes('\n')
	if err != nil {
		return nil, errors.New("dataexport: the file's header is cut off")
	}
	head := line[:len(line)-1]
	var h sealedHeader
	if err := json.Unmarshal(head, &h); err != nil || h.Format != 1 {
		return nil, errors.New("dataexport: unknown sealed file format")
	}
	if h.Tenant != tenant || h.Label != label {
		return nil, fmt.Errorf("dataexport: the file is %s of firm %s, not %s of %s", h.Label, h.Tenant, label, tenant)
	}
	return &SealedReader{r: br, s: s, tenant: tenant, head: head}, nil
}

func (f *SealedReader) Read(p []byte) (int, error) {
	for len(f.buf) == 0 {
		if f.done {
			return 0, io.EOF
		}
		if err := f.next(); err != nil {
			return 0, err
		}
	}
	n := copy(p, f.buf)
	f.buf = f.buf[n:]
	return n, nil
}

func (f *SealedReader) next() error {
	var h [5]byte
	if _, err := io.ReadFull(f.r, h[:]); err != nil {
		return errors.New("dataexport: the file is cut off (no end frame)")
	}
	size := binary.BigEndian.Uint32(h[1:])
	if size > sealedChunk+1024 {
		return errors.New("dataexport: a frame is too large; the file is damaged")
	}
	ct := make([]byte, size)
	if _, err := io.ReadFull(f.r, ct); err != nil {
		return errors.New("dataexport: the file is cut off")
	}
	pt, err := f.s.Open(f.tenant, sealedAAD(f.head, f.i, h[0]), ct)
	if err != nil {
		return fmt.Errorf("dataexport: frame %d does not open: %w", f.i, err)
	}
	f.i++
	switch h[0] {
	case frameData:
		f.buf = pt
		f.n += int64(len(pt))
	case frameEnd:
		if len(pt) != 8 || int64(binary.BigEndian.Uint64(pt)) != f.n {
			return errors.New("dataexport: the file's length does not match its end frame")
		}
		if _, err := f.r.ReadByte(); err != io.EOF {
			return errors.New("dataexport: data after the end frame")
		}
		f.done = true
	default:
		return errors.New("dataexport: unknown frame type")
	}
	return nil
}
