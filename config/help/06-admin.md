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

- Jobs: the Autopilot stages, debit reminders, monthly statements, the KRA upload file and emailed reports to management.
- **New job** (Admin) or **Edit**: time (IST), which days, and who is emailed on success or failure.
- **Run now** to run one immediately. Each run is recorded with its result.

Contract notes, margin calls, KYC reminders, inactive flagging and interest are Autopilot steps, not jobs.

## message-templates
title: Change a message template
screen: messages
roles: Ops checker, Compliance officer
ask: message template; edit sms; email template; dlt template id; whatsapp template; change message text; sms not sent; promotional sms
related: whatsapp-consent, bulk-message

On [[messages|Messages]]:

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

On [[messages|Messages]], click **Bulk message**, choose the clients (all, a group or a list), the channel and the text. WhatsApp goes only to clients who opted in; promotional SMS follows the time window.

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

When a day is closed (**EOD close**), the whole firm is backed up: clients, ledgers, trades, files, settings and the audit trail. The backup is encrypted with the firm's data keys and read back to check it before it counts. Open [[backups|Backups]] to see them.

- **Daily, weekly, quarterly, yearly:** every backup is a daily one. The backup of the **last trading day** of a week, a quarter or the year (31 March, or 31 December if set) is also kept as weekly, quarterly or yearly. Last trading days follow the approved exchange holiday calendar.
- **Kept:** 30 daily, 13 weekly, 12 quarterly and 10 yearly backups unless an Admin changes it under **Retention**. Keep yearly backups for as many years as your records must be kept.
- **A day not closed** by 23:30 is backed up anyway, and again once it is closed.
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
