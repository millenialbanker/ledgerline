package ops

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"
	"time"

	"ledgerline/internal/docs"
)

// ---- collecting client money by UPI: validated handles and the SEBI QR ----
//
// SEBI circular SEBI/HO/DEPA-II/DEPA-II_SRG/P/CIR/2025/86 (11 June 2025):
// an intermediary that collects money from investors by UPI does so only
// through a standardised, validated UPI ID — a readable username with the
// category suffix of its registration and an "@valid" handle with the
// bank's name, for example abc.brk@validhdfc — available from 1 October
// 2025 (para 8), old IDs to be stopped within 180 days (para 6.1.2). The
// suffixes are in Annexure B (UPICategories): brk for stock brokers, dp for
// depository participants, ra for research analysts, ia for investment
// advisers, and others. An intermediary with several registrations applies
// for a handle for each category separately (Annexure F). The RA and IA
// master circulars of 6 February 2026 (paras 26 and 28) refer RAs and IAs
// to it. The QR code is generated on SEBI's SI portal (the white thumbs-up
// in a green triangle, the UPI ID under it) and shown on the website, the
// client portal and payment pages.
// NSE/COMP/75700 (11 August 2026): every account whose number or QR is
// given to investors for payment is linked to an @valid UPI ID, so it can
// be verified through SEBI Check.
//
// Ledgerline does not start payments. It takes the firm's handles of the
// categories it holds a registration for: brk (broking: client money), dp
// (when the firm is a DP: DP bills, posted to the client's ledger), and ra
// and ia (when the firm is registered as a research analyst or investment
// adviser: the firm's fees). It shows the client-money handles and the QR
// in the portal's Add funds panel (the bank account as the alternative),
// and tags statement credits: paid to one of the firm's client-money
// handles (a receipt as usual), to its research or advisory fee handle
// (the firm's own money, kept out of every client's ledger: bank.go), or
// to a UPI ID that is not one, which is raised for the compliance officer.
// Ledgerline has no research or advisory fee module: fees collected
// through the ra and ia handles are expected in the firm's own account.

const cUPICollect = "upicollect"

// MaxUPIQRBytes caps the QR image.
const MaxUPIQRBytes = 256 << 10

// UPIHandle is one validated UPI ID of the firm.
type UPIHandle struct {
	VPA     string `json:"vpa"`
	Bank    string `json:"bank,omitempty"`
	Account string `json:"account,omitempty"` // the bank account it credits: the client bank account for brk and dp, the firm's own for ra and ia
	// Category is the suffix in the ID (brk, dp, ra, ia) and Purpose what
	// the money is in Ledgerline (UPIClientMoney or UPIFirmFee).
	Category string `json:"category,omitempty"`
	Purpose  string `json:"purpose,omitempty"`
}

// What money a validated UPI ID collects.
const (
	UPIClientMoney = "client money"
	UPIFirmFee     = "firm's fees"
)

// UPICategory is an intermediary category of Annexure B of SEBI/HO/DEPA-II/
// DEPA-II_SRG/P/CIR/2025/86 and its username suffix.
type UPICategory struct {
	Suffix string `json:"suffix"`
	Name   string `json:"name"`
	// Purpose is what Ledgerline does with money paid to such an ID; ""
	// for a category Ledgerline does not collect for.
	Purpose string `json:"purpose,omitempty"`
	// Reg is the firm profile registration it needs ("" none; "dp" the
	// firm is a depository participant).
	Reg string `json:"reg,omitempty"`
}

// UPICategories are Annexure B's investor-facing intermediaries, in its
// order.
var UPICategories = []UPICategory{
	{"brk", "Stock broker", UPIClientMoney, ""},
	{"bti", "Banker to an issue", "", ""},
	{"dp", "Depository participant", UPIClientMoney, "dp"},
	{"ra", "Research analyst", UPIFirmFee, "ra"},
	{"ia", "Investment adviser", UPIFirmFee, "ia"},
	{"invit", "Infrastructure investment trust", "", ""},
	{"mf", "Mutual fund", "", ""},
	{"pms", "Portfolio manager", "", ""},
	{"sreit", "SM REIT", "", ""},
	{"reit", "Real estate investment trust", "", ""},
}

func upiCategory(suffix string) (UPICategory, bool) {
	for _, c := range UPICategories {
		if c.Suffix == suffix {
			return c, true
		}
	}
	return UPICategory{}, false
}

// upiCategoryOf reads the category suffix of a validated UPI ID.
func upiCategoryOf(vpa string) string {
	m := reValidUPI.FindStringSubmatch(normVPA(vpa))
	if m == nil {
		return ""
	}
	return m[1]
}

// UPICollect is the firm's UPI collection set-up.
type UPICollect struct {
	Handles []UPIHandle `json:"handles"`
	// QR is the SEBI-portal QR code as a data: URL (PNG or JPEG).
	QR     string `json:"qr,omitempty"`
	QRName string `json:"qr_name,omitempty"`
	By     string `json:"by,omitempty"`
	At     string `json:"at,omitempty"`
}

// reValidUPI is a validated UPI ID: username.<category>@valid<bank>
// (SEBI/HO/DEPA-II/DEPA-II_SRG/P/CIR/2025/86, para 2.1); the category is
// the submatch.
var reValidUPI = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*\.([a-z]+)@valid[a-z]+$`)

// upiAllowed says which categories the firm can use: brk always (a stock
// broker), dp when it is a depository participant, ra and ia when the firm
// profile lists those registrations. Each says why not when it is not.
func (o *Ops) upiAllowed(ctx context.Context, tenant string) map[string]string {
	f := o.firmFacts(ctx, tenant)
	out := map[string]string{}
	for _, c := range UPICategories {
		switch {
		case c.Purpose == "":
			out[c.Suffix] = fmt.Sprintf("Ledgerline does not collect money for a %s (.%s); only broking (.brk), DP (.dp), research (.ra) and advisory (.ia) IDs", strings.ToLower(c.Name), c.Suffix)
		case c.Reg == "dp" && f.dp() != triYes:
			out[c.Suffix] = "the firm is not recorded as a depository participant: answer it in Settings → Firm profile"
		case (c.Reg == "ra" || c.Reg == "ia") && !f.Regs[c.Reg]:
			out[c.Suffix] = fmt.Sprintf("the firm profile does not list the %s registration: add it in Settings → Firm profile", strings.ToLower(c.Name))
		default:
			out[c.Suffix] = ""
		}
	}
	return out
}

// UPICollectConfig is the firm's set-up.
func (o *Ops) UPICollectConfig(ctx context.Context, tenant string) UPICollect {
	c, _, _ := docs.GetOr(ctx, o.Docs, tenant, cUPICollect, "main", UPICollect{})
	if c.Handles == nil {
		c.Handles = []UPIHandle{}
	}
	return c
}

// handle finds one of the firm's validated handles.
func (c UPICollect) handle(vpa string) (UPIHandle, bool) {
	vpa = normVPA(vpa)
	for _, h := range c.Handles {
		if h.VPA == vpa {
			if h.Category == "" { // saved before categories were kept
				h.Category = upiCategoryOf(h.VPA)
			}
			if cat, ok := upiCategory(h.Category); ok && h.Purpose == "" {
				h.Purpose = cat.Purpose
			}
			return h, true
		}
	}
	return UPIHandle{}, false
}

// clientMoneyIDs are the handles a client pays into its account with:
// brk for a trading client; for a client of the DP only, the dp handles
// (the brk ones when the firm has none). Never a fee handle.
func (c UPICollect) clientMoneyIDs(dpOnly bool) []string {
	var brk, dp []string
	for _, h := range c.Handles {
		h, _ = c.handle(h.VPA)
		switch h.Category {
		case "brk":
			brk = append(brk, h.VPA)
		case "dp":
			dp = append(dp, h.VPA)
		}
	}
	if dpOnly && len(dp) > 0 {
		return dp
	}
	return brk
}

// UPICategoryState is a category and whether the firm can use it.
type UPICategoryState struct {
	UPICategory
	Allowed bool   `json:"allowed"`
	Why     string `json:"why,omitempty"`
}

// UPICollectView is the set-up with the categories the firm can use.
type UPICollectView struct {
	UPICollect
	Categories []UPICategoryState `json:"categories"`
}

// UPICollectFor is the set-up for Settings: each handle with its category
// and purpose, and every category of Annexure B with whether the firm's
// registrations allow it.
func (o *Ops) UPICollectFor(ctx context.Context, tenant string) UPICollectView {
	c := o.UPICollectConfig(ctx, tenant)
	for i, h := range c.Handles {
		c.Handles[i], _ = c.handle(h.VPA)
	}
	v := UPICollectView{UPICollect: c}
	allowed := o.upiAllowed(ctx, tenant)
	for _, cat := range UPICategories {
		v.Categories = append(v.Categories, UPICategoryState{UPICategory: cat, Allowed: allowed[cat.Suffix] == "", Why: allowed[cat.Suffix]})
	}
	return v
}

// SaveUPICollect sets the handles and the QR (admin or compliance officer).
// An empty QR keeps the saved one; "-" removes it.
func (o *Ops) SaveUPICollect(ctx context.Context, tenant, actor string, c UPICollect) (UPICollect, error) {
	if err := o.require(ctx, tenant, actor, "Changing the firm's UPI collection IDs", RoleCompliance); err != nil {
		return c, err
	}
	seen := map[string]bool{}
	allowed := o.upiAllowed(ctx, tenant)
	var hs []UPIHandle
	for _, h := range c.Handles {
		h.VPA, h.Bank, h.Account = normVPA(h.VPA), strings.TrimSpace(h.Bank), strings.TrimSpace(h.Account)
		if h.VPA == "" {
			continue
		}
		if !reValidUPI.MatchString(h.VPA) {
			return c, invalid("%s is not a validated UPI ID: it is the username, the category (.brk for broking money; .ra or .ia for research or advisory fees) and @valid with the bank's name, like abc.brk@validhdfc (SEBI circular SEBI/HO/DEPA-II/DEPA-II_SRG/P/CIR/2025/86 of 11 June 2025)", h.VPA)
		}
		h.Category = upiCategoryOf(h.VPA)
		cat, known := upiCategory(h.Category)
		if !known {
			return c, invalid("%s has the category .%s, which is not one of SEBI's (Annexure B of the circular of 11 June 2025: .brk, .dp, .ra, .ia and others)", h.VPA, h.Category)
		}
		if why := allowed[h.Category]; why != "" {
			return c, invalid("%s is a %s's ID: %s", h.VPA, strings.ToLower(cat.Name), why)
		}
		h.Purpose = cat.Purpose
		if h.Account != "" && !reAcctNo.MatchString(h.Account) {
			return c, invalid("the bank account for %s must be its number (6 to 18 digits)", h.VPA)
		}
		if seen[h.VPA] {
			continue
		}
		seen[h.VPA] = true
		hs = append(hs, h)
	}
	if hs == nil {
		hs = []UPIHandle{}
	}
	old := o.UPICollectConfig(ctx, tenant)
	out := UPICollect{Handles: hs, QR: old.QR, QRName: old.QRName, By: actor, At: o.now().Format(time.RFC3339)}
	switch c.QR = strings.TrimSpace(c.QR); c.QR {
	case "":
	case "-":
		out.QR, out.QRName = "", ""
	default:
		if err := checkQRImage(c.QR); err != nil {
			return c, err
		}
		out.QR, out.QRName = c.QR, strings.TrimSpace(c.QRName)
	}
	if len(out.Handles) == 0 && out.QR != "" {
		return c, invalid("add the validated UPI ID the QR code is for")
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cUPICollect, "main", out, actor); err != nil {
		return out, err
	}
	cats := map[string]int{}
	for _, h := range out.Handles {
		cats[h.Category]++
	}
	o.log(ctx, tenant, actor, "upi.collect.saved", "main", map[string]any{"handles": len(out.Handles), "categories": cats, "qr": out.QR != ""})
	return out, nil
}

// checkQRImage accepts a PNG or JPEG data: URL up to MaxUPIQRBytes.
func checkQRImage(u string) error {
	var mime string
	for _, m := range []string{"image/png", "image/jpeg"} {
		if strings.HasPrefix(u, "data:"+m+";base64,") {
			mime = m
		}
	}
	if mime == "" {
		return invalid("the QR code must be the PNG or JPEG image downloaded from SEBI's SI portal")
	}
	b, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(u, "data:"+mime+";base64,"))
	if err != nil {
		return invalid("the QR image could not be read")
	}
	if len(b) > MaxUPIQRBytes {
		return invalid("the QR image is larger than %d KB", MaxUPIQRBytes>>10)
	}
	png := bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n"))
	jpg := bytes.HasPrefix(b, []byte{0xFF, 0xD8, 0xFF})
	if mime == "image/png" && !png || mime == "image/jpeg" && !jpg {
		return invalid("the QR file is not a %s image", strings.TrimPrefix(mime, "image/"))
	}
	return nil
}

// UPI channels on a statement credit.
const (
	ChannelValidUPI   = "Firm's validated UPI handle"
	ChannelFeeUPI     = "Firm's research or advisory fee UPI handle"
	ChannelUnknownUPI = "UPI ID that is not the firm's validated handle"
)

// tagUPI marks a UPI credit with the UPI ID it was paid to. A credit to a
// client-money handle (brk, dp) is the firm's own collection; one to a
// research or advisory fee handle (ra, ia) is the firm's fee, not client
// money (matchCredit keeps it out of the client's ledger); one to any
// other UPI ID opens a work item, since old UPI IDs were to be stopped.
func (o *Ops) tagUPI(ctx context.Context, tenant string, t *BankTxn) {
	t.PayeeVPA = normVPA(t.PayeeVPA)
	if t.PayeeVPA == "" {
		return
	}
	if h, ok := o.UPICollectConfig(ctx, tenant).handle(t.PayeeVPA); ok {
		t.Channel = ChannelValidUPI
		if h.Purpose == UPIFirmFee {
			t.Channel = ChannelFeeUPI
		}
		return
	}
	t.Channel = ChannelUnknownUPI
	o.raiseWork(ctx, tenant, "upi-collect", t.Ref, "UPI credit to "+t.PayeeVPA+", not a validated UPI ID",
		"A client paid ₹"+formatINR(t.Amount.Dec())+" by UPI to "+t.PayeeVPA+" (reference "+t.Ref+"). Clients' money is collected by UPI only through the firm's validated @valid IDs (.brk for broking; SEBI circular SEBI/HO/DEPA-II/DEPA-II_SRG/P/CIR/2025/86 of 11 June 2025): stop the old ID with the bank, or add the ID in Settings → UPI collection if it is a validated one.", "", RoleCompliance, "bank", t.Date)
}
