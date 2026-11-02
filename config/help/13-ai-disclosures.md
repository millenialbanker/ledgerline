Ledgy help articles: the AI controls (kill switch, masking, AI inventory, AI answer log) and the Exchange disclosures (the AI & ML systems report and the SaaS use report).

# AI controls and exchange disclosures

## ai-kill-switch
title: Switch AI off (the kill switch)
screen: aictl
roles: Compliance officer, Admin
ask: switch off ai; turn off ai; kill switch ai; stop ai; disable ai; ai off; block openai; block anthropic; block gemini; stop sending to ai provider; ai incident
related: ai-answer-log, ai-masking

On [[aictl|AI controls]], **Switch all AI off** stops every AI feature for the firm at the next call. Give the reason: it goes in the audit trail with your name. Ledgy keeps answering from its help articles, and scanned bills are typed by hand.

- **Switch off** next to a provider refuses that provider only (for example while its contract is reviewed).
- Whoever runs the server can also switch AI off for every firm with LEDGERLINE_AI_OFF=1.
- Each feature still needs its own switch in [[settings|Settings]] (Ledgy phrasing, reading bills); the kill switch wins over them.
- **Switch AI back on** also needs a reason.

## ai-masking
title: What is masked before an AI call
screen: aictl
roles: Compliance officer, Admin
ask: ai masking; what does ledgy send; is client data sent to ai; pan sent to ai; mask names ai; tokens in ai answer; aadhaar ai; personal data ai
related: ai-kill-switch, ai-answer-log

Before anything goes to an AI provider, Ledgerline replaces identifiers with tokens such as [PAN-1] or [CLIENT-2], and puts the real values back only in the answer shown to you:

- PAN, Aadhaar-like numbers (12 digits) and bank account numbers: **always** masked.
- Phone numbers, emails, client codes, client names and staff names: masked by default. Names and codes come from the firm's own client and staff lists.

Only an Admin can stop masking a category, on [[aictl|AI controls]]. The AI answer log is always fully masked, whatever is switched off. A scanned bill is an image and cannot be masked: it is the firm's own purchase bill, never client data.

## ai-answer-log
title: The AI answer log
screen: aictl
roles: Compliance officer, Admin
ask: ai answer log; ai log; what did ledgy send to ai; ai calls; export ai log; ai audit; ai retention; ai prompts
related: ai-masking, ai-credit

Every call to an AI model is logged: the time, the user, the feature, the provider and model, the tokens, the charge, the masked prompt, the masked answer and the outcome (answered, provider error, not metered, empty or unreadable answer).

1. Open [[aictl|AI controls]] → **Answer log**, choose the dates and **Show**.
2. **Download CSV** exports the period for an audit or an inspection.
3. **Change** sets how long prompts and answers are kept (at least 365 days; five years unless changed). Older entries keep the time, user, tokens, charge and outcome.

The log never holds an unmasked PAN, number or name.

## ai-inventory
title: The AI inventory
screen: aictl
roles: Compliance officer, Admin
ask: ai inventory; ai systems list; which ai do we use; ai register; add ai tool; ai used outside ledgerline; chatbot register; ml model register
related: aiml-report, ai-kill-switch

[[aictl|AI controls]] → **AI inventory** lists every AI use: Ledgerline's own (Ledgy phrasing and reading scanned bills), each with its purpose, provider and model, what is sent, who can use it, its controls and whether it is on for the firm; and the AI tools the firm uses elsewhere.

1. **Add an AI tool** for any AI or ML system used outside Ledgerline (a surveillance model, a chatbot, a research tool): what it does, the vendor, its answers for the AI & ML report and when it was in use.
2. Tick **A cloud (SaaS) service** to add it to the SaaS report as well.

The inventory feeds the AI & ML report on [[exdisc|Exchange disclosures]].

## exchange-disclosures
title: File the AI & ML and SaaS reports with the exchanges
screen: exdisc
roles: Compliance officer, Admin
ask: exchange disclosures; ai ml report; aiml submission; artificial intelligence report exchange; machine learning report; saas report; saas half yearly; nil ai report; enit ai ml; mcx member portal ai; mark ai report submitted
related: aiml-report, saas-report, circulars

[[exdisc|Exchange disclosures]] prints ready answers for the two forms the exchanges collect on their member portals:

1. Open the **AI & ML** or **SaaS** tab. The latest period that has ended is shown; choose another in the list.
2. Check each answer. **Edit** changes one (Ledgerline's own answer stays shown beside it); **Copy** copies it for the portal; **Copy all** copies the whole form.
3. File it on the exchange's portal.
4. **Mark submitted** for each exchange, with the date, the reference number and a screenshot or the acknowledgement. The answers are then kept as filed.

Each filing is a duty in the compliance calendar ([[circulars|Circulars & duties]]); a member of NSE and other exchanges has one, with NSE. It shows as due a week before and closes when marked submitted. **Download CSV** gives the answers and the filings.

## aiml-report
title: The AI & ML systems report
screen: exdisc
roles: Compliance officer, Admin
ask: aiml report answers; ai ml half yearly; ai ml quarterly; is ledgy ai; order execution ai; does ledgy give advice; ai safeguards; system audit ai; single filing nse ai
related: exchange-disclosures, ai-inventory

SEBI's circular of 4 January 2019 (SEBI/HO/MIRSD/DOS2/CIR/P/2019/10) asks brokers to report the AI and ML systems they use. It was quarterly, within 15 days of the quarter's end; NSE and MCX now collect it **half-yearly** (April–September and October–March, within 15 days) from members trading through algorithms, and yearly from others. Choose the cycle under **Details for the forms**.

- The header comes from [[settings|Settings]]: SEBI registration, firm name and PAN.
- Each AI system in use at any time in the period gets an entry, answered from the AI inventory: Ledgy does not initiate or route orders, manage portfolios, give advice (it answers staff questions about operations) or detect attacks; it is implemented by the vendor (Ledgerline) using the connected provider's model; its controls and safeguards are the kill switch, masking, the answer log and no autonomous actions.
- If AI was off for the whole period, the answer is **No** (no system falls under these categories).
- A member of NSE and other exchanges files it once, with NSE, which shares it with BSE, MCX and the others (NSE/INSP/69184, Phase I item 40): the calendar has one duty, **File with NSE (shared with the other exchanges)**.

## saas-report
title: The SaaS use report
screen: exdisc
roles: Compliance officer, Admin
ask: saas report answers; saas half yearly report; data hosted on saas; saas grc; cloud service provider report; deployment hosted self hosted; saas gaps
related: exchange-disclosures, data-in-india

The exchanges collect the SaaS report half-yearly (January–June by 31 July, July–December by 31 January), under SEBI's advisory on SaaS solutions of 3 November 2020 (SEBI/HO/MIRSD2/DOR/CIR/P/2020/221).

1. Under **Details for the forms**, set the **Deployment**: hosted by Ledgerline, on the broker's own server, or in the broker's own cloud account, with the hosting provider and region; and the information security contact.
2. **Hosted by Ledgerline**: one row for Ledgerline (back-office application; client KYC, ledger, holdings, trades; the region you entered). Edit the gaps and the deadline after your assessment.
3. On your own server or cloud account, Ledgerline is not SaaS: the answer is **No** unless AI was on.
4. When AI was on in the period, the AI provider is a SaaS row too. Add other SaaS services the firm uses (email, GRC…) under **Other SaaS services**.
5. A member of NSE and other exchanges files it once, with NSE, which shares it with the others (NSE/INSP/70505, Phase II item 19): the calendar has one duty, **File with NSE (shared with the other exchanges)**. Without NSE, it is filed with each exchange.
