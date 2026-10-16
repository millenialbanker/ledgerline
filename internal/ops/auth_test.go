package ops

import (
	"errors"
	"testing"
	"time"
)

// RFC 6238 appendix B test vector (SHA-1, secret "12345678901234567890").
func TestTOTPVector(t *testing.T) {
	secret := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	for ts, want := range map[int64]string{59: "287082", 1111111109: "081804", 1234567890: "005924", 2000000000: "279037"} {
		if got := TOTPCode(secret, time.Unix(ts, 0)); got != want {
			t.Errorf("TOTP at %d = %s, want %s", ts, got, want)
		}
	}
}

func TestSignIn(t *testing.T) {
	o, ctx := newOps(t)
	now := time.Date(2026, 10, 16, 10, 0, 0, 0, ist)
	o.Now = func() time.Time { return now }

	// Wrong password, unknown user: same generic error.
	if _, _, err := o.Login(ctx, T, "priya.nair", "nope", "", "1.1.1.1", ""); !errors.Is(err, ErrAuth) {
		t.Fatalf("wrong password: %v", err)
	}
	if _, _, err := o.Login(ctx, T, "ghost", DevPassword, "", "1.1.1.1", ""); !errors.Is(err, ErrAuth) {
		t.Fatalf("unknown user: %v", err)
	}
	tok, st, err := o.Login(ctx, T, "Priya.Nair", DevPassword, "", "1.1.1.1", "test")
	if err != nil || st.User != "priya.nair" || st.Restricted() {
		t.Fatalf("login: %v %+v", err, st)
	}
	if s, err := o.SessionUser(ctx, T, tok); err != nil || s.User != "priya.nair" {
		t.Fatalf("session: %v", err)
	}
	// Idle timeout (30 minutes by default).
	now = now.Add(31 * time.Minute)
	if _, err := o.SessionUser(ctx, T, tok); !errors.Is(err, ErrSessionEnded) {
		t.Fatalf("idle session still valid: %v", err)
	}
	// Lockout after five wrong passwords, even with the right one after.
	for i := 0; i < 5; i++ {
		o.Login(ctx, T, "farhan.khan", "wrong-password", "", "", "")
	}
	if _, _, err := o.Login(ctx, T, "farhan.khan", DevPassword, "", "", ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected lockout, got %v", err)
	}
	now = now.Add(16 * time.Minute)
	if _, _, err := o.Login(ctx, T, "farhan.khan", DevPassword, "", "", ""); err != nil {
		t.Fatalf("after lockout: %v", err)
	}

	// Admin creates a user: temporary password, must change, then 2FA.
	pw, err := o.CreateUser(ctx, T, "admin", User{ID: "meena.r", Name: "Meena R", Role: RoleMaker})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.CreateUser(ctx, T, "priya.nair", User{ID: "x.y", Name: "X", Role: RoleAdmin}); !errors.Is(err, ErrForbidden) {
		t.Fatal("a maker created a user")
	}
	tok, st, err = o.Login(ctx, T, "meena.r", pw, "", "", "")
	if err != nil || !st.MustChange || !st.Restricted() {
		t.Fatalf("new user: %v %+v", err, st)
	}
	if err := o.ChangePassword(ctx, T, "meena.r", pw, "short"); err == nil {
		t.Fatal("short password accepted")
	}
	if err := o.ChangePassword(ctx, T, "meena.r", pw, "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	secret, uri, err := o.TOTPSetup(ctx, T, "meena.r")
	if err != nil || len(secret) != 32 || uri == "" {
		t.Fatalf("setup: %v %q", err, uri)
	}
	if err := o.TOTPEnable(ctx, T, "meena.r", "000000"); err == nil {
		t.Fatal("wrong code enabled 2FA")
	}
	if err := o.TOTPEnable(ctx, T, "meena.r", TOTPCode(secret, now)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := o.Login(ctx, T, "meena.r", "correct horse battery", "", "", ""); !errors.Is(err, ErrOTPRequired) {
		t.Fatalf("expected a code request, got %v", err)
	}
	// The code used to enable cannot be replayed; the next step's can.
	if _, _, err := o.Login(ctx, T, "meena.r", "correct horse battery", TOTPCode(secret, now), "", ""); err == nil {
		t.Fatal("replayed code accepted")
	}
	now = now.Add(30 * time.Second)
	tok, st, err = o.Login(ctx, T, "meena.r", "correct horse battery", TOTPCode(secret, now), "", "")
	if err != nil || st.Restricted() {
		t.Fatalf("2FA login: %v %+v", err, st)
	}
	// Admin reset ends the session.
	if _, err := o.ResetPassword(ctx, T, "admin", "meena.r"); err != nil {
		t.Fatal(err)
	}
	if _, err := o.SessionUser(ctx, T, tok); !errors.Is(err, ErrSessionEnded) {
		t.Fatal("session survived a password reset")
	}
	// Two-factor required by policy: a user without it is restricted.
	set, _ := o.Settings(ctx, T)
	set.TwoFactor = "required"
	o.SaveSettings(ctx, T, "admin", set)
	_, st, err = o.Login(ctx, T, "vikram.shah", DevPassword, "", "", "")
	if err != nil || !st.TOTPSetupRequired {
		t.Fatalf("policy: %v %+v", err, st)
	}
	// Deactivated users cannot sign in.
	u, _ := o.user(ctx, T, "anita.rao")
	u.Active = false
	o.PutUser(ctx, T, "admin", u)
	if _, _, err := o.Login(ctx, T, "anita.rao", DevPassword, "", "", ""); err == nil {
		t.Fatal("deactivated user signed in")
	}
}
