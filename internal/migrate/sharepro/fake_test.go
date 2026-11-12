package sharepro

import (
	"context"
	"fmt"
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
				ty := "varchar"
				if strings.EqualFold(c, "contents") || strings.EqualFold(c, "Signed") {
					ty = "text"
				}
				if err := fn(migrate.Row{"t": t, "c": strings.TrimSpace(c), "ty": ty}); err != nil {
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
// Firm "TT METALS PVT LTD": CO 05 MCX. Firm "TT CREDIT PVT LTD": CO 09,
// inactive. Clients (made up): TEST001 Asha Test (active, in debit),
// TEST002 Bina Test (dormant), TEST003 Chetan Test (closed, in credit),
// TEST004 Dev Test (bad PAN, IFSC, PIN; frozen DP account), TEST005 Test
// Traders (a company with contact persons), TEST006 Esha Test (a client of
// the metals firm). DP-only clients (DP accounts with no trading client):
// 00000009 Gita Test (open), 00000010 Kiran Test (closed), 00000011 Lal
// Test (frozen; its trading code is not a client).

func adminFake() *fake {
	f := newFake("TTADMIN")
	f.table("COLIST", "CO,DESCR,SEBI_REG,SYSTEM,Inactive,CDSL_BEN,NSDL_BEN,DPID")
	f.table("ACC_YEAR", "CO,ACC_YEAR,DETACHED")
	f.table("SSMAIL", "slno,id,date")
	f.table("SSMAIL_ATTACHMENT", "slno,attnumber,contents,Signed")
	f.table("SHUSER", "USERID,PASSWORD")
	f.table("BRANCHES", "BranchId,BranchName,CITY,Contact1,PBranchId,BranchType,Status,Dt_Close")
	f.on("admin-colist",
		migrate.Row{"co": "02", "descr": "TT SECURITIES PVT. LTD.", "sebi_reg": "INZ000000001", "system": "NSE", "inactive": false},
		migrate.Row{"co": "03", "descr": "TT SECURITIES PVT. LTD", "sebi_reg": "INZ000000001", "system": "NFO", "inactive": false},
		migrate.Row{"co": "05", "descr": "TT METALS PVT. LTD (MCX)", "sebi_reg": "INZ000000002", "system": "MCFO", "inactive": false},
		migrate.Row{"co": "06", "descr": "TT SECURITIES PVT. LTD (CDSL)", "sebi_reg": "IN-DP-0-0000", "system": "CDSL", "inactive": false, "cdsl_ben": true},
		migrate.Row{"co": "08", "descr": "TT SECURITIES PVT. LTD(C - MCX)", "sebi_reg": "INZ000000001", "system": "", "inactive": false},
		migrate.Row{"co": "09", "descr": "TT CREDIT PVT LTD", "sebi_reg": "", "system": "NBFC", "inactive": true},
		migrate.Row{"co": "11", "descr": "TT METALS PVT. LTD.(SPOT)", "sebi_reg": "", "system": "", "inactive": false},
	)
	var ys []migrate.Row
	for _, co := range []string{"02", "03", "05", "06"} {
		ys = append(ys, migrate.Row{"co": co, "acc_year": "2526"}, migrate.Row{"co": co, "acc_year": "2627"})
	}
	f.on("admin-acc-year", ys...)
	f.on("admin-years", migrate.Row{"first": "2526", "last": "2627"})
	f.on("branches",
		migrate.Row{"branchid": "HO", "branchname": "Head Office", "city": "KOLKATA", "contact1": "Ravi Manager", "status": "ACTIVE"},
		migrate.Row{"branchid": "EZ", "branchname": "East Zone", "pbranchid": "HO", "status": "ACTIVE"},
		migrate.Row{"branchid": "OLD", "branchname": "Old Branch", "status": "ACTIVE", "dt_close": d("2024-01-01")},
	)
	return f
}

func companyFake(name string) *fake {
	f := newFake(name)
	f.table("DLMAST", "SUBCD,DESCR,CLIENTTYPE,PAN_NO,BIRTH_DATE,MOBILE,EMAIL1,CITY,RES_CITY,STATE,STATECD,COUNTRY,ADD1,ADD2,ADD3,PIN,RES_ADD1,RES_ADD2,RES_ADD3,RES_PIN,FATHERNM,code_Occupation,REG_NO,"+
		"CONTACT,DESIGNATN,CONTACT2,C_DESIG2,CONTACT3,C_DESIG3,C_PAN1,C_PAN2,C_PAN3,C_ADD1,C_ADD2,C_ADD3,C_PHONE1,C_PHONE2,C_PHONE3,C_EMAIL1,C_EMAIL2,C_EMAIL3,"+
		"BranchID,SBCODE,RMCODE,DLRCODE,GROUPCD,FAMILYCD,ClRiskProfile,AnnIncome,BANK,BANKACNUM,MICRNO,BANKACTYPE,NomineeNm,NomineeRel,COMMNCEDT,ADD_DTTIME,CloseOutDt,eContract,KYCStatus,TaxStatus,CLIENTCAT,GENDER,OCCUPATION,CUST_UCC,NoInterest,REG_NSE,REG_BSE,REG_NFO,REG_MCX,ACC_ACCD,ACC_SUBCOD,Web_Pwd")
	f.table("DLAGREE", "Subcd,KycCd,PermState,PermCountry,CorState,CorCountry,MaritalStatus,Nation,NationOth,OccupDtl,OccupDtlOth,IncomeDt,PepConn,IpvDt,ApplDt,DinCont1,DinCont2,DinCont3,C_City1,C_City2,C_City3,C_Pin1,C_Pin2,C_Pin3,UID,NomineeAdhar")
	f.table("DLAGREE2", "Subcd,C_UID1,C_UID2,C_UID3")
	f.table("JVFILE", "CO,TYPE,TT,VRNO,VRDT,ACCD,SUBCODE,NARR,AMTDR,AMTCR,IDN")
	f.table("BCFILE", "co,TYPE,TT,P_R,VRNO,VRDT,ACCD,SUBCODE,NARR,AMTDR,AMTCR,CHEQNO,VALUEDT,BankRef,TXNNO,EcsRef,Gateway,IDN")
	f.table("ACCTOT", "co,branchid,accd,subcode,vrdt,amtdr,amtcr")
	f.table("LY_ACCTOT", "co,branchid,accd,subcode,vrdt,Type,amtdr,amtcr")
	f.table("ACCTGROUP", "AgrCode,PAgrCode,Descr")
	f.table("MASTMSFILE", "ACCD,DESCR,AgrCode,ACCTTYPE,BANKCD,TT,SUBLEDGER")
	f.table("MSFILE", "co,BranchID,ACCD,DESCR,TT,SUBLEDGER,AMTDROB,AMTCROB")
	f.table("MLFILE", "co,BranchID,ACCD,SUBCODE,DESCR,INOPBAL,AMTDROB,AMTCROB")
	f.table("MSACTYPES", "accttype,lExchange")
	f.table("DLMAST_REG2", "co,subcd,status,effectdate,ts")
	f.table("DLMAST_NOMINEE", "SUBCD,NOMINEENO,DESCR,PERCENT,RELATION,IDTYPE,IDNUMBER,ISMINOR,MINORDOB,GDDESCR,GDRELATION,MOBILE,EMAIL,ADD1,CITY,PINCODE,Date,WithDt")
	f.table("DLGROUP", "EffectDate,Type,Code,Subcd")
	f.table("BKMAST", "TYPE,SUBCD,ACNO,ACTYPE,Closed,BANKCD,ADD_DTTIME,POA_RECVD")
	f.table("BANKMAST", "BANKCD,DESCR,RTGS,MICRNO,BRANCH")
	f.table("CLLIST", "CO,DPID,BENACNUM,FBENACNUM,SUBCODE,BENSTAT,BENCLOSEDT,ACTIVEDT,SUSPFLAG,SUSPRSNCD,BSDA,Web_Pwd,FHOLDER_ADHAR,"+strings.Join(holderCols, ","))
	dpDet := "CO,BENACNUM,SEX,MARITALSTATUS,NATION,NATIONOTH,OCCUPN,OCCUPNALL,GROSSINCOME,PEP,CKYCNO,KRASTATDES,IPV,IPV_DATE,IPVDT,REGNO,UID,FHADHARNOM,SHADHARNOM,THADHARNOM"
	for i := 1; i <= 3; i++ {
		for _, c := range []string{"Name", "Desig", "Din", "Pan", "Add", "City", "Pin", "Phone", "Email"} {
			dpDet += fmt.Sprintf(",Cp%d_%s", i, c)
		}
	}
	f.table("CLLIST_DET", dpDet)
	f.table("CDNOMREG", "CO,NOMREGNO,BENACNUM,NOMSLNO,BOREL,SHAREPR,FIRSTNM,MIDNM,LASTNM,NOMFNAME,NOMGRNAME,ADD1,ADD2,ADD3,CITY,PIN,MOBILE,SMobile,Email,PEMAIL,PAN,UID,NomDob,NOMACTVDT")
	f.table("CDMAST", "TYPE,EXCHANGE,SUBCD,DPCODE,CLIENTID,CmBpId,CLIENTNAME,POA_RECVD,Closed")
	f.table("DPMAST", "DPCODE,DESCR,DPID")
	f.table("CLPOALIST", "co,benacnum,poaid,poaimg,PoaType,PoaSetupDt,PoaDeacDt")
	f.table("FREEZERSN", "FREEZERSN,DESCR")
	f.table("HUFCOPER", "CO,BenAcNum,CoPerName,KRTAREL,Status")
	f.table("CLAUTHMAST", "Co,BenAcNum,AuthPerson,UID,MOBILE,EMAIL")
	f.table("SBMAST", "SBCODE,BRANCHID,DESCR,SEBIREGNO,BANKCD,ACNO,PAN_NO,pan,GSTIN,email,Dt_Close,Web_Pwd")
	f.table("RMMAST", "rmcode,descr,branchid,email,pan_no,Bankcd,Acno,Dt_Close,Web_Pwd")
	f.table("DLRMAST", "DLRCODE,DESCR,BRANCHID,email,PAN_NO,GSTIN,Bankcd,Acno,Dt_Close,web_Pwd")
	f.table("HOLD", "CO,BENACNUM,ISINCD,HOLDDATE,BALANCE,CURBAL,PLDGBAL,LOCINBAL,FRZBAL,RATE")
	f.table("DPPRICE", "CO,CLOSEDT,ISINCD,RATE")
	f.table("TRFILE", "NPK,CO,STNO,STTYPE,TRDT,SUBCD,TRNO,cTRDNO,TRDNO,ORDNO,ITEMCD,QTY,ACT_RATE,RATE,BRKG,CNTRNO,Segment")
	f.table("ITC", "ITEMCD,INSTR_TYPE,EXP_DATE,CONT_RATE,NSE_ITEMCD,NSE_SERIES,BSE_ITEMCD,MCX_ITEMCD,DESCR")
	f.table("ICCLCONT", "Nse_itemcd,Bse_itemcd,Cont_type,AssetCd,Nse_series,ContDescr,Bfo_SrsCd,ProductTp")
	f.table("ISINMAST", "ITEMCD,ISINCD")
	f.table("ISINLIST", "ISINCD,ISINSHORT")
	f.table("STATES", "STATECD,DESCR")
	f.table("NSQBRKG", "SUBCD,EffectDate,ITEMCD,UNDITEMCD,CO,PRODUCTCD,ATWHAT,SQBRPTGE,SQBRMNRT,SQBRMNPER,OF,SQBRFLRT,SQFLATPER,SLABCD")
	f.table("NSDBRKG", "SUBCD,EffectDate,ITEMCD,UNDITEMCD,CO,PRODUCTCD,ATWHAT,ONWHICH,DLBRPTGE,DLBRKGMIN,DLBRMNPER,DLBRKGFLT,DLFLATPER,DLSLABCD")
	f.table("OTHBRKG", "SUBCD,CO,EffectDate,ORD_MINBRK,CNT_MINBRK,MINLOTBRKG,DEFA_BRKG,brkgbasket")
	f.table("INTERESTDTL", "Subcd,EffectDate,InterestPer,NoInterest,cGraceDays,SegmentType")
	f.table("DLRUNACMAST", "SUBCD,SEGMENT,ACC_YEAR,SETTLETYPE")
	f.table("KYCMAST", "KYCCD,KYCNO,STATUS,DT_ACTIVE,COMMNCEDT")
	f.table("KYCDTL", "KYCCD,EFFECTDATE,PANNO,GENDER,MARITAL,FAFNAME,FAMNAME,FALNAME,SPFNAME,SPMNAME,SPLNAME,COCN,OCCUP_CODE")
	f.table("KYCADD", "KYCCD,EFFECTDATE,TYPE,ADDTYPE,ADD1,ADD2,ADD3,CITY,STATE,PIN,COA")
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
			"city": "KOLKATA", "statecd": "WB", "add1": "1 Test Lane", "pin": "700001", "branchid": "HO", "sbcode": "SB01", "rmcode": "RM01", "groupcd": "",
			"clriskprofile": "LOW", "annincome": "1-5 LAC", "bank": "TEST BANK", "bankacnum": "123456789012", "micrno": "700000001", "bankactype": "SB",
			"commncedt": d("2020-01-01"), "add_dttime": d("2020-01-01"), "closeoutdt": d("1900-01-01"), "econtract": true, "kycstatus": "Y", "country": "INDIA",
			"reg_nse": true, "reg_bse": true, "reg_nfo": false, "reg_mcx": false, "acc_accd": "", "acc_subcod": "", "nointerest": false, "gender": "F"}
		for k, v := range extra {
			r[k] = v
		}
		return r
	}
	f.on("clients",
		client("TEST001", "Asha Test", "AAAPT0001A", "+91 9800000001", "ASHA@EXAMPLE.COM", migrate.Row{"reg_nfo": true, "groupcd": "G1"}),
		client("TEST002", "Bina Test", "AAAPT0002B", "9800000002", "bina@example.com", migrate.Row{"sbcode": "SB09", "rmcode": "", "dlrcode": "D01"}),
		client("TEST003", "Chetan Test", "AAAPT0003C", "9800000003", "chetan@example.com", migrate.Row{"closeoutdt": d("2024-01-01")}),
		client("TEST004", "Dev Test", "BADPAN", "12345", "dev@example", migrate.Row{"nointerest": true}),
		client("TEST005", "Test Traders Pvt Ltd", "AAACT0005E", "9800000005", "traders@example.com", nil),
		client("TEST006", "Esha Test", "AAAPT0006F", "9800000006", "esha@example.com", migrate.Row{"reg_nse": false, "reg_bse": false, "reg_mcx": true}),
	)
	f.on("client-accounts", migrate.Row{"subcd": "TEST001"}, migrate.Row{"subcd": "TEST002"}, migrate.Row{"subcd": "TEST003"},
		migrate.Row{"subcd": "TEST004"}, migrate.Row{"subcd": "TEST005"}, migrate.Row{"subcd": "TEST006"})
	f.on("client-groups", migrate.Row{"subcd": "TEST001", "type": "F", "code": "FAM1", "rn": int64(1)}, migrate.Row{"subcd": "TEST001", "type": "F", "code": "OLD", "rn": int64(2)})
	f.on("running-account", migrate.Row{"subcd": "TEST001", "settletype": "3", "rn": int64(1)}, migrate.Row{"subcd": "TEST002", "settletype": "1", "rn": int64(1)},
		migrate.Row{"subcd": "TEST003", "settletype": "2", "rn": int64(1)})
	f.on("states", migrate.Row{"statecd": "WB", "descr": "West Bengal"})
	f.on("kyc", migrate.Row{"kyccd": "K1", "kycno": "12345678901234", "status": "UPLOADED", "dt_active": d("2020-02-01"), "panno": "AAAPT0001A"},
		migrate.Row{"kyccd": "K2", "kycno": "", "status": "UPLOADED", "panno": "AAAPT0002B"})
	f.on("kyc-detail", migrate.Row{"kyccd": "K2", "gender": "M", "marital": "2", "occup_code": "S-01", "fafname": "Old", "falname": "Test", "rn": int64(1)})
	f.on("kyc-address",
		migrate.Row{"kyccd": "K2", "type": "COR", "add1": "5 Kyc Street", "city": "HOWRAH", "state": "WB", "pin": "711101", "rn": int64(1)},
		migrate.Row{"kyccd": "K2", "type": "PER", "add1": "6 Home Lane", "city": "HOWRAH", "state": "WB", "pin": "711102", "rn": int64(1)})
	f.on("ucc-details",
		migrate.Row{"subcd": "TEST001", "add1": "1 Test Lane", "add2": "Floor 2", "city": "KOLKATA", "pin": "700001", "statecd": "WB", "country": "INDIA", "gender": "F", "fathernm": "Ravi Test",
			"code_occupation": "02", "kyccd": "K1", "maritalstatus": "1", "nation": "01", "pepconn": "NA", "ipvdt": d("2020-01-05"), "appldt": d("2020-01-02"),
			"res_add1": "2 Home Road", "res_city": "KOLKATA", "res_pin": "700002", "permstate": "WB", "permcountry": "INDIA"},
		migrate.Row{"subcd": "TEST002", "kyccd": "K2"},
		migrate.Row{"subcd": "TEST003", "add1": "3 Lane", "city": "KOLKATA", "pin": "700003", "statecd": "WB"},
		migrate.Row{"subcd": "TEST004", "add1": "4 Lane", "city": "KOLKATA", "pin": "", "statecd": "WB"},
		migrate.Row{"subcd": "TEST005", "add1": "5 Office Road", "city": "KOLKATA", "pin": "700005", "statecd": "WB", "clientcat": "Body Corporate", "reg_no": "U12345WB2020PTC000001"},
		migrate.Row{"subcd": "TEST006", "add1": "6 Lane", "city": "KOLKATA", "pin": "700006", "statecd": "WB"},
	)
	f.on("contacts",
		migrate.Row{"subcd": "TEST001", "contact": "Not Used"},
		migrate.Row{"subcd": "TEST005", "contact": "Ravi Director", "designatn": "Director", "c_pan1": "AAAPD0001D", "dincont1": "01234567", "c_add1": "9 Road", "c_city1": "KOLKATA",
			"c_phone1": "9800000101", "c_email1": "RAVI@EXAMPLE.COM", "contact2": "Sita Partner", "c_desig2": ""},
	)
	f.on("client-banks",
		migrate.Row{"subcd": "TEST001", "type": "Primary", "acno": "123456789012", "actype": "SB", "closed": false, "bank": "TEST BANK", "ifsc": "TEST0000001", "micr": "700000001", "branch": "MAIN", "poa_recvd": true},
		migrate.Row{"subcd": "TEST001", "type": "Secondary", "acno": "999988887777", "actype": "CA", "closed": true, "bank": "OTHER BANK", "ifsc": "OTHR0000002"},
		migrate.Row{"subcd": "TEST001", "type": "Pool", "acno": "555544443333", "actype": "CA", "closed": false, "bank": "TEST BANK", "ifsc": "TEST0000001"},
		migrate.Row{"subcd": "TEST004", "type": "Primary", "acno": "12AB", "actype": "SB", "closed": false, "bank": "TEST BANK", "ifsc": "BAD"},
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
		migrate.Row{"co": "02", "subcd": "TEST005", "status": "ACTIVE", "effectdate": d("2023-01-01")},
		migrate.Row{"co": "05", "subcd": "TEST006", "status": "ACTIVE", "effectdate": d("2023-01-01")},
	)
	// DP accounts (CLLIST, CO 06): TEST001 has two (the second a joint
	// account); 00000009 (Gita Test, open), 00000010 (Kiran Test, closed)
	// and 00000011 (Lal Test, frozen, its trading code not a client) have
	// no trading client: DP-only clients
	acct := func(ben, sub string, extra migrate.Row) migrate.Row {
		r := migrate.Row{"co": "06", "dpid": "12345678", "benacnum": ben, "fbenacnum": "12345678" + ben, "subcode": sub, "benclosedt": d("1900-01-01"), "benstat": "1"}
		for k, v := range extra {
			r[k] = v
		}
		return r
	}
	holder := func(name, pan, mobile, email, add1, pin, bankAc string, extra migrate.Row) migrate.Row {
		r := migrate.Row{"ben1holder": name, "itpan": pan, "fholder_mobile": mobile, "custemail": email, "ben1add1": add1, "ben1city": "KOLKATA", "ben1pin": pin,
			"statecd": "WB", "ben1cntry": "INDIA", "benbnkname": "TEST BANK", "benbnkac": bankAc, "bankcd": "TB001", "benbnktype": int64(10), "branchid": "HO", "bentype": int64(1)}
		for k, v := range extra {
			r[k] = v
		}
		return r
	}
	f.on("dp-accounts",
		acct("00000001", "TEST001", migrate.Row{"activedt": d("2020-01-01"), "suspflag": "0", "bsda": "N"}),
		acct("00000009", "", holder("Gita Test", "AAAPT0009G", "+91-9800000009", "GITA@EXAMPLE.COM", "9 DP Road", "700 009", "1234 5678 9009", migrate.Row{
			"activedt": d("2021-02-01"), "bsda": "Y", "ben1fathub": "Hari Test", "dob": d("1985-07-01"), "bosex": "F", "ben1add2": "Block B", "ben1add3": "Near Park", "ben1add4": "Ward 5",
			"ben1state": "WEST BENGAL", "ben1fadd1": "9 Village Road", "ben1fcity": "HOWRAH", "ben1fstate": "WB", "ben1fpin": "711109", "clriskprofile": "HIGH", "micrcode": "700000009", "groupcd": "DPG"})),
		acct("00000004", "TEST004", migrate.Row{"suspflag": "D", "susprsncd": "12"}),
		acct("00000003", "TEST003", migrate.Row{"benclosedt": d("2024-01-01"), "benstat": "3"}),
		acct("00000002", "TEST001", migrate.Row{"activedt": d("2022-06-01"), "ben2holder": "Ravi Test", "ben2pan": "AAAPT0011K", "ben3holder": "Mira Test", "ben3pan": "BADPAN3"}),
		acct("00000010", "", holder("Kiran Test", "AAAPT0010K", "", "kiran@example.com", "10 Old Lane", "700010", "555566667777", migrate.Row{
			"activedt": d("2015-04-01"), "benclosedt": d("2023-03-31"), "benstat": "3", "bankcd": "ZZ999", "benbnkname": "OTHER BANK"})),
		acct("00000011", "OLD999", holder("Lal Test", "AAAPT0013L", "9800000013", "lal@example.com", "11 New Lane", "700011", "123456780013", migrate.Row{
			"activedt": d("2019-01-01"), "benstat": "2", "susprsncd": "0"})),
	)
	f.on("dp-details",
		migrate.Row{"co": "06", "benacnum": "00000001", "sex": "M"}, // a trading client's: not used
		migrate.Row{"co": "06", "benacnum": "00000009", "sex": "F", "maritalstatus": "01", "nation": "01", "occupn": "01", "pep": "N", "ckycno": "98765432109876",
			"krastatdes": "KRA VALIDATED", "ipv_date": d("2021-01-20"), "grossincome": "6"},
		migrate.Row{"co": "06", "benacnum": "00000010", "occupn": "06"},
	)
	f.on("dp-nominees",
		migrate.Row{"co": "06", "benacnum": "00000001", "nomslno": "01", "borel": "01", "firstnm": "Not Used"},
		migrate.Row{"co": "06", "benacnum": "00000009", "nomslno": "01", "borel": "06", "sharepr": "60.00", "firstnm": "Raju", "lastnm": "Test", "nomdob": d("2010-01-01"),
			"nomgrname": "Hari Test", "add1": "9 DP Road", "city": "KOLKATA", "pin": "700009", "mobile": "9800000019", "nomactvdt": d("2021-02-01")},
		migrate.Row{"co": "06", "benacnum": "00000009", "nomslno": "02", "borel": "01", "sharepr": "40.00", "firstnm": "Lata", "midnm": "", "lastnm": "Test", "pan": "AAAPT0019R", "mobile": "0"},
		migrate.Row{"co": "06", "benacnum": "00000010", "nomslno": "01", "borel": "04", "firstnm": "Old Test", "pan": "BADPAN"},
	)
	// the client demat master: TEST001's primary is its own account at this
	// DP, a second one at another DP; TEST005's primary is at another DP
	f.on("client-demat",
		migrate.Row{"subcd": "TEST001", "type": "Primary", "dpcode": "D1", "dpid": "12345678", "clientid": "00000001", "closed": false},
		migrate.Row{"subcd": "TEST001", "type": "Secondary", "dpcode": "D2", "dpid": "87654321", "clientid": "00000777", "closed": false},
		migrate.Row{"subcd": "TEST005", "type": "Primary", "dpcode": "D2", "dpid": "87654321", "clientid": "00001234", "closed": false},
		migrate.Row{"subcd": "TEST002", "type": "Pool", "dpcode": "D1", "dpid": "12345678", "clientid": "00000555", "closed": false},
		migrate.Row{"subcd": "TEST003", "type": "Primary", "dpcode": "D9", "clientid": "BAD", "closed": false},
	)
	f.on("dp-poa", migrate.Row{"co": "06", "benacnum": "00000001", "poatype": "DDPI"})
	f.on("freeze-reasons", migrate.Row{"code": "12", "descr": "TEST FREEZE"})
	// 7 accounts; less a dormant trading client's (NOT IN) and, in dormant
	// mode skip, the closed DP-only one (BENCLOSEDT)
	f.rows["check-dp-accounts"] = func(q string) []migrate.Row {
		n := int64(7)
		if strings.Contains(q, "NOT IN") {
			n--
		}
		if strings.Contains(q, "BENCLOSEDT") {
			n--
		}
		return []migrate.Row{{"n": n}}
	}
	f.rows["check-dp-only-clients"] = func(q string) []migrate.Row {
		if strings.Contains(q, "BENCLOSEDT") {
			return []migrate.Row{{"n": int64(2)}}
		}
		return []migrate.Row{{"n": int64(3)}}
	}
	f.on("sub-brokers", migrate.Row{"sbcode": "SB01", "branchid": "HO", "descr": "Test AP One", "sebiregno": "AP0001", "bankcd": "TB001", "acno": "111122223333", "pan_no": "AAAPA0001A", "email": "AP@EXAMPLE.COM"})
	f.on("rms", migrate.Row{"rmcode": "RM01", "descr": "Test RM", "branchid": "HO"})
	f.on("dealers", migrate.Row{"dlrcode": "D01", "descr": "Test Dealer", "branchid": "HO", "dt_close": d("1900-01-01")})
	f.on("bank-names", migrate.Row{"bankcd": "TB001", "descr": "TEST BANK", "ifsc": "TEST0000001"})
	f.on("last-trade", migrate.Row{"subcd": "TEST001", "last": d("2026-10-02")})
	f.on("acctgroup",
		migrate.Row{"code": "1LIABILITI", "parent": "", "descr": "SOURCES OF FUNDS"},
		migrate.Row{"code": "CAPITAL", "parent": "1LIABILITI", "descr": "SHARES HOLDER FUNDS"},
		migrate.Row{"code": "CLIB", "parent": "1LIABILITI", "descr": "CURRENT LIABILITIES"},
		migrate.Row{"code": "2ASSETS", "parent": "", "descr": "APPLICATION OF FUNDS"},
		migrate.Row{"code": "CASSETS", "parent": "2ASSETS", "descr": "CURRENT ASSETS"},
		migrate.Row{"code": "SDEBTORS", "parent": "CASSETS", "descr": "SUNDRY DEBTORS"},
		migrate.Row{"code": "CASHINHAND", "parent": "CASSETS", "descr": "CASH AND BANK BALANCE"},
		migrate.Row{"code": "3INCOME", "parent": "", "descr": "INCOME"},
	)
	f.on("msactypes", migrate.Row{"accttype": "EXCH", "lexchange": true})
	f.on("mastmsfile",
		migrate.Row{"accd": "62", "descr": "CLIENTS", "agrcode": "SDEBTORS"},
		migrate.Row{"accd": "MRG", "descr": "CASH MARGIN DEPOSIT [CLIENTS]", "agrcode": "CLIB"},
		migrate.Row{"accd": "BANK1", "descr": "TEST BANK A/C", "agrcode": "CASHINHAND", "bankcd": "TB001", "tt": "B1"},
		migrate.Row{"accd": "CAP", "descr": "CAPITAL A/C", "agrcode": "CAPITAL"},
		migrate.Row{"accd": "BRKINC", "descr": "BROKERAGE", "agrcode": "3INCOME"},
		migrate.Row{"accd": "NSECL", "descr": "NSE CLEARING", "agrcode": "CASSETS", "accttype": "EXCH"},
	)
	f.on("msfile",
		migrate.Row{"co": "02", "accd": "BANK1", "descr": "TEST BANK A/C", "tt": "B1"},
		migrate.Row{"co": "02", "accd": "62", "descr": "CLIENTS"}, migrate.Row{"co": "03", "accd": "62", "descr": "CLIENTS"},
		migrate.Row{"co": "02", "accd": "MRG", "descr": "CASH MARGIN DEPOSIT [CLIENTS]"})
	var subs []migrate.Row
	for _, u := range []string{"TEST001", "TEST002", "TEST003", "TEST004", "TEST005"} {
		subs = append(subs, migrate.Row{"co": "02", "accd": "62", "subcode": u, "descr": u})
	}
	subs = append(subs, migrate.Row{"co": "03", "accd": "62", "subcode": "TEST001", "descr": "Asha Test"},
		migrate.Row{"co": "02", "accd": "MRG", "subcode": "TEST001", "descr": "Asha Test"})
	f.on("mlfile", subs...)
	// opening (MLFILE/MSFILE) plus movements (ACCTOT), as the server adds them up
	f.on("trial-balance",
		migrate.Row{"co": "02", "accd": "62", "subcode": "TEST001", "b": "1000.00"},
		migrate.Row{"co": "03", "accd": "62", "subcode": "TEST001", "b": "200.00"},
		migrate.Row{"co": "02", "accd": "62", "subcode": "TEST003", "b": "-300.00"},
		migrate.Row{"co": "02", "accd": "62", "subcode": "TEST002", "b": ".00"},
		migrate.Row{"co": "02", "accd": "MRG", "subcode": "TEST001", "b": "-150.00"},
		migrate.Row{"co": "02", "accd": "BANK1", "subcode": "", "b": "4000.00"},
		migrate.Row{"co": "02", "accd": "CAP", "subcode": "", "b": "-4000.00"},
		migrate.Row{"co": "02", "accd": "BRKINC", "subcode": "", "b": "-900.00"},
		migrate.Row{"co": "02", "accd": "NSECL", "subcode": "", "b": ".00"},
	)
	f.on("client-net",
		migrate.Row{"co": "02", "accd": "62", "subcode": "TEST001", "dr": "1500.00", "cr": "500.00"},
		migrate.Row{"co": "03", "accd": "62", "subcode": "TEST001", "dr": "200.00", "cr": ".00"},
		migrate.Row{"co": "02", "accd": "62", "subcode": "TEST003", "dr": ".00", "cr": "300.00"})
	f.on("client-days",
		migrate.Row{"co": "02", "accd": "62", "subcode": "TEST001", "vrdt": d("2026-04-05"), "dr": "1000.00", "cr": ".00"},
		migrate.Row{"co": "02", "accd": "62", "subcode": "TEST001", "vrdt": d("2026-05-10"), "dr": ".00", "cr": "500.00"},
		migrate.Row{"co": "02", "accd": "62", "subcode": "TEST001", "vrdt": d("2026-06-01"), "dr": "500.00", "cr": ".00"},
		migrate.Row{"co": "03", "accd": "62", "subcode": "TEST001", "vrdt": d("2026-06-01"), "dr": "200.00", "cr": ".00"},
		migrate.Row{"co": "02", "accd": "62", "subcode": "TEST003", "vrdt": d("2026-05-01"), "dr": ".00", "cr": "300.00"})
	f.on("check-tb", migrate.Row{"dr": "5200.00", "cr": "5350.00"})
	f.on("check-client-balances", migrate.Row{"dr": "1200.00", "cr": "300.00"})
	f.rows["check-clients"] = func(q string) []migrate.Row {
		if strings.Contains(q, "NOT ((EXISTS") {
			return []migrate.Row{{"n": int64(3)}}
		}
		return []migrate.Row{{"n": int64(5)}}
	}
	f.on("check-banks", migrate.Row{"n": int64(3)})
	f.on("count-secret", migrate.Row{"n": int64(2)})
	f.on("hold-date", migrate.Row{"d": d("2026-10-09")})
	f.on("holdings",
		migrate.Row{"co": "06", "ben": "00000001", "isin": "INE000A01011", "bal": "100.000", "pldg": "10.000"},
		migrate.Row{"co": "06", "ben": "00000002", "isin": "INE000A01011", "bal": "20.000", "pldg": ".000"},
		migrate.Row{"co": "06", "ben": "00000009", "isin": "INE000A01011", "bal": "5.000", "pldg": ".000"},
		migrate.Row{"co": "06", "ben": "00000009", "isin": "INF000A01015", "bal": "125.4320", "pldg": "25.1000"}, // mutual fund units
		migrate.Row{"co": "06", "ben": "00000099", "isin": "INE000A01011", "bal": "7.000", "pldg": ".000"})       // not in CLLIST
	f.on("isin-symbols", migrate.Row{"isin": "INE000A01011", "nse_itemcd": "TESTCO"})
	f.on("check-holdings", migrate.Row{"q": "250.4320"})
	f.on("positions",
		migrate.Row{"co": "03", "subcd": "TEST001", "itemcd": "FUT1", "trdt": d("2026-10-01"), "qty": "50", "price": "100.00", "instr_type": "FUTIDX", "exp_date": d("2026-10-29"), "assetcd": "NIFTY"},
		migrate.Row{"co": "03", "subcd": "TEST001", "itemcd": "FUT1", "trdt": d("2026-10-02"), "qty": "-20", "price": "110.00", "instr_type": "FUTIDX", "exp_date": d("2026-10-29"), "assetcd": "NIFTY"},
		migrate.Row{"co": "03", "subcd": "TEST001", "itemcd": "OPT1", "trdt": d("2026-10-01"), "qty": "75", "price": "10.00", "instr_type": "OPTIDX", "exp_date": d("2026-10-29"), "cont_rate": "20000.00", "k_descr": "NIFTY 29OCT 20000 CE", "assetcd": "NIFTY"},
		migrate.Row{"co": "03", "subcd": "TEST001", "itemcd": "OPT1", "trdt": d("2026-10-02"), "qty": "-75", "price": "12.00", "instr_type": "OPTIDX", "exp_date": d("2026-10-29"), "cont_rate": "20000.00", "k_descr": "NIFTY 29OCT 20000 CE", "assetcd": "NIFTY"},
		migrate.Row{"co": "03", "subcd": "TEST001", "itemcd": "OPT2", "trdt": d("2026-10-02"), "qty": "-50", "price": "5.00", "instr_type": "OPTIDX", "exp_date": d("2026-10-29"), "cont_rate": "21000.00", "descr": "NIFTY 29OCT 21000 PE"},
		migrate.Row{"co": "03", "subcd": "TEST001", "itemcd": "OPT3", "trdt": d("2026-10-02"), "qty": "10", "price": "5.00", "instr_type": "OPTSTK", "exp_date": d("2026-10-29"), "cont_rate": "500.00", "descr": "TESTCO OCT"},
	)
	f.on("check-positions", migrate.Row{"n": int64(3)})
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
	f.table("SSMAIL2", "slno,id,idtype,ReportCat,date,to,subject,signed,sentdate")
	f.table("SSMAIL2_ATTACHMENT", "slno,attnumber,filename,contents,Signed,SignType,embedded")
	f.table("XSSMAIL2", "slno,id,idtype,ReportCat,date,to,subject,signed,sentdate")
	f.table("XSSMAIL2_ATTACHMENT", "slno,attnumber,filename,contents,Signed,SignType,embedded")
	f.on("mail-dates", migrate.Row{"first": d("2026-04-01"), "last": d("2026-10-10"), "n": int64(8)})
	f.on("mail-bare", migrate.Row{"cat": "RUNACEMAILSMS", "n": int64(1)})
	f.on("mail-rows", migrate.Row{"n": int64(2)})
	att := func(slno int64, file, id, idtype, cat, subject string, data any, signed int64) migrate.Row {
		size := int64(0)
		switch x := data.(type) {
		case string:
			size = int64(len(x))
		case []byte:
			size = int64(len(x))
		}
		return migrate.Row{"slno": slno, "attnumber": int64(1), "filename": file, "id": id, "idtype": idtype, "reportcat": cat, "date": d("2026-05-02"),
			"sentto": "asha@example.com", "subject": subject, "mailsigned": signed, "use_signed": signed, "size": size, "data": data}
	}
	f.rows["attachments"] = func(q string) []migrate.Row {
		if strings.Contains(q, "FROM XSSMAIL2 m") {
			return []migrate.Row{
				att(1, "CN_TEST001.htm", "TEST001", "Client", "Contract", "Contract Note 501", "%PDF-1.4 S", 1), // already read
				att(8, "MARGIN.pdf", "TEST001", "Client", "Margin2Clients", "Margin statement", "%PDF-1.4 M", 1),
			}
		}
		big := att(4, "BIG.pdf", "TEST001", "Client", "Report", "Big", nil, 0)
		big["size"] = int64(200 << 20)
		return []migrate.Row{
			att(1, "CN_TEST001.htm", "TEST001", "Client", "Contract", "Contract Note 501", "%PDF-1.4 S", 1),
			att(2, "LEDGER.pdf", "TEST001", "CLIENT", "A/C Ledger", "Ledger", []byte("%PDF-1.7 X"), 0),
			att(3, "HOLD.htm", "00000001", "DP CLIENT", "", "Transaction Statement", "<html>x</html>", 0),
			big,
			att(5, "PAYOUT.htm", "HO", "Branchid", "Payout Report", "Payout report", "<html>p</html>", 0),
			att(6, "CN_TEST006.htm", "TEST006", "Client", "Contract", "Contract Note 601", "%PDF-1.4 T", 1),
			att(7, "MISC.htm", "ZZZ999", "Client", "", "Something", "<html>z</html>", 0),
			att(9, "DPSTMT.htm", "1234567800000009", "DP CLIENT", "", "Transaction Statement", "<html>d</html>", 0), // a DP-only client's
		}
	}
	return f
}
