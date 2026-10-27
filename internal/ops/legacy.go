package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// ---- legacy debts: old client debits kept apart from the daily work ----
//
// A firm moving to Ledgerline often brings client debits that have not
// moved for months: small dues on dead accounts, old disputes. Charging
// them interest only grows numbers nobody will pay, and chasing them every
// day in the debit queues hides the debits that matter. A legacy debt is
// such a debit, marked by one person and approved by another (or marked in
// the opening balances at go-live). While it is open:
//
//   - no interest is charged on it;
//   - it is left out of debit reminders and the debit-ageing work, and
//     shown on its own screen as money still owed;
//   - the client stays blocked for buying and payouts, as any client in
//     debit, and an inactive account cannot be reactivated until it is
//     paid or written off;
//   - recovery is recorded against it: reminders, calls, balance
//     confirmations, notices, part payments, and write-offs (posted to bad
//     debts by a checker).
//
// It closes by itself when the client's balance is no longer in debit.

const (
	cLegacy         = "legacydebt"
	ExpenseBadDebts = "EXPENSE:BAD_DEBTS"
)

// LegacyNoteKinds are the recovery steps a firm records.
var LegacyNoteKinds = []string{"Reminder sent", "Call", "Balance confirmed", "Part payment promised", "Legal notice", "Arbitration filed", "Settlement agreed", "Note"}

// LegacyDebt is one client's legacy debt.
type LegacyDebt struct {
	UCC        string       `json:"ucc"`
	Name       string       `json:"name"`
	Status     string       `json:"status"` // Proposed, Legacy, Rejected, Unmarked
	Amount     money.Amount `json:"amount"` // the debit when it was marked
	DebitSince string       `json:"debit_since,omitempty"`
	Reason     string       `json:"reason"`
	ProposedBy string       `json:"proposed_by"`
	ProposedOn string       `json:"proposed_on"`
	DecidedBy  string       `json:"decided_by,omitempty"`
	DecidedOn  string       `json:"decided_on,omitempty"`
	WrittenOff money.Amount `json:"written_off"`
	Notes      []LegacyNote `json:"notes"`
}

// LegacyNote is one recovery step or decision.
type LegacyNote struct {
	Date   string       `json:"date"`
	Kind   string       `json:"kind"`
	Text   string       `json:"text"`
	By     string       `json:"by"`
	Amount money.Amount `json:"amount,omitempty"` // write-offs
	Entry  string       `json:"entry,omitempty"`
}

// LegacyRow is a legacy debt as the screen shows it on a date.
type LegacyRow struct {
	LegacyDebt
	State      string       `json:"state"`     // Proposed, Open, Cleared, Rejected, Unmarked
	Debit      money.Amount `json:"debit"`     // owed now
	Recovered  money.Amount `json:"recovered"` // paid since marking (not counting write-offs)
	LastMove   string       `json:"last_move,omitempty"`
	LastAck    string       `json:"last_ack,omitempty"` // last payment or balance confirmation
	ThreeYears string       `json:"three_years,omitempty"`
}

// LegacyView is the legacy debts screen.
type LegacyView struct {
	Date       string       `json:"date"`
	Rows       []LegacyRow  `json:"rows"`
	Open       int          `json:"open"`
	Owed       money.Amount `json:"owed"`
	Recovered  money.Amount `json:"recovered"`
	WrittenOff money.Amount `json:"written_off"`
	Proposed   int          `json:"proposed"`
	Bands      []LegacyBand `json:"bands"`
	Kinds      []string     `json:"note_kinds"`
}

// LegacyBand counts open legacy debts by size.
type LegacyBand struct {
	Label  string       `json:"label"`
	Count  int          `json:"count"`
	Amount money.Amount `json:"amount"`
}

var legacyBands = []struct {
	label string
	upTo  money.Amount
}{{"Under ₹1,000", money.MustAmount("1000")}, {"₹1,000 – 10,000", money.MustAmount("10000")}, {"₹10,000 – 1 lakh", money.MustAmount("100000")}, {"Over ₹1 lakh", 0}}

// legacyOpen is the set of clients whose legacy debt is approved and still
// in debit on date: the clients the daily debit work leaves alone.
func (o *Ops) legacyOpen(ctx context.Context, tenant string) map[string]bool {
	ls, _ := docs.List[LegacyDebt](ctx, o.Docs, tenant, cLegacy, "")
	out := map[string]bool{}
	for _, l := range ls {
		if l.Status == "Legacy" {
			out[l.UCC] = true
		}
	}
	return out
}

// LegacyDebts lists the register with each debt's position on date.
func (o *Ops) LegacyDebts(ctx context.Context, tenant, date string) (LegacyView, error) {
	if !validDate(date) {
		return LegacyView{}, invalid("give the date (YYYY-MM-DD)")
	}
	v := LegacyView{Date: date, Rows: []LegacyRow{}, Kinds: LegacyNoteKinds}
	ls, err := docs.List[LegacyDebt](ctx, o.Docs, tenant, cLegacy, "")
	if err != nil {
		return v, err
	}
	moves, err := o.clientMoves(ctx, tenant, date, nil)
	if err != nil {
		return v, err
	}
	for _, b := range legacyBands {
		v.Bands = append(v.Bands, LegacyBand{Label: b.label})
	}
	for _, l := range ls {
		r := LegacyRow{LegacyDebt: l}
		if r.Notes == nil {
			r.Notes = []LegacyNote{}
		}
		var bal money.Amount
		for _, m := range moves[l.UCC] {
			bal += m.Amount
			r.LastMove = m.Date
			if m.Amount > 0 && m.Kind != "WRITEOFF" && m.Date > r.LastAck {
				r.LastAck = m.Date
			}
		}
		for _, n := range l.Notes {
			if n.Kind == "Balance confirmed" && n.Date > r.LastAck {
				r.LastAck = n.Date
			}
		}
		if bal < 0 {
			r.Debit = -bal
		}
		if rec := l.Amount - r.Debit - l.WrittenOff; rec > 0 {
			r.Recovered = rec
		}
		if from := firstOf(r.LastAck, l.DebitSince); from != "" {
			r.ThreeYears = addMonths(from, 36)
		}
		switch {
		case l.Status == "Legacy" && r.Debit == 0:
			r.State = "Cleared"
		case l.Status == "Legacy":
			r.State = "Open"
		default:
			r.State = l.Status
		}
		switch r.State {
		case "Open":
			v.Open++
			v.Owed += r.Debit
			for i, b := range legacyBands {
				if b.upTo == 0 || r.Debit < b.upTo {
					v.Bands[i].Count++
					v.Bands[i].Amount += r.Debit
					break
				}
			}
		case "Proposed":
			v.Proposed++
		}
		if l.Status == "Legacy" {
			v.Recovered += r.Recovered
			v.WrittenOff += l.WrittenOff
		}
		v.Rows = append(v.Rows, r)
	}
	order := map[string]int{"Proposed": 0, "Open": 1, "Cleared": 2, "Rejected": 3, "Unmarked": 4}
	sort.SliceStable(v.Rows, func(i, j int) bool {
		if order[v.Rows[i].State] != order[v.Rows[j].State] {
			return order[v.Rows[i].State] < order[v.Rows[j].State]
		}
		return v.Rows[i].Debit > v.Rows[j].Debit
	})
	return v, nil
}

// LegacyCandidate is a client debit that has not moved for a long time.
type LegacyCandidate struct {
	UCC      string       `json:"ucc"`
	Name     string       `json:"name"`
	Debit    money.Amount `json:"debit"`
	Since    string       `json:"since"`
	LastMove string       `json:"last_move"`
	Idle     int          `json:"idle_days"`
	Status   string       `json:"client_status"`
	Inactive bool         `json:"inactive"`
}

// FrozenDebits lists clients in debit whose balance has not moved for at
// least days: the likely legacy debts. Those already in the register are
// left out.
func (o *Ops) FrozenDebits(ctx context.Context, tenant, date string, days int) ([]LegacyCandidate, error) {
	if !validDate(date) {
		return nil, invalid("give the date (YYYY-MM-DD)")
	}
	if days < 30 {
		return nil, invalid("a debit must be idle for at least 30 days to count as legacy")
	}
	cs, err := o.ClientMap(ctx, tenant)
	if err != nil {
		return nil, err
	}
	moves, err := o.clientMoves(ctx, tenant, date, nil)
	if err != nil {
		return nil, err
	}
	ls, _ := docs.List[LegacyDebt](ctx, o.Docs, tenant, cLegacy, "")
	have := map[string]bool{}
	for _, l := range ls {
		if l.Status == "Proposed" || l.Status == "Legacy" {
			have[l.UCC] = true
		}
	}
	out := []LegacyCandidate{}
	for _, u := range sortedKeys(moves) {
		ms := moves[u]
		var bal money.Amount
		for _, m := range ms {
			bal += m.Amount
		}
		if bal >= 0 || have[u] || len(ms) == 0 {
			continue
		}
		last := ms[len(ms)-1].Date
		idle := daysBetween(last, date)
		if idle < days {
			continue
		}
		c := cs[u]
		out = append(out, LegacyCandidate{UCC: u, Name: c.Name, Debit: -bal, Since: debitSince(ms, date), LastMove: last, Idle: idle, Status: c.Status, Inactive: c.Inactive})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Debit > out[j].Debit })
	return out, nil
}

// debitSince is the first day of the current debit spell.
func debitSince(ms []clientMove, date string) string {
	if len(ms) == 0 {
		return ""
	}
	bals := dailyBalances(ms, ms[0].Date, date)
	i := len(bals) - 1
	for i > 0 && bals[i-1] < 0 {
		i--
	}
	return addDays(ms[0].Date, i)
}

// ProposeLegacy puts clients in debit forward as legacy debts; a different
// person approves them.
func (o *Ops) ProposeLegacy(ctx context.Context, tenant, actor, date string, uccs []string, reason string) (int, error) {
	if err := o.require(ctx, tenant, actor, "Proposing legacy debts", RoleMaker, RoleChecker, RoleRisk, RoleCompliance); err != nil {
		return 0, err
	}
	return o.proposeLegacy(ctx, tenant, actor, date, uccs, reason)
}

func (o *Ops) proposeLegacy(ctx context.Context, tenant, actor, date string, uccs []string, reason string) (int, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return 0, invalid("say why these debits are legacy (for example: no movement since before go-live, account dormant)")
	}
	if !validDate(date) {
		return 0, invalid("give the date (YYYY-MM-DD)")
	}
	if len(uccs) == 0 {
		return 0, invalid("choose the clients")
	}
	cs, err := o.ClientMap(ctx, tenant)
	if err != nil {
		return 0, err
	}
	moves, err := o.clientMoves(ctx, tenant, date, nil)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, u := range uccs {
		u = strings.ToUpper(strings.TrimSpace(u))
		c, ok := cs[u]
		if !ok {
			return n, fmt.Errorf("%w: client %s", ErrNotFound, u)
		}
		var bal money.Amount
		for _, m := range moves[u] {
			bal += m.Amount
		}
		if bal >= 0 {
			return n, invalid("%s is not in debit on %s", u, date)
		}
		old, _, err := docs.Get[LegacyDebt](ctx, o.Docs, tenant, cLegacy, u)
		if err == nil && (old.Status == "Proposed" || old.Status == "Legacy") {
			continue
		}
		l := LegacyDebt{UCC: u, Name: c.Name, Status: "Proposed", Amount: -bal, DebitSince: debitSince(moves[u], date), Reason: reason, ProposedBy: actor, ProposedOn: date,
			Notes: append(old.Notes, LegacyNote{Date: date, Kind: "Proposed", Text: reason, By: actor})}
		if l.Notes == nil {
			l.Notes = []LegacyNote{}
		}
		if err := docs.Upsert(ctx, o.Docs, tenant, cLegacy, u, l, actor); err != nil {
			return n, err
		}
		n++
	}
	if n > 0 {
		o.log(ctx, tenant, actor, "legacy.proposed", date, map[string]any{"clients": n, "reason": reason})
		o.raiseWork(ctx, tenant, "legacy", "proposed", fmt.Sprintf("Approve %d proposed legacy debts", n), "Debits put forward as legacy wait for a second person on Debtors → Legacy debts.", "", RoleChecker, "legacy", date)
	}
	return n, nil
}

// DecideLegacy approves or rejects proposed legacy debts. The person who
// proposed one cannot approve it.
func (o *Ops) DecideLegacy(ctx context.Context, tenant, actor, date string, uccs []string, approve bool, note string) (int, error) {
	if err := o.require(ctx, tenant, actor, "Approving legacy debts", RoleChecker, RoleCompliance); err != nil {
		return 0, err
	}
	if !approve && strings.TrimSpace(note) == "" {
		return 0, invalid("give a reason for rejecting")
	}
	n := 0
	for _, u := range uccs {
		u = strings.ToUpper(strings.TrimSpace(u))
		_, err := docs.Update(ctx, o.Docs, tenant, cLegacy, u, actor, func(l *LegacyDebt) error {
			if l.Status != "Proposed" {
				return fmt.Errorf("%w: %s is %s, not proposed", ErrState, u, strings.ToLower(l.Status))
			}
			if l.ProposedBy == actor {
				return fmt.Errorf("%w: the person who proposed %s as a legacy debt cannot approve it", ErrForbidden, u)
			}
			l.Status, l.DecidedBy, l.DecidedOn = "Rejected", actor, date
			kind := "Rejected"
			if approve {
				l.Status, kind = "Legacy", "Approved"
			}
			l.Notes = append(l.Notes, LegacyNote{Date: date, Kind: kind, Text: strings.TrimSpace(note), By: actor})
			return nil
		})
		if err != nil {
			return n, err
		}
		n++
	}
	verb := "rejected"
	if approve {
		verb = "approved"
	}
	o.log(ctx, tenant, actor, "legacy."+verb, date, map[string]any{"clients": n})
	if v, err := o.LegacyDebts(ctx, tenant, date); err == nil && v.Proposed == 0 {
		o.closeWork(ctx, tenant, actor, "legacy", "proposed", "decided")
	}
	return n, nil
}

// UnmarkLegacy returns a legacy debt to the daily debit work (interest,
// reminders and ageing apply again from the next run).
func (o *Ops) UnmarkLegacy(ctx context.Context, tenant, actor, date, ucc, reason string) (LegacyDebt, error) {
	if err := o.require(ctx, tenant, actor, "Unmarking a legacy debt", RoleChecker, RoleCompliance); err != nil {
		return LegacyDebt{}, err
	}
	if strings.TrimSpace(reason) == "" {
		return LegacyDebt{}, invalid("say why it goes back to the daily debit work")
	}
	l, err := docs.Update(ctx, o.Docs, tenant, cLegacy, strings.ToUpper(ucc), actor, func(l *LegacyDebt) error {
		if l.Status != "Legacy" {
			return fmt.Errorf("%w: only an approved legacy debt can be unmarked", ErrState)
		}
		l.Status = "Unmarked"
		l.Notes = append(l.Notes, LegacyNote{Date: date, Kind: "Unmarked", Text: strings.TrimSpace(reason), By: actor})
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "legacy.unmarked", l.UCC, map[string]any{"reason": reason})
	}
	return l, err
}

// NoteLegacy records a recovery step.
func (o *Ops) NoteLegacy(ctx context.Context, tenant, actor, date, ucc, kind, text string) (LegacyDebt, error) {
	if err := o.require(ctx, tenant, actor, "Recording recovery of a legacy debt", RoleMaker, RoleChecker, RoleRisk, RoleCompliance); err != nil {
		return LegacyDebt{}, err
	}
	ok := false
	for _, k := range LegacyNoteKinds {
		ok = ok || k == kind
	}
	if !ok {
		return LegacyDebt{}, invalid("kind must be one of: %s", strings.Join(LegacyNoteKinds, ", "))
	}
	if !validDate(date) {
		return LegacyDebt{}, invalid("give the date (YYYY-MM-DD)")
	}
	if strings.TrimSpace(text) == "" && kind != "Reminder sent" {
		return LegacyDebt{}, invalid("say what happened")
	}
	return docs.Update(ctx, o.Docs, tenant, cLegacy, strings.ToUpper(ucc), actor, func(l *LegacyDebt) error {
		if l.Status != "Legacy" {
			return fmt.Errorf("%w: %s is not an open legacy debt", ErrState, l.UCC)
		}
		l.Notes = append(l.Notes, LegacyNote{Date: date, Kind: kind, Text: strings.TrimSpace(text), By: actor})
		return nil
	})
}

// RemindLegacy sends each client a reminder of the old debit (the
// "legacy_debt.reminder" message) and records it.
func (o *Ops) RemindLegacy(ctx context.Context, tenant, actor, date string, uccs []string) (int, error) {
	if err := o.require(ctx, tenant, actor, "Sending legacy debt reminders", RoleMaker, RoleChecker, RoleRisk, RoleCompliance); err != nil {
		return 0, err
	}
	v, err := o.LegacyDebts(ctx, tenant, date)
	if err != nil {
		return 0, err
	}
	want := map[string]bool{}
	for _, u := range uccs {
		want[strings.ToUpper(strings.TrimSpace(u))] = true
	}
	n := 0
	for _, r := range v.Rows {
		if r.State != "Open" || (len(want) > 0 && !want[r.UCC]) {
			continue
		}
		o.notify(ctx, tenant, actor, "legacy_debt.reminder", r.UCC, map[string]string{"debit": formatINR(r.Debit.Dec()), "since": fmtDay(firstOf(r.DebitSince, date))})
		if _, err := docs.Update(ctx, o.Docs, tenant, cLegacy, r.UCC, actor, func(l *LegacyDebt) error {
			l.Notes = append(l.Notes, LegacyNote{Date: date, Kind: "Reminder sent", By: actor})
			return nil
		}); err != nil {
			return n, err
		}
		n++
	}
	o.log(ctx, tenant, actor, "legacy.reminded", date, map[string]any{"clients": n})
	return n, nil
}

// WriteOffLegacy writes off part or all of a legacy debt: the client is
// credited and bad debts debited, on the date given. A checker does it,
// with the reason; the period lock applies.
func (o *Ops) WriteOffLegacy(ctx context.Context, tenant, actor, date, ucc string, amount money.Amount, reason string) (LegacyDebt, error) {
	if err := o.require(ctx, tenant, actor, "Writing off a debt", RoleChecker); err != nil {
		return LegacyDebt{}, err
	}
	if strings.TrimSpace(reason) == "" {
		return LegacyDebt{}, invalid("give the reason (for example: not recoverable, approved by the directors on …)")
	}
	if err := o.checkLock(ctx, tenant, date); err != nil {
		return LegacyDebt{}, err
	}
	ucc = strings.ToUpper(ucc)
	v, err := o.LegacyDebts(ctx, tenant, date)
	if err != nil {
		return LegacyDebt{}, err
	}
	var row *LegacyRow
	for i := range v.Rows {
		if v.Rows[i].UCC == ucc {
			row = &v.Rows[i]
		}
	}
	if row == nil || row.State != "Open" {
		return LegacyDebt{}, fmt.Errorf("%w: %s has no open legacy debt", ErrState, ucc)
	}
	if amount <= 0 || amount > row.Debit {
		return LegacyDebt{}, invalid("the amount must be above zero and at most the ₹%s owed", formatINR(row.Debit.Dec()))
	}
	n := len(row.Notes)
	e, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: tenant, Key: fmt.Sprintf("WO:%s:%d", ucc, n), ValueDate: date,
		Narration: "Debit balance written off: " + strings.TrimSpace(reason), Source: ledger.SourceRef{Kind: "WRITEOFF", ID: ucc}, CreatedBy: actor,
		Postings: []ledger.Posting{{Account: ExpenseBadDebts, Debit: amount, Memo: "Bad debt written off"}, {Account: ledger.ClientAccount(ucc), Credit: amount, Memo: "Written off"}}})
	if err != nil {
		return LegacyDebt{}, err
	}
	l, err := docs.Update(ctx, o.Docs, tenant, cLegacy, ucc, actor, func(l *LegacyDebt) error {
		l.WrittenOff += amount
		l.Notes = append(l.Notes, LegacyNote{Date: date, Kind: "Written off", Text: strings.TrimSpace(reason), By: actor, Amount: amount, Entry: e.ID})
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "legacy.written_off", ucc, map[string]any{"amount": amount.String(), "entry": e.ID, "reason": reason})
	}
	return l, err
}

// legacyBlocksReactivation explains why an inactive account with an open
// legacy debt cannot be reactivated yet.
func (o *Ops) legacyBlocksReactivation(ctx context.Context, tenant, ucc, date string) error {
	l, _, err := docs.Get[LegacyDebt](ctx, o.Docs, tenant, cLegacy, ucc)
	if err != nil || l.Status != "Legacy" {
		return nil
	}
	moves, err := o.clientMoves(ctx, tenant, date, nil)
	if err != nil {
		return err
	}
	var bal money.Amount
	for _, m := range moves[ucc] {
		bal += m.Amount
	}
	if bal >= 0 {
		return nil
	}
	return fmt.Errorf("%w: %s still owes the old debit of ₹%s (Debtors → Legacy debts): it must be paid, or written off by a checker, before the account is reactivated", ErrState, ucc, formatINR((-bal).Dec()))
}
