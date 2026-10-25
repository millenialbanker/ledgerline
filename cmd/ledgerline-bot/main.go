// Command ledgerline-bot runs Ledgerline automations on this computer: it
// records tasks done in its own browser window and replays saved workflows.
//
//	ledgerline-bot pair --server https://bo.example.in --firm bhairav --code ABCD-EFGH
//	ledgerline-bot                 wait for work (recordings and runs) from Ledgerline
//	ledgerline-bot --headless      the same, with runs in a hidden browser
//	ledgerline-bot status          show the pairing
//	ledgerline-bot unpair          forget the pairing on this computer
//
// The bot keeps its own browser profile (its logins stay apart from yours)
// and its downloads in ~/.ledgerline-bot. Passwords are never saved here:
// Ledgerline gives each one only to the step of a run that needs it.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"ledgerline/internal/bot"
)

func main() {
	log := func(s string) { fmt.Printf("%s  %s\n", time.Now().Format("15:04:05"), s) }
	args := os.Args[1:]
	cmd := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var err error
	switch cmd {
	case "pair":
		err = pair(ctx, args, log)
	case "status":
		var c bot.Config
		if c, err = bot.LoadConfig(); err == nil {
			fmt.Printf("Paired with %s (firm %s) as %s, computer %q (%s).\n", c.Server, c.Tenant, c.User, c.Name, c.Agent)
		}
	case "unpair":
		err = os.Remove(bot.Home() + "/config.json")
		if err == nil {
			fmt.Println("This computer is no longer paired. Also remove it in Ledgerline (Automations → Computers).")
		}
	case "", "run":
		err = serve(ctx, args, log)
	default:
		err = fmt.Errorf("unknown command %q (pair, status, unpair, or nothing to wait for work)", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "ledgerline-bot:", err)
		os.Exit(1)
	}
}

func pair(ctx context.Context, args []string, log func(string)) error {
	fs := flag.NewFlagSet("pair", flag.ExitOnError)
	server := fs.String("server", "", "Ledgerline's address, e.g. https://bo.example.in")
	firm := fs.String("firm", "", "the firm's code in Ledgerline (shown with the pairing code)")
	code := fs.String("code", "", "the pairing code from Ledgerline (Automations → Computers → Add this computer)")
	host, _ := os.Hostname()
	name := fs.String("name", host, "a name for this computer")
	_ = fs.Parse(args)
	srv, err := bot.CheckServer(*server)
	if err != nil {
		return err
	}
	if *firm == "" || *code == "" {
		return fmt.Errorf("give --firm and --code (Ledgerline shows the whole command)")
	}
	c := &bot.Client{Server: srv, Tenant: *firm}
	agent, user, err := c.Pair(ctx, *code, *name, runtime.GOOS+"/"+runtime.GOARCH, bot.Version)
	if err != nil {
		return err
	}
	if err := bot.SaveConfig(bot.Config{Server: srv, Tenant: *firm, Token: c.Token, Agent: agent, User: user, Name: *name}); err != nil {
		return err
	}
	log(fmt.Sprintf("Paired as %s's computer %q. Now run: ledgerline-bot", user, *name))
	return nil
}

func serve(ctx context.Context, args []string, log func(string)) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	headless := fs.Bool("headless", false, "run workflows in a hidden browser (for scheduled runs); recording always shows the window")
	browser := fs.String("browser", "", "the browser program (default: Chrome or Edge)")
	_ = fs.Parse(args)
	cfg, err := bot.LoadConfig()
	if err != nil {
		return err
	}
	exe := *browser
	if exe == "" {
		if exe, err = bot.FindBrowser(); err != nil {
			return err
		}
	}
	a := &bot.Agent{Cfg: cfg, Client: &bot.Client{Server: cfg.Server, Tenant: cfg.Tenant, Token: cfg.Token}, Browser: exe, Headless: *headless, Log: log}
	return a.Serve(ctx)
}
