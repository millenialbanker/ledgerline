package control

import (
	"context"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// ---- customers ----

// Customers lists customers.
func (c *Control) Customers(ctx context.Context, by string) ([]Customer, error) {
	if err := c.need(ctx, by); err != nil {
		return nil, err
	}
	all, err := docs.List[Customer](ctx, c.Docs, T, cCustomer, "")
	sort.Slice(all, func(i, j int) bool { return strings.ToLower(all[i].Name) < strings.ToLower(all[j].Name) })
	return all, err
}

// SaveCustomer adds or changes a customer.
func (c *Control) SaveCustomer(ctx context.Context, by string, cu Customer) (Customer, error) {
	if err := c.need(ctx, by, RoleBilling); err != nil {
		return cu, err
	}
	cu.Name = clean(cu.Name, 100)
	if cu.Name == "" {
		return cu, invalid("give the customer a name")
	}
	cu.GSTIN = strings.ToUpper(strings.TrimSpace(cu.GSTIN))
	if cu.GSTIN != "" && !validGSTIN(cu.GSTIN) {
		return cu, invalid("the GSTIN doesn't look right (15 characters, e.g. 27ABCDE1234F1Z5)")
	}
	if cu.GSTIN != "" {
		cu.State = cu.GSTIN[:2]
	}
	if !has(CustomerStatuses, cu.Status) {
		cu.Status = "Active"
	}
	if d, err := money.ParseDec(firstOf(cu.DiscountPct, "0")); err != nil || d.Sign() < 0 || d.Cmp(money.DecInt(100)) > 0 {
		return cu, invalid("discount must be 0 to 100 %%")
	}
	if cu.SetupFee < 0 {
		return cu, invalid("setup fee can't be negative")
	}
	if cu.BillFrom != "" {
		if _, err := time.Parse("2006-01", cu.BillFrom); err != nil {
			return cu, invalid("billing start must be a month (YYYY-MM)")
		}
	}
	for _, d := range []struct{ v, what string }{{cu.TrialEnds, "trial end"}, {cu.ContractStart, "contract start"}, {cu.ContractEnd, "contract end"}} {
		if d.v != "" {
			if _, err := time.Parse("2006-01-02", d.v); err != nil {
				return cu, invalid("%s must be a date (YYYY-MM-DD)", d.what)
			}
		}
	}
	if cu.ContractStart != "" && cu.ContractEnd != "" && cu.ContractEnd < cu.ContractStart {
		return cu, invalid("the contract ends before it starts")
	}
	if cu.TDSPct != "" {
		if d, err := money.ParseDec(cu.TDSPct); err != nil || d.Sign() < 0 || d.Cmp(money.DecInt(20)) > 0 {
			return cu, invalid("TDS must be 0 to 20 %%")
		}
	}
	if cu.MinMonthly < 0 || cu.IncludedUsers < 0 {
		return cu, invalid("minimum bill and included logins can't be negative")
	}
	for e, a := range cu.EditionFee {
		if _, ok := editionRank[e]; !ok || a < 0 {
			return cu, invalid("agreed fees are per edition (manual, autopilot, full) and not negative")
		}
	}
	for _, e := range []string{cu.BillingEmail, cu.TechEmail} {
		if e != "" && !strings.Contains(e, "@") {
			return cu, invalid("%q is not an email address", e)
		}
	}
	cu.BillingEmail, cu.CCEmails, cu.TechContact, cu.TechEmail = clean(cu.BillingEmail, 120), clean(cu.CCEmails, 400), clean(cu.TechContact, 80), clean(cu.TechEmail, 120)
	cu.LegalName, cu.Address, cu.Contact, cu.Email, cu.Phone, cu.Notes = clean(cu.LegalName, 150), clean(cu.Address, 400), clean(cu.Contact, 80), clean(cu.Email, 120), clean(cu.Phone, 30), clean(cu.Notes, 2000)
	if cu.ID == "" {
		n, err := c.next(ctx, "customer")
		if err != nil {
			return cu, err
		}
		cu.ID, cu.Created = "CUS-"+pad4(n), c.stamp()
		if cu.BillFrom == "" {
			cu.BillFrom = c.month()
		}
		err = docs.Upsert(ctx, c.Docs, T, cCustomer, cu.ID, cu, by)
		c.log(ctx, by, "customer.create", cu.ID, cu.Name)
		return cu, err
	}
	out, err := docs.Update(ctx, c.Docs, T, cCustomer, cu.ID, by, func(x *Customer) error {
		billed, created := x.SetupBilled, x.Created
		*x = cu
		x.SetupBilled, x.Created = billed, created
		return nil
	})
	if err != nil {
		return out, ErrNotFound
	}
	c.log(ctx, by, "customer.save", cu.ID, cu.Name)
	return out, nil
}

func pad4(n int) string {
	s := "000" + itoa(n)
	return s[len(s)-4:]
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// LinkFirm moves a firm to another customer (e.g. one broker's two servers).
func (c *Control) LinkFirm(ctx context.Context, by, firm, customer string) (Firm, error) {
	if err := c.need(ctx, by, RoleBilling); err != nil {
		return Firm{}, err
	}
	if _, _, err := docs.Get[Customer](ctx, c.Docs, T, cCustomer, customer); err != nil {
		return Firm{}, invalid("no such customer")
	}
	f, err := docs.Update(ctx, c.Docs, T, cFirm, firm, by, func(x *Firm) error { x.Customer = customer; return nil })
	if err != nil {
		return f, ErrNotFound
	}
	// Its open requests follow it.
	ts, _ := docs.List[Ticket](ctx, c.Docs, T, cTicket, "")
	for _, t := range ts {
		if t.Firm == firm {
			_, _ = docs.Update(ctx, c.Docs, T, cTicket, t.ID, by, func(x *Ticket) error { x.Customer = customer; return nil })
		}
	}
	c.log(ctx, by, "firm.link", firm, customer)
	return f, nil
}

// ---- views ----

// FirmView is a firm with its server's state.
type FirmView struct {
	Firm
	InstallName string `json:"install_name"`
	Kind        string `json:"kind"`
	Online      bool   `json:"online"`
	Version     string `json:"version"`
	CustName    string `json:"cust_name"`
}

// InstallView is a server with its state.
type InstallView struct {
	Installation
	Online bool `json:"online"`
}

func (c *Control) firmViews(ctx context.Context) ([]FirmView, map[string]Installation, error) {
	ins, err := docs.List[Installation](ctx, c.Docs, T, cInstall, "")
	if err != nil {
		return nil, nil, err
	}
	im := map[string]Installation{}
	for _, in := range ins {
		im[in.ID] = in
	}
	cus, _ := docs.List[Customer](ctx, c.Docs, T, cCustomer, "")
	cm := map[string]string{}
	for _, cu := range cus {
		cm[cu.ID] = cu.Name
	}
	fs, err := docs.List[Firm](ctx, c.Docs, T, cFirm, "")
	if err != nil {
		return nil, nil, err
	}
	out := make([]FirmView, 0, len(fs))
	for _, f := range fs {
		in := im[f.Installation]
		// A firm no longer in its server's reports is shown as offline.
		online := c.Online(in) && has(in.Firms, f.Tenant)
		out = append(out, FirmView{Firm: f, InstallName: in.Name, Kind: in.Kind, Online: online, Version: in.Info.Version, CustName: cm[f.Customer]})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Latest.Firm) < strings.ToLower(out[j].Latest.Firm) })
	return out, im, nil
}

// Installations lists servers.
func (c *Control) Installations(ctx context.Context, by string) ([]InstallView, error) {
	if err := c.need(ctx, by); err != nil {
		return nil, err
	}
	ins, err := docs.List[Installation](ctx, c.Docs, T, cInstall, "")
	out := make([]InstallView, 0, len(ins))
	for _, in := range ins {
		in.KeyHash = ""
		out = append(out, InstallView{Installation: in, Online: c.Online(in)})
	}
	return out, err
}

// Firms lists every firm: who runs Ledgerline, on which server, what edition.
func (c *Control) Firms(ctx context.Context, by string) ([]FirmView, error) {
	if err := c.need(ctx, by); err != nil {
		return nil, err
	}
	fs, _, err := c.firmViews(ctx)
	return fs, err
}

// Overview is the front page.
type Overview struct {
	Month        string         `json:"month"`
	Customers    int            `json:"customers"`
	ByStatus     map[string]int `json:"by_status"`
	Firms        int            `json:"firms"`
	FirmsOnline  int            `json:"firms_online"`
	Installs     int            `json:"installs"`
	InstallsDown []InstallView  `json:"installs_down"`
	StaffActive  int            `json:"staff_active"`
	ActiveToday  int            `json:"active_today"`
	TradedMonth  int            `json:"traded_month"`
	ByEdition    map[string]int `json:"by_edition"`
	AIAnswers    int            `json:"ai_answers"`
	AICharged    money.Amount   `json:"ai_charged"`
	AICost       string         `json:"ai_cost"`
	AILow        []FirmView     `json:"ai_low"`        // firms with AI on and little credit left
	MonthToDate  money.Amount   `json:"month_to_date"` // this month's bills so far, before GST
	LastBilled   money.Amount   `json:"last_billed"`   // last month's issued invoices, before GST
	Outstanding  money.Amount   `json:"outstanding"`
	OverdueInv   int            `json:"overdue_invoices"`
	OverdueAmt   money.Amount   `json:"overdue_amount"`
	OpenTickets  int            `json:"open_tickets"`
	NewTickets   int            `json:"new_tickets"`
	OverdueTk    int            `json:"overdue_tickets"`
	Recent       []Ticket       `json:"recent_tickets"`
	Problems     []FirmView     `json:"problems"` // backups, unclosed days, breaks
}

// GetOverview builds the front page.
func (c *Control) GetOverview(ctx context.Context, by string) (Overview, error) {
	if err := c.need(ctx, by); err != nil {
		return Overview{}, err
	}
	o := Overview{Month: c.month(), ByStatus: map[string]int{}, ByEdition: map[string]int{}, InstallsDown: []InstallView{}, AILow: []FirmView{}, Recent: []Ticket{}, Problems: []FirmView{}}
	cus, _ := docs.List[Customer](ctx, c.Docs, T, cCustomer, "")
	o.Customers = len(cus)
	for _, cu := range cus {
		o.ByStatus[cu.Status]++
	}
	fs, im, err := c.firmViews(ctx)
	if err != nil {
		return o, err
	}
	o.Installs = len(im)
	for _, in := range im {
		if !in.Revoked && !c.Online(in) {
			in.KeyHash = ""
			o.InstallsDown = append(o.InstallsDown, InstallView{Installation: in})
		}
	}
	cost := money.Dec{}
	for _, f := range fs {
		o.Firms++
		if f.Online {
			o.FirmsOnline++
		}
		r := f.Latest
		o.StaffActive += r.StaffActive
		o.ActiveToday += r.ActiveToday
		if r.Month == o.Month {
			o.TradedMonth += r.TradedMonth
			o.AIAnswers += r.AI.Answers
			o.AICharged += money.Amount(r.AI.Charged)
			if d, err := money.ParseDec(firstOf(r.AI.CostINR, "0")); err == nil {
				cost = cost.Add(d)
			}
		}
		o.ByEdition[firstOf(r.Edition, "manual")]++
		if r.AI.On && r.AI.Balance < int64(money.MustAmount("200")) {
			o.AILow = append(o.AILow, f)
		}
		if r.Health.BackupProblem != "" || r.Health.OpenBreaks > 0 {
			o.Problems = append(o.Problems, f)
		}
	}
	o.AICost = cost.Round(money.UnitPaisa).String()

	if pv, err := c.Preview(ctx, by, o.Month); err == nil {
		for _, b := range pv {
			if b.Billable {
				o.MonthToDate += b.Invoice.Taxable
			}
		}
	}
	last := c.now().AddDate(0, -1, 0).Format("2006-01")
	invs, _ := docs.List[Invoice](ctx, c.Docs, T, cInvoice, "")
	for _, iv := range invs {
		if iv.Status == "Draft" || iv.Status == "Void" {
			continue
		}
		if iv.Period == last {
			o.LastBilled += iv.Taxable
		}
		iv.settle()
		due := iv.Balance
		if due > 0 {
			o.Outstanding += due
			if c.InvoiceOverdue(iv) {
				o.OverdueInv++
				o.OverdueAmt += due
			}
		}
	}
	ts, _ := docs.List[Ticket](ctx, c.Docs, T, cTicket, "")
	sort.Slice(ts, func(i, j int) bool { return ts[i].Updated > ts[j].Updated })
	for _, t := range ts {
		if t.Status != "Resolved" && t.Status != "Closed" {
			o.OpenTickets++
			if t.Status == "New" {
				o.NewTickets++
			}
			if c.Overdue(t) {
				o.OverdueTk++
			}
			if len(o.Recent) < 8 {
				o.Recent = append(o.Recent, t)
			}
		}
	}
	return o, nil
}

// CustomerDetail is everything about one customer.
type CustomerDetail struct {
	Customer Customer     `json:"customer"`
	Firms    []FirmView   `json:"firms"`
	Usage    []UsageMonth `json:"usage"` // last 12 months, all firms
	Invoices []Invoice    `json:"invoices"`
	Tickets  []Ticket     `json:"tickets"`
	Commands []CommandRec `json:"commands"`
	Bill     *BillLine    `json:"bill"` // this month so far
}

// GetCustomer returns one customer in full.
func (c *Control) GetCustomer(ctx context.Context, by, id string) (CustomerDetail, error) {
	if err := c.need(ctx, by); err != nil {
		return CustomerDetail{}, err
	}
	cu, _, err := docs.Get[Customer](ctx, c.Docs, T, cCustomer, id)
	if err != nil {
		return CustomerDetail{}, ErrNotFound
	}
	d := CustomerDetail{Customer: cu, Firms: []FirmView{}, Usage: []UsageMonth{}, Invoices: []Invoice{}, Tickets: []Ticket{}, Commands: []CommandRec{}}
	fs, _, _ := c.firmViews(ctx)
	mine := map[string]bool{}
	for _, f := range fs {
		if f.Customer == id {
			d.Firms = append(d.Firms, f)
			mine[f.ID] = true
		}
	}
	from := c.now().AddDate(0, -11, 0).Format("2006-01")
	us, _ := docs.List[UsageMonth](ctx, c.Docs, T, cUsage, "")
	for _, u := range us {
		if mine[u.Firm] && u.Month >= from {
			d.Usage = append(d.Usage, u)
		}
	}
	sort.Slice(d.Usage, func(i, j int) bool { return d.Usage[i].Month > d.Usage[j].Month })
	ts, _ := docs.List[Ticket](ctx, c.Docs, T, cTicket, "")
	for _, t := range ts {
		if t.Customer == id || mine[t.Firm] {
			d.Tickets = append(d.Tickets, t)
		}
	}
	sort.Slice(d.Tickets, func(i, j int) bool { return d.Tickets[i].Updated > d.Tickets[j].Updated })
	cs, _ := docs.List[CommandRec](ctx, c.Docs, T, cCommand, "")
	for _, x := range cs {
		if mine[x.Firm] {
			d.Commands = append(d.Commands, x)
		}
	}
	sort.Slice(d.Commands, func(i, j int) bool { return d.Commands[i].ID > d.Commands[j].ID })
	if c.need(ctx, by, RoleBilling) == nil {
		ivs, _ := docs.List[Invoice](ctx, c.Docs, T, cInvoice, "")
		for _, iv := range ivs {
			if iv.Customer == id {
				d.Invoices = append(d.Invoices, iv)
			}
		}
		sort.Slice(d.Invoices, func(i, j int) bool { return d.Invoices[i].ID > d.Invoices[j].ID })
		if pv, err := c.Preview(ctx, by, c.month()); err == nil {
			for i := range pv {
				if pv[i].Customer.ID == id {
					d.Bill = &pv[i]
				}
			}
		}
	}
	return d, nil
}

// UsageRow is one firm's use in a month.
type UsageRow struct {
	UsageMonth
	FirmName string `json:"firm_name"`
	Customer string `json:"customer"`
	CustName string `json:"cust_name"`
}

// Usage lists every firm's use in a month.
func (c *Control) Usage(ctx context.Context, by, month string) ([]UsageRow, error) {
	if err := c.need(ctx, by); err != nil {
		return nil, err
	}
	fs, _, err := c.firmViews(ctx)
	if err != nil {
		return nil, err
	}
	out := []UsageRow{}
	for _, f := range fs {
		u, _, err := docs.Get[UsageMonth](ctx, c.Docs, T, cUsage, f.ID+"|"+month)
		if err != nil {
			continue
		}
		out = append(out, UsageRow{UsageMonth: u, FirmName: firstOf(u.Report.Firm, f.Tenant), Customer: f.Customer, CustName: f.CustName})
	}
	return out, nil
}

// Log lists recent actions in Control.
func (c *Control) Log(ctx context.Context, by string, limit int) ([]LogEntry, error) {
	if err := c.need(ctx, by, RoleOwner); err != nil {
		return nil, err
	}
	all, err := docs.List[LogEntry](ctx, c.Docs, T, cLog, "")
	sort.Slice(all, func(i, j int) bool { return all[i].At > all[j].At })
	if len(all) > limit {
		all = all[:limit]
	}
	return all, err
}
