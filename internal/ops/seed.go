package ops

import (
	"context"
	"errors"
	"fmt"

	"ledgerline/internal/charges"
	"ledgerline/internal/docs"
	"ledgerline/internal/money"
	"ledgerline/internal/sample"
)

// DevUsers are the users created for a development tenant.
var DevUsers = []User{
	{ID: "priya.nair", Name: "Priya Nair", Role: RoleMaker, Active: true},
	{ID: "farhan.khan", Name: "Farhan Khan", Role: RoleChecker, Active: true},
	{ID: "anita.rao", Name: "Anita Rao", Role: RoleRisk, Active: true},
	{ID: "vikram.shah", Name: "Vikram Shah", Role: RoleCompliance, Active: true},
	{ID: "admin", Name: "Administrator", Role: RoleAdmin, Active: true},
}

// SeedDev creates users and settings for a development tenant if absent.
func (o *Ops) SeedDev(ctx context.Context, tenant string) error {
	us, err := o.Users(ctx, tenant)
	if err != nil {
		return err
	}
	if len(us) == 0 {
		for _, u := range DevUsers {
			if err := docs.Upsert(ctx, o.Docs, tenant, cUser, u.ID, u, "system"); err != nil {
				return err
			}
			if err := o.SetPassword(ctx, tenant, "system", u.ID, DevPassword, false); err != nil {
				return err
			}
		}
	}
	if _, _, err := docs.Get[Settings](ctx, o.Docs, tenant, cSettings, "main"); errors.Is(err, docs.ErrNotFound) {
		s := DefaultSettings()
		s.Firm = "Bhairav Securities Pvt Ltd (sample)"
		s.SEBIReg = "INZ000XXXXXX (sample)"
		s.MemberCodes = map[string]string{"NSE": "90311", "BSE": "6127", "MCX": "56204"}
		s.ComplianceEmail = "compliance@bhairav.example"
		s.RASMonthly, s.RASQuarterly = "2026-11-06", "2027-01-01"
		s.Holidays = []string{"2026-12-25"}
		s.TwoFactor = "optional"               // development: sign in with password only
		s.GatewaySecret = "dev-gateway-secret" // sample only
		s.FirmState, s.FirmGSTIN = "Maharashtra", "27AAACB1234C1Z5"
		s.PayoutDebitAccount = "50200012345678"
		s.Calendar = []CalendarItem{
			{"2026-10-16", "Client funds and securities balance upload", "Accounts"},
			{"2026-10-16", "Weekly holding statements to clients", "Operations"},
			{"2026-10-19", "Margin collection file for 16-Oct", "Risk"},
			{"2026-10-31", "Half-yearly net worth certificate", "Compliance"},
			{"2026-11-06", "Monthly running account settlement", "Accounts"},
			{"2026-11-07", "Investor grievance summary", "Compliance"},
			{"2026-11-30", "CSCRF compliance evidence", "Technology"},
			{"2027-01-01", "Quarterly running account settlement", "Accounts"},
		}
		if err := docs.Upsert(ctx, o.Docs, tenant, cSettings, "main", s, "system"); err != nil {
			return err
		}
	}
	return nil
}

// DevPassword is every development user's password.
const DevPassword = "ledgerline-demo"

// TradeIngester is the core file ingestion used for trade files.
type TradeIngester func(ctx context.Context, tenant, actor, source, name string, data []byte) error

// LoadSampleDay loads a generated day through the normal file loaders.
func (o *Ops) LoadSampleDay(ctx context.Context, tenant, actor string, day sample.Day, later bool, trades TradeIngester) (int, error) {
	n := 0
	for _, f := range day.Files {
		if f.Later != later {
			continue
		}
		var err error
		if IsMasterSource(f.Source) {
			_, err = o.LoadFile(ctx, tenant, actor, f.Source, f.Date, f.Name, f.Data)
		} else {
			err = trades(ctx, tenant, actor, f.Source, f.Name, f.Data)
		}
		if err != nil {
			return n, fmt.Errorf("%s: %w", f.Name, err)
		}
		n++
	}
	return n, nil
}

// ---- consecutive sample days (development) ----

const cDevSample = "devsample"

// SampleState records which generated days have been loaded.
type SampleState struct {
	Start  string   `json:"start"`
	Loaded []string `json:"loaded"`
}

// SampleState returns the loaded sample days.
func (o *Ops) SampleStatus(ctx context.Context, tenant string) (SampleState, error) {
	st, _, err := docs.GetOr(ctx, o.Docs, tenant, cDevSample, "state", SampleState{Loaded: []string{}})
	return st, err
}

// NextSampleDay is the date the next generated day would load for.
func (o *Ops) NextSampleDay(ctx context.Context, tenant string) (string, error) {
	st, err := o.SampleStatus(ctx, tenant)
	if err != nil || len(st.Loaded) == 0 {
		return "", err
	}
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return "", err
	}
	return sample.NextBusinessDay(st.Loaded[len(st.Loaded)-1], set.Holidays), nil
}

// LoadSampleFor loads the generated files for date. The first call starts
// the sample on that date with the full set (clients, balances, holdings,
// positions); each later call must be the next trading day, after the
// previous one is closed, and loads that day's market data, trade files and
// bank statement.
func (o *Ops) LoadSampleFor(ctx context.Context, tenant, actor, date string, trades TradeIngester) (int, error) {
	st, err := o.SampleStatus(ctx, tenant)
	if err != nil {
		return 0, err
	}
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return 0, err
	}
	k := 0
	if st.Start == "" {
		st.Start = date
	} else {
		for _, d := range st.Loaded {
			if d == date {
				return 0, fmt.Errorf("%w: the sample day %s is already loaded", ErrState, date)
			}
		}
		want := sample.NextBusinessDay(st.Loaded[len(st.Loaded)-1], set.Holidays)
		if date != want {
			return 0, invalid("sample days load in order; the next one is %s", want)
		}
		if err := o.DayOpenError(ctx, tenant, date); err != nil {
			return 0, err
		}
		k = len(st.Loaded)
		if k >= 20 {
			return 0, invalid("the sample runs for at most 20 trading days")
		}
	}
	day := sample.Days(st.Start, k+1, set.Holidays)[k]
	n, err := o.LoadSampleDay(ctx, tenant, actor, day, false, trades)
	if err != nil {
		return n, err
	}
	if k == 0 {
		if err := o.seedSampleBrokerage(ctx, tenant, date); err != nil {
			return n, fmt.Errorf("sample groups and plans: %w", err)
		}
	}
	st.Loaded = append(st.Loaded, date)
	return n, docs.Upsert(ctx, o.Docs, tenant, cDevSample, "state", st, actor)
}

// SampleRun is what an automatic sample day did.
type SampleRun struct {
	Date    string `json:"date"`
	Files   int    `json:"files"`
	Trades  int    `json:"trades"`
	Breaks  int    `json:"breaks"`
	Bills   int    `json:"bills"`
	MTM     int    `json:"carried_mtm"`
	Expiry  int    `json:"expiry"`
	Settled int    `json:"settled"`
	Notes   int    `json:"notes"`
	Rolled  int    `json:"rolled_positions"`
	NextDay string `json:"next_day"`
}

// RunSampleDay loads the next generated day and works it end to end the way
// operations would: reconcile, resolve the breaks, bill, settle derivatives
// and cash-market obligations, send contract notes and close the day, which
// rolls positions into the next day. The actor needs the Admin role.
func (o *Ops) RunSampleDay(ctx context.Context, tenant, actor, date string, trades TradeIngester) (SampleRun, error) {
	run := SampleRun{Date: date}
	if err := o.require(ctx, tenant, actor, "Running a sample day", RoleAdmin); err != nil {
		return run, err
	}
	var err error
	if run.Files, err = o.LoadSampleFor(ctx, tenant, actor, date, trades); err != nil {
		return run, err
	}
	rec, err := o.App.RunRecon(ctx, tenant, actor, date)
	if err != nil {
		return run, err
	}
	run.Trades, run.Breaks = rec.Result.ExchangeCount, len(rec.Result.Breaks)
	open, _ := o.App.Breaks.List(ctx, tenant, date, "OPEN")
	for _, b := range open {
		if _, err := o.App.ResolveBreak(ctx, tenant, actor, b.ID, "Checked against the exchange record; OMS corrected (sample run)"); err != nil {
			return run, err
		}
	}
	m, _, err := o.Market(ctx, tenant, date)
	if err != nil {
		return run, err
	}
	bill, err := o.App.RunBillingOpts(ctx, tenant, actor, date, m.SettlementPrices(), false)
	if err != nil {
		return run, err
	}
	run.Bills = len(bill.Posted)
	dv, err := o.SettleDerivatives(ctx, tenant, actor, date)
	if err != nil {
		return run, err
	}
	run.MTM, run.Expiry = len(dv.MTM), len(dv.Expiry)
	if run.Settled, err = o.SettleAllReady(ctx, tenant, actor, date); err != nil {
		return run, err
	}
	if run.Notes, err = o.GenerateNotes(ctx, tenant, actor, date); err != nil {
		return run, err
	}
	if _, _, err = o.SendNotes(ctx, tenant, actor, date, ""); err != nil {
		return run, err
	}
	st, err := o.CloseDay(ctx, tenant, actor, date, "Sample run: closed automatically")
	if err != nil {
		return run, err
	}
	run.Rolled, run.NextDay = st.Rolled, st.NextDay
	return run, nil
}

// seedSampleBrokerage puts the sample clients into groups and the groups on
// brokerage plans, so the sample shows plans and overrides at work.
func (o *Ops) seedSampleBrokerage(ctx context.Context, tenant, date string) error {
	cs, err := o.Clients(ctx, tenant)
	if err != nil {
		return err
	}
	funds, err := o.clientFunds(ctx, tenant, date, "")
	if err != nil {
		return err
	}
	staff := 0
	for _, c := range cs {
		var tags []string
		if funds[c.UCC].Dec().Cmp(money.MustDec("2000000")) > 0 {
			tags = append(tags, "HNI")
		}
		for _, s := range c.Segments {
			if s == "FO" {
				tags = append(tags, "ACTIVE-TRADER")
				break
			}
		}
		if c.Type == "Corporate" {
			tags = append(tags, "CORPORATE")
		}
		if c.Status == "Active" && staff < 2 && c.Type == "Individual" && len(tags) == 0 {
			tags, staff = append(tags, "STAFF"), staff+1
		}
		if len(tags) == 0 {
			continue
		}
		if _, err := docs.Update(ctx, o.Docs, tenant, cClient, c.UCC, "system", func(x *Client) error {
			x.Tags = tags
			return nil
		}); err != nil {
			return err
		}
	}
	d := func(s string) money.Dec { return money.MustDec(s) }
	type R = charges.BrokerageRule
	plans := []PlanInput{
		{ID: "standard", Name: "Standard", Note: "Default for retail clients", Rules: map[string]R{
			"CM-DEL": {Mode: charges.PlanPct, Pct: d("0.3"), Min: d("20")}, "CM-INTRA": {Mode: charges.PlanLowerOf, Flat: d("20"), Pct: d("0.03")},
			"FO-FUT": {Mode: charges.PlanLowerOf, Flat: d("20"), Pct: d("0.03")}, "FO-OPT": {Mode: charges.PlanFlat, Flat: d("20")},
			"CD-FUT": {Mode: charges.PlanLowerOf, Flat: d("20"), Pct: d("0.03")}, "CD-OPT": {Mode: charges.PlanFlat, Flat: d("20")},
			"COM-FUT": {Mode: charges.PlanLowerOf, Flat: d("20"), Pct: d("0.03")}, "COM-OPT": {Mode: charges.PlanFlat, Flat: d("20")}}},
		{ID: "active-trader", Name: "Active trader", Note: "Low intraday and F&O rates for frequent traders", Rules: map[string]R{
			"CM-DEL": {Mode: charges.PlanPct, Pct: d("0.1")}, "CM-INTRA": {Mode: charges.PlanPct, Pct: d("0.01"), Max: d("15")},
			"FO-FUT": {Mode: charges.PlanPct, Pct: d("0.01"), Max: d("15")}, "FO-OPT": {Mode: charges.PlanPerLot, PerLot: d("5"), Max: d("50")}}},
		{ID: "hni-flat", Name: "HNI flat", Note: "Flat fee per order", Rules: map[string]R{
			"CM-DEL": {Mode: charges.PlanPct, Pct: d("0.05")}, "CM-INTRA": {Mode: charges.PlanFlat, Flat: d("15")},
			"FO-FUT": {Mode: charges.PlanFlat, Flat: d("15")}, "FO-OPT": {Mode: charges.PlanFlat, Flat: d("15")},
			"COM-FUT": {Mode: charges.PlanFlat, Flat: d("15")}, "COM-OPT": {Mode: charges.PlanFlat, Flat: d("15")}}},
		{ID: "staff", Name: "Staff", Note: "Employees and their families", Rules: map[string]R{
			"CM-DEL": {Mode: charges.PlanZero}, "CM-INTRA": {Mode: charges.PlanZero}, "FO-FUT": {Mode: charges.PlanZero}, "FO-OPT": {Mode: charges.PlanZero}}},
	}
	for _, p := range plans {
		p.From = "2026-04-01"
		if _, err := o.SavePlan(ctx, tenant, "admin", p); err != nil {
			return err
		}
	}
	for _, a := range [][2]string{{"default", "standard"}, {"tag:ACTIVE-TRADER", "active-trader"}, {"tag:HNI", "hni-flat"}, {"tag:STAFF", "staff"}, {"ucc:BH1003", "hni-flat"}} {
		if _, err := o.Assign(ctx, tenant, "admin", a[0], a[1], "2026-04-01"); err != nil {
			return err
		}
	}
	return nil
}
