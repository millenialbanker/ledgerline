// Command ledgerline-signer signs Ledgerline's contract notes and
// statements with the firm's digital signature certificate on a USB token
// (ePass2003, ProxKey, mToken, Watchdata, HyperPKI and the like). It runs
// on the office computer the token is plugged into, connects out to
// Ledgerline (nothing connects in), takes the digest of each document to
// sign and sends back the signature. Document contents never come here.
//
//	ledgerline-signer setup -server https://bo.example.in    save the address and agent key, choose the certificate
//	ledgerline-signer                                         start signing (asks the token PIN once)
//	ledgerline-signer choose                                  choose the token's certificate again
//	ledgerline-signer -selftest                               sign a sample digest and check it, then stop
//	ledgerline-signer -pfx signer.pfx                         sign with a certificate file instead of a token
//
// The settings are kept in %APPDATA%\Ledgerline Signer\config.json, with
// the agent key protected by Windows (DPAPI) for this Windows user. The
// PIN is kept in memory only, for as long as the program runs.
package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"ledgerline/internal/sign"
)

// version is the signer's version, sent to Ledgerline on every call.
const version = "1.0"

func main() {
	args := os.Args[1:]
	cmd := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("ledgerline-signer", flag.ExitOnError)
	cfgPath := fs.String("config", "", "settings file (default: "+defaultConfigPath()+")")
	server := fs.String("server", "", "setup: Ledgerline's address, https://…")
	key := fs.String("key", "", "setup: the agent key from Ledgerline (asked for when left out)")
	pfx := fs.String("pfx", "", "sign with this certificate file (.pfx, .p12 or PEM) instead of a USB token")
	selftest := fs.Bool("selftest", false, "sign a sample digest with the certificate and check it, then stop")
	_ = fs.Parse(args)
	if *cfgPath == "" {
		*cfgPath = defaultConfigPath()
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	in := bufio.NewReader(os.Stdin)
	var err error
	switch cmd {
	case "setup":
		err = setup(*cfgPath, *server, *key, *pfx, in)
	case "choose":
		err = choose(*cfgPath, in)
	case "", "run":
		err = run(ctx, *cfgPath, *pfx, *selftest, in)
	default:
		err = fmt.Errorf("unknown command %q (setup, choose, or nothing to start signing)", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "ledgerline-signer:", err)
		os.Exit(1)
	}
}

func say(s string) { fmt.Printf("%s  %s\n", time.Now().Format("15:04:05"), s) }

func setup(path, server, key, pfx string, in *bufio.Reader) error {
	srv, err := checkServer(server)
	if err != nil {
		return err
	}
	if key == "" {
		if key, err = readSecret("Agent key (from Ledgerline, Settings → Digital signature): ", in); err != nil {
			return err
		}
	}
	key = strings.TrimSpace(key)
	if !strings.HasPrefix(key, "llsign.") {
		return errors.New("that is not an agent key: it starts with llsign.")
	}
	prot, err := protect([]byte(key))
	if err != nil {
		return fmt.Errorf("protecting the agent key: %w", err)
	}
	c, _ := loadConfig(path)
	c.Server, c.Key, c.PFX = srv, prot, pfx
	if pfx == "" && tokenSupported {
		if c.Thumbprint, err = chooseCert(in); err != nil {
			return err
		}
	}
	if err := saveConfig(path, c); err != nil {
		return err
	}
	fmt.Println("Saved. Start signing with: ledgerline-signer")
	return nil
}

func choose(path string, in *bufio.Reader) error {
	c, err := loadConfig(path)
	if err != nil {
		return err
	}
	if c.Thumbprint, err = chooseCert(in); err != nil {
		return err
	}
	return saveConfig(path, c)
}

// openBackend opens the certificate file or the token, asking for its
// password or PIN once.
func openBackend(c config, pfx string, in *bufio.Reader) (backend, error) {
	if pfx = firstOf(pfx, c.PFX); pfx != "" {
		pw, err := readSecret("Password of "+pfx+": ", in)
		if err != nil {
			return nil, err
		}
		return openFile(pfx, pw)
	}
	if !tokenSupported {
		return nil, errors.New("USB tokens are read on Windows only; give a certificate file with -pfx")
	}
	if c.Thumbprint == "" {
		return nil, errors.New("no certificate chosen yet: run ledgerline-signer choose")
	}
	pin, err := readSecret("Token PIN: ", in)
	if err != nil {
		return nil, err
	}
	return openToken(c.Thumbprint, pin)
}

func run(ctx context.Context, path, pfx string, selftest bool, in *bufio.Reader) error {
	c, err := loadConfig(path)
	if err != nil && !(selftest && pfx != "") {
		return fmt.Errorf("%w (run ledgerline-signer setup first)", err)
	}
	be, err := openBackend(c, pfx, in)
	if err != nil {
		return err
	}
	defer be.Close()
	info := sign.CertInfo(be.Certificate())
	say(fmt.Sprintf("Certificate: %s, serial %s, issued by %s, valid until %s.", info.Name, info.Serial, info.Issuer, info.Until[:10]))
	if selftest {
		ms, err := selfTest(be)
		if err != nil {
			return fmt.Errorf("self-test failed: %w", err)
		}
		say(fmt.Sprintf("Self-test passed: the certificate signed a sample in %d ms and the signature checks.", ms))
		return nil
	}
	key, err := unprotect(c.Key)
	if err != nil {
		return fmt.Errorf("reading the agent key: %w (run ledgerline-signer setup again)", err)
	}
	host, _ := os.Hostname()
	cl := newClient(c.Server, string(key), host, be, say)
	say("Connecting to " + c.Server + ". Keep this window open while documents are signed; press Ctrl+C to stop.")
	return cl.run(ctx)
}

// selfTest signs a sample digest, checks the raw signature against the
// certificate and makes a CMS signature with it the way Ledgerline does.
func selfTest(be backend) (int64, error) {
	start := time.Now()
	sample := sha256.Sum256([]byte("Ledgerline Signer self-test"))
	sig, err := signDigest(be, sample[:])
	if err != nil {
		return 0, err
	}
	if err := verifyRaw(be.Certificate(), sample[:], sig); err != nil {
		return 0, err
	}
	content := []byte("Ledgerline Signer self-test document")
	cms, err := (&sign.Signer{Key: be, Cert: be.Certificate()}).SignDetached(content, time.Now())
	if err != nil {
		return 0, err
	}
	if _, err := sign.Verify(cms, content); err != nil {
		return 0, err
	}
	return time.Since(start).Milliseconds(), nil
}

func firstOf(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

// readLine reads one line from in.
func readLine(in *bufio.Reader) (string, error) {
	s, err := in.ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && s != "") {
		return "", err
	}
	return strings.TrimRight(s, "\r\n"), nil
}
