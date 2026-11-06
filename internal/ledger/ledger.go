// Package ledger is the double-entry, append-only journal at the centre of
// the back office. Every money movement (bills, receipts, payments, charges,
// settlements) is a balanced Entry. Entries are never edited: corrections are
// reversing entries. Balances, trial balances and statements are projections
// over the journal.
package ledger

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/money"
)

var (
	ErrInvalid  = errors.New("ledger: invalid entry")
	ErrConflict = errors.New("ledger: idempotency key already used for a different entry")
	ErrNotFound = errors.New("ledger: entry not found")
)

// AccountType classifies an account for reporting.
type AccountType string

const (
	Asset     AccountType = "ASSET"
	Liability AccountType = "LIABILITY"
	Income    AccountType = "INCOME"
	Expense   AccountType = "EXPENSE"
	Equity    AccountType = "EQUITY"
)

// Standard account codes. Codes are PREFIX:qualifier; the prefix sets the type.
const (
	BankClient      = "BANK:CLIENT"
	BankOwn         = "BANK:OWN"
	IncomeBrokerage = "INCOME:BROKERAGE"
	PayableSTT      = "PAYABLE:STT"
	PayableStamp    = "PAYABLE:STAMP"
	PayableSEBIFee  = "PAYABLE:SEBI_FEE"
	PayableGST      = "PAYABLE:GST"
	SuspenseBank    = "SUSPENSE:BANK"
	OpeningEquity   = "EQUITY:OPENING"
	CCCollateral    = "CC:COLLATERAL"
	PayablePenalty  = "PAYABLE:CC_PENALTY"
)

// ClientAccount is a client's trading ledger. A credit balance means the
// broker owes the client.
func ClientAccount(ucc string) string { return "CLIENT:" + ucc }

// A firm that is only a trading member on a commodity exchange (its
// commodity trades are cleared by another clearing member and traded on
// another front end) keeps each client's money in two ledgers: CLIENT:<ucc>
// stays the equity ledger (NSE and BSE), so every reader of CLIENT: keeps
// working unchanged, and CLIENTCOM:<ucc> is the commodity ledger (MCX).
// Money received without an instruction saying which ledger it is for
// waits in CLIENTUNALLOC:<ucc> until a person allocates it. The two new
// prefixes do not start with "CLIENT:", so no existing
// strings.CutPrefix(acct, "CLIENT:") reader can mistake them for a client
// code: those readers see the equity ledger only. ClientCode reads all
// three. Firms that do not separate the ledgers never post to the two new
// accounts.
const (
	ClientEquityPrefix    = "CLIENT:"
	ClientCommodityPrefix = "CLIENTCOM:"
	ClientAwaitingPrefix  = "CLIENTUNALLOC:"
)

// Client ledger kinds, as ClientCode returns them.
const (
	LedgerEquity    = "EQ"
	LedgerCommodity = "COM"
	LedgerAwaiting  = "UNALLOC"
)

// ClientCommodityAccount is a client's commodity ledger.
func ClientCommodityAccount(ucc string) string { return ClientCommodityPrefix + ucc }

// ClientAwaitingAccount holds a client's money that is waiting to be
// allocated to the equity or the commodity ledger.
func ClientAwaitingAccount(ucc string) string { return ClientAwaitingPrefix + ucc }

// ClientLedgerAccount is a client's account for a ledger kind; an empty
// kind is the equity ledger.
func ClientLedgerAccount(ucc, kind string) string {
	switch kind {
	case LedgerCommodity:
		return ClientCommodityAccount(ucc)
	case LedgerAwaiting:
		return ClientAwaitingAccount(ucc)
	}
	return ClientAccount(ucc)
}

// ClientCode reads any of a client's ledger accounts: the client code and
// the ledger kind (LedgerEquity, LedgerCommodity or LedgerAwaiting).
func ClientCode(acct string) (ucc, kind string, ok bool) {
	if u, ok := strings.CutPrefix(acct, ClientEquityPrefix); ok {
		return u, LedgerEquity, true
	}
	if u, ok := strings.CutPrefix(acct, ClientCommodityPrefix); ok {
		return u, LedgerCommodity, true
	}
	if u, ok := strings.CutPrefix(acct, ClientAwaitingPrefix); ok {
		return u, LedgerAwaiting, true
	}
	return "", "", false
}

// IsClientAccount reports whether acct is one of a client's ledgers.
func IsClientAccount(acct string) bool {
	_, _, ok := ClientCode(acct)
	return ok
}

// ClearingAccount is the settlement account with a clearing corporation for
// one exchange segment, e.g. CC:NSE-CM.
func ClearingAccount(exchange, segment string) string { return "CC:" + exchange + "-" + segment }

// ExchangeTxnPayable holds exchange transaction charges owed to an exchange.
func ExchangeTxnPayable(exchange string) string { return "PAYABLE:EXCH_TXN:" + exchange }

var prefixTypes = map[string]AccountType{
	"CLIENT": Liability, "CLIENTCOM": Liability, "CLIENTUNALLOC": Liability, "PAYABLE": Liability, "AP": Liability, "SUSPENSE": Liability, "LOAN": Liability,
	"BANK": Asset, "CC": Asset, "RECEIVABLE": Asset, "CASH": Asset, "ASSET": Asset,
	"INCOME": Income, "EXPENSE": Expense, "EQUITY": Equity,
}

// TypeOf returns the account type implied by a code's prefix.
func TypeOf(code string) (AccountType, error) {
	prefix, rest, ok := strings.Cut(code, ":")
	if !ok || rest == "" || strings.ContainsAny(code, " \t\n") {
		return "", fmt.Errorf("account code %q must look like PREFIX:NAME", code)
	}
	t, ok := prefixTypes[prefix]
	if !ok {
		return "", fmt.Errorf("account code %q has unknown prefix %q", code, prefix)
	}
	return t, nil
}

// SourceRef points at what caused an entry: a file line, a billing run, a user.
type SourceRef struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Line int    `json:"line,omitempty"`
}

// Posting is one leg of an entry. Exactly one of Debit or Credit is positive.
type Posting struct {
	Account string       `json:"account"`
	Debit   money.Amount `json:"debit"`
	Credit  money.Amount `json:"credit"`
	Memo    string       `json:"memo,omitempty"`
}

// Entry is a balanced journal entry.
type Entry struct {
	ID         string    `json:"id"`
	Seq        int64     `json:"seq"`
	Tenant     string    `json:"tenant"`
	Key        string    `json:"key"` // idempotency key, unique per tenant
	ValueDate  string    `json:"value_date"`
	Narration  string    `json:"narration"`
	Source     SourceRef `json:"source"`
	Postings   []Posting `json:"postings"`
	ReversalOf string    `json:"reversal_of,omitempty"`
	CreatedBy  string    `json:"created_by"`
	CreatedAt  time.Time `json:"created_at"`
}

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrInvalid}, a...)...)
}

// ValidDate reports whether s is a YYYY-MM-DD date.
func ValidDate(s string) bool {
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

// Validate checks structure and balance.
func (e Entry) Validate() error {
	switch {
	case e.Tenant == "":
		return invalid("tenant is required")
	case e.Key == "":
		return invalid("idempotency key is required")
	case !ValidDate(e.ValueDate):
		return invalid("value date %q must be YYYY-MM-DD", e.ValueDate)
	case strings.TrimSpace(e.Narration) == "":
		return invalid("narration is required")
	case e.CreatedBy == "":
		return invalid("created_by is required")
	case len(e.Postings) < 2:
		return invalid("an entry needs at least two postings, got %d", len(e.Postings))
	}
	var dr, cr money.Amount
	for i, p := range e.Postings {
		if _, err := TypeOf(p.Account); err != nil {
			return invalid("posting %d: %v", i+1, err)
		}
		if p.Debit < 0 || p.Credit < 0 {
			return invalid("posting %d: amounts cannot be negative", i+1)
		}
		if (p.Debit > 0) == (p.Credit > 0) {
			return invalid("posting %d: exactly one of debit or credit must be positive", i+1)
		}
		dr += p.Debit
		cr += p.Credit
	}
	if dr != cr {
		return invalid("debits %s do not equal credits %s", dr, cr)
	}
	return nil
}

// Fingerprint identifies an entry's content for idempotency checks. It
// ignores server-assigned fields and the author, so a retried request from a
// different worker still matches.
func (e Entry) Fingerprint() string {
	c := struct {
		Tenant, Key, ValueDate, Narration, ReversalOf string
		Source                                        SourceRef
		Postings                                      []Posting
	}{e.Tenant, e.Key, e.ValueDate, e.Narration, e.ReversalOf, e.Source, e.Postings}
	b, _ := json.Marshal(c)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Filter narrows a listing. Dates are inclusive YYYY-MM-DD; empty means open.
type Filter struct {
	Account string
	From    string
	To      string
}

// Matches reports whether e passes the filter.
func (f Filter) Matches(e Entry) bool {
	if f.From != "" && e.ValueDate < f.From {
		return false
	}
	if f.To != "" && e.ValueDate > f.To {
		return false
	}
	if f.Account == "" {
		return true
	}
	for _, p := range e.Postings {
		if p.Account == f.Account {
			return true
		}
	}
	return false
}

// Store persists entries. Implementations must be append-only, assign ID,
// Seq and CreatedAt, and enforce idempotency: appending an entry whose Key
// already exists returns the stored entry with created=false when the
// fingerprints match, and ErrConflict otherwise.
type Store interface {
	Append(ctx context.Context, e Entry) (stored Entry, created bool, err error)
	Get(ctx context.Context, tenant, id string) (Entry, error)
	List(ctx context.Context, tenant string, f Filter) ([]Entry, error)
}

// Ledger provides posting and projections over a Store.
type Ledger struct {
	Store Store
	// Guard, when set, may refuse an entry before it is appended, for
	// example one dated in a locked period.
	Guard func(ctx context.Context, e Entry) error
}

func New(s Store) *Ledger { return &Ledger{Store: s} }

// Post validates and appends an entry.
func (l *Ledger) Post(ctx context.Context, e Entry) (Entry, bool, error) {
	if err := e.Validate(); err != nil {
		return Entry{}, false, err
	}
	if l.Guard != nil {
		if err := l.Guard(ctx, e); err != nil {
			return Entry{}, false, err
		}
	}
	return l.Store.Append(ctx, e)
}

// Reverse posts the mirror image of an entry. Each entry can be reversed
// once; a repeated call with the same reason returns the existing reversal.
func (l *Ledger) Reverse(ctx context.Context, tenant, id, actor, reason, valueDate string) (Entry, bool, error) {
	if strings.TrimSpace(reason) == "" {
		return Entry{}, false, invalid("a reason is required to reverse an entry")
	}
	orig, err := l.Store.Get(ctx, tenant, id)
	if err != nil {
		return Entry{}, false, err
	}
	if orig.ReversalOf != "" {
		return Entry{}, false, invalid("%s is itself a reversal; post a new correcting entry instead", id)
	}
	if valueDate == "" {
		valueDate = orig.ValueDate
	}
	ps := make([]Posting, len(orig.Postings))
	for i, p := range orig.Postings {
		ps[i] = Posting{Account: p.Account, Debit: p.Credit, Credit: p.Debit, Memo: "reversal"}
	}
	return l.Post(ctx, Entry{
		Tenant: tenant, Key: "REV:" + id, ValueDate: valueDate,
		Narration: "Reversal of " + id + ": " + reason,
		Source:    SourceRef{Kind: "REVERSAL", ID: id},
		Postings:  ps, ReversalOf: id, CreatedBy: actor,
	})
}

// Balances returns debit-minus-credit per account for entries up to asOf
// (inclusive; empty means all). Positive is a debit balance.
func (l *Ledger) Balances(ctx context.Context, tenant, asOf string) (map[string]money.Amount, error) {
	es, err := l.Store.List(ctx, tenant, Filter{To: asOf})
	if err != nil {
		return nil, err
	}
	out := map[string]money.Amount{}
	for _, e := range es {
		for _, p := range e.Postings {
			out[p.Account] += p.Debit - p.Credit
		}
	}
	return out, nil
}

// TBRow is one line of a trial balance.
type TBRow struct {
	Account string       `json:"account"`
	Type    AccountType  `json:"type"`
	Debit   money.Amount `json:"debit"`
	Credit  money.Amount `json:"credit"`
}

// TrialBalance lists every non-zero account with total debit and credit
// columns, which must agree.
type TrialBalance struct {
	AsOf        string       `json:"as_of"`
	Rows        []TBRow      `json:"rows"`
	TotalDebit  money.Amount `json:"total_debit"`
	TotalCredit money.Amount `json:"total_credit"`
	Balanced    bool         `json:"balanced"`
}

func (l *Ledger) TrialBalance(ctx context.Context, tenant, asOf string) (TrialBalance, error) {
	bal, err := l.Balances(ctx, tenant, asOf)
	if err != nil {
		return TrialBalance{}, err
	}
	tb := TrialBalance{AsOf: asOf, Rows: []TBRow{}}
	for acct, b := range bal {
		if b == 0 {
			continue
		}
		t, _ := TypeOf(acct)
		row := TBRow{Account: acct, Type: t}
		if b > 0 {
			row.Debit = b
		} else {
			row.Credit = -b
		}
		tb.TotalDebit += row.Debit
		tb.TotalCredit += row.Credit
		tb.Rows = append(tb.Rows, row)
	}
	sort.Slice(tb.Rows, func(i, j int) bool { return tb.Rows[i].Account < tb.Rows[j].Account })
	tb.Balanced = tb.TotalDebit == tb.TotalCredit
	return tb, nil
}

// StatementLine is one posting on an account statement.
type StatementLine struct {
	EntryID   string       `json:"entry_id"`
	ValueDate string       `json:"value_date"`
	Narration string       `json:"narration"`
	Memo      string       `json:"memo,omitempty"`
	Debit     money.Amount `json:"debit"`
	Credit    money.Amount `json:"credit"`
	Balance   money.Amount `json:"balance"` // running debit-minus-credit
	// Ledger is the client ledger a line of a combined client statement
	// is in (LedgerEquity, LedgerCommodity, LedgerAwaiting), on staff
	// views only; empty on an account's own statement.
	Ledger string `json:"ledger,omitempty"`
}

// Statement is an account's movements between two dates with an opening
// balance carried in.
type Statement struct {
	Account string          `json:"account"`
	From    string          `json:"from"`
	To      string          `json:"to"`
	Opening money.Amount    `json:"opening"`
	Lines   []StatementLine `json:"lines"`
	Closing money.Amount    `json:"closing"`
}

func (l *Ledger) Statement(ctx context.Context, tenant, account, from, to string) (Statement, error) {
	if _, err := TypeOf(account); err != nil {
		return Statement{}, invalid("%v", err)
	}
	es, err := l.Store.List(ctx, tenant, Filter{Account: account, To: to})
	if err != nil {
		return Statement{}, err
	}
	st := Statement{Account: account, From: from, To: to, Lines: []StatementLine{}}
	run := money.Amount(0)
	for _, e := range es {
		for _, p := range e.Postings {
			if p.Account != account {
				continue
			}
			run += p.Debit - p.Credit
			if from != "" && e.ValueDate < from {
				st.Opening = run
				continue
			}
			st.Lines = append(st.Lines, StatementLine{EntryID: e.ID, ValueDate: e.ValueDate, Narration: e.Narration, Memo: p.Memo, Debit: p.Debit, Credit: p.Credit, Balance: run})
		}
	}
	st.Closing = run
	return st, nil
}

// ClientStatement is a client's comprehensive statement: the equity,
// commodity and awaiting-allocation ledgers merged in posting order (value
// date, then sequence, as Store.List returns them) with one running
// balance, as account CLIENT:<ucc>. With staff false it is the client's
// own view: an entry that only moves money between the client's own
// ledgers (a transfer between equity and commodity, the allocation of
// money that was waiting) nets to nothing for the client and is left out;
// with staff true every line is kept and carries its ledger. A client
// with only the equity ledger gets the same lines as Statement.
func (l *Ledger) ClientStatement(ctx context.Context, tenant, ucc, from, to string, staff bool) (Statement, error) {
	accts := []string{ClientAccount(ucc), ClientCommodityAccount(ucc), ClientAwaitingAccount(ucc)}
	seen := map[string]bool{}
	var es []Entry
	for _, a := range accts {
		xs, err := l.Store.List(ctx, tenant, Filter{Account: a, To: to})
		if err != nil {
			return Statement{}, err
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
	mine := func(a string) (string, bool) {
		u, kind, ok := ClientCode(a)
		return kind, ok && u == ucc
	}
	st := Statement{Account: ClientAccount(ucc), From: from, To: to, Lines: []StatementLine{}}
	run := money.Amount(0)
	for _, e := range es {
		if !staff && InternalClientEntry(e, ucc) {
			continue
		}
		for _, p := range e.Postings {
			kind, ok := mine(p.Account)
			if !ok {
				continue
			}
			run += p.Debit - p.Credit
			if from != "" && e.ValueDate < from {
				st.Opening = run
				continue
			}
			ln := StatementLine{EntryID: e.ID, ValueDate: e.ValueDate, Narration: e.Narration, Memo: p.Memo, Debit: p.Debit, Credit: p.Credit, Balance: run}
			if staff {
				ln.Ledger = kind
			}
			st.Lines = append(st.Lines, ln)
		}
	}
	st.Closing = run
	return st, nil
}

// InternalClientEntry reports whether an entry only moves a client's money
// between the client's own ledgers: it touches two or more of them and
// nets to nothing across them.
func InternalClientEntry(e Entry, ucc string) bool {
	kinds := map[string]bool{}
	var net money.Amount
	for _, p := range e.Postings {
		if u, kind, ok := ClientCode(p.Account); ok && u == ucc {
			kinds[kind] = true
			net += p.Debit - p.Credit
		}
	}
	return len(kinds) > 1 && net == 0
}
