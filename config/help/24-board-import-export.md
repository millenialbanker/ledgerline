Ledgy help articles: importing a board from Trello, printing and exporting boards and cards, sharing a card, collections and recently viewed boards on Boards.

# Boards: import, export and collections

## board-import-trello
title: Import a board from Trello
screen: boards
roles: Ops maker, Ops checker, Risk manager, Compliance officer, Branch staff, Authorised person, Admin
ask: import from trello; trello import; move from trello; bring a trello board; trello json; export trello board json; migrate trello to ledgerline; import trello members; trello attachments; trello comments
related: board-export, board-collections

Bring a whole Trello board over: its lists, cards, checklists, labels, custom fields and comments.

1. In Trello, open the board, then its menu → **Print, export and share** → **Export as JSON**, and save the file.
2. On [[boards|Boards]] choose **Import from Trello** and choose the file (or drop it on the window). It can be up to 20 MB.
3. The preview shows what comes over: lists, cards, checklists and items, labels, custom fields, comments, attachments and members, and the first cards of each list. Nothing is saved yet.
4. Under **Who is who**, choose the Ledgerline user for each Trello member. Ledgerline guesses from the same full name or username; change any guess, or choose **Leave off**.
5. Check the board's name and choose **Import board**. The new board opens.

What you get:

- A private team board: you are its admin, and the people you mapped are its members (Trello admins as admins, observers as observers). They get one notice that they were added; nothing else in the import notifies anyone.
- Lists in Trello's order (archived lists archived); cards with their title, description, place, start and due dates, completion, labels and the mapped members; archived cards archived. Card numbers stay as in Trello.
- Labels in Trello's colours, light and dark shades as Ledgerline's subtle and bold ones; checklists with what was ticked; custom fields and their values.
- Comments are posted by you, each starting "Imported from Trello: written by … on …", so it is clear who wrote it in Trello and when.
- Attachments become links to their Trello addresses. The files are not downloaded, so opening one needs access to the Trello board.
- Card covers keep their colour; image covers are not copied. Power-Ups, automation, stickers and votes are not imported.

Only a Trello board export is taken; anything else, including a single card's export, is refused with the reason. Very large boards are cut at 5,000 cards, 200 items a checklist and 20,000 comments, and the preview says what is left out. The import is recorded in the board's activity.

## board-export
title: Print or export a board
screen: boards
roles: Ops maker, Ops checker, Risk manager, Compliance officer, Admin
ask: print a board; export a board; export to excel; export cards to csv; board csv; download board; board json export; backup a board; print view; save board as pdf
related: board-import-trello, card-share

Board menu → **Print and export**:

- **Print** (anyone who can see the board): a print-friendly view of the open lists and their cards, as the board's filter shows them, with labels, dates, checklist progress, members and the fields shown on card fronts. Press **Print**; the browser's print window can also save it as a PDF.
- **Export as CSV** (members and admins of the board): every card, archived ones too, one a row, with its list, labels, members, dates, checklist progress, description, counts of comments and attachments, stickers and every custom field. It opens in Excel. Cells that would start a spreadsheet formula get a ' in front, so nothing in a card can run in Excel.
- **Export as JSON** (board admins): the whole board in Ledgerline's own format: lists, labels, custom fields, members, every card with its checklists and comments, and the board's activity.

Each print and export is noted in the board's activity, with who did it.

## card-share
title: Share, print or export a card
screen: boards
roles: Ops maker, Ops checker, Risk manager, Compliance officer, Admin
ask: share a card; link to a card; print a card; export a card; card json; copy card link; card pdf
related: board-export

In the card window choose **Share**:

- **Link to this card**: copy it to send to a colleague. Only people who can see the board can open it.
- **Print**: a print view of the card with its details, description, custom fields, checklists, attachments and comments.
- **Export JSON**: the card in Ledgerline's own format, with its board's labels and fields and its activity.

Printing and exporting a card are noted in the board's activity.

## board-collections
title: Collections of boards
screen: boards
roles: Ops maker, Ops checker, Risk manager, Compliance officer, Admin
ask: collection; collections; group boards; organise boards; board folders; filter boards on the home; add a board to a collection; rename a collection; delete a collection
related: boards-recent

Collections group boards on the boards home, for everyone at head office. A board can be in several.

1. On [[boards|Boards]] choose **New collection** and name it. Anyone at head office can make one, and rename one under **Manage**.
2. An admin of a board adds it: board menu → **Collections**, then tick the collections it belongs in (or make a new one there with **Create and add**).
3. On the boards home, choose a collection to see only its boards; **All boards** shows every board again. Your choice is kept on this computer.

Each person sees only the boards in a collection that they can see. Deleting a collection (its maker or an Admin, under **Manage**) leaves its boards as they are. Branch and partner staff do not see collections.

## boards-recent
title: Recently viewed boards
screen: boards
roles: Ops maker, Ops checker, Risk manager, Compliance officer, Branch staff, Authorised person, Admin
ask: recently viewed; recent boards; last boards i opened; where is the board i was on
related: board-collections

**Recently viewed** on the boards home shows the last six boards you opened. It is kept on the server, so it is the same on any computer you sign in on. Boards you can no longer see, and closed boards, are left out.
