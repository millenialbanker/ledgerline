package ops

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// fakeUPIX plays NSE Clearing's UPIX: it checks each call's JWT.
type fakeUPIX struct {
	t     *testing.T
	key   []byte
	now   func() time.Time
	calls []string
	reply map[string]string // path -> JSON
}

func (f *fakeUPIX) Post(ctx context.Context, u string, h http.Header, body []byte) (int, []byte, error) {
	path := u[strings.Index(u, "/v1/"):]
	if strings.Contains(u, "/rest/v1/") {
		path = u[strings.Index(u, "/rest/v1/"):]
	}
	f.calls = append(f.calls, path)
	tok := strings.TrimPrefix(h.Get("Authorization"), "Bearer ")
	parts := strings.Split(tok, ".")
	if len(parts) != 3 || h.Get("User-Agent") != "/" {
		return 401, []byte(`{"code":401,"messages":["no token"]}`), nil
	}
	pb, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var p map[string]any
	json.Unmarshal(pb, &p)
	if p["sub"] != "CM_12345" || p["aud"] != "NCL" || p["upixApi"] != "POST_"+path {
		f.t.Errorf("%s: claims %v", path, p)
	}
	// The signature with the shared key, as the protocol makes it.
	want := upixJWT("CM_12345", "NCL", "POST_"+path, f.key, time.UnixMilli(int64(p["iat"].(float64))))
	if tok != want {
		return 401, []byte(`{"code":401,"messages":["bad signature"]}`), nil
	}
	return 200, []byte(f.reply[path]), nil
}

func TestUPIXJWT(t *testing.T) {
	// The protocol's own example: the header and claims encode as shown.
	tok := upixJWT("CM_12345", "NCL", "POST_/v1/request/blockrelease", make([]byte, 32), time.UnixMilli(1688024568404))
	want := "eyJ0eXAiOiJKV1QiLCJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJDTV8xMjM0NSIsImF1ZCI6Ik5DTCIsImlhdCI6MTY4ODAyNDU2ODQwNCwidXBpeEFwaSI6IlBPU1RfL3YxL3JlcXVlc3QvYmxvY2tyZWxlYXNlIn0."
	if !strings.HasPrefix(tok, want) {
		t.Fatalf("token %s", tok)
	}
	key := []byte("0123456789abcdef0123456789abcdef")
	now := time.Date(2026, 11, 18, 11, 0, 0, 0, ist)
	n := upixJWT("NCL", "CM_12345", "POST_/v1/notify/blockdebit", key, now)
	if err := upixVerify(n, "CM_12345", "/notify/blockdebit", key, now.Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{
		"wrong key":  upixVerify(n, "CM_12345", "/notify/blockdebit", []byte("fedcba9876543210fedcba9876543210"), now),
		"too old":    upixVerify(n, "CM_12345", "/notify/blockdebit", key, now.Add(2*time.Minute)),
		"other api":  upixVerify(n, "CM_12345", "/notify/regdereg", key, now),
		"other firm": upixVerify(n, "CM_54321", "/notify/blockdebit", key, now),
	} {
		if err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestUPIXSync(t *testing.T) {
	o, ctx := setup(t)
	now := time.Date(2026, 11, 18, 11, 0, 0, 0, ist)
	o.Now = func() time.Time { return now }
	cs, _ := o.Clients(ctx, T)
	a := cs[0].UCC
	key := []byte("0123456789abcdef0123456789abcdef")
	f := &fakeUPIX{t: t, key: key, reply: map[string]string{
		"/v1/inquire/regdereg":            `[{"seg":"CM","cmCd":"12345","tmCd":"12345","ucc":"` + a + `","status":"01010100","regStatus":"R","regTime":"16-JAN-2026 16:15:01","exch":"NSE","bankList":[{"ifsc":"BANK0000111","accNo":"1","pri":"Y"}],"depo":"IN000026","cliId":"1234567890123456"}]`,
		"/v1/inquire/segblockbal":         `[{"seg":"CM","cmCd":"12345","tmCd":"12345","ucc":"` + a + `","status":"01010100","balAmount":50000}]`,
		"/rest/v1/inquire/blockdebitlist": `["9876520261118R0000000100"]`,
		"/rest/v1/inquire/blockdebit":     `[{"seg":"CM","cmCd":"12345","tmCd":"12345","ucc":"` + a + `","tmRefId":"T1","umn":"UMN1","amtBlocked":51000,"ccRefId":"9876520261118R0000000100","rc":100,"drAmt":1000,"drTime":"18-NOV-2026 10:15:01","desc":"Debit for obligation","totAmtDr":1000,"balAmount":50000}]`,
	}}
	o.UPIX = f
	if _, err := o.UPIXSync(ctx, T, "admin", false); err == nil {
		t.Fatal("checked without a connection")
	}
	vals := map[string]string{"member_type": "CM", "member": "12345", "segments": "CM, FO", "auto": "On", "post": "On"}
	sec := map[string]string{"secret": base64.StdEncoding.EncodeToString(key)}
	if _, err := o.SaveConnection(ctx, T, "admin", connUPIX, vals, sec); err != nil {
		t.Fatal(err)
	}
	// Until NSE Clearing gives the address, nothing is called.
	if _, err := o.UPIXSync(ctx, T, "admin", false); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 0 || o.UPIXStatus(ctx, T).State.LastError == "" {
		t.Fatalf("called without an address: %v", f.calls)
	}
	vals["url"] = "https://upix.example.test"
	if _, err := o.SaveConnection(ctx, T, "admin", connUPIX, vals, sec); err != nil {
		t.Fatal(err)
	}
	run, err := o.UPIXSync(ctx, T, "admin", false)
	if err != nil || run.Registered != 1 || run.Balances != 1 || run.Debits != 1 || run.Posted != 1 || len(run.Problems) != 0 {
		t.Fatalf("run %+v %v (calls %v)", run, err, f.calls)
	}
	if b := o.upixBlocked(ctx, T); b[a].String() != "50000" {
		t.Fatalf("blocked %v", b)
	}
	c, _ := o.Client(ctx, T, a)
	if upiBlock(c, o.upixBlocked(ctx, T)).String() != "50000" {
		t.Fatal("risk does not use UPIX's balance")
	}
	d, _, _ := docs.Get[UPIXDebit](ctx, o.Docs, T, cUPIXDebit, "9876520261118R0000000100")
	if d.Posted == "" || d.Amount != money.Amount(100000) || d.Date != "2026-11-18" {
		t.Fatalf("debit %+v", d)
	}
	// The next check finds the same debit and does not post it again.
	now = now.Add(20 * time.Minute)
	if run, err = o.UPIXSync(ctx, T, "admin", false); err != nil || run.Debits != 0 || run.Posted != 0 || run.Registered != 0 {
		t.Fatalf("second run %+v %v", run, err)
	}
	// A notification from NSE Clearing: a new debit, signed with the key.
	body := `[{"seg":"CM","ucc":"` + a + `","tmRefId":"T1","umn":"UMN1","ccRefId":"9876520261118R0000000200","rc":100,"drAmt":2500.50,"drTime":"18-NOV-2026 11:15:01","totAmtDr":3500.50,"balAmount":47499.50}]`
	tok := upixJWT("NCL", "CM_12345", "POST_/v1/notify/blockdebit", key, now)
	if _, err := o.UPIXHook(ctx, T, "v1", "blockdebit", "Bearer "+tok, []byte(body)); err != nil {
		t.Fatal(err)
	}
	if d, _, _ := docs.Get[UPIXDebit](ctx, o.Docs, T, cUPIXDebit, "9876520261118R0000000200"); d.Posted == "" || d.Amount != money.Amount(250050) {
		t.Fatalf("notified debit %+v", d)
	}
	if st := o.UPIXStatus(ctx, T); st.State.Notices != 1 || len(st.State.Refresh) != 1 || len(st.Debits) != 2 {
		t.Fatalf("status %+v", st)
	}
	// A notification not signed with the firm's key is refused.
	bad := upixJWT("NCL", "CM_12345", "POST_/v1/notify/blockdebit", []byte("fedcba9876543210fedcba9876543210"), now)
	if _, err := o.UPIXHook(ctx, T, "v1", "blockdebit", "Bearer "+bad, []byte(body)); err == nil {
		t.Fatal("a notification with the wrong key was taken")
	}
	// The tick refreshes the notified client's balance within a minute.
	f.reply["/v1/inquire/segblockbal"] = `[{"seg":"CM","ucc":"` + a + `","status":"01010100","balAmount":47499.50}]`
	o.upixTick(ctx, T, now.Add(time.Minute))
	if b := o.upixBlocked(ctx, T); b[a].String() != "47499.5" {
		t.Fatalf("blocked after the notice %v", b)
	}
}
