package ops

import (
	"context"
	"fmt"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// ---- NSE eIPO: sovereign gold bonds (SGB) ----
//
// NSE's SGB master (GET sgbmaster) gives each tranche: bidding dates, lot
// size (grams), minimum and maximum quantity, minPrice (the online price)
// and maxPrice (the offline price), the T1 modification window and the
// last day incomplete applications may be completed. An application (POST
// sgb/add, a list) is a quantity of grams at the online or offline price,
// in demat (D, with the client's depository account) or as a certificate
// (P, with the client code). NSE gives the application and order numbers;
// status is fetched by order number (sgb/fetch).
//
// RBI needs investor details that a demat application does not carry: GET
// sgb/incomplete/{symbol} lists applications with the codes of what is
// missing (301 date of birth, 302 state, 303-305 bank IFSC, 306-308 bank
// account, 309-312 second and third holders, 313-316 guardian), and POST
// sgb/incomplete/update sends them. Ledgerline raises these as a work item
// for an Ops maker, fills in what the client master has (date of birth,
// state, bank account) and sends what the maker confirms. The protocol has
// no nominee field: nomination is not sent through the API.
//
// The money is collected by the member and paid to NSCCL (clearingStatus
// FP, FS, then FR funds received, RS sent to RBI, RA allotted by RBI), so
// it is checked against and held on the client's ledger as for NCB.
//
// RBI's scheme limits a subscription to 4 kg a financial year for an
// individual or HUF and 20 kg for a trust (and similar bodies); Ledgerline
// counts the client's own orders through the firm, not those elsewhere.

// sgbIncompleteCodes are the protocol's list of incomplete codes.
var sgbIncompleteCodes = map[int]string{
	301: "first holder's date of birth", 302: "first holder's state", 303: "bank IFSC", 304: "bank IFSC (11 characters)", 305: "bank IFSC (format)",
	306: "bank account", 307: "bank account (at most 16 digits)", 308: "bank account (digits only)", 309: "second holder's PAN", 310: "second holder's date of birth",
	311: "third holder's PAN", 312: "third holder's date of birth", 313: "guardian's first name", 314: "guardian's relation", 315: "guardian's date of birth", 316: "guardian's PAN",
}

func sgbIncompleteText(codes []int) string {
	var out []string
	for _, c := range codes {
		out = append(out, firstOf(sgbIncompleteCodes[c], fmt.Sprint(c)))
	}
	return strings.Join(out, ", ")
}

// sgbLimit is RBI's yearly limit in grams for the client's type, or 0 when
// the type may not subscribe.
func sgbLimit(c Client) int64 {
	switch c.Type {
	case "", "Individual", "HUF":
		return 4000
	case "Trust":
		return 20000
	}
	return 0
}

// sgbCheck checks an SGB application and works out the money to hold.
func (o *Ops) sgbCheck(ctx context.Context, tenant string, iss IPOIssue, c Client, x *EIPOOrder, in EIPOOrderInput) error {
	limit := sgbLimit(c)
	if limit == 0 {
		return invalid("sovereign gold bonds are for resident individuals, HUFs, trusts, universities and charitable institutions; %s is a %s account", c.UCC, c.Type)
	}
	if err := qtyCheck(iss, x.Qty, "grams"); err != nil {
		return err
	}
	x.PriceKind, x.Price = "Offline", iss.PriceHigh
	if in.Online {
		x.PriceKind, x.Price = "Online", iss.PriceLow
	}
	if x.Price.Sign() <= 0 {
		return fmt.Errorf("%w: the %s issue price of %s is not announced yet: set it on the issue when RBI announces it", ErrState, strings.ToLower(x.PriceKind), iss.Name)
	}
	x.Amount = x.Price.MulInt(x.Qty).Round(money.UnitPaisa)
	// The year's subscriptions through the firm.
	fy := finYear(iss.Open)
	opens := map[string]string{}
	if is, err := docs.List[IPOIssue](ctx, o.Docs, tenant, cIPOIssue, ""); err == nil {
		for _, i := range is {
			opens[i.ID] = i.Open
		}
	}
	used := int64(0)
	all, _ := docs.List[EIPOOrder](ctx, o.Docs, tenant, cEIPOOrder, "")
	for _, y := range all {
		if y.Product == IssueSGB && y.ID != x.ID && y.live() && strings.EqualFold(y.PAN, c.PAN) && finYear(opens[y.IssueID]) == fy {
			used += y.Qty
		}
	}
	if used+x.Qty > limit {
		return invalid("%s may subscribe up to %d grams of sovereign gold bonds in %s; %d already applied for through the firm", c.UCC, limit, fy, used)
	}
	x.Physical = in.Physical
	if x.Physical {
		x.Depository, x.DPID, x.BenID, x.Demat = "", "", "", ""
		return nil
	}
	return o.setDemat(ctx, tenant, x, c, in.Demat, true)
}

// sgbKYCFor is the investor detail for an incomplete application, filled in
// from the client master where it has it.
func (o *Ops) sgbKYCFor(ctx context.Context, tenant string, x EIPOOrder) SGBKYC {
	k := SGBKYC{Symbol: x.Symbol, OrderNo: x.OrderNo, Codes: x.Incomplete}
	if c, err := o.Client(ctx, tenant, x.UCC); err == nil {
		k.DOB, k.State, k.BankIFSC, k.BankAccount = c.DOB, c.State, strings.ToUpper(c.Bank.IFSC), c.Bank.Account
	}
	return k
}

// sgbNeeds lists what each incomplete code needs filled.
func sgbNeeds(k SGBKYC) error {
	need := func(v, what string) error {
		if strings.TrimSpace(v) == "" {
			return invalid("NSE needs the %s", what)
		}
		return nil
	}
	date := func(v, what string) error {
		if v != "" && !validDate(v) {
			return invalid("the %s is a date (yyyy-mm-dd)", what)
		}
		return nil
	}
	pan := func(v, what string) error {
		if v != "" && !rePAN.MatchString(v) {
			return invalid("the %s is not a valid PAN", what)
		}
		return nil
	}
	for _, c := range k.Codes {
		var err error
		switch c {
		case 301:
			err = need(k.DOB, "first holder's date of birth")
		case 302:
			err = need(k.State, "first holder's state")
		case 303, 304, 305:
			err = need(k.BankIFSC, "bank IFSC")
		case 306, 307, 308:
			err = need(k.BankAccount, "bank account number")
		case 309:
			err = need(k.SecondPAN, "second holder's PAN")
		case 310:
			err = need(k.SecondDOB, "second holder's date of birth")
		case 311:
			err = need(k.ThirdPAN, "third holder's PAN")
		case 312:
			err = need(k.ThirdDOB, "third holder's date of birth")
		case 313:
			err = need(k.GuardianName, "guardian's first name")
		case 314:
			err = need(k.GuardianRel, "guardian's relation")
		case 315:
			err = need(k.GuardianDOB, "guardian's date of birth")
		case 316:
			err = need(k.GuardianPAN, "guardian's PAN")
		}
		if err != nil {
			return err
		}
	}
	for _, e := range []error{date(k.DOB, "date of birth"), date(k.SecondDOB, "second holder's date of birth"), date(k.ThirdDOB, "third holder's date of birth"), date(k.GuardianDOB, "guardian's date of birth"),
		pan(k.SecondPAN, "second holder's PAN"), pan(k.ThirdPAN, "third holder's PAN"), pan(k.GuardianPAN, "guardian's PAN")} {
		if e != nil {
			return e
		}
	}
	switch {
	case k.BankIFSC != "" && !reIFSC.MatchString(k.BankIFSC):
		return invalid("%s is not an IFSC (11 characters, four letters first)", k.BankIFSC)
	case k.BankAccount != "" && (len(k.BankAccount) > 16 || strings.Trim(k.BankAccount, "0123456789") != ""):
		return invalid("the bank account number is digits only, at most 16 (the protocol's limit)")
	case len(k.State) > 50 || len(k.GuardianName) > 250 || len(k.GuardianRel) > 10:
		return invalid("a detail is longer than the protocol allows (state 50, guardian's name 250, relation 10 characters)")
	}
	return nil
}

// CompleteSGBApplication sends the investor details NSE lists as missing
// for an SGB application (sgb/incomplete/update).
func (o *Ops) CompleteSGBApplication(ctx context.Context, tenant, actor, id string, k SGBKYC) (EIPOOrder, error) {
	if err := o.require(ctx, tenant, actor, "Sending NCB, SGB and tender orders to NSE", RoleMaker, RoleChecker); err != nil {
		return EIPOOrder{}, err
	}
	conn, err := o.eipoUsable(ctx, tenant)
	if err != nil {
		return EIPOOrder{}, err
	}
	x, _, err := docs.Get[EIPOOrder](ctx, o.Docs, tenant, cEIPOOrder, id)
	if err != nil {
		return x, fmt.Errorf("%w: no order %s", ErrNotFound, id)
	}
	if x.Product != IssueSGB || x.OrderNo == "" {
		return x, invalid("%s is not an SGB application NSE holds", id)
	}
	iss, err := o.IPOIssueByID(ctx, tenant, x.IssueID)
	if err != nil {
		return x, err
	}
	now := o.now()
	if to := productOr(iss).IncompleteTo; to != "" && now.In(ist).Format("2006-01-02") > to {
		return x, fmt.Errorf("%w: incomplete applications could be completed until %s", ErrState, fmtDay(to))
	}
	k.Symbol, k.OrderNo = iss.Symbol, x.OrderNo
	k.Codes = x.Incomplete
	k.BankIFSC, k.SecondPAN, k.ThirdPAN, k.GuardianPAN = strings.ToUpper(strings.TrimSpace(k.BankIFSC)), strings.ToUpper(strings.TrimSpace(k.SecondPAN)), strings.ToUpper(strings.TrimSpace(k.ThirdPAN)), strings.ToUpper(strings.TrimSpace(k.GuardianPAN))
	k.BankAccount, k.State = strings.TrimSpace(k.BankAccount), strings.TrimSpace(k.State)
	if err := sgbNeeds(k); err != nil {
		return x, err
	}
	// Only what NSE lists goes (with what the maker added).
	if err := eipoProducts.SGBCompleteDetails(ctx, conn.cfg, k); err != nil {
		return x, err
	}
	sent := sgbIncompleteText(k.Codes)
	x, err = docs.Update(ctx, o.Docs, tenant, cEIPOOrder, id, actor, func(y *EIPOOrder) error {
		y.Incomplete, y.KYCSent = nil, now.Format(time.RFC3339)
		y.event(now, actor, "investor details sent to NSE (%s)", firstOf(sent, "as entered"))
		return nil
	})
	if err != nil {
		return x, err
	}
	o.productWork(ctx, tenant, iss, now)
	o.log(ctx, tenant, actor, "ipo.sgb_details_sent", id, map[string]any{"codes": fmt.Sprint(k.Codes)})
	return x, nil
}
