Ledgy help articles: choosing how Ledgerline is laid out, for the firm and for yourself, and the command bar and client page every layout shares.

# Screen layouts

## screen-layouts
title: Choose how Ledgerline is laid out
screen: layout
roles: Admin, Ops maker, Ops checker, Risk manager, Compliance officer, Branch staff, Authorised person
ask: screen layout; change layout; change the ui; change the look; new layout; layout options; runbook layout; inbox layout; workspaces; control room; role home; spreadsheet view; phone layout; dark mode; dark theme; compact tables; density; why does my screen look different; where is the sidebar; classic layout; go back to the old layout
related: screen-layout-firm, command-bar, client-page

Ledgerline can be laid out in nine ways. Every layout reaches every screen you may open; only the way round changes. Open [[layout|Screen layout]] (Admin, or **Screen layout** in the menu under your name) to see which applies to you and why.

- **Classic**: today's layout: the dark sidebar of grouped screens, the trade date and search across the top, and the dashboard you arrange yourself.
- **A · Day runbook**: the day as numbered steps from before the open to the commodity close. Each step shows whether it is done, needs someone or hasn't happened yet, and opens its screen with a banner saying which step it is, who owns it, what Autopilot did and the actions prepared for it. **[** and **]** move between steps; the **All** tab has every other screen.
- **B · Exceptions inbox**: only what needs a person: the work queue grouped by when it is due, with the chosen item's detail and its prepared action. **J** and **K** move, **O** opens the item's screen, **P** does the prepared action after you confirm, **X** closes it with a note. A narrow rail of icons opens the rest.
- **C · Workspaces**: a hub per department (Operations, Risk, Money, Compliance and so on) across the top, each with its own menu and a page of its daily steps. Closest to the classic layout.
- **D · Control room**: one dense, dark screen for the whole day: a timeline of the steps, the day's numbers, margin, Autopilot by stage, contract notes, what needs a person and the close checks. It refreshes itself every minute.
- **E · Supervised Autopilot**: Ledgerline does the work and people approve checkpoints: the equity close as a plan, the next approval waiting for you, how much each job does alone, and what Autopilot did. The box at the top asks Ledgy.
- **F · Role home**: a home of tiles chosen for your role. **Edit tiles** picks and orders your own; **Use my role's tiles** goes back.
- **G · Spreadsheet workbench**: spreadsheet-style tables of clients, margin, reconciliation breaks and the work queue, with saved views, filters, grouping, chosen columns, totals, a peek panel (**Space**) and export.
- **H · Phone approvals**: made for a phone: approvals waiting for you with the prepared action, today at a glance, clients and Ask Ledgy, with tabs along the bottom.

Your firm decides which layout you have. When it lets staff choose, pick yours on [[layout|Screen layout]]; **Follow the firm** goes back to the firm's. Whatever the layout, you choose your own **tables** (comfortable or compact) and **colours** (follow the computer, light or dark). The control room is always dark unless you choose light.

Branch staff and authorised persons see only their own clients, so they can use Classic, the Exceptions inbox, the Spreadsheet workbench or Phone approvals; a firm layout that works from the whole firm's day leaves them on Classic.

## screen-layout-firm
title: Set the firm's screen layout
screen: layout
roles: Admin
ask: set layout for the firm; layout for a role; layout per role; let staff choose layout; staff choose their own layout; firm layout; default layout; layout policy; phone layout automatically; client opens as a page; turn off command bar; new look; classic look
related: screen-layouts, command-bar, client-page

An Admin sets the firm's layout on [[layout|Screen layout]], then clicks **Save the firm's layout**. The change is recorded in the [[audit|audit trail]].

- **The firm's layout**: the one everyone has, unless a role or the person has another.
- **A layout per role**: for example the Control room for the risk manager and the Exceptions inbox for the ops checkers. A role's layout overrides the firm's.
- **Staff may pick their own layout**, and from which layouts. A person's own choice overrides both. Turning this off, or no longer offering a layout, takes people back to the firm's or their role's (their choice is kept, and returns if the firm offers it again). An Admin can always try any layout for themselves.
- **On a phone-sized screen, switch to Phone approvals**.
- **Building blocks every layout uses**: whether a client opens in the side panel (as before) or as a page with sections; the search and command bar (**⌘ K** or **Ctrl K**); the look (classic: IBM Plex and warm greys; new: Inter, cool greys, one accent colour, and status shown by shape as well as colour); and the tables and colours for people who haven't chosen their own.

Nothing about the firm's data changes with the layout: every screen, permission and check is the same in each.

## command-bar
title: Find anything with the command bar (⌘ K)
roles: Admin, Ops maker, Ops checker, Risk manager, Compliance officer, Branch staff, Authorised person
ask: command bar; command palette; cmd k; ctrl k; search everything; find a screen; jump to a screen; quick search; keyboard shortcut search; find work item
related: screen-layouts, client-page

Press **⌘ K** (Mac) or **Ctrl K** (Windows), or click the search box, in any layout. Type part of a client's name, UCC or PAN, a screen's name, or words from a work item:

- **↑ ↓** move, **Enter** opens.
- **Tab** on a client shows what you can do for them: open the client, the ledger, statements and reports, record a receipt or payout, send a message, or ask Ledgy about them. **Esc** goes back.
- With nothing matching, **Enter** asks Ledgy your words.

An Admin can turn the command bar off on [[layout|Screen layout]]; the client search ( **/** ) stays.

## client-page
title: The client page
roles: Admin, Ops maker, Ops checker, Risk manager, Compliance officer, Branch staff, Authorised person
ask: client page; client 360; open client as a page; client side panel; client drawer; client details page; client sections
related: screen-layouts, command-bar, screen-layout-firm

When the firm opens clients **as a page** ([[layout|Screen layout]]), clicking a client code anywhere opens the client's page instead of the side panel. It holds the same details and actions, with a bar of sections (identity, banks, demat accounts, segments, nomination, positions, holdings, trades, ledger, documents and so on) that jumps to each, the client's open work items with their prepared actions, and shortcuts to statements, the full ledger, money and messages. **Back to …** returns to the screen you came from.
