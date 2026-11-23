package ops

import "fmt"

// ---- self-inspection test of the books' back-dated entry lock ----
//
// From the compliance gap report of November 2026 (workstream J, row
// NSE-N0015): NSE's testing criteria for back-office software (Master
// Circular for Membership Compliance NSE/COMP/73981, Exhibit 33 Annexure
// VII) ask for a system lock of back-dated entries beyond T-2. Ledgerline
// has a manual "Books locked up to" date and an automatic lock a set number
// of business days behind today (Settings.AutoLockDays): only the automatic
// one is a system lock. Nothing may be posted on or before the locked date,
// so AutoLockDays 3 locks every day before T-2 and leaves T-2 open; a
// smaller number is stricter and also meets the rule.

// siLockMaxDays is the largest automatic lock that still refuses entries
// older than T-2.
const siLockMaxDays = 3

func init() {
	siTests = append(siTests, SITest{ID: "backdated_lock", Title: "Back-dated entries beyond T-2 locked by the system", Area: "Operations", Severity: "medium", Owner: RoleAdmin,
		Source:    "NSE/COMP/73981 (Master Circular for Membership Compliance Department), Exhibit 33 Annexure VII: system lock of back-dated entries beyond T-2",
		ToConfirm: "The criterion is read from the gap register's summary of Annexure VII; check its exact words against the master circular.",
		Checks:    "Settings → Period locking: the automatic lock is on and keeps the books locked no more than 3 business days behind today, so an entry dated before T-2 is refused. A manual 'Books locked up to' date alone does not count.",
		Fix:       "An Admin sets 'Also lock automatically' to 3 (or fewer) business days in Settings → Period locking.",
		Screen:    "settings", ScreenName: "Settings → Period locking", Real: true, run: siBackdatedLock})
}

func siBackdatedLock(e *siEnv) (siOut, error) {
	var s siOut
	s.input("Settings → Period locking", e.date)
	s.checked = 1
	n := e.set.AutoLockDays
	locked := e.set.lockedUpTo(e.date)
	switch {
	case n <= 0:
		s.bad(fmt.Sprintf("The automatic lock is off; the books are locked only up to %s by hand.", firstOf(fmtDay(e.set.LockedUpTo), "no date")))
		s.summary = "No system lock of back-dated entries: entries older than T-2 can still be posted."
	case n > siLockMaxDays:
		s.bad(fmt.Sprintf("The automatic lock is %d business days behind today (locked up to %s): entries dated from T-%d to T-3 can still be posted.", n, fmtDay(locked), n-1))
		s.summary = fmt.Sprintf("The system lock is %d business days behind today, more than the %d that keeps entries older than T-2 out.", n, siLockMaxDays)
	default:
		s.summary = fmt.Sprintf("The books lock themselves %d business days behind today (locked up to %s): entries older than T-2 are refused.", n, fmtDay(locked))
	}
	return s, nil
}
