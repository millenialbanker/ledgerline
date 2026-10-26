package control

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/mail"
	"ledgerline/internal/money"
	"ledgerline/internal/pdf"
)

// ---- credit notes ----

// CreditNote reduces an issued invoice (GST section 34): a price agreed
// down, a service not given, an error found after issue. It has its own
// number series and carries GST the same way as the invoice.
type CreditNote struct {
	ID       string       `json:"id"`
	Number   string       `json:"number"`
	Invoice  string       `json:"invoice"`
	InvNo    string       `json:"invoice_number"`
	Customer string       `json:"customer"`
	Date     string       `json:"date"`
	Reason   string       `json:"reason"`
	Taxable  money.Amount `json:"taxable"`
	CGST     money.Amount `json:"cgst"`
	SGST     money.Amount `json:"sgst"`
	IGST     money.Amount `json:"igst"`
	Total    money.Amount `json:"total"`
	Bill     Customer     `json:"bill"`
	Vendor   Vendor       `json:"vendor"`
	GSTPct   string       `json:"gst_pct"`
	By       string       `json:"by"`
	Created  string       `json:"created"`
}

// IssueCreditNote credits part (or all) of an issued invoice. taxable is
// the value before GST; GST is worked out as on the invoice.
func (c *Control) IssueCreditNote(ctx context.Context, by, invoice string, taxable money.Amount, reason string) (CreditNote, error) {
	if err := c.need(ctx, by, RoleBilling); err != nil {
		return CreditNote{}, err
	}
	reason = clean(reason, 300)
	if reason == "" {
		return CreditNote{}, invalid("say why the credit is given; it is printed on the credit note")
	}
	if taxable <= 0 {
		return CreditNote{}, invalid("enter the amount to credit, before GST")
	}
	iv, _, err := docs.Get[Invoice](ctx, c.Docs, T, cInvoice, invoice)
	if err != nil {
		return CreditNote{}, ErrNotFound
	}
	if iv.Status == "Draft" || iv.Status == "Void" {
		return CreditNote{}, invalid("credit notes are for issued invoices; change or void a draft instead")
	}
	// What is already credited, before GST.
	prev, _ := c.creditNotesFor(ctx, iv.ID)
	var credited money.Amount
	for _, cn := range prev {
		credited += cn.Taxable
	}
	if credited+taxable > iv.Taxable {
		return CreditNote{}, invalid("at most ₹%s (before GST) is left to credit on %s", inr(iv.Taxable-credited), iv.Number)
	}
	cn := CreditNote{Invoice: iv.ID, InvNo: iv.Number, Customer: iv.Customer, Reason: reason, Taxable: taxable, Bill: iv.Bill, Vendor: iv.Vendor, GSTPct: iv.GSTPct, By: by, Created: c.stamp()}
	tax := pct(taxable, iv.GSTPct)
	if iv.IGST > 0 {
		cn.IGST = tax
	} else {
		cn.CGST = money.Amount(int64(tax) / 2)
		cn.SGST = tax - cn.CGST
	}
	cn.Total = taxable + tax
	d := c.now()
	cn.Date = d.Format("2006-01-02")
	f := fy(d)
	n, err := c.next(ctx, "cnno-"+f)
	if err != nil {
		return cn, err
	}
	set, _ := c.Settings(ctx)
	cn.Number = fmt.Sprintf("CN/%s/%04d", f, n)
	if p := set.Vendor.Prefix; p != "" {
		cn.Number = p + "-" + cn.Number
	}
	k, err := c.next(ctx, "credit")
	if err != nil {
		return cn, err
	}
	cn.ID = fmt.Sprintf("CRN-%05d", k)
	if err := docs.Upsert(ctx, c.Docs, T, cCredit, cn.ID, cn, by); err != nil {
		return cn, err
	}
	// The invoice's balance goes down; anything credited beyond what is
	// due is a refund owed (shown on the customer's statement).
	if _, err := docs.Update(ctx, c.Docs, T, cInvoice, iv.ID, by, func(x *Invoice) error {
		x.Credited += cn.Total
		x.settle()
		return nil
	}); err != nil {
		return cn, err
	}
	c.log(ctx, by, "creditnote.issue", cn.ID, cn.Number+" on "+iv.Number)
	return cn, nil
}

func (c *Control) creditNotesFor(ctx context.Context, invoice string) ([]CreditNote, error) {
	all, err := docs.List[CreditNote](ctx, c.Docs, T, cCredit, "")
	var out []CreditNote
	for _, cn := range all {
		if invoice == "" || cn.Invoice == invoice {
			out = append(out, cn)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, err
}

// CreditNotes lists credit notes (one invoice's, or all).
func (c *Control) CreditNotes(ctx context.Context, by, invoice string) ([]CreditNote, error) {
	if err := c.need(ctx, by, RoleBilling); err != nil {
		return nil, err
	}
	out, err := c.creditNotesFor(ctx, invoice)
	if out == nil {
		out = []CreditNote{}
	}
	return out, err
}

// ---- receivables: statement, ageing, TDS ----

// StatementLine is one entry on a customer's statement of account.
type StatementLine struct {
	Date    string       `json:"date"`
	Kind    string       `json:"kind"` // Invoice, Credit note, Payment, TDS
	Ref     string       `json:"ref"`
	Note    string       `json:"note,omitempty"`
	Debit   money.Amount `json:"debit"`
	Credit  money.Amount `json:"credit"`
	Balance money.Amount `json:"balance"`
}

// Ageing is what a customer owes by how late it is.
type Ageing struct {
	Customer string       `json:"customer"`
	Name     string       `json:"name"`
	Status   string       `json:"status"`
	NotDue   money.Amount `json:"not_due"`
	D30      money.Amount `json:"d1_30"`
	D60      money.Amount `json:"d31_60"`
	D90      money.Amount `json:"d61_90"`
	Over90   money.Amount `json:"over_90"`
	Total    money.Amount `json:"total"`
	Oldest   int          `json:"oldest_days"`
	LastPaid string       `json:"last_paid,omitempty"`
}

// StatementView is a customer's statement of account.
type StatementView struct {
	Customer Customer        `json:"customer"`
	Vendor   Vendor          `json:"vendor"`
	From     string          `json:"from"`
	To       string          `json:"to"`
	Opening  money.Amount    `json:"opening"`
	Lines    []StatementLine `json:"lines"`
	Closing  money.Amount    `json:"closing"`
	Ageing   Ageing          `json:"ageing"`
}

// Statement is a customer's account: invoices, credit notes and money
// received, with the running balance, for a period (from may be empty).
func (c *Control) Statement(ctx context.Context, by, customer, from, to string) (StatementView, error) {
	if err := c.need(ctx, by, RoleBilling); err != nil {
		return StatementView{}, err
	}
	cu, _, err := docs.Get[Customer](ctx, c.Docs, T, cCustomer, customer)
	if err != nil {
		return StatementView{}, ErrNotFound
	}
	if to == "" {
		to = c.today()
	}
	set, _ := c.Settings(ctx)
	v := StatementView{Customer: cu, Vendor: set.Vendor, From: from, To: to, Lines: []StatementLine{}}
	var all []StatementLine
	ivs, _ := docs.List[Invoice](ctx, c.Docs, T, cInvoice, "")
	for _, iv := range ivs {
		if iv.Customer != customer || iv.Status == "Draft" || iv.Status == "Void" {
			continue
		}
		all = append(all, StatementLine{Date: iv.Date, Kind: "Invoice", Ref: iv.Number, Note: monthName(iv.Period), Debit: iv.Total})
		for _, p := range iv.Payments {
			if p.Amount > 0 {
				all = append(all, StatementLine{Date: p.Date, Kind: "Payment", Ref: firstOf(p.Ref, p.Mode), Note: "against " + iv.Number, Credit: p.Amount})
			}
			if p.TDS > 0 {
				all = append(all, StatementLine{Date: p.Date, Kind: "TDS", Ref: firstOf(p.TDSCert, "certificate awaited"), Note: "deducted on " + iv.Number, Credit: p.TDS})
			}
		}
	}
	cns, _ := c.creditNotesFor(ctx, "")
	for _, cn := range cns {
		if cn.Customer == customer {
			all = append(all, StatementLine{Date: cn.Date, Kind: "Credit note", Ref: cn.Number, Note: "on " + cn.InvNo + ": " + cn.Reason, Credit: cn.Total})
		}
	}
	order := map[string]int{"Invoice": 0, "Credit note": 1, "TDS": 2, "Payment": 3}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Date != all[j].Date {
			return all[i].Date < all[j].Date
		}
		return order[all[i].Kind] < order[all[j].Kind]
	})
	bal := money.Amount(0)
	for _, l := range all {
		if l.Date > to {
			continue
		}
		bal += l.Debit - l.Credit
		if from != "" && l.Date < from {
			v.Opening = bal
			continue
		}
		l.Balance = bal
		v.Lines = append(v.Lines, l)
	}
	v.Closing = bal
	ag, _ := c.ageingFor(ctx, cu, ivs)
	v.Ageing = ag
	return v, nil
}

func (c *Control) ageingFor(ctx context.Context, cu Customer, ivs []Invoice) (Ageing, error) {
	a := Ageing{Customer: cu.ID, Name: cu.Name, Status: cu.Status}
	for _, iv := range ivs {
		if iv.Customer != cu.ID || iv.Status == "Draft" || iv.Status == "Void" {
			continue
		}
		for _, p := range iv.Payments {
			if p.Date > a.LastPaid {
				a.LastPaid = p.Date
			}
		}
		iv.settle()
		if iv.Balance <= 0 {
			continue
		}
		days := c.DaysOverdue(iv)
		switch {
		case days <= 0:
			a.NotDue += iv.Balance
		case days <= 30:
			a.D30 += iv.Balance
		case days <= 60:
			a.D60 += iv.Balance
		case days <= 90:
			a.D90 += iv.Balance
		default:
			a.Over90 += iv.Balance
		}
		a.Total += iv.Balance
		a.Oldest = max(a.Oldest, days)
	}
	return a, nil
}

// Receivables is what every customer owes, by age.
func (c *Control) Receivables(ctx context.Context, by string) ([]Ageing, Ageing, error) {
	if err := c.need(ctx, by, RoleBilling); err != nil {
		return nil, Ageing{}, err
	}
	cus, err := docs.List[Customer](ctx, c.Docs, T, cCustomer, "")
	if err != nil {
		return nil, Ageing{}, err
	}
	ivs, _ := docs.List[Invoice](ctx, c.Docs, T, cInvoice, "")
	out := []Ageing{}
	tot := Ageing{Name: "All customers"}
	for _, cu := range cus {
		a, _ := c.ageingFor(ctx, cu, ivs)
		if a.Total == 0 {
			continue
		}
		out = append(out, a)
		tot.NotDue += a.NotDue
		tot.D30 += a.D30
		tot.D60 += a.D60
		tot.D90 += a.D90
		tot.Over90 += a.Over90
		tot.Total += a.Total
		tot.Oldest = max(tot.Oldest, a.Oldest)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Oldest > out[j].Oldest || (out[i].Oldest == out[j].Oldest && out[i].Total > out[j].Total)
	})
	return out, tot, nil
}

// TDSRow is TDS a customer deducted on one payment.
type TDSRow struct {
	Invoice  string       `json:"invoice"`
	InvNo    string       `json:"invoice_number"`
	Customer string       `json:"customer"`
	Name     string       `json:"name"`
	PAN      string       `json:"tan_pan,omitempty"`
	Date     string       `json:"date"`
	Quarter  string       `json:"quarter"` // FY 26-27 Q2
	Index    int          `json:"index"`   // the payment's place on the invoice
	Amount   money.Amount `json:"amount"`
	Cert     string       `json:"cert"`
}

func quarterOf(date string) string {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return ""
	}
	q := (int(t.Month())+8)%12/3 + 1 // Apr–Jun = Q1
	return fmt.Sprintf("FY %s Q%d", fy(t), q)
}

// TDSRegister lists TDS deducted by customers, with whether each
// certificate (Form 16A) has come in.
func (c *Control) TDSRegister(ctx context.Context, by string) ([]TDSRow, error) {
	if err := c.need(ctx, by, RoleBilling); err != nil {
		return nil, err
	}
	ivs, err := docs.List[Invoice](ctx, c.Docs, T, cInvoice, "")
	out := []TDSRow{}
	for _, iv := range ivs {
		for i, p := range iv.Payments {
			if p.TDS > 0 {
				out = append(out, TDSRow{Invoice: iv.ID, InvNo: iv.Number, Customer: iv.Customer, Name: firstOf(iv.Bill.LegalName, iv.Bill.Name), Date: p.Date, Quarter: quarterOf(p.Date), Index: i, Amount: p.TDS, Cert: p.TDSCert})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date > out[j].Date })
	return out, err
}

// TDSCertificate records the Form 16A for TDS on one payment.
func (c *Control) TDSCertificate(ctx context.Context, by, invoice string, index int, cert string) (Invoice, error) {
	if err := c.need(ctx, by, RoleBilling); err != nil {
		return Invoice{}, err
	}
	cert = clean(cert, 80)
	if cert == "" {
		return Invoice{}, invalid("enter the certificate number")
	}
	iv, err := docs.Update(ctx, c.Docs, T, cInvoice, invoice, by, func(x *Invoice) error {
		if index < 0 || index >= len(x.Payments) || x.Payments[index].TDS <= 0 {
			return invalid("no TDS on that payment")
		}
		x.Payments[index].TDSCert = cert
		return nil
	})
	if err == nil {
		c.log(ctx, by, "tds.certificate", invoice, cert)
	}
	return iv, err
}

// ---- invoice PDF, email and reminders ----

func vendorLines(v Vendor) []string {
	out := []string{v.Name}
	for _, l := range strings.Split(v.Address, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, strings.TrimSpace(l))
		}
	}
	id := []string{}
	if v.GSTIN != "" {
		id = append(id, "GSTIN "+v.GSTIN)
	}
	if v.PAN != "" {
		id = append(id, "PAN "+v.PAN)
	}
	if len(id) > 0 {
		out = append(out, strings.Join(id, " · "))
	}
	return out
}

func partyLines(cu Customer) []string {
	out := []string{firstOf(cu.LegalName, cu.Name)}
	for _, l := range strings.Split(cu.Address, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, strings.TrimSpace(l))
		}
	}
	if cu.GSTIN != "" {
		out = append(out, "GSTIN "+cu.GSTIN)
	}
	return out
}

// InvoicePDF is the tax invoice as a PDF.
func (c *Control) InvoicePDF(ctx context.Context, by, id string) (string, []byte, error) {
	iv, err := c.GetInvoice(ctx, by, id)
	if err != nil {
		return "", nil, err
	}
	r := pdf.Report{Firm: vendorLines(iv.Vendor), Title: "Tax invoice", Party: append([]string{"Bill to"}, partyLines(iv.Bill)...)}
	r.Meta = []string{"No. " + firstOf(iv.Number, "DRAFT "+iv.ID), "Date " + firstOf(iv.Date, "—"), "For " + monthName(iv.Period)}
	if iv.Due != "" {
		r.Meta = append(r.Meta, "Due "+iv.Due)
	}
	switch iv.Status {
	case "Draft":
		r.Banner = "DRAFT: not a tax invoice until issued."
	case "Void":
		r.Banner = "VOID: this invoice has been cancelled."
	}
	t := pdf.Table{Cols: []pdf.Col{{Label: "#", Weight: 0.4}, {Label: "Description", Weight: 5}, {Label: "SAC", Weight: 0.9}, {Label: "Qty", Right: true, Weight: 0.7}, {Label: "Rate", Right: true, Weight: 1.2}, {Label: "Amount", Right: true, Weight: 1.3}}}
	for i, l := range iv.Lines {
		t.Rows = append(t.Rows, []string{fmt.Sprint(i + 1), l.Desc, l.SAC, fmt.Sprint(l.Qty), inr(l.Rate), inr(l.Amount)})
	}
	r.Sections = []pdf.Table{t}
	sum := [][2]string{{"Subtotal", inr(iv.Subtotal)}}
	if iv.Discount > 0 {
		sum = append(sum, [2]string{"Discount", "-" + inr(iv.Discount)})
	}
	sum = append(sum, [2]string{"Taxable value", inr(iv.Taxable)})
	if iv.IGST > 0 {
		sum = append(sum, [2]string{"IGST " + iv.GSTPct + "%", inr(iv.IGST)})
	} else {
		sum = append(sum, [2]string{"CGST " + halfPct(iv.GSTPct) + "%", inr(iv.CGST)}, [2]string{"SGST " + halfPct(iv.GSTPct) + "%", inr(iv.SGST)})
	}
	sum = append(sum, [2]string{"Total", "Rs. " + inr(iv.Total)})
	if iv.Credited > 0 {
		sum = append(sum, [2]string{"Credit notes", "-" + inr(iv.Credited)})
	}
	if iv.Paid > 0 {
		sum = append(sum, [2]string{"Received (incl. TDS)", inr(iv.Paid)})
	}
	if iv.Status != "Draft" && iv.Status != "Void" {
		sum = append(sum, [2]string{"Balance due", inr(iv.Balance)})
	}
	r.Summary = sum
	r.Notes = []string{InWords(iv.Total)}
	if s := strings.TrimSpace(iv.Notes); s != "" {
		r.Notes = append(r.Notes, s)
	}
	if v := iv.Vendor; v.Account != "" {
		r.Notes = append(r.Notes, fmt.Sprintf("Pay to %s, %s, A/c %s, IFSC %s%s", v.Name, v.Bank, v.Account, v.IFSC, map[bool]string{true: ", UPI " + v.UPI, false: ""}[v.UPI != ""]))
	}
	place := "Place of supply: state code " + firstOf(custState(iv.Bill), "—")
	if iv.IGST > 0 {
		place += " (inter-state, IGST)"
	} else {
		place += " (intra-state, CGST + SGST)"
	}
	r.Notes = append(r.Notes, place)
	r.Footer = iv.Vendor.Footer
	r.Stamp = []string{"For " + iv.Vendor.Name, "", "Authorised signatory"}
	b, err := pdf.Render(r).Bytes(pdf.Meta{Title: "Invoice " + firstOf(iv.Number, iv.ID), Author: iv.Vendor.Name, Created: c.now()}, nil)
	name := "Invoice_" + strings.NewReplacer("/", "-", " ", "_").Replace(firstOf(iv.Number, iv.ID)) + ".pdf"
	return name, b, err
}

// StatementPDF is a customer's statement of account as a PDF.
func (c *Control) StatementPDF(ctx context.Context, by, customer, from, to string) (string, []byte, error) {
	v, err := c.Statement(ctx, by, customer, from, to)
	if err != nil {
		return "", nil, err
	}
	r := pdf.Report{Firm: vendorLines(v.Vendor), Title: "Statement of account", Party: partyLines(v.Customer), Meta: []string{"As at " + v.To}}
	if v.From != "" {
		r.Meta = append(r.Meta, "From "+v.From)
	}
	t := pdf.Table{Cols: []pdf.Col{{Label: "Date", Weight: 1}, {Label: "Entry", Weight: 1.1}, {Label: "Reference", Weight: 1.6}, {Label: "Details", Weight: 3}, {Label: "Debit", Right: true, Weight: 1.2}, {Label: "Credit", Right: true, Weight: 1.2}, {Label: "Balance", Right: true, Weight: 1.3}}, Empty: "No entries."}
	if v.From != "" {
		t.Rows = append(t.Rows, []string{v.From, "Opening", "", "", "", "", inr(v.Opening)})
	}
	for _, l := range v.Lines {
		d, cr := "", ""
		if l.Debit > 0 {
			d = inr(l.Debit)
		}
		if l.Credit > 0 {
			cr = inr(l.Credit)
		}
		t.Rows = append(t.Rows, []string{l.Date, l.Kind, l.Ref, l.Note, d, cr, inr(l.Balance)})
	}
	r.Sections = []pdf.Table{t}
	a := v.Ageing
	r.Summary = [][2]string{{"Balance due", "Rs. " + inr(v.Closing)}, {"Not yet due", inr(a.NotDue)}, {"1-30 days late", inr(a.D30)}, {"31-60 days late", inr(a.D60)}, {"61-90 days late", inr(a.D90)}, {"Over 90 days late", inr(a.Over90)}}
	if v.Closing < 0 {
		r.Summary[0] = [2]string{"In your favour (refund or adjust)", "Rs. " + inr(-v.Closing)}
	}
	if vd := v.Vendor; vd.Account != "" {
		r.Notes = []string{fmt.Sprintf("Pay to %s, %s, A/c %s, IFSC %s", vd.Name, vd.Bank, vd.Account, vd.IFSC)}
	}
	b, err := pdf.Render(r).Bytes(pdf.Meta{Title: "Statement of account", Author: v.Vendor.Name, Created: c.now()}, nil)
	return "Statement_" + v.Customer.ID + "_" + v.To + ".pdf", b, err
}

func billTo(cu Customer) (string, []string) {
	to := strings.TrimSpace(firstOf(cu.BillingEmail, cu.Email))
	var cc []string
	for _, e := range strings.Split(cu.CCEmails, ",") {
		if e = strings.TrimSpace(e); mail.ValidAddress(e) && e != to {
			cc = append(cc, e)
		}
	}
	return to, cc
}

// SendInvoice emails an issued invoice as a PDF to the customer.
func (c *Control) SendInvoice(ctx context.Context, by, id string) (Invoice, error) {
	if err := c.need(ctx, by, RoleBilling); err != nil {
		return Invoice{}, err
	}
	if c.Mailer == nil {
		return Invoice{}, invalid("no mail server is set up for Control (LLCTL_SMTP_URL)")
	}
	iv, err := c.GetInvoice(ctx, by, id)
	if err != nil {
		return iv, err
	}
	if iv.Status == "Draft" || iv.Status == "Void" {
		return iv, invalid("issue the invoice first")
	}
	cu, _, _ := docs.Get[Customer](ctx, c.Docs, T, cCustomer, iv.Customer)
	to, cc := billTo(cu)
	if !mail.ValidAddress(to) {
		return iv, invalid("add the customer's billing email first")
	}
	name, data, err := c.InvoicePDF(ctx, by, id)
	if err != nil {
		return iv, err
	}
	m := mail.Message{To: to, CC: cc, Subject: fmt.Sprintf("Invoice %s from %s, %s", iv.Number, iv.Vendor.Name, monthName(iv.Period)),
		Text: fmt.Sprintf("Dear %s,\n\nPlease find attached our invoice %s for %s: Rs. %s, due on %s.\n\n%s\n\nThank you,\n%s",
			firstOf(cu.Contact, cu.Name), iv.Number, monthName(iv.Period), inr(iv.Total), iv.Due, payLine(iv.Vendor), iv.Vendor.Name),
		Attach: []mail.File{{Name: name, Type: "application/pdf", Data: data}}}
	if _, err := c.Mailer.Send(ctx, m); err != nil {
		return iv, fmt.Errorf("%w: the mail server refused it: %s", ErrInvalid, err.Error())
	}
	out, err := docs.Update(ctx, c.Docs, T, cInvoice, id, by, func(x *Invoice) error { x.Emailed, x.EmailedTo = c.stamp(), to; return nil })
	c.log(ctx, by, "invoice.email", id, to)
	return out, err
}

func payLine(v Vendor) string {
	if v.Account == "" {
		return ""
	}
	s := fmt.Sprintf("Bank: %s, A/c %s, IFSC %s.", v.Bank, v.Account, v.IFSC)
	if v.UPI != "" {
		s += " UPI: " + v.UPI + "."
	}
	return s
}

// SendReminder sends a payment reminder for an overdue (or soon due)
// invoice: by email with the invoice attached, and if asked as a notice to
// the firm's Admins inside Ledgerline.
func (c *Control) SendReminder(ctx context.Context, by, id string, notice bool) (Invoice, error) {
	if err := c.need(ctx, by, RoleBilling); err != nil {
		return Invoice{}, err
	}
	iv, err := c.GetInvoice(ctx, by, id)
	if err != nil {
		return iv, err
	}
	if iv.Status != "Issued" && iv.Status != "Part-paid" {
		return iv, invalid("nothing is due on this invoice")
	}
	cu, _, _ := docs.Get[Customer](ctx, c.Docs, T, cCustomer, iv.Customer)
	days := c.DaysOverdue(iv)
	rem := Reminder{At: c.stamp(), By: by, Days: days}
	var via []string
	if c.Mailer != nil {
		to, cc := billTo(cu)
		if mail.ValidAddress(to) {
			name, data, err := c.InvoicePDF(ctx, by, id)
			if err != nil {
				return iv, err
			}
			when := "is due on " + iv.Due
			if days > 0 {
				when = fmt.Sprintf("was due on %s (%d days ago)", iv.Due, days)
			}
			m := mail.Message{To: to, CC: cc, Subject: fmt.Sprintf("Payment reminder: invoice %s, Rs. %s", iv.Number, inr(iv.Balance)),
				Text: fmt.Sprintf("Dear %s,\n\nThis is a reminder that Rs. %s on our invoice %s (%s) %s. The invoice is attached.\n\n%s\n\nIf you have already paid, please send us the payment reference (and any TDS deducted) so we can match it.\n\nThank you,\n%s",
					firstOf(cu.Contact, cu.Name), inr(iv.Balance), iv.Number, monthName(iv.Period), when, payLine(iv.Vendor), iv.Vendor.Name),
				Attach: []mail.File{{Name: name, Type: "application/pdf", Data: data}}}
			if _, err := c.Mailer.Send(ctx, m); err != nil {
				rem.Result = "email failed: " + err.Error()
			} else {
				via, rem.To = append(via, "email"), to
			}
		} else {
			rem.Result = "no billing email"
		}
	}
	if notice {
		text := fmt.Sprintf("Rs. %s on Ledgerline invoice %s (%s) %s. Please arrange payment, or write to %s if it has been paid.", inr(iv.Balance), iv.Number, monthName(iv.Period),
			map[bool]string{true: fmt.Sprintf("is %d days overdue", days), false: "is due on " + iv.Due}[days > 0], firstOf(iv.Vendor.Email, "us"))
		until := c.now().AddDate(0, 0, 14).Format(time.RFC3339)
		if _, err := c.SendNotice(ctx, by, NoticeInput{Title: "Payment reminder: invoice " + iv.Number, Text: text, Level: "warn", Audience: "admins", Until: until, Target: "customer:" + cu.ID}); err == nil {
			via = append(via, "notice")
		} else if rem.Result == "" {
			rem.Result = "notice failed: " + err.Error()
		}
	}
	if len(via) == 0 {
		if rem.Result == "" {
			rem.Result = "no mail server is set up for Control (LLCTL_SMTP_URL)"
		}
		return iv, invalid("reminder not sent: %s", rem.Result)
	}
	rem.Via = strings.Join(via, "+")
	out, err := docs.Update(ctx, c.Docs, T, cInvoice, id, by, func(x *Invoice) error { x.Reminders = append(x.Reminders, rem); return nil })
	c.log(ctx, by, "invoice.reminder", id, rem.Via)
	return out, err
}

// DueReminder is an invoice that needs a reminder now.
type DueReminder struct {
	Invoice  string       `json:"invoice"`
	Number   string       `json:"number"`
	Customer string       `json:"customer"`
	Name     string       `json:"name"`
	Balance  money.Amount `json:"balance"`
	Days     int          `json:"days"`
	Last     string       `json:"last,omitempty"`
	Count    int          `json:"count"`
	Suspend  bool         `json:"suspend"` // past the days set for suspension
}

// RemindersDue lists overdue invoices whose last reminder is older than
// the interval in Settings.
func (c *Control) RemindersDue(ctx context.Context, by string) ([]DueReminder, error) {
	if err := c.need(ctx, by, RoleBilling); err != nil {
		return nil, err
	}
	set, _ := c.Settings(ctx)
	every := set.Ops.RemindEvery
	if every <= 0 {
		every = 7
	}
	ivs, err := docs.List[Invoice](ctx, c.Docs, T, cInvoice, "")
	out := []DueReminder{}
	for _, iv := range ivs {
		iv.settle()
		days := c.DaysOverdue(iv)
		if days <= 0 || iv.Balance <= 0 {
			continue
		}
		d := DueReminder{Invoice: iv.ID, Number: iv.Number, Customer: iv.Customer, Name: firstOf(iv.Bill.Name, iv.Customer), Balance: iv.Balance, Days: days, Count: len(iv.Reminders)}
		if n := len(iv.Reminders); n > 0 {
			d.Last = iv.Reminders[n-1].At
			if t, err := time.Parse(time.RFC3339, d.Last); err == nil && c.now().Sub(t) < time.Duration(every)*24*time.Hour {
				continue
			}
		}
		d.Suspend = set.Ops.SuspendAfter > 0 && days >= set.Ops.SuspendAfter
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Days > out[j].Days })
	return out, err
}

// Daily runs Control's own daily work: automatic payment reminders when
// switched on. It runs once a day (after 10:00 IST); later calls the same
// day do nothing.
func (c *Control) Daily(ctx context.Context) (int, error) {
	now := c.now()
	if now.Hour() < 10 {
		return 0, nil
	}
	set, _ := c.Settings(ctx)
	if !set.Ops.AutoRemind {
		return 0, nil
	}
	day := now.Format("2006-01-02")
	done, _, _ := docs.GetOr(ctx, c.Docs, T, cSeq, "daily-run", struct{ Day string }{})
	if done.Day == day {
		return 0, nil
	}
	if err := docs.Upsert(ctx, c.Docs, T, cSeq, "daily-run", struct{ Day string }{day}, "system"); err != nil {
		return 0, err
	}
	due, err := c.RemindersDue(ctx, SystemStaff)
	if err != nil {
		return 0, err
	}
	n := 0
	var errs []string
	for _, d := range due {
		if _, err := c.SendReminder(ctx, SystemStaff, d.Invoice, set.Ops.NoticeOverdue); err != nil {
			errs = append(errs, d.Number+": "+err.Error())
			continue
		}
		n++
	}
	if len(errs) > 0 {
		return n, errors.New(strings.Join(errs, "; "))
	}
	return n, nil
}

// CreditNotePDF is a credit note as a PDF.
func (c *Control) CreditNotePDF(ctx context.Context, by, id string) (string, []byte, error) {
	if err := c.need(ctx, by, RoleBilling); err != nil {
		return "", nil, err
	}
	cn, _, err := docs.Get[CreditNote](ctx, c.Docs, T, cCredit, id)
	if err != nil {
		return "", nil, ErrNotFound
	}
	r := pdf.Report{Firm: vendorLines(cn.Vendor), Title: "Credit note", Party: append([]string{"Issued to"}, partyLines(cn.Bill)...),
		Meta: []string{"No. " + cn.Number, "Date " + cn.Date, "Against invoice " + cn.InvNo}}
	r.Sections = []pdf.Table{{Cols: []pdf.Col{{Label: "Reason", Weight: 5}, {Label: "Value before GST", Right: true, Weight: 1.6}}, Rows: [][]string{{cn.Reason, inr(cn.Taxable)}}}}
	sum := [][2]string{{"Taxable value", inr(cn.Taxable)}}
	if cn.IGST > 0 {
		sum = append(sum, [2]string{"IGST " + cn.GSTPct + "%", inr(cn.IGST)})
	} else {
		sum = append(sum, [2]string{"CGST " + halfPct(cn.GSTPct) + "%", inr(cn.CGST)}, [2]string{"SGST " + halfPct(cn.GSTPct) + "%", inr(cn.SGST)})
	}
	r.Summary = append(sum, [2]string{"Total credit", "Rs. " + inr(cn.Total)})
	r.Notes = []string{InWords(cn.Total), "Issued under section 34 of the CGST Act against the invoice above."}
	r.Stamp = []string{"For " + cn.Vendor.Name, "", "Authorised signatory"}
	b, err := pdf.Render(r).Bytes(pdf.Meta{Title: "Credit note " + cn.Number, Author: cn.Vendor.Name, Created: c.now()}, nil)
	return "CreditNote_" + strings.NewReplacer("/", "-").Replace(cn.Number) + ".pdf", b, err
}
