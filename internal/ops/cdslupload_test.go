package ops

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/docs"
)

// The harmonised BO upload is written from the same record as the
// fixed-width 08 file, with CDSL's old codes as standard values.
func TestCDSLBOUploadCSV(t *testing.T) {
	in := cdslJointIn()
	in.D.Codes = map[string]string{"occupation": "S"}
	in.Def["nationality_code"], in.Def["bo_statement_cycle_code"] = "01", "EM"
	rec, probs, _ := boSetupRecord(in)
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	at := time.Date(2026, 10, 30, 11, 5, 0, 0, ist)
	name, b, p := cdslBOUploadCSV(boCSVIn{Records: [][]boLine{rec}, DPID: "10900", Operator: "OPR01", Batch: 7, At: at}, 7)
	if len(p) > 0 || name != "BO_UPLD_010900_202610301105_007.csv" {
		t.Fatalf("%s %v", name, p)
	}
	lib, _ := XFiles()
	s, _ := lib.Get("cdsl-bo-upload")
	if got, err := lib.Recognise(name, b); err != nil || got.ID != s.ID {
		t.Fatalf("recognised as %v %v", got, err)
	}
	tb, err := s.Read(name, b)
	if err != nil || len(tb.Issues) != 0 || len(tb.Rows) != 12 || len(tb.Header) != 185 {
		t.Fatalf("%v %v rows %d header %d", err, tb.Issues, len(tb.Rows), len(tb.Header))
	}
	want := []string{"FH", "SH", "POALB", "POALB", "FH", "NM", "NM", "NMG", "BOUCC", "BOUCC", "BOUCC", "BOUCC"}
	for i, r := range tb.Rows {
		if r.Get("Purpse") != want[i] || r.Get("RcrdNb") != "1" || r.Get("RcdSRNumber") != fmt.Sprint(i+1) || r.Get("BOTxnTyp") != "BOSET" || r.Get("CntrlSctiesDpstryPtcpt") != "010900" {
			t.Errorf("line %d: %v", i+1, r.Values)
		}
		for k, v := range r.Values {
			if v == "DFT" {
				t.Errorf("line %d: %s is DFT (not allowed in uploads)", i+1, k)
			}
		}
	}
	fh := tb.Rows[0]
	for k, v := range map[string]string{"PurpCd": "CORAD", "FrstNm": "KIRAN", "PAN": "ABCPK1234L", "PrdNb": "IND", "Gndr": "MALE", "BirthDt": "1990-04-12", "MdOfOpr": "ANOSUR", "ComToBeSentTo": "ALH",
		"CtrySubDvsnCd": "27", "NoNmntnFlg": "NO", "BSDAFlg": "NBSD", "Ocptn": "SER", "Ntlty": "IN", "BnfclOwnrSttlmCyclCd": "EM", "MICRCd": "411002003", "SndrRefNb1": "BH2001", "RcvDt": "2026-10-30T11:05:09"} {
		if fh.Get(k) != v {
			t.Errorf("first holder %s = %q, want %q", k, fh.Get(k), v)
		}
	}
	if r := tb.Rows[2]; r.Get("GPABPAFlg") != "SET" || r.Get("PoaLnkPurpCd") != "ARH" || r.Get("POAMstrID") != "1234567890123456" || r.Get("FrDt") != "2026-10-29" {
		t.Errorf("DDPI line %v", r.Values)
	}
	if r := tb.Rows[5]; r.Get("PurpCd") != "NOMAD" || r.Get("NmneePctgOfShr") != "60" || r.Get("RsdlSecFlg") != "YES" || r.Get("RltshWthBnfclOwnr") == "" {
		t.Errorf("nominee line %v", r.Values)
	}
	if r := tb.Rows[8]; r.Get("Xchg") != "BSE" || r.Get("Sgmt") != "CM" || r.Get("BOUCCLkFlg") != "ADD" || r.Get("CnsntInd") != "BOUCC" || r.Get("UnqClntId") != "BH2001" {
		t.Errorf("BO-UCC line %v", r.Values)
	}
	// A code with no standard value stops the file (CDSL would reject it).
	in.Def["bo_statement_cycle_code"] = "MO"
	rec, _, _ = boSetupRecord(in)
	if _, _, p := cdslBOUploadCSV(boCSVIn{Records: [][]boLine{rec}, DPID: "10900", Operator: "OPR01", Batch: 8, At: at}, 8); len(p) != 1 || !strings.Contains(p[0], "BnfclOwnrSttlmCyclCd") {
		t.Errorf("problems %v", p)
	}
}

func TestCDSLBOUploadFormatSetting(t *testing.T) {
	o, ctx := newOps(t)
	if o.cdslBOUploadFormat(ctx, T) != cdslFmtFixed {
		t.Fatal("the default should stay fixed-width: the catalogue gives no date for the CSV upload")
	}
	if _, err := o.SaveCDSLBODefaults(ctx, T, "farhan.khan", map[string]string{"bo_upload_format": "XML"}); err == nil {
		t.Fatal("an unknown format was saved")
	}
	if _, err := o.SaveCDSLBODefaults(ctx, T, "farhan.khan", map[string]string{"bo_upload_format": "csv", "operator_id": "OPR01"}); err != nil {
		t.Fatal(err)
	}
	if o.cdslBOUploadFormat(ctx, T) != cdslFmtCSV {
		t.Fatal("CSV not saved")
	}
}

func TestCDSLUploadResponses(t *testing.T) {
	o, ctx := setup(t)
	c, h := cdxTestClient(t, o, ctx)
	// BO_RESP: an account opening accepted with its BO ID; a modify rejected.
	var other Client
	cs, _ := o.ClientMap(ctx, T)
	for _, u := range sortedKeys(cs) {
		if u != c.UCC && cs[u].Status == "Active" {
			other = cs[u]
			break
		}
	}
	for _, r := range []CDSLRequest{{ID: "CR-90001", Kind: CDSLOpen, UCC: other.UCC, Name: other.Name, Status: crSent, At: D}, {ID: "CR-90002", Kind: CDSLModify, UCC: c.UCC, Name: c.Name, Status: crSent, At: D}} {
		if err := docs.Upsert(ctx, o.Docs, T, cCDSLReq, r.ID, r, "admin"); err != nil {
			t.Fatal(err)
		}
	}
	rh := xheader(t, "cdsl-bo-response")
	resp := strings.Join([]string{strings.Join(rh, ","),
		xline(rh, map[string]string{"Src": "CDSL", "CntrlSctiesDpstryPtcpt": "081600", "IntlRefNb": other.UCC, "RcrdNb": "1", "LineNb": "1", "Status": "A", "ClntId": "1208160000765432"}),
		xline(rh, map[string]string{"Src": "CDSL", "CntrlSctiesDpstryPtcpt": "081600", "IntlRefNb": c.UCC, "RcrdNb": "2", "LineNb": "1", "Status": "R", "ErrCD": "E101", "ErrDesc": "Invalid PIN code"})}, "\r\n") + "\r\n"
	res, err := o.LoadExchangeFile(ctx, T, "priya.nair", "BO_RESP_081600_202610161230_001.csv", []byte(resp), true)
	if err != nil || res[0].Format != "cdsl-bo-response" || res[0].Added != 2 {
		t.Fatalf("%+v %v", res, err)
	}
	r1, _ := o.cdslGet(ctx, T, "CR-90001")
	r2, _ := o.cdslGet(ctx, T, "CR-90002")
	if r1.Status != crCompleted || r1.BOID != "1208160000765432" || r2.Status != crRejected || !strings.Contains(r2.Error, "Invalid PIN code") {
		t.Fatalf("%+v\n%+v", r1, r2)
	}
	if x, _ := o.Client(ctx, T, other.UCC); x.Demat.ID != "1208160000765432" {
		t.Fatalf("BO ID not on the client: %+v", x.Demat)
	}
	// TXN_RESP: the uploaded slip is executed.
	d := cdxTestDIS(t, o, ctx, c, h, 3)
	if _, err := docs.Update(ctx, o.Docs, T, cDIS, d.ID, "admin", func(x *DIS) error { x.Status, x.Batch = "Uploaded", "B1"; return nil }); err != nil {
		t.Fatal(err)
	}
	th := xheader(t, "cdsl-txn-response")
	tr := strings.Join(th, ",") + "\r\n" + xline(th, map[string]string{"Src": "CDSL", "CntrlSctiesDpstryPtcpt": "081600", "LineNb": "1", "IntlRefNb": d.ID, "SeqNm": "1", "Status": "A", "TxUnqId": "900000001"}) + "\r\n"
	res, err = o.LoadExchangeFile(ctx, T, "priya.nair", "TXN_RESP_081600_202610161300_00001.csv", []byte(tr), true)
	if err != nil || res[0].Format != "cdsl-txn-response" || res[0].Added != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if x, _, _ := docs.Get[DIS](ctx, o.Docs, T, cDIS, d.ID); x.Status != "Executed" {
		t.Fatalf("slip %s", x.Status)
	}
	// The API's response reader understands the harmonised columns too.
	body := strings.Join(th, ",") + "\n" + xline(th, map[string]string{"IntlRefNb": "DIS-1", "Status": "R", "ErrCD": "E9", "ErrDesc": "Insufficient balance"}) + "\n" + xline(th, map[string]string{"IntlRefNb": "DIS-2", "Status": "A", "TxUnqId": "5"})
	got, unread := cdxReadResponse("TXN_RESP.csv", []byte(body), []string{"DIS-1", "DIS-2"})
	if unread || !strings.Contains(got["DIS-1"], "Insufficient balance") || got["DIS-2"] != "" {
		t.Fatalf("%v %v", got, unread)
	}
}

func TestCDSLDISMaster(t *testing.T) {
	o, ctx := setup(t)
	c, _ := cdxTestClient(t, o, ctx)
	bo := o.bo(ctx, T, c).BOID
	book, err := o.IssueDISBook(ctx, T, "priya.nair", c.UCC, 5)
	if err != nil {
		t.Fatal(err)
	}
	h := xheader(t, "cdsl-dis-master")
	line := func(n int, slip int64, st, cxl string) string {
		return xline(h, map[string]string{"Src": "CDSL", "CntrlSctiesDpstryPtcpt": "81600", "LineNb": fmt.Sprint(n), "DisInstrId": fmt.Sprint(n), "DISIssdToClntOrPwrOfAttnyHldr": "BO", "SrcClntIdOrBnfclOwnrId": bo,
			"DISFrmtFlg": "NEW", "DISSrlNb": fmt.Sprintf("%012d", slip), "DISIssncDt": D, "DISSts": st, "DISCxlFlg": cxl, "DISBookltTp": "NRM"})
	}
	file := strings.Join([]string{strings.Join(h, ","), line(1, book.From, "CAN", "LD"), line(2, book.From+1, "USE", "DF"), line(3, book.From+2, "ACT", "DF")}, "\r\n") + "\r\n"
	res, err := o.LoadExchangeFile(ctx, T, "priya.nair", "DIS_MSTR_081600_77_F_202610161200_001.csv", []byte(file), true)
	if err != nil || res[0].Format != "cdsl-dis-master" || res[0].Problems != 0 || res[0].Added != 3 {
		t.Fatalf("%+v %v", res, err)
	}
	b, _, _ := docs.Get[DISBook](ctx, o.Docs, T, cDISBook, book.ID)
	if !contains(i64s(b.Lost), fmt.Sprint(book.From)) {
		t.Fatalf("cancelled slip not marked lost: %+v", b)
	}
	if _, err := o.EnterDIS(ctx, T, "priya.nair", DIS{UCC: c.UCC, Slip: book.From, Kind: "Off-market", ToBO: "1208160099999999", Reason: "Gift", ExecDate: D, Received: D, Lines: []DISLine{{ISIN: "INE000X01010", Qty: dq(1)}}}); err == nil || !strings.Contains(err.Error(), "lost") {
		t.Errorf("cancelled slip accepted: %v", err)
	}
	if w := cdslWork(t, o, "cdsldis", c.UCC+":"); len(w) != 1 || !strings.Contains(w[0].Detail, "used at CDSL") {
		t.Errorf("work %+v", w)
	}
}
