package ops

import (
	"context"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"ledgerline/internal/docs"
)

// ---- sign-in: passwords, two-factor codes and sessions ----
//
// Passwords are stored as PBKDF2-HMAC-SHA256 hashes (600,000 iterations,
// 16-byte salt). Two-factor authentication is TOTP (RFC 6238: 30-second
// steps, 6 digits), the scheme authenticator apps use. A session is a random
// 32-byte token held in an HttpOnly cookie; only its SHA-256 is stored.
// Sessions end after a period of inactivity, after 12 hours regardless, on
// sign-out, and for every session of a user when an admin resets their
// password or two-factor setup. Five wrong passwords lock the user for 15
// minutes. Every sign-in, failure and change is in the audit trail.

// PasswordIterations is the PBKDF2 work factor for new hashes. Tests lower it.
var PasswordIterations = 600000

const (
	cCred    = "cred"
	cSession = "session"

	maxFailures    = 5
	lockFor        = 15 * time.Minute
	sessionMaxAge  = 12 * time.Hour
	minPasswordLen = 12
)

// ErrAuth is a failed sign-in. The message never says which part was wrong.
var ErrAuth = errors.New("wrong user ID or password")

// ErrOTPRequired means the password was right and a two-factor code is needed.
var ErrOTPRequired = errors.New("enter the 6-digit code from your authenticator app")

// ErrSessionEnded means the session is missing, expired or revoked.
var ErrSessionEnded = errors.New("your session has ended; sign in again")

// Credential is a user's sign-in secrets. It is never returned by the API.
type Credential struct {
	User         string `json:"user"`
	Hash         string `json:"hash"` // pbkdf2-sha256$iter$salt$key (base64)
	MustChange   bool   `json:"must_change"`
	TOTPSecret   string `json:"totp_secret,omitempty"`
	TOTPPending  string `json:"totp_pending,omitempty"`
	TOTPEnabled  bool   `json:"totp_enabled"`
	TOTPLastStep int64  `json:"totp_last_step,omitempty"`
	Failed       int    `json:"failed"`
	LockedUntil  string `json:"locked_until,omitempty"`
	Epoch        int    `json:"epoch"` // bumped to end every session
	LastLogin    string `json:"last_login,omitempty"`
	ChangedAt    string `json:"changed_at,omitempty"`
}

// Session is a signed-in browser.
type Session struct {
	User     string `json:"user"`
	Created  string `json:"created"`
	LastSeen string `json:"last_seen"`
	Expires  string `json:"expires"`
	Epoch    int    `json:"epoch"`
	IP       string `json:"ip,omitempty"`
	Agent    string `json:"agent,omitempty"`
	Revoked  bool   `json:"revoked,omitempty"`
}

// AuthState tells the console what the signed-in user still has to do.
type AuthState struct {
	User              string `json:"user"`
	MustChange        bool   `json:"must_change_password"`
	TOTPSetupRequired bool   `json:"totp_setup_required"`
	TOTPEnabled       bool   `json:"totp_enabled"`
}

// Restricted reports whether the session may only finish sign-in setup.
func (a AuthState) Restricted() bool { return a.MustChange || a.TOTPSetupRequired }

// ---- passwords ----

func hashPassword(pw string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	k, err := pbkdf2.Key(sha256.New, pw, salt, PasswordIterations, 32)
	if err != nil {
		return "", err
	}
	b := base64.RawStdEncoding
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", PasswordIterations, b.EncodeToString(salt), b.EncodeToString(k)), nil
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

var weakPasswords = map[string]bool{"password1234": true, "123456789012": true, "qwertyuiop12": true, "ledgerline123": true, "welcome12345": true, "changeme1234": true}

// CheckPasswordPolicy enforces the minimum: 12 characters, not the user
// ID, not a well-known password.
func CheckPasswordPolicy(user, pw string) error {
	if len([]rune(pw)) < minPasswordLen {
		return invalid("the password must be at least %d characters", minPasswordLen)
	}
	low := strings.ToLower(pw)
	if strings.Contains(low, strings.ToLower(user)) {
		return invalid("the password must not contain your user ID")
	}
	if weakPasswords[low] {
		return invalid("that password is too common")
	}
	return nil
}

// TempPassword returns a random 16-character password for a new or reset user.
func TempPassword() string {
	const alphabet = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 16)
	rand.Read(b)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b[:4]) + "-" + string(b[4:8]) + "-" + string(b[8:12]) + "-" + string(b[12:])
}

// ---- TOTP ----

func newTOTPSecret() string {
	b := make([]byte, 20)
	rand.Read(b)
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
}

func totpAt(secret string, step int64) (string, bool) {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(strings.ReplaceAll(secret, " ", "")))
	if err != nil {
		return "", false
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	m := hmac.New(sha1.New, key)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	code := (binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff) % 1000000
	return fmt.Sprintf("%06d", code), true
}

// TOTPCode is the current code for a secret (used by tests and the
// development sign-in).
func TOTPCode(secret string, t time.Time) string {
	c, _ := totpAt(secret, t.Unix()/30)
	return c
}

// verifyTOTP accepts the current code or one step either side, never a
// step at or before the last one used.
func verifyTOTP(secret, code string, now time.Time, last int64) (int64, bool) {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != 6 {
		return 0, false
	}
	cur := now.Unix() / 30
	for _, s := range []int64{cur - 1, cur, cur + 1} {
		if s <= last {
			continue
		}
		if c, ok := totpAt(secret, s); ok && subtle.ConstantTimeCompare([]byte(c), []byte(code)) == 1 {
			return s, true
		}
	}
	return 0, false
}

// ---- credentials ----

func (o *Ops) cred(ctx context.Context, tenant, user string) (Credential, int64, error) {
	return docs.Get[Credential](ctx, o.Docs, tenant, cCred, user)
}

func (o *Ops) user(ctx context.Context, tenant, id string) (User, error) {
	u, _, err := docs.Get[User](ctx, o.Docs, tenant, cUser, id)
	return u, err
}

// SetPassword sets a user's password directly (bootstrap and development).
func (o *Ops) SetPassword(ctx context.Context, tenant, actor, user, pw string, mustChange bool) error {
	h, err := hashPassword(pw)
	if err != nil {
		return err
	}
	_, err = upd(ctx, o.Docs, tenant, cCred, user, actor, &Credential{User: user}, func(c *Credential) error {
		c.User, c.Hash, c.MustChange, c.Failed, c.LockedUntil, c.ChangedAt = user, h, mustChange, 0, "", o.now().Format(time.RFC3339)
		return nil
	})
	return err
}

// Login checks a password and, when enabled, a two-factor code, and opens a
// session. It returns the session token for the cookie.
func (o *Ops) Login(ctx context.Context, tenant, userID, pw, otp, ip, agent string) (string, AuthState, error) {
	userID = strings.ToLower(strings.TrimSpace(userID))
	now := o.now()
	u, uerr := o.user(ctx, tenant, userID)
	c, _, cerr := o.cred(ctx, tenant, userID)
	if uerr != nil || cerr != nil || c.Hash == "" {
		checkPassword("pbkdf2-sha256$600000$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", pw) // same work either way
		o.log(ctx, tenant, userID, "auth.login_failed", userID, map[string]any{"reason": "unknown user", "ip": ip})
		return "", AuthState{}, ErrAuth
	}
	if c.LockedUntil != "" {
		if t, err := time.Parse(time.RFC3339, c.LockedUntil); err == nil && now.Before(t) {
			o.log(ctx, tenant, userID, "auth.login_failed", userID, map[string]any{"reason": "locked", "ip": ip})
			return "", AuthState{}, fmt.Errorf("%w: too many wrong attempts; try again after %s", ErrForbidden, t.In(ist).Format("15:04"))
		}
	}
	if !checkPassword(c.Hash, pw) {
		var locked bool
		upd(ctx, o.Docs, tenant, cCred, userID, userID, nil, func(x *Credential) error {
			x.Failed++
			if x.Failed >= maxFailures {
				x.LockedUntil, x.Failed, locked = now.Add(lockFor).Format(time.RFC3339), 0, true
			}
			return nil
		})
		o.log(ctx, tenant, userID, "auth.login_failed", userID, map[string]any{"reason": "password", "ip": ip, "locked": locked})
		return "", AuthState{}, ErrAuth
	}
	if !u.Active {
		o.log(ctx, tenant, userID, "auth.login_failed", userID, map[string]any{"reason": "deactivated", "ip": ip})
		return "", AuthState{}, fmt.Errorf("%w: this user is deactivated; ask your administrator", ErrForbidden)
	}
	if c.TOTPEnabled {
		if strings.TrimSpace(otp) == "" {
			return "", AuthState{}, ErrOTPRequired
		}
		step, ok := verifyTOTP(c.TOTPSecret, otp, now, c.TOTPLastStep)
		if !ok {
			upd(ctx, o.Docs, tenant, cCred, userID, userID, nil, func(x *Credential) error {
				x.Failed++
				if x.Failed >= maxFailures {
					x.LockedUntil, x.Failed = now.Add(lockFor).Format(time.RFC3339), 0
				}
				return nil
			})
			o.log(ctx, tenant, userID, "auth.login_failed", userID, map[string]any{"reason": "two-factor code", "ip": ip})
			return "", AuthState{}, fmt.Errorf("%w: that code is wrong or already used", ErrAuth)
		}
		c.TOTPLastStep = step
	}
	_, err := upd(ctx, o.Docs, tenant, cCred, userID, userID, nil, func(x *Credential) error {
		x.Failed, x.LockedUntil, x.LastLogin = 0, "", now.Format(time.RFC3339)
		if c.TOTPLastStep > x.TOTPLastStep {
			x.TOTPLastStep = c.TOTPLastStep
		}
		return nil
	})
	if err != nil {
		return "", AuthState{}, err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", AuthState{}, err
	}
	tok := base64.RawURLEncoding.EncodeToString(raw)
	s := Session{User: userID, Created: now.Format(time.RFC3339), LastSeen: now.Format(time.RFC3339), Expires: now.Add(sessionMaxAge).Format(time.RFC3339), Epoch: c.Epoch, IP: ip, Agent: trim(agent, 200)}
	if _, err := docs.Put(ctx, o.Docs, tenant, cSession, tokenID(tok), s, 0, userID); err != nil {
		return "", AuthState{}, err
	}
	o.log(ctx, tenant, userID, "auth.login", userID, map[string]any{"ip": ip, "two_factor": c.TOTPEnabled})
	st, err := o.authState(ctx, tenant, userID)
	return tok, st, err
}

func trim(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func tokenID(tok string) string {
	h := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(h[:])
}

func (o *Ops) authState(ctx context.Context, tenant, user string) (AuthState, error) {
	c, _, err := o.cred(ctx, tenant, user)
	if err != nil {
		return AuthState{}, err
	}
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return AuthState{}, err
	}
	return AuthState{User: user, MustChange: c.MustChange, TOTPEnabled: c.TOTPEnabled, TOTPSetupRequired: set.Require2FA() && !c.TOTPEnabled}, nil
}

// SessionUser resolves a session token to its user, ending it if it has
// been idle too long, has passed its maximum age, or was revoked.
func (o *Ops) SessionUser(ctx context.Context, tenant, tok string) (AuthState, error) {
	if tok == "" {
		return AuthState{}, ErrSessionEnded
	}
	id := tokenID(tok)
	s, ver, err := docs.Get[Session](ctx, o.Docs, tenant, cSession, id)
	if err != nil {
		return AuthState{}, ErrSessionEnded
	}
	now := o.now()
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return AuthState{}, err
	}
	idle := time.Duration(set.SessionIdleMinutes) * time.Minute
	if idle <= 0 {
		idle = 30 * time.Minute
	}
	exp, _ := time.Parse(time.RFC3339, s.Expires)
	seen, _ := time.Parse(time.RFC3339, s.LastSeen)
	c, _, cerr := o.cred(ctx, tenant, s.User)
	u, uerr := o.user(ctx, tenant, s.User)
	if s.Revoked || now.After(exp) || now.Sub(seen) > idle || cerr != nil || uerr != nil || c.Epoch != s.Epoch || !u.Active {
		return AuthState{}, ErrSessionEnded
	}
	if now.Sub(seen) > 5*time.Minute {
		s.LastSeen = now.Format(time.RFC3339)
		docs.Put(ctx, o.Docs, tenant, cSession, id, s, ver, s.User) // best effort; a race just means another request touched it
	}
	return AuthState{User: s.User, MustChange: c.MustChange, TOTPEnabled: c.TOTPEnabled, TOTPSetupRequired: set.Require2FA() && !c.TOTPEnabled}, nil
}

// Logout ends a session.
func (o *Ops) Logout(ctx context.Context, tenant, tok string) error {
	id := tokenID(tok)
	_, err := upd(ctx, o.Docs, tenant, cSession, id, "", nil, func(s *Session) error {
		if s.User == "" {
			return ErrSessionEnded
		}
		s.Revoked = true
		o.log(ctx, tenant, s.User, "auth.logout", s.User, nil)
		return nil
	})
	if errors.Is(err, ErrSessionEnded) {
		return nil
	}
	return err
}

// ChangePassword changes the signed-in user's own password.
func (o *Ops) ChangePassword(ctx context.Context, tenant, user, current, next string) error {
	c, _, err := o.cred(ctx, tenant, user)
	if err != nil {
		return err
	}
	if !checkPassword(c.Hash, current) {
		return fmt.Errorf("%w: the current password is wrong", ErrInvalid)
	}
	if current == next {
		return invalid("choose a password different from the current one")
	}
	if err := CheckPasswordPolicy(user, next); err != nil {
		return err
	}
	if err := o.SetPassword(ctx, tenant, user, user, next, false); err != nil {
		return err
	}
	o.log(ctx, tenant, user, "auth.password_changed", user, nil)
	return nil
}

// TOTPSetup starts two-factor setup: it stores a pending secret and returns
// it with the otpauth:// link authenticator apps read.
func (o *Ops) TOTPSetup(ctx context.Context, tenant, user string) (secret, uri string, err error) {
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return "", "", err
	}
	secret = newTOTPSecret()
	_, err = upd(ctx, o.Docs, tenant, cCred, user, user, nil, func(c *Credential) error {
		if c.User == "" {
			return ErrNotFound
		}
		c.TOTPPending = secret
		return nil
	})
	issuer := "Ledgerline"
	if set.Firm != "" {
		issuer = "Ledgerline " + set.Firm
	}
	uri = "otpauth://totp/" + url.PathEscape(issuer+":"+user+"@"+tenant) + "?secret=" + secret + "&issuer=" + url.QueryEscape(issuer) + "&algorithm=SHA1&digits=6&period=30"
	return secret, uri, err
}

// TOTPEnable confirms setup with a code from the app and turns two-factor
// sign-in on.
func (o *Ops) TOTPEnable(ctx context.Context, tenant, user, code string) error {
	now := o.now()
	_, err := upd(ctx, o.Docs, tenant, cCred, user, user, nil, func(c *Credential) error {
		if c.TOTPPending == "" {
			return invalid("start two-factor setup first")
		}
		step, ok := verifyTOTP(c.TOTPPending, code, now, 0)
		if !ok {
			return invalid("that code does not match; check the time on your phone and try the next code")
		}
		c.TOTPSecret, c.TOTPPending, c.TOTPEnabled, c.TOTPLastStep = c.TOTPPending, "", true, step
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, user, "auth.2fa_enabled", user, nil)
	}
	return err
}

// ---- user administration ----

// UserView is a user with sign-in status, for the admin screen.
type UserView struct {
	User
	HasPassword bool   `json:"has_password"`
	TOTPEnabled bool   `json:"totp_enabled"`
	MustChange  bool   `json:"must_change_password"`
	Locked      bool   `json:"locked"`
	LastLogin   string `json:"last_login,omitempty"`
}

// UserViews lists users with their sign-in status.
func (o *Ops) UserViews(ctx context.Context, tenant string) ([]UserView, error) {
	us, err := o.Users(ctx, tenant)
	if err != nil {
		return nil, err
	}
	now := o.now()
	out := make([]UserView, 0, len(us))
	for _, u := range us {
		v := UserView{User: u}
		if c, _, err := o.cred(ctx, tenant, u.ID); err == nil {
			v.HasPassword, v.TOTPEnabled, v.MustChange, v.LastLogin = c.Hash != "", c.TOTPEnabled, c.MustChange, c.LastLogin
			if t, err := time.Parse(time.RFC3339, c.LockedUntil); err == nil && now.Before(t) {
				v.Locked = true
			}
		}
		out = append(out, v)
	}
	return out, nil
}

// CreateUser adds a user with a one-time password they must change at
// first sign-in. The temporary password is returned once and not stored.
func (o *Ops) CreateUser(ctx context.Context, tenant, actor string, u User) (string, error) {
	if err := o.require(ctx, tenant, actor, "Adding users", RoleAdmin); err != nil {
		return "", err
	}
	u.ID = strings.ToLower(strings.TrimSpace(u.ID))
	if _, err := o.user(ctx, tenant, u.ID); err == nil {
		return "", invalid("user %s already exists", u.ID)
	}
	u.Active = true
	if err := o.PutUser(ctx, tenant, actor, u); err != nil {
		return "", err
	}
	pw := TempPassword()
	if err := o.SetPassword(ctx, tenant, actor, u.ID, pw, true); err != nil {
		return "", err
	}
	o.log(ctx, tenant, actor, "user.created", u.ID, map[string]any{"role": u.Role})
	return pw, nil
}

// ResetPassword gives a user a new one-time password and ends their sessions.
func (o *Ops) ResetPassword(ctx context.Context, tenant, actor, user string) (string, error) {
	if err := o.require(ctx, tenant, actor, "Resetting passwords", RoleAdmin); err != nil {
		return "", err
	}
	if _, err := o.user(ctx, tenant, user); err != nil {
		return "", err
	}
	pw := TempPassword()
	if err := o.SetPassword(ctx, tenant, actor, user, pw, true); err != nil {
		return "", err
	}
	if err := o.endSessions(ctx, tenant, actor, user); err != nil {
		return "", err
	}
	o.log(ctx, tenant, actor, "user.password_reset", user, nil)
	return pw, nil
}

// ResetTOTP turns off a user's two-factor sign-in (lost phone) and ends
// their sessions. They set it up again at next sign-in if it is required.
func (o *Ops) ResetTOTP(ctx context.Context, tenant, actor, user string) error {
	if err := o.require(ctx, tenant, actor, "Resetting two-factor sign-in", RoleAdmin); err != nil {
		return err
	}
	_, err := upd(ctx, o.Docs, tenant, cCred, user, actor, nil, func(c *Credential) error {
		if c.User == "" {
			return ErrNotFound
		}
		c.TOTPSecret, c.TOTPPending, c.TOTPEnabled, c.TOTPLastStep = "", "", false, 0
		c.Epoch++
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "user.2fa_reset", user, nil)
	}
	return err
}

func (o *Ops) endSessions(ctx context.Context, tenant, actor, user string) error {
	_, err := upd(ctx, o.Docs, tenant, cCred, user, actor, nil, func(c *Credential) error {
		c.Epoch++
		return nil
	})
	return err
}

// BootstrapAdmin creates the first administrator of a tenant that has no
// one able to sign in yet. It does nothing once any user has a password.
func (o *Ops) BootstrapAdmin(ctx context.Context, tenant, id, name, pw string) (bool, error) {
	us, err := o.Users(ctx, tenant)
	if err != nil {
		return false, err
	}
	for _, u := range us {
		if c, _, err := o.cred(ctx, tenant, u.ID); err == nil && c.Hash != "" {
			return false, nil
		}
	}
	if err := CheckPasswordPolicy(id, pw); err != nil {
		return false, err
	}
	if err := o.PutUser(ctx, tenant, "bootstrap", User{ID: id, Name: name, Role: RoleAdmin, Active: true}); err != nil {
		return false, err
	}
	return true, o.SetPassword(ctx, tenant, "bootstrap", id, pw, true)
}

// upd is docs.Update that can start from def when the document is missing.
func upd[T any](ctx context.Context, s docs.Store, tenant, coll, id, actor string, def *T, fn func(*T) error) (T, error) {
	for attempt := 0; attempt < 5; attempt++ {
		v, ver, err := docs.Get[T](ctx, s, tenant, coll, id)
		if errors.Is(err, docs.ErrNotFound) && def != nil {
			v, ver, err = *def, 0, nil
		}
		if err != nil {
			return v, err
		}
		if err := fn(&v); err != nil {
			return v, err
		}
		if _, err := docs.Put(ctx, s, tenant, coll, id, v, ver, actor); err != nil {
			if errors.Is(err, docs.ErrStale) {
				continue
			}
			return v, err
		}
		return v, nil
	}
	var zero T
	return zero, docs.ErrStale
}
