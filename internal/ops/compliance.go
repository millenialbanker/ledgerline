package ops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// ---- PMLA, KYC registries, debarred entities, documents, recons ----
//
// Client risk (PMLA): every client is Low, Medium or High risk. Clients of
// special category (CSC) such as non-residents, politically exposed persons,
// trusts and charities, and clients from high-risk jurisdictions are High.
// Periodic updation of KYC follows the risk: every 2 years for High, 8 for
// Medium and 10 for Low.
//
// SEBI and exchange debarment orders, and the UN Security Council sanctions
// lists, are loaded as files. A client whose PAN matches is blocked at once
// and an alert raised; a name-only match is raised for review. Onboarding a
// matching PAN is refused.
//
// KRA and CKYC: the upload file lists new and changed clients; status files
// from the KRA and CKYC numbers from CERSAI are loaded back.

const (
	cDebarred  = "debarred"
	cClientDoc = "clientdoc"
	cSTTStmt   = "stt_stmt"
)

// SpecialCategories are the PMLA client categories treated as high risk.
var SpecialCategories = []string{"NRI", "Politically exposed person", "Relative of a PEP", "Trust / charity / NGO", "High-risk jurisdiction", "Non-face-to-face", "Company with close family shareholding", "High net worth", "Dormant account reactivated"}

// highCSC are the categories that make a client High risk by themselves.
var highCSC = map[string]bool{"NRI": true, "Politically exposed person": true, "Relative of a PEP": true, "Trust / charity / NGO": true, "High-risk jurisdiction": true}

// Debarred is one entry of a debarment or sanctions list.
type Debarred struct {
	ID        string `json:"id"`
	PAN       string `json:"pan,omitempty"`
	Name      string `json:"name"`
	Regulator string `json:"regulator"` // SEBI, NSE, BSE, MCX, UNSC
	OrderNo   string `json:"order_no"`
	OrderDate string `json:"order_date"`
	Till      string `json:"till,omitempty"` // empty: until further orders
}

// ClientDoc is a scanned document held for a client.
type ClientDoc struct {
	ID          string `json:"id"`
	UCC         string `json:"ucc"`
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
	FileID      string `json:"file_id"`
	By          string `json:"by"`
	At          string `json:"at"`
}

// DocKinds are the document kinds held for clients.
var DocKinds = []string{"PAN card", "Address proof", "Photograph", "Signature", "Bank proof", "Income proof", "KYC form", "Account opening agreement", "DDPI / POA", "Nomination form", "IPV / VIPV record", "Other"}

func reKYCYears(risk string) int {
	switch risk {
	case "High":
		return 2
	case "Medium":
		return 8
	}
	return 10
}

// suggestedRisk applies the firm's PMLA rules to a client.
func suggestedRisk(c Client, debarredHit bool, strFiled bool, turnover money.Dec) (string, []string) {
	var why []string
	if debarredHit {
		why = append(why, "matches a debarment or sanctions list")
	}
	if strFiled {
		why = append(why, "suspicious transaction report filed")
	}
	for _, x := range c.CSC {
		if highCSC[x] {
			why = append(why, "special category: "+x)
		}
	}
	if c.Type == "NRI" && !contains(c.CSC, "NRI") {
		why = append(why, "non-resident")
	}
	if len(why) > 0 {
		return "High", why
	}
	// Activity out of line with declared income: turnover above twenty
	// times the top of the income band.
	limits := map[string]int64{"<1 L": 100000, "1-5 L": 500000, "5-10 L": 1000000, "10-25 L": 2500000, "25 L-1 Cr": 10000000}
	if lim, ok := limits[c.Income]; ok && turnover.Cmp(money.DecInt(lim*20)) > 0 {
		why = append(why, fmt.Sprintf("turnover ₹%s is out of line with declared income %s", formatINR(turnover), c.Income))
	}
	if len(c.CSC) > 0 {
		why = append(why, "special category: "+strings.Join(c.CSC, ", "))
	}
	if len(why) > 0 {
		return "Medium", why
	}
	return "Low", nil
}


// PMLARow is one client's PMLA and KYC position.
type PMLARow struct {
	UCC        string    `json:"ucc"`
	Name       string    `json:"name"`
	PAN        string    `json:"pan"`
	Type       string    `json:"type"`
	CSC        []string  `json:"csc"`
	Risk       string    `json:"risk"`
	Suggested  string    `json:"suggested"`
	Why        []string  `json:"why"`
	Turnover   money.Dec `json:"turnover"` // financial year to date
	KYCDate    string    `json:"kyc_date"`
	ReKYCDue   string    `json:"rekyc_due"`
	ReKYCState string    `json:"rekyc_state"` // Current, Due soon, Overdue
	KRAStatus  string    `json:"kra_status"`
	CKYC       string    `json:"ckyc"`
	Debarred   []string  `json:"debarred"`
	Blocked    bool      `json:"blocked"`
	Review     bool      `json:"review"` // rules point to a higher category
}

var riskRank = map[string]int{"Low": 1, "Medium": 2, "High": 3}

// PMLAView is the compliance desk.
type PMLAView struct {
	Date     string         `json:"date"`
	Rows     []PMLARow      `json:"rows"`
	Lists    []Debarred     `json:"lists"`
	Counts   map[string]int `json:"counts"`
	CSC      []string       `json:"csc"`
	DocKinds []string       `json:"doc_kinds"`
}

func normName(s string) string {
	f := strings.Fields(strings.ToUpper(strings.NewReplacer(".", " ", ",", " ", "-", " ").Replace(s)))
	var keep []string
	for _, w := range f {
		if w != "MR" && w != "MS" && w != "MRS" && w != "DR" && w != "SHRI" && w != "SMT" {
			keep = append(keep, w)
		}
	}
	sort.Strings(keep)
	return strings.Join(keep, " ")
}

// debarredMatches finds list entries in force on date that match a client
// by PAN (certain) or by name (to review).
func debarredMatches(c Client, list []Debarred, date string) (byPAN, byName []Debarred) {
	nn := normName(c.Name)
	for _, d := range list {
		if d.Till != "" && d.Till < date {
			continue
		}
		switch {
		case d.PAN != "" && strings.EqualFold(d.PAN, c.PAN):
			byPAN = append(byPAN, d)
		case nn != "" && normName(d.Name) == nn:
			byName = append(byName, d)
		}
	}
	return
}

// PMLA builds the compliance desk for a date.
func (o *Ops) PMLA(ctx context.Context, tenant, date string) (PMLAView, error) {
	v := PMLAView{Date: date, Rows: []PMLARow{}, Counts: map[string]int{}, CSC: SpecialCategories, DocKinds: DocKinds}
	cs, err := o.ClientMap(ctx, tenant)
	if err != nil {
		return v, err
	}
	list, _ := docs.List[Debarred](ctx, o.Docs, tenant, cDebarred, "")
	v.Lists = list
	if v.Lists == nil {
		v.Lists = []Debarred{}
	}
	alerts, _ := o.Alerts(ctx, tenant)
	str := map[string]bool{}
	for _, a := range alerts {
		if a.Status == "STR filed" {
			str[a.UCC] = true
		}
	}
	turnover := map[string]money.Dec{}
	from := fyStartOf(date)
	ts, _ := o.App.Trades.List(ctx, tenant, "EXCHANGE", "")
	for _, t := range ts {
		if t.TradeDate >= from && t.TradeDate <= date && (t.Segment == "CM" || t.InstrumentType == "FUT") {
			turnover[t.UCC] = turnover[t.UCC].Add(t.Value())
		}
	}
	for _, u := range sortedKeys(cs) {
		c := cs[u]
		if c.Status != "Active" {
			continue
		}
		byPAN, byName := debarredMatches(c, list, date)
		var hits []string
		for _, d := range append(byPAN, byName...) {
			hits = append(hits, fmt.Sprintf("%s %s (%s)", d.Regulator, d.OrderNo, map[bool]string{true: "PAN", false: "name"}[d.PAN != "" && strings.EqualFold(d.PAN, c.PAN)]))
		}
		r := PMLARow{UCC: u, Name: c.Name, PAN: MaskPAN(c.PAN), Type: c.Type, CSC: c.CSC, Risk: c.Risk, Turnover: turnover[u], KYCDate: c.KYCDate,
			KRAStatus: c.KRAStatus, CKYC: c.CKYC, Debarred: hits, Blocked: c.Blocked}
		if r.CSC == nil {
			r.CSC = []string{}
		}
		if r.Debarred == nil {
			r.Debarred = []string{}
		}
		r.Suggested, r.Why = suggestedRisk(c, len(byPAN) > 0, str[u], turnover[u])
		if r.Why == nil {
			r.Why = []string{}
		}
		last := c.KYCDate
		if c.ReKYCOn > last {
			last = c.ReKYCOn
		}
		if last != "" {
			r.ReKYCDue = addMonths(last, 12*reKYCYears(c.Risk))
			switch {
			case r.ReKYCDue < date:
				r.ReKYCState = "Overdue"
			case r.ReKYCDue <= addDays(date, 90):
				r.ReKYCState = "Due soon"
			default:
				r.ReKYCState = "Current"
			}
		}
		v.Counts["Risk "+c.Risk]++
		// The firm may rate a client higher than the rules; only a rule
		// pointing higher than the recorded category needs review.
		if riskRank[r.Suggested] > riskRank[c.Risk] {
			r.Review = true
			v.Counts["Risk to review"]++
		}
		v.Counts["Re-KYC "+r.ReKYCState]++
		if len(hits) > 0 {
			v.Counts["List matches"]++
		}
		if c.KRAStatus != "Validated" && c.KRAStatus != "Registered" {
			v.Counts["KRA not validated"]++
		}
		if c.CKYC == "" {
			v.Counts["No CKYC number"]++
		}
		v.Rows = append(v.Rows, r)
	}
	return v, nil
}

// SetClientRisk records the compliance officer's risk category and special
// categories for a client.
func (o *Ops) SetClientRisk(ctx context.Context, tenant, actor, ucc, risk string, csc []string, note string) (Client, error) {
	if err := o.require(ctx, tenant, actor, "Setting client risk", RoleCompliance); err != nil {
		return Client{}, err
	}
	if risk != "Low" && risk != "Medium" && risk != "High" {
		return Client{}, invalid("risk must be Low, Medium or High")
	}
	for _, x := range csc {
		if !contains(SpecialCategories, x) {
			return Client{}, invalid("unknown special category %q", x)
		}
	}
	c, err := docs.Update(ctx, o.Docs, tenant, cClient, strings.ToUpper(ucc), actor, func(x *Client) error {
		x.Risk, x.CSC, x.RiskReviewedOn = risk, csc, o.now().Format("2006-01-02")
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "pmla.risk", c.UCC, map[string]any{"risk": risk, "csc": csc, "note": note})
	}
	return c, err
}

// RecordReKYC records a completed periodic KYC update.
func (o *Ops) RecordReKYC(ctx context.Context, tenant, actor, ucc, date, mode string) (Client, error) {
	if err := o.require(ctx, tenant, actor, "Recording re-KYC", RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return Client{}, err
	}
	if !ledger.ValidDate(date) || strings.TrimSpace(mode) == "" {
		return Client{}, invalid("give the date and how the KYC was updated")
	}
	c, err := docs.Update(ctx, o.Docs, tenant, cClient, strings.ToUpper(ucc), actor, func(x *Client) error {
		x.ReKYCOn, x.ReKYCMode = date, mode
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "kyc.updated", c.UCC, map[string]any{"date": date, "mode": mode})
	}
	return c, err
}

// screenDebarred checks every active client against the lists, blocking PAN
// matches and raising alerts. It runs when a list is loaded.
func (o *Ops) screenDebarred(ctx context.Context, tenant, actor, date string) (blocked, review int, err error) {
	list, err := docs.List[Debarred](ctx, o.Docs, tenant, cDebarred, "")
	if err != nil {
		return 0, 0, err
	}
	cs, err := o.Clients(ctx, tenant)
	if err != nil {
		return 0, 0, err
	}
	for _, c := range cs {
		if c.Status != "Active" {
			continue
		}
		byPAN, byName := debarredMatches(c, list, date)
		for _, d := range byPAN {
			if !c.Blocked {
				reason := fmt.Sprintf("Debarred: %s order %s of %s", d.Regulator, d.OrderNo, d.OrderDate)
				if _, err := docs.Update(ctx, o.Docs, tenant, cClient, c.UCC, actor, func(x *Client) error {
					x.Blocked, x.BlockedReason = true, reason
					return nil
				}); err != nil {
					return blocked, review, err
				}
				c.Blocked = true
				blocked++
				o.log(ctx, tenant, actor, "client.debarred", c.UCC, map[string]any{"order": d.OrderNo, "regulator": d.Regulator})
			}
			o.raiseAlert(ctx, tenant, actor, Alert{Source: "Internal", Type: "Debarred entity", UCC: c.UCC, Severity: "High",
				Detail: fmt.Sprintf("PAN matches %s order %s (%s); trading blocked", d.Regulator, d.OrderNo, d.Name)}, "DEB:"+c.UCC+":"+d.ID)
		}
		for _, d := range byName {
			if o.raiseAlert(ctx, tenant, actor, Alert{Source: "Internal", Type: "Possible debarred entity", UCC: c.UCC, Severity: "Medium",
				Detail: fmt.Sprintf("Name matches %s order %s (%s, PAN %s); verify identity", d.Regulator, d.OrderNo, d.Name, MaskPAN(d.PAN))}, "DEBN:"+c.UCC+":"+d.ID) {
				review++
			}
		}
	}
	return blocked, review, nil
}

// IsDebarredPAN reports whether a PAN is on a list in force.
func (o *Ops) IsDebarredPAN(ctx context.Context, tenant, pan, date string) (Debarred, bool) {
	list, _ := docs.List[Debarred](ctx, o.Docs, tenant, cDebarred, "")
	for _, d := range list {
		if d.PAN != "" && strings.EqualFold(d.PAN, pan) && (d.Till == "" || d.Till >= date) {
			return d, true
		}
	}
	return Debarred{}, false
}

// prepareCompliance validates and applies compliance files.
func (o *Ops) prepareCompliance(ctx context.Context, tenant, actor, source, date string, rows []row) (func() (int, int, error), []ingest.ParseError) {
	var errs []ingest.ParseError
	bad := func(l int, f, m string, a ...any) {
		errs = append(errs, ingest.ParseError{Line: l, Field: f, Msg: fmt.Sprintf(m, a...)})
	}
	switch source {
	case "DEBARRED-LIST":
		var ds []Debarred
		for _, r := range rows {
			d := Debarred{PAN: strings.ToUpper(r.get("PAN")), Name: r.get("Name"), Regulator: strings.ToUpper(r.get("Regulator")), OrderNo: r.get("OrderNo"), OrderDate: r.get("OrderDate"), Till: r.get("Till")}
			if d.Name == "" {
				bad(r.line, "Name", "is required")
			}
			if d.PAN != "" && !rePAN.MatchString(d.PAN) {
				bad(r.line, "PAN", "is not a valid PAN")
			}
			if !ledger.ValidDate(d.OrderDate) {
				bad(r.line, "OrderDate", "must be YYYY-MM-DD")
			}
			if d.Till != "" && !ledger.ValidDate(d.Till) {
				bad(r.line, "Till", "must be YYYY-MM-DD or empty")
			}
			h := sha256.Sum256([]byte(d.Regulator + "|" + d.OrderNo + "|" + d.PAN + "|" + d.Name))
			d.ID = hex.EncodeToString(h[:])[:16]
			ds = append(ds, d)
		}
		return func() (int, int, error) {
			n, dup := 0, 0
			for _, d := range ds {
				if _, _, err := docs.Get[Debarred](ctx, o.Docs, tenant, cDebarred, d.ID); err == nil {
					dup++
					continue
				}
				if _, err := docs.Put(ctx, o.Docs, tenant, cDebarred, d.ID, d, 0, actor); err != nil {
					return n, dup, err
				}
				n++
			}
			b, rv, err := o.screenDebarred(ctx, tenant, actor, o.fileDate(date))
			o.log(ctx, tenant, actor, "debarred.screened", "", map[string]any{"blocked": b, "to_review": rv})
			return n, dup, err
		}, errs
	case "KRA-STATUS":
		type st struct{ pan, status, remark string }
		var ss []st
		valid := map[string]bool{"Registered": true, "Validated": true, "On hold": true, "Rejected": true, "Under process": true, "Not available": true}
		for _, r := range rows {
			s := st{strings.ToUpper(r.get("PAN")), r.get("Status"), r.get("Remarks")}
			if !valid[s.status] {
				bad(r.line, "Status", "must be Registered, Validated, On hold, Rejected, Under process or Not available")
			}
			ss = append(ss, s)
		}
		return func() (int, int, error) {
			cs, _ := o.Clients(ctx, tenant)
			byPAN := map[string]string{}
			for _, c := range cs {
				byPAN[c.PAN] = c.UCC
			}
			n, skip := 0, 0
			for _, s := range ss {
				u, ok := byPAN[s.pan]
				if !ok {
					skip++
					continue
				}
				c, err := docs.Update(ctx, o.Docs, tenant, cClient, u, actor, func(x *Client) error {
					x.KRAStatus, x.KRARemark = s.status, s.remark
					return nil
				})
				if err != nil {
					return n, skip, err
				}
				if (s.status == "On hold" || s.status == "Rejected") && c.Status == "Active" {
					o.raiseAlert(ctx, tenant, actor, Alert{Source: "Internal", Type: "KRA " + strings.ToLower(s.status), UCC: u, Severity: "Medium",
						Detail: "KYC is " + s.status + " at the KRA: " + s.remark + ". Collect documents and re-upload; new accounts cannot be opened on this KYC."}, "KRA:"+u+":"+s.status)
				}
				n++
			}
			return n, skip, nil
		}, errs
	case "CKYC-NUMBERS":
		type ck struct{ pan, num string }
		var ns []ck
		for _, r := range rows {
			c := ck{strings.ToUpper(r.get("PAN")), r.get("CKYCNumber")}
			if len(c.num) != 14 || strings.Trim(c.num, "0123456789") != "" {
				bad(r.line, "CKYCNumber", "must be the 14-digit CKYC identifier")
			}
			ns = append(ns, c)
		}
		return func() (int, int, error) {
			cs, _ := o.Clients(ctx, tenant)
			byPAN := map[string]string{}
			for _, c := range cs {
				byPAN[c.PAN] = c.UCC
			}
			n, skip := 0, 0
			for _, c := range ns {
				u, ok := byPAN[c.pan]
				if !ok {
					skip++
					continue
				}
				if _, err := docs.Update(ctx, o.Docs, tenant, cClient, u, actor, func(x *Client) error { x.CKYC = c.num; return nil }); err != nil {
					return n, skip, err
				}
				n++
			}
			return n, skip, nil
		}, errs
	case "STT-STATEMENT":
		var ss []STTLine
		for _, r := range rows {
			l := STTLine{Date: date, Exchange: strings.ToUpper(r.get("Exchange")), Segment: strings.ToUpper(r.get("Segment"))}
			var err error
			if l.Turnover, err = money.ParseAmount(r.get("Turnover")); err != nil {
				bad(r.line, "Turnover", "must be rupees")
			}
			if l.STT, err = money.ParseAmount(r.get("STT")); err != nil {
				bad(r.line, "STT", "must be rupees")
			}
			ss = append(ss, l)
		}
		return func() (int, int, error) {
			for _, l := range ss {
				if err := docs.Upsert(ctx, o.Docs, tenant, cSTTStmt, date+"|"+l.Exchange+"-"+l.Segment, l, actor); err != nil {
					return 0, 0, err
				}
			}
			return len(ss), 0, nil
		}, errs
	}
	return nil, []ingest.ParseError{{Msg: "unknown compliance file " + source}}
}

// KRAUploadFile lists clients whose KYC must be uploaded to the KRA: new
// accounts and changed contact details since `since`.
func (o *Ops) KRAUploadFile(ctx context.Context, tenant, since string) ([]byte, error) {
	cs, err := o.Clients(ctx, tenant)
	if err != nil {
		return nil, err
	}
	rows := [][]string{{"PAN", "Name", "DOB", "Mobile", "Email", "City", "State", "KYCMode", "IPVDone", "AccountOpened", "Change", "UCC"}}
	for _, c := range cs {
		change := ""
		switch {
		case c.KYCDate >= since && c.KRAStatus == "":
			change = "New"
		case c.KYCUpdatedOn >= since && c.KYCUpdatedOn != "":
			change = "Modify"
		default:
			continue
		}
		rows = append(rows, []string{c.PAN, c.Name, c.DOB, c.Mobile, c.Email, c.City, c.State, c.KYCRoute, "Y", c.KYCDate, change, c.UCC})
	}
	return CSV(rows), nil
}

// CKYCRequestFile lists active clients without a CKYC number for a bulk
// search and download request to CERSAI.
func (o *Ops) CKYCRequestFile(ctx context.Context, tenant string) ([]byte, error) {
	cs, err := o.Clients(ctx, tenant)
	if err != nil {
		return nil, err
	}
	rows := [][]string{{"IDType", "IDNumber", "Name", "DOB", "UCC"}}
	for _, c := range cs {
		if c.Status == "Active" && c.CKYC == "" {
			rows = append(rows, []string{"C", c.PAN, c.Name, c.DOB, c.UCC}) // C = PAN in the CKYC search
		}
	}
	return CSV(rows), nil
}

// ---- STT and turnover reconciliation ----

// STTLine is the exchange's statement of STT and turnover for a segment.
type STTLine struct {
	Date     string       `json:"date"`
	Exchange string       `json:"exchange"`
	Segment  string       `json:"segment"`
	Turnover money.Amount `json:"turnover"`
	STT      money.Amount `json:"stt"`
}

// STTReconRow is one exchange segment in the reconciliation.
type STTReconRow struct {
	Exchange string       `json:"exchange"`
	Segment  string       `json:"segment"`
	OurTurn  money.Amount `json:"our_turnover"`
	ExchTurn money.Amount `json:"exchange_turnover"`
	OurSTT   money.Amount `json:"our_stt"`
	ExchSTT  money.Amount `json:"exchange_stt"`
	DiffTurn money.Amount `json:"diff_turnover"`
	DiffSTT  money.Amount `json:"diff_stt"`
	HasStmt  bool         `json:"has_statement"`
	Status   string       `json:"status"` // Matched, Difference, No statement
}

// STTRecon compares STT and turnover as billed with the exchange's statement.
type STTRecon struct {
	Date string        `json:"date"`
	Rows []STTReconRow `json:"rows"`
}

// STTReconcile builds the reconciliation for a date.
func (o *Ops) STTReconcile(ctx context.Context, tenant, date string) (STTRecon, error) {
	r := STTRecon{Date: date, Rows: []STTReconRow{}}
	ts, err := o.exchangeTrades(ctx, tenant, date)
	if err != nil {
		return r, err
	}
	turn := map[string]money.Dec{}
	for _, t := range ts {
		// Options turnover is the premium value, as the exchanges report it.
		turn[t.Exchange+"-"+t.Segment] = turn[t.Exchange+"-"+t.Segment].Add(t.Value())
	}
	es, err := o.App.Ledger.Store.List(ctx, tenant, ledgerFilterDate(date))
	if err != nil {
		return r, err
	}
	stt := map[string]money.Amount{}
	for _, e := range es {
		if e.Source.Kind != "BILL" && e.Source.Kind != "REVERSAL" {
			continue
		}
		p := strings.Split(e.Source.ID, "/")
		seg := ""
		if len(p) == 3 {
			seg = p[2]
		}
		if e.Source.Kind == "REVERSAL" {
			if orig, err := o.App.Ledger.Store.Get(ctx, tenant, e.ReversalOf); err == nil {
				if q := strings.Split(orig.Source.ID, "/"); len(q) == 3 {
					seg = q[2]
				}
			}
		}
		for _, x := range e.Postings {
			if x.Account == ledger.PayableSTT {
				stt[seg] += x.Credit - x.Debit
			}
		}
	}
	stmts, _ := docs.List[STTLine](ctx, o.Docs, tenant, cSTTStmt, date+"|")
	sm := map[string]STTLine{}
	for _, s := range stmts {
		sm[s.Exchange+"-"+s.Segment] = s
	}
	keys := map[string]bool{}
	for k := range turn {
		keys[k] = true
	}
	for k := range sm {
		keys[k] = true
	}
	for _, k := range sortedKeys(keys) {
		ex, seg, _ := strings.Cut(k, "-")
		x := STTReconRow{Exchange: ex, Segment: seg, OurTurn: turn[k].Round(money.UnitPaisa), OurSTT: stt[k]}
		if s, ok := sm[k]; ok {
			x.HasStmt, x.ExchTurn, x.ExchSTT = true, s.Turnover, s.STT
			x.DiffTurn, x.DiffSTT = x.OurTurn-x.ExchTurn, x.OurSTT-x.ExchSTT
			x.Status = "Matched"
			// STT is rounded per contract note; allow a rupee per segment.
			if x.DiffSTT > 100 || x.DiffSTT < -100 || x.DiffTurn > 100 || x.DiffTurn < -100 {
				x.Status = "Difference"
			}
		} else {
			x.Status = "No statement"
		}
		r.Rows = append(r.Rows, x)
	}
	return r, nil
}

// ---- documents ----

// MaxDocBytes is the largest document accepted.
const MaxDocBytes = 5 << 20

var docTypes = map[string]string{"application/pdf": ".pdf", "image/jpeg": ".jpg", "image/png": ".png"}

// AddClientDoc stores a scanned document for a client.
func (o *Ops) AddClientDoc(ctx context.Context, tenant, actor, ucc, kind, name string, data []byte) (ClientDoc, error) {
	if err := o.require(ctx, tenant, actor, "Adding client documents", RoleMaker, RoleChecker, RoleCompliance, RoleBranch); err != nil {
		return ClientDoc{}, err
	}
	ucc = strings.ToUpper(ucc)
	if _, err := o.Client(ctx, tenant, ucc); err != nil {
		return ClientDoc{}, err
	}
	if !contains(DocKinds, kind) {
		return ClientDoc{}, invalid("document kind must be one of %s", strings.Join(DocKinds, ", "))
	}
	if len(data) == 0 || len(data) > MaxDocBytes {
		return ClientDoc{}, invalid("documents must be between 1 byte and 5 MB")
	}
	ct := http.DetectContentType(data)
	if _, ok := docTypes[ct]; !ok {
		return ClientDoc{}, invalid("only PDF, JPEG and PNG files are accepted (this looks like %s)", ct)
	}
	sum := sha256.Sum256(data)
	rf, err := o.App.Ingest.Raw.Put(ctx, ingest.RawFile{Tenant: tenant, Source: "CLIENT-DOC", Name: name, SHA256: hex.EncodeToString(sum[:]), Content: data})
	if err != nil {
		return ClientDoc{}, err
	}
	n, err := o.next(ctx, tenant, "clientdoc", actor)
	if err != nil {
		return ClientDoc{}, err
	}
	d := ClientDoc{ID: fmt.Sprintf("DOC-%06d", n), UCC: ucc, Kind: kind, Name: name, ContentType: ct, Size: int64(len(data)), SHA256: rf.SHA256, FileID: rf.ID, By: actor, At: o.now().Format(time.RFC3339)}
	if _, err := docs.Put(ctx, o.Docs, tenant, cClientDoc, ucc+"|"+d.ID, d, 0, actor); err != nil {
		return d, err
	}
	o.log(ctx, tenant, actor, "client.document.added", ucc, map[string]any{"doc": d.ID, "kind": kind, "sha256": d.SHA256})
	return d, nil
}

// ClientDocs lists a client's documents.
func (o *Ops) ClientDocs(ctx context.Context, tenant, ucc string) ([]ClientDoc, error) {
	return docs.List[ClientDoc](ctx, o.Docs, tenant, cClientDoc, strings.ToUpper(ucc)+"|")
}

// ClientDocFile returns a document's bytes.
func (o *Ops) ClientDocFile(ctx context.Context, tenant, actor, ucc, id string) (ClientDoc, []byte, error) {
	d, _, err := docs.Get[ClientDoc](ctx, o.Docs, tenant, cClientDoc, strings.ToUpper(ucc)+"|"+id)
	if err != nil {
		return d, nil, err
	}
	rf, err := o.App.Ingest.Raw.Get(ctx, tenant, d.FileID)
	if err != nil {
		return d, nil, err
	}
	o.log(ctx, tenant, actor, "client.document.viewed", d.UCC, map[string]any{"doc": d.ID})
	return d, rf.Content, nil
}
