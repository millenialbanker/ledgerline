package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
)

// ---- holding statement to BSE ----
//
// BSE notice 20230113-13 (13 January 2023) moved the holding statement from
// weekly to daily from 30 January 2023: each day of the week except Sunday,
// due the next day, by BSE's API (Annexure I of the notice) or on BSE's
// BEFS portal. The daily bank balances in the same notice are no longer
// sent by members: the banks report them (NSE/INSP/59044 from 30 October
// 2023; the firm only confirms its banks do, a yearly duty).
//
// From 15 October 2025 a member of NSE that is also a member of BSE reports
// the holding statement to NSE only, and NSE shares it with BSE
// (NSE/INSP/70505, Phase II, item 25). So this path is for a BSE member that
// is not an NSE member; for others the screen says the NSE submission
// covers BSE.
//
// BSE's API document is given to members only and has not been published,
// so there is no BSE API connector yet: the statement is downloaded here,
// checked with the same record checks as the NSE submission, uploaded on
// BEFS by a person, and recorded with BEFS's acknowledgement. A day not
// recorded by the next day opens a work item. The API is to be added once
// the firm obtains BSE's API specification.

const (
	cBSEHFiling = "bsehfiling" // key: data date

	// BSEHoldingAPIStatus is shown until BSE's API document is available.
	BSEHoldingAPIStatus = "Waiting for BSE's API document (Annexure I of BSE notice 20230113-13 is given to members, not published): upload on BEFS until then"

	// sharedHoldingFrom is when NSE started sharing the holding statement
	// with the other exchanges (NSE/INSP/70505).
	sharedHoldingFrom = "2025-10-15"
)

// BSEHoldingFiling is one day's holding statement uploaded on BEFS.
type BSEHoldingFiling struct {
	Date    string `json:"date"`
	Channel string `json:"channel"` // BEFS portal
	Ack     string `json:"ack"`
	File    string `json:"file,omitempty"`
	Records int    `json:"records"`
	SHA256  string `json:"sha256,omitempty"`
	By      string `json:"by"`
	At      string `json:"at"`
}

// BSEHoldingView is the BSE part of the holding statement panel.
type BSEHoldingView struct {
	Date     string             `json:"date"`
	Applies  bool               `json:"applies"`
	Note     string             `json:"note"`
	API      string             `json:"api"`
	Pending  []string           `json:"pending"`  // data dates not yet recorded as uploaded
	Problems []string           `json:"problems"` // record checks on the file for Date
	Filings  []BSEHoldingFiling `json:"filings"`
}

// bseHoldingApplies says whether the firm itself sends BSE the holding
// statement for a data date, and why not.
func bseHoldingApplies(set Settings, date string) (bool, string) {
	exs := memberExchanges(set)
	switch {
	case !contains(exs, "BSE"):
		return false, "The firm has no BSE member code in Settings, so no holding statement goes to BSE."
	case contains(exs, "NSE") && date >= sharedHoldingFrom:
		return false, "Covered by the NSE submission: from 15 October 2025 a member of NSE and BSE reports the daily holding statement to NSE only, and NSE shares it with BSE (NSE/INSP/70505)."
	}
	return true, "Daily except Sunday, by the next day, on BSE's BEFS portal (BSE notice 20230113-13)."
}

// bseHoldingStart is the first data date the firm tracks: the day before
// the BSE duty was added to the calendar.
func (o *Ops) bseHoldingStart(ctx context.Context, tenant string) string {
	d, _, err := docs.Get[Duty](ctx, o.Docs, tenant, cObligation, "OB-BSEHOLD")
	if err == nil && validDate(d.Since) {
		return addDays(d.Since, -1)
	}
	return ""
}

// bseHoldingPending are the data dates up to the day before date (not
// Sundays, from the start, at most 31 days back) not recorded as uploaded.
func (o *Ops) bseHoldingPending(ctx context.Context, tenant string, set Settings, date string) []string {
	out := []string{}
	start := o.bseHoldingStart(ctx, tenant)
	if start == "" {
		return out
	}
	if lim := addDays(date, -31); start < lim {
		start = lim
	}
	fs, _ := docs.List[BSEHoldingFiling](ctx, o.Docs, tenant, cBSEHFiling, "")
	done := map[string]bool{}
	for _, f := range fs {
		done[f.Date] = true
	}
	for d := start; d < date; d = addDays(d, 1) {
		if t, _ := time.Parse("2006-01-02", d); t.Weekday() == time.Sunday || done[d] {
			continue
		}
		if ok, _ := bseHoldingApplies(set, d); ok {
			out = append(out, d)
		}
	}
	return out
}

// bseHoldingFile is the statement for a data date with its record checks.
func (o *Ops) bseHoldingFile(ctx context.Context, tenant string, set Settings, date string) (string, []byte, []holdingItem, error) {
	lib, err := XFiles()
	if err != nil {
		return "", nil, nil, err
	}
	spec, _ := lib.Get("holding-statement-api")
	name, j, err := o.holdingStatementFile(ctx, tenant, spec, set, date, map[string]string{"date": date, "seq": "1"})
	if err != nil {
		return "", nil, nil, err
	}
	var msg struct {
		HoldingData []holdingItem `json:"holdingData"`
	}
	if err := json.Unmarshal(j, &msg); err != nil {
		return "", nil, nil, err
	}
	return name, j, msg.HoldingData, nil
}

// BSEHolding is the BSE holding statement panel for a data date.
func (o *Ops) BSEHolding(ctx context.Context, tenant, actor, date string) (BSEHoldingView, error) {
	if err := o.require(ctx, tenant, actor, "Downloading exchange files", RoleMaker, RoleChecker, RoleRisk, RoleCompliance); err != nil {
		return BSEHoldingView{}, err
	}
	if !validDate(date) {
		return BSEHoldingView{}, invalid("give the date the holdings are for")
	}
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return BSEHoldingView{}, err
	}
	v := BSEHoldingView{Date: date, API: BSEHoldingAPIStatus, Pending: []string{}, Problems: []string{}, Filings: []BSEHoldingFiling{}}
	v.Applies, v.Note = bseHoldingApplies(set, date)
	if !v.Applies {
		v.API = ""
	}
	v.Pending = o.bseHoldingPending(ctx, tenant, set, o.now().In(ist).Format("2006-01-02"))
	if v.Applies {
		if _, _, items, err := o.bseHoldingFile(ctx, tenant, set, date); err == nil {
			v.Problems = append(v.Problems, nseHoldingProblems(items)...)
		} else {
			v.Problems = append(v.Problems, strings.TrimPrefix(err.Error(), "invalid: "))
		}
	}
	fs, err := docs.List[BSEHoldingFiling](ctx, o.Docs, tenant, cBSEHFiling, "")
	sort.SliceStable(fs, func(i, j int) bool { return fs[i].Date > fs[j].Date })
	if len(fs) > 60 {
		fs = fs[:60]
	}
	if fs != nil {
		v.Filings = fs
	}
	return v, err
}

// BSEHoldingUploaded records a day's statement uploaded on BEFS with its
// acknowledgement. The file is checked first, as for the NSE API: a record
// that would fail is not recorded as sent.
func (o *Ops) BSEHoldingUploaded(ctx context.Context, tenant, actor, date, ack string) (BSEHoldingFiling, error) {
	if err := o.require(ctx, tenant, actor, "Recording the holding statement sent to BSE", RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return BSEHoldingFiling{}, err
	}
	if !validDate(date) {
		return BSEHoldingFiling{}, invalid("give the date the holdings are for")
	}
	if t, _ := time.Parse("2006-01-02", date); t.Weekday() == time.Sunday {
		return BSEHoldingFiling{}, invalid("BSE takes no holding statement for a Sunday")
	}
	ack = strings.TrimSpace(ack)
	if ack == "" {
		return BSEHoldingFiling{}, invalid("enter BEFS's acknowledgement or upload reference")
	}
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return BSEHoldingFiling{}, err
	}
	if ok, why := bseHoldingApplies(set, date); !ok {
		return BSEHoldingFiling{}, fmt.Errorf("%w: %s", ErrState, why)
	}
	f := BSEHoldingFiling{Date: date, Channel: "BEFS portal", Ack: ack, By: actor, At: o.now().Format(time.RFC3339)}
	// The file can be made only from the holdings Ledgerline has now: for a
	// past day it describes what was uploaded, not what is held today.
	if name, j, items, err := o.bseHoldingFile(ctx, tenant, set, date); err == nil {
		if ps := nseHoldingProblems(items); len(ps) > 0 && date == o.latestHoldingFile(ctx, tenant) {
			return f, invalid("%d record(s) would fail the exchange's checks, for example %s; correct them and upload again", len(ps), ps[0])
		}
		f.File, f.Records, f.SHA256 = maskPANs(name), len(items), sha256Hex(j)
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cBSEHFiling, date, f, actor); err != nil {
		return f, err
	}
	o.log(ctx, tenant, actor, "holding.bse.uploaded", date, map[string]any{"ack": ack, "records": f.Records})
	o.closeWork(ctx, tenant, actor, "bse-holding", date, "uploaded on BEFS, acknowledgement "+ack)
	return f, nil
}

// bseHoldingChase opens a work item for each day not recorded as uploaded
// by its due day (the compliance job, every morning).
func (o *Ops) bseHoldingChase(ctx context.Context, tenant, date string) int {
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return 0
	}
	n := 0
	for _, d := range o.bseHoldingPending(ctx, tenant, set, date) {
		o.raiseWork(ctx, tenant, "bse-holding", d, "Holding statement for "+fmtDay(d)+" to BSE: upload on BEFS",
			"Download the holding statement for "+fmtDay(d)+" on Exchange files, upload it on BSE's BEFS portal and record BEFS's acknowledgement there. BSE takes it daily except Sunday, by the next day (BSE notice 20230113-13). BSE's API is not set up: its document is not public.", "", RoleCompliance, "xfiles", date)
		n++
	}
	return n
}
