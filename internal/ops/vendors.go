package ops

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/billscan"
	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// ---- vendors and the bills the firm pays ----
//
// The vendor master holds who the firm buys from: GSTIN (checked), PAN,
// bank account, the TDS section their payments fall under and whether they
// are a micro or small enterprise (MSME), whose bills must be paid within
// 45 days (15 without a written agreement) or the expense is disallowed
// until paid (Income-tax Act section 43B(h)). Each vendor has a ledger
// account, PAYABLE:VENDOR:<id>.
//
// A vendor bill is booked as a journal voucher, made by one person and
// posted by another like every voucher: expense and input GST debited,
// TDS and the vendor credited (reverse charge credits GST payable instead
// of the vendor). Paying it makes a payment voucher from the firm's own
// bank. The bill register follows each bill from booking to payment.

const (
	cVendor     = "vendor"
	cVendorBill = "vendorbill"
	cBillScan   = "billscan"

	// PayableRCM is GST the firm pays itself on reverse-charge purchases.
	PayableRCM = "PAYABLE:GST_RCM"
	// MaxBillBytes caps a scanned bill.
	MaxBillBytes = 10 << 20
)

// TDSSections are the sections vendor payments usually fall under, with
// the usual rate (%). Rates are suggestions; the person booking the bill
// can change the TDS on any bill (thresholds, lower-deduction certificates).
var TDSSections = []TDSSection{
	{"", "No TDS", ""},
	{"194C", "194C Contractors (1% individual or HUF, 2% others)", "2"},
	{"194J", "194J Professional fees", "10"},
	{"194JA", "194J Technical services, call centres", "2"},
	{"194I", "194I Rent of land, building, furniture", "10"},
	{"194IPM", "194I Rent of plant and machinery", "2"},
	{"194H", "194H Commission or brokerage", "2"},
	{"194Q", "194Q Purchase of goods", "0.1"},
}

// TDSSection is a TDS section and its usual rate.
type TDSSection struct {
	Code  string `json:"code"`
	Label string `json:"label"`
	Rate  string `json:"rate"`
}

func tdsSection(code string) (TDSSection, bool) {
	for _, s := range TDSSections {
		if s.Code == code {
			return s, true
		}
	}
	return TDSSection{}, false
}

// tdsRate is the usual rate for a vendor: 20% without a PAN (206AA), 1%
// under 194C for an individual or HUF.
func tdsRate(section, pan string) string {
	s, _ := tdsSection(section)
	if section == "" {
		return ""
	}
	if pan == "" {
		return "20"
	}
	if section == "194C" && len(pan) == 10 && (pan[3] == 'P' || pan[3] == 'H') {
		return "1"
	}
	return s.Rate
}

// Vendor is someone the firm buys from.
type Vendor struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	GSTIN      string `json:"gstin"`
	PAN        string `json:"pan"`
	State      string `json:"state"`
	Address    string `json:"address"`
	Email      string `json:"email"`
	Phone      string `json:"phone"`
	Contact    string `json:"contact"`
	BankName   string `json:"bank_name"`
	IFSC       string `json:"ifsc"`
	Account    string `json:"bank_account"`
	TDSSection string `json:"tds_section"`
	TDSRate    string `json:"tds_rate"` // %, blank for the usual rate
	MSME       string `json:"msme"`     // Udyam registration number
	MSMEType   string `json:"msme_type"`
	TermsDays  int64  `json:"terms_days"`
	Expense    string `json:"expense"` // usual expense account
	Ledger     string `json:"ledger"`  // PAYABLE:VENDOR:<id>
	Active     bool   `json:"active"`
	Notes      string `json:"notes"`
	Created    string `json:"created"`
	By         string `json:"by"`

	// Filled when listed.
	Balance  money.Amount `json:"balance"`  // owed to the vendor (credit positive)
	Bills    int          `json:"bills"`    // bills booked
	Overdue  money.Amount `json:"overdue"`  // unpaid past due
	Billed12 money.Amount `json:"billed12"` // billed in the last 12 months
}

var (
	reIFSCv    = regexp.MustCompile(`^[A-Z]{4}0[A-Z0-9]{6}$`)
	reUdyam    = regexp.MustCompile(`^UDYAM-[A-Z]{2}-\d{2}-\d{7}$`)
	reAcctNo   = regexp.MustCompile(`^\d{6,18}$`)
	reEmailLax = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
)

// msmeSmall says whether 43B(h) applies: micro and small enterprises only.
func (v Vendor) msmeSmall() bool {
	return v.MSME != "" && (v.MSMEType == "Micro" || v.MSMEType == "Small")
}

// payBy is the last day an MSME bill may be paid: the agreed days up to
// 45, or 15 days without an agreement.
func (v Vendor) payBy(billDate string) string {
	if !v.msmeSmall() || billDate == "" {
		return ""
	}
	days := v.TermsDays
	if days <= 0 {
		days = 15
	}
	if days > 45 {
		days = 45
	}
	return addDays(billDate, int(days))
}

// Vendors lists the vendor master with what is owed to each.
func (o *Ops) Vendors(ctx context.Context, tenant, actor string) ([]Vendor, error) {
	if err := o.require(ctx, tenant, actor, "Viewing vendors", RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return nil, err
	}
	vs, err := docs.List[Vendor](ctx, o.Docs, tenant, cVendor, "")
	if err != nil {
		return nil, err
	}
	bills, err := o.vendorBills(ctx, tenant)
	if err != nil {
		return nil, err
	}
	today := o.now().Format("2006-01-02")
	yearAgo := addDays(today, -365)
	idx := map[string]int{}
	for i := range vs {
		idx[vs[i].ID] = i
	}
	for _, b := range bills {
		i, ok := idx[b.Vendor]
		if !ok || b.Status == BillRejected || b.Status == BillCancelled {
			continue
		}
		v := &vs[i]
		v.Bills++
		if b.BillDate >= yearAgo {
			v.Billed12 += b.Total
		}
		if b.Status == BillPosted || b.Status == BillPartPaid {
			v.Balance += b.Payable - b.Paid
			if b.dueBy() != "" && b.dueBy() < today {
				v.Overdue += b.Payable - b.Paid
			}
		}
	}
	sort.SliceStable(vs, func(i, j int) bool { return strings.ToLower(vs[i].Name) < strings.ToLower(vs[j].Name) })
	return vs, nil
}

// Vendor returns one vendor.
func (o *Ops) Vendor(ctx context.Context, tenant, id string) (Vendor, error) {
	v, _, err := docs.Get[Vendor](ctx, o.Docs, tenant, cVendor, id)
	return v, err
}

// SaveVendor adds or changes a vendor. A changed bank account is logged
// with the old and new last four digits: changed vendor bank details are
// the usual route of payment fraud.
func (o *Ops) SaveVendor(ctx context.Context, tenant, actor string, v Vendor) (Vendor, error) {
	if err := o.require(ctx, tenant, actor, "Changing vendors", RoleMaker, RoleChecker); err != nil {
		return v, err
	}
	v.Name = strings.Join(strings.Fields(v.Name), " ")
	v.GSTIN = strings.ToUpper(strings.TrimSpace(v.GSTIN))
	v.PAN = strings.ToUpper(strings.TrimSpace(v.PAN))
	v.IFSC = strings.ToUpper(strings.TrimSpace(v.IFSC))
	v.Account = strings.ReplaceAll(strings.TrimSpace(v.Account), " ", "")
	v.MSME = strings.ToUpper(strings.TrimSpace(v.MSME))
	v.Email = strings.ToLower(strings.TrimSpace(v.Email))
	v.Expense = strings.ToUpper(strings.TrimSpace(v.Expense))
	if v.Name == "" {
		return v, invalid("give the vendor's name")
	}
	if v.GSTIN != "" {
		if !billscan.ValidGSTIN(v.GSTIN) {
			return v, invalid("GSTIN %s is not valid (check digit or state code); check it on the GST portal", v.GSTIN)
		}
		if v.PAN == "" {
			v.PAN = billscan.GSTINPAN(v.GSTIN)
		} else if v.PAN != billscan.GSTINPAN(v.GSTIN) {
			return v, invalid("PAN %s does not match the PAN inside the GSTIN (%s)", v.PAN, billscan.GSTINPAN(v.GSTIN))
		}
		v.State = billscan.GSTINState(v.GSTIN)
	}
	if v.PAN != "" && !billscan.ValidPAN(v.PAN) {
		return v, invalid("PAN %s is not in PAN form (ABCDE1234F)", v.PAN)
	}
	if v.IFSC != "" && !reIFSCv.MatchString(v.IFSC) {
		return v, invalid("IFSC %s is not in IFSC form (four letters, 0, six letters or digits)", v.IFSC)
	}
	if v.Account != "" && !reAcctNo.MatchString(v.Account) {
		return v, invalid("the bank account number should be 6 to 18 digits")
	}
	if (v.Account == "") != (v.IFSC == "") {
		return v, invalid("give both the bank account number and the IFSC, or neither")
	}
	if v.Email != "" && !reEmailLax.MatchString(v.Email) {
		return v, invalid("the email address is not valid")
	}
	if v.MSME != "" {
		if !reUdyam.MatchString(v.MSME) {
			return v, invalid("the Udyam registration number looks like UDYAM-MH-01-0001234")
		}
		if v.MSMEType != "Micro" && v.MSMEType != "Small" && v.MSMEType != "Medium" {
			return v, invalid("choose Micro, Small or Medium for an MSME vendor")
		}
	} else {
		v.MSMEType = ""
	}
	if v.TermsDays < 0 || v.TermsDays > 365 {
		return v, invalid("payment terms must be 0 to 365 days")
	}
	if _, ok := tdsSection(v.TDSSection); !ok {
		return v, invalid("unknown TDS section %s", v.TDSSection)
	}
	if v.TDSRate != "" {
		r, err := money.ParseDec(v.TDSRate)
		if err != nil || r.Sign() < 0 || r.Cmp(money.DecInt(30)) > 0 {
			return v, invalid("the TDS rate must be a percentage from 0 to 30")
		}
	}
	if v.Expense != "" {
		if t, err := ledger.TypeOf(v.Expense); err != nil || (t != ledger.Expense && !strings.HasPrefix(v.Expense, "ASSET:")) {
			return v, invalid("the usual account must be an EXPENSE: or ASSET: account")
		}
	}

	all, err := docs.List[Vendor](ctx, o.Docs, tenant, cVendor, "")
	if err != nil {
		return v, err
	}
	var old *Vendor
	for i := range all {
		if all[i].ID == v.ID && v.ID != "" {
			old = &all[i]
			continue
		}
		if v.GSTIN != "" && all[i].GSTIN == v.GSTIN {
			return v, invalid("%s already has GSTIN %s (vendor %s)", all[i].Name, v.GSTIN, all[i].ID)
		}
	}
	if v.ID != "" && old == nil {
		return v, fmt.Errorf("%w: vendor %s", ErrNotFound, v.ID)
	}
	if old == nil {
		n, err := o.next(ctx, tenant, "vendor", actor)
		if err != nil {
			return v, err
		}
		v.ID = fmt.Sprintf("V%04d", n)
		v.Ledger = "PAYABLE:VENDOR:" + v.ID
		v.Created, v.By, v.Active = o.now().Format(time.RFC3339), actor, true
		if err := docs.Upsert(ctx, o.Docs, tenant, cAccount, v.Ledger, Account{Code: v.Ledger, Name: "Vendor · " + v.Name,
			Type: accountType(v.Ledger), Group: accountGroup(v.Ledger), System: true, AllowedUsers: []string{}}, actor); err != nil {
			return v, err
		}
	} else {
		v.Ledger, v.Created, v.By = old.Ledger, old.Created, old.By
	}
	v.Balance, v.Bills, v.Overdue, v.Billed12 = 0, 0, 0, 0
	if err := docs.Upsert(ctx, o.Docs, tenant, cVendor, v.ID, v, actor); err != nil {
		return v, err
	}
	detail := map[string]any{"name": v.Name, "gstin": v.GSTIN}
	if old != nil && (old.Account != v.Account || old.IFSC != v.IFSC) {
		detail["bank_changed"] = fmt.Sprintf("%s → %s", last4(old.Account), last4(v.Account))
		o.log(ctx, tenant, actor, "vendor.bank_changed", v.ID, detail)
	}
	if old == nil {
		o.log(ctx, tenant, actor, "vendor.added", v.ID, detail)
	} else {
		o.log(ctx, tenant, actor, "vendor.changed", v.ID, detail)
	}
	return v, nil
}

func last4(s string) string {
	if s == "" {
		return "none"
	}
	if len(s) <= 4 {
		return "…" + s
	}
	return "…" + s[len(s)-4:]
}

// Bill statuses.
const (
	BillPending   = "Awaiting approval"
	BillPosted    = "Unpaid"
	BillPartPaid  = "Part paid"
	BillPaid      = "Paid"
	BillRejected  = "Rejected"
	BillCancelled = "Cancelled"
)

// BillPayment is a payment voucher against a bill.
type BillPayment struct {
	Voucher string       `json:"voucher"`
	Date    string       `json:"date"`
	Amount  money.Amount `json:"amount"`
	Status  string       `json:"status"` // the voucher's
}

// VendorBill is a bill the firm has received.
type VendorBill struct {
	ID          string        `json:"id"`
	Vendor      string        `json:"vendor"`
	VendorName  string        `json:"vendor_name"`
	VendorGSTIN string        `json:"vendor_gstin"`
	BillNo      string        `json:"bill_no"`
	BillDate    string        `json:"bill_date"`
	DueDate     string        `json:"due_date"`
	MSMEPayBy   string        `json:"msme_pay_by,omitempty"`
	Branch      string        `json:"branch,omitempty"`
	Expense     string        `json:"expense"`
	Narration   string        `json:"narration"`
	Taxable     money.Amount  `json:"taxable"`
	CGST        money.Amount  `json:"cgst"`
	SGST        money.Amount  `json:"sgst"`
	IGST        money.Amount  `json:"igst"`
	RCM         bool          `json:"rcm"` // reverse charge: the firm pays the GST
	ITC         bool          `json:"itc"` // input tax credit claimed
	TDSSection  string        `json:"tds_section"`
	TDS         money.Amount  `json:"tds"`
	Total       money.Amount  `json:"total"`   // the bill's value
	Payable     money.Amount  `json:"payable"` // owed to the vendor after TDS
	Paid        money.Amount  `json:"paid"`
	Payments    []BillPayment `json:"payments"`
	Voucher     string        `json:"voucher"`
	Status      string        `json:"status"`
	Scan        string        `json:"scan,omitempty"`
	ReadBy      string        `json:"read_by,omitempty"` // text, ai: how the scan was read
	Edited      []string      `json:"edited,omitempty"`  // fields changed from what was read
	Warnings    []string      `json:"warnings"`
	Maker       string        `json:"maker"`
	At          string        `json:"at"`
}

func (b VendorBill) gst() money.Amount { return b.CGST + b.SGST + b.IGST }

// dueBy is the earlier of the due date and the MSME limit.
func (b VendorBill) dueBy() string {
	d := b.DueDate
	if b.MSMEPayBy != "" && (d == "" || b.MSMEPayBy < d) {
		d = b.MSMEPayBy
	}
	return d
}

func billNoKey(s string) string {
	return strings.ToUpper(strings.NewReplacer(" ", "", "-", "", "/", "", ".", "").Replace(s))
}

// vendorBills lists bills with their status worked out from the vouchers.
func (o *Ops) vendorBills(ctx context.Context, tenant string) ([]VendorBill, error) {
	bs, err := docs.List[VendorBill](ctx, o.Docs, tenant, cVendorBill, "")
	if err != nil {
		return nil, err
	}
	vch := map[string]Voucher{}
	if vs, err := docs.List[Voucher](ctx, o.Docs, tenant, cVoucher, ""); err == nil {
		for _, v := range vs {
			vch[v.No] = v
		}
	}
	for i := range bs {
		b := &bs[i]
		if b.Payments == nil {
			b.Payments = []BillPayment{}
		}
		if b.Warnings == nil {
			b.Warnings = []string{}
		}
		switch vch[b.Voucher].Status {
		case "Pending":
			b.Status = BillPending
		case "Rejected":
			b.Status = BillRejected
		case "Cancelled":
			b.Status = BillCancelled
		case "Posted":
			b.Paid = 0
			for j := range b.Payments {
				p := &b.Payments[j]
				p.Status = vch[p.Voucher].Status
				if p.Status == "Posted" {
					b.Paid += p.Amount
				}
			}
			switch {
			case b.Paid >= b.Payable:
				b.Status = BillPaid
			case b.Paid > 0:
				b.Status = BillPartPaid
			default:
				b.Status = BillPosted
			}
		}
	}
	return bs, nil
}

// BillRegister is the bill register for a period.
type BillRegister struct {
	From      string       `json:"from"`
	To        string       `json:"to"`
	Bills     []VendorBill `json:"bills"`
	Taxable   money.Amount `json:"taxable"`
	GST       money.Amount `json:"gst"`
	ITC       money.Amount `json:"itc"`
	TDS       money.Amount `json:"tds"`
	Unpaid    money.Amount `json:"unpaid"`
	Overdue   money.Amount `json:"overdue"`
	MSMELate  int          `json:"msme_late"` // MSME bills unpaid past the 43B(h) limit
	MSMESoon  int          `json:"msme_soon"` // due within 7 days
	Pending   int          `json:"pending"`   // awaiting approval
	TDSBySect []TDSLine    `json:"tds_by_section"`
}

// TDSLine is TDS deducted under one section.
type TDSLine struct {
	Section string       `json:"section"`
	Bills   int          `json:"bills"`
	Amount  money.Amount `json:"amount"` // paid or credited
	TDS     money.Amount `json:"tds"`
}

// VendorBills is the bill register: bills dated in the period, newest first.
func (o *Ops) VendorBills(ctx context.Context, tenant, actor, from, to string) (BillRegister, error) {
	if err := o.require(ctx, tenant, actor, "Viewing vendor bills", RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return BillRegister{}, err
	}
	if from == "" {
		from = fyStartOf(o.now().Format("2006-01-02"))
	}
	if to == "" {
		to = o.now().Format("2006-01-02")
	}
	all, err := o.vendorBills(ctx, tenant)
	if err != nil {
		return BillRegister{}, err
	}
	today := o.now().Format("2006-01-02")
	r := BillRegister{From: from, To: to, Bills: []VendorBill{}, TDSBySect: []TDSLine{}}
	sect := map[string]*TDSLine{}
	for _, b := range all {
		open := b.Status == BillPosted || b.Status == BillPartPaid
		// Unpaid bills are always shown, whatever their date.
		if (b.BillDate < from || b.BillDate > to) && !(open || b.Status == BillPending) {
			continue
		}
		r.Bills = append(r.Bills, b)
		if b.Status == BillPending {
			r.Pending++
		}
		if open {
			r.Unpaid += b.Payable - b.Paid
			if d := b.dueBy(); d != "" && d < today {
				r.Overdue += b.Payable - b.Paid
			}
			if b.MSMEPayBy != "" {
				if b.MSMEPayBy < today {
					r.MSMELate++
				} else if b.MSMEPayBy <= addDays(today, 7) {
					r.MSMESoon++
				}
			}
		}
		if b.BillDate < from || b.BillDate > to || b.Status == BillRejected || b.Status == BillCancelled {
			continue
		}
		r.Taxable += b.Taxable
		r.GST += b.gst()
		if b.ITC {
			r.ITC += b.gst()
		}
		r.TDS += b.TDS
		if b.TDSSection != "" && b.TDS > 0 {
			l := sect[b.TDSSection]
			if l == nil {
				l = &TDSLine{Section: b.TDSSection}
				sect[b.TDSSection] = l
			}
			l.Bills++
			l.Amount += b.Taxable
			l.TDS += b.TDS
		}
	}
	for _, l := range sect {
		r.TDSBySect = append(r.TDSBySect, *l)
	}
	sort.Slice(r.TDSBySect, func(i, j int) bool { return r.TDSBySect[i].Section < r.TDSBySect[j].Section })
	sort.SliceStable(r.Bills, func(i, j int) bool {
		if r.Bills[i].BillDate != r.Bills[j].BillDate {
			return r.Bills[i].BillDate > r.Bills[j].BillDate
		}
		return r.Bills[i].ID > r.Bills[j].ID
	})
	return r, nil
}

// BillRegisterCSV is the register as a spreadsheet, with the TDS columns
// needed for the quarterly TDS return (26Q).
func (o *Ops) BillRegisterCSV(ctx context.Context, tenant, actor, from, to string) ([]byte, error) {
	r, err := o.VendorBills(ctx, tenant, actor, from, to)
	if err != nil {
		return nil, err
	}
	vs, _ := docs.List[Vendor](ctx, o.Docs, tenant, cVendor, "")
	pan := map[string]string{}
	for _, v := range vs {
		pan[v.ID] = v.PAN
	}
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	w.Write([]string{"Bill", "Vendor", "VendorName", "GSTIN", "PAN", "BillNo", "BillDate", "DueDate", "MSMEPayBy", "Expense", "Taxable", "CGST", "SGST", "IGST", "RCM", "ITC", "Total", "TDSSection", "TDS", "Payable", "Paid", "Status", "Voucher"})
	for _, b := range r.Bills {
		yn := func(x bool) string {
			if x {
				return "Y"
			}
			return "N"
		}
		w.Write([]string{b.ID, b.Vendor, b.VendorName, b.VendorGSTIN, pan[b.Vendor], b.BillNo, b.BillDate, b.DueDate, b.MSMEPayBy, b.Expense,
			b.Taxable.String(), b.CGST.String(), b.SGST.String(), b.IGST.String(), yn(b.RCM), yn(b.ITC), b.Total.String(), b.TDSSection, b.TDS.String(),
			b.Payable.String(), b.Paid.String(), b.Status, b.Voucher})
	}
	w.Flush()
	return buf.Bytes(), w.Error()
}

// SaveVendorBill checks a bill and books it as a journal voucher for an
// Ops checker to post.
func (o *Ops) SaveVendorBill(ctx context.Context, tenant, actor string, b VendorBill) (VendorBill, error) {
	if err := o.require(ctx, tenant, actor, "Booking vendor bills", RoleMaker, RoleChecker); err != nil {
		return b, err
	}
	v, err := o.Vendor(ctx, tenant, b.Vendor)
	if err != nil {
		return b, invalid("choose a vendor from the vendor master (or add the vendor first)")
	}
	if !v.Active {
		return b, invalid("vendor %s is inactive", v.Name)
	}
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return b, err
	}
	b.BillNo = strings.TrimSpace(b.BillNo)
	b.Expense = strings.ToUpper(strings.TrimSpace(b.Expense))
	if b.Expense == "" {
		b.Expense = v.Expense
	}
	switch {
	case b.BillNo == "":
		return b, invalid("give the vendor's bill number")
	case !ledger.ValidDate(b.BillDate):
		return b, invalid("the bill date must be YYYY-MM-DD")
	case b.DueDate != "" && !ledger.ValidDate(b.DueDate):
		return b, invalid("the due date must be YYYY-MM-DD")
	case b.DueDate != "" && b.DueDate < b.BillDate:
		return b, invalid("the due date is before the bill date")
	case b.BillDate > o.now().Format("2006-01-02"):
		return b, invalid("the bill date is in the future")
	case b.Taxable <= 0:
		return b, invalid("give the taxable value (the bill before GST)")
	case b.CGST < 0 || b.SGST < 0 || b.IGST < 0 || b.TDS < 0:
		return b, invalid("amounts cannot be negative")
	case b.CGST != b.SGST:
		return b, invalid("CGST and SGST must be equal")
	case b.IGST > 0 && b.CGST > 0:
		return b, invalid("a bill has either IGST or CGST and SGST, not both")
	}
	if t, err := ledger.TypeOf(b.Expense); err != nil || (t != ledger.Expense && !strings.HasPrefix(b.Expense, "ASSET:")) {
		return b, invalid("choose the expense (or asset) account the bill is for")
	}
	if !o.AccountVisible(ctx, tenant, actor)(b.Expense) {
		return b, fmt.Errorf("%w: you cannot post to %s", ErrForbidden, b.Expense)
	}
	if _, ok := tdsSection(b.TDSSection); !ok {
		return b, invalid("unknown TDS section %s", b.TDSSection)
	}
	if b.TDSSection == "" && b.TDS > 0 {
		return b, invalid("choose the TDS section for the TDS deducted")
	}
	if b.TDS > b.Taxable {
		return b, invalid("TDS is more than the taxable value")
	}
	gst := b.gst()
	if gst > b.Taxable.Dec().Pct(money.DecInt(28)).Round(money.UnitRupee)+100 {
		return b, invalid("GST of %s is more than 28%% of the taxable value", gst)
	}
	if b.RCM && gst == 0 {
		return b, invalid("for reverse charge, enter the GST the firm pays on the bill")
	}
	if gst == 0 {
		b.ITC = false
	}

	// No bill twice.
	all, err := o.vendorBills(ctx, tenant)
	if err != nil {
		return b, err
	}
	for _, x := range all {
		if x.Vendor == v.ID && billNoKey(x.BillNo) == billNoKey(b.BillNo) && x.Status != BillRejected && x.Status != BillCancelled {
			return b, fmt.Errorf("%w: bill %s of %s is already booked (%s)", ErrState, b.BillNo, v.Name, x.ID)
		}
	}

	// Checks that need a person's attention but do not stop the bill.
	b.Warnings = []string{}
	if gst > 0 && set.FirmState != "" && v.State != "" {
		same := strings.EqualFold(v.State, set.FirmState)
		if same && b.IGST > 0 {
			b.Warnings = append(b.Warnings, "The vendor is in "+v.State+", the firm's state, but the bill charges IGST: usually CGST and SGST apply (unless the place of supply is another state).")
		}
		if !same && b.CGST > 0 {
			b.Warnings = append(b.Warnings, "The vendor is in "+v.State+" and the firm in "+set.FirmState+", but the bill charges CGST and SGST: usually IGST applies (hotel and event bills are an exception).")
		}
	}
	if b.ITC && v.GSTIN == "" {
		b.Warnings = append(b.Warnings, "The vendor has no GSTIN in the vendor master: input tax credit needs the vendor's GSTIN.")
	}
	if b.ITC && set.FirmGSTIN == "" {
		b.Warnings = append(b.Warnings, "The firm's GSTIN is not set in Settings.")
	}
	if v.TDSSection != "" && b.TDSSection == "" {
		b.Warnings = append(b.Warnings, "The vendor's payments usually fall under "+v.TDSSection+" but no TDS was deducted: check the threshold or the lower-deduction certificate.")
	}
	if b.TDSSection != "" && b.TDS > 0 {
		rate := v.TDSRate
		if rate == "" || b.TDSSection != v.TDSSection {
			rate = tdsRate(b.TDSSection, v.PAN)
		}
		if r, err := money.ParseDec(rate); err == nil {
			want := b.Taxable.Dec().Pct(r).Round(money.UnitRupee)
			if d := want - b.TDS; d > 100 || d < -100 {
				b.Warnings = append(b.Warnings, fmt.Sprintf("TDS at %s%% of the taxable value would be %s; %s was entered.", rate, want, b.TDS))
			}
		}
	}

	b.Total = b.Taxable + gst
	if b.RCM {
		b.Total = b.Taxable // the vendor does not charge the GST
	}
	b.Payable = b.Total - b.TDS
	b.VendorName, b.VendorGSTIN = v.Name, v.GSTIN
	b.MSMEPayBy = v.payBy(b.BillDate)
	if b.DueDate == "" {
		days := v.TermsDays
		if days <= 0 {
			days = 30
		}
		b.DueDate = addDays(b.BillDate, int(days))
	}
	if b.MSMEPayBy != "" && b.DueDate > b.MSMEPayBy {
		b.Warnings = append(b.Warnings, "The vendor is a "+strings.ToLower(v.MSMEType)+" enterprise: pay by "+b.MSMEPayBy+" or the expense is disallowed until paid (section 43B(h)).")
	}

	// The journal voucher.
	var lines []VoucherLine
	expense := b.Taxable
	if !b.ITC {
		expense += gst
	}
	lines = append(lines, VoucherLine{Account: b.Expense, Debit: expense, Memo: "Bill " + b.BillNo})
	if b.ITC {
		for _, g := range []struct {
			acct string
			amt  money.Amount
		}{{InputIGST, b.IGST}, {InputCGST, b.CGST}, {InputSGST, b.SGST}} {
			if g.amt > 0 {
				lines = append(lines, VoucherLine{Account: g.acct, Debit: g.amt})
			}
		}
	}
	if b.RCM {
		lines = append(lines, VoucherLine{Account: PayableRCM, Credit: gst, Memo: "GST on reverse charge"})
	}
	if b.TDS > 0 {
		lines = append(lines, VoucherLine{Account: "PAYABLE:TDS:" + b.TDSSection, Credit: b.TDS, Memo: "TDS " + b.TDSSection})
	}
	if b.Payable > 0 {
		lines = append(lines, VoucherLine{Account: v.Ledger, Credit: b.Payable})
	}
	narr := fmt.Sprintf("Bill %s dated %s", b.BillNo, b.BillDate)
	if s := strings.TrimSpace(b.Narration); s != "" {
		narr += " · " + s
	}
	n, err := o.next(ctx, tenant, "vendorbill", actor)
	if err != nil {
		return b, err
	}
	b.ID = fmt.Sprintf("VB-%06d", n)
	vch, err := o.SaveVoucher(ctx, tenant, actor, Voucher{Type: "Journal", Date: b.BillDate, Branch: b.Branch, Party: v.Name, Ref: b.BillNo, Narration: narr, Lines: lines, Bill: b.ID})
	if err != nil {
		return b, err
	}
	b.Voucher, b.Status, b.Maker, b.At, b.Paid, b.Payments = vch.No, BillPending, actor, o.now().Format(time.RFC3339), 0, []BillPayment{}
	if b.Scan != "" {
		if sc, _, err := docs.Get[BillScanDoc](ctx, o.Docs, tenant, cBillScan, b.Scan); err == nil {
			b.ReadBy = sc.Method
			b.Edited = editedFields(sc.Fields, b, v)
			sc.Bill = b.ID
			docs.Upsert(ctx, o.Docs, tenant, cBillScan, sc.ID, sc, actor)
		} else {
			b.Scan = ""
		}
	}
	if _, err := docs.Put(ctx, o.Docs, tenant, cVendorBill, b.ID, b, 0, actor); err != nil {
		return b, err
	}
	o.log(ctx, tenant, actor, "vendorbill.booked", b.ID, map[string]any{"vendor": v.ID, "bill_no": b.BillNo, "total": b.Total.String(), "voucher": vch.No, "read_by": b.ReadBy, "edited": b.Edited})
	return b, nil
}

// editedFields lists what the person changed from what was read off the
// scan (for the audit trail and to see how well bills are read).
func editedFields(f billscan.Fields, b VendorBill, v Vendor) []string {
	var out []string
	chk := func(name string, read, saved string) {
		if contains(f.Found, name) && !strings.EqualFold(strings.TrimSpace(read), strings.TrimSpace(saved)) {
			out = append(out, name)
		}
	}
	chk("vendor_gstin", f.VendorGSTIN, v.GSTIN)
	chk("bill_no", f.BillNo, b.BillNo)
	chk("bill_date", f.BillDate, b.BillDate)
	for _, a := range []struct {
		name       string
		read, save money.Amount
	}{{"taxable", f.Taxable, b.Taxable}, {"cgst", f.CGST, b.CGST}, {"sgst", f.SGST, b.SGST}, {"igst", f.IGST, b.IGST}} {
		if contains(f.Found, a.name) && a.read != a.save {
			out = append(out, a.name)
		}
	}
	return out
}

// PayVendorBill makes a payment voucher from the firm's own bank account.
func (o *Ops) PayVendorBill(ctx context.Context, tenant, actor, id, date, bank, ref string, amount money.Amount) (VendorBill, error) {
	if err := o.require(ctx, tenant, actor, "Paying vendor bills", RoleMaker, RoleChecker); err != nil {
		return VendorBill{}, err
	}
	all, err := o.vendorBills(ctx, tenant)
	if err != nil {
		return VendorBill{}, err
	}
	var b *VendorBill
	for i := range all {
		if all[i].ID == id {
			b = &all[i]
		}
	}
	if b == nil {
		return VendorBill{}, fmt.Errorf("%w: bill %s", ErrNotFound, id)
	}
	if b.Status != BillPosted && b.Status != BillPartPaid {
		return *b, fmt.Errorf("%w: bill %s is %s; a bill is paid once its voucher is posted", ErrState, id, strings.ToLower(b.Status))
	}
	pending := money.Amount(0)
	for _, p := range b.Payments {
		if p.Status == "Pending" {
			pending += p.Amount
		}
	}
	left := b.Payable - b.Paid - pending
	if amount == 0 {
		amount = left
	}
	if amount <= 0 || amount > left {
		return *b, invalid("the payment must be above zero and at most %s still to pay", left)
	}
	if bank == "" {
		bank = ledger.BankOwn
	}
	bank = strings.ToUpper(bank)
	if !strings.HasPrefix(bank, "BANK:") && !strings.HasPrefix(bank, "CASH:") || bank == ledger.BankClient {
		return *b, invalid("pay vendors from the firm's own bank account")
	}
	v, err := o.Vendor(ctx, tenant, b.Vendor)
	if err != nil {
		return *b, err
	}
	if strings.HasPrefix(bank, "BANK:") && v.Account == "" {
		return *b, invalid("add the vendor's bank account to the vendor master before paying by bank")
	}
	if date == "" {
		date = o.now().Format("2006-01-02")
	}
	narr := fmt.Sprintf("Payment of bill %s dated %s", b.BillNo, b.BillDate)
	if strings.HasPrefix(bank, "BANK:") {
		narr += fmt.Sprintf(" to a/c %s (%s)", last4(v.Account), v.IFSC)
	}
	vch, err := o.SaveVoucher(ctx, tenant, actor, Voucher{Type: "Payment", Date: date, Branch: b.Branch, Party: v.Name, Ref: ref, Narration: narr, Bill: b.ID,
		Lines: []VoucherLine{{Account: v.Ledger, Debit: amount, Memo: "Bill " + b.BillNo}, {Account: bank, Credit: amount}}})
	if err != nil {
		return *b, err
	}
	out, err := docs.Update(ctx, o.Docs, tenant, cVendorBill, b.ID, actor, func(x *VendorBill) error {
		x.Payments = append(x.Payments, BillPayment{Voucher: vch.No, Date: date, Amount: amount})
		return nil
	})
	if err != nil {
		return out, err
	}
	o.log(ctx, tenant, actor, "vendorbill.payment", b.ID, map[string]any{"voucher": vch.No, "amount": amount.String()})
	all, _ = o.vendorBills(ctx, tenant)
	for _, x := range all {
		if x.ID == b.ID {
			return x, nil
		}
	}
	return out, nil
}
