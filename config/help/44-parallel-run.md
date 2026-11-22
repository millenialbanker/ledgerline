Ledgy help articles: the parallel run before go-live (Files → Parallel run): comparing the old back office's files with Ledgerline's, day by day, until ten business days in a row match.

# Parallel run

## parallel-run
title: Parallel run: compare the old system's files with Ledgerline's
screen: parallel
roles: Ops maker, Ops checker, Compliance officer, Admin, Risk manager
ask: parallel run; compare old system file; compare files; old back office vs ledgerline; go live condition; ten matching days; matching days in a row; streak; file differs from old system; compare margin file; compare holding statement; reconcile with previous software; why do the files differ
related: self-inspection, migration-tool

Before going live, the firm runs Ledgerline beside its old back office and compares the files both make for the same day. [[parallel|Parallel run]] (Files) does the comparing.

1. Press **Compare two files**. Choose the day, the kind of file (or let Ledgerline recognise it from the name), the old system's file and Ledgerline's file of the same kind.
2. Ledgerline matches the rows on their identifying columns: client code, demat account, account type, ISIN, segment, exchange, settlement, symbol, series, expiry, strike, option type, trade and order numbers. Record numbers and fillers are ignored.
3. Numbers are compared as numbers (1,000.50 is the same as 1000.5); text is compared without case or extra spaces. A JSON file is read from its first list of records.
4. The result shows the rows only in the old file, the rows only in Ledgerline's, and the rows that differ with the column. Each column has its count of differences and, for number columns, the total in each file.

On the screen, client codes, demat accounts and text are masked; numbers are in full. Neither file is kept: only the names, row counts, fingerprints (SHA-256) and up to 300 difference lines.

- **A day matches** when the latest comparison of every kind of file compared that day matches. Compare again after a fix: the latest comparison counts.
- **Matching days in a row**: counted back over business days from the latest day compared. A business day with no comparison, or one that differs, ends the run. The pilot goes live after **10** in a row.
- A comparison that differs raises a work item for the **Ops checker** (Autopilot & work queue). A later comparison of the same file and day that matches closes it.

Who: Ops makers, Ops checkers and the compliance officer compare; risk managers can look.
