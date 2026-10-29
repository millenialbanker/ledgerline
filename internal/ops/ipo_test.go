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

func ipoAt(y, m, d, hh, mm int) time.Time { return time.Date(y, time.Month(m), d, hh, mm, 0, 0, ist) }

// ipoClients picks active individual clients with a CDSL demat account.
func ipoClients(t *testing.T, o *Ops, ctx context.Context, n int) []Client {
	t.Helper()
	cs, _ := o.Clients(ctx, T)
	var out []Client
	pans := map[string]bool{}
	for _, c := range cs {
		if c.Status == "Active" && c.Type == "Individual" && rePAN.MatchString(c.PAN) && !pans[c.PAN] && reIFSC.MatchString(c.Bank.IFSC) && c.Bank.Account != "" {
			if dep, _, _, err := splitDemat(c.Demat); err == nil && dep == "CDSL" {
				if _, hit := o.IsDebarredPAN(ctx, T, c.PAN, "2026-10-28"); !hit {
					out = append(out, c)
					pans[c.PAN] = true
				}
			}
		}
		if len(out) == n {
			return out
		}
	}
	t.Fatalf("only %d suitable clients", len(out))
	return nil
}

func staffSaid(o *Ops) IPOInstructionIn {
	return IPOInstructionIn{Channel: IPOChannels[0], Ref: "CALL-REC-0001", Received: o.now().Add(-5 * time.Minute).Format(time.RFC3339), Text: "apply please"}
}

func vpaOf(ucc string) string { return strings.ToLower(ucc) + "@okicici" }

func regUPI(t *testing.T, o *Ops, ctx context.Context, ucc string) {
	t.Helper()
	if _, err := o.RegisterIPOUPI(ctx, T, "priya.nair", ucc, vpaOf(ucc), "name match on the bank's UPI lookup"); err != nil {
		t.Fatal(err)
	}
}

func opt(lots int64, price string, cut bool) []IPOBidOption {
	p := money.Dec{}
	if price != "" {
		p = money.MustDec(price)
	}
	return []IPOBidOption{{Lots: lots, Price: p, CutOff: cut}}
}

func mainboard(t *testing.T, o *Ops, ctx context.Context, sym string) IPOIssue {
	t.Helper()
	iss, err := o.SaveIPOIssue(ctx, T, "priya.nair", IPOIssue{Symbol: sym, Company: sym + " Ltd (made up)", Type: IssueMainboard, Exchanges: []string{"NSE", "BSE"},
		Open: "2026-10-27", Close: "2026-10-29", PriceLow: money.MustDec("95"), PriceHigh: money.MustDec("100"), LotSize: 150, Registrar: "KFintech", CommPerApp: money.MustAmount("10")})
	if err != nil {
		t.Fatal(err)
	}
	return iss
}

func bidOf(t *testing.T, o *Ops, ctx context.Context, id string) IPOApp {
	t.Helper()
	a, err := o.ipoBid(ctx, T, id)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestIPOIssueAndBidRules(t *testing.T) {
	o, ctx := setup(t)
	o.Now = func() time.Time { return ipoAt(2026, 10, 28, 11, 0) }
	if _, err := o.SaveIPOIssue(ctx, T, "vikram.shah", IPOIssue{Symbol: "X"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("compliance set up an issue: %v", err)
	}
	if _, err := o.SaveIPOIssue(ctx, T, "priya.nair", IPOIssue{Symbol: "BADCO", Exchanges: []string{"NSE"}, Open: "2026-10-29", Close: "2026-10-27", PriceLow: money.MustDec("1"), LotSize: 1}); err == nil {
		t.Fatal("closing before opening accepted")
	}
	iss := mainboard(t, o, ctx, "FICTIPO")
	if iss.Status != "Open" || iss.Timeline.Allot != "2026-10-30" || iss.Timeline.Unblock != "2026-11-02" || iss.Timeline.Listing != "2026-11-03" || len(iss.Categories) != 6 || iss.CloseTime != "16:00" {
		t.Fatalf("issue %+v", iss)
	}
	cs := ipoClients(t, o, ctx, 6)
	place := func(c Client, cat string, ops []IPOBidOption, f func(*IPOBidInput)) (IPOApp, error) {
		in := IPOBidInput{Issue: iss.ID, UCC: c.UCC, Category: cat, Options: ops, UPI: vpaOf(c.UCC), Instruction: staffSaid(o)}
		if f != nil {
			f(&in)
		}
		return o.PlaceIPOBid(ctx, T, "priya.nair", in)
	}
	// The UPI ID must be on record as the client's own.
	if _, err := place(cs[0], "RII", opt(2, "", true), nil); err == nil || !strings.Contains(err.Error(), "not on record") {
		t.Fatalf("unrecorded UPI: %v", err)
	}
	for _, c := range cs {
		regUPI(t, o, ctx, c.UCC)
	}
	if _, err := o.RegisterIPOUPI(ctx, T, "priya.nair", cs[1].UCC, vpaOf(cs[0].UCC), "x"); err == nil || !strings.Contains(err.Error(), "another client") {
		t.Fatalf("third-party UPI: %v", err)
	}
	// The client's prior instruction is required.
	if _, err := place(cs[0], "RII", opt(2, "", true), func(in *IPOBidInput) { in.Instruction = IPOInstructionIn{} }); err == nil || !strings.Contains(err.Error(), "how the client gave") {
		t.Fatalf("no instruction: %v", err)
	}
	if _, err := place(cs[0], "RII", opt(2, "", true), func(in *IPOBidInput) { in.Instruction.Received = o.now().Add(time.Hour).Format(time.RFC3339) }); err == nil {
		t.Fatal("instruction after the bid accepted")
	}
	b0, err := place(cs[0], "RII", opt(2, "", true), nil)
	if err != nil {
		t.Fatal(err)
	}
	if b0.Status != BidDraft || b0.Pending != "New" || b0.Amount != money.MustAmount("30000") || b0.Mode != ModeUPI || b0.Ack == "" || b0.DPID == "" || b0.Exchange != "NSE" {
		t.Fatalf("bid %+v", b0)
	}
	if ms, _ := o.Messages(ctx, T, cs[0].UCC, 5); len(ms) == 0 || ms[0].Event != "ipo.bid_ack" || !strings.Contains(ms[0].Body, b0.Ack) {
		t.Fatalf("acknowledgement %+v", ms)
	}
	if log, _ := o.IPOInstructions(ctx, T, "farhan.khan", "", b0.ID, "", ""); len(log) != 1 || log[0].Channel != IPOChannels[0] || log[0].Ack != b0.Ack || log[0].Action != "Place" {
		t.Fatalf("consent log %+v", log)
	}
	// One application per PAN.
	if _, err := place(cs[0], "RII", opt(1, "", true), nil); err == nil || !strings.Contains(err.Error(), "one application per PAN") {
		t.Fatalf("second bid on a PAN: %v", err)
	}
	// Category limits, cut-off and price.
	for _, c := range []struct {
		cat  string
		ops  []IPOBidOption
		want string
	}{
		{"RII", opt(14, "", true), "up to ₹2,00,000.00"},
		{"SNII", opt(14, "", true), "cut-off is only for retail"},
		{"SNII", opt(13, "100", false), "above ₹2,00,000.00"},
		{"SNII", opt(14, "101", false), "within the band"},
		{"SNII", opt(14, "97.5", false), "steps of ₹1"},
		{"EMP", opt(3, "", true), "declaration"},
		{"XYZ", opt(1, "", true), "choose a category"},
	} {
		if _, err := place(cs[1], c.cat, c.ops, nil); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s %+v: %v, want %q", c.cat, c.ops, err, c.want)
		}
	}
	b1, err := place(cs[1], "SNII", opt(14, "100", false), nil)
	if err != nil || b1.Amount != money.MustAmount("210000") || b1.Mode != ModeUPI {
		t.Fatalf("SNII %+v %v", b1, err)
	}
	b2, err := place(cs[2], "BNII", opt(70, "99", false), func(in *IPOBidInput) { in.UPI = "" })
	if err != nil || b2.Mode != ModeASBA || b2.UPI != "" {
		t.Fatalf("BNII above ₹5 lakh goes by ASBA: %+v %v", b2, err)
	}
	b3, err := place(cs[3], "EMP", opt(3, "", true), func(in *IPOBidInput) { in.Declared = true })
	if err != nil {
		t.Fatal(err)
	}
	b4, err := place(cs[4], "RII", opt(1, "96", false), func(in *IPOBidInput) { in.Exchange = "BSE" })
	if err != nil || b4.Exchange != "BSE" {
		t.Fatalf("BSE bid %+v %v", b4, err)
	}
	// A bid never sent can be cancelled outright, freeing the PAN.
	if c, err := o.CancelIPOBid(ctx, T, "priya.nair", b0.ID, staffSaid(o)); err != nil || c.Status != BidCancelled {
		t.Fatalf("cancel draft %+v %v", c, err)
	}
	b0, err = place(cs[0], "RII", opt(2, "", true), nil)
	if err != nil {
		t.Fatal(err)
	}

	// The day's bid file for NSE.
	o.Now = func() time.Time { return ipoAt(2026, 10, 28, 18, 0) }
	if _, err := o.IPOBidFile(ctx, T, "priya.nair", iss.ID, "NSE"); !errors.Is(err, ErrState) {
		t.Fatalf("file after hours: %v", err)
	}
	o.Now = func() time.Time { return ipoAt(2026, 10, 28, 11, 30) }
	if _, err := o.IPOBidFile(ctx, T, "vikram.shah", iss.ID, "NSE"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("compliance sent bids: %v", err)
	}
	batch, err := o.IPOBidFile(ctx, T, "priya.nair", iss.ID, "NSE")
	if err != nil {
		t.Fatal(err)
	}
	name, data, err := o.IPOBatchFile(ctx, T, "priya.nair", batch.ID)
	if err != nil || name != "NSE_EIPO_BID_90311_FICTIPO_28102026_001.csv" || len(batch.Bids) != 4 {
		t.Fatalf("file %s %+v %v", name, batch, err)
	}
	lib, _ := XFiles()
	spec, _ := lib.Recognise(name, data)
	tb, err := spec.Read(name, data)
	if err != nil || spec.ID != "nse-eipo-bid-upload" || len(tb.Rows) != 4 || len(tb.Issues) != 0 {
		t.Fatalf("read back %v %+v %v", spec, tb, err)
	}
	for _, r := range tb.Rows {
		if r.Get("referenceNumber") == b0.ID && (r.Get("atCutOff") != "Y" || r.Get("price") != "100.00" || r.Get("quantity") != "300" || r.Get("upi") != vpaOf(cs[0].UCC) || r.Get("activityType") != "N") {
			t.Errorf("row %+v", r.Values)
		}
		if r.Get("referenceNumber") == b2.ID && (r.Get("upiFlag") != "N" || r.Get("bankAccount") != cs[2].Bank.Account) {
			t.Errorf("ASBA row %+v", r.Values)
		}
	}
	if a := bidOf(t, o, ctx, b0.ID); a.Status != BidUploaded || a.Sent != "New" || a.Batch != batch.ID {
		t.Fatalf("after the file %+v", a)
	}
	if _, err := o.IPOBidFile(ctx, T, "priya.nair", iss.ID, "NSE"); err == nil {
		t.Fatal("empty second file")
	}
	if bb, err := o.IPOBidFile(ctx, T, "priya.nair", iss.ID, "BSE"); err != nil || len(bb.Bids) != 1 || !strings.HasPrefix(bb.Name, "BSE_IBBS_BID_6127_FICTIPO_") {
		t.Fatalf("BSE file %+v %v", bb, err)
	}

	// NSE's response file comes back through the exchange file loader.
	resp := xwrite(t, "nse-eipo-bid-response",
		xr("", "symbol", "FICTIPO", "applicationNumber", "2610280000001", "referenceNumber", b0.ID, "activityType", "N", "status", "Success", "upiStatus", "10"),
		xr("", "symbol", "FICTIPO", "applicationNumber", "2610280000002", "referenceNumber", b1.ID, "activityType", "N", "status", "Success", "upiStatus", "100", "dpVerStatus", "Y", "umn", "UMN0002"),
		xr("", "symbol", "FICTIPO", "applicationNumber", "2610280000003", "referenceNumber", b2.ID, "activityType", "N", "status", "Success"),
		xr("", "symbol", "FICTIPO", "referenceNumber", b3.ID, "activityType", "N", "status", "Error", "errorCode", "E104", "reason", "PAN and DP ID do not match"),
		xr("", "symbol", "FICTIPO", "referenceNumber", "SOMEONE-ELSE", "status", "Success"))
	res, err := o.LoadExchangeFile(ctx, T, "priya.nair", "NSE_EIPO_RESP_28102026_01.csv", resp, true)
	if err != nil || len(res) != 1 || res[0].Format != "nse-eipo-bid-response" || res[0].Added != 4 || res[0].Skipped != 1 {
		t.Fatalf("response %+v %v", res, err)
	}
	for id, want := range map[string]string{b0.ID: BidMandatePending, b1.ID: BidAccepted, b2.ID: BidAccepted, b3.ID: BidRejected} {
		if a := bidOf(t, o, ctx, id); a.Status != want || a.Sent != "" {
			t.Errorf("%s %s, want %s (%+v)", id, a.Status, want, a)
		}
	}
	if a := bidOf(t, o, ctx, b1.ID); a.AppNo != "2610280000002" || a.DPCheck != "Verified" || a.Mandate != "Accepted" || a.UMN != "UMN0002" {
		t.Fatalf("b1 %+v", a)
	}
	// Loading the same file again changes nothing.
	if res, _ := o.LoadExchangeFile(ctx, T, "priya.nair", "NSE_EIPO_RESP_28102026_01.csv", resp, true); res[0].Added != 0 {
		t.Fatalf("second load %+v", res)
	}

	// Non-retail bids only go up; retail bids may be withdrawn.
	if _, err := o.ModifyIPOBid(ctx, T, "priya.nair", b1.ID, IPOModifyInput{Options: opt(14, "99", false), Instruction: staffSaid(o)}); err == nil || !strings.Contains(err.Error(), "only be revised upward") {
		t.Fatalf("downward NII revision: %v", err)
	}
	if _, err := o.CancelIPOBid(ctx, T, "priya.nair", b1.ID, staffSaid(o)); err == nil || !strings.Contains(err.Error(), "cannot be withdrawn") {
		t.Fatalf("NII withdrawal: %v", err)
	}
	m1, err := o.ModifyIPOBid(ctx, T, "priya.nair", b1.ID, IPOModifyInput{Options: opt(15, "100", false), Instruction: staffSaid(o)})
	if err != nil || m1.Pending != "Modify" || m1.Amount != money.MustAmount("225000") {
		t.Fatalf("upward revision %+v %v", m1, err)
	}
	c0, err := o.CancelIPOBid(ctx, T, "priya.nair", b0.ID, staffSaid(o))
	if err != nil || c0.Pending != "Cancel" || c0.Status != BidMandatePending {
		t.Fatalf("retail cancel %+v %v", c0, err)
	}
	if _, err := o.ModifyIPOBid(ctx, T, "priya.nair", b0.ID, IPOModifyInput{Options: opt(1, "", true), Instruction: staffSaid(o)}); !errors.Is(err, ErrState) {
		t.Fatalf("modify while cancelling: %v", err)
	}
	b2nd, err := o.IPOBidFile(ctx, T, "priya.nair", iss.ID, "NSE")
	if err != nil || b2nd.Bids[b0.ID] != "Cancel" || b2nd.Bids[b1.ID] != "Modify" || !strings.HasSuffix(b2nd.Name, "_002.csv") {
		t.Fatalf("second file %+v %v", b2nd, err)
	}
	resp2 := xwrite(t, "nse-eipo-bid-response",
		xr("", "symbol", "FICTIPO", "applicationNumber", "2610280000001", "referenceNumber", b0.ID, "activityType", "C", "status", "Success"),
		xr("", "symbol", "FICTIPO", "applicationNumber", "2610280000002", "referenceNumber", b1.ID, "activityType", "M", "status", "Success", "upiStatus", "100"))
	if _, err := o.LoadExchangeFile(ctx, T, "priya.nair", "NSE_EIPO_RESP_28102026_02.csv", resp2, true); err != nil {
		t.Fatal(err)
	}
	if a := bidOf(t, o, ctx, b0.ID); a.Status != BidCancelled || a.CancelOn != "2026-10-28" {
		t.Fatalf("cancelled %+v", a)
	}
	if a := bidOf(t, o, ctx, b1.ID); a.Status != BidAccepted || a.Amount != money.MustAmount("225000") {
		t.Fatalf("revised %+v", a)
	}

	// SME: exactly two lots, no cut-off, no cancellation.
	sme, err := o.SaveIPOIssue(ctx, T, "priya.nair", IPOIssue{Symbol: "SMEFICT", Type: IssueSME, Exchanges: []string{"NSE"}, Open: "2026-10-28", Close: "2026-10-30", PriceLow: money.MustDec("110"), PriceHigh: money.MustDec("120"), LotSize: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.PlaceIPOBid(ctx, T, "priya.nair", IPOBidInput{Issue: sme.ID, UCC: cs[5].UCC, Category: "IND", Options: opt(1, "120", false), UPI: vpaOf(cs[5].UCC), Instruction: staffSaid(o)}); err == nil || !strings.Contains(err.Error(), "exactly 2 lots") {
		t.Fatalf("SME one lot: %v", err)
	}
	if _, err := o.PlaceIPOBid(ctx, T, "priya.nair", IPOBidInput{Issue: sme.ID, UCC: cs[5].UCC, Category: "IND", Options: opt(2, "", true), UPI: vpaOf(cs[5].UCC), Instruction: staffSaid(o)}); err == nil || !strings.Contains(err.Error(), "no cut-off") {
		t.Fatalf("SME cut-off: %v", err)
	}
	sb, err := o.PlaceIPOBid(ctx, T, "priya.nair", IPOBidInput{Issue: sme.ID, UCC: cs[5].UCC, Category: "IND", Options: opt(2, "120", false), UPI: vpaOf(cs[5].UCC), Instruction: staffSaid(o)})
	if err != nil || sb.Amount != money.MustAmount("240000") || sb.Mode != ModeUPI {
		t.Fatalf("SME bid %+v %v", sb, err)
	}
	if _, err := o.IPOBidFile(ctx, T, "priya.nair", sme.ID, "NSE"); err != nil {
		t.Fatal(err)
	}
	if _, err := o.CancelIPOBid(ctx, T, "priya.nair", sb.ID, staffSaid(o)); err == nil || !strings.Contains(err.Error(), "SME bids cannot be cancelled") {
		t.Fatalf("SME cancel: %v", err)
	}
	if _, err := o.ModifyIPOBid(ctx, T, "priya.nair", sb.ID, IPOModifyInput{Options: opt(2, "115", false), Instruction: staffSaid(o)}); err == nil || !strings.Contains(err.Error(), "downward") {
		t.Fatalf("SME downward: %v", err)
	}

	// Branch staff only bid for their own clients.
	var other Client
	for _, c := range cs {
		if c.Branch != "Bengaluru" {
			other = c
		}
	}
	if other.UCC != "" {
		if _, err := o.PlaceIPOBid(ctx, T, "meera.iyer", IPOBidInput{Issue: iss.ID, UCC: other.UCC, Category: "RII", Options: opt(1, "", true), UPI: vpaOf(other.UCC), Instruction: staffSaid(o)}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("branch bid outside the branch: %v", err)
		}
	}

	// Closing day: bidding closes at 4 pm, mandates lapse at 5 pm.
	o.Now = func() time.Time { return ipoAt(2026, 10, 29, 16, 5) }
	if _, err := o.PlaceIPOBid(ctx, T, "priya.nair", IPOBidInput{Issue: iss.ID, UCC: cs[5].UCC, Category: "RII", Options: opt(1, "", true), UPI: vpaOf(cs[5].UCC), Instruction: staffSaid(o)}); !errors.Is(err, ErrState) {
		t.Fatalf("bid after the close: %v", err)
	}
	o.ipoTick(ctx, T, o.now())
	o.ipoTick(ctx, T, o.now().Add(time.Minute))
	ws, _ := docs.List[WorkItem](ctx, o.Docs, T, cWorkItem, "")
	found := 0
	for _, w := range ws {
		if w.Key == "ipo:mandates-"+iss.ID && w.Status == "Open" {
			found++
			if !strings.Contains(w.Detail, b4.ID) {
				t.Errorf("work item misses the BSE bid: %s", w.Detail)
			}
		}
	}
	if found != 1 {
		t.Fatalf("4 pm work item: %d", found)
	}
	if ms, _ := o.Messages(ctx, T, cs[4].UCC, 5); len(ms) == 0 || ms[0].Event != "ipo.mandate_pending" {
		t.Fatalf("4 pm reminder %+v", ms)
	}
	mv, _ := o.IPOMandates(ctx, T, "priya.nair")
	var mt IPOMandateIssue
	for _, m := range mv {
		if m.Issue.ID == iss.ID {
			mt = m
		}
	}
	if len(mt.Pending) == 0 || mt.Alerts["close:2026-10-29"] == "" || mt.Cutoff != "2026-10-29 17:00" {
		t.Fatalf("mandate tracker %+v", mt)
	}
	o.Now = func() time.Time { return ipoAt(2026, 10, 29, 17, 1) }
	o.ipoTick(ctx, T, o.now())
	if a := bidOf(t, o, ctx, b4.ID); a.Status != BidFailed || a.Mandate != "Failed" || !strings.Contains(a.MandateWhy, "cut-off") {
		t.Fatalf("lapsed %+v", a)
	}
	if _, err := o.ResendIPOMandate(ctx, T, "priya.nair", b4.ID, staffSaid(o)); !errors.Is(err, ErrState) {
		t.Fatalf("re-send after the last day's cut-off: %v", err)
	}

	// Allotment on T+1.
	o.Now = func() time.Time { return ipoAt(2026, 10, 30, 19, 0) }
	if _, err := o.RecordIPOAllotment(ctx, T, "priya.nair", iss.ID, "2026-10-30", map[string]int64{b1.ID: 2}); err == nil || !strings.Contains(err.Error(), "final issue price") {
		t.Fatalf("allotment without the price: %v", err)
	}
	iss.IssuePrice, iss.ISIN = money.MustDec("100"), "INE999Z01011"
	if _, err := o.SaveIPOIssue(ctx, T, "priya.nair", iss); err != nil {
		t.Fatal(err)
	}
	if _, err := o.RecordIPOAllotment(ctx, T, "priya.nair", iss.ID, "2026-10-30", map[string]int64{b4.ID: 1}); err == nil || !strings.Contains(err.Error(), "no money was blocked") {
		t.Fatalf("allotment to a lapsed bid: %v", err)
	}
	ar, err := o.RecordIPOAllotment(ctx, T, "priya.nair", iss.ID, "2026-10-30", map[string]int64{b1.ID: 2})
	if err != nil || ar.Allotted != 1 || ar.NotAllotted != 1 {
		t.Fatalf("allotment %+v %v", ar, err)
	}
	if a := bidOf(t, o, ctx, b1.ID); a.Status != BidAllotted || a.Shares != 300 {
		t.Fatalf("allotted %+v", a)
	}
	// The sponsor bank reports b2's money released on T+2.
	o.Now = func() time.Time { return ipoAt(2026, 11, 2, 15, 0) }
	resp3 := xwrite(t, "nse-eipo-bid-response", xr("", "symbol", "FICTIPO", "referenceNumber", b2.ID, "upiStatus", "110", "timestamp", "2026-11-02T14:00:00+05:30"))
	if _, err := o.LoadExchangeFile(ctx, T, "priya.nair", "NSE_EIPO_INQ_02112026.csv", resp3, true); err != nil {
		t.Fatal(err)
	}
	if a := bidOf(t, o, ctx, b2.ID); a.Status != BidUnblocked || a.UnblockedOn != "2026-11-02" {
		t.Fatalf("released %+v", a)
	}
	// Three days later b1's unused money is still blocked and its shares
	// have not reached the demat.
	o.Now = func() time.Time { return ipoAt(2026, 11, 5, 19, 0) }
	rec, err := o.IPOReconciliation(ctx, T, "priya.nair", iss.ID)
	if err != nil {
		t.Fatal(err)
	}
	row := func(id string) IPOReconRow {
		for _, r := range rec.Rows {
			if r.Bid == id {
				return r
			}
		}
		t.Fatalf("no row %s", id)
		return IPOReconRow{}
	}
	r1 := row(b1.ID)
	if r1.ToRelease != money.MustAmount("195000") || r1.Unblock != "Late" || r1.LateDays != 3 || r1.Compensation != money.MustAmount("300") || r1.Credit == "Credited" {
		t.Fatalf("b1 recon %+v", r1)
	}
	if r2 := row(b2.ID); r2.Unblock != "Done" || r2.Compensation != 0 {
		t.Fatalf("b2 recon %+v", r2)
	}
	if got := IPOCompensation(money.MustAmount("500000"), 10); got != money.MustAmount("2054.79") {
		t.Fatalf("15%% a year wins: %s", got)
	}
	// The shares arrive in the demat.
	if err := docs.Upsert(ctx, o.Docs, T, cHolding, holdingID(cs[1].UCC, "INE999Z01011"), Holding{UCC: cs[1].UCC, ISIN: "INE999Z01011", Symbol: "FICTIPO", Qty: 300}, "test"); err != nil {
		t.Fatal(err)
	}
	if n, err := o.SyncIPOCredits(ctx, T, "priya.nair", iss.ID); err != nil || n != 1 {
		t.Fatalf("credits %d %v", n, err)
	}
	if a := bidOf(t, o, ctx, b1.ID); a.Status != BidCredited || a.CreditedQty != 300 {
		t.Fatalf("credited %+v", a)
	}
	// The firm pays the compensation itself: it reaches the client's ledger.
	before, _ := o.clientFunds(ctx, T, "2026-11-05", "")
	if _, err := o.RecordIPOCompensation(ctx, T, "priya.nair", b1.ID, IPOComp{Status: "Paid", Party: "Firm", Paid: money.MustAmount("300")}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker paid compensation: %v", err)
	}
	if _, err := o.RecordIPOCompensation(ctx, T, "farhan.khan", b1.ID, IPOComp{Status: "Paid", Party: "Firm", Paid: money.MustAmount("300")}); err != nil {
		t.Fatal(err)
	}
	after, _ := o.clientFunds(ctx, T, "2026-11-05", "")
	if after[cs[1].UCC]-before[cs[1].UCC] != money.MustAmount("300") {
		t.Fatalf("compensation in the ledger: %s", after[cs[1].UCC]-before[cs[1].UCC])
	}
	// Allotments posted as purchases, then the commission.
	if pr, err := o.PostIssueAllotments(ctx, T, "priya.nair", iss.ID, ""); err != nil || pr.Posted != 1 || pr.Shares != 300 {
		t.Fatalf("post %+v %v", pr, err)
	}
	ci, err := o.ClaimIPOCommission(ctx, T, "farhan.khan", iss.ID, "2026-11-05", "INV-IPO-1")
	if err != nil || ci.Commission.Apps != 1 || ci.Commission.Amount != money.MustAmount("10") || ci.Commission.Total != money.MustAmount("11.80") {
		t.Fatalf("claim %+v %v", ci.Commission, err)
	}
	if _, err := o.ClaimIPOCommission(ctx, T, "farhan.khan", iss.ID, "2026-11-05", ""); !errors.Is(err, ErrState) {
		t.Fatalf("second claim: %v", err)
	}
	if _, err := o.ReceiveIPOCommission(ctx, T, "farhan.khan", iss.ID, "2026-11-20", money.MustAmount("10.80"), money.MustAmount("1"), "NEFT-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := o.ReceiveIPOCommission(ctx, T, "farhan.khan", iss.ID, "2026-11-21", money.MustAmount("1"), 0, "x"); err == nil {
		t.Fatal("received more than due")
	}
	if tb, _ := o.App.Ledger.TrialBalance(ctx, T, "2026-12-31"); !tb.Balanced {
		t.Fatal("trial balance off")
	}
	// A complaint about a bid goes to the help desk and is linked.
	tk, err := o.IPOComplaint(ctx, T, "priya.nair", b1.ID, "", "", "Money not released on time")
	if err != nil || tk.Category != "IPO / mutual funds" || tk.Kind != KindComplaint {
		t.Fatalf("complaint %+v %v", tk, err)
	}
	if a := bidOf(t, o, ctx, b1.ID); len(a.Tickets) != 1 {
		t.Fatalf("ticket not linked %+v", a.Tickets)
	}
	// The consent log as a file.
	if csv, err := o.IPOInstructionsCSV(ctx, T, "vikram.shah", "", ""); err != nil || strings.Count(string(csv), "\n") < 10 {
		t.Fatalf("consent log file %v", err)
	}
	// Applications from the platform's IPO-BIDS file are still listed, and
	// one that is a Ledgerline bid is shown once.
	_ = docs.Upsert(ctx, o.Docs, T, cIPO, "2610280000002", IPOApp{ID: "2610280000002", UCC: cs[1].UCC, Issue: iss.Name, Lots: 15, Mandate: "Accepted", Allotment: "Awaiting"}, "test")
	_ = docs.Upsert(ctx, o.Docs, T, cIPO, "LEGACY-1", IPOApp{ID: "LEGACY-1", UCC: cs[5].UCC, Issue: "Other (made up)", Lots: 1, Mandate: "Declined", Allotment: "Awaiting"}, "test")
	apps, _ := o.IPOApps(ctx, T)
	seen := map[string]IPOApp{}
	for _, a := range apps {
		seen[a.ID] = a
	}
	if _, dup := seen["2610280000002"]; dup || seen["LEGACY-1"].Status != BidDeclined || seen["LEGACY-1"].Source != "Exchange file" {
		t.Fatalf("legacy rows %+v", seen["LEGACY-1"])
	}
}

func TestIPOPortal(t *testing.T) {
	o, ctx := setup(t)
	o.Now = func() time.Time { return ipoAt(2026, 10, 28, 11, 0) }
	iss := mainboard(t, o, ctx, "PORTALCO")
	cs := ipoClients(t, o, ctx, 2)
	user, _, err := o.PortalAccess(ctx, T, "farhan.khan", cs[0].UCC, []string{cs[1].UCC}, true)
	if err != nil {
		t.Fatal(err)
	}
	v, err := o.PortalIPO(ctx, T, user, "")
	if err != nil || !v.Own || len(v.Issues) != 1 || !v.Issues[0].CanApply {
		t.Fatalf("portal %+v %v", v, err)
	}
	var rii PortalIPOCategory
	for _, c := range v.Issues[0].Choices {
		if c.Code == "RII" {
			rii = c
		}
	}
	if rii.MinLots != 1 || rii.MaxLots != 13 || !rii.CutOff {
		t.Fatalf("RII lots %+v", rii)
	}
	in := IPOBidInput{Issue: iss.ID, Category: "RII", Options: opt(13, "", true), UPI: "Me.Self@okhdfcbank"}
	if _, err := o.ClientPlaceIPOBid(ctx, T, user, in); err == nil || !strings.Contains(err.Error(), "own bank account") {
		t.Fatalf("undeclared UPI: %v", err)
	}
	in.OwnUPI = true
	b, err := o.ClientPlaceIPOBid(ctx, T, user, in)
	if err != nil || b.UCC != cs[0].UCC || b.Source != channelPortal || b.UPI != "me.self@okhdfcbank" || b.Amount != money.MustAmount("195000") {
		t.Fatalf("portal bid %+v %v", b, err)
	}
	if ids, _ := o.ClientUPIs(ctx, T, cs[0].UCC); len(ids) == 0 || ids[0].Source != "Declared by the client" {
		t.Fatalf("declared UPI %+v", ids)
	}
	if log, _ := o.IPOInstructions(ctx, T, "priya.nair", cs[0].UCC, "", "", ""); len(log) != 1 || log[0].Channel != channelPortal || log[0].By != user {
		t.Fatalf("portal consent %+v", log)
	}
	// A family account is visible but only its holder applies.
	in.UCC = cs[1].UCC
	if _, err := o.ClientPlaceIPOBid(ctx, T, user, in); err == nil || !strings.Contains(err.Error(), "only the account holder") {
		t.Fatalf("family bid: %v", err)
	}
	if fv, _ := o.PortalIPO(ctx, T, user, cs[1].UCC); fv.Own || fv.Issues[0].CanApply {
		t.Fatalf("family view %+v", fv)
	}
	// Someone else's bid is not the client's to touch.
	if _, err := o.ClientCancelIPOBid(ctx, T, "priya.nair", b.ID); err == nil {
		t.Fatal("staff used the client's cancel")
	}
	m, err := o.ClientModifyIPOBid(ctx, T, user, b.ID, IPOModifyInput{Options: opt(10, "", true)})
	if err != nil || m.Lots != 10 {
		t.Fatalf("portal modify %+v %v", m, err)
	}
	c, err := o.ClientCancelIPOBid(ctx, T, user, b.ID)
	if err != nil || c.Status != BidCancelled {
		t.Fatalf("portal cancel %+v %v", c, err)
	}
	v, _ = o.PortalIPO(ctx, T, user, "")
	ours := 0
	for _, x := range v.Bids {
		if x.IssueID == iss.ID {
			ours++
		}
	}
	if ours != 1 || !v.Issues[0].CanApply {
		t.Fatalf("after cancel %d bids, %+v", ours, v.Issues[0])
	}
}

// fakeEIPO stands in for NSE's eIPO API.
type fakeEIPO struct {
	sent []EIPOBid
	cfg  EIPOConfig
}

func (f *fakeEIPO) SendBids(_ context.Context, c EIPOConfig, bids []EIPOBid) ([]IPOStatusUpdate, error) {
	f.cfg = c
	f.sent = append(f.sent, bids...)
	var out []IPOStatusUpdate
	for i, b := range bids {
		out = append(out, IPOStatusUpdate{Ref: b.Ref, AppNo: fmt.Sprintf("88%014d", i+1), Status: "success", PayStatus: "10", Source: "fake eIPO"})
	}
	return out, nil
}

func (f *fakeEIPO) Inquire(_ context.Context, _ EIPOConfig, _ string, refs []string) ([]IPOStatusUpdate, error) {
	var out []IPOStatusUpdate
	for _, r := range refs {
		out = append(out, IPOStatusUpdate{Ref: r, PayStatus: "31", Source: "fake inquiry"})
	}
	return out, nil
}

func TestIPOEIPOAPIAndCallback(t *testing.T) {
	o, ctx := setup(t)
	o.Now = func() time.Time { return ipoAt(2026, 10, 28, 11, 0) }
	fake := &fakeEIPO{}
	old := eipoClient
	eipoClient = fake
	t.Cleanup(func() { eipoClient = old })
	iss := mainboard(t, o, ctx, "APICO")
	cs := ipoClients(t, o, ctx, 2)
	var bids []IPOApp
	for _, c := range cs {
		regUPI(t, o, ctx, c.UCC)
		b, err := o.PlaceIPOBid(ctx, T, "priya.nair", IPOBidInput{Issue: iss.ID, UCC: c.UCC, Category: "RII", Options: opt(1, "", true), UPI: vpaOf(c.UCC), Instruction: staffSaid(o)})
		if err != nil {
			t.Fatal(err)
		}
		bids = append(bids, b)
	}
	if _, err := o.EIPOSend(ctx, T, "priya.nair", iss.ID); !errors.Is(err, ErrState) {
		t.Fatalf("send without a connection: %v", err)
	}
	vals := map[string]string{"member": "90311", "login": "LLAPI01", "env": "Mock (UAT)", "send": "On"}
	secrets := map[string]string{"password": "made-up-password", "callback_token": "cb-token-made-up-0123456789"}
	if _, err := o.SaveConnection(ctx, T, "admin", connNSEEIPO, vals, secrets); err != nil {
		t.Fatal(err)
	}
	// Without NSE's document (endpoints) nothing is sent.
	if _, err := o.EIPOSend(ctx, T, "priya.nair", iss.ID); err == nil || !strings.Contains(err.Error(), "API document is needed") {
		t.Fatalf("send without the spec: %v", err)
	}
	if v := o.EIPOStatus(ctx, T); !v.Set || v.Usable || v.Needs == "" || !v.Token {
		t.Fatalf("status %+v", v)
	}
	vals["url"], vals["bid_path"], vals["inquiry_path"] = "https://eipo.example.test/api", "/bids", "/bids/inquiry"
	if _, err := o.SaveConnection(ctx, T, "admin", connNSEEIPO, vals, nil); err != nil {
		t.Fatal(err)
	}
	b, err := o.EIPOSend(ctx, T, "priya.nair", iss.ID)
	if err != nil || b.Via != "API" || len(fake.sent) != 2 || fake.cfg.Member != "90311" || fake.cfg.Live {
		t.Fatalf("API send %+v %v (%d sent)", b, err, len(fake.sent))
	}
	if s := fake.sent[0]; s.UPIFlag != "Y" || s.Ref != bids[0].ID || s.Bids[0].ActivityType != "new" || !s.Bids[0].AtCutOff || s.Bids[0].Quantity != 150 || s.Category != "RII" {
		t.Fatalf("API bid %+v", s)
	}
	if a := bidOf(t, o, ctx, bids[0].ID); a.Status != BidMandatePending || a.AppNo != "8800000000000001" || a.Sent != "" {
		t.Fatalf("after send %+v", a)
	}
	// The callback: wrong token, address not listed, then the real one.
	body := []byte(`{"status":"success","data":[{"symbol":"APICO","referenceNumber":"` + bids[0].ID + `","applicationNumber":8800000000000001,"upiPaymentStatus":"100","umn":"UMN-1","dpVerStatusFlag":"Y"},
		{"symbol":"APICO","referenceNumber":"NOT-OURS","upiPaymentStatus":"100"}]}`)
	if _, err := o.EIPOCallback(ctx, T, "wrong", "203.0.113.5:443", body); !errors.Is(err, ErrForbidden) {
		t.Fatalf("wrong token: %v", err)
	}
	vals["callback_ips"] = "203.0.113.9"
	if _, err := o.SaveConnection(ctx, T, "admin", connNSEEIPO, vals, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := o.EIPOCallback(ctx, T, "cb-token-made-up-0123456789", "203.0.113.5:443", body); !errors.Is(err, ErrForbidden) {
		t.Fatalf("unlisted address: %v", err)
	}
	res, err := o.EIPOCallback(ctx, T, "cb-token-made-up-0123456789", "203.0.113.9:443", body)
	if err != nil || res.Applied != 1 || res.Unknown != 1 {
		t.Fatalf("callback %+v %v", res, err)
	}
	if a := bidOf(t, o, ctx, bids[0].ID); a.Status != BidAccepted || a.UMN != "UMN-1" || a.DPCheck != "Verified" || a.AppNo != "8800000000000001" {
		t.Fatalf("after callback %+v", a)
	}
	// Inquiry: the other client declined.
	if n, err := o.EIPOInquire(ctx, T, "priya.nair", iss.ID); err != nil || n != 1 {
		t.Fatalf("inquiry %d %v", n, err)
	}
	if a := bidOf(t, o, ctx, bids[1].ID); a.Status != BidDeclined || a.Mandate != "Declined" {
		t.Fatalf("declined %+v", a)
	}
	// A declined mandate can be sent again once, after the day's close.
	if _, err := o.ResendIPOMandate(ctx, T, "priya.nair", bids[1].ID, staffSaid(o)); !errors.Is(err, ErrState) {
		t.Fatalf("re-send during bidding: %v", err)
	}
	o.Now = func() time.Time { return ipoAt(2026, 10, 28, 17, 30) }
	if a, err := o.ResendIPOMandate(ctx, T, "priya.nair", bids[1].ID, staffSaid(o)); err != nil || a.Status != BidMandatePending || a.Reinit == "" {
		t.Fatalf("re-send %+v %v", a, err)
	}
	if _, err := o.ResendIPOMandate(ctx, T, "priya.nair", bids[1].ID, staffSaid(o)); !errors.Is(err, ErrState) {
		t.Fatalf("second re-send: %v", err)
	}
	// The tick sends new bids while bidding is open.
	o.Now = func() time.Time { return ipoAt(2026, 10, 29, 10, 30) }
	c3 := ipoClients(t, o, ctx, 3)[2]
	regUPI(t, o, ctx, c3.UCC)
	if _, err := o.PlaceIPOBid(ctx, T, "priya.nair", IPOBidInput{Issue: iss.ID, UCC: c3.UCC, Category: "RII", Options: opt(1, "", true), UPI: vpaOf(c3.UCC), Instruction: staffSaid(o)}); err != nil {
		t.Fatal(err)
	}
	o.ipoTick(ctx, T, o.now())
	if len(fake.sent) != 3 {
		t.Fatalf("tick sent %d", len(fake.sent))
	}
}

func TestIPOEIPOHTTPClient(t *testing.T) {
	srv := newEIPOFake(t)
	c := nseEIPOHTTP{client: srv.Client()}
	cfg := EIPOConfig{Base: srv.URL + "/eipo", Member: "90311", Login: "LLAPI01", Password: "pw-made-up", BidPath: "/bids"}
	ups, err := c.SendBids(context.Background(), cfg, []EIPOBid{{Symbol: "APICO", Ref: "IPB-0000001", PAN: "AAAAA0000A", UPIFlag: "Y", UPI: "x@okicici", Bids: []EIPOBidLine{{ActivityType: "new", Quantity: 150, AtCutOff: true, Price: "100.00", Amount: "15000.00"}}}})
	if err != nil || len(ups) != 1 || ups[0].Ref != "IPB-0000001" || ups[0].AppNo != "12345678901234567890" || ups[0].Status != "success" {
		t.Fatalf("HTTP send %+v %v", ups, err)
	}
	cfg.Password = "wrong"
	if _, err := c.SendBids(context.Background(), cfg, []EIPOBid{{Ref: "IPB-0000002"}}); err == nil || !strings.Contains(err.Error(), "refused the login") {
		t.Fatalf("bad login: %v", err)
	}
	if _, err := c.SendBids(context.Background(), EIPOConfig{Base: srv.URL}, nil); err != nil {
		t.Fatalf("nothing to send: %v", err)
	}
	if _, err := c.Inquire(context.Background(), EIPOConfig{Base: srv.URL}, "APICO", nil); err == nil {
		t.Fatal("inquiry without the endpoint")
	}
}

func TestIPOOfferForSale(t *testing.T) {
	o, ctx := setup(t)
	o.Now = func() time.Time { return ipoAt(2026, 10, 16, 10, 0) }
	iss, err := o.SaveIPOIssue(ctx, T, "priya.nair", IPOIssue{Symbol: "OFSCO", Type: IssueOFS, Exchanges: []string{"NSE"}, Open: "2026-10-15", Close: "2026-10-16",
		PriceLow: money.MustDec("250"), PriceHigh: money.MustDec("260"), LotSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	cs, _ := o.Clients(ctx, T)
	var rich, poor Client
	for _, c := range cs {
		if c.Status != "Active" || c.Type != "Individual" {
			continue
		}
		if _, _, _, err := splitDemat(c.Demat); err != nil {
			continue
		}
		w, err := o.Withdrawable(ctx, T, D, c.UCC)
		if err != nil {
			continue
		}
		if w.Available >= money.MustAmount("50000") && rich.UCC == "" {
			rich = c
		}
		if w.Available < money.MustAmount("1000") && !w.Blocked && poor.UCC == "" {
			poor = c
		}
	}
	if rich.UCC == "" || poor.UCC == "" {
		t.Fatalf("no clients with and without funds")
	}
	in := IPOBidInput{Issue: iss.ID, UCC: poor.UCC, Category: "RETAIL", Options: opt(100, "", true), Instruction: staffSaid(o)}
	if _, err := o.PlaceIPOBid(ctx, T, "priya.nair", in); err == nil || !strings.Contains(err.Error(), "100% cash margin") {
		t.Fatalf("no funds: %v", err)
	}
	in.UCC, in.Category = rich.UCC, "NONRETAIL"
	in.Options = opt(1000, "255", false)
	if _, err := o.PlaceIPOBid(ctx, T, "priya.nair", in); !errors.Is(err, ErrState) {
		t.Fatalf("non-retail bid on T+1: %v", err)
	}
	before, _ := o.clientFunds(ctx, T, D, "")
	in.Category, in.Options = "RETAIL", opt(100, "", true)
	b, err := o.PlaceIPOBid(ctx, T, "priya.nair", in)
	if err != nil || b.Mode != ModeMargin || b.Margin != money.MustAmount("26000") || b.MarginEntry == "" {
		t.Fatalf("OFS bid %+v %v", b, err)
	}
	after, _ := o.clientFunds(ctx, T, D, "")
	if before[rich.UCC]-after[rich.UCC] != money.MustAmount("26000") {
		t.Fatalf("margin not taken from the ledger: %s", before[rich.UCC]-after[rich.UCC])
	}
	// Revised up: the margin moves with it.
	b, err = o.ModifyIPOBid(ctx, T, "priya.nair", b.ID, IPOModifyInput{Options: opt(150, "", true), Instruction: staffSaid(o)})
	if err != nil || b.Margin != money.MustAmount("39000") {
		t.Fatalf("revised %+v %v", b, err)
	}
	if now, _ := o.clientFunds(ctx, T, D, ""); before[rich.UCC]-now[rich.UCC] != money.MustAmount("39000") {
		t.Fatalf("revised margin %s", before[rich.UCC]-now[rich.UCC])
	}
	if _, err := o.IPOBidFile(ctx, T, "priya.nair", iss.ID, "NSE"); err == nil || !strings.Contains(err.Error(), "OFS window") {
		t.Fatalf("OFS bid file: %v", err)
	}
	// The dealer places it in the OFS window.
	if a, err := o.RecordIPOStatus(ctx, T, "priya.nair", b.ID, IPOStatusUpdate{Status: "Accepted", AppNo: "OFSORD0001"}); err != nil || a.Status != BidAccepted || a.Pending != "" {
		t.Fatalf("placed %+v %v", a, err)
	}
	// Allocation at ₹258 for 120 shares: the margin comes back, the
	// allotment joins the day's trades and is billed with them.
	res, err := o.RecordOFSAllocation(ctx, T, "priya.nair", iss.ID, D, []OFSAllocation{{Bid: b.ID, Qty: 120, Price: money.MustDec("258")}}, true)
	if err != nil || res.Allotted != 1 || res.Added != 1 || res.Released != 1 {
		t.Fatalf("allocation %+v %v", res, err)
	}
	if now, _ := o.clientFunds(ctx, T, D, ""); now[rich.UCC] != before[rich.UCC] {
		t.Fatalf("margin not released: %s", before[rich.UCC]-now[rich.UCC])
	}
	trades, _ := o.exchangeTrades(ctx, T, D)
	found := false
	for _, tr := range trades {
		if tr.TradeNo == "OFS-"+b.ID && tr.UCC == rich.UCC && tr.Qty == 120 && tr.Price.String() == "258" && tr.Side == "B" {
			found = true
		}
	}
	if !found {
		t.Fatal("allotment not in the day's trades")
	}
	m, _, _ := o.Market(ctx, T, D)
	br, err := o.App.RunBilling(ctx, T, "priya.nair", D, m.SettlementPrices())
	if err != nil {
		t.Fatal(err)
	}
	billed := false
	for _, g := range br.Groups {
		if g.UCC == rich.UCC && g.Segment == "CM" && g.TradeNet <= -money.MustAmount("30960") {
			billed = true
		}
	}
	if !billed {
		t.Fatal("OFS obligation not billed")
	}
	if a := bidOf(t, o, ctx, b.ID); a.Status != BidAllotted || a.Shares != 120 || !strings.HasPrefix(a.Obligation, "Added") || a.MarginEntry != "" {
		t.Fatalf("allotted %+v", a)
	}
	if tb, _ := o.App.Ledger.TrialBalance(ctx, T, D); !tb.Balanced {
		t.Fatal("trial balance off")
	}
}

// newEIPOFake is a stand-in eIPO server: Basic sign-in as NSE's published
// query API uses, and a reply naming the application by number.
func newEIPOFake(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte("90311^LLAPI01:pw-made-up")) {
			w.WriteHeader(401)
			return
		}
		var b EIPOBid
		if r.URL.Path != "/eipo/bids" || json.NewDecoder(r.Body).Decode(&b) != nil || b.Ref == "" {
			w.WriteHeader(400)
			w.Write([]byte(`{"status":"failed","reason":"bad request"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"success","applicationNumber":12345678901234567890,"symbol":"APICO"}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}
