package ops

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
	"ledgerline/internal/xfile"
)

// ---- self-clearing at BSE Clearing (ICCL / BSECL) ----
//
// A firm that clears its own trades at ICCL (Settings → Cleared through:
// ICCL for BSE-CM, BSE-FO, BSE-CD, or another exchange's segment under
// interoperability) has ICCL's daily collateral and margin round to run,
// the mirror of NSE Clearing's (nclalloc.go, segmon.go, ccdata.go):
//
//   - BSE2-N0001, ICCL notice 20250502-5 table item 1: the client-wise
//     margin reporting file, the actual margin collected at EOD and the
//     peak margin collected (not the margin required); not reporting is
//     treated as 100% short (iccl-margin-reporting);
//   - BSE2-N0002, the same notice item 2 and the inspection master
//     circular para 6.1-6.2: the daily client-level collateral segregation
//     report, collateral held at BSECL only (never added to NSE
//     Clearing's), with the nil-balance clients who traded in the last 12
//     months (iccl-collateral-segregation);
//   - BSE2-N0003, ICCL notice 20250603-44: ICCL's SHORTALLOCATION_CM file
//     is loaded, each short client gets reasons 01-08 with the collateral
//     available, and the upload carries ICCL's rows unaltered;
//   - BSE2-N0004, ICCL cash master circular para 5.20-5.26: client-wise
//     allocation of cash collateral by the ICCLCOLL file (no ICCL API
//     specification is in Ledgerline), never more than the client gave and
//     not below the margin reported as collected;
//   - BSE2-N0005, ICCL notice 20250221-5: ICCL's ALLOCATIONDIFF exception
//     report is loaded and each mismatch is a work item;
//   - BSE2-N0009, ICCL circular 20200829-4: the EPN (securities) and EPF
//     (funds) early pay-in files with the firm's own clearing number as
//     the TM code;
//   - BSE2-N0007, N0008 (pay-in from the CM settlement and pool accounts
//     with BSECL's cut-offs, internal shortages funded by noon) in
//     icclsettle.go; BSE2-N0011 (pre-delivery margin) and NCL-BSE-0132
//     (trading and position limits) in icclrisk.go.
//
// ICCL has not published the column layouts of these files. Each is built
// from the notice's description into config/formats/iccl-*.json, marked
// "confirm": true with the notice it follows, and listed on the screen as
// a question for the firm: confirm it against ICCL's own file description
// before the first upload. Nothing here runs, shows or nags for a firm that
// does not clear at ICCL: a firm at NSE Clearing sees no change.

const (
	cICCLData     = "iccldata"     // kind|date|segment: ICCL's rows as loaded, with the raw lines
	cICCLReason   = "icclsareason" // date|snapshot|segment|ucc|code: reasons reported for a short allocation
	cICCLReported = "icclreported" // date: the EOD margin collected as last reported, per segment|ucc
	wICCL         = "iccl"         // work items
)

// icclSelf says whether the firm clears its own trades at ICCL. It reads
// the firm-modules condition selfat:ICCL (compduties.go dutyNeed); where
// that condition is not known to this build yet, it falls back to the same
// fact the condition is made from (Settings → Cleared through).
func (o *Ops) icclSelf(ctx context.Context, tenant string) (bool, string) {
	return o.selfAtCC(ctx, tenant, "ICCL")
}

// selfAtCC is icclSelf for any clearing corporation.
func (o *Ops) selfAtCC(ctx context.Context, tenant, cc string) (bool, string) {
	f := o.firmFacts(ctx, tenant)
	v, why := dutyNeed("selfat:"+cc, f)
	if strings.HasPrefix(why, "unknown condition") {
		if f.SelfAt[cc] {
			return true, "you clear your own trades at " + firstOf(ClearingCorpNames[cc], cc)
		}
		return false, "you do not clear your own trades at " + firstOf(ClearingCorpNames[cc], cc)
	}
	return v == triYes, why
}

// icclSegments are the segments (CM, FO, CD) the firm clears at ICCL, and
// notes for any of them whose trades on another exchange are cleared at
// another clearing corporation. Ledgerline allocates a client's collateral
// per segment, not per clearing corporation, so such a segment's ICCL
// files are not made: collateral reported to ICCL must be ICCL's only.
func icclSegments(set Settings) (segs []string, split []string) {
	at := map[string]map[string]bool{}
	for _, c := range set.activeSegments() {
		th := set.ClearedThrough(c.Key)
		if _, cc := ClearingCorpNames[th]; !cc {
			continue
		}
		if at[c.Segment] == nil {
			at[c.Segment] = map[string]bool{}
		}
		at[c.Segment][th] = true
	}
	for _, s := range []string{"CM", "FO", "CD"} {
		if !at[s]["ICCL"] {
			continue
		}
		if len(at[s]) > 1 {
			split = append(split, fmt.Sprintf("The %s segment is cleared at ICCL for some exchanges and at %s for others. Ledgerline keeps one collateral allocation per segment, so it does not make ICCL's %s files: they would report collateral held elsewhere. Clear the segment at one clearing corporation, or prepare ICCL's %s files by hand.", s, strings.Join(otherCCs(at[s]), ", "), s, s))
			continue
		}
		segs = append(segs, s)
	}
	return segs, split
}

func otherCCs(m map[string]bool) []string {
	var out []string
	for _, k := range sortedKeys(m) {
		if k != "ICCL" {
			out = append(out, ClearingCorpNames[k])
		}
	}
	return out
}

// icclCodes are the firm's codes for ICCL's files: its clearing member
// code at ICCL (Settings → member codes, ICCL; else its BSE code) and its
// BSE trading member code.
func icclCodes(set Settings) (cm, tm string, err error) {
	tm = strings.TrimSpace(set.MemberCodes["BSE"])
	cm = firstNonEmptyStr(set.MemberCodes["ICCL"], tm)
	if cm == "" {
		return "", "", invalid("set the firm's ICCL clearing member code (or BSE member code) in Settings first")
	}
	return cm, firstOf(tm, cm), nil
}

// ---- ICCL's files as loaded ----

// ICCLData is a day's rows of one of ICCL's files, with the raw header and
// lines so an upload can carry them unaltered.
type ICCLData struct {
	ID       string              `json:"id"`
	Kind     string              `json:"kind"` // shortalloc, allocdiff
	Date     string              `json:"date"`
	Segment  string              `json:"segment"`
	File     string              `json:"file"`
	LoadedAt string              `json:"loaded_at"`
	Header   string              `json:"header,omitempty"`
	Delim    string              `json:"delim,omitempty"`
	Rows     []map[string]string `json:"rows"` // by column key, with "_raw" the line as received
}

// icclSegOf reads a segment value as CM, FO or CD.
func icclSegOf(v, def string) string {
	switch u := strings.ToUpper(strings.TrimSpace(v)); u {
	case "C", "CM", "EQ", "EQUITY", "CASH":
		return "CM"
	case "F", "FO", "EQD", "DERIVATIVES", "F&O":
		return "FO"
	case "X", "CD", "CURRENCY", "CDS":
		return "CD"
	case "":
		return def
	default:
		return u
	}
}

// loadICCLFile keeps ICCL's SHORTALLOCATION_CM or ALLOCATIONDIFF file; ok
// is false for any other layout.
func (o *Ops) loadICCLFile(ctx context.Context, tenant, actor string, s *xfile.Spec, f xfile.File, rows []xfile.Row, r *XFileResult) bool {
	kind := map[string]string{"iccl-short-allocation": "shortalloc", "iccl-allocation-diff": "allocdiff"}[s.ID]
	if kind == "" {
		return false
	}
	lines := strings.Split(strings.ReplaceAll(string(bytes.TrimPrefix(f.Data, []byte("\xef\xbb\xbf"))), "\r\n", "\n"), "\n")
	date := ymdFromName(f.Name)
	if len(rows) > 0 {
		if d := anyDate(rows[0].Get("date")); d != "" {
			date = d
		}
	}
	if date == "" {
		r.Loaded = "Not loaded: the trade date is neither in the file nor in its name"
		return true
	}
	seg := icclSegOf("", "CM")
	if len(rows) > 0 {
		seg = icclSegOf(rows[0].Get("segment"), "CM")
	}
	d := ICCLData{ID: kind + "|" + date + "|" + seg, Kind: kind, Date: date, Segment: seg, File: f.Name, LoadedAt: o.now().In(ist).Format("2006-01-02 15:04")}
	if len(rows) > 0 && rows[0].Line >= 2 && rows[0].Line-2 < len(lines) {
		d.Header = strings.TrimRight(lines[rows[0].Line-2], "\r")
		d.Delim = sniffDelim(d.Header)
	}
	for _, x := range rows {
		m := map[string]string{}
		for k, v := range x.Values {
			if v = strings.TrimSpace(v); v != "" {
				m[k] = v
			}
		}
		m["ucc"] = strings.ToUpper(m["ucc"])
		if x.Line >= 1 && x.Line-1 < len(lines) {
			m["_raw"] = strings.TrimRight(lines[x.Line-1], "\r")
		}
		d.Rows = append(d.Rows, m)
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cICCLData, d.ID, d, actor); err != nil {
		r.Loaded = "Not loaded: " + err.Error()
		return true
	}
	r.Added = len(d.Rows)
	switch kind {
	case "shortalloc":
		r.Loaded = "Loaded as ICCL's short allocations of " + date + ": record each client's reasons on Exchange files → BSE Clearing (ICCL), then download the reporting file"
		o.raiseICCLShort(ctx, tenant, d)
	case "allocdiff":
		r.Loaded = "Loaded as ICCL's allocation mismatches of " + date + ": each is a work item to correct the allocation or the reporting"
		o.raiseICCLDiffs(ctx, tenant, d)
	}
	return true
}

func sniffDelim(h string) string {
	best, n := ",", 0
	for _, d := range []string{",", "|", "\t", ";", "~"} {
		if c := strings.Count(h, d); c > n {
			best, n = d, c
		}
	}
	return best
}

func (o *Ops) icclData(ctx context.Context, tenant, kind, date string) []ICCLData {
	ds, _ := docs.List[ICCLData](ctx, o.Docs, tenant, cICCLData, kind+"|"+date+"|")
	return ds
}

// raiseICCLShort raises one work item per day while short allocations
// have no reason recorded.
func (o *Ops) raiseICCLShort(ctx context.Context, tenant string, d ICCLData) {
	open := 0
	for _, r := range d.Rows {
		if decOr0(r["short"]).Sign() > 0 && len(o.icclReasonsFor(ctx, tenant, d.Date, r["snapshot"], d.Segment, r["ucc"])) == 0 {
			open++
		}
	}
	key := "short|" + d.Date + "|" + d.Segment
	if open == 0 {
		o.closeWork(ctx, tenant, SystemActor, wICCL, key, "reasons recorded")
		return
	}
	o.raiseWork(ctx, tenant, wICCL, key, fmt.Sprintf("Report reasons for %d ICCL short allocations of %s", open, fmtDay(d.Date)),
		fmt.Sprintf("ICCL's SHORTALLOCATION_CM file %s lists %d client snapshots short of margin with no reason recorded. Where the client's collateral was in fact available, record the reason (01-08) and the collateral available, then download the reporting file and upload it to ICCL. Do not change ICCL's own figures (ICCL notice 20250603-44).", d.File, open),
		"", RoleRisk, "xfiles", d.Date)
}

// raiseICCLDiffs raises a work item per client in ICCL's ALLOCATIONDIFF.
func (o *Ops) raiseICCLDiffs(ctx context.Context, tenant string, d ICCLData) {
	for _, r := range d.Rows {
		u := r["ucc"]
		if u == "" {
			continue
		}
		o.raiseWork(ctx, tenant, wICCL, "diff|"+d.Date+"|"+d.Segment+"|"+u, fmt.Sprintf("ICCL allocation mismatch: %s (%s, %s)", u, d.Segment, fmtDay(d.Date)),
			fmt.Sprintf("ICCL's ALLOCATIONDIFF report %s shows the collateral allocated to %s (₹%s) differs from the segregation reporting (₹%s). Find which is wrong: correct the client's allocation at ICCL (ICCLCOLL) or the next segregation report, and fix the step that caused it (ICCL notice 20250221-5).", d.File, u, formatINR(decOr0(r["allocated"])), formatINR(decOr0(r["reported"]))),
			u, RoleChecker, "xfiles", d.Date)
	}
}

// ---- short allocation reasons ----

// ICCLReason is one reason reported for a client's short allocation at an
// ICCL snapshot.
type ICCLReason struct {
	ID       string    `json:"id"`
	Date     string    `json:"date"`
	Snapshot string    `json:"snapshot"`
	Seg      string    `json:"segment"`
	UCC      string    `json:"ucc"`
	Code     string    `json:"code"`
	Amount   money.Dec `json:"amount"`
	By       string    `json:"by"`
}

// ICCLReasonCodes are the reason codes ICCL accepts (01-08). ICCL's list
// is not public: 01-07 are given as NSE Clearing's codes read, and 08 as
// ICCL's further code; each is to confirm against ICCL's list (notice
// 20250603-44), and the code, not this text, is what is uploaded.
var ICCLReasonCodes = map[string]string{"01": "Excess collateral with another clearing corporation (to confirm)", "02": "Early pay-in of securities sold on the day (to confirm)", "03": "Trades in a wrong client code (to confirm)",
	"04": "Trades for NRI clients (to confirm)", "05": "Allocation submitted, accepted later (to confirm)", "06": "Re-pledge not yet processed (to confirm)", "07": "Custodian early pay-in of funds (to confirm)", "08": "ICCL's eighth reason code (to confirm its meaning with ICCL)"}

func (o *Ops) icclReasonsFor(ctx context.Context, tenant, date, snap, seg, ucc string) []ICCLReason {
	rs, _ := docs.List[ICCLReason](ctx, o.Docs, tenant, cICCLReason, date+"|"+snap+"|"+seg+"|"+strings.ToUpper(ucc)+"|")
	return rs
}

// SaveICCLReason records (or with a zero amount removes) a reason for a
// client's short allocation at one of ICCL's snapshots.
func (o *Ops) SaveICCLReason(ctx context.Context, tenant, actor string, x ICCLReason) (ICCLReason, error) {
	if err := o.require(ctx, tenant, actor, "Reporting short allocation to ICCL", RoleMaker, RoleChecker, RoleRisk); err != nil {
		return x, err
	}
	if on, why := o.icclSelf(ctx, tenant); !on {
		return x, fmt.Errorf("%w: %s", ErrState, why)
	}
	x.UCC, x.Seg, x.Snapshot = strings.ToUpper(strings.TrimSpace(x.UCC)), icclSegOf(x.Seg, "CM"), strings.TrimSpace(x.Snapshot)
	if !validDate(x.Date) || x.UCC == "" {
		return x, invalid("give the date, segment and client")
	}
	if _, ok := ICCLReasonCodes[x.Code]; !ok {
		return x, invalid("ICCL's reason code is 01 to 08")
	}
	if x.Amount.Sign() < 0 {
		return x, invalid("the collateral available cannot be negative")
	}
	found := false
	for _, d := range o.icclData(ctx, tenant, "shortalloc", x.Date) {
		for _, r := range d.Rows {
			if d.Segment == x.Seg && r["ucc"] == x.UCC && r["snapshot"] == x.Snapshot {
				found = true
			}
		}
	}
	if !found {
		return x, invalid("ICCL's short allocation file of %s has no %s row for %s at snapshot %q", x.Date, x.Seg, x.UCC, x.Snapshot)
	}
	x.ID, x.By = x.Date+"|"+x.Snapshot+"|"+x.Seg+"|"+x.UCC+"|"+x.Code, actor
	if err := docs.Upsert(ctx, o.Docs, tenant, cICCLReason, x.ID, x, actor); err != nil {
		return x, err
	}
	o.log(ctx, tenant, actor, "iccl.short_reason", x.ID, map[string]any{"amount": x.Amount.Fixed(2)})
	for _, d := range o.icclData(ctx, tenant, "shortalloc", x.Date) {
		o.raiseICCLShort(ctx, tenant, d)
	}
	return x, nil
}

// ---- the margin collected, as reported ----

// ICCLMarginRow is one client's margin in one segment cleared at ICCL.
type ICCLMarginRow struct {
	Segment     string    `json:"segment"`
	UCC         string    `json:"ucc"`
	Name        string    `json:"name"`
	Required    money.Dec `json:"required"`     // Ledgerline's EOD margin, pre-delivery margin included
	PreDelivery money.Dec `json:"pre_delivery"` // the part that is ITM stock option pre-delivery margin (icclrisk.go)
	EOD         money.Dec `json:"eod_collected"`
	Peak        money.Dec `json:"peak_collected"`
	Short       money.Dec `json:"short"`
	Cash        money.Dec `json:"cash"`     // cash allocated at ICCL
	NonCash     money.Dec `json:"non_cash"` // securities re-pledged to ICCL, after haircut
	Own         bool      `json:"own,omitempty"`
}

// icclMargins are the date's margins of every client in the segments
// cleared at ICCL: EOD collected from the end-of-day allocation (as NSE
// Clearing's file is made, ccdata.go), and the peak collected from the
// day's margin report (rms.go Margins: the lower of the client's collection
// and its peak), shared across the client's ICCL segments by their margin.
func (o *Ops) icclMargins(ctx context.Context, tenant string, set Settings, date string) ([]ICCLMarginRow, error) {
	segs, _ := icclSegments(set)
	if len(segs) == 0 {
		return nil, nil
	}
	al, err := o.Allocation(ctx, tenant, date)
	if err != nil {
		return nil, err
	}
	rep, _ := o.Margins(ctx, tenant, date)
	peak := map[string]money.Dec{}
	for _, r := range rep.Rows {
		peak[r.UCC] = money.Min(r.Collected, r.Peak)
	}
	pd := map[string]money.Dec{}
	if rows, _ := o.preDeliveryRows(ctx, tenant, set, date); rows != nil {
		for _, p := range rows {
			pd[p.UCC] = pd[p.UCC].Add(p.Margin)
		}
	}
	cs, _ := o.ClientMap(ctx, tenant)
	lim := o.RiskLimitConfig(ctx, tenant)
	prop := map[string]bool{}
	for _, u := range lim.ProprietaryUCCs {
		prop[u] = true
	}
	var out []ICCLMarginRow
	tot := map[string]money.Dec{}
	for _, a := range al {
		if contains(segs, a.Segment) {
			tot[a.UCC] = tot[a.UCC].Add(a.Required)
		}
	}
	for _, a := range al {
		if !contains(segs, a.Segment) {
			continue
		}
		r := ICCLMarginRow{Segment: a.Segment, UCC: a.UCC, Name: cs[a.UCC].Name, Required: a.Required, Cash: a.Cash, NonCash: a.NonCash, Own: prop[a.UCC]}
		if a.Segment == "FO" && pd[a.UCC].Sign() > 0 {
			r.PreDelivery = pd[a.UCC]
			r.Required = r.Required.Add(r.PreDelivery)
		}
		r.EOD = a.Cash.Add(a.NonCash)
		share := money.DecInt(1)
		if t := tot[a.UCC]; t.Sign() > 0 {
			share, _ = a.Required.Quo(t)
		}
		r.Peak = money.Max(money.Dec{}, peak[a.UCC].Mul(share)).Round(money.UnitPaisa).Dec()
		if d := r.Required.Sub(r.EOD); d.Sign() > 0 {
			r.Short = d
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Segment != out[j].Segment {
			return segRank(out[i].Segment) < segRank(out[j].Segment)
		}
		return out[i].UCC < out[j].UCC
	})
	return out, nil
}

// icclReported is what the last margin reporting file of a date said was
// collected, per segment|ucc.
type icclReported struct {
	ID   string               `json:"id"`
	EOD  map[string]money.Dec `json:"eod"`
	File string               `json:"file"`
}

// ---- segregation: ICCL-only collateral, nil-balance clients included ----

// icclSegregation is the client-level collateral segregation report rows:
// every client with collateral at ICCL, and every client with none who
// traded in the last 12 months (by the client's last trade date and the
// date's trades), in each ICCL segment the client trades or, with no
// positions, in the cash segment when it is ICCL's.
func (o *Ops) icclSegregation(ctx context.Context, tenant string, set Settings, date string) ([]ICCLMarginRow, int, error) {
	rows, err := o.icclMargins(ctx, tenant, set, date)
	if err != nil {
		return nil, 0, err
	}
	segs, _ := icclSegments(set)
	if len(segs) == 0 {
		return nil, 0, nil
	}
	have := map[string]bool{}
	for _, r := range rows {
		have[r.UCC] = true
	}
	traded := map[string]bool{}
	ts, _ := o.exchangeTrades(ctx, tenant, date)
	for _, t := range ts {
		if set.ClearedThrough(t.Exchange+"-"+t.Segment) == "ICCL" {
			traded[strings.ToUpper(t.UCC)] = true
		}
	}
	cs, _ := o.tradingClientMap(ctx, tenant)
	since := addDays(date, -365)
	nilSeg := segs[0]
	nils := 0
	for _, u := range sortedKeys(cs) {
		c := cs[u]
		if have[u] || !(traded[u] || (c.LastTrade != "" && c.LastTrade[:min(10, len(c.LastTrade))] >= since)) {
			continue
		}
		rows = append(rows, ICCLMarginRow{Segment: nilSeg, UCC: u, Name: c.Name})
		nils++
	}
	return rows, nils, nil
}

// ---- the allocation to ICCL (ICCLCOLL) ----

// ICCLAllocRow is one client's cash allocation at ICCL and its checks.
type ICCLAllocRow struct {
	Segment  string    `json:"segment"`
	UCC      string    `json:"ucc"`
	Name     string    `json:"name"`
	Cash     money.Dec `json:"cash"`
	Received money.Dec `json:"received"` // cash the client gave (its credit balance allocated at clearing corporations)
	Reported money.Dec `json:"reported"` // EOD margin reported to ICCL as collected (less non-cash), the floor
	Own      bool      `json:"own,omitempty"`
	Problem  string    `json:"problem,omitempty"`
}

// icclAllocation is the day's ICCLCOLL allocation: each client's cash in
// each ICCL segment from the end-of-day allocation, checked against the
// caps of the cash master circular para 5.20-5.26: never more than the
// cash the client gave (its credit summed over every segment and clearing
// corporation), and not below the cash part of the margin reported to
// ICCL as collected (the last margin reporting file of the date, else what
// it would report).
func (o *Ops) icclAllocation(ctx context.Context, tenant string, set Settings, date string) ([]ICCLAllocRow, error) {
	ms, err := o.icclMargins(ctx, tenant, set, date)
	if err != nil {
		return nil, err
	}
	al, _ := o.Allocation(ctx, tenant, date)
	received := map[string]money.Dec{}
	for _, a := range al {
		received[a.UCC] = received[a.UCC].Add(a.Cash)
	}
	rep, _, _ := docs.Get[icclReported](ctx, o.Docs, tenant, cICCLReported, date)
	used := map[string]money.Dec{}
	var out []ICCLAllocRow
	for _, m := range ms {
		r := ICCLAllocRow{Segment: m.Segment, UCC: m.UCC, Name: m.Name, Cash: m.Cash, Received: received[m.UCC], Own: m.Own}
		reported := m.EOD
		if v, ok := rep.EOD[m.Segment+"|"+m.UCC]; ok {
			reported = v
		}
		r.Reported = money.Max(money.Dec{}, reported.Sub(m.NonCash))
		used[m.UCC] = used[m.UCC].Add(r.Cash)
		switch {
		case !r.Own && used[m.UCC].Cmp(r.Received) > 0:
			r.Problem = fmt.Sprintf("allocated ₹%s, more than the ₹%s cash the client gave", formatINR(used[m.UCC]), formatINR(r.Received))
		case r.Cash.Cmp(r.Reported) < 0:
			r.Problem = fmt.Sprintf("allocated ₹%s, below the ₹%s reported to ICCL as collected in cash", formatINR(r.Cash), formatINR(r.Reported))
		}
		out = append(out, r)
	}
	return out, nil
}

// ---- writing ICCL's files ----

// icclWriters are ICCL's outgoing layouts Ledgerline writes.
var icclWriters = []string{"iccl-margin-reporting", "iccl-collateral-segregation", "iccl-short-allocation-reporting", "iccl-collateral-allocation", "iccl-early-payin-securities", "iccl-early-payin-funds"}

func init() {
	for _, id := range icclWriters {
		xWriters[id] = true
	}
}

// icclFile writes one of ICCL's files for a date (ExchangeFile, xfiles.go).
func (o *Ops) icclFile(ctx context.Context, tenant, actor string, s *xfile.Spec, set Settings, date string, vars xfile.Vars) (string, []byte, error) {
	if on, why := o.icclSelf(ctx, tenant); !on {
		return "", nil, fmt.Errorf("%w: ICCL's files are for a firm that clears its own trades at ICCL; %s (Settings → Cleared through)", ErrState, why)
	}
	cm, tm, err := icclCodes(set)
	if err != nil {
		return "", nil, err
	}
	vars["member"] = cm
	dd := date // written in the layout's DD-MM-YYYY
	var rows []xfile.Row
	switch s.ID {
	case "iccl-margin-reporting":
		ms, err := o.icclMargins(ctx, tenant, set, date)
		if err != nil {
			return "", nil, err
		}
		rep := icclReported{ID: date, EOD: map[string]money.Dec{}}
		for _, m := range ms {
			rows = append(rows, xfile.Row{Values: map[string]string{"Trade Date": dd, "Segment": m.Segment, "CM Code": cm, "TM Code": tm, "CP Code": "", "Client Code": m.UCC,
				"EOD Margin Collected": m.EOD.Fixed(2), "Peak Margin Collected": m.Peak.Fixed(2)}})
			rep.EOD[m.Segment+"|"+m.UCC] = m.EOD
		}
		if len(rows) == 0 {
			return "", nil, fmt.Errorf("%w: no client has margin or collateral at ICCL on %s", ErrState, date)
		}
		name, b, err := writeSpec(s, rows, vars)
		if err == nil {
			rep.File = name
			_ = docs.Upsert(ctx, o.Docs, tenant, cICCLReported, date, rep, actor)
		}
		return name, b, err
	case "iccl-collateral-segregation":
		ms, _, err := o.icclSegregation(ctx, tenant, set, date)
		if err != nil {
			return "", nil, err
		}
		for _, m := range ms {
			tot := m.Cash.Add(m.NonCash)
			rows = append(rows, xfile.Row{Values: map[string]string{"Trade Date": dd, "Segment": m.Segment, "TM Code": tm, "Client Code": m.UCC,
				"Cash Allocated": m.Cash.Fixed(2), "Cash Equivalent Value": "0.00", "Non Cash Value": m.NonCash.Fixed(2), "Total Value": tot.Fixed(2), "Margin": m.Required.Fixed(2)}})
		}
		if len(rows) == 0 {
			return "", nil, fmt.Errorf("%w: no client to report to ICCL on %s", ErrState, date)
		}
		return writeSpec(s, rows, vars)
	case "iccl-collateral-allocation":
		as, err := o.icclAllocation(ctx, tenant, set, date)
		if err != nil {
			return "", nil, err
		}
		var bad []string
		for _, a := range as {
			if a.Problem != "" {
				bad = append(bad, a.UCC+" "+a.Segment+": "+a.Problem)
				continue
			}
			acc := "C"
			if a.Own {
				acc = "P"
			}
			rows = append(rows, xfile.Row{Values: map[string]string{"Trade Date": dd, "Segment": a.Segment, "CM Code": cm, "TM Code": tm, "CP Code": "", "Client Code": a.UCC, "Account Type": acc, "Amount": a.Cash.Fixed(2), "Action": "U"}})
		}
		if len(bad) > 0 {
			return "", nil, invalid("the ICCLCOLL file is not made: %d allocation(s) break the caps, first: %s", len(bad), bad[0])
		}
		if len(rows) == 0 {
			return "", nil, fmt.Errorf("%w: no cash to allocate at ICCL on %s", ErrState, date)
		}
		return writeSpec(s, rows, vars)
	case "iccl-short-allocation-reporting":
		return o.icclShortReport(ctx, tenant, s, date, vars)
	case "iccl-early-payin-securities", "iccl-early-payin-funds":
		return o.icclEPIFile(ctx, tenant, s, set, cm, date, vars)
	}
	return "", nil, invalid("Ledgerline does not write %s", s.ID)
}

func writeSpec(s *xfile.Spec, rows []xfile.Row, vars xfile.Vars) (string, []byte, error) {
	b, err := s.Write(rows)
	if err != nil {
		return "", nil, err
	}
	name, err := s.FileName(vars)
	if err != nil {
		return "", nil, invalid("%v", err)
	}
	return name, b, nil
}

// icclShortReport is ICCL's SHORTALLOCATION_CM file as received, header and
// lines unaltered, with two columns added to each line (the reason code and
// the collateral available) and one line per reason recorded.
func (o *Ops) icclShortReport(ctx context.Context, tenant string, s *xfile.Spec, date string, vars xfile.Vars) (string, []byte, error) {
	var b strings.Builder
	n := 0
	for _, d := range o.icclData(ctx, tenant, "shortalloc", date) {
		dl := firstOf(d.Delim, ",")
		if b.Len() == 0 && d.Header != "" {
			b.WriteString(d.Header + dl + "Reason Code" + dl + "Collateral Available\n")
		}
		for _, r := range d.Rows {
			for _, x := range o.icclReasonsFor(ctx, tenant, date, r["snapshot"], d.Segment, r["ucc"]) {
				if x.Amount.Sign() <= 0 {
					continue
				}
				b.WriteString(r["_raw"] + dl + x.Code + dl + x.Amount.Fixed(2) + "\n")
				n++
			}
		}
	}
	if n == 0 {
		return "", nil, fmt.Errorf("%w: no reasons recorded for ICCL's short allocations of %s", ErrState, date)
	}
	name, err := s.FileName(vars)
	if err != nil {
		return "", nil, invalid("%v", err)
	}
	return name, []byte(b.String()), nil
}

// icclEPIFile writes EPN (client sales paid in early through CDSL,
// cdslepi.go, on an exchange cleared at ICCL) or EPF (client purchases at
// ICCL that are funded, for a firm that pays in clients' funds early:
// condition epi:funds).
func (o *Ops) icclEPIFile(ctx context.Context, tenant string, s *xfile.Spec, set Settings, cm, date string, vars xfile.Vars) (string, []byte, error) {
	var rows []xfile.Row
	if s.ID == "iccl-early-payin-securities" {
		for _, e := range o.EPIAccepted(ctx, tenant, date) {
			if set.ClearedThrough(firstOf(e.Exchange, "BSE")+"-CM") != "ICCL" {
				continue
			}
			rows = append(rows, xfile.Row{Values: map[string]string{"Settlement No": e.SettID, "TM Code": cm, "Client Code": e.UCC, "ISIN": e.ISIN, "Quantity": fmt.Sprint(e.Qty)}})
		}
		if len(rows) == 0 {
			return "", nil, fmt.Errorf("%w: no early pay-in of securities accepted by CDSL on %s for trades cleared at ICCL", ErrState, date)
		}
		return writeSpec(s, rows, vars)
	}
	if v, why := dutyNeed("epi:funds", o.firmFacts(ctx, tenant)); v == triNo {
		return "", nil, fmt.Errorf("%w: %s", ErrState, why)
	}
	st, err := o.Obligations(ctx, tenant, date)
	if err != nil {
		return "", nil, err
	}
	// The settlement is BSE's in CDSL's settlement calendar (the same
	// settlement IDs the EPN carries); without one the file is not made.
	ids := o.EPISettlements(ctx, tenant, "BSE", date)
	if len(ids) == 0 {
		return "", nil, invalid("load CDSL's settlement calendar covering %s first: the EPF needs BSE's settlement number", date)
	}
	per := map[string]money.Dec{}
	for _, ob := range st.Obligations {
		if ob.Clearing != "ICCL" || ob.NetQty <= 0 || ob.Status != "Funded" {
			continue
		}
		per[ob.UCC] = per[ob.UCC].Add(ob.Funds.Neg())
	}
	for _, u := range sortedKeys(per) {
		rows = append(rows, xfile.Row{Values: map[string]string{"Settlement No": ids[0], "TM Code": cm, "Client Code": u, "Amount": per[u].Fixed(2)}})
	}
	if len(rows) == 0 {
		return "", nil, fmt.Errorf("%w: no funded client purchase cleared at ICCL on %s", ErrState, date)
	}
	return writeSpec(s, rows, vars)
}
