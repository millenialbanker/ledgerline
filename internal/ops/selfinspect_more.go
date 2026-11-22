package ops

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
)

// ---- self-inspection tests added from the penalty audit ----
//
// Each closes a gap found by reading the exchanges' lists of common
// inspection findings and recent penalty orders (the help article "Penalty
// risks and how Ledgerline covers them", config/help/45-penalty-risks.md):
//
//   - NSE/INSP/73503 (circular 9/2026) of 27 March 2026, Annexure A-B:
//     common violations found in inspections from October 2024 to
//     September 2025.
//   - NSE/INSP/65693 of 19 December 2024: the same for April to
//     September 2024.
//   - SEBI adjudication orders: Axis Securities (21 February 2025),
//     Prabhudas Lilladher (16 July 2025); NSE's Member Committee order
//     against Simandhar Broking (23 May 2025).

// siTradesSince is the exchange trades of the window ending on the date.
func siTradesSince(e *siEnv, from string) ([]ingest.TradeEvent, error) {
	all, err := e.o.App.Trades.List(e.ctx, e.tenant, ingest.FromExchange, "")
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, t := range all {
		if t.TradeDate >= from && t.TradeDate <= e.date {
			out = append(out, t)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].TradeDate+out[i].UCC < out[j].TradeDate+out[j].UCC })
	return out, nil
}

// ---- terminals ----

// siDealerTerminals: business from terminals no dealer is registered on,
// or by a dealer without a valid NISM certificate on the day.
func siDealerTerminals(e *siEnv) (siOut, error) {
	ds, err := e.o.allDealers(e.ctx, e.tenant)
	if err != nil {
		return siOut{}, err
	}
	if len(ds) == 0 {
		return siSkip("Waiting for data: no dealer or terminal is recorded on Dealers & terminals.")
	}
	from := addDays(e.date, -siWindow)
	ts, err := siTradesSince(e, from)
	if err != nil {
		return siOut{}, err
	}
	with := ts[:0]
	for _, t := range ts {
		if strings.TrimSpace(t.Terminal) != "" {
			with = append(with, t)
		}
	}
	if len(ts) > 0 && len(with) == 0 {
		return siSkip("Not checkable: the trade files of the last 30 days carry no terminal IDs.")
	}
	var s siOut
	s.input("Trades with their terminals", from)
	s.input("Dealers & terminals", e.date)
	rep := dealerAttribution(ds, with)
	s.checked = rep.Trades
	for _, u := range rep.Unattributed {
		s.found += u.Trades - 1
		s.bad(fmt.Sprintf("%s %s terminal %s · %d trades of %d clients from %s to %s, on no dealer's record", u.Exchange, u.Segment, u.Terminal, u.Trades, u.Clients, fmtDay(u.First), fmtDay(u.Last)))
	}
	for _, x := range rep.ExpiredNISM {
		s.found += x.Trades - 1
		s.bad(fmt.Sprintf("%s terminal %s · %s on %s · %d trades: %s", x.Exchange, x.Terminal, x.DealerID, fmtDay(x.Date), x.Trades, x.Reason))
	}
	s.summary = fmt.Sprintf("%d trades of the last %d days with a terminal ID checked; %d from terminals on no dealer's record or by dealers without a valid NISM certificate.", s.checked, siWindow, s.found)
	return s, nil
}

// ---- minors ----

// siAgeOn is a client's age in whole years on a day (-1 when the date of
// birth is not known).
func siAgeOn(dob, day string) int {
	b, err1 := time.Parse("2006-01-02", strings.TrimSpace(dob))
	d, err2 := time.Parse("2006-01-02", day)
	if err1 != nil || err2 != nil {
		return -1
	}
	age := d.Year() - b.Year()
	if d.Month() < b.Month() || (d.Month() == b.Month() && d.Day() < b.Day()) {
		age-- // the birthday of the year is still to come
	}
	return age
}

// siMinorTrading: a minor's account traded in derivatives or intraday.
func siMinorTrading(e *siEnv) (siOut, error) {
	from := addDays(e.date, -siWindow)
	ts, err := siTradesSince(e, from)
	if err != nil {
		return siOut{}, err
	}
	var s siOut
	s.input("Trades", from)
	s.input("Clients' dates of birth", e.date)
	type day struct{ buy, sell map[string]bool }
	days := map[string]*day{}
	deriv := map[string]bool{}
	minors := map[string]bool{}
	for _, t := range ts {
		c := e.clients[strings.ToUpper(t.UCC)]
		if c.Type != "Individual" && c.Type != "" {
			continue
		}
		if a := siAgeOn(c.DOB, t.TradeDate); a < 0 || a >= 18 {
			continue
		}
		k := t.UCC + "|" + t.TradeDate
		minors[t.UCC] = true
		if t.Segment != "CM" {
			deriv[k] = true
			continue
		}
		d := days[k]
		if d == nil {
			d = &day{buy: map[string]bool{}, sell: map[string]bool{}}
			days[k] = d
		}
		sc := t.Exchange + "|" + t.Symbol
		if t.Side == "B" {
			d.buy[sc] = true
		} else {
			d.sell[sc] = true
		}
	}
	keys := map[string]bool{}
	for k := range deriv {
		keys[k] = true
	}
	for k := range days {
		keys[k] = true
	}
	for _, k := range sortedKeys(keys) {
		ucc, d, _ := strings.Cut(k, "|")
		s.checked++
		var why []string
		if deriv[k] {
			why = append(why, "derivatives")
		}
		if x := days[k]; x != nil {
			for sc := range x.buy {
				if x.sell[sc] {
					why = append(why, "bought and sold the same scrip the same day (intraday)")
					break
				}
			}
		}
		if len(why) > 0 {
			s.bad(fmt.Sprintf("%s · a minor · traded on %s: %s", siClient(ucc), fmtDay(d), strings.Join(why, "; ")))
		}
	}
	for _, u := range sortedKeys(e.clients) {
		c := e.clients[u]
		if c.Status != "Active" || c.DPOnly {
			continue
		}
		if a := siAgeOn(c.DOB, e.date); a < 0 || a >= 18 {
			continue
		}
		minors[u] = true
		for _, g := range c.Segments {
			if g != "CM" {
				s.checked++
				s.bad(fmt.Sprintf("%s · a minor · %s segment enabled", siClient(u), g))
				break
			}
		}
	}
	s.summary = fmt.Sprintf("%d minors' accounts; %d trading days and accounts checked; %d problems.", len(minors), s.checked, s.found)
	return s, nil
}

// ---- KYC before trading ----

// siKYCMissing are the six KYC attributes a client lacks (name, PAN,
// address, mobile, e-mail, income range).
func siKYCMissing(c Client, d UCCDetails, app *Application) []string {
	var m []string
	if strings.TrimSpace(c.Name) == "" {
		m = append(m, "name")
	}
	if !rePAN.MatchString(c.PAN) {
		m = append(m, "PAN")
	}
	if a := uccAddress(d, app); strings.TrimSpace(a[0]) == "" && strings.TrimSpace(a[3]) == "" {
		m = append(m, "address")
	}
	if strings.TrimSpace(c.Mobile) == "" {
		m = append(m, "mobile")
	}
	if strings.TrimSpace(c.Email) == "" {
		m = append(m, "e-mail")
	}
	if strings.TrimSpace(c.Income) == "" {
		m = append(m, "income range")
	}
	return m
}

// siKYCBeforeTrade: trades by clients whose KYC was not complete.
func siKYCBeforeTrade(e *siEnv) (siOut, error) {
	from := addDays(e.date, -siWindow)
	ts, err := siTradesSince(e, from)
	if err != nil {
		return siOut{}, err
	}
	var s siOut
	s.input("Trades", from)
	s.input("Clients' KYC", e.date)
	src := e.o.uccSources(e.ctx, e.tenant)
	regs, _ := docs.List[NSEUCIReg](e.ctx, e.o.Docs, e.tenant, cUCCReg, "NSE|")
	reg := map[string]NSEUCIReg{}
	for _, r := range regs {
		reg[r.UCC] = r
	}
	first := map[string]string{}
	for _, t := range ts {
		u := strings.ToUpper(t.UCC)
		if first[u] == "" || t.TradeDate < first[u] {
			first[u] = t.TradeDate
		}
	}
	for _, u := range sortedKeys(first) {
		c, ok := e.clients[u]
		if !ok {
			continue
		}
		s.checked++
		var why []string
		if c.KRAStatus != "Validated" && c.KRAStatus != "Registered" {
			why = append(why, "KRA "+strings.ToLower(firstOf(c.KRAStatus, "status not loaded")))
		}
		if m := siKYCMissing(c, src.details[u], src.app(u)); len(m) > 0 {
			why = append(why, "no "+strings.Join(m, ", "))
		}
		if r, ok := reg[u]; ok {
			if r.PANStatus == "I" {
				why = append(why, "PAN not verified at NSE")
			}
			if c.Type == "Individual" && strings.HasPrefix(strings.ToUpper(strings.TrimSpace(r.AadhaarSeeding)), "N") {
				why = append(why, "PAN not linked with Aadhaar (NSE)")
			}
		}
		if len(why) > 0 {
			s.bad(fmt.Sprintf("%s · traded from %s · %s", siClient(u), fmtDay(first[u]), strings.Join(why, "; ")))
		}
	}
	s.summary = fmt.Sprintf("%d clients who traded in the last %d days checked; %d traded without complete, validated KYC.", s.checked, siWindow, s.found)
	return s, nil
}

// ---- shared mobile numbers and e-mail addresses ----

// siFamily are the relations a shared mobile or e-mail may be declared
// with (the client's spouse, dependent children and dependent parents).
var siFamily = map[string]bool{"spouse": true, "dependent child": true, "dependent children": true, "dependent parent": true, "dependent parents": true}

// siSharedContacts: one mobile or e-mail on several clients not declared as
// family.
func siSharedContacts(e *siEnv) (siOut, error) {
	var s siOut
	s.input("Clients' mobiles and e-mails", e.date)
	details := e.o.uccSources(e.ctx, e.tenant).details
	type member struct{ ucc, rel string }
	groups := map[string][]member{}
	for _, u := range sortedKeys(e.clients) {
		c := e.clients[u]
		if c.Status != "Active" || c.DPOnly {
			continue
		}
		s.checked++
		d := details[u]
		if m := uccNorm("mobile", c.Mobile); m != "" {
			groups["mobile|"+m] = append(groups["mobile|"+m], member{u, strings.ToLower(strings.TrimSpace(d.MobileRelation))})
		}
		if m := uccNorm("email", c.Email); m != "" {
			groups["email|"+m] = append(groups["email|"+m], member{u, strings.ToLower(strings.TrimSpace(d.EmailRelation))})
		}
	}
	shared := 0
	for _, k := range sortedKeys(groups) {
		g := groups[k]
		if len(g) < 2 {
			continue
		}
		shared++
		own := 0
		for _, m := range g {
			if !siFamily[m.rel] {
				own++ // the holder's own (Self), or no relation declared
			}
		}
		if own <= 1 {
			continue
		}
		kind, v, _ := strings.Cut(k, "|")
		tail := uccTail(kind, v)
		var who []string
		for i, m := range g {
			if i == 3 {
				who = append(who, fmt.Sprintf("and %d more", len(g)-3))
				break
			}
			who = append(who, siMask(m.ucc))
		}
		s.bad(fmt.Sprintf("%s %s · on %d clients (%s); %d not declared as the holder's family", map[string]string{"mobile": "Mobile", "email": "E-mail"}[kind], tail, len(g), strings.Join(who, ", "), own))
	}
	s.summary = fmt.Sprintf("%d active clients checked; %d mobiles or e-mails shared; %d shared by clients not declared as one family.", s.checked, shared, s.found)
	return s, nil
}

// ---- payouts within one working day ----

func siPayoutOneDay(e *siEnv) (siOut, error) {
	var s siOut
	from := addDays(e.date, -siWindow)
	s.input("Payout requests", from)
	prs, err := docs.List[PayoutRequest](e.ctx, e.o.Docs, e.tenant, cPayout, "")
	if err != nil {
		return s, err
	}
	bs, err := e.o.PayoutBatches(e.ctx, e.tenant)
	if err != nil {
		return s, err
	}
	paidOn := map[string]string{}
	for _, b := range bs {
		paidOn[b.ID] = b.Date
	}
	sort.SliceStable(prs, func(i, j int) bool { return prs[i].ID < prs[j].ID })
	for _, p := range prs {
		if p.Date < from || p.Date > e.date || p.Status == "Rejected" || p.Status == "Cancelled" || p.Amount <= 0 {
			continue
		}
		due := e.set.NextBusinessDay(p.Date)
		s.checked++
		switch on := paidOn[p.Batch]; {
		case on != "" && on > due:
			s.bad(fmt.Sprintf("Payout %s · %s · ₹%s asked on %s, paid on %s (due by %s)", p.ID, siClient(p.UCC), formatINR(p.Amount.Dec()), fmtDay(p.Date), fmtDay(on), fmtDay(due)))
		case on == "" && due < e.date && p.Status != "Paid":
			s.bad(fmt.Sprintf("Payout %s · %s · ₹%s asked on %s, %s, not paid by %s", p.ID, siClient(p.UCC), formatINR(p.Amount.Dec()), fmtDay(p.Date), strings.ToLower(p.Status), fmtDay(due)))
		}
	}
	s.summary = fmt.Sprintf("%d payout requests of the last %d days checked; %d not paid within one working day.", s.checked, siWindow, s.found)
	return s, nil
}

// ---- statements ----

// siDelivered is when each document was first delivered, by kind|ref|ucc.
func siDelivered(e *siEnv) (map[string]string, error) {
	ds, err := docs.List[Dispatch](e.ctx, e.o.Docs, e.tenant, cDispatch, "")
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, d := range ds {
		if d.Status != "Sent" && d.Status != "Physical sent" && d.Status != "Physical pending" {
			continue
		}
		at := dayOf(firstOf(firstOf(d.SentAt, d.CourierAt), d.Created))
		if at == "" {
			continue
		}
		if p := out[d.Key]; p == "" || at < p {
			out[d.Key] = at
		}
	}
	return out, nil
}

// siMarginStatements: each client in a day's margin report sent the daily
// margin statement by the next trading day.
func siMarginStatements(e *siEnv) (siOut, error) {
	var s siOut
	sent, err := siDelivered(e)
	if err != nil {
		return s, err
	}
	since := siFirstTrade(e)
	if since == "" {
		return siSkip("Nothing to check: no trades in Ledgerline yet.")
	}
	d, days := e.date, 0
	for i := 0; i < 8 && days < 5; i++ {
		d = e.set.PrevBusinessDay(d)
		if d < since {
			break
		}
		due := e.set.NextBusinessDay(d)
		if due >= e.date {
			continue // the day's statements are not yet due
		}
		rep, err := e.o.Margins(e.ctx, e.tenant, d)
		if err != nil || len(rep.Rows) == 0 {
			continue
		}
		days++
		s.input("Margin report and statements sent", d)
		for _, r := range rep.Rows {
			s.checked++
			at := sent[DocMargin+"|"+d+"|"+r.UCC]
			switch {
			case at == "":
				s.bad(fmt.Sprintf("Margin statement of %s · %s · not sent", fmtDay(d), siClient(r.UCC)))
			case at > due:
				s.bad(fmt.Sprintf("Margin statement of %s · %s · sent on %s, after %s", fmtDay(d), siClient(r.UCC), fmtDay(at), fmtDay(due)))
			}
		}
	}
	if days == 0 {
		return siSkip("Nothing to check yet: no trading day with margins whose statements are due.", s.inputs...)
	}
	s.summary = fmt.Sprintf("%d daily margin statements due on %d trading days; %d not sent by the next trading day.", s.checked, days, s.found)
	return s, nil
}

// siRASStatementDays is the working days after a settlement by which its
// statement is sent.
const siRASStatementDays = 5

// siRASStatements: each client settled (paid or retained) sent the
// statement of the settlement.
func siRASStatements(e *siEnv) (siOut, error) {
	var s siOut
	from := addDays(e.date, -2*siWindow)
	s.input("Running account settlements", from)
	sent, err := siDelivered(e)
	if err != nil {
		return s, err
	}
	bs, err := docs.List[RASBatch](e.ctx, e.o.Docs, e.tenant, cRASBatch, "")
	if err != nil {
		return s, err
	}
	for _, b := range bs {
		due := addTradingDays(e.set, b.Date, siRASStatementDays)
		if b.Date < from || due >= e.date {
			continue
		}
		for _, it := range append(append([]RASItem(nil), b.Items...), b.Retained...) {
			s.checked++
			at := sent[DocRAS+"|"+b.Date+"|"+it.UCC]
			switch {
			case at == "":
				s.bad(fmt.Sprintf("Settlement of %s · %s · statement not sent", fmtDay(b.Date), siClient(it.UCC)))
			case at > due:
				s.bad(fmt.Sprintf("Settlement of %s · %s · statement sent on %s, after %s", fmtDay(b.Date), siClient(it.UCC), fmtDay(at), fmtDay(due)))
			}
		}
	}
	s.summary = fmt.Sprintf("%d clients settled checked; %d without the settlement's statement within %d working days.", s.checked, s.found, siRASStatementDays)
	return s, nil
}
