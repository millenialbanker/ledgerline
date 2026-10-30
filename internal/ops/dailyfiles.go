package ops

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
)

// ---- clearing corporations and the daily file checklist ----
//
// Under SEBI's interoperability framework each exchange segment can be
// cleared at any linked clearing corporation, and a firm that is only a
// trading member in a segment has its trades cleared by another clearing
// member. Settings → "Cleared through" records the choice per exchange
// segment (Settings.ClearingFor, keyed "BSE-CM" …): a clearing corporation
// from ClearingSegments, CM:<code> for another clearing member, or NONE for
// a segment the firm does not trade.
//
// The daily file checklist lists the files the firm expects to receive and
// send each trading day. Each file is tagged with where it comes from or
// goes to: the exchange itself (trade files, bhavcopy, stamp duty, scrip
// master), needed whenever the firm trades on that exchange; or a clearing
// corporation (allocation, margin reporting, VaR and haircuts …), needed
// only when some segment is cleared there. A segment cleared by another
// clearing member has no clearing-corporation files: they come from that
// member. The writers of outgoing files are not affected: they work for
// the segments that use them.

// ClearingCorpNames names the clearing corporations.
var ClearingCorpNames = map[string]string{
	"NCL":    "NSE Clearing",
	"ICCL":   "Indian Clearing Corporation (BSE)",
	"MCCIL":  "Metropolitan Clearing Corporation of India",
	"MCXCCL": "MCX Clearing Corporation",
	"NCCL":   "National Commodity Clearing (NCDEX)",
}

// ClearingSegment is one exchange segment and the clearing corporations it
// may be cleared at.
type ClearingSegment struct {
	Key      string   `json:"key"` // EXCH-SEG
	Exchange string   `json:"exchange"`
	Segment  string   `json:"segment"`
	Allowed  []string `json:"allowed"`
	Default  string   `json:"default"`
}

// ClearingSegments is the table of allowed clearing corporations. Every
// segment also allows another clearing member (CM:<code>) and NONE.
// Commodity interoperability is not live yet; add a corporation to Allowed
// when it is.
var ClearingSegments = []ClearingSegment{
	{"NSE-CM", "NSE", "CM", []string{"NCL", "ICCL"}, "NCL"},
	{"NSE-FO", "NSE", "FO", []string{"NCL", "ICCL"}, "NCL"},
	{"NSE-CD", "NSE", "CD", []string{"NCL", "ICCL"}, "NCL"},
	{"NSE-COM", "NSE", "COM", []string{"NCL"}, "NCL"},
	{"BSE-CM", "BSE", "CM", []string{"NCL", "ICCL"}, "NCL"},
	{"BSE-FO", "BSE", "FO", []string{"NCL", "ICCL"}, "NCL"},
	{"BSE-CD", "BSE", "CD", []string{"NCL", "ICCL"}, "NCL"},
	{"MSE-CM", "MSE", "CM", []string{"MCCIL", "NCL", "ICCL"}, "NCL"},
	{"MSE-FO", "MSE", "FO", []string{"MCCIL", "NCL", "ICCL"}, "NCL"},
	{"MSE-CD", "MSE", "CD", []string{"MCCIL", "NCL", "ICCL"}, "NCL"},
	{"MCX-COM", "MCX", "COM", []string{"MCXCCL"}, "MCXCCL"},
	{"NCDEX-COM", "NCDEX", "COM", []string{"NCCL"}, "NCCL"},
}

// ClearNone marks a segment the firm does not trade.
const ClearNone = "NONE"

func clearingSeg(key string) (ClearingSegment, bool) {
	for _, c := range ClearingSegments {
		if c.Key == key {
			return c, true
		}
	}
	return ClearingSegment{}, false
}

// ClearedThrough is the clearing corporation of an exchange segment (NCL,
// ICCL…), CM:<code> for another clearing member, or NONE.
func (s Settings) ClearedThrough(key string) string {
	if v := strings.TrimSpace(s.ClearingFor[key]); v != "" {
		return v
	}
	c, _ := clearingSeg(key)
	return c.Default
}

// tradesOn says whether the firm trades on an exchange: it has a member
// code there (with no member codes set at all, NSE, BSE and MCX).
func (s Settings) tradesOn(exch string) bool {
	set := false
	for _, c := range ClearingSegments {
		if strings.TrimSpace(s.MemberCodes[c.Exchange]) != "" {
			set = true
		}
	}
	if !set {
		return exch == "NSE" || exch == "BSE" || exch == "MCX"
	}
	return strings.TrimSpace(s.MemberCodes[exch]) != ""
}

// activeSegments are the exchange segments the firm trades.
func (s Settings) activeSegments() []ClearingSegment {
	var out []ClearingSegment
	for _, c := range ClearingSegments {
		if s.tradesOn(c.Exchange) && s.ClearedThrough(c.Key) != ClearNone {
			out = append(out, c)
		}
	}
	return out
}

// checkClearing validates and tidies the clearing choices.
func checkClearing(s *Settings) error {
	clean := map[string]string{}
	names := map[string]string{}
	for k, v := range s.ClearingFor {
		k, v = strings.ToUpper(strings.TrimSpace(k)), strings.ToUpper(strings.TrimSpace(v))
		c, ok := clearingSeg(k)
		if !ok {
			return invalid("%s is not an exchange segment Ledgerline knows", k)
		}
		if v == "" || v == c.Default {
			continue
		}
		switch {
		case v == ClearNone, contains(c.Allowed, v):
		case strings.HasPrefix(v, "CM:"):
			if strings.TrimSpace(v[3:]) == "" {
				return invalid("give the code of the clearing member that clears %s", k)
			}
			n := strings.TrimSpace(s.ClearingMemberNames[k])
			if n == "" {
				return invalid("give the name of the clearing member that clears %s", k)
			}
			v, names[k] = "CM:"+strings.TrimSpace(v[3:]), n
		default:
			return invalid("%s can be cleared through %s, or another clearing member; not %s", k, strings.Join(c.Allowed, " or "), v)
		}
		clean[k] = v
	}
	s.ClearingFor, s.ClearingMemberNames = clean, names
	return nil
}

// logClearing records every change of a segment's clearing.
func (o *Ops) logClearing(ctx context.Context, tenant, actor string, old, s Settings) {
	for _, c := range ClearingSegments {
		a, b := old.ClearedThrough(c.Key), s.ClearedThrough(c.Key)
		if a != b || old.ClearingMemberNames[c.Key] != s.ClearingMemberNames[c.Key] {
			o.log(ctx, tenant, actor, "settings.clearing", c.Key, map[string]any{"from": a, "to": b, "clearing_member": s.ClearingMemberNames[c.Key]})
		}
	}
}

// DailyFile is one file the firm expects on a trading day.
type DailyFile struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Format    string   `json:"format,omitempty"` // xfile layout; "" when Ledgerline does not know the layout yet
	Name      string   `json:"-"`                // narrows the layout by file name
	Party     string   `json:"party"`            // the exchange (NSE, BSE…) or the clearing corporation (NCL, ICCL…)
	Exchange  string   `json:"exchange,omitempty"`
	Segments  []string `json:"segments"`
	Direction string   `json:"direction"` // in or out
	At        string   `json:"at"`        // usual time, IST
	NextDay   bool     `json:"next_day,omitempty"`
	Intraday  bool     `json:"intraday,omitempty"` // several in the day; never alerted
}

var all3 = []string{"CM", "FO", "CD"}

// DailyFiles is the registry of expected files.
var DailyFiles = []DailyFile{
	// NSE (and NSE Clearing's trade file, which follows the exchange)
	{ID: "nse-trade-p", Title: "NSE provisional trade files (_P_)", Format: "ncl-trade", Name: `_P_\d+`, Party: "NSE", Exchange: "NSE", Segments: []string{"CM", "FO", "CD", "COM"}, Direction: "in", At: "09:30", Intraday: true},
	{ID: "nse-trade-f", Title: "NSE final trade file (_F_)", Format: "ncl-trade", Name: `_F_\d+`, Party: "NSE", Exchange: "NSE", Segments: []string{"CM", "FO", "CD", "COM"}, Direction: "in", At: "18:00"},
	{ID: "nse-bhav-cm", Title: "NSE bhavcopy, cash (common format)", Format: "udiff-bhavcopy", Name: `_NSE_CM_`, Party: "NSE", Exchange: "NSE", Segments: []string{"CM"}, Direction: "in", At: "18:00"},
	{ID: "nse-bhav-fo", Title: "NSE bhavcopy, F&O (common format)", Format: "udiff-bhavcopy", Name: `_NSE_FO_`, Party: "NSE", Exchange: "NSE", Segments: []string{"FO"}, Direction: "in", At: "18:30"},
	{ID: "nse-secmaster", Title: "NSE security master", Format: "nse-security-master", Party: "NSE", Exchange: "NSE", Segments: []string{"CM"}, Direction: "in", At: "08:00"},
	{ID: "nse-stamp", Title: "NSE stamp duty file", Format: "stamp-duty", Name: `^\d{8}\.txt$`, Party: "NSE", Exchange: "NSE", Segments: []string{"CM", "FO", "CD"}, Direction: "in", At: "09:00", NextDay: true},
	{ID: "nse-stt", Title: "STT / CTT file", Format: "stt-ctt", Party: "NSE", Exchange: "NSE", Segments: []string{"CM", "FO"}, Direction: "in", At: "09:00", NextDay: true},
	// BSE
	{ID: "bse-trade-p", Title: "BSE provisional trade files (_P_)", Format: "bse-trade", Name: `_P_\d+`, Party: "BSE", Exchange: "BSE", Segments: all3, Direction: "in", At: "09:30", Intraday: true},
	{ID: "bse-trade-f", Title: "BSE final trade file (_F_)", Format: "bse-trade", Name: `_F_\d+`, Party: "BSE", Exchange: "BSE", Segments: all3, Direction: "in", At: "17:00"},
	{ID: "bse-bhav-cm", Title: "BSE bhavcopy, cash (common format)", Format: "udiff-bhavcopy", Name: `_BSE_CM_`, Party: "BSE", Exchange: "BSE", Segments: []string{"CM"}, Direction: "in", At: "16:30"},
	{ID: "bse-bhav-fo", Title: "BSE bhavcopy, F&O (common format)", Format: "udiff-bhavcopy", Name: `_BSE_FO_`, Party: "BSE", Exchange: "BSE", Segments: []string{"FO"}, Direction: "in", At: "17:20"},
	{ID: "bse-stamp", Title: "BSE stamp duty file (MBRSTD)", Format: "bse-stamp-duty", Party: "BSE", Exchange: "BSE", Segments: []string{"CM"}, Direction: "in", At: "15:00", NextDay: true},
	{ID: "bse-scrip", Title: "BSE scrip and series mapping", Format: "bse-scrip-series", Party: "BSE", Exchange: "BSE", Segments: []string{"CM"}, Direction: "in", At: "08:00"},
	{ID: "bse-ucc-np-1", Title: "BSE UCCs not permitted to trade (first run)", Format: "bse-ucc-not-permitted", Name: `_01\.csv$`, Party: "BSE", Exchange: "BSE", Segments: []string{"CM", "FO"}, Direction: "in", At: "15:00"},
	{ID: "bse-ucc-np-2", Title: "BSE UCCs not permitted to trade (evening run)", Format: "bse-ucc-not-permitted", Name: `_(0[2-9]|[1-9]\d)\.csv$`, Party: "BSE", Exchange: "BSE", Segments: []string{"CM", "FO"}, Direction: "in", At: "22:00"},
	{ID: "bse-ucc-ts-1", Title: "BSE UCCs permitted to trade (first run)", Format: "bse-ucc-trading-status", Name: `_01\.csv$`, Party: "BSE", Exchange: "BSE", Segments: []string{"CM", "FO"}, Direction: "in", At: "15:00"},
	{ID: "bse-ucc-ts-2", Title: "BSE UCCs permitted to trade (evening run)", Format: "bse-ucc-trading-status", Name: `_(0[2-9]|[1-9]\d)\.csv$`, Party: "BSE", Exchange: "BSE", Segments: []string{"CM", "FO"}, Direction: "in", At: "22:00"},
	{ID: "bse-bulk", Title: "BSE bulk deals (BULK)", Format: "bse-bulk-deals", Party: "BSE", Exchange: "BSE", Segments: []string{"CM"}, Direction: "in", At: "16:50"},
	{ID: "bse-orders", Title: "BSE order log (BO)", Format: "bse-order-log", Party: "BSE", Exchange: "BSE", Segments: []string{"CM"}, Direction: "in", At: "17:40"},
	{ID: "bse-trc", Title: "BSE turnover and transaction charges (Turnover_TRC)", Format: "bse-turnover-trc", Party: "BSE", Exchange: "BSE", Segments: []string{"CM"}, Direction: "in", At: "19:15"},
	{ID: "bse-va", Title: "BSE clearing house account statement (VA)", Format: "bse-clearing-statement", Party: "BSE", Exchange: "BSE", Segments: []string{"CM", "FO"}, Direction: "in", At: "19:40"},
	{ID: "bse-ordchg", Title: "BSE order charges", Format: "bse-order-charges", Party: "BSE", Exchange: "BSE", Segments: []string{"CM"}, Direction: "in", At: "20:30"},
	{ID: "bse-debr", Title: "BSE statement of PANs debarred by SEBI (DEBR)", Format: "bse-debarred-pan", Party: "BSE", Exchange: "BSE", Segments: []string{"CM", "FO"}, Direction: "in", At: "21:00"},
	{ID: "bse-nome", Title: "BSE mobile and email exceptions (NOME)", Format: "bse-contact-exceptions", Party: "BSE", Exchange: "BSE", Segments: []string{"CM", "FO"}, Direction: "in", At: "21:20"},
	// MCX
	{ID: "mcx-trade-f", Title: "MCX trade file", Format: "mcx-trade", Party: "MCX", Exchange: "MCX", Segments: []string{"COM"}, Direction: "in", At: "23:59"},
	// NSE Clearing
	{ID: "ncl-var", Title: "NSE Clearing VaR file", Format: "ncl-var", Party: "NCL", Segments: []string{"CM"}, Direction: "in", At: "08:30"},
	{ID: "ncl-haircut", Title: "NSE Clearing haircuts (approved securities)", Format: "ncl-cb-haircut", Party: "NCL", Segments: []string{"CM", "FO", "CD"}, Direction: "in", At: "08:30"},
	{ID: "ncl-mg13", Title: "NSE Clearing client margin file (MG13)", Format: "ncl-span-client", Party: "NCL", Segments: all3, Direction: "in", At: "20:00"},
	{ID: "ncl-sa04", Title: "NSE Clearing short allocation (SA04)", Format: "ncl-short-allocation", Party: "NCL", Segments: all3, Direction: "in", At: "20:00"},
	{ID: "ncl-nmass", Title: "NSE Clearing N-MASS collateral", Format: "ncl-nmass-cm", Party: "NCL", Segments: []string{"CM"}, Direction: "in", At: "20:00"},
	{ID: "ncl-nmass-fo", Title: "NSE Clearing N-MASS collateral (F&O)", Format: "ncl-nmass-fo", Party: "NCL", Segments: []string{"FO", "CD"}, Direction: "in", At: "20:00"},
	{ID: "ncl-alloc", Title: "Client allocation to NSE Clearing", Format: "ncl-allocation", Party: "NCL", Segments: all3, Direction: "out", At: "19:00"},
	{ID: "ncl-cc01", Title: "Client collateral file (CC01)", Format: "ncl-client-collateral", Party: "NCL", Segments: all3, Direction: "out", At: "19:00"},
	{ID: "ncl-segmon", Title: "Client funds and collateral (SEGMON)", Format: "segmon-client-funds", Party: "NCL", Segments: all3, Direction: "out", At: "12:00", NextDay: true},
	{ID: "ncl-mrg", Title: "Margin reporting file to NSE Clearing", Format: "ncl-margin-reporting", Party: "NCL", Segments: all3, Direction: "out", At: "12:00", NextDay: true},
	{ID: "ncl-sa-rpt", Title: "Short allocation reporting to NSE Clearing", Format: "ncl-short-allocation-reporting", Party: "NCL", Segments: all3, Direction: "out", At: "12:00", NextDay: true},
	{ID: "ncl-sett", Title: "Combined settlement master (monthly)", Format: "ncl-settlement-master", Party: "NCL", Segments: all3, Direction: "in", At: "09:00", Intraday: true},
	// ICCL: layouts not yet in Ledgerline, checked by hand
	{ID: "iccl-var", Title: "ICCL VaR and ELM rates (VARELMAM)", Party: "ICCL", Segments: []string{"CM"}, Direction: "in", At: "08:30"},
	{ID: "iccl-haircut", Title: "ICCL haircuts (approved securities)", Party: "ICCL", Segments: all3, Direction: "in", At: "08:30"},
	{ID: "iccl-coll", Title: "ICCL collateral reports", Party: "ICCL", Segments: all3, Direction: "in", At: "20:00"},
	{ID: "iccl-margin", Title: "ICCL client margin file", Party: "ICCL", Segments: all3, Direction: "in", At: "20:00"},
	{ID: "iccl-alloc", Title: "Client allocation to ICCL", Party: "ICCL", Segments: all3, Direction: "out", At: "19:00"},
	{ID: "iccl-icoll", Title: "ICOLL response (ICCL collateral)", Party: "ICCL", Segments: all3, Direction: "in", At: "19:30"},
	{ID: "iccl-mrg", Title: "Margin reporting file to ICCL", Party: "ICCL", Segments: all3, Direction: "out", At: "12:00", NextDay: true},
	// MCXCCL
	{ID: "mcx-margin", Title: "MCXCCL client margin file", Format: "mcx-client-margin", Party: "MCXCCL", Segments: []string{"COM"}, Direction: "in", At: "08:00", NextDay: true},
	{ID: "mcx-mrg", Title: "Margin reporting file to MCXCCL", Format: "mcx-margin-reporting", Party: "MCXCCL", Segments: []string{"COM"}, Direction: "out", At: "12:00", NextDay: true},
}

const cDailyRecv = "xfilerecv" // date|format|name

// DailyFileRecv records a file received or written for a date.
type DailyFileRecv struct {
	Date   string `json:"date"`
	Format string `json:"format"`
	Name   string `json:"name"`
	At     string `json:"at"`
	By     string `json:"by"`
}

// noteDailyFile ticks a file off the checklist of its date (from the file
// name; today when the name has none).
func (o *Ops) noteDailyFile(ctx context.Context, tenant, actor, format, name, date string) {
	if date == "" {
		date = ymdFromName(name)
	}
	if date == "" {
		date = bseFileDate(name, o.now())
	}
	if date == "" {
		date = o.now().In(ist).Format("2006-01-02")
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cDailyRecv, date+"|"+format+"|"+name, DailyFileRecv{Date: date, Format: format, Name: name, At: o.now().In(ist).Format("2006-01-02 15:04"), By: actor}, actor)
}

// ChecklistItem is one expected file on the checklist.
type ChecklistItem struct {
	DailyFile
	For    []string `json:"for"`    // exchange segments it serves
	Due    string   `json:"due"`    // YYYY-MM-DD HH:MM
	Status string   `json:"status"` // Received, Due, Missing, Intraday, Check by hand
	Files  []string `json:"files,omitempty"`
}

// FileChecklist is a day's expected files.
type FileChecklist struct {
	Date     string          `json:"date"`
	Items    []ChecklistItem `json:"items"`
	Missing  int             `json:"missing"`
	Notes    []string        `json:"notes"`
	Clearing []ClearingRow   `json:"clearing"`
}

// ClearingRow shows how one traded segment is cleared.
type ClearingRow struct {
	Key     string `json:"key"`
	Through string `json:"through"`
	Name    string `json:"name"`
}

// DailyFileChecklist lists the files expected for a trading date under the
// firm's clearing arrangements, with what has arrived.
func (o *Ops) DailyFileChecklist(ctx context.Context, tenant, date string) (FileChecklist, error) {
	if !validDate(date) {
		return FileChecklist{}, invalid("give a date (YYYY-MM-DD)")
	}
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return FileChecklist{}, err
	}
	out := FileChecklist{Date: date, Items: []ChecklistItem{}, Notes: []string{}, Clearing: []ClearingRow{}}
	act := set.activeSegments()
	for _, c := range act {
		th := set.ClearedThrough(c.Key)
		row := ClearingRow{Key: c.Key, Through: th, Name: ClearingCorpNames[th]}
		if strings.HasPrefix(th, "CM:") {
			row.Name = set.ClearingMemberNames[c.Key] + " (clearing member " + th[3:] + ")"
			out.Notes = append(out.Notes, fmt.Sprintf("%s is cleared by %s: its clearing-corporation files (margins, collateral, allocation) come from the clearing member, not to this checklist.", c.Key, row.Name))
		}
		out.Clearing = append(out.Clearing, row)
	}
	recv, _ := docs.List[DailyFileRecv](ctx, o.Docs, tenant, cDailyRecv, date+"|")
	now := o.now().In(ist).Format("2006-01-02 15:04")
	for _, f := range DailyFiles {
		var serves []string
		for _, c := range act {
			if !contains(f.Segments, c.Segment) {
				continue
			}
			if f.Exchange != "" && f.Exchange == c.Exchange || f.Exchange == "" && set.ClearedThrough(c.Key) == f.Party {
				serves = append(serves, c.Key)
			}
		}
		if len(serves) == 0 {
			continue
		}
		it := ChecklistItem{DailyFile: f, For: serves}
		day := date
		if f.NextDay {
			day = set.NextBusinessDay(date)
		}
		it.Due = day + " " + f.At
		var rx *regexp.Regexp
		if f.Name != "" {
			rx = regexp.MustCompile("(?i)" + f.Name)
		}
		for _, r := range recv {
			if f.Format != "" && r.Format == f.Format && (rx == nil || rx.MatchString(r.Name)) {
				it.Files = append(it.Files, r.Name)
			}
		}
		switch {
		case len(it.Files) > 0:
			it.Status = "Received"
			if f.Direction == "out" {
				it.Status = "Written"
			}
		case f.Format == "":
			it.Status = "Check by hand"
		case f.Intraday:
			it.Status = "Intraday"
		case now > it.Due:
			it.Status = "Missing"
			out.Missing++
		default:
			it.Status = "Due"
		}
		out.Items = append(out.Items, it)
	}
	sort.SliceStable(out.Items, func(i, j int) bool { return out.Items[i].Due < out.Items[j].Due })
	return out, nil
}

// MissingDailyFiles lists the expected files past their time and not
// received, and records an xfile.missing alert in the audit trail for each,
// once a day.
func (o *Ops) MissingDailyFiles(ctx context.Context, tenant, actor, date string) ([]ChecklistItem, error) {
	cl, err := o.DailyFileChecklist(ctx, tenant, date)
	if err != nil {
		return nil, err
	}
	var out []ChecklistItem
	for _, it := range cl.Items {
		if it.Status != "Missing" {
			continue
		}
		out = append(out, it)
		id := date + "|alert|" + it.ID
		if _, _, err := docs.Get[DailyFileRecv](ctx, o.Docs, tenant, cDailyRecv, id); err == nil {
			continue
		}
		_ = docs.Upsert(ctx, o.Docs, tenant, cDailyRecv, id, DailyFileRecv{Date: date, Format: "alert", Name: it.ID, At: o.now().In(ist).Format(time.DateTime), By: actor}, actor)
		o.log(ctx, tenant, actor, "xfile.missing", it.ID, map[string]any{"date": date, "title": it.Title, "party": it.Party, "due": it.Due, "for": strings.Join(it.For, ", ")})
	}
	return out, nil
}
