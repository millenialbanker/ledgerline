package billscan

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/money"
)

// Fields are the details read from a bill. Every one is a suggestion: a
// person checks them before the bill is saved.
type Fields struct {
	VendorName    string `json:"vendor_name"`
	VendorGSTIN   string `json:"vendor_gstin"`
	VendorPAN     string `json:"vendor_pan"`
	VendorState   string `json:"vendor_state"`
	VendorAddress string `json:"vendor_address"`
	VendorEmail   string `json:"vendor_email"`
	VendorPhone   string `json:"vendor_phone"`
	BuyerGSTIN    string `json:"buyer_gstin"`

	BankName    string `json:"bank_name"`
	BankIFSC    string `json:"bank_ifsc"`
	BankAccount string `json:"bank_account"`

	BillNo   string `json:"bill_no"`
	BillDate string `json:"bill_date"` // YYYY-MM-DD
	DueDate  string `json:"due_date"`

	Taxable money.Amount `json:"taxable"`
	CGST    money.Amount `json:"cgst"`
	SGST    money.Amount `json:"sgst"`
	IGST    money.Amount `json:"igst"`
	Total   money.Amount `json:"total"`

	// Found lists the fields that were read (not worked out or blank).
	Found    []string `json:"found"`
	Warnings []string `json:"warnings"`
	Source   string   `json:"source"` // text, ai
}

// GST returns the tax on the bill.
func (f Fields) GST() money.Amount { return f.CGST + f.SGST + f.IGST }

// ---- GSTIN ----

const gstChars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"

var reGSTIN = regexp.MustCompile(`\b\d{2}[A-Z]{5}\d{4}[A-Z][0-9A-Z]Z[0-9A-Z]\b`)

// ValidGSTIN checks a GSTIN's form and check digit.
func ValidGSTIN(g string) bool {
	g = strings.ToUpper(strings.TrimSpace(g))
	if len(g) != 15 || !reGSTIN.MatchString(g) {
		return false
	}
	if _, ok := StateCodes[g[:2]]; !ok {
		return false
	}
	sum := 0
	for i := 0; i < 14; i++ {
		v := strings.IndexByte(gstChars, g[i])
		if v < 0 {
			return false
		}
		p := v * (1 + i%2)
		sum += p/36 + p%36
	}
	return gstChars[(36-sum%36)%36] == g[14]
}

// GSTINPAN returns the PAN inside a GSTIN.
func GSTINPAN(g string) string {
	if len(g) != 15 {
		return ""
	}
	return strings.ToUpper(g[2:12])
}

// GSTINState returns the state a GSTIN is registered in.
func GSTINState(g string) string {
	if len(g) < 2 {
		return ""
	}
	return StateCodes[g[:2]]
}

// StateCodes are the GST state codes.
var StateCodes = map[string]string{
	"01": "Jammu and Kashmir", "02": "Himachal Pradesh", "03": "Punjab", "04": "Chandigarh",
	"05": "Uttarakhand", "06": "Haryana", "07": "Delhi", "08": "Rajasthan", "09": "Uttar Pradesh",
	"10": "Bihar", "11": "Sikkim", "12": "Arunachal Pradesh", "13": "Nagaland", "14": "Manipur",
	"15": "Mizoram", "16": "Tripura", "17": "Meghalaya", "18": "Assam", "19": "West Bengal",
	"20": "Jharkhand", "21": "Odisha", "22": "Chhattisgarh", "23": "Madhya Pradesh", "24": "Gujarat",
	"26": "Dadra and Nagar Haveli and Daman and Diu", "27": "Maharashtra", "29": "Karnataka",
	"30": "Goa", "31": "Lakshadweep", "32": "Kerala", "33": "Tamil Nadu", "34": "Puducherry",
	"35": "Andaman and Nicobar Islands", "36": "Telangana", "37": "Andhra Pradesh", "38": "Ladakh",
	"97": "Other Territory",
}

// ValidPAN checks a PAN's form.
func ValidPAN(p string) bool { return rePAN.MatchString(strings.ToUpper(p)) && len(p) == 10 }

// ValidIFSC checks an IFSC's form.
func ValidIFSC(s string) bool { return reIFSC.MatchString(strings.ToUpper(s)) && len(s) == 11 }

var (
	rePAN     = regexp.MustCompile(`\b[A-Z]{3}[ABCFGHLJPTE][A-Z]\d{4}[A-Z]\b`)
	reIFSC    = regexp.MustCompile(`\b[A-Z]{4}0[A-Z0-9]{6}\b`)
	reEmail   = regexp.MustCompile(`(?i)\b[A-Z0-9._%+\-]+@[A-Z0-9.\-]+\.[A-Z]{2,}\b`)
	rePhone   = regexp.MustCompile(`(?:\+91[\s\-]?)?\b[6-9]\d{4}[\s\-]?\d{5}\b|\b0\d{2,4}[\s\-]\d{6,8}\b`)
	reAmount  = regexp.MustCompile(`(?:₹|Rs\.?|INR)?\s*(\d{1,3}(?:,\d{2,3})+(?:\.\d{1,2})?|\d+\.\d{1,2}|\d{2,})`)
	rePct     = regexp.MustCompile(`\d+(?:\.\d+)?\s*%`)
	reAccount = regexp.MustCompile(`(?i)(?:a/?c|account)\s*(?:no|number|#)?\.?\s*[:\-]?\s*(\d[\d\s]{7,20}\d)`)
	reBank    = regexp.MustCompile(`(?i)bank\s*(?:name)?\s*[:\-]\s*([A-Za-z][A-Za-z .&]{2,60})`)
	reBillNo  = regexp.MustCompile(`(?i)\b(?:tax\s+)?(?:invoice|inv|bill)\s*(?:no|number|#)\.?\s*[:\-]?\s*([A-Za-z0-9][A-Za-z0-9/\-_.]{0,29})`)
	reDateLbl = regexp.MustCompile(`(?i)\b(?:invoice|inv|bill)?\s*date[d]?\s*[:\-]?\s*(.{6,20})`)
	reDueLbl  = regexp.MustCompile(`(?i)\bdue\s*(?:date|on|by)\s*[:\-]?\s*(.{6,20})`)
	reDateAny = regexp.MustCompile(`(?i)\b(\d{1,2}[/\-.]\d{1,2}[/\-.]\d{2,4}|\d{1,2}[\s\-](?:jan|feb|mar|apr|may|jun|jul|aug|sep|sept|oct|nov|dec)[a-z]*[\s\-,]+\d{2,4}|\d{4}-\d{2}-\d{2})\b`)
	reCompany = regexp.MustCompile(`(?i)\b(pvt\.?\s*ltd|private\s+limited|limited|ltd\.?|llp|& co|and co|enterprises|services|solutions|traders|associates|technologies|consultants|agency|industries|systems|corporation|company)\b`)
	reNoise   = regexp.MustCompile(`(?i)^(tax\s+invoice|invoice|bill|original|duplicate|triplicate|for recipient|copy|page \d+|gst invoice|bill of supply|e-?invoice)\b`)
)

// ParseDate reads the dates bills use (Indian day-first order).
func ParseDate(s string) (string, bool) {
	s = strings.TrimSpace(strings.Trim(strings.TrimSpace(s), ".,"))
	m := reDateAny.FindString(s)
	if m == "" {
		return "", false
	}
	m = strings.NewReplacer(".", "/", "-", "/", ",", " ").Replace(m)
	m = strings.Join(strings.Fields(m), " ")
	for _, l := range []string{"2/1/2006", "2/1/06", "2006/01/02", "2 Jan 2006", "2 January 2006", "2/Jan/2006", "2 Jan 06", "2/Jan/06"} {
		mm := m
		if strings.Contains(l, "Jan") {
			mm = titleMonth(strings.ReplaceAll(m, "/", " "))
			l = strings.ReplaceAll(l, "/", " ")
		}
		if t, err := time.Parse(l, mm); err == nil {
			if t.Year() < 2000 || t.Year() > 2100 {
				continue
			}
			return t.Format("2006-01-02"), true
		}
	}
	return "", false
}

func titleMonth(s string) string {
	f := strings.Fields(strings.ToLower(s))
	for i, w := range f {
		if w == "sept" {
			w = "sep"
		}
		if w != "" && w[0] >= 'a' && w[0] <= 'z' {
			f[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(f, " ")
}

func lastAmount(line string) (money.Amount, bool) {
	line = rePct.ReplaceAllString(line, " ")
	line = reDateAny.ReplaceAllString(line, " ")
	line = reGSTIN.ReplaceAllString(line, " ")
	ms := reAmount.FindAllStringSubmatch(line, -1)
	if len(ms) == 0 {
		return 0, false
	}
	a, err := money.ParseAmount(strings.ReplaceAll(ms[len(ms)-1][1], ",", ""))
	return a, err == nil && a > 0
}

type amountLabel struct {
	field *money.Amount
	name  string
	re    *regexp.Regexp
}

// Parse reads bill details from a bill's text. firmGSTIN is the firm's own
// registration, so the other GSTIN on the bill is taken as the vendor's.
func Parse(text, firmGSTIN string) Fields {
	f := Fields{Source: "text", Found: []string{}, Warnings: []string{}}
	firmGSTIN = strings.ToUpper(strings.TrimSpace(firmGSTIN))
	var lines []string
	for _, l := range strings.Split(strings.ReplaceAll(text, "\r", "\n"), "\n") {
		if l = strings.Join(strings.Fields(l), " "); l != "" {
			lines = append(lines, l)
		}
	}
	upper := strings.ToUpper(text)
	found := func(n string) { f.Found = append(f.Found, n) }

	// GSTINs: the vendor's is the valid one that is not the firm's.
	vendorLine := -1
	var bad []string
	for i, l := range lines {
		for _, g := range reGSTIN.FindAllString(strings.ToUpper(l), -1) {
			if !ValidGSTIN(g) {
				bad = append(bad, g)
				continue
			}
			if g == firmGSTIN {
				f.BuyerGSTIN = g
				continue
			}
			if f.VendorGSTIN == "" {
				f.VendorGSTIN, vendorLine = g, i
			} else if f.BuyerGSTIN == "" && g != f.VendorGSTIN {
				f.BuyerGSTIN = g
			}
		}
	}
	if f.VendorGSTIN != "" {
		found("vendor_gstin")
		f.VendorPAN, f.VendorState = GSTINPAN(f.VendorGSTIN), GSTINState(f.VendorGSTIN)
	} else if m := rePAN.FindString(upper); m != "" {
		f.VendorPAN = m
		found("vendor_pan")
	}
	for _, g := range bad {
		f.Warnings = append(f.Warnings, fmt.Sprintf("%s looks like a GSTIN but its check digit is wrong; it may be misprinted.", g))
	}
	if firmGSTIN != "" && f.BuyerGSTIN != "" && f.BuyerGSTIN != firmGSTIN {
		f.Warnings = append(f.Warnings, "The bill is made out to GSTIN "+f.BuyerGSTIN+", not the firm's "+firmGSTIN+": input tax credit needs the firm's GSTIN on the bill.")
	}
	if firmGSTIN != "" && f.BuyerGSTIN == "" && f.VendorGSTIN != "" {
		f.Warnings = append(f.Warnings, "The firm's GSTIN is not on the bill: input tax credit needs it.")
	}

	// Vendor name and address: the letterhead above the vendor's GSTIN, or
	// the first company-like line.
	end := len(lines)
	if vendorLine >= 0 {
		end = vendorLine + 1
	}
	if end > 15 {
		end = 15
	}
	nameAt := -1
	for i := 0; i < end; i++ {
		l := stripLabel(lines[i])
		if reCompany.MatchString(l) && !reGSTIN.MatchString(strings.ToUpper(l)) && !reNoise.MatchString(l) && len(l) < 80 {
			nameAt = i
			break
		}
	}
	if nameAt < 0 {
		for i := 0; i < end && i < 6; i++ {
			l := stripLabel(lines[i])
			if !reNoise.MatchString(l) && letters(l) >= 4 && !reGSTIN.MatchString(strings.ToUpper(l)) && !strings.Contains(strings.ToLower(l), "date") {
				nameAt = i
				break
			}
		}
	}
	if nameAt >= 0 {
		f.VendorName = cleanName(stripLabel(lines[nameAt]))
		found("vendor_name")
		var addr []string
		for i := nameAt + 1; i < len(lines) && len(addr) < 3; i++ {
			l := lines[i]
			lo := strings.ToLower(l)
			if reGSTIN.MatchString(strings.ToUpper(l)) || reEmail.MatchString(l) || rePhone.MatchString(l) || reBillNo.MatchString(l) ||
				strings.Contains(lo, "gstin") || strings.Contains(lo, "pan") || strings.Contains(lo, "invoice") || strings.Contains(lo, "bill to") || strings.Contains(lo, "date") {
				break
			}
			addr = append(addr, strings.Trim(l, " ,"))
		}
		if len(addr) > 0 {
			f.VendorAddress = strings.Join(addr, ", ")
			found("vendor_address")
		}
	}
	if m := reEmail.FindString(text); m != "" {
		f.VendorEmail = strings.ToLower(m)
		found("vendor_email")
	}
	if m := rePhone.FindString(text); m != "" {
		f.VendorPhone = strings.Join(strings.Fields(strings.ReplaceAll(m, "-", " ")), "")
		found("vendor_phone")
	}

	// Bank details for paying the vendor.
	if m := reIFSC.FindString(upper); m != "" {
		f.BankIFSC = m
		found("bank_ifsc")
	}
	if m := reAccount.FindStringSubmatch(text); m != nil {
		f.BankAccount = strings.ReplaceAll(m[1], " ", "")
		found("bank_account")
	}
	if m := reBank.FindStringSubmatch(text); m != nil {
		n := strings.TrimSpace(m[1])
		if i := strings.Index(n, "  "); i > 0 {
			n = n[:i]
		}
		if i := strings.Index(strings.ToLower(n), " a/c"); i > 0 {
			n = n[:i]
		}
		if i := strings.Index(strings.ToLower(n), " account"); i > 0 {
			n = n[:i]
		}
		if i := strings.Index(strings.ToLower(n), " ifsc"); i > 0 {
			n = n[:i]
		}
		f.BankName = strings.TrimSpace(n)
		found("bank_name")
	}

	// Bill number and dates.
	for _, l := range lines {
		if m := reBillNo.FindStringSubmatch(l); m != nil && f.BillNo == "" {
			no := strings.Trim(m[1], ".-_/")
			if lo := strings.ToLower(no); lo != "date" && lo != "dated" && no != "" && strings.ContainsAny(no, "0123456789") {
				f.BillNo = no
				found("bill_no")
			}
		}
		if f.DueDate == "" {
			if m := reDueLbl.FindStringSubmatch(l); m != nil {
				if d, ok := ParseDate(m[1]); ok {
					f.DueDate = d
					found("due_date")
					continue
				}
			}
		}
		if f.BillDate == "" && !strings.Contains(strings.ToLower(l), "due") {
			if m := reDateLbl.FindStringSubmatch(l); m != nil {
				if d, ok := ParseDate(m[1]); ok {
					f.BillDate = d
					found("bill_date")
				}
			}
		}
	}
	if f.BillDate == "" {
		if d, ok := ParseDate(reDateAny.FindString(text)); ok {
			f.BillDate = d
			f.Warnings = append(f.Warnings, "The bill date was taken as the first date on the bill; check it.")
		}
	}

	// Amounts: on each labelled line the last figure is the amount. Where a
	// label appears more than once (item lines, then the summary) the
	// largest is the bill's.
	labels := []amountLabel{
		{&f.IGST, "igst", regexp.MustCompile(`(?i)\b(igst|integrated\s+tax)\b`)},
		{&f.CGST, "cgst", regexp.MustCompile(`(?i)\b(cgst|central\s+tax)\b`)},
		{&f.SGST, "sgst", regexp.MustCompile(`(?i)\b(sgst|utgst|state\s+tax|ut\s+tax)\b`)},
		{&f.Taxable, "taxable", regexp.MustCompile(`(?i)\b(taxable\s+(value|amount)|sub\s*-?\s*total|assessable\s+value|amount\s+before\s+tax|total\s+before\s+tax)\b`)},
		{&f.Total, "total", regexp.MustCompile(`(?i)\b(grand\s+total|invoice\s+total|total\s+invoice\s+(value|amount)|total\s+amount|net\s+amount|amount\s+payable|net\s+payable|balance\s+due|total)\b`)},
	}
	best := map[string]money.Amount{}
	for _, l := range lines {
		lo := strings.ToLower(l)
		for _, lb := range labels {
			if !lb.re.MatchString(l) {
				continue
			}
			if lb.name == "total" && (strings.Contains(lo, "tax amount") || strings.Contains(lo, "total tax") || strings.Contains(lo, "in words") || strings.Contains(lo, "gst")) {
				continue
			}
			if lb.name == "taxable" || lb.name == "total" {
				// A taxable/total line that also names a tax is a table header.
				if regexp.MustCompile(`(?i)\b(cgst|sgst|igst)\b`).MatchString(l) {
					continue
				}
			}
			if a, ok := lastAmount(l); ok && a > best[lb.name] {
				best[lb.name] = a
			}
			break
		}
	}
	for _, lb := range labels {
		if a := best[lb.name]; a > 0 {
			*lb.field = a
			found(lb.name)
		}
	}
	f.settleAmounts()
	return f
}

// settleAmounts fills what the other figures imply and checks they add up.
func (f *Fields) settleAmounts() {
	gst := f.GST()
	switch {
	case f.Taxable == 0 && f.Total > 0 && gst > 0 && f.Total > gst:
		f.Taxable = f.Total - gst
		f.Warnings = append(f.Warnings, "The taxable value was worked out as total less GST.")
	case f.Total == 0 && f.Taxable > 0:
		f.Total = f.Taxable + gst
		f.Warnings = append(f.Warnings, "The total was worked out as taxable value plus GST.")
	}
	if f.Taxable > 0 && f.Total > 0 {
		diff := f.Total - (f.Taxable + gst)
		if diff < 0 {
			diff = -diff
		}
		if diff > 100 { // more than ₹1 (rounding off)
			f.Warnings = append(f.Warnings, fmt.Sprintf("Taxable value %s plus GST %s does not make the total %s: check the figures (TDS, round-off or other charges may be on the bill).", f.Taxable, gst, f.Total))
		}
	}
	if f.IGST > 0 && (f.CGST > 0 || f.SGST > 0) {
		f.Warnings = append(f.Warnings, "The bill shows both IGST and CGST/SGST: check which applies.")
	}
	if f.CGST != f.SGST && f.CGST > 0 && f.SGST > 0 {
		f.Warnings = append(f.Warnings, "CGST and SGST should be equal.")
	}
}

func stripLabel(l string) string {
	for _, p := range []string{"from:", "from :", "seller:", "supplier:", "sold by:", "vendor:", "billed by:"} {
		if strings.HasPrefix(strings.ToLower(l), p) {
			return strings.TrimSpace(l[len(p):])
		}
	}
	return l
}

func cleanName(s string) string {
	if i := strings.Index(strings.ToUpper(s), "GSTIN"); i > 0 {
		s = s[:i]
	}
	return strings.Trim(strings.TrimSpace(s), ",:-")
}

func letters(s string) int {
	n := 0
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			n++
		}
	}
	return n
}

// ---- AI reading ----

// AIInstruction asks a model for a bill's details as JSON.
const AIInstruction = `This is a vendor bill (tax invoice) received by an Indian stock broking firm. Read it and reply with ONLY a JSON object, no other text, with these keys (use "" or 0 when a value is not on the bill; never guess):
{"vendor_name":"","vendor_gstin":"","vendor_pan":"","vendor_address":"","vendor_email":"","vendor_phone":"","buyer_gstin":"","bank_name":"","bank_ifsc":"","bank_account":"","bill_no":"","bill_date":"YYYY-MM-DD","due_date":"YYYY-MM-DD","taxable":0,"cgst":0,"sgst":0,"igst":0,"total":0}
Amounts are rupees as numbers (e.g. 11800.00). The vendor is the business that issued the bill, not the buyer. Dates on Indian bills are day first.`

// ParseAI reads a model's JSON reply and checks it like text reading.
func ParseAI(reply, firmGSTIN string) (Fields, error) {
	s := strings.TrimSpace(reply)
	if i := strings.Index(s, "{"); i >= 0 {
		s = s[i:]
	}
	if j := strings.LastIndex(s, "}"); j >= 0 {
		s = s[:j+1]
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		return Fields{}, fmt.Errorf("billscan: the AI reply was not the expected JSON")
	}
	f := Fields{Source: "ai", Found: []string{}, Warnings: []string{}}
	str := func(k string) string {
		v, _ := raw[k].(string)
		v = strings.TrimSpace(v)
		if v != "" {
			f.Found = append(f.Found, k)
		}
		return v
	}
	amt := func(k string) money.Amount {
		var a money.Amount
		switch v := raw[k].(type) {
		case float64:
			a, _ = money.ParseAmount(fmt.Sprintf("%.2f", v))
		case string:
			a, _ = money.ParseAmount(strings.NewReplacer(",", "", "₹", "", " ", "").Replace(v))
		}
		if a > 0 {
			f.Found = append(f.Found, k)
		} else {
			a = 0
		}
		return a
	}
	f.VendorName, f.VendorGSTIN, f.VendorPAN = str("vendor_name"), strings.ToUpper(str("vendor_gstin")), strings.ToUpper(str("vendor_pan"))
	f.VendorAddress, f.VendorEmail, f.VendorPhone = str("vendor_address"), strings.ToLower(str("vendor_email")), str("vendor_phone")
	f.BuyerGSTIN = strings.ToUpper(str("buyer_gstin"))
	f.BankName, f.BankIFSC, f.BankAccount = str("bank_name"), strings.ToUpper(str("bank_ifsc")), strings.ReplaceAll(str("bank_account"), " ", "")
	f.BillNo = str("bill_no")
	for _, d := range []struct {
		k   string
		dst *string
	}{{"bill_date", &f.BillDate}, {"due_date", &f.DueDate}} {
		if v := str(d.k); v != "" {
			if t, err := time.Parse("2006-01-02", v); err == nil {
				*d.dst = t.Format("2006-01-02")
			} else if p, ok := ParseDate(v); ok {
				*d.dst = p
			}
		}
	}
	f.Taxable, f.CGST, f.SGST, f.IGST, f.Total = amt("taxable"), amt("cgst"), amt("sgst"), amt("igst"), amt("total")

	firmGSTIN = strings.ToUpper(strings.TrimSpace(firmGSTIN))
	if f.VendorGSTIN != "" && f.VendorGSTIN == firmGSTIN && f.BuyerGSTIN != "" {
		f.VendorGSTIN, f.BuyerGSTIN = f.BuyerGSTIN, f.VendorGSTIN
	}
	if f.VendorGSTIN != "" {
		if !ValidGSTIN(f.VendorGSTIN) {
			f.Warnings = append(f.Warnings, "The vendor GSTIN "+f.VendorGSTIN+" read from the bill fails the GSTIN check: check it against the bill.")
		} else {
			f.VendorState = GSTINState(f.VendorGSTIN)
			if f.VendorPAN == "" {
				f.VendorPAN = GSTINPAN(f.VendorGSTIN)
			}
		}
	}
	if f.VendorPAN != "" && !ValidPAN(f.VendorPAN) {
		f.Warnings = append(f.Warnings, "The PAN read from the bill is not in PAN form.")
	}
	if f.BankIFSC != "" && !ValidIFSC(f.BankIFSC) {
		f.Warnings = append(f.Warnings, "The IFSC read from the bill is not in IFSC form.")
	}
	if firmGSTIN != "" && f.BuyerGSTIN != "" && f.BuyerGSTIN != firmGSTIN {
		f.Warnings = append(f.Warnings, "The bill is made out to GSTIN "+f.BuyerGSTIN+", not the firm's "+firmGSTIN+": input tax credit needs the firm's GSTIN on the bill.")
	}
	f.settleAmounts()
	sort.Strings(f.Found)
	return f, nil
}
