package control

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"ledgerline/internal/docs"
)

const pwIter = 600000

func hashPassword(pw string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	k, err := pbkdf2.Key(sha256.New, pw, salt, pwIter, 32)
	if err != nil {
		return "", err
	}
	b := base64.RawStdEncoding
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", pwIter, b.EncodeToString(salt), b.EncodeToString(k)), nil
}

func checkPassword(hash, pw string) bool {
	p := strings.Split(hash, "$")
	if len(p) != 4 || p[0] != "pbkdf2-sha256" {
		return false
	}
	var iter int
	if _, err := fmt.Sscan(p[1], &iter); err != nil || iter < 1 {
		return false
	}
	b := base64.RawStdEncoding
	salt, e1 := b.DecodeString(p[2])
	want, e2 := b.DecodeString(p[3])
	if e1 != nil || e2 != nil {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, pw, salt, iter, len(want))
	return err == nil && subtle.ConstantTimeCompare(got, want) == 1
}

// TempPassword is a one-time password for a new login.
func TempPassword() string {
	const abc = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 14)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = abc[int(b[i])%len(abc)]
	}
	return string(b)
}

func checkNewPassword(pw string) error {
	if len(pw) < 12 {
		return invalid("use at least 12 characters")
	}
	if len(pw) > 200 {
		return invalid("that password is too long")
	}
	return nil
}

func (c *Control) staff(ctx context.Context, id string) (Staff, error) {
	s, _, err := docs.Get[Staff](ctx, c.Docs, T, cStaff, strings.ToLower(id))
	if err != nil {
		return s, ErrNotFound
	}
	return s, nil
}

// SystemStaff is Control itself, for its own scheduled work.
const SystemStaff = "system:control"

// need checks the person is active and has one of the roles (Owner always does).
func (c *Control) need(ctx context.Context, who string, roles ...string) error {
	if who == SystemStaff {
		return nil
	}
	s, err := c.staff(ctx, who)
	if err != nil || !s.Active {
		return ErrForbidden
	}
	if s.Role == RoleOwner || len(roles) == 0 || has(roles, s.Role) {
		return nil
	}
	return fmt.Errorf("%w: this needs the %s role", ErrForbidden, strings.Join(roles, " or "))
}

// Bootstrap makes the first Owner when there is nobody yet, returning the
// one-time password (empty when staff already exist).
func (c *Control) Bootstrap(ctx context.Context, id, name, pw string) (string, error) {
	all, err := docs.List[Staff](ctx, c.Docs, T, cStaff, "")
	if err != nil || len(all) > 0 {
		return "", err
	}
	id = strings.ToLower(firstOf(strings.TrimSpace(id), "owner"))
	must := pw == ""
	if must {
		pw = TempPassword()
	}
	h, err := hashPassword(pw)
	if err != nil {
		return "", err
	}
	s := Staff{ID: id, Name: firstOf(name, "Owner"), Role: RoleOwner, Hash: h, Active: true, MustReset: must, Created: c.stamp()}
	if err := docs.Upsert(ctx, c.Docs, T, cStaff, id, s, "system"); err != nil {
		return "", err
	}
	return pw, nil
}

var (
	failMu sync.Mutex
	fails  = map[string][]time.Time{}
)

func tooMany(key string, now time.Time) bool {
	failMu.Lock()
	defer failMu.Unlock()
	keep := fails[key][:0]
	for _, t := range fails[key] {
		if now.Sub(t) < 15*time.Minute {
			keep = append(keep, t)
		}
	}
	fails[key] = keep
	return len(keep) >= 8
}

func failed(key string, now time.Time) {
	failMu.Lock()
	fails[key] = append(fails[key], now)
	failMu.Unlock()
}

// Login checks a password and starts a session, returning the cookie value.
func (c *Control) Login(ctx context.Context, id, pw, from string) (string, Session, Staff, error) {
	id = strings.ToLower(strings.TrimSpace(id))
	now := c.now()
	if tooMany(id, now) || tooMany("ip:"+from, now) {
		return "", Session{}, Staff{}, fmt.Errorf("%w: too many attempts; wait 15 minutes", ErrForbidden)
	}
	s, err := c.staff(ctx, id)
	if err != nil || !s.Active || !checkPassword(s.Hash, pw) {
		failed(id, now)
		failed("ip:"+from, now)
		return "", Session{}, Staff{}, fmt.Errorf("%w: wrong user name or password", ErrForbidden)
	}
	tok := randHex(32)
	ss := Session{ID: sha(tok), Staff: s.ID, CSRF: randHex(16), Created: c.stamp(), LastSeen: c.stamp()}
	if err := docs.Upsert(ctx, c.Docs, T, cSession, ss.ID, ss, s.ID); err != nil {
		return "", ss, s, err
	}
	_, _ = docs.Update(ctx, c.Docs, T, cStaff, s.ID, s.ID, func(x *Staff) error { x.LastLogin = c.stamp(); return nil })
	c.log(ctx, s.ID, "auth.login", s.ID, from)
	return tok, ss, s, nil
}

// SessionFor finds the session for a cookie.
func (c *Control) SessionFor(ctx context.Context, tok string) (Session, Staff, error) {
	if tok == "" {
		return Session{}, Staff{}, ErrForbidden
	}
	ss, _, err := docs.Get[Session](ctx, c.Docs, T, cSession, sha(tok))
	if err != nil || ss.Ended {
		return ss, Staff{}, ErrForbidden
	}
	last, _ := time.Parse(time.RFC3339, ss.LastSeen)
	if c.now().Sub(last) > 12*time.Hour {
		return ss, Staff{}, ErrForbidden
	}
	s, err := c.staff(ctx, ss.Staff)
	if err != nil || !s.Active {
		return ss, s, ErrForbidden
	}
	if c.now().Sub(last) > time.Minute {
		ss, _ = docs.Update(ctx, c.Docs, T, cSession, ss.ID, s.ID, func(x *Session) error { x.LastSeen = c.stamp(); return nil })
	}
	return ss, s, nil
}

// Logout ends a session.
func (c *Control) Logout(ctx context.Context, tok string) {
	_, _ = docs.Update(ctx, c.Docs, T, cSession, sha(tok), "system", func(x *Session) error { x.Ended = true; return nil })
}

// ChangePassword sets a new password.
func (c *Control) ChangePassword(ctx context.Context, who, old, pw string) error {
	s, err := c.staff(ctx, who)
	if err != nil {
		return err
	}
	if !checkPassword(s.Hash, old) {
		return invalid("the current password is wrong")
	}
	if err := checkNewPassword(pw); err != nil {
		return err
	}
	if pw == old {
		return invalid("choose a different password")
	}
	h, err := hashPassword(pw)
	if err != nil {
		return err
	}
	_, err = docs.Update(ctx, c.Docs, T, cStaff, s.ID, s.ID, func(x *Staff) error { x.Hash, x.MustReset = h, false; return nil })
	c.log(ctx, who, "auth.password", who, "")
	return err
}

// StaffList lists Control's logins.
func (c *Control) StaffList(ctx context.Context, by string) ([]Staff, error) {
	if err := c.need(ctx, by); err != nil {
		return nil, err
	}
	all, err := docs.List[Staff](ctx, c.Docs, T, cStaff, "")
	for i := range all {
		all[i].Hash = ""
	}
	return all, err
}

// SaveStaff adds a login (returning its one-time password) or changes one.
func (c *Control) SaveStaff(ctx context.Context, by, id, name, role string, active, reset bool) (Staff, string, error) {
	if err := c.need(ctx, by, RoleOwner); err != nil {
		return Staff{}, "", err
	}
	id = strings.ToLower(strings.TrimSpace(id))
	if id == "" || len(id) > 60 || strings.ContainsAny(id, " /\\:") {
		return Staff{}, "", invalid("give a user name without spaces")
	}
	if !has(Roles, role) {
		return Staff{}, "", invalid("role must be Owner, Billing or Support")
	}
	cur, err := c.staff(ctx, id)
	isNew := errors.Is(err, ErrNotFound)
	if id == by && (!active || role != RoleOwner) {
		return Staff{}, "", invalid("you can't remove your own Owner access")
	}
	pw := ""
	if isNew || reset {
		pw = TempPassword()
		h, err := hashPassword(pw)
		if err != nil {
			return Staff{}, "", err
		}
		cur.Hash, cur.MustReset = h, true
	}
	if isNew {
		cur.ID, cur.Created = id, c.stamp()
	}
	cur.Name, cur.Role, cur.Active = firstOf(clean(name, 80), id), role, active
	if err := docs.Upsert(ctx, c.Docs, T, cStaff, id, cur, by); err != nil {
		return cur, "", err
	}
	if !active {
		ss, _ := docs.List[Session](ctx, c.Docs, T, cSession, "")
		for _, s := range ss {
			if s.Staff == id && !s.Ended {
				_, _ = docs.Update(ctx, c.Docs, T, cSession, s.ID, by, func(x *Session) error { x.Ended = true; return nil })
			}
		}
	}
	c.log(ctx, by, "staff.save", id, role)
	cur.Hash = ""
	return cur, pw, nil
}
