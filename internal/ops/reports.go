package ops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/money"
)

// ReportDef describes a regulatory or client report.
type ReportDef struct {
	Code   string `json:"code"`
	Name   string `json:"name"`
	To     string `json:"to"`
	Freq   string `json:"frequency"`
	Due    string `json:"due"`
	Format string `json:"format"`
}

// ReportStatus is a report's state for a date.
type ReportStatus struct {
	ReportDef
	Date      string `json:"date"`
	Status    string `json:"status"` // Not generated, Generated, Validated, Uploaded
	Rows      int    `json:"rows"`
	SHA256    string `json:"sha256,omitempty"`
	By        string `json:"by,omitempty"`
	At        string `json:"at,omitempty"`
	Ack       string `json:"ack,omitempty"`
	Available bool   `json:"available"`
}

// ReportDefs lists the reports the system generates. Column layouts are
// placeholders until each exchange's current specification is loaded.
func ReportDefs() []ReportDef {
	return []ReportDef{
		{"funds_balance", "Client funds and securities balance", "NSE, BSE, MCX", "Daily", "T 23:59", "CSV (sample layout)"},
		{"margin", "Client-wise margin collection (peak and EOD)", "Clearing corporations", "Daily", "T+1 18:00", "CSV (sample layout)"},
		{"holdings", "Weekly holding statement", "Clients", "Weekly", "Friday 23:59", "CSV"},
		{"ucc", "UCC upload batch", "NSE, BSE, MCX", "On change", "Before first trade", "CSV (sample layout)"},
		{"ccm", "Client code modification log", "Exchanges", "Daily", "T 16:00", "CSV (sample layout)"},
		{"ras", "Running account settlement statement", "Clients", "Monthly / quarterly", "Settlement date", "CSV"},
		{"bills", "Bills and charges register", "Internal audit", "Daily", "T", "CSV"},
		{"trades", "Trade register", "Internal audit", "Daily", "T", "CSV"},
		{"alerts", "Surveillance alert register", "Exchanges / FIU-IND", "Monthly", "7th of next month", "CSV"},
		{"register_securities", "Register of securities (client-wise, with the firm and in demat)", "Inspection", "On demand", "—", "CSV"},
		{"client_register", "Client register in exchange format", "Inspection", "On demand", "—", "CSV"},
		{"ledger_balances", "Client ledger balances with days in debit", "Inspection", "On demand", "—", "CSV"},
		{"no_ras_auth", "Clients without a running account authorisation", "Inspection", "On demand", "—", "CSV"},
		{"kyc_registry", "KRA, CKYC and periodic KYC status", "Inspection / compliance", "On demand", "—", "CSV"},
	}
}

func reportDef(code string) (ReportDef, bool) {
	for _, d := range ReportDefs() {
		if d.Code == code {
			return d, true
		}
	}
	return ReportDef{}, false
}

// Reports lists every report with its status for a date.
func (o *Ops) Reports(ctx context.Context, tenant, date string) ([]ReportStatus, error) {
	st, err := docs.List[ReportStatus](ctx, o.Docs, tenant, cReport, date+"|")
	if err != nil {
		return nil, err
	}
	m := map[string]ReportStatus{}
	for _, s := range st {
		m[s.Code] = s
	}
	var out []ReportStatus
	for _, d := range ReportDefs() {
		s, ok := m[d.Code]
		if !ok {
			s = ReportStatus{ReportDef: d, Date: date, Status: "Not generated"}
		}
		s.ReportDef = d
		s.Available = true
		out = append(out, s)
	}
	return out, nil
}

// ReportRows builds a report's rows (header first).
func (o *Ops) ReportRows(ctx context.Context, tenant, date, code string) ([][]string, error) {
	clients, err := o.ClientMap(ctx, tenant)
	if err != nil {
		return nil, err
	}
	switch code {
	case "funds_balance":
		rv, err := o.Risk(ctx, tenant, date)
		if err != nil {
			return nil, err
		}
		rows := [][]string{{"TradeDate", "UCC", "PAN", "LedgerBalance", "UPIBlocked", "CollateralAfterHaircut", "MarginRequired", "FreeBalance"}}
		hs, _ := o.Holdings(ctx, tenant, "")
		held := map[string]bool{}
		for _, h := range hs {
			if h.Qty > 0 {
				held[h.UCC] = true
			}
		}
		for _, r := range rv.Rows {
			// Inactive clients with nil funds and securities are exempt.
			if r.Inactive && r.Funds.Sign() == 0 && !held[r.UCC] {
				continue
			}
			rows = append(rows, []string{date, r.UCC, clients[r.UCC].PAN, r.Funds.Fixed(2), r.UPI.Fixed(2), r.Collateral.Fixed(2), r.Required.Fixed(2), r.Free.Fixed(2)})
		}
		return rows, nil
	case "margin":
		m, err := o.Margins(ctx, tenant, date)
		if err != nil {
			return nil, err
		}
		rows := [][]string{{"TradeDate", "UCC", "PAN", "PeakMarginRequired", "PeakAt", "EODMarginRequired", "MarginCollected", "ShortCollection", "Penalty"}}
		for _, r := range m.Rows {
			rows = append(rows, []string{date, r.UCC, clients[r.UCC].PAN, r.Peak.Fixed(2), r.PeakAt, r.EOD.Fixed(2), r.Collected.Fixed(2), r.Short.Fixed(2), r.Penalty.Fixed(2)})
		}
		return rows, nil
	case "holdings":
		hs, err := o.Holdings(ctx, tenant, "")
		if err != nil {
			return nil, err
		}
		rows := [][]string{{"UCC", "Name", "ISIN", "Symbol", "Quantity", "Pledged"}}
		for _, h := range hs {
			rows = append(rows, []string{h.UCC, clients[h.UCC].Name, h.ISIN, h.Symbol, fmt.Sprint(h.Qty), fmt.Sprint(h.Pledged)})
		}
		return rows, nil
	case "ucc":
		rows := [][]string{{"UCC", "Name", "PAN", "Category", "Segments", "Mobile", "Email", "Status"}}
		for _, u := range sortedKeys(clients) {
			c := clients[u]
			switch {
			case c.Status == "UCC upload" || c.KYCDate == date:
				rows = append(rows, []string{c.UCC, c.Name, c.PAN, c.Type, strings.Join(c.Segments, "/"), c.Mobile, c.Email, c.Status})
			case c.Inactive && c.InactiveSince == date:
				rows = append(rows, []string{c.UCC, c.Name, c.PAN, c.Type, strings.Join(c.Segments, "/"), c.Mobile, c.Email, "Inactive"})
			case !c.Inactive && c.ReactivatedOn == date:
				rows = append(rows, []string{c.UCC, c.Name, c.PAN, c.Type, strings.Join(c.Segments, "/"), c.Mobile, c.Email, "Active (reactivated)"})
			}
		}
		return rows, nil
	case "ccm":
		cs, err := o.CCMs(ctx, tenant, date)
		if err != nil {
			return nil, err
		}
		rows := [][]string{{"TradeDate", "Exchange", "Segment", "TradeNo", "OriginalUCC", "ModifiedUCC", "Reason", "ModifiedBy", "Time"}}
		for _, c := range cs {
			rows = append(rows, []string{c.Date, c.Exchange, c.Segment, c.TradeNo, c.From, c.To, c.Reason, c.By, c.At})
		}
		return rows, nil
	case "ras":
		v, err := o.RAS(ctx, tenant, date)
		if err != nil {
			return nil, err
		}
		rows := [][]string{{"UCC", "Name", "Preference", "LastTrade", "CreditBalance", "Retention", "Payout", "SettlesOn", "Settled"}}
		for _, r := range v.Rows {
			rows = append(rows, []string{r.UCC, r.Name, r.Preference, r.LastTrade, r.Credit.Fixed(2), r.Retention.Fixed(2), r.Payout.Fixed(2), r.SettlesOn, fmt.Sprint(r.Settled)})
		}
		return rows, nil
	case "bills":
		es, err := o.App.Ledger.Store.List(ctx, tenant, ledgerFilterDate(date))
		if err != nil {
			return nil, err
		}
		rows := [][]string{{"EntryID", "Key", "Narration", "Account", "Debit", "Credit", "Memo"}}
		for _, e := range es {
			if e.Source.Kind != "BILL" && e.Source.Kind != "REVERSAL" {
				continue
			}
			for _, p := range e.Postings {
				rows = append(rows, []string{e.ID, e.Key, e.Narration, p.Account, p.Debit.String(), p.Credit.String(), p.Memo})
			}
		}
		return rows, nil
	case "trades":
		ts, err := o.exchangeTrades(ctx, tenant, date)
		if err != nil {
			return nil, err
		}
		sort.Slice(ts, func(i, j int) bool { return ts[i].TradeTime < ts[j].TradeTime })
		rows := [][]string{{"Exchange", "Segment", "TradeNo", "OrderNo", "Time", "UCC", "Contract", "Side", "Qty", "Price", "Product", "AlgoID", "File", "Line"}}
		for _, t := range ts {
			rows = append(rows, []string{t.Exchange, t.Segment, t.TradeNo, t.OrderNo, t.TradeTime, t.UCC, InstrumentName(t.Symbol, t.InstrumentType, t.Expiry, t.Strike), t.Side, fmt.Sprint(t.Qty), t.Price.String(), t.Product, t.AlgoID, t.FileID, fmt.Sprint(t.Line)})
		}
		return rows, nil
	case "alerts":
		as, err := o.Alerts(ctx, tenant)
		if err != nil {
			return nil, err
		}
		rows := [][]string{{"AlertID", "Date", "Source", "Type", "UCC", "Severity", "Status", "Detail", "Notes"}}
		for _, a := range as {
			rows = append(rows, []string{a.ID, a.Date, a.Source, a.Type, a.UCC, a.Severity, a.Status, a.Detail, strings.Join(a.Notes, " | ")})
		}
		return rows, nil
	case "register_securities":
		hs, err := o.Holdings(ctx, tenant, "")
		if err != nil {
			return nil, err
		}
		rows := [][]string{{"Date", "UCC", "Name", "ISIN", "Symbol", "DematQty", "PledgedToFirm", "HeldInCUSPA"}}
		cu, _ := docs.List[CUSPAItem](ctx, o.Docs, tenant, cCUSPA, "")
		held := map[string]int64{}
		for _, it := range cu {
			if it.Status == "Held in CUSPA" {
				held[holdingID(it.UCC, it.ISIN)] += it.Qty
			}
		}
		seen := map[string]bool{}
		for _, h := range hs {
			id := holdingID(h.UCC, h.ISIN)
			seen[id] = true
			rows = append(rows, []string{date, h.UCC, clients[h.UCC].Name, h.ISIN, h.Symbol, fmt.Sprint(h.Qty), fmt.Sprint(h.Pledged), fmt.Sprint(held[id])})
		}
		for _, it := range cu {
			if id := holdingID(it.UCC, it.ISIN); it.Status == "Held in CUSPA" && !seen[id] {
				seen[id] = true
				rows = append(rows, []string{date, it.UCC, clients[it.UCC].Name, it.ISIN, it.Symbol, "0", "0", fmt.Sprint(held[id])})
			}
		}
		return rows, nil
	case "client_register":
		rows := [][]string{{"UCC", "Name", "Category", "PAN", "DOB", "City", "State", "Mobile", "Email", "Bank", "IFSC", "BankAccount", "Depository", "DematID", "Segments", "KYCDate", "Nomination", "Status", "Branch", "AP"}}
		for _, u := range sortedKeys(clients) {
			c := clients[u]
			st := c.Status
			if c.Inactive {
				st = "Inactive"
			}
			rows = append(rows, []string{c.UCC, c.Name, c.Type, c.PAN, c.DOB, c.City, c.State, c.Mobile, c.Email, c.Bank.Bank, c.Bank.IFSC, MaskAccount(c.Bank.Account), c.Demat.Depository, c.Demat.ID, strings.Join(c.Segments, "/"), c.KYCDate, c.Nominee, st, c.Branch, c.Partner})
		}
		return rows, nil
	case "ledger_balances":
		funds, err := o.clientFunds(ctx, tenant, date, "")
		if err != nil {
			return nil, err
		}
		deb, _ := o.Debits(ctx, tenant, date)
		days := map[string]int{}
		for _, r := range deb.Rows {
			days[r.UCC] = r.Days
		}
		rows := [][]string{{"Date", "UCC", "Name", "Balance", "DrCr", "DaysInDebit"}}
		for _, u := range sortedKeys(clients) {
			f := funds[u]
			if f == 0 {
				continue
			}
			dc, amt := "Cr", f
			if f < 0 {
				dc, amt = "Dr", -f
			}
			rows = append(rows, []string{date, u, clients[u].Name, amt.String(), dc, fmt.Sprint(days[u])})
		}
		return rows, nil
	case "no_ras_auth":
		rows := [][]string{{"UCC", "Name", "PAN", "Status", "RunningAccountPreference"}}
		for _, u := range sortedKeys(clients) {
			c := clients[u]
			if c.RAS != "Monthly" && c.RAS != "Quarterly" {
				rows = append(rows, []string{u, c.Name, MaskPAN(c.PAN), c.Status, c.RAS})
			}
		}
		return rows, nil
	case "kyc_registry":
		v, err := o.PMLA(ctx, tenant, date)
		if err != nil {
			return nil, err
		}
		rows := [][]string{{"UCC", "Name", "Risk", "SpecialCategories", "KYCDate", "ReKYCDue", "ReKYCState", "KRAStatus", "CKYC"}}
		for _, r := range v.Rows {
			rows = append(rows, []string{r.UCC, r.Name, r.Risk, strings.Join(r.CSC, "; "), r.KYCDate, r.ReKYCDue, r.ReKYCState, r.KRAStatus, r.CKYC})
		}
		return rows, nil
	}
	return nil, fmt.Errorf("%w: unknown report %q", ErrNotFound, code)
}

// CSV renders rows as CSV bytes.
func CSV(rows [][]string) []byte {
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	_ = w.WriteAll(rows)
	return b.Bytes()
}

// ReportStep moves a report through generate → validate → upload.
func (o *Ops) ReportStep(ctx context.Context, tenant, actor, date, code, step, ack string) (ReportStatus, error) {
	d, ok := reportDef(code)
	if !ok {
		return ReportStatus{}, fmt.Errorf("%w: unknown report %q", ErrNotFound, code)
	}
	if step == "upload" {
		if err := o.require(ctx, tenant, actor, "Marking a report uploaded", RoleCompliance, RoleChecker); err != nil {
			return ReportStatus{}, err
		}
	} else if err := o.require(ctx, tenant, actor, "Generating reports", RoleMaker, RoleChecker, RoleCompliance, RoleRisk); err != nil {
		return ReportStatus{}, err
	}
	cur, _, err := docs.GetOr(ctx, o.Docs, tenant, cReport, date+"|"+code, ReportStatus{ReportDef: d, Date: date, Status: "Not generated"})
	if err != nil {
		return cur, err
	}
	switch step {
	case "generate", "validate":
		rows, err := o.ReportRows(ctx, tenant, date, code)
		if err != nil {
			return cur, err
		}
		sum := sha256.Sum256(CSV(rows))
		cur.Rows, cur.SHA256 = len(rows)-1, hex.EncodeToString(sum[:])
		cur.Status = "Generated"
		if step == "validate" {
			// Structural validation: every row has the header's width and
			// no empty client codes.
			for i, r := range rows {
				if len(r) != len(rows[0]) {
					return cur, invalid("row %d has %d columns, expected %d", i, len(r), len(rows[0]))
				}
			}
			cur.Status = "Validated"
		}
	case "upload":
		if cur.Status != "Validated" {
			return cur, fmt.Errorf("%w: validate the report before marking it uploaded", ErrState)
		}
		if strings.TrimSpace(ack) == "" {
			return cur, invalid("enter the exchange acknowledgement number")
		}
		cur.Status, cur.Ack = "Uploaded", ack
	default:
		return cur, invalid("unknown step %q", step)
	}
	cur.ReportDef, cur.Date, cur.By, cur.At = d, date, actor, o.now().Format("15:04")
	if err := docs.Upsert(ctx, o.Docs, tenant, cReport, date+"|"+code, cur, actor); err != nil {
		return cur, err
	}
	o.log(ctx, tenant, actor, "report."+step, code, map[string]any{"date": date, "rows": cur.Rows, "sha256": cur.SHA256, "ack": ack})
	return cur, nil
}

// EODCheck is one end-of-day condition.
type EODCheck struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
	View   string `json:"view"`
}

// EODState is the day's close status.
type EODState struct {
	Date       string     `json:"date"`
	Checks     []EODCheck `json:"checks"`
	Closed     bool       `json:"closed"`
	ClosedAt   string     `json:"closed_at,omitempty"`
	ClosedBy   string     `json:"closed_by,omitempty"`
	Exceptions []string   `json:"exceptions,omitempty"`
	NextDay    string     `json:"next_day,omitempty"`
	Rolled     int        `json:"rolled_positions"`
	Previous   string     `json:"previous_open,omitempty"` // earlier day still open
}

// EOD runs the end-of-day checks.
func (o *Ops) EOD(ctx context.Context, tenant, date string) (EODState, error) {
	st, _, err := docs.GetOr(ctx, o.Docs, tenant, cEOD, date, EODState{Date: date})
	if err != nil {
		return st, err
	}
	var cs []EODCheck
	ex, err := o.exchangeTrades(ctx, tenant, date)
	if err != nil {
		return st, err
	}
	om, _ := o.App.Trades.List(ctx, tenant, ingest.FromOMS, date)
	cs = append(cs, EODCheck{"Exchange and OMS files loaded", len(ex) > 0 && len(om) > 0, fmt.Sprintf("%d exchange trades, %d OMS fills", len(ex), len(om)), "files"})
	open, _ := o.App.Breaks.List(ctx, tenant, date, "OPEN")
	all, _ := o.App.Breaks.List(ctx, tenant, date, "")
	cs = append(cs, EODCheck{"Every reconciliation break resolved", len(ex) > 0 && len(all) > 0 && len(open) == 0, fmt.Sprintf("%d open of %d break(s)", len(open), len(all)), "recon"})
	es, _ := o.App.Ledger.Store.List(ctx, tenant, ledgerFilterDate(date))
	bills, mtm := 0, 0
	for _, e := range es {
		switch e.Source.Kind {
		case "BILL":
			bills++
		case "MTM":
			mtm++
		}
	}
	cs = append(cs, EODCheck{"Bills posted", bills > 0, fmt.Sprintf("%d bill entries", bills), "billing"})
	if cf, _ := o.carried(ctx, tenant, date); len(cf) > 0 {
		fut := map[string]bool{}
		for _, c := range cf {
			if strings.Contains(c.Instrument, "|FUT|") {
				fut[c.UCC+"|"+c.Exchange+"-"+c.Segment] = true
			}
		}
		cs = append(cs, EODCheck{"Carried futures marked to market", len(fut) == 0 || mtm > 0, fmt.Sprintf("%d MTM entries for %d client-segments with carried futures", mtm, len(fut)), "billing"})
	}
	notes, _ := o.Notes(ctx, tenant, date)
	unsent := 0
	for _, n := range notes {
		if n.Status != "Sent" {
			unsent++
		}
	}
	cs = append(cs, EODCheck{"Contract notes sent", len(notes) > 0 && unsent == 0, fmt.Sprintf("%d of %d not delivered", unsent, len(notes)), "notes"})
	rv, err := o.Risk(ctx, tenant, date)
	if err != nil {
		return st, err
	}
	breach := 0
	for _, r := range rv.Rows {
		if r.Status == "Breach" {
			breach++
		}
	}
	cs = append(cs, EODCheck{"No unaddressed margin breaches", breach == 0, fmt.Sprintf("%d client(s) in breach", breach), "rms"})
	tb, _ := o.App.Ledger.TrialBalance(ctx, tenant, date)
	cs = append(cs, EODCheck{"Trial balance agrees", tb.Balanced, fmt.Sprintf("₹%s each side", formatINR(tb.TotalDebit.Dec())), "ledger"})
	f, _ := o.Funds(ctx, tenant, date)
	cs = append(cs, EODCheck{"Client funds segregation", f.OK, fmt.Sprintf("cover %.2f×", f.Cover), "ledger"})
	st.Checks = cs
	st.Date = date
	if err := o.DayOpenError(ctx, tenant, date); err != nil {
		st.Previous = strings.TrimPrefix(err.Error(), ErrState.Error()+": ")
	}
	return st, nil
}

// CloseDay records the close. Failing checks are recorded as exceptions
// and need a note.
func (o *Ops) CloseDay(ctx context.Context, tenant, actor, date, note string) (EODState, error) {
	if err := o.require(ctx, tenant, actor, "Closing the day", RoleChecker); err != nil {
		return EODState{}, err
	}
	st, err := o.EOD(ctx, tenant, date)
	if err != nil {
		return st, err
	}
	if st.Closed {
		return st, fmt.Errorf("%w: %s is already closed", ErrState, date)
	}
	var ex []string
	for _, c := range st.Checks {
		if !c.OK {
			ex = append(ex, c.Name+": "+c.Detail)
		}
	}
	if len(ex) > 0 && strings.TrimSpace(note) == "" {
		return st, invalid("%d check(s) failed; add a note explaining why the day is closed with exceptions", len(ex))
	}
	if note != "" {
		ex = append(ex, "Note: "+note)
	}
	if err := o.DayOpenError(ctx, tenant, date); err != nil {
		return st, err
	}
	next, n, err := o.RollPositions(ctx, tenant, actor, date)
	if err != nil {
		return st, fmt.Errorf("rolling positions forward: %w", err)
	}
	st.Closed, st.ClosedAt, st.ClosedBy, st.Exceptions, st.NextDay, st.Rolled = true, o.now().Format("15:04"), actor, ex, next, n
	if _, err := docs.Put(ctx, o.Docs, tenant, cEOD, date, st, 0, actor); err != nil {
		return st, err
	}
	o.log(ctx, tenant, actor, "eod.closed", date, map[string]any{"exceptions": ex})
	o.backupAfterClose(ctx, tenant, date)
	return st, nil
}

// ---- dashboard ----

// Dashboard is the day overview.
type Dashboard struct {
	Date        string            `json:"date"`
	Trades      int               `json:"trades"`
	OMSFills    int               `json:"oms_fills"`
	Turnover    money.Dec         `json:"turnover"`
	BySegment   []LabelValue      `json:"by_segment"`
	ByHour      []LabelValue      `json:"by_hour"`
	Recon       map[string]int    `json:"recon"`
	Billed      bool              `json:"billed"`
	Risk        map[string]int    `json:"risk"`
	Required    money.Dec         `json:"required"`
	Collected   money.Dec         `json:"collected"`
	UtilBuckets []LabelValue      `json:"util_buckets"`
	Funds       FundsPosition     `json:"funds"`
	Notes       map[string]int    `json:"notes"`
	Penalty     money.Dec         `json:"penalty"`
	Short       money.Dec         `json:"short"`
	Settlement  map[string]int    `json:"settlement"`
	Bank        map[string]int    `json:"bank"`
	Alerts      map[string]int    `json:"alerts"`
	Overrides   int               `json:"pending_overrides"`
	ReportsDue  int               `json:"reports_due"`
	EOD         EODState          `json:"eod"`
	Pledges     int               `json:"pending_pledges"`
	Onboarding  int               `json:"onboarding"`
	Warnings    []string          `json:"warnings"`
	Extra       map[string]string `json:"extra"`

	// For the dashboard widgets.
	TopTurnover   []ClientStat `json:"top_turnover"`  // clients by today's turnover
	TopUtil       []ClientStat `json:"top_util"`      // clients by margin utilisation
	TopContracts  []LabelValue `json:"top_contracts"` // instruments by today's turnover
	BreaksByType  []LabelValue `json:"breaks_by_type"`
	Sides         []LabelValue `json:"sides"` // Buy, Sell
	ClientsTraded int          `json:"clients_traded"`
}

// ClientStat is one client in a dashboard top-list.
type ClientStat struct {
	UCC    string    `json:"ucc"`
	Name   string    `json:"name"`
	Value  money.Dec `json:"value"`
	Count  int       `json:"count,omitempty"`
	Util   float64   `json:"utilisation,omitempty"`
	Status string    `json:"status,omitempty"`
}

// dashTop is how many rows a top-list carries.
const dashTop = 8

// LabelValue is one bar in a chart.
type LabelValue struct {
	Label string    `json:"label"`
	Value money.Dec `json:"value"`
	Count int       `json:"count"`
}

// Dashboard aggregates the day.
func (o *Ops) Dashboard(ctx context.Context, tenant, date string) (Dashboard, error) {
	d := Dashboard{Date: date, Recon: map[string]int{}, Risk: map[string]int{}, Notes: map[string]int{}, Settlement: map[string]int{}, Bank: map[string]int{}, Alerts: map[string]int{}, Warnings: []string{}, Extra: map[string]string{}}
	ex, err := o.exchangeTrades(ctx, tenant, date)
	if err != nil {
		return d, err
	}
	om, _ := o.App.Trades.List(ctx, tenant, ingest.FromOMS, date)
	d.Trades, d.OMSFills = len(ex), len(om)
	seg := map[string]money.Dec{}
	segN := map[string]int{}
	hour := map[string]int{}
	segName := map[string]string{"CM": "Cash", "FO": "F&O", "CD": "Currency", "COM": "Commodity"}
	byUCC, byUCCn := map[string]money.Dec{}, map[string]int{}
	byInst, byInstN := map[string]money.Dec{}, map[string]int{}
	side := [2]LabelValue{{Label: "Buy"}, {Label: "Sell"}}
	for _, t := range ex {
		k := t.Exchange + " " + segName[t.Segment]
		seg[k] = seg[k].Add(t.Value())
		segN[k]++
		d.Turnover = d.Turnover.Add(t.Value())
		if len(t.TradeTime) >= 2 {
			hour[t.TradeTime[:2]]++
		}
		byUCC[t.UCC] = byUCC[t.UCC].Add(t.Value())
		byUCCn[t.UCC]++
		byInst[t.Instrument()] = byInst[t.Instrument()].Add(t.Value())
		byInstN[t.Instrument()]++
		i := 0
		if t.Side == "S" {
			i = 1
		}
		side[i].Value, side[i].Count = side[i].Value.Add(t.Value()), side[i].Count+1
	}
	d.Sides = side[:]
	d.ClientsTraded = len(byUCC)
	for _, k := range sortedKeys(byInst) {
		d.TopContracts = append(d.TopContracts, LabelValue{Label: k, Value: byInst[k], Count: byInstN[k]})
	}
	sort.SliceStable(d.TopContracts, func(i, j int) bool { return d.TopContracts[i].Value.Cmp(d.TopContracts[j].Value) > 0 })
	if len(d.TopContracts) > dashTop {
		d.TopContracts = d.TopContracts[:dashTop]
	}
	for _, k := range sortedKeys(seg) {
		d.BySegment = append(d.BySegment, LabelValue{Label: k, Value: seg[k], Count: segN[k]})
	}
	sort.SliceStable(d.BySegment, func(i, j int) bool { return d.BySegment[i].Value.Cmp(d.BySegment[j].Value) > 0 })
	for h := 9; h <= 15; h++ {
		k := fmt.Sprintf("%02d", h)
		d.ByHour = append(d.ByHour, LabelValue{Label: k + ":00", Count: hour[k]})
	}
	all, _ := o.App.Breaks.List(ctx, tenant, date, "")
	byType := map[string]int{}
	for _, b := range all {
		d.Recon[b.Status]++
		if b.Status == "OPEN" {
			byType[string(b.Type)]++
		}
	}
	for _, k := range sortedKeys(byType) {
		d.BreaksByType = append(d.BreaksByType, LabelValue{Label: k, Count: byType[k]})
	}
	sort.SliceStable(d.BreaksByType, func(i, j int) bool { return d.BreaksByType[i].Count > d.BreaksByType[j].Count })
	d.Recon["total"] = len(all)
	es, _ := o.App.Ledger.Store.List(ctx, tenant, ledgerFilterDate(date))
	for _, e := range es {
		if e.Source.Kind == "BILL" {
			d.Billed = true
		}
	}
	rv, err := o.Risk(ctx, tenant, date)
	if err != nil {
		return d, err
	}
	if rv.Warning != "" {
		d.Warnings = append(d.Warnings, rv.Warning)
	}
	var breachAmt money.Dec
	buckets := []LabelValue{{Label: "Under 50%"}, {Label: "50–80%"}, {Label: "80–100%"}, {Label: "Over 100%"}}
	for _, r := range rv.Rows {
		d.Risk[r.Status]++
		if r.Blocked {
			d.Risk["Blocked"]++
		}
		if r.Required.Sign() > 0 {
			d.Risk["WithMargin"]++
			d.Required = d.Required.Add(r.Required)
			d.Collected = d.Collected.Add(r.Cash.Add(r.CollUsable))
			switch {
			case r.Util < .5:
				buckets[0].Count++
			case r.Util < .8:
				buckets[1].Count++
			case r.Util < 1:
				buckets[2].Count++
			default:
				buckets[3].Count++
			}
		}
		if r.Status == "Breach" {
			breachAmt = breachAmt.Add(r.Free.Neg())
		}
	}
	d.UtilBuckets = buckets
	for _, r := range rv.Rows {
		if r.Required.Sign() > 0 {
			d.TopUtil = append(d.TopUtil, ClientStat{UCC: r.UCC, Name: r.Name, Value: r.Free, Util: r.Util, Status: r.Status})
		}
	}
	sort.SliceStable(d.TopUtil, func(i, j int) bool { return d.TopUtil[i].Util > d.TopUtil[j].Util })
	if len(d.TopUtil) > dashTop {
		d.TopUtil = d.TopUtil[:dashTop]
	}
	d.Extra["breach_amount"] = breachAmt.Fixed(2)
	for _, ov := range rv.Overrides {
		if ov.Status == "Pending" {
			d.Overrides++
		}
	}
	if m, err := o.Margins(ctx, tenant, date); err == nil {
		for _, r := range m.Rows {
			d.Penalty = d.Penalty.Add(r.Penalty)
			d.Short = d.Short.Add(r.Short)
		}
	}
	d.Funds, _ = o.Funds(ctx, tenant, date)
	notes, _ := o.Notes(ctx, tenant, date)
	for _, n := range notes {
		d.Notes[n.Status]++
	}
	d.Notes["total"] = len(notes)
	if st, err := o.Obligations(ctx, tenant, date); err == nil {
		for _, ob := range st.Obligations {
			d.Settlement[ob.Status]++
		}
		d.Extra["pay_in"] = st.PayIn
	}
	if dv, err := o.Dormancy(ctx, tenant, date); err == nil {
		d.Extra["inactive_due"] = fmt.Sprint(dv.Counts["Due"])
		if ws, err := o.WorkItems(ctx, tenant, "Open"); err == nil {
			over := 0
			for _, w := range ws {
				if w.Overdue {
					over++
				}
			}
			d.Extra["work_open"], d.Extra["work_overdue"] = fmt.Sprint(len(ws)), fmt.Sprint(over)
		}
		d.Extra["reactivation_pending"] = fmt.Sprint(dv.Counts["Reactivation pending"])
	}
	bt, _ := o.BankTxns(ctx, tenant)
	for _, t := range bt {
		d.Bank[t.Status]++
	}
	as, _ := o.Alerts(ctx, tenant)
	for _, a := range as {
		if a.Status != "Closed" && a.Status != "STR filed" {
			d.Alerts["open"]++
			if a.Severity == "High" {
				d.Alerts["high"]++
			}
		}
	}
	rs, _ := o.Reports(ctx, tenant, date)
	for _, r := range rs {
		if r.Freq == "Daily" && r.Status != "Uploaded" {
			d.ReportsDue++
		}
	}
	ps, _ := o.PledgeRequests(ctx, tenant)
	for _, p := range ps {
		if p.Status == "Pending" {
			d.Pledges++
		}
	}
	cs, _ := o.Clients(ctx, tenant)
	names := map[string]string{}
	for _, c := range cs {
		names[c.UCC] = c.Name
		if c.Status != "Active" {
			d.Onboarding++
		}
	}
	for _, k := range sortedKeys(byUCC) {
		d.TopTurnover = append(d.TopTurnover, ClientStat{UCC: k, Name: names[k], Value: byUCC[k], Count: byUCCn[k]})
	}
	sort.SliceStable(d.TopTurnover, func(i, j int) bool { return d.TopTurnover[i].Value.Cmp(d.TopTurnover[j].Value) > 0 })
	if len(d.TopTurnover) > dashTop {
		d.TopTurnover = d.TopTurnover[:dashTop]
	}
	d.EOD, _ = o.EOD(ctx, tenant, date)
	// Backups and the exchange holiday calendar, for "needs attention".
	set, _ := o.Settings(ctx, tenant)
	if o.Backups == nil {
		d.Extra["backup_problem"] = "Backups are not set up on this server"
	} else if es, err := o.Backups.List(tenant); err == nil {
		if ps := o.backupProblems(ctx, tenant, set, es); len(ps) > 0 {
			d.Extra["backup_problem"] = ps[0]
		}
	}
	y := o.now().Year()
	pending := 0
	for _, yr := range []int{y, y + 1} {
		if _, p, _, _ := o.approvedAndPending(ctx, tenant, yr); p != nil {
			pending++
		}
		if ws := o.holidayWarnings(ctx, tenant, set, yr); len(ws) > 0 && d.Extra["holiday_warning"] == "" {
			d.Extra["holiday_warning"] = ws[0]
		}
	}
	d.Extra["holidays_pending"] = fmt.Sprint(pending)
	return d, nil
}
