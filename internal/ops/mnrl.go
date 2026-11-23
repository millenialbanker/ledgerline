package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/xfile"
)

// ---- mobile numbers on DoT's revocation list (MNRL) ----
//
// DoT keeps the Mobile Number Revocation List: numbers disconnected (for
// non-use, on the subscriber's request, after failed re-verification and
// so on) that an operator may give to someone else. A client's registered
// mobile on it must be taken up with the client and corrected, or account
// alerts and one-time codes reach a stranger.
//
// The lists Ledgerline reads. None has a published layout, so each is read
// by its header names (config/formats; xfile/headers.go):
//   - NSE's "Report for disconnected Mobile numbers", daily at UCI Member
//     Portal > Download since NSE/ISC/72805 (13 Feb 2026): the firm's
//     client codes whose numbers are on the MNRL (nse-mnrl).
//   - DoT's MNRL on its Digital Intelligence Platform: every disconnected
//     number with the date and reason, near real time (dot-mnrl). Members
//     are to onboard DIP (NSE/COMP/74753, 17 Jun 2026; an API is possible)
//     and CDSL DPs to download and verify it daily
//     (CDSL/OPS/DP/POLCY/2026/517, 30 Jul 2026).
//   - NSDL's DP-ID-wise MNRL matches on e-PASS, and from July 2026 its
//     monthly "Reactivation Data of DoT" (NSDL/POLICY/2026/0090, 16 Jun
//     2026): numbers reconnected to the same person, for which only the
//     suspension levied because of the MNRL is lifted.
//
// What a match does (NSE/ISC/45168 of 29 Jul 2020 and NSE/ISC/47834 of
// 31 Mar 2021: match the list to clients, confirm the right number with
// the client, update the back office and the UCC at once; NSE stops its
// own messages to listed numbers):
//   - it flags the client (Client.MNRL) and holds SMS and WhatsApp to the
//     number, the WhatsApp bot included (email to the registered address
//     goes on); an Ops maker gets a work item to confirm the client's
//     number through another registered channel;
//   - a match is New, Contacted, Number updated (set when the client's
//     mobile changes by an approved change request), Not the client's
//     number, or Reactivated (on a reactivation list, or confirmed by DoT
//     after an action taken report); every change is kept in its history;
//   - the new number goes to the exchanges as a UCC modification by the
//     UCC paths (nseuci.go, bseucc.go, mcxucc.go send a changed record
//     again) and, for a CDSL account, as a BO modify (cdslbo.go captures
//     an approved mobile change); an NSDL account is listed for the change
//     in NSDL's system.

const (
	cMNRLList  = "mnrllist"  // key: ML-nnnnnn
	cMNRLMatch = "mnrlmatch" // key: MN-nnnnnn
	cMNRLHold  = "mnrlhold"  // key: the 10-digit number

	MNRLNew         = "New"
	MNRLContacted   = "Contacted"
	MNRLUpdated     = "Number updated"
	MNRLNotClients  = "Not the client's number"
	MNRLReactivated = "Reactivated"

	// The lists (xfile layout IDs).
	MNRLListNSE  = "nse-mnrl"
	MNRLListDoT  = "dot-mnrl"
	MNRLListNSDL = "nsdl-mnrl"
	MNRLListReac = "nsdl-mnrl-reactivation"

	// mnrlActDays: business days to take a new match up with the client.
	// NSE asks for the number to be updated "immediately"; the days are the
	// firm's practice (to confirm).
	mnrlActDays = 3
	// mnrlContactDays: days a contacted client may take to give a number
	// before the self-inspection reports it (the firm's practice).
	mnrlContactDays = 30

	wMNRL = "mnrl" // work process
)

// MNRLStatuses are a match's statuses, in order.
var MNRLStatuses = []string{MNRLNew, MNRLContacted, MNRLUpdated, MNRLNotClients, MNRLReactivated}

// mnrlOpen: the match still holds messages and waits for someone.
func mnrlOpen(s string) bool { return s == MNRLNew || s == MNRLContacted }

// MNRLFlag marks a client whose registered mobile, or a joint holder's, is
// on DoT's revocation list.
type MNRLFlag struct {
	Match  string `json:"match"`
	Mobile string `json:"mobile"`
	Holder string `json:"holder"` // First, Second or Third
	Since  string `json:"since"`
}

// MNRLSource is one list Ledgerline reads, for the screen.
type MNRLSource struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	From    string `json:"from"`  // where it is downloaded
	For     string `json:"for"`   // Trading accounts, Demat accounts
	When    string `json:"when"`  // how often
	Basis   string `json:"basis"` // the circular
	Applies bool   `json:"applies"`
	Last    string `json:"last,omitempty"` // date of the latest list loaded
}

// mnrlSources are the lists, in the order the screen shows them.
var mnrlSources = []MNRLSource{
	{ID: MNRLListNSE, Title: "NSE: report for disconnected mobile numbers", From: "NSE UCI Member Portal > Download > Report for disconnected Mobile numbers", For: "Trading accounts", When: "Daily", Basis: "NSE/ISC/72805 of 13 Feb 2026"},
	{ID: MNRLListDoT, Title: "DoT: Mobile Number Revocation List (DIP)", From: "DoT's Digital Intelligence Platform (diu.sancharsaathi.gov.in), MNRL download", For: "Trading and demat accounts", When: "Daily", Basis: "NSE/COMP/74753 of 17 Jun 2026; CDSL/OPS/DP/POLCY/2026/517 of 30 Jul 2026"},
	{ID: MNRLListNSDL, Title: "NSDL: MNRL matches (e-PASS)", From: "e-PASS > iAssist > Download > View/Search - BSDA/MNRL/Email Bounce, file type MNRL", For: "Demat accounts (NSDL)", When: "Daily", Basis: "NSDL/POLICY/2026/0090 of 16 Jun 2026"},
	{ID: MNRLListReac, Title: "NSDL: Reactivation Data of DoT (e-PASS)", From: "e-PASS > iAssist > Download > View/Search - BSDA/MNRL/Email Bounce, file type Reactivation Data of DoT", For: "Demat accounts (NSDL)", When: "Monthly, from July 2026", Basis: "NSDL/POLICY/2026/0090 of 16 Jun 2026"},
}

func mnrlSourceOf(id string) (MNRLSource, bool) {
	for _, s := range mnrlSources {
		if s.ID == id {
			return s, true
		}
	}
	return MNRLSource{}, false
}

// mnrlSourceName is the list's issuer, as a match shows it.
var mnrlSourceName = map[string]string{MNRLListNSE: "NSE", MNRLListDoT: "DoT", MNRLListNSDL: "NSDL", MNRLListReac: "NSDL reactivation"}

// MNRLList is one list as loaded.
type MNRLList struct {
	ID      string   `json:"id"`
	Format  string   `json:"format"`
	Source  string   `json:"source"`
	File    string   `json:"file"`
	FileID  string   `json:"file_id,omitempty"`
	Date    string   `json:"date"` // the list's date
	By      string   `json:"by"`
	At      string   `json:"at"`
	Rows    int      `json:"rows"`
	New     int      `json:"new"`     // new matches
	Again   int      `json:"again"`   // matches already open, listed again
	Cleared int      `json:"cleared"` // matches reactivated
	Skipped int      `json:"skipped"` // rows of nobody of the firm's, or not a number
	Issues  int      `json:"issues"`  // rows that did not fit the layout
	Notes   []string `json:"notes,omitempty"`
}

// MNRLEvent is one change of a match.
type MNRLEvent struct {
	At     string `json:"at"`
	By     string `json:"by"`
	Status string `json:"status"`
	Note   string `json:"note,omitempty"`
}

// MNRLMatch is a client's number found on a list.
type MNRLMatch struct {
	ID     string `json:"id"`
	UCC    string `json:"ucc"`
	Name   string `json:"name"`
	Holder string `json:"holder"` // First (the client), Second or Third (a joint holder of a demat account)
	// HolderName is a joint holder's name; Account and Depository the demat
	// account, for a depository's list or a joint holder.
	HolderName string `json:"holder_name,omitempty"`
	Account    string `json:"account,omitempty"`
	Depository string `json:"depository,omitempty"`
	Mobile     string `json:"mobile"` // 10 digits
	// OnRecord: the number was the one on Ledgerline's record when it was
	// found (an NSDL list may give the depository's record instead).
	OnRecord  bool     `json:"on_record"`
	Source    string   `json:"source"` // NSE, DoT, NSDL
	Format    string   `json:"format"`
	List      string   `json:"list"`
	ListDate  string   `json:"list_date"`
	File      string   `json:"file,omitempty"`
	Revoked   string   `json:"revoked,omitempty"` // the date of disconnection, as the list gives it
	Reason    string   `json:"reason,omitempty"`  // DoT's reason, when given
	Status    string   `json:"status"`
	Opened    string   `json:"opened"` // YYYY-MM-DD
	Due       string   `json:"due"`    // to take up with the client by
	Closed    string   `json:"closed,omitempty"`
	NewMobile string   `json:"new_mobile,omitempty"` // masked
	Change    string   `json:"change,omitempty"`     // the change request that updated it
	Seen      []string `json:"seen,omitempty"`       // later lists that had it again
	// For a CDSL account (CDSL/OPS/DP/POLCY/2025/618, kycgate_cdsl.go):
	// the 30-day notice to register a valid mobile, when it expires, and
	// the debit-freeze request (reason 31) raised after it.
	NoticeOn      string      `json:"notice_on,omitempty"`
	NoticeExpiry  string      `json:"notice_expiry,omitempty"`
	FreezeRequest string      `json:"freeze_request,omitempty"`
	History       []MNRLEvent `json:"history"`
}

// mnrlHold is a number whose SMS and WhatsApp are held, with the open
// matches (by client) that hold it.
type mnrlHold struct {
	Mobile string            `json:"mobile"`
	Holds  map[string]string `json:"holds"`
}

// normMobile is an Indian mobile number as its 10 digits: +91 or 91, a
// leading 0, spaces, hyphens and brackets are removed ("" when it is not
// one).
func normMobile(s string) string {
	d := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
	switch {
	case len(d) == 12 && strings.HasPrefix(d, "91"):
		d = d[2:]
	case len(d) == 11 && strings.HasPrefix(d, "0"):
		d = d[1:]
	case len(d) == 13 && strings.HasPrefix(d, "091"):
		d = d[3:]
	case len(d) == 14 && strings.HasPrefix(d, "0091"):
		d = d[4:]
	}
	if !reMobile.MatchString(d) {
		return ""
	}
	return d
}

// mnrlHolder is a number Ledgerline holds, and whose it is.
type mnrlHolder struct {
	UCC, Name, Holder, HolderName, Account, Depository, Mobile string
}

func (h mnrlHolder) key() string { return h.UCC + "|" + h.Holder + "|" + h.Mobile }

func (m MNRLMatch) key() string { return m.UCC + "|" + m.Holder + "|" + m.Mobile }

// mnrlIndex is every mobile number on Ledgerline's records: each client's
// (trading and DP-only) and each joint holder's of a CDSL account
// (cdslbomap.go keeps them), by number.
func (o *Ops) mnrlIndex(ctx context.Context, tenant string) (map[string][]mnrlHolder, map[string]Client) {
	cs, _ := o.ClientMap(ctx, tenant)
	by := map[string][]mnrlHolder{}
	for _, u := range sortedKeys(cs) {
		c := cs[u]
		if c.Status == "Closed" || c.Status == "Rejected" {
			continue
		}
		if m := normMobile(c.Mobile); m != "" {
			by[m] = append(by[m], mnrlHolder{UCC: u, Name: c.Name, Holder: "First", Mobile: m})
		}
	}
	ds, _ := docs.List[CDSLBODetail](ctx, o.Docs, tenant, cCDSLBODetail, "")
	for _, d := range ds {
		c, ok := cs[d.UCC]
		if !ok || c.Status == "Closed" || c.Status == "Rejected" {
			continue
		}
		acct := ""
		for _, x := range demats(c) {
			if x.Depository == "CDSL" && acct == "" {
				acct = x.ID
			}
		}
		for i, h := range d.Holders {
			if m := normMobile(h.Mobile); m != "" && h.DiedOn == "" {
				by[m] = append(by[m], mnrlHolder{UCC: c.UCC, Name: c.Name, Holder: []string{"Second", "Third"}[min(i, 1)], HolderName: h.Name, Account: acct, Depository: "CDSL", Mobile: m})
			}
		}
	}
	return by, cs
}

// mnrlHeld says whether SMS and WhatsApp to a number are held, and by
// which match: a revoked number is held whoever it is sent for.
func (o *Ops) mnrlHeld(ctx context.Context, tenant, number string) (string, bool) {
	m := normMobile(number)
	if m == "" {
		return "", false
	}
	h, _, err := docs.Get[mnrlHold](ctx, o.Docs, tenant, cMNRLHold, m)
	if err != nil || len(h.Holds) == 0 {
		return "", false
	}
	return h.Holds[sortedKeys(h.Holds)[0]], true
}

// mnrlHeldNote is what a held message says.
func mnrlHeldNote(match string) string {
	return "held: the number is on DoT's revocation list (" + match + "); it is sent again only to a number the client confirms"
}

func (o *Ops) mnrlSetHold(ctx context.Context, tenant, mobile, ucc, match string, on bool) {
	h, _, _ := docs.GetOr(ctx, o.Docs, tenant, cMNRLHold, mobile, mnrlHold{Mobile: mobile})
	if h.Holds == nil {
		h.Holds = map[string]string{}
	}
	switch {
	case on:
		h.Holds[ucc] = match
	case h.Holds[ucc] == match:
		delete(h.Holds, ucc)
	default:
		return
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cMNRLHold, mobile, h, SystemActor)
}

// mnrlFlag sets or clears a match's flag on the client.
func (o *Ops) mnrlFlag(ctx context.Context, tenant, actor string, m MNRLMatch, on bool) {
	_, _ = docs.Update(ctx, o.Docs, tenant, cClient, m.UCC, actor, func(c *Client) error {
		var keep []MNRLFlag
		for _, f := range c.MNRL {
			if f.Match != m.ID {
				keep = append(keep, f)
			}
		}
		if on {
			keep = append(keep, MNRLFlag{Match: m.ID, Mobile: m.Mobile, Holder: m.Holder, Since: m.Opened})
		}
		c.MNRL = keep
		return nil
	})
}

// mnrlWork is the Ops maker's work item for a new match.
func (o *Ops) mnrlWork(ctx context.Context, tenant string, m MNRLMatch) {
	who := m.UCC
	if m.Holder != "First" {
		who = fmt.Sprintf("%s's %s holder", m.UCC, strings.ToLower(m.Holder))
	}
	detail := fmt.Sprintf("The mobile %s registered for %s is on the %s list of %s. SMS and WhatsApp to it are held. Contact the client through another registered channel (email, letter or the branch), confirm the right number and raise a mobile change with the client's request; then mark the match on Clients & onboarding → Revoked mobiles.",
		maskMobile(m.Mobile), who, m.Source, fmtDay(m.ListDate))
	if m.Holder != "First" || m.Depository == "NSDL" {
		detail = fmt.Sprintf("The mobile %s of %s (demat account %s) is on the %s list of %s. Contact the holder through another registered channel and change the number at the depository; then mark the match on Clients & onboarding → Revoked mobiles.",
			maskMobile(m.Mobile), who, cdslMaskBO(m.Account), m.Source, fmtDay(m.ListDate))
	}
	o.workDue(ctx, tenant, wMNRL, m.ID, "Confirm the mobile of "+who+": it is on DoT's revocation list", detail, m.UCC, RoleMaker, "mnrl", m.Opened, m.Due+" 17:00")
}

// mnrlDue is the date a match found on date is to be taken up by.
func mnrlDue(set Settings, date string) string {
	d := date
	for i := 0; i < mnrlActDays; i++ {
		d = set.NextBusinessDay(d)
	}
	return d
}

// mnrlClose closes an open match with status and lifts its hold.
func (o *Ops) mnrlClose(ctx context.Context, tenant, actor string, m *MNRLMatch, status, note string) error {
	now := o.now().Format(time.RFC3339)
	m.Status, m.Closed = status, now
	m.History = append(m.History, MNRLEvent{At: now, By: actor, Status: status, Note: note})
	if err := docs.Upsert(ctx, o.Docs, tenant, cMNRLMatch, m.ID, *m, actor); err != nil {
		return err
	}
	o.mnrlFlag(ctx, tenant, actor, *m, false)
	o.mnrlSetHold(ctx, tenant, m.Mobile, m.UCC, m.ID, false)
	o.closeWork(ctx, tenant, actor, wMNRL, m.ID, status)
	o.log(ctx, tenant, actor, "mnrl.status", m.ID, map[string]any{"ucc": m.UCC, "status": status, "note": note})
	return nil
}

// ---- loading a list ----

// MNRLLoadReq is a list loaded on the screen.
type MNRLLoadReq struct {
	Format string `json:"format"` // the list (layout ID)
	Name   string `json:"name"`   // the file's name
	Date   string `json:"date"`   // the list's date (empty: today)
}

// LoadMNRLList reads a list the person chose and acts on its matches.
func (o *Ops) LoadMNRLList(ctx context.Context, tenant, actor string, req MNRLLoadReq, data []byte) (MNRLList, error) {
	if err := o.require(ctx, tenant, actor, "Loading revocation lists", RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return MNRLList{}, err
	}
	if _, ok := mnrlSourceOf(req.Format); !ok {
		return MNRLList{}, invalid("choose the list: NSE's report, DoT's MNRL, NSDL's MNRL or NSDL's reactivation data")
	}
	lf, err := o.openListFile(ctx, tenant, req.Format, req.Name, req.Date, data)
	if err != nil {
		return MNRLList{}, err
	}
	l, err := o.applyMNRLFile(ctx, tenant, actor, lf)
	if err != nil {
		return l, err
	}
	o.log(ctx, tenant, actor, "xfile.received", lf.name, map[string]any{"format": lf.spec.ID, "rows": l.Rows, "problems": l.Issues, "file": lf.fileID, "loaded": "Revoked mobiles"})
	o.noteDailyFile(ctx, tenant, actor, lf.spec.ID, lf.name, "")
	return l, nil
}

// applyMNRLFile reads a list and records it with what it found.
func (o *Ops) applyMNRLFile(ctx context.Context, tenant, actor string, lf *listFile) (MNRLList, error) {
	n, err := o.next(ctx, tenant, "mnrllist", actor)
	if err != nil {
		return MNRLList{}, err
	}
	l := MNRLList{ID: fmt.Sprintf("ML-%06d", n), Format: lf.spec.ID, Source: firstOf(mnrlSourceName[lf.spec.ID], lf.spec.ID), File: lf.name, FileID: lf.fileID, Date: lf.date, By: actor, At: o.now().Format(time.RFC3339)}
	if err := o.applyMNRL(ctx, tenant, actor, lf.spec, &l, lf.feed); err != nil {
		return l, err
	}
	l.Issues = lf.issues
	if _, err := docs.Put(ctx, o.Docs, tenant, cMNRLList, l.ID, l, 0, actor); err != nil {
		return l, err
	}
	o.log(ctx, tenant, actor, "mnrl.list.loaded", l.ID, map[string]any{"source": l.Source, "file": l.File, "date": l.Date, "rows": l.Rows, "new": l.New, "again": l.Again, "reactivated": l.Cleared, "skipped": l.Skipped, "problems": l.Issues})
	// The day's check asked for this list (Periodic work).
	for task, formats := range mnrlTaskLists {
		if contains(formats, l.Format) {
			o.closeWork(ctx, tenant, actor, "periodic", task+":"+l.Date, "loaded "+l.ID)
		}
	}
	return l, nil
}

// summary says in a line what a list did.
func (l MNRLList) summary() string {
	s := fmt.Sprintf("%d row%s: %d new match%s", l.Rows, plural(l.Rows), l.New, map[bool]string{true: "es", false: ""}[l.New != 1])
	if l.Again > 0 {
		s += fmt.Sprintf(", %d already open", l.Again)
	}
	if l.Cleared > 0 {
		s += fmt.Sprintf(", %d reactivated", l.Cleared)
	}
	if l.Issues > 0 {
		s += fmt.Sprintf(", %d row%s left out as they do not fit the layout", l.Issues, plural(l.Issues))
	}
	if len(l.Notes) > 0 {
		s += "; " + strings.Join(l.Notes, "; ")
	}
	return s
}

// applyMNRL matches a list's rows with the numbers on record and acts on
// each match: new matches are flagged, held and queued; matches already
// open are noted as listed again; a reactivation list clears them.
func (o *Ops) applyMNRL(ctx context.Context, tenant, actor string, s *xfile.Spec, l *MNRLList, feed func(func(xfile.Row)) error) error {
	set, _ := o.Settings(ctx, tenant)
	idx, cs := o.mnrlIndex(ctx, tenant)
	var bo map[string]string
	if s.ID == MNRLListNSDL || s.ID == MNRLListReac {
		bo = o.boClients(ctx, tenant)
	}
	all, _ := docs.List[MNRLMatch](ctx, o.Docs, tenant, cMNRLMatch, "")
	open := map[string]*MNRLMatch{}
	for i := range all {
		if mnrlOpen(all[i].Status) {
			open[all[i].key()] = &all[i]
		}
	}
	type cand struct {
		h                mnrlHolder
		revoked, reason  string
		onRecord         bool
		reactivation     bool
		reactivationDate string
	}
	var cands []cand
	notes := map[string]int{}
	nsdlBO := func(r xfile.Row) string {
		cl := normDematID(r.Get("client"))
		if strings.HasPrefix(cl, "IN") && len(cl) == 16 {
			return cl
		}
		dp := normDematID(r.Get("dpid"))
		if dp != "" && !strings.HasPrefix(dp, "IN") {
			dp = "IN" + dp
		}
		if dp == "" || cl == "" {
			return ""
		}
		return dp + cl
	}
	err := feed(func(r xfile.Row) {
		l.Rows++
		raw := strings.TrimSpace(r.Get("mobile"))
		m := normMobile(raw)
		switch s.ID {
		case MNRLListNSE:
			u := strings.ToUpper(strings.TrimSpace(r.Get("ucc")))
			c, ok := cs[u]
			if !ok || c.Status == "Closed" || c.Status == "Rejected" {
				notes["a client code that is not an open client of the firm"]++
				return
			}
			cur := normMobile(c.Mobile)
			switch {
			case raw != "" && m == "":
				notes["no 10-digit Indian mobile number"]++
				return
			case m == "" && cur == "":
				notes["a client with no mobile on record"]++
				return
			case m == "":
				m = cur
			case m != cur:
				// NSE's UCC record has a number the back office no longer
				// has: nothing to hold here, but the UCC may need sending.
				notes["a number that is no longer the client's mobile on record (send the UCC again if NSE still has it)"]++
				return
			}
			cands = append(cands, cand{h: mnrlHolder{UCC: u, Name: c.Name, Holder: "First", Mobile: m}, revoked: r.Get("date"), onRecord: true})
		case MNRLListDoT:
			if m == "" {
				notes["no 10-digit Indian mobile number"]++
				return
			}
			hs := idx[m]
			if len(hs) == 0 {
				return // not a number of the firm's clients: the usual case
			}
			for _, h := range hs {
				cands = append(cands, cand{h: h, revoked: r.Get("date"), reason: r.Get("reason"), onRecord: true})
			}
		case MNRLListNSDL:
			id := nsdlBO(r)
			u, ok := bo[id]
			if id == "" || !ok {
				notes["a demat account that is not one of the firm's clients'"]++
				return
			}
			c := cs[u]
			if c.UCC == "" || c.Status == "Closed" || c.Status == "Rejected" {
				notes["a demat account of no open client"]++
				return
			}
			cur := normMobile(c.Mobile)
			if raw != "" && m == "" {
				notes["no 10-digit Indian mobile number"]++
				return
			}
			holder := "First"
			switch hn := strings.ToLower(strings.TrimSpace(r.Get("holder"))); {
			case hn == "2" || strings.HasPrefix(hn, "second") || strings.HasPrefix(hn, "sh"):
				holder = "Second"
			case hn == "3" || strings.HasPrefix(hn, "third") || strings.HasPrefix(hn, "th"):
				holder = "Third"
			}
			if m == "" {
				m = cur
			}
			if m == "" {
				notes["no number in the row and none on record"]++
				return
			}
			cands = append(cands, cand{h: mnrlHolder{UCC: u, Name: c.Name, Holder: holder, HolderName: map[bool]string{true: r.Get("name")}[holder != "First"], Account: id, Depository: "NSDL", Mobile: m}, revoked: r.Get("date"), onRecord: holder == "First" && m == cur})
		case MNRLListReac:
			if m == "" {
				notes["no 10-digit Indian mobile number"]++
				return
			}
			cands = append(cands, cand{h: mnrlHolder{Mobile: m, Account: nsdlBO(r)}, reactivation: true, reactivationDate: r.Get("date")})
		}
	})
	if err != nil {
		return err
	}
	now := o.now()
	today := now.Format("2006-01-02")
	seen := map[string]bool{}
	for _, c := range cands {
		if c.reactivation {
			for _, k := range sortedKeys(open) {
				m := open[k]
				if m.Mobile != c.h.Mobile || !mnrlOpen(m.Status) || (c.h.Account != "" && m.Account != "" && normDematID(m.Account) != c.h.Account) {
					continue
				}
				note := "On " + firstOf(mnrlSourceName[s.ID], s.ID) + " list of " + fmtDay(l.Date) + ": reconnected to the same person"
				if d := strings.TrimSpace(c.reactivationDate); d != "" {
					note += " (" + d + ")"
				}
				if err := o.mnrlClose(ctx, tenant, actor, m, MNRLReactivated, note+". Only the hold placed for the MNRL is lifted."); err != nil {
					return err
				}
				l.Cleared++
			}
			continue
		}
		k := c.h.key()
		if seen[k] {
			continue
		}
		seen[k] = true
		if m := open[k]; m != nil {
			l.Again++
			if len(m.Seen) < 30 {
				m.Seen = append(m.Seen, l.ID)
			}
			_ = docs.Upsert(ctx, o.Docs, tenant, cMNRLMatch, m.ID, *m, actor)
			continue
		}
		n, err := o.next(ctx, tenant, "mnrl", actor)
		if err != nil {
			return err
		}
		m := MNRLMatch{ID: fmt.Sprintf("MN-%06d", n), UCC: c.h.UCC, Name: c.h.Name, Holder: c.h.Holder, HolderName: c.h.HolderName, Account: c.h.Account, Depository: c.h.Depository,
			Mobile: c.h.Mobile, OnRecord: c.onRecord, Source: firstOf(mnrlSourceName[s.ID], s.ID), Format: s.ID, List: l.ID, ListDate: l.Date, File: l.File,
			Revoked: strings.TrimSpace(c.revoked), Reason: strings.TrimSpace(c.reason), Status: MNRLNew, Opened: today, Due: mnrlDue(set, today),
			History: []MNRLEvent{{At: now.Format(time.RFC3339), By: actor, Status: MNRLNew, Note: "On the " + firstOf(mnrlSourceName[s.ID], s.ID) + " list of " + fmtDay(l.Date) + " (" + l.File + "); SMS and WhatsApp to the number held"}}}
		if _, err := docs.Put(ctx, o.Docs, tenant, cMNRLMatch, m.ID, m, 0, actor); err != nil {
			return err
		}
		o.mnrlFlag(ctx, tenant, actor, m, true)
		o.mnrlSetHold(ctx, tenant, m.Mobile, m.UCC, m.ID, true)
		o.mnrlWork(ctx, tenant, m)
		o.log(ctx, tenant, actor, "mnrl.matched", m.ID, map[string]any{"ucc": m.UCC, "holder": m.Holder, "source": m.Source, "list": l.ID})
		open[k] = &m
		l.New++
	}
	for _, k := range sortedKeys(notes) {
		l.Skipped += notes[k]
		l.Notes = append(l.Notes, fmt.Sprintf("%d row%s with %s", notes[k], plural(notes[k]), k))
	}
	return nil
}

// ---- the person's actions ----

// SetMNRLStatus records what was done about a match: Contacted, Not the
// client's number, Reactivated or (for a number changed at the
// depository) Number updated, with a note.
func (o *Ops) SetMNRLStatus(ctx context.Context, tenant, actor, id, status, note string) (MNRLMatch, error) {
	if err := o.require(ctx, tenant, actor, "Working revoked mobile numbers", RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return MNRLMatch{}, err
	}
	m, _, err := docs.Get[MNRLMatch](ctx, o.Docs, tenant, cMNRLMatch, id)
	if err != nil {
		return m, err
	}
	note = strings.TrimSpace(note)
	if !mnrlOpen(m.Status) {
		return m, fmt.Errorf("%w: %s is already %s", ErrState, id, strings.ToLower(m.Status))
	}
	if len(note) < 4 {
		return m, invalid("say what was done: how the client was reached, or what the check found")
	}
	switch status {
	case MNRLContacted:
		now := o.now().Format(time.RFC3339)
		m.Status = MNRLContacted
		m.History = append(m.History, MNRLEvent{At: now, By: actor, Status: status, Note: note})
		if err := docs.Upsert(ctx, o.Docs, tenant, cMNRLMatch, m.ID, m, actor); err != nil {
			return m, err
		}
		o.log(ctx, tenant, actor, "mnrl.status", m.ID, map[string]any{"ucc": m.UCC, "status": status, "note": note})
		return m, nil
	case MNRLUpdated:
		if m.Holder == "First" && m.OnRecord {
			if c, err := o.Client(ctx, tenant, m.UCC); err == nil && normMobile(c.Mobile) == m.Mobile {
				return m, invalid("the client's mobile on record is still the listed number: raise a mobile change on Change requests with the client's request; the match closes when it is approved")
			}
		}
	case MNRLNotClients, MNRLReactivated:
	default:
		return m, invalid("status must be %s, %s, %s or %s", MNRLContacted, MNRLUpdated, MNRLNotClients, MNRLReactivated)
	}
	if err := o.mnrlClose(ctx, tenant, actor, &m, status, note); err != nil {
		return m, err
	}
	return m, nil
}

// mnrlSync closes as Number updated every open match whose number is no
// longer on the record (the client's mobile was changed, or the joint
// holder's), and lifts its hold. It runs when a mobile change is
// approved, when the screen is opened and with the daily task.
func (o *Ops) mnrlSync(ctx context.Context, tenant, actor string) int {
	ms, _ := docs.List[MNRLMatch](ctx, o.Docs, tenant, cMNRLMatch, "")
	var open []MNRLMatch
	for _, m := range ms {
		if mnrlOpen(m.Status) && m.OnRecord {
			open = append(open, m)
		}
	}
	if len(open) == 0 {
		return 0
	}
	cs, _ := o.ClientMap(ctx, tenant)
	var chs []ChangeRequest
	n := 0
	for i := range open {
		m := &open[i]
		c, ok := cs[m.UCC]
		if !ok {
			continue
		}
		cur := normMobile(c.Mobile)
		if m.Holder != "First" {
			d := o.CDSLBODetailFor(ctx, tenant, m.UCC)
			k := map[string]int{"Second": 0, "Third": 1}[m.Holder]
			cur = ""
			if k < len(d.Holders) {
				cur = normMobile(d.Holders[k].Mobile)
			}
		}
		if cur == m.Mobile {
			continue
		}
		note := "The number on record changed"
		if cur != "" {
			m.NewMobile = maskMobile(cur)
			note = "The mobile on record is now " + m.NewMobile
		}
		if m.Holder == "First" {
			if chs == nil {
				chs, _ = docs.List[ChangeRequest](ctx, o.Docs, tenant, cChange, "")
			}
			for _, ch := range chs {
				if ch.UCC == m.UCC && ch.Kind == "Mobile" && ch.Status == "Approved" && ch.Decided >= m.Opened && ch.ID > m.Change {
					m.Change = ch.ID
				}
			}
			if m.Change != "" {
				note += " (change " + m.Change + ")"
			}
		}
		if o.mnrlClose(ctx, tenant, actor, m, MNRLUpdated, note) == nil {
			n++
		}
	}
	return n
}

// ---- the screen ----

// MNRLRow is a match on the screen, the number masked.
type MNRLRow struct {
	MNRLMatch
	Overdue bool `json:"overdue"`
	// Follow is what still has to happen elsewhere after the number was
	// updated: the UCC modification at each exchange, the depository.
	Follow []string `json:"follow,omitempty"`
}

// MNRLView is Clients & onboarding → Revoked mobiles.
type MNRLView struct {
	Date     string         `json:"date"`
	Counts   map[string]int `json:"counts"`
	Open     int            `json:"open"`
	Overdue  int            `json:"overdue"`
	Held     int            `json:"held"` // numbers whose messages are held
	Matches  []MNRLRow      `json:"matches"`
	Lists    []MNRLList     `json:"lists"`
	Sources  []MNRLSource   `json:"sources"`
	Statuses []string       `json:"statuses"`
	CanWork  bool           `json:"can_work"`
}

// MNRL builds the screen for a date.
func (o *Ops) MNRL(ctx context.Context, tenant, actor, date string) (MNRLView, error) {
	v := MNRLView{Date: date, Counts: map[string]int{}, Matches: []MNRLRow{}, Lists: []MNRLList{}, Statuses: MNRLStatuses}
	if err := o.require(ctx, tenant, actor, "Viewing revoked mobile numbers", RoleMaker, RoleChecker, RoleCompliance, RoleRisk); err != nil {
		return v, err
	}
	if !validDate(date) {
		return v, invalid("give a date (YYYY-MM-DD)")
	}
	v.CanWork = o.require(ctx, tenant, actor, "Working revoked mobile numbers", RoleMaker, RoleChecker, RoleCompliance) == nil
	if v.CanWork {
		o.mnrlSync(ctx, tenant, SystemActor)
	}
	ms, err := docs.List[MNRLMatch](ctx, o.Docs, tenant, cMNRLMatch, "")
	if err != nil {
		return v, err
	}
	for _, s := range MNRLStatuses {
		v.Counts[s] = 0
	}
	cs, _ := o.ClientMap(ctx, tenant)
	regs := o.listUCCRegs(ctx, tenant)
	held := map[string]bool{}
	for _, m := range ms {
		r := MNRLRow{MNRLMatch: m}
		r.Mobile = maskMobile(m.Mobile)
		r.Account = cdslMaskBO(m.Account)
		v.Counts[m.Status]++
		if mnrlOpen(m.Status) {
			v.Open++
			held[m.Mobile] = true
			if m.Due < date {
				r.Overdue = true
				v.Overdue++
			}
		}
		if m.Status == MNRLUpdated {
			r.Follow = o.mnrlFollow(ctx, tenant, m, cs[m.UCC], regs)
		}
		v.Matches = append(v.Matches, r)
	}
	v.Held = len(held)
	sort.SliceStable(v.Matches, func(i, j int) bool {
		a, b := v.Matches[i], v.Matches[j]
		if mnrlOpen(a.Status) != mnrlOpen(b.Status) {
			return mnrlOpen(a.Status)
		}
		return a.ID > b.ID
	})
	ls, _ := docs.List[MNRLList](ctx, o.Docs, tenant, cMNRLList, "")
	sort.Slice(ls, func(i, j int) bool { return ls[i].ID > ls[j].ID })
	last := map[string]string{}
	for _, l := range ls {
		if l.Date > last[l.Format] {
			last[l.Format] = l.Date
		}
	}
	if len(ls) > 30 {
		ls = ls[:30]
	}
	v.Lists = append(v.Lists, ls...)
	f := o.firmFacts(ctx, tenant)
	for _, s := range mnrlSources {
		s.Last = last[s.ID]
		switch s.ID {
		case MNRLListNSE:
			s.Applies = len(f.Member) > 0
		case MNRLListDoT:
			s.Applies = true
		default:
			s.Applies = f.NSDL != triNo
		}
		v.Sources = append(v.Sources, s)
	}
	return v, nil
}

// listUCCReg is an exchange UCC registration as the client lists read it
// (NSE, BSE and MCX keep theirs in one collection).
type listUCCReg struct {
	Exchange string `json:"exchange"`
	UCC      string `json:"ucc"`
	Status   string `json:"status"`
	Reason   string `json:"reason"`
	SentAt   string `json:"sent_at"`
	ResultAt string `json:"result_at"`
}

// listUCCRegs are the registrations by UCC.
func (o *Ops) listUCCRegs(ctx context.Context, tenant string) map[string][]listUCCReg {
	rs, _ := docs.List[listUCCReg](ctx, o.Docs, tenant, cUCCReg, "")
	out := map[string][]listUCCReg{}
	for _, r := range rs {
		if r.UCC != "" {
			out[r.UCC] = append(out[r.UCC], r)
		}
	}
	return out
}

// after says whether an RFC 3339 time is at or after another.
func after(t, since string) bool {
	a, err1 := time.Parse(time.RFC3339, t)
	b, err2 := time.Parse(time.RFC3339, since)
	if err1 != nil || err2 != nil {
		return t >= since
	}
	return !a.Before(b)
}

// mnrlFollow says what the updated number still needs elsewhere.
func (o *Ops) mnrlFollow(ctx context.Context, tenant string, m MNRLMatch, c Client, regs map[string][]listUCCReg) []string {
	var out []string
	if m.Holder != "First" {
		return []string{fmt.Sprintf("%s: change the %s holder's mobile of %s in the depository's system (CDAS or NSDL's DPM) if it was not done there", firstOf(m.Depository, "Depository"), strings.ToLower(m.Holder), cdslMaskBO(m.Account))}
	}
	if !c.DPOnly {
		rs := regs[m.UCC]
		sort.Slice(rs, func(i, j int) bool { return rs[i].Exchange < rs[j].Exchange })
		for _, r := range rs {
			switch {
			case r.SentAt != "" && after(r.SentAt, m.Closed):
				out = append(out, fmt.Sprintf("%s UCC: sent %s, %s", r.Exchange, fmtTSDay(r.SentAt), strings.ToLower(firstOf(r.Status, "sent"))))
			default:
				out = append(out, r.Exchange+" UCC: the modification is waiting to be sent (Clients & onboarding → Exchange UCC)")
			}
		}
		if len(rs) == 0 {
			out = append(out, "Exchange UCC: send the modification to each exchange (Clients & onboarding → Exchange UCC, or the exchanges' portals)")
		}
	}
	for _, b := range o.boAccountsOf(ctx, tenant, m.UCC) {
		if b.Status == "Closed" {
			continue
		}
		switch b.Depository {
		case "CDSL":
			if r, ok := o.cdslBySource(ctx, tenant, m.Change); ok && m.Change != "" {
				out = append(out, fmt.Sprintf("CDSL: BO modify %s for %s is %s", r.ID, cdslMaskBO(b.BOID), strings.ToLower(r.Status)))
			} else {
				out = append(out, fmt.Sprintf("CDSL: the BO modify for %s is raised on CDSL requests when the change is captured, or change it in CDAS", cdslMaskBO(b.BOID)))
			}
		case "NSDL":
			out = append(out, fmt.Sprintf("NSDL: change the mobile of %s in NSDL's system (DPM)", cdslMaskBO(b.BOID)))
		}
	}
	return out
}

// fmtTSDay is the day of an RFC 3339 time.
func fmtTSDay(t string) string {
	if len(t) >= 10 {
		return fmtDay(t[:10])
	}
	return t
}

// ---- the daily check (Periodic work) ----

// mnrlDayDone says whether a list of kind for was loaded for the day.
func (o *Ops) mnrlLoadedOn(ctx context.Context, tenant, day string, formats ...string) (MNRLList, bool) {
	ls, _ := docs.List[MNRLList](ctx, o.Docs, tenant, cMNRLList, "")
	for i := len(ls) - 1; i >= 0; i-- {
		l := ls[i]
		if contains(formats, l.Format) && (l.Date == day || strings.HasPrefix(l.At, day)) {
			return l, true
		}
	}
	return MNRLList{}, false
}

// mnrlPeriodic is the daily check for the trading or the demat side: the
// day's list loaded, and the open matches in the work queue.
func (o *Ops) mnrlPeriodic(ctx context.Context, tenant, actor, task, day string) (string, error) {
	synced := o.mnrlSync(ctx, tenant, actor)
	formats, what := mnrlTaskLists[task], "NSE's report for disconnected mobile numbers (UCI Member Portal > Download) or DoT's MNRL from DIP"
	if task == "mnrl_demat" {
		what = "DoT's MNRL from DIP (CDSL accounts) or NSDL's MNRL matches from e-PASS (NSDL accounts)"
	}
	ms, _ := docs.List[MNRLMatch](ctx, o.Docs, tenant, cMNRLMatch, "")
	open := 0
	for _, m := range ms {
		if mnrlOpen(m.Status) {
			open++
			o.mnrlWork(ctx, tenant, m)
		}
	}
	tail := ""
	if open > 0 {
		tail = fmt.Sprintf("; %d match%s open in the work queue", open, map[bool]string{true: "es", false: ""}[open != 1])
	}
	if synced > 0 {
		tail += fmt.Sprintf("; %d closed as the number was updated", synced)
	}
	if l, ok := o.mnrlLoadedOn(ctx, tenant, day, formats...); ok {
		return fmt.Sprintf("%s list of %s loaded: %d new match%s%s", l.Source, fmtDay(l.Date), l.New, map[bool]string{true: "es", false: ""}[l.New != 1], tail), nil
	}
	msg := "Download " + what + " for " + fmtDay(day) + " and load it on Clients & onboarding → Revoked mobiles" + tail
	o.raiseWork(ctx, tenant, "periodic", task+":"+day, "Load today's revoked mobile list", msg, "", RoleMaker, "mnrl", day)
	return "", &pendingErr{msg, "Waiting for a file"}
}

// mnrlTaskLists are the lists that do each daily check.
var mnrlTaskLists = map[string][]string{"mnrl_trading": {MNRLListNSE, MNRLListDoT}, "mnrl_demat": {MNRLListDoT, MNRLListNSDL}}

// listTaskNone says why a list's Periodic work task has nothing to do.
var listTaskNone = map[string]string{
	"mnrl_trading": "The firm is not a member of an exchange (Settings → member codes).",
	"mnrl_demat":   "The firm is not a depository participant (Settings → Firm profile).",
	"kra_demise":   "The firm is not a member of an exchange (Settings → member codes).",
}

// mnrlTaskApplies says whether a daily check applies to the firm: the
// trading side for a member of an exchange, the demat side for a DP.
func (o *Ops) mnrlTaskApplies(ctx context.Context, tenant, task string) bool {
	f := o.firmFacts(ctx, tenant)
	if task == "mnrl_demat" {
		return f.dp() != triNo
	}
	return len(f.Member) > 0
}

// ---- self-inspection ----

func siMNRL(e *siEnv) (siOut, error) {
	var s siOut
	s.input("Revoked mobile matches", e.date)
	ms, err := docs.List[MNRLMatch](e.ctx, e.o.Docs, e.tenant, cMNRLMatch, "")
	if err != nil {
		return s, err
	}
	ls, _ := docs.List[MNRLList](e.ctx, e.o.Docs, e.tenant, cMNRLList, "")
	if len(ls) == 0 && len(ms) == 0 {
		return siSkip("Waiting for data: no revocation list (NSE's report, DoT's MNRL or NSDL's) has been loaded on Clients & onboarding → Revoked mobiles.")
	}
	for _, m := range ms {
		if !mnrlOpen(m.Status) || m.Opened > e.date {
			continue
		}
		s.checked++
		switch {
		case m.Status == MNRLNew && m.Due < e.date:
			s.bad(fmt.Sprintf("%s · %s · on the %s list of %s, not taken up by %s", m.ID, siClient(m.UCC), m.Source, fmtDay(m.ListDate), fmtDay(m.Due)))
		case m.Status == MNRLContacted && daysBetween(m.Opened, e.date) > mnrlContactDays:
			s.bad(fmt.Sprintf("%s · %s · contacted, but no number confirmed in %d days (found %s)", m.ID, siClient(m.UCC), daysBetween(m.Opened, e.date), fmtDay(m.Opened)))
		}
	}
	s.summary = fmt.Sprintf("%d open matches checked; %d not acted on in time (new matches within %d business days, contacted clients within %d days).", s.checked, s.found, mnrlActDays, mnrlContactDays)
	return s, nil
}
