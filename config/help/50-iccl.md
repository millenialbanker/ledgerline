Ledgy help articles: the day at BSE Clearing (ICCL) for a firm that clears its own trades there.

# BSE Clearing (ICCL)

## iccl-day
title: The day at BSE Clearing (ICCL)
screen: xfiles
roles: Ops maker, Ops checker, Risk manager, Compliance officer, Admin
ask: iccl; bse clearing; bsecl; clear at iccl; iccl margin reporting; peak margin to iccl; iccl collateral segregation; segregation report to bsecl; iccl allocation; icclcoll; short allocation iccl; shortallocation_cm; reason code iccl; allocationdiff; allocation mismatch iccl; epn file; epf file; early pay-in iccl; pre-delivery margin; itm stock options margin; iccl trading limit; iccl pay-in cut-off; iccl layouts to confirm
related: iccl-files, exchange-files, position-limits

When the firm clears its own trades at **ICCL** (Settings → Cleared through: ICCL), [[xfiles|Exchange files]] shows a **BSE Clearing (ICCL)** panel for the date. A firm that clears at NSE Clearing does not see it.

**Each day, after the close:**

1. **Margin reporting file**: the margin actually collected from each client at end of day, and the peak margin collected during the day. Not reporting counts as 100% short, so upload it every trading day. The pre-delivery margin on in-the-money BSE stock options is part of the margin required.
2. **Collateral segregation report**: each client's collateral held at ICCL only (never NSE Clearing's), with the clients who have nil balance but traded in the last 12 months.
3. **ICCLCOLL allocation**: each client's cash at ICCL. The file is not made while any client's allocation is more than the cash the client gave, or below the margin you reported as collected. Fix those first; they are listed in the panel.

**When ICCL sends its reports:**

1. Load **SHORTALLOCATION_CM** on Exchange files. For each short client where collateral was in fact available, press **Add reason**: give the reason code (01 to 08) and the collateral available. You can give several reasons. Then download **Short allocation reasons**: it carries ICCL's own lines unchanged with your reason and amount added.
2. Load **ALLOCATIONDIFF**. Each mismatch becomes a work item: correct the allocation at ICCL or the next segregation report.

**Settlement at ICCL:** pay-in reminders use ICCL's cut-offs (funds and securities by 10:30 on T+1; auction pay-in by 08:00 with instructions by 07:50), set on RMS → Position limits → Settings. ICCL pays out directly to clients, so each client's sale is delivered in full from the CM pool account. Internal shortages at ICCL get their own work item to fund the settlement price plus 20% by 12:00 noon. The panel warns when no settlement account for BSE (Bank accounts) or no pool account (Firm demat accounts) is set up.

**Risk:** the panel lists the **pre-delivery margin** on long in-the-money BSE stock options (10%, 25%, 45%, 70% and 100% of the delivery margin from four trading days before expiry) and an estimate of the **trading limit** used. From 18:00 Ledgerline raises work items for both. Check BSECL's own trading limit figure on its extranet.

**Bank accounts:** for a firm that clears at ICCL, Bank accounts warns when client and settlement accounts together are more than 30.

**Layouts to confirm:** ICCL has not published the layouts of these files. Each one is built from the notice named in the panel's **ICCL layouts to confirm** list. Before the first upload, check each against ICCL's file description on its extranet and tell your Ledgerline provider what differs. The reason codes' meanings are also to confirm with ICCL.
