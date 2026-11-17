package ops

import (
	"context"
	"fmt"
	"strings"
	"time"

	"ledgerline/internal/docs"
)

// ---- the firm profile: what the circulars' rules need to know ----
//
// Whether a circular applies depends on how the firm is set up. Most of it
// Ledgerline knows already:
//
//   - exchanges and segments: the member codes and "Cleared through" in
//     Settings;
//   - the clearing route of each segment: the firm's own clearing at a
//     clearing corporation, or another clearing member;
//   - a CDSL DP: the CDSL connection has the firm's DP ID;
//   - MTF: the compliance profile (Circulars & calendar → RAS dates &
//     profile);
//   - authorised persons, branches and NRI clients: Partners, branches and
//     the client list.
//
// The rest is answered by the compliance officer or an Admin in Settings →
// Firm profile: NSDL participant, algo trading, other SEBI registrations
// (portfolio manager, investment adviser …) and the firm's category under
// SEBI's cyber security and cyber resilience framework (CSCRF:
// SEBI/HO/ITD-1/ITD_CSC_EXT/P/CIR/2024/113 of 20 August 2024; the
// categories for stock brokers, by registered clients and clients' trading
// volume, in SEBI/HO/ITD-1/ITD_CSC_EXT/P/CIR/2025/60 of 30 April 2025). An
// answer given there takes the place of what Ledgerline found. Nothing in
// it is client data, and it is never sent to an AI model.

const cFirmProfile = "firmprofile"

// Answers to a yes-or-no question on the profile.
const (
	AnswerYes = "yes"
	AnswerNo  = "no"
)

// FirmProfile is what the compliance officer told Ledgerline. Each answer
// is "yes", "no" or "" (go by what Ledgerline knows).
type FirmProfile struct {
	CDSL     string `json:"cdsl"`     // a CDSL depository participant
	NSDL     string `json:"nsdl"`     // an NSDL depository participant
	Algo     string `json:"algo"`     // algorithmic trading: the firm's own, or clients' through its systems
	IBT      string `json:"ibt"`      // internet-based trading: clients trade through the firm's website or app
	NRI      string `json:"nri"`      // NRI clients
	AP       string `json:"ap"`       // authorised persons
	Branches string `json:"branches"` // branch offices
	// Registrations are the firm's other SEBI registrations (FirmRegistrations).
	Registrations []string `json:"registrations"`
	OtherReg      string   `json:"other_registration,omitempty"`
	CSCRF         string   `json:"cscrf"` // CSCRFCategories; "" not known
	// MTF is shown and saved with the profile but kept on the compliance
	// profile (ComplianceProfile.MTF); nil on save leaves it as it is.
	MTF *bool  `json:"mtf,omitempty"`
	By  string `json:"by,omitempty"`
	At  string `json:"at,omitempty"`
}

// FirmRegistrations are the other SEBI registrations a broker may hold.
var FirmRegistrations = []struct{ ID, Name string }{
	{"pms", "Portfolio manager"},
	{"ia", "Investment adviser"},
	{"ra", "Research analyst"},
	{"mb", "Merchant banker"},
	{"mfd", "Mutual fund distributor (AMFI)"},
}

// CSCRFCategories are SEBI's CSCRF categories of regulated entities other
// than MIIs, with the stock broker thresholds of SEBI/HO/ITD-1/ITD_CSC_EXT/
// P/CIR/2025/60 (30 April 2025): the higher category applies when either
// the number of registered clients or the clients' trading volume in the
// year puts the broker there.
var CSCRFCategories = []struct{ ID, Name, Note string }{
	{"qualified", "Qualified RE", "More than 10 lakh registered clients, or more than ₹10,00,000 crore of clients' trading in the year"},
	{"mid", "Mid-size RE", "More than 1 lakh and up to 10 lakh clients, or more than ₹1,00,000 crore and up to ₹10,00,000 crore"},
	{"small", "Small-size RE", "More than 10,000 and up to 1 lakh clients, or more than ₹10,000 crore and up to ₹1,00,000 crore"},
	{"self", "Self-certification RE", "More than 1,000 and up to 10,000 clients, or more than ₹1,000 crore and up to ₹10,000 crore"},
	{"exempt", "Below the CSCRF thresholds", "Fewer than 1,000 clients and less than ₹1,000 crore of clients' trading in the year"},
}

func cscrfName(id string) string {
	for _, c := range CSCRFCategories {
		if c.ID == id {
			return c.Name
		}
	}
	return ""
}

// tri is a fact that is true, false or not known.
type tri int

const (
	triUnknown tri = iota
	triYes
	triNo
)

func triOf(b bool) tri {
	if b {
		return triYes
	}
	return triNo
}

// firmFacts are the profile as the applicability rules read it.
type firmFacts struct {
	Member  map[string]bool   // exchange → the firm is a member
	Seg     map[string]bool   // CM, FO, CD, COM → the firm trades that kind of segment somewhere
	SelfAt  map[string]bool   // clearing corporation → the firm clears its own trades there
	ViaCM   map[string]string // clearing corporation → the clearing member that clears the firm's trades there
	CDSL    tri
	NSDL    tri
	MTF     tri
	Algo    tri
	IBT     tri
	NRI     tri
	AP      tri
	Branch  tri
	Regs    map[string]bool
	CSCRF   string
	Assumed bool // no member codes in Settings: NSE, BSE and MCX assumed
}

func (f firmFacts) dp() tri {
	switch {
	case f.CDSL == triYes || f.NSDL == triYes:
		return triYes
	case f.CDSL == triNo && f.NSDL == triNo:
		return triNo
	}
	return triUnknown
}

// FirmFact is one line of the profile as the screen shows it.
type FirmFact struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Value  string `json:"value"`            // in words
	Known  bool   `json:"known"`            // the rules can rely on it
	From   string `json:"from"`             // where it comes from
	Answer string `json:"answer,omitempty"` // the profile's own answer, for the editable ones
	Edit   bool   `json:"editable,omitempty"`
}

// FirmProfileView is the profile with what Ledgerline found.
type FirmProfileView struct {
	Profile       FirmProfile `json:"profile"`
	Facts         []FirmFact  `json:"facts"`
	Questions     []string    `json:"questions"` // what to answer for the rules to be sure
	MTF           bool        `json:"mtf"`
	Registrations any         `json:"registrations"`
	Categories    any         `json:"cscrf_categories"`
	CanEdit       bool        `json:"can_edit"`
}

func (o *Ops) firmProfile(ctx context.Context, tenant string) FirmProfile {
	p, _, _ := docs.GetOr(ctx, o.Docs, tenant, cFirmProfile, "main", FirmProfile{})
	if p.Registrations == nil {
		p.Registrations = []string{}
	}
	p.MTF = nil
	return p
}

// firmFacts reads the profile for the rules.
func (o *Ops) firmFacts(ctx context.Context, tenant string) firmFacts {
	f, _ := o.firmProfileFacts(ctx, tenant)
	return f
}

// firmProfileFacts reads the profile and says, fact by fact, where each
// came from.
func (o *Ops) firmProfileFacts(ctx context.Context, tenant string) (firmFacts, []FirmFact) {
	return o.firmProfileFactsOpt(ctx, tenant, true)
}

// firmProfileFactsOpt reads the profile; without clients it does not
// count NRI clients (the compliance calendar reads it often and needs that
// only for a duty about NRI clients).
func (o *Ops) firmProfileFactsOpt(ctx context.Context, tenant string, clients bool) (firmFacts, []FirmFact) {
	set, _ := o.Settings(ctx, tenant)
	p := o.firmProfile(ctx, tenant)
	cp := o.CompProfile(ctx, tenant)
	f := firmFacts{Member: map[string]bool{}, Seg: map[string]bool{}, SelfAt: map[string]bool{}, ViaCM: map[string]string{}, Regs: map[string]bool{}, CSCRF: p.CSCRF}
	var facts []FirmFact

	// Exchanges, segments and clearing, from Settings.
	f.Assumed = len(memberExchanges(set)) == 0
	var segs, self, via []string
	for _, s := range set.activeSegments() {
		f.Member[s.Exchange], f.Seg[s.Segment] = true, true
		name := s.Exchange + " " + s.Segment
		segs = append(segs, name)
		switch ct := set.ClearedThrough(s.Key); {
		case strings.HasPrefix(ct, "CM:"):
			cm := firstOf(strings.TrimSpace(set.ClearingMemberNames[s.Key]), "clearing member "+ct[3:])
			f.ViaCM[s.Default] = cm
			via = append(via, name+" through "+cm)
		default:
			f.SelfAt[ct] = true
			self = append(self, name+" at "+firstOf(ClearingCorpNames[ct], ct))
		}
	}
	from := "Settings → member codes and Cleared through"
	if f.Assumed {
		from = "Assumed: no member codes in Settings yet"
	}
	facts = append(facts, FirmFact{Key: "exchanges", Label: "Exchanges and segments", Value: firstOf(strings.Join(segs, ", "), "None"), Known: !f.Assumed, From: from})
	route := []string{}
	if len(self) > 0 {
		route = append(route, "Clears its own trades: "+strings.Join(self, "; "))
	}
	if len(via) > 0 {
		route = append(route, "Through a clearing member: "+strings.Join(via, "; "))
	}
	facts = append(facts, FirmFact{Key: "clearing", Label: "Clearing route", Value: firstOf(strings.Join(route, ". "), "None"), Known: !f.Assumed, From: "Settings → Cleared through"})

	// A yes-or-no fact: the profile's answer, else what Ledgerline found.
	yn := func(key, label string, answer string, found tri, foundFrom string) tri {
		x := FirmFact{Key: key, Label: label, Answer: answer, Edit: true}
		v := found
		switch answer {
		case AnswerYes:
			v, x.From = triYes, "Firm profile (your answer)"
		case AnswerNo:
			v, x.From = triNo, "Firm profile (your answer)"
		default:
			x.From = foundFrom
		}
		x.Known = v != triUnknown
		x.Value = map[tri]string{triYes: "Yes", triNo: "No", triUnknown: "Not known"}[v]
		facts = append(facts, x)
		return v
	}
	cdslFound, cdslFrom := triUnknown, "no CDSL connection with a DP ID in Connections"
	if _, ok := o.cdslConnFor(ctx, tenant); ok {
		cdslFound, cdslFrom = triYes, "Connections → CDSL (the firm's DP ID)"
	}
	f.CDSL = yn("cdsl", "CDSL depository participant", p.CDSL, cdslFound, cdslFrom)
	f.NSDL = yn("nsdl", "NSDL depository participant", p.NSDL, triUnknown, "nothing in Ledgerline says")
	f.MTF = triOf(cp.MTF)
	facts = append(facts, FirmFact{Key: "mtf", Label: "Margin trading facility (MTF)", Value: map[bool]string{true: "Yes", false: "No"}[cp.MTF], Known: true, From: "Compliance profile (also on Circulars & calendar → RAS dates & profile)", Edit: true})
	f.Algo = yn("algo", "Algorithmic trading (the firm's own, or clients' through its systems)", p.Algo, triUnknown, "nothing in Ledgerline says")
	f.IBT = yn("ibt", "Internet-based trading (clients trade through the firm's website or app)", p.IBT, triUnknown, "nothing in Ledgerline says")

	var cl map[string]Client
	if clients {
		cl, _ = o.ClientMap(ctx, tenant)
	}
	nri := 0
	for _, c := range cl {
		if c.Status != "Closed" && (c.Type == "NRI" || contains(c.Tags, "NRI")) {
			nri++
		}
	}
	nriFound, nriFrom := triUnknown, "no clients yet"
	if !clients {
		nriFrom = "not counted"
	}
	if len(cl) > 0 {
		nriFound, nriFrom = triOf(nri > 0), fmt.Sprintf("Your clients: %d NRI", nri)
	}
	f.NRI = yn("nri", "NRI clients", p.NRI, nriFound, nriFrom)

	ps, _ := docs.List[Partner](ctx, o.Docs, tenant, cPartner, "")
	aps := 0
	for _, x := range ps {
		if x.Active && (x.Kind == "" || x.Kind == RoleAP || strings.EqualFold(x.Kind, "Authorised person")) {
			aps++
		}
	}
	f.AP = yn("ap", "Authorised persons", p.AP, triOf(aps > 0), fmt.Sprintf("Partners: %d active authorised person(s)", aps))
	bs, _ := o.Branches(ctx, tenant)
	br := 0
	for _, b := range bs {
		if b.Active {
			br++
		}
	}
	f.Branch = yn("branches", "Branch offices", p.Branches, triOf(br > 0), fmt.Sprintf("Branches: %d active", br))

	var regs []string
	for _, r := range FirmRegistrations {
		if contains(p.Registrations, r.ID) {
			f.Regs[r.ID] = true
			regs = append(regs, r.Name)
		}
	}
	if p.OtherReg != "" {
		regs = append(regs, p.OtherReg)
	}
	facts = append(facts, FirmFact{Key: "registrations", Label: "Other SEBI registrations", Value: firstOf(strings.Join(regs, ", "), "None given"), Known: true, From: "Firm profile", Edit: true})
	cat := FirmFact{Key: "cscrf", Label: "CSCRF category", Answer: p.CSCRF, Edit: true, From: "Firm profile"}
	if n := cscrfName(p.CSCRF); n != "" {
		cat.Value, cat.Known = n, true
	} else {
		cat.Value = "Not known"
	}
	facts = append(facts, cat)
	return f, facts
}

// FirmProfileFor is the profile for Settings (compliance officer and Admin).
func (o *Ops) FirmProfileFor(ctx context.Context, tenant, actor string) (FirmProfileView, error) {
	if err := o.require(ctx, tenant, actor, "Viewing the firm profile", RoleCompliance); err != nil {
		return FirmProfileView{}, err
	}
	_, facts := o.firmProfileFacts(ctx, tenant)
	v := FirmProfileView{Profile: o.firmProfile(ctx, tenant), Facts: facts, Questions: []string{}, MTF: o.CompProfile(ctx, tenant).MTF, Registrations: FirmRegistrations, Categories: CSCRFCategories, CanEdit: true}
	for _, x := range facts {
		if x.Known {
			continue
		}
		switch x.Key {
		case "cdsl", "nsdl":
			v.Questions = append(v.Questions, map[string]string{"cdsl": "Is the firm a CDSL depository participant?", "nsdl": "Is the firm an NSDL depository participant?"}[x.Key])
		case "algo":
			v.Questions = append(v.Questions, "Does the firm run or offer algorithmic trading?")
		case "ibt":
			v.Questions = append(v.Questions, "Do clients trade through the firm's website or app (internet-based trading)?")
		case "cscrf":
			v.Questions = append(v.Questions, "What is the firm's category under SEBI's cyber security and cyber resilience framework (CSCRF)?")
		case "nri":
			v.Questions = append(v.Questions, "Does the firm have NRI clients?")
		case "exchanges", "clearing":
			if !contains(v.Questions, "Which exchanges is the firm a member of? (Settings → member codes)") {
				v.Questions = append(v.Questions, "Which exchanges is the firm a member of? (Settings → member codes)")
			}
		}
	}
	return v, nil
}

// SaveFirmProfile saves the compliance officer's answers.
func (o *Ops) SaveFirmProfile(ctx context.Context, tenant, actor string, p FirmProfile) (FirmProfileView, error) {
	if err := o.require(ctx, tenant, actor, "Changing the firm profile", RoleCompliance); err != nil {
		return FirmProfileView{}, err
	}
	for _, x := range []struct{ name, v string }{{"CDSL participant", p.CDSL}, {"NSDL participant", p.NSDL}, {"algo trading", p.Algo}, {"internet-based trading", p.IBT}, {"NRI clients", p.NRI}, {"authorised persons", p.AP}, {"branch offices", p.Branches}} {
		if x.v != "" && x.v != AnswerYes && x.v != AnswerNo {
			return FirmProfileView{}, invalid("%s is yes, no, or left to what Ledgerline knows", x.name)
		}
	}
	if p.CSCRF != "" && cscrfName(p.CSCRF) == "" {
		return FirmProfileView{}, invalid("unknown CSCRF category %q", p.CSCRF)
	}
	regs := []string{}
	for _, r := range p.Registrations {
		ok := false
		for _, x := range FirmRegistrations {
			ok = ok || x.ID == r
		}
		if !ok {
			return FirmProfileView{}, invalid("unknown registration %q", r)
		}
		if !contains(regs, r) {
			regs = append(regs, r)
		}
	}
	p.Registrations, p.OtherReg = regs, clip(strings.TrimSpace(p.OtherReg), 100)
	mtf := p.MTF
	p.MTF, p.By, p.At = nil, actor, o.now().Format(time.RFC3339)
	if err := docs.Upsert(ctx, o.Docs, tenant, cFirmProfile, "main", p, actor); err != nil {
		return FirmProfileView{}, err
	}
	o.log(ctx, tenant, actor, "firm.profile", "main", map[string]any{"cdsl": p.CDSL, "nsdl": p.NSDL, "algo": p.Algo, "ibt": p.IBT, "nri": p.NRI, "ap": p.AP, "branches": p.Branches, "registrations": p.Registrations, "other": p.OtherReg, "cscrf": p.CSCRF})
	if cp := o.CompProfile(ctx, tenant); mtf != nil && *mtf != cp.MTF {
		cp.MTF, cp.By, cp.At = *mtf, actor, p.At
		if err := docs.Upsert(ctx, o.Docs, tenant, cCompProfile, "main", cp, actor); err != nil {
			return FirmProfileView{}, err
		}
		o.log(ctx, tenant, actor, "compliance.profile", "main", map[string]any{"mtf": cp.MTF, "from": "firm profile"})
	}
	return o.FirmProfileFor(ctx, tenant, actor)
}
