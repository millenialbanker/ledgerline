Ledgy help articles: locked sign-ins and password rules, incident follow-up reports, cyber controls (restore drills, vendor exit plans, the SBOM, the clock source, the Admins' activity review), clients' terms and data rights, fraud reports, erasure, CKYC records, the insider trading code's records and staff controls.

# Cyber controls and data protection

## signin-lock-unlock
title: Unlocking a sign-in after wrong passwords, and the password rules
screen: users
roles: Admin, Ops checker, Compliance officer
ask: user locked out; sign-in locked; unlock user; unlock code; too many wrong passwords; forgot password locked; password expiry; password expires every 90 days; password history; cannot reuse password; out of band unlock; captcha; rate limiting sign in; client locked out of portal
related: security-alert-rules, security-incidents, portal-terms-data-rights

Five wrong passwords or codes lock a sign-in. The lock does not end by itself: CSCRF (Annexure-G) asks for an unlock through another channel.

To get back in, the person either:

- clicks **get an unlock code by e-mail** on the sign-in screen. Ledgerline sends a 6-digit code, valid for 15 minutes, to the e-mail the firm holds for them: the employee record for staff, the client record for a client. They enter the code there and sign in.
- asks an Admin (for a client portal login, an Ops checker can do it too). First confirm it is the person: call their registered mobile or see them. Then open [[users|Users]], click **Unlock** and say how you confirmed it. Nobody unlocks their own sign-in.

Ledgerline also refuses an address after 30 sign-in attempts in 15 minutes, which stands in for a CAPTCHA.

Passwords last 90 days, and a new password can't be any of the last 6 (NSE circular 20/2026, Annex D). When a password is older, the person must choose a new one at the next sign-in. Change these on [[incidents|Incidents]] → **Rules and contacts**. You can also go back to the old 15-minute lock there, but CSCRF asks for the out-of-band unlock.

Logins to Finspot, the exchanges' portals, CDAS and the firm's e-mail follow those systems' own rules. Those are the firm's to check (duty CC-T-IBT-CONTROLS).

## incident-follow-ups
title: Follow-up reports after a cyber incident, and the time to take it on
screen: incidents
roles: Compliance officer, Admin
ask: incident follow up report; interim report t+3; rca 30 days; root cause analysis incident; vapt after incident; forensic report 75 days; sebi incident portal 24 hours; cdsl incident report same day; mitigation report; incident sla; investigate alert time; data protection board breach 72 hours; personal data breach; tell clients about breach; declare disaster; rto 2 hours rpo 15 minutes
related: security-incidents, cyber-controls, security-alert-rules

Once you record the first report of an incident to CERT-In, SEBI, an exchange or a depository, the incident shows its follow-up reports, each with its date counted from the day it was detected:

- **SEBI's incident portal** within 24 hours, then updates until it is resolved.
- For a depository participant, an **immediate mitigation report** the same day (CDSL Annexure A Table 2).
- **Interim report** by day 3, **mitigation measures** by day 7, **root cause analysis** by day 30, **VAPT of the incident and closure** by day 45 (CSCRF Annexure-O).
- **Forensic audit report** by day 75, where a forensic audit is needed. If none is needed, click **Not needed** and say why.

Record each one with **Reported to CERT-In / SEBI**, choosing **Which report**. A late report goes on the compliance officer's work queue.

**Personal data breach.** If clients' or staff's personal data was affected, click **Personal data breach**. From 13 May 2027 the DPDP rules ask you to tell the Data Protection Board without delay, send its detailed report within 72 hours, and tell each affected person. **Tell affected clients** e-mails each client what happened, what it may mean for them, what the firm did, what they can do and whom to contact.

**Time to take it on.** CSCRF asks for every alert to be looked into within a set time. An incident must be taken on (**Take it on**, or moved to **Investigating**) within 1 hour for Critical, 4 for High, 24 for Medium, 72 for Low and a week for Info. If not, the Admin gets a work item.

**Disaster.** If critical systems are down, click **Declare a disaster** within 30 minutes of the disruption (SEBI circular 2025/119). When they are back, click **Systems back**. Ledgerline shows whether the 2-hour recovery time was met.

## cyber-controls
title: Cyber controls: restore drills, vendor exit plans, SBOM, clock source and the Admins' review
screen: cyber
roles: Compliance officer, Admin
ask: restore drill; technical glitch framework; 10000 clients; backup restore test; dr drill; ransomware drill; rto rpo; exit plan vendor; vendor lock in; concentration risk; switch software vendor; source code escrow; sbom; software bill of materials; cyclonedx; ntp; time server; nic npl time; clock synchronisation cert-in; review admin activity; privileged user review; log retention two years
related: incident-follow-ups, backups, full-data-export

Open [[cyber|Cyber controls & data protection]].

- **Restore drills.** Restore a backup into a test environment at least twice a year (SEBI cloud framework s.6.2.12). Include a ransomware drill that restores from an isolated or immutable copy. Click **Record a drill** and give when it started, when the system was back, and how recent the restored data was. Ledgerline works out the recovery time and the data lost against the targets for critical systems: 2 hours and 15 minutes. After six months without a drill, the Admin gets a work item.
- **Vendors & exit plans.** For each technology provider (the trading front end, RMS, cloud hosting, Ledgerline), record what makes leaving hard, what else depends on the same provider, who could take over, how the data comes out, the exit steps, the contract terms and any source code escrow. Review it every year. Ledgerline's own row is filled in from the full export and data exit it offers.
- **Ledgerline SBOM** downloads Ledgerline's software bill of materials (CycloneDX 1.5 JSON) from the modules built into the running server. Give it to your auditor. Get Finspot's SBOM from Finspot, or record the proprietor's approved exception.
- **Clock source** shows which time servers the Ledgerline server follows. CERT-In asks for NIC's or NPL's servers (samay1.nic.in, samay2.nic.in, time.nplindia.org). Your own computers are your duty.
- **Admin activity review.** Every half year, click **Build the review** for the last half year. It lists each Admin's actions. Someone other than that Admin signs each one off (CSCRF PR.AA.S11).

- **Technical glitch framework.** Ledgerline counts the registered clients (closed accounts left out) on 31 March. With more than 10,000 and online trading, SEBI's technical glitch framework applies, and the compliance officer gets a work item in April. Whether an exchange names the firm is the firm's to check.

Security logs are kept for 730 days by default, because CSCRF asks for two years of user access logs. Below that, the Incidents screen warns.

## portal-terms-data-rights
title: Clients' terms, privacy notice and data rights
screen: cyber
roles: Compliance officer, Admin
ask: client terms of use; acknowledge terms first login; privacy notice; dpdp notice; dpdp act; data principal rights; client asks what data we hold; data summary; correct my data; erase my data; withdraw consent; nominee for data rights; data request 90 days; grievance data protection board; consent sensitive personal data branch
related: client-fraud-report, data-erasure, signin-lock-unlock

A client sees the firm's **terms of use** and **privacy notice** at their first sign-in to the portal or the app, and must accept both before going on. They accept again whenever you change either. Each acceptance is kept with the version, the time and the address. Edit both on [[cyber|Cyber controls & data protection]] → **Portal terms & notice**. The notice Ledgerline starts with lists what is held, why, whom it goes to, the client's rights and how to complain, as the DPDP Act asks (its duties apply from 13 May 2027).

In the portal, **My data & security** shows the client:

- what the firm holds about them (masked), why, and whom it goes to;
- their consents. Withdrawing WhatsApp consent takes effect at once;
- buttons to ask for a full summary, a correction, an erasure or a withdrawal, or to complain. Each request goes to the compliance officer with a date 90 days on. Answer it on **Data requests & erasure**; the client gets the answer by e-mail;
- a **nominee for data rights**: someone who may use these rights on the client's death or incapacity. This is not the securities nominee.

For an account opened at a branch, record the signed form's consent to collect bank details, photograph and signature: give the form number when you create the application, or later on the client. Online applications record it from the e-sign.

## client-fraud-report
title: A client reports a fraudulent transaction
screen: cyber
roles: Compliance officer, Admin
ask: report fraud; fraudulent transaction; client says trade not mine; unauthorised trade; payout not requested; shares stolen; client reports fraud in app; fraud report; block account fraud; cyber fraud 1930
related: incident-follow-ups, portal-terms-data-rights

In the portal (and the client app), **My data & security** → **Report a fraudulent transaction** lets a client tell the firm at once about a trade, payout, share transfer, pledge, sign-in or change they did not make (CSCRF PR.AT.S3).

At once:

- the security contacts and the compliance officer get an e-mail, and the compliance officer and the Admin a work item;
- a High security incident opens, with its CERT-In clock;
- if the client asked, their account is blocked from trading and payouts. The risk manager lifts the block once it is checked;
- the client gets the reference by e-mail, and is told to call 1930 or use cybercrime.gov.in for a cyber fraud.

Look into it and close it on [[cyber|Cyber controls & data protection]] → **Fraud reports**, saying what was found. The client gets that by e-mail. Acting on police requests and the national cyber crime portal is the firm's duty CC-T-CYBERFRAUD-LEA.

## data-erasure
title: Erasing a closed client's personal data after retention
screen: cyber
roles: Compliance officer, Admin
ask: erase client data; delete client data; dpdp erasure; right to be forgotten; how long keep client records; retention period closed account; legal hold; ckyc record sharing; kyc from ckyc; onward sharing ckyc
related: portal-terms-data-rights, archive-period

A closed client's records are kept for the firm's record retention: the archive's years, 10 by default and never fewer than SEBI's 8. This also covers the PMLA's 5 years after the account closes. After that, the DPDP Act asks for the personal data to be erased.

On [[cyber|Cyber controls & data protection]] → **Data requests & erasure**, each closed account shows when it was closed and how long it is kept. When it is due, the compliance officer gets a work item. Click **Erase**. This erases the name, PAN, contacts, bank and demat numbers, nominees, tax residency and document names. The ledger and trades stay, without the person. A client tagged LEGAL-HOLD, or under a sanctions case, is not erased.

**KYC from CKYC.** Mark a client whose KYC record came from the CKYC registry (Clients → the client → KYC source: CKYC). The PML Rules (rule 9(1-F)) let such a record be used only to verify identity or address, and not passed on unless the client, SEBI or FIU-IND's Director allows it. Opening that client's KYC documents then asks for the purpose, and the purpose is recorded.

## insider-trading-code
title: The insider trading code: designated persons, declarations and reports
screen: hrcomp
roles: Compliance officer, HR manager
ask: designated person; board approval designated persons; upsi; unpublished price sensitive information; upsi register; structured digital database; pre-clearance validity trading days; seven trading days; contra trade six months; contra trade mutual fund two months; post trade report; decision not to trade; relatives disclosure; material financial relationship; insider trading code
related: own-trading, staff-controls

Under the SEBI insider trading regulations, People → [[hrcomp|Certificates & own trading]] keeps:

- **Designated persons.** Record the criteria the board set with the compliance officer (role, access to UPSI, seniority) and the board's approval, with the date and the resolution: **Record the approval**. Changing the criteria needs a fresh approval. **Designate a person** records the ground for each one.
- **The no-UPSI declaration.** Every pre-clearance request carries the person's declaration that they hold no unpublished price sensitive information.
- **Validity in trading days.** A pre-clearance is valid for the code's days counted in trading days (7 at most), the day of approval being the first when it is a trading day.
- **Contra trades.** An opposite trade within six months (two months for mutual fund units) is approved only with the compliance officer's written relaxation in the note. Any profit goes to SEBI's investor protection fund: that is the firm's step.
- **Post-trade and no-trade reports.** After an approved pre-clearance, the person reports the trade (date, quantity, price) or that they decided not to trade. A pre-clearance that ran out with no report goes to compliance.
- **The yearly disclosure.** Each designated person gives the names and PAN of immediate relatives and of anyone with a material financial relationship, and the phone numbers they use. Schools and past employers are given once.
- **UPSI shared.** **Record UPSI shared** adds who was given UPSI, why, their PAN or another identifier, and whether they were told to keep it confidential. Entries are never changed.

Reviewing the code each year and reporting violations to the exchanges are the firm's duties CC-T-PIT-CODE-REVIEW and CC-T-PIT-VIOLATION.

## staff-controls
title: Staff controls: AML-sensitive positions, the DP compliance officer and photo ID
screen: hrcomp
roles: Compliance officer, HR manager
ask: aml sensitive positions; staff screening aml; suitable and competent staff; dp compliance officer eligibility; compliance officer qualification; graduate two years experience; nism iii-a within one year; photo id dp staff; id proof staff
related: insider-trading-code, staff-master

People → [[hrcomp|Certificates & own trading]] → **Staff controls** lists the gaps:

- **AML-sensitive positions.** SEBI's AML master circular (para 80) asks the firm to know which positions are sensitive to money laundering and that the people in them are suitable and competent. By default these are the compliance officer, DP operations and back office duties, and the principal officer and designated director. Change the list in HR policies. **Record check** keeps what was checked (references, background, AML training) and who checked it. Nobody records their own.
- **The DP compliance officer.** CDSL asks for an employee who is a director, or a graduate with 2 years', or an SSLC holder with 5 years', securities market experience (CDSL master circular 10.1.1). **Record** keeps the eligibility. NISM Series III-A is due within a year of joining.
- **Photo ID of DP staff.** Everyone in DP operations needs an ID proof on file (CDSL operating instructions 17.7.2). Add it to the person's documents as **ID proof**.

Each gap also goes to the compliance officer's work queue once a day.
