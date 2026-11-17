package ops

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// saveBankTxn stores a credit as the statement loaders do.
func (o *Ops) saveBankTxn(ctx context.Context, tenant string, t BankTxn) error {
	_, err := docs.Put(ctx, o.Docs, tenant, cBankTxn, t.ID, t, 0, "bank")
	return err
}

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

// SEBI/HO/DEPA-II/DEPA-II_SRG/P/CIR/2025/86, Annexure B: each registration
// has its own suffix. A research or advisory fee is the firm's money: it
// never reaches a client's ledger or the client-money figures.
func TestUPICategoriesAndFees(t *testing.T) {
	o, ctx := setup(t)
	// Without the registrations, only the broking ID is taken.
	for _, vpa := range []string{"bhresearch.ra@validicici", "bhadvice.ia@validicici", "bh.dp@validhdfc"} {
		if _, err := o.SaveUPICollect(ctx, T, "vikram.shah", UPICollect{Handles: []UPIHandle{{VPA: vpa}}}); err == nil || !strings.Contains(err.Error(), "Firm profile") {
			t.Fatalf("%s taken without the registration: %v", vpa, err)
		}
	}
	for _, vpa := range []string{"bh.pms@validhdfc", "bh.xyz@validhdfc"} {
		if _, err := o.SaveUPICollect(ctx, T, "vikram.shah", UPICollect{Handles: []UPIHandle{{VPA: vpa}}}); err == nil {
			t.Fatalf("%s taken", vpa)
		}
	}
	if _, err := o.SaveFirmProfile(ctx, T, "vikram.shah", FirmProfile{CDSL: AnswerYes, Registrations: []string{"ra", "ia"}}); err != nil {
		t.Fatal(err)
	}
	handles := []UPIHandle{{VPA: "bh.brk@validhdfc"}, {VPA: "bh.dp@validhdfc"}, {VPA: "bhresearch.ra@validicici", Account: "50200011112222"}, {VPA: "bhadvice.ia@validicici"}}
	if _, err := o.SaveUPICollect(ctx, T, "vikram.shah", UPICollect{Handles: handles}); err != nil {
		t.Fatal(err)
	}
	v := o.UPICollectFor(ctx, T)
	cats := map[string]UPICategoryState{}
	for _, c := range v.Categories {
		cats[c.Suffix] = c
	}
	if len(v.Categories) != 10 || !cats["ra"].Allowed || !cats["dp"].Allowed || cats["mf"].Allowed || cats["ra"].Purpose != UPIFirmFee || cats["brk"].Purpose != UPIClientMoney {
		t.Fatalf("categories %+v", v.Categories)
	}
	if v.Handles[2].Category != "ra" || v.Handles[2].Purpose != UPIFirmFee || v.Handles[0].Purpose != UPIClientMoney {
		t.Fatalf("handles %+v", v.Handles)
	}
	// Clients are shown only the client-money IDs: brk to trade, dp for a
	// client of the DP only; never a fee ID.
	cfg := o.UPICollectConfig(ctx, T)
	if ids := cfg.clientMoneyIDs(false); strings.Join(ids, ",") != "bh.brk@validhdfc" {
		t.Fatalf("trading client sees %v", ids)
	}
	if ids := cfg.clientMoneyIDs(true); strings.Join(ids, ",") != "bh.dp@validhdfc" {
		t.Fatalf("DP-only client sees %v", ids)
	}
	if _, _, err := o.PortalAccess(ctx, T, "farhan.khan", "BH1001", nil, false); err != nil {
		t.Fatal(err)
	}
	if pv, err := o.Portal(ctx, T, "bh1001", D, "BH1001"); err != nil || pv.Firm["upi_ids"] != "bh.brk@validhdfc" {
		t.Fatalf("portal shows %q %v", pv.Firm["upi_ids"], err)
	}

	// A fee paid to the RA ID from the client's own account: not a receipt.
	cl := mustClient(t, o, "BH1001")
	clients, _ := o.Clients(ctx, T)
	before, _ := o.App.Ledger.Balances(ctx, T, D)
	fundsBefore, _ := o.Funds(ctx, T, D)
	fee, err := o.matchCredit(ctx, T, "priya.nair", BankTxn{ID: "FEE1", Ref: "FEE1", Date: D, Mode: "UPI", Remitter: cl.Name, Account: cl.Bank.Account, Amount: 1180000, PayeeVPA: "BHResearch.ra@validicici"}, clients)
	if err != nil || fee.Status != BankFirmFee || fee.Channel != ChannelFeeUPI || fee.UCC != "BH1001" {
		t.Fatalf("fee credit %+v %v", fee, err)
	}
	if err := o.saveBankTxn(ctx, T, fee); err != nil {
		t.Fatal(err)
	}
	after, _ := o.App.Ledger.Balances(ctx, T, D)
	if after[ledger.ClientAccount("BH1001")] != before[ledger.ClientAccount("BH1001")] {
		t.Fatal("the fee was credited to the client's ledger")
	}
	if after[ledger.SuspenseBank]-before[ledger.SuspenseBank] != -1180000 || after[ledger.BankClient]-before[ledger.BankClient] != 1180000 {
		t.Fatal("the fee is not held apart in suspense")
	}
	fundsAfter, _ := o.Funds(ctx, T, D)
	if fundsAfter.ClientCredits.Cmp(fundsBefore.ClientCredits) != 0 || fundsAfter.Suspense.Sub(fundsBefore.Suspense).Cmp(money.DecInt(11800)) != 0 {
		t.Fatalf("client money moved: credits %s→%s suspense %s→%s", fundsBefore.ClientCredits, fundsAfter.ClientCredits, fundsBefore.Suspense, fundsAfter.Suspense)
	}
	ws, _ := o.WorkItems(ctx, T, "")
	open := false
	for _, w := range ws {
		open = open || w.Process == "upi-fee" && w.Key == "upi-fee:FEE1" && w.Status == "Open"
	}
	if !open {
		t.Fatal("no work item to move the fee out of the client bank account")
	}
	// It can never be identified to a client; it is moved, or returned.
	if _, err := o.IdentifyCredit(ctx, T, "farhan.khan", "FEE1", "BH1001", true); err == nil {
		t.Fatal("a fee identified to a client")
	}
	if _, err := o.MoveFeeCredit(ctx, T, "priya.nair", "FEE1", "NEFT 1"); err == nil {
		t.Fatal("a maker moved the fee")
	}
	if _, err := o.MoveFeeCredit(ctx, T, "farhan.khan", "FEE1", " "); err == nil {
		t.Fatal("moved without the transfer reference")
	}
	moved, err := o.MoveFeeCredit(ctx, T, "farhan.khan", "FEE1", "NEFT N123")
	if err != nil || moved.Status != BankFeeMoved {
		t.Fatalf("move %+v %v", moved, err)
	}
	end, _ := o.App.Ledger.Balances(ctx, T, o.now().Format("2006-01-02"))
	if end[ledger.SuspenseBank] != before[ledger.SuspenseBank] || end[ledger.BankClient] != before[ledger.BankClient] || end[ReceivableFees] != -1180000 || end[ledger.BankOwn]-before[ledger.BankOwn] != 1180000 {
		t.Fatalf("after the move: suspense %v client bank %v receivable %v", end[ledger.SuspenseBank], end[ledger.BankClient], end[ReceivableFees])
	}
	if _, err := o.MoveFeeCredit(ctx, T, "farhan.khan", "FEE1", "again"); err == nil {
		t.Fatal("moved twice")
	}
	ws, _ = o.WorkItems(ctx, T, "")
	for _, w := range ws {
		if w.Key == "upi-fee:FEE1" && w.Status == "Open" {
			t.Fatal("the work item stayed open")
		}
	}
	// A fee paid by mistake can be returned.
	fee2, _ := o.matchCredit(ctx, T, "priya.nair", BankTxn{ID: "FEE2", Ref: "FEE2", Date: D, Mode: "UPI", Remitter: cl.Name, Account: cl.Bank.Account, Amount: 590000, PayeeVPA: "bhadvice.ia@validicici"}, clients)
	if err := o.saveBankTxn(ctx, T, fee2); err != nil {
		t.Fatal(err)
	}
	if r, err := o.ReturnCredit(ctx, T, "priya.nair", "FEE2", "paid by mistake"); err != nil || r.Status != "Returned" {
		t.Fatalf("return %+v %v", r, err)
	}
}
