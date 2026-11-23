package ops

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/sheet"
	"ledgerline/internal/xfile"
)

// ---- clients the KRAs report as deceased ----
//
// The rules:
//   - SEBI/HO/OIAE/OIAE_IAD-1/P/CIR/2023/0000000163 (3 Oct 2023, in force
//     1 Jan 2024): an investor's death is reported once, through the KRA.
//     The intermediary verifies the death certificate by the next working
//     day (para 3) and updates the KRA; on the KRA's "Blocked Permanently"
//     it blocks all debit transactions at once (para 8(a)); within 5 days
//     it gives the nominee, legal heirs or surviving holders the
//     transmission procedure, form and list of documents (para 8(b)); an
//     intimation proved false is reported to the KRA the same day (para 10).
//   - NSE/ISC/75724 (13 Aug 2026): for deceased investors, block debits,
//     suspend all transactions and inactivate or close the UCC on all
//     exchanges; the KRAs share demise data daily.
//   - NSE/ISC/76893 (14 Oct 2026) and NSE/ISC/77375 (9 Nov 2026): each
//     month NSE posts <TMcode>_Demise_Clients_by_KRA on ENIT (Log >
//     Downloads) with a cut-off in that month's circular: the PANs reported
//     up to the end of the previous month, whose UCCs are marked "Closed"
//     after due diligence before the cut-off (31 Oct 2026, then 21 Nov
//     2026). From the cut-off the PANs are Not Permitted to Trade and
//     cannot even square off.
//
// What Ledgerline does with a match (by PAN across every client, and by
// the client code when the file has one):
//   - debits are blocked at once: the client is Blocked, with
//     Client.Deceased, which also holds payouts (payments.go), running
//     account settlement (ras.go), debits from the client's demat accounts
//     (dp.go freezeFor: slips, eDIS, pledges, early and settlement pay-in)
//     and mutual fund orders (mf.go), and keeps the trading front ends'
//     limits at zero (finspot.go, bowapi.go), until transmission;
//   - the steps are tracked: reported by the KRA; death verified, or the
//     intimation false (the KRA told, the block lifted); the UCC closed on
//     each exchange; the transmission information sent (within 5 days of
//     the KRA notice); with the days left to the month's cut-off and work
//     items for the compliance officer;
//   - closing the UCCs marks every segment Closed, so the exchanges' UCC
//     paths send the change: NSE's UCI API as client status C, BSE's UCC
//     as Is Active C, MCX's UCC as status 2 (inactive) with the remark, as
//     MCX's layout has no closed status. A firm that does not use the APIs
//     downloads the list for each exchange's portal (NSE: UCI Online >
//     Client Detail > Update Client Status). The layout of NSE's bulk
//     "Upload Client Status" file (attached to NSE/ISC/73973) could not be
//     read, so that file is not written.

const (
	cDemiseList = "demiselist" // key: DL-nnnnnn
	cDemiseCase = "demisecase" // key: DC-nnnnnn

	DemiseListNSE = "nse-kra-demise"
	// DemiseListKRA is the KRAs' daily demise data (NSE/ISC/75724: the
	// KRAs share it daily; MCX/S&I/522/2026 para 2), read like NSE's
	// monthly list: debits blocked at once, the UCCs closed on every
	// exchange, against the cycle of the month it is reported in.
	DemiseListKRA = "kra-demise-daily"

	DemiseReported = "Reported by KRA"
	DemiseVerified = "Death verified"
	DemiseFalse    = "Intimation false"
	DemiseClosed   = "UCCs closed"
	DemiseDone     = "Done"

	// UCC closure at one exchange.
	DemiseToClose = "To close"
	DemiseSending = "In the UCC upload"
	DemiseUCCDone = "Closed"

	// demiseTransmissionDays: SEBI's days to give the transmission
	// information from the KRA notice (para 8(b)).
	demiseTransmissionDays = 5
	// demiseCutoffDay: the cut-off's usual day in the month after the
	// period when the person loading gives none (NSE/ISC/77375 set the
	// 21st); a known cycle's own date is used when there is one.
	demiseCutoffDay = 21

	wDeceased = "deceased"

	// deceasedReason begins a deceased client's BlockedReason.
	deceasedReason = "Deceased (reported by the KRA)"
)

// DemiseStages are the cases' stages, in order.
var DemiseStages = []string{DemiseReported, DemiseVerified, DemiseClosed, DemiseDone, DemiseFalse}

// demiseCycles are the cycles whose circular Ledgerline knows: the period
// (PANs reported up to the end of that month), the cut-off and the
// circular.
var demiseCycles = map[string][2]string{
	"2026-09": {"2026-10-31", "NSE/ISC/76893 of 14 Oct 2026"},
	"2026-10": {"2026-11-21", "NSE/ISC/77375 of 9 Nov 2026"},
}

// demiseCycle is a period's cut-off and circular: the known one, else the
// usual day of the following month.
func demiseCycle(period string) (cutoff, circular string) {
	if c, ok := demiseCycles[period]; ok {
		return c[0], c[1]
	}
	return addMonths(period+"-01", 1)[:8] + fmt.Sprintf("%02d", demiseCutoffDay), ""
}

// DeceasedFlag is on a client the KRA reported as deceased: debits are
// blocked until transmission (deceased.go).
type DeceasedFlag struct {
	Case       string `json:"case"`
	ReportedOn string `json:"reported_on"`
	Verified   string `json:"verified,omitempty"`
}

// deceasedWhy says why a deceased client's money and securities are held.
func deceasedWhy(c Client) string {
	return fmt.Sprintf("%s is reported deceased by the KRA (%s): debits are blocked until transmission", c.UCC, c.Deceased.Case)
}

// DemiseList is one KRA demise file as loaded.
type DemiseList struct {
	ID         string   `json:"id"`
	File       string   `json:"file"`
	FileID     string   `json:"file_id,omitempty"`
	Date       string   `json:"date"`   // the day it was loaded for
	Period     string   `json:"period"` // YYYY-MM: PANs reported up to the end of this month
	Cutoff     string   `json:"cutoff"` // close the UCCs before this day
	Circular   string   `json:"circular,omitempty"`
	By         string   `json:"by"`
	At         string   `json:"at"`
	Rows       int      `json:"rows"`
	Matched    int      `json:"matched"`     // new cases
	Again      int      `json:"again"`       // cases already open
	NotClients int      `json:"not_clients"` // PANs of no client
	Issues     int      `json:"issues"`      // rows that did not fit the layout
	Notes      []string `json:"notes,omitempty"`
}

func (l DemiseList) summary() string {
	s := fmt.Sprintf("%d row%s: %d client%s matched", l.Rows, plural(l.Rows), l.Matched, plural(l.Matched))
	if l.Again > 0 {
		s += fmt.Sprintf(", %d already open", l.Again)
	}
	if l.NotClients > 0 {
		s += fmt.Sprintf(", %d PAN%s of no client", l.NotClients, plural(l.NotClients))
	}
	if l.Issues > 0 {
		s += fmt.Sprintf(", %d row%s left out as they do not fit the layout", l.Issues, plural(l.Issues))
	}
	s += "; close the UCCs before " + fmtDay(l.Cutoff)
	if len(l.Notes) > 0 {
		s += "; " + strings.Join(l.Notes, "; ")
	}
	return s
}

// DemiseStep is a step done: when, by whom, how.
type DemiseStep struct {
	On   string `json:"on"` // YYYY-MM-DD
	By   string `json:"by"`
	At   string `json:"at"`
	Note string `json:"note,omitempty"`
	Ref  string `json:"ref,omitempty"`
}

// DemiseUCC is the UCC closure at one exchange.
type DemiseUCC struct {
	Exchange string `json:"exchange"`
	Status   string `json:"status"` // To close, In the UCC upload, Closed
	Via      string `json:"via,omitempty"`
	On       string `json:"on,omitempty"`
	By       string `json:"by,omitempty"`
	Ref      string `json:"ref,omitempty"`
}

// DemiseCase is a client the KRA reported as deceased.
type DemiseCase struct {
	ID              string      `json:"id"`
	UCC             string      `json:"ucc"`
	Name            string      `json:"name"`
	PAN             string      `json:"pan"`
	MatchedBy       string      `json:"matched_by"` // PAN, or PAN and client code
	List            string      `json:"list"`
	File            string      `json:"file,omitempty"`
	Period          string      `json:"period"`
	Cutoff          string      `json:"cutoff"`
	Circular        string      `json:"circular,omitempty"`
	ReportedOn      string      `json:"reported_on"` // the KRA notice
	KRA             string      `json:"kra,omitempty"`
	KRAStatus       string      `json:"kra_status,omitempty"`
	Status          string      `json:"status"`
	VerifyDue       string      `json:"verify_due"`
	TransmissionDue string      `json:"transmission_due"`
	Verified        *DemiseStep `json:"verified,omitempty"`
	False           *DemiseStep `json:"false,omitempty"`
	Transmission    *DemiseStep `json:"transmission,omitempty"`
	UCCs            []DemiseUCC `json:"uccs"`
	ClosedAt        string      `json:"closed_at,omitempty"` // when Ledgerline closed the segments
	Blocked         string      `json:"blocked"`             // when debits were blocked
	// WasBlocked and WasReason are the client's block before, put back if
	// the intimation is false.
	WasBlocked bool        `json:"was_blocked,omitempty"`
	WasReason  string      `json:"was_reason,omitempty"`
	Seen       []string    `json:"seen,omitempty"` // later lists that had it again
	History    []MNRLEvent `json:"history"`
}

func (d DemiseCase) uccsClosed() bool {
	for _, u := range d.UCCs {
		if u.Status != DemiseUCCDone {
			return false
		}
	}
	return true
}

// stage is where the case is.
func (d DemiseCase) stage() string {
	switch {
	case d.False != nil:
		return DemiseFalse
	case d.Verified == nil:
		return DemiseReported
	case !d.uccsClosed():
		return DemiseVerified
	case d.Transmission == nil:
		return DemiseClosed
	}
	return DemiseDone
}

// demiseOpen: the case is not done and not found false.
func demiseOpen(d DemiseCase) bool { s := d.stage(); return s != DemiseDone && s != DemiseFalse }

// parseListDate reads a date as lists give it: 2026-10-31, 31-10-2026,
// 31/10/2026, 31-Oct-2026, 31102026, 20261031, or an Excel serial number.
func parseListDate(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if len(s) > 10 && (s[10] == ' ' || s[10] == 'T') {
		s = s[:10]
	}
	for _, l := range []string{"2006-01-02", "02-01-2006", "02/01/2006", "02-Jan-2006", "02 Jan 2006", "02-Jan-06", "02.01.2006", "2006/01/02"} {
		if t, err := time.Parse(l, s); err == nil {
			return t.Format("2006-01-02"), true
		}
	}
	if len(s) == 8 && allDigits(s) {
		for _, l := range []string{"20060102", "02012006"} {
			if t, err := time.Parse(l, s); err == nil && t.Year() >= 2000 && t.Year() < 2100 {
				return t.Format("2006-01-02"), true
			}
		}
	}
	if n, err := strconv.Atoi(s); err == nil && n > 36500 && n < 73000 {
		if d, ok := sheet.Date(s); ok {
			return d, true
		}
	}
	return "", false
}

// demiseExchanges are the exchanges a client's UCC is on: those with a
// registration in Ledgerline, and those of the firm's where the client has
// a segment.
func demiseExchanges(set Settings, c Client, regs []listUCCReg) []string {
	if c.DPOnly {
		return nil
	}
	on := map[string]bool{}
	for _, r := range regs {
		if r.Exchange != "" {
			on[r.Exchange] = true
		}
	}
	for _, s := range set.activeSegments() {
		if contains(c.Segments, s.Segment) {
			on[s.Exchange] = true
		}
	}
	order := map[string]int{"NSE": 0, "BSE": 1, "MCX": 2, "MSE": 3, "NCDEX": 4}
	out := sortedKeys(on)
	sort.SliceStable(out, func(i, j int) bool { return order[out[i]] < order[out[j]] })
	return out
}

// demiseBlock blocks a client's debits for a case (or, with off, puts the
// client's block back as it was).
func (o *Ops) demiseBlock(ctx context.Context, tenant, actor string, d *DemiseCase, off bool) error {
	_, err := docs.Update(ctx, o.Docs, tenant, cClient, d.UCC, actor, func(c *Client) error {
		if off {
			if c.Deceased != nil && c.Deceased.Case == d.ID {
				c.Deceased = nil
				c.Blocked, c.BlockedReason = d.WasBlocked, d.WasReason
			}
			return nil
		}
		if c.Deceased == nil {
			d.WasBlocked, d.WasReason = c.Blocked, c.BlockedReason
		}
		c.Deceased = &DeceasedFlag{Case: d.ID, ReportedOn: d.ReportedOn}
		c.Blocked, c.BlockedReason = true, deceasedReason+": "+d.ID+"; debits blocked until transmission"
		return nil
	})
	return err
}

// demiseWork queues (or refreshes) a case's work items; a step done closes
// its item.
func (o *Ops) demiseWork(ctx context.Context, tenant, actor string, d DemiseCase) {
	if !demiseOpen(d) {
		for _, k := range []string{":verify", ":ucc", ":transmission"} {
			o.closeWork(ctx, tenant, actor, wDeceased, d.ID+k, d.stage())
		}
		return
	}
	who := d.UCC + " (" + MaskPAN(d.PAN) + ")"
	if d.Verified == nil {
		o.workDue(ctx, tenant, wDeceased, d.ID+":verify", "Verify the death of "+who+" reported by the KRA",
			fmt.Sprintf("The KRA reported %s as deceased (%s list, %s). Debits are blocked. Get the death certificate and verify it (online, original seen, or an exchange's or depository's validation report) by %s, or, if the intimation is false, tell the KRA the same day; record it on Compliance → Deceased clients.", who, fmtDay(d.ReportedOn), d.File, fmtDay(d.VerifyDue)),
			d.UCC, RoleCompliance, "deceased", d.ReportedOn, d.VerifyDue+" 17:00")
	} else {
		o.closeWork(ctx, tenant, actor, wDeceased, d.ID+":verify", "verified")
	}
	if !d.uccsClosed() {
		last := addDays(d.Cutoff, -1)
		o.workDue(ctx, tenant, wDeceased, d.ID+":ucc", "Close the UCCs of "+who+" before "+fmtDay(d.Cutoff),
			fmt.Sprintf("Mark %s's UCC Closed on every exchange after the due diligence, before %s (%s); from then the PAN is not permitted to trade and cannot square off. Close them on Compliance → Deceased clients: the exchanges' UCC paths send the change.", d.UCC, fmtDay(d.Cutoff), firstOf(d.Circular, "the month's NSE circular")),
			d.UCC, RoleCompliance, "deceased", d.ReportedOn, last+" 17:00")
	} else {
		o.closeWork(ctx, tenant, actor, wDeceased, d.ID+":ucc", "closed")
	}
	if d.Transmission == nil {
		o.workDue(ctx, tenant, wDeceased, d.ID+":transmission", "Send the transmission procedure for "+who,
			fmt.Sprintf("Give the nominee, legal heirs or surviving holders of %s the transmission procedure, the request form and the list of documents by %s (5 days from the KRA notice), and record it on Compliance → Deceased clients.", d.UCC, fmtDay(d.TransmissionDue)),
			d.UCC, RoleCompliance, "deceased", d.ReportedOn, d.TransmissionDue+" 17:00")
	} else {
		o.closeWork(ctx, tenant, actor, wDeceased, d.ID+":transmission", "sent")
	}
}

// ---- loading the list ----

// DemiseLoadReq is the KRAs' demise list as loaded on the screen, with the
// month's cut-off from NSE's circular.
type DemiseLoadReq struct {
	// Format: nse-kra-demise (NSE's monthly list, the default) or
	// kra-demise-daily (the KRA's daily demise data).
	Format   string `json:"format"`
	Name     string `json:"name"`
	Date     string `json:"date"`     // the day it is loaded for (empty: today)
	Period   string `json:"period"`   // YYYY-MM: PANs reported up to the end of it (empty: the month before Date)
	Cutoff   string `json:"cutoff"`   // close the UCCs before it (empty: the cycle's)
	Circular string `json:"circular"` // the month's circular
}

// LoadDemiseList reads <TMcode>_Demise_Clients_by_KRA and opens a case for
// each client on it.
func (o *Ops) LoadDemiseList(ctx context.Context, tenant, actor string, req DemiseLoadReq, data []byte) (DemiseList, error) {
	if err := o.require(ctx, tenant, actor, "Loading the KRAs' demise list", RoleCompliance, RoleChecker, RoleMaker); err != nil {
		return DemiseList{}, err
	}
	format := firstOf(req.Format, DemiseListNSE)
	if format != DemiseListNSE && format != DemiseListKRA {
		return DemiseList{}, invalid("the list is %s (NSE's monthly list) or %s (the KRA's daily data)", DemiseListNSE, DemiseListKRA)
	}
	lf, err := o.openListFile(ctx, tenant, format, req.Name, req.Date, data)
	if err != nil {
		return DemiseList{}, err
	}
	period := req.Period
	if format == DemiseListKRA && period == "" {
		period = lf.date[:7] // reported this month: the next NSE cycle's
	}
	l, err := o.applyDemiseFile(ctx, tenant, actor, lf, period, req.Cutoff, req.Circular)
	if err != nil {
		return l, err
	}
	o.log(ctx, tenant, actor, "xfile.received", lf.name, map[string]any{"format": format, "rows": l.Rows, "problems": l.Issues, "file": lf.fileID, "loaded": "Deceased clients"})
	o.noteDailyFile(ctx, tenant, actor, format, lf.name, "")
	return l, nil
}

// applyDemiseFile matches the list's PANs (and client codes) with every
// client, blocks the debits of each new match and records the list.
func (o *Ops) applyDemiseFile(ctx context.Context, tenant, actor string, lf *listFile, period, cutoff string, circular ...string) (DemiseList, error) {
	if period == "" {
		period = prevMonth(lf.date)
	}
	if _, err := time.Parse("2006-01", period); err != nil {
		return DemiseList{}, invalid("the period is the month the PANs were reported up to (YYYY-MM)")
	}
	defCut, defCirc := demiseCycle(period)
	if cutoff == "" {
		cutoff = defCut
	}
	if !validDate(cutoff) || cutoff <= monthEnd(period) {
		return DemiseList{}, invalid("the cut-off is the date in the month's NSE circular before which the UCCs are closed, after the end of %s", monthLabel(period))
	}
	circ := defCirc
	if len(circular) > 0 && strings.TrimSpace(circular[0]) != "" {
		circ = strings.TrimSpace(circular[0])
	}
	n, err := o.next(ctx, tenant, "demiselist", actor)
	if err != nil {
		return DemiseList{}, err
	}
	now := o.now()
	l := DemiseList{ID: fmt.Sprintf("DL-%06d", n), File: lf.name, FileID: lf.fileID, Date: lf.date, Period: period, Cutoff: cutoff, Circular: circ, By: actor, At: now.Format(time.RFC3339)}
	cs, err := o.ClientMap(ctx, tenant)
	if err != nil {
		return l, err
	}
	byPAN := map[string][]string{}
	for _, u := range sortedKeys(cs) {
		if c := cs[u]; c.PAN != "" && c.Status != "Rejected" {
			byPAN[strings.ToUpper(c.PAN)] = append(byPAN[strings.ToUpper(c.PAN)], u)
		}
	}
	all, _ := docs.List[DemiseCase](ctx, o.Docs, tenant, cDemiseCase, "")
	open := map[string]*DemiseCase{}
	for i := range all {
		if demiseOpen(all[i]) {
			open[all[i].UCC+"|"+all[i].PAN] = &all[i]
		}
	}
	type hit struct {
		ucc, pan, by, kra, status, date string
	}
	var hits []hit
	notes := map[string]int{}
	err = lf.feed(func(r xfile.Row) {
		l.Rows++
		pan := strings.ToUpper(strings.TrimSpace(r.Get("pan")))
		ucc := strings.ToUpper(strings.TrimSpace(r.Get("ucc")))
		if !rePAN.MatchString(pan) {
			notes["no PAN in the PAN form"]++
			return
		}
		us := byPAN[pan]
		if ucc != "" {
			if c, ok := cs[ucc]; ok && !strings.EqualFold(c.PAN, pan) {
				notes["a client code whose PAN in Ledgerline is another (check the client; nothing was blocked)"]++
			}
		}
		if len(us) == 0 {
			l.NotClients++
			return
		}
		for _, u := range us {
			by := "PAN"
			if u == ucc {
				by = "PAN and client code"
			}
			hits = append(hits, hit{u, pan, by, r.Get("kra"), r.Get("status"), r.Get("date")})
		}
	})
	if err != nil {
		return l, err
	}
	set, _ := o.Settings(ctx, tenant)
	regs := o.listUCCRegs(ctx, tenant)
	seen := map[string]bool{}
	for _, h := range hits {
		k := h.ucc + "|" + h.pan
		if seen[k] {
			continue
		}
		seen[k] = true
		if d := open[k]; d != nil {
			l.Again++
			if len(d.Seen) < 30 {
				d.Seen = append(d.Seen, l.ID)
			}
			_ = docs.Upsert(ctx, o.Docs, tenant, cDemiseCase, d.ID, *d, actor)
			continue
		}
		c := cs[h.ucc]
		rep, ok := parseListDate(h.date)
		if !ok || rep > lf.date {
			rep = lf.date
		}
		n, err := o.next(ctx, tenant, "demisecase", actor)
		if err != nil {
			return l, err
		}
		d := DemiseCase{ID: fmt.Sprintf("DC-%06d", n), UCC: h.ucc, Name: c.Name, PAN: h.pan, MatchedBy: h.by, List: l.ID, File: l.File, Period: period, Cutoff: cutoff, Circular: circ,
			ReportedOn: rep, KRA: strings.TrimSpace(h.kra), KRAStatus: strings.TrimSpace(h.status), Status: DemiseReported, VerifyDue: set.NextBusinessDay(lf.date),
			TransmissionDue: addDays(rep, demiseTransmissionDays), Blocked: now.Format(time.RFC3339), UCCs: []DemiseUCC{}}
		for _, ex := range demiseExchanges(set, c, regs[h.ucc]) {
			d.UCCs = append(d.UCCs, DemiseUCC{Exchange: ex, Status: DemiseToClose})
		}
		d.History = []MNRLEvent{{At: now.Format(time.RFC3339), By: actor, Status: DemiseReported, Note: fmt.Sprintf("On the KRAs' demise list %s (matched by %s); debits blocked", l.File, h.by)}}
		if err := o.demiseBlock(ctx, tenant, actor, &d, false); err != nil {
			return l, err
		}
		if _, err := docs.Put(ctx, o.Docs, tenant, cDemiseCase, d.ID, d, 0, actor); err != nil {
			return l, err
		}
		o.demiseWork(ctx, tenant, actor, d)
		o.log(ctx, tenant, actor, "deceased.reported", d.ID, map[string]any{"ucc": d.UCC, "pan": MaskPAN(d.PAN), "list": l.ID, "cutoff": cutoff})
		l.Matched++
	}
	for _, k := range sortedKeys(notes) {
		l.Notes = append(l.Notes, fmt.Sprintf("%d row%s with %s", notes[k], plural(notes[k]), k))
	}
	l.Issues = lf.issues
	if _, err := docs.Put(ctx, o.Docs, tenant, cDemiseList, l.ID, l, 0, actor); err != nil {
		return l, err
	}
	o.log(ctx, tenant, actor, "deceased.list.loaded", l.ID, map[string]any{"file": l.File, "period": period, "cutoff": cutoff, "rows": l.Rows, "matched": l.Matched, "again": l.Again, "not_clients": l.NotClients})
	if lf.spec == nil || lf.spec.ID != DemiseListKRA {
		o.closeWork(ctx, tenant, actor, "periodic", "kra_demise:"+period, "loaded "+l.ID) // asked for by Periodic work
	}
	return l, nil
}

// ---- the steps ----

// DemiseActionReq is a step recorded on a case.
type DemiseActionReq struct {
	// Action: verify, false, close (Ledgerline closes the segments),
	// closed (closed on an exchange's portal), transmission.
	Action   string `json:"action"`
	On       string `json:"on"` // when it was done (empty: today)
	Note     string `json:"note"`
	Ref      string `json:"ref"`
	Exchange string `json:"exchange"` // for closed
}

// DemiseAction records a step on a case.
func (o *Ops) DemiseAction(ctx context.Context, tenant, actor, id string, a DemiseActionReq) (DemiseCase, error) {
	d, _, err := docs.Get[DemiseCase](ctx, o.Docs, tenant, cDemiseCase, id)
	if err != nil {
		return d, err
	}
	switch a.Action {
	case "verify", "false":
		err = o.require(ctx, tenant, actor, "Deciding a reported death", RoleCompliance)
	case "close":
		err = o.require(ctx, tenant, actor, "Closing a deceased client's UCCs", RoleCompliance, RoleChecker)
	case "closed", "transmission":
		err = o.require(ctx, tenant, actor, "Recording a deceased client's steps", RoleCompliance, RoleChecker, RoleMaker)
	default:
		return d, invalid("unknown step %q", a.Action)
	}
	if err != nil {
		return d, err
	}
	if !demiseOpen(d) {
		return d, fmt.Errorf("%w: %s is %s", ErrState, id, strings.ToLower(d.stage()))
	}
	now := o.now()
	on := firstOf(strings.TrimSpace(a.On), now.Format("2006-01-02"))
	if !validDate(on) || on > now.Format("2006-01-02") {
		return d, invalid("give the date it was done (not in the future)")
	}
	note, ref := strings.TrimSpace(a.Note), strings.TrimSpace(a.Ref)
	step := &DemiseStep{On: on, By: actor, At: now.Format(time.RFC3339), Note: note, Ref: ref}
	what := ""
	switch a.Action {
	case "verify":
		if d.Verified != nil {
			return d, fmt.Errorf("%w: the death was verified on %s", ErrState, fmtDay(d.Verified.On))
		}
		if len(note) < 4 {
			return d, invalid("say how the death certificate was verified (online, original seen, or the validation report)")
		}
		d.Verified, what = step, DemiseVerified
		_, _ = docs.Update(ctx, o.Docs, tenant, cClient, d.UCC, actor, func(c *Client) error {
			if c.Deceased != nil && c.Deceased.Case == d.ID {
				c.Deceased.Verified = on
			}
			return nil
		})
	case "false":
		if len(note) < 4 {
			return d, invalid("give the due diligence that shows the investor is alive (for example a video call), and the KRA modification sent")
		}
		if d.Verified != nil {
			return d, fmt.Errorf("%w: the death was verified on %s", ErrState, fmtDay(d.Verified.On))
		}
		if d.ClosedAt != "" {
			return d, fmt.Errorf("%w: the UCCs were closed on %s; a closed UCC cannot be reopened at NSE", ErrState, fmtDay(d.ClosedAt[:10]))
		}
		d.False, what = step, DemiseFalse
		if err := o.demiseBlock(ctx, tenant, actor, &d, true); err != nil {
			return d, err
		}
	case "close":
		if d.Verified == nil {
			return d, fmt.Errorf("%w: verify the death first: the UCC is closed after the due diligence", ErrState)
		}
		if d.ClosedAt != "" {
			return d, fmt.Errorf("%w: the segments were closed on %s", ErrState, fmtDay(d.ClosedAt[:10]))
		}
		if err := o.demiseCloseSegments(ctx, tenant, actor, &d, on); err != nil {
			return d, err
		}
		what = "Segments closed"
	case "closed":
		if d.Verified == nil {
			return d, fmt.Errorf("%w: verify the death first: the UCC is closed after the due diligence", ErrState)
		}
		ex := strings.ToUpper(strings.TrimSpace(a.Exchange))
		k := -1
		for i, u := range d.UCCs {
			if u.Exchange == ex {
				k = i
			}
		}
		if k < 0 {
			return d, invalid("%s is not one of the client's exchanges (%s)", firstOf(ex, "the exchange"), d.exchangeNames())
		}
		if d.UCCs[k].Status == DemiseUCCDone {
			return d, fmt.Errorf("%w: %s is already recorded closed", ErrState, ex)
		}
		if ref == "" && len(note) < 4 {
			return d, invalid("give the exchange's reference or a note of how it was closed")
		}
		d.UCCs[k] = DemiseUCC{Exchange: ex, Status: DemiseUCCDone, Via: firstOf(note, "Exchange's portal"), On: on, By: actor, Ref: ref}
		what = "UCC closed at " + ex
	case "transmission":
		if d.Transmission != nil {
			return d, fmt.Errorf("%w: the transmission information was sent on %s", ErrState, fmtDay(d.Transmission.On))
		}
		if len(note) < 4 {
			return d, invalid("say to whom and how the transmission procedure, form and documents list were sent")
		}
		d.Transmission, what = step, "Transmission information sent"
	}
	d.Status = d.stage()
	d.History = append(d.History, MNRLEvent{At: now.Format(time.RFC3339), By: actor, Status: what, Note: strings.TrimSpace(note + map[bool]string{true: " · " + ref}[ref != ""])})
	if err := docs.Upsert(ctx, o.Docs, tenant, cDemiseCase, d.ID, d, actor); err != nil {
		return d, err
	}
	o.demiseWork(ctx, tenant, actor, d)
	o.log(ctx, tenant, actor, "deceased."+a.Action, d.ID, map[string]any{"ucc": d.UCC, "on": on, "note": note, "ref": ref, "exchange": a.Exchange, "stage": d.Status})
	return d, nil
}

func (d DemiseCase) exchangeNames() string {
	var out []string
	for _, u := range d.UCCs {
		out = append(out, u.Exchange)
	}
	return firstOf(strings.Join(out, ", "), "none")
}

// demiseCloseSegments marks every open segment of the client Closed (no
// closure letter: the client is deceased), so the exchanges' UCC paths
// send it, and records each exchange as in the UCC upload.
func (o *Ops) demiseCloseSegments(ctx context.Context, tenant, actor string, d *DemiseCase, on string) error {
	reason := fmt.Sprintf("Deceased, reported by the KRA (%s); closed after due diligence", d.ID)
	_, err := docs.Update(ctx, o.Docs, tenant, cClient, d.UCC, actor, func(c *Client) error {
		for _, s := range append([]string(nil), c.Segments...) {
			if st := c.SegmentStatus(s); st == SegClosed || st == "Not registered" {
				continue
			}
			if err := applySegmentStatus(c, s, SegClosed, on, reason, actor, "Ledgerline", o.now()); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	d.ClosedAt = o.now().Format(time.RFC3339)
	for i, u := range d.UCCs {
		if u.Status == DemiseToClose {
			via := "Exchange UCC: sent with the next UCC upload (API or file)"
			if u.Exchange == "MCX" {
				via = "Exchange UCC: MCX takes it as status 2 (inactive) with the remark; its layout has no closed status"
			}
			d.UCCs[i] = DemiseUCC{Exchange: u.Exchange, Status: DemiseSending, Via: via, On: on, By: actor}
		}
	}
	o.log(ctx, tenant, actor, "client.segment_status", d.UCC, map[string]any{"to": SegClosed, "on": on, "reason": reason})
	return nil
}

// demiseRefresh marks an exchange closed once its UCC system accepted the
// closed record (the registration was sent after the segments closed).
func demiseRefresh(d *DemiseCase, regs []listUCCReg) bool {
	changed := false
	for i, u := range d.UCCs {
		if u.Status != DemiseSending || d.ClosedAt == "" {
			continue
		}
		for _, r := range regs {
			if r.Exchange == u.Exchange && r.Status == "Accepted" && r.SentAt != "" && after(r.SentAt, d.ClosedAt) {
				d.UCCs[i].Status, d.UCCs[i].By, d.UCCs[i].Ref = DemiseUCCDone, SystemActor, "accepted by "+u.Exchange+"'s UCC system"
				d.UCCs[i].On = fmtTSDate(firstOf(r.ResultAt, r.SentAt))
				changed = true
			}
		}
	}
	return changed
}

// fmtTSDate is the YYYY-MM-DD of an RFC 3339 time.
func fmtTSDate(t string) string {
	if len(t) >= 10 {
		return t[:10]
	}
	return t
}

// SetDemiseCutoff changes a period's cut-off (from the month's circular)
// for its open cases.
func (o *Ops) SetDemiseCutoff(ctx context.Context, tenant, actor, period, cutoff, circular string) (int, error) {
	if err := o.require(ctx, tenant, actor, "Changing the demise list's cut-off", RoleCompliance, RoleChecker); err != nil {
		return 0, err
	}
	if _, err := time.Parse("2006-01", period); err != nil || !validDate(cutoff) || cutoff <= monthEnd(period) {
		return 0, invalid("give the period (YYYY-MM) and the circular's cut-off, after the end of the period")
	}
	ds, err := docs.List[DemiseCase](ctx, o.Docs, tenant, cDemiseCase, "")
	if err != nil {
		return 0, err
	}
	n := 0
	for _, d := range ds {
		if d.Period != period || !demiseOpen(d) {
			continue
		}
		d.Cutoff = cutoff
		if strings.TrimSpace(circular) != "" {
			d.Circular = strings.TrimSpace(circular)
		}
		d.History = append(d.History, MNRLEvent{At: o.now().Format(time.RFC3339), By: actor, Status: "Cut-off " + fmtDay(cutoff), Note: d.Circular})
		if err := docs.Upsert(ctx, o.Docs, tenant, cDemiseCase, d.ID, d, actor); err != nil {
			return n, err
		}
		o.demiseWork(ctx, tenant, actor, d)
		n++
	}
	ls, _ := docs.List[DemiseList](ctx, o.Docs, tenant, cDemiseList, "")
	for _, l := range ls {
		if l.Period == period {
			l.Cutoff = cutoff
			if strings.TrimSpace(circular) != "" {
				l.Circular = strings.TrimSpace(circular)
			}
			_ = docs.Upsert(ctx, o.Docs, tenant, cDemiseList, l.ID, l, actor)
		}
	}
	o.log(ctx, tenant, actor, "deceased.cutoff", period, map[string]any{"cutoff": cutoff, "circular": circular, "cases": n})
	return n, nil
}

// ---- the screen ----

// DemiseRow is a case on the screen.
type DemiseRow struct {
	DemiseCase
	Stage     string   `json:"stage"`
	DaysLeft  int      `json:"days_left"` // to the cut-off
	Overdue   []string `json:"overdue"`   // the steps past their date
	Positions int      `json:"positions"` // open derivatives positions
	Held      []string `json:"held"`      // what the block stops
}

// DemiseView is Compliance → Deceased clients.
type DemiseView struct {
	Date    string         `json:"date"`
	Counts  map[string]int `json:"counts"`
	Open    int            `json:"open"`
	Overdue int            `json:"overdue"`
	Cases   []DemiseRow    `json:"cases"`
	Lists   []DemiseList   `json:"lists"`
	// Period is the current cycle (PANs reported to the end of last
	// month), with its cut-off and whether its list is loaded.
	Period   string   `json:"period"`
	Cutoff   string   `json:"cutoff"`
	Circular string   `json:"circular,omitempty"`
	Loaded   bool     `json:"loaded"`
	Stages   []string `json:"stages"`
	CanAct   bool     `json:"can_act"`
	CanClose bool     `json:"can_close"`
	CanLoad  bool     `json:"can_load"`
	CanStep  bool     `json:"can_step"` // record a closure on a portal, the transmission
}

// Demise builds the screen for a date.
func (o *Ops) Demise(ctx context.Context, tenant, actor, date string) (DemiseView, error) {
	v := DemiseView{Date: date, Counts: map[string]int{}, Cases: []DemiseRow{}, Lists: []DemiseList{}, Stages: DemiseStages}
	if err := o.require(ctx, tenant, actor, "Viewing deceased clients", RoleCompliance, RoleChecker, RoleMaker, RoleRisk); err != nil {
		return v, err
	}
	if !validDate(date) {
		return v, invalid("give a date (YYYY-MM-DD)")
	}
	v.CanAct = o.require(ctx, tenant, actor, "Deciding a reported death", RoleCompliance) == nil
	v.CanClose = o.require(ctx, tenant, actor, "Closing a deceased client's UCCs", RoleCompliance, RoleChecker) == nil
	v.CanLoad = o.require(ctx, tenant, actor, "Loading the KRAs' demise list", RoleCompliance, RoleChecker, RoleMaker) == nil
	v.CanStep = o.require(ctx, tenant, actor, "Recording a deceased client's steps", RoleCompliance, RoleChecker, RoleMaker) == nil
	ds, err := docs.List[DemiseCase](ctx, o.Docs, tenant, cDemiseCase, "")
	if err != nil {
		return v, err
	}
	for _, s := range DemiseStages {
		v.Counts[s] = 0
	}
	regs := o.listUCCRegs(ctx, tenant)
	pos := map[string]int{}
	if len(ds) > 0 {
		if rv, err := o.Risk(ctx, tenant, date); err == nil {
			for _, r := range rv.Rows {
				pos[r.UCC] = r.Positions
			}
		}
	}
	for _, d := range ds {
		if demiseRefresh(&d, regs[d.UCC]) {
			d.Status = d.stage()
			d.History = append(d.History, MNRLEvent{At: o.now().Format(time.RFC3339), By: SystemActor, Status: "UCC closure accepted", Note: "the exchange's UCC system accepted the closed record"})
			_ = docs.Upsert(ctx, o.Docs, tenant, cDemiseCase, d.ID, d, SystemActor)
			o.demiseWork(ctx, tenant, SystemActor, d)
		}
		r := DemiseRow{DemiseCase: d, Stage: d.stage(), Overdue: []string{}, Held: []string{}}
		r.PAN = MaskPAN(d.PAN)
		v.Counts[r.Stage]++
		if demiseOpen(d) {
			v.Open++
			r.DaysLeft = daysBetween(date, d.Cutoff)
			r.Positions = pos[d.UCC]
			if d.Verified == nil && date > d.VerifyDue {
				r.Overdue = append(r.Overdue, "death not verified by "+fmtDay(d.VerifyDue))
			}
			if !d.uccsClosed() && date >= d.Cutoff {
				r.Overdue = append(r.Overdue, "UCCs not closed before the cut-off "+fmtDay(d.Cutoff))
			}
			if d.Transmission == nil && date > d.TransmissionDue {
				r.Overdue = append(r.Overdue, "transmission information not sent by "+fmtDay(d.TransmissionDue))
			}
			if len(r.Overdue) > 0 {
				v.Overdue++
			}
			r.Held = []string{"payouts and running account settlement", "debits from the demat accounts", "trading limits and orders", "mutual fund orders"}
		}
		v.Cases = append(v.Cases, r)
	}
	sort.SliceStable(v.Cases, func(i, j int) bool {
		a, b := v.Cases[i], v.Cases[j]
		if demiseOpen(a.DemiseCase) != demiseOpen(b.DemiseCase) {
			return demiseOpen(a.DemiseCase)
		}
		if a.Cutoff != b.Cutoff {
			return a.Cutoff < b.Cutoff
		}
		return a.ID < b.ID
	})
	ls, _ := docs.List[DemiseList](ctx, o.Docs, tenant, cDemiseList, "")
	sort.Slice(ls, func(i, j int) bool { return ls[i].ID > ls[j].ID })
	v.Period = prevMonth(date)
	v.Cutoff, v.Circular = demiseCycle(v.Period)
	for _, l := range ls {
		if l.Period == v.Period {
			v.Loaded = true
			v.Cutoff, v.Circular = l.Cutoff, firstOf(l.Circular, v.Circular)
			break
		}
	}
	if len(ls) > 24 {
		ls = ls[:24]
	}
	v.Lists = append(v.Lists, ls...)
	return v, nil
}

// DemiseUCCList is the list of UCCs to close on an exchange's portal, for
// a firm that does not send them by the UCC API or file.
func (o *Ops) DemiseUCCList(ctx context.Context, tenant, actor, exchange string) ([]byte, error) {
	if err := o.require(ctx, tenant, actor, "Closing a deceased client's UCCs", RoleCompliance, RoleChecker); err != nil {
		return nil, err
	}
	ex := strings.ToUpper(strings.TrimSpace(exchange))
	ds, err := docs.List[DemiseCase](ctx, o.Docs, tenant, cDemiseCase, "")
	if err != nil {
		return nil, err
	}
	set, _ := o.Settings(ctx, tenant)
	cs, _ := o.ClientMap(ctx, tenant)
	rows := [][]string{{"Exchange", "Member code", "Client code", "Client name", "Segments", "Status to set", "Reason", "Close before", "Case"}}
	for _, d := range ds {
		if !demiseOpen(d) || d.Verified == nil {
			continue
		}
		for _, u := range d.UCCs {
			if u.Status == DemiseUCCDone || (ex != "" && u.Exchange != ex) {
				continue
			}
			st := "Closed (C)"
			if u.Exchange == "MCX" {
				st = "Inactive (2)"
			}
			rows = append(rows, []string{u.Exchange, set.MemberCodes[u.Exchange], d.UCC, d.Name, strings.Join(cs[d.UCC].Segments, " "), st, "Deceased, reported by the KRA", d.Cutoff, d.ID})
		}
	}
	o.log(ctx, tenant, actor, "deceased.ucc_list", firstOf(ex, "all"), map[string]any{"rows": len(rows) - 1})
	return csvBytesOf(rows), nil
}

// ---- the monthly cycle (Periodic work) ----

// demisePeriodic is the month's cycle: the list loaded and every UCC on it
// closed before the cut-off.
func (o *Ops) demisePeriodic(ctx context.Context, tenant, actor, period, date string) (string, error) {
	ls, _ := docs.List[DemiseList](ctx, o.Docs, tenant, cDemiseList, "")
	var l *DemiseList
	for i := range ls {
		if ls[i].Period == period {
			l = &ls[i]
		}
	}
	cut, _ := demiseCycle(period)
	if l == nil {
		msg := fmt.Sprintf("Download <TMcode>_Demise_Clients_by_KRA from NSE's member portal (ENIT > Log > Downloads) for the PANs reported up to the end of %s and load it on Compliance → Deceased clients, with the cut-off from the month's circular (usually %s)", monthLabel(period), fmtDay(cut))
		o.raiseWork(ctx, tenant, "periodic", "kra_demise:"+period, "Load the KRAs' demise list for "+monthLabel(period), msg, "", RoleCompliance, "deceased", date)
		return "", &pendingErr{msg, "Waiting for a file"}
	}
	ds, _ := docs.List[DemiseCase](ctx, o.Docs, tenant, cDemiseCase, "")
	open := 0
	for _, d := range ds {
		if d.Period == period && demiseOpen(d) && !d.uccsClosed() {
			open++
			o.demiseWork(ctx, tenant, actor, d)
		}
	}
	if open > 0 {
		return "", &pendingErr{fmt.Sprintf("%d client%s on the list of %s still to close before %s", open, plural(open), fmtDay(l.Date), fmtDay(l.Cutoff)), "Due"}
	}
	return fmt.Sprintf("List of %s loaded: %d client%s matched; every UCC closed or the intimation found false", fmtDay(l.Date), l.Matched, plural(l.Matched)), nil
}

// demiseDone says whether the month's cycle is done, for Periodic work.
func (o *Ops) demiseDone(ctx context.Context, tenant, period string) (bool, string) {
	ls, _ := docs.List[DemiseList](ctx, o.Docs, tenant, cDemiseList, "")
	var l *DemiseList
	for i := range ls {
		if ls[i].Period == period {
			l = &ls[i]
		}
	}
	if l == nil {
		return false, ""
	}
	ds, _ := docs.List[DemiseCase](ctx, o.Docs, tenant, cDemiseCase, "")
	open := 0
	for _, d := range ds {
		if d.Period == period && demiseOpen(d) && !d.uccsClosed() {
			open++
		}
	}
	if open > 0 {
		return false, fmt.Sprintf("List of %s loaded; %d client%s still to close before %s", fmtDay(l.Date), open, plural(open), fmtDay(l.Cutoff))
	}
	return true, fmt.Sprintf("List of %s loaded: %d client%s matched, every UCC closed", fmtDay(l.Date), l.Matched, plural(l.Matched))
}

// demiseDue is the last day to close the period's UCCs: the day before
// its cut-off.
func (o *Ops) demiseDue(ctx context.Context, tenant, period string) string {
	cut, _ := demiseCycle(period)
	ls, _ := docs.List[DemiseList](ctx, o.Docs, tenant, cDemiseList, "")
	for _, l := range ls {
		if l.Period == period && validDate(l.Cutoff) {
			cut = l.Cutoff
		}
	}
	return addDays(cut, -1)
}

// ---- self-inspection ----

func siDeceased(e *siEnv) (siOut, error) {
	var s siOut
	s.input("Deceased clients", e.date)
	ds, err := docs.List[DemiseCase](e.ctx, e.o.Docs, e.tenant, cDemiseCase, "")
	if err != nil {
		return s, err
	}
	ls, _ := docs.List[DemiseList](e.ctx, e.o.Docs, e.tenant, cDemiseList, "")
	if len(ls) == 0 && len(ds) == 0 {
		return siSkip("Waiting for data: no KRA demise list (<TMcode>_Demise_Clients_by_KRA) has been loaded on Compliance → Deceased clients.")
	}
	for _, d := range ds {
		if d.False != nil || d.ReportedOn > e.date {
			continue
		}
		s.checked++
		c := e.clients[d.UCC]
		switch {
		case !d.uccsClosed() && e.date >= d.Cutoff:
			s.bad(fmt.Sprintf("%s · %s · UCC not closed on %s before the cut-off %s", d.ID, siClient(d.UCC), d.openExchanges(), fmtDay(d.Cutoff)))
		case c.UCC != "" && (!c.Blocked || c.Deceased == nil):
			s.bad(fmt.Sprintf("%s · %s · reported deceased on %s, but debits are not blocked", d.ID, siClient(d.UCC), fmtDay(d.ReportedOn)))
		}
	}
	s.summary = fmt.Sprintf("%d clients reported deceased checked; %d with a UCC not closed by the cut-off or debits not blocked.", s.checked, s.found)
	return s, nil
}

func (d DemiseCase) openExchanges() string {
	var out []string
	for _, u := range d.UCCs {
		if u.Status != DemiseUCCDone {
			out = append(out, u.Exchange)
		}
	}
	return strings.Join(out, ", ")
}
