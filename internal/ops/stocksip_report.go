package ops

import (
	"context"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// ---- stock SIPs: messages, screens and reports ----

func init() {
	EventVars["stocksip.predebit"] = []string{"amount", "debit_date", "trade_date", "scrips"}
	EventVars["stocksip.placed"] = []string{"scrip", "qty", "price", "date", "ref"}
	EventVars["stocksip.filled"] = []string{"scrip", "qty", "price", "value", "date"}
	EventVars["stocksip.failed"] = []string{"scrip", "date", "reason"}
	EventVars["stocksip.cancelled"] = []string{"scrip", "reason"}
}

// sipTemplates are the stock SIP messages a firm starts with. SMS needs
// templates registered on DLT, so the firm adds those itself; WhatsApp
// needs the template approved by Meta under the name set.
func sipTemplates() []Template {
	t := func(ev, ch, subj, body string) Template {
		return Template{ID: "TPL-" + strings.ReplaceAll(ev, ".", "-") + "-" + strings.ToLower(ch), Event: ev, Channel: ch, Category: "Transactional", Subject: subj, Body: body, Active: true,
			WAName: map[bool]string{true: strings.ReplaceAll(ev, ".", "_"), false: ""}[ch == ChWhatsApp]}
	}
	return []Template{
		t("stocksip.predebit", ChEmail, "₹{amount} will be debited on {debit_date} for your stock SIP",
			"Dear {name},\n\nAs per the mandate you gave us, ₹{amount} will be debited from your registered bank account on {debit_date} and credited to your trading account {ucc}, so that your stock SIP instalments on {trade_date} ({scrips}) can be bought.\n\nIf you do not want this debit, pause the mandate in your UPI app or bank, or write to us, before {debit_date}.\n\n{firm}"),
		t("stocksip.predebit", ChWhatsApp, "", "{name}, ₹{amount} will be debited from your bank on {debit_date} for your stock SIP on {trade_date} ({scrips}) and credited to your trading account {ucc}. To stop it, pause the mandate in your UPI app or write to us before then."),
		t("stocksip.placed", ChEmail, "SIP order placed: {qty} {scrip}", "Dear {name},\n\nYour stock SIP order to buy {qty} shares of {scrip} at about ₹{price} has been placed for {date} (reference {ref}). You will get the contract note once it is executed.\n\n{firm}"),
		t("stocksip.placed", ChWhatsApp, "", "{name}, your SIP order to buy {qty} {scrip} at about ₹{price} is placed for {date} (ref {ref})."),
		t("stocksip.filled", ChEmail, "SIP bought: {qty} {scrip}", "Dear {name},\n\nYour stock SIP bought {qty} shares of {scrip} at ₹{price} (₹{value}) on {date}. The contract note follows separately.\n\n{firm}"),
		t("stocksip.filled", ChWhatsApp, "", "{name}, your SIP bought {qty} {scrip} at ₹{price} (₹{value}) on {date}."),
		t("stocksip.failed", ChEmail, "SIP instalment in {scrip} not bought", "Dear {name},\n\nYour stock SIP instalment in {scrip} for {date} was not bought: {reason}.\n\n{firm}"),
		t("stocksip.failed", ChWhatsApp, "", "{name}, your SIP instalment in {scrip} for {date} was not bought: {reason}."),
		t("stocksip.cancelled", ChEmail, "Your stock SIP in {scrip} has stopped", "Dear {name},\n\nYour stock SIP in {scrip} has stopped: {reason}. You can start a new one from the client portal.\n\n{firm}"),
		t("stocksip.cancelled", ChWhatsApp, "", "{name}, your stock SIP in {scrip} has stopped: {reason}. You can start a new one from the client portal."),
	}
}

// sipNotify sends a stock SIP event, first installing any of its default
// templates the firm does not have (a template switched off stays off).
func (o *Ops) sipNotify(ctx context.Context, tenant, actor, event, ucc string, data map[string]string) {
	if ts, err := o.templates(ctx, tenant); err == nil {
		have := map[string]bool{}
		for _, t := range ts {
			have[t.Event+"|"+t.Channel] = true
		}
		for _, t := range sipTemplates() {
			if t.Event == event && !have[t.Event+"|"+t.Channel] {
				_ = docs.Upsert(ctx, o.Docs, tenant, cTemplate, t.ID, t, "system")
			}
		}
	}
	o.notify(ctx, tenant, actor, event, ucc, data)
}

// ---- reporting ----

// SIPReportRow is one SIP's result.
type SIPReportRow struct {
	SIP       string    `json:"sip"`
	UCC       string    `json:"ucc"`
	Name      string    `json:"name"`
	Exchange  string    `json:"exchange"`
	Symbol    string    `json:"symbol"`
	Status    string    `json:"status"`
	Describe  string    `json:"describe"`
	Due       int       `json:"due"` // instalments worked out (not skipped)
	Bought    int       `json:"bought"`
	Failed    int       `json:"failed"`
	Skipped   int       `json:"skipped"`
	Units     int64     `json:"units"`
	Invested  money.Dec `json:"invested"` // at the prices bought, before charges
	AvgCost   money.Dec `json:"avg_cost"`
	Price     money.Dec `json:"price"`
	Value     money.Dec `json:"value"`
	Gain      money.Dec `json:"gain"`
	XIRR      *float64  `json:"xirr,omitempty"` // annual %, from the purchases and today's value
	FirstBuy  string    `json:"first_buy,omitempty"`
	LastBuy   string    `json:"last_buy,omitempty"`
	LastError string    `json:"last_error,omitempty"`
}

// StockSIPReport is each SIP's invested amount, units, average cost and
// XIRR as at date (valued at the day's price), and the failed instalments.
func (o *Ops) StockSIPReport(ctx context.Context, tenant, ucc, date string) ([]SIPReportRow, []SIPOrder, error) {
	sips, err := o.StockSIPs(ctx, tenant, ucc)
	if err != nil {
		return nil, nil, err
	}
	orders, err := docs.List[SIPOrder](ctx, o.Docs, tenant, cSIPOrder, "")
	if err != nil {
		return nil, nil, err
	}
	_, look, _ := o.Market(ctx, tenant, date)
	by := map[string][]SIPOrder{}
	failed := []SIPOrder{}
	for _, x := range orders {
		if x.Date > date || (ucc != "" && x.UCC != strings.ToUpper(ucc)) {
			continue
		}
		by[x.SIP] = append(by[x.SIP], x)
		switch x.Status {
		case SIPOrdFailed, SIPOrdLapsed, SIPOrdNotSent, SIPOrdCheck:
			failed = append(failed, x)
		}
	}
	sort.SliceStable(failed, func(i, j int) bool { return failed[i].ID > failed[j].ID })
	rows := []SIPReportRow{}
	for _, s := range sips {
		r := SIPReportRow{SIP: s.ID, UCC: s.UCC, Name: s.Name, Exchange: s.Exchange, Symbol: s.Symbol, Status: s.Status, Describe: s.Describe()}
		var flows []float64
		var when []time.Time
		for _, x := range by[s.ID] {
			switch x.Status {
			case SIPOrdSkipped:
				r.Skipped++
				continue
			case SIPOrdFailed, SIPOrdLapsed:
				r.Failed++
				r.LastError = x.Date + ": " + x.Reason
			case SIPOrdFilled, SIPOrdPartly:
				r.Bought++
			}
			r.Due++
			if x.FilledQty > 0 && (x.Status == SIPOrdFilled || x.Status == SIPOrdPartly) {
				v := x.AvgPrice.MulInt(x.FilledQty)
				r.Units += x.FilledQty
				r.Invested = r.Invested.Add(v)
				if r.FirstBuy == "" || x.Date < r.FirstBuy {
					r.FirstBuy = x.Date
				}
				if x.Date > r.LastBuy {
					r.LastBuy = x.Date
				}
				if t, err := time.Parse("2006-01-02", x.Date); err == nil {
					flows, when = append(flows, -v.Float64()), append(when, t)
				}
			}
		}
		if i, ok := look.Get(s.Exchange, "CM", s.Symbol+"|EQ||"); ok {
			r.Price = sipPrice(i)
		}
		if r.Units > 0 {
			r.AvgCost, _ = r.Invested.Quo(money.DecInt(r.Units))
			r.Value = r.Price.MulInt(r.Units)
			r.Gain = r.Value.Sub(r.Invested)
			if t, err := time.Parse("2006-01-02", date); err == nil && r.Price.Sign() > 0 {
				if x, ok := xirr(append(flows, r.Value.Float64()), append(when, t)); ok {
					pc := math.Round(x*10000) / 100
					r.XIRR = &pc
				}
			}
		}
		rows = append(rows, r)
	}
	return rows, failed, nil
}

// xirr is the annual rate at which the dated cash flows are worth nothing
// today (Newton's method, then bisection).
func xirr(flows []float64, when []time.Time) (float64, bool) {
	if len(flows) < 2 || len(flows) != len(when) {
		return 0, false
	}
	neg, pos := false, false
	for _, f := range flows {
		neg, pos = neg || f < 0, pos || f > 0
	}
	if !neg || !pos {
		return 0, false
	}
	t0 := when[0]
	for _, t := range when {
		if t.Before(t0) {
			t0 = t
		}
	}
	yrs := make([]float64, len(when))
	for i, t := range when {
		yrs[i] = t.Sub(t0).Hours() / 24 / 365
	}
	if yrs[len(yrs)-1] < 1.0/365 {
		return 0, false // bought today: no period to measure
	}
	npv := func(r float64) float64 {
		s := 0.0
		for i, f := range flows {
			s += f / math.Pow(1+r, yrs[i])
		}
		return s
	}
	r := 0.1
	for i := 0; i < 60; i++ {
		f, d := npv(r), 0.0
		for j, c := range flows {
			d -= yrs[j] * c / math.Pow(1+r, yrs[j]+1)
		}
		if d == 0 || math.IsNaN(f) {
			break
		}
		nr := r - f/d
		if nr <= -0.9999 || math.IsNaN(nr) || math.IsInf(nr, 0) {
			break
		}
		if math.Abs(nr-r) < 1e-9 {
			return nr, true
		}
		r = nr
	}
	lo, hi := -0.9999, 100.0
	flo, fhi := npv(lo), npv(hi)
	if flo*fhi > 0 {
		return 0, false
	}
	for i := 0; i < 200; i++ {
		mid := (lo + hi) / 2
		fm := npv(mid)
		if math.Abs(fm) < 1e-9 {
			return mid, true
		}
		if fm*flo < 0 {
			hi = mid
		} else {
			lo, flo = mid, fm
		}
	}
	return (lo + hi) / 2, true
}

// StockSIPReportCSV is the report for download.
func (o *Ops) StockSIPReportCSV(ctx context.Context, tenant, date string) ([]byte, error) {
	rows, failed, err := o.StockSIPReport(ctx, tenant, "", date)
	if err != nil {
		return nil, err
	}
	out := [][]string{{"SIP", "UCC", "Name", "Exchange", "Symbol", "Plan", "Status", "Instalments", "Bought", "Failed", "Skipped", "Units", "Invested", "AvgCost", "Price", "Value", "Gain", "XIRRPct", "FirstBuy", "LastBuy"}}
	for _, r := range rows {
		x := ""
		if r.XIRR != nil {
			x = strconv.FormatFloat(*r.XIRR, 'f', 2, 64)
		}
		out = append(out, []string{r.SIP, r.UCC, r.Name, r.Exchange, r.Symbol, r.Describe, r.Status, strconv.Itoa(r.Due), strconv.Itoa(r.Bought), strconv.Itoa(r.Failed), strconv.Itoa(r.Skipped),
			strconv.FormatInt(r.Units, 10), r.Invested.Fixed(2), r.AvgCost.Fixed(2), r.Price.Fixed(2), r.Value.Fixed(2), r.Gain.Fixed(2), x, r.FirstBuy, r.LastBuy})
	}
	out = append(out, []string{}, []string{"Failed instalments"}, []string{"Order", "Date", "UCC", "Symbol", "Status", "Reason"})
	for _, f := range failed {
		out = append(out, []string{f.Ref, f.Date, f.UCC, f.Symbol, f.Status, firstOf(f.Reason, f.Note)})
	}
	return CSV(out), nil
}

// ---- screens ----

// StockSIPRow is a SIP with its next instalment.
type StockSIPRow struct {
	StockSIP
	Next      string    `json:"next,omitempty"`
	NextTrade string    `json:"next_trade,omitempty"`
	Price     money.Dec `json:"price"`
}

// SIPListInfo describes an uploaded list.
type SIPListInfo struct {
	About      string `json:"about"`
	Count      int    `json:"count"`
	File       string `json:"file,omitempty"`
	UploadedAt string `json:"uploaded_at,omitempty"`
	UploadedBy string `json:"uploaded_by,omitempty"`
}

// StockSIPScreen is the staff screen.
type StockSIPScreen struct {
	Date          string                 `json:"date"`
	Settings      SIPSettings            `json:"settings"`
	SIPs          []StockSIPRow          `json:"sips"`
	Due           []StockSIPRow          `json:"due"` // instalments falling to the date not yet worked out
	Orders        []SIPOrder             `json:"orders"`
	Failures      []SIPOrder             `json:"failures"`
	Batches       []SIPBatch             `json:"batches"`
	Topups        []SIPTopup             `json:"topups"`
	Mandates      []SIPMandate           `json:"mandates"`
	Lists         map[string]SIPListInfo `json:"lists"`
	Report        []SIPReportRow         `json:"report"`
	Counts        map[string]int         `json:"counts"`
	Run           SIPRunState            `json:"run"`
	Router        string                 `json:"router"`
	RouterReady   bool                   `json:"router_ready"`
	RouterNote    string                 `json:"router_note,omitempty"`
	BasketFormats []string               `json:"basket_formats"`
	Channels      []string               `json:"channels"`
	ConsentSrc    []string               `json:"consent_sources"`
}

// sipRows adds each SIP's next instalment not yet worked out (orders are
// the instalments worked out so far) and its price.
func (o *Ops) sipRows(ctx context.Context, tenant string, sips []StockSIP, date string, orders []SIPOrder) []StockSIPRow {
	set, _ := o.Settings(ctx, tenant)
	_, look, _ := o.Market(ctx, tenant, date)
	today := o.now().Format("2006-01-02")
	done := map[string]bool{}
	for _, x := range orders {
		done[x.SIP+"|"+x.Scheduled] = true
	}
	rows := []StockSIPRow{}
	for _, s := range sips {
		r := StockSIPRow{StockSIP: s}
		r.History = nil
		if s.Status == SIPActive || s.Status == SIPPaused {
			from := max(today, date)
			if s.Status == SIPPaused && s.PausedUntil != "" && s.PausedUntil >= from {
				t, _ := time.Parse("2006-01-02", s.PausedUntil)
				from = t.AddDate(0, 0, 1).Format("2006-01-02")
			}
			if s.Status == SIPActive || s.PausedUntil != "" {
				r.Next = sipNextScheduled(s, set, from)
				for i := 0; i < 400 && r.Next != "" && done[s.ID+"|"+r.Next]; i++ {
					t, _ := time.Parse("2006-01-02", r.Next)
					r.Next = sipNextScheduled(s, set, t.AddDate(0, 0, 1).Format("2006-01-02"))
				}
				r.NextTrade = sipTradeDate(set, r.Next)
			}
		}
		if i, ok := look.Get(s.Exchange, "CM", s.Symbol+"|EQ||"); ok {
			r.Price = sipPrice(i)
		}
		rows = append(rows, r)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i].Status == SIPActive || rows[i].Status == SIPPaused, rows[j].Status == SIPActive || rows[j].Status == SIPPaused
		if a != b {
			return a
		}
		return rows[i].ID > rows[j].ID
	})
	return rows
}

// StockSIPView is the staff screen for a date.
func (o *Ops) StockSIPView(ctx context.Context, tenant, date string) (StockSIPScreen, error) {
	v := StockSIPScreen{Date: date, Settings: o.sipSettings(ctx, tenant), Due: []StockSIPRow{}, Orders: []SIPOrder{}, Failures: []SIPOrder{}, Mandates: []SIPMandate{},
		Lists: map[string]SIPListInfo{}, Counts: map[string]int{}, BasketFormats: SIPBasketFormats(), ConsentSrc: []string{SrcForm, SrcCall}}
	if !validDate(date) {
		return v, invalid("give the date (YYYY-MM-DD)")
	}
	sips, err := o.StockSIPs(ctx, tenant, "")
	if err != nil {
		return v, err
	}
	set, _ := o.Settings(ctx, tenant)
	all, _ := docs.List[SIPOrder](ctx, o.Docs, tenant, cSIPOrder, "")
	v.SIPs = o.sipRows(ctx, tenant, sips, date, all)
	have := map[string]bool{}
	from := time.Now().AddDate(0, 0, -45).Format("2006-01-02")
	if t, err := time.Parse("2006-01-02", date); err == nil {
		from = t.AddDate(0, 0, -45).Format("2006-01-02")
	}
	for _, x := range all {
		if x.Date == date {
			v.Orders = append(v.Orders, x)
			have[x.ID] = true
			v.Counts[x.Status]++
		}
		if x.Date >= from && x.Date <= date && (x.Status == SIPOrdFailed || x.Status == SIPOrdLapsed || x.Status == SIPOrdNotSent || x.Status == SIPOrdCheck) {
			v.Failures = append(v.Failures, x)
		}
	}
	sort.SliceStable(v.Orders, func(i, j int) bool { return v.Orders[i].Ref < v.Orders[j].Ref })
	sort.SliceStable(v.Failures, func(i, j int) bool { return v.Failures[i].ID > v.Failures[j].ID })
	for _, r := range v.SIPs {
		if r.Status != SIPActive && r.Status != SIPPaused {
			continue
		}
		for _, sched := range r.StockSIP.scheduledFor(set, date) {
			if !have[date+"|"+r.ID+"|"+sched] {
				v.Due = append(v.Due, r)
				break
			}
		}
	}
	v.Batches, _ = o.StockSIPBatches(ctx, tenant)
	if len(v.Batches) > 30 {
		v.Batches = v.Batches[:30]
	}
	v.Topups = o.StockSIPTopups(ctx, tenant, 100)
	ms, _ := docs.List[SIPMandate](ctx, o.Docs, tenant, cSIPMandate, "")
	for _, m := range ms {
		v.Mandates = append(v.Mandates, m.Public())
	}
	for k, about := range SIPListKinds {
		l := o.sipList(ctx, tenant, k)
		v.Lists[k] = SIPListInfo{About: about, Count: len(l.Items), File: l.File, UploadedAt: l.UploadedAt, UploadedBy: l.UploadedBy}
	}
	v.Report, _, _ = o.StockSIPReport(ctx, tenant, "", date)
	v.Run, _, _ = docs.GetOr(ctx, o.Docs, tenant, cSIPRun, date, SIPRunState{Date: date})
	if bs, err := o.BrokerageSetup(ctx, tenant); err == nil {
		v.Channels = bs.Channels
	}
	if r, err := o.sipRouter(ctx, tenant, v.Settings, date, 1); err != nil {
		v.Router, v.RouterNote = v.Settings.Router, strings.TrimPrefix(strings.TrimPrefix(err.Error(), "invalid: "), "invalid state: ")
	} else {
		v.Router, v.RouterReady = r.Name(), v.Settings.Router != "other"
		if v.Settings.Router == "other" {
			v.RouterNote = "the OMS's order-entry API is not public: SIP orders go by basket file until it is"
		}
	}
	return v, nil
}

// PortalSIPView is a client's stock SIP page.
type PortalSIPView struct {
	UCC       string         `json:"ucc"`
	Enabled   bool           `json:"enabled"`
	SIPs      []StockSIPRow  `json:"sips"`
	Orders    []SIPOrder     `json:"orders"`
	Report    []SIPReportRow `json:"report"`
	Mandate   *SIPMandate    `json:"mandate,omitempty"`
	Topups    []SIPTopup     `json:"topups"`
	Eligible  []SIPListItem  `json:"eligible"` // the firm's list, when it keeps one
	OrderType string         `json:"order_type"`
	PlaceAt   string         `json:"place_at"`
	MinAmount money.Amount   `json:"min_amount"`
	MaxFail   int            `json:"max_failures"`
	TopupsOn  bool           `json:"topups_on"`
	TopupCap  money.Amount   `json:"topup_cap"`
	Version   int            `json:"consent_version"`
}

// PortalStockSIPs is the client's own SIPs, for one of their accounts.
func (o *Ops) PortalStockSIPs(ctx context.Context, tenant, actor, ucc, date string) (PortalSIPView, error) {
	accts, err := o.PortalAccounts(ctx, tenant, actor)
	if err != nil {
		return PortalSIPView{}, err
	}
	ucc = strings.ToUpper(strings.TrimSpace(ucc))
	if ucc == "" && len(accts) > 0 {
		ucc = accts[0]
	}
	if !contains(accts, ucc) {
		return PortalSIPView{}, ErrNotFound
	}
	cfg := o.sipSettings(ctx, tenant)
	v := PortalSIPView{UCC: ucc, Enabled: cfg.Enabled, Orders: []SIPOrder{}, Topups: []SIPTopup{}, Eligible: []SIPListItem{}, OrderType: cfg.OrderType, PlaceAt: cfg.PlaceAt,
		MinAmount: cfg.MinAmount, MaxFail: cfg.MaxFailures, TopupsOn: cfg.Topups, TopupCap: cfg.TopupCap, Version: cfg.ConsentVersion}
	sips, err := o.StockSIPs(ctx, tenant, ucc)
	if err != nil {
		return v, err
	}
	all, _ := docs.List[SIPOrder](ctx, o.Docs, tenant, cSIPOrder, "")
	v.SIPs = o.sipRows(ctx, tenant, sips, date, all)
	for i := len(all) - 1; i >= 0 && len(v.Orders) < 200; i-- {
		if all[i].UCC == ucc {
			x := all[i]
			x.Free, x.Topup = 0, 0
			v.Orders = append(v.Orders, x)
		}
	}
	sort.SliceStable(v.Orders, func(i, j int) bool { return v.Orders[i].ID > v.Orders[j].ID })
	v.Report, _, _ = o.StockSIPReport(ctx, tenant, ucc, date)
	if m, _, err := docs.Get[SIPMandate](ctx, o.Docs, tenant, cSIPMandate, ucc); err == nil {
		p := m.Public()
		v.Mandate = &p
	}
	for _, t := range o.StockSIPTopups(ctx, tenant, 0) {
		if t.UCC == ucc && len(v.Topups) < 20 {
			v.Topups = append(v.Topups, t)
		}
	}
	if cfg.Eligibility == "list" {
		v.Eligible = o.sipList(ctx, tenant, "eligible").Items
	}
	return v, nil
}
