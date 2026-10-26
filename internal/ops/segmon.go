package ops

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"strings"
	"time"

	"ledgerline/internal/money"
)

// Client-level collateral reporting to the clearing corporation (SEBI's
// segregation and monitoring of collateral at client level, "SEGMON").
// Each trading member reports, every day, the collateral of each client:
// cash allocated, securities re-pledged, the total, and the client's margin.
// NSE Clearing's CC01 file (circular NCL/CMPT/078/2022) carries:
//
//	Client/UCC Code, Cash Component-Allocated,
//	Cash Component-Value of Gsec/GMF/CMF repledged,
//	Non-Cash Component-Value of Non-Cash repledged, Total Value, Margins
//
// and the cash-market file (CC02) has the trading member code first. The
// layout is checked against the clearing corporation's current file
// specification before the first upload (it is revised from time to time,
// for example with retention reason codes); Settings carries the choices.

// CCSegments are the segments reported, with the clearing corporation's
// file code. CM is reported with the member code first.
var CCSegments = map[string]string{"FO": "CC01", "CD": "CC01", "COM": "CC01", "CM": "CC02"}

// CollateralRow is one client's reported collateral in one segment.
type CollateralRow struct {
	Segment  string    `json:"segment"`
	UCC      string    `json:"ucc"`
	Name     string    `json:"name"`
	Cash     money.Dec `json:"cash"`     // cash allocated to the segment
	GSec     money.Dec `json:"gsec"`     // G-secs, gilt and Treasury funds re-pledged (cash-equivalent)
	NonCash  money.Dec `json:"non_cash"` // other securities re-pledged, after haircut
	Total    money.Dec `json:"total"`    // Cash + GSec + NonCash
	Margins  money.Dec `json:"margins"`  // margin required at end of day
	Shortage money.Dec `json:"shortage"` // margin not covered by the collateral
}

// ClientCollateral builds the day's client-level collateral, every segment.
// It follows the end-of-day allocation (the same split the firm uploads as
// its client-level allocation), so the two always agree.
func (o *Ops) ClientCollateral(ctx context.Context, tenant, date string) ([]CollateralRow, error) {
	if !validDate(date) {
		return nil, invalid("give the trade date")
	}
	al, err := o.Allocation(ctx, tenant, date)
	if err != nil {
		return nil, err
	}
	cs, _ := o.ClientMap(ctx, tenant)
	var out []CollateralRow
	for _, a := range al {
		if _, ok := CCSegments[a.Segment]; !ok {
			continue
		}
		r := CollateralRow{Segment: a.Segment, UCC: a.UCC, Name: cs[a.UCC].Name, Cash: a.Cash, NonCash: a.NonCash, Margins: a.Required}
		r.Total = r.Cash.Add(r.GSec).Add(r.NonCash)
		if d := r.Margins.Sub(r.Total); d.Sign() > 0 {
			r.Shortage = d
		}
		out = append(out, r)
	}
	return out, nil
}

// collateralRows is a segment's file content in the clearing corporation's
// column order.
func collateralRows(rows []CollateralRow, segment, member string, header bool) [][]string {
	tmFirst := CCSegments[segment] == "CC02"
	var out [][]string
	if header {
		h := []string{"Client/UCC Code", "Cash Component-Allocated", "Cash Component-Value of Gsec/GMF/CMF repledged", "Non-Cash Component-Value of Non-Cash repledged", "Total Value", "Margins"}
		if tmFirst {
			h = append([]string{"TM Code"}, h...)
		}
		out = append(out, h)
	}
	for _, r := range rows {
		if r.Segment != segment {
			continue
		}
		l := []string{r.UCC, r.Cash.Fixed(2), r.GSec.Fixed(2), r.NonCash.Fixed(2), r.Total.Fixed(2), r.Margins.Fixed(2)}
		if tmFirst {
			l = append([]string{member}, l...)
		}
		out = append(out, l)
	}
	return out
}

// ClientCollateralFile is one segment's upload file, gzip-compressed and
// named as the clearing corporation expects: C_CC01_<member>_<DDMMYYYY>.csv.gz.
func (o *Ops) ClientCollateralFile(ctx context.Context, tenant, actor, date, segment string, header bool) (string, []byte, error) {
	if err := o.require(ctx, tenant, actor, "Downloading the collateral file", RoleMaker, RoleChecker, RoleCompliance, RoleRisk); err != nil {
		return "", nil, err
	}
	segment = strings.ToUpper(strings.TrimSpace(segment))
	code, ok := CCSegments[segment]
	if !ok {
		return "", nil, invalid("segment must be one of CM, FO, CD or COM")
	}
	set, _ := o.Settings(ctx, tenant)
	member := strings.TrimSpace(set.MemberCodes["NSE"])
	if member == "" {
		return "", nil, invalid("set the firm's NSE member code in Settings first; it is part of the file name")
	}
	rows, err := o.ClientCollateral(ctx, tenant, date)
	if err != nil {
		return "", nil, err
	}
	d, _ := time.Parse("2006-01-02", date)
	name := fmt.Sprintf("C_%s_%s_%s.csv.gz", code, member, d.Format("02012006"))
	if segment != "FO" {
		name = fmt.Sprintf("C_%s_%s_%s_%s.csv.gz", code, member, segment, d.Format("02012006"))
	}
	var b bytes.Buffer
	z := gzip.NewWriter(&b)
	z.Name = strings.TrimSuffix(name, ".gz")
	z.ModTime = d
	if _, err := z.Write(csvBytesOf(collateralRows(rows, segment, member, header))); err != nil {
		return "", nil, err
	}
	if err := z.Close(); err != nil {
		return "", nil, err
	}
	o.log(ctx, tenant, actor, "collateral.file", date, map[string]any{"segment": segment, "file": name})
	return name, b.Bytes(), nil
}

// collateralReportRows is the review copy for the Regulatory reports page:
// every segment, with the client's name and any shortage.
func (o *Ops) collateralReportRows(ctx context.Context, tenant, date string) ([][]string, error) {
	rows, err := o.ClientCollateral(ctx, tenant, date)
	if err != nil {
		return nil, err
	}
	out := [][]string{{"Segment", "ClientCode", "Name", "CashAllocated", "GsecGMFCMFRepledged", "NonCashRepledged", "TotalValue", "Margins", "Shortage"}}
	for _, r := range rows {
		out = append(out, []string{r.Segment, r.UCC, r.Name, r.Cash.Fixed(2), r.GSec.Fixed(2), r.NonCash.Fixed(2), r.Total.Fixed(2), r.Margins.Fixed(2), r.Shortage.Fixed(2)})
	}
	return out, nil
}
