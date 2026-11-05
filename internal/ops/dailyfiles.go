package ops

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

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
// clearing member has no clearing-corporation files (they come from that
// member) except those the clearing corporation sends the trading member
// itself (TMLevel: MCXCCL's NOBL, MCX_MRG, peak margin, NCMR and the
// like, which a trading member under another clearing member still
// receives on its own FTP folder). The writers of outgoing files are not affected: they work for
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
	// TMLevel marks a clearing corporation's file to the trading member:
	// sent to the firm itself even when another clearing member clears the
	// segment (MCXCCL's TM files: NOBL, MCX_MRG, peak margin, NCMR …).
	TMLevel bool `json:"tm_level,omitempty"`
	// Also are other layouts of the same file (a clearing member's version
	// under the same name) that tick it off.
	Also []string `json:"-"`
	// HandNote says, for a file with no layout yet (checked by hand), what
	// Ledgerline needs before it can read or write it.
	HandNote string `json:"hand_note,omitempty"`
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
	{ID: "mcx-products", Title: "MCX product (contract) master", Format: "mcx-product-master", Party: "MCX", Exchange: "MCX", Segments: []string{"COM"}, Direction: "in", At: "08:00"},
	{ID: "mcx-cp", Title: "MCX custodian participant master", Format: "mcx-cp-master", Party: "MCX", Exchange: "MCX", Segments: []string{"COM"}, Direction: "in", At: "08:00"},
	{ID: "mcx-trade-f", Title: "MCX trade file", Format: "mcx-trade", Party: "MCX", Exchange: "MCX", Segments: []string{"COM"}, Direction: "in", At: "23:59"},
	{ID: "mcx-orders", Title: "MCX online orders (ORD, from the trading terminal)", Format: "mcx-orders", Party: "MCX", Exchange: "MCX", Segments: []string{"COM"}, Direction: "in", At: "23:59"},
	{ID: "mcx-bhav", Title: "MCX bhavcopy (common format)", Format: "mcx-bhavcopy", Party: "MCX", Exchange: "MCX", Segments: []string{"COM"}, Direction: "in", At: "07:00", NextDay: true},
	{ID: "mcx-index", Title: "MCX daily index file", Format: "mcx-daily-index", Party: "MCX", Exchange: "MCX", Segments: []string{"COM"}, Direction: "in", At: "07:00", NextDay: true},
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
	{ID: "ncl-mrg", Title: "Margin reporting file to NSE Clearing, cash", Format: "ncl-margin-reporting", Party: "NCL", Segments: []string{"CM"}, Direction: "out", At: "12:00", NextDay: true},
	{ID: "ncl-mrg-fo", Title: "Margin reporting file to NSE Clearing, F&O", Format: "ncl-margin-reporting-fo", Party: "NCL", Segments: []string{"FO"}, Direction: "out", At: "12:00", NextDay: true},
	{ID: "ncl-sa-rpt", Title: "Short allocation reporting to NSE Clearing", Format: "ncl-short-allocation-reporting", Party: "NCL", Segments: all3, Direction: "out", At: "12:00", NextDay: true},
	{ID: "ncl-sett", Title: "Combined settlement master (monthly)", Format: "ncl-settlement-master", Party: "NCL", Segments: all3, Direction: "in", At: "09:00", Intraday: true},
	// ICCL: layouts not yet in Ledgerline, checked by hand. No public ICCL
	// or BSE circular found (November 2026) publishes the column layout of
	// these files: the eligible-collateral notices of 17 December 2025
	// ("Standardization of file format for eligible collateral") name some
	// fields but not the full ordered layout, BSE notice 20230120-19 (20
	// January 2023, for ICCL) lists the fields of the intraday short
	// collateral report only and says the formats will be given separately,
	// and BSE's member downloads (ICCL sections) hold paper formats for
	// bank guarantees, FDRs and pledges. Ledgerline does not guess layouts:
	// each file stays "check by hand" until ICCL's own file description is
	// in hand (from the member extranet or ICCL), and its HandNote says what
	// is needed.
	{ID: "iccl-var", Title: "ICCL VaR and ELM rates (VARELMAM)", Party: "ICCL", Segments: []string{"CM"}, Direction: "in", At: "08:30", HandNote: iCCLNeed("the VaR and ELM rates file (VARELMAM): its columns, delimiter and whether it has a header")},
	{ID: "iccl-haircut", Title: "ICCL haircuts (approved securities)", Party: "ICCL", Segments: all3, Direction: "in", At: "08:30", HandNote: iCCLNeed("the eligible-collateral (haircut) file in the standard format of December 2025: the full ordered field list with lengths, and the file name")},
	{ID: "iccl-coll", Title: "ICCL collateral reports", Party: "ICCL", Segments: all3, Direction: "in", At: "20:00", HandNote: iCCLNeed("the end-of-day client collateral reports: which reports, their names and columns")},
	{ID: "iccl-margin", Title: "ICCL client margin file", Party: "ICCL", Segments: all3, Direction: "in", At: "20:00", HandNote: iCCLNeed("the client margin file per segment: its columns (margin types, as of time) and name")},
	{ID: "iccl-alloc", Title: "Client allocation to ICCL", Party: "ICCL", Segments: all3, Direction: "out", At: "19:00", HandNote: iCCLNeed("the client collateral allocation upload: field order, codes and the response file")},
	{ID: "iccl-icoll", Title: "ICOLL response (ICCL collateral)", Party: "ICCL", Segments: all3, Direction: "in", At: "19:30", HandNote: iCCLNeed("the ICOLL response file: its columns and status codes")},
	{ID: "iccl-mrg", Title: "Margin reporting file to ICCL", Party: "ICCL", Segments: all3, Direction: "out", At: "12:00", NextDay: true, HandNote: iCCLNeed("the client margin reporting upload (collected against required margin): field order, segment codes and the file name")},
	// MCXCCL: trading-member files, which the firm receives itself even when
	// another clearing member clears its commodity trades.
	{ID: "mcx-pnobl", Title: "MCXCCL provisional obligation (PNOBL)", Format: "mcx-obligation-tm", Also: []string{"mcx-obligation-cm"}, Name: `MCX_PNOBL_`, Party: "MCXCCL", Segments: []string{"COM"}, Direction: "in", At: "18:00", Intraday: true, TMLevel: true},
	{ID: "mcx-peak", Title: "MCXCCL peak margin snapshots", Format: "mcx-peak-margin-tm", Also: []string{"mcx-peak-margin-cm"}, Party: "MCXCCL", Segments: []string{"COM"}, Direction: "in", At: "10:00", Intraday: true, TMLevel: true},
	{ID: "mcx-dcnote", Title: "MCXCCL debit / credit notes", Format: "mcx-dc-note", Party: "MCXCCL", Segments: []string{"COM"}, Direction: "in", At: "18:00", Intraday: true, TMLevel: true},
	{ID: "mcx-nobl", Title: "MCXCCL obligation (NOBL)", Format: "mcx-obligation-tm", Also: []string{"mcx-obligation-cm"}, Name: `MCX_NOBL_`, Party: "MCXCCL", Segments: []string{"COM"}, Direction: "in", At: "08:00", NextDay: true, TMLevel: true},
	{ID: "mcx-mrgfile", Title: "MCXCCL margin file (MCX_MRG)", Format: "mcx-margin-tm", Also: []string{"mcx-margin-cm"}, Party: "MCXCCL", Segments: []string{"COM"}, Direction: "in", At: "08:00", NextDay: true, TMLevel: true},
	{ID: "mcx-margin", Title: "MCXCCL client margin file", Format: "mcx-client-margin", Party: "MCXCCL", Segments: []string{"COM"}, Direction: "in", At: "08:00", NextDay: true, TMLevel: true},
	{ID: "mcx-udiff-margin", Title: "MCXCCL margin file (UDiFF)", Format: "mcxccl-margin", Party: "MCXCCL", Segments: []string{"COM"}, Direction: "in", At: "08:00", NextDay: true, TMLevel: true},
	{ID: "mcx-udiff-position", Title: "MCXCCL position file (UDiFF)", Format: "mcxccl-position", Party: "MCXCCL", Segments: []string{"COM"}, Direction: "in", At: "08:00", NextDay: true, TMLevel: true},
	{ID: "mcx-ctt", Title: "MCXCCL CTT file (UDiFF)", Format: "mcxccl-ctt", Party: "MCXCCL", Segments: []string{"COM"}, Direction: "in", At: "08:00", NextDay: true, TMLevel: true},
	{ID: "mcx-stamp", Title: "MCXCCL stamp duty file (UDiFF)", Format: "mcxccl-stamp", Party: "MCXCCL", Segments: []string{"COM"}, Direction: "in", At: "08:00", NextDay: true, TMLevel: true},
	{ID: "mcx-collateral", Title: "MCXCCL collateral file (MCX_CD)", Format: "mcx-collateral", Party: "MCXCCL", Segments: []string{"COM"}, Direction: "in", At: "08:00", NextDay: true, TMLevel: true},
	{ID: "mcx-collateral-ucc", Title: "MCXCCL client collateral file (MCX_CD_…_UCC)", Format: "mcx-collateral-client", Party: "MCXCCL", Segments: []string{"COM"}, Direction: "in", At: "08:00", NextDay: true, TMLevel: true},
	{ID: "mcx-weballoc", Title: "MCXCCL web allocation / deallocation report", Format: "mcx-web-allocation", Party: "MCXCCL", Segments: []string{"COM"}, Direction: "in", At: "08:00", NextDay: true, TMLevel: true},
	{ID: "mcx-eodsar", Title: "MCXCCL EOD short allocation (EODSAR)", Format: "mcx-eod-short-allocation", Party: "MCXCCL", Segments: []string{"COM"}, Direction: "in", At: "09:00", NextDay: true, TMLevel: true},
	{ID: "mcx-intrasar", Title: "MCXCCL intraday short allocation (INTRASAR)", Format: "mcx-intraday-short-allocation", Party: "MCXCCL", Segments: []string{"COM"}, Direction: "in", At: "09:00", NextDay: true, TMLevel: true},
	{ID: "mcx-mrg", Title: "Margin reporting file to MCXCCL", Format: "mcx-margin-reporting", Party: "MCXCCL", Segments: []string{"COM"}, Direction: "out", At: "12:00", NextDay: true, TMLevel: true},
	{ID: "mcx-ncmr", Title: "MCXCCL margin shortfall and penalty (NCMR)", Format: "mcx-ncmr", Party: "MCXCCL", Segments: []string{"COM"}, Direction: "in", At: "20:00", NextDay: true, TMLevel: true},
	{ID: "mcx-penalty", Title: "MCXCCL penalty file (UDiFF)", Format: "mcxccl-penalty", Party: "MCXCCL", Segments: []string{"COM"}, Direction: "in", At: "20:00", NextDay: true, TMLevel: true},
	// CDSL, for a firm that is a CDSL DP (listed when the CDSL connection
	// has a DP ID or a CDSL member code is set; cdslDPFirm).
	{ID: "cdsl-cln", Title: "CDSL client master (CLN_MSTR, incremental)", Format: "cdsl-client-master", Party: "CDSL", Segments: []string{"DP"}, Direction: "in", At: "09:00"},
	{ID: "cdsl-dpe6", Title: "CDSL online freeze master report (DPE6), as it arrives", Format: "cdsl-freeze-master", Party: "CDSL", Segments: []string{"DP"}, Direction: "in", At: "09:00", Intraday: true},
}

// cdslDPFirm says whether the firm is a CDSL DP, for the checklist.
func (o *Ops) cdslDPFirm(ctx context.Context, tenant string, set Settings) bool {
	if x, ok := o.cdxConn(ctx, tenant); ok && strings.TrimSpace(x.DPID) != "" {
		return true
	}
	return strings.TrimSpace(set.MemberCodes["CDSL"]) != "" || strings.TrimSpace(set.MemberCodes["CDSL-SENDER"]) != ""
}

// iCCLNeed is the note on an ICCL file Ledgerline cannot read yet.
func iCCLNeed(what string) string {
	return "Checked by hand: no public ICCL layout found. To read it, Ledgerline needs ICCL's file description of " + what + " (ICCL member extranet or ICCL's clearing operations), and a sample file with made-up data."
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
	o.closeMissingWork(ctx, tenant, actor, format, name, date) // filealerts.go
}

// ChecklistItem is one expected file on the checklist.
type ChecklistItem struct {
	DailyFile
	Date   string   `json:"date"`   // the trading date it is for
	For    []string `json:"for"`    // exchange segments it serves
	Due    string   `json:"due"`    // YYYY-MM-DD HH:MM
	Status string   `json:"status"` // Received, Due, Missing, Intraday, Check by hand
	Files  []string `json:"files,omitempty"`
	// AlertedAt is when the file was chased as missing (filealerts.go).
	AlertedAt string `json:"alerted_at,omitempty"`
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
			note := fmt.Sprintf("%s is cleared by %s: its clearing-corporation files (margins, collateral, allocation) come from the clearing member, not to this checklist.", c.Key, row.Name)
			for _, f := range DailyFiles {
				if f.TMLevel && contains(f.Segments, c.Segment) && contains(c.Allowed, f.Party) {
					note += fmt.Sprintf(" The files %s sends the trading member itself (obligation, margin, peak margin, penalty …) are still listed.", f.Party)
					break
				}
			}
			out.Notes = append(out.Notes, note)
		}
		out.Clearing = append(out.Clearing, row)
	}
	recv, _ := docs.List[DailyFileRecv](ctx, o.Docs, tenant, cDailyRecv, date+"|")
	alerted := alertTimes(recv)
	now := o.now().In(ist).Format("2006-01-02 15:04")
	dp := o.cdslDPFirm(ctx, tenant, set)
	for _, f := range DailyFiles {
		var serves []string
		if f.Party == "CDSL" && dp {
			serves = []string{"CDSL DP"}
		}
		for _, c := range act {
			if !contains(f.Segments, c.Segment) {
				continue
			}
			th := set.ClearedThrough(c.Key)
			switch {
			case f.Exchange != "" && f.Exchange == c.Exchange, f.Exchange == "" && th == f.Party:
				serves = append(serves, c.Key)
			case f.Exchange == "" && f.TMLevel && strings.HasPrefix(th, "CM:") && contains(c.Allowed, f.Party):
				// cleared by another member: the trading-member file still comes
				serves = append(serves, c.Key)
			}
		}
		if len(serves) == 0 {
			continue
		}
		it := ChecklistItem{DailyFile: f, Date: date, For: serves, AlertedAt: alerted[f.ID]}
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
			if f.Format != "" && (r.Format == f.Format || contains(f.Also, r.Format)) && (rx == nil || rx.MatchString(r.Name)) {
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
// received, and chases each (an xfile.missing entry in the audit trail and
// a work item), once a day; with no grace period, unlike the scheduled
// alerts (RunFileAlerts), and with no email.
func (o *Ops) MissingDailyFiles(ctx context.Context, tenant, actor, date string) ([]ChecklistItem, error) {
	if _, err := o.chaseMissing(ctx, tenant, actor, date, o.now().In(ist).Format("2006-01-02 15:04")); err != nil {
		return nil, err
	}
	cl, err := o.DailyFileChecklist(ctx, tenant, date)
	if err != nil {
		return nil, err
	}
	var out []ChecklistItem
	for _, it := range cl.Items {
		if it.Status == "Missing" {
			out = append(out, it)
		}
	}
	return out, nil
}
