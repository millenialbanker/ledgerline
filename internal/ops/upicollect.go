package ops

import (
	"bytes"
	"context"
	"encoding/base64"
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
// category suffix (".brk" for stock brokers) and an "@valid" handle with
// the bank's name, for example abc.brk@validhdfc — available from 1 October
// 2025, old IDs to be stopped within 180 days. The QR code is generated on
// SEBI's SI portal (the white thumbs-up in a green triangle, the UPI ID
// under it) and shown on the website, the client portal and payment pages.
// NSE/COMP/75700 (11 August 2026): every account whose number or QR is
// given to investors for payment is linked to an @valid UPI ID, so it can
// be verified through SEBI Check.
//
// Ledgerline does not start payments. It shows the handles and the QR in
// the portal's Add funds panel (the bank account as the alternative), and
// tags statement credits: paid to one of the firm's validated handles, or
// to a UPI ID that is not one, which is raised for the compliance officer.

const cUPICollect = "upicollect"

// MaxUPIQRBytes caps the QR image.
const MaxUPIQRBytes = 256 << 10

// UPIHandle is one validated UPI ID of the firm.
type UPIHandle struct {
	VPA     string `json:"vpa"`
	Bank    string `json:"bank,omitempty"`
	Account string `json:"account,omitempty"` // the client bank account it credits (masked when shown)
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

// reValidUPI is a broker's validated UPI ID: username.brk@valid<bank>.
var reValidUPI = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*\.brk@valid[a-z]+$`)

// UPICollectConfig is the firm's set-up.
func (o *Ops) UPICollectConfig(ctx context.Context, tenant string) UPICollect {
	c, _, _ := docs.GetOr(ctx, o.Docs, tenant, cUPICollect, "main", UPICollect{})
	if c.Handles == nil {
		c.Handles = []UPIHandle{}
	}
	return c
}

// validated says whether a UPI ID is one of the firm's validated handles.
func (c UPICollect) validated(vpa string) bool {
	vpa = normVPA(vpa)
	for _, h := range c.Handles {
		if h.VPA == vpa {
			return true
		}
	}
	return false
}

// SaveUPICollect sets the handles and the QR (admin or compliance officer).
// An empty QR keeps the saved one; "-" removes it.
func (o *Ops) SaveUPICollect(ctx context.Context, tenant, actor string, c UPICollect) (UPICollect, error) {
	if err := o.require(ctx, tenant, actor, "Changing the firm's UPI collection IDs", RoleCompliance); err != nil {
		return c, err
	}
	seen := map[string]bool{}
	var hs []UPIHandle
	for _, h := range c.Handles {
		h.VPA, h.Bank, h.Account = normVPA(h.VPA), strings.TrimSpace(h.Bank), strings.TrimSpace(h.Account)
		if h.VPA == "" {
			continue
		}
		if !reValidUPI.MatchString(h.VPA) {
			return c, invalid("%s is not a validated UPI ID: a broker's is the username, .brk, and @valid with the bank's name, like abc.brk@validhdfc (SEBI circular of 11 June 2025)", h.VPA)
		}
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
	o.log(ctx, tenant, actor, "upi.collect.saved", "main", map[string]any{"handles": len(out.Handles), "qr": out.QR != ""})
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
	ChannelUnknownUPI = "UPI ID that is not the firm's validated handle"
)

// tagUPI marks a UPI credit with the UPI ID it was paid to. A credit to a
// validated handle is the firm's own collection; one to any other UPI ID
// opens a work item, since old UPI IDs were to be stopped.
func (o *Ops) tagUPI(ctx context.Context, tenant string, t *BankTxn) {
	t.PayeeVPA = normVPA(t.PayeeVPA)
	if t.PayeeVPA == "" {
		return
	}
	if o.UPICollectConfig(ctx, tenant).validated(t.PayeeVPA) {
		t.Channel = ChannelValidUPI
		return
	}
	t.Channel = ChannelUnknownUPI
	o.raiseWork(ctx, tenant, "upi-collect", t.Ref, "UPI credit to "+t.PayeeVPA+", not a validated UPI ID",
		"A client paid ₹"+formatINR(t.Amount.Dec())+" by UPI to "+t.PayeeVPA+" (reference "+t.Ref+"). Clients' money is collected by UPI only through the firm's validated .brk@valid IDs (SEBI circular of 11 June 2025): stop the old ID with the bank, or add the ID in Settings → UPI collection if it is a validated one.", "", RoleCompliance, "bank", t.Date)
}
