package ops

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// Alert is a surveillance or AML case.
type Alert struct {
	ID       string   `json:"id"`
	Date     string   `json:"date"`
	Source   string   `json:"source"` // NSE, BSE, MCX, Internal
	Type     string   `json:"type"`
	UCC      string   `json:"ucc"`
	Name     string   `json:"name,omitempty"`
	Detail   string   `json:"detail"`
	Severity string   `json:"severity"` // High, Medium, Low
	Status   string   `json:"status"`   // Open, Under review, Closed, STR filed
	Notes    []string `json:"notes"`
	Owner    string   `json:"owner,omitempty"`
}

var sevRank = map[string]int{"High": 0, "Medium": 1, "Low": 2}

// Alerts lists every alert, open and severe first.
func (o *Ops) Alerts(ctx context.Context, tenant string) ([]Alert, error) {
	as, err := docs.List[Alert](ctx, o.Docs, tenant, cAlert, "")
	closed := func(s string) bool { return s == "Closed" || s == "STR filed" }
	sort.SliceStable(as, func(i, j int) bool {
		if closed(as[i].Status) != closed(as[j].Status) {
			return !closed(as[i].Status)
		}
		return sevRank[as[i].Severity] < sevRank[as[j].Severity]
	})
	return as, err
}

// raiseAlert creates an alert once per dedupe key.
func (o *Ops) raiseAlert(ctx context.Context, tenant, actor string, a Alert, dedupe string) bool {
	id := "A-" + strings.NewReplacer(":", "-", "|", "-", " ", "").Replace(dedupe)
	if _, _, err := docs.Get[Alert](ctx, o.Docs, tenant, cAlert, id); err == nil {
		return false
	}
	a.ID = id
	if a.Status == "" {
		a.Status = "Open"
	}
	if a.Date == "" {
		a.Date = o.now().Format("2006-01-02")
	}
	if a.Notes == nil {
		a.Notes = []string{}
	}
	if c, err := o.Client(ctx, tenant, a.UCC); err == nil {
		a.Name = c.Name
	}
	if _, err := docs.Put(ctx, o.Docs, tenant, cAlert, id, a, 0, actor); err != nil {
		return false
	}
	o.log(ctx, tenant, actor, "alert.raised", id, map[string]any{"type": a.Type, "ucc": a.UCC, "severity": a.Severity})
	return true
}

var incomeMid = map[string]int64{"<1 L": 50_000, "1-5 L": 300_000, "5-10 L": 750_000, "10-25 L": 1_750_000, "25 L-1 Cr": 6_250_000, ">1 Cr": 20_000_000}

// RunRules runs the internal surveillance rules for a date and returns how
// many new alerts were raised.
func (o *Ops) RunRules(ctx context.Context, tenant, actor, date string) (int, error) {
	if err := o.require(ctx, tenant, actor, "Running surveillance rules", RoleCompliance, RoleChecker); err != nil {
		return 0, err
	}
	trades, err := o.exchangeTrades(ctx, tenant, date)
	if err != nil {
		return 0, err
	}
	clients, err := o.ClientMap(ctx, tenant)
	if err != nil {
		return 0, err
	}
	turnover := map[string]money.Dec{}
	for _, t := range trades {
		turnover[t.UCC] = turnover[t.UCC].Add(t.Value())
	}
	n := 0
	for _, u := range sortedKeys(turnover) {
		c := clients[u]
		mid := incomeMid[c.Income]
		if mid > 0 && turnover[u].Cmp(money.DecInt(mid).MulInt(10)) > 0 {
			if o.raiseAlert(ctx, tenant, actor, Alert{Date: date, Source: "Internal", Type: "Turnover inconsistent with income", UCC: u, Severity: "High",
				Detail: fmt.Sprintf("Turnover ₹%s on %s against declared income %s", formatINR(turnover[u]), date, c.Income)}, "TOI:"+date+":"+u) {
				n++
			}
		}
		if c.Inactive {
			if o.raiseAlert(ctx, tenant, actor, Alert{Date: date, Source: "Internal", Type: "Trade in an inactive account", UCC: u, Severity: "High",
				Detail: fmt.Sprintf("Trades of ₹%s on %s while the UCC is flagged inactive since %s; the account must be reactivated (IPV, KYC check, KRA validated) before trading", formatINR(turnover[u]), date, c.InactiveSince)}, "INA:"+date+":"+u) {
				n++
			}
		}
		if c.LastTrade != "" && daysBetween(c.LastTrade, date) > 180 && turnover[u].Cmp(money.DecInt(1_000_000)) > 0 {
			if o.raiseAlert(ctx, tenant, actor, Alert{Date: date, Source: "Internal", Type: "Dormant account reactivated with large trade", UCC: u, Severity: "Medium",
				Detail: fmt.Sprintf("No trades since %s, then ₹%s on %s", c.LastTrade, formatINR(turnover[u]), date)}, "DOR:"+date+":"+u) {
				n++
			}
		}
	}
	ccms, _ := o.CCMs(ctx, tenant, "")
	per := map[string]int{}
	for _, c := range ccms {
		per[c.From]++
	}
	for u, k := range per {
		if k >= 2 {
			if o.raiseAlert(ctx, tenant, actor, Alert{Date: date, Source: "Internal", Type: "Frequent client code modification", UCC: u, Severity: "Medium",
				Detail: fmt.Sprintf("%d client code modifications away from this client", k)}, "CCM:"+date+":"+u) {
				n++
			}
		}
	}
	o.log(ctx, tenant, actor, "surveillance.rules.run", date, map[string]any{"new_alerts": n})
	return n, nil
}

// ActOnAlert moves an alert: review, close (with rationale) or str.
func (o *Ops) ActOnAlert(ctx context.Context, tenant, actor, id, action, note string) (Alert, error) {
	roles := []string{RoleCompliance}
	if action == "review" {
		roles = append(roles, RoleChecker)
	}
	if err := o.require(ctx, tenant, actor, "This alert action", roles...); err != nil {
		return Alert{}, err
	}
	if (action == "close" || action == "str") && strings.TrimSpace(note) == "" {
		return Alert{}, invalid("record the rationale before closing or filing")
	}
	a, err := docs.Update(ctx, o.Docs, tenant, cAlert, id, actor, func(x *Alert) error {
		if x.Status == "Closed" || x.Status == "STR filed" {
			return fmt.Errorf("%w: alert is already %s", ErrState, x.Status)
		}
		switch action {
		case "review":
			x.Status, x.Owner = "Under review", actor
		case "close":
			x.Status = "Closed"
		case "str":
			x.Status = "STR filed"
		default:
			return invalid("unknown action %q", action)
		}
		if note != "" {
			x.Notes = append(x.Notes, actor+": "+note)
		}
		return nil
	})
	if errors.Is(err, docs.ErrNotFound) {
		return a, err
	}
	if err == nil {
		o.log(ctx, tenant, actor, "alert."+action, id, map[string]any{"note": note})
	}
	return a, err
}

// IPOApp is an IPO application through the UPI mandate (ASBA) route.
type IPOApp struct {
	ID        string       `json:"id"`
	UCC       string       `json:"ucc"`
	Name      string       `json:"name,omitempty"`
	Issue     string       `json:"issue"`
	Lots      int64        `json:"lots"`
	Amount    money.Amount `json:"amount"`
	UPI       string       `json:"upi"`
	Mandate   string       `json:"mandate"` // Accepted, Pending, Declined
	Allotment string       `json:"allotment"`
	Allotted  int64        `json:"allotted_lots"`
	Date      string       `json:"date,omitempty"` // bid date (the upload's business date)
}

// MFOrder is a mutual fund order through an exchange platform.
type MFOrder struct {
	ID       string       `json:"id"`
	UCC      string       `json:"ucc"`
	Name     string       `json:"name,omitempty"`
	Scheme   string       `json:"scheme"`
	Type     string       `json:"type"`
	Amount   money.Amount `json:"amount"`
	Platform string       `json:"platform"`
	Status   string       `json:"status"`
	Date     string       `json:"date,omitempty"`
}

// IPOApps lists IPO applications.
func (o *Ops) IPOApps(ctx context.Context, tenant string) ([]IPOApp, error) {
	return docs.List[IPOApp](ctx, o.Docs, tenant, cIPO, "")
}

// MFOrders lists mutual fund orders.
func (o *Ops) MFOrders(ctx context.Context, tenant string) ([]MFOrder, error) {
	return docs.List[MFOrder](ctx, o.Docs, tenant, cMF, "")
}
