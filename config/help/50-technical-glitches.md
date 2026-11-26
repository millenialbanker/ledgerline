## technical-glitches
title: Technical glitches: record one, the clocks and the reports
screen: glitches
roles: Compliance officer, Risk manager, Admin
ask: technical glitch; glitch register; trading app down; login not working for clients; orders not reaching the exchange; 5 minutes glitch; report glitch to exchange; intimate nse within 1 hour; 2 hour intimation; preliminary incident report; annexure b; root cause analysis; rca 14 working days; annexure c; samuhik prativedan manch; common reporting platform; tell clients about glitch; glitch pop-up sms
related: glitch-exemption-specified, cyber-controls, security-incidents

SEBI's technical glitch framework covers a broker whose clients trade online (internet or app) with more than 10,000 registered clients, or one an exchange names. Ledgerline reads this from **Settings → Firm modules**; until that is answered it uses its own count of registered clients on 31 March and your answer on online trading. When the framework does not apply, the screen says so and nothing is chased.

**What counts.** A stoppage, slowdown or variance of login, placing, changing, cancelling or executing orders, or the view of margin, collateral or funds, for **5 contiguous minutes or more** during trading hours, in the firm's own systems or an outsourced one. Left out: a global outage of a cloud provider, a problem at an exchange, clearing corporation or depository, KYC or account opening systems, back-office-only systems and payment gateways. For any other exclusion the circular lists, choose **Other exclusion** and say which.

**Record it.** Open [[glitches|Technical glitches]] and click **Record a glitch**: what failed, what it affected, when it began (and ended, once it has), the exchanges affected and the clients affected. Ledgerline applies the test. While it is still going on, it becomes reportable at 5 minutes.

**The clocks**, all counted from when the glitch began:

- **NSE within 1 hour**, if the firm is an NSE member (NSE takes the common submission for the other exchanges).
- **Other exchanges and clients within 2 hours.** Click **Tell clients** to send the notice through Client messages (e-mail, SMS, WhatsApp; edit the "glitch.notice" templates on [[messages|Client messages]]), or record the notice on your website or app pop-up.
- **Preliminary Incident Report (Annexure B) by T+1**: the next trading day, or the one after if that is a holiday.
- **Root Cause Analysis (Annexure C) within 14 working days.**

Both reports are filed on the **Samuhik Prativedan Manch** portal. **Draft Annexure B** and **Draft Annexure C** give the content from the register; copy it into the portal's form and record the reference. Each clock not met is on the compliance officer's work queue until it is.

**Also a security incident?** If an attack caused the glitch, click **Also a security incident**. The two records are linked and each keeps its own clock: CERT-In's 6 hours on [[incidents|Incidents]], the exchanges' on this screen.

## glitch-exemption-specified
title: Glitches: MCX's exemption, specified stockbrokers and capacity
screen: glitches
roles: Compliance officer, Admin
ask: mcx exemption financial disincentive; 2 percent unique clients; 2000 clients glitch; app only glitch; specified stockbroker; top 20 brokers nse; enhanced glitch obligations; capacity planning trading systems; peak load monitoring; lama; bcp dr specified broker
related: technical-glitches, cyber-resilience

**MCX's exemption.** For a minor or app-only glitch, MCX does not levy the financial disincentive if the unique clients affected stayed below 2% of the quarter's average unique clients or 2,000. Ledgerline takes the lower of the two. Click **MCX exemption evidence** on the glitch, give both counts and what they rest on (the logs or audit trail, and who checked). Keep the logs themselves.

**Specified stockbroker.** NSE names the top 20 brokers by online clients among those with more than 10,000 (NSE/COMP/74381). Once named, the enhanced obligations apply within two months: capacity planning every half year, BCP-DR with a DR site in another seismic zone and drills, and API-based monitoring (LAMA). Say so in **Settings → Firm modules**; the screen then lists them and the compliance calendar dates them.

**Capacity.** Plan the capacity of the trading systems each year (half-yearly for a specified broker) and watch the peak load. For Ledgerline itself, [[cyber|Cyber controls]] → **Capacity** shows the load, the busiest minute, the answer times and the data growth. The trading front end, RMS and network are yours or your vendors' to measure.
