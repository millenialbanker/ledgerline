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
	{"cuspa_review", "Unpaid securities (CUSPA): daily review", "Settlement", "Daily", "Every trading day under SEBI's CUSPA framework: records the shares the depository released at the end of the 6th trading day after pay-out, releases what is over each client's limit of 2.25 × (margin − ledger balance) oldest first, finds the sale of invoked shares in the trades, and puts in the work queue the notices, invocations (up to the 5th trading day after pay-out) and sales due.", "Each trading day, before the market opens", "SEBI circular HO/38/11/(9)2026-MIRSD-POD/I/15382/2026 (CUSPA framework); NSE circulars 35/2026 and 41/2026", true, "settlement", ""},
	{"dis_master", "DIS master upload to CDSL", "Depository", "Weekly", "Builds CDSL's transaction upload (DIS issue) for the delivery instruction slip books issued in the week and sends it through CDSL's common upload API when it is on (else for upload on CDSL's system).", "Monday, for the week to Friday", "CDSL operating instructions on DIS issuance", true, "dp", "cdsl-txn-upload"},
	{"ap_mapping", "Clients mapped to authorised persons: weekly report to each exchange", "Compliance", "Weekly", "Makes each exchange's file of the week's clients newly mapped, re-mapped or unmapped to an authorised person, and new clients of APs, checked against the client's UCC at that exchange, and queues the upload. Upload it on each exchange's portal and record the acknowledgement on Branches & partners → AP client reporting.", "By the 2nd trading day of the next week", "NSE/COMP/49509 (3 Sep 2021); MSE circular 10854 (4 Sep 2021), item 15 of MSE/MEM/15275/2024 (30 Apr 2024)", true, "partners", "nse-ap-client-mapping"},
	{"cuspa_mtf_payout", "CUSPA and MTF pay-out reporting", "Settlement", "Settlement day", "Reports to NSE Clearing the settlement's purchases that are not paid out to clients' demat accounts: unpaid purchases kept in CUSPA and MTF purchases kept for the pledge.", "Settlement day, before pay-out", "SEBI and NSE Clearing rules on pay-out of securities to clients", true, "settlement", "ncl-cuspa-mtf-payout"},
	{"settlement_master", "Settlement calendar for the coming month", "Settlement", "Monthly", "Checks that NSE Clearing's settlement master for the coming month is loaded: settlement numbers and pay-in and pay-out dates used by the pay-out reporting and the settlement screen.", "Before the month starts", "", true, "xfiles", "ncl-settlement-master"},
	{"ccil_prices", "CCIL prices of G-secs and T-bills", "Risk", "Monthly", "Checks that CCIL's month-end valuation prices are loaded, so G-secs and T-bills held or given as collateral are valued at month end. With the CCIL prices connection's fetch on, they are downloaded from CCIL's website each business day and the task is done by itself.", "By the 5th", "", true, "xfiles", "ccil-gsec-price"},
	{"month_end", "Month-end postings", "Accounts", "Monthly", "Interest on debit balances, MTF interest, DP bills with GST and the authorised persons' brokerage share (commission) for the month just ended.", "By the 5th", "", true, "autopilot", ""},
	{"ledger_statements", "Monthly statement of account", "Clients", "Monthly", "Emails every active client a signed statement of account for the month just ended.", "By the 7th", "Exchange rules on statements of accounts to clients", true, "dispatch", ""},
	{"dp_statements", "DP transaction and holding statements", "Depository", "Monthly", "Emails every demat account its signed statement of transactions and holdings for the month just ended.", "By the 10th", "Depository rules on statements to beneficial owners", true, "dp", ""},
	{"kra_demise", "Deceased clients reported by the KRAs: close the UCCs", "Compliance", "Monthly", "Each month NSE posts <TMcode>_Demise_Clients_by_KRA on ENIT (Log > Downloads) with a cut-off in that month's circular. Load it on Compliance → Deceased clients: debits are blocked at once, and each client's UCC is closed on every exchange after the death is verified, before the cut-off; the nominee or legal heirs get the transmission procedure within 5 days of the KRA notice.", "Before the cut-off in the month's NSE circular", "NSE/ISC/77375 of 9 Nov 2026 and NSE/ISC/76893 of 14 Oct 2026; SEBI/HO/OIAE/OIAE_IAD-1/P/CIR/2023/0000000163 of 3 Oct 2023", true, "deceased", "nse-kra-demise"},
	{"complaints_disclosure", "Investor complaints disclosure", "Compliance", "Monthly", "Builds the month's complaints tables for the website: the broker's, the DP's when the firm is a depository participant, and the research analyst's and investment adviser's, each with its line for complaints due to impersonation, when the firm holds those registrations.", "By the 7th", "SEBI/HO/MIRSD/MIRSD-PoD1/P/CIR/2025/22 (Annexure B); RA and IA master circulars of 6 Feb 2026 (Annexure E, Annexure C)", true, "helpdesk", ""},
	{"raia_impersonation", "Impersonation cases to BSE (research analyst, investment adviser)", "Compliance", "Monthly", "Builds the month's report of the cases of someone impersonating the firm's research analyst or investment adviser business, with the number of complaints they caused, for BSE's portal (RAIA Inspection → Report Impersonation). With no case, record the month's nil check.", "Within 7 days of the month's end", "BSE notices 20250804-58 (IA) and 20250804-59 (RA) of 4 Aug 2025", true, "helpdesk", ""},
	{"ap_client_count", "AP client counts reconciled with each exchange", "Compliance", "Monthly", "Compares the number of clients each exchange shows for each authorised person at the month's end with Ledgerline's own count, so the exchange's records stay current.", "By the 10th (Ledgerline's date; no circular sets one)", "NSE/COMP/63628 (28 Aug 2024), para 5", true, "partners", ""},
	{"dp_impersonation", "Impersonation of the DP: report to the depository", "Depository", "Monthly", "Builds the month's report of the cases of someone impersonating the firm's depository participant business, in NSDL's columns (DP ID, DP name, particulars, action taken, status), to email to NSDL; with no case there is nothing to send.", "By the 10th", "NSDL/POLICY/2024/0048 (18 Apr 2024); CDSL communiqué DP2024-234 (30 Apr 2024)", true, "helpdesk", ""},
	{"ras", "Running account settlement", "Money", "Monthly / quarterly", "Settles running accounts on the settlement date (monthly or quarterly as each client chose), pays out the funds and sends each client the settlement statement.", "On the exchanges' RAS dates (Circulars & calendar)", "SEBI rules on running account settlement", true, "ras", ""},
	{"mnrl_trading", "Revoked mobiles (MNRL): trading accounts", "Clients", "Daily", "Every trading day: load NSE's report for disconnected mobile numbers (or DoT's MNRL from DIP) on Clients & onboarding → Revoked mobiles. Each client whose mobile is on it is flagged, SMS and WhatsApp to the number are held, and an Ops maker confirms the client's number through another registered channel and has it changed; the UCC modification follows.", "Each trading day", "NSE/ISC/72805 of 13 Feb 2026 (daily report); NSE/ISC/45168 of 29 Jul 2020 and NSE/ISC/47834 of 31 Mar 2021 (confirm and update the back office and the UCC)", true, "mnrl", "nse-mnrl"},
	{"mnrl_demat", "Revoked mobiles (MNRL): demat accounts", "Depository", "Daily", "Every business day: load DoT's MNRL from DIP (CDSL accounts) or NSDL's MNRL matches from e-PASS (NSDL accounts), and each month NSDL's reactivation data, on Clients & onboarding → Revoked mobiles. Matches are flagged and held as for trading accounts; a number DoT reconnected to the same person lifts only the MNRL hold.", "Each business day", "CDSL/OPS/DP/POLCY/2026/517 of 30 Jul 2026 (daily on DIP); NSDL/POLICY/2026/0090 of 16 Jun 2026 (e-PASS, monthly reactivation data)", true, "mnrl", "dot-mnrl"},
	{"ras_idle", "Return idle credit balances (30 days)", "Money", "Daily", "Every morning: clients with a credit balance and no transaction (trade, open position or funds received) for 30 calendar days are settled and paid out on the upcoming monthly settlement date of the exchanges' calendar, whatever their monthly or quarterly choice, under the running account settlement policy in Autopilot; one still unpaid after that date is settled at once.", "On the upcoming monthly settlement date", "SEBI/HO/MIRSD/MIRSD-PoD1/P/CIR/2025/1 of 6 January 2025 (NSE/INSP/66014)", true, "ras", ""},
	{"ras_ucc_report", "UCC-wise running account settlement report", "Money", "Monthly / quarterly", "After each running account settlement: makes the file of what each client was paid and retained, for upload on NSE's member portal, and keeps a reminder open until the upload (or the not-applicable declaration) is recorded on Running account.", "Within 10 trading days of each settlement date", "NSE/INSP/55024 (kept by NSE/INSP/61904); NSE/INSP/55135; NSE/INSP/56261 (₹10,000 a day late)", true, "ras", "nse-ras-ucc"},
	{"client_money_recon", "Daily client money reconciliation", "Money", "Daily", "Each business day: the statement of money moved between clients, the USCNBA, the clearing corporation, the DSCNBA, the settlement and own accounts, with what needs explaining, prepared for a checker to sign off on Client money reconciliation.", "The next business day", "SEBI master circular SEBI/HO/MIRSD/MIRSD-PoD/P/CIR/2025/90, para 15.4.4.2; NSE/INSP/55904", true, "clientmoney", ""},
	{"bsda_review", "BSDA review", "Depository", "Quarterly", "Checks every demat account against the BSDA rules: eligible regular accounts become BSDA unless the client opted out; BSDAs above the limit become regular. A checker applies it; the depository modification file is kept and clients are told.", "By the 10th after the quarter", "SEBI circular on Basic Services Demat Accounts", false, "dp", ""},
}

// periodicApplies are the tasks that only some firms have, with who they
// apply to, read against the firm profile as the compliance calendar's
// duties are (compduties.go dutyNeed). A task that does not apply is listed
// with nothing to do and does not run.
var periodicApplies = map[string][]string{
	"ap_mapping":         {"broker", "ap"},
	"ap_client_count":    {"broker", "ap"},
	"raia_impersonation": {"ra|ia"},
	"dp_impersonation":   {"dp"},
}

// periodicApplicability says whether a task applies to the firm (nil: to
// every firm).
func (o *Ops) periodicApplicability(ctx context.Context, tenant, id string) *Applicability {
	needs := periodicApplies[id]
	if len(needs) == 0 {
		return nil
	}
	f, _ := o.firmProfileFactsOpt(ctx, tenant, false)
	a := dutyApplies(needs, f)
	return &a
}

// RetiredPeriodicTasks were periodic tasks that no rule requires any more,
// like RetiredJobKinds for scheduled jobs: they are not listed, never run
// and their open work items are cleared. Their past runs and files are
// kept for inspection.
var RetiredPeriodicTasks = map[string]string{
	// The weekly enhanced-supervision return of client funds (12
	// particulars, NSE/INSP/37395) was discontinued by NSE/INSP/60283 of
	// 16 Jan 2024 (SEBI/HO/MIRSD/MIRSD-PoD-1/P/CIR/2024/03), and the
	// client-level cash balance reporting by NSE/INSP/61121 from 18 Mar
	// 2024. The exchanges now monitor client funds from the banks' and
	// clearing corporations' data and the client-level collateral reports
	// (Segregation and monitoring), which Ledgerline still files.
	"es_return": "Discontinued: client funds monitoring return (NSE/INSP/60283, NSE/INSP/61121)",
}

// retiredWorkKeys are the key prefixes of the work items a retired task
// raised (process "periodic").
var retiredWorkKeys = map[string]string{"es_return": "periodic:es:"}

// clearRetiredWork closes the open work items of retired tasks.
func (o *Ops) clearRetiredWork(ctx context.Context, tenant string) int {
	ws, _ := docs.List[WorkItem](ctx, o.Docs, tenant, cWorkItem, "")
	n := 0
	for _, w := range ws {
		if w.Process != "periodic" || w.Status != "Open" {
			continue
		}
		for id, prefix := range retiredWorkKeys {
			if strings.HasPrefix(w.Key, prefix) {
				w.Status, w.Closed, w.ClosedBy, w.Note = "Cleared", o.now().Format(time.RFC3339), SystemActor, "no longer required: "+RetiredPeriodicTasks[id]
				_ = docs.Upsert(ctx, o.Docs, tenant, cWorkItem, w.ID, w, SystemActor)
				n++
			}
		}
	}
	return n
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
	case "dis_master":
		fri := lastFriday(date)
		return fri, "Week to " + fmtDay(fri), addDays(fri, 3)
	case "month_end", "ccil_prices":
		m := prevMonth(date)
		return m, monthLabel(m), addMonths(m+"-01", 1)[:8] + "05"
	case "cuspa_mtf_payout":
		set, _ := o.Settings(ctx, tenant)
		td := set.PrevBusinessDay(date)
		return td, "Trades of " + fmtDay(td), set.NextBusinessDay(td)
	case "settlement_master":
		m := addDays(date, 10)[:7]
		return m, monthLabel(m), addDays(m+"-01", -1)
	case "ledger_statements", "complaints_disclosure", "raia_impersonation":
		// raia_impersonation: within 7 days of the month's end (BSE notices
		// 20250804-58 and 20250804-59).
		m := prevMonth(date)
		return m, monthLabel(m), addMonths(m+"-01", 1)[:8] + "07"
	case "dp_statements", "dp_impersonation":
		// dp_impersonation: by the 10th of the following month
		// (NSDL/POLICY/2024/0048).
		m := prevMonth(date)
		return m, monthLabel(m), addMonths(m+"-01", 1)[:8] + "10"
	case "ap_mapping":
		// The week Monday to Sunday, due on the 2nd trading day of the next
		// week (MSE/MEM/15275/2024, item 15; NSE/COMP/49509).
		set, _ := o.Settings(ctx, tenant)
		sun := apLastWeek(date)
		return sun, "Week of " + apWeekLabel(sun), apWeekDue(set, sun)
	case "ap_client_count":
		m := prevMonth(date)
		return m, monthLabel(m), apCountDue(m)
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
	case "mnrl_trading", "mnrl_demat":
		if !o.mnrlTaskApplies(ctx, tenant, t.ID) {
			return "", "", ""
		}
		set, _ := o.Settings(ctx, tenant)
		d := date
		if !set.IsBusinessDay(d) {
			d = set.PrevBusinessDay(d)
		}
		return d, fmtDay(d), d
	case "kra_demise":
		if !o.mnrlTaskApplies(ctx, tenant, "mnrl_trading") {
			return "", "", ""
		}
		m := prevMonth(date)
		return m, "PANs reported up to the end of " + monthLabel(m), o.demiseDue(ctx, tenant, m)
	case "ras_ucc_report":
		return o.rasUCCPeriod(ctx, tenant, date)
	case "client_money_recon":
		set, _ := o.Settings(ctx, tenant)
		d := set.PrevBusinessDay(date)
		return d, "Statement of " + fmtDay(d), set.NextBusinessDay(d)
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
	if a := o.periodicApplicability(ctx, tenant, t.ID); a != nil && a.Verdict == ApplyNo {
		s.Status, s.Note, s.DueOn = "Nothing to do", "Doesn't apply: "+a.Reason+".", ""
		return s
	}
	if run, _, err := docs.Get[PeriodicRun](ctx, o.Docs, tenant, cPeriodicRun, t.ID+"|"+p); err == nil {
		s.Run = &run
	}
	done, note := o.periodicDone(ctx, tenant, t, p, date)
	switch {
	case p == "":
		s.Status, s.Note = "Nothing to do", firstOf(listTaskNone[t.ID], "No RAS date is listed: add the exchanges' dates on Circulars & calendar → RAS dates & profile.")
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
	case "ras_idle":
		due, _, err := o.IdleRASDue(ctx, tenant, period)
		if err == nil && len(due) == 0 {
			return true, "No idle credit balance to return"
		}
	case "ras_ucc_report":
		return o.rasUCCDone(ctx, tenant, period)
	case "client_money_recon":
		return o.moneyReconDone(ctx, tenant, period)
	case "bsda_review":
		r, _, err := docs.Get[BSDAReview](ctx, o.Docs, tenant, cBSDAReview, period)
		if err == nil && r.Status == "Applied" {
			return true, fmt.Sprintf("%d accounts changed, applied %s", len(r.Lines), r.AppliedAt)
		}
		if err == nil {
			return false, fmt.Sprintf("%d changes proposed, waiting for a checker", len(r.Lines))
		}
	case "mnrl_trading", "mnrl_demat":
		if l, ok := o.mnrlLoadedOn(ctx, tenant, period, mnrlTaskLists[t.ID]...); ok {
			return true, fmt.Sprintf("%s list of %s loaded: %d new match%s", l.Source, fmtDay(l.Date), l.New, map[bool]string{true: "es", false: ""}[l.New != 1])
		}
	case "kra_demise":
		return o.demiseDone(ctx, tenant, period)
	case "ap_mapping":
		return o.apWeekDone(ctx, tenant, period, date)
	case "ap_client_count":
		return o.apCountDone(ctx, tenant, period)
	case "raia_impersonation":
		return o.impReportsDone(ctx, tenant, period, impBodiesRAIA)
	case "dp_impersonation":
		return o.impReportsDone(ctx, tenant, period, impBodiesDP)
	}
	return false, ""
}

// RunPeriodic runs a task for a period ("" = the current one).
func (o *Ops) RunPeriodic(ctx context.Context, tenant, actor, id, period, date string) (PeriodicRun, error) {
	if why, gone := RetiredPeriodicTasks[id]; gone {
		return PeriodicRun{}, fmt.Errorf("%w: periodic task %q is retired (%s)", ErrState, id, why)
	}
	t, ok := periodicTask(id)
	if !ok {
		return PeriodicRun{}, fmt.Errorf("%w: periodic task %q", ErrNotFound, id)
	}
	if a := o.periodicApplicability(ctx, tenant, id); a != nil && a.Verdict == ApplyNo {
		return PeriodicRun{}, fmt.Errorf("%w: %s doesn't apply to the firm: %s (Settings → Firm profile)", ErrState, t.Name, a.Reason)
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
		if why := listTaskNone[t.ID]; why != "" {
			return PeriodicRun{}, invalid("nothing to run: %s", why)
		}
		return PeriodicRun{}, invalid("there is no period to run: add the exchanges' RAS dates on Circulars & calendar → RAS dates & profile")
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
	return o.unattendedAs(ctx, tenant, process, process, date, queue)
}

// unattendedAs is unattended with the approval queued under its own work
// process, so the Autopilot run of the policy does not clear it.
func (o *Ops) unattendedAs(ctx context.Context, tenant, process, workProcess, date string, queue func(*runCtx)) (bool, error) {
	if o.manual(ctx, tenant) {
		return false, &pendingErr{"Nothing runs by itself in the Manual edition: run it here when ready", "Due"}
	}
	_, mode, params := o.policy(ctx, tenant, process)
	switch mode {
	case ModeAuto:
		return true, nil
	case ModeSuggest:
		r := &runCtx{o: o, ctx: ctx, tenant: tenant, date: date, mode: mode, params: params, process: workProcess, keys: map[string]bool{}}
		queue(r)
		return false, &pendingErr{"Waiting for a checker to approve it in the Autopilot work queue", "Waiting for approval"}
	}
	return false, &pendingErr{"Switched off in Autopilot: run it here by hand", "Due"}
}

// wRASIdle is the work process of the 30-day idle balance approvals.
const wRASIdle = "ras_idle"

// closeIdleRASWork closes the open idle-balance approvals once nothing is
// left to return.
func (o *Ops) closeIdleRASWork(ctx context.Context, tenant, actor string) {
	ws, _ := docs.List[WorkItem](ctx, o.Docs, tenant, cWorkItem, "")
	for _, w := range ws {
		if w.Process == wRASIdle && w.Status == "Open" {
			o.closeWork(ctx, tenant, actor, wRASIdle, strings.TrimPrefix(w.Key, wRASIdle+":"), "idle balances returned")
		}
	}
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
		// One table for each business the firm has: the broker's always,
		// the DP's, the RA's and the IA's when it has them (helpdesk.go).
		var files []PeriodicFile
		var parts []string
		for _, biz := range o.disclosureBusinesses(ctx, tenant) {
			d, err := o.ComplaintsDisclosureFor(ctx, tenant, biz, period)
			if err != nil {
				return "", nil, err
			}
			name := "ComplaintsDisclosure_" + period + ".csv"
			if biz != BizBroking {
				name = "ComplaintsDisclosure_" + biz + "_" + period + ".csv"
			}
			f, err := o.keepFile(ctx, tenant, name, d.CSV())
			if err != nil {
				return "", nil, err
			}
			files = append(files, f)
			part := fmt.Sprintf("%d received, %d resolved, %d pending at the end of %s", d.Total.Received, d.Total.Resolved, d.Total.Pending, monthLabel(period))
			if biz != BizBroking {
				part = biz + ": " + part
				if d.Impersonation != nil {
					part += fmt.Sprintf(", %d due to impersonation", *d.Impersonation)
				}
			}
			parts = append(parts, part)
		}
		return strings.Join(parts, "; "), files, nil
	case "ap_mapping":
		return o.runAPMapping(ctx, tenant, period, date)
	case "ap_client_count":
		return o.runAPCount(ctx, tenant, period, date)
	case "raia_impersonation":
		return o.runImpReports(ctx, tenant, period, date, impBodiesRAIA)
	case "dp_impersonation":
		return o.runImpReports(ctx, tenant, period, date, impBodiesDP)
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
	case "ras_idle":
		// The 30-day rule: settled under the RAS policy (Auto settles; Suggest
		// asks a checker; a person running it must be a checker, as RunRAS
		// requires).
		due, overdue, err := o.IdleRASDue(ctx, tenant, period)
		if err != nil {
			return "", nil, err
		}
		if len(due) == 0 {
			o.closeIdleRASWork(ctx, tenant, actor)
			return "No idle credit balance to return", nil, nil
		}
		if actor == SystemActor {
			if ok, why := o.unattendedAs(ctx, tenant, "ras", wRASIdle, date, func(r *runCtx) { queueIdleRAS(r, period, due) }); !ok {
				if len(overdue) > 0 {
					o.log(ctx, tenant, actor, "ras.idle.overdue", period, map[string]any{"clients": len(overdue)})
				}
				return "", nil, why
			}
		}
		s, err := o.settleRAS(ctx, tenant, actor, period)
		if err != nil {
			return "", nil, err
		}
		o.closeIdleRASWork(ctx, tenant, actor)
		return fmt.Sprintf("Idle balances (30 days): %s", s), nil, nil
	case "cuspa_review":
		return o.cuspaReview(ctx, tenant, actor, date)
	case "mnrl_trading", "mnrl_demat":
		s, err := o.mnrlPeriodic(ctx, tenant, actor, t.ID, period)
		return s, nil, err
	case "kra_demise":
		s, err := o.demisePeriodic(ctx, tenant, actor, period, date)
		return s, nil, err
	case "ras_ucc_report":
		return o.rasUCCRun(ctx, tenant, actor, period, date)
	case "client_money_recon":
		return o.moneyReconRun(ctx, tenant, actor, period)
	case "settlement_master":
		msg := fmt.Sprintf("NSE Clearing's settlement master for %s is not loaded: download it from the member portal and load it on Exchange files", monthLabel(period))
		o.raiseWork(ctx, tenant, "periodic", "settmaster:"+period, "Load the settlement master for "+monthLabel(period), msg, "", RoleMaker, "xfiles", date)
		return "", nil, &pendingErr{msg, "Waiting for a file"}
	case "ccil_prices":
		// Fetched from CCIL's website when the CCIL prices connection's
		// fetch is on (ccilfetch.go); otherwise, or when that fails, the
		// file is loaded by hand.
		note := ""
		if cn, ok := o.ccilConn(ctx, tenant); ok && cn.Values["fetch"] == "On" {
			set, _ := o.Settings(ctx, tenant)
			end := monthEnd(period)
			if !set.IsBusinessDay(end) {
				end = set.PrevBusinessDay(end)
			}
			run, err := o.CCILFetch(ctx, tenant, SystemActor, end, 4)
			if err == nil {
				return fmt.Sprintf("Fetched from CCIL: %d prices of %s (%s)", run.Prices, fmtDay(run.Date), run.File), nil, nil
			}
			note = " (" + err.Error() + ")"
		}
		msg := fmt.Sprintf("CCIL's prices for the end of %s are not loaded%s: download MTMPRICE for the last business day from CCIL and load it on Exchange files", monthLabel(period), note)
		o.raiseWork(ctx, tenant, "periodic", "ccil:"+period, "Load CCIL's G-sec prices for "+monthLabel(period), msg, "", RoleMaker, "xfiles", date)
		return "", nil, &pendingErr{msg, "Waiting for a file"}
	case "dis_master":
		// With CDSL's common upload API on, the week's books go to CDSL by
		// API and CDSL's response marks each one (cdsltxn.go). If the API
		// refuses the file, the file is made for upload by hand as before.
		apiNote := ""
		if x, ok := o.cdxConn(ctx, tenant); ok && x.On(cdslFamUpload) {
			u, err := o.SendDISMasterByAPI(ctx, tenant, SystemActor, period)
			switch {
			case err == nil:
				f := []PeriodicFile{}
				if u.FileID != "" {
					f = append(f, PeriodicFile{ID: u.FileID, Name: u.File})
				}
				return fmt.Sprintf("Sent to CDSL by API: %d DIS book%s, upload %s, acknowledgement %s; CDSL's response marks each book", len(u.Books), plural(len(u.Books)), u.ID, u.Ack), f, nil
			case errors.Is(err, ErrState) && strings.Contains(err.Error(), "no DIS books issued"):
				return "No DIS books issued in the week", nil, nil
			default:
				apiNote = " CDSL's upload API refused it (" + err.Error() + "), so upload the file on CDSL's system."
			}
		}
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
		o.raiseWork(ctx, tenant, "periodic", "dismaster:"+period, "Upload "+name+" to CDSL", "The DIS master upload for books issued in the week to "+fmtDay(period)+" is on Periodic work."+apiNote, "", RoleMaker, "periodic", date)
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
	o.clearRetiredWork(ctx, tenant)
	o.flagNominationsOverLimit(ctx, tenant, date)
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

// Upcoming is the one compliance calendar: Periodic work and the firm's
// duties (Circulars & calendar), each once, with the next due date, for
// the coming days and anything overdue. Done items are left out.
func (o *Ops) Upcoming(ctx context.Context, tenant, date string, days int) ([]CalendarItem, error) {
	var out []CalendarItem
	until := addDays(date, days)
	ps, err := o.Periodic(ctx, tenant, date)
	if err != nil {
		return nil, err
	}
	for _, p := range ps {
		if p.DueOn == "" || p.DueOn > until || p.Status == "Done" || p.Status == "Nothing to do" {
			continue
		}
		out = append(out, CalendarItem{Date: p.DueOn, Obligation: p.Name + " · " + p.Label, Owner: p.Area, Status: p.Status, View: "periodic"})
	}
	ds, err := o.Duties(ctx, tenant, date)
	if err != nil {
		return nil, err
	}
	for _, d := range ds {
		if !d.Active || d.Task != "" || d.NextDue == "" || d.NextDue > until {
			continue
		}
		st := "Due"
		if d.Overdue {
			st = "Overdue"
		}
		out = append(out, CalendarItem{Date: d.NextDue, Obligation: d.Title, Owner: d.Role, Status: st, View: "circulars"})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Date < out[j].Date })
	return out, nil
}
