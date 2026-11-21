Ledgy help articles: roles and permissions per screen.

# Roles

## roles-overview
title: How roles and permissions work
screen: roles
roles: Admin
ask: roles and permissions; custom role; make my own role; permissions per screen; who can see which screen; limit what a user sees; restrict a screen; give crm to some people; brokerage settings for one person; edit a role; built-in roles
related: role-editor, give-roles, role-approval, role-preview, permission-denied

A role says, for every screen in the sidebar, whether its holders may:

- **View**: open the screen and read it;
- **Do**: create and change things on it (the maker's power);
- **Approve**: approve what someone else entered (the checker's power).

It also sets four limits: which clients the person reaches (the whole firm, their own branch, or their own authorised person), whether they see brokerage totals, whether they may reveal full PAN, Aadhaar and bank numbers, whether they may download files and exports, and the largest payout they may approve.

The **built-in roles** (Ops maker, Ops checker, Risk manager, Compliance officer, Branch staff, Authorised person, HR manager, Payroll, Admin) work exactly as they always have until the firm edits them. Admin always has everything and cannot be edited. You can edit a built-in role (with **Reset to default** to undo it) or **Copy** one into a new role.

On [[roles|Roles]] each role has a **Summary** of what it can and cannot do. Every action is checked again by the server, whatever the screen shows: a person without a screen gets "not permitted" from its links too.

A person may hold more than one role: they may do everything any of their roles allows. Their roles must all reach the same clients.

## role-editor
title: Make or edit a role
screen: roles
roles: Admin
ask: new role; create a role; role editor; copy a role; edit ops maker role; take away a screen; tick view do approve; payout approval limit; role limits; reset role to default; retire a role
related: roles-overview, role-approval, role-preview

On [[roles|Roles]], click **New role**, or **Edit** or **Copy** on a role. The editor has four steps:

1. **Start from:** pick a built-in role to start from, or Blank. Give the role a name and a short description.
2. **Screens:** tick **View**, **Do** and **Approve** for each screen. Ticking a section or a group ticks every screen in it. Do and Approve include View.
3. **Limits:** branch scope, brokerage totals, full identifiers, downloads, and the payout approval limit in rupees (blank for no limit).
4. **Review:** read what the role can and cannot do and the warnings, then **Preview as this role** or **Send for approval**.

Where you keep a tick the starting role had, the new role does there what that role does today. A tick you add gives every action of that kind on the screen. Actions only an Admin may take stay with Admin, except on the Admin section's screens. Some stay with Admin whatever the role: users and roles (a role may only look at those screens), sign-in resets, full data exports, closing the account and deleting the firm's data, the firm's encryption keys and Ledgerline support access settings.

Of the built-in roles, only the Compliance officer (and Admin) has [[practices|Firm practices]], Support access, Staff app and Client app. The Compliance officer only looks at Staff app and Client app. Settings → Modules is the Admin's alone, and switching a module on or off stays with Admin whatever the role. Every staff role has My HR, Staff, Attendance and Leave, each showing only the person's own records and their team's; Joiners & leavers, Certificates & own trading, HR policies and Payroll go with the HR manager, Payroll and Compliance officer roles, as the People screens have them. With the HRMS module off, the People screens are hidden for every role. Give a role **Approve** on Firm practices to let its holders approve a change to a firm practice; answering the questions needs **Do** there.

A role limited to a branch or an authorised person reaches only the client-level screens (clients, receipts and payouts, debtors, RMS, CRM and the like), each showing only those clients.

**Reset to default** puts an edited built-in role back as shipped. **Retire** takes a role the firm made out of use; nobody may hold it then. Both need a second Admin, like any change.

## give-roles
title: Give someone roles
screen: users
roles: Admin
ask: give a user a role; assign role; more than one role; two roles; change someone's roles; give crm role; role waiting for approval
related: roles-overview, role-approval, add-user

1. On [[users|Users]] (or **Who holds which roles** on [[roles|Roles]]), click **Change roles…** next to the person.
2. Tick their roles. The first one they already held stays their main role.
3. For a role limited to a branch or an authorised person, give the branch or the partner code.
4. Click **Send for approval**. Nothing changes until another Admin approves.

A single built-in role can still be chosen in the role list on [[users|Users]], as before.

## role-approval
title: Approve a role change
screen: roles
roles: Admin
ask: approve role change; second admin; role change pending; who approves roles; maker checker for roles; reject role change; withdraw role change
related: roles-overview, role-editor, give-roles

Every change to a role, and giving someone roles on the Roles screen, is a proposal. It takes effect only when **another Admin** approves it.

1. On [[roles|Roles]], **Waiting for a second Admin** lists the proposals. **Details** shows what the role would do and the warnings.
2. **Approve** it (it takes effect at once) or **Reject** it with the reason.
3. The Admin who proposed it can **Withdraw** it instead.

The firm needs at least two Admins for this. Every proposal, decision and change is in the [[audit|Audit trail]] and is a security event.

## role-preview
title: Preview as a role
screen: roles
roles: Admin
ask: preview role; see what a role sees; view as role; test a role; preview as this role; end preview
related: role-editor, roles-overview

To check a role before (or after) it is approved, click **Preview as this role** on the editor's Review step, or **Preview** next to a role on [[roles|Roles]].

The console then shows only what the role would see: its sidebar, tabs and buttons. The preview is read-only: nothing can be changed, and starting and ending it is recorded in the audit trail. Click **End preview** at the top of the page to work as yourself again.

A preview of a role limited to a branch shows its screens with the whole firm's clients, because it is not tied to a person's branch.
