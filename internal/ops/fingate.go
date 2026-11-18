package ops

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// ---- FIU-IND FINGate 2.0: suspicious transaction reports ----
//
// When the Principal Officer decides an alert is suspicious (Surveillance &
// AML → File STR), the STR is due on FIU-IND's FINGate 2.0 within 7
// working days (PML Rules 8(2)). Ledgerline writes the FINGate "Bulk Data
// Upload" for the decided STRs (FINGate 2.0 Reports user manual v1.6,
// section 2 and Annexure 12.1; FIU-IND Reporting Format v1.14, section 2.5
// Brokerage Firms): a .zip with no folders holding one CSV per format, named
// as FINGate names them, each row tied to its report by the report
// reference number:
//
//   - KC1 / KC2: the client's KYC (individual / non-individual);
//   - Account_Detail and Account_Person_Relation: the trading account and
//     the client's relation to it;
//   - TS7: the client's trades in the STR's period (Brokerage Firms STR);
//   - GS1: the grounds of suspicion, from the Principal Officer's reasons.
//
// The Principal Officer uploads it on FINGate (My Reports → Bulk Data
// Upload, fresh data), creates the batch, signs it (DSC or eSign) and
// records the batch ID and report ID on the alert, which closes the work
// item. FINGate's own report API needs FIU-IND's separate API
// specification, not yet in hand.

const strDueWorkingDays = 7

// AlertSTR is the STR decided on an alert and its filing.
type AlertSTR struct {
	DecidedOn string `json:"decided_on"`
	DecidedBy string `json:"decided_by"`
	Due       string `json:"due"`
	Grounds   string `json:"grounds"`            // the Principal Officer's reasons (GS1 narration)
	From      string `json:"from"`               // the transactions reported: trade dates
	To        string `json:"to"`                 //
	Exported  string `json:"exported,omitempty"` // when the FINGate upload was last written
	Filing    string `json:"filing,omitempty"`   // FINGate batch and report IDs
	FiledOn   string `json:"filed_on,omitempty"`
	FiledBy   string `json:"filed_by,omitempty"`
	// Ref is the report reference number in the upload; the rest the
	// grounds-of-suspicion details (GS1).
	Ref         string `json:"ref,omitempty"`
	FundSource  string `json:"fund_source,omitempty"`
	FundDest    string `json:"fund_destination,omitempty"`
	Suspicion   string `json:"suspicion,omitempty"`
	SourceAlert string `json:"source_alert,omitempty"`
	RedFlag     string `json:"red_flag,omitempty"`
	RedFlagNote string `json:"red_flag_other,omitempty"`
	Offence     string `json:"offence,omitempty"`
	OffenceNote string `json:"offence_other,omitempty"`
}

// ---- FINGate codes ----
//
// FINGate's coded fields take FIU-IND's "Entry ID" values from the metadata
// CSVs on FINGate's Learning and Resources tab (country, state, district,
// city, nationality, customer type, occupation, account type, account
// holder type and the grounds-of-suspicion tags); the manual prints none of
// them. The firm loads each metadata CSV once; Ledgerline then finds a
// value's ID by its name. A value it cannot code is left blank and listed,
// for the Principal Officer to pick on FINGate after the upload.

const (
	cFINGateMeta = "fingatemeta" // key: list
	cFINGateSet  = "fingateset"  // one document: the firm's choices
)

// FINGateLists are the metadata lists Ledgerline uses, by key and name.
var FINGateLists = []struct{ Key, Name string }{
	{"country", "Country"}, {"state", "State"}, {"district", "District"}, {"city", "City / Village / Town"},
	{"nationality", "Nationality"}, {"customer_type", "Customer Type"}, {"occupation", "Occupation"},
	{"account_type", "Account Type"}, {"relation_type", "Account Holder Type"},
	{"suspicion", "Suspicious Category (GoS Tag 1)"}, {"source_alert", "Source of Alert (GoS Tag 2)"},
	{"red_flag", "Red Flag Indicator (GoS Tag 3)"}, {"offence", "Type of Offence (GoS Tag 4)"},
}

// FINGateCode is one metadata value.
type FINGateCode struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// FINGateMeta is one loaded metadata list.
type FINGateMeta struct {
	List   string        `json:"list"`
	Rows   []FINGateCode `json:"rows"`
	Loaded string        `json:"loaded"`
	By     string        `json:"by"`
}

// FINGateSettings are the firm's FINGate choices: the branch code of its
// RE profile and the metadata values (names or IDs) for a client's trading
// account.
type FINGateSettings struct {
	BranchCode     string `json:"branch_code"`
	AccountType    string `json:"account_type"`     // e.g. Trading Account
	RelationType   string `json:"relation_type"`    // e.g. Primary Account Holder
	CustTypeInd    string `json:"cust_type_ind"`    // customer type for individuals
	CustTypeEntity string `json:"cust_type_entity"` // and for non-individuals
	SourceAlert    string `json:"source_alert"`     // GoS Tag 2 when the STR does not say
}

// FINGateView is the codes page.
type FINGateView struct {
	Settings FINGateSettings   `json:"settings"`
	Lists    []FINGateListView `json:"lists"`
}

// FINGateListView is one list's state.
type FINGateListView struct {
	Key    string        `json:"key"`
	Name   string        `json:"name"`
	Count  int           `json:"count"`
	Loaded string        `json:"loaded,omitempty"`
	Values []FINGateCode `json:"values,omitempty"` // the short lists, for choosing
}

func fgNorm(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

type fgCodes map[string]FINGateMeta

func (o *Ops) fingateCodes(ctx context.Context, tenant string) fgCodes {
	m := fgCodes{}
	l, _ := docs.List[FINGateMeta](ctx, o.Docs, tenant, cFINGateMeta, "")
	for _, x := range l {
		m[x.List] = x
	}
	return m
}

// code is a value's ID in a list ("" when not found); an ID given as such
// is kept.
func (c fgCodes) code(list, v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	n := fgNorm(v)
	for _, r := range c[list].Rows {
		if fgNorm(r.Text) == n || r.ID == v {
			return r.ID
		}
	}
	return ""
}

// FINGateStatus is the codes page.
func (o *Ops) FINGateStatus(ctx context.Context, tenant, actor string) (FINGateView, error) {
	if err := o.require(ctx, tenant, actor, "FINGate reporting", RoleCompliance); err != nil {
		return FINGateView{}, err
	}
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cFINGateSet, "settings", FINGateSettings{})
	codes := o.fingateCodes(ctx, tenant)
	v := FINGateView{Settings: st}
	for _, l := range FINGateLists {
		m := codes[l.Key]
		x := FINGateListView{Key: l.Key, Name: l.Name, Count: len(m.Rows), Loaded: m.Loaded}
		if len(m.Rows) <= 200 && l.Key != "country" && l.Key != "state" && l.Key != "nationality" {
			x.Values = m.Rows
		}
		v.Lists = append(v.Lists, x)
	}
	return v, nil
}

// SaveFINGateSettings stores the firm's choices.
func (o *Ops) SaveFINGateSettings(ctx context.Context, tenant, actor string, s FINGateSettings) (FINGateSettings, error) {
	if err := o.require(ctx, tenant, actor, "FINGate reporting", RoleCompliance); err != nil {
		return s, err
	}
	for _, p := range []*string{&s.BranchCode, &s.AccountType, &s.RelationType, &s.CustTypeInd, &s.CustTypeEntity, &s.SourceAlert} {
		*p = strings.Join(strings.Fields(*p), " ")
	}
	if len(s.BranchCode) > 30 {
		return s, invalid("the branch code is at most 30 characters, as in the FINGate RE profile")
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cFINGateSet, "settings", s, actor); err != nil {
		return s, err
	}
	o.log(ctx, tenant, actor, "fingate.settings", "settings", nil)
	return s, nil
}

var reFGID = regexp.MustCompile(`(?i)^(entry ?id|id|.*[ _]id|code)$`)

// LoadFINGateMeta reads one of FINGate's metadata CSVs: the ID column
// (Entry ID, or a column named …ID) and the first other column as its name.
// Source of Alert takes its exact text, so its name is its value.
func (o *Ops) LoadFINGateMeta(ctx context.Context, tenant, actor, list string, data []byte) (int, error) {
	if err := o.require(ctx, tenant, actor, "FINGate reporting", RoleCompliance); err != nil {
		return 0, err
	}
	known := false
	for _, l := range FINGateLists {
		known = known || l.Key == list
	}
	if !known {
		return 0, invalid("no such FINGate list %q", list)
	}
	r := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))))
	r.FieldsPerRecord, r.LazyQuotes = -1, true
	recs, err := r.ReadAll()
	if err != nil || len(recs) < 2 {
		return 0, invalid("not a metadata CSV (a header row, then values): %v", err)
	}
	idc, txc := -1, -1
	for i, h := range recs[0] {
		h = strings.TrimSpace(strings.TrimPrefix(h, "\uFEFF"))
		if idc < 0 && reFGID.MatchString(h) {
			idc = i
		} else if txc < 0 && h != "" {
			txc = i
		}
	}
	if list == "source_alert" {
		if txc < 0 {
			txc = idc
		}
		idc = txc
	}
	if idc < 0 || txc < 0 {
		return 0, invalid("the CSV needs an ID column (Entry ID) and a name column")
	}
	var rows []FINGateCode
	for _, rec := range recs[1:] {
		if idc >= len(rec) || txc >= len(rec) {
			continue
		}
		id, tx := strings.TrimSpace(rec[idc]), strings.TrimSpace(rec[txc])
		if id != "" && tx != "" {
			rows = append(rows, FINGateCode{ID: id, Text: tx})
		}
	}
	if len(rows) == 0 {
		return 0, invalid("no values in the CSV")
	}
	m := FINGateMeta{List: list, Rows: rows, Loaded: o.now().Format(time.RFC3339), By: actor}
	if err := docs.Upsert(ctx, o.Docs, tenant, cFINGateMeta, list, m, actor); err != nil {
		return 0, err
	}
	o.log(ctx, tenant, actor, "fingate.metadata", list, map[string]any{"values": len(rows)})
	return len(rows), nil
}

// ---- The STR's own details ----

// STRInput is what the Principal Officer adds when deciding an STR.
type STRInput struct {
	Grounds     string `json:"grounds"`
	From        string `json:"from"`
	To          string `json:"to"`
	FundSource  string `json:"fund_source"`
	FundDest    string `json:"fund_destination"`
	Suspicion   string `json:"suspicion"`    // GoS Tag 1
	SourceAlert string `json:"source_alert"` // GoS Tag 2
	RedFlag     string `json:"red_flag"`     // GoS Tag 3
	RedFlagNote string `json:"red_flag_other"`
	Offence     string `json:"offence"` // GoS Tag 4
	OffenceNote string `json:"offence_other"`
}

// DecideSTR records the decision to report an alert (ActOnAlert "str")
// with the STR's details.
func (o *Ops) DecideSTR(ctx context.Context, tenant, actor, id string, in STRInput) (Alert, error) {
	for _, d := range []string{in.From, in.To} {
		if d != "" && !validDate(d) {
			return Alert{}, invalid("the period's dates are YYYY-MM-DD")
		}
	}
	if in.From != "" && in.To != "" && (in.From > in.To || daysBetween(in.From, in.To) > 366) {
		return Alert{}, invalid("the period runs forward, at most a year")
	}
	a, err := o.ActOnAlert(ctx, tenant, actor, id, "str", in.Grounds)
	if err != nil {
		return a, err
	}
	n, _ := o.next(ctx, tenant, "strref", SystemActor)
	return docs.Update(ctx, o.Docs, tenant, cAlert, id, actor, func(x *Alert) error {
		s := x.STR
		s.Ref = fmt.Sprintf("LLSTR%06d", n)
		s.From, s.To = firstOf(in.From, s.From), firstOf(in.To, s.To)
		s.FundSource, s.FundDest = strings.TrimSpace(in.FundSource), strings.TrimSpace(in.FundDest)
		s.Suspicion, s.SourceAlert, s.RedFlag, s.RedFlagNote = strings.TrimSpace(in.Suspicion), strings.TrimSpace(in.SourceAlert), strings.TrimSpace(in.RedFlag), strings.TrimSpace(in.RedFlagNote)
		s.Offence, s.OffenceNote = strings.TrimSpace(in.Offence), strings.TrimSpace(in.OffenceNote)
		return nil
	})
}

// ---- The upload ----

// FINGateGap is a field the upload leaves for FINGate's portal.
type FINGateGap struct {
	Report string `json:"report"`
	File   string `json:"file"`
	Field  string `json:"field"`
	Why    string `json:"why"`
}

// FINGateExport is the upload for a set of STRs.
type FINGateExport struct {
	Name    string       `json:"name"`
	Reports []string     `json:"reports"` // report reference numbers
	Alerts  []string     `json:"alerts"`
	Files   []string     `json:"files"`
	Rows    int          `json:"rows"`
	Gaps    []FINGateGap `json:"gaps"`
	Zip     []byte       `json:"-"`
}

var (
	fgKC1 = []string{"Report Reference Number", "UCIC", "Customer ID", "PAN", "Declaration (If PAN is not available)", "CKYC Number", "Declaration (If CKYC is not available)",
		"Passport Number", "Voter ID", "Drivers License Number", "NREGA Card", "DIN / DPIN", "First Name", "Middle Name", "Last Name", "Declaration (If Last name is not available)",
		"Name of Father", "Name of Mother", "Spouse/Partner Name", "Gender", "Date of Birth", "Nationality", "Mobile Number", "Alternate Mobile Number", "Telephone Number", "Email ID",
		"Primary Address 1", "Primary Address Country", "Primary Address Pin Code", "Primary Address Locality", "Primary Address State", "Primary Address District", "Primary Address City",
		"Secondary Address 1", "Secondary Address Country", "Secondary Address Pin Code", "Secondary Address Locality", "Secondary Address State", "Secondary Address District", "Secondary Address City",
		"Customer Type", "Other Customer Type", "Annual Income (INR)", "Occupation", "Other Occupation", "Employer Name", "Employer Address 1", "Employer Address Country", "Employer Address Pin Code",
		"Employer Address Locality", "Employer Address State", "Employer Address District", "Employer Address City", "Date of Customer On-boarding", "Date of Last KYC / re-KYC", "Customer Risk Level",
		"NPR", "PEKRN", "Primary State Name", "Primary District Name", "Primary City Name", "Secondary State Name", "Secondary District Name", "Secondary City Name",
		"Employee State Name", "Employee District Name", "Employee City Name", "Identity verified using Aadhaar ID"}
	fgKC2 = []string{"Report Reference Number", "UCIC", "Entity Name", "Company ID Type", "Company ID Number", "PAN Number", "Declaration (If PAN is not available)", "TAN", "GSTIN",
		"No identifier available", "IEC", "Declaration (If IEC is not available)", "Telephone Number", "Mobile Number", "Email ID", "Company Website", "Country", "Address Line 1", "PIN Code",
		"Locality", "State", "District", "City / Village / Town", "Line of Business", "FCRA Status", "FCRA Registration State", "FCRA Registration Number", "Customer ID", "Registered Country",
		"Registered Address", "Registered PIN Code", "Registered Locality", "Registered State", "Registered District", "Registered City / Village / Town", "Date Of Incorporation",
		"Customer Type", "Last KYC Date", "Onboarding Date", "Customer Risk Level", "Declaration (If UBO is not available)", "PEKRN", "State Name", "District Name",
		"City / Village / Town Name", "Registered State Name", "Registered District Name", "Registered City / Village / Town Name", "Other Customer Type"}
	fgAcct = []string{"Report Reference Number", "Report Type", "Account Type", "Account Number", "Branch Code of Account", "Date of Account Opening", "Date of Account Closing", "Account Status",
		"No Of Debits (In last 12 months)", "Total Debit Amount (In last 12 months)", "No Of Credits (In last 12 months)", "Total Credit Amount (In last 12 months)",
		"No Of Cash Transaction (In last 12 months)", "Total Cash Deposit (In last 12 months)", "Total Cash Withdrawal (In last 12 months)", "Reason for Account Freeze"}
	fgAPR = []string{"Report Reference Number", "Report Type", "Account Number", "Relationship Type", "Unique Reference Number", "Individual / Non-individual", "Name of Non-customer"}
	fgTS7 = []string{"Report Reference Number", "Relationship Flag", "Transaction ID", "Transaction Type", "Trade Type", "Trade Date", "Trade Time", "Amount", "DP ID",
		"Declaration (If DP ID is not available)", "Related Bank Account Number", "Client ID/Customer ID/UCIC", "Related AC IFSC", "ISIN/ Product Code/Commodity", "Security Code",
		"Commodity Exchange Code", "Rate Per Unit / Lot", "No Of Units / Lots", "Expiry Date", "Call / Put", "Strike Price", "Report Type"}
	fgGS1 = []string{"Report Reference Number", "KYC Source of Funds", "KYC Destination of Funds", "GoS Tag 1 - Suspicion Due To", "GoS Tag 2 - Source of Alert", "GoS Tag 3 - Red Flag Indicator",
		"Other Red Flag Indicator", "Narration", "GoS Tag 4 - Type of Suspicion Suspected", "Other Offence Type"}
)

type fgTable struct {
	name string
	head []string
	rows [][]string
}

func (t *fgTable) add(v map[string]string) {
	row := make([]string, len(t.head))
	for i, h := range t.head {
		row[i] = v[h]
	}
	t.rows = append(t.rows, row)
}

func fgDate(d string) string {
	t, err := time.Parse("2006-01-02", d)
	if err != nil {
		return ""
	}
	return t.Format("02/01/2006")
}

var (
	reFGName = regexp.MustCompile(`[^A-Za-z' ]+`)
	reFGAlnm = regexp.MustCompile(`[^A-Za-z0-9]+`)
)

func fgMobile(m string) string {
	d := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, m)
	if len(d) == 12 && strings.HasPrefix(d, "91") {
		d = d[2:]
	}
	if len(d) != 10 {
		return ""
	}
	return "91-" + d
}

// fgText trims free text to FINGate's length, padding nothing: too short
// is returned empty, to be listed.
func fgText(s string, min, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		s = strings.TrimSpace(s[:max])
	}
	if len(s) < min {
		return ""
	}
	return s
}

// FINGateUpload builds the upload for the given alerts, or every alert with
// an STR to file when none is given.
func (o *Ops) FINGateUpload(ctx context.Context, tenant, actor string, ids []string) (FINGateExport, error) {
	if err := o.require(ctx, tenant, actor, "Preparing STRs for FINGate", RoleCompliance); err != nil {
		return FINGateExport{}, err
	}
	all, err := docs.List[Alert](ctx, o.Docs, tenant, cAlert, "")
	if err != nil {
		return FINGateExport{}, err
	}
	want := map[string]bool{}
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			want[id] = true
		}
	}
	var as []Alert
	for _, a := range all {
		if a.STR == nil || (len(want) > 0 && !want[a.ID]) || (len(want) == 0 && a.Status != "STR to file") {
			continue
		}
		if a.Status != "STR to file" && a.Status != "STR filed" {
			continue
		}
		as = append(as, a)
	}
	if len(as) == 0 {
		return FINGateExport{}, invalid("no STRs to file: decide one on an alert first (File STR)")
	}
	sort.Slice(as, func(i, j int) bool { return as[i].STR.Ref < as[j].STR.Ref })
	set, _, _ := docs.GetOr(ctx, o.Docs, tenant, cFINGateSet, "settings", FINGateSettings{})
	codes := o.fingateCodes(ctx, tenant)
	kc1, kc2 := &fgTable{name: "KC1", head: fgKC1}, &fgTable{name: "KC2", head: fgKC2}
	acct, apr := &fgTable{name: "Account_Detail", head: fgAcct}, &fgTable{name: "Account_Person_Relation", head: fgAPR}
	ts7, gs1 := &fgTable{name: "TS7", head: fgTS7}, &fgTable{name: "GS1", head: fgGS1}
	ex := FINGateExport{Gaps: []FINGateGap{}}
	gap := func(ref, file, field, why string) {
		ex.Gaps = append(ex.Gaps, FINGateGap{Report: ref, File: file, Field: field, Why: why})
	}
	// coded looks a value up, listing it when it cannot be coded.
	coded := func(ref, file, field, list, v string) string {
		id := codes.code(list, v)
		switch {
		case id != "":
		case strings.TrimSpace(v) == "":
			gap(ref, file, field, "not on record")
		case len(codes[list].Rows) == 0:
			gap(ref, file, field, fmt.Sprintf("%q: load FINGate's %s metadata", v, list))
		default:
			gap(ref, file, field, fmt.Sprintf("%q is not in FINGate's %s list", v, list))
		}
		return id
	}
	for _, a := range as {
		s, ref := *a.STR, a.STR.Ref
		if ref == "" {
			ref = "LL" + strings.ToUpper(reFGAlnm.ReplaceAllString(a.ID, ""))
			if len(ref) > 20 {
				ref = ref[:20]
			}
		}
		c, err := o.Client(ctx, tenant, a.UCC)
		if err != nil {
			return FINGateExport{}, fmt.Errorf("%w: alert %s names no client on record", ErrState, a.ID)
		}
		d := o.UCCDetailsFor(ctx, tenant, c.UCC)
		ex.Reports, ex.Alerts = append(ex.Reports, ref), append(ex.Alerts, a.ID)
		addr := fgText(strings.Join([]string{d.Address1, d.Address2, d.Address3}, " "), 5, 255)
		loc := fgText(firstOf(strings.TrimSpace(d.Address2+" "+d.Address3), firstOf(d.City, c.City)), 5, 255)
		city, state, pin := firstOf(d.City, c.City), firstOf(d.State, c.State), d.PIN

		if loc == "" {
			loc = fgText(addr, 5, 255)
		}
		risk := map[string]string{"low": "Low", "medium": "Medium", "high": "High"}[strings.ToLower(c.Risk)]
		onboard := fgDate(c.KYCDate)
		lastKYC := fgDate(firstOf(c.ReKYCOn, firstOf(c.KYCUpdatedOn, c.KYCDate)))
		individual := c.Type == "" || c.Type == "Individual" || (len(c.PAN) == 10 && c.PAN[3] == 'P')
		if addr == "" {
			gap(ref, map[bool]string{true: "KC1", false: "KC2"}[individual], "Address", "the client's address is not on record (Clients → UCC details)")
		}
		if individual {
			f := strings.Fields(reFGName.ReplaceAllString(c.Name, " "))
			v := map[string]string{"Report Reference Number": ref, "UCIC": c.UCC, "Customer ID": c.UCC, "PAN": c.PAN, "CKYC Number": c.CKYC,
				"Gender":        map[string]string{"male": "Male", "m": "Male", "female": "Female", "f": "Female", "transgender": "Others", "t": "Others", "u": "Others", "others": "Others"}[strings.ToLower(d.Gender)],
				"Date of Birth": fgDate(c.DOB), "Mobile Number": fgMobile(c.Mobile), "Email ID": strings.TrimSpace(c.Email), "Name of Father": fgText(reFGName.ReplaceAllString(d.Guardian, " "), 3, 100),
				"Primary Address 1": addr, "Primary Address Pin Code": pin, "Primary Address Locality": loc,
				"Date of Customer On-boarding": onboard, "Date of Last KYC / re-KYC": lastKYC, "Customer Risk Level": risk}
			if c.PAN == "" {
				v["Declaration (If PAN is not available)"] = "NA"
			}
			if c.CKYC == "" {
				v["Declaration (If CKYC is not available)"] = "NA"
			}
			if len(f) > 0 {
				v["First Name"] = f[0]
			}
			if len(f) > 1 {
				v["Last Name"] = f[len(f)-1]
				v["Middle Name"] = strings.Join(f[1:len(f)-1], " ")
			} else {
				v["Declaration (If Last name is not available)"] = "NA"
			}
			v["Nationality"] = coded(ref, "KC1", "Nationality", "nationality", firstOf(d.Nationality, "Indian"))
			v["Primary Address Country"] = coded(ref, "KC1", "Primary Address Country", "country", "India")
			v["Primary Address State"] = coded(ref, "KC1", "Primary Address State", "state", state)
			v["Primary Address City"] = coded(ref, "KC1", "Primary Address City", "city", city)
			v["Primary Address District"] = codes.code("district", city)
			if v["Primary Address District"] == "" {
				gap(ref, "KC1", "Primary Address District", "Ledgerline does not keep the district: pick it on FINGate")
			}
			v["Customer Type"] = coded(ref, "KC1", "Customer Type", "customer_type", firstOf(set.CustTypeInd, "Individual"))
			if d.Occupation != "" {
				v["Occupation"] = codes.code("occupation", firstOf(d.OccupationDetail, d.Occupation))
			}
			for _, k := range []string{"Gender", "Date of Birth", "Customer Risk Level", "Date of Customer On-boarding", "First Name"} {
				if v[k] == "" {
					gap(ref, "KC1", k, "not on record")
				}
			}
			kc1.add(v)
		} else {
			v := map[string]string{"Report Reference Number": ref, "UCIC": c.UCC, "Customer ID": c.UCC, "Entity Name": fgText(c.Name, 2, 100), "PAN Number": c.PAN, "GSTIN": c.GSTIN,
				"Mobile Number": fgMobile(c.Mobile), "Email ID": strings.TrimSpace(c.Email), "Address Line 1": addr, "PIN Code": pin, "Locality": loc,
				"FCRA Status": "No", "Date Of Incorporation": fgDate(c.DOB), "Last KYC Date": lastKYC, "Onboarding Date": onboard, "Customer Risk Level": risk}
			if d.CIN != "" {
				v["Company ID Type"], v["Company ID Number"] = "CIN", d.CIN
			}
			if c.PAN == "" {
				v["Declaration (If PAN is not available)"] = "NA"
			}
			v["Country"] = coded(ref, "KC2", "Country", "country", "India")
			v["State"] = coded(ref, "KC2", "State", "state", state)
			v["City / Village / Town"] = coded(ref, "KC2", "City / Village / Town", "city", city)
			v["District"] = codes.code("district", city)
			if v["District"] == "" {
				gap(ref, "KC2", "District", "Ledgerline does not keep the district: pick it on FINGate")
			}
			v["Customer Type"] = coded(ref, "KC2", "Customer Type", "customer_type", firstOf(set.CustTypeEntity, c.Type))
			if risk == "" {
				gap(ref, "KC2", "Customer Risk Level", "not on record")
			}
			kc2.add(v)
		}
		// The trading account and the client's relation to it.
		status := "ACTIVE"
		switch {
		case c.Status == "Closed":
			status = "CLOSED"
		case c.Blocked:
			status = "BLOCKED"
		case c.Inactive || c.Status == "Dormant":
			status = "DORMANT"
		}
		stat := o.fgAccountYear(ctx, tenant, c.UCC, s.From)
		branch := firstOf(set.BranchCode, c.Branch)
		if branch == "" {
			gap(ref, "Account_Detail", "Branch Code of Account", "enter the branch code of the FINGate RE profile")
		}
		acct.add(map[string]string{"Report Reference Number": ref, "Report Type": "STR", "Account Type": coded(ref, "Account_Detail", "Account Type", "account_type", firstOf(set.AccountType, "Trading Account")),
			"Account Number": c.UCC, "Branch Code of Account": branch, "Date of Account Opening": onboard, "Account Status": status,
			"No Of Debits (In last 12 months)": fmt.Sprint(stat.debits), "Total Debit Amount (In last 12 months)": stat.debit.Dec().Fixed(2),
			"No Of Credits (In last 12 months)": fmt.Sprint(stat.credits), "Total Credit Amount (In last 12 months)": stat.credit.Dec().Fixed(2),
			"No Of Cash Transaction (In last 12 months)": "0", "Total Cash Deposit (In last 12 months)": "0", "Total Cash Withdrawal (In last 12 months)": "0"})
		apr.add(map[string]string{"Report Reference Number": ref, "Report Type": "STR", "Account Number": c.UCC,
			"Relationship Type":       coded(ref, "Account_Person_Relation", "Relationship Type", "relation_type", firstOf(set.RelationType, "Primary Account Holder")),
			"Unique Reference Number": c.UCC, "Individual / Non-individual": map[bool]string{true: "Individual", false: "Non-individual"}[individual]})
		// The client's trades in the period.
		_, dpid, ben, _ := splitDemat(c.Demat)
		n := 0
		for day := s.From; day != "" && day <= s.To; day = addDays(day, 1) {
			trades, _ := o.exchangeTrades(ctx, tenant, day)
			var lk Lookup
			got := false
			for _, t := range trades {
				if t.UCC != c.UCC {
					continue
				}
				if !got {
					_, lk, _ = o.Market(ctx, tenant, day)
					got = true
				}
				tt := map[string]string{"EQ": "Equity", "FUT": "Futures", "FUTIDX": "Futures", "FUTSTK": "Futures", "CE": "Options", "PE": "Options"}[t.InstrumentType]
				if tt == "" {
					tt = "Equity"
					if t.Segment != "CM" {
						tt = "Futures"
					}
				}
				isin := ""
				if in, ok := lk.ForTrade(t); ok {
					isin = in.ISIN
				}
				v := map[string]string{"Report Reference Number": ref, "Relationship Flag": "Customer", "Transaction ID": fgText(reFGAlnm.ReplaceAllString(t.Exchange+t.Segment+t.TradeNo, ""), 1, 50),
					"Transaction Type": map[string]string{"B": "Buy", "S": "Sell"}[t.Side], "Trade Type": tt, "Trade Date": fgDate(t.TradeDate), "Trade Time": fgTime(t.TradeTime),
					"Amount": t.Value().Fixed(2), "Related Bank Account Number": c.Bank.Account, "Client ID/Customer ID/UCIC": c.UCC, "Related AC IFSC": strings.ToUpper(c.Bank.IFSC),
					"ISIN/ Product Code/Commodity": firstOf(isin, t.Symbol), "Security Code": t.Symbol, "Commodity Exchange Code": t.Exchange,
					"Rate Per Unit / Lot": t.Price.Fixed(2), "No Of Units / Lots": fmt.Sprint(t.Qty), "Report Type": "STR"}
				if dpid != "" {
					v["DP ID"] = dpid + ben
					if len(v["DP ID"]) > 20 {
						v["DP ID"] = dpid
					}
				} else {
					v["Declaration (If DP ID is not available)"] = "NA"
				}
				if tt != "Equity" {
					v["Expiry Date"] = fgDate(t.Expiry)
				}
				if tt == "Options" {
					v["Call / Put"] = map[string]string{"CE": "Call", "PE": "Put"}[t.InstrumentType]
					v["Strike Price"] = t.Strike
				}
				ts7.add(v)
				n++
			}
		}
		if n == 0 {
			gap(ref, "TS7", "Transactions", fmt.Sprintf("no trades of the client from %s to %s: set the STR's period, or add the transactions on FINGate", s.From, s.To))
		}
		// The grounds of suspicion.
		narr := strings.TrimSpace(s.Grounds)
		if a.Detail != "" {
			narr = strings.TrimSpace(a.Type + ": " + a.Detail + ". " + narr)
		}
		gv := map[string]string{"Report Reference Number": ref,
			"KYC Source of Funds":      fgText(firstOf(s.FundSource, "Funds received from the client's registered bank account"+fgBank(c)), 1, 1000),
			"KYC Destination of Funds": fgText(firstOf(s.FundDest, "Pay-outs to the client's registered bank account"+fgBank(c)), 1, 1000),
			"Narration":                fgText(narr, 1, 20000), "Other Red Flag Indicator": fgText(s.RedFlagNote, 3, 50), "Other Offence Type": fgText(s.OffenceNote, 3, 50)}
		gv["GoS Tag 1 - Suspicion Due To"] = coded(ref, "GS1", "GoS Tag 1 - Suspicion Due To", "suspicion", s.Suspicion)
		src := firstOf(s.SourceAlert, firstOf(set.SourceAlert, "Transaction Monitoring"))
		if id := codes.code("source_alert", src); id != "" || len(codes["source_alert"].Rows) == 0 {
			gv["GoS Tag 2 - Source of Alert"] = firstOf(id, src) // exact text
		} else {
			gap(ref, "GS1", "GoS Tag 2 - Source of Alert", fmt.Sprintf("%q is not in FINGate's source of alert list", src))
		}
		gv["GoS Tag 3 - Red Flag Indicator"] = coded(ref, "GS1", "GoS Tag 3 - Red Flag Indicator", "red_flag", s.RedFlag)
		gv["GoS Tag 4 - Type of Suspicion Suspected"] = coded(ref, "GS1", "GoS Tag 4 - Type of Suspicion Suspected", "offence", s.Offence)
		gs1.add(gv)
	}
	// The zip: one CSV per format used, no folders.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, t := range []*fgTable{kc1, kc2, acct, apr, ts7, gs1} {
		if len(t.rows) == 0 {
			continue
		}
		w, err := zw.Create(t.name + ".csv")
		if err != nil {
			return ex, err
		}
		cw := csv.NewWriter(w)
		_ = cw.Write(t.head)
		_ = cw.WriteAll(t.rows)
		if err := cw.Error(); err != nil {
			return ex, err
		}
		ex.Files = append(ex.Files, t.name+".csv")
		ex.Rows += len(t.rows)
	}
	if err := zw.Close(); err != nil {
		return ex, err
	}
	now := o.now()
	ex.Zip, ex.Name = buf.Bytes(), "FINGate_STR_"+now.In(ist).Format("20060102_1504")+".zip"
	for _, a := range as {
		if a.Status == "STR to file" {
			_, _ = docs.Update(ctx, o.Docs, tenant, cAlert, a.ID, actor, func(x *Alert) error {
				if x.STR != nil {
					x.STR.Exported = now.Format(time.RFC3339)
				}
				return nil
			})
		}
	}
	o.log(ctx, tenant, actor, "fingate.upload", ex.Name, map[string]any{"reports": len(ex.Reports), "gaps": len(ex.Gaps)})
	return ex, nil
}

var reFGTime = regexp.MustCompile(`\d{2}:\d{2}:\d{2}`)

// fgTime is a trade time as HH:MM:SS, or "".
func fgTime(t string) string {
	if m := reFGTime.FindString(strings.TrimSpace(t)); m != "" {
		return m
	}
	return ""
}

func fgBank(c Client) string {
	if c.Bank.Bank == "" {
		return ""
	}
	return " (" + c.Bank.Bank + ")"
}

type fgYear struct {
	debits, credits int
	debit, credit   money.Amount
}

// fgAccountYear is the client ledger's debits and credits in the 12 months
// before the first reported transaction.
func (o *Ops) fgAccountYear(ctx context.Context, tenant, ucc, first string) fgYear {
	var y fgYear
	if !validDate(first) {
		return y
	}
	t, _ := time.Parse("2006-01-02", first)
	from, to := t.AddDate(-1, 0, 0).Format("2006-01-02"), t.AddDate(0, 0, -1).Format("2006-01-02")
	es, _ := o.clientEntries(ctx, tenant, ucc, to)
	for _, e := range es {
		if e.ValueDate < from {
			continue
		}
		for _, p := range e.Postings {
			if !strings.HasPrefix(p.Account, "CLIENT") || !strings.HasSuffix(p.Account, ":"+ucc) {
				continue
			}
			if p.Debit > 0 {
				y.debits++
				y.debit += p.Debit
			}
			if p.Credit > 0 {
				y.credits++
				y.credit += p.Credit
			}
		}
	}
	return y
}
