package ops

import (
	"context"
	"crypto/hmac"
	"fmt"
	"strings"
	"time"

	"ledgerline/internal/docs"
)

// ---- locked sign-ins, out-of-band unlock, password age and history ----
//
// CSCRF (SEBI/HO/ITD-1/ITD_CSC_EXT/P/CIR/2024/113) Annexure-G 5-7 asks for
// accounts locked after failed logins with an out-of-band unlock, and
// CAPTCHA or rate limiting against brute force; Part II PR.AA 1f asks for
// the lock itself. NSE/INSP/73850 (circular 20/2026) Annex D 5-6 asks for
// lock after 5 failed attempts, passwords that expire after 90 days and a
// history of 6.
//
// So five wrong passwords or codes lock a sign-in until it is unlocked
// out of band: by an Admin (or, for a client login, an Ops checker) who
// confirmed it is the person by phone or in person and says so, or by the
// person with a one-time code sent to the e-mail the firm holds for them
// (the employee record for staff, the client record for a client). A
// firm can choose the older 15-minute timed lock instead (Security
// settings); the choice is a security event. Rate limiting already stands
// in front of the sign-in (30 tries per address in 15 minutes, api/auth.go),
// so there is no CAPTCHA.
//
// A password older than the firm's expiry (90 days by default) must be
// changed at the next sign-in, and a new password may not be any of the
// last 6 (the current one included).

const (
	cUnlock = "unlockcode"

	// DefaultPasswordDays and DefaultPasswordHistory follow NSE/INSP/73850
	// Annex D 5-6.
	DefaultPasswordDays    = 90
	DefaultPasswordHistory = 6
)

// unlockCode is a pending self-service unlock.
type unlockCode struct {
	User    string `json:"user"`
	Hash    string `json:"hash"`
	Expires string `json:"expires"`
	Tries   int    `json:"tries"`
	SentTo  string `json:"sent_to"`
}

// passwordDays is how long a password stays valid (0: no expiry).
func (s SecuritySettings) passwordDays() int {
	switch {
	case s.NoPasswordExpiry:
		return 0
	case s.PasswordDays > 0:
		return s.PasswordDays
	}
	return DefaultPasswordDays
}

func (s SecuritySettings) passwordHistory() int {
	if s.PasswordHistory > 0 {
		return s.PasswordHistory
	}
	return DefaultPasswordHistory
}

// lockState reports whether a credential is locked now and, for a timed
// lock, until when.
func (c Credential) lockState(now time.Time) (locked bool, until time.Time) {
	if c.Locked {
		return true, time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, c.LockedUntil); err == nil && now.Before(t) {
		return true, t
	}
	return false, time.Time{}
}

// failSignIn counts a wrong password or code and locks the sign-in at the
// limit: until unlocked out of band, or for 15 minutes when the firm chose
// the timed lock.
func (o *Ops) failSignIn(ctx context.Context, tenant, userID string, now time.Time) bool {
	timed := o.SecuritySettings(ctx, tenant).TimedUnlock
	locked := false
	upd(ctx, o.Docs, tenant, cCred, userID, userID, nil, func(x *Credential) error {
		x.Failed++
		if x.Failed >= maxFailures {
			x.Failed, locked = 0, true
			if timed {
				x.LockedUntil = now.Add(lockFor).Format(time.RFC3339)
			} else {
				x.Locked, x.LockedAt = true, now.Format(time.RFC3339)
			}
		}
		return nil
	})
	if locked {
		o.log(ctx, tenant, userID, "auth.locked", userID, map[string]any{"timed": timed})
	}
	return locked
}

// lockedError says how to get back in.
func lockedError(until time.Time) error {
	if !until.IsZero() {
		return fmt.Errorf("%w: too many wrong attempts; try again after %s", ErrForbidden, until.In(ist).Format("15:04"))
	}
	return fmt.Errorf("%w: this sign-in is locked after too many wrong attempts. Ask for an unlock code by e-mail on the sign-in screen, or ask your administrator (clients: call the firm) to unlock it", ErrForbidden)
}

// UnlockUser unlocks a locked sign-in after the person was verified out of
// band (a call to their registered mobile, in person): how says how. An
// Admin for anyone; an Ops checker for a client portal login. The wrong
// attempts count starts again.
func (o *Ops) UnlockUser(ctx context.Context, tenant, actor, user, how string) error {
	if err := notSupport(actor, "Unlocking sign-ins"); err != nil {
		return err
	}
	user = strings.ToLower(strings.TrimSpace(user))
	u, err := o.user(ctx, tenant, user)
	if err != nil {
		return err
	}
	roles := []string{RoleAdmin}
	if u.Role == RoleClient {
		roles = append(roles, RoleChecker)
	}
	if err := o.require(ctx, tenant, actor, "Unlocking sign-ins", roles...); err != nil {
		return err
	}
	if actor == user {
		return fmt.Errorf("%w: someone else unlocks your sign-in", ErrForbidden)
	}
	how = strings.TrimSpace(how)
	if len(how) < 5 {
		return invalid("say how you confirmed it is the person (for example: called the registered mobile)")
	}
	_, err = upd(ctx, o.Docs, tenant, cCred, user, actor, nil, func(c *Credential) error {
		if c.User == "" {
			return ErrNotFound
		}
		if l, _ := c.lockState(o.now()); !l {
			return fmt.Errorf("%w: %s is not locked", ErrState, user)
		}
		c.Locked, c.LockedAt, c.LockedUntil, c.Failed = false, "", "", 0
		return nil
	})
	if err != nil {
		return err
	}
	o.log(ctx, tenant, actor, "user.unlocked", user, map[string]any{"how": trim(how, 200), "by": "staff"})
	return nil
}

// unlockEmail is the e-mail the firm holds for a sign-in: the client
// record for a client login, the employee record for staff.
func (o *Ops) unlockEmail(ctx context.Context, tenant string, u User) string {
	if u.Role == RoleClient {
		if len(u.Clients) > 0 {
			if c, err := o.Client(ctx, tenant, u.Clients[0]); err == nil {
				return c.Email
			}
		}
		return ""
	}
	if e, ok := o.employeeOf(ctx, tenant, u.ID); ok {
		return firstOf(e.Email, e.PersonalEmail)
	}
	return ""
}

// RequestUnlockCode sends a one-time unlock code to the e-mail the firm
// holds for a locked sign-in. It answers the same whether or not the user
// exists or is locked, so it cannot be used to find user IDs.
func (o *Ops) RequestUnlockCode(ctx context.Context, tenant, user, ip string) string {
	const answer = "If this sign-in is locked and the firm has your e-mail address, a code is on its way. It is valid for 15 minutes."
	user = strings.ToLower(strings.TrimSpace(user))
	u, err := o.user(ctx, tenant, user)
	if err != nil || !u.Active {
		return answer
	}
	c, _, err := o.cred(ctx, tenant, user)
	if l, _ := c.lockState(o.now()); err != nil || !l {
		return answer
	}
	to := o.unlockEmail(ctx, tenant, u)
	if !reEmail.MatchString(to) {
		o.log(ctx, tenant, user, "auth.unlock_code", user, map[string]any{"sent": false, "why": "no e-mail on record", "ip": ip})
		return answer
	}
	code := otpCode()
	_ = docs.Upsert(ctx, o.Docs, tenant, cUnlock, user, unlockCode{User: user, Hash: hashOTP(user, code), Expires: o.now().Add(15 * time.Minute).Format(time.RFC3339), SentTo: maskEmail(to)}, user)
	set, _ := o.Settings(ctx, tenant)
	body := fmt.Sprintf("Your Ledgerline sign-in %s was locked after too many wrong attempts.\n\nYour unlock code is %s. It is valid for 15 minutes. Enter it on the sign-in screen with your user ID.\n\nIf you did not try to sign in, someone may be guessing your password: do not use the code, and tell %s.\n\n%s",
		user, code, firstOf(set.ComplianceEmail, "the firm"), firstOf(set.Firm, "Ledgerline"))
	if u.Role == RoleClient {
		ucc := ""
		if len(u.Clients) > 0 {
			ucc = u.Clients[0]
		}
		o.sendDirect(ctx, tenant, user, ChEmail, to, ucc, "auth.unlock_code", "Your sign-in unlock code", body)
	} else {
		o.mailStaff(ctx, tenant, to, "Your Ledgerline sign-in unlock code", body, user, nil)
	}
	o.log(ctx, tenant, user, "auth.unlock_code", user, map[string]any{"sent": true, "to": maskEmail(to), "ip": ip})
	if o.DevCodes {
		return answer + " (Development server: the code is " + code + ".)"
	}
	return answer
}

// UnlockWithCode unlocks a sign-in with the code sent to its e-mail.
func (o *Ops) UnlockWithCode(ctx context.Context, tenant, user, code, ip string) error {
	user = strings.ToLower(strings.TrimSpace(user))
	code = strings.TrimSpace(code)
	bad := invalid("that code is wrong or has expired; ask for a new one")
	x, _, err := docs.Get[unlockCode](ctx, o.Docs, tenant, cUnlock, user)
	if err != nil || x.Hash == "" {
		return bad
	}
	if exp, _ := time.Parse(time.RFC3339, x.Expires); o.now().After(exp) || x.Tries >= 3 {
		_ = docs.Upsert(ctx, o.Docs, tenant, cUnlock, user, unlockCode{User: user}, user)
		return bad
	}
	if !hmac.Equal([]byte(hashOTP(user, code)), []byte(x.Hash)) {
		x.Tries++
		_ = docs.Upsert(ctx, o.Docs, tenant, cUnlock, user, x, user)
		o.log(ctx, tenant, user, "auth.unlock_failed", user, map[string]any{"ip": ip})
		return bad
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cUnlock, user, unlockCode{User: user}, user)
	if _, err := upd(ctx, o.Docs, tenant, cCred, user, user, nil, func(c *Credential) error {
		c.Locked, c.LockedAt, c.LockedUntil, c.Failed = false, "", "", 0
		return nil
	}); err != nil {
		return err
	}
	o.log(ctx, tenant, user, "user.unlocked", user, map[string]any{"how": "code sent to " + x.SentTo, "by": "self", "ip": ip})
	return nil
}

// passwordExpired: the password is older than the firm's limit.
func (o *Ops) passwordExpired(ctx context.Context, tenant string, c Credential) bool {
	days := o.SecuritySettings(ctx, tenant).passwordDays()
	if days <= 0 || c.ChangedAt == "" || c.MustChange {
		return false
	}
	t, err := time.Parse(time.RFC3339, c.ChangedAt)
	return err == nil && o.now().Sub(t) > time.Duration(days)*24*time.Hour
}

// reusedPassword: the new password is the current one or one of the
// last few.
func (o *Ops) reusedPassword(ctx context.Context, tenant string, c Credential, pw string) bool {
	n := o.SecuritySettings(ctx, tenant).passwordHistory()
	for i, h := range append([]string{c.Hash}, c.History...) {
		if i >= n {
			break
		}
		if checkPassword(h, pw) {
			return true
		}
	}
	return false
}
