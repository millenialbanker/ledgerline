// Command ledgerline runs the back office API server.
//
//	ledgerline -dev                     in-memory stores, demo key printed at start
//	LEDGERLINE_API_KEYS=key:tenant \
//	LEDGERLINE_DB=postgres://... \
//	ledgerline                          PostgreSQL (build with -tags pgx)
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"ledgerline"
	"ledgerline/internal/api"
	"ledgerline/internal/app"
	"ledgerline/internal/charges"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ops"
	"ledgerline/internal/pgstore"
	"ledgerline/internal/vault"
	"ledgerline/internal/web"
)

// devKey is the API key used by -dev when no keys are configured.
const devKey = "dev-key-not-for-production"

// sqlDriver is set by driver_pgx.go when built with -tags pgx.
var sqlDriver string

func main() {
	addr := flag.String("addr", envOr("LEDGERLINE_ADDR", ":8080"), "listen address")
	adapters := flag.String("adapters", os.Getenv("LEDGERLINE_ADAPTERS"), "directory of file layout specs (default: built into this binary)")
	schedule := flag.String("charges", os.Getenv("LEDGERLINE_CHARGES"), "charge schedule JSON (default: built-in sample schedule)")
	dev := flag.Bool("dev", false, "development mode: in-memory stores and a demo API key")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(log, *addr, *adapters, *schedule, *dev); err != nil {
		log.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger, addr, adapterDir, schedulePath string, dev bool) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var reg *ingest.Registry
	var err error
	if adapterDir == "" {
		reg, err = ingest.LoadFS(ledgerline.FS, "config/adapters")
	} else {
		reg, err = ingest.LoadDir(adapterDir)
	}
	if err != nil {
		return err
	}
	var f fs.File
	if schedulePath == "" {
		f, err = ledgerline.FS.Open("config/charges/sample-schedule.json")
	} else {
		f, err = os.Open(schedulePath)
	}
	if err != nil {
		return fmt.Errorf("charge schedule: %w", err)
	}
	sched, err := charges.LoadSchedule(f)
	f.Close()
	if err != nil {
		return err
	}
	if sched.Sample {
		log.Warn("charge schedule is marked SAMPLE; do not bill real clients with it", "version", sched.Version)
	}

	keys, err := api.ParseKeys(os.Getenv("LEDGERLINE_API_KEYS"))
	if err != nil {
		return err
	}
	var stores app.Stores
	dsn := os.Getenv("LEDGERLINE_DB")
	switch {
	case dsn != "":
		if sqlDriver == "" {
			return errors.New("LEDGERLINE_DB is set but this binary has no PostgreSQL driver; build with: go get github.com/jackc/pgx/v5 && go build -tags pgx ./cmd/ledgerline")
		}
		st, err := pgstore.Open(ctx, sqlDriver, dsn)
		if err != nil {
			return err
		}
		defer st.DB.Close()
		stores = st.Stores()
		log.Info("using PostgreSQL")
	case dev:
		stores = app.MemoryStores()
		log.Warn("development mode: in-memory stores, data is lost on exit")
	default:
		return errors.New("set LEDGERLINE_DB for PostgreSQL, or pass -dev for in-memory stores")
	}
	// Client data is encrypted before it reaches any store. A database
	// without data keys is refused, so a misconfigured server can never
	// write client data in clear.
	kr, err := dataKeys(dsn != "", dev)
	if err != nil {
		return err
	}
	stores = vault.Protect(stores, vault.New(kr))
	log.Info("client data encrypted", "keys", kr.Describe(), "key_version", kr.Current())
	if dev && len(keys) == 0 {
		keys = map[string]string{devKey: "demo"}
		log.Warn("development API key enabled", "key", devKey, "tenant", "demo")
	}
	if len(keys) == 0 {
		return errors.New("no API keys configured; set LEDGERLINE_API_KEYS=key:tenant[,key:tenant]")
	}

	a := app.New(stores, reg, sched)
	o := ops.New(a)
	if dev {
		o.SimulateOMS = true
		o.DevCodes = true
		for _, t := range keys {
			if err := o.SeedDev(ctx, t); err != nil {
				return fmt.Errorf("seeding development tenant %s: %w", t, err)
			}
		}
	}
	// LEDGERLINE_BOOTSTRAP_ADMIN=tenant:user-id:password creates the first
	// administrator of a firm that has no one able to sign in. The password
	// must be changed at first sign-in. Remove the variable afterwards.
	if b := os.Getenv("LEDGERLINE_BOOTSTRAP_ADMIN"); b != "" {
		p := strings.SplitN(b, ":", 3)
		if len(p) != 3 {
			return errors.New("LEDGERLINE_BOOTSTRAP_ADMIN must be tenant:user-id:password")
		}
		created, err := o.BootstrapAdmin(ctx, p[0], p[1], "Administrator", p[2])
		if err != nil {
			return fmt.Errorf("bootstrap admin: %w", err)
		}
		if created {
			log.Warn("created the first administrator; sign in, change the password, then unset LEDGERLINE_BOOTSTRAP_ADMIN", "tenant", p[0], "user", p[1])
		}
	}
	apiSrv := api.New(a, o, keys, log, dev)
	cfg := web.Config{Dev: dev, Version: "2.1"}
	if ts := apiSrv.Tenants(); len(ts) == 1 {
		cfg.Tenant = ts[0]
	}
	if dev {
		cfg.DevPassword = ops.DevPassword
		for _, u := range ops.DevUsers {
			cfg.DevUsers = append(cfg.DevUsers, web.DevUser{ID: u.ID, Name: u.Name, Role: u.Role})
		}
	}
	root := http.NewServeMux()
	root.Handle("/v1/", apiSrv)
	root.Handle("/healthz", apiSrv)
	root.Handle("/hooks/", apiSrv)   // payment gateway and WhatsApp webhooks
	root.Handle("/optin/", apiSrv)   // WhatsApp consent links sent to clients
	root.Handle("/onboard/", apiSrv) // online account opening (public API)
	root.Handle("/", web.Handler(cfg))
	srv := &http.Server{
		Addr: addr, Handler: secure(root),
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 5 * time.Minute, WriteTimeout: 5 * time.Minute, IdleTimeout: 2 * time.Minute,
	}
	// The scheduler checks each firm's jobs once a minute.
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-t.C:
				for _, tn := range apiSrv.Tenants() {
					if n, err := o.Tick(ctx, tn, now); err != nil {
						log.Error("scheduler", "tenant", tn, "err", err)
					} else if n > 0 {
						log.Info("scheduler ran jobs", "tenant", tn, "jobs", n)
					}
				}
			}
		}
	}()
	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", addr, "adapters", len(reg.List()), "schedule", sched.Version)
		scheme := "http"
		if os.Getenv("LEDGERLINE_TLS_CERT") != "" {
			scheme = "https"
		}
		fmt.Fprintf(os.Stderr, "\nLedgerline console: %s://localhost%s\nPress Ctrl+C to stop.\n\n", scheme, portOf(addr))
		cert, key := os.Getenv("LEDGERLINE_TLS_CERT"), os.Getenv("LEDGERLINE_TLS_KEY")
		if cert != "" || key != "" {
			// TLS 1.2 is the floor (SEBI CSCRF); Go's defaults pick
			// forward-secret AEAD suites only and prefer TLS 1.3.
			srv.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
			errc <- srv.ListenAndServeTLS(cert, key)
			return
		}
		errc <- srv.ListenAndServe()
	}()
	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		log.Info("shutting down")
		sctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := srv.Shutdown(sctx); err != nil {
			return fmt.Errorf("shutdown: %w", err)
		}
	}
	return nil
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func portOf(addr string) string {
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		return addr[i:]
	}
	return ":" + addr
}

// dataKeys loads the master keys that protect client data:
// LEDGERLINE_DATA_KEYS="2:<base64>,1:<base64>" (newest first) or a file
// named by LEDGERLINE_DATA_KEYS_FILE holding the same. Create a key with
// "ledgerctl keygen". An in-memory development server without keys uses a
// temporary key; a server with a database must be given keys.
func dataKeys(db, dev bool) (*vault.Keyring, error) {
	spec, src := os.Getenv("LEDGERLINE_DATA_KEYS"), "LEDGERLINE_DATA_KEYS environment variable"
	if p := os.Getenv("LEDGERLINE_DATA_KEYS_FILE"); p != "" {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("data keys file: %w", err)
		}
		if st, err := os.Stat(p); err == nil && st.Mode().Perm()&0o077 != 0 {
			return nil, fmt.Errorf("data keys file %s is readable by other users; run: chmod 600 %s", p, p)
		}
		spec, src = string(b), "key file "+p
	}
	if strings.TrimSpace(spec) == "" {
		if db || !dev {
			return nil, errors.New("no data keys: client data must be encrypted. Create one with \"ledgerctl keygen\" and set LEDGERLINE_DATA_KEYS or LEDGERLINE_DATA_KEYS_FILE")
		}
		return vault.Ephemeral(), nil
	}
	return vault.ParseKeyring(spec, src)
}

// secure adds the browser security headers every response needs. HSTS is
// sent only over HTTPS (directly or behind a TLS load balancer).
func secure(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
			hd.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("Referrer-Policy", "no-referrer")
		if strings.HasPrefix(r.URL.Path, "/v1/") || strings.HasPrefix(r.URL.Path, "/onboard/") {
			hd.Set("Cache-Control", "no-store")
		}
		h.ServeHTTP(w, r)
	})
}
