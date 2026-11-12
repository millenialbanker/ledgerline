package sharepro

import (
	"context"
	"fmt"
	"strings"

	"ledgerline/internal/migrate"
	"ledgerline/internal/migrate/pkg"
	"ledgerline/internal/money"
)

// History writes one year's read-only history from a company database.
// Every file is streamed: a row is written as it is read.

const (
	qHistJV       = `/*sp:hist-jv*/ SELECT %s FROM JVFILE WHERE CO IN (%s) ORDER BY %s`
	qHistBC       = `/*sp:hist-bc*/ SELECT %s FROM BCFILE WHERE co IN (%s) ORDER BY %s`
	qHistBills    = `/*sp:hist-bills*/ SELECT %s FROM SETTNET WHERE co IN (%s) ORDER BY trdt, co, stno, subcd`
	qHistTrades   = `/*sp:hist-trades*/ SELECT %s FROM TRFILE t %s WHERE t.CO IN (%s) ORDER BY %s`
	qHistCN       = `/*sp:hist-contract-notes*/ SELECT RTRIM(CO) AS co, CNTRNO AS cntrno, TRDT AS trdt, RTRIM(SUBCD) AS subcd FROM TRFILE WHERE CO IN (%s) AND CNTRNO > 0 GROUP BY CO, CNTRNO, TRDT, SUBCD ORDER BY TRDT, CO, CNTRNO`
	qHistDP       = `/*sp:hist-dp*/ SELECT %s FROM DPTRAN d %s WHERE d.CO IN (%s) ORDER BY %s`
	qHistDPBills  = `/*sp:hist-dp-bills*/ SELECT %s FROM INVOICEHEAD WHERE co IN (%s) AND (UPPER(AutoType) LIKE '%%CDSL%%' OR UPPER(AutoType) LIKE '%%NSDL%%' OR UPPER(AutoType) LIKE '%%DPBILL%%') ORDER BY InvoiceDt, InvoiceNo`
	qHistHoldDate = `/*sp:hist-hold-date*/ SELECT MAX(HOLDDATE) AS d FROM HOLD WHERE CO IN (%s) AND HOLDDATE < '%s'`
	qHistHold     = `/*sp:hist-holdings*/ SELECT %s FROM HOLD h %s WHERE h.CO IN (%s) AND h.HOLDDATE >= '%s' AND h.HOLDDATE < '%s' ORDER BY h.BENACNUM, h.ISINCD`
)

type hist struct {
	m     *Mapper
	d     db
	s     *schema
	k     migrate.Kind
	w     *pkg.Writer
	p     migrate.Progress
	f     firmInfo
	rep   *report
	chart *chart
	dp    map[string]dpAcct // co|benacnum -> account
	byUCC map[string]dpAcct // ucc -> first account
	fy    string
}

func (m *Mapper) History(ctx context.Context, q migrate.Querier, k migrate.Kind, plan migrate.Plan, w *pkg.Writer, p migrate.Progress) error {
	if p == nil {
		p = migrate.NopProgress{}
	}
	d := db{q}
	s, err := loadSchema(ctx, d)
	if err != nil {
		return err
	}
	if !isCompany(s) {
		return fmt.Errorf("sharepro: %s is not a company database", q.Database())
	}
	firms := m.firmsFor(ctx, d, s, plan)
	if len(firms) == 0 {
		return errNoFirm
	}
	for _, f := range firms {
		rep := newReport(f.Code, k.FY)
		if n := newest(f.Years); n != "" && !yearsBack(k.FY, n, plan.Years) {
			rep.infof("History", k.FY, "out-of-range", "Year %s is outside the %d years chosen; not migrated", k.FY, max(plan.Years, 8))
			rep.flush(w, p)
			continue
		}
		h := &hist{m: m, d: d, s: s, k: k, w: w, p: p, f: f, rep: rep, fy: k.FY}
		err := h.run(ctx)
		rep.flush(w, p)
		if err != nil {
			return err
		}
	}
	return nil
}

func newest(ys []string) string {
	n := ""
	for _, y := range ys {
		if y > n {
			n = y
		}
	}
	return n
}

func (h *hist) run(ctx context.Context) error {
	var err error
	h.chart, err = loadChart(ctx, h.d, h.s, h.rep)
	if err != nil {
		return err
	}
	h.dp, h.byUCC = map[string]dpAcct{}, map[string]dpAcct{}
	if h.s.has("CLLIST") {
		list, _ := h.s.sel("CLLIST", "", "CO", "DPID", "BENACNUM", "FBENACNUM", "SUBCODE")
		if err := h.d.query(ctx, fmt.Sprintf(qDPAccts, list), func(r migrate.Row) error {
			a := dpAcct{co: str(r, "co"), dpid: upper(r, "dpid"), ben: upper(r, "benacnum"), boid: upper(r, "fbenacnum"), ucc: upper(r, "subcode")}
			if a.boid == "" {
				a.boid = a.dpid + a.ben
			}
			h.dp[a.co+"|"+a.ben] = a
			if _, ok := h.byUCC[a.ucc]; !ok && a.ucc != "" {
				h.byUCC[a.ucc] = a
			}
			return nil
		}); err != nil {
			return err
		}
	}
	steps := []struct {
		name, kind, table string
		fn                func(context.Context, *pkg.CSV) error
	}{
		{"Ledger", pkg.HistoryLedger, "JVFILE", h.ledger},
		{"Bills", pkg.HistoryBills, "SETTNET", h.bills},
		{"Trades", pkg.HistoryTrades, "TRFILE", h.trades},
		{"Contract notes", pkg.HistoryContractNotes, "TRFILE", h.contractNotes},
		{"Receipts and payments", pkg.HistoryReceipts, "BCFILE", h.receipts},
		{"DP transactions", pkg.HistoryDP, "DPTRAN", h.dpTxns},
		{"DP bills", pkg.HistoryDPBills, "INVOICEHEAD", h.dpBills},
		{"DP holdings at year end", pkg.HistoryHoldings, "HOLD", h.holdings},
	}
	for _, st := range steps {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !h.s.has(st.table) {
			h.rep.infof("History", st.kind, "missing-table", "This year's database has no %s table; %s is not written for %s", st.table, st.kind, h.fy)
			continue
		}
		if (st.kind == pkg.HistoryDP || st.kind == pkg.HistoryHoldings || st.kind == pkg.HistoryDPBills) && len(h.f.dpCOs()) == 0 && st.kind != pkg.HistoryDPBills {
			continue
		}
		h.p.Step(st.name + " " + h.fy)
		cw, err := h.w.History(h.f.Code, h.fy, st.kind, pkg.HistoryColumns[st.kind])
		if err != nil {
			return err
		}
		if err := st.fn(ctx, cw); err != nil {
			cw.Close()
			return fmt.Errorf("%s %s: %w", st.name, h.fy, err)
		}
		if err := cw.Close(); err != nil {
			return err
		}
	}
	h.chart.reportUnknown(h.rep, "History ledger")
	return nil
}

// row writes values by HistoryColumns name.
func hrow(cw *pkg.CSV, kind string, v map[string]string) error {
	return cw.Write(rec(pkg.HistoryColumns[kind], v))
}

func (h *hist) segName(co string) (exch, seg string) {
	return h.f.exchOf(co), h.f.segOf(co)
}

func (h *hist) ledger(ctx context.Context, cw *pkg.CSV) error {
	cos := inList(h.f.COs)
	var td, tc money.Dec
	n := 0
	write := func(v map[string]string, dr, cr money.Dec) error {
		td, tc = td.Add(dr), tc.Add(cr)
		v["Debit"], v["Credit"] = rupees(dr), rupees(cr)
		n++
		if n%5000 == 0 {
			h.p.Count(n, 0)
		}
		return hrow(cw, pkg.HistoryLedger, v)
	}
	list, _ := h.s.sel("JVFILE", "", "CO", "TYPE", "TT", "VRNO", "VRDT", "ACCD", "SUBCODE", "NARR", "AMTDR", "AMTCR", "IDN")
	if err := h.d.query(ctx, fmt.Sprintf(qHistJV, list, cos, h.s.order("JVFILE", "", "VRDT", "TT", "VRNO", "IDN")), func(r migrate.Row) error {
		co := str(r, "co")
		acct, name, ucc := h.chart.account(co, str(r, "accd"), str(r, "subcode"))
		vt := "Journal"
		if t := str(r, "type"); t != "" {
			vt += " " + t
		}
		_, seg := h.segName(co)
		return write(map[string]string{"Date": date(r, "vrdt"), "VoucherType": vt, "VoucherNo": str(r, "tt") + "/" + str(r, "vrno"), "Account": acct, "AccountName": name,
			"UCC": ucc, "Segment": seg, "Narration": oneLine(str(r, "narr")), "SourceRef": fmt.Sprintf("JVFILE:%s:%s/%s:%s", co, str(r, "tt"), str(r, "vrno"), str(r, "idn"))},
			dec(r, "amtdr"), dec(r, "amtcr"))
	}); err != nil {
		return err
	}
	if h.s.has("BCFILE") {
		noBook := map[string]bool{}
		list, _ := h.s.sel("BCFILE", "", "co", "TYPE", "TT", "P_R", "VRNO", "VRDT", "ACCD", "SUBCODE", "NARR", "AMTDR", "AMTCR", "CHEQNO", "VALUEDT", "IDN")
		if err := h.d.query(ctx, fmt.Sprintf(qHistBC, list, cos, h.s.order("BCFILE", "", "VRDT", "TT", "VRNO", "IDN")), func(r migrate.Row) error {
			co, tt := str(r, "co"), upper(r, "tt")
			acct, name, ucc := h.chart.account(co, str(r, "accd"), str(r, "subcode"))
			_, seg := h.segName(co)
			vt := bcType(upper(r, "type"), upper(r, "p_r"))
			ref := fmt.Sprintf("BCFILE:%s:%s/%s:%s", co, tt, str(r, "vrno"), str(r, "idn"))
			base := map[string]string{"Date": date(r, "vrdt"), "VoucherType": vt, "VoucherNo": tt + "/" + str(r, "vrno"), "Segment": seg,
				"Narration": oneLine(str(r, "narr")), "ChequeNo": str(r, "cheqno"), "ValueDate": date(r, "valuedt")}
			party := map[string]string{"Account": acct, "AccountName": name, "UCC": ucc, "SourceRef": ref}
			for k, v := range base {
				party[k] = v
			}
			dr, cr := dec(r, "amtdr"), dec(r, "amtcr")
			if err := write(party, dr, cr); err != nil {
				return err
			}
			// the bank or cash book's own side, which SharePro does not store as a line
			bh := h.chart.bookHead[co+"|"+tt]
			if bh == "" {
				noBook[co+"|"+tt] = true
				return nil
			}
			if strings.EqualFold(bh, str(r, "accd")) {
				return nil
			}
			bacct, bname, _ := h.chart.account(co, bh, "")
			side := map[string]string{"Account": bacct, "AccountName": bname, "SourceRef": ref + ":book"}
			for k, v := range base {
				side[k] = v
			}
			return write(side, cr, dr)
		}); err != nil {
			return err
		}
		for _, k := range sortedKeys(noBook) {
			h.rep.warnf("History ledger", k, "no-book", "The bank side of these vouchers is missing from the history; it is in the old system's bank book", "Bank or cash book %s has no ledger head in MSFILE (TT); its vouchers' bank side is not written", k)
		}
	}
	if td.Cmp(tc) != 0 {
		h.rep.warnf("History ledger", h.fy, "unbalanced", "Compare with SharePro's day book for the year", "The year's ledger lines do not balance: debits %s, credits %s", td.Fixed(2), tc.Fixed(2))
	}
	return nil
}

func bcType(typ, pr string) string {
	switch {
	case pr == "C" || typ == "C" && pr == "":
		return "Contra"
	case typ == "C" && pr == "R":
		return "Cash receipt"
	case typ == "C" && pr == "P":
		return "Cash payment"
	case pr == "R":
		return "Receipt"
	case pr == "P":
		return "Payment"
	}
	return "Bank"
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func (h *hist) bills(ctx context.Context, cw *pkg.CSV) error {
	list, _ := h.s.sel("SETTNET", "", "co", "stno", "sttype", "trdt", "subcd", "cntrno", "Turnover", "Brokerage", "STTax", "Trancharge", "Clrcharge", "SebiChg",
		"BNoteStamp", "CGST", "SGST", "IGST", "UTGST", "ServiceTax", "DematCh", "DeliveryCh", "Penalty", "AddlBrkg", "RmsCharge", "IPFT", "OthCharge", "NetRecPay", "npk")
	return h.d.query(ctx, fmt.Sprintf(qHistBills, list, inList(h.f.COs)), func(r migrate.Row) error {
		co := str(r, "co")
		exch, seg := h.segName(co)
		sum := func(cols ...string) money.Dec {
			t := money.Dec{}
			for _, c := range cols {
				t = t.Add(dec(r, c))
			}
			return t
		}
		billNo := str(r, "cntrno")
		if billNo == "" || billNo == "0" {
			billNo = str(r, "stno") + str(r, "sttype") + "/" + upper(r, "subcd")
		}
		return hrow(cw, pkg.HistoryBills, map[string]string{"BillNo": co + "-" + billNo, "BillDate": date(r, "trdt"), "Exchange": exch, "Segment": seg,
			"Settlement": str(r, "stno") + str(r, "sttype"), "UCC": upper(r, "subcd"), "Turnover": rupees(dec(r, "turnover")), "Brokerage": rupees(dec(r, "brokerage")),
			"STT": rupees(dec(r, "sttax")), "ExchangeCharges": rupees(sum("trancharge", "clrcharge")), "SEBIFees": rupees(dec(r, "sebichg")),
			"StampDuty": rupees(dec(r, "bnotestamp")), "GST": rupees(sum("cgst", "sgst", "igst", "utgst", "servicetax")),
			"OtherCharges": rupees(sum("dematch", "deliverych", "penalty", "addlbrkg", "rmscharge", "ipft", "othcharge")), "NetAmount": rupees0(dec(r, "netrecpay")),
			"SourceRef": fmt.Sprintf("SETTNET:%s:%s", co, str(r, "npk"))})
	})
}

// tradeApply joins the item master and ISIN to trades (alias t).
func (h *hist) tradeApply() (cols, apply string) {
	cols, apply = "NULL AS instr_type, NULL AS exp_date, NULL AS cont_rate, NULL AS nse_itemcd, NULL AS nse_series, NULL AS descr, NULL AS cont_type, NULL AS assetcd, NULL AS mcx_itemcd", ""
	if h.s.has("ITC") {
		il, _ := h.s.sel("ITC", "ITC", "INSTR_TYPE", "EXP_DATE", "CONT_RATE", "NSE_ITEMCD", "NSE_SERIES", "MCX_ITEMCD", "DESCR")
		apply = " OUTER APPLY (SELECT TOP 1 " + il + " FROM ITC WHERE ITC.ITEMCD = t.ITEMCD) i"
		cols = "i.instr_type, i.exp_date, i.cont_rate, i.nse_itemcd, i.nse_series, i.mcx_itemcd, i.descr, NULL AS cont_type, NULL AS assetcd"
		if h.s.has("ICCLCONT") {
			kl, _ := h.s.sel("ICCLCONT", "ICCLCONT", "Cont_type", "AssetCd")
			apply += " OUTER APPLY (SELECT TOP 1 " + kl + " FROM ICCLCONT WHERE ICCLCONT.Nse_itemcd = t.ITEMCD OR ICCLCONT.Bse_itemcd = t.ITEMCD) k"
			cols = strings.Replace(cols, "NULL AS cont_type, NULL AS assetcd", "k.cont_type, k.assetcd", 1)
		}
	}
	if h.s.has("ISINMAST") {
		apply += " OUTER APPLY (SELECT TOP 1 RTRIM(ISINCD) AS isin FROM ISINMAST WHERE ISINMAST.ITEMCD = t.ITEMCD) x"
		cols += ", x.isin"
	} else {
		cols += ", NULL AS isin"
	}
	return cols, apply
}

func (h *hist) trades(ctx context.Context, cw *pkg.CSV) error {
	tl, _ := h.s.sel("TRFILE", "t", "CO", "STNO", "STTYPE", "TRDT", "SUBCD", "TRNO", "cTRDNO", "TRDNO", "ORDNO", "ITEMCD", "QTY", "ACT_RATE", "RATE", "BRKG", "CNTRNO", "NPK")
	cols, apply := h.tradeApply()
	return h.d.query(ctx, fmt.Sprintf(qHistTrades, tl+", "+cols, apply, inList(h.f.COs), h.s.order("TRFILE", "t", "TRDT", "CO", "NPK")), func(r migrate.Row) error {
		co := str(r, "co")
		exch, seg := h.segName(co)
		q := dec(r, "qty")
		side := "B"
		if q.Sign() < 0 {
			side = "S"
		}
		price := dec(r, "act_rate")
		if price.Sign() == 0 {
			price = dec(r, "rate")
		}
		typ, sym := instrument(r)
		opt := ""
		if typ == "CE" || typ == "PE" {
			opt = typ
		}
		strike := ""
		if opt != "" {
			strike = num(dec(r, "cont_rate"))
		}
		expiry := ""
		if typ != "" {
			expiry = date(r, "exp_date")
		}
		if seg == "CM" || seg == "" {
			sym = firstNonEmpty(upper(r, "nse_itemcd"), upper(r, "itemcd"))
		}
		return hrow(cw, pkg.HistoryTrades, map[string]string{"TradeDate": date(r, "trdt"), "Exchange": exch, "Segment": seg, "Settlement": str(r, "stno") + str(r, "sttype"),
			"UCC": upper(r, "subcd"), "TradeNo": firstNonEmpty(str(r, "ctrdno"), zeroBlank(str(r, "trdno")), str(r, "trno")), "OrderNo": str(r, "ordno"), "Symbol": sym,
			"Series": upper(r, "nse_series"), "ISIN": upper(r, "isin"), "Instrument": upper(r, "instr_type"), "Expiry": expiry, "Strike": strike, "OptionType": opt,
			"Side": side, "Quantity": q.Abs().String(), "Price": num(price), "Brokerage": rupees(dec(r, "brkg").Mul(q.Abs())), "BillNo": zeroBlank(str(r, "cntrno")),
			"SourceRef": fmt.Sprintf("TRFILE:%s:%s", co, str(r, "npk"))})
	})
}

func zeroBlank(s string) string {
	if strings.Trim(s, "0.") == "" {
		return ""
	}
	return s
}

func (h *hist) contractNotes(ctx context.Context, cw *pkg.CSV) error {
	if !h.s.hasCol("TRFILE", "CNTRNO") {
		h.rep.infof("History", pkg.HistoryContractNotes, "no-cntrno", "TRFILE has no contract note numbers in %s", h.fy)
		return nil
	}
	return h.d.query(ctx, fmt.Sprintf(qHistCN, inList(h.f.COs)), func(r migrate.Row) error {
		co := str(r, "co")
		exch, seg := h.segName(co)
		// SentOn, SentTo, Signed and DocumentID come from the mail archive,
		// which is read after the company databases: Ledgerline links the
		// archived notes by contract note number and client.
		return hrow(cw, pkg.HistoryContractNotes, map[string]string{"CNNo": str(r, "cntrno"), "Date": date(r, "trdt"), "UCC": upper(r, "subcd"), "Exchange": exch, "Segment": seg,
			"SourceRef": fmt.Sprintf("TRFILE:%s:CNTRNO=%s", co, str(r, "cntrno"))})
	})
}

func (h *hist) receipts(ctx context.Context, cw *pkg.CSV) error {
	list, _ := h.s.sel("BCFILE", "", "co", "TYPE", "TT", "P_R", "VRNO", "VRDT", "ACCD", "SUBCODE", "NARR", "AMTDR", "AMTCR", "CHEQNO", "BankRef", "TXNNO", "EcsRef", "Gateway", "IDN")
	return h.d.query(ctx, fmt.Sprintf(qHistBC, list, inList(h.f.COs), h.s.order("BCFILE", "", "VRDT", "TT", "VRNO", "IDN")), func(r migrate.Row) error {
		pr := upper(r, "p_r")
		if pr != "R" && pr != "P" {
			return nil
		}
		co, tt := str(r, "co"), upper(r, "tt")
		_, _, ucc := h.chart.account(co, str(r, "accd"), str(r, "subcode"))
		typ, amt := "Receipt", dec(r, "amtcr").Sub(dec(r, "amtdr"))
		if pr == "P" {
			typ, amt = "Payment", dec(r, "amtdr").Sub(dec(r, "amtcr"))
		}
		mode := "Bank transfer"
		switch {
		case upper(r, "type") == "C":
			mode = "Cash"
		case str(r, "cheqno") != "":
			mode = "Cheque"
		case str(r, "gateway") != "":
			mode = "Online (" + str(r, "gateway") + ")"
		}
		bank := ""
		if bh := h.chart.bookHead[co+"|"+tt]; bh != "" {
			_, bank, _ = h.chart.account(co, bh, "")
		}
		return hrow(cw, pkg.HistoryReceipts, map[string]string{"Date": date(r, "vrdt"), "Type": typ, "VoucherNo": tt + "/" + str(r, "vrno"), "UCC": ucc, "Amount": rupees0(amt),
			"Mode": mode, "Bank": bank, "ChequeNo": str(r, "cheqno"), "Reference": firstNonEmpty(str(r, "bankref"), str(r, "txnno"), str(r, "ecsref")),
			"Narration": oneLine(str(r, "narr")), "SourceRef": fmt.Sprintf("BCFILE:%s:%s/%s:%s", co, tt, str(r, "vrno"), str(r, "idn"))})
	})
}

func (h *hist) isinApply(alias string) (col, apply string) {
	if !h.s.has("ISINLIST") {
		return "NULL AS security", ""
	}
	return "s.security", " OUTER APPLY (SELECT TOP 1 RTRIM(ISINSHORT) AS security FROM ISINLIST WHERE ISINLIST.ISINCD = " + alias + ".ISINCD) s"
}

func (h *hist) dpTxns(ctx context.Context, cw *pkg.CSV) error {
	dpc := h.f.dpCOs()
	list, _ := h.s.sel("DPTRAN", "d", "CO", "TRDT", "BENACNUM", "FBENACNUM", "ISINCD", "DRCR", "QTYBOOKED", "TRANCODE", "TRANDESCR", "TXNID", "DRFNO", "INTERNALRF", "COUNTERDP", "COUNTERBEN", "CNTRCMBPID", "NPK")
	sc, apply := h.isinApply("d")
	return h.d.query(ctx, fmt.Sprintf(qHistDP, list+", "+sc, apply, inList(dpc), h.s.order("DPTRAN", "d", "TRDT", "CO", "NPK")), func(r migrate.Row) error {
		co, ben := str(r, "co"), upper(r, "benacnum")
		a := h.dp[co+"|"+ben]
		boid := firstNonEmpty(upper(r, "fbenacnum"), a.boid, ben)
		q := dec(r, "qtybooked").Abs()
		if upper(r, "drcr") == "D" {
			q = q.Neg()
		}
		cp := strings.TrimSpace(str(r, "counterdp") + str(r, "counterben"))
		return hrow(cw, pkg.HistoryDP, map[string]string{"Date": date(r, "trdt"), "BOID": boid, "UCC": a.ucc, "ISIN": upper(r, "isincd"), "Security": str(r, "security"),
			"TxnType": firstNonEmpty(str(r, "trancode"), str(r, "drcr")), "Quantity": q.String(), "Reference": firstNonEmpty(str(r, "txnid"), str(r, "drfno"), str(r, "internalrf")),
			"Counterparty": firstNonEmpty(cp, str(r, "cntrcmbpid")), "Narration": oneLine(str(r, "trandescr")), "SourceRef": fmt.Sprintf("DPTRAN:%s:%s", co, str(r, "npk"))})
	})
}

func (h *hist) dpBills(ctx context.Context, cw *pkg.CSV) error {
	list, _ := h.s.sel("INVOICEHEAD", "", "co", "VrNo", "InvoiceNo", "InvoiceDt", "SubCode", "GrossAmt", "CGST", "SGST", "IGST", "UTGST", "NetAmt", "AutoType")
	if !h.s.hasCol("INVOICEHEAD", "AutoType") {
		h.rep.infof("History", pkg.HistoryDPBills, "no-autotype", "INVOICEHEAD has no AutoType in %s; DP bills cannot be told apart", h.fy)
		return nil
	}
	return h.d.query(ctx, fmt.Sprintf(qHistDPBills, list, inList(h.f.COs)), func(r migrate.Row) error {
		co, sub := str(r, "co"), upper(r, "subcode")
		a, ok := h.dp[co+"|"+sub]
		if !ok {
			a = h.byUCC[sub]
		}
		ucc := firstNonEmpty(a.ucc, sub)
		gst := dec(r, "cgst").Add(dec(r, "sgst")).Add(dec(r, "igst")).Add(dec(r, "utgst"))
		return hrow(cw, pkg.HistoryDPBills, map[string]string{"BillNo": str(r, "invoiceno"), "BillDate": date(r, "invoicedt"), "BOID": a.boid, "UCC": ucc,
			"Amount": rupees0(dec(r, "grossamt")), "GST": rupees0(gst), "Total": rupees0(dec(r, "netamt")), "SourceRef": fmt.Sprintf("INVOICEHEAD:%s:%s", co, str(r, "vrno"))})
	})
}

func (h *hist) holdings(ctx context.Context, cw *pkg.CSV) error {
	dpc := h.f.dpCOs()
	end := fyEnd(h.fy)
	r, err := h.d.one(ctx, fmt.Sprintf(qHistHoldDate, inList(dpc), day(end.AddDate(0, 0, 1))))
	if err != nil {
		return err
	}
	hd := tm(r, "d")
	if hd.IsZero() {
		h.rep.infof("History", pkg.HistoryHoldings, "no-holdings", "No DP holdings in %s", h.fy)
		return nil
	}
	if day(hd) != day(end) {
		h.rep.infof("History", pkg.HistoryHoldings, "hold-date", "The year's last DP holdings in SharePro are of %s", day(hd))
	}
	list, _ := h.s.sel("HOLD", "h", "CO", "BENACNUM", "ISINCD", "BALANCE", "CURBAL", "PLDGBAL", "LOCINBAL", "FRZBAL", "RATE")
	sc, apply := h.isinApply("h")
	return h.d.query(ctx, fmt.Sprintf(qHistHold, list+", "+sc, apply, inList(dpc), day(hd), day(hd.AddDate(0, 0, 1))), func(r migrate.Row) error {
		a := h.dp[str(r, "co")+"|"+upper(r, "benacnum")]
		tot, rate := dec(r, "balance"), dec(r, "rate")
		return hrow(cw, pkg.HistoryHoldings, map[string]string{"Date": day(hd), "BOID": firstNonEmpty(a.boid, upper(r, "benacnum")), "UCC": a.ucc, "ISIN": upper(r, "isincd"),
			"Security": str(r, "security"), "Free": qty(dec(r, "curbal")), "Pledged": qty(dec(r, "pldgbal")), "Locked": qty(dec(r, "locinbal").Add(dec(r, "frzbal"))),
			"Total": qty(tot), "Rate": num(rate), "Value": rupees(tot.Mul(rate))})
	})
}
