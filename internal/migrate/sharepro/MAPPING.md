# SharePro to Ledgerline: where each column comes from

SharePro (StanSoft) keeps, on one SQL Server:

- an **admin database** (`COLIST`, `ACC_YEAR`, `BRANCHES`, users, SMS);
- one **company database per financial year**, named `<prefix><yy><yy+1>`
  (the prefix is the broker's own); inside it `CO` is SharePro's
  "company": one exchange segment (or the DP, PMS, NBFC) of a legal entity;
- **mail archives** (`SSMAIL2`/`SSMAIL2_ATTACHMENT` with the archive copy
  `XSSMAIL2`; older `SSMAIL`/`SSMAIL_ATTACHMENT`), per year or spanning
  several years.

Only table and column names appear here. Each mapping is marked
**[verified]** (checked with aggregate queries against a real SharePro
database) or **[assumed]** (read from names and counts; check on a real
database first).

## Classify

| Kind | Test | FY |
|---|---|---|
| admin (order 0) | tables `COLIST` and `ACC_YEAR` | – |
| company (order 10–49, newest first) | tables `DLMAST`, `JVFILE` and `ACCTOT` or `MLFILE` | `<letter><yy><yy+1>` in the database name; else the year with most `JVFILE.VRDT` |
| mail (order 50–99) | `SSMAIL2`+`SSMAIL2_ATTACHMENT` (or `XSSMAIL2`, `SSMAIL`, `XSSMAIL`) with `contents` | name as above; else `MIN/MAX(date)` if in one year; else empty (several years) |
| other | anything else (skipped) | – |

The admin database also has (empty) `SSMAIL` tables: it is tested first.

## Firms

`COLIST.CO, DESCR, SYSTEM, SEBI_REG, Inactive, CDSL_BEN, NSDL_BEN` and
`ACC_YEAR.CO, ACC_YEAR`. COs are grouped into one firm per legal entity by
`DESCR` cut at the first "(", without dots and with spaces collapsed
("X PVT. LTD.", "X PVT. LTD", "X PVT. LTD (CDSL)", "X PVT. LTD(C - MCX)",
"X PVT. LTD.(SPOT)" are one firm) [verified]. Firm code = the lowest CO;
name = the first active CO's `DESCR` before the bracket. A firm all of whose
COs are `Inactive` gets "(inactive in SharePro)" after its name. A CO's
segment: `TRFILE.Segment` (most trades) if it has trades, else
`COLIST.SYSTEM` (NSE, NFO, BSE, CDSL, MCFO...) [verified], else the bracketed
tag of `DESCR`, else `SEBI_REG` `IN-DP` / `CDSL_BEN` → DP.
`COLIST.ACC_MERGEID` (companies sharing books) is not used. Without the
admin database every CO in the company database is one firm.

Clients: `DLMAST` is shared by all COs. A client belongs to each firm in
whose COs it has a `DLMAST_REG2` status or a `CLLIST` DP account (over all
SharePro's firms, so choosing one firm never takes another's clients);
clients with neither go to the first firm.

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
| RAS | `DLRUNACMAST.SETTLETYPE` of the latest `ACC_YEAR`: 2 Quarterly, 3 Monthly [assumed: values 1, 2, 3 seen; 2 is most clients]; 1 and others blank, counted in an Info issue — **to be confirmed** |
| Bank, IFSC, Account | the client's `BKMAST` row of `TYPE` Primary [verified], else the one whose `ACNO` is `DLMAST.BANKACNUM`, else the first open row; bank `BANKMAST.DESCR`, IFSC `BANKMAST.RTGS` (an IFSC for most banks [verified]; others are reported) |
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
| AP | `SBCODE` (sub-broker / authorised person) [verified: filled for about half the clients] |
| RM | `RMCODE`, else `DLRCODE` (dealer) [verified: DLRCODE filled for about a quarter] |
| Groups | `GROUPCD`, then each type's latest `DLGROUP.Code` (by `EffectDate`), `;`-separated [assumed: DLGROUP holds client group codes] |
| Inactive, InactiveSince | Y and the latest status date for a client with no Active/Reactivated segment in the firm (or, with no status rows, a `CloseOutDt`) |

AP, RM, Groups, Inactive and InactiveSince are not read by the import on
this branch yet.

Dormant clients: `Plan.DormantMode` "inactive" (default) writes them with
Inactive = Y; "skip" leaves them out of every client file (and their DP
accounts), keeps their balances in OPENING-BALANCES and raises an Error
for any balance, holding or position they have.

Every row is checked against the import's rules (UCC, name, PAN, mobile,
email, city, segment, bank, IFSC, account) and each failure is an Error
issue with the client code: the import rejects the whole file on one bad
row.

### UCC-DETAILS (`DLMAST` ⋈ `DLAGREE` on `Subcd`; KYC register via `DLAGREE.KycCd` or `KYCDTL.PANNO`)

| Column | From |
|---|---|
| Address1–3, City, PIN | `DLMAST.ADD1`–`ADD3`, `CITY`, `PIN` [verified: the correspondence address]; when blank, the latest `KYCADD` row of the client's KYC whose `TYPE`/`ADDTYPE` is not permanent [assumed] |
| State | `STATES.DESCR` of `DLMAST.STATECD`, else `STATE`, `DLAGREE.CorState` (as a name) |
| Country | `DLMAST.COUNTRY`, else `DLAGREE.CorCountry` |
| PermAddress1–3, PermCity, PermPIN | `DLMAST.RES_ADD1`–`RES_ADD3`, `RES_CITY`, `RES_PIN` [verified]; when blank, `KYCADD` with `TYPE`/`ADDTYPE` containing PER [assumed] |
| PermState, PermCountry | `DLAGREE.PermState` (as a state name), `PermCountry` [assumed] |
| Category | `DLMAST.CLIENTCAT` [assumed] |
| Gender | `DLMAST.GENDER`, else `KYCDTL.GENDER` (M/F [verified]) → Male/Female/Transgender |
| Guardian | `DLMAST.FATHERNM` [verified], else `KYCDTL` father's (`FA*NAME`), else spouse's (`SP*NAME`) name |
| Marital | blank: `KYCDTL.MARITAL` holds 1, 2, 01, 02 whose meaning is not confirmed; an Info issue asks to confirm |
| Nationality | `DLAGREE.Nation` (01 Indian, 02 `NationOth`), else `KYCDTL.COCN` [assumed] |
| Occupation | `DLMAST.code_Occupation` when an exchange code 1–9 or 99 [assumed]; else `KYCDTL.OCCUP_CODE` as words (CKYC codes S-01 Private Sector Service … X-01 Not categorised [verified]); else `DLMAST.OCCUPATION` |
| OccupationDetail | `DLAGREE.OccupDtlOth` / `OccupDtl`, else the CKYC code when Occupation came from it |
| IncomeAsOn | `DLAGREE.IncomeDt` [assumed] |
| PEP | `DLAGREE.PepConn`: PEP, RPEP, Y, 1 → Y; other values N [assumed] |
| IPV | Y when `DLAGREE.IpvDt` is set [assumed] |
| POAFunds | Y when an open `BKMAST` row has `POA_RECVD` [assumed] |
| POASecurities | Y when a `CLPOALIST` row of the client's DP accounts has no `PoaDeacDt` [assumed] |
| AgreementDate | `DLAGREE.ApplDt`, else `DLMAST.COMMNCEDT` [assumed] |
| CIN | `DLMAST.REG_NO` when it has the shape of a CIN [assumed] |
| BankType | the primary bank account's type (`BKMAST.ACTYPE`: SB, CA, NRE, NRO) |
| UPI, MobileRelation, EmailRelation | blank: SharePro does not hold them (Info issue) |

Missing Address1, City, State or PIN is an Error per client; a PIN that is
not 6 digits a Warning.

### UCC-CONTACTS (non-individual clients only)

`DLMAST` slots [verified]: (`CONTACT`, `DESIGNATN`?, `C_PAN1`, `C_ADD1`,
`C_PHONE1`, `C_EMAIL1`), (`CONTACT2`, `C_DESIG2`, `C_PAN2`, …), (`CONTACT3`,
`C_DESIG3`, …); `DLAGREE.DinCont1`–`3` (DIN), `C_City1`–`3`, `C_Pin1`–`3`
(added to the address) [assumed]. Designation defaults to "Authorised
signatory". Also `HUFCOPER.CoPerName` (Karta when `KRTAREL` says so, else
Coparcener) and `CLAUTHMAST.AuthPerson` ("Authorised signatory (DP)", with
`MOBILE`, `EMAIL`) via the DP account's trading code [assumed]. Without
any of these tables the file is not written (Info issue).

### BRANCHES (admin `BRANCHES`; a company database's own `BRANCHES` if it has one)

Code `BranchId`; Name `BranchName`; City `CITY`; Manager `Contact1`; Parent
`PBranchId`; Kind Zone/Region/Franchise from the name, else Branch
[assumed: `BranchType` codes are not known]; Active N when `Dt_Close` is
set or `Status` says closed/inactive/suspended. Without the admin
database: the client master's branch codes, by code only (Warning). Client
branches missing from the master are reported.

### PARTNERS

| Kind | From |
|---|---|
| Authorised person | `SBMAST`: Code `SBCODE`, Name `DESCR`, Branch `BRANCHID`, PAN `PAN_NO`/`pan`, GSTIN, RegNo `SEBIREGNO`, Bank/IFSC `BANKMAST` of `BANKCD`, Account `ACNO`, Email `email`, Active (no `Dt_Close`) |
| Relationship manager | `RMMAST` (`rmcode`, `descr`, `branchid`, `email`, `pan_no`, `Bankcd`, `Acno`, `Dt_Close`) and dealers `DLRMAST` (`DLRCODE`, `DESCR`, `BRANCHID`, `email`, `PAN_NO`, `GSTIN`, `Bankcd`, `Acno`, `Dt_Close`) — the client master's RM is the RM or the dealer |

Passwords (`Web_Pwd`) are never selected. SharePct is blank: SharePro
keeps the share per client (`DLMAST.SBROK_PER`). Client AP/RM codes not in
the masters are reported. A code used by two kinds is written once.

### DP-ACCOUNTS (`CLLIST` of the firm's DP companies)

UCC `SUBCODE`, or for DP-only accounts the DP client code `BENACNUM` (Info
count); BOID `FBENACNUM` (or `DPID`+`BENACNUM`); Depository CDSL (NSDL for
IN… BOIDs); Status Closed (`BENCLOSEDT` set or `BENSTAT` 3), Frozen
(`SUSPFLAG` D/C/B or `BENSTAT` 2), else Active; Opened `ACTIVEDT`;
ClosedOn `BENCLOSEDT`; BSDA `BSDA` Y/N; Freeze `SUSPFLAG` D Debit, C
Credit, B/S/F/Y Both; FreezeReason `FREEZERSN.DESCR` of `SUSPRSNCD`; DDPI Y
when an active `CLPOALIST` row's `PoaType` says DDPI [all assumed except
BENSTAT 3 = closed]. `FHOLDER_ADHAR`, `SHOLDER_ADHAR`, `THOLDER_ADHAR` and
`Web_Pwd` are counted, never selected. Check dp-accounts: `COUNT(*)` of
these `CLLIST` rows (without skipped clients').

### CLIENT-BANKS (`BKMAST` ⋈ `BANKMAST` on `BANKCD`)

UCC `BKMAST.SUBCD`; Bank `BANKMAST.DESCR`; IFSC `BANKMAST.RTGS`; Account
`BKMAST.ACNO`; Primary `TYPE` Primary [verified]; Status `Closed` →
Inactive [verified]; `TYPE` Pool rows are the firm's own and left out
[verified]; Branch `BANKMAST.BRANCH`; MICR `BANKMAST.MICRNO`; Type
`BKMAST.ACTYPE` (SB/CA/NRE/NRO); AddedOn `BKMAST.ADD_DTTIME`. Clients with
no `BKMAST` row get the client master's account (warning: no IFSC).

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

### OPENING-BALANCES

Balance at the as-of date = opening + movements [verified]:

- opening of a sub-account: `MLFILE.AMTDROB - AMTCROB` (one row per
  `BranchID`, added up) for (`co`, `ACCD`, `SUBCODE`);
- opening of a head with no `MLFILE` rows: `MSFILE.AMTDROB - AMTCROB`
  (a head with sub-accounts carries their sum in `MSFILE`: never both);
- movements: `ACCTOT.amtdr - amtcr` with `vrdt <= as-of` (daily totals; no
  opening row). `MLFILE.INOPBAL` and `MSFILE.SUBLEDGER` are not used.

Account codes:

- client-coded heads [verified]: per company, a head whose `MLFILE`
  sub-accounts are (mostly) client codes (`DLMAST.SUBCD`); `DLMAST.ACC_ACCD`
  is blank and not used.
  - a client-coded head whose `MSFILE.DESCR` contains CLIENT and not
    MARGIN, CONTROL or ADDBACK is the client's ledger → `CLIENT:<SUBCD>`,
    one account over the firm's companies;
  - every other client-coded head (margin deposits, early pay-in, per-client
    STT and stamp duty, members, …) → one firm account for the head,
    `<PREFIX>:<CO>-<ACCD>`, with a Warning per head (clients with a balance,
    their debits and credits) asking the accountant to decide;
  - every client-coded head is listed in the report (Info for merged ones);
- any other head or sub-account — `<PREFIX>:<CO>-<ACCD>[-<SUBCODE>]`,
  PREFIX from the head's group (`MASTMSFILE.AgrCode` up the `ACCTGROUP`
  tree, `PAgrCode`): CASHINHAND → BANK (with `BANKCD` or "BANK" in the
  name) or CASH; `MSACTYPES.lExchange` for `MASTMSFILE.ACCTTYPE` or
  "EXCHANGE"/"CLEARING" → CC; CAPITAL/RESERVE → EQUITY; LOAN → LOAN;
  SDEBTORS → RECEIVABLE; fixed assets, investments, deposits, advances,
  stock, current assets → ASSET; creditors, duties and taxes, provisions,
  current liabilities → PAYABLE; INCOME → INCOME; EXPENDITURE → EXPENSE;
  otherwise SUSPENSE (warning per head) [assumed].

Name: `MSFILE.DESCR` / `MLFILE.DESCR`. Zero balances are left out.

**SharePro's balances do not always balance** [verified: the real data
nets to a non-zero amount per company and overall; P&L heads carry
openings, so a year was not fully closed]. The difference goes to one line
`SUSPENSE:OPENING-DIFFERENCE` ("Difference in opening balances carried
from SharePro"), with a Warning giving the amount and the split by company,
and the check `opening-difference` (the account's balance, debit positive).

Since (client debits): the year's lines of the client ledger heads
(`JVFILE` and `BCFILE`, from the year's start to the as-of date) netted per
day, first in first out — each credit pays the oldest debit — starting from
the year's opening balance (balance minus the year's movement) dated the
year's first day. A debit already due at the year start keeps the year's
first day, counted in an Info issue: `LY_ACCTOT` is incomplete [verified]
and is not used.

Checks, all summed on the server from the same openings and movements,
client ledgers merged by the same rule (pairs of company and head):
trial-balance-debit and trial-balance-credit (including the difference
line), opening-difference, client-debit-total and client-credit-total
(merged client heads only).

### DP-HOLDINGS (`HOLD` at `MAX(HOLDDATE) <= as-of`, the firm's DP COs)

UCC `CLLIST.SUBCODE` of (`CO`, `BENACNUM`); ISIN `ISINCD`; Symbol
`ITC.NSE_ITEMCD` / `BSE_ITEMCD` via `ISINMAST.ITEMCD`; Qty `SUM(BALANCE)`
[verified: BALANCE is the total]; Pledged `SUM(PLDGBAL)`; AvgCost empty.
One row per client and ISIN. DP accounts with no trading code are reported
(they are in DP-ACCOUNTS). Check dp-holding-quantity: `SUM(HOLD.BALANCE)` of
the migrated clients' accounts.

### HOLDING-LOTS

Not written: SharePro holds no purchase lots or cost for DP holdings.

### POSITIONS (`TRFILE` of FO, CD and COM COs, `TRDT <= as-of`, contract `ITC.EXP_DATE >= as-of`)

Net `QTY` (signed: minus = sell [verified]) per client, CO and `ITEMCD`;
open quantity and average price by FIFO over `ACT_RATE` (the trade price
[verified]; `RATE` is ACT_RATE ± BRKG), else `RATE`; Type from
`ITC.INSTR_TYPE` (FUT…, OPT…), else `ICCLCONT.ProductTp` (SF/IF futures,
SO/IO options [verified]); call or put from `ITC.NSE_SERIES`,
`ICCLCONT.Nse_series`, `ContDescr`, `ITC.DESCR` or `Bfo_SrsCd` (CE/PE)
[assumed] — `ICCLCONT.Cont_type` is M/W, not call/put [verified]; an
option whose type cannot be told is left out with a Warning. Symbol
`ICCLCONT.AssetCd`, else `ITC.NSE_ITEMCD` / `MCX_ITEMCD`; Expiry
`ITC.EXP_DATE`; Strike `ITC.CONT_RATE`. Check open-positions: the same
count on the server (`HAVING SUM(QTY) <> 0`).

### BROKERAGE-CLIENT-RATES (`NSQBRKG`, `NSDBRKG`, `OTHBRKG`) [assumed]

For every date on which any of a client's rows takes effect, a version
with the latest row of each line in force on that date.

| SharePro | Ledgerline |
|---|---|
| `NSQBRKG` row (square-off) in a cash CO | CM-INTRA |
| `NSDBRKG` row (delivery) in a cash CO | CM-DEL |
| derivative CO, `ITEMCD` ALL | FO/CD/COM-FUT and -OPT |
| ALLFUT, ALLOPT | -FUT, -OPT |
| ALLFID / ALLFST / ALLOID / ALLSTK | FUT index / FUT stock / OPT index / OPT stock |
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
| `OTHBRKG.DEFA_BRKG` (another client whose rates apply) | that client's rows copied |
| Exchange | the CO's exchange |
| `PRODUCTCD` | Note |

Not converted (warning per row): slab rates, baskets
(`OTHBRKG.brkgbasket`), unknown `ALL…` groups, items not in the item
master; sub-broker sharing (`NSBQBRKG`, `NSBDBRKG`). Every line is checked
with Ledgerline's own rate-key and rule validation. BROKERAGE-CARDS,
BROKERAGE-CLIENTS and SCRIP-GROUPS are not written: SharePro has no shared
cards and `DLGROUP` holds client groups.

### CLIENT-INTEREST (`INTERESTDTL`) [verified]

From `EffectDate`; Rate `InterestPer` (> 0; 0 is the firm's rate); Free Y
`NoInterest`; GraceDays `cGraceDays` when a number ("Default" or blank is
the firm's). Rows with none of them are the firm's terms (a row ending own
terms when it follows them). `DLMAST.NoInterest` with no `INTERESTDTL` row
→ Free from the year start. `SegmentType` is reported.

### KRA-STATUS, CKYC-NUMBERS (`KYCMAST` ⋈ latest `KYCDTL` on `KYCCD`, by `KYCDTL.PANNO`)

Status from `KYCMAST.STATUS`: UPLOADED, MIGRATED, REGISTERED → Registered;
VALIDATED → Validated; ON HOLD, REJECTED, PENDING mapped; DRAFT and others
not written [assumed]. CKYCNumber `KYCMAST.KYCNO` when 14 digits
[verified].

## History (each company year in `Plan.Years`)

All files streamed, ordered by date. Older years: tables that are missing
are skipped with an Info issue; columns that are missing come as empty.

| File | From |
|---|---|
| LEDGER | `JVFILE` (VoucherType "Journal" + `TYPE`, VoucherNo `TT/VRNO`, account as in OPENING-BALANCES, UCC the client of any client-coded head, `NARR`, `AMTDR`, `AMTCR`) and `BCFILE` (Receipt/Payment/Contra/Cash by `TYPE`, `P_R`; `CHEQNO`, `VALUEDT`) with the bank or cash book's own side, which `BCFILE` does not store as a line, on the head whose `MSFILE.TT`/`MASTMSFILE.TT` is the voucher's `TT` [assumed]. SourceRef table:CO:TT/VRNO:IDN. An unbalanced year is a warning. |
| BILLS | `SETTNET` per client and settlement (not `INVOICEHEAD`, whose `CONTRACT` rows are the GST invoices of contract notes): BillNo CO-`cntrno` (or `stno sttype/subcd`), `Turnover`, `Brokerage`, `STTax`, `Trancharge`+`Clrcharge`, `SebiChg`, `BNoteStamp`, `CGST`+`SGST`+`IGST`+`UTGST`+`ServiceTax`, other charges (`DematCh`, `DeliveryCh`, `Penalty`, `AddlBrkg`, `RmsCharge`, `IPFT`, `OthCharge`), `NetRecPay` [assumed] |
| TRADES | `TRFILE` with `ITC`, `ISINMAST`, `ICCLCONT` as for positions; Side by the sign of `QTY` [verified]; Price `ACT_RATE` [verified] else `RATE`; Brokerage `BRKG × |QTY|` (BRKG per unit [verified]); BillNo `CNTRNO`; OptionType blank (Info count) when call/put cannot be told |
| CONTRACT-NOTES | distinct `TRFILE` (`CO`, `CNTRNO`, `TRDT`, `SUBCD`). SentOn, SentTo, Signed and DocumentID are empty: the mail archives are read after the company databases, so the register cannot be linked here. |
| RECEIPTS-PAYMENTS | `BCFILE` rows with `P_R` R or P; Mode Cash (`TYPE` C), Cheque (`CHEQNO`), Online (`Gateway`), else bank transfer; Bank = the book's head name; Reference `BankRef`/`TXNNO`/`EcsRef` |
| DP-TRANSACTIONS | `DPTRAN` of the DP COs; BOID `FBENACNUM`; UCC via `CLLIST`; Security `ISINLIST.ISINSHORT`; TxnType `TRANCODE`; Quantity `QTYBOOKED`, − for `DRCR` D; Reference `TXNID`/`DRFNO`/`INTERNALRF`; Counterparty `COUNTERDP`+`COUNTERBEN` or `CNTRCMBPID` |
| DP-BILLS | `INVOICEHEAD` with `AutoType` CDSLBILL (also NSDLBILL, DPBILL) [verified]: `InvoiceNo`, `InvoiceDt`, `SubCode` (BO account or client), `GrossAmt`, GST parts, `NetAmt`. Period from/to empty. |
| DP-HOLDINGS-YEAR-END | `HOLD` at the year's last `HOLDDATE`: Free `CURBAL`, Pledged `PLDGBAL`, Locked `BALANCE − CURBAL − PLDGBAL`, Total `BALANCE` [verified]; Rate the latest `DPPRICE.RATE` with `CLOSEDT` on or before the date for the ISIN (`HOLD.RATE` is never filled [verified]); Rate and Value empty without a price |

## Documents (mail databases in `Plan.Years`) [verified unless marked]

One document per attachment (`<mail>_ATTACHMENT` joined to the mail on
`slno`), read one row at a time; `XSSMAIL2` (the archive copy, same
layout) is read too when it has rows, skipping attachments (`slno`,
`attnumber`) already read from `SSMAIL2`:

- the kept copy is chosen on the server: the signed copy (`Signed`) when it
  starts with `%PDF` (Signed = Y, pdf), else the file as sent (`contents`):
  HTML as html, PDF, xlsx, jpg as they are; both are text columns, read as
  their bytes (`CAST(CAST(… AS varchar(max)) AS varbinary(max))`); an
  attachment over 100 MB is not read and is reported;
- Type from `ReportCat`: Contract → Contract note; Margin2Clients,
  Obligation Mrgn → Margin statement; StandardUpload, RunAcRep2Client,
  A/C Ledger, Client P/L, Net Position, Trade Register → Statement; Vallan
  Bills → Bill; DPBLPRN2 → DP bill; blank with `idtype` DP CLIENT → DP
  statement; reKYC → KYC; anything else → Other;
- `idtype` (any case): Client → UCC `id`; DP CLIENT → `id` is the DP
  account, turned into the trading code through the newest company
  database's `CLLIST`; Branchid, Group, SubBroker → the firm's records (Type
  Other, no UCC: payout reports of several clients, never shown to a
  client);
- firm: the mail's company (`CO`) if the mail table has one (it does not in
  the database checked), else the client's firm, else the DP account's,
  else the first firm (Info count); documents of firms not chosen are left
  out (Info count);
- Date `date`; FY the database's (or the date's); Subject `subject`; To
  `to`; SourceRef `<table>:slno=…,attnumber=…;ReportCat=…`; RetainUntil
  date + 8 years;
- mails with no attachment are counted per `ReportCat` (Info), not archived;
- `SCANIMAGE` (in older mail databases): scanned images, Type KYC when the
  category (`IMAGECAT.Descr`) says KYC [assumed].

## Never migrated

| Field | Why |
|---|---|
| `DLMAST.Web_Pwd`, `CLLIST.Web_Pwd`, `SBMAST.Web_Pwd`, `RMMAST.Web_Pwd`, `DLRMAST.web_Pwd` | passwords: counted with `COUNT(...)`, never selected; users set new ones |
| `CLLIST.FHOLDER_ADHAR`, `SHOLDER_ADHAR`, `THOLDER_ADHAR`, `CDNOMREG.UID`, `DLAGREE.UID`, `DLAGREE.NomineeAdhar`, `DLAGREE2.C_UID1`–`3`, `CLAUTHMAST.UID`, `BOMAST.FHUID` | full Aadhaar numbers: counted, never selected (Ledgerline keeps no client Aadhaar) |
| staff passwords (`SHUSER`), `SYSTEMLOGFILES.Content`, OTP and token tables | never read |
| Family (`DLMAST.FAMILYCD`) | no Ledgerline column: counted as dropped |
| FATCA details | no import file |
| DP tariff (`DPCLBSKT`, `DPCHARGE`, `DPCLBASKET`, `DPTRTYPE`) | no import file: counted as dropped |
| online account opening and re-KYC documents (`EKYC_documents`, `REKYC_*`) | not read (identity documents; to be handled with the KYC records) |
