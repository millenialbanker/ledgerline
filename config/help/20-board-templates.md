Ledgy help articles: board templates, card templates, custom fields, covers and backgrounds, votes, mirror cards and email-to-board on Boards.

# Boards: templates and extras

## board-templates
title: Start a board from a template
screen: boards
roles: Ops maker, Ops checker, Risk manager, Compliance officer, Admin
ask: board template; board templates; template gallery; new board from a template; month-end close board; compliance calendar board; onboarding follow-up board; make a board a template; save board as template; stop being a template; copy a board with its lists
related: card-templates, board-custom-fields

The boards home starts with a gallery of templates: Ledgerline's own (client onboarding follow-ups, month-end close, compliance calendar, inspection and audit preparation, branch and AP visits, IT and cyber security tasks, weekly team meeting) and the firm's own template boards.

1. On [[boards|Boards]], under **Start from a template**, choose **Use template** on a card. The window shows the template's lists, labels, custom fields and starter cards.
2. Give the board a name, choose who can see it (firm-wide or private; branch and partner staff always get a private board) and whether to **Keep the starter cards** with their checklists.
3. **Create board** opens the new board; you are its admin. Nothing changes on the template.

Ledgerline's templates name the work in general terms. Dates and formats come from the circulars in force: set each card's due date from them.

To make one of the firm's boards a template, an admin of the board chooses **Make template** in the board menu (**Stop being a template** takes it out of the gallery). It shows in the gallery to everyone who can see the board, so a private template is for its members only.

## card-templates
title: Card templates
screen: boards
roles: Ops maker, Ops checker, Risk manager, Compliance officer, Admin
ask: card template; make a card a template; create card from template; template card; reuse a checklist; same checklist every month; new card with the same checklist
related: board-templates

A card template is a card you copy again and again, such as a follow-up for each new application or a visit report.

1. Open the card and choose **Make template**. It shows a **Template** badge in its list and in the card window. **Not a template** turns it back into an ordinary card.
2. At the foot of any list choose **Create from template**, pick the template and type the new card's title.
3. The new card gets the template's description, labels, members, checklists (unticked), custom fields and cover. Dates are not copied.

## board-custom-fields
title: Custom fields on cards
screen: boards
roles: Ops maker, Ops checker, Risk manager, Compliance officer, Admin
ask: custom field; custom fields; add a field to cards; client code on a card; amount on a card; dropdown field on a card; show field on card front; reorder custom fields
related: board-templates

Custom fields add details every card on a board should carry: text, a number, a date, a checkbox or a list of options.

1. A board admin opens the board menu → **Custom fields**, adds fields (**Add a field**), names them, chooses the type and, for a list, types the options separated by commas.
2. Tick **On card front** to show a field's value on the card in its list. Use the arrows to change the order, **Remove** to take a field off (its values are hidden on every card).
3. **Save fields**. In each card window the **Custom fields** section shows the fields; members change a value and it is saved straight away. A number takes digits only (1250.50), a date is a date, and a list takes one of its options.

## board-covers
title: Card covers and board backgrounds
screen: boards
roles: Ops maker, Ops checker, Risk manager, Compliance officer, Admin
ask: card cover; cover colour; full cover; remove cover; board background; change background; background image; hide card covers
related: board-custom-fields

- **Cover** in the card window: choose **Above the title** or **Full card** (the cover fills the card), then a colour or one of the card's own images. **Remove cover** takes it off. An image attached to a card becomes its cover when it has none.
- **Change background** in the board menu (board admins): one of ten colours, or **Upload an image** (JPEG, PNG, GIF or WebP, up to 15 MB). **Card covers** there switches covers on or off for the whole board.

## card-votes
title: Vote on cards
screen: boards
roles: Ops maker, Ops checker, Risk manager, Compliance officer, Admin
ask: vote on a card; votes; who voted; remove my vote; most voted cards
related: card-templates

Everyone who can see a board, observers included, can vote on its cards. Under **Votes** in the card window, the **Vote** button adds or takes back yours, with the number of votes and who gave them beside it. On a closed board votes cannot change. Cards with votes show the count on their front (point at it to see who voted); the count is highlighted on the cards you voted for.

## mirror-cards
title: Mirror a card on another board
screen: boards
roles: Ops maker, Ops checker, Risk manager, Compliance officer, Admin
ask: mirror card; mirror a card to another board; same card on two boards; linked card; show a card on another board; open original card; mirror shows nothing; change through the mirror
related: card-templates

A mirror shows a card's title, description, due date and completion on another team board, and is brought up to date each time that board is opened.

1. In the card window choose **Mirror to another board**, then the board and list. Only team boards where you can add cards are offered; a card has one mirror per board.
2. The mirror's card window says **Mirror of #12 … on <board>** with **Open original**. If you can change the original, edit its title, description and dates there and choose **Save to the original**: the original and all its mirrors change.
3. A change made on the mirror card itself is made on the original as you, if you may change it; otherwise it is undone and you get a notice saying why. Labels, members, checklists and comments on a mirror stay on the mirror.
4. Archiving the original archives its mirrors; if the original moves to another board, its mirrors follow it.

A mirror shows nothing of its original unless everyone who can see the mirror's board can see the original's: otherwise it reads **Mirror of a card on another board**, and only people who can see the original see its details in the card window.

## email-to-board
title: Email-to-board: turn emails into cards
screen: boards
roles: Ops maker, Ops checker, Risk manager, Compliance officer, Admin
ask: email to board; email a board; inbound email; emails become cards; board email address; forward email to board; sendgrid inbound parse; postmark inbound; ses inbound; rotate board email address; switch off board email
related: board-templates

Each team board can have its own inbound address. A board admin sets it up:

1. Board menu → **Email-to-board** → choose the list new cards go to → **Switch on**.
2. Copy the **Inbound address** and, in the mail provider's inbound settings (Amazon SES, SendGrid or Postmark), have mail for the board posted to it as the raw message (SendGrid: post the raw, full MIME message; Postmark: include the raw email content).
3. Each email becomes a card: the subject is the title, the text the description with the sender on its first line, and attachments that pass the same checks as an upload are attached; any that do not are listed on the card. Each delivery is recorded in the board's activity, a message sent twice makes one card, and a closed board refuses mail.

Anyone who has the address can add cards to the board, so keep it private. **Give it a new address** stops the old one at once; **Switch off** refuses all mail to the board.
