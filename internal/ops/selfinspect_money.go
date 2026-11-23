package ops

import (
	"fmt"
	"sort"
	"strings"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// ---- self-inspection tests of client money and the firm's bank accounts ----
//
// From the compliance gap report of November 2026 (workstream D): the
// firm's bank accounts named, reported and verifiable; client money
// upstreamed every day with only the stipulated reasons for keeping any;
// client-fund FDRs within SEBI's conditions; journals never used to pay a
// client or move money between clients. Sources are on each test.

func init() {
	siTests = append(siTests, siMoneyTests...)
}

var siMoneyTests = []SITest{
	{ID: "firm_bank_register", Title: "Firm bank accounts named, reported to each exchange and verifiable by clients", Area: "Money", Severity: "high", Owner: RoleCompliance,
		Source:    "SEBI master circular SEBI/HO/MIRSD/MIRSD-PoD/P/CIR/2025/90, paras 15.3.1-15.3.2 (names) and 15.4.3 as amended by SEBI HO/38/11/(1)2026-MIRSD-POD/I/7656/2026 of 23 Mar 2026 (NSE/INSP/73436: new and closed bank accounts reported within 7 working days); NSE/INSP/73976 item 8.2; NSE/INSP/77149 (52/2026), Annexure A (bank undertaking addendum within 7 working days); NSE/COMP/75700 (73/2026), Annexure B (accounts given to investors linked to an @valid UPI ID)",
		ToConfirm: "The undertaking's 7 working days are applied to the USCNBA, DSCNBA and settlement accounts; the gap report's reading of NSE/INSP/77149 was not checked against its full text.",
		Checks:    "Every active account in the register: the USCNBA, DSCNBA and settlement accounts named '<firm> - USCNB account', '- DSCNB account', '- Settlement Account'; the day it was opened; its opening (and, once closed, its closure) recorded as reported to each exchange within 7 working days; the bank undertaking of each client account within 7 working days; every account clients pay into linked to a validated @valid UPI ID; no client account marked as an overdraft or loan account.",
		Fix:       "Record the missing dates and the UPI ID on Client money reconciliation → Firm bank accounts after reporting on the exchanges' portals; ask the bank to rename an account or link an @valid UPI ID.",
		Screen:    "clientmoney", ScreenName: "Receipts & payouts → Client money reconciliation", Real: true, run: siFirmBankRegister},
	{ID: "client_money_nil", Title: "Client money upstreamed every day; kept back only for the stipulated reasons", Area: "Money", Severity: "high", Owner: RoleChecker,
		Source: "SEBI/HO/MIRSD/MIRSD-PoD-1/P/CIR/2023/84 of 8 Jun 2023, para 3 (NSE/INSP/57041: no client funds kept overnight); NSE Clearing NCL/CMPL/61186 (03/2024), Annexure 1 (reason codes RC01-RC09) and NCL/CMPL/76318 (27/2026), FAQ 5; NSE/INSP/73976, Exhibit 29 paras 8-12",
		Checks: "The last five business days: the USCNBA and settlement accounts end the day with no more than the stipulated reasons recorded for it explain (money received after the cut-off, FDRs or MFOS in transit, OFS refunds, a bank's failure), the DSCNBA with no more than payouts with the bank, uncleared cheques and settlement-day funds explain, and nothing is reported in SEGMON under RC09.",
		Fix:    "Upstream what is left in the client accounts, or record the reason with its evidence on Client money reconciliation → Retained funds; set the firm's cut-off time there.",
		Screen: "clientmoney", ScreenName: "Receipts & payouts → Client money reconciliation", Real: true, run: siClientMoneyNil},
	{ID: "client_fdrs", Title: "Client-fund FDRs made only from the USCNBA, within SEBI's conditions", Area: "Money", Severity: "high", Owner: RoleChecker,
		Source: "SEBI master circular SEBI/HO/MIRSD/MIRSD-PoD/P/CIR/2025/90, paras 97.5-97.6 (from SEBI/HO/MIRSD/MIRSD-PoD-1/P/CIR/2023/84, para 3.A); NCL/CMPL/61186, RC02 (an FDR in transit deposited within the next five settlement days)",
		Checks: "Every active fixed deposit marked as made from client money: funded from a USCNBA, matures within one year and one day, principal protected and pre-terminable, lien-marked to the clearing corporation, not used for any bank facility, and deposited with the clearing corporation within five settlement days of being made.",
		Fix:    "Correct the deposit's record on Risk → Collateral → Clearing deposits, or break the FDR and upstream the money as cash.",
		Screen: "deposits", ScreenName: "Risk → Collateral → Clearing deposits", Real: true, run: siClientFDRs},
	{ID: "client_journals", Title: "No journal used to pay a client or move money between clients", Area: "Money", Severity: "high", Owner: RoleChecker,
		Source: "SEBI master circular SEBI/HO/MIRSD/MIRSD-PoD/P/CIR/2025/90, para 23.1.1(j) (no inter-client adjustments) and para 48.3 (settled only by payment to the client's bank account; journal entries only to levy or reverse charges); NSE/INSP/53820, note d",
		Checks: "Journal vouchers posted in the last 90 days on a client's ledger: none moves money to another client's ledger, and none is set against a bank, cash, suspense, clearing or other party's account instead of income, expense or statutory dues.",
		Fix:    "Cancel the voucher (it is reversed) and pay the client through Receipts & payouts; record any charge by its own voucher.",
		Screen: "books", ScreenName: "Accounts → Firm accounts & GST", Real: true, run: siClientJournals},
}

// siFirmBankRegister: the register's rules (firmbankrules.go).
func siFirmBankRegister(e *siEnv) (siOut, error) {
	bs := e.o.firmBanks(e.ctx, e.tenant)
	if len(bs) == 0 {
		return siSkip("Waiting for data: the firm's bank accounts are not named yet (Client money reconciliation → Firm bank accounts).")
	}
	var s siOut
	s.input("Firm bank accounts", e.date)
	exs := e.o.reportingExchanges(e.ctx, e.tenant)
	cfg := e.o.UPICollectConfig(e.ctx, e.tenant)
	for _, b := range bs {
		if b.Kind == BankCC {
			continue
		}
		s.checked++
		if b.Active {
			if why := bankNameProblem(e.set, b); why != "" {
				s.bad(why)
			}
			if b.OpenedOn == "" {
				s.bad(b.Label() + ": the day it was opened is not recorded")
			}
			if b.investorFacing() && !upiLinked(b, cfg) {
				s.bad(b.Label() + ": clients pay into it, and it has no @valid UPI ID linked")
			}
		}
		if b.Overdraft && clientMoneyKind(b.Kind) {
			s.bad(b.Label() + ": marked as an overdraft or loan account, but it holds client money")
		}
		for _, it := range bankReportItems(e.set, exs, b) {
			switch {
			case it.Done == "" && e.date > it.Due:
				s.bad(fmt.Sprintf("%s: due by %s, not recorded", it.title(), fmtDay(it.Due)))
			case it.Late:
				s.bad(fmt.Sprintf("%s: done on %s, due by %s", it.title(), fmtDay(it.Done), fmtDay(it.Due)))
			}
		}
	}
	s.summary = fmt.Sprintf("%d bank accounts checked; %d problems.", s.checked, s.found)
	return s, nil
}

// siClientMoneyNil: each of the last five business days, the client money
// accounts end with only what the stipulated reasons explain, and SEGMON
// reports nothing under RC09.
func siClientMoneyNil(e *siEnv) (siOut, error) {
	since := siFirstTrade(e)
	if since == "" {
		return siSkip("Nothing to check: no trades in Ledgerline yet.")
	}
	reg := len(e.o.firmBanks(e.ctx, e.tenant)) > 0
	var s siOut
	d := e.date
	for i := 0; i < 5; i++ {
		d = e.set.PrevBusinessDay(d)
		if d == "" || d < since {
			break
		}
		s.checked++
		s.input("Retained funds by reason", d)
		rr, err := e.o.retentionReport(e.ctx, e.tenant, d, nil, nil)
		if err != nil {
			return s, err
		}
		if x := rr.byCode("RC09"); x.Sign() > 0 {
			s.bad(fmt.Sprintf("%s · ₹%s kept with no stipulated reason (RC09)", fmtDay(d), formatINR(x)))
		}
		if !reg {
			continue
		}
		s.input("Client money reconciliation", d)
		r, err := e.o.buildMoneyRecon(e.ctx, e.tenant, d)
		if err != nil {
			return s, err
		}
		for _, f := range r.Flags {
			if f.Kind == "not_upstreamed" || (f.Kind == "dscnba_balance" && f.Amount > 0 && strings.Contains(f.Text, "not a running account settlement day")) {
				s.bad(fmt.Sprintf("%s · %s", fmtDay(d), f.Text))
			}
		}
	}
	if s.checked == 0 {
		return siSkip("Nothing to check yet: the first business day in Ledgerline is today.", s.inputs...)
	}
	note := ""
	if !reg {
		note = " The firm's bank accounts are not named, so their balances were not checked."
	}
	s.summary = fmt.Sprintf("%d business days checked; %d found.%s", s.checked, s.found, note)
	return s, nil
}

// siClientFDRs: client-fund FDRs against paras 97.5-97.6.
func siClientFDRs(e *siEnv) (siOut, error) {
	ds, err := docs.List[Deposit](e.ctx, e.o.Docs, e.tenant, cDeposit, "")
	if err != nil {
		return siOut{}, err
	}
	reg := e.o.firmBanks(e.ctx, e.tenant)
	var s siOut
	s.input("Clearing deposits", e.date)
	for _, d := range ds {
		if !d.ClientFunds || d.Status != "Active" {
			continue
		}
		s.checked++
		for _, p := range clientFDRProblems(d, reg) {
			s.bad(fmt.Sprintf("%s %s %s: %s", d.ID, d.Issuer, d.Ref, p))
		}
		if d.inTransitOn(e.date) && businessDaysBetween(e.set, d.Issued, e.date) > retentionSettleDays {
			s.bad(fmt.Sprintf("%s %s %s: lien-marked on %s and still not deposited with %s after %d settlement days", d.ID, d.Issuer, d.Ref, fmtDay(d.Issued), d.CC, retentionSettleDays))
		}
	}
	if s.checked == 0 {
		return siSkip("Nothing to check: no fixed deposit of client money is recorded.", s.inputs...)
	}
	s.summary = fmt.Sprintf("%d client-fund FDRs checked; %d problems.", s.checked, s.found)
	return s, nil
}

// siClientJournals: posted journals on client ledgers against the rule
// SaveVoucher now enforces (clientJournalRule), for vouchers posted before
// it or by any other way.
func siClientJournals(e *siEnv) (siOut, error) {
	vs, err := docs.List[Voucher](e.ctx, e.o.Docs, e.tenant, cVoucher, "")
	if err != nil {
		return siOut{}, err
	}
	from := addDays(e.date, -90)
	sort.SliceStable(vs, func(i, j int) bool { return vs[i].No < vs[j].No })
	var s siOut
	s.input("Vouchers", from)
	for _, v := range vs {
		if v.Type != "Journal" || v.Status != "Posted" || v.Date < from || v.Date > e.date {
			continue
		}
		client := false
		for _, l := range v.Lines {
			client = client || strings.HasPrefix(l.Account, "CLIENT")
		}
		if !client {
			continue
		}
		s.checked++
		if err := clientJournalRule(v.Lines); err != nil {
			var amt money.Amount = v.Total
			s.bad(fmt.Sprintf("%s of %s · ₹%s · %s", v.No, fmtDay(v.Date), formatINR(amt.Dec()), strings.TrimPrefix(err.Error(), ErrInvalid.Error()+": ")))
		}
	}
	s.summary = fmt.Sprintf("%d journals on client ledgers in the last 90 days checked; %d not allowed.", s.checked, s.found)
	return s, nil
}
