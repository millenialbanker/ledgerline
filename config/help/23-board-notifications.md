Ledgy help articles: board notifications by email — your address, how often, which kinds, pausing, what an email holds, and the firm's switch.

# Board notifications by email

## board-email-notifications
title: Board notifications by email
screen: boards
roles: Ops maker, Ops checker, Risk manager, Compliance officer, Branch staff, Authorised person, Admin
ask: email notifications; board notifications by email; email me when i am mentioned; email digest; daily email of notifications; trello email notifications; stop board emails; change my email for notifications; notification settings; how often do i get emails; why did i get this email; no email from boards
related: boards-notifications, board-email-pause, board-email-firm

Boards can email you your notifications, as Trello does. Open the bell on the boards screen and press **Notification settings** at its foot (the line beside it says how email is set for you).

1. **Email address**: type your work address and press **Send code**. Ledgerline emails a 6-digit code to it; type the code and **Confirm**. Nothing is emailed to an address until its code is confirmed, so a typing mistake cannot send your cards to someone else. The code is valid for 30 minutes; five wrong tries need a new code. **Change** or **Stop emails** later.
2. **How often**:
   - **Right away**: within about five minutes. Notifications that come close together share one email, so a busy card does not send ten.
   - **Every hour**: one email at the start of each hour with what came in the hour before.
   - **Once a day** at the time you choose (IST).
   - **Never**: notifications stay in the bell only.
3. **What to email**: being added to a card (or given a checklist item), mentions, comments on cards you watch, cards you watch moving, due-date reminders, other activity on the cards, lists and boards you watch, and automation messages. Until you choose, it is mentions, being added and due-date reminders, once a day at 09:00.
4. **Save**.

Each email lists the notifications that are still unread, oldest first: for each one the board's name, the card's number and title, the notification and a link that opens the card in Ledgerline. Nothing else from the card is sent: not its description, attachments or other comments. Emailed notifications stay unread in the bell. Each notification is emailed at most once, even if Ledgerline restarts, and notifications from before your address was confirmed are not emailed.

An automation that **emails** you on purpose (the *Email people* action of a rule or button) comes within about five minutes whatever kinds and how often you chose, unless you chose **Never** or paused email.

You get nothing by email from a board you can no longer see, or once your login is switched off. **Sent lately** in the window lists your last emails and what happened to each; Admins and compliance officers also see them in the staff outbox.

## board-email-pause
title: Pause board emails for a day or a week
screen: boards
roles: Ops maker, Ops checker, Risk manager, Compliance officer, Branch staff, Authorised person, Admin
ask: pause email notifications; snooze board emails; on leave stop emails; no emails for a week; resume board emails
related: board-email-notifications

In **Notification settings**, press **Pause for 1 day** or **Pause for 1 week**. The window and the foot of the bell say until when. Notifications that come in while email is paused stay in the bell; they are not emailed later, so you do not come back to one long email. **Start again now** ends the pause early.

## board-email-firm
title: Switch board emails off for the whole firm
screen: boards
roles: Admin
ask: switch off board emails for the firm; stop all board notification emails; firm email switch boards; board emails not sent; no mail server board emails; links in board emails
related: board-email-notifications

An Admin sees **For the whole firm** at the foot of **Notification settings**: **Switch off** stops every board email for everyone (the bell still works); **Switch on** starts them again, for what comes in from then on.

Board emails go through the firm's mail server in Settings → Connections, like other emails to staff. Without one, nothing is sent, the window says so, and each attempt is in the outbox as not sent. If the mail server refuses an email, Ledgerline tries again about fifteen minutes later.

The link in each email uses the address people reach Ledgerline at: the server's public address when it has one, otherwise the address an Admin last opened Notification settings from. Until Ledgerline knows it, the email says where to find the card instead (Boards → the board → the card's number).
