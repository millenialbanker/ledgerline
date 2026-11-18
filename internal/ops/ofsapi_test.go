package ops

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/money"
)

type fakeOFS struct {
	calls  []string
	orders []OFSOrder
	book   map[string]string // orderId -> status
	refuse string
}

func (f *fakeOFS) Place(_ context.Context, _ OFSConfig, method string, o OFSOrder) (OFSReply, error) {
	f.calls = append(f.calls, method)
	f.orders = append(f.orders, o)
	if f.refuse != "" {
		return OFSReply{Message: f.refuse}, nil
	}
	if o.OrderID == "" {
		o.OrderID = fmt.Sprintf("202610160000%04d", len(f.orders))
	}
	o.Status = map[string]string{http.MethodPost: "Requested", http.MethodPut: "Modify Requested", http.MethodDelete: "Cancel Requested"}[method]
	return OFSReply{Order: &o}, nil
}

func (f *fakeOFS) Orders(_ context.Context, _ OFSConfig) ([]OFSOrder, error) {
	var out []OFSOrder
	for id, st := range f.book {
		out = append(out, OFSOrder{OrderID: id, Symbol: "OFSCO", Status: st})
	}
	return out, nil
}

func TestOFSAPI(t *testing.T) {
	o, ctx := setup(t)
	o.Now = func() time.Time { return ipoAt(2026, 10, 16, 10, 0) }
	fake := &fakeOFS{book: map[string]string{}}
	old := ofsClient
	ofsClient = fake
	t.Cleanup(func() { ofsClient = old })
	iss, err := o.SaveIPOIssue(ctx, T, "priya.nair", IPOIssue{Symbol: "OFSCO", Type: IssueOFS, Exchanges: []string{"NSE"}, Open: "2026-10-15", Close: "2026-10-16",
		PriceLow: money.MustDec("250"), PriceHigh: money.MustDec("260"), LotSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	cs, _ := o.Clients(ctx, T)
	var rich Client
	for _, c := range cs {
		if c.Status != "Active" || c.Type != "Individual" {
			continue
		}
		if _, _, _, err := splitDemat(c.Demat); err != nil {
			continue
		}
		if w, err := o.Withdrawable(ctx, T, D, c.UCC); err == nil && w.Available >= money.MustAmount("50000") {
			rich = c
			break
		}
	}
	b, err := o.PlaceIPOBid(ctx, T, "priya.nair", IPOBidInput{Issue: iss.ID, UCC: rich.UCC, Category: "RETAIL", Options: opt(100, "", true), Instruction: staffSaid(o)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.OFSSend(ctx, T, "priya.nair", iss.ID); err == nil {
		t.Fatal("sent without a connection")
	}
	if _, err := o.SaveConnection(ctx, T, "admin", connNSEOFS, map[string]string{"member": "TESTM", "login": "0123", "env": "UAT", "send": "On"}, map[string]string{"password": "made-up"}); err != nil {
		t.Fatal(err)
	}
	// The tick places the retail bid on T+1: series RS, 100% margin, a
	// market order at cut-off with no price.
	o.ipoTick(ctx, T, o.now())
	if len(fake.orders) != 1 {
		t.Fatalf("placed %d", len(fake.orders))
	}
	p := fake.orders[0]
	if fake.calls[0] != http.MethodPost || p.Series != "RS" || p.ClientType != "CLI" || p.ClientID != rich.UCC || p.MarginType != "1" || !p.IsMarket || p.Price != "" || p.Quantity != 100 {
		t.Fatalf("order %+v", p)
	}
	a := bidOf(t, o, ctx, b.ID)
	if a.AppNo != "2026101600000001" || a.Pending != "" {
		t.Fatalf("after placing %+v", a)
	}
	// NSE's order book confirms it; nothing is sent again.
	fake.book[a.AppNo] = "Confirmed"
	if res, err := o.OFSSend(ctx, T, "priya.nair", iss.ID); err != nil || res.Sent != 0 {
		t.Fatalf("confirm %+v %v", res, err)
	}
	if a = bidOf(t, o, ctx, b.ID); a.Status != BidAccepted || a.Pending != "" {
		t.Fatalf("confirmed %+v", a)
	}
	// A change goes against NSE's order number.
	if _, err := o.ModifyIPOBid(ctx, T, "priya.nair", b.ID, IPOModifyInput{Options: opt(150, "", true), Instruction: staffSaid(o)}); err != nil {
		t.Fatal(err)
	}
	if res, err := o.OFSSend(ctx, T, "priya.nair", iss.ID); err != nil || res.Sent != 1 {
		t.Fatalf("modify %+v %v", res, err)
	}
	if m := fake.orders[1]; fake.calls[1] != http.MethodPut || m.OrderID != a.AppNo || m.Quantity != 150 {
		t.Fatalf("modify order %+v", m)
	}
	// A refusal is recorded with NSE's words.
	fake.refuse = "The order price is out of the day's price range"
	if _, err := o.CancelIPOBid(ctx, T, "priya.nair", b.ID, staffSaid(o)); err != nil {
		t.Fatal(err)
	}
	if res, err := o.OFSSend(ctx, T, "priya.nair", iss.ID); err != nil || res.Refused != 1 || fake.calls[2] != http.MethodDelete {
		t.Fatalf("cancel %+v %v %v", res, err, fake.calls)
	}
	a = bidOf(t, o, ctx, b.ID)
	found := false
	for _, h := range a.History {
		found = found || strings.Contains(h.What, "cancel rejected by the exchange: The order price is out of the day's price range")
	}
	if !found || a.Status != BidAccepted {
		t.Fatalf("refusal not recorded %+v", a.History)
	}
}
