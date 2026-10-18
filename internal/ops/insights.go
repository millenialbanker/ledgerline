package ops

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// ---- client insights: opportunities found in each client's own account ----
//
// Every evening the insight automations look through each client's
// holdings, purchase lots, positions, funds and the firm's corporate-action
// calendar for things the client would want to know: losses that could be
// booked before the financial year ends to cut tax, a buy-back at a premium
// on shares they hold, stock derivatives about to be settled by delivery,
// funds lying idle, holdings that could be pledged for margin, an account
// about to turn inactive, a dividend or bonus on their shares, an advance
// tax instalment on gains already booked.
//
// What is sent is information about the client's own account, never a
// recommendation to buy or sell a security: a stockbroker that is not a
// registered research analyst or investment adviser must not advise. The
// wording lives in the message templates, which the compliance officer
// approves (Messages); every message carries a line saying it is not
// advice. Clients can turn insights off in the portal, promotional SMS
// follows the DND register, and a client gets at most one non-urgent
// insight in a few days.
//
// Each automation is Auto (message the client), Suggest (put the client on
// the relationship managers' call list only) or Off, set on Autopilot.
// Outcomes are tracked: a client who trades, tenders or rolls over within
// 15 days of an insight counts as acted on, with the turnover.

const cInsight = "insight"

// Insight is one opportunity for one client.
type Insight struct {
	ID       string            `json:"id"`
	Kind     string            `json:"kind"`
	UCC      string            `json:"ucc"`
	Name     string            `json:"name"`
	Period   string            `json:"period"` // FY, offer, expiry, month…
	Title    string            `json:"title"`
	Detail   string            `json:"detail"`
	Value    money.Dec         `json:"value"` // what it is worth to the client, in rupees
	Facts    map[string]string `json:"facts"`
	Symbols  []string          `json:"symbols,omitempty"`
	Found    string            `json:"found"`
	Updated  string            `json:"updated"`
	Due      string            `json:"due,omitempty"` // act by
	Status   string            `json:"status"`        // Open, Sent, Called, Acted, Dismissed, Expired
	SentAt   string            `json:"sent_at,omitempty"`
	SentOn   string            `json:"sent_on,omitempty"` // business date of the first message
	Reminded string            `json:"reminded,omitempty"`
	Channels []string          `json:"channels,omitempty"`
	Held     string            `json:"held,omitempty"` // why a message was not sent
	Calls    []InsightCall     `json:"calls,omitempty"`
	ActedOn  string            `json:"acted_on,omitempty"`
	Trades   int               `json:"trades,omitempty"`
	Turnover money.Dec         `json:"turnover"`
	By       string            `json:"by,omitempty"`
	Note     string            `json:"note,omitempty"`
}

// InsightCall is a relationship manager's call or note.
type InsightCall struct {
	At      string `json:"at"`
	By      string `json:"by"`
	Outcome string `json:"outcome"`
	Text    string `json:"text,omitempty"`
}

// InsightKind describes one kind of insight.
type InsightKind struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Urgent bool   `json:"urgent"` // time-bound: not held back by the cool-off
	Value  string `json:"value"`  // what Value measures
}

// InsightKinds lists the kinds in display order.
var InsightKinds = []InsightKind{
	{"tax_harvest", "Tax-loss harvesting", true, "Estimated tax saved"},
	{"buyback", "Buy-back eligibility", true, "Premium on the entitlement"},
	{"fo_expiry", "Physical settlement at expiry", true, "Delivery value"},
	{"advance_tax", "Advance tax on gains", true, "Gains booked this year"},
	{"corp_action", "Dividends and bonuses", false, "Dividend due"},
	{"idle_funds", "Idle funds", false, "Funds"},
	{"pledge_margin", "Margin from holdings", false, "Margin available"},
	{"dormant", "Account turning inactive", false, "Assets in the account"},
}

func insightKind(id string) InsightKind {
	for _, k := range InsightKinds {
		if k.ID == id {
			return k
		}
	}
	return InsightKind{ID: id, Name: id}
}

// Insight statuses.
const (
	InsOpen      = "Open"
	InsSent      = "Sent"
	InsCalled    = "Called"
	InsActed     = "Acted"
	InsDismissed = "Dismissed"
	InsExpired   = "Expired"
)

// actWindow is how long after an insight a trade counts as acting on it.
const actWindow = 15

// ---- tax rates for listed equity (resident individuals) ----
//
// Section 111A short-term gains at 20% and section 112A long-term gains at
// 12.5% above ₹1.25 lakh a year, as amended by the Finance (No. 2) Act 2024
// for transfers from 23 July 2024; 4% health and education cess. Surcharge,
// the basic exemption and other income are not considered: figures are
// estimates for the client to check with their tax adviser.
var (
	rateSTCG     = money.MustDec("0.20")
	rateLTCG     = money.MustDec("0.125")
	ltcgExempt   = money.MustDec("125000")
	cessFactor   = money.MustDec("1.04")
	sttDelivery  = money.MustDec("0.001") // per side, delivery equity
	smallHolder  = money.MustDec("200000")
	advanceTaxes = []string{"06-15", "09-15", "12-15", "03-15"}
)

// fyOf is the financial year a date falls in: label, first and last day.
func fyOf(date string) (label, from, to string) {
	y, _ := strconv.Atoi(date[:4])
	if date[5:7] < "04" {
		y--
	}
	return fmt.Sprintf("%d-%02d", y, (y+1)%100), fmt.Sprintf("%d-04-01", y), fmt.Sprintf("%d-03-31", y+1)
}

// lastTradeDayIn is the last business day whose T+1 settlement falls on or
// before end.
func lastTradeDayIn(set Settings, end string) string {
	d := addDays(end, 1)
	for i := 0; i < 15; i++ {
		p := set.PrevBusinessDay(d)
		if p == "" {
			return end
		}
		if set.nextBusinessDay(p) <= end {
			return p
		}
		d = p
	}
	return end
}

// capTax is the estimated tax (with cess) on a year's short- and long-term
// gains after setting off losses: short-term losses against short-term then
// long-term gains, long-term losses against long-term gains only.
func capTax(st, lt money.Dec) money.Dec {
	if st.Sign() < 0 {
		lt, st = lt.Add(st), money.Dec{}
	}
	if lt.Sign() < 0 {
		lt = money.Dec{}
	}
	ltTaxable := money.Max(lt.Sub(ltcgExempt), money.Dec{})
	return st.Mul(rateSTCG).Add(ltTaxable.Mul(rateLTCG)).Mul(cessFactor)
}

// HarvestLot is one open purchase lot with its unrealised gain or loss.
type HarvestLot struct {
	Symbol    string    `json:"symbol"`
	ISIN      string    `json:"isin"`
	Qty       int64     `json:"qty"`
	BuyDate   string    `json:"buy_date"`
	BuyPrice  money.Dec `json:"buy_price"`
	LTP       money.Dec `json:"ltp"`
	PnL       money.Dec `json:"pnl"`
	Term      string    `json:"term"` // Short, Long
	LongOn    string    `json:"long_on,omitempty"`
	TurnsLong bool      `json:"turns_long,omitempty"` // a short-term gain that turns long-term within 60 days
}

// TaxPlan is a client's capital gains position for the financial year and
// what booking losses (or long-term gains within the exemption) would do.
type TaxPlan struct {
	UCC          string       `json:"ucc"`
	Date         string       `json:"date"`
	FY           string       `json:"fy"`
	From         string       `json:"from"`
	To           string       `json:"to"`
	LastDay      string       `json:"last_day"` // last trade date that settles in the year
	DaysLeft     int          `json:"days_left"`
	STCG         money.Dec    `json:"stcg"` // realised this year (net of short-term losses)
	LTCG         money.Dec    `json:"ltcg"`
	Spec         money.Dec    `json:"speculative"`
	STLoss       money.Dec    `json:"st_loss"` // unrealised losses available to book (positive)
	LTLoss       money.Dec    `json:"lt_loss"`
	STGain       money.Dec    `json:"st_gain"` // unrealised gains
	LTGain       money.Dec    `json:"lt_gain"`
	TaxNow       money.Dec    `json:"tax_now"`
	TaxAfter     money.Dec    `json:"tax_after"`
	Saving       money.Dec    `json:"saving"`
	CarryForward money.Dec    `json:"carry_forward"` // losses beyond this year's gains
	ExemptLeft   money.Dec    `json:"exempt_left"`   // long-term gains still tax-free this year
	GainHarvest  money.Dec    `json:"gain_harvest"`  // long-term gains that could be booked within it
	STTCost      money.Dec    `json:"stt_cost"`      // STT on selling the loss lots and buying back
	Losses       []HarvestLot `json:"losses"`
	Gains        []HarvestLot `json:"gains"`
	UnknownCost  int          `json:"unknown_cost"`
	Notes        []string     `json:"notes"`
}

// TaxPlan works out a client's harvesting position on a date.
func (o *Ops) TaxPlan(ctx context.Context, tenant, ucc, date string) (TaxPlan, error) {
	ucc = strings.ToUpper(ucc)
	set, _ := o.Settings(ctx, tenant)
	p := TaxPlan{UCC: ucc, Date: date, Losses: []HarvestLot{}, Gains: []HarvestLot{}}
	p.FY, p.From, p.To = fyOf(date)
	p.LastDay = lastTradeDayIn(set, p.To)
	p.DaysLeft = daysBetween(date, p.LastDay)
	open, sales, _, err := o.equityBook(ctx, tenant, ucc, date)
	if err != nil {
		return p, err
	}
	g := gainsBy(sales, p.From, date)
	p.STCG, p.LTCG, p.Spec = g[CatSTCG], g[CatLTCG], g[CatSpec]
	_, look, _ := o.Market(ctx, tenant, date)
	for _, l := range open {
		if l.Qty <= 0 {
			continue
		}
		if l.Date == "" {
			p.UnknownCost++
			continue
		}
		i, ok := look.ByISIN(l.ISIN)
		if !ok {
			i, ok = look.Get("NSE", "CM", l.Symbol+"|EQ||")
		}
		if !ok || i.LTP.Sign() <= 0 {
			continue
		}
		term, _ := gainCategory(l.Date, date)
		h := HarvestLot{Symbol: l.Symbol, ISIN: l.ISIN, Qty: l.Qty, BuyDate: l.Date, BuyPrice: l.Price, LTP: i.LTP, PnL: i.LTP.Sub(l.Price).MulInt(l.Qty), Term: "Short"}
		if tb, err := time.Parse("2006-01-02", l.Date); err == nil {
			h.LongOn = tb.AddDate(1, 0, 1).Format("2006-01-02")
		}
		if term == CatLTCG {
			h.Term, h.LongOn = "Long", ""
		}
		switch {
		case h.PnL.Sign() < 0:
			p.Losses = append(p.Losses, h)
			p.STTCost = p.STTCost.Add(i.LTP.MulInt(l.Qty).Mul(sttDelivery).MulInt(2))
			if h.Term == "Long" {
				p.LTLoss = p.LTLoss.Sub(h.PnL)
			} else {
				p.STLoss = p.STLoss.Sub(h.PnL)
			}
		case h.PnL.Sign() > 0:
			h.TurnsLong = h.Term == "Short" && h.LongOn != "" && daysBetween(date, h.LongOn) <= 60
			p.Gains = append(p.Gains, h)
			if h.Term == "Long" {
				p.LTGain = p.LTGain.Add(h.PnL)
			} else {
				p.STGain = p.STGain.Add(h.PnL)
			}
		}
	}
	sort.SliceStable(p.Losses, func(i, j int) bool { return p.Losses[i].PnL.Cmp(p.Losses[j].PnL) < 0 })
	sort.SliceStable(p.Gains, func(i, j int) bool { return p.Gains[i].PnL.Cmp(p.Gains[j].PnL) > 0 })
	p.TaxNow = capTax(p.STCG, p.LTCG)
	stAfter, ltAfter := p.STCG.Sub(p.STLoss), p.LTCG.Sub(p.LTLoss)
	p.TaxAfter = capTax(stAfter, ltAfter)
	p.Saving = p.TaxNow.Sub(p.TaxAfter)
	// Losses left after this year's gains are carried forward (8 years,
	// against capital gains only, if the return is filed on time).
	if stAfter.Sign() < 0 {
		ltAfter, stAfter = ltAfter.Add(stAfter), money.Dec{}
	}
	if ltAfter.Sign() < 0 {
		p.CarryForward = ltAfter.Neg()
	}
	ltNet := p.LTCG
	if p.STCG.Sign() < 0 {
		ltNet = ltNet.Add(p.STCG)
	}
	p.ExemptLeft = money.Max(ltcgExempt.Sub(money.Max(ltNet, money.Dec{})), money.Dec{})
	p.GainHarvest = money.Min(p.ExemptLeft, p.LTGain)
	p.Notes = []string{
		"Estimates for listed shares only, at short-term 20% and long-term 12.5% above ₹1.25 lakh, with 4% cess. Surcharge, other income, F&O (business income) and intraday (speculative) results are not included.",
		"Losses are set off short-term against short-term or long-term gains, long-term against long-term only; what is left is carried forward for 8 years if the return is filed on time.",
		"Selling and buying back costs brokerage, STT and other charges. This is information from your account, not tax or investment advice.",
	}
	if p.UnknownCost > 0 {
		p.Notes = append(p.Notes, fmt.Sprintf("%d holding(s) have no purchase cost on record and are left out; add them under Trades elsewhere.", p.UnknownCost))
	}
	return p, nil
}

// ---- running the insight automations ----

func insightID(kind, ucc, period string) string {
	return "INS-" + strings.NewReplacer(" ", "", "/", "-", "|", "-", ":", "-").Replace(kind+"-"+ucc+"-"+period)
}

// raise records an insight found today and, in Auto, messages the client.
func (r *runCtx) raise(ctx context.Context, in Insight, remind bool) {
	o := r.o
	in.ID = insightID(in.Kind, in.UCC, in.Period)
	r.keys[in.ID] = true
	now := o.now().Format(time.RFC3339)
	old, _, err := docs.Get[Insight](ctx, o.Docs, r.tenant, cInsight, in.ID)
	if err == nil {
		// Keep what has happened to it; refresh the figures.
		in.Found, in.Status, in.SentAt, in.SentOn, in.Reminded, in.Channels, in.Calls = old.Found, old.Status, old.SentAt, old.SentOn, old.Reminded, old.Channels, old.Calls
		in.ActedOn, in.Trades, in.Turnover, in.By, in.Note, in.Held = old.ActedOn, old.Trades, old.Turnover, old.By, old.Note, old.Held
		if in.Status == InsExpired {
			in.Status = InsOpen
		}
	} else {
		in.Found, in.Status = r.date, InsOpen
	}
	in.Updated = now
	if in.Facts == nil {
		in.Facts = map[string]string{}
	}
	if r.auto() && (in.Status == InsOpen || (remind && in.Status == InsSent && in.Reminded == "" && in.SentOn < r.date)) {
		if why := o.insightHold(ctx, r.tenant, in, r.date); why != "" {
			in.Held = why
		} else {
			ms, err := o.sendEvent(ctx, r.tenant, SystemActor, "insight."+in.Kind, in.UCC, in.Facts, "")
			sent := 0
			for _, m := range ms {
				if m.Status == "Sent" {
					sent++
					if !contains(in.Channels, m.Channel) {
						in.Channels = append(in.Channels, m.Channel)
					}
				}
			}
			switch {
			case err != nil:
				in.Held = err.Error()
			case sent == 0:
				in.Held = "no active template reached the client"
			case in.Status == InsSent:
				in.Reminded, in.Held = now, ""
				r.done++
			default:
				in.Status, in.SentAt, in.SentOn, in.Held = InsSent, now, r.date, ""
				r.done++
			}
		}
	}
	_ = docs.Upsert(ctx, o.Docs, r.tenant, cInsight, in.ID, in, SystemActor)
}

// insightHold says why an insight should not be messaged now ("" to send).
func (o *Ops) insightHold(ctx context.Context, tenant string, in Insight, date string) string {
	c, err := o.Client(ctx, tenant, in.UCC)
	if err != nil {
		return "client not found"
	}
	if c.NoInsights {
		return "client turned insights off"
	}
	if c.Status != "Active" {
		return "account is not active"
	}
	if insightKind(in.Kind).Urgent {
		return ""
	}
	set, _ := o.Settings(ctx, tenant)
	days := set.InsightCoolOffDays
	if days <= 0 {
		days = 3
	}
	all, _ := docs.List[Insight](ctx, o.Docs, tenant, cInsight, "INS-")
	for _, x := range all {
		if x.UCC == in.UCC && x.ID != in.ID && x.SentOn != "" && x.SentOn <= date && daysBetween(x.SentOn, date) < int(days) {
			return fmt.Sprintf("another insight was sent on %s (one every %d days)", x.SentOn, days)
		}
	}
	return ""
}

// finishKind expires this kind's insights that no longer apply and, in
// Suggest, puts one item on the queue for the call list.
func (r *runCtx) finishKind(kind string) {
	all, _ := docs.List[Insight](r.ctx, r.o.Docs, r.tenant, cInsight, "INS-")
	open := 0
	for _, x := range all {
		if x.Kind != kind {
			continue
		}
		live := x.Status == InsOpen || x.Status == InsSent || x.Status == InsCalled
		if live && !r.keys[x.ID] && x.Found <= r.date {
			x.Status, x.Updated = InsExpired, r.o.now().Format(time.RFC3339)
			_ = docs.Upsert(r.ctx, r.o.Docs, r.tenant, cInsight, x.ID, x, SystemActor)
			continue
		}
		if x.Status == InsOpen && r.keys[x.ID] {
			open++
		}
	}
	if open > 0 && !r.auto() {
		k := insightKind(kind)
		r.queue("list", fmt.Sprintf("%s: %d client(s) to contact", k.Name, open), "Open Client insights to call them or send the message.", "", "", "insights", r.date+" 23:59", nil)
	}
	r.note("%d open", open)
}

func paramInt(p map[string]string, k string, def int64) int64 {
	if v, err := strconv.ParseInt(strings.TrimSpace(p[k]), 10, 64); err == nil && v > 0 {
		return v
	}
	return def
}

func paramRupees(p map[string]string, k, def string) money.Dec {
	if v, err := money.ParseDec(strings.ReplaceAll(strings.TrimSpace(p[k]), ",", "")); err == nil && v.Sign() > 0 {
		return v
	}
	return money.MustDec(def)
}

func rupees(d money.Dec) string {
	return strings.TrimSuffix(formatINR(money.Max(d, money.Dec{}).CeilRupee()), ".00")
}

// inWindow says whether date's month-day lies in [from, to] (MM-DD, may wrap).
func inWindow(date, from, to string) bool {
	md := date[5:]
	if from <= to {
		return md >= from && md <= to
	}
	return md >= from || md <= to
}

// autoTaxHarvest: between mid-January and the last settling day of the
// year, clients whose open losses would cut tax on gains already booked
// (or who can still book long-term gains tax-free).
func autoTaxHarvest(r *runCtx) error {
	ctx := r.ctx
	set, _ := r.o.Settings(ctx, r.tenant)
	_, _, fyEnd := fyOf(r.date)
	last := lastTradeDayIn(set, fyEnd)
	from := firstOf(r.params["from"], "01-15")
	if !inWindow(r.date, from, firstOf(r.params["to"], "03-31")) || r.date > last {
		r.note("outside the harvesting window (%s to %s)", from, last[5:])
		r.finishKind("tax_harvest")
		return nil
	}
	min := paramRupees(r.params, "min_saving", "1000")
	remindDays := int(paramInt(r.params, "remind_days", 7))
	cs, err := r.o.ClientMap(ctx, r.tenant)
	if err != nil {
		return err
	}
	for _, u := range sortedKeys(cs) {
		c := cs[u]
		if c.Status != "Active" {
			continue
		}
		p, err := r.o.TaxPlan(ctx, r.tenant, u, r.date)
		if err != nil || len(p.Losses)+len(p.Gains) == 0 {
			continue
		}
		worth := p.Saving
		gainTax := p.GainHarvest.Mul(rateLTCG).Mul(cessFactor) // tax a future sale avoids
		if worth.Cmp(min) < 0 && gainTax.Cmp(min) < 0 {
			continue
		}
		var syms []string
		for _, l := range p.Losses {
			if !contains(syms, l.Symbol) {
				syms = append(syms, l.Symbol)
			}
		}
		title := fmt.Sprintf("Could save about ₹%s tax by booking losses before %s", rupees(p.Saving), fmtDay(p.LastDay))
		detail := fmt.Sprintf("Booked this year: short-term ₹%s, long-term ₹%s. Open losses: short-term ₹%s, long-term ₹%s in %s.", rupees(p.STCG), rupees(p.LTCG), rupees(p.STLoss), rupees(p.LTLoss), strings.Join(syms, ", "))
		if p.Saving.Cmp(min) < 0 {
			title = fmt.Sprintf("Can book ₹%s of long-term gains tax-free before %s", rupees(p.GainHarvest), fmtDay(p.LastDay))
			detail = fmt.Sprintf("₹%s of this year's ₹1.25 lakh long-term exemption is unused; unrealised long-term gains ₹%s.", rupees(p.ExemptLeft), rupees(p.LTGain))
			worth = gainTax
		}
		r.raise(ctx, Insight{Kind: "tax_harvest", UCC: u, Name: c.Name, Period: "FY" + p.FY, Title: title, Detail: detail, Value: worth, Symbols: syms, Due: p.LastDay,
			Facts: map[string]string{"fy": p.FY, "stcg": rupees(p.STCG), "ltcg": rupees(p.LTCG), "st_loss": rupees(p.STLoss), "lt_loss": rupees(p.LTLoss),
				"saving": rupees(p.Saving), "exempt_left": rupees(p.ExemptLeft), "gain_harvest": rupees(p.GainHarvest), "last_day": fmtDay(p.LastDay), "symbols": strings.Join(syms, ", ")}},
			p.DaysLeft <= remindDays)
	}
	r.finishKind("tax_harvest")
	return nil
}

// autoBuyBack: holders of shares in an open buy-back priced above the
// market, who have not tendered yet. Reminder two days before it closes.
func autoBuyBack(r *runCtx) error {
	ctx := r.ctx
	bs, err := r.o.BuyBacks(ctx, r.tenant)
	if err != nil {
		return err
	}
	_, look, _ := r.o.Market(ctx, r.tenant, r.date)
	cs, _ := r.o.ClientMap(ctx, r.tenant)
	for _, b := range bs {
		if b.Status != "Open" || r.date > b.Close {
			continue
		}
		i, ok := look.ByISIN(b.ISIN)
		if !ok || i.LTP.Sign() <= 0 {
			r.note("%s: no market price", b.Symbol)
			continue
		}
		prem := b.Price.Sub(i.LTP)
		if prem.Sign() <= 0 {
			continue
		}
		premPct, _ := prem.MulInt(100).Quo(i.LTP)
		tendered := map[string]bool{}
		for _, x := range b.Bids {
			if x.Status == "Tendered" || x.Status == "Accepted" || x.Status == "Partly accepted" {
				tendered[x.UCC] = true
			}
		}
		hs, _ := r.o.Holdings(ctx, r.tenant, "")
		for _, h := range hs {
			free := h.Qty - h.Pledged
			c, okc := cs[h.UCC]
			if h.ISIN != b.ISIN || free <= 0 || tendered[h.UCC] || !okc || c.Status != "Active" {
				continue
			}
			value := i.LTP.MulInt(h.Qty)
			cat, ratio := "General", b.GeneralRatio
			if value.Cmp(smallHolder) <= 0 {
				cat, ratio = "Small shareholder (reserved)", b.SmallRatio
			}
			ent := entitlement(free, ratio)
			gain := prem.MulInt(ent)
			entTxt := "as per the letter of offer"
			if ent > 0 {
				entTxt = fmt.Sprintf("%d shares", ent)
			}
			r.raise(ctx, Insight{Kind: "buyback", UCC: h.UCC, Name: c.Name, Period: b.ID, Due: b.Close, Symbols: []string{b.Symbol},
				Title:  fmt.Sprintf("%s buy-back at ₹%s, %s%% above the market", b.Symbol, b.Price.Fixed(2), premPct.Fixed(1)),
				Detail: fmt.Sprintf("Holds %d (%d free). Category: %s; entitlement %s. Tender by %s.", h.Qty, free, cat, entTxt, fmtDay(b.Close)),
				Value:  gain,
				Facts: map[string]string{"symbol": b.Symbol, "company": firstOf(b.Company, b.Symbol), "price": b.Price.Fixed(2), "ltp": i.LTP.Fixed(2), "premium_pct": premPct.Fixed(1),
					"held": fmt.Sprint(free), "category": cat, "entitlement": entTxt, "record": fmtDay(b.Record), "close": fmtDay(b.Close)}},
				daysBetween(r.date, b.Close) <= 2)
		}
	}
	r.finishKind("buyback")
	return nil
}

// entitlement applies a ratio written "a/b" (a shares for every b held).
func entitlement(held int64, ratio string) int64 {
	a, b, ok := strings.Cut(strings.ReplaceAll(ratio, " ", ""), "/")
	if !ok {
		a, b, ok = strings.Cut(strings.ReplaceAll(ratio, " ", ""), ":")
	}
	na, e1 := strconv.ParseInt(a, 10, 64)
	nb, e2 := strconv.ParseInt(b, 10, 64)
	if !ok || e1 != nil || e2 != nil || na <= 0 || nb <= 0 {
		return 0
	}
	return min(held, held*na/nb)
}

// autoFOExpiry: stock futures, and stock options in the money, expiring
// within a few business days are settled by delivery.
func autoFOExpiry(r *runCtx) error {
	ctx := r.ctx
	set, _ := r.o.Settings(ctx, r.tenant)
	days := int(paramInt(r.params, "days", 3))
	horizon := r.date
	for i := 0; i < days; i++ {
		horizon = set.nextBusinessDay(horizon)
	}
	rv, err := r.o.Risk(ctx, r.tenant, r.date)
	if err != nil {
		return err
	}
	_, look, _ := r.o.Market(ctx, r.tenant, r.date)
	cs, _ := r.o.ClientMap(ctx, r.tenant)
	for _, u := range sortedKeys(rv.Positions) {
		for _, p := range rv.Positions[u] {
			parts := strings.Split(p.Instrument, "|")
			if len(parts) < 4 || p.Index || p.Qty == 0 || p.Segment != "FO" {
				continue
			}
			sym, typ, exp, strike := parts[0], parts[1], parts[2], parts[3]
			if exp == "" || exp < r.date || exp > horizon {
				continue
			}
			ins, _ := look.Get(p.Exchange, p.Segment, p.Instrument)
			und := ins.Underlying
			if und.Sign() <= 0 {
				if e, ok := look.Get("NSE", "CM", sym+"|EQ||"); ok {
					und = e.LTP
				}
			}
			k, _ := money.ParseDec(strike)
			long := p.Qty > 0
			n := abs(p.Qty)
			var action string
			var value money.Dec
			switch typ {
			case "FUT":
				value = und.MulInt(n)
				action = map[bool]string{true: "take delivery of", false: "deliver"}[long]
			case "CE", "PE":
				itm := (typ == "CE" && und.Cmp(k) > 0) || (typ == "PE" && und.Cmp(k) < 0)
				if !itm || und.Sign() <= 0 {
					continue
				}
				value = k.MulInt(n)
				buys := (typ == "CE") == long // long call or short put ends in a purchase
				action = map[bool]string{true: "take delivery of", false: "deliver"}[buys]
			default:
				continue
			}
			c := cs[u]
			r.raise(ctx, Insight{Kind: "fo_expiry", UCC: u, Name: c.Name, Period: p.Instrument, Due: exp, Symbols: []string{sym}, Value: value,
				Title:  fmt.Sprintf("%s expires %s: would %s %d %s", p.Name, fmtDay(exp), action, n, sym),
				Detail: fmt.Sprintf("Stock derivatives held at expiry settle by delivery: about ₹%s. Close or roll over before expiry to avoid it.", rupees(value)),
				Facts:  map[string]string{"contract": p.Name, "expiry": fmtDay(exp), "action": action, "qty": fmt.Sprint(n), "symbol": sym, "value": rupees(value)}},
				exp == set.nextBusinessDay(r.date) || exp == r.date)
		}
	}
	r.finishKind("fo_expiry")
	return nil
}

// autoAdvanceTax: in the ten days before an advance-tax instalment, clients
// with capital gains booked this year.
func autoAdvanceTax(r *runCtx) error {
	ctx := r.ctx
	due := ""
	for _, md := range advanceTaxes {
		y := r.date[:4]
		d := y + "-" + md
		if days := daysBetween(r.date, d); days >= 0 && days <= int(paramInt(r.params, "days", 10)) {
			due = d
		}
	}
	if due == "" {
		r.note("no instalment due soon")
		r.finishKind("advance_tax")
		return nil
	}
	min := paramRupees(r.params, "min_gain", "50000")
	cs, err := r.o.ClientMap(ctx, r.tenant)
	if err != nil {
		return err
	}
	_, from, _ := fyOf(r.date)
	for _, u := range sortedKeys(cs) {
		c := cs[u]
		if c.Status != "Active" {
			continue
		}
		_, sales, _, err := r.o.equityBook(ctx, r.tenant, u, r.date)
		if err != nil {
			continue
		}
		g := gainsBy(sales, from, r.date)
		total := g[CatSTCG].Add(g[CatLTCG])
		if total.Cmp(min) < 0 {
			continue
		}
		tax := capTax(g[CatSTCG], g[CatLTCG])
		r.raise(ctx, Insight{Kind: "advance_tax", UCC: u, Name: c.Name, Period: due, Due: due, Value: total,
			Title:  fmt.Sprintf("Advance tax instalment due %s; gains booked this year ₹%s", fmtDay(due), rupees(total)),
			Detail: fmt.Sprintf("Short-term ₹%s, long-term ₹%s; estimated tax on them ₹%s.", rupees(g[CatSTCG]), rupees(g[CatLTCG]), rupees(tax)),
			Facts:  map[string]string{"due": fmtDay(due), "stcg": rupees(g[CatSTCG]), "ltcg": rupees(g[CatLTCG]), "tax": rupees(tax), "total": rupees(total)}}, false)
	}
	r.finishKind("advance_tax")
	return nil
}

// autoCorpAction: dividends, bonuses and splits with a record date in the
// next days, for clients who hold the shares.
func autoCorpAction(r *runCtx) error {
	ctx := r.ctx
	cas, err := r.o.CorpActions(ctx, r.tenant)
	if err != nil {
		return err
	}
	days := int(paramInt(r.params, "days", 10))
	cs, _ := r.o.ClientMap(ctx, r.tenant)
	hs, _ := r.o.Holdings(ctx, r.tenant, "")
	for _, ca := range cas {
		if ca.Status != "Announced" || ca.RecordDate < r.date || daysBetween(r.date, ca.RecordDate) > days {
			continue
		}
		what := ca.Type
		switch ca.Type {
		case "Dividend":
			what = fmt.Sprintf("dividend of ₹%s a share", ca.DPS.Fixed(2))
		case "Bonus":
			what = fmt.Sprintf("bonus of %d for every %d held", ca.RatioNew, ca.RatioOld)
		case "Split":
			what = fmt.Sprintf("split from face value ₹%d to ₹%d", ca.RatioOld, ca.RatioNew)
		}
		for _, h := range hs {
			c, ok := cs[h.UCC]
			if h.ISIN != ca.ISIN || h.Qty <= 0 || !ok || c.Status != "Active" {
				continue
			}
			v := money.Dec{}
			if ca.Type == "Dividend" {
				v = ca.DPS.MulInt(h.Qty)
			}
			r.raise(ctx, Insight{Kind: "corp_action", UCC: h.UCC, Name: c.Name, Period: ca.ID, Due: ca.RecordDate, Symbols: []string{ca.Symbol}, Value: v,
				Title:  fmt.Sprintf("%s: %s, record date %s", ca.Symbol, what, fmtDay(ca.RecordDate)),
				Detail: fmt.Sprintf("Holds %d. Ex-date %s.", h.Qty, fmtDay(ca.ExDate)),
				Facts:  map[string]string{"symbol": ca.Symbol, "what": what, "record": fmtDay(ca.RecordDate), "ex": fmtDay(ca.ExDate), "held": fmt.Sprint(h.Qty), "amount": rupees(v)}}, false)
		}
	}
	r.finishKind("corp_action")
	return nil
}

// autoIdleFunds: clients with a credit balance, no positions and no trade
// for a month: they can withdraw it at any time.
func autoIdleFunds(r *runCtx) error {
	ctx := r.ctx
	min := paramRupees(r.params, "min", "25000")
	days := int(paramInt(r.params, "days", 30))
	rv, err := r.o.Risk(ctx, r.tenant, r.date)
	if err != nil {
		return err
	}
	cs, _ := r.o.ClientMap(ctx, r.tenant)
	month := r.date[:7]
	for _, row := range rv.Rows {
		c := cs[row.UCC]
		if row.Funds.Cmp(min) < 0 || row.Positions > 0 || c.Status != "Active" || (c.LastTrade != "" && daysBetween(c.LastTrade, r.date) < days) {
			continue
		}
		since := firstOf(c.LastTrade, "account opening")
		if c.LastTrade != "" {
			since = fmtDay(c.LastTrade)
		}
		r.raise(ctx, Insight{Kind: "idle_funds", UCC: row.UCC, Name: c.Name, Period: month, Value: row.Funds,
			Title:  fmt.Sprintf("₹%s lying in the trading account", rupees(row.Funds)),
			Detail: fmt.Sprintf("No trade since %s and no open positions.", since),
			Facts:  map[string]string{"amount": rupees(row.Funds), "since": since}}, false)
	}
	r.finishKind("idle_funds")
	return nil
}

// autoPledgeMargin: derivatives clients whose unpledged holdings could be
// pledged as margin.
func autoPledgeMargin(r *runCtx) error {
	ctx := r.ctx
	min := paramRupees(r.params, "min", "50000")
	cs, _ := r.o.ClientMap(ctx, r.tenant)
	_, look, _ := r.o.Market(ctx, r.tenant, r.date)
	hs, _ := r.o.Holdings(ctx, r.tenant, "")
	type acc struct {
		margin money.Dec
		n      int
		syms   []string
	}
	by := map[string]*acc{}
	for _, h := range hs {
		free := h.Qty - h.Pledged
		if free <= 0 {
			continue
		}
		i, ok := look.ByISIN(h.ISIN)
		if !ok || i.LTP.Sign() <= 0 || i.HaircutPct.Cmp(money.DecInt(100)) >= 0 {
			continue
		}
		a := by[h.UCC]
		if a == nil {
			a = &acc{}
			by[h.UCC] = a
		}
		a.margin = a.margin.Add(i.LTP.MulInt(free).Sub(i.LTP.MulInt(free).Pct(i.HaircutPct)))
		a.n++
		if len(a.syms) < 4 {
			a.syms = append(a.syms, h.Symbol)
		}
	}
	quarter := fmt.Sprintf("%s-Q%d", r.date[:4], (int(r.date[5]-'0')*10+int(r.date[6]-'0')-1)/3+1)
	for _, u := range sortedKeys(by) {
		a, c := by[u], cs[u]
		if a.margin.Cmp(min) < 0 || c.Status != "Active" || !(contains(c.Segments, "FO") || contains(c.Segments, "CD") || contains(c.Segments, "COM")) {
			continue
		}
		r.raise(ctx, Insight{Kind: "pledge_margin", UCC: u, Name: c.Name, Period: quarter, Value: a.margin, Symbols: a.syms,
			Title:  fmt.Sprintf("₹%s of margin available by pledging holdings", rupees(a.margin)),
			Detail: fmt.Sprintf("%d unpledged holding(s), e.g. %s, after haircuts.", a.n, strings.Join(a.syms, ", ")),
			Facts:  map[string]string{"margin": rupees(a.margin), "count": fmt.Sprint(a.n)}}, false)
	}
	r.finishKind("pledge_margin")
	return nil
}

// autoDormant: accounts that will be flagged inactive within the notice
// period unless there is activity.
func autoDormant(r *runCtx) error {
	ctx := r.ctx
	days := int(paramInt(r.params, "days", 60))
	v, err := r.o.Dormancy(ctx, r.tenant, r.date)
	if err != nil {
		return err
	}
	for _, row := range v.Rows {
		if row.State != "Approaching" || row.DaysLeft < 0 || row.DaysLeft > days {
			continue
		}
		assets := row.Funds.Add(row.Securities)
		r.raise(ctx, Insight{Kind: "dormant", UCC: row.UCC, Name: row.Name, Period: row.InactiveFrom, Due: row.InactiveFrom, Value: assets,
			Title:  fmt.Sprintf("Account turns inactive on %s", fmtDay(row.InactiveFrom)),
			Detail: fmt.Sprintf("Last activity %s (%s). Funds ₹%s, holdings ₹%s.", fmtDay(row.LastActivity), strings.ToLower(row.ActivityKind), rupees(row.Funds), rupees(row.Securities)),
			Facts:  map[string]string{"date": fmtDay(row.InactiveFrom), "last": fmtDay(row.LastActivity)}}, row.DaysLeft <= 7)
	}
	r.finishKind("dormant")
	return nil
}

// autoInsightOutcomes marks insights acted on: the client traded in the
// symbols concerned (or at all, for account-level insights), tendered, or
// rolled over within 15 days.
func autoInsightOutcomes(r *runCtx) error {
	ctx := r.ctx
	all, err := docs.List[Insight](ctx, r.o.Docs, r.tenant, cInsight, "INS-")
	if err != nil {
		return err
	}
	ts, err := r.o.exchangeTrades(ctx, r.tenant, r.date)
	if err != nil {
		return err
	}
	type tr struct {
		syms  map[string]bool
		sells map[string]bool
		n     int
		value money.Dec
	}
	by := map[string]*tr{}
	for _, t := range ts {
		x := by[t.UCC]
		if x == nil {
			x = &tr{syms: map[string]bool{}, sells: map[string]bool{}}
			by[t.UCC] = x
		}
		x.syms[t.Symbol] = true
		if t.Side == "S" {
			x.sells[t.Symbol] = true
		}
		x.n++
		x.value = x.value.Add(tradeValue(t))
	}
	bbs, _ := r.o.BuyBacks(ctx, r.tenant)
	tendered := map[string]bool{}
	for _, b := range bbs {
		for _, x := range b.Bids {
			if x.Status != "Cancelled" {
				tendered[b.ID+"|"+x.UCC] = true
			}
		}
	}
	for _, in := range all {
		if in.Status != InsSent && in.Status != InsCalled && in.Status != InsOpen {
			continue
		}
		start := firstOf(in.SentOn, in.Found)
		if r.date < start || daysBetween(start, r.date) > actWindow {
			continue
		}
		t := by[in.UCC]
		acted := false
		switch in.Kind {
		case "buyback":
			acted = tendered[in.Period+"|"+in.UCC]
		case "tax_harvest":
			for _, s := range in.Symbols {
				acted = acted || (t != nil && t.sells[s])
			}
		case "fo_expiry", "corp_action", "pledge_margin":
			for _, s := range in.Symbols {
				acted = acted || (t != nil && t.syms[s])
			}
		default:
			acted = t != nil
		}
		if !acted {
			continue
		}
		if in.Status != InsActed {
			in.Status, in.ActedOn = InsActed, r.date
			r.done++
		}
		if t != nil {
			in.Trades += t.n
			in.Turnover = in.Turnover.Add(t.value)
		}
		_ = docs.Upsert(ctx, r.o.Docs, r.tenant, cInsight, in.ID, in, SystemActor)
	}
	return nil
}

// ---- staff screen ----

// InsightsView is the Client insights screen.
type InsightsView struct {
	Date     string            `json:"date"`
	Kinds    []InsightKind     `json:"kinds"`
	Items    []Insight         `json:"items"`
	Counts   map[string]int    `json:"counts"`
	Policies map[string]string `json:"policies"` // kind -> mode
	Sent     int               `json:"sent"`
	Acted    int               `json:"acted"`
	Turnover money.Dec         `json:"turnover"`
	Value    money.Dec         `json:"value"` // open opportunity value
}

// Insights lists insights, newest first, live ones first.
func (o *Ops) Insights(ctx context.Context, tenant, date string, scope func(string) bool) (InsightsView, error) {
	v := InsightsView{Date: date, Kinds: InsightKinds, Items: []Insight{}, Counts: map[string]int{}, Policies: map[string]string{}}
	for _, k := range InsightKinds {
		_, mode, _ := o.policy(ctx, tenant, "ins_"+k.ID)
		v.Policies[k.ID] = mode
	}
	all, err := docs.List[Insight](ctx, o.Docs, tenant, cInsight, "INS-")
	if err != nil {
		return v, err
	}
	for _, x := range all {
		if scope != nil && !scope(x.UCC) {
			continue
		}
		if x.Status == InsExpired && x.Due != "" && daysBetween(x.Due, date) > 30 {
			continue
		}
		v.Items = append(v.Items, x)
		v.Counts[x.Status]++
		if x.Status == InsOpen || x.Status == InsSent || x.Status == InsCalled {
			v.Counts["live:"+x.Kind]++
			v.Value = v.Value.Add(x.Value)
		}
		if x.SentAt != "" || len(x.Calls) > 0 {
			v.Sent++
		}
		if x.Status == InsActed {
			v.Acted++
			v.Turnover = v.Turnover.Add(x.Turnover)
		}
	}
	rank := map[string]int{InsOpen: 0, InsCalled: 1, InsSent: 2, InsActed: 3, InsDismissed: 4, InsExpired: 5}
	sort.SliceStable(v.Items, func(i, j int) bool {
		a, b := v.Items[i], v.Items[j]
		if rank[a.Status] != rank[b.Status] {
			return rank[a.Status] < rank[b.Status]
		}
		return a.Value.Cmp(b.Value) > 0
	})
	return v, nil
}

// ActOnInsight sends an insight's message, logs a call, or dismisses it.
func (o *Ops) ActOnInsight(ctx context.Context, tenant, actor, id, action, outcome, text string) (Insight, error) {
	if err := o.require(ctx, tenant, actor, "Working client insights", RoleMaker, RoleChecker, RoleCompliance, RoleBranch, RoleAP); err != nil {
		return Insight{}, err
	}
	in, _, err := docs.Get[Insight](ctx, o.Docs, tenant, cInsight, id)
	if err != nil {
		return in, err
	}
	if in.Status == InsExpired {
		return in, fmt.Errorf("%w: this insight has expired", ErrState)
	}
	now := o.now().Format(time.RFC3339)
	switch action {
	case "send":
		if c, err := o.Client(ctx, tenant, in.UCC); err == nil && c.NoInsights {
			return in, invalid("%s has turned insights off in the portal", in.UCC)
		}
		ms, err := o.sendEvent(ctx, tenant, actor, "insight."+in.Kind, in.UCC, in.Facts, "")
		if err != nil {
			return in, err
		}
		sent := 0
		for _, m := range ms {
			if m.Status == "Sent" {
				sent++
				if !contains(in.Channels, m.Channel) {
					in.Channels = append(in.Channels, m.Channel)
				}
			}
		}
		if sent == 0 {
			return in, invalid("no message went out: check the client's email and mobile and the active templates for this insight")
		}
		if in.SentAt == "" {
			in.SentAt, in.SentOn = now, o.businessDate(ctx, tenant)
		} else {
			in.Reminded = now
		}
		if in.Status == InsOpen {
			in.Status = InsSent
		}
		in.Held = ""
	case "call":
		if !contains([]string{"Interested", "Will act", "Not interested", "No answer", "Already done"}, outcome) {
			return in, invalid("call outcome must be Interested, Will act, Not interested, No answer or Already done")
		}
		in.Calls = append(in.Calls, InsightCall{At: now, By: actor, Outcome: outcome, Text: strings.TrimSpace(text)})
		switch {
		case outcome == "Not interested":
			in.Status, in.Note, in.By = InsDismissed, "Client not interested", actor
		case outcome == "Already done":
			in.Status, in.Note, in.By = InsDismissed, "Client had already done it", actor
		case in.Status == InsOpen || in.Status == InsSent:
			in.Status = InsCalled
		}
	case "dismiss":
		if strings.TrimSpace(text) == "" {
			return in, invalid("say why it is dismissed")
		}
		in.Status, in.Note, in.By = InsDismissed, text, actor
	default:
		return in, invalid("action must be send, call or dismiss")
	}
	in.Updated = now
	if err := docs.Upsert(ctx, o.Docs, tenant, cInsight, in.ID, in, actor); err != nil {
		return in, err
	}
	o.log(ctx, tenant, actor, "insight."+action, in.ID, map[string]any{"ucc": in.UCC, "kind": in.Kind, "outcome": outcome})
	return in, nil
}

// InsightByID returns one insight.
func (o *Ops) InsightByID(ctx context.Context, tenant, id string) (Insight, error) {
	in, _, err := docs.Get[Insight](ctx, o.Docs, tenant, cInsight, id)
	return in, err
}

// FindInsights runs every insight automation now, in Suggest at most, so
// staff can see today's opportunities without messaging anyone.
func (o *Ops) FindInsights(ctx context.Context, tenant, actor, date string) (int, error) {
	if err := o.require(ctx, tenant, actor, "Finding client insights", RoleMaker, RoleChecker, RoleCompliance, RoleAdmin); err != nil {
		return 0, err
	}
	if date == "" {
		date = o.businessDate(ctx, tenant)
	}
	n := 0
	for _, k := range InsightKinds {
		d, mode, params := o.policy(ctx, tenant, "ins_"+k.ID)
		if mode == ModeOff {
			continue
		}
		r := &runCtx{o: o, ctx: ctx, tenant: tenant, date: date, mode: ModeSuggest, params: params, process: d.ID, keys: map[string]bool{}}
		if err := autoFns[d.ID](r); err != nil {
			return n, fmt.Errorf("%s: %w", d.Name, err)
		}
		for k := range r.keys {
			if strings.HasPrefix(k, "INS-") {
				n++
			}
		}
	}
	o.log(ctx, tenant, actor, "insights.find", date, map[string]any{"found": n})
	return n, nil
}

// ---- portal ----

// PortalInsights is the client's Tax & insights page.
type PortalInsights struct {
	UCC      string          `json:"ucc"`
	Off      bool            `json:"off"`
	Items    []Insight       `json:"items"`
	Tax      TaxPlan         `json:"tax"`
	BuyBacks []PortalBuyBack `json:"buybacks"`
}

// PortalBuyBack is an open buy-back on shares the client holds.
type PortalBuyBack struct {
	ID          string    `json:"id"`
	Symbol      string    `json:"symbol"`
	Company     string    `json:"company"`
	Price       money.Dec `json:"price"`
	LTP         money.Dec `json:"ltp"`
	Close       string    `json:"close"`
	Free        int64     `json:"free"`
	Category    string    `json:"category"`
	Entitlement int64     `json:"entitlement"`
	Tendered    int64     `json:"tendered"`
	Open        bool      `json:"open"`
}

// ClientInsights builds the portal page for one of the actor's accounts.
func (o *Ops) ClientInsights(ctx context.Context, tenant, actor, date, ucc string) (PortalInsights, error) {
	v := PortalInsights{Items: []Insight{}, BuyBacks: []PortalBuyBack{}}
	accts, err := o.PortalAccounts(ctx, tenant, actor)
	if err != nil {
		return v, err
	}
	ucc = strings.ToUpper(ucc)
	if ucc == "" && len(accts) > 0 {
		ucc = accts[0]
	}
	if !contains(accts, ucc) {
		return v, ErrNotFound
	}
	v.UCC = ucc
	c, err := o.Client(ctx, tenant, ucc)
	if err != nil {
		return v, err
	}
	v.Off = c.NoInsights
	all, _ := docs.List[Insight](ctx, o.Docs, tenant, cInsight, "INS-")
	for _, x := range all {
		if x.UCC == ucc && (x.Status == InsOpen || x.Status == InsSent || x.Status == InsCalled) {
			x.Calls, x.Note, x.By, x.Held = nil, "", "", "" // staff notes stay internal
			v.Items = append(v.Items, x)
		}
	}
	sort.SliceStable(v.Items, func(i, j int) bool { return v.Items[i].Due < v.Items[j].Due })
	if v.Tax, err = o.TaxPlan(ctx, tenant, ucc, date); err != nil {
		return v, err
	}
	bs, _ := o.BuyBacks(ctx, tenant)
	_, look, _ := o.Market(ctx, tenant, date)
	for _, b := range bs {
		if b.Status != "Open" || date > b.Close {
			continue
		}
		h, _, err := docs.Get[Holding](ctx, o.Docs, tenant, cHolding, holdingID(ucc, b.ISIN))
		if err != nil || h.Qty <= 0 {
			continue
		}
		pb := PortalBuyBack{ID: b.ID, Symbol: b.Symbol, Company: firstOf(b.Company, b.Symbol), Price: b.Price, Close: b.Close, Free: h.Qty - h.Pledged, Open: date >= b.Open}
		if i, ok := look.ByISIN(b.ISIN); ok {
			pb.LTP = i.LTP
		}
		pb.Category, pb.Entitlement = "General", entitlement(pb.Free, b.GeneralRatio)
		if pb.LTP.MulInt(h.Qty).Cmp(smallHolder) <= 0 {
			pb.Category, pb.Entitlement = "Small shareholder (reserved)", entitlement(pb.Free, b.SmallRatio)
		}
		for _, x := range b.Bids {
			if x.UCC == ucc && x.Status == "Tendered" {
				pb.Tendered = x.Qty
			}
		}
		v.BuyBacks = append(v.BuyBacks, pb)
	}
	return v, nil
}

// SetInsightsOff turns a client's insight messages off or back on.
func (o *Ops) SetInsightsOff(ctx context.Context, tenant, actor, ucc string, off bool) (Client, error) {
	accts, err := o.PortalAccounts(ctx, tenant, actor)
	if err != nil {
		return Client{}, err
	}
	ucc = strings.ToUpper(ucc)
	if !contains(accts, ucc) {
		return Client{}, ErrNotFound
	}
	c, err := docs.Update(ctx, o.Docs, tenant, cClient, ucc, actor, func(c *Client) error {
		c.NoInsights = off
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "client.insights", ucc, map[string]any{"off": off})
	}
	return c, err
}

// ClientTenderBuyBack is a client's own tender from the portal.
func (o *Ops) ClientTenderBuyBack(ctx context.Context, tenant, actor, date, id, ucc string, qty int64) (BuyBack, error) {
	accts, err := o.PortalAccounts(ctx, tenant, actor)
	if err != nil {
		return BuyBack{}, err
	}
	if !contains(accts, strings.ToUpper(ucc)) {
		return BuyBack{}, ErrNotFound
	}
	return o.TenderBuyBack(ctx, tenant, actor, id, date, ucc, qty, true)
}
