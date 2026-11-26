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

// ---- broker-specific staff compliance ----
//
// Certificates per person with expiry and the register of who must hold
// which certificate by duty (HRPolicy.CertRules, each rule citing what it
// rests on); reminders before expiry (60, 30 and 7 days by default); an
// expired certificate a terminal needs raises a work item and flags the
// person's terminals. The own-trading register: employees' and relatives'
// trading accounts (outside accounts declared), holdings declarations
// (joining, annual, leaving) and pre-clearance of trades where the firm's
// code requires it (HRPolicy.OwnTrading).

const (
	cTradeAcct = "hr_tradeacct"
	cHoldDecl  = "hr_holddecl"
	cPreClear  = "hr_preclear"
	cHRTick    = "hr_tick"
)

// CertNeed is a certificate a person must hold (or holds) and its state.
type CertNeed struct {
	Cert           string `json:"cert"`
	Duty           string `json:"duty,omitempty"` // the duty that needs it ("" if held but not required)
	Basis          string `json:"basis,omitempty"`
	BlocksTerminal bool   `json:"blocks_terminal"`
	Number         string `json:"number,omitempty"`
	ValidTill      string `json:"valid_till,omitempty"`
	Days           int    `json:"days"`  // days left (negative: expired)
	State          string `json:"state"` // Valid, Expiring (within the first reminder), Expired, Missing
}

// certNeeds lists the certificates a person's duties need, with the best
// certificate held for each, then any other certificates held.
func certNeeds(e Employee, p HRPolicy, day string) []CertNeed {
	first := 60
	for _, d := range p.ReminderDays {
		first = max(first, d)
	}
	best := func(cert string) (StaffCert, bool) {
		var b StaffCert
		ok := false
		for _, c := range e.Certs {
			if strings.EqualFold(strings.TrimSpace(c.Cert), strings.TrimSpace(cert)) && (!ok || c.ValidTill > b.ValidTill) {
				b, ok = c, true
			}
		}
		return b, ok
	}
	state := func(n *CertNeed, c StaffCert) {
		n.Number, n.ValidTill = c.Number, c.ValidTill
		n.Days = daysBetween(day, c.ValidTill)
		switch {
		case c.ValidTill == "":
			n.State = "Valid" // no expiry
		case n.Days < 0:
			n.State = "Expired"
		case n.Days <= first:
			n.State = "Expiring"
		default:
			n.State = "Valid"
		}
	}
	out := []CertNeed{}
	seen := map[string]bool{}
	for _, r := range p.CertRules {
		if !contains(e.Duties, r.Duty) {
			continue
		}
		k := strings.ToLower(r.Cert)
		if seen[k] {
			// Two duties needing the same certificate: one line, blocking
			// if either rule blocks.
			for i := range out {
				if strings.ToLower(out[i].Cert) == k {
					out[i].BlocksTerminal = out[i].BlocksTerminal || r.BlocksTerminal
				}
			}
			continue
		}
		seen[k] = true
		n := CertNeed{Cert: r.Cert, Duty: r.Duty, Basis: r.Basis, BlocksTerminal: r.BlocksTerminal, State: "Missing"}
		if c, ok := best(r.Cert); ok {
			state(&n, c)
		}
		out = append(out, n)
	}
	for _, c := range e.Certs {
		if seen[strings.ToLower(c.Cert)] {
			continue
		}
		seen[strings.ToLower(c.Cert)] = true
		n := CertNeed{Cert: c.Cert}
		if b, ok := best(c.Cert); ok {
			state(&n, b)
		}
		out = append(out, n)
	}
	return out
}

// SaveStaffCert adds or replaces a certificate on a person. HR managers
// and the compliance officer; a person may add their own (unverified until
// checked).
func (o *Ops) SaveStaffCert(ctx context.Context, tenant, actor, code string, c StaffCert) (Employee, error) {
	if err := o.needModule(ctx, tenant, ModHRMS); err != nil {
		return Employee{}, err
	}
	code = strings.ToUpper(code)
	self := false
	if me, ok := o.employeeOf(ctx, tenant, actor); ok && me.Code == code {
		self = true
	}
	role, err := o.hrRole(ctx, tenant, actor)
	if err != nil {
		return Employee{}, err
	}
	checker := role == RoleHR || role == RoleCompliance || role == RoleAdmin
	if !self || checker {
		if err := o.require(ctx, tenant, actor, "Recording staff certificates", RoleHR, RoleCompliance); err != nil {
			return Employee{}, err
		}
	}
	c.Cert, c.Number = strings.TrimSpace(c.Cert), strings.TrimSpace(c.Number)
	if c.Cert == "" || c.Number == "" {
		return Employee{}, invalid("give the certificate and its number")
	}
	if c.ValidTill != "" && !validDay(c.ValidTill) || c.IssuedOn != "" && !validDay(c.IssuedOn) {
		return Employee{}, invalid("dates are YYYY-MM-DD")
	}
	c.Verified = checker // a person's own entry waits for HR or compliance to check it
	c.By, c.At = actor, o.now().Format(time.RFC3339)
	e, err := docs.Update(ctx, o.Docs, tenant, cEmployee, code, actor, func(x *Employee) error {
		if c.ID == "" {
			c.ID = fmt.Sprintf("C%d", len(x.Certs)+1)
			for _, y := range x.Certs {
				if y.ID == c.ID {
					c.ID = fmt.Sprintf("C%d-%d", len(x.Certs)+1, o.now().Unix()%100000)
				}
			}
			x.Certs = append(x.Certs, c)
			return nil
		}
		for i := range x.Certs {
			if x.Certs[i].ID == c.ID {
				x.Certs[i] = c
				return nil
			}
		}
		return ErrNotFound
	})
	if err != nil {
		return e, err
	}
	o.log(ctx, tenant, actor, "hr.cert.saved", code, map[string]any{"cert": c.Cert, "number": c.Number, "valid_till": c.ValidTill, "verified": c.Verified})
	if certsOK(e, o.hrPolicy(ctx, tenant), o.today()) {
		o.tickItem(ctx, tenant, actor, code, "join", "certs", "certificates on file")
		o.tickItem(ctx, tenant, actor, code, "move", "certs", "certificates on file")
	}
	return e, nil
}

// CertRegisterRow is one person and one certificate on the register.
type CertRegisterRow struct {
	Code    string   `json:"code"`
	Name    string   `json:"name"`
	Branch  string   `json:"branch,omitempty"`
	Partner string   `json:"partner,omitempty"`
	Duties  []string `json:"duties"`
	CertNeed
	Dealer    string   `json:"dealer,omitempty"`
	Terminals []string `json:"terminals"` // open terminals, flagged when the certificate a terminal needs is not valid
	Flagged   bool     `json:"flagged"`
}

// CertRegister is who must hold which certificate, and who does.
type CertRegister struct {
	Rows  []CertRegisterRow `json:"rows"`
	Rules []CertRule        `json:"rules"`
	Days  []int             `json:"reminder_days"`
}

// StaffCertRegister lists every active person's certificates. HR and
// compliance.
func (o *Ops) StaffCertRegister(ctx context.Context, tenant, actor string) (CertRegister, error) {
	if err := o.needModule(ctx, tenant, ModHRMS); err != nil {
		return CertRegister{}, err
	}
	if err := o.require(ctx, tenant, actor, "Seeing the staff certificate register", RoleHR, RoleCompliance); err != nil {
		return CertRegister{}, err
	}
	p := o.hrPolicy(ctx, tenant)
	es, err := o.employees(ctx, tenant)
	today := o.today()
	v := CertRegister{Rows: []CertRegisterRow{}, Rules: p.CertRules, Days: p.ReminderDays}
	for _, e := range es {
		if e.Status == EmpLeft {
			continue
		}
		terms := o.terminalsOf(ctx, tenant, e, today)
		for _, n := range certNeeds(e, p, today) {
			r := CertRegisterRow{Code: e.Code, Name: e.Name, Branch: e.Branch, Partner: e.Partner, Duties: e.Duties, CertNeed: n, Dealer: e.Dealer, Terminals: terms}
			r.Flagged = n.BlocksTerminal && (n.State == "Expired" || n.State == "Missing")
			v.Rows = append(v.Rows, r)
		}
	}
	sort.SliceStable(v.Rows, func(i, j int) bool { return certOrder[v.Rows[i].State] < certOrder[v.Rows[j].State] })
	return v, err
}

var certOrder = map[string]int{"Expired": 0, "Missing": 1, "Expiring": 2, "Valid": 3}

// terminalsOf is the person's open terminals from the dealers register.
func (o *Ops) terminalsOf(ctx context.Context, tenant string, e Employee, day string) []string {
	out := []string{}
	if e.Dealer == "" {
		return out
	}
	d, _, err := docs.Get[Dealer](ctx, o.Docs, tenant, cDealer, e.Dealer)
	if err != nil {
		return out
	}
	for _, t := range d.openTerminals(day) {
		out = append(out, t.Exchange+" "+t.TerminalID)
	}
	return out
}

type hrTickDoc struct {
	Date string `json:"date"`
}

// hrCertTick reminds about certificates and documents running out, once a
// day: a work item at each reminder level (60, 30, 7 days by default), on
// expiry and for a required certificate that is missing. An expired or
// missing certificate a terminal needs flags the person's terminals in the
// work item. A NISM certificate the dealers register already tracks for
// the same dealer (dealersTick) is not reminded twice.
func (o *Ops) hrCertTick(ctx context.Context, tenant string, now time.Time) {
	today := now.In(ist).Format("2006-01-02")
	last, _, _ := docs.GetOr(ctx, o.Docs, tenant, cHRTick, "certs", hrTickDoc{})
	if last.Date >= today {
		return
	}
	p := o.hrPolicy(ctx, tenant)
	levels := append([]int{}, p.ReminderDays...)
	sort.Ints(levels)
	es, err := o.employees(ctx, tenant)
	if err != nil {
		return
	}
	level := func(n int) string {
		if n <= 0 {
			return "expiry"
		}
		for _, l := range levels {
			if n <= l {
				return fmt.Sprint(l)
			}
		}
		return ""
	}
	for _, e := range es {
		if e.Status == EmpLeft || !e.inService(today) && e.Status != EmpJoining {
			continue
		}
		dealerCert := ""
		if e.Dealer != "" {
			if d, _, err := docs.Get[Dealer](ctx, o.Docs, tenant, cDealer, e.Dealer); err == nil {
				dealerCert = d.NISM.CertNo
			}
		}
		terms := o.terminalsOf(ctx, tenant, e, today)
		for _, n := range certNeeds(e, p, today) {
			flag := n.BlocksTerminal && len(terms) > 0
			switch {
			case n.State == "Missing":
				detail := fmt.Sprintf("%s's duties (%s) need %s (%s); none is on file.", e.Name, n.Duty, n.Cert, n.Basis)
				if flag {
					detail += " Flag and block the terminals " + strings.Join(terms, ", ") + " until it is."
				}
				o.raiseWork(ctx, tenant, wHR, e.Code+":cert-missing:"+n.Cert, fmt.Sprintf("%s (%s) has no %s", e.Name, e.Code, n.Cert), detail, "", RoleCompliance, "people", today)
			case n.ValidTill != "" && (dealerCert == "" || !strings.EqualFold(dealerCert, n.Number)):
				lv := level(n.Days)
				if lv == "" {
					continue
				}
				title := fmt.Sprintf("%s of %s (%s) runs out in %d days", n.Cert, e.Name, e.Code, n.Days)
				detail := fmt.Sprintf("Certificate %s is valid till %s. Get the renewed certificate and record it on the person.", n.Number, fmtDay(n.ValidTill))
				if lv == "expiry" {
					title = fmt.Sprintf("%s of %s (%s) has expired", n.Cert, e.Name, e.Code)
					if n.Days == 0 {
						title = fmt.Sprintf("%s of %s (%s) expires today", n.Cert, e.Name, e.Code)
					}
					if flag {
						title += ": terminal flagged"
						detail += fmt.Sprintf(" Block %s at the exchange and in the trading system until a valid certificate is recorded.", strings.Join(terms, ", "))
					}
				}
				o.raiseWork(ctx, tenant, wHR, e.Code+":cert-"+lv+":"+n.Cert+":"+n.ValidTill, title, detail, "", RoleCompliance, "people", today)
				for _, x := range append(append([]int{}, levels...), -1) {
					xl := fmt.Sprint(x)
					if x == -1 {
						xl = "expiry"
					}
					if xl != lv && (lv == "expiry" || x > atoiSafe(lv)) {
						o.closeWork(ctx, tenant, SystemActor, wHR, e.Code+":cert-"+xl+":"+n.Cert+":"+n.ValidTill, "a nearer reminder replaced it")
					}
				}
			}
		}
		for _, d := range e.Docs {
			if d.Expiry == "" {
				continue
			}
			if lv := level(daysBetween(today, d.Expiry)); lv != "" {
				o.raiseWork(ctx, tenant, wHR, e.Code+":doc-"+lv+":"+d.ID, fmt.Sprintf("%s of %s (%s) expires %s", d.Kind, e.Name, e.Code, fmtDay(d.Expiry)),
					"Get the renewed document and add it to the person's file.", "", RoleHR, "people", today)
			}
		}
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cHRTick, "certs", hrTickDoc{Date: today}, SystemActor)
}

func atoiSafe(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// ---- own-trading register ----

// StaffAccount is an employee's or a relative's trading or demat account.
type StaffAccount struct {
	ID         string `json:"id"`
	Employee   string `json:"employee"`
	Holder     string `json:"holder"`   // the account holder's name
	Relation   string `json:"relation"` // Self, Spouse, Parent, Child, Sibling, Other
	Broker     string `json:"broker"`   // the broker or DP (this firm or another)
	Account    string `json:"account"`  // client code or BO ID
	Outside    bool   `json:"outside"`  // with another broker
	OpenedOn   string `json:"opened_on,omitempty"`
	ClosedOn   string `json:"closed_on,omitempty"`
	DeclaredOn string `json:"declared_on"`
	DeclaredBy string `json:"declared_by"`
	Reviewed   string `json:"reviewed,omitempty"` // who in compliance reviewed it
}

// Relations for declared accounts.
var Relations = []string{"Self", "Spouse", "Parent", "Child", "Sibling", "Other dependant"}

// HoldingLine is one security held.
type HoldingLine struct {
	Holder   string `json:"holder"`
	Security string `json:"security"`
	ISIN     string `json:"isin,omitempty"`
	Qty      int64  `json:"qty"`
}

// HoldingsDecl is a holdings declaration: on joining, every year, on leaving.
type HoldingsDecl struct {
	ID         string        `json:"id"` // code|kind|period
	Employee   string        `json:"employee"`
	Kind       string        `json:"kind"`   // Joining, Annual, Leaving
	Period     string        `json:"period"` // the financial year (Annual) or the date
	Holdings   []HoldingLine `json:"holdings"`
	Nil        bool          `json:"nil"` // holds nothing
	DeclaredOn string        `json:"declared_on"`
	DeclaredBy string        `json:"declared_by"`
	Reviewed   string        `json:"reviewed,omitempty"`
}

// PreClearance is a request to trade, decided by the compliance officer.
type PreClearance struct {
	ID        string       `json:"id"`
	Employee  string       `json:"employee"`
	Name      string       `json:"name"`
	Account   string       `json:"account"` // a declared account's ID
	Security  string       `json:"security"`
	ISIN      string       `json:"isin,omitempty"`
	Side      string       `json:"side"` // Buy, Sell
	Qty       int64        `json:"qty"`
	Value     money.Amount `json:"value"` // approximate value
	Reason    string       `json:"reason,omitempty"`
	Needed    bool         `json:"needed"` // the firm's code needs pre-clearance for this trade
	Warnings  []string     `json:"warnings"`
	Status    string       `json:"status"` // Pending, Approved, Rejected, Withdrawn
	ValidTill string       `json:"valid_till,omitempty"`
	DecidedBy string       `json:"decided_by,omitempty"`
	Note      string       `json:"note,omitempty"`
	At        string       `json:"at"`
	// MFUnits: units of a mutual fund (the contra period is the code's
	// MF period, Schedule C cl. 8: two months).
	MFUnits bool `json:"mf_units,omitempty"`
	// NoUPSI is the applicant's declaration that they hold no unpublished
	// price sensitive information (Schedule C cl. 6), with its words.
	NoUPSI     bool   `json:"no_upsi"`
	NoUPSIText string `json:"no_upsi_text,omitempty"`
	// Contra: an opposite trade within the contra period; approving it
	// needs the compliance officer's written relaxation (Schedule C cl. 8).
	Contra        bool   `json:"contra,omitempty"`
	ContraRelaxed string `json:"contra_relaxed,omitempty"`
	// Report is what was done with the pre-clearance (pit.go): traded,
	// or decided not to trade (Schedule C cl. 9).
	Report *PreClearReport `json:"report,omitempty"`
}

// selfOrRole lets a person act on their own records, or the roles given.
func (o *Ops) selfOr(ctx context.Context, tenant, actor, code string) (Employee, bool, error) {
	code = strings.ToUpper(code)
	if me, ok := o.employeeOf(ctx, tenant, actor); ok && (me.Code == code || code == "") {
		return me, true, nil
	}
	if err := o.require(ctx, tenant, actor, "Keeping the own-trading register for others", RoleHR, RoleCompliance); err != nil {
		return Employee{}, false, err
	}
	e, err := o.employee(ctx, tenant, code)
	return e, false, err
}

// DeclareAccount records a trading or demat account of a person or their
// relative. The person themselves, HR or compliance.
func (o *Ops) DeclareAccount(ctx context.Context, tenant, actor, code string, a StaffAccount) (StaffAccount, error) {
	if err := o.needModule(ctx, tenant, ModHRMS); err != nil {
		return a, err
	}
	e, _, err := o.selfOr(ctx, tenant, actor, code)
	if err != nil {
		return a, err
	}
	p := o.hrPolicy(ctx, tenant).OwnTrading
	a.Holder, a.Broker, a.Account = strings.TrimSpace(a.Holder), strings.TrimSpace(a.Broker), strings.ToUpper(strings.TrimSpace(a.Account))
	if a.Relation == "" {
		a.Relation = "Self"
	}
	switch {
	case !contains(Relations, a.Relation):
		return a, invalid("relation is one of %s", strings.Join(Relations, ", "))
	case a.Relation != "Self" && !p.RelativesCovered:
		return a, invalid("the firm's code does not cover relatives' accounts")
	case a.Broker == "" || a.Account == "":
		return a, invalid("give the broker or DP and the account (client code or BO ID)")
	}
	if a.Holder == "" {
		a.Holder = e.Name
	}
	a.Employee = e.Code
	if a.ID == "" {
		n, err := o.next(ctx, tenant, "hr-tradeacct", actor)
		if err != nil {
			return a, err
		}
		a.ID = fmt.Sprintf("TA-%05d", n)
	}
	a.DeclaredOn, a.DeclaredBy, a.Reviewed = o.today(), actor, ""
	if err := docs.Upsert(ctx, o.Docs, tenant, cTradeAcct, a.ID, a, actor); err != nil {
		return a, err
	}
	o.log(ctx, tenant, actor, "hr.trading_account.declared", e.Code, map[string]any{"id": a.ID, "relation": a.Relation, "outside": a.Outside, "broker": a.Broker})
	if a.Outside {
		detail := "An account with another broker or DP was declared; review it against the firm's code."
		if !p.OutsideAllowed {
			detail = "The firm's code does not allow accounts with other brokers: ask the person to close it or move it to the firm, or record an exception."
		}
		o.raiseWork(ctx, tenant, wHR, e.Code+":outside-acct:"+a.ID, fmt.Sprintf("%s (%s) declared an outside account (%s)", e.Name, e.Code, a.Broker), detail, "", RoleCompliance, "people", o.today())
	}
	return a, nil
}

// DeclareHoldings records a holdings declaration.
func (o *Ops) DeclareHoldings(ctx context.Context, tenant, actor, code string, d HoldingsDecl) (HoldingsDecl, error) {
	if err := o.needModule(ctx, tenant, ModHRMS); err != nil {
		return d, err
	}
	e, _, err := o.selfOr(ctx, tenant, actor, code)
	if err != nil {
		return d, err
	}
	if !contains([]string{"Joining", "Annual", "Leaving"}, d.Kind) {
		return d, invalid("the declaration is Joining, Annual or Leaving")
	}
	if len(d.Holdings) == 0 && !d.Nil {
		return d, invalid("list the holdings, or declare that there are none")
	}
	for _, h := range d.Holdings {
		if strings.TrimSpace(h.Security) == "" || h.Qty <= 0 {
			return d, invalid("each holding needs the security and a quantity above zero")
		}
	}
	if d.Kind == "Annual" {
		d.Period = finYear(o.today())
	} else if d.Period == "" {
		d.Period = o.today()
	}
	d.Employee, d.ID, d.DeclaredOn, d.DeclaredBy = e.Code, e.Code+"|"+d.Kind+"|"+d.Period, o.today(), actor
	if d.Holdings == nil {
		d.Holdings = []HoldingLine{}
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cHoldDecl, d.ID, d, actor); err != nil {
		return d, err
	}
	o.log(ctx, tenant, actor, "hr.holdings.declared", e.Code, map[string]any{"kind": d.Kind, "period": d.Period, "lines": len(d.Holdings)})
	switch d.Kind {
	case "Joining":
		o.tickItem(ctx, tenant, actor, e.Code, "join", "trading", "holdings declared "+d.DeclaredOn)
	case "Leaving":
		o.tickItem(ctx, tenant, actor, e.Code, "leave", "holdings", "holdings declared "+d.DeclaredOn)
	case "Annual":
		o.closeWork(ctx, tenant, actor, wHR, e.Code+":annual-decl:"+d.Period, "declared")
	}
	return d, nil
}

// needsPreClear: the firm's code needs this person to pre-clear a trade of
// this value.
func needsPreClear(p OwnTradingPolicy, e Employee, value money.Amount) bool {
	if p.Applies != "all" && !e.Designated {
		return false
	}
	return p.PreClearAbove <= 0 || value > money.Amount(p.PreClearAbove*100)
}

// RequestPreClearance asks to trade. A security on the restricted list is
// refused at once; an opposite trade within the contra period is flagged.
func (o *Ops) RequestPreClearance(ctx context.Context, tenant, actor string, r PreClearance) (PreClearance, error) {
	if err := o.needModule(ctx, tenant, ModHRMS); err != nil {
		return r, err
	}
	e, ok := o.employeeOf(ctx, tenant, actor)
	if !ok {
		return r, fmt.Errorf("%w: your login is not linked to an employee", ErrForbidden)
	}
	p := o.hrPolicy(ctx, tenant).OwnTrading
	r.Security, r.ISIN = strings.ToUpper(strings.TrimSpace(r.Security)), strings.ToUpper(strings.TrimSpace(r.ISIN))
	if r.Security == "" || r.Qty <= 0 || (r.Side != "Buy" && r.Side != "Sell") {
		return r, invalid("give the security, Buy or Sell and a quantity")
	}
	if !r.NoUPSI {
		return r, invalid("declare that you hold no unpublished price sensitive information (the code needs it with every request)")
	}
	r.NoUPSIText = NoUPSIDeclaration
	if a, _, err := docs.Get[StaffAccount](ctx, o.Docs, tenant, cTradeAcct, r.Account); err != nil || a.Employee != e.Code || a.ClosedOn != "" {
		return r, invalid("choose one of your declared accounts (declare the account first)")
	}
	for _, x := range p.Restricted {
		if x = strings.ToUpper(strings.TrimSpace(x)); x != "" && (x == r.Security || x == r.ISIN) {
			return r, invalid("%s is on the firm's restricted list: trading in it is not allowed now", r.Security)
		}
	}
	r.Warnings = []string{}
	if days := p.contraDays(r.MFUnits); days > 0 {
		ps, _ := docs.List[PreClearance](ctx, o.Docs, tenant, cPreClear, "")
		from := o.now().AddDate(0, 0, -days).Format("2006-01-02")
		for _, x := range ps {
			if x.Employee != e.Code || x.Status != "Approved" || x.Side == r.Side || !(x.Security == r.Security || x.ISIN != "" && x.ISIN == r.ISIN) {
				continue
			}
			// The trade itself (from the post-trade report) when known;
			// a pre-clearance not traded on is no prior trade.
			on := x.At[:10]
			if x.Report != nil {
				if !x.Report.Traded {
					continue
				}
				on = x.Report.TradeDate
			}
			if on >= from {
				r.Contra = true
				r.Warnings = append(r.Warnings, fmt.Sprintf("Contra trade: a %s of %s on %s, within %d days. It can be approved only with the compliance officer's written relaxation.", strings.ToLower(x.Side), x.Security, fmtDay(on), days))
			}
		}
	}
	n, err := o.next(ctx, tenant, "hr-preclear", actor)
	if err != nil {
		return r, err
	}
	r.ID, r.Employee, r.Name, r.Status, r.At, r.Needed = fmt.Sprintf("PC-%05d", n), e.Code, e.Name, "Pending", o.now().Format(time.RFC3339), needsPreClear(p, e, r.Value)
	r.DecidedBy, r.ValidTill = "", ""
	if _, err := docs.Put(ctx, o.Docs, tenant, cPreClear, r.ID, r, 0, actor); err != nil {
		return r, err
	}
	o.log(ctx, tenant, actor, "hr.preclear.requested", r.ID, map[string]any{"security": r.Security, "side": r.Side, "qty": r.Qty, "value": r.Value.String()})
	o.raiseWork(ctx, tenant, wHR, "preclear:"+r.ID, fmt.Sprintf("Pre-clearance %s: %s %s %d %s", r.ID, e.Name, strings.ToLower(r.Side), r.Qty, r.Security),
		"Decide the request on People → Own trading."+map[bool]string{true: " " + strings.Join(r.Warnings, " "), false: ""}[len(r.Warnings) > 0], "", RoleCompliance, "people", o.today())
	return r, nil
}

// DecidePreClearance approves or rejects. The compliance officer, never
// for their own request.
func (o *Ops) DecidePreClearance(ctx context.Context, tenant, actor, id string, approve bool, note string) (PreClearance, error) {
	if err := o.needModule(ctx, tenant, ModHRMS); err != nil {
		return PreClearance{}, err
	}
	if err := o.require(ctx, tenant, actor, "Deciding pre-clearance of staff trades", RoleCompliance); err != nil {
		return PreClearance{}, err
	}
	p := o.hrPolicy(ctx, tenant).OwnTrading
	set, _ := o.Settings(ctx, tenant)
	me, _ := o.employeeOf(ctx, tenant, actor)
	r, err := docs.Update(ctx, o.Docs, tenant, cPreClear, id, actor, func(r *PreClearance) error {
		if r.Status != "Pending" {
			return fmt.Errorf("%w: %s is %s", ErrState, r.ID, strings.ToLower(r.Status))
		}
		if me.Code != "" && me.Code == r.Employee {
			return fmt.Errorf("%w: someone else decides your own pre-clearance", ErrForbidden)
		}
		if !approve && strings.TrimSpace(note) == "" {
			return invalid("give the reason for rejecting")
		}
		if approve && r.Contra {
			if len(strings.TrimSpace(note)) < 10 {
				return invalid("this is a contra trade: approve it only with a written relaxation in the note (why, under the code)")
			}
			r.ContraRelaxed = trim(strings.TrimSpace(note), 300)
		}
		r.Status, r.DecidedBy, r.Note = map[bool]string{true: "Approved", false: "Rejected"}[approve], actor, trim(note, 300)
		if approve {
			r.ValidTill = preClearValidTill(set, o.today(), p.PreClearValidDays)
		}
		return nil
	})
	if err != nil {
		return r, err
	}
	o.closeWork(ctx, tenant, actor, wHR, "preclear:"+r.ID, strings.ToLower(r.Status))
	o.log(ctx, tenant, actor, "hr.preclear."+strings.ToLower(r.Status), r.ID, map[string]any{"employee": r.Employee, "note": r.Note, "valid_till": r.ValidTill, "contra_relaxed": r.ContraRelaxed != ""})
	return r, nil
}

// OwnTradingView is the register, or one person's part of it.
type OwnTradingView struct {
	Accounts     []StaffAccount   `json:"accounts"`
	Declarations []HoldingsDecl   `json:"declarations"`
	PreClear     []PreClearance   `json:"pre_clearances"`
	Policy       OwnTradingPolicy `json:"policy"`
	Missing      []string         `json:"missing_annual"` // people without this year's annual declaration (register only)
	// The insider trading code's other records (pit.go).
	Disclosures   []PITDisclosure `json:"disclosures"`
	MissingDiscl  []string        `json:"missing_disclosures"` // designated persons without this year's relatives disclosure
	NoBasis       []string        `json:"designated_without_basis"`
	NoReport      []string        `json:"pre_clearances_without_report"`
	UPSI          []UPSIShare     `json:"upsi_register,omitempty"` // compliance only
	NoUPSIText    string          `json:"no_upsi_text"`
	DesignatedFor []string        `json:"designation_grounds"`
}

// OwnTrading returns the register (compliance, HR) or, with self, the
// actor's own part.
func (o *Ops) OwnTrading(ctx context.Context, tenant, actor string, self bool) (OwnTradingView, error) {
	if err := o.needModule(ctx, tenant, ModHRMS); err != nil {
		return OwnTradingView{}, err
	}
	only := ""
	if self {
		me, ok := o.employeeOf(ctx, tenant, actor)
		if !ok {
			return OwnTradingView{}, fmt.Errorf("%w: your login is not linked to an employee", ErrForbidden)
		}
		only = me.Code
	} else if err := o.require(ctx, tenant, actor, "Seeing the own-trading register", RoleCompliance, RoleHR); err != nil {
		return OwnTradingView{}, err
	}
	v := OwnTradingView{Accounts: []StaffAccount{}, Declarations: []HoldingsDecl{}, PreClear: []PreClearance{}, Policy: o.hrPolicy(ctx, tenant).OwnTrading, Missing: []string{}}
	as, _ := docs.List[StaffAccount](ctx, o.Docs, tenant, cTradeAcct, "")
	for _, a := range as {
		if only == "" || a.Employee == only {
			v.Accounts = append(v.Accounts, a)
		}
	}
	ds, _ := docs.List[HoldingsDecl](ctx, o.Docs, tenant, cHoldDecl, "")
	have := map[string]bool{}
	fy := finYear(o.today())
	for _, d := range ds {
		if only == "" || d.Employee == only {
			v.Declarations = append(v.Declarations, d)
		}
		if d.Kind == "Annual" && d.Period == fy {
			have[d.Employee] = true
		}
	}
	ps, _ := docs.List[PreClearance](ctx, o.Docs, tenant, cPreClear, "")
	for i := len(ps) - 1; i >= 0; i-- {
		if only == "" || ps[i].Employee == only {
			v.PreClear = append(v.PreClear, ps[i])
		}
	}
	if only == "" {
		es, _ := o.employees(ctx, tenant)
		for _, e := range es {
			if e.Status != EmpLeft && (v.Policy.Applies == "all" || e.Designated) && !have[e.Code] {
				v.Missing = append(v.Missing, e.Code)
			}
		}
	}
	o.pitView(ctx, tenant, actor, only, &v)
	return v, nil
}

// annualDeclTick: from the first day of the month the annual declaration
// is due in, each person the code covers who has not declared this
// financial year gets a reminder (a work item for compliance to chase).
func (o *Ops) annualDeclTick(ctx context.Context, tenant string, now time.Time) {
	now = now.In(ist)
	p := o.hrPolicy(ctx, tenant).OwnTrading
	if int(now.Month()) != p.AnnualDueMonth || now.Day() != 1 {
		return
	}
	fy := finYear(now.Format("2006-01-02"))
	ds, _ := docs.List[HoldingsDecl](ctx, o.Docs, tenant, cHoldDecl, "")
	have := map[string]bool{}
	for _, d := range ds {
		if d.Kind == "Annual" && d.Period == fy {
			have[d.Employee] = true
		}
	}
	es, _ := o.employees(ctx, tenant)
	for _, e := range es {
		if e.Status == EmpLeft || have[e.Code] || (p.Applies != "all" && !e.Designated) {
			continue
		}
		o.raiseWork(ctx, tenant, wHR, e.Code+":annual-decl:"+fy, fmt.Sprintf("Annual holdings declaration %s due from %s (%s)", fy, e.Name, e.Code),
			fmt.Sprintf("Due by the end of %s under the firm's code (%s). A designated person also gives the yearly disclosure of immediate relatives and persons with a material financial relationship (Schedule C cl. 12).", now.Month(), p.Basis), "", RoleCompliance, "people", now.Format("2006-01-02"))
	}
}
