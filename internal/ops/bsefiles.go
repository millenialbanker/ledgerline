package ops

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
	"ledgerline/internal/xfile"
)

// ---- BSE extranet files (and the SEBI common bhavcopy) ----
//
//   - BSE's stamp duty file MBRSTD<nnn>_OTB_CM_<member>_<DDMMYY>.CSV: a
//     summary block (header, one row with the record count and total) and
//     a details block (header, one row per client, scrip and settlement
//     with square-off and delivery stamp duty). The summary must agree
//     with the details; the details are kept and reconciled client-wise on
//     Clearing data against the stamp duty billed on BSE trades.
//   - The SEBI common (UDiFF) bhavcopy, BhavCopy_<NSE|BSE>_<CM|FO>_0_0_0_
//     <YYYYMMDD>_F_0000.csv: closing, last and settlement prices go into
//     the day's market data for that exchange and segment. BSE cash rows
//     carry the BSE scrip code (FinInstrmId); a row without an ISIN or
//     symbol takes them from the BSE scrip and series mapping, and the
//     mapping learns the scrip code of ISINs it did not have.
//
// BSE's provisional (_P_) and final (_F_) trade files are the SEBI
// standardised trade layout and are loaded with NSE's (xfiles.go,
// provtrades.go).

// loadBSEXFile handles the files above; ok is false for any other file.
func (o *Ops) loadBSEXFile(ctx context.Context, tenant, actor string, s *xfile.Spec, f xfile.File, rows []xfile.Row, r *XFileResult) bool {
	fail := func(err error) { r.Loaded = "Not loaded: " + strings.TrimPrefix(err.Error(), "invalid: ") }
	switch s.ID {
	case "bse-stamp-duty":
		date, n, err := o.loadBSEStamp(ctx, tenant, actor, f.Name, rows)
		if err != nil {
			fail(err)
			return true
		}
		r.Loaded, r.Added = "Loaded and reconciled with the stamp duty Ledgerline charged on BSE trades of "+date+" (Clearing data)", n
	case "udiff-bhavcopy":
		date, exch, seg, n, err := o.loadBhavcopy(ctx, tenant, actor, f.Name, rows)
		if err != nil {
			fail(err)
			return true
		}
		r.Loaded, r.Added = fmt.Sprintf("Loaded as %s %s closing prices of %s (market data)", exch, seg, date), n
	default:
		return false
	}
	return true
}

// loadBSEStamp checks the summary against the details and keeps the details.
func (o *Ops) loadBSEStamp(ctx context.Context, tenant, actor, name string, rows []xfile.Row) (string, int, error) {
	var sums, det []xfile.Row
	for _, row := range rows {
		switch row.Record {
		case "#summary":
			sums = append(sums, row)
		case "":
			det = append(det, row)
		}
	}
	if len(sums) != 1 {
		return "", 0, fmt.Errorf("the file has %d summary rows, expected one", len(sums))
	}
	sm := sums[0]
	date := anyDate(sm.Get("TradeDate"))
	if date == "" && len(det) > 0 {
		date = anyDate(det[0].Get("TradeDate"))
	}
	if date == "" {
		return "", 0, fmt.Errorf("the file has no trade date")
	}
	var total money.Dec
	for _, d := range det {
		if dd := anyDate(d.Get("TradeDate")); dd != "" && dd != date {
			return "", 0, fmt.Errorf("line %d is for %s, the summary for %s", d.Line, dd, date)
		}
		total = total.Add(decOr0(d.Get("TotalStamp")))
	}
	if want := decOr0(sm.Get("RecordCount")); want.Cmp(money.DecInt(int64(len(det)))) != 0 {
		return "", 0, fmt.Errorf("the summary says %s detail records, the file has %d", want.String(), len(det))
	}
	if want := decOr0(sm.Get("TotalStampDuty")); want.Sub(total).Abs().Cmp(money.MustDec("0.01")) > 0 {
		return "", 0, fmt.Errorf("the summary total stamp duty %s does not equal the details' total %s", want.Fixed(2), total.Fixed(2))
	}
	n, err := o.putCCData(ctx, tenant, actor, "bsestamp", date, "CM", name, det, nil)
	return date, n, err
}

// loadBhavcopy puts a common-format bhavcopy's prices into the day's market data.
func (o *Ops) loadBhavcopy(ctx context.Context, tenant, actor, name string, rows []xfile.Row) (string, string, string, int, error) {
	up := strings.ToUpper(name)
	date, exch, seg := ymdFromName(name), "", ""
	if len(rows) > 0 {
		date = firstOf(anyDate(rows[0].Get("TradDt")), date)
		exch, seg = strings.ToUpper(rows[0].Get("Src")), strings.ToUpper(rows[0].Get("Sgmt"))
	}
	for _, x := range []string{"NSE", "BSE"} {
		if exch == "" && strings.Contains(up, "_"+x+"_") {
			exch = x
		}
	}
	for _, x := range []string{"CM", "FO", "CD"} {
		if seg == "" && strings.Contains(up, "_"+x+"_") {
			seg = x
		}
	}
	if date == "" || exch == "" || seg == "" {
		return "", "", "", 0, fmt.Errorf("the file does not say its date, exchange and segment")
	}
	m, _, err := docs.Get[Market](ctx, o.Docs, tenant, cMarket, date)
	if errors.Is(err, docs.ErrNotFound) {
		// Start from the latest earlier day so its masters and margin
		// rates carry over; this file's prices replace that day's.
		prev, _, _ := o.Market(ctx, tenant, date)
		m = Market{Instruments: append([]Instrument(nil), prev.Instruments...)}
	} else if err != nil {
		return "", "", "", 0, err
	}
	m.Date = date
	at := map[string]int{}
	for i, x := range m.Instruments {
		at[x.Exchange+"|"+x.Segment+"|"+x.Key()] = i
	}
	// BSE scrip code -> ISIN and symbol, from the scrip and series mapping.
	byCode := map[string]SecRef{}
	if exch == "BSE" && seg == "CM" {
		refs, _ := docs.List[SecRef](ctx, o.Docs, tenant, cSecRef, "")
		for _, x := range refs {
			if x.BSECode != "" {
				byCode[x.BSECode] = x
			}
		}
	}
	n := 0
	for _, row := range rows {
		typ := "EQ"
		ft := strings.ToUpper(row.Get("FinInstrmTp"))
		if seg != "CM" {
			switch {
			case ft == "IDF" || ft == "STF" || strings.HasPrefix(ft, "FUT"):
				typ = "FUT"
			case ft == "IDO" || ft == "STO" || strings.HasPrefix(ft, "OPT"):
				typ = strings.ToUpper(row.Get("OptnTp"))
				if typ != "CE" && typ != "PE" {
					continue
				}
			default:
				continue
			}
		}
		sym, isin, code := strings.ToUpper(row.Get("TckrSymb")), strings.ToUpper(row.Get("ISIN")), strings.TrimSpace(row.Get("FinInstrmId"))
		if exch == "BSE" && seg == "CM" && code != "" {
			if ref, ok := byCode[code]; ok {
				isin = firstOf(isin, ref.ISIN)
				sym = firstOf(sym, strings.ToUpper(ref.Symbol))
			} else if len(isin) == 12 {
				// The bhavcopy itself pairs the scrip code with the ISIN.
				ref, _, _ := docs.Get[SecRef](ctx, o.Docs, tenant, cSecRef, isin)
				if ref.BSECode == "" {
					ref.ISIN, ref.BSECode = isin, code
					ref.Name = firstOf(ref.Name, row.Get("FinInstrmNm"))
					_ = docs.Upsert(ctx, o.Docs, tenant, cSecRef, isin, ref, actor)
				}
			}
			sym = firstOf(sym, code)
		}
		if sym == "" {
			continue
		}
		in := Instrument{Exchange: exch, Segment: seg, Symbol: sym, Type: typ, ISIN: isin, Company: row.Get("FinInstrmNm"), Index: ft == "IDF" || ft == "IDO"}
		if typ != "EQ" {
			in.Expiry = anyDate(row.Get("XpryDt"))
			if in.Expiry == "" {
				continue
			}
		}
		if typ == "CE" || typ == "PE" {
			k, err := money.ParseDec(strings.TrimSpace(row.Get("StrkPric")))
			if err != nil || k.IsZero() {
				continue
			}
			in.Strike = k.String()
		}
		cls := decOr0(row.Get("ClsPric"))
		if sp := decOr0(row.Get("SttlmPric")); seg != "CM" && sp.Sign() > 0 {
			cls = sp
		}
		ltp := decOr0(row.Get("LastPric"))
		if ltp.Sign() <= 0 {
			ltp = decOr0(row.Get("ClsPric"))
		}
		if cls.Sign() <= 0 && ltp.Sign() <= 0 {
			continue
		}
		if cls.Sign() <= 0 {
			cls = ltp
		}
		lot, _ := strconv.ParseInt(strings.TrimSpace(strings.Split(row.Get("NewBrdLotQty"), ".")[0]), 10, 64)
		und := decOr0(row.Get("UndrlygPric"))
		k := exch + "|" + seg + "|" + in.Key()
		if i, ok := at[k]; ok {
			x := &m.Instruments[i]
			x.LTP, x.Close = ltp, cls
			if lot > 0 {
				x.Lot = lot
			}
			if x.ISIN == "" {
				x.ISIN = isin
			}
			if und.Sign() > 0 {
				x.Underlying = und
			}
		} else {
			in.LTP, in.Close, in.Lot, in.Underlying = ltp, cls, max(lot, 1), und
			at[k] = len(m.Instruments)
			m.Instruments = append(m.Instruments, in)
		}
		n++
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cMarket, date, m, actor); err != nil {
		return "", "", "", 0, err
	}
	return date, exch, seg, n, nil
}
