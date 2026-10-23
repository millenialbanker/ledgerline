// Command ledgerline-kyc is Ledgerline KYC: Aadhaar (and PAN) for account
// opening, from DigiLocker or from an Aadhaar offline e-KYC file the
// investor uploads, as a service for any back office.
//
//	ledgerline-kyc -config kyc.json -data /var/lib/ledgerline-kyc
//	ledgerline-kyc -simulate                  practice DigiLocker, demo customer
//	ledgerline-kyc key                        make an API key for a customer
//
// Environment: LLKYC_ADDR (default :8090), LLKYC_CONFIG, LLKYC_DATA_DIR,
// LLKYC_DATA_KEYS or LLKYC_DATA_KEYS_FILE (encryption keys, as for
// Ledgerline: "ledgerctl keygen"), LLKYC_UIDAI_CERTS (UIDAI's offline e-KYC
// certificate files, comma-separated; or "uidai_certificates" in the
// config), LLKYC_PUBLIC_URL (overrides the
// config's), LLKYC_DIGILOCKER_URL (a DigiLocker gateway; default
// production).
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"ledgerline/internal/kyc"
	"ledgerline/internal/vault"
)

// DemoKey is the simulated customer's API key; never valid with a config file.
const DemoKey = "dev-kyc-key-not-for-production"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "key" {
		b := make([]byte, 24)
		_, _ = rand.Read(b)
		k := "llkyc_" + hex.EncodeToString(b)
		h := sha256.Sum256([]byte(k))
		fmt.Printf("API key (give to the customer once):  %s\napi_key_sha256 (put in the config):   %x\n", k, h)
		return
	}
	addr := flag.String("addr", envOr("LLKYC_ADDR", ":8090"), "listen address")
	cfgPath := flag.String("config", os.Getenv("LLKYC_CONFIG"), "configuration JSON (customers, DigiLocker registrations)")
	data := flag.String("data", envOr("LLKYC_DATA_DIR", filepath.Join(os.TempDir(), "ledgerline-kyc")), "folder for sessions and usage")
	sim := flag.Bool("simulate", os.Getenv("LLKYC_SIMULATE") == "1", "use the practice DigiLocker (demos and tests)")
	flag.Parse()
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(log, *addr, *cfgPath, *data, *sim); err != nil {
		log.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func run(log *slog.Logger, addr, cfgPath, data string, simulate bool) error {
	var cfg kyc.Config
	switch {
	case cfgPath != "":
		st, err := os.Stat(cfgPath)
		if err != nil {
			return err
		}
		if st.Mode().Perm()&0o077 != 0 {
			return fmt.Errorf("%s holds DigiLocker secrets and is readable by other users; run: chmod 600 %s", cfgPath, cfgPath)
		}
		b, err := os.ReadFile(cfgPath)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(b, &cfg); err != nil {
			return fmt.Errorf("config: %w", err)
		}
	case simulate:
		h := sha256.Sum256([]byte(DemoKey))
		cfg.Customers = []kyc.Customer{{ID: "demo", Name: "Demo broker", APIKeySHA256: hex.EncodeToString(h[:]),
			DigiLocker: kyc.DigiLocker{ClientID: "DEMO-CLIENT", ClientSecret: "demo-secret-" + hex.EncodeToString(h[:6])}}}
		log.Warn("practice DigiLocker with a demo customer", "api_key", DemoKey)
	default:
		return errors.New("give a configuration with -config (or LLKYC_CONFIG), or run with -simulate")
	}
	if u := os.Getenv("LLKYC_PUBLIC_URL"); u != "" {
		cfg.PublicURL = u
	}
	if cfg.PublicURL == "" {
		cfg.PublicURL = "http://localhost" + portOf(addr)
	}
	if !simulate && !strings.HasPrefix(cfg.PublicURL, "https://") {
		return errors.New("public_url must be https: DigiLocker sends investors back to it")
	}
	for _, c := range cfg.Customers {
		if c.ID == "" || len(c.APIKeySHA256) != 64 {
			return fmt.Errorf("customer %q needs an id and api_key_sha256 (make one with: ledgerline-kyc key)", c.ID)
		}
	}
	certFiles := cfg.UIDAICertificates
	if v := os.Getenv("LLKYC_UIDAI_CERTS"); v != "" {
		certFiles = strings.Split(v, ",")
	}
	certs, err := kyc.LoadCerts(certFiles)
	if err != nil {
		return fmt.Errorf("UIDAI certificate: %w", err)
	}
	for _, c := range certs {
		if time.Now().After(c.NotAfter) {
			log.Warn("UIDAI certificate has expired; download the current one from UIDAI", "subject", c.Subject.String(), "expired", c.NotAfter)
		}
	}
	if len(certs) == 0 && !simulate {
		log.Warn("no UIDAI offline e-KYC certificate (uidai_certificates or LLKYC_UIDAI_CERTS): investors can use DigiLocker only")
	}
	kr, err := keys(simulate)
	if err != nil {
		return err
	}
	st := &kyc.Store{Dir: data, V: vault.New(kr)}
	srv := &kyc.Server{Cfg: cfg, Store: st, Base: os.Getenv("LLKYC_DIGILOCKER_URL"), Simulate: simulate, Log: log, Certs: certs}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// Personal data is not kept longer than needed.
	go func() {
		t := time.NewTicker(10 * time.Minute)
		defer t.Stop()
		for {
			if n, err := st.Purge(time.Now(), srv.Retention()); err != nil {
				log.Error("purge", "err", err)
			} else if n > 0 {
				log.Info("purged finished sessions", "count", n)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	hs := &http.Server{Addr: addr, Handler: secure(srv.Handler()), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: time.Minute, WriteTimeout: 2 * time.Minute}
	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", addr, "public_url", cfg.PublicURL, "customers", len(cfg.Customers), "simulate", simulate)
		fmt.Fprintf(os.Stderr, "\nLedgerline KYC: %s\n\n", cfg.PublicURL)
		errc <- hs.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return hs.Shutdown(sctx)
	case err := <-errc:
		return err
	}
}

func keys(simulate bool) (*vault.Keyring, error) {
	spec, src := os.Getenv("LLKYC_DATA_KEYS"), "LLKYC_DATA_KEYS"
	if p := os.Getenv("LLKYC_DATA_KEYS_FILE"); p != "" {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		spec, src = string(b), "key file "+p
	}
	if strings.TrimSpace(spec) == "" {
		if simulate {
			return vault.Ephemeral(), nil
		}
		return nil, errors.New("no encryption keys: set LLKYC_DATA_KEYS or LLKYC_DATA_KEYS_FILE (make one with: ledgerctl keygen)")
	}
	return vault.ParseKeyring(spec, src)
}

func portOf(addr string) string {
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		return addr[i:]
	}
	return ":" + addr
}

func secure(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		}
		h.ServeHTTP(w, r)
	})
}
