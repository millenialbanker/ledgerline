package ingest

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

// Clamd is a ClamAV daemon (clamd), reached over TCP or a unix socket. Files
// are sent with its INSTREAM command: the bytes in chunks, each led by its
// length, then a zero length; clamd answers "stream: OK" or
// "stream: <signature> FOUND".
type Clamd struct {
	Network string        // "tcp" or "unix"
	Address string        // host:port, or the socket's path
	Timeout time.Duration // a whole scan (default 2 minutes)
}

// ErrScanTooLarge is clamd refusing a file over its StreamMaxLength.
var ErrScanTooLarge = errors.New("clamd: the file is larger than the scanner takes")

// ParseClamd reads LEDGERLINE_CLAMD: tcp://host:port or unix:///path (a bare
// host:port or absolute path also works).
func ParseClamd(s string) (*Clamd, error) {
	s = strings.TrimSpace(s)
	switch {
	case strings.HasPrefix(s, "tcp://"):
		s = strings.TrimPrefix(s, "tcp://")
		if _, _, err := net.SplitHostPort(s); err != nil {
			return nil, fmt.Errorf("LEDGERLINE_CLAMD: give tcp://host:port (%v)", err)
		}
		return &Clamd{Network: "tcp", Address: s}, nil
	case strings.HasPrefix(s, "unix://"):
		p := strings.TrimPrefix(s, "unix://")
		if !strings.HasPrefix(p, "/") {
			return nil, errors.New("LEDGERLINE_CLAMD: give unix:///path/to/clamd.sock")
		}
		return &Clamd{Network: "unix", Address: p}, nil
	case strings.HasPrefix(s, "/"):
		return &Clamd{Network: "unix", Address: s}, nil
	}
	if _, _, err := net.SplitHostPort(s); err == nil {
		return &Clamd{Network: "tcp", Address: s}, nil
	}
	return nil, fmt.Errorf("LEDGERLINE_CLAMD: %q is neither tcp://host:port nor unix:///path", s)
}

func (c *Clamd) String() string {
	if c.Network == "unix" {
		return "ClamAV at unix://" + c.Address
	}
	return "ClamAV at tcp://" + c.Address
}

func (c *Clamd) dial(ctx context.Context) (net.Conn, error) {
	t := c.Timeout
	if t <= 0 {
		t = 2 * time.Minute
	}
	d := net.Dialer{Timeout: 10 * time.Second}
	conn, err := d.DialContext(ctx, c.Network, c.Address)
	if err != nil {
		return nil, err
	}
	dl := time.Now().Add(t)
	if cd, ok := ctx.Deadline(); ok && cd.Before(dl) {
		dl = cd
	}
	_ = conn.SetDeadline(dl)
	return conn, nil
}

// reply reads clamd's answer, which ends with a NUL in the z-commands.
func reply(r io.Reader) (string, error) {
	var out []byte
	buf := make([]byte, 512)
	for len(out) < 4096 {
		n, err := r.Read(buf)
		out = append(out, buf[:n]...)
		if i := bytes.IndexByte(out, 0); i >= 0 {
			return strings.TrimSpace(string(out[:i])), nil
		}
		if err != nil {
			if len(out) > 0 {
				return strings.TrimSpace(string(out)), nil
			}
			return "", err
		}
	}
	return strings.TrimSpace(string(out)), nil
}

// Ping checks the daemon answers.
func (c *Clamd) Ping(ctx context.Context) error {
	conn, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("zPING\x00")); err != nil {
		return err
	}
	r, err := reply(conn)
	if err != nil {
		return err
	}
	if r != "PONG" {
		return fmt.Errorf("clamd answered %q to PING", r)
	}
	return nil
}

// Version is the daemon's engine and signature version, for example
// "ClamAV 1.4.1/27470/Mon Nov 16 09:12:00 2026".
func (c *Clamd) Version(ctx context.Context) (string, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("zVERSION\x00")); err != nil {
		return "", err
	}
	return reply(conn)
}

// Scan sends data to the daemon and reads its verdict.
func (c *Clamd) Scan(ctx context.Context, data []byte) (ScanResult, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return ScanResult{}, err
	}
	defer conn.Close()
	werr := func() error {
		if _, err := conn.Write([]byte("zINSTREAM\x00")); err != nil {
			return err
		}
		var n [4]byte
		for off := 0; off < len(data); off += 64 << 10 {
			end := min(off+64<<10, len(data))
			binary.BigEndian.PutUint32(n[:], uint32(end-off))
			if _, err := conn.Write(n[:]); err != nil {
				return err
			}
			if _, err := conn.Write(data[off:end]); err != nil {
				return err
			}
		}
		binary.BigEndian.PutUint32(n[:], 0)
		_, err := conn.Write(n[:])
		return err
	}()
	// clamd may stop reading part way (a file over its limit) and answer
	// at once, so the answer is read even when sending failed.
	r, rerr := reply(conn)
	if r == "" {
		if werr != nil {
			return ScanResult{}, werr
		}
		if rerr != nil {
			return ScanResult{}, rerr
		}
		return ScanResult{}, errors.New("clamd gave no answer")
	}
	return parseClamdReply(r)
}

func parseClamdReply(r string) (ScanResult, error) {
	msg := strings.TrimPrefix(r, "stream: ")
	switch {
	case msg == "OK":
		return ScanResult{}, nil
	case strings.HasSuffix(msg, " FOUND"):
		sig := strings.TrimSpace(strings.TrimSuffix(msg, " FOUND"))
		if sig == "" {
			sig = "malware"
		}
		return ScanResult{Infected: true, Signature: sig}, nil
	case strings.Contains(strings.ToLower(msg), "size limit exceeded"):
		return ScanResult{}, ErrScanTooLarge
	}
	return ScanResult{}, fmt.Errorf("clamd answered %q", r)
}
