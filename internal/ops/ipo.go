package ops

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// ---- public issues: IPOs, SME IPOs, NCDs and offers for sale ----
//
// The firm sets up each issue (price band, lot size, dates, categories and
// their limits, registrar, the commission in the red herring prospectus),
// takes each client's prior instruction (staff record how it came; the
// client portal records the client's own action), checks the bid against
// the rules, and sends the day's bids to the exchange: as a bulk upload file
// for NSE's eIPO or BSE's iBBS (layouts in config/formats, provisional until
// the exchanges' own are in hand), or through NSE's eIPO API once NSE has
// given access (ipoeipo.go). Responses, the UPI mandate status, allotment,
// unblocking and the demat credit then move each bid through its life:
//
//	Draft → Uploaded → Mandate pending → Accepted | Declined | Failed
//	      → Allotted | Not allotted → Unblocked → Credited
//
// with Rejected (by the exchange) and Cancelled on the side.
//
// Rules (SEBI ICDR master circular; UPI for individuals bidding up to ₹5
// lakh, SEBI circular 2022/45; T+3 listing, SEBI circular 2023/140):
//   - Retail individual: up to ₹2 lakh; non-institutional: one third for
//     ₹2–10 lakh, two thirds above ₹10 lakh; employees up to ₹5 lakh.
//   - Cut-off price only for retail, employee, shareholder and
//     policyholder bids. One application per PAN per issue.
//   - SME issues from 1 July 2025 (NSE circular IPO68604,
//     https://nsearchives.nseindia.com/content/circulars/IPO68604.pdf): an
//     individual bids exactly 2 lots; no cut-off; no cancellation or
//     downward revision in any category.
//   - Mainboard bids up to ₹2 lakh may be cancelled or revised; other
//     categories may only revise upward.
//   - Bids close at 4 pm on the closing day (T); UPI mandates may be
//     accepted until 5 pm; a failed mandate may be re-sent once.
//   - T+1 allotment, T+2 unblocking and demat credit, T+3 listing.
//
// Every instruction (place, modify, cancel) is kept in a consent log with
// the acknowledgement given to the client; documents are never deleted
// (electronic records are kept at least 3 years, NSE circular 65/2025).

const (
	cIPOIssue = "ipoissue"
	cIPOInstr = "ipoinstr" // consent log: one record per client instruction, never changed
	cIPOUPI   = "ipoupi"   // each client's UPI IDs for ASBA, keyed by UCC
	cIPOBatch = "ipobatch" // bid files written for an exchange
	cIPOState = "ipostate" // alerts sent and API state
)

// Issue types.
const (
	IssueMainboard = "Mainboard"
	IssueSME       = "SME"
	IssueNCD       = "NCD"
	IssueOFS       = "OFS"
)

// IssueTypes lists the issue types in display order.
var IssueTypes = []string{IssueMainboard, IssueSME, IssueNCD, IssueOFS}

// Bid statuses.
const (
	BidDraft          = "Draft"
	BidUploaded       = "Uploaded"
	BidMandatePending = "Mandate pending"
	BidAccepted       = "Accepted"
	BidDeclined       = "Declined"
	BidFailed         = "Failed"
	BidRejected       = "Rejected"
	BidCancelled      = "Cancelled"
	BidAllotted       = "Allotted"
	BidNotAllotted    = "Not allotted"
	BidUnblocked      = "Unblocked"
	BidCredited       = "Credited"
)

// BidStatuses lists the statuses in lifecycle order.
var BidStatuses = []string{BidDraft, BidUploaded, BidMandatePending, BidAccepted, BidDeclined, BidFailed, BidRejected, BidCancelled, BidAllotted, BidNotAllotted, BidUnblocked, BidCredited}

// Bid modes.
const (
	ModeUPI    = "UPI"    // UPI mandate on the client's own UPI ID
	ModeASBA   = "ASBA"   // blocked in the client's own bank account by the SCSB (above ₹5 lakh, non-individuals)
	ModeMargin = "Margin" // offer for sale: cash margin from the client's ledger
)

// IPOBidOption is one of up to three price and quantity options of a bid.
type IPOBidOption struct {
	Lots   int64     `json:"lots"`
	Price  money.Dec `json:"price"`
	CutOff bool      `json:"cut_off"`
	Series string    `json:"series,omitempty"` // NCD series
}

// IPOEvent is one step in a bid's history.
type IPOEvent struct {
	At   string `json:"at"`
	By   string `json:"by"`
	What string `json:"what"`
}

// IPOComp tracks compensation for late unblocking (₹100 a day or 15% a
// year of the bid amount, whichever is higher, payable by whoever caused
// the delay: usually the client's bank as SCSB).
type IPOComp struct {
	Status    string       `json:"status"` // Claimed, Paid, Waived
	Party     string       `json:"party"`  // SCSB, Sponsor bank, Firm
	Ref       string       `json:"ref,omitempty"`
	ClaimedOn string       `json:"claimed_on,omitempty"`
	PaidOn    string       `json:"paid_on,omitempty"`
	Paid      money.Amount `json:"paid,omitempty"`
	Entry     string       `json:"entry,omitempty"` // ledger entry when the firm paid
	Note      string       `json:"note,omitempty"`
	By        string       `json:"by"`
}

// IPOApp is a client's application in a public issue. Applications placed
// through Ledgerline carry the issue, options and lifecycle; those loaded
// from the exchange platform's IPO-BIDS file carry only the first block of
// fields, and their status is worked out from the mandate and allotment.
type IPOApp struct {
	ID        string       `json:"id"`
	UCC       string       `json:"ucc"`
	Name      string       `json:"name,omitempty"`
	Issue     string       `json:"issue"`
	Lots      int64        `json:"lots"`
	Amount    money.Amount `json:"amount"`
	UPI       string       `json:"upi"`
	Mandate   string       `json:"mandate"` // Accepted, Pending, Declined, Failed
	Allotment string       `json:"allotment"`
	Allotted  int64        `json:"allotted_lots"`
	Date      string       `json:"date,omitempty"` // bid date (the upload's business date)

	IssueID    string         `json:"issue_id,omitempty"`
	Exchange   string         `json:"exchange,omitempty"`
	Category   string         `json:"category,omitempty"`
	Options    []IPOBidOption `json:"options,omitempty"`
	Mode       string         `json:"mode,omitempty"`
	PAN        string         `json:"pan,omitempty"`
	Depository string         `json:"depository,omitempty"`
	DPID       string         `json:"dp_id,omitempty"`
	BenID      string         `json:"ben_id,omitempty"`
	Status     string         `json:"status"`
	Source     string         `json:"source,omitempty"` // Staff, Client portal, Exchange file
	// Instruction is the consent log record of the latest instruction and
	// Ack the acknowledgement number given to the client for it.
	Instruction string `json:"instruction,omitempty"`
	Ack         string `json:"ack,omitempty"`
	// Pending is an action waiting to go to the exchange (New, Modify,
	// Cancel); Sent the one sent and not yet answered.
	Pending    string `json:"pending,omitempty"`
	Sent       string `json:"sent,omitempty"`
	SentAt     string `json:"sent_at,omitempty"`
	Batch      string `json:"batch,omitempty"`
	AppNo      string `json:"app_no,omitempty"` // the platform's application number
	ExchStatus string `json:"exch_status,omitempty"`
	ExchReason string `json:"exch_reason,omitempty"`
	DPCheck    string `json:"dp_check,omitempty"` // Verified, Mismatch, Pending
	UMN        string `json:"umn,omitempty"`
	PayStatus  string `json:"pay_status,omitempty"` // sponsor bank code
	MandateAt  string `json:"mandate_at,omitempty"`
	MandateWhy string `json:"mandate_reason,omitempty"`
	Reinit     string `json:"reinitiated,omitempty"` // when a failed mandate was re-sent (once)
	CancelOn   string `json:"cancelled_on,omitempty"`
	// After the issue closes.
	AllotDate   string       `json:"allot_date,omitempty"`
	Shares      int64        `json:"allotted_shares,omitempty"`
	AllotPrice  money.Dec    `json:"allot_price,omitempty"`
	UnblockedOn string       `json:"unblocked_on,omitempty"`
	Unblocked   money.Amount `json:"unblocked_amount,omitempty"`
	CreditedOn  string       `json:"credited_on,omitempty"`
	CreditedQty int64        `json:"credited_qty,omitempty"`
	Comp        *IPOComp     `json:"compensation,omitempty"`
	Tickets     []string     `json:"tickets,omitempty"`
	// Offer for sale: the cash margin taken from the client's ledger and
	// the entry that took it; Obligation says how the allotted shares are
	// billed.
	Margin      money.Amount `json:"margin,omitempty"`
	MarginEntry string       `json:"margin_entry,omitempty"`
	MarginRev   int          `json:"margin_rev,omitempty"`
	Obligation  string       `json:"obligation,omitempty"`
	By          string       `json:"by,omitempty"`
	Created     string       `json:"created,omitempty"`
	Updated     string       `json:"updated,omitempty"`
	History     []IPOEvent   `json:"history,omitempty"`
}

// live reports whether the bid still counts against the PAN's one bid.
func (a IPOApp) live() bool {
	return a.Status != BidCancelled && a.Status != BidRejected
}

// preAllotment statuses can still move on with the allotment.
var preAllotment = map[string]bool{"": true, BidDraft: true, BidUploaded: true, BidMandatePending: true, BidAccepted: true, BidDeclined: true, BidFailed: true}

// normalise works out the status of applications loaded from the exchange
// platform's file (IPO-BIDS), which the IPO-ALLOTMENT file then moves on.
// A bid placed through Ledgerline keeps its own status.
func (a *IPOApp) normalise() {
	if a.IssueID != "" {
		return
	}
	a.Source = firstOf(a.Source, "Exchange file")
	if !preAllotment[a.Status] {
		return
	}
	switch {
	case a.Allotted > 0:
		a.Status = BidAllotted
	case strings.HasPrefix(a.Allotment, "Not allotted"):
		a.Status = BidNotAllotted
	case strings.HasPrefix(a.Allotment, "Rejected"):
		a.Status = BidRejected
	case a.Mandate == "Accepted":
		a.Status = BidAccepted
	case a.Mandate == "Declined":
		a.Status = BidDeclined
	case a.Mandate == "Failed":
		a.Status = BidFailed
	default:
		a.Status = BidMandatePending
	}
}

func (a *IPOApp) event(at time.Time, by, what string, args ...any) {
	a.History = append(a.History, IPOEvent{At: at.Format(time.RFC3339), By: by, What: fmt.Sprintf(what, args...)})
	if len(a.History) > 60 {
		a.History = a.History[len(a.History)-60:]
	}
	a.Updated = at.Format(time.RFC3339)
}

// IPOApps lists every application, with the status worked out for those
// loaded from the exchange platform. An application downloaded from the
// platform that is a bid placed through Ledgerline is shown once.
func (o *Ops) IPOApps(ctx context.Context, tenant string) ([]IPOApp, error) {
	as, err := docs.List[IPOApp](ctx, o.Docs, tenant, cIPO, "")
	if err != nil {
		return nil, err
	}
	ours := map[string]bool{}
	for _, a := range as {
		if a.IssueID != "" && a.AppNo != "" {
			ours[a.AppNo] = true
		}
	}
	out := make([]IPOApp, 0, len(as))
	for _, a := range as {
		if a.IssueID == "" && ours[a.ID] {
			continue
		}
		a.normalise()
		out = append(out, a)
	}
	return out, nil
}

func (o *Ops) ipoBid(ctx context.Context, tenant, id string) (IPOApp, error) {
	a, _, err := docs.Get[IPOApp](ctx, o.Docs, tenant, cIPO, id)
	if errors.Is(err, docs.ErrNotFound) {
		return a, fmt.Errorf("%w: no bid %s", ErrNotFound, id)
	}
	a.normalise()
	return a, err
}

// ---- the issue master ----

// IPOCategory is an investor category of an issue with its limits.
type IPOCategory struct {
	Code string `json:"code"`
	Name string `json:"name"`
	// ExchCode is the platform's code for the category, by exchange (as
	// the issue's bid file or API expects it); the code itself if unset.
	ExchCode   map[string]string `json:"exch_code,omitempty"`
	SubCode    string            `json:"sub_code,omitempty"`
	Above      money.Amount      `json:"above,omitempty"`      // the application must exceed this
	Upto       money.Amount      `json:"upto,omitempty"`       // and be at most this (0: no limit)
	MinAmount  money.Amount      `json:"min_amount,omitempty"` // and at least this (NCD minimum application)
	MinLots    int64             `json:"min_lots,omitempty"`
	ExactLots  int64             `json:"exact_lots,omitempty"`
	CutOff     bool              `json:"cut_off"`
	Individual bool              `json:"individual"`         // individuals (and HUFs) only
	Modify     string            `json:"modify"`             // any (raise, lower, cancel) or up (raise only, no cancel)
	Discount   money.Dec         `json:"discount,omitempty"` // ₹ a share off the price
	Declare    string            `json:"declare,omitempty"`  // eligibility the client must declare (employee, shareholder, policyholder)
	// EligibleISIN: a shareholder reservation needs a holding of this ISIN.
	EligibleISIN string `json:"eligible_isin,omitempty"`
	Day          string `json:"day,omitempty"` // offer for sale: T (non-retail) or T+1 (retail)
}

func (c IPOCategory) exch(ex string) string {
	if v := strings.TrimSpace(c.ExchCode[ex]); v != "" {
		return v
	}
	return c.Code
}

const lakh = money.Amount(100_000_00) // ₹1 lakh in paise

// DefaultIPOCategories are the usual categories of an issue type; the firm
// adjusts them from the red herring prospectus.
func DefaultIPOCategories(typ string) []IPOCategory {
	switch typ {
	case IssueSME:
		return []IPOCategory{
			{Code: "IND", Name: "Individual investor (exactly 2 lots)", ExactLots: 2, Individual: true, Modify: "up"},
			{Code: "SNII", Name: "Non-institutional, up to ₹10 lakh (more than 2 lots)", MinLots: 3, Upto: 10 * lakh, Modify: "up"},
			{Code: "BNII", Name: "Non-institutional, above ₹10 lakh", MinLots: 3, Above: 10 * lakh, Modify: "up"},
		}
	case IssueNCD:
		return []IPOCategory{
			{Code: "IV", Name: "Category IV: retail individuals, up to ₹10 lakh", Upto: 10 * lakh, MinAmount: 10_000_00, Individual: true, Modify: "any"},
			{Code: "III", Name: "Category III: individuals above ₹10 lakh", Above: 10 * lakh, Individual: true, Modify: "any"},
			{Code: "II", Name: "Category II: non-institutional (companies, trusts, firms)", MinAmount: 10_000_00, Modify: "any"},
		}
	case IssueOFS:
		return []IPOCategory{
			{Code: "RETAIL", Name: "Retail (up to ₹2 lakh, on T+1)", Upto: 2 * lakh, CutOff: true, Individual: true, Modify: "any", Day: "T+1"},
			{Code: "NONRETAIL", Name: "Non-retail (on T)", Above: 2 * lakh, Modify: "up", Day: "T"},
		}
	}
	return []IPOCategory{
		{Code: "RII", Name: "Retail individual, up to ₹2 lakh", Upto: 2 * lakh, CutOff: true, Individual: true, Modify: "any"},
		{Code: "SNII", Name: "Non-institutional, ₹2 lakh to ₹10 lakh", Above: 2 * lakh, Upto: 10 * lakh, Modify: "up"},
		{Code: "BNII", Name: "Non-institutional, above ₹10 lakh", Above: 10 * lakh, Modify: "up"},
		{Code: "EMP", Name: "Employee reservation, up to ₹5 lakh", Upto: 5 * lakh, CutOff: true, Individual: true, Modify: "any", Declare: "an eligible employee of the issuer"},
		{Code: "SHA", Name: "Shareholder reservation, up to ₹2 lakh", Upto: 2 * lakh, CutOff: true, Individual: true, Modify: "any", Declare: "a shareholder of the issuer's listed parent on the record date"},
		{Code: "POL", Name: "Policyholder reservation, up to ₹2 lakh", Upto: 2 * lakh, CutOff: true, Individual: true, Modify: "any", Declare: "a policyholder of the issuer"},
	}
}

// IPOCommission is the commission the issuer pays the firm on its
// clients' applications, and how far it has been claimed and received.
type IPOCommission struct {
	Apps       int          `json:"applications"`
	Value      money.Amount `json:"allotted_value"`
	Amount     money.Amount `json:"amount"` // before GST
	GST        money.Amount `json:"gst"`
	Total      money.Amount `json:"total"`
	ClaimedOn  string       `json:"claimed_on,omitempty"`
	ClaimEntry string       `json:"claim_entry,omitempty"`
	Received   money.Amount `json:"received,omitempty"`
	TDS        money.Amount `json:"tds,omitempty"`
	Receipts   []string     `json:"receipt_entries,omitempty"`
	Ref        string       `json:"ref,omitempty"` // invoice or claim number
}

// IPOTimeline gives the issue's dates under the T+3 timetable.
type IPOTimeline struct {
	T       string `json:"t"`
	Allot   string `json:"allotment"`
	Unblock string `json:"unblock_by"`
	Credit  string `json:"credit_by"`
	Listing string `json:"listing"`
}

// IPOIssue is a public issue the firm's clients can bid in.
type IPOIssue struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`   // shown on each application
	Symbol    string   `json:"symbol"` // the platform's issue symbol
	Company   string   `json:"company,omitempty"`
	Type      string   `json:"type"`
	Exchanges []string `json:"exchanges"`
	ISIN      string   `json:"isin,omitempty"`
	Open      string   `json:"open"`
	Close     string   `json:"close"`
	// PreApply lets clients apply before the issue opens; their bids wait
	// as drafts for the first bidding day.
	PreApply      string        `json:"pre_apply,omitempty"`
	OpenTime      string        `json:"open_time"`      // daily bidding from (IST)
	DayClose      string        `json:"day_close"`      // daily bidding to, other than T
	CloseTime     string        `json:"close_time"`     // bidding to on T
	MandateCutoff string        `json:"mandate_cutoff"` // UPI mandates accepted to on T
	PriceLow      money.Dec     `json:"price_low"`      // floor (offer for sale), face value (NCD)
	PriceHigh     money.Dec     `json:"price_high"`     // cap; offer for sale: the retail cut-off price once known
	Tick          money.Dec     `json:"tick,omitempty"`
	LotSize       int64         `json:"lot_size"` // shares (NCD units) a lot
	Categories    []IPOCategory `json:"categories"`
	Series        []string      `json:"series,omitempty"` // NCD series
	Registrar     string        `json:"registrar,omitempty"`
	SponsorBank   string        `json:"sponsor_bank,omitempty"`
	IssuePrice    money.Dec     `json:"issue_price,omitempty"` // final price on the basis of allotment
	// Commission as the red herring prospectus sets it: per valid (or
	// allotted) application and/or a percentage of the value allotted.
	CommPerApp money.Amount   `json:"commission_per_app,omitempty"`
	CommPct    money.Dec      `json:"commission_pct,omitempty"`
	CommOn     string         `json:"commission_on,omitempty"` // Allotted (default) or Valid
	CommCats   []string       `json:"commission_categories,omitempty"`
	Commission *IPOCommission `json:"commission,omitempty"`
	AllotDate  string         `json:"allotment_date,omitempty"`
	Listing    string         `json:"listing_date,omitempty"`
	Withdrawn  bool           `json:"withdrawn,omitempty"`
	Source     string         `json:"source,omitempty"` // Staff, Imported
	By         string         `json:"by,omitempty"`
	Updated    string         `json:"updated,omitempty"`
	// Worked out when shown.
	Status   string      `json:"status"` // Upcoming, Open, Closed, Allotment, Listed, Withdrawn
	Timeline IPOTimeline `json:"timeline"`
}

func (iss IPOIssue) category(code string) (IPOCategory, bool) {
	for _, c := range iss.Categories {
		if strings.EqualFold(c.Code, code) {
			return c, true
		}
	}
	return IPOCategory{}, false
}

// reHHMM (HH:MM) is declared in mf.go.

func hhmmOr(s, def string) string {
	if reHHMM.MatchString(strings.TrimSpace(s)) {
		return strings.TrimSpace(s)
	}
	return def
}

// defaults fills times and categories the firm left out.
func (iss *IPOIssue) defaults() {
	open, day, closeT, mand := "10:00", "17:00", "16:00", "17:00"
	if iss.Type == IssueOFS {
		open, day, closeT, mand = "09:15", "15:30", "15:30", ""
	}
	iss.OpenTime, iss.DayClose, iss.CloseTime = hhmmOr(iss.OpenTime, open), hhmmOr(iss.DayClose, day), hhmmOr(iss.CloseTime, closeT)
	iss.MandateCutoff = hhmmOr(iss.MandateCutoff, mand)
	if len(iss.Categories) == 0 {
		iss.Categories = DefaultIPOCategories(iss.Type)
	}
	if iss.Tick.Sign() <= 0 {
		iss.Tick = money.DecInt(1)
	}
	if iss.CommOn == "" {
		iss.CommOn = "Allotted"
	}
}

// timeline works out the T+3 dates (or the offer-for-sale settlement).
func (iss IPOIssue) timeline(set Settings) IPOTimeline {
	t := IPOTimeline{T: iss.Close}
	if !validDate(iss.Close) {
		return t
	}
	if iss.Type == IssueOFS {
		t.Allot = iss.Close
		t.Credit = set.NextSettlementDay(iss.Close)
		t.Unblock = t.Credit
		return t
	}
	t.Allot = firstOf(iss.AllotDate, set.NextBusinessDay(iss.Close))
	t.Unblock = set.NextBusinessDay(set.NextBusinessDay(iss.Close))
	t.Credit = t.Unblock
	t.Listing = firstOf(iss.Listing, set.NextBusinessDay(t.Unblock))
	return t
}

// status works out where the issue is at now.
func (iss IPOIssue) status(now time.Time, allotted bool) string {
	today, hm := now.Format("2006-01-02"), now.Format("15:04")
	switch {
	case iss.Withdrawn:
		return "Withdrawn"
	case today < iss.Open:
		return "Upcoming"
	case today < iss.Close || today == iss.Close && hm < iss.CloseTime:
		return "Open"
	case iss.Timeline.Listing != "" && today >= iss.Timeline.Listing:
		return "Listed"
	case allotted || iss.Timeline.Allot != "" && today > iss.Timeline.Allot:
		return "Allotment"
	}
	return "Closed"
}

// biddingDay reports whether date is one of the issue's bidding days.
func (iss IPOIssue) biddingDay(set Settings, date string) bool {
	return date >= iss.Open && date <= iss.Close && set.IsBusinessDay(date)
}

// liveAt reports whether bids can reach the exchange at now (the session
// is open on a bidding day).
func (iss IPOIssue) liveAt(set Settings, now time.Time) bool {
	d, hm := now.Format("2006-01-02"), now.Format("15:04")
	if iss.Withdrawn || !iss.biddingDay(set, d) || hm < iss.OpenTime {
		return false
	}
	if d == iss.Close {
		return hm < iss.CloseTime
	}
	return hm < iss.DayClose
}

// closed reports whether the last bidding cut-off has passed.
func (iss IPOIssue) closed(now time.Time) bool {
	d, hm := now.Format("2006-01-02"), now.Format("15:04")
	return d > iss.Close || d == iss.Close && hm >= iss.CloseTime
}

func (iss IPOIssue) windowText() string {
	return fmt.Sprintf("%s to %s, %s to %s (%s on %s)", fmtDay(iss.Open), fmtDay(iss.Close), iss.OpenTime, iss.DayClose, iss.CloseTime, fmtDay(iss.Close))
}

// reISIN is declared in mffeeds.go (IN + 9 characters + a check digit).
var reSymbol = regexp.MustCompile(`^[A-Z0-9][A-Z0-9&_-]{0,19}$`)

// SaveIPOIssue creates or updates an issue.
func (o *Ops) SaveIPOIssue(ctx context.Context, tenant, actor string, iss IPOIssue) (IPOIssue, error) {
	if err := o.require(ctx, tenant, actor, "Setting up public issues", RoleMaker, RoleChecker); err != nil {
		return iss, err
	}
	return o.saveIPOIssue(ctx, tenant, actor, iss, "Staff")
}

func (o *Ops) saveIPOIssue(ctx context.Context, tenant, actor string, iss IPOIssue, source string) (IPOIssue, error) {
	iss.Symbol = strings.ToUpper(strings.TrimSpace(iss.Symbol))
	iss.Name = strings.TrimSpace(iss.Name)
	iss.ISIN = strings.ToUpper(strings.TrimSpace(iss.ISIN))
	if iss.Name == "" && iss.Symbol != "" {
		iss.Name = iss.Symbol + " " + map[string]string{IssueNCD: "NCD", IssueOFS: "OFS"}[iss.Type]
		if iss.Type == IssueMainboard || iss.Type == IssueSME || iss.Type == "" {
			iss.Name = iss.Symbol + " IPO"
		}
	}
	if iss.Type == "" {
		iss.Type = IssueMainboard
	}
	exs := []string{}
	for _, e := range iss.Exchanges {
		e = strings.ToUpper(strings.TrimSpace(e))
		if e != "" && !contains(exs, e) {
			exs = append(exs, e)
		}
	}
	iss.Exchanges = exs
	iss.defaults()
	switch {
	case !reSymbol.MatchString(iss.Symbol):
		return iss, invalid("give the issue symbol as the exchange shows it (letters and digits)")
	case !contains(IssueTypes, iss.Type):
		return iss, invalid("issue type is Mainboard, SME, NCD or OFS")
	case len(iss.Exchanges) == 0:
		return iss, invalid("choose the exchange(s) the issue bids on")
	case !validDate(iss.Open) || !validDate(iss.Close) || iss.Close < iss.Open:
		return iss, invalid("give the opening and closing dates (closing on or after opening)")
	case iss.PreApply != "" && (!validDate(iss.PreApply) || iss.PreApply > iss.Open):
		return iss, invalid("clients can apply early from a date on or before the opening date")
	case iss.ISIN != "" && !reISIN.MatchString(iss.ISIN):
		return iss, invalid("the ISIN is 12 characters starting IN")
	case iss.LotSize <= 0:
		return iss, invalid("give the lot size (shares, or NCD units, in a lot)")
	case iss.PriceLow.Sign() <= 0:
		return iss, invalid("give the price band (the floor price for an offer for sale, the face value for NCDs)")
	case iss.CommPerApp < 0 || iss.CommPct.Sign() < 0 || iss.CommPct.Cmp(money.DecInt(10)) > 0:
		return iss, invalid("commission must be zero or more (at most 10%% of the value)")
	case iss.CommOn != "Allotted" && iss.CommOn != "Valid":
		return iss, invalid("commission is paid on Allotted or Valid applications")
	}
	for _, e := range iss.Exchanges {
		if e != "NSE" && e != "BSE" {
			return iss, invalid("public issues bid on NSE or BSE")
		}
	}
	if iss.Type == IssueOFS && iss.Close != iss.Open {
		set, _ := o.Settings(ctx, tenant)
		if set.NextBusinessDay(iss.Open) != iss.Close {
			return iss, invalid("an offer for sale runs on two days: T for non-retail and the next trading day (T+1) for retail")
		}
	}
	switch iss.Type {
	case IssueNCD:
		iss.PriceHigh = iss.PriceLow
	case IssueOFS:
		if iss.PriceHigh.Sign() != 0 && iss.PriceHigh.Cmp(iss.PriceLow) < 0 {
			return iss, invalid("the retail cut-off price cannot be below the floor price")
		}
	default:
		if iss.PriceHigh.Sign() == 0 {
			iss.PriceHigh = iss.PriceLow // fixed price issue
		}
		if iss.PriceHigh.Cmp(iss.PriceLow) < 0 {
			return iss, invalid("the cap price cannot be below the floor price")
		}
	}
	seen := map[string]bool{}
	for i := range iss.Categories {
		c := &iss.Categories[i]
		c.Code = strings.ToUpper(strings.TrimSpace(c.Code))
		c.EligibleISIN = strings.ToUpper(strings.TrimSpace(c.EligibleISIN))
		if c.Modify == "" {
			c.Modify = "up"
		}
		if iss.Type == IssueSME {
			c.CutOff, c.Modify = false, "up" // no cut-off, no cancellation or downward revision
		}
		switch {
		case c.Code == "" || seen[c.Code]:
			return iss, invalid("each category needs its own code")
		case c.Modify != "any" && c.Modify != "up":
			return iss, invalid("category %s: modification is any or up", c.Code)
		case c.Above < 0 || c.Upto < 0 || c.MinAmount < 0 || c.MinLots < 0 || c.ExactLots < 0 || c.Discount.Sign() < 0:
			return iss, invalid("category %s: limits cannot be negative", c.Code)
		case c.Upto > 0 && c.Upto <= c.Above:
			return iss, invalid("category %s: the upper limit must be above the lower", c.Code)
		case c.EligibleISIN != "" && !reISIN.MatchString(c.EligibleISIN):
			return iss, invalid("category %s: the eligible ISIN is 12 characters starting IN", c.Code)
		case c.Day != "" && c.Day != "T" && c.Day != "T+1":
			return iss, invalid("category %s: the bidding day is T or T+1", c.Code)
		}
		if c.Name == "" {
			c.Name = c.Code
		}
		seen[c.Code] = true
	}
	for _, c := range iss.CommCats {
		if !seen[strings.ToUpper(c)] {
			return iss, invalid("commission category %s is not a category of the issue", c)
		}
	}
	now := o.now()
	if iss.ID == "" {
		iss.ID = iss.Symbol + "-" + strings.ReplaceAll(iss.Open, "-", "")
		if _, _, err := docs.Get[IPOIssue](ctx, o.Docs, tenant, cIPOIssue, iss.ID); err == nil {
			return iss, invalid("issue %s is already set up; change it instead", iss.ID)
		}
	} else if old, _, err := docs.Get[IPOIssue](ctx, o.Docs, tenant, cIPOIssue, iss.ID); err == nil {
		iss.Commission = old.Commission // claimed and received only through the commission actions
		if old.Symbol != iss.Symbol || old.Type != iss.Type {
			if bs, _ := o.issueBids(ctx, tenant, iss.ID); len(bs) > 0 {
				return iss, invalid("the symbol and type cannot change once clients have bid")
			}
		}
	} else {
		return iss, fmt.Errorf("%w: no issue %s", ErrNotFound, iss.ID)
	}
	iss.Source, iss.By, iss.Updated = firstOf(source, "Staff"), actor, now.Format(time.RFC3339)
	iss.Status, iss.Timeline = "", IPOTimeline{}
	if err := docs.Upsert(ctx, o.Docs, tenant, cIPOIssue, iss.ID, iss, actor); err != nil {
		return iss, err
	}
	o.log(ctx, tenant, actor, "ipo.issue_saved", iss.ID, map[string]any{"type": iss.Type, "open": iss.Open, "close": iss.Close, "band": iss.PriceLow.String() + "-" + iss.PriceHigh.String(), "lot": iss.LotSize})
	return o.IPOIssueByID(ctx, tenant, iss.ID)
}

// IPOIssueByID returns one issue with its status and timeline.
func (o *Ops) IPOIssueByID(ctx context.Context, tenant, id string) (IPOIssue, error) {
	iss, _, err := docs.Get[IPOIssue](ctx, o.Docs, tenant, cIPOIssue, id)
	if errors.Is(err, docs.ErrNotFound) {
		return iss, fmt.Errorf("%w: no issue %s", ErrNotFound, id)
	}
	if err != nil {
		return iss, err
	}
	set, _ := o.Settings(ctx, tenant)
	bs, _ := o.issueBids(ctx, tenant, id)
	o.fillIssue(&iss, set, bs)
	return iss, nil
}

func (o *Ops) fillIssue(iss *IPOIssue, set Settings, bs []IPOApp) {
	iss.defaults()
	iss.Timeline = iss.timeline(set)
	allotted := false
	for _, b := range bs {
		allotted = allotted || b.AllotDate != "" || b.Status == BidAllotted || b.Status == BidNotAllotted
	}
	iss.Status = iss.status(o.now(), allotted)
}

// IPOIssues lists every issue, open and upcoming first.
func (o *Ops) IPOIssues(ctx context.Context, tenant string) ([]IPOIssue, error) {
	is, err := docs.List[IPOIssue](ctx, o.Docs, tenant, cIPOIssue, "")
	if err != nil {
		return nil, err
	}
	set, _ := o.Settings(ctx, tenant)
	all, _ := docs.List[IPOApp](ctx, o.Docs, tenant, cIPO, "")
	by := map[string][]IPOApp{}
	for _, a := range all {
		if a.IssueID != "" {
			by[a.IssueID] = append(by[a.IssueID], a)
		}
	}
	for i := range is {
		o.fillIssue(&is[i], set, by[is[i].ID])
	}
	rank := map[string]int{"Open": 0, "Upcoming": 1, "Closed": 2, "Allotment": 3, "Listed": 4, "Withdrawn": 5}
	sort.SliceStable(is, func(i, j int) bool {
		if rank[is[i].Status] != rank[is[j].Status] {
			return rank[is[i].Status] < rank[is[j].Status]
		}
		return is[i].Close > is[j].Close
	})
	return is, nil
}

// issueBids are the Ledgerline bids of an issue.
func (o *Ops) issueBids(ctx context.Context, tenant, issueID string) ([]IPOApp, error) {
	all, err := docs.List[IPOApp](ctx, o.Docs, tenant, cIPO, "")
	if err != nil {
		return nil, err
	}
	var out []IPOApp
	for _, a := range all {
		if a.IssueID == issueID {
			a.normalise()
			out = append(out, a)
		}
	}
	return out, nil
}

// ImportIPOIssues reads issues from a CSV (one issue a line): Symbol, Name,
// Type, Exchanges (NSE BSE), Open, Close, PriceLow, PriceHigh, LotSize and
// optionally ISIN, Registrar, CommissionPerApp, CommissionPct. Categories
// take the type's defaults, to adjust on the screen.
func (o *Ops) ImportIPOIssues(ctx context.Context, tenant, actor string, data []byte) ([]IPOIssue, error) {
	if err := o.require(ctx, tenant, actor, "Setting up public issues", RoleMaker, RoleChecker); err != nil {
		return nil, err
	}
	rows, perrs := parseCSV(data, []string{"Symbol", "Name", "Type", "Exchanges", "Open", "Close", "PriceLow", "PriceHigh", "LotSize"})
	if len(perrs) > 0 {
		return nil, invalid("line %d: %s", perrs[0].Line, perrs[0].Msg)
	}
	var out []IPOIssue
	for _, r := range rows {
		lot, _ := parseInt(r.get("LotSize"))
		iss := IPOIssue{Symbol: r.get("Symbol"), Name: r.get("Name"), Type: firstOf(r.get("Type"), IssueMainboard), Exchanges: strings.FieldsFunc(r.get("Exchanges"), func(c rune) bool { return c == ' ' || c == ';' || c == '|' || c == '/' }),
			Open: r.get("Open"), Close: r.get("Close"), PriceLow: dec(r.get("PriceLow")), PriceHigh: dec(r.get("PriceHigh")), LotSize: lot, ISIN: r.get("ISIN"), Registrar: r.get("Registrar"), CommPct: dec(r.get("CommissionPct"))}
		if v := r.get("CommissionPerApp"); v != "" {
			a, err := money.ParseAmount(v)
			if err != nil {
				return out, invalid("line %d: CommissionPerApp must be rupees", r.line)
			}
			iss.CommPerApp = a
		}
		saved, err := o.saveIPOIssue(ctx, tenant, actor, iss, "Imported")
		if err != nil {
			return out, fmt.Errorf("line %d: %w", r.line, err)
		}
		out = append(out, saved)
	}
	return out, nil
}

func parseInt(s string) (int64, error) {
	var n int64
	_, err := fmt.Sscanf(strings.TrimSpace(s), "%d", &n)
	return n, err
}

// ---- small helpers ----

func ipoMax(a, b money.Amount) money.Amount {
	if a > b {
		return a
	}
	return b
}

// ipoAccount codes.
const (
	PayableOFSMargin      = "PAYABLE:OFS_MARGIN" // client money held as offer-for-sale margin
	ReceivableIPOComm     = "RECEIVABLE:IPO_COMMISSION"
	IncomeIPOComm         = "INCOME:IPO_COMMISSION"
	ReceivableTDS         = "RECEIVABLE:TDS_194H"
	ExpenseIPOCompensated = "EXPENSE:IPO_COMPENSATION"
)
