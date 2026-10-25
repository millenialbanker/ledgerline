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
	"ledgerline/internal/backup"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"ledgerline"
	"ledgerline/internal/api"
	"ledgerline/internal/app"
	"ledgerline/internal/charges"
	"ledgerline/internal/help"
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
	if len(os.Args) > 1 && os.Args[1] == "backup" {
		if err := backupCmd(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}
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
	vlt := vault.New(kr)
	stores = vault.Protect(stores, vlt)
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
	// Backups: after each day's close, into LEDGERLINE_BACKUP_DIR (a folder
	// on a separate disk, copied off the server). Encrypted with the data
	// keys, so keep a copy of the keys somewhere other than the backups.
	o.Sealer = vlt
	if dir := os.Getenv("LEDGERLINE_BACKUP_DIR"); dir != "" {
		o.Backups = &backup.Dir{Path: dir}
		log.Info("backups after each day's close", "dir", dir)
	} else if dev {
		o.Backups = &backup.Dir{Path: filepath.Join(os.TempDir(), "ledgerline-backups")}
		log.Warn("development backups go to a temporary folder", "dir", o.Backups.Path)
	} else {
		log.Warn("BACKUPS ARE OFF: set LEDGERLINE_BACKUP_DIR to a folder on a separate disk")
	}
	// Exchange holidays: NSE's lists are fetched from its API (BSE follows
	// them); MCX's come from its circular. LEDGERLINE_HOLIDAY_FETCH=off
	// stops fetching; LEDGERLINE_NSE_URL points at a gateway or mirror.
	if !strings.EqualFold(os.Getenv("LEDGERLINE_HOLIDAY_FETCH"), "off") {
		o.HolidaySources = []ops.HolidaySource{&ops.NSEHolidays{BaseURL: os.Getenv("LEDGERLINE_NSE_URL")}}
	}
	// Editions: how much runs by itself, per firm (manual, autopilot, full).
	// Set by the provider: LEDGERLINE_EDITION for every firm, and
	// LEDGERLINE_EDITIONS="firm:edition,..." for particular firms.
	if e := strings.ToLower(strings.TrimSpace(os.Getenv("LEDGERLINE_EDITION"))); e != "" {
		if _, err := ops.ParseEditions("x:" + e); err != nil {
			return fmt.Errorf("LEDGERLINE_EDITION: %w", err)
		}
		o.DefaultEdition = e
	}
	if spec := os.Getenv("LEDGERLINE_EDITIONS"); spec != "" {
		m, err := ops.ParseEditions(spec)
		if err != nil {
			return fmt.Errorf("LEDGERLINE_EDITIONS: %w", err)
		}
		o.EditionOf = m
	}
	// Where the server is hosted, and India-only mode, which refuses any
	// connection known to send data outside India.
	o.Residency = ops.Residency{IndiaOnly: os.Getenv("LEDGERLINE_INDIA_ONLY") == "1", Region: os.Getenv("LEDGERLINE_REGION"), Database: dbHost(dsn)}
	if o.Residency.IndiaOnly {
		if o.Residency.Region != "" && !ops.InIndia(o.Residency.Region) {
			return fmt.Errorf("India-only mode: LEDGERLINE_REGION %q is not a region in India", o.Residency.Region)
		}
		for _, k := range []string{"LEDGERLINE_LEDGY_KEY", "LEDGERLINE_ANTHROPIC_KEY", "LEDGERLINE_OPENAI_KEY", "LEDGERLINE_GEMINI_KEY"} {
			if os.Getenv(k) != "" {
				return fmt.Errorf("India-only mode: %s connects Ledgy to an AI provider outside India; unset it or LEDGERLINE_INDIA_ONLY", k)
			}
		}
		log.Info("India-only mode: no connection outside India is allowed", "region", o.Residency.Region)
	}
	// Aadhaar and PAN from DigiLocker through the Ledgerline KYC plugin,
	// under the firm's own DigiLocker registration.
	if u := os.Getenv("LEDGERLINE_KYC_URL"); u != "" {
		key := os.Getenv("LEDGERLINE_KYC_KEY")
		if len(key) < 24 {
			return errors.New("LEDGERLINE_KYC_URL is set: also set LEDGERLINE_KYC_KEY, the firm's API key for the KYC plugin")
		}
		if !dev && !strings.HasPrefix(u, "https://") {
			return errors.New("LEDGERLINE_KYC_URL must be https")
		}
		pk := &ops.PluginKYC{URL: u, Key: key}
		if dev {
			pk.Base = ops.SimulatedKYC()
		}
		o.KYC = pk
		log.Info("KYC plugin connected", "url", u)
	}
	// Ledgy can phrase its answers with an AI model when one is configured
	// and the firm switches it on in Settings. Only the masked question and
	// the help articles are sent. Every answer is metered and paid from the
	// firm's prepaid AI credit at a markup of at least 2x the provider's cost.
	if w, err := aiWriter(); err != nil {
		return err
	} else if w != nil {
		prices, err := ops.LoadAIPrices(os.Getenv("LEDGERLINE_AI_PRICES"))
		if err != nil {
			return err
		}
		rate := os.Getenv("LEDGERLINE_AI_USDINR")
		if rate == "" && dev {
			rate = "88"
			log.Warn("AI metering uses a sample rate of ₹88 per US dollar; set LEDGERLINE_AI_USDINR")
		}
		m, err := ops.NewMeter(prices, rate, os.Getenv("LEDGERLINE_AI_MARKUP"), os.Getenv("LEDGERLINE_AI_MIN_CHARGE"), "")
		if err != nil {
			return err
		}
		if _, _, _, err := m.Charge(w.Provider(), w.Model(), help.Usage{}); err != nil {
			return err
		}
		o.LedgyAI, o.Meter = w, m
		log.Info("Ledgy AI available to firms that switch it on", "provider", w.Provider(), "model", w.Model(), "markup", m.Markup.String(), "usd_inr", m.USDINR.String())
	}
	if _, err := ops.HelpKB(); err != nil {
		return fmt.Errorf("Ledgy help articles: %w", err)
	}
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
	apiSrv.ProviderKey = os.Getenv("LEDGERLINE_PROVIDER_KEY")
	apiSrv.PublicURL = os.Getenv("LEDGERLINE_PUBLIC_URL")
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
	root.Handle("/hooks/", apiSrv)    // payment gateway and WhatsApp webhooks
	root.Handle("/optin/", apiSrv)    // WhatsApp consent links sent to clients
	root.Handle("/onboard/", apiSrv)  // online account opening (public API)
	root.Handle("/provider/", apiSrv) // the software provider's own endpoints
	root.Handle("/agent/", apiSrv)    // the Ledgerline bot on paired computers
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

// dbHost returns the host part of a database URL, without credentials.
func dbHost(dsn string) string {
	if dsn == "" {
		return ""
	}
	u, err := url.Parse(dsn)
	if err != nil || u.Host == "" {
		return "configured"
	}
	return u.Hostname()
}

// aiWriter picks the AI provider from the environment: LEDGERLINE_AI_PROVIDER
// (anthropic, openai or gemini) with that provider's key, and optionally
// LEDGERLINE_AI_MODEL. LEDGERLINE_LEDGY_KEY alone means Anthropic.
func aiWriter() (help.Writer, error) {
	model, baseURL := os.Getenv("LEDGERLINE_AI_MODEL"), os.Getenv("LEDGERLINE_AI_BASE_URL") // base URL: a gateway or proxy in front of the provider
	if model == "" {
		model = os.Getenv("LEDGERLINE_LEDGY_MODEL")
	}
	provider := strings.ToLower(os.Getenv("LEDGERLINE_AI_PROVIDER"))
	anth := os.Getenv("LEDGERLINE_ANTHROPIC_KEY")
	if anth == "" {
		anth = os.Getenv("LEDGERLINE_LEDGY_KEY")
	}
	if provider == "" {
		switch {
		case anth != "":
			provider = "anthropic"
		case os.Getenv("LEDGERLINE_OPENAI_KEY") != "":
			provider = "openai"
		case os.Getenv("LEDGERLINE_GEMINI_KEY") != "":
			provider = "gemini"
		default:
			return nil, nil
		}
	}
	need := func(k, v string) error {
		if v == "" {
			return fmt.Errorf("LEDGERLINE_AI_PROVIDER=%s needs %s", provider, k)
		}
		return nil
	}
	switch provider {
	case "anthropic":
		return &help.Claude{Key: anth, ModelID: model, BaseURL: baseURL}, need("LEDGERLINE_ANTHROPIC_KEY", anth)
	case "openai":
		k := os.Getenv("LEDGERLINE_OPENAI_KEY")
		return &help.OpenAI{Key: k, ModelID: model, BaseURL: baseURL}, need("LEDGERLINE_OPENAI_KEY", k)
	case "gemini":
		k := os.Getenv("LEDGERLINE_GEMINI_KEY")
		return &help.Gemini{Key: k, ModelID: model, BaseURL: baseURL}, need("LEDGERLINE_GEMINI_KEY", k)
	}
	return nil, fmt.Errorf("LEDGERLINE_AI_PROVIDER must be anthropic, openai or gemini (got %q)", provider)
}
