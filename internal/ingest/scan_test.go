package ingest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeClamd speaks clamd's PING and INSTREAM. A stream holding the marker
// below is "infected" (no real malware or test virus is used); a stream
// over limit bytes gets clamd's size-limit error.
type fakeClamd struct {
	ln    net.Listener
	limit int
	mu    sync.Mutex
	scans int
}

const fakeMarker = "FAKE-MALWARE-MARKER"

func newFakeClamd(t *testing.T, network string) *fakeClamd {
	t.Helper()
	addr := "127.0.0.1:0"
	if network == "unix" {
		addr = filepath.Join(t.TempDir(), "clamd.sock")
	}
	ln, err := net.Listen(network, addr)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeClamd{ln: ln, limit: 1 << 20}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return f
}

func (f *fakeClamd) serve(c net.Conn) {
	defer c.Close()
	cmd := make([]byte, 0, 16)
	b := make([]byte, 1)
	for {
		if _, err := c.Read(b); err != nil {
			return
		}
		if b[0] == 0 {
			break
		}
		cmd = append(cmd, b[0])
	}
	switch string(cmd) {
	case "zPING":
		c.Write([]byte("PONG\x00"))
	case "zVERSION":
		c.Write([]byte("ClamAV 1.4.1/27470/Mon Nov 16 09:12:00 2026\x00"))
	case "zINSTREAM":
		f.mu.Lock()
		f.scans++
		f.mu.Unlock()
		var data []byte
		for {
			var n [4]byte
			if _, err := io.ReadFull(c, n[:]); err != nil {
				return
			}
			size := binary.BigEndian.Uint32(n[:])
			if size == 0 {
				break
			}
			if len(data)+int(size) > f.limit {
				c.Write([]byte("INSTREAM size limit exceeded. ERROR\x00"))
				return
			}
			chunk := make([]byte, size)
			if _, err := io.ReadFull(c, chunk); err != nil {
				return
			}
			data = append(data, chunk...)
		}
		if bytes.Contains(data, []byte(fakeMarker)) {
			c.Write([]byte("stream: Eicar-Signature FOUND\x00"))
		} else {
			c.Write([]byte("stream: OK\x00"))
		}
	default:
		c.Write([]byte("UNKNOWN COMMAND\x00"))
	}
}

func raw(source, name string, data []byte) RawFile {
	sum := sha256.Sum256(data)
	return RawFile{Tenant: "t", Source: source, Name: name, SHA256: hex.EncodeToString(sum[:]), Content: data}
}

func (f *fakeClamd) count() int { f.mu.Lock(); defer f.mu.Unlock(); return f.scans }

type logged struct{ actor, action string }

func scanStore(t *testing.T, sc Scanner, max int64) (*ScanningRawStore, *MemRawStore, *[]logged) {
	t.Helper()
	mem := NewMemRawStore()
	var log []logged
	s := &ScanningRawStore{RawStore: mem, Scanner: sc, Max: max,
		Kind: func(src string) string {
			switch src {
			case "NSE-FO-TRADE":
				return ScanBulk
			case "REPORT-OUT":
				return ScanSkip
			}
			return ScanDocument
		},
		Log: func(_ context.Context, tenant, actor, action, object string, d map[string]any) {
			log = append(log, logged{actor, action})
		}}
	return s, mem, &log
}

func TestParseClamd(t *testing.T) {
	for in, want := range map[string]string{"tcp://127.0.0.1:3310": "tcp 127.0.0.1:3310", "unix:///run/clamd.sock": "unix /run/clamd.sock", "/var/run/clamd.ctl": "unix /var/run/clamd.ctl", "clamav.internal:3310": "tcp clamav.internal:3310"} {
		c, err := ParseClamd(in)
		if err != nil || c.Network+" "+c.Address != want {
			t.Errorf("%s: %+v %v", in, c, err)
		}
	}
	for _, bad := range []string{"", "tcp://nohost", "unix://relative.sock", "http://x:1"} {
		if _, err := ParseClamd(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestScanningRawStore(t *testing.T) {
	ctx := WithActor(context.Background(), "priya.nair")
	for _, network := range []string{"tcp", "unix"} {
		fc := newFakeClamd(t, network)
		cl := &Clamd{Network: network, Address: fc.ln.Addr().String()}
		if err := cl.Ping(ctx); err != nil {
			t.Fatalf("%s ping: %v", network, err)
		}
		s, mem, log := scanStore(t, cl, 2<<20)

		// Clean: stored. Chunks over 64 KB are sent whole.
		clean := bytes.Repeat([]byte("clean bytes "), 20000)
		f, err := s.Put(ctx, raw("BOARD", "notes.txt", clean))
		if err != nil || f.ID == "" {
			t.Fatalf("%s clean: %v", network, err)
		}
		if got, _ := mem.Get(ctx, "t", f.ID); !bytes.Equal(got.Content, clean) {
			t.Fatal("clean file not kept")
		}
		// Infected: refused with the signature, audited, nothing stored.
		before := len(mem.files)
		_, err = s.Put(ctx, raw("BOARD", "invoice.pdf", []byte("%PDF "+fakeMarker)))
		var inf *InfectedError
		if !errors.As(err, &inf) || !errors.Is(err, ErrRejected) || err.Error() != "This file was refused: it contains Eicar-Signature" {
			t.Fatalf("%s infected: %v", network, err)
		}
		if len(mem.files) != before {
			t.Fatal("an infected file was stored")
		}
		if l := (*log)[len(*log)-1]; l.action != "file.malware_found" || l.actor != "priya.nair" {
			t.Fatalf("audit %+v", l)
		}
		// Ledgerline's own output is not scanned.
		n := fc.count()
		if _, err := s.Put(ctx, raw("REPORT-OUT", "r.csv", []byte(fakeMarker))); err != nil || fc.count() != n {
			t.Fatalf("%s skip: %v (%d scans)", network, err, fc.count()-n)
		}
	}
}

func TestScanningRawStoreLimits(t *testing.T) {
	ctx := context.Background()
	fc := newFakeClamd(t, "tcp")
	cl := &Clamd{Network: "tcp", Address: fc.ln.Addr().String()}
	s, mem, log := scanStore(t, cl, 3<<20)
	big := bytes.Repeat([]byte("x"), 4<<20) // over our limit
	// A document too large to scan is refused, not stored unscanned.
	if _, err := s.Put(ctx, raw("CLIENT-DOC", "scan.pdf", big)); !errors.Is(err, ErrRejected) || !strings.Contains(err.Error(), "larger than the malware scanner takes (3 MB)") {
		t.Fatalf("large document: %v", err)
	}
	// An exchange file over the limit is stored unscanned, and the audit says so.
	f, err := s.Put(ctx, raw("NSE-FO-TRADE", "trades.csv", big))
	if err != nil || f.ID == "" || (*log)[len(*log)-1].action != "file.not_scanned" || (*log)[len(*log)-1].actor != "system:malware-scan" {
		t.Fatalf("large exchange file: %v %+v", err, *log)
	}
	// clamd's own limit is lower than ours (fake: 1 MB): the same rules.
	mid := bytes.Repeat([]byte("y"), 2<<20)
	if _, err := s.Put(ctx, raw("BOARD", "big.zip", mid)); !errors.Is(err, ErrRejected) {
		t.Fatalf("over clamd's limit: %v", err)
	}
	if _, err := s.Put(ctx, raw("NSE-FO-TRADE", "t2.csv", mid)); err != nil {
		t.Fatalf("exchange file over clamd's limit: %v", err)
	}
	// Within the limit, an exchange file is scanned like any other.
	if _, err := s.Put(ctx, raw("NSE-FO-TRADE", "t3.csv", []byte(fakeMarker))); !errors.Is(err, ErrRejected) {
		t.Fatalf("infected exchange file: %v", err)
	}
	if len(mem.files) != 2 {
		t.Fatalf("%d files kept", len(mem.files))
	}
}

func TestScanningRawStoreFailsClosed(t *testing.T) {
	ctx := context.Background()
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close() // nothing listens there now
	cl := &Clamd{Network: "tcp", Address: addr}
	s, mem, log := scanStore(t, cl, 0)
	_, err := s.Put(ctx, raw("VENDOR-BILL", "bill.pdf", []byte("%PDF clean")))
	if !errors.Is(err, ErrScanUnavailable) || errors.Is(err, ErrRejected) || !strings.Contains(err.Error(), "cannot be reached") {
		t.Fatalf("unreachable: %v", err)
	}
	if len(mem.files) != 0 || (*log)[0].action != "file.scan_failed" {
		t.Fatalf("stored while the scanner was down: %d %+v", len(mem.files), *log)
	}
	if err := s.Ready(ctx); !errors.Is(err, ErrScanUnavailable) {
		t.Fatalf("ready: %v", err)
	}
	// Even an exchange file is not stored unscanned because the scanner is down.
	if _, err := s.Put(ctx, raw("NSE-FO-TRADE", "t.csv", []byte("a,b"))); !errors.Is(err, ErrScanUnavailable) {
		t.Fatalf("exchange file with the scanner down: %v", err)
	}
	// Not configured: the plain store, as before.
	var plain RawStore = NewMemRawStore()
	if _, err := plain.Put(ctx, raw("BOARD", "x", []byte(fakeMarker))); err != nil {
		t.Fatal(err)
	}
}

// The daemon's version is read for the Security panel.
func TestClamdVersion(t *testing.T) {
	f := newFakeClamd(t, "tcp")
	c, err := ParseClamd("tcp://" + f.ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	v, err := c.Version(context.Background())
	if err != nil || !strings.HasPrefix(v, "ClamAV 1.4.1/") {
		t.Fatalf("version %q %v", v, err)
	}
}
