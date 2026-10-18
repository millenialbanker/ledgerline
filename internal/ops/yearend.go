package ops

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"sort"
	"strconv"
	"strings"
	"time"

	"ledgerline/internal/charges"
	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// ---- year-end packs ----
//
// After 31 March every client asks for the same papers for their tax
// return: the securities transaction tax certificate (Form No. 10DB under
// rule 20AB of the Income-tax Rules, one per exchange), capital gains for
// the year, charges paid, and the closing balance and holdings. The pack
// puts them in one document per client and financial year, available in
// the client portal and emailed in bulk.

const cYearEnd = "yearend"

// STTRow is one row of Form 10DB: an exchange and transaction code.
type STTRow struct {
	Exchange string    `json:"exchange"`
	Code     string    `json:"code"`
	Desc     string    `json:"desc"`
	Value    money.Dec `json:"value"`
	STT      money.Dec `json:"stt"`
	Trades   int       `json:"trades"`
}

// Form 10DB transaction codes.
var sttCodes = map[string]string{
	"01": "Purchase of equity shares or units of equity-oriented funds, settled by delivery",
	"02": "Sale of equity shares or units of equity-oriented funds, settled by delivery",
	"03": "Sale of equity shares or units of equity-oriented funds, settled otherwise than by delivery",
	"04": "Sale of an option in securities",
	"05": "Sale of a future in securities",
}

// sttCode is a trade's Form 10DB code ("" when STT does not apply).
func sttCode(b charges.Bucket, side string) string {
	switch {
	case b == charges.CMDelivery && side == "B":
		return "01"
	case b == charges.CMDelivery && side == "S":
		return "02"
	case b == charges.CMIntraday && side == "S":
		return "03"
	case b == charges.FOOpt && side == "S":
		return "04"
	case b == charges.FOFut && side == "S":
		return "05"
	}
	return ""
}

// fyRange turns "2026-27" into its first and last day.
func fyRange(fy string) (from, to string, err error) {
	y, err := strconv.Atoi(strings.SplitN(fy, "-", 2)[0])
	if err != nil || len(fy) != 7 || fy[4] != '-' || fmt.Sprintf("%02d", (y+1)%100) != fy[5:] {
		return "", "", invalid("financial year is written like 2026-27")
	}
	return fmt.Sprintf("%d-04-01", y), fmt.Sprintf("%d-03-31", y+1), nil
}

// sttForYear recomputes STT per client, exchange and code from the year's
// exchange trades, the same way the bills charged it.
func (o *Ops) sttForYear(ctx context.Context, tenant, from, to string, only string) (map[string][]STTRow, error) {
	agg := map[string]map[string]*STTRow{}
	for d := from; d <= to; d = addDays(d, 1) {
		ts, err := o.exchangeTrades(ctx, tenant, d)
		if err != nil {
			return nil, err
		}
		for _, t := range ts {
			if only != "" && t.UCC != only {
				continue
			}
			b, err := charges.BucketFor(t.Segment, t.InstrumentType, t.Product)
			if err != nil {
				continue
			}
			code := sttCode(b, t.Side)
			if code == "" {
				continue
			}
			bd, err := o.App.Schedule.Compute(charges.Leg{Bucket: b, Exchange: t.Exchange, Side: t.Side, Qty: t.Qty, Price: t.Price, Date: t.TradeDate, NoBrokerage: true})
			if err != nil {
				continue
			}
			if agg[t.UCC] == nil {
				agg[t.UCC] = map[string]*STTRow{}
			}
			k := t.Exchange + "|" + code
			l := agg[t.UCC][k]
			if l == nil {
				l = &STTRow{Exchange: t.Exchange, Code: code, Desc: sttCodes[code]}
				agg[t.UCC][k] = l
			}
			l.Value = l.Value.Add(bd.Turnover)
			l.STT = l.STT.Add(bd.STT.Dec())
			l.Trades++
		}
	}
	out := map[string][]STTRow{}
	for u, m := range agg {
		var ls []STTRow
		for _, l := range m {
			ls = append(ls, *l)
		}
		sort.Slice(ls, func(i, j int) bool {
			if ls[i].Exchange != ls[j].Exchange {
				return ls[i].Exchange < ls[j].Exchange
			}
			return ls[i].Code < ls[j].Code
		})
		out[u] = ls
	}
	return out, nil
}

// YearEndPack is one client's papers for one financial year.
type YearEndPack struct {
	UCC       string       `json:"ucc"`
	Name      string       `json:"name"`
	PAN       string       `json:"pan"`
	City      string       `json:"city"`
	FY        string       `json:"fy"`
	From      string       `json:"from"`
	To        string       `json:"to"` // the year's last day, or today while it runs
	Running   bool         `json:"running"`
	Firm      string       `json:"firm"`
	SEBIReg   string       `json:"sebi_reg"`
	Members   []string     `json:"members"`
	STT       []STTRow     `json:"stt"`
	STTTotal  money.Dec    `json:"stt_total"`
	Exchanges []string     `json:"exchanges"`
	STCG      money.Dec    `json:"stcg"`
	LTCG      money.Dec    `json:"ltcg"`
	Spec      money.Dec    `json:"speculative"`
	Deriv     money.Dec    `json:"derivatives"` // F&O and other derivative results (MTM and premium)
	Charges   []ChargeLine `json:"charges"`
	Closing   money.Dec    `json:"closing"` // ledger balance; + is the client's credit
	Holdings  money.Dec    `json:"holdings"`
	Generated string       `json:"generated"`
	SentAt    string       `json:"sent_at,omitempty"`
}

// ChargeLine is one charge head paid in the year.
type ChargeLine struct {
	Head   string    `json:"head"`
	Amount money.Dec `json:"amount"`
}

// YearEnd builds a client's pack for a financial year.
func (o *Ops) YearEnd(ctx context.Context, tenant, ucc, fy, today string) (YearEndPack, error) {
	from, to, err := fyRange(fy)
	if err != nil {
		return YearEndPack{}, err
	}
	c, err := o.Client(ctx, tenant, strings.ToUpper(ucc))
	if err != nil {
		return YearEndPack{}, err
	}
	if from > today {
		return YearEndPack{}, invalid("financial year %s has not started", fy)
	}
	p := YearEndPack{UCC: c.UCC, Name: c.Name, PAN: c.PAN, City: c.City, FY: fy, From: from, To: to, Generated: o.now().Format(time.RFC3339), Charges: []ChargeLine{}, STT: []STTRow{}}
	if today < to {
		p.To, p.Running = today, true
	}
	set, _ := o.Settings(ctx, tenant)
	p.Firm, p.SEBIReg = set.Firm, set.SEBIReg
	for _, k := range sortedKeys(set.MemberCodes) {
		p.Members = append(p.Members, k+" "+set.MemberCodes[k])
	}
	stt, err := o.sttForYear(ctx, tenant, from, p.To, c.UCC)
	if err != nil {
		return p, err
	}
	p.STT = append(p.STT, stt[c.UCC]...)
	for _, l := range p.STT {
		p.STTTotal = p.STTTotal.Add(l.STT)
		if !contains(p.Exchanges, l.Exchange) {
			p.Exchanges = append(p.Exchanges, l.Exchange)
		}
	}
	_, sales, _, err := o.equityBook(ctx, tenant, c.UCC, p.To)
	if err != nil {
		return p, err
	}
	g := gainsBy(sales, from, p.To)
	p.STCG, p.LTCG, p.Spec = g[CatSTCG], g[CatLTCG], g[CatSpec]
	bt, err := o.bills(ctx, tenant, c.UCC, from, p.To)
	if err != nil {
		return p, err
	}
	p.Deriv = bt.derivTotal()
	for _, h := range chargeHeads {
		if v, ok := bt.charges[h.label]; ok && !v.IsZero() {
			p.Charges = append(p.Charges, ChargeLine{h.label, v.Abs()})
		}
	}
	st, err := o.App.Ledger.Statement(ctx, tenant, ledger.ClientAccount(c.UCC), "", p.To)
	if err == nil && len(st.Lines) > 0 {
		p.Closing = st.Lines[len(st.Lines)-1].Balance.Dec().Neg()
	}
	hs, _, _ := o.holdLines(ctx, tenant, c.UCC, p.To)
	for _, h := range hs {
		p.Holdings = p.Holdings.Add(h.value)
	}
	if r, _, err := docs.Get[yearEndSent](ctx, o.Docs, tenant, cYearEnd, fy+"|"+c.UCC); err == nil {
		p.SentAt = r.At
	}
	return p, nil
}

type yearEndSent struct {
	At string `json:"at"`
	By string `json:"by"`
}

// YearEndRow is one client on the year-end screen.
type YearEndRow struct {
	UCC    string    `json:"ucc"`
	Name   string    `json:"name"`
	Email  bool      `json:"email"`
	Trades int       `json:"trades"`
	Value  money.Dec `json:"value"`
	STT    money.Dec `json:"stt"`
	SentAt string    `json:"sent_at,omitempty"`
}

// YearEndView lists the clients for a financial year.
type YearEndView struct {
	FY      string       `json:"fy"`
	From    string       `json:"from"`
	To      string       `json:"to"`
	Running bool         `json:"running"`
	Rows    []YearEndRow `json:"rows"`
	STT     money.Dec    `json:"stt"`
	Sent    int          `json:"sent"`
	Years   []string     `json:"years"`
}

// DefaultFY is the year a pack is wanted for on a date: the year just
// ended until September, the running year after.
func DefaultFY(date string) string {
	l, from, _ := fyOf(date)
	if date[5:7] >= "04" && date[5:7] <= "09" {
		l, _, _ = fyOf(addDays(from, -1))
	}
	return l
}

// YearEnds lists every active client with their STT for the year.
func (o *Ops) YearEnds(ctx context.Context, tenant, fy, today string, scope func(string) bool) (YearEndView, error) {
	if fy == "" {
		fy = DefaultFY(today)
	}
	from, to, err := fyRange(fy)
	if err != nil {
		return YearEndView{}, err
	}
	v := YearEndView{FY: fy, From: from, To: to, Rows: []YearEndRow{}}
	cur, _, _ := fyOf(today)
	prev, _, _ := fyOf(addDays(fmt.Sprintf("%s-04-01", cur[:4]), -1))
	v.Years = []string{cur, prev}
	if from > today {
		return v, invalid("financial year %s has not started", fy)
	}
	if today < to {
		v.To, v.Running = today, true
	}
	stt, err := o.sttForYear(ctx, tenant, from, v.To, "")
	if err != nil {
		return v, err
	}
	cs, err := o.ClientMap(ctx, tenant)
	if err != nil {
		return v, err
	}
	for _, u := range sortedKeys(cs) {
		c := cs[u]
		if c.Status != "Active" || (scope != nil && !scope(u)) {
			continue
		}
		r := YearEndRow{UCC: u, Name: c.Name, Email: reEmail.MatchString(c.Email) && !strings.HasSuffix(c.Email, ".invalid")}
		for _, l := range stt[u] {
			r.Trades += l.Trades
			r.Value = r.Value.Add(l.Value)
			r.STT = r.STT.Add(l.STT)
		}
		if s, _, err := docs.Get[yearEndSent](ctx, o.Docs, tenant, cYearEnd, fy+"|"+u); err == nil {
			r.SentAt = s.At
			v.Sent++
		}
		v.STT = v.STT.Add(r.STT)
		v.Rows = append(v.Rows, r)
	}
	return v, nil
}

// SendYearEnd emails clients that their pack is ready (all active clients
// with an email when uccs is empty). The pack itself is in the portal.
func (o *Ops) SendYearEnd(ctx context.Context, tenant, actor, fy, today string, uccs []string) (int, int, error) {
	if err := o.require(ctx, tenant, actor, "Sending year-end packs", RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return 0, 0, err
	}
	v, err := o.YearEnds(ctx, tenant, fy, today, nil)
	if err != nil {
		return 0, 0, err
	}
	if v.Running {
		return 0, 0, invalid("financial year %s is still running; packs go out after 31 March", fy)
	}
	want := map[string]bool{}
	for _, u := range uccs {
		want[strings.ToUpper(u)] = true
	}
	sent, failed := 0, 0
	for _, r := range v.Rows {
		if len(want) > 0 && !want[r.UCC] {
			continue
		}
		ms, err := o.sendEvent(ctx, tenant, actor, "yearend.pack", r.UCC, map[string]string{"fy": fy, "stt": rupees(r.STT)}, "")
		ok := err == nil && len(ms) > 0
		for _, m := range ms {
			ok = ok && m.Status == "Sent"
		}
		if !ok {
			failed++
			continue
		}
		sent++
		_ = docs.Upsert(ctx, o.Docs, tenant, cYearEnd, fy+"|"+r.UCC, yearEndSent{At: o.now().Format(time.RFC3339), By: actor}, actor)
	}
	o.log(ctx, tenant, actor, "yearend.sent", fy, map[string]any{"sent": sent, "failed": failed})
	return sent, failed, nil
}

// RenderYearEnd is the pack as a printable page.
func RenderYearEnd(p YearEndPack) ([]byte, error) {
	var b bytes.Buffer
	err := yearEndTmpl.Execute(&b, p)
	return b.Bytes(), err
}

var yearEndTmpl = template.Must(template.New("ye").Funcs(template.FuncMap{
	"inr": func(d money.Dec) string { return formatINR(d) },
	"day": fmtDay,
	"neg": func(d money.Dec) bool { return d.Sign() < 0 },
	"abs": func(d money.Dec) money.Dec { return d.Abs() },
	"ex": func(ls []STTRow, ex string) []STTRow {
		var o []STTRow
		for _, l := range ls {
			if l.Exchange == ex {
				o = append(o, l)
			}
		}
		return o
	},
	"tot": func(ls []STTRow, ex string) [2]money.Dec {
		var t [2]money.Dec
		for _, l := range ls {
			if l.Exchange == ex {
				t[0], t[1] = t[0].Add(l.Value), t[1].Add(l.STT)
			}
		}
		return t
	},
	"code": func() []string { return []string{"01", "02", "03", "04", "05"} },
}).Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><title>Year-end statement {{.FY}} · {{.UCC}}</title>
<style>
body{font:12.5px/1.45 -apple-system,system-ui,"Segoe UI",sans-serif;color:#16201B;margin:32px;max-width:960px}
h1{font-size:18px;margin:0}h2{font-size:14px;margin:26px 0 6px}h3{font-size:12.5px;margin:14px 0 4px}
.meta{display:flex;justify-content:space-between;gap:24px;border-bottom:2px solid #16201B;padding-bottom:12px;flex-wrap:wrap}
.meta div{display:flex;flex-direction:column;gap:2px}
table{width:100%;border-collapse:collapse;margin-top:6px}th,td{padding:5px 6px;border-bottom:1px solid #C9CDC3;text-align:left;vertical-align:top}
th{font-size:11px;text-transform:uppercase;letter-spacing:.04em;background:#EAECE6}td.r,th.r{text-align:right;white-space:nowrap}
tfoot td{font-weight:700;border-top:2px solid #16201B}.small{font-size:11px;color:#525D57}
.box{border:1px solid #16201B;padding:12px 14px;margin-top:10px;page-break-inside:avoid}.warn{background:#FAEFD6;padding:8px 10px;border-radius:6px}
@media print{body{margin:12mm}}
</style></head><body>
<div class="meta"><div><h1>Year-end statement · FY {{.FY}}</h1><span>{{.Name}} · client code {{.UCC}} · PAN {{.PAN}}</span><span>{{day .From}} to {{day .To}}</span></div>
<div style="text-align:right"><b>{{.Firm}}</b><span>SEBI registration {{.SEBIReg}}</span>{{range .Members}}<span>{{.}}</span>{{end}}</div></div>
{{if .Running}}<p class="warn">The financial year is still running: figures are to {{day .To}}. The final statement is issued after 31 March.</p>{{end}}

<h2>1. Summary</h2>
<table><tbody>
<tr><td>Short-term capital gains on listed shares (section 111A)</td><td class="r">{{inr .STCG}}</td></tr>
<tr><td>Long-term capital gains on listed shares (section 112A)</td><td class="r">{{inr .LTCG}}</td></tr>
<tr><td>Intraday (speculative) result</td><td class="r">{{inr .Spec}}</td></tr>
<tr><td>Futures and options result (mark-to-market and premium)</td><td class="r">{{inr .Deriv}}</td></tr>
<tr><td>Securities transaction tax paid</td><td class="r">{{inr .STTTotal}}</td></tr>
<tr><td>Ledger balance on {{day .To}} ({{if neg .Closing}}owed by you{{else}}in your favour{{end}})</td><td class="r">{{inr (abs .Closing)}}</td></tr>
<tr><td>Value of holdings with us on {{day .To}}</td><td class="r">{{inr .Holdings}}</td></tr>
</tbody></table>
<p class="small">Capital gains are worked first-in first-out over your purchase lots and do not include charges; the stock-wise statement is in the client portal under Reports &amp; statements. Results through other brokers are not included.</p>

<h2>2. Charges paid</h2>
<table><thead><tr><th>Charge</th><th class="r">Amount (₹)</th></tr></thead><tbody>
{{range .Charges}}<tr><td>{{.Head}}</td><td class="r">{{inr .Amount}}</td></tr>{{else}}<tr><td colspan="2">No charges in the year.</td></tr>{{end}}
</tbody></table>

<h2>3. Securities transaction tax: Form No. 10DB</h2>
<p class="small">Particulars for Form No. 10DB (see rule 20AB of the Income-tax Rules, 1962), one for each stock exchange. Columns 5 and 6 are for transactions entered into in the course of your business, which you fill in.</p>
{{$p := .}}{{range .Exchanges}}{{$ex := .}}<div class="box">
<h3>FORM No. 10DB · Financial year {{$p.FY}} · {{$ex}}</h3>
<table><tbody>
<tr><td>1. Name of the assessee</td><td>{{$p.Name}}</td></tr><tr><td>2. Address of the assessee</td><td>{{$p.City}}</td></tr>
<tr><td>3. PAN of the assessee</td><td>{{$p.PAN}}</td></tr><tr><td>4. MAPIN of the assessee</td><td>Not applicable</td></tr>
<tr><td>5. Name of the stock exchange</td><td>{{$ex}}</td></tr><tr><td>6. Financial year</td><td>{{$p.FY}}</td></tr>
<tr><td>7–9. Stock broker, address, code</td><td>{{$p.Firm}} · SEBI {{$p.SEBIReg}}</td></tr></tbody></table>
<table><thead><tr><th>(1) Client code</th><th>(2) Code of transaction</th><th class="r">(3) Value of transactions (₹)</th><th class="r">(4) STT collected (₹)</th><th class="r">(5) Of (3), in the course of business</th><th class="r">(6) STT on (5)</th></tr></thead><tbody>
{{range ex $p.STT $ex}}<tr><td>{{$p.UCC}}</td><td>{{.Code}} · <span class="small">{{.Desc}}</span></td><td class="r">{{inr .Value}}</td><td class="r">{{inr .STT}}</td><td class="r"></td><td class="r"></td></tr>{{end}}
</tbody>{{$t := tot $p.STT $ex}}<tfoot><tr><td colspan="2">Total</td><td class="r">{{inr (index $t 0)}}</td><td class="r">{{inr (index $t 1)}}</td><td></td><td></td></tr></tfoot></table>
<p class="small">Verification (by the assessee): I, ________, son/daughter of ________, solemnly declare that to the best of my knowledge and belief the information given in this Form is correct and complete and that the amount of securities transaction tax shown is truly stated. Place ______ Date ______ Signature ______</p>
</div>{{else}}<p>No transactions liable to securities transaction tax in the year.</p>{{end}}
<p class="small" style="margin-top:20px">Generated {{.Generated}} from the records of {{.Firm}}. This statement is information from your account to help you prepare your return; it is not tax advice. Please report any discrepancy to us within 30 days.</p>
</body></html>`))
