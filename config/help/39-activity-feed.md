Ledgy help articles: the Activity feed beside every screen, showing what was done this week and who did it.

# Activity feed

## activity-feed
title: See what was done this week, and who did it
roles: Ops maker, Ops checker, Risk manager, Compliance officer, Branch staff, Authorised person, Admin
ask: activity feed; what happened today; what happened this morning; who did what; who approved this; what did ledgerline do overnight; what did the autopilot do; recent activity; activity panel; timeline; what changed since i last looked; new since you last looked; show only my actions; what did farhan do today
related: activity-feed-who-sees, audit-seals

Press **Activity** on the top bar, on any screen. A panel opens on the right with everything done this trading week, newest first, grouped by day with the time of each line. For example: "Farhan Khan released payout batch PB-12 (14 payouts, ₹3.2 lakh)" or "Ledgerline loaded NSE CM trade file (1,204 trades)". Work Ledgerline did by itself (the autopilot, scheduled jobs, the bank feed) is shown as **Ledgerline**, not as a person.

New lines appear on their own while the panel is open, about every 10 seconds. Nothing is fetched while the browser tab is in the background.

A line marked **New since you last looked** separates what happened since you last had the panel open. If the panel is closed, the number on the **Activity** button counts those new lines.

Ledgerline remembers whether you keep the panel open, on any computer you sign in from. Press **Activity** again or **×** to close it.

Narrow what you see:

- **Search this week** finds words in the lines: a client code, a batch number, a file name.
- **Everyone** picks one person. **Every desk** picks a role, such as Ops checker, or Ledgerline.
- **Every area** picks Operations, Risk, Accounts, Compliance, Depository, Investments or Admin.
- **People**, **Ledgerline** and **Mine** show only what people did, only what Ledgerline did, or only your own work.

The Admin and head-office staff also see **Today** at the top: how many lines each person has today. Press a name to see only theirs.

Press a line to open the screen it is about. A line about a client also opens the client.

The week runs from Monday 00:00 to Saturday 23:59 IST. On Sunday the panel still shows the week just ended. It starts again on Monday at 00:00. Earlier weeks are in the [[audit|Audit trail]], which keeps every entry.

## activity-feed-who-sees
title: Who sees what in the Activity feed
roles: Compliance officer, Admin, Branch staff, Authorised person
ask: who can see the activity feed; can branch staff see the activity feed; activity feed privacy; why can't i see sign-ins in the activity feed; sign-ins in the activity feed; activity feed shows pan; does the activity feed show team messages; is the activity feed a separate log
related: activity-feed, audit-seals

The Activity feed is written from the audit trail. It keeps no record of its own, so it can't differ from the [[audit|Audit trail]].

- Each person sees only the areas they can open.
- Branch staff see only lines about their branch's clients, and what people of their branch (and the offices under it) did. Authorised persons see only their own clients and their downline's. Lines about screens they can't open, such as surveillance alerts, billing runs, Admin and Depository work, are not shown to them, even about their own clients.
- Sign-ins, failed sign-ins, Ledgerline support access, password and key changes, user changes, full PAN looks and data exports are shown only to the Admin and the Compliance officer.
- A line says no more than its screen shows. PAN, Aadhaar, bank account and demat numbers are masked, and reasons and notes typed by people are left out. Open the screen to read them.
- Team messages and boards are not in the feed: private channels, direct messages and private boards stay private. Very frequent entries, such as every look at a document or every call to a connection, are left out too. All of these are still in the audit trail.
- Client portal logins have no Activity feed.
