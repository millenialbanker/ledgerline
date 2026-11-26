## cyber-resilience
title: Recovery point, intraday backups and capacity
screen: cyber
roles: Compliance officer, Admin
ask: rpo 15 minutes; rto 2 hours; recovery point objective; how much data could we lose; intraday backup; backup every 15 minutes; continuous backup; wal archiving; point in time recovery; capacity monitoring; threshold alert; server load; data growth forecast; backup taking too long
related: backups, cyber-controls, technical-glitches

[[cyber|Cyber controls]] → **Recovery point** shows the targets (RTO 2 hours, RPO 15 minutes for a critical system unless you set others with a reason) and what Ledgerline actually achieves, measured from the backups: the newest good backup, the worst gap between good backups in the last 7 days and how long a backup takes.

Ledgerline backs up after each day's close. A failure during the day would then lose the day's work, which is hours, not 15 minutes. To do better, switch on **intraday backups** (every 5, 10, 15, 30, 60, 120 or 240 minutes, inside a time window, on trading days). Each is a full, encrypted, checked copy; the last 16 are kept. The data at risk is the interval plus the time a backup takes, so a 15-minute RPO needs backups every 10 minutes or less. Short intervals cost disk and some load.

If the operator runs the database with continuous archiving (PostgreSQL WAL), the screen shows it as **on** and when it last archived. That covers the whole server and only the operator can restore from it.

**Capacity** shows, for this firm's Ledgerline: requests a day, the busiest minute, the slowest quarter hour (95th percentile answer time), server errors, the data size, its growth a month and where that leads in three years. Set the thresholds under **Change**; when one is crossed the Admin gets a work item.

## cyber-evidence-access
title: Ledgerline's evidence, the test environment, inspections, agreements and API callers
screen: cyber
roles: Compliance officer, Admin
ask: sast dast evidence; vapt of ledgerline; release test evidence; evidence pack; cloud framework principles; cloud principles 1 to 9; test environment; uat; sandbox; non production environment; inspection by exchange; inspector access; mcx inspection back office; confidentiality agreement; nda staff; hold login until nda; third party staff agreement; api allow list; whitelist api; rate limit api; webhook allowed addresses; rooted phone refuse; jailbroken phone block; qualified stock broker vapt half yearly; qsb cyber audit; log retention 2 years
related: cyber-controls, cyber-resilience, full-data-export, staff-app-setup

**Ledgerline evidence** lists the tests Ledgerline recorded for each of its releases (SAST, DAST, VAPT, functional audit) and says which are missing for the release you run. **Download the evidence pack** gives your auditor one file: the release evidence, the SBOM, the recovery point, capacity, and SEBI's cloud framework principles 1 to 9 with what Ledgerline shows for each and what stays the firm's part. Your own VAPT and cyber audit are separate. The tab also says your VAPT and cyber audit cycle: every half year for a Qualified Stock Broker (Settings → Firm modules), otherwise by CSCRF category.

**Test environment.** If Ledgerline set up a test firm code for you, **Prepare the test environment** makes you a login there (a one-time password) and loads a sample trading day. Use it for practice and for inspectors' hands-on testing. Never put production credentials in it.

**Inspection access.** The procedure to follow when SEBI, an exchange or a depository inspects is on the tab. **Record a visit**: who came, when, and what they were given (supervised screens, an inspection pack, a full data export, the test environment). Close it with what they observed.

**Confidentiality agreements.** Record each login's signed confidentiality and integrity agreement, for staff and for third parties' staff. The joining checklist has the item too; ticking it records the agreement. With **Hold new logins** on (Settings), a login made after that cannot sign in until its agreement is recorded; existing logins are not affected.

**API callers.** In Settings, list the addresses your API keys and your webhooks may be called from, and the calls one key may make in a minute. Empty lists let any address call. A refused call is a security event.

**Rooted or jailbroken phones.** The apps warn by default. To refuse such phones, choose **Refuse the phone** on Settings → Staff app.

**Security logs** must be kept two years for Small, Mid and Qualified REs. If yours are kept for less, the tab says so.
