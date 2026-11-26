package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// ---- DIS controls: books, slips and the checks before a slip moves ----
//
// The rules the DP follows for delivery instruction slips, from SEBI's
// Master Circular for Depositories (SEBI/HO/MRD/MRD-PoD-1/P/CIR/2024/168,
// MC) and CDSL's Operating Instructions for DPs (DPOI):
//
//   - A new book is issued only against the requisition slip of the
//     previous book, unless the book was lost (MC 1.12.4, DPOI 6.5.1); on
//     a written report of loss the unused slips of the book are cancelled
//     (MC 1.12.3). A book for an account with no transaction for 12 months
//     goes only to the registered address, with the compliance officer's
//     authorisation (MC 1.12.4).
//   - Loose slips: at most 10 per account per financial year, given to the
//     holder in person, who signs the register (MC 1.12.5, DPOI 6.5.2).
//   - Accounts with a POA or DDPI get slips of a separate series
//     (appendix DP-0204).
//   - Slips of an old book are not accepted one month after a new book is
//     issued (appendix DP-0038).
//   - Before a slip is executed the holder is called on the registered
//     number, the call recorded and approved by the compliance officer,
//     and the call noted on the slip, when (a) the account had no
//     transaction for 12 months and any security is transferred (MC
//     1.12.8), (b) the account was not operated for 6 months and all its
//     balances are transferred (DPOI 6.5.4.9), or (c) an active account
//     transfers 5 or more ISINs in full (MC 1.12.8).
//   - A slip above ₹5 lakh is verified by a senior official and
//     countersigned by another employee (DPOI 6.5.4.5-6.5.4.7). Ledgerline
//     reads this as: two people besides the maker, at least one of them a
//     senior official (the firm's list, or a compliance officer or Admin).
//   - Cut-offs for a delivery to the clearing member: by 4 pm (physical)
//     or 6 pm (electronic) on the day before the pay-in, and 11 am for an
//     early pay-in on the same day. A slip received later is still taken
//     on a best-effort basis and marked so (MC Annexure 21; appendix
//     DP-0125/DP-0206). The gap report notes that CDSL's T+1 wording
//     cannot work with a 10:30 am pay-in, so the T-day times are used.
//   - An inter-depository slip may be dated at most 10 days ahead
//     (appendix DP-0216; calendar days, the stricter reading).
//
// Firm-specific facts (who the senior officials are, from when Ledgerline
// holds the DP's transaction history) are settings on the DP controls
// panel, never hard-coded.

const (
	cDPCtl = "dpctl" // "config", "stock|<n>", "count|<n>", "recon|<date>", "tick"

	disLooseLimit    = 10 // loose slips per account per financial year (MC 1.12.5)
	disDormantMonths = 12 // MC 1.12.4 and 1.12.8
	disIdleMonths    = 6  // DPOI 6.5.4.9: not operated for 6 months, all balances out
	disManyISINs     = 5  // MC 1.12.8: 5 or more ISINs transferred in full
	disOldBookDays   = 30 // DP-0038: one month after a new book
	disInterDepDays  = 10 // DP-0216
	disSeriesDDPI    = "POA/DDPI"
	disDDPIFrom      = int64(5000001) // first slip of the POA/DDPI series
)

// DPControls are the firm's settings for the DP controls.
type DPControls struct {
	// SeniorOfficials may verify or countersign slips above ₹5 lakh as the
	// senior official; empty: compliance officers and Admins.
	SeniorOfficials []string `json:"senior_officials,omitempty"`
	// TxnHistoryFrom is the day from which Ledgerline holds every demat
	// transaction of the DP's accounts (CDSL's transaction files). An
	// account with no transaction in Ledgerline is judged dormant only
	// from then on; before it is set, only accounts with a transaction on
	// record are judged.
	TxnHistoryFrom string `json:"txn_history_from,omitempty"`
	// StockCountDays is how often the blank slip stock is counted.
	StockCountDays int `json:"stock_count_days,omitempty"`
	// FreezeModes are the ways the firm offers clients to freeze their
	// online trading (at least two: NSE/INSP/73976 Exhibit 33);
	// FreezePolicy where the policy is published.
	FreezeModes  []string `json:"freeze_modes,omitempty"`
	FreezePolicy string   `json:"freeze_policy,omitempty"`
	By           string   `json:"by,omitempty"`
	At           string   `json:"at,omitempty"`
}

func (c DPControls) stockDays() int {
	if c.StockCountDays > 0 {
		return c.StockCountDays
	}
	return 30
}

// DPControlsConfig is the firm's DP controls settings.
func (o *Ops) DPControlsConfig(ctx context.Context, tenant string) DPControls {
	c, _, _ := docs.GetOr(ctx, o.Docs, tenant, cDPCtl, "config", DPControls{})
	return c
}

// SaveDPControls stores the settings (compliance officer).
func (o *Ops) SaveDPControls(ctx context.Context, tenant, actor string, c DPControls) (DPControls, error) {
	if err := o.require(ctx, tenant, actor, "Setting the DP controls", RoleCompliance); err != nil {
		return c, err
	}
	if c.TxnHistoryFrom != "" && !validDate(c.TxnHistoryFrom) {
		return c, invalid("give the date the transaction history starts (YYYY-MM-DD)")
	}
	if c.StockCountDays < 0 || c.StockCountDays > 366 {
		return c, invalid("count the slip stock every 1 to 366 days")
	}
	clean := func(xs []string) []string {
		var out []string
		for _, x := range xs {
			if x = strings.TrimSpace(x); x != "" && !contains(out, x) {
				out = append(out, x)
			}
		}
		return out
	}
	c.SeniorOfficials, c.FreezeModes = clean(c.SeniorOfficials), clean(c.FreezeModes)
	c.FreezePolicy = strings.TrimSpace(c.FreezePolicy)
	c.By, c.At = actor, o.now().Format(time.RFC3339)
	if err := docs.Upsert(ctx, o.Docs, tenant, cDPCtl, "config", c, actor); err != nil {
		return c, err
	}
	o.log(ctx, tenant, actor, "dpctl.settings", "config", map[string]any{"senior_officials": c.SeniorOfficials, "txn_history_from": c.TxnHistoryFrom, "freeze_modes": c.FreezeModes})
	return c, nil
}

// holdsRole says whether a user holds a role (Admin holds every role).
func (o *Ops) holdsRole(ctx context.Context, tenant, user, role string) bool {
	a, err := o.Access(ctx, tenant, user)
	if err != nil {
		return false
	}
	return a.Admin || contains(a.Legacy, role)
}

// seniorOfficial says whether a user may sign as the senior official on a
// slip above ₹5 lakh.
func (o *Ops) seniorOfficial(ctx context.Context, tenant, user string) bool {
	if c := o.DPControlsConfig(ctx, tenant); len(c.SeniorOfficials) > 0 {
		return contains(c.SeniorOfficials, user)
	}
	return o.holdsRole(ctx, tenant, user, RoleCompliance)
}

// ---- the account's last transaction ----

// dpLastTxn is the last demat transaction of one of the client's accounts
// on or before day, and whether Ledgerline can tell (see TxnHistoryFrom).
// An account with none counts from when it was opened, or from the start
// of the history if later.
func (o *Ops) dpLastTxn(ctx context.Context, tenant string, c Client, b BOAccount, day string) (string, bool) {
	ts, _ := o.DPTransactions(ctx, tenant, c.UCC)
	acct := holdingAcct(c, b.BOID)
	last := ""
	for _, t := range ts {
		if t.Date > day || t.Pledge != "" || holdingAcct(c, t.BOID) != acct {
			continue
		}
		if t.Date > last {
			last = t.Date
		}
	}
	if last != "" {
		return last, true
	}
	from := o.DPControlsConfig(ctx, tenant).TxnHistoryFrom
	if from == "" || from > day {
		return "", false
	}
	if b.Opened > from {
		return b.Opened, true
	}
	return from, true
}

// dpDormant says whether the account had no transaction for months before
// day ("" when it had one, or Ledgerline cannot tell: known false).
func (o *Ops) dpDormant(ctx context.Context, tenant string, c Client, b BOAccount, day string, months int) (since string, known bool) {
	last, ok := o.dpLastTxn(ctx, tenant, c, b, day)
	if !ok {
		return "", false
	}
	if last <= addMonths(day, -months) {
		return last, true
	}
	return "", true
}

// ---- books ----

// DISBookRequest is a book (or loose slips) to issue.
type DISBookRequest struct {
	UCC   string `json:"ucc"`
	BOID  string `json:"bo_id"`
	Slips int64  `json:"slips"`
	// Requisition is the requisition slip of the previous book the holder
	// handed in; LossRef the written report of its loss (then the unused
	// slips of the earlier books are cancelled).
	Requisition int64  `json:"requisition,omitempty"`
	LossRef     string `json:"loss_ref,omitempty"`
	// Delivery: In person, Registered address (by post or courier), or
	// Collected by authorised person.
	Delivery string `json:"delivery,omitempty"`
	// Loose slips are given to the holder in person, who signs the
	// register (Register: the register's entry).
	Loose    bool   `json:"loose,omitempty"`
	Register string `json:"register,omitempty"`
}

// DISDeliveries are the ways a book reaches the holder.
var DISDeliveries = []string{"In person", "Registered address", "Collected by authorised person"}

// fyStart is the 1 April that starts the financial year of day.
func fyStart(day string) string {
	if day[5:] >= "04-01" {
		return day[:4] + "-04-01"
	}
	var y int
	fmt.Sscanf(day[:4], "%d", &y)
	return fmt.Sprintf("%04d-04-01", y-1)
}

// accountBooks are the books issued for one of the client's accounts,
// oldest first.
func (o *Ops) accountBooks(ctx context.Context, tenant string, c Client, boid string) []DISBook {
	books, _ := docs.List[DISBook](ctx, o.Docs, tenant, cDISBook, "")
	var out []DISBook
	for _, b := range books {
		if b.UCC == c.UCC && (b.BOID == "" || b.BOID == normDematID(boid)) {
			out = append(out, b)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].From < out[j].From })
	return out
}

// IssueDISBookWith issues a book of slips under the rules above.
func (o *Ops) IssueDISBookWith(ctx context.Context, tenant, actor string, req DISBookRequest) (DISBook, error) {
	if err := o.require(ctx, tenant, actor, "Issuing DIS books", RoleMaker, RoleChecker); err != nil {
		return DISBook{}, err
	}
	c, err := o.Client(ctx, tenant, req.UCC)
	if err != nil {
		return DISBook{}, err
	}
	bo := o.boFor(ctx, tenant, c, req.BOID)
	if bo.Status != "Active" {
		return DISBook{}, fmt.Errorf("%w: the demat account is %s", ErrState, strings.ToLower(bo.Status))
	}
	n := req.Slips
	if n < 1 || n > 100 {
		return DISBook{}, invalid("a book has 1 to 100 slips")
	}
	today := o.now().In(ist).Format("2006-01-02")
	req.Delivery = strings.TrimSpace(req.Delivery)
	if req.Delivery != "" && !contains(DISDeliveries, req.Delivery) {
		return DISBook{}, invalid("delivery must be one of %s", strings.Join(DISDeliveries, ", "))
	}
	prev := o.accountBooks(ctx, tenant, c, bo.BOID)
	var regular []DISBook
	loose := int64(0)
	for _, b := range prev {
		if b.Loose {
			if b.Issued >= fyStart(today) {
				loose += b.To - b.From + 1
			}
			continue
		}
		regular = append(regular, b)
	}
	book := DISBook{UCC: c.UCC, BOID: normDematID(bo.BOID), Issued: today, Used: []int64{}, Lost: []int64{}, Delivery: req.Delivery}
	if req.Loose {
		// MC 1.12.5 / DPOI 6.5.2: in person, signed for, 10 a year.
		if loose+n > disLooseLimit {
			return DISBook{}, invalid("only %d loose slips a financial year per account: %d already given since %s", disLooseLimit, loose, fmtDay(fyStart(today)))
		}
		if strings.TrimSpace(req.Register) == "" {
			return DISBook{}, invalid("loose slips are given to the holder in person, after the signature is verified: give the register entry the holder signed")
		}
		book.Loose, book.Register, book.Delivery = true, strings.TrimSpace(req.Register), "In person"
	} else if len(regular) > 0 {
		// MC 1.12.4 / DPOI 6.5.1: against the requisition slip of the
		// previous book, or a written report of its loss.
		last := regular[len(regular)-1]
		switch {
		case strings.TrimSpace(req.LossRef) != "":
			book.LossRef = strings.TrimSpace(req.LossRef)
		case req.Requisition != 0:
			if req.Requisition < last.From || req.Requisition > last.To {
				return DISBook{}, invalid("requisition slip %d is not in the last book issued (%d to %d)", req.Requisition, last.From, last.To)
			}
			if contains(i64s(last.Used), fmt.Sprint(req.Requisition)) || contains(i64s(last.Lost), fmt.Sprint(req.Requisition)) {
				return DISBook{}, invalid("requisition slip %d was already used or cancelled", req.Requisition)
			}
			book.Requisition = req.Requisition
		default:
			return DISBook{}, invalid("a new book is issued only against the requisition slip of the previous book (%d to %d), or the written report that it was lost", last.From, last.To)
		}
	}
	// MC 1.12.4: an account with no transaction for 12 months gets a book
	// only at its registered address, with the compliance officer's
	// authorisation (AuthoriseDISBook).
	if !book.Loose {
		if since, _ := o.dpDormant(ctx, tenant, c, bo, today, disDormantMonths); since != "" {
			if book.Delivery != "Registered address" {
				return DISBook{}, invalid("the account had no transaction since %s: the book goes only to the registered address (choose that delivery); the compliance officer then authorises it", fmtDay(since))
			}
			book.AwaitingCO = "no transaction since " + fmtDay(since)
		}
	}
	// A separate series for accounts with a POA or DDPI (DP-0204).
	if bo.DDPI {
		book.Series = disSeriesDDPI
	}
	books, _ := docs.List[DISBook](ctx, o.Docs, tenant, cDISBook, "")
	next := int64(1000001)
	if book.Series == disSeriesDDPI {
		next = disDDPIFrom
	}
	for _, b := range books {
		if b.Series == book.Series && b.To >= next {
			next = b.To + 1
		}
	}
	if book.Series == "" && next+n-1 >= disDDPIFrom {
		return DISBook{}, invalid("the regular slip numbers have reached the POA/DDPI series; ask Ledgerline support")
	}
	seq, err := o.next(ctx, tenant, "disbook", actor)
	if err != nil {
		return DISBook{}, err
	}
	book.ID, book.From, book.To = fmt.Sprintf("DB-%05d", seq), next, next+n-1
	if err := o.disStockTake(ctx, tenant, book.Series, n); err != nil {
		return DISBook{}, err
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cDISBook, book.ID, book, actor); err != nil {
		return book, err
	}
	// The requisition slip is used up; a lost book's unused slips are
	// cancelled so they cannot be presented (MC 1.12.3).
	if book.Requisition != 0 {
		last := regular[len(regular)-1]
		_, _ = docs.Update(ctx, o.Docs, tenant, cDISBook, last.ID, actor, func(x *DISBook) error { x.Used = append(x.Used, book.Requisition); return nil })
	}
	if book.LossRef != "" {
		for _, b := range regular {
			if _, err := o.cancelUnused(ctx, tenant, actor, b.ID, "lost: "+book.LossRef); err != nil {
				return book, err
			}
		}
	}
	o.log(ctx, tenant, actor, "dis.book_issued", book.ID, map[string]any{"ucc": c.UCC, "from": book.From, "to": book.To, "series": book.Series, "loose": book.Loose, "requisition": book.Requisition, "loss_ref": book.LossRef, "delivery": book.Delivery, "awaiting_co": book.AwaitingCO})
	return book, nil
}

// cancelUnused cancels every slip of a book not used yet (marks it lost).
func (o *Ops) cancelUnused(ctx context.Context, tenant, actor, id, why string) (DISBook, error) {
	b, err := docs.Update(ctx, o.Docs, tenant, cDISBook, id, actor, func(x *DISBook) error {
		used, lost := map[int64]bool{}, map[int64]bool{}
		for _, s := range x.Used {
			used[s] = true
		}
		for _, s := range x.Lost {
			lost[s] = true
		}
		for s := x.From; s <= x.To; s++ {
			if !used[s] && !lost[s] {
				x.Lost = append(x.Lost, s)
			}
		}
		x.Cancelled = why
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "dis.book_cancelled", id, map[string]any{"why": why})
	}
	return b, err
}

// ReportDISBookLost cancels the unused slips of a book on the holder's
// written report of its loss or theft (MC 1.12.3).
func (o *Ops) ReportDISBookLost(ctx context.Context, tenant, actor, id, ref string) (DISBook, error) {
	if err := o.require(ctx, tenant, actor, "Issuing DIS books", RoleMaker, RoleChecker); err != nil {
		return DISBook{}, err
	}
	if strings.TrimSpace(ref) == "" {
		return DISBook{}, invalid("give the reference of the holder's written report of the loss")
	}
	return o.cancelUnused(ctx, tenant, actor, id, "lost: "+strings.TrimSpace(ref))
}

// AuthoriseDISBook is the compliance officer's authorisation of a book for
// a dormant account, sent to the registered address (MC 1.12.4).
func (o *Ops) AuthoriseDISBook(ctx context.Context, tenant, actor, id, note string) (DISBook, error) {
	if err := o.require(ctx, tenant, actor, "Authorising DIS books for dormant accounts", RoleCompliance); err != nil {
		return DISBook{}, err
	}
	b, err := docs.Update(ctx, o.Docs, tenant, cDISBook, id, actor, func(x *DISBook) error {
		if x.AwaitingCO == "" {
			return fmt.Errorf("%w: %s does not need the compliance officer's authorisation", ErrState, id)
		}
		x.AuthorisedBy, x.AuthorisedAt, x.AuthNote = actor, o.now().Format(time.RFC3339), strings.TrimSpace(note)
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "dis.book_authorised", id, map[string]any{"note": note})
	}
	return b, err
}

// RecordDISWaiver records the holder's written waiver of a DIS book at
// account opening (appendix DP-0153: a book at opening, or a waiver).
func (o *Ops) RecordDISWaiver(ctx context.Context, tenant, actor, ucc, boid, ref string) (BOAccount, error) {
	if err := o.require(ctx, tenant, actor, "Issuing DIS books", RoleMaker, RoleChecker); err != nil {
		return BOAccount{}, err
	}
	if strings.TrimSpace(ref) == "" {
		return BOAccount{}, invalid("give the reference of the holder's signed waiver")
	}
	c, err := o.Client(ctx, tenant, ucc)
	if err != nil {
		return BOAccount{}, err
	}
	b := o.boForRecord(ctx, tenant, c, boid)
	if b.BOID == "" {
		return b, fmt.Errorf("%w: %s has no demat account with the DP", ErrState, c.UCC)
	}
	b.DISWaiver = strings.TrimSpace(ref) + " (" + o.now().In(ist).Format("2006-01-02") + ")"
	if err := o.putBO(ctx, tenant, actor, b); err != nil {
		return b, err
	}
	o.log(ctx, tenant, actor, "dis.waiver", b.BOID, map[string]any{"ucc": c.UCC, "ref": ref})
	return b, nil
}

// ---- slips: checks when a slip is entered ----

// DISCall is the call to the holder before a slip is executed.
type DISCall struct {
	Ref      string `json:"ref"`    // the recording's reference
	Number   string `json:"number"` // the registered number called (masked)
	At       string `json:"at"`     // when (RFC3339)
	By       string `json:"by"`
	Note     string `json:"note,omitempty"`
	Approved string `json:"approved_by,omitempty"` // the compliance officer
	ApprovAt string `json:"approved_at,omitempty"`
}

// disCutoff says when a delivery to the clearing member was due for its
// execution date: 11:00 the same day for an early pay-in (executed on the
// day received), else 16:00 (physical) or 18:00 (electronic) on the
// business day before.
func disCutoff(set Settings, d DIS) (day, hm string) {
	if d.ExecDate == d.Received {
		return d.Received, "11:00"
	}
	day = set.PrevBusinessDay(d.ExecDate)
	if d.Mode == "Electronic" {
		return day, "18:00"
	}
	return day, "16:00"
}

// disChecks are the checks of a slip being entered (EnterDIS), after the
// slip register and free balance: the book's state, the old-book rule,
// the date limits, the cut-off, and whether the holder must be called.
func (o *Ops) disChecks(ctx context.Context, tenant string, c Client, b BOAccount, book DISBook, d *DIS) error {
	if err := o.forClosureCheck(ctx, tenant, c, b, *d); err != nil {
		return err
	}
	if book.AwaitingCO != "" && book.AuthorisedBy == "" {
		return fmt.Errorf("%w: book %s waits for the compliance officer's authorisation (%s)", ErrState, book.ID, book.AwaitingCO)
	}
	if book.Cancelled != "" && contains(i64s(book.Lost), fmt.Sprint(d.Slip)) {
		return invalid("slip %d was cancelled (%s)", d.Slip, book.Cancelled)
	}
	// DP-0038: a newer book for the account makes the old one's slips
	// unusable a month after it was issued.
	if !book.Loose {
		for _, nb := range o.accountBooks(ctx, tenant, c, b.BOID) {
			if !nb.Loose && nb.From > book.From && nb.Series == book.Series && addDays(nb.Issued, disOldBookDays) < d.Received {
				return invalid("slip %d is from book %s, replaced by book %s on %s: slips of the old book are not accepted a month after the new one", d.Slip, book.ID, nb.ID, fmtDay(nb.Issued))
			}
		}
	}
	if d.Kind == "Inter-depository" && d.ExecDate > addDays(d.Received, disInterDepDays) {
		return invalid("an inter-depository slip may be dated at most %d days ahead (by %s)", disInterDepDays, fmtDay(addDays(d.Received, disInterDepDays)))
	}
	d.Mode = firstOf(d.Mode, "Physical")
	if d.Mode != "Physical" && d.Mode != "Electronic" {
		return invalid("mode must be Physical or Electronic")
	}
	now := o.now().In(ist)
	if d.ReceivedAt == "" && d.Received == now.Format("2006-01-02") {
		d.ReceivedAt = now.Format("15:04")
	}
	if d.ReceivedAt != "" {
		if _, err := time.Parse("15:04", d.ReceivedAt); err != nil {
			return invalid("give the time the slip was received as HH:MM")
		}
	}
	if d.Kind == "On-market" {
		set, _ := o.Settings(ctx, tenant)
		day, hm := disCutoff(set, *d)
		if d.Received > day || (d.Received == day && d.ReceivedAt > hm) {
			d.BestEffort = fmt.Sprintf("received after the cut-off (%s %s): taken on a best-effort basis", fmtDay(day), hm)
		}
	}
	// The call to the holder (MC 1.12.8; DPOI 6.5.4.9).
	d.CallNeeded = nil
	if since, known := o.dpDormant(ctx, tenant, c, b, d.Received, disDormantMonths); since != "" {
		d.CallNeeded = append(d.CallNeeded, "no transaction since "+fmtDay(since)+" (12 months)")
	} else if !known {
		d.Note = strings.TrimSpace(d.Note + " Ledgerline has no transaction history for this account: check in CDAS whether it is dormant before verifying.")
	}
	full, all := o.disFullTransfers(ctx, tenant, c, b, *d)
	if idle, _ := o.dpDormant(ctx, tenant, c, b, d.Received, disIdleMonths); idle != "" && all && len(d.CallNeeded) == 0 {
		d.CallNeeded = append(d.CallNeeded, "not operated since "+fmtDay(idle)+" (6 months) and all balances transferred")
	}
	if full >= disManyISINs {
		d.CallNeeded = append(d.CallNeeded, fmt.Sprintf("%d ISINs transferred in full", full))
	}
	return nil
}

// disFullTransfers counts the ISINs the slip, with the account's other
// slips received the same day and not yet executed, moves in full, and
// says whether together they move every balance of the account.
func (o *Ops) disFullTransfers(ctx context.Context, tenant string, c Client, b BOAccount, d DIS) (int, bool) {
	acct := holdingAcct(c, b.BOID)
	out := map[string]money.Dec{}
	add := func(x DIS) {
		for _, l := range x.Lines {
			out[l.ISIN] = out[l.ISIN].Add(l.Qty)
		}
	}
	add(d)
	ds, _ := docs.List[DIS](ctx, o.Docs, tenant, cDIS, "")
	for _, x := range ds {
		if x.UCC == c.UCC && holdingAcct(c, x.BOID) == acct && x.Received == d.Received && (x.Status == "Entered" || x.Status == "Verified" || x.Status == "Uploaded") {
			add(x)
		}
	}
	hs, _ := o.Holdings(ctx, tenant, c.UCC)
	full, held := 0, 0
	all := true
	for _, h := range hs {
		if h.BOID != acct || h.Qty.Sign() <= 0 {
			continue
		}
		held++
		if q, ok := out[h.ISIN]; ok && q.Cmp(h.Qty) >= 0 {
			full++
		} else {
			all = false
		}
	}
	return full, all && held > 0
}

// RecordDISCall records the call to the holder on the registered number
// (maker or checker); the compliance officer then approves it.
func (o *Ops) RecordDISCall(ctx context.Context, tenant, actor, id string, call DISCall) (DIS, error) {
	if err := o.require(ctx, tenant, actor, "Recording the call to the holder", RoleMaker, RoleChecker); err != nil {
		return DIS{}, err
	}
	if strings.TrimSpace(call.Ref) == "" {
		return DIS{}, invalid("give the reference of the call recording")
	}
	d, err := docs.Update(ctx, o.Docs, tenant, cDIS, id, actor, func(x *DIS) error {
		if x.Status != "Entered" {
			return fmt.Errorf("%w: %s is %s", ErrState, id, strings.ToLower(x.Status))
		}
		if len(x.CallNeeded) == 0 {
			return fmt.Errorf("%w: %s does not need a call", ErrState, id)
		}
		c, _ := o.Client(ctx, tenant, x.UCC)
		number := firstOf(strings.TrimSpace(call.Number), c.Mobile)
		if c.Mobile != "" && call.Number != "" && digitsOnly(call.Number) != digitsOnly(c.Mobile) {
			return invalid("the call must be to the registered number (ending %s)", lastN(c.Mobile, 4))
		}
		x.Call = &DISCall{Ref: strings.TrimSpace(call.Ref), Number: maskMobile(number), At: firstOf(call.At, o.now().Format(time.RFC3339)), By: actor, Note: strings.TrimSpace(call.Note)}
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "dis.call", id, map[string]any{"ref": call.Ref})
	}
	return d, err
}

// ApproveDISCall is the compliance officer's approval of the call.
func (o *Ops) ApproveDISCall(ctx context.Context, tenant, actor, id string) (DIS, error) {
	if err := o.require(ctx, tenant, actor, "Approving the call to the holder", RoleCompliance); err != nil {
		return DIS{}, err
	}
	d, err := docs.Update(ctx, o.Docs, tenant, cDIS, id, actor, func(x *DIS) error {
		if x.Call == nil {
			return fmt.Errorf("%w: record the call first", ErrState)
		}
		if x.Maker == actor {
			return fmt.Errorf("%w: the person who entered the slip cannot approve its call", ErrForbidden)
		}
		x.Call.Approved, x.Call.ApprovAt = actor, o.now().Format(time.RFC3339)
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "dis.call_approved", id, nil)
	}
	return d, err
}

// CountersignDIS is the second signature on a verified slip above ₹5 lakh
// (DPOI 6.5.4.5-6.5.4.7): someone other than the maker and the verifier;
// if the verifier was not a senior official, the countersigner must be.
func (o *Ops) CountersignDIS(ctx context.Context, tenant, actor, id string) (DIS, error) {
	if err := o.require(ctx, tenant, actor, "Countersigning high-value slips", RoleChecker, RoleCompliance); err != nil {
		return DIS{}, err
	}
	senior := o.seniorOfficial(ctx, tenant, actor)
	d, err := docs.Update(ctx, o.Docs, tenant, cDIS, id, actor, func(x *DIS) error {
		switch {
		case x.Status != "Verified":
			return fmt.Errorf("%w: %s is %s; only a verified slip is countersigned", ErrState, id, strings.ToLower(x.Status))
		case x.Value.Cmp(HighValueDIS) <= 0:
			return fmt.Errorf("%w: %s is not above ₹5 lakh", ErrState, id)
		case x.Countersigned != "":
			return fmt.Errorf("%w: %s is already countersigned", ErrState, id)
		case actor == x.Maker || actor == x.Checker:
			return fmt.Errorf("%w: the countersignature is by another employee than the maker and the verifier", ErrForbidden)
		case !x.SeniorVerified && !senior:
			return fmt.Errorf("%w: the verifier was not a senior official, so a senior official countersigns", ErrForbidden)
		}
		x.Countersigned, x.CountersignedAt = actor, o.now().Format(time.RFC3339)
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "dis.countersigned", id, nil)
	}
	return d, err
}

// disReady says why a verified slip may not go in an upload batch yet
// ("" when it may).
func (d DIS) disReady() string {
	if d.Value.Cmp(HighValueDIS) > 0 && d.Countersigned == "" {
		return "above ₹5 lakh: waits for the countersignature"
	}
	return ""
}

// DISVerify is a checker's verification of a slip.
type DISVerify struct {
	Approve      bool   `json:"approve"`
	Note         string `json:"note"`
	Confirmation string `json:"confirmation"`
	// AllSigned: every holder signed and the signatures match (DPOI
	// 6.5.4.5); needed for a joint account operated jointly.
	AllSigned bool `json:"all_signed"`
}

// verifyChecks are the checks VerifyDIS makes before approving: the call
// to the holder approved, and every holder's signature on a joint
// account operated jointly.
func (o *Ops) verifyChecks(ctx context.Context, tenant string, d DIS, v DISVerify) error {
	if len(d.CallNeeded) > 0 && (d.Call == nil || d.Call.Approved == "") {
		return fmt.Errorf("%w: %s needs a recorded call to the holder on the registered number, approved by the compliance officer (%s)", ErrState, d.ID, strings.Join(d.CallNeeded, "; "))
	}
	c, _ := o.Client(ctx, tenant, d.UCC)
	if c.UCC == "" {
		return nil
	}
	b := o.boForRecord(ctx, tenant, c, d.BOID)
	if len(b.JointHolders) > 0 && b.operation() == JointlyOp && !v.AllSigned {
		return invalid("the account is operated jointly: confirm that every holder signed the slip and the signatures match")
	}
	return nil
}

// digitsOnly keeps the digits of a number, and the last 10 of them.
func digitsOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return lastN(b.String(), 10)
}

func lastN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// ---- joint accounts: mode of operation ----

// Modes of operation of a joint account (appendix DP-0045/DP-0160).
const (
	JointlyOp  = "Jointly"
	AnyoneOp   = "Anyone or survivor"
	opDefault  = JointlyOp
	opNoJoints = ""
)

// operation is the account's mode of operation: Jointly unless all
// holders asked for another.
func (b BOAccount) operation() string {
	if len(b.JointHolders) == 0 {
		return opNoJoints
	}
	return firstOf(b.Operation, opDefault)
}

// SetJointOperation changes a joint account's mode of operation. Moving
// away from Jointly needs every holder's signed request; going back to
// Jointly is done on any one holder's request (the safe reading of
// DP-0045/DP-0160: no holder can be left out of a decision by the
// others).
func (o *Ops) SetJointOperation(ctx context.Context, tenant, actor, ucc, boid, mode string, holders int, ref string) (BOAccount, error) {
	if err := o.require(ctx, tenant, actor, "Changing a joint account's mode of operation", RoleChecker); err != nil {
		return BOAccount{}, err
	}
	c, err := o.Client(ctx, tenant, ucc)
	if err != nil {
		return BOAccount{}, err
	}
	b := o.boForRecord(ctx, tenant, c, boid)
	if len(b.JointHolders) == 0 {
		return b, fmt.Errorf("%w: the account has one holder", ErrState)
	}
	if mode != JointlyOp && mode != AnyoneOp {
		return b, invalid("the mode is %s or %s", JointlyOp, AnyoneOp)
	}
	if strings.TrimSpace(ref) == "" {
		return b, invalid("give the reference of the signed request")
	}
	all := len(b.JointHolders) + 1
	if mode == AnyoneOp && holders < all {
		return b, invalid("all %d holders must sign the request to change from Jointly", all)
	}
	if holders < 1 {
		return b, invalid("at least one holder signs the request")
	}
	was := b.operation()
	b.Operation = mode
	if err := o.putBO(ctx, tenant, actor, b); err != nil {
		return b, err
	}
	o.log(ctx, tenant, actor, "dp.joint_operation", b.BOID, map[string]any{"from": was, "to": mode, "holders": holders, "ref": ref})
	return b, nil
}

// badSlipWork raises a work item to tell the holder at once that a slip
// used, cancelled or not issued for the account was presented on it
// (DPOI 6.5.5).
func (o *Ops) badSlipWork(ctx context.Context, tenant string, c Client, b BOAccount, slip int64, why string) {
	today := o.now().In(ist).Format("2006-01-02")
	o.raiseWork(ctx, tenant, wDPCtl, fmt.Sprintf("badslip:%s:%d:%s", c.UCC, slip, today), "Tell "+c.UCC+" a slip "+why+" was presented",
		fmt.Sprintf("Slip %d (%s) was presented for a transfer from the demat account %s of %s and refused. Tell the holder at once (CDSL DPOI 6.5.5), and record how you told them in the work item's note.", slip, why, cdslMaskBO(b.BOID), c.Name), c.UCC, RoleMaker, "dp", today)
}
