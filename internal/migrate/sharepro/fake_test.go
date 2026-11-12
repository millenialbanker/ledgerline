package sharepro

import (
	"context"
	"regexp"
	"strings"
	"sync"
	"time"

	"ledgerline/internal/migrate"
)

// fake is a Querier that answers by the tag (/*sp:name*/) at the start of
// each query, with made-up rows.
type fake struct {
	name    string
	tables  map[string][]string
	rows    map[string]func(q string) []migrate.Row
	stream  map[string]func(q string, fn func(migrate.Row) error) error
	mu      sync.Mutex
	queries []string
}

var reTag = regexp.MustCompile(`/\*sp:([a-z0-9-]+)\*/`)

func newFake(name string) *fake {
	return &fake{name: name, tables: map[string][]string{}, rows: map[string]func(string) []migrate.Row{}, stream: map[string]func(string, func(migrate.Row) error) error{}}
}

func (f *fake) Database() string { return f.name }

func (f *fake) table(name, cols string) {
	f.tables[name] = strings.Split(cols, ",")
}

func (f *fake) on(tag string, rows ...migrate.Row) {
	f.rows[tag] = func(string) []migrate.Row { return rows }
}

func (f *fake) Query(ctx context.Context, q string, fn func(migrate.Row) error, args ...any) error {
	f.mu.Lock()
	f.queries = append(f.queries, q)
	f.mu.Unlock()
	tag := ""
	if m := reTag.FindStringSubmatch(q); m != nil {
		tag = m[1]
	}
	if tag == "schema" {
		for t, cs := range f.tables {
			for _, c := range cs {
				if err := fn(migrate.Row{"t": t, "c": strings.TrimSpace(c)}); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if s := f.stream[tag]; s != nil {
		return s(q, fn)
	}
	if h := f.rows[tag]; h != nil {
		for _, r := range h(q) {
			if err := fn(r); err != nil {
				return err
			}
		}
	}
	return nil
}

func (f *fake) asked(tag string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, q := range f.queries {
		if strings.Contains(q, "/*sp:"+tag+"*/") {
			out = append(out, q)
		}
	}
	return out
}

func d(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

// ---- the synthetic broker ----
//
// Firm "TT SECURITIES PVT LTD": CO 02 NSE cash, 03 NSE F&O, 06 CDSL DP.
// Firm "TT METALS PVT LTD": CO 05 MCX.
// Clients (made up): TEST001 Asha Test (active, in debit), TEST002 Bina
// Test (dormant, nothing owed), TEST003 Chetan Test (closed, in credit),
// TEST004 Dev Test (active, bad PAN and IFSC).

func adminFake() *fake {
	f := newFake("TTADMIN")
	f.table("COLIST", "CO,DESCR,SEBI_REG,Inactive,CDSL_BEN,NSDL_BEN,DPID,Web_Note")
	f.table("ACC_YEAR", "CO,ACC_YEAR,DETACHED")
	f.table("SSMAIL", "slno,id,date")
	f.table("SSMAIL_ATTACHMENT", "slno,attnumber,contents,Signed")
	f.table("SHUSER", "USERID,PASSWORD")
	f.on("admin-colist",
		migrate.Row{"co": "02", "descr": "TT SECURITIES PVT. LTD. (NSE)", "sebi_reg": "INZ000000001", "inactive": false},
		migrate.Row{"co": "03", "descr": "TT SECURITIES PVT. LTD. (NFO)", "sebi_reg": "INZ000000001", "inactive": false},
		migrate.Row{"co": "05", "descr": "TT METALS PVT. LTD. (MCX)", "sebi_reg": "INZ000000002", "inactive": false},
		migrate.Row{"co": "06", "descr": "TT SECURITIES PVT LTD (CDSL)", "sebi_reg": "IN-DP-0-0000", "inactive": false, "cdsl_ben": true},
	)
	var ys []migrate.Row
	for _, co := range []string{"02", "03", "05", "06"} {
		ys = append(ys, migrate.Row{"co": co, "acc_year": "2526"}, migrate.Row{"co": co, "acc_year": "2627"})
	}
	f.on("admin-acc-year", ys...)
	f.on("admin-years", migrate.Row{"first": "2526", "last": "2627"})
	return f
}

func companyFake(name string) *fake {
	f := newFake(name)
	f.table("DLMAST", "SUBCD,DESCR,CLIENTTYPE,PAN_NO,BIRTH_DATE,MOBILE,EMAIL1,CITY,RES_CITY,STATE,STATECD,COUNTRY,ADD1,PIN,BranchID,SBCODE,RMCODE,GROUPCD,FAMILYCD,ClRiskProfile,AnnIncome,BANK,BANKACNUM,MICRNO,BANKACTYPE,NomineeNm,NomineeRel,COMMNCEDT,ADD_DTTIME,CloseOutDt,eContract,KYCStatus,TaxStatus,CLIENTCAT,GENDER,OCCUPATION,CUST_UCC,NoInterest,REG_NSE,REG_BSE,REG_NFO,REG_MCX,ACC_ACCD,ACC_SUBCOD,Web_Pwd")
	f.table("JVFILE", "CO,TYPE,TT,VRNO,VRDT,ACCD,SUBCODE,NARR,AMTDR,AMTCR,IDN")
	f.table("BCFILE", "co,TYPE,TT,P_R,VRNO,VRDT,ACCD,SUBCODE,NARR,AMTDR,AMTCR,CHEQNO,VALUEDT,BankRef,TXNNO,EcsRef,Gateway,IDN")
	f.table("ACCTOT", "co,branchid,accd,subcode,vrdt,amtdr,amtcr")
	f.table("ACCTGROUP", "AgrCode,PAgrCode,Descr")
	f.table("MASTMSFILE", "ACCD,DESCR,AgrCode,ACCTTYPE,BANKCD,TT,SUBLEDGER")
	f.table("MSFILE", "co,ACCD,DESCR,TT,SUBLEDGER")
	f.table("MLFILE", "co,ACCD,SUBCODE,DESCR")
	f.table("MSACTYPES", "accttype,lExchange")
	f.table("DLMAST_REG2", "co,subcd,status,effectdate,ts")
	f.table("DLMAST_NOMINEE", "SUBCD,NOMINEENO,DESCR,PERCENT,RELATION,IDTYPE,IDNUMBER,ISMINOR,MINORDOB,GDDESCR,GDRELATION,MOBILE,EMAIL,ADD1,CITY,PINCODE,Date,WithDt")
	f.table("BKMAST", "SUBCD,ACNO,ACTYPE,Closed,BANKCD,ADD_DTTIME")
	f.table("BANKMAST", "BANKCD,DESCR,RTGS,MICRNO,BRANCH")
	f.table("CLLIST", "CO,DPID,BENACNUM,FBENACNUM,SUBCODE,BENSTAT,BENCLOSEDT,ACTIVEDT,Web_Pwd,FHOLDER_ADHAR")
	f.table("HOLD", "CO,BENACNUM,ISINCD,HOLDDATE,BALANCE,CURBAL,PLDGBAL,LOCINBAL,FRZBAL,RATE")
	f.table("TRFILE", "NPK,CO,STNO,STTYPE,TRDT,SUBCD,TRNO,cTRDNO,TRDNO,ORDNO,ITEMCD,QTY,ACT_RATE,RATE,BRKG,CNTRNO,Segment")
	f.table("ITC", "ITEMCD,INSTR_TYPE,EXP_DATE,CONT_RATE,NSE_ITEMCD,NSE_SERIES,BSE_ITEMCD,MCX_ITEMCD,DESCR")
	f.table("ICCLCONT", "Nse_itemcd,Bse_itemcd,Cont_type,AssetCd")
	f.table("ISINMAST", "ITEMCD,ISINCD")
	f.table("ISINLIST", "ISINCD,ISINSHORT")
	f.table("NSQBRKG", "SUBCD,EffectDate,ITEMCD,UNDITEMCD,CO,PRODUCTCD,ATWHAT,SQBRPTGE,SQBRMNRT,SQBRMNPER,OF,SQBRFLRT,SQFLATPER,SLABCD")
	f.table("NSDBRKG", "SUBCD,EffectDate,ITEMCD,UNDITEMCD,CO,PRODUCTCD,ATWHAT,ONWHICH,DLBRPTGE,DLBRKGMIN,DLBRMNPER,DLBRKGFLT,DLFLATPER,DLSLABCD")
	f.table("OTHBRKG", "SUBCD,CO,EffectDate,ORD_MINBRK,CNT_MINBRK,MINLOTBRKG,DEFA_BRKG,brkgbasket")
	f.table("INTERESTDTL", "Subcd,EffectDate,InterestPer,NoInterest,cGraceDays,SegmentType")
	f.table("KYCMAST", "KYCCD,KYCNO,STATUS,DT_ACTIVE,COMMNCEDT")
	f.table("KYCDTL", "KYCCD,EFFECTDATE,PANNO")
	f.table("SETTNET", "npk,co,stno,sttype,trdt,subcd,cntrno,Turnover,Brokerage,STTax,Trancharge,Clrcharge,SebiChg,BNoteStamp,CGST,SGST,IGST,UTGST,DematCh,NetRecPay")
	f.table("DPTRAN", "NPK,CO,TRDT,BENACNUM,FBENACNUM,ISINCD,DRCR,QTYBOOKED,TRANCODE,TRANDESCR,TXNID")
	f.table("INVOICEHEAD", "co,VrNo,InvoiceNo,InvoiceDt,SubCode,GrossAmt,CGST,SGST,IGST,UTGST,NetAmt,AutoType")
	f.table("SYSTEMLOGFILES", "Id,Content")

	f.on("jv-cos", migrate.Row{"co": "02"}, migrate.Row{"co": "03"})
	f.on("latest-date", migrate.Row{"jv": d("2026-10-12"), "tr": d("2026-10-09")})
	f.on("company-cos",
		migrate.Row{"co": "02", "segment": "NSE", "n": int64(100)},
		migrate.Row{"co": "03", "segment": "NFO", "n": int64(10)},
		migrate.Row{"co": "05", "segment": "MCFO", "n": int64(3)})
	client := func(u, name, pan, mobile, email string, extra migrate.Row) migrate.Row {
		r := migrate.Row{"subcd": u, "descr": name, "clienttype": "Normal", "pan_no": pan, "birth_date": d("1990-05-01"), "mobile": mobile, "email1": email,
			"city": "KOLKATA", "statecd": "WB", "add1": "1 Test Lane", "pin": "700001", "branchid": "HO", "sbcode": "SB01", "rmcode": "", "groupcd": "",
			"clriskprofile": "LOW", "annincome": "1-5 LAC", "bank": "TEST BANK", "bankacnum": "123456789012", "micrno": "700000001", "bankactype": "SB",
			"commncedt": d("2020-01-01"), "add_dttime": d("2020-01-01"), "closeoutdt": d("1900-01-01"), "econtract": true, "kycstatus": "Y", "country": "INDIA",
			"reg_nse": true, "reg_bse": true, "reg_nfo": false, "reg_mcx": false, "acc_accd": "62", "acc_subcod": "", "nointerest": false, "gender": "F"}
		for k, v := range extra {
			r[k] = v
		}
		return r
	}
	f.on("clients",
		client("TEST001", "Asha Test", "AAAPT0001A", "+91 9800000001", "ASHA@EXAMPLE.COM", migrate.Row{"reg_nfo": true}),
		client("TEST002", "Bina Test", "AAAPT0002B", "9800000002", "bina@example.com", nil),
		client("TEST003", "Chetan Test", "AAAPT0003C", "9800000003", "chetan@example.com", migrate.Row{"closeoutdt": d("2024-01-01")}),
		client("TEST004", "Dev Test", "BADPAN", "12345", "dev@example", migrate.Row{"nointerest": true}),
	)
	f.on("client-accounts",
		migrate.Row{"subcd": "TEST001", "acc_accd": "62"}, migrate.Row{"subcd": "TEST002", "acc_accd": "62"},
		migrate.Row{"subcd": "TEST003", "acc_accd": "62"}, migrate.Row{"subcd": "TEST004", "acc_accd": "62"})
	f.on("kyc", migrate.Row{"kyccd": "K1", "kycno": "12345678901234", "status": "UPLOADED", "dt_active": d("2020-02-01"), "panno": "AAAPT0001A"})
	f.on("client-banks",
		migrate.Row{"subcd": "TEST001", "acno": "123456789012", "actype": "SB", "closed": false, "bank": "TEST BANK", "ifsc": "TEST0000001", "micr": "700000001", "branch": "MAIN"},
		migrate.Row{"subcd": "TEST001", "acno": "999988887777", "actype": "CA", "closed": true, "bank": "OTHER BANK", "ifsc": "OTHR0000002"},
		migrate.Row{"subcd": "TEST004", "acno": "12AB", "actype": "SB", "closed": false, "bank": "TEST BANK", "ifsc": "BAD"},
	)
	f.on("nominees", migrate.Row{"subcd": "TEST001", "nomineeno": "1", "descr": "Ravi Test", "percent": "100.00", "relation": "Brother", "idtype": "AADHAAR", "id": "4321", "date": d("2021-03-01")})
	f.on("segment-status",
		migrate.Row{"co": "02", "subcd": "TEST001", "status": "ACTIVE", "effectdate": d("2021-01-01")},
		migrate.Row{"co": "03", "subcd": "TEST001", "status": "ACTIVE", "effectdate": d("2021-01-01")},
		migrate.Row{"co": "02", "subcd": "TEST002", "status": "ACTIVE", "effectdate": d("2020-01-01")},
		migrate.Row{"co": "02", "subcd": "TEST002", "status": "DORMANT", "effectdate": d("2025-06-01")},
		migrate.Row{"co": "02", "subcd": "TEST003", "status": "ACTIVE", "effectdate": d("2019-01-01")},
		migrate.Row{"co": "02", "subcd": "TEST003", "status": "CLOSED", "effectdate": d("2024-01-01")},
		migrate.Row{"co": "02", "subcd": "TEST004", "status": "ACTIVE", "effectdate": d("2022-01-01")},
		migrate.Row{"co": "02", "subcd": "TEST004", "status": "WEIRD", "effectdate": d("2022-02-01")},
	)
	f.on("dp-accounts",
		migrate.Row{"co": "06", "dpid": "12345678", "benacnum": "00000001", "fbenacnum": "1234567800000001", "subcode": "TEST001", "activedt": d("2020-01-01"), "benclosedt": d("1900-01-01")},
		migrate.Row{"co": "06", "dpid": "12345678", "benacnum": "00000009", "fbenacnum": "1234567800000009", "subcode": "", "benclosedt": d("1900-01-01")},
	)
	f.on("last-trade", migrate.Row{"subcd": "TEST001", "last": d("2026-10-02")})
	f.on("acctgroup",
		migrate.Row{"code": "1LIABILITI", "parent": "", "descr": "SOURCES OF FUNDS"},
		migrate.Row{"code": "CAPITAL", "parent": "1LIABILITI", "descr": "SHARES HOLDER FUNDS"},
		migrate.Row{"code": "2ASSETS", "parent": "", "descr": "APPLICATION OF FUNDS"},
		migrate.Row{"code": "CASSETS", "parent": "2ASSETS", "descr": "CURRENT ASSETS"},
		migrate.Row{"code": "SDEBTORS", "parent": "CASSETS", "descr": "SUNDRY DEBTORS"},
		migrate.Row{"code": "CASHINHAND", "parent": "CASSETS", "descr": "CASH AND BANK BALANCE"},
		migrate.Row{"code": "3INCOME", "parent": "", "descr": "INCOME"},
	)
	f.on("msactypes", migrate.Row{"accttype": "EXCH", "lexchange": true})
	f.on("mastmsfile",
		migrate.Row{"accd": "62", "descr": "CLIENTS", "agrcode": "SDEBTORS", "subledger": true},
		migrate.Row{"accd": "BANK1", "descr": "TEST BANK A/C", "agrcode": "CASHINHAND", "bankcd": "TB001", "tt": "B1"},
		migrate.Row{"accd": "CAP", "descr": "CAPITAL A/C", "agrcode": "CAPITAL"},
		migrate.Row{"accd": "BRKINC", "descr": "BROKERAGE", "agrcode": "3INCOME"},
		migrate.Row{"accd": "NSECL", "descr": "NSE CLEARING", "agrcode": "CASSETS", "accttype": "EXCH"},
		migrate.Row{"accd": "ODD", "descr": "SOMETHING", "agrcode": ""},
	)
	f.on("msfile", migrate.Row{"co": "02", "accd": "BANK1", "descr": "TEST BANK A/C", "tt": "B1"}, migrate.Row{"co": "02", "accd": "62", "descr": "CLIENTS"})
	f.on("mlfile", migrate.Row{"co": "02", "accd": "62", "subcode": "TEST001", "descr": "Asha Test"})
	f.on("trial-balance",
		migrate.Row{"co": "02", "accd": "62", "subcode": "TEST001", "dr": "1500.00", "cr": "500.00"},
		migrate.Row{"co": "03", "accd": "62", "subcode": "TEST001", "dr": "200.00", "cr": ".00"},
		migrate.Row{"co": "02", "accd": "62", "subcode": "TEST003", "dr": ".00", "cr": "300.00"},
		migrate.Row{"co": "02", "accd": "62", "subcode": "TEST002", "dr": "100.00", "cr": "100.00"},
		migrate.Row{"co": "02", "accd": "BANK1", "subcode": "", "dr": "5000.00", "cr": "1000.00"},
		migrate.Row{"co": "02", "accd": "CAP", "subcode": "", "dr": ".00", "cr": "4000.00"},
		migrate.Row{"co": "02", "accd": "BRKINC", "subcode": "", "dr": ".00", "cr": "900.00"},
		migrate.Row{"co": "02", "accd": "NSECL", "subcode": "", "dr": "250.00", "cr": "250.00"},
	)
	f.on("client-net",
		migrate.Row{"accd": "62", "subcode": "TEST001", "dr": "1700.00", "cr": "500.00"},
		migrate.Row{"accd": "62", "subcode": "TEST003", "dr": ".00", "cr": "300.00"})
	f.on("client-days",
		migrate.Row{"accd": "62", "subcode": "TEST001", "vrdt": d("2026-04-05"), "dr": "1000.00", "cr": ".00"},
		migrate.Row{"accd": "62", "subcode": "TEST001", "vrdt": d("2026-05-10"), "dr": ".00", "cr": "500.00"},
		migrate.Row{"accd": "62", "subcode": "TEST001", "vrdt": d("2026-06-01"), "dr": "700.00", "cr": ".00"},
		migrate.Row{"accd": "62", "subcode": "TEST003", "vrdt": d("2026-05-01"), "dr": ".00", "cr": "300.00"})
	f.on("check-tb", migrate.Row{"dr": "5200.00", "cr": "5200.00"})
	f.on("check-client-balances", migrate.Row{"dr": "1200.00", "cr": "300.00"})
	f.rows["check-clients"] = func(q string) []migrate.Row {
		if strings.Contains(q, "NOT ((EXISTS") {
			return []migrate.Row{{"n": int64(2)}}
		}
		return []migrate.Row{{"n": int64(4)}}
	}
	f.on("check-banks", migrate.Row{"n": int64(3)})
	f.on("count-secret", migrate.Row{"n": int64(2)})
	f.on("hold-date", migrate.Row{"d": d("2026-10-09")})
	f.on("holdings",
		migrate.Row{"co": "06", "ben": "00000001", "isin": "INE000A01011", "bal": "100.000", "pldg": "10.000"},
		migrate.Row{"co": "06", "ben": "00000009", "isin": "INE000A01011", "bal": "5.000", "pldg": ".000"})
	f.on("isin-symbols", migrate.Row{"isin": "INE000A01011", "nse_itemcd": "TESTCO"})
	f.on("check-holdings", migrate.Row{"q": "100.000"})
	f.on("check-dp-accounts", migrate.Row{"n": int64(1)})
	f.on("positions",
		migrate.Row{"co": "03", "subcd": "TEST001", "itemcd": "FUT1", "trdt": d("2026-10-01"), "qty": "50", "price": "100.00", "instr_type": "FUTIDX", "exp_date": d("2026-10-29"), "assetcd": "NIFTY"},
		migrate.Row{"co": "03", "subcd": "TEST001", "itemcd": "FUT1", "trdt": d("2026-10-02"), "qty": "-20", "price": "110.00", "instr_type": "FUTIDX", "exp_date": d("2026-10-29"), "assetcd": "NIFTY"},
		migrate.Row{"co": "03", "subcd": "TEST001", "itemcd": "OPT1", "trdt": d("2026-10-01"), "qty": "75", "price": "10.00", "instr_type": "OPTIDX", "exp_date": d("2026-10-29"), "cont_rate": "20000.00", "cont_type": "C", "assetcd": "NIFTY"},
		migrate.Row{"co": "03", "subcd": "TEST001", "itemcd": "OPT1", "trdt": d("2026-10-02"), "qty": "-75", "price": "12.00", "instr_type": "OPTIDX", "exp_date": d("2026-10-29"), "cont_rate": "20000.00", "cont_type": "C", "assetcd": "NIFTY"},
		migrate.Row{"co": "03", "subcd": "TEST001", "itemcd": "OPT2", "trdt": d("2026-10-02"), "qty": "-50", "price": "5.00", "instr_type": "OPTIDX", "exp_date": d("2026-10-29"), "cont_rate": "21000.00", "descr": "NIFTY 29OCT 21000 PE"},
	)
	f.on("check-positions", migrate.Row{"n": int64(2)})
	f.on("items", migrate.Row{"itemcd": "SC001", "nse_itemcd": "TESTCO"})
	f.on("item-isin", migrate.Row{"itemcd": "SC001", "isin": "INE000A01011"})
	f.on("brokerage-sqr",
		migrate.Row{"subcd": "TEST001", "effectdate": d("2024-04-01"), "itemcd": "ALL", "co": "02", "sqbrptge": ".030000", "sqbrmnrt": ".0100", "sqbrmnper": "", "of": "", "sqbrflrt": ".0000"},
		migrate.Row{"subcd": "TEST001", "effectdate": d("2025-04-01"), "itemcd": "ALL", "co": "02", "sqbrptge": ".020000", "sqbrmnrt": ".0100", "sqbrmnper": "", "of": "", "sqbrflrt": ".0000"},
		migrate.Row{"subcd": "TEST001", "effectdate": d("2024-04-01"), "itemcd": "ALLOPT", "co": "03", "sqbrptge": ".000000", "sqbrflrt": "45.0000", "sqflatper": "Lot"},
		migrate.Row{"subcd": "TEST001", "effectdate": d("2024-04-01"), "itemcd": "ALLXYZ", "co": "02", "sqbrptge": ".05"},
	)
	f.on("brokerage-del",
		migrate.Row{"subcd": "TEST001", "effectdate": d("2024-04-01"), "itemcd": "SC001", "co": "02", "dlbrptge": ".100000", "dlbrkgmin": ".0500", "dlbrmnper": "Per Share"})
	f.on("brokerage-other", migrate.Row{"subcd": "TEST001", "co": "02", "effectdate": d("2024-04-01"), "ord_minbrk": "20.00", "cnt_minbrk": "25.00"})
	f.on("interest",
		migrate.Row{"subcd": "TEST001", "effectdate": d("2026-04-01"), "interestper": "18.00", "nointerest": false, "cgracedays": "3"},
		migrate.Row{"subcd": "TEST002", "effectdate": d("2026-04-01"), "interestper": ".00", "nointerest": false, "cgracedays": "Default"},
	)
	return f
}

func mailFake() *fake {
	f := newFake("TTMAIL2627")
	f.table("SSMAIL2", "slno,id,idtype,ReportCat,date,to,subject,signed")
	f.table("SSMAIL2_ATTACHMENT", "slno,attnumber,filename,contents,Signed,SignType")
	f.on("mail-dates", migrate.Row{"first": d("2026-04-01"), "last": d("2026-10-10"), "n": int64(4)})
	f.on("mail-count", migrate.Row{"mails": int64(4), "bare": int64(1)})
	f.on("attachments",
		migrate.Row{"slno": int64(1), "attnumber": int64(1), "filename": "CN_TEST001.htm", "id": "TEST001", "idtype": "Client", "reportcat": "Contract", "date": d("2026-05-02"),
			"sentto": "asha@example.com", "subject": "Contract Note 501", "mailsigned": int64(1), "use_signed": int64(1), "size": int64(9), "data": "%PDF-1.4 S"},
		migrate.Row{"slno": int64(2), "attnumber": int64(1), "filename": "LEDGER.pdf", "id": "TEST001", "idtype": "Client", "reportcat": "A/C Ledger", "date": d("2026-06-02"),
			"sentto": "asha@example.com", "subject": "Ledger", "mailsigned": int64(0), "use_signed": int64(0), "size": int64(10), "data": []byte("%PDF-1.7 X")},
		migrate.Row{"slno": int64(3), "attnumber": int64(1), "filename": "HOLD.htm", "id": "00000001", "idtype": "DP CLIENT", "reportcat": "", "date": d("2026-07-02"),
			"sentto": "asha@example.com", "subject": "Transaction Statement", "use_signed": int64(0), "size": int64(13), "data": "<html>x</html>"},
		migrate.Row{"slno": int64(4), "attnumber": int64(1), "filename": "BIG.pdf", "id": "TEST001", "idtype": "Client", "reportcat": "Report", "date": d("2026-07-03"),
			"use_signed": int64(0), "size": int64(200 << 20), "data": nil},
	)
	return f
}
