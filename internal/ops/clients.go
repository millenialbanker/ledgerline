package ops

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// Onboarding stages, in order.
var Stages = []string{"Application", "KYC verification", "In-person verification", "UCC upload", "Active"}

// Segment names.
var SegmentNames = map[string]string{"CM": "Equity cash", "FO": "Equity F&O", "CD": "Currency", "COM": "Commodity"}

// BankAccount is a client's registered bank account.
type BankAccount struct {
	Bank    string `json:"bank"`
	IFSC    string `json:"ifsc"`
	Account string `json:"account"`
}

// Demat is a client's demat account.
type Demat struct {
	Depository string `json:"depository"`
	ID         string `json:"id"`
}

// Client is a trading client.
type Client struct {
	UCC            string       `json:"ucc"`
	Name           string       `json:"name"`
	Type           string       `json:"type"`
	PAN            string       `json:"pan"`
	DOB            string       `json:"dob,omitempty"`
	Mobile         string       `json:"mobile"`
	Email          string       `json:"email"`
	City           string       `json:"city"`
	Status         string       `json:"status"`
	Segments       []string     `json:"segments"`
	Risk           string       `json:"risk"`
	Income         string       `json:"income"`
	RAS            string       `json:"ras"`
	Bank           BankAccount  `json:"bank"`
	Demat          Demat        `json:"demat"`
	Nominee        string       `json:"nominee"`
	NomineeName    string       `json:"nominee_name,omitempty"`
	KYCDate        string       `json:"kyc_date"`
	KYCRoute       string       `json:"kyc_route,omitempty"`
	IncomeProof    bool         `json:"income_proof"`
	RiskDisclosure bool         `json:"risk_disclosure"`
	IPVScheduled   bool         `json:"ipv_scheduled"`
	API            bool         `json:"api"`
	UPIBlock       money.Amount `json:"upi_block"`
	Blocked        bool         `json:"blocked"`
	BlockedReason  string       `json:"blocked_reason,omitempty"`
	LastTrade      string       `json:"last_trade,omitempty"`
	CreatedBy      string       `json:"created_by,omitempty"`
	ActivatedBy    string       `json:"activated_by,omitempty"`
}

var (
	rePAN    = regexp.MustCompile(`^[A-Z]{5}[0-9]{4}[A-Z]$`)
	reIFSC   = regexp.MustCompile(`^[A-Z]{4}0[A-Z0-9]{6}$`)
	reMobile = regexp.MustCompile(`^[6-9][0-9]{9}$`)
	reEmail  = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
	reAcct   = regexp.MustCompile(`^[0-9]{9,18}$`)
	reUCC    = regexp.MustCompile(`^[A-Z0-9]{3,10}$`)
)

// FieldError is one validation problem on a form field.
type FieldError struct {
	Field string `json:"field"`
	Msg   string `json:"message"`
}

// ValidationError collects field errors.
type ValidationError struct{ Fields []FieldError }

func (v *ValidationError) Error() string {
	var s []string
	for _, f := range v.Fields {
		s = append(s, f.Field+": "+f.Msg)
	}
	return "invalid: " + strings.Join(s, "; ")
}
func (v *ValidationError) Unwrap() error { return ErrInvalid }

// Normalise cleans user input in place.
func (c *Client) Normalise() {
	c.UCC = strings.ToUpper(strings.TrimSpace(c.UCC))
	c.Name = strings.Join(strings.Fields(c.Name), " ")
	c.PAN = strings.ToUpper(strings.TrimSpace(c.PAN))
	c.Bank.IFSC = strings.ToUpper(strings.TrimSpace(c.Bank.IFSC))
	c.Bank.Account = strings.TrimSpace(c.Bank.Account)
	c.Email = strings.TrimSpace(strings.ToLower(c.Email))
	c.Mobile = strings.TrimSpace(c.Mobile)
	c.City = strings.TrimSpace(c.City)
	if c.Type == "" {
		c.Type = "Individual"
	}
	if c.RAS == "" {
		c.RAS = "Quarterly"
	}
	if c.Risk == "" {
		c.Risk = "Medium"
	}
	if c.Nominee == "" {
		c.Nominee = "Opted out"
	}
	if c.Demat.Depository == "" {
		c.Demat.Depository = "CDSL"
	}
	seen := map[string]bool{}
	var segs []string
	for _, s := range c.Segments {
		s = strings.ToUpper(strings.TrimSpace(s))
		if SegmentNames[s] != "" && !seen[s] {
			segs = append(segs, s)
			seen[s] = true
		}
	}
	sort.Slice(segs, func(i, j int) bool { return segOrder(segs[i]) < segOrder(segs[j]) })
	c.Segments = segs
}

func segOrder(s string) int { return strings.Index("CM FO CD COM", s) }

// Validate checks a client record.
func (c Client) Validate() error {
	var fe []FieldError
	add := func(f, m string) { fe = append(fe, FieldError{f, m}) }
	if c.UCC != "" && !reUCC.MatchString(c.UCC) {
		add("ucc", "UCC is 3 to 10 letters or digits")
	}
	if len(c.Name) < 3 {
		add("name", "Enter the full name as per PAN")
	}
	if !rePAN.MatchString(c.PAN) {
		add("pan", "PAN must be 5 letters, 4 digits, 1 letter")
	} else {
		want := map[string]byte{"Individual": 'P', "NRI": 'P', "HUF": 'H', "Corporate": 'C'}[c.Type]
		if want != 0 && c.PAN[3] != want {
			add("pan", fmt.Sprintf("The 4th character of a %s PAN is %c", strings.ToLower(c.Type), want))
		}
	}
	switch c.Type {
	case "Individual", "HUF", "Corporate", "NRI":
	default:
		add("type", "Type must be Individual, HUF, Corporate or NRI")
	}
	if !reMobile.MatchString(c.Mobile) {
		add("mobile", "Enter a 10-digit Indian mobile number")
	}
	if !reEmail.MatchString(c.Email) {
		add("email", "Enter a valid email address")
	}
	if c.City == "" {
		add("city", "Enter the city")
	}
	if strings.TrimSpace(c.Bank.Bank) == "" {
		add("bank.bank", "Enter the bank name")
	}
	if !reIFSC.MatchString(c.Bank.IFSC) {
		add("bank.ifsc", "IFSC is 4 letters, 0, then 6 characters")
	}
	if !reAcct.MatchString(c.Bank.Account) {
		add("bank.account", "Account number is 9 to 18 digits")
	}
	if len(c.Segments) == 0 {
		add("segments", "Choose at least one segment")
	}
	for _, s := range c.Segments {
		if s != "CM" && !c.IncomeProof {
			add("income_proof", "Derivatives segments need income proof")
			break
		}
	}
	if c.RAS != "Monthly" && c.RAS != "Quarterly" {
		add("ras", "Running account settlement must be Monthly or Quarterly")
	}
	if c.Nominee == "Opted in" && strings.TrimSpace(c.NomineeName) == "" {
		add("nominee_name", "Enter the nominee name")
	}
	if len(fe) > 0 {
		return &ValidationError{fe}
	}
	return nil
}

// Clients lists every client.
func (o *Ops) Clients(ctx context.Context, tenant string) ([]Client, error) {
	return docs.List[Client](ctx, o.Docs, tenant, cClient, "")
}

// ClientMap indexes clients by UCC.
func (o *Ops) ClientMap(ctx context.Context, tenant string) (map[string]Client, error) {
	cs, err := o.Clients(ctx, tenant)
	if err != nil {
		return nil, err
	}
	m := make(map[string]Client, len(cs))
	for _, c := range cs {
		m[c.UCC] = c
	}
	return m, nil
}

// Client returns one client.
func (o *Ops) Client(ctx context.Context, tenant, ucc string) (Client, error) {
	c, _, err := docs.Get[Client](ctx, o.Docs, tenant, cClient, strings.ToUpper(ucc))
	return c, err
}

// CreateApplication starts onboarding. A UCC is assigned when none is given.
func (o *Ops) CreateApplication(ctx context.Context, tenant, actor string, c Client) (Client, error) {
	if err := o.require(ctx, tenant, actor, "Creating a client", RoleMaker, RoleChecker); err != nil {
		return Client{}, err
	}
	c.Normalise()
	if c.Nominee == "" {
		c.Nominee = "Opted out"
	}
	if !c.RiskDisclosure {
		return Client{}, &ValidationError{[]FieldError{{"risk_disclosure", "Risk disclosure acknowledgement is required"}}}
	}
	if err := c.Validate(); err != nil {
		return Client{}, err
	}
	all, err := o.Clients(ctx, tenant)
	if err != nil {
		return Client{}, err
	}
	for _, x := range all {
		if x.PAN == c.PAN {
			return Client{}, &ValidationError{[]FieldError{{"pan", "A client with this PAN already exists (" + x.UCC + ")"}}}
		}
	}
	if c.UCC == "" {
		n, err := o.next(ctx, tenant, "ucc", actor)
		if err != nil {
			return Client{}, err
		}
		c.UCC = fmt.Sprintf("BH%d", 2000+n)
	}
	c.Status = "KYC verification"
	c.KYCDate = o.now().Format("2006-01-02")
	c.CreatedBy = actor
	c.UPIBlock, c.Blocked, c.LastTrade = 0, false, ""
	if _, err := docs.Put(ctx, o.Docs, tenant, cClient, c.UCC, c, 0, actor); err != nil {
		return Client{}, err
	}
	o.log(ctx, tenant, actor, "client.application.created", c.UCC, map[string]any{"pan": MaskPAN(c.PAN), "segments": c.Segments})
	return c, nil
}

// AdvanceStage moves a client to the next onboarding stage. Activation
// needs an Ops checker who is not the person who created the application.
func (o *Ops) AdvanceStage(ctx context.Context, tenant, actor, ucc string) (Client, error) {
	c, err := o.Client(ctx, tenant, ucc)
	if err != nil {
		return Client{}, err
	}
	i := -1
	for k, s := range Stages {
		if s == c.Status {
			i = k
		}
	}
	if i < 0 || i == len(Stages)-1 {
		return Client{}, fmt.Errorf("%w: %s is %s and cannot be advanced", ErrState, ucc, c.Status)
	}
	next := Stages[i+1]
	if next == "Active" {
		if err := o.require(ctx, tenant, actor, "Activating a client", RoleChecker); err != nil {
			return Client{}, err
		}
		if c.CreatedBy == actor {
			return Client{}, fmt.Errorf("%w: the person who created the application cannot activate it", ErrForbidden)
		}
	} else if err := o.require(ctx, tenant, actor, "Advancing onboarding", RoleMaker, RoleChecker); err != nil {
		return Client{}, err
	}
	c, err = docs.Update(ctx, o.Docs, tenant, cClient, c.UCC, actor, func(x *Client) error {
		if x.Status != c.Status {
			return docs.ErrStale
		}
		x.Status = next
		if next == "Active" {
			x.ActivatedBy = actor
		}
		return nil
	})
	if err != nil {
		return Client{}, err
	}
	o.log(ctx, tenant, actor, "client.stage", ucc, map[string]any{"stage": next})
	return c, nil
}

// SetBlocked turns a client's kill switch on or off (Risk manager).
func (o *Ops) SetBlocked(ctx context.Context, tenant, actor, ucc string, blocked bool, reason string) (Client, error) {
	if err := o.require(ctx, tenant, actor, "The kill switch", RoleRisk); err != nil {
		return Client{}, err
	}
	if blocked && strings.TrimSpace(reason) == "" {
		return Client{}, invalid("give a reason for blocking the client")
	}
	c, err := docs.Update(ctx, o.Docs, tenant, cClient, strings.ToUpper(ucc), actor, func(x *Client) error {
		if x.Status != "Active" {
			return fmt.Errorf("%w: only active clients can be blocked", ErrState)
		}
		x.Blocked, x.BlockedReason = blocked, reason
		if !blocked {
			x.BlockedReason = ""
		}
		return nil
	})
	if err != nil {
		return Client{}, err
	}
	action := "rms.kill.on"
	if !blocked {
		action = "rms.kill.off"
	}
	o.log(ctx, tenant, actor, action, c.UCC, map[string]any{"reason": reason})
	return c, nil
}

// Public returns a copy safe for display: account numbers and PAN masked.
func (c Client) Public() Client {
	c.Bank.Account = MaskAccount(c.Bank.Account)
	c.PAN = MaskPAN(c.PAN)
	return c
}
