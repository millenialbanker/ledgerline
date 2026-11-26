package ops

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
)

// ---- firm modules: the one place a broker says what it does ----
//
// Settings → Firm modules is where a broker chooses its modules: which
// exchange segments it trades and how each is cleared (not traded, a
// trading member cleared by another clearing member, a self-clearing
// member, or a clearing member that also clears for other trading members
// or custodial participants), whether it is a CDSL or NSDL participant, the
// products it offers, the depository options it uses and facts about the
// firm itself. With the number of clients Ledgerline counts, the answers
// decide which compliance applies: calendar duties ("applies" conditions
// in config/compliance/duties.json, read by dutyNeed), self-inspection
// tests (siApplies), periodic tasks and the screens in the menu
// (ScreenGates).
//
// One source of truth for each answer:
//   - the segment roles are Settings.ClearingFor and Settings.MemberCodes
//     (written by this screen through saveSettings, so the Settings screen's
//     "Cleared through" and this screen can never disagree); only "clears
//     for other members as well" is kept here, in ClearsOthers;
//   - CDSL, NSDL, algo and internet trading stay on the firm profile
//     (FirmProfile) and MTF on the compliance profile, as before;
//   - everything new is in FirmModules (collection firmmodules, "main").
//
// Existing tenants' settings load unchanged: nothing here is read until
// it is answered, and an unanswered switch is "not known" (the duty shows
// as Not sure; it is never nagged or blocked on).

const (
	cFirmModules = "firmmodules"
	fmMain       = "main"
	fmCounted    = "counted"   // the latest client count (a snapshot)
	fmCMChanges  = "cmchanges" // changes of clearing member made in Settings
)

// Segment roles on the Firm modules screen.
const (
	SegNone = "none" // not traded
	SegTM   = "tm"   // trading member: another clearing member clears
	SegSelf = "self" // self-clearing member
	SegPCM  = "pcm"  // clearing member that also clears for other TMs or CPs
)

// FirmSwitch is one yes-or-no switch of the screen. ID is the condition
// it answers (dutyNeed reads it by that name).
type FirmSwitch struct {
	ID    string `json:"id"`
	Group string `json:"group"` // Products, Depository, Firm
	Label string `json:"label"`
	Note  string `json:"note"`
	// Exchanges are where the product is offered, when it matters which
	// (the firm ticks them; the condition is firm-wide).
	Exchanges []string `json:"exchanges,omitempty"`
}

// FirmSwitches are the switches of Settings → Firm modules beyond the
// ones the firm profile already had (CDSL, NSDL, algo, internet trading,
// MTF). Add a line here and a condition of the same name works in
// duties.json, self-inspection and the menu.
var FirmSwitches = []FirmSwitch{
	// Products
	{"api", "Products", "Client API access (clients trade through an API the firm gives them)", "SEBI retail algo framework (SEBI/HO/MIRSD/MIRSD-PoD/P/CIR/2025/132): API orders only through exchange-registered algos.", []string{"NSE", "BSE", "MCX"}},
	{"dma", "Products", "Direct market access (DMA) for institutional clients", "DMA conditions in the stock brokers' master circular.", []string{"NSE", "BSE"}},
	{"sor", "Products", "Smart order routing (SOR)", "SOR approval and audit conditions in the stock brokers' master circular.", []string{"NSE", "BSE"}},
	{"epi:funds", "Products", "Early pay-in of funds for margin benefit", "NCL/CMPT/69520: allocation by NMASS and the CEPF report.", nil},
	{"mfos", "Products", "Client money in mutual fund overnight schemes (MFOS)", "Client Nodal MFOS demat account, units pledged to the clearing corporation (NSE/INSP/57041).", nil},
	{"tpp", "Products", "Third-party products distributed (mutual funds, insurance, bonds, NPS …)", "Client consent before sharing data with a TPP provider; TPP money kept out of the broking books (NSE/INSP/68566).", nil},
	{"inst", "Products", "Institutional or custodial (DVP) clients", "Custodial participant codes and confirmation by the custodian.", nil},
	{"prop:only", "Products", "Only proprietary trading or institutional clients (no retail clients)", "Some retail-client duties do not apply.", nil},
	{"clientbg", "Products", "Client bank guarantees accepted for commodity hedging", "Annex-44 of the stock brokers' master circular: eligible clients, approved banks, 25% cap.", []string{"MCX", "NSE"}},
	{"hedge", "Products", "Clients hold MCX hedge limits", "Commodity master circular para 3.7.2.", []string{"MCX"}},
	{"sgtcm", "Products", "Opted into SGTCM (the stock broker's own clearing member arrangement)", "As the exchange's SGTCM circular says.", nil},
	{"buyback", "Products", "Buyback orders placed through the exchange mechanism", "Tendering by the exchange mechanism.", []string{"NSE", "BSE"}},
	{"corpbond:prop", "Products", "Proprietary trading in corporate bonds", "Reporting of the firm's own corporate bond deals.", nil},
	{"underwrite", "Products", "Underwriting of issues", "Underwriting registration conditions.", nil},
	// Depository options
	{"dp:saral", "Depository", "SARAL demat accounts", "SARAL account opening and limits.", nil},
	{"dp:escrow", "Depository", "Escrow, unclaimed or suspense demat accounts", "CDSL Operating Instructions 2.3.15-2.3.17: closure after the period, debits only after verifying the claimant.", nil},
	{"dp:fax", "Depository", "Fax instructions accepted", "CDSL Operating Instructions 6.5.6: indemnity, time stamp, original DIS within 3 working days.", nil},
	{"dp:einstr", "Depository", "Digitally signed e-instructions accepted", "CDSL Operating Instructions 17.8: DSC check, logs, link to CDSL transaction IDs.", nil},
	{"dp:bankbo", "Depository", "Banks as demat account holders (bank BOs)", "CDSL Operating Instructions 16.5: weekly holding statement every Friday.", nil},
	{"dp:mfredeem", "Depository", "Mutual fund redemption through the DP", "CDSL Operating Instructions 18.6: AMFI/NISM certificate, Annexure 18.4.", nil},
	{"dp:accredited", "Depository", "Accredited investor applications processed", "Accreditation agency work as the depository's circular says.", nil},
	{"dp:ipopledge", "Depository", "IPO allotment pledge facility used", "As the depository's circular says.", nil},
	{"dp:nilbal", "Depository", "Nil-balance statement exemption used", "No physical statement for nil-balance accounts as allowed.", nil},
	{"dp:dropbox", "Depository", "Drop boxes for instruction slips", "Drop-box controls in the depository's operating instructions.", nil},
	{"dp:arbitration", "Depository", "Depository arbitration cases", "Arbitration under the depository's bye-laws.", nil},
	// The firm itself
	{"gstin:multi", "Firm", "GST registrations in more than one state", "A GST return for each GSTIN.", nil},
	{"fy:nonmarch", "Firm", "Accounting year not ending on 31 March", "Tell MCX and file the annual returns within six months of the year end (MCX/MEM/577/2026).", nil},
	{"fingroup", "Firm", "Part of a financial group (bank, NBFC, insurer …)", "Group-level policies and related-party rules.", nil},
	{"overseas", "Firm", "Overseas offices, subsidiaries or units", "Overseas investment and reporting rules.", nil},
	{"otherreg", "Firm", "Also regulated by RBI, IRDAI, PFRDA or another regulator", "That regulator's returns and inspections.", nil},
	{"upsi", "Firm", "Handles unpublished price sensitive information (issuer or merchant banking mandates)", "Structured digital database kept in house for 8 years (PIT Regulations reg 3(5)-(6)).", nil},
	{"gift", "Firm", "A GIFT IFSC or NDS-OM unit", "The IFSCA or RBI rules for that unit.", nil},
	{"subkua", "Firm", "Aadhaar e-KYC as a sub-KUA", "Consent naming the KUA and sub-KUA, OTP or biometric through the KUA, authentication logs (KYC master circular paras 75-76).", nil},
	{"cloud", "Firm", "Uses cloud or SaaS services for regulated work", "SEBI cloud framework; Ledgerline itself is a cloud service when it is hosted.", nil},
	{"rule237", "Firm", "A reporting person under rule 237 of the Income-tax Rules", "Statements of specified transactions.", nil},
	{"sdf", "Firm", "Notified as a significant data fiduciary (DPDP Act)", "Data protection officer, yearly DPIA and audit.", nil},
	{"pricedata", "Firm", "Shares real-time or delayed price data with others", "Exchange data-feed agreements.", nil},
	{"ra:public", "Firm", "Publicly gives advice or recommendations on securities", "Research analyst rules and the exchanges' advisory notices.", nil},
	{"bo:inhouse", "Firm", "Back office software developed in house", "Ledgerline is a vendor's back office, so normally No: the vendor's undertakings cover it.", nil},
}

func firmSwitch(id string) (FirmSwitch, bool) {
	for _, s := range FirmSwitches {
		if s.ID == id {
			return s, true
		}
	}
	return FirmSwitch{}, false
}

// Constitutions of a firm.
var Constitutions = []struct{ ID, Name string }{
	{"proprietor", "Sole proprietor"}, {"partnership", "Partnership firm"}, {"llp", "Limited liability partnership"}, {"company", "Company"},
}

// CountOverride is a number the firm gave instead of Ledgerline's count.
type CountOverride struct {
	Value int64  `json:"value"`
	Note  string `json:"note,omitempty"`
	By    string `json:"by"`
	At    string `json:"at"`
}

// FirmModules is what the firm answered on Settings → Firm modules beyond
// the firm profile.
type FirmModules struct {
	// ClearsOthers: for a segment the firm clears itself, "yes" when it
	// also clears for other trading members or custodial participants
	// (a TM-CM or a professional clearing member), "no", or "" not answered.
	ClearsOthers map[string]string   `json:"clears_others,omitempty"`
	Switches     map[string]string   `json:"switches,omitempty"` // FirmSwitch.ID → yes, no or ""
	On           map[string][]string `json:"on,omitempty"`       // FirmSwitch.ID → exchanges it is offered on
	Constitution string              `json:"constitution,omitempty"`
	// QSB: "yes" (designated a qualified stock broker), "ex" (was one;
	// QSBEndedOn says when it stopped) or "no". The exchanges publish the
	// list each year (NSE/INSP/73575).
	QSB        string `json:"qsb,omitempty"`
	QSBEndedOn string `json:"qsb_ended_on,omitempty"`
	// Specified: on NSE's list of specified stockbrokers (top 20 IBT/STWT
	// brokers by active clients) under the technical glitch framework.
	Specified string `json:"specified,omitempty"`
	// CMChangedOn: the last change of clearing member the firm tells
	// Ledgerline about (changes made in Settings are recorded on their own).
	CMChangedOn string                   `json:"cm_changed_on,omitempty"`
	Counts      map[string]CountOverride `json:"counts,omitempty"`
	By          string                   `json:"by,omitempty"`
	At          string                   `json:"at,omitempty"`
}

// Client number keys.
const (
	CountRegistered = "registered"
	CountActive     = "active"
	CountIBT        = "ibt"
	CountVolume     = "volume_cr"
	countAPI        = "api_clients"
)

var countLabels = map[string]string{
	CountRegistered: "Registered clients",
	CountActive:     "Active clients (traded in the last 12 months)",
	CountIBT:        "Clients registered for internet or mobile trading (IBT/STWT)",
	CountVolume:     "Clients' trading volume in the last financial year (₹ crore)",
}

// fmCount is Ledgerline's own count of the firm's clients.
type fmCount struct {
	Counts map[string]int64 `json:"counts"`
	// Partial: the count is a lower bound because Ledgerline's trades do
	// not cover the whole period (key → why).
	Partial    map[string]string `json:"partial,omitempty"`
	FY         string            `json:"fy"`          // the financial year the volume is for
	FirstTrade string            `json:"first_trade"` // the first exchange trade in Ledgerline
	Date       string            `json:"date"`        // counted as on
	At         string            `json:"at"`
}

// cmChange is one change of the clearing route of a segment.
type cmChange struct {
	Date    string `json:"date"`
	Segment string `json:"segment"`
	From    string `json:"from"`
	To      string `json:"to"`
	By      string `json:"by"`
}

type cmChangeLog struct {
	Changes []cmChange `json:"changes"`
}

func (o *Ops) firmModules(ctx context.Context, tenant string) FirmModules {
	m, _, _ := docs.GetOr(ctx, o.Docs, tenant, cFirmModules, fmMain, FirmModules{})
	return m
}

func (o *Ops) firmCount(ctx context.Context, tenant string) fmCount {
	c, _, _ := docs.GetOr(ctx, o.Docs, tenant, cFirmModules, fmCounted, fmCount{})
	return c
}

// ---- counting clients ----

// countClients counts the firm's clients from Ledgerline's own data:
//   - registered: trading accounts past onboarding (Active, Dormant and so
//     on; not Closed, not still an application, not DP-only);
//   - active: registered clients who traded in any segment in the 12
//     months to date (the NSE meaning of an active client), from the
//     exchange trade files and the last trade date migrated with the
//     client;
//   - volume: the value of the clients' exchange trades in the last full
//     financial year (quantity × price; options at their premium), the
//     firm's own codes left out because they are not client records;
//   - client API: clients marked for API access.
//
// When the trades start inside the period the count is a lower bound and
// says so.
func (o *Ops) countClients(ctx context.Context, tenant, date string) (fmCount, error) {
	cs, err := o.Clients(ctx, tenant)
	if err != nil {
		return fmCount{}, err
	}
	c := fmCount{Counts: map[string]int64{}, Partial: map[string]string{}, Date: date, At: o.now().Format(time.RFC3339)}
	reg := map[string]bool{}
	var api int64
	from := addMonths(date, -12)
	active := map[string]bool{}
	for _, x := range cs {
		if x.Status == "Closed" || x.DPOnly || contains(Stages[:3], x.Status) {
			continue
		}
		reg[x.UCC] = true
		if x.API {
			api++
		}
		if x.LastTrade > from && x.LastTrade <= date {
			active[x.UCC] = true
		}
	}
	ts, err := o.App.Trades.List(ctx, tenant, ingest.FromExchange, "")
	if err != nil {
		return c, err
	}
	fy, fyStart, fyEnd := fyOf(addDays(fyStartOf(date), -1))
	c.FY = fy
	var vol float64
	for _, t := range ts {
		if c.FirstTrade == "" || t.TradeDate < c.FirstTrade {
			c.FirstTrade = t.TradeDate
		}
		if !reg[t.UCC] {
			continue
		}
		if t.TradeDate > from && t.TradeDate <= date {
			active[t.UCC] = true
		}
		if t.TradeDate >= fyStart && t.TradeDate <= fyEnd {
			p, _ := strconv.ParseFloat(t.Price.String(), 64)
			vol += p * float64(t.Qty)
		}
	}
	c.Counts[CountRegistered] = int64(len(reg))
	c.Counts[CountActive] = int64(len(active))
	c.Counts[CountVolume] = int64(vol / 1e7)
	c.Counts[countAPI] = api
	switch {
	case c.FirstTrade == "":
		c.Partial[CountActive] = "no exchange trades in Ledgerline yet"
		c.Partial[CountVolume] = "no exchange trades in Ledgerline yet"
	default:
		if c.FirstTrade > addDays(from, 7) {
			c.Partial[CountActive] = "Ledgerline's trades start on " + fmtDay(c.FirstTrade)
		}
		if c.FirstTrade > addDays(fyStart, 7) {
			c.Partial[CountVolume] = "Ledgerline's trades start on " + fmtDay(c.FirstTrade)
		}
	}
	if len(cs) == 0 {
		c.Partial[CountRegistered] = "no clients in Ledgerline yet"
	}
	return c, nil
}

// recountClients counts now and keeps the count for the rules.
func (o *Ops) recountClients(ctx context.Context, tenant string) (fmCount, error) {
	c, err := o.countClients(ctx, tenant, o.now().In(ist).Format("2006-01-02"))
	if err != nil {
		return c, err
	}
	return c, docs.Upsert(ctx, o.Docs, tenant, cFirmModules, fmCounted, c, SystemActor)
}

func init() {
	// Count once a day so the rules read a fresh number without counting
	// the trades each time the calendar is opened.
	boardTickHooks = append(boardTickHooks, func(ctx context.Context, o *Ops, tenant string, now time.Time) {
		if c := o.firmCount(ctx, tenant); c.Date == now.In(ist).Format("2006-01-02") {
			return
		}
		_, _ = o.recountClients(ctx, tenant)
	})
}

// clientNumber is a number the rules use: the firm's figure, else
// Ledgerline's count. exact is false when the count is a lower bound.
type clientNumber struct {
	Value int64
	Known bool // there is a figure at all
	Exact bool // and it is not a lower bound
	From  string
}

func numberFor(key string, m FirmModules, c fmCount) clientNumber {
	if ov, ok := m.Counts[key]; ok {
		return clientNumber{Value: ov.Value, Known: true, Exact: true, From: "your figure (" + ov.By + ")"}
	}
	v, ok := c.Counts[key]
	if !ok || key == CountIBT {
		return clientNumber{}
	}
	why, partial := c.Partial[key]
	if partial && v == 0 {
		return clientNumber{From: why}
	}
	return clientNumber{Value: v, Known: true, Exact: !partial, From: "Ledgerline's count" + map[bool]string{true: " (at least: " + why + ")"}[partial]}
}

// cscrfRank orders the CSCRF categories from exempt (0) to qualified (4).
var cscrfRank = map[string]int{"exempt": 0, "self": 1, "small": 2, "mid": 3, "qualified": 4}
var cscrfByRank = []string{"exempt", "self", "small", "mid", "qualified"}

// cscrfFromNumbers is the category the numbers put the firm in under
// SEBI/HO/ITD-1/ITD_CSC_EXT/P/CIR/2025/60: the higher of the category by
// registered clients and by clients' trading volume in the year. Exactly
// 1,000 clients or ₹1,000 crore is read as self-certification (the
// exemption is for fewer than 1,000 and less than ₹1,000 crore). sure is
// false when a measure is missing or a lower bound and the other does not
// already reach the top.
func cscrfFromNumbers(reg, vol clientNumber) (cat string, sure bool) {
	byClients := func(n int64) int {
		switch {
		case n > 1000000:
			return 4
		case n > 100000:
			return 3
		case n > 10000:
			return 2
		case n >= 1000:
			return 1
		}
		return 0
	}
	byVolume := func(cr int64) int {
		switch {
		case cr > 1000000:
			return 4
		case cr > 100000:
			return 3
		case cr > 10000:
			return 2
		case cr >= 1000:
			return 1
		}
		return 0
	}
	if !reg.Known && !vol.Known {
		return "", false
	}
	r := 0
	if reg.Known {
		r = byClients(reg.Value)
	}
	if vol.Known {
		r = max(r, byVolume(vol.Value))
	}
	sure = (reg.Known && reg.Exact && vol.Known && vol.Exact) || r == 4
	return cscrfByRank[r], sure
}

// ---- the facts the rules read ----

// moduleFacts adds what Firm modules knows to the firm facts.
func (o *Ops) moduleFacts(ctx context.Context, tenant string, f *firmFacts, facts *[]FirmFact, set Settings, p FirmProfile, m FirmModules) {
	f.Sw = map[string]tri{}
	f.SwFrom = map[string]string{}
	c := o.firmCount(ctx, tenant)
	f.Count = c
	f.Mods = m
	ans := func(v string) tri {
		switch v {
		case AnswerYes:
			return triYes
		case AnswerNo:
			return triNo
		}
		return triUnknown
	}
	// What Ledgerline finds for some switches.
	found := map[string]tri{}
	from := map[string]string{}
	if n, ok := c.Counts[countAPI]; ok && n > 0 {
		found["api"], from["api"] = triYes, fmt.Sprintf("Clients: %d with API access", n)
	}
	if contains(p.Registrations, "mfd") {
		found["tpp"], from["tpp"] = triYes, "Firm profile: mutual fund distributor"
	}
	if contains(p.Registrations, "mb") {
		found["upsi"], from["upsi"] = triYes, "Firm profile: merchant banker"
	}
	switch o.deployment(set).Deployment {
	case "hosted", "own_cloud":
		found["cloud"], from["cloud"] = triYes, "Ledgerline runs in the cloud for the firm (Exchange disclosures → deployment)"
	}
	found["bo:inhouse"], from["bo:inhouse"] = triNo, "Ledgerline is the firm's back office, from a vendor"
	for _, s := range FirmSwitches {
		v := ans(m.Switches[s.ID])
		f.SwFrom[s.ID] = "Firm modules (your answer)"
		if v == triUnknown {
			v, f.SwFrom[s.ID] = found[s.ID], firstOf(from[s.ID], "not answered")
		}
		f.Sw[s.ID] = v
	}
	f.Const = m.Constitution

	// Clearing for others.
	selfSegs, yes, no := 0, 0, 0
	for k, r := range f.Role {
		if r != SegSelf {
			continue
		}
		selfSegs++
		switch m.ClearsOthers[k] {
		case AnswerYes:
			f.Role[k] = SegPCM
			yes++
		case AnswerNo:
			no++
		}
	}
	switch {
	case yes > 0:
		f.PCM = triYes
	case selfSegs == 0 || no == selfSegs:
		f.PCM = triNo
	default:
		f.PCM = triUnknown
	}

	// A change of clearing member in the last 30 days.
	today := o.now().In(ist).Format("2006-01-02")
	last := m.CMChangedOn
	ch, _, _ := docs.GetOr(ctx, o.Docs, tenant, cFirmModules, fmCMChanges, cmChangeLog{})
	for _, x := range ch.Changes {
		if x.Date > last {
			last = x.Date
		}
	}
	f.CMChangeOn = last
	f.CMChange = triOf(last != "" && last > addDays(today, -30) && last <= today)

	// The numbers.
	f.Registered, f.Active, f.IBTClients, f.Volume = numberFor(CountRegistered, m, c), numberFor(CountActive, m, c), numberFor(CountIBT, m, c), numberFor(CountVolume, m, c)
	f.CSCRFCounted, f.CSCRFSure = cscrfFromNumbers(f.Registered, f.Volume)
	if f.CSCRF == "" && f.CSCRFSure {
		f.CSCRF, f.CSCRFFrom = f.CSCRFCounted, "counted"
	}

	// QSB and specified stockbroker: firm answers (the exchanges publish
	// the lists).
	f.QSB, f.QSBEx, f.QSBExUntil = triUnknown, triUnknown, ""
	switch m.QSB {
	case AnswerYes:
		f.QSB, f.QSBEx = triYes, triNo
	case AnswerNo:
		f.QSB, f.QSBEx = triNo, triNo
	case "ex":
		f.QSB = triNo
		if m.QSBEndedOn != "" {
			_, _, end := fyOf(m.QSBEndedOn)
			f.QSBExUntil = addMonths(end, 36) // three more financial years (NSE/INSP/73575)
			f.QSBEx = triOf(today <= f.QSBExUntil)
		}
	}
	f.Specified = ans(m.Specified)

	known := func(v tri) bool { return v != triUnknown }
	word := map[tri]string{triYes: "Yes", triNo: "No", triUnknown: "Not known"}
	*facts = append(*facts,
		FirmFact{Key: "pcm", Label: "Clears for other trading members or custodial participants", Value: word[f.PCM], Known: known(f.PCM), From: "Settings → Firm modules"},
		FirmFact{Key: "qsb", Label: "Qualified stock broker", Value: word[f.QSB] + map[bool]string{true: " (ex-QSB until " + fmtDay(f.QSBExUntil) + ")"}[f.QSBEx == triYes], Known: known(f.QSB), From: "Settings → Firm modules"},
	)
}

// moduleNeed reads the conditions of the Firm modules vocabulary; ok is
// false for a condition it does not know.
func moduleNeed(n string, f firmFacts) (v tri, why string, ok bool) {
	ask := func(what string) string { return "tell Ledgerline " + what + " (Settings → Firm modules)" }
	fact := func(v tri, yes, no, unknown string) (tri, string, bool) {
		switch v {
		case triYes:
			return triYes, yes, true
		case triNo:
			return triNo, no, true
		}
		return triUnknown, unknown, true
	}
	switch {
	case strings.HasPrefix(n, "xseg:"):
		k := n[5:]
		name := strings.Replace(k, "-", " ", 1)
		return fact(triOf(f.XSeg[k]), "you trade "+name, "you don't trade "+name+" (Settings → Firm modules)", "")
	case strings.HasPrefix(n, "selfat:"):
		cc := n[7:]
		return fact(triOf(f.SelfAt[cc]), "you clear your own trades at "+firstOf(ClearingCorpNames[cc], cc), "you don't clear your own trades at "+firstOf(ClearingCorpNames[cc], cc), "")
	case n == "pcm":
		return fact(f.PCM, "you clear for other trading members or custodial participants", "you clear only your own trades, if any", ask("whether the firm clears trades of other trading members or custodial participants"))
	case n == "cmchange":
		return fact(f.CMChange, "you changed clearing member on "+fmtDay(f.CMChangeOn), "no change of clearing member in the last 30 days", ask("when the firm last changed its clearing member"))
	case n == "slb":
		return fact(triOf(f.XSeg["NSE-SLB"] || f.XSeg["BSE-SLB"]), "you take part in securities lending and borrowing (SLB)", "you don't trade an SLB segment (Settings → Firm modules)", "")
	case strings.HasPrefix(n, "clients:"):
		m := reClientsNeed.FindStringSubmatch(n)
		if m == nil {
			return triUnknown, "unknown condition " + n, true
		}
		want, _ := strconv.ParseInt(m[2], 10, 64)
		num, label := f.Active, "active clients"
		if m[1] == "registered" {
			num, label = f.Registered, "registered clients"
		}
		switch {
		case num.Known && num.Value >= want:
			return triYes, fmt.Sprintf("you have %s %s (%s)", fmtCount(num.Value), label, num.From), true
		case num.Known && num.Exact:
			return triNo, fmt.Sprintf("you have %s %s, fewer than %s", fmtCount(num.Value), label, fmtCount(want)), true
		}
		return triUnknown, ask("the number of " + label), true
	case n == "ibt:10k":
		switch {
		case f.IBT == triNo:
			return triNo, "clients don't trade through your website or app", true
		case f.IBTClients.Known && f.IBTClients.Value > 10000 && f.IBT == triYes:
			return triYes, fmt.Sprintf("%s clients are registered for internet or mobile trading", fmtCount(f.IBTClients.Value)), true
		case f.IBTClients.Known && f.IBTClients.Value <= 10000:
			return triNo, fmt.Sprintf("%s clients registered for internet or mobile trading, not more than 10,000", fmtCount(f.IBTClients.Value)), true
		case f.Registered.Known && f.Registered.Exact && f.Registered.Value <= 10000:
			return triNo, fmt.Sprintf("you have %s registered clients in all, not more than 10,000", fmtCount(f.Registered.Value)), true
		}
		return triUnknown, ask("whether clients trade through the firm's website or app and how many are registered for it"), true
	case n == "specified":
		// NSE names specified stockbrokers only among brokers with more
		// than 10,000 online clients, so a firm below that is not one
		// unless it says so.
		if f.Specified == triUnknown {
			if v, _, _ := moduleNeed("ibt:10k", f); v == triNo {
				return triNo, "NSE names specified stockbrokers only among brokers with more than 10,000 online clients", true
			}
		}
		return fact(f.Specified, "you are on NSE's list of specified stockbrokers", "you are not a specified stockbroker", ask("whether the firm is on NSE's list of specified stockbrokers"))
	case n == "qsb":
		return fact(f.QSB, "you are a qualified stock broker", "you are not a qualified stock broker", ask("whether the firm is a qualified stock broker"))
	case n == "qsb:ex":
		return fact(f.QSBEx, "you were a qualified stock broker: the obligations continue until "+fmtDay(f.QSBExUntil), "you are not an ex-QSB within three financial years", ask("whether and when the firm stopped being a qualified stock broker"))
	case strings.HasPrefix(n, "const:"):
		x := n[6:]
		name := constitutionName(x)
		if f.Const == "" {
			return triUnknown, ask("the firm's constitution"), true
		}
		return fact(triOf(f.Const == x), "the firm is a "+strings.ToLower(name), "the firm is a "+strings.ToLower(constitutionName(f.Const))+", not a "+strings.ToLower(name), "")
	}
	if s, known := firmSwitch(n); known {
		label := strings.ToLower(s.Label[:1]) + s.Label[1:]
		return fact(f.Sw[n], "Firm modules: "+label, "Firm modules: not "+label, ask("whether: "+label))
	}
	return triUnknown, "", false
}

var reClientsNeed = regexp.MustCompile(`^clients:(active|registered)>=(\d+)$`)

func constitutionName(id string) string {
	for _, c := range Constitutions {
		if c.ID == id {
			return c.Name
		}
	}
	return id
}

func fmtCount(n int64) string { return formatINRInt(n) }

// formatINRInt writes a whole number in the Indian grouping (12,34,567).
func formatINRInt(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	if len(s) > 3 {
		head, tail := s[:len(s)-3], s[len(s)-3:]
		var parts []string
		for len(head) > 2 {
			parts = append([]string{head[len(head)-2:]}, parts...)
			head = head[:len(head)-2]
		}
		if head != "" {
			parts = append([]string{head}, parts...)
		}
		s = strings.Join(parts, ",") + "," + tail
	}
	if neg {
		return "-" + s
	}
	return s
}

// moduleCondKnown checks a condition of the Firm modules vocabulary.
func moduleCondKnown(n string) bool {
	switch {
	case strings.HasPrefix(n, "xseg:"):
		_, ok := clearingSeg(n[5:])
		return ok
	case strings.HasPrefix(n, "selfat:"):
		_, ok := ClearingCorpNames[n[7:]]
		return ok
	case strings.HasPrefix(n, "const:"):
		return constitutionName(n[6:]) != n[6:]
	case reClientsNeed.MatchString(n):
		return true
	}
	switch n {
	case "pcm", "cmchange", "slb", "ibt:10k", "specified", "qsb", "qsb:ex":
		return true
	}
	_, ok := firmSwitch(n)
	return ok
}

// ModuleConditions is the vocabulary of conditions, for the screen and the
// help: what each means.
func ModuleConditions() []struct{ Cond, Means string } {
	out := []struct{ Cond, Means string }{
		{"broker", "a trading member of any exchange"}, {"member:NSE", "a member of that exchange (NSE, BSE, MCX, MSE, NCDEX)"},
		{"seg:CM", "trades that kind of segment somewhere (CM, FO, CD, COM, SLB)"}, {"derivatives", "trades a derivatives segment"},
		{"clearing", "clears its own trades somewhere"}, {"dp", "a CDSL or NSDL participant"}, {"cdsl", "a CDSL participant"}, {"nsdl", "an NSDL participant"},
		{"mtf", "offers MTF"}, {"algo", "runs or offers algo trading"}, {"ibt", "clients trade through the firm's website or app"},
		{"nri", "has NRI clients"}, {"ap", "has authorised persons"}, {"branch", "has branches"}, {"ras:monthly", "some clients settled monthly"},
		{"ra|ia|pms", "holds that SEBI registration"}, {"cscrf:<ids>", "the CSCRF category is one of these (the firm's answer, else counted)"},
	}
	for _, c := range ClearingSegments {
		out = append(out, struct{ Cond, Means string }{"xseg:" + c.Key, "trades " + c.Exchange + " " + c.Segment})
	}
	for _, cc := range []string{"NCL", "ICCL", "MCXCCL", "NCCL", "MCCIL"} {
		out = append(out, struct{ Cond, Means string }{"selfat:" + cc, "clears its own trades at " + ClearingCorpNames[cc]})
	}
	out = append(out, []struct{ Cond, Means string }{
		{"pcm", "clears for other trading members or custodial participants (TM-CM or professional clearing member)"},
		{"cmchange", "changed clearing member in the last 30 days"}, {"slb", "trades NSE or BSE SLB"},
		{"clients:active>=2000", "2,000 or more active clients (automated surveillance)"},
		{"ibt:10k", "internet or mobile trading with more than 10,000 registered clients (technical glitch framework)"},
		{"specified", "on NSE's list of specified stockbrokers"}, {"qsb", "a qualified stock broker"}, {"qsb:ex", "was a QSB within the last three financial years"},
	}...)
	for _, s := range FirmSwitches {
		out = append(out, struct{ Cond, Means string }{s.ID, s.Label})
	}
	for _, c := range Constitutions {
		out = append(out, struct{ Cond, Means string }{"const:" + c.ID, c.Name})
	}
	return out
}

// ---- screens and self-inspection tests that belong to a module ----

// ScreenGate hides a menu screen, or a part of a screen ("screen.part"),
// when its condition does not hold for the firm. An unknown answer shows
// it. Later branches add a line for their screens.
type ScreenGate struct {
	Key  string `json:"key"`
	Cond string `json:"cond"`
	What string `json:"what"`
}

// ScreenGates is the one table of screens that belong to a module.
var ScreenGates = []ScreenGate{
	{"mtf", "mtf", "Margin trading (MTF)"},
	{"dp", "dp", "Depository: demat accounts and DIS"},
	{"cdslreq", "cdsl", "CDSL requests"},
	{"edis", "cdsl", "eDIS authorisations (CDSL TPIN)"},
	{"cdsltxn", "cdsl", "CDSL transactions by API"},
	{"nri", "nri", "NRI (PIS)"},
	{"dealers.algo", "algo|api", "Algo terminal fields on Dealers & terminals"},
	{"recon.mcx", "member:MCX", "The MCX filter on Trade reconciliation"},
	{"settle.mtm", "derivatives", "The derivatives MTM tab on Settlement"},
}

// siApplies are the self-inspection tests that belong to a module: a test
// whose condition does not hold is skipped as not applicable (unknown
// runs it, as before).
var siApplies = map[string][]string{
	"bsda_review":      {"dp"},
	"dp_pass_through":  {"dp"},
	"dis_setup_delay":  {"dp"},
	"aop_accounts":     {"dp"},
	"ro_kit_delivery":  {"dp"},
	"dis_books":        {"dp"},
	"dis_checks":       {"dp"},
	"dp_request_tat":   {"dp"},
	"dp_recon_daily":   {"dp"},
	"dis_stock":        {"dp"},
	"dp_adverse_7":     {"dp"},
	"cdsl_kyc_freeze":  {"cdsl"},
	"cdsl_mnrl_freeze": {"cdsl"},
	"position_limits":  {"derivatives"},
	"index_limits":     {"derivatives"},
	"fo_ban_period":    {"derivatives"},
	"nse_user_limits":  {"member:NSE"},
	"bse_complaints":   {"member:BSE"},
	"segment_deposit":  {"selfat:NCL|selfat:ICCL"},
	"segmon":           {"broker"},
}

// GateState is how a gated screen stands for the firm.
type GateState struct {
	ScreenGate
	Verdict string `json:"verdict"` // yes, no, unsure
	Why     string `json:"why"`
	Hidden  bool   `json:"hidden"`
}

func gateStates(f firmFacts) []GateState {
	out := make([]GateState, 0, len(ScreenGates))
	for _, g := range ScreenGates {
		a := dutyApplies(strings.Split(g.Cond, ","), f)
		out = append(out, GateState{ScreenGate: g, Verdict: a.Verdict, Why: a.Reason, Hidden: a.Verdict == ApplyNo})
	}
	return out
}

// HiddenScreens are the screens (and parts) Firm modules hides from the
// menu, with why. The console shows them anyway to someone who asks to see
// all modules.
func (o *Ops) HiddenScreens(ctx context.Context, tenant string) map[string]string {
	f, _ := o.firmProfileFactsOpt(ctx, tenant, false)
	out := map[string]string{}
	for _, g := range gateStates(f) {
		if g.Hidden {
			out[g.Key] = g.Why
		}
	}
	return out
}

// ---- the screen ----

// ModSegment is one exchange segment on the screen.
type ModSegment struct {
	ClearingSegment
	Role   string `json:"role"` // SegNone, SegTM, SegSelf, SegPCM; "self?" when self-clearing and not answered whether for others
	CC     string `json:"cc,omitempty"`
	CMCode string `json:"cm_code,omitempty"`
	CMName string `json:"cm_name,omitempty"`
}

// ModSwitchView is a switch with its answer and what Ledgerline finds.
type ModSwitchView struct {
	FirmSwitch
	Answer string   `json:"answer"` // yes, no or ""
	Value  string   `json:"value"`  // in words: Yes, No, Not known
	From   string   `json:"from"`
	On     []string `json:"on,omitempty"`
	Store  string   `json:"store,omitempty"` // profile (kept on the firm profile) or compliance (compliance profile)
}

// ClientCountView is one client number.
type ClientCountView struct {
	Key      string         `json:"key"`
	Label    string         `json:"label"`
	Counted  *int64         `json:"counted,omitempty"`
	Partial  string         `json:"partial,omitempty"`
	How      string         `json:"how"`
	Override *CountOverride `json:"override,omitempty"`
	Value    *int64         `json:"value,omitempty"`
}

// DerivedView is what the numbers and answers make of the firm.
type DerivedView struct {
	Key     string `json:"key"`
	Label   string `json:"label"`
	Verdict string `json:"verdict"`
	Value   string `json:"value"`
	Why     string `json:"why"`
	Warn    string `json:"warn,omitempty"`
}

// CondView is one condition of the vocabulary as it stands for the firm.
type CondView struct {
	Cond    string `json:"cond"`
	Means   string `json:"means"`
	Verdict string `json:"verdict"`
	Why     string `json:"why"`
}

// FirmModulesView is Settings → Firm modules.
type FirmModulesView struct {
	Segments      []ModSegment      `json:"segments"`
	MemberCodes   map[string]string `json:"member_codes"`
	Exchanges     []string          `json:"exchanges"`
	ClearingCorps map[string]string `json:"clearing_corps"`
	Assumed       bool              `json:"assumed"`
	Depository    []ModSwitchView   `json:"depository"`
	Products      []ModSwitchView   `json:"products"`
	DPOptions     []ModSwitchView   `json:"dp_options"`
	Firm          []ModSwitchView   `json:"firm"`
	Constitution  string            `json:"constitution"`
	Constitutions any               `json:"constitutions"`
	QSB           string            `json:"qsb"`
	QSBEndedOn    string            `json:"qsb_ended_on,omitempty"`
	QSBExUntil    string            `json:"qsb_ex_until,omitempty"`
	Specified     string            `json:"specified"`
	CMChangedOn   string            `json:"cm_changed_on,omitempty"`
	CMChanges     []cmChange        `json:"cm_changes"`
	Counts        []ClientCountView `json:"counts"`
	CountedAt     string            `json:"counted_at,omitempty"`
	CountFY       string            `json:"count_fy,omitempty"`
	CSCRF         string            `json:"cscrf"` // the firm's answer
	CSCRFCounted  string            `json:"cscrf_counted,omitempty"`
	Categories    any               `json:"cscrf_categories"`
	Derived       []DerivedView     `json:"derived"`
	Conditions    []CondView        `json:"conditions"`
	Screens       []GateState       `json:"screens"`
	Tests         map[string]string `json:"test_conditions"`
	CanEdit       bool              `json:"can_edit"`
	By            string            `json:"by,omitempty"`
	At            string            `json:"at,omitempty"`
	Unknown       []string          `json:"unknown"` // questions still open
	Profile       FirmProfile       `json:"profile"`
	MTF           bool              `json:"mtf"`
}

var verdictWord = map[tri]string{triYes: ApplyYes, triNo: ApplyNo, triUnknown: ApplyUnsure}

// FirmModulesFor is the screen. Everyone on the staff may read it (it
// holds no client data); the compliance officer and an Admin change it.
func (o *Ops) FirmModulesFor(ctx context.Context, tenant, actor string) (FirmModulesView, error) {
	if err := o.require(ctx, tenant, actor, "Viewing the firm modules", RoleMaker, RoleChecker, RoleRisk, RoleCompliance, RoleAdmin); err != nil {
		return FirmModulesView{}, err
	}
	if o.firmCount(ctx, tenant).Date == "" {
		_, _ = o.recountClients(ctx, tenant)
	}
	set, _ := o.Settings(ctx, tenant)
	f, _ := o.firmProfileFactsOpt(ctx, tenant, false)
	v := o.modulesView(ctx, tenant, set, o.firmProfile(ctx, tenant), o.CompProfile(ctx, tenant), o.firmModules(ctx, tenant), f)
	v.CanEdit = o.require(ctx, tenant, actor, "Changing the firm modules", RoleCompliance) == nil
	return v, nil
}

func (o *Ops) modulesView(ctx context.Context, tenant string, set Settings, p FirmProfile, cp ComplianceProfile, m FirmModules, f firmFacts) FirmModulesView {
	v := FirmModulesView{MemberCodes: map[string]string{}, ClearingCorps: ClearingCorpNames, Assumed: f.Assumed, Constitutions: Constitutions, Constitution: m.Constitution,
		QSB: m.QSB, QSBEndedOn: m.QSBEndedOn, QSBExUntil: f.QSBExUntil, Specified: m.Specified, CMChangedOn: m.CMChangedOn, CSCRF: p.CSCRF, CSCRFCounted: f.CSCRFCounted,
		Categories: CSCRFCategories, By: m.By, At: m.At, Profile: p, MTF: cp.MTF, Tests: map[string]string{}, Unknown: []string{}}
	for k, x := range set.MemberCodes {
		v.MemberCodes[k] = x
	}
	for _, c := range ClearingSegments {
		if !contains(v.Exchanges, c.Exchange) {
			v.Exchanges = append(v.Exchanges, c.Exchange)
		}
		s := ModSegment{ClearingSegment: c, Role: SegNone}
		ct := set.ClearedThrough(c.Key)
		switch {
		case !set.tradesOn(c.Exchange) || ct == ClearNone:
		case strings.HasPrefix(ct, "CM:"):
			s.Role, s.CMCode, s.CMName = SegTM, ct[3:], set.ClearingMemberNames[c.Key]
		default:
			s.CC = ct
			switch m.ClearsOthers[c.Key] {
			case AnswerYes:
				s.Role = SegPCM
			case AnswerNo:
				s.Role = SegSelf
			default:
				s.Role = "self?"
			}
		}
		v.Segments = append(v.Segments, s)
	}
	ch, _, _ := docs.GetOr(ctx, o.Docs, tenant, cFirmModules, fmCMChanges, cmChangeLog{})
	v.CMChanges = append([]cmChange{}, ch.Changes...)
	sort.Slice(v.CMChanges, func(i, j int) bool { return v.CMChanges[i].Date > v.CMChanges[j].Date })

	word := map[tri]string{triYes: "Yes", triNo: "No", triUnknown: "Not known"}
	profileSw := func(id, label, answer string, val tri, note string) ModSwitchView {
		x := ModSwitchView{FirmSwitch: FirmSwitch{ID: id, Label: label, Note: note}, Answer: answer, Value: word[val], Store: "profile", From: "Firm profile"}
		if answer == "" {
			x.From = "what Ledgerline finds"
		}
		return x
	}
	v.Depository = []ModSwitchView{
		profileSw("cdsl", "CDSL depository participant", p.CDSL, f.CDSL, "CDSL's DP duties and the depository screens."),
		profileSw("nsdl", "NSDL depository participant", p.NSDL, f.NSDL, "NSDL's DP duties."),
	}
	mtf := ModSwitchView{FirmSwitch: FirmSwitch{ID: "mtf", Label: "Margin trading facility (MTF)", Note: "Kept on the compliance profile; the MTF screen and its duties."}, Answer: map[bool]string{true: AnswerYes, false: AnswerNo}[cp.MTF], Value: word[f.MTF], Store: "compliance", From: "Compliance profile"}
	v.Products = []ModSwitchView{mtf,
		profileSw("algo", "Algorithmic trading (the firm's own, or clients' through its systems)", p.Algo, f.Algo, "Algo duties, the half-yearly AI & ML report and cyber audit."),
		profileSw("ibt", "Internet or mobile trading (IBT or STWT)", p.IBT, f.IBT, "IBT controls; with more than 10,000 registered clients, the technical glitch framework."),
		{FirmSwitch: FirmSwitch{ID: "slb", Label: "Securities lending and borrowing (SLB)", Note: "Choose NSE-SLB or BSE-SLB in the segments above."}, Value: word[triOf(f.XSeg["NSE-SLB"] || f.XSeg["BSE-SLB"])], From: "the segments above", Store: "segments"},
	}
	for _, s := range FirmSwitches {
		x := ModSwitchView{FirmSwitch: s, Answer: m.Switches[s.ID], Value: word[f.Sw[s.ID]], From: f.SwFrom[s.ID], On: m.On[s.ID]}
		switch s.Group {
		case "Products":
			v.Products = append(v.Products, x)
		case "Depository":
			v.DPOptions = append(v.DPOptions, x)
		default:
			v.Firm = append(v.Firm, x)
		}
	}

	// The client numbers.
	c := f.Count
	how := map[string]string{
		CountRegistered: "Trading accounts in the client list past onboarding: not closed, not an application still in KYC or UCC upload, not DP-only.",
		CountActive:     "Registered clients with a trade in any exchange segment in the 12 months to " + fmtDay(firstOf(c.Date, "today")) + " (NSE's meaning of an active client), from the exchange trade files and the last trade date migrated with each client.",
		CountIBT:        "Ledgerline cannot count these: the trading platform holds the internet and mobile trading log-ins. Enter the number.",
		CountVolume:     "The value of clients' exchange trades in FY " + firstOf(c.FY, "last year") + " (quantity × price; options at their premium). The firm's own codes are left out.",
	}
	for _, k := range []string{CountRegistered, CountActive, CountIBT, CountVolume} {
		x := ClientCountView{Key: k, Label: countLabels[k], How: how[k], Partial: c.Partial[k]}
		if n, ok := c.Counts[k]; ok && k != CountIBT {
			n := n
			x.Counted = &n
		}
		if ov, ok := m.Counts[k]; ok {
			ov := ov
			x.Override = &ov
		}
		if nb := numberFor(k, m, c); nb.Known {
			n := nb.Value
			x.Value = &n
		}
		v.Counts = append(v.Counts, x)
	}
	v.CountedAt, v.CountFY = c.At, c.FY

	// What the numbers and answers make of the firm.
	d := func(key, label, cond, note string) {
		t, why := dutyNeed(cond, f)
		v.Derived = append(v.Derived, DerivedView{Key: key, Label: label, Verdict: verdictWord[t], Value: map[tri]string{triYes: "Yes", triNo: "No", triUnknown: "Not known"}[t] + note, Why: why})
	}
	cat := DerivedView{Key: "cscrf", Label: "CSCRF category (SEBI/HO/ITD-1/ITD_CSC_EXT/P/CIR/2025/60)"}
	switch {
	case p.CSCRF != "":
		cat.Verdict, cat.Value, cat.Why = ApplyYes, cscrfName(p.CSCRF), "your answer"
		if f.CSCRFCounted != "" && f.CSCRFCounted != p.CSCRF && (f.CSCRFSure || cscrfRank[f.CSCRFCounted] > cscrfRank[p.CSCRF]) {
			cat.Warn = "Ledgerline's numbers put the firm in " + cscrfName(f.CSCRFCounted) + map[bool]string{false: " at least"}[f.CSCRFSure] + ": check the numbers or your answer. Your answer is what the rules use."
		}
	case f.CSCRFSure:
		cat.Verdict, cat.Value, cat.Why = ApplyYes, cscrfName(f.CSCRFCounted), "worked out from the client numbers below (no answer given)"
	case f.CSCRFCounted != "":
		cat.Verdict, cat.Value, cat.Why = ApplyUnsure, "At least "+cscrfName(f.CSCRFCounted), "a number is missing or covers only part of the year: give the figure or the category"
	default:
		cat.Verdict, cat.Value, cat.Why = ApplyUnsure, "Not known", "give the number of registered clients and the clients' trading volume, or the category"
	}
	v.Derived = append(v.Derived, cat)
	d("surveillance", "Automated surveillance needed (2,000 or more active clients, NSE/INVG/71818)", "clients:active>=2000", "")
	d("glitch", "Technical glitch framework (IBT/STWT with more than 10,000 registered clients)", "ibt:10k", "")
	d("specified", "Specified stockbroker (NSE's list)", "specified", "")
	qn := ""
	if f.QSBEx == triYes {
		qn = " (ex-QSB: obligations continue until " + fmtDay(f.QSBExUntil) + ")"
	}
	d("qsb", "Qualified stock broker (the exchanges' list)", "qsb|qsb:ex", qn)

	for _, c := range ModuleConditions() {
		if strings.Contains(c.Cond, "<") || strings.Contains(c.Cond, "|") {
			continue
		}
		t, why := dutyNeed(c.Cond, f)
		v.Conditions = append(v.Conditions, CondView{Cond: c.Cond, Means: c.Means, Verdict: verdictWord[t], Why: why})
		if t == triUnknown && strings.Contains(why, "Settings → Firm") && !contains(v.Unknown, why) {
			v.Unknown = append(v.Unknown, why)
		}
	}
	v.Screens = gateStates(f)
	for id, n := range siApplies {
		v.Tests[id] = strings.Join(n, " and ")
	}
	return v
}

// ---- saving ----

// SegChoice is a segment's role as the screen sends it.
type SegChoice struct {
	Role   string `json:"role"`
	CC     string `json:"cc,omitempty"`
	CMCode string `json:"cm_code,omitempty"`
	CMName string `json:"cm_name,omitempty"`
}

// CountInput is a figure the firm gives (nil Value takes it back).
type CountInput struct {
	Value *int64 `json:"value"`
	Note  string `json:"note,omitempty"`
}

// FirmModulesReq is a change on the screen. A field left out is not
// changed.
type FirmModulesReq struct {
	Segments     map[string]SegChoice   `json:"segments,omitempty"`
	MemberCodes  map[string]string      `json:"member_codes,omitempty"`
	CDSL         *string                `json:"cdsl,omitempty"`
	NSDL         *string                `json:"nsdl,omitempty"`
	Algo         *string                `json:"algo,omitempty"`
	IBT          *string                `json:"ibt,omitempty"`
	MTF          *bool                  `json:"mtf,omitempty"`
	Switches     map[string]string      `json:"switches,omitempty"`
	On           map[string][]string    `json:"on,omitempty"`
	Constitution *string                `json:"constitution,omitempty"`
	QSB          *string                `json:"qsb,omitempty"`
	QSBEndedOn   *string                `json:"qsb_ended_on,omitempty"`
	Specified    *string                `json:"specified,omitempty"`
	CMChangedOn  *string                `json:"cm_changed_on,omitempty"`
	Counts       map[string]*CountInput `json:"counts,omitempty"`
	CSCRF        *string                `json:"cscrf,omitempty"`
}

// modulesState is everything the screen changes.
type modulesState struct {
	set  Settings
	prof FirmProfile
	comp ComplianceProfile
	mods FirmModules
}

func (o *Ops) currentModules(ctx context.Context, tenant string) (modulesState, error) {
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return modulesState{}, err
	}
	m := o.firmModules(ctx, tenant)
	return modulesState{set: set, prof: o.firmProfile(ctx, tenant), comp: o.CompProfile(ctx, tenant), mods: m}, nil
}

func cloneMap[K comparable, V any](m map[K]V) map[K]V {
	out := make(map[K]V, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func validAnswer(v string) bool { return v == "" || v == AnswerYes || v == AnswerNo }

// applyModules applies a change to the current state and says, in words,
// what changed. Nothing is saved.
func (o *Ops) applyModules(cur modulesState, actor string, now time.Time, r FirmModulesReq) (modulesState, []string, error) {
	st := cur
	st.set.ClearingFor, st.set.ClearingMemberNames, st.set.MemberCodes = cloneMap(cur.set.ClearingFor), cloneMap(cur.set.ClearingMemberNames), cloneMap(cur.set.MemberCodes)
	if st.set.ClearingFor == nil {
		st.set.ClearingFor = map[string]string{}
	}
	m := cur.mods
	m.ClearsOthers, m.Switches, m.On, m.Counts = cloneMap(m.ClearsOthers), cloneMap(m.Switches), cloneMap(m.On), cloneMap(m.Counts)
	var changes []string
	note := func(f string, a ...any) { changes = append(changes, fmt.Sprintf(f, a...)) }
	ts := now.Format(time.RFC3339)

	if r.MemberCodes != nil {
		for k, v := range r.MemberCodes {
			k, v = strings.ToUpper(strings.TrimSpace(k)), strings.TrimSpace(v)
			if !contains(discExchangeOrder, k) {
				return st, nil, invalid("%s is not an exchange Ledgerline knows", k)
			}
			if v != st.set.MemberCodes[k] {
				note("member code %s: %s → %s", k, firstOf(st.set.MemberCodes[k], "none"), firstOf(v, "none"))
			}
			if v == "" {
				delete(st.set.MemberCodes, k)
			} else {
				st.set.MemberCodes[k] = v
			}
		}
	}
	roleName := map[string]string{SegNone: "not traded", SegTM: "trading member, cleared by another clearing member", SegSelf: "self-clearing", SegPCM: "clears for other members as well"}
	for k, c := range r.Segments {
		seg, ok := clearingSeg(strings.ToUpper(k))
		if !ok {
			return st, nil, invalid("%s is not an exchange segment Ledgerline knows", k)
		}
		k = seg.Key
		before := st.set.ClearedThrough(k)
		switch c.Role {
		case SegNone:
			st.set.ClearingFor[k] = ClearNone
			delete(st.set.ClearingMemberNames, k)
			delete(m.ClearsOthers, k)
		case SegTM:
			code, name := strings.ToUpper(strings.TrimSpace(c.CMCode)), strings.TrimSpace(c.CMName)
			if code == "" || name == "" {
				return st, nil, invalid("give the code and name of the clearing member that clears %s", k)
			}
			st.set.ClearingFor[k], st.set.ClearingMemberNames[k] = "CM:"+code, name
			delete(m.ClearsOthers, k)
		case SegSelf, SegPCM, "self?":
			cc := strings.ToUpper(strings.TrimSpace(firstOf(c.CC, seg.Default)))
			if cc == ClearNone {
				cc = seg.Allowed[0]
			}
			if !contains(seg.Allowed, cc) {
				return st, nil, invalid("%s can be cleared at %s; not %s", k, strings.Join(seg.Allowed, " or "), cc)
			}
			st.set.ClearingFor[k] = cc
			delete(st.set.ClearingMemberNames, k)
			switch c.Role {
			case SegPCM:
				m.ClearsOthers[k] = AnswerYes
			case SegSelf:
				m.ClearsOthers[k] = AnswerNo
			}
		default:
			return st, nil, invalid("the role of %s is not traded, trading member, self-clearing or clears for others", k)
		}
		if after := st.set.ClearedThrough(k); after != before || cur.mods.ClearsOthers[k] != m.ClearsOthers[k] {
			note("%s: %s", k, roleName[strings.TrimSuffix(c.Role, "?")]+map[bool]string{true: " at " + after}[!strings.HasPrefix(after, "CM:") && after != ClearNone])
		}
	}
	// A traded segment needs the exchange's member code, unless no code is
	// given at all (then Ledgerline assumes NSE, BSE and MCX as before).
	if len(memberExchanges(st.set)) > 0 {
		for _, c := range ClearingSegments {
			if r.Segments[c.Key].Role != "" && r.Segments[c.Key].Role != SegNone && strings.TrimSpace(st.set.MemberCodes[c.Exchange]) == "" {
				return st, nil, invalid("give the firm's %s member code to trade %s", c.Exchange, c.Key)
			}
		}
	}
	yn := func(name string, p *string, dst *string) error {
		if p == nil {
			return nil
		}
		if !validAnswer(*p) {
			return invalid("%s is yes, no, or left to what Ledgerline knows", name)
		}
		if *p != *dst {
			note("%s: %s → %s", name, firstOf(*dst, "not answered"), firstOf(*p, "not answered"))
		}
		*dst = *p
		return nil
	}
	for _, x := range []struct {
		n   string
		p   *string
		dst *string
	}{{"CDSL participant", r.CDSL, &st.prof.CDSL}, {"NSDL participant", r.NSDL, &st.prof.NSDL}, {"algo trading", r.Algo, &st.prof.Algo}, {"internet or mobile trading", r.IBT, &st.prof.IBT}, {"specified stockbroker", r.Specified, &m.Specified}} {
		if err := yn(x.n, x.p, x.dst); err != nil {
			return st, nil, err
		}
	}
	if r.MTF != nil && *r.MTF != st.comp.MTF {
		note("MTF: %v → %v", map[bool]string{true: "yes", false: "no"}[st.comp.MTF], map[bool]string{true: "yes", false: "no"}[*r.MTF])
		st.comp.MTF = *r.MTF
	}
	for id, v := range r.Switches {
		s, ok := firmSwitch(id)
		if !ok {
			return st, nil, invalid("%s is not a switch of Firm modules", id)
		}
		if !validAnswer(v) {
			return st, nil, invalid("%s is yes, no, or not answered", s.Label)
		}
		if v != m.Switches[id] {
			note("%s: %s → %s", s.Label, firstOf(m.Switches[id], "not answered"), firstOf(v, "not answered"))
		}
		if v == "" {
			delete(m.Switches, id)
		} else {
			m.Switches[id] = v
		}
	}
	for id, exs := range r.On {
		s, ok := firmSwitch(id)
		if !ok {
			return st, nil, invalid("%s is not a switch of Firm modules", id)
		}
		var clean []string
		for _, x := range exs {
			x = strings.ToUpper(strings.TrimSpace(x))
			if !contains(s.Exchanges, x) {
				return st, nil, invalid("%s is offered on %s; not %s", s.Label, strings.Join(s.Exchanges, ", "), x)
			}
			if !contains(clean, x) {
				clean = append(clean, x)
			}
		}
		sort.Strings(clean)
		if strings.Join(clean, ",") != strings.Join(m.On[id], ",") {
			note("%s offered on: %s", s.Label, firstOf(strings.Join(clean, ", "), "none"))
		}
		if len(clean) == 0 {
			delete(m.On, id)
		} else {
			m.On[id] = clean
		}
	}
	if r.Constitution != nil {
		c := *r.Constitution
		if c != "" && constitutionName(c) == c {
			return st, nil, invalid("the constitution is a sole proprietor, partnership, LLP or company")
		}
		if c != m.Constitution {
			note("constitution: %s", firstOf(constitutionName(c), "not answered"))
		}
		m.Constitution = c
	}
	if r.QSB != nil {
		switch *r.QSB {
		case "", AnswerYes, AnswerNo, "ex":
		default:
			return st, nil, invalid("a qualified stock broker answer is yes, no or ex (was one)")
		}
		if *r.QSB != m.QSB {
			note("qualified stock broker: %s", firstOf(*r.QSB, "not answered"))
		}
		m.QSB = *r.QSB
	}
	for _, x := range []struct {
		n   string
		p   *string
		dst *string
	}{{"stopped being a QSB on", r.QSBEndedOn, &m.QSBEndedOn}, {"changed clearing member on", r.CMChangedOn, &m.CMChangedOn}} {
		if x.p == nil {
			continue
		}
		d := strings.TrimSpace(*x.p)
		if d != "" && (!validDate(d) || d > now.In(ist).Format("2006-01-02")) {
			return st, nil, invalid("%s: give a date that has passed (YYYY-MM-DD)", x.n)
		}
		if d != *x.dst {
			note("%s %s", x.n, firstOf(d, "(none)"))
		}
		*x.dst = d
	}
	if m.QSB == "ex" && m.QSBEndedOn == "" {
		return st, nil, invalid("give the date the firm stopped being a qualified stock broker: the obligations continue for three financial years")
	}
	for k, in := range r.Counts {
		if _, ok := countLabels[k]; !ok {
			return st, nil, invalid("%s is not a client number Ledgerline keeps", k)
		}
		old, had := m.Counts[k]
		switch {
		case in == nil || in.Value == nil:
			if had {
				note("%s: back to Ledgerline's count", countLabels[k])
				delete(m.Counts, k)
			}
		case *in.Value < 0:
			return st, nil, invalid("%s cannot be negative", countLabels[k])
		case !had || old.Value != *in.Value || old.Note != strings.TrimSpace(in.Note):
			note("%s: %s", countLabels[k], fmtCount(*in.Value))
			m.Counts[k] = CountOverride{Value: *in.Value, Note: clip(strings.TrimSpace(in.Note), 200), By: actor, At: ts}
		}
	}
	if r.CSCRF != nil {
		if *r.CSCRF != "" && cscrfName(*r.CSCRF) == "" {
			return st, nil, invalid("unknown CSCRF category %q", *r.CSCRF)
		}
		if *r.CSCRF != st.prof.CSCRF {
			note("CSCRF category: %s", firstOf(cscrfName(*r.CSCRF), "worked out from the numbers"))
		}
		st.prof.CSCRF = *r.CSCRF
	}
	st.mods = m
	return st, changes, nil
}

// ModulesDiff is what a change switches on or off.
type ModulesDiff struct {
	Changes    []string   `json:"changes"`
	Duties     []DiffItem `json:"duties"`
	Tests      []DiffItem `json:"tests"`
	Screens    []DiffItem `json:"screens"`
	Conditions []DiffItem `json:"conditions"`
}

// DiffItem is one thing that changes.
type DiffItem struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	From  string `json:"from"`
	To    string `json:"to"`
	Why   string `json:"why"`
}

// factsFor are the firm facts with a proposed state in place of the saved one.
func (o *Ops) factsFor(ctx context.Context, tenant string, st modulesState) firmFacts {
	f, _ := o.firmProfileFactsWith(ctx, tenant, false, &st)
	return f
}

func (o *Ops) modulesDiff(ctx context.Context, tenant string, before, after firmFacts) ModulesDiff {
	d := ModulesDiff{Changes: []string{}, Duties: []DiffItem{}, Tests: []DiffItem{}, Screens: []DiffItem{}, Conditions: []DiffItem{}}
	item := func(id, title string, needs []string) (DiffItem, bool) {
		a, b := dutyApplies(needs, before), dutyApplies(needs, after)
		return DiffItem{ID: id, Title: title, From: a.Verdict, To: b.Verdict, Why: b.Reason}, a.Verdict != b.Verdict
	}
	if cat, err := dutyCatalogue(); err == nil {
		for _, x := range cat.Duties {
			if len(x.Applies) == 0 {
				continue
			}
			if it, ch := item(x.ID, x.Title, x.Applies); ch {
				d.Duties = append(d.Duties, it)
			}
		}
	}
	// The firm's own obligations with conditions.
	obs, _ := docs.List[Duty](ctx, o.Docs, tenant, cObligation, "")
	seen := map[string]bool{}
	for _, x := range d.Duties {
		seen[x.ID] = true
	}
	for _, x := range obs {
		if len(x.Applies) == 0 || seen[x.ID] {
			continue
		}
		if it, ch := item(x.ID, x.Title, x.Applies); ch {
			d.Duties = append(d.Duties, it)
		}
	}
	for _, t := range siTests {
		if needs := siApplies[t.ID]; len(needs) > 0 {
			if it, ch := item(t.ID, t.Title, needs); ch {
				d.Tests = append(d.Tests, it)
			}
		}
	}
	for _, g := range ScreenGates {
		if it, ch := item(g.Key, g.What, strings.Split(g.Cond, ",")); ch {
			d.Screens = append(d.Screens, it)
		}
	}
	for _, c := range ModuleConditions() {
		if strings.Contains(c.Cond, "<") || strings.Contains(c.Cond, "|") {
			continue
		}
		if it, ch := item(c.Cond, c.Means, []string{c.Cond}); ch {
			d.Conditions = append(d.Conditions, it)
		}
	}
	return d
}

// PreviewFirmModules says what a change would switch on and off, without
// saving it.
func (o *Ops) PreviewFirmModules(ctx context.Context, tenant, actor string, r FirmModulesReq) (ModulesDiff, error) {
	if err := o.require(ctx, tenant, actor, "Changing the firm modules", RoleCompliance); err != nil {
		return ModulesDiff{}, err
	}
	cur, err := o.currentModules(ctx, tenant)
	if err != nil {
		return ModulesDiff{}, err
	}
	next, changes, err := o.applyModules(cur, actor, o.now(), r)
	if err != nil {
		return ModulesDiff{}, err
	}
	d := o.modulesDiff(ctx, tenant, o.factsFor(ctx, tenant, cur), o.factsFor(ctx, tenant, next))
	d.Changes = append(d.Changes, changes...)
	return d, nil
}

// SaveFirmModules saves a change (compliance officer or Admin). The
// segment roles and member codes go to Settings, the firm profile answers
// to the firm profile, MTF to the compliance profile, and the rest to Firm
// modules. The change and what it switched on and off go to the audit
// trail.
func (o *Ops) SaveFirmModules(ctx context.Context, tenant, actor string, r FirmModulesReq) (FirmModulesView, error) {
	if err := o.require(ctx, tenant, actor, "Changing the firm modules", RoleCompliance); err != nil {
		return FirmModulesView{}, err
	}
	cur, err := o.currentModules(ctx, tenant)
	if err != nil {
		return FirmModulesView{}, err
	}
	now := o.now()
	next, changes, err := o.applyModules(cur, actor, now, r)
	if err != nil {
		return FirmModulesView{}, err
	}
	if len(changes) == 0 {
		return o.FirmModulesFor(ctx, tenant, actor)
	}
	diff := o.modulesDiff(ctx, tenant, o.factsFor(ctx, tenant, cur), o.factsFor(ctx, tenant, next))
	ts := now.Format(time.RFC3339)
	if fmt.Sprint(cur.set.ClearingFor, cur.set.ClearingMemberNames, cur.set.MemberCodes) != fmt.Sprint(next.set.ClearingFor, next.set.ClearingMemberNames, next.set.MemberCodes) {
		set, err := o.Settings(ctx, tenant)
		if err != nil {
			return FirmModulesView{}, err
		}
		set.ClearingFor, set.ClearingMemberNames, set.MemberCodes = next.set.ClearingFor, next.set.ClearingMemberNames, next.set.MemberCodes
		if err := o.saveSettings(ctx, tenant, actor, set); err != nil {
			return FirmModulesView{}, err
		}
	}
	if fmt.Sprint(cur.prof) != fmt.Sprint(next.prof) {
		p := next.prof
		p.MTF, p.By, p.At = nil, actor, ts
		if err := docs.Upsert(ctx, o.Docs, tenant, cFirmProfile, "main", p, actor); err != nil {
			return FirmModulesView{}, err
		}
	}
	if cur.comp.MTF != next.comp.MTF {
		cp := o.CompProfile(ctx, tenant)
		cp.MTF, cp.By, cp.At = next.comp.MTF, actor, ts
		if err := docs.Upsert(ctx, o.Docs, tenant, cCompProfile, "main", cp, actor); err != nil {
			return FirmModulesView{}, err
		}
		o.log(ctx, tenant, actor, "compliance.profile", "main", map[string]any{"mtf": cp.MTF, "from": "firm modules"})
	}
	m := next.mods
	m.By, m.At = actor, ts
	if err := docs.Upsert(ctx, o.Docs, tenant, cFirmModules, fmMain, m, actor); err != nil {
		return FirmModulesView{}, err
	}
	names := func(xs []DiffItem) []string {
		out := []string{}
		for _, x := range xs {
			out = append(out, x.ID+": "+x.From+" → "+x.To)
		}
		return out
	}
	o.log(ctx, tenant, actor, "firm.modules", fmMain, map[string]any{"changed": changes, "duties": names(diff.Duties), "tests": names(diff.Tests), "screens": names(diff.Screens)})
	return o.FirmModulesFor(ctx, tenant, actor)
}

// RecountClients counts the firm's clients again now.
func (o *Ops) RecountClients(ctx context.Context, tenant, actor string) (FirmModulesView, error) {
	if err := o.require(ctx, tenant, actor, "Changing the firm modules", RoleCompliance); err != nil {
		return FirmModulesView{}, err
	}
	if _, err := o.recountClients(ctx, tenant); err != nil {
		return FirmModulesView{}, err
	}
	return o.FirmModulesFor(ctx, tenant, actor)
}

// noteCMChange records a change of clearing route that involves another
// clearing member (Settings → Cleared through or Firm modules), for the
// cmchange condition. Starting or stopping to trade a segment is not a
// change of clearing member.
func (o *Ops) noteCMChange(ctx context.Context, tenant, actor, seg, from, to string) {
	if from == to || from == ClearNone || to == ClearNone || (!strings.HasPrefix(from, "CM:") && !strings.HasPrefix(to, "CM:")) {
		return
	}
	ch, _, _ := docs.GetOr(ctx, o.Docs, tenant, cFirmModules, fmCMChanges, cmChangeLog{})
	ch.Changes = append(ch.Changes, cmChange{Date: o.now().In(ist).Format("2006-01-02"), Segment: seg, From: from, To: to, By: actor})
	if n := len(ch.Changes); n > 50 {
		ch.Changes = ch.Changes[n-50:]
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cFirmModules, fmCMChanges, ch, actor)
}
