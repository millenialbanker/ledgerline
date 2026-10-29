package ops

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ---- NSE MF Invest ----
//
// Built from NSE's "Protocol for Non-NEAT Front End (NNF) Mutual Funds
// Service System" v1.9.7 (June 2026),
// https://nseinvestuat.nseindia.com/nsemfdesk/resources/upload/apidetails/NSEMF_API_Details_V1.9.7.pdf
// (pages 1-54 of the public copy could be read), and NSE's API FAQ
// (https://nsearchives.nseindia.com/web/sites/default/files/inline-files/NSE%20INVEST%20API%20FAQ_310725.pdf).
// NSE MF Invest replaced NMF II, whose APIs were retired on 31 July 2025
// (circular https://nsearchives.nseindia.com/content/circulars/NMF68809.pdf).
//
//   - Addresses: UAT https://nseinvestuat.nseindia.com, live
//     https://www.nseinvest.com; every API is POST
//     /nsemfdesk/api/v2/<group>/<name> with a JSON body.
//   - Authentication, on every request: Authorization BASIC
//     base64(<login user ID>:<encrypted password>), where the encrypted
//     password is base64(iv::salt::AES-128(<API secret>|<random number>))
//     keyed by the API member license key. NSE issues the keys by email
//     for up to 4 whitelisted IP addresses per user.
//   - Order entry (transaction/NORMAL, list "transaction_details", up to
//     50): purchase P, redemption R; the answer echoes the order with
//     trxn_order_id, trxn_status (TRXN SUCCESS / TRXN FAILED) and
//     trxn_remark.
//   - Switch (transaction/SWITCH): trxn_so_order_id and trxn_si_order_id.
//   - Order cancellation (cancellation/ORDER_CAN, list "can_data"):
//     can_status CAN SUCCESS / CAN FAILED.
//   - XSIP registration (registration/product/XSIP, list "reg_data"):
//     reg_id, reg_status REG_SUCCESS / REG_FAILED, reg_remark.
//   - Client registration, 183 columns (registration/CLIENTCOMMON183, list
//     "reg_details").
//
// Lenient where the specification is unclear:
//   - The password's AES mode is not stated. The example's arguments
//     (32-hex-digit salt and IV, a passphrase) are those of the common
//     "AesUtil" construction: PBKDF2-HMAC-SHA1, 1000 rounds, a 128-bit
//     key, AES-CBC with PKCS#7 padding, salt and IV hex-decoded. That is
//     what is sent; confirm it in UAT.
//   - The header table shows memberId with the value 1; the member code
//     is sent.
//   - Every value is sent as a JSON string.
//   - Answers are read from whichever list the reply holds (the
//     cancellation sample answers in "reg_data", not "can_data").
//   - The client registration's answer is not in the readable part; a
//     reply with a status containing SUCCESS (or no failure) is taken as
//     submitted, and the investor then authorises it by NSE's link.
//   - The FAQ says requests without browser-like headers may be blocked
//     by NSE's CDN; they are sent, with Accept-Encoding gzip and deflate
//     (Go has no brotli).
//
// Not implemented, because their sections are beyond the readable part:
// SIP and XSIP cancellation, mandate registration and the mandate status
// report, order status, allotment and redemption statements, and the
// purchase payment and short link APIs. They return errMFNoAPI. NSE sends
// the investor the authorisation and payment link itself (the payment
// goes to the NSE Clearing settlement account).

const (
	connNSEMFInvest = "nse-mfinvest"
	nseMFLive       = "https://www.nseinvest.com"
	nseMFUAT        = "https://nseinvestuat.nseindia.com"
	nseMFAPI        = "/nsemfdesk/api/v2/"
)

func init() {
	ConnProviders = append(ConnProviders, ConnProvider{ID: connNSEMFInvest, Name: "NSE MF Invest", Group: "Exchanges", Test: "https",
		Purpose: "Mutual fund client registration, orders and XSIPs on NSE MF Invest (API v1.9.7). Email NSE for the API secret and member license key, giving the member code, the Member Desk login and up to 4 static IP addresses",
		Fields: []ConnField{{Key: "member", Label: "Member code"}, {Key: "user", Label: "Login user ID (the Member Desk login)"},
			{Key: "secret", Label: "API secret", Secret: true}, {Key: "license_key", Label: "API member license key", Secret: true},
			{Key: "env", Label: "Environment", Options: []string{"Live", "UAT"}},
			{Key: "url", Label: "Address (leave empty for NSE's)", Optional: true, Placeholder: nseMFLive},
			{Key: "fetch", Label: "Check orders during the day", Options: []string{"On", "Off"}, Optional: true}, fNotes}})
}

// nseMFHTTP talks to NSE; tests replace it.
var nseMFHTTP = &http.Client{Timeout: 60 * time.Second}

// nseMFRand supplies the salt, IV and random number; tests may replace it.
var nseMFRand io.Reader = rand.Reader

type nseMFInvest struct{}

// nseMFPassword encrypts "<secret>|<random number>" as NSE describes.
func nseMFPassword(secret, licenseKey string, rnd io.Reader) (string, error) {
	b := make([]byte, 32)
	if _, err := io.ReadFull(rnd, b); err != nil {
		return "", err
	}
	n, err := rand.Int(rnd, big.NewInt(1e11))
	if err != nil {
		return "", err
	}
	salt, iv := b[:16], b[16:]
	key, err := pbkdf2.Key(sha1.New, licenseKey, salt, 1000, 16)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	plain := []byte(secret + "|" + n.String())
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	plain = append(plain, bytes.Repeat([]byte{byte(pad)}, pad)...)
	ct := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ct, plain)
	inner := hex.EncodeToString(iv) + "::" + hex.EncodeToString(salt) + "::" + base64.StdEncoding.EncodeToString(ct)
	return base64.StdEncoding.EncodeToString([]byte(inner)), nil
}

// nseMFDecrypt reverses nseMFPassword (for tests and checks).
func nseMFDecrypt(enc, licenseKey string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", err
	}
	parts := strings.SplitN(string(raw), "::", 3)
	if len(parts) != 3 {
		return "", fmt.Errorf("not iv::salt::text")
	}
	iv, err1 := hex.DecodeString(parts[0])
	salt, err2 := hex.DecodeString(parts[1])
	ct, err3 := base64.StdEncoding.DecodeString(parts[2])
	if err1 != nil || err2 != nil || err3 != nil || len(iv) != 16 || len(ct)%16 != 0 || len(ct) == 0 {
		return "", fmt.Errorf("bad parts")
	}
	key, err := pbkdf2.Key(sha1.New, licenseKey, salt, 1000, 16)
	if err != nil {
		return "", err
	}
	block, _ := aes.NewCipher(key)
	out := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, ct)
	p := int(out[len(out)-1])
	if p == 0 || p > 16 || p > len(out) {
		return "", fmt.Errorf("bad padding")
	}
	return string(out[:len(out)-p]), nil
}

func (nseMFInvest) post(ctx context.Context, cn mfConn, path, list string, rows []map[string]string) (map[string]any, error) {
	pw, err := nseMFPassword(cn.Secret, cn.LicenseKey, nseMFRand)
	if err != nil {
		return nil, err
	}
	body, _ := json.Marshal(map[string]any{list: rows})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cn.Base+nseMFAPI+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	h := req.Header
	h.Set("Content-Type", "application/json")
	h.Set("memberId", cn.Member)
	h.Set("Authorization", "BASIC "+base64.StdEncoding.EncodeToString([]byte(cn.User+":"+pw)))
	h.Set("Accept", "*/*")
	h.Set("Accept-Language", "en-US")
	h.Set("Accept-Encoding", "gzip, deflate")
	h.Set("User-Agent", "Mozilla/5.0 (compatible; Ledgerline back office)")
	h.Set("Connection", "keep-alive")
	resp, err := nseMFHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("NSE MF Invest did not answer: %v", err)
	}
	defer resp.Body.Close()
	var rd io.Reader = resp.Body
	switch strings.ToLower(resp.Header.Get("Content-Encoding")) {
	case "gzip":
		if z, err := gzip.NewReader(resp.Body); err == nil {
			rd = z
		}
	case "deflate":
		rd = flate.NewReader(resp.Body)
	}
	b, _ := io.ReadAll(io.LimitReader(rd, 8<<20))
	var m map[string]any
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	derr := d.Decode(&m)
	switch {
	case resp.StatusCode == 403:
		return nil, fmt.Errorf("%w: NSE MF Invest refused the login: %s (check the keys and that this server's IP is whitelisted)", ErrState, firstOf(nseStr(m["message"]), trimBody(b)))
	case resp.StatusCode != 200 && derr != nil:
		return nil, fmt.Errorf("NSE MF Invest answered %d: %s", resp.StatusCode, trimBody(b))
	case derr != nil:
		return nil, fmt.Errorf("NSE MF Invest's answer is not JSON: %s", trimBody(b))
	}
	if resp.StatusCode != 200 && nseFirst(m) == nil {
		return nil, fmt.Errorf("%w: NSE MF Invest answered %d: %s", ErrState, resp.StatusCode, firstOf(nseStr(m["message"]), trimBody(b)))
	}
	return m, nil
}

func nseStr(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(x)
	case json.Number:
		return x.String()
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

// nseFirst is the first record of whichever list the reply holds.
func nseFirst(m map[string]any) map[string]any {
	for _, k := range []string{"transaction_details", "reg_data", "reg_details", "can_data", "data"} {
		if l, ok := m[k].([]any); ok && len(l) > 0 {
			if r, ok := l[0].(map[string]any); ok {
				return r
			}
		}
	}
	for _, v := range m {
		if l, ok := v.([]any); ok && len(l) > 0 {
			if r, ok := l[0].(map[string]any); ok {
				return r
			}
		}
	}
	return nil
}

func nseAmt(r mfOrderReq) string {
	if r.Amount == 0 {
		return ""
	}
	return r.Amount.String()
}

// Order places a purchase or redemption (transaction/NORMAL).
func (n nseMFInvest) Order(ctx context.Context, cn mfConn, r mfOrderReq) (mfReply, error) {
	row := map[string]string{"order_ref_number": r.Ref, "scheme_code": r.SchemeCode, "trxn_type": "P", "buy_sell_type": r.BuyType, "client_code": r.ClientCode,
		"demat_physical": r.DPTxn, "order_amount": nseAmt(r), "folio_no": r.Folio, "remarks": r.Remarks, "kyc_flag": "Y", "sub_broker_code": "",
		"euin_number": r.EUIN, "euin_declaration": yn(r.EUIN != ""), "min_redemption_flag": "N", "dpc_flag": "Y", "all_units": yn(r.AllUnits),
		"redemption_units": "", "sub_broker_arn": "", "bank_ref_no": "", "account_no": "", "mobile_no": r.Mobile, "email": r.Email, "mandate_id": r.MandateID,
		"filler1": "", "trxn_order_id": "", "trxn_status": "", "trxn_remark": "", "member_unique_id": r.Ref}
	if r.Type == "Redemption" {
		row["trxn_type"], row["buy_sell_type"], row["mandate_id"] = "R", "", ""
		if !r.Units.IsZero() {
			row["redemption_units"] = r.Units.Fixed(3)
		}
		if r.AllUnits {
			row["order_amount"], row["redemption_units"] = "", ""
		}
		if r.DPTxn != "P" {
			row["account_no"] = r.BankAccount // mandatory for a demat redemption
		}
	}
	m, err := n.post(ctx, cn, "transaction/NORMAL", "transaction_details", []map[string]string{row})
	if err != nil {
		return mfReply{}, err
	}
	rec := nseFirst(m)
	if rec == nil {
		raw, _ := json.Marshal(m)
		return mfReply{}, fmt.Errorf("NSE MF Invest's answer has no order: %s", trimBody(raw))
	}
	st := nseStr(rec["trxn_status"])
	return mfReply{OK: strings.EqualFold(st, "TRXN SUCCESS"), OrderNo: nseStr(rec["trxn_order_id"]), Status: st, Remark: nseStr(rec["trxn_remark"])}, nil
}

// Switch places a switch (transaction/SWITCH).
func (n nseMFInvest) Switch(ctx context.Context, cn mfConn, r mfOrderReq) (mfReply, error) {
	row := map[string]string{"order_ref_number": r.Ref, "from_scheme_code": r.SchemeCode, "to_scheme_code": r.ToSchemeCode, "buy_sell_type": "",
		"client_code": r.ClientCode, "demat_physical": r.DPTxn, "amount": nseAmt(r), "units": "", "all_units": yn(r.AllUnits), "folio_no": r.Folio,
		"remarks": r.Remarks, "kyc_flag": "Y", "sub_broker_code": "", "euin_number": r.EUIN, "euin_declaration": yn(r.EUIN != ""), "sub_broker_arn": "",
		"mobile_no": r.Mobile, "email": r.Email, "filler1": "", "filler2": "", "filler3": "", "trxn_so_order_id": "", "trxn_si_order_id": "",
		"trxn_status": "", "trxn_remark": "", "member_unique_id": r.Ref}
	if !r.Units.IsZero() {
		row["units"], row["amount"] = r.Units.Fixed(3), ""
	}
	if r.DPTxn != "P" {
		row["all_units"] = "N" // must be N for demat
	}
	m, err := n.post(ctx, cn, "transaction/SWITCH", "transaction_details", []map[string]string{row})
	if err != nil {
		return mfReply{}, err
	}
	rec := nseFirst(m)
	if rec == nil {
		return mfReply{}, fmt.Errorf("NSE MF Invest's answer has no switch")
	}
	st := nseStr(rec["trxn_status"])
	rep := mfReply{OK: strings.EqualFold(st, "TRXN SUCCESS"), OrderNo: nseStr(rec["trxn_so_order_id"]), Status: st, Remark: nseStr(rec["trxn_remark"])}
	if si := nseStr(rec["trxn_si_order_id"]); si != "" {
		rep.Remark = strings.TrimSpace(rep.Remark + " (switch-in order " + si + ")")
	}
	return rep, nil
}

// CancelOrder cancels a normal or switch order (cancellation/ORDER_CAN).
func (n nseMFInvest) CancelOrder(ctx context.Context, cn mfConn, ucc, orderNo, remark string) (mfReply, error) {
	m, err := n.post(ctx, cn, "cancellation/ORDER_CAN", "can_data", []map[string]string{{"client_code": ucc, "order_no": orderNo, "remarks": remark, "can_status": "", "can_remark": ""}})
	if err != nil {
		return mfReply{}, err
	}
	rec := nseFirst(m)
	if rec == nil {
		return mfReply{}, fmt.Errorf("NSE MF Invest's answer has no cancellation")
	}
	st := nseStr(rec["can_status"])
	return mfReply{OK: strings.EqualFold(st, "CAN SUCCESS"), OrderNo: orderNo, Status: st, Remark: nseStr(rec["can_remark"])}, nil
}

// RegisterSIP registers an XSIP (registration/product/XSIP), debited by
// the investor's approved mandate.
func (n nseMFInvest) RegisterSIP(ctx context.Context, cn mfConn, r mfSIPReq) (mfReply, error) {
	if r.MandateID == "" {
		return mfReply{}, invalid("an NSE XSIP needs the investor's approved mandate")
	}
	if r.AMCCode == "" {
		return mfReply{}, invalid("the scheme has no NSE AMC code: load NSE's scheme file with the AMC code column")
	}
	start := ""
	if t, err := time.Parse("2006-01-02", r.StartDate); err == nil {
		start = t.Format("02/01/2006")
	}
	mode := "D"
	if r.DPTxn == "P" {
		mode = "P"
	}
	row := map[string]string{"amc_code": r.AMCCode, "sch_code": r.SchemeCode, "client_code": r.ClientCode, "bank_ref_no": "", "trans_mode": mode,
		"dp_txn_mode": r.DPTxn, "start_date": start, "frequency_type": r.Frequency, "frequency_allowed": "1", "installment_amount": r.Amount.String(),
		"status": "1", "member_code": cn.Member, "folio_no": r.Folio, "sip_remarks": r.Remarks, "installment_no": strconv.Itoa(r.Installments),
		"convenience_fee": "0", "xsip_mandate_id": r.MandateID, "sub_broker_code": "", "euin_number": r.EUIN, "euin_declaration": yn(r.EUIN != ""),
		"dpc_flag": "Y", "first_order_today": yn(r.FirstOrder), "isip_mandate": "", "sub_broker_arn": "", "end_date": "",
		"primary_holder_mobile": r.Mobile, "primary_holder_email": r.Email, "step_up_required": "N", "step_up_start_date": "", "step_up_end_date": "",
		"step_up_frequency": "", "step_up_amount": "", "filler_1": "", "filler_2": "", "filler_3": "", "filler_4": "", "filler_5": "",
		"reg_id": "", "reg_status": "", "reg_remark": "", "member_unique_id": r.Ref}
	m, err := n.post(ctx, cn, "registration/product/XSIP", "reg_data", []map[string]string{row})
	if err != nil {
		return mfReply{}, err
	}
	rec := nseFirst(m)
	if rec == nil {
		return mfReply{}, fmt.Errorf("NSE MF Invest's answer has no registration")
	}
	st := nseStr(rec["reg_status"])
	return mfReply{OK: strings.EqualFold(st, "REG_SUCCESS"), RegID: nseStr(rec["reg_id"]), Status: st, Remark: nseStr(rec["reg_remark"])}, nil
}

// nseName splits a name into first, middle and last.
func nseName(n string) (string, string, string) {
	f := strings.Fields(n)
	switch len(f) {
	case 0:
		return "", "", ""
	case 1:
		return f[0], "", ""
	case 2:
		return f[0], "", f[1]
	}
	return f[0], strings.Join(f[1:len(f)-1], " "), f[len(f)-1]
}

func nseDate(d string) string {
	if t, err := time.Parse("2006-01-02", d); err == nil {
		return t.Format("02/01/2006")
	}
	return ""
}

// nseUCC183 maps a client to the 183-column registration (175 fields).
// Fields Ledgerline has no data for are sent empty.
func nseUCC183(c Client, p MFProfile) map[string]string {
	first, middle, last := nseName(c.Name)
	r := map[string]string{"client_code": c.UCC, "primary_holder_first_name": first, "primary_holder_middle_name": middle, "primary_holder_last_name": last,
		"tax_status": p.TaxStatus, "gender": p.Gender, "primary_holder_dob_incorporation": nseDate(c.DOB), "occupation_code": p.Occupation, "holding_nature": "SI",
		"primary_holder_pan_exempt": "N", "primary_holder_pan": c.PAN, "client_type": "P", "pms": "N",
		"account_type_1": p.AccountType, "account_no_1": c.Bank.Account, "ifsc_code_1": c.Bank.IFSC, "default_bank_flag_1": "Y",
		"cheque_name": "", "div_pay_mode": "02", "address_1": p.Address, "city": c.City, "state": p.StateCode, "pincode": p.Pincode, "country": "India",
		"email": c.Email, "communication_mode": "E", "foreign_address_country": "", "indian_mobile_no": c.Mobile, "primary_holder_kyc_type": "K",
		"aadhaar_updated": "", "paperless_flag": "Z", "mobile_declaration_flag": "SE", "email_declaration_flag": "SE",
		"nomination_opt": yn(!p.NomOptOut && len(p.Nominees) > 0), "nomination_authentication": firstOf(p.NomAuth, "O"), "nominee_soa": "N"}
	if c.CKYC != "" {
		r["primary_holder_kyc_type"], r["primary_holder_ckyc_number"] = "C", c.CKYC
	}
	if p.Mode == "Demat" {
		r["client_type"] = "D"
		id := strings.ToUpper(strings.TrimSpace(c.Demat.ID))
		if strings.EqualFold(c.Demat.Depository, "NSDL") {
			r["default_dp"] = "NSDL"
			if len(id) == 16 {
				r["nsdldpid"], r["nsdlcltid"] = id[:8], id[8:]
			}
		} else {
			r["default_dp"] = "CDSL"
			if len(id) == 16 {
				r["cdsl_dpid"], r["cdslcltid"] = id[:8], id[8:]
			}
		}
	}
	if p.NomOptOut {
		r["nomination_authentication"] = ""
	}
	for i, nm := range p.Nominees {
		k := fmt.Sprintf("nominee_%d_", i+1)
		minor := nm.DOB != "" && nm.DOB > time.Now().AddDate(-18, 0, 0).Format("2006-01-02")
		r[k+"name"], r[k+"relationship"], r[k+"applicable"], r[k+"dob"], r[k+"minor_flag"] = nm.Name, nm.Relation, nm.Share, nseDate(nm.DOB), yn(minor)
		r[k+"guardian"], r[k+"identity_type"], r[k+"identity_number"], r[k+"email"], r[k+"mobile"] = nm.Guardian, nm.IDType, nm.ID, nm.Email, nm.Mobile
		r[k+"country"] = "India"
	}
	return r
}

// RegisterClient sends the 183-column client registration.
func (n nseMFInvest) RegisterClient(ctx context.Context, cn mfConn, c Client, p MFProfile) (mfReply, error) {
	m, err := n.post(ctx, cn, "registration/CLIENTCOMMON183", "reg_details", []map[string]string{nseUCC183(c, p)})
	if err != nil {
		return mfReply{}, err
	}
	rec := nseFirst(m)
	if rec == nil {
		rec = m
	}
	st := firstOf(nseStr(rec["reg_status"]), firstOf(nseStr(rec["status"]), nseStr(m["status"])))
	remark := firstOf(nseStr(rec["reg_remark"]), firstOf(nseStr(rec["remarks"]), nseStr(m["message"])))
	u := strings.ToUpper(st)
	ok := !strings.Contains(u, "FAIL") && !strings.Contains(u, "ERROR") && !strings.Contains(u, "REJECT")
	return mfReply{OK: ok, Status: st, Remark: remark}, nil
}

func (nseMFInvest) CancelSIP(context.Context, mfConn, mfSIPReq) (mfReply, error) {
	return mfReply{}, mfNoAPI(MFNSE, "XSIP cancellation", "the XSIP cancellation API's section of NSE's specification could not be read; cancel on NSE MF Invest and record it here")
}

func (nseMFInvest) RegisterMandate(context.Context, mfConn, mfMandateReq) (mfReply, error) {
	return mfReply{}, mfNoAPI(MFNSE, "mandate registration", "the mandate registration API's section of NSE's specification could not be read; register the mandate on NSE MF Invest and record it here")
}

func (nseMFInvest) MandateStatus(context.Context, mfConn, string, string) ([]mfMandateRow, error) {
	return nil, mfNoAPI(MFNSE, "mandate status", "the mandate status report API's section could not be read; load NSE's mandate report through MF imports")
}

func (nseMFInvest) OrderStatus(context.Context, mfConn, string, string) ([]mfStatusRow, error) {
	return nil, mfNoAPI(MFNSE, "order status", "the order status report API's section could not be read; load NSE's order status report through MF imports")
}

func (nseMFInvest) Allotments(context.Context, mfConn, string, string) ([]mfAllotRow, error) {
	return nil, mfNoAPI(MFNSE, "allotment statement", "the allotment statement API's section could not be read; load NSE's allotment statement through MF imports")
}
