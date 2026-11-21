package ops

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"ledgerline/internal/control"
	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// ---- Settings → Firm practices ----
//
// How a firm operates is not built into Ledgerline: each operating
// question a broker answers when it starts (how limits are shared between
// two front ends, who creates front-end logins, how bank changes are
// authorised…) is a question here, with its options and "Other". Ticking
// an option switches that behaviour on.
//
// One source of truth: a question whose behaviour is built reads its
// current option from the real setting (Settings, a connection's switch,
// the brokerage setup, the DP tariff) every time, and ticking an option
// writes that setting. Nothing is copied here. A question that is not
// built yet, or an option that is not, shows "Not available yet" and
// cannot be chosen. A recorded-only question (no behaviour yet) keeps its
// answer here.
//
// "Other" records the firm's own practice in its words, keeps the
// behaviour as it is, and gives the Admin a work item (and a line on the
// go-live checklist Ledgerline Control sees) saying the practice needs
// configuring.
//
// Who: an Admin answers. A change that affects money or client limits
// (Money) waits for a second person, the compliance officer or another
// Admin, never the one who proposed it; the others apply at once.
// Confirming the option already in force needs no approval: nothing
// changes. Every answer, approval, rejection and withdrawal is audited and
// kept in the question's history with who and when.

const (
	cFirmPractice = "firmpractice" // question id -> FirmPractice
	wFirmPractice = "practice"
	PracticeOther = "other"

	PracticeApplied   = "Applied"
	PracticeConfirmed = "Confirmed (no change)"
	PracticeWaiting   = "Waiting for approval"
	PracticeApproved  = "Approved and applied"
	PracticeRejected  = "Rejected"
	PracticeWithdrawn = "Withdrawn"
	PracticeRecorded  = "Other: recorded, needs configuring"

	practiceNotYet  = "Not available yet"
	practiceHistMax = 200
)

// PracticeOption is one answer to a question.
type PracticeOption struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Available bool   `json:"available"`
	// Note says what the option does, or why it cannot be chosen now.
	Note string `json:"note,omitempty"`
}

// PracticeField is a value a question asks for beside the option.
type PracticeField struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Hint  string `json:"hint,omitempty"`
}

// practiceState is a question's behaviour as it stands.
type practiceState struct {
	Option string
	Values map[string]string
	Note   string
}

type practiceDef struct {
	ID, Short, Question, Why string
	// Note is said under the question (what is and is not built).
	Note string
	// Setting names where the behaviour lives, for the screen.
	Setting string
	// Money: a change affects money or client limits and needs a second
	// approver.
	Money   bool
	Options []PracticeOption
	Fields  []PracticeField
	// read is the behaviour now, from the real setting; nil for a
	// recorded-only question (its answer is kept here).
	read func(o *Ops, ctx context.Context, tenant string) practiceState
	// write switches the behaviour to an option; nil: recorded only.
	write func(o *Ops, ctx context.Context, tenant, actor, opt string, vals map[string]string) error
	// avail is why an option cannot be chosen now ("" = it can).
	avail func(o *Ops, ctx context.Context, tenant, opt string) string
}

func (d practiceDef) option(id string) (PracticeOption, bool) {
	for _, x := range d.Options {
		if x.ID == id {
			return x, true
		}
	}
	return PracticeOption{}, false
}

func (d practiceDef) label(id string) string {
	if id == PracticeOther {
		return "Other"
	}
	if x, ok := d.option(id); ok {
		return x.Label
	}
	return firstOf(id, "none of the options")
}

// FirmPractice is a firm's answer to one question and its history.
type FirmPractice struct {
	ID string `json:"id"`
	// Answer is the firm's last answer: an option or other.
	Answer string            `json:"answer,omitempty"`
	Other  string            `json:"other,omitempty"`
	Values map[string]string `json:"values,omitempty"`
	// Applied is the last option applied; a recorded-only question reads
	// its behaviour from it.
	Applied string `json:"applied,omitempty"`
	By      string `json:"by,omitempty"`
	At      string `json:"at,omitempty"`
	// WorkKey is the open "needs configuring" work item for Other.
	WorkKey string           `json:"work_key,omitempty"`
	Pending *PracticeChange  `json:"pending,omitempty"`
	History []PracticeChange `json:"history"`
}

// PracticeChange is one answer given, with what became of it.
type PracticeChange struct {
	N      int               `json:"n"`
	Option string            `json:"option"`
	Label  string            `json:"label"`
	Other  string            `json:"other,omitempty"`
	Values map[string]string `json:"values,omitempty"`
	Note   string            `json:"note,omitempty"`
	// Was is the behaviour before (an option id).
	Was       string `json:"was,omitempty"`
	By        string `json:"by"`
	At        string `json:"at"`
	Status    string `json:"status"`
	DecidedBy string `json:"decided_by,omitempty"`
	DecidedAt string `json:"decided_at,omitempty"`
	Decision  string `json:"decision,omitempty"`
}

// PracticeAnswer is an Admin's answer to a question.
type PracticeAnswer struct {
	Option string            `json:"option"`
	Other  string            `json:"other,omitempty"`
	Values map[string]string `json:"values,omitempty"`
	Note   string            `json:"note,omitempty"`
}

// PracticeView is one question for the screen.
type PracticeView struct {
	ID       string           `json:"id"`
	N        int              `json:"n"`
	Short    string           `json:"short"`
	Question string           `json:"question"`
	Why      string           `json:"why"`
	Note     string           `json:"note,omitempty"`
	Setting  string           `json:"setting,omitempty"`
	Money    bool             `json:"money"`
	Options  []PracticeOption `json:"options"`
	Fields   []PracticeField  `json:"fields,omitempty"`
	// Current is the behaviour now ("" when no option matches it).
	Current     string            `json:"current"`
	CurrentNote string            `json:"current_note,omitempty"`
	Values      map[string]string `json:"values,omitempty"`
	Answered    bool              `json:"answered"`
	Answer      string            `json:"answer,omitempty"`
	Other       string            `json:"other,omitempty"`
	By          string            `json:"by,omitempty"`
	At          string            `json:"at,omitempty"`
	Pending     *PracticeChange   `json:"pending,omitempty"`
	History     []PracticeChange  `json:"history"` // newest first
}

// FirmPracticesView is the questionnaire.
type FirmPracticesView struct {
	Questions  []PracticeView      `json:"questions"`
	Answered   int                 `json:"answered"`
	Total      int                 `json:"total"`
	CanAnswer  bool                `json:"can_answer"`
	CanApprove bool                `json:"can_approve"`
	Checklist  []control.SetupItem `json:"checklist"`
}

// ---- the questions ----

func onOff(on, off string) []PracticeOption {
	return []PracticeOption{{ID: "on", Label: on, Available: true}, {ID: "off", Label: off, Available: true}}
}

func onOffState(on bool, note string) practiceState {
	return practiceState{Option: map[bool]string{true: "on", false: "off"}[on], Note: note}
}

// practiceSettings changes the settings and saves them (the actor was
// checked by the caller).
func (o *Ops) practiceSettings(ctx context.Context, tenant, actor string, fn func(*Settings)) error {
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return err
	}
	fn(&set)
	return o.saveSettings(ctx, tenant, actor, set)
}

// connOn says whether a connection is set up, and the value of one field.
func (o *Ops) connValue(ctx context.Context, tenant, id, key string) (bool, string) {
	c, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, id)
	if err != nil || c.Status == "" || c.Status == "Not set" {
		return false, ""
	}
	return true, c.Values[key]
}

// setConnValue changes one option field of a saved connection, keeping
// its other values, secrets and status (a firm practice's switch).
func (o *Ops) setConnValue(ctx context.Context, tenant, actor, id, key, val string) error {
	p, ok := connProvider(id)
	if !ok {
		return ErrNotFound
	}
	var field *ConnField
	for i := range p.Fields {
		if p.Fields[i].Key == key {
			field = &p.Fields[i]
		}
	}
	if field == nil || len(field.Options) == 0 || !contains(field.Options, val) {
		return invalid("%s has no option %q for %s", p.Name, val, key)
	}
	was := ""
	_, err := docs.Update(ctx, o.Docs, tenant, cConn, id, actor, func(c *Connection) error {
		if c.Status == "" || c.Status == "Not set" {
			return fmt.Errorf("%w: set up %s in Settings → Connections first", ErrState, p.Name)
		}
		if c.Values == nil {
			c.Values = map[string]string{}
		}
		was, c.Values[key] = c.Values[key], val
		if id == connFinspotAPI {
			if err := checkFinspotAPIConn(*c); err != nil {
				return err
			}
		}
		c.UpdatedAt, c.UpdatedBy = o.now().Format(time.RFC3339), actor
		return nil
	})
	if errors.Is(err, docs.ErrNotFound) {
		return fmt.Errorf("%w: set up %s in Settings → Connections first", ErrState, p.Name)
	}
	if err == nil && was != val {
		o.log(ctx, tenant, actor, "connection.value_changed", id, map[string]any{"field": key, "from": was, "to": val})
	}
	return err
}

func needConn(id string) func(o *Ops, ctx context.Context, tenant, opt string) string {
	return func(o *Ops, ctx context.Context, tenant, opt string) string {
		if opt != "on" {
			return ""
		}
		if ok, _ := o.connValue(ctx, tenant, id, ""); !ok {
			p, _ := connProvider(id)
			return "Set up " + p.Name + " in Settings → Connections first"
		}
		return ""
	}
}

// The DP tariff's pledge events: when a pledge is created, and after.
var (
	pledgeMadeCodes  = []string{PledgeCreated, MarginPledgeCreated}
	pledgeAfterCodes = []string{PledgeClosed, MarginPledgeClosed, PledgeInvoked, MarginPledgeInvoked}
)

func pledgeCharged(l DPChargeLine) bool { return !l.Off && !l.DPRate.zero() }

func readDPPledge(o *Ops, ctx context.Context, tenant string) practiceState {
	t, _ := o.DPTariffNow(ctx, tenant)
	by := map[string]DPChargeLine{}
	for _, l := range t.PledgeCharges {
		by[l.Code] = l
	}
	made, after := false, false
	for _, c := range pledgeMadeCodes {
		made = made || pledgeCharged(by[c])
	}
	for _, c := range pledgeAfterCodes {
		after = after || pledgeCharged(by[c])
	}
	st := practiceState{Values: map[string]string{}}
	if l := by[PledgeCreated]; pledgeCharged(l) && l.Flat > 0 {
		st.Values["charge"] = l.Flat.String()
	}
	switch {
	case after:
		st.Option = "all"
	case made:
		st.Option = "created"
	default:
		st.Note = "The DP tariff charges nothing for pledges."
	}
	return st
}

func writeDPPledge(o *Ops, ctx context.Context, tenant, actor, opt string, vals map[string]string) error {
	t, err := o.DPTariffNow(ctx, tenant)
	if err != nil {
		return err
	}
	var amt money.Amount
	if s := strings.TrimSpace(vals["charge"]); s != "" {
		if amt, err = money.ParseAmount(s); err != nil || amt <= 0 {
			return invalid("the charge per pledge is an amount in rupees, more than 0")
		}
	}
	by := map[string]DPChargeLine{}
	for _, l := range t.PledgeCharges {
		by[l.Code] = l
	}
	// The rate a missing line takes: the amount given, else the firm's
	// own rate for a pledge created (or, failing that, closed).
	var base *DPRate
	if amt > 0 {
		base = &DPRate{Flat: amt}
	} else {
		for _, c := range append(append([]string{}, pledgeMadeCodes...), pledgeAfterCodes...) {
			if l, ok := by[c]; ok && !l.DPRate.zero() {
				r := l.DPRate
				base = &r
				break
			}
		}
	}
	if base == nil {
		return invalid("give the charge per pledge: the DP tariff has no pledge rate yet")
	}
	set := func(c string, on bool) {
		l, ok := by[c]
		if !ok {
			if !on {
				return
			}
			l = DPChargeLine{Code: c, Name: dpChargeCodes[c]}
		}
		if on && (l.DPRate.zero() || amt > 0) {
			l.DPRate = *base
		}
		l.Off = !on
		by[c] = l
	}
	for _, c := range pledgeMadeCodes {
		set(c, true)
	}
	for _, c := range pledgeAfterCodes {
		set(c, opt == "all")
	}
	var lines []DPChargeLine
	for _, c := range PledgeEvents {
		if l, ok := by[c]; ok {
			lines = append(lines, l)
		}
	}
	t.PledgeCharges = lines
	_, err = o.saveDPTariff(ctx, tenant, actor, t)
	return err
}

var practiceDefs = []practiceDef{
	{ID: "limit-sharing", Short: "Equity limit on two front ends", Money: true,
		Question: "When a client can trade on two front ends (for example BOW and Finspot), how is the client's one equity limit shared between them?",
		Why:      "If both front ends show the full limit, the client can use the same money twice between two reads.",
		Setting:  "How Ledgerline works out each front end's limit (no setting yet)",
		Note:     "Today Ledgerline gives each front end the client's whole money. The primary front end and the one-click move are not built yet.",
		Options: []PracticeOption{
			{ID: "primary", Label: "Primary front end only: the client trades on one front end with the full limit; the other gets none", Note: practiceNotYet},
			{ID: "move", Label: "Primary front end, and a one-click move of the unused part to the other (a dealer button; the total is never changed)", Note: practiceNotYet},
			{ID: "same", Label: "The same limit is shown on both front ends", Available: true, Note: "What Ledgerline does today."},
		},
		read: func(o *Ops, ctx context.Context, tenant string) practiceState {
			return practiceState{Option: "same"}
		},
		write: func(o *Ops, ctx context.Context, tenant, actor, opt string, vals map[string]string) error { return nil },
	},
	{ID: "kyc-blocks", Short: "Blocks for KYC-pending and inactive clients",
		Question: "Are clients whose KYC is pending, and inactive clients, blocked on the front ends automatically?",
		Why:      "A client who may not trade should not depend on someone remembering to block them by hand.",
		Setting:  "Settings: KYC-pending clients blocked on the front ends",
		Note:     "Ledgerline does not fetch KYC statuses from the KRA every day yet. With this on, the block uses the KYC status already loaded in Ledgerline (KRA status uploads on KYC & PMLA): on hold, rejected, pending or incomplete. Accounts flagged Inactive are blocked either way.",
		Options: onOff("Yes: Ledgerline blocks them in the front ends' block file and limits, and lifts the block when it is cleared",
			"No: only accounts flagged Inactive are blocked; KYC-pending clients are blocked by hand"),
		read: func(o *Ops, ctx context.Context, tenant string) practiceState {
			set, _ := o.Settings(ctx, tenant)
			return onOffState(set.KYCPendingBlock, "")
		},
		write: func(o *Ops, ctx context.Context, tenant, actor, opt string, vals map[string]string) error {
			return o.practiceSettings(ctx, tenant, actor, func(s *Settings) { s.KYCPendingBlock = opt == "on" })
		},
	},
	{ID: "finspot-logins", Short: "Finspot client logins",
		Question: "Who creates a new client's user ID (login) on Finspot?",
		Why:      "A client needs a Finspot login to trade there, and must never get two.",
		Setting:  "Settings: Finspot client logins, and Connections → Finspot by API: client logins by API",
		Note:     "Ledgerline recognises logins made in Finspot's admin console (Finspot answers \"Client exists\", a Check Finspot login finds the account, or staff mark it), never creates a second one, and shows the client as having a Finspot login.",
		Options: []PracticeOption{
			{ID: FinspotLoginsConsole, Label: "In Finspot's admin console only", Available: true, Note: "Ledgerline never creates a login."},
			{ID: FinspotLoginsLedgerline, Label: "From Ledgerline: by API, with the upload file as the fallback", Available: true, Note: "Each new active client's login is created by API (account, segments and products, never a password); without the API, it goes in Ledgerline's Investor Client upload file."},
			{ID: FinspotLoginsBoth, Label: "Both: in the console, or from Ledgerline when someone asks", Available: true, Note: "Ledgerline creates a client's login only when someone clicks Create Finspot login (needs Finspot by API)."},
		},
		read: func(o *Ops, ctx context.Context, tenant string) practiceState {
			return practiceState{Option: o.finspotLoginMode(ctx, tenant)}
		},
		write: func(o *Ops, ctx context.Context, tenant, actor, opt string, vals map[string]string) error {
			if err := o.practiceSettings(ctx, tenant, actor, func(s *Settings) { s.FinspotLogins = opt }); err != nil {
				return err
			}
			if ok, _ := o.connValue(ctx, tenant, connFinspotAPI, "clients"); ok {
				return o.setConnValue(ctx, tenant, actor, connFinspotAPI, "clients", map[bool]string{true: "Off", false: "On"}[opt == FinspotLoginsConsole])
			}
			return nil
		},
	},
	{ID: "finspot-baskets", Short: "Margin baskets by API",
		Question: "Are the front end's margin baskets (NSE and BSE intraday margin %) set from Ledgerline by API every morning?",
		Why:      "Ledgerline works out each scrip's margin from the exchange files; sending it by API saves the RMS team setting it by hand.",
		Setting:  "Connections → Finspot by API: margin baskets by API",
		Note:     "The margin basket files stay on Live RMS → Front-office files as the fallback.",
		Options:  onOff("Yes: by API before the market opens", "No: RMS sets them on the front end, or uploads Ledgerline's margin basket files"),
		read: func(o *Ops, ctx context.Context, tenant string) practiceState {
			ok, v := o.connValue(ctx, tenant, connFinspotAPI, "baskets")
			return onOffState(ok && v == "On", map[bool]string{true: "", false: "Finspot by API is not set up."}[ok])
		},
		write: func(o *Ops, ctx context.Context, tenant, actor, opt string, vals map[string]string) error {
			if ok, _ := o.connValue(ctx, tenant, connFinspotAPI, ""); !ok && opt == "off" {
				return nil
			}
			return o.setConnValue(ctx, tenant, actor, connFinspotAPI, "baskets", map[bool]string{true: "On", false: "Off"}[opt == "on"])
		},
		avail: needConn(connFinspotAPI),
	},
	{ID: "cuspa-api", Short: "CUSPA and pledge instructions by API",
		Question: "Are CUSPA releases and invocations (and other pledge releases and invocations) sent to the depository by its API?",
		Why:      "Sending them from Ledgerline as soon as they are approved removes a second entry on the depository's system.",
		Setting:  "Connections → CDSL APIs (DP): normal pledge, unpledge and confiscate",
		Note:     "Built for CDSL. Lines Ledgerline cannot send (no pledge number yet, or the API off) stay on the day's depository list, to be given on the depository's system or by file.",
		Options:  onOff("Yes: by the depository's API, the list as the fallback", "No: given on the depository's system or by file from the day's list"),
		read: func(o *Ops, ctx context.Context, tenant string) practiceState {
			ok, _ := o.connValue(ctx, tenant, connCDSL, "")
			return onOffState(o.cdslFamilyOn(ctx, tenant, cdslFamPledge), map[bool]string{true: "", false: "CDSL APIs (DP) is not set up."}[ok])
		},
		write: func(o *Ops, ctx context.Context, tenant, actor, opt string, vals map[string]string) error {
			if ok, _ := o.connValue(ctx, tenant, connCDSL, ""); !ok && opt == "off" {
				return nil
			}
			return o.setConnValue(ctx, tenant, actor, connCDSL, cdslFamPledge, map[bool]string{true: "On", false: "Off"}[opt == "on"])
		},
		avail: needConn(connCDSL),
	},
	{ID: "mcx-apart", Short: "MCX money as its own balance", Money: true,
		Question: "With one bank account for all exchanges, is each client's MCX (commodity) money kept as a balance of its own?",
		Why:      "MCX money must not count in the equity limit, and equity money must not pay MCX losses unless RMS moves it.",
		Setting:  "Settings → Equity and commodity ledgers: keep each client's MCX money as a separate balance",
		Note:     "Money in is equity money; RMS moves it to MCX money with a maker and a checker; MCX bills add to or take from it; pledged shares cover equity margin only.",
		Options:  onOff("Yes: a separate MCX balance inside the one ledger", "No: one balance for all exchanges"),
		read: func(o *Ops, ctx context.Context, tenant string) practiceState {
			set, _ := o.Settings(ctx, tenant)
			n := ""
			if set.SeparateCommodityLedger {
				n = "The firm keeps a separate commodity ledger: MCX money is apart there."
			}
			return onOffState(set.MCXMoneyApart, n)
		},
		write: func(o *Ops, ctx context.Context, tenant, actor, opt string, vals map[string]string) error {
			return o.practiceSettings(ctx, tenant, actor, func(s *Settings) { s.MCXMoneyApart = opt == "on" })
		},
	},
	{ID: "bank-auth", Short: "Client authorisation of bank changes", Money: true,
		Question: "When staff raise a change to a client's bank accounts, how does the client authorise it?",
		Why:      "Payouts go to the client's registered bank account, so a change needs the client's own authority on record.",
		Setting:  "Settings → Receipts and payouts: client authorisation of bank changes raised by staff",
		Note:     "Ask the compliance officer to confirm in writing that the client's confirmation by code or link is acceptable before using it.",
		Options: []PracticeOption{
			{ID: BankAuthBoth, Label: "The client's one-time code and link on the registered mobile and e-mail, with a signed letter or e-sign as the fallback", Available: true},
			{ID: BankAuthConfirm, Label: "The client's one-time code and link only", Available: true},
			{ID: BankAuthLetter, Label: "A signed letter or e-signed request only", Available: true},
		},
		read: func(o *Ops, ctx context.Context, tenant string) practiceState {
			set, _ := o.Settings(ctx, tenant)
			return practiceState{Option: firstOf(set.BankAuthForms, BankAuthBoth)}
		},
		write: func(o *Ops, ctx context.Context, tenant, actor, opt string, vals map[string]string) error {
			return o.practiceSettings(ctx, tenant, actor, func(s *Settings) { s.BankAuthForms = opt })
		},
	},
	{ID: "auction-rate", Short: "Brokerage on auction trades", Money: true,
		Question: "Are auction trades always billed at the auction rate, even when the client has a special rate for that scrip?",
		Why:      "The two rules give different brokerage on the same trade.",
		Setting:  "Brokerage plans → Channels: auction trades",
		Options: []PracticeOption{
			{ID: AuctionRateAlways, Label: "Yes: the auction rate always wins", Available: true},
			{ID: AuctionRateScrip, Label: "No: a client's special rate for the scrip (or its group) wins", Available: true},
		},
		read: func(o *Ops, ctx context.Context, tenant string) practiceState {
			s, _ := o.BrokerageSetup(ctx, tenant)
			return practiceState{Option: firstOf(s.AuctionRate, AuctionRateAlways)}
		},
		write: func(o *Ops, ctx context.Context, tenant, actor, opt string, vals map[string]string) error {
			s, err := o.BrokerageSetup(ctx, tenant)
			if err != nil {
				return err
			}
			s.AuctionRate = map[bool]string{true: "", false: opt}[opt == AuctionRateAlways]
			s.By, s.At = actor, o.now().Format("2006-01-02 15:04")
			if err := docs.Upsert(ctx, o.Docs, tenant, cBrokSetup, "setup", s, actor); err != nil {
				return err
			}
			o.log(ctx, tenant, actor, "brokerage.setup", "setup", map[string]any{"auction_rate": s.AuctionRate})
			return nil
		},
	},
	{ID: "dp-pledge", Short: "DP pledge charges", Money: true,
		Question: "When are DP pledge charges billed?",
		Why:      "Some firms charge only when a pledge is created; others also when it is closed (released) or invoked.",
		Setting:  "Depository → DP tariff: pledge charges",
		Note:     "Changes the firm's DP tariff. Rate cards with their own pledge lines keep them: change those on the DP tariff screen.",
		Options: []PracticeOption{
			{ID: "created", Label: "Only when a pledge is created", Available: true},
			{ID: "all", Label: "Also when a pledge is closed (released) or invoked", Available: true},
		},
		Fields: []PracticeField{{Key: "charge", Label: "Charge per pledge per ISIN (₹, before GST)", Hint: "Blank keeps the tariff's rates; a line the tariff does not have takes this amount"}},
		read:   readDPPledge,
		write:  writeDPPledge,
	},
	{ID: "account-opening", Short: "Online account opening contracts",
		Question: "Online account opening: whose contracts are the e-Sign, penny drop, KRA and DigiLocker, and is a fee charged online?",
		Why:      "Ledgerline connects to the firm's own accounts with these providers; contracts held by an old vendor must be replaced before it is switched off.",
		Setting:  "Recorded here; the providers are set in Settings → Connections",
		Note:     "Ledgerline does not collect a fee online yet: the amount is recorded only.",
		Options: []PracticeOption{
			{ID: "own", Label: "The firm's own contracts (e-Sign, penny drop, KRA, DigiLocker)", Available: true, Note: "Enter each in Settings → Connections."},
			{ID: "arrangement", Label: "Another arrangement (for example through a vendor)", Available: true, Note: "The firm needs its own before that arrangement ends."},
		},
		Fields: []PracticeField{{Key: "fee", Label: "Account-opening fee charged online (₹, blank for none)"}},
	},
}

func practiceDefByID(id string) (practiceDef, bool) {
	for _, d := range practiceDefs {
		if d.ID == id {
			return d, true
		}
	}
	return practiceDef{}, false
}

// state is the question's behaviour now.
func (o *Ops) practiceNow(ctx context.Context, tenant string, d practiceDef, fp FirmPractice) practiceState {
	if d.read != nil {
		st := d.read(o, ctx, tenant)
		if d.write == nil {
			st.Values = fp.Values
		}
		return st
	}
	return practiceState{Option: fp.Applied, Values: fp.Values}
}

func (o *Ops) firmPractice(ctx context.Context, tenant, id string) FirmPractice {
	fp, _, _ := docs.GetOr(ctx, o.Docs, tenant, cFirmPractice, id, FirmPractice{ID: id})
	if fp.History == nil {
		fp.History = []PracticeChange{}
	}
	return fp
}

// FirmPractices is the questionnaire with each answer, the behaviour now
// and the history (Admin and the compliance officer).
func (o *Ops) FirmPractices(ctx context.Context, tenant, actor string) (FirmPracticesView, error) {
	if err := o.require(ctx, tenant, actor, "Viewing firm practices", RoleCompliance, RoleAdmin); err != nil {
		return FirmPracticesView{}, err
	}
	v := FirmPracticesView{Total: len(practiceDefs)}
	r, _ := o.RoleOf(ctx, tenant, actor)
	v.CanAnswer = r == RoleAdmin && !IsSupportActor(actor)
	v.CanApprove = (r == RoleAdmin || r == RoleCompliance) && !IsSupportActor(actor)
	for i, d := range practiceDefs {
		fp := o.firmPractice(ctx, tenant, d.ID)
		st := o.practiceNow(ctx, tenant, d, fp)
		q := PracticeView{ID: d.ID, N: i + 1, Short: d.Short, Question: d.Question, Why: d.Why, Note: d.Note, Setting: d.Setting, Money: d.Money,
			Fields: d.Fields, Current: st.Option, CurrentNote: st.Note, Values: st.Values, Answered: fp.Answer != "", Answer: fp.Answer, Other: fp.Other,
			By: fp.By, At: fp.At, Pending: fp.Pending}
		for _, x := range d.Options {
			if x.Available && d.avail != nil {
				if why := d.avail(o, ctx, tenant, x.ID); why != "" {
					x.Available, x.Note = false, why
				}
			}
			q.Options = append(q.Options, x)
		}
		q.History = make([]PracticeChange, 0, len(fp.History))
		for j := len(fp.History) - 1; j >= 0; j-- {
			q.History = append(q.History, fp.History[j])
		}
		if q.Answered {
			v.Answered++
		}
		v.Questions = append(v.Questions, q)
	}
	if set, err := o.Settings(ctx, tenant); err == nil {
		v.Checklist = o.setupChecklist(ctx, tenant, set) // the whole go-live checklist, practices included
	}
	return v, nil
}

func cleanPracticeValues(d practiceDef, in map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for k, x := range in {
		ok := false
		for _, f := range d.Fields {
			ok = ok || f.Key == k
		}
		if !ok {
			return nil, invalid("this question has no field %q", k)
		}
		if x = strings.TrimSpace(x); len(x) > 200 {
			return nil, invalid("keep %s under 200 characters", k)
		} else if x != "" {
			out[k] = x
		}
	}
	if f := out["fee"]; f != "" {
		if a, err := money.ParseAmount(f); err != nil || a < 0 {
			return nil, invalid("the account-opening fee is an amount in rupees")
		}
	}
	return out, nil
}

func sameValues(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, x := range a {
		if b[k] != x {
			return false
		}
	}
	return true
}

// SetFirmPractice is an Admin's answer to a question: an option, applied at
// once (or, for a change that affects money or client limits, after a
// second person approves), or Other with the firm's own practice, which
// keeps the behaviour and raises a work item to configure it.
func (o *Ops) SetFirmPractice(ctx context.Context, tenant, actor, id string, a PracticeAnswer) (PracticeView, error) {
	if err := o.require(ctx, tenant, actor, "Answering firm practices", RoleAdmin); err != nil {
		return PracticeView{}, err
	}
	if IsSupportActor(actor) {
		return PracticeView{}, fmt.Errorf("%w: Ledgerline support cannot answer the firm's practices", ErrForbidden)
	}
	d, ok := practiceDefByID(id)
	if !ok {
		return PracticeView{}, ErrNotFound
	}
	a.Option, a.Other, a.Note = strings.TrimSpace(a.Option), strings.TrimSpace(a.Other), strings.TrimSpace(a.Note)
	if len(a.Note) > 500 || len(a.Other) > 1000 {
		return PracticeView{}, invalid("keep the note under 500 characters and the firm's practice under 1,000")
	}
	vals, err := cleanPracticeValues(d, a.Values)
	if err != nil {
		return PracticeView{}, err
	}
	fp := o.firmPractice(ctx, tenant, id)
	if fp.Pending != nil {
		return PracticeView{}, fmt.Errorf("%w: a change to this question from %s is waiting for approval; approve, reject or withdraw it first", ErrState, fp.Pending.By)
	}
	now := o.now()
	st := o.practiceNow(ctx, tenant, d, fp)
	ch := PracticeChange{N: len(fp.History) + 1, Option: a.Option, Label: d.label(a.Option), Values: vals, Note: a.Note, Was: st.Option, By: actor, At: now.Format(time.RFC3339)}
	if a.Option == PracticeOther {
		if a.Other == "" {
			return PracticeView{}, invalid("say how the firm does it")
		}
		ch.Other, ch.Status = a.Other, PracticeRecorded
		key := fmt.Sprintf("%s|%d", id, ch.N)
		if fp.WorkKey != "" {
			o.closeWork(ctx, tenant, actor, wFirmPractice, fp.WorkKey, "answered again")
		}
		o.raiseWork(ctx, tenant, wFirmPractice, key, "Firm practice to configure: "+d.Short,
			fmt.Sprintf("The firm's practice for %q is not one of Ledgerline's options: %s. Ledgerline keeps %q until it is configured. Settle it with Ledgerline, then choose the option in Settings → Firm practices.", d.Question, a.Other, d.label(st.Option)),
			"", RoleAdmin, "practices", now.In(ist).Format("2006-01-02"))
		fp.Answer, fp.Other, fp.WorkKey = PracticeOther, a.Other, key
		if len(vals) > 0 {
			fp.Values = vals
		}
		o.kickControl()
	} else {
		opt, ok := d.option(a.Option)
		if !ok {
			return PracticeView{}, invalid("choose one of the options or Other")
		}
		if !opt.Available {
			return PracticeView{}, invalid("%q is not available yet", opt.Label)
		}
		if d.avail != nil {
			if why := d.avail(o, ctx, tenant, a.Option); why != "" {
				return PracticeView{}, invalid("%s", why)
			}
		}
		if d.ID == "dp-pledge" && vals["charge"] != "" {
			if amt, err := money.ParseAmount(vals["charge"]); err != nil || amt <= 0 {
				return PracticeView{}, invalid("the charge per pledge is an amount in rupees, more than 0")
			}
		}
		same := st.Option == a.Option && (d.write == nil || len(vals) == 0 || sameValues(vals, st.Values))
		switch {
		case same && (d.write != nil || sameValues(vals, fp.Values)):
			ch.Status = PracticeConfirmed
		case d.Money && d.write != nil:
			ch.Status = PracticeWaiting
			fp.Pending = &ch
			if err := docs.Upsert(ctx, o.Docs, tenant, cFirmPractice, id, fp, actor); err != nil {
				return PracticeView{}, err
			}
			o.raiseWork(ctx, tenant, wFirmPractice, fmt.Sprintf("%s|approve|%d", id, ch.N), "Firm practice change to approve: "+d.Short,
				fmt.Sprintf("%s proposes %q (now %q). It affects money or client limits, so the compliance officer or another Admin approves it in Settings → Firm practices.", actor, ch.Label, d.label(st.Option)),
				"", RoleCompliance, "practices", now.In(ist).Format("2006-01-02"))
			o.log(ctx, tenant, actor, "firm_practice.proposed", id, map[string]any{"option": a.Option, "was": st.Option, "n": ch.N})
			return o.practiceView(ctx, tenant, actor, id)
		default:
			if d.write != nil {
				if err := d.write(o, ctx, tenant, actor, a.Option, vals); err != nil {
					return PracticeView{}, err
				}
			}
			ch.Status = PracticeApplied
		}
		o.practiceAnswered(ctx, tenant, actor, &fp, ch)
	}
	fp.By, fp.At = actor, ch.At
	fp.History = appendPracticeHist(fp.History, ch)
	if err := docs.Upsert(ctx, o.Docs, tenant, cFirmPractice, id, fp, actor); err != nil {
		return PracticeView{}, err
	}
	o.log(ctx, tenant, actor, "firm_practice.answered", id, map[string]any{"option": a.Option, "was": st.Option, "status": ch.Status, "n": ch.N})
	return o.practiceView(ctx, tenant, actor, id)
}

// practiceAnswered records an option as the firm's answer and closes the
// "needs configuring" item an earlier Other raised.
func (o *Ops) practiceAnswered(ctx context.Context, tenant, actor string, fp *FirmPractice, ch PracticeChange) {
	fp.Answer, fp.Applied, fp.Other = ch.Option, ch.Option, ""
	if len(ch.Values) > 0 || fp.Values != nil {
		fp.Values = ch.Values
	}
	if fp.WorkKey != "" {
		o.closeWork(ctx, tenant, actor, wFirmPractice, fp.WorkKey, "the firm chose "+ch.Label)
		fp.WorkKey = ""
		o.kickControl()
	}
}

func appendPracticeHist(h []PracticeChange, ch PracticeChange) []PracticeChange {
	h = append(h, ch)
	if len(h) > practiceHistMax {
		h = h[len(h)-practiceHistMax:]
	}
	return h
}

// DecideFirmPractice approves or rejects a change waiting for approval
// (the compliance officer or another Admin), or withdraws it (the Admin
// who proposed it, with approve false). Approving applies it.
func (o *Ops) DecideFirmPractice(ctx context.Context, tenant, actor, id string, approve bool, note string) (PracticeView, error) {
	if err := o.require(ctx, tenant, actor, "Approving firm practices", RoleCompliance, RoleAdmin); err != nil {
		return PracticeView{}, err
	}
	if IsSupportActor(actor) {
		return PracticeView{}, fmt.Errorf("%w: Ledgerline support cannot approve the firm's practices", ErrForbidden)
	}
	d, ok := practiceDefByID(id)
	if !ok {
		return PracticeView{}, ErrNotFound
	}
	fp := o.firmPractice(ctx, tenant, id)
	if fp.Pending == nil {
		return PracticeView{}, fmt.Errorf("%w: no change to this question is waiting for approval", ErrState)
	}
	note = strings.TrimSpace(note)
	if len(note) > 500 {
		return PracticeView{}, invalid("keep the note under 500 characters")
	}
	ch := *fp.Pending
	own := ch.By == actor
	switch {
	case own && approve:
		return PracticeView{}, fmt.Errorf("%w: you proposed this change; the compliance officer or another Admin approves it", ErrForbidden)
	case !approve && !own && note == "":
		return PracticeView{}, invalid("say why the change is rejected")
	}
	now := o.now().Format(time.RFC3339)
	ch.DecidedBy, ch.DecidedAt, ch.Decision = actor, now, note
	event := "firm_practice.rejected"
	switch {
	case approve:
		if err := d.write(o, ctx, tenant, actor, ch.Option, ch.Values); err != nil {
			return PracticeView{}, err
		}
		ch.Status, event = PracticeApproved, "firm_practice.approved"
		o.practiceAnswered(ctx, tenant, actor, &fp, ch)
		fp.By, fp.At = ch.By, now
	case own:
		ch.Status, event = PracticeWithdrawn, "firm_practice.withdrawn"
	default:
		ch.Status = PracticeRejected
	}
	fp.Pending = nil
	fp.History = appendPracticeHist(fp.History, ch)
	if err := docs.Upsert(ctx, o.Docs, tenant, cFirmPractice, id, fp, actor); err != nil {
		return PracticeView{}, err
	}
	o.closeWork(ctx, tenant, actor, wFirmPractice, fmt.Sprintf("%s|approve|%d", id, ch.N), strings.ToLower(ch.Status))
	o.log(ctx, tenant, actor, event, id, map[string]any{"option": ch.Option, "proposed_by": ch.By, "n": ch.N, "note": note})
	return o.practiceView(ctx, tenant, actor, id)
}

func (o *Ops) practiceView(ctx context.Context, tenant, actor, id string) (PracticeView, error) {
	v, err := o.FirmPractices(ctx, tenant, actor)
	if err != nil {
		return PracticeView{}, err
	}
	for _, q := range v.Questions {
		if q.ID == id {
			return q, nil
		}
	}
	return PracticeView{}, ErrNotFound
}

// practicesOpen is how many questions are not answered yet.
func (o *Ops) practicesOpen(ctx context.Context, tenant string) int {
	n := 0
	for _, d := range practiceDefs {
		if o.firmPractice(ctx, tenant, d.ID).Answer == "" {
			n++
		}
	}
	return n
}

// practiceChecklist is the firm practices' part of the go-live checklist:
// every question while any is unanswered, then one line; and a line for
// each practice answered Other that still needs configuring.
func (o *Ops) practiceChecklist(ctx context.Context, tenant string) []control.SetupItem {
	var all, other []control.SetupItem
	open := 0
	for _, d := range practiceDefs {
		fp := o.firmPractice(ctx, tenant, d.ID)
		it := control.SetupItem{Key: "practice_" + d.ID, Label: "Firm practice: " + d.Short, OK: fp.Answer != "" && fp.Answer != PracticeOther}
		switch {
		case fp.Answer == "" && fp.Pending != nil:
			it.Detail = "waiting for approval"
		case fp.Answer == "":
			it.Detail = "not answered (Settings → Firm practices)"
		case fp.Answer == PracticeOther:
			it.Detail = "answered Other: needs configuring"
			other = append(other, it)
		}
		if fp.Answer == "" {
			open++
		}
		all = append(all, it)
	}
	if open > 0 {
		return all
	}
	return append([]control.SetupItem{{Key: "practices", Label: "Firm practices answered", OK: true, Detail: fmt.Sprintf("%d questions", len(practiceDefs))}}, other...)
}

// GoLiveChecklist is the firm's go-live checklist as Ledgerline Control
// sees it (setupChecklist), for the firm's Admin and compliance officer.
func (o *Ops) GoLiveChecklist(ctx context.Context, tenant, actor string) ([]control.SetupItem, error) {
	if err := o.require(ctx, tenant, actor, "Viewing the go-live checklist", RoleCompliance, RoleAdmin); err != nil {
		return nil, err
	}
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return nil, err
	}
	return o.setupChecklist(ctx, tenant, set), nil
}
