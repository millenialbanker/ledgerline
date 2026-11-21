package ops

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/docs"
)

// mobileClock fixes the time and returns a way to move it.
func mobileClock(o *Ops) func(time.Duration) {
	now := time.Date(2026, 10, 16, 10, 0, 0, 0, ist)
	o.Now = func() time.Time { return now }
	return func(d time.Duration) { now = now.Add(d) }
}

func staffOn(t *testing.T, o *Ops, ctx context.Context, m MobileSettings) {
	t.Helper()
	m.StaffOn = true
	if _, err := o.SaveMobileSettings(ctx, T, "admin", m); err != nil {
		t.Fatal(err)
	}
}

func signIn(t *testing.T, o *Ops, ctx context.Context, user, pw string) string {
	t.Helper()
	tok, _, err := o.Login(ctx, T, user, pw, "", "10.0.0.1", "test")
	if err != nil {
		t.Fatalf("login %s: %v", user, err)
	}
	return tok
}

const pushTok = "fcm-token-made-up-0000000000000000000001"

func TestMobileSettingsAndStaffAccess(t *testing.T) {
	o, ctx := setup(t)
	mobileClock(o)
	if err := o.StaffAppAccess(ctx, T, "farhan.khan"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("off by default: %v", err)
	}
	if _, err := o.SaveMobileSettings(ctx, T, "priya.nair", MobileSettings{StaffOn: true}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a maker changed the settings: %v", err)
	}
	if _, err := o.MobileSettingsFor(ctx, T, "priya.nair"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a maker saw the settings: %v", err)
	}
	for _, bad := range []MobileSettings{
		{StaffUsers: []string{"nobody.here"}},
		{StaffRoles: []string{"Janitor"}},
		{StaffBranches: []string{"Atlantis"}},
		{IdleMinutes: 90},
		{ReauthDays: 400},
		{Client: ClientAppBrand{On: true}},
		{Client: ClientAppBrand{AppName: "Demo", Primary: "blue"}},
		{Client: ClientAppBrand{AppName: "Demo", Logo: "data:image/svg+xml;base64,PHN2Zz4="}},
		{Client: ClientAppBrand{AppName: "Demo", InvestorCharterURL: "http://example.com/charter"}},
	} {
		if _, err := o.SaveMobileSettings(ctx, T, "admin", bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v saved: %v", bad, err)
		}
	}
	staffOn(t, o, ctx, MobileSettings{StaffUsers: []string{"Priya.Nair"}, StaffRoles: []string{RoleChecker}, StaffBranches: []string{"Bengaluru"}})
	for user, want := range map[string]bool{"priya.nair": true, "farhan.khan": true, "meera.iyer": true, "anita.rao": false, "vikram.shah": false, "admin": false, "sunrise.ap": false} {
		if err := o.StaffAppAccess(ctx, T, user); (err == nil) != want {
			t.Errorf("%s: access %v, want %v", user, err, want)
		}
	}
	v, err := o.MobileSettingsFor(ctx, T, "vikram.shah")
	if err != nil || !v.StaffOn || v.CanEdit || v.IdleMinutes != 5 || v.ReauthDays != 30 || len(v.StaffUsers) != 1 || v.StaffUsers[0] != "priya.nair" {
		t.Fatalf("view %+v %v", v, err)
	}
	cfg, _ := o.MobileAppConfig(ctx, T, AppClient)
	if cfg.On || cfg.ScoresURL == "" || cfg.ODRURL == "" || cfg.Brand == nil || cfg.Brand.AppName == "" {
		t.Fatalf("client config %+v", cfg)
	}
}

func TestMobileDevicesAndUnlock(t *testing.T) {
	o, ctx := setup(t)
	move := mobileClock(o)
	tok := signIn(t, o, ctx, "farhan.khan", DevPassword)
	req := DeviceReq{App: AppStaff, Platform: "ios", Name: "Farhan's iPhone", PushToken: pushTok}
	if _, _, err := o.RegisterDevice(ctx, T, "farhan.khan", tok, req); !errors.Is(err, ErrForbidden) {
		t.Fatalf("registered with the staff app off: %v", err)
	}
	staffOn(t, o, ctx, MobileSettings{StaffRoles: []string{RoleChecker, RoleAdmin}})
	if _, _, err := o.RegisterDevice(ctx, T, "farhan.khan", tok, DeviceReq{App: AppClient, Platform: "ios"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("staff registered the client app: %v", err)
	}
	if _, _, err := o.RegisterDevice(ctx, T, "farhan.khan", tok, DeviceReq{App: AppStaff, Platform: "windows"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown platform: %v", err)
	}
	if _, _, err := o.RegisterDevice(ctx, T, "farhan.khan", "not-a-session", req); !errors.Is(err, ErrForbidden) {
		t.Fatalf("registered without a session: %v", err)
	}
	other := signIn(t, o, ctx, "admin", DevPassword)
	if _, _, err := o.RegisterDevice(ctx, T, "farhan.khan", other, req); !errors.Is(err, ErrForbidden) {
		t.Fatalf("registered with another person's session: %v", err)
	}
	d, secret, err := o.RegisterDevice(ctx, T, "farhan.khan", tok, req)
	if err != nil || len(secret) < 40 || !d.Push || d.Status != "Active" || d.ReauthBy != "2026-11-15" {
		t.Fatalf("register %+v %q %v", d, secret, err)
	}
	// The registering session is now the phone's: a second registration
	// from it is refused, and it ends after the app's idle time.
	if _, _, err := o.RegisterDevice(ctx, T, "farhan.khan", tok, req); !errors.Is(err, ErrForbidden) {
		t.Fatalf("registered twice from one session: %v", err)
	}
	// The secret is never stored or shown; the token is not shown either.
	b, _ := json.Marshal(d)
	if strings.Contains(string(b), secret) || strings.Contains(string(b), pushTok) || strings.Contains(string(b), "secret_hash") {
		t.Fatalf("view leaks: %s", b)
	}
	raw, _, _ := docsGetDevice(ctx, o, d.ID)
	if raw.SecretHash == "" || strings.Contains(raw.SecretHash, secret) {
		t.Fatal("secret stored in clear")
	}
	if _, _, _, err := o.Unlock(ctx, T, d.ID, "wrong-secret", "10.0.0.2", ""); !errors.Is(err, ErrDeviceRevoked) {
		t.Fatalf("wrong secret: %v", err)
	}
	atok, st, _, err := o.Unlock(ctx, T, d.ID, secret, "10.0.0.2", "")
	if err != nil || st.User != "farhan.khan" {
		t.Fatalf("unlock %+v %v", st, err)
	}
	if _, err := o.SessionUser(ctx, T, atok); err != nil {
		t.Fatal(err)
	}
	if o.SessionDevice(ctx, T, atok) != d.ID || o.SessionDevice(ctx, T, tok) != d.ID {
		t.Fatal("sessions not bound to the phone")
	}
	move(4 * time.Minute)
	if _, err := o.SessionUser(ctx, T, atok); err != nil {
		t.Fatalf("4 minutes idle: %v", err)
	}
	move(6 * time.Minute)
	if _, err := o.SessionUser(ctx, T, atok); !errors.Is(err, ErrSessionEnded) {
		t.Fatalf("app session outlived 5 minutes idle: %v", err)
	}
	if _, err := o.SessionUser(ctx, T, tok); !errors.Is(err, ErrSessionEnded) {
		t.Fatalf("registering session outlived the app's idle time: %v", err)
	}
	// A browser session keeps the firm's 30 minutes.
	btok := signIn(t, o, ctx, "admin", DevPassword)
	move(10 * time.Minute)
	if _, err := o.SessionUser(ctx, T, btok); err != nil {
		t.Fatalf("browser session: %v", err)
	}
	ds, _ := o.Devices(ctx, T, "farhan.khan", "")
	if len(ds) != 1 || ds[0].ID != d.ID {
		t.Fatalf("own devices %+v", ds)
	}
	if _, err := o.Devices(ctx, T, "farhan.khan", AppStaff); !errors.Is(err, ErrForbidden) {
		t.Fatalf("checker listed everyone's phones: %v", err)
	}
	if all, err := o.Devices(ctx, T, "admin", AppStaff); err != nil || len(all) != 1 || all[0].UserName != "Farhan Khan" {
		t.Fatalf("admin list %+v %v", all, err)
	}
	// Revoking: not by another user; by Admin with a reason.
	if _, err := o.RevokeDevice(ctx, T, "priya.nair", d.ID, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("maker revoked: %v", err)
	}
	if _, err := o.RevokeDevice(ctx, T, "admin", d.ID, " "); !errors.Is(err, ErrInvalid) {
		t.Fatalf("revoked without a reason: %v", err)
	}
	atok, _, _, _ = o.Unlock(ctx, T, d.ID, secret, "", "")
	if v, err := o.RevokeDevice(ctx, T, "admin", d.ID, "Phone lost"); err != nil || v.Status != "Revoked" || v.Push {
		t.Fatalf("revoke %+v %v", v, err)
	}
	if _, err := o.SessionUser(ctx, T, atok); !errors.Is(err, ErrSessionEnded) {
		t.Fatalf("session of a revoked phone: %v", err)
	}
	if _, _, _, err := o.Unlock(ctx, T, d.ID, secret, "", ""); !errors.Is(err, ErrDeviceRevoked) {
		t.Fatalf("unlock after revoke: %v", err)
	}

	// A password reset stops every phone of the user.
	reg := func() (DeviceView, string) {
		t.Helper()
		move(time.Minute)
		v, s, err := o.RegisterDevice(ctx, T, "farhan.khan", signIn(t, o, ctx, "farhan.khan", DevPassword), req)
		if err != nil {
			t.Fatal(err)
		}
		return v, s
	}
	d2, s2 := reg()
	pw, err := o.ResetPassword(ctx, T, "admin", "farhan.khan")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := o.Unlock(ctx, T, d2.ID, s2, "", ""); !errors.Is(err, ErrDeviceRevoked) {
		t.Fatalf("unlock after a password reset: %v", err)
	}
	o.SetPassword(ctx, T, "admin", "farhan.khan", DevPassword, false)
	_ = pw
	// After ReauthDays the password is needed again.
	d3, s3 := reg()
	move(31 * 24 * time.Hour)
	if _, _, _, err := o.Unlock(ctx, T, d3.ID, s3, "", ""); !errors.Is(err, ErrReauth) {
		t.Fatalf("unlock after 31 days: %v", err)
	}
	// Five wrong secrets revoke the phone and record a security event.
	d4, s4 := reg()
	for i := 0; i < 5; i++ {
		o.Unlock(ctx, T, d4.ID, "guess", "10.9.9.9", "")
	}
	if _, _, _, err := o.Unlock(ctx, T, d4.ID, s4, "", ""); !errors.Is(err, ErrDeviceRevoked) {
		t.Fatalf("right secret after five wrong ones: %v", err)
	}
	// Losing access to the app stops the phone working.
	d5, s5 := reg()
	staffOn(t, o, ctx, MobileSettings{StaffRoles: []string{RoleAdmin}})
	if _, _, _, err := o.Unlock(ctx, T, d5.ID, s5, "", ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("unlock without access: %v", err)
	}
	staffOn(t, o, ctx, MobileSettings{StaffRoles: []string{RoleChecker}})
	// At most five phones: the oldest goes.
	for i := 0; i < 5; i++ {
		reg()
	}
	if mine, _ := o.devicesOf(ctx, T, "farhan.khan"); len(mine) != 5 {
		t.Fatalf("%d active phones", len(mine))
	}
	if x, _, _ := docsGetDevice(ctx, o, d5.ID); x.Status != "Revoked" || x.RevokeNote != "replaced by a newer phone" {
		t.Fatalf("oldest phone %+v", x)
	}
	// The owner sets a new push token; nobody else can.
	if _, err := o.SetPushToken(ctx, T, "priya.nair", d5.ID, pushTok); !errors.Is(err, ErrNotFound) {
		t.Fatalf("someone else's token: %v", err)
	}
	if _, err := o.SetPushToken(ctx, T, "farhan.khan", d5.ID, "short"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad token: %v", err)
	}
}

func docsGetDevice(ctx context.Context, o *Ops, id string) (MobileDevice, int64, error) {
	return docs.Get[MobileDevice](ctx, o.Docs, T, cMobileDev, id)
}

func TestClientAppDevice(t *testing.T) {
	o, ctx := setup(t)
	mobileClock(o)
	cs, _ := o.Clients(ctx, T)
	var ucc string
	for _, c := range cs {
		if c.Status == "Active" {
			ucc = c.UCC
			break
		}
	}
	id, _, err := o.PortalAccess(ctx, T, "farhan.khan", ucc, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	o.SetPassword(ctx, T, "admin", id, "a long client password", false)
	tok := signIn(t, o, ctx, id, "a long client password")
	req := DeviceReq{App: AppClient, Platform: "android", Name: "Pixel", PushToken: pushTok}
	if _, _, err := o.RegisterDevice(ctx, T, id, tok, req); !errors.Is(err, ErrForbidden) {
		t.Fatalf("client app off: %v", err)
	}
	if _, err := o.SaveMobileSettings(ctx, T, "admin", MobileSettings{Client: ClientAppBrand{On: true, AppName: "Demo Invest", Primary: "#123456"}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := o.RegisterDevice(ctx, T, id, tok, DeviceReq{App: AppStaff, Platform: "android"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("client registered the staff app: %v", err)
	}
	d, s, err := o.RegisterDevice(ctx, T, id, tok, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, st, _, err := o.Unlock(ctx, T, d.ID, s, "", ""); err != nil || st.User != id {
		t.Fatalf("client unlock %+v %v", st, err)
	}
	if _, err := o.StaffApprovals(ctx, T, id, D); !errors.Is(err, ErrForbidden) {
		t.Fatalf("client opened the staff app: %v", err)
	}
	if v, err := o.RevokeDevice(ctx, T, id, d.ID, ""); err != nil || v.Status != "Revoked" {
		t.Fatalf("client revoke %+v %v", v, err)
	}
}
