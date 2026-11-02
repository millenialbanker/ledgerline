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

// ---- order-to-trade ratio (OTR) alerts ----
//
// The exchanges charge a trading member whose daily order-to-trade ratio
// is high. The rules, from the public circulars:
//
//   - SEBI/HO/MRD/DP/CIR/P/2018/62 (9 April 2018, para 14) set the
//     framework; BSE notice 20180622-41 and NSE circular NSE/SURV/38122
//     (161/2018), both 22 June 2018, applied it to the equity segment from
//     29 June 2018: below 50 nil; 50 to below 250, 2 paise an algo order;
//     250 to below 500, 10 paise; 500 or more, 10 paise and a cooling-off:
//     no orders for the first 15 minutes of the next trading day. A member
//     penalised more than ten times in the previous 30 trading days
//     (rolling) loses proprietary trading for the first hour of the next day.
//   - SEBI/HO/MRD1/DSAP/CIR/P/2020/107 (24 June 2020) let the exchanges
//     add slabs above 500 jointly; NSE circular NSE/SURV/45016 (447/2020,
//     14 July 2020, from 22 July 2020) set 500 to below 1000, 15 paise;
//     1000 to below 2000, 20 paise; 2000 or more, 25 paise (each "on an
//     incremental basis").
//   - SEBI HO/47/11/16(2)2025-MRD-POD2/I/4113/2026 (4 February 2026),
//     NSE/SURV/73597 (240/2026, 2 April 2026) and BSE notice 20260406-8
//     (6 April 2026), from 6 April 2026: orders within ±0.75% of the last
//     traded price stay out of the ratio, and for equity options the band
//     is ±40% of the LTP (premium) or ±₹20, whichever is higher;
//     algorithmic orders of designated market makers for market making are
//     not counted. The penal charges stay as before.
//
// Also excluded (2018 circulars): SME and ETF securities and those with a
// market maker; the odd lot, auction, block deal, pre-open, post-close,
// periodic call auction and IPO call auction sessions.
//
// Every order event counts: entry, modification and cancellation. The
// exchange computes the ratio at member level, daily, on algo orders only,
// and collects the charge monthly. Ledgerline works it out from the day's
// order log for the member, each client and each terminal (the dealer
// from the dealers register), as an early warning. Two things the order
// log does not carry are not applied: the last traded price when each
// order was entered (so the LTP bands are not taken out), and which
// scrips have a market maker. The ratio shown is therefore at or above the
// exchange's. With Count "algo" only terminals marked algo in the dealers
// register are counted; the default, "all", counts every order.
//
// The slabs and the alert level are settings, defaulting to the above.

const (
	cOTRSet = "otrset" // main
	cOTRDay = "otrday" // exchange|date
	wOTR    = "otr"    // work item process; key exchange|date|by|key

	// OTRCountAll and OTRCountAlgo are what the ratio counts.
	OTRCountAll  = "all"
	OTRCountAlgo = "algo"
)

// OTRSlab is a penalty slab: from this ratio (inclusive) up to the next
// slab, Paise for each algo order.
type OTRSlab struct {
	From  int `json:"from"`
	Paise int `json:"paise"`
}

// OTRSettings are the firm's OTR alert rules.
type OTRSettings struct {
	Off       bool      `json:"off"`
	AlertFrom int       `json:"alert_from"`  // ratio from which a work item is raised (default 50)
	CoolOffAt int       `json:"cool_off_at"` // ratio at which the exchange's cooling-off applies (default 500)
	Slabs     []OTRSlab `json:"slabs"`
	Count     string    `json:"count"` // all or algo
	UpdatedBy string    `json:"updated_by,omitempty"`
	UpdatedAt string    `json:"updated_at,omitempty"`
}

// DefaultOTRSlabs are the exchanges' slabs (NSE/SURV/45016, BSE notice
// 20180622-41 as extended under SEBI/HO/MRD1/DSAP/CIR/P/2020/107).
func DefaultOTRSlabs() []OTRSlab {
	return []OTRSlab{{50, 2}, {250, 10}, {500, 15}, {1000, 20}, {2000, 25}}
}

// DefaultOTRSettings: alerts on from 50, cooling-off at 500, every order counted.
func DefaultOTRSettings() OTRSettings {
	return OTRSettings{AlertFrom: 50, CoolOffAt: 500, Slabs: DefaultOTRSlabs(), Count: OTRCountAll}
}

// OTRSettings returns the rules (defaults until saved).
func (o *Ops) OTRSettings(ctx context.Context, tenant string) OTRSettings {
	s, _, err := docs.Get[OTRSettings](ctx, o.Docs, tenant, cOTRSet, "main")
	if err != nil {
		return DefaultOTRSettings()
	}
	d := DefaultOTRSettings()
	if s.AlertFrom <= 0 {
		s.AlertFrom = d.AlertFrom
	}
	if s.CoolOffAt <= 0 {
		s.CoolOffAt = d.CoolOffAt
	}
	if len(s.Slabs) == 0 {
		s.Slabs = d.Slabs
	}
	if s.Count == "" {
		s.Count = OTRCountAll
	}
	return s
}

// SaveOTRSettings checks and stores the rules.
func (o *Ops) SaveOTRSettings(ctx context.Context, tenant, actor string, s OTRSettings) (OTRSettings, error) {
	if err := o.require(ctx, tenant, actor, "Changing order-to-trade ratio alerts", RoleCompliance); err != nil {
		return s, err
	}
	d := DefaultOTRSettings()
	if s.AlertFrom == 0 {
		s.AlertFrom = d.AlertFrom
	}
	if s.CoolOffAt == 0 {
		s.CoolOffAt = d.CoolOffAt
	}
	if len(s.Slabs) == 0 {
		s.Slabs = d.Slabs
	}
	if s.Count == "" {
		s.Count = OTRCountAll
	}
	if s.AlertFrom < 1 || s.CoolOffAt < 1 {
		return s, invalid("the alert level and the cooling-off ratio must be 1 or more")
	}
	if s.Count != OTRCountAll && s.Count != OTRCountAlgo {
		return s, invalid("count %q: all or algo", s.Count)
	}
	for i, x := range s.Slabs {
		if x.From < 1 || x.Paise < 0 {
			return s, invalid("slab %d: the ratio must be 1 or more and the charge 0 or more paise", i+1)
		}
		if i > 0 && x.From <= s.Slabs[i-1].From {
			return s, invalid("slabs must go up: %d after %d", x.From, s.Slabs[i-1].From)
		}
	}
	s.UpdatedBy, s.UpdatedAt = actor, o.now().Format(time.RFC3339)
	if err := docs.Upsert(ctx, o.Docs, tenant, cOTRSet, "main", s, actor); err != nil {
		return s, err
	}
	o.log(ctx, tenant, actor, "otr.settings", "main", map[string]any{"off": s.Off, "alert_from": s.AlertFrom, "cool_off_at": s.CoolOffAt, "count": s.Count, "slabs": fmt.Sprint(s.Slabs)})
	return s, nil
}

// slabFor is the slab a ratio falls in (ok false below the first).
func (s OTRSettings) slabFor(ratio money.Dec) (OTRSlab, string, bool) {
	for i := len(s.Slabs) - 1; i >= 0; i-- {
		x := s.Slabs[i]
		if ratio.Cmp(money.DecInt(int64(x.From))) >= 0 {
			label := fmt.Sprintf("%d or more", x.From)
			if i+1 < len(s.Slabs) {
				label = fmt.Sprintf("%d to below %d", x.From, s.Slabs[i+1].From)
			}
			return x, label, true
		}
	}
	if len(s.Slabs) > 0 {
		return OTRSlab{}, fmt.Sprintf("below %d", s.Slabs[0].From), false
	}
	return OTRSlab{}, "", false
}

// OTRLine is one member, client or terminal on a day.
type OTRLine struct {
	By      string    `json:"by"` // Member, Client, Terminal
	Key     string    `json:"key"`
	Name    string    `json:"name,omitempty"` // client name, or the terminal's dealer
	Orders  int       `json:"orders"`
	Trades  int       `json:"trades"`
	Ratio   money.Dec `json:"ratio"`
	Slab    string    `json:"slab"`
	Paise   int       `json:"paise"`
	Over    bool      `json:"over"` // at or above the alert level
	CoolOff bool      `json:"cool_off"`
}

// OTRDay is a day's ratios on one exchange.
type OTRDay struct {
	ID       string    `json:"id"`
	Exchange string    `json:"exchange"`
	Date     string    `json:"date"`
	File     string    `json:"file"`
	Count    string    `json:"count"`
	Lines    []OTRLine `json:"lines"`
	Over     int       `json:"over"`
}

type otrOrder struct{ ucc, terminal string }
type otrTrade struct{ ucc, terminal string }

// otrCheck works out the day's ratios from an order log and the day's
// trades, keeps them, and raises a compliance work item for each member,
// client or terminal at or above the alert level (closing those of an
// earlier load of the same day that no longer are).
func (o *Ops) otrCheck(ctx context.Context, tenant, actor, exchange, date, file string, orders []otrOrder, trades []otrTrade) OTRDay {
	st := o.OTRSettings(ctx, tenant)
	day := OTRDay{ID: exchange + "|" + date, Exchange: exchange, Date: date, File: file, Count: st.Count, Lines: []OTRLine{}}
	ds, _ := o.allDealers(ctx, tenant)
	book := newDealerBook(ds)
	algo := func(terminal string) bool {
		_, t, ok := book.findTerminal(exchange, terminal, date)
		return ok && t.Algo
	}
	ord := map[string]map[string]int{"Member": {}, "Client": {}, "Terminal": {}}
	trd := map[string]map[string]int{"Member": {}, "Client": {}, "Terminal": {}}
	count := func(m map[string]map[string]int, ucc, terminal string) {
		if st.Count == OTRCountAlgo && !algo(terminal) {
			return
		}
		m["Member"][exchange]++
		if ucc != "" {
			m["Client"][ucc]++
		}
		if terminal != "" {
			m["Terminal"][terminal]++
		}
	}
	for _, x := range orders {
		count(ord, x.ucc, x.terminal)
	}
	for _, x := range trades {
		count(trd, x.ucc, x.terminal)
	}
	cs, _ := o.ClientMap(ctx, tenant)
	for _, by := range []string{"Member", "Client", "Terminal"} {
		for _, k := range sortedKeys(ord[by]) {
			l := OTRLine{By: by, Key: k, Orders: ord[by][k], Trades: trd[by][k]}
			l.Ratio, _ = money.DecInt(int64(l.Orders)).Quo(money.DecInt(int64(max(l.Trades, 1))))
			sl, label, _ := st.slabFor(l.Ratio)
			l.Slab, l.Paise = label, sl.Paise
			l.Over = l.Ratio.Cmp(money.DecInt(int64(st.AlertFrom))) >= 0
			l.CoolOff = l.Ratio.Cmp(money.DecInt(int64(st.CoolOffAt))) >= 0
			switch by {
			case "Client":
				l.Name = cs[k].Name
			case "Terminal":
				if i, ok := book.find(exchange, k, date); ok {
					l.Name = ds[i].ID + " " + ds[i].Name()
				}
			case "Member":
				l.Name = "The firm (all " + map[string]string{OTRCountAll: "orders", OTRCountAlgo: "algo orders"}[st.Count] + ")"
			}
			if l.Over {
				day.Over++
			}
			day.Lines = append(day.Lines, l)
		}
	}
	rank := map[string]int{"Member": 0, "Client": 1, "Terminal": 2}
	sort.SliceStable(day.Lines, func(i, j int) bool {
		a, b := day.Lines[i], day.Lines[j]
		if a.By != b.By {
			return rank[a.By] < rank[b.By]
		}
		return a.Ratio.Cmp(b.Ratio) > 0
	})
	_ = docs.Upsert(ctx, o.Docs, tenant, cOTRDay, day.ID, day, actor)
	raised := map[string]bool{}
	if !st.Off {
		for _, l := range day.Lines {
			if !l.Over {
				continue
			}
			key := exchange + "|" + date + "|" + l.By + "|" + l.Key
			raised[key] = true
			who := strings.ToLower(l.By) + " " + l.Key
			if l.Name != "" {
				who += " (" + l.Name + ")"
			}
			detail := fmt.Sprintf("%d order events (entries, changes and cancellations) against %d trades on %s on %s: ratio %s, slab %s", l.Orders, l.Trades, exchange, fmtDay(date), l.Ratio.Fixed(2), l.Slab)
			if l.Paise > 0 {
				detail += fmt.Sprintf(", %d paise an algo order", l.Paise)
			}
			detail += ". The exchange charges the member on its algo orders, daily, collected monthly (SEBI/HO/MRD/DP/CIR/P/2018/62; BSE notice 20180622-41; NSE/SURV/45016)."
			if l.CoolOff {
				detail += fmt.Sprintf(" At %d or more the exchange bars the member's orders for the first 15 minutes of the next trading day.", st.CoolOffAt)
			}
			detail += " Ledgerline cannot take out orders within the LTP band (the order log has no LTP), so the exchange's figure may be lower. Review the strategy or the client's order entry."
			ucc := ""
			if l.By == "Client" {
				ucc = l.Key
			}
			o.raiseWork(ctx, tenant, wOTR, key, fmt.Sprintf("Order-to-trade ratio %s:1 for %s on %s", l.Ratio.Fixed(0), who, exchange), detail, ucc, RoleCompliance, "dealers", date)
		}
	}
	ws, _ := docs.List[WorkItem](ctx, o.Docs, tenant, cWorkItem, "")
	pre := wOTR + ":" + exchange + "|" + date + "|"
	for _, w := range ws {
		if w.Process == wOTR && w.Status == "Open" && strings.HasPrefix(w.Key, pre) && !raised[strings.TrimPrefix(w.Key, wOTR+":")] {
			o.closeWork(ctx, tenant, actor, wOTR, strings.TrimPrefix(w.Key, wOTR+":"), "Below the alert level on the order log loaded again")
		}
	}
	return day
}

// OTRView is GET /v1/otr: the days' ratios with the rules.
type OTRView struct {
	Settings OTRSettings `json:"settings"`
	Days     []OTRDay    `json:"days"`
	Basis    string      `json:"basis"`
}

// OTRReport lists the ratios worked out between two dates, newest first.
func (o *Ops) OTRReport(ctx context.Context, tenant, actor, from, to string) (OTRView, error) {
	v := OTRView{Settings: o.OTRSettings(ctx, tenant), Days: []OTRDay{}}
	if err := o.require(ctx, tenant, actor, "Viewing order-to-trade ratios", RoleRisk, RoleCompliance); err != nil {
		return v, err
	}
	if (from != "" && !validDate(from)) || (to != "" && !validDate(to)) {
		return v, invalid("give dates as YYYY-MM-DD")
	}
	all, err := docs.List[OTRDay](ctx, o.Docs, tenant, cOTRDay, "")
	if err != nil {
		return v, err
	}
	for _, d := range all {
		if (from != "" && d.Date < from) || (to != "" && d.Date > to) {
			continue
		}
		v.Days = append(v.Days, d)
	}
	sort.SliceStable(v.Days, func(i, j int) bool {
		if v.Days[i].Date != v.Days[j].Date {
			return v.Days[i].Date > v.Days[j].Date
		}
		return v.Days[i].Exchange < v.Days[j].Exchange
	})
	v.Basis = "From each day's order log: every order event (entry, change, cancellation) against the day's trades, for the firm, each client and each terminal. The exchange counts only algo orders outside the LTP band (±0.75%; equity options ±40% of the premium or ±₹20, whichever is higher) and leaves out market makers' orders, SME and ETF scrips and the call auction, pre-open and post-close sessions; the order log does not carry the LTP, so those bands are not applied here and the ratio shown is at or above the exchange's."
	return v, nil
}
