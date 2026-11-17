package ops

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/xfile"
)

// ---- clients mapped to authorised persons: the weekly report to each exchange ----
//
// Every change of a client's authorised person (AP) is recorded: the day it
// took effect, the AP before and after, and who made it. Each week the
// clients newly mapped, re-mapped or unmapped, and new clients of APs, are
// reported to every exchange the firm is a member of:
//
//   - NSE/COMP/49509 (3 Sep 2021): reporting of clients mapped to APs, on
//     ENIT (Compliance > Authorised Person);
//   - MSE circular 10854 (4 Sep 2021), item 15 of MSE's membership master
//     circular MSE/MEM/15275/2024 (30 Apr 2024): weekly from 1 Nov 2021, on
//     or before the 2nd trading day of the following week, for the clients
//     registered or (re)mapped in the week; the details must match the
//     exchange's UCC records;
//   - it is not on NSE's lists of common submissions (NSE/INSP/69184,
//     NSE/INSP/70505), so each exchange gets its own file.
//
// NSE/COMP/63628 (28 Aug 2024) asks that each AP's details, with the number
// of its clients, be kept current in the exchange's records (para 5), and
// that an AP's own mobile number and email address are not on any of its
// clients' UCC (Annexure A, item 28): apContactHits checks that, and the
// monthly count reconciliation compares each AP's client count on the
// exchange with Ledgerline's.
//
// The exchanges' upload layouts (Annexure 1 and the user manual attached to
// the circulars) could not be read: each exchange's file is a layout in
// config/formats (<exchange>-ap-client-mapping), marked unconfirmed, whose
// columns can be changed to the exchange's template without new code.
//
// What each exchange already has is kept per client (cAPMapEx): the AP as
// last reported in an upload, or as it was when the register started. The
// week's file is the difference between that and the client's AP at the
// end of the week, so a record held back one week (the client's UCC not yet
// accepted, an AP without a registration number) goes in a later week's
// file once it is right.

const (
	cAPMap      = "apmap"      // key: UCC|nnnnnnnn: every change of a client's AP
	cAPMapEx    = "apmapex"    // key: EXCHANGE|UCC: the AP the exchange has for the client
	cAPMapUp    = "apmapup"    // key: EXCHANGE|week|nn: an upload of the week's file
	cAPMapState = "apmapstate" // "main": when the register started
	cAPCount    = "apcount"    // key: month|EXCHANGE: the monthly count reconciliation
)

// Kinds of change of a client's AP.
const (
	APMapped   = "Mapped"
	APRemapped = "Re-mapped"
	APUnmapped = "Unmapped"
	APExisting = "Existing mapping"
)

// Where a change was made.
const (
	APViaMapping  = "Client mapping" // Clients → Branch and partner (or the API)
	APViaOpening  = "Account opening"
	APViaOnline   = "Online application"
	APViaImport   = "Client master import"
	APViaFound    = "Found on check" // the client record changed outside these
	APViaExisting = "On record when the register started"
)

// States of a record in the week's file.
const (
	APInFile      = "In the file"
	APInFileCheck = "In the file · check"
	APWaits       = "Waits"
	APToFix       = "To fix"
)

// APMapChange is one change of a client's authorised person.
type APMapChange struct {
	ID        string `json:"id"`  // UCC|nnnnnnnn
	Seq       int64  `json:"seq"` // 0: an existing mapping
	UCC       string `json:"ucc"`
	Name      string `json:"name"`
	From      string `json:"from,omitempty"` // the AP before ("" none)
	To        string `json:"to,omitempty"`   // the AP after ("" none)
	Kind      string `json:"kind"`
	NewClient bool   `json:"new_client,omitempty"` // mapped when the account was opened
	// Date is the day it took effect: the day it was made, or for an
	// existing mapping the client's account opening date ("" when that is
	// not known).
	Date string `json:"date,omitempty"`
	At   string `json:"at"`
	By   string `json:"by"`
	Via  string `json:"via"`
}

// APMapState is when the register started.
type APMapState struct {
	Started  string `json:"started"`
	At       string `json:"at"`
	Existing int    `json:"existing"` // mappings on record then
}

// APMapKnown is the AP an exchange has for a client: as last reported, or
// as it was when the register started.
type APMapKnown struct {
	Exchange string `json:"exchange"`
	UCC      string `json:"ucc"`
	AP       string `json:"ap"` // "" none
	RegNo    string `json:"reg_no,omitempty"`
	Since    string `json:"since,omitempty"`
	Upload   string `json:"upload"` // the upload that reported it, or "existing"
}

func apKind(from, to string) string {
	switch {
	case from == "":
		return APMapped
	case to == "":
		return APUnmapped
	}
	return APRemapped
}

// ---- recording changes ----

// ensureAPBaseline starts the register once: every client mapped to an AP
// then is recorded as an existing mapping, dated its account opening date,
// and taken as known to each exchange the firm is a member of. Existing
// mappings are never in a weekly file.
func (o *Ops) ensureAPBaseline(ctx context.Context, tenant string) APMapState {
	if st, _, err := docs.Get[APMapState](ctx, o.Docs, tenant, cAPMapState, "main"); err == nil {
		return st
	}
	st := APMapState{Started: o.now().Format("2006-01-02"), At: o.now().Format(time.RFC3339)}
	if _, err := docs.Put(ctx, o.Docs, tenant, cAPMapState, "main", st, 0, SystemActor); err != nil {
		got, _, _ := docs.Get[APMapState](ctx, o.Docs, tenant, cAPMapState, "main")
		return got // started by another request
	}
	cs, _ := o.Clients(ctx, tenant)
	for _, c := range cs {
		if strings.TrimSpace(c.Partner) == "" {
			continue
		}
		o.recordAPExisting(ctx, tenant, SystemActor, c, APViaExisting)
		st.Existing++
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cAPMapState, "main", st, SystemActor)
	o.log(ctx, tenant, SystemActor, "ap.mapping.started", "main", map[string]any{"existing": st.Existing})
	return st
}

// recordAPExisting records a mapping that was there before Ledgerline knew
// it (the register's start, or a client brought from the previous system),
// as known to each exchange the firm is a member of.
func (o *Ops) recordAPExisting(ctx context.Context, tenant, actor string, c Client, via string) {
	ap := strings.ToUpper(strings.TrimSpace(c.Partner))
	if ap == "" {
		return
	}
	ch := APMapChange{ID: c.UCC + "|00000000", UCC: c.UCC, Name: c.Name, To: ap, Kind: APExisting, Date: c.KYCDate, At: o.now().Format(time.RFC3339), By: actor, Via: via}
	if err := docs.Upsert(ctx, o.Docs, tenant, cAPMap, ch.ID, ch, actor); err != nil {
		return
	}
	set, _ := o.Settings(ctx, tenant)
	p, _, _ := docs.Get[Partner](ctx, o.Docs, tenant, cPartner, ap)
	for _, x := range memberExchanges(set) {
		if apOnExchange(c, x) {
			_ = docs.Upsert(ctx, o.Docs, tenant, cAPMapEx, x+"|"+c.UCC, APMapKnown{Exchange: x, UCC: c.UCC, AP: ap, RegNo: apRegFor(p, x), Since: c.KYCDate, Upload: "existing"}, actor)
		}
	}
}

// recordAPChange records a change of a client's AP (nothing when it did not
// change).
func (o *Ops) recordAPChange(ctx context.Context, tenant, actor string, c Client, from, to, via string, newClient bool) {
	from, to = strings.ToUpper(strings.TrimSpace(from)), strings.ToUpper(strings.TrimSpace(to))
	if from == to {
		return
	}
	if via == APViaOpening && strings.HasPrefix(actor, "applicant") {
		via = APViaOnline
	}
	n, err := o.next(ctx, tenant, "apmap", actor)
	if err != nil {
		o.log(ctx, tenant, actor, "ap.mapping.error", c.UCC, map[string]any{"error": err.Error()})
		return
	}
	ch := APMapChange{ID: fmt.Sprintf("%s|%08d", c.UCC, n), Seq: n, UCC: c.UCC, Name: c.Name, From: from, To: to, Kind: apKind(from, to), NewClient: newClient,
		Date: o.now().Format("2006-01-02"), At: o.now().Format(time.RFC3339), By: actor, Via: via}
	if err := docs.Upsert(ctx, o.Docs, tenant, cAPMap, ch.ID, ch, actor); err != nil {
		o.log(ctx, tenant, actor, "ap.mapping.error", c.UCC, map[string]any{"error": err.Error()})
		return
	}
	o.log(ctx, tenant, actor, "ap.mapping.changed", c.UCC, map[string]any{"from": from, "to": to, "kind": ch.Kind, "via": via, "date": ch.Date})
}

// apHistory is every client's changes, oldest first.
func (o *Ops) apHistory(ctx context.Context, tenant string) map[string][]APMapChange {
	all, _ := docs.List[APMapChange](ctx, o.Docs, tenant, cAPMap, "")
	out := map[string][]APMapChange{}
	for _, ch := range all {
		out[ch.UCC] = append(out[ch.UCC], ch)
	}
	for _, h := range out {
		sort.SliceStable(h, func(i, j int) bool { return h[i].Seq < h[j].Seq })
	}
	return out
}

// syncAPMap records, as found on check, a client whose AP is not the one the
// register last recorded (changed by a way that does not record it).
func (o *Ops) syncAPMap(ctx context.Context, tenant string, cs []Client, hist map[string][]APMapChange) map[string][]APMapChange {
	found := false
	for _, c := range cs {
		h := hist[c.UCC]
		last := ""
		if len(h) > 0 {
			last = h[len(h)-1].To
		}
		if cur := strings.ToUpper(strings.TrimSpace(c.Partner)); cur != last {
			o.recordAPChange(ctx, tenant, SystemActor, c, last, cur, APViaFound, false)
			found = true
		}
	}
	if found {
		return o.apHistory(ctx, tenant)
	}
	return hist
}

// apAt is a client's AP on a date, and the change that made it so. An
// existing mapping counts from the start: it is taken as known to the
// exchanges already, whatever its date.
func apAt(h []APMapChange, date string) (string, *APMapChange) {
	ap, chg := "", (*APMapChange)(nil)
	for i := range h {
		if h[i].Kind == APExisting || h[i].Date == "" || h[i].Date <= date {
			ap, chg = h[i].To, &h[i]
		}
	}
	return ap, chg
}

// isAP says whether a partner is an authorised person (a partner saved
// without a kind is taken as one, as the firm profile does).
func isAP(p Partner) bool { return p.Kind == "" || p.Kind == "Authorised person" }

// ---- weeks ----

// apWeekEnd is the Sunday that ends the week (Monday to Sunday) holding date.
func apWeekEnd(date string) string {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return date
	}
	return t.AddDate(0, 0, (7-int(t.Weekday()))%7).Format("2006-01-02")
}

// apLastWeek is the Sunday of the latest week that ended before date.
func apLastWeek(date string) string {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return date
	}
	back := int(t.Weekday())
	if back == 0 {
		back = 7
	}
	return t.AddDate(0, 0, -back).Format("2006-01-02")
}

// apWeekLabel names a week by its Monday and Sunday.
func apWeekLabel(sun string) string {
	e, err := time.Parse("2006-01-02", sun)
	if err != nil {
		return sun
	}
	s := e.AddDate(0, 0, -6)
	switch {
	case s.Year() != e.Year():
		return s.Format("2 Jan 2006") + " – " + e.Format("2 Jan 2006")
	case s.Month() != e.Month():
		return s.Format("2 Jan") + " – " + e.Format("2 Jan 2006")
	}
	return s.Format("2") + "–" + e.Format("2 Jan 2006")
}

// apWeekDue is the 2nd trading day of the week after (MSE/MEM/15275/2024,
// item 15).
func apWeekDue(set Settings, sun string) string { return addTradingDays(set, sun, 2) }

// ---- the client at each exchange ----

// apExchangeSegs are the segments a client can trade on each exchange.
var apExchangeSegs = map[string][]string{"NSE": {"CM", "FO", "CD", "COM"}, "BSE": {"CM", "FO", "CD", "COM"}, "MSE": {"CM", "FO", "CD"}, "MCX": {"COM"}, "NCDEX": {"COM"}}

// apOnExchange says whether a client trades on an exchange (has a segment
// it offers): a client of the DP only is on none.
func apOnExchange(c Client, x string) bool {
	if c.DPOnly {
		return false
	}
	if len(c.Segments) == 0 {
		return true
	}
	for _, s := range c.Segments {
		if contains(apExchangeSegs[x], s) {
			return true
		}
	}
	return false
}

// apRegFor is an AP's registration number at an exchange.
func apRegFor(p Partner, x string) string {
	return firstOf(p.ExchangeRegs[x], p.RegNo)
}

// apUCCReg is the part of an exchange UCC registration (NSE, BSE and MCX
// keep theirs in one collection) the check reads.
type apUCCReg struct {
	Exchange     string `json:"exchange"`
	UCC          string `json:"ucc"`
	Status       string `json:"status"`
	Reason       string `json:"reason"`
	PANStatus    string `json:"pan_status"`
	Accepted     bool   `json:"accepted"`      // NSE: saved at NSE at least once
	AcceptedHash string `json:"accepted_hash"` // BSE, MCX: the record the exchange holds
	Via          string `json:"via"`
}

// held says whether the exchange holds the client's UCC.
func (r apUCCReg) held() bool {
	return r.Accepted || r.Status == UCCAccepted || r.AcceptedHash != "" || r.Via == "Before Ledgerline"
}

func (o *Ops) apUCCRegs(ctx context.Context, tenant string) (map[string]apUCCReg, map[string]bool) {
	regs, _ := docs.List[apUCCReg](ctx, o.Docs, tenant, cUCCReg, "")
	by, uses := map[string]apUCCReg{}, map[string]bool{}
	for _, r := range regs {
		x := strings.ToUpper(r.Exchange)
		by[x+"|"+strings.ToUpper(r.UCC)] = r
		uses[x] = true
	}
	return by, uses
}

// ---- the AP's own contact details on clients ----

// APContactHit is a client whose record carries an AP's own mobile or
// email (NSE/COMP/63628, Annexure A item 28).
type APContactHit struct {
	Partner   string `json:"partner"`
	Name      string `json:"name"`  // the AP's
	Field     string `json:"field"` // mobile or email
	UCC       string `json:"ucc"`
	Client    string `json:"client"`
	OwnClient bool   `json:"own_client"` // mapped to this AP
}

func apMobileKey(m string) string {
	d := digitsOf(m)
	if len(d) > 10 {
		d = d[len(d)-10:]
	}
	return d
}

// apContactHits lists the clients whose mobile or email is an AP's own. A
// client with the AP's own PAN is the AP's own account and is left out.
func apContactHits(cs []Client, ps []Partner) []APContactHit {
	var out []APContactHit
	for _, p := range ps {
		if !isAP(p) || (p.Mobile == "" && p.Email == "") {
			continue
		}
		mob, mail := apMobileKey(p.Mobile), strings.ToLower(strings.TrimSpace(p.Email))
		for _, c := range cs {
			if c.Status == "Closed" || (p.PAN != "" && strings.EqualFold(c.PAN, p.PAN)) {
				continue
			}
			own := strings.EqualFold(c.Partner, p.Code)
			if mob != "" && len(mob) == 10 && apMobileKey(c.Mobile) == mob {
				out = append(out, APContactHit{Partner: p.Code, Name: p.Name, Field: "mobile", UCC: c.UCC, Client: c.Name, OwnClient: own})
			}
			if mail != "" && strings.ToLower(strings.TrimSpace(c.Email)) == mail {
				out = append(out, APContactHit{Partner: p.Code, Name: p.Name, Field: "email", UCC: c.UCC, Client: c.Name, OwnClient: own})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Partner != out[j].Partner {
			return out[i].Partner < out[j].Partner
		}
		return out[i].UCC < out[j].UCC
	})
	return out
}

// APContactCheck is the check shown on Branches & partners.
func (o *Ops) APContactCheck(ctx context.Context, tenant, actor string) ([]APContactHit, error) {
	if err := o.require(ctx, tenant, actor, "Viewing the AP client reporting", RoleMaker, RoleChecker, RoleCompliance, RoleRisk); err != nil {
		return nil, err
	}
	cs, err := o.Clients(ctx, tenant)
	if err != nil {
		return nil, err
	}
	ps, _ := docs.List[Partner](ctx, o.Docs, tenant, cPartner, "")
	out := apContactHits(cs, ps)
	if out == nil {
		out = []APContactHit{}
	}
	return out, nil
}

// ---- the week's report ----

// APMapRecord is one client in an exchange's file for the week.
type APMapRecord struct {
	UCC       string   `json:"ucc"`
	Name      string   `json:"name"`
	PAN       string   `json:"pan"` // masked
	Kind      string   `json:"kind"`
	NewClient bool     `json:"new_client,omitempty"`
	From      string   `json:"from,omitempty"`
	FromReg   string   `json:"from_reg,omitempty"`
	To        string   `json:"to,omitempty"`
	ToReg     string   `json:"to_reg,omitempty"`
	ChangedOn string   `json:"changed_on"`
	By        string   `json:"by,omitempty"`
	Via       string   `json:"via,omitempty"`
	State     string   `json:"state"`
	Notes     []string `json:"notes"`
	pan       string   // the full PAN, for the file only
}

func (r APMapRecord) inFile() bool { return r.State == APInFile || r.State == APInFileCheck }

// APMapUpload is one upload of an exchange's file, with its acknowledgement.
type APMapUpload struct {
	ID       string        `json:"id"`
	Exchange string        `json:"exchange"`
	Week     string        `json:"week"`
	Date     string        `json:"date"` // uploaded on
	Ack      string        `json:"ack"`
	Note     string        `json:"note,omitempty"`
	FileID   string        `json:"file_id"`
	FileName string        `json:"file_name"`
	Rows     int           `json:"rows"`
	Records  []APMapRecord `json:"records"`
	Late     bool          `json:"late,omitempty"`
	By       string        `json:"by"`
	At       string        `json:"at"`
}

// APMapExchange is one exchange's part of the week.
type APMapExchange struct {
	Exchange  string         `json:"exchange"`
	Member    string         `json:"member"`
	Layout    string         `json:"layout"`
	Confirmed bool           `json:"confirmed"`
	FileName  string         `json:"file_name,omitempty"`
	Records   []APMapRecord  `json:"records"`
	Counts    map[string]int `json:"counts"`
	Uploads   []APMapUpload  `json:"uploads"`
	Status    string         `json:"status"` // Nothing to report, To upload, Uploaded, To fix, Waiting
	UsesUCC   bool           `json:"uses_ucc"`
	Note      string         `json:"note,omitempty"`
}

// APMappingView is the week's report to every exchange.
type APMappingView struct {
	Week      string          `json:"week"` // the Sunday
	From      string          `json:"from"` // the Monday
	Label     string          `json:"label"`
	Due       string          `json:"due"`
	Status    string          `json:"status"` // Done, Due, Overdue, Upcoming
	Exchanges []APMapExchange `json:"exchanges"`
	Changes   []APMapChange   `json:"changes"` // made in the week
	Started   string          `json:"started"`
	Existing  int             `json:"existing"`
	Contacts  []APContactHit  `json:"contacts"`
	Weeks     []string        `json:"weeks"` // recent weeks (Sundays), latest first
	Notes     []string        `json:"notes"`
}

func apLayoutID(x string) string { return strings.ToLower(x) + "-ap-client-mapping" }

// apEnv is what the report reads, once.
type apEnv struct {
	set      Settings
	clients  []Client
	byUCC    map[string]Client
	hist     map[string][]APMapChange
	partners map[string]Partner
	regs     map[string]apUCCReg
	uses     map[string]bool
	contacts map[string][]string // UCC: the APs whose own mobile or email it carries
}

// errAPNotStarted: the register has not started (it starts the first time
// it is opened or a client's AP is changed).
var errAPNotStarted = errors.New("the register of clients' AP mappings has not started: open Branches & partners → AP client reporting")

// apEnvFor reads what the report needs. With write it starts the register
// if it has not, and records a client's AP changed without a record as
// found on check; without it (the self-inspection, a task's status) it
// changes nothing and takes those changes as found today.
func (o *Ops) apEnvFor(ctx context.Context, tenant string, write bool) (apEnv, error) {
	if write {
		o.ensureAPBaseline(ctx, tenant)
	} else if _, _, err := docs.Get[APMapState](ctx, o.Docs, tenant, cAPMapState, "main"); err != nil {
		return apEnv{}, errAPNotStarted
	}
	e := apEnv{byUCC: map[string]Client{}, partners: map[string]Partner{}, contacts: map[string][]string{}}
	var err error
	if e.set, err = o.Settings(ctx, tenant); err != nil {
		return e, err
	}
	if e.clients, err = o.Clients(ctx, tenant); err != nil {
		return e, err
	}
	sort.Slice(e.clients, func(i, j int) bool { return e.clients[i].UCC < e.clients[j].UCC })
	for _, c := range e.clients {
		e.byUCC[c.UCC] = c
	}
	if e.hist = o.apHistory(ctx, tenant); write {
		e.hist = o.syncAPMap(ctx, tenant, e.clients, e.hist)
	} else {
		today := o.now().Format("2006-01-02")
		for _, c := range e.clients {
			h := e.hist[c.UCC]
			last := ""
			if len(h) > 0 {
				last = h[len(h)-1].To
			}
			if cur := strings.ToUpper(strings.TrimSpace(c.Partner)); cur != last {
				e.hist[c.UCC] = append(h, APMapChange{UCC: c.UCC, Name: c.Name, From: last, To: cur, Kind: apKind(last, cur), Date: today, By: SystemActor, Via: APViaFound})
			}
		}
	}
	ps, _ := docs.List[Partner](ctx, o.Docs, tenant, cPartner, "")
	for _, p := range ps {
		e.partners[p.Code] = p
	}
	for _, h := range apContactHits(e.clients, ps) {
		e.contacts[h.UCC] = appendUnique(e.contacts[h.UCC], h.Partner+"'s own "+h.Field)
	}
	e.regs, e.uses = o.apUCCRegs(ctx, tenant)
	return e, nil
}

// apRecordsFor works out an exchange's records for the week ending sun:
// each client whose AP at the end of the week is not the one the exchange
// has, checked against the client's UCC at that exchange.
func (o *Ops) apRecordsFor(ctx context.Context, tenant string, e apEnv, x, sun string) []APMapRecord {
	known, _ := docs.List[APMapKnown](ctx, o.Docs, tenant, cAPMapEx, x+"|")
	have := map[string]APMapKnown{}
	for _, k := range known {
		have[k.UCC] = k
	}
	out := []APMapRecord{}
	for _, c := range e.clients {
		if !apOnExchange(c, x) {
			continue
		}
		want, ch := apAt(e.hist[c.UCC], sun)
		k := have[c.UCC]
		if want == k.AP {
			continue
		}
		r := APMapRecord{UCC: c.UCC, Name: c.Name, PAN: MaskPAN(c.PAN), pan: c.PAN, Kind: apKind(k.AP, want), From: k.AP, To: want, Notes: []string{}}
		if ch != nil {
			r.ChangedOn, r.By, r.Via, r.NewClient = ch.Date, ch.By, ch.Via, ch.NewClient && k.AP == ""
		}
		var fix, wait, warn []string
		// The client, as the exchange's UCC has it.
		switch {
		case !rePAN.MatchString(strings.ToUpper(c.PAN)):
			fix = append(fix, "the client's PAN is missing or not valid")
		case strings.TrimSpace(c.Name) == "":
			fix = append(fix, "the client's name is missing")
		}
		reg, hasReg := e.regs[x+"|"+c.UCC]
		switch {
		case hasReg && reg.held() && (reg.PANStatus == "I" || reg.PANStatus == "Invalid"):
			fix = append(fix, x+" could not verify the client's PAN: correct it and send the UCC again")
		case hasReg && reg.held():
		case hasReg && (reg.Status == UCCRejected || reg.Status == UCCNeedsFixing):
			why := x + "'s UCC is " + strings.ToLower(reg.Status)
			if reg.Reason != "" {
				why += ": " + clip(reg.Reason, 80)
			}
			fix = append(fix, why+"; correct it on Exchange UCC")
		case hasReg:
			wait = append(wait, "the client's UCC is not yet accepted by "+x+" ("+strings.ToLower(firstOf(reg.Status, "not sent"))+"): it goes in the week it is")
		case c.Status == "Application" || c.Status == "KYC verification" || c.Status == "UCC upload":
			wait = append(wait, "the account is still being opened ("+strings.ToLower(c.Status)+"): it goes in the week its UCC is registered at "+x)
		case e.uses[x]:
			warn = append(warn, "Ledgerline has no answer from "+x+"'s UCC for this client: check the UCC is registered there")
		}
		// The APs, by their registration numbers at the exchange.
		if want != "" {
			p, ok := e.partners[want]
			switch {
			case !ok:
				fix = append(fix, want+" is not on Branches & partners")
			case !isAP(p):
				fix = append(fix, want+" is a "+strings.ToLower(p.Kind)+", not an authorised person")
			case !p.Active:
				fix = append(fix, want+" is not an active AP")
			}
			if r.ToReg = apRegFor(p, x); r.ToReg == "" && ok {
				fix = append(fix, want+" has no "+x+" AP registration number: add it on Branches & partners")
			}
		}
		if k.AP != "" {
			r.FromReg = firstOf(k.RegNo, apRegFor(e.partners[k.AP], x))
			if r.FromReg == "" {
				fix = append(fix, "the "+x+" registration number of "+k.AP+", the AP before, is not known")
			}
		}
		for _, w := range e.contacts[c.UCC] {
			warn = append(warn, "the client's record carries "+w+" (NSE/COMP/63628)")
		}
		switch {
		case len(fix) > 0:
			r.State, r.Notes = APToFix, append(fix, wait...)
		case len(wait) > 0:
			r.State, r.Notes = APWaits, wait
		case len(warn) > 0:
			r.State, r.Notes = APInFileCheck, warn
		default:
			r.State = APInFile
		}
		out = append(out, r)
	}
	return out
}

func (o *Ops) apUploads(ctx context.Context, tenant, x, sun string) []APMapUpload {
	us, _ := docs.List[APMapUpload](ctx, o.Docs, tenant, cAPMapUp, x+"|"+sun+"|")
	if us == nil {
		us = []APMapUpload{}
	}
	return us
}

// apExchangeStatus is where an exchange's week stands, and whether it is
// done: nothing left to go in the file, and nothing to fix unless the
// week's file was uploaded.
func apExchangeStatus(ex APMapExchange) (string, bool) {
	n := ex.Counts
	switch {
	case n[APInFile]+n[APInFileCheck] > 0:
		return "To upload", false
	case n[APToFix] > 0 && len(ex.Uploads) == 0:
		return "To fix", false
	case len(ex.Uploads) > 0:
		return "Uploaded", true
	case n[APWaits] > 0:
		return "Waiting", true
	}
	return "Nothing to report", true
}

// apMappingReport builds the week's report (sun: the week's Sunday; ""
// the latest week that has ended); write as apEnvFor.
func (o *Ops) apMappingReport(ctx context.Context, tenant, sun, date string, write bool) (APMappingView, apEnv, error) {
	if !validDate(date) {
		date = o.now().Format("2006-01-02")
	}
	if sun == "" {
		sun = apLastWeek(date)
	} else if !validDate(sun) {
		return APMappingView{}, apEnv{}, invalid("give the week as a date in it (YYYY-MM-DD)")
	}
	sun = apWeekEnd(sun)
	e, err := o.apEnvFor(ctx, tenant, write)
	if err != nil {
		return APMappingView{}, e, err
	}
	st, _, _ := docs.Get[APMapState](ctx, o.Docs, tenant, cAPMapState, "main")
	mon := addDays(sun, -6)
	v := APMappingView{Week: sun, From: mon, Label: apWeekLabel(sun), Due: apWeekDue(e.set, sun), Exchanges: []APMapExchange{}, Changes: []APMapChange{},
		Started: st.Started, Existing: st.Existing, Contacts: []APContactHit{}, Notes: []string{}}
	for i := 0; i < 8; i++ {
		v.Weeks = append(v.Weeks, addDays(apLastWeek(date), -7*i))
	}
	for _, u := range sortedKeys(e.hist) {
		for _, ch := range e.hist[u] {
			if ch.Kind != APExisting && ch.Date >= mon && ch.Date <= sun {
				v.Changes = append(v.Changes, ch)
			}
		}
	}
	lib, err := XFiles()
	if err != nil {
		return v, e, err
	}
	exs := memberExchanges(e.set)
	if len(exs) == 0 {
		v.Notes = append(v.Notes, "Enter the firm's member codes in Settings: the file is made for each exchange the firm is a member of.")
	}
	if st.Started > mon {
		v.Notes = append(v.Notes, "The register started on "+fmtDay(st.Started)+": mappings on record then are taken as already reported to the exchanges.")
	}
	done := true
	for _, x := range exs {
		ex := APMapExchange{Exchange: x, Member: e.set.MemberCodes[x], Layout: apLayoutID(x), Counts: map[string]int{}, UsesUCC: e.uses[x]}
		if s, ok := lib.Get(ex.Layout); ok {
			ex.Confirmed = s.Confirmed
			ex.FileName, _ = s.FileName(xfile.Vars{"date": sun, "member": ex.Member})
		}
		ex.Records = o.apRecordsFor(ctx, tenant, e, x, sun)
		for _, r := range ex.Records {
			ex.Counts[r.State]++
		}
		ex.Uploads = o.apUploads(ctx, tenant, x, sun)
		if !e.uses[x] {
			ex.Note = "Ledgerline has no answers from " + x + "'s UCC, so the records are checked against the client master only: make sure each client's UCC at " + x + " has the same PAN and name."
		}
		var ok bool
		ex.Status, ok = apExchangeStatus(ex)
		done = done && ok
		v.Exchanges = append(v.Exchanges, ex)
	}
	switch {
	case done:
		v.Status = "Done"
	case date > v.Due:
		v.Status = "Overdue"
	case date > sun:
		v.Status = "Due"
	default:
		v.Status = "Upcoming"
	}
	for _, h := range apContactHits(e.clients, mapValues(e.partners)) {
		v.Contacts = append(v.Contacts, h)
	}
	return v, e, nil
}

func mapValues[V any](m map[string]V) []V {
	out := make([]V, 0, len(m))
	for _, k := range sortedKeys(m) {
		out = append(out, m[k])
	}
	return out
}

// APMappingReport is the week's report on Branches & partners.
func (o *Ops) APMappingReport(ctx context.Context, tenant, actor, week, date string) (APMappingView, error) {
	if err := o.require(ctx, tenant, actor, "Viewing the AP client reporting", RoleMaker, RoleChecker, RoleCompliance, RoleRisk); err != nil {
		return APMappingView{}, err
	}
	v, _, err := o.apMappingReport(ctx, tenant, week, date, true)
	return v, err
}

// apFileRows are the file's rows: a mapping is an A row for the new AP, an
// unmapping a D row for the AP before; a re-mapping is both.
func apFileRows(x, member string, ps map[string]Partner, rs []APMapRecord) []xfile.Row {
	var rows []xfile.Row
	row := func(r APMapRecord, ap, reg, action, words string) xfile.Row {
		return xfile.Row{Values: map[string]string{"member": member, "exchange": x, "ap_reg": reg, "ap_code": ap, "ap_name": ps[ap].Name, "ucc": r.UCC, "pan": strings.ToUpper(r.pan), "name": r.Name,
			"action": action, "action_words": words, "date": r.ChangedOn, "kind": r.Kind, "new_client": map[bool]string{true: "Y", false: "N"}[r.NewClient]}}
	}
	for _, r := range rs {
		if !r.inFile() {
			continue
		}
		if r.From != "" {
			rows = append(rows, row(r, r.From, r.FromReg, "D", "Unmap"))
		}
		if r.To != "" {
			rows = append(rows, row(r, r.To, r.ToReg, "A", "Map"))
		}
	}
	return rows
}

// apFile writes an exchange's file for the week from its records.
func (o *Ops) apFile(e apEnv, ex APMapExchange, sun string) (string, []byte, int, error) {
	lib, err := XFiles()
	if err != nil {
		return "", nil, 0, err
	}
	s, ok := lib.Get(ex.Layout)
	if !ok {
		return "", nil, 0, fmt.Errorf("%w: no layout %s in config/formats", ErrNotFound, ex.Layout)
	}
	rows := apFileRows(ex.Exchange, ex.Member, e.partners, ex.Records)
	if len(rows) == 0 {
		return "", nil, 0, fmt.Errorf("%w: nothing to report to %s for the week of %s", ErrState, ex.Exchange, apWeekLabel(sun))
	}
	content, err := s.Write(rows)
	if err != nil {
		return "", nil, 0, err
	}
	name, err := s.FileName(xfile.Vars{"date": sun, "member": ex.Member})
	return name, content, len(rows), err
}

func apExchangeOf(v APMappingView, x string) (APMapExchange, bool) {
	for _, ex := range v.Exchanges {
		if ex.Exchange == x {
			return ex, true
		}
	}
	return APMapExchange{}, false
}

// APMappingFile is an exchange's file for the week, to download and upload
// on the exchange's portal.
func (o *Ops) APMappingFile(ctx context.Context, tenant, actor, week, exchange string) (string, []byte, error) {
	if err := o.require(ctx, tenant, actor, "Making the AP client reporting file", RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return "", nil, err
	}
	v, e, err := o.apMappingReport(ctx, tenant, week, "", true)
	if err != nil {
		return "", nil, err
	}
	ex, ok := apExchangeOf(v, strings.ToUpper(strings.TrimSpace(exchange)))
	if !ok {
		return "", nil, invalid("the firm has no member code for %s in Settings", exchange)
	}
	name, content, _, err := o.apFile(e, ex, v.Week)
	if err == nil {
		o.log(ctx, tenant, actor, "ap.mapping.file", ex.Exchange+"|"+v.Week, map[string]any{"name": name})
	}
	return name, content, err
}

// APUploadReq records an upload of the week's file on an exchange's portal.
type APUploadReq struct {
	Week     string `json:"week"`
	Exchange string `json:"exchange"`
	Date     string `json:"date"` // uploaded on
	Ack      string `json:"ack"`  // the portal's acknowledgement or reference
	Note     string `json:"note,omitempty"`
}

// MarkAPMappingUploaded records the upload with the portal's acknowledgement:
// the file as made is kept, and the exchange is taken to have the records
// in it from then on.
func (o *Ops) MarkAPMappingUploaded(ctx context.Context, tenant, actor string, req APUploadReq) (APMappingView, error) {
	if err := o.require(ctx, tenant, actor, "Recording the AP client reporting upload", RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return APMappingView{}, err
	}
	today := o.now().Format("2006-01-02")
	v, e, err := o.apMappingReport(ctx, tenant, req.Week, today, true)
	if err != nil {
		return v, err
	}
	x := strings.ToUpper(strings.TrimSpace(req.Exchange))
	ex, ok := apExchangeOf(v, x)
	switch {
	case !ok:
		return v, invalid("the firm has no member code for %s in Settings", req.Exchange)
	case !validDate(req.Date) || req.Date > today:
		return v, invalid("give the date it was uploaded, not after today")
	case req.Date <= v.Week:
		return v, invalid("the week ends on %s: its file cannot be uploaded before %s", fmtDay(v.Week), fmtDay(addDays(v.Week, 1)))
	case strings.TrimSpace(req.Ack) == "":
		return v, invalid("give the acknowledgement or reference the exchange's portal gave")
	}
	for _, u := range docsListAll[APMapUpload](ctx, o, tenant, cAPMapUp, x+"|") {
		if u.Week > v.Week {
			return v, fmt.Errorf("%w: the week of %s is already recorded as uploaded to %s; a file for an earlier week would undo it", ErrState, apWeekLabel(u.Week), x)
		}
	}
	name, content, n, err := o.apFile(e, ex, v.Week)
	if err != nil {
		return v, err
	}
	f, err := o.keepFile(ctx, tenant, name, content)
	if err != nil {
		return v, err
	}
	u := APMapUpload{ID: fmt.Sprintf("%s|%s|%02d", x, v.Week, len(ex.Uploads)+1), Exchange: x, Week: v.Week, Date: req.Date, Ack: clip(strings.TrimSpace(req.Ack), 120),
		Note: clip(strings.TrimSpace(req.Note), 300), FileID: f.ID, FileName: f.Name, Rows: n, Late: req.Date > v.Due, By: actor, At: o.now().Format(time.RFC3339), Records: []APMapRecord{}}
	for _, r := range ex.Records {
		if r.inFile() {
			u.Records = append(u.Records, r)
		}
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cAPMapUp, u.ID, u, actor); err != nil {
		return v, err
	}
	for _, r := range u.Records {
		k := APMapKnown{Exchange: x, UCC: r.UCC, AP: r.To, RegNo: r.ToReg, Since: r.ChangedOn, Upload: u.ID}
		if err := docs.Upsert(ctx, o.Docs, tenant, cAPMapEx, x+"|"+r.UCC, k, actor); err != nil {
			return v, err
		}
	}
	o.log(ctx, tenant, actor, "ap.mapping.uploaded", u.ID, map[string]any{"exchange": x, "week": v.Week, "date": req.Date, "ack": u.Ack, "records": len(u.Records), "late": u.Late})
	o.closeWork(ctx, tenant, actor, "periodic", "apmap:"+v.Week+":"+x, "uploaded, acknowledgement "+u.Ack)
	v, _, err = o.apMappingReport(ctx, tenant, v.Week, today, true)
	return v, err
}

func docsListAll[T any](ctx context.Context, o *Ops, tenant, coll, prefix string) []T {
	xs, _ := docs.List[T](ctx, o.Docs, tenant, coll, prefix)
	return xs
}

// TakeAPMappingAsReported takes every client's present AP as already known
// to an exchange: for an exchange the firm was reporting to before it gave
// Ledgerline the member code. Compliance only; it is in the audit trail.
func (o *Ops) TakeAPMappingAsReported(ctx context.Context, tenant, actor, exchange, note string) (int, error) {
	if err := o.require(ctx, tenant, actor, "Taking the AP mappings as reported", RoleCompliance); err != nil {
		return 0, err
	}
	x := strings.ToUpper(strings.TrimSpace(exchange))
	set, _ := o.Settings(ctx, tenant)
	if !contains(memberExchanges(set), x) {
		return 0, invalid("the firm has no member code for %s in Settings", exchange)
	}
	if strings.TrimSpace(note) == "" {
		return 0, invalid("say why: for example, the mappings were reported to %s from the previous system", x)
	}
	e, err := o.apEnvFor(ctx, tenant, true)
	if err != nil {
		return 0, err
	}
	n := 0
	today := o.now().Format("2006-01-02")
	for _, c := range e.clients {
		if !apOnExchange(c, x) {
			continue
		}
		ap, _ := apAt(e.hist[c.UCC], today)
		k := APMapKnown{Exchange: x, UCC: c.UCC, AP: ap, RegNo: apRegFor(e.partners[ap], x), Since: today, Upload: "taken as reported by " + actor}
		if err := docs.Upsert(ctx, o.Docs, tenant, cAPMapEx, x+"|"+c.UCC, k, actor); err != nil {
			return n, err
		}
		n++
	}
	o.log(ctx, tenant, actor, "ap.mapping.taken_as_reported", x, map[string]any{"clients": n, "note": clip(note, 300)})
	return n, nil
}

// ClientAPHistory is one client's changes of AP, oldest first.
func (o *Ops) ClientAPHistory(ctx context.Context, tenant, actor, ucc string) ([]APMapChange, error) {
	if err := o.require(ctx, tenant, actor, "Viewing the AP client reporting", RoleMaker, RoleChecker, RoleCompliance, RoleRisk); err != nil {
		return nil, err
	}
	ucc = strings.ToUpper(strings.TrimSpace(ucc))
	if _, err := o.Client(ctx, tenant, ucc); err != nil {
		return nil, err
	}
	e, err := o.apEnvFor(ctx, tenant, true)
	if err != nil {
		return nil, err
	}
	h := e.hist[ucc]
	if h == nil {
		h = []APMapChange{}
	}
	return h, nil
}

// apWeekDone is whether the week's report is done for every exchange.
func (o *Ops) apWeekDone(ctx context.Context, tenant, sun, date string) (bool, string) {
	v, _, err := o.apMappingReport(ctx, tenant, sun, date, false)
	if err != nil {
		return false, err.Error()
	}
	var parts []string
	for _, ex := range v.Exchanges {
		n := ex.Counts[APInFile] + ex.Counts[APInFileCheck]
		switch ex.Status {
		case "To upload":
			parts = append(parts, fmt.Sprintf("%s: %d to upload", ex.Exchange, n))
		case "To fix":
			parts = append(parts, fmt.Sprintf("%s: %d to fix", ex.Exchange, ex.Counts[APToFix]))
		case "Uploaded":
			last := ex.Uploads[len(ex.Uploads)-1]
			parts = append(parts, fmt.Sprintf("%s: uploaded %s (%s)", ex.Exchange, fmtDay(last.Date), last.Ack))
		default:
			parts = append(parts, ex.Exchange+": "+strings.ToLower(ex.Status))
		}
	}
	return v.Status == "Done", strings.Join(parts, "; ")
}

// runAPMapping is the weekly Periodic work task: it makes each exchange's
// file and queues its upload for an Ops maker.
func (o *Ops) runAPMapping(ctx context.Context, tenant, sun, date string) (string, []PeriodicFile, error) {
	v, e, err := o.apMappingReport(ctx, tenant, sun, date, true)
	if err != nil {
		return "", nil, err
	}
	var files []PeriodicFile
	var made, fix []string
	for _, ex := range v.Exchanges {
		if ex.Counts[APToFix] > 0 {
			fix = append(fix, fmt.Sprintf("%s %d", ex.Exchange, ex.Counts[APToFix]))
		}
		if ex.Status != "To upload" {
			continue
		}
		name, content, n, err := o.apFile(e, ex, v.Week)
		if err != nil {
			return "", files, err
		}
		f, err := o.keepFile(ctx, tenant, name, content)
		if err != nil {
			return "", files, err
		}
		files = append(files, f)
		made = append(made, fmt.Sprintf("%s (%d row%s)", ex.Exchange, n, plural(n)))
		o.raiseWork(ctx, tenant, "periodic", "apmap:"+v.Week+":"+ex.Exchange, "Upload the clients mapped to APs to "+ex.Exchange+" · week of "+v.Label,
			fmt.Sprintf("%s is ready on Branches & partners → AP client reporting. Upload it on %s's member portal (NSE: ENIT, Compliance > Authorised Person) by %s and record the acknowledgement there.", name, ex.Exchange, fmtDay(v.Due)), "", RoleMaker, "partners", date)
	}
	if len(made) == 0 {
		if len(fix) > 0 {
			return "", nil, &pendingErr{"Records to fix before they can be reported: " + strings.Join(fix, ", "), "To fix"}
		}
		_, note := o.apWeekDone(ctx, tenant, v.Week, date)
		return "Nothing to report for the week of " + v.Label + firstOf(map[bool]string{true: " (" + note + ")"}[note != ""], ""), nil, nil
	}
	msg := "Files made for " + strings.Join(made, ", ") + ": upload each on the exchange's portal and record the acknowledgement on Branches & partners → AP client reporting"
	if len(fix) > 0 {
		msg += "; to fix: " + strings.Join(fix, ", ")
	}
	return "", files, &pendingErr{msg, "Waiting for upload"}
}

// ---- the monthly count reconciliation ----

// APCountLine is one AP's client count at an exchange.
type APCountLine struct {
	Partner    string   `json:"partner"`
	Name       string   `json:"name"`
	RegNo      string   `json:"reg_no"`
	Ledgerline int      `json:"ledgerline"` // clients mapped to it at the month's end who trade on the exchange
	Exchange   *int     `json:"exchange"`   // the count the exchange shows, as recorded
	Diff       int      `json:"difference"` // exchange − Ledgerline
	Missing    []string `json:"missing,omitempty"`
	Extra      []string `json:"extra,omitempty"`
	clients    []string
}

// APCountRecon is a recorded reconciliation of one exchange for a month.
type APCountRecon struct {
	ID       string        `json:"id"`
	Month    string        `json:"month"`
	Exchange string        `json:"exchange"`
	Lines    []APCountLine `json:"lines"`
	Matched  bool          `json:"matched"`
	Note     string        `json:"note,omitempty"`
	By       string        `json:"by"`
	At       string        `json:"at"`
}

// APCountExchange is an exchange's part of the month.
type APCountExchange struct {
	Exchange string        `json:"exchange"`
	Lines    []APCountLine `json:"lines"`
	Recorded *APCountRecon `json:"recorded,omitempty"`
}

// APCountView is the month's reconciliation.
type APCountView struct {
	Month     string            `json:"month"`
	Label     string            `json:"label"`
	Due       string            `json:"due"`
	Status    string            `json:"status"`
	Exchanges []APCountExchange `json:"exchanges"`
}

// APCountInput is the count an exchange shows for one AP, and optionally
// its list of the AP's clients.
type APCountInput struct {
	Partner string `json:"partner"`
	Count   *int   `json:"count"`
	Clients string `json:"clients,omitempty"` // client codes, separated by commas, spaces or lines
}

// apCountDue is Ledgerline's own date for the reconciliation: no circular
// sets one (NSE/COMP/63628 asks for the count to be kept current).
func apCountDue(month string) string { return addMonths(month+"-01", 1)[:8] + "10" }

func (o *Ops) apCountView(ctx context.Context, tenant, month string, write bool) (APCountView, error) {
	if _, err := time.Parse("2006-01", month); err != nil {
		return APCountView{}, invalid("the month is YYYY-MM")
	}
	e, err := o.apEnvFor(ctx, tenant, write)
	if err != nil {
		return APCountView{}, err
	}
	end := monthEnd(month)
	v := APCountView{Month: month, Label: monthLabel(month), Due: apCountDue(month), Exchanges: []APCountExchange{}}
	done := true
	for _, x := range memberExchanges(e.set) {
		ex := APCountExchange{Exchange: x, Lines: []APCountLine{}}
		by := map[string]*APCountLine{}
		for _, p := range mapValues(e.partners) {
			if isAP(p) && p.Active {
				by[p.Code] = &APCountLine{Partner: p.Code, Name: p.Name, RegNo: apRegFor(p, x)}
			}
		}
		for _, c := range e.clients {
			if !apOnExchange(c, x) || c.Status == "Closed" {
				continue
			}
			ap, _ := apAt(e.hist[c.UCC], end)
			if ap == "" {
				continue
			}
			l := by[ap]
			if l == nil {
				p := e.partners[ap]
				l = &APCountLine{Partner: ap, Name: p.Name, RegNo: apRegFor(p, x)}
				by[ap] = l
			}
			l.Ledgerline++
			l.clients = append(l.clients, c.UCC)
		}
		for _, k := range sortedKeys(by) {
			ex.Lines = append(ex.Lines, *by[k])
		}
		if r, _, err := docs.Get[APCountRecon](ctx, o.Docs, tenant, cAPCount, month+"|"+x); err == nil {
			ex.Recorded = &r
		} else if len(ex.Lines) > 0 {
			done = false
		}
		v.Exchanges = append(v.Exchanges, ex)
	}
	today := o.now().Format("2006-01-02")
	switch {
	case done:
		v.Status = "Done"
	case today > v.Due:
		v.Status = "Overdue"
	case today > end:
		v.Status = "Due"
	default:
		v.Status = "Upcoming"
	}
	return v, nil
}

// APCountReconFor is the month's reconciliation of each AP's client count.
func (o *Ops) APCountReconFor(ctx context.Context, tenant, actor, month string) (APCountView, error) {
	if err := o.require(ctx, tenant, actor, "Viewing the AP client reporting", RoleMaker, RoleChecker, RoleCompliance, RoleRisk); err != nil {
		return APCountView{}, err
	}
	if month == "" {
		month = prevMonth(o.now().Format("2006-01-02"))
	}
	return o.apCountView(ctx, tenant, month, true)
}

func splitCodes(s string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '\n' || r == '\r' || r == '\t' }) {
		if f = strings.ToUpper(strings.TrimSpace(f)); f != "" && !contains(out, f) {
			out = append(out, f)
		}
	}
	return out
}

// SaveAPCountRecon records an exchange's counts for the month against
// Ledgerline's. Differences need a note saying what was done about them.
func (o *Ops) SaveAPCountRecon(ctx context.Context, tenant, actor, month, exchange string, in []APCountInput, note string) (APCountView, error) {
	if err := o.require(ctx, tenant, actor, "Recording the AP client count reconciliation", RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return APCountView{}, err
	}
	v, err := o.apCountView(ctx, tenant, month, true)
	if err != nil {
		return v, err
	}
	x := strings.ToUpper(strings.TrimSpace(exchange))
	var ex *APCountExchange
	for i := range v.Exchanges {
		if v.Exchanges[i].Exchange == x {
			ex = &v.Exchanges[i]
		}
	}
	switch {
	case ex == nil:
		return v, invalid("the firm has no member code for %s in Settings", exchange)
	case o.now().Format("2006-01-02") <= monthEnd(month):
		return v, invalid("%s has not ended", monthLabel(month))
	}
	given := map[string]APCountInput{}
	for _, i := range in {
		given[strings.ToUpper(strings.TrimSpace(i.Partner))] = i
	}
	r := APCountRecon{ID: month + "|" + x, Month: month, Exchange: x, Lines: []APCountLine{}, Matched: true, Note: clip(strings.TrimSpace(note), 500), By: actor, At: o.now().Format(time.RFC3339)}
	for _, l := range ex.Lines {
		g, ok := given[l.Partner]
		if !ok || g.Count == nil || *g.Count < 0 {
			return v, invalid("give the number of clients %s shows for %s (%s)", x, l.Partner, l.Name)
		}
		n := *g.Count
		l.Exchange, l.Diff = &n, n-l.Ledgerline
		if list := splitCodes(g.Clients); len(list) > 0 {
			for _, u := range l.clients {
				if !contains(list, u) {
					l.Missing = append(l.Missing, u)
				}
			}
			for _, u := range list {
				if !contains(l.clients, u) {
					l.Extra = append(l.Extra, u)
				}
			}
		}
		r.Matched = r.Matched && l.Diff == 0 && len(l.Missing) == 0 && len(l.Extra) == 0
		r.Lines = append(r.Lines, l)
	}
	if !r.Matched && r.Note == "" {
		return v, invalid("the counts differ: say what was done about it (for example, the missing clients were uploaded on the exchange's portal)")
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cAPCount, r.ID, r, actor); err != nil {
		return v, err
	}
	o.log(ctx, tenant, actor, "ap.count.reconciled", r.ID, map[string]any{"matched": r.Matched, "aps": len(r.Lines)})
	o.closeWork(ctx, tenant, actor, "periodic", "apcount:"+month+":"+x, "reconciled")
	return o.apCountView(ctx, tenant, month, true)
}

// apCountDone is whether every exchange's count is reconciled for the month.
func (o *Ops) apCountDone(ctx context.Context, tenant, month string) (bool, string) {
	v, err := o.apCountView(ctx, tenant, month, false)
	if err != nil {
		return false, err.Error()
	}
	var parts []string
	for _, ex := range v.Exchanges {
		switch {
		case ex.Recorded != nil && ex.Recorded.Matched:
			parts = append(parts, ex.Exchange+": matched")
		case ex.Recorded != nil:
			parts = append(parts, ex.Exchange+": differences noted")
		case len(ex.Lines) == 0:
			parts = append(parts, ex.Exchange+": no APs")
		default:
			parts = append(parts, ex.Exchange+": to reconcile")
		}
	}
	return v.Status == "Done", strings.Join(parts, "; ")
}

// runAPCount is the monthly task: it needs the exchanges' counts, so it
// queues the reconciliation for an Ops maker.
func (o *Ops) runAPCount(ctx context.Context, tenant, month, date string) (string, []PeriodicFile, error) {
	v, err := o.apCountView(ctx, tenant, month, true)
	if err != nil {
		return "", nil, err
	}
	var todo []string
	for _, ex := range v.Exchanges {
		if ex.Recorded != nil || len(ex.Lines) == 0 {
			continue
		}
		todo = append(todo, ex.Exchange)
		o.raiseWork(ctx, tenant, "periodic", "apcount:"+month+":"+ex.Exchange, "Reconcile each AP's client count with "+ex.Exchange+" · "+v.Label,
			"Note the number of clients "+ex.Exchange+"'s portal shows for each AP at the end of "+v.Label+" and record it on Branches & partners → AP client reporting (NSE/COMP/63628 asks for the count to be kept current in the exchange's records).", "", RoleMaker, "partners", date)
	}
	if len(todo) == 0 {
		return "No AP to reconcile", nil, nil
	}
	return "", nil, &pendingErr{"Waiting for the exchanges' counts: " + strings.Join(todo, ", "), "Waiting for the counts"}
}

// ---- self-inspection ----

// siAPContacts: no AP's own mobile or email on a client's record
// (NSE/COMP/63628, Annexure A item 28).
func siAPContacts(e *siEnv) (siOut, error) {
	ps, err := docs.List[Partner](e.ctx, e.o.Docs, e.tenant, cPartner, "")
	if err != nil {
		return siOut{}, err
	}
	aps, withContact := 0, 0
	for _, p := range ps {
		if isAP(p) {
			aps++
			if p.Mobile != "" || p.Email != "" {
				withContact++
			}
		}
	}
	switch {
	case aps == 0:
		return siSkip("Not applicable: the firm has no authorised persons on Branches & partners.")
	case withContact == 0:
		return siSkip("Not checked: no authorised person's own mobile or email is on Branches & partners; add them so they can be checked.")
	}
	var s siOut
	s.input("Authorised persons", e.date)
	s.input("Clients", e.date)
	cs := mapValues(e.clients)
	s.checked = withContact
	for _, h := range apContactHits(cs, ps) {
		own := ""
		if h.OwnClient {
			own = " (one of its clients)"
		}
		s.bad(fmt.Sprintf("AP %s · its own %s is on the record of %s%s", h.Partner, h.Field, siClient(h.UCC), own))
	}
	s.summary = fmt.Sprintf("%d APs' own mobile and email checked against %d clients; %d records carry them.", withContact, len(cs), s.found)
	return s, nil
}

// siAPMapping: the latest week whose due date has passed is reported to
// every exchange (NSE/COMP/49509; MSE/MEM/15275/2024 item 15).
func siAPMapping(e *siEnv) (siOut, error) {
	ps, _ := docs.List[Partner](e.ctx, e.o.Docs, e.tenant, cPartner, "")
	aps := 0
	for _, p := range ps {
		if isAP(p) {
			aps++
		}
	}
	st, _, err := docs.Get[APMapState](e.ctx, e.o.Docs, e.tenant, cAPMapState, "main")
	switch {
	case aps == 0 && err != nil:
		return siSkip("Not applicable: the firm has no authorised persons on Branches & partners.")
	case err != nil:
		return siSkip("Nothing to check yet: " + errAPNotStarted.Error() + ".")
	}
	sun := apLastWeek(e.date)
	if apWeekDue(e.set, sun) >= e.date {
		sun = addDays(sun, -7)
	}
	if st.Started > sun {
		return siSkip("Nothing to check yet: the register of clients' AP mappings started on " + fmtDay(st.Started) + ", after the last week that has fallen due.")
	}
	v, _, err := e.o.apMappingReport(e.ctx, e.tenant, sun, e.date, false)
	if err != nil {
		return siOut{}, err
	}
	var s siOut
	s.input("AP mappings", sun)
	for _, ex := range v.Exchanges {
		s.checked++
		for _, r := range ex.Records {
			switch r.State {
			case APInFile, APInFileCheck:
				s.bad(fmt.Sprintf("%s · %s · %s %s on %s · not in an upload recorded by %s", ex.Exchange, siClient(r.UCC), strings.ToLower(r.Kind), firstOf(r.To, r.From), fmtDay(r.ChangedOn), fmtDay(v.Due)))
			case APToFix:
				s.bad(fmt.Sprintf("%s · %s · held back: %s", ex.Exchange, siClient(r.UCC), r.Notes[0]))
			}
		}
	}
	if s.checked == 0 {
		return siSkip("Not applicable: the firm has no member codes in Settings.", s.inputs...)
	}
	s.summary = fmt.Sprintf("Week of %s (due %s): %d exchange%s checked; %d clients not reported or held back.", v.Label, fmtDay(v.Due), s.checked, plural(s.checked), s.found)
	return s, nil
}
