package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// ---- the firm's deposits with the clearing corporations ----
//
// A clearing member keeps collateral with each clearing corporation: base
// minimum capital and additional collateral, as cash, fixed deposits lien-
// marked to the clearing corporation, bank guarantees and securities. Fixed
// deposits mature and bank guarantees expire; a lapse cuts the firm's
// trading limit the next morning. The register keeps every deposit, values
// it after haircut, checks that at least half is cash or cash equivalents,
// compares the total with what each clearing corporation requires, and
// queues renewals well before they fall due.

const cDeposit = "ccdeposit"

// Deposit kinds; the first three count as cash equivalents.
var DepositKinds = []string{"Cash", "Fixed deposit", "Bank guarantee", "Government securities", "Securities"}

var cashLike = map[string]bool{"Cash": true, "Fixed deposit": true, "Bank guarantee": true}

// DepositBMC is the purpose of a deposit kept as base minimum capital.
const DepositBMC = "Base minimum capital"

// ClearingCorps are the clearing corporations.
var ClearingCorps = []string{"NSE Clearing", "ICCL", "MCXCCL", "NCCL"}

// Deposit is one item of collateral with a clearing corporation.
type Deposit struct {
	ID         string    `json:"id"`
	CC         string    `json:"cc"`
	Segment    string    `json:"segment"` // CM, FO, CD, COM or All
	Kind       string    `json:"kind"`
	Purpose    string    `json:"purpose"` // Base minimum capital, Additional collateral
	Issuer     string    `json:"issuer"`  // bank, or the ISIN's company
	Ref        string    `json:"ref"`     // FD or BG number, ISIN
	Amount     money.Dec `json:"amount"`  // face value
	HaircutPct money.Dec `json:"haircut_pct"`
	Value      money.Dec `json:"value"` // after haircut
	Issued     string    `json:"issued,omitempty"`
	Expiry     string    `json:"expiry,omitempty"`
	Lien       bool      `json:"lien"` // lien marked in favour of the clearing corporation
	Status     string    `json:"status"`
	Note       string    `json:"note,omitempty"`
	By         string    `json:"by"`
	At         string    `json:"at"`
	Released   string    `json:"released,omitempty"`
	DaysLeft   int       `json:"days_left"`
	Alert      string    `json:"alert,omitempty"`
	// ClientFunds: a fixed deposit made from clients' money to upstream it
	// (SEBI master circular 2025/90 paras 97.5-97.6, from SEBI/HO/MIRSD/
	// MIRSD-PoD-1/P/CIR/2023/84 para 3.A): made only from the USCNBA
	// (FundedFrom, a register account), with a bank the clearing
	// corporation accepts, for at most one year and one day, principal
	// protected and pre-terminable on demand, lien-marked to the clearing
	// corporation, and never used for any funded or non-funded bank
	// facility (BankFacility). DepositedOn is the day the clearing
	// corporation took it; until then it is in transit (RC02).
	ClientFunds        bool   `json:"client_funds,omitempty"`
	FundedFrom         string `json:"funded_from,omitempty"`
	PrincipalProtected bool   `json:"principal_protected,omitempty"`
	PreTerminable      bool   `json:"pre_terminable,omitempty"`
	BankFacility       bool   `json:"bank_facility,omitempty"`
	DepositedOn        string `json:"deposited_on,omitempty"`
}

// clientFDRMaxExpiry is the latest maturity of a client-fund FDR: its tenor is at most one year and one
// day (SEBI master circular 2025/90, para 97.5).
func clientFDRMaxExpiry(issued string) string {
	t, err := time.Parse("2006-01-02", issued)
	if err != nil {
		return ""
	}
	return t.AddDate(1, 0, 1).Format("2006-01-02")
}

// clientFDRProblems says what is wrong with a client-fund FDR against
// paras 97.5-97.6 (nil when nothing).
func clientFDRProblems(d Deposit, reg []FirmBank) []string {
	if !d.ClientFunds {
		return nil
	}
	var out []string
	if d.Kind != "Fixed deposit" {
		out = append(out, "client money is upstreamed as cash, a fixed deposit or MFOS units: a "+strings.ToLower(d.Kind)+" cannot be made from it")
	}
	if b, ok := firmBankByID(reg, d.FundedFrom); !ok || b.Kind != BankUSCNBA {
		out = append(out, "a client-fund FDR is made only from the USCNBA: choose the USCNBA it was funded from")
	}
	if d.Issued == "" {
		out = append(out, "give the day the FDR was made")
	} else if max := clientFDRMaxExpiry(d.Issued); d.Expiry == "" || d.Expiry > max {
		out = append(out, fmt.Sprintf("its tenor may not exceed one year and one day: it must mature by %s", fmtDay(max)))
	}
	if !d.PrincipalProtected || !d.PreTerminable {
		out = append(out, "it must be principal protected and pre-terminable on demand")
	}
	if !d.Lien {
		out = append(out, "it must be lien-marked to the clearing corporation")
	}
	if d.BankFacility {
		out = append(out, "it may not be used for any funded or non-funded bank facility")
	}
	return out
}

// inTransitOn says whether a client-fund FDR was lien-marked to the
// clearing corporation and not yet deposited with it on date (RC02).
func (d Deposit) inTransitOn(date string) bool {
	return d.ClientFunds && d.Kind == "Fixed deposit" && d.Lien && d.Status == "Active" && d.Issued != "" && d.Issued <= date && (d.DepositedOn == "" || d.DepositedOn > date)
}

// DepositRequirement is what one clearing corporation asks for.
type DepositRequirement struct {
	CC       string    `json:"cc"`
	BMC      money.Dec `json:"bmc"` // base minimum capital
	Value    money.Dec `json:"value"`
	CashLike money.Dec `json:"cash_like"`
	CashPct  float64   `json:"cash_pct"`
	Short    money.Dec `json:"short"` // below BMC
	Expiring money.Dec `json:"expiring_30d"`
	// BMCHeld is the value of the deposits kept as base minimum capital
	// and BMCCash the cash and cash equivalents among them: at least half
	// (SEBI CIR/MRD/DRMNP/36/2012, para 3(g)).
	BMCHeld    money.Dec `json:"bmc_held"`
	BMCCash    money.Dec `json:"bmc_cash"`
	BMCCashPct float64   `json:"bmc_cash_pct"`
}

// DepositView is the register.
type DepositView struct {
	Date     string               `json:"date"`
	Deposits []Deposit            `json:"deposits"`
	ByCC     []DepositRequirement `json:"by_cc"`
	Total    money.Dec            `json:"total"`
	Margin   money.Dec            `json:"client_margin"` // clients' margin requirement today, for comparison
	Kinds    []string             `json:"kinds"`
	CCs      []string             `json:"ccs"`
}

func (d *Deposit) value() {
	d.Value = d.Amount.Sub(d.Amount.Pct(d.HaircutPct))
}

// SaveDeposit records a new deposit or corrects one.
func (o *Ops) SaveDeposit(ctx context.Context, tenant, actor string, d Deposit) (Deposit, error) {
	if err := o.require(ctx, tenant, actor, "Recording clearing deposits", RoleChecker); err != nil {
		return d, err
	}
	if !contains(ClearingCorps, d.CC) || !contains(DepositKinds, d.Kind) {
		return d, invalid("choose the clearing corporation and the kind of deposit")
	}
	if d.Amount.Sign() <= 0 {
		return d, invalid("amount must be above zero")
	}
	if d.HaircutPct.Sign() < 0 || d.HaircutPct.Cmp(money.DecInt(100)) >= 0 {
		return d, invalid("haircut is a percentage from 0 to below 100")
	}
	if (d.Kind == "Fixed deposit" || d.Kind == "Bank guarantee") && (!ledger.ValidDate(d.Expiry) || strings.TrimSpace(d.Issuer) == "" || strings.TrimSpace(d.Ref) == "") {
		return d, invalid("a %s needs the bank, its number and the maturity or expiry date", strings.ToLower(d.Kind))
	}
	if d.Issued != "" && !ledger.ValidDate(d.Issued) {
		return d, invalid("issue date must be YYYY-MM-DD")
	}
	if d.DepositedOn != "" && !ledger.ValidDate(d.DepositedOn) {
		return d, invalid("the day the clearing corporation took it must be YYYY-MM-DD")
	}
	if !d.ClientFunds {
		d.FundedFrom, d.PrincipalProtected, d.PreTerminable, d.BankFacility, d.DepositedOn = "", false, false, false, ""
	}
	if ps := clientFDRProblems(d, o.firmBanks(ctx, tenant)); len(ps) > 0 {
		return d, invalid("a fixed deposit of client money: %s (SEBI master circular 2025/90, paras 97.5-97.6)", strings.Join(ps, "; "))
	}
	d.Segment = firstOf(strings.ToUpper(d.Segment), "ALL")
	d.Purpose = firstOf(d.Purpose, "Additional collateral")
	if d.ID == "" {
		n, err := o.next(ctx, tenant, "ccdeposit", actor)
		if err != nil {
			return d, err
		}
		d.ID = fmt.Sprintf("CCD-%04d", n)
		d.Status = "Active"
	} else if old, _, err := docs.Get[Deposit](ctx, o.Docs, tenant, cDeposit, d.ID); err == nil {
		d.Status, d.Released = old.Status, old.Released
	} else {
		return d, err
	}
	d.value()
	d.By, d.At, d.DaysLeft, d.Alert = actor, o.now().Format(time.RFC3339), 0, ""
	if err := docs.Upsert(ctx, o.Docs, tenant, cDeposit, d.ID, d, actor); err != nil {
		return d, err
	}
	o.log(ctx, tenant, actor, "ccdeposit.saved", d.ID, map[string]any{"cc": d.CC, "kind": d.Kind, "amount": d.Amount.String(), "expiry": d.Expiry, "client_funds": d.ClientFunds, "funded_from": d.FundedFrom})
	return d, nil
}

// ReleaseDeposit marks a deposit released, renewed (replaced) or invoked.
func (o *Ops) ReleaseDeposit(ctx context.Context, tenant, actor, id, status, note string) (Deposit, error) {
	if err := o.require(ctx, tenant, actor, "Releasing clearing deposits", RoleChecker); err != nil {
		return Deposit{}, err
	}
	if !contains([]string{"Released", "Renewed", "Invoked"}, status) {
		return Deposit{}, invalid("status must be Released, Renewed or Invoked")
	}
	if strings.TrimSpace(note) == "" {
		return Deposit{}, invalid("give the clearing corporation's release reference or the new deposit")
	}
	d, err := docs.Update(ctx, o.Docs, tenant, cDeposit, id, actor, func(x *Deposit) error {
		if x.Status != "Active" {
			return fmt.Errorf("%w: %s is %s", ErrState, id, strings.ToLower(x.Status))
		}
		x.Status, x.Note, x.Released = status, strings.TrimSpace(note), o.now().Format("2006-01-02")
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "ccdeposit."+strings.ToLower(status), id, map[string]any{"note": note})
		o.closeWork(ctx, tenant, actor, "deposits", id, status)
	}
	return d, err
}

// Deposits builds the register for a date.
func (o *Ops) Deposits(ctx context.Context, tenant, date string) (DepositView, error) {
	v := DepositView{Date: date, Deposits: []Deposit{}, ByCC: []DepositRequirement{}, Kinds: DepositKinds, CCs: ClearingCorps}
	ds, err := docs.List[Deposit](ctx, o.Docs, tenant, cDeposit, "")
	if err != nil {
		return v, err
	}
	set, _ := o.Settings(ctx, tenant)
	reg := o.firmBanks(ctx, tenant)
	by := map[string]*DepositRequirement{}
	for _, cc := range ClearingCorps {
		by[cc] = &DepositRequirement{CC: cc}
		if b, ok := set.ClearingBMC[cc]; ok {
			by[cc].BMC, _ = money.ParseDec(b)
		}
	}
	for _, d := range ds {
		if d.Status == "Active" && d.Expiry != "" {
			d.DaysLeft = daysBetween(date, d.Expiry)
			switch {
			case d.DaysLeft < 0:
				d.Alert = "Expired: the clearing corporation no longer counts it"
			case d.DaysLeft <= 30:
				d.Alert = fmt.Sprintf("%s in %d days: renew or replace", map[bool]string{true: "Matures", false: "Expires"}[d.Kind == "Fixed deposit"], d.DaysLeft)
			}
		}
		if d.Status == "Active" && !d.Lien && d.Kind == "Fixed deposit" {
			d.Alert = strings.TrimPrefix(d.Alert+"; lien not yet marked to the clearing corporation", "; ")
		}
		if d.Status == "Active" {
			if ps := clientFDRProblems(d, reg); len(ps) > 0 {
				d.Alert = strings.TrimPrefix(d.Alert+"; client-fund FDR: "+strings.Join(ps, "; "), "; ")
			}
		}
		v.Deposits = append(v.Deposits, d)
		// A client-fund FDR is clients' money upstreamed, not the firm's
		// collateral against its base capital.
		if d.Status != "Active" || (d.Expiry != "" && d.DaysLeft < 0) || d.ClientFunds {
			continue
		}
		r := by[d.CC]
		r.Value = r.Value.Add(d.Value)
		if cashLike[d.Kind] {
			r.CashLike = r.CashLike.Add(d.Value)
		}
		if d.Expiry != "" && d.DaysLeft <= 30 {
			r.Expiring = r.Expiring.Add(d.Value)
		}
		if d.Purpose == DepositBMC {
			r.BMCHeld = r.BMCHeld.Add(d.Value)
			if cashLike[d.Kind] {
				r.BMCCash = r.BMCCash.Add(d.Value)
			}
		}
		v.Total = v.Total.Add(d.Value)
	}
	for _, cc := range ClearingCorps {
		r := by[cc]
		if r.Value.Sign() > 0 {
			f, _ := r.CashLike.Quo(r.Value)
			r.CashPct = f.Float64() * 100
		}
		if r.BMC.Cmp(r.Value) > 0 {
			r.Short = r.BMC.Sub(r.Value)
		}
		if r.BMCHeld.Sign() > 0 {
			f, _ := r.BMCCash.Quo(r.BMCHeld)
			r.BMCCashPct = f.Float64() * 100
		}
		if r.Value.Sign() > 0 || r.BMC.Sign() > 0 {
			v.ByCC = append(v.ByCC, *r)
		}
	}
	sort.SliceStable(v.Deposits, func(i, j int) bool {
		a, b := v.Deposits[i], v.Deposits[j]
		if (a.Status == "Active") != (b.Status == "Active") {
			return a.Status == "Active"
		}
		if a.Expiry != b.Expiry {
			return a.Expiry != "" && (b.Expiry == "" || a.Expiry < b.Expiry)
		}
		return a.ID < b.ID
	})
	if rv, err := o.Risk(ctx, tenant, date); err == nil {
		for _, r := range rv.Rows {
			v.Margin = v.Margin.Add(r.Required)
		}
	}
	return v, nil
}

// autoDeposits queues deposits maturing or expiring within 30 days, those
// without a lien, and clearing corporations below their base capital or
// with less than half in cash and cash equivalents.
func autoDeposits(r *runCtx) error {
	v, err := r.o.Deposits(r.ctx, r.tenant, r.date)
	if err != nil {
		return err
	}
	for _, d := range v.Deposits {
		if d.Status != "Active" || d.Alert == "" {
			continue
		}
		due := r.date + " 17:00"
		if d.Expiry != "" && d.DaysLeft > 7 {
			due = addDays(d.Expiry, -7) + " 17:00"
		}
		r.queue(d.ID, fmt.Sprintf("%s %s %s ₹%s: %s", d.CC, strings.ToLower(d.Kind), d.Ref, formatINR(d.Amount), d.Alert), "Arrange the renewal or a replacement with the bank and the clearing corporation, then mark this deposit renewed with the new one's reference.", "", RoleChecker, "deposits", due, nil)
	}
	for _, c := range v.ByCC {
		if c.Short.Sign() > 0 {
			r.queue(c.CC+"|bmc", fmt.Sprintf("%s: collateral ₹%s below base minimum capital ₹%s", c.CC, formatINR(c.Value), formatINR(c.BMC)), "Deposit the shortfall before the clearing corporation reduces the limit.", "", RoleChecker, "deposits", r.date+" 12:00", nil)
		}
		if c.BMCHeld.Sign() > 0 && c.BMCCashPct < 50 {
			r.queue(c.CC+"|bmccash", fmt.Sprintf("%s: only %.0f%% of the base minimum capital deposit is cash or cash equivalents", c.CC, c.BMCCashPct), "At least half of the base minimum capital is kept in cash and cash equivalents (SEBI CIR/MRD/DRMNP/36/2012, para 3(g)); replace securities with cash, a fixed deposit or a bank guarantee.", "", RoleChecker, "deposits", r.date+" 12:00", nil)
		}
		if c.Value.Sign() > 0 && c.CashPct < 50 {
			r.queue(c.CC+"|cash", fmt.Sprintf("%s: only %.0f%% of collateral is cash or cash equivalents", c.CC, c.CashPct), "At least half must be cash, fixed deposits or bank guarantees; the rest counts only up to that.", "", RoleChecker, "deposits", r.date+" 12:00", nil)
		}
	}
	r.note("%d deposits, ₹%s", len(v.Deposits), formatINR(v.Total))
	return nil
}
