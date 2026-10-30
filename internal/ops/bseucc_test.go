package ops

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

func TestBSEUCCPasswordHeader(t *testing.T) {
	// The notice of 23 Jan 2026: MD5 of the password, then SHA-512 of that.
	// Worked out independently (Python hashlib) for "Password@111".
	cases := map[string]string{
		bseHexLower: "0508f738ceaa8972141b4a4c4dd4ba70dea5f532a541673d5e81ea8da90e0664f7a0fcfcfce7d611fc5131d5eab70489303b77309f8ee60042e6b9756385d8dd",
		bseHexUpper: "1E6CF999F47765377362C99330A2DBF4CDC5A1895A6BD7F72A5CD6B0D53FDED1ECE5C8B7BCCBE41CA60F998D6A8F5DDEE9E31555772B8A74B5D29D1FD0F0F19D",
		bseBase64:   "9/UoVb2uxFf3ssb8zCs03G8NSeB7HZemeUrun3/krOUbbRjxHldCtPB+ssdbmrAl6jPdo2tK2nMObD/Xgx0/dQ==",
	}
	for enc, want := range cases {
		if got := bseUCCPassword("Password@111", bseMD5SHA, enc); got != want {
			t.Errorf("%s: %s", enc, got)
		}
	}
	if got := bseUCCPassword("Password@111", bseMD5Only, bseHexLower); got != "509002f53bdb3d448900a819c720a8a2" {
		t.Errorf("md5 only: %s", got)
	}
}

func testBSEKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func pemPKIX(t *testing.T, k *rsa.PrivateKey) string {
	der, err := x509.MarshalPKIXPublicKey(&k.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

func TestBSEPublicKeyForms(t *testing.T) {
	k := testBSEKey(t)
	der, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
	pk1 := x509.MarshalPKCS1PublicKey(&k.PublicKey)
	xmlKey := "<RSAKeyValue><Modulus>" + base64.StdEncoding.EncodeToString(k.N.Bytes()) + "</Modulus><Exponent>AQAB</Exponent></RSAKeyValue>"
	for name, s := range map[string]string{
		"pem pkix":   pemPKIX(t, k),
		"pem pkcs1":  string(pem.EncodeToMemory(&pem.Block{Type: "RSA PUBLIC KEY", Bytes: pk1})),
		"base64 der": base64.StdEncoding.EncodeToString(der),
		"wrapped":    strings.Join([]string{base64.StdEncoding.EncodeToString(der)[:64], base64.StdEncoding.EncodeToString(der)[64:]}, "\n"),
		"xml":        xmlKey,
	} {
		got, err := parseBSEPublicKey(s)
		if err != nil || got.N.Cmp(k.N) != 0 || got.E != k.E {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, s := range []string{"", "not a key", "<RSAKeyValue><Modulus>AQAB</Modulus><Exponent>AQAB</Exponent></RSAKeyValue>"} {
		if _, err := parseBSEPublicKey(s); err == nil {
			t.Errorf("%q accepted", s)
		}
	}
	// The checksum decrypts with BSE's private key to the body's SHA-256 hex.
	body := []byte(`{"a":"b"}`)
	sum, err := bseUCCChecksum(&k.PublicKey, body, false)
	if err != nil {
		t.Fatal(err)
	}
	ct, _ := base64.StdEncoding.DecodeString(sum)
	pt, err := rsa.DecryptPKCS1v15(nil, k, ct)
	h := sha256.Sum256(body)
	if err != nil || string(pt) != hex.EncodeToString(h[:]) {
		t.Fatalf("checksum decrypts to %q (%v)", pt, err)
	}
	// BSE's header names are case-sensitive: sent exactly as written.
	x := bseUCCConn{base: "https://example.invalid", member: "6543", user: "LLAPI", key: &k.PublicKey, pwHash: bseMD5SHA, enc: bseHexLower}
	req, err := x.request(context.Background(), "AddUCCData_V5", body, "Password@111")
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"MemberCode", "UserID", "Password", "Checksum", "Content-Type"} {
		if len(req.Header[h]) != 1 {
			t.Errorf("header %s not set as written: %v", h, req.Header)
		}
	}
	if req.Header["Password"][0] != bseUCCPassword("Password@111", bseMD5SHA, bseHexLower) || req.URL.String() != "https://example.invalid/AddUCCData_V5" {
		t.Errorf("request %v %s", req.Header, req.URL)
	}
	sum2, _ := bseUCCChecksum(&k.PublicKey, body, true)
	ct, _ = base64.StdEncoding.DecodeString(sum2)
	if pt, _ := rsa.DecryptPKCS1v15(nil, k, ct); string(pt) != strings.ToUpper(hex.EncodeToString(h[:])) {
		t.Fatalf("upper checksum %q", pt)
	}
}

// bseClient is a complete individual client ready for BSE.
func bseClient(ucc, pan string, now time.Time) Client {
	return Client{UCC: ucc, Name: "Asha Ramesh Kulkarni", Type: "Individual", PAN: pan, DOB: "1988-04-12", Mobile: "9876543210", Email: strings.ToLower(ucc) + "@example.in",
		City: "Pune", State: "Maharashtra", Status: "UCC upload", Segments: []string{"CM", "FO"}, Income: "10-25 L", RAS: "Quarterly", Risk: "Medium",
		Bank: BankAccount{Bank: "HDFC Bank, Ltd", IFSC: "HDFC0001234", Account: "50100012345678"}, Demat: Demat{Depository: "CDSL", ID: "1208160012345678"},
		Nominee: "Opted out", KYCDate: now.AddDate(0, 0, -3).Format("2006-01-02"), IncomeProof: true, RiskDisclosure: true}
}

func putBSEClient(t *testing.T, o *Ops, ctx context.Context, c Client, addr, pin string) {
	t.Helper()
	if err := docs.Upsert(ctx, o.Docs, T, cClient, c.UCC, c, "priya.nair"); err != nil {
		t.Fatal(err)
	}
	if addr != "" {
		p := mfProfileFrom(c)
		p.Address, p.Pincode = addr, pin
		if err := docs.Upsert(ctx, o.Docs, T, cMFProf, c.UCC, p, "priya.nair"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBSEUCCMapping(t *testing.T) {
	now := time.Date(2026, 10, 30, 11, 0, 0, 0, ist)
	src := bseUCCSources{apps: map[string]Application{}, mf: map[string]MFProfile{"BSE01": {Address: "Flat 4, Shanti Niwas, FC Road", Pincode: "411004"}}, nri: map[string]NRIDetails{}}
	c := bseClient("BSE01", "ABCPK1234F", now)
	c.UPIBlock = money.Amount(500000)
	r := bseUCCMap(c, src, "Add", nil, now)
	if len(r.Problems) != 0 {
		t.Fatalf("problems %v", r.Problems)
	}
	want := map[string]string{"TRANSACTIONCODE": "N", "CLIENTTYPE": "I", "STATUS": "CL", "CATEGORY": "I", "CLIENTCODE": "BSE01", "PANNO": "ABCPK1234F",
		"POLITICALEXPERSON": "N", "ADDRESS1": "Flat 4, Shanti Niwas, FC Road", "PERMNEQUALCORP": "Y", "COUNTRY": "INDIA", "STATE": "MAHARASHTRA", "CITY": "PUNE",
		"PINCODE": "411004", "TYPEOFSERVICE": "3", "CONTACTDETAILS": "2", "MOBILENUMBER": "9876543210", "DEPOSITORYNAME1": "CDSL", "DEMANTID1": "",
		"DEPOSITORYPARTICIPANT1": "12081600", "BENEFICIALOWNACNTNO1": "1208160012345678", "BANKNAME1": "HDFC Bank Ltd", "ACCOUNTNO1": "50100012345678",
		"BANKBRANCHIFSCCODE1": "HDFC0001234", "PRIMARYORSECONDARYBANK1": "P", "PRIMARYORSECONDARYDP1": "P", "PROVIDEDETAILS": "1", "INCOME": "4",
		"INCOMEDATE": "27/10/2026", "CLIENTAGGREMENTDATE": "27/10/2026", "ISACTIVE": "Y", "FIRSTNAME": "ASHA", "MIDDLENAME": "RAMESH", "LASTNAME": "KULKARNI",
		"DATEOFBIRTH": "12/04/1988", "CLIENTNAME": "ASHA RAMESH KULKARNI", "CASH": "Y", "EQUITY_DERIVATIVE": "Y", "CURRENCY": "N", "COMMDERIVATIVES": "N",
		"SLB": "N", "DEBT": "N", "EGR": "N", "ISPOA": "N", "OPTEDFORNOMINATION": "N", "OPTED_FOR_UPI": "Y", "WHETHERCORPORATE": "N", "NUMBEROFDIRECTORS": "0",
		"AADHARCARDNO": "", "UPDATEREASON": ""}
	for k, x := range want {
		if r.Values[k] != x {
			t.Errorf("%s = %q, want %q", k, r.Values[k], x)
		}
	}
	// The JSON is one line, nulls for empty values, in BSE's sample order.
	j := string(r.JSON())
	if !strings.HasPrefix(j, `{"CLIENTDETAILS_V5":[{"TRANSACTIONCODE":"N","CLIENTTYPE":"I","STATUS":"CL","CATEGORY":"I","CLIENTCODE":"BSE01"`) ||
		!strings.Contains(j, `"CONTACTPERSONMOBILE2":null,"SERVERIP":null,"BATUSER":null,"CASH":"Y"`) || !strings.HasSuffix(j, `"CLIENTNAMEDESCRIPTION":null,"DIRECTORDETAILS":null}]}`) ||
		strings.ContainsAny(j, "\n\t") {
		t.Fatalf("json %s", j)
	}
	var parsed map[string][]map[string]any
	if err := json.Unmarshal(r.JSON(), &parsed); err != nil || len(parsed["CLIENTDETAILS_V5"][0]) != 138 {
		t.Fatalf("json fields %d %v", len(parsed["CLIENTDETAILS_V5"][0]), err)
	}
	if m := string(r.Masked().JSON()); !strings.Contains(m, `"ACCOUNTNO1":"XXXXXXXXXX5678"`) {
		t.Fatalf("masked %s", m)
	}
	// The hash ignores Add/Modify.
	if bseUCCMap(c, src, "Modify", nil, now).Hash() != r.Hash() {
		t.Fatal("hash depends on the transaction code")
	}

	// Problems in plain English instead of bad data.
	bad := c
	bad.UCC, bad.Name, bad.State, bad.DOB, bad.KYCDate = "BSE02", "Rhea D'Souza", "Bombay", "", now.AddDate(-2, 0, 0).Format("2006-01-02")
	bad.Demat = Demat{Depository: "NSDL", ID: "IN30021412345678"}
	r = bseUCCMap(bad, src, "Add", nil, now)
	all := strings.Join(r.Problems, " | ")
	for _, w := range []string{"no address", "not an Indian state", "date of birth is missing", "more than a year ago", "The name has a character"} {
		if !strings.Contains(all, w) {
			t.Errorf("missing problem %q in %s", w, all)
		}
	}
	if r.Values["DEPOSITORYNAME1"] != "NSDL" || r.Values["DEMANTID1"] != "IN300214" || r.Values["BENEFICIALOWNACNTNO1"] != "12345678" {
		t.Errorf("nsdl %v", r.Values)
	}
	for typ, w := range map[string]string{"Corporate": "body corporate", "HUF": "HUF", "NRI": "NRE or NRO"} {
		x := c
		x.Type = typ
		r := bseUCCMap(x, src, "Add", nil, now)
		if !strings.Contains(strings.Join(r.Problems, " "), w) {
			t.Errorf("%s: %v", typ, r.Problems)
		}
	}
	src.nri["BSE01"] = NRIDetails{Account: "NRO", CPCode: "cp123"}
	x := c
	x.Type = "NRI"
	if r := bseUCCMap(x, src, "Add", nil, now); len(r.Problems) != 0 || r.Values["CATEGORY"] != "NRO" || r.Values["FNOCPCODE"] != "CP123" {
		t.Errorf("nri %v %v", r.Problems, r.Values["CATEGORY"])
	}
	// Segment statuses, inactivity and the reason BSE wants for them.
	last := bseUCCMap(c, src, "Add", nil, now).Summary
	y := c
	y.Status = "Active"
	if err := applySegmentStatus(&y, "FO", SegSuspended, now.Format("2006-01-02"), "income proof lapsed", "farhan.khan", "Ledgerline", now); err != nil {
		t.Fatal(err)
	}
	r = bseUCCMap(y, src, "Modify", last, now)
	if r.Values["EQUITY_DERIVATIVE"] != "N" || r.Values["CASH"] != "Y" || r.Values["ISACTIVE"] != "Y" || r.Values["UPDATEREASON"] != "Segment status FO Suspended" {
		t.Errorf("suspended FO: %q %q %q", r.Values["EQUITY_DERIVATIVE"], r.Values["ISACTIVE"], r.Values["UPDATEREASON"])
	}
	y.Inactive, y.InactiveSince = true, "2026-10-01"
	r = bseUCCMap(y, src, "Modify", last, now)
	if r.Values["ISACTIVE"] != "N" || !strings.Contains(r.Values["UPDATEREASON"], "Inactive under exchange inactivity rules since 01/10/2026") {
		t.Errorf("inactive: %q %q", r.Values["ISACTIVE"], r.Values["UPDATEREASON"])
	}
	z := c
	z.UPIBlock = 0
	if r := bseUCCMap(z, src, "Modify", last, now); r.Values["OPTED_FOR_UPI"] != "D" {
		t.Errorf("UPI deregistered after Y: %q", r.Values["OPTED_FOR_UPI"])
	}
	if r := bseUCCMap(z, src, "Add", nil, now); r.Values["OPTED_FOR_UPI"] != "N" {
		t.Errorf("UPI not opted: %q", r.Values["OPTED_FOR_UPI"])
	}
	// Error messages in English.
	if s, auth := BSEUCCError("Invalid UserID or Password"); !auth || !strings.Contains(s, "password") {
		t.Errorf("auth %q", s)
	}
	if s, auth := BSEUCCError("INVALID PAN"); auth || !strings.Contains(s, "PAN is not valid") || !strings.Contains(s, "(BSE: INVALID PAN)") {
		t.Errorf("pan %q", s)
	}
	if s, _ := BSEUCCError("Something new"); s != "BSE rejected it: Something new" {
		t.Errorf("other %q", s)
	}
}

// fakeBSE is BSE's UCC service: it checks the headers and checksum like
// BSE, and answers each client as told.
type fakeBSE struct {
	t        *testing.T
	key      *rsa.PrivateKey
	password string
	mu       sync.Mutex
	adds     []map[string]any
	pans     []map[string]string
	answer   func(code string, rec map[string]any) (string, string) // status, error
	panList  string
	resets   int
}

func (f *fakeBSE) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	w.Header().Set("Content-Type", "application/json")
	ct, err := base64.StdEncoding.DecodeString(r.Header.Get("Checksum"))
	if err != nil {
		w.Write([]byte(`{"CLIENTCODE":"","STATUS":"R","ERRORRESPONSE":"ErrorCode : 203 Please Contact Administrator."}`))
		return
	}
	pt, err := rsa.DecryptPKCS1v15(nil, f.key, ct)
	h := sha256.Sum256(body)
	if err != nil || string(pt) != hex.EncodeToString(h[:]) {
		w.Write([]byte(`{"CLIENTCODE":"","STATUS":"R","ERRORRESPONSE":"Request data is invalid"}`))
		return
	}
	if r.Header.Get("MemberCode") != "6543" || r.Header.Get("UserID") != "LLAPI" || r.Header.Get("Password") != bseUCCPassword(f.password, bseMD5SHA, bseHexLower) ||
		r.Header.Get("Content-Type") != "application/json" {
		if strings.HasSuffix(r.URL.Path, "/PANStatus") {
			w.Write([]byte(`{"ResponceListPAN":null,"ErrorResponceListPAN":[{"REQUEST":"","STATUS":"R","ERRORRESPONSE":"Invalid UserName/Password"}]}`))
			return
		}
		w.Write([]byte(`{"CLIENTCODE":"","STATUS":"R","ERRORRESPONSE":"Invalid UserID or Password"}`))
		return
	}
	switch r.URL.Path {
	case "/UCCService.svc/AddUCCData_V5":
		var req map[string][]map[string]any
		if err := json.Unmarshal(body, &req); err != nil || len(req["CLIENTDETAILS_V5"]) != 1 {
			f.t.Errorf("add body %s", body)
			return
		}
		rec := req["CLIENTDETAILS_V5"][0]
		f.adds = append(f.adds, rec)
		code := fmt.Sprint(rec["CLIENTCODE"])
		st, e := f.answer(code, rec)
		json.NewEncoder(w).Encode(map[string]string{"CLIENTCODE": code, "STATUS": st, "ERRORRESPONSE": e})
	case "/UCCService.svc/PANStatus":
		var req map[string]string
		json.Unmarshal(body, &req)
		f.pans = append(f.pans, req)
		w.Write([]byte(f.panList))
	case "/UCCService.svc/ResetPassword":
		var req map[string]string
		json.Unmarshal(body, &req)
		if req["OldPassword"] != f.password {
			w.Write([]byte(`{"CLIENTCODE":"","STATUS":"R","ERRORRESPONSE":"Invalid USER ID / PASSWORD."}`))
			return
		}
		f.password = req["NewPassword"]
		f.resets++
		w.Write([]byte(`{"CLIENTCODE":"","STATUS":"S","ERRORRESPONSE":"Password has been changed successfully."}`))
	default:
		w.WriteHeader(404)
	}
}

func bseSetup(t *testing.T) (*Ops, context.Context, *fakeBSE, *time.Time) {
	t.Helper()
	o, ctx := newOps(t)
	now := time.Date(2026, 10, 30, 11, 0, 0, 0, ist)
	o.Now = func() time.Time { return now }
	k := testBSEKey(t)
	f := &fakeBSE{t: t, key: k, password: "Bse@pass1", answer: func(string, map[string]any) (string, string) { return "S", "" },
		panList: `{"ResponceListPAN":[],"ErrorResponceListPAN":null}`}
	srv := httptest.NewTLSServer(http.HandlerFunc(f.handler))
	t.Cleanup(srv.Close)
	old := bseUCCHTTP
	bseUCCHTTP = srv.Client()
	t.Cleanup(func() { bseUCCHTTP = old })
	if _, err := o.SaveConnection(ctx, T, "admin", connBSEUCC, map[string]string{"member": "6543", "user": "LLAPI", "env": "UAT", "send": "On", "url": srv.URL + "/UCCService.svc"},
		map[string]string{"password": "Bse@pass1", "public_key": pemPKIX(t, k)}); err != nil {
		t.Fatal(err)
	}
	return o, ctx, f, &now
}

func bseReg(t *testing.T, o *Ops, ctx context.Context, ucc string) BSEUCCReg {
	t.Helper()
	r, _, err := docs.Get[BSEUCCReg](ctx, o.Docs, T, cBSEUCCReg, "BSE|"+ucc)
	if err != nil {
		t.Fatalf("%s: %v", ucc, err)
	}
	return r
}

func TestBSEUCCSend(t *testing.T) {
	o, ctx, f, now := bseSetup(t)
	putBSEClient(t, o, ctx, bseClient("BSEA1", "ABCPA1111A", *now), "12 MG Road", "411001")
	putBSEClient(t, o, ctx, bseClient("BSEB2", "ABCPB2222B", *now), "14 MG Road", "411001")
	putBSEClient(t, o, ctx, bseClient("BSEC3", "ABCPC3333C", *now), "", "") // no address
	old := bseClient("BSEOLD", "ABCPD4444D", *now)
	old.Status = "Active"
	putBSEClient(t, o, ctx, old, "1 Old Road", "411001")
	f.answer = func(code string, _ map[string]any) (string, string) {
		if code == "BSEB2" {
			return "R", "INVALID PAN"
		}
		return "S", ""
	}

	if _, err := o.BSEUCCSend(ctx, T, "anita.rao", nil); !errors.Is(err, ErrForbidden) {
		t.Fatalf("risk manager sent UCC: %v", err)
	}
	res, err := o.BSEUCCSend(ctx, T, "priya.nair", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Sent != 2 || res.Accepted != 1 || res.Rejected != 1 || len(f.adds) != 2 {
		t.Fatalf("send %+v (%d adds)", res, len(f.adds))
	}
	if !strings.Contains(strings.Join(res.Problems, " "), "BSEC3: There is no address") || !strings.Contains(strings.Join(res.Problems, " "), "BSEB2: BSE says the PAN is not valid") {
		t.Fatalf("problems %v", res.Problems)
	}
	if r := bseReg(t, o, ctx, "BSEA1"); r.Status != UCCAccepted || r.Action != "Add" || r.AcceptedHash == "" || r.ResultAt == "" || len(r.History) != 1 {
		t.Fatalf("A %+v", r)
	}
	if r := bseReg(t, o, ctx, "BSEB2"); r.Status != UCCRejected || !strings.Contains(r.Reason, "PAN is not valid") {
		t.Fatalf("B %+v", r)
	}
	if r := bseReg(t, o, ctx, "BSEC3"); r.Status != UCCNeedsFixing || len(r.Problems) == 0 {
		t.Fatalf("C %+v", r)
	}
	// The active client is taken as registered before; nothing sent for it.
	if r := bseReg(t, o, ctx, "BSEOLD"); r.Status != UCCAccepted || r.Via != "Before Ledgerline" {
		t.Fatalf("old %+v", r)
	}
	for _, a := range f.adds {
		if a["CLIENTCODE"] == "BSEOLD" {
			t.Fatal("the registered client was sent")
		}
	}
	// Nothing is sent again while nothing changed.
	res, err = o.BSEUCCSend(ctx, T, "farhan.khan", nil)
	if err != nil || res.Sent != 0 || len(f.adds) != 2 {
		t.Fatalf("resend %+v %v", res, err)
	}
	if res, _ := o.BSEUCCSend(ctx, T, "priya.nair", []string{"BSEA1"}); res.Sent != 0 || !strings.Contains(res.Problems[0], "nothing to send") {
		t.Fatalf("named %+v", res)
	}

	// An approved change to an accepted client goes as a Modify; the PAN fix
	// goes again; the old client's segment change goes with its reason.
	if _, err := docs.Update(ctx, o.Docs, T, cClient, "BSEA1", "farhan.khan", func(c *Client) error { c.Email = "asha.new@example.in"; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := docs.Update(ctx, o.Docs, T, cClient, "BSEB2", "farhan.khan", func(c *Client) error { c.PAN = "ABCPB2223B"; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := o.SetSegmentStatus(ctx, T, "farhan.khan", "BSEOLD", "FO", SegSuspended, now.Format("2006-01-02"), "income proof lapsed"); err != nil {
		t.Fatal(err)
	}
	v, err := o.BSEUCCView(ctx, T, "priya.nair")
	if err != nil {
		t.Fatal(err)
	}
	if v.Exchange != "BSE" || v.Connection != "Saved" && v.Connection != "Working" || v.Live || v.Counts[UCCNotSent] != 3 || v.Counts[UCCNeedsFixing] != 1 {
		t.Fatalf("view %+v", v)
	}
	f.adds = nil
	f.answer = func(string, map[string]any) (string, string) { return "S", "" }
	res, err = o.BSEUCCSend(ctx, T, SystemActor, nil)
	if err != nil || res.Sent != 3 || res.Accepted != 3 {
		t.Fatalf("modify %+v %v", res, err)
	}
	got := map[string]map[string]any{}
	for _, a := range f.adds {
		got[fmt.Sprint(a["CLIENTCODE"])] = a
	}
	if got["BSEA1"]["TRANSACTIONCODE"] != "M" || got["BSEA1"]["EMAIL"] != "asha.new@example.in" || got["BSEB2"]["TRANSACTIONCODE"] != "N" {
		t.Fatalf("modify bodies %v %v", got["BSEA1"]["TRANSACTIONCODE"], got["BSEB2"]["TRANSACTIONCODE"])
	}
	if got["BSEOLD"]["TRANSACTIONCODE"] != "M" || got["BSEOLD"]["EQUITY_DERIVATIVE"] != "N" || got["BSEOLD"]["UPDATEREASON"] != "Segment status FO Suspended" {
		t.Fatalf("segment change %v", got["BSEOLD"])
	}
	if r := bseReg(t, o, ctx, "BSEA1"); r.Action != "Modify" || r.Status != UCCAccepted || len(r.History) != 2 {
		t.Fatalf("A after modify %+v", r)
	}

	// Each run is audited: account numbers masked, no password or checksum.
	runs, _ := docs.List[BSEUCCRun](ctx, o.Docs, T, cBSEUCCRun, "")
	if len(runs) < 2 {
		t.Fatalf("runs %d", len(runs))
	}
	raw, _ := json.Marshal(runs)
	for _, s := range []string{"50100012345678", "Bse@pass1", bseUCCPassword("Bse@pass1", bseMD5SHA, bseHexLower)} {
		if strings.Contains(string(raw), s) {
			t.Fatalf("audit holds %q", s)
		}
	}
	if !strings.Contains(string(raw), "XXXXXXXXXX5678") || !strings.Contains(string(raw), `\"CLIENTCODE\":\"BSEA1\"`) {
		t.Fatal("audit lacks the masked request")
	}
	if v, _ := o.BSEUCCView(ctx, T, "vikram.shah"); len(v.Runs) < 2 || v.Runs[0].Sent != 3 {
		t.Fatalf("runs on screen %+v", v.Runs)
	}

	// "Already exists" to an Add turns it into a Modify on the next run.
	putBSEClient(t, o, ctx, bseClient("BSEE5", "ABCPE5555E", *now), "5 Lane", "411002")
	f.answer = func(code string, rec map[string]any) (string, string) {
		if code == "BSEE5" && rec["TRANSACTIONCODE"] == "N" {
			return "R", "CLIENT CODE ALREADY EXIST"
		}
		return "S", ""
	}
	f.adds = nil
	o.BSEUCCSend(ctx, T, "priya.nair", nil)
	res, _ = o.BSEUCCSend(ctx, T, "priya.nair", nil)
	if len(f.adds) != 2 || f.adds[1]["TRANSACTIONCODE"] != "M" || res.Accepted != 1 {
		t.Fatalf("already exists: %d adds, %+v", len(f.adds), res)
	}

	// Preview: the masked record and problems.
	p, err := o.BSEUCCPreview(ctx, T, "priya.nair", "BSEC3")
	if err != nil || p.Action != "Add" || p.Status != UCCNeedsFixing || len(p.Problems) == 0 || !strings.Contains(string(p.Payload), `"CLIENTCODE":"BSEC3"`) || strings.Contains(string(p.Payload), "50100012345678") {
		t.Fatalf("preview %+v %v", p, err)
	}
}

func TestBSEUCCAuthFailureStops(t *testing.T) {
	o, ctx, f, now := bseSetup(t)
	putBSEClient(t, o, ctx, bseClient("BSEA1", "ABCPA1111A", *now), "12 MG Road", "411001")
	putBSEClient(t, o, ctx, bseClient("BSEB2", "ABCPB2222B", *now), "14 MG Road", "411001")
	f.password = "changed-at-bse"
	res, err := o.BSEUCCSend(ctx, T, "priya.nair", nil)
	if err != nil || res.Sent != 0 || len(f.adds) != 0 || !strings.Contains(strings.Join(res.Problems, " "), "Stopped") {
		t.Fatalf("%+v %v %d", res, err, len(f.adds))
	}
	cn, _, _ := docs.Get[Connection](ctx, o.Docs, T, cConn, connBSEUCC)
	if cn.Status != "Problem" {
		t.Fatalf("connection %s", cn.Status)
	}
	if r := bseReg(t, o, ctx, "BSEA1"); r.Status != UCCNotSent {
		t.Fatalf("A %+v", r)
	}
	// The scheduler leaves it alone until the Admin saves the connection.
	*now = now.Add(time.Hour)
	o.bseUCCTick(ctx, T, *now)
	if len(f.adds) != 0 {
		t.Fatal("ticked after the login was refused")
	}
}

func TestBSEUCCPANStatusAndPassword(t *testing.T) {
	o, ctx, f, now := bseSetup(t)
	putBSEClient(t, o, ctx, bseClient("BSEA1", "ABCPA1111A", *now), "12 MG Road", "411001")
	putBSEClient(t, o, ctx, bseClient("BSEB2", "ABCPB2222B", *now), "14 MG Road", "411001")
	if res, err := o.BSEUCCSend(ctx, T, "priya.nair", nil); err != nil || res.Accepted != 2 {
		t.Fatalf("%+v %v", res, err)
	}
	f.panList = `{"ResponceListPAN":[{"CLIENTCODE":"BSEA1","PANNO":"ABCPA1111A","CREATEDON":"2026-10-30 11:00:07","CLIENTNAME":"ASHA RAMESH KULKARNI","PANNAME":"ASHA RAMESH KULKARNI","NAME_ON_PANCARD":"","PANSTATUS":"V","PANVALIDATIONON":"2026-10-30","AADHAAR_SEED_FLAG":"Y"},
		{"CLIENTCODE":"BSEB2","PANNO":"ABCPB2222B","CREATEDON":"2026-10-30 11:00:08","CLIENTNAME":"ASHA RAMESH KULKARNI","PANNAME":"ASHA R KULKARNI","NAME_ON_PANCARD":"","PANSTATUS":"I","PANVALIDATIONON":"2026-10-30","AADHAAR_SEED_FLAG":"R"}],"ErrorResponceListPAN":null}`
	res, err := o.BSEUCCPANStatus(ctx, T, "farhan.khan", nil)
	if err != nil || res.Sent != 2 || res.Accepted != 1 || res.Rejected != 1 {
		t.Fatalf("pan %+v %v", res, err)
	}
	if len(f.pans) != 1 || f.pans[0]["CreateDate"] != "30/10/2026" || f.pans[0]["UCCList"] != "BSEA1,BSEB2" || f.pans[0]["TimeFrom"] != "00:00" {
		t.Fatalf("pan request %v", f.pans)
	}
	if r := bseReg(t, o, ctx, "BSEB2"); r.PANStatus != "Invalid" || r.AadhaarSeed != "R" || r.PANCheckedOn != "2026-10-30" {
		t.Fatalf("B %+v", r)
	}
	ws, _ := o.WorkItems(ctx, T, "Open")
	keys := map[string]bool{}
	for _, w := range ws {
		keys[w.Key] = true
	}
	for _, k := range []string{"bseucc:pan:BSEB2", "bseucc:panname:BSEB2", "bseucc:seed:BSEB2"} {
		if !keys[k] {
			t.Errorf("no work item %s", k)
		}
	}
	if keys["bseucc:pan:BSEA1"] {
		t.Error("work item for a valid PAN")
	}
	// BSE's two-minute rule.
	if _, err := o.BSEUCCPANStatus(ctx, T, "farhan.khan", []string{"BSEA1"}); !errors.Is(err, ErrState) {
		t.Fatalf("second request within two minutes: %v", err)
	}
	*now = now.Add(5 * time.Minute)
	if res, _ := o.BSEUCCPANStatus(ctx, T, "farhan.khan", nil); res.Sent != 0 || len(f.pans) != 1 {
		t.Fatalf("asked again the same day %+v", res)
	}

	// Reset password: Admin only, BSE's rules, then the new one is used.
	if _, err := o.BSEUCCResetPassword(ctx, T, "priya.nair", "New@pass22"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker reset the password: %v", err)
	}
	if _, err := o.BSEUCCResetPassword(ctx, T, "admin", "weakpass"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("weak password: %v", err)
	}
	if _, err := o.BSEUCCResetPassword(ctx, T, "admin", "New@pass22"); err != nil || f.resets != 1 || f.password != "New@pass22" {
		t.Fatalf("reset %v %d", err, f.resets)
	}
	cn, _, _ := docs.Get[Connection](ctx, o.Docs, T, cConn, connBSEUCC)
	if cn.Secrets["password"] != "New@pass22" {
		t.Fatal("new password not kept")
	}
	putBSEClient(t, o, ctx, bseClient("BSEC3", "ABCPC3333C", *now), "3 Road", "411001")
	if res, err := o.BSEUCCSend(ctx, T, "priya.nair", nil); err != nil || res.Accepted != 1 {
		t.Fatalf("after reset %+v %v", res, err)
	}
	runs, _ := docs.List[BSEUCCRun](ctx, o.Docs, T, cBSEUCCRun, "")
	raw, _ := json.Marshal(runs)
	if strings.Contains(string(raw), "New@pass22") || strings.Contains(string(raw), "Bse@pass1") {
		t.Fatal("a password is in the audit")
	}
}

func TestBSEUCCTick(t *testing.T) {
	o, ctx, f, now := bseSetup(t)
	putBSEClient(t, o, ctx, bseClient("BSEA1", "ABCPA1111A", *now), "12 MG Road", "411001")
	*now = time.Date(2026, 10, 30, 7, 30, 0, 0, ist)
	o.bseUCCTick(ctx, T, *now)
	if len(f.adds) != 0 {
		t.Fatal("sent before 08:00")
	}
	*now = time.Date(2026, 10, 30, 8, 5, 0, 0, ist)
	o.bseUCCTick(ctx, T, *now)
	if len(f.adds) != 1 {
		t.Fatalf("not sent at 08:05: %d", len(f.adds))
	}
	putBSEClient(t, o, ctx, bseClient("BSEB2", "ABCPB2222B", *now), "14 MG Road", "411001")
	*now = now.Add(5 * time.Minute)
	o.bseUCCTick(ctx, T, *now)
	if len(f.adds) != 1 {
		t.Fatal("sent within 15 minutes")
	}
	*now = now.Add(11 * time.Minute)
	o.bseUCCTick(ctx, T, *now)
	if len(f.adds) != 2 || len(f.pans) == 0 {
		t.Fatalf("after 15 minutes: %d adds, %d pan requests", len(f.adds), len(f.pans))
	}
	// Send Off: nothing automatic.
	if _, err := o.SaveConnection(ctx, T, "admin", connBSEUCC, map[string]string{"member": "6543", "user": "LLAPI", "env": "UAT", "send": "Off", "url": "https://example.invalid/UCCService.svc"}, nil); err != nil {
		t.Fatal(err)
	}
	putBSEClient(t, o, ctx, bseClient("BSEC3", "ABCPC3333C", *now), "3 Road", "411001")
	*now = now.Add(time.Hour)
	o.bseUCCTick(ctx, T, *now)
	if len(f.adds) != 2 {
		t.Fatal("sent with sending off")
	}
	// The password reminder from day 25.
	*now = now.AddDate(0, 0, 26)
	o.bseUCCTick(ctx, T, *now)
	ws, _ := o.WorkItems(ctx, T, "Open")
	found := false
	for _, w := range ws {
		found = found || w.Key == "bseucc:password" && w.Role == RoleAdmin
	}
	if !found {
		t.Fatal("no password reminder")
	}
}

func TestBSEUCCBatchFile(t *testing.T) {
	o, ctx, _, now := bseSetup(t)
	putBSEClient(t, o, ctx, bseClient("BSEA1", "ABCPA1111A", *now), "12 MG Road", "411001")
	putBSEClient(t, o, ctx, bseClient("BSEC3", "ABCPC3333C", *now), "", "")
	b, err := o.BSEUCCBatchFile(ctx, T, "priya.nair")
	if err != nil {
		t.Fatal(err)
	}
	if b.Name != "BSE_UCC_30102026.TXT" || len(b.Clients) != 1 || b.Clients[0] != "BSEA1" || len(b.Problems) != 1 || !strings.HasPrefix(b.Problems[0], "BSEC3: ") {
		t.Fatalf("batch %+v", b)
	}
	// Golden line: the 136 fields in the specification's order, CRLF.
	golden := "N||I|CL|I|BSEA1|ABCPA1111A|N|12 MG Road|Y|12 MG Road|INDIA|MAHARASHTRA|PUNE|411001|3|2|bsea1@example.in|9876543210|" + // 1-19
		"||||||CDSL||12081600|||||||" + // 20-34
		"HDFC Bank Ltd|50100012345678|||||27/10/2026|1|4|27/10/2026|||Y||ASHA|RAMESH|KULKARNI||12/04/1988|ASHA RAMESH KULKARNI|" + // 35-54
		"||||N||0|||||||||||||||" + // 55-75
		"Y|Y|N|N|N|N|N|N|||INDIA|MAHARASHTRA|PUNE|411001||||N|N|" + // 76-94
		"|||||||||||N|1208160012345678|||N|" + // 95-110
		"HDFC0001234|P|||||||||||||P|||||||||||" + // 111-136
		"\r\n"
	if string(b.Content) != golden {
		t.Fatalf("batch file\n got %q\nwant %q", b.Content, golden)
	}
	line := strings.TrimSuffix(string(b.Content), "\r\n")
	f := strings.Split(line, "|")
	if strings.Count(line, "|") != 135 || f[5] != "BSEA1" || f[25] != "CDSL" || f[106] != "1208160012345678" || f[109] != "N" || f[110] != "HDFC0001234" || f[124] != "P" {
		t.Fatalf("fields %d", len(f))
	}
	// The same file from the Exchange files screen.
	name, content, err := o.ExchangeFile(ctx, T, "priya.nair", "bse-ucc", "2026-10-30", XFileOptions{})
	if err != nil || name != b.Name || string(content) != golden {
		t.Fatalf("exchange file %s %v", name, err)
	}
	// Marked as uploaded, the client is Sent and left out of the next file;
	// BSE's PAN status then shows it was taken.
	if res, err := o.BSEUCCMarkUploaded(ctx, T, "priya.nair", b.Name, []string{"BSEA1"}); err != nil || res.Sent != 1 {
		t.Fatalf("uploaded %+v %v", res, err)
	}
	if b2, _ := o.BSEUCCBatchFile(ctx, T, "priya.nair"); len(b2.Clients) != 0 || len(b2.Content) != 0 {
		t.Fatalf("second file %+v", b2)
	}
	if r := bseReg(t, o, ctx, "BSEA1"); r.Status != UCCSent || r.Via != "File" {
		t.Fatalf("A %+v", r)
	}
}
