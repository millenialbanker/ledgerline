// Package hdfctest is a stand-in for HDFC Bank's API gateway and CBX batch
// payments, for tests. It checks requests the way the bank does (OAuth
// token, headers, the firm's JWS inside a JWE to the bank's key) and
// answers signed and encrypted to the firm.
package hdfctest

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"ledgerline/internal/hdfcapi"
)

// Line is one payment as the fake bank holds it.
type Line struct {
	hdfcapi.Payment
	Status, UTR, BankRef, Reason, RBI string
}

// Batch is a batch the fake bank accepted.
type Batch struct {
	Num, FileRef, Date, Group, Maker, Client string
	Lines                                    []*Line
	Inquiries                                int
}

// Bank is the fake gateway.
type Bank struct {
	Server  *httptest.Server
	Key     *rsa.PrivateKey
	CertPEM string // the bank's certificate, as given to the firm

	APIKey, Secret, Scope string

	mu      sync.Mutex
	firm    *rsa.PublicKey
	batches map[string]*Batch // by batch number
	byRef   map[string]string // date|fileref -> batch number
	seq     int
	tokens  int
	calls   []string

	// LoseNextReply makes the next batch initiation be accepted but answer
	// 504, as when the reply is lost on the way.
	LoseNextReply bool
	// DropNext makes the next batch initiation never reach the bank (504,
	// nothing recorded).
	DropNext bool
	// RejectNext makes the next initiation be refused (400, a reason).
	RejectNext string
	// Ready is how many inquiries a batch needs before its real state (the
	// bank's loop of 3: accepted, under process, then the state).
	Ready int
}

// New starts a fake bank for a firm whose public key is firm.
func New(firm *rsa.PublicKey) *Bank {
	k, _ := rsa.GenerateKey(rand.Reader, 2048)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "api.hdfcbank.test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(1, 0, 0)}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	b := &Bank{Key: k, CertPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		APIKey: "l7test", Secret: "s3cret", Scope: "LLTEST", firm: firm, batches: map[string]*Batch{}, byRef: map[string]string{}, Ready: 3}
	b.Server = httptest.NewTLSServer(http.HandlerFunc(b.serve))
	return b
}

// Base is the gateway base to configure.
func (b *Bank) Base() string { return b.Server.URL + "/api" }

// Close stops the server.
func (b *Bank) Close() { b.Server.Close() }

// Batches returns the accepted batches, in no order.
func (b *Bank) Batches() []*Batch {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []*Batch
	for _, x := range b.batches {
		out = append(out, x)
	}
	return out
}

// Calls lists the paths called, in order.
func (b *Bank) Calls() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.calls...)
}

// Tokens is how many OAuth tokens were issued.
func (b *Bank) Tokens() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.tokens
}

// Set changes a payment's state, found by its customer reference.
func (b *Bank) Set(custRef, status, utr, reason, rbi string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, x := range b.batches {
		for _, l := range x.Lines {
			if l.CustRef == custRef {
				l.Status, l.UTR, l.Reason, l.RBI = status, utr, reason, rbi
				if status == "C" && l.Type == hdfcapi.TypeInternal {
					l.BankRef, l.UTR = utr, ""
				}
				return true
			}
		}
	}
	return false
}

func (b *Bank) fail(w http.ResponseWriter, status int, code, reason string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"title": http.StatusText(status), "errors": []map[string]string{{"code": code, "reason": reason}}})
}

func (b *Bank) serve(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls = append(b.calls, r.URL.Path)
	if r.URL.Path == "/auth/oauth/v1/token" {
		u, p, ok := r.BasicAuth()
		if !ok || u != b.APIKey || p != b.Secret || r.URL.Query().Get("grant_type") != "client_credentials" {
			w.WriteHeader(401)
			_, _ = io.WriteString(w, `{"error":"invalid_client","error_description":"Client authentication failed"}`)
			return
		}
		if r.URL.Query().Get("scope") != b.Scope {
			w.WriteHeader(400)
			_, _ = io.WriteString(w, `{"error":"invalid_scope","error_description":"No registered scope value for this client has been requested"}`)
			return
		}
		b.tokens++
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": fmt.Sprintf("tok%d", b.tokens), "expires_in": "899", "token_type": "BearerToken"})
		return
	}
	if r.Header.Get("apikey") != b.APIKey {
		b.fail(w, 401, "TH99401", "Invalid API Key")
		return
	}
	if r.Header.Get("scope") != b.Scope {
		b.fail(w, 401, "TH99401", "Scope Validation Failed")
		return
	}
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer tok") {
		b.fail(w, 401, "TH99401", "Invalid access token")
		return
	}
	if r.Header.Get("Content-Type") != "application/jose" || r.Header.Get("transactionId") == "" || strings.Trim(r.Header.Get("transactionId"), "0123456789abcdefABCDEFghijklmnopqrstuvwxyzGHIJKLMNOPQRSTUVWXYZ") != "" {
		b.fail(w, 400, "TH99400", "headers")
		return
	}
	raw, _ := io.ReadAll(r.Body)
	myKid, _ := hdfcapi.KID(&b.Key.PublicKey)
	firmKid, _ := hdfcapi.KID(b.firm)
	jws, err := hdfcapi.Decrypt(string(raw), b.Key, myKid)
	if err != nil {
		b.fail(w, 400, "TH99400", "decrypt: "+err.Error())
		return
	}
	plain, err := hdfcapi.Verify(string(jws), b.firm, firmKid)
	if err != nil {
		b.fail(w, 400, "TH99400", "signature: "+err.Error())
		return
	}
	var out any
	switch r.URL.Path {
	case "/api/" + hdfcapi.PathBulkPayment:
		var q hdfcapi.BatchRequest
		if err := json.Unmarshal(plain, &q); err != nil || len(q.Payments) == 0 || len(q.Payments) > hdfcapi.MaxBatch || q.Count != fmt.Sprint(len(q.Payments)) || len(q.FileRef) > 6 || q.FileRef == "" {
			b.fail(w, 400, "BAD", "invalid batch")
			return
		}
		if b.DropNext {
			b.DropNext = false
			w.WriteHeader(504)
			return
		}
		if b.RejectNext != "" {
			reason := b.RejectNext
			b.RejectNext = ""
			b.fail(w, 400, "CBX400", reason)
			return
		}
		key := q.Payments[0].Date + "|" + q.FileRef
		if num, dup := b.byRef[key]; dup {
			b.fail(w, 400, "CBX409", "Duplicate file reference number; batch "+num)
			return
		}
		b.seq++
		num := fmt.Sprintf("%s4903API%s%04d", q.GroupID, time.Now().Format("20060102150405"), b.seq)
		x := &Batch{Num: num, FileRef: q.FileRef, Date: q.Payments[0].Date, Group: q.GroupID, Maker: q.Maker, Client: q.ClientCode}
		for _, p := range q.Payments {
			x.Lines = append(x.Lines, &Line{Payment: p, Status: "P"})
		}
		b.batches[num], b.byRef[key] = x, num
		if b.LoseNextReply {
			b.LoseNextReply = false
			w.WriteHeader(504)
			return
		}
		out = map[string]any{"txtstatus": "ACCEPTED", "codstatus": "0", "batchnum": num, "clientcode": q.ClientCode, "groupid": q.GroupID, "iduser": q.Maker, "nooftran": q.Count, "filerefno": q.FileRef}
	case "/api/" + hdfcapi.PathBulkInquiry:
		var q hdfcapi.InquiryRequest
		_ = json.Unmarshal(plain, &q)
		x, ok := b.batches[q.BatchNum]
		if !ok || x.FileRef != q.FileRef {
			out = map[string]any{"batchnum": q.BatchNum, "codstatus": "R", "message": "NDF No data found"}
			break
		}
		x.Inquiries++
		switch {
		case x.Inquiries%b.Ready == 1 && b.Ready > 1:
			out = map[string]any{"batchnum": x.Num, "message": "We have accepted your request, please enquire again after sometime"}
		case x.Inquiries%b.Ready == 2 && b.Ready > 2:
			out = map[string]any{"batchnum": x.Num, "message": "Your request is under process, please try after sometime"}
		default:
			var lines []map[string]any
			for _, l := range x.Lines {
				lines = append(lines, map[string]any{"cdflag": l.Type, "code": l.BeneCode, "name": l.Name, "amount": json.Number(l.Amount), "custrefno": l.CustRef, "accno": l.Account,
					"bankrefno": l.BankRef, "codstatus": l.Status, "txtreason": l.Reason, "ifsc": l.IFSC, "refno": l.UTR, "rbistatus": l.RBI})
			}
			out = map[string]any{"gcif": x.Group, "iduser": x.Maker, "batchnum": x.Num, "nooftran": len(x.Lines), "codstatus": "A", "message": "Accepted", "filerefno": json.Number(strings.TrimLeft(x.FileRef, "0")), "trans": lines}
		}
	case "/api/" + hdfcapi.PathBatchNumber:
		var q hdfcapi.BatchNumberRequest
		_ = json.Unmarshal(plain, &q)
		if num, ok := b.byRef[q.Date+"|"+q.FileRef]; ok {
			out = map[string]any{"gcif": q.GroupID, "batchnum": num, "codstatus": "0", "message": "Success", "filerefno": q.FileRef, "nooftran": fmt.Sprint(len(b.batches[num].Lines))}
		} else {
			out = map[string]any{"codstatus": "1", "message": "Please check the filerefno, No record found for Filerefno", "filerefno": q.FileRef}
		}
	default:
		b.fail(w, 404, "TH99404", "no such API")
		return
	}
	body, _ := json.Marshal(out)
	signed, _ := hdfcapi.Sign(body, b.Key, myKid)
	enc, _ := hdfcapi.Encrypt([]byte(signed), b.firm, firmKid)
	w.Header().Set("Content-Type", "application/jose")
	_, _ = io.WriteString(w, enc)
}
