package ops

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
	"ledgerline/internal/xfile"
)

// ---- the DP's smaller duties ----
//
//   - Nominee to legal heirs: shares a nominee received by transmission
//     and passes on to the legal heirs go to CDSL with reason TLH, so
//     CBDT does not treat them as a transfer (SEBI/HO/MIRSD/MIRSD-PoD/P/
//     CIR/2025/130). A slip from an account that received a transmission
//     takes reason "Transmission" unless staff chose another; the upload
//     sends it as TLH (cdsltxn.go).
//   - Set-up of off-market slips: the date of receipt is kept; a slip is
//     set up the same or the next day, and one set up more than 2 days
//     after receipt needs the reason (CDSL communiqués DP2025-543 and
//     DP2025-641, bye-law 7.2.1). The reason may be added after entry.
//   - "To be closed" accounts: CDSL's periodic DP-wise list of accounts
//     holding only illiquid, suspended or delisted securities (communiqué
//     of 30 Sep 2025, CDSL/OPS/DP/POLCY/2026/792): no maintenance charge
//     on them while they are on the latest list.
//   - Stamp duty not funded: CDSL marks an off-market or invocation
//     transaction "Overdue - Insufficient Stamp Duty" and cancels it at
//     end of day (communiqués DP2025-720, 731, DP2026-116, 169); the
//     transaction export's status gives it, and a work item asks to fund
//     the virtual account at once.
//   - The Rights and Obligations document to each new account holder, and
//     the DP-attested client master list on request, each logged.
//   - Pass-through charges: what clients paid for CDSL's charges against
//     CDSL's bill to the DP (true to label, SEBI master circular for
//     depositories, para 4.86.6).

// receivedByTransmission says whether securities came to the client's
// account by a completed transmission request.
func (o *Ops) receivedByTransmission(ctx context.Context, tenant, ucc string) bool {
	rs, _ := docs.List[DPRequest](ctx, o.Docs, tenant, cDPReq, "")
	for _, r := range rs {
		if r.Kind == "Transmission" && r.Status == "Completed" && strings.EqualFold(r.Detail["to_ucc"], ucc) {
			return true
		}
	}
	return false
}

// dpDISSetup dates a slip's set-up and counts the days since receipt.
func dpDISSetup(d *DIS, today string) {
	d.SetupOn = today
	d.DelayDays = 0
	if validDate(d.Received) && d.Received < today {
		d.DelayDays = daysBetween(d.Received, today)
	}
	d.DelayReason = clip(strings.TrimSpace(d.DelayReason), 300)
	d.StampEst = 0
	if d.Consideration > 0 {
		d.StampEst = d.Consideration.Dec().Pct(money.MustDec("0.015")).Round(money.UnitPaisa)
	}
}

// disDelayWork asks for the reason of a slip set up more than 2 days late.
func (o *Ops) disDelayWork(ctx context.Context, tenant string, d DIS) {
	if d.DelayDays > 2 && d.DelayReason == "" && d.Kind != "On-market" {
		o.raiseWork(ctx, tenant, "dis-delay", d.ID, fmt.Sprintf("Reason for setting up slip %d %d days after receipt", d.Slip, d.DelayDays),
			fmt.Sprintf("Slip %d of %s was received on %s and set up on %s. CDSL asks for set-up the same or the next day, and a justified reason when it is more than 2 days (communiqués DP2025-543, DP2025-641). Record the reason on the slip (Depository → Demat accounts & DIS).", d.Slip, d.UCC, fmtDay(d.Received), fmtDay(d.SetupOn)),
			d.UCC, RoleChecker, "dp", d.SetupOn)
	}
}

// RecordDISDelayReason records why a slip was set up late.
func (o *Ops) RecordDISDelayReason(ctx context.Context, tenant, actor, id, reason string) (DIS, error) {
	if err := o.require(ctx, tenant, actor, "Entering delivery instructions", RoleMaker, RoleChecker, RoleBranch); err != nil {
		return DIS{}, err
	}
	reason = strings.TrimSpace(reason)
	if len(reason) < 5 {
		return DIS{}, invalid("give the reason the slip was set up late")
	}
	d, err := docs.Update(ctx, o.Docs, tenant, cDIS, id, actor, func(x *DIS) error {
		x.DelayReason = clip(reason, 300)
		return nil
	})
	if err == nil {
		o.closeWork(ctx, tenant, actor, "dis-delay", id, "reason recorded")
		o.log(ctx, tenant, actor, "dis.delay_reason", id, map[string]any{"reason": reason})
	}
	return d, err
}

// ---- CDSL's "to be closed" list ----

const cDPTBC = "dptbc" // "list" -> DPToBeClosed

// DPToBeClosed is CDSL's latest list.
type DPToBeClosed struct {
	ListDate string   `json:"list_date"`
	BOIDs    []string `json:"bo_ids"`
	File     string   `json:"file"`
	By       string   `json:"by"`
	At       string   `json:"at"`
}

var reBO16 = regexp.MustCompile(`(^|[^0-9])([0-9]{16})($|[^0-9])`)

// LoadToBeClosed loads CDSL's list: every 16-digit BO ID in the file
// (whatever its layout: text, CSV or CDSL's report) that is one of the
// DP's accounts. The list replaces the one before.
func (o *Ops) LoadToBeClosed(ctx context.Context, tenant, actor, listDate, file string, data []byte) (DPToBeClosed, error) {
	if err := o.require(ctx, tenant, actor, "DP billing", RoleChecker); err != nil {
		return DPToBeClosed{}, err
	}
	if !validDate(listDate) {
		return DPToBeClosed{}, invalid("give the list's date (YYYY-MM-DD)")
	}
	mine := map[string]bool{}
	for bo := range o.boClients(ctx, tenant) {
		mine[normDematID(bo)] = true
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		for _, m := range reBO16.FindAllStringSubmatch(line, -1) {
			if mine[m[2]] {
				seen[m[2]] = true
			}
		}
	}
	l := DPToBeClosed{ListDate: listDate, BOIDs: sortedKeys(seen), File: clip(file, 120), By: actor, At: o.now().Format(time.RFC3339)}
	if l.BOIDs == nil {
		l.BOIDs = []string{}
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cDPTBC, "list", l, actor); err != nil {
		return l, err
	}
	o.log(ctx, tenant, actor, "dp.to_be_closed_loaded", listDate, map[string]any{"accounts": len(l.BOIDs)})
	return l, nil
}

func (o *Ops) dpToBeClosed(ctx context.Context, tenant string) map[string]bool {
	l, _, _ := docs.GetOr(ctx, o.Docs, tenant, cDPTBC, "list", DPToBeClosed{})
	out := map[string]bool{}
	for _, b := range l.BOIDs {
		out[b] = true
	}
	return out
}

// ---- stamp duty not funded ----

// cdslStampOverdue raises a work item for a transaction CDSL marks
// overdue for insufficient stamp duty (the code the firm recorded in Firm
// practices; without it, a status text saying so).
func (o *Ops) cdslStampOverdue(ctx context.Context, tenant string, r xfile.Row, ucc string) {
	st := strings.TrimSpace(r.Get("OrdSttsTo"))
	if st == "" {
		return
	}
	opt, vals := o.tcPractice(ctx, tenant, "cdsl-stamp-status")
	code := strings.TrimSpace(vals["code"])
	hit := (opt == "code" && code != "" && strings.EqualFold(st, code)) || strings.Contains(strings.ToUpper(st), "INSUFFICIENT STAMP")
	if !hit {
		return
	}
	id := strings.TrimSpace(r.Get("TxnID"))
	o.raiseWork(ctx, tenant, "cdsl-stamp", id, "Fund stamp duty today: CDSL transaction "+id,
		fmt.Sprintf("CDSL shows transaction %s (BO %s, ISIN %s) as Overdue - Insufficient Stamp Duty. Fund the stamp duty in the DP's (or the client's) virtual account before CDSL's end of day, or the transaction is cancelled, and tell the client.", id, cdslMaskBO(r.Get("ClntId")), r.Get("ISIN")),
		ucc, RoleChecker, "cdsltxn", o.now().Format("2006-01-02"))
}

// ---- Rights and Obligations, attested CML ----

// SendROKit e-mails a client the link to the DP's Rights and Obligations
// document and logs the delivery.
func (o *Ops) SendROKit(ctx context.Context, tenant, actor, ucc string) (CompRecord, error) {
	if err := o.require(ctx, tenant, actor, "Recording rights and obligations", RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return CompRecord{}, err
	}
	_, vals := o.tcPractice(ctx, tenant, "ro-kit")
	url := strings.TrimSpace(vals["url"])
	if url == "" {
		return CompRecord{}, invalid("enter the link to the Rights and Obligations document on the firm's website first (Settings → Firm practices → Rights and Obligations to demat clients)")
	}
	today := o.now().Format("2006-01-02")
	d, err := o.sendLetter(ctx, tenant, actor, today, strings.ToUpper(ucc), "RO-"+strings.ToUpper(ucc), "Rights and Obligations of a demat account holder", []string{
		"Thank you for opening your demat account with us. The document setting out your rights and obligations and ours as your depository participant is on our website: " + url + ".",
		"Please read it and keep it. If you would like a printed copy, write to us and we will send one.",
	})
	if err != nil {
		return CompRecord{}, err
	}
	k, _ := compKind("ro-kit")
	return o.addCompRecord(ctx, tenant, actor, k, map[string]string{"ucc": ucc, "mode": "Electronic", "delivered_on": today, "ref": d.ID}, "")
}

// IssueAttestedCML gives a client a signed copy of the client master list
// of one demat account, from CDSL's client master as last loaded (it must
// be not older than two months), and logs it.
func (o *Ops) IssueAttestedCML(ctx context.Context, tenant, actor, ucc, boid, purpose string) (CompRecord, error) {
	if err := o.require(ctx, tenant, actor, "Recording client master lists", RoleChecker, RoleCompliance); err != nil {
		return CompRecord{}, err
	}
	c, err := o.Client(ctx, tenant, strings.ToUpper(ucc))
	if err != nil {
		return CompRecord{}, err
	}
	b := o.boFor(ctx, tenant, c, boid)
	if b.BOID == "" {
		return CompRecord{}, invalid("%s has no demat account with the DP", c.UCC)
	}
	today := o.now().Format("2006-01-02")
	asOf := ""
	if b.CDSL != nil && len(b.CDSL.At) >= 10 {
		asOf = b.CDSL.At[:10]
	}
	if asOf == "" || asOf < addMonths(today, -2) {
		return CompRecord{}, invalid("CDSL's client master for %s was last loaded %s: load a fresh one (not older than two months) first", cdslMaskBO(b.BOID), firstOf(fmtDay(asOf), "never"))
	}
	set, _ := o.Settings(ctx, tenant)
	lines := []string{
		fmt.Sprintf("Client master list of demat account %s (DP ID %s) as at %s, from the depository's records.", b.BOID, b.BOID[:8], fmtDay(asOf)),
		"First holder: " + c.Name + " · PAN " + MaskPAN(c.PAN),
	}
	for i, j := range b.JointHolders {
		lines = append(lines, fmt.Sprintf("Holder %d: %s", i+2, j.Name))
	}
	if b.CDSL != nil {
		lines = append(lines, fmt.Sprintf("Account status: %s · category %s · nominees %d", firstOf(b.CDSL.StatusText, b.CDSL.Status), firstOf(b.CDSL.Category, "—"), b.CDSL.Nominees))
	}
	lines = append(lines, "Correspondence: "+firstOf(c.City, "as registered")+" · mobile ending "+lastN(c.Mobile, 4)+" · e-mail "+maskEmail(c.Email),
		"Bank: "+firstOf(c.Bank.Bank, "—")+" · account ending "+lastN(c.Bank.Account, 4),
		fmt.Sprintf("Attested by %s as depository participant on %s, for: %s.", firstOf(set.Firm, "the DP"), fmtDay(today), purpose))
	d, err := o.sendLetter(ctx, tenant, actor, today, c.UCC, "CML-"+b.BOID+"-"+today, "Client master list (attested by the DP)", lines)
	if err != nil {
		return CompRecord{}, err
	}
	k, _ := compKind("cml")
	return o.addCompRecord(ctx, tenant, actor, k, map[string]string{"ucc": c.UCC, "bo_id": b.BOID, "purpose": purpose, "cml_date": asOf, "issued_on": today, "ref": d.ID}, "")
}

func lastN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// ---- pass-through charges ----

// PassThroughRow is one month's CDSL charges recovered against CDSL's
// bill.
type PassThroughRow struct {
	Month       string       `json:"month"`
	Head        string       `json:"head"`
	Recovered   money.Amount `json:"recovered"` // from clients' DP bills
	Billed      money.Amount `json:"billed"`    // CDSL's bill, as entered
	Entered     bool         `json:"entered"`
	OverByMoney money.Amount `json:"over"` // recovered beyond what CDSL billed
}

// dpPassThrough compares, by month, the depository's share of the
// transaction charges and the stamp duty on clients' DP bills with the
// heads of CDSL's bill entered in the register.
func (o *Ops) dpPassThrough(ctx context.Context, tenant string) []PassThroughRow {
	bills, _ := docs.List[DPBill](ctx, o.Docs, tenant, cDPBill, "")
	type k2 struct{ m, h string }
	rec := map[k2]money.Amount{}
	for _, b := range bills {
		for _, l := range b.Lines {
			switch {
			case l.Stamp:
				rec[k2{b.Month, "Stamp duty"}] += l.Amount
			case l.Share > 0:
				rec[k2{b.Month, "Transaction charges"}] += l.Share
			}
		}
	}
	billed := map[k2]money.Amount{}
	entered := map[k2]bool{}
	for _, r := range o.compRecs(ctx, tenant, "cdsl-bill") {
		a, _ := money.ParseAmount(r.V("amount"))
		k := k2{r.V("month"), r.V("head")}
		billed[k] += a
		entered[k] = true
	}
	keys := map[k2]bool{}
	for k := range rec {
		keys[k] = true
	}
	for k := range billed {
		keys[k] = true
	}
	var out []PassThroughRow
	for k := range keys {
		r := PassThroughRow{Month: k.m, Head: k.h, Recovered: rec[k], Billed: billed[k], Entered: entered[k]}
		if r.Entered && r.Recovered > r.Billed {
			r.OverByMoney = r.Recovered - r.Billed
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Month != out[j].Month {
			return out[i].Month > out[j].Month
		}
		return out[i].Head < out[j].Head
	})
	return out
}

// DPPassThrough is the comparison for the screen.
func (o *Ops) DPPassThrough(ctx context.Context, tenant, actor string) ([]PassThroughRow, error) {
	if err := o.require(ctx, tenant, actor, "Viewing compliance registers", RoleMaker, RoleChecker, RoleRisk, RoleCompliance); err != nil {
		return nil, err
	}
	rs := o.dpPassThrough(ctx, tenant)
	if rs == nil {
		rs = []PassThroughRow{}
	}
	return rs, nil
}

// ---- complaints forwarded by an exchange ----

// SetTicketExchange records the exchange that forwarded a complaint, the
// day it wants the redressal on its portal, and the amount claimed.
func (o *Ops) SetTicketExchange(ctx context.Context, tenant, actor, id, exchange, due, claim string) (Ticket, error) {
	if err := o.require(ctx, tenant, actor, "Recording complaints", RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return Ticket{}, err
	}
	exchange = strings.ToUpper(strings.TrimSpace(exchange))
	if exchange != "" && !contains([]string{"BSE", "NSE", "MCX", "NCDEX", "MSE"}, exchange) {
		return Ticket{}, invalid("the exchange is BSE, NSE, MCX, NCDEX or MSE")
	}
	if due != "" && !validDate(due) {
		return Ticket{}, invalid("the exchange's due date is YYYY-MM-DD")
	}
	var amt money.Amount
	if strings.TrimSpace(claim) != "" {
		a, err := money.ParseAmount(strings.ReplaceAll(claim, ",", ""))
		if err != nil || a < 0 {
			return Ticket{}, invalid("the amount claimed is in rupees")
		}
		amt = a
	}
	t, err := docs.Update(ctx, o.Docs, tenant, cTicket, id, actor, func(x *Ticket) error {
		if exchange != "" && x.Source != "Stock exchanges" {
			return invalid("only a complaint received from the stock exchanges has an exchange")
		}
		x.Exchange, x.ExchangeDue, x.Claim = exchange, due, amt
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "ticket.exchange", id, map[string]any{"exchange": exchange, "due": due, "claim": amt.String()})
	}
	return t, err
}

// ---- the daily reminders of these registers ----

const cGapMiscTick = "gapmisctick" // "last" -> dealerTickDoc

// gapMiscTick raises, once a day, the work these registers need: website
// lists that changed, credit on bills unpaid 180 days, TDS certificates
// due within 7 days, and complaints BSE wants answered within 2 days.
func (o *Ops) gapMiscTick(ctx context.Context, tenant string, now time.Time) {
	today := now.In(ist).Format("2006-01-02")
	last, _, _ := docs.GetOr(ctx, o.Docs, tenant, cGapMiscTick, "last", dealerTickDoc{})
	if last.Date >= today {
		return
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cGapMiscTick, "last", dealerTickDoc{Date: today}, SystemActor)
	o.websiteTick(ctx, tenant, today)
	for _, r := range o.itcUnpaid180(ctx, tenant, today) {
		o.raiseWork(ctx, tenant, "itc180", r.Bill, "Reverse input tax credit: bill "+r.Bill+" unpaid 180 days",
			fmt.Sprintf("Bill %s of %s dated %s is unpaid (₹%s) past %s. Add the credit on the unpaid part, ₹%s, to output tax in this month's GSTR-3B (CGST Act s.16(2)), and take it again once the vendor is paid.", r.Bill, r.Vendor, fmtDay(r.Date), formatINR(r.Unpaid.Dec()), fmtDay(r.Limit), formatINR(r.Reverse.Dec())),
			"", RoleChecker, "compregs", today)
	}
	for _, c := range o.tdsCertsDue(ctx, tenant) {
		if c.Issued != "" || c.Due == "" || c.Due < today || c.Due > addDays(today, 7) {
			continue
		}
		o.raiseWork(ctx, tenant, "tds-cert", c.PayeeKind+"|"+c.Payee+"|"+c.FY+"|"+c.Quarter, fmt.Sprintf("Give the TDS certificate for %s %s to %s by %s", c.FY, c.Quarter, c.Name, fmtDay(c.Due)),
			"Download it from TRACES, give it to the payee and record it on Compliance → Registers → TDS certificates issued.", "", RoleChecker, "compregs", today)
	}
	if ts, err := o.Tickets(ctx, tenant, today); err == nil {
		for _, t := range ts {
			if t.Exchange != "BSE" || t.ExchangeDue == "" || t.Status == "Resolved" || t.Status == "Closed" || t.ExchangeDue > addDays(today, 2) {
				continue
			}
			o.raiseWork(ctx, tenant, "bse-complaint", t.ID+"|"+t.ExchangeDue, "Upload the redressal of "+t.ID+" on BSE's portal by "+fmtDay(t.ExchangeDue),
				fmt.Sprintf("BSE forwarded complaint %s (reference %s) and wants the redressal and documents on its portal by %s.", t.ID, t.Ref, fmtDay(t.ExchangeDue)), t.UCC, RoleCompliance, "helpdesk", today)
		}
	}
}

// TDSCertsDue lists the TDS certificates due, with those given (for the
// Registers screen; the screen's roles open it).
func (o *Ops) TDSCertsDue(ctx context.Context, tenant string) []TDSCertDue {
	out := o.tdsCertsDue(ctx, tenant)
	if out == nil {
		out = []TDSCertDue{}
	}
	return out
}

// APStatusList is every AP's hold and cancellation, with the AP's name.
func (o *Ops) APStatusList(ctx context.Context, tenant string) []map[string]any {
	st := o.apStatuses(ctx, tenant)
	ps, _ := docs.List[Partner](ctx, o.Docs, tenant, cPartner, "")
	out := []map[string]any{}
	for _, p := range ps {
		if !isAP(p) {
			continue
		}
		out = append(out, map[string]any{"code": p.Code, "name": p.Name, "branch": p.Branch, "active": p.Active, "status": st[p.Code]})
	}
	return out
}

// ToBeClosedList is CDSL's latest "to be closed" list as loaded.
func (o *Ops) ToBeClosedList(ctx context.Context, tenant string) DPToBeClosed {
	l, _, _ := docs.GetOr(ctx, o.Docs, tenant, cDPTBC, "list", DPToBeClosed{BOIDs: []string{}})
	if l.BOIDs == nil {
		l.BOIDs = []string{}
	}
	return l
}
