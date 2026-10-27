package ops

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
)

// ---- periodic work ----
//
// Work that is not daily: weekly statements, month-end postings and
// statements, the running account settlement, the quarterly BSDA review,
// monthly disclosures and the periodic exchange files. Each task knows its
// period (the week, month or quarter it covers), when it is due and whether
// it is done, so the calendar shows what is due or overdue and the
// scheduler runs the ones that can run by themselves. Tasks that need a
// person's decision put a work item in the queue instead.

const cPeriodicRun = "periodicrun"

// PeriodicTask describes one kind of periodic work.
type PeriodicTask struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Area  string `json:"area"`
	Freq  string `json:"freq"` // Weekly, Monthly, Quarterly, As needed
	About string `json:"about"`
	Due   string `json:"due"`             // when it is due, for people
	Basis string `json:"basis,omitempty"` // the rule behind it
	Auto  bool   `json:"auto"`            // the scheduler may run it unattended
	View  string `json:"view,omitempty"`  // the screen where it is worked
	File  string `json:"file,omitempty"`  // exchange-file layout it loads or writes
}

// PeriodicTasks is every periodic task, in calendar order.
var PeriodicTasks = []PeriodicTask{
	{"funds_securities", "Weekly statement of funds and securities", "Clients", "Weekly", "Emails every client with a balance, a movement or securities with the broker a signed statement of the week's funds and securities.", "Saturday, for the week to Friday", "Exchange and SEBI rules on statements of accounts to clients", true, "dispatch", ""},
	{"cuspa_review", "Unpaid securities (CUSPA): release or sell", "Settlement", "Weekly", "Releases unpaid purchases held in the client unpaid securities pledgee account to clients who have since paid, and queues for sale those still unpaid after the allowed days.", "Monday", "SEBI rules on unpaid securities (CUSPA)", true, "settle", ""},
	{"es_return", "Client funds monitoring return (enhanced supervision)", "Compliance", "Weekly", "Builds the weekly statement of client money held and where (client bank balances, collateral with clearing corporations, client credit and debit balances, own collateral, bank guarantees, margin used by credit-balance clients) with the client-wise workbook, for upload to the exchanges.", "Tuesday, for the week to Friday", "SEBI and exchange circulars on enhanced supervision of stock brokers", true, "periodic", ""},
	{"dis_master", "DIS master upload to CDSL", "Depository", "Weekly", "Builds CDSL's transaction upload (DIS issue) for the delivery instruction slip books issued in the week, for upload on CDSL's system.", "Monday, for the week to Friday", "CDSL operating instructions on DIS issuance", true, "dp", "cdsl-txn-upload"},
	{"cuspa_mtf_payout", "CUSPA and MTF pay-out reporting", "Settlement", "Settlement day", "Reports to NSE Clearing the settlement's purchases that are not paid out to clients' demat accounts: unpaid purchases kept in CUSPA and MTF purchases kept for the pledge.", "Settlement day, before pay-out", "SEBI and NSE Clearing rules on pay-out of securities to clients", true, "settle", "ncl-cuspa-mtf-payout"},
	{"settlement_master", "Settlement calendar for the coming month", "Settlement", "Monthly", "Checks that NSE Clearing's settlement master for the coming month is loaded: settlement numbers and pay-in and pay-out dates used by the pay-out reporting and the settlement screen.", "Before the month starts", "", true, "xfiles", "ncl-settlement-master"},
	{"ccil_prices", "CCIL prices of G-secs and T-bills", "Risk", "Monthly", "Checks that CCIL's month-end valuation prices are loaded, so G-secs and T-bills held or given as collateral are valued at month end.", "By the 5th", "", true, "xfiles", "ccil-gsec-price"},
	{"month_end", "Month-end postings", "Accounts", "Monthly", "Interest on debit balances, MTF interest, DP bills with GST and the authorised persons' brokerage share (commission) for the month just ended.", "By the 5th", "", true, "autopilot", ""},
	{"ledger_statements", "Monthly statement of account", "Clients", "Monthly", "Emails every active client a signed statement of account for the month just ended.", "By the 7th", "Exchange rules on statements of accounts to clients", true, "dispatch", ""},
	{"dp_statements", "DP transaction and holding statements", "Depository", "Monthly", "Emails every demat account its signed statement of transactions and holdings for the month just ended.", "By the 10th", "Depository rules on statements to beneficial owners", true, "dp", ""},
	{"complaints_disclosure", "Investor complaints disclosure", "Compliance", "Monthly", "Builds the month's complaints data (received, resolved, pending, average days) for the website disclosure.", "By the 7th", "SEBI circular on disclosure of investor complaints", true, "helpdesk", ""},
	{"ras", "Running account settlement", "Money", "Monthly / quarterly", "Settles running accounts on the settlement date (monthly or quarterly as each client chose), pays out the funds and sends each client the settlement statement.", "On the settlement date set in Settings", "SEBI rules on running account settlement", true, "ras", ""},
	{"bsda_review", "BSDA review", "Depository", "Quarterly", "Checks every demat account against the BSDA rules: eligible regular accounts become BSDA unless the client opted out; BSDAs above the limit become regular. A checker applies it; the depository modification file is kept and clients are told.", "By the 10th after the quarter", "SEBI circular on Basic Services Demat Accounts", false, "dp", ""},
}

// PeriodicRun is one run of a task for a period.
type PeriodicRun struct {
	ID      string         `json:"id"` // task|period
	Task    string         `json:"task"`
	Period  string         `json:"period"`
	Status  string         `json:"status"` // Done, Waiting for approval, Failed
	Summary string         `json:"summary"`
	Files   []PeriodicFile `json:"files,omitempty"`
	By      string         `json:"by"`
	At      string         `json:"at"`
}

// PeriodicFile is a file a run produced.
type PeriodicFile struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// PeriodicStatus is a task's state on a date.
type PeriodicStatus struct {
	PeriodicTask
	Period string       `json:"period"`
	Label  string       `json:"label"` // the period for people
	DueOn  string       `json:"due_on"`
	Status string       `json:"status"` // Done, Due, Overdue, Upcoming, Waiting for approval, Failed, Nothing to do
	Note   string       `json:"note,omitempty"`
	Run    *PeriodicRun `json:"run,omitempty"`
}

func periodicTask(id string) (PeriodicTask, bool) {
	for _, t := range PeriodicTasks {
		if t.ID == id {
			return t, true
		}
	}
	return PeriodicTask{}, false
}

func prevMonth(date string) string { return addDays(date[:8]+"01", -1)[:7] }

func monthLabel(m string) string {
	t, err := time.Parse("2006-01", m)
	if err != nil {
		return m
	}
	return t.Format("January 2006")
}

// periodFor gives the period a task covers on date, its label and due date.
func (o *Ops) periodFor(ctx context.Context, tenant string, t PeriodicTask, date string) (period, label, due string) {
	switch t.ID {
	case "funds_securities":
		fri := lastFriday(date)
		return fri, fmtDay(addDays(fri, -6)) + " to " + fmtDay(fri), addDays(fri, 1)
	case "cuspa_review", "dis_master":
		fri := lastFriday(date)
		return fri, "Week to " + fmtDay(fri), addDays(fri, 3)
	case "month_end", "ccil_prices":
		m := prevMonth(date)
		return m, monthLabel(m), addMonths(m+"-01", 1)[:8] + "05"
	case "es_return":
		fri := lastFriday(date)
		return fri, "Week to " + fmtDay(fri), addDays(fri, 4)
	case "cuspa_mtf_payout":
		set, _ := o.Settings(ctx, tenant)
		td := set.PrevBusinessDay(date)
		return td, "Trades of " + fmtDay(td), set.NextBusinessDay(td)
	case "settlement_master":
		m := addDays(date, 10)[:7]
		return m, monthLabel(m), addDays(m+"-01", -1)
	case "ledger_statements", "complaints_disclosure":
		m := prevMonth(date)
		return m, monthLabel(m), addMonths(m+"-01", 1)[:8] + "07"
	case "dp_statements":
		m := prevMonth(date)
		return m, monthLabel(m), addMonths(m+"-01", 1)[:8] + "10"
	case "ras":
		set, _ := o.Settings(ctx, tenant)
		d := ""
		for _, x := range []string{set.RASMonthly, set.RASQuarterly} {
			if validDate(x) && (d == "" || x < d) {
				d = x
			}
		}
		return d, "Settlement of " + fmtDay(d), d
	case "bsda_review":
		end := prevQuarterEnd(date)
		return quarterID(end), quarterLabel(end), addDays(end, 10)
	}
	return date, fmtDay(date), date
}

func quarterLabel(end string) string {
	t, err := time.Parse("2006-01-02", end)
	if err != nil {
		return end
	}
	return t.AddDate(0, -2, 0).Format("Jan") + "–" + t.Format("Jan 2006")
}

// Periodic lists every task with its state on date.
func (o *Ops) Periodic(ctx context.Context, tenant, date string) ([]PeriodicStatus, error) {
	if !validDate(date) {
		return nil, invalid("give a date (YYYY-MM-DD)")
	}
	var out []PeriodicStatus
	for _, t := range PeriodicTasks {
		out = append(out, o.periodicStatus(ctx, tenant, t, date))
	}
	return out, nil
}

func (o *Ops) periodicStatus(ctx context.Context, tenant string, t PeriodicTask, date string) PeriodicStatus {
	p, label, due := o.periodFor(ctx, tenant, t, date)
	s := PeriodicStatus{PeriodicTask: t, Period: p, Label: label, DueOn: due}
	if run, _, err := docs.Get[PeriodicRun](ctx, o.Docs, tenant, cPeriodicRun, t.ID+"|"+p); err == nil {
		s.Run = &run
	}
	done, note := o.periodicDone(ctx, tenant, t, p, date)
	switch {
	case p == "":
		s.Status, s.Note = "Nothing to do", "No settlement date is set in Settings."
	case done:
		s.Status, s.Note = "Done", note
	case s.Run != nil && s.Run.Status != "Done" && s.Run.Status != "Due":
		s.Status, s.Note = s.Run.Status, s.Run.Summary
	case date > due:
		s.Status, s.Note = "Overdue", note
	case date == due || (t.ID != "ras" && date >= periodEndFor(t, p)):
		s.Status, s.Note = "Due", note
	default:
		s.Status, s.Note = "Upcoming", note
	}
	return s
}

// periodEndFor is the last day of the period: work can start the day after.
func periodEndFor(t PeriodicTask, p string) string {
	switch t.Freq {
	case "Weekly":
		return addDays(p, 1)
	case "Settlement day":
		return addDays(p, 1)
	case "Monthly":
		return addMonths(p+"-01", 1)
	case "Quarterly":
		return ""
	}
	return p
}

// periodicDone checks the records the task leaves behind, so work done
// elsewhere (by Autopilot or on its own screen) counts.
func (o *Ops) periodicDone(ctx context.Context, tenant string, t PeriodicTask, period, date string) (bool, string) {
	if run, _, err := docs.Get[PeriodicRun](ctx, o.Docs, tenant, cPeriodicRun, t.ID+"|"+period); err == nil && run.Status == "Done" {
		return true, run.Summary
	}
	switch t.ID {
	case "settlement_master":
		ds, _ := o.SettlementCalendar(ctx, tenant, period+"-01")
		for _, d := range ds {
			if d.Segment == "CM" && strings.HasPrefix(d.TradeDate, period) {
				return true, "Loaded from " + d.LoadedFrom
			}
		}
	case "ccil_prices":
		if p, ok := o.gsecPricesOn(ctx, tenant, monthEnd(period)); ok && p.Date >= addDays(monthEnd(period), -7) {
			return true, fmt.Sprintf("%d prices of %s", len(p.Prices), fmtDay(p.Date))
		}
	case "month_end":
		if _, _, err := docs.Get[AutoRun](ctx, o.Docs, tenant, cAutoRun, "monthly:"+period); err == nil {
			return true, "Posted by Autopilot"
		}
	case "ras":
		if period == "" {
			return false, ""
		}
		bs, _ := o.RASBatches(ctx, tenant, period)
		if len(bs) > 0 {
			n := 0
			for _, b := range bs {
				n += len(b.Items)
			}
			return true, fmt.Sprintf("%d clients settled", n)
		}
	case "bsda_review":
		r, _, err := docs.Get[BSDAReview](ctx, o.Docs, tenant, cBSDAReview, period)
		if err == nil && r.Status == "Applied" {
			return true, fmt.Sprintf("%d accounts changed, applied %s", len(r.Lines), r.AppliedAt)
		}
		if err == nil {
			return false, fmt.Sprintf("%d changes proposed, waiting for a checker", len(r.Lines))
		}
	}
	return false, ""
}

// RunPeriodic runs a task for a period ("" = the current one).
func (o *Ops) RunPeriodic(ctx context.Context, tenant, actor, id, period, date string) (PeriodicRun, error) {
	t, ok := periodicTask(id)
	if !ok {
		return PeriodicRun{}, fmt.Errorf("%w: periodic task %q", ErrNotFound, id)
	}
	if actor != SystemActor {
		if err := o.require(ctx, tenant, actor, "Running periodic work", RoleMaker, RoleChecker); err != nil {
			return PeriodicRun{}, err
		}
	}
	if !validDate(date) {
		date = o.now().Format("2006-01-02")
	}
	if period == "" {
		period, _, _ = o.periodFor(ctx, tenant, t, date)
	}
	if period == "" {
		return PeriodicRun{}, invalid("there is no period to run: set the settlement dates in Settings")
	}
	run := PeriodicRun{ID: t.ID + "|" + period, Task: t.ID, Period: period, Status: "Done", By: actor, At: o.now().Format("2006-01-02 15:04")}
	var err error
	run.Summary, run.Files, err = o.runPeriodic(ctx, tenant, actor, t, period, date)
	var pending *pendingErr
	switch {
	case errors.As(err, &pending):
		run.Status, run.Summary, err = firstOf(pending.status, "Waiting for approval"), pending.msg, nil
	case err != nil:
		run.Status, run.Summary = "Failed", strings.TrimPrefix(err.Error(), "invalid: ")
	}
	if perr := docs.Upsert(ctx, o.Docs, tenant, cPeriodicRun, run.ID, run, actor); perr != nil {
		return run, perr
	}
	o.log(ctx, tenant, actor, "periodic.run", run.ID, map[string]any{"status": run.Status, "summary": run.Summary})
	return run, err
}

// unattended says whether a scheduled run may do an Autopilot process
// itself: only when its policy is Auto. Otherwise the approval is queued
// (Suggest) or the work is left to a person (Off, or the Manual edition).
func (o *Ops) unattended(ctx context.Context, tenant, process, date string, queue func(*runCtx)) (bool, error) {
	if o.manual(ctx, tenant) {
		return false, &pendingErr{"Nothing runs by itself in the Manual edition: run it here when ready", "Due"}
	}
	_, mode, params := o.policy(ctx, tenant, process)
	switch mode {
	case ModeAuto:
		return true, nil
	case ModeSuggest:
		r := &runCtx{o: o, ctx: ctx, tenant: tenant, date: date, mode: mode, params: params, process: process, keys: map[string]bool{}}
		queue(r)
		return false, &pendingErr{"Waiting for a checker to approve it in the Autopilot work queue", "Waiting for approval"}
	}
	return false, &pendingErr{"Switched off in Autopilot: run it here by hand", "Due"}
}

// pendingErr: the task did its part and waits for a person.
type pendingErr struct{ msg, status string }

func (p *pendingErr) Error() string { return p.msg }

func (o *Ops) keepFile(ctx context.Context, tenant, name string, data []byte) (PeriodicFile, error) {
	rf, err := o.App.Ingest.Raw.Put(ctx, ingest.RawFile{Tenant: tenant, Source: "PERIODIC-OUT", Name: name, SHA256: sha256Hex(data), Size: int64(len(data)), Content: data, ReceivedAt: o.now()})
	return PeriodicFile{ID: rf.ID, Name: name}, err
}

func (o *Ops) runPeriodic(ctx context.Context, tenant, actor string, t PeriodicTask, period, date string) (string, []PeriodicFile, error) {
	switch t.ID {
	case "funds_securities":
		res, err := o.SendStatements(ctx, tenant, actor, DocFundsSec, period)
		if err != nil {
			return "", nil, err
		}
		if len(res.Errors) > 0 {
			return statementSummary(res), nil, fmt.Errorf("%d statements failed: %s", len(res.Errors), res.Errors[0])
		}
		return statementSummary(res), nil, nil
	case "ledger_statements", "dp_statements":
		kind := map[string]string{"ledger_statements": DocLedger, "dp_statements": DocDP}[t.ID]
		res, err := o.SendStatements(ctx, tenant, actor, kind, period)
		if err != nil {
			return "", nil, err
		}
		if len(res.Errors) > 0 {
			return statementSummary(res), nil, fmt.Errorf("%d statements failed: %s", len(res.Errors), res.Errors[0])
		}
		return statementSummary(res), nil, nil
	case "month_end":
		// Autopilot's month-end policy decides, however the work is started:
		// unattended it posts only on Auto and otherwise asks a checker in
		// the work queue; a person running it must be a checker.
		if actor == SystemActor {
			if ok, why := o.unattended(ctx, tenant, "monthly", date, func(r *runCtx) { r.queueMonthly(period) }); !ok {
				return "", nil, why
			}
		} else if err := o.require(ctx, tenant, actor, "Month-end postings", RoleChecker); err != nil {
			return "", nil, err
		}
		s, err := o.runMonthly(ctx, tenant, actor, period)
		return s, nil, err
	case "complaints_disclosure":
		d, err := o.ComplaintsDisclosure(ctx, tenant, period)
		if err != nil {
			return "", nil, err
		}
		rows := [][]string{{"Received from", "Pending at the end of last month", "Received", "Resolved", "Total pending", "Pending over 3 months", "Average resolution time (days)"}}
		for _, r := range append(d.Rows, d.Total) {
			rows = append(rows, []string{r.Source, fmt.Sprint(r.PendingAtOpen), fmt.Sprint(r.Received), fmt.Sprint(r.Resolved), fmt.Sprint(r.Pending), fmt.Sprint(r.Over3Months), fmt.Sprint(r.AvgDays)})
		}
		f, err := o.keepFile(ctx, tenant, "ComplaintsDisclosure_"+period+".csv", csvBytesOf(rows))
		if err != nil {
			return "", nil, err
		}
		return fmt.Sprintf("%d received, %d resolved, %d pending at the end of %s", d.Total.Received, d.Total.Resolved, d.Total.Pending, monthLabel(period)), []PeriodicFile{f}, nil
	case "ras":
		if date < period {
			return "", nil, invalid("the settlement date %s has not come", period)
		}
		if actor == SystemActor {
			if ok, why := o.unattended(ctx, tenant, "ras", date, func(r *runCtx) { r.queueRAS(period) }); !ok {
				return "", nil, why
			}
		}
		s, err := o.settleRAS(ctx, tenant, actor, period)
		if errors.Is(err, ErrState) {
			// Settled already (by Autopilot or by hand): send any statements not sent.
			res, serr := o.SendStatements(ctx, tenant, actor, DocRAS, period)
			if serr != nil {
				return "", nil, serr
			}
			return "Settled already; statements: " + statementSummary(res), nil, nil
		}
		return s, nil, err
	case "cuspa_review":
		return o.cuspaReview(ctx, tenant, actor, date)
	case "es_return":
		return o.runESReturn(ctx, tenant, period)
	case "settlement_master":
		msg := fmt.Sprintf("NSE Clearing's settlement master for %s is not loaded: download it from the member portal and load it on Exchange files", monthLabel(period))
		o.raiseWork(ctx, tenant, "periodic", "settmaster:"+period, "Load the settlement master for "+monthLabel(period), msg, "", RoleMaker, "xfiles", date)
		return "", nil, &pendingErr{msg, "Waiting for a file"}
	case "ccil_prices":
		msg := fmt.Sprintf("CCIL's prices for the end of %s are not loaded: download MTMPRICE for the last business day from CCIL and load it on Exchange files", monthLabel(period))
		o.raiseWork(ctx, tenant, "periodic", "ccil:"+period, "Load CCIL's G-sec prices for "+monthLabel(period), msg, "", RoleMaker, "xfiles", date)
		return "", nil, &pendingErr{msg, "Waiting for a file"}
	case "dis_master":
		lib, err := XFiles()
		if err != nil {
			return "", nil, err
		}
		s, _ := lib.Get("cdsl-txn-upload")
		set, _ := o.Settings(ctx, tenant)
		name, content, err := o.disMasterFile(ctx, tenant, s, set, period, map[string]string{"date": period, "seq": "1"})
		if errors.Is(err, ErrState) {
			return "No DIS books issued in the week", nil, nil
		}
		if err != nil {
			return "", nil, err
		}
		f, err := o.keepFile(ctx, tenant, name, content)
		if err != nil {
			return "", nil, err
		}
		o.raiseWork(ctx, tenant, "periodic", "dismaster:"+period, "Upload "+name+" to CDSL", "The DIS master upload for books issued in the week to "+fmtDay(period)+" is on Periodic work.", "", RoleMaker, "periodic", date)
		return name + " written", []PeriodicFile{f}, nil
	case "cuspa_mtf_payout":
		lib, err := XFiles()
		if err != nil {
			return "", nil, err
		}
		s, _ := lib.Get("ncl-cuspa-mtf-payout")
		set, _ := o.Settings(ctx, tenant)
		name, content, err := o.cuspaMTFFile(ctx, tenant, s, set, period, map[string]string{"date": period, "seq": "1"})
		if errors.Is(err, ErrState) {
			return "Nothing to report: no unpaid or MTF purchases", nil, nil
		}
		if err != nil {
			return "", nil, err
		}
		f, err := o.keepFile(ctx, tenant, name, content)
		if err != nil {
			return "", nil, err
		}
		o.raiseWork(ctx, tenant, "periodic", "cuspamtf:"+period, "Upload "+name+" to NSE Clearing", "The CUSPA and MTF pay-out reporting file for trades of "+fmtDay(period)+" is on Periodic work; upload it before the pay-out.", "", RoleMaker, "periodic", date)
		return fmt.Sprintf("%s written", name), []PeriodicFile{f}, nil
	case "bsda_review":
		if r, _, err := docs.Get[BSDAReview](ctx, o.Docs, tenant, cBSDAReview, period); err == nil && r.Status == "Applied" {
			return fmt.Sprintf("%d accounts changed", len(r.Lines)), []PeriodicFile{{r.FileID, r.FileName}}, nil
		}
		end := prevQuarterEnd(addDays(date, 0))
		if quarterID(end) != period {
			end = quarterEndOf(period)
		}
		r, err := o.ReviewBSDA(ctx, tenant, actor, end)
		if err != nil {
			return "", nil, err
		}
		if len(r.Lines) == 0 {
			r2, err := o.ApplyBSDAReview(ctx, tenant, SystemActor, r.ID)
			if err != nil {
				return "", nil, err
			}
			return fmt.Sprintf("No changes: %d accounts reviewed, %d BSDA", r2.Accounts, r2.BSDA), nil, nil
		}
		o.raiseWork(ctx, tenant, "periodic", "bsda:"+r.ID, fmt.Sprintf("Apply the BSDA review for %s", r.ID),
			fmt.Sprintf("%d demat accounts to change (%d reviewed). Check and apply on the Depository screen.", len(r.Lines), r.Accounts), "", RoleChecker, "dp", date)
		return "", nil, &pendingErr{fmt.Sprintf("%d changes proposed; a checker applies them on the Depository screen", len(r.Lines)), ""}
	}
	return "", nil, invalid("%s is not run from here", t.Name)
}

func quarterEndOf(q string) string {
	var y, n int
	fmt.Sscanf(q, "%d-Q%d", &y, &n)
	return addDays(fmt.Sprintf("%04d-%02d-01", y, n*3+1), -1)
}

// cuspaReview releases paid-up CUSPA holdings and queues the overdue ones.
func (o *Ops) cuspaReview(ctx context.Context, tenant, actor, date string) (string, []PeriodicFile, error) {
	set, _ := o.Settings(ctx, tenant)
	funds, err := o.clientFunds(ctx, tenant, "", "")
	if err != nil {
		return "", nil, err
	}
	items, _ := docs.List[CUSPAItem](ctx, o.Docs, tenant, cCUSPA, "")
	sort.Slice(items, func(i, j int) bool { return items[i].Date < items[j].Date })
	released, overdue, waiting := 0, 0, 0
	limit := set.CUSPADays
	if limit <= 0 {
		limit = 7
	}
	for _, it := range items {
		if it.Status != "Held in CUSPA" {
			continue
		}
		if funds[it.UCC] >= 0 {
			if _, err := o.ReleaseCUSPA(ctx, tenant, SystemActor, it.ID); err == nil {
				released++
				continue
			}
		}
		if businessDaysBetween(set, it.Date, date) > limit {
			overdue++
			o.raiseWork(ctx, tenant, "periodic", "cuspa:"+it.ID, fmt.Sprintf("Sell unpaid %s of %s", it.Symbol, it.UCC),
				fmt.Sprintf("%d %s bought on %s are still unpaid after %d trading days; sell them and apply the proceeds to the debit.", it.Qty, it.Symbol, it.Date, limit), it.UCC, RoleChecker, "settle", date)
			continue
		}
		waiting++
	}
	return fmt.Sprintf("%d released to clients who paid, %d overdue queued for sale, %d still within the allowed days", released, overdue, waiting), nil, nil
}

func businessDaysBetween(set Settings, from, to string) int {
	n := 0
	for d := addDays(from, 1); d <= to; d = addDays(d, 1) {
		if set.IsBusinessDay(d) {
			n++
		}
	}
	return n
}

// RunDuePeriodic runs every unattended task that is due and not done, and
// queues the others for people. The scheduler calls it every morning.
func (o *Ops) RunDuePeriodic(ctx context.Context, tenant, date string) (string, error) {
	var ran, queued, failed []string
	for _, t := range PeriodicTasks {
		s := o.periodicStatus(ctx, tenant, t, date)
		if s.Status != "Due" && s.Status != "Overdue" && s.Status != "Failed" {
			continue
		}
		if !t.Auto {
			if t.ID == "bsda_review" { // proposes, then waits for a checker
				if _, err := o.RunPeriodic(ctx, tenant, SystemActor, t.ID, s.Period, date); err != nil {
					failed = append(failed, t.Name+": "+err.Error())
				} else {
					queued = append(queued, t.Name)
				}
				continue
			}
			o.raiseWork(ctx, tenant, "periodic", t.ID+":"+s.Period, t.Name+" for "+s.Label+" is "+strings.ToLower(s.Status), t.About, "", RoleChecker, "periodic", date)
			queued = append(queued, t.Name)
			continue
		}
		r, err := o.RunPeriodic(ctx, tenant, SystemActor, t.ID, s.Period, date)
		if err != nil || r.Status == "Failed" {
			failed = append(failed, t.Name+": "+firstOf(r.Summary, fmt.Sprint(err)))
			continue
		}
		ran = append(ran, t.Name)
	}
	msg := fmt.Sprintf("%d run, %d queued for people, %d failed", len(ran), len(queued), len(failed))
	if len(failed) > 0 {
		return msg, fmt.Errorf("%s", strings.Join(failed, "; "))
	}
	return msg, nil
}

// PeriodicFileData returns a file a periodic run produced.
func (o *Ops) PeriodicFileData(ctx context.Context, tenant, actor, id string) (ingest.RawFile, error) {
	if err := o.require(ctx, tenant, actor, "Downloading periodic files", RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return ingest.RawFile{}, err
	}
	f, err := o.App.Ingest.Raw.Get(ctx, tenant, id)
	if err != nil {
		return f, err
	}
	if f.Source != "PERIODIC-OUT" {
		return ingest.RawFile{}, fmt.Errorf("%w: file %s", ErrNotFound, id)
	}
	o.log(ctx, tenant, actor, "periodic.file", id, map[string]any{"name": f.Name})
	return f, nil
}
