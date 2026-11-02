Ledgy help articles: missing daily file alerts, BSE's not-permitted reasons on the Exchange UCC screen, order-to-trade ratio alerts and the ICCL files checked by hand.

# Exchange alerts

## missing-file-alerts
title: Missing daily file alerts
screen: xfiles
roles: Ops maker, Ops checker, Risk manager, Compliance officer, Admin
ask: missing file alert; file not received alert; daily file missing email; chase missing files; bhavcopy not received; trade file not received; file alert grace period; missing file work item; alerted at; stop missing file emails
related: exchange-files, iccl-files

On business days Ledgerline chases the files on the **daily file checklist** ([[xfiles|Exchange files]]) that are late. Weekends and exchange holidays (the approved exchange calendar and the holidays in Settings) are never chased.

1. When a file is still not received (or, for a file the firm sends, not written) **30 minutes** after its usual time, it gets one work item for the day, for the role that deals with it: the Ops maker for exchange files, the Risk manager for clearing-corporation files (VaR, haircuts, margins, collateral, allocation, margin reporting), the Compliance officer for surveillance files (DEBR, NOME, bulk deals, the order log, SEGMON).
2. Each run sends **one email** listing every file newly missing, through the firm's mail server (Connections). It goes to the management emails in Settings unless other recipients are set.
3. The checklist shows **Alerted at** for each file chased. When the file is loaded on Exchange files, its work item closes by itself.
4. Only the files of the clearing corporations chosen under **Cleared through** in [[settings|Settings]] appear, so a clearing corporation the firm does not use is never chased. Intraday files and files checked by hand are not chased.

The Compliance officer or Risk manager changes the rules with **Alert settings** on the checklist: switch the alerts off, change the grace period (1 to 600 minutes), or set the recipients (email addresses, or *management* and *compliance* for the emails in Settings). **Check now** runs the check at once.

## bse-not-permitted
title: Clients BSE does not permit to trade
screen: uccapi
roles: Ops maker, Ops checker, Risk manager, Compliance officer, Admin
ask: bse not permitted; bse ucc not permitted to trade; why can't client trade on bse; bse blocked client; bse not permitted reason; eqx not permitted; eqd not permitted; bse pan validation invalid; bse kra status; bse dp status pending
related: bse-ucc-api, exchange-files

BSE's **UCCs not permitted to trade** reports (EQX for cash, EQD for derivatives, loaded on [[xfiles|Exchange files]]) list the clients BSE will not take orders for, with the reason.

1. On [[uccapi|Exchange UCC]], BSE tab, the **BSE trading** column shows *Not permitted* for each such client, the segments, and each reason in plain words with the fix (for example: the PAN did not verify with the Income Tax database; check the PAN, name and date of birth and upload the UCC again). The figures come from BSE's latest list of each segment.
2. **Blocked by BSE** filters to those clients; select a client code to open the client.
3. The client's own page shows the same note at the top.

Each blocked client also has a work item for the Ops maker; it closes when the client drops off BSE's next list.

## otr-alerts
title: Order-to-trade ratio (OTR) alerts
screen: dealers
roles: Risk manager, Compliance officer, Admin
ask: order to trade ratio; otr; high otr; otr penalty; otr slab; order to trade ratio alert; algo order penalty; cooling off 15 minutes; too many orders no trades; otr by terminal; otr by client
related: dealers-nism, exchange-files

The exchanges charge a member whose daily ratio of algo order events (entries, changes and cancellations) to trades is high: below 50 nil; 50 to below 250, 2 paise an algo order; 250 to below 500, 10 paise; 500 to below 1000, 15 paise; 1000 to below 2000, 20 paise; 2000 or more, 25 paise. At 500 or more the member cannot place orders for the first 15 minutes of the next trading day. (SEBI/HO/MRD/DP/CIR/P/2018/62, BSE notice 20180622-41, SEBI/HO/MRD1/DSAP/CIR/P/2020/107, NSE/SURV/45016.) Orders within 0.75% of the last traded price, and for equity options within 40% of the premium or ₹20 whichever is higher, are not counted, nor market makers' orders (SEBI circular of 4 February 2026, BSE notice 20260406-8).

1. When BSE's order log (BO) is loaded on [[xfiles|Exchange files]], Ledgerline works out the ratio for the firm, each client and each terminal (with its dealer), and raises a work item for the Compliance officer for each at or above the alert level (50 by default), with the ratio and the slab.
2. [[dealers|Dealers & terminals]] → **Order-to-trade** lists each day's ratios with the slab; filter to those over the alert level or see all.
3. The order log does not carry the last traded price, so the price bands are not taken out: the ratio shown is at or above the exchange's. By default every order counts; the Compliance officer can count only terminals marked algo in the dealers register, and change the alert level and slabs, with **Change**.

## iccl-files
title: ICCL files checked by hand
screen: xfiles
roles: Ops maker, Ops checker, Risk manager, Compliance officer, Admin
ask: iccl file layout; iccl var file; varelmam; iccl haircut file; iccl margin file; iccl collateral report; icoll; client allocation to iccl; margin reporting to iccl; check by hand
related: exchange-files, missing-file-alerts

When a segment is cleared at **ICCL** (Settings → Cleared through), the checklist on [[xfiles|Exchange files]] lists ICCL's files as **Check by hand**: the VaR and ELM rates, haircuts, collateral reports, client margin file, the ICOLL response, client allocation and margin reporting.

ICCL has not published the column layouts of these files in a public circular, and Ledgerline never guesses a layout. Each line of the checklist says what is needed. Until then, download the files from ICCL's extranet and check them there; they are not chased as missing. To have Ledgerline read one, send its ICCL file description and a sample with made-up data to support.
