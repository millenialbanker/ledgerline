package ops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"

	"ledgerline/internal/billscan"
	"ledgerline/internal/docs"
	"ledgerline/internal/help"
	"ledgerline/internal/ingest"
)

// ---- scanning a vendor bill ----
//
// A bill is uploaded (PDF, JPEG or PNG) and kept with the books. A PDF made
// by the vendor's software has its text read on this server and the details
// picked out: vendor, GSTIN (check digit verified), PAN, address, bank,
// bill number and dates, taxable value, GST and total. A scanned or
// photographed bill has no text; if the firm has switched on "Read bills
// with AI", it is sent to the AI model connected to the server, metered and
// paid from the firm's AI credit like Ledgy's answers. Either way the
// details only fill in the bill form: a person checks them against the
// bill and saves, and an Ops checker posts the voucher.

// BillScanDoc is an uploaded bill and what was read from it.
type BillScanDoc struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	ContentType string          `json:"content_type"`
	Size        int64           `json:"size"`
	SHA256      string          `json:"sha256"`
	FileID      string          `json:"file_id"`
	By          string          `json:"by"`
	At          string          `json:"at"`
	Method      string          `json:"method"` // text, ai, none
	Note        string          `json:"note,omitempty"`
	Fields      billscan.Fields `json:"fields"`
	// The vendor master entry the bill matched, and how.
	Vendor      string   `json:"vendor,omitempty"`
	VendorMatch string   `json:"vendor_match,omitempty"` // gstin, pan, name
	Checks      []string `json:"checks"`                 // differences from the vendor master
	Duplicate   string   `json:"duplicate,omitempty"`    // an earlier scan of the same file
	Bill        string   `json:"bill,omitempty"`         // the bill booked from it
	AIOffer     bool     `json:"ai_offer"`               // reading with AI is possible
	AINote      string   `json:"ai_note,omitempty"`
}

var billTypes = map[string]bool{"application/pdf": true, "image/jpeg": true, "image/png": true}

// billAI says whether bills may be read with AI, and why not.
func (o *Ops) billAI(ctx context.Context, tenant string) (help.DocumentReader, string) {
	if o.Residency.IndiaOnly {
		return nil, "Reading bills with AI is not allowed in India-only mode."
	}
	dr, ok := o.LedgyAI.(help.DocumentReader)
	if o.LedgyAI == nil || !ok || o.Meter == nil {
		return nil, "No AI model is connected on this server."
	}
	if set, _ := o.Settings(ctx, tenant); !set.BillAI {
		return nil, "An admin can switch on “Read bills with AI” in Settings (the bill is then sent to the AI provider)."
	}
	return dr, ""
}

// ScanBill stores a bill and reads its details. With useAI, a bill whose
// text cannot be read is read by the AI model (when the firm allows it).
func (o *Ops) ScanBill(ctx context.Context, tenant, actor, name string, data []byte, useAI bool) (BillScanDoc, error) {
	if err := o.require(ctx, tenant, actor, "Scanning vendor bills", RoleMaker, RoleChecker); err != nil {
		return BillScanDoc{}, err
	}
	if len(data) == 0 || len(data) > MaxBillBytes {
		return BillScanDoc{}, invalid("bills must be between 1 byte and 10 MB")
	}
	ct := http.DetectContentType(data)
	if !billTypes[ct] {
		return BillScanDoc{}, invalid("only PDF, JPEG and PNG bills are accepted (this looks like %s)", ct)
	}
	name = strings.TrimSpace(strings.NewReplacer("/", "_", "\\", "_").Replace(name))
	if name == "" {
		name = "bill"
	}
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	prev, err := docs.List[BillScanDoc](ctx, o.Docs, tenant, cBillScan, "")
	if err != nil {
		return BillScanDoc{}, err
	}
	n, err := o.next(ctx, tenant, "billscan", actor)
	if err != nil {
		return BillScanDoc{}, err
	}
	id := fmt.Sprintf("BS-%06d", n)
	rf, err := o.App.Ingest.Raw.Put(ctx, ingest.RawFile{Tenant: tenant, Source: "VENDOR-BILL", Name: id + " " + name, SHA256: sha, Content: data})
	if err != nil {
		return BillScanDoc{}, err
	}
	sc := BillScanDoc{ID: id, Name: name, ContentType: ct, Size: int64(len(data)), SHA256: sha, FileID: rf.ID, By: actor, At: o.now().Format(time.RFC3339), Checks: []string{}}
	for _, p := range prev {
		if p.SHA256 == sha {
			sc.Duplicate = p.ID
			if p.Bill != "" {
				sc.Duplicate = p.ID + " (booked as " + p.Bill + ")"
			}
		}
	}
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return sc, err
	}

	sc.Method = "none"
	sc.Fields = billscan.Fields{Found: []string{}, Warnings: []string{}}
	if ct == "application/pdf" {
		if text, err := billscan.ExtractText(data); err == nil {
			sc.Fields, sc.Method = billscan.Parse(text, set.FirmGSTIN), "text"
		}
	}
	weak := sc.Method == "none" || sc.Fields.Total == 0 || (sc.Fields.VendorGSTIN == "" && sc.Fields.VendorName == "")
	dr, why := o.billAI(ctx, tenant)
	sc.AIOffer = dr != nil
	switch {
	case weak && useAI && dr != nil:
		o.readBillAI(ctx, tenant, actor, dr, data, ct, set.FirmGSTIN, &sc)
	case sc.Method == "none":
		sc.Note = "This bill is a scan or photo, so its text cannot be read on the server. Type the details from the bill."
		if dr != nil {
			sc.Note = "This bill is a scan or photo. Read it with AI, or type the details from the bill."
		} else {
			sc.AINote = why
		}
	case weak:
		sc.Note = "Some details could not be read from this bill; fill them in from the bill."
		if dr == nil {
			sc.AINote = why
		}
	}
	o.matchVendor(ctx, tenant, &sc)
	if _, err := docs.Put(ctx, o.Docs, tenant, cBillScan, sc.ID, sc, 0, actor); err != nil {
		return sc, err
	}
	o.log(ctx, tenant, actor, "vendorbill.scanned", sc.ID, map[string]any{"sha256": sha, "method": sc.Method, "found": len(sc.Fields.Found), "vendor": sc.Vendor})
	return sc, nil
}

// ReadBillWithAI reads an uploaded bill with the AI model.
func (o *Ops) ReadBillWithAI(ctx context.Context, tenant, actor, id string) (BillScanDoc, error) {
	if err := o.require(ctx, tenant, actor, "Scanning vendor bills", RoleMaker, RoleChecker); err != nil {
		return BillScanDoc{}, err
	}
	sc, _, err := docs.Get[BillScanDoc](ctx, o.Docs, tenant, cBillScan, id)
	if err != nil {
		return sc, err
	}
	if sc.Bill != "" {
		return sc, fmt.Errorf("%w: this bill is already booked as %s", ErrState, sc.Bill)
	}
	dr, why := o.billAI(ctx, tenant)
	if dr == nil {
		return sc, invalid("%s", why)
	}
	rf, err := o.App.Ingest.Raw.Get(ctx, tenant, sc.FileID)
	if err != nil {
		return sc, err
	}
	set, _ := o.Settings(ctx, tenant)
	if err := o.readBillAI(ctx, tenant, actor, dr, rf.Content, sc.ContentType, set.FirmGSTIN, &sc); err != nil {
		return sc, err
	}
	o.matchVendor(ctx, tenant, &sc)
	if err := docs.Upsert(ctx, o.Docs, tenant, cBillScan, sc.ID, sc, actor); err != nil {
		return sc, err
	}
	return sc, nil
}

// readBillAI sends the bill to the model, meters the call and keeps what
// was read when it is the expected JSON.
func (o *Ops) readBillAI(ctx context.Context, tenant, actor string, dr help.DocumentReader, data []byte, ct, firmGSTIN string, sc *BillScanDoc) error {
	reserve, err := o.aiReserve(o.LedgyAI.Provider(), o.LedgyAI.Model(), 2500+len(data)/400, help.MaxDocOutputTokens)
	if err != nil {
		sc.AINote = "AI reading is unavailable: " + err.Error()
		return invalid("%s", sc.AINote)
	}
	if o.aiBalance(ctx, tenant) < reserve {
		sc.AINote = "The firm's AI credit has run out. Ask your Ledgerline provider to add credit, or type the details from the bill."
		return invalid("%s", sc.AINote)
	}
	cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	r, err := dr.ReadDocument(cctx, data, ct, billscan.AIInstruction)
	cancel()
	if err != nil {
		sc.AINote = "The AI model could not read the bill: " + err.Error()
		return fmt.Errorf("%w: %s", ErrState, sc.AINote)
	}
	if _, err := o.aiMeter(ctx, tenant, actor, r); err != nil {
		sc.AINote = "The AI reading was not used: it could not be metered (" + err.Error() + ")."
		return fmt.Errorf("%w: %s", ErrState, sc.AINote)
	}
	f, err := billscan.ParseAI(r.Text, firmGSTIN)
	if err != nil {
		sc.AINote = "The AI model's reply could not be used; type the details from the bill."
		return fmt.Errorf("%w: %s", ErrState, sc.AINote)
	}
	sc.Fields, sc.Method, sc.Note = f, "ai", "Read by "+aiName(r)+". Check every figure against the bill before saving."
	sc.AINote = ""
	o.log(ctx, tenant, actor, "vendorbill.read_ai", sc.ID, map[string]any{"model": r.Model, "tokens": r.Usage.InputTokens + r.Usage.OutputTokens})
	return nil
}

// matchVendor finds the vendor the bill is from and notes where the bill
// differs from the vendor master, above all the bank account.
func (o *Ops) matchVendor(ctx context.Context, tenant string, sc *BillScanDoc) {
	sc.Vendor, sc.VendorMatch, sc.Checks = "", "", []string{}
	vs, err := docs.List[Vendor](ctx, o.Docs, tenant, cVendor, "")
	if err != nil {
		return
	}
	f := sc.Fields
	var v *Vendor
	for i := range vs {
		switch {
		case f.VendorGSTIN != "" && vs[i].GSTIN == f.VendorGSTIN:
			v, sc.VendorMatch = &vs[i], "gstin"
		case v == nil && f.VendorPAN != "" && vs[i].PAN == f.VendorPAN && f.VendorGSTIN == "":
			v, sc.VendorMatch = &vs[i], "pan"
		case v == nil && f.VendorName != "" && vendorNameKey(vs[i].Name) == vendorNameKey(f.VendorName):
			v, sc.VendorMatch = &vs[i], "name"
		}
		if sc.VendorMatch == "gstin" {
			break
		}
	}
	if v == nil {
		return
	}
	sc.Vendor = v.ID
	if sc.VendorMatch == "name" && f.VendorGSTIN != "" && v.GSTIN != "" && v.GSTIN != f.VendorGSTIN {
		sc.Checks = append(sc.Checks, "The bill's GSTIN "+f.VendorGSTIN+" differs from "+v.Name+"'s "+v.GSTIN+" in the vendor master: it may be another registration of the vendor, or another business.")
	}
	if f.BankAccount != "" && v.Account != "" && f.BankAccount != v.Account {
		sc.Checks = append(sc.Checks, "The bank account on the bill (ending "+last4(f.BankAccount)+") differs from the vendor master (ending "+last4(v.Account)+"). Confirm with the vendor by phone on a known number before paying: changed bank details are the commonest bill fraud.")
	}
	if f.BankIFSC != "" && v.IFSC != "" && f.BankIFSC != v.IFSC && f.BankAccount == v.Account {
		sc.Checks = append(sc.Checks, "The IFSC on the bill ("+f.BankIFSC+") differs from the vendor master ("+v.IFSC+").")
	}
	if f.BillNo != "" {
		if bills, err := o.vendorBills(ctx, tenant); err == nil {
			for _, b := range bills {
				if b.Vendor == v.ID && billNoKey(b.BillNo) == billNoKey(f.BillNo) && b.Status != BillRejected && b.Status != BillCancelled {
					sc.Checks = append(sc.Checks, "Bill "+b.BillNo+" of "+v.Name+" is already in the register ("+b.ID+"): this may be a copy of a bill already booked.")
					break
				}
			}
		}
	}
	if f.BankAccount != "" && v.Account == "" {
		sc.Checks = append(sc.Checks, "The vendor master has no bank account; the bill shows one ending "+last4(f.BankAccount)+". Confirm it with the vendor before adding it.")
	}
}

func vendorNameKey(s string) string {
	s = strings.ToUpper(s)
	for _, x := range []string{"PRIVATE LIMITED", "PVT. LTD.", "PVT LTD", "PVT. LTD", "LIMITED", "LTD.", "LTD", "LLP", "M/S.", "M/S", ".", ",", "&", " AND "} {
		s = strings.ReplaceAll(s, x, " ")
	}
	return strings.Join(strings.Fields(s), " ")
}

// BillScans lists uploaded bills not yet booked, newest first.
func (o *Ops) BillScans(ctx context.Context, tenant, actor string) ([]BillScanDoc, error) {
	if err := o.require(ctx, tenant, actor, "Viewing vendor bills", RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return nil, err
	}
	all, err := docs.List[BillScanDoc](ctx, o.Docs, tenant, cBillScan, "")
	if err != nil {
		return nil, err
	}
	out := []BillScanDoc{}
	for i := len(all) - 1; i >= 0; i-- {
		if all[i].Bill == "" {
			out = append(out, all[i])
		}
	}
	return out, nil
}

// BillScan returns one uploaded bill.
func (o *Ops) BillScan(ctx context.Context, tenant, actor, id string) (BillScanDoc, error) {
	if err := o.require(ctx, tenant, actor, "Viewing vendor bills", RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return BillScanDoc{}, err
	}
	sc, _, err := docs.Get[BillScanDoc](ctx, o.Docs, tenant, cBillScan, id)
	return sc, err
}

// BillScanFile returns an uploaded bill's file.
func (o *Ops) BillScanFile(ctx context.Context, tenant, actor, id string) (BillScanDoc, []byte, error) {
	sc, err := o.BillScan(ctx, tenant, actor, id)
	if err != nil {
		return sc, nil, err
	}
	rf, err := o.App.Ingest.Raw.Get(ctx, tenant, sc.FileID)
	if err != nil {
		return sc, nil, err
	}
	return sc, rf.Content, nil
}

// DiscardBillScan removes an uploaded bill that will not be booked (the
// file stays in the raw store, as every upload does).
func (o *Ops) DiscardBillScan(ctx context.Context, tenant, actor, id string) error {
	sc, err := o.BillScan(ctx, tenant, actor, id)
	if err != nil {
		return err
	}
	if err := o.require(ctx, tenant, actor, "Discarding scanned bills", RoleMaker, RoleChecker); err != nil {
		return err
	}
	if sc.Bill != "" {
		return fmt.Errorf("%w: booked as %s", ErrState, sc.Bill)
	}
	sc.Bill = "discarded"
	if err := docs.Upsert(ctx, o.Docs, tenant, cBillScan, sc.ID, sc, actor); err != nil {
		return err
	}
	o.log(ctx, tenant, actor, "vendorbill.scan_discarded", sc.ID, nil)
	return nil
}

// BillAIAvailable says whether the server can read bills with AI at all
// (a model that reads documents is connected, and not India-only mode).
func (o *Ops) BillAIAvailable() bool {
	_, ok := o.LedgyAI.(help.DocumentReader)
	return ok && o.LedgyAI != nil && o.Meter != nil && !o.Residency.IndiaOnly
}
