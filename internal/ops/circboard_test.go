package ops

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/money"
)

// Made-up circulars: numbers, dates and text are invented for the tests.

// circRow is one line of a CIRCULARS file.
type circRow struct{ Source, Ref, Date, Subject, Dept, URL, Text string }

func circLoad(t *testing.T, o *Ops, ctx context.Context, rows ...circRow) map[string]Circular {
	t.Helper()
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	w.Write([]string{"Source", "Ref", "Date", "Subject", "Department", "URL", "Text"})
	for _, r := range rows {
		w.Write([]string{r.Source, r.Ref, r.Date, r.Subject, r.Dept, r.URL, r.Text})
	}
	w.Flush()
	if _, err := o.LoadFile(ctx, T, "vikram.shah", "CIRCULARS", "", "circ.csv", b.Bytes()); err != nil {
		t.Fatal(err)
	}
	return circBy(t, o, ctx)
}

func circBy(t *testing.T, o *Ops, ctx context.Context) map[string]Circular {
	t.Helper()
	cs, err := o.Circulars(ctx, T)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]Circular{}
	for _, c := range cs {
		by[c.Ref] = c
	}
	return by
}

// circFirm: a member of NSE, BSE and MCX; NSE cash and BSE cash cleared by
// the firm at NSE Clearing, NSE F&O through another clearing member, MCX
// through a commodity clearing member; no currency or NSE commodity.
func circFirm(t *testing.T, o *Ops, ctx context.Context) {
	t.Helper()
	set, _ := o.Settings(ctx, T)
	set.MemberCodes = map[string]string{"NSE": "90001", "BSE": "9001", "MCX": "50001"}
	set.ClearingFor = map[string]string{"NSE-FO": "CM:CM901", "NSE-CD": "NONE", "NSE-COM": "NONE", "BSE-FO": "NONE", "BSE-CD": "NONE", "MCX-COM": "CM:CM777"}
	set.ClearingMemberNames = map[string]string{"NSE-FO": "Example Clearing Ltd", "MCX-COM": "Sample Commodity Clearing Pvt Ltd"}
	if err := o.SaveSettings(ctx, T, "admin", set); err != nil {
		t.Fatal(err)
	}
}

const circToBrokers = "To, All Stock Brokers through the recognised stock exchanges. Dear Sir / Madam, "

var circSample = []circRow{
	{"NCDEX", "NCDEX/TRD/2026/101", "2026-10-12", "Revision in daily price limits", "Trading", "", "Members are informed of revised daily price limits."},
	{"CDSL", "CDSL/OPS/DP/2026/501", "2026-10-12", "Changes to beneficial owner account modification", "Operations", "", "All Depository Participants are advised to note the changes."},
	{"SEBI", "SEBI/HO/MIRSD/2026/901", "2026-10-13", "Margin trading facility: revised conditions", "MIRSD", "", circToBrokers + "Stock brokers providing margin trading facility shall follow the revised conditions with effect from 01-Dec-2026."},
	{"SEBI", "SEBI/HO/IMD/2026/902", "2026-10-13", "Periodic reporting format", "IMD", "", "To, All Portfolio Managers. Dear Sir / Madam, Portfolio managers shall report in the revised format."},
	{"SEBI", "SEBI/HO/ITD/2026/903", "2026-10-14", "Cybersecurity and Cyber Resilience Framework (CSCRF): clarifications", "ITD", "", circToBrokers + "Regulated entities shall complete VAPT and submit the reports."},
	{"MCXCCL", "MCXCCL/RISK/2026/77", "2026-10-14", "Collection of upfront margins", "Risk", "", "Clearing members shall collect upfront margins from their constituents."},
	{"SEBI", "SEBI/HO/MRD/2026/904", "2026-10-15", "Position limits in currency derivatives", "MRD", "", circToBrokers + "Position limits for currency derivatives are revised."},
	{"NSE", "NSE/INSP/2026/905", "2026-10-15", "Revised format for the half-yearly net worth certificate", "Inspection", "", "Members are required to submit the net worth certificate in the revised format."},
}

func TestCircularApplicability(t *testing.T) {
	o, ctx := setup(t)
	circFirm(t, o, ctx)
	by := circLoad(t, o, ctx, circSample...)
	want := func(ref, verdict, why string) {
		t.Helper()
		a := by[ref].Applies
		if a == nil || a.Verdict != verdict || !strings.Contains(strings.Join(append([]string{a.Reason}, a.Reasons...), " | "), why) {
			t.Fatalf("%s: want %s (%s), got %+v", ref, verdict, why, a)
		}
	}
	want("NCDEX/TRD/2026/101", ApplyNo, "you are not a member of NCDEX")
	want("CDSL/OPS/DP/2026/501", ApplyUnsure, "tell Ledgerline whether you are a CDSL DP")
	want("SEBI/HO/MIRSD/2026/901", ApplyNo, "you have no MTF")
	want("SEBI/HO/IMD/2026/902", ApplyNo, "it is addressed to portfolio managers")
	want("SEBI/HO/ITD/2026/903", ApplyUnsure, "tell Ledgerline your CSCRF category")
	want("MCXCCL/RISK/2026/77", ApplyUnsure, "Sample Commodity Clearing Pvt Ltd clears your trades at MCX Clearing Corporation")
	want("SEBI/HO/MRD/2026/904", ApplyNo, "you do not trade currency derivatives")
	want("NSE/INSP/2026/905", ApplyYes, "you are a member of NSE")
	if l := by["SEBI/HO/MIRSD/2026/901"].Applies.Label(); l != "Doesn't apply: you have no MTF" {
		t.Fatalf("label %q", l)
	}
	// The rules decide nothing by themselves: every one is still to review.
	for _, c := range by {
		if c.Status == "Not applicable" && len(c.Tasks) > 0 {
			t.Fatalf("%s closed by the rules", c.Ref)
		}
	}

	// The profile: compliance officer and Admin only; it says what to answer.
	if _, err := o.FirmProfileFor(ctx, T, "priya.nair"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a maker read the firm profile: %v", err)
	}
	if _, err := o.SaveFirmProfile(ctx, T, "anita.rao", FirmProfile{CSCRF: "small"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("risk changed the firm profile: %v", err)
	}
	v, err := o.FirmProfileFor(ctx, T, "vikram.shah")
	if err != nil {
		t.Fatal(err)
	}
	qs := strings.Join(v.Questions, " | ")
	if !strings.Contains(qs, "CSCRF") || !strings.Contains(qs, "CDSL") || !strings.Contains(qs, "algorithmic") {
		t.Fatalf("questions %v", v.Questions)
	}
	facts := map[string]FirmFact{}
	for _, f := range v.Facts {
		facts[f.Key] = f
	}
	if f := facts["clearing"]; !strings.Contains(f.Value, "Example Clearing Ltd") || !strings.Contains(f.Value, "NSE Clearing") || !strings.Contains(f.From, "Settings") {
		t.Fatalf("clearing fact %+v", f)
	}
	if f := facts["exchanges"]; !strings.Contains(f.Value, "NSE CM") || strings.Contains(f.Value, "CD") || !f.Known {
		t.Fatalf("exchanges fact %+v", f)
	}
	if _, err := o.SaveFirmProfile(ctx, T, "vikram.shah", FirmProfile{CSCRF: "huge"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown category: %v", err)
	}
	if _, err := o.SaveFirmProfile(ctx, T, "vikram.shah", FirmProfile{Algo: "maybe"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad answer: %v", err)
	}
	yes := true
	v, err = o.SaveFirmProfile(ctx, T, "vikram.shah", FirmProfile{CDSL: AnswerYes, NSDL: AnswerNo, Algo: AnswerNo, CSCRF: "small", Registrations: []string{"pms"}, MTF: &yes})
	if err != nil {
		t.Fatal(err)
	}
	if !o.CompProfile(ctx, T).MTF || !v.MTF {
		t.Fatal("MTF not kept on the compliance profile")
	}
	for _, q := range v.Questions {
		if strings.Contains(q, "CSCRF") || strings.Contains(q, "CDSL") {
			t.Fatalf("answered question still asked: %v", v.Questions)
		}
	}
	by = circBy(t, o, ctx)
	want("CDSL/OPS/DP/2026/501", ApplyYes, "you are a CDSL DP")
	want("SEBI/HO/MIRSD/2026/901", ApplyYes, "you offer MTF")
	want("SEBI/HO/IMD/2026/902", ApplyYes, "portfolio managers and you are one")
	want("SEBI/HO/ITD/2026/903", ApplyYes, "you are a Small-size RE under CSCRF")
	o.SaveFirmProfile(ctx, T, "vikram.shah", FirmProfile{CDSL: AnswerNo, NSDL: AnswerNo, CSCRF: "exempt"})
	by = circBy(t, o, ctx)
	want("CDSL/OPS/DP/2026/501", ApplyNo, "you are not a CDSL DP")
	want("SEBI/HO/ITD/2026/903", ApplyNo, "you are below the CSCRF thresholds")
	evs, _ := o.App.Audit.List(ctx, T, 0)
	n := 0
	for _, e := range evs {
		if e.Action == "firm.profile" && e.Actor == "vikram.shah" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("%d firm profile changes audited", n)
	}

	// A circular addressed only to the MIIs; and one whose subject names a
	// segment the firm trades.
	by = circLoad(t, o, ctx,
		circRow{"SEBI", "SEBI/HO/MRD/2026/906", "2026-10-16", "Framework for market infrastructure institutions", "MRD", "", "To, All Recognised Stock Exchanges, Clearing Corporations and Depositories. Dear Sir / Madam, MIIs shall ..."},
		circRow{"SEBI", "SEBI/HO/MRD/2026/907", "2026-10-16", "Daily price bands", "MRD", "", "Margin and price band rules are revised for all segments."})
	want("SEBI/HO/MRD/2026/906", ApplyNo, "it is addressed to stock exchanges, clearing corporations and depositories")
	want("SEBI/HO/MRD/2026/907", ApplyUnsure, "Ledgerline could not tell who it is for")
}

func TestCircularOverride(t *testing.T) {
	o, ctx := setup(t)
	circFirm(t, o, ctx)
	by := circLoad(t, o, ctx, circSample...)
	mtf := by["SEBI/HO/MIRSD/2026/901"]
	if mtf.Applies.Verdict != ApplyNo || len(mtf.Tasks) == 0 {
		t.Fatalf("mtf %+v", mtf.Applies)
	}
	if _, err := o.ActOnCircular(ctx, T, "anita.rao", mtf.ID, CircularAction{Kind: "applies", Verdict: ApplyYes, Note: "x"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("risk decided applicability: %v", err)
	}
	if _, err := o.ActOnCircular(ctx, T, "vikram.shah", mtf.ID, CircularAction{Kind: "applies", Verdict: ApplyYes}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no reason: %v", err)
	}
	c, err := o.ActOnCircular(ctx, T, "vikram.shah", mtf.ID, CircularAction{Kind: "applies", Verdict: ApplyYes, Note: "MTF approval applied for; start now"})
	if err != nil {
		t.Fatal(err)
	}
	if a := c.Applies; a.Verdict != ApplyYes || !a.Overridden || a.Rules != ApplyNo || !strings.Contains(a.RulesWhy, "no MTF") || a.By != "vikram.shah" || c.Applies.Label() != "Applies: MTF approval applied for; start now" {
		t.Fatalf("override %+v", a)
	}
	evs, _ := o.App.Audit.List(ctx, T, 0)
	found := false
	for _, e := range evs {
		found = found || e.Action == "circular.applies" && e.Object == mtf.ID && e.Actor == "vikram.shah" && fmt.Sprint(e.Detail["reason"]) == "MTF approval applied for; start now" &&
			fmt.Sprint(e.Detail["rules"]) == ApplyNo && fmt.Sprint(e.Detail["to"]) == ApplyYes
	}
	if !found {
		t.Fatal("the override is not in the audit trail with its reason")
	}
	// Doesn't apply, with the reason: the circular is closed as not applicable.
	nse := by["NSE/INSP/2026/905"]
	c, err = o.ActOnCircular(ctx, T, "admin", nse.ID, CircularAction{Kind: "applies", Verdict: ApplyNo, Note: "Filed by the group company"})
	if err != nil || c.Status != "Not applicable" || c.Applies.Verdict != ApplyNo || c.Applies.Rules != ApplyYes {
		t.Fatalf("no: %+v %v", c.Applies, err)
	}
	if _, err := o.ActOnCircular(ctx, T, "vikram.shah", nse.ID, CircularAction{Kind: "accept_task", Task: c.Tasks[0].ID}); !errors.Is(err, ErrState) {
		t.Fatalf("accepted on a circular that does not apply: %v", err)
	}
	// Back to the rules: it applies again and is open.
	c, err = o.ActOnCircular(ctx, T, "vikram.shah", nse.ID, CircularAction{Kind: "applies", Verdict: "rules"})
	if err != nil || c.Override != nil || c.Applies.Verdict != ApplyYes || c.Status != "Action needed" {
		t.Fatalf("rules: %+v %s %v", c.Applies, c.Status, err)
	}
	// The older "not applicable" records the decision the same way.
	c, _ = o.ActOnCircular(ctx, T, "vikram.shah", nse.ID, CircularAction{Kind: "not_applicable", Note: "Not a net worth filer"})
	if c.Override == nil || c.Applies.Verdict != ApplyNo || !c.Applies.Overridden {
		t.Fatalf("not applicable %+v", c.Applies)
	}
}

const circAIReply = `Settle clients' running accounts within the new period | +30 effective | Ops checker | Settlement register for the month | payouts | List clients due; Run the settlement; Keep the register
Change the CUSPA payment period in Ledgerline | 2026-12-01 | Admin | Screenshot of the new setting | cuspa | Change the setting; Check a sample client
- Tell clients about the change | - | compliance | Copy of the notice sent | charter | Draft the notice; Send it
nonsense without a separator`

func circAIOn(t *testing.T, o *Ops, ctx context.Context, reply string) *fakeComposer {
	t.Helper()
	fc := &fakeComposer{reply: reply}
	o.LedgyAI, o.Meter = fc, meter(t, "2", "")
	set, _ := o.Settings(ctx, T)
	set.CircularAI = true
	if err := o.SaveSettings(ctx, T, "admin", set); err != nil {
		t.Fatal(err)
	}
	if _, err := o.AddAICredit(ctx, T, "provider", money.Amount(100000), "INV-T"); err != nil {
		t.Fatal(err)
	}
	return fc
}

func TestCircularAIProposals(t *testing.T) {
	o, ctx := setup(t)
	circFirm(t, o, ctx)
	var client Client
	cs, _ := o.Clients(ctx, T)
	for _, c := range cs {
		if strings.Contains(c.Name, " ") && !c.Private {
			client = c
			break
		}
	}
	text := circToBrokers + "Stock brokers shall settle the running accounts of clients within the revised period with effect from 01-Dec-2026. A complaint from " + client.Name + " is cited as an example. Unpaid securities shall be dealt with as before."
	by := circLoad(t, o, ctx, circRow{"SEBI", "SEBI/HO/MIRSD/2026/950", "2026-10-15", "Running account settlement: revised period", "MIRSD", "https://www.sebi.gov.in/example", text})
	c := by["SEBI/HO/MIRSD/2026/950"]
	rules := len(c.Tasks)
	if rules == 0 || c.Applies.Verdict != ApplyYes {
		t.Fatalf("rules %+v", c)
	}

	// No model, then the feature off: refused with the reason; the rule
	// actions stand, nothing is sent.
	if _, err := o.ReadCircularAI(ctx, T, "vikram.shah", c.ID); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "No AI model") {
		t.Fatalf("no model: %v", err)
	}
	fc := &fakeComposer{reply: circAIReply}
	o.LedgyAI, o.Meter = fc, meter(t, "2", "")
	if _, err := o.ReadCircularAI(ctx, T, "vikram.shah", c.ID); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "Read circulars with AI") || len(fc.sent) != 0 {
		t.Fatalf("feature off: %v (%d sent)", err, len(fc.sent))
	}
	// India-only mode: cannot be switched on, and the gate refuses.
	o.Residency.IndiaOnly = true
	set, _ := o.Settings(ctx, T)
	set.CircularAI = true
	if err := o.SaveSettings(ctx, T, "admin", set); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "India-only") {
		t.Fatalf("switched on in India-only mode: %v", err)
	}
	o.Residency.IndiaOnly = false
	fc = circAIOn(t, o, ctx, circAIReply)
	o.Residency.IndiaOnly = true
	if on, why := o.aiFeatureState(ctx, T, AIFeatureCirculars); on || !strings.Contains(why, "India-only") {
		t.Fatalf("India-only: %v %q", on, why)
	}
	got, err := o.ReadCircularAI(ctx, T, "vikram.shah", c.ID)
	if !errors.Is(err, ErrInvalid) || len(fc.sent) != 0 || len(circBy(t, o, ctx)["SEBI/HO/MIRSD/2026/950"].Tasks) != rules {
		t.Fatalf("India-only read: %v (%d sent)", err, len(fc.sent))
	}
	if after := circBy(t, o, ctx)["SEBI/HO/MIRSD/2026/950"]; after.AI == nil || !after.AI.Refused || !strings.Contains(after.AI.Note, "rule-based actions stand") {
		t.Fatalf("refusal not noted: %+v", after.AI)
	}
	o.Residency.IndiaOnly = false

	// On: the model reads the masked text and proposes; a maker may not ask.
	if _, err := o.ReadCircularAI(ctx, T, "priya.nair", c.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker: %v", err)
	}
	got, err = o.ReadCircularAI(ctx, T, "vikram.shah", c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(fc.sent) != 1 || fc.instr[0] != circAIInstr || !strings.Contains(fc.sent[0], "SEBI/HO/MIRSD/2026/950") || !strings.Contains(fc.sent[0], "running accounts") {
		t.Fatalf("sent %v", fc.sent)
	}
	if client.Name != "" && strings.Contains(fc.sent[0], client.Name) {
		t.Fatal("a client name went to the model unmasked")
	}
	if len(got.Proposals) != 3 || got.AI == nil || got.AI.Proposed != 3 || got.AI.Model == "" || !strings.Contains(got.AI.Note, "1 line") {
		t.Fatalf("proposals %+v ai %+v", got.Proposals, got.AI)
	}
	p1, p2, p3 := got.Proposals[0], got.Proposals[1], got.Proposals[2]
	if p1.ID != "O1" || p1.Role != RoleChecker || p1.DueDays != 30 || p1.DueFrom != "effective" || p1.Area != "payouts" || len(p1.Steps) != 3 || p1.Status != ObProposed {
		t.Fatalf("p1 %+v", p1)
	}
	if d, rule := p1.due(got); d != "2026-12-31" || !strings.Contains(rule, "30 days after the effective date") {
		t.Fatalf("p1 due %s %s", d, rule)
	}
	if p2.Due != "2026-12-01" || p2.Role != RoleAdmin || p2.Area != "cuspa" || p3.Role != RoleCompliance || p3.Area != "charter" || p3.Due != "" || p3.DueDays != 0 {
		t.Fatalf("p2 %+v p3 %+v", p2, p3)
	}
	log, _ := o.AILog(ctx, T, "vikram.shah", "", "")
	if len(log.Entries) != 1 || log.Entries[0].Feature != AIFeatureCirculars || log.Entries[0].Outcome != AIAnswered || log.Entries[0].Charge <= 0 {
		t.Fatalf("AI log %+v", log.Entries)
	}

	// Nothing is applied before acceptance: no task, no work item.
	if len(got.Tasks) != rules || got.Review != rules+3 {
		t.Fatalf("tasks %d review %d", len(got.Tasks), got.Review)
	}
	if _, err := o.RunStage(ctx, T, SystemActor, "2026-11-28", "", "compliance"); err != nil {
		t.Fatal(err)
	}
	ws, _ := o.WorkItems(ctx, T, "")
	for _, w := range ws {
		if strings.Contains(w.Title, "CUSPA payment period") || strings.Contains(w.Title, "Settle clients' running accounts") {
			t.Fatalf("a proposal reached the work queue: %+v", w)
		}
	}

	// Accept, changing it; reject with the reason.
	if _, err := o.ActOnCircular(ctx, T, "anita.rao", c.ID, CircularAction{Kind: "accept", Proposal: "O2"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("risk accepted: %v", err)
	}
	got, err = o.ActOnCircular(ctx, T, "vikram.shah", c.ID, CircularAction{Kind: "accept", Proposal: "O2", Role: RoleCompliance, Due: "2026-11-25", Title: "Change the CUSPA payment period to 3 days"})
	if err != nil {
		t.Fatal(err)
	}
	var task CircularTask
	for _, x := range got.Tasks {
		if x.ID == "O2" {
			task = x
		}
	}
	if task.Source != "ai" || task.Accepted != "vikram.shah" || task.Role != RoleCompliance || task.Due != "2026-11-25" || task.Area != "cuspa" || task.Needs != "Screenshot of the new setting" || task.Title != "Change the CUSPA payment period to 3 days" {
		t.Fatalf("task %+v", task)
	}
	if _, err := o.ActOnCircular(ctx, T, "vikram.shah", c.ID, CircularAction{Kind: "accept", Proposal: "O2"}); !errors.Is(err, ErrState) {
		t.Fatalf("accepted twice: %v", err)
	}
	if _, err := o.ActOnCircular(ctx, T, "vikram.shah", c.ID, CircularAction{Kind: "reject", Proposal: "O3"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("rejected without a reason: %v", err)
	}
	got, _ = o.ActOnCircular(ctx, T, "vikram.shah", c.ID, CircularAction{Kind: "reject", Proposal: "O3", Note: "The charter already covers it"})
	if got.Proposals[2].Status != ObRejected || got.Review != rules+1 {
		t.Fatalf("reject %+v review %d", got.Proposals[2], got.Review)
	}
	// Reading again replaces only the proposals still waiting.
	fc.reply = "Publish the revised period on the website | +7 circular | Compliance officer | Screenshot of the page | charter | Update the page"
	got, err = o.ReadCircularAI(ctx, T, "vikram.shah", c.ID)
	if err != nil || len(got.Proposals) != 3 || got.Proposals[2].ID != "O4" || got.Proposals[0].Status != ObAccepted || got.Proposals[1].Status != ObRejected {
		t.Fatalf("again %+v %v", got.Proposals, err)
	}
	// AI switched off for the firm: refused, the rules' actions and the
	// accepted one stand.
	if _, err := o.SetAIOff(ctx, T, "vikram.shah", true, "Vendor review"); err != nil {
		t.Fatal(err)
	}
	if _, err := o.ReadCircularAI(ctx, T, "vikram.shah", c.ID); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "switched AI off") {
		t.Fatalf("AI off: %v", err)
	}
	if after := circBy(t, o, ctx)["SEBI/HO/MIRSD/2026/950"]; len(after.Tasks) != rules+1 {
		t.Fatalf("tasks after AI off: %d", len(after.Tasks))
	}
	// A reply that says nothing applies.
	if r, note := parseCircAI("NONE", c, 1); len(r) != 0 || !strings.Contains(note, "nothing") {
		t.Fatalf("NONE: %v %q", r, note)
	}
	// The inventory and the disclosures know the feature.
	inv, _ := o.AIInventory(ctx, T, "vikram.shah")
	found := false
	for _, s := range inv {
		found = found || s.ID == AIFeatureCirculars && strings.Contains(strings.Join(s.Controls, " "), "16C")
	}
	if !found {
		t.Fatal("no inventory entry")
	}
}

// circView brings the Circulars board up to date and reads it.
func circView(t *testing.T, o *Ops, ctx context.Context, actor, board string) BoardView {
	t.Helper()
	b, _, _ := o.boardFor(ctx, T, "admin", board)
	if _, err := o.bsSync(ctx, T, b, o.now(), 0); err != nil {
		t.Fatal(err)
	}
	v, err := o.GetBoard(ctx, T, actor, board, false)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func circCard(t *testing.T, v BoardView, cid, ob string) (Card, BoardList) {
	t.Helper()
	c, ok := bsCardFor(v, "circular", cid+"|"+ob)
	if !ok {
		t.Fatalf("no card for %s %s", cid, ob)
	}
	l, _ := boardList(v.Board, c.List)
	return c, l
}

func TestCircularBoard(t *testing.T) {
	o, ctx := setup(t)
	step := bsClock(o)
	circFirm(t, o, ctx)
	if _, ok := bsSourceOf("circulars"); !ok {
		t.Fatal("no circulars source")
	}
	set, err := o.SetUpLedgerlineBoards(ctx, T, "admin", []string{"circulars"})
	if err != nil || len(set) != 1 || !set[0].Created {
		t.Fatalf("set up %+v %v", set, err)
	}
	bid := set[0].Board
	var names []string
	v := circView(t, o, ctx, "vikram.shah", bid)
	for _, l := range v.Board.Lists {
		names = append(names, l.Name)
	}
	if strings.Join(names, ", ") != "To review, Accepted / to do, In progress, Waiting for evidence, Done, Not applicable" {
		t.Fatalf("lists %v", names)
	}
	by := circLoad(t, o, ctx, circSample...)
	nse := by["NSE/INSP/2026/905"] // applies: rule actions, not yet reviewed
	mtf := by["SEBI/HO/MIRSD/2026/901"]
	if len(nse.Tasks) == 0 || nse.Applies.Verdict != ApplyYes {
		t.Fatalf("nse %+v", nse)
	}
	tk := nse.Tasks[0]
	v = circView(t, o, ctx, "vikram.shah", bid)
	cd, l := circCard(t, v, nse.ID, tk.ID)
	if l.Source != "review" || cd.Link.View != "circulars" || cd.Link.Status != "To review" || !strings.HasPrefix(cd.Title, "NSE/INSP/2026/905: ") {
		t.Fatalf("card %+v in %s", cd, l.Name)
	}
	for _, s := range []string{"NSE circular NSE/INSP/2026/905 of 15-Oct-2026", "**What to do:** " + tk.Title, "**Owner:** " + tk.Role, "**Evidence needed:** " + tk.Needs, "Applies: you are a member of NSE", "Ledgerline may need a change:** Reporting formats"} {
		if !strings.Contains(cd.Description, s) {
			t.Fatalf("description lacks %q:\n%s", s, cd.Description)
		}
	}
	if len(cd.Checklists) != 2 || cd.Checklists[0].Name != "Steps" || cd.Checklists[1].Name != "Evidence" || len(cd.Checklists[1].Items) == 0 {
		t.Fatalf("checklists %+v", cd.Checklists)
	}
	labels := map[string]bool{}
	for _, id := range cd.Labels {
		lb, _ := boardLabel(v.Board, id)
		labels[lb.Name] = true
	}
	if !labels["NSE"] || !labels[tk.Role] || !labels["Ledgerline may need a change"] {
		t.Fatalf("labels %v", labels)
	}
	if v.Board.Settings[bsLabelKey+"circ:update"] == "" {
		t.Fatal("no Ledgerline updates label on the board")
	}
	// Rules say the MTF circular does not apply: no card is made for it.
	if _, ok := bsCardFor(v, "circular", mtf.ID+"|"+mtf.Tasks[0].ID); ok {
		t.Fatal("a card for a circular that does not apply")
	}
	// The card window shows the obligation live.
	h, err := o.BoardLinkRecord(ctx, T, "priya.nair", "circular", nse.ID+"|"+tk.ID)
	if err != nil || h.Status != "To review" || !strings.Contains(strings.Join(h.Lines, " | "), "Applies: you are a member of NSE") {
		t.Fatalf("link record %+v %v", h, err)
	}
	if _, err := o.BoardLinkRecord(ctx, T, "meera.iyer", "circular", nse.ID+"|"+tk.ID); err == nil {
		t.Fatal("branch staff saw the obligation")
	}

	list := func(src string) string { return bsListBy(t, v.Board, src).ID }
	move := func(actor, src string) error {
		_, err := o.MoveCard(ctx, T, actor, bid, cd.ID, CardMove{List: list(src)})
		return err
	}
	// Accepting is the compliance officer's; nothing goes to Done unaccepted.
	if err := move("priya.nair", "todo"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker accepted: %v", err)
	}
	if err := move("vikram.shah", "doing"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("started before acceptance: %v", err)
	}
	if err := move("vikram.shah", "todo"); err != nil {
		t.Fatal(err)
	}
	c := circBy(t, o, ctx)["NSE/INSP/2026/905"]
	// Its only action accepted, the circular counts as reviewed.
	if c.Tasks[0].Accepted != "vikram.shah" || c.Status != "Action needed" {
		t.Fatalf("accepted %+v %s", c.Tasks[0], c.Status)
	}
	if err := move("vikram.shah", "review"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("back to review: %v", err)
	}
	if err := move("vikram.shah", "na"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("not applicable without a reason: %v", err)
	}
	// The owner moves it on.
	owner := map[string]string{RoleCompliance: "vikram.shah", RoleChecker: "farhan.khan", RoleRisk: "anita.rao", RoleMaker: "priya.nair", RoleAdmin: "admin"}[tk.Role]
	if err := move(owner, "doing"); err != nil {
		t.Fatal(err)
	}
	if c = circBy(t, o, ctx)["NSE/INSP/2026/905"]; c.Tasks[0].Stage != StageInProgress {
		t.Fatalf("stage %q", c.Tasks[0].Stage)
	}
	// Evidence first: refused without a file, then without the checklist.
	if err := move(owner, "done"); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "attach the evidence") {
		t.Fatalf("done without evidence: %v", err)
	}
	if cd, _, _ = o.GetCard(ctx, T, owner, bid, cd.ID); cd.List != list("doing") {
		t.Fatal("the refused card moved")
	}
	if _, err := o.AttachFile(ctx, T, owner, bid, cd.ID, "networth-format.txt", []byte("Net worth certificate filed in the new format (made-up test file).\n")); err != nil {
		t.Fatal(err)
	}
	if err := move(owner, "done"); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "tick the Evidence checklist") {
		t.Fatalf("done with the checklist open: %v", err)
	}
	cd, _, _ = o.GetCard(ctx, T, owner, bid, cd.ID)
	ev := cd.Checklists[1]
	for _, it := range ev.Items {
		done := true
		if _, err := o.EditChecklist(ctx, T, owner, bid, cd.ID, ChecklistOp{Op: "update_item", Checklist: ev.ID, Item: it.ID, Done: &done}); err != nil {
			t.Fatal(err)
		}
	}
	if err := move(owner, "done"); err != nil {
		t.Fatal(err)
	}
	c = circBy(t, o, ctx)["NSE/INSP/2026/905"]
	t0 := c.Tasks[0]
	if t0.Status != "Done" || !strings.Contains(t0.Evidence, fmt.Sprintf("card #%d", cd.No)) || !strings.Contains(t0.Evidence, "networth-format.txt") || len(t0.Files) != 1 || t0.Files[0].File == "" || t0.Card != bid+"|"+cd.ID {
		t.Fatalf("task after the board %+v", t0)
	}

	// The other way: an action done on Calendar & circulars moves its card.
	step(10 * time.Second)
	cy := by["SEBI/HO/ITD/2026/903"] // not sure (no CSCRF category): on the board
	if cy.Applies.Verdict != ApplyUnsure {
		t.Fatalf("cyber %+v", cy.Applies)
	}
	o.ActOnCircular(ctx, T, "vikram.shah", cy.ID, CircularAction{Kind: "review", Note: "Applies"})
	if _, err := o.ActOnCircular(ctx, T, "vikram.shah", cy.ID, CircularAction{Kind: "stage", Task: cy.Tasks[0].ID, Stage: StageEvidence}); err != nil {
		t.Fatal(err)
	}
	v = circView(t, o, ctx, "vikram.shah", bid)
	cc, l := circCard(t, v, cy.ID, cy.Tasks[0].ID)
	labels = map[string]bool{}
	for _, id := range cc.Labels {
		lb, _ := boardLabel(v.Board, id)
		labels[lb.Name] = true
	}
	if l.Source != "evidence" || !labels["Applies? Not sure"] || !labels["High priority"] {
		t.Fatalf("cyber card in %s %v", l.Name, labels)
	}
	if _, err := o.ActOnCircular(ctx, T, "vikram.shah", cy.ID, CircularAction{Kind: "done_task", Task: cy.Tasks[0].ID, Evidence: "Policy v5 approved, IT committee note 3"}); err != nil {
		t.Fatal(err)
	}
	v = circView(t, o, ctx, "vikram.shah", bid)
	if _, l = circCard(t, v, cy.ID, cy.Tasks[0].ID); l.Source != "done" {
		t.Fatalf("done on the screen, card in %s", l.Name)
	}
	if _, err := o.ActOnCircular(ctx, T, "vikram.shah", cy.ID, CircularAction{Kind: "drop_task", Task: cy.Tasks[1].ID, Note: "The vendor was told on the call"}); err != nil {
		t.Fatal(err)
	}
	v = circView(t, o, ctx, "vikram.shah", bid)
	if _, l = circCard(t, v, cy.ID, cy.Tasks[1].ID); l.Source != "na" {
		t.Fatalf("dropped, card in %s", l.Name)
	}

	// A proposal is a card to review; accepting it keeps the same card.
	circAIOn(t, o, ctx, "Publish the revised net worth format to branches | +10 circular | Compliance officer | Copy of the message | reporting | Write it; Send it")
	got, err := o.ReadCircularAI(ctx, T, "vikram.shah", by["SEBI/HO/ITD/2026/903"].ID)
	if err != nil || len(got.Proposals) != 1 {
		t.Fatalf("read %+v %v", got.Proposals, err)
	}
	v = circView(t, o, ctx, "vikram.shah", bid)
	pc, l := circCard(t, v, cy.ID, "O1")
	if l.Source != "review" || !strings.Contains(pc.Description, "Proposed by the AI model") {
		t.Fatalf("proposal card in %s:\n%s", l.Name, pc.Description)
	}
	if _, err := o.MoveCard(ctx, T, "vikram.shah", bid, pc.ID, CardMove{List: list("doing")}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a proposal started: %v", err)
	}
	if _, err := o.MoveCard(ctx, T, "vikram.shah", bid, pc.ID, CardMove{List: list("todo")}); err != nil {
		t.Fatal(err)
	}
	c = circBy(t, o, ctx)["SEBI/HO/ITD/2026/903"]
	if c.Proposals[0].Status != ObAccepted || c.Tasks[len(c.Tasks)-1].ID != "O1" || c.Tasks[len(c.Tasks)-1].Source != "ai" {
		t.Fatalf("accepted from the board %+v", c.Proposals)
	}
	step(10 * time.Second)
	v = circView(t, o, ctx, "vikram.shah", bid)
	pc2, l := circCard(t, v, cy.ID, "O1")
	if pc2.ID != pc.ID || l.Source != "todo" || !strings.Contains(pc2.Description, "accepted by vikram.shah") {
		t.Fatalf("accepted card %s in %s:\n%s", pc2.ID, l.Name, pc2.Description)
	}

	// Automation does not change circulars.
	if _, err := o.MoveCard(ctx, T, "system:rules", bid, pc.ID, CardMove{List: list("done")}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("automation moved a circular card: %v", err)
	}
}

func TestCircularDailyNotice(t *testing.T) {
	o, ctx := setup(t)
	step := bsClock(o)
	circFirm(t, o, ctx)
	circLoad(t, o, ctx, circSample...)
	at := time.Date(2026, 10, 17, 8, 0, 0, 0, ist)
	o.Now = func() time.Time { return at }
	// No Circulars board: no notice (the work queue still asks for a review).
	o.circTick(ctx, T, at.Add(2*time.Hour))
	if ns, _, _ := o.BoardNotices(ctx, T, "vikram.shah", 50); len(ns) != 0 {
		t.Fatalf("notice without a board: %+v", ns)
	}
	_ = step
	if _, err := o.SetUpLedgerlineBoards(ctx, T, "admin", []string{"circulars"}); err != nil {
		t.Fatal(err)
	}
	o.circTick(ctx, T, at) // before 09:30
	if ns, _, _ := o.BoardNotices(ctx, T, "vikram.shah", 50); len(ns) != 0 {
		t.Fatal("notice before 09:30")
	}
	o.circTick(ctx, T, at.Add(3*time.Hour))
	ns, _, _ := o.BoardNotices(ctx, T, "vikram.shah", 50)
	if len(ns) != 1 || ns[0].Kind != NoticeOther || ns[0].Board != "B-ledgerline-circulars" {
		t.Fatalf("notices %+v", ns)
	}
	if !strings.HasPrefix(ns[0].Text, "1 new circular applies to you, 3 may apply, ") || !strings.Contains(ns[0].Text, "obligations need review") || !strings.Contains(ns[0].Text, "look not to apply") {
		t.Fatalf("text %q", ns[0].Text)
	}
	for _, s := range []string{"NSE/INSP", "net worth", "SEBI/HO"} {
		if strings.Contains(ns[0].Text, s) {
			t.Fatalf("the notice carries circular content: %q", ns[0].Text)
		}
	}
	o.circTick(ctx, T, at.Add(5*time.Hour))
	if ns, _, _ = o.BoardNotices(ctx, T, "vikram.shah", 50); len(ns) != 1 {
		t.Fatalf("%d notices in one day", len(ns))
	}
	if ns, _, _ = o.BoardNotices(ctx, T, "priya.nair", 50); len(ns) != 0 {
		t.Fatal("a maker got the compliance notice")
	}
	if got := circNoticeText(3, 0, 0, 2); got != "3 new circulars apply to you, 2 obligations need review (Calendar & circulars)" {
		t.Fatalf("text %q", got)
	}
}

// The AI reading of new circulars by itself, a few a minute, once each.
func TestCircularAIQueue(t *testing.T) {
	o, ctx := setup(t)
	bsClock(o)
	circFirm(t, o, ctx)
	fc := circAIOn(t, o, ctx, "Check the revised format | - | Compliance officer | Note of the check | reporting | Read it")
	long := strings.Repeat("Members are required to submit the certificate in the revised format. ", 5)
	circLoad(t, o, ctx,
		circRow{"NSE", "NSE/INSP/2026/961", "2026-10-15", "Revised reporting format A", "Inspection", "", long},
		circRow{"NSE", "NSE/INSP/2026/962", "2026-10-15", "Revised reporting format B", "Inspection", "", long},
		circRow{"NSE", "NSE/INSP/2026/963", "2026-10-15", "Revised reporting format C", "Inspection", "", long},
		circRow{"NCDEX", "NCDEX/2026/964", "2026-10-15", "Revised reporting format D", "Inspection", "", long})
	o.circAIQueue(ctx, T, o.now())
	if len(fc.sent) != 2 {
		t.Fatalf("read %d in a minute", len(fc.sent))
	}
	o.circAIQueue(ctx, T, o.now())
	o.circAIQueue(ctx, T, o.now())
	if len(fc.sent) != 3 {
		t.Fatalf("read %d in all (the NCDEX one does not apply)", len(fc.sent))
	}
	for _, c := range circBy(t, o, ctx) {
		if c.Source == "NSE" && (c.AI == nil || c.AI.By != circAIActor || len(c.Proposals) != 1) {
			t.Fatalf("%s: %+v", c.Ref, c.AI)
		}
	}
}
