package ops

import (
	"context"
	"errors"
	"fmt"

	"ledgerline/internal/docs"
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
