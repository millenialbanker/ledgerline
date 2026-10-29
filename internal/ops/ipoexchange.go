package ops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/xfile"
)

// ---- sending bids to the exchange and reading what comes back ----
//
// Bids go to the exchange as a bulk upload file per issue and exchange
// (NSE eIPO, BSE iBBS; layouts nse-eipo-bid-upload and bse-ibbs-bid-upload,
// provisional until the exchanges' own are in hand) or through NSE's eIPO
// API (ipoeipo.go). The platforms answer with success and error files,
// bid inquiry downloads, the eIPO callback, or the API's replies; each is
// an IPOStatusUpdate applied to the bid it names. Staff can also record
// what the platform's screen shows.

// IPOStatusUpdate is news about a bid from the exchange.
type IPOStatusUpdate struct {
	Ref       string `json:"ref,omitempty"` // Ledgerline's bid number (the member reference)
	AppNo     string `json:"app_no,omitempty"`
	Symbol    string `json:"symbol,omitempty"`
	PAN       string `json:"pan,omitempty"`
	Exchange  string `json:"exchange,omitempty"`
	Activity  string `json:"activity,omitempty"` // N, M or C: what this answers
	Status    string `json:"status,omitempty"`   // Success, Error, Accepted, Rejected, Cancelled
	Code      string `json:"code,omitempty"`
	Reason    string `json:"reason,omitempty"`
	PayStatus string `json:"pay_status,omitempty"` // sponsor bank code (10, 11, 12, 13, 21, 22, 31, 100, 110) or its words
	UMN       string `json:"umn,omitempty"`
	DPStatus  string `json:"dp_status,omitempty"` // Y verified, N failed, P pending
	DPReason  string `json:"dp_reason,omitempty"`
	At        string `json:"at,omitempty"`
	Source    string `json:"source,omitempty"`
}

// payText explains the sponsor bank's pay status codes (NSE IPO Sponsor
// Bank API Protocol v0.14.0, payStatus).
var payText = map[string]string{
	"10": "mandate request sent to the client's UPI app", "11": "rejected by the sponsor bank: UPI ID not valid", "12": "rejected by the sponsor bank",
	"13": "the client's bank does not support UPI 2.0 mandates", "21": "rejected by the client's bank", "22": "rejected by the client's bank (technical)",
	"31": "declined by the client in the UPI app", "100": "funds blocked in the client's bank account", "110": "blocked funds released",
}

// payCode reads a pay status given as a code or in words.
func payCode(s string) string {
	t := strings.ToUpper(strings.TrimSpace(s))
	if _, ok := payText[t]; ok {
		return t
	}
	switch {
	case t == "":
		return ""
	case strings.Contains(t, "RELEASE") || strings.Contains(t, "UNBLOCK") || strings.Contains(t, "REVOK"):
		return "110"
	case strings.Contains(t, "DECLIN") || strings.Contains(t, "BY CLIENT") || strings.Contains(t, "BY INVESTOR") || strings.Contains(t, "BY CUSTOMER"):
		return "31"
	case strings.Contains(t, "INVALID") && strings.Contains(t, "UPI"):
		return "11"
	case strings.Contains(t, "FAIL") || strings.Contains(t, "EXPIR") || strings.Contains(t, "LAPS") || strings.Contains(t, "REJECT") || strings.Contains(t, "TIMEOUT"):
		return "12"
	case strings.Contains(t, "ACCEPT") || strings.Contains(t, "BLOCKED") || strings.Contains(t, "SUCCESS") || t == "PAID":
		return "100"
	case strings.Contains(t, "PEND") || strings.Contains(t, "SENT") || strings.Contains(t, "INITIATED") || strings.Contains(t, "REQUEST"):
		return "10"
	}
	return ""
}

func exchOutcome(s string) (ok, bad, cancelled bool) {
	t := strings.ToUpper(strings.TrimSpace(s))
	switch t {
	case "SUCCESS", "S", "ACCEPTED", "A", "OK", "SUBMITTED", "VALID", "PLACED", "MODIFIED", "CONFIRMED", "0":
		return true, false, false
	case "CANCELLED", "CANCELED", "DELETED", "WITHDRAWN":
		return true, false, true
	case "ERROR", "E", "REJECTED", "R", "FAILED", "FAILURE", "F", "INVALID":
		return false, true, false
	}
	return false, false, false
}

func activityOf(s string) string {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "N", "NEW", "ADD", "A":
		return "New"
	case "M", "MODIFY", "MOD", "U", "UPDATE":
		return "Modify"
	case "C", "D", "CANCEL", "DELETE":
		return "Cancel"
	}
	return ""
}

func dateOf(at string, now time.Time) string {
	if t, ok := parseWhen(at); ok {
		return t.Format("2006-01-02")
	}
	for _, l := range []string{"02-01-2006 15:04:05", "02-01-2006", "02/01/2006 15:04:05", "02/01/2006", "2006-01-02"} {
		if t, err := time.ParseInLocation(l, strings.TrimSpace(at), ist); err == nil {
			return t.Format("2006-01-02")
		}
	}
	return now.Format("2006-01-02")
}

// applyUpdate moves a bid on with the exchange's news; it returns what
// changed, empty when nothing did.
func applyUpdate(a *IPOApp, u IPOStatusUpdate, now time.Time, by string) string {
	var did []string
	if u.AppNo != "" && a.AppNo != u.AppNo {
		a.AppNo = strings.TrimSpace(u.AppNo)
		did = append(did, "application number "+a.AppNo)
	}
	act := activityOf(u.Activity)
	if act == "" {
		act = firstOf(a.Sent, a.Pending)
	}
	ok, bad, cancelled := exchOutcome(u.Status)
	if cancelled {
		act = "Cancel"
	}
	clear := func() {
		if a.Sent == act {
			a.Sent = ""
		}
		if a.Pending == act {
			a.Pending = ""
		}
	}
	switch {
	case ok && act == "Cancel":
		clear()
		if a.Status != BidCancelled {
			a.Status, a.Allotment, a.CancelOn = BidCancelled, "Cancelled", dateOf(u.At, now)
			did = append(did, "cancelled on the platform")
		}
		a.ExchStatus, a.ExchReason = "Cancelled", ""
	case ok:
		wasSent, before := a.Sent != "" || a.Pending == act && act != "", a.Status+a.ExchStatus
		clear()
		a.ExchStatus, a.ExchReason = "Accepted", ""
		fresh := preAllotment[a.Status] && a.Status != BidMandatePending && a.Status != BidAccepted && a.Status != BidDeclined && a.Status != BidFailed
		if fresh || act == "Modify" && wasSent {
			switch a.Mode {
			case ModeUPI:
				a.Status, a.Mandate = BidMandatePending, "Pending"
			default:
				a.Status, a.Mandate = BidAccepted, "Accepted"
			}
		}
		if wasSent || before != a.Status+a.ExchStatus {
			did = append(did, strings.ToLower(firstOf(act, "bid"))+" accepted by the exchange")
		}
	case bad:
		inFlight := a.Sent == act || a.Pending == act
		clear()
		why := strings.TrimSpace(strings.Trim(u.Code+" "+u.Reason, " "))
		news := a.ExchStatus != "Rejected" || a.ExchReason != why
		a.ExchStatus, a.ExchReason = "Rejected", why
		switch {
		case (act == "New" || act == "" && a.AppNo == "") && a.Status != BidRejected && a.Status != BidCancelled:
			a.Status, a.Allotment, a.Mandate = BidRejected, "Rejected by the exchange: "+firstOf(why, "no reason given"), "Not sent"
			did = append(did, "rejected by the exchange: "+firstOf(why, "no reason given"))
		case act != "New" && (inFlight || news):
			did = append(did, strings.ToLower(firstOf(act, "change"))+" rejected by the exchange: "+firstOf(why, "no reason given")+"; the earlier bid stands")
		}
	}
	if code := payCode(u.PayStatus); code != "" && a.PayStatus != code {
		a.PayStatus = code
		when := firstOf(u.At, now.Format(time.RFC3339))
		live := preAllotment[a.Status]
		switch code {
		case "10":
			if live && a.Status != BidAccepted {
				a.Status, a.Mandate = BidMandatePending, "Pending"
			}
		case "100":
			if live {
				a.Status = BidAccepted
			}
			a.Mandate, a.MandateAt, a.MandateWhy = "Accepted", when, ""
		case "31":
			if live {
				a.Status = BidDeclined
			}
			a.Mandate, a.MandateAt, a.MandateWhy = "Declined", when, payText[code]
		case "11", "12", "13", "21", "22":
			if live {
				a.Status = BidFailed
			}
			a.Mandate, a.MandateAt, a.MandateWhy = "Failed", when, firstOf(strings.TrimSpace(u.Reason), payText[code])
		case "110":
			a.UnblockedOn = dateOf(u.At, now)
			if a.Status == BidNotAllotted || a.Status == BidAllotted {
				a.Status = BidUnblocked
			}
		}
		did = append(did, "UPI: "+payText[code])
	}
	if u.UMN != "" && a.UMN != u.UMN {
		a.UMN = strings.TrimSpace(u.UMN)
	}
	switch strings.ToUpper(strings.TrimSpace(u.DPStatus)) {
	case "Y", "S", "VERIFIED", "SUCCESS":
		if a.DPCheck != "Verified" {
			a.DPCheck = "Verified"
			did = append(did, "PAN and demat verified by the depository")
		}
	case "N", "F", "FAILED", "MISMATCH":
		if a.DPCheck != "Mismatch" {
			a.DPCheck = "Mismatch"
			did = append(did, "depository check failed: "+firstOf(strings.TrimSpace(u.DPReason), "DP ID, client ID and PAN do not match")+"; correct and modify the bid before the close")
		}
	case "P":
		if a.DPCheck == "" {
			a.DPCheck = "Pending"
		}
	}
	if len(did) == 0 {
		return ""
	}
	s := strings.Join(did, "; ")
	a.event(now, by, "%s%s", s, map[bool]string{true: " (" + u.Source + ")", false: ""}[u.Source != ""])
	return s
}

// ApplyIPOUpdates applies the exchange's news to the bids it names. It
// returns how many bids changed and how many updates named no bid placed
// through Ledgerline.
func (o *Ops) ApplyIPOUpdates(ctx context.Context, tenant, actor string, ups []IPOStatusUpdate) (int, int, error) {
	all, err := docs.List[IPOApp](ctx, o.Docs, tenant, cIPO, "")
	if err != nil {
		return 0, 0, err
	}
	is, _ := docs.List[IPOIssue](ctx, o.Docs, tenant, cIPOIssue, "")
	sym := map[string]string{}
	for _, i := range is {
		sym[i.ID] = i.Symbol
	}
	find := func(u IPOStatusUpdate) string {
		ref, app, s := strings.TrimSpace(u.Ref), strings.TrimSpace(u.AppNo), strings.ToUpper(strings.TrimSpace(u.Symbol))
		for _, a := range all {
			if a.IssueID != "" && ref != "" && a.ID == ref {
				return a.ID
			}
		}
		for _, a := range all {
			if a.IssueID != "" && app != "" && a.AppNo == app && (s == "" || sym[a.IssueID] == s) {
				return a.ID
			}
		}
		if p := strings.ToUpper(strings.TrimSpace(u.PAN)); p != "" && s != "" {
			id, n := "", 0
			for _, a := range all {
				a.normalise()
				if a.IssueID != "" && a.live() && sym[a.IssueID] == s && strings.EqualFold(a.PAN, p) {
					id, n = a.ID, n+1
				}
			}
			if n == 1 {
				return id
			}
		}
		return ""
	}
	now := o.now()
	changed, unknown := map[string]bool{}, 0
	seen := map[string]bool{}
	for _, u := range ups {
		id := find(u)
		if id == "" {
			unknown++
			continue
		}
		k := id + "|" + u.Activity + "|" + u.Status + "|" + u.PayStatus + "|" + u.DPStatus + "|" + u.AppNo
		if seen[k] {
			continue // the same news on each bid option's line
		}
		seen[k] = true
		var what string
		_, err := docs.Update(ctx, o.Docs, tenant, cIPO, id, actor, func(x *IPOApp) error {
			x.normalise()
			what = applyUpdate(x, u, now, actor)
			if what == "" {
				return errAlreadyDone
			}
			return nil
		})
		if err == errAlreadyDone {
			continue
		}
		if err != nil {
			return len(changed), unknown, err
		}
		changed[id] = true
		o.log(ctx, tenant, actor, "ipo.bid_status", id, map[string]any{"what": what, "source": u.Source})
	}
	return len(changed), unknown, nil
}

// RecordIPOStatus records what the exchange platform shows for a bid
// (status, application or order number, mandate) when no file or API
// carries it; for an offer for sale, that the dealer placed the bid.
func (o *Ops) RecordIPOStatus(ctx context.Context, tenant, actor, id string, u IPOStatusUpdate) (IPOApp, error) {
	if err := o.require(ctx, tenant, actor, "Recording IPO bid status", RoleMaker, RoleChecker); err != nil {
		return IPOApp{}, err
	}
	a, err := o.ipoBid(ctx, tenant, id)
	if err != nil {
		return a, err
	}
	if a.IssueID == "" {
		return a, fmt.Errorf("%w: %s was placed outside Ledgerline", ErrState, id)
	}
	u.Ref, u.Source = id, firstOf(u.Source, "recorded by "+actor)
	if u.Status == "" && u.PayStatus == "" && u.DPStatus == "" && u.AppNo == "" {
		return a, invalid("give what the platform shows: status, mandate status, depository check or application number")
	}
	if n, _, err := o.ApplyIPOUpdates(ctx, tenant, actor, []IPOStatusUpdate{u}); err != nil {
		return a, err
	} else if n == 0 {
		return a, fmt.Errorf("%w: nothing changed on %s", ErrState, id)
	}
	return o.ipoBid(ctx, tenant, id)
}

// ---- bid files ----

// IPOBatch is a bid file (or API call) sent to an exchange.
type IPOBatch struct {
	ID       string            `json:"id"`
	Issue    string            `json:"issue"`
	Exchange string            `json:"exchange"`
	Format   string            `json:"format,omitempty"`
	Name     string            `json:"name,omitempty"`
	File     string            `json:"file,omitempty"` // raw file
	Via      string            `json:"via"`            // File or API
	Created  string            `json:"created"`
	By       string            `json:"by"`
	Bids     map[string]string `json:"bids"` // bid → New, Modify, Cancel
	Lines    int               `json:"lines"`
	Note     string            `json:"note,omitempty"`
}

var bidFormat = map[string]string{"NSE": "nse-eipo-bid-upload", "BSE": "bse-ibbs-bid-upload"}

func actCode(act string) string {
	return map[string]string{"New": "N", "Modify": "M", "Cancel": "C"}[act]
}

// waitingBids are the issue's bids with an action to send to an exchange.
func (o *Ops) waitingBids(ctx context.Context, tenant string, iss IPOIssue, exchange string) ([]IPOApp, error) {
	bids, err := o.issueBids(ctx, tenant, iss.ID)
	if err != nil {
		return nil, err
	}
	var out []IPOApp
	for _, b := range bids {
		if b.Pending != "" && b.Exchange == exchange && b.Status != BidCancelled && b.Status != BidRejected {
			out = append(out, b)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// sendable checks bids may go to the exchange now.
func (o *Ops) sendable(ctx context.Context, tenant string, iss IPOIssue, exchange string) (Settings, error) {
	set, _ := o.Settings(ctx, tenant)
	switch {
	case iss.Type == IssueOFS:
		return set, invalid("offer-for-sale bids are placed in the OFS window of the trading system; record each as placed")
	case !contains(iss.Exchanges, exchange):
		return set, invalid("%s bids on %s", iss.Name, strings.Join(iss.Exchanges, " and "))
	case !iss.liveAt(set, o.now()):
		return set, fmt.Errorf("%w: bids go to the exchange during bidding hours: %s", ErrState, iss.windowText())
	case strings.TrimSpace(set.MemberCodes[exchange]) == "":
		return set, invalid("set the firm's %s member code in Settings first", exchange)
	}
	return set, nil
}

// IPOBidFile writes the bid upload file of an issue's waiting bids for an
// exchange and marks them sent.
func (o *Ops) IPOBidFile(ctx context.Context, tenant, actor, issueID, exchange string) (IPOBatch, error) {
	if err := o.require(ctx, tenant, actor, "Sending IPO bids to the exchange", RoleMaker, RoleChecker); err != nil {
		return IPOBatch{}, err
	}
	exchange = strings.ToUpper(strings.TrimSpace(exchange))
	iss, err := o.IPOIssueByID(ctx, tenant, issueID)
	if err != nil {
		return IPOBatch{}, err
	}
	set, err := o.sendable(ctx, tenant, iss, exchange)
	if err != nil {
		return IPOBatch{}, err
	}
	bids, err := o.waitingBids(ctx, tenant, iss, exchange)
	if err != nil {
		return IPOBatch{}, err
	}
	if len(bids) == 0 {
		return IPOBatch{}, invalid("no bids are waiting to go to %s for %s", exchange, iss.Name)
	}
	lib, err := XFiles()
	if err != nil {
		return IPOBatch{}, err
	}
	spec, ok := lib.Get(bidFormat[exchange])
	if !ok {
		return IPOBatch{}, invalid("no bid file layout for %s", exchange)
	}
	cm, _ := o.ClientMap(ctx, tenant)
	var rows []xfile.Row
	acts := map[string]string{}
	for _, b := range bids {
		cat, _ := iss.category(b.Category)
		acts[b.ID] = b.Pending
		c := cm[b.UCC]
		for i, op := range b.Options {
			eff := op.Price.Sub(cat.Discount)
			v := map[string]string{"activityType": actCode(b.Pending), "symbol": iss.Symbol, "category": cat.exch(exchange), "subCategory": cat.SubCode, "applicationNumber": b.AppNo,
				"referenceNumber": b.ID, "clientName": firstOf(c.Name, b.Name), "pan": b.PAN, "depository": b.Depository, "dpId": b.DPID, "clientBenId": b.BenID,
				"upiFlag": map[bool]string{true: "Y", false: "N"}[b.Mode == ModeUPI], "upi": b.UPI, "bidId": fmt.Sprint(i + 1), "quantity": fmt.Sprint(op.Lots * iss.LotSize),
				"atCutOff": map[bool]string{true: "Y", false: "N"}[op.CutOff], "price": op.Price.Fixed(2), "amount": eff.MulInt(op.Lots * iss.LotSize).Fixed(2), "series": op.Series, "clientCode": b.UCC}
			if b.Mode == ModeASBA {
				v["bankName"], v["bankAccount"] = c.Bank.Bank, c.Bank.Account
			}
			rows = append(rows, xfile.Row{Values: v})
		}
	}
	content, err := spec.Write(rows)
	if err != nil {
		return IPOBatch{}, err
	}
	now := o.now()
	prev, _ := docs.List[IPOBatch](ctx, o.Docs, tenant, cIPOBatch, "")
	seq := 1
	for _, p := range prev {
		if p.Issue == iss.ID && p.Exchange == exchange && p.Via == "File" && strings.HasPrefix(p.Created, now.Format("2006-01-02")) {
			seq++
		}
	}
	name, err := spec.FileName(xfile.Vars{"member": strings.TrimSpace(set.MemberCodes[exchange]), "symbol": iss.Symbol, "date": now.Format("2006-01-02"), "seq": fmt.Sprint(seq)})
	if err != nil {
		return IPOBatch{}, invalid("%v", err)
	}
	sum := sha256.Sum256(content)
	rf, err := o.App.Ingest.Raw.Put(ctx, ingest.RawFile{Tenant: tenant, Source: "IPO-BIDFILE-" + exchange, Name: name, SHA256: hex.EncodeToString(sum[:]), Content: content})
	if err != nil {
		return IPOBatch{}, err
	}
	b, err := o.recordBatch(ctx, tenant, actor, IPOBatch{Issue: iss.ID, Exchange: exchange, Format: spec.ID, Name: name, File: rf.ID, Via: "File", Bids: acts, Lines: len(rows)}, now)
	if err != nil {
		return b, err
	}
	o.log(ctx, tenant, actor, "ipo.bid_file", b.ID, map[string]any{"issue": iss.ID, "exchange": exchange, "bids": len(acts), "lines": len(rows), "file": rf.ID, "provisional_layout": !spec.Confirmed})
	return b, nil
}

// recordBatch stores a batch and marks its bids sent.
func (o *Ops) recordBatch(ctx context.Context, tenant, actor string, b IPOBatch, now time.Time) (IPOBatch, error) {
	n, err := o.next(ctx, tenant, "ipobatch", actor)
	if err != nil {
		return b, err
	}
	b.ID, b.Created, b.By = fmt.Sprintf("IPF-%06d", n), now.Format(time.RFC3339), actor
	if _, err := docs.Put(ctx, o.Docs, tenant, cIPOBatch, b.ID, b, 0, actor); err != nil {
		return b, err
	}
	for id, act := range b.Bids {
		_, err := docs.Update(ctx, o.Docs, tenant, cIPO, id, actor, func(x *IPOApp) error {
			x.normalise()
			if x.Pending != act {
				return nil
			}
			x.Pending, x.Sent, x.SentAt, x.Batch = "", act, now.Format(time.RFC3339), b.ID
			if act != "Cancel" && (x.Status == BidDraft || act == "Modify") {
				x.Status = BidUploaded
				if x.Mode == ModeUPI {
					x.Mandate, x.PayStatus = "Pending", ""
				}
			}
			x.event(now, actor, "%s sent to %s (%s %s)", strings.ToLower(act), b.Exchange, b.Via, firstOf(b.Name, b.ID))
			return nil
		})
		if err != nil {
			return b, err
		}
	}
	return b, nil
}

// IPOBatches lists the files and API calls sent, newest first.
func (o *Ops) IPOBatches(ctx context.Context, tenant, issue string) ([]IPOBatch, error) {
	all, err := docs.List[IPOBatch](ctx, o.Docs, tenant, cIPOBatch, "")
	out := []IPOBatch{}
	for i := len(all) - 1; i >= 0; i-- {
		if issue == "" || all[i].Issue == issue {
			out = append(out, all[i])
		}
	}
	return out, err
}

// IPOBatchFile returns a bid file as it was written.
func (o *Ops) IPOBatchFile(ctx context.Context, tenant, actor, id string) (string, []byte, error) {
	if err := o.require(ctx, tenant, actor, "Sending IPO bids to the exchange", RoleMaker, RoleChecker); err != nil {
		return "", nil, err
	}
	b, _, err := docs.Get[IPOBatch](ctx, o.Docs, tenant, cIPOBatch, id)
	if err != nil {
		return "", nil, err
	}
	if b.File == "" {
		return "", nil, invalid("%s was sent through the API; there is no file", id)
	}
	rf, err := o.App.Ingest.Raw.Get(ctx, tenant, b.File)
	if err != nil {
		return "", nil, err
	}
	return b.Name, rf.Content, nil
}

// loadIPOXFile applies the exchanges' bid response files (called for
// every checked exchange file, uploaded or fetched).
func (o *Ops) loadIPOXFile(ctx context.Context, tenant, actor string, s *xfile.Spec, f xfile.File, rows []xfile.Row, r *XFileResult) bool {
	ex := map[string]string{"nse-eipo-bid-response": "NSE", "bse-ibbs-bid-response": "BSE"}[s.ID]
	if ex == "" {
		return false
	}
	var ups []IPOStatusUpdate
	for _, row := range rows {
		ups = append(ups, IPOStatusUpdate{Ref: row.Get("referenceNumber"), AppNo: row.Get("applicationNumber"), Symbol: row.Get("symbol"), PAN: row.Get("pan"), Exchange: ex,
			Activity: row.Get("activityType"), Status: row.Get("status"), Code: row.Get("errorCode"), Reason: row.Get("reason"), PayStatus: row.Get("upiStatus"), UMN: row.Get("umn"),
			DPStatus: row.Get("dpVerStatus"), DPReason: row.Get("dpVerReason"), At: row.Get("timestamp"), Source: f.Name})
	}
	n, unknown, err := o.ApplyIPOUpdates(ctx, tenant, actor, ups)
	if err != nil {
		r.Loaded = "Not loaded: " + err.Error()
		return true
	}
	r.Loaded, r.Added, r.Skipped = "Applied to IPO bids (provisional layout)", n, unknown
	if unknown > 0 {
		r.Note = fmt.Sprintf("%d line(s) are for bids not placed through Ledgerline.", unknown)
	}
	return true
}
