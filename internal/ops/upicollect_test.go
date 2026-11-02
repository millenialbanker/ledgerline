package ops

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestUPICollection(t *testing.T) {
	o, ctx := setup(t)
	png := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nfake-qr"))

	if _, err := o.SaveUPICollect(ctx, T, "priya.nair", UPICollect{Handles: []UPIHandle{{VPA: "bh.brk@validhdfc"}}}); err == nil {
		t.Fatal("maker set the UPI IDs")
	}
	for _, bad := range []string{"bharat@hdfcbank", "bharat.brk@hdfc", "bharat.mf@validhdfc"} {
		if _, err := o.SaveUPICollect(ctx, T, "vikram.shah", UPICollect{Handles: []UPIHandle{{VPA: bad}}}); err == nil {
			t.Fatalf("%s accepted as a validated UPI ID", bad)
		}
	}
	if _, err := o.SaveUPICollect(ctx, T, "vikram.shah", UPICollect{Handles: []UPIHandle{{VPA: "bh.brk@validhdfc"}}, QR: "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("GIF89a"))}); err == nil {
		t.Fatal("a non-PNG accepted as the QR")
	}
	c, err := o.SaveUPICollect(ctx, T, "admin", UPICollect{Handles: []UPIHandle{{VPA: " BH.brk@ValidHDFC ", Bank: "HDFC Bank"}}, QR: png, QRName: "qr.png"})
	if err != nil || c.Handles[0].VPA != "bh.brk@validhdfc" || c.QR != png {
		t.Fatalf("save: %+v %v", c, err)
	}
	// An empty QR keeps the saved one.
	if c, _ = o.SaveUPICollect(ctx, T, "admin", UPICollect{Handles: c.Handles}); c.QR != png {
		t.Fatal("QR lost")
	}

	// The portal shows the handle and the QR, the bank account as the alternative.
	if _, _, err := o.PortalAccess(ctx, T, "farhan.khan", "BH1001", nil, false); err != nil {
		t.Fatal(err)
	}
	v, err := o.Portal(ctx, T, "bh1001", D, "BH1001")
	if err != nil || v.Firm["upi_ids"] != "bh.brk@validhdfc" || v.Firm["upi_qr"] != png {
		t.Fatalf("portal firm: %+v %v", v.Firm, err)
	}

	// Receipt matching: through the validated handle it is the firm's own
	// collection; to any other UPI ID it is raised.
	cl := mustClient(t, o, "BH1001")
	clients, _ := o.Clients(ctx, T)
	got, err := o.matchCredit(ctx, T, "priya.nair", BankTxn{ID: "UPI1", Ref: "UPI1", Date: D, Mode: "UPI", Remitter: cl.Name, Account: cl.Bank.Account, Amount: 500000, PayeeVPA: "BH.brk@validhdfc"}, clients)
	if err != nil || got.Status != "Matched" || got.Channel != ChannelValidUPI {
		t.Fatalf("validated: %+v %v", got, err)
	}
	got, err = o.matchCredit(ctx, T, "priya.nair", BankTxn{ID: "UPI2", Ref: "UPI2", Date: D, Mode: "UPI", Remitter: cl.Name, Account: cl.Bank.Account, Amount: 500000, PayeeVPA: "bharatbroking@hdfcbank"}, clients)
	if err != nil || got.Status != "Matched" || got.Channel != ChannelUnknownUPI {
		t.Fatalf("old ID: %+v %v", got, err)
	}
	ws, _ := o.WorkItems(ctx, T, "")
	found := false
	for _, w := range ws {
		found = found || w.Process == "upi-collect" && strings.Contains(w.Title, "bharatbroking@hdfcbank")
	}
	if !found {
		t.Fatal("no work item for the old UPI ID")
	}
}
