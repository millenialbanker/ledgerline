Ledgy help articles: the DP's own surveillance rules, CDSL's and MCX's alert files with the download log, the checklist and documents on an alert, adverse observations and their 7 days, the sample of closures, mule accounts, staff front running and the thresholds.

# DP surveillance

## dp-surveillance-rules
title: The DP's own surveillance rules and mule accounts
screen: surv
roles: Compliance officer, Ops checker
ask: dp surveillance; own alerts; cdsl indicative themes; shared pan alert; shared mobile alert; shared email alert; shared bank alert; shared address alert; cluster of similar clients; bounced communications; frequent modification; bo modification; off-market against income; pledge against income; transfer after modification; dormant demat debit; one to several off-market transfers; fan out transfers; reason code mismatch; mule account; pass-through funds; front running; staff trades before client; circular trading; connected clients; group of connected clients; email mobile modified more than twice a month
related: surveillance, cdsl-alert-files, alert-checklist, surveillance-thresholds

CDSL's surveillance framework for DPs (DP2026-521) has each DP raise its own alerts on CDSL's themes. **Run internal rules** on [[surv|Surveillance & AML]] runs them; the autopilot also runs them every evening. Each rule looks back two months, so a late file is still checked.

The rules raise an alert when:

- Several clients share a **PAN, mobile, e-mail, bank or address**. These alerts come every half-year and have 60 days. If more holders share a detail than the cluster size (20 by default), the alert is a **cluster** and is High. One bank for several PANs is High. A mobile or e-mail shared only by declared family members (spouse, dependent child or parent) is Low.
- Statements or notices to the client **bounce**, and more so when a debit follows the bounce.
- The client's details **change** more than the monthly limit (2 by default). Changes of e-mail and mobile count too, so this also covers MCX's alert on more than two in a month.
- **Off-market debits and pledges** in 30 days are more than the declared annual income.
- An off-market debit comes **soon after a change** of mobile, e-mail, address, bank or demat details (30 days by default).
- A debit comes from a **dormant** demat, with no transaction for 180 days by default.
- One holder sends off-market to several others, or several of the DP's holders send to one.
- The **reason code** does not fit: a gift with consideration, a sale under half the market value, or a transfer "to own account" that goes to another person.
- Money **passes through**: paid in, then mostly paid back out, with little trading. This may be a mule, used by another person to deal (SEBI's PFUTP Regulations).
- A **staff** member trades shortly before a large client order in the same contract and direction (possible front running).
- **Connected clients** trade against each other in the same contract on the same day (possible circular trading).

**Connected clients** lists the groups of clients joined by a shared mobile, e-mail, bank or address.

## cdsl-alert-files
title: Load CDSL's surveillance and FIU alerts and MCX's MRS alerts
screen: surv
roles: Compliance officer, Ops checker
ask: cdsl surveillance alerts; cdas surveillance; cdsl fiu alerts; fiu inquiry download; download cdsl alerts; mcx mrs alerts; mcx transactional alerts; alert download log; no alerts today; nil download; principal officer download fiu alerts; designated director fiu alerts
related: dp-surveillance-rules, alert-checklist, fingate-str

CDSL's Master Circular for DPs (14.5.8 to 14.5.12) asks the Designated Director and the Principal Officer to make sure CDSL's FIU alerts are downloaded and analysed. Download both lists from CDAS: **CDAS Surveillance** and **Surveillance FIU Inquiry / Download**. MCX sends its transactional alerts on MRS.

1. On [[surv|Surveillance & AML]], under **CDSL and MCX alert files**, pick the list and load the file. CSV, tilde, pipe or tab files with a heading row are accepted. Ledgerline finds the alert number, date, type, BO ID or client code, ISIN, quantity, value and period by their headings. It keeps the rest of the row with the alert.
2. Each row becomes an alert, raised once. An alert has 45 days, or 60 for a half-yearly multiple-account alert, or CDSL's own due date if that is earlier. A row whose account is not on record is still raised, without a client, and is listed for you.
3. If there were no alerts, click **Nothing to download**. That keeps the log complete.

The **download log** shows every load and every nil record, with who did it and when. If a CDSL list has not been loaded for 7 days (you can change this in the thresholds), a work item is raised and the self-inspection flags it.

CDSL and MCX have not given Ledgerline their file layouts. The headings are read as they come, and the layouts are marked unconfirmed. A file Ledgerline cannot read is refused, with a list of the headings it looks for.

## alert-checklist
title: Work an alert: the checklist, documents and due date
screen: surv
roles: Compliance officer, Ops checker
ask: alert checklist; nine point checklist; cdsl checklist; bo explanation; source target relationship; supporting documents; attach documents to alert; alert due date; 45 days; 60 days; half yearly alert; mcx alert checklist; bank statement 15 days; warehouse receipts; close cdsl alert
related: surveillance, adverse-observation, cdsl-alert-files

Every CDSL alert and every DP alert carries CDSL's nine-point checklist. An MCX alert carries MCX's checklist: the client's explanation, bank statements for 15 days before and after, warehouse receipts, and the findings. Open the alert with **Work**:

1. Mark each point **Done** or **Not applicable** and add a note. A point that is not applicable needs a reason.
2. **Attach** the documents: PDF, JPEG or PNG, up to 5 MB each. Examples are the gift deed, the client's letter or a bank statement. "Documents attached" can be marked Done only once a document is attached.
3. **Close** with the rationale, record an **adverse observation**, or **File STR**. You cannot close until the checklist is worked. The last point, "filed within the due date", is filled in when you answer. If you answer late, it records by how many days.

The due date is on each alert: 45 days from the alert, or 60 for CDSL's half-yearly multiple-account alerts. The nightly self-inspection flags alerts past their due date. It also flags closed alerts whose checklist was not worked.

An STR on a CDSL alert must cite the alert, so record the DP's analysis and the BO's explanation on the checklist first. If asking the BO would tip them off, mark the explanation **Not applicable** and say so. The STR then quotes CDSL's alert number and date, the ISIN, quantity, value and period, the client's KYC, the analysis and the explanation. If the client tried a transaction and did not go through with it, tick **Attempted transaction** on the STR.

## adverse-observation
title: Report an adverse observation to CDSL within 7 days
screen: surv
roles: Compliance officer
ask: adverse observation; report to cdsl; cdsl surveillance system; 7 days adverse; two months identification; own observation; report to mcx; do not re-report cdsl alert; quarterly status cdsl; cdsl quarterly surveillance report; surveillance status 15 days cdsl
related: alert-checklist, surveillance-return, alert-sample

When the checklist shows that the activity is adverse, record an **Adverse observation** on the alert. Write the analysis, the rationale and the action taken. Only the compliance officer can do this.

- It is then due with CDSL within **7 days**. Ledgerline counts calendar days, the stricter reading. A work item tracks it.
- For the DP's **own** alerts, the observation must be made within **two months** of the transactions. After that, Ledgerline asks why it is late.
- For a **CDSL** alert, answer CDSL's alert as adverse on CDSL's system. Do not report it again as the DP's own observation.
- For an **MCX** alert, report it to MCX, within the 45 days.

Once it is reported, record the date and the reference CDSL gave. The alert then counts as disposed of. Each quarter, [[reports|Regulatory reports]] → *DP surveillance alert status for CDSL* gives the counts and the adverse reports. Use it to fill in CDSL's quarterly form within 15 days of the quarter's end. It is a working sheet, not CDSL's own layout. CDSL has penalty provisions for lapses in surveillance duties (DP2026-236).

## alert-sample
title: Check a sample of alert closures each quarter
screen: surv
roles: Compliance officer
ask: sample of closures; compliance officer sample; check closed alerts; alert closure review; quarterly sample
related: adverse-observation, surveillance-thresholds

After each quarter, Ledgerline picks a sample of the alerts closed in it. By default the sample is 10%, and at least 5. The draw is fixed, so nobody chooses which alerts are checked. Open **Sample of closures** on [[surv|Surveillance & AML]]. For each alert, record **Agree**, or **Disagree** with what is wrong and what is to be done. A disagreement raises a work item. A work item reminds you until the sample is checked. The self-inspection flags unchecked samples once the quarterly status is due.

## surveillance-thresholds
title: Set the surveillance thresholds and the monitoring by risk category
screen: surv
roles: Compliance officer
ask: surveillance thresholds; alert thresholds; risk based thresholds; monitoring by risk category; high risk monitoring; change dormant days; cluster size; pass through threshold; front running minutes; download interval
related: dp-surveillance-rules, alert-sample

**Thresholds** on [[surv|Surveillance & AML]] sets the limits of the DP's own rules. These are the income multiple, changes per month, days after a change, dormant days, fan-out count, cluster size, pass-through amount and shares, front-running minutes and value, the download interval and the sample size.

Each value and count limit is scaled by the client's **risk category**: Low 100%, Medium 75% and High 50% by default. A high-risk client is watched more closely. The defaults are a starting point. Set the values your surveillance policy approves, and record the policy's date in its notes.
