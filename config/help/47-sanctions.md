Ledgy help articles: screening clients against the UN Security Council, UAPA and WMD lists; sanctions cases, the 24-hour report register, false positives and unfreezing; FATF public statements and EDD.

# Sanctions screening

## sanctions-screening
title: Screen clients against the UNSC, UAPA and WMD sanctions lists
screen: kyc
roles: Compliance officer
ask: sanctions screening; unsc list; un security council list; un consolidated list xml; uapa list; uapa 51a; wmd act 12a; designated list; terrorist list; al qaida list; dprk list; 1718 list; load sanctions list; sanctions list version; screen all clients; name screening; fuzzy name match; transliteration; joint holder screening; nominee screening; defaulting clients database; nse defaulting clients; sanctions score threshold
check: client
related: sanctions-match, sanctions-false-positive, fatf-statement, kyc-pmla

No account may be held for a person on the UN Security Council's lists or designated under UAPA section 51A, and every client is checked against the Designated List under section 12A of the WMD Act at onboarding and regularly (SEBI's AML/CFT master circular SEBI/HO/MIRSD/MIRSDSECFATF/P/CIR/2024/78, paras 54-67). These lists seldom carry a PAN, so Ledgerline screens by name too.

**Load the lists.** On [[kyc|KYC & PMLA]] → **Sanctions**, click **Load a list** and choose the list:

- **UN Security Council consolidated list**: the UN's XML file as published (it covers ISIL/Al-Qaida, the Taliban, DPRK 1718 and the other committees). The version is the date in the file.
- **UAPA designated individuals and organisations** and **WMD Act Designated List**: a CSV with a **Name** column and, where the list gives them, PAN, Aliases (separated by ;), DOB (YYYY-MM-DD or a year), Nationality, Ref, Regime, ListedOn and Type (Individual or Entity). Give the version (the list's date).
- **Exchanges' defaulting clients**: a CSV with Name and PAN. A match holds a new account for you to decide; it is not a block.

Each load of a UNSC, UAPA or WMD file replaces that list: an entry not in the new file is marked delisted. Every load is kept with its version, file and who loaded it. The DEBARRED-LIST file on [[files|Files]] still works, and takes the UN's XML too.

**Who is screened.** The client, joint holders, the guardian of a minor, an HUF's or company's controlling persons, and nominees (a setting). A joint holder owns the account, so a match holds it like the client's. A nominee has no right to the account while the holder lives, so a nominee's match is reported as a link to a listed person but does not hold the account.

**How names are matched.** By PAN where the list has one (certain). By name with every alias: accents and punctuation are ignored, words in any order, common spellings of one name read alike (Mohammed, Muhammad, Mohd; Yusuf, Yousef), and names written as one word or two. The date of birth and nationality add to the score when they agree and take from it when they do not. A score of 82 or more opens a case; 94 or more also holds the client at once. Change these under **Settings**.

**When.** On every new application (before it can go to the exchanges or be activated), on every list load (all clients), on each FATF statement, and every day (a setting). **Screen all clients now** runs it at once.

**Self-inspection.** The test *Clients screened against the UNSC, UAPA and WMD lists* fails a client not screened since the latest list load or within the set number of days, a case past its 24 hours, a confirmed holder not blocked, and late FATF EDD.

## sanctions-match
title: Verify and report a sanctions match within 24 hours
screen: kyc
roles: Compliance officer
ask: sanctions match; sanctions case; client matches designated list; client matches a designated person; what to do on a match; confirm sanctions match; report to nodal officer; central nodal officer; cno; uapa nodal officer; state nodal officer; report to sebi uapa; sebi_uapa; freeze designated person; freeze demat sanctions; 24 hours sanctions; str for designated person; report register sanctions; unfreeze order; inadvertent freezing; unfreezing application
related: sanctions-screening, sanctions-false-positive, fingate-str

A possible match opens a case on [[kyc|KYC & PMLA]] → **Sanctions** with a 24-hour clock from the moment it was found. The MHA's UAPA order asks for the report "not later than 24 hours from the time of finding out such customer"; SEBI's circular says without delay.

1. **Open** the case: the person, the list entry (with its reference and regime), the score and why.
2. Compare the client's documents with the entry: name and aliases, date and place of birth, nationality, father's name, passport or ID numbers in the list.
3. If it is the listed person, click **Confirm match** and say what you checked. At once:
   - the client is blocked: no trading, no payouts, no IPO, mutual fund or bond transactions;
   - nothing leaves the client's demat accounts with the firm's DP, and a **Freeze** request (debit) goes to [[dp|DP requests]] for an Ops checker to approve and apply in CDSL's system;
   - an alert goes to [[surv|Surveillance & AML]] for the STR (all transactions in the account);
   - the **report register** lists who must be told. Under UAPA: the UAPA Central Nodal Officer (MHA), the State UAPA Nodal Officer, SEBI, the exchanges and the depository, and FIU-IND. Under the WMD Act (the DPRK and Iran lists, and the WMD list): the Central Nodal Officer (the Director, FIU-IND), SEBI, the exchanges and the depository.
4. Send each report with full particulars of the funds, securities and services held, then click **Record** on its line with the date and reference.
5. Decide the STR on the alert. Then **Close as reported**. The client stays blocked and the account frozen until an unfreezing order.

**Unfreezing.** If the person applies to have assets unfrozen (inadvertent freezing, or the person was delisted), record **Unfreezing application**: it must go to the Central Nodal Officer, with details of the frozen assets, within two working days. Record **Forwarded to the CNO**. When the order comes, record **Unfreezing order**: the hold is lifted and unfreeze requests are raised for frozen demat accounts.

A confirmed case is also recorded as an event on the compliance calendar (Report a designated-person match), due 24 hours after it was found.

## sanctions-false-positive
title: Clear a sanctions match that is not the listed person
screen: kyc
roles: Compliance officer
ask: false positive sanctions; clear sanctions match; not a match; sanctions name match wrong person; approve clearing; maker checker sanctions; lift sanctions hold; client held sanctions; application held sanctions; defaulter match decide
related: sanctions-match, sanctions-screening

Most name matches are other people with a similar name.

1. On [[kyc|KYC & PMLA]] → **Sanctions**, click **Not a match** on the case and give the reason (for example the date of birth on the PAN card differs from the list's).
2. Another compliance officer (or an admin) clicks **Approve clearing**. One person cannot propose and approve. **Reject** sends it back to be verified.
3. Once approved, any hold is lifted and the client's earlier block, if any, is put back. A new application can then go ahead.

The same list entry is not raised again for that person unless the list changes the entry (a new alias or date of birth). For a match with the exchanges' defaulting clients, **Not a match** also records the firm's decision to open the account after its own checks.

## fatf-statement
title: Act on a FATF public statement: re-screen and record EDD
screen: kyc
roles: Compliance officer
ask: fatf statement; fatf public statement; fatf plenary; high risk jurisdiction; call for action; increased monitoring; grey list; black list fatf; enhanced due diligence fatf; edd fatf; cdsl fatf communique; fatf declaration
related: sanctions-screening, kyc-pmla, compliance-duties

After each FATF plenary (February, June and October) SEBI, the exchanges and the depositories circulate its public statements. Take the listed jurisdictions into account and apply enhanced due diligence to clients linked to them (SEBI's AML/CFT master circular paras 18(iii)(f), 42, 71-72).

1. On [[kyc|KYC & PMLA]] → **Sanctions**, click **Record a FATF statement**: the plenary, the date, the circular's reference, and the countries under *call for action* and under *increased monitoring* (ISO codes or names).
2. Every client is checked for links to those countries: nationality, country of birth, tax residence, and the same for an entity's controlling persons (from the FATCA/CRS self-certification). Each linked client gets an EDD item, due in 30 days, in the work queue. All clients are also screened again against the sanctions lists.
3. For each item, review the source of funds and the activity, then **Record EDD** with what you found. Tick *Make the client High risk* for a client linked to a call-for-action country: it becomes a client of special category (high-risk jurisdiction).

The statement is recorded on the compliance calendar's FATF duties. A depository participant also submits the actions taken to CDSL online, as the communique asks.
