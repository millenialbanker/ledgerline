package ops

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
	"ledgerline/internal/xfile"
)

// ---- dealers and trading terminals ----
//
// The register of who trades on each terminal: the firm's dealers (its own
// employees, authorised persons and their employees), the terminals or user
// IDs each one uses on each exchange, and each dealer's NISM Series VIII
// certificate. The exchanges expect a member to know the user of every
// terminal and to keep each dealer's certificate valid; trades carry the
// terminal ID, so business is credited to dealers from it.
//
// A terminal's ID may be a pattern (* any run of characters, ? one), as in
// the brokerage channels. A terminal is in use from ActiveFrom up to the day
// before ClosedOn. The daily check (dealersTick) raises work for the
// compliance officer 60, 30 and 7 days before a certificate runs out and on
// the day it does, and for a dealer who left with terminals still open.
//
// BSE takes three upload files (registration, NISM and PAN; layouts in
// config/formats/bse-dealer-*.json); they are written from this register
// for the dealers chosen. NSE publishes no such file layout.

const (
	cDealer     = "dealer"
	cDealerTick = "dealertick"
	wDealers    = "dealers" // work queue process

	DealerEmployee   = "Employee"
	DealerAP         = "AP"
	DealerAPEmployee = "AP employee"

	DealerActive = "Active"
	DealerLeft   = "Left"
)

// DealerRelationships are a dealer's relationships with the firm.
var DealerRelationships = []string{DealerEmployee, DealerAP, DealerAPEmployee}

// DealerExchanges are the exchanges terminals are registered with.
var DealerExchanges = []string{"NSE", "BSE", "MCX"}

// DealerSegments are the segments a terminal may trade.
var DealerSegments = []string{"CM", "FO", "CD", "COM", "EGR"}

// BSEAlgoCodes are the order type codes of the "14 digit" field on BSE's
// dealer registration file.
var BSEAlgoCodes = map[string]string{"0": "Non-algo", "1": "Algo", "2": "Algo using SOR", "3": "Non-algo SOR", "4": "Inter-exchange algo", "5": "RMS square-off", "6": "AMO", "7": "Basket", "8": "Batch"}

var rePincode = regexp.MustCompile(`^[1-9][0-9]{5}$`)

// DealerAddress is an office or residence address (as BSE's files need it).
type DealerAddress struct {
	Address string `json:"address"`
	Area    string `json:"area"`
	City    string `json:"city"`
	State   string `json:"state,omitempty"` // office only (BSE's dealer PAN file)
	Pincode string `json:"pincode"`
}

// NISMCert is a NISM certificate.
type NISMCert struct {
	CertNo    string `json:"cert_no"`
	Series    string `json:"series"` // VIII
	ExamDate  string `json:"exam_date"`
	ValidTill string `json:"valid_till"` // last day it is valid
}

// DealerTerminal is a terminal or user ID a dealer trades on.
type DealerTerminal struct {
	Exchange   string   `json:"exchange"` // NSE, BSE, MCX
	Segments   []string `json:"segments"` // CM, FO, CD, COM, EGR
	TerminalID string   `json:"terminal_id"`
	LocationID string   `json:"location_id,omitempty"`
	CTCLID     string   `json:"ctcl_id,omitempty"`
	Algo       bool     `json:"algo"`
	// AlgoCode is BSE's order type code (0–8, see BSEAlgoCodes); blank
	// means 1 for an algo terminal and 0 otherwise.
	AlgoCode       string `json:"algo_code,omitempty"`
	ConnectionMode string `json:"connection_mode,omitempty"`
	BranchID       string `json:"branch_id,omitempty"`  // BSE TWS/branch ID
	Vendor         string `json:"vendor,omitempty"`     // CTCL vendor code
	TradingID      string `json:"trading_id,omitempty"` // trading ID allotted by the exchange
	ActiveFrom     string `json:"active_from"`
	ClosedOn       string `json:"closed_on,omitempty"`
	// Purge asks BSE to delete the closed terminal's record for good ('P').
	Purge bool `json:"purge,omitempty"`
	// ExchangeRecordID is the exchange's ID of the record (BSE's ID
	// field), needed to modify it.
	ExchangeRecordID string `json:"exchange_record_id,omitempty"`
}

// openOn: not closed by day (it may start later).
func (t DealerTerminal) openOn(day string) bool { return t.ClosedOn == "" || day < t.ClosedOn }

// activeOn: in use on day.
func (t DealerTerminal) activeOn(day string) bool {
	return t.ActiveFrom <= day && t.openOn(day)
}

func (t DealerTerminal) pattern() bool { return strings.ContainsAny(t.TerminalID, "*?[") }

func (t DealerTerminal) algoCode() string {
	if t.AlgoCode != "" {
		return t.AlgoCode
	}
	if t.Algo {
		return "1"
	}
	return "0"
}

// Dealer is a person who places orders on the firm's terminals.
type Dealer struct {
	ID           string           `json:"id"` // DL-0001
	FirstName    string           `json:"first_name"`
	MiddleName   string           `json:"middle_name,omitempty"`
	LastName     string           `json:"last_name,omitempty"`
	PAN          string           `json:"pan"`
	DOB          string           `json:"dob,omitempty"`
	Mobile       string           `json:"mobile,omitempty"`
	Email        string           `json:"email,omitempty"`
	Branch       string           `json:"branch,omitempty"`
	Partner      string           `json:"partner,omitempty"` // AP code
	Relationship string           `json:"relationship"`      // Employee, AP, AP employee
	Office       DealerAddress    `json:"office"`
	Residence    DealerAddress    `json:"residence"`
	NISM         NISMCert         `json:"nism"`
	NISMHistory  []NISMCert       `json:"nism_history,omitempty"` // earlier certificates
	NISMFiled    string           `json:"nism_filed,omitempty"`   // certificate|valid till last written to BSE's NISM file
	Terminals    []DealerTerminal `json:"terminals"`
	Status       string           `json:"status"` // Active, Left
	LeftOn       string           `json:"left_on,omitempty"`
	Notes        string           `json:"notes,omitempty"`
	CreatedBy    string           `json:"created_by,omitempty"`
	CreatedAt    string           `json:"created_at,omitempty"`
	UpdatedBy    string           `json:"updated_by,omitempty"`
	UpdatedAt    string           `json:"updated_at,omitempty"`
}

// Name is the dealer's full name.
func (d Dealer) Name() string {
	return strings.Join(strings.Fields(d.FirstName+" "+d.MiddleName+" "+d.LastName), " ")
}

func (d Dealer) openTerminals(day string) []DealerTerminal {
	var out []DealerTerminal
	for _, t := range d.Terminals {
		if t.openOn(day) {
			out = append(out, t)
		}
	}
	return out
}

// nismValidOn: a certificate (current or earlier) covered day.
func (d Dealer) nismValidOn(day string) bool {
	for _, c := range append([]NISMCert{d.NISM}, d.NISMHistory...) {
		if c.ValidTill != "" && day <= c.ValidTill && (c.ExamDate == "" || c.ExamDate <= day) {
			return true
		}
	}
	return false
}

func (o *Ops) today() string { return o.now().Format("2006-01-02") }

func dealerTime(o *Ops) string { return o.now().Format(time.RFC3339) }

// ---- reading ----

// AllDealers lists the register (PANs in full; callers mask them).
func (o *Ops) allDealers(ctx context.Context, tenant string) ([]Dealer, error) {
	ds, err := docs.List[Dealer](ctx, o.Docs, tenant, cDealer, "")
	sort.Slice(ds, func(i, j int) bool { return ds[i].ID < ds[j].ID })
	return ds, err
}

func (o *Ops) canSeeDealers(ctx context.Context, tenant, actor string) error {
	return o.require(ctx, tenant, actor, "Viewing dealers and terminals", RoleMaker, RoleChecker, RoleRisk, RoleCompliance)
}

// canEditDealers: maker and compliance see PANs in full (they key them in).
func (o *Ops) canEditDealers(ctx context.Context, tenant, actor string) bool {
	r, err := o.RoleOf(ctx, tenant, actor)
	return err == nil && (r == RoleMaker || r == RoleCompliance || r == RoleAdmin)
}

// GetDealer returns one dealer; the PAN is masked for those who cannot edit.
func (o *Ops) GetDealer(ctx context.Context, tenant, actor, id string) (Dealer, error) {
	if err := o.canSeeDealers(ctx, tenant, actor); err != nil {
		return Dealer{}, err
	}
	d, _, err := docs.Get[Dealer](ctx, o.Docs, tenant, cDealer, id)
	if err != nil {
		return d, err
	}
	if !o.canEditDealers(ctx, tenant, actor) {
		d.PAN = MaskPAN(d.PAN)
	}
	return d, nil
}

// ---- checking ----

func cleanDealer(d *Dealer) {
	tr := strings.TrimSpace
	sp := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	d.ID = strings.ToUpper(tr(d.ID))
	d.FirstName, d.MiddleName, d.LastName = sp(d.FirstName), sp(d.MiddleName), sp(d.LastName)
	d.PAN = strings.ToUpper(tr(d.PAN))
	d.DOB, d.Mobile, d.Email = tr(d.DOB), tr(d.Mobile), tr(d.Email)
	d.Branch, d.Partner = tr(d.Branch), strings.ToUpper(tr(d.Partner))
	for _, r := range DealerRelationships {
		if strings.EqualFold(tr(d.Relationship), r) {
			d.Relationship = r
		}
	}
	for _, a := range []*DealerAddress{&d.Office, &d.Residence} {
		a.Address, a.Area, a.City, a.State, a.Pincode = sp(a.Address), sp(a.Area), sp(a.City), sp(a.State), tr(a.Pincode)
	}
	d.Residence.State = ""
	d.NISM.CertNo, d.NISM.ExamDate, d.NISM.ValidTill = strings.ToUpper(tr(d.NISM.CertNo)), tr(d.NISM.ExamDate), tr(d.NISM.ValidTill)
	if d.NISM.Series = strings.ToUpper(tr(d.NISM.Series)); d.NISM.Series == "" {
		d.NISM.Series = "VIII"
	}
	d.LeftOn, d.Notes = tr(d.LeftOn), tr(d.Notes)
	if d.Terminals == nil {
		d.Terminals = []DealerTerminal{}
	}
	for i := range d.Terminals {
		cleanTerminal(&d.Terminals[i])
	}
}

func cleanTerminal(t *DealerTerminal) {
	tr := strings.TrimSpace
	t.Exchange = strings.ToUpper(tr(t.Exchange))
	seen := map[string]bool{}
	segs := []string{}
	for _, s := range t.Segments {
		for _, x := range strings.FieldsFunc(strings.ToUpper(s), func(r rune) bool { return r == ';' || r == ',' || r == ' ' || r == '/' }) {
			if !seen[x] {
				seen[x] = true
				segs = append(segs, x)
			}
		}
	}
	t.Segments = segs
	t.TerminalID, t.LocationID, t.CTCLID = tr(t.TerminalID), tr(t.LocationID), tr(t.CTCLID)
	t.AlgoCode, t.ConnectionMode, t.BranchID, t.Vendor, t.TradingID = tr(t.AlgoCode), tr(t.ConnectionMode), tr(t.BranchID), tr(t.Vendor), tr(t.TradingID)
	t.ActiveFrom, t.ClosedOn, t.ExchangeRecordID = tr(t.ActiveFrom), tr(t.ClosedOn), tr(t.ExchangeRecordID)
}

// shiftDate moves a YYYY-MM-DD date.
func shiftDate(d string, y, m, n int) string {
	t, err := time.Parse("2006-01-02", d)
	if err != nil {
		return ""
	}
	return t.AddDate(y, m, n).Format("2006-01-02")
}

// terminalIssues checks one terminal on its own.
func terminalIssues(t DealerTerminal, today string) []FieldError {
	var out []FieldError
	bad := func(f, m string, a ...any) { out = append(out, FieldError{Field: f, Msg: fmt.Sprintf(m, a...)}) }
	name := firstOf(t.TerminalID, "a terminal")
	if !contains(DealerExchanges, t.Exchange) {
		bad("exchange", "terminal %s: exchange must be NSE, BSE or MCX", name)
	}
	if t.TerminalID == "" {
		bad("terminal_id", "give the terminal or user ID")
	} else if _, err := path.Match(t.TerminalID, ""); err != nil {
		bad("terminal_id", "terminal pattern %q is not valid", t.TerminalID)
	}
	if len(t.Segments) == 0 {
		bad("segments", "terminal %s: choose at least one segment", name)
	}
	for _, s := range t.Segments {
		switch {
		case !contains(DealerSegments, s):
			bad("segments", "terminal %s: segment %q must be one of %s", name, s, strings.Join(DealerSegments, ", "))
		case t.Exchange == "MCX" && s != "COM":
			bad("segments", "terminal %s: MCX terminals trade commodities (COM) only", name)
		case s == "EGR" && t.Exchange != "BSE":
			bad("segments", "terminal %s: electronic gold receipts (EGR) trade on BSE only", name)
		}
	}
	if _, ok := BSEAlgoCodes[t.AlgoCode]; t.AlgoCode != "" && !ok {
		bad("algo_code", "terminal %s: the order type code must be 0 to 8", name)
	}
	if t.Algo && t.AlgoCode == "0" || !t.Algo && (t.AlgoCode == "1" || t.AlgoCode == "2" || t.AlgoCode == "4") {
		bad("algo_code", "terminal %s: the order type code %s (%s) does not agree with the algo flag", name, t.AlgoCode, BSEAlgoCodes[t.AlgoCode])
	}
	switch {
	case !validDate(t.ActiveFrom):
		bad("active_from", "terminal %s: active from must be a date (YYYY-MM-DD)", name)
	case t.ActiveFrom < "2000-01-01" || t.ActiveFrom > shiftDate(today, 0, 0, 31):
		bad("active_from", "terminal %s: active from %s is not a sensible date", name, t.ActiveFrom)
	}
	if t.ClosedOn != "" {
		switch {
		case !validDate(t.ClosedOn):
			bad("closed_on", "terminal %s: closed on must be a date (YYYY-MM-DD)", name)
		case t.ClosedOn < t.ActiveFrom:
			bad("closed_on", "terminal %s: closed on %s is before it was active from %s", name, t.ClosedOn, t.ActiveFrom)
		case t.ClosedOn > shiftDate(today, 0, 0, 31):
			bad("closed_on", "terminal %s: closed on %s is too far ahead", name, t.ClosedOn)
		}
	}
	if t.Purge && t.ClosedOn == "" {
		bad("purge", "terminal %s: only a closed terminal can be deleted at the exchange", name)
	}
	return out
}

// dealerIssues checks a dealer's own fields (not other dealers').
func dealerIssues(d Dealer, today string, branches map[string]bool, partners map[string]Partner) []FieldError {
	var out []FieldError
	bad := func(f, m string, a ...any) { out = append(out, FieldError{Field: f, Msg: fmt.Sprintf(m, a...)}) }
	if d.FirstName == "" {
		bad("first_name", "give the dealer's first name")
	}
	if !rePAN.MatchString(d.PAN) {
		bad("pan", "PAN must be 5 letters, 4 digits and a letter (AAAAA9999A)")
	}
	if d.DOB != "" {
		switch {
		case !validDate(d.DOB):
			bad("dob", "date of birth must be YYYY-MM-DD")
		case d.DOB < "1920-01-01" || d.DOB > shiftDate(today, -18, 0, 0):
			bad("dob", "date of birth %s is not sensible (a dealer is at least 18)", d.DOB)
		}
	}
	if d.Mobile != "" && !reMobile.MatchString(d.Mobile) {
		bad("mobile", "mobile must be 10 digits starting 6 to 9")
	}
	if d.Email != "" && !reEmail.MatchString(d.Email) {
		bad("email", "email is not an email address")
	}
	switch d.Relationship {
	case DealerEmployee:
		if d.Branch == "" {
			bad("branch", "an employee dealer needs the branch they work at")
		}
		if d.Partner != "" {
			bad("partner", "an employee dealer is not under an AP; clear the AP or change the relationship")
		}
	case DealerAP, DealerAPEmployee:
		p, ok := partners[d.Partner]
		switch {
		case d.Partner == "":
			bad("partner", "give the code of the authorised person (AP)")
		case !ok:
			bad("partner", "AP %s is not on Branches & partners", d.Partner)
		case p.Kind != RoleAP:
			bad("partner", "%s is a %s, not an authorised person", d.Partner, strings.ToLower(p.Kind))
		}
	default:
		bad("relationship", "relationship must be one of %s", strings.Join(DealerRelationships, ", "))
	}
	if d.Branch != "" && !branches[d.Branch] {
		bad("branch", "branch %s is not on Branches & partners", d.Branch)
	}
	for _, a := range []struct {
		f string
		v string
	}{{"office.pincode", d.Office.Pincode}, {"residence.pincode", d.Residence.Pincode}} {
		if a.v != "" && !rePincode.MatchString(a.v) {
			bad(a.f, "pincode %q must be 6 digits", a.v)
		}
	}
	n := d.NISM
	if n.Series != "VIII" {
		bad("nism.series", "dealers need NISM Series VIII (equity derivatives)")
	}
	if n.ExamDate != "" {
		switch {
		case !validDate(n.ExamDate):
			bad("nism.exam_date", "NISM exam date must be YYYY-MM-DD")
		case n.ExamDate < "2008-01-01" || n.ExamDate > today:
			bad("nism.exam_date", "NISM exam date %s is not sensible", n.ExamDate)
		}
	}
	if n.ValidTill != "" {
		switch {
		case !validDate(n.ValidTill):
			bad("nism.valid_till", "NISM valid till must be YYYY-MM-DD")
		case n.ExamDate != "" && n.ValidTill <= n.ExamDate:
			bad("nism.valid_till", "NISM valid till %s must be after the exam date %s", n.ValidTill, n.ExamDate)
		case n.ExamDate != "" && n.ValidTill > shiftDate(n.ExamDate, 3, 1, 0):
			bad("nism.valid_till", "a NISM certificate is valid for 3 years; %s is too long after the exam date %s", n.ValidTill, n.ExamDate)
		}
		if n.CertNo == "" {
			bad("nism.cert_no", "give the NISM certificate (enrolment) number")
		}
	}
	if (n.CertNo != "" || n.ExamDate != "") && n.ValidTill == "" {
		bad("nism.valid_till", "give the date the NISM certificate is valid till")
	}
	if len(d.openTerminals(today)) > 0 && n.ValidTill == "" {
		bad("nism.valid_till", "a dealer with an active terminal needs a NISM Series VIII certificate with its valid-till date")
	}
	switch d.Status {
	case DealerActive:
		if d.LeftOn != "" {
			bad("left_on", "an active dealer has no leaving date")
		}
	case DealerLeft:
		switch {
		case !validDate(d.LeftOn):
			bad("left_on", "give the date the dealer left (YYYY-MM-DD)")
		case d.LeftOn > today || d.LeftOn < "2000-01-01":
			bad("left_on", "left on %s is not sensible (not in the future)", d.LeftOn)
		}
	default:
		bad("status", "status must be Active or Left")
	}
	return out
}

// clash is a terminal ID taken by another open terminal on the exchange.
func termKey(t DealerTerminal) string { return t.Exchange + "|" + strings.ToUpper(t.TerminalID) }

// uniqueIssues: d's open terminals against each other and against the
// other dealers' open terminals (index of d's terminal, message).
func uniqueIssues(d Dealer, others []Dealer, today string) map[int]string {
	taken := map[string]string{}
	for _, x := range others {
		if x.ID == d.ID && d.ID != "" {
			continue
		}
		for _, t := range x.openTerminals(today) {
			taken[termKey(t)] = x.ID + " " + x.Name()
		}
	}
	out := map[int]string{}
	mine := map[string]bool{}
	for i, t := range d.Terminals {
		if !t.openOn(today) {
			continue
		}
		k := termKey(t)
		if who, ok := taken[k]; ok {
			out[i] = fmt.Sprintf("%s terminal %s is already active for %s", t.Exchange, t.TerminalID, who)
		} else if mine[k] {
			out[i] = fmt.Sprintf("%s terminal %s is listed twice", t.Exchange, t.TerminalID)
		}
		mine[k] = true
	}
	return out
}

func (o *Ops) dealerRefs(ctx context.Context, tenant string) (map[string]bool, map[string]Partner) {
	bs, _ := o.Branches(ctx, tenant)
	ps, _ := docs.List[Partner](ctx, o.Docs, tenant, cPartner, "")
	branches, partners := map[string]bool{}, map[string]Partner{}
	for _, b := range bs {
		branches[b.Code] = true
	}
	for _, p := range ps {
		partners[p.Code] = p
	}
	return branches, partners
}

// checkDealer runs every check on d against the register.
func (o *Ops) checkDealer(ctx context.Context, tenant string, d Dealer, all []Dealer) error {
	today := o.today()
	branches, partners := o.dealerRefs(ctx, tenant)
	fs := dealerIssues(d, today, branches, partners)
	for i, t := range d.Terminals {
		for _, f := range terminalIssues(t, today) {
			f.Field = fmt.Sprintf("terminals[%d].%s", i, f.Field)
			fs = append(fs, f)
		}
	}
	u := uniqueIssues(d, all, today)
	for i := range d.Terminals {
		if m, ok := u[i]; ok {
			fs = append(fs, FieldError{Field: fmt.Sprintf("terminals[%d].terminal_id", i), Msg: m})
		}
	}
	if d.Status == DealerActive {
		for _, x := range all {
			if x.ID != d.ID && x.Status == DealerActive && x.PAN == d.PAN {
				fs = append(fs, FieldError{Field: "pan", Msg: "PAN " + MaskPAN(d.PAN) + " is already on the register as " + x.ID + " " + x.Name()})
			}
		}
	}
	if len(fs) > 0 {
		return &ValidationError{Fields: fs}
	}
	return nil
}

// ---- changing ----

// SaveDealer adds a dealer (no ID) or changes one. Status and leaving are
// kept as they were: a dealer leaves with MarkDealerLeft.
func (o *Ops) SaveDealer(ctx context.Context, tenant, actor string, d Dealer) (Dealer, error) {
	if err := o.require(ctx, tenant, actor, "Adding or changing dealers and terminals", RoleMaker, RoleCompliance); err != nil {
		return d, err
	}
	cleanDealer(&d)
	all, err := o.allDealers(ctx, tenant)
	if err != nil {
		return d, err
	}
	var old *Dealer
	if d.ID != "" {
		for i := range all {
			if all[i].ID == d.ID {
				old = &all[i]
			}
		}
		if old == nil {
			return d, fmt.Errorf("%w: dealer %s", ErrNotFound, d.ID)
		}
		if d.PAN == MaskPAN(old.PAN) {
			d.PAN = old.PAN // the form showed it masked
		}
		d.Status, d.LeftOn = old.Status, old.LeftOn
		d.CreatedBy, d.CreatedAt, d.NISMHistory, d.NISMFiled = old.CreatedBy, old.CreatedAt, old.NISMHistory, old.NISMFiled
		if old.NISM.ValidTill != "" && (old.NISM.CertNo != d.NISM.CertNo || old.NISM.ValidTill != d.NISM.ValidTill) {
			d.NISMHistory = append(append([]NISMCert{}, old.NISMHistory...), old.NISM)
		}
	} else {
		d.Status, d.LeftOn = DealerActive, ""
	}
	if err := o.checkDealer(ctx, tenant, d, all); err != nil {
		return d, err
	}
	if d.ID == "" {
		n, err := o.next(ctx, tenant, cDealer, actor)
		if err != nil {
			return d, err
		}
		d.ID = fmt.Sprintf("DL-%04d", n)
		d.CreatedBy, d.CreatedAt = actor, dealerTime(o)
	}
	d.UpdatedBy, d.UpdatedAt = actor, dealerTime(o)
	if err := docs.Upsert(ctx, o.Docs, tenant, cDealer, d.ID, d, actor); err != nil {
		return d, err
	}
	o.log(ctx, tenant, actor, map[bool]string{true: "dealer.created", false: "dealer.changed"}[old == nil], d.ID, dealerLogDetail(d, old))
	if old != nil {
		o.settleDealerWork(ctx, tenant, actor, *old, d)
	}
	return d, nil
}

func dealerLogDetail(d Dealer, old *Dealer) map[string]any {
	m := map[string]any{"name": d.Name(), "pan": MaskPAN(d.PAN), "relationship": d.Relationship, "branch": d.Branch, "partner": d.Partner,
		"nism": d.NISM.CertNo, "nism_valid_till": d.NISM.ValidTill, "terminals": len(d.Terminals), "status": d.Status}
	if old != nil {
		var ch []string
		if old.PAN != d.PAN {
			ch = append(ch, "PAN "+MaskPAN(old.PAN)+" → "+MaskPAN(d.PAN))
		}
		if old.Name() != d.Name() {
			ch = append(ch, "name")
		}
		if old.NISM != d.NISM {
			ch = append(ch, "NISM "+old.NISM.CertNo+" "+old.NISM.ValidTill+" → "+d.NISM.CertNo+" "+d.NISM.ValidTill)
		}
		if old.Branch != d.Branch || old.Partner != d.Partner || old.Relationship != d.Relationship {
			ch = append(ch, "branch or AP")
		}
		ot, nt := map[string]string{}, map[string]string{}
		for _, t := range old.Terminals {
			ot[termKey(t)] = t.ActiveFrom + "/" + t.ClosedOn
		}
		for _, t := range d.Terminals {
			nt[termKey(t)] = t.ActiveFrom + "/" + t.ClosedOn
			if v, ok := ot[termKey(t)]; !ok {
				ch = append(ch, "terminal added "+t.Exchange+" "+t.TerminalID)
			} else if v != nt[termKey(t)] {
				ch = append(ch, "terminal dates "+t.Exchange+" "+t.TerminalID)
			}
		}
		for _, t := range old.Terminals {
			if _, ok := nt[termKey(t)]; !ok {
				ch = append(ch, "terminal removed "+t.Exchange+" "+t.TerminalID)
			}
		}
		m["changes"] = ch
	}
	return m
}

// settleDealerWork closes work items a change has settled.
func (o *Ops) settleDealerWork(ctx context.Context, tenant, actor string, old, d Dealer) {
	if old.NISM.ValidTill != "" && old.NISM.ValidTill != d.NISM.ValidTill {
		for _, lv := range nismLevels {
			o.closeWork(ctx, tenant, actor, wDealers, d.ID+":nism-"+lv+":"+old.NISM.ValidTill, "NISM certificate updated")
		}
	}
	if d.Status == DealerLeft && len(d.openTerminals(o.today())) == 0 {
		o.closeWork(ctx, tenant, actor, wDealers, d.ID+":left-open:"+d.LeftOn, "terminals closed")
	}
}

// LeftRequest marks a dealer as left.
type LeftRequest struct {
	LeftOn string `json:"left_on"`
	// CloseTerminals closes the dealer's open terminals from the day after
	// leaving (the exchange files then report them inactive).
	CloseTerminals bool   `json:"close_terminals"`
	Note           string `json:"note"`
}

// MarkDealerLeft records that a dealer left the firm.
func (o *Ops) MarkDealerLeft(ctx context.Context, tenant, actor, id string, r LeftRequest) (Dealer, error) {
	if err := o.require(ctx, tenant, actor, "Marking a dealer as left", RoleCompliance); err != nil {
		return Dealer{}, err
	}
	d, _, err := docs.Get[Dealer](ctx, o.Docs, tenant, cDealer, id)
	if err != nil {
		return d, err
	}
	if d.Status == DealerLeft {
		return d, fmt.Errorf("%w: %s already left on %s", ErrState, id, d.LeftOn)
	}
	old := d
	d.Terminals = append([]DealerTerminal{}, d.Terminals...)
	d.Status, d.LeftOn = DealerLeft, strings.TrimSpace(r.LeftOn)
	if r.CloseTerminals && validDate(d.LeftOn) {
		after := shiftDate(d.LeftOn, 0, 0, 1)
		for i, t := range d.Terminals {
			if t.openOn(after) {
				d.Terminals[i].ClosedOn = max(after, t.ActiveFrom)
			}
		}
	}
	if n := strings.TrimSpace(r.Note); n != "" {
		d.Notes = strings.TrimSpace(d.Notes + "\n" + "Left: " + n)
	}
	all, err := o.allDealers(ctx, tenant)
	if err != nil {
		return d, err
	}
	if err := o.checkDealer(ctx, tenant, d, all); err != nil {
		return old, err
	}
	d.UpdatedBy, d.UpdatedAt = actor, dealerTime(o)
	if err := docs.Upsert(ctx, o.Docs, tenant, cDealer, d.ID, d, actor); err != nil {
		return d, err
	}
	det := dealerLogDetail(d, &old)
	det["left_on"], det["closed_terminals"] = d.LeftOn, r.CloseTerminals
	o.log(ctx, tenant, actor, "dealer.left", d.ID, det)
	o.settleDealerWork(ctx, tenant, actor, old, d)
	return d, nil
}

// ---- who traded: terminal to dealer ----

// dealerIndex finds the dealer of a terminal on a day.
type dealerIndex struct {
	exact    map[string][]dealerTerm // exchange|ID upper-cased
	patterns []dealerTerm
}

type dealerTerm struct {
	d int
	t DealerTerminal
}

type dealerBook struct {
	ds  []Dealer
	idx dealerIndex
}

func newDealerBook(ds []Dealer) *dealerBook {
	b := &dealerBook{ds: ds, idx: dealerIndex{exact: map[string][]dealerTerm{}}}
	for i, d := range ds {
		for _, t := range d.Terminals {
			if t.pattern() {
				b.idx.patterns = append(b.idx.patterns, dealerTerm{i, t})
				continue
			}
			b.idx.exact[t.Exchange+"|"+strings.ToUpper(t.TerminalID)] = append(b.idx.exact[t.Exchange+"|"+strings.ToUpper(t.TerminalID)], dealerTerm{i, t})
			if t.CTCLID != "" && !strings.EqualFold(t.CTCLID, t.TerminalID) {
				b.idx.exact[t.Exchange+"|"+strings.ToUpper(t.CTCLID)] = append(b.idx.exact[t.Exchange+"|"+strings.ToUpper(t.CTCLID)], dealerTerm{i, t})
			}
		}
	}
	return b
}

// find: an exact terminal or CTCL ID first, then patterns in register
// order; only terminals in use on day.
func (b *dealerBook) find(exchange, terminal, day string) (int, bool) {
	terminal = strings.TrimSpace(terminal)
	if terminal == "" {
		return -1, false
	}
	exchange = strings.ToUpper(strings.TrimSpace(exchange))
	for _, x := range b.idx.exact[exchange+"|"+strings.ToUpper(terminal)] {
		if x.t.activeOn(day) {
			return x.d, true
		}
	}
	for _, x := range b.idx.patterns {
		if x.t.Exchange != exchange || !x.t.activeOn(day) {
			continue
		}
		if ok, _ := path.Match(strings.ToUpper(x.t.TerminalID), strings.ToUpper(terminal)); ok {
			return x.d, true
		}
	}
	return -1, false
}

// DealerFor is the dealer who used a terminal on a date.
func (o *Ops) DealerFor(ctx context.Context, tenant, exchange, terminal, date string) (Dealer, bool) {
	ds, err := o.allDealers(ctx, tenant)
	if err != nil {
		return Dealer{}, false
	}
	b := newDealerBook(ds)
	if i, ok := b.find(exchange, terminal, date); ok {
		return ds[i], true
	}
	return Dealer{}, false
}

// tagOTRDealers names the dealer of each terminal on an order-to-trade
// list (BSE's order log).
func (o *Ops) tagOTRDealers(ctx context.Context, tenant, exchange, date string, rows []OTRRow) {
	ds, err := o.allDealers(ctx, tenant)
	if err != nil || len(ds) == 0 {
		return
	}
	b := newDealerBook(ds)
	for i := range rows {
		if j, ok := b.find(exchange, rows[i].Key, date); ok {
			rows[i].Dealer = ds[j].ID + " " + ds[j].Name()
		}
	}
}

// ---- the screen ----

// DealerRow is a dealer as listed (PAN masked).
type DealerRow struct {
	Dealer
	Name      string `json:"name"`
	PANMasked string `json:"pan_masked"`
	Open      int    `json:"open_terminals"`
	NISMDays  *int   `json:"nism_days,omitempty"` // days left (negative: expired)
	NISMState string `json:"nism_state"`          // Valid, Expiring, Expired, Missing
	AP        string `json:"ap_name,omitempty"`
}

// DealerCounts are the screen's figures.
type DealerCounts struct {
	Active       int `json:"active"`
	Left         int `json:"left"`
	Terminals    int `json:"terminals"` // open terminals of all dealers
	Expiring60   int `json:"expiring_60"`
	Expired      int `json:"expired"`
	Missing      int `json:"nism_missing"`
	LeftOpen     int `json:"left_with_terminals"`
	Unattributed int `json:"unattributed_trades"` // on the date
}

// DealersView is the Dealers & terminals screen.
type DealersView struct {
	Date          string              `json:"date"`
	Dealers       []DealerRow         `json:"dealers"`
	Expiring      []DealerRow         `json:"expiring"`
	Counts        DealerCounts        `json:"counts"`
	Unattributed  []UnattributedRow   `json:"unattributed"`
	Branches      []string            `json:"branches"`
	APs           []map[string]string `json:"aps"`
	Relationships []string            `json:"relationships"`
	Exchanges     []string            `json:"exchanges"`
	Segments      []string            `json:"segments"`
	AlgoCodes     map[string]string   `json:"algo_codes"`
	CanEdit       bool                `json:"can_edit"`
	CanLeave      bool                `json:"can_leave"`
	ImportColumns []string            `json:"import_columns"`
}

func dealerRow(d Dealer, today string, partners map[string]Partner) DealerRow {
	r := DealerRow{Dealer: d, Name: d.Name(), PANMasked: MaskPAN(d.PAN), Open: len(d.openTerminals(today)), AP: partners[d.Partner].Name}
	r.PAN = r.PANMasked
	switch {
	case d.NISM.ValidTill == "":
		r.NISMState = "Missing"
	default:
		n := daysBetween(today, d.NISM.ValidTill)
		r.NISMDays = &n
		switch {
		case n < 0:
			r.NISMState = "Expired"
		case n <= 60:
			r.NISMState = "Expiring"
		default:
			r.NISMState = "Valid"
		}
	}
	return r
}

// Dealers is the register with its figures for a date.
func (o *Ops) Dealers(ctx context.Context, tenant, actor, date string) (DealersView, error) {
	if err := o.canSeeDealers(ctx, tenant, actor); err != nil {
		return DealersView{}, err
	}
	if date == "" {
		date = o.today()
	}
	if !validDate(date) {
		return DealersView{}, invalid("date must be YYYY-MM-DD")
	}
	ds, err := o.allDealers(ctx, tenant)
	if err != nil {
		return DealersView{}, err
	}
	today := o.today()
	branches, partners := o.dealerRefs(ctx, tenant)
	v := DealersView{Date: date, Dealers: []DealerRow{}, Expiring: []DealerRow{}, Unattributed: []UnattributedRow{}, Branches: sortedKeys(branches), APs: []map[string]string{},
		Relationships: DealerRelationships, Exchanges: DealerExchanges, Segments: DealerSegments, AlgoCodes: BSEAlgoCodes, ImportColumns: dealerCSVColumns}
	for _, c := range sortedKeys(partners) {
		if p := partners[c]; p.Kind == RoleAP {
			v.APs = append(v.APs, map[string]string{"code": p.Code, "name": p.Name, "branch": p.Branch})
		}
	}
	v.CanEdit = o.canEditDealers(ctx, tenant, actor)
	v.CanLeave = o.require(ctx, tenant, actor, "Marking a dealer as left", RoleCompliance) == nil
	for _, d := range ds {
		r := dealerRow(d, today, partners)
		v.Dealers = append(v.Dealers, r)
		if d.Status == DealerLeft {
			v.Counts.Left++
			if r.Open > 0 {
				v.Counts.LeftOpen++
			}
		} else {
			v.Counts.Active++
			switch r.NISMState {
			case "Expiring":
				v.Counts.Expiring60++
			case "Expired":
				v.Counts.Expired++
			case "Missing":
				v.Counts.Missing++
			}
			if r.NISMState == "Expiring" || r.NISMState == "Expired" || r.NISMState == "Missing" && r.Open > 0 {
				v.Expiring = append(v.Expiring, r)
			}
		}
		v.Counts.Terminals += r.Open
	}
	sort.SliceStable(v.Expiring, func(i, j int) bool { return v.Expiring[i].NISM.ValidTill < v.Expiring[j].NISM.ValidTill })
	ts, _ := o.exchangeTrades(ctx, tenant, date)
	rep := dealerAttribution(ds, ts)
	v.Unattributed = rep.Unattributed
	for _, u := range rep.Unattributed {
		v.Counts.Unattributed += u.Trades
	}
	return v, nil
}

// ---- report ----

// DealerReportRow is one dealer's business in a period.
type DealerReportRow struct {
	DealerID     string       `json:"dealer_id"`
	Name         string       `json:"name"`
	Branch       string       `json:"branch,omitempty"`
	Partner      string       `json:"partner,omitempty"`
	Relationship string       `json:"relationship"`
	Status       string       `json:"status"`
	Trades       int          `json:"trades"`
	Clients      int          `json:"clients"`
	Turnover     money.Dec    `json:"turnover"`
	Brokerage    money.Amount `json:"brokerage"`
}

// UnattributedRow is business from a terminal no dealer had.
type UnattributedRow struct {
	Exchange string    `json:"exchange"`
	Segment  string    `json:"segment"`
	Terminal string    `json:"terminal"` // blank: the trade carried none
	Trades   int       `json:"trades"`
	Clients  int       `json:"clients"`
	Turnover money.Dec `json:"turnover"`
	First    string    `json:"first"`
	Last     string    `json:"last"`
}

// ExpiredNISMRow is business by a dealer without a valid NISM certificate
// on the trade date.
type ExpiredNISMRow struct {
	Date      string    `json:"date"`
	DealerID  string    `json:"dealer_id"`
	Name      string    `json:"name"`
	Exchange  string    `json:"exchange"`
	Terminal  string    `json:"terminal"`
	ValidTill string    `json:"valid_till,omitempty"`
	Reason    string    `json:"reason"`
	Trades    int       `json:"trades"`
	Clients   int       `json:"clients"`
	Turnover  money.Dec `json:"turnover"`
}

// DealerReport is dealer-wise business with its exceptions.
type DealerReport struct {
	From         string            `json:"from"`
	To           string            `json:"to"`
	Rows         []DealerReportRow `json:"rows"`
	Unattributed []UnattributedRow `json:"unattributed"`
	ExpiredNISM  []ExpiredNISMRow  `json:"expired_nism"`
	Trades       int               `json:"trades"`
	Attributed   int               `json:"attributed"`
	Turnover     money.Dec         `json:"turnover"`
	Brokerage    money.Amount      `json:"brokerage"` // unattributed included
	BrokerageOn  bool              `json:"brokerage_from_bills"`
	BrokerageOff money.Amount      `json:"unattributed_brokerage"`
}

type dealerAttr struct {
	rows      map[int]*DealerReportRow
	clients   map[int]map[string]bool
	un        map[string]*UnattributedRow
	unCl      map[string]map[string]bool
	ex        map[string]*ExpiredNISMRow
	exCl      map[string]map[string]bool
	trades    int
	attrib    int
	turnover  money.Dec
	byGroup   map[string]map[int]money.Dec // bill group -> dealer (-1 none) -> turnover
	groupTrds map[string]map[int]int
	Report    DealerReport
}

// dealerAttribution credits trades to dealers.
func dealerAttribution(ds []Dealer, ts []ingest.TradeEvent) DealerReport {
	a := attribute(ds, ts)
	return a.Report
}

func attribute(ds []Dealer, ts []ingest.TradeEvent) *dealerAttr {
	b := newDealerBook(ds)
	a := &dealerAttr{rows: map[int]*DealerReportRow{}, clients: map[int]map[string]bool{}, un: map[string]*UnattributedRow{}, unCl: map[string]map[string]bool{},
		ex: map[string]*ExpiredNISMRow{}, exCl: map[string]map[string]bool{}, byGroup: map[string]map[int]money.Dec{}, groupTrds: map[string]map[int]int{}}
	for _, t := range ts {
		v := t.Value()
		a.trades++
		a.turnover = a.turnover.Add(v)
		g := t.TradeDate + "|" + t.UCC + "|" + t.Exchange + "|" + t.Segment
		if a.byGroup[g] == nil {
			a.byGroup[g], a.groupTrds[g] = map[int]money.Dec{}, map[int]int{}
		}
		i, ok := b.find(t.Exchange, t.Terminal, t.TradeDate)
		if !ok {
			a.byGroup[g][-1] = a.byGroup[g][-1].Add(v)
			a.groupTrds[g][-1]++
			k := t.Exchange + "|" + t.Segment + "|" + strings.TrimSpace(t.Terminal)
			u := a.un[k]
			if u == nil {
				u = &UnattributedRow{Exchange: t.Exchange, Segment: t.Segment, Terminal: strings.TrimSpace(t.Terminal), First: t.TradeDate, Last: t.TradeDate}
				a.un[k], a.unCl[k] = u, map[string]bool{}
			}
			u.Trades++
			u.Turnover = u.Turnover.Add(v)
			u.First, u.Last = min(u.First, t.TradeDate), max(u.Last, t.TradeDate)
			a.unCl[k][t.UCC] = true
			continue
		}
		a.attrib++
		a.byGroup[g][i] = a.byGroup[g][i].Add(v)
		a.groupTrds[g][i]++
		d := ds[i]
		r := a.rows[i]
		if r == nil {
			r = &DealerReportRow{DealerID: d.ID, Name: d.Name(), Branch: d.Branch, Partner: d.Partner, Relationship: d.Relationship, Status: d.Status}
			a.rows[i], a.clients[i] = r, map[string]bool{}
		}
		r.Trades++
		r.Turnover = r.Turnover.Add(v)
		a.clients[i][t.UCC] = true
		if !d.nismValidOn(t.TradeDate) {
			k := t.TradeDate + "|" + d.ID + "|" + t.Exchange + "|" + strings.TrimSpace(t.Terminal)
			e := a.ex[k]
			if e == nil {
				e = &ExpiredNISMRow{Date: t.TradeDate, DealerID: d.ID, Name: d.Name(), Exchange: t.Exchange, Terminal: strings.TrimSpace(t.Terminal), ValidTill: lastValidTill(d, t.TradeDate)}
				e.Reason = "NISM certificate expired on " + e.ValidTill
				if e.ValidTill == "" {
					e.Reason = "No NISM certificate recorded for the date"
				}
				a.ex[k], a.exCl[k] = e, map[string]bool{}
			}
			e.Trades++
			e.Turnover = e.Turnover.Add(v)
			a.exCl[k][t.UCC] = true
		}
	}
	rep := DealerReport{Rows: []DealerReportRow{}, Unattributed: []UnattributedRow{}, ExpiredNISM: []ExpiredNISMRow{}, Trades: a.trades, Attributed: a.attrib, Turnover: a.turnover}
	for i, r := range a.rows {
		r.Clients = len(a.clients[i])
		rep.Rows = append(rep.Rows, *r)
	}
	sort.Slice(rep.Rows, func(i, j int) bool {
		if c := rep.Rows[i].Turnover.Cmp(rep.Rows[j].Turnover); c != 0 {
			return c > 0
		}
		return rep.Rows[i].DealerID < rep.Rows[j].DealerID
	})
	for k, u := range a.un {
		u.Clients = len(a.unCl[k])
		rep.Unattributed = append(rep.Unattributed, *u)
	}
	sort.Slice(rep.Unattributed, func(i, j int) bool {
		x, y := rep.Unattributed[i], rep.Unattributed[j]
		if x.Trades != y.Trades {
			return x.Trades > y.Trades
		}
		return x.Exchange+x.Segment+x.Terminal < y.Exchange+y.Segment+y.Terminal
	})
	for k, e := range a.ex {
		e.Clients = len(a.exCl[k])
		rep.ExpiredNISM = append(rep.ExpiredNISM, *e)
	}
	sort.Slice(rep.ExpiredNISM, func(i, j int) bool {
		x, y := rep.ExpiredNISM[i], rep.ExpiredNISM[j]
		return x.Date+x.DealerID+x.Exchange+x.Terminal < y.Date+y.DealerID+y.Exchange+y.Terminal
	})
	a.Report = rep
	return a
}

// lastValidTill is the latest certificate end on or before day.
func lastValidTill(d Dealer, day string) string {
	best := ""
	for _, c := range append([]NISMCert{d.NISM}, d.NISMHistory...) {
		if c.ValidTill != "" && c.ValidTill < day && c.ValidTill > best {
			best = c.ValidTill
		}
	}
	return best
}

// billBrokerage is brokerage billed per trade date, client, exchange and
// segment (net of reversals), keyed date|ucc|exchange|segment.
func (o *Ops) billBrokerage(ctx context.Context, tenant, from, to string) (map[string]money.Amount, error) {
	all, err := o.App.Ledger.Store.List(ctx, tenant, ledger.Filter{})
	if err != nil {
		return nil, err
	}
	byID := map[string]ledger.Entry{}
	for _, e := range all {
		byID[e.ID] = e
	}
	out := map[string]money.Amount{}
	for _, e := range all {
		src := e
		if e.ReversalOf != "" {
			src = byID[e.ReversalOf]
		}
		if src.Source.Kind != "BILL" {
			continue
		}
		p := strings.Split(src.Source.ID, "/") // date/ucc/EXCH-SEG
		if len(p) < 3 || p[0] < from || p[0] > to {
			continue
		}
		es := strings.SplitN(p[2], "-", 2)
		if len(es) != 2 {
			continue
		}
		for _, x := range e.Postings {
			if x.Account == ledger.IncomeBrokerage {
				out[p[0]+"|"+p[1]+"|"+es[0]+"|"+es[1]] += x.Credit - x.Debit
			}
		}
	}
	return out, nil
}

// MaxDealerReportDays caps a report's period.
const MaxDealerReportDays = 366

// DealerBusiness is dealer-wise turnover, trades, clients and brokerage
// for a period, with trades no dealer is known for and trades by dealers
// without a valid NISM certificate on the day.
func (o *Ops) DealerBusiness(ctx context.Context, tenant, actor, from, to string) (DealerReport, error) {
	if err := o.canSeeDealers(ctx, tenant, actor); err != nil {
		return DealerReport{}, err
	}
	if !validDate(from) || !validDate(to) || to < from {
		return DealerReport{}, invalid("give the period as from and to dates (YYYY-MM-DD), from not after to")
	}
	if daysBetween(from, to) > MaxDealerReportDays {
		return DealerReport{}, invalid("a report covers at most %d days", MaxDealerReportDays)
	}
	ds, err := o.allDealers(ctx, tenant)
	if err != nil {
		return DealerReport{}, err
	}
	var ts []ingest.TradeEvent
	for d := from; d <= to; d = shiftDate(d, 0, 0, 1) {
		x, err := o.exchangeTrades(ctx, tenant, d)
		if err != nil {
			return DealerReport{}, err
		}
		ts = append(ts, x...)
	}
	a := attribute(ds, ts)
	rep := a.Report
	rep.From, rep.To = from, to
	// Brokerage is billed per client, exchange segment and day: each
	// bill's brokerage is shared among the dealers (and "no dealer") in
	// proportion to the turnover each put through in it.
	brok, err := o.billBrokerage(ctx, tenant, from, to)
	if err != nil {
		return rep, err
	}
	rep.BrokerageOn = len(brok) > 0
	share := map[int]money.Amount{}
	for g, amt := range brok {
		rep.Brokerage += amt
		parts := a.byGroup[g]
		if len(parts) == 0 {
			share[-1] += amt
			continue
		}
		var tot money.Dec
		keys := make([]int, 0, len(parts))
		for k, v := range parts {
			tot = tot.Add(v)
			keys = append(keys, k)
		}
		sort.Ints(keys)
		left := amt
		for n, k := range keys {
			var s money.Amount
			switch {
			case n == len(keys)-1:
				s = left
			case tot.Sign() > 0:
				q, _ := amt.Dec().Mul(parts[k]).Quo(tot)
				s = q.Round(money.UnitPaisa)
			default:
				s = money.Amount(int64(amt) * int64(a.groupTrds[g][k]) / int64(max(1, sumInts(a.groupTrds[g]))))
			}
			share[k] += s
			left -= s
		}
	}
	idx := map[string]int{}
	for i, d := range ds {
		idx[d.ID] = i
	}
	for i := range rep.Rows {
		rep.Rows[i].Brokerage = share[idx[rep.Rows[i].DealerID]]
	}
	rep.BrokerageOff = share[-1]
	return rep, nil
}

func sumInts(m map[int]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

// ---- daily check ----

var nismLevels = []string{"60", "30", "7", "expiry"}

type dealerTickDoc struct {
	Date string `json:"date"`
}

// dealersTick raises the day's dealer work once a day: NISM certificates
// running out (60, 30 and 7 days ahead, and on the day), and dealers who
// left with terminals still open. Each item's key names its threshold and
// the certificate's end date, so it is raised once, not every day.
func (o *Ops) dealersTick(ctx context.Context, tenant string, now time.Time) {
	today := now.In(ist).Format("2006-01-02")
	last, _, _ := docs.GetOr(ctx, o.Docs, tenant, cDealerTick, "last", dealerTickDoc{})
	if last.Date >= today {
		return
	}
	ds, err := o.allDealers(ctx, tenant)
	if err != nil {
		return
	}
	for _, d := range ds {
		open := d.openTerminals(today)
		var ids []string
		for _, t := range open {
			ids = append(ids, t.Exchange+" "+t.TerminalID)
		}
		terms := "no open terminals"
		if len(ids) > 0 {
			terms = "terminals " + strings.Join(ids, ", ")
		}
		if d.Status == DealerActive && d.NISM.ValidTill != "" {
			n := daysBetween(today, d.NISM.ValidTill)
			lv := ""
			switch {
			case n <= 0:
				lv = "expiry"
			case n <= 7:
				lv = "7"
			case n <= 30:
				lv = "30"
			case n <= 60:
				lv = "60"
			}
			if lv != "" {
				title := fmt.Sprintf("NISM of %s (%s) runs out in %d days", d.Name(), d.ID, n)
				detail := fmt.Sprintf("NISM Series VIII certificate %s is valid till %s (%s). Get the renewed certificate (CPE or exam) and update the dealer; then send BSE's NISM file (renewal).", d.NISM.CertNo, fmtDay(d.NISM.ValidTill), terms)
				switch {
				case n < 0:
					title = fmt.Sprintf("NISM of %s (%s) expired: block the terminal", d.Name(), d.ID)
					detail = fmt.Sprintf("NISM certificate %s expired on %s. Block or disable %s at the exchange and in the trading system until a renewed certificate is on the register; trades from them are listed as exceptions in the dealer report.", d.NISM.CertNo, fmtDay(d.NISM.ValidTill), terms)
				case n == 0:
					title = fmt.Sprintf("NISM of %s (%s) expires today: block the terminal from tomorrow", d.Name(), d.ID)
					detail = fmt.Sprintf("NISM certificate %s is valid till today (%s). Unless the renewed certificate is on the register, block %s before the next trading day.", d.NISM.CertNo, fmtDay(d.NISM.ValidTill), terms)
				}
				o.raiseWork(ctx, tenant, wDealers, d.ID+":nism-"+lv+":"+d.NISM.ValidTill, title, detail, "", RoleCompliance, "dealers", today)
				for _, x := range nismLevels {
					if x == lv {
						break
					}
					o.closeWork(ctx, tenant, SystemActor, wDealers, d.ID+":nism-"+x+":"+d.NISM.ValidTill, "a nearer reminder replaced it")
				}
			}
		}
		if d.Status == DealerLeft && len(open) > 0 {
			o.raiseWork(ctx, tenant, wDealers, d.ID+":left-open:"+d.LeftOn, fmt.Sprintf("%s (%s) left: close %d terminal(s)", d.Name(), d.ID, len(open)),
				fmt.Sprintf("%s left on %s but %s are still open. Disable them at the exchange and in the trading system, record the closing date on the dealer, and send BSE's dealer registration file.", d.Name(), fmtDay(d.LeftOn), terms),
				"", RoleCompliance, "dealers", today)
		}
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cDealerTick, "last", dealerTickDoc{Date: today}, SystemActor)
}

// ---- CSV import and export ----

// dealerCSVColumns are the dealer file's columns: one row per terminal (a
// dealer's details repeated on each), or one row for a dealer without
// terminals.
var dealerCSVColumns = []string{"DealerID", "PAN", "FirstName", "MiddleName", "LastName", "DOB", "Mobile", "Email", "Relationship", "Branch", "Partner",
	"OfficeAddress", "OfficeArea", "OfficeCity", "OfficeState", "OfficePincode", "ResidenceAddress", "ResidenceArea", "ResidenceCity", "ResidencePincode",
	"NISMCertNo", "NISMExamDate", "NISMValidTill", "Status", "LeftOn", "Notes",
	"Exchange", "Segments", "Terminal", "LocationID", "CTCLID", "Algo", "AlgoCode", "ConnectionMode", "BranchID", "Vendor", "TradingID", "ActiveFrom", "ClosedOn", "Purge", "ExchangeRecordID"}

var dealerCSVRequired = []string{"PAN", "FirstName", "Relationship"}

// dealer-level columns (the same on every row of a dealer).
var dealerLevelCols = dealerCSVColumns[:26]

func dealerFromRow(r row) Dealer {
	g := r.get
	return Dealer{ID: g("DealerID"), PAN: g("PAN"), FirstName: g("FirstName"), MiddleName: g("MiddleName"), LastName: g("LastName"), DOB: g("DOB"), Mobile: g("Mobile"), Email: g("Email"),
		Relationship: g("Relationship"), Branch: g("Branch"), Partner: g("Partner"),
		Office:    DealerAddress{Address: g("OfficeAddress"), Area: g("OfficeArea"), City: g("OfficeCity"), State: g("OfficeState"), Pincode: g("OfficePincode")},
		Residence: DealerAddress{Address: g("ResidenceAddress"), Area: g("ResidenceArea"), City: g("ResidenceCity"), Pincode: g("ResidencePincode")},
		NISM:      NISMCert{CertNo: g("NISMCertNo"), ExamDate: g("NISMExamDate"), ValidTill: g("NISMValidTill")},
		Status:    g("Status"), LeftOn: g("LeftOn"), Notes: g("Notes")}
}

func yes(s string) bool {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "Y", "YES", "TRUE", "1":
		return true
	}
	return false
}

func terminalFromRow(r row) DealerTerminal {
	g := r.get
	return DealerTerminal{Exchange: g("Exchange"), Segments: []string{g("Segments")}, TerminalID: g("Terminal"), LocationID: g("LocationID"), CTCLID: g("CTCLID"),
		Algo: yes(g("Algo")), AlgoCode: g("AlgoCode"), ConnectionMode: g("ConnectionMode"), BranchID: g("BranchID"), Vendor: g("Vendor"), TradingID: g("TradingID"),
		ActiveFrom: g("ActiveFrom"), ClosedOn: g("ClosedOn"), Purge: yes(g("Purge")), ExchangeRecordID: g("ExchangeRecordID")}
}

// DealerImportResult is what a dealer file did.
type DealerImportResult struct {
	Parsed  int                 `json:"parsed"`
	Added   int                 `json:"added"`
	Updated int                 `json:"updated"`
	IDs     []string            `json:"ids"`
	Errors  []ingest.ParseError `json:"errors"`
}

// ImportDealers loads a dealer CSV (columns as dealerCSVColumns; PAN,
// FirstName and Relationship are required). A dealer is matched by
// DealerID, else by PAN among active dealers; the terminals in the file
// replace the dealer's list (an exchange record ID left blank is kept from
// the same terminal on the register). Strict: any row error rejects the
// whole file.
func (o *Ops) ImportDealers(ctx context.Context, tenant, actor string, data []byte) (DealerImportResult, error) {
	res := DealerImportResult{IDs: []string{}, Errors: []ingest.ParseError{}}
	if err := o.require(ctx, tenant, actor, "Importing dealers and terminals", RoleMaker, RoleCompliance); err != nil {
		return res, err
	}
	bad := func(l int, f, m string, a ...any) {
		res.Errors = append(res.Errors, ingest.ParseError{Line: l, Field: f, Msg: fmt.Sprintf(m, a...)})
	}
	reject := func() (DealerImportResult, error) {
		sort.SliceStable(res.Errors, func(i, j int) bool { return res.Errors[i].Line < res.Errors[j].Line })
		err := fmt.Errorf("%w: %d row error(s) in the dealer file", ingest.ErrRejected, len(res.Errors))
		o.log(ctx, tenant, actor, "dealer.import.rejected", "dealers", map[string]any{"rows": res.Parsed, "errors": len(res.Errors)})
		return res, err
	}
	rows, perrs := parseCSV(data, dealerCSVRequired)
	res.Parsed = len(rows)
	if len(perrs) > 0 {
		res.Errors = perrs
		return reject()
	}
	if len(rows) == 0 {
		bad(1, "", "the file has no dealers")
		return reject()
	}
	role, _ := o.RoleOf(ctx, tenant, actor)
	canLeave := role == RoleCompliance || role == RoleAdmin
	all, err := o.allDealers(ctx, tenant)
	if err != nil {
		return res, err
	}
	byID := map[string]Dealer{}
	byPAN := map[string]Dealer{}
	for _, d := range all {
		byID[d.ID] = d
		if d.Status == DealerActive {
			byPAN[d.PAN] = d
		}
	}
	type entry struct {
		d     Dealer
		line  int
		tline []int // terminal i's line
		first row
		old   *Dealer
	}
	var order []string
	groups := map[string]*entry{}
	for _, r := range rows {
		k := strings.ToUpper(firstOf(r.get("DealerID"), r.get("PAN")))
		e := groups[k]
		if e == nil {
			d := dealerFromRow(r)
			cleanDealer(&d)
			e = &entry{d: d, line: r.line, first: r}
			groups[k] = e
			order = append(order, k)
		} else {
			for _, c := range dealerLevelCols {
				if v := r.get(c); v != "" && !strings.EqualFold(v, e.first.get(c)) {
					bad(r.line, c, "%q differs from line %d for the same dealer", v, e.line)
				}
			}
		}
		if r.get("Exchange") != "" || r.get("Terminal") != "" {
			t := terminalFromRow(r)
			cleanTerminal(&t)
			for _, f := range terminalIssues(t, o.today()) {
				bad(r.line, f.Field, "%s", f.Msg)
			}
			e.d.Terminals = append(e.d.Terminals, t)
			e.tline = append(e.tline, r.line)
		}
	}
	today := o.today()
	branches, partners := o.dealerRefs(ctx, tenant)
	var result []Dealer
	replaced := map[string]bool{}
	for _, k := range order {
		e := groups[k]
		d := &e.d
		switch {
		case d.ID != "":
			old, ok := byID[d.ID]
			if !ok {
				bad(e.line, "DealerID", "no dealer %s on the register (leave DealerID blank to add one)", d.ID)
				continue
			}
			e.old = &old
		default:
			if old, ok := byPAN[d.PAN]; ok {
				e.old = &old
				d.ID = old.ID
			}
		}
		if e.old != nil {
			if replaced[e.old.ID] {
				bad(e.line, "DealerID", "dealer %s appears twice in the file", e.old.ID)
				continue
			}
			replaced[e.old.ID] = true
			d.CreatedBy, d.CreatedAt, d.NISMHistory, d.NISMFiled = e.old.CreatedBy, e.old.CreatedAt, e.old.NISMHistory, e.old.NISMFiled
			if e.old.NISM.ValidTill != "" && (e.old.NISM.CertNo != d.NISM.CertNo || e.old.NISM.ValidTill != d.NISM.ValidTill) {
				d.NISMHistory = append(append([]NISMCert{}, e.old.NISMHistory...), e.old.NISM)
			}
			for i, t := range d.Terminals {
				for _, x := range e.old.Terminals {
					if t.ExchangeRecordID == "" && termKey(x) == termKey(t) {
						d.Terminals[i].ExchangeRecordID = x.ExchangeRecordID
					}
				}
			}
		}
		if d.Status == "" {
			d.Status = DealerActive
			if e.old != nil {
				d.Status = e.old.Status
				d.LeftOn = firstOf(d.LeftOn, e.old.LeftOn)
			}
		}
		for _, s := range []string{DealerActive, DealerLeft} {
			if strings.EqualFold(d.Status, s) {
				d.Status = s
			}
		}
		if d.Status == DealerLeft && !canLeave && (e.old == nil || e.old.Status != DealerLeft) {
			bad(e.line, "Status", "marking a dealer as left needs the Compliance officer")
		}
		if d.Status == DealerActive && e.old != nil && e.old.Status == DealerLeft {
			bad(e.line, "Status", "%s has left; add the dealer again as a new record (leave DealerID blank)", e.old.ID)
		}
		for _, f := range dealerIssues(*d, today, branches, partners) {
			bad(e.line, dealerCSVField(f.Field), "%s", f.Msg)
		}
		result = append(result, *d)
	}
	// Terminals and PANs unique across the register as it will be.
	var after []Dealer
	for _, d := range all {
		if !replaced[d.ID] {
			after = append(after, d)
		}
	}
	// A clash is reported on the later line: earlier rows of the file
	// count as on the register.
	for n, k := range order {
		e := groups[k]
		d := e.d
		others := append([]Dealer{}, after...)
		for _, k2 := range order[:n] {
			x := groups[k2].d
			if x.ID == "" {
				x.ID = fmt.Sprintf("line %d", groups[k2].line)
			}
			others = append(others, x)
		}
		if d.ID == "" {
			d.ID = fmt.Sprintf("line %d", e.line)
		}
		for i, m := range uniqueIssues(d, others, today) {
			bad(e.tline[i], "Terminal", "%s", m)
		}
		if d.Status == DealerActive {
			for _, x := range others {
				if x.Status == DealerActive && x.PAN == d.PAN && x.ID != d.ID {
					bad(e.line, "PAN", "PAN %s is on another dealer (%s %s)", MaskPAN(d.PAN), x.ID, x.Name())
					break
				}
			}
		}
	}
	if len(res.Errors) > 0 {
		return reject()
	}
	for _, d := range result {
		var old *Dealer
		if x, ok := byID[d.ID]; ok && d.ID != "" {
			old = &x
		}
		if d.ID == "" {
			n, err := o.next(ctx, tenant, cDealer, actor)
			if err != nil {
				return res, err
			}
			d.ID = fmt.Sprintf("DL-%04d", n)
			d.CreatedBy, d.CreatedAt = actor, dealerTime(o)
			res.Added++
		} else {
			res.Updated++
		}
		d.UpdatedBy, d.UpdatedAt = actor, dealerTime(o)
		if err := docs.Upsert(ctx, o.Docs, tenant, cDealer, d.ID, d, actor); err != nil {
			return res, err
		}
		det := dealerLogDetail(d, old)
		det["source"] = "import"
		o.log(ctx, tenant, actor, map[bool]string{true: "dealer.created", false: "dealer.changed"}[old == nil], d.ID, det)
		if old != nil {
			o.settleDealerWork(ctx, tenant, actor, *old, d)
		}
		res.IDs = append(res.IDs, d.ID)
	}
	o.log(ctx, tenant, actor, "dealer.imported", "dealers", map[string]any{"rows": res.Parsed, "added": res.Added, "updated": res.Updated})
	return res, nil
}

// dealerCSVField names the CSV column of a form field.
func dealerCSVField(f string) string {
	m := map[string]string{"first_name": "FirstName", "pan": "PAN", "dob": "DOB", "mobile": "Mobile", "email": "Email", "branch": "Branch", "partner": "Partner",
		"relationship": "Relationship", "office.pincode": "OfficePincode", "residence.pincode": "ResidencePincode", "nism.series": "NISMCertNo",
		"nism.exam_date": "NISMExamDate", "nism.valid_till": "NISMValidTill", "nism.cert_no": "NISMCertNo", "left_on": "LeftOn", "status": "Status"}
	return firstOf(m[f], f)
}

// ExportDealersCSV writes the register in the import layout (PANs in
// full: for moving the register, so maker and compliance only).
func (o *Ops) ExportDealersCSV(ctx context.Context, tenant, actor string) ([]byte, error) {
	if err := o.require(ctx, tenant, actor, "Exporting dealers with PANs", RoleMaker, RoleCompliance); err != nil {
		return nil, err
	}
	ds, err := o.allDealers(ctx, tenant)
	if err != nil {
		return nil, err
	}
	yn := func(b bool) string {
		if b {
			return "Y"
		}
		return ""
	}
	out := [][]string{dealerCSVColumns}
	for _, d := range ds {
		base := []string{d.ID, d.PAN, d.FirstName, d.MiddleName, d.LastName, d.DOB, d.Mobile, d.Email, d.Relationship, d.Branch, d.Partner,
			d.Office.Address, d.Office.Area, d.Office.City, d.Office.State, d.Office.Pincode, d.Residence.Address, d.Residence.Area, d.Residence.City, d.Residence.Pincode,
			d.NISM.CertNo, d.NISM.ExamDate, d.NISM.ValidTill, d.Status, d.LeftOn, d.Notes}
		if len(d.Terminals) == 0 {
			out = append(out, append(base, make([]string, len(dealerCSVColumns)-len(base))...))
		}
		for _, t := range d.Terminals {
			out = append(out, append(append([]string{}, base...), t.Exchange, strings.Join(t.Segments, ";"), t.TerminalID, t.LocationID, t.CTCLID, yn(t.Algo), t.AlgoCode,
				t.ConnectionMode, t.BranchID, t.Vendor, t.TradingID, t.ActiveFrom, t.ClosedOn, yn(t.Purge), t.ExchangeRecordID))
		}
	}
	o.log(ctx, tenant, actor, "dealer.exported", "dealers", map[string]any{"dealers": len(ds)})
	return csvBytesOf(out), nil
}

// InspectionCSV is "Terminals and users" for an inspection: every terminal
// and segment with its location, its dealer (PAN masked), the dealer's
// NISM certificate and branch or AP, and whether it is open.
func (o *Ops) InspectionCSV(ctx context.Context, tenant, actor string) ([]byte, error) {
	if err := o.canSeeDealers(ctx, tenant, actor); err != nil {
		return nil, err
	}
	ds, err := o.allDealers(ctx, tenant)
	if err != nil {
		return nil, err
	}
	_, partners := o.dealerRefs(ctx, tenant)
	today := o.today()
	out := [][]string{{"Exchange", "Segment", "Terminal / user ID", "Location ID", "CTCL ID", "Algo", "Active from", "Closed on", "Terminal status",
		"Dealer ID", "Dealer", "PAN", "Relationship", "Branch / AP", "NISM certificate", "NISM series", "NISM valid till", "NISM status", "Dealer status"}}
	type line struct {
		k string
		r []string
	}
	var ls []line
	for _, d := range ds {
		r := dealerRow(d, today, partners)
		where := d.Branch
		if d.Partner != "" {
			where = strings.TrimSpace("AP " + d.Partner + " " + partners[d.Partner].Name)
		}
		for _, t := range d.Terminals {
			st := "Open"
			if !t.openOn(today) {
				st = "Closed"
			} else if t.ActiveFrom > today {
				st = "From " + t.ActiveFrom
			}
			algo := "N"
			if t.Algo {
				algo = "Y"
			}
			for _, s := range t.Segments {
				ls = append(ls, line{t.Exchange + "|" + s + "|" + t.TerminalID + "|" + d.ID, []string{t.Exchange, s, t.TerminalID, t.LocationID, t.CTCLID, algo, t.ActiveFrom, t.ClosedOn, st,
					d.ID, d.Name(), MaskPAN(d.PAN), d.Relationship, where, d.NISM.CertNo, d.NISM.Series, d.NISM.ValidTill, r.NISMState, d.Status}})
			}
		}
	}
	sort.Slice(ls, func(i, j int) bool { return ls[i].k < ls[j].k })
	for _, l := range ls {
		out = append(out, l.r)
	}
	o.log(ctx, tenant, actor, "dealer.inspection", "dealers", map[string]any{"rows": len(ls)})
	return csvBytesOf(out), nil
}

// ---- BSE upload files ----

// DealerFileKinds maps a download kind to its layout in config/formats.
var DealerFileKinds = map[string]string{"bse-dealer": "bse-dealer-registration", "bse-nism": "bse-dealer-nism", "bse-dealer-pan": "bse-dealer-pan"}

// Codes written on BSE's dealer registration file where BSE's code lists
// are not in the published layout (to confirm with BSE).
var (
	bseRelationship = map[string]string{DealerEmployee: "0", DealerAP: "1", DealerAPEmployee: "2"}
	bseUserType     = "1"
	bseAppType      = "0"
)

// DealerFile is a BSE upload file made from the register.
type DealerFile struct {
	Kind     string   `json:"kind"`
	Format   string   `json:"format"`
	Title    string   `json:"title"`
	Name     string   `json:"name"`
	Rows     int      `json:"rows"`
	Problems []string `json:"problems"`
	Content  []byte   `json:"-"`
}

var bseClean = strings.NewReplacer("|", " ", "\r", " ", "\n", " ", `"`, "'")

func bv(s string) string { return strings.TrimSpace(bseClean.Replace(s)) }

func ynFlag(b bool) string {
	if b {
		return "Y"
	}
	return "N"
}

// locPincode is the pincode of a terminal's location: the first six
// digits of a numeric location ID, else the office pincode.
func locPincode(t DealerTerminal, d Dealer) string {
	if len(t.LocationID) >= 6 && rePincode.MatchString(t.LocationID[:6]) {
		return t.LocationID[:6]
	}
	return d.Office.Pincode
}

// MakeDealerFile writes one of BSE's dealer files for the dealers chosen
// (all when ids is empty). The NISM file's action (A, M or RN) is worked
// out from what was last written for the dealer unless action names one.
// With check, nothing is recorded and problems are listed instead of
// refusing.
func (o *Ops) MakeDealerFile(ctx context.Context, tenant, actor, kind string, ids []string, action string, check bool) (DealerFile, error) {
	f := DealerFile{Kind: kind, Problems: []string{}}
	if err := o.require(ctx, tenant, actor, "Making BSE dealer files", RoleMaker, RoleCompliance); err != nil {
		return f, err
	}
	f.Format = DealerFileKinds[kind]
	if f.Format == "" {
		return f, invalid("file kind must be bse-dealer, bse-nism or bse-dealer-pan (NSE publishes no dealer file layout)")
	}
	action = strings.ToUpper(strings.TrimSpace(action))
	if action != "" && action != "A" && action != "M" && action != "RN" {
		return f, invalid("NISM action must be A, M or RN")
	}
	lib, err := XFiles()
	if err != nil {
		return f, err
	}
	s, ok := lib.Get(f.Format)
	if !ok {
		return f, fmt.Errorf("layout %s is missing", f.Format)
	}
	f.Title = s.Title
	ds, err := o.allDealers(ctx, tenant)
	if err != nil {
		return f, err
	}
	want := map[string]bool{}
	for _, id := range ids {
		if id = strings.ToUpper(strings.TrimSpace(id)); id != "" {
			want[id] = true
		}
	}
	_, partners := o.dealerRefs(ctx, tenant)
	today := o.today()
	var rows []xfile.Row
	var filed []Dealer
	prob := func(d Dealer, m string, a ...any) {
		f.Problems = append(f.Problems, d.ID+" "+d.Name()+": "+fmt.Sprintf(m, a...))
	}
	found := map[string]bool{}
	for _, d := range ds {
		if len(want) > 0 && !want[d.ID] {
			continue
		}
		found[d.ID] = true
		switch kind {
		case "bse-dealer":
			for _, t := range d.Terminals {
				if t.Exchange != "BSE" {
					continue
				}
				active := "Y"
				switch {
				case !t.openOn(today) && t.Purge:
					active = "P"
				case !t.openOn(today):
					active = "N"
				}
				act := "A"
				if t.ExchangeRecordID != "" {
					act = "M"
				}
				if active != "Y" && t.ExchangeRecordID == "" {
					if len(want) > 0 {
						prob(d, "closed terminal %s has no BSE record ID; nothing to change at BSE", t.TerminalID)
					}
					continue
				}
				if active != "Y" && len(want) == 0 && t.ClosedOn < shiftDate(today, 0, 0, -30) {
					continue // closed long ago: sent then; choose the dealer to send it again
				}
				if t.pattern() {
					prob(d, "terminal %s is a pattern; BSE needs the exact user ID", t.TerminalID)
					continue
				}
				if d.DOB == "" || d.Office.Address == "" || d.Office.City == "" || d.Office.Pincode == "" {
					prob(d, "BSE needs the date of birth and the office address, city and pincode")
				}
				nature := "0"
				if d.Partner != "" {
					nature = "1"
				}
				segs := map[string]bool{}
				for _, x := range t.Segments {
					segs[x] = true
				}
				sebi := ""
				if d.Relationship != DealerEmployee {
					sebi = partners[d.Partner].RegNo
				}
				rows = append(rows, xfile.Row{Values: map[string]string{
					"Add/Modify": act, "UserType": bseUserType, "Pincode": locPincode(t, d), "Tws/BranchId": bv(t.BranchID), "UserId": bv(t.TerminalID), "14 Digit": t.algoCode(),
					"Vendorcode": bv(t.Vendor), "OfficeAddress": bv(d.Office.Address), "OfficeAreaName": bv(d.Office.Area), "OfficeCity": bv(d.Office.City), "OfficePincode": d.Office.Pincode,
					"NatureofOffice": nature, "FirstName": bv(d.FirstName), "MiddleName": bv(d.MiddleName), "LastName": bv(d.LastName), "Emailaddress": bv(d.Email), "PhoneNumber": "",
					"Mobile": d.Mobile, "Mapinno": "", "SebiRegiNo": bv(sebi), "Relationship": bseRelationship[d.Relationship], "Dateofbirth": d.DOB,
					"ResidenceAddress": bv(d.Residence.Address), "ResidenceAreaName": bv(d.Residence.Area), "ResidenceCity": bv(d.Residence.City), "ResidencePincode": d.Residence.Pincode,
					"ConnectionMode": firstOf(bv(t.ConnectionMode), "0"), "ActiveTerminal": active, "ApplicationType": bseAppType, "PANNumber": d.PAN,
					"Cash": ynFlag(segs["CM"]), "EquityDerivative": ynFlag(segs["FO"]), "CurrencyDerivative": ynFlag(segs["CD"]), "Commodity": ynFlag(segs["COM"]), "EGR": ynFlag(segs["EGR"]),
					"AlGO_NONALGO": ynFlag(t.Algo), "ExchangeAllotedTradingID": bv(t.TradingID), "ID": bv(t.ExchangeRecordID)}})
			}
		case "bse-nism":
			n := d.NISM
			if d.Status != DealerActive && len(want) == 0 {
				continue
			}
			if n.CertNo == "" || n.ValidTill == "" || n.ExamDate == "" {
				if len(want) > 0 || len(d.openTerminals(today)) > 0 {
					prob(d, "the NISM certificate number, exam date and valid-till date are needed")
				}
				continue
			}
			a := action
			if a == "" {
				switch {
				case d.NISMFiled == "":
					a = "A"
				case d.NISMFiled != n.CertNo+"|"+n.ValidTill:
					a = "RN"
				default:
					a = "M"
				}
			}
			rows = append(rows, xfile.Row{Values: map[string]string{"Add/Modify/Renew": a, "PAN_Number": d.PAN, "FirstName": bv(d.FirstName), "MiddleName": bv(d.MiddleName),
				"LastName": bv(d.LastName), "Segment": "EQD", "Enrollment_Number": bv(n.CertNo), "ExamDate": n.ExamDate, "ValidTillDate": n.ValidTill, "NISM_Approve_Flag": ""}})
			filed = append(filed, d)
		case "bse-dealer-pan":
			for _, t := range d.Terminals {
				if t.Exchange != "BSE" || !t.openOn(today) {
					continue
				}
				if t.LocationID == "" {
					prob(d, "BSE terminal %s needs its location ID", t.TerminalID)
					continue
				}
				flag := "A"
				if t.ExchangeRecordID != "" {
					flag = "M"
				}
				rows = append(rows, xfile.Row{Values: map[string]string{"Initial Flag": flag, "Location ID": bv(t.LocationID), "First Name": bv(d.FirstName), "Middle Name": bv(d.MiddleName),
					"Last Name": bv(d.LastName), "Pincode": locPincode(t, d), "Area Name": bv(d.Office.Area), "City Name": bv(d.Office.City), "State Name": bv(d.Office.State),
					"Address": bv(d.Office.Address), "Date Of Birth": d.DOB, "PAN NO": d.PAN}})
			}
		}
	}
	for id := range want {
		if !found[id] {
			f.Problems = append(f.Problems, "no dealer "+id)
		}
	}
	sort.Strings(f.Problems)
	f.Rows = len(rows)
	f.Name, err = s.FileName(xfile.Vars{"date": today})
	if err != nil {
		return f, err
	}
	if check {
		return f, nil
	}
	if len(f.Problems) > 0 {
		return f, invalid("%s: %s", s.Title, strings.Join(f.Problems, "; "))
	}
	if len(rows) == 0 {
		return f, invalid("%s: no dealer has anything for this file", s.Title)
	}
	if f.Content, err = s.Write(rows); err != nil {
		return f, invalid("%v", err)
	}
	for _, d := range filed {
		_, _ = docs.Update(ctx, o.Docs, tenant, cDealer, d.ID, actor, func(x *Dealer) error { x.NISMFiled = x.NISM.CertNo + "|" + x.NISM.ValidTill; return nil })
	}
	o.log(ctx, tenant, actor, "dealer.file", f.Name, map[string]any{"format": f.Format, "rows": f.Rows, "dealers": len(found)})
	return f, nil
}
