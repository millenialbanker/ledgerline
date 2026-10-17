package ops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// MasterSource is a non-trade file the back office loads.
type MasterSource struct {
	Name     string   `json:"name"`
	Purpose  string   `json:"purpose"`
	Columns  []string `json:"columns"`
	NeedDate bool     `json:"needs_date"`
	When     string   `json:"when"`
}

// MasterSources lists the non-trade file types, with their CSV headers.
func MasterSources() []MasterSource {
	return []MasterSource{
		{"CLIENT-MASTER", "Client master import (migration or bulk onboarding). Optional columns: Branch, State (place of supply for GST), GSTIN", []string{"UCC", "Name", "Type", "PAN", "DOB", "Mobile", "Email", "City", "Segments", "Risk", "Income", "RAS", "Bank", "IFSC", "Account", "Depository", "DematID", "Nominee", "NomineeName", "KYCDate", "Status", "API", "LastTrade", "UPIBlock"}, false, "Once, then on change"},
		{"OPENING-BALANCES", "Opening trial balance (migration); must balance. Optional column Since: for a client debit, the date it became due, so ageing, interest and debit blocks carry over", []string{"Account", "Debit", "Credit"}, true, "Once at go-live"},
		{"MARKET-DATA", "Contract master, prices and risk parameters (SPAN, VaR, haircuts)", []string{"Exchange", "Segment", "Symbol", "Type", "Expiry", "Strike", "ISIN", "Company", "Lot", "LTP", "Close", "VaRPct", "ELMPct", "HaircutPct", "SPANPct", "ExposurePct", "Delta", "Underlying", "Index"}, true, "BOD and EOD"},
		{"DP-HOLDINGS", "Client demat holdings and pledges from the depository", []string{"UCC", "ISIN", "Symbol", "Qty", "Pledged", "AvgCost"}, false, "BOD"},
		{"HOLDING-LOTS", "Purchase lots behind client holdings (quantity, buy date, buy price) for P&L and capital gains; from the previous back office", []string{"UCC", "ISIN", "Symbol", "Qty", "BuyDate", "BuyPrice"}, true, "Once at go-live (as at the day before)"},
		{"POSITIONS", "Derivatives positions carried forward from the previous day", []string{"UCC", "Exchange", "Segment", "Symbol", "Type", "Expiry", "Strike", "Qty", "AvgPrice"}, true, "BOD"},
		{"PLEDGE-REQUESTS", "Margin pledge requests initiated by clients at the depository", []string{"RequestID", "UCC", "ISIN", "Symbol", "Qty", "Time"}, false, "Intraday"},
		{"BANK-STMT", "Client bank account statement", []string{"Date", "Time", "Mode", "Reference", "Remitter", "Account", "Amount", "DrCr"}, false, "Intraday and EOD"},
		{"SURV-ALERTS", "Exchange surveillance alerts", []string{"AlertID", "Exchange", "Type", "UCC", "Detail", "Severity"}, false, "Daily"},
		{"IPO-BIDS", "IPO applications from the exchange bidding platform", []string{"ApplicationNo", "UCC", "Issue", "Lots", "Amount", "UPI", "Mandate"}, false, "During the issue"},
		{"IPO-ALLOTMENT", "IPO allotment results from the registrar", []string{"ApplicationNo", "AllottedLots"}, false, "After allotment"},
		{"MF-ORDERS", "Mutual fund orders from the exchange platform", []string{"OrderNo", "UCC", "Scheme", "Type", "Amount", "Platform", "Status"}, false, "Daily"},
	}
}

// IsMasterSource reports whether a file type is handled here.
func IsMasterSource(name string) bool {
	for _, s := range MasterSources() {
		if s.Name == name {
			return true
		}
	}
	return false
}

type row struct {
	line int
	get  func(col string) string
}

// LoadFile stores a master file and applies it. Strict: any row error
// rejects the whole file and nothing is applied.
func (o *Ops) LoadFile(ctx context.Context, tenant, actor, source, date, name string, data []byte) (ingest.Result, error) {
	var spec MasterSource
	for _, s := range MasterSources() {
		if s.Name == source {
			spec = s
		}
	}
	if spec.Name == "" {
		return ingest.Result{}, fmt.Errorf("%w: unknown file type %q", ErrInvalid, source)
	}
	if spec.NeedDate && !ledger.ValidDate(date) {
		return ingest.Result{}, invalid("%s needs the business date (YYYY-MM-DD)", source)
	}
	if err := o.require(ctx, tenant, actor, "Loading files", RoleMaker, RoleChecker, RoleRisk, RoleCompliance); err != nil {
		return ingest.Result{}, err
	}
	sum := sha256.Sum256(data)
	rf, err := o.App.Ingest.Raw.Put(ctx, ingest.RawFile{Tenant: tenant, Source: source, Name: name, SHA256: hex.EncodeToString(sum[:]), Content: data})
	if err != nil {
		return ingest.Result{}, err
	}
	res := ingest.Result{File: rf, Adapter: source + "@1.0", Errors: []ingest.ParseError{}}
	rows, perrs := parseCSV(data, spec.Columns)
	res.Errors = append(res.Errors, perrs...)
	var apply func() (int, int, error)
	if len(perrs) == 0 {
		apply, res.Errors = o.prepare(ctx, tenant, actor, source, date, rows)
	}
	logDetail := map[string]any{"source": source, "name": name, "parsed": len(rows), "row_errors": len(res.Errors)}
	if len(res.Errors) > 0 {
		err := fmt.Errorf("%w: %d row error(s) in %s", ingest.ErrRejected, len(res.Errors), source)
		logDetail["error"] = err.Error()
		o.log(ctx, tenant, actor, "file.rejected", rf.ID, logDetail)
		return res, err
	}
	res.Parsed = len(rows)
	res.Added, res.DuplicateRows, err = apply()
	logDetail["added"], logDetail["duplicates"] = res.Added, res.DuplicateRows
	if err != nil {
		logDetail["error"] = err.Error()
		o.log(ctx, tenant, actor, "file.rejected", rf.ID, logDetail)
		return res, err
	}
	o.log(ctx, tenant, actor, "file.ingested", rf.ID, logDetail)
	return res, nil
}

func parseCSV(data []byte, cols []string) ([]row, []ingest.ParseError) {
	r := csv.NewReader(bytes.NewReader(data))
	r.FieldsPerRecord = -1
	r.TrimLeadingSpace = true
	h, err := r.Read()
	if err != nil {
		return nil, []ingest.ParseError{{Line: 1, Msg: "cannot read the header row: " + err.Error()}}
	}
	idx := map[string]int{}
	for i, c := range h {
		idx[strings.ToLower(strings.TrimSpace(strings.TrimPrefix(c, "\ufeff")))] = i
	}
	var missing []string
	for _, c := range cols {
		if _, ok := idx[strings.ToLower(c)]; !ok {
			missing = append(missing, c)
		}
	}
	if len(missing) > 0 {
		return nil, []ingest.ParseError{{Line: 1, Msg: "header is missing column(s) " + strings.Join(missing, ", ") + "; expected " + strings.Join(cols, ",")}}
	}
	var rows []row
	var errs []ingest.ParseError
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		line, _ := r.FieldPos(0)
		if err != nil {
			errs = append(errs, ingest.ParseError{Line: line, Msg: err.Error()})
			continue
		}
		if len(rec) == 1 && strings.TrimSpace(rec[0]) == "" {
			continue
		}
		fields := append([]string(nil), rec...)
		rows = append(rows, row{line: line, get: func(c string) string {
			i, ok := idx[strings.ToLower(c)]
			if !ok || i >= len(fields) {
				return ""
			}
			return strings.TrimSpace(fields[i])
		}})
	}
	return rows, errs
}

// prepare validates every row and returns a function that applies them.
func (o *Ops) prepare(ctx context.Context, tenant, actor, source, date string, rows []row) (func() (int, int, error), []ingest.ParseError) {
	var errs []ingest.ParseError
	bad := func(l int, f, m string, a ...any) {
		errs = append(errs, ingest.ParseError{Line: l, Field: f, Msg: fmt.Sprintf(m, a...)})
	}
	pint := func(r row, f string, allowZero bool) int64 {
		v, err := strconv.ParseInt(r.get(f), 10, 64)
		if err != nil || v < 0 || (!allowZero && v == 0) {
			bad(r.line, f, "must be a whole number%s, got %q", map[bool]string{true: "", false: " above zero"}[allowZero], r.get(f))
		}
		return v
	}
	pdec := func(r row, f string, optional bool) money.Dec {
		s := r.get(f)
		if s == "" && optional {
			return money.Dec{}
		}
		v, err := money.ParseDec(s)
		if err != nil {
			bad(r.line, f, "must be a number, got %q", s)
		}
		return v
	}
	pdate := func(r row, f string, optional bool) string {
		s := r.get(f)
		if s == "" && optional {
			return ""
		}
		if !ledger.ValidDate(s) {
			bad(r.line, f, "must be YYYY-MM-DD, got %q", s)
		}
		return s
	}
	switch source {
	case "CLIENT-MASTER":
		var cs []Client
		seen := map[string]bool{}
		for _, r := range rows {
			c := Client{UCC: r.get("UCC"), Name: r.get("Name"), Type: r.get("Type"), PAN: r.get("PAN"), DOB: r.get("DOB"), Mobile: r.get("Mobile"), Email: r.get("Email"),
				City: r.get("City"), Segments: strings.Split(r.get("Segments"), ";"), Risk: r.get("Risk"), Income: r.get("Income"), RAS: r.get("RAS"),
				Bank: BankAccount{r.get("Bank"), r.get("IFSC"), r.get("Account")}, Demat: Demat{r.get("Depository"), r.get("DematID")},
				Nominee: r.get("Nominee"), NomineeName: r.get("NomineeName"), KYCDate: pdate(r, "KYCDate", true), Status: r.get("Status"),
				API: strings.EqualFold(r.get("API"), "Y"), LastTrade: pdate(r, "LastTrade", true), IncomeProof: true, RiskDisclosure: true, CreatedBy: "import",
				Branch: r.get("Branch"), State: r.get("State"), GSTIN: strings.ToUpper(r.get("GSTIN"))}
			if c.GSTIN != "" && !reGSTIN.MatchString(c.GSTIN) {
				bad(r.line, "GSTIN", "is not a valid GSTIN")
			}
			if v := r.get("UPIBlock"); v != "" {
				a, err := money.ParseAmount(v)
				if err != nil {
					bad(r.line, "UPIBlock", "must be rupees with at most 2 decimals")
				}
				c.UPIBlock = a
			}
			c.Normalise()
			if c.UCC == "" {
				bad(r.line, "UCC", "is required")
			}
			if seen[c.UCC] {
				bad(r.line, "UCC", "%s appears twice in the file", c.UCC)
			}
			seen[c.UCC] = true
			okStage := false
			for _, s := range Stages {
				okStage = okStage || s == c.Status
			}
			if !okStage {
				bad(r.line, "Status", "must be one of %s", strings.Join(Stages, ", "))
			}
			if err := c.Validate(); err != nil {
				var ve *ValidationError
				if errors.As(err, &ve) {
					for _, f := range ve.Fields {
						bad(r.line, f.Field, "%s", f.Msg)
					}
				}
			}
			cs = append(cs, c)
		}
		return func() (int, int, error) {
			n := 0
			for _, c := range cs {
				// Groups, blocks, inactivity and other back-office fields are
				// kept when a re-import does not carry them.
				if old, _, err := docs.Get[Client](ctx, o.Docs, tenant, cClient, c.UCC); err == nil {
					if len(c.Tags) == 0 {
						c.Tags = old.Tags
					}
					c.Blocked, c.BlockedReason = old.Blocked, old.BlockedReason
					c.Inactive, c.InactiveSince, c.ReactivatedOn, c.KYCUpdatedOn = old.Inactive, old.InactiveSince, old.ReactivatedOn, old.KYCUpdatedOn
					if c.Branch == "" {
						c.Branch = old.Branch
					}
					if c.State == "" {
						c.State = old.State
					}
					if c.GSTIN == "" {
						c.GSTIN = old.GSTIN
					}
				}
				if err := docs.Upsert(ctx, o.Docs, tenant, cClient, c.UCC, c, actor); err != nil {
					return n, 0, err
				}
				n++
			}
			return n, 0, nil
		}, errs
	case "OPENING-BALANCES":
		var ps []ledger.Posting
		type agedDebit struct {
			acct  string
			amt   money.Amount
			since string
		}
		var aged []agedDebit
		for _, r := range rows {
			acct := strings.ToUpper(r.get("Account"))
			if _, err := ledger.TypeOf(acct); err != nil {
				bad(r.line, "Account", "%v", err)
			}
			var dr, cr money.Amount
			if v := r.get("Debit"); v != "" {
				a, err := money.ParseAmount(v)
				if err != nil || a < 0 {
					bad(r.line, "Debit", "must be rupees ≥ 0 with at most 2 decimals")
				}
				dr = a
			}
			if v := r.get("Credit"); v != "" {
				a, err := money.ParseAmount(v)
				if err != nil || a < 0 {
					bad(r.line, "Credit", "must be rupees ≥ 0 with at most 2 decimals")
				}
				cr = a
			}
			if (dr > 0) == (cr > 0) {
				bad(r.line, "Debit", "exactly one of Debit or Credit must be above zero")
			}
			ps = append(ps, ledger.Posting{Account: acct, Debit: dr, Credit: cr, Memo: "Opening balance"})
			if v := r.get("Since"); v != "" {
				switch {
				case !ledger.ValidDate(v) || v > date:
					bad(r.line, "Since", "must be a date (YYYY-MM-DD) on or before the opening date")
				case !strings.HasPrefix(acct, "CLIENT:") || dr == 0:
					bad(r.line, "Since", "only a client debit balance can carry a due date")
				case v < date:
					aged = append(aged, agedDebit{acct, dr, v})
				}
			}
		}
		var td, tc money.Amount
		for _, p := range ps {
			td += p.Debit
			tc += p.Credit
		}
		if len(errs) == 0 && td != tc {
			bad(0, "", "opening balances do not balance: debits %s, credits %s", td, tc)
		}
		return func() (int, int, error) {
			h := sha256.New()
			for _, p := range ps {
				fmt.Fprintf(h, "%s|%d|%d;", p.Account, p.Debit, p.Credit)
			}
			key := "OPEN:" + date + ":" + hex.EncodeToString(h.Sum(nil))[:16]
			// A client debit with a due date is brought in on that date
			// against opening equity, and the main entry carries the equity
			// leg instead, so the go-live position is unchanged.
			for i, a := range aged {
				if _, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: tenant, Key: fmt.Sprintf("%s:aged:%d", key, i), ValueDate: a.since,
					Narration: "Opening debit balance due since " + a.since, Source: ledger.SourceRef{Kind: "OPENING", ID: date},
					Postings: []ledger.Posting{{Account: a.acct, Debit: a.amt, Memo: "Opening debit (aged)"}, {Account: ledger.OpeningEquity, Credit: a.amt, Memo: "Opening debit (aged)"}}, CreatedBy: actor}); err != nil {
					return 0, 0, err
				}
				for j := range ps {
					if ps[j].Account == a.acct && ps[j].Debit == a.amt {
						ps[j].Account, ps[j].Memo = ledger.OpeningEquity, "Opening debit of "+a.acct+" brought in on "+a.since
						break
					}
				}
			}
			_, created, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: tenant, Key: key, ValueDate: date,
				Narration: "Opening balances", Source: ledger.SourceRef{Kind: "OPENING", ID: date}, Postings: ps, CreatedBy: actor})
			if err != nil {
				return 0, 0, err
			}
			if !created {
				return 0, len(ps), nil
			}
			return len(ps), 0, nil
		}, errs
	case "MARKET-DATA":
		m := Market{Date: date}
		for _, r := range rows {
			i := Instrument{Exchange: strings.ToUpper(r.get("Exchange")), Segment: strings.ToUpper(r.get("Segment")), Symbol: strings.ToUpper(r.get("Symbol")),
				Type: strings.ToUpper(r.get("Type")), Expiry: pdate(r, "Expiry", true), ISIN: strings.ToUpper(r.get("ISIN")), Company: r.get("Company"),
				Lot: pint(r, "Lot", false), LTP: pdec(r, "LTP", false), Close: pdec(r, "Close", false), VaRPct: pdec(r, "VaRPct", true), ELMPct: pdec(r, "ELMPct", true),
				HaircutPct: pdec(r, "HaircutPct", true), SPANPct: pdec(r, "SPANPct", true), ExpoPct: pdec(r, "ExposurePct", true), Delta: pdec(r, "Delta", true),
				Underlying: pdec(r, "Underlying", true), Index: strings.EqualFold(r.get("Index"), "Y")}
			if k := r.get("Strike"); k != "" {
				i.Strike = pdec(r, "Strike", false).String()
			}
			switch i.Type {
			case "EQ", "FUT", "CE", "PE":
			default:
				bad(r.line, "Type", "must be EQ, FUT, CE or PE")
			}
			if i.Type != "EQ" && i.Expiry == "" {
				bad(r.line, "Expiry", "derivatives need an expiry")
			}
			if i.Type == "EQ" && i.ISIN == "" {
				bad(r.line, "ISIN", "equities need an ISIN")
			}
			if SegmentNames[i.Segment] == "" {
				bad(r.line, "Segment", "must be CM, FO, CD or COM")
			}
			m.Instruments = append(m.Instruments, i)
		}
		return func() (int, int, error) {
			return len(m.Instruments), 0, docs.Upsert(ctx, o.Docs, tenant, cMarket, date, m, actor)
		}, errs
	case "DP-HOLDINGS":
		var hs []Holding
		for _, r := range rows {
			h := Holding{UCC: strings.ToUpper(r.get("UCC")), ISIN: strings.ToUpper(r.get("ISIN")), Symbol: strings.ToUpper(r.get("Symbol")), Qty: pint(r, "Qty", true), Pledged: pint(r, "Pledged", true), AvgCost: pdec(r, "AvgCost", true)}
			if h.Pledged > h.Qty {
				bad(r.line, "Pledged", "cannot exceed Qty")
			}
			hs = append(hs, h)
		}
		return func() (int, int, error) {
			for _, h := range hs {
				if err := docs.Upsert(ctx, o.Docs, tenant, cHolding, holdingID(h.UCC, h.ISIN), h, actor); err != nil {
					return 0, 0, err
				}
			}
			return len(hs), 0, nil
		}, errs
	case "HOLDING-LOTS":
		books := map[string]*LotBook{}
		var order []string
		for _, r := range rows {
			u := strings.ToUpper(r.get("UCC"))
			l := Lot{ISIN: strings.ToUpper(r.get("ISIN")), Symbol: strings.ToUpper(r.get("Symbol")), Qty: pint(r, "Qty", false), Date: pdate(r, "BuyDate", false), Price: pdec(r, "BuyPrice", false)}
			if u == "" {
				bad(r.line, "UCC", "is required")
			}
			if l.ISIN == "" {
				bad(r.line, "ISIN", "is required")
			}
			if l.Price.Sign() <= 0 {
				bad(r.line, "BuyPrice", "must be above zero")
			}
			if l.Date > date {
				bad(r.line, "BuyDate", "%s is after the as-at date %s", l.Date, date)
			}
			b := books[u]
			if b == nil {
				b = &LotBook{UCC: u, AsOf: date}
				books[u] = b
				order = append(order, u)
			}
			b.Lots = append(b.Lots, l)
		}
		return func() (int, int, error) {
			n := 0
			for _, u := range order {
				b := books[u]
				sort.SliceStable(b.Lots, func(i, j int) bool { return b.Lots[i].Date < b.Lots[j].Date })
				if err := docs.Upsert(ctx, o.Docs, tenant, cLot, u, *b, actor); err != nil {
					return n, 0, err
				}
				n += len(b.Lots)
			}
			return n, 0, nil
		}, errs
	case "POSITIONS":
		var ps []Position
		for _, r := range rows {
			typ := strings.ToUpper(r.get("Type"))
			strike := ""
			if r.get("Strike") != "" {
				strike = pdec(r, "Strike", false).String()
			}
			q, err := strconv.ParseInt(r.get("Qty"), 10, 64)
			if err != nil || q == 0 {
				bad(r.line, "Qty", "must be a non-zero whole number (negative for short)")
			}
			p := Position{Date: date, UCC: strings.ToUpper(r.get("UCC")), Exchange: strings.ToUpper(r.get("Exchange")), Segment: strings.ToUpper(r.get("Segment")),
				Instrument: strings.ToUpper(r.get("Symbol")) + "|" + typ + "|" + pdate(r, "Expiry", false) + "|" + strike, Qty: q, AvgPrice: pdec(r, "AvgPrice", false)}
			ps = append(ps, p)
		}
		return func() (int, int, error) {
			for _, p := range ps {
				if err := docs.Upsert(ctx, o.Docs, tenant, cPosition, date+"|"+p.UCC+"|"+p.Exchange+"|"+p.Segment+"|"+p.Instrument, p, actor); err != nil {
					return 0, 0, err
				}
			}
			return len(ps), 0, nil
		}, errs
	case "PLEDGE-REQUESTS":
		var ps []PledgeRequest
		for _, r := range rows {
			ps = append(ps, PledgeRequest{ID: r.get("RequestID"), UCC: strings.ToUpper(r.get("UCC")), ISIN: strings.ToUpper(r.get("ISIN")), Symbol: strings.ToUpper(r.get("Symbol")), Qty: pint(r, "Qty", false), Time: r.get("Time"), Status: "Pending"})
			if r.get("RequestID") == "" {
				bad(r.line, "RequestID", "is required")
			}
		}
		return func() (int, int, error) {
			n, d := 0, 0
			for _, p := range ps {
				if _, err := docs.Put(ctx, o.Docs, tenant, cPledge, p.ID, p, 0, actor); errors.Is(err, docs.ErrStale) {
					d++
				} else if err != nil {
					return n, d, err
				} else {
					n++
				}
			}
			return n, d, nil
		}, errs
	case "BANK-STMT":
		var ts []BankTxn
		for _, r := range rows {
			amt, err := money.ParseAmount(r.get("Amount"))
			if err != nil || amt <= 0 {
				bad(r.line, "Amount", "must be rupees above zero with at most 2 decimals")
			}
			dir := strings.ToUpper(r.get("DrCr"))
			if dir != "CR" && dir != "DR" {
				bad(r.line, "DrCr", "must be CR or DR")
			}
			if r.get("Reference") == "" {
				bad(r.line, "Reference", "is required")
			}
			if dir == "CR" {
				ts = append(ts, BankTxn{ID: r.get("Reference"), Date: pdate(r, "Date", false), Time: r.get("Time"), Mode: strings.ToUpper(r.get("Mode")), Ref: r.get("Reference"), Remitter: r.get("Remitter"), Account: r.get("Account"), Amount: amt})
			}
		}
		return func() (int, int, error) {
			clients, err := o.Clients(ctx, tenant)
			if err != nil {
				return 0, 0, err
			}
			n, d := 0, 0
			for _, t := range ts {
				if _, _, err := docs.Get[BankTxn](ctx, o.Docs, tenant, cBankTxn, t.ID); err == nil {
					d++
					continue
				}
				t, err := o.matchCredit(ctx, tenant, actor, t, clients)
				if err != nil {
					return n, d, err
				}
				if _, err := docs.Put(ctx, o.Docs, tenant, cBankTxn, t.ID, t, 0, actor); err != nil {
					return n, d, err
				}
				n++
			}
			return n, d, nil
		}, errs
	case "SURV-ALERTS":
		var as []Alert
		for _, r := range rows {
			sev := r.get("Severity")
			if sevRank[sev] == 0 && sev != "High" {
				bad(r.line, "Severity", "must be High, Medium or Low")
			}
			as = append(as, Alert{ID: r.get("AlertID"), Source: strings.ToUpper(r.get("Exchange")), Type: r.get("Type"), UCC: strings.ToUpper(r.get("UCC")), Detail: r.get("Detail"), Severity: sev})
		}
		return func() (int, int, error) {
			n, d := 0, 0
			for _, a := range as {
				if o.raiseAlert(ctx, tenant, actor, a, a.Source+":"+a.ID) {
					n++
				} else {
					d++
				}
			}
			return n, d, nil
		}, errs
	case "IPO-BIDS":
		var as []IPOApp
		for _, r := range rows {
			amt, err := money.ParseAmount(r.get("Amount"))
			if err != nil {
				bad(r.line, "Amount", "must be rupees")
			}
			m := r.get("Mandate")
			if m != "Accepted" && m != "Pending" && m != "Declined" {
				bad(r.line, "Mandate", "must be Accepted, Pending or Declined")
			}
			as = append(as, IPOApp{ID: r.get("ApplicationNo"), UCC: strings.ToUpper(r.get("UCC")), Issue: r.get("Issue"), Lots: pint(r, "Lots", false), Amount: amt, UPI: r.get("UPI"), Mandate: m, Allotment: "Awaiting", Date: o.fileDate(date)})
		}
		return func() (int, int, error) {
			cm, _ := o.ClientMap(ctx, tenant)
			n, d := 0, 0
			for _, a := range as {
				a.Name = cm[a.UCC].Name
				if _, err := docs.Put(ctx, o.Docs, tenant, cIPO, a.ID, a, 0, actor); errors.Is(err, docs.ErrStale) {
					d++
				} else if err != nil {
					return n, d, err
				} else {
					n++
				}
			}
			return n, d, nil
		}, errs
	case "IPO-ALLOTMENT":
		type al struct {
			id   string
			lots int64
		}
		var as []al
		for _, r := range rows {
			as = append(as, al{r.get("ApplicationNo"), pint(r, "AllottedLots", true)})
		}
		return func() (int, int, error) {
			n, d := 0, 0
			for _, a := range as {
				_, err := docs.Update(ctx, o.Docs, tenant, cIPO, a.id, actor, func(x *IPOApp) error {
					if x.Allotment != "Awaiting" {
						return errAlreadyDone
					}
					switch {
					case x.Mandate != "Accepted":
						x.Allotment = "Rejected · mandate not accepted"
					case a.lots > 0:
						x.Allotment, x.Allotted = fmt.Sprintf("Allotted %d lot(s)", a.lots), a.lots
					default:
						x.Allotment = "Not allotted · block released"
					}
					return nil
				})
				switch {
				case errors.Is(err, errAlreadyDone):
					d++
				case errors.Is(err, docs.ErrNotFound):
					return n, d, fmt.Errorf("%w: application %s is not on record", ErrInvalid, a.id)
				case err != nil:
					return n, d, err
				default:
					n++
				}
			}
			return n, d, nil
		}, errs
	case "MF-ORDERS":
		var ms []MFOrder
		for _, r := range rows {
			amt, err := money.ParseAmount(r.get("Amount"))
			if err != nil {
				bad(r.line, "Amount", "must be rupees")
			}
			ms = append(ms, MFOrder{ID: r.get("OrderNo"), UCC: strings.ToUpper(r.get("UCC")), Scheme: r.get("Scheme"), Type: r.get("Type"), Amount: amt, Platform: r.get("Platform"), Status: r.get("Status"), Date: o.fileDate(date)})
		}
		return func() (int, int, error) {
			cm, _ := o.ClientMap(ctx, tenant)
			for _, m := range ms {
				m.Name = cm[m.UCC].Name
				if err := docs.Upsert(ctx, o.Docs, tenant, cMF, m.ID, m, actor); err != nil {
					return 0, 0, err
				}
			}
			return len(ms), 0, nil
		}, errs
	}
	return func() (int, int, error) { return 0, 0, nil }, []ingest.ParseError{{Msg: "no loader for " + source}}
}

var errAlreadyDone = errors.New("already applied")
