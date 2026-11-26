package ops

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// ---- CUSPA: intimation after the pledge, the written policy, extensions ----
//
// The rest of SEBI's framework for unpaid securities (circular
// HO/38/11/(9)2026-MIRSD-POD/I/15382/2026 of 3 July 2026, revised master
// circular paras 46.2-46.4 and 46.12-46.14; NSE circular 41/2026,
// NSE/INSP/75508 of 31 July 2026, guidelines 2 and 10):
//
//   - Intimation (46.2; guideline 2): once unpaid securities are pledged to
//     the CUSPA the client is told, by email or SMS (or in the daily margin
//     statement), of the debit in the ledger, the funds obligation, the
//     securities pledged and the firm's right to sell them if the client
//     does not pay; the logs are kept. Ledgerline sends it when a line is
//     made (Settlement → To CUSPA) and the daily CUSPA review, which runs
//     before the market opens, sends any not yet sent, so it reaches the
//     client by the start of the next trading day.
//   - Policy (46.3-46.4): the firm frames a written policy on unpaid
//     securities (on its own or in the RMS policy): the reasons, manner and
//     timing of invoking and releasing the pledge and of selling the
//     securities, and the most time a client has to pay, at most 5 trading
//     days from pay-out. It is communicated to every client before it is
//     put in place. Ledgerline keeps the policy's text and version, who
//     approved it, from when it applies and where it is on the firm's
//     website, and sends it to every active client (and, by the daily
//     review, to each new client), recording who was sent which version.
//   - Extension (46.12-46.14; guideline 10; from about 3 January 2027, six
//     months from the circular): where unpaid securities cannot be sold
//     within the 5 trading days (lower circuit after the sell order was
//     placed, suspension or halt, and the other reasons the market
//     infrastructure institutions accept, coded CUSPA01 to CUSPA06), the
//     firm asks the depository for an extension of up to one calendar week
//     by 6 PM on the 5th trading day, again on the last day of an extension
//     while the reason lasts, and tells the client of each extension.
//     Ledgerline records the request (reason and code) on the line, moves
//     the last day to invoke to the last trading day of the week asked for
//     (the depository then releases at the end of the next trading day:
//     the safe reading of "auto-released" for an extended pledge) and sends
//     the client the extension notice. The request itself is given on the
//     depository's system (the day's Depository list carries it): CDSL's
//     extension request is not in the API handbook Ledgerline has.

// cuspaExtFrom is the first day an extension may be asked for: six months
// from the circular of 3 July 2026 (to confirm against the exchanges'
// notice of the effective date).
const cuspaExtFrom = "2027-01-03"

// cuspaExtCutoff is the latest time of day (IST) for the request.
const cuspaExtCutoff = "18:00"

// cuspaFrameworkFrom is the day the framework applied from (pledges to the
// CUSPA instead of transfers to the client unpaid securities account).
const cuspaFrameworkFrom = "2026-10-31"

var reCUSPAExtCode = regexp.MustCompile(`^CUSPA0[1-6]$`)

// CUSPAExtension is one request to the depository to extend a CUSPA
// pledge.
type CUSPAExtension struct {
	Date   string `json:"date"`  // the business date it was asked on (the last day to invoke)
	At     string `json:"at"`    // when it was recorded
	By     string `json:"by"`    // who asked
	Code   string `json:"code"`  // CUSPA01 to CUSPA06, as the depository takes it
	Why    string `json:"why"`   // the reason in words
	Until  string `json:"until"` // the new last day to invoke (trading day)
	Notice string `json:"notice,omitempty"`
	// NoticeOK says the client was told (email or SMS sent).
	NoticeOK bool `json:"notice_ok,omitempty"`
}

func init() {
	EventVars["cuspa.pledged"] = []string{"lines", "amount", "obligation", "pay_by", "last_day", "date"}
	EventVars["cuspa.extension"] = []string{"lines", "until", "reason", "date"}
	EventVars["cuspa.policy"] = []string{"version", "effective", "pay_days", "url"}
	t := func(ev, ch, subj, body, dlt string) Template {
		return Template{Event: ev, Channel: ch, Category: "Transactional", Subject: subj, Body: body, DLT: dlt, Active: true}
	}
	extraTemplates = append(extraTemplates,
		t("cuspa.pledged", ChEmail, "Unpaid shares pledged to us: ₹{amount} due",
			"Dear {name},\n\nShares you bought have not been fully paid for. As SEBI's rules on unpaid securities require, they were delivered to your demat account and pledged to us in the client unpaid securities pledgee account (CUSPA) on {date}: {lines}.\n\nThe funds obligation for these purchases is ₹{obligation}, and the debit in your trading account {ucc} is ₹{amount}.\n\nPlease pay by {pay_by}. If you do not pay, we have the right to sell these shares under your client code, up to {last_day}, and credit the proceeds to your account; any amount still due after the sale remains payable. When you pay, the pledge is released.\n\n{firm}", ""),
		t("cuspa.pledged", ChSMS, "",
			"Dear {name}, unpaid shares ({lines}) are pledged to us in CUSPA on {date}. Rs {amount} is due in {ucc} (obligation Rs {obligation}). Pay by {pay_by}, else we may sell them. -{firm}", "1107166000000000013"),
		t("cuspa.extension", ChEmail, "Unpaid shares: the pledge is extended to {until}",
			"Dear {name},\n\nThe unpaid shares pledged to us in the client unpaid securities pledgee account ({lines}) could not be sold within the usual five trading days: {reason}. As SEBI's rules allow, we have asked the depository to extend the pledge until {until}. We may sell them under your client code up to that day unless you pay the amount due in your trading account {ucc}.\n\nThis notice is sent on {date}.\n\n{firm}", ""),
		t("cuspa.extension", ChSMS, "",
			"Dear {name}, the CUSPA pledge of your unpaid shares ({lines}) is extended to {until}: {reason}. Pay the dues in {ucc} to have it released. -{firm}", "1107166000000000014"),
		t("cuspa.policy", ChEmail, "Our policy on unpaid securities",
			"Dear {name},\n\nThis is our policy on securities you buy and do not pay for in full (version {version}, in force from {effective}), as SEBI's rules on unpaid securities require us to tell you before it applies.\n\nShares not paid for are delivered to your demat account and pledged to us in the client unpaid securities pledgee account (CUSPA). You have {pay_days} trading day(s) after the pay-out to pay. If the amount is still due, we give you notice and may sell the shares under your client code, up to the 5th trading day after the pay-out, and credit the proceeds to your account; when you pay, the pledge is released. The full policy is on our website: {url}.\n\n{firm}", ""),
		t("cuspa.policy", ChSMS, "",
			"Dear {name}, our policy on unpaid securities (v{version}, from {effective}): pay within {pay_days} trading day(s) of pay-out, else the pledged shares may be sold. Details: {url} -{firm}", "1107166000000000015"),
	)
}

// ---- intimation after the pledge ----

// cuspaIntimate sends each client of the chosen lines (all held lines not
// yet intimated when ids is nil) the intimation after the pledge, and
// records on each line how it went. It returns the number of clients told
// and the clients it could not tell, with why.
func (o *Ops) cuspaIntimate(ctx context.Context, tenant, actor, date string, ids []string) (int, map[string]string) {
	b, err := o.cuspaBook(ctx, tenant, date)
	if err != nil {
		return 0, nil
	}
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	by := map[string][]CUSPAItem{}
	var order []string
	for _, it := range b.items {
		if it.Qty <= 0 || it.IntimatedAt != "" || (ids != nil && !want[it.ID]) {
			continue
		}
		if by[it.UCC] == nil {
			order = append(order, it.UCC)
		}
		by[it.UCC] = append(by[it.UCC], it)
	}
	sent, failed := 0, map[string]string{}
	for _, u := range order {
		var lines []string
		var oblig money.Dec
		first := cuspaDates{}
		for i, it := range by[u] {
			lines = append(lines, fmt.Sprintf("%d %s", it.Qty, it.Symbol))
			oblig = oblig.Add(it.Value)
			if d := it.dates(b.set, date); i == 0 || d.PayBy < first.PayBy {
				first = d
			}
		}
		owed := money.Max(b.funds[u].Dec().Neg(), money.Dec{})
		ms, err := o.sendEvent(ctx, tenant, actor, "cuspa.pledged", u, map[string]string{
			"lines": strings.Join(lines, ", "), "amount": owed.Fixed(2), "obligation": oblig.Fixed(2), "pay_by": fmtDay(first.PayBy),
			"last_day": fmtDay(first.LastInvoke), "date": fmtDay(firstOf(first.PayOut, date)),
		}, "")
		note := cuspaSentNote(ms, err, "cuspa.pledged")
		ok := strings.HasPrefix(note, "sent by ")
		at := o.now().Format(time.RFC3339)
		for _, it := range by[u] {
			_, _ = docs.Update(ctx, o.Docs, tenant, cCUSPA, it.ID, actor, func(x *CUSPAItem) error {
				if ok {
					x.IntimatedAt = at
				}
				x.Intimation = note
				x.Events = append(x.Events, o.cuspaEvent(date, map[bool]string{true: "Intimated", false: "Intimation not sent"}[ok], 0, actor, note))
				return nil
			})
		}
		if ok {
			sent++
			o.log(ctx, tenant, actor, "cuspa.intimated", u, map[string]any{"lines": len(lines), "date": date})
		} else {
			failed[u] = note
			o.log(ctx, tenant, actor, "cuspa.intimation.failed", u, map[string]any{"why": note})
		}
	}
	return sent, failed
}

// cuspaSentNote says how a message went: "sent by Email and SMS", or why
// it did not go.
func cuspaSentNote(ms []Message, err error, event string) string {
	var sent, other []string
	for _, m := range ms {
		if m.Status == "Sent" {
			sent = append(sent, m.Channel)
		} else {
			other = append(other, m.Channel+" "+strings.ToLower(m.Status))
		}
	}
	switch {
	case len(sent) > 0:
		return "sent by " + strings.Join(sent, " and ")
	case err != nil:
		return "could not be sent: " + errText(err)
	case len(other) > 0:
		return "not delivered: " + strings.Join(other, ", ")
	}
	return "no active " + event + " template (Client messages)"
}

// ---- extensions ----

func cuspaExtCheckReq(req CUSPABulkReq) error {
	if !reCUSPAExtCode.MatchString(strings.ToUpper(strings.TrimSpace(req.Code))) {
		return invalid("give the depository's reason code for the extension, CUSPA01 to CUSPA06 (NSE circular 41/2026, guideline 10)")
	}
	if strings.TrimSpace(req.Note) == "" {
		return invalid("say why the shares could not be sold (for example: lower circuit after the sell order was placed; trading suspended)")
	}
	return nil
}

// cuspaExtUntil is the last trading day of an extension asked on date: one
// calendar week on, or the trading day before it.
func cuspaExtUntil(set Settings, date string) string {
	u := addDays(date, 7)
	for !set.IsBusinessDay(u) && u > date {
		u = addDays(u, -1)
	}
	return u
}

// cuspaExtWhyNot says why a line's pledge cannot be extended on date ("":
// it can).
func (o *Ops) cuspaExtWhyNot(b cuspaBook, it CUSPAItem, date string) string {
	d := it.dates(b.set, date)
	now := o.now().In(ist)
	today := now.Format("2006-01-02")
	switch {
	case date < cuspaExtFrom:
		return "extensions may be asked for from " + fmtDay(cuspaExtFrom)
	case it.Qty <= 0:
		return "nothing held (" + it.Status + ")"
	case !b.owes(it.UCC):
		return "the client's ledger is not in debit: release it"
	case date != d.LastInvoke:
		return "an extension is asked for on the last day to invoke (" + fmtDay(d.LastInvoke) + ")"
	case date < today:
		return "the day to ask (" + fmtDay(date) + ") is over"
	case date == today && now.Format("15:04") >= cuspaExtCutoff:
		return "it is past 6 PM, the depository's cut-off for the request"
	}
	return ""
}

// cuspaExtend records the extensions planned and tells each client.
func (o *Ops) cuspaExtend(ctx context.Context, tenant, actor string, b cuspaBook, req CUSPABulkReq, chosen []CUSPAItem, plan map[string]int64, res CUSPABulkResult) (CUSPABulkResult, error) {
	code, why := strings.ToUpper(strings.TrimSpace(req.Code)), strings.TrimSpace(req.Note)
	until := cuspaExtUntil(b.set, req.Date)
	at := o.now().Format(time.RFC3339)
	by := map[string][]CUSPAItem{}
	var order []string
	for _, it := range chosen {
		q, ok := plan[it.ID]
		if !ok {
			continue
		}
		_, err := docs.Update(ctx, o.Docs, tenant, cCUSPA, it.ID, actor, func(x *CUSPAItem) error {
			if x.Qty <= 0 {
				return fmt.Errorf("%w: %s holds nothing", ErrState, x.ID)
			}
			x.Extensions = append(x.Extensions, CUSPAExtension{Date: req.Date, At: at, By: actor, Code: code, Why: why, Until: until})
			x.Events = append(x.Events, o.cuspaEvent(req.Date, "Extended", x.Qty, actor, fmt.Sprintf("%s: %s; to %s", code, why, fmtDay(until))))
			return nil
		})
		line := CUSPALine{ID: it.ID, UCC: it.UCC, Symbol: it.Symbol, Qty: q, OK: err == nil}
		if err != nil {
			line.Why = errText(err)
		} else {
			if by[it.UCC] == nil {
				order = append(order, it.UCC)
			}
			by[it.UCC] = append(by[it.UCC], it)
			o.log(ctx, tenant, actor, "cuspa.extended", it.ID, map[string]any{"ucc": it.UCC, "code": code, "until": until, "date": req.Date})
		}
		res.Lines = append(res.Lines, line)
	}
	for _, u := range order {
		var lines []string
		for _, it := range by[u] {
			lines = append(lines, fmt.Sprintf("%d %s", it.Qty, it.Symbol))
		}
		ms, err := o.sendEvent(ctx, tenant, actor, "cuspa.extension", u, map[string]string{"lines": strings.Join(lines, ", "), "until": fmtDay(until), "reason": why, "date": fmtDay(req.Date)}, "")
		note := cuspaSentNote(ms, err, "cuspa.extension")
		ok := strings.HasPrefix(note, "sent by ")
		for _, it := range by[u] {
			_, _ = docs.Update(ctx, o.Docs, tenant, cCUSPA, it.ID, actor, func(x *CUSPAItem) error {
				if n := len(x.Extensions); n > 0 {
					x.Extensions[n-1].Notice, x.Extensions[n-1].NoticeOK = note, ok
				}
				return nil
			})
		}
		if !ok {
			o.raiseWork(ctx, tenant, "cuspa-ext", u+":"+req.Date, "Tell "+u+" of the CUSPA extension",
				fmt.Sprintf("The pledge of %s was extended to %s, but the client's notice %s. Tell the client (email or SMS) and keep the record.", strings.Join(lines, ", "), fmtDay(until), note), u, RoleChecker, "settlement", req.Date)
		}
	}
	if len(order) > 0 {
		o.raiseWork(ctx, tenant, "cuspa-ext", "depository:"+req.Date, "Ask the depository for the CUSPA extension(s) by 6 PM",
			fmt.Sprintf("Give the extension request (%s) for the lines on today's Depository list (Settlement → Unpaid securities) on the depository's system by 6 PM today. The last day to invoke is now %s.", code, fmtDay(until)), "", RoleChecker, "settlement", req.Date)
	}
	for _, l := range res.Lines {
		if l.OK {
			res.Done++
		} else {
			res.Skipped++
		}
	}
	return res, nil
}

// ---- the written policy on unpaid securities ----

const (
	cCUSPAPolicy     = "cuspapolicy"  // "current", and "v<N>" for each version
	cCUSPAPolicySent = "cuspapolsent" // UCC: the version the client was sent
)

// CUSPAPolicyRecord is the firm's written policy on unpaid securities.
type CUSPAPolicyRecord struct {
	Version        int    `json:"version"`
	Text           string `json:"text"`
	PayDays        int    `json:"pay_days"`       // the payment period it states (trading days after pay-out)
	EffectiveFrom  string `json:"effective_from"` // from when it applies
	WebsiteURL     string `json:"website_url"`    // where it is published on the firm's website
	ApprovedBy     string `json:"approved_by"`
	ApprovedAt     string `json:"approved_at"`
	Board          string `json:"board,omitempty"` // board or committee approval reference, when there is one
	CommunicatedOn string `json:"communicated_on,omitempty"`
	CommunicatedBy string `json:"communicated_by,omitempty"`
	Sent           int    `json:"sent,omitempty"`
	NotSent        int    `json:"not_sent,omitempty"`
}

// CUSPAPolicySent is the policy version a client was sent.
type CUSPAPolicySent struct {
	UCC     string `json:"ucc"`
	Version int    `json:"version"`
	On      string `json:"on"`
	How     string `json:"how"`
}

// CUSPAPolicyInput is a new version of the policy.
type CUSPAPolicyInput struct {
	Text          string `json:"text"`
	EffectiveFrom string `json:"effective_from"`
	WebsiteURL    string `json:"website_url"`
	Board         string `json:"board,omitempty"`
}

// CUSPAPolicyView is the policy screen.
type CUSPAPolicyView struct {
	Policy   *CUSPAPolicyRecord `json:"policy,omitempty"`
	Draft    string             `json:"draft"`
	PayDays  int                `json:"pay_days"` // Settings
	Active   int                `json:"active"`   // active trading clients
	SentNow  int                `json:"sent_now"` // of them, sent the current version
	Problems []string           `json:"problems"`
}

// CUSPAPolicyDraft is a starting text for the policy from the firm's
// settings, for the compliance officer to edit.
func (o *Ops) CUSPAPolicyDraft(ctx context.Context, tenant string) string {
	set, _ := o.Settings(ctx, tenant)
	firm := firstOf(set.Firm, "The firm")
	pay := set.cuspaPayDays()
	return strings.Join([]string{
		firm + ": policy on unpaid securities",
		"1. Scope. This policy applies to securities a client buys and does not pay for in full by the pay-in (other than under the margin trading facility). It is made under SEBI's framework for handling clients' unpaid securities (circular HO/38/11/(9)2026-MIRSD-POD/I/15382/2026) and forms part of our risk management policy.",
		"2. Pay-out and pledge. Unpaid securities are paid out to the client's demat account and pledged automatically, with the reason 'unpaid', to our client unpaid securities pledgee account (CUSPA). We tell the client by email or SMS, by the start of the next trading day, of the debit in the ledger, the funds obligation, the securities pledged and our right to sell them if the client does not pay.",
		fmt.Sprintf("3. Payment period. The client pays the amount due within %d trading day(s) after the pay-out (never more than 5 trading days).", pay),
		"4. Release. We release the pledge when the client pays, in full or in part (for the securities paid for), by the next trading day. Each day the value kept pledged (price less haircut) is at most 2.25 times the client's margin less the ledger balance; anything over it is released, oldest pledge first.",
		"5. Invocation and sale. If the amount is still due at the end of the payment period, we give the client notice by email or SMS and may then invoke the pledge, up to the 5th trading day after the pay-out, for no more than the amount due. The invoked securities are sold on the exchange under the client's own code (our own code only where the client's code may not trade, with the pay-in completed the same day); the sale's proceeds are credited to the client's ledger and the contract note says so. Any amount still due after the sale remains payable.",
		"6. Not used for funding. Securities in the CUSPA are never pledged or transferred to banks or NBFCs to raise funds, and are not used for any other client's or our own obligations.",
		"7. Auto-release and extension. What is neither released nor invoked by the 5th trading day is released by the depository at the end of the 6th trading day. Where the securities cannot be sold within that time (lower circuit after the sell order was placed, suspension or halt of trading, or another reason the exchanges and depositories accept), we may ask the depository, by 6 PM on the 5th trading day, for an extension of up to one calendar week, again while the reason lasts, and tell the client of each extension.",
		"8. Margin. Unpaid securities in the CUSPA may be counted towards the client's margin reported to the clearing corporation; no further exposure is given to the client on their basis.",
		"9. Communication. This policy is sent to every client before it applies, to every new client at account opening, and is published on our website.",
	}, "\n\n")
}

// CUSPAPolicyStatus is the policy, a draft and how far it has gone out.
func (o *Ops) CUSPAPolicyStatus(ctx context.Context, tenant string) (CUSPAPolicyView, error) {
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return CUSPAPolicyView{}, err
	}
	v := CUSPAPolicyView{Draft: o.CUSPAPolicyDraft(ctx, tenant), PayDays: set.cuspaPayDays(), Problems: []string{}}
	p, _, err := docs.Get[CUSPAPolicyRecord](ctx, o.Docs, tenant, cCUSPAPolicy, "current")
	if err == nil && p.Version > 0 {
		v.Policy = &p
	}
	cs, err := o.tradingClientMap(ctx, tenant)
	if err != nil {
		return v, err
	}
	sent := o.cuspaPolicySentMap(ctx, tenant)
	for u, c := range cs {
		if c.Status != "Active" {
			continue
		}
		v.Active++
		if v.Policy != nil && sent[u].Version == v.Policy.Version {
			v.SentNow++
		}
	}
	v.Problems = cuspaPolicyProblems(v.Policy, set, v.Active, v.SentNow)
	return v, nil
}

// cuspaPolicyProblems says what is missing from the policy and its
// communication.
func cuspaPolicyProblems(p *CUSPAPolicyRecord, set Settings, active, sentNow int) []string {
	out := []string{}
	if p == nil {
		return append(out, "No written policy on unpaid securities is recorded.")
	}
	if p.PayDays != set.cuspaPayDays() {
		out = append(out, fmt.Sprintf("The policy states %d trading day(s) to pay; Settings has %d. Record a new version.", p.PayDays, set.cuspaPayDays()))
	}
	if p.WebsiteURL == "" {
		out = append(out, "Where the policy is on the firm's website is not recorded.")
	}
	switch {
	case p.CommunicatedOn == "":
		out = append(out, fmt.Sprintf("Version %d has not been sent to clients.", p.Version))
	case p.CommunicatedOn > p.EffectiveFrom:
		out = append(out, fmt.Sprintf("Version %d was sent to clients on %s, after it applied (%s).", p.Version, fmtDay(p.CommunicatedOn), fmtDay(p.EffectiveFrom)))
	}
	if p.CommunicatedOn != "" && sentNow < active {
		out = append(out, fmt.Sprintf("%d active client(s) have not been sent version %d.", active-sentNow, p.Version))
	}
	return out
}

func (o *Ops) cuspaPolicySentMap(ctx context.Context, tenant string) map[string]CUSPAPolicySent {
	ss, _ := docs.List[CUSPAPolicySent](ctx, o.Docs, tenant, cCUSPAPolicySent, "")
	out := map[string]CUSPAPolicySent{}
	for _, s := range ss {
		out[s.UCC] = s
	}
	return out
}

// SaveCUSPAPolicy records a new version of the policy (compliance). It is
// sent to clients with CommunicateCUSPAPolicy.
func (o *Ops) SaveCUSPAPolicy(ctx context.Context, tenant, actor string, in CUSPAPolicyInput) (CUSPAPolicyRecord, error) {
	if err := o.require(ctx, tenant, actor, "Recording the policy on unpaid securities", RoleCompliance); err != nil {
		return CUSPAPolicyRecord{}, err
	}
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return CUSPAPolicyRecord{}, err
	}
	in.Text = strings.TrimSpace(in.Text)
	if len(in.Text) < 200 {
		return CUSPAPolicyRecord{}, invalid("write the policy in full: the reasons, manner and timing of invoking and releasing the pledge and of selling the securities, and the payment period")
	}
	if !validDate(in.EffectiveFrom) {
		return CUSPAPolicyRecord{}, invalid("give the date the policy applies from (YYYY-MM-DD)")
	}
	if u, err := url.Parse(strings.TrimSpace(in.WebsiteURL)); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return CUSPAPolicyRecord{}, invalid("give the address of the policy on the firm's website (https://...)")
	}
	cur, _, _ := docs.Get[CUSPAPolicyRecord](ctx, o.Docs, tenant, cCUSPAPolicy, "current")
	p := CUSPAPolicyRecord{Version: cur.Version + 1, Text: in.Text, PayDays: set.cuspaPayDays(), EffectiveFrom: in.EffectiveFrom, WebsiteURL: strings.TrimSpace(in.WebsiteURL),
		ApprovedBy: actor, ApprovedAt: o.now().Format(time.RFC3339), Board: strings.TrimSpace(in.Board)}
	if err := docs.Upsert(ctx, o.Docs, tenant, cCUSPAPolicy, fmt.Sprintf("v%d", p.Version), p, actor); err != nil {
		return p, err
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cCUSPAPolicy, "current", p, actor); err != nil {
		return p, err
	}
	o.log(ctx, tenant, actor, "cuspa.policy.saved", fmt.Sprint(p.Version), map[string]any{"effective": p.EffectiveFrom, "url": p.WebsiteURL})
	return p, nil
}

// CommunicateCUSPAPolicy sends the current policy to every active client
// not yet sent it (compliance; the daily review does it for new clients).
func (o *Ops) CommunicateCUSPAPolicy(ctx context.Context, tenant, actor, date string) (CUSPAPolicyRecord, error) {
	if err := o.require(ctx, tenant, actor, "Sending the policy on unpaid securities to clients", RoleCompliance); err != nil {
		return CUSPAPolicyRecord{}, err
	}
	if !validDate(date) {
		return CUSPAPolicyRecord{}, invalid("give a date (YYYY-MM-DD)")
	}
	return o.cuspaPolicySend(ctx, tenant, actor, date, true)
}

// cuspaPolicySend sends the current policy to active clients without it.
// first marks the communication (the compliance officer's send); the daily
// review's sends to new clients only add to the counts.
func (o *Ops) cuspaPolicySend(ctx context.Context, tenant, actor, date string, first bool) (CUSPAPolicyRecord, error) {
	p, _, err := docs.Get[CUSPAPolicyRecord](ctx, o.Docs, tenant, cCUSPAPolicy, "current")
	if err != nil || p.Version == 0 {
		return p, fmt.Errorf("%w: record the policy on unpaid securities first", ErrState)
	}
	if !first && p.CommunicatedOn == "" {
		return p, nil // not sent out yet: the compliance officer starts it
	}
	cs, err := o.tradingClientMap(ctx, tenant)
	if err != nil {
		return p, err
	}
	sent := o.cuspaPolicySentMap(ctx, tenant)
	ucs := sortedKeys(cs)
	sort.Strings(ucs)
	ok, notOK := 0, 0
	for _, u := range ucs {
		if cs[u].Status != "Active" || sent[u].Version == p.Version {
			continue
		}
		ms, err := o.sendEvent(ctx, tenant, actor, "cuspa.policy", u, map[string]string{"version": fmt.Sprint(p.Version), "effective": fmtDay(p.EffectiveFrom), "pay_days": fmt.Sprint(p.PayDays), "url": p.WebsiteURL}, "")
		note := cuspaSentNote(ms, err, "cuspa.policy")
		if !strings.HasPrefix(note, "sent by ") {
			notOK++
			continue
		}
		ok++
		_ = docs.Upsert(ctx, o.Docs, tenant, cCUSPAPolicySent, u, CUSPAPolicySent{UCC: u, Version: p.Version, On: date, How: note}, actor)
	}
	p, err = docs.Update(ctx, o.Docs, tenant, cCUSPAPolicy, "current", actor, func(x *CUSPAPolicyRecord) error {
		if first && x.CommunicatedOn == "" {
			x.CommunicatedOn, x.CommunicatedBy = date, actor
		}
		x.Sent += ok
		x.NotSent = notOK
		return nil
	})
	if err == nil {
		_ = docs.Upsert(ctx, o.Docs, tenant, cCUSPAPolicy, fmt.Sprintf("v%d", p.Version), p, actor)
		if ok > 0 || first {
			o.log(ctx, tenant, actor, "cuspa.policy.sent", fmt.Sprint(p.Version), map[string]any{"sent": ok, "not_sent": notOK, "date": date})
		}
	}
	return p, err
}

// ---- counting CUSPA as margin collected (optional) ----

// cuspaMarginValue is each client's unpaid securities held in the CUSPA on
// date, valued as margin pledges (T-1 close less haircut, collhaircut.go):
// lines paid out by date and not yet released by the depository.
func (o *Ops) cuspaMarginValue(ctx context.Context, tenant string, in riskInput, date string) map[string]money.Dec {
	out := map[string]money.Dec{}
	items, _ := docs.List[CUSPAItem](ctx, o.Docs, tenant, cCUSPA, "")
	for _, it := range items {
		if it.Qty <= 0 {
			continue
		}
		d := it.dates(in.set, date)
		if d.PayOut > date || date > d.AutoRelease {
			continue
		}
		if v, ok := pledgeValueOf(in.look, in.prevLook, it.ISIN, money.DecInt(it.Qty)); ok {
			out[it.UCC] = out[it.UCC].Add(v)
		}
	}
	return out
}

// ---- the daily review's share ----

// cuspaReviewMore is the daily review's work for the intimation, the
// policy, the last day and invoked shares of clients who cannot trade. It
// returns a note for the review's summary.
func (o *Ops) cuspaReviewMore(ctx context.Context, tenant, date string, b cuspaBook, rc *runCtx) string {
	var notes []string
	if n, failed := o.cuspaIntimate(ctx, tenant, SystemActor, date, nil); n > 0 || len(failed) > 0 {
		notes = append(notes, fmt.Sprintf("%d intimation(s) sent", n))
		for _, u := range sortedKeys(failed) {
			rc.queue(u+":intimate", "Tell "+u+" that unpaid shares are pledged to the CUSPA",
				"The intimation after the pledge (debit, obligation, shares pledged, the right to sell) "+failed[u]+". Tell the client by email or SMS before the market opens and keep the record; correct the client's email or mobile.",
				u, RoleChecker, "settlement", "", nil)
		}
	}
	if p, err := o.cuspaPolicySend(ctx, tenant, SystemActor, date, false); err == nil && p.CommunicatedOn != "" && p.NotSent > 0 {
		rc.queue("policy:"+date, "Send the policy on unpaid securities to the clients it did not reach",
			fmt.Sprintf("%d active client(s) could not be sent version %d of the policy. Correct their email or mobile and send it from Settlement → Unpaid securities → Policy.", p.NotSent, p.Version), "", RoleCompliance, "settlement", "", nil)
	}
	set := b.set
	frozen := map[string]string{}
	for _, it := range b.items {
		d := it.dates(set, date)
		if it.Qty > 0 && b.owes(it.UCC) && date == d.LastInvoke && date >= cuspaExtFrom {
			rc.queue(it.UCC+":lastday:"+date, fmt.Sprintf("Last day for the unpaid %s of %s: sell, or ask for an extension by 6 PM", it.Symbol, it.UCC),
				fmt.Sprintf("Today is the last day to invoke CUSPA line %s. Invoke and sell it today. If it cannot be sold (lower circuit after the sell order, trading suspended or halted), ask the depository for an extension of up to one week by 6 PM (Settlement → Unpaid securities → Extend, with the reason code CUSPA01 to CUSPA06); the client is told of the extension.", it.ID),
				it.UCC, RoleRisk, "settlement", date+" "+cuspaExtCutoff, nil)
		}
		// Invoked shares of a client who cannot trade are sold under the
		// firm's own code and paid in the same day (MCX/INSP/325/2025
		// amendment 3; NSE/INSP/68373 para 4).
		if it.unsold() > 0 && it.InvokedOn != "" {
			c := b.clients[it.UCC]
			why, seen := frozen[it.UCC]
			if !seen {
				why = cuspaCannotTrade(c)
				frozen[it.UCC] = why
			}
			if why != "" {
				rc.queue(it.ID+":frozen", fmt.Sprintf("Sell invoked %s of %s under the firm's own code today", it.Symbol, it.UCC),
					fmt.Sprintf("%s cannot trade (%s). Move the %d invoked shares of CUSPA line %s to the firm's own account, sell them under the firm's own (proprietary) code and complete the pay-in the same day as the invocation (%s). The proceeds go to the client's ledger.", it.UCC, why, it.unsold(), it.ID, fmtDay(it.InvokedOn)),
					it.UCC, RoleRisk, "settlement", it.InvokedOn+" 23:59", nil)
			}
		}
	}
	return strings.Join(notes, "; ")
}

// cuspaCannotTrade says why a client may not sell under its own code (""
// when it may): its account is not active, inactive at the exchanges, has
// no segment, or is blocked for a reason other than a debit.
func cuspaCannotTrade(c Client) string {
	switch {
	case c.UCC == "":
		return "not on record"
	case c.Status != "Active":
		return "account " + strings.ToLower(firstOf(c.Status, "not active"))
	case c.Inactive:
		return "marked inactive at the exchanges"
	case len(c.OpenSegments()) == 0:
		return "no segment active"
	case c.Blocked && !strings.HasPrefix(c.BlockedReason, debitReason):
		// A block for a debit stops buying only; any other block (the RMS
		// kill switch, a regulator's order) stops the sale too.
		return firstOf(c.BlockedReason, "blocked")
	}
	return ""
}
