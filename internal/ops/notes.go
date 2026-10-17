package ops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html/template"
	"sort"
	"strings"

	"ledgerline/internal/app"
	"ledgerline/internal/billing"
	"ledgerline/internal/charges"
	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/money"
)

// ContractNote is the delivery record of one client's note for a date.
type ContractNote struct {
	No        string    `json:"no"`
	Date      string    `json:"date"`
	UCC       string    `json:"ucc"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	Trades    int       `json:"trades"`
	Net       money.Dec `json:"net"`
	Status    string    `json:"status"` // Generated, Sent, Bounced
	SentAt    string    `json:"sent_at,omitempty"`
	Attempts  int       `json:"attempts"`
	Generated string    `json:"generated_by"`
	Revision  int       `json:"revision,omitempty"` // 0 for the original note
	Replaces  string    `json:"replaces,omitempty"` // number of the note this one replaces
	Reason    string    `json:"reason,omitempty"`   // why it was revised
}

// OutboxItem is a message handed to the mail relay.
type OutboxItem struct {
	ID      string `json:"id"`
	To      string `json:"to"`
	Subject string `json:"subject"`
	Ref     string `json:"ref"`
	At      string `json:"at"`
	Result  string `json:"result"`
}

func fyLabel(date string) string {
	y := 0
	m := 0
	fmt.Sscanf(date, "%d-%d", &y, &m)
	if m < 4 {
		y--
	}
	return fmt.Sprintf("%d-%02d", y, (y+1)%100)
}

// Notes lists a date's contract notes.
func (o *Ops) Notes(ctx context.Context, tenant, date string) ([]ContractNote, error) {
	return docs.List[ContractNote](ctx, o.Docs, tenant, cNote, date+"|")
}

// noteData prices one client's trades for a note.
func (o *Ops) noteData(ctx context.Context, tenant, date, ucc string) ([]ingest.TradeEvent, billing.Output, error) {
	all, err := o.exchangeTrades(ctx, tenant, date)
	if err != nil {
		return nil, billing.Output{}, err
	}
	var mine []ingest.TradeEvent
	for _, t := range all {
		if t.UCC == ucc {
			mine = append(mine, t)
		}
	}
	sort.Slice(mine, func(i, j int) bool { return mine[i].TradeTime < mine[j].TradeTime })
	if len(mine) == 0 {
		return nil, billing.Output{}, fmt.Errorf("%w: %s has no trades on %s", ErrState, ucc, date)
	}
	m, _, _ := o.Market(ctx, tenant, date)
	out, err := o.App.BuildBills(ctx, billing.Input{Tenant: tenant, Date: date, Actor: "notes", Trades: mine, SettlementPrices: m.SettlementPrices(), Schedule: o.App.Schedule})
	return mine, out, err
}

// GenerateNotes creates a note for every client who traded on the date and
// does not have one yet. Bills must be posted first.
func (o *Ops) GenerateNotes(ctx context.Context, tenant, actor, date string) (int, error) {
	if err := o.require(ctx, tenant, actor, "Generating contract notes", RoleMaker, RoleChecker); err != nil {
		return 0, err
	}
	trades, err := o.exchangeTrades(ctx, tenant, date)
	if err != nil {
		return 0, err
	}
	clients, err := o.ClientMap(ctx, tenant)
	if err != nil {
		return 0, err
	}
	existing, err := o.Notes(ctx, tenant, date)
	if err != nil {
		return 0, err
	}
	have := map[string]bool{}
	for _, n := range existing {
		have[n.UCC] = true
	}
	uccs := map[string]int{}
	for _, t := range trades {
		uccs[t.UCC]++
	}
	n := 0
	for _, u := range sortedKeys(uccs) {
		if have[u] {
			continue
		}
		_, out, err := o.noteData(ctx, tenant, date, u)
		if err != nil {
			return n, fmt.Errorf("%s: %w", u, err)
		}
		net := money.Dec{}
		for _, g := range out.Groups {
			net = net.Add(g.Net.Dec())
		}
		seq, err := o.next(ctx, tenant, "cnote:"+fyLabel(date), actor)
		if err != nil {
			return n, err
		}
		c := clients[u]
		note := ContractNote{No: fmt.Sprintf("CN/%s/%06d", fyLabel(date), seq), Date: date, UCC: u, Name: c.Name, Email: c.Email, Trades: uccs[u], Net: net, Status: "Generated", Generated: actor}
		if _, err := docs.Put(ctx, o.Docs, tenant, cNote, date+"|"+u, note, 0, actor); err != nil {
			return n, err
		}
		n++
	}
	o.log(ctx, tenant, actor, "notes.generated", date, map[string]any{"count": n})
	return n, nil
}

// SendNotes emails generated or bounced notes. Without a configured mail
// relay the messages go to the outbox only; addresses on reserved test
// domains (.invalid) bounce, as a real relay would report.
func (o *Ops) SendNotes(ctx context.Context, tenant, actor, date, ucc string) (sent, bounced int, err error) {
	if err := o.require(ctx, tenant, actor, "Sending contract notes", RoleMaker, RoleChecker); err != nil {
		return 0, 0, err
	}
	ns, err := o.Notes(ctx, tenant, date)
	if err != nil {
		return 0, 0, err
	}
	clients, err := o.ClientMap(ctx, tenant)
	if err != nil {
		return 0, 0, err
	}
	at := o.now().Format("15:04")
	for _, n := range ns {
		// A named client is sent their note even if it went before (a resend);
		// a bulk send skips notes already delivered.
		if (ucc != "" && n.UCC != ucc) || (ucc == "" && n.Status == "Sent") {
			continue
		}
		email := clients[n.UCC].Email
		result := "Sent"
		if strings.HasSuffix(email, ".invalid") || !reEmail.MatchString(email) {
			result = "Bounced"
		}
		id, err := o.next(ctx, tenant, "outbox", actor)
		if err != nil {
			return sent, bounced, err
		}
		_ = docs.Upsert(ctx, o.Docs, tenant, cOutbox, fmt.Sprintf("%08d", id), OutboxItem{ID: fmt.Sprintf("%08d", id), To: email, Subject: noteSubject(n), Ref: n.No, At: at, Result: result}, actor)
		_, err = docs.Update(ctx, o.Docs, tenant, cNote, date+"|"+n.UCC, actor, func(x *ContractNote) error {
			x.Status, x.Email = result, email
			x.SentAt = ""
			x.Attempts++
			if result == "Sent" {
				x.SentAt = at
			}
			return nil
		})
		if err != nil {
			return sent, bounced, err
		}
		if result == "Sent" {
			sent++
		} else {
			bounced++
		}
	}
	o.log(ctx, tenant, actor, "notes.sent", date, map[string]any{"sent": sent, "bounced": bounced})
	return sent, bounced, nil
}

var noteTmpl = template.Must(template.New("note").Funcs(template.FuncMap{
	"inr": func(d money.Dec) string { return formatINR(d) },
	"amt": func(a money.Amount) string { return formatINR(a.Dec()) },
}).Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><title>{{.Note.No}}</title>
<style>
body{font:12.5px/1.45 -apple-system,system-ui,"Segoe UI",sans-serif;color:#16201B;margin:32px;max-width:1000px}
h1{font-size:18px;margin:0}h2{font-size:14px;margin:22px 0 6px}
.meta{display:flex;justify-content:space-between;gap:24px;border-bottom:2px solid #16201B;padding-bottom:12px}
.meta div{display:flex;flex-direction:column;gap:2px}.r{text-align:right}
table{width:100%;border-collapse:collapse;margin-top:6px}th,td{padding:5px 6px;border-bottom:1px solid #C9CDC3;text-align:left;white-space:nowrap}
th{font-size:11px;text-transform:uppercase;letter-spacing:.04em;background:#EAECE6}td.r,th.r{text-align:right}
td.n{font-family:ui-monospace,Menlo,monospace}tfoot td{font-weight:700;border-top:2px solid #16201B}
.note{margin-top:20px;font-size:11px;color:#525D57}.sample{background:#FAEFD6;padding:8px 10px;border-radius:6px;margin-bottom:14px}
@media print{body{margin:12mm}.noprint{display:none}}
</style></head><body>
{{if .Note.Revision}}<div class="sample" style="background:#F6DEDB"><b>REVISED CONTRACT NOTE</b> (revision {{.Note.Revision}}). It replaces contract note {{.Note.Replaces}} for this trade date, which stands cancelled. Reason: {{.Note.Reason}}</div>{{end}}
{{if .Sample}}<div class="sample">Charges use the sample rate schedule {{.Schedule}}. Not valid for real clients.</div>{{end}}
<div class="meta"><div><h1>{{.Firm}}</h1><span>SEBI registration {{.SEBIReg}}</span><span>{{.Members}}</span><span>Compliance officer: {{.Compliance}}</span></div>
<div class="r"><b>CONTRACT NOTE {{.Note.No}}</b><span>Trade date {{.Note.Date}}</span><span>Settlement {{.PayIn}}</span></div></div>
<div class="meta" style="border-bottom:1px solid #C9CDC3"><div><b>{{.Client.Name}}</b><span>UCC {{.Client.UCC}} · PAN {{.PAN}}</span><span>{{.Client.City}}</span></div>
<div class="r"><span>{{.Client.Email}}</span><span>{{.Client.Demat.Depository}} {{.Client.Demat.ID}}</span></div></div>
<h2>Trades</h2>
<table><thead><tr><th>Order no.</th><th>Trade no.</th><th>Time</th><th>Exchange</th><th>Security / contract</th><th>B/S</th><th class="r">Qty</th><th class="r">Price</th><th class="r">Brokerage</th><th class="r">Net total</th></tr></thead><tbody>
{{range .Lines}}<tr><td class="n">{{.OrderNo}}</td><td class="n">{{.TradeNo}}</td><td class="n">{{.Time}}</td><td>{{.Seg}}</td><td>{{.Name}}</td><td>{{.Side}}</td><td class="r n">{{.Qty}}</td><td class="r n">{{.Price}}</td><td class="r n">{{amt .Brok}}</td><td class="r n">{{inr .Net}}</td></tr>{{end}}
</tbody></table>
<h2>Obligation and charges by segment</h2>
<table><thead><tr><th>Segment</th><th class="r">Pay-in / pay-out (incl. MTM)</th><th class="r">Brokerage</th><th class="r">Exchange charges</th><th class="r">STT / CTT</th><th class="r">SEBI fee</th><th class="r">Stamp duty</th><th class="r">GST</th><th class="r">Net</th></tr></thead><tbody>
{{range .Groups}}<tr><td>{{.Exchange}}-{{.Segment}}</td><td class="r n">{{amt .TradeNet}}</td><td class="r n">{{amt .Charges.Brokerage}}</td><td class="r n">{{amt .Charges.ExchTxn}}</td><td class="r n">{{amt .Charges.STT}}</td><td class="r n">{{amt .Charges.SEBIFee}}</td><td class="r n">{{amt .Charges.Stamp}}</td><td class="r n">{{amt .Charges.GST}}</td><td class="r n">{{amt .Net}}</td></tr>{{end}}
</tbody><tfoot><tr><td colspan="8">Net amount {{if .Payable}}payable by{{else}}receivable by{{end}} client</td><td class="r n">{{inr .NetAbs}}</td></tr></tfoot></table>
<p class="note">Positive amounts are payable to the client; negative amounts are payable by the client. Futures are marked to market at the day's settlement price. This contract note is issued subject to the rules, bye-laws and regulations of the exchanges and SEBI. Status: {{.Note.Status}}{{if .Note.SentAt}} at {{.Note.SentAt}}{{end}}. Digital signature: applied by the signing service on dispatch.</p>
</body></html>`))

type noteLine struct {
	OrderNo, TradeNo, Time, Seg, Name, Side, Qty, Price string
	Brok                                                money.Amount
	Net                                                 money.Dec
}

// RenderNote returns a printable HTML contract note.
func (o *Ops) RenderNote(ctx context.Context, tenant, date, ucc string) ([]byte, error) {
	n, _, err := docs.Get[ContractNote](ctx, o.Docs, tenant, cNote, date+"|"+strings.ToUpper(ucc))
	if err != nil {
		return nil, err
	}
	c, err := o.Client(ctx, tenant, n.UCC)
	if err != nil {
		return nil, err
	}
	set, _ := o.Settings(ctx, tenant)
	trades, out, err := o.noteData(ctx, tenant, date, n.UCC)
	if err != nil {
		return nil, err
	}
	var lines []noteLine
	for _, t := range trades {
		b, _ := charges.BucketFor(t.Segment, t.InstrumentType, t.Product)
		bd, err := o.App.Schedule.Compute(charges.Leg{Bucket: b, Exchange: t.Exchange, Side: t.Side, Qty: t.Qty, Price: t.Price, Date: t.TradeDate})
		if err != nil {
			return nil, err
		}
		if out.TradeBrokerage != nil {
			bd.Brokerage = out.TradeBrokerage[billing.TradeKey(t)]
		}
		v := t.Value()
		if t.Side == "B" {
			v = v.Neg()
		}
		side := "Buy"
		if t.Side == "S" {
			side = "Sell"
		}
		lines = append(lines, noteLine{t.OrderNo, t.TradeNo, t.TradeTime, t.Exchange + "-" + t.Segment, InstrumentName(t.Symbol, t.InstrumentType, t.Expiry, t.Strike), side, fmt.Sprint(t.Qty), t.Price.Fixed(2), bd.Brokerage, v})
	}
	net := money.Dec{}
	for _, g := range out.Groups {
		net = net.Add(g.Net.Dec())
	}
	var members []string
	for _, k := range sortedKeys(set.MemberCodes) {
		members = append(members, k+" "+set.MemberCodes[k])
	}
	var buf bytes.Buffer
	err = noteTmpl.Execute(&buf, map[string]any{
		"Note": n, "Client": c, "PAN": MaskPAN(c.PAN), "Firm": set.Firm, "SEBIReg": set.SEBIReg, "Members": "Member codes " + strings.Join(members, " · "),
		"Compliance": set.ComplianceEmail, "PayIn": set.NextBusinessDay(date), "Lines": lines, "Groups": out.Groups,
		"Payable": net.Sign() < 0, "NetAbs": net.Abs(), "Sample": o.App.Schedule.Sample, "Schedule": o.App.Schedule.Version,
	})
	return buf.Bytes(), err
}

// formatINR formats rupees with Indian digit grouping, e.g. 1,23,456.78.
func formatINR(d money.Dec) string {
	s := d.Fixed(2)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	whole, frac, _ := strings.Cut(s, ".")
	if len(whole) > 3 {
		head, tail := whole[:len(whole)-3], whole[len(whole)-3:]
		var parts []string
		for len(head) > 2 {
			parts = append([]string{head[len(head)-2:]}, parts...)
			head = head[:len(head)-2]
		}
		if head != "" {
			parts = append([]string{head}, parts...)
		}
		whole = strings.Join(parts, ",") + "," + tail
	}
	if neg && s != "0.00" {
		return "-" + whole + "." + frac
	}
	return whole + "." + frac
}

func noteSubject(n ContractNote) string {
	if n.Revision > 0 {
		return "Revised contract note " + n.No + " (replaces " + n.Replaces + ")"
	}
	return "Contract note " + n.No
}

// ReviseNote issues a revised contract note after a client's bill changed:
// a new number for the same trade date, marked as replacing the earlier
// note, ready to send. The earlier note's record stays in the history.
func (o *Ops) ReviseNote(ctx context.Context, tenant, actor, date, ucc, reason string) (ContractNote, bool, error) {
	cur, _, err := docs.Get[ContractNote](ctx, o.Docs, tenant, cNote, date+"|"+ucc)
	if errors.Is(err, docs.ErrNotFound) {
		return ContractNote{}, false, nil
	}
	if err != nil {
		return cur, false, err
	}
	_, out, err := o.noteData(ctx, tenant, date, ucc)
	if err != nil {
		return cur, false, err
	}
	net := money.Dec{}
	for _, g := range out.Groups {
		net = net.Add(g.Net.Dec())
	}
	seq, err := o.next(ctx, tenant, "cnote:"+fyLabel(date), actor)
	if err != nil {
		return cur, false, err
	}
	n, err := docs.Update(ctx, o.Docs, tenant, cNote, date+"|"+ucc, actor, func(x *ContractNote) error {
		x.Replaces, x.Revision, x.Reason = x.No, x.Revision+1, reason
		x.No = fmt.Sprintf("CN/%s/%06d", fyLabel(date), seq)
		x.Net, x.Status, x.SentAt, x.Attempts, x.Generated = net, "Generated", "", 0, actor
		return nil
	})
	if err != nil {
		return n, false, err
	}
	o.log(ctx, tenant, actor, "notes.revised", ucc, map[string]any{"date": date, "no": n.No, "replaces": n.Replaces, "reason": reason})
	return n, true, nil
}

// BillCorrection is the outcome of correcting one client's bill.
type BillCorrection struct {
	Billing app.BillingResult `json:"billing"`
	Note    *ContractNote     `json:"note,omitempty"`
	Sent    string            `json:"sent,omitempty"` // Sent or Bounced when sent straight away
	Plan    string            `json:"plan,omitempty"`
}

// CorrectBill rebills one client for one date after a correction (a
// brokerage plan change for the client, or corrected trades), reverses the
// old bill, posts the new one, revises the contract note and optionally
// sends it. Needs an Ops checker; the reason is kept on the reversal, the
// revised note and in the audit trail.
func (o *Ops) CorrectBill(ctx context.Context, tenant, actor, date, ucc, reason, planID, scope string, send bool) (BillCorrection, error) {
	var res BillCorrection
	if err := o.require(ctx, tenant, actor, "Correcting a bill", RoleChecker); err != nil {
		return res, err
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return res, invalid("give the reason for the correction; it is printed on the revised contract note")
	}
	ucc = strings.ToUpper(strings.TrimSpace(ucc))
	if _, err := o.Client(ctx, tenant, ucc); err != nil {
		return res, err
	}
	if planID != "" {
		// The plan in force for this client before the change, to restore
		// after a one-day change.
		prev := ""
		if as, err := o.Assignments(ctx, tenant); err == nil {
			for _, a := range as {
				if a.Target == "ucc:"+ucc && a.From <= date {
					prev = a.PlanID
				}
			}
		}
		if _, err := o.Assign(ctx, tenant, actor, "ucc:"+ucc, planID, date); err != nil {
			return res, err
		}
		if scope == "day" {
			set, _ := o.Settings(ctx, tenant)
			if _, err := o.Assign(ctx, tenant, actor, "ucc:"+ucc, prev, set.NextBusinessDay(date)); err != nil {
				return res, err
			}
		}
		res.Plan = planID
	}
	m, _, err := o.Market(ctx, tenant, date)
	if err != nil && !errors.Is(err, ErrNoMarketData) {
		return res, err
	}
	b, err := o.App.RunBillingFor(ctx, tenant, actor, date, m.SettlementPrices(), true, ucc, reason)
	res.Billing = b
	if err != nil {
		return res, err
	}
	if len(b.Posted) == 0 && len(b.Reversed) == 0 {
		return res, invalid("the bill for %s on %s did not change; correct the cause first (brokerage plan, trades or client code), then rebill", ucc, fmtDay(date))
	}
	n, revised, err := o.ReviseNote(ctx, tenant, actor, date, ucc, reason)
	if err != nil {
		return res, err
	}
	if revised {
		res.Note = &n
		if send {
			s, _, err := o.SendNotes(ctx, tenant, actor, date, ucc)
			if err != nil {
				return res, err
			}
			res.Sent = "Bounced"
			if s > 0 {
				res.Sent = "Sent"
			}
			if cur, _, err := docs.Get[ContractNote](ctx, o.Docs, tenant, cNote, date+"|"+ucc); err == nil {
				res.Note = &cur
			}
		}
	}
	o.log(ctx, tenant, actor, "bill.corrected", ucc, map[string]any{"date": date, "reason": reason, "plan": planID, "scope": scope, "posted": len(b.Posted), "reversed": len(b.Reversed)})
	return res, nil
}
