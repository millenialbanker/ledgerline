// Command ledgerctl is the operator's command-line tool.
//
//	ledgerctl demo                                    run the sample day end to end
//	ledgerctl check -source NSE-CM-TRADE file.csv     parse a file without storing it
//	ledgerctl quote -bucket CM-DEL -exchange NSE -side B -qty 10 -price 1412
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"ledgerline"
	"ledgerline/internal/app"
	"ledgerline/internal/charges"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
	"ledgerline/internal/vault"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "demo":
		err = demo(os.Args[2:])
	case "check":
		err = check(os.Args[2:])
	case "quote":
		err = quote(os.Args[2:])
	case "keygen":
		err = keygen(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: ledgerctl demo | keygen [-version N] | check -source NAME FILE | quote -bucket B -exchange X -side B|S -qty N -price P [-date YYYY-MM-DD]")
	os.Exit(2)
}

// source returns where config and sample data come from: the files built
// into this binary by default, or a repository folder given with -root.
func source(root string) fs.FS {
	if root == "" {
		return ledgerline.FS
	}
	return os.DirFS(root)
}

func loadConfig(root string) (*ingest.Registry, charges.Schedule, error) {
	src := source(root)
	reg, err := ingest.LoadFS(src, "config/adapters")
	if err != nil {
		return nil, charges.Schedule{}, err
	}
	f, err := src.Open("config/charges/sample-schedule.json")
	if err != nil {
		return nil, charges.Schedule{}, fmt.Errorf("charge schedule: %w", err)
	}
	defer f.Close()
	s, err := charges.LoadSchedule(f)
	return reg, s, err
}

func demo(args []string) error {
	fl := flag.NewFlagSet("demo", flag.ExitOnError)
	root := fl.String("root", "", "repository folder to read config and sample data from (default: built into this binary)")
	fl.Parse(args)
	reg, sched, err := loadConfig(*root)
	if err != nil {
		return err
	}
	ctx := context.Background()
	a := app.New(app.MemoryStores(), reg, sched)
	const tenant, date, actor = "demo", "2026-10-16", "ledgerctl"
	w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)

	fmt.Println("== 1. Ingest")
	for _, f := range [][2]string{{"NSE-CM-TRADE", "nse_cm_trades.csv"}, {"NSE-FO-TRADE", "nse_fo_trades.csv"}, {"MCX-TRADE", "mcx_trades.txt"}, {"OMS-TRADE", "oms_trades.csv"}} {
		b, err := fs.ReadFile(source(*root), "testdata/sample/"+f[1])
		if err != nil {
			return err
		}
		r, err := a.IngestFile(ctx, tenant, actor, f[0], "", f[1], b)
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "  %s\t%s\t%d trades\tsha256 %s…\n", f[1], r.Adapter, r.Added, r.File.SHA256[:12])
	}
	w.Flush()

	fmt.Println("\n== 2. Reconcile exchange vs OMS")
	sum, err := a.RunRecon(ctx, tenant, actor, date)
	if err != nil {
		return err
	}
	fmt.Printf("  %d exchange trades, %d OMS fills, %d matched, %d breaks\n", sum.Result.ExchangeCount, sum.Result.OMSCount, sum.Result.Matched, len(sum.Result.Breaks))
	for _, b := range sum.Result.Breaks {
		var d []string
		for _, x := range b.Diffs {
			d = append(d, fmt.Sprintf("%s %s≠%s", x.Field, x.Exchange, x.OMS))
		}
		fmt.Fprintf(w, "  %s\t%s\t%s-%s %s\t%s\t%s\n", b.ID, b.Type, b.Exchange, b.Segment, b.TradeNo, b.UCC, strings.Join(d, "; "))
	}
	w.Flush()

	fmt.Println("\n== 3. Bill")
	pb, err := fs.ReadFile(source(*root), "testdata/sample/settlement_prices.json")
	if err != nil {
		return err
	}
	var prices map[string]money.Dec
	if err := json.Unmarshal(pb, &prices); err != nil {
		return err
	}
	br, err := a.RunBilling(ctx, tenant, actor, date, prices)
	if err != nil {
		return err
	}
	fmt.Fprintln(w, "  UCC\tSegment\tTrades\tTrade value / MTM\tCharges\tNet to client")
	for _, g := range br.Groups {
		fmt.Fprintf(w, "  %s\t%s-%s\t%d\t%s\t%s\t%s\n", g.UCC, g.Exchange, g.Segment, g.Trades, g.TradeNet, g.Charges.Total, g.Net)
	}
	w.Flush()
	for _, x := range br.Warnings {
		fmt.Println("  warning:", x)
	}

	fmt.Println("\n== 4. Trial balance")
	tb, err := a.Ledger.TrialBalance(ctx, tenant, date)
	if err != nil {
		return err
	}
	for _, r := range tb.Rows {
		fmt.Fprintf(w, "  %s\t%s\t%s\n", r.Account, blank(r.Debit), blank(r.Credit))
	}
	fmt.Fprintf(w, "  TOTAL\t%s\t%s\n", tb.TotalDebit, tb.TotalCredit)
	w.Flush()
	fmt.Printf("  balanced: %v\n", tb.Balanced)

	fmt.Println("\n== 5. Statement · CLIENT:BH1001")
	st, _ := a.Ledger.Statement(ctx, tenant, ledger.ClientAccount("BH1001"), date, date)
	for _, l := range st.Lines {
		fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\n", l.EntryID, l.Memo, blank(l.Debit), blank(l.Credit), drcr(l.Balance))
	}
	w.Flush()
	return nil
}

func blank(a money.Amount) string {
	if a == 0 {
		return ""
	}
	return a.String()
}

func drcr(a money.Amount) string {
	if a < 0 {
		return (-a).String() + " Cr"
	}
	return a.String() + " Dr"
}

func check(args []string) error {
	fl := flag.NewFlagSet("check", flag.ExitOnError)
	root := fl.String("root", "", "repository folder to read config and sample data from (default: built into this binary)")
	source := fl.String("source", "", "layout name, e.g. NSE-CM-TRADE")
	version := fl.String("version", "", "layout version (default latest)")
	fl.Parse(args)
	if *source == "" || fl.NArg() != 1 {
		return fmt.Errorf("usage: ledgerctl check -source NAME FILE")
	}
	reg, _, err := loadConfig(*root)
	if err != nil {
		return err
	}
	spec, err := reg.Get(*source, *version)
	if err != nil {
		return err
	}
	b, err := os.ReadFile(fl.Arg(0))
	if err != nil {
		return err
	}
	evs, errs, perr := spec.Parse(bytes.NewReader(b))
	fmt.Printf("%s@%s: %d rows parsed, %d row errors\n", spec.Name, spec.Version, len(evs), len(errs))
	for _, e := range errs {
		fmt.Println("  ", e.Error())
	}
	return perr
}

func quote(args []string) error {
	fl := flag.NewFlagSet("quote", flag.ExitOnError)
	root := fl.String("root", "", "repository folder to read config and sample data from (default: built into this binary)")
	bucket := fl.String("bucket", "CM-DEL", "CM-DEL, CM-INTRA, FO-FUT, FO-OPT, CD-FUT, CD-OPT, COM-FUT, COM-OPT")
	exch := fl.String("exchange", "NSE", "exchange")
	side := fl.String("side", "B", "B or S")
	qty := fl.Int64("qty", 1, "quantity")
	price := fl.String("price", "0", "price")
	date := fl.String("date", time.Now().In(time.FixedZone("IST", 19800)).Format("2006-01-02"), "trade date")
	fl.Parse(args)
	_, sched, err := loadConfig(*root)
	if err != nil {
		return err
	}
	p, err := money.ParseDec(*price)
	if err != nil {
		return err
	}
	b, err := sched.Compute(charges.Leg{Bucket: charges.Bucket(*bucket), Exchange: *exch, Side: *side, Qty: *qty, Price: p, Date: *date})
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', tabwriter.AlignRight)
	for _, r := range [][2]string{{"Turnover", b.Turnover.Round(money.UnitPaisa).String()}, {"Brokerage", b.Brokerage.String()}, {"STT / CTT", b.STT.String()}, {"Exchange txn", b.ExchTxn.String()}, {"SEBI fee", b.SEBIFee.String()}, {"Stamp duty", b.Stamp.String()}, {"GST", b.GST.String()}, {"Total charges", b.Total.String()}} {
		fmt.Fprintf(w, "%s\t%s\t\n", r[0], r[1])
	}
	w.Flush()
	if sched.Sample {
		fmt.Println("(sample rate schedule " + sched.Version + ")")
	}
	return nil
}

// keygen prints a new master key for LEDGERLINE_DATA_KEYS. To rotate, put
// the new key first and keep the old ones after it: "2:<new>,1:<old>".
func keygen(args []string) error {
	fs := flag.NewFlagSet("keygen", flag.ContinueOnError)
	v := fs.Int("version", 1, "key version (one more than the current key when rotating)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *v <= 0 {
		return fmt.Errorf("version must be positive")
	}
	fmt.Println(vault.NewKey(*v))
	fmt.Fprintln(os.Stderr, "Store this key in your secrets manager and set it as LEDGERLINE_DATA_KEYS (or in a file named by LEDGERLINE_DATA_KEYS_FILE, chmod 600).")
	fmt.Fprintln(os.Stderr, "Losing every copy of it makes the firm's stored data unreadable. Rotate by prepending a new version and keeping the old one.")
	return nil
}
