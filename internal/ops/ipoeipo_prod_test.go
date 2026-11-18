package ops

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// All data here is made up; nothing is sent anywhere.

// fakeProducts stands in for NSE's eIPO NCB, SGB and tender calls.
type fakeProducts struct {
	masters map[string][]EIPOProductMaster
	sent    []EIPOProductOrder
	orderNo map[string]string // by Ledgerline's order
	refuse  map[string]string // by Ledgerline's order: the reason NSE refuses it
	fetch   map[string]EIPOOrderUpdate
	mism    []EIPOOrderUpdate
	inc     []SGBIncomplete
	kyc     []SGBKYC
	asked   []string
}

func newFakeProducts() *fakeProducts {
	return &fakeProducts{masters: map[string][]EIPOProductMaster{}, orderNo: map[string]string{}, refuse: map[string]string{}, fetch: map[string]EIPOOrderUpdate{}}
}

func (f *fakeProducts) ProductMaster(_ context.Context, _ EIPOConfig, product string) ([]EIPOProductMaster, error) {
	return f.masters[product], nil
}

func (f *fakeProducts) ProductSend(_ context.Context, _ EIPOConfig, product string, orders []EIPOProductOrder) ([]EIPOOrderUpdate, error) {
	var out []EIPOOrderUpdate
	for _, o := range orders {
		f.sent = append(f.sent, o)
		if why := f.refuse[o.Ref]; why != "" {
			out = append(out, EIPOOrderUpdate{Ref: o.Ref, AppNo: o.AppNo, Status: "success", OrderStatus: "EF", Reason: why})
			continue
		}
		if f.orderNo[o.Ref] == "" {
			f.orderNo[o.Ref] = fmt.Sprintf("20261016%08d", len(f.orderNo)+1)
		}
		u := EIPOOrderUpdate{Ref: o.Ref, AppNo: o.AppNo, OrderNo: f.orderNo[o.Ref], Status: "success", OrderStatus: map[string]string{"New": "ES", "Modify": "MS", "Cancel": "CS"}[o.Activity]}
		if product != IssueTender {
			u.VerStatus, u.Clearing = "P", "FP"
		}
		if product == IssueSGB && u.AppNo == "" {
			u.AppNo = "SGB" + f.orderNo[o.Ref]
		}
		out = append(out, u)
	}
	return out, nil
}

func (f *fakeProducts) ProductFetch(_ context.Context, _ EIPOConfig, _, _ string, keys []string) ([]EIPOOrderUpdate, error) {
	var out []EIPOOrderUpdate
	for _, k := range keys {
		f.asked = append(f.asked, k)
		if u, ok := f.fetch[k]; ok {
			out = append(out, u)
		}
	}
	return out, nil
}

func (f *fakeProducts) ProductMismatches(_ context.Context, _ EIPOConfig, _, _ string) ([]EIPOOrderUpdate, error) {
	return f.mism, nil
}

func (f *fakeProducts) SGBIncomplete(_ context.Context, _ EIPOConfig, _ string) ([]SGBIncomplete, error) {
	return f.inc, nil
}

func (f *fakeProducts) SGBCompleteDetails(_ context.Context, _ EIPOConfig, k SGBKYC) error {
	f.kyc = append(f.kyc, k)
	return nil
}

// productSetup connects the eIPO API, turns the products on and loads the
// fake masters: an NCB T-bill, an SGB tranche and a buy-back, bidding on
// the sample day.
func productSetup(t *testing.T) (*Ops, context.Context, *fakeProducts) {
	t.Helper()
	o, ctx := setup(t)
	o.Now = func() time.Time { return ipoAt(2026, 10, 16, 10, 30) }
	fake := newFakeProducts()
	old := eipoProducts
	eipoProducts = fake
	t.Cleanup(func() { eipoProducts = old })
	if _, err := o.SaveConnection(ctx, T, "admin", connNSEEIPO, map[string]string{"member": "90311", "login": "LLAPI01", "env": "Mock (UAT)", "send": "On"},
		map[string]string{"password": "made-up-password"}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.SetEIPOProductSettings(ctx, T, "priya.nair", EIPOProductSettings{NCB: true}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a maker turned products on: %v", err)
	}
	if _, err := o.SetEIPOProductSettings(ctx, T, "farhan.khan", EIPOProductSettings{NCB: true, SGB: true, Tender: true}); err != nil {
		t.Fatal(err)
	}
	fake.masters[IssueNCB] = []EIPOProductMaster{{Product: IssueNCB, Symbol: "TB182D", Series: "TB", Name: "182 day T-bill (made up)", LotSize: 10000, MinBidQuantity: 10000, MaxQuantity: 20000000,
		FaceValue: money.MustDec("100"), CutoffPrice: "182DTB", BiddingStartDate: "15-10-2026", BiddingEndDate: "16-10-2026", DailyStartTime: "09:00:00", DailyEndTime: "17:00:00",
		LastDayBiddingEndTime: "16-10-2026 14:30:00", T1ModStartDate: "16-10-2026", T1ModEndDate: "16-10-2026", T1ModStartTime: "15:00:00", T1ModEndTime: "16:00:00", IssueSize: 100000000}}
	fake.masters[IssueSGB] = []EIPOProductMaster{{Product: IssueSGB, Symbol: "SGBOCT26", Series: "GB", Name: "Sovereign Gold Bond", IssueType: "GB", LotSize: 1, MinBidQuantity: 1, MaxQuantity: 4000,
		MinPrice: money.MustDec("6213"), MaxPrice: money.MustDec("6263"), BiddingStartDate: "12-10-2026", BiddingEndDate: "16-10-2026", DailyStartTime: "10:00:00", DailyEndTime: "17:00:00",
		AllotmentDate: "21-10-2026", IncompleteModEndDate: "19-10-2026"}}
	return o, ctx, fake
}

// productClient is an active individual with a demat account, a valid PAN
// and at least need free in the ledger on the sample day.
func productClient(t *testing.T, o *Ops, ctx context.Context, need money.Amount, skip ...string) Client {
	t.Helper()
	cs, _ := o.Clients(ctx, T)
	for _, c := range cs {
		if c.Status != "Active" || c.Type != "Individual" || !rePAN.MatchString(c.PAN) || c.DPOnly || contains(skip, c.UCC) {
			continue
		}
		if _, _, _, err := splitDemat(payoutDemat(c)); err != nil {
			continue
		}
		if _, hit := o.IsDebarredPAN(ctx, T, c.PAN, D); hit {
			continue
		}
		if w, err := o.Withdrawable(ctx, T, D, c.UCC); err == nil && !w.Blocked && w.Available >= need {
			return c
		}
	}
	t.Fatalf("no client with ₹%s free", need)
	return Client{}
}

func productIssueOf(t *testing.T, o *Ops, ctx context.Context, typ string) IPOIssue {
	t.Helper()
	is, _ := o.IPOIssues(ctx, T)
	for _, i := range is {
		if i.Type == typ {
			return i
		}
	}
	t.Fatalf("no %s issue", typ)
	return IPOIssue{}
}

func prodOrder(t *testing.T, o *Ops, ctx context.Context, id string) EIPOOrder {
	t.Helper()
	x, _, err := docs.Get[EIPOOrder](ctx, o.Docs, T, cEIPOOrder, id)
	if err != nil {
		t.Fatal(err)
	}
	return x
}

func prodWork(t *testing.T, o *Ops, ctx context.Context, key string) *WorkItem {
	t.Helper()
	ws, _ := o.WorkItems(ctx, T, "Open")
	for _, w := range ws {
		if w.Key == "ipo:"+key {
			return &w
		}
	}
	return nil
}

func TestEIPONCB(t *testing.T) {
	o, ctx, fake := productSetup(t)
	added, _, err := o.EIPOSyncProducts(ctx, T, "priya.nair")
	if err != nil || added != 2 {
		t.Fatalf("masters %d %v", added, err)
	}
	iss := productIssueOf(t, o, ctx, IssueNCB)
	if iss.Source != "NSE eIPO NCB master" || iss.LotSize != 10000 || iss.CloseTime != "14:30" || iss.Product == nil || iss.Product.MaxQty != 20000000 ||
		iss.Product.T1From != "2026-10-16 15:00" || iss.MandateCutoff != "" || len(iss.Categories) != 0 || iss.Status != "Open" {
		t.Fatalf("NCB issue %+v %+v", iss, iss.Product)
	}
	if a, c, err := o.EIPOSyncProducts(ctx, T, "priya.nair"); err != nil || a != 0 || c != 0 {
		t.Fatalf("again %d %d %v", a, c, err)
	}
	rich := productClient(t, o, ctx, money.MustAmount("30000"))
	in := EIPOOrderInput{Issue: iss.ID, UCC: rich.UCC, Qty: 15000, Instruction: staffSaid(o)}
	if _, err := o.PlaceEIPOOrder(ctx, T, "priya.nair", in); err == nil || !strings.Contains(err.Error(), "multiples of 10000") {
		t.Fatalf("not a multiple: %v", err)
	}
	if _, err := o.PlaceIPOBid(ctx, T, "priya.nair", IPOBidInput{Issue: iss.ID, UCC: rich.UCC, Options: opt(1, "", true), Instruction: staffSaid(o)}); err == nil || !strings.Contains(err.Error(), "Bonds, SGB") {
		t.Fatalf("IPO bid on an NCB: %v", err)
	}
	before, _ := o.clientFunds(ctx, T, D, "")
	in.Qty = 20000
	x, err := o.PlaceEIPOOrder(ctx, T, "priya.nair", in)
	if err != nil || x.Amount != money.MustAmount("20000") || x.FundsEntry == "" || x.Status != OrderDraft || x.Pending != "New" || x.Ack == "" || x.Depository == "" {
		t.Fatalf("NCB order %+v %v", x, err)
	}
	if after, _ := o.clientFunds(ctx, T, D, ""); before[rich.UCC]-after[rich.UCC] != money.MustAmount("20000") {
		t.Fatalf("money not held: %s", before[rich.UCC]-after[rich.UCC])
	}
	if _, err := o.PlaceEIPOOrder(ctx, T, "priya.nair", in); err == nil || !strings.Contains(err.Error(), "one per PAN") {
		t.Fatalf("second order: %v", err)
	}
	if ins, _ := o.IPOInstructions(ctx, T, "priya.nair", rich.UCC, x.ID, "", ""); len(ins) != 1 || ins[0].Action != "Place" {
		t.Fatalf("consent log %+v", ins)
	}
	// A client without the money is refused.
	cs, _ := o.Clients(ctx, T)
	for _, c := range cs {
		if c.Status != "Active" || c.Type != "Individual" || c.UCC == rich.UCC || !rePAN.MatchString(c.PAN) || c.DPOnly {
			continue
		}
		if _, hit := o.IsDebarredPAN(ctx, T, c.PAN, D); hit {
			continue
		}
		if w, err := o.Withdrawable(ctx, T, D, c.UCC); err == nil && !w.Blocked && w.Available < money.MustAmount("10000") {
			if _, _, _, err := splitDemat(payoutDemat(c)); err != nil {
				continue
			}
			if _, err := o.PlaceEIPOOrder(ctx, T, "priya.nair", EIPOOrderInput{Issue: iss.ID, UCC: c.UCC, Qty: 10000, Instruction: staffSaid(o)}); err == nil || !strings.Contains(err.Error(), "collect the money") {
				t.Fatalf("no funds: %v", err)
			}
			break
		}
	}
	// Sent: NSE takes it, funds pending: the desk is asked to pay in.
	res, err := o.EIPOSendOrders(ctx, T, "priya.nair", iss.ID)
	if err != nil || res.Sent != 1 || res.Placed != 1 || len(fake.sent) != 1 {
		t.Fatalf("send %+v %v", res, err)
	}
	s0 := fake.sent[0]
	if s0.Activity != "New" || s0.Qty != 20000 || s0.Ref != x.ID || len(s0.AppNo) != 16 || !strings.HasPrefix(s0.AppNo, "90311") || s0.Price.Sign() != 0 {
		t.Fatalf("sent %+v", s0)
	}
	x = prodOrder(t, o, ctx, x.ID)
	if x.Status != OrderPlaced || x.OrderNo == "" || x.Clearing != "FP" || x.VerStatus != "Pending" || x.Pending != "" {
		t.Fatalf("placed %+v", x)
	}
	if w := prodWork(t, o, ctx, "eipo-funds-"+iss.ID); w == nil || !strings.Contains(w.Title, "20,000") {
		t.Fatalf("pay-in work %+v", w)
	}
	// Status: the amount payable after the auction (in paise, ₹106 for ₹100
	// face) and a mismatch.
	fake.fetch[x.AppNo] = EIPOOrderUpdate{AppNo: x.AppNo, OrderNo: x.OrderNo, Symbol: iss.Symbol, Clearing: "FS", Amount: "2120000", VerStatus: "S"}
	fake.mism = []EIPOOrderUpdate{{AppNo: x.AppNo, OrderNo: x.OrderNo, Mismatch: "Account does not exist"}}
	if r, err := o.EIPOInquireOrders(ctx, T, "priya.nair", iss.ID); err != nil || r.Updated != 1 || r.Mismatch != 1 {
		t.Fatalf("inquiry %+v %v", r, err)
	}
	x = prodOrder(t, o, ctx, x.ID)
	if x.ExchAmount != money.MustAmount("21200") || x.Funds != money.MustAmount("21200") || x.Clearing != "FS" || x.Mismatch == "" || x.VerStatus != "Verified" {
		t.Fatalf("after inquiry %+v", x)
	}
	if prodWork(t, o, ctx, "eipo-mismatch-"+iss.ID) == nil || prodWork(t, o, ctx, "eipo-funds-"+iss.ID) != nil {
		t.Fatal("mismatch work missing, or pay-in work still open")
	}
	// Corrected in the T1 window: the modification goes to NSE.
	o.Now = func() time.Time { return ipoAt(2026, 10, 16, 15, 10) }
	if _, err := o.ModifyEIPOOrder(ctx, T, "priya.nair", x.ID, EIPOOrderChange{Instruction: staffSaid(o)}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.EIPOSendOrders(ctx, T, "priya.nair", iss.ID); err != nil || fake.sent[1].Activity != "Modify" || fake.sent[1].OrderNo != x.OrderNo {
		t.Fatalf("modify sent %+v %v", fake.sent, err)
	}
	fake.mism = nil
	fake.fetch[x.AppNo] = EIPOOrderUpdate{AppNo: x.AppNo, OrderNo: x.OrderNo, Clearing: "AL"}
	if _, err := o.EIPOInquireOrders(ctx, T, "priya.nair", iss.ID); err != nil {
		t.Fatal(err)
	}
	if x = prodOrder(t, o, ctx, x.ID); x.Status != OrderAllotted || x.Mismatch != "" || x.FundsEntry == "" {
		t.Fatalf("allotted %+v", x)
	}
	if prodWork(t, o, ctx, "eipo-mismatch-"+iss.ID) != nil {
		t.Fatal("mismatch work still open")
	}
	if _, err := o.CancelEIPOOrder(ctx, T, "priya.nair", x.ID, staffSaid(o)); !errors.Is(err, ErrState) {
		t.Fatalf("cancel an allotted order: %v", err)
	}
	if tb, _ := o.App.Ledger.TrialBalance(ctx, T, D); !tb.Balanced {
		t.Fatal("trial balance off")
	}
}

func TestEIPOSGB(t *testing.T) {
	o, ctx, fake := productSetup(t)
	if _, _, err := o.EIPOSyncProducts(ctx, T, "priya.nair"); err != nil {
		t.Fatal(err)
	}
	iss := productIssueOf(t, o, ctx, IssueSGB)
	if iss.PriceLow.String() != "6213" || iss.PriceHigh.String() != "6263" || iss.Product.IncompleteTo != "2026-10-19" || iss.Timeline.Allot != "2026-10-21" || iss.Name != "Sovereign Gold Bond SGBOCT26" {
		t.Fatalf("SGB issue %+v %+v", iss, iss.Product)
	}
	c := productClient(t, o, ctx, money.MustAmount("70000"))
	if _, err := o.PlaceEIPOOrder(ctx, T, "priya.nair", EIPOOrderInput{Issue: iss.ID, UCC: c.UCC, Qty: 4001, Instruction: staffSaid(o)}); err == nil || !strings.Contains(err.Error(), "maximum") {
		t.Fatalf("over the maximum: %v", err)
	}
	x, err := o.PlaceEIPOOrder(ctx, T, "priya.nair", EIPOOrderInput{Issue: iss.ID, UCC: c.UCC, Qty: 10, Physical: true, Instruction: staffSaid(o)})
	if err != nil || x.Amount != money.MustAmount("62630") || x.PriceKind != "Offline" || !x.Physical || x.Depository != "" || x.FundsEntry == "" {
		t.Fatalf("SGB order %+v %v", x, err)
	}
	// Revised to the online price and fewer grams.
	if x, err = o.ModifyEIPOOrder(ctx, T, "priya.nair", x.ID, EIPOOrderChange{Qty: 5, Online: true, Instruction: staffSaid(o)}); err != nil || x.Amount != money.MustAmount("31065") || x.Pending != "New" {
		t.Fatalf("revised %+v %v", x, err)
	}
	if _, err := o.EIPOSendOrders(ctx, T, "priya.nair", iss.ID); err != nil {
		t.Fatal(err)
	}
	s := fake.sent[0]
	if s.AppNo != "" || !s.Physical || s.ClientCode != c.UCC || s.Qty != 5 || s.Price.String() != "6213" {
		t.Fatalf("SGB sent %+v", s)
	}
	x = prodOrder(t, o, ctx, x.ID)
	if x.Status != OrderPlaced || x.OrderNo == "" || x.AppNo == "" {
		t.Fatalf("placed %+v", x)
	}
	// NSE lists it incomplete: the maker sends the details.
	fake.inc = []SGBIncomplete{{Symbol: iss.Symbol, OrderNo: x.OrderNo, Codes: []int{301, 306}}}
	if r, err := o.EIPOInquireOrders(ctx, T, "priya.nair", iss.ID); err != nil || r.Missing != 1 {
		t.Fatalf("inquiry %+v %v", r, err)
	}
	if w := prodWork(t, o, ctx, "eipo-incomplete-"+iss.ID); w == nil || !strings.Contains(w.Detail, "date of birth") {
		t.Fatalf("incomplete work %+v", w)
	}
	d, err := o.EIPOOrderByID(ctx, T, "priya.nair", x.ID)
	if err != nil || d.KYC == nil || len(d.KYC.Codes) != 2 || d.KYC.BankAccount != c.Bank.Account {
		t.Fatalf("detail %+v %v", d.KYC, err)
	}
	if _, err := o.CompleteSGBApplication(ctx, T, "priya.nair", x.ID, SGBKYC{BankAccount: "123456"}); err == nil || !strings.Contains(err.Error(), "date of birth") {
		t.Fatalf("missing DOB: %v", err)
	}
	if _, err := o.CompleteSGBApplication(ctx, T, "priya.nair", x.ID, SGBKYC{DOB: "1980-02-29", BankAccount: "12AB"}); err == nil || !strings.Contains(err.Error(), "digits only") {
		t.Fatalf("bad account: %v", err)
	}
	if x, err = o.CompleteSGBApplication(ctx, T, "priya.nair", x.ID, SGBKYC{DOB: "1980-02-29", BankAccount: "123456789012"}); err != nil || len(x.Incomplete) != 0 || x.KYCSent == "" {
		t.Fatalf("details %+v %v", x, err)
	}
	if len(fake.kyc) != 1 || fake.kyc[0].OrderNo != x.OrderNo || fake.kyc[0].DOB != "1980-02-29" || fake.kyc[0].Symbol != iss.Symbol {
		t.Fatalf("kyc sent %+v", fake.kyc)
	}
	// RBI allots: the money held stays for the pay-in; then a rejection on
	// another application gives its money back.
	fake.inc = nil
	fake.fetch[x.OrderNo] = EIPOOrderUpdate{OrderNo: x.OrderNo, Clearing: "RA"}
	if _, err := o.EIPOInquireOrders(ctx, T, "priya.nair", iss.ID); err != nil {
		t.Fatal(err)
	}
	if x = prodOrder(t, o, ctx, x.ID); x.Status != OrderAllotted || !x.final() {
		t.Fatalf("allotted %+v", x)
	}
	c2 := productClient(t, o, ctx, money.MustAmount("10000"), c.UCC)
	before, _ := o.clientFunds(ctx, T, D, "")
	y, err := o.PlaceEIPOOrder(ctx, T, "priya.nair", EIPOOrderInput{Issue: iss.ID, UCC: c2.UCC, Qty: 1, Instruction: staffSaid(o)})
	if err != nil || y.Depository == "" {
		t.Fatalf("demat SGB %+v %v", y, err)
	}
	if _, err := o.EIPOSendOrders(ctx, T, "priya.nair", iss.ID); err != nil {
		t.Fatal(err)
	}
	y = prodOrder(t, o, ctx, y.ID)
	fake.fetch[y.OrderNo] = EIPOOrderUpdate{OrderNo: y.OrderNo, Clearing: "RJ", ClearReason: "funds not received"}
	if _, err := o.EIPOInquireOrders(ctx, T, "priya.nair", iss.ID); err != nil {
		t.Fatal(err)
	}
	if y = prodOrder(t, o, ctx, y.ID); y.Status != OrderRejected || y.FundsEntry != "" {
		t.Fatalf("rejected %+v", y)
	}
	if after, _ := o.clientFunds(ctx, T, D, ""); after[c2.UCC] != before[c2.UCC] {
		t.Fatalf("money not released: %s", before[c2.UCC]-after[c2.UCC])
	}
}

func TestEIPOTender(t *testing.T) {
	o, ctx, fake := productSetup(t)
	// A client with free shares in the primary account.
	hs, _ := o.Holdings(ctx, T, "")
	cm, _ := o.ClientMap(ctx, T)
	var h Holding
	var c Client
	for _, x := range hs {
		cl := cm[x.UCC]
		if x.BOID == "" && wholeUnits(x.Free()) >= 10 && x.ISIN != "" && cl.Status == "Active" && rePAN.MatchString(cl.PAN) && !cl.DPOnly {
			if _, _, _, err := splitDemat(primaryDemat(cl)); err != nil {
				continue
			}
			if _, hit := o.IsDebarredPAN(ctx, T, cl.PAN, D); hit {
				continue
			}
			h, c = x, cl
			break
		}
	}
	if c.UCC == "" {
		t.Fatal("no client with free shares")
	}
	free := wholeUnits(h.Free())
	fake.masters[IssueTender] = []EIPOProductMaster{{Product: IssueTender, Symbol: "BBTEST", Series: "BB", Name: "Test Industries", LotSize: 1, MinBidQuantity: 1, MaxQuantity: 1000000,
		FaceValue: money.MustDec("10"), TickSize: money.MustDec("0.05"), CutoffPrice: "250.00", BiddingStartDate: "14-10-2026", BiddingEndDate: "20-10-2026",
		DailyStartTime: "10:00:00", DailyEndTime: "17:00:00", ISIN: h.ISIN, Registrar: "XYZ Registrars"}}
	if _, _, err := o.EIPOSyncProducts(ctx, T, "priya.nair"); err != nil {
		t.Fatal(err)
	}
	iss := productIssueOf(t, o, ctx, IssueTender)
	if iss.Product.Kind != "Buy-back" || iss.tenderPrice().String() != "250" || iss.PriceHigh.String() != "250" || len(iss.Categories) != 1 || iss.Categories[0].Code != "IND" {
		t.Fatalf("tender issue %+v %+v", iss, iss.Product)
	}
	in := EIPOOrderInput{Issue: iss.ID, UCC: c.UCC, Qty: free + 1, Instruction: staffSaid(o)}
	if _, err := o.PlaceEIPOOrder(ctx, T, "priya.nair", in); err == nil || !strings.Contains(err.Error(), "cannot be tendered") {
		t.Fatalf("more than held: %v", err)
	}
	in.Qty, in.Price = 10, money.MustDec("260")
	if _, err := o.PlaceEIPOOrder(ctx, T, "priya.nair", in); err == nil || !strings.Contains(err.Error(), "offer price") {
		t.Fatalf("a buy-back at another price: %v", err)
	}
	in.Price = money.Dec{}
	before, _ := o.clientFunds(ctx, T, D, "")
	x, err := o.PlaceEIPOOrder(ctx, T, "priya.nair", in)
	if err != nil || x.Amount != money.MustAmount("2500") || x.FundsEntry != "" || x.Category != "IND" || x.Price.String() != "250" {
		t.Fatalf("tender %+v %v", x, err)
	}
	if now, _ := o.clientFunds(ctx, T, D, ""); now[c.UCC] != before[c.UCC] {
		t.Fatal("a tender held money")
	}
	if _, err := o.EIPOSendOrders(ctx, T, "priya.nair", iss.ID); err != nil {
		t.Fatal(err)
	}
	if s := fake.sent[0]; s.Series != "BB" || s.Category != "IND" || s.Physical || s.ClientCode != c.UCC || s.Qty != 10 || s.Price.String() != "250" {
		t.Fatalf("tender sent %+v", s)
	}
	if prodWork(t, o, ctx, "tender-block-"+iss.ID+"-2026-10-16") == nil {
		t.Fatal("no work item to confirm the block")
	}
	// After the close the tick asks for the acceptance.
	o.Now = func() time.Time { return ipoAt(2026, 10, 21, 10, 0) }
	o.eipoProductTick(ctx, T, eipoConn{cfg: EIPOConfig{Member: "90311"}, usable: true, send: true}, Settings{}, o.now())
	if prodWork(t, o, ctx, "tender-accept-"+iss.ID) == nil {
		t.Fatal("no acceptance work item")
	}
	if _, err := o.RecordTenderAcceptance(ctx, T, "priya.nair", iss.ID, "2026-10-21", money.Dec{}, nil); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a maker settled: %v", err)
	}
	if _, err := o.RecordTenderAcceptance(ctx, T, "farhan.khan", iss.ID, "2026-10-21", money.Dec{}, []TenderAcceptance{{Order: x.ID, Qty: 11}}); err == nil {
		t.Fatal("accepted more than tendered")
	}
	res, err := o.RecordTenderAcceptance(ctx, T, "farhan.khan", iss.ID, "2026-10-21", money.Dec{}, []TenderAcceptance{{Order: x.ID, Qty: 6}})
	if err != nil || res.Partly != 1 || res.Posted != 1 || res.Proceeds != money.MustAmount("1500") {
		t.Fatalf("acceptance %+v %v", res, err)
	}
	x = prodOrder(t, o, ctx, x.ID)
	if x.Status != OrderPartly || x.AcceptedQty != 6 || x.ProceedsEntry == "" {
		t.Fatalf("accepted %+v", x)
	}
	hs2, _ := o.Holdings(ctx, T, c.UCC)
	for _, y := range hs2 {
		if y.ISIN == h.ISIN && y.BOID == "" && y.Qty.Cmp(h.Qty.Sub(money.DecInt(6))) != 0 {
			t.Fatalf("holding %s, was %s", y.Qty, h.Qty)
		}
	}
	if after, _ := o.clientFunds(ctx, T, "2026-10-21", ""); after[c.UCC]-before[c.UCC] < money.MustAmount("1500") {
		t.Fatalf("proceeds not credited: %s", after[c.UCC]-before[c.UCC])
	}
	if prodWork(t, o, ctx, "tender-settle-"+iss.ID) == nil {
		t.Fatal("no follow-up work item")
	}
	if tb, _ := o.App.Ledger.TrialBalance(ctx, T, "2026-10-21"); !tb.Balanced {
		t.Fatal("trial balance off")
	}
}

// TestEIPOProductsSkipIPOFlow: the IPO screens and ticks leave NCB, SGB
// and tender issues alone; orders refused by NSE and never placed lapse
// after the close with their money released.
func TestEIPOProductsSkipIPOFlow(t *testing.T) {
	o, ctx, fake := productSetup(t)
	if _, _, err := o.EIPOSyncProducts(ctx, T, "priya.nair"); err != nil {
		t.Fatal(err)
	}
	iss := productIssueOf(t, o, ctx, IssueNCB)
	if _, err := o.IPOBidFile(ctx, T, "priya.nair", iss.ID, "NSE"); err == nil || !strings.Contains(err.Error(), "eIPO API") {
		t.Fatalf("bid file for an NCB: %v", err)
	}
	if _, err := o.RecordIPOAllotment(ctx, T, "priya.nair", iss.ID, "2026-10-17", nil); err == nil {
		t.Fatal("IPO allotment for an NCB")
	}
	if ms, _ := o.IPOMandates(ctx, T, "priya.nair"); len(ms) != 0 {
		t.Fatalf("mandate tracker lists products: %+v", ms)
	}
	c := productClient(t, o, ctx, money.MustAmount("10000"))
	before, _ := o.clientFunds(ctx, T, D, "")
	x, err := o.PlaceEIPOOrder(ctx, T, "priya.nair", EIPOOrderInput{Issue: iss.ID, UCC: c.UCC, Qty: 10000, Instruction: staffSaid(o)})
	if err != nil {
		t.Fatal(err)
	}
	fake.refuse[x.ID] = "Invalid Ben. Id"
	// The tick sends it (bidding is open); NSE refuses it: a work item.
	o.ipoTick(ctx, T, o.now())
	if x = prodOrder(t, o, ctx, x.ID); x.Status != OrderRefused || x.Reason != "Invalid Ben. Id" || len(fake.sent) != 1 {
		t.Fatalf("refused %+v (%d sent)", x, len(fake.sent))
	}
	if prodWork(t, o, ctx, "eipo-refused-"+iss.ID) == nil {
		t.Fatal("no work item for the refused order")
	}
	// The next day, after the close and the T1 window: it lapses.
	o.Now = func() time.Time { return ipoAt(2026, 10, 19, 10, 0) }
	o.ipoTick(ctx, T, o.now())
	if x = prodOrder(t, o, ctx, x.ID); x.Status != OrderRejected || x.FundsEntry != "" {
		t.Fatalf("lapsed %+v", x)
	}
	if after, _ := o.clientFunds(ctx, T, "2026-10-19", ""); after[c.UCC] != before[c.UCC] {
		t.Fatalf("money not released: %s", before[c.UCC]-after[c.UCC])
	}
	if prodWork(t, o, ctx, "eipo-refused-"+iss.ID) != nil {
		t.Fatal("refused work still open")
	}
	if done := o.ipoDone(ctx, T, iss.ID); done["close:"+iss.Close] != "" {
		t.Fatal("the UPI mandate step ran for an NCB")
	}
}

// fakeQuery stands in for NSE's eIPO query server.
type fakeQuery struct{ calls int }

func (f *fakeQuery) Demand(_ context.Context, _ EIPOQueryConfig, _ string) ([]EIPODemandPoint, error) {
	f.calls++
	return []EIPODemandPoint{{CutOff: true, AbsQty: 100, CumQty: 100, AbsBids: 1, CumBids: 1}, {Price: money.MustDec("100"), AbsQty: 300, CumQty: 400, AbsBids: 3, CumBids: 4}}, nil
}

func (f *fakeQuery) CatWise(_ context.Context, _ EIPOQueryConfig, _ string) ([]EIPOCatDemand, error) {
	return []EIPOCatDemand{{Category: "RETAIL", SubCategory: "IND", Qty: 400, Bids: 4}}, nil
}

func TestEIPODemand(t *testing.T) {
	o, ctx := setup(t)
	o.Now = func() time.Time { return ipoAt(2026, 10, 28, 11, 0) }
	fq := &fakeQuery{}
	old := eipoQuery
	eipoQuery = fq
	t.Cleanup(func() { eipoQuery = old })
	iss := mainboard(t, o, ctx, "DEMCO")
	if _, err := o.IssueDemand(ctx, T, "priya.nair", iss.ID, true); !errors.Is(err, ErrState) {
		t.Fatalf("without the connection: %v", err)
	}
	if _, err := o.SaveConnection(ctx, T, "admin", connNSEEIPO, map[string]string{"member": "90311", "login": "LLAPI01", "env": "Mock (UAT)", "send": "Off"}, map[string]string{"password": "made-up-password"}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.SaveConnection(ctx, T, "admin", connNSEEIPOQuery, map[string]string{"login": "QRY01"}, nil); err != nil {
		t.Fatal(err)
	}
	q, err := o.eipoQueryConn(ctx, T)
	if err != nil || q.Member != "90311" || q.Login != "QRY01" || q.Password != "made-up-password" || q.Base != eipoQueryLive {
		t.Fatalf("query connection %+v %v", q, err)
	}
	d, err := o.IssueDemand(ctx, T, "priya.nair", iss.ID, true)
	if err != nil || len(d.ByPrice) != 2 || len(d.ByCategory) != 1 || d.TotalQty != 400 || d.At == "" || fq.calls != 1 {
		t.Fatalf("demand %+v %v", d, err)
	}
	// Asked again a minute later: the last reading.
	o.Now = func() time.Time { return ipoAt(2026, 10, 28, 11, 1) }
	if d, err = o.IssueDemand(ctx, T, "priya.nair", iss.ID, true); err != nil || fq.calls != 1 || d.TotalQty != 400 {
		t.Fatalf("throttled %+v %v (%d calls)", d, err, fq.calls)
	}
	o.Now = func() time.Time { return ipoAt(2026, 10, 28, 11, 5) }
	if _, err = o.IssueDemand(ctx, T, "priya.nair", iss.ID, false); err != nil || fq.calls != 1 {
		t.Fatalf("not asked to refresh: %v (%d calls)", err, fq.calls)
	}
	if _, err = o.IssueDemand(ctx, T, "priya.nair", iss.ID, true); err != nil || fq.calls != 2 {
		t.Fatalf("refresh: %v (%d calls)", err, fq.calls)
	}
}

// newProductFake is a stand-in eIPO server answering the products' calls
// with the protocol's sample replies (their stray spaces in keys, numbers
// as strings and nested lists included), and the query server.
func newProductFake(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/eipo/mktdata/v3/") {
			if r.Header.Get("Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte("90311^QRY01:qpw-made-up")) {
				w.WriteHeader(401)
				return
			}
			if r.Method == http.MethodGet {
				w.WriteHeader(405) // this stand-in takes POST, as the document's tables say
				return
			}
			switch {
			case strings.HasSuffix(r.URL.Path, "/demand/TESTSYM01"):
				w.Write([]byte(`{"symbol":"TESTSYM01","status":"success","demand":[{"cutOffIndicator":true,"absoluteQuantity":100,"cumulativeQuantity":100,"absoluteBidCount":1,"cumulativeBidCount":1},
					{"cutOffIndicator":false,"price":121.00,"absoluteQuantity":200,"cumulativeQuantity":300,"absoluteBidCount":2,"cumulativeBidCount":3}]}`))
			case strings.HasSuffix(r.URL.Path, "/catwise/TESTSYM01"):
				w.Write([]byte(`{"symbol":"TESTSYM01","status":"success","demand":[{"category":"QIB","subCategory":"IC","quantity":100,"bidCount":1},{"category":"RETAIL","subCategory":"IND","quantity":500,"bidCount":5}]}`))
			default:
				w.Write([]byte(`{"status":"failed","reason":"Invalid Symbol"}`))
			}
			return
		}
		p := strings.TrimPrefix(r.URL.Path, "/eipo/v3/")
		if p == "login" {
			w.Write([]byte(`{"member":"90311","loginId":"LLAPI01","status":"success","token":"tok-p"}`))
			return
		}
		if r.Header.Get("Authorization") != "Bearer tok-p" {
			w.WriteHeader(401)
			return
		}
		var body any
		json.NewDecoder(r.Body).Decode(&body)
		m, _ := body.(map[string]any)
		switch p {
		case "ncbmaster":
			w.Write([]byte(`Success: {"data":[],"status":"success"}`)) // the document's layout, not JSON
		case "sgbmaster":
			w.Write([]byte(`{"data":[{"biddingStartDate":"13-01-2017","symbol":"TEST2","minBidQuantity":100,"maxQuantity":100000,"lotSize":100,"t1ModEndDate":"03-07-2019","dailyStartTime":"09:00:00",
				"allotmentDate":"","t1ModStartTime":"07:00:00","biddingEndDate":"02-07-2019","t1ModEndTime":"13:00:00","dailyEndTime":"23:00:00","tickSize":10000,"series":"GB","faceValue":10000,
				"minPrice":0,"onlinePrice":3000,"offlinePrice":3500,"t1ModStartDate":"03-07-2019","issueValueSize":0,"name":"Sovereign Gold Bond","issueSize":100000000,"incompleteModEndDate ":"05-07-2019","maxPrice":0,"isin":"INE00124571A"}],"status":"success"}`))
		case "tendermaster":
			w.Write([]byte(`{"status":"success","data":[{"symbol":"TEST","series":"BB","name":"Test Industries","lotSize":1,"faceValue":10.00,"minBidQuantity":"100 ","minPrice":12.00,"maxPrice":12.50,
				"tickSize":0.05,"cutoffPrice":12.50,"biddingStartDate":"01-01-2019","biddingEndDate":"03-01-2019","dailyStartTime":"10:00:00","dailyEndTime":"17:00:00","isin":"INE121212121",
				"issueSize":150000000,"issueValueSize":0.00,"registrar":"XYZ Registrars","maxQuantity":4563}]}`))
		case "ncb/add":
			if m["investmentValue"] != json.Number("20000") && fmt.Sprint(m["investmentValue"]) != "20000" || m["activityType"] != "N" || m["physicalDematFlag"] != "D" || m["clientRefNumber"] != "NCB-0000001" || m["price"] != nil || m["dpId"] != nil {
				w.Write([]byte(`{"status":"failed","reason":"bad request"}`))
				return
			}
			w.Write([]byte(`{"symbol":"TEST","orderNumber":2019042500000003,"series":"GS","applicationNumber":"9031100000000001","investmentValue":20000,"price":10600,"totalAmountPayable":2120000,
				"physicalDematFlag":"D","pan":"AFAKA2323L","depository":"CDSL","clientBenId":"1208160000000001","clientRefNumber":"NCB-0000001","orderStatus ":"ES","rejectionReason ":null,
				"enteredBy ":"samir","entryTime ":"25-04-2019 12:39:01","verificationStatus ":"P","verificationReason ":null,"clearingStatus ":"FP","clearingReason ":"","LastActionTime ":"25-04-2019 12:39:01","status":"success"}`))
		case "ncb/fetch":
			if m["applicationNumber"] != "9031100000000001" {
				w.Write([]byte(`{"status":"failed","reason":"No Records Found"}`))
				return
			}
			w.Write([]byte(`{"transactions":[{"symbol":"TEST","clearingStatus":"AL","orderNumber":2019042500000003,"depository":"CDSL","clearingReason":"","applicationNumber":"9031100000000001",
				"verificationStatus":"S","physicalDematFlag":"D","LastActionTime":"16-01-2017 12:17:52","orderStatus":"ES","investmentValue":20000,"price":10600,"totalAmountPayable":2120000,"status":"success"}],"status":"success"}`))
		case "ncb/mismatches/TEST":
			// The sample names the list "notifications".
			w.Write([]byte(`{"status":"success","notifications":[{"symbol":"TEST","orderNumber":2019042500000003,"applicationNumber":"9031100000000001","physicalDematFlag":"D","depository":"NSDL","reason":"Account does not exist"}]}`))
		case "sgb/add":
			l, _ := body.([]any)
			if len(l) != 1 {
				w.WriteHeader(400)
				return
			}
			o := l[0].(map[string]any)
			if o["activityType"] != "ER" || o["physicalDematFlag"] != "P" || o["clientCode"] != "LL0001" || o["depository"] != nil {
				w.Write([]byte(`[{"status":"failed","reason":"bad request"}]`))
				return
			}
			w.Write([]byte(`[{"symbol":"TEST","orderNumber":2019042500000003,"quantity":100,"applicationNumber":"1200299929020","price":55,"physicalDematFlag":"P","orderStatus":"ES",
				"clientRefNumber":"SGB-0000002","enteredBy ":"samir","verificationStatus ":"P","clearingStatus ":"FP","status":"success"}]`))
		case "sgb/fetch":
			// The sample's nested list.
			w.Write([]byte(`{"transactions":[[{"symbol":"TEST","orderNumber":2019042500000003,"quantity":100,"applicationNumber":"1200299929020","orderStatus":"ES","clearingStatus":"FR","status":"success"}]],"status":"success"}`))
		case "sgb/incomplete/TEST":
			w.Write([]byte(`{"status":"success","incomplete":[{"symbol":"TEST","orderNumber":2022042500000005,"incompleteCodesList":[301,308]}]}`))
		case "sgb/incomplete/update":
			l, _ := body.([]any)
			o, _ := l[0].(map[string]any)
			if o["firstHolderDOB"] != "25-10-1986" || o["firstHolderBankAccNo"] != "911010056464605" || o["orderNumber"] == nil {
				w.Write([]byte(`[{"status":"failed","reason":"bad request"}]`))
				return
			}
			w.Write([]byte(`[{"status":"success"}]`))
		case "tender/add":
			if m["activityType"] != "ER" || m["bidQuantity"] == nil || m["reference"] != "TND-0000003" || m["folioNo"] != "ABF001" || m["certificate1"] != "21035" || m["physicalDematFlag"] != "P" {
				w.Write([]byte(`{"status":"failed","reason":"bad request"}`))
				return
			}
			// The sample: a rejection reason, no order status.
			w.Write([]byte(`{"symbol":"TEST"," series ":"BB","applicationNumber":"9031100000000002","category":"IND","bidQuantity":100,"price":55.30,"reason":"Orders not allowed during this time of day. ",
				"physical/DematFlag":"P","referenceNumber":"TND-0000003","orderNumber":"20190406","rejectionReason":"Orders not allowed during this time of day. ","activityType":"ER","status":"success","rtaStatus":"NA","amount":"100000.00","custodianStatus":"NA"}`))
		case "tender/fetch":
			w.Write([]byte(`{"status":"success","transactions":[{"symbol":"TEST","series ":"BB","applicationNumber":"9031100000000002","bidQuantity":100,"price":55.30,"orderNumber":"20190406","rtaStatus":"NA","amount":9500.0,"custodianStatus":"NA","entryTime":"16-09-2026 13:01:03","rejectionReason":"","activityType":"ER","status":"success"}]}`))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestEIPOProductHTTPClient(t *testing.T) {
	srv := newProductFake(t)
	h := nseEIPOHTTP{client: srv.Client()}
	cfg := EIPOConfig{Base: srv.URL + "/eipo", Member: "90311", Login: "LLAPI01", Password: "pw-made-up-products"}
	ctx := context.Background()
	// Masters, with the samples' quirks.
	if ms, err := h.ProductMaster(ctx, cfg, IssueSGB); err != nil || len(ms) != 1 || ms[0].OnlinePrice.String() != "3000" || ms[0].IncompleteModEndDate != "05-07-2019" || ms[0].LotSize != 100 {
		t.Fatalf("SGB master %+v %v", ms, err)
	} else if iss, ok := productIssue(ms[0]); !ok || iss.PriceLow.String() != "3000" || iss.PriceHigh.String() != "3500" || iss.Product.IncompleteTo != "2019-07-05" || iss.Product.T1From != "2019-07-03 07:00" {
		t.Fatalf("SGB issue %+v %+v", iss, iss.Product)
	}
	ms, err := h.ProductMaster(ctx, cfg, IssueTender)
	if err != nil || len(ms) != 1 || ms[0].MinBidQuantity != 100 || ms[0].CutoffPrice != "12.50" || ms[0].Registrar != "XYZ Registrars" {
		t.Fatalf("tender master %+v %v", ms, err)
	}
	if iss, ok := productIssue(ms[0]); !ok || iss.Product.Kind != "Buy-back" || iss.tenderPrice().String() != "12.5" || iss.PriceLow.String() != "12" || iss.Open != "2019-01-01" {
		t.Fatalf("tender issue %+v %v", iss, ok)
	}
	if _, err := h.ProductMaster(ctx, cfg, IssueNCB); err == nil {
		t.Fatal("a reply that is not JSON was read")
	}
	// NCB: add, fetch, mismatches.
	ups, err := h.ProductSend(ctx, cfg, IssueNCB, []EIPOProductOrder{{Product: IssueNCB, Symbol: "TEST", Activity: "New", AppNo: "9031100000000001", Ref: "NCB-0000001", Qty: 20000,
		PAN: "AFAKA2323L", Depository: "CDSL", DPID: "12081600", BenID: "1208160000000001"}})
	if err != nil || len(ups) != 1 || ups[0].OrderStatus != "ES" || ups[0].OrderNo != "2019042500000003" || ups[0].Clearing != "FP" || ups[0].VerStatus != "P" || ups[0].Amount != "2120000" || ups[0].Ref != "NCB-0000001" {
		t.Fatalf("NCB add %+v %v", ups, err)
	}
	x := &EIPOOrder{Product: IssueNCB, Qty: 20000}
	if a := productExchAmount(x, ups[0].Amount); a != money.MustAmount("21200") {
		t.Fatalf("amount payable %s", a)
	}
	ups, err = h.ProductFetch(ctx, cfg, IssueNCB, "TEST", []string{"9031100000000001", "NOSUCH"})
	if err != nil || len(ups) != 1 || ups[0].Clearing != "AL" || ups[0].VerStatus != "S" {
		t.Fatalf("NCB fetch %+v %v", ups, err)
	}
	if mm, err := h.ProductMismatches(ctx, cfg, IssueNCB, "TEST"); err != nil || len(mm) != 1 || mm[0].Mismatch != "Account does not exist" || mm[0].AppNo != "9031100000000001" {
		t.Fatalf("NCB mismatches %+v %v", mm, err)
	}
	// SGB: a certificate application, fetch, incomplete and the update.
	ups, err = h.ProductSend(ctx, cfg, IssueSGB, []EIPOProductOrder{{Product: IssueSGB, Symbol: "TEST", Activity: "New", Ref: "SGB-0000002", Qty: 100, Price: money.MustDec("55"), Physical: true, PAN: "AFAKA2323L", ClientCode: "LL0001"}})
	if err != nil || len(ups) != 1 || ups[0].OrderStatus != "ES" || ups[0].AppNo != "1200299929020" || ups[0].OrderNo != "2019042500000003" {
		t.Fatalf("SGB add %+v %v", ups, err)
	}
	if ups, err = h.ProductFetch(ctx, cfg, IssueSGB, "TEST", []string{"2019042500000003"}); err != nil || len(ups) != 1 || ups[0].Clearing != "FR" {
		t.Fatalf("SGB fetch %+v %v", ups, err)
	}
	if inc, err := h.SGBIncomplete(ctx, cfg, "TEST"); err != nil || len(inc) != 1 || inc[0].OrderNo != "2022042500000005" || fmt.Sprint(inc[0].Codes) != "[301 308]" {
		t.Fatalf("SGB incomplete %+v %v", inc, err)
	}
	if err := h.SGBCompleteDetails(ctx, cfg, SGBKYC{Symbol: "TEST", OrderNo: "2019042500000005", DOB: "1986-10-25", BankAccount: "911010056464605"}); err != nil {
		t.Fatal(err)
	}
	if err := h.SGBCompleteDetails(ctx, cfg, SGBKYC{Symbol: "TEST", OrderNo: "2019042500000005", DOB: "1986-10-26"}); err == nil {
		t.Fatal("a refused update was taken")
	}
	// Tender of physical shares: the sample's rejection without an order
	// status is read as not taken.
	ups, err = h.ProductSend(ctx, cfg, IssueTender, []EIPOProductOrder{{Product: IssueTender, Symbol: "TEST", Series: "BB", Activity: "New", AppNo: "9031100000000002", Ref: "TND-0000003", Qty: 100,
		Price: money.MustDec("55.30"), Physical: true, PAN: "AFAKA2323L", ClientCode: "LL0001", ClientName: "Made Up Name", Category: "IND", Folio: "ABF001", Certs: []TenderCert{{No: "21035", From: "10015", To: "100125"}}}})
	if err != nil || len(ups) != 1 || ups[0].Ref != "TND-0000003" {
		t.Fatalf("tender add %+v %v", ups, err)
	}
	if ok, failed := orderOK(ups[0]); ok || !failed || !strings.Contains(ups[0].Reason, "not allowed") {
		t.Fatalf("tender rejection read as %v %v: %+v", ok, failed, ups[0])
	}
	if ups, err = h.ProductFetch(ctx, cfg, IssueTender, "TEST", []string{"9031100000000002"}); err != nil || len(ups) != 1 || ups[0].RTAStatus != "NA" || ups[0].OrderNo != "20190406" {
		t.Fatalf("tender fetch %+v %v", ups, err)
	}
	// NCB body: no price, and dpId only for NSDL.
	b, _ := json.Marshal(eipoOrderBody(EIPOProductOrder{Product: IssueNCB, Symbol: "T", Activity: "Cancel", OrderNo: "2019042500000003", Qty: 10000, Depository: "NSDL", DPID: "IN301245", BenID: "31412547"}))
	if s := string(b); !strings.Contains(s, `"activityType":"D"`) || !strings.Contains(s, `"orderNumber":2019042500000003`) || !strings.Contains(s, `"dpId":"IN301245"`) || !strings.Contains(s, `"clientBenId":31412547`) || strings.Contains(s, "price") {
		t.Fatalf("NCB body %s", s)
	}
	// The query server: Basic {member}^{login}:{password}, GET then POST.
	q := nseEIPOQueryHTTP{client: srv.Client()}
	qc := EIPOQueryConfig{Base: srv.URL + "/eipo", Member: "90311", Login: "QRY01", Password: "qpw-made-up"}
	pts, err := q.Demand(ctx, qc, "TESTSYM01")
	if err != nil || len(pts) != 2 || !pts[0].CutOff || pts[1].Price.String() != "121" || pts[1].CumQty != 300 || pts[1].AbsBids != 2 {
		t.Fatalf("demand %+v %v", pts, err)
	}
	cats, err := q.CatWise(ctx, qc, "TESTSYM01")
	if err != nil || len(cats) != 2 || cats[1].Category != "RETAIL" || cats[1].SubCategory != "IND" || cats[1].Qty != 500 {
		t.Fatalf("catwise %+v %v", cats, err)
	}
	if _, err := q.Demand(ctx, EIPOQueryConfig{Base: qc.Base, Member: "90311", Login: "QRY01", Password: "wrong"}, "TESTSYM01"); err == nil || !strings.Contains(err.Error(), "refused the login") {
		t.Fatalf("bad login: %v", err)
	}
	if _, err := q.Demand(ctx, qc, "NOSUCH"); err == nil || !strings.Contains(err.Error(), "Invalid Symbol") {
		t.Fatalf("unknown symbol: %v", err)
	}
}
