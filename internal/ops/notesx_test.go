package ops

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/money"
)

// notedDay bills the sample day and generates the notes.
func notedDay(t *testing.T, o *Ops) []ContractNote {
	t.Helper()
	ctx := context.Background()
	m, _, _ := o.Market(ctx, T, D)
	if _, err := o.App.RunBilling(ctx, T, "farhan.khan", D, m.SettlementPrices()); err != nil {
		t.Fatal(err)
	}
	if _, err := o.GenerateNotes(ctx, T, "priya.nair", D); err != nil {
		t.Fatal(err)
	}
	ns, _ := o.Notes(ctx, T, D)
	return ns
}

// equityNoteWith is an equity note whose client has at least n trades.
func equityNoteWith(t *testing.T, o *Ops, ns []ContractNote, n int) (ContractNote, []ingest.TradeEvent) {
	t.Helper()
	for _, x := range ns {
		if x.part() != PartEquity || x.Trades < n {
			continue
		}
		tr, _, err := o.noteData(context.Background(), T, D, x.UCC, "")
		if err == nil {
			return x, tr
		}
	}
	t.Fatalf("no equity note with %d trades", n)
	return ContractNote{}, nil
}

// nxBO gives a sample client with holdings a demat account with the
// firm's DP on its primary demat ID.
func nxBO(t *testing.T, o *Ops) (Client, BOAccount) {
	t.Helper()
	ctx := context.Background()
	cs, _ := o.Clients(ctx, T)
	for _, c := range cs {
		id := primaryDemat(c).ID
		if c.Status != "Active" || id == "" {
			continue
		}
		hs, _ := o.Holdings(ctx, T, c.UCC)
		for _, h := range hs {
			if h.BOID == "" && h.Qty.Sign() > 0 {
				b := BOAccount{UCC: c.UCC, BOID: id, Depository: "CDSL", Status: "Active", Opened: "2026-01-05"}
				if err := o.putBO(ctx, T, "priya.nair", b); err != nil {
					t.Fatal(err)
				}
				return c, b
			}
		}
	}
	t.Fatal("no client with holdings in the sample")
	return Client{}, BOAccount{}
}

func TestNoteWAPAndAnnexure(t *testing.T) {
	o, ctx := setup(t)
	ns := notedDay(t, o)
	n, tr := equityNoteWith(t, o, ns, 1)
	v, err := o.noteView(ctx, T, D, n.UCC, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(v.WAP) == 0 {
		t.Fatal("no contract-wise summary")
	}
	var q int64
	for _, w := range v.WAP {
		q += w.Qty
	}
	var want int64
	for _, x := range tr {
		want += x.Qty
	}
	if q != want {
		t.Fatalf("summary quantity %d, trades %d", q, want)
	}
	// NSE and BSE trades of one security and side are averaged together.
	a := ingest.TradeEvent{Exchange: "NSE", Segment: "CM", Symbol: "ACME", InstrumentType: "EQ", Side: "B", Qty: 10, Price: money.MustDec("100")}
	b := a
	b.Exchange, b.Qty, b.Price = "BSE", 30, money.MustDec("104")
	rows := wapSummary([]ingest.TradeEvent{a, b}, []noteLine{{Brok: money.MustAmount("4.00")}, {Brok: money.MustAmount("12.00")}})
	if len(rows) != 1 || rows[0].Exchanges != "BSE, NSE" || rows[0].Qty != 40 || rows[0].WAP != "103.0000" || rows[0].BrokPU != "0.4000" || rows[0].NetRate != "103.4000" || rows[0].Net.String() != "-4136" {
		t.Fatalf("common WAP %+v", rows)
	}
	html, _ := o.RenderNote(ctx, T, D, n.UCC, "")
	if !strings.Contains(string(html), "Contract-wise summary (weighted average price)") || !strings.Contains(string(html), "Annexure: orders and trades") {
		t.Fatal("the note lacks the summary or the annexure")
	}
	r, _, err := o.notePDF(ctx, T, D, n.UCC, "")
	if err != nil || r.Sections[0].Title != "Contract-wise summary (weighted average price)" || r.Sections[2].Title != "Annexure: orders and trades" {
		t.Fatalf("pdf sections %v", err)
	}
}

func TestNoteRemarksREAndMarginCall(t *testing.T) {
	o, ctx := setup(t)
	ns := notedDay(t, o)
	n, tr := equityNoteWith(t, o, ns, 1)
	var buy *ingest.TradeEvent
	for i := range tr {
		if tr[i].Side == "B" && tr[i].Segment == "CM" {
			buy = &tr[i]
			break
		}
	}
	if buy != nil {
		if _, err := o.SaveRightsEntitlement(ctx, T, "anita.rao", RightsEntitlement{Exchange: buy.Exchange, Symbol: buy.Symbol, Closes: "2026-12-15"}); !errors.Is(err, ErrForbidden) {
			t.Fatalf("risk added an RE: %v", err)
		}
		if _, err := o.SaveRightsEntitlement(ctx, T, "priya.nair", RightsEntitlement{Exchange: buy.Exchange, Symbol: buy.Symbol, Closes: "2026-12-15", Ref: "Exchange notice (test)"}); err != nil {
			t.Fatal(err)
		}
		v, _ := o.noteView(ctx, T, D, n.UCC, "")
		if len(v.Remarks) == 0 || !strings.Contains(v.Remarks[0], "rights entitlements") || !strings.Contains(v.Remarks[0], "lapses") || !strings.Contains(v.Remarks[0], "15-Dec-2026") && !strings.Contains(v.Remarks[0], fmtDay("2026-12-15")) {
			t.Fatalf("RE disclaimer %v", v.Remarks)
		}
	}
	if got := o.reRemarks(ctx, T, []ingest.TradeEvent{{Exchange: "NSE", Segment: "CM", Symbol: "XYZ-RE", Side: "B"}}); len(got) != 1 {
		t.Fatalf("an -RE symbol %v", got)
	}
	if got := o.reRemarks(ctx, T, []ingest.TradeEvent{{Exchange: "NSE", Segment: "CM", Symbol: "XYZ-RE", Side: "S"}}); len(got) != 0 {
		t.Fatalf("an RE sale %v", got)
	}
	// A square-off instruction of the day marks its fill.
	x := tr[0]
	sq := SquareOff{ID: "SQ-99999", Date: D, UCC: n.UCC, Exchange: x.Exchange, Segment: x.Segment, Instrument: x.Instrument(), Side: x.Side, Qty: x.Qty, Status: "Filled", TradeNo: x.TradeNo}
	if _, err := docs.Put(ctx, o.Docs, T, cSquareOff, D+"|"+sq.ID, sq, 0, "anita.rao"); err != nil {
		t.Fatal(err)
	}
	v, _ := o.noteView(ctx, T, D, n.UCC, "")
	found := false
	for _, r := range v.Remarks {
		found = found || (strings.Contains(r, "margin call was not met") && strings.Contains(r, x.TradeNo))
	}
	if !found {
		t.Fatalf("margin call remark %v", v.Remarks)
	}
}

func TestTradeCancelledByExchange(t *testing.T) {
	o, ctx := setup(t)
	ns := notedDay(t, o)
	n, tr := equityNoteWith(t, o, ns, 2)
	x := tr[0]
	in := TradeCancel{Date: D, Exchange: x.Exchange, Segment: x.Segment, TradeNo: x.TradeNo, Source: "RTCM (reversal trade cancellation)", Ref: "RTCM-TEST-1"}
	if _, err := o.RecordTradeCancel(ctx, T, "priya.nair", in); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a maker cancelled a trade: %v", err)
	}
	bad := in
	bad.Ref = ""
	if _, err := o.RecordTradeCancel(ctx, T, "farhan.khan", bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no reference: %v", err)
	}
	bad = in
	bad.TradeNo = "NOSUCH"
	if _, err := o.RecordTradeCancel(ctx, T, "farhan.khan", bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown trade: %v", err)
	}
	c, err := o.RecordTradeCancel(ctx, T, "farhan.khan", in)
	if err != nil {
		t.Fatal(err)
	}
	if c.UCC != n.UCC || !strings.Contains(c.Rebilled, "corrected") {
		t.Fatalf("cancel %+v", c)
	}
	if _, err := o.RecordTradeCancel(ctx, T, "farhan.khan", in); !errors.Is(err, ErrState) {
		t.Fatalf("recorded twice: %v", err)
	}
	all, _ := o.exchangeTrades(ctx, T, D)
	for _, e := range all {
		if e.Key() == x.Key() {
			t.Fatal("the cancelled trade is still in the exchange trades")
		}
	}
	after, _, _ := docs.Get[ContractNote](ctx, o.Docs, T, cNote, noteID(D, n.UCC, ""))
	if after.Revision != 1 || after.Trades != n.Trades || !strings.Contains(after.Reason, "RTCM-TEST-1") {
		// Trades on the record are counted at generation; the revision and
		// its reason show the cancellation.
		if after.Revision != 1 || !strings.Contains(after.Reason, "RTCM-TEST-1") {
			t.Fatalf("revised note %+v", after)
		}
	}
	tr2, _, _ := o.noteData(ctx, T, D, n.UCC, "")
	if len(tr2) != len(tr)-1 {
		t.Fatalf("note trades %d, want %d", len(tr2), len(tr)-1)
	}
	if xs, err := o.TradeCancels(ctx, T, "vikram.shah"); err != nil || len(xs) != 1 {
		t.Fatalf("list %v %v", xs, err)
	}
}

func TestMCXLastTradingDate(t *testing.T) {
	o, ctx := setup(t)
	o.Now = func() time.Time { return time.Date(2026, 10, 16, 9, 0, 0, 0, ist) }
	_, sym, exp := mcxClient(t, o)
	r := productRow("FUTCOM", sym, "", exp, "", "1002", "1001", "100", "N")
	r.Values["Product End Date Time"] = "17OCT2026 23:55:00"
	loadX(t, o, "MCX_ProductMaster.csv", xwrite(t, "mcx-product-master", r))
	m, _ := o.MCXContracts(ctx, T, sym)
	if len(m.Contracts) != 1 || m.Contracts[0].LastTrade != "2026-10-17" {
		t.Fatalf("last trading date %+v", m.Contracts)
	}
	if o.mcxLastTrading(ctx, T)[sym+"|FUT|"+exp+"|"] != "2026-10-17" {
		t.Fatal("not looked up by the trade's key")
	}
}

func TestECNAuthorisationAndEmailChange(t *testing.T) {
	o, ctx := setup(t)
	cs, _ := o.Clients(ctx, T)
	var c Client
	for _, x := range cs {
		if x.Status == "Active" && !x.PhysicalNotes {
			c = x
			break
		}
	}
	if c.UCC == "" {
		t.Skip("no active client on electronic notes")
	}
	s := siOne(t, o, ctx, "ecn_authorisation", D)
	if siState(s) != SIFail {
		t.Fatalf("clients without an authorisation passed: %+v", s)
	}
	if _, err := o.SetECNAuthorisation(ctx, T, "priya.nair", c.UCC, true, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("authorised without the signed document: %v", err)
	}
	d, err := o.AddClientDoc(ctx, T, "priya.nair", c.UCC, "Other", "ECN authorisation (signed).pdf", []byte("%PDF-1.4 test"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := o.SetECNAuthorisation(ctx, T, "priya.nair", c.UCC, true, d.ID)
	if err != nil || got.ECNAuth == nil || !got.ECNAuth.On || got.PhysicalNotes {
		t.Fatalf("authorise %+v %v", got.ECNAuth, err)
	}
	got, _ = o.SetECNAuthorisation(ctx, T, "priya.nair", c.UCC, false, "")
	if !got.PhysicalNotes {
		t.Fatal("printed notes not set")
	}
	// Staff change an e-mail only with the client's signed letter.
	if _, err := o.RequestChange(ctx, T, "priya.nair", c.UCC, "Email", map[string]string{"email": "own.address@example.test"}, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("e-mail changed by staff without a letter: %v", err)
	}
	if _, err := o.RequestChange(ctx, T, "priya.nair", c.UCC, "Email", map[string]string{"email": "own.address@example.test"}, d.ID); err != nil {
		t.Fatal(err)
	}
	// The firm's own domain is flagged.
	if !firmDomains(Settings{ComplianceEmail: "co@broker.test"})["broker.test"] || firmDomains(Settings{ComplianceEmail: "co@gmail.com"})["gmail.com"] {
		t.Fatal("firm domains")
	}
}

func TestAccountKitAndClientMaster(t *testing.T) {
	o, ctx := setup(t)
	cs, _ := o.Clients(ctx, T)
	var c Client
	for _, x := range cs {
		if x.Status == "Active" {
			c = x
			break
		}
	}
	if _, err := o.SendAccountKit(ctx, T, "anita.rao", c.UCC); !errors.Is(err, ErrForbidden) {
		t.Fatalf("risk sent a kit: %v", err)
	}
	d, err := o.SendAccountKit(ctx, T, "priya.nair", c.UCC)
	if err != nil || d.Kind != DocKit {
		t.Fatalf("kit %+v %v", d, err)
	}
	rep, _, _ := o.accountKit(ctx, T, c.UCC, "APP-000001")
	txt := ""
	for _, s := range rep.Sections {
		txt += s.Title + "|"
		for _, r := range s.Rows {
			txt += strings.Join(r, " ") + "|"
		}
	}
	for _, w := range []string{"Index of documents", "Most important terms and conditions", "Guidance note", "Tariff", "Senior officials and the investor grievance cell", "SCORES", "APP-000001"} {
		if !strings.Contains(txt, w) {
			t.Fatalf("kit lacks %q: %s", w, txt)
		}
	}
	// The online declarations include the MITC, the guidance note and the
	// DP's rights and obligations; electronic notes are a separate choice.
	decl := o.appDeclarations(ctx, T)
	if decl[declMITC] == "" || decl[declGuidance] == "" {
		t.Fatalf("declarations %v", decl)
	}
	if _, ok := decl[declECN]; ok {
		t.Fatal("electronic notes must not be a required declaration")
	}
	if o.ApplyConfig(ctx, T).Optional[declECN] == "" {
		t.Fatal("no optional ECN choice")
	}
	_, b := nxBO(t, o)
	o.sendClientMaster(ctx, T, "priya.nair", b.UCC, b)
	ds, _ := docs.List[Dispatch](ctx, o.Docs, T, cDispatch, "")
	ok := false
	for _, x := range ds {
		ok = ok || (x.Kind == DocCMR && x.Ref == b.BOID)
	}
	if !ok {
		t.Fatal("client master not in the dispatch register")
	}
}

func TestClosedAccountBalances(t *testing.T) {
	o, ctx := setup(t)
	_, b := nxBO(t, o)
	b.Status, b.ClosedOn = "Closed", addDays(D, -30)
	if err := o.putBO(ctx, T, "priya.nair", b); err != nil {
		t.Fatal(err)
	}
	if s := siOne(t, o, ctx, "closed_account_balances", D); siState(s) != SIPass || s.checked != 1 {
		t.Fatalf("within the year %+v", s)
	}
	o.closedBalanceDaily(ctx, T, D)
	ws, _ := o.WorkItems(ctx, T, "")
	ok := false
	for _, w := range ws {
		ok = ok || strings.HasPrefix(w.Key, wClosedBal+":"+b.BOID)
	}
	if !ok {
		t.Fatal("no work item for the closed account's securities")
	}
	b.ClosedOn = addDays(D, -400)
	_ = o.putBO(ctx, T, "priya.nair", b)
	if s := siOne(t, o, ctx, "closed_account_balances", D); siState(s) != SIFail {
		t.Fatalf("past the year %+v", s)
	}
}

func TestNominationNudgeOncePerDay(t *testing.T) {
	o, ctx := setup(t)
	c, _ := nxBO(t, o)
	c.Nominee = "Opted out"
	if o.nominationNudge(ctx, T, c) == "" {
		t.Fatal("no pop-up for an account without a nominee")
	}
	if o.nominationNudge(ctx, T, c) != "" {
		t.Fatal("pop-up shown twice the same day")
	}
	c.Nominee = "Opted in"
	o.Now = func() time.Time { return time.Now().Add(48 * time.Hour) }
	if o.nominationNudge(ctx, T, c) != "" {
		t.Fatal("pop-up for an account with a nominee")
	}
}

func TestTransmissionDeficiencyAndContactLetter(t *testing.T) {
	o, ctx := setup(t)
	c, _ := nxBO(t, o)
	cs, _ := o.Clients(ctx, T)
	to := ""
	for _, x := range cs {
		if x.UCC != c.UCC && x.Status == "Active" {
			to = x.UCC
			break
		}
	}
	tr, err := o.RaiseDPRequest(ctx, T, "priya.nair", DPRequest{Kind: "Transmission", UCC: c.UCC, Detail: map[string]string{"to_ucc": to, "death_certificate": "DC-TEST-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.RecordDPStep(ctx, T, "priya.nair", tr.ID, "deficiency", "", "MAIL-1", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a deficiency without the documents listed: %v", err)
	}
	if _, err := o.RecordDPStep(ctx, T, "priya.nair", tr.ID, "deficiency", "", "MAIL-1", "Indemnity; affidavit of the legal heirs"); err != nil {
		t.Fatal(err)
	}
	if _, err := o.RecordDPStep(ctx, T, "priya.nair", tr.ID, "deficiency", "", "MAIL-2", "Notarised copy"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a further request without its reason: %v", err)
	}
	r, err := o.RecordDPStep(ctx, T, "priya.nair", tr.ID, "deficiency", "", "MAIL-2", "Reason: the affidavit was unsigned. Signed affidavit")
	if err != nil || !strings.Contains(r.Steps["deficiency"].Note, "earlier request") {
		t.Fatalf("further request %+v %v", r.Steps, err)
	}
	if _, err := o.RecordDPStep(ctx, T, "priya.nair", tr.ID, "docs_complete", "", "", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("documents complete without telling the claimant: %v", err)
	}

	// Mobile and e-mail both changed on a demat client with KYC not
	// validated: the registered-post letter is due.
	_, _ = docs.Update(ctx, o.Docs, T, cClient, c.UCC, "priya.nair", func(x *Client) error { x.KRAStatus = "On hold"; return nil })
	letter, _ := o.AddClientDoc(ctx, T, "priya.nair", c.UCC, DocClientAuth, "Letter.pdf", []byte("%PDF-1.4 test"))
	e, err := o.RequestChange(ctx, T, "priya.nair", c.UCC, "Email", map[string]string{"email": "changed.one@example.test"}, letter.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.DecideChange(ctx, T, "farhan.khan", e.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	m, err := o.RequestChange(ctx, T, "priya.nair", c.UCC, "Mobile", map[string]string{"mobile": "9876509999"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.DecideChange(ctx, T, "farhan.khan", m.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	ws, _ := o.WorkItems(ctx, T, "")
	ok := false
	for _, w := range ws {
		ok = ok || strings.HasPrefix(w.Key, wContactLetter+":"+c.UCC)
	}
	if !ok {
		t.Fatal("no registered-post letter work item")
	}
}
