Ledgy help articles: automation, messages and administration.

# Automation and messages

## autopilot
title: Set up Autopilot
screen: autopilot
roles: Admin, Compliance officer
ask: autopilot; automation; turn on autopilot; automate daily work; auto mode; suggest mode; switch off automation; automation settings; payout auto approve limit
related: work-queue, scheduled-jobs

On [[autopilot|Autopilot & work queue]]:

1. Each routine process has a mode: **Auto** (does it and records it), **Suggest** (prepares it for a person to approve) or **Off**. Some can't be set to Auto because the rules need a person; the screen says why.
2. Set limits where shown, such as the payout amount Autopilot may approve, then **Save automation settings**. Changing automation needs an Admin or compliance officer.
3. Switch on the **schedule** so the stages (start of day, intraday, pre-close, end of day, month end) run by themselves on business days, or **Run stage now**.

## work-queue
title: Work the work queue
screen: autopilot
roles: Ops maker, Ops checker, Risk manager, Compliance officer
ask: work queue; my tasks; pending items; what do i need to do; queue item; to do list; exceptions; overdue items
check: work
related: autopilot

The work queue on [[autopilot|Autopilot & work queue]] holds only what needs a person: each item says who should act (by role), by when, and why.

1. **Open** an item. Where Autopilot has prepared the action (for example "Set to High risk" or "Post all"), check it and click it.
2. If you handled it some other way, click **Done elsewhere** with a note.
3. Items close themselves when the condition clears (a break resolved, a payment received).

Ask me "what's in my queue?" and I'll list yours.

## scheduled-jobs
title: Schedule a job
screen: jobs
roles: Admin
ask: scheduled job; schedule report email; monthly statements job; job failed; run job now; job not running; cron
related: autopilot

On [[jobs|Scheduled jobs]]:

- Jobs: the Autopilot stages, debit reminders, daily margin statements, Periodic work, the KRA upload file and emailed reports to management.
- **New job** (Admin) or **Edit**: time (IST), which days, and who is emailed on success or failure.
- **Run now** to run one immediately. Each run is recorded with its result.

Contract notes, margin calls, KYC reminders, inactive flagging and interest are Autopilot steps, not jobs. The weekly, monthly and DP statements are Periodic work (one job runs it every morning); old statement jobs are hidden and never run, so nothing goes twice.

## message-templates
title: Change a message template
screen: messages
roles: Ops checker, Compliance officer
ask: message template; edit sms; email template; dlt template id; whatsapp template; change message text; sms not sent; promotional sms
related: whatsapp-consent, bulk-message

On [[messages|Client messages]]:

1. Click **Edit** on a template, or **New template** for an event and channel (email, SMS or WhatsApp).
2. **SMS:** the text must match the DLT-registered template exactly, and the DLT template ID must be filled in, or the telecom operator blocks it.
3. **WhatsApp:** the template name must be approved by Meta.
4. **Test** sends it to yourself.

Promotional SMS is held outside 9 am to 9 pm. Until a provider is connected, messages wait in the outbox here.

## bulk-message
title: Send a message to many clients
screen: messages
roles: Ops checker, Compliance officer
ask: bulk message; message all clients; send circular to clients; mass email; broadcast; notify clients
related: message-templates, whatsapp-consent

On [[messages|Client messages]], click **Bulk message**, choose the clients (all, a group or a list), the channel and the text. WhatsApp goes only to clients who opted in; promotional SMS follows the time window.

## client-insights
title: Use client insights
screen: insights
roles: Ops maker, Ops checker, Compliance officer
ask: client insights; tax loss harvesting; buyback alert; idle funds; advance tax reminder; opportunities for clients; insights not sent
related: buyback, year-end-packs

On [[insights|Client insights]], Ledgerline finds opportunities in each client's own account every evening: tax losses before year end, buy-backs above market on shares they hold, stock derivatives that will settle by delivery, advance tax, dividends, idle funds, margin from holdings, accounts about to turn inactive.

- In **Auto**, clients get the approved message; in **Suggest**, they wait here. **Send**, **Call** (and **Log call**) or **Dismiss**.
- **Find opportunities now** runs the scan immediately; **Automations** sets each kind's mode.

Messages give facts about the client's own account, never advice.

## year-end-packs
title: Send year-end tax packs
screen: yearend
roles: Ops maker, Ops checker, Compliance officer
ask: year end pack; tax statement; form 10db; stt certificate; capital gains for itr; annual statement
related: client-reports

On [[yearend|Year-end packs]], choose the financial year. Each client's pack has capital gains, the F&O result, charges, closing balance and holdings, and the STT particulars for **Form 10DB**, one per exchange. **Email all clients** after 31 March, or **Open** and **Email** one. Clients also open it in their portal.

## leads
title: Track leads
screen: crm
roles: Ops maker, Branch staff, Authorised person
ask: lead; new lead; prospect; follow up lead; convert lead; appointment; crm
related: new-client, online-opening

On [[crm|Leads & CRM]]: **New lead**, then **Call**, **Book appointment** and **Set stage** as you follow up. **Open account** converts the lead, and the online application link carries it so it's marked converted when they apply.

# Administration

## add-user
title: Add a user
screen: settings
roles: Admin
ask: add user; new user; create login; new employee; give access; staff login; user roles; change user role
related: ap-login, reset-password, reset-2fa, permission-denied

1. On [[settings|Settings]], click **Add user**.
2. Enter their ID, name, email and **role**:
   - **Ops maker:** enters work (clients, receipts, vouchers, slips)
   - **Ops checker:** approves others' work, activates clients, closes the day
   - **Risk manager:** limits, margin, square-off, payouts
   - **Compliance officer:** KYC risk, surveillance, complaints, reports
   - **Admin:** users and settings
   - **Branch staff** / **Authorised person:** their own clients only
3. Give them the one-time password through a separate channel. At first sign-in they set a password and two-factor sign-in.

Use one login per person and never share logins: the audit trail and maker-checker depend on it.

## reset-password
title: Reset a user's password or unlock them
screen: settings
roles: Admin
ask: forgot password; reset my password; change my password; reset password; user locked out; account locked; cannot log in; too many wrong passwords; password expired
related: reset-2fa, portal-access

- **Your own password:** click your name at the top right → **Change password**.
- **Locked out after five wrong passwords:** the lock lifts by itself after 15 minutes.
- **Forgotten password:** an Admin opens [[settings|Settings]] → the user → **Reset password**. A one-time password is shown once; the user must change it at sign-in.

For a client's portal password, see "give a client portal access".

## reset-2fa
title: Reset two-factor sign-in
screen: settings
roles: Admin
ask: lost phone; authenticator app; two factor code not working; reset 2fa; new phone; otp app; totp wrong
related: reset-password

1. Check the phone's clock is set automatically. Codes fail when the time is off.
2. If the phone is lost or replaced, an Admin opens [[settings|Settings]] → the user → **Reset two-factor**.
3. At next sign-in the user scans a new QR code with their authenticator app.

## permission-denied
title: Fix "needs a different role" (permission errors)
screen: settings
roles: Admin
ask: not permitted; permission denied; needs ops checker; you are ops maker; forbidden; not allowed for this role; cannot approve; access denied; who can approve
related: add-user

An error like "**Approving payouts needs Ops checker or Risk manager (you are Ops maker)**" means your role can't do that step. That's deliberate: one person makes, another approves.

1. Ask a colleague with the role named in the message to do it.
2. If nobody in the firm has that role, an Admin assigns it on [[settings|Settings]].
3. You can't approve your own work even with the right role. A different person must check it.

Ask me "who can approve payouts?" (or anything else) and I'll tell you the roles.

## settings-overview
title: Change firm settings
screen: settings
roles: Admin
ask: settings; firm details; holidays; interest rate setting; gst rate; snapshot times; inactive months; session timeout; management emails; cheque return charge
related: period-locked, add-user

On [[settings|Settings]] (Admin only): firm details and SEBI registration; risk and settlement (watch threshold, RAS buffer and dates, margin snapshot times, exchange holidays, inactivity period); debit interest and GST; period locking; receipts and payouts; sign-in security (two-factor, session timeout); webhook secrets; management emails. **Save settings**: every change is in the audit trail.

## sample-data
title: Load sample data to practise
screen: dashboard
roles: Admin
ask: sample data; demo data; practice; test data; load sample day; sample week; try the software
related: daily-routine

On a development server, the [[dashboard|Dashboard]] offers **Load sample trading day** (20 sample files for one day) and **Run a sample week**. **Run a sample day on autopilot** on [[autopilot|Autopilot]] shows the automation end to end. Sample data never appears on a production server.

## editions
title: Which edition are we on, and what does it change?
screen: settings
roles: Admin
ask: edition; manual edition; autopilot edition; full autopilot; upgrade edition; why is autopilot missing; no autopilot screen; automation not available
related: autopilot, scheduled-jobs

Your edition is shown under the firm name in the sidebar and on [[settings|Settings]]. Your Ledgerline provider sets it.

- **Manual:** every step is a person's click. There's no Autopilot screen, no work queue and none of the firm's work runs on a timer; jobs can be run by hand with **Run now**. Backups and the exchange holiday check still run, because they keep data safe and change nothing until someone approves.
- **Autopilot:** routine work runs by itself where it's safe; people are asked whenever judgement or approval is needed.
- **Full autopilot:** everything runs automatically at the most the rules allow. People still do what the regulations reserve for them: activating and reactivating clients, surveillance decisions and STRs, verifying physical slips, client code modifications, bill corrections and overrides, bank, address and nomination changes, MTF enrolment, PMLA risk categories and firm vouchers.

Maker-checker and every rule are the same in all three. To change edition, ask your provider.

## data-in-india
title: Does our data stay in India?
screen: audit
roles: Compliance officer, Admin
ask: data in india; data localisation; data residency; where is data stored; data sovereignty; cscrf cloud; is data sent abroad; india only mode
related: audit-seals

Open [[audit|Audit trail]] and click **Check seals**. The Data protection panel lists **where data is kept and sent**, line by line: hosting, keys, pages, Ledgy, account-opening checks, messaging. It also shows whether **India-only mode** is on.

- Ledgerline keeps everything in its own database and serves its own pages and fonts. Browsers contact no other site.
- Data leaves only through providers you connect. Each is listed with whether it is in India.
- With India-only mode on, the server refuses to start with a hosting region outside India or with Ledgy's AI phrasing (which uses Anthropic's API in the USA).

Hosting, backups and the standby copy must also be in an Indian region. That's set up by whoever runs the server.

## ai-credit
title: How does AI credit for Ledgy work?
screen: settings
roles: Admin, Compliance officer
ask: ai credit; ledgy credits; ai answers cost; buy ai credits; ai credit ran out; ai usage; ai charges; top up ai; recharge ai
related: settings-overview

Ledgy always answers from its help articles at no cost. If your firm switches on **Let Ledgy write answers with AI** in [[settings|Settings]], an AI model rewrites the answer in plainer words, and each AI answer is charged from your firm's **prepaid AI credit** by the tokens it used.

- **AI credit and usage** in Settings shows the credit left, AI answers this month, the charge (plus GST), the most one answer can cost, each answer and every top-up.
- When credit runs out, Ledgy keeps answering from its help articles without AI and says so.
- Credit is added by your Ledgerline provider after payment. Ask them for a top-up.
- Only the question (with PAN, numbers and emails masked) and the help articles are sent to the AI provider; questions aren't stored with the charges.

## customize-dashboard
title: Customize your dashboard
screen: dashboard
roles: Ops maker, Ops checker, Risk manager, Compliance officer, Admin
ask: customize dashboard; customise dashboard; add widget; remove widget; move widget; resize widget; rearrange dashboard; dashboard layout; drag and drop dashboard; reset dashboard; firm default dashboard; change dashboard
related: daily-routine

On the [[dashboard|Dashboard]], click **Customize**:

1. **Move** a widget: drag it by its title bar. Or click its title bar and use the arrow keys.
2. **Resize** it: drag its bottom-right corner, use **Shift** and the arrow keys, or click **⋯** for Wider, Narrower, Taller and Shorter.
3. **Add widget** opens the list of all 23 widgets, from day progress and margin breaches to top clients, most traded contracts, settlement and client money segregation. **×** removes one.
4. Click **Done**. Changes save as you go, only for you.

**Reset** goes back to the firm's default layout. An Admin can click **Save as firm default** so new users, and anyone who resets, start from their layout; people's own layouts aren't changed.

## backups
title: How are backups taken, and what if one fails?
screen: backups
roles: Admin, Compliance officer, Ops checker
ask: backup; backups; database backup; daily backup; weekly backup; quarterly backup; yearly backup; backup failed; restore; restore backup; data recovery; disaster recovery; how long are backups kept; backup retention; back up now
related: eod-close, exchange-holidays, data-in-india

After each close (the **equity close** in the evening and the **commodity close** after MCX's session), the whole firm is backed up: clients, ledgers, trades, files, settings and the audit trail. The backup is encrypted with the firm's data keys and read back to check it before it counts. Open [[backups|Backups]] to see them.

- **Daily, weekly, quarterly, yearly:** every backup is a daily one. The backup of the **last trading day** of a week, a quarter or the year (31 March, or 31 December if set) is also kept as weekly, quarterly or yearly. Last trading days follow the approved exchange holiday calendar.
- **Kept:** 30 days of daily backups (both closes of each day), 13 weekly, 12 quarterly and 10 yearly unless an Admin changes it under **Retention**. Keep yearly backups for as many years as your records must be kept.
- **A part not closed** is backed up anyway: equity at 23:30, commodity at 00:30 the next morning (after MCX's session). It is backed up again once it is closed. Both times can be changed under **Retention**.
- **A failed backup** is retried every 15 minutes, four times. Management is emailed, and the dashboard shows it under **Needs attention**. Fix the cause (usually disk space or the backup folder), then an Admin clicks **Back up now**.
- **Check again** reads a backup end to end to prove it opens and is complete.
- **Restoring** is done by whoever runs the server, into an empty database, with the same data keys. It never overwrites data. See the **Restoring** panel.

Ask the people who run the server to copy the backup folder off the server every day, to storage in an Indian region.

## exchange-holidays
title: Load and approve exchange holidays
screen: circulars
roles: Ops maker, Ops checker, Compliance officer
ask: exchange holidays; holiday list; trading holidays; settlement holidays; clearing holidays; holiday calendar; import holidays; nse holidays; bse holidays; mcx holidays; next year holidays; approve holidays; pay-in date wrong; business day wrong; market holiday
related: settlement, backups, settings-overview

Open [[circulars|Circulars & calendar]] → **Exchange holidays**.

1. NSE's trading and settlement (clearing) holidays for every segment are fetched by themselves: every morning in December until next year's list is found, and every Monday for changes (for example a holiday added for an election). **Check NSE now** fetches them at once.
2. BSE's equity, derivative and currency lists follow NSE's (the exchanges publish matching circulars). For **MCX**, save its circular's holiday table as a CSV (Date and Description columns) and click **Upload a circular**.
3. A new or changed list waits for approval. An **Ops checker or the compliance officer** checks it against the circulars and clicks **Approve**. Whoever uploaded a list can't approve it.

Once approved, equity trading holidays decide business days, and settlement holidays also move the **pay-in** date (for example, a bank holiday that is a clearing holiday but a trading day). Holidays typed in Settings still count on top. The dashboard warns when a year has no approved list.

## connect-digilocker
title: Connect DigiLocker and Aadhaar offline e-KYC for online account opening
screen: apps
roles: Admin
ask: aadhaar offline ekyc; offline aadhaar xml; no digilocker; applicant has no digilocker; uidai certificate; share code; connect digilocker; digilocker not connected; aadhaar kyc; ledgerline kyc; kyc plugin; api setu; digilocker registration; online opening needs digilocker; why no ipv; in person verification

Online account opening takes the applicant's Aadhaar from DigiLocker (with their PAN record) or, for applicants who do not use DigiLocker, from an Aadhaar offline e-KYC file they download from UIDAI's myAadhaar with a 4-digit share code. That is why there is no in-person or video verification (SEBI's KYC circular SEBI/HO/MIRSD/DOP/CIR/P/2020/73). Applicants without a DigiLocker account can also create one inside the DigiLocker step.

1. Register your firm as a DigiLocker requester on API Setu (partners.apisetu.gov.in): you need the firm's GSTN, a nodal officer and a server in India. Each firm uses its own registration.
2. Set the redirect address in the API Setu partner account to your Ledgerline KYC address followed by `/callback`.
3. Give the client ID and secret to whoever runs Ledgerline KYC for you; they add your firm and give you an API key.
4. For the offline e-KYC file, whoever runs Ledgerline KYC adds UIDAI's offline e-KYC certificate (from UIDAI's website); no registration with UIDAI is needed. Applicants then see both choices at the address step.
5. Your Ledgerline administrator sets `LEDGERLINE_KYC_URL` and `LEDGERLINE_KYC_KEY` and restarts. Applicants then see **Continue to DigiLocker** at the address step.
6. Penny drop, face match and Aadhaar e-Sign still need their own vendors; until connected, those checks show **Check manually** and applications wait for a checker instead of activating by themselves.

## team-messages
title: Message colleagues, branches and APs
screen: chat
roles: Ops maker, Ops checker, Risk manager, Compliance officer, Branch staff, Authorised person, Admin
ask: team messages; message a colleague; chat; send a message to; talk to head office; talk to risk desk; message the branch; message an ap; direct message; mention someone; @all; send a photo; send a file; attach screenshot; mute notifications; turn off notifications; new work notification; ledgy message
related: manage-channels, work-queue

On [[chat|Team messages]]:

1. **Channels** (#name) are set up by the Admin; only their members can read them. Head office and each branch have their own.
2. **New message** writes to one person: anyone at head office if you are at head office, anyone in your branch if you are at a branch.
3. **Write to a desk** (branches and APs) opens your private thread with the Risk, DP or Compliance desk; head office desk staff use **Write as a desk** to start one with any branch or AP. Other branches and APs never see your thread.
4. Type **@** to mention someone; **@all** tells everyone in a channel or desk thread. Mentions are highlighted for the person.
5. Attach photos or files with the paper clip, by dragging them in, or by pasting a screenshot: photos, PDF, Excel, Word, PowerPoint, CSV, text and ZIP, up to 15 MB each.
6. Every new message pops up in the corner of whatever screen you are on, with a sound, and shows on the Team messages badge and the browser tab. Notifications cannot be switched off. Allow desktop notifications when the browser asks, to get them even when Ledgerline is in the background.
7. **Ledgy** writes to you when the work queue gives you something new, with a button to the right screen.

Messages cannot be edited or deleted: they are kept as the firm's record.

## manage-channels
title: Create channels and staff the desks
screen: chat
roles: Admin
ask: create channel; new channel; add member to channel; remove member from channel; archive channel; who can see channel; create desk; risk desk; dp desk; compliance desk; who answers the desk; add someone to desk
related: team-messages

On [[chat|Team messages]], click **Channels & desks**:

1. **New channel**: give it a name, choose head office or a branch, and tick the members. Only they can read it; head office channels take head office staff, a branch's channels take that branch's staff.
2. **Edit** a channel to change its members or archive it (members can still read an archived channel but not write). Someone removed can no longer read it.
3. **Official desks** answer branches and APs: tick the head office staff who answer for each. The Risk, DP and Compliance desks start with the people in those roles; a desk with nobody on it is answered by the Admins. **New desk** adds another (Accounts, say).

You manage channels without reading them; add yourself as a member to read one.

## automations
title: Record and run an automation
screen: rpa
roles: Ops maker, Ops checker, Risk manager, Compliance officer, Branch staff, Admin
ask: automation; automate a task; record a task; rpa; robotic process automation; bot; ledgerline bot; download files automatically; exchange portal login automatically; run a workflow; schedule a workflow; workflow failed; workflow waiting for me; captcha; add this computer; pair computer; saved login; save password; password vault; authenticator code
related: work-queue, team-messages

On [[rpa|Automations]]:

1. **Add this computer** once: get **ledgerline-bot** from your administrator, run the pair command shown (the code works once, for ten minutes), then start `ledgerline-bot` and leave it running. It needs Google Chrome or Microsoft Edge.
2. Under **Saved logins**, add the portals the task logs in to (name, site, user name, password, and the authenticator key if the portal uses an authenticator app). Passwords are never shown again.
3. **Record a task**: give the start address. The bot opens its own browser window; do the task there (log in, click, type, download). Each step appears in Ledgerline as you go. Password boxes are linked to your saved login for the site, never recorded; SMS one-time passwords and captchas become **Ask me** steps.
4. **Stop and save**, then check the steps. Add steps such as **Load into Ledgerline** (load a downloaded file as trades, obligations, bhavcopy…), **Read text**, **Message me**, or dates such as the trade date in any format.
5. **Run** it, or tick **Run by itself** with a time and days (trading days skip exchange holidays). The computer must be on with the bot running.
6. A run that needs you pauses: Ledgy messages you; do the step in the bot's window and click **Continue**. A failed run shows the step, the reason and a picture of the screen.

**Share** lets colleagues run a workflow on their own computers with their own saved logins. Admins can add firm logins (the firm's own portal accounts) and choose who may use them. Every use of a saved password is in the audit trail.

## connections
title: Connect KRA, the OMS, the bank, SMS and the mail server
screen: connections
roles: Admin
ask: connections; api key; kra login; oms api; mail server; smtp; sms gateway; whatsapp api; bank api; e-sign provider; penny drop; where do i put the password; ip whitelist; server ip; test connection
related: dispatch-register

On [[connections|Connections]] (Admin) enter the firm's own account for each service: KRA, CKYC, the trading front end (OMS), the exchange and depository portals, the client money bank account, SMS, WhatsApp, e-Sign, penny drop and the mail server.

1. **Set up** opens the fields that service needs. Passwords and keys are saved encrypted with the firm's keys and never shown again; leave them blank later to keep them.
2. **Save and test** checks that the address answers securely and, for the mail server, that the login works. Nothing is sent or changed at the provider.
3. Providers that accept only listed addresses need this server's address, shown at the top of the page.

The services marked "Needed to go live" are part of the go-live checklist. Contract notes and statements go out through the firm's own mail server once it is set here.
