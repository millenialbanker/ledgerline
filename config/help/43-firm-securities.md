Ledgy help articles: Accounts → Firm securities, the firm's own investments and stock-in-trade: the practice questions, the register from the PRO trade files, the stock journal, valuation, the reports, pledges, dividends, the demat check, net worth and opening lots; and the clients' capital gains statement.

# Firm securities

## firm-securities
title: The firm's own securities (investments and stock-in-trade)
screen: firmsec
roles: Ops maker, Ops checker, Risk manager, Compliance officer, Admin
ask: firm securities; own securities; proprietary holdings; pro trades; firm's own shares; stock in trade; investments of the firm; own investments; broker's own holdings; trading book; stock register; securities register; own demat holdings; pro account holdings
related: firm-securities-practices, firm-securities-register, stock-journal, firm-securities-valuation, firm-securities-reports

Open [[firmsec|Firm securities]] (Accounts). It keeps the firm's own securities in one or both portfolios:

- **Investments**: lots held at cost. A sale takes the oldest lots first. The profit goes to the books and to the capital gains register.
- **Stock-in-trade**: a purchase and sale book. The closing stock is valued at each month or year end.

The tabs:

1. **Holdings**: each portfolio, then each account (one per portfolio and kind, such as "Stock-in-trade – listed equity shares"), then each scrip, then each lot with its date and cost. Click a scrip to see its lots.
2. **Purchases & sales**: every line of the register, from the trade files and entered by hand.
3. **Stock journal**: moves between the two portfolios.
4. **Valuation**: the month-end or year-end value with the entry that brings the books to it.
5. **Reports**: the stock-in-trade reports, the capital gains register and the pledges report.
6. **Reconciliation**: the register against the firm's demat accounts, dividends received, and the net worth.

How the firm keeps its securities is set in [[practices|Firm practices]] under **Firm securities**. Until the firm says where it keeps them, nothing is posted.

## firm-securities-practices
title: Set how the firm keeps its own securities
screen: practices
roles: Admin, Compliance officer
ask: firm securities settings; set up own securities; where do we keep our own securities; investments or stock in trade setting; pro code portfolio; own demat account setting; valuation setting; closing stock setting; cost of investment setting; ind as or as; firm securities questions
related: firm-securities, firm-practice-approval

Open [[practices|Firm practices]] and go to the **Firm securities** section. Answer each question the way your firm works:

- Where the firm keeps its own securities: stock-in-trade, investments, both, or none. Answer this first: the rest applies only when the firm keeps a portfolio.
- The demat account of each portfolio (the same DP, another DP or the other depository), or one demat account for both.
- The trading codes whose trades are stock-in-trade (for example PRO) and those that are investments, and whether a trade's portfolio can be changed until the day closes.
- Whether holdings may move between the portfolios (by a stock journal with a second person's approval).
- The instruments covered. Add any other type the firm holds: the Admin is then asked how it is treated.
- What goes into the cost of an investment lot, the accounting standards (AS or Ind AS), long-term or current, and how investments are valued.
- Dividends booked automatically with TDS as receivable, intraday kept apart, how closing stock is valued, and whether charges are expenses or part of cost.
- The demat check, pledges, the proprietary trading disclosure for new clients, net worth, who approves entries, and how opening holdings are brought in.

Questions marked **Needs a second approver** change how the books are kept. The compliance officer or another Admin approves them before they apply.

If the firm follows Ind AS and values a trading book at cost, Ledgerline warns: under Ind AS a trading book is measured at fair value through profit or loss.

## firm-securities-register
title: Own trades from the trade files, and manual entries
screen: firmsec
roles: Ops maker, Ops checker
ask: pro trades into books; bring in own trades; proprietary trades register; change portfolio of a trade; own trade wrong portfolio; manual purchase of shares by firm; off market purchase firm; buy back entry firm; enter firm investment; approve firm securities entry; reject firm securities entry
related: firm-securities, stock-journal

**From the trade files.** After billing, Ledgerline brings in the day's trades under the firm's own codes. It makes one line per code, exchange, scrip and side, with the bill's charges spread by value. It does this again before the day closes. To do it now, click **Bring in own trades** on [[firmsec|Firm securities]] → Purchases & sales.

1. A line goes to the code's default portfolio.
2. Until the day is closed, click **Change** on the line to move it to the other portfolio, or change its kind. After the close, move it with a stock journal.
3. A scrip bought and sold the same day is intraday (speculative). It is kept apart from delivery.

**By hand.** Click **New entry** for a purchase or sale outside the exchange: an off-market transfer, a buy-back accepted, or a manual correction. Enter the date, portfolio, ISIN, quantity, price and charges.

- Manual and off-market entries wait for a second person (the firm's practice). Another person clicks **Approve** or **Reject**. Lines from the trade files post at once.
- Enter a scrip's lines in date order.
- A sale of pledged units goes through with a warning: pledges are tracked, not blocked.

## stock-journal
title: Move a holding between investments and stock-in-trade (stock journal)
screen: firmsec
roles: Ops maker, Ops checker
ask: stock journal; move shares from investment to stock in trade; convert investment to stock in trade; convert stock in trade to investment; transfer between portfolios; section 45(2) conversion; section 28(via); reclassify holding
related: firm-securities-register, firm-securities-capital-gains

On [[firmsec|Firm securities]] → Stock journal, click **New stock journal**.

1. Choose the date, the portfolio the holding leaves, the one it goes to, the ISIN, the quantity and the reason.
2. Ledgerline takes that day's closing price as the market value. Give a price only where there is none (unlisted shares).
3. The journal shows both sides: out at cost (the oldest lots first), in at market value, and the difference.
4. A second person approves it. It then posts one entry: in at market value, out at cost, and the difference as gain or loss on transfer.

The tax date and value are kept on the new lot. Investments converted into stock-in-trade fall under section 45(2) of the Income-tax Act, 1961: the capital gain to the market value is taxed when the stock is sold, and it shows in the capital gains register then. Stock-in-trade converted into investments falls under section 28(via): the market value is the cost for capital gains later. From 1 April 2026 the Income-tax Act, 2025 renumbers these sections.

## firm-securities-valuation
title: Value the firm's securities at month end or year end
screen: firmsec
roles: Ops maker, Ops checker
ask: closing stock valuation; value stock in trade; lower of cost and market; icds viii; category wise valuation; scrip wise valuation; provision for diminution investments; year end valuation of own shares; closing stock entry
related: firm-securities-reports

On [[firmsec|Firm securities]] → Valuation, choose the date and the portfolio.

- **Stock-in-trade** shows three values: at cost, the lower of cost and market value scrip by scrip, and the lower of cost and market value category by category (ICDS VIII: shares, debt securities, convertible securities, any other). Unlisted securities, and those without a price, stay at cost. The firm's practice chooses the one the books take.
- **Investments** are valued at cost, or at the lower of cost and closing price (current investments only, or all). The difference goes to a provision for diminution; the lots stay at cost.

Click **Propose the entry**. Another person checks it and clicks **Post**. Ledgerline works the entry out again when it is posted.

## firm-securities-reports
title: Stock-in-trade reports and the capital gains register
screen: firmsec
roles: Ops maker, Ops checker, Risk manager, Compliance officer
ask: scrip wise stock summary; stock summary with profit; purchase and sale register; speculative and non speculative profit; turnover for tax audit; closing stock statement; firm capital gains register; own pledges report; cost heads separately; brokerage stamp duty columns
related: firm-securities-valuation, firm-securities-capital-gains

On [[firmsec|Firm securities]] → Reports, choose a report, the portfolio and the period, then **View**. Download any report as CSV or print it.

- **Daily purchase and sale register**: every line with its charges.
- **Scrip-wise stock summary**: opening, bought, sold and closing, each with quantity and amount in separate columns, and the profit or loss on what was sold.
- **Speculative and non-speculative profit and loss**, with the charges split between them.
- **Turnover for the tax audit**: speculative turnover is the sum of the differences, each taken as positive; delivery turnover is the sale value.
- **Closing stock statement**: the three values and the one the books take.
- **Capital gains register** for investments.
- **Pledges** on a date.

Tick **Show each cost head** to see price, brokerage, exchange and SEBI charges, stamp duty, GST and other charges as separate columns.

## firm-securities-capital-gains
title: Capital gains: the firm's register and the clients' statement
screen: statements
roles: Ops maker, Ops checker, Compliance officer
ask: capital gains statement; grandfathering 31 january 2018; schedule 112a; ltcg 12.5 percent; stcg 20 percent; 1.25 lakh exemption; capital gains mutual funds; capital gains bonds; fair market value 2018; tax p&l for client; section 112a scrip wise
related: firm-securities-reports, stock-journal

The clients' **Profit & loss / capital gains** report on [[statements|Client reports]] and the firm's capital gains register work sales out the same way:

- **Short-term** listed equity and equity fund units (STT paid): 20% for sales from 23 July 2024 (15% before).
- **Long-term** on them: 12.5% on the year's gains above ₹1.25 lakh (10% above ₹1 lakh before 23 July 2024).
- **Other long-term** gains (bonds, other fund units): 12.5% without indexation. Debt fund units bought from 1 April 2023 are always short-term.
- **Grandfathering**: shares and equity fund units bought before 1 February 2018 and sold long-term take as cost the higher of their cost and the lower of their value on 31 January 2018 and the sale value. Load the values with **FMV-2018** on Operations → Files → Other uploads.

The clients' statement includes mutual fund units bought and redeemed through the firm, and a **Schedule 112A** section laid out as the return asks, scrip by scrip. The year-end pack has the same figures, the rates and Schedule 112A. Clients see it in the portal and the app.

From 1 April 2026 the Income-tax Act, 2025 applies: sections 111A, 112 and 112A are sections 196, 197 and 198.

## firm-securities-recon
title: Check the firm's demat accounts, dividends and pledges
screen: firmsec
roles: Ops maker, Ops checker, Risk manager
ask: own demat reconciliation; firm demat check; securities in transit; register vs cdsl; dividend received firm; tds on dividend receivable; match dividend to bank; pledge own shares to bank; pledged to nbfc; release pledge; invoke pledge
related: firm-securities, firm-securities-net-worth

On [[firmsec|Firm securities]] → Reconciliation:

1. **Demat check.** Ledgerline compares the register with each of the firm's demat accounts. A purchase counts in the demat from its pay-out date; a sale leaves on its pay-in date. Until then the units are **in transit**, not a difference. Enter the demat statement with **Enter demat holdings**, or Ledgerline uses the DP holdings it has for the firm's own demat accounts. A difference raises a work item.
2. **Dividends.** With the practice on, dividends are booked on the record date for the units held before the ex-date, with TDS as receivable once the year's dividends from the company pass the threshold. Paste the credits from the firm's bank statement and click **Match**: each credit with the same amount near the pay date clears the receivable.
3. **Pledges.** Record a pledge to the clearing corporation, a bank or an NBFC with **New pledge**. **Release** it when it ends, or record that it was **invoked** (an off-market sale is entered for approval).

## firm-securities-net-worth
title: Net worth with the firm's securities
screen: firmsec
roles: Ops checker, Compliance officer
ask: net worth computation; net worth certificate; schedule vi net worth; capital adequacy stock broker; net worth for exchange; haircut 30 percent securities; non allowable assets
related: firm-securities-recon

On [[firmsec|Firm securities]] → Reconciliation, **Net worth** works out the net worth on a date the way Schedule VI of the SEBI (Stock Brokers) Regulations describes it:

- Capital and free reserves from the books,
- less the non-allowable assets: fixed assets, pledged securities, the membership card, unlisted securities, bad deliveries, debts over three months, prepaid expenses, intangible assets and 30% of the marketable securities.

The firm's securities are taken at cost. Enter the figures the books do not hold (the card, bad deliveries, the required net worth) with **Figures not in the books**. The layout is provisional until it is matched to the firm's auditor's certificate.

## firm-securities-opening
title: Bring the firm's current holdings into Ledgerline
screen: firmsec
roles: Ops maker, Ops checker
ask: opening lots firm; firm lots upload; firm-lots file; migrate own holdings; opening stock of shares; own holdings from old back office; post opening lots
related: firm-securities

1. Load **FIRM-LOTS** on Operations → Files → Other uploads, as at the day before go-live: one row per lot with the portfolio (INV or SIT), ISIN, scrip, quantity, acquisition date and cost. Optional: the kind, long-term or current, and the value on 31 January 2018.
2. The lots count in the holdings at once. They show as **Opening lots to post** on [[firmsec|Firm securities]] → Holdings.
3. Another person checks them and clicks **Post opening lots**. Ledgerline posts them to the securities accounts against the opening balances account.

Loading again replaces lots not yet posted.
