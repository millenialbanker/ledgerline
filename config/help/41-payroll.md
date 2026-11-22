Ledgy help articles: the Payroll module: salary structures, the monthly run with a second approver, PF, ESI, professional tax and TDS, payslips and returns, paying salary by bank file or HDFC CBX, the books, advances, claims, declarations and full and final.

# Payroll

## payroll-run
title: Run the month’s payroll
screen: payroll
roles: Payroll, HR manager, Admin
ask: run payroll; monthly payroll; salary run; process salary; make payroll; approve payroll; payroll maker checker; submit payroll; return payroll; loss of pay; lop from attendance; arrears; incentive in salary; hold salary; salary register; payslips
related: payroll-statutory, payroll-pay, payroll-books

1. On [[payroll|Payroll]] choose the month and click **Make the run**. Each person with a salary structure gets a line: pay for the days paid, loss of pay from attendance and leave without pay, PF, ESI, professional tax, TDS, recoveries of advances and approved claims.
2. Click **Working** on a line to see how it was worked out, including the tax for the year. **Inputs** sets this month only: loss of pay days (blank takes them from attendance), arrears, an incentive, another deduction, or holding a person’s salary. Saving makes the run again.
3. **Submit for approval**. A second person (Payroll or the HR manager, never the maker) clicks **Approve and post**. They can **Return** it with a reason instead.
4. After approval: **Email payslips** (people also see them in My HR), download the **Salary register**, the **Bank payment file**, the **PF ECR** and the **ESI contributions** file, and see professional tax by state.

People with no salary structure are left out and listed as problems on the run.

## payroll-statutory
title: PF, ESI, professional tax and TDS: how they are worked out
screen: payroll
roles: Payroll, HR manager
ask: pf calculation; epf 12 percent; pf wage ceiling 15000; eps 8.33; edli; pf admin charges; esi 0.75 3.25; esi threshold 21000; professional tax maharashtra; professional tax west bengal; pt slab; tds on salary; old regime new regime; standard deduction; hra exemption; 80c; rebate 87a; cess; statutory rates change; new pt slab
related: payroll-run, payroll-declarations

The rates are in **statutory tables** on [[payroll|Payroll]] (Statutory tables & settings), each effective from a date with its source. A change is a new table from the date it applies; months already approved keep the figures they were paid with.

- **PF**: 12% of PF wages from the employee and 12% from the employer, on wages up to the ceiling (₹15,000) unless the person is set to full wages. The employer’s share splits into EPS (8.33%, on wages up to the ceiling, for EPS members) and EPF; EDLI and admin charges are added for the employer.
- **ESI**: 0.75% from the employee and 3.25% from the employer, for people whose monthly pay is up to ₹21,000 (₹25,000 for a person with disability). Once covered, a person stays covered to the end of the contribution period.
- **Professional tax**: by the state where the person works, from the state’s slabs (West Bengal, Maharashtra and other states in the table; Maharashtra’s February amount differs). Add or change a state’s slabs in a new table.
- **TDS on salary** (section 392 of the Income-tax Act 2025, section 192 before April 2026): the year’s tax on the person’s regime (new by default, old if chosen), with the standard deduction, rebate, surcharge and cess, spread over the months left, less tax already deducted.

## payroll-declarations
title: Tax declarations and proofs
screen: payroll
roles: Payroll, HR manager, Ops maker, Ops checker, Risk manager, Compliance officer, Branch staff
ask: tax declaration; investment declaration; rent receipts; hra declaration; upload proof; verify proofs; landlord pan; previous employer salary; choose tax regime; change regime
related: payroll-statutory

- Each person enters their declaration in [[me|My HR]] (**Tax declaration**): the regime, rent paid, metro city, landlord’s PAN, 80C, 80D, NPS, home loan interest, other income and salary and TDS from an earlier employer this year. TDS uses the declared figures until proofs are verified.
- They upload proofs with **Upload a proof**.
- Payroll checks them on [[payroll|Payroll]] (Tax & returns) and clicks **Verify proofs**, entering the amounts the proofs support. The next run uses them.

## payroll-pay
title: Pay salary: bank file or HDFC CBX
screen: payroll
roles: Payroll, HR manager, Admin
ask: pay salary; salary bank file; salary through hdfc; cbx salary; salary batch; salary client code; mark salary paid; salary payment failed; utr for salary; salary not credited
related: payroll-run, payroll-books

Salary is the firm’s own payment, never a client payout. Two ways:

- **Bank file**: download the **Bank payment file**, upload it in your bank’s portal from the firm’s own account, then click **Mark paid (bank file)** with the date and reference.
- **HDFC CBX**: in [[connections|Connections]], the HDFC Bank CBX connection needs the **CBX client code for the firm’s own payments (salary)**, which must not be the client payout client code. After approval, someone other than the payroll’s maker clicks **Send to HDFC (CBX)**. The batch waits in CBX for the firm’s own CBX verifier and authorisers. Ledgerline asks the bank for results during the day (or **Ask HDFC now**); paid lines are posted, and a rejected payment raises a work item for Payroll and stays in salary payable.

## payroll-books
title: Payroll in the firm’s books
screen: payroll
roles: Payroll, HR manager, Admin
ask: salary journal; payroll posting; salary payable; pf payable; esi payable; professional tax payable; tds on salary payable; salary expense account; employer pf expense; staff advances account
related: payroll-run, payroll-pay

Approving a run posts one balanced entry to the firm’s books: salaries and the employer’s PF and ESI as expenses, against salary payable, PF payable, ESI payable, professional tax payable and TDS on salary payable; recoveries of advances reduce staff advances. Paying the salary moves salary payable to the firm’s bank. Pay the statutory dues from the firm’s bank as usual (see [[books|Firm accounts & GST]]).

## payroll-advances
title: Salary advances, staff loans and reimbursement claims
screen: payroll
roles: Payroll, HR manager
ask: salary advance; staff loan; loan recovery from salary; advance instalment; reimbursement claim; expense claim; travel reimbursement; approve claim; claim with bill
related: payroll-run

- **Advances and loans**: Payroll records the amount, monthly instalment and first recovery month on [[payroll|Payroll]] (Advances & claims). A second person approves it with the day it was paid, which posts it from the firm’s bank to staff advances. Each run recovers the instalment until it is repaid.
- **Claims**: people claim in My HR with the bill. Payroll or HR approves or refuses; approved claims are paid with the next run.

## payroll-returns
title: PF ECR, ESI file, Form 138 (24Q) and Form 130 (Form 16)
screen: payroll
roles: Payroll, HR manager
ask: pf ecr file; ecr upload epfo; esi contribution file; esic monthly file; form 24q; form 138; tds return salary; form 16; form 130; form 16 part b; annexure ii; professional tax return figures
related: payroll-run, payroll-statutory

From an approved run on [[payroll|Payroll]]:

- **PF ECR**: the ECR 2.0 text file for the EPFO portal, one line a member with eleven fields separated by #~# (UAN, name, gross, EPF, EPS and EDLI wages, the three contributions, days not paid, refund of advances). Amounts are whole rupees; days not paid are whole days, because EPFO doesn't take half days.
- **ESI contributions**: the rows for ESIC's monthly contribution template (IP number, name in capitals and spaces, days paid rounded up, wages, reason code, last working day). Copy them into ESIC's own Excel 97-2003 template, with every column as text, and upload that. Someone who leaves part-way through a month is shown with their wages that month, and the next month with 0 days, reason 2 (left service) and their last working day, so ESIC takes them off the firm's list.
- **Professional tax**: the amounts by state and slab on the run.

On **Tax & returns**: **Form 138** (the quarterly TDS statement for salary that replaced Form 24Q) for each quarter, with **Annexure II** for the year in Q4, and **Form 130 Part B** (which replaced Form 16 Part B) for each person. People download their own Form 130 in My HR.

## payroll-fnf
title: Full and final settlement
screen: payroll
roles: Payroll, HR manager
ask: full and final; fnf; final settlement; leave encashment on leaving; gratuity; notice pay recovery; settlement of leaver
related: leaving-employee, payroll-run

For a leaver, click **Full and final** on [[hrlife|Joiners & leavers]]. Enter any notice pay to recover. Ledgerline works out leave encashment, gratuity (for five years’ service or more, within the limit in the statutory table), the notice pay recovered and loans still outstanding, and adds them to the leaver’s last month’s run.
