// Command ledgerline-control is Ledgerline Control: the provider's own
// console. Every Ledgerline installation (the hosted server and brokers' own
// servers) reports in to it; it shows who runs what, AI credit used, bills
// customers and handles their support requests.
//
//	ledgerline-control -dev                       # in memory, for trying it out
//	LLCTL_DATA=/var/lib/llctl LLCTL_DATA_KEYS=1:... ledgerline-control
//	ledgerline-control keygen                     # a new data key
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"ledgerline/internal/control"
	"ledgerline/internal/docs"
	"ledgerline/internal/vault"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "keygen" {
		fmt.Println(vault.NewKey(1))
		return
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "ledgerline-control:", err)
		os.Exit(1)
	}
}

func run() error {
	dev := flag.Bool("dev", false, "development: keep everything in memory with a temporary key")
	addr := flag.String("addr", envOr("LLCTL_ADDR", ":8070"), "listen address")
	flag.Parse()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var inner docs.Store
	var kr *vault.Keyring
	spec := os.Getenv("LLCTL_DATA_KEYS")
	if p := os.Getenv("LLCTL_DATA_KEYS_FILE"); p != "" {
		b, err := os.ReadFile(p)
		if err != nil {
			return fmt.Errorf("data keys file: %w", err)
		}
		if st, err := os.Stat(p); err == nil && st.Mode().Perm()&0o077 != 0 {
			return fmt.Errorf("data keys file %s is readable by other users; run: chmod 600 %s", p, p)
		}
		spec = string(b)
	}
	dir := os.Getenv("LLCTL_DATA")
	switch {
	case dir != "":
		if strings.TrimSpace(spec) == "" {
			return errors.New("LLCTL_DATA is set but LLCTL_DATA_KEYS is not; make a key with: ledgerline-control keygen")
		}
		fs, err := control.OpenFileStore(dir)
		if err != nil {
			return err
		}
		defer fs.Close()
		inner = fs
		log.Info("data", "dir", dir)
	case *dev:
		inner = docs.NewMem()
		log.Warn("development mode: data is kept in memory and lost on exit")
	default:
		return errors.New("set LLCTL_DATA (a folder for Control's data) and LLCTL_DATA_KEYS, or pass -dev")
	}
	if strings.TrimSpace(spec) != "" {
		k, err := vault.ParseKeyring(spec, "LLCTL_DATA_KEYS")
		if err != nil {
			return err
		}
		kr = k
	} else {
		kr = vault.Ephemeral()
	}
	c := control.New(&vault.Docs{Inner: inner, V: vault.New(kr)})

	pw := os.Getenv("LLCTL_OWNER_PASSWORD")
	if *dev && pw == "" {
		pw = "owner-password"
	}
	if p, err := c.Bootstrap(ctx, envOr("LLCTL_OWNER", "owner"), os.Getenv("LLCTL_OWNER_NAME"), pw); err != nil {
		return err
	} else if p != "" {
		if os.Getenv("LLCTL_OWNER_PASSWORD") == "" && !*dev {
			fmt.Fprintf(os.Stderr, "\nFirst sign-in: user %q, one-time password %s (you'll set your own).\n\n", envOr("LLCTL_OWNER", "owner"), p)
		} else {
			log.Info("owner login created", "user", envOr("LLCTL_OWNER", "owner"))
		}
	}

	s := control.NewServer(c, log)
	cert, key := os.Getenv("LLCTL_TLS_CERT"), os.Getenv("LLCTL_TLS_KEY")
	s.Secure = cert != "" || os.Getenv("LLCTL_SECURE_COOKIES") == "1"
	srv := &http.Server{Addr: *addr, Handler: s, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 2 * time.Minute, WriteTimeout: 2 * time.Minute, IdleTimeout: 2 * time.Minute}
	errc := make(chan error, 1)
	go func() {
		log.Info("Ledgerline Control listening", "addr", *addr)
		if cert != "" {
			errc <- srv.ListenAndServeTLS(cert, key)
		} else {
			errc <- srv.ListenAndServe()
		}
	}()
	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		return srv.Shutdown(sctx)
	}
	return nil
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
