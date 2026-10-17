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

// ---- client portal ----
//
// A client signs in with their own user ID (their UCC in lower case), a
// one-time password from the firm and, when the firm requires it, a
// two-factor code. A login can cover a family: accounts that share the
// client's email or mobile, or are declared as family (spouse, dependent
// children, dependent parents, as the exchange circulars allow). A client
// login sees only those accounts, and only the client-facing screens.

// RoleClient is the role of a client's portal login.
const RoleClient = "Client"

const (
	cChange  = "change"
	cTicket  = "ticket"
	cExtTrd  = "exttrade"
	cLead    = "lead"
	cCallLog = "calllog"
)

// PortalAccess grants (or extends) a client's portal login. It returns the
// user ID and, for a new login, a one-time password to give the client.
func (o *Ops) PortalAccess(ctx context.Context, tenant, actor, ucc string, family []string, declared bool) (string, string, error) {
	if err := o.require(ctx, tenant, actor, "Giving clients portal access", RoleChecker, RoleAdmin); err != nil {
		return "", "", err
	}
	ucc = strings.ToUpper(ucc)
	c, err := o.Client(ctx, tenant, ucc)
	if err != nil {
		return "", "", err
	}
	if c.Status != "Active" {
		return "", "", invalid("%s is not active yet", ucc)
	}
	accts := []string{ucc}
	for _, f := range family {
		f = strings.ToUpper(strings.TrimSpace(f))
		if f == "" || f == ucc || contains(accts, f) {
			continue
		}
		fc, err := o.Client(ctx, tenant, f)
		if err != nil || fc.Status != "Active" {
			return "", "", invalid("family account %s is not an active client", f)
		}
		shared := (fc.Email != "" && strings.EqualFold(fc.Email, c.Email)) || (fc.Mobile != "" && fc.Mobile == c.Mobile)
		if !shared && !declared {
			return "", "", invalid("%s does not share %s's email or mobile; record the family declaration to link it", f, ucc)
		}
		accts = append(accts, f)
	}
	id := strings.ToLower(ucc)
	if u, err := o.user(ctx, tenant, id); err == nil {
		if u.Role != RoleClient {
			return "", "", invalid("user ID %s is taken by a staff login", id)
		}
		u.Clients, u.Active = accts, true
		if err := o.PutUser(ctx, tenant, actor, u); err != nil {
			return "", "", err
		}
		o.log(ctx, tenant, actor, "portal.family", id, map[string]any{"accounts": accts, "declared": declared})
		return id, "", nil
	}
	if err := o.PutUser(ctx, tenant, actor, User{ID: id, Name: c.Name, Role: RoleClient, Active: true, Clients: accts}); err != nil {
		return "", "", err
	}
	pw := TempPassword()
	if err := o.SetPassword(ctx, tenant, actor, id, pw, true); err != nil {
		return "", "", err
	}
	o.log(ctx, tenant, actor, "portal.granted", id, map[string]any{"accounts": accts, "declared": declared})
	return id, pw, nil
}

// PortalAccount is what a client sees about one of their accounts.
type PortalAccount struct {
	UCC         string            `json:"ucc"`
	Name        string            `json:"name"`
	PAN         string            `json:"pan"`
	Email       string            `json:"email"`
	Mobile      string            `json:"mobile"`
	City        string            `json:"city"`
	Bank        BankAccount       `json:"bank"`
	Demat       Demat             `json:"demat"`
	Segments    []string          `json:"segments"`
	Nominee     string            `json:"nominee"`
	NomineeName string            `json:"nominee_name,omitempty"`
	RAS         string            `json:"ras"`
	Blocked     bool              `json:"blocked"`
	Inactive    bool              `json:"inactive"`
	Names       map[string]string `json:"segment_names"`
}

// PortalView is a client's home screen.
type PortalView struct {
	Accounts     []PortalAccount        `json:"accounts"`
	Account      PortalAccount          `json:"account"`
	Ledger       money.Amount           `json:"ledger"`
	Withdrawable Withdrawable           `json:"withdrawable"`
	Holdings     []HoldingView          `json:"holdings"`
	HoldValue    money.Dec              `json:"holdings_value"`
	Positions    []PositionView         `json:"positions"`
	Margin       *RiskRow               `json:"margin,omitempty"`
	Notes        []ContractNote         `json:"notes"`
	Payouts      []PayoutRequest        `json:"payouts"`
	Changes      []ChangeRequest        `json:"changes"`
	Tickets      []Ticket               `json:"tickets"`
	Ledgerlines  []ledger.StatementLine `json:"ledger_lines"`
	Firm         map[string]string      `json:"firm"`
}

func portalAccount(c Client) PortalAccount {
	b := c.Bank
	b.Account = MaskAccount(b.Account)
	return PortalAccount{UCC: c.UCC, Name: c.Name, PAN: MaskPAN(c.PAN), Email: c.Email, Mobile: c.Mobile, City: c.City, Bank: b, Demat: c.Demat,
		Segments: c.Segments, Nominee: c.Nominee, NomineeName: c.NomineeName, RAS: c.RAS, Blocked: c.Blocked, Inactive: c.Inactive, Names: SegmentNames}
}

// PortalAccounts lists the accounts a user may see in the portal.
func (o *Ops) PortalAccounts(ctx context.Context, tenant, actor string) ([]string, error) {
	u, err := o.user(ctx, tenant, actor)
	if err != nil {
		return nil, err
	}
	if u.Role != RoleClient {
		return nil, fmt.Errorf("%w: the client portal is for client logins", ErrForbidden)
	}
	return u.Clients, nil
}

// Portal builds a client's home screen for one of their accounts.
func (o *Ops) Portal(ctx context.Context, tenant, actor, date, ucc string) (PortalView, error) {
	v := PortalView{Holdings: []HoldingView{}, Positions: []PositionView{}, Notes: []ContractNote{}, Payouts: []PayoutRequest{}, Changes: []ChangeRequest{}, Tickets: []Ticket{}, Ledgerlines: []ledger.StatementLine{}}
	accts, err := o.PortalAccounts(ctx, tenant, actor)
	if err != nil {
		return v, err
	}
	ucc = strings.ToUpper(ucc)
	if ucc == "" && len(accts) > 0 {
		ucc = accts[0]
	}
	if !contains(accts, ucc) {
		return v, ErrNotFound
	}
	for _, a := range accts {
		if c, err := o.Client(ctx, tenant, a); err == nil {
			v.Accounts = append(v.Accounts, portalAccount(c))
		}
	}
	d, err := o.ClientDetail(ctx, tenant, ucc, date)
	if err != nil {
		return v, err
	}
	v.Account = portalAccount(d.Client)
	v.Holdings, v.Positions = d.Holdings, d.Positions
	for _, h := range d.Holdings {
		v.HoldValue = v.HoldValue.Add(h.Value)
	}
	v.Margin = d.Risk
	lines := d.Statement.Lines
	if len(lines) > 15 {
		lines = lines[len(lines)-15:]
	}
	v.Ledgerlines = lines
	if len(d.Statement.Lines) > 0 {
		v.Ledger = -d.Statement.Lines[len(d.Statement.Lines)-1].Balance
	}
	v.Withdrawable, _ = o.Withdrawable(ctx, tenant, date, ucc)
	ns, _ := docs.List[ContractNote](ctx, o.Docs, tenant, cNote, "")
	for _, n := range ns {
		if n.UCC == ucc {
			v.Notes = append(v.Notes, n)
		}
	}
	sort.Slice(v.Notes, func(i, j int) bool { return v.Notes[i].Date+v.Notes[i].No > v.Notes[j].Date+v.Notes[j].No })
	ps, _ := docs.List[PayoutRequest](ctx, o.Docs, tenant, cPayout, "")
	for _, p := range ps {
		if p.UCC == ucc {
			v.Payouts = append(v.Payouts, p)
		}
	}
	sort.Slice(v.Payouts, func(i, j int) bool { return v.Payouts[i].ID > v.Payouts[j].ID })
	cr, _ := docs.List[ChangeRequest](ctx, o.Docs, tenant, cChange, "")
	for _, c := range cr {
		if c.UCC == ucc {
			v.Changes = append(v.Changes, c.public())
		}
	}
	sort.Slice(v.Changes, func(i, j int) bool { return v.Changes[i].ID > v.Changes[j].ID })
	ts, _ := docs.List[Ticket](ctx, o.Docs, tenant, cTicket, "")
	for _, t := range ts {
		if contains(accts, t.UCC) {
			t.Internal = nil
			v.Tickets = append(v.Tickets, t)
		}
	}
	sort.Slice(v.Tickets, func(i, j int) bool { return v.Tickets[i].ID > v.Tickets[j].ID })
	set, _ := o.Settings(ctx, tenant)
	v.Firm = map[string]string{"name": set.Firm, "sebi_reg": set.SEBIReg, "compliance_email": set.ComplianceEmail, "payin_account": set.PayoutDebitAccount}
	return v, nil
}

// ---- profile change requests ----

// ChangeKinds are the details a client may ask to change.
var ChangeKinds = []string{"Email", "Mobile", "Address", "Bank account", "Nomination"}

// ChangeRequest is a request to change a client's KYC or account details,
// applied when a head-office checker approves it.
type ChangeRequest struct {
	ID      string            `json:"id"`
	UCC     string            `json:"ucc"`
	Name    string            `json:"name"`
	Kind    string            `json:"kind"`
	New     map[string]string `json:"new"`
	Old     map[string]string `json:"old"`
	Doc     string            `json:"doc,omitempty"` // supporting document ID
	Source  string            `json:"source"`        // Portal, Branch, Head office
	Status  string            `json:"status"`        // Pending, Approved, Rejected
	By      string            `json:"by"`
	At      string            `json:"at"`
	Checker string            `json:"checker,omitempty"`
	Reason  string            `json:"reason,omitempty"`
	Decided string            `json:"decided_at,omitempty"`
}

func changeOld(c Client, kind string) map[string]string {
	switch kind {
	case "Email":
		return map[string]string{"email": c.Email}
	case "Mobile":
		return map[string]string{"mobile": c.Mobile}
	case "Address":
		return map[string]string{"city": c.City, "state": c.State}
	case "Bank account":
		return map[string]string{"bank": c.Bank.Bank, "ifsc": c.Bank.IFSC, "account": MaskAccount(c.Bank.Account)}
	case "Nomination":
		return map[string]string{"nominee": c.Nominee, "nominee_name": c.NomineeName}
	}
	return map[string]string{}
}

// applyChange changes the client as the request asks, and validates.
func applyChange(c *Client, r ChangeRequest) error {
	n := r.New
	switch r.Kind {
	case "Email":
		c.Email = strings.TrimSpace(n["email"])
	case "Mobile":
		c.Mobile = strings.TrimSpace(n["mobile"])
	case "Address":
		c.City = strings.TrimSpace(n["city"])
		if s := strings.TrimSpace(n["state"]); s != "" {
			c.State = s
		}
	case "Bank account":
		c.Bank = BankAccount{Bank: strings.TrimSpace(n["bank"]), IFSC: strings.ToUpper(strings.TrimSpace(n["ifsc"])), Account: strings.TrimSpace(n["account"])}
	case "Nomination":
		c.Nominee = n["nominee"]
		c.NomineeName = strings.TrimSpace(n["nominee_name"])
		if c.Nominee == "Opted out" {
			c.NomineeName = ""
		} else if c.Nominee != "Opted in" {
			return invalid("nomination must be Opted in or Opted out")
		} else if c.NomineeName == "" {
			return invalid("enter the nominee's name")
		}
	default:
		return invalid("change must be one of %s", strings.Join(ChangeKinds, ", "))
	}
	c.Normalise()
	return c.Validate()
}

// RequestChange records a change request from the portal or from staff.
func (o *Ops) RequestChange(ctx context.Context, tenant, actor, ucc, kind string, values map[string]string, doc string) (ChangeRequest, error) {
	role, err := o.RoleOf(ctx, tenant, actor)
	if err != nil {
		return ChangeRequest{}, err
	}
	ucc = strings.ToUpper(ucc)
	src := "Head office"
	switch role {
	case RoleClient:
		accts, _ := o.PortalAccounts(ctx, tenant, actor)
		if !contains(accts, ucc) {
			return ChangeRequest{}, ErrNotFound
		}
		src = "Portal"
	case RoleBranch, RoleAP:
		src = "Branch"
	case RoleMaker, RoleChecker, RoleAdmin, RoleCompliance:
	default:
		return ChangeRequest{}, fmt.Errorf("%w: %s cannot request client changes", ErrForbidden, role)
	}
	c, err := o.Client(ctx, tenant, ucc)
	if err != nil {
		return ChangeRequest{}, err
	}
	r := ChangeRequest{UCC: ucc, Name: c.Name, Kind: kind, New: values, Old: changeOld(c, kind), Doc: doc, Source: src, Status: "Pending", By: actor, At: o.now().Format(time.RFC3339)}
	if r.New == nil {
		r.New = map[string]string{}
	}
	trial := c
	if err := applyChange(&trial, r); err != nil {
		return r, err
	}
	if fmt.Sprint(changeOld(trial, kind)) == fmt.Sprint(r.Old) && kind != "Bank account" {
		return r, invalid("nothing would change")
	}
	if kind == "Bank account" || kind == "Address" {
		need := map[string]string{"Bank account": "Bank proof", "Address": "Address proof"}[kind]
		ds, _ := o.ClientDocs(ctx, tenant, ucc)
		ok := false
		for _, d := range ds {
			ok = ok || (d.ID == doc && d.Kind == need)
		}
		if !ok {
			return r, invalid("attach a %s document (cancelled cheque or bank statement for a bank account, a valid address proof for an address)", strings.ToLower(need))
		}
	}
	if kind == "Bank account" {
		// Keep the full account number in the request for the checker, masked in what is shown.
		r.New["account_masked"] = MaskAccount(r.New["account"])
	}
	all, _ := docs.List[ChangeRequest](ctx, o.Docs, tenant, cChange, "")
	for _, x := range all {
		if x.UCC == ucc && x.Kind == kind && x.Status == "Pending" {
			return r, invalid("a %s change for %s is already waiting for approval (%s)", strings.ToLower(kind), ucc, x.ID)
		}
	}
	n, err := o.next(ctx, tenant, "change", actor)
	if err != nil {
		return r, err
	}
	r.ID = fmt.Sprintf("CHG-%05d", n)
	if _, err := docs.Put(ctx, o.Docs, tenant, cChange, r.ID, r, 0, actor); err != nil {
		return r, err
	}
	o.log(ctx, tenant, actor, "change.requested", r.ID, map[string]any{"ucc": ucc, "kind": kind, "source": src})
	return r.public(), nil
}

func (r ChangeRequest) public() ChangeRequest {
	if a, ok := r.New["account"]; ok {
		n := map[string]string{}
		for k, v := range r.New {
			n[k] = v
		}
		n["account"] = MaskAccount(a)
		r.New = n
	}
	return r
}

// ChangeRequests lists change requests, newest first.
func (o *Ops) ChangeRequests(ctx context.Context, tenant string) ([]ChangeRequest, error) {
	rs, err := docs.List[ChangeRequest](ctx, o.Docs, tenant, cChange, "")
	for i := range rs {
		rs[i] = rs[i].public()
	}
	sort.Slice(rs, func(i, j int) bool { return rs[i].ID > rs[j].ID })
	return rs, err
}

// DecideChange approves (applying the change) or rejects a request. The
// checker must not be the person who raised it.
func (o *Ops) DecideChange(ctx context.Context, tenant, actor, id string, approve bool, reason string) (ChangeRequest, error) {
	if err := o.require(ctx, tenant, actor, "Approving client changes", RoleChecker); err != nil {
		return ChangeRequest{}, err
	}
	r, _, err := docs.Get[ChangeRequest](ctx, o.Docs, tenant, cChange, id)
	if err != nil {
		return r, err
	}
	if r.Status != "Pending" {
		return r.public(), fmt.Errorf("%w: %s is already %s", ErrState, id, strings.ToLower(r.Status))
	}
	if r.By == actor {
		return r.public(), fmt.Errorf("%w: the person who raised a change cannot approve it", ErrForbidden)
	}
	if !approve && strings.TrimSpace(reason) == "" {
		return r.public(), invalid("give the reason for rejecting")
	}
	date := o.now().Format("2006-01-02")
	if approve {
		if _, err := docs.Update(ctx, o.Docs, tenant, cClient, r.UCC, actor, func(c *Client) error {
			if err := applyChange(c, r); err != nil {
				return err
			}
			// A change of KYC details goes to the KRA, and counts as
			// account activity under the inactivity rules.
			c.KYCUpdatedOn = date
			return nil
		}); err != nil {
			return r.public(), err
		}
	}
	r, err = docs.Update(ctx, o.Docs, tenant, cChange, id, actor, func(x *ChangeRequest) error {
		x.Status, x.Checker, x.Reason, x.Decided = map[bool]string{true: "Approved", false: "Rejected"}[approve], actor, reason, o.now().Format(time.RFC3339)
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "change."+strings.ToLower(r.Status), id, map[string]any{"ucc": r.UCC, "kind": r.Kind, "reason": reason})
		o.notify(ctx, tenant, actor, "change."+strings.ToLower(r.Status), r.UCC, map[string]string{"kind": r.Kind, "id": r.ID, "reason": reason})
	}
	return r.public(), err
}

// ---- trades through other brokers ----

// ExternalTrade is an equity trade the client made through another
// broker, entered so that their capital gains report is complete.
type ExternalTrade struct {
	ID     string    `json:"id"`
	UCC    string    `json:"ucc"`
	Date   string    `json:"date"`
	ISIN   string    `json:"isin"`
	Symbol string    `json:"symbol"`
	Side   string    `json:"side"` // B or S
	Qty    int64     `json:"qty"`
	Price  money.Dec `json:"price"`
	Broker string    `json:"broker"`
	By     string    `json:"by"`
}

// AddExternalTrade records a trade made elsewhere.
func (o *Ops) AddExternalTrade(ctx context.Context, tenant, actor string, t ExternalTrade) (ExternalTrade, error) {
	role, err := o.RoleOf(ctx, tenant, actor)
	if err != nil {
		return t, err
	}
	t.UCC = strings.ToUpper(t.UCC)
	if role == RoleClient {
		accts, _ := o.PortalAccounts(ctx, tenant, actor)
		if !contains(accts, t.UCC) {
			return t, ErrNotFound
		}
	} else if err := o.require(ctx, tenant, actor, "Entering outside trades", RoleMaker, RoleChecker, RoleBranch); err != nil {
		return t, err
	}
	if _, err := o.Client(ctx, tenant, t.UCC); err != nil {
		return t, err
	}
	t.Symbol, t.ISIN, t.Side = strings.ToUpper(strings.TrimSpace(t.Symbol)), strings.ToUpper(strings.TrimSpace(t.ISIN)), strings.ToUpper(t.Side)
	switch {
	case !ledger.ValidDate(t.Date) || t.Date > o.now().Format("2006-01-02"):
		return t, invalid("enter the trade date (not in the future)")
	case t.Symbol == "" && t.ISIN == "":
		return t, invalid("enter the scrip or ISIN")
	case t.Side != "B" && t.Side != "S":
		return t, invalid("side must be B (buy) or S (sell)")
	case t.Qty <= 0:
		return t, invalid("quantity must be above zero")
	case t.Price.Sign() <= 0:
		return t, invalid("price must be above zero")
	case strings.TrimSpace(t.Broker) == "":
		return t, invalid("enter the broker it was traded through")
	}
	if t.ISIN == "" {
		_, look, _ := o.Market(ctx, tenant, o.now().Format("2006-01-02"))
		if i, ok := look.Get("NSE", "CM", t.Symbol); ok {
			t.ISIN = i.ISIN
		}
		if t.ISIN == "" {
			hs, _ := o.Holdings(ctx, tenant, t.UCC)
			for _, h := range hs {
				if h.Symbol == t.Symbol {
					t.ISIN = h.ISIN
				}
			}
		}
		if t.ISIN == "" {
			t.ISIN = t.Symbol
		}
	}
	n, err := o.next(ctx, tenant, "exttrade", actor)
	if err != nil {
		return t, err
	}
	t.ID, t.By = fmt.Sprintf("EXT-%06d", n), actor
	if _, err := docs.Put(ctx, o.Docs, tenant, cExtTrd, t.UCC+"|"+t.ID, t, 0, actor); err != nil {
		return t, err
	}
	o.log(ctx, tenant, actor, "external_trade.added", t.ID, map[string]any{"ucc": t.UCC, "symbol": t.Symbol, "side": t.Side, "qty": t.Qty})
	return t, nil
}

// ExternalTrades lists a client's outside trades by date.
func (o *Ops) ExternalTrades(ctx context.Context, tenant, ucc string) ([]ExternalTrade, error) {
	all, err := docs.List[ExternalTrade](ctx, o.Docs, tenant, cExtTrd, strings.ToUpper(ucc)+"|")
	ts := []ExternalTrade{}
	for _, t := range all {
		if t.Qty > 0 {
			ts = append(ts, t)
		}
	}
	sort.Slice(ts, func(i, j int) bool { return ts[i].Date+ts[i].ID < ts[j].Date+ts[j].ID })
	return ts, err
}

// DeleteExternalTrade removes an outside trade entered by mistake.
func (o *Ops) DeleteExternalTrade(ctx context.Context, tenant, actor, ucc, id string) error {
	role, err := o.RoleOf(ctx, tenant, actor)
	if err != nil {
		return err
	}
	ucc = strings.ToUpper(ucc)
	if role == RoleClient {
		accts, _ := o.PortalAccounts(ctx, tenant, actor)
		if !contains(accts, ucc) {
			return ErrNotFound
		}
	}
	t, _, err := docs.Get[ExternalTrade](ctx, o.Docs, tenant, cExtTrd, ucc+"|"+id)
	if err != nil {
		return err
	}
	t.Qty = 0 // kept for the audit trail, ignored in reports
	if err := docs.Upsert(ctx, o.Docs, tenant, cExtTrd, ucc+"|"+id, t, actor); err != nil {
		return err
	}
	o.log(ctx, tenant, actor, "external_trade.deleted", id, map[string]any{"ucc": ucc})
	return nil
}
