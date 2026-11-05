package ops

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// sftpTestServer serves dir over SFTP on a local port with one user.
func sftpTestServer(t *testing.T, dir string) (addr string, key ssh.PublicKey) {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	conf := &ssh.ServerConfig{PasswordCallback: func(c ssh.ConnMetadata, p []byte) (*ssh.Permissions, error) {
		if c.User() == "DP081600" && string(p) == "pw" {
			return nil, nil
		}
		return nil, os.ErrPermission
	}}
	conf.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				_, chans, reqs, err := ssh.NewServerConn(nc, conf)
				if err != nil {
					return
				}
				go ssh.DiscardRequests(reqs)
				for ch := range chans {
					c, in, err := ch.Accept()
					if err != nil {
						continue
					}
					go func() {
						for r := range in {
							r.Reply(r.Type == "subsystem" && len(r.Payload) > 4 && string(r.Payload[4:]) == "sftp", nil)
							if r.Type == "subsystem" {
								srv, _ := sftp.NewServer(c, sftp.WithServerWorkingDirectory(dir))
								srv.Serve()
								c.Close()
							}
						}
					}()
				}
			}()
		}
	}()
	return ln.Addr().String(), signer.PublicKey()
}

func TestFetchSFTPReal(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "DPG1_20261016.txt"), []byte("01|16102026\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "old.txt"), []byte("x"), 0o644)
	os.Chtimes(filepath.Join(dir, "old.txt"), time.Now().AddDate(0, 0, -10), time.Now().AddDate(0, 0, -10))
	os.Mkdir(filepath.Join(dir, "sub"), 0o755)
	addr, key := sftpTestServer(t, dir)
	ctx := context.Background()
	cfg := CDSLSFTPConfig{Host: addr, User: "DP081600", Password: "pw", Dir: dir}

	// No host key saved: refused, and the error names the key offered.
	if _, err := fetchSFTP(ctx, cfg); err == nil || !strings.Contains(err.Error(), ssh.FingerprintSHA256(key)) {
		t.Fatalf("no host key: %v", err)
	}
	cfg.HostKey = "SHA256:wrong"
	if _, err := fetchSFTP(ctx, cfg); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("wrong key: %v", err)
	}
	for _, hk := range []string{ssh.FingerprintSHA256(key), strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))) + " cdsl"} {
		cfg.HostKey = hk
		files, err := fetchSFTP(ctx, cfg)
		if err != nil || len(files) != 1 || files[0].Name != "DPG1_20261016.txt" || string(files[0].Data) != "01|16102026\n" {
			t.Fatalf("fetch with %q: %v %v", hk, files, err)
		}
	}
	cfg.Password = "bad"
	if _, err := fetchSFTP(ctx, cfg); err == nil || !strings.Contains(err.Error(), "login failed") {
		t.Fatalf("bad password: %v", err)
	}
}

func TestSFTPAddr(t *testing.T) {
	for in, want := range map[string]string{"sftp.cdslindia.com": "sftp.cdslindia.com:22", "h:2222": "h:2222", "::1": "[::1]:22"} {
		if got := sftpAddr(in); got != want {
			t.Errorf("%s: %s", in, got)
		}
	}
}
