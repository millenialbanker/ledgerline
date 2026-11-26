Ledgy help articles: the nightly self-inspection (the tests an exchange inspector or internal auditor runs, on the firm's own data), its score and trend, the exceptions as cards on the Self-inspection board, accepting a risk and muting a test.

# Self-inspection

## self-inspection
title: The nightly self-inspection: what it tests and how it scores
screen: selfinspect
roles: Compliance officer, Admin, Ops maker, Ops checker, Risk manager
ask: self inspection; self-inspection; internal audit every night; inspector's tests; mock inspection; inspection readiness; compliance score; what does the score mean; run the self inspection now; when does the self inspection run; nightly checks; why did a test fail; which tests are real; tests waiting for data; inspection trend
related: self-inspection-exceptions, self-inspection-tests, ledgerline-boards

[[selfinspect|Self-inspection]] (Compliance) runs the tests an exchange inspector or internal auditor would run, on the firm's own data, every business day after the day's work, so a breach is found the next morning rather than at the half-yearly audit. It reads only; it changes nothing in the firm's work, so it runs in every edition.

- **When**: every business day at the firm's time (22:30 IST unless changed under **Schedule**; the compliance officer or an Admin can switch it off). **Run now** (compliance officer or Admin) runs every test at once.
- **What a run keeps**: each test's result as it was (passed, failed, not checked, muted or could not run), how many records it checked and found, up to five examples by record ID with client codes masked (BH**14), and the dates of the data it read. **Past runs** shows any run exactly as it was.
- **Score**: the pass rate weighted by severity (high 3, medium 2, low 1) of the tests that ran. Tests waiting for data, not applicable or with nothing to check, and muted tests are not counted. An accepted risk still counts as a failure: the test still fails.
- **Trend**: the score of each business day's last run over 90 days; **Show the numbers** gives the table.
- **Tests**: filter by area or show only failing ones; each shows its severity, owner, last result, the last 30 runs and the rule with its circular. A line marked **To confirm** is a part of the rule Ledgerline could not confirm against the circular; check it before relying on it.
- **Email**: after a nightly run with failures, the compliance officer gets an email with the counts and a link only: never a test, a card or a client.

Everyone at head office can see the screen; branch staff and authorised persons cannot.

## self-inspection-exceptions
title: Self-inspection exceptions: the board, fixing, accepting a risk, muting
screen: selfinspect
roles: Compliance officer, Admin, Ops maker, Ops checker, Risk manager
ask: self inspection card; self inspection board; exception card; accept the risk; accepted risk; risk acceptance; mute a test; unmute; mark fixed; being fixed; fixed waiting for the next run; card closed by itself; why did the card come back; due date of an exception; who owns an exception
related: self-inspection, self-inspection-tests, ledgerline-board-moves

Each failing test opens one **exception**, which is a card on the Ledgerline board **Self-inspection** (an Admin sets it up with **Set up the board**, or with the other Ledgerline boards on [[boards|Boards]]). The card carries the test, what it found, the masked examples, how to fix it, the rule, the owner role and a due date by severity: high 3 days, medium 10, low 30.

- **New**: found by a run. A later run that still finds it updates the counts and examples.
- **Being fixed**: the owner role, the compliance officer or an Admin moves it here (drag the card, or the button on the screen).
- **Fixed, waiting for the next run**: with a note of what was fixed. The next run checks it: it closes if the test passes, and goes back to **Being fixed** if not.
- **Closed**: the test passed on a later run. Nobody closes a card by hand; it closes by itself.
- **Accepted risk**: only the compliance officer, with a reason and a review date (within a year), from the card's **Accept risk** or the screen. Your name, role and reason go to the audit trail. An Admin cannot accept a risk: it is the compliance officer's own decision. If the test still fails after the review date, the exception is **New** again.

**Muting a test** (compliance officer only, not an Admin): on the test, **Mute this test** with a reason and an end date (within 180 days). It still runs every night, but its result is kept as muted, it does not count in the score and it opens no card. **End the mute** stops it early. Both go to the audit trail.

## self-inspection-tests
title: The self-inspection's tests and their rules
screen: selfinspect
roles: Compliance officer, Admin, Ops maker, Ops checker, Risk manager
ask: list of self inspection tests; which circular does the test follow; ucc fields test; minor trading test; dealer terminals test; margin statement test; segregation test; client money test; cuspa test; cuspa intimation test; cuspa policy test; repledge test; margin cover test; pool account test; t+1+5 test; pledge undertaking test; contract notes 24 hours test; complaints 21 days; payouts to registered bank; third party funds test; kyc test; inactive accounts test; ddpi test; alerts 45 days; holding statement test; segmon test; weekly statement test; debarred clients test; ucc test; bsda test; mnrl test; deceased clients test; firm bank register test; client money nil test; client fdr test; journal between clients test
related: self-inspection, self-inspection-exceptions

Each test reuses the check Ledgerline already makes on its own screen. All are real but one, which waits for data. The tests added from the penalty audit are explained in the help article "Penalty risks and how Ledgerline covers them".

- **Client money fully covered** (high): the segregation check; SEBI/HO/MIRSD/MIRSD2/CIR/P/2016/95 of 26 September 2016, para 3.3.1. With the firm's bank accounts named, the client bank accounts count only as far as the stipulated reasons for keeping money explain them (NSE/INSP/73976, Exhibit 34).
- **Running accounts settled on each client's chosen date** (high): SEBI/HO/MIRSD/MIRSD-PoD1/P/CIR/2023/197 of 28 December 2023 and the exchanges' joint calendar; idle balances returned on the upcoming monthly settlement date (SEBI/HO/MIRSD/MIRSD-PoD1/P/CIR/2025/1 of 6 January 2025).
- **Unpaid securities kept within 2.25 × (margin − ledger)** (medium), **Notice given before invoking** (high), **Nothing kept past the 6th trading day** (medium): SEBI HO/38/11/(9)2026-MIRSD-POD/I/15382/2026 of 3 July 2026 and NSE circular 41/2026 (NSE/INSP/75508) of 31 July 2026.
- **Client told of each CUSPA pledge by the start of the next trading day** (high), **Written policy on unpaid securities, sent to every client and on the website** (high), **CUSPA extensions asked by 6 PM on the last day, each told to the client** (medium), **CUSPA securities never pledged to banks or NBFCs** (high): the same circular, paras 46.2 to 46.4 and 46.10 to 46.14, and NSE circular 41/2026 guidelines 2 and 10.
- **Clients' securities not left in the pool or unpaid accounts** (high), **Each client's re-pledged securities cover only that client** (high), **Each client's collateral at NSE Clearing covers the client's margin** (high), **No further exposure past the 5th trading day after pay-in** (high): SEBI master circular SEBI/HO/MIRSD/MIRSD-PoD/P/CIR/2025/90, paras 42.10, 42.12.5, 43.13-43.14 and 15.4.4.4.
- **Notice given before a margin pledge was invoked** (high) and **Signed undertaking kept with each invocation and pledge to a lender** (medium): SEBI HO/47/14/12(1)2026-MRD-POD2/I/4229/2026 para 4 and CDSL communiqué DP2026-180.
- **Margin penalties worked out, and passed to clients only where allowed** (high): NSE/INSP/64315 of 1 October 2024 (dishonoured cheque, or a hedge break disclosed in the risk policy).
- **Contract notes sent within 24 hours** (high): the investor charter (SEBI/HO/MIRSD/MIRSD-PoD1/P/CIR/2025/22 of 21 February 2025).
- **Complaints resolved within 21 days** (high): SEBI/HO/OIAE/IGRD/CIR/P/2023/156 of 20 September 2023 (SCORES).
- **Payouts only to registered bank accounts** (high) and **Money from unregistered accounts returned, not used** (high): SEBI/MRD/SE/Cir-33/2003/27/08 of 27 August 2003. The three working days to return a parked credit are the firm's practice (to confirm).
- **KYC complete for active clients** (medium): SEBI/HO/MIRSD/FATF/P/CIR/2023/0144 of 11 August 2023; income proof for derivatives is to confirm.
- **Accounts inactive for 24 months flagged** (medium): NSE/INSP/64718 of 25 October 2024.
- **Alerts decided within 45 days** (high): NSE/SURV/48818 of 1 July 2021, kept in NSE/SURV/74008 of 30 April 2026.
- **Holding statement sent each day** (high): NSE/INSP/55039 of 28 December 2022; NCL/INSP/60046 of 29 December 2023 (T+1).
- **Client collateral (SEGMON) reported each day** (high): SEBI/HO/MRD2_DCAP/CIR/2021/0598 of 20 July 2021.
- **Weekly statement of funds and securities** (medium): NSE/INSP/73976 (Inspection master circular) item 4.5, the week Monday to Saturday sent within four trading days, to every active client who traded in the last 12 months (NSE/INSP/53820 FAQ 9).
- **Annual global statement sent within 30 days of the year's end** (medium): NSE/INSP/73976 item 4.17 and Exhibit 30; SEBI master circular 2025/90, para 23.9.
- **Referral rewards paid only to registered authorised persons** (high): NSE/INSP/73976 item 4.21; MCX/INSP/255/2026 para 9.9(a).
- **Clients told of SCORES and SMART ODR when a complaint stays unresolved** (medium): SEBI master circular 2025/90, Annex 37; NSE/ISC/73908, Part A 7.2.
- **No trades by debarred clients** (high): SEBI's debarment orders as the exchanges circulate them; needs the debarred list loaded with PANs.
- **Client details match the exchanges' UCC records** (medium): NSE/ISC/73973 of 30 April 2026 and the exchanges' UCC answers.
- **Client details at the exchanges match Ledgerline** (medium): the exchanges' UCC downloads of the last 45 days and what NSE's UCC API last accepted, field by field (PAN, mobile, e-mail, primary bank account, address); NSE/ISC/73973 and NSE/INSP/73503 items 46 and 47.
- **Firm's demat accounts tagged and in the holding statement with their type** (high): NSE/INSP/55039 and NSE/INSP/55380.
- **Trades only from registered terminals, by NISM-certified dealers** (high), **No derivatives or intraday trading in a minor's account** (high), **No trading before KYC is complete and validated** (high), **No mobile or e-mail shared by clients who are not one family** (medium): NSE/INSP/73503 of 27 March 2026 and SEBI/HO/MIRSD/FATF/P/CIR/2023/0144.
- **Funds paid out within one working day of the client's request** (high): SEBI/MIRSD/SE/Cir-19/2009 of 3 December 2009 and SEBI master circular 2025/90, para 97.4; a failed transfer is not paid, and a cheque counts from the day it was credited.
- **Firm bank accounts named, reported to each exchange and verifiable by clients** (high): SEBI master circular 2025/90, paras 15.3 and 15.4.3 as amended in March 2026; NSE/INSP/73976 item 8.2; NSE/INSP/77149; NSE/COMP/75700.
- **Client money upstreamed every day; kept back only for the stipulated reasons** (high): SEBI/HO/MIRSD/MIRSD-PoD-1/P/CIR/2023/84; NCL/CMPL/61186 and NCL/CMPL/76318 FAQ 5.
- **Client-fund FDRs made only from the USCNBA, within SEBI's conditions** (high): SEBI master circular 2025/90, paras 97.5 and 97.6.
- **No journal used to pay a client or move money between clients** (high): SEBI master circular 2025/90, paras 23.1.1(j) and 48.3.
- **Daily margin statements sent by the next trading day** (medium) and **Statement of each running account settlement sent, retention included** (medium): NSE/INSP/73503 items 24 and 26 to 28; the days allowed are to confirm.
- **Client mobiles on DoT's revocation list taken up in time** (medium): NSE/ISC/45168 of 29 July 2020, NSE/ISC/47834 of 31 March 2021 and NSE/ISC/72805 of 13 February 2026; the 3 business days and 30 days are the firm's practice (to confirm).
- **Deceased clients' UCCs closed by the cut-off, with debits blocked** (high): NSE/ISC/77375 of 9 November 2026 and NSE/ISC/76893 of 14 October 2026; SEBI/HO/OIAE/OIAE_IAD-1/P/CIR/2023/0000000163 of 3 October 2023.
- **BSDA review each quarter** (low), **The day's files received and made** (low) and **No compliance calendar duty overdue** (medium): the firm's own Periodic work, file timetable and calendar.
- **DDPI or POA used only for permitted purposes** waits for data: the depository records Ledgerline loads do not say which debits were made under a DDPI (SEBI/HO/MIRSD/MIRSD-PoD-1/P/CIR/2022/137 of 6 October 2022).
