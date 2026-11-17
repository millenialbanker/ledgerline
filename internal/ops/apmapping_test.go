package ops

import (
	"errors"
	"strings"
	"testing"

	"ledgerline/internal/docs"
)

// All clients, APs and numbers here are the made-up sample firm's.

func apReady(t *testing.T) (*Ops, func(string, string)) {
	t.Helper()
	o, ctx := setup(t)
	move := siClock(o, siAt("2026-10-12", "10:00")) // a Monday
	if err := o.seedSampleBrokerage(ctx, T, D); err != nil {
		t.Fatal(err)
	}
	if err := o.seedSamplePartners(ctx, T); err != nil { // maps 13 clients to AP001 and AP002
		t.Fatal(err)
	}
	return o, func(date, hm string) { move(siAt(date, hm)) }
}

func apEx(t *testing.T, v APMappingView, x string) APMapExchange {
	t.Helper()
	ex, ok := apExchangeOf(v, x)
	if !ok {
		t.Fatalf("no %s in %+v", x, v.Exchanges)
	}
	return ex
}

func apRec(ex APMapExchange, ucc string) (APMapRecord, bool) {
	for _, r := range ex.Records {
		if r.UCC == ucc {
			return r, true
		}
	}
	return APMapRecord{}, false
}

// Mappings made in a week go to each exchange the client trades on, checked
// and written in the layout; the upload is recorded with the acknowledgement.
func TestAPMappingWeek(t *testing.T) {
	o, at := apReady(t)
	ctx := t.Context()
	co := "vikram.shah"
	week := "2026-10-18" // 12 to 18 Oct 2026
	v, err := o.APMappingReport(ctx, T, co, week, "2026-10-14")
	if err != nil {
		t.Fatal(err)
	}
	set, _ := o.Settings(ctx, T)
	if v.Week != week || v.From != "2026-10-12" || v.Due != apWeekDue(set, week) || v.Label != "12–18 Oct 2026" || v.Status != "Upcoming" || len(v.Changes) != 13 {
		t.Fatalf("week %+v (%d changes)", v, len(v.Changes))
	}
	nse, mcx := apEx(t, v, "NSE"), apEx(t, v, "MCX")
	if len(nse.Records) != 13 || nse.Counts[APInFile] != 12 || nse.Counts[APWaits] != 1 || nse.Confirmed || nse.UsesUCC || nse.Status != "To upload" || nse.Note == "" {
		t.Fatalf("NSE %+v", nse)
	}
	// Only clients with a commodity segment trade on MCX.
	if len(mcx.Records) != 2 || mcx.FileName != "56204_MCX_AP_CLIENTS_18102026.csv" {
		t.Fatalf("MCX %+v", mcx)
	}
	r, _ := apRec(nse, "BH1006")
	if r.Kind != APMapped || r.To != "AP001" || r.ToReg != "AP0012345" || r.ChangedOn != "2026-10-12" || r.By != "admin" || r.Via != APViaMapping || r.State != APInFile || r.PAN != MaskPAN(r.pan) || r.PAN == r.pan {
		t.Fatalf("record %+v", r)
	}
	if w, _ := apRec(nse, "BH1060"); w.State != APWaits || !strings.Contains(w.Notes[0], "being opened") {
		t.Fatalf("a client whose UCC is still to be uploaded %+v", w)
	}
	// The file: a header and one A row per mapped client, PAN in full.
	name, b, err := o.APMappingFile(ctx, T, "priya.nair", week, "NSE")
	if err != nil || name != "90311_NSE_AP_CLIENTS_18102026.csv" {
		t.Fatalf("file %s %v", name, err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\r\n")
	c, _ := o.Client(ctx, T, "BH1006")
	if len(lines) != 13 || lines[0] != "AP Registration Number,Client Code,PAN,Client Name,Action,Date" || !strings.Contains(string(b), "AP0012345,BH1006,"+c.PAN+","+c.Name+",A,12-10-2026") {
		t.Fatalf("file:\n%s", b)
	}
	// It reads back against its own layout.
	lib, _ := XFiles()
	s, _ := lib.Get("nse-ap-client-mapping")
	if tb, err := s.Read(name, b); err != nil || len(tb.Issues) != 0 || len(tb.Rows) != 12 {
		t.Fatalf("read back %v %v", tb, err)
	}
	if _, _, err := o.APMappingFile(ctx, T, "meera.iyer", week, "NSE"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("branch staff made the file: %v", err)
	}
	// The periodic task: due on the 2nd trading day of the next week.
	at("2026-10-20", "10:00")
	ps, _ := o.Periodic(ctx, T, "2026-10-20")
	var task PeriodicStatus
	for _, p := range ps {
		if p.ID == "ap_mapping" {
			task = p
		}
	}
	if task.Period != week || task.DueOn != v.Due || task.Status != "Due" {
		t.Fatalf("task %+v", task)
	}
	run, err := o.RunPeriodic(ctx, T, SystemActor, "ap_mapping", "", "2026-10-20")
	if err != nil || run.Status != "Waiting for upload" || len(run.Files) != 3 || !strings.Contains(run.Summary, "NSE (12 rows)") {
		t.Fatalf("run %+v %v", run, err)
	}
	ws, _ := o.WorkItems(ctx, T, "")
	queued := 0
	for _, w := range ws {
		if strings.HasPrefix(w.Key, "periodic:apmap:"+week+":") && w.Status == "Open" && w.Role == RoleMaker {
			queued++
		}
	}
	if queued != 3 {
		t.Fatalf("%d uploads queued", queued)
	}
	// Recording the uploads.
	if _, err := o.MarkAPMappingUploaded(ctx, T, "priya.nair", APUploadReq{Week: week, Exchange: "NSE", Date: "2026-10-20"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no acknowledgement: %v", err)
	}
	if _, err := o.MarkAPMappingUploaded(ctx, T, "priya.nair", APUploadReq{Week: week, Exchange: "NSE", Date: "2026-10-17", Ack: "X"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("uploaded before the week ended: %v", err)
	}
	v, err = o.MarkAPMappingUploaded(ctx, T, "priya.nair", APUploadReq{Week: week, Exchange: "NSE", Date: "2026-10-20", Ack: "ENIT-ACK-0001"})
	if err != nil {
		t.Fatal(err)
	}
	nse = apEx(t, v, "NSE")
	if nse.Status != "Uploaded" || len(nse.Uploads) != 1 || nse.Uploads[0].Rows != 12 || nse.Uploads[0].FileID == "" || nse.Uploads[0].Late || len(nse.Records) != 1 || v.Status == "Done" {
		t.Fatalf("after the NSE upload %+v", nse)
	}
	for _, x := range []string{"BSE", "MCX"} {
		if _, err := o.MarkAPMappingUploaded(ctx, T, "priya.nair", APUploadReq{Week: week, Exchange: x, Date: "2026-10-20", Ack: x + "-ACK"}); err != nil {
			t.Fatal(err)
		}
	}
	ps, _ = o.Periodic(ctx, T, "2026-10-20")
	for _, p := range ps {
		if p.ID == "ap_mapping" && p.Status != "Done" {
			t.Fatalf("task after the uploads %+v", p)
		}
	}
	if _, err := o.MarkAPMappingUploaded(ctx, T, "priya.nair", APUploadReq{Week: week, Exchange: "NSE", Date: "2026-10-20", Ack: "again"}); !errors.Is(err, ErrState) {
		t.Fatalf("uploaded with nothing to report: %v", err)
	}
	f, err := o.PeriodicFileData(ctx, T, "priya.nair", nse.Uploads[0].FileID)
	if err != nil || !strings.Contains(string(f.Content), "BH1006") {
		t.Fatalf("kept file %v", err)
	}

	// The next week: a re-mapping is a D row and an A row; an unmapping a
	// D row; the client still being opened goes in once it is active.
	at("2026-10-21", "11:00")
	if _, err := o.AssignClient(ctx, T, "farhan.khan", "BH1006", "Mumbai", "AP002", "RM01", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := o.AssignClient(ctx, T, "farhan.khan", "BH1018", "Bengaluru", "", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := docs.Update(ctx, o.Docs, T, cClient, "BH1060", "test", func(c *Client) error { c.Status = "Active"; return nil }); err != nil {
		t.Fatal(err)
	}
	v, _ = o.APMappingReport(ctx, T, co, "2026-10-25", "2026-10-26")
	nse = apEx(t, v, "NSE")
	re, _ := apRec(nse, "BH1006")
	un, _ := apRec(nse, "BH1018")
	nw, _ := apRec(nse, "BH1060")
	if re.Kind != APRemapped || re.FromReg != "AP0012345" || re.ToReg != "AP0067890" || un.Kind != APUnmapped || un.FromReg != "AP0067890" || nw.Kind != APMapped || nw.State != APInFile || len(nse.Records) != 3 {
		t.Fatalf("second week %+v", nse.Records)
	}
	_, b, _ = o.APMappingFile(ctx, T, "priya.nair", "2026-10-25", "NSE")
	for _, want := range []string{"AP0012345,BH1006,", ",D,21-10-2026", "AP0067890,BH1006,", "AP0067890,BH1018,"} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("second file lacks %q:\n%s", want, b)
		}
	}
	// A client's history: who changed it, when, from and to.
	h, err := o.ClientAPHistory(ctx, T, co, "BH1006")
	if err != nil || len(h) != 2 || h[1].From != "AP001" || h[1].To != "AP002" || h[1].By != "farhan.khan" || h[1].Date != "2026-10-21" {
		t.Fatalf("history %+v %v", h, err)
	}
}

// The checks against the exchange's UCC and the AP's registration, and the
// AP's own mobile on a client's record (NSE/COMP/63628).
func TestAPMappingChecks(t *testing.T) {
	o, at := apReady(t)
	ctx := t.Context()
	put := func(x, ucc string, r map[string]any) {
		r["exchange"], r["ucc"] = x, ucc
		if err := docs.Upsert(ctx, o.Docs, T, cUCCReg, x+"|"+ucc, r, "test"); err != nil {
			t.Fatal(err)
		}
	}
	put("NSE", "BH1009", map[string]any{"status": nseUCIRejected, "reason": "Made-up reason"})
	put("NSE", "BH1012", map[string]any{"status": nseUCISent})
	put("NSE", "BH1030", map[string]any{"status": nseUCIAccepted, "accepted": true, "pan_status": "I"})
	put("NSE", "BH1036", map[string]any{"status": nseUCIAccepted, "accepted": true, "pan_status": "A"})
	// An AP with no registration number, and AP001's own mobile on a client.
	if _, err := o.SavePartner(ctx, T, "admin", Partner{Code: "AP003", Name: "Made-up AP", Kind: "Authorised person", Branch: "Mumbai", Active: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.AssignClient(ctx, T, "farhan.khan", "BH1054", "Mumbai", "AP003", "", "", ""); err != nil {
		t.Fatal(err)
	}
	c, _ := o.Client(ctx, T, "BH1045")
	ap, _, _ := docs.Get[Partner](ctx, o.Docs, T, cPartner, "AP001")
	ap.Mobile, ap.ExchangeRegs = "+91 "+c.Mobile, map[string]string{"bse": "BSEAP-0042"}
	if _, err := o.SavePartner(ctx, T, "admin", ap); err != nil {
		t.Fatal(err)
	}
	if saved, _, _ := docs.Get[Partner](ctx, o.Docs, T, cPartner, "AP001"); saved.Mobile != c.Mobile || saved.ExchangeRegs["BSE"] != "BSEAP-0042" {
		t.Fatalf("AP saved %+v", saved)
	}
	if _, err := o.SavePartner(ctx, T, "admin", Partner{Code: "AP004", Name: "x", Kind: "Authorised person", Mobile: "12345", Active: true}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad mobile saved: %v", err)
	}
	v, err := o.APMappingReport(ctx, T, "priya.nair", "2026-10-18", "2026-10-19")
	if err != nil {
		t.Fatal(err)
	}
	nse, bse := apEx(t, v, "NSE"), apEx(t, v, "BSE")
	state := func(ex APMapExchange, ucc string) APMapRecord { r, _ := apRec(ex, ucc); return r }
	if r := state(nse, "BH1009"); r.State != APToFix || !strings.Contains(r.Notes[0], "rejected: Made-up reason") {
		t.Fatalf("rejected UCC %+v", r)
	}
	if r := state(nse, "BH1012"); r.State != APWaits || !strings.Contains(r.Notes[0], "not yet accepted") {
		t.Fatalf("UCC sent %+v", r)
	}
	if r := state(nse, "BH1030"); r.State != APToFix || !strings.Contains(r.Notes[0], "could not verify the client's PAN") {
		t.Fatalf("PAN not verified %+v", r)
	}
	if r := state(nse, "BH1036"); r.State != APInFile {
		t.Fatalf("accepted UCC %+v", r)
	}
	// NSE answers for some clients: one with no answer is put in with a warning.
	if r := state(nse, "BH1022"); r.State != APInFileCheck || !strings.Contains(r.Notes[0], "no answer from NSE") || !nse.UsesUCC {
		t.Fatalf("no NSE answer %+v", r)
	}
	if r := state(nse, "BH1054"); r.State != APToFix || !strings.Contains(strings.Join(r.Notes, " "), "AP003 has no NSE AP registration number") {
		t.Fatalf("AP without a number %+v", r)
	}
	if r := state(bse, "BH1006"); r.ToReg != "BSEAP-0042" || r.State != APInFile {
		t.Fatalf("BSE's own AP number %+v", r)
	}
	if r := state(bse, "BH1045"); r.State != APInFileCheck || !strings.Contains(r.Notes[0], "AP001's own mobile") {
		t.Fatalf("the AP's own mobile on its client %+v", r)
	}
	hits, err := o.APContactCheck(ctx, T, "priya.nair")
	if err != nil || len(hits) != 1 || hits[0].UCC != "BH1045" || hits[0].Field != "mobile" || !hits[0].OwnClient || len(v.Contacts) != 1 {
		t.Fatalf("contact check %+v %v", hits, err)
	}
	if _, err := o.APContactCheck(ctx, T, "sunrise.ap"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("an AP ran the check: %v", err)
	}
	// The AP's own account (its own PAN) is left out.
	if _, err := docs.Update(ctx, o.Docs, T, cClient, "BH1045", "test", func(x *Client) error { x.PAN = ap.PAN; return nil }); err != nil {
		t.Fatal(err)
	}
	if hits, _ := o.APContactCheck(ctx, T, "priya.nair"); len(hits) != 0 {
		t.Fatalf("the AP's own account flagged %+v", hits)
	}
	// Records to fix hold the week until fixed or the rest is uploaded.
	at("2026-10-21", "10:00")
	done, note := o.apWeekDone(ctx, T, "2026-10-18", "2026-10-21")
	if done || !strings.Contains(note, "NSE:") {
		t.Fatalf("done with records pending: %s", note)
	}
}

// A mapping on record when the register starts is an existing mapping,
// never in a weekly file; a change made outside the recorded ways is found
// on check; a new client of an AP waits until its account is open.
func TestAPMappingBaselineAndFound(t *testing.T) {
	o, ctx := setup(t)
	move := siClock(o, siAt("2026-10-12", "10:00"))
	if _, err := o.SavePartner(ctx, T, "admin", Partner{Code: "AP001", Name: "Made-up AP", Kind: "Authorised person", Branch: "Mumbai", RegNo: "AP0000001", Active: true}); err != nil {
		t.Fatal(err)
	}
	// Mapped before the register existed (as a migration leaves it).
	if _, err := docs.Update(ctx, o.Docs, T, cClient, "BH1001", "test", func(c *Client) error { c.Partner = "AP001"; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := o.APMappingReport(ctx, T, "meera.iyer", "", ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("branch staff saw the report: %v", err)
	}
	// Self-inspection reads only: the register has not started.
	move(siAt("2026-10-19", "23:00"))
	siWant(t, siOne(t, o, ctx, "ap_mapping_weekly", "2026-10-19"), SISkipped, "before the register started")
	move(siAt("2026-10-12", "10:00"))
	v, err := o.APMappingReport(ctx, T, "priya.nair", "2026-10-18", "2026-10-12")
	if err != nil || v.Started != "2026-10-12" || v.Existing != 1 || len(apEx(t, v, "NSE").Records) != 0 {
		t.Fatalf("baseline %+v %v", v, err)
	}
	h, _ := o.ClientAPHistory(ctx, T, "priya.nair", "BH1001")
	c1, _ := o.Client(ctx, T, "BH1001")
	if len(h) != 1 || h[0].Kind != APExisting || h[0].Date != c1.KYCDate || h[0].Via != APViaExisting {
		t.Fatalf("existing mapping %+v", h)
	}
	// Changed outside the recorded ways: found on check, dated the day found.
	if _, err := docs.Update(ctx, o.Docs, T, cClient, "BH1006", "test", func(c *Client) error { c.Partner = "AP001"; return nil }); err != nil {
		t.Fatal(err)
	}
	move(siAt("2026-10-14", "10:00"))
	v, _ = o.APMappingReport(ctx, T, "priya.nair", "2026-10-18", "2026-10-14")
	r, ok := apRec(apEx(t, v, "NSE"), "BH1006")
	if !ok || r.Via != APViaFound || r.ChangedOn != "2026-10-14" || r.By != SystemActor {
		t.Fatalf("found on check %+v", r)
	}
	// A new client of the AP, from its account opening.
	nc, err := o.CreateApplication(ctx, T, "priya.nair", Client{Name: "Made Up Person", Type: "Individual", PAN: "ZZZPZ9999Z", Mobile: "9000000001", Email: "made.up@example.com", City: "Mumbai", Segments: []string{"CM"}, Risk: "Low", Income: "1-5 Lakh", RAS: "Quarterly",
		Bank: BankAccount{Bank: "Made-up Bank", IFSC: "HDFC0000001", Account: "123456789012"}, Demat: Demat{Depository: "CDSL", ID: "1234567800000001"}, RiskDisclosure: true, Partner: "AP001", FATCA: IndiaOnlyFATCA("Mumbai", "")})
	if err != nil {
		t.Fatal(err)
	}
	v, _ = o.APMappingReport(ctx, T, "priya.nair", "2026-10-18", "2026-10-14")
	r, ok = apRec(apEx(t, v, "NSE"), nc.UCC)
	if !ok || !r.NewClient || r.Via != APViaOpening || r.State != APWaits {
		t.Fatalf("new client of an AP %+v", r)
	}
	// Taking the present mappings as already reported to an exchange.
	if _, err := o.TakeAPMappingAsReported(ctx, T, "priya.nair", "NSE", "reported before"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a maker took the mappings as reported: %v", err)
	}
	if _, err := o.TakeAPMappingAsReported(ctx, T, "vikram.shah", "NSE", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("taken with no reason: %v", err)
	}
	if n, err := o.TakeAPMappingAsReported(ctx, T, "vikram.shah", "NSE", "Reported from the previous system"); err != nil || n == 0 {
		t.Fatalf("taken as reported %d %v", n, err)
	}
	v, _ = o.APMappingReport(ctx, T, "priya.nair", "2026-10-18", "2026-10-14")
	if n := len(apEx(t, v, "NSE").Records); n != 0 {
		t.Fatalf("%d records after taking the mappings as reported", n)
	}
}

// Each AP's client count at each exchange against Ledgerline's, monthly.
func TestAPCountRecon(t *testing.T) {
	o, at := apReady(t)
	ctx := t.Context()
	if _, err := o.SaveAPCountRecon(ctx, T, "priya.nair", "2026-10", "NSE", nil, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("reconciled before the month ended: %v", err)
	}
	at("2026-11-03", "10:00")
	v, err := o.APCountReconFor(ctx, T, "priya.nair", "")
	if err != nil || v.Month != "2026-10" || v.Due != "2026-11-10" || v.Status != "Due" || len(v.Exchanges) != 3 {
		t.Fatalf("view %+v %v", v, err)
	}
	var nse APCountExchange
	for _, ex := range v.Exchanges {
		if ex.Exchange == "NSE" {
			nse = ex
		}
	}
	counts := map[string]int{}
	for _, l := range nse.Lines {
		counts[l.Partner] = l.Ledgerline
	}
	if counts["AP001"] != 8 || counts["AP002"] != 5 {
		t.Fatalf("Ledgerline's counts %v", counts)
	}
	n8, n4 := 8, 4
	if _, err := o.SaveAPCountRecon(ctx, T, "priya.nair", "2026-10", "NSE", []APCountInput{{Partner: "AP001", Count: &n8}}, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an AP left out: %v", err)
	}
	in := []APCountInput{{Partner: "AP001", Count: &n8}, {Partner: "AP002", Count: &n4, Clients: "BH1018, BH1020\nBH1022 BH9999"}}
	if _, err := o.SaveAPCountRecon(ctx, T, "priya.nair", "2026-10", "NSE", in, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a difference with no note: %v", err)
	}
	v, err = o.SaveAPCountRecon(ctx, T, "priya.nair", "2026-10", "NSE", in, "Uploaded the two missing clients on ENIT")
	if err != nil {
		t.Fatal(err)
	}
	for _, ex := range v.Exchanges {
		if ex.Exchange != "NSE" {
			continue
		}
		r := ex.Recorded
		if r == nil || r.Matched || len(r.Lines) != 2 {
			t.Fatalf("recorded %+v", r)
		}
		l := r.Lines[1]
		if l.Partner != "AP002" || l.Diff != -1 || strings.Join(l.Missing, ",") != "BH1028,BH1034" || strings.Join(l.Extra, ",") != "BH9999" {
			t.Fatalf("AP002 %+v", l)
		}
	}
	if done, _ := o.apCountDone(ctx, T, "2026-10"); done {
		t.Fatal("done with BSE and MCX to reconcile")
	}
	run, err := o.RunPeriodic(ctx, T, SystemActor, "ap_client_count", "2026-10", "2026-11-03")
	if err != nil || run.Status != "Waiting for the counts" || !strings.Contains(run.Summary, "BSE, MCX") {
		t.Fatalf("run %+v %v", run, err)
	}
}

// The weekly self-inspection test and the AP contacts test.
func TestAPSelfInspection(t *testing.T) {
	o, at := apReady(t)
	ctx := t.Context()
	at("2026-10-21", "23:00")
	s := siOne(t, o, ctx, "ap_mapping_weekly", "2026-10-21")
	siWant(t, s, SIFail, "the week of 12 Oct not uploaded")
	if s.found != 12+12+2 || !strings.Contains(s.examples[0], "not in an upload") || strings.Contains(strings.Join(s.examples, " "), "BH10") {
		t.Fatalf("%d found: %v", s.found, s.examples)
	}
	for _, x := range []string{"NSE", "BSE", "MCX"} {
		if _, err := o.MarkAPMappingUploaded(ctx, T, "priya.nair", APUploadReq{Week: "2026-10-18", Exchange: x, Date: "2026-10-20", Ack: x + "-1"}); err != nil {
			t.Fatal(err)
		}
	}
	siWant(t, siOne(t, o, ctx, "ap_mapping_weekly", "2026-10-21"), SIPass, "after the uploads")
	siWant(t, siOne(t, o, ctx, "ap_contacts", "2026-10-21"), SISkipped, "no AP's own contact details recorded")
	ap, _, _ := docs.Get[Partner](ctx, o.Docs, T, cPartner, "AP002")
	c, _ := o.Client(ctx, T, "BH1006")
	ap.Email = strings.ToUpper(c.Email)
	if _, err := o.SavePartner(ctx, T, "admin", ap); err != nil {
		t.Fatal(err)
	}
	s = siOne(t, o, ctx, "ap_contacts", "2026-10-21")
	siWant(t, s, SIFail, "AP002's own email on a client")
	if !strings.Contains(s.examples[0], "AP AP002 · its own email") || strings.Contains(s.examples[0], "BH1006") {
		t.Fatalf("example %v", s.examples)
	}
}
