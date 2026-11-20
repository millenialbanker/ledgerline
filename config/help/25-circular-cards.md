Ledgy help articles: whether a circular applies to the firm (the firm profile), obligations proposed by AI and accepted by the compliance officer, and the Circulars board where each obligation is a card with its owner, due date, steps and evidence.

# Circulars become cards

## circular-applies
title: Does a circular apply to the firm?
screen: circulars
roles: Compliance officer, Admin, Ops maker, Ops checker, Risk manager
ask: does this circular apply to us; circular applies; circular not applicable; why does it say doesn't apply; not sure if circular applies; applicability of circular; circular applies to dp; override applicability; circular wrongly marked not applicable; mark circular as applying
related: firm-profile, circulars, circulars-ai, circulars-board

Every circular on [[circulars|Circulars & calendar]] has one of three answers, with the reason: **Applies** (for example "you are a CDSL DP"), **Doesn't apply** ("you have no MTF") or **Not sure** ("tell Ledgerline your CSCRF category"). Ledgerline works it out by rules from the [[settings|firm profile]]:

- an exchange's circular is for its members, a clearing corporation's for those who clear their own trades there, a depository's for its participants;
- a circular addressed only to portfolio managers, investment advisers, mutual funds, or the exchanges, clearing corporations and depositories, is not for a stock broker unless the firm holds that registration;
- a subject about a segment the firm does not trade (commodity, currency or equity derivatives), or about MTF, algo trading, NRI clients, authorised persons, branches, BSDA or CSCRF when the firm has none of it, does not apply;
- when the answer turns on something the profile does not know, it is **Not sure** and says what to answer.

The rules never close a circular: it stays in **New circulars** until the compliance officer has looked at it. Open it and, under **Your decision and the reason**, choose **It applies** or **It doesn't apply** with the reason, or **Back to Ledgerline's rules**. The decision, the reason and what the rules said go to the audit trail. **It doesn't apply** marks the circular not applicable, and its cards move to **Not applicable** on the Circulars board.

The rules read words in the circular's subject and text. They are a first sort, not legal advice: check every circular that applies or may apply.

## firm-profile
title: The firm profile for circulars
screen: settings
roles: Compliance officer, Admin
ask: firm profile; what circulars apply to us; set cscrf category; cscrf category; are we a dp; nsdl participant; algo trading yes no; internet trading; mobile trading; nri clients; other sebi registrations; portfolio manager registration; firm profile settings; ai tools outside ledgerline; no mtf
related: circular-applies, compliance-profile, aiml-report, ras-calendar

Settings → **Firm profile** (compliance officer and Admin) is what the rules read to tell whether a circular applies.

- **Read from Ledgerline:** exchanges and segments (member codes and **Cleared through** in Settings); the clearing route of each segment (the firm's own clearing at a clearing corporation, or another clearing member); a CDSL DP (the CDSL connection has the firm's DP ID); MTF (also on Circulars & calendar → RAS dates & profile); authorised persons, branches and NRI clients (Partners, branches and the client list).
- **Answered here:** CDSL and NSDL participant, algorithmic trading, internet or mobile trading (clients trade through the firm's website or app), NRI clients, authorised persons, branches and AI or ML tools used outside Ledgerline (each **Yes**, **No** or **Go by what Ledgerline finds**), MTF, other SEBI registrations, and the CSCRF category.

**What the answers change in the compliance calendar:**
- **MTF:** with MTF the September net worth certificate is due by 14 November; without it by 29 November. The March certificate is due by 30 May either way.
- **Internet or mobile trading, or algo trading,** with a mid-size or small-size CSCRF category: the cyber security audit is half-yearly. Without either, it is yearly. A qualified RE is always half-yearly.
- **Algo trading:** the AI & ML report is half-yearly with algo, yearly without (unless you choose a cycle on Exchange disclosures). The compliance profile warns if the system audit cycle does not match.
- **AI or ML tools outside Ledgerline: No:** the AI & ML report says there is nothing to report besides Ledgerline itself.
- **Running account settlement** (shown here, set on Circulars & calendar → RAS dates & profile): with every client quarterly, the monthly settlement duty does not apply.

CSCRF categories for stock brokers (SEBI/HO/ITD-1/ITD_CSC_EXT/P/CIR/2025/60 of 30 April 2025, under the framework of SEBI/HO/ITD-1/ITD_CSC_EXT/P/CIR/2024/113 of 20 August 2024): **Qualified RE** (more than 10 lakh registered clients or more than ₹10,00,000 crore of clients' trading in the year), **Mid-size** (more than 1 lakh up to 10 lakh clients, or more than ₹1,00,000 crore up to ₹10,00,000 crore), **Small-size** (more than 10,000 up to 1 lakh, or more than ₹10,000 crore up to ₹1,00,000 crore), **Self-certification** (more than 1,000 up to 10,000, or more than ₹1,000 crore up to ₹10,000 crore); below both 1,000 clients and ₹1,000 crore the framework does not apply. The higher category applies when either measure puts the firm there.

Questions still open are shown at the top of the profile and on Circulars & calendar. Every save goes to the audit trail. Nothing in the profile is client data, and it is never sent to an AI model.

## circulars-ai
title: Obligations proposed by AI, accepted by the compliance officer
screen: circulars
roles: Compliance officer, Admin
ask: read circular with ai; ai proposed obligations; accept obligation; reject obligation; change and accept; what does this circular require; ai reading circulars; circulars ai switch; proposed by ai; obligations to review
related: circular-applies, circulars-board, ai-inventory, ai-kill-switch

With **Read circulars with AI** switched on in Settings → Ledgy (by an Admin; off until then, and never in India-only mode), the AI model reads a circular's text and proposes what the firm must do. Each proposal has what to do, by when (a date the circular gives, or so many days after the circular or its effective date; 14 days after the circular when it gives none), the owner role, the evidence to keep, the steps, and the part of Ledgerline it may change (CUSPA payment days, margin rules, BSDA, nomination, KYC, payouts, the investor charter, CSCRF, reporting formats and so on).

New circulars of high or medium priority that apply or may apply are read once by themselves; on any circular the compliance officer can press **Read with AI** or **Read again with AI** (proposals still waiting are replaced; accepted and rejected ones stay).

They are proposals only. Nothing becomes an action, a card in work or a work-queue item until the compliance officer **Accepts** it, **Changes and accepts** it (what, due date, owner, evidence, area, steps, or that no evidence is needed), or **Rejects** it with the reason. Under regulation 16C of the SEBI (Intermediaries) Regulations, 2008 (added by notification SEBI/LAD-NRO/GN/2025/226 of 6 February 2025) the firm is solely responsible for the output of the AI tools it uses, so a person decides each one. The model never decides whether a circular applies.

The circular's number, date, issuer, subject and public text are sent, masked like any other text; nothing about the firm or its clients. Each call is metered from the firm's AI credit and written to the AI answer log. With the feature off, refused or failing, the circular keeps Ledgerline's rule-based actions, as before.

## circulars-board
title: The Circulars board: obligations as cards, with evidence
screen: boards
roles: Compliance officer, Admin, Ops maker, Ops checker, Risk manager
ask: circulars board; circular cards; obligation card; evidence for a circular; attach evidence to circular; move circular card to done; cannot move card to done; waiting for evidence; ledgerline updates; ledgerline may need a change; daily circulars notice; new circulars notice
related: ledgerline-boards, ledgerline-board-moves, circular-applies, circulars-ai

An Admin sets up the **Circulars** board on [[boards|Boards]] → **Set up Ledgerline boards**. Each obligation of a circular that applies or may apply is a card: the rule-based actions, those the compliance officer added, accepted AI proposals, and AI proposals waiting to be accepted. Its lists are **To review**, **Accepted / to do**, **In progress**, **Waiting for evidence**, **Done** and **Not applicable**.

A card is made with the circular's number, date and issuer, what to do, by when, the owner, the evidence needed and the Ledgerline area it may change in its description; a **Steps** checklist; and an **Evidence** checklist. The card's Ledgerline section shows the obligation live, with **Open in Ledgerline**. Labels give the issuer, the owner role, **High priority**, **Applies? Not sure**, **From AI reading** and **Ledgerline may need a change**.

Moving a card does the circular's own work, as you:

- **To review → Accepted / to do** accepts it (compliance officer or Admin).
- **In progress** and **Waiting for evidence** move it on (its owner role, or the compliance officer).
- **Done** closes the action with the card's evidence: at least one file or link attached and every item of the **Evidence** checklist ticked, unless the action needs no evidence. Otherwise the move is refused with the reason. The files and ticked items are recorded with the action on Calendar & circulars.
- Nothing goes back to **To review**, and nothing is moved to **Not applicable** from the board: on Calendar & circulars use **Not needed** (with the reason), **Reject**, or **It doesn't apply**.

The other way round, a change on Calendar & circulars moves the card. **Ledgerline updates** in the board's header shows only the cards whose obligation may need a change to a Ledgerline setting or rule, for the Admin or your Ledgerline provider; Circulars & calendar has the same list as a tab.

Once a day after 09:30 the compliance officer gets a notice in the bell, for example "1 new circular applies to you, 3 may apply, 4 obligations need review". It carries counts only, never the circulars' content.
