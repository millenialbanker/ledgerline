# SharePro to Ledgerline: where each column comes from

SharePro (StanSoft) keeps, on one SQL Server:

- an **admin database** (`COLIST`, `ACC_YEAR`, `BRANCHES`, users, SMS);
- one **company database per financial year**, named `<prefix><yy><yy+1>`
  (the prefix is the broker's own); inside it `CO` is SharePro's
  "company": one exchange segment (or the DP, PMS, NBFC) of a legal entity;
- **mail archives** (`SSMAIL2`/`SSMAIL2_ATTACHMENT`, older `SSMAIL`/
  `SSMAIL_ATTACHMENT`), per year or spanning several years.

Only table and column names appear here. `?` marks a column whose meaning
was inferred from its name and from counts, not confirmed against
SharePro's documentation: check these on a real database first.

## Classify

| Kind | Test | FY |
|---|---|---|
| admin (order 0) | tables `COLIST` and `ACC_YEAR` | – |
| company (order 10–49, newest first) | tables `DLMAST`, `JVFILE` and `ACCTOT` or `MLFILE` | `<letter><yy><yy+1>` in the database name; else the year with most `JVFILE.VRDT` |
| mail (order 50–99) | `SSMAIL2`+`SSMAIL2_ATTACHMENT` (or `SSMAIL`, `XSSMAIL2`, `XSSMAIL`) with `contents` | name as above; else `MIN/MAX(date)` if in one year; else empty (several years) |
| other | anything else (skipped) | – |

The admin database also has (empty) `SSMAIL` tables: it is tested first.

## Firms

`COLIST.CO, DESCR, SEBI_REG, Inactive, CDSL_BEN, NSDL_BEN` and
`ACC_YEAR.CO, ACC_YEAR`. COs are grouped into one firm per legal entity by
`DESCR` without its bracketed segment tag and punctuation ("X PVT. LTD.
(NSE)" and "X PVT LTD(C - MCX)" are one firm). Firm code = the lowest CO of
the group. A CO's segment: `TRFILE.Segment` (most trades) if it has trades,
else the bracketed tag of `DESCR` (NSE, NFO, BSE, BFO, C - NSE, C - MCX,
MCX, NCDEX, CDSL, NSDL), else `SEBI_REG` `IN-DP` / `CDSL_BEN` → DP.
Without the admin database every CO in the company database (`TRFILE`,
`JVFILE`, `CLLIST`) is one firm.

Clients: `DLMAST` is shared by all COs. With several firms a client belongs
to each firm in whose COs it has a `DLMAST_REG2` status or a `CLLIST` DP
account; clients with neither go to the first firm.

## Go-live files (newest company database)

As-of date: `Plan.AsOf`, else the later of `MAX(JVFILE.VRDT)` and
`MAX(TRFILE.TRDT)`.

### CLIENT-MASTER (`DLMAST`, one row per `SUBCD`)

| Column | From |
|---|---|
| UCC | `DLMAST.SUBCD` (the code ledgers use; `CUST_UCC` when different is reported) |
| Name | `DESCR` |
| Type | 4th character of `PAN_NO`: P Individual (NRI when `CLIENTCAT`/`CLIENTTYPE` says NRI or `COUNTRY` is not India), H HUF, C Corporate; others are reported (no Ledgerline type) |
| PAN | `PAN_NO` |
| DOB | `BIRTH_DATE` |
| Mobile | `MOBILE` (digits; +91/0 removed) |
| Email | `EMAIL1` |
| City | `CITY`, else `RES_CITY` |
| Segments | `REG_NSE`/`REG_BSE` CM, `REG_NFO`/`REG_BFO`/`REG_NSFO` FO, `REG_CNFO`/`REG_CBFO`/`REG_USFO` CD, `REG_MCX`/`REG_NCDEX`/`REG_CMFO`/`REG_NMCEIL`/`REG_ICFO`/`REG_KCFO`/`REG_DGCX` COM, plus every segment with a `DLMAST_REG2` row in the firm's COs |
| Risk | `ClRiskProfile` (LOW/MEDIUM/HIGH) |
| Income | `AnnIncome` (as text) |
| RAS | `DLRUNACMAST.SETTLETYPE?` latest `ACC_YEAR` (1/30 Monthly, 2/3/90 Quarterly) |
| Bank, IFSC, Account | primary account: the `BKMAST` row whose `ACNO` is `DLMAST.BANKACNUM`, else the first open `BKMAST` row; bank name `BANKMAST.DESCR`, IFSC `BANKMAST.RTGS?` (joined on `BANKCD`); else `DLMAST.BANK`, `BANKACNUM` |
| Depository, DematID | `CLLIST` (`SUBCODE` = client) in the firm's COs: `FBENACNUM` (or `DPID`+`BENACNUM`); CDSL, or NSDL when it starts with IN; an open account is preferred |
| Nominee, NomineeName | "Opted in" and the first `DLMAST_NOMINEE.DESCR`, else `DLMAST.NomineeNm` |
| KYCDate | `KYCMAST.DT_ACTIVE` / `COMMNCEDT` (via `KYCDTL.PANNO`), else `DLMAST.COMMNCEDT`, `ADD_DTTIME` |
| Status | `Active` (the onboarding stage is complete for every migrated client) |
| API | N |
| LastTrade | `MAX(TRFILE.TRDT)`, `MAX(LY_TRADES.trdt)` |
| UPIBlock, GSTIN, NominationMode | empty |
| Branch | `BranchID` |
| State | `STATES.DESCR` of `DLMAST.STATECD`, else `STATE` |
| ECN | `eContract` (Y/N) |
| NominationDate | earliest `DLMAST_NOMINEE.Date` |
| Inactive, InactiveSince | Y and the latest status date for a client with no Active/Reactivated segment in the firm (or, with no status rows, a `CloseOutDt`). **Not read by the import yet.** |

Dormant clients: `Plan.DormantMode` "inactive" (default) writes them with
Inactive = Y; "skip" leaves them out of every client file, keeps their
balances in OPENING-BALANCES and raises an Error for any balance, holding
or position they have.

Every row is checked against the import's rules (UCC, name, PAN, mobile,
email, city, segment, bank, IFSC, account) and each failure is an Error
issue with the client code: the import rejects the whole file on one bad
row.

### CLIENT-BANKS (`BKMAST` ⋈ `BANKMAST` on `BANKCD`)

UCC `BKMAST.SUBCD`; Bank `BANKMAST.DESCR`; IFSC `BANKMAST.RTGS?`; Account
`BKMAST.ACNO`; Primary as above; Status `Closed?` → Inactive; Branch
`BANKMAST.BRANCH`; MICR `BANKMAST.MICRNO`; Type `BKMAST.ACTYPE` (SB/CA/NRE/NRO);
AddedOn `BKMAST.ADD_DTTIME`. Clients with no `BKMAST` row get the client
master's account (warning: no IFSC).

### NOMINEES (`DLMAST_NOMINEE`, rows with `WithDt` set are withdrawn and left out)

Name `DESCR`; Relation `RELATION`; Share `PERCENT`; IDType from `IDTYPE`
(PAN, DL, AADHAAR, PASSPORT; others: no ID); ID `IDNUMBER` — for Aadhaar,
and for any 12-digit number, only `RIGHT(IDNUMBER, 4)` is selected, on the
server; DOB `MINORDOB` when `ISMINOR`; Guardian `GDDESCR`;
GuardianRelation `GDRELATION`; Mobile `MOBILE`; Email `EMAIL`; Address
`ADD1`, `CITY`, `PINCODE`; DeclaredOn `Date`.

### SEGMENT-STATUS (`DLMAST_REG2`: `co`, `subcd`, `status`, `effectdate`, ordered by `effectdate`, `ts`)

Segment from the CO (see Firms); Status ACTIVE, REACTIVATED, SUSPENDED,
DORMANT, CLOSED (others reported). Where a segment has two exchanges (NSE
and BSE cash) its status at each change is the most open of the two
(Active > Reactivated > Dormant > Suspended > Closed) and a row is written
only when that changes. Reason "SharePro <co> <status>". Statuses of DP,
PMS and other non-exchange COs are not migrated (Info).

### OPENING-BALANCES (`ACCTOT`)

`SUM(amtdr)`, `SUM(amtcr)` by `co, accd, subcode` with `vrdt <= as-of`, for
the firm's COs (ACCTOT holds the opening balance and daily totals?).

Account codes:

- client ledger — head `DLMAST.ACC_ACCD`, sub-account `ACC_SUBCOD` (or the
  client code); when no client names a head, the heads most of whose
  `MLFILE` sub-accounts are client codes — `CLIENT:<SUBCD>`, one account
  over all the firm's COs;
- any other head or sub-account — `<PREFIX>:<CO>-<ACCD>[-<SUBCODE>]`,
  PREFIX from the head's group (`MASTMSFILE.AgrCode` up the `ACCTGROUP`
  tree, `PAgrCode`): CASHINHAND → BANK (with `BANKCD` or "BANK" in the
  name) or CASH; `MSACTYPES.lExchange` for `MASTMSFILE.ACCTTYPE?` or
  "EXCHANGE"/"CLEARING" → CC; CAPITAL/RESERVE → EQUITY; LOAN → LOAN;
  SDEBTORS → RECEIVABLE; fixed assets, investments, deposits, advances,
  stock, current assets → ASSET; creditors, duties and taxes, provisions,
  current liabilities → PAYABLE; INCOME → INCOME; EXPENDITURE → EXPENSE;
  otherwise SUSPENSE (warning per head).

Name: `MSFILE.DESCR` / `MLFILE.DESCR`. Zero balances are left out. An
unbalanced total is an Error (never balanced by a made-up line).

Since (client debits): the year's client lines (`JVFILE` and `BCFILE` on
the client heads, from the year's start to the as-of date) netted per day,
first in first out — each credit pays the oldest debit — starting from the
year's opening balance (balance minus the year's movement) dated the
year's first day. Since = the date of the oldest debit still unpaid.

Checks (all summed on the server from `ACCTOT`, client ledgers merged by
the same `DLMAST` join): trial-balance-debit, trial-balance-credit,
client-debit-total, client-credit-total.

### DP-HOLDINGS (`HOLD` at `MAX(HOLDDATE) <= as-of`, the firm's DP COs)

UCC `CLLIST.SUBCODE` of (`CO`, `BENACNUM`); ISIN `ISINCD`; Symbol
`ITC.NSE_ITEMCD` / `BSE_ITEMCD` via `ISINMAST.ITEMCD`; Qty `SUM(BALANCE?)`;
Pledged `SUM(PLDGBAL?)`; AvgCost empty. One row per client and ISIN (a
client's DP accounts added up). DP accounts with no trading code are
reported, not written. Checks: dp-holding-quantity (`SUM(HOLD.BALANCE)` of
the migrated clients' accounts), dp-accounts (open `CLLIST` rows, no
`BENCLOSEDT`, of migrated clients).

### HOLDING-LOTS

Not written: SharePro holds no purchase lots or cost for DP holdings.

### POSITIONS (`TRFILE` of FO, CD and COM COs, `TRDT <= as-of`, contract `ITC.EXP_DATE >= as-of`)

Net `QTY` (signed: + buy, − sell?) per client, CO and `ITEMCD`; open
quantity and average price by FIFO over `ACT_RATE` (else `RATE`); Type
from `ITC.INSTR_TYPE` (FUT…, OPT… with `ICCLCONT.Cont_type?` C/P, or CE/PE
in `ITC.DESCR`); Symbol `ICCLCONT.AssetCd`, else `ITC.NSE_ITEMCD` /
`MCX_ITEMCD`; Expiry `ITC.EXP_DATE`; Strike `ITC.CONT_RATE`. Check
open-positions: the same count on the server (`HAVING SUM(QTY) <> 0`).

### BROKERAGE-CLIENT-RATES (`NSQBRKG`, `NSDBRKG`, `OTHBRKG`)

For every date on which any of a client's rows takes effect, a version
with the latest row of each line in force on that date.

| SharePro | Ledgerline |
|---|---|
| `NSQBRKG` row (square-off) in a cash CO | CM-INTRA |
| `NSDBRKG` row (delivery) in a cash CO | CM-DEL |
| derivative CO, `ITEMCD` ALL | FO/CD/COM-FUT and -OPT |
| ALLFUT, ALLOPT | -FUT, -OPT |
| ALLFID / ALLFST / ALLOID / ALLSTK? | FUT index / FUT stock / OPT index / OPT stock |
| ALLAUC, ALLROL | Case auction, rollover |
| other `ITEMCD` | cash: Scrip = ISIN (`ISINMAST`) or symbol (`ITC`); derivatives: Underlying (`ITC.NSE_ITEMCD`) |
| `UNDITEMCD` | Underlying |
| `NSDBRKG` in derivatives, `ATWHAT` "… Assignm/Excercise/Expiry", "Delivery" | Case assignment / exercise / expiry / physical |
| `SQBRPTGE`, `DLBRPTGE` > 0 | Mode pct, Pct |
| `SQBRMNRT`, `DLBRKGMIN` with `SQBRMNPER`, `DLBRMNPER` ("", Per Share, Instrument → MinPerShare; Lot → MinPerLot; Order → Min) | minimums |
| `[OF]` MktRate | Basis market |
| `SQBRFLRT`, `DLBRKGFLT` with `SQFLATPER`, `DLFLATPER` (Lot → per_lot; Order → flat; else per_share) | flat rates |
| no rate | zero |
| `OTHBRKG.ORD_MINBRK`, `CNT_MINBRK`, `MINLOTBRKG` (by CO, `EffectDate`) | Min, MinPerNote, MinPerLot on that CO's lines |
| `OTHBRKG.DEFA_BRKG?` (another client whose rates apply) | that client's rows copied |
| Exchange | the CO's exchange |
| `PRODUCTCD` | Note |

Not converted (warning per row): slab rates (`SLABCD`, `DLSLABCD`),
baskets (`OTHBRKG.brkgbasket`), unknown `ALL…` groups, items not in the
item master; sub-broker sharing (`NSBQBRKG`, `NSBDBRKG`). Every line is
checked with Ledgerline's own rate-key and rule validation before it is
written. BROKERAGE-CARDS, BROKERAGE-CLIENTS and SCRIP-GROUPS are not
written: SharePro has no shared cards (`BROKSLAB`, `BRSLAB` are slabs) and
`DLGROUP` holds client groups, not scrip groups.

### CLIENT-INTEREST (`INTERESTDTL`)

From `EffectDate`; Rate `InterestPer` (> 0); Free Y `NoInterest`;
GraceDays `cGraceDays` when a number ("Default" is the firm's). Rows with
none of them are the firm's terms (a row ending own terms when it follows
them). `DLMAST.NoInterest` with no `INTERESTDTL` row → Free from the year
start. `SegmentType` is reported (Ledgerline's terms are for all segments).

### KRA-STATUS, CKYC-NUMBERS (`KYCMAST` ⋈ latest `KYCDTL` on `KYCCD`, by `KYCDTL.PANNO`)

Status from `KYCMAST.STATUS?`: UPLOADED, MIGRATED, REGISTERED → Registered;
VALIDATED → Validated; ON HOLD, REJECTED, PENDING mapped; DRAFT and others
not written. CKYCNumber `KYCMAST.KYCNO?` when it is 14 digits.

## History (each company year in `Plan.Years`)

All files streamed, ordered by date. Older years: tables that are missing
are skipped with an Info issue; columns that are missing come as empty.

| File | From |
|---|---|
| LEDGER | `JVFILE` (VoucherType "Journal" + `TYPE`, VoucherNo `TT/VRNO`, account as in OPENING-BALANCES, `NARR`, `AMTDR`, `AMTCR`) and `BCFILE` (Receipt/Payment/Contra/Cash by `TYPE`, `P_R`; `CHEQNO`, `VALUEDT`) with the bank or cash book's own side, which `BCFILE` does not store as a line, on the head whose `MSFILE.TT`/`MASTMSFILE.TT` is the voucher's `TT?`. SourceRef table:CO:TT/VRNO:IDN. An unbalanced year is a warning. |
| BILLS | `SETTNET` per client and settlement: BillNo CO-`cntrno` (or `stno sttype/subcd`), `Turnover`, `Brokerage`, `STTax`, `Trancharge`+`Clrcharge`, `SebiChg`, `BNoteStamp`, `CGST`+`SGST`+`IGST`+`UTGST`+`ServiceTax`, other charges (`DematCh`, `DeliveryCh`, `Penalty`, `AddlBrkg`, `RmsCharge`, `IPFT`, `OthCharge`), `NetRecPay` |
| TRADES | `TRFILE` with `ITC` (symbol, series, instrument, expiry, strike), `ISINMAST` (ISIN), `ICCLCONT` (option type); Side by the sign of `QTY?`; Price `ACT_RATE` else `RATE`; Brokerage `BRKG? × |QTY|`; BillNo `CNTRNO` |
| CONTRACT-NOTES | distinct `TRFILE` (`CO`, `CNTRNO`, `TRDT`, `SUBCD`). SentOn, SentTo, Signed and DocumentID are empty: the mail archives are read after the company databases; Ledgerline can link the archived contract notes (Type Contract note, UCC, date) to the register. |
| RECEIPTS-PAYMENTS | `BCFILE` rows with `P_R` R or P; Mode Cash (`TYPE` C), Cheque (`CHEQNO`), Online (`Gateway`), else bank transfer; Bank = the book's head name; Reference `BankRef`/`TXNNO`/`EcsRef` |
| DP-TRANSACTIONS | `DPTRAN` of the DP COs; BOID `FBENACNUM`; UCC via `CLLIST`; Security `ISINLIST.ISINSHORT`; TxnType `TRANCODE`; Quantity `QTYBOOKED`, − for `DRCR` D; Reference `TXNID`/`DRFNO`/`INTERNALRF`; Counterparty `COUNTERDP`+`COUNTERBEN` or `CNTRCMBPID` |
| DP-BILLS | `INVOICEHEAD` with `AutoType` like CDSL/NSDL/DPBILL: `InvoiceNo`, `InvoiceDt`, `SubCode` (BO account or client), `GrossAmt`, GST parts, `NetAmt`. Period from/to empty. |
| DP-HOLDINGS-YEAR-END | `HOLD` at the year's last `HOLDDATE`: Free `CURBAL?`, Pledged `PLDGBAL`, Locked `LOCINBAL`+`FRZBAL`, Total `BALANCE`, Rate `RATE?` (valuation price), Value |

## Documents (mail databases in `Plan.Years`)

One document per attachment (`<mail>_ATTACHMENT`, joined to the mail on
`slno`), read one row at a time:

- the kept copy is chosen on the server: the signed copy (`Signed`) when it
  starts with `%PDF` (Signed = Y, pdf), else the file as sent (`contents`);
  an attachment over 100 MB is not read and is reported;
- Type from `ReportCat`, `subject`, file name: Contract → Contract note;
  Margin2Clients, Obligation Mrgn → Margin statement; DP CLIENT mails,
  DPBLPRN… → DP statement / DP bill; Vallan Bills → Bill; A/C Ledger,
  RunAcRep2Client, Client P/L, Net Position, StandardUpload? → Statement;
  reKYC → KYC; Payout Report and others → Other;
- UCC `id` for Client mails; for DP CLIENT mails the DP account is turned
  into the trading code through the newest company database's `CLLIST`;
- Date `date`; FY the database's (or the date's); Subject `subject`; To
  `to`; SourceRef `<table>:slno=…,attnumber=…`; RetainUntil date + 8 years;
- mails with no attachment are counted (Info), not archived;
- `SCANIMAGE` (in older mail databases): scanned images, Type KYC when the
  category (`IMAGECAT.Descr`) says KYC.

Documents go to the first chosen firm.

## Never migrated

| Field | Why |
|---|---|
| `DLMAST.Web_Pwd`, `CLLIST.Web_Pwd`, `SBMAST.Web_Pwd`, `RMMAST.Web_Pwd` | passwords: counted with `COUNT(...)`, never selected; users set new ones |
| `CLLIST.FHOLDER_ADHAR`, `SHOLDER_ADHAR`, `THOLDER_ADHAR`, `CDNOMREG.UID` | full Aadhaar numbers: counted, never selected (Ledgerline keeps no client Aadhaar) |
| staff passwords (`SHUSER`), `SYSTEMLOGFILES.Content`, OTP and token tables | never read |
| Address, PIN, gender, occupation, sub-broker/AP (`SBCODE`), RM (`RMCODE`), group (`GROUPCD`), family (`FAMILYCD`) | no CLIENT-MASTER column yet: counted as dropped |
| FATCA/NRI details, POA/DDPI (`CLPOALIST`, `BOMAST`) | no import file |
| branches, sub-brokers, RMs (masters), client groups (`DLGROUP`) | no import file |
| DP tariff (`DPCLBSKT`, `DPCHARGE`, `DPCLBASKET`, `DPTRTYPE`) | no import file: counted as dropped |
| online account opening and re-KYC documents (`EKYC_documents`, `REKYC_*`) | not read (identity documents; to be handled with the KYC records) |
| DP-only clients (DP accounts with no trading code) | no DP master import yet: reported |
