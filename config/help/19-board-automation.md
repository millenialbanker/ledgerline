Ledgy help articles: automation on Boards — rules, card and board buttons, scheduled and due-date commands, and the automation log.

# Board automation

## board-automation-rules
title: Make a board rule (when this happens to a card, do that)
screen: boards
roles: Ops maker, Ops checker, Risk manager, Compliance officer, Admin
ask: board rule; butler rule; trello butler; automate a board; automation on a board; when a card is moved into done mark it complete; when a card is moved to a list do something; move card automatically when label added; notify someone when a card moves; automatically assign a card; card automation trigger
related: board-automation-buttons, board-automation-scheduled, board-automation-due, board-automation-log

On a board, open the board menu → **Automation**. The **Rules** tab lists the board's rules as sentences, for example: *When a card is moved into Done, mark it complete and notify @farhan.khan.* Board admins and members make and change rules; observers can read them.

1. **New rule**, then choose what sets it off: a card added to the board or to a list, moved into or out of a list, a label added or removed, someone added, a due date set, the card marked complete, a checklist completed, the card archived, or a comment posted (optionally only comments containing some words). **by anyone** can be narrowed to one person.
2. **Only if** (optional): the card has or does not have a label or member, is or is not in a list, its title contains some words, it has or has no due date, it is overdue, it is complete or not.
3. **Then**: the actions, in order — move or copy the card (top or bottom of a list), add or remove a label or member, add the person who set it off, set the due date (in N days or working days, at a time) or remove it, mark it complete or not, add a checklist, post a comment, archive it, sort a list, notify people (named people, the card's members, its watchers), create a card, move or archive every card of a list. Text can use {card}, {no}, {list}, {due}, {board}, {user} and {date}.
4. The box **Reads as** shows the whole rule as a sentence while you build it. **Save rule**. Left without a name, the rule is named from its sentence.

Rules act as **Automation**: the board's activity shows each change by Automation and a line naming the rule that made it. The switch beside a rule turns it off without deleting it. A rule that fails (say its list was archived) never undoes or blocks the change that set it off; the rule shows **Last run failed** and the reason, and the log keeps it.

A rule's actions can set off other rules. A chain stops at the third rule in a row, and the log shows **Stopped: loop**, so two rules moving a card back and forth cannot run forever. A comment posted by automation does not set off comment rules. A board runs at most 1,000 automations a day; when it reaches that, automation stops until midnight, the log says so and the board's admins get a notice. On a Ledgerline board, whose cards follow their records, automation can label, assign, date, check off, comment on and sort cards and notify people, but not move, copy, archive or create cards.

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
