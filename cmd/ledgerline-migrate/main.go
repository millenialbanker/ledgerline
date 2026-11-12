// Command ledgerline-migrate is the Ledgerline migration tool: a desktop
// program a stockbroker's office runs by itself to move from its old back
// office (SharePro first) to Ledgerline. It opens a page in the browser,
// takes the latest backup and the archives of up to 8 earlier years, and
// writes one encrypted migration package to upload into Ledgerline.
//
//	ledgerline-migrate [files or folders...]        the page (files dropped on the icon are pre-loaded)
//	ledgerline-migrate --headless --mapper SharePro --server 'localhost\SQLEXPRESS' --plan plan.json files...
//
// Flags: --port (default: a random port), --no-browser, --work (folder for
// work files; default the system's temporary folder, on a Mac
// ~/Library/Caches/Ledgerline Migration), --out (default folder for the
// package).
//
// Headless mode reads secrets from the environment, never from flags or
// files: LLMIG_PASSPHRASE (the package's passphrase), LLMIG_SQL_USER and
// LLMIG_SQL_PASSWORD (a SQL login; Windows sign-in without them),
// LLMIG_7Z_PASSWORD (for protected .7z files). --server docker starts SQL
// Server Express in Docker. plan.json:
//
//	{"firms": [], "years": 8, "documents": true, "dormant": "inactive",
//	 "out": "/path/to/folder", "max_part_gb": 4, "keep": false,
//	 "live": ["NC2627", "shadmin"], "developer": false}
//
// Nothing is sent over the network except to the SQL Server on this
// computer (and, with Docker, Microsoft's image download). No telemetry,
// no update checks.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"ledgerline/internal/migrate"
	"ledgerline/internal/migrate/app"
	_ "ledgerline/internal/migrate/sharepro"
	"ledgerline/internal/migrate/sqlserver"
)

func main() {
	port := flag.Int("port", 0, "port for the local page (0: random)")
	noBrowser := flag.Bool("no-browser", false, "do not open the browser; print the address only")
	work := flag.String("work", "", "folder for the work files (deleted at the end)")
	out := flag.String("out", "", "default folder to save the package in")
	headless := flag.Bool("headless", false, "run without the page (testing)")
	mapperName := flag.String("mapper", "SharePro", "the old back office")
	server := flag.String("server", "", `headless: SQL Server on this computer, e.g. localhost\SQLEXPRESS, 127.0.0.1,1433, or docker`)
	planFile := flag.String("plan", "", "headless: the choices, as JSON")
	flag.Parse()

	logw, closeLog := logTarget()
	defer closeLog()

	m := migrate.MapperFor(*mapperName)
	if m == nil {
		var names []string
		for _, x := range migrate.Mappers() {
			names = append(names, x.Name())
		}
		fmt.Fprintf(os.Stderr, "unknown --mapper %q (known: %s)\n", *mapperName, strings.Join(names, ", "))
		os.Exit(2)
	}
	s, err := app.NewSession(m, *work)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	s.Log = logw
	if *out != "" {
		s.Out = *out
	}
	defer s.Close()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)

	if *headless {
		done := make(chan error, 1)
		go func() { done <- runHeadless(s, *server, *planFile, flag.Args(), logw) }()
		select {
		case err = <-done:
		case <-sig:
			fmt.Fprintln(logw, "stopping: removing temporary databases and work files")
			s.Cancel()
			err = errors.New("stopped")
		}
		s.Close()
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}

	u := &app.UI{S: s}
	if err := u.Listen(*port); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	s.AddLocal(flag.Args())
	go s.FindServer(context.Background())
	go u.Serve()
	fmt.Fprintf(logw, "Ledgerline migration tool %s\nOpen this address in your browser (it is for this computer only):\n\n  %s\n\nKeep this window open until you press Finish on the page.\n", app.Version, u.URL())
	if !*noBrowser {
		if err := app.OpenBrowser(u.URL()); err != nil {
			fmt.Fprintln(logw, "could not open the browser; copy the address above into it")
		}
	}
	// stop when the page has been closed for a long time with nothing running
	idle := time.NewTicker(30 * time.Second)
	defer idle.Stop()
	for {
		select {
		case <-u.Quit:
			fmt.Fprintln(logw, "finishing: removing temporary databases and work files")
			s.Close()
			u.Shutdown()
			return
		case <-sig:
			fmt.Fprintln(logw, "stopping: removing temporary databases and work files")
			s.Close()
			u.Shutdown()
			return
		case <-idle.C:
			if u.Idle() > 20*time.Minute && !s.Busy() {
				fmt.Fprintln(logw, "the page has been closed for 20 minutes: finishing")
				s.Close()
				u.Shutdown()
				return
			}
		}
	}
}

// logTarget is the console, or inside the macOS app (which has no console)
// a log file in ~/Library/Logs/Ledgerline Migration/.
func logTarget() (io.Writer, func()) {
	exe, _ := os.Executable()
	if runtime.GOOS != "darwin" || !strings.Contains(exe, ".app/Contents/MacOS/") {
		return os.Stdout, func() {}
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return os.Stdout, func() {}
	}
	dir := filepath.Join(h, "Library", "Logs", "Ledgerline Migration")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return os.Stdout, func() {}
	}
	f, err := os.OpenFile(filepath.Join(dir, "migrate-"+time.Now().Format("2006-01-02")+".log"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return os.Stdout, func() {}
	}
	return f, func() { f.Close() }
}

type plan struct {
	app.Choice
	Live      []string `json:"live"`
	Developer bool     `json:"developer"`
}

func runHeadless(s *app.Session, server, planFile string, files []string, logw io.Writer) error {
	ctx := context.Background()
	if planFile == "" {
		return errors.New("--plan is needed with --headless")
	}
	b, err := os.ReadFile(planFile)
	if err != nil {
		return err
	}
	var p plan
	p.Years, p.Documents, p.Dormant = 8, true, "inactive"
	if err := json.Unmarshal(b, &p); err != nil {
		return fmt.Errorf("%s: %w", planFile, err)
	}
	p.Passphrase = os.Getenv("LLMIG_PASSPHRASE")
	p.Passphrase2 = p.Passphrase
	if p.Out == "" {
		p.Out = s.Out
	}
	// SQL Server
	switch strings.ToLower(server) {
	case "":
		s.FindServer(ctx)
	case "docker":
		if err := s.CheckDocker(ctx); err != nil {
			return err
		}
		if err := s.StartDocker(ctx, p.Developer); err != nil {
			return err
		}
	default:
		e, err := app.ParseServer(server)
		if err != nil {
			return err
		}
		if err := s.Connect(ctx, e, sqlserver.Login{User: os.Getenv("LLMIG_SQL_USER"), Password: os.Getenv("LLMIG_SQL_PASSWORD")}); err != nil {
			return err
		}
	}
	st := s.State()
	if st.Server.Status != "ready" {
		return errors.New("SQL Server: " + st.Server.Message)
	}
	fmt.Fprintln(logw, st.Server.Message)
	// backups
	if len(files) > 0 {
		s.AddLocal(files)
		if err := scan(s); err != nil {
			return err
		}
		if pw := os.Getenv("LLMIG_7Z_PASSWORD"); pw != "" {
			again := false
			for _, f := range s.State().Passwords {
				s.SetPassword(f.ID, pw)
				again = true
			}
			if again {
				if err := scan(s); err != nil {
					return err
				}
			}
		}
		for _, f := range s.State().Passwords {
			return errors.New(f.Name + " needs its password (set LLMIG_7Z_PASSWORD)")
		}
	}
	if len(p.Live) > 0 {
		if _, err := s.ListLive(ctx); err != nil {
			return err
		}
		if err := s.UseLive(p.Live); err != nil {
			return err
		}
		if j := s.Wait(); j.Error != "" {
			return errors.New(j.Error)
		}
	}
	st = s.State()
	for _, f := range st.Files {
		if f.Status == "skipped" {
			fmt.Fprintf(logw, "not used: %s: %s\n", f.Name, f.Reason)
		}
	}
	for _, r := range st.Found {
		use := "use "
		if !r.Use {
			use = "skip"
		}
		fmt.Fprintf(logw, "%s  %-28s %-12s %-8s %s %s\n", use, r.File, r.Database, r.FY, r.Note, r.Why)
	}
	for _, w := range st.Warnings {
		fmt.Fprintln(logw, "warning:", w)
	}
	if err := s.Start(p.Choice); err != nil {
		return err
	}
	if j := s.Wait(); j.Error != "" {
		return errors.New(j.Error)
	}
	res := s.Result()
	if res == nil {
		return errors.New("no package was made")
	}
	for _, part := range res.Parts {
		fmt.Fprintf(logw, "saved %s (%d bytes)\n", part.Path, part.Bytes)
	}
	fmt.Fprintf(logw, "report %s\n", res.Report)
	return nil
}

func scan(s *app.Session) error {
	if err := s.Scan(); err != nil {
		return err
	}
	if j := s.Wait(); j.Error != "" {
		return errors.New(j.Error)
	}
	return nil
}
