Ledgy help articles: letting Ledgerline's support team look at the firm's data, and ending it.

# Security

## ledgerline-support-access
title: Approve, refuse or end Ledgerline support access
screen: supportaccess
roles: Admin, Compliance officer
ask: support access; ledgerline support access; approve support access; refuse support access; support wants access; ledgerline staff access; vendor access to our data; provider access; can ledgerline see our data; who from ledgerline saw our data; end support session; end now support; support session open; remote support access; support access request email; approve ledgerline request; support access work item; give support read only access
related: ledgerline-support-rules

Nobody from Ledgerline can see your firm's data unless you approve. SEBI's cloud framework treats the provider's staff looking at a broker's data without the broker's permission as a breach, so every look goes through a request you decide on.

**When support asks**, you get an e-mail (at the management and compliance addresses in Settings) and a work item. Open [[supportaccess|Support access]] (Admin → Security). Each request shows:

- **who** asks (their name and the ID they will act as, such as `support:ravi.k@yourfirm`),
- **why**, and the **ticket** it is for,
- the **areas**: clients, ledger and money, exchange files, settings, or everything (full, only when they asked for it),
- **read-only** or **read and write** (read-only unless they asked for write),
- **how long**: 2 hours unless they asked for more, 8 hours at most.

**Approve…** lets you give less than was asked: fewer areas, read-only instead of write, fewer hours. Access starts when you approve and ends by itself when the hours run out. **Refuse** says no; your note is shown to support. A request nobody answers lapses after 24 hours.

**While support is in**, their screens carry a support banner, they reach only the areas you approved, they can change nothing unless you approved write, and:

- PAN, Aadhaar and bank account numbers are **masked** for them, even where your own staff see them in full. They cannot use **Show full PAN** or download files (PDFs, ZIPs, uploaded documents), because those cannot be masked.
- They can never add or change users, reset passwords or two-factor sign-in, change connection passwords and keys, pair the USB token signer or automations, or approve support access.
- Every screen they open and everything they change is in the audit trail under their support ID.

**End now** on an open access ends it at once: their next click is refused. Under **Open access** you see whether they have used it yet and how many sessions they opened.

## ledgerline-support-rules
title: Block Ledgerline support, the outage rule and the support access log
screen: supportaccess
roles: Admin, Compliance officer
ask: block ledgerline support; block all support access; stop ledgerline staff; withdraw support access; support access log; download support access log; support access csv; what did support do; who approves support access; compliance officer approve support; outage rule; standing approval for support; pre-approve support; read only for outages; cscrf third party access; vendor access log
related: ledgerline-support-access

On [[supportaccess|Support access]] (Admin → Security):

- **Block all Ledgerline support access** (Admin): open access ends at once, waiting requests are refused, and new ones are refused until you click **Allow requests again**. Support sees that your firm blocks them.
- **Rules → Who approves besides the Admin**: let the Compliance officer (or an Ops checker or Risk manager) approve, refuse and end access too. The Admin always can. The Compliance officer always sees the screen and the log.
- **Rules → outage rule**, off unless you turn it on: when support marks a request as an outage, a **read-only** request (not write, not everything) is let in at once, for the hours you set (2 unless you change it, 8 at most). You still get the e-mail and a work item, and can end it at any time.
- **Log** shows, from the audit trail, every request, decision, session start and end, and every screen support opened or change they made, with the time and their support ID. **Download log (CSV)** gives the same for your records or an inspection (the oversight of third parties that SEBI's cybersecurity framework, CSCRF, asks for).

Ledgerline's support people sign in with logins of their own, with two-factor codes, which are not logins of your firm. They never use one of your users, so your users' passwords and settings are never shared with them.
