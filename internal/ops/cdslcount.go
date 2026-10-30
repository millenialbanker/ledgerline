package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"ledgerline/internal/docs"
)

// ---- CDSL API: BO active demat account count (CDSL API Handbook §5.3) ----
//
// CDSL says how many active demat accounts it holds against a PAN, a
// mobile number and an email. The check runs by itself whenever one of the
// DP's CDSL accounts is opened or its PAN, mobile, email or BO ID changes
// (the scheduler compares each account with what was last checked), and
// staff can run it from the client. The result is shown on the client:
//   - more than one account for the PAN: the client has another demat
//     account, so the account cannot be a BSDA (a BSDA is the investor's
//     only demat account); the DP record is marked "has another demat";
//   - an email or mobile on more than one account: SEBI allows that only
//     within a family, with the client's declaration.
// CDSL counts its own accounts only; accounts at NSDL are not included.

const cCDSLCount = "cdslcount"

// CDSLCount is the latest check for a client.
type CDSLCount struct {
	UCC         string         `json:"ucc"`
	BOID        string         `json:"bo_id,omitempty"`
	Fingerprint string         `json:"fingerprint,omitempty"`
	Status      string         `json:"status"` // Checked, Failed, Not checked
	Due         bool           `json:"due,omitempty"`
	At          string         `json:"at,omitempty"`
	By          string         `json:"by,omitempty"`
	RequestID   string         `json:"request_id,omitempty"`
	PAN         int            `json:"pan_count"`
	Email       int            `json:"email_count"`
	Mobile      int            `json:"mobile_count"`
	Notes       []string       `json:"notes,omitempty"`
	Error       string         `json:"error,omitempty"`
	Request     string         `json:"request,omitempty"`  // what was asked (PAN and mobile masked)
	Response    string         `json:"response,omitempty"` // CDSL's answer, decrypted
	History     []CDSLCountRun `json:"history,omitempty"`
}

// CDSLCountRun is one earlier check.
type CDSLCountRun struct {
	At     string `json:"at"`
	PAN    int    `json:"pan_count"`
	Email  int    `json:"email_count"`
	Mobile int    `json:"mobile_count"`
	Ref    string `json:"request_id"`
	Error  string `json:"error,omitempty"`
}

// cdslCountKey is what the check depends on.
func cdslCountKey(c Client) string {
	return sha256Hex([]byte(strings.Join([]string{c.PAN, c.Mobile, strings.ToLower(c.Email), c.Demat.ID}, "|")))
}

// cdslCountSoon asks the scheduler to check a client on its next run.
func (o *Ops) cdslCountSoon(ctx context.Context, tenant, ucc string) {
	_, _ = upd(ctx, o.Docs, tenant, cCDSLCount, ucc, SystemActor, &CDSLCount{UCC: ucc, Status: "Not checked"}, func(x *CDSLCount) error {
		x.Due = true
		return nil
	})
}

// CDSLDematCheck is a client's latest check.
func (o *Ops) CDSLDematCheck(ctx context.Context, tenant, actor, ucc string) (CDSLCount, error) {
	if err := o.require(ctx, tenant, actor, "Viewing CDSL demat account counts", RoleMaker, RoleChecker, RoleCompliance, RoleRisk); err != nil {
		return CDSLCount{}, err
	}
	ucc = strings.ToUpper(ucc)
	x, _, err := docs.GetOr(ctx, o.Docs, tenant, cCDSLCount, ucc, CDSLCount{UCC: ucc, Status: "Not checked"})
	return x, err
}

// RunCDSLDematCheck checks a client now.
func (o *Ops) RunCDSLDematCheck(ctx context.Context, tenant, actor, ucc string) (CDSLCount, error) {
	if err := o.require(ctx, tenant, actor, "Checking CDSL demat account counts", RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return CDSLCount{}, err
	}
	x, ok := o.cdslConnFor(ctx, tenant)
	if !ok || !x.On(cdslFamBOCount) {
		return CDSLCount{}, fmt.Errorf("%w: the CDSL active demat account API is off in Connections", ErrState)
	}
	c, err := o.Client(ctx, tenant, ucc)
	if err != nil {
		return CDSLCount{}, err
	}
	res := o.cdslCountRun(ctx, tenant, actor, x, c)
	if res.Status == "Failed" {
		return res, fmt.Errorf("%s", res.Error)
	}
	return res, nil
}

// cdslCountTick checks the accounts that are new or changed, a few at a
// time. Accounts that were already there when the API was switched on are
// noted, not checked, until something about them changes.
func (o *Ops) cdslCountTick(ctx context.Context, tenant string, x cdslConn, now time.Time) {
	cs, _ := o.Clients(ctx, tenant)
	have := map[string]CDSLCount{}
	all, _ := docs.List[CDSLCount](ctx, o.Docs, tenant, cCDSLCount, "")
	for _, a := range all {
		have[a.UCC] = a
	}
	baseline := len(all) == 0
	runs := 0
	for _, c := range cs {
		if c.Demat.Depository != "CDSL" || c.Demat.ID == "" || c.Status == "Closed" {
			continue
		}
		k := cdslCountKey(c)
		cur, seen := have[c.UCC]
		switch {
		case !seen && baseline:
			_ = docs.Upsert(ctx, o.Docs, tenant, cCDSLCount, c.UCC, CDSLCount{UCC: c.UCC, BOID: c.Demat.ID, Fingerprint: k, Status: "Not checked"}, SystemActor)
			continue
		case seen && cur.Fingerprint == k && !cur.Due:
			continue
		case seen && cur.Status == "Failed" && cur.Fingerprint == k && cdslSince(cur.At, now) < time.Hour:
			continue
		}
		if runs >= 20 {
			return
		}
		runs++
		o.cdslCountRun(ctx, tenant, SystemActor, x, c)
	}
}

// cdslCountRun asks CDSL and keeps the answer on the client.
func (o *Ops) cdslCountRun(ctx context.Context, tenant, actor string, x cdslConn, c Client) CDSLCount {
	now := o.now().Format(time.RFC3339)
	prev, _, _ := docs.GetOr(ctx, o.Docs, tenant, cCDSLCount, c.UCC, CDSLCount{UCC: c.UCC})
	res := CDSLCount{UCC: c.UCC, BOID: c.Demat.ID, Fingerprint: cdslCountKey(c), At: now, By: actor, History: prev.History}
	fail := func(err error) CDSLCount {
		res.Status, res.Error = "Failed", err.Error()
		res.History = append(res.History, CDSLCountRun{At: now, Ref: res.RequestID, Error: res.Error})
		if len(res.History) > 10 {
			res.History = res.History[len(res.History)-10:]
		}
		_ = docs.Upsert(ctx, o.Docs, tenant, cCDSLCount, c.UCC, res, actor)
		o.log(ctx, tenant, actor, "cdsl.demat_count_error", c.UCC, map[string]any{"error": res.Error})
		return res
	}
	rid, err := o.cdslReqID(ctx, tenant, actor, 16)
	if err != nil {
		return fail(err)
	}
	res.RequestID = rid
	// holderdetails: pan, mobileno, email, in the order of the handbook's sample.
	hd := struct {
		PAN    string `json:"pan"`
		Mobile string `json:"mobileno"`
		Email  string `json:"email"`
	}{c.PAN, c.Mobile, c.Email}
	sealed, err := x.seal(hd)
	if err != nil {
		return fail(err)
	}
	body := cdslJSON("requestid", rid, "version", "1.0", "dpid", x.DPID, "holderdetails", sealed)
	res.Request = string(cdslJSON("requestid", rid, "version", "1.0", "dpid", x.DPID, "holderdetails", fmt.Sprintf(`{"pan":%q,"mobileno":%q,"email":%q} (encrypted)`, MaskPAN(c.PAN), maskTail(c.Mobile), c.Email)))
	resp, err := x.do(ctx, cdslRequest{Path: cdslPathBOCount, JSON: body})
	if err != nil {
		return fail(err)
	}
	m, err := cdslCountAnswer(x, resp.Body)
	if err != nil {
		return fail(err)
	}
	b, _ := json.Marshal(m)
	res.Response = string(b)
	if got := cdslStr(m, "requestid"); got != "" && got != rid {
		return fail(fmt.Errorf("CDSL answered request %s, not %s", got, rid))
	}
	if cdslStr(m, "reqstatus") != "0" {
		return fail(fmt.Errorf("CDSL could not count the accounts: %s", cdslExplain(cdslStr(m, "reserror"), cdslStr(m, "reserrmsg"))))
	}
	hc, err := cdslCountData(x, m["holdercountdata"])
	if err != nil {
		return fail(err)
	}
	num := func(k string) int { n, _ := strconv.Atoi(cdslStr(hc, k)); return n }
	res.PAN, res.Email, res.Mobile, res.Status = num("pancount"), num("emailcount"), num("mobilenocount"), "Checked"
	b, _ = json.Marshal(map[string]any{"answer": m, "holdercountdata": hc})
	res.Response = string(b)
	bo := o.bo(ctx, tenant, c)
	if res.PAN > 1 {
		res.Notes = append(res.Notes, fmt.Sprintf("CDSL holds %d active demat accounts for this PAN: the client has another demat account", res.PAN))
		if !bo.OtherDemat {
			bo.OtherDemat = true
			_ = docs.Upsert(ctx, o.Docs, tenant, cBO, bo.UCC, bo, actor)
		}
		if bo.BSDA {
			res.Notes = append(res.Notes, "The account is a BSDA, which must be the investor's only demat account: review it")
			o.raiseWork(ctx, tenant, "cdslcount", c.UCC, "BSDA with another demat account: "+c.UCC,
				fmt.Sprintf("CDSL reports %d active demat accounts for %s's PAN, but %s is a Basic Services Demat Account. Convert it to a regular account in the BSDA review, or record why it stays.", res.PAN, c.Name, cdslMaskBO(c.Demat.ID)), c.UCC, RoleCompliance, "dp", "")
		}
	}
	if res.Email > 1 {
		res.Notes = append(res.Notes, fmt.Sprintf("The email is on %d active demat accounts: allowed only within a family, with the client's declaration", res.Email))
	}
	if res.Mobile > 1 {
		res.Notes = append(res.Notes, fmt.Sprintf("The mobile number is on %d active demat accounts: allowed only within a family, with the client's declaration", res.Mobile))
	}
	res.History = append(res.History, CDSLCountRun{At: now, PAN: res.PAN, Email: res.Email, Mobile: res.Mobile, Ref: rid})
	if len(res.History) > 10 {
		res.History = res.History[len(res.History)-10:]
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cCDSLCount, c.UCC, res, actor)
	o.log(ctx, tenant, actor, "cdsl.demat_count", c.UCC, map[string]any{"bo": cdslMaskBO(c.Demat.ID), "pan": res.PAN, "email": res.Email, "mobile": res.Mobile})
	return res
}

// cdslCountAnswer reads the answer, plain JSON or encrypted as a whole.
func cdslCountAnswer(x cdslConn, b []byte) (map[string]any, error) {
	var m map[string]any
	if json.Unmarshal(cdslCleanJSON(b), &m) == nil && m != nil {
		return cdslBody(b), nil
	}
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if err := x.open(s, &m); err != nil {
		return nil, fmt.Errorf("CDSL's answer could not be read: %v", err)
	}
	return m, nil
}

// cdslCountData reads holdercountdata: an object, or one encrypted.
func cdslCountData(x cdslConn, v any) (map[string]any, error) {
	switch t := v.(type) {
	case map[string]any:
		return t, nil
	case string:
		if strings.TrimSpace(t) == "" {
			return nil, fmt.Errorf("CDSL's answer has no counts")
		}
		var m map[string]any
		if err := x.open(t, &m); err != nil {
			return nil, err
		}
		return m, nil
	}
	return nil, fmt.Errorf("CDSL's answer has no counts")
}

func maskTail(s string) string {
	if len(s) <= 4 {
		return s
	}
	return strings.Repeat("X", len(s)-4) + s[len(s)-4:]
}
