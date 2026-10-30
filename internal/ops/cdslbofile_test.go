package ops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/docs"
)

// boFieldOf reads one field of a written line by its layout.
func boFieldOf(t *testing.T, line, layout, key string) string {
	t.Helper()
	for _, f := range boLayouts[layout] {
		if f.Key == key {
			if len(line) < f.Start-1+f.Len {
				t.Fatalf("%s line too short for %s: %d", layout, key, len(line))
			}
			return line[f.Start-1 : f.Start-1+f.Len]
		}
	}
	t.Fatalf("no field %s on %s", key, layout)
	return ""
}

// The tables: every field starts where the one before ends, and the setup
// and modify versions of a line (which the communiqué lists separately)
// come to the same length.
func TestCDSLBOLayoutTables(t *testing.T) {
	for code, fs := range boLayouts {
		pos := 1
		for _, f := range fs {
			if f.Start != pos || f.Len <= 0 || !strings.ContainsRune("NCDT", rune(f.Type)) || !strings.ContainsRune("MON", rune(f.Input)) {
				t.Errorf("%s %s: start %d (want %d), len %d, type %c, input %c", code, f.Key, f.Start, pos, f.Len, f.Type, f.Input)
			}
			pos += f.Len
		}
		if pos-1 != boLineLengths[code] {
			t.Errorf("%s: %d characters, table says %d", code, pos-1, boLineLengths[code])
		}
	}
	for _, l := range []string{"02", "03", "05", "06", "07", "21", "22"} {
		if boLineLengths["s"+l] != boLineLengths["m"+l] {
			t.Errorf("line %s: setup %d, modify %d", l, boLineLengths["s"+l], boLineLengths["m"+l])
		}
	}
	// Positions read off the communiqué's order and sizes.
	for _, x := range []struct {
		layout, key string
		start, n    int
	}{{"hdr", "records", 13, 5}, {"s01", "request_date", 23, 14}, {"s02", "addr1", 217, 55}, {"s02", "pan", 616, 10}, {"s02", "email", 661, 100},
		{"s02", "pan_verify", 869, 4}, {"s02", "open_source", 964, 1}, {"m02", "name_reason", 644, 2}, {"m03", "del_flag", 380, 3}, {"s06", "master_id", 731, 50},
		{"m06", "master_id", 731, 16}, {"m06", "txn_type", 835, 4}, {"s22", "ucc", 10, 11}, {"sig", "request_date", 58, 14}, {"clo", "int_ref", 138, 16}} {
		for _, f := range boLayouts[x.layout] {
			if f.Key == x.key && (f.Start != x.start || f.Len != x.n) {
				t.Errorf("%s %s at %d/%d, want %d/%d", x.layout, x.key, f.Start, f.Len, x.start, x.n)
			}
		}
	}
}

func TestCDSLBOFormatRules(t *testing.T) {
	s, p := boFormat(boLine{Layout: "s21", No: 9, V: map[string]string{"purpose": "21", "map_flag": "1", "ex_id": "12", "cm_id": "IN554433", "cm_account": "1201090000000099"}}, false)
	if len(p) > 0 || s != "09"+"21"+"1"+"12"+"IN554433"+"1201090000000099" {
		t.Fatalf("%q %v", s, p)
	}
	// Numbers are zero-padded on the left, text space-padded on the right.
	s, _ = boFormat(boLine{Layout: "hdr", V: map[string]string{"dp_id": "10900", "operator_id": "OP1", "records": "7"}}, false)
	if s != "010900OP1   00007" {
		t.Fatalf("%q", s)
	}
	// '@' blanks a field in a modify file, to the field's length.
	s, p = boFormat(boLine{Layout: "m02", No: 2, V: map[string]string{"title": boClear}}, true)
	if len(p) > 0 || boFieldOf(t, s, "m02", "title") != "@@@@@@@@@@" || strings.Count(s, "@") != 10 || strings.TrimSpace(s) != "02"+strings.Repeat(" ", 4+100+20+20)+"@@@@@@@@@@" {
		t.Fatalf("clear: %q %v", s, p)
	}
	// Problems in words: missing, too long, not digits, not modifiable.
	_, p = boFormat(boLine{Layout: "s02", No: 2, Who: "Holder", V: map[string]string{"product": "12A", "name": strings.Repeat("A", 101)}}, false)
	j := strings.Join(p, "|")
	for _, want := range []string{"Holder: product number must be digits only", "Holder: bo name is longer than CDSL's 100 characters", "Holder: income tax pan is missing", "Holder: bo account opening source is missing"} {
		if !strings.Contains(j, want) {
			t.Errorf("want %q in %s", want, j)
		}
	}
	_, p = boFormat(boLine{Layout: "m05", No: 5, V: map[string]string{"bo_category": "01"}}, true)
	if len(p) != 1 || !strings.Contains(p[0], "cannot be changed at CDSL") {
		t.Errorf("not modifiable: %v", p)
	}
	if boFileName("08", "10900", 7) != "08010900.007" || boFileName("11", "010900", 12345) != "11010900.12345" {
		t.Error("file names")
	}
}

func cdslGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	p := filepath.Join("testdata", name)
	if os.Getenv("LL_UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(p, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("%v (run with LL_UPDATE_GOLDEN=1 to write it)", err)
	}
	if string(want) != string(got) {
		t.Fatalf("%s differs from the golden file:\n%s", name, got)
	}
}

func cdslJointIn() boSetupIn {
	at := time.Date(2026, 10, 30, 11, 5, 9, 0, ist)
	c := Client{UCC: "BH2001", Name: "Kiran Mohan Rao", Type: "Individual", PAN: "ABCPK1234L", DOB: "1990-04-12", Mobile: "9876500011", Email: "kiran@example.test", City: "Pune", State: "Maharashtra",
		Income: "10-25 L", Segments: []string{"CM", "FO"}, Bank: BankAccount{Bank: "Sample Bank", IFSC: "SBIN0001234", Account: "123456789012"}, Nominee: "Opted in",
		Nominees: []Nominee{{Name: "Asha Rao", Relation: "Spouse", Share: "60", IDType: "PAN", ID: "ABCPA1234B", DOB: "1991-01-01", Address: "Flat 4, Lake View, Kothrud, Pune 411038"},
			{Name: "Ravi Rao", Relation: "Son", Share: "40", IDType: "DL", ID: "MH1220110012345", DOB: "2015-05-05", Address: "Flat 4, Lake View, Kothrud, Pune 411038", Guardian: "Asha Rao", GuardianRelation: "Mother"}}}
	return boSetupIn{C: c,
		U: UCCDetails{Address1: "Flat 12, Shanti Kunj", Address2: "Paud Road, Kothrud", PIN: "411038", Gender: "Male", Guardian: "Mohan Rao"},
		D: CDSLBODetail{Holders: []CDSLJointHolder{{Name: "Asha Rao", PAN: "ABCPA1234B", Mobile: "9876500012", Email: "asha@example.test", PANVerified: true}},
			ModeOfOperation: "2", CommPref: "2", PANVerified: true, DDPIRegNo: "DDPI000000001234", DDPIDate: "2026-10-29", Codes: map[string]string{"occupation": "1"}},
		Def: map[string]string{"product_number": "0001", "bo_category": "01", "bo_sub_status": "0101", "bo_settlement_planning_flag": "N", "bo_statement_cycle_code": "MO",
			"divnd_bank_acct_type": "10", "divnd_bank_ccy": "INR", "primary_mobil_isd_code": "91", "email_statement_flag": "Y", "ucc_cm_id": "IN554433", "nationality_code": "IND"},
		F:  map[string]string{"divnd_bank_code": "411002003", "guardian2_address": "Flat 4, Lake View, Kothrud", "guardian2_city": "Pune", "guardian2_pin": "411038"},
		BO: BOAccount{DDPI: true}, Serial: 1, Received: at, Members: map[string]string{"NSE": "90311", "BSE": "6127"}, DDPIID: "1234567890123456", Sig: true}
}

// A joint account with two nominees (one a minor), DDPI and BO-UCC links,
// byte for byte, and each rule of the communiqué checked on it.
func TestCDSLBOSetupFileGolden(t *testing.T) {
	rec, probs, warns := boSetupRecord(cdslJointIn())
	if len(probs) > 0 || len(warns) > 0 {
		t.Fatalf("problems %v warnings %v", probs, warns)
	}
	f := boFile{Prefix: "08", DPID: "10900", Operator: "OPR01", Records: [][]boLine{rec}}
	b, p := f.Bytes()
	if len(p) > 0 {
		t.Fatal(p)
	}
	cdslGolden(t, "cdsl-bo-setup-08010900.001", b)
	lines := strings.Split(strings.TrimSuffix(string(b), "\r\n"), "\r\n")
	codes := []string{"hdr", "s01", "s02", "s03", "s05", "s06", "s06", "s07", "s08", "s08", "s08", "s22", "s22", "s22", "s22"}
	if len(lines) != len(codes) || !strings.HasSuffix(string(b), "\r\n") {
		t.Fatalf("%d lines", len(lines))
	}
	for i, l := range lines {
		if len(l) != boLineLengths[codes[i]] {
			t.Errorf("line %d (%s): %d characters, want %d", i, codes[i], len(l), boLineLengths[codes[i]])
		}
	}
	if lines[0] != "010900OPR01 00001" {
		t.Errorf("header %q", lines[0])
	}
	if lines[1] != "01"+"0001"+strings.Repeat(" ", 16)+"30102026110509" {
		t.Errorf("line 01 %q", lines[1])
	}
	get := func(i int, k string) string { return strings.TrimSpace(boFieldOf(t, lines[i], codes[i], k)) }
	for _, x := range []struct {
		i         int
		key, want string
	}{{2, "name", "KIRAN"}, {2, "middle", "MOHAN"}, {2, "last", "RAO"}, {2, "father", "MOHAN RAO"}, {2, "addr1", "FLAT 12, SHANTI KUNJ PAUD ROAD, KOTHRUD"}, {2, "state_code", "IN-MH"},
		{2, "city_seq", "00"}, {2, "isd", "000091"}, {2, "pan_verify", "5500"}, {2, "sig_flag", "Y"}, {2, "open_source", "2"}, {2, "del_flag", ""},
		{3, "line", "03"}, {3, "name", "ASHA"}, {3, "pan", "ABCPA1234B"}, {3, "email", "asha@example.test"},
		{4, "income", "0008"}, {4, "mode_op", "0002"}, {4, "comm_pref", "0002"}, {4, "nom_opt_out", "N"}, {4, "bank_ccy", "000356"}, {4, "bank_code", "411002003"}, {4, "sex", "M"}, {4, "dob", "12041990"},
		{5, "gpa_flag", "S"}, {6, "gpa_flag", "P"}, {5, "master_id", "1234567890123456"}, {5, "purpose", "0004"}, {5, "setup_date", "29102026"},
		{7, "purpose", "12"}, {8, "line", "08"}, {8, "purpose", "06"}, {8, "share", "06000"}, {8, "residual", "Y"}, {8, "relation", "01"}, {8, "zip", "411038"},
		{9, "line", "09"}, {9, "share", "04000"}, {9, "relation", "02"}, {10, "purpose", "08"}, {10, "nominee_serial", "02"}, {10, "relation", "05"},
		{11, "line", "11"}, {11, "exchange_id", "11"}, {11, "tm_code", "6127"}, {11, "segment", "1"}, {14, "line", "14"}, {14, "exchange_id", "12"}, {14, "segment", "2"}} {
		if g := get(x.i, x.key); g != x.want {
			t.Errorf("line %d %s = %q, want %q", x.i, x.key, g, x.want)
		}
	}
	// The minor nominee's guardian without an address is reported.
	in := cdslJointIn()
	delete(in.F, "guardian2_pin")
	in.D.CommPref, in.D.Holders[0].Mobile = "2", ""
	_, probs, _ = boSetupRecord(in)
	j := strings.Join(probs, "|")
	if !strings.Contains(j, "Guardian of nominee 2: give the address, city and 6-digit PIN code") || !strings.Contains(j, "Holder 2: communication to all holders needs the holder's mobile and email") {
		t.Errorf("problems: %v", probs)
	}
}

// The first holder's death: a setup record for the same BO ID with the
// survivor first, deletion flag 100, reason 06 and the date of death.
func TestCDSLBODeleteFirstHolder(t *testing.T) {
	in := cdslJointIn()
	in.BO.BOID, in.DeleteFirst = "1201090000004444", "2026-09-15"
	rec, probs, _ := boSetupRecord(in)
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	b, p := boFile{Prefix: "08", DPID: "10900", Operator: "OPR01", Records: [][]boLine{rec}}.Bytes()
	if len(p) > 0 {
		t.Fatal(p)
	}
	lines := strings.Split(string(b), "\r\n")
	if boFieldOf(t, lines[1], "s01", "bo_id") != "1201090000004444" || strings.TrimSpace(boFieldOf(t, lines[2], "s02", "name")) != "ASHA" ||
		boFieldOf(t, lines[2], "s02", "del_flag") != "100" || boFieldOf(t, lines[2], "s02", "del_reason") != "06" || boFieldOf(t, lines[2], "s02", "death_date") != "15092026" ||
		boFieldOf(t, lines[2], "s02", "pan_verify") != "5000" || boFieldOf(t, lines[3], "s05", "line") != "05" {
		t.Fatalf("deletion:\n%s", b)
	}
}

// A modify file: an address change clears the lines it no longer has; a
// nomination change modifies, adds and deletes nominees and the opt-out.
func TestCDSLBOModifyFileGolden(t *testing.T) {
	at := time.Date(2026, 10, 30, 15, 0, 0, 0, ist)
	c := Client{UCC: "BH2001", Name: "Kiran Mohan Rao", State: "Maharashtra", Nominee: "Opted in",
		Nominees: []Nominee{{Name: "Asha Rao", Relation: "Spouse", Share: "100", IDType: "PAN", ID: "ABCPA1234B", DOB: "1991-01-01", Address: "Flat 4, Lake View, Kothrud, Pune 411038"}}}
	addr := ChangeRequest{Kind: "Address", New: map[string]string{"address": "7 MG Road", "city": "Mumbai", "state": "Maharashtra", "pin": "400001"}, Old: map[string]string{"city": "Pune"}}
	rec, probs, _ := boModifyRecord(boModifyIn{Ch: addr, C: c, BOID: "1201090000004444", Def: map[string]string{}, Received: at})
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	nom := ChangeRequest{Kind: "Nomination", New: map[string]string{"nominee": "Opted in"}, Old: map[string]string{"nominees": `[{"name":"Old One","share":"50"},{"name":"Old Two","share":"50"}]`}}
	rec2, probs, _ := boModifyRecord(boModifyIn{Ch: nom, C: c, BOID: "1201090000004444", Def: map[string]string{}, Received: at})
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	b, p := boFile{Prefix: "11", DPID: "10900", Operator: "OPR01", Records: [][]boLine{rec, rec2}}.Bytes()
	if len(p) > 0 {
		t.Fatal(p)
	}
	cdslGolden(t, "cdsl-bo-modify-11010900.001", b)
	lines := strings.Split(strings.TrimSuffix(string(b), "\r\n"), "\r\n")
	codes := []string{"hdr", "m01", "m02", "m01", "m05", "m08", "m08"}
	if len(lines) != len(codes) || lines[0] != "010900OPR01 00002" {
		t.Fatalf("lines: %d %q", len(lines), lines[0])
	}
	for i, l := range lines {
		if len(l) != boLineLengths[codes[i]] {
			t.Errorf("line %d: %d characters", i, len(l))
		}
	}
	if lines[1] != "01"+"1201090000004444"+"30102026150000" {
		t.Errorf("line 01 %q", lines[1])
	}
	if strings.TrimSpace(boFieldOf(t, lines[2], "m02", "addr1")) != "7 MG ROAD" || boFieldOf(t, lines[2], "m02", "addr2") != strings.Repeat("@", 55) || boFieldOf(t, lines[2], "m02", "addr3") != strings.Repeat("@", 55) ||
		boFieldOf(t, lines[2], "m02", "zip") != "400001    " || strings.TrimSpace(boFieldOf(t, lines[2], "m02", "name")) != "" || strings.TrimSpace(boFieldOf(t, lines[2], "m02", "email")) != "" {
		t.Errorf("address line %q", lines[2])
	}
	if boFieldOf(t, lines[4], "m05", "nom_opt_out") != "N" || strings.Count(lines[4], " ") != len(lines[4])-3 {
		t.Errorf("only the opt-out goes on line 05: %q", lines[4])
	}
	if strings.TrimSpace(boFieldOf(t, lines[5], "m08", "txn_type")) != "M" || boFieldOf(t, lines[5], "m08", "share") != "10000" ||
		strings.TrimSpace(boFieldOf(t, lines[6], "m08", "txn_type")) != "D" || boFieldOf(t, lines[6], "m08", "nominee_serial") != "02" || boFieldOf(t, lines[6], "m08", "line") != "09" {
		t.Errorf("nominee lines:\n%s\n%s", lines[5], lines[6])
	}
}

func TestCDSLBOFilesInTheFlow(t *testing.T) {
	o, ctx := newOps(t)
	f := newFakeCDSL(t, o, cdslFamBO)
	cdslTestDefaults(t, o, ctx)
	// Joint account: the second holder dies; the modify file deletes the name.
	c := cdslTestClient(t, o, ctx, "ABCPJ1111J", "Jaya Iyer", "1201090000006666")
	if _, err := o.SaveCDSLBODetail(ctx, T, "priya.nair", c.UCC, CDSLBODetail{Holders: []CDSLJointHolder{{Name: "Hari Iyer", PAN: "ABCPH2222H"}}, ModeOfOperation: "2", CommPref: "1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.CDSLDeleteHolder(ctx, T, "priya.nair", c.UCC, 2, o.now().AddDate(-2, 0, 0).Format("2006-01-02")); err == nil || !strings.Contains(err.Error(), "transmission") {
		t.Fatalf("an old death: %v", err)
	}
	r, err := o.CDSLDeleteHolder(ctx, T, "priya.nair", c.UCC, 2, o.now().AddDate(0, -1, 0).Format("2006-01-02"))
	if err != nil || r.Status != crChecker || r.File == nil || !strings.HasPrefix(r.File.Name, "11010900.") {
		t.Fatalf("deletion request: %+v %v", r, err)
	}
	_, data, _ := o.CDSLRequestFile(ctx, T, "priya.nair", r.ID, "sent")
	ls := strings.Split(string(data), "\r\n")
	if boFieldOf(t, ls[2], "m03", "del_flag") != "010" || boFieldOf(t, ls[2], "m03", "del_reason") != "06" || boFieldOf(t, ls[2], "m03", "line") != "03" {
		t.Fatalf("deletion file:\n%s", data)
	}
	res, err := o.ActOnCDSLRequest(ctx, T, "farhan.khan", r.ID, "approve", "", "")
	if err != nil || res.Request.Status != crSent {
		t.Fatalf("send: %+v %v", res.Request, err)
	}
	f.status = "Completed"
	o.ActOnCDSLRequest(ctx, T, "priya.nair", r.ID, "status", "", "")
	if d := o.CDSLBODetailFor(ctx, T, c.UCC); len(d.Holders) != 0 || d.ModeOfOperation != "0" {
		t.Fatalf("holder still on the details: %+v", d)
	}

	// Opening by BO setup upload: the file is made from the client.
	if _, err := o.SaveCDSLBODefaults(ctx, T, "farhan.khan", map[string]string{"product_number": "0001", "bo_category": "01", "bo_sub_status": "0101", "mode_of_operation": "1", "communication_preference": "1",
		"bo_settlement_planning_flag": "N", "bo_statement_cycle_code": "MO", "divnd_bank_acct_type": "10", "divnd_bank_ccy": "INR", "open_via": cdslViaUpload, "operator_id": "OPR01"}); err != nil {
		t.Fatal(err)
	}
	n := cdslTestClient(t, o, ctx, "ABCPN3333N", "Neha Kapoor", "")
	docs.Upsert(ctx, o.Docs, T, cCDSLBODetail, n.UCC, CDSLBODetail{UCC: n.UCC, PANVerified: true}, "admin")
	if _, err := o.SaveUCCDetails(ctx, T, "priya.nair", n.UCC, UCCDetails{Address1: "21 Hill Road", City: "Pune", State: "Maharashtra", PIN: "411001"}); err != nil {
		t.Fatal(err)
	}
	s, err := o.NewCDSLRequest(ctx, T, "priya.nair", CDSLNew{Kind: CDSLSetupUpload, UCC: n.UCC, Fields: map[string]string{"divnd_bank_code": "411002003"}})
	if err != nil || s.Status != crChecker || !strings.HasPrefix(s.File.Name, "08010900.") {
		t.Fatalf("setup file: %+v %v", s, err)
	}
	_, data, _ = o.CDSLRequestFile(ctx, T, "priya.nair", s.ID, "sent")
	if !strings.HasPrefix(string(data), "010900OPR01 00001\r\n010001") {
		t.Fatalf("setup file:\n%s", data)
	}
	res, err = o.ActOnCDSLRequest(ctx, T, "farhan.khan", s.ID, "approve", "", "")
	if err != nil || res.Request.Status != crSent || f.uploads[len(f.uploads)-1] != s.File.Name+".zip" {
		t.Fatalf("upload: %+v %v %v", res.Request, err, f.uploads)
	}
	o.ActOnCDSLRequest(ctx, T, "priya.nair", s.ID, "status", "", "")
	res, err = o.ActOnCDSLRequest(ctx, T, "farhan.khan", s.ID, "boid", "from the response file", "1201090000007070")
	if err != nil {
		t.Fatal(err)
	}
	if nc, _ := o.Client(ctx, T, n.UCC); nc.Demat.ID != "1201090000007070" {
		t.Fatalf("BO ID: %+v", nc.Demat)
	}
	// Signature and closure files for CDAS.
	o.AddClientDoc(ctx, T, "priya.nair", n.UCC, "Signature", "sig.png", append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 40)...))
	name, z, err := o.CDSLSignatureFile(ctx, T, "priya.nair", n.UCC, "S")
	if err != nil || !strings.HasPrefix(name, "06010900.") || len(z) == 0 {
		t.Fatalf("signature: %s %v", name, err)
	}
	dpr, err := o.RaiseDPRequest(ctx, T, "priya.nair", DPRequest{Kind: "Closure", UCC: n.UCC, Detail: map[string]string{"transfer_to": "1304140000000352"}})
	if err != nil {
		t.Fatal(err)
	}
	name, cl, err := o.CDSLClosureFile(ctx, T, "priya.nair", dpr.ID)
	cls := strings.Split(string(cl), "\r\n")
	if err != nil || !strings.HasPrefix(name, "13010900.") || len(cls[1]) != boLineLengths["clo"] || boFieldOf(t, cls[1], "clo", "balances") != "T" || boFieldOf(t, cls[1], "clo", "reason") != "92" {
		t.Fatalf("closure: %s %v\n%s", name, err, cl)
	}
}
