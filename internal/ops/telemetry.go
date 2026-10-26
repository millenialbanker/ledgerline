package ops

import (
	"context"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/control"
	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/money"
)

// ---- reporting to Ledgerline Control ----
//
// When an installation is connected to the provider's Ledgerline Control,
// it reports each firm's use every quarter hour: the edition, its logins
// and when they were last active, client counts and the clients who traded
// this month (what the per-client fee is based on), AI use, online account
// opening, automations, which areas are used, and the day's health. No
// client's details, trades or money leave: only counts and the firm's own
// staff logins.

// auditAreas maps audit actions to the areas reported as "who runs what".
var auditAreas = []struct{ prefix, area string }{
	{"recon", "Trade reconciliation"}, {"file", "Files"}, {"ingest", "Files"}, {"billing", "Billing"}, {"bill", "Billing"},
	{"settle", "Settlement"}, {"note", "Contract notes"}, {"client", "Clients"}, {"eapply", "Online account opening"},
	{"rms", "Risk"}, {"margin", "Risk"}, {"pledge", "Collateral"}, {"receipt", "Receipts & payouts"}, {"payout", "Receipts & payouts"},
	{"voucher", "Firm accounts"}, {"ras", "Running account"}, {"report", "Regulatory reports"}, {"kyc", "KYC & PMLA"},
	{"complaint", "Investor complaints"}, {"helpdesk", "Investor complaints"}, {"dp", "Depository"}, {"crm", "Leads & CRM"},
	{"lead", "Leads & CRM"}, {"autopilot", "Autopilot"}, {"work", "Autopilot"}, {"eod", "Day close"}, {"backup", "Backups"},
	{"chat", "Team messages"}, {"rpa", "Automations"}, {"ledgy", "Ledgy"}, {"ai", "Ledgy"}, {"msg", "Client messages"}, {"wa", "Client messages"},
	{"auth", "Sign-in"}, {"user", "Users"}, {"settings", "Settings"}, {"journal", "Firm accounts"}, {"mtf", "Margin trading"},
	{"dis", "Depository"}, {"reactivation", "Clients"}, {"change", "Clients"}, {"prepaid", "Brokerage plans"}, {"brokerage", "Brokerage plans"},
	{"buyback", "Corporate actions"}, {"corpaction", "Corporate actions"}, {"shortage", "Settlement"}, {"obligation", "Settlement"},
	{"ticket", "Investor complaints"}, {"portal", "Client portal"}, {"message", "Client messages"}, {"whatsapp", "Client messages"},
	{"template", "Client messages"}, {"external_trade", "Trades"}, {"debit", "Running account"}, {"ccdeposit", "Collateral"},
	{"bank", "Receipts & payouts"}, {"alert", "Surveillance"}, {"surveillance", "Surveillance"}, {"yearend", "Year end"},
	{"security", "Security"}, {"referrals", "Leads & CRM"}, {"partner", "Partners"}, {"positions", "Risk"}, {"pmla", "KYC & PMLA"}, {"debarred", "KYC & PMLA"},
	{"nri", "Online account opening"}, {"onboarding", "Online account opening"}, {"interest", "Billing"}, {"invoice", "Billing"},
	{"support", "Support"}, {"job", "Settings"}, {"holidays", "Settings"}, {"session", "Sign-in"}, {"dev", "Development"},
}

func auditArea(action string) string {
	for _, a := range auditAreas {
		if strings.HasPrefix(action, a.prefix) {
			return a.area
		}
	}
	return "Other"
}

// FirmReport builds a firm's report for Ledgerline Control.
func (o *Ops) FirmReport(ctx context.Context, tenant string) (control.FirmReport, error) {
	now := o.now()
	month, today := now.Format("2006-01"), now.Format("2006-01-02")
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return control.FirmReport{}, err
	}
	ed, _ := o.Edition(ctx, tenant)
	r := control.FirmReport{Tenant: tenant, Firm: set.Firm, SEBIReg: set.SEBIReg, Edition: ed, Month: month, BusinessDt: o.businessDate(ctx, tenant),
		Users: []control.UserInfo{}, ClientsByStg: map[string]int{}, Features: map[string]int{}}

	// Logins: when each was last active (from their sessions).
	seen := map[string]string{}
	ss, _ := docs.List[Session](ctx, o.Docs, tenant, cSession, "")
	for _, s := range ss {
		if s.LastSeen > seen[s.User] {
			seen[s.User] = s.LastSeen
		}
	}
	us, _ := o.Users(ctx, tenant)
	for _, u := range us {
		if u.Role == RoleClient {
			continue // client portal logins are clients, not users of the firm's licence
		}
		c, _, _ := docs.Get[Credential](ctx, o.Docs, tenant, cCred, u.ID)
		ui := control.UserInfo{ID: u.ID, Name: u.Name, Role: u.Role, Active: u.Active, LastLogin: c.LastLogin, LastSeen: seen[u.ID]}
		r.Users = append(r.Users, ui)
		if !u.Active {
			continue
		}
		r.StaffActive++
		last := firstOf(ui.LastSeen, ui.LastLogin)
		if t, err := time.Parse(time.RFC3339, last); err == nil {
			if t.In(ist).Format("2006-01-02") == today {
				r.ActiveToday++
			}
			if now.Sub(t) < 30*24*time.Hour {
				r.Active30++
			}
		}
	}
	sort.Slice(r.Users, func(i, j int) bool { return r.Users[i].Name < r.Users[j].Name })

	cs, _ := o.Clients(ctx, tenant)
	r.Clients = len(cs)
	for _, c := range cs {
		r.ClientsByStg[c.Status]++
	}
	// Clients who traded this month, from the exchange's trade files.
	traded := map[string]bool{}
	start, _ := time.ParseInLocation("2006-01-02", month+"-01", ist)
	for d := start; d.Format("2006-01") == month && !d.After(now); d = d.AddDate(0, 0, 1) {
		ts, _ := o.App.Trades.List(ctx, tenant, ingest.FromExchange, d.Format("2006-01-02"))
		for _, t := range ts {
			traded[t.UCC] = true
		}
		r.TradesMonth += len(ts)
	}
	r.TradedMonth = len(traded)

	// Online account opening verified through DigiLocker or offline e-KYC.
	apps, _ := docs.List[Application](ctx, o.Docs, tenant, cApplication, "")
	for _, a := range apps {
		if !strings.HasPrefix(a.Updated, month) {
			continue
		}
		for _, c := range a.Checks {
			if (c.Name == chkAadhaarDL || c.Name == chkAadhaarOffline) && c.Status != "Failed" {
				r.KYCMonth++
				break
			}
		}
	}

	if v, err := o.aiUsage(ctx, tenant, month); err == nil {
		cost := money.Dec{}
		for _, u := range v.Uses {
			x, _ := money.ParseDec(u.CostINR)
			cost = cost.Add(x)
		}
		r.AI = control.AIUse{Answers: v.Answers, CostINR: cost.Fixed(2), Charged: int64(v.Charged), Balance: int64(v.Wallet.Balance), On: set.LedgyAI}
		for _, tp := range v.TopUps {
			if strings.HasPrefix(tp.At, month) {
				r.AI.ToppedUp += int64(tp.Amount)
			}
		}
	}

	as, _ := docs.List[RPAAgent](ctx, o.Docs, tenant, cRPAAgent, "")
	for _, a := range as {
		if !a.Revoked {
			r.Bots.Computers++
		}
	}
	fs, _ := docs.List[RPAFlow](ctx, o.Docs, tenant, cRPAFlow, "")
	for _, f := range fs {
		if !strings.HasPrefix(f.Owner, "*deleted:") {
			r.Bots.Workflows++
		}
	}
	runs, _ := docs.List[RPARun](ctx, o.Docs, tenant, cRPARun, "")
	for _, x := range runs {
		if strings.HasPrefix(x.Queued, month) {
			r.Bots.RunsMonth++
			if x.Status == "Failed" {
				r.Bots.Failed++
			}
		}
	}

	// Which areas are used: actions in the last 30 days.
	evs, _ := o.App.Audit.List(ctx, tenant, 20000)
	for _, e := range evs {
		if now.Sub(e.At) > 30*24*time.Hour {
			continue
		}
		if e.Actor != SystemActor && !strings.HasPrefix(e.Actor, "system:") {
			r.Features[auditArea(e.Action)]++
		}
		if at := e.At.In(ist).Format(time.RFC3339); at > r.LastActivity {
			r.LastActivity = at
		}
	}

	if st, err := o.EOD(ctx, tenant, r.BusinessDt); err == nil {
		r.Health.EOD = map[bool]string{true: "closed", false: "open"}[st.Closed]
	}
	if bv, err := o.BackupsStatus(ctx, tenant, SystemActor); err == nil {
		if bv.LastGood != nil {
			r.Health.LastBackup = bv.LastGood.Created
		}
		if len(bv.Problems) > 0 {
			r.Health.BackupProblem = bv.Problems[0]
		}
	}
	if ws, err := o.WorkItems(ctx, tenant, "Open"); err == nil {
		r.Health.OpenWork = len(ws)
	}
	if bs, err := o.App.Breaks.List(ctx, tenant, "", "OPEN"); err == nil {
		r.Health.OpenBreaks = len(bs)
	}
	return r, nil
}

// ApplyCommand carries out a command from Ledgerline Control.
func (o *Ops) ApplyCommand(ctx context.Context, c control.Command) error {
	switch c.Kind {
	case "ai_credit":
		if c.Amount <= 0 {
			return invalid("amount must be positive")
		}
		// The same top-up is never added twice.
		v, err := o.aiUsage(ctx, c.Tenant, "")
		if err == nil {
			for _, tp := range v.TopUps {
				if tp.Ref == "control:"+c.ID {
					return nil
				}
			}
		}
		_, err = o.AddAICredit(ctx, c.Tenant, "system:control", money.Amount(c.Amount), "control:"+c.ID)
		return err
	}
	return invalid("unknown command %q", c.Kind)
}
