package ops

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"ledgerline/internal/money"
)

// ---- BSE StAR MF ----
//
// Built from BSE's "API File Structure" v3.5 (August 2023),
// https://www.bsestarmf.in/APIFileStructure.pdf, section "Order Entry API"
// (pages 15-49 of the public copy, the part that could be read):
//
//   - SOAP 1.2 with WS-Addressing to the order entry service
//     (demo: https://bsestarmfdemo.bseindia.com/MFOrderEntry/MFOrder.svc/Secure;
//     BSE gives the live address only to activated members after a demo).
//     Each call's action is http://bsestarmf.in/MFOrderEntry/<method>, the
//     body's elements are in the namespace http://bsestarmf.in/ in the
//     order the specification lists them.
//   - getPassword(UserId, Password, PassKey) answers "100|<encrypted
//     password>" (101: invalid). The encrypted password is sent as
//     Password on each order, with the same PassKey; it lasts 1 hour.
//   - orderEntryParam: lumpsum purchase (BuySell P) and redemption (R);
//     the answer is TransCode|UniqueRefNo|OrderNo|UserID|MemberId|
//     ClientCode|BSE remarks|Success flag.
//   - xsipOrderEntryParam: XSIP registration (TransactionCode NEW) and
//     cancellation (CXL with XsipRegID); the answer is TransactionCode|
//     UniqueRefNo|MemberId|ClientCode|UserId|XSIP REG_ID|BSE remarks|
//     Success flag|First order today order no.
//
// Where the specification is ambiguous, this is lenient:
//   - The success flag's values are not defined; the samples show 0 with
//     a confirmation, so 0 is success and anything else a failure.
//   - The lumpsum field table names the first parameter Param1, the
//     sample request Parma1 (as BSE's WSDL is known to); the sample's name
//     is used. The table's "AMOUNT" and "EUIN flag" are sent as the
//     sample's OrderVal and EUINVal.
//   - The EUIN declaration flag's meaning is not spelt out; it is sent Y
//     with an EUIN and N without one, as NSE's specification defines it.
//   - The SOAP 1.2 content type carries the action, as WCF services
//     expect; the specification shows no content type.
//
// Not implemented, because those sections of the public copy could not be
// read (the copy read stops at page 49, inside the switch order table):
// the switch order's remaining fields and answer, client registration
// (the JSON "Enhanced UCC" API), mandate registration and status, order
// status, allotment and redemption statements, and the payment gateway.
// They return errMFNoAPI; staff do them on BSE StAR MF's site and record
// them here, or load BSE's report files through the MF imports.

const (
	connBSEStarMF = "bse-starmf"
	bseStarDemo   = "https://bsestarmfdemo.bseindia.com/MFOrderEntry/MFOrder.svc/Secure"
	bseNS         = "http://bsestarmf.in/"
	bseActionBase = "http://bsestarmf.in/MFOrderEntry/"
)

func init() {
	ConnProviders = append(ConnProviders, ConnProvider{ID: connBSEStarMF, Name: "BSE StAR MF", Group: "Exchanges", Test: "https",
		Purpose: "Mutual fund orders and XSIPs on BSE StAR MF (order entry web service, API v3.5). BSE gives the user ID and password after membership; the pass key is any word the firm chooses. The live address comes from BSE after the demo and activation",
		Fields: []ConnField{{Key: "member", Label: "Member ID"}, {Key: "user", Label: "User ID"}, {Key: "password", Label: "Password", Secret: true},
			{Key: "passkey", Label: "Pass key (any letters and digits the firm chooses)", Secret: true},
			{Key: "env", Label: "Environment", Options: []string{"Live", "Demo"}},
			{Key: "url", Label: "Order entry address (leave empty for BSE's demo)", Optional: true, Placeholder: bseStarDemo},
			{Key: "fetch", Label: "Check orders during the day", Options: []string{"On", "Off"}, Optional: true}, fNotes}})
}

// bseMFHTTP talks to BSE; tests replace it.
var bseMFHTTP = &http.Client{Timeout: 60 * time.Second}

type bseStarMF struct{}

type bseField struct{ k, v string }

// bseEnvelope builds a SOAP 1.2 request in BSE's sample layout.
func bseEnvelope(to, method string, fields []bseField) []byte {
	var b bytes.Buffer
	b.WriteString(`<soap:Envelope xmlns:soap="http://www.w3.org/2003/05/soap-envelope" xmlns:bses="` + bseNS + `">`)
	b.WriteString(`<soap:Header xmlns:wsa="http://www.w3.org/2005/08/addressing"><wsa:Action>` + bseActionBase + method + `</wsa:Action><wsa:To>`)
	xml.EscapeText(&b, []byte(to))
	b.WriteString(`</wsa:To></soap:Header><soap:Body><bses:` + method + `>`)
	for _, f := range fields {
		if f.v == "" {
			b.WriteString(`<bses:` + f.k + `/>`)
			continue
		}
		b.WriteString(`<bses:` + f.k + `>`)
		xml.EscapeText(&b, []byte(f.v))
		b.WriteString(`</bses:` + f.k + `>`)
	}
	b.WriteString(`</bses:` + method + `></soap:Body></soap:Envelope>`)
	return b.Bytes()
}

// bseCall posts one SOAP request and returns the text of <method>Result.
func bseCall(ctx context.Context, to, method string, fields []bseField) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, to, bytes.NewReader(bseEnvelope(to, method, fields)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", `application/soap+xml; charset=utf-8; action="`+bseActionBase+method+`"`)
	resp, err := bseMFHTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("BSE StAR MF did not answer: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	res, ok := xmlResult(body, method+"Result")
	if !ok {
		if resp.StatusCode != 200 {
			return "", fmt.Errorf("BSE StAR MF answered %d: %s", resp.StatusCode, trimBody(body))
		}
		return "", fmt.Errorf("BSE StAR MF's answer has no %sResult: %s", method, trimBody(body))
	}
	return strings.TrimSpace(res), nil
}

// xmlResult finds the text of the first element with the local name.
func xmlResult(b []byte, local string) (string, bool) {
	d := xml.NewDecoder(bytes.NewReader(b))
	for {
		t, err := d.Token()
		if err != nil {
			return "", false
		}
		if se, ok := t.(xml.StartElement); ok && se.Name.Local == local {
			var s string
			if err := d.DecodeElement(&s, &se); err != nil {
				return "", false
			}
			return s, true
		}
	}
}

// The encrypted password lasts an hour; it is kept in memory only.
var (
	bseSessMu sync.Mutex
	bseSess   = map[string]struct {
		pw  string
		exp time.Time
	}{}
)

func (bseStarMF) password(ctx context.Context, cn mfConn) (string, error) {
	key := cn.Base + "|" + cn.User + "|" + cn.PassKey
	bseSessMu.Lock()
	s, ok := bseSess[key]
	bseSessMu.Unlock()
	if ok && time.Now().Before(s.exp) {
		return s.pw, nil
	}
	res, err := bseCall(ctx, cn.Base, "getPassword", []bseField{{"UserId", cn.User}, {"Password", cn.Password}, {"PassKey", cn.PassKey}})
	if err != nil {
		return "", err
	}
	code, pw, _ := strings.Cut(res, "|")
	if code != "100" || pw == "" {
		return "", fmt.Errorf("%w: BSE StAR MF refused the login (%s): %s", ErrState, code, pw)
	}
	bseSessMu.Lock()
	bseSess[key] = struct {
		pw  string
		exp time.Time
	}{pw, time.Now().Add(50 * time.Minute)}
	bseSessMu.Unlock()
	return pw, nil
}

func bseForget(cn mfConn) {
	bseSessMu.Lock()
	delete(bseSess, cn.Base+"|"+cn.User+"|"+cn.PassKey)
	bseSessMu.Unlock()
}

func yn(b bool) string {
	if b {
		return "Y"
	}
	return "N"
}

func bseAmount(a money.Amount) string {
	if a == 0 {
		return ""
	}
	return a.String()
}

func bseUnits(d money.Dec) string {
	if d.IsZero() {
		return ""
	}
	return d.Fixed(3)
}

// Order places a lumpsum purchase or redemption (orderEntryParam).
func (b bseStarMF) Order(ctx context.Context, cn mfConn, r mfOrderReq) (mfReply, error) {
	pw, err := b.password(ctx, cn)
	if err != nil {
		return mfReply{}, err
	}
	side, buyType, param2, param3 := "P", r.BuyType, "", ""
	amount, qty := bseAmount(r.Amount), ""
	if r.Type == "Redemption" {
		side, buyType = "R", "FRESH"
		qty = bseUnits(r.Units)
		param3 = r.BankAccount // "Redemption only, to choose the account in which the funds are redeemed to"
		if r.AllUnits {
			amount, qty = "", ""
		}
	}
	fields := []bseField{{"TransCode", "NEW"}, {"TransNo", r.Ref}, {"OrderId", ""}, {"UserID", cn.User}, {"MemberId", cn.Member}, {"ClientCode", r.ClientCode},
		{"SchemeCd", r.SchemeCode}, {"BuySell", side}, {"BuySellType", buyType}, {"DPTxn", r.DPTxn}, {"OrderVal", amount}, {"Qty", qty},
		{"AllRedeem", yn(r.AllUnits)}, {"FolioNo", r.Folio}, {"Remarks", r.Remarks}, {"KYCStatus", "Y"}, {"RefNo", r.Ref}, {"SubBrCode", ""},
		{"EUIN", r.EUIN}, {"EUINVal", yn(r.EUIN != "")}, {"MinRedeem", "N"}, {"DPC", "Y"}, {"IPAdd", cn.IP}, {"Password", pw}, {"PassKey", cn.PassKey},
		{"Parma1", ""}, {"Param2", param2}, {"Param3", param3}, {"MobileNo", r.Mobile}, {"EmailID", r.Email}, {"MandateID", r.MandateID},
		{"Filler1", ""}, {"Filler2", ""}, {"Filler3", ""}, {"Filler4", ""}, {"Filler5", ""}, {"Filler6", ""}}
	res, err := bseCall(ctx, cn.Base, "orderEntryParam", fields)
	if err != nil {
		return mfReply{}, err
	}
	f := strings.Split(res, "|")
	if len(f) < 8 {
		return mfReply{}, fmt.Errorf("BSE StAR MF's order answer has %d fields, not 8: %s", len(f), res)
	}
	rep := mfReply{OK: strings.TrimSpace(f[7]) == "0", OrderNo: strings.TrimSpace(f[2]), Remark: strings.TrimSpace(f[6])}
	if rep.OrderNo == "0" {
		rep.OrderNo = ""
	}
	if !rep.OK && strings.Contains(strings.ToUpper(rep.Remark), "PASSWORD") {
		bseForget(cn)
	}
	if rep.OK {
		rep.Status = "Order confirmed"
	}
	return rep, nil
}

func (b bseStarMF) xsip(ctx context.Context, cn mfConn, r mfSIPReq, code string) (mfReply, error) {
	pw, err := b.password(ctx, cn)
	if err != nil {
		return mfReply{}, err
	}
	start := ""
	if t, err := time.Parse("2006-01-02", r.StartDate); err == nil {
		start = t.Format("02/01/2006")
	}
	mode := "D"
	if r.DPTxn == "P" {
		mode = "P"
	}
	fields := []bseField{{"TransactionCode", code}, {"UniqueRefNo", r.Ref}, {"SchemeCode", r.SchemeCode}, {"MemberCode", cn.Member}, {"ClientCode", r.ClientCode},
		{"UserId", cn.User}, {"InternalRefNo", r.Remarks}, {"TransMode", mode}, {"DpTxnMode", r.DPTxn}, {"StartDate", start}, {"FrequencyType", r.Frequency}, // the XSIP table spells QUARTELY; the SIP table's QUARTERLY is sent
		{"FrequencyAllowed", "1"}, {"InstallmentAmount", r.Amount.String()}, {"NoOfInstallment", strconv.Itoa(r.Installments)}, {"Remarks", r.Remarks},
		{"FolioNo", r.Folio}, {"FirstOrderFlag", yn(r.FirstOrder)}, {"Brokerage", ""}, {"MandateID", r.MandateID}, {"SubberCode", ""},
		{"Euin", r.EUIN}, {"EuinVal", yn(r.EUIN != "")}, {"DPC", "Y"}, {"XsipRegID", r.RegID}, {"IPAdd", cn.IP}, {"Password", pw}, {"PassKey", cn.PassKey},
		{"Param1", ""}, {"Param2", ""}, {"Param3", ""}, {"Filler1", ""}, {"Filler2", ""}, {"Filler3", ""}, {"Filler4", ""}, {"Filler5", ""}, {"Filler6", ""}}
	res, err := bseCall(ctx, cn.Base, "xsipOrderEntryParam", fields)
	if err != nil {
		return mfReply{}, err
	}
	f := strings.Split(res, "|")
	if len(f) < 8 {
		return mfReply{}, fmt.Errorf("BSE StAR MF's XSIP answer has %d fields, not 9: %s", len(f), res)
	}
	rep := mfReply{OK: strings.TrimSpace(f[7]) == "0", RegID: strings.TrimSpace(f[5]), Remark: strings.TrimSpace(f[6])}
	if len(f) > 8 && strings.TrimSpace(f[8]) != "0" {
		rep.OrderNo = strings.TrimSpace(f[8]) // the first order placed today
	}
	if rep.RegID == "0" {
		rep.RegID = ""
	}
	return rep, nil
}

// RegisterSIP registers an XSIP, debited by the investor's mandate.
func (b bseStarMF) RegisterSIP(ctx context.Context, cn mfConn, r mfSIPReq) (mfReply, error) {
	if r.MandateID == "" {
		return mfReply{}, invalid("a BSE XSIP needs the investor's mandate ID")
	}
	return b.xsip(ctx, cn, r, "NEW")
}

// CancelSIP cancels an XSIP (TransactionCode CXL with its registration
// number).
func (b bseStarMF) CancelSIP(ctx context.Context, cn mfConn, r mfSIPReq) (mfReply, error) {
	if r.RegID == "" {
		return mfReply{}, invalid("the XSIP has no BSE registration number")
	}
	return b.xsip(ctx, cn, r, "CXL")
}

func (bseStarMF) RegisterClient(context.Context, mfConn, Client, MFProfile) (mfReply, error) {
	return mfReply{}, mfNoAPI(MFBSE, "client registration", "the Enhanced UCC API section of BSE's public specification could not be read; register the client on BSE StAR MF and mark it registered here")
}

func (bseStarMF) Switch(context.Context, mfConn, mfOrderReq) (mfReply, error) {
	return mfReply{}, mfNoAPI(MFBSE, "switch orders", "the public specification's switch section is cut off; place the switch on BSE StAR MF and load BSE's order file (MF-ORDERS)")
}

func (bseStarMF) CancelOrder(context.Context, mfConn, string, string, string) (mfReply, error) {
	return mfReply{}, mfNoAPI(MFBSE, "order cancellation", "BSE's specification says lumpsum orders can no longer be modified or cancelled through the API")
}

func (bseStarMF) RegisterMandate(context.Context, mfConn, mfMandateReq) (mfReply, error) {
	return mfReply{}, mfNoAPI(MFBSE, "mandate registration", "the mandate section of BSE's public specification could not be read; register the mandate on BSE StAR MF and record it here")
}

func (bseStarMF) MandateStatus(context.Context, mfConn, string, string) ([]mfMandateRow, error) {
	return nil, mfNoAPI(MFBSE, "mandate status", "not in the part of BSE's specification that could be read; load BSE's mandate report through MF imports")
}

func (bseStarMF) OrderStatus(context.Context, mfConn, string, string) ([]mfStatusRow, error) {
	return nil, mfNoAPI(MFBSE, "order status", "not in the part of BSE's specification that could be read; load BSE's order status report through MF imports")
}

func (bseStarMF) Allotments(context.Context, mfConn, string, string) ([]mfAllotRow, error) {
	return nil, mfNoAPI(MFBSE, "allotment statement", "not in the part of BSE's specification that could be read; load BSE's allotment statement through MF imports")
}
