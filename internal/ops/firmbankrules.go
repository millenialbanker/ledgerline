package ops

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// ---- rules on the firm's bank accounts ----
//
// What the register (firmbanks.go) is checked against:
//
//   - Names. Client bank accounts carry SEBI's names: "<name of the stock
//     broker or clearing member> - USCNB account", "<name> - DSCNB
//     account" and "<name of the stock broker> - Settlement Account"
//     (SEBI master circular SEBI/HO/MIRSD/MIRSD-PoD/P/CIR/2025/90 of 17
//     June 2025, paras 15.3.1-15.3.2, kept by SEBI HO/38/11/(1)2026-
//     MIRSD-POD/I/7656/2026 of 23 March 2026, forwarded by NSE/INSP/73436).
//     No name is prescribed for the own account, so it is not checked.
//   - Reporting. Every new bank account of the stock broking business is
//     reported to each exchange within 7 working days of opening, and the
//     closure of a reported account within 7 working days of closing
//     (para 15.4.3 as amended; NSE/INSP/73976 item 8.2). Demat accounts
//     are no longer reported by the broker.
//   - The bank undertaking addendum of a new client bank account, within 7
//     working days (NSE/INSP/77149, circular 52/2026, Annexure A). The
//     circular's text was read through the gap report; Ledgerline applies
//     the 7 days to USCNBA, DSCNBA and settlement accounts.
//   - SEBI Check. Every account whose number or QR is given to investors
//     for payment is linked to a validated @valid UPI ID (NSE/COMP/75700,
//     circular 73/2026, Annexure B). A USCNBA is always such an account.
//
// Seven working days are counted on Ledgerline's business-day calendar
// from the day after the account was opened or closed.

// bankReportDays is the working days to report an account's opening or
// closure, or to submit its bank undertaking.
const bankReportDays = 7

// bankNameSuffix is the prescribed end of each client account kind's name.
var bankNameSuffix = map[string]string{
	BankUSCNBA:     "USCNB ACCOUNT",
	BankDSCNBA:     "DSCNB ACCOUNT",
	BankSettlement: "SETTLEMENT ACCOUNT",
}

var reNameSep = regexp.MustCompile(`[^A-Z0-9]+`)

// normBankName upper-cases a name and turns every run of other characters
// (dashes of any kind, dots, slashes) into one space; "A/C" reads as
// "ACCOUNT", "PVT" as "PRIVATE" and "LTD" as "LIMITED".
func normBankName(s string) string {
	s = " " + strings.TrimSpace(reNameSep.ReplaceAllString(strings.ToUpper(s), " ")) + " "
	for _, r := range [][2]string{{" A C ", " ACCOUNT "}, {" PVT ", " PRIVATE "}, {" LTD ", " LIMITED "}} {
		s = strings.ReplaceAll(s, r[0], r[1])
	}
	return strings.TrimSpace(s)
}

// bankNameProblem says what is wrong with a client account's name ("" when
// it follows the nomenclature, or none is prescribed for the kind).
func bankNameProblem(set Settings, b FirmBank) string {
	why, _ := bankNameCheck(set, b)
	return why
}

// bankNameCheck is bankNameProblem, with hard set when the name does not
// end as prescribed (a save is refused); a name that does not carry the
// firm's name as Settings spell it is only warned about, since banks
// abbreviate.
func bankNameCheck(set Settings, b FirmBank) (why string, hard bool) {
	suf, ok := bankNameSuffix[b.Kind]
	if !ok {
		return "", false
	}
	want := fmt.Sprintf("<firm's name> - %s", map[string]string{BankUSCNBA: "USCNB account", BankDSCNBA: "DSCNB account", BankSettlement: "Settlement Account"}[b.Kind])
	n := normBankName(b.Name)
	if n == "" {
		return fmt.Sprintf("%s: give the account's name as the bank has it; it must be %q (SEBI master circular 2025/90, para 15.3)", b.Label(), want), false
	}
	if !strings.HasSuffix(n, " "+suf) {
		return fmt.Sprintf("%s: the name %q does not follow SEBI's nomenclature %q (master circular 2025/90, para 15.3): ask the bank to rename it", b.Label(), b.Name, want), true
	}
	if f := normBankName(set.Firm); f != "" && !strings.Contains(" "+n+" ", " "+f+" ") {
		return fmt.Sprintf("%s: the name %q does not carry the firm's name %q (master circular 2025/90, para 15.3): ask the bank to rename it, or correct the firm's name in Settings", b.Label(), b.Name, set.Firm), false
	}
	return "", false
}

// firmSettings is the tenant's settings, zero when they cannot be read.
func (o *Ops) firmSettings(ctx context.Context, tenant string) Settings {
	s, _ := o.Settings(ctx, tenant)
	return s
}

// reportingExchanges are the exchanges the firm is a member of, each of
// which is told of the firm's bank accounts.
func (o *Ops) reportingExchanges(ctx context.Context, tenant string) []string {
	set := o.firmSettings(ctx, tenant)
	var out []string
	for _, c := range ClearingSegments {
		if set.tradesOn(c.Exchange) && !contains(out, c.Exchange) {
			out = append(out, c.Exchange)
		}
	}
	return out
}

// cleanDateMap drops empty dates and upper-cases the exchanges.
func cleanDateMap(m map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range m {
		if k = strings.ToUpper(strings.TrimSpace(k)); k != "" && strings.TrimSpace(v) != "" {
			out[k] = strings.TrimSpace(v)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// BankReportItem is one thing to tell an exchange about an account, or the
// bank undertaking to submit, with its due date.
type BankReportItem struct {
	Account  string `json:"account"` // FB-01
	Label    string `json:"label"`
	What     string `json:"what"` // opening, closure, undertaking
	Exchange string `json:"exchange,omitempty"`
	From     string `json:"from"` // the day the clock starts
	Due      string `json:"due"`
	Done     string `json:"done,omitempty"`
	Late     bool   `json:"late,omitempty"`
}

// bankReportItems lists every report an account needs: its opening and,
// once closed, its closure to each exchange, and the undertaking of a
// client bank account. An account ticked Reported before the dates were
// kept counts its opening as reported.
func bankReportItems(set Settings, exs []string, b FirmBank) []BankReportItem {
	if b.Kind == BankCC {
		return nil
	}
	var out []BankReportItem
	add := func(what, exch, from, done string) {
		it := BankReportItem{Account: b.ID, Label: b.Label(), What: what, Exchange: exch, From: from, Due: addWorkingDays(set, from, bankReportDays), Done: done}
		it.Late = (done != "" && done > it.Due)
		out = append(out, it)
	}
	if b.OpenedOn != "" {
		for _, x := range exs {
			done := b.ReportedOn[x]
			if done == "" && b.Reported && len(b.ReportedOn) == 0 {
				done = b.OpenedOn // ticked before the dates were kept
			}
			add("opening", x, b.OpenedOn, done)
		}
		if clientMoneyKind(b.Kind) {
			add("undertaking", "", b.OpenedOn, b.UndertakingOn)
		}
	}
	if b.ClosedOn != "" && (b.Reported || len(b.ReportedOn) > 0) {
		for _, x := range exs {
			add("closure", x, b.ClosedOn, b.ClosureReportedOn[x])
		}
	}
	return out
}

// bankReportTitle names a report item for people.
func (it BankReportItem) title() string {
	switch it.What {
	case "undertaking":
		return fmt.Sprintf("Submit the bank undertaking addendum for %s", it.Label)
	case "closure":
		return fmt.Sprintf("Report the closure of %s to %s", it.Label, it.Exchange)
	}
	return fmt.Sprintf("Report %s to %s", it.Label, it.Exchange)
}

// upiLinked says whether an account is linked to one of the firm's
// validated client-money UPI IDs: its own UPIID, or a handle saved in
// Settings → UPI collection for its account number.
func upiLinked(b FirmBank, cfg UPICollect) bool {
	if b.UPIID != "" {
		if h, ok := cfg.handle(b.UPIID); ok && h.Purpose == UPIFirmFee {
			return false
		}
		return reValidUPI.MatchString(b.UPIID)
	}
	for _, h := range cfg.Handles {
		h, _ = cfg.handle(h.VPA)
		if h.Purpose != UPIFirmFee && h.Account != "" && normAcctNo(h.Account) == normAcctNo(b.Account) {
			return true
		}
	}
	return false
}

// investorFacing says whether clients are given the account to pay into.
func (b FirmBank) investorFacing() bool { return b.Kind == BankUSCNBA || b.InvestorFacing }

// firmBankProblems are what the register's rules find on date: names,
// reporting and undertakings due or late, investor-facing accounts with
// no @valid UPI ID, and client accounts marked as overdraft.
func (o *Ops) firmBankProblems(ctx context.Context, tenant string, set Settings, bs []FirmBank, date string) []string {
	var out []string
	exs := o.reportingExchanges(ctx, tenant)
	cfg := o.UPICollectConfig(ctx, tenant)
	for _, b := range bs {
		if b.Active {
			if why := bankNameProblem(set, b); why != "" {
				out = append(out, why)
			}
			if b.OpenedOn == "" && b.Kind != BankCC {
				out = append(out, fmt.Sprintf("%s: give the day it was opened, so the 7 working days to report it to the exchanges can be checked.", b.Label()))
			}
			if b.investorFacing() && !upiLinked(b, cfg) {
				out = append(out, fmt.Sprintf("%s is given to clients for payments but has no validated @valid UPI ID linked: link one with the bank and record it, so clients can verify the account through SEBI Check (NSE/COMP/75700).", b.Label()))
			}
		}
		if b.Overdraft && clientMoneyKind(b.Kind) {
			out = append(out, fmt.Sprintf("%s is marked as an overdraft or loan account but holds client money.", b.Label()))
		}
		for _, it := range bankReportItems(set, exs, b) {
			switch {
			case it.Done == "" && date > it.Due:
				out = append(out, fmt.Sprintf("%s: due by %s, not recorded.", it.title(), fmtDay(it.Due)))
			case it.Done == "":
				out = append(out, fmt.Sprintf("%s by %s.", it.title(), fmtDay(it.Due)))
			}
		}
	}
	sort.Strings(out)
	return out
}

// flagNoValidUPI opens a work item for the compliance officer when a
// client pays by bank transfer (NEFT, IMPS, RTGS) into one of the firm's
// investor-facing accounts that has no validated @valid UPI ID linked:
// investor money is accepted only into accounts investors can verify
// through SEBI Check (NSE/COMP/75700, circular 73/2026, Annexure B FAQ).
// One item per account and day. Without a register the account cannot be
// told, and nothing is raised.
func (o *Ops) flagNoValidUPI(ctx context.Context, tenant string, t BankTxn) {
	if t.PayeeVPA != "" || t.Amount <= 0 {
		return
	}
	reg := o.firmBanks(ctx, tenant)
	if len(reg) == 0 {
		return
	}
	var b FirmBank
	ok := false
	if t.ToAccount != "" {
		b, ok = firmBankByAccount(reg, t.ToAccount)
	} else {
		b, ok = defaultFirmBank(reg, BankUSCNBA, "")
	}
	if !ok || !b.investorFacing() || upiLinked(b, o.UPICollectConfig(ctx, tenant)) {
		return
	}
	o.raiseWork(ctx, tenant, "bank-novalid", b.ID+"|"+t.Date, "Client money received in "+b.Label()+", which has no @valid UPI ID",
		fmt.Sprintf("₹%s came in by %s (reference %s) to %s. Every account clients pay into must be linked to a validated @valid UPI ID so they can verify it through SEBI Check (NSE/COMP/75700). Link one with the bank and record it on Client money reconciliation → Firm bank accounts, or tell clients to pay into an account that has one.", formatINR(t.Amount.Dec()), firstOf(t.Mode, "bank transfer"), t.Ref, b.Label()),
		"", RoleCompliance, "clientmoney", t.Date)
}

// autoClientMoney queues the day's client money duties: reports of the
// firm's bank accounts to the exchanges and bank undertakings not yet
// recorded, investor-facing accounts with no @valid UPI ID, and cheques
// issued to clients that are due to be upstreamed (payoutcheque.go). An
// item closes by itself once it is no longer found.
func autoClientMoney(r *runCtx) error {
	set, err := r.o.Settings(r.ctx, r.tenant)
	if err != nil {
		return err
	}
	bs := r.o.firmBanks(r.ctx, r.tenant)
	exs := r.o.reportingExchanges(r.ctx, r.tenant)
	cfg := r.o.UPICollectConfig(r.ctx, r.tenant)
	n := 0
	for _, b := range bs {
		for _, it := range bankReportItems(set, exs, b) {
			if it.Done != "" {
				continue
			}
			n++
			verb := "opened"
			if it.What == "closure" {
				verb = "closed"
			}
			detail := fmt.Sprintf("The account was %s on %s; it is reported to %s within %d working days, by %s (SEBI master circular 2025/90 para 15.4.3, as amended in March 2026; NSE/INSP/73976 item 8.2). Report it on the exchange's portal, then record the day on Client money reconciliation → Firm bank accounts.",
				verb, fmtDay(it.From), it.Exchange, bankReportDays, fmtDay(it.Due))
			if it.What == "undertaking" {
				detail = fmt.Sprintf("The account was opened on %s; the bank's undertaking addendum (the exchange's authority to obtain its statements) is submitted within %d working days, by %s (NSE/INSP/77149). Record the day it was submitted on Client money reconciliation → Firm bank accounts.", fmtDay(it.From), bankReportDays, fmtDay(it.Due))
			}
			r.queue("bank-report|"+it.Account+"|"+it.What+"|"+it.Exchange, it.title(), detail, "", RoleCompliance, "clientmoney", it.Due+" 17:00", nil)
		}
		if b.Active && b.investorFacing() && !upiLinked(b, cfg) {
			n++
			r.queue("bank-upi|"+b.ID, b.Label()+" has no @valid UPI ID", "Clients are given this account to pay into. Every such account is linked to a validated @valid UPI ID so clients can verify it through SEBI Check (NSE/COMP/75700). Ask the bank to link one and record it on the account.", "", RoleCompliance, "clientmoney", "", nil)
		}
	}
	for _, q := range r.o.payoutCheques(r.ctx, r.tenant) {
		if !q.outstandingOn(r.date) {
			continue
		}
		days := businessDaysBetween(set, q.IssuedOn, r.date)
		if days < retentionSettleDays {
			continue
		}
		n++
		sixth := q.IssuedOn
		for i := 0; i <= retentionSettleDays; i++ {
			sixth = set.NextBusinessDay(sixth)
		}
		r.queue("cheque6|"+q.ID, fmt.Sprintf("Cheque %s to %s not cleared: upstream its funds by %s", q.ChequeNo, q.UCC, fmtDay(sixth)),
			fmt.Sprintf("Cheque %s for ₹%s was issued on %s and has not cleared. Funds kept for a cheque not cleared within five settlement days are upstreamed to the clearing corporation on the sixth settlement day (NCL/CMPL/61186, instruction 7). Ask the bank to stop it and cancel it on Receipts & payouts: the payout is reversed to the client's ledger and goes up with the day's allocation. If it has cleared, record the day.", q.ChequeNo, formatINR(q.Amount.Dec()), fmtDay(q.IssuedOn)),
			q.UCC, RoleChecker, "money", sixth+" 17:00", nil)
	}
	r.note("%d client money items", n)
	return nil
}
