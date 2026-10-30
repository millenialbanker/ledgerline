package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"net/url"
	"regexp"
	"strings"
	"time"

	"ledgerline/internal/docs"
)

// ---- CDSL APIs the client confirms with an OTP on CDSL's page ----
//
// Account transfer cum closure (CDSL API Handbook §5.1) and add beneficiary
// for off-market (§5.2) are "redirect" APIs: the DP's page posts a form to
// CDSL from the client's browser, CDSL shows the client the details and
// sends an OTP to the mobile and email it holds, and CDSL posts the result
// back, encrypted with the DP's API key, to the address given.
//
// In Ledgerline a checker approves the request first; the client then gets
// a link (by email and SMS, and shown once to the staff member, for a
// client at the branch). Opening the link builds the request afresh, with
// a new request ID each time (CDSL wants one never used before), and takes
// the client to CDSL. The answer comes back to an address that carries the
// request's token; it is decrypted with the API key, so a forged one is
// refused.
//
//   - A closure: the DP's "Closure" request (dp.go) is sent this way once
//     the checker approves it, the DP request reads "Sent to CDSL", and
//     the account is closed only when CDSL confirms the transfer.
//   - A beneficiary: up to ten buyer accounts the client may transfer to
//     off-market; each buyer's result is kept.

var (
	reBOIDCDSL = regexp.MustCompile(`^[0-9]{16}$`)
	reBOIDNSDL = regexp.MustCompile(`^IN[0-9A-Z]{14}$`)
)

// CDSLBuyer is one buyer account in an add-beneficiary request.
type CDSLBuyer struct {
	BOID   string `json:"bo_id"`
	Type   string `json:"type"`   // C (CDSL) or N (NSDL)
	Exempt string `json:"exempt"` // Y: the buyer is exempt from PAN
	PAN    string `json:"pan,omitempty"`
	ReqID  string `json:"req_id,omitempty"`
	Status string `json:"status,omitempty"` // Added, Failed
	Error  string `json:"error,omitempty"`
	ResID  string `json:"cdsl_ref,omitempty"`
}

// newBeneficiaryRequest checks the buyers as CDSL would (§5.2 error list)
// and raises the request for a checker.
func (o *Ops) newBeneficiaryRequest(ctx context.Context, tenant, actor string, c Client, buyers []CDSLBuyer) (CDSLRequest, error) {
	b := o.bo(ctx, tenant, c)
	if b.Depository != "CDSL" || !reBOIDCDSL.MatchString(b.BOID) {
		return CDSLRequest{}, invalid("%s has no CDSL demat account with the DP", c.UCC)
	}
	if b.Status != "Active" && b.Status != "Frozen" {
		return CDSLRequest{}, fmt.Errorf("%w: the demat account is %s", ErrState, strings.ToLower(b.Status))
	}
	if len(buyers) == 0 || len(buyers) > 10 {
		return CDSLRequest{}, invalid("give between 1 and 10 buyer accounts")
	}
	var probs []string
	seen := map[string]bool{}
	var out []CDSLBuyer
	for i, x := range buyers {
		who := fmt.Sprintf("Buyer %d", i+1)
		x.BOID, x.Type, x.Exempt, x.PAN = strings.ToUpper(strings.TrimSpace(x.BOID)), strings.ToUpper(strings.TrimSpace(x.Type)), strings.ToUpper(strings.TrimSpace(x.Exempt)), strings.ToUpper(strings.TrimSpace(x.PAN))
		if x.Type == "" {
			x.Type = "C"
			if strings.HasPrefix(x.BOID, "IN") {
				x.Type = "N"
			}
		}
		if x.Exempt == "" {
			x.Exempt = "N"
		}
		switch {
		case x.Type != "C" && x.Type != "N":
			probs = append(probs, who+": the depository must be C (CDSL) or N (NSDL)")
		case x.Type == "C" && !reBOIDCDSL.MatchString(x.BOID):
			probs = append(probs, who+": a CDSL BO ID is 16 digits")
		case x.Type == "N" && !reBOIDNSDL.MatchString(x.BOID):
			probs = append(probs, who+": an NSDL account is IN followed by 14 letters or digits (DP ID and client ID)")
		}
		if x.BOID == b.BOID {
			probs = append(probs, who+": the buyer cannot be the client's own account")
		}
		if seen[x.BOID] {
			probs = append(probs, who+": the same account is given twice")
		}
		seen[x.BOID] = true
		switch x.Exempt {
		case "N":
			if !rePAN.MatchString(x.PAN) {
				probs = append(probs, who+": give the buyer's PAN (first holder), or mark the buyer PAN-exempt")
			}
		case "Y":
			if x.PAN != "" {
				probs = append(probs, who+": a PAN-exempt buyer is sent without a PAN; leave it empty")
			}
		default:
			probs = append(probs, who+": PAN-exempt must be Y or N")
		}
		x.Status, x.Error, x.ResID, x.ReqID = "", "", "", ""
		out = append(out, x)
	}
	if len(probs) > 0 {
		return CDSLRequest{}, invalid("%s", strings.Join(probs, "; "))
	}
	if c.Mobile == "" && c.Email == "" {
		return CDSLRequest{}, invalid("CDSL sends the OTP to the mobile and email on the demat account; the client has neither on record")
	}
	r := CDSLRequest{Kind: CDSLBeneficiary, UCC: c.UCC, Name: c.Name, BOID: b.BOID, Buyers: out, Source: "staff", Status: crChecker, Maker: actor}
	return o.cdslNewRequest(ctx, tenant, actor, r)
}

// ---- closure (§5.1), from the DP's closure request ----

// cdslStartClosure sends an approved closure to CDSL as an account
// transfer cum closure when that API is on and the account is a CDSL one.
// It reports whether it took the closure over.
func (o *Ops) cdslStartClosure(ctx context.Context, tenant, actor string, dpr DPRequest, c Client, b BOAccount) (bool, error) {
	x, ok := o.cdslConnFor(ctx, tenant)
	if !ok || !x.On(cdslFamTransfer) || b.Depository != "CDSL" || b.BOID == "" {
		return false, nil
	}
	if !reBOIDCDSL.MatchString(b.BOID) {
		return true, invalid("the BO ID on record (%s) is not 16 digits", cdslMaskBO(b.BOID))
	}
	if !rePAN.MatchString(c.PAN) {
		return true, invalid("the client's PAN on record is not valid; CDSL needs it for the transfer")
	}
	if old, ok := o.cdslBySource(ctx, tenant, dpr.ID); ok && old.open() {
		return true, fmt.Errorf("%w: %s is already with CDSL (%s)", ErrState, dpr.ID, old.ID)
	}
	f := map[string]string{}
	if t := dpr.Detail["transfer_to"]; t != "" {
		f["transfer_to"] = t
	}
	r := CDSLRequest{Kind: CDSLTransfer, UCC: c.UCC, Name: c.Name, BOID: b.BOID, Source: dpr.ID, Fields: f, Maker: dpr.Maker, Checker: actor, Status: crApproved}
	r, err := o.cdslNewRequest(ctx, tenant, actor, r)
	if err != nil {
		return true, err
	}
	r.event(o.now().Format(time.RFC3339), actor, "Approved with "+dpr.ID)
	_, _ = o.cdslSend(ctx, tenant, actor, x, &r)
	return true, o.cdslSave(ctx, tenant, actor, &r)
}

// cdslFinishClosure closes the account once CDSL has transferred it: the
// holdings leave it and the DP's closure request is completed.
func (o *Ops) cdslFinishClosure(ctx context.Context, tenant, actor string, r *CDSLRequest, note string) error {
	c, err := o.Client(ctx, tenant, r.UCC)
	if err != nil {
		return err
	}
	b := o.bo(ctx, tenant, c)
	date := o.businessDate(ctx, tenant)
	to := firstOf(r.Fields["destination"], r.Fields["transfer_to"])
	hs, _ := o.Holdings(ctx, tenant, c.UCC)
	for _, h := range hs {
		if h.Qty <= 0 {
			continue
		}
		q := h.Qty
		if _, err := docs.Update(ctx, o.Docs, tenant, cHolding, holdingID(c.UCC, h.ISIN), actor, func(x *Holding) error { x.Qty, x.Pledged = 0, 0; return nil }); err != nil {
			return err
		}
		_ = o.dpTxn(ctx, tenant, actor, DPTxn{Date: date, UCC: c.UCC, ISIN: h.ISIN, Symbol: h.Symbol, Qty: -q, Kind: "Account transfer out", Ref: r.ID})
	}
	b.Status, b.ClosedOn, b.Freeze, b.FreezeWhy = "Closed", date, "", ""
	if err := docs.Upsert(ctx, o.Docs, tenant, cBO, b.UCC, b, actor); err != nil {
		return err
	}
	if strings.HasPrefix(r.Source, "DPR-") {
		_, _ = docs.Update(ctx, o.Docs, tenant, cDPReq, r.Source, actor, func(x *DPRequest) error {
			x.Status, x.Note = "Completed", strings.TrimSpace("CDSL "+r.ID+" "+firstOf(r.CDSLRef, "")+" "+note)
			return nil
		})
		o.closeWork(ctx, tenant, actor, "dp", r.Source, "closed")
	}
	o.notify(ctx, tenant, actor, "cdsl.account_closed", c.UCC, map[string]string{"bo_id": cdslMaskBO(b.BOID), "to": cdslMaskBO(to)})
	o.log(ctx, tenant, actor, "dp.bo_closed", c.UCC, map[string]any{"bo": cdslMaskBO(b.BOID), "to": cdslMaskBO(to), "request": r.ID})
	return nil
}

// ---- the client's link ----

// cdslClientLink makes the client's link and sends it.
func (o *Ops) cdslClientLink(ctx context.Context, tenant, actor string, x cdslConn, r *CDSLRequest) (string, error) {
	if x.Callback == "" {
		return "", invalid("enter Ledgerline's public address in the CDSL APIs connection (callback address): the client's link and CDSL's answer use it")
	}
	ret := x.Callback + "/hooks/" + url.PathEscape(tenant) + "/cdsl/return/" + strings.Repeat("x", 36)
	if len(ret) > 200 {
		return "", invalid("CDSL takes a return address of at most 200 characters; the public address is too long")
	}
	tok, err := o.cdslNewToken(ctx, tenant, actor, r, "link")
	if err != nil {
		return "", err
	}
	until := o.now().Add(72 * time.Hour)
	r.Status, r.LinkUntil, r.Error = crClient, until.Format(time.RFC3339), ""
	link := x.Callback + "/hooks/" + url.PathEscape(tenant) + "/cdsl/go/" + tok
	what := map[string]string{CDSLTransfer: "transfer your demat account " + cdslMaskBO(r.BOID) + " and close it", CDSLBeneficiary: "add beneficiary accounts to your demat account " + cdslMaskBO(r.BOID)}[r.Kind]
	o.notify(ctx, tenant, actor, "cdsl.client_link", r.UCC, map[string]string{"what": what, "link": link, "until": until.Format("02 Jan 2006 15:04")})
	r.event(o.now().Format(time.RFC3339), actor, "Link sent to the client (valid till "+until.Format("02 Jan 15:04")+")")
	o.log(ctx, tenant, actor, "cdsl.client_link", r.ID, map[string]any{"kind": r.Kind, "ucc": r.UCC})
	return link, nil
}

// CDSLPage is what the client sees: the step to CDSL, or the result.
type CDSLPage struct {
	Title   string
	Message string
	Action  string      // CDSL's address the form posts to
	Fields  [][2]string // the form's fields, in order
	Nonce   string
	OK      bool
}

// CDSLPageHTML renders a CDSLPage.
var CDSLPageHTML = template.Must(template.New("cdsl").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>{{.Title}}</title>
<style>body{font:16px/1.5 system-ui,sans-serif;max-width:560px;margin:40px auto;padding:0 16px;color:#1d2433}h1{font-size:20px}button{font:inherit;padding:10px 18px;border:0;border-radius:6px;background:#1f5fbf;color:#fff;cursor:pointer}.bad{color:#a32020}</style></head>
<body><h1>{{.Title}}</h1><p{{if not .OK}} class="bad"{{end}}>{{.Message}}</p>
{{if .Action}}<form id="go" method="post" action="{{.Action}}">{{range .Fields}}<input type="hidden" name="{{index . 0}}" value="{{index . 1}}">{{end}}<button type="submit">Continue to CDSL</button></form>
<script nonce="{{.Nonce}}">document.getElementById('go').submit()</script>{{end}}</body></html>`))

// CDSLGoPage builds the form to CDSL for the client's link. Each opening
// gets a new request ID.
func (o *Ops) CDSLGoPage(ctx context.Context, tenant, tok string) (CDSLPage, error) {
	bad := CDSLPage{Title: "Link not valid", Message: "This link is not valid or has expired. Ask your broker for a new one."}
	r, err := o.cdslByToken(ctx, tenant, tok)
	if err != nil || (r.Kind != CDSLTransfer && r.Kind != CDSLBeneficiary) {
		return bad, ErrNotFound
	}
	if r.Status != crClient {
		bad.Message = "This request is " + strings.ToLower(r.Status) + "; there is nothing more to do here."
		return bad, fmt.Errorf("%w: %s", ErrState, r.Status)
	}
	if t, err := time.Parse(time.RFC3339, r.LinkUntil); err != nil || o.now().After(t) {
		return bad, fmt.Errorf("%w: the link has expired", ErrState)
	}
	x, ok := o.cdslConnFor(ctx, tenant)
	_, fam := cdslKindInfo(r.Kind)
	if !ok || !x.On(fam) {
		bad.Message = "This service is not available just now. Please contact your broker."
		return bad, fmt.Errorf("%w: the API is off", ErrState)
	}
	actor := "client:" + r.UCC
	ret := x.Callback + "/hooks/" + url.PathEscape(tenant) + "/cdsl/return/" + tok
	reqid, err := o.cdslReqID(ctx, tenant, actor, 24)
	if err != nil {
		return bad, err
	}
	c, err := o.Client(ctx, tenant, r.UCC)
	if err != nil {
		return bad, err
	}
	var payload any
	var fields [][2]string
	var kept string
	switch r.Kind {
	case CDSLTransfer:
		p := struct {
			PAN      string `json:"pan"`
			BOID     string `json:"boid"`
			Remarks  string `json:"remarks"`
			Callback string `json:"callbackurl"`
		}{c.PAN, r.BOID, trunc("Closure "+r.Source+" "+r.ID, 100), ret}
		payload = p
		p.PAN, p.Callback = MaskPAN(p.PAN), "…"
		b, _ := json.Marshal(p)
		kept = string(b)
	case CDSLBeneficiary:
		sreq, err := o.cdslReqID(ctx, tenant, actor, 24)
		if err != nil {
			return bad, err
		}
		type byr struct {
			ReqID  string `json:"buyerboreqid"`
			BOID   string `json:"buyerboid"`
			Type   string `json:"buyerbotype"`
			Exempt string `json:"buyerexflg"`
			PAN    string `json:"buyerpan"`
		}
		p := struct {
			ReqID  string `json:"sellerboreqid"`
			BOID   string `json:"sellerboid"`
			Return string `json:"returnurl"`
			Buyers []byr  `json:"byrdtls"`
		}{ReqID: sreq, BOID: r.BOID, Return: ret}
		for i := range r.Buyers {
			r.Buyers[i].ReqID = sreq[:22] + fmt.Sprintf("%02d", i+1)
			bx := r.Buyers[i]
			p.Buyers = append(p.Buyers, byr{bx.ReqID, bx.BOID, bx.Type, bx.Exempt, bx.PAN})
		}
		payload = p
		p.Return = "…"
		b, _ := json.Marshal(p)
		kept = string(b)
	}
	sealed, err := x.seal(payload)
	if err != nil {
		return bad, err
	}
	switch r.Kind {
	case CDSLTransfer:
		fields = [][2]string{{"dpid", x.DPID}, {"reqid", reqid}, {"version", "1.0"}, {"bodetails", sealed}}
	case CDSLBeneficiary:
		// The form in §5.2 has dpid, reqid and slrbyrdtls; its error list
		// also asks for a version, which is sent as for the other APIs.
		fields = [][2]string{{"dpid", x.DPID}, {"reqid", reqid}, {"version", "1.0"}, {"slrbyrdtls", sealed}}
	}
	path := cdslPathTransfer
	if r.Kind == CDSLBeneficiary {
		path = cdslPathBeneficiary
	}
	r.Ref, r.Refs, r.Tries = reqid, append(r.Refs, reqid), r.Tries+1
	r.Request, r.SentAt = kept, o.now().Format(time.RFC3339)
	r.event(r.SentAt, actor, "The client opened the link; sent to CDSL with request ID "+reqid)
	if err := o.cdslSave(ctx, tenant, actor, &r); err != nil {
		return bad, err
	}
	nonce, _ := newToken()
	title := map[string]string{CDSLTransfer: "Transfer and close your demat account", CDSLBeneficiary: "Add beneficiary accounts"}[r.Kind]
	return CDSLPage{Title: title, Message: "Taking you to CDSL. Check the details there and confirm with the OTP CDSL sends to your registered mobile and email.", Action: x.URL(path), Fields: fields, Nonce: nonce, OK: true}, nil
}

// CDSLReturn takes CDSL's answer posted back through the client's browser.
func (o *Ops) CDSLReturn(ctx context.Context, tenant, tok string, form url.Values) (CDSLPage, error) {
	bad := CDSLPage{Title: "Not recognised", Message: "We could not match this answer to a request. Please contact your broker."}
	r, err := o.cdslByToken(ctx, tenant, tok)
	if err != nil {
		return bad, ErrNotFound
	}
	x, ok := o.cdslConnFor(ctx, tenant)
	if !ok {
		return bad, fmt.Errorf("%w: connection not set", ErrState)
	}
	actor := "cdsl:return"
	now := o.now().Format(time.RFC3339)
	field := map[string]string{CDSLTransfer: "bodetails", CDSLBeneficiary: "slrbyrresdtls"}[r.Kind]
	enc := form.Get(field)
	if enc == "" {
		return bad, invalid("the answer has no %s", field)
	}
	var m map[string]any
	if err := x.open(enc, &m); err != nil {
		o.log(ctx, tenant, actor, "cdsl.return_unreadable", r.ID, map[string]any{"error": err.Error()})
		return bad, invalid("the answer could not be decrypted")
	}
	if rid := strings.TrimSpace(form.Get("reqid")); rid != "" && !contains(r.Refs, rid) {
		o.log(ctx, tenant, actor, "cdsl.return_mismatch", r.ID, map[string]any{"reqid": rid})
		return bad, invalid("the answer's request ID is not this request's")
	}
	var page CDSLPage
	switch r.Kind {
	case CDSLTransfer:
		page = o.cdslTransferAnswer(ctx, tenant, actor, x, &r, m)
	case CDSLBeneficiary:
		page = o.cdslBeneficiaryAnswer(ctx, tenant, actor, &r, m)
	default:
		return bad, ErrNotFound
	}
	b, _ := json.Marshal(m)
	r.Response = string(b)
	r.Updated = now
	if err := o.cdslSave(ctx, tenant, actor, &r); err != nil {
		return bad, err
	}
	if !r.open() || r.Status == crRejected || r.Status == crFailed {
		o.closeWork(ctx, tenant, actor, "cdslbo", r.ID, strings.ToLower(r.Status))
	}
	o.log(ctx, tenant, actor, "cdsl.return", r.ID, map[string]any{"kind": r.Kind, "status": r.Status, "bo": cdslMaskBO(r.BOID)})
	return page, nil
}

// cdslTransferAnswer reads §5.1's answers: the first, after the client's
// OTP, lists the accounts the client ticked; the final one gives CDSL's
// transaction ID with the source and destination accounts.
func (o *Ops) cdslTransferAnswer(ctx context.Context, tenant, actor string, x cdslConn, r *CDSLRequest, m map[string]any) CDSLPage {
	now := o.now().Format(time.RFC3339)
	inner := m["bodetails"]
	if s, ok := inner.(string); ok && strings.TrimSpace(s) != "" {
		var v any
		if x.open(s, &v) == nil {
			inner = v
			m["bodetails"] = v
		}
	}
	ok := cdslStr(m, "reqstatus") == "0"
	why := cdslExplain(cdslStr(m, "reserror"), cdslStr(m, "reserrmsg"))
	trxn := cdslStr(m, "trxnid")
	final := trxn != ""
	if d, isMap := inner.(map[string]any); isMap {
		final = final || cdslStr(d, "sourceboid") != ""
		if dst := cdslStr(d, "destinationboid"); dst != "" {
			if r.Fields == nil {
				r.Fields = map[string]string{}
			}
			r.Fields["destination"] = dst
			if t := r.Fields["transfer_to"]; t != "" && t != dst {
				r.Warnings = append(r.Warnings, "CDSL transferred to "+dst+", not to "+t+" given on the closure request")
			}
		}
		if src := cdslStr(d, "sourceboid"); src != "" && src != r.BOID {
			r.Warnings = append(r.Warnings, "CDSL's source account "+cdslMaskBO(src)+" is not the account being closed")
		}
	}
	if !final {
		if !ok {
			r.Status, r.Error = crFailed, "Not confirmed at CDSL: "+why
			r.event(now, actor, r.Error)
			return CDSLPage{Title: "Not completed", Message: "CDSL did not take the request: " + why + ". Your broker will send you a new link if needed."}
		}
		var ticked []string
		if l, isList := inner.([]any); isList {
			for _, e := range l {
				if d, ok := e.(map[string]any); ok && cdslStr(d, "ischecked") == "true" {
					st := "accepted"
					if cdslStr(d, "reqstatus") != "0" {
						st = "failed: " + cdslExplain(cdslStr(d, "errorcode"), cdslStr(d, "errormsg"))
					}
					ticked = append(ticked, cdslMaskBO(cdslStr(d, "boid"))+" "+st)
				}
			}
		}
		r.Status = crConfirmed
		r.event(now, actor, "The client confirmed with the OTP at CDSL"+map[bool]string{true: " (" + strings.Join(ticked, "; ") + ")", false: ""}[len(ticked) > 0]+"; waiting for CDSL to complete the transfer")
		return CDSLPage{Title: "Request received", Message: "CDSL has your confirmation. The transfer and closure are completed by CDSL; we will tell you when it is done.", OK: true}
	}
	r.CDSLRef = firstOf(trxn, r.CDSLRef)
	if !ok {
		r.Status, r.Error = crRejected, "CDSL did not complete the transfer: "+why
		r.event(now, actor, r.Error)
		o.raiseWork(ctx, tenant, "cdslbo", r.ID, "CDSL did not complete the closure of "+r.UCC, r.Error+". Retry with a new link or close the account in CDAS.", r.UCC, RoleMaker, "cdslreq", "")
		return CDSLPage{Title: "Not completed", Message: "CDSL could not complete the transfer: " + why + "."}
	}
	if err := o.cdslFinishClosure(ctx, tenant, actor, r, "transfer "+trxn); err != nil {
		r.Error = err.Error()
		r.event(now, actor, "CDSL completed the transfer but the account could not be closed here: "+err.Error())
		return CDSLPage{Title: "Transfer completed", Message: "CDSL completed the transfer. Your broker will finish closing the account.", OK: true}
	}
	r.Status, r.DoneAt = crCompleted, now
	r.event(now, actor, "CDSL completed the transfer (transaction "+trxn+"); account closed")
	return CDSLPage{Title: "Transfer completed", Message: "CDSL has transferred your holdings and the account is being closed.", OK: true}
}

// cdslBeneficiaryAnswer reads §5.2's answer, buyer by buyer.
func (o *Ops) cdslBeneficiaryAnswer(ctx context.Context, tenant, actor string, r *CDSLRequest, m map[string]any) CDSLPage {
	now := o.now().Format(time.RFC3339)
	why := cdslExplain(cdslStr(m, "reserror"), cdslStr(m, "reserrmsg"))
	byID := map[string]int{}
	for i, b := range r.Buyers {
		byID[b.ReqID] = i
		byID[b.BOID] = i
	}
	added, failed := 0, 0
	if l, ok := m["byrresdtls"].([]any); ok {
		for _, e := range l {
			d, ok := e.(map[string]any)
			if !ok {
				continue
			}
			i, found := byID[cdslStr(d, "buyerboreqid")]
			if !found {
				i, found = byID[strings.ToUpper(cdslStr(d, "buyerboid"))]
			}
			if !found {
				continue
			}
			if cdslStr(d, "byrreqstatus") == "0" {
				r.Buyers[i].Status, r.Buyers[i].Error = "Added", ""
				added++
			} else {
				r.Buyers[i].Status, r.Buyers[i].Error = "Failed", cdslExplain(cdslStr(d, "errorcode"), cdslStr(d, "errormsg"))
				failed++
			}
			r.Buyers[i].ResID = cdslStr(d, "buyerresid")
		}
	}
	r.CDSLRef = firstOf(cdslStr(m, "resid"), r.CDSLRef)
	if cdslStr(m, "resstatus") != "0" {
		r.Status, r.Error = crRejected, "CDSL did not add the beneficiaries: "+why
		r.event(now, actor, r.Error)
		return CDSLPage{Title: "Not added", Message: "CDSL did not add the accounts: " + why + "."}
	}
	r.Status, r.DoneAt = crCompleted, now
	msg := fmt.Sprintf("%d buyer account(s) added at CDSL", added)
	if failed > 0 {
		msg += fmt.Sprintf(", %d not added (see each buyer)", failed)
		r.Warnings = append(r.Warnings, fmt.Sprintf("%d buyer account(s) were not added", failed))
	}
	r.event(now, actor, msg)
	return CDSLPage{Title: "Beneficiaries added", Message: msg + ".", OK: true}
}

func init() {
	EventVars["cdsl.client_link"] = []string{"what", "link", "until"}
	EventVars["cdsl.bo_opened"] = []string{"bo_id"}
	EventVars["cdsl.account_closed"] = []string{"bo_id", "to"}
	t := func(ev, ch, subj, body string) Template {
		return Template{Event: ev, Channel: ch, Category: "Transactional", Subject: subj, Body: body, Active: true}
	}
	extraTemplates = append(extraTemplates,
		t("cdsl.client_link", ChEmail, "Confirm your request at CDSL", "Dear {name},\n\nAs you asked, we have sent your request to {what} to CDSL. Open this link to see the details on CDSL's page and confirm them with the OTP CDSL sends to your registered mobile and email:\n\n{link}\n\nThe link works until {until}. If you did not ask for this, do not open it and tell us at once.\n\n{firm}"),
		t("cdsl.client_link", ChSMS, "", "Dear {name}, to {what}, confirm at CDSL with the OTP CDSL sends you: {link} (valid till {until}). Did not ask? Call us. -{firm}"),
		t("cdsl.bo_opened", ChEmail, "Your demat account is open", "Dear {name},\n\nYour demat account with us is open at CDSL. Your BO ID is {bo_id}.\n\n{firm}"),
		t("cdsl.account_closed", ChEmail, "Your demat account is closed", "Dear {name},\n\nCDSL has transferred the holdings of your demat account {bo_id} and the account is closed.\n\n{firm}"),
	)
}
