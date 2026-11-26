package ops

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// ---- Compliance → Registers: the small registers the rules ask for ----
//
// Several rules ask the firm only to keep a record: a TDS certificate
// issued to each payee, a lower deduction certificate a vendor gave, the
// IRN of an e-invoice, the Rights and Obligations document given to a new
// demat account holder, a DP-attested client master list given to a
// client, the confirmation an association of persons gave when its demat
// account was opened, the depository's monthly bill to the DP, a notice
// sent to clients. Each is a register "kind" here with its own fields and
// checks; the records are kept for good (a wrong one is withdrawn with a
// reason, never deleted) and the self-inspection tests read them.
//
// The questions these rules need answered once (the firm's turnover, its
// e-invoicing threshold, its e-mail for BSE's investor services, its NSE
// user ID limits …) are Firm practices in their own section, below.

const (
	cCompRec = "comprec" // record ID -> CompRecord

	CompActive    = "Active"
	CompWithdrawn = "Withdrawn"

	// SectionTaxCompliance is the Firm practices section of these
	// questions.
	SectionTaxCompliance = "Tax, partners and DP"
)

// CompField is one field of a register kind.
type CompField struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Type     string   `json:"type"` // text, date, month, amount, pct, choice, ucc, vendor, boid, email, fy
	Options  []string `json:"options,omitempty"`
	Optional bool     `json:"optional,omitempty"`
	Hint     string   `json:"hint,omitempty"`
}

// CompKind is one register.
type CompKind struct {
	ID     string      `json:"id"`
	Title  string      `json:"title"`
	Area   string      `json:"area"` // Tax and GST, Partners and website, Grievances, Depository
	Rule   string      `json:"rule"` // the source, in words
	Help   string      `json:"help"` // what the person records
	Roles  []string    `json:"roles"`
	Fields []CompField `json:"fields"`
	// Auto: written by an action of Ledgerline (a notice sent); staff may
	// still record one done outside it.
	Auto  bool `json:"auto,omitempty"`
	check func(o *Ops, ctx context.Context, tenant string, r *CompRecord) error
}

// CompRecord is one record of a register.
type CompRecord struct {
	ID     string            `json:"id"` // CR-000001
	Kind   string            `json:"kind"`
	Values map[string]string `json:"values"`
	Note   string            `json:"note,omitempty"`
	Status string            `json:"status"` // Active, Withdrawn
	Why    string            `json:"withdrawn_why,omitempty"`
	By     string            `json:"by"`
	At     string            `json:"at"`
}

// V reads a value.
func (r CompRecord) V(k string) string { return r.Values[k] }

var (
	reFY      = regexp.MustCompile(`^20[0-9]{2}-[0-9]{2}$`)
	reMonth   = regexp.MustCompile(`^20[0-9]{2}-(0[1-9]|1[0-2])$`)
	reBOID16  = regexp.MustCompile(`^[0-9]{16}$`)
	reIRN     = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
	staffRegs = []string{RoleMaker, RoleChecker, RoleCompliance}
)

// compKinds are the registers, in the order the screen lists them.
var compKinds = []CompKind{
	// Tax and GST
	{ID: "tds-cert", Title: "TDS certificates issued", Area: "Tax and GST", Roles: []string{RoleChecker, RoleCompliance},
		Rule: "Income-tax Rules, 2026 (successors of rules 31 and 31A of the 1962 Rules): the certificate downloaded from TRACES is given to the payee within 15 days of the due date of the quarterly statement (non-salary), and by 15 June for salary.",
		Help: "One line for each certificate given: the payee, the quarter and the certificate number from TRACES.",
		Fields: []CompField{{Key: "payee_kind", Label: "Payee", Type: "choice", Options: []string{"Vendor", "Employee", "Authorised person"}},
			{Key: "payee", Label: "Vendor ID, employee code or AP code", Type: "text"},
			{Key: "fy", Label: "Financial year (2026-27)", Type: "fy"},
			{Key: "quarter", Label: "Quarter", Type: "choice", Options: []string{"Q1", "Q2", "Q3", "Q4", "Year (salary)"}},
			{Key: "cert_no", Label: "Certificate number (TRACES)", Type: "text"},
			{Key: "issued_on", Label: "Given on", Type: "date"}}},
	{ID: "ldc", Title: "Lower or nil deduction certificates", Area: "Tax and GST", Roles: staffRegs,
		Rule: "Income-tax Act, 2025: a certificate the Assessing Officer issues to the payee for deduction at a lower rate or none (was section 197); honoured up to its amount and within its dates, and reported in the quarterly statement.",
		Help: "The vendor's certificate: its number, section, rate, the amount it covers and its dates. Vendor bills then expect TDS at its rate until the amount is used up.",
		Fields: []CompField{{Key: "vendor", Label: "Vendor", Type: "vendor"}, {Key: "section", Label: "TDS section (1961 code, e.g. 194J)", Type: "text"},
			{Key: "cert_no", Label: "Certificate number", Type: "text"}, {Key: "rate", Label: "Rate (%)", Type: "pct"},
			{Key: "limit", Label: "Amount covered (₹)", Type: "amount"}, {Key: "from", Label: "Valid from", Type: "date"}, {Key: "to", Label: "Valid to", Type: "date"}},
		check: func(o *Ops, ctx context.Context, tenant string, r *CompRecord) error {
			if r.V("to") < r.V("from") {
				return invalid("the certificate ends before it starts")
			}
			if _, ok := tdsSection(strings.ToUpper(r.V("section"))); !ok || r.V("section") == "" {
				return invalid("unknown TDS section %s", r.V("section"))
			}
			r.Values["section"] = strings.ToUpper(r.V("section"))
			return nil
		}},
	{ID: "tds-decl", Title: "No-deduction declarations received", Area: "Tax and GST", Roles: staffRegs,
		Rule: "Income-tax Act, 2025: a payee's self-declaration that no tax is payable (was Form 15G or 15H) is honoured for the year and reported in the quarterly statement.",
		Help: "The declaration's reference and the year it is for.",
		Fields: []CompField{{Key: "vendor", Label: "Vendor", Type: "vendor"}, {Key: "fy", Label: "Financial year", Type: "fy"},
			{Key: "ref", Label: "Declaration reference", Type: "text"}, {Key: "received_on", Label: "Received on", Type: "date"}}},
	{ID: "pan-status", Title: "Vendor PAN status checks", Area: "Tax and GST", Roles: staffRegs,
		Rule: "Income-tax Act, 2025, section 397 (was 206AA): TDS at the higher rate when the payee's PAN is not furnished or is inoperative (not linked with Aadhaar).",
		Help: "The status of the vendor's PAN as the income-tax portal showed it, with the date checked. The latest check counts.",
		Fields: []CompField{{Key: "vendor", Label: "Vendor", Type: "vendor"}, {Key: "status", Label: "PAN status", Type: "choice", Options: []string{"Operative", "Inoperative"}},
			{Key: "checked_on", Label: "Checked on", Type: "date"}}},
	{ID: "irn", Title: "E-invoice IRNs", Area: "Tax and GST", Roles: staffRegs,
		Rule: "CGST Rules, rule 48(4): a registered person above the notified turnover reports each B2B invoice to the Invoice Registration Portal; GSTN advisory: within 30 days of the invoice date when AATO is ₹10 crore or more.",
		Help: "The IRN and acknowledgement the IRP gave for an invoice or contract note to a client with a GSTIN.",
		Fields: []CompField{{Key: "invoice", Label: "Invoice or contract note number", Type: "text"}, {Key: "irn", Label: "IRN (64 characters)", Type: "text"},
			{Key: "ack_no", Label: "Acknowledgement number", Type: "text"}, {Key: "ack_date", Label: "Acknowledgement date", Type: "date"}},
		check: func(o *Ops, ctx context.Context, tenant string, r *CompRecord) error {
			if !reIRN.MatchString(r.V("irn")) {
				return invalid("the IRN is 64 hexadecimal characters")
			}
			return nil
		}},
	// Partners and website
	{ID: "ap-notice", Title: "Notices to branch clients of an AP change", Area: "Partners and website", Roles: []string{RoleChecker, RoleCompliance}, Auto: true,
		Rule: "SEBI master circular for stock brokers 2025/90, para 33.7.4: all registered clients of a branch are told before the authorised person at the branch changes.",
		Help: "Sent from this screen with Notify branch clients; record here one sent another way.",
		Fields: []CompField{{Key: "branch", Label: "Branch code", Type: "text"}, {Key: "old_ap", Label: "AP leaving (code)", Type: "text", Optional: true},
			{Key: "new_ap", Label: "AP taking over (code)", Type: "text", Optional: true}, {Key: "effective_on", Label: "Change takes effect on", Type: "date"},
			{Key: "sent_on", Label: "Notice sent on", Type: "date"}, {Key: "clients", Label: "Clients told", Type: "text", Optional: true}},
		check: func(o *Ops, ctx context.Context, tenant string, r *CompRecord) error {
			if r.V("sent_on") >= r.V("effective_on") {
				return invalid("the notice goes before the change takes effect")
			}
			return nil
		}},
	{ID: "prop-notice", Title: "Notice to clients before proprietary trading", Area: "Partners and website", Roles: []string{RoleCompliance}, Auto: true,
		Rule: "SEBI master circular for stock brokers 2025/90, paras 30.1-30.3: the broker tells each client whether it does client business, proprietary trading or both, and tells all clients before it starts proprietary trading.",
		Help: "Sent from this screen with Tell clients about proprietary trading; record here one sent another way.",
		Fields: []CompField{{Key: "starts_on", Label: "Proprietary trading starts on", Type: "date"}, {Key: "sent_on", Label: "Notice sent on", Type: "date"},
			{Key: "clients", Label: "Clients told", Type: "text", Optional: true}}},
	{ID: "website", Title: "Website updates", Area: "Partners and website", Roles: []string{RoleCompliance}, Auto: true,
		Rule: "NSE/INSP/73976 item 4.12 (the USCNBA accounts on the website); NSE/COMP/73981 Exhibits 42-44 (the active APs and those cancelled for disciplinary reasons).",
		Help: "Recorded with Mark the website updated once the list is on the firm's website.",
		Fields: []CompField{{Key: "list", Label: "List", Type: "choice", Options: []string{"USCNBA accounts", "Authorised persons"}},
			{Key: "published_on", Label: "Put on the website on", Type: "date"}, {Key: "hash", Label: "Version", Type: "text", Optional: true}}},
	// Depository
	{ID: "ro-kit", Title: "Rights and Obligations given to demat account holders", Area: "Depository", Roles: staffRegs, Auto: true,
		Rule: "SEBI master circular for depositories 2024/168, para 1.3.4: the Rights and Obligations document and the other account opening kit documents are given electronically or physically as the client chooses, with a log of electronic delivery.",
		Help: "How and when the client got the document. Sent by e-mail from this screen, the dispatch reference is filled in.",
		Fields: []CompField{{Key: "ucc", Label: "Client", Type: "ucc"}, {Key: "mode", Label: "Given", Type: "choice", Options: []string{"Electronic", "Physical"}},
			{Key: "delivered_on", Label: "Given on", Type: "date"}, {Key: "ref", Label: "Dispatch or courier reference", Type: "text", Optional: true}}},
	{ID: "cml", Title: "DP-attested client master lists given", Area: "Depository", Roles: staffRegs, Auto: true,
		Rule: "SEBI HO/38/13/(3)2026-MIRSD-POD/I/3763/2026, paras 4 and 6: on request the DP gives the client a copy of the client master list attested by it, not older than two months, for service requests and transfers or demat requests to RTAs.",
		Help: "Issued from this screen with Issue attested CML; record here one issued another way.",
		Fields: []CompField{{Key: "ucc", Label: "Client", Type: "ucc"}, {Key: "bo_id", Label: "BO ID", Type: "boid"},
			{Key: "purpose", Label: "For", Type: "choice", Options: []string{"Service request to an RTA", "Transfer or demat request", "Other"}},
			{Key: "cml_date", Label: "Client master as at", Type: "date"}, {Key: "issued_on", Label: "Given on", Type: "date"}, {Key: "ref", Label: "Dispatch reference", Type: "text", Optional: true}},
		check: func(o *Ops, ctx context.Context, tenant string, r *CompRecord) error {
			if r.V("cml_date") > r.V("issued_on") || r.V("cml_date") < addMonths(r.V("issued_on"), -2) {
				return invalid("the client master list given must be not older than two months on the day it is given")
			}
			return nil
		}},
	{ID: "aop", Title: "Association of persons: demat account declarations", Area: "Depository", Roles: staffRegs,
		Rule: "SEBI/HO/MRD/PoD1/CIR/P/2025/24, para 3: a demat account in an AoP's own name needs the PAN of the AoP and of its principal officer, and the AoP's confirmation that it holds only securities the law permits (mutual fund units, bonds, government securities), not equity shares.",
		Help: "The AoP's demat account with both PANs and the date of its confirmation. The account is then checked for equity holdings.",
		Fields: []CompField{{Key: "ucc", Label: "Client (the AoP)", Type: "ucc"}, {Key: "bo_id", Label: "BO ID", Type: "boid"}, {Key: "aop_pan", Label: "PAN of the AoP", Type: "text"},
			{Key: "officer", Label: "Principal officer", Type: "text"}, {Key: "officer_pan", Label: "Principal officer's PAN", Type: "text"},
			{Key: "confirmed_on", Label: "No-equity confirmation dated", Type: "date"}},
		check: func(o *Ops, ctx context.Context, tenant string, r *CompRecord) error {
			for _, k := range []string{"aop_pan", "officer_pan"} {
				r.Values[k] = strings.ToUpper(r.V(k))
				if !rePAN.MatchString(r.V(k)) {
					return invalid("%s is not a PAN", r.V(k))
				}
			}
			if r.V("aop_pan") == r.V("officer_pan") {
				return invalid("the AoP's PAN and the principal officer's PAN are different PANs")
			}
			return nil
		}},
	{ID: "cdsl-bill", Title: "CDSL's monthly bill to the DP", Area: "Depository", Roles: []string{RoleChecker, RoleCompliance},
		Rule: "SEBI master circular for depositories 2024/168, para 4.86.6: depository charges are recovered from clients only to the extent the depository levied them.",
		Help: "Each head of CDSL's bill for the month as CDSL billed it. The DP billing comparison sets it against what was recovered from clients.",
		Fields: []CompField{{Key: "month", Label: "Month (YYYY-MM)", Type: "month"},
			{Key: "head", Label: "Head", Type: "choice", Options: []string{"Transaction charges", "Stamp duty", "Other"}},
			{Key: "amount", Label: "Amount CDSL billed (₹)", Type: "amount"}, {Key: "bill_no", Label: "CDSL bill number", Type: "text", Optional: true}}},
}

func compKind(id string) (CompKind, bool) {
	for _, k := range compKinds {
		if k.ID == id {
			return k, true
		}
	}
	return CompKind{}, false
}

// CompKinds lists the registers.
func CompKinds() []CompKind { return append([]CompKind(nil), compKinds...) }

// cleanCompValues checks a record's values against its kind's fields.
func (o *Ops) cleanCompValues(ctx context.Context, tenant string, k CompKind, in map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for _, f := range k.Fields {
		v := strings.TrimSpace(in[f.Key])
		if v == "" {
			if !f.Optional {
				return nil, invalid("give %s", strings.ToLower(f.Label))
			}
			continue
		}
		if len(v) > 300 {
			return nil, invalid("%s is too long", f.Label)
		}
		switch f.Type {
		case "date":
			if !ledger.ValidDate(v) {
				return nil, invalid("%s must be a date (YYYY-MM-DD)", f.Label)
			}
		case "month":
			if !reMonth.MatchString(v) {
				return nil, invalid("%s is a month: YYYY-MM", f.Label)
			}
		case "fy":
			if !reFY.MatchString(v) {
				return nil, invalid("%s looks like 2026-27", f.Label)
			}
		case "amount":
			a, err := money.ParseAmount(v)
			if err != nil || a < 0 {
				return nil, invalid("%s is an amount in rupees", f.Label)
			}
			v = a.String()
		case "pct":
			d, err := money.ParseDec(v)
			if err != nil || d.Sign() < 0 || d.Cmp(money.DecInt(100)) > 0 {
				return nil, invalid("%s is a percentage from 0 to 100", f.Label)
			}
		case "choice":
			if !contains(f.Options, v) {
				return nil, invalid("%s must be one of %s", f.Label, strings.Join(f.Options, ", "))
			}
		case "ucc":
			v = strings.ToUpper(v)
			if _, err := o.Client(ctx, tenant, v); err != nil {
				return nil, invalid("client %s not found", v)
			}
		case "vendor":
			v = strings.ToUpper(v)
			if _, err := o.Vendor(ctx, tenant, v); err != nil {
				return nil, invalid("vendor %s not found", v)
			}
		case "boid":
			if v = normDematID(v); !reBOID16.MatchString(v) {
				return nil, invalid("%s is a 16-digit CDSL BO ID", f.Label)
			}
		case "email":
			if !reEmail.MatchString(v) {
				return nil, invalid("%s is not an e-mail address", f.Label)
			}
		}
		out[f.Key] = v
	}
	return out, nil
}

// AddCompRecord records one line of a register.
func (o *Ops) AddCompRecord(ctx context.Context, tenant, actor, kind string, vals map[string]string, note string) (CompRecord, error) {
	k, ok := compKind(kind)
	if !ok {
		return CompRecord{}, fmt.Errorf("%w: no register %q", ErrNotFound, kind)
	}
	if err := o.require(ctx, tenant, actor, "Recording "+strings.ToLower(k.Title), k.Roles...); err != nil {
		return CompRecord{}, err
	}
	return o.addCompRecord(ctx, tenant, actor, k, vals, note)
}

func (o *Ops) addCompRecord(ctx context.Context, tenant, actor string, k CompKind, vals map[string]string, note string) (CompRecord, error) {
	v, err := o.cleanCompValues(ctx, tenant, k, vals)
	if err != nil {
		return CompRecord{}, err
	}
	r := CompRecord{Kind: k.ID, Values: v, Note: clip(strings.TrimSpace(note), 500), Status: CompActive, By: actor, At: o.now().Format(time.RFC3339)}
	if k.check != nil {
		if err := k.check(o, ctx, tenant, &r); err != nil {
			return CompRecord{}, err
		}
	}
	n, err := o.next(ctx, tenant, "comprec", actor)
	if err != nil {
		return CompRecord{}, err
	}
	r.ID = fmt.Sprintf("CR-%06d", n)
	if _, err := docs.Put(ctx, o.Docs, tenant, cCompRec, r.ID, r, 0, actor); err != nil {
		return r, err
	}
	o.log(ctx, tenant, actor, "register.recorded", r.ID, map[string]any{"kind": k.ID})
	return r, nil
}

// WithdrawCompRecord withdraws a record made in error, with the reason.
func (o *Ops) WithdrawCompRecord(ctx context.Context, tenant, actor, id, why string) (CompRecord, error) {
	r, _, err := docs.Get[CompRecord](ctx, o.Docs, tenant, cCompRec, id)
	if err != nil {
		return r, err
	}
	k, _ := compKind(r.Kind)
	if err := o.require(ctx, tenant, actor, "Recording "+strings.ToLower(k.Title), k.Roles...); err != nil {
		return r, err
	}
	if strings.TrimSpace(why) == "" {
		return r, invalid("say why the record is withdrawn")
	}
	r, err = docs.Update(ctx, o.Docs, tenant, cCompRec, id, actor, func(x *CompRecord) error {
		if x.Status != CompActive {
			return fmt.Errorf("%w: %s is already withdrawn", ErrState, id)
		}
		x.Status, x.Why = CompWithdrawn, clip(strings.TrimSpace(why), 300)
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "register.withdrawn", id, map[string]any{"kind": r.Kind, "why": why})
	}
	return r, err
}

// compRecs is a register's active records, oldest first.
func (o *Ops) compRecs(ctx context.Context, tenant, kind string) []CompRecord {
	all, _ := docs.List[CompRecord](ctx, o.Docs, tenant, cCompRec, "")
	var out []CompRecord
	for _, r := range all {
		if r.Kind == kind && r.Status == CompActive {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// CompRegister is one register with its records, newest first.
type CompRegister struct {
	Kind    CompKind     `json:"kind"`
	Records []CompRecord `json:"records"`
}

// CompRegisters lists every register with its records (all staff but
// branch and AP logins see them).
func (o *Ops) CompRegisters(ctx context.Context, tenant, actor string) ([]CompRegister, error) {
	if err := o.require(ctx, tenant, actor, "Viewing compliance registers", RoleMaker, RoleChecker, RoleRisk, RoleCompliance); err != nil {
		return nil, err
	}
	all, err := docs.List[CompRecord](ctx, o.Docs, tenant, cCompRec, "")
	if err != nil {
		return nil, err
	}
	by := map[string][]CompRecord{}
	for _, r := range all {
		by[r.Kind] = append(by[r.Kind], r)
	}
	var out []CompRegister
	for _, k := range compKinds {
		rs := by[k.ID]
		sort.SliceStable(rs, func(i, j int) bool { return rs[i].ID > rs[j].ID })
		if rs == nil {
			rs = []CompRecord{}
		}
		out = append(out, CompRegister{Kind: k, Records: rs})
	}
	return out, nil
}

// ---- the Firm practices these registers rely on ----

func tcQ(id, short, question, why, note string, opts []PracticeOption, fields ...PracticeField) practiceDef {
	return practiceDef{ID: id, Section: SectionTaxCompliance, Short: short, Question: question, Why: why, Note: note,
		Setting: "Recorded here; read by Compliance → Registers and the self-inspection", Options: opts, Fields: fields}
}

var tcPracticeDefs = []practiceDef{
	tcQ("tax-turnover", "Turnover for TDS on purchases", "Were the firm's total sales, gross receipts or turnover from its business above ₹10 crore in the previous financial year?",
		"A buyer with turnover above ₹10 crore deducts 0.1% TDS on purchases of goods from a seller beyond ₹50 lakh in the year (Income-tax Act, 2025 s.393(1) Table Sl. 8(ii), was 194Q).",
		"Answer again each April for the new year.",
		[]PracticeOption{fsOpt("above", "Above ₹10 crore: TDS on purchases of goods applies", ""), fsOpt("below", "₹10 crore or less", "")},
		PracticeField{Key: "fy", Label: "For the financial year (2026-27)"}),
	tcQ("gst-aato", "GST turnover and e-invoicing", "What is the firm's aggregate annual turnover (AATO) for GST?",
		"E-invoicing applies above the notified AATO (₹5 crore since 1 August 2023, notification 10/2023-Central Tax), and from 1 April 2025 a taxpayer with AATO of ₹10 crore or more reports each invoice to the IRP within 30 days (GSTN advisory).",
		"Ledgerline does not report to the IRP itself: record each IRN on Compliance → Registers.",
		[]PracticeOption{fsOpt("10cr", "₹10 crore or more: e-invoices, reported within 30 days", ""), fsOpt("5cr", "Above ₹5 crore and below ₹10 crore: e-invoices", ""), fsOpt("none", "₹5 crore or less: no e-invoicing", "")},
		PracticeField{Key: "aato", Label: "AATO (₹ crore)"}),
	tcQ("bse-grievance-email", "E-mail for BSE's investor services", "Which dedicated e-mail address has the firm given BSE's Investor Services for complaint correspondence?",
		"BSE's master circular on investor grievances (paras 2.4-2.7) asks for a dedicated e-mail ID for complaints BSE forwards, which are acted on at once and answered on BSE's portal within BSE's timeline.",
		"",
		[]PracticeOption{fsOpt("given", "A dedicated address, given to BSE", ""), fsOpt("notyet", "Not given yet", "")},
		PracticeField{Key: "email", Label: "The e-mail address"}, PracticeField{Key: "given_on", Label: "Given to BSE on (YYYY-MM-DD)"}),
	tcQ("nse-user-limits", "NSE user ID limits", "How many NSE user IDs of each type may the firm have?",
		"NSE's user ID circular (NSE/MSD/73956) sets the permitted user types (corporate manager, branch manager, dealer) and the number the member may hold; the self-inspection counts open terminals of each type against them.",
		"Enter the numbers NSE allowed the firm; Ledgerline does not assume any.",
		[]PracticeOption{fsOpt("set", "As entered below", "")},
		PracticeField{Key: "cm", Label: "Corporate manager IDs"}, PracticeField{Key: "bm", Label: "Branch manager IDs"}, PracticeField{Key: "dealer", Label: "Dealer IDs"}),
	tcQ("fo-clearing", "Clearing of equity derivatives", "How does the firm clear its equity derivatives trades?",
		"SEBI (Stock Brokers) Regulations, 2026, regulation 48: a self-clearing member keeps a segment deposit of ₹50 lakh in equity derivatives with the clearing corporation; a trading member clearing through another clearing member keeps none.",
		"",
		[]PracticeOption{fsOpt("self", "Self-clearing member (₹50 lakh segment deposit)", ""), fsOpt("tm", "Another clearing member clears them", ""), fsOpt("none", "The firm does not trade equity derivatives", "")}),
	tcQ("cdsl-stamp-status", "CDSL status for unpaid stamp duty", "Which CDSL transaction status code means 'Overdue - Insufficient Stamp Duty'?",
		"CDSL cancels at end of day an off-market or invocation transaction whose stamp duty is not funded in the DP's or BO's virtual account (communiqués DP2025-720, 731, DP2026-116, 169). Ledgerline raises a work item when the transaction export shows that status.",
		"Take the code from CDSL's standard value list (field OrdSttsTo of the transaction export).",
		[]PracticeOption{fsOpt("code", "The code below", ""), fsOpt("unknown", "Not known yet", "")},
		PracticeField{Key: "code", Label: "Status code"}),
	tcQ("pool-voting", "Voting on pool securities", "Does the firm ever vote on securities in its pool or other own demat accounts that belong to clients?",
		"SEBI master circular for depositories 2024/168, para 4.36: no voting rights are exercised on securities in pool accounts, and corporate benefits are held in trust for the beneficial owners.",
		"",
		[]PracticeOption{fsOpt("never", "Never: only the beneficial owners vote", "")}),
	tcQ("ro-kit", "Rights and Obligations to demat clients", "How does the firm give a new demat account holder the Rights and Obligations document?",
		"SEBI master circular for depositories 2024/168, para 1.3.4: electronically or physically as the client chooses, with a log of electronic delivery; the document is also shown on the DP's website.",
		"Ledgerline e-mails a link to the document on the firm's website and logs it; a physical copy is recorded by hand.",
		[]PracticeOption{fsOpt("choice", "As the client chooses: e-mail (logged) or a printed copy", "")},
		PracticeField{Key: "url", Label: "Link to the document on the firm's website"}),
}

func init() { practiceDefs = append(practiceDefs, tcPracticeDefs...) }

// tcPractice is the option a firm chose for one of these questions, with
// its values ("" when not answered).
func (o *Ops) tcPractice(ctx context.Context, tenant, id string) (string, map[string]string) {
	fp := o.firmPractice(ctx, tenant, id)
	v := fp.Values
	if v == nil {
		v = map[string]string{}
	}
	return fp.Applied, v
}
