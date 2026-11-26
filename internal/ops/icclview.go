package ops

import (
	"context"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// ---- the BSE Clearing (ICCL) screen and its daily run ----

// ICCLShortRow is one client snapshot in ICCL's short allocation file.
type ICCLShortRow struct {
	Segment    string       `json:"segment"`
	Snapshot   string       `json:"snapshot"`
	UCC        string       `json:"ucc"`
	Name       string       `json:"name"`
	Required   money.Dec    `json:"required"`
	Collateral money.Dec    `json:"collateral"`
	Short      money.Dec    `json:"short"`
	Reasons    []ICCLReason `json:"reasons"`
}

// ICCLDiffRow is one mismatch in ICCL's ALLOCATIONDIFF.
type ICCLDiffRow struct {
	Segment   string    `json:"segment"`
	UCC       string    `json:"ucc"`
	Name      string    `json:"name"`
	Allocated money.Dec `json:"allocated"`
	Reported  money.Dec `json:"reported"`
	Diff      money.Dec `json:"diff"`
}

// ICCLView is the day at BSE Clearing for a firm that clears there.
type ICCLView struct {
	On          bool              `json:"on"`
	Why         string            `json:"why"`
	Date        string            `json:"date"`
	Segments    []string          `json:"segments"`
	Notes       []string          `json:"notes"`
	Layouts     []XFormat         `json:"layouts"` // ICCL's layouts built from notices: to confirm
	Margins     []ICCLMarginRow   `json:"margins"`
	Segregation int               `json:"segregation_rows"`
	NilBalance  int               `json:"nil_balance"`
	Short       []ICCLShortRow    `json:"short"`
	ShortFile   string            `json:"short_file,omitempty"`
	Diffs       []ICCLDiffRow     `json:"diffs"`
	DiffFile    string            `json:"diff_file,omitempty"`
	Alloc       []ICCLAllocRow    `json:"alloc"`
	AllocIssues int               `json:"alloc_issues"`
	PreDelivery []PreDeliveryRow  `json:"pre_delivery"`
	Limit       ICCLTradingLimit  `json:"trading_limit"`
	Cutoffs     payInCutoffsView  `json:"cutoffs"`
	Accounts    []string          `json:"accounts"` // what is missing for pay-in
	Codes       map[string]string `json:"codes"`
	ReasonCodes map[string]string `json:"reason_codes"`
}

type payInCutoffsView struct {
	Funds        string `json:"funds"`
	Securities   string `json:"securities"`
	Auction      string `json:"auction"`
	AuctionInstr string `json:"auction_instr"`
	Shortage     string `json:"internal_shortage"`
}

// ICCLDay builds the screen. A firm that does not clear at ICCL gets
// only On false and the reason.
func (o *Ops) ICCLDay(ctx context.Context, tenant, actor, date string) (ICCLView, error) {
	if err := o.require(ctx, tenant, actor, "Seeing the BSE Clearing (ICCL) day", RoleMaker, RoleChecker, RoleRisk, RoleCompliance); err != nil {
		return ICCLView{}, err
	}
	if !validDate(date) {
		return ICCLView{}, invalid("give a date (YYYY-MM-DD)")
	}
	v := ICCLView{Date: date, Segments: []string{}, Notes: []string{}, Layouts: []XFormat{}, Margins: []ICCLMarginRow{}, Short: []ICCLShortRow{}, Diffs: []ICCLDiffRow{}, Alloc: []ICCLAllocRow{}, PreDelivery: []PreDeliveryRow{}, Accounts: []string{}, ReasonCodes: ICCLReasonCodes}
	v.On, v.Why = o.icclSelf(ctx, tenant)
	if !v.On {
		return v, nil
	}
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return v, err
	}
	segs, split := icclSegments(set)
	v.Segments, v.Notes = append(v.Segments, segs...), append(v.Notes, split...)
	if fs, err := o.ExchangeFormats(); err == nil {
		for _, f := range fs {
			if f.Party == "ICCL" && f.Confirm {
				v.Layouts = append(v.Layouts, f)
			}
		}
	}
	cm, tm, err := icclCodes(set)
	if err != nil {
		v.Notes = append(v.Notes, strings.TrimPrefix(err.Error(), "invalid: "))
	}
	v.Codes = map[string]string{"cm": cm, "tm": tm}
	if v.Margins, err = o.icclMargins(ctx, tenant, set, date); err != nil {
		return v, err
	}
	if v.Margins == nil {
		v.Margins = []ICCLMarginRow{}
	}
	if seg, nils, err := o.icclSegregation(ctx, tenant, set, date); err == nil {
		v.Segregation, v.NilBalance = len(seg), nils
	}
	if v.Alloc, err = o.icclAllocation(ctx, tenant, set, date); err != nil {
		return v, err
	}
	if v.Alloc == nil {
		v.Alloc = []ICCLAllocRow{}
	}
	for _, a := range v.Alloc {
		if a.Problem != "" {
			v.AllocIssues++
		}
	}
	cs, _ := o.ClientMap(ctx, tenant)
	for _, d := range o.icclData(ctx, tenant, "shortalloc", date) {
		v.ShortFile = strings.TrimPrefix(v.ShortFile+", "+d.File, ", ")
		for _, r := range d.Rows {
			rs := o.icclReasonsFor(ctx, tenant, date, r["snapshot"], d.Segment, r["ucc"])
			if rs == nil {
				rs = []ICCLReason{}
			}
			v.Short = append(v.Short, ICCLShortRow{Segment: d.Segment, Snapshot: r["snapshot"], UCC: r["ucc"], Name: cs[r["ucc"]].Name, Required: decOr0(r["required"]), Collateral: decOr0(r["collateral"]), Short: decOr0(r["short"]), Reasons: rs})
		}
	}
	for _, d := range o.icclData(ctx, tenant, "allocdiff", date) {
		v.DiffFile = strings.TrimPrefix(v.DiffFile+", "+d.File, ", ")
		for _, r := range d.Rows {
			al, rp := decOr0(r["allocated"]), decOr0(r["reported"])
			df := decOr0(r["diff"])
			if df.IsZero() {
				df = al.Sub(rp)
			}
			v.Diffs = append(v.Diffs, ICCLDiffRow{Segment: d.Segment, UCC: r["ucc"], Name: cs[r["ucc"]].Name, Allocated: al, Reported: rp, Diff: df})
		}
	}
	if pd, err := o.preDeliveryRows(ctx, tenant, set, date); err == nil && pd != nil {
		sortPreDelivery(pd)
		v.PreDelivery = pd
	}
	v.Limit = o.icclTradingLimit(ctx, tenant, v.Margins, date)
	c := o.RiskLimitConfig(ctx, tenant).cutoffs("ICCL")
	v.Cutoffs = payInCutoffsView{c.Funds, c.Securities, c.Auction, c.AuctionInstr, intShortDue}
	v.Accounts = o.icclAccounts(ctx, tenant, set)
	if len(v.Layouts) > 0 {
		v.Notes = append(v.Notes, "ICCL has not published the layouts of these files: each is built from the notice named beside it. Confirm the field order and file name against ICCL's file description before the first upload, and tell your Ledgerline provider what differs.")
	}
	return v, nil
}

const cICCLRun = "icclrun" // date: when the day's ICCL checks last ran

// icclTick runs the ICCL checks for a firm that clears there, once an
// hour from 18:00 (after the day's margins are known): the pre-delivery
// margins to collect and the trading limit. It does nothing for any other
// firm.
func (o *Ops) icclTick(ctx context.Context, tenant string, now time.Time) {
	now = now.In(ist)
	if now.Format("15:04") < "18:00" {
		return
	}
	if on, _ := o.icclSelf(ctx, tenant); !on {
		return
	}
	set, err := o.Settings(ctx, tenant)
	today := now.Format("2006-01-02")
	if err != nil || !set.IsBusinessDay(today) {
		return
	}
	last, _, _ := docs.Get[struct{ At string }](ctx, o.Docs, tenant, cICCLRun, today)
	if t, err := time.Parse(time.RFC3339, last.At); err == nil && now.Sub(t) < time.Hour {
		return
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cICCLRun, today, struct{ At string }{now.Format(time.RFC3339)}, SystemActor)
	o.RunICCLChecks(ctx, tenant, today)
}

// RunICCLChecks raises the date's ICCL work items; it returns how many.
func (o *Ops) RunICCLChecks(ctx context.Context, tenant, date string) int {
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return 0
	}
	n := o.raisePreDelivery(ctx, tenant, set, date)
	ms, _ := o.icclMargins(ctx, tenant, set, date)
	t := o.icclTradingLimit(ctx, tenant, ms, date)
	if t.Status != "OK" {
		o.raiseTradingLimit(ctx, tenant, t, date)
		n++
	}
	return n
}
