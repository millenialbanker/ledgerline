package ops

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/docs"
)

// mcxUCCClient is a complete resident individual with the commodity segment
// (made-up data).
func mcxUCCClient(ucc, pan, mobile string, now time.Time) Client {
	return Client{UCC: ucc, Name: "Asha Ramesh Kulkarni", Type: "Individual", PAN: pan, DOB: "1988-04-12", Mobile: mobile, Email: strings.ToLower(ucc) + "@example.in",
		City: "Pune", State: "Maharashtra", Status: "UCC upload", Segments: []string{"CM", "COM"}, Income: "10-25 L", RAS: "Quarterly", Risk: "Medium",
		Bank: BankAccount{Bank: "HDFC Bank, Ltd", IFSC: "HDFC0001234", Account: "50100012345678", Branch: "FC Road"}, Demat: Demat{Depository: "CDSL", ID: "1208160012345678"},
		Nominee: "Opted out", KYCDate: now.AddDate(0, 0, -3).Format("2006-01-02"), IncomeProof: true, RiskDisclosure: true}
}

func mcxDetails(ucc string) UCCDetails {
	return UCCDetails{UCC: ucc, Address1: "Flat 4, Shanti Niwas", Address2: "FC Road", PIN: "411004", Gender: "Female", Guardian: "Ramesh Kulkarni",
		Marital: "Married", Occupation: "Private sector", BankType: "Savings"}
}

func mcxTestSources(details map[string]UCCDetails, withMaster bool) mcxUCCSources {
	s := mcxUCCSources{bse: bseUCCSources{apps: map[string]Application{}, mf: map[string]MFProfile{}, nri: map[string]NRIDetails{}},
		ucc: uccSources{details: details, apps: map[string]Application{}}, mobiles: map[string][]string{}, emails: map[string][]string{}}
	if withMaster {
		s.cities = &MCXStateCityMaster{Codes: map[string]string{mcxStateKey("MAHARASHTRA") + "|" + mcxPlace("Pune"): "1442", mcxStateKey("Maharashtra") + "|" + mcxPlace("Mumbai"): "1449"}}
	}
	return s
}

func TestMCXUCCMapping(t *testing.T) {
	now := time.Date(2026, 10, 30, 11, 0, 0, 0, ist)
	date := "2026-10-30"
	c := mcxUCCClient("MCX01", "ABCPK1234F", "9811100001", now)
	src := mcxTestSources(map[string]UCCDetails{"MCX01": mcxDetails("MCX01")}, true)
	r := mcxUCCMap(c, src, nil, date)
	if len(r.Problems) != 0 {
		t.Fatalf("problems %v", r.Problems)
	}
	want := map[string]string{mxCode: "MCX01", mxName: "Asha Ramesh Kulkarni", mxCategory: "01", mxPAN: "ABCPK1234F", mxGender: "F", mxGuardian: "Ramesh Kulkarni",
		mxMarital: "M", mxNationality: "1", mxNationalityOther: "", mxEmail: "mcx01@example.in", mxAddr1: "Flat 4 Shanti Niwas", mxAddr2: "FC Road", mxAddr3: "",
		mxCountry: "85", mxStateCity: "1442", mxPin: "411004", mxPermSame: "Y", mxPAddr1: "", mxPStateCity: "", mxMobile: "9811100001", mxDOB: "12041988", mxUID: "",
		mxBank1: "HDFC Bank Ltd", mxBranch1: "FC Road IFSC HDFC0001234", mxAcct1: "50100012345678", mxAcctType1: "10", mxDep1: "2", mxDepID1: "", mxBO1: "1208160012345678",
		mxOccupation: "02", mxOccDetail: "", mxIncome: "04", mxIncomeDate: "27102026", mxPEP: "0", mxIPV: "Y", mxAgreement: "27102026", mxStatus: "1", mxRemarks: "",
		mxCorporate: "N", mxRelMobile: "1", mxNameMobile: "", mxRelEmail: "1", mxNameEmail: "", mxFlag: "E", "CinLei": "", "ContactPerson1Name": ""}
	for k, x := range want {
		if r.Values[k] != x {
			t.Errorf("%s = %q, want %q", k, r.Values[k], x)
		}
	}
	// Every value is a field of the layout (the keys of mcx-ucc.json).
	lib, _ := XFiles()
	s, _ := lib.Get("mcx-ucc")
	keys := map[string]bool{}
	for _, col := range s.Records[1].Columns {
		keys[col.Key] = true
	}
	for k := range r.Values {
		if !keys[k] {
			t.Errorf("%s is not a field of the MCX UCC layout", k)
		}
	}
	if m := r.Masked(); m[mxAcct1] != "XXXXXXXXXX5678" || m[mxPAN] == "ABCPK1234F" {
		t.Errorf("masked %s %s", m[mxAcct1], m[mxPAN])
	}

	// NSDL: the DP ID and the client ID; PEP; occupation Others with details.
	x := c
	x.Demat = Demat{Depository: "NSDL", ID: "IN30021412345678"}
	x.CSC = []string{"Relative of a PEP"}
	d := mcxDetails("MCX01")
	d.Occupation, d.OccupationDetail = "Self employed", "Consultant"
	src.ucc.details["MCX01"] = d
	r = mcxUCCMap(x, src, nil, date)
	if r.Values[mxDep1] != "1" || r.Values[mxDepID1] != "IN300214" || r.Values[mxBO1] != "12345678" || r.Values[mxPEP] != "2" || r.Values[mxOccupation] != "99" || r.Values[mxOccDetail] != "Consultant" || len(r.Problems) != 0 {
		t.Errorf("nsdl %v %v", r.Values, r.Problems)
	}
	src.ucc.details["MCX01"] = mcxDetails("MCX01")

	// Non-individuals are left for MCX's portal, with no individual fields.
	for typ, w := range map[string]string{"Corporate": "body corporate", "HUF": "HUF", "NRI": "NRI"} {
		y := c
		y.Type = typ
		r := mcxUCCMap(y, src, nil, date)
		all := strings.Join(r.Problems, " | ")
		if !strings.Contains(all, w) || !strings.Contains(all, "MCX's UCC portal") || r.Values[mxGender] != "" || r.Values[mxMarital] != "" || r.Values[mxGuardian] != "" {
			t.Errorf("%s: %v %v", typ, r.Problems, r.Values[mxGender])
		}
	}

	// No master, or a city not in it: reported, never guessed.
	r = mcxUCCMap(c, mcxTestSources(map[string]UCCDetails{"MCX01": mcxDetails("MCX01")}, false), nil, date)
	if len(r.Problems) != 1 || !strings.Contains(r.Problems[0], "State-City code not found in MCX's master") || !strings.Contains(r.Problems[0], "load the latest master") || r.Values[mxStateCity] != "" {
		t.Errorf("no master %v", r.Problems)
	}
	y := c
	y.City = "Satara"
	r = mcxUCCMap(y, src, nil, date)
	if len(r.Problems) != 1 || !strings.Contains(r.Problems[0], "State-City code not found in MCX's master for SATARA, MAHARASHTRA") {
		t.Errorf("city not in master %v", r.Problems)
	}

	// Missing details, bank type, old income: each in words.
	y = c
	y.KYCDate = "2025-03-01"
	src2 := mcxTestSources(map[string]UCCDetails{}, true)
	src2.bse.mf["MCX01"] = MFProfile{Address: "12 MG Road, Camp", Pincode: "411001"}
	r = mcxUCCMap(y, src2, nil, date)
	all := strings.Join(r.Problems, " | ")
	for _, w := range []string{"gender is missing", "father's or spouse's name is missing", "marital status is missing", "occupation is missing", "bank account type", "more than 18 months ago"} {
		if !strings.Contains(all, w) {
			t.Errorf("missing problem %q in %s", w, all)
		}
	}
	if r.Values[mxAddr1] != "12 MG Road Camp" || r.Values[mxPin] != "411001" {
		t.Errorf("address from the MF profile %q %q", r.Values[mxAddr1], r.Values[mxPin])
	}

	// A mobile shared with another client needs to say whose it is.
	src.mobiles["9811100001"] = []string{"MCX01", "MCX09"}
	if r = mcxUCCMap(c, src, nil, date); len(r.Problems) != 1 || !strings.Contains(r.Problems[0], "also client MCX09's") {
		t.Errorf("shared mobile %v", r.Problems)
	}
	d = mcxDetails("MCX01")
	d.MobileRelation = "Spouse"
	src.ucc.details["MCX01"] = d
	if r = mcxUCCMap(c, src, nil, date); len(r.Problems) != 0 || r.Values[mxRelMobile] != "2" {
		t.Errorf("spouse's mobile %v %q", r.Problems, r.Values[mxRelMobile])
	}

	// Status: inactive with remarks when it changes; none when it does not.
	y = c
	y.Inactive, y.InactiveSince = true, "2026-10-01"
	r = mcxUCCMap(y, src, map[string]string{"status": "1"}, date)
	if r.Values[mxStatus] != "2" || !strings.HasPrefix(r.Values[mxRemarks], "No activity") || r.Summary["status"] != "2" {
		t.Errorf("inactive %q %q", r.Values[mxStatus], r.Values[mxRemarks])
	}
	if r = mcxUCCMap(y, src, map[string]string{"status": "2"}, date); r.Values[mxRemarks] != "" {
		t.Errorf("unchanged inactive remarks %q", r.Values[mxRemarks])
	}
	y.Inactive, y.ReactivatedOn = false, "2026-10-29"
	if r = mcxUCCMap(y, src, map[string]string{"status": "2"}, date); r.Values[mxStatus] != "1" || !strings.HasPrefix(r.Values[mxRemarks], "Reactivated on") {
		t.Errorf("reactivated %q %q", r.Values[mxStatus], r.Values[mxRemarks])
	}

	// Error codes in words.
	if e := MCXUCCError("15"); !strings.Contains(e, "015") || !strings.Contains(e, "client name") {
		t.Errorf("015: %s", e)
	}
	if e := MCXUCCError("299"); !strings.Contains(e, "contact person 20's e-mail") {
		t.Errorf("299: %s", e)
	}
	if e := MCXUCCError("128"); !strings.Contains(e, "contact person 1's e-mail") {
		t.Errorf("128: %s", e)
	}
}

func TestMCXStateCityMasterLenient(t *testing.T) {
	m, err := parseMCXStateCity([]byte("\xef\xbb\xbfSr No|STATE_NAME|CITY_NAME|STATE_CITY_CODE\r\n1|Maharashtra|Pune|1442\r\n2|Orissa|Cuttack|2101\r\n3|Delhi|New Delhi|0901\r\n"))
	if err != nil || m.Count != 3 || m.Columns[2] != "STATE_CITY_CODE" {
		t.Fatalf("%+v %v", m, err)
	}
	for _, x := range []struct{ state, city, code string }{{"MAHARASHTRA", "pune", "1442"}, {"Odisha", "Cuttack", "2101"}, {"NCT of Delhi", "New Delhi", "0901"}} {
		if c, ok := m.Lookup(x.state, x.city); !ok || c != x.code {
			t.Errorf("%s %s: %q %v", x.state, x.city, c, ok)
		}
	}
	if _, ok := m.Lookup("Karnataka", "Pune"); ok {
		t.Error("a city found in another state")
	}
	// Columns in another order, other names, comma-separated, a title line.
	m, err = parseMCXStateCity([]byte("MCX State City Master\nCity Code,City,State\n1442,Pune,MAHARASHTRA\n"))
	if err != nil || m.Count != 1 {
		t.Fatalf("%+v %v", m, err)
	}
	if c, _ := m.Lookup("Maharashtra", "Pune"); c != "1442" {
		t.Errorf("reordered %q", c)
	}
	// No state column: by city, unless the city has two codes.
	m, _ = parseMCXStateCity([]byte("City,Code\nPune,1442\nAurangabad,1401\nAurangabad,0501\n"))
	if c, ok := m.Lookup("Maharashtra", "Pune"); !ok || c != "1442" {
		t.Errorf("city only %q", c)
	}
	if _, ok := m.Lookup("Maharashtra", "Aurangabad"); ok {
		t.Error("an ambiguous city was guessed")
	}
	if _, err := parseMCXStateCity([]byte("State,Town\nMaharashtra,Pune\n")); err == nil {
		t.Error("a master without a code column was taken")
	}
}

func mcxSetup(t *testing.T) (*Ops, context.Context) {
	t.Helper()
	o, ctx := newOps(t)
	now := time.Date(2026, 10, 30, 11, 0, 0, 0, ist)
	o.Now = func() time.Time { return now }
	set, _ := o.Settings(ctx, T)
	set.MemberCodes = map[string]string{"NSE": "90311", "MCX": "56845"} // made up
	if err := o.SaveSettings(ctx, T, "admin", set); err != nil {
		t.Fatal(err)
	}
	put := func(c Client, d *UCCDetails) {
		if err := docs.Upsert(ctx, o.Docs, T, cClient, c.UCC, c, "priya.nair"); err != nil {
			t.Fatal(err)
		}
		if d != nil {
			if err := docs.Upsert(ctx, o.Docs, T, cUCCDetails, c.UCC, *d, "priya.nair"); err != nil {
				t.Fatal(err)
			}
		}
	}
	for i, u := range []string{"MCXA1", "MCXB2"} {
		d := mcxDetails(u)
		put(mcxUCCClient(u, "ABCP"+string(rune('A'+i))+"1111A", "981110000"+string(rune('2'+i)), now), &d)
	}
	co := mcxUCCClient("MCXC3", "ABCCC3333C", "9811100009", now)
	co.Type, co.Name = "Corporate", "Kulkarni Traders Pvt Ltd"
	put(co, nil)
	return o, ctx
}

func TestMCXUCCFileAndResponse(t *testing.T) {
	o, ctx := mcxSetup(t)
	// Without the State-City master nothing can go.
	if _, err := o.MCXUCCBatchFile(ctx, T, "priya.nair"); err == nil || !strings.Contains(err.Error(), "State-City code not found in MCX's master") {
		t.Fatalf("without the master: %v", err)
	}
	res, err := o.LoadExchangeFile(ctx, T, "priya.nair", "MCX_State_City_Master.csv", []byte("Sr No,State,City,State-City Code\n1,Maharashtra,Pune,1442\n2,Maharashtra,Mumbai,1449\n"), true)
	if err != nil || len(res) != 1 || res[0].Format != mcxStateCitySpec || res[0].Added != 2 || !strings.Contains(res[0].Loaded, "State-City") {
		t.Fatalf("master %+v %v", res, err)
	}

	b, err := o.MCXUCCBatchFile(ctx, T, "priya.nair")
	if err != nil {
		t.Fatal(err)
	}
	if b.Name != "MCX_30102026_UCC56845.M01" || b.Batch != 1 || strings.Join(b.Clients, ",") != "MCXA1,MCXB2" {
		t.Fatalf("batch %s %d %v %v", b.Name, b.Batch, b.Clients, b.Problems)
	}
	left := strings.Join(b.Problems, " | ")
	if !strings.Contains(left, "MCXC3: ") || !strings.Contains(left, "MCX's UCC portal") {
		t.Fatalf("left out %v", b.Problems)
	}
	content := string(b.Content)
	lines := strings.Split(strings.TrimSuffix(content, "\r\n"), "\r\n")
	if !strings.HasSuffix(content, "\r\n") || len(lines) != 3 || lines[0] != "10,M,56845,30102026,01,2" {
		t.Fatalf("file %q", content)
	}
	for _, l := range lines[1:] {
		f := strings.Split(l, ",")
		if len(f) != 240 || f[2] != "01" || f[14] != "1442" || f[27] != "" || f[239] != "E" {
			t.Fatalf("detail %d fields: %q", len(f), l)
		}
	}
	// Read back through the layout: recognised, no issues.
	lib, _ := XFiles()
	s, err := lib.Recognise(b.Name, b.Content)
	if err != nil || s.ID != "mcx-ucc" {
		t.Fatalf("recognise %v %v", s, err)
	}
	tb, err := s.Read(b.Name, b.Content)
	if err != nil || len(tb.Issues) != 0 || len(tb.Rows) != 3 || tb.Rows[0].Get("TotalRecords") != "2" || tb.Rows[1].Get("ClientCode") != "MCXA1" || tb.Rows[2].Get("StateCityCode") != "1442" {
		t.Fatalf("read back %v %v", err, tb.Issues)
	}
	// The same file from the Exchange files screen (not uploaded: still batch 01).
	name, again, err := o.ExchangeFile(ctx, T, "priya.nair", "mcx-ucc", "2026-10-30", XFileOptions{})
	if err != nil || name != b.Name || string(again) != content {
		t.Fatalf("exchange file %s %v", name, err)
	}

	// Uploaded: the clients are Sent, and the next file has nothing.
	if r, err := o.MCXUCCMarkUploaded(ctx, T, "priya.nair", b.Name); err != nil || r.Sent != 2 {
		t.Fatalf("uploaded %+v %v", r, err)
	}
	if r := mcxReg(t, o, ctx, "MCXA1"); r.Status != UCCSent || r.Via != "File" || r.Ref != b.Name {
		t.Fatalf("A %+v", r)
	}
	if _, err := o.MCXUCCBatchFile(ctx, T, "priya.nair"); !errors.Is(err, ErrState) {
		t.Fatalf("second file: %v", err)
	}
	if _, _, err := o.ExchangeFile(ctx, T, "priya.nair", "mcx-ucc", "2026-10-30", XFileOptions{Seq: 1}); err == nil || !strings.Contains(err.Error(), "already uploaded") {
		t.Fatalf("batch 01 again: %v", err)
	}

	// MCX's answer: A taken, B refused for its name.
	resp := "11,M,56845,30102026,01,2\r\nMCXA1,\r\nMCXB2,015\r\n"
	res, err = o.LoadExchangeFile(ctx, T, "priya.nair", "MCX_30102026_UCC56845.E01", []byte(resp), true)
	if err != nil || len(res) != 1 || res[0].Format != "mcx-ucc-response" || res[0].Problems != 0 || !strings.Contains(res[0].Loaded, "1 accepted, 1 rejected") {
		t.Fatalf("response %+v %v", res, err)
	}
	a, bb := mcxReg(t, o, ctx, "MCXA1"), mcxReg(t, o, ctx, "MCXB2")
	if a.Status != UCCAccepted || a.AcceptedHash == "" || a.AcceptedHash != a.Hash || a.Summary["status"] != "1" {
		t.Fatalf("A %+v", a)
	}
	if bb.Status != UCCRejected || !strings.Contains(bb.Reason, "015") || !strings.Contains(bb.Reason, "client name") {
		t.Fatalf("B %+v", bb)
	}
	ws, _ := o.WorkItems(ctx, T, "Open")
	found := false
	for _, w := range ws {
		found = found || w.Key == "mcxucc:rej:MCXB2" && w.Role == RoleMaker && w.UCC == "MCXB2"
	}
	if !found {
		t.Fatal("no work item for the rejected client")
	}
	v, err := o.MCXUCCView(ctx, T, "priya.nair")
	if err != nil || v.Exchange != "MCX" || v.NextBatch != 2 || v.MasterCount != 2 || len(v.Runs) != 1 || v.Runs[0].Accepted != 1 || v.Runs[0].Rejected != 1 {
		t.Fatalf("view %+v %v", v.Runs, err)
	}
	st := map[string]string{}
	for _, c := range v.Clients {
		st[c.UCC] = c.Status
	}
	if st["MCXA1"] != UCCAccepted || st["MCXB2"] != UCCRejected || st["MCXC3"] != UCCNeedsFixing {
		t.Fatalf("statuses %v", st)
	}

	// B corrected goes in batch 02; MCX refuses that file whole: B is Not
	// sent again.
	c, _ := o.Client(ctx, T, "MCXB2")
	c.Name = "Asha R Kulkarni"
	if err := docs.Upsert(ctx, o.Docs, T, cClient, c.UCC, c, "priya.nair"); err != nil {
		t.Fatal(err)
	}
	b2, err := o.MCXUCCBatchFile(ctx, T, "priya.nair")
	if err != nil || b2.Name != "MCX_30102026_UCC56845.M02" || strings.Join(b2.Clients, ",") != "MCXB2" {
		t.Fatalf("batch 2 %s %v %v", b2.Name, b2.Clients, err)
	}
	if _, err := o.MCXUCCMarkUploaded(ctx, T, "priya.nair", b2.Name); err != nil {
		t.Fatal(err)
	}
	res, err = o.LoadExchangeFile(ctx, T, "priya.nair", b2.Name+".F001", b2.Content, true)
	if err != nil || len(res) != 1 || res[0].Format != "mcx-ucc" || !strings.Contains(res[0].Loaded, "refused the whole file") {
		t.Fatalf("refused %+v %v", res, err)
	}
	if r := mcxReg(t, o, ctx, "MCXB2"); r.Status != UCCNotSent || r.Hash != "" {
		t.Fatalf("B after refusal %+v", r)
	}
	if b3, err := o.MCXUCCBatchFile(ctx, T, "priya.nair"); err != nil || b3.Name != "MCX_30102026_UCC56845.M03" {
		t.Fatalf("batch 3 %s %v", b3.Name, err)
	}

	// The preview shows the record by field number, PAN and account masked.
	p, err := o.MCXUCCPreview(ctx, T, "priya.nair", "MCXA1")
	if err != nil || len(p.Records) != 1 || p.Records[0]["015 State-City Code"] != "1442" || p.Records[0]["031 Bank Account Number 1"] != "XXXXXXXXXX5678" || len(p.Pending) != 0 || p.Status != UCCAccepted {
		t.Fatalf("preview %+v %v", p, err)
	}
	// A risk manager may download files but not view registrations.
	if _, err := o.MCXUCCView(ctx, T, "anita.rao"); err == nil {
		t.Fatal("risk manager viewed MCX UCC")
	}
}

func mcxReg(t *testing.T, o *Ops, ctx context.Context, ucc string) BSEUCCReg {
	t.Helper()
	r, _, err := docs.Get[BSEUCCReg](ctx, o.Docs, T, cBSEUCCReg, "MCX|"+ucc)
	if err != nil {
		t.Fatalf("%s: %v", ucc, err)
	}
	return r
}
