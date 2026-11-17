package ops

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// All complainants, cases and numbers here are made up.

// raiaFirm makes the sample firm a research analyst, an investment adviser
// and a CDSL participant.
func raiaFirm(t *testing.T, regs ...string) (*Ops, func(string, string)) {
	t.Helper()
	o, ctx := setup(t)
	move := siClock(o, siAt("2026-10-20", "10:00"))
	if _, err := o.SaveFirmProfile(ctx, T, "vikram.shah", FirmProfile{Registrations: regs, CDSL: AnswerYes}); err != nil {
		t.Fatal(err)
	}
	return o, func(d, hm string) { move(siAt(d, hm)) }
}

func tk(t *testing.T, o *Ops, actor string, x Ticket) Ticket {
	t.Helper()
	x.Kind = firstOf(x.Kind, KindComplaint)
	x.Category = firstOf(x.Category, "Others")
	x.Subject = firstOf(x.Subject, "Made-up complaint")
	got, err := o.OpenTicket(t.Context(), T, actor, x, "Made-up details")
	if err != nil {
		t.Fatalf("ticket %+v: %v", x, err)
	}
	return got
}

// Each business has its own table in its annexure's format; the RA and IA
// tables have no stock exchanges row and count impersonation complaints.
func TestComplaintsTablesByBusiness(t *testing.T) {
	o, at := raiaFirm(t, "ra", "ia")
	ctx := t.Context()
	if d, _ := o.ComplaintsDisclosure(ctx, T, "2026-10"); d.Total.Received != 0 {
		t.Fatalf("the sample has complaints: %+v", d.Total)
	}
	tk(t, o, "priya.nair", Ticket{UCC: "BH1001", Source: "Stock exchanges", Ref: "NSE-T1"})
	dp := tk(t, o, "priya.nair", Ticket{UCC: "BH1002", Category: "Demat / DP", Source: "Depositories", Ref: "CDSL-T1"})
	ra := tk(t, o, "priya.nair", Ticket{Name: "Made-up Subscriber", Contact: "subscriber@example.com", Business: "RA"})
	tk(t, o, "priya.nair", Ticket{UCC: "BH1003", Business: "Research analyst", Source: "SEBI (SCORES)", Ref: "SCORES-T2", Impersonation: true})
	tk(t, o, "priya.nair", Ticket{UCC: "BH1004", Business: BizIA, Source: "Other sources"})
	if dp.Business != BizDP || ra.UCC != "" || ra.Name != "Made-up Subscriber" || ra.Business != BizRA || ra.Contact != "subscriber@example.com" {
		t.Fatalf("tickets %+v %+v", dp, ra)
	}
	for _, bad := range []Ticket{
		{UCC: "BH1001", Business: BizRA, Source: "Stock exchanges", Ref: "X"}, // no exchanges row for an RA
		{UCC: "BH1001", Source: "Depositories", Ref: "X"},                     // a broker's complaint from a depository
		{UCC: "BH1001", Business: BizDP, Source: "Stock exchanges", Ref: "X"}, // a DP's from an exchange
		{Name: "Not a client"},                                    // a broker's complaint from a non-client
		{UCC: "BH1001", Business: "Merchant banker"},              // no such business
		{UCC: "BH1001", Business: BizRA, ImpCase: "IMP-999999"},   // no such case
		{UCC: "BH1001", Business: BizDP, Source: "SEBI (SCORES)"}, // SCORES without its reference
		{UCC: "", Business: BizRA},                                // a non-client without a name
	} {
		bad.Kind, bad.Category, bad.Subject = KindComplaint, "Others", "x"
		if _, err := o.OpenTicket(ctx, T, "priya.nair", bad, "x"); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted %+v: %v", bad, err)
		}
	}
	// A client's own complaint about the DP is the DP's.
	if _, _, err := o.PortalAccess(ctx, T, "farhan.khan", "BH1001", nil, false); err != nil {
		t.Fatal(err)
	}
	pt, err := o.OpenTicket(ctx, T, "bh1001", Ticket{UCC: "BH1001", Kind: KindComplaint, Category: "Demat / DP", Subject: "Shares not credited", Impersonation: true}, "Made-up")
	if err != nil || pt.Business != BizDP || pt.Impersonation || pt.Source != "Directly from investors" {
		t.Fatalf("portal complaint %+v %v", pt, err)
	}

	br, _ := o.ComplaintsDisclosureFor(ctx, T, BizBroking, "2026-10")
	if len(br.Rows) != 4 || br.Rows[1].Source != "SEBI (SCORES 2.0)" || br.Rows[2].Source != "Stock Exchanges" || br.Rows[2].Received != 1 || br.Total.Received != 1 || br.Total.TotalPending != 1 || br.Total.Under3Months != 1 || br.Impersonation != nil {
		t.Fatalf("broker %+v", br)
	}
	d, _ := o.ComplaintsDisclosureFor(ctx, T, BizDP, "2026-10")
	if len(d.Rows) != 4 || d.Rows[2].Source != "Depositories" || d.Rows[2].Received != 1 || d.Total.Received != 2 || d.Unconfirmed == "" {
		t.Fatalf("DP %+v", d)
	}
	r, _ := o.ComplaintsDisclosureFor(ctx, T, "ra", "2026-10")
	if len(r.Rows) != 3 || r.Rows[2].Source != "Other Sources (if any)" || r.Total.Received != 2 || r.Impersonation == nil || *r.Impersonation != 1 || r.Title != "Complaint data to be displayed by RAs" {
		t.Fatalf("RA %+v", r)
	}
	if len(r.Annual) != 6 || r.Annual[0].Month != "2021-22" || r.Annual[5].Month != "2026-27" || r.AnnualTotal.Received != 2 || r.TrendTotal.Month != "Grand Total" || r.TrendTotal.Received != 2 || len(br.Annual) != 5 {
		t.Fatalf("RA trends %+v %+v", r.Annual, r.TrendTotal)
	}
	csv := string(r.CSV())
	for _, want := range []string{"Complaint data to be displayed by RAs", "Pending complaints > 3 months", "Number of complaints received during month against the RA due to impersonation by some other entity:,1", "Trend of annual disposal of complaints", "2021-22", "^ Average Resolution time"} {
		if !strings.Contains(csv, want) {
			t.Fatalf("RA CSV lacks %q:\n%s", want, csv)
		}
	}
	if strings.Contains(csv, "Stock Exchanges") {
		t.Fatal("an exchanges row on the RA table")
	}
	if b := string(br.CSV()); !strings.Contains(b, "Carried forward from previous month") || !strings.Contains(b, "Pending at the end of the month**: pending for more than 3 months") || strings.Contains(b, "impersonation") {
		t.Fatalf("broker CSV:\n%s", b)
	}
	ia, _ := o.ComplaintsDisclosureFor(ctx, T, BizIA, "2026-10")
	h := string(ia.HTML("Made-up Firm"))
	if !strings.Contains(h, "font-size:12pt") || !strings.Contains(h, "Complaint data to be displayed by IAs") || !strings.Contains(h, "against the IA due to impersonation") || strings.Contains(h, "<script") {
		t.Fatalf("IA HTML:\n%s", h)
	}
	// The annexures' note allows leaving impersonation complaints out.
	if _, err := o.SaveRAIASettings(ctx, T, "priya.nair", RAIASettings{AdjustImpersonation: true}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a maker changed the settings: %v", err)
	}
	if _, err := o.SaveRAIASettings(ctx, T, "vikram.shah", RAIASettings{AdjustImpersonation: true, RANo: "INH000000001"}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.SaveRAIASettings(ctx, T, "vikram.shah", RAIASettings{RANo: "INA000000001"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an IA number saved as the RA's: %v", err)
	}
	r, _ = o.ComplaintsDisclosureFor(ctx, T, BizRA, "2026-10")
	if r.Total.Received != 1 || *r.Impersonation != 1 || !r.Adjusted || len(r.Notes) != 1 {
		t.Fatalf("adjusted RA %+v", r)
	}
	// Marking a complaint's business moves it between the tables.
	tg, err := o.TagTicket(ctx, T, "priya.nair", "TKT-000001", TicketTag{Business: BizDP})
	if err == nil {
		t.Fatalf("a complaint from an exchange marked the DP's: %+v", tg)
	}
	if _, err := o.TagTicket(ctx, T, "meera.iyer", dp.ID, TicketTag{Business: BizBroking}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("branch staff marked a complaint: %v", err)
	}
	if _, err := o.TagTicket(ctx, T, "priya.nair", ra.ID, TicketTag{Business: BizBroking}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a non-client's complaint made a broker's: %v", err)
	}
	if tg, err = o.TagTicket(ctx, T, "priya.nair", pt.ID, TicketTag{Business: BizBroking}); err != nil || tg.Business != "" {
		t.Fatalf("tag %+v %v", tg, err)
	}
	if br, _ = o.ComplaintsDisclosureFor(ctx, T, BizBroking, "2026-10"); br.Total.Received != 2 {
		t.Fatalf("broker after the tag %+v", br.Total)
	}
	// The month's run builds every table.
	at("2026-11-02", "10:00")
	run, err := o.RunPeriodic(ctx, T, "priya.nair", "complaints_disclosure", "", "2026-11-02")
	if err != nil || len(run.Files) != 4 || run.Files[2].Name != "ComplaintsDisclosure_RA_2026-10.csv" || !strings.Contains(run.Summary, "RA: 1 received") {
		t.Fatalf("run %+v %v", run, err)
	}
	if bs := o.DisclosureBusinesses(ctx, T); len(bs) != 4 {
		t.Fatalf("tables %+v", bs)
	}
}

// The register of impersonation cases, the monthly reports to BSE and the
// depository, the Periodic work tasks and the self-inspection tests.
func TestImpersonationRegister(t *testing.T) {
	o, at := raiaFirm(t, "ra", "ia")
	ctx := t.Context()
	c := ImpCase{Businesses: []string{"RA"}, Who: "The firm's research desk", FoundOn: "2026-10-05", FoundBy: "Our own monitoring", Where: "Social media", Platform: "Made-up chat app",
		Handle: "@madeup_fake_tips", Impersonator: "Made-up Advisory", Details: "Offers paid tips in the firm's name"}
	for _, bad := range []func(*ImpCase){
		func(x *ImpCase) { x.Businesses = nil },
		func(x *ImpCase) { x.Businesses = []string{"PMS"} },
		func(x *ImpCase) { x.FoundOn = "2026-12-01" },
		func(x *ImpCase) { x.Where = "Billboard" },
		func(x *ImpCase) { x.Handle = "" },
		func(x *ImpCase) { x.FIR = ImpFIR{Number: "12/2026"} },
		func(x *ImpCase) {
			x.Takedowns = []ImpTakedown{{Platform: "Made-up chat app", RequestedOn: "2026-10-06", Status: "Gone"}}
		},
		func(x *ImpCase) { x.Banks = make([]ImpBank, 6) },
		func(x *ImpCase) { x.Notice = ImpNotice{On: "2026-10-07"} },
	} {
		x := c
		bad(&x)
		if _, err := o.SaveImpCase(ctx, T, "priya.nair", x); !errors.Is(err, ErrInvalid) {
			t.Fatalf("saved %+v: %v", x, err)
		}
	}
	if _, err := o.SaveImpCase(ctx, T, "meera.iyer", c); !errors.Is(err, ErrForbidden) {
		t.Fatalf("branch staff recorded a case: %v", err)
	}
	saved, err := o.SaveImpCase(ctx, T, "priya.nair", c)
	if err != nil || saved.ID != "IMP-000001" || saved.Status != "Open" || len(saved.Missing) != 4 {
		t.Fatalf("saved %+v %v", saved, err)
	}
	// A complaint it caused, from someone who is not a client.
	cx := tk(t, o, "priya.nair", Ticket{Name: "Made-up Investor", Business: BizRA, Source: "SEBI (SCORES)", Ref: "SCORES-T9", ImpCase: saved.ID})
	if !cx.Impersonation {
		t.Fatalf("complaint %+v", cx)
	}
	cs, _ := o.ImpCases(ctx, T, "priya.nair")
	if len(cs) != 1 || len(cs[0].Complaints) != 1 || cs[0].Complaints[0] != cx.ID {
		t.Fatalf("case after the complaint %+v", cs)
	}
	// Files kept with the case: PDF or image, up to 5 MB.
	if _, err := o.UploadImpEvidence(ctx, T, "priya.nair", saved.ID, "fir.txt", []byte("plain text")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a text file kept: %v", err)
	}
	if _, err := o.UploadImpEvidence(ctx, T, "priya.nair", saved.ID, "big.pdf", append([]byte("%PDF-1.4\n"), bytes.Repeat([]byte("x"), MaxImpEvidence)...)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a file over 5 MB kept: %v", err)
	}
	ev, err := o.UploadImpEvidence(ctx, T, "priya.nair", saved.ID, "fir.pdf", []byte("%PDF-1.4\nmade-up FIR copy\n%%EOF"))
	if err != nil || len(ev.Evidence) != 1 {
		t.Fatalf("evidence %+v %v", ev, err)
	}
	if f, err := o.ImpEvidence(ctx, T, "vikram.shah", ev.Evidence[0].ID); err != nil || !bytes.Contains(f.Content, []byte("made-up FIR")) {
		t.Fatalf("evidence back %v", err)
	}

	// The self-inspection: more than 7 days on, the steps are missing.
	at("2026-10-20", "23:00")
	s := siOne(t, o, ctx, "impersonation_actions", "2026-10-20")
	siWant(t, s, SIFail, "a case without its steps")
	if !strings.Contains(s.examples[0], "IMP-000001") || !strings.Contains(s.examples[0], "without the FIR") {
		t.Fatalf("example %v", s.examples)
	}
	saved.FIR = ImpFIR{PoliceStation: "Made-up Cyber Police Station", Number: "CR-12/2026", Date: "2026-10-06"}
	saved.Takedowns = []ImpTakedown{{Platform: "Made-up chat app", RequestedOn: "2026-10-06", Ref: "TD-1", Status: "Taken down", StatusOn: "2026-10-09"}}
	saved.Notice = ImpNotice{On: "2026-10-07", URL: "https://firm.example/notice"}
	saved.Alert = ImpAlert{On: "2026-10-07", By: "Email and SMS", Clients: 64}
	saved.Banks = []ImpBank{{Bank: "Made-up Bank", Account: "0000 1111 2222", IFSC: "HDFC0000001"}}
	if saved, err = o.SaveImpCase(ctx, T, "vikram.shah", saved); err != nil || len(saved.Missing) != 0 || len(saved.Complaints) != 1 || len(saved.Evidence) != 1 || saved.UpdatedBy != "vikram.shah" || saved.Banks[0].Account != "000011112222" {
		t.Fatalf("updated %+v %v", saved, err)
	}
	siWant(t, siOne(t, o, ctx, "impersonation_actions", "2026-10-20"), SIPass, "every step recorded")

	// October's reports: BSE for the RA (one case) and the IA (nil), CDSL
	// for the DP (nothing to report).
	at("2026-11-03", "10:00")
	v, err := o.ImpReports(ctx, T, "priya.nair", "")
	if err != nil || v.Month != "2026-10" || len(v.Bodies) != 3 {
		t.Fatalf("reports %+v %v", v, err)
	}
	by := map[string]ImpBodyView{}
	for _, b := range v.Bodies {
		by[b.Body] = b
	}
	if b := by[ImpBSERA]; len(b.Cases) != 1 || b.Complaints != 1 || b.Status != "Due" || b.Due != "2026-11-07" || b.RegNo != "" || !b.NilNeeded {
		t.Fatalf("BSE RA %+v", b)
	}
	if b := by[ImpBSEIA]; len(b.Cases) != 0 || b.Status != "Due" {
		t.Fatalf("BSE IA %+v", b)
	}
	if b := by[ImpCDSL]; b.Status != "Nothing to report" || b.Due != "2026-11-10" || !strings.Contains(b.Note, "DP2024-234") {
		t.Fatalf("CDSL %+v", b)
	}
	name, data, err := o.ImpReportFile(ctx, T, "priya.nair", ImpBSERA, "2026-10")
	if err != nil || name != "Impersonation_BSE-RA_2026-10.csv" || !strings.Contains(string(data), "FIR police station") || !strings.Contains(string(data), "CR-12/2026") || !strings.Contains(string(data), "Made-up chat app on 06-Oct-2026 (taken down 09-Oct-2026) ref TD-1") {
		t.Fatalf("report file %s %v:\n%s", name, err, data)
	}
	run, err := o.RunPeriodic(ctx, T, SystemActor, "raia_impersonation", "", "2026-11-03")
	if err != nil || run.Status != "Waiting for the report" || len(run.Files) != 1 {
		t.Fatalf("run %+v %v", run, err)
	}
	if _, err := o.MarkImpReport(ctx, T, "priya.nair", ImpReportReq{Body: ImpBSERA, Month: "2026-10", SubmittedOn: "2026-11-03", Ref: "X"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a maker recorded the report: %v", err)
	}
	if _, err := o.MarkImpReport(ctx, T, "vikram.shah", ImpReportReq{Body: ImpBSERA, Month: "2026-10", SubmittedOn: "2026-11-03"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("recorded with no acknowledgement: %v", err)
	}
	if _, err := o.MarkImpReport(ctx, T, "vikram.shah", ImpReportReq{Body: ImpBSERA, Month: "2026-10", SubmittedOn: "2026-11-03", Ref: "RAIA-ACK-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.MarkImpReport(ctx, T, "vikram.shah", ImpReportReq{Body: ImpBSEIA, Month: "2026-10"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a nil month recorded with no check: %v", err)
	}
	// The self-inspection after the due date, before the IA's nil check.
	at("2026-11-09", "23:00")
	s = siOne(t, o, ctx, "impersonation_report", "2026-11-09")
	siWant(t, s, SIFail, "the IA's nil check not recorded")
	if s.found != 1 || !strings.Contains(s.examples[0], "BSE · investment adviser · October 2026 · the nil check not recorded") {
		t.Fatalf("report check %v", s.examples)
	}
	v, err = o.MarkImpReport(ctx, T, "vikram.shah", ImpReportReq{Body: ImpBSEIA, Month: "2026-10", Searched: "Web search, app stores, the main social media", SearchedOn: "2026-11-02"})
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range v.Bodies {
		if b.Body == ImpBSEIA && (b.Recorded == nil || !b.Recorded.Nil || b.Status != "Done") {
			t.Fatalf("IA nil check %+v", b)
		}
		if b.Body == ImpBSERA && (b.Recorded == nil || b.Recorded.FileID == "" || b.Recorded.Ref != "RAIA-ACK-1" || b.Recorded.Late) {
			t.Fatalf("RA report %+v", b.Recorded)
		}
	}
	siWant(t, siOne(t, o, ctx, "impersonation_report", "2026-11-09"), SIPass, "both recorded")
	ps, _ := o.Periodic(ctx, T, "2026-11-09")
	for _, p := range ps {
		if p.ID == "raia_impersonation" && p.Status != "Done" {
			t.Fatalf("task %+v", p)
		}
	}

	// The DP impersonated: reported to the depository in NSDL's columns.
	at("2026-11-12", "10:00")
	if _, err := o.SaveImpCase(ctx, T, "vikram.shah", ImpCase{Businesses: []string{"DP"}, Who: "The firm's DP", FoundOn: "2026-11-11", Where: "Website", Handle: "https://madeup-fake-dp.example", Details: "A look-alike site asking for demat logins"}); err != nil {
		t.Fatal(err)
	}
	at("2026-12-02", "10:00")
	if _, err := o.SaveRAIASettings(ctx, T, "vikram.shah", RAIASettings{CDSLDP: "12345678", DPName: "Made-up DP Ltd"}); err != nil {
		t.Fatal(err)
	}
	run, err = o.RunPeriodic(ctx, T, SystemActor, "dp_impersonation", "", "2026-12-02")
	if err != nil || run.Status != "Waiting for the report" || len(run.Files) != 1 || run.Files[0].Name != "Impersonation_CDSL_2026-11.csv" {
		t.Fatalf("DP run %+v %v", run, err)
	}
	f, _ := o.PeriodicFileData(ctx, T, "priya.nair", run.Files[0].ID)
	if lines := strings.Split(strings.TrimSpace(string(f.Content)), "\n"); len(lines) != 2 || lines[0] != "DP ID,DP Name,Particulars of instance,Action Taken,Status" || !strings.HasPrefix(lines[1], "12345678,Made-up DP Ltd,") {
		t.Fatalf("NSDL columns:\n%s", f.Content)
	}
}

// Without the registrations the RA and IA work does not apply.
func TestImpersonationNotApplicable(t *testing.T) {
	o, _ := raiaFirm(t)
	ctx := t.Context()
	ps, _ := o.Periodic(ctx, T, "2026-11-03")
	for _, p := range ps {
		if p.ID == "raia_impersonation" && (p.Status != "Nothing to do" || !strings.Contains(p.Note, "Doesn't apply") || p.DueOn != "") {
			t.Fatalf("task %+v", p)
		}
	}
	if _, err := o.RunPeriodic(ctx, T, "priya.nair", "raia_impersonation", "", "2026-11-03"); !errors.Is(err, ErrState) {
		t.Fatalf("ran without the registrations: %v", err)
	}
	if _, err := o.OpenTicket(ctx, T, "priya.nair", Ticket{UCC: "BH1001", Kind: KindComplaint, Category: "Others", Subject: "x", Business: BizRA}, "x"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an RA complaint at a firm with no RA registration: %v", err)
	}
	if _, err := o.SaveImpCase(ctx, T, "priya.nair", ImpCase{Businesses: []string{"IA"}, Who: "x", FoundOn: "2026-10-05", Where: "Website", Handle: "x", Details: "x"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an IA case at a firm with no IA registration: %v", err)
	}
	siWant(t, siOne(t, o, ctx, "impersonation_actions", "2026-10-20"), SISkipped, "no case on the register")
}
