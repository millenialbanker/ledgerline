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

// Nomination under SEBI/HO/OIAE/OIAE_IAD-3/P/CIR/2026/12676 (29 May 2026,
// in force from 1 Sep 2026; it supersedes the 2025 nomination circulars):
//
//   - up to 3 nominees;
//   - only the name and relationship are mandatory, and the date of birth
//     of a minor nominee; the identity detail (PAN, driving licence, last
//     four digits of Aadhaar, passport), contact details, share and the
//     guardian of a minor are optional;
//   - shares not given are equal, any odd lot to the first nominee;
//   - opting out is by the signed declaration (Annexure-B) or online, with
//     the declaration shown and accepted; there is no other mode;
//   - online it is validated by DSC, e-Sign or an OTP to the registered
//     mobile and email; offline by wet signature without witnesses, or a
//     thumb impression with two witnesses;
//   - for a joint account nomination is optional, and giving or changing
//     it needs the consent of every holder;
//   - statements show the nominees' names or only Yes/No, as the client
//     chooses.
//
// Nominations made before 1 Sep 2026 with more than 3 nominees are kept as
// they are; they are flagged for review and cannot be changed to another
// list of more than 3.

// MaxNominees is the most a client may name.
const MaxNominees = 3

// legacyMaxNominees was the limit before 1 Sep 2026 (up to 10). Records
// within it are kept when other client details change.
const legacyMaxNominees = 10

// NomineeIDTypes are the identity details SEBI accepts for a nominee.
var NomineeIDTypes = map[string]string{"PAN": "PAN", "DL": "Driving licence", "AADHAAR": "Aadhaar (last 4 digits)", "PASSPORT": "Passport"}

// NominationModes record how a nomination or opt-out was declared and
// validated. Online: e-Sign, digital signature or OTP; offline: the signed
// form or declaration (no witness), or a thumb impression with two
// witnesses.
var NominationModes = []string{"Online with e-Sign", "Online with digital signature", "Online with OTP", "Signed form", "Thumb impression with two witnesses"}

// modeThumb needs the two witnesses' names and addresses.
const modeThumb = "Thumb impression with two witnesses"

// StatementNomination returns what a statement shows about nomination:
// the nominees' names when the client chose that, otherwise Yes or No.
func (c Client) StatementNomination() string {
	if len(c.Nominees) == 0 && c.Nominee != "Opted in" {
		return "No"
	}
	if c.NomineesOnStatement && c.NomineeName != "" {
		return c.NomineeName
	}
	return "Yes"
}

// Nominee is one person nominated.
type Nominee struct {
	Name     string `json:"name"`
	Relation string `json:"relation"`
	Share    string `json:"share"` // percent, two decimals; all shares add to 100
	// IDType and ID are optional (one identity detail, if given).
	IDType string `json:"id_type"`
	ID     string `json:"id"` // for Aadhaar only the last 4 digits are kept
	// IsMinor marks a nominee under 18; the date of birth is then required.
	IsMinor bool   `json:"minor,omitempty"`
	DOB     string `json:"dob,omitempty"`
	Address string `json:"address,omitempty"`
	Mobile  string `json:"mobile,omitempty"`
	Email   string `json:"email,omitempty"`
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
		if n.ID == "" {
			n.IDType = "" // the identity detail is optional
		}
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

// validateNominees checks a new or changed list of nominees against the
// rules, at most MaxNominees.
func validateNominees(ns []Nominee, today time.Time) []FieldError {
	return checkNominees(ns, today, MaxNominees)
}

// checkNominees checks nominees with a limit on their number: MaxNominees
// for a new nomination, legacyMaxNominees for one recorded before the
// limit fell to 3 (kept as it is and flagged for review).
func checkNominees(ns []Nominee, today time.Time, limit int) []FieldError {
	var fe []FieldError
	add := func(i int, f, m string) {
		fe = append(fe, FieldError{fmt.Sprintf("nominees.%d.%s", i, f), fmt.Sprintf("Nominee %d: %s", i+1, m)})
	}
	if len(ns) > limit {
		return []FieldError{{"nominees", fmt.Sprintf("At most %d nominees", limit)}}
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
		case "":
			if n.ID != "" {
				add(i, "id_type", "say which identity detail this is: PAN, driving licence, last 4 digits of Aadhaar, or passport")
			}
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
			add(i, "id_type", "the identity detail is PAN, driving licence, last 4 digits of Aadhaar, or passport")
		}
		if n.DOB != "" && !validDate(n.DOB) {
			add(i, "dob", "date of birth must be YYYY-MM-DD")
		}
		// A minor's date of birth is mandatory; the guardian is optional.
		if n.DOB == "" && (n.IsMinor || n.Guardian != "") {
			add(i, "dob", "a minor nominee's date of birth is required")
		}
		if n.Guardian != "" && len(n.Guardian) < 3 {
			add(i, "guardian", "enter the guardian's full name")
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

// NominationOverLimit is the state of a nomination recorded with more than
// MaxNominees nominees before the limit fell: kept, but to be reviewed.
const NominationOverLimit = "More than 3 nominees (review)"

// NominationState says where a client stands under the nomination rules.
func (c Client) NominationState() string {
	switch {
	case len(c.Nominees) > MaxNominees:
		return NominationOverLimit
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
			return invalid("give the nominees as a list, each with a name and relationship")
		}
		if len(ns) == 0 {
			return invalid("name at least one nominee, or opt out")
		}
		if len(ns) > MaxNominees {
			return invalid("at most %d nominees", MaxNominees)
		}
		c.Nominee, c.Nominees = "Opted in", ns
	default:
		return invalid("nomination must be Opted in or Opted out")
	}
	if m := strings.TrimSpace(n["mode"]); m != "" {
		if !contains(NominationModes, m) {
			return invalid("a nomination or opt-out is declared %s (a video is not one of them)", strings.Join(NominationModes, ", "))
		}
		if m == modeThumb && (strings.TrimSpace(n["witness_1"]) == "" || strings.TrimSpace(n["witness_2"]) == "") {
			return invalid("a thumb impression needs two witnesses: give each one's name and address")
		}
		c.NominationMode = m
	}
	switch n["on_statement"] {
	case "names":
		c.NomineesOnStatement = true
	case "yes_no":
		c.NomineesOnStatement = false
	}
	return nil
}

// checkNominationConsent: giving or changing a nomination on a joint
// account needs the consent of every holder, whatever the mode of
// operation, so the request must confirm that all holders consented.
func (o *Ops) checkNominationConsent(ctx context.Context, tenant, ucc string, n map[string]string) error {
	d, _, err := docs.Get[CDSLBODetail](ctx, o.Docs, tenant, cCDSLBODetail, ucc)
	if err != nil || len(d.Holders) == 0 {
		return nil // a sole holder
	}
	if n["joint_consent"] != "all" {
		return invalid("%s is a joint account: all %d holders must consent to the nomination; keep their signed or OTP consents and confirm that all consented", ucc, len(d.Holders)+1)
	}
	return nil
}

func nominationOld(c Client) map[string]string {
	b, _ := json.Marshal(maskNominees(c.Nominees))
	show := "yes_no"
	if c.NomineesOnStatement {
		show = "names"
	}
	return map[string]string{"nominee": c.Nominee, "nominee_name": c.NomineeName, "nominees": string(b), "on_statement": show}
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
	// OverLimit: more than 3 nominees, recorded before the limit fell.
	// Kept as it is; the client is asked to bring it within 3.
	OverLimit bool `json:"over_limit,omitempty"`
}

// flagNominationsOverLimit raises a work item for every client whose
// recorded nomination has more than MaxNominees nominees, and closes the
// items of those now within it. Nothing is deleted.
func (o *Ops) flagNominationsOverLimit(ctx context.Context, tenant, date string) int {
	cs, err := o.Clients(ctx, tenant)
	if err != nil {
		return 0
	}
	n := 0
	for _, c := range cs {
		if c.Status == "Closed" || c.Status == "Rejected" {
			continue
		}
		if len(c.Nominees) > MaxNominees {
			o.raiseWork(ctx, tenant, wNominations, "over:"+c.UCC, fmt.Sprintf("%s has %d nominees: ask the client to bring it within %d", c.UCC, len(c.Nominees), MaxNominees),
				fmt.Sprintf("%s's nomination (%s) was recorded with %d nominees. From 1 Sep 2026 a demat account may have at most %d (SEBI circular of 29 May 2026). It is kept as it is: ask the client for a new nomination of up to %d, or note why it stays.", c.Name, firstOf(c.NominationOn, "date not recorded"), len(c.Nominees), MaxNominees, MaxNominees),
				c.UCC, RoleCompliance, "clients", date)
			n++
		} else {
			o.closeWork(ctx, tenant, SystemActor, wNominations, "over:"+c.UCC, "nomination now within the limit")
		}
	}
	return n
}

// wNominations is the work queue process of nomination reviews.
const wNominations = "nominations"

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
		out = append(out, NominationRow{UCC: c.UCC, Name: c.Name, State: st, Count: len(c.Nominees), On: c.NominationOn, Mode: c.NominationMode, Nominees: maskNominees(c.Nominees), OverLimit: len(c.Nominees) > MaxNominees})
	}
	if by[NominationOverLimit] > 0 {
		o.flagNominationsOverLimit(ctx, tenant, o.now().Format("2006-01-02"))
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
	rowLines := map[string][]int{}
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
		rowLines[u] = append(rowLines[u], r.line)
		if lines[u] == 0 {
			lines[u] = r.line
		}
	}
	cs, _ := o.ClientMap(ctx, tenant)
	now := o.now()
	warned := map[string]bool{} // inactive clients whose nominees load incomplete
	for _, u := range sortedKeys(by) {
		ns := by[u]
		cl, ok := cs[u]
		if !ok {
			errs = append(errs, ingest.ParseError{Line: lines[u], Field: "UCC", Msg: u + " is not a client"})
			continue
		}
		if cl.Inactive {
			// An inactive client's nominees load as the previous system had
			// them (incomplete.go); a row without even a name or relationship
			// is left out.
			var keep []Nominee
			for i, n := range ns {
				if strings.TrimSpace(n.Name) == "" && strings.TrimSpace(n.Relation) == "" {
					errs = append(errs, ingest.ParseError{Line: rowLines[u][i], Field: "Name", Msg: u + ": a nominee row has neither a name nor a relationship; it is left out", Warning: true})
					continue
				}
				keep = append(keep, n)
			}
			if ns = keep; len(ns) == 0 {
				delete(by, u)
				continue
			}
		}
		c := Client{Nominees: ns}
		normaliseNominees(&c)
		// A migration file carries nominations already made, some under
		// the old limit of 10: they load as they are and are flagged.
		for _, f := range checkNominees(c.Nominees, now, legacyMaxNominees) {
			if cl.Inactive && len(c.Nominees) <= legacyMaxNominees {
				errs = append(errs, ingest.ParseError{Line: lines[u], Field: f.Field, Msg: u + ": " + f.Msg + incompleteNote, Warning: true})
				warned[u] = true
				continue
			}
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
				if warned[u] {
					addIncomplete(c, "Nominee details")
				}
				refreshIncomplete(c, nil)
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
