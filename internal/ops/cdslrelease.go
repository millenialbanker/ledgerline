package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// ---- CDSL margin re-pledge, normal pledge, unpledge and invocation
// (CDSL API Handbook §5.18, §5.19, §5.20) ----
//
// All three are server-to-server form posts (dpid, reqid, version and the
// encrypted pledgedtls) from the address CDSL has whitelisted; CDSL's
// answer comes back in the reply, encrypted with the same API key. Each
// goes to CDSL only after a second person approves it:
//
//   - Re-pledge: risk picks clients' margin pledges (PSNs) to re-pledge to
//     the clearing member / clearing corporation; a checker sends them.
//     NSE Clearing's re-pledge status file (Clearing data) is reconciled
//     against what CDSL accepted.
//   - Normal pledge: a client's pledge to a lender (loan against shares);
//     a DP maker enters it, a DP checker sends it.
//   - Unpledge: on the client's request (portal or desk), a checker sends
//     it. Released margin pledges come off the client's pledged quantity.
//   - Invocation (confiscation): risk raises it for an overdue debit or an
//     MTF position to be liquidated; compliance approves it and the client
//     gets notice; once the notice period is over (and the debit is still
//     unpaid) it goes to CDSL and the shares leave the client's account.

// ---- shared reply handling ----

// cplSetup is a pledgedtls for normal pledge, unpledge and confiscate
// (§5.19, §5.20). Every field is sent, empty when not used, as the
// handbook's samples do.
type cplSetup struct {
	PledgeIdentifier string         `json:"pledgeidentifier"`
	ReqTime          string         `json:"reqtime"`
	PledgorBOID      string         `json:"pledgorboid"`
	PledgeeBOID      string         `json:"pledgeeboid"`
	ExecutionDate    string         `json:"executiondate"`
	ExpiryDate       string         `json:"expirydate"`
	UCCID            string         `json:"uccid"`
	ExID             string         `json:"exid"`
	EntityIdentifier string         `json:"entityidentifier"`
	TMID             string         `json:"tmid"`
	Remarks          string         `json:"remarks"`
	ReturnURL        string         `json:"returnurl"`
	Type             string         `json:"type,omitempty"` // pledgor / pledgee (unpledge)
	ISINs            []cplSetupISIN `json:"isindtls"`
}

type cplSetupISIN struct {
	PRFNumber       string `json:"prfnumber"`
	PledgorIntRef   string `json:"pledgorintref"`
	ISINReqID       string `json:"isinreqid"`
	ISIN            string `json:"isin"`
	Quantity        string `json:"quantity"`
	Value           string `json:"value"`
	SegmentID       string `json:"segmentid"`
	CMID            string `json:"cmid"`
	ReasonCode      string `json:"reasoncode"`
	PSN             string `json:"psn,omitempty"`
	PartQuantity    string `json:"partquantity,omitempty"`
	InvocationValue string `json:"invocationvalue,omitempty"`
}

// cplSend posts a setup and reads the pledge reply. noReply is true when
// the post went out but no answer could be read: the request may or may
// not be set up at CDSL.
func (o *Ops) cplSend(ctx context.Context, cc cplConn, kind, reqID string, v any) (res cplPledgeRes, noReply bool, err error) {
	enc, err := cc.seal(v)
	if err != nil {
		return res, false, err
	}
	body, err := cc.postForm(ctx, kind, url.Values{"dpid": {cc.DPID}, "reqid": {reqID}, "version": {cplVersion}, "pledgedtls": {enc}})
	if err != nil && len(body) == 0 {
		return res, true, err
	}
	p, derr := cc.open(body, "pledgeresdtls", "encryptresdtl", "data")
	if derr != nil {
		if err == nil {
			err = derr
		}
		return res, true, err
	}
	if err := json.Unmarshal(p, &res); err != nil {
		return res, true, err
	}
	if res.ReqID != "" && res.ReqID != reqID {
		return res, true, fmt.Errorf("CDSL's answer is for request %s, not %s", res.ReqID, reqID)
	}
	return res, false, nil
}

// cplLineOK finds an ISIN line of a reply and says whether it succeeded.
func cplLineOK(res cplPledgeRes, isinReq string) (cplISINReply, bool, bool) {
	for _, l := range res.lines() {
		if strings.TrimSpace(l.ISINReqID) == isinReq {
			return l, true, strings.TrimSpace(l.ReqStatus) == "0"
		}
	}
	return cplISINReply{}, false, false
}

func cplPrice(look Lookup, isin string) money.Dec {
	if i, ok := look.ByISIN(isin); ok {
		return firstDec(i.LTP, i.Close)
	}
	return money.Dec{}
}

// ---- PSNs: what each client has pledged through the APIs ----

// PSNAvail is one pledge (PSN) set up through CDSL's APIs, with what is
// left of it.
type PSNAvail struct {
	PSN       string `json:"psn"`
	UCC       string `json:"ucc"`
	ISIN      string `json:"isin"`
	Symbol    string `json:"symbol"`
	Of        string `json:"of"` // MP, MF (pledged to the firm) or EP (to a lender)
	Ref       string `json:"ref"`
	Lender    string `json:"lender,omitempty"`
	Expiry    string `json:"expiry,omitempty"`
	MTFID     string `json:"mtf_id,omitempty"`
	Qty       int64  `json:"qty"`
	Released  int64  `json:"released"`  // unpledged or invoked, or asked to be
	Repledged int64  `json:"repledged"` // re-pledged, or being re-pledged
	Free      int64  `json:"free"`      // still pledged and not being released
}

// cplPSNs lists the PSNs of one client, or all when ucc is "".
func (o *Ops) cplPSNs(ctx context.Context, tenant, ucc string) []PSNAvail {
	by := map[string]*PSNAvail{}
	var order []string
	add := func(a PSNAvail) {
		if a.PSN == "" || (ucc != "" && a.UCC != ucc) {
			return
		}
		if x, ok := by[a.PSN]; ok {
			x.Qty += a.Qty
			return
		}
		by[a.PSN] = &a
		order = append(order, a.PSN)
	}
	ps, _ := docs.List[PledgeRequest](ctx, o.Docs, tenant, cPledge, "")
	for _, p := range ps {
		if p.Via == cplVia && p.Status == "Accepted" {
			add(PSNAvail{PSN: p.PSN, UCC: p.UCC, ISIN: p.ISIN, Symbol: p.Symbol, Of: firstOf(p.Kind, "MP"), Ref: p.ID, Qty: p.Qty, Expiry: p.Expiry, MTFID: p.MTFID})
		}
	}
	for _, n := range o.cplNormals(ctx, tenant) {
		for _, l := range n.Lines {
			if l.Status == "Done" {
				add(PSNAvail{PSN: l.PSN, UCC: n.UCC, ISIN: l.ISIN, Symbol: l.Symbol, Of: "EP", Ref: n.ID, Lender: n.Lender, Qty: l.Qty, Expiry: n.Expiry})
			}
		}
	}
	for _, r := range o.cplReleases(ctx, tenant) {
		if x, ok := by[r.PSN]; ok && !cplReleaseClosed(r.Status) {
			x.Released += r.Qty
		}
	}
	for _, b := range o.cplRepledges(ctx, tenant) {
		if b.Status == "Rejected" {
			continue
		}
		for _, l := range b.Lines {
			if x, ok := by[l.PSN]; ok && l.Status != "Failed" {
				x.Repledged += l.Qty
			}
		}
	}
	out := []PSNAvail{}
	for _, k := range order {
		x := by[k]
		x.Free = max(x.Qty-x.Released, 0)
		out = append(out, *x)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].UCC != out[j].UCC {
			return out[i].UCC < out[j].UCC
		}
		return out[i].PSN < out[j].PSN
	})
	return out
}

// cplReleaseClosed says a release no longer holds its quantity.
func cplReleaseClosed(st string) bool {
	return st == "Rejected" || st == "Failed" || st == "Cancelled"
}

// cplEncumbered is what a client has pledged, or asked to pledge, to
// lenders: it is not free to pledge again, and does not count as margin.
func (o *Ops) cplEncumbered(ctx context.Context, tenant, ucc string) map[string]int64 {
	out := map[string]int64{}
	rel := map[string]int64{}
	for _, r := range o.cplReleases(ctx, tenant) {
		if r.UCC == ucc && r.Of == "EP" && r.Status == "Done" {
			rel[r.PSN] += r.Qty
		}
	}
	for _, n := range o.cplNormals(ctx, tenant) {
		if n.UCC != ucc || n.Status == "Rejected" || n.Status == "Failed" {
			continue
		}
		for _, l := range n.Lines {
			if l.Status != "Failed" {
				out[l.ISIN] += max(l.Qty-rel[l.PSN], 0)
			}
		}
	}
	return out
}

// ---- margin re-pledge (§5.18) ----

// Repledge is one batch of clients' margin pledges re-pledged to the
// clearing member / clearing corporation.
type Repledge struct {
	ID      string         `json:"id"`
	Status  string         `json:"status"` // Pending, Rejected, Sent, Done, Partly done, Failed, No reply
	Lines   []RepledgeLine `json:"lines"`
	Maker   string         `json:"maker"`
	Checker string         `json:"checker,omitempty"`
	At      string         `json:"at"`
	ReqID   string         `json:"req_id,omitempty"`
	SentAt  string         `json:"sent_at,omitempty"`
	Result  string         `json:"result,omitempty"`
	Reason  string         `json:"reason,omitempty"`
}

// RepledgeLine is one PSN (or part of it) in a re-pledge.
type RepledgeLine struct {
	UCC     string `json:"ucc"`
	ISIN    string `json:"isin"`
	Symbol  string `json:"symbol"`
	PSN     string `json:"psn"`
	Qty     int64  `json:"qty"`
	Value   string `json:"value"`
	BOReqID string `json:"bo_req_id"`
	Status  string `json:"status"` // Pending, Done, Failed
	ErrCode string `json:"err_code,omitempty"`
	ErrMsg  string `json:"err_msg,omitempty"`
}

// RepledgeAsk is one PSN asked to be re-pledged.
type RepledgeAsk struct {
	PSN string `json:"psn"`
	Qty int64  `json:"qty"`
}

func (o *Ops) cplRepledges(ctx context.Context, tenant string) []Repledge {
	rs, _ := docs.List[Repledge](ctx, o.Docs, tenant, cCPLRepledge, "")
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].At > rs[j].At })
	return rs
}

// cplRepledgable lists margin pledges (MP) with quantity not yet re-pledged.
func (o *Ops) cplRepledgable(ctx context.Context, tenant string) []PSNAvail {
	out := []PSNAvail{}
	for _, a := range o.cplPSNs(ctx, tenant, "") {
		if a.Of == "MP" && a.Free-a.Repledged > 0 {
			out = append(out, a)
		}
	}
	return out
}

// RequestRepledge records a re-pledge batch (maker). A checker sends it.
func (o *Ops) RequestRepledge(ctx context.Context, tenant, actor string, asks []RepledgeAsk) (Repledge, error) {
	if err := o.require(ctx, tenant, actor, "Re-pledging clients' collateral", RoleRisk, RoleMaker); err != nil {
		return Repledge{}, err
	}
	if len(asks) == 0 || len(asks) > 100 {
		return Repledge{}, invalid("choose 1 to 100 pledges to re-pledge")
	}
	avail := map[string]PSNAvail{}
	for _, a := range o.cplRepledgable(ctx, tenant) {
		avail[a.PSN] = a
	}
	now := o.now()
	_, look, _ := o.Market(ctx, tenant, now.Format("2006-01-02"))
	n, err := o.next(ctx, tenant, "cdslrp", actor)
	if err != nil {
		return Repledge{}, err
	}
	b := Repledge{ID: fmt.Sprintf("RP-%d", n), Status: "Pending", Maker: actor, At: now.Format(time.RFC3339)}
	seen := map[string]bool{}
	for i, a := range asks {
		x, ok := avail[a.PSN]
		if !ok || seen[a.PSN] {
			return b, invalid("PSN %s is not a margin pledge with quantity left to re-pledge", a.PSN)
		}
		seen[a.PSN] = true
		if a.Qty <= 0 {
			a.Qty = x.Free - x.Repledged
		}
		if a.Qty > x.Free-x.Repledged {
			return b, invalid("only %d of PSN %s is left to re-pledge", x.Free-x.Repledged, a.PSN)
		}
		px := cplPrice(look, x.ISIN)
		b.Lines = append(b.Lines, RepledgeLine{UCC: x.UCC, ISIN: x.ISIN, Symbol: x.Symbol, PSN: x.PSN, Qty: a.Qty, Value: px.MulInt(a.Qty).Fixed(2), BOReqID: fmt.Sprintf("%s%04d", strings.ReplaceAll(b.ID, "-", ""), i+1), Status: "Pending"})
	}
	if _, err := docs.Put(ctx, o.Docs, tenant, cCPLRepledge, b.ID, b, 0, actor); err != nil {
		return b, err
	}
	o.raiseWork(ctx, tenant, "cdslrepledge", b.ID, "Send re-pledge "+b.ID+" to CDSL", fmt.Sprintf("%d pledges to re-pledge to the clearing corporation. Check and send.", len(b.Lines)), "", RoleChecker, "cdslpledge", now.Format("2006-01-02"))
	o.log(ctx, tenant, actor, "cdsl.repledge.requested", b.ID, map[string]any{"lines": len(b.Lines)})
	return b, nil
}

// cplMRSetup is §5.18's pledgedtls.
type cplMRSetup struct {
	PledgeIdentifier string      `json:"pledgeidentifier"`
	ReqTime          string      `json:"reqtime"`
	ExecutionDate    string      `json:"executiondate"`
	Lines            []cplMRLine `json:"setupdtls"`
}

type cplMRLine struct {
	PledgeeBOReqID  string `json:"pledgeeboreqid"`
	PledgeeBOID     string `json:"pledgeeboid"`
	PledgeSeqNo     string `json:"pledgeseqno"`
	PRFNo           string `json:"prfno"`
	PledgeType      string `json:"pledgetype"`
	PledgeQuantity  string `json:"pledgequantity"`
	PledgeExpDate   string `json:"pledgeexpdate"`
	PledgeValue     string `json:"pledgevalue"`
	AgrmntNo        string `json:"agrmntno"`
	PldgrDPIntRefNo string `json:"pldgrdpintrefno"`
	PledgeRemarks   string `json:"pledgeremarks"`
	ReasonCode      string `json:"reasoncode"`
	CMID            string `json:"cmid"`
}

// cplMRRes is §5.18's reply.
type cplMRRes struct {
	ReqID     string `json:"reqid"`
	ResStatus string `json:"resstatus"`
	ResError  string `json:"reserror"`
	ResErrMsg string `json:"reserrmsg"`
	Lines     []struct {
		BOReqID     string `json:"pledgeeboreqid"`
		PledgeSeqNo string `json:"pledgeseqno"`
		Status      string `json:"boreqstatus"`
		ResError    string `json:"reserror"`
		ResErrMsg   string `json:"reserrmsg"`
	} `json:"mrgnrepldgdtls"`
}

// DecideRepledge approves (and sends) or rejects a re-pledge batch. The
// checker is not the person who made it.
func (o *Ops) DecideRepledge(ctx context.Context, tenant, actor, id string, accept bool, reason string) (Repledge, error) {
	if err := o.require(ctx, tenant, actor, "Approving re-pledges", RoleChecker, RoleRisk); err != nil {
		return Repledge{}, err
	}
	b, _, err := docs.Get[Repledge](ctx, o.Docs, tenant, cCPLRepledge, id)
	if err != nil {
		return b, err
	}
	if b.Status != "Pending" {
		return b, fmt.Errorf("%w: re-pledge %s is %s", ErrState, id, b.Status)
	}
	if actor == b.Maker {
		return b, fmt.Errorf("%w: a different person approves the re-pledge (maker and checker)", ErrForbidden)
	}
	if !accept {
		if strings.TrimSpace(reason) == "" {
			return b, invalid("give a reason for rejecting the re-pledge")
		}
		b, err = docs.Update(ctx, o.Docs, tenant, cCPLRepledge, id, actor, func(x *Repledge) error {
			x.Status, x.Checker, x.Reason = "Rejected", actor, reason
			return nil
		})
		o.closeWork(ctx, tenant, actor, "cdslrepledge", id, "Rejected")
		o.log(ctx, tenant, actor, "cdsl.repledge.rejected", id, nil)
		return b, err
	}
	cc, err := o.cplConnection(ctx, tenant, "MR")
	if err != nil {
		return b, err
	}
	cfg := o.CDSLPledgeSettings(ctx, tenant)
	if !cplReBO16.MatchString(cfg.RepledgeeBOID) || cfg.RepledgeCMID == "" {
		return b, invalid("enter the re-pledge pledgee BO ID and CM ID in the CDSL pledge settings first")
	}
	exp := map[string]string{}
	for _, a := range o.cplPSNs(ctx, tenant, "") {
		exp[a.PSN] = a.Expiry
	}
	now := o.now()
	reqID, err := o.cplReqID(ctx, tenant, "LR", 24)
	if err != nil {
		return b, err
	}
	d := cplMRSetup{PledgeIdentifier: "MR", ReqTime: cplTime(now), ExecutionDate: cplDate(now.Format("2006-01-02"))}
	for _, l := range b.Lines {
		e := firstOf(exp[l.PSN], addDays(now.Format("2006-01-02"), cfg.ExpiryDays))
		d.Lines = append(d.Lines, cplMRLine{PledgeeBOReqID: l.BOReqID, PledgeeBOID: cfg.RepledgeeBOID, PledgeSeqNo: l.PSN, PRFNo: strings.ReplaceAll(b.ID, "-", ""), PledgeType: cfg.RepledgeType,
			PledgeQuantity: cplQty(l.Qty), PledgeExpDate: cplDate(e), PledgeValue: l.Value, PledgeRemarks: "Re-pledge " + b.ID, ReasonCode: cfg.RepledgeReason, CMID: cfg.RepledgeCMID})
	}
	b, err = docs.Update(ctx, o.Docs, tenant, cCPLRepledge, id, actor, func(x *Repledge) error {
		if x.Status != "Pending" {
			return fmt.Errorf("%w: re-pledge %s is %s", ErrState, id, x.Status)
		}
		x.Status, x.Checker, x.ReqID, x.SentAt = "Sent", actor, reqID, now.Format(time.RFC3339)
		return nil
	})
	if err != nil {
		return b, err
	}
	o.closeWork(ctx, tenant, actor, "cdslrepledge", id, "Sent")
	enc, err := cc.seal(d)
	if err != nil {
		return b, err
	}
	body, herr := cc.postForm(ctx, "MR", url.Values{"dpid": {cc.DPID}, "reqid": {reqID}, "version": {cplVersion}, "pledgedtls": {enc}})
	var res cplMRRes
	if herr == nil || len(body) > 0 {
		p, derr := cc.open(body, "pledgeresdtls", "encryptresdtl", "data")
		if derr == nil {
			derr = json.Unmarshal(p, &res)
		}
		if derr != nil && herr == nil {
			herr = derr
		} else if derr == nil {
			herr = nil
		}
	}
	if herr != nil {
		b, _ = docs.Update(ctx, o.Docs, tenant, cCPLRepledge, id, SystemActor, func(x *Repledge) error {
			x.Status, x.Result = "No reply", herr.Error()
			return nil
		})
		o.raiseWork(ctx, tenant, "cdslrepledge", id+"-reply", "Re-pledge "+id+": no answer from CDSL", "Check the re-pledge in CDSL before sending it again: "+herr.Error(), "", RoleRisk, "cdslpledge", now.Format("2006-01-02"))
		o.log(ctx, tenant, actor, "cdsl.repledge.no_reply", id, map[string]any{"error": herr.Error()})
		return b, nil
	}
	by := map[string]int{}
	for i, l := range res.Lines {
		by[strings.TrimSpace(l.BOReqID)] = i
	}
	done, failed := 0, 0
	b, err = docs.Update(ctx, o.Docs, tenant, cCPLRepledge, id, SystemActor, func(x *Repledge) error {
		for i := range x.Lines {
			l := &x.Lines[i]
			j, ok := by[l.BOReqID]
			switch {
			case ok && strings.TrimSpace(res.Lines[j].Status) == "0":
				l.Status = "Done"
				done++
			case ok:
				l.Status, l.ErrCode, l.ErrMsg = "Failed", strings.TrimSpace(res.Lines[j].ResError), strings.TrimSpace(res.Lines[j].ResErrMsg)
				failed++
			default:
				l.Status, l.ErrCode, l.ErrMsg = "Failed", strings.TrimSpace(res.ResError), firstOf(strings.TrimSpace(res.ResErrMsg), "not in CDSL's answer")
				failed++
			}
		}
		switch {
		case failed == 0:
			x.Status = "Done"
		case done == 0:
			x.Status = "Failed"
		default:
			x.Status = "Partly done"
		}
		x.Result = fmt.Sprintf("%d re-pledged, %d not", done, failed)
		return nil
	})
	o.log(ctx, tenant, actor, "cdsl.repledge.sent", id, map[string]any{"done": done, "failed": failed})
	return b, err
}

// RepledgeReconRow compares, per client and security, what the client has
// pledged to the firm, what was re-pledged through CDSL's API, and what
// NSE Clearing's re-pledge status file shows as accepted.
type RepledgeReconRow struct {
	UCC       string `json:"ucc"`
	ISIN      string `json:"isin"`
	Symbol    string `json:"symbol"`
	Pledged   int64  `json:"pledged"`
	Repledged int64  `json:"repledged"`
	Accepted  int64  `json:"accepted"` // NSE Clearing, -1 when no file
	Diff      int64  `json:"diff"`     // re-pledged less accepted
	Note      string `json:"note,omitempty"`
}

// RepledgeRecon reconciles API re-pledges with NSE Clearing's re-pledge
// status file of date (ncl-repledge-status, Clearing data).
func (o *Ops) RepledgeRecon(ctx context.Context, tenant, date string) ([]RepledgeReconRow, bool) {
	rows := map[string]*RepledgeReconRow{}
	get := func(ucc, isin, sym string) *RepledgeReconRow {
		k := ucc + "|" + isin
		if rows[k] == nil {
			rows[k] = &RepledgeReconRow{UCC: ucc, ISIN: isin, Symbol: sym}
		}
		if rows[k].Symbol == "" {
			rows[k].Symbol = sym
		}
		return rows[k]
	}
	for _, b := range o.cplRepledges(ctx, tenant) {
		for _, l := range b.Lines {
			if l.Status == "Done" {
				get(l.UCC, l.ISIN, l.Symbol).Repledged += l.Qty
			}
		}
	}
	for _, r := range o.cplReleases(ctx, tenant) {
		if r.Status == "Done" && (r.Of == "MP" || r.Of == "MF") {
			// A release of a re-pledged PSN takes the re-pledge with it.
			if x, ok := rows[r.UCC+"|"+r.ISIN]; ok {
				x.Repledged = max(x.Repledged-r.Qty, 0)
			}
		}
	}
	loaded := false
	for _, d := range o.ccData(ctx, tenant, "repledge", date, "") {
		loaded = true
		for _, r := range d.Rows {
			ucc, isin := strings.ToUpper(r["ClientCpCode"]), strings.ToUpper(r["Isin"])
			if len(isin) != 12 || ucc == "" || ucc == "CLIENT/CPCODE" {
				continue
			}
			q := decOr0(r["Qty"])
			n := int64(q.Float64())
			get(ucc, isin, r["Symbol"]).Accepted += n
		}
	}
	if len(rows) == 0 {
		return []RepledgeReconRow{}, loaded
	}
	hs, _ := o.Holdings(ctx, tenant, "")
	for _, h := range hs {
		if x, ok := rows[h.UCC+"|"+h.ISIN]; ok {
			x.Pledged = h.Pledged
		}
	}
	out := []RepledgeReconRow{}
	for _, k := range sortedKeys(rows) {
		x := rows[k]
		if !loaded {
			x.Accepted = -1
		} else {
			x.Diff = x.Repledged - x.Accepted
			switch {
			case x.Diff > 0:
				x.Note = "re-pledged at CDSL, not (all) accepted by NSE Clearing"
			case x.Diff < 0:
				x.Note = "NSE Clearing shows more than was re-pledged through the API"
			}
		}
		if x.Repledged > x.Pledged {
			x.Note = strings.TrimPrefix(x.Note+"; re-pledged more than the client has pledged", "; ")
		}
		out = append(out, *x)
	}
	return out, loaded
}

// ---- normal pledge (§5.19) ----

// NormalPledge is a client's pledge to a lender (loan against shares),
// set up by the DP through CDSL's normal pledge API.
type NormalPledge struct {
	ID          string       `json:"id"`
	UCC         string       `json:"ucc"`
	Name        string       `json:"name"`
	Lender      string       `json:"lender"`
	PledgeeBOID string       `json:"pledgee_bo_id"`
	Agreement   string       `json:"agreement,omitempty"`
	Expiry      string       `json:"expiry"`
	Remarks     string       `json:"remarks,omitempty"`
	Lines       []NormalLine `json:"lines"`
	Status      string       `json:"status"` // Pending, Rejected, Sent, Done, Partly done, Failed, No reply
	Maker       string       `json:"maker"`
	Checker     string       `json:"checker,omitempty"`
	At          string       `json:"at"`
	ReqID       string       `json:"req_id,omitempty"`
	SentAt      string       `json:"sent_at,omitempty"`
	Result      string       `json:"result,omitempty"`
	Reason      string       `json:"reason,omitempty"`
}

// NormalLine is one security of a normal pledge.
type NormalLine struct {
	ISIN      string `json:"isin"`
	Symbol    string `json:"symbol"`
	Qty       int64  `json:"qty"`
	Value     string `json:"value"`
	ISINReqID string `json:"isin_req_id,omitempty"`
	PSN       string `json:"psn,omitempty"`
	PSNStatus string `json:"psn_status,omitempty"`
	Status    string `json:"status"` // Pending, Done, Failed
	ErrCode   string `json:"err_code,omitempty"`
	ErrMsg    string `json:"err_msg,omitempty"`
}

func (o *Ops) cplNormals(ctx context.Context, tenant string) []NormalPledge {
	ns, _ := docs.List[NormalPledge](ctx, o.Docs, tenant, cCPLNormal, "")
	sort.SliceStable(ns, func(i, j int) bool { return ns[i].At > ns[j].At })
	return ns
}

// RequestNormalPledge records a normal pledge (DP maker), on the client's
// signed pledge form. A DP checker sends it.
func (o *Ops) RequestNormalPledge(ctx context.Context, tenant, actor string, n NormalPledge) (NormalPledge, error) {
	if err := o.require(ctx, tenant, actor, "Entering pledges to lenders", RoleMaker, RoleChecker); err != nil {
		return n, err
	}
	n.UCC = strings.ToUpper(strings.TrimSpace(n.UCC))
	c, err := o.Client(ctx, tenant, n.UCC)
	if err != nil {
		return n, invalid("client %s not found", n.UCC)
	}
	bo := o.bo(ctx, tenant, c)
	if bo.Status != "Active" || bo.Freeze == "Debit" || bo.Freeze == "Both" || !cplReBO16.MatchString(bo.BOID) {
		return n, invalid("%s has no active CDSL demat account with this DP", n.UCC)
	}
	n.PledgeeBOID = strings.TrimSpace(n.PledgeeBOID)
	n.Lender = strings.TrimSpace(n.Lender)
	if !cplReBO16.MatchString(n.PledgeeBOID) || n.PledgeeBOID == bo.BOID {
		return n, invalid("the lender's (pledgee's) BO ID is 16 digits")
	}
	if n.Lender == "" {
		return n, invalid("name the lender")
	}
	today := o.now().Format("2006-01-02")
	if n.Expiry == "" {
		n.Expiry = addDays(today, 365)
	}
	if _, err := time.Parse("2006-01-02", n.Expiry); err != nil || n.Expiry <= today {
		return n, invalid("the expiry is a date after today")
	}
	if len(n.Lines) == 0 || len(n.Lines) > 50 {
		return n, invalid("give 1 to 50 securities")
	}
	if len(n.Remarks) > 100 {
		n.Remarks = n.Remarks[:100]
	}
	_, look, _ := o.Market(ctx, tenant, today)
	seen := map[string]bool{}
	for i := range n.Lines {
		l := &n.Lines[i]
		l.ISIN = strings.ToUpper(strings.TrimSpace(l.ISIN))
		if len(l.ISIN) != 12 || seen[l.ISIN] || l.Qty <= 0 {
			return n, invalid("each ISIN is 12 characters, given once, with a quantity")
		}
		seen[l.ISIN] = true
		h, free := o.cplFree(ctx, tenant, n.UCC, l.ISIN, "")
		if l.Qty > free {
			return n, invalid("only %d %s are free to pledge", max(free, 0), firstOf(h.Symbol, l.ISIN))
		}
		px := cplPrice(look, l.ISIN)
		if px.Sign() <= 0 {
			return n, invalid("there is no price for %s", l.ISIN)
		}
		l.Symbol, l.Value, l.Status, l.PSN, l.PSNStatus, l.ISINReqID = firstOf(h.Symbol, l.ISIN), px.MulInt(l.Qty).Fixed(2), "Pending", "", "", ""
	}
	seq, err := o.next(ctx, tenant, "cdslnp", actor)
	if err != nil {
		return n, err
	}
	n.ID, n.Name, n.Status, n.Maker, n.At = fmt.Sprintf("NP-%d", seq), c.Name, "Pending", actor, o.now().Format(time.RFC3339)
	n.Checker, n.ReqID, n.SentAt, n.Result, n.Reason = "", "", "", "", ""
	if _, err := docs.Put(ctx, o.Docs, tenant, cCPLNormal, n.ID, n, 0, actor); err != nil {
		return n, err
	}
	o.raiseWork(ctx, tenant, "cdslnpledge", n.ID, "Send pledge "+n.ID+" to CDSL", fmt.Sprintf("%s pledges %d securities to %s. Check the signed pledge form and send.", n.UCC, len(n.Lines), n.Lender), n.UCC, RoleChecker, "cdslpledge", today)
	o.log(ctx, tenant, actor, "cdsl.npledge.requested", n.ID, map[string]any{"ucc": n.UCC, "lines": len(n.Lines)})
	return n, nil
}

// DecideNormalPledge sends (or rejects) a normal pledge. Checker only, not
// the maker.
func (o *Ops) DecideNormalPledge(ctx context.Context, tenant, actor, id string, accept bool, reason string) (NormalPledge, error) {
	if err := o.require(ctx, tenant, actor, "Approving pledges to lenders", RoleChecker); err != nil {
		return NormalPledge{}, err
	}
	n, _, err := docs.Get[NormalPledge](ctx, o.Docs, tenant, cCPLNormal, id)
	if err != nil {
		return n, err
	}
	if n.Status != "Pending" {
		return n, fmt.Errorf("%w: pledge %s is %s", ErrState, id, n.Status)
	}
	if actor == n.Maker {
		return n, fmt.Errorf("%w: a different person approves the pledge (maker and checker)", ErrForbidden)
	}
	if !accept {
		if strings.TrimSpace(reason) == "" {
			return n, invalid("give a reason for rejecting the pledge")
		}
		n, err = docs.Update(ctx, o.Docs, tenant, cCPLNormal, id, actor, func(x *NormalPledge) error {
			x.Status, x.Checker, x.Reason = "Rejected", actor, reason
			return nil
		})
		o.closeWork(ctx, tenant, actor, "cdslnpledge", id, "Rejected")
		o.log(ctx, tenant, actor, "cdsl.npledge.rejected", id, nil)
		return n, err
	}
	cc, err := o.cplConnection(ctx, tenant, "EP")
	if err != nil {
		return n, err
	}
	cfg := o.CDSLPledgeSettings(ctx, tenant)
	c, err := o.Client(ctx, tenant, n.UCC)
	if err != nil {
		return n, err
	}
	bo := o.bo(ctx, tenant, c)
	for _, l := range n.Lines {
		if _, free := o.cplFree(ctx, tenant, n.UCC, l.ISIN, ""); free < 0 {
			return n, fmt.Errorf("%w: %s no longer has the %s free to pledge", ErrState, n.UCC, l.Symbol)
		}
	}
	now := o.now()
	reqID, err := o.cplReqID(ctx, tenant, "LN", 24)
	if err != nil {
		return n, err
	}
	d := cplSetup{PledgeIdentifier: "EP", ReqTime: cplTime(now), PledgorBOID: bo.BOID, PledgeeBOID: n.PledgeeBOID, ExecutionDate: cplDate(now.Format("2006-01-02")),
		ExpiryDate: cplDate(n.Expiry), Remarks: truncate(firstOf(n.Remarks, "Pledge "+n.ID), 100)}
	reqs := make([]string, len(n.Lines))
	for i, l := range n.Lines {
		if reqs[i], err = o.cplReqID(ctx, tenant, "LJ", 20); err != nil {
			return n, err
		}
		d.ISINs = append(d.ISINs, cplSetupISIN{PRFNumber: firstOf(truncate(n.Agreement, 16), strings.ReplaceAll(n.ID, "-", "")), ISINReqID: reqs[i], ISIN: l.ISIN, Quantity: cplQty(l.Qty), Value: l.Value, ReasonCode: cfg.NormalReason})
	}
	n, err = docs.Update(ctx, o.Docs, tenant, cCPLNormal, id, actor, func(x *NormalPledge) error {
		if x.Status != "Pending" {
			return fmt.Errorf("%w: pledge %s is %s", ErrState, id, x.Status)
		}
		x.Status, x.Checker, x.ReqID, x.SentAt = "Sent", actor, reqID, now.Format(time.RFC3339)
		for i := range x.Lines {
			x.Lines[i].ISINReqID = reqs[i]
		}
		return nil
	})
	if err != nil {
		return n, err
	}
	o.closeWork(ctx, tenant, actor, "cdslnpledge", id, "Sent")
	o.log(ctx, tenant, actor, "cdsl.npledge.sent", id, map[string]any{"bo": cdslMaskBO(bo.BOID), "req": reqID})
	res, noReply, herr := o.cplSend(ctx, cc, "EP", reqID, d)
	if noReply {
		n, _ = docs.Update(ctx, o.Docs, tenant, cCPLNormal, id, SystemActor, func(x *NormalPledge) error {
			x.Status, x.Result = "No reply", errText(herr)
			return nil
		})
		o.raiseWork(ctx, tenant, "cdslnpledge", id+"-reply", "Pledge "+id+": no answer from CDSL", "Check the pledge in CDSL, then record the result: "+errText(herr), n.UCC, RoleChecker, "cdslpledge", now.Format("2006-01-02"))
		return n, nil
	}
	allFail, code, msg := res.failed()
	done, failed := 0, 0
	n, err = docs.Update(ctx, o.Docs, tenant, cCPLNormal, id, SystemActor, func(x *NormalPledge) error {
		for i := range x.Lines {
			l := &x.Lines[i]
			r, have, ok := cplLineOK(res, l.ISINReqID)
			if ok && !allFail {
				l.Status, l.PSN, l.PSNStatus = "Done", strings.TrimSpace(r.PSN), strings.TrimSpace(r.PSNStatus)
				done++
				continue
			}
			l.Status, l.ErrCode, l.ErrMsg = "Failed", firstOf(strings.TrimSpace(r.ErrorCode), code), firstOf(strings.TrimSpace(r.ErrorMsg), msg)
			if !have && l.ErrMsg == "" {
				l.ErrMsg = "not in CDSL's answer"
			}
			failed++
		}
		x.Status = map[bool]string{true: "Done", false: map[bool]string{true: "Failed", false: "Partly done"}[done == 0]}[failed == 0]
		x.Result = fmt.Sprintf("%d set up, %d not", done, failed)
		return nil
	})
	o.log(ctx, tenant, SystemActor, "cdsl.npledge.result", id, map[string]any{"done": done, "failed": failed})
	return n, err
}

func errText(err error) string {
	if err == nil {
		return "no answer"
	}
	return err.Error()
}

// ---- unpledge and invocation (§5.20) ----

// PledgeRelease is an unpledge (UN) or an invocation (CF, confiscation) of
// a pledge set up through CDSL's APIs.
type PledgeRelease struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"` // UN or CF
	UCC         string `json:"ucc"`
	Name        string `json:"name"`
	Of          string `json:"of"` // MP, MF or EP: the pledge released
	PSN         string `json:"psn"`
	ISIN        string `json:"isin"`
	Symbol      string `json:"symbol"`
	Qty         int64  `json:"qty"`
	Value       string `json:"value,omitempty"` // invocation value
	Reason      string `json:"reason"`
	Debit       string `json:"debit,omitempty"` // CF: the overdue amount
	MTFID       string `json:"mtf_id,omitempty"`
	Source      string `json:"source"` // Client, Desk, Debit, MTF
	Status      string `json:"status"` // UN: Pending, Rejected, Sent, Done, Failed, No reply; CF: Awaiting compliance, Notice given, Sent, Done, Failed, No reply, Rejected, Cancelled
	RequestedBy string `json:"requested_by"`
	ApprovedBy  string `json:"approved_by,omitempty"`
	At          string `json:"at"`
	NoticeAt    string `json:"notice_at,omitempty"`
	DueAt       string `json:"due_at,omitempty"`
	ReqID       string `json:"req_id,omitempty"`
	ISINReqID   string `json:"isin_req_id,omitempty"`
	SentAt      string `json:"sent_at,omitempty"`
	DoneAt      string `json:"done_at,omitempty"`
	PSNStatus   string `json:"psn_status,omitempty"`
	ErrCode     string `json:"err_code,omitempty"`
	ErrMsg      string `json:"err_msg,omitempty"`
	Note        string `json:"note,omitempty"`
}

func (o *Ops) cplReleases(ctx context.Context, tenant string) []PledgeRelease {
	rs, _ := docs.List[PledgeRelease](ctx, o.Docs, tenant, cCPLRelease, "")
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].At > rs[j].At })
	return rs
}

func (o *Ops) cplPSN(ctx context.Context, tenant, psn string) (PSNAvail, bool) {
	for _, a := range o.cplPSNs(ctx, tenant, "") {
		if a.PSN == psn {
			return a, true
		}
	}
	return PSNAvail{}, false
}

// RequestUnpledge records a client's request to release a pledge (all or
// part of a PSN): from the client portal (own login) or the desk. A
// checker sends it to CDSL.
func (o *Ops) RequestUnpledge(ctx context.Context, tenant, actor, ucc, psn string, qty int64, reason string) (PledgeRelease, error) {
	role, err := o.RoleOf(ctx, tenant, actor)
	if err != nil {
		return PledgeRelease{}, err
	}
	ucc = strings.ToUpper(strings.TrimSpace(ucc))
	src := "Desk"
	if role == RoleClient {
		accts, err := o.PortalAccounts(ctx, tenant, actor)
		if err != nil {
			return PledgeRelease{}, err
		}
		if !contains(accts, ucc) {
			return PledgeRelease{}, ErrNotFound
		}
		src = "Client"
	} else if err := o.require(ctx, tenant, actor, "Entering unpledge requests", RoleMaker, RoleChecker, RoleRisk); err != nil {
		return PledgeRelease{}, err
	}
	a, ok := o.cplPSN(ctx, tenant, strings.TrimSpace(psn))
	if !ok || a.UCC != ucc {
		return PledgeRelease{}, invalid("PSN %s is not a pledge of %s set up through CDSL's APIs", psn, ucc)
	}
	if qty <= 0 {
		qty = a.Free
	}
	if qty > a.Free {
		return PledgeRelease{}, invalid("only %d of PSN %s can be released", a.Free, a.PSN)
	}
	if a.Of == "MP" && qty > a.Free-a.Repledged {
		return PledgeRelease{}, invalid("%d of PSN %s is re-pledged to the clearing corporation; it is released when the re-pledge is", a.Repledged, a.PSN)
	}
	if a.Of == "MF" && a.MTFID != "" {
		if m, _, err := docs.Get[MTFPosition](ctx, o.Docs, tenant, cMTFPos, a.MTFID); err == nil && m.Status == "Open" && qty > a.Free-m.Qty {
			return PledgeRelease{}, invalid("the MTF position %s still needs these shares pledged; repay it first", a.MTFID)
		}
	}
	c, _ := o.Client(ctx, tenant, ucc)
	n, err := o.next(ctx, tenant, "cdslun", actor)
	if err != nil {
		return PledgeRelease{}, err
	}
	now := o.now()
	r := PledgeRelease{ID: fmt.Sprintf("UN-%d", n), Kind: "UN", UCC: ucc, Name: c.Name, Of: a.Of, PSN: a.PSN, ISIN: a.ISIN, Symbol: a.Symbol, Qty: qty,
		Reason: firstOf(strings.TrimSpace(reason), "client's request"), Source: src, Status: "Pending", RequestedBy: actor, At: now.Format(time.RFC3339)}
	if _, err := docs.Put(ctx, o.Docs, tenant, cCPLRelease, r.ID, r, 0, actor); err != nil {
		return r, err
	}
	o.raiseWork(ctx, tenant, "cdslunpledge", r.ID, "Unpledge "+r.ID, fmt.Sprintf("%s asks to release %d %s from PSN %s. Check the client's dues and send.", ucc, qty, a.Symbol, a.PSN), ucc, RoleChecker, "cdslpledge", now.Format("2006-01-02"))
	o.log(ctx, tenant, actor, "cdsl.unpledge.requested", r.ID, map[string]any{"ucc": ucc, "psn": a.PSN, "qty": qty})
	return r, nil
}

// DecideUnpledge sends (or rejects) an unpledge. A checker who did not
// enter it.
func (o *Ops) DecideUnpledge(ctx context.Context, tenant, actor, id string, accept bool, reason string) (PledgeRelease, error) {
	if err := o.require(ctx, tenant, actor, "Approving unpledges", RoleChecker, RoleRisk); err != nil {
		return PledgeRelease{}, err
	}
	r, _, err := docs.Get[PledgeRelease](ctx, o.Docs, tenant, cCPLRelease, id)
	if err != nil {
		return r, err
	}
	if r.Kind != "UN" || r.Status != "Pending" {
		return r, fmt.Errorf("%w: %s is %s", ErrState, id, r.Status)
	}
	if actor == r.RequestedBy {
		return r, fmt.Errorf("%w: a different person approves the unpledge (maker and checker)", ErrForbidden)
	}
	if !accept {
		if strings.TrimSpace(reason) == "" {
			return r, invalid("give a reason for rejecting the unpledge")
		}
		r, err = docs.Update(ctx, o.Docs, tenant, cCPLRelease, id, actor, func(x *PledgeRelease) error {
			x.Status, x.ApprovedBy, x.Note = "Rejected", actor, reason
			return nil
		})
		o.closeWork(ctx, tenant, actor, "cdslunpledge", id, "Rejected")
		o.log(ctx, tenant, actor, "cdsl.unpledge.rejected", id, nil)
		return r, err
	}
	o.closeWork(ctx, tenant, actor, "cdslunpledge", id, "Sent")
	return o.cplSendRelease(ctx, tenant, actor, r)
}

// cplSendRelease sends an approved unpledge or invocation to CDSL and
// records the answer.
func (o *Ops) cplSendRelease(ctx context.Context, tenant, actor string, r PledgeRelease) (PledgeRelease, error) {
	cc, err := o.cplConnection(ctx, tenant, r.Kind)
	if err != nil {
		return r, err
	}
	now := o.now()
	// §5.20: the reqid of an unpledge or confiscation is at most 16.
	reqID, err := o.cplReqID(ctx, tenant, map[string]string{"UN": "LU", "CF": "LC"}[r.Kind], 16)
	if err != nil {
		return r, err
	}
	isinReq, err := o.cplReqID(ctx, tenant, "LK", 20)
	if err != nil {
		return r, err
	}
	want := map[string]string{"UN": "Pending", "CF": "Notice given"}[r.Kind]
	r, err = docs.Update(ctx, o.Docs, tenant, cCPLRelease, r.ID, actor, func(x *PledgeRelease) error {
		if x.Status != want {
			return fmt.Errorf("%w: %s is %s", ErrState, x.ID, x.Status)
		}
		x.Status, x.ReqID, x.ISINReqID, x.SentAt = "Sent", reqID, isinReq, now.Format(time.RFC3339)
		if x.Kind == "UN" {
			x.ApprovedBy = actor
		}
		return nil
	})
	if err != nil {
		return r, err
	}
	// The firm releases its own margin pledges as pledgee; a pledge to a
	// lender is released at the client's (pledgor's) request.
	typ := map[bool]string{true: "pledgor", false: "pledgee"}[r.Of == "EP"]
	line := cplSetupISIN{ISINReqID: isinReq, PSN: r.PSN, PartQuantity: cplQty(r.Qty) + ".000"}
	if r.Kind == "CF" {
		typ, line.InvocationValue = "", r.Value
	}
	d := cplSetup{PledgeIdentifier: r.Kind, ReqTime: cplTime(now), Remarks: truncate(r.Reason, 100), Type: typ, ISINs: []cplSetupISIN{line}}
	o.log(ctx, tenant, actor, "cdsl."+map[string]string{"UN": "unpledge", "CF": "invocation"}[r.Kind]+".sent", r.ID, map[string]any{"psn": r.PSN, "qty": r.Qty, "req": reqID})
	res, noReply, herr := o.cplSend(ctx, cc, r.Kind, reqID, d)
	if noReply {
		r, _ = docs.Update(ctx, o.Docs, tenant, cCPLRelease, r.ID, SystemActor, func(x *PledgeRelease) error {
			x.Status, x.ErrMsg = "No reply", errText(herr)
			return nil
		})
		o.raiseWork(ctx, tenant, "cdslunpledge", r.ID+"-reply", r.ID+": no answer from CDSL", "Check PSN "+r.PSN+" in CDSL, then record the result: "+errText(herr), r.UCC, RoleRisk, "cdslpledge", now.Format("2006-01-02"))
		return r, nil
	}
	allFail, code, msg := res.failed()
	l, _, ok := cplLineOK(res, isinReq)
	if ok && !allFail {
		return o.cplReleaseDone(ctx, tenant, r, strings.TrimSpace(l.PSNStatus), "CDSL")
	}
	r, err = docs.Update(ctx, o.Docs, tenant, cCPLRelease, r.ID, SystemActor, func(x *PledgeRelease) error {
		x.Status, x.ErrCode, x.ErrMsg, x.DoneAt = "Failed", firstOf(strings.TrimSpace(l.ErrorCode), code), firstOf(firstOf(strings.TrimSpace(l.ErrorMsg), msg), "not done by CDSL"), now.Format(time.RFC3339)
		return nil
	})
	o.log(ctx, tenant, SystemActor, "cdsl.release.failed", r.ID, map[string]any{"code": r.ErrCode})
	return r, err
}

// cplReleaseDone records a release CDSL has done: margin pledges come off
// the client's pledged quantity; invoked shares leave the account.
func (o *Ops) cplReleaseDone(ctx context.Context, tenant string, r PledgeRelease, psnStatus, how string) (PledgeRelease, error) {
	now := o.now()
	r, err := docs.Update(ctx, o.Docs, tenant, cCPLRelease, r.ID, SystemActor, func(x *PledgeRelease) error {
		if x.Status == "Done" {
			return fmt.Errorf("%w: already done", ErrState)
		}
		x.Status, x.PSNStatus, x.DoneAt, x.ErrCode, x.ErrMsg = "Done", psnStatus, now.Format(time.RFC3339), "", ""
		return nil
	})
	if err != nil {
		return r, err
	}
	scrips := fmt.Sprintf("%d %s", r.Qty, r.Symbol)
	if r.Of == "MP" || r.Of == "MF" {
		dQty := int64(0)
		if r.Kind == "CF" {
			dQty = -r.Qty
		}
		if _, err := o.adjustHolding(ctx, tenant, SystemActor, r.UCC, r.ISIN, r.Symbol, dQty, -r.Qty); err != nil {
			o.raiseWork(ctx, tenant, "cdslunpledge", r.ID+"-holding", r.ID+" done at CDSL but not in holdings", fmt.Sprintf("CDSL released %s from PSN %s, but Ledgerline's holdings do not allow it (%v). Load the day's CDSL holding file.", scrips, r.PSN, err), r.UCC, RoleRisk, "cdslpledge", "")
		}
	}
	if r.Kind == "CF" {
		if r.MTFID != "" {
			o.raiseWork(ctx, tenant, "mtf", r.MTFID+"-invoked", "Sell invoked shares of "+r.MTFID, fmt.Sprintf("Pledge %s invoked: %s moved to the firm's account. Sell them and close the MTF position with the proceeds.", r.PSN, scrips), r.UCC, RoleRisk, "mtf", now.Format("2006-01-02"))
		} else {
			o.raiseWork(ctx, tenant, "debits", r.ID+"-invoked", "Sell invoked shares of "+r.UCC, fmt.Sprintf("Pledge %s invoked: %s moved to the firm's account. Sell them and credit the proceeds against the debit of ₹%s.", r.PSN, scrips, r.Debit), r.UCC, RoleRisk, "debits", now.Format("2006-01-02"))
		}
		o.notify(ctx, tenant, SystemActor, "cdsl.invocation_done", r.UCC, map[string]string{"ref": r.ID, "scrips": scrips, "psn": r.PSN})
	} else {
		o.notify(ctx, tenant, SystemActor, "cdsl.unpledge_done", r.UCC, map[string]string{"ref": r.ID, "scrips": scrips, "psn": r.PSN})
	}
	o.log(ctx, tenant, SystemActor, "cdsl.release.done", r.ID, map[string]any{"psn": r.PSN, "qty": r.Qty, "via": how})
	return r, nil
}

// ResolveCDSLRequest records the result of a request CDSL did not answer
// (status "No reply"), after a checker has looked it up in CDSL: an
// unpledge or invocation (UN-, CF-), a normal pledge (NP-) or a re-pledge
// (RP-). done false marks it failed. psn is the new PSN of a normal
// pledge with one security.
func (o *Ops) ResolveCDSLRequest(ctx context.Context, tenant, actor, id string, done bool, psn, note string) (any, error) {
	if err := o.require(ctx, tenant, actor, "Recording CDSL results", RoleChecker, RoleRisk); err != nil {
		return nil, err
	}
	if strings.TrimSpace(note) == "" {
		return nil, invalid("say where the result was checked (e.g. CDSL's PSN enquiry)")
	}
	now := o.now().Format(time.RFC3339)
	o.closeWork(ctx, tenant, actor, map[bool]string{true: "cdslnpledge", false: map[bool]string{true: "cdslrepledge", false: "cdslunpledge"}[strings.HasPrefix(id, "RP-")]}[strings.HasPrefix(id, "NP-")], id+"-reply", "Resolved")
	o.log(ctx, tenant, actor, "cdsl.request.resolved", id, map[string]any{"done": done, "note": note})
	switch {
	case strings.HasPrefix(id, "UN-") || strings.HasPrefix(id, "CF-"):
		r, _, err := docs.Get[PledgeRelease](ctx, o.Docs, tenant, cCPLRelease, id)
		if err != nil {
			return nil, err
		}
		if r.Status != "No reply" {
			return r, fmt.Errorf("%w: %s is %s", ErrState, id, r.Status)
		}
		if done {
			return o.cplReleaseDone(ctx, tenant, r, "", "checked by "+actor)
		}
		return docs.Update(ctx, o.Docs, tenant, cCPLRelease, id, actor, func(x *PledgeRelease) error {
			x.Status, x.ErrMsg, x.DoneAt, x.Note = "Failed", "not done at CDSL", now, note
			return nil
		})
	case strings.HasPrefix(id, "NP-"):
		return docs.Update(ctx, o.Docs, tenant, cCPLNormal, id, actor, func(x *NormalPledge) error {
			if x.Status != "No reply" {
				return fmt.Errorf("%w: %s is %s", ErrState, id, x.Status)
			}
			if done && len(x.Lines) == 1 && strings.TrimSpace(psn) == "" {
				return invalid("give the PSN CDSL shows")
			}
			for i := range x.Lines {
				if done {
					x.Lines[i].Status = "Done"
					if len(x.Lines) == 1 {
						x.Lines[i].PSN = strings.TrimSpace(psn)
					}
				} else {
					x.Lines[i].Status = "Failed"
				}
			}
			x.Status, x.Result = map[bool]string{true: "Done", false: "Failed"}[done], note
			return nil
		})
	case strings.HasPrefix(id, "RP-"):
		return docs.Update(ctx, o.Docs, tenant, cCPLRepledge, id, actor, func(x *Repledge) error {
			if x.Status != "No reply" {
				return fmt.Errorf("%w: %s is %s", ErrState, id, x.Status)
			}
			for i := range x.Lines {
				x.Lines[i].Status = map[bool]string{true: "Done", false: "Failed"}[done]
			}
			x.Status, x.Result = map[bool]string{true: "Done", false: "Failed"}[done], note
			return nil
		})
	}
	return nil, ErrNotFound
}

// ---- invocation for unpaid debits and MTF liquidation ----

// InvocationCandidate is a pledge the firm may invoke: the client has an
// overdue debit past the blocking period (Debtors), or an MTF position is
// to be liquidated (Margin trading).
type InvocationCandidate struct {
	UCC    string    `json:"ucc"`
	Name   string    `json:"name"`
	Source string    `json:"source"` // Debit or MTF
	Why    string    `json:"why"`
	Debit  string    `json:"debit"` // ₹ to recover
	MTFID  string    `json:"mtf_id,omitempty"`
	PSN    string    `json:"psn"`
	ISIN   string    `json:"isin"`
	Symbol string    `json:"symbol"`
	Free   int64     `json:"free"`
	Qty    int64     `json:"qty"` // suggested
	Price  money.Dec `json:"price"`
	Value  string    `json:"value"`
	Raised string    `json:"raised,omitempty"` // an invocation already open for this PSN
}

// InvocationCandidates lists pledges that may be invoked on date.
func (o *Ops) InvocationCandidates(ctx context.Context, tenant, date string) []InvocationCandidate {
	out := []InvocationCandidate{}
	psns := o.cplPSNs(ctx, tenant, "")
	if len(psns) == 0 {
		return out
	}
	open := map[string]string{}
	for _, r := range o.cplReleases(ctx, tenant) {
		if r.Kind == "CF" && !cplReleaseClosed(r.Status) && r.Status != "Done" {
			open[r.PSN] = r.ID
		}
	}
	_, look, _ := o.Market(ctx, tenant, date)
	add := func(a PSNAvail, src, why, name, mtf string, need money.Dec) {
		free := a.Free
		if a.Of == "MP" {
			free = a.Free - a.Repledged
		}
		px := cplPrice(look, a.ISIN)
		if free <= 0 || px.Sign() <= 0 {
			return
		}
		q := free
		if need.Sign() > 0 {
			f, _ := need.Quo(px)
			q = int64(f.Float64())
			if px.MulInt(q).Cmp(need) < 0 {
				q++
			}
			q = min(max(q, 1), free)
		}
		out = append(out, InvocationCandidate{UCC: a.UCC, Name: name, Source: src, Why: why, Debit: need.Fixed(2), MTFID: mtf, PSN: a.PSN, ISIN: a.ISIN, Symbol: a.Symbol, Free: free, Qty: q, Price: px, Value: px.MulInt(q).Fixed(2), Raised: open[a.PSN]})
	}
	if v, err := o.Debits(ctx, tenant, date); err == nil {
		for _, row := range v.Rows {
			if row.Debit <= 0 || row.Legacy || row.Days < v.BlockDays {
				continue
			}
			need := row.Debit.Dec()
			for _, a := range psns {
				if a.UCC == row.UCC && a.Of == "MP" && need.Sign() > 0 {
					add(a, "Debit", fmt.Sprintf("Debit of ₹%s overdue %d days", formatINR(row.Debit.Dec()), row.Days), row.Name, "", need)
					if n := len(out); n > 0 && out[n-1].PSN == a.PSN {
						need = need.Sub(money.MustDec(out[n-1].Value))
					}
				}
			}
		}
	}
	if v, err := o.MTF(ctx, tenant, date); err == nil {
		for _, p := range v.Positions {
			if p.State != "Liquidate" || p.PledgeRef == "" {
				continue
			}
			for _, a := range psns {
				if a.PSN == p.PledgeRef && a.Of == "MF" {
					add(a, "MTF", "MTF position "+p.ID+" to be liquidated: "+firstOf(p.Why, "margin not restored"), p.Name, p.ID, p.Funded.Dec())
				}
			}
		}
	}
	return out
}

// RaiseInvocation starts an invocation (risk). Compliance approves it,
// which sends the client notice; it goes to CDSL when the notice runs out.
func (o *Ops) RaiseInvocation(ctx context.Context, tenant, actor, date, psn string, qty int64, reason string) (PledgeRelease, error) {
	if err := o.require(ctx, tenant, actor, "Raising pledge invocations", RoleRisk); err != nil {
		return PledgeRelease{}, err
	}
	var cand *InvocationCandidate
	for _, x := range o.InvocationCandidates(ctx, tenant, date) {
		if x.PSN == psn {
			cand = &x
			break
		}
	}
	if cand == nil {
		return PledgeRelease{}, invalid("PSN %s is not a pledge that may be invoked on %s (an overdue debit past the blocking period, or an MTF position to liquidate)", psn, date)
	}
	if cand.Raised != "" {
		return PledgeRelease{}, fmt.Errorf("%w: %s is already open for PSN %s", ErrState, cand.Raised, psn)
	}
	if qty <= 0 {
		qty = cand.Qty
	}
	if qty > cand.Free {
		return PledgeRelease{}, invalid("only %d of PSN %s can be invoked", cand.Free, psn)
	}
	a, _ := o.cplPSN(ctx, tenant, psn)
	n, err := o.next(ctx, tenant, "cdslcf", actor)
	if err != nil {
		return PledgeRelease{}, err
	}
	r := PledgeRelease{ID: fmt.Sprintf("CF-%d", n), Kind: "CF", UCC: cand.UCC, Name: cand.Name, Of: a.Of, PSN: psn, ISIN: cand.ISIN, Symbol: cand.Symbol, Qty: qty,
		Value: cand.Price.MulInt(qty).Fixed(3), Reason: firstOf(strings.TrimSpace(reason), cand.Why), Debit: cand.Debit, MTFID: cand.MTFID, Source: cand.Source,
		Status: "Awaiting compliance", RequestedBy: actor, At: o.now().Format(time.RFC3339)}
	if _, err := docs.Put(ctx, o.Docs, tenant, cCPLRelease, r.ID, r, 0, actor); err != nil {
		return r, err
	}
	o.raiseWork(ctx, tenant, "cdslinvoke", r.ID, "Approve invocation "+r.ID, fmt.Sprintf("Risk asks to invoke %d %s of %s (PSN %s): %s. Approving sends the client notice.", qty, r.Symbol, r.UCC, psn, r.Reason), r.UCC, RoleCompliance, "cdslpledge", date)
	o.log(ctx, tenant, actor, "cdsl.invocation.raised", r.ID, map[string]any{"ucc": r.UCC, "psn": psn, "qty": qty})
	return r, nil
}

// DecideInvocation is compliance's decision. Approval sends the client
// notice; the invocation goes to CDSL when the notice period is over.
func (o *Ops) DecideInvocation(ctx context.Context, tenant, actor, id string, accept bool, reason string) (PledgeRelease, error) {
	if err := o.require(ctx, tenant, actor, "Approving pledge invocations", RoleCompliance); err != nil {
		return PledgeRelease{}, err
	}
	r, _, err := docs.Get[PledgeRelease](ctx, o.Docs, tenant, cCPLRelease, id)
	if err != nil {
		return r, err
	}
	if r.Kind != "CF" || r.Status != "Awaiting compliance" {
		return r, fmt.Errorf("%w: %s is %s", ErrState, id, r.Status)
	}
	if actor == r.RequestedBy {
		return r, fmt.Errorf("%w: compliance approval is by someone other than the person who raised it", ErrForbidden)
	}
	if !accept && strings.TrimSpace(reason) == "" {
		return r, invalid("give a reason")
	}
	cfg := o.CDSLPledgeSettings(ctx, tenant)
	now := o.now()
	due := now.AddDate(0, 0, cfg.NoticeDays)
	r, err = docs.Update(ctx, o.Docs, tenant, cCPLRelease, id, actor, func(x *PledgeRelease) error {
		x.ApprovedBy = actor
		if !accept {
			x.Status, x.Note = "Rejected", reason
			return nil
		}
		x.Status, x.NoticeAt, x.DueAt = "Notice given", now.Format(time.RFC3339), due.Format(time.RFC3339)
		return nil
	})
	if err != nil {
		return r, err
	}
	o.closeWork(ctx, tenant, actor, "cdslinvoke", id, r.Status)
	if accept {
		o.notify(ctx, tenant, actor, "cdsl.invocation_notice", r.UCC, map[string]string{"ref": r.ID, "scrips": fmt.Sprintf("%d %s", r.Qty, r.Symbol), "psn": r.PSN,
			"debit": formatINR(money.MustDec(firstOf(r.Debit, "0"))), "due": due.In(ist).Format("02 Jan 2006 15:04"), "reason": r.Reason})
	}
	o.log(ctx, tenant, actor, "cdsl.invocation."+map[bool]string{true: "approved", false: "rejected"}[accept], id, nil)
	return r, nil
}

// InvokeNow sends an invocation whose notice is over (risk). The tick does
// the same on its own.
func (o *Ops) InvokeNow(ctx context.Context, tenant, actor, id string) (PledgeRelease, error) {
	if err := o.require(ctx, tenant, actor, "Invoking pledges", RoleRisk); err != nil {
		return PledgeRelease{}, err
	}
	r, _, err := docs.Get[PledgeRelease](ctx, o.Docs, tenant, cCPLRelease, id)
	if err != nil {
		return r, err
	}
	if r.Kind != "CF" || r.Status != "Notice given" {
		return r, fmt.Errorf("%w: %s is %s", ErrState, id, r.Status)
	}
	if due, err := time.Parse(time.RFC3339, r.DueAt); err != nil || o.now().Before(due) {
		return r, fmt.Errorf("%w: the client's notice runs until %s", ErrState, r.DueAt)
	}
	return o.cplInvoke(ctx, tenant, actor, r)
}

// cplInvoke sends a due invocation, unless the debit has been paid since.
func (o *Ops) cplInvoke(ctx context.Context, tenant, actor string, r PledgeRelease) (PledgeRelease, error) {
	if r.Source == "Debit" {
		funds, err := o.clientFunds(ctx, tenant, o.now().Format("2006-01-02"), "")
		if err == nil && funds[r.UCC] >= 0 {
			r, err = docs.Update(ctx, o.Docs, tenant, cCPLRelease, r.ID, SystemActor, func(x *PledgeRelease) error {
				x.Status, x.Note = "Cancelled", "the debit was paid within the notice period"
				return nil
			})
			o.log(ctx, tenant, SystemActor, "cdsl.invocation.cancelled", r.ID, nil)
			return r, err
		}
	}
	if r.Source == "MTF" && r.MTFID != "" {
		if m, _, err := docs.Get[MTFPosition](ctx, o.Docs, tenant, cMTFPos, r.MTFID); err == nil && m.Status != "Open" {
			r, err = docs.Update(ctx, o.Docs, tenant, cCPLRelease, r.ID, SystemActor, func(x *PledgeRelease) error {
				x.Status, x.Note = "Cancelled", "the MTF position was closed within the notice period"
				return nil
			})
			return r, err
		}
	}
	return o.cplSendRelease(ctx, tenant, actor, r)
}

// cplReleaseTick sends invocations whose notice is over and raises work
// for pledges that may be invoked.
func (o *Ops) cplReleaseTick(ctx context.Context, tenant string, now time.Time) {
	for _, r := range o.cplReleases(ctx, tenant) {
		if r.Kind != "CF" || r.Status != "Notice given" {
			continue
		}
		if due, err := time.Parse(time.RFC3339, r.DueAt); err == nil && !now.Before(due) {
			_, _ = o.cplInvoke(ctx, tenant, SystemActor, r)
		}
	}
	// Pledges that may be invoked: looked for once a day, after 10:00.
	date := now.In(ist).Format("2006-01-02")
	if now.In(ist).Hour() < 10 {
		return
	}
	if _, _, err := docs.Get[struct{}](ctx, o.Docs, tenant, cCPLCfg, "scan|"+date); err == nil {
		return
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cCPLCfg, "scan|"+date, struct{}{}, SystemActor)
	for _, c := range o.InvocationCandidates(ctx, tenant, date) {
		if c.Raised == "" {
			o.raiseWork(ctx, tenant, "cdslinvoke", c.UCC+"|"+c.PSN, "Pledge "+c.PSN+" of "+c.UCC+" may be invoked", c.Why+". Raise an invocation on Pledges (CDSL) if the client does not pay.", c.UCC, RoleRisk, "cdslpledge", date)
		}
	}
}
