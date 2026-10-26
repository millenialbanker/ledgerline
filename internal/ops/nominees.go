package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/money"
)

// Nomination under SEBI's revised nomination rules for demat and trading
// accounts (January 2025): up to 10 nominees, each with a share (equal
// shares when none are given), the nominee's relationship and one identity
// detail (PAN, driving licence, the last four digits of Aadhaar, or
// passport), a date of birth and guardian for a minor; a sole holder must
// either nominate or declare that they do not wish to.

// MaxNominees is the most a client may name.
const MaxNominees = 10

// NomineeIDTypes are the identity details SEBI accepts for a nominee.
var NomineeIDTypes = map[string]string{"PAN": "PAN", "DL": "Driving licence", "AADHAAR": "Aadhaar (last 4 digits)", "PASSPORT": "Passport"}

// NominationModes record how a nomination or opt-out was declared.
var NominationModes = []string{"Online with e-Sign", "Signed form", "Video recorded"}

// Nominee is one person nominated.
type Nominee struct {
	Name     string `json:"name"`
	Relation string `json:"relation"`
	Share    string `json:"share"` // percent, two decimals; all shares add to 100
	IDType   string `json:"id_type"`
	ID       string `json:"id"` // for Aadhaar only the last 4 digits are kept
	DOB      string `json:"dob,omitempty"`
	Address  string `json:"address,omitempty"`
	Mobile   string `json:"mobile,omitempty"`
	Email    string `json:"email,omitempty"`
	// Guardian of a minor nominee.
	Guardian         string `json:"guardian,omitempty"`
	GuardianRelation string `json:"guardian_relation,omitempty"`
}

var (
	reDL       = regexp.MustCompile(`^[A-Z]{2}[0-9A-Z -]{8,18}$`)
	rePassport = regexp.MustCompile(`^[A-Z][0-9]{7}$`)
	reLast4    = regexp.MustCompile(`^[0-9]{4}$`)
)

// Minor reports whether the nominee is under 18 on the given day.
func (n Nominee) Minor(on time.Time) bool {
	d, err := time.Parse("2006-01-02", n.DOB)
	return err == nil && d.AddDate(18, 0, 0).After(on)
}

// MaskedID is the identity detail for display.
func (n Nominee) MaskedID() string {
	switch n.IDType {
	case "AADHAAR":
		return "XXXX XXXX " + n.ID
	case "PAN":
		return MaskPAN(n.ID)
	}
	if len(n.ID) > 4 {
		return strings.Repeat("X", len(n.ID)-4) + n.ID[len(n.ID)-4:]
	}
	return n.ID
}

func normaliseNominees(c *Client) {
	if len(c.Nominees) == 0 {
		return
	}
	blank := 0
	for i := range c.Nominees {
		n := &c.Nominees[i]
		n.Name = strings.Join(strings.Fields(n.Name), " ")
		n.Relation = strings.TrimSpace(n.Relation)
		n.IDType = strings.ToUpper(strings.TrimSpace(n.IDType))
		if n.IDType == "DRIVING LICENCE" || n.IDType == "DRIVING LICENSE" {
			n.IDType = "DL"
		}
		n.ID = strings.ToUpper(strings.TrimSpace(n.ID))
		if n.IDType == "AADHAAR" {
			// Only the last four digits are ever kept.
			d := strings.Map(func(r rune) rune {
				if r >= '0' && r <= '9' {
					return r
				}
				return -1
			}, n.ID)
			if len(d) >= 4 {
				d = d[len(d)-4:]
			}
			n.ID = d
		}
		n.Share = strings.TrimSuffix(strings.TrimSpace(n.Share), "%")
		n.Guardian = strings.Join(strings.Fields(n.Guardian), " ")
		if n.Share == "" {
			blank++
		}
	}
	// No shares given: equal shares, the first nominee taking the paise left over.
	if blank == len(c.Nominees) {
		k := int64(len(c.Nominees))
		each := 10000 / k
		for i := range c.Nominees {
			v := each
			if i == 0 {
				v += 10000 - each*k
			}
			c.Nominees[i].Share = fmt.Sprintf("%d.%02d", v/100, v%100)
		}
	}
	c.Nominee = "Opted in"
	names := make([]string, len(c.Nominees))
	for i, n := range c.Nominees {
		names[i] = n.Name
	}
	c.NomineeName = strings.Join(names, ", ")
}

// validateNominees checks the nominees against the rules.
func validateNominees(ns []Nominee, today time.Time) []FieldError {
	var fe []FieldError
	add := func(i int, f, m string) {
		fe = append(fe, FieldError{fmt.Sprintf("nominees.%d.%s", i, f), fmt.Sprintf("Nominee %d: %s", i+1, m)})
	}
	if len(ns) > MaxNominees {
		return []FieldError{{"nominees", fmt.Sprintf("At most %d nominees", MaxNominees)}}
	}
	total := money.Dec{}
	for i, n := range ns {
		if len(n.Name) < 3 {
			add(i, "name", "enter the full name")
		}
		if n.Relation == "" {
			add(i, "relation", "enter the relationship")
		}
		sh, err := money.ParseDec(n.Share)
		if err != nil || sh.Sign() <= 0 {
			add(i, "share", "the share must be a percentage above 0")
		} else {
			total = total.Add(sh)
		}
		switch n.IDType {
		case "PAN":
			if !rePAN.MatchString(n.ID) {
				add(i, "id", "PAN must be 5 letters, 4 digits, 1 letter")
			}
		case "DL":
			if !reDL.MatchString(n.ID) {
				add(i, "id", "enter the driving licence number")
			}
		case "AADHAAR":
			if !reLast4.MatchString(n.ID) {
				add(i, "id", "enter the last 4 digits of Aadhaar")
			}
		case "PASSPORT":
			if !rePassport.MatchString(n.ID) {
				add(i, "id", "passport number is a letter and 7 digits")
			}
		default:
			add(i, "id_type", "give one identity detail: PAN, driving licence, last 4 digits of Aadhaar, or passport")
		}
		if n.DOB != "" && !validDate(n.DOB) {
			add(i, "dob", "date of birth must be YYYY-MM-DD")
		}
		if n.Minor(today) && (len(n.Guardian) < 3 || n.GuardianRelation == "") {
			add(i, "guardian", "a minor nominee needs a guardian and the guardian's relationship")
		}
		if n.Mobile != "" && !reMobile.MatchString(n.Mobile) {
			add(i, "mobile", "enter a 10-digit mobile number")
		}
		if n.Email != "" && !reEmail.MatchString(n.Email) {
			add(i, "email", "enter a valid email address")
		}
	}
	if len(ns) > 0 && total.Cmp(money.DecInt(100)) != 0 {
		fe = append(fe, FieldError{"nominees", fmt.Sprintf("The shares add up to %s%%; they must add up to 100%%", total.Fixed(2))})
	}
	return fe
}

// NominationState says where a client stands under the nomination rules.
func (c Client) NominationState() string {
	switch {
	case len(c.Nominees) > 0:
		return "Nominated"
	case c.Nominee == "Opted in":
		return "Nominee details missing" // a name from an older record, without share or identity
	case c.Nominee == "Opted out" && c.NominationOn != "":
		return "Opted out (declared)"
	default:
		return "No declaration"
	}
}

// nominationChange parses the "Nomination" change request values: nominee
// (Opted in / Opted out), nominees (JSON list), mode.
func nominationChange(c *Client, n map[string]string) error {
	switch n["nominee"] {
	case "Opted out":
		c.Nominee, c.NomineeName, c.Nominees = "Opted out", "", nil
	case "Opted in":
		var ns []Nominee
		if raw := strings.TrimSpace(n["nominees"]); raw != "" {
			if err := json.Unmarshal([]byte(raw), &ns); err != nil {
				return invalid("nominees must be a list")
			}
		} else if name := strings.TrimSpace(n["nominee_name"]); name != "" {
			return invalid("give the nominee's share and identity detail (PAN, driving licence, last 4 digits of Aadhaar or passport)")
		}
		if len(ns) == 0 {
			return invalid("name at least one nominee, or opt out")
		}
		c.Nominee, c.Nominees = "Opted in", ns
	default:
		return invalid("nomination must be Opted in or Opted out")
	}
	if m := strings.TrimSpace(n["mode"]); m != "" {
		c.NominationMode = m
	}
	return nil
}

func nominationOld(c Client) map[string]string {
	b, _ := json.Marshal(maskNominees(c.Nominees))
	return map[string]string{"nominee": c.Nominee, "nominee_name": c.NomineeName, "nominees": string(b)}
}

// maskNominees hides identity numbers for portal and change history.
func maskNominees(ns []Nominee) []Nominee {
	out := make([]Nominee, len(ns))
	for i, n := range ns {
		n.ID = n.MaskedID()
		out[i] = n
	}
	return out
}

// NominationRow is one client on the nomination report.
type NominationRow struct {
	UCC      string    `json:"ucc"`
	Name     string    `json:"name"`
	State    string    `json:"state"`
	Count    int       `json:"nominees"`
	On       string    `json:"on,omitempty"`
	Mode     string    `json:"mode,omitempty"`
	Nominees []Nominee `json:"list,omitempty"`
}

// Nominations lists every active client's nomination state.
func (o *Ops) Nominations(ctx context.Context, tenant, actor string) ([]NominationRow, map[string]int, error) {
	if err := o.require(ctx, tenant, actor, "Viewing nominations", RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return nil, nil, err
	}
	cs, err := o.Clients(ctx, tenant)
	if err != nil {
		return nil, nil, err
	}
	var out []NominationRow
	by := map[string]int{}
	for _, c := range cs {
		if c.Status == "Closed" || c.Status == "Rejected" {
			continue
		}
		st := c.NominationState()
		by[st]++
		out = append(out, NominationRow{UCC: c.UCC, Name: c.Name, State: st, Count: len(c.Nominees), On: c.NominationOn, Mode: c.NominationMode, Nominees: maskNominees(c.Nominees)})
	}
	return out, by, nil
}

// prepareNominees loads a NOMINEES file: rows grouped by client, each
// client's list replaced.
func (o *Ops) prepareNominees(ctx context.Context, tenant, actor string, rows []row) (func() (int, int, error), []ingest.ParseError) {
	var errs []ingest.ParseError
	by := map[string][]Nominee{}
	decl := map[string][2]string{}
	lines := map[string]int{}
	for _, r := range rows {
		u := strings.ToUpper(r.get("UCC"))
		if u == "" {
			errs = append(errs, ingest.ParseError{Line: r.line, Field: "UCC", Msg: "is required"})
			continue
		}
		by[u] = append(by[u], Nominee{Name: r.get("Name"), Relation: r.get("Relation"), Share: r.get("Share"), IDType: r.get("IDType"), ID: r.get("ID"), DOB: r.get("DOB"),
			Guardian: r.get("Guardian"), GuardianRelation: r.get("GuardianRelation"), Mobile: r.get("Mobile"), Email: r.get("Email"), Address: r.get("Address")})
		if d := r.get("DeclaredOn"); d != "" || r.get("Mode") != "" {
			decl[u] = [2]string{d, r.get("Mode")}
		}
		if lines[u] == 0 {
			lines[u] = r.line
		}
	}
	cs, _ := o.ClientMap(ctx, tenant)
	now := o.now()
	for u, ns := range by {
		if _, ok := cs[u]; !ok {
			errs = append(errs, ingest.ParseError{Line: lines[u], Field: "UCC", Msg: u + " is not a client"})
			continue
		}
		c := Client{Nominees: ns}
		normaliseNominees(&c)
		for _, f := range validateNominees(c.Nominees, now) {
			errs = append(errs, ingest.ParseError{Line: lines[u], Field: f.Field, Msg: u + ": " + f.Msg})
		}
		by[u] = c.Nominees
	}
	return func() (int, int, error) {
		n := 0
		for _, u := range sortedKeys(by) {
			if _, ok := cs[u]; !ok {
				continue
			}
			ns := by[u]
			if _, err := docs.Update(ctx, o.Docs, tenant, cClient, u, actor, func(c *Client) error {
				c.Nominees = ns
				normaliseNominees(c)
				if d, ok := decl[u]; ok {
					c.NominationOn, c.NominationMode = firstOf(d[0], c.NominationOn), firstOf(d[1], c.NominationMode)
				}
				return nil
			}); err != nil {
				return n, 0, err
			}
			n += len(ns)
		}
		o.log(ctx, tenant, actor, "nominees.loaded", "", map[string]any{"clients": len(by), "nominees": n})
		return n, 0, nil
	}, errs
}
