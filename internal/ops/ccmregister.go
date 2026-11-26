package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
)

// ---- the register of client code modifications ----
//
// NSE/SURV/74008 (circular 313/2026): keep client code modifications to a
// minimum under internal controls, record a reason code for each one on
// ENIT (Compliance), and keep a single designated ERROR account
// (UCCControls, onboardx.go). Ledgerline keeps every modification (CCM,
// market.go) with its reason; staff record the reason code chosen on ENIT
// and the date it was reported here. The circular's reason code list is
// ENIT's own: the code is kept as staff enter it, not checked against a
// list Ledgerline would have to invent.

const cCCMReport = "ccmreport" // key: the CCM's trade key

// CCMReport is how a modification was reported on ENIT.
type CCMReport struct {
	TradeKey   string `json:"trade_key"`
	ENITCode   string `json:"enit_code"`
	ReportedOn string `json:"reported_on"`
	Ref        string `json:"ref,omitempty"`
	By         string `json:"by"`
	At         string `json:"at"`
}

// CCMRegRow is one modification on the register.
type CCMRegRow struct {
	CCM
	ENITCode   string `json:"enit_code,omitempty"`
	ReportedOn string `json:"reported_on,omitempty"`
	ReportRef  string `json:"report_ref,omitempty"`
	ToError    bool   `json:"to_error,omitempty"` // moved to the ERROR account
}

// CCMRegister lists the modifications between two dates with how each
// was reported.
func (o *Ops) CCMRegister(ctx context.Context, tenant, actor, from, to string) ([]CCMRegRow, error) {
	out := []CCMRegRow{}
	if err := o.require(ctx, tenant, actor, "Viewing client code modifications", RoleMaker, RoleChecker, RoleCompliance, RoleRisk); err != nil {
		return out, err
	}
	if !validDate(from) || !validDate(to) || from > to {
		return out, invalid("give the period as two dates, from before to")
	}
	return o.ccmRegister(ctx, tenant, from, to), nil
}

func (o *Ops) ccmRegister(ctx context.Context, tenant, from, to string) []CCMRegRow {
	out := []CCMRegRow{}
	all, _ := o.CCMs(ctx, tenant, "")
	reps, _ := docs.List[CCMReport](ctx, o.Docs, tenant, cCCMReport, "")
	by := map[string]CCMReport{}
	for _, r := range reps {
		by[r.TradeKey] = r
	}
	errAcct := o.UCCControlsNow(ctx, tenant).ErrorAccount
	for _, c := range all {
		if c.Date < from || c.Date > to {
			continue
		}
		r := by[c.TradeKey]
		out = append(out, CCMRegRow{CCM: c, ENITCode: r.ENITCode, ReportedOn: r.ReportedOn, ReportRef: r.Ref, ToError: errAcct != "" && c.To == errAcct})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Date+out[i].TradeKey < out[j].Date+out[j].TradeKey })
	return out
}

// CCMRegisterCSV is the register as a CSV file.
func (o *Ops) CCMRegisterCSV(ctx context.Context, tenant, actor, from, to string) (string, []byte, error) {
	rs, err := o.CCMRegister(ctx, tenant, actor, from, to)
	if err != nil {
		return "", nil, err
	}
	rows := [][]string{{"Trade date", "Exchange", "Segment", "Trade number", "From code", "To code", "Reason", "Modified by", "At", "ENIT reason code", "Reported on ENIT", "Reference"}}
	for _, r := range rs {
		rows = append(rows, []string{r.Date, r.Exchange, r.Segment, r.TradeNo, r.From, r.To, r.Reason, r.By, r.At, r.ENITCode, r.ReportedOn, r.ReportRef})
	}
	return fmt.Sprintf("ClientCodeModifications_%s_%s.csv", from, to), csvBytesOf(rows), nil
}

// RecordCCMReport records the reason code a modification was reported
// with on ENIT (Ops checker or Compliance).
func (o *Ops) RecordCCMReport(ctx context.Context, tenant, actor, tradeKey, code, on, ref string) (CCMReport, error) {
	if err := o.require(ctx, tenant, actor, "Recording the ENIT reason code", RoleChecker, RoleCompliance); err != nil {
		return CCMReport{}, err
	}
	if _, _, err := docs.Get[CCM](ctx, o.Docs, tenant, cCCM, tradeKey); err != nil {
		return CCMReport{}, err
	}
	code = strings.TrimSpace(code)
	if code == "" || len(code) > 40 || !validDate(on) {
		return CCMReport{}, invalid("give the reason code chosen on ENIT and the date it was reported")
	}
	r := CCMReport{TradeKey: tradeKey, ENITCode: code, ReportedOn: on, Ref: strings.TrimSpace(ref), By: actor, At: o.now().Format(time.RFC3339)}
	if err := docs.Upsert(ctx, o.Docs, tenant, cCCMReport, tradeKey, r, actor); err != nil {
		return r, err
	}
	o.closeWork(ctx, tenant, actor, "ccm-enit", tradeKey, "reported with "+code)
	o.log(ctx, tenant, actor, "ccm.enit.recorded", tradeKey, map[string]any{"code": code, "on": on})
	return r, nil
}

// ccmRaised asks for the ENIT report of a modification just made.
func (o *Ops) ccmRaised(ctx context.Context, tenant string, c CCM) {
	o.raiseWork(ctx, tenant, "ccm-enit", c.TradeKey, fmt.Sprintf("Report the code modification %s → %s on ENIT", c.From, c.To),
		fmt.Sprintf("Trade %s of %s (%s %s) was moved from %s to %s: %s. Record the modification's reason code on ENIT (Compliance) and enter it on Trades → Client code modifications. NSE/SURV/74008.", c.TradeNo, fmtDay(c.Date), c.Exchange, c.Segment, c.From, c.To, c.Reason),
		c.To, RoleChecker, "recon", c.Date)
}

func init() {
	siTests = append(siTests, SITest{ID: "ccm_enit", Title: "Client code modifications reported on ENIT with a reason code", Area: "Trading", Severity: "medium", Owner: RoleChecker,
		Source: "NSE/SURV/74008 (circular 313/2026)", Checks: "Client code modifications in the last 30 days, up to yesterday: the reason code reported on ENIT recorded.",
		Fix: "Report each modification's reason code on ENIT and record it on Trades → Client code modifications.", Screen: "recon", ScreenName: "Operations → Reconciliation", Real: true, run: siCCMENIT})
}

func siCCMENIT(e *siEnv) (siOut, error) {
	var s siOut
	s.input("Client code modifications", e.date)
	for _, r := range e.o.ccmRegister(e.ctx, e.tenant, addDays(e.date, -siWindow), addDays(e.date, -1)) {
		s.checked++
		if r.ENITCode == "" {
			s.bad(fmt.Sprintf("%s trade %s of %s: %s → %s not reported on ENIT", r.Exchange, r.TradeNo, fmtDay(r.Date), siClient(r.From), siClient(r.To)))
		}
	}
	s.summary = fmt.Sprintf("%d modifications checked; %d without the ENIT reason code.", s.checked, s.found)
	return s, nil
}

// ---- inter-depository transfers on trading holidays ----

// idtHolidayWhy says why an inter-depository slip cannot be taken for its
// execution date ("" when it can): no inter-depository transfers are
// exchanged on a trading holiday, so a slip dated on one is not accepted
// or set up (CDSL communiqué DP2025-829 for 2026's trading holidays). The
// firm's holiday list is the trading holidays; a weekend is not a trading
// day either, the safe reading.
func (o *Ops) idtHolidayWhy(ctx context.Context, tenant string, d DIS) string {
	if d.Kind != "Inter-depository" || !validDate(d.ExecDate) {
		return ""
	}
	set, _ := o.Settings(ctx, tenant)
	if set.IsBusinessDay(d.ExecDate) {
		return ""
	}
	why := "not a trading day"
	if h, name := set.IsHoliday(d.ExecDate); h {
		why = "a trading holiday" + map[bool]string{true: " (" + name + ")", false: ""}[name != ""]
	}
	return fmt.Sprintf("%s is %s: no inter-depository transfers are exchanged then, so the slip cannot be dated for it (CDSL DP2025-829). Date it for the next trading day, %s", fmtDay(d.ExecDate), why, fmtDay(set.NextBusinessDay(d.ExecDate)))
}
