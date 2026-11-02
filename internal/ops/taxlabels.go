package ops

// ---- section and form names: Income-tax Act 1961 or 2025 ----
//
// The Income-tax Act, 2025 replaced the Income-tax Act, 1961 from 1 April
// 2026; the Income-tax Rules, 2026 replaced the 1962 Rules. Rates did not
// change with the renumbering; the names did. Labels are chosen by the date
// of the payment or the year a document is for, so papers for periods
// before 1 April 2026 keep the 1961 names. The 2025-Act references below
// are from incometaxindia.gov.in (sections 37, 196, 393; Form 131 FAQ;
// rules 215 and 245):
//
//	111A  short-term capital gains (STT paid)         → section 196
//	112A  long-term capital gains (STT paid)          → section 198
//	194H  commission or brokerage                     → section 393(1) [Table: Sl. No. 1(ii)]
//	194-I rent                                        → section 393(1) [Table: Sl. No. 2(ii)]
//	194C  contractors                                 → section 393(1) [Table: Sl. No. 6(i)]
//	194J  professional and technical fees             → section 393(1) [Table: Sl. No. 6(iii)]
//	194Q  purchase of goods                           → section 393(1) [Table: Sl. No. 8(ii)]
//	195   payments to non-residents                   → section 393(2) (other sums: Table Sl. No. 17)
//	43B(h) micro and small enterprises paid late      → section 37(2)(g)
//	Form 16A TDS certificate (non-salary)             → Form 131 (section 395(4), rule 215)
//	Form 26AS annual information statement            → Form 168 (rule 245)
//	"previous year" / "assessment year"               → "tax year"

// ITAct2025From is the first day the Income-tax Act, 2025 applies.
const ITAct2025From = "2026-04-01"

// newTaxAct says whether the 2025 Act's names apply on a date (or for a
// year starting on it).
func newTaxAct(date string) bool { return date >= ITAct2025From }

var tdsRefs = map[string][2]string{
	"194C":   {"section 194C", "section 393(1) [Table: Sl. No. 6(i)]"},
	"194J":   {"section 194J", "section 393(1) [Table: Sl. No. 6(iii)]"},
	"194JA":  {"section 194J", "section 393(1) [Table: Sl. No. 6(iii)]"},
	"194I":   {"section 194-I", "section 393(1) [Table: Sl. No. 2(ii)]"},
	"194IPM": {"section 194-I", "section 393(1) [Table: Sl. No. 2(ii)]"},
	"194H":   {"section 194H", "section 393(1) [Table: Sl. No. 1(ii)]"},
	"194Q":   {"section 194Q", "section 393(1) [Table: Sl. No. 8(ii)]"},
	"195":    {"section 195", "section 393(2) [Table: Sl. No. 17]"},
}

// tdsRef names the TDS provision for a section code ("194C" …) on a date.
// The codes stay as the keys of vendor records and ledger accounts.
func tdsRef(code, date string) string {
	r, ok := tdsRefs[code]
	if !ok {
		return code
	}
	if newTaxAct(date) {
		return r[1]
	}
	return r[0]
}

// cgRef names the capital gains section: kind "st" (111A / 196) or "lt"
// (112A / 198), for a year starting on from.
func cgRef(kind, from string) string {
	switch {
	case kind == "st" && newTaxAct(from):
		return "section 196"
	case kind == "st":
		return "section 111A"
	case newTaxAct(from):
		return "section 198"
	}
	return "section 112A"
}

// msmeRef names the rule that disallows late payment to micro and small
// enterprises until paid.
func msmeRef(date string) string {
	if newTaxAct(date) {
		return "section 37(2)(g)"
	}
	return "section 43B(h)"
}

// taxYearWord is "tax year" under the 2025 Act, "financial year" before.
func taxYearWord(from string) string {
	if newTaxAct(from) {
		return "tax year"
	}
	return "financial year"
}

// tdsCertForm names the non-salary TDS certificate for a payment date.
func tdsCertForm(date string) string {
	if newTaxAct(date) {
		return "Form 131"
	}
	return "Form 16A"
}

// TDSSectionLabel is the label of a TDS section code for a date, with the
// 1961 section it replaced.
func TDSSectionLabel(code, date string) string {
	r, ok := tdsRefs[code]
	if !ok {
		return code
	}
	if newTaxAct(date) {
		return r[1] + " (was " + r[0][len("section "):] + ")"
	}
	return r[0]
}
