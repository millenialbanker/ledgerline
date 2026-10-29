package ops

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Fake exchange platforms answering in the formats of their public
// specifications (see mfbse.go and mfnse.go). Nothing here talks to a
// real platform.

const (
	nseFakeUser   = "MDLOGIN"
	nseFakeSecret = "SECRET-XYZ"
	nseFakeKey    = "LK-TEST-0001"
	nseFakeMember = "12345"
)

type nseCall struct {
	Path string
	Row  map[string]string
}

type nseFake struct {
	t     *testing.T
	mu    sync.Mutex
	calls []nseCall
	seq   int
	// fail makes the next answer for a path a failure with this remark.
	fail map[string]string
}

func (f *nseFake) last(path string) map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.calls) - 1; i >= 0; i-- {
		if f.calls[i].Path == path {
			return f.calls[i].Row
		}
	}
	return nil
}

func (f *nseFake) count(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c.Path == path {
			n++
		}
	}
	return n
}

func newNSEFake(t *testing.T) (*nseFake, *httptest.Server) {
	f := &nseFake{t: t, fail: map[string]string{}}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		raw, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(r.Header.Get("Authorization"), "BASIC "))
		user, enc, _ := strings.Cut(string(raw), ":")
		plain, err := nseMFDecrypt(enc, nseFakeKey)
		secret, num, _ := strings.Cut(plain, "|")
		if err != nil || user != nseFakeUser || secret != nseFakeSecret || num == "" || r.Header.Get("memberId") != nseFakeMember || r.Method != http.MethodPost {
			w.WriteHeader(403)
			io.WriteString(w, `{"status":"403 FORBIDDEN","error_type":"unauthorized","message":"Invalid authorization header or IP Address not mapped with user."}`)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/nsemfdesk/api/v2/")
		var body map[string][]map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(400)
			return
		}
		var list string
		var row map[string]string
		for k, v := range body {
			list, row = k, v[0]
		}
		f.mu.Lock()
		f.calls = append(f.calls, nseCall{path, row})
		f.seq++
		seq := f.seq
		remark, failing := f.fail[path]
		delete(f.fail, path)
		f.mu.Unlock()
		out := map[string]any{}
		for k, v := range row {
			out[k] = v
		}
		key := list
		switch path {
		case "transaction/NORMAL":
			if failing {
				out["trxn_status"], out["trxn_remark"] = "TRXN FAILED", remark
			} else {
				// A number, as NUMBER(12) may come.
				out["trxn_order_id"], out["trxn_status"], out["trxn_remark"] = json.Number(fmt.Sprintf("4321600%05d", seq)), "TRXN SUCCESS", ""
			}
		case "transaction/SWITCH":
			out["trxn_so_order_id"], out["trxn_si_order_id"], out["trxn_status"] = fmt.Sprintf("4321610%05d", seq), fmt.Sprintf("4321620%05d", seq), "TRXN SUCCESS"
		case "registration/product/XSIP":
			if failing {
				out["reg_status"], out["reg_remark"] = "REG_FAILED", remark
			} else {
				out["reg_id"], out["reg_status"] = fmt.Sprintf("2026102910%05d", seq), "REG_SUCCESS"
			}
		case "registration/CLIENTCOMMON183":
			out["reg_status"], out["reg_remark"] = "REG_SUCCESS", "CLIENT REGISTERED SUCCESSFULLY"
		case "cancellation/ORDER_CAN":
			key = "reg_data" // as the specification's sample answers
			out["can_status"], out["can_remark"] = "CAN SUCCESS", "ORDER CANCELLED"
		default:
			w.WriteHeader(404)
			io.WriteString(w, `{"status":"404","message":"Service not found"}`)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{key: []any{out}})
	}))
	t.Cleanup(srv.Close)
	return f, srv
}

type bseCallRec struct {
	Method string
	Action string
	Names  []string
	Vals   map[string]string
}

type bseFake struct {
	mu        sync.Mutex
	calls     []bseCallRec
	failOrder string // remark for a failed order
}

func (f *bseFake) last(method string) bseCallRec {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.calls) - 1; i >= 0; i-- {
		if f.calls[i].Method == method {
			return f.calls[i]
		}
	}
	return bseCallRec{}
}

func (f *bseFake) count(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c.Method == method {
			n++
		}
	}
	return n
}

const bseFakePW = "UWQ/yLAS7KmiHyExtsKKq3DuHTWuyGOzy0P4s38PSL8nA=="

func newBSEFake(t *testing.T) (*bseFake, *httptest.Server) {
	f := &bseFake{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		rec := bseCallRec{Vals: map[string]string{}}
		_, act, _ := strings.Cut(r.Header.Get("Content-Type"), `action="`)
		rec.Action = strings.TrimSuffix(act, `"`)
		d := xml.NewDecoder(bytes.NewReader(b))
		depth, inBody := 0, false
		var cur string
		var wsaTo string
		for {
			tok, err := d.Token()
			if err != nil {
				break
			}
			switch x := tok.(type) {
			case xml.StartElement:
				depth++
				if x.Name.Local == "Body" {
					inBody = true
				}
				if inBody && depth == 3 {
					if x.Name.Space != bseNS {
						t.Errorf("method namespace %q", x.Name.Space)
					}
					rec.Method = x.Name.Local
				}
				if inBody && depth == 4 {
					cur = x.Name.Local
					rec.Names = append(rec.Names, cur)
				}
				if x.Name.Local == "To" {
					var s string
					d.DecodeElement(&s, &x)
					wsaTo = s
					depth--
				}
			case xml.CharData:
				if cur != "" {
					rec.Vals[cur] += string(x)
				}
			case xml.EndElement:
				depth--
				cur = ""
			}
		}
		if rec.Action != bseActionBase+rec.Method || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/soap+xml") || wsaTo != "https://"+r.Host+r.URL.Path {
			w.WriteHeader(400)
			io.WriteString(w, "bad SOAP request")
			return
		}
		f.mu.Lock()
		f.calls = append(f.calls, rec)
		failRemark := f.failOrder
		f.mu.Unlock()
		v := rec.Vals
		var res string
		switch rec.Method {
		case "getPassword":
			res = "101|INVALID USER ID OR PASSWORD"
			if v["UserId"] == "612701" && v["Password"] == "pw-test" && v["PassKey"] == "pk1" {
				res = "100|" + bseFakePW
			}
		case "orderEntryParam":
			switch {
			case v["Password"] != bseFakePW:
				res = fmt.Sprintf("NEW|%s|0|%s|%s|%s|FAILED: INVALID PASSWORD|1", v["TransNo"], v["UserID"], v["MemberId"], v["ClientCode"])
			case failRemark != "":
				res = fmt.Sprintf("NEW|%s|0|%s|%s|%s|FAILED: %s|1", v["TransNo"], v["UserID"], v["MemberId"], v["ClientCode"], failRemark)
			default:
				res = fmt.Sprintf("NEW|%s|7531973|%s|%s|%s|ORD CONF: Your Request for FRESH PURCHASE %s is confirmed for CLIENT CODE %s|0", v["TransNo"], v["UserID"], v["MemberId"], v["ClientCode"], v["SchemeCd"], v["ClientCode"])
			}
		case "xsipOrderEntryParam":
			if v["TransactionCode"] == "CXL" {
				res = fmt.Sprintf("CXL|%s|%s|%s|%s|%s|X-SIP CANCELLED SUCCESSFULLY|0|0", v["UniqueRefNo"], v["MemberCode"], v["ClientCode"], v["UserId"], v["XsipRegID"])
			} else {
				res = fmt.Sprintf("NEW|%s|%s|%s|%s|582597|X-SIP HAS BEEN REGISTERED, REG NO IS : 582597|0|0", v["UniqueRefNo"], v["MemberCode"], v["ClientCode"], v["UserId"])
			}
		default:
			w.WriteHeader(500)
			return
		}
		w.Header().Set("Content-Type", "application/soap+xml; charset=utf-8")
		fmt.Fprintf(w, `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:a="http://www.w3.org/2005/08/addressing"><s:Header><a:Action s:mustUnderstand="1">http://bsestarmf.in/MFOrderEntry/%sResponse</a:Action></s:Header><s:Body><%sResponse xmlns="http://bsestarmf.in/"><%sResult>%s</%sResult></%sResponse></s:Body></s:Envelope>`,
			rec.Method, rec.Method, rec.Method, res, rec.Method, rec.Method)
	}))
	t.Cleanup(srv.Close)
	return f, srv
}
