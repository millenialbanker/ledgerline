package ops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
)

// ---- sanctions screening: UNSC, UAPA s.51A and WMD Act s.12A ----
//
// The rules:
//
//   - SEBI master circular SEBI/HO/MIRSD/MIRSDSECFATF/P/CIR/2024/78 (6 June
//     2024) on AML/CFT: para 18(vii) no account for a person banned by any
//     enforcement or regulatory agency; para 54 no account for a person on
//     the UN Security Council lists under section 51A of the UAPA; para 55
//     the MHA order of 2 February 2021 (as amended) sets the UAPA procedure;
//     para 56-61 the Ministry of Finance order of 30 January 2023 under
//     section 12A of the WMD Act: keep the Designated List current (59(i)),
//     do not carry out a matching person's transaction and inform the
//     Central Nodal Officer (the Director, FIU-IND) at once (59(ii)), check
//     at onboarding and periodically (59(iii)), copy SEBI (59(iv)), stop
//     the person's transactions where it is beyond doubt (59(v)) and file
//     an STR (59(vi)); paras 62-63 note the UAPA s.35(1) and s.51A orders
//     SEBI circulates; para 64 the UNSC ISIL (Da'esh) & Al-Qaida list and
//     the UNSCR 1718 (DPRK) list; para 65 scan all existing accounts
//     continuously; para 66 keep the lists electronically and check them
//     regularly; para 67 use current technology for name screening; para
//     68 file an STR on designated accounts; paras 69-70 report a
//     resembling account at once to the Central [Designated] Nodal Officer
//     for the UAPA, with copies to the State UAPA Nodal Officer, SEBI and
//     FIU-IND; paras 71-72 FATF public statements.
//   - The MHA UAPA order (2 February 2021, corrigenda of 2023 and 2024)
//     gives the 24 hours: a match is reported "immediately, not later than
//     24 hours from the time of finding out such customer". The SEBI
//     circular itself says "without delay" and "immediately"; the 24-hour
//     clock is the stricter reading and applies to every case here, WMD
//     included.
//   - SEBI Master Circular for Depositories (SEBI/HO/MRD/MRD-PoD-1/P/CIR/
//     2024/168) 2.28 and CDSL's FATF communiqués (DP2026-480, DP2025-739):
//     the DP freezes the demat account of a designated person.
//
// What Ledgerline does:
//
//   - Lists: the UN Security Council's consolidated list (its XML, as the
//     UN publishes it, or a CSV), the MHA's UAPA designated individuals and
//     organisations, the WMD designated list, and the existing debarment
//     lists (SEBI, exchanges) and the exchanges' defaulting clients. Each
//     load keeps the list's version (the UN file's dateGenerated, or the
//     date given) and replaces the list of that type: an entry no longer
//     in the file is marked delisted.
//   - Screening (sanctionsmatch.go) by PAN where the list has one and by
//     name (with aliases, transliteration-tolerant), with the date of birth
//     and nationality as supporting evidence. It covers every person on an
//     account: the client, joint holders, the guardian of a minor, an
//     entity's controlling persons and (a setting, on by default) the
//     nominees. It runs on each new application, on every list load
//     against all clients, on each FATF statement, and on a schedule
//     (every day by default).
//   - A possible match opens a sanctions case with a 24-hour clock. A
//     strong one (the hold threshold) blocks the client at once while
//     Compliance verifies. A new application with an open case cannot be
//     activated.
//   - Compliance confirms a match: trading is blocked, payouts and MF and
//     IPO transactions stop, the demat accounts at the firm's DP are frozen
//     for debits in Ledgerline at once and a freeze request goes to the DP
//     desk (dp.go) for CDSL; the report register lists who must be told
//     (by statute) with the date and reference of each report; an alert is
//     raised on Surveillance & AML for the STR decision.
//   - A false positive is closed by one compliance officer and approved by
//     another (maker-checker), with the reason; the hold is lifted and the
//     same list entry is not raised again for the person unless it changes.
//
// Joint holders and nominees: a joint holder owns and operates the account,
// so a match freezes it like the first holder's. A nominee has no right to
// the account while the holder lives, so a nominee's match does not block
// the account: it is a link to a listed person (para 65), reported and
// considered for an STR. When a nominee claims the account by
// transmission they become a client and are screened as one.

const (
	cSanctionsCase  = "sanctionscase"
	cSanctionsList  = "sanctionslist" // list versions loaded
	cSanctionsRun   = "sanctionsrun"
	cSanctionsCfg   = "sanctionscfg"
	cFATFStatement  = "fatfstatement"
	cFATFEDD        = "fatfedd"
	wSanctions      = "sanctions"
	sanctionsReason = "Sanctions screening"
)

// List types.
const (
	ListUNSC       = "UNSC"
	ListUAPA       = "UAPA"
	ListWMD        = "WMD"
	ListSEBI       = "SEBI"
	ListExchange   = "Exchange"
	ListDefaulters = "Defaulters"
)

// SanctionsListType describes a list type.
type SanctionsListType struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	About string `json:"about"`
	// Sanctions lists are screened by name and open sanctions cases; the
	// others are the debarment lists (PAN matches block, compliance.go).
	Sanctions bool `json:"sanctions"`
}

// SanctionsListTypes are the lists Ledgerline screens against.
var SanctionsListTypes = []SanctionsListType{
	{ListUNSC, "UN Security Council consolidated list", "All UNSC sanctions committees' lists (ISIL/Al-Qaida, Taliban, 1718 DPRK and others), as the UN publishes it (XML) or as a CSV. The DPRK and Iran entries fall under the WMD Act, the others under UAPA s.51A.", true},
	{ListUAPA, "UAPA designated individuals and organisations", "MHA's First and Fourth Schedule designations under UAPA s.35(1), and s.51A orders, as SEBI or the exchanges circulate them (CSV).", true},
	{ListWMD, "WMD Act Designated List", "The Central Nodal Officer's Designated List under s.12A of the WMD Act (CSV).", true},
	{ListSEBI, "SEBI debarment orders", "Entities SEBI has debarred from the securities market (PAN matches block).", false},
	{ListExchange, "Exchange debarments", "Entities an exchange has debarred or expelled (PAN matches block).", false},
	{ListDefaulters, "Exchanges' defaulting clients", "The exchanges' database of clients reported as defaulters by members. A match holds a new account for Compliance to decide; it does not block an existing client.", false},
}

func validListType(t string) bool {
	for _, x := range SanctionsListTypes {
		if x.ID == t {
			return true
		}
	}
	return false
}

func isSanctionsList(t string) bool { return t == ListUNSC || t == ListUAPA || t == ListWMD }

// listTypeOf is the list an entry belongs to (entries loaded before list
// types existed carry only the regulator).
func listTypeOf(d Debarred) string {
	if d.List != "" {
		return d.List
	}
	switch strings.ToUpper(strings.TrimSpace(d.Regulator)) {
	case "UNSC", "UN":
		return ListUNSC
	case "MHA", "UAPA":
		return ListUAPA
	case "WMD", "CNO":
		return ListWMD
	case "NSE", "BSE", "MCX", "MSE", "NCDEX":
		return ListExchange
	}
	return ListSEBI
}

// statuteOf is the law a sanctions match is reported under: the UNSC's
// DPRK and Iran (non-proliferation) regimes and the WMD list under the WMD
// Act s.12A; every other UNSC regime and the UAPA list under UAPA s.51A
// (SEBI's AML/CFT master circular paras 54-70).
func statuteOf(d Debarred) string {
	switch listTypeOf(d) {
	case ListWMD:
		return "WMD"
	case ListUAPA:
		return "UAPA"
	case ListUNSC:
		r := strings.ToUpper(d.Regime)
		for _, k := range []string{"DPRK", "1718", "KOREA", "IRAN", "2231"} {
			if strings.Contains(r, k) {
				return "WMD"
			}
		}
		return "UAPA"
	}
	return ""
}

// ---- settings ----

// SanctionsContact is where reports of one kind go.
type SanctionsContact struct {
	Recipient string `json:"recipient"`
	Address   string `json:"address"`
}

// Report recipients.
const (
	RcptUAPACNO   = "UAPA Central Nodal Officer (MHA)"
	RcptUAPAState = "State UAPA Nodal Officer"
	RcptWMDCNO    = "Central Nodal Officer, WMD Act (Director, FIU-IND)"
	RcptSEBI      = "SEBI"
	RcptExchange  = "Exchanges and depository"
	RcptFIU       = "FIU-IND"
)

// recipientsFor are the reports a match needs, by statute (SEBI AML/CFT
// master circular para 59(ii)-(iv) for the WMD Act; paras 69-70 for UAPA).
// The exchanges and the depository are told as the gap report reads the
// UAPA order for SEBI-regulated entities (the safe reading).
func recipientsFor(statute string) []string {
	if statute == "WMD" {
		return []string{RcptWMDCNO, RcptSEBI, RcptExchange}
	}
	return []string{RcptUAPACNO, RcptUAPAState, RcptSEBI, RcptExchange, RcptFIU}
}

// SanctionsConfig is the firm's screening settings.
type SanctionsConfig struct {
	// ReviewAt: a name score (0-100) at or above it opens a case to verify.
	ReviewAt int `json:"review_at"`
	// HoldAt: at or above it the client is also blocked at once while the
	// case is verified.
	HoldAt int `json:"hold_at"`
	// EveryDays: all clients are screened again at least this often.
	EveryDays int `json:"every_days"`
	// MaxAgeDays: the self-inspection fails a client not screened in this
	// many days.
	MaxAgeDays int `json:"max_age_days"`
	// ScreenNominees: nominees are screened too (a match is a link to
	// report, not a block).
	ScreenNominees bool `json:"screen_nominees"`
	// Contacts: the address each kind of report goes to.
	Contacts  []SanctionsContact `json:"contacts"`
	UpdatedBy string             `json:"updated_by,omitempty"`
	UpdatedAt string             `json:"updated_at,omitempty"`
}

// DefaultSanctionsConfig: the addresses are those SEBI's AML/CFT master
// circular gives (para 59(ii) the Director FIU-IND, 59(iv) and 70 SEBI,
// 69 the MHA's CTCR division). The State UAPA Nodal Officers are listed on
// the MHA's website: the firm fills in its state's.
func DefaultSanctionsConfig() SanctionsConfig {
	return SanctionsConfig{ReviewAt: 82, HoldAt: 94, EveryDays: 1, MaxAgeDays: 7, ScreenNominees: true, Contacts: []SanctionsContact{
		{RcptUAPACNO, "jsctcr-mha@gov.in (Joint Secretary CTCR, MHA, North Block, New Delhi)"},
		{RcptUAPAState, ""},
		{RcptWMDCNO, "dir@fiuindia.gov.in"},
		{RcptSEBI, "sebi_uapa@sebi.gov.in (and by post)"},
		{RcptExchange, ""},
		{RcptFIU, "FINGate (STR) and the Director, FIU-IND"},
	}}
}

func (o *Ops) sanctionsConfig(ctx context.Context, tenant string) SanctionsConfig {
	c, _, _ := docs.GetOr(ctx, o.Docs, tenant, cSanctionsCfg, "main", DefaultSanctionsConfig())
	d := DefaultSanctionsConfig()
	if c.ReviewAt <= 0 {
		c.ReviewAt = d.ReviewAt
	}
	if c.HoldAt <= 0 {
		c.HoldAt = d.HoldAt
	}
	if c.EveryDays <= 0 {
		c.EveryDays = d.EveryDays
	}
	if c.MaxAgeDays <= 0 {
		c.MaxAgeDays = d.MaxAgeDays
	}
	// Every recipient has a line, even after an upgrade added one.
	for _, x := range d.Contacts {
		found := false
		for _, y := range c.Contacts {
			found = found || y.Recipient == x.Recipient
		}
		if !found {
			c.Contacts = append(c.Contacts, x)
		}
	}
	return c
}

// SaveSanctionsConfig sets the thresholds, schedule and addresses
// (Compliance).
func (o *Ops) SaveSanctionsConfig(ctx context.Context, tenant, actor string, c SanctionsConfig) (SanctionsConfig, error) {
	if err := o.require(ctx, tenant, actor, "Setting sanctions screening", RoleCompliance); err != nil {
		return c, err
	}
	switch {
	case c.ReviewAt < 60 || c.ReviewAt > 100:
		return c, invalid("the review threshold is a score from 60 to 100")
	case c.HoldAt < c.ReviewAt || c.HoldAt > 100:
		return c, invalid("the hold threshold is from the review threshold to 100")
	case c.EveryDays < 1 || c.EveryDays > 30:
		return c, invalid("screen all clients at least every 30 days")
	case c.MaxAgeDays < c.EveryDays || c.MaxAgeDays > 90:
		return c, invalid("the self-inspection's limit is from the screening interval to 90 days")
	}
	known := map[string]bool{}
	for _, x := range DefaultSanctionsConfig().Contacts {
		known[x.Recipient] = true
	}
	for i := range c.Contacts {
		if !known[c.Contacts[i].Recipient] {
			return c, invalid("unknown recipient %q", c.Contacts[i].Recipient)
		}
		c.Contacts[i].Address = clip(strings.TrimSpace(c.Contacts[i].Address), 200)
	}
	c.UpdatedBy, c.UpdatedAt = actor, o.now().Format(time.RFC3339)
	if err := docs.Upsert(ctx, o.Docs, tenant, cSanctionsCfg, "main", c, actor); err != nil {
		return c, err
	}
	o.log(ctx, tenant, actor, "sanctions.settings", "", map[string]any{"review_at": c.ReviewAt, "hold_at": c.HoldAt, "every_days": c.EveryDays, "nominees": c.ScreenNominees})
	return o.sanctionsConfig(ctx, tenant), nil
}

// ---- lists ----

// SanctionsListVersion is one load of a list.
type SanctionsListVersion struct {
	ID       string `json:"id"`
	List     string `json:"list"`
	Version  string `json:"version"` // the list's own date or version
	File     string `json:"file"`
	SHA256   string `json:"sha256"`
	Format   string `json:"format"` // UN XML, CSV
	Entries  int    `json:"entries"`
	Added    int    `json:"added"`
	Delisted int    `json:"delisted"`
	LoadedAt string `json:"loaded_at"`
	By       string `json:"by"`
}

// unXML is the UN Security Council consolidated list as the UN publishes
// it (scsanctions.un.org/resources/xml/en/consolidated.xml).
type unXML struct {
	XMLName     xml.Name   `xml:"CONSOLIDATED_LIST"`
	Generated   string     `xml:"dateGenerated,attr"`
	Individuals []unPerson `xml:"INDIVIDUALS>INDIVIDUAL"`
	Entities    []unPerson `xml:"ENTITIES>ENTITY"`
}

type unPerson struct {
	DataID   string `xml:"DATAID"`
	First    string `xml:"FIRST_NAME"`
	Second   string `xml:"SECOND_NAME"`
	Third    string `xml:"THIRD_NAME"`
	Fourth   string `xml:"FOURTH_NAME"`
	ListType string `xml:"UN_LIST_TYPE"`
	Ref      string `xml:"REFERENCE_NUMBER"`
	ListedOn string `xml:"LISTED_ON"`
	Original string `xml:"NAME_ORIGINAL_SCRIPT"`
	Nation   []struct {
		Value []string `xml:"VALUE"`
	} `xml:"NATIONALITY"`
	Aliases []struct {
		Quality string `xml:"QUALITY"`
		Name    string `xml:"ALIAS_NAME"`
	} `xml:"INDIVIDUAL_ALIAS"`
	EntityAliases []struct {
		Quality string `xml:"QUALITY"`
		Name    string `xml:"ALIAS_NAME"`
	} `xml:"ENTITY_ALIAS"`
	DOBs []struct {
		Type string `xml:"TYPE_OF_DATE"`
		Date string `xml:"DATE"`
		Year string `xml:"YEAR"`
		From string `xml:"FROM_YEAR"`
		To   string `xml:"TO_YEAR"`
	} `xml:"INDIVIDUAL_DATE_OF_BIRTH"`
}

// looksXML says whether a file is XML rather than CSV.
func looksXML(b []byte) bool {
	b = bytes.TrimLeft(bytes.TrimPrefix(b, []byte("\xef\xbb\xbf")), " \t\r\n")
	return len(b) > 0 && b[0] == '<'
}

// parseUNList reads the UN consolidated list. An entry's name is its name
// parts joined; aliases of any quality are kept (a "low" quality alias
// still names the person).
func parseUNList(b []byte) (version string, out []Debarred, err error) {
	var x unXML
	if err := xml.Unmarshal(b, &x); err != nil {
		return "", nil, fmt.Errorf("%w: this is not the UN Security Council consolidated list (XML): %v", ErrInvalid, err)
	}
	conv := func(p unPerson, entity bool) Debarred {
		name := strings.Join(strings.Fields(strings.Join([]string{p.First, p.Second, p.Third, p.Fourth}, " ")), " ")
		d := Debarred{Name: name, Regulator: "UNSC", List: ListUNSC, Regime: strings.TrimSpace(p.ListType), Ref: strings.TrimSpace(p.Ref), OrderNo: strings.TrimSpace(p.Ref), OrderDate: unDate(p.ListedOn)}
		if entity {
			d.Kind = "Entity"
		} else {
			d.Kind = "Individual"
		}
		for _, n := range p.Nation {
			for _, v := range n.Value {
				if v = strings.TrimSpace(v); v != "" {
					d.Nationality = append(d.Nationality, v)
				}
			}
		}
		for _, a := range append(p.Aliases, p.EntityAliases...) {
			if a.Name = strings.TrimSpace(a.Name); a.Name != "" && !strings.EqualFold(a.Name, name) {
				d.Aliases = append(d.Aliases, a.Name)
			}
		}
		if s := strings.TrimSpace(p.Original); s != "" {
			d.Aliases = append(d.Aliases, s)
		}
		for _, b := range p.DOBs {
			switch {
			case unDate(b.Date) != "":
				d.DOB = append(d.DOB, unDate(b.Date))
			case strings.TrimSpace(b.Year) != "":
				d.DOB = append(d.DOB, strings.TrimSpace(b.Year))
			case strings.TrimSpace(b.From) != "" && strings.TrimSpace(b.To) != "":
				f, _ := strconv.Atoi(strings.TrimSpace(b.From))
				t, _ := strconv.Atoi(strings.TrimSpace(b.To))
				for y := f; y > 0 && y <= t && y-f < 20; y++ {
					d.DOB = append(d.DOB, strconv.Itoa(y))
				}
			}
		}
		if d.Ref == "" {
			d.Ref = strings.TrimSpace(p.DataID)
		}
		return d
	}
	for _, p := range x.Individuals {
		out = append(out, conv(p, false))
	}
	for _, p := range x.Entities {
		out = append(out, conv(p, true))
	}
	if len(out) == 0 {
		return "", nil, invalid("the UN list has no individuals or entities")
	}
	version = x.Generated
	if len(version) >= 10 {
		version = version[:10]
	}
	return version, out, nil
}

// unDate is the date part of a UN list date (2001-01-25 or 2001-01-25T…).
func unDate(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 10 && validDate(s[:10]) {
		return s[:10]
	}
	return ""
}

// splitList splits a CSV cell of several values (; or |).
func splitMulti(s string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == ';' || r == '|' }) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// parseListCSV reads a list as CSV. Name is required; the other columns
// are optional: PAN, Aliases (; separated), DOB (YYYY-MM-DD or YYYY, ;
// separated), Nationality, Ref (or Reference, OrderNo), Regime, ListedOn
// (or OrderDate), Regulator, Type (Individual or Entity), Till.
func parseListCSV(list string, b []byte) ([]Debarred, error) {
	rows, perrs := parseCSV(b, []string{"Name"})
	if len(perrs) > 0 {
		return nil, invalid("line %d: %s", perrs[0].Line, perrs[0].Msg)
	}
	var out []Debarred
	var bad []string
	for _, r := range rows {
		d := Debarred{Name: strings.Join(strings.Fields(r.get("Name")), " "), PAN: strings.ToUpper(r.get("PAN")), List: list,
			Regime: r.get("Regime"), Ref: firstOf(r.get("Ref"), firstOf(r.get("Reference"), r.get("OrderNo"))),
			OrderDate: firstOf(r.get("ListedOn"), r.get("OrderDate")), Till: r.get("Till"), Kind: r.get("Type"),
			Aliases: splitMulti(r.get("Aliases")), DOB: splitMulti(r.get("DOB")), Nationality: splitMulti(r.get("Nationality"))}
		d.OrderNo = d.Ref
		d.Regulator = strings.ToUpper(firstOf(r.get("Regulator"), map[string]string{ListUNSC: "UNSC", ListUAPA: "MHA", ListWMD: "CNO", ListSEBI: "SEBI", ListExchange: "Exchange", ListDefaulters: "Exchange"}[list]))
		switch {
		case d.Name == "":
			bad = append(bad, fmt.Sprintf("line %d: Name is required", r.line))
		case d.PAN != "" && !rePAN.MatchString(d.PAN):
			bad = append(bad, fmt.Sprintf("line %d: %q is not a valid PAN", r.line, d.PAN))
		case d.OrderDate != "" && !ledger.ValidDate(d.OrderDate):
			bad = append(bad, fmt.Sprintf("line %d: ListedOn must be YYYY-MM-DD", r.line))
		case d.Till != "" && !ledger.ValidDate(d.Till):
			bad = append(bad, fmt.Sprintf("line %d: Till must be YYYY-MM-DD", r.line))
		case d.Kind != "" && d.Kind != "Individual" && d.Kind != "Entity":
			bad = append(bad, fmt.Sprintf("line %d: Type is Individual or Entity", r.line))
		}
		for _, x := range d.DOB {
			if !validDate(x) && !(len(x) == 4 && allDigits(x)) {
				bad = append(bad, fmt.Sprintf("line %d: DOB %q is not YYYY-MM-DD or YYYY", r.line, x))
			}
		}
		out = append(out, d)
	}
	if len(bad) > 0 {
		if len(bad) > 10 {
			bad = append(bad[:10], fmt.Sprintf("and %d more", len(bad)-10))
		}
		return nil, invalid("%s", strings.Join(bad, "; "))
	}
	if len(out) == 0 {
		return nil, invalid("the file has no entries")
	}
	return out, nil
}

// entryID is a list entry's key: the list's reference when it has one.
func entryID(d Debarred) string {
	if d.Ref != "" {
		return "SL-" + d.List + "-" + strings.NewReplacer(" ", "", "/", "-", ".", "-", "|", "-").Replace(strings.ToUpper(d.Ref))
	}
	h := sha256.Sum256([]byte(d.List + "|" + strings.ToUpper(d.Name) + "|" + d.PAN + "|" + strings.Join(d.DOB, ",")))
	return "SL-" + d.List + "-" + hex.EncodeToString(h[:])[:12]
}

// entryPrint changes when an entry's identifying details change, so a
// cleared match is raised again if the list adds an alias or a birth date.
func entryPrint(d Debarred) string {
	h := sha256.Sum256([]byte(strings.Join([]string{d.Name, d.PAN, strings.Join(d.Aliases, ";"), strings.Join(d.DOB, ";"), strings.Join(d.Nationality, ";")}, "|")))
	return hex.EncodeToString(h[:])[:12]
}

// SanctionsLoad is the result of loading a list.
type SanctionsLoad struct {
	Version SanctionsListVersion `json:"version"`
	Run     SanctionsRun         `json:"run"`
}

// LoadSanctionsList loads a list: the UN consolidated list as XML, or any
// list type as CSV. A sanctions list (UNSC, UAPA, WMD) replaces the list
// of its type: an entry missing from the new file is delisted from the
// file's date. A debarment or defaulters list adds to what is loaded. All
// clients are then screened against the lists in force.
func (o *Ops) LoadSanctionsList(ctx context.Context, tenant, actor, list, version, name string, data []byte) (SanctionsLoad, error) {
	var res SanctionsLoad
	if err := o.require(ctx, tenant, actor, "Loading sanctions and debarment lists", RoleCompliance, RoleMaker, RoleChecker); err != nil {
		return res, err
	}
	format := "CSV"
	var entries []Debarred
	var err error
	if looksXML(data) {
		format = "UN XML"
		var v string
		v, entries, err = parseUNList(data)
		if err != nil {
			return res, err
		}
		if list == "" {
			list = ListUNSC
		}
		version = firstOf(strings.TrimSpace(version), v)
		for i := range entries {
			entries[i].List = list
		}
	} else {
		if !validListType(list) {
			return res, invalid("say which list this is: %s", strings.Join(listTypeIDs(), ", "))
		}
		if entries, err = parseListCSV(list, data); err != nil {
			return res, err
		}
	}
	if !validListType(list) {
		return res, invalid("unknown list %q", list)
	}
	today := o.now().Format("2006-01-02")
	version = firstOf(strings.TrimSpace(version), today)
	sum := sha256.Sum256(data)
	ver := SanctionsListVersion{List: list, Version: clip(version, 40), File: clip(name, 120), SHA256: hex.EncodeToString(sum[:]), Format: format, Entries: len(entries), LoadedAt: o.now().Format(time.RFC3339), By: actor}
	ver.ID = list + "-" + o.now().Format("20060102T150405.000")
	existing, err := docs.List[Debarred](ctx, o.Docs, tenant, cDebarred, "")
	if err != nil {
		return res, err
	}
	have := map[string]Debarred{}
	for _, d := range existing {
		have[d.ID] = d
	}
	seen := map[string]bool{}
	for _, d := range entries {
		d.Version = ver.Version
		d.ID = entryID(d)
		if seen[d.ID] {
			continue
		}
		seen[d.ID] = true
		old, ok := have[d.ID]
		if !ok || old.Delisted != "" {
			ver.Added++
		}
		if err := docs.Upsert(ctx, o.Docs, tenant, cDebarred, d.ID, d, actor); err != nil {
			return res, err
		}
	}
	if isSanctionsList(list) {
		for _, d := range existing {
			if listTypeOf(d) != list || seen[d.ID] || d.Delisted != "" || !strings.HasPrefix(d.ID, "SL-") {
				continue
			}
			d.Delisted, d.Till = today, addDays(today, -1)
			if err := docs.Upsert(ctx, o.Docs, tenant, cDebarred, d.ID, d, actor); err != nil {
				return res, err
			}
			ver.Delisted++
		}
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cSanctionsList, ver.ID, ver, actor); err != nil {
		return res, err
	}
	o.log(ctx, tenant, actor, "sanctions.list_loaded", ver.ID, map[string]any{"list": list, "version": ver.Version, "entries": ver.Entries, "added": ver.Added, "delisted": ver.Delisted, "format": format})
	res.Version = ver
	if !isSanctionsList(list) {
		// Debarment lists: PAN matches block and name matches go for
		// review, as before (compliance.go).
		b, rv, err := o.screenDebarred(ctx, tenant, actor, today)
		o.log(ctx, tenant, actor, "debarred.screened", "", map[string]any{"blocked": b, "to_review": rv})
		if err != nil {
			return res, err
		}
	}
	res.Run, err = o.runSanctionsScreening(ctx, tenant, actor, "list load: "+list+" "+ver.Version)
	return res, err
}

func listTypeIDs() []string {
	var out []string
	for _, x := range SanctionsListTypes {
		out = append(out, x.ID)
	}
	return out
}

// latestListVersions is the latest load of each list type.
func (o *Ops) latestListVersions(ctx context.Context, tenant string) map[string]SanctionsListVersion {
	vs, _ := docs.List[SanctionsListVersion](ctx, o.Docs, tenant, cSanctionsList, "")
	out := map[string]SanctionsListVersion{}
	for _, v := range vs {
		if v.LoadedAt > out[v.List].LoadedAt {
			out[v.List] = v
		}
	}
	return out
}

// ---- screening ----

// Roles of the people screened on an account. The first four are holders
// (or act for the holder): a match blocks the account. A nominee's match
// is a link to report.
const (
	RoleHolder     = "Client"
	RoleJoint      = "Joint holder"
	RoleGuardian   = "Guardian"
	RoleController = "Controlling person"
	RoleNominee    = "Nominee"
	RoleApplicant  = "Applicant"
)

func blockingRole(r string) bool { return r != RoleNominee && r != RoleApplicant }

type screenPerson struct {
	UCC, Role, Name, PAN, DOB, Nation string
	Entity                            bool
}

// personsOf lists the people on a client's account to screen.
func personsOf(c Client, bo CDSLBODetail, cfg SanctionsConfig) []screenPerson {
	nat := ""
	if c.FATCA != nil {
		nat = firstOf(c.FATCA.Nationality, c.FATCA.CountryOfBirth)
	}
	ps := []screenPerson{{UCC: c.UCC, Role: RoleHolder, Name: c.Name, PAN: c.PAN, DOB: c.DOB, Nation: nat, Entity: nonIndividual(c.Type)}}
	for _, h := range bo.Holders {
		if h.DiedOn == "" && strings.TrimSpace(h.Name) != "" {
			ps = append(ps, screenPerson{UCC: c.UCC, Role: RoleJoint, Name: h.Name, PAN: h.PAN})
		}
	}
	if g := bo.Guardian; g != nil && strings.TrimSpace(g.Name) != "" {
		ps = append(ps, screenPerson{UCC: c.UCC, Role: RoleGuardian, Name: g.Name, PAN: g.PAN, DOB: g.DOB})
	}
	if c.FATCA != nil {
		for _, p := range c.FATCA.ControllingPersons {
			if strings.TrimSpace(p.Name) != "" {
				ps = append(ps, screenPerson{UCC: c.UCC, Role: RoleController, Name: p.Name, PAN: p.PAN, DOB: p.DOB, Nation: firstOf(p.Nationality, p.CountryOfBirth)})
			}
		}
	}
	if cfg.ScreenNominees {
		for _, n := range c.Nominees {
			if strings.TrimSpace(n.Name) != "" {
				pan := ""
				if strings.EqualFold(n.IDType, "PAN") {
					pan = strings.ToUpper(n.ID)
				}
				ps = append(ps, screenPerson{UCC: c.UCC, Role: RoleNominee, Name: n.Name, PAN: pan, DOB: n.DOB})
			}
		}
		if len(c.Nominees) == 0 && strings.TrimSpace(c.NomineeName) != "" {
			ps = append(ps, screenPerson{UCC: c.UCC, Role: RoleNominee, Name: c.NomineeName})
		}
	}
	return ps
}

// sanIndex holds the entries in force for screening.
type sanIndex struct {
	entries []Debarred
	toks    [][][]string // per entry: name and aliases as words
	byKey   map[byte][]int
	byPAN   map[string][]int
	nats    [][]string // per entry: nationality codes
}

func buildSanIndex(list []Debarred, date string, want func(string) bool) *sanIndex {
	ix := &sanIndex{byKey: map[byte][]int{}, byPAN: map[string][]int{}}
	for _, d := range list {
		if !want(listTypeOf(d)) || d.Delisted != "" || (d.Till != "" && d.Till < date) {
			continue
		}
		i := len(ix.entries)
		ix.entries = append(ix.entries, d)
		var vs [][]string
		keys := map[byte]bool{}
		for _, n := range append([]string{d.Name}, d.Aliases...) {
			if t := sanTokens(n); len(t) > 0 {
				vs = append(vs, t)
				for _, k := range nameKeys(t) {
					keys[k] = true
				}
			}
		}
		ix.toks = append(ix.toks, vs)
		for k := range keys {
			ix.byKey[k] = append(ix.byKey[k], i)
		}
		if d.PAN != "" {
			ix.byPAN[strings.ToUpper(d.PAN)] = append(ix.byPAN[strings.ToUpper(d.PAN)], i)
		}
		var ns []string
		for _, n := range d.Nationality {
			if c := nationCode(n); c != "" {
				ns = append(ns, c)
			}
		}
		ix.nats = append(ix.nats, ns)
	}
	return ix
}

// nationCode is a country's ISO code from its code or English name in any
// word order ("Democratic People's Republic of Korea").
func nationCode(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if c, ok := countryByName(strings.ToUpper(s)); ok {
		return c
	}
	if c, ok := countryByName(s); ok {
		return c
	}
	key := func(x string) string {
		var ws []string
		for _, w := range sanTokens(x) {
			if w != "OF" && w != "THE" && w != "AND" {
				ws = append(ws, w)
			}
		}
		sort.Strings(ws)
		return strings.Join(ws, " ")
	}
	k := key(s)
	for code, name := range Countries {
		if key(name) == k {
			return code
		}
	}
	return ""
}

// sanHit is a person's possible match with one entry.
type sanHit struct {
	entry Debarred
	score int
	why   []string
	byPAN bool
}

// match scores a person against the index; hits at or above the review
// threshold, best first.
func (ix *sanIndex) match(p screenPerson, cfg SanctionsConfig) []sanHit {
	var out []sanHit
	done := map[int]bool{}
	if p.PAN != "" {
		for _, i := range ix.byPAN[strings.ToUpper(p.PAN)] {
			done[i] = true
			out = append(out, sanHit{entry: ix.entries[i], score: 100, why: []string{"PAN matches"}, byPAN: true})
		}
	}
	pt := sanTokens(p.Name)
	if len(pt) == 0 {
		return out
	}
	cand := map[int]bool{}
	for _, k := range nameKeys(pt) {
		for _, i := range ix.byKey[k] {
			cand[i] = true
		}
	}
	idx := make([]int, 0, len(cand))
	for i := range cand {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	pnat := nationCode(p.Nation)
	for _, i := range idx {
		if done[i] {
			continue
		}
		best, which := 0, ""
		for k, v := range ix.toks[i] {
			if s := nameScore(pt, v); s > best {
				best = s
				which = map[bool]string{true: "name", false: "alias"}[k == 0]
			}
		}
		if best < cfg.ReviewAt-15 {
			continue
		}
		d := ix.entries[i]
		score := best
		why := []string{fmt.Sprintf("%s scores %d", which, best)}
		if p.DOB != "" && len(d.DOB) > 0 {
			exact, year, near := false, false, false
			py, _ := strconv.Atoi(p.DOB[:min(4, len(p.DOB))])
			for _, x := range d.DOB {
				ey, _ := strconv.Atoi(x[:min(4, len(x))])
				exact = exact || x == p.DOB
				year = year || (ey != 0 && ey == py)
				near = near || (ey != 0 && py != 0 && ey-py <= 2 && py-ey <= 2)
			}
			switch {
			case exact:
				score += 8
				why = append(why, "date of birth matches")
			case year:
				score += 4
				why = append(why, "year of birth matches")
			case !near:
				score -= 15
				why = append(why, "date of birth differs ("+strings.Join(d.DOB, ", ")+")")
			}
		}
		if pnat != "" && len(ix.nats[i]) > 0 {
			if contains(ix.nats[i], pnat) {
				score += 4
				why = append(why, "nationality matches")
			} else {
				score -= 4
				why = append(why, "nationality differs ("+strings.Join(d.Nationality, ", ")+")")
			}
		}
		if d.Kind != "" && (d.Kind == "Entity") != p.Entity {
			score -= 10
			why = append(why, "one is a person, the other an entity")
		}
		score = max(0, min(100, score))
		if score >= cfg.ReviewAt {
			out = append(out, sanHit{entry: d, score: score, why: why})
		}
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].score > out[b].score })
	return out
}

// SanctionsRun is one screening.
type SanctionsRun struct {
	ID      string            `json:"id"`
	At      string            `json:"at"`
	Trigger string            `json:"trigger"`
	By      string            `json:"by"`
	All     bool              `json:"all"`            // every client
	UCCs    []string          `json:"uccs,omitempty"` // otherwise these
	Lists   map[string]string `json:"lists"`          // list type: version screened against
	Clients int               `json:"clients"`
	Persons int               `json:"persons"`
	Hits    int               `json:"hits"`
	New     int               `json:"new"` // cases opened
}

// RunSanctionsScreening screens every client against the lists in force
// now (Compliance, or Admin).
func (o *Ops) RunSanctionsScreening(ctx context.Context, tenant, actor string) (SanctionsRun, error) {
	if err := o.require(ctx, tenant, actor, "Running sanctions screening", RoleCompliance); err != nil {
		return SanctionsRun{}, err
	}
	return o.runSanctionsScreening(ctx, tenant, actor, "run by "+actor)
}

func (o *Ops) runSanctionsScreening(ctx context.Context, tenant, actor, trigger string) (SanctionsRun, error) {
	cs, err := o.Clients(ctx, tenant)
	if err != nil {
		return SanctionsRun{}, err
	}
	var live []Client
	for _, c := range cs {
		if c.Status != "Closed" {
			live = append(live, c)
		}
	}
	return o.screenClients(ctx, tenant, actor, trigger, live, true)
}

// screenClients screens clients and opens cases for new hits.
func (o *Ops) screenClients(ctx context.Context, tenant, actor, trigger string, cs []Client, all bool) (SanctionsRun, error) {
	cfg := o.sanctionsConfig(ctx, tenant)
	today := o.now().Format("2006-01-02")
	list, err := docs.List[Debarred](ctx, o.Docs, tenant, cDebarred, "")
	if err != nil {
		return SanctionsRun{}, err
	}
	ix := buildSanIndex(list, today, isSanctionsList)
	dix := buildSanIndex(list, today, func(t string) bool { return t == ListDefaulters })
	bos, _ := docs.List[CDSLBODetail](ctx, o.Docs, tenant, cCDSLBODetail, "")
	boBy := map[string]CDSLBODetail{}
	for _, b := range bos {
		boBy[b.UCC] = b
	}
	cases, _ := docs.List[SanctionsCase](ctx, o.Docs, tenant, cSanctionsCase, "")
	known := map[string]SanctionsCase{}
	for _, k := range cases {
		known[k.Key] = k
	}
	run := SanctionsRun{At: o.now().Format(time.RFC3339), Trigger: clip(trigger, 120), By: actor, All: all, Lists: map[string]string{}}
	for t, v := range o.latestListVersions(ctx, tenant) {
		run.Lists[t] = v.Version
	}
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].UCC < cs[j].UCC })
	for _, c := range cs {
		run.Clients++
		if !all {
			run.UCCs = append(run.UCCs, c.UCC)
		}
		for _, p := range personsOf(c, boBy[c.UCC], cfg) {
			run.Persons++
			for _, h := range ix.match(p, cfg) {
				run.Hits++
				if opened, err := o.openSanctionsCase(ctx, tenant, actor, p, h, trigger, cfg, known); err != nil {
					return run, err
				} else if opened {
					run.New++
				}
			}
			// The exchanges' defaulting clients: checked for the account
			// holder of an account not yet active (NSE-N0083), as a case to
			// decide before activation.
			if p.Role == RoleHolder && c.Status != "Active" {
				for _, h := range dix.match(p, cfg) {
					if !h.byPAN && h.score < cfg.HoldAt {
						continue
					}
					run.Hits++
					if opened, err := o.openSanctionsCase(ctx, tenant, actor, p, h, trigger, cfg, known); err != nil {
						return run, err
					} else if opened {
						run.New++
					}
				}
			}
		}
	}
	run.ID = "SR-" + o.now().Format("20060102T150405.000000")
	if err := docs.Upsert(ctx, o.Docs, tenant, cSanctionsRun, run.ID, run, actor); err != nil {
		return run, err
	}
	o.log(ctx, tenant, actor, "sanctions.screened", run.ID, map[string]any{"trigger": run.Trigger, "clients": run.Clients, "persons": run.Persons, "hits": run.Hits, "new": run.New})
	return run, nil
}

// screenNewClient screens a new application (createApplication).
func (o *Ops) screenNewClient(ctx context.Context, tenant, actor string, c Client) {
	_, _ = o.screenClients(ctx, tenant, actor, "new application", []Client{c}, false)
}

// screenApplicant records a refused applicant whose PAN is on a sanctions
// list as a case, so the attempt is reported like a client's (SEBI AML/CFT
// master circular para 59(ii): a person seeking a transaction).
func (o *Ops) screenApplicant(ctx context.Context, tenant, actor, name, pan, dob string) {
	cfg := o.sanctionsConfig(ctx, tenant)
	list, err := docs.List[Debarred](ctx, o.Docs, tenant, cDebarred, "")
	if err != nil {
		return
	}
	ix := buildSanIndex(list, o.now().Format("2006-01-02"), isSanctionsList)
	cases, _ := docs.List[SanctionsCase](ctx, o.Docs, tenant, cSanctionsCase, "")
	known := map[string]SanctionsCase{}
	for _, k := range cases {
		known[k.Key] = k
	}
	p := screenPerson{Role: RoleApplicant, Name: name, PAN: pan, DOB: dob}
	for _, h := range ix.match(p, cfg) {
		if h.byPAN {
			_, _ = o.openSanctionsCase(ctx, tenant, actor, p, h, "account opening refused", cfg, known)
		}
	}
}

// ---- cases ----

// Case statuses.
const (
	SCToVerify   = "To verify"
	SCClearing   = "Clearing proposed"
	SCConfirmed  = "Confirmed"
	SCReported   = "Reported"
	SCFalse      = "False positive"
	SCUnfrozen   = "Unfrozen"
	sanctionsDue = 24 * time.Hour
)

// SanctionsReport is one line of a case's report register.
type SanctionsReport struct {
	Recipient  string `json:"recipient"`
	Address    string `json:"address,omitempty"`
	ReportedOn string `json:"reported_on,omitempty"`
	Ref        string `json:"ref,omitempty"`
	By         string `json:"by,omitempty"`
	At         string `json:"at,omitempty"`
}

// SanctionsCase is a possible or confirmed match of a person on an
// account (or an applicant) with a list entry.
type SanctionsCase struct {
	ID      string   `json:"id"`
	Key     string   `json:"key"`
	UCC     string   `json:"ucc,omitempty"`
	Client  string   `json:"client,omitempty"` // the account's name
	Person  string   `json:"person"`
	Role    string   `json:"role"`
	PAN     string   `json:"pan,omitempty"` // masked
	Entry   string   `json:"entry"`
	Print   string   `json:"print"`
	List    string   `json:"list"`
	Regime  string   `json:"regime,omitempty"`
	Ref     string   `json:"ref,omitempty"`
	Listed  string   `json:"listed_name"`
	Statute string   `json:"statute,omitempty"` // UAPA, WMD ("" for a defaulters match)
	Score   int      `json:"score"`
	Why     []string `json:"why"`
	Trigger string   `json:"trigger"`
	Status  string   `json:"status"`
	Found   string   `json:"found"`
	DueBy   string   `json:"due_by"` // 24 hours from Found
	// Held: the client is blocked by this case; WasBlocked and WasReason
	// are the client's block before it, put back if the case is cleared.
	Held        bool              `json:"held"`
	WasBlocked  bool              `json:"was_blocked,omitempty"`
	WasReason   string            `json:"was_reason,omitempty"`
	ConfirmedBy string            `json:"confirmed_by,omitempty"`
	ConfirmedAt string            `json:"confirmed_at,omitempty"`
	Freezes     []string          `json:"freezes,omitempty"` // DP freeze requests raised
	Alert       string            `json:"alert,omitempty"`   // for the STR decision
	Reports     []SanctionsReport `json:"reports,omitempty"`
	ClearBy     string            `json:"clear_by,omitempty"`
	ClearReason string            `json:"clear_reason,omitempty"`
	ClearedBy   string            `json:"cleared_by,omitempty"`
	ClosedAt    string            `json:"closed_at,omitempty"`
	// An application to unfreeze (inadvertent freezing, or delisting) is
	// forwarded to the Central Nodal Officer within two working days (MHA
	// UAPA order, para 11A as amended).
	UnfreezeAppOn  string   `json:"unfreeze_app_on,omitempty"`
	UnfreezeBy     string   `json:"unfreeze_forward_by,omitempty"`
	UnfreezeFwdOn  string   `json:"unfreeze_forwarded_on,omitempty"`
	UnfreezeFwdRef string   `json:"unfreeze_forwarded_ref,omitempty"`
	UnfreezeOrder  string   `json:"unfreeze_order,omitempty"`
	Notes          []string `json:"notes"`
}

// open says whether the case still needs work.
func (k SanctionsCase) open() bool {
	return k.Status == SCToVerify || k.Status == SCClearing || k.Status == SCConfirmed
}

// barsAccount says whether the case stops an account being activated.
func (k SanctionsCase) barsAccount() bool {
	return k.Status != SCFalse && k.Status != SCUnfrozen && (blockingRole(k.Role) || k.Status != SCReported)
}

// reportsDone says whether every report in the register is recorded.
func (k SanctionsCase) reportsDone() bool {
	for _, r := range k.Reports {
		if r.ReportedOn == "" {
			return false
		}
	}
	return true
}

func caseKey(p screenPerson, entry string) string {
	who := p.UCC
	if who == "" {
		who = "APP-" + strings.ToUpper(p.PAN)
	}
	return who + "|" + p.Role + "|" + strings.Join(sanTokens(p.Name), " ") + "|" + entry
}

// openSanctionsCase opens a case for a hit unless one is open for the same
// person and entry, or one was cleared for the entry as it now reads.
func (o *Ops) openSanctionsCase(ctx context.Context, tenant, actor string, p screenPerson, h sanHit, trigger string, cfg SanctionsConfig, known map[string]SanctionsCase) (bool, error) {
	key := caseKey(p, h.entry.ID)
	if k, ok := known[key]; ok && (k.Status != SCFalse || k.Print == entryPrint(h.entry)) {
		return false, nil
	}
	n, err := o.next(ctx, tenant, "sanctionscase", actor)
	if err != nil {
		return false, err
	}
	now := o.now()
	k := SanctionsCase{ID: fmt.Sprintf("SC-%05d", n), Key: key, UCC: p.UCC, Person: p.Name, Role: p.Role, PAN: MaskPAN(p.PAN), Entry: h.entry.ID, Print: entryPrint(h.entry),
		List: listTypeOf(h.entry), Regime: h.entry.Regime, Ref: firstOf(h.entry.Ref, h.entry.OrderNo), Listed: h.entry.Name, Statute: statuteOf(h.entry),
		Score: h.score, Why: h.why, Trigger: trigger, Status: SCToVerify, Found: now.Format(time.RFC3339), DueBy: now.Add(sanctionsDue).Format(time.RFC3339), Notes: []string{}}
	if k.UCC != "" {
		if c, err := o.Client(ctx, tenant, k.UCC); err == nil {
			k.Client = c.Name
		}
	}
	if _, err := docs.Put(ctx, o.Docs, tenant, cSanctionsCase, k.ID, k, 0, actor); err != nil {
		return false, err
	}
	known[key] = k
	// A strong match on a sanctions list blocks the account while it is
	// verified (the safe reading of "without delay").
	if isSanctionsList(k.List) && blockingRole(k.Role) && k.UCC != "" && h.score >= cfg.HoldAt {
		if err := o.sanctionsHold(ctx, tenant, actor, &k, false); err != nil {
			return true, err
		}
		_ = docs.Upsert(ctx, o.Docs, tenant, cSanctionsCase, k.ID, k, actor)
	}
	o.log(ctx, tenant, actor, "sanctions.case_opened", k.ID, map[string]any{"ucc": k.UCC, "role": k.Role, "list": k.List, "ref": k.Ref, "score": k.Score, "held": k.Held})
	what := "a possible match"
	if k.List == ListDefaulters {
		what = "on the exchanges' defaulting clients list"
	}
	held := ""
	if k.Held {
		held = " The client is blocked until the case is decided."
	}
	o.raiseWork(ctx, tenant, wSanctions, k.ID, "Verify sanctions match "+k.ID+" by "+fmtTimeIST(k.DueBy),
		fmt.Sprintf("%s (%s%s) is %s: %s %s (%s), score %d (%s).%s Check the identity documents against the list entry and confirm the match or propose to clear it, within 24 hours of %s, on KYC & PMLA → Sanctions.",
			k.Person, k.Role, map[bool]string{true: " of " + k.UCC, false: ""}[k.UCC != ""], what, k.List, k.Ref, k.Listed, k.Score, strings.Join(k.Why, "; "), held, fmtTimeIST(k.Found)),
		k.UCC, RoleCompliance, "kyc", now.Format("2006-01-02"))
	return true, nil
}

// fmtTimeIST is an RFC 3339 time as "2 Nov 14:05".
func fmtTimeIST(s string) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	return t.In(ist).Format("2 Jan 15:04")
}

// SanctionsFlag marks a client held by a sanctions case.
type SanctionsFlag struct {
	Case      string `json:"case"`
	Since     string `json:"since"`
	Confirmed bool   `json:"confirmed,omitempty"`
}

// sanctionsHold blocks the client of a case (and marks it, so payouts,
// demat debits and other transactions stop).
func (o *Ops) sanctionsHold(ctx context.Context, tenant, actor string, k *SanctionsCase, confirmed bool) error {
	if k.UCC == "" || !blockingRole(k.Role) {
		return nil
	}
	// Held already by another case: that case knows the client's block
	// before any sanctions hold, which is what a release puts back.
	var prior *SanctionsCase
	if c, err := o.Client(ctx, tenant, k.UCC); err == nil && c.Sanctions != nil && c.Sanctions.Case != k.ID {
		if p, _, err := docs.Get[SanctionsCase](ctx, o.Docs, tenant, cSanctionsCase, c.Sanctions.Case); err == nil {
			prior = &p
		}
	}
	_, err := docs.Update(ctx, o.Docs, tenant, cClient, k.UCC, actor, func(c *Client) error {
		switch {
		case k.Held:
		case prior != nil:
			k.WasBlocked, k.WasReason = prior.WasBlocked, prior.WasReason
		case c.Sanctions == nil:
			k.WasBlocked, k.WasReason = c.Blocked, c.BlockedReason
		}
		c.Sanctions = &SanctionsFlag{Case: k.ID, Since: o.now().Format(time.RFC3339), Confirmed: confirmed || (c.Sanctions != nil && c.Sanctions.Confirmed)}
		c.Blocked = true
		state := "possible match, to verify"
		if c.Sanctions.Confirmed {
			state = "designated person, transactions stopped"
		}
		c.BlockedReason = sanctionsReason + ": " + k.ID + " (" + state + ")"
		return nil
	})
	if err != nil {
		return err
	}
	k.Held = true
	o.log(ctx, tenant, actor, "sanctions.hold", k.UCC, map[string]any{"case": k.ID, "confirmed": confirmed})
	return nil
}

// sanctionsRelease lifts a case's hold, unless another case still holds
// the client.
func (o *Ops) sanctionsRelease(ctx context.Context, tenant, actor string, k *SanctionsCase) error {
	if !k.Held || k.UCC == "" {
		return nil
	}
	cases, _ := docs.List[SanctionsCase](ctx, o.Docs, tenant, cSanctionsCase, "")
	var other *SanctionsCase
	for i := range cases {
		x := cases[i]
		if x.ID != k.ID && x.UCC == k.UCC && x.Held && (x.open() || x.Status == SCReported) {
			other = &cases[i]
		}
	}
	_, err := docs.Update(ctx, o.Docs, tenant, cClient, k.UCC, actor, func(c *Client) error {
		if other != nil {
			c.Sanctions = &SanctionsFlag{Case: other.ID, Since: o.now().Format(time.RFC3339), Confirmed: other.Status == SCConfirmed || other.Status == SCReported}
			return nil
		}
		c.Sanctions = nil
		c.Blocked, c.BlockedReason = k.WasBlocked, k.WasReason
		return nil
	})
	if err != nil {
		return err
	}
	k.Held = false
	o.log(ctx, tenant, actor, "sanctions.release", k.UCC, map[string]any{"case": k.ID})
	return nil
}

// sanctionsStop refuses a transaction for a client held by a sanctions
// case (SEBI AML/CFT master circular para 59(ii): do not carry out the
// transaction).
func sanctionsStop(c Client) error {
	if c.Sanctions == nil {
		return nil
	}
	return fmt.Errorf("%w: %s is held by sanctions case %s; no transaction until Compliance decides it", ErrForbidden, c.UCC, c.Sanctions.Case)
}

// sanctionsGate stops a client with an open or confirmed case from being
// activated (SEBI AML/CFT master circular paras 54, 65: no account for a
// listed person).
func (o *Ops) sanctionsGate(ctx context.Context, tenant, ucc string) error {
	cases, _ := docs.List[SanctionsCase](ctx, o.Docs, tenant, cSanctionsCase, "")
	for _, k := range cases {
		if k.UCC == ucc && k.barsAccount() {
			if k.List == ListDefaulters {
				return fmt.Errorf("%w: %s is on the exchanges' defaulting clients list (case %s): Compliance must decide it before the account is activated", ErrState, ucc, k.ID)
			}
			return fmt.Errorf("%w: %s has sanctions case %s (%s): the account cannot be activated until Compliance clears it", ErrState, ucc, k.ID, strings.ToLower(k.Status))
		}
	}
	return nil
}

// SanctionsAction is a decision on a case.
type SanctionsAction struct {
	Action    string `json:"action"` // confirm, clear, approve_clear, reject_clear, report, close, unfreeze_app, unfreeze_forwarded, unfreeze
	Note      string `json:"note"`
	Recipient string `json:"recipient"`
	Date      string `json:"date"`
	Ref       string `json:"ref"`
}

// ActOnSanctionsCase moves a case on (Compliance; Admin always may).
func (o *Ops) ActOnSanctionsCase(ctx context.Context, tenant, actor, id string, a SanctionsAction) (SanctionsCase, error) {
	if err := o.require(ctx, tenant, actor, "Deciding sanctions cases", RoleCompliance); err != nil {
		return SanctionsCase{}, err
	}
	k, _, err := docs.Get[SanctionsCase](ctx, o.Docs, tenant, cSanctionsCase, id)
	if err != nil {
		return k, err
	}
	note := clip(strings.TrimSpace(a.Note), 500)
	now := o.now()
	today := now.Format("2006-01-02")
	set, _ := o.Settings(ctx, tenant)
	need := func(st ...string) error {
		if !contains(st, k.Status) {
			return fmt.Errorf("%w: %s is %s", ErrState, k.ID, strings.ToLower(k.Status))
		}
		return nil
	}
	switch a.Action {
	case "confirm":
		if err := need(SCToVerify); err != nil {
			return k, err
		}
		if note == "" {
			return k, invalid("record what was checked: the documents compared with the list entry")
		}
		k.Status, k.ConfirmedBy, k.ConfirmedAt = SCConfirmed, actor, now.Format(time.RFC3339)
		if k.List != ListDefaulters {
			if err := o.confirmSanctions(ctx, tenant, actor, &k, set); err != nil {
				return k, err
			}
		}
	case "clear":
		if err := need(SCToVerify); err != nil {
			return k, err
		}
		if note == "" {
			return k, invalid("give the reason it is not the listed person (for example a different date of birth or father's name on the documents)")
		}
		k.Status, k.ClearBy, k.ClearReason = SCClearing, actor, note
	case "approve_clear":
		if err := need(SCClearing); err != nil {
			return k, err
		}
		if k.ClearBy == actor {
			return k, fmt.Errorf("%w: a different compliance officer approves clearing a match (maker-checker)", ErrForbidden)
		}
		if err := o.sanctionsRelease(ctx, tenant, actor, &k); err != nil {
			return k, err
		}
		k.Status, k.ClearedBy, k.ClosedAt = SCFalse, actor, now.Format(time.RFC3339)
		o.closeWork(ctx, tenant, actor, wSanctions, k.ID, "Cleared as a false positive: "+k.ClearReason)
	case "reject_clear":
		if err := need(SCClearing); err != nil {
			return k, err
		}
		if note == "" {
			return k, invalid("say why the match is not cleared")
		}
		k.Status, k.ClearBy, k.ClearReason = SCToVerify, "", ""
	case "report":
		if err := need(SCConfirmed, SCReported); err != nil {
			return k, err
		}
		i := -1
		for j, r := range k.Reports {
			if r.Recipient == a.Recipient {
				i = j
			}
		}
		if i < 0 {
			return k, invalid("%q is not in this case's report register", a.Recipient)
		}
		if !validDate(a.Date) || a.Date > today {
			return k, invalid("give the date it was reported, on or before today")
		}
		if strings.TrimSpace(a.Ref) == "" {
			return k, invalid("give the reference: the email's date and subject, the letter number or the acknowledgement")
		}
		k.Reports[i].ReportedOn, k.Reports[i].Ref, k.Reports[i].By, k.Reports[i].At = a.Date, clip(strings.TrimSpace(a.Ref), 200), actor, now.Format(time.RFC3339)
		if k.reportsDone() {
			o.closeWork(ctx, tenant, actor, wSanctions, k.ID+"-report", "Every report recorded")
		}
	case "close":
		if err := need(SCConfirmed); err != nil {
			return k, err
		}
		if !k.reportsDone() {
			return k, fmt.Errorf("%w: record every report in the register first", ErrState)
		}
		if k.Alert != "" {
			if al, _, err := docs.Get[Alert](ctx, o.Docs, tenant, cAlert, k.Alert); err == nil && (al.Status == "Open" || al.Status == "Under review") {
				return k, fmt.Errorf("%w: decide the STR on alert %s first (Surveillance & AML)", ErrState, k.Alert)
			}
		}
		k.Status, k.ClosedAt = SCReported, now.Format(time.RFC3339)
		o.closeWork(ctx, tenant, actor, wSanctions, k.ID, "Reported")
	case "unfreeze_app":
		if err := need(SCConfirmed, SCReported); err != nil {
			return k, err
		}
		if !validDate(a.Date) || a.Date > today {
			return k, invalid("give the date the application was received")
		}
		due := a.Date
		for i := 0; i < 2; i++ {
			due = set.NextBusinessDay(due)
		}
		k.UnfreezeAppOn, k.UnfreezeBy = a.Date, due
		o.raiseWork(ctx, tenant, wSanctions, k.ID+"-unfreeze-"+a.Date, "Forward the unfreezing application on "+k.ID+" by "+fmtDay(due),
			fmt.Sprintf("An application to unfreeze %s's assets (inadvertent freezing or delisting) was received on %s. Forward it, with full details of the frozen funds and securities, to the Central Nodal Officer within two working days (by %s), and record it on the case.", firstOf(k.UCC, k.Person), fmtDay(a.Date), fmtDay(due)),
			k.UCC, RoleCompliance, "kyc", today)
	case "unfreeze_forwarded":
		if k.UnfreezeAppOn == "" {
			return k, fmt.Errorf("%w: record the unfreezing application first", ErrState)
		}
		if !validDate(a.Date) || a.Date > today || a.Date < k.UnfreezeAppOn || strings.TrimSpace(a.Ref) == "" {
			return k, invalid("give the date it was forwarded (on or after it was received) and the reference")
		}
		k.UnfreezeFwdOn, k.UnfreezeFwdRef = a.Date, clip(strings.TrimSpace(a.Ref), 200)
		o.closeWork(ctx, tenant, actor, wSanctions, k.ID+"-unfreeze-"+k.UnfreezeAppOn, "Forwarded "+a.Date)
	case "unfreeze":
		if err := need(SCConfirmed, SCReported); err != nil {
			return k, err
		}
		if strings.TrimSpace(a.Ref) == "" || note == "" {
			return k, invalid("give the unfreezing order (its number and date) and what it says")
		}
		k.UnfreezeOrder = clip(strings.TrimSpace(a.Ref), 200)
		if err := o.sanctionsRelease(ctx, tenant, actor, &k); err != nil {
			return k, err
		}
		if c, err := o.Client(ctx, tenant, k.UCC); err == nil {
			for _, d := range demats(o.withDemats(ctx, tenant, c)) {
				if b := o.boFor(ctx, tenant, c, d.ID); d.OwnDP && b.Status == "Frozen" {
					if r, err := o.RaiseDPRequest(ctx, tenant, actor, DPRequest{Kind: "Unfreeze", UCC: c.UCC, BOID: b.BOID, Detail: map[string]string{"reason": "Unfreezing order " + k.UnfreezeOrder + " (sanctions case " + k.ID + ")"}}); err == nil {
						k.Freezes = append(k.Freezes, r.ID)
					}
				}
			}
		}
		k.Status, k.ClosedAt = SCUnfrozen, now.Format(time.RFC3339)
	default:
		return k, invalid("unknown action %q", a.Action)
	}
	if note != "" {
		k.Notes = append(k.Notes, actor+" ("+a.Action+"): "+note)
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cSanctionsCase, k.ID, k, actor); err != nil {
		return k, err
	}
	o.log(ctx, tenant, actor, "sanctions.case_"+a.Action, k.ID, map[string]any{"ucc": k.UCC, "status": k.Status, "recipient": a.Recipient})
	return k, nil
}

// confirmSanctions applies a confirmed match: the hold, the demat debit
// freeze, the report register, the STR alert and the calendar event.
func (o *Ops) confirmSanctions(ctx context.Context, tenant, actor string, k *SanctionsCase, set Settings) error {
	if blockingRole(k.Role) && k.UCC != "" {
		if err := o.sanctionsHold(ctx, tenant, actor, k, true); err != nil {
			return err
		}
		// The demat accounts at the firm's DP: frozen for debits in
		// Ledgerline at once (the client's Sanctions flag, demats.go), and
		// a freeze request for the DP desk to approve and apply at CDSL.
		if c, err := o.Client(ctx, tenant, k.UCC); err == nil {
			law := map[string]string{"UAPA": "UAPA s.51A", "WMD": "WMD Act s.12A"}[k.Statute]
			for _, d := range demats(o.withDemats(ctx, tenant, c)) {
				if !d.OwnDP {
					continue // another DP's account: that DP freezes it on the order
				}
				b := o.boFor(ctx, tenant, c, d.ID)
				if b.Status == "Closed" || b.Status == "Not opened" {
					continue
				}
				r, err := o.RaiseDPRequest(ctx, tenant, actor, DPRequest{Kind: "Freeze", UCC: c.UCC, BOID: b.BOID, Detail: map[string]string{"type": "Debit",
					"reason": fmt.Sprintf("Designated person (%s %s), sanctions case %s: freeze under %s", k.List, k.Ref, k.ID, firstOf(law, "the order"))}})
				if err != nil {
					return fmt.Errorf("raising the freeze request for %s: %w", b.BOID, err)
				}
				k.Freezes = append(k.Freezes, r.ID)
				o.raiseWork(ctx, tenant, wSanctions, k.ID+"-freeze-"+r.ID, "Approve demat freeze "+r.ID+" now (sanctions)",
					fmt.Sprintf("Sanctions case %s is confirmed. Approve freeze request %s for BO %s on DP requests and freeze the account for debits in CDSL's system without delay.", k.ID, r.ID, b.BOID), c.UCC, RoleChecker, "dp", "")
			}
		}
	}
	cfg := o.sanctionsConfig(ctx, tenant)
	addr := map[string]string{}
	for _, c := range cfg.Contacts {
		addr[c.Recipient] = c.Address
	}
	k.Reports = nil
	for _, r := range recipientsFor(k.Statute) {
		k.Reports = append(k.Reports, SanctionsReport{Recipient: r, Address: addr[r]})
	}
	// The STR (paras 59(vi), 68): decided on Surveillance & AML like any
	// other alert. No list detail in the alert's text beyond the case.
	dedupe := "SANC:" + k.ID
	o.raiseAlert(ctx, tenant, actor, Alert{Source: "Internal", Type: "Sanctions match", UCC: k.UCC, Severity: "High",
		Detail: fmt.Sprintf("Confirmed match with the %s list (case %s): file an STR covering all transactions in the account (SEBI AML/CFT master circular paras 59(vi), 68)", k.List, k.ID)}, dedupe)
	k.Alert = "A-" + strings.NewReplacer(":", "-").Replace(dedupe)
	var rs []string
	for _, r := range k.Reports {
		rs = append(rs, r.Recipient+map[bool]string{true: " (" + r.Address + ")", false: ""}[r.Address != ""])
	}
	o.raiseWork(ctx, tenant, wSanctions, k.ID+"-report", "Report sanctions match "+k.ID+" by "+fmtTimeIST(k.DueBy),
		fmt.Sprintf("Confirmed: %s (%s) matches %s %s under the %s. Report with full particulars of the funds, securities and services held, not later than 24 hours from %s: %s. Record each report's date and reference on the case; decide the STR on alert %s.",
			k.Person, k.Role, k.List, k.Ref, firstOf(map[string]string{"UAPA": "UAPA s.51A order", "WMD": "WMD Act s.12A order"}[k.Statute], "order"), fmtTimeIST(k.Found), strings.Join(rs, "; "), k.Alert),
		k.UCC, RoleCompliance, "kyc", "")
	// The compliance calendar's event, so the 24 hours show there too.
	found, _ := time.Parse(time.RFC3339, k.Found)
	f := found.In(ist)
	_, _ = o.RecordDutyEvent(ctx, tenant, SystemActor, o.now().Format("2006-01-02"), "CC-T-SANCTIONS-MATCH", DutyEventReq{Date: f.Format("2006-01-02"), Time: f.Format("15:04"), What: "Sanctions case " + k.ID + " confirmed (" + k.List + " " + k.Ref + ")"})
	return nil
}

// ---- FATF public statements ----

// FATFStatement is a FATF public statement the firm acted on.
type FATFStatement struct {
	ID            string   `json:"id"`
	Plenary       string   `json:"plenary"` // e.g. October 2026
	Date          string   `json:"date"`    // the communique's date
	Ref           string   `json:"ref"`     // SEBI / exchange / CDSL reference
	CallForAction []string `json:"call_for_action"`
	Monitoring    []string `json:"monitoring"`
	By            string   `json:"by"`
	At            string   `json:"at"`
	Clients       int      `json:"clients"` // screened
	Linked        int      `json:"linked"`  // with a link to a listed jurisdiction
}

// FATFEDD is the enhanced due diligence on one client for a statement.
type FATFEDD struct {
	ID        string   `json:"id"` // statement|UCC
	Statement string   `json:"statement"`
	UCC       string   `json:"ucc"`
	Name      string   `json:"name"`
	Level     string   `json:"level"` // Call for action, Increased monitoring
	Links     []string `json:"links"`
	Status    string   `json:"status"` // To do, Done
	Due       string   `json:"due"`
	Note      string   `json:"note,omitempty"`
	HighRisk  bool     `json:"high_risk,omitempty"`
	By        string   `json:"by,omitempty"`
	At        string   `json:"at,omitempty"`
}

// fatfEDDDays: the firm's own time to record the EDD on a linked client
// (no circular sets one).
const fatfEDDDays = 30

// countryLinks are a client's links to countries: nationality, birth, tax
// residence, and those of its controlling persons.
func countryLinks(c Client) map[string][]string {
	out := map[string][]string{}
	add := func(code, why string) {
		if code = strings.ToUpper(strings.TrimSpace(code)); code != "" && code != "IN" {
			out[code] = append(out[code], why)
		}
	}
	if f := c.FATCA; f != nil {
		add(f.Nationality, "nationality")
		add(f.CountryOfBirth, "country of birth")
		for _, r := range f.Residences {
			add(r.Country, "tax residence")
		}
		for _, p := range f.ControllingPersons {
			add(p.Nationality, "controlling person "+p.Name+": nationality")
			add(p.CountryOfBirth, "controlling person "+p.Name+": country of birth")
			for _, r := range p.Residences {
				add(r.Country, "controlling person "+p.Name+": tax residence")
			}
		}
	}
	return out
}

// RecordFATFStatement records a FATF public statement: every client is
// checked for links to the listed jurisdictions (an EDD item for each one
// linked), screened again against the sanctions lists, and the compliance
// calendar's FATF duty gets its event.
func (o *Ops) RecordFATFStatement(ctx context.Context, tenant, actor string, st FATFStatement) (FATFStatement, error) {
	if err := o.require(ctx, tenant, actor, "Recording FATF statements", RoleCompliance); err != nil {
		return st, err
	}
	st.Plenary, st.Ref = clip(strings.TrimSpace(st.Plenary), 60), clip(strings.TrimSpace(st.Ref), 120)
	if st.Plenary == "" || !validDate(st.Date) || st.Date > o.now().Format("2006-01-02") {
		return st, invalid("give the plenary (for example October 2026) and the statement's date")
	}
	norm := func(xs []string) ([]string, error) {
		var out []string
		for _, x := range xs {
			if x = strings.TrimSpace(x); x == "" {
				continue
			}
			c := nationCode(x)
			if c == "" {
				return nil, invalid("%q is not a country Ledgerline knows (give the ISO code)", x)
			}
			if !contains(out, c) {
				out = append(out, c)
			}
		}
		return out, nil
	}
	var err error
	if st.CallForAction, err = norm(st.CallForAction); err != nil {
		return st, err
	}
	if st.Monitoring, err = norm(st.Monitoring); err != nil {
		return st, err
	}
	if len(st.CallForAction)+len(st.Monitoring) == 0 {
		return st, invalid("list the jurisdictions under call for action and under increased monitoring")
	}
	st.ID = "FATF-" + st.Date
	st.By, st.At = actor, o.now().Format(time.RFC3339)
	cs, err := o.Clients(ctx, tenant)
	if err != nil {
		return st, err
	}
	due := addDays(o.now().Format("2006-01-02"), fatfEDDDays)
	for _, c := range cs {
		if c.Status == "Closed" {
			continue
		}
		st.Clients++
		links := countryLinks(c)
		level := ""
		var why []string
		for _, code := range st.CallForAction {
			if ws, ok := links[code]; ok {
				level = "Call for action"
				why = append(why, Countries[code]+": "+strings.Join(ws, ", "))
			}
		}
		for _, code := range st.Monitoring {
			if ws, ok := links[code]; ok {
				if level == "" {
					level = "Increased monitoring"
				}
				why = append(why, Countries[code]+": "+strings.Join(ws, ", "))
			}
		}
		if level == "" {
			continue
		}
		st.Linked++
		e := FATFEDD{ID: st.ID + "|" + c.UCC, Statement: st.ID, UCC: c.UCC, Name: c.Name, Level: level, Links: why, Status: "To do", Due: due}
		if old, _, err := docs.Get[FATFEDD](ctx, o.Docs, tenant, cFATFEDD, e.ID); err == nil && old.Status == "Done" {
			continue
		}
		if err := docs.Upsert(ctx, o.Docs, tenant, cFATFEDD, e.ID, e, actor); err != nil {
			return st, err
		}
		o.raiseWork(ctx, tenant, wSanctions, "edd-"+e.ID, "EDD for "+c.UCC+" ("+level+", FATF "+st.Plenary+")",
			fmt.Sprintf("%s has links to a jurisdiction in FATF's %s statement: %s. Apply enhanced due diligence proportionate to the risk (SEBI AML/CFT master circular paras 18(iii)(f), 42, 71-72): review the source of funds and the activity, and record it on KYC & PMLA → Sanctions by %s. A client linked to a call-for-action jurisdiction is a client of special category.", c.Name, st.Plenary, strings.Join(why, "; "), fmtDay(due)),
			c.UCC, RoleCompliance, "kyc", "")
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cFATFStatement, st.ID, st, actor); err != nil {
		return st, err
	}
	o.log(ctx, tenant, actor, "sanctions.fatf", st.ID, map[string]any{"plenary": st.Plenary, "linked": st.Linked, "clients": st.Clients})
	// The re-screen the statement calls for.
	if _, err := o.runSanctionsScreening(ctx, tenant, actor, "FATF statement "+st.Plenary); err != nil {
		return st, err
	}
	for _, id := range []string{"CC-D-FATF", "CC-T-FATF"} {
		_, _ = o.RecordDutyEvent(ctx, tenant, SystemActor, o.now().Format("2006-01-02"), id, DutyEventReq{Date: min(st.Date, o.now().Format("2006-01-02")), What: "FATF statement " + st.Plenary + " recorded: " + strconv.Itoa(st.Linked) + " clients linked"})
	}
	return st, nil
}

// RecordFATFEDD records the enhanced due diligence done on a client; with
// highRisk the client is also made High risk with the special category
// "High-risk jurisdiction".
func (o *Ops) RecordFATFEDD(ctx context.Context, tenant, actor, id, note string, highRisk bool) (FATFEDD, error) {
	if err := o.require(ctx, tenant, actor, "Recording enhanced due diligence", RoleCompliance); err != nil {
		return FATFEDD{}, err
	}
	note = clip(strings.TrimSpace(note), 500)
	if note == "" {
		return FATFEDD{}, invalid("record what was reviewed and what it showed")
	}
	e, err := docs.Update(ctx, o.Docs, tenant, cFATFEDD, id, actor, func(x *FATFEDD) error {
		if x.Status == "Done" {
			return fmt.Errorf("%w: the EDD is already recorded", ErrState)
		}
		x.Status, x.Note, x.HighRisk, x.By, x.At = "Done", note, highRisk, actor, o.now().Format(time.RFC3339)
		return nil
	})
	if err != nil {
		return e, err
	}
	if highRisk {
		if c, err := o.Client(ctx, tenant, e.UCC); err == nil {
			csc := c.CSC
			if !contains(csc, "High-risk jurisdiction") {
				csc = append(append([]string(nil), csc...), "High-risk jurisdiction")
			}
			if _, err := o.SetClientRisk(ctx, tenant, actor, e.UCC, "High", csc, "FATF "+e.Statement+": "+note); err != nil {
				return e, err
			}
		}
	}
	o.closeWork(ctx, tenant, actor, wSanctions, "edd-"+e.ID, "EDD recorded")
	o.log(ctx, tenant, actor, "sanctions.edd", e.UCC, map[string]any{"statement": e.Statement, "high_risk": highRisk})
	return e, nil
}

// ---- the screen ----

// SanctionsView is KYC & PMLA → Sanctions.
type SanctionsView struct {
	ListTypes []SanctionsListType    `json:"list_types"`
	Lists     []SanctionsListVersion `json:"lists"`   // latest of each type
	Entries   map[string]int         `json:"entries"` // in force, by type
	LastRun   *SanctionsRun          `json:"last_run,omitempty"`
	Cases     []SanctionsCase        `json:"cases"`
	Config    SanctionsConfig        `json:"config"`
	FATF      []FATFStatement        `json:"fatf"`
	EDD       []FATFEDD              `json:"edd"`
	Counts    map[string]int         `json:"counts"`
}

// Sanctions is the sanctions screen (Compliance only: a case is
// confidential like an STR).
func (o *Ops) Sanctions(ctx context.Context, tenant, actor string) (SanctionsView, error) {
	v := SanctionsView{ListTypes: SanctionsListTypes, Lists: []SanctionsListVersion{}, Entries: map[string]int{}, Cases: []SanctionsCase{}, FATF: []FATFStatement{}, EDD: []FATFEDD{}, Counts: map[string]int{}}
	if err := o.require(ctx, tenant, actor, "Viewing sanctions screening", RoleCompliance); err != nil {
		return v, err
	}
	v.Config = o.sanctionsConfig(ctx, tenant)
	for _, t := range SanctionsListTypes {
		if x, ok := o.latestListVersions(ctx, tenant)[t.ID]; ok {
			v.Lists = append(v.Lists, x)
		}
	}
	today := o.now().Format("2006-01-02")
	list, _ := docs.List[Debarred](ctx, o.Docs, tenant, cDebarred, "")
	for _, d := range list {
		if d.Delisted == "" && (d.Till == "" || d.Till >= today) {
			v.Entries[listTypeOf(d)]++
		}
	}
	if r, ok := o.lastFullRun(ctx, tenant); ok {
		v.LastRun = &r
	}
	cs, _ := docs.List[SanctionsCase](ctx, o.Docs, tenant, cSanctionsCase, "")
	now := o.now().Format(time.RFC3339)
	for _, k := range cs {
		if k.open() {
			v.Counts["Open"]++
			if k.DueBy < now && (k.Status != SCConfirmed || !k.reportsDone()) {
				v.Counts["Past 24 hours"]++
			}
		}
		if k.Held {
			v.Counts["Held"]++
		}
		if k.Notes == nil {
			k.Notes = []string{}
		}
		if k.Why == nil {
			k.Why = []string{}
		}
		v.Cases = append(v.Cases, k)
	}
	rank := map[string]int{SCToVerify: 0, SCClearing: 1, SCConfirmed: 2, SCReported: 3, SCUnfrozen: 4, SCFalse: 5}
	sort.SliceStable(v.Cases, func(i, j int) bool {
		if rank[v.Cases[i].Status] != rank[v.Cases[j].Status] {
			return rank[v.Cases[i].Status] < rank[v.Cases[j].Status]
		}
		return v.Cases[i].ID > v.Cases[j].ID
	})
	fs, _ := docs.List[FATFStatement](ctx, o.Docs, tenant, cFATFStatement, "")
	sort.SliceStable(fs, func(i, j int) bool { return fs[i].Date > fs[j].Date })
	v.FATF = append(v.FATF, fs...)
	es, _ := docs.List[FATFEDD](ctx, o.Docs, tenant, cFATFEDD, "")
	sort.SliceStable(es, func(i, j int) bool {
		if (es[i].Status == "Done") != (es[j].Status == "Done") {
			return es[i].Status != "Done"
		}
		return es[i].ID < es[j].ID
	})
	for _, e := range es {
		if e.Status != "Done" {
			v.Counts["EDD to do"]++
		}
	}
	v.EDD = append(v.EDD, es...)
	return v, nil
}

// lastFullRun is the latest screening of every client.
func (o *Ops) lastFullRun(ctx context.Context, tenant string) (SanctionsRun, bool) {
	rs, _ := docs.List[SanctionsRun](ctx, o.Docs, tenant, cSanctionsRun, "")
	var best SanctionsRun
	for _, r := range rs {
		if r.All && r.At > best.At {
			best = r
		}
	}
	return best, best.ID != ""
}

// ---- schedule ----

// sanctionsTick screens every client again when the last full screening is
// older than the firm's interval (from 06:00 IST), and chases cases past
// their 24 hours.
func (o *Ops) sanctionsTick(ctx context.Context, tenant string, now time.Time) {
	vs := o.latestListVersions(ctx, tenant)
	loaded := false
	for t := range vs {
		loaded = loaded || isSanctionsList(t)
	}
	if !loaded {
		return
	}
	cfg := o.sanctionsConfig(ctx, tenant)
	n := now.In(ist)
	today := n.Format("2006-01-02")
	if r, ok := o.lastFullRun(ctx, tenant); (!ok || r.At[:10] <= addDays(today, -cfg.EveryDays)) && n.Format("15:04") >= "06:00" {
		_, _ = o.runSanctionsScreening(ctx, tenant, SystemActor, "scheduled")
	}
	cs, _ := docs.List[SanctionsCase](ctx, o.Docs, tenant, cSanctionsCase, "")
	stamp := n.Format(time.RFC3339)
	for _, k := range cs {
		if k.open() && k.DueBy < stamp && (k.Status != SCConfirmed || !k.reportsDone()) {
			o.raiseWork(ctx, tenant, wSanctions, k.ID+"-late", "Overdue: sanctions case "+k.ID+" is past its 24 hours",
				fmt.Sprintf("Case %s (%s, %s) was found at %s and is still %s. A match must be verified and reported not later than 24 hours from finding it. Decide it and record the reports now; note the reason for the delay on the case.", k.ID, k.Person, firstOf(k.UCC, k.Role), fmtTimeIST(k.Found), strings.ToLower(k.Status)),
				k.UCC, RoleCompliance, "kyc", today)
		}
	}
}

// ---- self-inspection ----

// siSanctions: every client screened against the latest sanctions lists
// within the firm's limit, no case past its 24 hours, every confirmed
// holder blocked with a freeze request, and FATF EDD done on time.
func siSanctions(e *siEnv) (siOut, error) {
	vs := e.o.latestListVersions(e.ctx, e.tenant)
	var latest string
	var names []string
	for _, t := range []string{ListUNSC, ListUAPA, ListWMD} {
		if v, ok := vs[t]; ok {
			names = append(names, t+" "+v.Version)
			if v.LoadedAt > latest {
				latest = v.LoadedAt
			}
		}
	}
	if latest == "" {
		return siSkip("Waiting for data: no UN Security Council, UAPA or WMD list is loaded (KYC & PMLA → Sanctions).")
	}
	cfg := e.o.sanctionsConfig(e.ctx, e.tenant)
	var s siOut
	s.input("Sanctions lists ("+strings.Join(names, ", ")+")", latest[:10])
	rs, err := docs.List[SanctionsRun](e.ctx, e.o.Docs, e.tenant, cSanctionsRun, "")
	if err != nil {
		return s, err
	}
	var full string
	per := map[string]string{}
	for _, r := range rs {
		if r.At[:10] > e.date {
			continue
		}
		if r.All {
			if r.At > full {
				full = r.At
			}
			continue
		}
		for _, u := range r.UCCs {
			if r.At > per[u] {
				per[u] = r.At
			}
		}
	}
	if full != "" {
		s.input("Last screening of all clients", full[:10])
	}
	oldest := addDays(e.date, -cfg.MaxAgeDays)
	for _, u := range sortedKeys(e.clients) {
		c := e.clients[u]
		if c.Status != "Active" {
			continue
		}
		s.checked++
		at := full
		if per[u] > at {
			at = per[u]
		}
		switch {
		case at == "":
			s.bad(fmt.Sprintf("%s · never screened against the sanctions lists", siClient(u)))
		case at < latest:
			s.bad(fmt.Sprintf("%s · last screened %s, before the latest list load (%s)", siClient(u), fmtDay(at[:10]), fmtDay(latest[:10])))
		case at[:10] < oldest:
			s.bad(fmt.Sprintf("%s · last screened %s, more than %d days ago", siClient(u), fmtDay(at[:10]), cfg.MaxAgeDays))
		}
	}
	cases, _ := docs.List[SanctionsCase](e.ctx, e.o.Docs, e.tenant, cSanctionsCase, "")
	stamp := e.now.In(ist).Format(time.RFC3339)
	for _, k := range cases {
		s.checked++
		switch {
		case (k.Status == SCToVerify || k.Status == SCClearing) && k.DueBy < stamp:
			s.bad(fmt.Sprintf("Case %s · %s, not decided within 24 hours of %s", k.ID, strings.ToLower(k.Status), fmtTimeIST(k.Found)))
		case k.Status == SCConfirmed && !k.reportsDone() && k.DueBy < stamp:
			s.bad(fmt.Sprintf("Case %s · confirmed, reports not all recorded within 24 hours of %s", k.ID, fmtTimeIST(k.Found)))
		}
		if (k.Status == SCConfirmed || k.Status == SCReported) && blockingRole(k.Role) && k.UCC != "" && k.List != ListDefaulters {
			c := e.clients[k.UCC]
			if c.Sanctions == nil || !c.Blocked {
				s.bad(fmt.Sprintf("Case %s · %s is a confirmed match but not blocked", k.ID, siClient(k.UCC)))
			}
		}
	}
	es, _ := docs.List[FATFEDD](e.ctx, e.o.Docs, e.tenant, cFATFEDD, "")
	for _, x := range es {
		s.checked++
		if x.Status != "Done" && x.Due < e.date {
			s.bad(fmt.Sprintf("%s · EDD for FATF %s (%s) not recorded by %s", siClient(x.UCC), x.Statement, strings.ToLower(x.Level), fmtDay(x.Due)))
		}
	}
	s.summary = fmt.Sprintf("%d clients, cases and EDD items checked against %s; %d problems.", s.checked, strings.Join(names, ", "), s.found)
	return s, nil
}

func init() {
	siTests = append(siTests, SITest{ID: "sanctions_screening", Title: "Clients screened against the UNSC, UAPA and WMD lists; matches reported in 24 hours", Area: "Compliance", Severity: "high", Owner: RoleCompliance,
		Source:    "SEBI/HO/MIRSD/MIRSDSECFATF/P/CIR/2024/78 of 6 Jun 2024 (AML/CFT master circular) paras 54-70; MHA order under UAPA s.51A of 2 Feb 2021 (24 hours); MoF order under WMD Act s.12A of 30 Jan 2023",
		ToConfirm: "The 24 hours are the UAPA order's; the SEBI circular says without delay. The screening interval is the firm's setting.",
		Checks:    "Active clients screened against the latest UNSC, UAPA and WMD lists, after their last load and within the firm's limit of days; possible matches decided and confirmed matches reported within 24 hours; confirmed holders blocked; EDD on clients linked to FATF-listed jurisdictions recorded on time.",
		Fix:       "Load the latest lists and screen all clients on KYC & PMLA → Sanctions; decide each case and record the reports; record the EDD.",
		Screen:    "kyc", ScreenName: "Compliance → KYC & PMLA → Sanctions", Real: true, run: siSanctions})
}
