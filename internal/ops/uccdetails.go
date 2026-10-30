package ops

import (
	"context"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
)

// ---- exchange UCC details ----
//
// The exchanges' UCC records ask for more than Ledgerline's client record
// holds: the address in lines with its PIN, the father's or spouse's name,
// marital status, occupation, nationality, the contact persons of a firm or
// company, and so on. An online application gives most of them; for other
// clients (and to correct any of them) staff fill them in here. They are
// kept per client, for every exchange's UCC upload.

const cUCCDetails = "uccdetails" // key: UCC

// UCCContact is a partner, karta, director or trustee of a non-individual
// client.
type UCCContact struct {
	Name        string `json:"name"`
	Designation string `json:"designation,omitempty"`
	PAN         string `json:"pan,omitempty"`
	Address     string `json:"address,omitempty"`
	Phone       string `json:"phone,omitempty"`
	Email       string `json:"email,omitempty"`
	DIN         string `json:"din,omitempty"`
}

// UCCDetails are a client's details for the exchanges' UCC that the client
// record does not hold. Empty fields fall back to the client's online
// application and the client record.
type UCCDetails struct {
	UCC string `json:"ucc"`
	// Category is the exchange client category code when the client type
	// does not say it (2 partnership or LLP, 5 trust or society…).
	Category string `json:"category,omitempty"`
	Address1 string `json:"address1,omitempty"`
	Address2 string `json:"address2,omitempty"`
	Address3 string `json:"address3,omitempty"`
	City     string `json:"city,omitempty"`
	State    string `json:"state,omitempty"`   // state name or the exchange's state code
	Country  string `json:"country,omitempty"` // exchange country code; India (85) when empty
	PIN      string `json:"pin,omitempty"`
	// The permanent address, when it is not the correspondence address.
	PermAddress1 string `json:"perm_address1,omitempty"`
	PermAddress2 string `json:"perm_address2,omitempty"`
	PermAddress3 string `json:"perm_address3,omitempty"`
	PermCity     string `json:"perm_city,omitempty"`
	PermState    string `json:"perm_state,omitempty"`
	PermCountry  string `json:"perm_country,omitempty"`
	PermPIN      string `json:"perm_pin,omitempty"`

	Gender           string `json:"gender,omitempty"`   // Male, Female, Transgender (or M, F, U)
	Guardian         string `json:"guardian,omitempty"` // father's or spouse's name
	Marital          string `json:"marital,omitempty"`  // Single, Married, Widowed, Divorced
	Nationality      string `json:"nationality,omitempty"`
	Occupation       string `json:"occupation,omitempty"` // exchange code 1-9 or 99, or a word
	OccupationDetail string `json:"occupation_detail,omitempty"`
	IncomeAsOn       string `json:"income_as_on,omitempty"` // when the income range was declared
	PEP              string `json:"pep,omitempty"`          // 0 not, 1 PEP, 2 related to a PEP
	IPV              string `json:"ipv,omitempty"`          // Y or N
	POAFunds         string `json:"poa_funds,omitempty"`    // Y or N
	POASecurities    string `json:"poa_securities,omitempty"`
	UPI              string `json:"upi,omitempty"` // Y registered for UPI, N not
	AgreementDate    string `json:"agreement_date,omitempty"`
	CIN              string `json:"cin,omitempty"`

	Contacts []UCCContact `json:"contacts,omitempty"`

	UpdatedBy string `json:"updated_by,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

func (d *UCCDetails) normalise() {
	t := func(s *string) { *s = strings.Join(strings.Fields(*s), " ") }
	for _, p := range []*string{&d.Category, &d.Address1, &d.Address2, &d.Address3, &d.City, &d.State, &d.Country, &d.PIN,
		&d.PermAddress1, &d.PermAddress2, &d.PermAddress3, &d.PermCity, &d.PermState, &d.PermCountry, &d.PermPIN,
		&d.Gender, &d.Guardian, &d.Marital, &d.Nationality, &d.Occupation, &d.OccupationDetail, &d.IncomeAsOn, &d.PEP, &d.IPV,
		&d.POAFunds, &d.POASecurities, &d.UPI, &d.AgreementDate, &d.CIN} {
		t(p)
	}
	for _, p := range []*string{&d.IPV, &d.POAFunds, &d.POASecurities, &d.UPI, &d.CIN} {
		*p = strings.ToUpper(*p)
	}
	var cs []UCCContact
	for _, c := range d.Contacts {
		for _, p := range []*string{&c.Name, &c.Designation, &c.PAN, &c.Address, &c.Phone, &c.Email, &c.DIN} {
			t(p)
		}
		c.PAN, c.DIN = strings.ToUpper(c.PAN), strings.ToUpper(c.DIN)
		if c.Name != "" || c.PAN != "" || c.Designation != "" {
			cs = append(cs, c)
		}
	}
	d.Contacts = cs
}

// UCCDetailsFor returns the details kept for a client (empty when none).
func (o *Ops) UCCDetailsFor(ctx context.Context, tenant, ucc string) UCCDetails {
	ucc = strings.ToUpper(strings.TrimSpace(ucc))
	d, _, _ := docs.GetOr(ctx, o.Docs, tenant, cUCCDetails, ucc, UCCDetails{UCC: ucc})
	return d
}

// SaveUCCDetails stores a client's exchange UCC details. Dates are
// YYYY-MM-DD; codes are checked when the record is mapped for an exchange,
// where each problem is reported in words.
func (o *Ops) SaveUCCDetails(ctx context.Context, tenant, actor, ucc string, d UCCDetails) (UCCDetails, error) {
	if err := o.require(ctx, tenant, actor, "Editing exchange UCC details", RoleMaker, RoleChecker); err != nil {
		return d, err
	}
	ucc = strings.ToUpper(strings.TrimSpace(ucc))
	if _, err := o.Client(ctx, tenant, ucc); err != nil {
		return d, err
	}
	d.normalise()
	d.UCC = ucc
	for _, x := range []struct{ v, what string }{{d.IncomeAsOn, "income declared on"}, {d.AgreementDate, "agreement date"}} {
		if x.v != "" && !validDate(x.v) {
			return d, invalid("the %s is a date (YYYY-MM-DD)", x.what)
		}
	}
	if len(d.Contacts) > 5 {
		return d, invalid("give at most 5 contact persons")
	}
	d.UpdatedBy, d.UpdatedAt = actor, o.now().Format(time.RFC3339)
	if err := docs.Upsert(ctx, o.Docs, tenant, cUCCDetails, ucc, d, actor); err != nil {
		return d, err
	}
	o.log(ctx, tenant, actor, "ucc.details.saved", ucc, nil)
	return d, nil
}

// uccSources holds, for every client, what the exchange UCC mapping reads
// besides the client record: the details kept here and the online
// application the account came from.
type uccSources struct {
	details map[string]UCCDetails
	apps    map[string]Application
}

func (o *Ops) uccSources(ctx context.Context, tenant string) uccSources {
	s := uccSources{details: map[string]UCCDetails{}, apps: map[string]Application{}}
	ds, _ := docs.List[UCCDetails](ctx, o.Docs, tenant, cUCCDetails, "")
	for _, d := range ds {
		s.details[d.UCC] = d
	}
	as, _ := docs.List[Application](ctx, o.Docs, tenant, cApplication, "")
	sort.SliceStable(as, func(i, j int) bool { return as[i].Updated < as[j].Updated })
	for _, a := range as {
		if a.UCC != "" && a.Status != "Rejected" {
			s.apps[a.UCC] = a // the latest wins
		}
	}
	return s
}
