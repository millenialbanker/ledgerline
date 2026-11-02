Ledgy help articles: the exchanges' RAS calendar, the system audit cycle, common submissions filed with NSE only, and the other dated duties in the compliance calendar.

# Compliance calendar

## ras-calendar
title: Keep the exchanges' RAS dates
screen: circulars
roles: Compliance officer, Admin
ask: ras dates; ras calendar; running account settlement dates; first friday; friday or saturday settlement; quarterly settlement date; monthly settlement date; annual calendar for settlement of running account; next ras date
related: ras, compliance-profile

Running accounts are settled on the dates the exchanges set jointly for each financial year, not on the first Friday (SEBI/HO/MIRSD/MIRSD-PoD1/P/CIR/2023/197 of 28 December 2023; NSE/INSP/60014). Each date is a Friday and/or the Saturday after it, separately for clients who chose monthly and quarterly settlement.

1. On [[circulars|Circulars & calendar]] → **RAS dates & profile**, the list shows each settlement, the exchanges' dates and the day the firm settles. FY 2026-27 is already there (for example 17/18 April, 3/4 July, 16/17 October 2026 and 1/2 January 2027 for quarterly clients; 15/16 May, 6/7 November 2026 for monthly clients).
2. **Edit RAS dates** (compliance officer or admin): one line per settlement — *Monthly* or *Quarterly*, the period (2026-11, or 2026-27 Q3; Q1 is April–June), the Friday and/or Saturday, and the day the firm settles (the Friday unless you choose the Saturday). Only Fridays and Saturdays are accepted.
3. The next monthly and quarterly date in Settings, the [[ras|Running account]] screen, Periodic work and Autopilot all follow the list; it is applied each morning and whenever it is saved. The compliance calendar has one duty for quarterly clients and one for monthly clients, due on the last day of each window and done once the settlement has run.
4. When the list ends within two months a work item asks for the next year's calendar, which the exchanges publish in January.

## compliance-profile
title: System audit cycle, auditor and MTF
screen: circulars
roles: Compliance officer, Admin
ask: system audit; system audit due date; type iii system audit; half yearly system audit; empanelled auditor; enit audit plan; action taken report; atr; net worth certificate date; mtf net worth
related: ras-calendar, shared-filing

NSE's system audit circular NSE/INSP/73850 (22 April 2026) sets the cycle by member type: half-yearly for members trading through NEAT, NNF and algo (Type III); yearly for Type I and Type II members with more than 10 locations or 50 terminals, and for depository participants; once in 2 years for the smaller Type II members; none for NEAT-only members who are not DPs.

On **RAS dates & profile** → **System audit, auditor and MTF** set the cycle, the auditor, and whether NSE has empanelled them (NSE/INSP/69631: only an empanelled auditor's report is accepted). The calendar then has three duties:

- **Audit plan** on NSE's ENIT portal (auditor, systems, office locations to visit): by 5 June; also by 5 November for the April–September half year.
- **Preliminary audit report**: by 30 June; also by 30 November for the half year (NSE/INSP/76769). The algo MIS, version confirmation, assessment details and the auditor's geo-tagged visit details go in first.
- **Action taken report**, validated by the same auditor, if there were non-compliances: by 30 September; by 28 February for the half year.

**MTF**: a member approved for margin trading files the September half-year net worth certificate within 45 days (by 14 November); others within 60 days (29 November). The March certificate is due within 60 days (30 May) for everyone (NSE/INSP/70505).

## shared-filing
title: Submissions filed with NSE only
screen: circulars
roles: Compliance officer
ask: file with nse only; common submissions; single filing exchanges; net worth certificate to which exchange; annual return to bse; technology based sharing mechanism; nse shares with bse
related: compliance-profile

A member of NSE that is also a member of BSE, MSE, MCX or NCDEX files the common submissions with NSE only, and NSE shares them (NSE/INSP/69184 of 18 July 2025, 40 items from 1 August 2025; NSE/INSP/70505 of 29 September 2025: 28 items from 15 October 2025, and the half-yearly net worth certificate and annual returns from 15 December 2025). The list includes the holding statement, reporting of bank and demat accounts, the cyber security and VAPT reports, the SaaS and AI/ML reports, and the net worth certificate.

In the compliance calendar these duties appear once and say **File with NSE (shared with the other exchanges)** when the firm has an NSE member code and another exchange's in Settings. A firm that is not an NSE member sees **File with each exchange**. Bank account reporting to NSE uses the revised undertaking, authorisation and bank confirmation formats from 15 October 2025 (Annexures B to D of NSE/INSP/70505); Ledgerline does not print these letters.

The yearly duty **Confirm your banks submit daily client bank balances to the exchange** remains: members stopped sending daily bank balances from 30 October 2023, but must ensure their banks send them to the exchange on T+1 (NSE/INSP/59044).

## surveillance-return
title: Quarterly surveillance obligation report (Formats I to III)
screen: surv
roles: Compliance officer
ask: surveillance report; quarterly surveillance reporting; surveillance obligation; alert register; format i; format ii; format iii; nil report surveillance; alerts referred to exchange; surveillance return due
related: shared-filing

Each quarter, within 15 calendar days of its end, the firm reports the status of its surveillance alerts to every exchange it trades on (NSE/SURV/48818 and NSE/SURV/49765, in the consolidated surveillance circular NSE/SURV/74008 of 30 April 2026; BSE's guidelines, on BEFS). It is not a monthly register.

- **Format I**, for each alert name: alerts under process at the start of the quarter, new in the quarter, verified and closed, referred to the exchange, and pending at the end.
- **Format II**: each alert referred to the exchange, with the date of the alert, its type, the brief observation and action taken, and the date referred.
- **Format III**: any major surveillance action other than referring alerts.

On [[surv|Surveillance & AML]], **Referred to exchange** records the date and observation for Format II; **Add an action** records a Format III entry. An alert counts as closed in the quarter Ledgerline first sees it Closed (or STR filed). [[reports|Regulatory reports]] → *Surveillance obligation report* downloads the three formats for the quarter ended before the chosen date, with **NIL** where there is nothing to report; the compliance calendar has the quarterly duty.
