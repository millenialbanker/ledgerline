Ledgy help articles: Compliance → Registers (TDS and GST records, notices to clients, the website's lists, AP payout holds, NSE user types, terminal turnover, the DP's records) and the related checks on vendor bills, receipts, deposits, slips and closures.

# Registers and smaller duties

## compliance-registers
title: Compliance registers: what to record and where
screen: compregs
roles: Compliance officer, Ops checker, Ops maker, Admin
ask: compliance registers; registers screen; record a tds certificate; lower deduction certificate; nil deduction certificate; form 15g; no deduction declaration; e-invoice irn; irp; record irn; withdraw a record; cdsl bill to dp
related: tds-vendor-checks, gst-2b-credit, website-lists, dp-small-duties

Some rules ask the firm only to keep a record. Each is a register on [[compregs|Registers]], under its tab (Tax and GST, Partners and website, Depository).

1. Click **Record** on the register, fill in the fields and save. Dates are YYYY-MM-DD; amounts in rupees.
2. A record is kept for good. If one is wrong, click **Withdraw** and say why; it stays in the list, marked withdrawn.

The registers: **TDS certificates issued**, **Lower or nil deduction certificates**, **No-deduction declarations received**, **Vendor PAN status checks**, **E-invoice IRNs**, **Notices to branch clients of an AP change**, **Notice to clients before proprietary trading**, **Website updates**, **Rights and Obligations given**, **DP-attested client master lists given**, **Association of persons: demat account declarations** and **CDSL's monthly bill to the DP**.

Questions the firm answers once (its turnover, its GST turnover for e-invoicing, the e-mail given to BSE, NSE's user ID limits, how it clears equity derivatives, CDSL's status code for unpaid stamp duty, voting on pool securities, the link to the Rights and Obligations document) are in [[practices|Firm practices]] under **Tax, partners and DP**.

## tds-vendor-checks
title: TDS on vendor bills: purchases of goods, PAN status, certificates
screen: books
roles: Ops maker, Ops checker, Compliance officer
ask: 194q; tds on purchase of goods; 50 lakh threshold; turnover 10 crore tds; inoperative pan; pan not linked with aadhaar tds; higher tds rate 20%; 206aa; lower deduction certificate rate; tds certificate due; form 16a; traces certificate 15 days; quarterly statement lower deduction
related: compliance-registers

When a vendor bill is booked on [[books|Firm accounts & GST]], Ledgerline warns (it never blocks) when the TDS looks wrong:

- **Purchases of goods** (Income-tax Act, 2025 s.393(1) Table Sl. 8(ii), was 194Q). Only if the firm's turnover was above ₹10 crore last year (answer it in Firm practices each April), and only on what is bought from that seller beyond ₹50 lakh in the year. When purchases from a vendor first cross ₹50 lakh, the bill says so.
- **No PAN or an inoperative PAN** (s.397, was 206AA): 20%, or 5% for goods. Record the result of each PAN check in **Vendor PAN status checks**; the latest one counts.
- **A lower or nil deduction certificate**: record it with its rate, amount and dates. Bills then expect its rate until the amount is used up. A **no-deduction declaration** for the year means no TDS.

For the quarterly statement, download **Lower and nil deductions** on Registers → Tax and GST: the bills taxed under a certificate or declaration, with the reason.

TDS certificates: Registers → Tax and GST lists each vendor and AP with TDS by quarter and the last day to give the certificate (15 August, 15 November, 15 February, 15 June). Download it from TRACES, give it, and record it. A work item comes 7 days before the date. Salary certificates are not in this list.

Self-inspection tests: **tds_vendor_rates**, **tds_certificates**.

## gst-2b-credit
title: GSTR-2B match, credit unpaid 180 days, reverse charge and credit notes
screen: compregs
roles: Ops checker, Compliance officer
ask: gstr-2b; gstr 2b match; input tax credit not in 2b; itc reversal 180 days; unpaid vendor 180 days; rule 37; reverse charge 3.1(d); rcm gstr-3b; credit note brokerage reversal; debit note gst; cdnr; table 9b; bill corrected after return; e-invoice; aato 10 crore 30 days
related: compliance-registers, tds-vendor-checks, correct-bill

**GSTR-2B.** Download the month's GSTR-2B from the GST portal (JSON, or the B2B sheet saved as CSV) and load it on [[compregs|Registers]] → Tax and GST with its period. Every vendor bill with credit taken is matched by the supplier's GSTIN and invoice number: In GSTR-2B, Not in GSTR-2B, Tax differs or Credit not available. Hold or reverse the credit for those not matched.

**180 days.** A bill with credit taken and some value unpaid 180 days after its date is listed with the credit to reverse (CGST Act s.16(2)); a work item asks for it. Take the credit again once the vendor is paid.

**Reverse charge.** The GSTR-3B summary on Firm accounts & GST shows table **3.1(d)** from vendor bills marked reverse charge, and adds that tax to the tax payable in cash.

**Credit and debit notes.** A bill corrected after the 10th of the next month (its GSTR-1 already due) is not changed in the month filed. The change becomes a credit note (less brokerage) or a debit note (more) of the month the correction was made. Click **Issue this month's notes** to number them (CRN/… and DBN/…). They appear in GSTR-1 table 9B (CSV **cdnr**) for clients with a GSTIN, are netted into table 7 for the others, and count in table 13.

**E-invoices.** Answer the GST turnover question in Firm practices. Above ₹5 crore, record the IRN of each invoice to a client with a GSTIN in **E-invoice IRNs**; at ₹10 crore or more, within 30 days of the invoice date.

Self-inspection tests: **itc_gstr2b**, **itc_180_days**, **gst_credit_notes**, **einvoice_irn**, **stamp_duty_rates** (the charge schedule's stamp duty at the Schedule I rates).

## website-lists
title: The website's lists and notices to clients
screen: compregs
roles: Compliance officer, Ops checker
ask: website ap list; authorised persons on website; ap cancelled disciplinary; uscnba on website; bank accounts on website; payment only into these accounts; notify branch clients ap change; change of authorised person notice; proprietary trading notice to clients; start prop trading; scores link
related: compliance-registers, ap-payout-hold

On [[compregs|Registers]] → Partners and website:

- **Website: USCNBA accounts** is the text of the bank accounts clients may pay into, with the note. **Website: Authorised persons** lists the active APs and those cancelled for disciplinary reasons, with their exchange registrations and terminals. Click **Copy HTML**, put it on the website, then **Mark the website updated**. When a list changes, a work item asks you to update the site again.
- **Notify branch clients of an AP change** writes to every active client of the branch before the change takes effect, and keeps the notice. Send it before moving the branch's clients to the new AP.
- **Tell clients about proprietary trading** writes to every active client before the firm starts trading on its own account.

The client portal and app show the SEBI SCORES and SMART ODR links on the home screen.

Self-inspection tests: **website_lists**, **ap_change_notice**, **prop_trading_notice**.

## ap-payout-hold
title: Holding an AP's payouts, cancelling an AP, NSE user types, terminal turnover
screen: compregs
roles: Compliance officer, Ops checker, Ops maker
ask: hold ap payout; withhold ap commission; ap irregularities; ap complaints unresolved; cancel authorised person; nse user type; corporate manager branch manager dealer; user id limit; terminal turnover; low volume terminal; declining terminal
related: website-lists, ap-payout

**Payout hold.** On finding irregularities by an AP, click **Hold or cancel** on its line, choose **Held** and say why. Every AP payout worked out while the hold is on withholds the whole net amount (it stays in the AP's account) until the hold is lifted. The self-inspection test **ap_payout_hold** lists APs whose clients have complaints past their due date and no hold, for you to judge.

**Cancellation.** Record the date and whether it was for disciplinary reasons. Disciplinary cancellations appear on the website's AP list.

**NSE user types.** Set each open NSE terminal's type (corporate manager, branch manager, dealer) and enter NSE's limits in Firm practices. Test **nse_user_limits**.

**Terminal turnover.** Each terminal's turnover by month for six months, with terminals to review: last full month below a tenth of the median terminal, or lower three months running. Download the CSV if MCX asks for it. Test **terminal_turnover**.

## dp-small-duties
title: DP records: R&O, attested CML, AoP accounts, CDSL's bill, "to be closed" list
screen: compregs
roles: Ops checker, Ops maker, Compliance officer
ask: rights and obligations; r&o kit; dp account opening kit; attested cml; client master list attested; cml not older than two months; association of persons demat; aop principal officer pan; cdsl bill; true to label; pass through charges; to be closed accounts; no amc illiquid suspended delisted; stamp duty overdue insufficient; off-market slip set up late; delay reason; tlh; nominee to legal heir; voting pool account
related: compliance-registers, dp-billing

On [[compregs|Registers]] → Depository:

- **Send the Rights and Obligations** e-mails the client the link to the document on the firm's website (enter the link in Firm practices) and logs it. Record a printed copy by hand. Test **ro_kit_delivery**.
- **Issue an attested client master list** sends a signed copy of the account's client master, from CDSL's client master as last loaded; it must be not older than two months. It is logged.
- **Association of persons**: record the AoP's demat account with both PANs and the date of its no-equity confirmation. Test **aop_accounts** flags equity shares in it.
- **CDSL's bill**: enter each head (transaction charges, stamp duty) of CDSL's monthly bill. The table above it compares what clients paid. Test **dp_pass_through**.
- **"To be closed" list**: load CDSL's periodic list. Accounts on the latest list get no maintenance charge on DP bills.

Slips: an off-market slip is set up the day it is received or the next. One set up more than 2 days after receipt raises a work item for the reason (**Reason for a slip set up late**). For reason **Others**, say what the transfer is for. A slip from an account that received a transmission defaults to reason **Transmission**, sent to CDSL as **TLH** (nominee to legal heirs). Tests **dis_setup_delay**, **transmission_tlh**.

Stamp duty: enter CDSL's status code for "Overdue - Insufficient Stamp Duty" in Firm practices; a transaction export showing it raises a work item to fund the virtual account before CDSL's end of day.

Pool accounts: record in Firm practices that the firm never votes on securities in its pool accounts. Test **pool_voting**.

## receipts-dd-closure
title: Demand drafts, cheque realisation, BSE complaints, deposits and closure requests
screen: money
roles: Ops maker, Ops checker, Compliance officer, Risk manager
ask: demand draft; pay order; banker's cheque; dd 50000; bank certificate dd; realisation date cheque; settlement date physical instrument; bse complaint due date; bse investor services email; complaints over 30 days net worth; bmc 50% cash; base minimum capital cash; segment deposit 50 lakh; closure request sms whatsapp; closure registered email
related: record-receipt, website-lists

- **Demand drafts and pay orders**: record them as mode **DD / pay order**. From ₹50,000 for a client in a day, enter the name and account the issuing bank certified; the account must be the client's registered one. Like a cheque, a draft is credited only once it is realised (cleared): the day it clears is its settlement date. Test **dd_bank_certificate**.
- **Complaints from an exchange**: on [[compregs|Registers]] → Complaints from exchanges, record the exchange, its due date and the amount claimed. A work item comes 2 days before BSE's date. Test **bse_complaints**; test **complaints_networth** compares claims open over 30 days with half the net worth.
- **Deposits**: mark base minimum capital deposits with purpose **Base minimum capital**; at least half of them must be cash, fixed deposits or bank guarantees (test **bmc_cash**). A self-clearing member in equity derivatives keeps ₹50 lakh (test **segment_deposit**).
- **Closure requests** come signed, online with two-factor login, or from the client's registered e-mail (give the address it came from). Requests by SMS or messaging apps are refused. Test **closure_channel**.
