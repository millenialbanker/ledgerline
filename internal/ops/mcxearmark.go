package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// ---- MCX money in one ledger: the MCX-only bank account earmark ----
//
// A firm that keeps one ledger per client (the separate commodity ledger
// is off) may still collect MCX money in a bank account of its own
// (Settings.MCXOnlyBankAccounts). Money paid into such an account is for
// MCX only: it must never count for an equity limit. The accounts stay one
// ledger, CLIENT:<ucc>, and the client sees one ledger; what changes is a
// memo figure computed from it, the client's MCX earmark (mcxEarmarks):
//
//	+ receipts posted from an MCX-only bank account (bank statement and
//	  e-CMS credits, gateway payments, receipts entered by hand, recorded
//	  as Earmark "MCX" when they come in)
//	± MCX bills and charges (bills, carried futures MTM, expiry and its
//	  brokerage of MCX / COM): profits add, losses and charges take from
//	  it first
//	− payouts made from MCX money (from the MCX-only bank account)
//	± RMS moves between MCX and equity money (MCXMove, maker-checker)
//
// The running figure never goes below zero at the end of a day (an MCX
// loss larger than the earmark is borne by the client's other money, and
// is not owed back by later MCX receipts), and the earmark is at most the
// client's ledger credit: it can never exceed what the client has. Equity
// money is the ledger less the earmark.
//
// The readers that keep equity and commodity apart with a separate
// commodity ledger (segFunds: Risk, live risk, margin collection, NSE
// Clearing allocation and margin reporting, MCX allocation and margin
// reporting, SEGMON, Finspot and its pay-out hold lines, payouts, stock
// SIP) read equity money and MCX money the same way here. Everything the
// client sees (statements, portal, messages, running account settlement,
// debit ageing, interest) is the one ledger, unchanged.
//
// With no MCX-only account set, or with the separate commodity ledger on
// (where the account's mapping to the commodity ledger does this), nothing
// here changes anything.

const (
	cMCXMove = "mcx_earmark_moves"
	// EarmarkMCX marks money in or out that is MCX money.
	EarmarkMCX = "MCX"
)

// isCommoditySegment reports whether trades of an exchange segment are MCX.
func isCommoditySegment(exchange, segment string) bool {
	return strings.EqualFold(segment, "COM") || strings.EqualFold(exchange, "MCX")
}

// mcxEarmark reports whether MCX money is earmarked in one ledger: an
// MCX-only bank account is set and the separate commodity ledger is off.
func (s Settings) mcxEarmark() bool { return !s.sepCom() && len(s.MCXOnlyBankAccounts) > 0 }

// segApart reports whether equity and commodity money are kept apart for
// limits, by separate ledgers or by the earmark.
func (s Settings) segApart() bool { return s.sepCom() || s.mcxEarmark() }

// viewLedger is the side (EQ or COM) whose money covers an exchange
// segment's margin, for the readers that keep them apart.
func (s Settings) viewLedger(exchange, segment string) string {
	if s.segApart() && isCommoditySegment(exchange, segment) {
		return ledger.LedgerCommodity
	}
	return ledger.LedgerEquity
}

// mcxOnly reports whether money paid into (or out of) the firm's bank
// account acct is MCX money.
func (s Settings) mcxOnly(acct string) bool {
	if !s.mcxEarmark() || normAcctNo(acct) == "" {
		return false
	}
	for _, a := range s.MCXOnlyBankAccounts {
		if normAcctNo(a) == normAcctNo(acct) {
			return true
		}
	}
	return false
}

// mcxPayoutAccount is the firm account MCX money is paid out from: the
// first MCX-only account.
func (s Settings) mcxPayoutAccount() string {
	if len(s.MCXOnlyBankAccounts) == 0 {
		return ""
	}
	return s.MCXOnlyBankAccounts[0]
}

// checkMCXAccounts tidies the MCX-only bank accounts. They are for one
// ledger; and the last one cannot be removed while clients have MCX money
// earmarked, which would then count for equity again.
func (o *Ops) checkMCXAccounts(ctx context.Context, tenant string, s *Settings) error {
	var out []string
	seen := map[string]bool{}
	for _, a := range s.MCXOnlyBankAccounts {
		a = strings.ReplaceAll(strings.TrimSpace(a), " ", "")
		if n := normAcctNo(a); n != "" && !seen[n] {
			seen[n] = true
			out = append(out, a)
		}
	}
	s.MCXOnlyBankAccounts = out
	if len(out) > 0 && s.SeparateCommodityLedger {
		return invalid("MCX-only bank accounts are for one ledger per client; with the separate commodity ledger on, put the account under \"money paid into these firm bank accounts\" as Commodity instead")
	}
	prev, _ := o.Settings(ctx, tenant)
	if prev.mcxEarmark() && !s.mcxEarmark() {
		em, err := o.mcxEarmarks(ctx, tenant, "", "")
		if err != nil {
			return err
		}
		var left []string
		for _, u := range sortedKeys(em) {
			if em[u].MCX > 0 {
				left = append(left, u)
			}
		}
		if len(left) > 0 {
			if len(left) > 5 {
				left = append(left[:5], fmt.Sprintf("and %d more", len(left)-5))
			}
			return invalid("clients have MCX money earmarked (%s): it would count for equity limits again; move it to equity on RMS first", strings.Join(left, ", "))
		}
	}
	return nil
}

// ---- the earmark ----

// MCXEarmark is a client's money split into MCX money and equity money.
type MCXEarmark struct {
	Ledger money.Amount `json:"ledger"` // the client's one ledger, credit positive
	MCX    money.Amount `json:"mcx"`    // MCX money (earmarked)
	Equity money.Amount `json:"equity"` // the ledger less the earmark
	Raw    money.Amount `json:"raw"`    // the running MCX figure before the cap at the ledger credit
}

// MCXMove is an RMS move of a client's money between MCX and equity:
// requested by one person and approved by another; it counts from Date.
type MCXMove struct {
	ID        string       `json:"id"`
	Date      string       `json:"date"`
	UCC       string       `json:"ucc"`
	Name      string       `json:"name"`
	To        string       `json:"to"` // MCX or EQ
	Amount    money.Amount `json:"amount"`
	Note      string       `json:"note,omitempty"`
	Maker     string       `json:"maker"`
	Checker   string       `json:"checker,omitempty"`
	Status    string       `json:"status"` // Pending, Approved, Rejected
	Reason    string       `json:"reason,omitempty"`
	CreatedAt string       `json:"created_at"`
	DecidedAt string       `json:"decided_at,omitempty"`
	// AfterSeq is the last ledger entry posted when the move was approved:
	// on its date it counts after that entry and before any later one.
	AfterSeq int64 `json:"after_seq,omitempty"`
}

// after is where the move falls among its day's entries (0: at the end).
func (m MCXMove) after() int64 {
	if m.AfterSeq <= 0 {
		return 1<<62 - 1
	}
	return m.AfterSeq
}

// mcxSegmentEntry reports whether an entry is an MCX bill or charge: a
// bill, carried futures MTM, option expiry or expiry brokerage whose
// source names an MCX or COM segment (date/ucc/EXCH-SEG).
func mcxSegmentEntry(e ledger.Entry) bool {
	switch e.Source.Kind {
	case "BILL", "MTM", "EXPIRY", "EXPBRK":
	default:
		return false
	}
	i := strings.LastIndex(e.Source.ID, "/")
	exch, seg, _ := strings.Cut(e.Source.ID[i+1:], "-")
	return isCommoditySegment(exch, seg)
}

// computeEarmarks is the earmark of every client from the ledger's
// entries up to the date wanted (es), the entries that are MCX money in or
// out (mcxEntries: receipts into and payouts from an MCX-only account), and
// the approved RMS moves up to the date. With excludeBillsOn set, the
// bills of that date are left out of both the ledger and the earmark, as
// clientLedgerFunds does. It is the one computation of the earmark.
func computeEarmarks(es []ledger.Entry, mcxEntries map[string]bool, moves []MCXMove, excludeBillsOn string) map[string]MCXEarmark {
	es = append([]ledger.Entry(nil), es...)
	sort.SliceStable(es, func(i, j int) bool {
		if es[i].ValueDate != es[j].ValueDate {
			return es[i].ValueDate < es[j].ValueDate
		}
		return es[i].Seq < es[j].Seq
	})
	byDay := map[string][]MCXMove{}
	days := map[string]bool{}
	for _, e := range es {
		days[e.ValueDate] = true
	}
	for _, m := range moves {
		if m.Status == "Approved" {
			byDay[m.Date] = append(byDay[m.Date], m)
			days[m.Date] = true
		}
	}
	order := sortedKeys(days)
	bal := map[string]money.Amount{}
	raw := map[string]money.Amount{}
	isMCX := map[string]bool{} // entry -> MCX money (for its reversals)
	i := 0
	for _, d := range order {
		delta := map[string]money.Amount{}
		ms := byDay[d]
		sort.SliceStable(ms, func(a, b int) bool { return ms[a].after() < ms[b].after() })
		// apply makes the moves approved before entry seq (all that are
		// left when seq is -1) take effect.
		apply := func(seq int64) {
			for len(ms) > 0 && (seq < 0 || ms[0].after() < seq) {
				m := ms[0]
				ms = ms[1:]
				if m.To == EarmarkMCX {
					delta[m.UCC] += m.Amount
					continue
				}
				// A move to equity takes from the earmark as it stands (at
				// most the ledger credit), so it always frees what it says.
				cur := min(max(raw[m.UCC]+delta[m.UCC], 0), max(bal[m.UCC], 0))
				raw[m.UCC], delta[m.UCC] = max(cur-m.Amount, 0), 0
			}
		}
		for ; i < len(es) && es[i].ValueDate == d; i++ {
			e := es[i]
			apply(e.Seq)
			if excludeBillsOn != "" && e.ValueDate == excludeBillsOn && (e.Source.Kind == "BILL" || e.Source.Kind == "REVERSAL") {
				continue
			}
			mcx := mcxEntries[e.ID] || mcxSegmentEntry(e) || (e.ReversalOf != "" && isMCX[e.ReversalOf])
			if mcx {
				isMCX[e.ID] = true
			}
			for _, p := range e.Postings {
				u, _, ok := ledger.ClientCode(p.Account)
				if !ok {
					continue
				}
				bal[u] += p.Credit - p.Debit
				if mcx {
					delta[u] += p.Credit - p.Debit
				}
			}
		}
		apply(-1)
		for u, a := range delta {
			raw[u] = max(raw[u]+a, 0)
		}
	}
	out := make(map[string]MCXEarmark, len(bal))
	for u, b := range bal {
		e := min(raw[u], max(b, 0))
		out[u] = MCXEarmark{Ledger: b, MCX: e, Equity: b - e, Raw: raw[u]}
	}
	return out
}

// mcxEntries are the ledger entries that are MCX money in or out: credits
// and receipts into an MCX-only bank account, payouts of MCX money.
func (o *Ops) mcxEntries(ctx context.Context, tenant string) (map[string]bool, error) {
	out := map[string]bool{}
	ts, err := docs.List[BankTxn](ctx, o.Docs, tenant, cBankTxn, "")
	if err != nil {
		return nil, err
	}
	for _, t := range ts {
		if t.Earmark == EarmarkMCX && t.EntryID != "" && (t.Status == "Matched" || t.Status == "Identified") {
			out[t.EntryID] = true
		}
	}
	rs, err := docs.List[Receipt](ctx, o.Docs, tenant, cReceipt, "")
	if err != nil {
		return nil, err
	}
	for _, r := range rs {
		if r.Earmark == EarmarkMCX && r.Entry != "" {
			out[r.Entry] = true
		}
	}
	ps, err := docs.List[PayoutRequest](ctx, o.Docs, tenant, cPayout, "")
	if err != nil {
		return nil, err
	}
	for _, p := range ps {
		if p.Earmark == EarmarkMCX && p.Entry != "" {
			out[p.Entry] = true
		}
	}
	return out, nil
}

// mcxEarmarks is every client's earmark as of asOf (empty: all entries),
// leaving out the bills of excludeBillsOn when set.
func (o *Ops) mcxEarmarks(ctx context.Context, tenant, asOf, excludeBillsOn string) (map[string]MCXEarmark, error) {
	es, err := o.App.Ledger.Store.List(ctx, tenant, ledger.Filter{To: asOf})
	if err != nil {
		return nil, err
	}
	ids, err := o.mcxEntries(ctx, tenant)
	if err != nil {
		return nil, err
	}
	ms, err := docs.List[MCXMove](ctx, o.Docs, tenant, cMCXMove, "")
	if err != nil {
		return nil, err
	}
	var moves []MCXMove
	for _, m := range ms {
		if asOf == "" || m.Date <= asOf {
			moves = append(moves, m)
		}
	}
	return computeEarmarks(es, ids, moves, excludeBillsOn), nil
}

// segFunds is each client's money as equity and commodity, for the
// readers that keep them apart (Settings.segApart): with a separate
// commodity ledger, the ledgers (clientLedgerFunds); with the MCX earmark,
// the one ledger less the earmark as equity and the earmark as commodity;
// otherwise the one ledger as equity.
func (o *Ops) segFunds(ctx context.Context, tenant string, set Settings, asOf, excludeBillsOn string) (map[string]LedgerFunds, error) {
	if !set.mcxEarmark() {
		return o.clientLedgerFunds(ctx, tenant, asOf, excludeBillsOn)
	}
	em, err := o.mcxEarmarks(ctx, tenant, asOf, excludeBillsOn)
	if err != nil {
		return nil, err
	}
	out := make(map[string]LedgerFunds, len(em))
	for u, e := range em {
		out[u] = LedgerFunds{Equity: e.Equity, Commodity: e.MCX, hasEquity: true}
	}
	return out, nil
}

// limitFunds is equityFunds for the equity front end's limits (Finspot,
// stock SIP): with the MCX earmark, the one ledger less MCX money.
func (o *Ops) limitFunds(ctx context.Context, tenant string, set Settings, asOf, excludeBillsOn string) (map[string]money.Amount, error) {
	if !set.mcxEarmark() {
		return o.equityFunds(ctx, tenant, asOf, excludeBillsOn)
	}
	lf, err := o.segFunds(ctx, tenant, set, asOf, excludeBillsOn)
	if err != nil {
		return nil, err
	}
	out := make(map[string]money.Amount, len(lf))
	for u, f := range lf {
		out[u] = f.Equity
	}
	return out, nil
}

// ---- RMS moves between MCX and equity money ----

// RequestMCXMove asks to move a client's money between MCX money and
// equity money (to "MCX" or "EQ"). Nothing is posted: it changes only
// which side of the one ledger the money counts for, once a second person
// approves it (DecideMCXMove).
func (o *Ops) RequestMCXMove(ctx context.Context, tenant, actor, ucc, to, date string, amount money.Amount, note string) (MCXMove, error) {
	if err := o.require(ctx, tenant, actor, "Moving money between MCX and equity", RoleRisk, RoleMaker, RoleChecker); err != nil {
		return MCXMove{}, err
	}
	set, _ := o.Settings(ctx, tenant)
	if !set.mcxEarmark() {
		return MCXMove{}, invalid("MCX money is earmarked only with an MCX-only bank account and one ledger per client (Settings)")
	}
	to = strings.ToUpper(strings.TrimSpace(to))
	switch to {
	case "MCX", "COM", "COMMODITY":
		to = EarmarkMCX
	case "EQ", "EQUITY":
		to = ledger.LedgerEquity
	default:
		return MCXMove{}, invalid("move to MCX or to equity")
	}
	ucc = strings.ToUpper(strings.TrimSpace(ucc))
	c, err := o.Client(ctx, tenant, ucc)
	if err != nil {
		return MCXMove{}, invalid("client %s not found", ucc)
	}
	if amount <= 0 {
		return MCXMove{}, invalid("amount must be above zero")
	}
	if date == "" {
		date = o.now().Format("2006-01-02")
	}
	if !ledger.ValidDate(date) {
		return MCXMove{}, invalid("date must be YYYY-MM-DD")
	}
	m := MCXMove{Date: date, UCC: c.UCC, Name: c.Name, To: to, Amount: amount, Note: strings.TrimSpace(note), Maker: actor, Status: "Pending", CreatedAt: o.now().Format(time.RFC3339)}
	if err := o.checkMCXMove(ctx, tenant, m); err != nil {
		return MCXMove{}, err
	}
	n, err := o.next(ctx, tenant, "mcx-move", actor)
	if err != nil {
		return MCXMove{}, err
	}
	m.ID = fmt.Sprintf("MV-%06d", n)
	if _, err := docs.Put(ctx, o.Docs, tenant, cMCXMove, m.ID, m, 0, actor); err != nil {
		return MCXMove{}, err
	}
	o.log(ctx, tenant, actor, "mcx.move.requested", m.ID, map[string]any{"ucc": m.UCC, "to": m.To, "amount": m.Amount.String(), "note": m.Note})
	return m, nil
}

// checkMCXMove refuses a move of more than the side it comes from has.
func (o *Ops) checkMCXMove(ctx context.Context, tenant string, m MCXMove) error {
	em, err := o.mcxEarmarks(ctx, tenant, m.Date, "")
	if err != nil {
		return err
	}
	e := em[m.UCC]
	if m.To == EarmarkMCX && m.Amount > max(e.Equity, 0) {
		return invalid("₹%s is more than the client's ₹%s of equity money", formatINR(m.Amount.Dec()), formatINR(max(e.Equity, 0).Dec()))
	}
	if m.To != EarmarkMCX && m.Amount > e.MCX {
		return invalid("₹%s is more than the client's ₹%s of MCX money", formatINR(m.Amount.Dec()), formatINR(e.MCX.Dec()))
	}
	return nil
}

// DecideMCXMove approves or rejects a requested move; the person who
// asked cannot decide it.
func (o *Ops) DecideMCXMove(ctx context.Context, tenant, actor, id string, approve bool, reason string) (MCXMove, error) {
	if err := o.require(ctx, tenant, actor, "Approving a move between MCX and equity", RoleChecker, RoleRisk); err != nil {
		return MCXMove{}, err
	}
	m, _, err := docs.Get[MCXMove](ctx, o.Docs, tenant, cMCXMove, id)
	if err != nil {
		return m, err
	}
	if m.Status != "Pending" {
		return m, fmt.Errorf("%w: move %s is %s", ErrState, id, m.Status)
	}
	if m.Maker == actor {
		return m, fmt.Errorf("%w: the person who asked for a move cannot approve or reject it", ErrForbidden)
	}
	if !approve && strings.TrimSpace(reason) == "" {
		return m, invalid("give the reason for rejecting")
	}
	var last int64
	if approve {
		if err := o.checkMCXMove(ctx, tenant, m); err != nil {
			return m, err
		}
		es, err := o.App.Ledger.Store.List(ctx, tenant, ledger.Filter{})
		if err != nil {
			return m, err
		}
		for _, e := range es {
			last = max(last, e.Seq)
		}
	}
	m, err = docs.Update(ctx, o.Docs, tenant, cMCXMove, id, actor, func(x *MCXMove) error {
		x.Checker, x.Reason, x.DecidedAt = actor, strings.TrimSpace(reason), o.now().Format(time.RFC3339)
		x.Status = "Rejected"
		if approve {
			x.Status, x.AfterSeq = "Approved", last
		}
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "mcx.move."+strings.ToLower(m.Status), id, map[string]any{"ucc": m.UCC, "to": m.To, "amount": m.Amount.String()})
	}
	return m, err
}

// MCXMoves lists moves, newest first: every client's, or one client's.
func (o *Ops) MCXMoves(ctx context.Context, tenant, ucc string) ([]MCXMove, error) {
	ms, err := docs.List[MCXMove](ctx, o.Docs, tenant, cMCXMove, "")
	out := []MCXMove{}
	for _, m := range ms {
		if ucc == "" || m.UCC == strings.ToUpper(ucc) {
			out = append(out, m)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, err
}

// ClientEarmark is the client drawer's MCX money and equity money.
type ClientEarmark struct {
	MCXEarmark
	Accounts []string  `json:"accounts"` // the firm's MCX-only bank accounts
	Moves    []MCXMove `json:"moves"`    // the client's moves, newest first
}

func (o *Ops) clientEarmarkView(ctx context.Context, tenant string, set Settings, ucc, date string) (*ClientEarmark, error) {
	em, err := o.mcxEarmarks(ctx, tenant, date, "")
	if err != nil {
		return nil, err
	}
	ms, _ := o.MCXMoves(ctx, tenant, ucc)
	return &ClientEarmark{MCXEarmark: em[ucc], Accounts: set.MCXOnlyBankAccounts, Moves: ms}, nil
}

// ---- payouts ----

// checkEarmarkPayout refuses a payout of equity money (from the firm's
// usual bank account) that would use MCX money: RMS moves it to equity
// first. A payout of MCX money uses the earmark first, then equity money,
// within what the client may withdraw.
func checkEarmarkPayout(w Withdrawable, amount money.Amount, from string) error {
	if w.Ledgers == nil || from == ledger.LedgerCommodity {
		return nil
	}
	have := max(w.Ledgers.Equity, 0)
	if amount <= have {
		return nil
	}
	return invalid("₹%s is more than the ₹%s of equity money; ₹%s of this client's money is MCX money (paid into the MCX-only bank account). Pay it from MCX money, or ask RMS to move ₹%s to equity first",
		formatINR(amount.Dec()), formatINR(have.Dec()), formatINR(max(w.Ledgers.Commodity, 0).Dec()), formatINR((amount - have).Dec()))
}
