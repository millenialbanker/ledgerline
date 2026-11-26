package ops

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// All data here is made up.

// nextTestBook issues a book for the client's primary account against the
// requisition slip of the last book (the rule of dpdis.go).
func nextTestBook(t *testing.T, o *Ops, ctx context.Context, ucc string, n int64) DISBook {
	t.Helper()
	c, err := o.Client(ctx, T, ucc)
	if err != nil {
		t.Fatal(err)
	}
	req := DISBookRequest{UCC: ucc, Slips: n}
	var last *DISBook
	for _, b := range o.accountBooks(ctx, T, c, o.boFor(ctx, T, c, "").BOID) {
		if !b.Loose {
			b := b
			last = &b
		}
	}
	if last != nil {
		used := map[int64]bool{}
		for _, s := range append(last.Used, last.Lost...) {
			used[s] = true
		}
		for s := last.To; s >= last.From; s-- {
			if !used[s] {
				req.Requisition = s
				break
			}
		}
		if req.Requisition == 0 {
			req.LossRef = "test: book used up"
		}
	}
	b, err := o.IssueDISBookWith(ctx, T, "priya.nair", req)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDISBookRules(t *testing.T) {
	o, ctx := setup(t)
	siClock(o, siAt(D, "11:00"))
	c, h := cdxTestClient(t, o, ctx)
	b1, err := o.IssueDISBook(ctx, T, "priya.nair", c.UCC, 5)
	if err != nil {
		t.Fatal(err)
	}
	// A second book only against the requisition slip, or a loss report.
	if _, err := o.IssueDISBook(ctx, T, "priya.nair", c.UCC, 5); err == nil || !strings.Contains(err.Error(), "requisition slip") {
		t.Fatalf("second book without requisition: %v", err)
	}
	if _, err := o.IssueDISBookWith(ctx, T, "priya.nair", DISBookRequest{UCC: c.UCC, Slips: 5, Requisition: b1.To + 1}); err == nil {
		t.Fatal("requisition slip from outside the book accepted")
	}
	b2, err := o.IssueDISBookWith(ctx, T, "priya.nair", DISBookRequest{UCC: c.UCC, Slips: 5, Requisition: b1.To})
	if err != nil || b2.Requisition != b1.To {
		t.Fatalf("against requisition: %+v %v", b2, err)
	}
	if x, _, _ := docs.Get[DISBook](ctx, o.Docs, T, cDISBook, b1.ID); !contains(i64s(x.Used), i64s([]int64{b1.To})[0]) {
		t.Fatalf("requisition slip not used up: %+v", x)
	}
	// Slips of an old book are not taken a month after the new one.
	o.Now = func() time.Time { return siAt(addDays(D, 31), "11:00") }
	if _, err := o.EnterDIS(ctx, T, "priya.nair", DIS{UCC: c.UCC, Slip: b1.From, Kind: "Off-market", ToBO: "1208160099999999", Reason: "Gift", ExecDate: addDays(D, 31), Received: addDays(D, 31), Lines: []DISLine{{ISIN: h.ISIN, Qty: dq(1)}}}); err == nil || !strings.Contains(err.Error(), "a month after") {
		t.Fatalf("old book's slip: %v", err)
	}
	siClock(o, siAt(D, "11:00"))
	// A lost book: the unused slips of the earlier books are cancelled.
	b3, err := o.IssueDISBookWith(ctx, T, "priya.nair", DISBookRequest{UCC: c.UCC, Slips: 5, LossRef: "Letter L-1 of 16 Oct"})
	if err != nil {
		t.Fatal(err)
	}
	if x, _, _ := docs.Get[DISBook](ctx, o.Docs, T, cDISBook, b2.ID); len(x.Lost) != 5 || x.Cancelled == "" {
		t.Fatalf("lost book's slips: %+v", x)
	}
	if _, err := o.EnterDIS(ctx, T, "priya.nair", DIS{UCC: c.UCC, Slip: b2.From, Kind: "Off-market", ToBO: "1208160099999999", Reason: "Gift", ExecDate: D, Received: D, Lines: []DISLine{{ISIN: h.ISIN, Qty: dq(1)}}}); err == nil {
		t.Fatal("slip of a lost book accepted")
	}
	if !hasOpenWork(t, o, wDPCtl, "badslip:"+c.UCC) && !hasOpenWorkPrefix(t, o, wDPCtl, "badslip:"+c.UCC) {
		t.Fatal("holder not to be told of the cancelled slip")
	}
	if _, err := o.EnterDIS(ctx, T, "priya.nair", DIS{UCC: c.UCC, Slip: b3.From, Kind: "Inter-depository", ToBO: "IN30012310000001", Reason: "Gift", ExecDate: addDays(D, 11), Received: D, Lines: []DISLine{{ISIN: h.ISIN, Qty: dq(1)}}}); err == nil || !strings.Contains(err.Error(), "10 days") {
		t.Fatalf("inter-depository 11 days ahead: %v", err)
	}
	// Loose slips: in person, signed for, at most 10 a year.
	if _, err := o.IssueDISBookWith(ctx, T, "priya.nair", DISBookRequest{UCC: c.UCC, Slips: 2, Loose: true}); err == nil {
		t.Fatal("loose slips without the register entry")
	}
	if _, err := o.IssueDISBookWith(ctx, T, "priya.nair", DISBookRequest{UCC: c.UCC, Slips: 8, Loose: true, Register: "LR/12"}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.IssueDISBookWith(ctx, T, "priya.nair", DISBookRequest{UCC: c.UCC, Slips: 3, Loose: true, Register: "LR/13"}); err == nil || !strings.Contains(err.Error(), "10 loose slips") {
		t.Fatalf("eleventh loose slip: %v", err)
	}
	// POA/DDPI accounts have their own series.
	bo := o.boForRecord(ctx, T, c, "")
	bo.DDPI = true
	if err := o.putBO(ctx, T, "priya.nair", bo); err != nil {
		t.Fatal(err)
	}
	b4 := nextTestBook(t, o, ctx, c.UCC, 5)
	if b4.Series != disSeriesDDPI || b4.From != disDDPIFrom {
		t.Fatalf("DDPI series: %+v", b4)
	}
	bo.DDPI = false
	_ = o.putBO(ctx, T, "priya.nair", bo)

	// A dormant account: registered address and the compliance officer.
	if _, err := o.SaveDPControls(ctx, T, "priya.nair", DPControls{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker set the DP controls: %v", err)
	}
	if _, err := o.SaveDPControls(ctx, T, "vikram.shah", DPControls{TxnHistoryFrom: "2024-01-01"}); err != nil {
		t.Fatal(err)
	}
	n := cdslTestClient(t, o, ctx, "ABCPD7654N", "Dev Sharma", "1208160000007654")
	nb := o.boForRecord(ctx, T, n, "")
	nb.Opened = "2023-01-01"
	if err := o.putBO(ctx, T, "priya.nair", nb); err != nil {
		t.Fatal(err)
	}
	acct := holdingAcct(n, "")
	var lines []DISLine
	for _, isin := range []string{"INE000D01011", "INE000D01012", "INE000D01013", "INE000D01014", "INE000D01015"} {
		if err := docs.Upsert(ctx, o.Docs, T, cHolding, holdingIDAt(n.UCC, isin, acct), Holding{UCC: n.UCC, ISIN: isin, Symbol: "X" + isin[9:], Qty: dq(10), BOID: acct}, "test"); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, DISLine{ISIN: isin, Qty: dq(10)})
	}
	req := DISBookRequest{UCC: n.UCC, Slips: 5}
	if _, err := o.IssueDISBookWith(ctx, T, "priya.nair", req); err == nil || !strings.Contains(err.Error(), "registered address") {
		t.Fatalf("dormant book not to the registered address: %v", err)
	}
	req.Delivery = "Registered address"
	b5, err := o.IssueDISBookWith(ctx, T, "priya.nair", req)
	if err != nil || b5.AwaitingCO == "" {
		t.Fatalf("dormant book: %+v %v", b5, err)
	}
	siWant(t, siOne(t, o, ctx, "dis_books", D), SIFail, "a dormant book not authorised")
	slip := DIS{UCC: n.UCC, Slip: b5.From, Kind: "Off-market", ToBO: "1208160099999999", Reason: "Gift", ExecDate: D, Received: D, Lines: lines}
	if _, err := o.EnterDIS(ctx, T, "priya.nair", slip); err == nil || !strings.Contains(err.Error(), "authorisation") {
		t.Fatalf("slip of an unauthorised book: %v", err)
	}
	if _, err := o.AuthoriseDISBook(ctx, T, "farhan.khan", b5.ID, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("checker authorised: %v", err)
	}
	if _, err := o.AuthoriseDISBook(ctx, T, "vikram.shah", b5.ID, "Address checked"); err != nil {
		t.Fatal(err)
	}
	// The slip of a dormant account, moving 5 ISINs in full, needs the
	// call to the holder.
	d, err := o.EnterDIS(ctx, T, "priya.nair", slip)
	if err != nil || len(d.CallNeeded) != 2 || !strings.Contains(d.CallNeeded[1], "5 ISINs") {
		t.Fatalf("dormant slip: %+v %v", d.CallNeeded, err)
	}
	if _, err := o.VerifyDIS(ctx, T, "farhan.khan", d.ID, true, "", "Call ref C-1"); err == nil || !strings.Contains(err.Error(), "call to the holder") {
		t.Fatalf("verified without the call: %v", err)
	}
	if _, err := o.ApproveDISCall(ctx, T, "vikram.shah", d.ID); err == nil {
		t.Fatal("approved a call not recorded")
	}
	if _, err := o.RecordDISCall(ctx, T, "priya.nair", d.ID, DISCall{Ref: "REC-1", Number: "9000000001"}); err == nil {
		t.Fatal("call to another number accepted")
	}
	if _, err := o.RecordDISCall(ctx, T, "priya.nair", d.ID, DISCall{Ref: "REC-1", Number: n.Mobile}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.ApproveDISCall(ctx, T, "priya.nair", d.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker approved the call: %v", err)
	}
	if _, err := o.ApproveDISCall(ctx, T, "vikram.shah", d.ID); err != nil {
		t.Fatal(err)
	}
	if d, err = o.VerifyDIS(ctx, T, "farhan.khan", d.ID, true, "", "Call ref C-1"); err != nil || d.Status != "Verified" {
		t.Fatalf("verify after the call: %+v %v", d, err)
	}
	// Not operated for 6 months and every balance out: a call too.
	if _, err := o.SaveDPControls(ctx, T, "vikram.shah", DPControls{}); err != nil {
		t.Fatal(err)
	}
	if err := o.dpTxn(ctx, T, "test", DPTxn{Date: addMonths(D, -7), UCC: n.UCC, BOID: nb.BOID, ISIN: "INE000D01011", Qty: dq(1), Kind: "Off-market transfer"}); err != nil {
		t.Fatal(err)
	}
	idle := DIS{UCC: n.UCC, BOID: nb.BOID, Kind: "Off-market", ToBO: "1208160099999999", Received: D}
	idle.Lines = nil
	if _, all := o.disFullTransfers(ctx, T, n, nb, idle); !all {
		t.Fatal("the verified slip with this one does not move every balance")
	}
	if since, _ := o.dpDormant(ctx, T, n, nb, D, disIdleMonths); since == "" {
		t.Fatal("not idle for 6 months")
	}
	// The self-inspection sees the books in order.
	siWant(t, siOne(t, o, ctx, "dis_books", D), SIPass, "books")
	siWant(t, siOne(t, o, ctx, "dis_checks", D), SIPass, "slip checks")
}

func TestDISHighValueAndJoint(t *testing.T) {
	o, ctx := setup(t)
	siClock(o, siAt(D, "11:00"))
	c, h := cdxTestClient(t, o, ctx)
	book, _ := o.IssueDISBook(ctx, T, "priya.nair", c.UCC, 5)
	d, err := o.EnterDIS(ctx, T, "priya.nair", DIS{UCC: c.UCC, Slip: book.From, Kind: "Off-market", ToBO: "1208160099999999", Reason: "Gift", ExecDate: D, Received: D, Lines: []DISLine{{ISIN: h.ISIN, Qty: dq(1)}}})
	if err != nil {
		t.Fatal(err)
	}
	// Made high value (the sample's prices are small).
	if _, err := docs.Update(ctx, o.Docs, T, cDIS, d.ID, "test", func(x *DIS) error { x.Value = money.DecInt(800000); return nil }); err != nil {
		t.Fatal(err)
	}
	if d, err = o.VerifyDIS(ctx, T, "farhan.khan", d.ID, true, "", "Call ref C-9"); err != nil || d.SeniorVerified {
		t.Fatalf("verify: %+v %v", d, err)
	}
	if _, n, _ := o.DISBatch(ctx, T, "priya.nair", D); n != 0 {
		t.Fatal("a high-value slip went in the batch before the countersignature")
	}
	for _, who := range []string{"priya.nair", "farhan.khan"} {
		if _, err := o.CountersignDIS(ctx, T, who, d.ID); err == nil {
			t.Fatalf("%s countersigned", who)
		}
	}
	siWant(t, siOne(t, o, ctx, "dis_checks", D), SIPass, "verified, not yet sent")
	if d, err = o.CountersignDIS(ctx, T, "vikram.shah", d.ID); err != nil || d.Countersigned != "vikram.shah" {
		t.Fatalf("countersign: %+v %v", d, err)
	}
	if _, n, err := o.DISBatch(ctx, T, "priya.nair", D); n != 1 || err != nil {
		t.Fatalf("batch after the countersignature: %d %v", n, err)
	}
	// A joint account operated jointly: every holder signs.
	bo := o.boForRecord(ctx, T, c, "")
	bo.JointHolders = []JointHolder{{Name: "Second Holder", PAN: "ABCPS1234Q"}}
	if err := o.putBO(ctx, T, "priya.nair", bo); err != nil {
		t.Fatal(err)
	}
	if bo.operation() != JointlyOp {
		t.Fatal("joint account not Jointly by default")
	}
	d2, err := o.EnterDIS(ctx, T, "priya.nair", DIS{UCC: c.UCC, Slip: book.From + 1, Kind: "Off-market", ToBO: "1208160099999999", Reason: "Gift", ExecDate: D, Received: D, Lines: []DISLine{{ISIN: h.ISIN, Qty: dq(1)}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.VerifyDISWith(ctx, T, "farhan.khan", d2.ID, DISVerify{Approve: true}); err == nil || !strings.Contains(err.Error(), "every holder") {
		t.Fatalf("joint slip without all signatures: %v", err)
	}
	if _, err := o.VerifyDISWith(ctx, T, "farhan.khan", d2.ID, DISVerify{Approve: true, AllSigned: true}); err != nil {
		t.Fatal(err)
	}
	// Changing from Jointly needs every holder; back on one holder's.
	if _, err := o.SetJointOperation(ctx, T, "farhan.khan", c.UCC, "", AnyoneOp, 1, "REQ-1"); err == nil {
		t.Fatal("anyone-or-survivor on one holder's request")
	}
	if b, err := o.SetJointOperation(ctx, T, "farhan.khan", c.UCC, "", AnyoneOp, 2, "REQ-2"); err != nil || b.operation() != AnyoneOp {
		t.Fatalf("anyone or survivor: %+v %v", b, err)
	}
	if b, err := o.SetJointOperation(ctx, T, "farhan.khan", c.UCC, "", JointlyOp, 1, "REQ-3"); err != nil || b.operation() != JointlyOp {
		t.Fatalf("back to jointly: %+v %v", b, err)
	}
	// An on-market slip after the cut-off is taken on a best-effort basis.
	on, err := o.EnterDIS(ctx, T, "priya.nair", DIS{UCC: c.UCC, Slip: book.From + 2, Kind: "On-market", ToBO: "1100001100099999", Exchange: "NSE", Settlement: "2026200", ExecDate: D, Received: D, ReceivedAt: "11:30", Lines: []DISLine{{ISIN: h.ISIN, Qty: dq(1)}}})
	if err != nil || on.BestEffort == "" {
		t.Fatalf("early pay-in after 11:00: %+v %v", on, err)
	}
	set, _ := o.Settings(ctx, T)
	on2, err := o.EnterDIS(ctx, T, "priya.nair", DIS{UCC: c.UCC, Slip: book.From + 3, Kind: "On-market", ToBO: "1100001100099999", Exchange: "NSE", Settlement: "2026201", ExecDate: set.NextBusinessDay(D), Received: D, ReceivedAt: "15:59", Lines: []DISLine{{ISIN: h.ISIN, Qty: dq(1)}}})
	if err != nil || on2.BestEffort != "" {
		t.Fatalf("physical slip before 4 pm: %+v %v", on2, err)
	}
}

func TestDPRequestTimelines(t *testing.T) {
	o, ctx := setup(t)
	siClock(o, siAt(D, "11:00"))
	set, _ := o.Settings(ctx, T)
	n := cdslTestClient(t, o, ctx, "ABCPT4321K", "Tara Menon", "1208160000004321")
	// A demat request pending stops the closure.
	dm, err := o.RaiseDPRequest(ctx, T, "priya.nair", DPRequest{Kind: "Demat", UCC: n.UCC, Detail: map[string]string{"drf": "DRF-9", "isin": "INE999A01015", "qty": "10", "certificates": "1"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.RaiseDPRequest(ctx, T, "priya.nair", DPRequest{Kind: "Closure", UCC: n.UCC}); err == nil || !strings.Contains(err.Error(), "demat request is pending") {
		t.Fatalf("closure with a demat pending: %v", err)
	}
	if _, err := o.ActOnDPRequest(ctx, T, "priya.nair", dm.ID, "send", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := o.ActOnDPRequest(ctx, T, "farhan.khan", dm.ID, "reject", "Certificates defaced"); err != nil {
		t.Fatal(err)
	}
	dm, _, _ = docs.Get[DPRequest](ctx, o.Docs, T, cDPReq, dm.ID)
	ds := dpDuties(set, dm, addDays(D, 40))
	if len(ds) < 2 || !ds[len(ds)-2].Late {
		t.Fatalf("rejected demat duties: %+v", ds)
	}
	if _, err := o.RecordDPStep(ctx, T, "priya.nair", dm.ID, "docs_returned", D, "Courier CX-1", ""); err != nil {
		t.Fatal(err)
	}
	// A closure the DP starts: 30 days' notice, the compliance officer.
	cl, err := o.RaiseDPRequest(ctx, T, "priya.nair", DPRequest{Kind: "Closure", UCC: n.UCC, Detail: map[string]string{"initiated_by": "DP", "notice_sent": D}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.ActOnDPRequest(ctx, T, "vikram.shah", cl.ID, "approve", ""); err == nil || !strings.Contains(err.Error(), "notice runs") {
		t.Fatalf("closed within the notice: %v", err)
	}
	siClock(o, siAt(addDays(D, 31), "11:00"))
	if _, err := o.ActOnDPRequest(ctx, T, "farhan.khan", cl.ID, "approve", ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("checker closed the DP's own closure: %v", err)
	}
	if cl, err = o.ActOnDPRequest(ctx, T, "vikram.shah", cl.ID, "approve", ""); err != nil || cl.Status != "Completed" || cl.DoneOn != addDays(D, 31) {
		t.Fatalf("DP closure: %+v %v", cl, err)
	}
	ds = dpDuties(set, cl, addDays(D, 31))
	var letter DPDuty
	for _, d := range ds {
		if d.Step == "closure_letter" {
			letter = d
		}
	}
	if letter.Due != addWorkingDays(set, addDays(D, 31), 2) || letter.Done != "" {
		t.Fatalf("closure letter duty: %+v", ds)
	}
	if _, err := o.RecordDPStep(ctx, T, "priya.nair", cl.ID, "closure_letter", "", "", ""); err == nil {
		t.Fatal("closure letter without a reference")
	}
	if _, err := o.RecordDPStep(ctx, T, "priya.nair", cl.ID, "closure_letter", "", "Letter CL-1 with the final statement", ""); err != nil {
		t.Fatal(err)
	}
	// Closure with dues: told in 2 working days, 30 days to pay.
	r := DPRequest{Kind: "Closure", At: D + "T10:00:00+05:30", Status: "Pending", Detail: map[string]string{"dues": "120.00"}}
	ds = dpDuties(set, r, addDays(D, 5))
	if len(ds) == 0 || ds[0].Step != "dues_notice" || !ds[0].Late {
		t.Fatalf("dues notice duty: %+v", ds)
	}
	r.step("dues_notice", D, "x", "L-2")
	ds = dpDuties(set, r, addDays(D, 40))
	if len(ds) < 2 || !strings.Contains(ds[1].What, "Rejected with the dues") || ds[1].Due != addWorkingDays(set, addDays(D, 30), 2) {
		t.Fatalf("dues rejection duty: %+v", ds)
	}
	// Freezes: the holder's signed request; transmission: survivors'
	// nomination and the claim's category.
	siClock(o, siAt(D, "11:00"))
	m := cdslTestClient(t, o, ctx, "ABCPU5432L", "Uma Rao", "1208160000005432")
	if _, err := o.RaiseDPRequest(ctx, T, "priya.nair", DPRequest{Kind: "Freeze", UCC: m.UCC, Detail: map[string]string{"type": "Debit", "reason": "Holder's request", "by": "Client"}}); err == nil {
		t.Fatal("client freeze without the signed request")
	}
	fr, err := o.RaiseDPRequest(ctx, T, "priya.nair", DPRequest{Kind: "Freeze", UCC: m.UCC, Detail: map[string]string{"type": "Debit", "reason": "Holder's request", "by": "Client", "signed_request": "FRQ-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if fr, err = o.ActOnDPRequest(ctx, T, "farhan.khan", fr.ID, "approve", ""); err != nil {
		t.Fatal(err)
	}
	if ds := dpDuties(set, fr, D); len(ds) != 1 || ds[0].Step != "intimated" {
		t.Fatalf("freeze intimation duty: %+v", ds)
	}
	tr, err := o.RaiseDPRequest(ctx, T, "priya.nair", DPRequest{Kind: "Transmission", UCC: n.UCC, Detail: map[string]string{"to_ucc": m.UCC, "death_certificate": "DC-1", "survivors": "yes"}})
	if err == nil {
		t.Fatalf("transmission of a closed account: %+v", tr)
	}
	tr, err = o.RaiseDPRequest(ctx, T, "priya.nair", DPRequest{Kind: "Transmission", UCC: m.UCC, Detail: map[string]string{"to_ucc": n.UCC, "death_certificate": "DC-1", "survivors": "yes"}})
	if err != nil || !strings.HasPrefix(tr.Detail["category"], "Quick") || tr.Detail["survivors"] != "Yes" {
		t.Fatalf("transmission: %+v %v", tr, err)
	}
	if _, err := o.ActOnDPRequest(ctx, T, "farhan.khan", tr.ID, "approve", ""); err == nil || !strings.Contains(err.Error(), "nomination") {
		t.Fatalf("survivors without a fresh nomination: %v", err)
	}
	siWant(t, siOne(t, o, ctx, "dp_request_tat", D), SIPass, "requests on time")
}

func TestDPReconStockAndPool(t *testing.T) {
	o, ctx := setup(t)
	siClock(o, siAt(D, "19:00"))
	c, _ := cdxTestClient(t, o, ctx)
	if _, err := o.RecordDISStock(ctx, T, "priya.nair", DISStockEntry{Kind: "Received", Slips: 30}); err == nil {
		t.Fatal("receipt without the printer's reference")
	}
	if _, err := o.RecordDISStock(ctx, T, "priya.nair", DISStockEntry{Kind: "Received", Slips: 30, Ref: "PRN-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.IssueDISBook(ctx, T, "priya.nair", c.UCC, 40); err == nil || !strings.Contains(err.Error(), "in stock") {
		t.Fatalf("book above the stock: %v", err)
	}
	if _, err := o.IssueDISBook(ctx, T, "priya.nair", c.UCC, 20); err != nil {
		t.Fatal(err)
	}
	if _, err := o.RecordDISStock(ctx, T, "priya.nair", DISStockEntry{Kind: "Count", Counted: 9}); err == nil {
		t.Fatal("a count that differs, with no reason")
	}
	e, err := o.RecordDISStock(ctx, T, "priya.nair", DISStockEntry{Kind: "Count", Counted: 9, Note: "one slip spoilt in printing"})
	if err != nil || e.Diff != -1 || e.Balance != 9 {
		t.Fatalf("count: %+v %v", e, err)
	}
	siWant(t, siOne(t, o, ctx, "dis_stock", D), SIPass, "stock counted")
	// The day's reconciliation, signed by a checker.
	r, err := o.DPReconcile(ctx, T, "priya.nair", D)
	if err != nil || len(r.Lines) != len(DPReconKinds) || !r.Balanced || r.Signed != "" {
		t.Fatalf("recon: %+v %v", r, err)
	}
	if _, err := o.SignDPRecon(ctx, T, "priya.nair", D, nil, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker signed: %v", err)
	}
	if _, err := o.SignDPRecon(ctx, T, "farhan.khan", D, map[string]int{"Delivery instructions": 3}, ""); err == nil {
		t.Fatal("a CDSL difference signed with no note")
	}
	if r, err = o.SignDPRecon(ctx, T, "farhan.khan", D, nil, ""); err != nil || r.Signed != "farhan.khan" {
		t.Fatalf("sign: %+v %v", r, err)
	}
	// Pool pay-outs: one working day; the delay charge estimated.
	set, _ := o.Settings(ctx, T)
	ps := o.poolPayouts(ctx, T, D, D)
	for _, p := range ps {
		if p.Due != set.NextBusinessDay(p.PayOut) {
			t.Fatalf("pay-out due: %+v", p)
		}
	}
	siClock(o, siAt(addDays(D, 9), "19:00"))
	for _, p := range o.poolPayouts(ctx, T, D, D) {
		if p.Paid == "" && (p.Late == 0 || p.Charge.Sign() <= 0) {
			t.Fatalf("late pay-out not charged: %+v", p)
		}
	}
	// The pure estimate: 0.06% a week, a part week as a week.
	if got := money.DecInt(1000000).Pct(cdslPoolRate).MulInt(2).Round(money.UnitPaisa).Dec(); got.Cmp(money.DecInt(1200)) != 0 {
		t.Fatalf("charge %s", got)
	}
}

func TestTradeFreezeRequests(t *testing.T) {
	o, ctx := setup(t)
	siClock(o, siAt(D, "10:00"))
	set, _ := o.Settings(ctx, T)
	c := cdslTestClient(t, o, ctx, "ABCPV6543M", "Vani Iyer", "")
	// In trading hours: 15 minutes; outside: before the next session.
	if due := freezeDue(set, c, siAt(D, "10:00")); !due.Equal(siAt(D, "10:15")) {
		t.Fatalf("in hours: %v", due)
	}
	if due := freezeDue(set, c, siAt(D, "18:00")); !due.Equal(siAt(set.NextBusinessDay(D), "09:00")) {
		t.Fatalf("after hours: %v", due)
	}
	// From the registered e-mail: frozen at once.
	f, err := o.ReceiveTradeFreeze(ctx, T, "priya.nair", TradeFreezeIntake{UCC: c.UCC, Mode: "Email", From: strings.ToUpper(c.Email)})
	if err != nil || f.Status != "Frozen" || f.Validated == "" {
		t.Fatalf("freeze: %+v %v", f, err)
	}
	if x, _ := o.Client(ctx, T, c.UCC); x.TradeFrozen != f.ID || finspotBlockWhy(x) == "" {
		t.Fatalf("client not blocked: %+v", x.TradeFrozen)
	}
	if !hasOpenWork(t, o, wTradeFreeze, "cancel:"+f.ID) || !hasOpenWork(t, o, wTradeFreeze, "positions:"+f.ID) {
		t.Fatal("no work items for the cancellation and positions")
	}
	for _, st := range []string{"ack", "cancelled", "positions"} {
		if _, err := o.ActOnTradeFreeze(ctx, T, "anita.rao", f.ID, st, "ref "+st); err != nil {
			t.Fatal(err)
		}
	}
	if hasOpenWork(t, o, wTradeFreeze, "positions:"+f.ID) {
		t.Fatal("positions work item left open")
	}
	// Not from a registered contact: checked by staff first.
	g, err := o.ReceiveTradeFreeze(ctx, T, "priya.nair", TradeFreezeIntake{UCC: c.UCC, Mode: "SMS", From: "9000000009"})
	if err != nil || g.Status != "Received" {
		t.Fatalf("unvalidated: %+v %v", g, err)
	}
	siClock(o, siAt(D, "10:30"))
	if why := freezeLate(g, o.now()); len(why) == 0 {
		t.Fatal("a request past its 15 minutes not late")
	}
	if g, err = o.ActOnTradeFreeze(ctx, T, "anita.rao", g.ID, "reject", "Number not the client's; client called back"); err != nil || g.Status != "Rejected" {
		t.Fatalf("reject: %+v %v", g, err)
	}
	// Re-enabled only with due diligence.
	if _, err := o.ReenableTrading(ctx, T, "priya.nair", f.ID, TradeReenable{Mode: "Email", Diligence: "Client asked by registered e-mail; called back"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker re-enabled: %v", err)
	}
	if _, err := o.ReenableTrading(ctx, T, "anita.rao", f.ID, TradeReenable{Mode: "Email", Diligence: "ok"}); err == nil {
		t.Fatal("re-enabled without due diligence")
	}
	if f, err = o.ReenableTrading(ctx, T, "anita.rao", f.ID, TradeReenable{Mode: "Email", Diligence: "Client asked by registered e-mail; called back on the registered mobile"}); err != nil || f.Status != freezeReenableBy {
		t.Fatalf("re-enable: %+v %v", f, err)
	}
	if x, _ := o.Client(ctx, T, c.UCC); x.TradeFrozen != "" {
		t.Fatal("still frozen")
	}
	// The firm's modes: at least two, and the policy published.
	siWant(t, siOne(t, o, ctx, "trade_freeze", D), SIFail, "no modes recorded")
	if _, err := o.SaveDPControls(ctx, T, "vikram.shah", DPControls{FreezeModes: []string{"Email", "SMS"}, FreezePolicy: "https://broker.example/freeze-policy"}); err != nil {
		t.Fatal(err)
	}
	siWant(t, siOne(t, o, ctx, "trade_freeze", D), SIPass, "modes recorded, requests on time")
}

func hasOpenWorkPrefix(t *testing.T, o *Ops, process, prefix string) bool {
	t.Helper()
	ws, _ := o.WorkItems(t.Context(), T, "")
	for _, w := range ws {
		if w.Status == "Open" && w.Process == process && strings.HasPrefix(w.Key, process+":"+prefix) {
			return true
		}
	}
	return false
}
