package ops

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"ledgerline/internal/docs"
)

// cdslTestClient makes an active client (synthetic data).
func cdslTestClient(t *testing.T, o *Ops, ctx context.Context, pan, name, boid string) Client {
	t.Helper()
	c := Client{Name: name, Type: "Individual", PAN: pan, DOB: "1990-04-12", Mobile: "9876500011", Email: strings.ToLower(strings.Fields(name)[0]) + "@example.test", City: "Pune", State: "Maharashtra",
		Segments: []string{"CM"}, Income: "5-10L", Bank: BankAccount{Bank: "Sample Bank", IFSC: "SBIN0001234", Account: "123456789012"}, Demat: Demat{Depository: "CDSL", ID: boid}, RiskDisclosure: true,
		Nominee: "Opted in", NomineeName: "Asha " + strings.Fields(name)[len(strings.Fields(name))-1],
		Nominees: []Nominee{{Name: "Asha " + strings.Fields(name)[len(strings.Fields(name))-1], Relation: "Spouse", Share: "100", IDType: "PAN", ID: "ABCPA1234B", DOB: "1991-01-01", Address: "Flat 4, Lake View, Kothrud, Pune 411038"}}}
	n, err := o.CreateApplication(ctx, T, "priya.nair", c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.AdvanceStage(ctx, T, "priya.nair", n.UCC); err != nil {
		t.Fatal(err)
	}
	n, err = o.AdvanceStage(ctx, T, "farhan.khan", n.UCC)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func cdslTestDefaults(t *testing.T, o *Ops, ctx context.Context) {
	t.Helper()
	if _, err := o.SaveCDSLBODefaults(ctx, T, "farhan.khan", map[string]string{"product_number": "0001", "bo_category": "01", "bo_sub_status": "0101", "mode_of_operation": "1", "communication_preference": "1",
		"bo_settlement_planning_flag": "N", "bo_statement_cycle_code": "MO", "divnd_bank_acct_type": "10", "divnd_bank_ccy": "INR", "cust_addr_cntry_code": "IN", "primary_mobil_isd_code": "91", "email_statement_flag": "Y"}); err != nil {
		t.Fatal(err)
	}
}

// fakeCDSL is a fake CDSL gateway for the BO lifecycle APIs.
type fakeCDSL struct {
	t        *testing.T
	mu       sync.Mutex
	key, iv  []byte
	opened   map[string]any // BOSetupDetails received
	callback string
	sigHash  string
	uploads  []string
	status   string
	counts   int
}

func (f *fakeCDSL) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := f.t
	checkSign := func(body []byte) bool {
		s, err := cdslDecrypt(f.key, f.iv, r.Header.Get("AuthSign"))
		return err == nil && string(s) == string(cdslHash(body, cdslHashHexLower))
	}
	switch r.URL.Path {
	case "/" + cdslPathAuth:
		b, _ := io.ReadAll(r.Body)
		if !checkSign(b) {
			w.Write([]byte(`{"Body":{"success":false,"AuthResponse":{"ErrorId":"ERR009","ErrorMessage":"Invalid Auth sign"}}}`))
			return
		}
		w.Write([]byte(`{"Body":{"success":true,"AuthToken":"TOK"}}`))
	case "/" + cdslPathBOOnline:
		if err := r.ParseMultipartForm(4 << 20); err != nil {
			t.Errorf("BO online is not multipart: %v", err)
			w.WriteHeader(400)
			return
		}
		v := r.MultipartForm.Value
		get := func(k string) string {
			if len(v[k]) > 0 {
				return v[k][0]
			}
			return ""
		}
		body := `{"receiptdate":` + q(get("receiptdate")) + `,"callbackurl":` + q(get("callbackurl")) + `,"urn":` + q(get("urn")) + `,"BOSetupDetails":` + get("BOSetupDetails") + `,"Signature":""}`
		if r.Header.Get("AuthToken") != "TOK" || !checkSign([]byte(body)) {
			w.Write([]byte(`{"Body":{"success":"False","URN":"","CRN":"","Response":{"ErrorId":"ERR01","ErrorMessage":"Invalid Auth sign"}}}`))
			return
		}
		if fs := r.MultipartForm.File["file"]; len(fs) == 1 {
			fh, _ := fs[0].Open()
			b, _ := io.ReadAll(fh)
			if r.Header.Get("BOSignComputeHash") != string(cdslHash(b, cdslHashHexLower)) {
				t.Error("BOSignComputeHash does not match the signature file")
			}
			f.sigHash = r.Header.Get("BOSignComputeHash")
		}
		json.Unmarshal([]byte(get("BOSetupDetails")), &f.opened)
		f.callback = get("callbackurl")
		w.Write([]byte(`{"Body":{"success":"True","URN":` + q(get("urn")) + `,"CRN":"CRN000123"}}`))
	case "/" + cdslPathBOModUpload, "/" + cdslPathBOSetupUpload:
		r.ParseMultipartForm(4 << 20)
		v := r.MultipartForm.Value
		var body []byte
		if r.URL.Path == "/"+cdslPathBOModUpload {
			body = cdslJSON("DPID", v["DPID"][0], "UploadId", v["UploadId"][0], "Signature", "")
		} else {
			body = cdslJSON("UploadId", v["UploadId"][0], "Signature", "")
		}
		fh, _ := r.MultipartForm.File["file"][0].Open()
		b, _ := io.ReadAll(fh)
		if !checkSign(body) || r.Header.Get("FileComputeHash") != string(cdslHash(b, cdslHashHexLower)) {
			w.Write([]byte(`{"Body":{"success":false,"UploadResponse":{"ErrorId":"ERR002","ErrorMessage":"Invalid Upload File Name"}}}`))
			return
		}
		f.uploads = append(f.uploads, r.MultipartForm.File["file"][0].Filename)
		w.Write([]byte(`{"Body":{"success":true,"AcknowledgmentID":"ACK77"}}`))
	case "/" + cdslPathStatus:
		b, _ := io.ReadAll(r.Body)
		if !checkSign(b) {
			w.WriteHeader(401)
			return
		}
		w.Write([]byte(`{"Body":{"success":true,"Status":` + q(f.status) + `}}`))
	case "/" + cdslPathDownload:
		w.Write([]byte(`{"Body":{"success":true,"ResponseFileName":"77_resp.zip","ResponseFilebuffer":"` + base64.StdEncoding.EncodeToString([]byte("RESPONSE")) + `"}}`))
	case "/" + cdslPathBOCount:
		var req map[string]string
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &req)
		plain, err := cdslDecrypt(f.key, f.iv, req["holderdetails"])
		var hd map[string]string
		json.Unmarshal(plain, &hd)
		if err != nil || req["version"] != "1.0" || req["dpid"] != "10900" || len(req["requestid"]) != 16 || !rePAN.MatchString(hd["pan"]) || hd["mobileno"] == "" {
			w.Write([]byte(`{"requestid":` + q(req["requestid"]) + `,"reqstatus":"1","reserror":"ERR016","reserrmsg":"Decrytion Failed","holdercountdata":""}`))
			return
		}
		f.counts++
		enc, _ := cdslEncrypt(f.key, f.iv, []byte(`{"pancount":"2","emailcount":"1","mobilenocount":"3"}`))
		w.Write([]byte(`{"requestid":` + q(req["requestid"]) + `,"dpid":"10900","restime":"30102026120000","reqstatus":"0","reserror":"","reserrmsg":"","holdercountdata":` + q(enc) + `}`))
	default:
		w.WriteHeader(404)
	}
}

func q(s string) string { b, _ := json.Marshal(s); return string(b) }

func newFakeCDSL(t *testing.T, o *Ops, on ...string) *fakeCDSL {
	key, _ := cdslAESKey(testCDSLKey, "k")
	iv, _ := cdslIV("")
	f := &fakeCDSL{t: t, key: key, iv: iv, status: "In Process"}
	srv := useCDSLServer(t, f.handler)
	fakeCDSLConn(t, o, srv.URL, on...)
	return f
}

func TestCDSLBOOnlineOpenAccount(t *testing.T) {
	o, ctx := newOps(t)
	f := newFakeCDSL(t, o, cdslFamBO)
	c := cdslTestClient(t, o, ctx, "ABCPK1234L", "Kiran Mohan Rao", "")
	if _, err := o.AddClientDoc(ctx, T, "priya.nair", c.UCC, "Signature", "sig.png", append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{1}, 64)...)); err != nil {
		t.Fatal(err)
	}
	// Without the DP's CDSL codes and the address, the problems are listed in words.
	r, err := o.NewCDSLRequest(ctx, T, "priya.nair", CDSLNew{Kind: CDSLOpen, UCC: c.UCC})
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != crNeedsDetails || !strings.Contains(strings.Join(r.Problems, "|"), "PIN code") || !strings.Contains(strings.Join(r.Problems, "|"), "product number") {
		t.Fatalf("problems: %s %v", r.Status, r.Problems)
	}
	cdslTestDefaults(t, o, ctx)
	r, err = o.SetCDSLFields(ctx, T, "priya.nair", r.ID, map[string]string{"address": "Flat 12, Shanti Kunj, Paud Road, Kothrud", "pin": "411038", "divnd_bank_code": "411002003", "gender": "Male"})
	if err != nil || r.Status != crChecker || len(r.Problems) != 0 {
		t.Fatalf("after details: %+v %v", r, err)
	}
	if _, err := o.ActOnCDSLRequest(ctx, T, "priya.nair", r.ID, "approve", "", ""); err == nil {
		t.Fatal("the maker approved their own request")
	}
	res, err := o.ActOnCDSLRequest(ctx, T, "farhan.khan", r.ID, "approve", "", "")
	if err != nil || res.Request.Status != crSent || res.Request.CDSLRef != "CRN000123" {
		t.Fatalf("approve: %+v %v", res.Request, err)
	}
	fh := f.opened["FirstHolderDetails"].(map[string]any)
	pd := f.opened["ProductDetails"].(map[string]any)
	if fh["income_tax_pan"] != "ABCPK1234L" || fh["bo_name"] != "KIRAN" || fh["bo_middle_name"] != "MOHAN" || fh["lastsearch_name"] != "RAO" || fh["cust_addr_zip"] != "411038" || fh["product_number"] != "0001" {
		t.Errorf("first holder: %v", fh)
	}
	if pd["divnd_acct_numb"] != "123456789012" || pd["date_of_birth_origin"] != "12041990" || pd["sex_code"] != "M" || pd["bo_category"] != "01" {
		t.Errorf("product: %v", pd)
	}
	if f.opened["SecondHolderDetails"] != nil || f.opened["UCCLink"] != nil {
		t.Error("absent sections must be null")
	}
	nom := f.opened["NomineeDetails"].([]any)[0].(map[string]any)
	if nom["percentage_of_shares"] != "10000" || nom["relationship_with_bo"] != "01" || nom["cust_addr_zip"] != "411038" || nom["city"] != "PUNE" || nom["income_tax_pan"] != "ABCPA1234B" {
		t.Errorf("nominee: %v", nom)
	}
	if f.sigHash == "" {
		t.Error("the signature file was not sent")
	}
	// CDSL calls back with the BO ID.
	u, _ := url.Parse(f.callback)
	tok := u.Path[strings.LastIndex(u.Path, "/")+1:]
	if !strings.HasPrefix(u.Path, "/hooks/"+T+"/cdsl/bo/") {
		t.Fatalf("callback address %s", f.callback)
	}
	if err := o.CDSLBOCallback(ctx, T, tok, []byte(`{"urn":"WRONG","crn":"CRN000123","boid":"1201090000001234","status":"1"}`)); err == nil {
		t.Fatal("a call-back for another URN was taken")
	}
	got, _ := o.cdslGet(ctx, T, r.ID)
	cb := `{"urn":` + q(got.Ref) + `,"crn":"CRN000123","boid":"1201090000001234","status":"1","filename":"ack.pdf","resfile":"` + base64.StdEncoding.EncodeToString([]byte("%PDF-ack")) + `"}`
	if err := o.CDSLBOCallback(ctx, T, tok, []byte(cb)); err != nil {
		t.Fatal(err)
	}
	got, _ = o.cdslGet(ctx, T, r.ID)
	nc, _ := o.Client(ctx, T, c.UCC)
	b, _, _ := docs.Get[BOAccount](ctx, o.Docs, T, cBO, c.UCC)
	if got.Status != crCompleted || got.BOID != "1201090000001234" || nc.Demat.ID != "1201090000001234" || b.BOID != "1201090000001234" || b.Status != "Active" || got.Reply == nil {
		t.Fatalf("after call-back: %+v client %+v bo %+v", got, nc.Demat, b)
	}
	if strings.Contains(got.Response, "%PDF") {
		t.Error("the file should be kept apart from the answer")
	}
	// Again: no harm.
	if err := o.CDSLBOCallback(ctx, T, tok, []byte(cb)); err != nil {
		t.Fatal(err)
	}
	if cnt, _ := o.CDSLDematCheck(ctx, T, "priya.nair", c.UCC); !cnt.Due {
		t.Error("a new account should be counted at CDSL")
	}
}

func TestCDSLAutoOpenAndManualFallback(t *testing.T) {
	o, ctx := newOps(t)
	newFakeCDSL(t, o) // connection set, BO API off
	cdslTestDefaults(t, o, ctx)
	c := cdslTestClient(t, o, ctx, "ABCPD4321E", "Divya Nair", "")
	o.cdslBOTick(ctx, T, time.Now())
	if _, ok := o.cdslOpenFor(ctx, T, c.UCC); ok {
		t.Fatal("no automatic opening while the BO API is off")
	}
	// Turned on: the activated client gets a request, waiting for details.
	fakeCDSLConn(t, o, o.mustConnURL(t, ctx), cdslFamBO)
	o.cdslBOTick(ctx, T, time.Now())
	r, ok := o.cdslOpenFor(ctx, T, c.UCC)
	if !ok || r.Status != crNeedsDetails || r.Checker != "" {
		t.Fatalf("auto open: %+v", r)
	}
	// Off again: approved, it is to be done in CDAS and the BO ID recorded.
	fakeCDSLConn(t, o, o.mustConnURL(t, ctx))
	if _, err := o.SetCDSLFields(ctx, T, "priya.nair", r.ID, map[string]string{"address": "7 MG Road", "pin": "411001", "divnd_bank_code": "411002003"}); err != nil {
		t.Fatal(err)
	}
	res, err := o.ActOnCDSLRequest(ctx, T, "farhan.khan", r.ID, "approve", "", "")
	if err != nil || res.Request.Status != crManual {
		t.Fatalf("manual: %+v %v", res.Request, err)
	}
	if _, err := o.ActOnCDSLRequest(ctx, T, "farhan.khan", r.ID, "done", "opened in CDAS", "12345"); err == nil {
		t.Fatal("a short BO ID was taken")
	}
	res, err = o.ActOnCDSLRequest(ctx, T, "farhan.khan", r.ID, "done", "opened in CDAS", "1201090000005678")
	if err != nil || res.Request.Status != crManualDone {
		t.Fatalf("done: %+v %v", res.Request, err)
	}
	if nc, _ := o.Client(ctx, T, c.UCC); nc.Demat.ID != "1201090000005678" {
		t.Fatalf("BO ID not recorded: %+v", nc.Demat)
	}
}

func (o *Ops) mustConnURL(t *testing.T, ctx context.Context) string {
	c, _, err := docs.Get[Connection](ctx, o.Docs, T, cConn, connCDSL)
	if err != nil {
		t.Fatal(err)
	}
	return c.Values["url"]
}

func TestCDSLModifyFromChangeRequest(t *testing.T) {
	o, ctx := newOps(t)
	f := newFakeCDSL(t, o, cdslFamBO)
	c := cdslTestClient(t, o, ctx, "ABCPM5555M", "Meera Joshi", "1201090000004444")
	o.cdslBOTick(ctx, T, time.Now()) // the API is first seen on now
	ch, err := o.RequestChange(ctx, T, "priya.nair", c.UCC, "Email", map[string]string{"email": "meera.new@example.test"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.DecideChange(ctx, T, "farhan.khan", ch.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	o.cdslBOTick(ctx, T, time.Now())
	r, ok := o.cdslBySource(ctx, T, ch.ID)
	if !ok || r.Kind != CDSLModify || r.Status != crChecker || len(r.Changes) != 1 || r.Changes[0].Field != "primary_email" || r.Changes[0].New != "meera.new@example.test" {
		t.Fatalf("captured: %+v", r)
	}
	// The BO modify file is made from the change: header, line 01, and a
	// line 02 with only the new email.
	if r.File == nil || !reBOFileName.MatchString(r.File.Name) || !strings.HasPrefix(r.File.Name, "11010900.") {
		t.Fatalf("generated file: %+v", r.File)
	}
	_, gen, _ := o.CDSLRequestFile(ctx, T, "priya.nair", r.ID, "sent")
	gl := strings.Split(strings.TrimSuffix(string(gen), "\r\n"), "\r\n")
	if len(gl) != 3 || gl[0] != "010900APIUSE00001" || boFieldOf(t, gl[1], "m01", "bo_id") != "1201090000004444" || strings.TrimSpace(boFieldOf(t, gl[2], "m02", "email")) != "meera.new@example.test" || strings.TrimSpace(boFieldOf(t, gl[2], "m02", "mobile")) != "" {
		t.Fatalf("modify file:\n%q", gen)
	}
	// Staff can still attach a file of their own.
	if _, err := o.AttachCDSLFile(ctx, T, "priya.nair", r.ID, "bad name!.csv", []byte("a")); err == nil {
		t.Fatal("a bad file name was taken")
	}
	if _, err := o.AttachCDSLFile(ctx, T, "priya.nair", r.ID, "BO_UPLD_10900_202610301200_0001.csv", []byte("H,1\nD,1201090000004444,meera.new@example.test\n")); err != nil {
		t.Fatal(err)
	}
	res, err := o.ActOnCDSLRequest(ctx, T, "farhan.khan", r.ID, "approve", "", "")
	if err != nil || res.Request.Status != crSent || res.Request.CDSLRef != "ACK77" || f.uploads[0] != "BO_UPLD_10900_202610301200_0001.csv.zip" {
		t.Fatalf("upload: %+v %v %v", res.Request, err, f.uploads)
	}
	res, _ = o.ActOnCDSLRequest(ctx, T, "priya.nair", r.ID, "status", "", "")
	if res.Request.Status != crSent || res.Request.CDSLStatus != "In Process" {
		t.Fatalf("in process: %+v", res.Request)
	}
	f.status = "Completed"
	res, _ = o.ActOnCDSLRequest(ctx, T, "priya.nair", r.ID, "status", "", "")
	if res.Request.Status != crCompleted || res.Request.Reply == nil {
		t.Fatalf("completed: %+v", res.Request)
	}
	if name, data, err := o.CDSLRequestFile(ctx, T, "vikram.shah", r.ID, "reply"); err != nil || name != "77_resp.zip" || string(data) != "RESPONSE" {
		t.Fatalf("reply file: %s %q %v", name, data, err)
	}
}

func TestCDSLActiveDematCount(t *testing.T) {
	o, ctx := newOps(t)
	f := newFakeCDSL(t, o, cdslFamBOCount)
	c := cdslTestClient(t, o, ctx, "ABCPS7777S", "Sunil Patil", "1201090000007777")
	o.cdslBOTick(ctx, T, time.Now()) // the first run notes the accounts already there
	if f.counts != 0 {
		t.Fatal("existing accounts are not checked until they change")
	}
	if _, err := docs.Update(ctx, o.Docs, T, cClient, c.UCC, "admin", func(x *Client) error { x.Mobile = "9876500099"; return nil }); err != nil {
		t.Fatal(err)
	}
	o.cdslBOTick(ctx, T, time.Now())
	got, _ := o.CDSLDematCheck(ctx, T, "anita.rao", c.UCC)
	if f.counts != 1 || got.Status != "Checked" || got.PAN != 2 || got.Mobile != 3 || got.Email != 1 || len(got.Notes) != 2 {
		t.Fatalf("count: %d %+v", f.counts, got)
	}
	if strings.Contains(got.Request, "ABCPS7777S") || strings.Contains(got.Request, "9876500099") {
		t.Error("the PAN and mobile must be masked in what is kept")
	}
	b, _, _ := docs.Get[BOAccount](ctx, o.Docs, T, cBO, c.UCC)
	if !b.OtherDemat {
		t.Error("two accounts for the PAN: the DP record should say the client has another demat account")
	}
	o.cdslBOTick(ctx, T, time.Now())
	if f.counts != 1 {
		t.Error("an unchanged account is not checked again")
	}
	if _, err := o.RunCDSLDematCheck(ctx, T, "priya.nair", c.UCC); err != nil || f.counts != 2 {
		t.Fatalf("run now: %v", err)
	}
}

// cdslPost is CDSL's encrypted answer as a form post.
func cdslPost(t *testing.T, field, reqid string, v any) url.Values {
	t.Helper()
	key, _ := cdslAESKey(testCDSLKey, "k")
	iv, _ := cdslIV("")
	b, _ := json.Marshal(v)
	enc, err := cdslEncrypt(key, iv, b)
	if err != nil {
		t.Fatal(err)
	}
	return url.Values{"reqid": {reqid}, field: {enc}}
}

func cdslOpenForm(t *testing.T, p CDSLPage, field string) (map[string]string, map[string]any) {
	t.Helper()
	fs := map[string]string{}
	for _, f := range p.Fields {
		fs[f[0]] = f[1]
	}
	key, _ := cdslAESKey(testCDSLKey, "k")
	iv, _ := cdslIV("")
	plain, err := cdslDecrypt(key, iv, fs[field])
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(plain, &m)
	return fs, m
}

func TestCDSLTransferCumClosure(t *testing.T) {
	o, ctx := newOps(t)
	newFakeCDSL(t, o, cdslFamTransfer)
	c := cdslTestClient(t, o, ctx, "ABCPT2222T", "Tara Kulkarni", "1201090000002222")
	docs.Upsert(ctx, o.Docs, T, cHolding, holdingID(c.UCC, "INE002A01018"), Holding{UCC: c.UCC, ISIN: "INE002A01018", Symbol: "RELIANCE", Qty: 10}, "admin")
	dpr, err := o.RaiseDPRequest(ctx, T, "priya.nair", DPRequest{Kind: "Closure", UCC: c.UCC, Detail: map[string]string{"transfer_to": "1304140000000352"}})
	if err != nil {
		t.Fatal(err)
	}
	dpr, err = o.ActOnDPRequest(ctx, T, "farhan.khan", dpr.ID, "approve", "")
	if err != nil || dpr.Status != "Sent to CDSL" {
		t.Fatalf("closure: %+v %v", dpr, err)
	}
	if b, _, _ := docs.Get[BOAccount](ctx, o.Docs, T, cBO, c.UCC); b.Status == "Closed" {
		t.Fatal("closed before CDSL confirmed")
	}
	if _, err := o.ActOnDPRequest(ctx, T, "farhan.khan", dpr.ID, "approve", ""); err == nil {
		t.Fatal("approved twice")
	}
	r, _ := o.cdslBySource(ctx, T, dpr.ID)
	if r.Status != crClient {
		t.Fatalf("CDSL request: %+v", r)
	}
	// The client's link: a new token is made so the test can open it.
	x, _ := o.cdslConnFor(ctx, T)
	link, err := o.cdslClientLink(ctx, T, "priya.nair", x, &r)
	if err != nil {
		t.Fatal(err)
	}
	o.cdslSave(ctx, T, "priya.nair", &r)
	tok := link[strings.LastIndex(link, "/")+1:]
	page, err := o.CDSLGoPage(ctx, T, tok)
	if err != nil || !strings.HasSuffix(page.Action, "/"+cdslPathTransfer) {
		t.Fatalf("page: %+v %v", page, err)
	}
	fs, m := cdslOpenForm(t, page, "bodetails")
	if fs["dpid"] != "10900" || fs["version"] != "1.0" || len(fs["reqid"]) != 24 || m["pan"] != "ABCPT2222T" || m["boid"] != "1201090000002222" || !strings.Contains(m["callbackurl"].(string), "/cdsl/return/"+tok) {
		t.Fatalf("form %v payload %v", fs, m)
	}
	// Opening the link again uses a new request ID.
	page2, _ := o.CDSLGoPage(ctx, T, tok)
	fs2, _ := cdslOpenForm(t, page2, "bodetails")
	if fs2["reqid"] == fs["reqid"] {
		t.Fatal("request ID reused")
	}
	// A forged answer (wrong key) is refused.
	if _, err := o.CDSLReturn(ctx, T, tok, url.Values{"reqid": {fs2["reqid"]}, "bodetails": {"bm90IGVuY3J5cHRlZA=="}}); err == nil {
		t.Fatal("unreadable answer taken")
	}
	// The client confirms with the OTP.
	p, err := o.CDSLReturn(ctx, T, tok, cdslPost(t, "bodetails", fs2["reqid"], map[string]any{"reqid": fs2["reqid"], "restime": "30102026120000", "reqstatus": "0", "reserror": "", "reserrmsg": "",
		"bodetails": []map[string]string{{"boid": "1201090000002222", "ischecked": "true", "reqstatus": "0"}}}))
	if err != nil || !p.OK {
		t.Fatal(p, err)
	}
	r, _ = o.cdslGet(ctx, T, r.ID)
	if r.Status != crConfirmed {
		t.Fatalf("after OTP: %s", r.Status)
	}
	// CDSL completes it: the inner details are encrypted too.
	key, _ := cdslAESKey(testCDSLKey, "k")
	iv, _ := cdslIV("")
	inner, _ := cdslEncrypt(key, iv, []byte(`{"sourceboid":"1201090000002222","destinationboid":"1304140000000352"}`))
	if _, err := o.CDSLReturn(ctx, T, tok, cdslPost(t, "bodetails", "", map[string]any{"trxnid": "6765756", "restime": "30102026130000", "reqstatus": "0", "reserror": "", "reserrmsg": "", "bodetails": inner})); err != nil {
		t.Fatal(err)
	}
	r, _ = o.cdslGet(ctx, T, r.ID)
	b, _, _ := docs.Get[BOAccount](ctx, o.Docs, T, cBO, c.UCC)
	d, _, _ := docs.Get[DPRequest](ctx, o.Docs, T, cDPReq, dpr.ID)
	h, _, _ := docs.Get[Holding](ctx, o.Docs, T, cHolding, holdingID(c.UCC, "INE002A01018"))
	if r.Status != crCompleted || r.CDSLRef != "6765756" || b.Status != "Closed" || d.Status != "Completed" || h.Qty != 0 || len(r.Warnings) != 0 {
		t.Fatalf("completed: %+v bo %+v dp %+v holding %d", r, b, d, h.Qty)
	}
	// The link is spent.
	if _, err := o.CDSLGoPage(ctx, T, tok); err == nil {
		t.Fatal("link still opens")
	}
}

func TestCDSLAddBeneficiary(t *testing.T) {
	o, ctx := newOps(t)
	newFakeCDSL(t, o, cdslFamBeneficiary)
	c := cdslTestClient(t, o, ctx, "ABCPB3333B", "Bhavna Shah", "1201090000003333")
	if _, err := o.NewCDSLRequest(ctx, T, "priya.nair", CDSLNew{Kind: CDSLBeneficiary, UCC: c.UCC, Buyers: []CDSLBuyer{{BOID: "1201090000003333"}, {BOID: "IN30012345678901", Exempt: "Y", PAN: "ABCPZ1111Z"}}}); err == nil ||
		!strings.Contains(err.Error(), "own account") || !strings.Contains(err.Error(), "without a PAN") {
		t.Fatalf("checks: %v", err)
	}
	r, err := o.NewCDSLRequest(ctx, T, "priya.nair", CDSLNew{Kind: CDSLBeneficiary, UCC: c.UCC, Buyers: []CDSLBuyer{{BOID: "1201090000009999", PAN: "abcpz1111z"}, {BOID: "IN30012345678901", Exempt: "Y"}}})
	if err != nil || r.Status != crChecker || r.Buyers[1].Type != "N" {
		t.Fatalf("raise: %+v %v", r, err)
	}
	res, err := o.ActOnCDSLRequest(ctx, T, "farhan.khan", r.ID, "approve", "", "")
	if err != nil || res.Request.Status != crClient || res.Link == "" {
		t.Fatalf("approve: %+v %v", res, err)
	}
	tok := res.Link[strings.LastIndex(res.Link, "/")+1:]
	page, err := o.CDSLGoPage(ctx, T, tok)
	if err != nil {
		t.Fatal(err)
	}
	fs, m := cdslOpenForm(t, page, "slrbyrdtls")
	byr := m["byrdtls"].([]any)
	b1 := byr[0].(map[string]any)
	if m["sellerboid"] != "1201090000003333" || len(m["sellerboreqid"].(string)) != 24 || len(byr) != 2 || b1["buyerpan"] != "ABCPZ1111Z" || b1["buyerbotype"] != "C" || b1["buyerexflg"] != "N" {
		t.Fatalf("payload %v", m)
	}
	r, _ = o.cdslGet(ctx, T, r.ID)
	p, err := o.CDSLReturn(ctx, T, tok, cdslPost(t, "slrbyrresdtls", fs["reqid"], map[string]any{"reqid": fs["reqid"], "sellerboid": "1201090000003333", "resstatus": "0", "reserror": "", "reserrmsg": "",
		"byrresdtls": []map[string]string{{"buyerboreqid": r.Buyers[0].ReqID, "byrreqstatus": "0", "buyerboid": "1201090000009999", "buyerresid": "BYRRES1"},
			{"buyerboreqid": r.Buyers[1].ReqID, "byrreqstatus": "1", "buyerboid": "IN30012345678901", "errorcode": "ERR024", "errormsg": "Buyer BO is not opted for PAN Exemption"}}}))
	if err != nil || !p.OK {
		t.Fatal(p, err)
	}
	r, _ = o.cdslGet(ctx, T, r.ID)
	if r.Status != crCompleted || r.Buyers[0].Status != "Added" || r.Buyers[0].ResID != "BYRRES1" || r.Buyers[1].Status != "Failed" || !strings.Contains(r.Buyers[1].Error, "ERR024") {
		t.Fatalf("result: %+v", r)
	}
}

func TestCDSLScreenAndRetry(t *testing.T) {
	o, ctx := newOps(t)
	newFakeCDSL(t, o, cdslFamBO)
	cdslTestDefaults(t, o, ctx)
	c := cdslTestClient(t, o, ctx, "ABCPR6666R", "Ravi Menon", "")
	r, _ := o.NewCDSLRequest(ctx, T, "priya.nair", CDSLNew{Kind: CDSLOpen, UCC: c.UCC, Fields: map[string]string{"address": "1 Main Rd", "pin": "560001", "divnd_bank_code": "560002003"}})
	// No callback address: the send fails in words and can be retried.
	cn, _, _ := docs.Get[Connection](ctx, o.Docs, T, cConn, connCDSL)
	cn.Values["callback_url"] = ""
	docs.Upsert(ctx, o.Docs, T, cConn, connCDSL, cn, "admin")
	t.Setenv("LEDGERLINE_PUBLIC_URL", "")
	res, _ := o.ActOnCDSLRequest(ctx, T, "farhan.khan", r.ID, "approve", "", "")
	if res.Request.Status != crFailed || !strings.Contains(res.Request.Error, "public address") {
		t.Fatalf("failed: %+v", res.Request)
	}
	cn.Values["callback_url"] = "https://bo.example.test"
	docs.Upsert(ctx, o.Docs, T, cConn, connCDSL, cn, "admin")
	res, err := o.ActOnCDSLRequest(ctx, T, "priya.nair", r.ID, "retry", "", "")
	if err != nil || res.Request.Status != crSent || res.Request.Tries != 2 || len(res.Request.Refs) != 1 {
		t.Fatalf("retry: %+v %v", res.Request, err)
	}
	s, err := o.CDSLRequests(ctx, T, "vikram.shah")
	if err != nil || len(s.Requests) != 1 || s.Counts["open"] != 1 || !s.Set || s.Requests[0].TokenHash != "" {
		t.Fatalf("screen: %+v %v", s, err)
	}
	if _, err := o.CDSLRequests(ctx, T, "anita.rao"); err == nil {
		t.Error("risk is not on this screen")
	}
}
