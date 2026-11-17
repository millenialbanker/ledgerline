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
ask: list of self inspection tests; which circular does the test follow; segregation test; client money test; cuspa test; contract notes 24 hours test; complaints 21 days; payouts to registered bank; third party funds test; kyc test; inactive accounts test; ddpi test; alerts 45 days; holding statement test; segmon test; weekly statement test; debarred clients test; ucc test; bsda test
related: self-inspection, self-inspection-exceptions

Each test reuses the check Ledgerline already makes on its own screen. 21 are real; 1 waits for data.

- **Client money fully covered** (high): the segregation check; SEBI/HO/MIRSD/MIRSD2/CIR/P/2016/95 of 26 September 2016, para 3.3.1.
- **Running accounts settled on each client's chosen date** (high): SEBI/HO/MIRSD/MIRSD-PoD1/P/CIR/2023/197 of 28 December 2023 and the exchanges' joint calendar; idle balances within three working days of 30 days (NSE/INSP/53820, to confirm).
- **Unpaid securities kept within 2.25 × (margin − ledger)** (medium), **Notice given before invoking** (high), **Nothing kept past the 6th trading day** (medium): SEBI HO/38/11/(9)2026-MIRSD-POD/I/15382/2026 of 3 July 2026 and NSE circular 41/2026 (NSE/INSP/75508) of 31 July 2026.
- **Margin penalties worked out, and passed to clients only where allowed** (high): NSE/INSP/64315 of 1 October 2024 (dishonoured cheque, or a hedge break disclosed in the risk policy).
- **Contract notes sent within 24 hours** (high): the investor charter (SEBI/HO/MIRSD/MIRSD-PoD1/P/CIR/2025/22 of 21 February 2025).
- **Complaints resolved within 21 days** (high): SEBI/HO/OIAE/IGRD/CIR/P/2023/156 of 20 September 2023 (SCORES).
- **Payouts only to registered bank accounts** (high) and **Money from unregistered accounts returned, not used** (high): SEBI/MRD/SE/Cir-33/2003/27/08 of 27 August 2003. The three working days to return a parked credit are the firm's practice (to confirm).
- **KYC complete for active clients** (medium): SEBI/HO/MIRSD/FATF/P/CIR/2023/0144 of 11 August 2023; income proof for derivatives is to confirm.
- **Accounts inactive for 24 months flagged** (medium): NSE/INSP/64718 of 25 October 2024.
- **Alerts decided within 45 days** (high): NSE/SURV/48818 of 1 July 2021, kept in NSE/SURV/74008 of 30 April 2026.
- **Holding statement sent each day** (high): NSE/INSP/55039 of 28 December 2022; NCL/INSP/60046 of 29 December 2023 (T+1).
- **Client collateral (SEGMON) reported each day** (high): SEBI/HO/MRD2_DCAP/CIR/2021/0598 of 20 July 2021.
- **Weekly statement of funds and securities** (medium): NSE/INSP/47227 of 3 February 2021 and NSE/INSP/48431 of 28 May 2021; to confirm that it still applies, since the 2025 investor charter lists monthly statements.
- **No trades by debarred clients** (high): SEBI's debarment orders as the exchanges circulate them; needs the debarred list loaded with PANs.
- **Client details match the exchanges' UCC records** (medium): NSE/ISC/73973 of 30 April 2026 and the exchanges' UCC answers.
- **BSDA review each quarter** (low), **The day's files received and made** (low) and **No compliance calendar duty overdue** (medium): the firm's own Periodic work, file timetable and calendar.
- **DDPI or POA used only for permitted purposes** waits for data: the depository records Ledgerline loads do not say which debits were made under a DDPI (SEBI/HO/MIRSD/MIRSD-PoD-1/P/CIR/2022/137 of 6 October 2022).
