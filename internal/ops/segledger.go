package ops

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// ---- separate equity and commodity ledgers ----
//
// A firm that is only a trading member on a commodity exchange (MCX trades
// cleared by another clearing member and placed on another front end, BOW)
// keeps each client's money in two ledgers, Settings.SeparateCommodityLedger:
//
//	CLIENT:<ucc>        equity (NSE, BSE): Finspot limits, NSE Clearing
//	                    allocation and margin reporting, SEGMON, equity
//	                    settlement, CUSPA, MTF, stock SIP, DP charges,
//	                    interest. Unchanged from the one-ledger books.
//	CLIENTCOM:<ucc>     commodity (MCX): bills of COM trades, BOW funds,
//	                    MCX margin reporting.
//	CLIENTUNALLOC:<ucc> money received with no instruction where it goes;
//	                    in neither front end's limit until Risk allocates it.
//
// The two new prefixes do not start with "CLIENT:", so every existing
// CLIENT: reader sees the equity ledger only; readers that look at the
// client as a whole (statements, debit ageing and interest, running account
// settlement, dormant funds, the segregation check, the balance sheet) read
// all three with ledger.ClientCode. The client always sees one ledger:
// statements merge the three with one running balance and leave out the
// entries that only move money between them (ledger.ClientStatement).
//
// Money in goes to a ledger by the first instruction that applies: the
// firm's bank account it was paid into (BankAccountLedgers), a keyword in
// the narration, the client's default (Client.FundsLedger). Otherwise it
// waits (CLIENTUNALLOC) with an urgent work item for Risk, who allocates
// it to Equity or Commodity on RMS; the allocation is a journal from the
// awaiting account to the chosen ledger. Moving money between a client's
// ledgers is a transfer journal (maker-checker, as journals are). A
// payout from one ledger never takes it negative to pay from the other:
// the shortfall is transferred first when the firm allows it, otherwise
// the payout is refused.
//
// Off (the default), nothing here changes anything: nothing is posted to
// the new accounts and every figure is the one ledger's.

const (
	cFundsAwait    = "funds_awaiting"
	wFundsAlloc    = "fundsalloc"
	srcAllocation  = "ALLOCATION"
	srcLedgerXfer  = "LEDGER-TRANSFER"
	ledgerChoiceEQ = ledger.LedgerEquity
	ledgerChoiceCO = ledger.LedgerCommodity
)

// Default narration keywords (whole words, any case).
var (
	DefaultCommodityKeywords = []string{"MCX", "COMM", "COMMODITY", "COMMODITIES"}
	DefaultEquityKeywords    = []string{"NSE", "BSE", "EQ", "EQUITY", "F&O", "FNO"}
)

// LedgerNames are the client ledgers as people call them.
var LedgerNames = map[string]string{ledger.LedgerEquity: "Equity", ledger.LedgerCommodity: "Commodity", ledger.LedgerAwaiting: "Awaiting allocation"}

// sepCom reports whether the firm keeps a separate commodity ledger.
func (s Settings) sepCom() bool { return s.SeparateCommodityLedger }

func (o *Ops) sepCom(ctx context.Context, tenant string) bool {
	s, _ := o.Settings(ctx, tenant)
	return s.sepCom()
}

// normLedger reads a ledger choice: EQ (also "", EQUITY, CLIENT), COM
// (COMMODITY, MCX). ok is false for anything else.
func normLedger(v string) (string, bool) {
	switch strings.ToUpper(strings.TrimSpace(v)) {
	case "", "EQ", "EQUITY", "CLIENT":
		return ledger.LedgerEquity, true
	case "COM", "COMMODITY", "MCX", "CLIENTCOM":
		return ledger.LedgerCommodity, true
	}
	return "", false
}

// segmentLedger is the client ledger trades of an exchange segment post to.
func (s Settings) segmentLedger(exchange, segment string) string {
	if s.sepCom() && (strings.EqualFold(segment, "COM") || strings.EqualFold(exchange, "MCX")) {
		return ledger.LedgerCommodity
	}
	return ledger.LedgerEquity
}

// clientAccountFor is the client account bills and charges of an exchange
// segment post to: CLIENT:<ucc> unless the firm separates commodity.
func (s Settings) clientAccountFor(ucc, exchange, segment string) string {
	return ledger.ClientLedgerAccount(ucc, s.segmentLedger(exchange, segment))
}

func normAcctNo(a string) string {
	return strings.TrimLeft(strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(a), " ", "")), "0")
}

func normKeywords(ks []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, k := range ks {
		k = strings.ToUpper(strings.TrimSpace(k))
		if k != "" && !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
}

// checkSegLedger validates and tidies the separate-ledger settings. The
// setting cannot be turned off while any client has money in a commodity
// or awaiting ledger: those balances would vanish from every one-ledger
// figure.
func (o *Ops) checkSegLedger(ctx context.Context, tenant string, s *Settings) error {
	if len(s.BankAccountLedgers) > 0 {
		m := map[string]string{}
		for a, v := range s.BankAccountLedgers {
			n := normAcctNo(a)
			if n == "" {
				continue
			}
			switch strings.ToUpper(strings.TrimSpace(v)) {
			case "", "ASK":
				m[n] = "ASK"
			default:
				k, ok := normLedger(v)
				if !ok {
					return invalid("bank account %s: money in goes to Equity, Commodity or Ask", a)
				}
				m[n] = k
			}
		}
		s.BankAccountLedgers = m
	}
	s.CommodityKeywords, s.EquityKeywords = normKeywords(s.CommodityKeywords), normKeywords(s.EquityKeywords)
	for _, k := range s.CommodityKeywords {
		if contains(s.EquityKeywords, k) {
			return invalid("keyword %s is in both lists", k)
		}
	}
	if s.AllocateAfterMinutes < 0 || s.AllocateAfterMinutes > 24*60 {
		return invalid("allocate unallocated money after 0 (never) to 1440 minutes")
	}
	switch strings.ToUpper(strings.TrimSpace(s.AllocateAfterTo)) {
	case "", "WAIT":
		s.AllocateAfterTo = ""
	default:
		k, ok := normLedger(s.AllocateAfterTo)
		if !ok {
			return invalid("unallocated money goes to Equity or Commodity, or keeps waiting")
		}
		s.AllocateAfterTo = k
	}
	if !s.SeparateCommodityLedger {
		prev, _ := o.Settings(ctx, tenant)
		if prev.SeparateCommodityLedger {
			lf, err := o.clientLedgerFunds(ctx, tenant, "", "")
			if err != nil {
				return err
			}
			var left []string
			for _, u := range sortedKeys(lf) {
				if lf[u].Commodity != 0 || lf[u].Awaiting != 0 {
					left = append(left, u)
				}
			}
			if len(left) > 0 {
				if len(left) > 5 {
					left = append(left[:5], fmt.Sprintf("and %d more", len(left)-5))
				}
				return invalid("the separate commodity ledger cannot be turned off while clients have money in a commodity or awaiting ledger (%s); transfer it to the equity ledger first", strings.Join(left, ", "))
			}
		}
	}
	return nil
}

var reWord = map[string]*regexp.Regexp{}

func hasWord(text, word string) bool {
	re, ok := reWord[word]
	if !ok {
		re = regexp.MustCompile(`(^|[^A-Z0-9])` + regexp.QuoteMeta(word) + `([^A-Z0-9]|$)`)
	}
	return re.MatchString(text)
}

func init() {
	for _, k := range append(append([]string{}, DefaultCommodityKeywords...), DefaultEquityKeywords...) {
		reWord[k] = regexp.MustCompile(`(^|[^A-Z0-9])` + regexp.QuoteMeta(k) + `([^A-Z0-9]|$)`)
	}
}

// fundsInstruction is the ledger money received for a client goes to, and
// why: the receiving bank account's ledger, a narration keyword, the
// client's default. kind is "" when there is no instruction (or two that
// disagree in the narration): the money waits for a person.
func (s Settings) fundsInstruction(c Client, toAccount, narration string) (kind, why string) {
	if v := s.BankAccountLedgers[normAcctNo(toAccount)]; v == ledger.LedgerEquity || v == ledger.LedgerCommodity {
		return v, "paid into the " + LedgerNames[v] + " bank account " + MaskAccount(toAccount)
	}
	if n := strings.ToUpper(narration); strings.TrimSpace(n) != "" {
		ck, ek := s.CommodityKeywords, s.EquityKeywords
		if len(ck) == 0 {
			ck = DefaultCommodityKeywords
		}
		if len(ek) == 0 {
			ek = DefaultEquityKeywords
		}
		var cw, ew string
		for _, k := range ck {
			if hasWord(n, k) {
				cw = k
				break
			}
		}
		for _, k := range ek {
			if hasWord(n, k) {
				ew = k
				break
			}
		}
		switch {
		case cw != "" && ew != "":
			return "", fmt.Sprintf("the narration names both commodity (%s) and equity (%s)", cw, ew)
		case cw != "":
			return ledger.LedgerCommodity, "narration says " + cw
		case ew != "":
			return ledger.LedgerEquity, "narration says " + ew
		}
	}
	if k, ok := normLedger(c.FundsLedger); ok && c.FundsLedger != "" {
		return k, "the client's default ledger"
	}
	return "", "no instruction"
}

// routeCredit decides the ledger a bank credit from client c goes to,
// records it on the credit, and returns the account to credit.
func (o *Ops) routeCredit(set Settings, c Client, t *BankTxn) string {
	kind, why := set.fundsInstruction(c, t.ToAccount, t.Narration)
	if kind == "" {
		kind = ledger.LedgerAwaiting
	}
	t.Ledger, t.LedgerWhy = kind, why
	return ledger.ClientLedgerAccount(c.UCC, kind)
}

// creditedToLedger follows money in that reached a client's ledger: money
// for the equity ledger (or with one ledger, any money) may be allocated
// to NSE Clearing at once; money with no instruction waits for Risk.
func (o *Ops) creditedToLedger(ctx context.Context, tenant string, t BankTxn, name string) {
	if t.Earmark == EarmarkMCX {
		return // MCX money is never allocated to NSE Clearing
	}
	switch t.Ledger {
	case "", ledger.LedgerEquity:
		o.AllocationMoneyIn(ctx, tenant, t.UCC, t.Amount, "BANK:"+t.Ref)
	case ledger.LedgerAwaiting:
		o.awaitFunds(ctx, tenant, FundsAwaiting{ID: "BANK-" + t.Ref, Date: t.Date, Time: t.Time, UCC: t.UCC, Name: name, Amount: t.Amount, Mode: t.Mode, Ref: t.Ref,
			Narration: t.Narration, Source: firstOf(t.Source, "Bank statement"), Why: t.LedgerWhy, Entry: t.EntryID})
	}
}

// ---- money waiting to be allocated ----

// FundsAwaiting is money received for a client with no instruction which
// ledger it is for, kept in CLIENTUNALLOC:<ucc> until allocated.
type FundsAwaiting struct {
	ID        string       `json:"id"`
	Date      string       `json:"date"`
	Time      string       `json:"time,omitempty"`
	UCC       string       `json:"ucc"`
	Name      string       `json:"name"`
	Amount    money.Amount `json:"amount"`
	Mode      string       `json:"mode,omitempty"`
	Ref       string       `json:"ref"`
	Narration string       `json:"narration,omitempty"`
	Source    string       `json:"source,omitempty"`
	Why       string       `json:"why,omitempty"`
	Received  string       `json:"received"`         // RFC3339
	Status    string       `json:"status"`           // Waiting, Allocated
	Ledger    string       `json:"ledger,omitempty"` // EQ or COM once allocated
	By        string       `json:"by,omitempty"`
	At        string       `json:"at,omitempty"`
	Entry     string       `json:"entry,omitempty"` // the receipt's entry
	AllocEnt  string       `json:"alloc_entry,omitempty"`
	Auto      bool         `json:"auto,omitempty"` // allocated by the firm's time rule
}

// awaitFunds records money posted to the awaiting account and asks Risk
// to allocate it.
func (o *Ops) awaitFunds(ctx context.Context, tenant string, a FundsAwaiting) {
	a.Status = "Waiting"
	if a.Received == "" {
		a.Received = o.now().Format(time.RFC3339)
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cFundsAwait, a.ID, a, SystemActor); err != nil {
		return
	}
	o.log(ctx, tenant, SystemActor, "funds.awaiting", a.ID, map[string]any{"ucc": a.UCC, "amount": a.Amount.String(), "why": a.Why})
	rc := &runCtx{o: o, ctx: ctx, tenant: tenant, date: firstOf(a.Date, o.now().Format("2006-01-02")), process: wFundsAlloc, keys: map[string]bool{}}
	due := o.now().Add(15 * time.Minute).Format("2006-01-02 15:04")
	rc.queue(a.ID, fmt.Sprintf("₹%s from client %s: choose Equity or Commodity", formatINR(a.Amount.Dec()), a.UCC),
		fmt.Sprintf("%s %s from %s (%s) came in with no instruction which ledger it is for (%s). It is in the client's ledger but in neither front end's limit until it is allocated. Choose Equity or Commodity on RMS → Money awaiting allocation.", a.Mode, a.Ref, a.Name, a.UCC, a.Why),
		a.UCC, RoleRisk, "rms", due, nil)
}

// FundsAwaitingList lists money awaiting allocation (all, or only waiting).
func (o *Ops) FundsAwaitingList(ctx context.Context, tenant string, waitingOnly bool) ([]FundsAwaiting, error) {
	as, err := docs.List[FundsAwaiting](ctx, o.Docs, tenant, cFundsAwait, "")
	out := []FundsAwaiting{}
	for _, a := range as {
		if !waitingOnly || a.Status == "Waiting" {
			out = append(out, a)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].Status == "Waiting") != (out[j].Status == "Waiting") {
			return out[i].Status == "Waiting"
		}
		return out[i].Received < out[j].Received
	})
	return out, err
}

// AllocateFunds puts money awaiting allocation in the equity or commodity
// ledger: a journal from CLIENTUNALLOC to the chosen ledger, dated the day
// the money came in (today if that day is locked), so the next limit files
// for the front ends carry it.
func (o *Ops) AllocateFunds(ctx context.Context, tenant, actor, id, to string) (FundsAwaiting, error) {
	if err := o.require(ctx, tenant, actor, "Allocating money to a ledger", RoleRisk, RoleChecker, RoleMaker); err != nil {
		return FundsAwaiting{}, err
	}
	return o.allocateFunds(ctx, tenant, actor, id, to, false)
}

func (o *Ops) allocateFunds(ctx context.Context, tenant, actor, id, to string, auto bool) (FundsAwaiting, error) {
	kind, ok := normLedger(to)
	if !ok || strings.TrimSpace(to) == "" {
		return FundsAwaiting{}, invalid("allocate to Equity or Commodity")
	}
	a, _, err := docs.Get[FundsAwaiting](ctx, o.Docs, tenant, cFundsAwait, id)
	if err != nil {
		return a, err
	}
	if a.Status != "Waiting" {
		return a, fmt.Errorf("%w: %s from %s was already allocated to %s", ErrState, a.Ref, a.UCC, LedgerNames[a.Ledger])
	}
	date := a.Date
	if date == "" || o.checkLock(ctx, tenant, date) != nil {
		date = o.now().Format("2006-01-02")
	}
	how := "allocated by " + actor
	if auto {
		how = "not allocated in time; put in " + LedgerNames[kind] + " by the firm's rule"
	}
	e, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: tenant, Key: "ALLOC:" + a.ID, ValueDate: date, CreatedBy: actor,
		Narration: fmt.Sprintf("Allocation to %s ledger · %s %s", LedgerNames[kind], a.Mode, a.Ref), Source: ledger.SourceRef{Kind: srcAllocation, ID: a.ID},
		Postings: []ledger.Posting{{Account: ledger.ClientAwaitingAccount(a.UCC), Debit: a.Amount, Memo: how},
			{Account: ledger.ClientLedgerAccount(a.UCC, kind), Credit: a.Amount, Memo: "Money in allocated to " + LedgerNames[kind]}}})
	if err != nil {
		return a, err
	}
	a, err = docs.Update(ctx, o.Docs, tenant, cFundsAwait, id, actor, func(x *FundsAwaiting) error {
		x.Status, x.Ledger, x.By, x.At, x.AllocEnt, x.Auto = "Allocated", kind, actor, o.now().Format(time.RFC3339), e.ID, auto
		return nil
	})
	if err != nil {
		return a, err
	}
	o.closeWork(ctx, tenant, actor, wFundsAlloc, a.ID, "allocated to "+LedgerNames[kind])
	o.log(ctx, tenant, actor, "funds.allocated", a.ID, map[string]any{"ucc": a.UCC, "amount": a.Amount.String(), "ledger": kind, "auto": auto})
	if kind == ledger.LedgerEquity {
		o.AllocationMoneyIn(ctx, tenant, a.UCC, a.Amount, a.ID)
	}
	return a, nil
}

// fundsAllocTick applies the firm's rule for money left waiting too long.
func (o *Ops) fundsAllocTick(ctx context.Context, tenant string, now time.Time) {
	set, _ := o.Settings(ctx, tenant)
	if !set.sepCom() || set.AllocateAfterMinutes <= 0 || set.AllocateAfterTo == "" {
		return
	}
	as, _ := o.FundsAwaitingList(ctx, tenant, true)
	for _, a := range as {
		at, err := time.Parse(time.RFC3339, a.Received)
		if err != nil || now.Sub(at) < time.Duration(set.AllocateAfterMinutes)*time.Minute {
			continue
		}
		_, _ = o.allocateFunds(ctx, tenant, SystemActor, a.ID, set.AllocateAfterTo, true)
	}
}

// ---- transfers between a client's own ledgers ----

// LedgerTransfer asks to move a client's money between the equity and the
// commodity ledger: a journal voucher, approved by a checker as every
// journal is. It is hidden from the client's statements (it nets to
// nothing for the client).
func (o *Ops) LedgerTransfer(ctx context.Context, tenant, actor, ucc, from, to, date string, amount money.Amount, note string) (Voucher, error) {
	if err := o.require(ctx, tenant, actor, "Transferring between a client's ledgers", RoleRisk, RoleMaker, RoleChecker); err != nil {
		return Voucher{}, err
	}
	set, _ := o.Settings(ctx, tenant)
	if !set.sepCom() {
		return Voucher{}, invalid("the firm keeps one ledger per client (Settings → separate commodity ledger is off)")
	}
	fk, ok1 := normLedger(from)
	tk, ok2 := normLedger(to)
	if !ok1 || !ok2 || fk == tk {
		return Voucher{}, invalid("transfer from Equity to Commodity or from Commodity to Equity")
	}
	ucc = strings.ToUpper(strings.TrimSpace(ucc))
	c, err := o.Client(ctx, tenant, ucc)
	if err != nil {
		return Voucher{}, invalid("client %s not found", ucc)
	}
	if amount <= 0 {
		return Voucher{}, invalid("amount must be above zero")
	}
	if date == "" {
		date = o.now().Format("2006-01-02")
	}
	narr := fmt.Sprintf("Transfer between equity and commodity: %s to %s", LedgerNames[fk], LedgerNames[tk])
	if strings.TrimSpace(note) != "" {
		narr += " · " + strings.TrimSpace(note)
	}
	v := Voucher{Type: "Journal", Date: date, Party: c.UCC + " " + c.Name, Narration: narr,
		Lines: []VoucherLine{{Account: ledger.ClientLedgerAccount(c.UCC, fk), Debit: amount, Memo: "Transfer to " + LedgerNames[tk]},
			{Account: ledger.ClientLedgerAccount(c.UCC, tk), Credit: amount, Memo: "Transfer from " + LedgerNames[fk]}}}
	v, err = o.saveVoucher(ctx, tenant, actor, v)
	if err == nil {
		o.log(ctx, tenant, actor, "ledger.transfer.requested", v.No, map[string]any{"ucc": c.UCC, "from": fk, "to": tk, "amount": amount.String()})
	}
	return v, err
}

// postLedgerTransfer moves money between a client's ledgers at once, for
// what the firm's settings allow without a second person (a payout's
// shortfall, netting at running account settlement).
func (o *Ops) postLedgerTransfer(ctx context.Context, tenant, actor, key, date, ucc, from, to string, amount money.Amount, why string) (ledger.Entry, error) {
	e, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: tenant, Key: key, ValueDate: date, CreatedBy: actor,
		Narration: fmt.Sprintf("Transfer between equity and commodity: %s to %s · %s", LedgerNames[from], LedgerNames[to], why), Source: ledger.SourceRef{Kind: srcLedgerXfer, ID: key},
		Postings: []ledger.Posting{{Account: ledger.ClientLedgerAccount(ucc, from), Debit: amount, Memo: "Transfer to " + LedgerNames[to]},
			{Account: ledger.ClientLedgerAccount(ucc, to), Credit: amount, Memo: "Transfer from " + LedgerNames[from]}}})
	if err == nil {
		o.log(ctx, tenant, actor, "ledger.transfer.posted", key, map[string]any{"ucc": ucc, "from": from, "to": to, "amount": amount.String(), "why": why})
	}
	return e, err
}

// SetClientFundsLedger sets where a client's money goes when it comes in
// with no other instruction (EQ, COM, or "" to ask every time).
func (o *Ops) SetClientFundsLedger(ctx context.Context, tenant, actor, ucc, kind string) (Client, error) {
	if err := o.require(ctx, tenant, actor, "Setting a client's default ledger", RoleRisk, RoleChecker); err != nil {
		return Client{}, err
	}
	k := ""
	if strings.TrimSpace(kind) != "" {
		var ok bool
		if k, ok = normLedger(kind); !ok {
			return Client{}, invalid("the default ledger is Equity, Commodity or none (ask)")
		}
	}
	c, err := docs.Update(ctx, o.Docs, tenant, cClient, strings.ToUpper(ucc), actor, func(x *Client) error { x.FundsLedger = k; return nil })
	if err == nil {
		o.log(ctx, tenant, actor, "client.funds_ledger", c.UCC, map[string]any{"ledger": k})
	}
	return c.Public(), err
}

// ---- views ----

// ClientLedgers is a client's balance ledger by ledger, for staff.
type ClientLedgers struct {
	Equity    money.Dec       `json:"equity"`
	Commodity money.Dec       `json:"commodity"`
	Awaiting  money.Dec       `json:"awaiting"`
	Combined  money.Dec       `json:"combined"`
	Default   string          `json:"default,omitempty"`
	Waiting   []FundsAwaiting `json:"waiting"`
}

func (o *Ops) clientLedgersView(ctx context.Context, tenant, ucc, date string) (*ClientLedgers, error) {
	lf, err := o.clientLedgerFunds(ctx, tenant, date, "")
	if err != nil {
		return nil, err
	}
	f := lf[ucc]
	v := &ClientLedgers{Equity: f.Equity.Dec(), Commodity: f.Commodity.Dec(), Awaiting: f.Awaiting.Dec(), Combined: f.Combined().Dec(), Waiting: []FundsAwaiting{}}
	if c, err := o.Client(ctx, tenant, ucc); err == nil {
		v.Default = c.FundsLedger
	}
	as, _ := o.FundsAwaitingList(ctx, tenant, true)
	for _, a := range as {
		if a.UCC == ucc {
			v.Waiting = append(v.Waiting, a)
		}
	}
	return v, nil
}

// clientStatement is a client's ledger as the client sees it: with one
// ledger, the CLIENT:<ucc> statement exactly as always; with a separate
// commodity ledger, the equity, commodity and awaiting ledgers merged with
// one running balance and no internal transfers.
func (o *Ops) clientStatement(ctx context.Context, tenant, ucc, from, to string) (ledger.Statement, error) {
	if !o.sepCom(ctx, tenant) {
		return o.App.Ledger.Statement(ctx, tenant, ledger.ClientAccount(ucc), from, to)
	}
	return o.App.Ledger.ClientStatement(ctx, tenant, ucc, from, to, false)
}

// ClientStatement is clientStatement for other packages (the API's
// statement for a client code).
func (o *Ops) ClientStatement(ctx context.Context, tenant, ucc, from, to string) (ledger.Statement, error) {
	return o.clientStatement(ctx, tenant, ucc, from, to)
}

// clientEntries are the entries touching any of a client's ledgers up to
// a date, in posting order; with one ledger, CLIENT:<ucc>'s entries.
func (o *Ops) clientEntries(ctx context.Context, tenant, ucc, to string) ([]ledger.Entry, error) {
	if !o.sepCom(ctx, tenant) {
		return o.App.Ledger.Store.List(ctx, tenant, ledger.Filter{Account: ledger.ClientAccount(ucc), To: to})
	}
	seen := map[string]bool{}
	var es []ledger.Entry
	for _, k := range []string{ledger.LedgerEquity, ledger.LedgerCommodity, ledger.LedgerAwaiting} {
		xs, err := o.App.Ledger.Store.List(ctx, tenant, ledger.Filter{Account: ledger.ClientLedgerAccount(ucc, k), To: to})
		if err != nil {
			return nil, err
		}
		for _, e := range xs {
			if !seen[e.ID] {
				seen[e.ID] = true
				es = append(es, e)
			}
		}
	}
	sort.SliceStable(es, func(i, j int) bool {
		if es[i].ValueDate != es[j].ValueDate {
			return es[i].ValueDate < es[j].ValueDate
		}
		return es[i].Seq < es[j].Seq
	})
	return es, nil
}

// StaffStatement is staffStatement for other packages.
func (o *Ops) StaffStatement(ctx context.Context, tenant, ucc, from, to string) (ledger.Statement, error) {
	return o.staffStatement(ctx, tenant, ucc, from, to)
}

// staffStatement is the client drawer's statement: every line of every
// ledger, each marked with its ledger, when the ledgers are separate.
func (o *Ops) staffStatement(ctx context.Context, tenant, ucc, from, to string) (ledger.Statement, error) {
	if !o.sepCom(ctx, tenant) {
		return o.App.Ledger.Statement(ctx, tenant, ledger.ClientAccount(ucc), from, to)
	}
	return o.App.Ledger.ClientStatement(ctx, tenant, ucc, from, to, true)
}

// ---- money per ledger for limits and payouts ----

// unclearedBy sums uncleared cheques per client and ledger.
func (o *Ops) unclearedBy(ctx context.Context, tenant string) map[string]LedgerFunds {
	out := map[string]LedgerFunds{}
	rs, _ := docs.List[Receipt](ctx, o.Docs, tenant, cReceipt, "")
	for _, r := range rs {
		if r.Status != "Uncleared" {
			continue
		}
		f := out[r.UCC]
		if r.Ledger == ledger.LedgerCommodity || r.Earmark == EarmarkMCX {
			f.Commodity += r.Amount
		} else {
			f.Equity += r.Amount
		}
		out[r.UCC] = f
	}
	return out
}

// bowFunds is each client's commodity balance for the commodity front end
// (BOW): the commodity ledger at the end of the business day before date,
// less commodity cheques not yet cleared. Money awaiting allocation is not
// in it.
func (o *Ops) bowFunds(ctx context.Context, tenant, date string) (map[string]money.Dec, error) {
	set, _ := o.Settings(ctx, tenant)
	lf, err := o.segFunds(ctx, tenant, set, set.PrevBusinessDay(date), "")
	if err != nil {
		return nil, err
	}
	out := map[string]money.Dec{}
	for u, f := range lf {
		out[u] = f.Commodity.Dec()
	}
	for u, f := range o.unclearedBy(ctx, tenant) {
		if f.Commodity != 0 {
			out[u] = out[u].Sub(f.Commodity.Dec())
		}
	}
	return out, nil
}

// BOWFundsFile lists every active client's commodity funds for the
// commodity front end (BOW): a plain CSV of client code and commodity
// funds, for checking. BOW's own upload (BOW_FUND, bow-fund) is written by
// BOWFundFile (bowfiles.go): by default a securities and a commodities
// line per client (DEPOSIT_UPLOAD has a category), or the old single
// user-level line (Securities + Commodities).
func (o *Ops) BOWFundsFile(ctx context.Context, tenant, date string) ([]byte, error) {
	set, _ := o.Settings(ctx, tenant)
	if !set.segApart() {
		return nil, invalid("commodity funds are kept apart only with a separate commodity ledger or an MCX-only bank account (Settings)")
	}
	f, err := o.bowFunds(ctx, tenant, date)
	if err != nil {
		return nil, err
	}
	cs, _ := o.ClientMap(ctx, tenant)
	rows := [][]string{{"TradeDate", "ClientCode", "CommodityFunds"}}
	for _, u := range sortedKeys(cs) {
		c := cs[u]
		if c.Status != "Active" {
			continue
		}
		v := f[u]
		if c.Blocked || c.Inactive {
			v = money.Min(v, money.Dec{})
		}
		rows = append(rows, []string{date, u, v.Fixed(2)})
	}
	return csvBytesOf(rows), nil
}

// ---- payouts from separate ledgers ----

// payoutLegs is how a payout of amt for ucc is paid from the client's
// ledgers, given each ledger's balance now: from the ledger asked for
// (with the shortfall transferred in first when the firm allows it), or,
// with no ledger asked for, from each ledger only up to its own credit,
// the larger first. No ledger is taken below zero by the payout.
func payoutLegs(f LedgerFunds, amt money.Amount, want string, autoTransfer bool) (legs map[string]money.Amount, transfer money.Amount, from string, err error) {
	legs = map[string]money.Amount{}
	pos := func(a money.Amount) money.Amount { return max(a, 0) }
	if want != "" {
		other := ledger.LedgerCommodity
		if want == ledger.LedgerCommodity {
			other = ledger.LedgerEquity
		}
		have := pos(f.Of(want))
		if amt <= have {
			legs[want] = amt
			return legs, 0, "", nil
		}
		short := amt - have
		if !autoTransfer {
			return nil, 0, "", invalid("₹%s is more than the ₹%s credit in the %s ledger; transfer ₹%s from the %s ledger first (or let payouts transfer it, in Settings)",
				formatINR(amt.Dec()), formatINR(have.Dec()), LedgerNames[want], formatINR(short.Dec()), LedgerNames[other])
		}
		if pos(f.Of(other)) < short {
			return nil, 0, "", invalid("₹%s is more than the client's credit in the %s and %s ledgers together", formatINR(amt.Dec()), LedgerNames[want], LedgerNames[other])
		}
		legs[want] = amt
		return legs, short, other, nil
	}
	order := []string{ledger.LedgerEquity, ledger.LedgerCommodity}
	if pos(f.Commodity) > pos(f.Equity) {
		order = []string{ledger.LedgerCommodity, ledger.LedgerEquity}
	}
	rest := amt
	for _, k := range order {
		take := min(rest, pos(f.Of(k)))
		if take > 0 {
			legs[k] = take
			rest -= take
		}
	}
	if rest > 0 {
		return nil, 0, "", invalid("₹%s is more than the client's credit in the equity and commodity ledgers (₹%s waiting to be allocated is not paid out until it is allocated)", formatINR(amt.Dec()), formatINR(f.Awaiting.Dec()))
	}
	return legs, 0, "", nil
}

// rasPostings are the client legs of a running account settlement payout
// with a separate commodity ledger. The settlement looks at the client as
// a whole, so a ledger in debit is first made good from the others (a
// transfer, hidden from the client like every transfer); the payout then
// comes from money awaiting allocation, then each ledger's money above
// its own retention, then (only if still short) the rest of a ledger's
// credit. No ledger is taken below zero.
func (o *Ops) rasPostings(ctx context.Context, tenant, actor, asOf, settleDate string, r RASRow, amt money.Amount) ([]ledger.Posting, error) {
	lf, err := o.clientLedgerFunds(ctx, tenant, asOf, "")
	if err != nil {
		return nil, err
	}
	f := lf[r.UCC]
	unc := o.unclearedBy(ctx, tenant)[r.UCC]
	kinds := []string{ledger.LedgerAwaiting, ledger.LedgerCommodity, ledger.LedgerEquity}
	// Net a ledger in debit against the others' credit.
	for _, neg := range kinds {
		for _, src := range kinds {
			if f.Of(neg) >= 0 || src == neg || f.Of(src) <= 0 {
				continue
			}
			mv := min(-f.Of(neg), f.Of(src))
			if _, err := o.postLedgerTransfer(ctx, tenant, actor, fmt.Sprintf("RASNET:%s:%s:%s:%s", settleDate, r.UCC, src, neg), asOf, r.UCC, src, neg, mv, "running account settlement "+settleDate); err != nil {
				return nil, err
			}
			f.add(src, -mv)
			f.add(neg, mv)
		}
	}
	ret := map[string]money.Amount{
		ledger.LedgerEquity:    r.retEq.Round(money.UnitPaisa) + unc.Equity,
		ledger.LedgerCommodity: r.retCom.Round(money.UnitPaisa) + unc.Commodity,
	}
	legs := map[string]money.Amount{}
	rest := amt
	for _, pass := range []bool{true, false} {
		for _, k := range kinds {
			room := f.Of(k) - legs[k] - unc.Of(k) // uncleared cheques are never paid out
			if pass {
				room -= ret[k] - unc.Of(k)
			}
			if take := min(rest, max(room, 0)); take > 0 {
				legs[k] += take
				rest -= take
			}
		}
	}
	if rest > 0 {
		return nil, fmt.Errorf("%w: %s's ledgers cannot pay ₹%s without going below zero", ErrState, r.UCC, formatINR(amt.Dec()))
	}
	var ps []ledger.Posting
	for _, k := range []string{ledger.LedgerEquity, ledger.LedgerCommodity, ledger.LedgerAwaiting} {
		if a := legs[k]; a > 0 {
			ps = append(ps, ledger.Posting{Account: ledger.ClientLedgerAccount(r.UCC, k), Debit: a, Memo: "Running account settlement payout"})
		}
	}
	return append(ps, ledger.Posting{Account: ledger.BankClient, Credit: amt}), nil
}

// add changes one ledger's funds.
func (f *LedgerFunds) add(kind string, a money.Amount) {
	switch kind {
	case ledger.LedgerCommodity:
		f.Commodity += a
	case ledger.LedgerAwaiting:
		f.Awaiting += a
	default:
		f.Equity += a
	}
}

// withdrawableLedgers fills in what each ledger can pay on its own, and
// the client's total: money awaiting allocation is not paid out until it
// is allocated, and MCX margin is kept in the commodity ledger.
func (o *Ops) withdrawableLedgers(ctx context.Context, tenant string, set Settings, date string, w *Withdrawable, prs []PayoutRequest, comMargin money.Amount) error {
	lf, err := o.segFunds(ctx, tenant, set, date, "")
	if err != nil {
		return err
	}
	f := lf[w.UCC]
	unc := o.unclearedBy(ctx, tenant)[w.UCC]
	var eqPend, comPend money.Amount
	for _, p := range prs {
		if p.UCC != w.UCC || p.Status != "Approved" || p.Batch != "" {
			continue
		}
		switch {
		case p.Ledger == ledger.LedgerCommodity:
			comPend += p.Amount
		case p.Ledger == ledger.LedgerEquity, set.mcxEarmark(): // with the earmark, any other payout is equity money
			eqPend += p.Amount
		}
	}
	eqMargin := w.Margin
	w.Margin += comMargin
	w.Ledgers = &WithdrawLedgers{
		Equity:    f.Equity - unc.Equity - eqMargin - w.Accrued - eqPend,
		Commodity: f.Commodity - unc.Commodity - comMargin - comPend,
		Awaiting:  f.Awaiting,
	}
	w.Available = w.Ledger - f.Awaiting - w.Margin - w.Uncleared - w.Accrued - w.Pending
	if f.Awaiting > 0 {
		w.Note = strings.TrimSpace(w.Note + fmt.Sprintf(" ₹%s is waiting to be allocated to a ledger and is paid out once allocated.", formatINR(f.Awaiting.Dec())))
	}
	return nil
}

// checkPayoutLedger refuses a payout from one ledger that the ledger
// cannot pay on its own, unless the firm lets payouts transfer the
// shortfall from the other ledger.
func (o *Ops) checkPayoutLedger(ctx context.Context, tenant string, w Withdrawable, amount money.Amount, from string) error {
	set, _ := o.Settings(ctx, tenant)
	if set.mcxEarmark() {
		return checkEarmarkPayout(w, amount, from)
	}
	if w.Ledgers == nil || from == "" {
		return nil
	}
	have := w.Ledgers.Equity
	other, otherHave := ledger.LedgerCommodity, w.Ledgers.Commodity
	if from == ledger.LedgerCommodity {
		have, other, otherHave = w.Ledgers.Commodity, ledger.LedgerEquity, w.Ledgers.Equity
	}
	if amount <= have {
		return nil
	}
	if set.PayoutAutoTransfer && amount-max(have, 0) <= max(otherHave, 0) {
		return nil
	}
	return invalid("₹%s is more than the ₹%s the %s ledger can pay; transfer ₹%s from the %s ledger first (or let payouts transfer it, in Settings)",
		formatINR(amount.Dec()), formatINR(max(have, 0).Dec()), LedgerNames[from], formatINR((amount - max(have, 0)).Dec()), LedgerNames[other])
}

// checkPayoutBatch works through a batch's payouts in order against the
// ledgers, as CreatePayoutBatch will post them, and refuses the batch if
// any would take a ledger below zero.
func (o *Ops) checkPayoutBatch(ctx context.Context, tenant, date string, set Settings, prs []PayoutRequest) error {
	lf, err := o.clientLedgerFunds(ctx, tenant, date, "")
	if err != nil {
		return err
	}
	for _, p := range prs {
		if p.Status != "Approved" || p.Batch != "" {
			continue
		}
		f := lf[p.UCC]
		legs, tr, from, err := payoutLegs(f, p.Amount, p.Ledger, set.PayoutAutoTransfer)
		if err != nil {
			return fmt.Errorf("payout %s of %s: %w", p.ID, p.UCC, err)
		}
		if tr > 0 {
			f.add(from, -tr)
			f.add(p.Ledger, tr)
		}
		for k, a := range legs {
			f.add(k, -a)
		}
		lf[p.UCC] = f
	}
	return nil
}

// payoutPostings are a payout's client legs from the ledgers it is paid
// from, after transferring a shortfall in when the firm allows it, and
// the transfer's entry.
func (o *Ops) payoutPostings(ctx context.Context, tenant, actor, date string, set Settings, p PayoutRequest) ([]ledger.Posting, string, error) {
	lf, err := o.clientLedgerFunds(ctx, tenant, date, "")
	if err != nil {
		return nil, "", err
	}
	legs, tr, from, err := payoutLegs(lf[p.UCC], p.Amount, p.Ledger, set.PayoutAutoTransfer)
	if err != nil {
		return nil, "", fmt.Errorf("payout %s of %s: %w", p.ID, p.UCC, err)
	}
	xfer := ""
	if tr > 0 {
		e, err := o.postLedgerTransfer(ctx, tenant, actor, "LTX:PAY:"+p.ID, date, p.UCC, from, p.Ledger, tr, "shortfall for payout "+p.ID)
		if err != nil {
			return nil, "", err
		}
		xfer = e.ID
	}
	var ps []ledger.Posting
	for _, k := range []string{ledger.LedgerEquity, ledger.LedgerCommodity} {
		if a := legs[k]; a > 0 {
			ps = append(ps, ledger.Posting{Account: ledger.ClientLedgerAccount(p.UCC, k), Debit: a, Memo: "Payout on request"})
		}
	}
	return append(ps, ledger.Posting{Account: ledger.BankClient, Credit: p.Amount}), xfer, nil
}
