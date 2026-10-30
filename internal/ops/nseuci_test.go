package ops

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"ledgerline/internal/docs"
)

// nseUCITestClient is a client that maps cleanly once its details are in.
func nseUCITestClient(ucc, pan string) Client {
	return Client{UCC: ucc, Name: "Test Client " + ucc, Type: "Individual", PAN: pan, DOB: "1985-04-12", Mobile: "9876543210", Email: "client." + strings.ToLower(ucc) + "@example.com",
		City: "Mumbai", State: "Maharashtra", Status: "Active", Segments: []string{"CM", "FO"}, Income: "5-10 L", RAS: "Quarterly", Risk: "Medium", Nominee: "Opted out",
		Bank: BankAccount{Bank: "HDFC Bank, Fort", IFSC: "HDFC0001234", Account: "50100012345678"}, Demat: Demat{Depository: "CDSL", ID: "1208160000012345"}, KYCDate: "2026-09-01"}
}

var nseUCITestDetails = UCCDetails{Address1: "12 Test Lane, Fort", PIN: "400001", Gender: "Female", Guardian: "Test Guardian", Marital: "Married", Occupation: "Professional"}

func nseUCIAddClient(t *testing.T, o *Ops, c Client, withDetails bool) {
	t.Helper()
	ctx := t.Context()
	if err := docs.Upsert(ctx, o.Docs, T, cClient, c.UCC, c, "test"); err != nil {
		t.Fatal(err)
	}
	if withDetails {
		if _, err := o.SaveUCCDetails(ctx, T, "priya.nair", c.UCC, nseUCITestDetails); err != nil {
			t.Fatal(err)
		}
	}
}

func TestNSEUCIMapping(t *testing.T) {
	now := time.Date(2026, 10, 30, 11, 0, 0, 0, ist)
	c := nseUCITestClient("TC001", "ABCPV1234K")
	c.Bank.Bank = "HDFC Bank, Fort"
	m := nseUCIMap(c, UCCDetails{Occupation: "Salaried"}, nil, Settings{}, "90311", now, NSEUCIReg{})
	want := []string{"Address line 1", "Gender", "Father's or spouse's name", "Marital status", "public sector, private sector or government service"}
	all := strings.Join(m.Problems, "\n")
	for _, w := range want {
		if !strings.Contains(all, w) {
			t.Errorf("problems do not mention %q:\n%s", w, all)
		}
	}
	m = nseUCIMap(c, nseUCITestDetails, nil, Settings{}, "90311", now, NSEUCIReg{})
	if len(m.Problems) != 0 || len(m.Records) != 2 {
		t.Fatalf("problems %v, %d records", m.Problems, len(m.Records))
	}
	b := m.Records[0].Body
	for k, v := range map[string]string{"ccdMemCd": "90311", "ccdCd": "TC001", "ccdSegInd": "C", "ccdCategory": "1", "ccdDob": "12-04-1985", "ccdAddState": "19", "ccdAddCountry": "85",
		"ccdPinCode": "400001", "ccdDeposName": "CDSL", "ccdBenAcctNo": "1208160000012345", "ccdDeposId": "", "ccdBankName": "HDFC Bank Fort", "ccdPriSecBnk": "P",
		"ccdGrosAnnlRnge": "3", "ccdGrosAnnlAsdate": "01-09-2026", "ccdOptForUpi": "N", "ccdNomFlag": "2", "ccdCltStatus": "A", "ccdGender": "F", "ccdMaritlStatus": "M",
		"ccdOccupation": "5", "ccdNationality": "1", "ccdPermantAddFlg": "Y", "ccdAcctType": "CLIENT", "ccdPep": "0", "ccdPoaFunds": "N", "ccdIpv": "Y"} {
		if b[k] != v {
			t.Errorf("%s = %q, want %q", k, b[k], v)
		}
	}
	if len(b) != len(nseUCIKeys) || m.Records[1].Body["ccdSegInd"] != "F" || m.Records[0].Hash == m.Records[1].Hash {
		t.Fatalf("%d fields; segments %s", len(b), m.Records[1].Body["ccdSegInd"])
	}
	// NSDL demat splits into DP ID and client ID; a suspended segment is
	// inactive; the firm's own PAN is an OWN account.
	c.Demat = Demat{Depository: "NSDL", ID: "IN30216412344405"}
	c.SegStatus = map[string]SegmentState{"FO": {Status: SegSuspended, Since: "2026-10-01", Reason: "income proof lapsed"}}
	m = nseUCIMap(c, nseUCITestDetails, nil, Settings{FirmPAN: c.PAN}, "90311", now, NSEUCIReg{})
	if b := m.Records[0].Body; b["ccdDeposId"] != "IN302164" || b["ccdBenAcctNo"] != "12344405" || b["ccdAcctType"] != "OWN" {
		t.Fatalf("NSDL / OWN: %q %q %q", b["ccdDeposId"], b["ccdBenAcctNo"], b["ccdAcctType"])
	}
	if b := m.Records[1].Body; b["ccdCltStatus"] != "I" || b["ccdCltStatusReason"] != "income proof lapsed" {
		t.Fatalf("suspended F&O: %q %q", b["ccdCltStatus"], b["ccdCltStatusReason"])
	}
	// An old income declaration and a bad e-mail are reported.
	c.KYCDate, c.Email = "2024-03-01", "noemail@example.com"
	m = nseUCIMap(c, nseUCITestDetails, nil, Settings{}, "90311", now, NSEUCIReg{})
	all = strings.Join(m.Problems, "\n")
	if !strings.Contains(all, "previous calendar year") || !strings.Contains(all, "E-mail: is a placeholder") {
		t.Fatalf("problems:\n%s", all)
	}
	// A company needs its CIN and two directors.
	co := nseUCITestClient("TC002", "AABCT1234C")
	co.Type, co.Segments = "Corporate", []string{"CM"}
	m = nseUCIMap(co, UCCDetails{Address1: "1 Test Park, Andheri", PIN: "400069"}, nil, Settings{}, "90311", now, NSEUCIReg{})
	all = strings.Join(m.Problems, "\n")
	if !strings.Contains(all, "CIN") || !strings.Contains(all, "Contact persons: NSE needs 2") {
		t.Fatalf("company problems:\n%s", all)
	}
	d := UCCDetails{Address1: "1 Test Park, Andheri", PIN: "400069", CIN: "U67120MH2001PTC123456", Contacts: []UCCContact{
		{Name: "Director One", PAN: "ABCPD1234E", Address: "1 Test Park Andheri Mumbai", Phone: "02212345678", DIN: "01234567"},
		{Name: "Director Two", PAN: "ABCPD1235E", Address: "2 Test Park Andheri Mumbai", Phone: "02212345679", DIN: "NO_DIN"}}}
	m = nseUCIMap(co, d, nil, Settings{}, "90311", now, NSEUCIReg{})
	if len(m.Problems) != 0 {
		t.Fatalf("company: %v", m.Problems)
	}
	if b := m.Records[0].Body; b["ccdDir1Desig"] != "DIRECTOR" || b["ccdClntTyp"] != "Y" || b["ccdGender"] != "" || b["ccdOptForUpi"] != "NA" || b["ccdDinCntrctPerson2"] != "NO_DIN" {
		t.Fatalf("company record %v", b)
	}
	// The UPI flag: D only after Y.
	c = nseUCITestClient("TC003", "ABCPV1235K")
	m = nseUCIMap(c, nseUCITestDetails, nil, Settings{}, "90311", now, NSEUCIReg{UPI: "Y"})
	if m.Records[0].Body["ccdOptForUpi"] != "D" {
		t.Fatal("deregistration not sent as D")
	}
	// Masking for the audit copy and the screen.
	r := nseUCIMask(map[string]string{"ccdUid": "123456789012", "ccdPanNo": "ABCPV1234K", "ccdBankAcctNo": "50100012345678"}, false)
	if r["ccdUid"] != "XXXXXXXX9012" || r["ccdPanNo"] != "ABCPV1234K" {
		t.Fatalf("audit mask %v", r)
	}
	r = nseUCIMask(map[string]string{"ccdPanNo": "ABCPV1234K", "ccdBankAcctNo": "50100012345678"}, true)
	if r["ccdPanNo"] != "ABCPVXXXXK" || !strings.HasSuffix(r["ccdBankAcctNo"], "5678") || strings.Contains(r["ccdBankAcctNo"], "501000") {
		t.Fatalf("screen mask %v", r)
	}
}

func TestNSEUCIEmailRules(t *testing.T) {
	for e, ok := range map[string]bool{"a.b@example.com": true, "ab@example": false, "a@@b.com": false, ".ab@x.com": false, "ab@.example.com": false,
		"ab@example.com.": false, "a#b@example.com": false, "x@notprovided.com": false, "a b@example.com": false} {
		if got := nseUCIEmailProblem(e) == ""; got != ok {
			t.Errorf("%s: ok %v, want %v (%s)", e, got, ok, nseUCIEmailProblem(e))
		}
	}
}

// fakeNSEUCI is NSE's UCI API as the TDD describes it.
type fakeNSEUCI struct {
	mu        sync.Mutex
	logins    int
	token     string
	uploads   [][]map[string]string
	panCalls  int
	dateCalls int
	refuse    string            // a whole-request msgCode to answer with
	reject    map[string]string // UCC: msgCode
	pan       map[string]string // UCC: panStatus
	bodies    []string
}

func (f *fakeNSEUCI) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		var raw json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&raw)
		f.bodies = append(f.bodies, string(raw))
		if r.Header.Get("Content-Type") != "application/json" {
			w.WriteHeader(400)
			return
		}
		if r.URL.Path == "/uciservice/api/auth" {
			var b map[string]string
			_ = json.Unmarshal(raw, &b)
			if b["username"] != "90311" || b["password"] != "Uci@pass1" || b["apiName"] != "uci_clientupload" {
				w.WriteHeader(401)
				w.Write([]byte(`{"msgCode":"401","msgDesc":"Authentication Failed"}`))
				return
			}
			f.logins++
			claims, _ := json.Marshal(map[string]any{"sub": "90311", "exp": time.Now().Add(time.Hour).Unix()})
			f.token = fmt.Sprintf("eyJhbGciOiJIUzUxMiJ9.%s.sig%d", base64.RawURLEncoding.EncodeToString(claims), f.logins)
			w.Write([]byte(`{"token":"` + f.token + `"}`))
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+f.token {
			w.WriteHeader(401)
			w.Write([]byte(`{"msgCode":"401","msgDesc":"Authentication Failed"}`))
			return
		}
		switch r.URL.Path {
		case "/uciservice/api/clientupload":
			var req struct {
				Request []map[string]string `json:"request"`
			}
			_ = json.Unmarshal(raw, &req)
			if len(req.Request) > 50 {
				w.Write([]byte(`{"msgCode":"212","msgDesc":"Request exceeding maximum UCCs allowed per request."}`))
				return
			}
			if f.refuse != "" {
				w.Write([]byte(`{"msgCode":"` + f.refuse + `","msgDesc":"Your request cannot be process at this moment."}`))
				return
			}
			f.uploads = append(f.uploads, req.Request)
			var out []map[string]string
			for _, x := range req.Request {
				if len(x) != len(nseUCIKeys) {
					t.Errorf("record with %d fields", len(x))
				}
				code, desc, st := "200", "Client info saved/created successfully", "SUCCESS"
				if c := f.reject[x["ccdCd"]]; c != "" {
					code, desc, st = c, "PAN is debarred", "FAILURE"
				}
				// The TDD's sample spells the key "Status" in some entries.
				out = append(out, map[string]string{"clientCode": x["ccdCd"], "segment": x["ccdSegInd"], "Status": st, "msgCode": code, "msgDesc": desc})
			}
			json.NewEncoder(w).Encode(map[string]any{"response": out})
		case "/uciservice/api/getuccpanstatus":
			f.panCalls++
			var req struct {
				Request struct {
					MemberCode string   `json:"memberCode"`
					UCC        []string `json:"ucc"`
				} `json:"request"`
			}
			_ = json.Unmarshal(raw, &req)
			var groups []map[string]any
			for _, u := range req.Request.UCC {
				st, ok := f.pan[u]
				if !ok {
					groups = append(groups, map[string]any{"status": "FAILURE", "statusCode": "202", "msgDesc": "No Data found against given UCC"})
					continue
				}
				groups = append(groups, map[string]any{"status": "SUCCESS", "statusCode": "200", "uccStatusData": []map[string]any{
					{"memberCode": "90311", "clientCode": u, "segment": "C", "panStatus": st, "panStatusDate": "2026-10-30 00:00:00.0", "adhrSeedingStatus": "Y", "exchangeStatus": "Permitted to trade"},
					{"memberCode": "90311", "clientCode": u, "segment": "F", "panStatus": st, "panStatusDate": "2026-10-30 00:00:00.0", "adhrSeedingStatus": nil, "exchangeStatus": map[bool]string{true: "Not Permitted to Trade", false: "Permitted to trade"}[st == "I"]}}})
			}
			json.NewEncoder(w).Encode(map[string]any{"response": groups})
		case "/uciservice/api/getpanstatus":
			f.dateCalls++
			w.Write([]byte(`{"status":"FAILURE","statusCode":"202","msgDesc":"No Data found for given criterion","uccStatusData":[]}`))
		default:
			w.WriteHeader(404)
		}
	})
}

func nseUCISetup(t *testing.T) (*Ops, *fakeNSEUCI, *time.Time) {
	t.Helper()
	o, ctx := newOps(t)
	day := time.Date(2026, 10, 30, 11, 0, 0, 0, ist)
	o.Now = func() time.Time { return day }
	f := &fakeNSEUCI{reject: map[string]string{}, pan: map[string]string{}}
	srv := httptest.NewTLSServer(f.handler(t))
	t.Cleanup(srv.Close)
	old := nseUCIHTTP
	nseUCIHTTP = srv.Client()
	t.Cleanup(func() { nseUCIHTTP = old })
	if _, err := o.SaveConnection(ctx, T, "admin", connNSEUCI, map[string]string{"member": "90311", "env": "UAT", "send": "On", "url": srv.URL + "/uciservice/api"},
		map[string]string{"password": "Uci@pass1"}); err != nil {
		t.Fatal(err)
	}
	return o, f, &day
}

func TestNSEUCISend(t *testing.T) {
	o, f, day := nseUCISetup(t)
	ctx := t.Context()
	nseUCIAddClient(t, o, nseUCITestClient("TC001", "ABCPV1234K"), true)
	nseUCIAddClient(t, o, nseUCITestClient("TC002", "ABCPV1235K"), true) // NSE rejects
	nseUCIAddClient(t, o, nseUCITestClient("TC003", "ABCPV1236K"), false)
	pre := nseUCITestClient("TC004", "ABCPV1237K")
	pre.Status = "KYC verification" // not yet at the UCC upload stage
	nseUCIAddClient(t, o, pre, true)
	f.reject["TC002"] = "209"

	v, err := o.NSEUCI(ctx, T, "anita.rao")
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Clients) != 3 || v.Counts[nseUCINotSent] != 2 || v.Counts[nseUCINeedsFix] != 1 || v.Connection != "Saved" || v.Live || v.Exchange != "NSE" {
		t.Fatalf("view %+v", v)
	}
	if v.Clients[0].UCC != "TC003" || len(v.Clients[0].Problems) == 0 || v.Clients[0].Action != "Add" {
		t.Fatalf("first row %+v", v.Clients[0])
	}
	if _, err := o.NSEUCISend(ctx, T, "anita.rao", nil); !errors.Is(err, ErrForbidden) {
		t.Fatalf("risk manager sent: %v", err)
	}
	pv, err := o.NSEUCIPreview(ctx, T, "priya.nair", "TC001")
	if err != nil || len(pv.Records) != 2 || pv.Records[0]["ccdPanNo"] != "ABCPVXXXXK" || len(pv.Pending) != 2 || len(pv.Problems) != 0 {
		t.Fatalf("preview %+v %v", pv, err)
	}

	res, err := o.NSEUCISend(ctx, T, "priya.nair", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Sent != 2 || res.Accepted != 1 || res.Rejected != 1 || len(res.Problems) != 1 || !strings.HasPrefix(res.Problems[0], "TC003: ") {
		t.Fatalf("send %+v", res)
	}
	if f.logins != 1 || len(f.uploads) != 1 || len(f.uploads[0]) != 4 {
		t.Fatalf("logins %d, uploads %d", f.logins, len(f.uploads))
	}
	v, _ = o.NSEUCI(ctx, T, "priya.nair")
	rows := map[string]NSEUCIClientRow{}
	for _, r := range v.Clients {
		rows[r.UCC] = r
	}
	if rows["TC001"].Status != nseUCIAccepted || rows["TC002"].Status != nseUCIRejected || !strings.Contains(rows["TC002"].Reason, "PAN is debarred") || rows["TC001"].Ref == "" || rows["TC001"].SentAt == "" {
		t.Fatalf("rows %+v", rows)
	}
	if len(v.Runs) != 1 || v.Runs[0].Sent != 2 || v.Runs[0].By != "priya.nair" {
		t.Fatalf("runs %+v", v.Runs)
	}
	ws, _ := docs.List[WorkItem](ctx, o.Docs, T, cWorkItem, "")
	found := false
	for _, w := range ws {
		found = found || (w.UCC == "TC002" && w.Role == RoleMaker && w.Status == "Open" && w.View == "uccapi")
	}
	if !found {
		t.Fatal("no work item for the rejected client")
	}
	// The audit copy has the records and never the password.
	run, _, _ := docs.Get[NSEUCIRun](ctx, o.Docs, T, cUCCRun, v.Runs[0].ID)
	if len(run.Batches) != 1 || !strings.Contains(run.Batches[0].Request, `"ccdCd":"TC001"`) || !strings.Contains(run.Batches[0].Response, "PAN is debarred") {
		t.Fatalf("audit %+v", run.Batches)
	}
	j, _ := json.Marshal(run)
	if strings.Contains(string(j), "Uci@pass1") {
		t.Fatal("the password is in the audit copy")
	}

	// Nothing changed: nothing is sent again, and the token is reused.
	*day = day.Add(10 * time.Minute)
	res, _ = o.NSEUCISend(ctx, T, "priya.nair", nil)
	if res.Sent != 0 || len(f.uploads) != 1 {
		t.Fatalf("resent unchanged records: %+v", res)
	}
	// An approved change (a new e-mail) is sent as a modification of both
	// segments; the token is still good.
	if _, err := docs.Update(ctx, o.Docs, T, cClient, "TC001", "test", func(c *Client) error { c.Email = "new.address@example.com"; return nil }); err != nil {
		t.Fatal(err)
	}
	res, _ = o.NSEUCISend(ctx, T, "farhan.khan", []string{"TC001"})
	if res.Sent != 1 || res.Accepted != 1 || len(f.uploads) != 2 || len(f.uploads[1]) != 2 || f.logins != 1 {
		t.Fatalf("modify %+v, uploads %d, logins %d", res, len(f.uploads), f.logins)
	}
	reg, _, _ := docs.Get[NSEUCIReg](ctx, o.Docs, T, cUCCReg, "NSE|TC001")
	if reg.Action != "Modify" || len(reg.History) != 2 {
		t.Fatalf("reg %+v", reg)
	}
	// A segment status change is sent for that segment only.
	if _, err := o.SetSegmentStatus(ctx, T, "farhan.khan", "TC001", "FO", SegSuspended, "2026-10-30", "income proof lapsed"); err != nil {
		t.Fatal(err)
	}
	res, _ = o.NSEUCISend(ctx, T, "priya.nair", nil)
	if res.Sent != 1 || len(f.uploads[2]) != 1 || f.uploads[2][0]["ccdSegInd"] != "F" || f.uploads[2][0]["ccdCltStatus"] != "I" {
		t.Fatalf("segment change %+v", f.uploads[len(f.uploads)-1])
	}

	// NSE replaces the token: a 401 logs in again, once.
	f.mu.Lock()
	f.token = "replaced"
	f.mu.Unlock()
	if _, err := docs.Update(ctx, o.Docs, T, cClient, "TC002", "test", func(c *Client) error { c.PAN = "ABCPV1299K"; return nil }); err != nil {
		t.Fatal(err)
	}
	delete(f.reject, "TC002")
	res, _ = o.NSEUCISend(ctx, T, "priya.nair", nil)
	if res.Accepted != 1 || f.logins != 2 {
		t.Fatalf("after 401: %+v, logins %d", res, f.logins)
	}
	ws, _ = docs.List[WorkItem](ctx, o.Docs, T, cWorkItem, "")
	for _, w := range ws {
		if w.UCC == "TC002" && w.Status == "Open" {
			t.Fatal("the rejection work item stayed open after NSE accepted the fix")
		}
	}

	// NSE busy (299) for the whole request: nothing is marked sent, and the
	// next run sends it.
	if _, err := docs.Update(ctx, o.Docs, T, cClient, "TC001", "test", func(c *Client) error { c.Mobile = "9876500000"; return nil }); err != nil {
		t.Fatal(err)
	}
	f.refuse = "299"
	res, _ = o.NSEUCISend(ctx, T, "priya.nair", nil)
	if res.Sent != 0 || len(res.Problems) == 0 {
		t.Fatalf("busy %+v", res)
	}
	v, _ = o.NSEUCI(ctx, T, "priya.nair")
	for _, r := range v.Clients {
		if r.UCC == "TC001" && (r.Status != nseUCINotSent || !strings.Contains(r.Reason, "tried again")) {
			t.Fatalf("busy row %+v", r)
		}
	}
	if v.Connection != "Problem" {
		t.Fatalf("connection %s", v.Connection)
	}
	f.refuse = ""
	res, _ = o.NSEUCISend(ctx, T, "priya.nair", nil)
	if res.Sent != 1 || res.Accepted != 1 {
		t.Fatalf("retry %+v", res)
	}
}

func TestNSEUCIBatches(t *testing.T) {
	o, f, _ := nseUCISetup(t)
	ctx := t.Context()
	for i := 0; i < 27; i++ {
		nseUCIAddClient(t, o, nseUCITestClient(fmt.Sprintf("TB%03d", i), fmt.Sprintf("ABCPB%04dK", i)), true)
	}
	res, err := o.NSEUCISend(ctx, T, "priya.nair", nil)
	if err != nil || res.Sent != 27 || res.Accepted != 27 {
		t.Fatalf("%+v %v", res, err)
	}
	total := 0
	for _, u := range f.uploads {
		if len(u) > 50 {
			t.Fatalf("a call with %d records", len(u))
		}
		total += len(u)
		// A client's segments travel together.
		if u[len(u)-1]["ccdSegInd"] != "F" {
			t.Fatal("a client split across calls")
		}
	}
	if len(f.uploads) != 2 || total != 54 || f.logins != 1 {
		t.Fatalf("%d calls, %d records, %d logins", len(f.uploads), total, f.logins)
	}
}

func TestNSEUCIPANStatus(t *testing.T) {
	o, f, _ := nseUCISetup(t)
	ctx := t.Context()
	nseUCIAddClient(t, o, nseUCITestClient("TC001", "ABCPV1234K"), true)
	nseUCIAddClient(t, o, nseUCITestClient("TC002", "ABCPV1235K"), true)
	if _, err := o.NSEUCISend(ctx, T, "priya.nair", nil); err != nil {
		t.Fatal(err)
	}
	f.pan["TC001"], f.pan["TC002"] = "A", "I"
	if _, err := o.NSEUCIPANStatus(ctx, T, "anita.rao", nil); !errors.Is(err, ErrForbidden) {
		t.Fatalf("risk manager checked PAN status: %v", err)
	}
	res, err := o.NSEUCIPANStatus(ctx, T, "vikram.shah", nil)
	if err != nil || res.Sent != 2 || res.Accepted != 1 || res.Rejected != 1 || f.panCalls != 1 {
		t.Fatalf("%+v %v (calls %d)", res, err, f.panCalls)
	}
	v, _ := o.NSEUCI(ctx, T, "vikram.shah")
	rows := map[string]NSEUCIClientRow{}
	for _, r := range v.Clients {
		rows[r.UCC] = r
	}
	if rows["TC001"].PANStatus != "Approved" || !strings.HasPrefix(rows["TC002"].PANStatus, "Incorrect PAN · not permitted to trade") {
		t.Fatalf("rows %+v", rows)
	}
	ws, _ := docs.List[WorkItem](ctx, o.Docs, T, cWorkItem, "")
	roles := map[string]string{}
	for _, w := range ws {
		if w.UCC == "TC002" && w.Status == "Open" {
			roles[w.Key] = w.Role
		}
	}
	if roles["nseuci:pan-TC002"] != RoleMaker || roles["nseuci:trade-TC002"] != RoleCompliance {
		t.Fatalf("work items %v", roles)
	}
	// By default only clients not yet approved are asked again.
	res, _ = o.NSEUCIPANStatus(ctx, T, "priya.nair", nil)
	if res.Sent != 1 {
		t.Fatalf("second check asked %d", res.Sent)
	}
	// A client NSE does not know yet is reported.
	res, _ = o.NSEUCIPANStatus(ctx, T, "priya.nair", []string{"TC009"})
	if len(res.Problems) != 1 || !strings.Contains(res.Problems[0], "no record") {
		t.Fatalf("unknown %+v", res)
	}
}

func TestNSEUCITick(t *testing.T) {
	o, f, day := nseUCISetup(t)
	ctx := t.Context()
	nseUCIAddClient(t, o, nseUCITestClient("TC001", "ABCPV1234K"), true)
	f.pan["TC001"] = "P"
	*day = time.Date(2026, 10, 30, 22, 30, 0, 0, ist)
	o.nseUCITick(ctx, T, *day)
	if len(f.uploads) != 0 {
		t.Fatal("sent after 22:00")
	}
	*day = time.Date(2026, 10, 31, 9, 0, 0, 0, ist)
	o.nseUCITick(ctx, T, *day)
	if len(f.uploads) != 1 || f.dateCalls != 2 || f.panCalls != 1 { // by date: one call per segment (C, F)
		t.Fatalf("uploads %d, PAN by date %d, by UCC %d", len(f.uploads), f.dateCalls, f.panCalls)
	}
	nseUCIAddClient(t, o, nseUCITestClient("TC002", "ABCPV1235K"), true)
	*day = day.Add(5 * time.Minute)
	o.nseUCITick(ctx, T, *day)
	if len(f.uploads) != 1 {
		t.Fatal("sent again within 15 minutes")
	}
	*day = day.Add(15 * time.Minute)
	o.nseUCITick(ctx, T, *day)
	if len(f.uploads) != 2 || f.dateCalls != 2 {
		t.Fatalf("uploads %d, PAN by date %d (once a day)", len(f.uploads), f.dateCalls)
	}
	// Off: nothing.
	cn, _, _ := docs.Get[Connection](ctx, o.Docs, T, cConn, connNSEUCI)
	cn.Values["send"] = "Off"
	_ = docs.Upsert(ctx, o.Docs, T, cConn, connNSEUCI, cn, "test")
	nseUCIAddClient(t, o, nseUCITestClient("TC003", "ABCPV1236K"), true)
	*day = day.Add(time.Hour)
	o.nseUCITick(ctx, T, *day)
	if len(f.uploads) != 2 {
		t.Fatal("sent with automatic sending off")
	}
}

func TestNSEUCILoginRefused(t *testing.T) {
	o, f, _ := nseUCISetup(t)
	ctx := t.Context()
	nseUCIAddClient(t, o, nseUCITestClient("TC001", "ABCPV1234K"), true)
	cn, _, _ := docs.Get[Connection](ctx, o.Docs, T, cConn, connNSEUCI)
	cn.Secrets["password"] = "wrong"
	_ = docs.Upsert(ctx, o.Docs, T, cConn, connNSEUCI, cn, "test")
	res, err := o.NSEUCISend(ctx, T, "priya.nair", nil)
	if err != nil || res.Sent != 0 || len(res.Problems) != 1 || !strings.Contains(res.Problems[0], "did not accept the UCC API login") || len(f.uploads) != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	v, _ := o.NSEUCI(ctx, T, "priya.nair")
	if v.Clients[0].Status != nseUCINotSent || v.Connection != "Problem" {
		t.Fatalf("%+v", v)
	}
}
