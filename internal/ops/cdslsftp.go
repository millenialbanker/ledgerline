package ops

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// The SFTP client for CDSL's report server.
//
// The server's host key must be entered in the connection (sftp_hostkey):
// either its SHA256 fingerprint as ssh-keygen prints it ("SHA256:…") or the
// full key line ("ssh-ed25519 AAAA…"). A fetch without one, or with one
// that does not match, is refused, and the error shows the key the server
// offered so staff can check it with CDSL before saving it.
//
// Only regular files changed in the last three days are read, at most 200
// files and 64 MB each; the caller skips files already loaded.

const (
	sftpMaxFiles = 200
	sftpMaxBytes = 64 << 20
	sftpRecent   = 72 * time.Hour
	sftpTimeout  = 2 * time.Minute
)

func init() { cdslSFTPFetch = fetchSFTP }

func sftpHostKeyCheck(want string) ssh.HostKeyCallback {
	want = strings.TrimSpace(want)
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		fp := ssh.FingerprintSHA256(key)
		line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
		if want == "" {
			return fmt.Errorf("no host key saved for this server; it offered %s (%s). Confirm it with CDSL, then enter it as the host key", fp, key.Type())
		}
		ok := subtle.ConstantTimeCompare([]byte(want), []byte(fp)) == 1
		if !ok && strings.Contains(want, " ") {
			f := strings.Fields(want)
			l := strings.Fields(line)
			ok = len(f) >= 2 && len(l) >= 2 && f[0] == l[0] && f[1] == l[1]
		}
		if !ok {
			return fmt.Errorf("the server's host key %s does not match the one saved; do not continue until CDSL confirms the change", fp)
		}
		return nil
	}
}

func sftpAddr(host string) string {
	if _, _, err := net.SplitHostPort(host); err == nil {
		return host
	}
	return net.JoinHostPort(strings.Trim(host, "[]"), "22")
}

func fetchSFTP(ctx context.Context, cfg CDSLSFTPConfig) ([]CDSLReportFile, error) {
	ctx, cancel := context.WithTimeout(ctx, sftpTimeout)
	defer cancel()
	conf := &ssh.ClientConfig{
		User: cfg.User,
		Auth: []ssh.AuthMethod{ssh.Password(cfg.Password), ssh.KeyboardInteractive(func(_, _ string, qs []string, _ []bool) ([]string, error) {
			a := make([]string, len(qs))
			for i := range a {
				a[i] = cfg.Password
			}
			return a, nil
		})},
		HostKeyCallback: sftpHostKeyCheck(cfg.HostKey),
		Timeout:         30 * time.Second,
	}
	var d net.Dialer
	nc, err := d.DialContext(ctx, "tcp", sftpAddr(cfg.Host))
	if err != nil {
		return nil, fmt.Errorf("could not reach the SFTP server: %w", err)
	}
	stop := context.AfterFunc(ctx, func() { nc.Close() })
	defer stop()
	sc, chans, reqs, err := ssh.NewClientConn(nc, sftpAddr(cfg.Host), conf)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("SFTP login failed: %w", err)
	}
	client := ssh.NewClient(sc, chans, reqs)
	defer client.Close()
	fs, err := sftp.NewClient(client)
	if err != nil {
		return nil, fmt.Errorf("SFTP session failed: %w", err)
	}
	defer fs.Close()

	dir := cfg.Dir
	if dir == "" {
		dir = "/"
	}
	entries, err := fs.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("could not list %s: %w", dir, err)
	}
	cut := time.Now().Add(-sftpRecent)
	sort.Slice(entries, func(i, j int) bool { return entries[i].ModTime().Before(entries[j].ModTime()) })
	var out []CDSLReportFile
	var errs []error
	for _, e := range entries {
		if len(out) >= sftpMaxFiles {
			break
		}
		if !e.Mode().IsRegular() || e.ModTime().Before(cut) {
			continue
		}
		if e.Size() > sftpMaxBytes {
			errs = append(errs, fmt.Errorf("%s is larger than 64 MB and was skipped", e.Name()))
			continue
		}
		f, err := fs.Open(path.Join(dir, e.Name()))
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", e.Name(), err))
			continue
		}
		data, err := io.ReadAll(io.LimitReader(f, sftpMaxBytes+1))
		f.Close()
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", e.Name(), err))
			continue
		}
		if len(data) > sftpMaxBytes {
			errs = append(errs, fmt.Errorf("%s is larger than 64 MB and was skipped", e.Name()))
			continue
		}
		out = append(out, CDSLReportFile{Name: e.Name(), Data: data})
	}
	if len(out) == 0 && len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return out, nil
}
