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
clients with neither go to the first firm. A DP-only client (below)
belongs to the firm whose company holds its DP account (the CO with
`SYSTEM` CDSL).

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
| Depository, DematID | the client's first open `TYPE` Primary row of the client demat master `CDMAST` (account ID as in CLIENT-DEMATS) [verified: `TYPE` Primary, Secondary, Pool]; else the client's oldest open account at the firm's DP in `CLLIST` (by `ACTIVEDT`): `FBENACNUM` (or `DPID`+`BENACNUM`); else another open `CDMAST` row. CDSL, or NSDL when it starts with IN. A primary account at another DP (not in `CLLIST`) is counted (Info) |
| DPOnly | N (Y for DP-only clients, below) |
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

DPOnly is not read by the import on this branch yet (being added, with
several DP accounts per client and DP-only clients); the header test
skips the columns `ops.MasterSources` does not describe yet
(`optionalNotYetRead`). The DP-only clients' rows (below) follow the
trading clients'.

Dormant clients: `Plan.DormantMode` "inactive" (default) writes them with
Inactive = Y; "skip" leaves them out of every client file (and their DP
accounts), keeps their balances in OPENING-BALANCES and raises an Error
for any balance, holding or position they have.

Every row is checked against the import's rules (UCC, name, PAN, mobile,
email, city, segment, bank, IFSC, account) and each failure is an Error
issue with the client code: the import rejects the whole file on one bad
row. An inactive client's failures (except the code and name) are
Warnings: Ledgerline keeps the record as it is, marked incomplete.

Check clients: `COUNT` of the firm's `DLMAST` clients (as assigned above;
dormant ones left out in mode skip) plus `COUNT` of the firm's `CLLIST`
accounts with no trading client (`NOT EXISTS` a `DLMAST.SUBCD` =
`SUBCODE`, nor one = `TRADINGID`, nor a `CDMAST` row of the account with
a `DLMAST` client whose `PAN_NO` is `ITPAN`; closed ones left out in mode
skip).

### DP-only clients (`CLLIST` accounts with no trading client)

A demat account holder of the firm's DP with no trading account: a
`CLLIST` account whose `SUBCODE` is blank or is not a client in `DLMAST`
[verified: about half of the DP master's accounts, nearly all closed
ones among them]. The link is `SUBCODE`; an account with no client in
`SUBCODE` whose `TRADINGID` is a client in `DLMAST` is that trading
client's (not DP-only; Info count) [verified: a few accounts with no
`SUBCODE` have `TRADINGID` set]. Else an account that a trading client
lists as a demat account in `CDMAST` (the account's BO ID, Pool rows left
out) and whose `DLMAST.PAN_NO` is the first holder's `CLLIST.ITPAN` is
that client's, the smallest code when the person has several (Info count;
several codes a Warning) [verified: most accounts with no `SUBCODE`,
nearly all closed, are listed so by their holder's trading code]. An
account whose `SUBCODE` is not a
client is counted (Info). Each account is a client of its own in every client file,
`DPOnly` = Y, under the DP client code `BENACNUM` (8 digits, a valid
UCC); when a trading client has that code or it is not 3–10 letters or
digits, `DP` + the code (else the CO + the code), with a Warning; an
account for which no code can be made is an Error and is left out. The
same code is the client's in DP-ACCOUNTS, DP-HOLDINGS, the DP history and
the DP documents. Columns verified to exist; their meaning is assumed
unless marked.

| CLIENT-MASTER | From |
|---|---|
| UCC | `BENACNUM` (see above) |
| Name | `BEN1HOLDER` |
| Type | 4th character of `ITPAN` as for trading clients (NRI when `BENTYPE` 3 or `BEN1CNTRY` is not India); without a PAN, `BENTYPE` (CDSL product number: 1 Individual, 3 NRI, 8 HUF, others Corporate) |
| PAN, DOB | `ITPAN` (first holder), `DOB` |
| Mobile | `FHOLDER_MOBILE` (digits; +91/0 removed) |
| Email | `FHOLDER_EMAIL`, else `CUSTEMAIL` |
| City | `BEN1CITY`, else `CCITY` |
| Segments, RAS, Income, LastTrade, ECN, AP, RM | blank (Income: `CLLIST_DET.GROSSINCOME` codes are not confirmed; counted, Info) |
| Risk | `CLRISKPROFILE` (LOW/MEDIUM/HIGH, as for trading clients) |
| Bank, IFSC, Account | `BENBNKNAME` (else `BANKMAST.DESCR`), `BANKMAST.RTGS` of `CLLIST.BANKCD`, `BENBNKAC` without spaces and dashes |
| Depository, DematID | CDSL (NSDL for IN…), `FBENACNUM` (or `DPID`+`BENACNUM`) |
| Nominee, NomineeName, NominationDate | "Opted in" and the first `CDNOMREG` nominee (else `CLLIST.NOMFNAME`); earliest nomination date |
| KYCDate | the KYC register's date for the PAN (`KYCMAST`, as for trading clients), else `ACTIVEDT` |
| Status, API | Active, N |
| Branch | `CLLIST.BRANCHID` |
| State | `STATES.DESCR` of `CLLIST.StateCd`, else `BEN1STATE` |
| Groups | `CLLIST.GROUPCD` |
| Inactive, InactiveSince | Y and `BENCLOSEDT` for a closed account (`BENCLOSEDT` set or `BENSTAT` 3) [verified: BENSTAT 3 = closed]; a frozen account (`BENSTAT` 2) is not inactive. Closed accounts come across (the firm keeps their records for 8 years; their history and documents attach to them); dormant mode skip leaves them out like dormant trading clients |
| DPOnly | Y |

The client master's checks apply, as errors for an open account and
warnings for a closed one; no segment is required.

| File | From |
|---|---|
| UCC-DETAILS | Address1–3, City, PIN, Country: `BEN1ADD1`–`BEN1ADD4` (lines 3 and 4 joined), `BEN1CITY`, `BEN1PIN`, `BEN1CNTRY`, else `CADD1`–`CADD4`, `CCITY`, `CPIN`, `CCNTRY`; State as in the client master; PermAddress1–3, PermCity, PermState, PermCountry, PermPIN: `BEN1FADD1`–`3`, `BEN1FCITY`, `BEN1FSTATE`, `BEN1FCNTRY`, `BEN1FPIN` (permanent or foreign address); Gender `BOSEX`, else `CLLIST_DET.SEX`; Guardian `BEN1FATHUB`, else `FATHUSNAME`; Nationality `CLLIST_DET.NATION` (01 Indian, 02 `NATIONOTH`); Occupation `CLLIST_DET.OCCUPN` when a code 1–9 or 99, else `OCCUPNALL` (OccupationDetail `OCCUPNALL` beside a code); PEP `CLLIST_DET.PEP` (as for trading clients); IPV Y when `CLLIST_DET.IPV` is Y or `IPV_DATE`/`IPVDT` is set; AgreementDate `ACTIVEDT`; POASecurities from `CLPOALIST` of the account; CIN `CLLIST_DET.REGNO` when CIN-shaped; BankType `BENBNKTYPE` (10 SB, 11 CA). Marital blank and counted with the trading clients' (`MARITALSTATUS` codes not confirmed). Missing address fields as for trading clients |
| NOMINEES | `CDNOMREG` rows of the account (`CO`, `BENACNUM`, by `NOMSLNO`): Name `FIRSTNM` `MIDNM` `LASTNM` (else `NOMFNAME`); Relation `BOREL` (CDSL codes: 1 Spouse, 2 Son, 3 Daughter, 4 Father, 5 Mother, 6 Brother, 7 Sister, 8 Grandson, 9 Granddaughter, 10 Grandfather, 11 Grandmother, 13 Other; 12 "not provided" blank; words kept); Share `SHAREPR`; IDType PAN and ID `PAN` when a valid PAN (others counted, Info); DOB `NomDob`; Guardian `NOMGRNAME`; Mobile `MOBILE` (0 blank), else `SMobile`; Email `Email`, else `PEMAIL`; Address `ADD1`–`ADD3`, `CITY`, `PIN`; DeclaredOn `NOMACTVDT`, else `ACTIVEDT`. `CDNOMREG.UID` is never selected |
| CLIENT-BANKS | the DP bank account as in the client master, Primary Y, Active, MICR `MICRCODE`, Type `BENBNKTYPE` |
| KRA-STATUS, CKYC-NUMBERS | the KYC register for the PAN as for trading clients; else `CLLIST_DET.KRASTATDES` (words: VALIDATED → Validated, REGISTERED/VERIFIED → Registered, HOLD, REJECT, PROCESS/PENDING/SUBMITTED) and `CLLIST_DET.CKYCNO` when 14 digits |
| UCC-CONTACTS | non-individual DP-only clients: `CLLIST_DET` `Cp1_`–`Cp3_` (`Name`, `Desig`, `Din`, `Pan`, `Add`+`City`+`Pin`, `Phone`, `Email`), and `HUFCOPER`/`CLAUTHMAST` of the account as for trading clients |

`CLLIST_DET.UID`, `FHADHARNOM`, `SHADHARNOM`, `THADHARNOM` are counted,
never selected.

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

Every account of the firm's DP companies, several per client: UCC
`SUBCODE` (the trading client), or the DP-only client's code (above; Info
count); BOID `FBENACNUM` (or `DPID`+`BENACNUM`); Depository CDSL (NSDL for
IN… BOIDs); Status Closed (`BENCLOSEDT` set or `BENSTAT` 3), Frozen
(`SUSPFLAG` D/C/B or `BENSTAT` 2), else Active; Opened `ACTIVEDT`;
ClosedOn `BENCLOSEDT`; BSDA `BSDA` Y/N; Freeze `SUSPFLAG` D Debit, C
Credit, B/S/F/Y Both; FreezeReason `FREEZERSN.DESCR` of `SUSPRSNCD`, else
`BENSTATRSN`, else "Frozen in SharePro (no reason recorded)" with a
Warning (Ledgerline needs a reason for a frozen account); DDPI Y when an
active `CLPOALIST` row's `PoaType` says DDPI; Primary Y on one account per
client: the client master's demat account when it is one of them and
open, else the oldest open one (by `ACTIVEDT`); a client whose accounts
are all closed has none (Primary N); Holder2Name,
Holder2PAN, Holder3Name, Holder3PAN `BEN2HOLDER`, `BEN2PAN`, `BEN3HOLDER`,
`BEN3PAN` (a PAN that is not valid is a Warning) [all assumed except
BENSTAT 3 = closed]. Clients with more than one account are counted
(Info); so are accounts linked to their client by `TRADINGID`. A trading
client's demat account at another DP (from `CDMAST`) is not an account of
this DP: it is in CLIENT-DEMATS (and the client master's DematID when it
is the primary).
`FHOLDER_ADHAR`, `SHOLDER_ADHAR`, `THOLDER_ADHAR` and `Web_Pwd` are
counted, never selected. Check dp-accounts: `COUNT(*)` of the DP
companies' `CLLIST` rows, trading and DP-only clients' (without dormant
trading clients' and, in mode skip, closed DP-only accounts).

### DP-TARIFFS, DP-TARIFF-ACCOUNTS (`DPCLBSKT`, `DPCHARGE`, `DPCLBASKET`, `DPAMCDB`) [verified: counts]

| Ledgerline | SharePro |
|---|---|
| Plan, Name | `DPCLBSKT.BASKETCD`, `DESCR` |
| Pool | Y when `DPCLBSKT.TYPE` is CM (clearing member and pool accounts) |
| AMCBilling | Yearly when `DPCLBSKT.PERIOD` is Yearly, else Monthly |
| From, Code | `DPCHARGE.EFFECTDATE` (1900-01-01, SharePro's "no date", is kept: in force from the start), `TRANCODE` of the rows whose `BENACNUM` is a basket code; NSDL codes as the CDSL code of the same charge (901 → 2201, ODEL → ODTA, PIN → DCM, …; the first of a pair is kept) |
| Flat, Min, Max, Mail | `FLAT`, `MIN`, `MAX`, `MAILCHG` |
| Pct or PerUnit | `RATE`: per certificate on demat and remat (2201, 2205, 901, 902), else % of value |
| OwnFlat … OwnMax | the `CM*` set (transfers linked to the firm's own trades); all zero: free |
| SpecialFlat … SpecialMax | the `SP*` set, kept for the record, not billed (Warning sp-rates with the count of lines where it differs) |
| AM, 0015 | the version's annual maintenance and, on top for a body corporate, corporate maintenance (`MAX(FLAT, MIN)`) |
| DP-TARIFF-ACCOUNTS BOID, Plan, From | `DPCLBASKET` rows (`BENACNUM`, `BASKETCD`, `EffectDate`, 1900-01-01 kept as from the start) of the accounts in DP-ACCOUNTS |
| AMCPaidUpto | `MAX(DPAMCDB.AMDATETO)` where `AMTDR > 0` (9999-01-01: paid for life), on the account's first row |

Rows keyed by an account (account-specific tariffs) are counted (Info),
not migrated. Assignments to a basket not in `DPCLBSKT` or blank, to a
basket with no rates, or with a NULL `EffectDate` are left out (Warning
each); accounts with no basket pay the firm tariff (Info). `DPTRTYPE`
is the code master and is not read. Checks: dp-tariff-versions
(distinct basket and `EFFECTDATE` in `DPCHARGE`), dp-tariff-accounts
(DP accounts with a basket that has rates, effective on the as-of date)
and dp-amc-paid-ahead (those accounts with maintenance paid beyond the
as-of date).

### CLIENT-DEMATS (`CDMAST` ⋈ `DPMAST` on `DPCODE`) [verified]

Every demat account of each migrated client in the trading side's client
demat master, at this DP or another: UCC `CDMAST.SUBCD`; DematID: CDSL
`DPMAST.DPID` (8 digits) + `CLIENTID` (8 digits); NSDL (`DPID` starting
IN, or 6 characters with NSDL in `DPMAST.DESCR`) IN + the 6-character DP
ID + `CLIENTID`; a `CLIENTID` already holding the whole ID is taken as it
is; Depository CDSL or NSDL accordingly; Primary Y on the client master's
demat account when it is an open `TYPE` Primary row (a second open
primary is a Warning); Status Inactive when `Closed`, else Active. `TYPE`
Pool rows (the firm's own) are left out. An account of the firm's DP
closed in `CLLIST` is Inactive and not the primary, whatever `CDMAST`
says [verified: most `CDMAST` primaries of closed DP accounts are not
marked `Closed`]; the client master's DematID is then the client's open
account, else (all closed) its oldest at the firm's DP. The same account
on two clients (a joint or family account, or one person's two codes
[verified: some 330 other-DP and 60 own-DP IDs, about half with one PAN])
loads with a Warning in Ledgerline. A client's rows of the same
account are one row (Info count). A row whose ID cannot be made (DP code
not in `DPMAST`, client ID not 8 digits, wrong length) is left out with a
Warning, an Error for an active client's open primary. Not yet in
`ops.MasterSources` on this branch (the import is being added).

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
- DP client heads [verified: in the CDSL company, and a few in a cash
  company, `64` CDSL CLIENTS with a sub-ledger per DP account, trading and
  DP-only clients' alike; `2ODCL` CDSL OVERDUE CLIENT; `2ADVTR` ADVANCE DP
  TRAN CHARGE]: a head (not client-coded) whose sub-accounts are (mostly)
  DP client codes (`CLLIST.BENACNUM`) of the firm's DP companies.
  - a sub-account is the DP account's client's: its trading client
    (`SUBCODE`, else `TRADINGID`, when a client in `DLMAST`, else the
    `CDMAST` client of the first holder's PAN, as above), else the DP-only
    client; a code of two clients, or not in `CLLIST`, has none;
  - a DP client head whose name contains CLIENT (as above) or ADVANCE (an
    advance from the client) is the client's ledger → `CLIENT:<client>`,
    with the client's trading ledger and its other DP accounts' (they add
    up); another DP client head → one firm account `<PREFIX>:<CO>-<ACCD>`
    with a Warning;
  - a sub-account with no client stays `<PREFIX>:<CO>-<ACCD>-<SUBCODE>`;
  - each DP client head is reported: sub-ledgers that went to a client and
    those that did not, with their balances (a Warning when any did not);
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

Since (client debits): the year's lines of the client ledger heads (DP
client heads included; `JVFILE` and `BCFILE`, from the year's start to the
as-of date, in date order) netted per day and sub-account, first in first
out over the client's ledger — each credit pays the oldest debit — starting from
the year's opening balance (balance minus the year's movement) dated the
year's first day. A debit already due at the year start keeps the year's
first day, counted in an Info issue: `LY_ACCTOT` is incomplete [verified]
and is not used.

Checks, all summed on the server from the same openings and movements,
client ledgers merged by the same rule (pairs of company and head):
trial-balance-debit and trial-balance-credit (including the difference
line), opening-difference, client-debit-total and client-credit-total
(merged client heads and merged DP client heads). The DP client heads'
sub-accounts are joined on the server to `CLLIST.BENACNUM` of the firm's
DP companies and keyed by the account's client (the trading code, or the
DP client code of a DP-only client; a code of two clients left out), so a
client's trading ledger and DP accounts add up as Ledgerline holds them.

### DP-HOLDINGS (`HOLD` at `MAX(HOLDDATE) <= as-of`, the firm's DP COs)

One row per client, DP account and ISIN: UCC the client of `CLLIST`
(`CO`, `BENACNUM`), trading or DP-only; BOID its `FBENACNUM`; ISIN
`ISINCD`; Symbol `ITC.NSE_ITEMCD` / `BSE_ITEMCD` via `ISINMAST.ITEMCD`; Qty
`SUM(BALANCE)` [verified: BALANCE is the total]; Pledged `SUM(PLDGBAL)`
(at most Qty); AvgCost empty. Quantities are exact, with their decimals
(mutual fund units), trailing zeros trimmed (125.432, 100); fractional
holdings are counted (Info). Accounts in `HOLD` but not in `CLLIST` are
reported (Warning) and left out. Check dp-holding-quantity: the exact
`SUM(HOLD.BALANCE)` of every `CLLIST` account of the DP companies at that
date (without dormant trading clients' and, in mode skip, closed DP-only
accounts).

### HOLDING-LOTS

Not written: SharePro holds no purchase lots or cost for DP holdings (if
it ever is, it takes BOID and exact quantities like DP-HOLDINGS).

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
| LEDGER | `JVFILE` (VoucherType "Journal" + `TYPE`, VoucherNo `TT/VRNO`, account as in OPENING-BALANCES, UCC the client of any client-coded head or DP client head (a DP account's client as the go-live step gave it, else the year's `CLLIST.SUBCODE`), `NARR`, `AMTDR`, `AMTCR`) and `BCFILE` (Receipt/Payment/Contra/Cash by `TYPE`, `P_R`; `CHEQNO`, `VALUEDT`) with the bank or cash book's own side, which `BCFILE` does not store as a line, on the head whose `MSFILE.TT`/`MASTMSFILE.TT` is the voucher's `TT` [assumed]. SourceRef table:CO:TT/VRNO:IDN. An unbalanced year is a warning. |
| BILLS | `SETTNET` per client and settlement (not `INVOICEHEAD`, whose `CONTRACT` rows are the GST invoices of contract notes): BillNo CO-`cntrno` (or `stno sttype/subcd`), `Turnover`, `Brokerage`, `STTax`, `Trancharge`+`Clrcharge`, `SebiChg`, `BNoteStamp`, `CGST`+`SGST`+`IGST`+`UTGST`+`ServiceTax`, other charges (`DematCh`, `DeliveryCh`, `Penalty`, `AddlBrkg`, `RmsCharge`, `IPFT`, `OthCharge`), `NetRecPay` [assumed] |
| TRADES | `TRFILE` with `ITC`, `ISINMAST`, `ICCLCONT` as for positions; Side by the sign of `QTY` [verified]; Price `ACT_RATE` [verified] else `RATE`; Brokerage `BRKG × |QTY|` (BRKG per unit [verified]); BillNo `CNTRNO`; OptionType blank (Info count) when call/put cannot be told |
| CONTRACT-NOTES | distinct `TRFILE` (`CO`, `CNTRNO`, `TRDT`, `SUBCD`). SentOn, SentTo, Signed and DocumentID are empty: the mail archives are read after the company databases, so the register cannot be linked here. |
| RECEIPTS-PAYMENTS | `BCFILE` rows with `P_R` R or P; Mode Cash (`TYPE` C), Cheque (`CHEQNO`), Online (`Gateway`), else bank transfer; Bank = the book's head name; Reference `BankRef`/`TXNNO`/`EcsRef` |
| DP-TRANSACTIONS | `DPTRAN` of the DP COs; BOID `FBENACNUM`; UCC via `CLLIST` (the year's `SUBCODE`; for an account with none, the client the go-live step gave the account — the DP-only client's code); Security `ISINLIST.ISINSHORT`; TxnType `TRANCODE`; Quantity `QTYBOOKED` exact (decimals kept), − for `DRCR` D; Reference `TXNID`/`DRFNO`/`INTERNALRF`; Counterparty `COUNTERDP`+`COUNTERBEN` or `CNTRCMBPID` |
| DP-BILLS | `INVOICEHEAD` with `AutoType` CDSLBILL (also NSDLBILL, DPBILL) [verified]: `InvoiceNo`, `InvoiceDt`, `SubCode` (BO account or client), `GrossAmt`, GST parts, `NetAmt`. Period from/to empty. |
| DP-HOLDINGS-YEAR-END | `HOLD` at the year's last `HOLDDATE`, per BOID; UCC as for DP-TRANSACTIONS; Free `CURBAL`, Pledged `PLDGBAL`, Locked `BALANCE − CURBAL − PLDGBAL`, Total `BALANCE` [verified], all exact; Rate the latest `DPPRICE.RATE` with `CLOSEDT` on or before the date for the ISIN (`HOLD.RATE` is never filled [verified]); Rate and Value empty without a price |

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
  account (`BENACNUM` or `FBENACNUM`), turned into its client through the
  newest company database's `CLLIST`: the trading code, or the DP-only
  client's code for an account with no trading client; a DP account not in
  `CLLIST` gets no UCC (Info count); Branchid, Group, SubBroker → the firm's records (Type
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
| `CLLIST.FHOLDER_ADHAR`, `SHOLDER_ADHAR`, `THOLDER_ADHAR`, `CLLIST_DET.UID`, `FHADHARNOM`, `SHADHARNOM`, `THADHARNOM`, `CDNOMREG.UID`, `DLAGREE.UID`, `DLAGREE.NomineeAdhar`, `DLAGREE2.C_UID1`–`3`, `CLAUTHMAST.UID`, `BOMAST.FHUID` | full Aadhaar numbers: counted, never selected (Ledgerline keeps no client Aadhaar) |
| DP-only clients' income range (`CLLIST_DET.GROSSINCOME`) | codes not confirmed: counted (Info), Income blank |
| staff passwords (`SHUSER`), `SYSTEMLOGFILES.Content`, OTP and token tables | never read |
| Family (`DLMAST.FAMILYCD`) | no Ledgerline column: counted as dropped |
| FATCA details | no import file |
| account-specific DP tariffs (`DPCHARGE` rows keyed by an account) | counted (Info); the accounts pay their plan's rates |
| online account opening and re-KYC documents (`EKYC_documents`, `REKYC_*`) | not read (identity documents; to be handled with the KYC records) |
