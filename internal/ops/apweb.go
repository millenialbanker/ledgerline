package ops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/money"
)

// ---- authorised persons: status, payout holds, notices; the website ----
//
// The partner master (partners.go) keeps who an AP is. What happens to an
// AP afterwards is kept here, apart from it, so editing the partner never
// loses it:
//
//   - a payout hold: on finding irregularities, the amounts due to the AP
//     are withheld until the investors' issues are resolved (SEBI master
//     circular for stock brokers 2025/90, para 33.7.7). APPayouts then
//     withholds the whole net payable, with the reason;
//   - a cancellation, with whether it was for disciplinary reasons: the
//     website lists active APs and those cancelled for disciplinary
//     reasons (NSE/COMP/73981, Exhibits 42-44).
//
// Before the AP at a branch changes, every registered client of the
// branch is told (para 33.7.4): NotifyAPChange writes to them and keeps
// the notice in the register. Before the firm starts proprietary trading,
// every client is told (paras 30.1-30.3): SendPropTradingNotice.
//
// The website's lists (the APs, and the USCNBA accounts clients may pay
// into, NSE/INSP/73976 item 4.12) are produced here as text the firm puts
// on its site; each has a version, and a change to the list after the
// version last marked published is a work item and a failed self-
// inspection test until the website is updated.

const (
	cAPStatus = "apstatus" // AP code -> APStatus

	APCancelDisciplinary = "Disciplinary"
	APCancelOther        = "Other (not disciplinary)"
)

// APStatus is what happened to an AP after it was registered.
type APStatus struct {
	Code         string `json:"code"`
	Hold         bool   `json:"hold"`
	HoldReason   string `json:"hold_reason,omitempty"`
	HoldSince    string `json:"hold_since,omitempty"`
	CancelledOn  string `json:"cancelled_on,omitempty"`
	CancelKind   string `json:"cancel_kind,omitempty"`
	CancelReason string `json:"cancel_reason,omitempty"`
	By           string `json:"by,omitempty"`
	At           string `json:"at,omitempty"`
}

func (o *Ops) apStatuses(ctx context.Context, tenant string) map[string]APStatus {
	xs, _ := docs.List[APStatus](ctx, o.Docs, tenant, cAPStatus, "")
	out := map[string]APStatus{}
	for _, x := range xs {
		out[x.Code] = x
	}
	return out
}

// SetAPStatus records an AP's payout hold or cancellation (the compliance
// officer or an ops checker).
func (o *Ops) SetAPStatus(ctx context.Context, tenant, actor string, in APStatus) (APStatus, error) {
	if err := o.require(ctx, tenant, actor, "Holding AP payouts", RoleChecker, RoleCompliance); err != nil {
		return in, err
	}
	in.Code = strings.ToUpper(strings.TrimSpace(in.Code))
	p, _, err := docs.Get[Partner](ctx, o.Docs, tenant, cPartner, in.Code)
	if err != nil || !isAP(p) {
		return in, invalid("%s is not an authorised person", in.Code)
	}
	old, _, _ := docs.GetOr(ctx, o.Docs, tenant, cAPStatus, in.Code, APStatus{Code: in.Code})
	today := o.now().Format("2006-01-02")
	in.HoldReason, in.CancelReason = strings.TrimSpace(in.HoldReason), strings.TrimSpace(in.CancelReason)
	switch {
	case in.Hold && in.HoldReason == "":
		return in, invalid("say why the AP's payouts are held (the irregularity, or the complaints not yet resolved)")
	case in.CancelledOn != "" && !validDate(in.CancelledOn):
		return in, invalid("the cancellation date is YYYY-MM-DD")
	case in.CancelledOn != "" && !contains([]string{APCancelDisciplinary, APCancelOther}, in.CancelKind):
		return in, invalid("say whether the cancellation was for disciplinary reasons")
	case in.CancelledOn != "" && in.CancelReason == "":
		return in, invalid("give the reason for the cancellation")
	}
	if in.CancelledOn == "" {
		in.CancelKind, in.CancelReason = "", ""
	}
	if !in.Hold {
		in.HoldReason, in.HoldSince = "", ""
	} else if !old.Hold {
		in.HoldSince = today
	} else {
		in.HoldSince = old.HoldSince
	}
	in.By, in.At = actor, o.now().Format(time.RFC3339)
	if err := docs.Upsert(ctx, o.Docs, tenant, cAPStatus, in.Code, in, actor); err != nil {
		return in, err
	}
	o.log(ctx, tenant, actor, "partner.status", in.Code, map[string]any{"hold": in.Hold, "reason": in.HoldReason, "cancelled_on": in.CancelledOn, "cancel_kind": in.CancelKind})
	return in, nil
}

// apHoldPayout withholds an AP's whole net payable while a hold is on.
func apHoldPayout(a *APPayout, st APStatus) {
	if !st.Hold || a.Payable <= 0 {
		return
	}
	a.Withheld += a.Payable
	a.Payable = 0
	a.Held = "Payout held since " + fmtDay(st.HoldSince) + ": " + st.HoldReason
}

// ---- notices ----

// NotifyAPChange writes to every active client of a branch before its AP
// changes, and keeps the notice.
func (o *Ops) NotifyAPChange(ctx context.Context, tenant, actor, branch, oldAP, newAP, effective string) (CompRecord, error) {
	if err := o.require(ctx, tenant, actor, "Notifying branch clients", RoleChecker, RoleCompliance); err != nil {
		return CompRecord{}, err
	}
	branch = strings.ToUpper(strings.TrimSpace(branch))
	today := o.now().Format("2006-01-02")
	if !validDate(effective) || effective <= today {
		return CompRecord{}, invalid("the clients are told before the change: give a date after today")
	}
	ps := map[string]Partner{}
	if all, err := docs.List[Partner](ctx, o.Docs, tenant, cPartner, ""); err == nil {
		for _, p := range all {
			ps[p.Code] = p
		}
	}
	name := func(code string) string {
		if p, ok := ps[strings.ToUpper(code)]; ok {
			return p.Name
		}
		return code
	}
	cs, err := o.ClientMap(ctx, tenant)
	if err != nil {
		return CompRecord{}, err
	}
	var told int
	for _, u := range sortedKeys(cs) {
		c := cs[u]
		if c.Branch != branch || c.Status != "Active" {
			continue
		}
		paras := []string{fmt.Sprintf("From %s the authorised person serving our branch %s changes.", fmtDay(effective), branch)}
		if oldAP != "" {
			paras = append(paras, fmt.Sprintf("%s will no longer act for us at the branch.", name(oldAP)))
		}
		if newAP != "" {
			paras = append(paras, fmt.Sprintf("%s takes over as our authorised person there.", name(newAP)))
		}
		paras = append(paras, "Your account, its balances and holdings stay with us as they are. Pay money only into our bank accounts shown on our website, never to an authorised person. Write to us if you have any question.")
		if _, err := o.sendLetter(ctx, tenant, actor, today, u, "APCHG-"+branch+"-"+effective, "Change of authorised person at your branch", paras); err == nil {
			told++
		}
	}
	if told == 0 {
		return CompRecord{}, invalid("branch %s has no active client to tell", branch)
	}
	k, _ := compKind("ap-notice")
	return o.addCompRecord(ctx, tenant, actor, k, map[string]string{"branch": branch, "old_ap": strings.ToUpper(oldAP), "new_ap": strings.ToUpper(newAP),
		"effective_on": effective, "sent_on": today, "clients": fmt.Sprint(told)}, "")
}

// SendPropTradingNotice tells every active client that the firm will also
// trade on its own account from a date.
func (o *Ops) SendPropTradingNotice(ctx context.Context, tenant, actor, startsOn string) (CompRecord, error) {
	if err := o.require(ctx, tenant, actor, "Notifying clients of proprietary trading", RoleCompliance); err != nil {
		return CompRecord{}, err
	}
	today := o.now().Format("2006-01-02")
	if !validDate(startsOn) || startsOn <= today {
		return CompRecord{}, invalid("the clients are told before proprietary trading starts: give a date after today")
	}
	set, _ := o.Settings(ctx, tenant)
	cs, err := o.ClientMap(ctx, tenant)
	if err != nil {
		return CompRecord{}, err
	}
	told := 0
	for _, u := range sortedKeys(cs) {
		if cs[u].Status != "Active" {
			continue
		}
		if _, err := o.sendLetter(ctx, tenant, actor, today, u, "PROP-"+startsOn, "We will also trade on our own account", []string{
			fmt.Sprintf("From %s %s will, besides doing business for clients, also trade on its own account (proprietary trading).", fmtDay(startsOn), firstOf(set.Firm, "we")),
			"Your orders and your money and securities are kept apart from the firm's own, as the rules require. Write to us if you have any question."}); err == nil {
			told++
		}
	}
	k, _ := compKind("prop-notice")
	return o.addCompRecord(ctx, tenant, actor, k, map[string]string{"starts_on": startsOn, "sent_on": today, "clients": fmt.Sprint(told)}, "")
}

// ---- the website's lists ----

// APWebRow is one AP on the website's list.
type APWebRow struct {
	Name      string            `json:"name"`
	Code      string            `json:"code"`
	Regs      map[string]string `json:"regs"` // exchange -> AP registration number
	Branch    string            `json:"branch"`
	City      string            `json:"city,omitempty"`
	Terminals []string          `json:"terminals"` // exchange: terminal IDs
	Cancelled string            `json:"cancelled_on,omitempty"`
	Reason    string            `json:"reason,omitempty"`
}

// WebsiteList is a list for the website, with its version.
type WebsiteList struct {
	Title     string     `json:"title"`
	Active    []APWebRow `json:"active,omitempty"`
	Cancelled []APWebRow `json:"cancelled,omitempty"`
	Accounts  []FirmBank `json:"accounts,omitempty"`
	Text      string     `json:"text"`
	HTML      string     `json:"html"`
	Version   string     `json:"version"`
	Published string     `json:"published_on,omitempty"`
	Changed   bool       `json:"changed"` // differs from the version last published
	ToConfirm string     `json:"to_confirm"`
}

func listVersion(parts []string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(h[:6])
}

func (o *Ops) lastPublished(ctx context.Context, tenant, list string) (string, string) {
	on, ver := "", ""
	for _, r := range o.compRecs(ctx, tenant, "website") {
		if r.V("list") == list && r.V("published_on") >= on {
			on, ver = r.V("published_on"), r.V("hash")
		}
	}
	return on, ver
}

// websiteAPs builds the AP list.
func (o *Ops) websiteAPs(ctx context.Context, tenant string) WebsiteList {
	l := WebsiteList{Title: "Authorised persons", Active: []APWebRow{}, Cancelled: []APWebRow{},
		ToConfirm: "Columns follow what NSE/COMP/73981 Exhibits 42-44 name (name, exchange-wise registration, terminals, cancellations for disciplinary reasons); check them against the exhibit's format before publishing."}
	ps, _ := docs.List[Partner](ctx, o.Docs, tenant, cPartner, "")
	bs, _ := o.Branches(ctx, tenant)
	city := map[string]string{}
	for _, b := range bs {
		city[b.Code] = b.City
	}
	st := o.apStatuses(ctx, tenant)
	ds, _ := o.allDealers(ctx, tenant)
	today := o.now().Format("2006-01-02")
	terms := map[string][]string{}
	for _, d := range ds {
		if d.Partner == "" {
			continue
		}
		for _, t := range d.openTerminals(today) {
			terms[d.Partner] = append(terms[d.Partner], t.Exchange+": "+t.TerminalID)
		}
	}
	var parts []string
	sort.Slice(ps, func(i, j int) bool { return ps[i].Name < ps[j].Name })
	for _, p := range ps {
		if !isAP(p) {
			continue
		}
		regs := map[string]string{}
		for _, x := range discExchangeOrder {
			if r := apRegFor(p, x); r != "" {
				regs[x] = r
			}
		}
		ts := terms[p.Code]
		sort.Strings(ts)
		if ts == nil {
			ts = []string{}
		}
		row := APWebRow{Name: p.Name, Code: p.Code, Regs: regs, Branch: p.Branch, City: city[p.Branch], Terminals: ts}
		s := st[p.Code]
		switch {
		case s.CancelledOn != "" && s.CancelledOn <= today:
			if s.CancelKind != APCancelDisciplinary {
				continue // a cancellation for other reasons is not listed
			}
			row.Cancelled, row.Reason = s.CancelledOn, s.CancelReason
			l.Cancelled = append(l.Cancelled, row)
		case p.Active:
			l.Active = append(l.Active, row)
		default:
			continue
		}
		parts = append(parts, fmt.Sprint(row))
	}
	l.Version = listVersion(parts)
	var tx, ht strings.Builder
	regsText := func(r APWebRow) string {
		var xs []string
		for _, x := range discExchangeOrder {
			if v := r.Regs[x]; v != "" {
				xs = append(xs, x+" "+v)
			}
		}
		return strings.Join(xs, "; ")
	}
	tx.WriteString("Active authorised persons\nSr. No.\tName\tRegistration (exchange-wise)\tLocation\tTerminals\n")
	ht.WriteString("<h3>Active authorised persons</h3>\n<table>\n<tr><th>Sr. No.</th><th>Name</th><th>Registration (exchange-wise)</th><th>Location</th><th>Terminals</th></tr>\n")
	for i, r := range l.Active {
		fmt.Fprintf(&tx, "%d\t%s\t%s\t%s\t%s\n", i+1, r.Name, regsText(r), firstOf(r.City, r.Branch), strings.Join(r.Terminals, ", "))
		fmt.Fprintf(&ht, "<tr><td>%d</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td></tr>\n", i+1, html.EscapeString(r.Name), html.EscapeString(regsText(r)), html.EscapeString(firstOf(r.City, r.Branch)), html.EscapeString(strings.Join(r.Terminals, ", ")))
	}
	tx.WriteString("\nAuthorised persons cancelled for disciplinary reasons\nSr. No.\tName\tRegistration (exchange-wise)\tCancelled on\tReason\n")
	ht.WriteString("</table>\n<h3>Authorised persons cancelled for disciplinary reasons</h3>\n<table>\n<tr><th>Sr. No.</th><th>Name</th><th>Registration (exchange-wise)</th><th>Cancelled on</th><th>Reason</th></tr>\n")
	for i, r := range l.Cancelled {
		fmt.Fprintf(&tx, "%d\t%s\t%s\t%s\t%s\n", i+1, r.Name, regsText(r), fmtDay(r.Cancelled), r.Reason)
		fmt.Fprintf(&ht, "<tr><td>%d</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td></tr>\n", i+1, html.EscapeString(r.Name), html.EscapeString(regsText(r)), fmtDay(r.Cancelled), html.EscapeString(r.Reason))
	}
	ht.WriteString("</table>\n")
	l.Text, l.HTML = tx.String(), ht.String()
	l.Published, _ = o.lastPublished(ctx, tenant, "Authorised persons")
	_, ver := o.lastPublished(ctx, tenant, "Authorised persons")
	l.Changed = ver != l.Version
	return l
}

// websiteUSCNBA builds the text of the accounts clients may pay into.
func (o *Ops) websiteUSCNBA(ctx context.Context, tenant string) WebsiteList {
	set, _ := o.Settings(ctx, tenant)
	l := WebsiteList{Title: "USCNBA accounts", Accounts: []FirmBank{},
		ToConfirm: "The note's words are Ledgerline's; NSE/INSP/73976 item 4.12 prescribes a note: use its exact text if it differs."}
	var parts []string
	for _, b := range o.firmBanks(ctx, tenant) {
		if b.Kind != BankUSCNBA || !b.Active {
			continue
		}
		l.Accounts = append(l.Accounts, b)
		parts = append(parts, b.Name+"|"+b.Bank+"|"+b.Account+"|"+b.IFSC)
	}
	sort.Strings(parts)
	l.Version = listVersion(parts)
	firm := firstOf(set.Firm, "the firm")
	note := fmt.Sprintf("Clients may pay %s only into the bank accounts listed here. Do not pay into any other account, or to any person, in cash or otherwise.", firm)
	var tx, ht strings.Builder
	tx.WriteString("Bank accounts for payments to " + firm + "\nAccount name\tBank\tAccount number\tIFSC\n")
	ht.WriteString("<h3>Bank accounts for payments to " + html.EscapeString(firm) + "</h3>\n<table>\n<tr><th>Account name</th><th>Bank</th><th>Account number</th><th>IFSC</th></tr>\n")
	for _, b := range l.Accounts {
		n := firstOf(b.Name, firm+" – USCNB account")
		fmt.Fprintf(&tx, "%s\t%s\t%s\t%s\n", n, b.Bank, b.Account, b.IFSC)
		fmt.Fprintf(&ht, "<tr><td>%s</td><td>%s</td><td>%s</td><td>%s</td></tr>\n", html.EscapeString(n), html.EscapeString(b.Bank), html.EscapeString(b.Account), html.EscapeString(b.IFSC))
	}
	tx.WriteString("\nNote: " + note + "\n")
	ht.WriteString("</table>\n<p><b>Note:</b> " + html.EscapeString(note) + "</p>\n")
	l.Text, l.HTML = tx.String(), ht.String()
	for i := range l.Accounts {
		l.Accounts[i] = l.Accounts[i].Public()
	}
	var ver string
	l.Published, ver = o.lastPublished(ctx, tenant, "USCNBA accounts")
	l.Changed = ver != l.Version
	return l
}

// WebsiteLists are both lists for the screen.
func (o *Ops) WebsiteLists(ctx context.Context, tenant, actor string) ([]WebsiteList, error) {
	if err := o.require(ctx, tenant, actor, "Viewing compliance registers", RoleMaker, RoleChecker, RoleRisk, RoleCompliance); err != nil {
		return nil, err
	}
	return []WebsiteList{o.websiteUSCNBA(ctx, tenant), o.websiteAPs(ctx, tenant)}, nil
}

// MarkWebsitePublished records that a list's current version is on the
// firm's website.
func (o *Ops) MarkWebsitePublished(ctx context.Context, tenant, actor, list string) (CompRecord, error) {
	if err := o.require(ctx, tenant, actor, "Recording website updates", RoleCompliance); err != nil {
		return CompRecord{}, err
	}
	var l WebsiteList
	switch list {
	case "USCNBA accounts":
		l = o.websiteUSCNBA(ctx, tenant)
	case "Authorised persons":
		l = o.websiteAPs(ctx, tenant)
	default:
		return CompRecord{}, invalid("the list is USCNBA accounts or Authorised persons")
	}
	k, _ := compKind("website")
	today := o.now().Format("2006-01-02")
	r, err := o.addCompRecord(ctx, tenant, actor, k, map[string]string{"list": list, "published_on": today, "hash": l.Version}, "")
	if err == nil {
		o.closeWork(ctx, tenant, actor, "website", list+"|"+l.Version, "published")
	}
	return r, err
}

// websiteTick raises a work item when a list changed after it was last
// published.
func (o *Ops) websiteTick(ctx context.Context, tenant, today string) {
	for _, l := range []WebsiteList{o.websiteUSCNBA(ctx, tenant), o.websiteAPs(ctx, tenant)} {
		if !l.Changed || (len(l.Accounts) == 0 && len(l.Active) == 0 && len(l.Cancelled) == 0) {
			continue
		}
		o.raiseWork(ctx, tenant, "website", l.Title+"|"+l.Version, "Update the website: "+strings.ToLower(l.Title),
			"The list of "+strings.ToLower(l.Title)+" changed since it was last put on the website. Copy the new text from Compliance → Registers → Website, put it on the site, then mark the website updated.",
			"", RoleCompliance, "compregs", today)
	}
}

// ---- terminal-wise turnover ----

// TerminalTurnoverRow is one terminal's turnover by month.
type TerminalTurnoverRow struct {
	Exchange string         `json:"exchange"`
	Terminal string         `json:"terminal"`
	Location string         `json:"location"`
	Dealer   string         `json:"dealer,omitempty"`
	Branch   string         `json:"branch,omitempty"`
	Partner  string         `json:"partner,omitempty"`
	Months   []money.Amount `json:"months"` // oldest first
	Review   string         `json:"review,omitempty"`
}

// TerminalTurnover is the report.
type TerminalTurnover struct {
	Months []string              `json:"months"`
	Rows   []TerminalTurnoverRow `json:"rows"`
	Rule   string                `json:"rule"`
}

// terminalTurnover adds up the turnover (price × quantity; the premium for
// options) of each terminal by month for the six months to date's month,
// and flags for review a terminal whose last full month is below a tenth
// of the median terminal's, or that fell three months running. MCX sets no
// number for "low"; the tenth is Ledgerline's, for the firm to review.
func (o *Ops) terminalTurnover(ctx context.Context, tenant, date string) TerminalTurnover {
	rep := TerminalTurnover{Rows: []TerminalTurnoverRow{},
		Rule: "Low: last full month below a tenth of the median terminal. Declining: lower in each of the last three full months. Review these for business done away from the terminal (MCX/INSP/255/2026, para 5.1)."}
	for i := 5; i >= 0; i-- {
		rep.Months = append(rep.Months, addMonths(date[:7]+"-01", -i)[:7])
	}
	ts, _ := o.App.Trades.List(ctx, tenant, ingest.FromExchange, "")
	ds, _ := o.allDealers(ctx, tenant)
	book := newDealerBook(ds)
	rows := map[string]*TerminalTurnoverRow{}
	for _, t := range ts {
		if strings.TrimSpace(t.Terminal) == "" || len(t.TradeDate) < 7 {
			continue
		}
		mi := -1
		for i, m := range rep.Months {
			if t.TradeDate[:7] == m {
				mi = i
			}
		}
		if mi < 0 || t.TradeDate > date {
			continue
		}
		k := t.Exchange + "|" + strings.ToUpper(t.Terminal)
		r := rows[k]
		if r == nil {
			r = &TerminalTurnoverRow{Exchange: t.Exchange, Terminal: t.Terminal, Months: make([]money.Amount, len(rep.Months))}
			if i, term, ok := book.findTerminal(t.Exchange, t.Terminal, t.TradeDate); ok {
				d := ds[i]
				r.Dealer, r.Branch, r.Partner, r.Location = d.Name(), d.Branch, d.Partner, firstOf(term.LocationID, d.Office.City)
			}
			rows[k] = r
		}
		r.Months[mi] += t.Price.MulInt(t.Qty).Round(money.UnitPaisa)
	}
	// The last full month is the one before date's month.
	last := len(rep.Months) - 2
	var lastVals []money.Amount
	for _, r := range rows {
		lastVals = append(lastVals, r.Months[last])
	}
	sort.Slice(lastVals, func(i, j int) bool { return lastVals[i] < lastVals[j] })
	var median money.Amount
	if n := len(lastVals); n > 0 {
		median = lastVals[n/2]
	}
	for _, k := range sortedKeys(rows) {
		r := rows[k]
		var why []string
		if median > 0 && r.Months[last]*10 < median {
			why = append(why, "low volume")
		}
		if last >= 3 && r.Months[last] < r.Months[last-1] && r.Months[last-1] < r.Months[last-2] && r.Months[last-2] < r.Months[last-3] {
			why = append(why, "declining three months running")
		}
		r.Review = strings.Join(why, "; ")
		rep.Rows = append(rep.Rows, *r)
	}
	return rep
}

// TerminalTurnoverReport is the report for the screen.
func (o *Ops) TerminalTurnoverReport(ctx context.Context, tenant, actor, date string) (TerminalTurnover, error) {
	if err := o.require(ctx, tenant, actor, "Viewing dealers", RoleChecker, RoleRisk, RoleCompliance); err != nil {
		return TerminalTurnover{}, err
	}
	date = firstOf(date, o.businessDate(ctx, tenant))
	if !validDate(date) {
		return TerminalTurnover{}, invalid("date must be YYYY-MM-DD")
	}
	return o.terminalTurnover(ctx, tenant, date), nil
}

// TerminalTurnoverCSV is the report as CSV, as MCX may ask for it.
func (o *Ops) TerminalTurnoverCSV(ctx context.Context, tenant, actor, date string) ([]byte, error) {
	rep, err := o.TerminalTurnoverReport(ctx, tenant, actor, date)
	if err != nil {
		return nil, err
	}
	head := []string{"Exchange", "Terminal", "Location", "Dealer", "Branch", "AP"}
	head = append(head, rep.Months...)
	rows := [][]string{append(head, "Review")}
	for _, r := range rep.Rows {
		line := []string{r.Exchange, r.Terminal, r.Location, r.Dealer, r.Branch, r.Partner}
		for _, m := range r.Months {
			line = append(line, m.String())
		}
		rows = append(rows, append(line, r.Review))
	}
	return CSV(rows), nil
}

// ---- NSE user types ----

const cNSEUserType = "nseusertype" // "map" -> NSEUserTypes

// NSEUserTypeNames are NSE's user types (NSE/MSD/73956).
var NSEUserTypeNames = []string{"Corporate manager", "Branch manager", "Dealer"}

// NSEUserTypes is each NSE terminal's user type.
type NSEUserTypes struct {
	Types map[string]string `json:"types"` // terminal ID -> type
}

// SetNSEUserType records an NSE terminal's user type.
func (o *Ops) SetNSEUserType(ctx context.Context, tenant, actor, terminal, typ string) (NSEUserTypes, error) {
	if !o.canEditDealers(ctx, tenant, actor) {
		return NSEUserTypes{}, fmt.Errorf("%w: changing dealers needs an ops maker or the compliance officer", ErrForbidden)
	}
	terminal = strings.ToUpper(strings.TrimSpace(terminal))
	if terminal == "" || (typ != "" && !contains(NSEUserTypeNames, typ)) {
		return NSEUserTypes{}, invalid("give the terminal and its type: %s", strings.Join(NSEUserTypeNames, ", "))
	}
	x, _, _ := docs.GetOr(ctx, o.Docs, tenant, cNSEUserType, "map", NSEUserTypes{Types: map[string]string{}})
	if x.Types == nil {
		x.Types = map[string]string{}
	}
	if typ == "" {
		delete(x.Types, terminal)
	} else {
		x.Types[terminal] = typ
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cNSEUserType, "map", x, actor); err != nil {
		return x, err
	}
	o.log(ctx, tenant, actor, "dealer.nse_user_type", terminal, map[string]any{"type": typ})
	return x, nil
}

// NSEUserCount is the open NSE terminals by type against NSE's limit.
type NSEUserCount struct {
	Type    string   `json:"type"`
	Open    int      `json:"open"`
	Limit   string   `json:"limit"` // as the firm entered it, "" not entered
	Over    bool     `json:"over"`
	Untyped []string `json:"untyped,omitempty"`
}

func (o *Ops) nseUserCounts(ctx context.Context, tenant, date string) ([]NSEUserCount, []string) {
	ds, _ := o.allDealers(ctx, tenant)
	x, _, _ := docs.GetOr(ctx, o.Docs, tenant, cNSEUserType, "map", NSEUserTypes{Types: map[string]string{}})
	_, lim := o.tcPractice(ctx, tenant, "nse-user-limits")
	keys := map[string]string{"Corporate manager": "cm", "Branch manager": "bm", "Dealer": "dealer"}
	n := map[string]int{}
	var untyped []string
	for _, d := range ds {
		for _, t := range d.openTerminals(date) {
			if t.Exchange != "NSE" || !t.activeOn(date) {
				continue
			}
			typ := x.Types[strings.ToUpper(t.TerminalID)]
			if typ == "" {
				untyped = append(untyped, t.TerminalID)
				continue
			}
			n[typ]++
		}
	}
	var out []NSEUserCount
	for _, typ := range NSEUserTypeNames {
		c := NSEUserCount{Type: typ, Open: n[typ], Limit: strings.TrimSpace(lim[keys[typ]])}
		var l int
		if _, err := fmt.Sscanf(c.Limit, "%d", &l); err == nil {
			c.Over = c.Open > l
		}
		out = append(out, c)
	}
	sort.Strings(untyped)
	return out, untyped
}

// NSEUserView is the screen's view of user types.
type NSEUserView struct {
	Counts    []NSEUserCount    `json:"counts"`
	Untyped   []string          `json:"untyped"`
	Types     map[string]string `json:"types"`
	Terminals []string          `json:"terminals"`
}

// NSEUsers is the view for the screen.
func (o *Ops) NSEUsers(ctx context.Context, tenant, actor string) (NSEUserView, error) {
	if err := o.canSeeDealers(ctx, tenant, actor); err != nil {
		return NSEUserView{}, err
	}
	d := o.businessDate(ctx, tenant)
	v := NSEUserView{Untyped: []string{}, Terminals: []string{}}
	v.Counts, v.Untyped = o.nseUserCounts(ctx, tenant, d)
	if v.Untyped == nil {
		v.Untyped = []string{}
	}
	x, _, _ := docs.GetOr(ctx, o.Docs, tenant, cNSEUserType, "map", NSEUserTypes{Types: map[string]string{}})
	v.Types = x.Types
	if v.Types == nil {
		v.Types = map[string]string{}
	}
	ds, _ := o.allDealers(ctx, tenant)
	for _, dl := range ds {
		for _, t := range dl.openTerminals(d) {
			if t.Exchange == "NSE" {
				v.Terminals = append(v.Terminals, t.TerminalID)
			}
		}
	}
	sort.Strings(v.Terminals)
	return v, nil
}
