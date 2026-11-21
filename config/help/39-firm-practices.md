Ledgy help articles: Settings → Firm practices, the questionnaire on how the firm works, its approvals and history, the go-live checklist, and clients' Finspot logins.

# Firm practices

## firm-practices
title: Tell Ledgerline how the firm works (firm practices)
screen: practices
roles: Admin, Compliance officer
ask: firm practices; how our firm works; operating questionnaire; office questionnaire; change how ledgerline works for us; our own way of working; firm settings questions; other practice; configure our practice; practice needs configuring; switch on a proposal; settings questionnaire; how we do it is different
related: firm-practice-approval, go-live-checklist, finspot-client-logins

Open [[practices|Firm practices]] (Admin → Settings). Each question shows why it matters, its options and **Other**.

1. The option Ledgerline follows now is ticked and marked **In force now**. It is read from the real setting each time, so a change made elsewhere shows here too.
2. Tick another option and click **Save answer**. Ledgerline now works that way. An option marked **Not available yet** is not built yet and cannot be chosen.
3. If the firm works another way, tick **Other** and say how. Ledgerline records it and keeps what it does now. The Admin gets a work item saying the practice needs configuring, and it shows on the go-live checklist. Once it is settled, come back and choose the option.
4. Ticking the option already in force records the answer. Nothing changes and no approval is needed.
5. Each question keeps its **History**: every answer, who gave it and when, and who approved or rejected it.

Only an Admin answers. The compliance officer can see the page and approve changes.

The questions:

- **Equity limit on two front ends.** Today each front end gets the client's whole money. A primary front end, and a one-click move of the unused part, are not available yet.
- **Blocks for KYC-pending and inactive clients.** With Yes, clients whose KYC is on hold, rejected, pending or incomplete at the KRA are blocked on the front ends (block file, limits) until it is cleared. Ledgerline does not fetch KYC statuses from the KRA every day yet: it uses the status already loaded (KRA status uploads on [[kyc|KYC & PMLA]]). Accounts flagged Inactive are blocked either way.
- **Finspot client logins.** In Finspot's admin console, from Ledgerline, or both. See the Finspot logins article.
- **Margin baskets by API** each morning. Needs Finspot by API in Connections. The basket files stay as the fallback.
- **CUSPA and pledge instructions by the depository's API.** Built for CDSL; needs CDSL APIs (DP) in Connections. Lines that cannot go by API stay on the day's list.
- **MCX money as its own balance** in the one ledger.
- **Client authorisation of bank changes** raised by staff: the client's one-time code and link (signed letter or e-sign as the fallback), the code and link only, or the signed letter only.
- **Brokerage on auction trades:** the auction rate always wins, or the client's special rate for the scrip wins.
- **DP pledge charges:** only when a pledge is created, or also when it is closed or invoked. This changes the firm's DP tariff.
- **Online account opening contracts:** the firm's own or another arrangement, and the fee charged online. Recorded only: Ledgerline does not collect a fee online yet.

## firm-practice-approval
title: Approve a firm practice change
screen: practices
roles: Admin, Compliance officer
ask: approve firm practice; firm practice waiting for approval; second approver firm practice; who approves practice change; reject practice change; withdraw practice change; needs a second approver; money affecting setting approval; change mcx money apart; change auction rate; change bank change authorisation; change dp pledge charges
related: firm-practices

Questions marked **Needs a second approver** affect money or client limits: the equity limit on two front ends, MCX money apart, client authorisation of bank changes, brokerage on auction trades and DP pledge charges.

1. An Admin saves the new answer. It shows **Waiting for approval**. Ledgerline keeps what it does now.
2. The compliance officer or another Admin opens [[practices|Firm practices]], clicks **Approve or reject**, and approves. The change applies at once. The person who proposed it cannot approve it.
3. To reject, give the reason. The proposer can **Withdraw** the change instead.

Both steps are in the audit trail and in the question's history. The work queue has an item for the approver.

These settings are no longer changed on the Settings or brokerage screens: they show a link to Firm practices.

## go-live-checklist
title: Go-live checklist
screen: practices
roles: Admin, Compliance officer
ask: go live checklist; go-live checklist; what is left before going live; setup checklist; are we ready to go live; new firm checklist; first run; onboarding checklist for the firm; questions to answer before go live
related: firm-practices, connections

A new firm sees the **Go-live checklist** at the top of [[practices|Firm practices]], and the dashboard reminds the Admin while questions are unanswered.

It lists the firm details, member codes, the digital signature, the mail server, required connections, backups, online KYC, the compliance email, and every firm practice question until all are answered. A practice answered **Other** stays on the list until it is configured.

Ledgerline's provider sees the same list, so it can help with what is missing. It sees the questions, never the firm's own words for an Other answer.

## finspot-client-logins
title: Finspot client logins: created in the console or by Ledgerline
screen: practices
roles: Admin, Ops maker, Ops checker, Risk manager
ask: finspot login; create finspot login; client user id on finspot; finspot user creation; add investor client; client has no finspot login; duplicate finspot login; login made in finspot admin console; check finspot login; client exists on finspot; finspot user id not created
related: firm-practices, finspot-api

The firm chooses on [[practices|Firm practices]] who creates a new client's user ID on Finspot:

- **In Finspot's admin console only.** Ledgerline never creates one.
- **From Ledgerline.** Ledgerline creates each new active client's login by API (account, segments and products; never a password, Finspot gives the client one). Without Finspot by API, the login goes in Ledgerline's Investor Client upload file on [[live|Live RMS]] → Front-office files.
- **Both.** Staff create some in the console. Ledgerline creates one only when someone clicks **Create Finspot login** on the client (needs Finspot by API).

Ledgerline never creates a second login. It knows a client has one when it created it, when Finspot answers "Client exists" (the login was made in the console: Ledgerline changes it instead and records it), when **Check Finspot login** finds the account, or when staff click **Made in Finspot's console**.

The client drawer shows **Finspot login**: whether the client has one, how Ledgerline knows, and when. The Finspot panel on Live RMS shows it for each client.
