Ledgy help articles: Ledgerline boards (boards whose cards are Ledgerline's own records, kept in step with them) and linking any card to a client or record.

# Boards

## ledgerline-boards
title: Ledgerline boards: the work queue, breaks, unpaid securities, onboarding, complaints and leads as boards
screen: boards
roles: Admin, Ops maker, Ops checker, Risk manager, Compliance officer
ask: ledgerline boards; set up ledgerline boards; work queue as a board; kanban of breaks; board of reconciliation breaks; cuspa board; onboarding board; complaints board; leads board; cards made by ledgerline; why did a card appear on the board; card moved by itself; card archived by itself; system ledgerline moved my card
related: ledgerline-board-moves, link-card-record, work-queue

A Ledgerline board's cards are Ledgerline's own records. An Admin sets them up once: on [[boards|Boards]], **Set up Ledgerline boards**, tick the boards and **Set up**. Running it again does not make a second board; it gives an existing one any list it lacks.

- **Work queue**: Autopilot's work items, open or closed in the last 7 days. Lists Open, Done (done, or done elsewhere) and Cleared.
- **Reconciliation breaks**: open breaks, and those resolved or cleared today. Lists Open, Resolved and Cleared.
- **Unpaid securities (CUSPA)**: open CUSPA lines, and those closed in the last 7 days, by where they are in SEBI's timeline: In payment period, Pay by today, Unpaid: notice or invoke, Last day to invoke, Depository releases, Invoked: to sell, Closed.
- **Onboarding**: clients not yet active and online applications not yet signed, and clients activated or rejected in the last 7 days. Lists Application, KYC verification, UCC upload, Active and Rejected.
- **Complaints and requests**: open tickets, and those resolved or closed in the last 7 days, due on their 21- or 7-day date. Lists Open, In progress, Awaiting client, Resolved and Closed.
- **Leads**: open leads, and those converted or lost in the last 7 days, due on the next follow-up. Lists New, Contacted, Interested, Documents pending, Converted and Lost.

Ledgerline keeps each card in step with its record whenever the board is opened, and every minute: it adds a card for a new record, moves a card when its record's status changes (the activity says **system:ledgerline moved…**, and the card's members and watchers are told), updates its title, due date and labels (the work item's role and area, the break type, the CUSPA position, the ticket's kind and category, the lead's source), and archives it when the record leaves the board. What people add to a card stays: comments, checklists, members, their own labels and attachments. An archived card comes back if its record changes again.

Every head-office user works on these boards; branch and partner staff do not see them. The Leads board is only for the roles that see leads on [[crm|Leads & CRM]] (Ops maker, Ops checker, Compliance officer and Admin). A private client's records are never put on a board.

Cards carry client codes and names, never PAN, bank or demat numbers, Aadhaar or phone numbers. The card window shows the record as it is now; **Open in Ledgerline** opens its screen.

## ledgerline-board-moves
title: What moving a card on a Ledgerline board does
screen: boards
roles: Ops maker, Ops checker, Risk manager, Compliance officer, Admin
ask: move card to done on the work queue board; drag card to done; close work item from the board; why can't i move this card; card would not move; move refused on ledgerline board; advance onboarding from the board; move complaint to in progress; move lead to contacted; release cuspa from the board; resolve break from the board
related: ledgerline-boards, work-queue, recon-breaks, cuspa, activate-client, complaints, leads

Moving a card on a Ledgerline board does the record's own work, as you, with your role's permissions and the usual maker-checker rules. Where a move needs more than a drag, it is refused with the screen that does it, and the card stays where it was.

- **Work queue**: moving a card to **Done** closes the item with the note "Moved to Done on the board" (as **Done elsewhere** does on [[autopilot|Autopilot & work queue]]); the item's role is checked. A prepared action is not carried out by the move; use its button on the work queue. Ledgerline clears items itself, and a closed item does not reopen.
- **Reconciliation breaks**: never moved from the board. Resolve a break with a note on [[recon|Trade reconciliation]]; a break clears itself when a later reconciliation no longer finds it.
- **Unpaid securities**: never moved from the board. Release, give notice, invoke and mark sold on [[settlement|Settlement]] → Unpaid securities.
- **Onboarding**: move a client's card one stage on (KYC verification → UCC upload → Active) to advance it. Activation needs an Ops checker who did not create the application. Stages are not skipped or undone; online applications are rejected, with the reason, on [[apps|Online applications]].
- **Complaints and requests**: move a card to **Open**, **In progress** or **Awaiting client**; once the ticket is resolved, to **Closed**. An internal note records the move. Resolving needs the reply to the client, on [[helpdesk|Investor complaints]].
- **Leads**: move a card to **New**, **Contacted**, **Interested** or **Documents pending**. Converting (with the client code) and marking lost (with the reason) are done on [[crm|Leads & CRM]].

Automation rules do not change Ledgerline records: a move made by a rule on a Ledgerline board is refused.

## link-card-record
title: Link a card to a client or Ledgerline record
screen: boards
roles: Ops maker, Ops checker, Risk manager, Compliance officer, Branch staff, Authorised person, Admin
ask: link card to client; link a card to a work item; attach a client to a card; link card to ledgerline; connect card with break; card linked to ticket; open the client from a card; remove link from card; link to ledgerline button
related: ledgerline-boards

Any card, on a team board too, can be linked to one client or Ledgerline record.

1. Open the card and choose **Link to Ledgerline**.
2. Type a client code or name (or a full PAN), or words from a work item, break, CUSPA line, ticket or lead. The buttons narrow it to one kind.
3. Choose the record. The card shows it as it is now, with **Open in Ledgerline** and, for a client's record, the client.

You find only what you may see on the record's own screen: branch staff their branch's clients, authorised persons their own; breaks and unpaid securities are for head office; leads for the roles that see them on [[crm|Leads & CRM]]. Someone who cannot see the record sees only the link's name. **Remove link** takes it off a team board's card. Moving a linked card on a team board changes nothing in Ledgerline.
