package ops

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/docs"
)

// All names, PANs, references and dates here are made up.

// sanUNXML is a made-up UN consolidated list in the UN's XML layout.
func sanUNXML(withBektash bool) string {
	b := `<INDIVIDUAL><DATAID>900002</DATAID><VERSIONNUM>1</VERSIONNUM><FIRST_NAME>HAROUN</FIRST_NAME><SECOND_NAME>BEKTASH</SECOND_NAME><UN_LIST_TYPE>Al-Qaida</UN_LIST_TYPE><REFERENCE_NUMBER>QDi.902</REFERENCE_NUMBER><LISTED_ON>2024-06-01</LISTED_ON><INDIVIDUAL_DATE_OF_BIRTH><TYPE_OF_DATE>APPROXIMATELY</TYPE_OF_DATE><YEAR>1960</YEAR></INDIVIDUAL_DATE_OF_BIRTH></INDIVIDUAL>`
	if !withBektash {
		b = ""
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<CONSOLIDATED_LIST xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" dateGenerated="2026-10-15T08:00:00.000Z">
<INDIVIDUALS>
<INDIVIDUAL><DATAID>900001</DATAID><VERSIONNUM>1</VERSIONNUM><FIRST_NAME>ZARQON</FIRST_NAME><SECOND_NAME>AL-VELHAADI</SECOND_NAME><UN_LIST_TYPE>DPRK</UN_LIST_TYPE><REFERENCE_NUMBER>KPi.901</REFERENCE_NUMBER><LISTED_ON>2025-03-04</LISTED_ON>
<NATIONALITY><VALUE>Democratic People's Republic of Korea</VALUE></NATIONALITY>
<INDIVIDUAL_ALIAS><QUALITY>Good</QUALITY><ALIAS_NAME>Zarkon Velhadi</ALIAS_NAME></INDIVIDUAL_ALIAS>
<INDIVIDUAL_DATE_OF_BIRTH><TYPE_OF_DATE>EXACT</TYPE_OF_DATE><DATE>1971-05-09</DATE></INDIVIDUAL_DATE_OF_BIRTH></INDIVIDUAL>
` + b + `
</INDIVIDUALS>
<ENTITIES><ENTITY><DATAID>900010</DATAID><FIRST_NAME>KORVEX TRADING COMPANY</FIRST_NAME><UN_LIST_TYPE>Al-Qaida</UN_LIST_TYPE><REFERENCE_NUMBER>QDe.903</REFERENCE_NUMBER><LISTED_ON>2024-01-02</LISTED_ON><ENTITY_ALIAS><QUALITY>a.k.a.</QUALITY><ALIAS_NAME>Korvex Traders</ALIAS_NAME></ENTITY_ALIAS></ENTITY></ENTITIES>
</CONSOLIDATED_LIST>`
}

func TestSanctionsNameMatching(t *testing.T) {
	score := func(a, b string) int { return nameScore(sanTokens(a), sanTokens(b)) }
	for _, p := range [][2]string{
		{"Zarqun Velhady", "Zarqon Al-Velhaadi"}, // transliteration, a particle
		{"Mohd Yusuf Tarvesh", "Muhammad Yousuf Tarvesh"},
		{"Harun Bektas", "Haroun Bektash"},
		{"Abdulrahman Tavesk", "Abdul Rahman Tavesk"}, // written as one word
		{"Usama Brennik", "Osama Brennik"},
		{"Velhadi Zarqon", "Zarqon Velhadi"}, // word order
		{"Ibrahim Velhadi", "Ebrahim Velhaadi"},
		{"Zárqon Velhádi", "Zarqon Velhadi"}, // accents
	} {
		if s := score(p[0], p[1]); s < 90 {
			t.Errorf("%s / %s: %d, want a match", p[0], p[1], s)
		}
	}
	for _, p := range [][2]string{
		{"Rahul Sharma", "Rahul Verma"},
		{"Ramesh Kumar", "Suresh Kumar"},
		{"Mahmood Ali", "Mohammed Ali"},
		{"Priya Nair", "Zarqon Velhadi"},
		{"Ali", "Ali Hassan Khan"},
	} {
		if s := score(p[0], p[1]); s >= DefaultSanctionsConfig().ReviewAt {
			t.Errorf("%s / %s: %d, want no match", p[0], p[1], s)
		}
	}
	if translitKey("MOHAMMED") != translitKey("MUHAMMAD") || translitKey("MAHMOOD") == translitKey("MOHAMMED") || translitKey("YOUSEF") != translitKey("YUSUF") {
		t.Error("transliteration keys")
	}
	if nationCode("Democratic People's Republic of Korea") != "KP" || nationCode("india") != "IN" || nationCode("Atlantis") != "" {
		t.Error("nationality codes")
	}
	v, es, err := parseUNList([]byte(sanUNXML(true)))
	if err != nil || v != "2026-10-15" || len(es) != 3 {
		t.Fatalf("UN list: %s %d %v", v, len(es), err)
	}
	z := es[0]
	if z.Ref != "KPi.901" || z.Regime != "DPRK" || z.Kind != "Individual" || len(z.Aliases) != 1 || z.DOB[0] != "1971-05-09" || statuteOf(z) != "WMD" || statuteOf(es[1]) != "UAPA" || es[2].Kind != "Entity" {
		t.Fatalf("UN entries %+v", es)
	}
	if _, _, err := parseUNList([]byte("<OTHER/>")); err == nil {
		t.Fatal("not the UN list")
	}
	// Supporting evidence: a different date of birth takes the score down.
	ix := buildSanIndex(es, D, isSanctionsList)
	cfg := DefaultSanctionsConfig()
	if h := ix.match(screenPerson{Name: "Zarqun Velhady", DOB: "1971-05-09", Nation: "KP"}, cfg); len(h) != 1 || h[0].score != 100 {
		t.Fatalf("with evidence %+v", h)
	}
	if h := ix.match(screenPerson{Name: "Zarqun Velhady", DOB: "1994-01-01", Nation: "IN"}, cfg); len(h) != 0 {
		t.Fatalf("different birth and nationality %+v", h)
	}
	if h := ix.match(screenPerson{Name: "Anyone", PAN: "ZZZPQ0000Q"}, cfg); len(h) != 0 {
		t.Fatal("no PAN on the list")
	}
}

// sanClock sets the time. The sample day was loaded on the real clock, so
// its list load and screening are moved before the test's time.
func sanClock(o *Ops, at time.Time) {
	ctx := context.Background()
	old := "2026-01-01T09:00:00+05:30"
	vs, _ := docs.List[SanctionsListVersion](ctx, o.Docs, T, cSanctionsList, "")
	for _, v := range vs {
		if v.LoadedAt > at.Format(time.RFC3339) {
			v.LoadedAt = old
			_ = docs.Upsert(ctx, o.Docs, T, cSanctionsList, v.ID, v, "test")
		}
	}
	rs, _ := docs.List[SanctionsRun](ctx, o.Docs, T, cSanctionsRun, "")
	for _, r := range rs {
		if r.At > at.Format(time.RFC3339) {
			r.At = old
			_ = docs.Upsert(ctx, o.Docs, T, cSanctionsRun, r.ID, r, "test")
		}
	}
	o.Now = func() time.Time { return at }
}

func sanCase(t *testing.T, o *Ops, ctx context.Context, ucc, role string) SanctionsCase {
	t.Helper()
	cs, _ := docs.List[SanctionsCase](ctx, o.Docs, T, cSanctionsCase, "")
	for _, k := range cs {
		if k.UCC == ucc && k.Role == role {
			return k
		}
	}
	t.Fatalf("no %s case for %s (%d cases)", role, ucc, len(cs))
	return SanctionsCase{}
}

func TestSanctionsCases(t *testing.T) {
	o, ctx := setup(t)
	now := time.Date(2026, 10, 16, 10, 0, 0, 0, ist)
	sanClock(o, now)
	rename := func(ucc, name, dob string, more func(c *Client)) {
		t.Helper()
		if _, err := docs.Update(ctx, o.Docs, T, cClient, ucc, "test", func(c *Client) error {
			c.Name, c.DOB = name, dob
			if more != nil {
				more(c)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	rename("BH1002", "Zarqun Velhady", "1971-05-09", nil)
	rename("BH1004", "Harun Bektas Merovan", "", nil)
	rename("BH1005", "Kiran Patil", "", func(c *Client) { c.Nominees = []Nominee{{Name: "Zarqon Velhadi", Relation: "Brother", Share: "100"}} })
	if _, err := o.LoadFile(ctx, T, "farhan.khan", "DEBARRED-LIST", "", "consolidated.xml", []byte(sanUNXML(true))); err != nil {
		t.Fatal(err)
	}
	v, err := o.Sanctions(ctx, T, "vikram.shah")
	if err != nil || len(v.Lists) != 1 || v.Lists[0].Version != "2026-10-15" || v.Lists[0].Entries != 3 || v.Entries[ListUNSC] < 3 || v.LastRun == nil {
		t.Fatalf("view %+v %v", v.Lists, err)
	}
	inForce := v.Entries[ListUNSC]
	if _, err := o.Sanctions(ctx, T, "farhan.khan"); !errors.Is(err, ErrForbidden) {
		t.Fatal("cases are for compliance only")
	}
	// The holder with a strong match is held at once; the weaker match
	// and the nominee wait for review.
	z := sanCase(t, o, ctx, "BH1002", RoleHolder)
	if z.Status != SCToVerify || !z.Held || z.Statute != "WMD" || z.Score < 94 || z.DueBy != now.Add(24*time.Hour).Format(time.RFC3339) {
		t.Fatalf("strong case %+v", z)
	}
	c, _ := o.Client(ctx, T, "BH1002")
	if !c.Blocked || c.Sanctions == nil || !strings.HasPrefix(c.BlockedReason, sanctionsReason) {
		t.Fatalf("not held %+v", c)
	}
	if w, err := o.Withdrawable(ctx, T, D, "BH1002"); err != nil || !w.Blocked {
		t.Fatalf("payout not held %+v %v", w, err)
	}
	if b := o.boFor(ctx, T, c, ""); !b.debitFrozen() {
		t.Fatal("demat debits not stopped")
	}
	if sanctionsStop(c) == nil {
		t.Fatal("transactions not stopped")
	}
	b := sanCase(t, o, ctx, "BH1004", RoleHolder)
	if b.Held || b.Score >= 94 || b.Statute != "UAPA" {
		t.Fatalf("weaker case %+v", b)
	}
	n := sanCase(t, o, ctx, "BH1005", RoleNominee)
	if n.Held {
		t.Fatal("a nominee's match does not hold the account")
	}
	if c, _ := o.Client(ctx, T, "BH1005"); c.Sanctions != nil {
		t.Fatal("nominee's account held")
	}
	siWant(t, siOne(t, o, ctx, "sanctions_screening", D), SIPass, "screened, cases within 24 hours")

	// Confirming: the register, the DP freeze request, the STR alert. The
	// client's demat account is at the firm's DP.
	if err := o.putBO(ctx, T, "test", BOAccount{UCC: "BH1002", BOID: c.Demat.ID, Depository: "CDSL", Status: "Active"}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.ActOnSanctionsCase(ctx, T, "farhan.khan", z.ID, SanctionsAction{Action: "confirm", Note: "x"}); !errors.Is(err, ErrForbidden) {
		t.Fatal("a checker confirmed")
	}
	if _, err := o.ActOnSanctionsCase(ctx, T, "vikram.shah", z.ID, SanctionsAction{Action: "confirm"}); err == nil {
		t.Fatal("confirmed without a note")
	}
	z, err = o.ActOnSanctionsCase(ctx, T, "vikram.shah", z.ID, SanctionsAction{Action: "confirm", Note: "Passport and date of birth match the list entry"})
	if err != nil {
		t.Fatal(err)
	}
	if z.Status != SCConfirmed || len(z.Reports) != 3 || z.Reports[0].Recipient != RcptWMDCNO || z.Reports[0].Address == "" || z.Alert == "" || len(z.Freezes) == 0 {
		t.Fatalf("confirmed %+v", z)
	}
	dr, _, err := docs.Get[DPRequest](ctx, o.Docs, T, cDPReq, z.Freezes[0])
	if err != nil || dr.Kind != "Freeze" || dr.Detail["type"] != "Debit" || dr.Status != "Pending" || !strings.Contains(dr.Detail["reason"], z.ID) {
		t.Fatalf("freeze request %+v %v", dr, err)
	}
	if c, _ := o.Client(ctx, T, "BH1002"); c.Sanctions == nil || !c.Sanctions.Confirmed {
		t.Fatal("confirmed hold")
	}
	if du := dutyOf(t, o, "CC-T-SANCTIONS-MATCH", D); len(du.Events) != 1 || du.Events[0].Due != "2026-10-17" || du.Events[0].DueAt != "10:00" {
		t.Fatalf("calendar event %+v", du.Events)
	}
	if _, err := o.ActOnSanctionsCase(ctx, T, "vikram.shah", z.ID, SanctionsAction{Action: "close", Note: "done"}); !errors.Is(err, ErrState) {
		t.Fatalf("closed before the reports: %v", err)
	}
	for _, r := range z.Reports {
		if _, err := o.ActOnSanctionsCase(ctx, T, "vikram.shah", z.ID, SanctionsAction{Action: "report", Recipient: r.Recipient, Date: D}); err == nil {
			t.Fatal("report without a reference")
		}
		if z, err = o.ActOnSanctionsCase(ctx, T, "vikram.shah", z.ID, SanctionsAction{Action: "report", Recipient: r.Recipient, Date: D, Ref: "Email of 16 Oct 2026 10:40"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := o.ActOnSanctionsCase(ctx, T, "vikram.shah", z.ID, SanctionsAction{Action: "close", Note: "reported"}); !errors.Is(err, ErrState) || !strings.Contains(err.Error(), "STR") {
		t.Fatalf("closed before the STR decision: %v", err)
	}
	if _, err := o.ActOnAlert(ctx, T, "vikram.shah", z.Alert, "str", "Designated person under the WMD Act; all transactions in the account"); err != nil {
		t.Fatal(err)
	}
	if z, err = o.ActOnSanctionsCase(ctx, T, "vikram.shah", z.ID, SanctionsAction{Action: "close", Note: "reported"}); err != nil || z.Status != SCReported {
		t.Fatalf("close %+v %v", z, err)
	}
	if c, _ := o.Client(ctx, T, "BH1002"); !c.Blocked || c.Sanctions == nil {
		t.Fatal("a reported case keeps the hold")
	}

	// A false positive: proposed by one compliance officer, approved by
	// another person.
	if _, err := o.ActOnSanctionsCase(ctx, T, "vikram.shah", b.ID, SanctionsAction{Action: "clear"}); err == nil {
		t.Fatal("cleared without a reason")
	}
	if b, err = o.ActOnSanctionsCase(ctx, T, "vikram.shah", b.ID, SanctionsAction{Action: "clear", Note: "Born 1988 per PAN card; listed person born 1960"}); err != nil || b.Status != SCClearing {
		t.Fatalf("clear %+v %v", b, err)
	}
	if _, err := o.ActOnSanctionsCase(ctx, T, "vikram.shah", b.ID, SanctionsAction{Action: "approve_clear"}); !errors.Is(err, ErrForbidden) {
		t.Fatal("the same person approved")
	}
	if b, err = o.ActOnSanctionsCase(ctx, T, "admin", b.ID, SanctionsAction{Action: "approve_clear"}); err != nil || b.Status != SCFalse {
		t.Fatalf("approve %+v %v", b, err)
	}
	run, err := o.RunSanctionsScreening(ctx, T, "vikram.shah")
	if err != nil || run.New != 0 || run.Hits < 3 {
		t.Fatalf("a cleared match is not raised again: %+v %v", run, err)
	}

	// The nominee's case is still open after 24 hours: the self-inspection
	// fails and the tick chases it; the next morning all clients are
	// screened again.
	sanClock(o, now.Add(25*time.Hour))
	siWant(t, siOne(t, o, ctx, "sanctions_screening", "2026-10-17"), SIFail, "a case past 24 hours")
	o.sanctionsTick(ctx, T, o.now())
	if r, ok := o.lastFullRun(ctx, T); !ok || r.Trigger != "scheduled" || r.At[:10] != "2026-10-17" {
		t.Fatalf("scheduled run %+v", r)
	}
	if w, _, err := docs.Get[WorkItem](ctx, o.Docs, T, cWorkItem, "W-sanctions-"+n.ID+"-late"); err != nil || w.Status != "Open" {
		t.Fatalf("overdue chase %+v %v", w, err)
	}

	// Unfreezing: the application forwarded within two working days, then
	// the order lifts the hold.
	if z, err = o.ActOnSanctionsCase(ctx, T, "vikram.shah", z.ID, SanctionsAction{Action: "unfreeze_app", Date: "2026-10-17"}); err != nil || z.UnfreezeBy != "2026-10-20" {
		t.Fatalf("unfreeze application %+v %v", z, err)
	}
	if z, err = o.ActOnSanctionsCase(ctx, T, "vikram.shah", z.ID, SanctionsAction{Action: "unfreeze_forwarded", Date: "2026-10-17", Ref: "Letter LL/CNO/1"}); err != nil {
		t.Fatal(err)
	}
	if z, err = o.ActOnSanctionsCase(ctx, T, "vikram.shah", z.ID, SanctionsAction{Action: "unfreeze", Ref: "CNO order 12 of 17 Oct 2026", Note: "Not the designated person (inadvertent freezing)"}); err != nil || z.Status != SCUnfrozen {
		t.Fatalf("unfreeze %+v %v", z, err)
	}
	if c, _ := o.Client(ctx, T, "BH1002"); c.Sanctions != nil || c.Blocked {
		t.Fatalf("hold not lifted %+v", c)
	}

	// A new list without an entry delists it.
	l, err := o.LoadSanctionsList(ctx, T, "vikram.shah", "", "", "consolidated2.xml", []byte(sanUNXML(false)))
	if err != nil || l.Version.Delisted != 1 || l.Version.Entries != 2 {
		t.Fatalf("delisting %+v %v", l.Version, err)
	}
	v, _ = o.Sanctions(ctx, T, "vikram.shah")
	if v.Entries[ListUNSC] != inForce-1 {
		t.Fatalf("in force %+v", v.Entries)
	}
}

func TestSanctionsOnboardingDefaultersFATF(t *testing.T) {
	o, ctx := setup(t)
	sanClock(o, time.Date(2026, 10, 16, 11, 0, 0, 0, ist))
	if _, err := o.LoadSanctionsList(ctx, T, "vikram.shah", ListUAPA, "MHA 2026-10-10", "uapa.csv", []byte("Name,Aliases,DOB,PAN,Ref,Type\nTarvesh Okonmar,Tarvesh Okonmaar;T Okonmar,1980,ZZZPT1234Z,UAPA-IV-901,Individual\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := o.LoadSanctionsList(ctx, T, "vikram.shah", ListUAPA, "", "bad.csv", []byte("Name,DOB\nSomeone,May 1980\n")); err == nil {
		t.Fatal("a bad date of birth loaded")
	}
	if _, err := o.LoadSanctionsList(ctx, T, "vikram.shah", ListDefaulters, "", "defaulters.csv", []byte("Name,PAN,Ref,Regulator\nDelnor Pashkey,ZZZPD5555D,DEF-1,NSE\n")); err != nil {
		t.Fatal(err)
	}
	app := func(name, pan, dob string) (Client, error) {
		return o.CreateApplication(ctx, T, "priya.nair", Client{Name: name, Type: "Individual", PAN: pan, DOB: dob, Mobile: "9000000071", Email: "made.up@example.test", City: "Pune", Segments: []string{"CM"}, Income: "5-10 L", RAS: "Quarterly",
			Bank: BankAccount{Bank: "Made-up Bank", IFSC: "HDFC0000001", Account: "123456789012"}, Demat: Demat{Depository: "CDSL", ID: "1234567800000071"}, RiskDisclosure: true, FATCA: IndiaOnlyFATCA("Pune", "")})
	}
	// The listed PAN is refused, and the attempt is a case to report.
	if _, err := app("Some Name", "ZZZPT1234Z", "1980-03-03"); err == nil {
		t.Fatal("a designated person's PAN opened an account")
	}
	cs, _ := docs.List[SanctionsCase](ctx, o.Docs, T, cSanctionsCase, "")
	if len(cs) != 1 || cs[0].Role != RoleApplicant || cs[0].UCC != "" || cs[0].Score != 100 {
		t.Fatalf("applicant case %+v", cs)
	}
	// A name match with another PAN: the application is made but cannot go
	// further until Compliance decides.
	nc, err := app("Tarvesh Okonmaar", "ZZZPT7777Z", "1980-07-01")
	if err != nil {
		t.Fatal(err)
	}
	k := sanCase(t, o, ctx, nc.UCC, RoleHolder)
	if !k.Held || !nc.Blocked {
		t.Fatalf("held %+v %+v", k, nc)
	}
	if _, err := o.AdvanceStage(ctx, T, "priya.nair", nc.UCC); !errors.Is(err, ErrState) || !strings.Contains(err.Error(), k.ID) {
		t.Fatalf("advanced with an open case: %v", err)
	}
	// The exchanges' defaulting clients: a case, not a refusal.
	dc, err := app("Delnor Pashkey", "ZZZPD5555D", "1975-01-01")
	if err != nil {
		t.Fatal(err)
	}
	dk := sanCase(t, o, ctx, dc.UCC, RoleHolder)
	if dk.List != ListDefaulters || dk.Held || dk.Statute != "" {
		t.Fatalf("defaulter case %+v", dk)
	}
	if _, err := o.AdvanceStage(ctx, T, "priya.nair", dc.UCC); !errors.Is(err, ErrState) {
		t.Fatal("defaulter advanced before Compliance decided")
	}
	if _, err := o.ActOnSanctionsCase(ctx, T, "vikram.shah", dk.ID, SanctionsAction{Action: "clear", Note: "Dues to the other member settled; letter on file"}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.ActOnSanctionsCase(ctx, T, "admin", dk.ID, SanctionsAction{Action: "approve_clear"}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.AdvanceStage(ctx, T, "priya.nair", dc.UCC); err != nil {
		t.Fatal(err)
	}

	// FATF: a client of Iranian nationality is linked; EDD makes it High.
	if _, err := docs.Update(ctx, o.Docs, T, cClient, "BH1003", "test", func(c *Client) error {
		c.FATCA = IndiaOnlyFATCA("Tehran", "")
		c.FATCA.Nationality, c.FATCA.CountryOfBirth = "IR", "IR"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.RecordFATFStatement(ctx, T, "vikram.shah", FATFStatement{Plenary: "October 2026", Date: D, CallForAction: []string{"Atlantis"}}); err == nil {
		t.Fatal("unknown country")
	}
	st, err := o.RecordFATFStatement(ctx, T, "vikram.shah", FATFStatement{Plenary: "October 2026", Date: D, Ref: "CDSL communique (made-up)", CallForAction: []string{"Iran", "KP"}, Monitoring: []string{"Viet Nam"}})
	if err != nil || st.Linked < 1 || st.CallForAction[0] != "IR" {
		t.Fatalf("FATF %+v %v", st, err)
	}
	if r, ok := o.lastFullRun(ctx, T); !ok || !strings.Contains(r.Trigger, "FATF") {
		t.Fatalf("re-screen %+v", r)
	}
	if du := dutyOf(t, o, "CC-T-FATF", D); len(du.Events) != 1 {
		t.Fatalf("FATF event %+v", du.Events)
	}
	e, _, err := docs.Get[FATFEDD](ctx, o.Docs, T, cFATFEDD, st.ID+"|BH1003")
	if err != nil || e.Level != "Call for action" || e.Status != "To do" {
		t.Fatalf("EDD %+v %v", e, err)
	}
	if _, err := o.RecordFATFEDD(ctx, T, "vikram.shah", e.ID, "", false); err == nil {
		t.Fatal("EDD without a note")
	}
	if e, err = o.RecordFATFEDD(ctx, T, "vikram.shah", e.ID, "Source of funds: salary in India, bank statements seen", true); err != nil || e.Status != "Done" {
		t.Fatalf("EDD %+v %v", e, err)
	}
	if c, _ := o.Client(ctx, T, "BH1003"); c.Risk != "High" || !contains(c.CSC, "High-risk jurisdiction") {
		t.Fatalf("risk %+v", c)
	}

	// Settings.
	cfg := o.sanctionsConfig(ctx, T)
	if _, err := o.SaveSanctionsConfig(ctx, T, "farhan.khan", cfg); !errors.Is(err, ErrForbidden) {
		t.Fatal("a checker changed the settings")
	}
	cfg.HoldAt = cfg.ReviewAt - 1
	if _, err := o.SaveSanctionsConfig(ctx, T, "vikram.shah", cfg); err == nil {
		t.Fatal("hold below review")
	}
	cfg.ReviewAt, cfg.HoldAt, cfg.EveryDays, cfg.MaxAgeDays = 85, 95, 2, 10
	cfg.Contacts[1].Address = "made-up-state-nodal@example.test"
	if cfg, err = o.SaveSanctionsConfig(ctx, T, "vikram.shah", cfg); err != nil || cfg.ReviewAt != 85 || cfg.Contacts[1].Address == "" {
		t.Fatalf("settings %+v %v", cfg, err)
	}
}

// The DEBARRED-LIST file still loads debarments, and its UNSC rows go to
// sanctions screening.
func TestSanctionsFromDebarredList(t *testing.T) {
	o, ctx := setup(t)
	sanClock(o, time.Date(2026, 10, 16, 9, 0, 0, 0, ist))
	cs, _ := o.ClientMap(ctx, T)
	b := cs["BH1006"]
	csv := "PAN,Name,Regulator,OrderNo,OrderDate,Till,Aliases\n,Mr. " + b.Name + ",UNSC,QDi.999,2020-02-02,,\n"
	if _, err := o.LoadFile(ctx, T, "vikram.shah", "DEBARRED-LIST", "", "debarred.csv", []byte(csv)); err != nil {
		t.Fatal(err)
	}
	k := sanCase(t, o, ctx, "BH1006", RoleHolder)
	if k.List != ListUNSC || k.Score != 100 || !k.Held {
		t.Fatalf("case %+v", k)
	}
	if vs := o.latestListVersions(ctx, T); vs[ListUNSC].Version != "2026-10-16" {
		t.Fatalf("version %+v", vs)
	}
	if _, err := o.LoadFile(ctx, T, "vikram.shah", "DEBARRED-LIST", "", "bad.csv", []byte("PAN,Name,Regulator,OrderNo,OrderDate,Till,List\n,X Y,SEBI,O-1,2026-01-01,,Nowhere\n")); err == nil {
		t.Fatal("unknown list")
	}
}
