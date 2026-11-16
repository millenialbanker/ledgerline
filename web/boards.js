'use strict';
/* Ledgerline console: Boards (Trello-style boards, lists and cards).
   Staff screen "boards": the boards home, a board (lists and cards, drag
   and drop), the card window, filters, search and notifications.
   Server: internal/ops/boards.go, internal/api/boards.go.

   The other board files plug into BOARDS (they load after this one):
     BOARDS.views[id] = { label, order, render(ctx) -> html, after?(ctx) }
         Another way to show the board's cards (table, calendar, timeline,
         dashboard). ctx = BOARDS.ctx(): { board, cards (filtered), all
         (every open card), role, users, el }. The built-in view is "board".
     BOARDS.cardSections.push({ id, order, render(card, ctx) -> html|'' })
         A section in the card window's main column.
     BOARDS.cardButtons.push({ id, label, group: 'add'|'actions', order,
         show?(card, ctx) -> bool, run(card, ctx, el) })
         A button in the card window's sidebar.
     BOARDS.cardBadges.push((card, ctx) -> html)
         Small badges on a card's front in a list.
     BOARDS.boardMenu.push({ id, label, order, show?(ctx) -> bool, run(ctx) })
         An entry in the board's menu.
     BOARDS.headerButtons.push({ id, order, render(ctx) -> html })
         Buttons in the board header.
     BOARDS.composerButtons.push({ id, label, run(listId, ctx) })
         Extra buttons beside "Add card" in a list's composer.
     BOARDS.homeSections.push({ id, order, render(home) -> html, load?() -> Promise })
         Sections on the boards home page.
     BOARDS.reload()          reloads the open board
     BOARDS.ctx()             the open board's context
     BOARDS.openCard(b, c)    opens a card window
     BOARDS.openBoard(id)     opens a board
   Actions use the global A object; each file prefixes its names (bv*,
   br*, bx*, bs*) and its UI state keys the same way. */

const BOARDS = {
  views: {}, cardSections: [], cardButtons: [], cardBadges: [], boardMenu: [], headerButtons: [], composerButtons: [], homeSections: [],
  reload: () => {}, ctx: () => null, openCard: () => {}, openBoard: () => {}
};
