package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// ---- the insider trading code: the rest of Schedule C ----
//
// The SEBI (Prohibition of Insider Trading) Regulations 2015 ask an
// intermediary's code of conduct for more than pre-clearance (hrcomp.go):
//
//   - reg. 9(4): designated persons are specified by the board with the
//     compliance officer, on their role, access to UPSI and seniority
//     (support staff with UPSI access included). The firm writes the
//     criteria, records the board's approval (date and resolution) and
//     the ground for each designated person.
//   - reg. 9A and 3(5): a record of the persons UPSI is shared with, with
//     their PAN or other identifier and whether they were given notice to
//     keep it confidential. Entries are never changed, only added.
//   - Schedule C cl. 6: with each pre-clearance, the applicant's
//     declaration that they hold no UPSI (hrcomp.go).
//   - cl. 7: a pre-cleared trade is done within the code's period, at most
//     seven trading days; after it a fresh pre-clearance is needed.
//     Ledgerline counts trading days on the exchange calendar, the day of
//     approval being the first when it is a trading day (the shorter,
//     safer reading).
//   - cl. 8: no contra trade within six months (two months for mutual
//     fund units) unless the compliance officer relaxes it in writing
//     (hrcomp.go). Profit from a contra trade goes to SEBI's IPEF: the
//     firm's step.
//   - cl. 9: formats for the post-trade report and the report of a
//     decision not to trade after pre-clearance.
//   - cl. 12: each designated person discloses every year, and on change,
//     the names and PAN (or other identifier) of immediate relatives and
//     of persons with whom they have a material financial relationship,
//     and the phone numbers they use; and once, their educational
//     institutions and past employers.

const (
	cPITDiscl = "hr_pitdiscl"
	cUPSI     = "hr_upsi"
)

// NoUPSIDeclaration is the declaration with each pre-clearance.
const NoUPSIDeclaration = "I do not hold any unpublished price sensitive information about this security, and I will not trade if I come to hold any before the trade."

// DesignationGrounds are the grounds of reg. 9(4).
var DesignationGrounds = []string{"Role or function", "Access to UPSI", "Seniority (CEO and up to two levels below)", "Support staff with UPSI access", "Promoter"}

// BoardApproval is the board's (or the proprietor's) approval of the
// designated-person criteria.
type BoardApproval struct {
	Date       string `json:"date"`
	Resolution string `json:"resolution"` // resolution or minute reference
	By         string `json:"by"`
	At         string `json:"at"`
}

// PreClearReport is the post-trade report, or the report of a decision
// not to trade.
type PreClearReport struct {
	Traded    bool         `json:"traded"`
	TradeDate string       `json:"trade_date,omitempty"`
	Qty       int64        `json:"qty,omitempty"`
	Price     money.Amount `json:"price,omitempty"`  // average price per share, paise
	Reason    string       `json:"reason,omitempty"` // why not traded
	By        string       `json:"by"`
	At        string       `json:"at"`
	Late      bool         `json:"late,omitempty"` // traded after the pre-clearance ran out
}

// PITPerson is a relative or a person with a material financial
// relationship.
type PITPerson struct {
	Name     string   `json:"name"`
	Relation string   `json:"relation"`
	PAN      string   `json:"pan,omitempty"`
	OtherID  string   `json:"other_id,omitempty"` // when there is no PAN
	Phones   []string `json:"phones,omitempty"`
}

// PITDisclosure is a designated person's yearly disclosure.
type PITDisclosure struct {
	ID            string      `json:"id"` // code|FY
	Employee      string      `json:"employee"`
	Period        string      `json:"period"`
	Relatives     []PITPerson `json:"relatives"`
	Financial     []PITPerson `json:"financial"` // persons with a material financial relationship
	Phones        []string    `json:"phones"`    // the person's own
	Education     []string    `json:"education,omitempty"`
	PastEmployers []string    `json:"past_employers,omitempty"`
	NoRelatives   bool        `json:"no_relatives,omitempty"`
	NoFinancial   bool        `json:"no_financial,omitempty"`
	DeclaredOn    string      `json:"declared_on"`
	DeclaredBy    string      `json:"declared_by"`
}

// UPSIShare is one entry of the UPSI sharing record.
type UPSIShare struct {
	ID         string `json:"id"`
	Subject    string `json:"subject"` // the company or security
	Nature     string `json:"nature"`  // what the information is
	SharedWith string `json:"shared_with"`
	Org        string `json:"org,omitempty"`
	PAN        string `json:"pan,omitempty"`
	OtherID    string `json:"other_id,omitempty"`
	Purpose    string `json:"purpose"`
	Notice     bool   `json:"notice"` // given notice to keep it confidential, or under a confidentiality agreement
	SharedOn   string `json:"shared_on"`
	SharedBy   string `json:"shared_by"`
	At         string `json:"at"`
}

// contraDays is the code's contra period for the kind of trade.
func (p OwnTradingPolicy) contraDays(mf bool) int {
	if mf {
		if p.ContraDaysMF > 0 {
			return p.ContraDaysMF
		}
		return 60
	}
	return p.ContraDays
}

// preClearValidTill is the last day a pre-clearance given on day may be
// traded on: n trading days, the day itself the first when it is one.
func preClearValidTill(set Settings, day string, n int) string {
	if n <= 0 {
		n = 7
	}
	if hol, _ := set.IsHoliday(day); isTradingDay(set, day) && !hol {
		return addTradingDays(set, day, n-1)
	}
	return addTradingDays(set, day, n)
}

// ReportPreClearance records what was done with an approved
// pre-clearance: the trade (date, quantity, price) or the decision not to
// trade, with why. The person themselves, or compliance.
func (o *Ops) ReportPreClearance(ctx context.Context, tenant, actor, id string, rep PreClearReport) (PreClearance, error) {
	if err := o.needModule(ctx, tenant, ModHRMS); err != nil {
		return PreClearance{}, err
	}
	pc, _, err := docs.Get[PreClearance](ctx, o.Docs, tenant, cPreClear, id)
	if err != nil {
		return PreClearance{}, ErrNotFound
	}
	if _, _, err := o.selfOr(ctx, tenant, actor, pc.Employee); err != nil {
		return PreClearance{}, err
	}
	if rep.Traded {
		if !validDay(rep.TradeDate) || rep.Qty <= 0 || rep.Price <= 0 {
			return PreClearance{}, invalid("give the trade date, the quantity and the price")
		}
		if rep.Qty > pc.Qty {
			return PreClearance{}, invalid("the quantity traded is more than was pre-cleared (%d)", pc.Qty)
		}
		if rep.TradeDate > o.today() {
			return PreClearance{}, invalid("the trade date is in the future")
		}
		rep.Reason = ""
	} else {
		rep.Reason = strings.TrimSpace(rep.Reason)
		if len(rep.Reason) < 5 {
			return PreClearance{}, invalid("say why you decided not to trade")
		}
		rep.TradeDate, rep.Qty, rep.Price = "", 0, 0
	}
	rep.By, rep.At = actor, o.now().Format(time.RFC3339)
	pc, err = docs.Update(ctx, o.Docs, tenant, cPreClear, id, actor, func(x *PreClearance) error {
		if x.Status != "Approved" {
			return fmt.Errorf("%w: only an approved pre-clearance is reported on", ErrState)
		}
		if x.Report != nil {
			return fmt.Errorf("%w: %s is already reported", ErrState, x.ID)
		}
		if rep.Traded && (rep.TradeDate > x.ValidTill || rep.TradeDate < x.At[:10]) {
			rep.Late = true
		}
		x.Report = &rep
		return nil
	})
	if err != nil {
		return pc, err
	}
	o.closeWork(ctx, tenant, actor, wHR, "preclear-report:"+pc.ID, "reported")
	if rep.Late {
		o.raiseWork(ctx, tenant, wHR, "preclear-late:"+pc.ID, fmt.Sprintf("%s traded %s outside its pre-clearance (%s)", pc.Name, pc.Security, pc.ID),
			fmt.Sprintf("Traded on %s; the pre-clearance was valid till %s. A trade outside the period needs a fresh pre-clearance (Schedule C cl. 7): review it under the code.", fmtDay(rep.TradeDate), fmtDay(pc.ValidTill)), "", RoleCompliance, "people", o.today())
	}
	o.log(ctx, tenant, actor, "hr.preclear.reported", pc.ID, map[string]any{"traded": rep.Traded, "trade_date": rep.TradeDate, "qty": rep.Qty, "late": rep.Late})
	return pc, nil
}

// DeclarePITDetails records a designated person's yearly disclosure of
// relatives, persons with a material financial relationship and phone
// numbers (Schedule C cl. 12). The person themselves, HR or compliance.
func (o *Ops) DeclarePITDetails(ctx context.Context, tenant, actor, code string, d PITDisclosure) (PITDisclosure, error) {
	if err := o.needModule(ctx, tenant, ModHRMS); err != nil {
		return d, err
	}
	e, _, err := o.selfOr(ctx, tenant, actor, code)
	if err != nil {
		return d, err
	}
	clean := func(xs []PITPerson, what string) ([]PITPerson, error) {
		out := []PITPerson{}
		for _, x := range xs {
			x.Name, x.Relation = strings.TrimSpace(x.Name), strings.TrimSpace(x.Relation)
			x.PAN, x.OtherID = strings.ToUpper(strings.TrimSpace(x.PAN)), strings.TrimSpace(x.OtherID)
			if x.Name == "" {
				continue
			}
			if x.PAN != "" && !rePAN.MatchString(x.PAN) {
				return nil, invalid("%s: %q is not a PAN", x.Name, x.PAN)
			}
			if x.PAN == "" && x.OtherID == "" {
				return nil, invalid("%s (%s): give the PAN, or another identifier when there is none", x.Name, what)
			}
			out = append(out, x)
		}
		return out, nil
	}
	if d.Relatives, err = clean(d.Relatives, "relative"); err != nil {
		return d, err
	}
	if d.Financial, err = clean(d.Financial, "material financial relationship"); err != nil {
		return d, err
	}
	if len(d.Relatives) == 0 && !d.NoRelatives {
		return d, invalid("list your immediate relatives, or declare that you have none")
	}
	if len(d.Financial) == 0 && !d.NoFinancial {
		return d, invalid("list the persons you have a material financial relationship with, or declare that there are none")
	}
	var ph []string
	for _, p := range d.Phones {
		if p = strings.TrimSpace(p); p != "" {
			ph = append(ph, p)
		}
	}
	if len(ph) == 0 {
		return d, invalid("give the phone and mobile numbers you use")
	}
	d.Phones = ph
	d.Period = finYear(o.today())
	d.Employee, d.ID, d.DeclaredOn, d.DeclaredBy = e.Code, e.Code+"|"+d.Period, o.today(), actor
	// Education and past employers are disclosed once: kept from an
	// earlier disclosure when not given again.
	if len(d.Education) == 0 || len(d.PastEmployers) == 0 {
		if ds, _ := docs.List[PITDisclosure](ctx, o.Docs, tenant, cPITDiscl, e.Code+"|"); len(ds) > 0 {
			for _, x := range ds {
				if len(d.Education) == 0 {
					d.Education = x.Education
				}
				if len(d.PastEmployers) == 0 {
					d.PastEmployers = x.PastEmployers
				}
			}
		}
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cPITDiscl, d.ID, d, actor); err != nil {
		return d, err
	}
	o.closeWork(ctx, tenant, actor, wHR, e.Code+":pit-discl:"+d.Period, "disclosed")
	o.log(ctx, tenant, actor, "hr.pit.disclosed", e.Code, map[string]any{"period": d.Period, "relatives": len(d.Relatives), "financial": len(d.Financial)})
	return d, nil
}

// RecordPITBoardApproval records the board's approval of the designated
// person criteria. The compliance officer (the board decides; Ledgerline
// keeps the record).
func (o *Ops) RecordPITBoardApproval(ctx context.Context, tenant, actor, criteria, date, resolution string) (OwnTradingPolicy, error) {
	if err := o.needModule(ctx, tenant, ModHRMS); err != nil {
		return OwnTradingPolicy{}, err
	}
	if err := o.require(ctx, tenant, actor, "Recording the board's approval of designated persons", RoleCompliance); err != nil {
		return OwnTradingPolicy{}, err
	}
	criteria, resolution = strings.TrimSpace(criteria), strings.TrimSpace(resolution)
	if len(criteria) < 20 {
		return OwnTradingPolicy{}, invalid("write the criteria: which roles, access to UPSI and seniority make a person designated")
	}
	if !validDay(date) || date > o.today() {
		return OwnTradingPolicy{}, invalid("give the date of the board meeting (not in the future)")
	}
	if resolution == "" {
		return OwnTradingPolicy{}, invalid("give the resolution or minute reference")
	}
	p := o.hrPolicy(ctx, tenant)
	p.OwnTrading.DesignationCriteria = trim(criteria, 4000)
	p.OwnTrading.BoardApproval = &BoardApproval{Date: date, Resolution: trim(resolution, 200), By: actor, At: o.now().Format(time.RFC3339)}
	p.UpdatedBy, p.UpdatedAt = actor, o.now().Format(time.RFC3339)
	if err := docs.Upsert(ctx, o.Docs, tenant, cHRPolicy, "main", p, actor); err != nil {
		return OwnTradingPolicy{}, err
	}
	o.log(ctx, tenant, actor, "hr.pit.board_approval", "own-trading", map[string]any{"date": date, "resolution": resolution})
	return p.OwnTrading, nil
}

// SetDesignated marks a person designated (or not) with the ground. The
// compliance officer.
func (o *Ops) SetDesignated(ctx context.Context, tenant, actor, code string, on bool, ground string) (Employee, error) {
	if err := o.needModule(ctx, tenant, ModHRMS); err != nil {
		return Employee{}, err
	}
	if err := o.require(ctx, tenant, actor, "Designating persons under the insider trading code", RoleCompliance); err != nil {
		return Employee{}, err
	}
	if on && !contains(DesignationGrounds, ground) {
		return Employee{}, invalid("the ground is one of %s", strings.Join(DesignationGrounds, ", "))
	}
	e, err := docs.Update(ctx, o.Docs, tenant, cEmployee, strings.ToUpper(code), actor, func(x *Employee) error {
		x.Designated = on
		x.DesignatedFor = map[bool]string{true: ground}[on]
		x.UpdatedBy, x.UpdatedAt = actor, o.now().Format(time.RFC3339)
		return nil
	})
	if err != nil {
		return e, err
	}
	o.log(ctx, tenant, actor, "hr.pit.designated", e.Code, map[string]any{"on": on, "ground": ground})
	return e, nil
}

// RecordUPSIShare adds an entry to the UPSI sharing record. The
// compliance officer; entries are never changed.
func (o *Ops) RecordUPSIShare(ctx context.Context, tenant, actor string, u UPSIShare) (UPSIShare, error) {
	if err := o.needModule(ctx, tenant, ModHRMS); err != nil {
		return u, err
	}
	if err := o.require(ctx, tenant, actor, "Keeping the UPSI sharing record", RoleCompliance); err != nil {
		return u, err
	}
	u.Subject, u.Nature, u.SharedWith, u.Purpose = strings.TrimSpace(u.Subject), strings.TrimSpace(u.Nature), strings.TrimSpace(u.SharedWith), strings.TrimSpace(u.Purpose)
	u.PAN, u.OtherID, u.Org = strings.ToUpper(strings.TrimSpace(u.PAN)), strings.TrimSpace(u.OtherID), strings.TrimSpace(u.Org)
	switch {
	case u.Subject == "" || u.Nature == "" || u.SharedWith == "" || u.Purpose == "":
		return u, invalid("give the company, what the information is, with whom it was shared and why")
	case u.PAN != "" && !rePAN.MatchString(u.PAN):
		return u, invalid("%q is not a PAN", u.PAN)
	case u.PAN == "" && u.OtherID == "":
		return u, invalid("give the recipient's PAN, or another identifier when there is none")
	case !validDay(u.SharedOn) || u.SharedOn > o.today():
		return u, invalid("give the date it was shared (not in the future)")
	}
	n, err := o.next(ctx, tenant, "hr-upsi", actor)
	if err != nil {
		return u, err
	}
	u.ID, u.SharedBy, u.At = fmt.Sprintf("UPSI-%05d", n), actor, o.now().Format(time.RFC3339)
	if _, err := docs.Put(ctx, o.Docs, tenant, cUPSI, u.ID, u, 0, actor); err != nil {
		return u, err
	}
	o.log(ctx, tenant, actor, "hr.upsi.shared", u.ID, map[string]any{"subject": u.Subject, "with": u.SharedWith, "notice": u.Notice})
	if !u.Notice {
		o.raiseWork(ctx, tenant, wHR, "upsi-notice:"+u.ID, "UPSI shared without a confidentiality notice ("+u.ID+")",
			"Give "+u.SharedWith+" notice to keep the information confidential (reg. 3(4)) and record it.", "", RoleCompliance, "people", o.today())
	}
	return u, nil
}

// pitView adds the code's other records to the own-trading view.
func (o *Ops) pitView(ctx context.Context, tenant, actor, only string, v *OwnTradingView) {
	v.Disclosures, v.MissingDiscl, v.NoBasis, v.NoReport = []PITDisclosure{}, []string{}, []string{}, []string{}
	v.NoUPSIText, v.DesignatedFor = NoUPSIDeclaration, DesignationGrounds
	fy := finYear(o.today())
	ds, _ := docs.List[PITDisclosure](ctx, o.Docs, tenant, cPITDiscl, "")
	have := map[string]bool{}
	for _, d := range ds {
		if only == "" || d.Employee == only {
			v.Disclosures = append(v.Disclosures, d)
		}
		if d.Period == fy {
			have[d.Employee] = true
		}
	}
	for _, p := range v.PreClear {
		if p.Status == "Approved" && p.Report == nil && p.ValidTill != "" && p.ValidTill < o.today() {
			v.NoReport = append(v.NoReport, p.ID)
		}
	}
	if only != "" {
		return
	}
	es, _ := o.employees(ctx, tenant)
	for _, e := range es {
		if e.Status == EmpLeft || !e.Designated {
			continue
		}
		if !have[e.Code] {
			v.MissingDiscl = append(v.MissingDiscl, e.Code)
		}
		if e.DesignatedFor == "" {
			v.NoBasis = append(v.NoBasis, e.Code)
		}
	}
	if err := o.require(ctx, tenant, actor, "Seeing the UPSI sharing record", RoleCompliance); err == nil {
		us, _ := docs.List[UPSIShare](ctx, o.Docs, tenant, cUPSI, "")
		sort.Slice(us, func(i, j int) bool { return us[i].ID > us[j].ID })
		v.UPSI = us
	}
}

// pitTick, once a day: an approved pre-clearance that ran out with no
// post-trade or no-trade report, and a designated person without this
// year's disclosure once the annual declaration month is over, go to
// compliance.
func (o *Ops) pitTick(ctx context.Context, tenant string, now time.Time) {
	today := now.In(ist).Format("2006-01-02")
	last, _, _ := docs.GetOr(ctx, o.Docs, tenant, cHRTick, "pit", hrTickDoc{})
	if last.Date >= today {
		return
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cHRTick, "pit", hrTickDoc{Date: today}, SystemActor)
	ps, _ := docs.List[PreClearance](ctx, o.Docs, tenant, cPreClear, "")
	for _, p := range ps {
		if p.Status == "Approved" && p.Report == nil && p.ValidTill != "" && p.ValidTill < today {
			o.raiseWork(ctx, tenant, wHR, "preclear-report:"+p.ID, fmt.Sprintf("No post-trade report on %s (%s, %s)", p.ID, p.Name, p.Security),
				fmt.Sprintf("The pre-clearance ran out on %s. Ask %s to report the trade, or the decision not to trade (Schedule C cl. 9).", fmtDay(p.ValidTill), p.Name), "", RoleCompliance, "people", today)
		}
	}
	pol := o.hrPolicy(ctx, tenant).OwnTrading
	n := now.In(ist)
	if int(n.Month()) <= pol.AnnualDueMonth || int(n.Month()) > pol.AnnualDueMonth+1 {
		return // the month after the annual declaration is due
	}
	fy := finYear(today)
	ds, _ := docs.List[PITDisclosure](ctx, o.Docs, tenant, cPITDiscl, "")
	have := map[string]bool{}
	for _, d := range ds {
		if d.Period == fy {
			have[d.Employee] = true
		}
	}
	es, _ := o.employees(ctx, tenant)
	for _, e := range es {
		if e.Status != EmpLeft && e.Designated && !have[e.Code] {
			o.raiseWork(ctx, tenant, wHR, e.Code+":pit-discl:"+fy, fmt.Sprintf("Relatives disclosure %s due from %s (%s)", fy, e.Name, e.Code),
				"Each year a designated person discloses immediate relatives and persons with a material financial relationship (names and PAN) and the phone numbers used (Schedule C cl. 12).", "", RoleCompliance, "people", today)
		}
	}
}
