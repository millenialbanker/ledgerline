package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
)

// ---- CDSL early pay-in (CDSL API Handbook §5.9 insta response setup,
// §5.10 inquiry) ----
//
// A client who sells shares can have them paid in early, from the demat
// account to the clearing member's early pay-in account, on the trade day.
// The sale then needs no margin (it is not a short delivery), and its
// proceeds can be given as credit. Ledgerline sends early pay-ins through
// CDSL's API: a DP maker enters them (from the day's delivery sells), a
// DP checker sends them, and CDSL answers at once ("insta response") with
// an early pay-in transaction ID per ISIN. Requests whose answer did not
// come back are asked about with the inquiry API until CDSL answers.
//
// Accepted early pay-ins are kept by date, client, ISIN, quantity and
// settlement (collection "cdslepi") for the planned credit-against-sale
// screen, SEGMON column 20 and live risk, where they do not count as short
// delivery.

const cEPI = "cdslepi"

// EPIRecord is one ISIN of a client's early pay-in.
type EPIRecord struct {
	ID        string `json:"id"`
	Date      string `json:"date"` // trade date
	UCC       string `json:"ucc"`
	Name      string `json:"name"`
	BOID      string `json:"bo_id"`
	ISIN      string `json:"isin"`
	Symbol    string `json:"symbol"`
	Qty       int64  `json:"qty"`
	Exchange  string `json:"exchange"`
	SettID    string `json:"sett_id"` // CDSL's 13-digit settlement ID
	Flag      string `json:"flag"`    // electxnflag: D (DDPI), Y, N, E, G
	DISNo     string `json:"dis_no,omitempty"`
	Status    string `json:"status"` // Pending, Rejected, Sent, Accepted, Failed, No reply
	Maker     string `json:"maker"`
	Checker   string `json:"checker,omitempty"`
	At        string `json:"at"`
	SeqNo     string `json:"seq_no,omitempty"`     // requestseqno of the call
	BOReqSeq  string `json:"bo_req_seq,omitempty"` // reqseqno of the client's part
	ISINReqID string `json:"isin_req_id,omitempty"`
	SentAt    string `json:"sent_at,omitempty"`
	EPTxnID   string `json:"ep_txn_id,omitempty"`
	TxnStatus string `json:"txn_status,omitempty"` // inquiry "status"
	DoneAt    string `json:"done_at,omitempty"`
	ErrCode   string `json:"err_code,omitempty"`
	ErrMsg    string `json:"err_msg,omitempty"`
	LastAsk   string `json:"last_inquiry,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// EPIInput is one early pay-in to enter.
type EPIInput struct {
	Date     string `json:"date"`
	UCC      string `json:"ucc"`
	ISIN     string `json:"isin"`
	Qty      int64  `json:"qty"`
	Exchange string `json:"exchange"`
	SettID   string `json:"sett_id"`
	Flag     string `json:"flag"`
	DISNo    string `json:"dis_no"`
}

func (o *Ops) epiAll(ctx context.Context, tenant, prefix string) []EPIRecord {
	rs, _ := docs.List[EPIRecord](ctx, o.Docs, tenant, cEPI, prefix)
	return rs
}

// EPIAccepted lists the early pay-ins CDSL accepted for trade date date.
func (o *Ops) EPIAccepted(ctx context.Context, tenant, date string) []EPIRecord {
	out := []EPIRecord{}
	for _, r := range o.epiAll(ctx, tenant, date+"|") {
		if r.Status == "Accepted" {
			out = append(out, r)
		}
	}
	return out
}

// epiCovered is the quantity paid in early per client and symbol on a
// trade date (live risk).
func (o *Ops) epiCovered(ctx context.Context, tenant, date string) map[string]int64 {
	out := map[string]int64{}
	for _, r := range o.EPIAccepted(ctx, tenant, date) {
		out[r.UCC+"|"+r.Symbol] += r.Qty
	}
	return out
}

// EPISettlements finds CDSL settlement IDs for a trade date in CDSL's
// settlement calendar (Clearing data, cdsl-calendar): 13-digit IDs whose
// settlement period covers the date, for an exchange when given.
func (o *Ops) EPISettlements(ctx context.Context, tenant, exchange, date string) []string {
	ds, _ := docs.List[CCData](ctx, o.Docs, tenant, cCCData, "cdslcal|")
	seen := map[string]bool{}
	var out []string
	ex := strings.ToUpper(strings.TrimSpace(exchange))
	for _, d := range ds {
		for _, r := range d.Rows {
			id := strings.TrimSpace(r["SctiesSttlmTxId"])
			if len(id) != 13 || !cplDigits(id) || seen[id] {
				continue
			}
			from, to := anyDate(r["SttlmPrdFr"]), anyDate(r["SttlmPrdTo"])
			if from == "" || date < from || (to != "" && date > to) {
				continue
			}
			if ex != "" && r["Xchg"] != "" && !strings.Contains(strings.ToUpper(r["Xchg"]), ex) {
				continue
			}
			seen[id] = true
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// EnterEPI records early pay-ins (DP maker). A checker sends them.
func (o *Ops) EnterEPI(ctx context.Context, tenant, actor string, in []EPIInput) ([]EPIRecord, error) {
	if err := o.require(ctx, tenant, actor, "Entering early pay-ins", RoleMaker, RoleChecker); err != nil {
		return nil, err
	}
	if len(in) == 0 || len(in) > 200 {
		return nil, invalid("give 1 to 200 early pay-ins")
	}
	cfg := o.CDSLPledgeSettings(ctx, tenant)
	now := o.now()
	var out []EPIRecord
	for _, x := range in {
		x.UCC, x.ISIN, x.Exchange = strings.ToUpper(strings.TrimSpace(x.UCC)), strings.ToUpper(strings.TrimSpace(x.ISIN)), strings.ToUpper(firstOf(strings.TrimSpace(x.Exchange), cfg.EPIExchange()))
		if x.Date == "" {
			x.Date = now.In(ist).Format("2006-01-02")
		}
		if _, err := time.Parse("2006-01-02", x.Date); err != nil {
			return out, invalid("the trade date is YYYY-MM-DD")
		}
		c, err := o.Client(ctx, tenant, x.UCC)
		if err != nil {
			return out, invalid("client %s not found", x.UCC)
		}
		bo := o.bo(ctx, tenant, c)
		if bo.Status != "Active" || bo.Freeze == "Debit" || bo.Freeze == "Both" || !cplReBO16.MatchString(bo.BOID) {
			return out, invalid("%s has no CDSL demat account with this DP open for debits", x.UCC)
		}
		if len(x.ISIN) != 12 || x.Qty <= 0 {
			return out, invalid("each early pay-in has a 12-character ISIN and a quantity")
		}
		h, free := o.cplFree(ctx, tenant, x.UCC, x.ISIN, "")
		for _, r := range o.epiAll(ctx, tenant, x.Date+"|"+x.UCC+"|"+x.ISIN+"|") {
			if r.Status == "Pending" || r.Status == "Sent" || r.Status == "No reply" {
				free -= r.Qty
			}
		}
		if x.Qty > free {
			return out, invalid("%s has only %d %s free (not pledged) for early pay-in", x.UCC, max(free, 0), firstOf(h.Symbol, x.ISIN))
		}
		if x.SettID == "" {
			if ids := o.EPISettlements(ctx, tenant, x.Exchange, x.Date); len(ids) == 1 {
				x.SettID = ids[0]
			}
		}
		if len(x.SettID) != 13 || !cplDigits(x.SettID) {
			return out, invalid("give CDSL's 13-digit settlement ID for %s %s (load CDSL's settlement calendar to fill it in)", x.Exchange, x.Date)
		}
		x.Flag = strings.ToUpper(strings.TrimSpace(x.Flag))
		if x.Flag == "" {
			x.Flag = map[bool]string{true: "D", false: "N"}[bo.DDPI]
		}
		if !strings.Contains("YNDEG", x.Flag) || len(x.Flag) != 1 {
			return out, invalid("the electronic transaction flag is Y, N, D, E or G")
		}
		if x.Flag == "D" && !bo.DDPI {
			return out, invalid("%s has no DDPI: give the DIS (or eDIS) number instead", x.UCC)
		}
		if x.Flag != "D" && strings.TrimSpace(x.DISNo) == "" {
			return out, invalid("give the DIS or eDIS number for %s", x.UCC)
		}
		if len(x.DISNo) > 16 {
			return out, invalid("the DIS number is at most 16 characters")
		}
		n, err := o.next(ctx, tenant, "cdslepi", actor)
		if err != nil {
			return out, err
		}
		r := EPIRecord{ID: fmt.Sprintf("%s|%s|%s|%d", x.Date, x.UCC, x.ISIN, n), Date: x.Date, UCC: x.UCC, Name: c.Name, BOID: bo.BOID, ISIN: x.ISIN, Symbol: firstOf(h.Symbol, x.ISIN),
			Qty: x.Qty, Exchange: x.Exchange, SettID: x.SettID, Flag: x.Flag, DISNo: strings.TrimSpace(x.DISNo), Status: "Pending", Maker: actor, At: now.Format(time.RFC3339)}
		if _, err := docs.Put(ctx, o.Docs, tenant, cEPI, r.ID, r, 0, actor); err != nil {
			return out, err
		}
		o.log(ctx, tenant, actor, "cdsl.epi.entered", r.ID, map[string]any{"qty": r.Qty})
		out = append(out, r)
	}
	if len(out) > 0 {
		o.raiseWork(ctx, tenant, "cdslepi", out[0].Date, "Send early pay-ins to CDSL", fmt.Sprintf("%d early pay-ins entered for %s. Check and send.", len(out), out[0].Date), "", RoleChecker, "cdslepi", out[0].Date)
	}
	return out, nil
}

// EPIExchange is the exchange early pay-ins default to.
func (c CDSLPledgeConfig) EPIExchange() string {
	return map[string]string{"12": "BSE", "11": "NSE"}[c.EPIExID]
}

// cplEPIBO is a client's part of the early pay-in setup (§5.9.6 setupdtls).
type cplEPIBO struct {
	ReqSeqNo   string        `json:"reqseqno"`
	BOID       string        `json:"boid"`
	SettID     string        `json:"settid"`
	CM_ID      string        `json:"cm_id"`
	EPBOID     string        `json:"epboid"`
	POAID      string        `json:"poaid"`
	ElecTxn    string        `json:"electxnflag"`
	DISNo      string        `json:"disno"`
	MakerID    string        `json:"makerid"`
	CheckerID  string        `json:"checkerid"`
	VerifierID string        `json:"verifierid"`
	EntityID   string        `json:"entityid"`
	UCCID      string        `json:"uccid"`
	ExID       string        `json:"exid"`
	SegmentID  string        `json:"segmentid"`
	CMID       string        `json:"cmid"`
	TMCPID     string        `json:"tmcpid"`
	ISINs      []cplEPIISINq `json:"isindtls"`
}

type cplEPIISINq struct {
	ISINReqID string `json:"isinreqid"`
	ISIN      string `json:"isin"`
	Qty       string `json:"qty"`
	IntRef    string `json:"intrrefnumb"`
}

type cplEPISetup struct {
	DPID        string     `json:"dpid"`
	ReqDateTime string     `json:"reqdatetime"`
	Setup       []cplEPIBO `json:"setupdtls"`
}

// cplEPIRes is the setup and inquiry answer (epsetupresdtls).
type cplEPIRes struct {
	ReqSeqNo     string `json:"reqseqno"`
	RequestSeqNo string `json:"requestseqno"`
	DPReqSeqNo   string `json:"dpreqseqno"`
	ResStatus    string `json:"resstatus"`
	RResStatus   string `json:"rresstatus"` // as the §5.10 sample spells it
	ErrCode      string `json:"reqerrorcode"`
	ErrMsg       string `json:"reqerrormsg"`
	BOs          []struct {
		BOReqID   string `json:"boreqid"`
		BOID      string `json:"boid"`
		Status    string `json:"boreqstatus"`
		ResError  string `json:"reserror"`
		ResErrMsg string `json:"reserrmsg"`
		ISINs     []struct {
			ISINReqID string `json:"isinreqid"`
			ISIN      string `json:"isin"`
			Status    string `json:"isinreqstatus"`
			Qty       string `json:"quantity"`
			EPTxnID   string `json:"eptxnid"`
			TxnStatus string `json:"status"`
			ErrorCode string `json:"errorcode"`
			ErrorMsg  string `json:"errormsg"`
		} `json:"isinresdtls"`
	} `json:"resdtls"`
}

// DecideEPI sends (or rejects) entered early pay-ins. A checker who did
// not enter them. CDSL takes up to 10 clients of up to 10 ISINs a call.
func (o *Ops) DecideEPI(ctx context.Context, tenant, actor string, ids []string, accept bool, reason string) ([]EPIRecord, error) {
	if err := o.require(ctx, tenant, actor, "Sending early pay-ins", RoleChecker); err != nil {
		return nil, err
	}
	var rs []EPIRecord
	for _, id := range ids {
		r, _, err := docs.Get[EPIRecord](ctx, o.Docs, tenant, cEPI, id)
		if err != nil {
			return nil, err
		}
		if r.Status != "Pending" {
			return nil, fmt.Errorf("%w: %s is %s", ErrState, id, r.Status)
		}
		if r.Maker == actor {
			return nil, fmt.Errorf("%w: a different person sends the early pay-ins (maker and checker)", ErrForbidden)
		}
		rs = append(rs, r)
	}
	if len(rs) == 0 {
		return nil, invalid("choose the early pay-ins")
	}
	if !accept {
		if strings.TrimSpace(reason) == "" {
			return nil, invalid("give a reason")
		}
		var out []EPIRecord
		for _, r := range rs {
			x, err := docs.Update(ctx, o.Docs, tenant, cEPI, r.ID, actor, func(x *EPIRecord) error {
				x.Status, x.Checker, x.Reason = "Rejected", actor, reason
				return nil
			})
			if err != nil {
				return out, err
			}
			out = append(out, x)
		}
		o.log(ctx, tenant, actor, "cdsl.epi.rejected", rs[0].Date, map[string]any{"n": len(out)})
		return out, nil
	}
	cc, err := o.cplConnection(ctx, tenant, "EPI")
	if err != nil {
		return nil, err
	}
	cfg := o.CDSLPledgeSettings(ctx, tenant)
	if !cplReBO16.MatchString(cfg.EPIBOID) || cfg.EPICMID == "" {
		return nil, invalid("enter the early pay-in BO ID and CM ID in the CDSL settings first")
	}
	// Group by client, settlement and flag: one setupdtls each, 10 ISINs at most.
	var groups [][]EPIRecord
	at := map[string]int{}
	for _, r := range rs {
		k := r.BOID + "|" + r.SettID + "|" + r.Flag + "|" + r.DISNo
		i, ok := at[k]
		if !ok || len(groups[i]) == 10 {
			i = len(groups)
			at[k] = i
			groups = append(groups, nil)
		}
		groups[i] = append(groups[i], r)
	}
	var out []EPIRecord
	for len(groups) > 0 {
		n := min(len(groups), 10)
		got, err := o.epiSend(ctx, tenant, actor, cc, cfg, groups[:n])
		groups = groups[n:]
		out = append(out, got...)
		if err != nil {
			return out, err
		}
	}
	o.closeWork(ctx, tenant, actor, "cdslepi", rs[0].Date, "Sent")
	return out, nil
}

// epiSend makes one setup call for up to 10 clients' parts.
func (o *Ops) epiSend(ctx context.Context, tenant, actor string, cc cplConn, cfg CDSLPledgeConfig, groups [][]EPIRecord) ([]EPIRecord, error) {
	now := o.now()
	seq, err := o.cplReqID(ctx, tenant, "LE", 24)
	if err != nil {
		return nil, err
	}
	d := cplEPISetup{DPID: cc.DPID, ReqDateTime: cplTime(now)}
	var ids []string
	upd := map[string][2]string{} // id -> bo reqseqno, isinreqid
	for _, g := range groups {
		boSeq, err := o.cplReqID(ctx, tenant, "LB", 24)
		if err != nil {
			return nil, err
		}
		r0 := g[0]
		b := cplEPIBO{ReqSeqNo: boSeq, BOID: r0.BOID, SettID: r0.SettID, CM_ID: truncate(cfg.EPICMID, 8), EPBOID: cfg.EPIBOID, ElecTxn: r0.Flag, DISNo: r0.DISNo,
			MakerID: truncate(r0.Maker, 12), CheckerID: truncate(actor, 12), EntityID: cfg.EPIEntity, UCCID: truncate(r0.UCC, 11), ExID: cfg.EPIExID, SegmentID: cfg.EPISegment,
			CMID: cfg.EPICMID, TMCPID: cfg.EPITMCPID}
		if r0.Flag == "D" {
			b.POAID = cfg.DDPIPOAID
		}
		for _, r := range g {
			isinReq, err := o.cplReqID(ctx, tenant, "LF", 20)
			if err != nil {
				return nil, err
			}
			b.ISINs = append(b.ISINs, cplEPIISINq{ISINReqID: isinReq, ISIN: r.ISIN, Qty: cplQty(r.Qty), IntRef: truncate(strings.ReplaceAll(r.ID[strings.LastIndex(r.ID, "|")+1:], "|", ""), 16)})
			ids = append(ids, r.ID)
			upd[r.ID] = [2]string{boSeq, isinReq}
		}
		d.Setup = append(d.Setup, b)
	}
	if d.Setup[0].POAID == "" && d.Setup[0].ElecTxn == "D" {
		return nil, invalid("enter the firm's DDPI POA ID in the CDSL settings first")
	}
	var out []EPIRecord
	for _, id := range ids {
		x, err := docs.Update(ctx, o.Docs, tenant, cEPI, id, actor, func(x *EPIRecord) error {
			if x.Status != "Pending" {
				return fmt.Errorf("%w: %s is %s", ErrState, id, x.Status)
			}
			x.Status, x.Checker, x.SeqNo, x.BOReqSeq, x.ISINReqID, x.SentAt = "Sent", actor, seq, upd[id][0], upd[id][1], now.Format(time.RFC3339)
			return nil
		})
		if err != nil {
			return out, err
		}
		out = append(out, x)
	}
	enc, err := cc.seal(d)
	if err != nil {
		return out, err
	}
	o.log(ctx, tenant, actor, "cdsl.epi.sent", seq, map[string]any{"records": len(ids), "clients": len(d.Setup)})
	body, herr := cc.postJSON(ctx, "EPI", seq, enc)
	if herr == nil || len(body) > 0 {
		if res, err := epiParse(cc, body); err == nil {
			o.epiApply(ctx, tenant, res, "setup")
			herr = nil
		} else if herr == nil {
			herr = err
		}
	}
	if herr != nil {
		o.log(ctx, tenant, actor, "cdsl.epi.no_reply", seq, map[string]any{"error": herr.Error()})
	}
	for i := range out {
		out[i], _, _ = docs.Get[EPIRecord](ctx, o.Docs, tenant, cEPI, out[i].ID)
	}
	return out, nil
}

func epiParse(cc cplConn, body []byte) (cplEPIRes, error) {
	var res cplEPIRes
	p, err := cc.open(body, "encryptresdtl", "epsetupresdtls", "encryptreqdtl", "data")
	if err != nil {
		return res, err
	}
	// The answer may come wrapped as {"epsetupresdtls": {...}}.
	var w map[string]json.RawMessage
	if json.Unmarshal(p, &w) == nil {
		if in, ok := w["epsetupresdtls"]; ok && len(in) > 0 && in[0] == '{' {
			p = in
		}
	}
	err = json.Unmarshal(p, &res)
	return res, err
}

// epiApply records an answer (setup, inquiry or postback) on the records
// it is about. Records already settled stay as they are.
func (o *Ops) epiApply(ctx context.Context, tenant string, res cplEPIRes, how string) (accepted, failed int) {
	now := o.now().Format(time.RFC3339)
	type line struct {
		ok             bool
		txn, st        string
		code, msg      string
		boFail, reqErr bool
	}
	got := map[string]line{}
	for _, b := range res.BOs {
		for _, l := range b.ISINs {
			ok := strings.TrimSpace(l.Status) == "0" && strings.TrimSpace(l.EPTxnID) != "" && strings.TrimSpace(b.Status) != "1"
			got[strings.TrimSpace(l.ISINReqID)] = line{ok: ok, txn: strings.TrimSpace(l.EPTxnID), st: strings.TrimSpace(l.TxnStatus),
				code: firstOf(strings.TrimSpace(l.ErrorCode), strings.TrimSpace(b.ResError)), msg: firstOf(strings.TrimSpace(l.ErrorMsg), strings.TrimSpace(b.ResErrMsg))}
		}
	}
	seq := firstOf(firstOf(res.RequestSeqNo, res.ReqSeqNo), res.DPReqSeqNo)
	reqFailed := strings.TrimSpace(firstOf(res.ResStatus, res.RResStatus)) == "1" && len(res.BOs) == 0
	for _, r := range o.epiAll(ctx, tenant, "") {
		if r.Status != "Sent" && r.Status != "No reply" {
			continue
		}
		l, have := got[r.ISINReqID]
		if !have {
			if !(reqFailed && seq != "" && (r.SeqNo == seq || r.BOReqSeq == seq) && how == "setup") {
				continue
			}
			l = line{code: strings.TrimSpace(res.ErrCode), msg: firstOf(strings.TrimSpace(res.ErrMsg), "refused by CDSL")}
		}
		_, _ = docs.Update(ctx, o.Docs, tenant, cEPI, r.ID, SystemActor, func(x *EPIRecord) error {
			if l.ok {
				x.Status, x.EPTxnID, x.TxnStatus, x.DoneAt, x.ErrCode, x.ErrMsg = "Accepted", l.txn, l.st, now, "", ""
			} else {
				x.Status, x.ErrCode, x.ErrMsg, x.DoneAt = "Failed", l.code, firstOf(l.msg, "not accepted by CDSL"), now
			}
			return nil
		})
		if l.ok {
			accepted++
		} else {
			failed++
		}
		o.log(ctx, tenant, SystemActor, "cdsl.epi."+map[bool]string{true: "accepted", false: "failed"}[l.ok], r.ID, map[string]any{"via": how, "code": l.code})
	}
	return accepted, failed
}

// InquireEPI asks CDSL about early pay-ins sent under one client part's
// request sequence number (§5.10: dpreqseqno is the reqseqno given at
// setup).
func (o *Ops) InquireEPI(ctx context.Context, tenant, actor, boReqSeq string) (int, error) {
	if actor != SystemActor {
		if err := o.require(ctx, tenant, actor, "Asking CDSL about early pay-ins", RoleMaker, RoleChecker, RoleRisk); err != nil {
			return 0, err
		}
	}
	cc, err := o.cplConnection(ctx, tenant, "EPIInquiry")
	if err != nil {
		return 0, err
	}
	now := o.now()
	var mine []EPIRecord
	for _, r := range o.epiAll(ctx, tenant, "") {
		if r.BOReqSeq == boReqSeq && (r.Status == "Sent" || r.Status == "No reply") {
			mine = append(mine, r)
		}
	}
	if len(mine) == 0 {
		return 0, ErrNotFound
	}
	for _, r := range mine {
		_, _ = docs.Update(ctx, o.Docs, tenant, cEPI, r.ID, SystemActor, func(x *EPIRecord) error { x.LastAsk = now.Format(time.RFC3339); return nil })
	}
	seq, err := o.cplReqID(ctx, tenant, "LQ", 24)
	if err != nil {
		return 0, err
	}
	enc, err := cc.seal(struct {
		DPReqSeqNo  string `json:"dpreqseqno"`
		ReqDateTime string `json:"reqdatetime"`
	}{boReqSeq, cplTime(now)})
	if err != nil {
		return 0, err
	}
	body, herr := cc.postJSON(ctx, "EPIInquiry", seq, enc)
	if herr != nil && len(body) == 0 {
		return 0, fmt.Errorf("%w: %v", ErrState, herr)
	}
	res, err := epiParse(cc, body)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrState, err)
	}
	if len(res.BOs) == 0 {
		o.log(ctx, tenant, actor, "cdsl.epi.inquiry_error", boReqSeq, map[string]any{"code": res.ErrCode})
		return 0, nil
	}
	a, f := o.epiApply(ctx, tenant, res, "inquiry")
	return a + f, nil
}

// CDSLEPIHook takes an early pay-in answer CDSL posts to Ledgerline. It is
// trusted only if it decrypts with the firm's API key and is about early
// pay-ins Ledgerline sent and has no answer for yet.
func (o *Ops) CDSLEPIHook(ctx context.Context, tenant string, body []byte) (int, error) {
	cc, err := o.cplConnection(ctx, tenant, "EPI")
	if err != nil {
		return 0, err
	}
	res, err := epiParse(cc, body)
	if err != nil {
		o.log(ctx, tenant, SystemActor, "cdsl.epi.hook_rejected", "", map[string]any{"why": "did not decrypt"})
		return 0, ErrInvalid
	}
	known := false
	open := map[string]bool{}
	for _, r := range o.epiAll(ctx, tenant, "") {
		if r.Status == "Sent" || r.Status == "No reply" {
			open[r.ISINReqID] = true
		}
	}
	for _, b := range res.BOs {
		for _, l := range b.ISINs {
			if open[strings.TrimSpace(l.ISINReqID)] {
				known = true
			}
		}
	}
	if !known {
		o.log(ctx, tenant, SystemActor, "cdsl.epi.hook_rejected", "", map[string]any{"why": "not an open request"})
		return 0, ErrNotFound
	}
	a, f := o.epiApply(ctx, tenant, res, "postback")
	return a + f, nil
}

// epiTick asks about early pay-ins without an answer: after 2 minutes,
// then every 5; after a day they are left for a person to check.
func (o *Ops) epiTick(ctx context.Context, tenant string, now time.Time) {
	asked := map[string]bool{}
	for _, r := range o.epiAll(ctx, tenant, "") {
		if (r.Status != "Sent" && r.Status != "No reply") || r.BOReqSeq == "" || asked[r.BOReqSeq] {
			continue
		}
		at, err := time.Parse(time.RFC3339, r.SentAt)
		if err != nil || now.Sub(at) < 2*time.Minute {
			continue
		}
		if now.Sub(at) > 24*time.Hour {
			if r.Status == "Sent" {
				_, _ = docs.Update(ctx, o.Docs, tenant, cEPI, r.ID, SystemActor, func(x *EPIRecord) error { x.Status = "No reply"; return nil })
				o.raiseWork(ctx, tenant, "cdslepi", r.ID+"-reply", "Early pay-in "+r.UCC+" "+r.Symbol+": no answer from CDSL", "CDSL has not answered for a day. Check the early pay-in in CDSL.", r.UCC, RoleChecker, "cdslepi", r.Date)
			}
			continue
		}
		if li, err := time.Parse(time.RFC3339, r.LastAsk); err == nil && now.Sub(li) < 5*time.Minute {
			continue
		}
		asked[r.BOReqSeq] = true
		_, _ = o.InquireEPI(ctx, tenant, SystemActor, r.BOReqSeq)
	}
}

// EPIView is the Early pay-in screen.
type EPIView struct {
	Date        string      `json:"date"`
	Connected   bool        `json:"connected"`
	Problem     string      `json:"problem,omitempty"`
	Env         string      `json:"env,omitempty"`
	Settings    any         `json:"settings"`
	Records     []EPIRecord `json:"records"`
	Settlements []string    `json:"settlements"`
	Sells       []EPISell   `json:"sells"`
}

// EPISell is a client's delivery sell of the day not yet paid in early.
type EPISell struct {
	UCC      string `json:"ucc"`
	Name     string `json:"name"`
	Exchange string `json:"exchange"`
	ISIN     string `json:"isin"`
	Symbol   string `json:"symbol"`
	Sold     int64  `json:"sold"`
	Entered  int64  `json:"entered"`
	Free     int64  `json:"free"`
	DDPI     bool   `json:"ddpi"`
}

// EarlyPayins builds the screen for a trade date.
func (o *Ops) EarlyPayins(ctx context.Context, tenant, date string) (EPIView, error) {
	v := EPIView{Date: date, Settings: o.CDSLPledgeSettings(ctx, tenant), Records: []EPIRecord{}, Sells: []EPISell{}}
	if cc, err := o.cplConnection(ctx, tenant, "EPI"); err != nil {
		v.Problem = err.Error()
	} else {
		v.Connected, v.Env = true, cc.Env
	}
	entered := map[string]int64{}
	for _, r := range o.epiAll(ctx, tenant, date+"|") {
		r.BOID = cdslMaskBO(r.BOID)
		v.Records = append(v.Records, r)
		if r.Status != "Rejected" && r.Status != "Failed" {
			entered[r.UCC+"|"+r.ISIN] += r.Qty
		}
	}
	sort.SliceStable(v.Records, func(i, j int) bool { return v.Records[i].At > v.Records[j].At })
	v.Settlements = o.EPISettlements(ctx, tenant, "", date)
	if v.Settlements == nil {
		v.Settlements = []string{}
	}
	// The day's delivery sells, net of buys, per client and scrip.
	trades, err := o.App.ExchangeTrades(ctx, tenant, date)
	if err != nil {
		return v, nil
	}
	_, look, _ := o.Market(ctx, tenant, date)
	type k struct{ ucc, ex, sym string }
	net := map[k]int64{}
	for _, t := range trades {
		if t.Segment != "CM" {
			continue
		}
		key := k{t.UCC, t.Exchange, strings.Split(t.Instrument(), "|")[0]}
		if t.Side == "S" && t.Product == "CNC" {
			net[key] += t.Qty
		} else if t.Side == "B" {
			net[key] -= t.Qty
		}
	}
	cs, _ := o.ClientMap(ctx, tenant)
	keys := make([]k, 0, len(net))
	for x, q := range net {
		if q > 0 {
			keys = append(keys, x)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].ucc != keys[j].ucc {
			return keys[i].ucc < keys[j].ucc
		}
		return keys[i].sym < keys[j].sym
	})
	for _, x := range keys {
		i, ok := look.Get(x.ex, "CM", x.sym)
		if !ok || i.ISIN == "" {
			continue
		}
		c := cs[x.ucc]
		_, free := o.cplFree(ctx, tenant, x.ucc, i.ISIN, "")
		v.Sells = append(v.Sells, EPISell{UCC: x.ucc, Name: c.Name, Exchange: x.ex, ISIN: i.ISIN, Symbol: x.sym, Sold: net[x], Entered: entered[x.ucc+"|"+i.ISIN], Free: max(free, 0), DDPI: o.bo(ctx, tenant, c).DDPI})
	}
	return v, nil
}

