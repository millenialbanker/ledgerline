package control

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

var editionName = map[string]string{"manual": "Manual", "autopilot": "Autopilot", "full": "Full"}

func monthName(m string) string {
	t, err := time.Parse("2006-01", m)
	if err != nil {
		return m
	}
	return t.Format("Jan 2006")
}

func pct(a money.Amount, p string) money.Amount {
	d, err := money.ParseDec(p)
	if err != nil || d.Sign() <= 0 {
		return 0
	}
	return a.Dec().Mul(d).Mul(money.MustDec("0.01")).Round(money.UnitPaisa)
}

// lines works out what a customer owes for a month.
func (c *Control) lines(ctx context.Context, cu Customer, month string, set Settings, forInvoice string) ([]InvoiceLine, []string, error) {
	p := set.Prices
	var out []InvoiceLine
	fs, err := docs.List[Firm](ctx, c.Docs, T, cFirm, "")
	if err != nil {
		return nil, nil, err
	}
	sort.Slice(fs, func(i, j int) bool { return fs[i].ID < fs[j].ID })
	traded := 0
	for _, f := range fs {
		if f.Customer != cu.ID {
			continue
		}
		u, _, err := docs.Get[UsageMonth](ctx, c.Docs, T, cUsage, f.ID+"|"+month)
		if err != nil {
			continue // no report from this firm in the month
		}
		name := firstOf(u.Report.Firm, f.Tenant)
		ed := firstOf(u.Edition, u.Report.Edition, "manual")
		if fee := p.Edition[ed]; fee > 0 {
			out = append(out, InvoiceLine{Desc: fmt.Sprintf("Ledgerline %s edition, %s — %s", firstOf(editionName[ed], ed), monthName(month), name), SAC: p.SAC, Qty: 1, Rate: fee, Amount: fee, Kind: "edition", Firm: f.ID})
		}
		if extra := u.PeakStaff - p.IncludedUsers; extra > 0 && p.ExtraUser > 0 {
			out = append(out, InvoiceLine{Desc: fmt.Sprintf("Staff logins above the %d included — %s", p.IncludedUsers, name), SAC: p.SAC, Qty: extra, Rate: p.ExtraUser, Amount: p.ExtraUser * money.Amount(extra), Kind: "users", Firm: f.ID})
		}
		if u.PeakBots > 0 && p.BotComputer > 0 {
			out = append(out, InvoiceLine{Desc: "Automation bot computers — " + name, SAC: p.SAC, Qty: u.PeakBots, Rate: p.BotComputer, Amount: p.BotComputer * money.Amount(u.PeakBots), Kind: "bots", Firm: f.ID})
		}
		if k := u.Report.KYCMonth; k > 0 && p.KYC > 0 {
			out = append(out, InvoiceLine{Desc: "Online account opening verifications — " + name, SAC: p.SAC, Qty: k, Rate: p.KYC, Amount: p.KYC * money.Amount(k), Kind: "kyc", Firm: f.ID})
		}
		traded += u.Report.TradedMonth
	}
	// Clients who traded in the month, in graduated slabs.
	lo := 0
	for _, s := range p.Slabs {
		if traded <= lo {
			break
		}
		hi := s.UpTo
		if hi == 0 || hi > traded {
			hi = traded
		}
		if n := hi - lo; n > 0 && s.Rate > 0 {
			band := fmt.Sprintf("%d–%d", lo+1, s.UpTo)
			if s.UpTo == 0 {
				band = fmt.Sprintf("above %d", lo)
			}
			out = append(out, InvoiceLine{Desc: fmt.Sprintf("Active trading clients, %s (clients %s)", monthName(month), band), SAC: p.SAC, Qty: n, Rate: s.Rate, Amount: s.Rate * money.Amount(n), Kind: "clients"})
		}
		if s.UpTo == 0 {
			break
		}
		lo = s.UpTo
	}
	// AI credit topped up from Control in the month.
	var cmds []string
	cs, _ := docs.List[CommandRec](ctx, c.Docs, T, cCommand, "")
	for _, x := range cs {
		if x.Customer != cu.ID || x.Kind != "ai_credit" || x.Status != "Done" || !x.Bill || !strings.HasPrefix(x.Done, month) {
			continue
		}
		if x.Invoice != "" && x.Invoice != forInvoice {
			continue
		}
		fm, _, _ := docs.Get[Firm](ctx, c.Docs, T, cFirm, x.Firm)
		a := money.Amount(x.Amount)
		out = append(out, InvoiceLine{Desc: fmt.Sprintf("Ledgy AI credit top-up %s — %s", x.ID, firstOf(fm.Latest.Firm, fm.Tenant)), SAC: p.SAC, Qty: 1, Rate: a, Amount: a, Kind: "ai", Firm: x.Firm})
		cmds = append(cmds, x.ID)
	}
	if cu.SetupFee > 0 && (cu.SetupBilled == "" || cu.SetupBilled == forInvoice) {
		out = append(out, InvoiceLine{Desc: "One-time setup and onboarding", SAC: p.SAC, Qty: 1, Rate: cu.SetupFee, Amount: cu.SetupFee, Kind: "setup"})
	}
	return out, cmds, nil
}

// totals fills an invoice's sums and GST.
func totals(inv *Invoice, cu Customer, set Settings) {
	inv.Subtotal, inv.Discount = 0, 0
	var discountable money.Amount
	for _, l := range inv.Lines {
		inv.Subtotal += l.Amount
		if l.Kind != "ai" {
			discountable += l.Amount
		}
	}
	inv.Discount = pct(discountable, cu.DiscountPct)
	inv.Taxable = inv.Subtotal - inv.Discount
	inv.GSTPct = firstOf(set.Prices.GSTPct, "18")
	tax := pct(inv.Taxable, inv.GSTPct)
	inv.CGST, inv.SGST, inv.IGST = 0, 0, 0
	if sameState(set.Vendor, cu) {
		inv.CGST = money.Amount(int64(tax) / 2)
		inv.SGST = tax - inv.CGST
	} else {
		inv.IGST = tax
	}
	inv.Total = inv.Taxable + tax
}

func custState(cu Customer) string {
	if len(cu.GSTIN) >= 2 {
		return cu.GSTIN[:2]
	}
	return cu.State
}

func sameState(v Vendor, cu Customer) bool {
	vs := firstOf(v.State, func() string {
		if len(v.GSTIN) >= 2 {
			return v.GSTIN[:2]
		}
		return ""
	}())
	cs := custState(cu)
	return vs == "" || cs == "" || vs == cs // unknown place of supply: treat as within the state
}

// BillLine is one customer's bill for a month, before it is saved.
type BillLine struct {
	Customer Customer `json:"customer"`
	Invoice  Invoice  `json:"invoice"`
	Existing string   `json:"existing,omitempty"` // invoice already made for the month
	Billable bool     `json:"billable"`
	Why      string   `json:"why,omitempty"`
}

func (c *Control) billable(cu Customer, month string) (bool, string) {
	switch {
	case cu.Status == "Trial":
		return false, "on trial"
	case cu.Status == "Left":
		return false, "left"
	case cu.Status == "Suspended":
		return false, "suspended"
	case cu.BillFrom != "" && month < cu.BillFrom:
		return false, "billing starts " + monthName(cu.BillFrom)
	}
	return true, ""
}

// Preview works out every customer's bill for a month.
func (c *Control) Preview(ctx context.Context, by, month string) ([]BillLine, error) {
	if err := c.need(ctx, by, RoleBilling); err != nil {
		return nil, err
	}
	if _, err := time.Parse("2006-01", month); err != nil {
		return nil, invalid("month must be YYYY-MM")
	}
	set, err := c.Settings(ctx)
	if err != nil {
		return nil, err
	}
	cus, err := docs.List[Customer](ctx, c.Docs, T, cCustomer, "")
	if err != nil {
		return nil, err
	}
	invs, _ := docs.List[Invoice](ctx, c.Docs, T, cInvoice, "")
	var out []BillLine
	for _, cu := range cus {
		b := BillLine{Customer: cu}
		for _, iv := range invs {
			if iv.Customer == cu.ID && iv.Period == month && iv.Status != "Void" {
				b.Existing = iv.ID
			}
		}
		ls, _, err := c.lines(ctx, cu, month, set, b.Existing)
		if err != nil {
			return nil, err
		}
		b.Invoice = Invoice{Customer: cu.ID, Period: month, Lines: ls}
		totals(&b.Invoice, cu, set)
		b.Billable, b.Why = c.billable(cu, month)
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Customer.Name < out[j].Customer.Name })
	return out, nil
}

// Generate makes (or refreshes) draft invoices for a month for every
// billable customer, or just one.
func (c *Control) Generate(ctx context.Context, by, month, only string) ([]Invoice, error) {
	pv, err := c.Preview(ctx, by, month)
	if err != nil {
		return nil, err
	}
	set, _ := c.Settings(ctx)
	var out []Invoice
	for _, b := range pv {
		if (only != "" && b.Customer.ID != only) || !b.Billable || len(b.Invoice.Lines) == 0 {
			continue
		}
		if b.Existing != "" {
			cur, _, _ := docs.Get[Invoice](ctx, c.Docs, T, cInvoice, b.Existing)
			if cur.Status != "Draft" {
				continue
			}
			iv, err := docs.Update(ctx, c.Docs, T, cInvoice, b.Existing, by, func(x *Invoice) error {
				x.Lines = append(b.Invoice.Lines, manualLines(x.Lines)...)
				totals(x, b.Customer, set)
				return nil
			})
			if err != nil {
				return out, err
			}
			out = append(out, iv)
			continue
		}
		n, err := c.next(ctx, "invoice")
		if err != nil {
			return out, err
		}
		iv := b.Invoice
		iv.ID, iv.Status, iv.Payments, iv.Created, iv.CreatedBy = fmt.Sprintf("INV-%05d", n), "Draft", []Payment{}, c.stamp(), by
		if err := docs.Upsert(ctx, c.Docs, T, cInvoice, iv.ID, iv, by); err != nil {
			return out, err
		}
		out = append(out, iv)
	}
	c.log(ctx, by, "invoice.generate", month, fmt.Sprint(len(out)))
	return out, nil
}

func manualLines(ls []InvoiceLine) []InvoiceLine {
	var out []InvoiceLine
	for _, l := range ls {
		if l.Kind == "other" {
			out = append(out, l)
		}
	}
	return out
}

// Invoices lists invoices.
func (c *Control) Invoices(ctx context.Context, by string) ([]Invoice, error) {
	if err := c.need(ctx, by, RoleBilling); err != nil {
		return nil, err
	}
	all, err := docs.List[Invoice](ctx, c.Docs, T, cInvoice, "")
	sort.Slice(all, func(i, j int) bool { return all[i].ID > all[j].ID })
	return all, err
}

// GetInvoice returns one invoice (with the customer's current details while a draft).
func (c *Control) GetInvoice(ctx context.Context, by, id string) (Invoice, error) {
	if err := c.need(ctx, by, RoleBilling); err != nil {
		return Invoice{}, err
	}
	iv, _, err := docs.Get[Invoice](ctx, c.Docs, T, cInvoice, id)
	if err != nil {
		return iv, ErrNotFound
	}
	if iv.Status == "Draft" {
		iv.Bill, _, _ = docs.Get[Customer](ctx, c.Docs, T, cCustomer, iv.Customer)
		set, _ := c.Settings(ctx)
		iv.Vendor = set.Vendor
	}
	return iv, nil
}

// EditDraft adds or removes a manual line on a draft, or sets its notes.
func (c *Control) EditDraft(ctx context.Context, by, id string, add *InvoiceLine, remove int, notes *string) (Invoice, error) {
	if err := c.need(ctx, by, RoleBilling); err != nil {
		return Invoice{}, err
	}
	set, _ := c.Settings(ctx)
	iv, err := docs.Update(ctx, c.Docs, T, cInvoice, id, by, func(x *Invoice) error {
		if x.Status != "Draft" {
			return invalid("only a draft can be changed; void it and make a new one")
		}
		if add != nil {
			add.Desc = clean(add.Desc, 200)
			if add.Desc == "" || add.Qty <= 0 || add.Qty > 1000000 {
				return invalid("give a description and a quantity")
			}
			add.Kind, add.SAC, add.Amount = "other", firstOf(add.SAC, set.Prices.SAC), add.Rate*money.Amount(add.Qty)
			x.Lines = append(x.Lines, *add)
		}
		if remove >= 0 && remove < len(x.Lines) {
			x.Lines = append(x.Lines[:remove], x.Lines[remove+1:]...)
		}
		if notes != nil {
			x.Notes = clean(*notes, 1000)
		}
		cu, _, _ := docs.Get[Customer](ctx, c.Docs, T, cCustomer, x.Customer)
		totals(x, cu, set)
		return nil
	})
	if err != nil {
		return iv, err
	}
	return iv, nil
}

func fy(d time.Time) string {
	y := d.Year()
	if d.Month() < time.April {
		y--
	}
	return fmt.Sprintf("%02d-%02d", y%100, (y+1)%100)
}

// IssueInvoice gives a draft its number from the financial year's series
// and fixes the customer and provider details on it.
func (c *Control) IssueInvoice(ctx context.Context, by, id, date string) (Invoice, error) {
	if err := c.need(ctx, by, RoleBilling); err != nil {
		return Invoice{}, err
	}
	d := c.now()
	if date != "" {
		t, err := time.ParseInLocation("2006-01-02", date, IST)
		if err != nil {
			return Invoice{}, invalid("date must be YYYY-MM-DD")
		}
		d = t
	}
	set, _ := c.Settings(ctx)
	cur, _, err := docs.Get[Invoice](ctx, c.Docs, T, cInvoice, id)
	if err != nil {
		return cur, ErrNotFound
	}
	if cur.Status != "Draft" {
		return cur, invalid("this invoice is already issued")
	}
	if len(cur.Lines) == 0 {
		return cur, invalid("the invoice has no lines")
	}
	cu, _, err := docs.Get[Customer](ctx, c.Docs, T, cCustomer, cur.Customer)
	if err != nil {
		return cur, invalid("the customer is missing")
	}
	if strings.TrimSpace(firstOf(cu.LegalName, cu.Name)) == "" || strings.TrimSpace(cu.Address) == "" {
		return cur, invalid("add the customer’s legal name and billing address first")
	}
	if set.Vendor.Name == "" || set.Vendor.Address == "" {
		return cur, invalid("add your company’s name and address in Settings first")
	}
	f := fy(d)
	n, err := c.next(ctx, "invno-"+f)
	if err != nil {
		return cur, err
	}
	num := fmt.Sprintf("%s/%s/%04d", firstOf(set.Vendor.Prefix, "LL"), f, n)
	iv, err := docs.Update(ctx, c.Docs, T, cInvoice, id, by, func(x *Invoice) error {
		if x.Status != "Draft" {
			return invalid("this invoice is already issued")
		}
		totals(x, cu, set)
		x.Number, x.Status, x.Date, x.Due = num, "Issued", d.Format("2006-01-02"), d.AddDate(0, 0, set.Vendor.DueDays).Format("2006-01-02")
		x.Bill, x.Vendor = cu, set.Vendor
		x.Commands = nil
		for _, l := range x.Lines {
			if l.Kind == "ai" {
				if f := strings.Fields(strings.TrimPrefix(l.Desc, "Ledgy AI credit top-up ")); len(f) > 0 {
					x.Commands = append(x.Commands, f[0])
				}
			}
		}
		return nil
	})
	if err != nil {
		return iv, err
	}
	for _, l := range iv.Lines {
		if l.Kind == "setup" {
			_, _ = docs.Update(ctx, c.Docs, T, cCustomer, cu.ID, by, func(x *Customer) error { x.SetupBilled = iv.ID; return nil })
		}
	}
	for _, cid := range iv.Commands {
		_, _ = docs.Update(ctx, c.Docs, T, cCommand, cid, by, func(x *CommandRec) error { x.Invoice = iv.ID; return nil })
	}
	c.log(ctx, by, "invoice.issue", iv.ID, num)
	return iv, nil
}

// VoidInvoice cancels an invoice. An issued invoice keeps its number (GST
// series stay unbroken); what it billed can be billed again.
func (c *Control) VoidInvoice(ctx context.Context, by, id, why string) (Invoice, error) {
	if err := c.need(ctx, by, RoleBilling); err != nil {
		return Invoice{}, err
	}
	if strings.TrimSpace(why) == "" {
		return Invoice{}, invalid("say why it is voided")
	}
	iv, err := docs.Update(ctx, c.Docs, T, cInvoice, id, by, func(x *Invoice) error {
		if x.Status == "Void" {
			return invalid("already void")
		}
		if x.Paid > 0 {
			return invalid("money has been received against it; record a credit note instead")
		}
		x.Status = "Void"
		x.Notes = strings.TrimSpace(x.Notes + "\nVoided: " + clean(why, 300))
		return nil
	})
	if err != nil {
		return iv, err
	}
	cu, _, _ := docs.Get[Customer](ctx, c.Docs, T, cCustomer, iv.Customer)
	if cu.SetupBilled == iv.ID {
		_, _ = docs.Update(ctx, c.Docs, T, cCustomer, cu.ID, by, func(x *Customer) error { x.SetupBilled = ""; return nil })
	}
	cs, _ := docs.List[CommandRec](ctx, c.Docs, T, cCommand, "")
	for _, x := range cs {
		if x.Invoice == iv.ID {
			_, _ = docs.Update(ctx, c.Docs, T, cCommand, x.ID, by, func(y *CommandRec) error { y.Invoice = ""; return nil })
		}
	}
	c.log(ctx, by, "invoice.void", iv.ID, why)
	return iv, nil
}

// RecordPayment records money received against an issued invoice.
func (c *Control) RecordPayment(ctx context.Context, by, id string, p Payment) (Invoice, error) {
	if err := c.need(ctx, by, RoleBilling); err != nil {
		return Invoice{}, err
	}
	if p.Amount <= 0 {
		return Invoice{}, invalid("enter the amount received")
	}
	if _, err := time.Parse("2006-01-02", p.Date); err != nil {
		p.Date = c.today()
	}
	p.Mode, p.Ref, p.By = firstOf(clean(p.Mode, 30), "Bank transfer"), clean(p.Ref, 80), by
	iv, err := docs.Update(ctx, c.Docs, T, cInvoice, id, by, func(x *Invoice) error {
		if x.Status != "Issued" && x.Status != "Part-paid" {
			return invalid("payments go against an issued, unpaid invoice")
		}
		if x.Paid+p.Amount > x.Total {
			return invalid("that is more than the ₹%s still due", (x.Total - x.Paid).String())
		}
		x.Payments = append(x.Payments, p)
		x.Paid += p.Amount
		x.Status = "Part-paid"
		if x.Paid == x.Total {
			x.Status = "Paid"
		}
		return nil
	})
	if err != nil {
		return iv, err
	}
	c.log(ctx, by, "invoice.payment", iv.ID, p.Amount.String())
	return iv, nil
}

// InvoiceOverdue says an issued invoice is past its due date and unpaid.
func (c *Control) InvoiceOverdue(iv Invoice) bool {
	return (iv.Status == "Issued" || iv.Status == "Part-paid") && iv.Due != "" && iv.Due < c.today()
}

// ---- amounts in words (Indian numbering) ----

var ones = []string{"", "One", "Two", "Three", "Four", "Five", "Six", "Seven", "Eight", "Nine", "Ten", "Eleven", "Twelve", "Thirteen", "Fourteen", "Fifteen", "Sixteen", "Seventeen", "Eighteen", "Nineteen"}
var tens = []string{"", "", "Twenty", "Thirty", "Forty", "Fifty", "Sixty", "Seventy", "Eighty", "Ninety"}

func words99(n int64) string {
	if n < 20 {
		return ones[n]
	}
	if n%10 == 0 {
		return tens[n/10]
	}
	return tens[n/10] + "-" + ones[n%10]
}

func words999(n int64) string {
	var p []string
	if n >= 100 {
		p = append(p, ones[n/100]+" Hundred")
		n %= 100
	}
	if n > 0 {
		p = append(p, words99(n))
	}
	return strings.Join(p, " ")
}

func intWords(n int64) string {
	if n == 0 {
		return "Zero"
	}
	var p []string
	for _, u := range []struct {
		v    int64
		name string
	}{{10000000, "Crore"}, {100000, "Lakh"}, {1000, "Thousand"}} {
		if n >= u.v {
			q := n / u.v
			if q >= 100 && u.name == "Crore" {
				p = append(p, intWords(q)+" Crore")
			} else {
				p = append(p, words99(q)+" "+u.name)
			}
			n %= u.v
		}
	}
	if n > 0 {
		p = append(p, words999(n))
	}
	return strings.Join(p, " ")
}

// InWords is an amount as written on an Indian invoice.
func InWords(a money.Amount) string {
	r, p := int64(a)/100, int64(a)%100
	s := "Rupees " + intWords(r)
	if p > 0 {
		s += " and " + words99(p) + " Paise"
	}
	return s + " Only"
}
