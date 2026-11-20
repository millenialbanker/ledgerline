Ledgy help articles: security incidents, the alert rules and contacts, the CERT-In 6-hour report and the report step, and incident notices from Ledgerline.

# Security incidents

## security-incidents
title: Security incidents and the 6-hour report to CERT-In
screen: incidents
roles: Compliance officer, Admin
ask: security incident; cyber incident; report to cert-in; cert-in 6 hours; incident register; cyber attack; hacked; report cyber incident to sebi; cscrf incident reporting; incident deadline; reported to cert-in; incident reference number; record an incident; phishing incident
related: security-alert-rules, provider-incident-notice, log-export-siem, audit-seals

CERT-In's directions of 28 April 2022 ask every intermediary to report a cyber incident to CERT-In within 6 hours of noticing it, or of being told of it. SEBI's cybersecurity and cyber resilience framework (CSCRF) asks for incidents to be reported to SEBI too, and the exchanges and depositories ask for the same.

Open [[incidents|Incidents]]. Each incident shows:

- **Where it came from**: opened by one of Ledgerline's security rules, recorded by the firm, or a notice from Ledgerline about the platform.
- **The CERT-In clock**: when it was detected and when the 6 hours end, in IST, with the time left. A red pill means the 6 hours have passed.
- **The events** behind it, and its history.

To work an incident:

1. Click **Open**, then **Take it on** so others know you are on it. Set the status to **Investigating**.
2. Decide whether it is reportable. The directions list the types of incident (Annexure I), such as unauthorised access, malware, data breaches and attacks on servers. It is the firm's judgement.
3. If it is, report it to CERT-In by e-mail to incident@cert-in.org.in or on CERT-In's website, using its incident reporting form. Report it to SEBI and to the exchanges or the depository as CSCRF and their circulars ask.
4. Click **Reported to CERT-In / SEBI** and record whom you told, when, how and the reference number you were given. A report to CERT-In moves the incident to **Reported**. Record each report separately.
5. When it is dealt with, choose **Closed** and write what was found and done. A closed incident stays in the register.

To add something the firm found itself, such as a phishing mail a dealer opened or a lost laptop, click **Record an incident**. Give the time it was noticed: the clock starts then.

## security-alert-rules
title: Which events open a security incident, and who is alerted
screen: incidents
roles: Compliance officer, Admin
ask: security alert rules; who gets security alerts; security contacts; failed login alert; too many wrong passwords; brute force; alert when admin is added; large download alert; office ip addresses; sign-in from outside office; change alert threshold; one alert per incident; security events
related: security-incidents, log-export-siem, security-log-retention

Ledgerline keeps a security record of every sign-in and failed sign-in, lockouts, two-factor resets, new users and role changes, sign-ins from a new address, large downloads, audit seal checks, malware found in uploads, changes to sign-in settings and connection secrets, and Ledgerline support staff's access. See them under [[incidents|Incidents]] → **Events**.

These rules open an incident:

- Wrong passwords or codes for one user: 5 within 15 minutes, unless the firm sets other numbers.
- Wrong sign-ins from one address: 10 within 15 minutes, or the server refusing an address for too many attempts.
- Any audit trail seal that does not match. The seals are checked every night, and whenever someone clicks **Check seals**.
- Malware found in an uploaded file.
- Someone given the Admin role. The firm can switch this one off.
- One download or export of 50 MB or more, unless the firm sets another size.
- A sign-in from outside the firm's own networks, once the firm lists them. The sign-in is not refused; it is flagged.
- Ledgerline support access without the firm's approval.

A new incident sends one e-mail to each security contact and puts a work item on the compliance officer's and the Admin's queues. Later matches for the same user, address or file join the open incident, so there is one alert, not one per event. Once the incident is closed, the next match opens a new one.

Set the contacts, numbers and networks under [[incidents|Incidents]] → **Rules and contacts**. With no contacts set, alerts go to the compliance e-mail and the management e-mails in Settings.

## provider-incident-notice
title: An incident notice from Ledgerline
screen: incidents
roles: Compliance officer, Admin
ask: ledgerline outage; ledgerline breach; notice from ledgerline; provider incident; cloud provider incident; platform incident; ledgerline was down; service provider breach notice
related: security-incidents, data-in-india

SEBI's cloud framework asks a cloud service provider to tell the firm promptly of an incident that affects it. When Ledgerline has an outage or a security incident affecting firms, it declares it once and every affected firm is told at the same time:

- It appears in the firm's [[incidents|Incidents]] register as **Notice from Ledgerline**, with Ledgerline's description of what happened and when it started.
- Each security contact gets an e-mail, and the compliance officer and the Admin get work items.
- Each update from Ledgerline is added to the incident's history and e-mailed. Ledgerline's own status is shown beside the firm's.

The firm's own record stays the firm's: decide whether it is reportable, record any report to CERT-In or SEBI, and close it when the firm is satisfied. The 6-hour clock starts when Ledgerline's notice arrived, since that is when the firm was told of it.
