Ledgy help articles: automation on Boards — rules, card and board buttons, scheduled and due-date commands, the automation log and the card repeater.

# Board automation

## board-automation-rules
title: Make a board rule (when this happens to a card, do that)
screen: boards
roles: Ops maker, Ops checker, Risk manager, Compliance officer, Admin
ask: board rule; butler rule; trello butler; automate a board; automation on a board; when a checklist item is ticked; when a custom field is set; when a card is renamed; when an attachment is added; card created by me; by someone else; set a custom field automatically; tick all checklist items; remove all labels; copy card to another board; add a watcher automatically; email a board member; cardname cardlink variables; when a card is moved into done mark it complete; when a card is moved to a list do something; move card automatically when label added; notify someone when a card moves; automatically assign a card; card automation trigger
related: board-automation-buttons, board-automation-scheduled, board-automation-due, board-automation-log

On a board, open the board menu → **Automation**. The **Rules** tab lists the board's rules as sentences, for example: *When a card is moved into Done, mark it complete and notify @farhan.khan.* Board admins and members make and change rules; observers can read them.

1. **New rule**, then choose what sets it off:
   - a card added to the board or to a list, moved into or out of a list, or moved to the top or bottom of a list (within the list too);
   - a card renamed, a label added or removed, someone added, a due date set, the card marked complete or not complete;
   - a checklist completed, a checklist item ticked or unticked, an item added to a checklist (each optionally only items containing some words);
   - an attachment added, a custom field set or changed (any field, or one field, optionally only to one value);
   - the card archived or sent back to the board, a comment posted (optionally only comments containing some words).
   **by anyone** can be narrowed to **by me** (you, who saves the rule), **by someone else** (anyone but you; automation does not count), or one person. "Created by me" is *a card is added to the board, by me*.
2. **Only if** (optional): the card has or does not have a label or member, is or is not in a list, its title contains some words, it has or has no due date, it is overdue, it is complete or not, and all, some or none of its checklist items are done (with none, a card without a checklist counts).
3. **Then**: the actions, in order:
   - move the card to a list (top or bottom), or to the top or bottom of its own list; copy it within the board, or **copy it to another board** you can add cards to;
   - add or remove a label, remove every label; add or remove a member, remove every member, add the person who set it off, **add a watcher**;
   - set the due date (in N days or working days, at a time) or remove it, mark it complete or not;
   - add a checklist, tick or untick every checklist item, **set a custom field** (or clear it);
   - post a comment, archive it, sort a list, **notify people** or **email people** (named people, the card's members, its watchers), create a card, move or archive every card of a list.
   Text can use Trello's names {cardname}, {cardlink}, {boardname}, {listname}, {username} (who set it off), {date} (today) and {duedate}, and {no} for the card's number; the older {card}, {list}, {board}, {user} and {due} still work. {cardlink} is the card's address in Ledgerline.
4. The box **Reads as** shows the whole rule as a sentence while you build it. **Save rule**. Left without a name, the rule is named from its sentence.

Rules act as **Automation**: the board's activity shows each change by Automation and a line naming the rule that made it. The switch beside a rule turns it off without deleting it. A rule that fails (say its list was archived) never undoes or blocks the change that set it off; the rule shows **Last run failed** and the reason, and the log keeps it.

A rule's actions can set off other rules. A chain stops at the third rule in a row, and the log shows **Stopped: loop**, so two rules moving a card back and forth cannot run forever. A comment posted by automation does not set off comment rules. A board runs at most 1,000 automations a day; when it reaches that, automation stops until midnight, the log says so and the board's admins get a notice. On a Ledgerline board, whose cards follow their records, automation can label, assign, date, check off, comment on and sort cards, move them up or down their own list and notify or email people, but not move them to another list, copy, archive or create cards. A card linked to a Ledgerline record is never moved to another list or copied to another board by automation.

- **Email people** leaves a notification in each person's bell, as **Notify people** does, and sends an email within about five minutes saying a message from an automation rule is waiting (the message itself stays in Ledgerline), whatever they chose to have emailed and how often, unless they have no confirmed address, paused email or chose **Never** (see [[board-email-notifications|Board notifications by email]]).
- **Copy it to another board** works only while the person who last saved the rule can still add cards to that board; if they lose that, the run fails and says why. Labels and custom fields belong to their board, so the copy keeps the title, description, dates, checklists and members.

## board-automation-buttons
title: Card buttons and board buttons
screen: boards
roles: Ops maker, Ops checker, Risk manager, Compliance officer, Admin
ask: card button; board button; add a button to a card; one click to send a card for checking; button to archive all done cards; run actions on every card in a list; butler button
related: board-automation-rules, board-automation-log

Board menu → **Automation** → **Card buttons** or **Board buttons** → **New**. Give the button a short name (it is the button's label) and its actions, the same actions as rules.

- A **card button** appears in every card window of the board, under Actions, for board admins and members. Pressing it does its actions on that card: for example *Send for checking* moves the card to the top of Checking, adds the checker and comments "Ready for checking, sent by {user}".
- A **board button** appears in the board header. It acts on every open card of one list, or of the whole board, that meets its conditions: for example *Archive finished* archives the complete cards in Done. Actions that are not about one card (sort a list, notify, create a card) run once after the cards are done. A board button that would act on more than 20 cards asks first and says how many.

Buttons act as Automation, and each press is in the automation log with what it did.

## board-automation-scheduled
title: Scheduled commands on a board (every day, working day or month-end)
screen: boards
roles: Ops maker, Ops checker, Admin
ask: scheduled card every day; create a card every morning; recurring card on a board; every working day at 9; first working day of the month card; last working day of the month; board command on trading holidays; move all cards every friday
related: board-automation-rules, board-automation-due, board-automation-log

Board menu → **Automation** → **Scheduled** → **New scheduled command**. Choose when, in IST:

- every day; every weekday (Monday to Friday); every **working day** (Monday to Friday except the firm's trading holidays in Settings and the approved exchange holiday calendars);
- on chosen weekdays; on a day of the month (day 31 runs on the last day of shorter months);
- on the first or last working day of the month.

Every day, weekdays, chosen weekdays and day of the month can also skip trading holidays. Then the actions: create a card in a list (title, labels, members, a due date in N days or working days, a checklist; "Daily recon {date}" puts today's date in the title), move every card from one list to another, sort a list, archive every card of a list, notify people.

A command runs once for each date and time, even if Ledgerline restarts; if Ledgerline was not running at the time it runs when it comes back, up to three hours late, and the log says it ran late. On a trading holiday a working-day command does not run and the log shows **Skipped** with the holiday's name. A new command, or one switched back on, runs from its next time onwards. **Run now** runs it at once, as well.

## board-automation-due
title: Due-date commands (before or after a card is due)
screen: boards
roles: Ops maker, Ops checker, Risk manager, Compliance officer, Admin
ask: remind before a card is due; do something when a card is overdue; due date command; label overdue cards automatically; notify card members a day before due; one hour before the due date
related: board-automation-rules, board-automation-scheduled

Board menu → **Automation** → **Due date** → **New due-date command**. Set how long before or after the due date (minutes, hours or days; 0 for the moment it falls due), and optionally only cards in one list or with one label. Cards marked complete are left out unless you tick **complete cards too**. Then the actions, for example add the red label and notify the card's members.

It runs once for each card and due date: a card given a new due date gets it again for the new date. A due date without a time counts as 23:59 that day. It runs for moments after the command was made, so a new command does not act at once on cards whose moment has already passed.

## board-automation-log
title: The board's automation log
screen: boards
roles: Ops maker, Ops checker, Risk manager, Compliance officer, Admin
ask: automation log; why did the rule not run; what did automation do; rule failed; automation stopped for today; daily automation limit; loop guard stopped rule; who moved this card automation
related: board-automation-rules, board-automation-buttons

Board menu → **Automation** → **Log** lists every run of the board's rules, buttons and commands, newest first: when, which one, the card (or how many cards), what set it off and by whom, what it did, and the result:

- **Done**; **Failed**, with the reason (for example the list it moves cards to was archived, or a person is no longer on the board): change the rule and it works again;
- **Stopped: loop**, when rules kept setting each other off: look at the rules named in the runs just before;
- **Daily limit**, when the board reached 1,000 runs that day;
- **Skipped**, a scheduled command on a trading holiday.

Pick one rule, button or command in the box to see only its runs; **Log** beside each one opens this directly. Everyone who can see the board can read the log. Each change automation makes is also in the board's activity and the card's activity, by Automation.

## board-card-repeater
title: Repeat a card (every day, week, month, quarter or year)
screen: boards
roles: Ops maker, Ops checker, Risk manager, Compliance officer, Admin
ask: repeat a card; recurring card; card repeater; trello card repeater; copy a card every month; monthly filing card; quarterly card; yearly card; repeat every working day; repeat on the last working day of the month; stop repeating a card; card repeats; recurring compliance task
related: board-automation-scheduled, board-templates, ras-calendar

Open a card on a team board and press **Repeat** (under Actions; board admins and members). Choose:

- **Repeat**: every day, every weekday (Monday to Friday), every **working day** (Monday to Friday except the firm's trading holidays, as scheduled commands use them), every week on the days you pick, every month, every quarter or every year;
- for months, quarters and years: on a day of the month (day 31 uses the last day of shorter months), or on the **first** or **last working day**; for quarters, in the first, second or third month of each quarter (day 15 of the first month is 15 January, April, July and October); for years, the month;
- **At**: the time (IST) the copy is made;
- **Put each copy in**: the card's own list, or another list of the board;
- **Give each copy a due date**: N days, or working days, after it is made, at a time if you like;
- **Ends**: never, after a number of copies, or on a date.

The box under the form reads the repeat back and shows the next three dates. **Start repeating** saves it. The card then shows a **Repeat** section (for example *Repeats every month on the last working day · next 30 Nov, 10:00*) with **Edit** and **Stop**, and a repeat badge on its front.

Each time, Automation adds a copy of the card: the same title, description, labels, members and custom fields, and its checklists with every item unticked (items lose their own dates). Attachments, comments and image covers stay on the original. The copy does not repeat; the card's members are told about it, and the card's activity says which copy was made.

- A repeat runs once for each time, even if Ledgerline restarts. If Ledgerline was not running at the time, it makes one copy when it comes back (for the latest time it missed, however many it missed) and the activity says it was late.
- It stops when the card is archived, deleted or moved to another board (the section says why; **Set it to repeat again** starts it afresh). While the board is closed its times are skipped; reopened, it carries on from the next time.
- Cards on Ledgerline boards, cards linked to a Ledgerline record and mirror cards do not repeat: their records, or the original card, decide what they are.

It suits the **Compliance calendar** board template: make a card for each filing with its checklist and custom fields (Period, Filed with), set it to repeat monthly, quarterly or yearly on the day the circular sets (or the last working day), put the copies in the list for that period, and give them a due date a few working days on. Each period then gets its own card to tick through, and the filed ones move to **Filed**.
