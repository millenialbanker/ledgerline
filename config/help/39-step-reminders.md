Ledgy help articles: reminders for steps not done by their time, escalation, the pending list from earlier days, the daily interest run's exemptions, and setting due times and owners.

# Step reminders

## step-reminders
title: Reminders for steps not done on time
screen: steps
roles: Ops maker, Ops checker, Risk manager, Compliance officer, Admin
ask: step reminders; missed step; forgot a step; reminder not done; step not done by due time; due by time; remind me; escalate to admin; escalation; head of the desk; pending from earlier days; carried over; carry over to next day; easy to miss tasks; prompt for a task; nudge; step overdue; done by hand outside ledgerline; skip a step; skipped with a reason; one reminder a day
related: step-interest-exemptions, step-settings, work-queue, periodic-work, missing-file-alerts

Every step Ledgerline knows about has a due-by time (IST) and an owner. That covers the Autopilot automations, the periodic work, the files on the daily file checklist, a few manual steps such as uploading the daily reports, and the firm's own steps. The owner is a role or a named person.

Open [[steps|Steps & reminders]] to see the day's steps, each with its due time, its owner and where it stands.

When a step is not done by its due time:

- Its owner gets a Ledgy notice and a work item.
- If it is still not done after the step's escalation delay (30 minutes unless the Admin set another), the Admin hears of it, or the head of the desk the firm named. That person also gets a work item.
- A step is reminded at most once a day and escalated at most once a day. Later checks the same day send nothing more.
- Reminders go only on the days the step runs. Exchange holidays and the firm's own holidays are respected. Saturdays count only for steps that run on Saturdays, such as the weekly statement of funds and securities.
- Periodic work due on a day the step does not run is reminded on the last working day before it. A task due "by the 5th" that falls on a Sunday is reminded on the Friday.

A step that Ledgerline can tell is done closes by itself: its automation ran, its file arrived or its periodic work is done. Its work items close with it.

Anything not done carries over. It stays under **Pending from earlier days** on [[steps|Steps & reminders]], on the [[dashboard|Dashboard]] and in the Day runbook layout, until one of these happens:

- It is done.
- Someone marks it **Done by hand** (done outside Ledgerline). Say how it was done.
- Someone marks it **Skip**. Say why it is skipped.

The owner, anyone with the owner's role, the person it escalates to, or the Admin can do this. The note or the reason goes in the audit trail. Closing a reminder's work item with a note also counts as done by hand.

**Check now** looks at every step at once. Otherwise Ledgerline checks every five minutes.

Each reminder and escalation is also recorded as an event in the audit trail (step.reminder, step.escalation), ready for a push to the staff mobile app later.

## step-interest-exemptions
title: Run the daily interest with its exemptions
screen: steps
roles: Ops checker, Risk manager, Admin
ask: daily interest run reminder; delayed payment interest exempt clients; interest exemptions; run with these exemptions; important clients not charged interest; who is not charged interest today; interest not charged; confirm interest run; exempt clients list; dpc exemptions; interest run by hand
related: step-reminders, debit-interest, client-interest

When interest on debit balances is posted daily (Settings → Debit balances and interest → **Interest posting: Daily**), the daily interest run is a step. It is due at 20:30 and belongs to the Ops checker by default.

Some clients are deliberately left out of interest:

- clients with their own "no interest" terms (on the client's page)
- clients in a group the firm does not charge (Settings → **Groups not charged interest**)
- clients with a legacy debt

The step lists these clients with its reminder. On [[steps|Steps & reminders]], click **Check exemptions** on the step. For each client you see:

- why they are exempt
- their overdue debit
- the days and the interest not charged (at their own rate, or the firm's)

The days run from the day after the last run to the day of the step. A Monday run covers the weekend.

If the list is right, click **Run with these exemptions**. Ledgerline posts the day's interest for every other client in overdue debit. It records who was left out and the amount not charged, with your name, in the audit trail (step.exceptions_confirmed). The step is then done.

If the list changes between the moment you open it and the moment you confirm, the confirmation is refused. Open the list again and check it.

To change who is exempt, change the client's interest terms or the exempt groups first, then check the list again.

The step is also done when the interest is posted another way: by Autopilot, or by approving the posting in the work queue.

## step-settings
title: Set due times, owners and escalation for steps
screen: steps
roles: Admin
ask: change step due time; step owner; assign a step to a person; change who a step reminds; escalation delay; escalate after minutes; switch off a reminder; turn off step reminders; add our own step; firm's own step; custom step; daily checklist of our own; reminder for a manual task
related: step-reminders, step-interest-exemptions

On [[steps|Steps & reminders]] → **Settings**, the Admin sets, for each step:

- **Due by**: the time in IST. The default comes from the step's own schedule. Autopilot steps default to a while after their scheduled run, and checklist files to their usual time plus the missing-file grace.
- **Owner**: a role, or a named person. The default is the step's own role.
- **Escalate after**: minutes after the reminder (30 by default).
- **Remind**: untick it to stop the step's reminders. Client insight steps send no reminders unless you tick it.

Also set here:

- **Escalate to**: the Admin, or the head of the desk (a role or a person).
- **Switch every step reminder off**.

Under **The firm's own steps**, add steps Ledgerline does not know about, such as counting the petty cash. Give each a time, its days (business days, Saturdays too, a weekday or a day of the month) and an owner. Ledgerline reminds the owner, and the owner marks the step done by hand.

Click **Save settings**. The change is in the audit trail (step.settings). [[settings|Settings]] links here too.
