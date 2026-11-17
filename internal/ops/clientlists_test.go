package ops

import (
	"context"
	"errors"
	"strings"
	"testing"

	"ledgerline/internal/docs"
)

// All data here is made up: the sample firm's clients and lists written by
// the tests. No number, PAN or code is real.

// twoActive returns two active trading clients with mobiles.
func twoActive(t *testing.T, cs []Client) (Client, Client) {
	t.Helper()
	var out []Client
	for _, c := range cs {
		if c.Status == "Active" && !c.DPOnly && normMobile(c.Mobile) != "" && len(c.Segments) > 0 {
			out = append(out, c)
		}
	}
	if len(out) < 2 {
		t.Fatal("the sample firm has fewer than two active clients with mobiles")
	}
	return out[0], out[1]
}

func workFor(t *testing.T, o *Ops, process, key string) WorkItem {
	t.Helper()
	ws, _ := o.WorkItems(context.Background(), T, "")
	for _, w := range ws {
		if w.Process == process && w.Key == process+":"+key {
			return w
		}
	}
	return WorkItem{}
}

func TestMNRLMatchesHoldAndClear(t *testing.T) {
	o, ctx := setup(t)
	siClock(o, siAt(D, "10:00"))
	cs, _ := o.Clients(ctx, T)
	c1, c2 := twoActive(t, cs)
	siWant(t, siOne(t, o, ctx, "mnrl_matches", D), SISkipped, "no list loaded yet")
	// Before the day's list is loaded, the daily check asks for it.
	if r, err := o.RunPeriodic(ctx, T, SystemActor, "mnrl_trading", "", D); err != nil || r.Status != "Waiting for a file" {
		t.Fatalf("daily check %+v %v", r, err)
	}
	if w := workFor(t, o, "periodic", "mnrl_trading:"+D); w.Status != "Open" || w.View != "mnrl" {
		t.Fatalf("no work item to load the list: %+v", w)
	}

	// DoT's list: one of the firm's numbers (written as +91 with spaces)
	// among others.
	m1 := normMobile(c1.Mobile)
	dot := "Mobile Number,Date of Disconnection,Reason of Disconnection\n9000000001,01-10-2026,Non-usage\n+91 " + m1[:5] + " " + m1[5:] + ",02-10-2026,Customer request\n"
	l, err := o.LoadMNRLList(ctx, T, "priya.nair", MNRLLoadReq{Format: MNRLListDoT, Name: "MNRL_DIP_16102026.csv", Date: D}, []byte(dot))
	if err != nil || l.Rows != 2 || l.New < 1 || l.ID == "" {
		t.Fatalf("%+v %v", l, err)
	}
	v, err := o.MNRL(ctx, T, "priya.nair", D)
	if err != nil || v.Open != l.New {
		t.Fatalf("%+v %v", v, err)
	}
	if w := workFor(t, o, "periodic", "mnrl_trading:"+D); w.Status != "Done" {
		t.Fatalf("the work item to load the list is still %s", w.Status)
	}
	var m MNRLRow
	for _, x := range v.Matches {
		if x.UCC == c1.UCC && x.Holder == "First" {
			m = x
		}
	}
	if m.ID == "" || m.Status != MNRLNew || m.Mobile == m1 || !strings.HasSuffix(m.Mobile, m1[6:]) || m.Reason != "Customer request" {
		t.Fatalf("match %+v (the number is shown masked)", m)
	}
	// The client is flagged; SMS and WhatsApp are held, email goes on.
	c, _ := o.Client(ctx, T, c1.UCC)
	if len(c.MNRL) == 0 || c.MNRL[0].Match != m.ID {
		t.Fatalf("flag %+v", c.MNRL)
	}
	ms, err := o.sendEvent(ctx, T, "priya.nair", "margin.shortfall", c1.UCC, map[string]string{"shortfall": "1", "date": D}, "")
	if err != nil || len(ms) == 0 {
		t.Fatal(ms, err)
	}
	for _, x := range ms {
		if (x.Channel == ChSMS || x.Channel == ChWhatsApp) && (x.Status != "Held" || !strings.Contains(x.Note, m.ID)) {
			t.Fatalf("%s not held: %+v", x.Channel, x)
		}
	}
	em, _ := o.sendEvent(ctx, T, "priya.nair", "change.approved", c1.UCC, map[string]string{"kind": "Email", "id": "X"}, "")
	for _, x := range em {
		if x.Channel == ChEmail && x.Status == "Held" {
			t.Fatalf("email held: %+v", x)
		}
	}
	// The WhatsApp bot says nothing about the account to the number.
	reply, err := o.WhatsAppInbound(ctx, T, "+91"+m1, "BAL", D)
	if err != nil || strings.Contains(reply, "₹") || !strings.Contains(reply, "registered email") {
		t.Fatalf("bot reply %q %v", reply, err)
	}
	// An Ops maker has the work item.
	if w := workFor(t, o, wMNRL, m.ID); w.Status != "Open" || w.Role != RoleMaker || w.View != "mnrl" || strings.Contains(w.Detail, m1) {
		t.Fatalf("work %+v", w)
	}
	// The same list again opens nothing new.
	l2, _ := o.LoadMNRLList(ctx, T, "priya.nair", MNRLLoadReq{Format: MNRLListDoT, Name: "MNRL_DIP_16102026b.csv", Date: D}, []byte(dot))
	if l2.New != 0 || l2.Again != l.New {
		t.Fatalf("again %+v", l2)
	}
	// Contacted, then the number is changed by an approved change request.
	if _, err := o.SetMNRLStatus(ctx, T, "priya.nair", m.ID, MNRLContacted, "Emailed the client at the registered address"); err != nil {
		t.Fatal(err)
	}
	if _, err := o.SetMNRLStatus(ctx, T, "priya.nair", m.ID, MNRLUpdated, "changed"); err == nil {
		t.Fatal("marked updated while the record still has the number")
	}
	if _, err := o.SetMNRLStatus(ctx, T, "meera.iyer", m.ID, MNRLContacted, "called"); err == nil {
		t.Fatal("branch staff worked the match")
	}
	ch, err := o.RequestChange(ctx, T, "priya.nair", c1.UCC, "Mobile", map[string]string{"mobile": "6000000017"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.DecideChange(ctx, T, "farhan.khan", ch.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	got, _, _ := docs.Get[MNRLMatch](ctx, o.Docs, T, cMNRLMatch, m.ID)
	if got.Status != MNRLUpdated || got.Change != ch.ID || len(got.History) != 3 {
		t.Fatalf("after the change %+v", got)
	}
	if _, held := o.mnrlHeld(ctx, T, m1); held {
		t.Fatal("the old number is still held")
	}
	if c, _ := o.Client(ctx, T, c1.UCC); len(c.MNRL) != 0 {
		t.Fatalf("flag not cleared %+v", c.MNRL)
	}
	if w := workFor(t, o, wMNRL, m.ID); w.Status != "Done" {
		t.Fatalf("work %+v", w)
	}
	v, _ = o.MNRL(ctx, T, "priya.nair", D)
	for _, x := range v.Matches {
		if x.ID == m.ID && len(x.Follow) == 0 {
			t.Fatalf("no follow-up shown for the exchanges: %+v", x)
		}
	}

	// NSE's report: matched by client code; then not the client's number.
	m2 := normMobile(c2.Mobile)
	nse := "Report for disconnected Mobile numbers\nCLIENT_CODE|Mobile No.\n" + c2.UCC + "|" + m2 + "\nNOSUCH1|9000000002\n"
	ln, err := o.LoadMNRLList(ctx, T, "priya.nair", MNRLLoadReq{Format: MNRLListNSE, Name: "disconnected.txt", Date: D}, []byte(nse))
	if err != nil || ln.New != 1 || ln.Skipped != 1 {
		t.Fatalf("%+v %v", ln, err)
	}
	ms2, _ := docs.List[MNRLMatch](ctx, o.Docs, T, cMNRLMatch, "")
	var n2 MNRLMatch
	for _, x := range ms2 {
		if x.UCC == c2.UCC && x.Source == "NSE" {
			n2 = x
		}
	}
	if _, held := o.mnrlHeld(ctx, T, m2); !held {
		t.Fatal("NSE's match is not held")
	}
	if _, err := o.SetMNRLStatus(ctx, T, "farhan.khan", n2.ID, MNRLNotClients, "NSE still had an old number for the code; the UCC is sent again"); err != nil {
		t.Fatal(err)
	}
	if _, held := o.mnrlHeld(ctx, T, m2); held {
		t.Fatal("still held after the check")
	}

	// DoT's list again for the second client, then NSDL's reactivation data
	// lifts only that hold.
	if _, err := o.LoadMNRLList(ctx, T, "priya.nair", MNRLLoadReq{Format: MNRLListDoT, Name: "mnrl2.csv", Date: D}, []byte("Mobile Number\n"+m2+"\n")); err != nil {
		t.Fatal(err)
	}
	if _, held := o.mnrlHeld(ctx, T, m2); !held {
		t.Fatal("not held")
	}
	lr, err := o.LoadMNRLList(ctx, T, "priya.nair", MNRLLoadReq{Format: MNRLListReac, Name: "Reactivation_Data_of_DoT.csv", Date: D}, []byte("Mobile Number,Reactivation Date\n0"+m2+",10-10-2026\n"))
	if err != nil || lr.Cleared != 1 {
		t.Fatalf("%+v %v", lr, err)
	}
	if _, held := o.mnrlHeld(ctx, T, m2); held {
		t.Fatal("still held after reactivation")
	}

	// The daily check is done for the day; a new match not taken up in 3
	// business days fails the self-inspection.
	ps, _ := o.Periodic(ctx, T, D)
	for _, p := range ps {
		if p.ID == "mnrl_trading" && p.Status != "Done" {
			t.Fatalf("daily check %+v", p)
		}
	}
	if _, err := o.LoadMNRLList(ctx, T, "priya.nair", MNRLLoadReq{Format: MNRLListDoT, Name: "mnrl3.csv", Date: D}, []byte("Mobile Number\n"+m2+"\n")); err != nil {
		t.Fatal(err)
	}
	siWant(t, siOne(t, o, ctx, "mnrl_matches", D), SIPass, "a new match on its first day")
	siWant(t, siOne(t, o, ctx, "mnrl_matches", "2026-10-26"), SIFail, "a match not taken up")

	// A file without the layout's header is refused.
	if _, err := o.LoadMNRLList(ctx, T, "priya.nair", MNRLLoadReq{Format: MNRLListNSE, Name: "x.csv"}, []byte("Name,City\nA,B\n")); err == nil || !strings.Contains(err.Error(), "header") {
		t.Fatalf("no header: %v", err)
	}
}

func TestDeceasedBlockCloseAndSteps(t *testing.T) {
	o, ctx := setup(t)
	siClock(o, siAt("2026-11-17", "10:00"))
	date := "2026-11-17"
	siWant(t, siOne(t, o, ctx, "deceased_ucc", date), SISkipped, "no list loaded yet")
	if r, err := o.RunPeriodic(ctx, T, SystemActor, "kra_demise", "", date); err != nil || r.Status != "Waiting for a file" || r.Period != "2026-10" {
		t.Fatalf("monthly %+v %v", r, err)
	}
	ras, err := o.RAS(ctx, T, D)
	if err != nil || len(ras.Rows) == 0 {
		t.Fatal("no client in credit", err)
	}
	c, _ := o.Client(ctx, T, ras.Rows[0].UCC) // a client with money to pay out
	cs, _ := o.Clients(ctx, T)
	var other Client
	for _, x := range cs {
		if x.Status == "Active" && x.UCC != c.UCC && x.PAN != c.PAN && !x.DPOnly {
			other = x
			break
		}
	}
	// The month's list: one client by PAN and code, one PAN of nobody, one
	// row not a PAN, and a client whose intimation will be false.
	list := "PAN,Client Code,Client Name\n" + c.PAN + "," + c.UCC + ",A\nZZZPZ9999Z,,B\n" + other.PAN + ",,C\nNOTAPAN,,D\n"
	if _, err := o.LoadDemiseList(ctx, T, "meera.iyer", DemiseLoadReq{Name: "12345_Demise_Clients_by_KRA.csv", Date: date}, []byte(list)); !errors.Is(err, ErrForbidden) {
		t.Fatalf("branch staff loaded the demise list: %v", err)
	}
	l, err := o.LoadDemiseList(ctx, T, siCO, DemiseLoadReq{Name: "12345_Demise_Clients_by_KRA.csv", Date: date}, []byte(list))
	if err != nil || l.Matched != 2 || l.NotClients != 1 || l.Issues != 1 || l.Period != "2026-10" || l.Cutoff != "2026-11-21" || !strings.Contains(l.Circular, "77375") {
		t.Fatalf("%+v %v", l, err)
	}
	v, err := o.Demise(ctx, T, siCO, date)
	if err != nil || v.Open != 2 || !v.Loaded || v.Cutoff != "2026-11-21" || !v.CanAct || !v.CanStep {
		t.Fatalf("%+v %v", v, err)
	}
	if w := workFor(t, o, "periodic", "kra_demise:2026-10"); w.Status != "Done" {
		t.Fatalf("the work item to load the list is still %s", w.Status)
	}
	var d, f DemiseRow
	for _, x := range v.Cases {
		switch x.UCC {
		case c.UCC:
			d = x
		case other.UCC:
			f = x
		}
	}
	if d.MatchedBy != "PAN and client code" || d.DaysLeft != 4 || d.Stage != DemiseReported || d.PAN == c.PAN || len(d.UCCs) == 0 {
		t.Fatalf("case %+v", d)
	}
	// Debits are blocked: payouts, the kill switch, demat debits, running
	// account settlement.
	c, _ = o.Client(ctx, T, c.UCC)
	if !c.Blocked || c.Deceased == nil || !strings.HasPrefix(c.BlockedReason, deceasedReason) {
		t.Fatalf("not blocked %+v", c)
	}
	if w, _ := o.Withdrawable(ctx, T, D, c.UCC); !w.Blocked || w.Available != 0 {
		t.Fatalf("withdrawable %+v", w)
	}
	if _, err := o.RequestPayout(ctx, T, "priya.nair", D, c.UCC, 100, "Head office"); !errors.Is(err, ErrState) {
		t.Fatalf("payout requested: %v", err)
	}
	if _, err := o.SetBlocked(ctx, T, "anita.rao", c.UCC, false, ""); !errors.Is(err, ErrState) {
		t.Fatalf("unblocked by the kill switch: %v", err)
	}
	if b := o.bo(ctx, T, c); !b.debitFrozen() || !strings.Contains(b.freezeWhy(), "deceased") {
		t.Fatalf("demat debits not frozen: %+v", b)
	}
	ras, _ = o.RAS(ctx, T, D)
	for _, r := range ras.Rows {
		if r.UCC == c.UCC {
			t.Fatal("running account settlement would pay the deceased client")
		}
	}
	// Work items for the compliance officer.
	for _, k := range []string{":verify", ":ucc", ":transmission"} {
		if w := workFor(t, o, wDeceased, d.ID+k); w.Status != "Open" || w.Role != RoleCompliance {
			t.Fatalf("%s %+v", k, w)
		}
	}
	siWant(t, siOne(t, o, ctx, "deceased_ucc", date), SIPass, "before the cut-off")
	siWant(t, siOne(t, o, ctx, "deceased_ucc", "2026-11-21"), SIFail, "not closed by the cut-off")

	// Closing comes after the death is verified.
	if _, err := o.DemiseAction(ctx, T, "farhan.khan", d.ID, DemiseActionReq{Action: "close"}); !errors.Is(err, ErrState) {
		t.Fatalf("closed before verification: %v", err)
	}
	if _, err := o.DemiseAction(ctx, T, "farhan.khan", d.ID, DemiseActionReq{Action: "verify", Note: "original seen"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a checker verified a death: %v", err)
	}
	if _, err := o.DemiseAction(ctx, T, siCO, d.ID, DemiseActionReq{Action: "verify", Note: "Death certificate verified online"}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.DemiseAction(ctx, T, siCO, d.ID, DemiseActionReq{Action: "verify", Note: "again"}); !errors.Is(err, ErrState) {
		t.Fatalf("verified twice: %v", err)
	}
	dc, err := o.DemiseAction(ctx, T, "farhan.khan", d.ID, DemiseActionReq{Action: "close"})
	if err != nil || dc.ClosedAt == "" {
		t.Fatalf("%+v %v", dc, err)
	}
	if _, err := o.DemiseAction(ctx, T, "farhan.khan", d.ID, DemiseActionReq{Action: "close"}); !errors.Is(err, ErrState) {
		t.Fatalf("closed twice: %v", err)
	}
	c, _ = o.Client(ctx, T, c.UCC)
	for _, s := range c.Segments {
		if c.SegmentStatus(s) != SegClosed {
			t.Fatalf("%s is %s", s, c.SegmentStatus(s))
		}
	}
	csv, err := o.DemiseUCCList(ctx, T, siCO, "")
	if err != nil || !strings.Contains(string(csv), c.UCC) {
		t.Fatalf("%s %v", csv, err)
	}
	for _, u := range dc.UCCs {
		if u.Status != DemiseSending {
			t.Fatalf("%+v", u)
		}
		if _, err := o.DemiseAction(ctx, T, "priya.nair", d.ID, DemiseActionReq{Action: "closed", Exchange: u.Exchange, Ref: "ACK-1"}); err != nil {
			t.Fatal(err)
		}
	}
	siWant(t, siOne(t, o, ctx, "deceased_ucc", "2026-11-21"), SIFail, "the other client is still open")
	dt, err := o.DemiseAction(ctx, T, "priya.nair", d.ID, DemiseActionReq{Action: "transmission", Note: "Emailed the nominee the procedure, form and documents list"})
	if err != nil || dt.Status != DemiseDone {
		t.Fatalf("%+v %v", dt, err)
	}
	if w := workFor(t, o, wDeceased, d.ID+":transmission"); w.Status != "Done" {
		t.Fatalf("%+v", w)
	}
	// The other intimation is false: the KRA is told, debits are free again.
	before := other.Blocked
	if _, err := o.DemiseAction(ctx, T, siCO, f.ID, DemiseActionReq{Action: "false", Note: "Video call with the client; KRA modification sent", Ref: "KRA-1"}); err != nil {
		t.Fatal(err)
	}
	other, _ = o.Client(ctx, T, other.UCC)
	if other.Deceased != nil || other.Blocked != before {
		t.Fatalf("block not lifted %+v", other)
	}
	siWant(t, siOne(t, o, ctx, "deceased_ucc", "2026-11-21"), SIPass, "closed and false")
	ps, _ := o.Periodic(ctx, T, date)
	for _, p := range ps {
		if p.ID == "kra_demise" && (p.Status != "Done" || p.Period != "2026-10") {
			t.Fatalf("monthly %+v", p)
		}
	}

	// The cut-off can be changed for the period's open cases.
	if _, err := o.SetDemiseCutoff(ctx, T, siCO, "2026-10", "2026-10-15", ""); err == nil {
		t.Fatal("a cut-off before the end of the period")
	}
}

func TestListsOnExchangeFiles(t *testing.T) {
	o, ctx := setup(t)
	siClock(o, siAt("2026-11-17", "10:00"))
	cs, _ := o.Clients(ctx, T)
	c, _ := twoActive(t, cs)
	rs, err := o.LoadExchangeFile(ctx, T, siCO, "12345_Demise_Clients_by_KRA.csv", []byte("PAN\n"+c.PAN+"\nBADPAN\n"), true)
	if err != nil || len(rs) != 1 || rs[0].Format != DemiseListNSE || rs[0].Added != 1 || !strings.Contains(rs[0].Loaded, "Deceased clients") {
		t.Fatalf("%+v %v", rs, err)
	}
	if c2, _ := o.Client(ctx, T, c.UCC); c2.Deceased == nil {
		t.Fatal("not blocked from Exchange files")
	}
	rs, err = o.LoadExchangeFile(ctx, T, "priya.nair", "Report_for_disconnected_Mobile_numbers.csv", []byte("Client Code,Mobile Number\n"+c.UCC+","+c.Mobile+"\n"), true)
	if err != nil || len(rs) != 1 || rs[0].Format != MNRLListNSE || rs[0].Added != 1 {
		t.Fatalf("%+v %v", rs, err)
	}
}
