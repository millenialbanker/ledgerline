// Command ledgerline runs the back office API server.
//
//	ledgerline -dev                     in-memory stores, demo key printed at start
//	LEDGERLINE_API_KEYS=key:tenant \
//	LEDGERLINE_DB=postgres://... \
//	ledgerline                          PostgreSQL (build with -tags pgx)
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ledgerline"
	"ledgerline/internal/api"
	"ledgerline/internal/app"
	"ledgerline/internal/charges"
	"ledgerline/internal/ingest"
	"ledgerline/internal/pgstore"
)

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
	if dev && len(keys) == 0 {
		keys = map[string]string{"dev-key-not-for-production": "demo"}
		log.Warn("development API key enabled", "key", "dev-key-not-for-production", "tenant", "demo")
	}
	if len(keys) == 0 {
		return errors.New("no API keys configured; set LEDGERLINE_API_KEYS=key:tenant[,key:tenant]")
	}

	a := app.New(stores, reg, sched)
	srv := &http.Server{
		Addr: addr, Handler: api.New(a, keys, log),
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 5 * time.Minute, WriteTimeout: 5 * time.Minute, IdleTimeout: 2 * time.Minute,
	}
	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", addr, "adapters", len(reg.List()), "schedule", sched.Version)
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
