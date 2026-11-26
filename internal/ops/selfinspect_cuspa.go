package ops

import (
	"fmt"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// ---- self-inspection: unpaid securities, pledges, re-pledge, collateral ----
//
// The tests of the CUSPA framework's other duties (cuspa_more.go) and of
// how clients' pledged securities are used: SEBI's master circular for
// stock brokers (SEBI/HO/MIRSD/MIRSD-PoD/P/CIR/2025/90 of 17 June 2025)
// paras 15.4.4.4, 42.10, 42.12.5 and 43.13-43.14, as revised by
// HO/38/11/(9)2026-MIRSD-POD/I/15382/2026 (para 46); the depositories'
// master circular on notice before invoking a pledge (SEBI
// HO/47/14/12(1)2026-MRD-POD2/I/4229/2026 para 4).

func init() {
	siTests = append(siTests,
		SITest{ID: "cuspa_intimation", Title: "Client told of each CUSPA pledge by the start of the next trading day", Area: "Settlement", Severity: "high", Owner: RoleChecker,
			Source:    "SEBI HO/38/11/(9)2026-MIRSD-POD/I/15382/2026 of 3 Jul 2026, revised master circular para 46.2; NSE circular 41/2026 (NSE/INSP/75508) of 31 Jul 2026, guideline 2",
			ToConfirm: "The start of the next trading day is read as 9:00 AM IST on the trading day after the pay-out.",
			Checks:    "CUSPA lines paid out in the last 30 days (from 31 Oct 2026): an email or SMS to the client recorded (debit, obligation, shares pledged, the right to sell) by 9:00 AM on the trading day after the pay-out.",
			Fix:       "Run the daily CUSPA review (it sends what is missing); correct the client's email or mobile; tell the client and keep the record.",
			Screen:    "settlement", ScreenName: "Operations → Settlement", Real: true, run: siCUSPAIntimation},
		SITest{ID: "cuspa_policy", Title: "Written policy on unpaid securities, sent to every client and on the website", Area: "Settlement", Severity: "high", Owner: RoleCompliance,
			Source: "SEBI HO/38/11/(9)2026-MIRSD-POD/I/15382/2026 of 3 Jul 2026, revised master circular paras 46.3-46.4",
			Checks: "The policy is recorded with its payment period (as in Settings, at most 5 trading days), where it is on the website, and sent to clients before it applied; every active client has been sent the current version.",
			Fix:    "Record the policy on Settlement → Unpaid securities → Policy, publish it on the website and send it to clients.",
			Screen: "settlement", ScreenName: "Operations → Settlement", Real: true, run: siCUSPAPolicy},
		SITest{ID: "cuspa_extension", Title: "CUSPA extensions asked by 6 PM on the last day, each told to the client", Area: "Settlement", Severity: "medium", Owner: RoleChecker,
			Source: "SEBI HO/38/11/(9)2026-MIRSD-POD/I/15382/2026, revised master circular paras 46.12-46.14 (from 3 Jan 2027); NSE circular 41/2026 (NSE/INSP/75508), guideline 10",
			Checks: "Every extension recorded: asked on the line's last day to invoke, before 6 PM, with a reason code CUSPA01 to CUSPA06, and the client's notice sent.",
			Fix:    "Tell the client of the extension; confirm the request reached the depository in time; review late requests with the compliance officer.",
			Screen: "settlement", ScreenName: "Operations → Settlement", Real: true, run: siCUSPAExtension},
		SITest{ID: "cuspa_no_lender", Title: "CUSPA securities never pledged to banks or NBFCs", Area: "Settlement", Severity: "high", Owner: RoleCompliance,
			Source: "SEBI HO/38/11/(9)2026-MIRSD-POD/I/15382/2026, revised master circular para 46.11",
			Checks: "The firm's client unpaid securities accounts (CLNT UNPAID) in CDSL's last statement of holding hold nothing and have nothing pledged out; no pledge to a lender or re-pledge carries a pledge of the CUSPA.",
			Fix:    "Release the pledge to the lender at once, return the securities to the client's demat account and report it to the compliance officer and the exchanges.",
			Screen: "cdslpledge", ScreenName: "Collateral → Pledges (CDSL)", Real: true, run: siCUSPANoLender},
		SITest{ID: "client_secs_pool", Title: "Clients' securities not left in the pool or unpaid accounts", Area: "Settlement", Severity: "high", Owner: RoleChecker,
			Source:    "SEBI master circular SEBI/HO/MIRSD/MIRSD-PoD/P/CIR/2025/90, para 42.10; CDSL master circular for DPs (Mar 2026) 4.10.2.1 (pay-out to clients within one working day)",
			ToConfirm: "A balance seen in two statements of holding in a row is taken as kept beyond one working day; the firm's own securities belong in OWN accounts.",
			Checks:    "The firm's pool (POOL) and client unpaid securities (CLNT UNPAID) accounts in CDSL's statements of holding: an ISIN held on the date and on the previous working day as well.",
			Fix:       "Transfer the securities to the clients' own demat accounts (or deliver them in the settlement they were received for); record why they were kept.",
			Screen:    "settings", ScreenName: "Settings → Settings (Firm demat accounts)", Real: true, run: siClientSecsPool},
		SITest{ID: "repledge_own_client", Title: "Each client's re-pledged securities cover only that client", Area: "Risk", Severity: "high", Owner: RoleRisk,
			Source: "SEBI master circular SEBI/HO/MIRSD/MIRSD-PoD/P/CIR/2025/90, para 42.12.5 (client securities re-pledged to the clearing corporation never used for the member's or another client's obligations)",
			Checks: "NSE Clearing's re-pledge status file of the date against the clients' margin pledges: no client shown with more of an ISIN than the client pledged to the firm, and nothing under the firm's own code or a code that is not a client.",
			Fix:    "Correct the re-pledge (release what was re-pledged under the wrong code and re-pledge it under the client's); find whose securities cover the client.",
			Screen: "cdslpledge", ScreenName: "Collateral → Pledges (CDSL)", Real: true, run: siRepledgeOwnClient},
		SITest{ID: "client_margin_cover", Title: "Each client's collateral at NSE Clearing covers the client's margin", Area: "Risk", Severity: "high", Owner: RoleRisk,
			Source: "SEBI master circular SEBI/HO/MIRSD/MIRSD-PoD/P/CIR/2025/90, paras 43.13-43.14 (allocated collateral plus re-pledged securities at or above the client's minimum margin in each segment at all times)",
			Checks: "NSE Clearing's client margins of the date (margins API), per segment: the client's cash collateral (allocated) plus non-cash collateral (re-pledged) at least the client's total margin.",
			Fix:    "Allocate more of the client's collateral (or re-pledge more of the client's pledged securities); collect the shortfall from the client; the clearing corporation blocks the firm's own collateral for the gap.",
			Screen: "allocation", ScreenName: "Collateral → Clearing allocation", Real: true, run: siClientMarginCover},
		SITest{ID: "debit_day5_block", Title: "No further exposure past the 5th trading day after pay-in", Area: "Risk", Severity: "high", Owner: RoleRisk,
			Source: "SEBI master circular SEBI/HO/MIRSD/MIRSD-PoD/P/CIR/2025/90, para 15.4.4.4 (debit from non-payment beyond T+1+5); NSE/INSP/73976 Exhibit 26 Q19 (no fresh buying while unpaid securities are sold)",
			Checks: "Clients in debit on the date: those whose oldest unpaid debit's pay-in is more than 5 trading days old, or whose unpaid securities are being sold, are blocked for buying.",
			Fix:    "Block the clients on Debtors & interest (Apply blocks) and send the block file to the front end.",
			Screen: "debits", ScreenName: "Debtors → Debtors & interest", Real: true, run: siDebitDay5},
		SITest{ID: "pledge_invoke_notice", Title: "Notice given before a margin pledge was invoked", Area: "Risk", Severity: "high", Owner: RoleCompliance,
			Source: "SEBI HO/47/14/12(1)2026-MRD-POD2/I/4229/2026, para 4 (reasonable notice to the pledgor before selling, ss. 176-177 Indian Contract Act); CDSL communiqué DP2026-180",
			Checks: "Invocations of margin and MTF pledges sent to CDSL in the last 30 days: the client's notice given, and the invocation sent only after the notice period.",
			Fix:    "Find the notice sent and record it; review with the compliance officer why the pledge was invoked without it.",
			Screen: "cdslpledge", ScreenName: "Collateral → Pledges (CDSL)", Real: true, run: siPledgeInvokeNotice},
	)
}

// ---- CUSPA ----

func siCUSPAIntimation(e *siEnv) (siOut, error) {
	items, err := docs.List[CUSPAItem](e.ctx, e.o.Docs, e.tenant, cCUSPA, "")
	if err != nil {
		return siOut{}, err
	}
	var s siOut
	s.input("CUSPA lines", e.date)
	from := max(addDays(e.date, -siWindow), cuspaFrameworkFrom)
	for _, it := range items {
		d := it.dates(e.set, e.date)
		if d.PayOut < from || d.PayOut > e.date {
			continue
		}
		due := e.set.NextBusinessDay(d.PayOut) + " 09:00"
		if e.date+" 23:59" < due {
			continue // not due yet
		}
		s.checked++
		at := ""
		if t, err := time.Parse(time.RFC3339, it.IntimatedAt); err == nil {
			at = t.In(ist).Format("2006-01-02 15:04")
		}
		switch {
		case at == "":
			s.bad(fmt.Sprintf("CUSPA line %s · %s · paid out %s · no intimation recorded%s", it.ID, siClient(it.UCC), fmtDay(d.PayOut), map[bool]string{true: " (" + it.Intimation + ")", false: ""}[it.Intimation != ""]))
		case at > due:
			s.bad(fmt.Sprintf("CUSPA line %s · %s · paid out %s · told on %s, after the start of the next trading day", it.ID, siClient(it.UCC), fmtDay(d.PayOut), at))
		}
	}
	s.summary = fmt.Sprintf("%d CUSPA lines checked; %d without the intimation in time.", s.checked, s.found)
	return s, nil
}

func siCUSPAPolicy(e *siEnv) (siOut, error) {
	v, err := e.o.CUSPAPolicyStatus(e.ctx, e.tenant)
	if err != nil {
		return siOut{}, err
	}
	var s siOut
	s.input("Policy on unpaid securities", e.date)
	s.checked = 1 + v.Active
	for _, p := range v.Problems {
		s.bad(p)
	}
	if v.Policy != nil {
		s.summary = fmt.Sprintf("Version %d, in force from %s; %d of %d active clients sent it.", v.Policy.Version, fmtDay(v.Policy.EffectiveFrom), v.SentNow, v.Active)
	} else {
		s.summary = "No policy on unpaid securities recorded."
	}
	return s, nil
}

func siCUSPAExtension(e *siEnv) (siOut, error) {
	items, err := docs.List[CUSPAItem](e.ctx, e.o.Docs, e.tenant, cCUSPA, "")
	if err != nil {
		return siOut{}, err
	}
	var s siOut
	s.input("CUSPA lines", e.date)
	for _, it := range items {
		var prev CUSPAItem = it
		prev.Extensions = nil
		for i, x := range it.Extensions {
			if x.Date > e.date {
				continue
			}
			s.checked++
			// The last day to invoke before this extension.
			last := prev.dates(e.set, x.Date).LastInvoke
			at := ""
			if t, err := time.Parse(time.RFC3339, x.At); err == nil {
				at = t.In(ist).Format("2006-01-02 15:04")
			}
			switch {
			case x.Date != last:
				s.bad(fmt.Sprintf("CUSPA line %s · %s · extension asked on %s, not on the last day to invoke (%s)", it.ID, siClient(it.UCC), fmtDay(x.Date), fmtDay(last)))
			case at > x.Date+" "+cuspaExtCutoff:
				s.bad(fmt.Sprintf("CUSPA line %s · %s · extension recorded at %s, after 6 PM", it.ID, siClient(it.UCC), at))
			case !reCUSPAExtCode.MatchString(x.Code):
				s.bad(fmt.Sprintf("CUSPA line %s · %s · extension of %s without a reason code", it.ID, siClient(it.UCC), fmtDay(x.Date)))
			case !x.NoticeOK:
				s.bad(fmt.Sprintf("CUSPA line %s · %s · extension of %s not told to the client (%s)", it.ID, siClient(it.UCC), fmtDay(x.Date), firstOf(x.Notice, "no notice recorded")))
			}
			prev.Extensions = it.Extensions[:i+1]
		}
	}
	if s.checked == 0 {
		s.summary = "No CUSPA extension recorded."
		return s, nil
	}
	s.summary = fmt.Sprintf("%d extensions checked; %d late, without a code or not told to the client.", s.checked, s.found)
	return s, nil
}

func siCUSPANoLender(e *siEnv) (siOut, error) {
	var s siOut
	cuspaPSN := map[string]bool{}
	ps, _ := docs.List[CUSPAPledge](e.ctx, e.o.Docs, e.tenant, cCUSPAPSN, "")
	for _, p := range ps {
		cuspaPSN[p.PSN] = true
	}
	items, _ := docs.List[CUSPAItem](e.ctx, e.o.Docs, e.tenant, cCUSPA, "")
	for _, it := range items {
		if it.PSN != "" {
			cuspaPSN[strings.TrimLeft(it.PSN, "0")] = true
		}
	}
	s.input("CUSPA pledges (CDSL transaction export)", e.date)
	for _, n := range e.o.cplNormals(e.ctx, e.tenant) {
		for _, l := range n.Lines {
			s.checked++
			if l.PSN != "" && cuspaPSN[strings.TrimLeft(l.PSN, "0")] {
				s.bad(fmt.Sprintf("Pledge %s to %s · PSN %s is a CUSPA pledge", n.ID, n.Lender, l.PSN))
			}
		}
	}
	for _, b := range e.o.cplRepledges(e.ctx, e.tenant) {
		for _, l := range b.Lines {
			s.checked++
			if cuspaPSN[strings.TrimLeft(l.PSN, "0")] {
				s.bad(fmt.Sprintf("Re-pledge %s · %s · PSN %s is a CUSPA pledge", b.ID, siClient(l.UCC), l.PSN))
			}
		}
	}
	snap, _, _ := docs.GetOr(e.ctx, e.o.Docs, e.tenant, cFirmDematSnap, "cdsl", FirmDematSnap{})
	unpaid := 0
	for _, typ := range e.set.FirmDemats {
		if typ == "CLNT UNPAID" {
			unpaid++
		}
	}
	if snap.Date != "" {
		s.input("CDSL statement of holding", snap.Date)
		for _, a := range snap.Accounts {
			if e.set.FirmDemats[a.BOID] != "CLNT UNPAID" {
				continue
			}
			s.checked++
			for _, l := range a.Lines {
				if decOr0(l.Pledged).Sign() > 0 {
					s.bad(fmt.Sprintf("CUSPA account %s · %s: %s pledged out", MaskAccount(a.BOID), l.ISIN, l.Pledged))
				}
			}
		}
	}
	if s.checked == 0 {
		return siSkip("Waiting for data: no pledge to a lender, no re-pledge and no tagged client unpaid securities account (CLNT UNPAID) with a statement of holding.")
	}
	s.summary = fmt.Sprintf("%d pledges, re-pledges and CUSPA accounts checked (%d CUSPA account(s) tagged); %d CUSPA securities pledged out.", s.checked, unpaid, s.found)
	return s, nil
}

// siClientSecsPool looks at the firm's pool and client unpaid accounts in
// the statements of holding of the date and the working day before.
func siClientSecsPool(e *siEnv) (siOut, error) {
	snap, _, err := docs.GetOr(e.ctx, e.o.Docs, e.tenant, cFirmDematSnap, "cdsl", FirmDematSnap{})
	if err != nil {
		return siOut{}, err
	}
	if snap.Date == "" {
		return siSkip("Waiting for data: load CDSL's statement of holding on Exchange files.")
	}
	day := snap.Date
	if day > e.date {
		day = e.date
		if x, _, err := docs.Get[FirmDematSnap](e.ctx, e.o.Docs, e.tenant, cFirmDematSnap, "cdsl|"+day); err == nil {
			snap = x
		} else {
			return siSkip("Waiting for data: no statement of holding for " + fmtDay(day) + ".")
		}
	}
	prevDay := e.set.PrevBusinessDay(snap.Date)
	prev, _, err := docs.Get[FirmDematSnap](e.ctx, e.o.Docs, e.tenant, cFirmDematSnap, "cdsl|"+prevDay)
	var s siOut
	s.input("CDSL statement of holding", snap.Date)
	before := map[string]money.Dec{}
	if err == nil {
		s.input("CDSL statement of holding", prevDay)
		for _, a := range prev.Accounts {
			for _, l := range a.Lines {
				before[a.BOID+"|"+l.ISIN] = decOr0(l.Total)
			}
		}
	}
	tagged := 0
	for _, a := range snap.Accounts {
		typ := e.set.FirmDemats[a.BOID]
		if typ != "POOL" && typ != "CLNT UNPAID" {
			continue
		}
		tagged++
		for _, l := range a.Lines {
			s.checked++
			q := decOr0(l.Total)
			switch {
			case typ == "CLNT UNPAID" && q.Sign() > 0:
				// Since 31 Oct 2026 unpaid securities stay in the client's
				// account, pledged to the CUSPA: the account holds nothing.
				s.bad(fmt.Sprintf("Client unpaid securities account %s · %s: %s held (unpaid securities are pledged in the client's account)", MaskAccount(a.BOID), l.ISIN, l.Total))
			case typ == "POOL" && q.Sign() > 0 && before[a.BOID+"|"+l.ISIN].Sign() > 0:
				s.bad(fmt.Sprintf("Pool account %s · %s: %s held on %s and %s held on %s", MaskAccount(a.BOID), l.ISIN, l.Total, fmtDay(snap.Date), before[a.BOID+"|"+l.ISIN].String(), fmtDay(prevDay)))
			}
		}
	}
	if tagged == 0 {
		return siSkip("Waiting for data: no pool (POOL) or client unpaid securities (CLNT UNPAID) account is tagged in Settings → Firm demat accounts, or none is in the statement of holding.")
	}
	if err != nil {
		s.summary = fmt.Sprintf("%d balances in %d pool and unpaid accounts on %s; the statement of %s is not loaded, so balances kept from the day before are not checked.", s.checked, tagged, fmtDay(snap.Date), fmtDay(prevDay))
		return s, nil
	}
	s.summary = fmt.Sprintf("%d balances in %d pool and unpaid accounts on %s; %d kept beyond a working day or held in the unpaid account.", s.checked, tagged, fmtDay(snap.Date), s.found)
	return s, nil
}

// ---- re-pledge and margin cover ----

func siRepledgeOwnClient(e *siEnv) (siOut, error) {
	rows, loaded := e.o.RepledgeRecon(e.ctx, e.tenant, e.date)
	if !loaded {
		return siSkip("Waiting for data: load NSE Clearing's re-pledge status file of the date on Clearing data.")
	}
	var s siOut
	s.input("NSE Clearing re-pledge status", e.date)
	s.input("Clients' margin pledges", e.date)
	own := strings.ToUpper(strings.TrimSpace(e.set.FirmUCC))
	for _, r := range rows {
		if r.Accepted <= 0 {
			continue
		}
		s.checked++
		_, isClient := e.clients[r.UCC]
		switch {
		case r.UCC == "PRO" || (own != "" && r.UCC == own):
			s.bad(fmt.Sprintf("%s under the firm's own code %s: %d re-pledged (clients' securities do not cover the firm's obligations)", firstOf(r.Symbol, r.ISIN), r.UCC, r.Accepted))
		case !isClient:
			s.bad(fmt.Sprintf("%s under code %s, which is no client: %d re-pledged", firstOf(r.Symbol, r.ISIN), r.UCC, r.Accepted))
		case r.Accepted > r.Pledged:
			s.bad(fmt.Sprintf("%s · %s: NSE Clearing holds %d re-pledged, the client pledged %d to the firm", siClient(r.UCC), firstOf(r.Symbol, r.ISIN), r.Accepted, r.Pledged))
		}
	}
	s.summary = fmt.Sprintf("%d client re-pledges at NSE Clearing checked; %d not covered by the client's own pledge.", s.checked, s.found)
	return s, nil
}

func siClientMarginCover(e *siEnv) (siOut, error) {
	var s siOut
	segs := 0
	for _, seg := range []string{"CM", "FO", "CD", "SLB"} {
		cs, err := docs.List[NCLMClient](e.ctx, e.o.Docs, e.tenant, cNCLMCli, seg+"|"+e.date+"|")
		if err != nil {
			return siOut{}, err
		}
		if len(cs) == 0 {
			continue
		}
		segs++
		s.input("NSE Clearing client margins ("+seg+")", e.date)
		for _, c := range cs {
			if c.Total.Sign() <= 0 {
				continue
			}
			s.checked++
			have := c.CashCollateral.Add(c.NonCash)
			if have.Cmp(c.Total) < 0 {
				s.bad(fmt.Sprintf("%s · %s · margin ₹%s, collateral ₹%s (cash %s, non-cash %s): short ₹%s", siClient(c.UCC), seg, formatINR(c.Total), formatINR(have), formatINR(c.CashCollateral), formatINR(c.NonCash), formatINR(c.Total.Sub(have))))
			}
		}
	}
	if segs == 0 {
		return siSkip("Waiting for data: NSE Clearing's client margins for the date (Connections → NSE Clearing margins API).")
	}
	s.summary = fmt.Sprintf("%d client margins in %d segment(s); %d not covered by the client's allocated and re-pledged collateral.", s.checked, segs, s.found)
	return s, nil
}

// ---- debits ----

func siDebitDay5(e *siEnv) (siOut, error) {
	v, err := e.o.Debits(e.ctx, e.tenant, e.date)
	if err != nil {
		return siOut{}, err
	}
	var s siOut
	s.input("Debtors", e.date)
	for _, r := range v.Rows {
		if r.Debit <= 0 || r.Legacy {
			continue
		}
		s.checked++
		if (r.PayInDays > debitPayInDays || r.BlockWhy == "unpaid securities being sold") && !r.Blocked {
			c := e.clients[r.UCC]
			if c.Status != "Active" {
				continue
			}
			s.bad(fmt.Sprintf("%s · debit ₹%s · %s · not blocked", siClient(r.UCC), r.Debit, r.BlockWhy))
		}
	}
	s.summary = fmt.Sprintf("%d clients in debit; %d still free to buy past the 5th trading day after pay-in or while their unpaid securities are sold.", s.checked, s.found)
	return s, nil
}

// ---- pledges ----

func siPledgeInvokeNotice(e *siEnv) (siOut, error) {
	var s siOut
	s.input("Pledge invocations (CDSL)", e.date)
	from := addDays(e.date, -siWindow)
	for _, r := range e.o.cplReleases(e.ctx, e.tenant) {
		if r.Kind != "CF" || (r.Of != "MP" && r.Of != "MF") || r.SentAt == "" {
			continue
		}
		sent, err := time.Parse(time.RFC3339, r.SentAt)
		if err != nil {
			continue
		}
		day := sent.In(ist).Format("2006-01-02")
		if day < from || day > e.date {
			continue
		}
		s.checked++
		due, derr := time.Parse(time.RFC3339, r.DueAt)
		switch {
		case r.NoticeAt == "":
			s.bad(fmt.Sprintf("Invocation %s · %s · %d %s sent %s with no notice to the client", r.ID, siClient(r.UCC), r.Qty, r.Symbol, fmtDay(day)))
		case derr == nil && sent.Before(due):
			s.bad(fmt.Sprintf("Invocation %s · %s · sent %s, before the notice ran out (%s)", r.ID, siClient(r.UCC), sent.In(ist).Format("02 Jan 15:04"), due.In(ist).Format("02 Jan 15:04")))
		}
	}
	s.summary = fmt.Sprintf("%d invocations of margin and MTF pledges checked; %d without notice or before it ran out.", s.checked, s.found)
	return s, nil
}
