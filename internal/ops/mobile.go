package ops

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/vault"
)

// ---- mobile apps: the staff app and the client app ----
//
// Both apps are thin native shells around phone pages served by Ledgerline
// (/m/ for staff, /app/ for clients). The first sign-in on a phone is the
// usual one: password and two-factor code for staff, the portal sign-in for
// clients. The page then registers the phone as a device: Ledgerline keeps
// the device's push token and the SHA-256 of a random device secret, and
// the app keeps the secret in the phone's keychain (iOS) or keystore
// (Android), released only by the phone's biometric check. Later the app
// unlocks with the secret instead of the password and gets a session that
// ends after a short idle time (5 minutes unless the firm sets another).
//
// A device stops working when it is revoked (by its owner, or by an Admin),
// when an Admin resets the user's password or two-factor sign-in, when the
// user is deactivated or loses access to the app, and after ReauthDays
// without a password sign-in on it. Five wrong secrets revoke it.

const (
	cMobileSet = "mobileset" // "main": the firm's mobile app settings
	cMobileDev = "mobiledev" // registered phones

	// AppStaff and AppClient are the two apps.
	AppStaff  = "staff"
	AppClient = "client"

	mobileIdleDefault   = 5
	mobileReauthDefault = 30
	maxDevicesPerUser   = 5
	maxUnlockFailures   = 5
	// A phone is registered from a password sign-in made in the last 15
	// minutes, so a browser session left open elsewhere cannot add one.
	registerWithin = 15 * time.Minute
)

// MobileSettings is Settings → Staff app and Settings → Client app.
type MobileSettings struct {
	// Staff app: off until an Admin switches it on and says who may use it,
	// by person, by role, or by branch (everyone of that branch).
	StaffOn       bool     `json:"staff_on"`
	StaffUsers    []string `json:"staff_users"`
	StaffRoles    []string `json:"staff_roles"`
	StaffBranches []string `json:"staff_branches"`
	// IdleMinutes ends an app session after this much inactivity (1 to 30);
	// LockSeconds is how long the app may be in the background before it
	// asks for the fingerprint or face again (0 to 600). ReauthDays is how
	// long a phone works without the password (1 to 90).
	IdleMinutes int `json:"idle_minutes"`
	LockSeconds int `json:"lock_seconds"`
	ReauthDays  int `json:"reauth_days"`
	// Client is the client app's on/off switch and brand.
	Client    ClientAppBrand `json:"client"`
	UpdatedAt string         `json:"updated_at,omitempty"`
	UpdatedBy string         `json:"updated_by,omitempty"`
}

// ClientAppBrand is how the client app looks and what it shows about the
// firm. The broker publishes the app under its own developer accounts; the
// build takes the app name, bundle ID, icon and server address from
// mobile/client/brand.json, and the pages take these values from here.
type ClientAppBrand struct {
	On      bool   `json:"on"`
	AppName string `json:"app_name"`
	Primary string `json:"primary"` // #RRGGBB, buttons and header
	Accent  string `json:"accent"`  // #RRGGBB, highlights
	// Logo is a PNG or JPEG as a data: URL, at most 150 KB.
	Logo string `json:"logo,omitempty"`
	// What Indian broker apps must show (SEBI's investor charter and
	// grievance circulars): registration numbers come from Settings
	// (SEBI registration, member codes) and DPReg here.
	DPReg              string `json:"dp_reg,omitempty"`
	RegisteredOffice   string `json:"registered_office,omitempty"`
	SupportEmail       string `json:"support_email,omitempty"`
	SupportPhone       string `json:"support_phone,omitempty"`
	GrievanceOfficer   string `json:"grievance_officer,omitempty"`
	GrievanceEmail     string `json:"grievance_email,omitempty"`
	GrievancePhone     string `json:"grievance_phone,omitempty"`
	ComplianceOfficer  string `json:"compliance_officer,omitempty"`
	InvestorCharterURL string `json:"investor_charter_url,omitempty"`
	RiskDisclosureURL  string `json:"risk_disclosure_url,omitempty"`
	WebsiteURL         string `json:"website_url,omitempty"`
}

// MobileSettingsView is the settings with the defaults filled in.
type MobileSettingsView struct {
	MobileSettings
	Roles    []string `json:"roles"`    // roles that can be given the staff app
	Branches []string `json:"branches"` // the firm's branches
	CanEdit  bool     `json:"can_edit"`
}

func defaultMobileSettings() MobileSettings {
	return MobileSettings{StaffUsers: []string{}, StaffRoles: []string{}, StaffBranches: []string{}, IdleMinutes: mobileIdleDefault, LockSeconds: 60, ReauthDays: mobileReauthDefault,
		Client: ClientAppBrand{Primary: "#1f4fd1", Accent: "#0f9d76"}}
}

func (o *Ops) mobileSettings(ctx context.Context, tenant string) MobileSettings {
	m, _, err := docs.GetOr(ctx, o.Docs, tenant, cMobileSet, "main", defaultMobileSettings())
	if err != nil {
		return defaultMobileSettings()
	}
	if m.IdleMinutes <= 0 {
		m.IdleMinutes = mobileIdleDefault
	}
	if m.ReauthDays <= 0 {
		m.ReauthDays = mobileReauthDefault
	}
	if m.StaffUsers == nil {
		m.StaffUsers = []string{}
	}
	if m.StaffRoles == nil {
		m.StaffRoles = []string{}
	}
	if m.StaffBranches == nil {
		m.StaffBranches = []string{}
	}
	return m
}

// MobileSettingsFor is the settings screen; Admin and the compliance
// officer see it, only an Admin changes it.
func (o *Ops) MobileSettingsFor(ctx context.Context, tenant, actor string) (MobileSettingsView, error) {
	if err := o.require(ctx, tenant, actor, "Viewing the mobile app settings", RoleCompliance, RoleAdmin); err != nil {
		return MobileSettingsView{}, err
	}
	v := MobileSettingsView{MobileSettings: o.mobileSettings(ctx, tenant), Roles: staffAppRoles(), Branches: []string{}}
	if bs, err := o.Branches(ctx, tenant); err == nil {
		for _, b := range bs {
			v.Branches = append(v.Branches, b.Code)
		}
	}
	if us, err := o.Users(ctx, tenant); err == nil {
		for _, u := range us {
			if u.Role == RoleBranch && u.Branch != "" && !contains(v.Branches, u.Branch) {
				v.Branches = append(v.Branches, u.Branch)
			}
		}
	}
	sort.Strings(v.Branches)
	r, _ := o.RoleOf(ctx, tenant, actor)
	v.CanEdit = r == RoleAdmin
	return v, nil
}

func staffAppRoles() []string {
	return []string{RoleMaker, RoleChecker, RoleRisk, RoleCompliance, RoleBranch, RoleAP, RoleHR, RolePayroll, RoleAdmin}
}

var (
	reHexColour = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
	reLogoData  = regexp.MustCompile(`^data:image/(png|jpeg);base64,[A-Za-z0-9+/=]+$`)
)

// SaveMobileSettings stores Settings → Staff app and Client app.
func (o *Ops) SaveMobileSettings(ctx context.Context, tenant, actor string, m MobileSettings) (MobileSettingsView, error) {
	if err := o.require(ctx, tenant, actor, "Changing the mobile app settings", RoleAdmin); err != nil {
		return MobileSettingsView{}, err
	}
	if IsSupportActor(actor) {
		return MobileSettingsView{}, fmt.Errorf("%w: Ledgerline support cannot change who may use the mobile apps", ErrForbidden)
	}
	us, err := o.Users(ctx, tenant)
	if err != nil {
		return MobileSettingsView{}, err
	}
	known := map[string]User{}
	for _, u := range us {
		known[u.ID] = u
	}
	clean := func(xs []string, lower bool) []string {
		out := []string{}
		for _, x := range xs {
			x = strings.TrimSpace(x)
			if lower {
				x = strings.ToLower(x)
			}
			if x != "" && !contains(out, x) {
				out = append(out, x)
			}
		}
		sort.Strings(out)
		return out
	}
	m.StaffUsers, m.StaffRoles, m.StaffBranches = clean(m.StaffUsers, true), clean(m.StaffRoles, false), clean(m.StaffBranches, false)
	for _, u := range m.StaffUsers {
		k, ok := known[u]
		if !ok {
			return MobileSettingsView{}, invalid("%s is not a user of this firm", u)
		}
		if k.Role == RoleClient {
			return MobileSettingsView{}, invalid("%s is a client login: clients use the client app", u)
		}
	}
	for _, r := range m.StaffRoles {
		if _, own := o.roleDef(ctx, tenant, r); !contains(staffAppRoles(), r) && (!own || contains(Roles, r)) { // built-in, or one the firm made (roles.go)
			return MobileSettingsView{}, invalid("unknown role %q", r)
		}
	}
	if len(m.StaffBranches) > 0 {
		// A branch of the register, or one a branch login names.
		bs, _ := o.Branches(ctx, tenant)
		for _, b := range m.StaffBranches {
			found := false
			for _, x := range bs {
				found = found || x.Code == b
			}
			for _, u := range us {
				found = found || (u.Role == RoleBranch && u.Branch == b)
			}
			if !found {
				return MobileSettingsView{}, invalid("no branch %s", b)
			}
		}
	}
	if m.IdleMinutes == 0 {
		m.IdleMinutes = mobileIdleDefault
	}
	if m.IdleMinutes < 1 || m.IdleMinutes > 30 {
		return MobileSettingsView{}, invalid("the app's idle time-out is 1 to 30 minutes")
	}
	if m.LockSeconds < 0 || m.LockSeconds > 600 {
		return MobileSettingsView{}, invalid("lock the app after 0 to 600 seconds in the background")
	}
	if m.ReauthDays == 0 {
		m.ReauthDays = mobileReauthDefault
	}
	if m.ReauthDays < 1 || m.ReauthDays > 90 {
		return MobileSettingsView{}, invalid("ask for the password again after 1 to 90 days")
	}
	if err := checkClientBrand(&m.Client); err != nil {
		return MobileSettingsView{}, err
	}
	old := o.mobileSettings(ctx, tenant)
	m.UpdatedAt, m.UpdatedBy = o.now().Format(time.RFC3339), actor
	if err := docs.Upsert(ctx, o.Docs, tenant, cMobileSet, "main", m, actor); err != nil {
		return MobileSettingsView{}, err
	}
	o.log(ctx, tenant, actor, "mobile.settings", "main", map[string]any{"staff_on": m.StaffOn, "staff_users": m.StaffUsers, "staff_roles": m.StaffRoles,
		"staff_branches": m.StaffBranches, "idle_minutes": m.IdleMinutes, "reauth_days": m.ReauthDays, "client_on": m.Client.On})
	if old.StaffOn && !m.StaffOn {
		o.log(ctx, tenant, actor, "mobile.staff_app_off", "main", nil)
	}
	return o.MobileSettingsFor(ctx, tenant, actor)
}

func checkClientBrand(b *ClientAppBrand) error {
	b.AppName = strings.TrimSpace(b.AppName)
	if len([]rune(b.AppName)) > 30 {
		return invalid("the app name is at most 30 characters (it shows under the icon)")
	}
	if b.On && b.AppName == "" {
		return invalid("give the client app a name before switching it on")
	}
	for _, c := range []*string{&b.Primary, &b.Accent} {
		*c = strings.TrimSpace(*c)
		if *c != "" && !reHexColour.MatchString(*c) {
			return invalid("colours are written #RRGGBB, for example #1f4fd1")
		}
	}
	if b.Logo != "" {
		if len(b.Logo) > 200_000 || !reLogoData.MatchString(b.Logo) {
			return invalid("the logo must be a PNG or JPEG of at most 150 KB")
		}
	}
	for _, u := range []*string{&b.InvestorCharterURL, &b.RiskDisclosureURL, &b.WebsiteURL} {
		*u = strings.TrimSpace(*u)
		if *u == "" {
			continue
		}
		p, err := url.Parse(*u)
		if err != nil || p.Scheme != "https" || p.Host == "" {
			return invalid("links must start with https://")
		}
	}
	for _, s := range []*string{&b.DPReg, &b.RegisteredOffice, &b.SupportEmail, &b.SupportPhone, &b.GrievanceOfficer, &b.GrievanceEmail, &b.GrievancePhone, &b.ComplianceOfficer} {
		*s = strings.TrimSpace(*s)
		if len(*s) > 300 {
			return invalid("keep each contact line under 300 characters")
		}
	}
	for _, e := range []string{b.SupportEmail, b.GrievanceEmail} {
		if e != "" && !reEmail.MatchString(e) {
			return invalid("%s is not an email address", e)
		}
	}
	return nil
}

// StaffAppAccess says whether a user may use the staff app, and if not,
// why, in words for the person.
func (o *Ops) StaffAppAccess(ctx context.Context, tenant, user string) error {
	m := o.mobileSettings(ctx, tenant)
	if !m.StaffOn {
		return fmt.Errorf("%w: the staff app is switched off for this firm; an Admin switches it on in Settings → Staff app", ErrForbidden)
	}
	u, err := o.user(ctx, tenant, user)
	if errors.Is(err, vault.ErrKeyUnavailable) {
		return err
	}
	if err != nil || !u.Active || u.Role == RoleClient {
		return fmt.Errorf("%w: the staff app is for the firm's staff", ErrForbidden)
	}
	if contains(m.StaffUsers, u.ID) || (u.scope() == RoleBranch && u.Branch != "" && contains(m.StaffBranches, u.Branch)) {
		return nil
	}
	for _, r := range u.roles() { // every role held (roles.go)
		if contains(m.StaffRoles, r) {
			return nil
		}
	}
	return fmt.Errorf("%w: you are not on the staff app's list; ask your Admin (Settings → Staff app)", ErrForbidden)
}

// ---- devices ----

// MobileDevice is a registered phone.
type MobileDevice struct {
	ID         string `json:"id"`
	User       string `json:"user"`
	App        string `json:"app"`      // staff, client
	Platform   string `json:"platform"` // ios, android
	Name       string `json:"name"`
	Model      string `json:"model,omitempty"`
	AppVersion string `json:"app_version,omitempty"`
	// Rooted: the app found signs the phone is rooted or jailbroken when
	// it was registered (the person was warned).
	Rooted     bool   `json:"rooted,omitempty"`
	PushToken  string `json:"push_token,omitempty"`
	SecretHash string `json:"secret_hash"`
	Epoch      int    `json:"epoch"`
	SignedInAt string `json:"signed_in_at"` // the last password sign-in on this phone
	LastSeen   string `json:"last_seen"`
	Created    string `json:"created"`
	Status     string `json:"status"` // Active, Revoked
	Failures   int    `json:"failures,omitempty"`
	RevokedAt  string `json:"revoked_at,omitempty"`
	RevokedBy  string `json:"revoked_by,omitempty"`
	RevokeNote string `json:"revoke_note,omitempty"`
}

// DeviceView is a device for a screen: never the secret's hash, and only
// whether it can get pushes, not the token.
type DeviceView struct {
	ID         string `json:"id"`
	User       string `json:"user"`
	UserName   string `json:"user_name,omitempty"`
	App        string `json:"app"`
	Platform   string `json:"platform"`
	Name       string `json:"name"`
	Model      string `json:"model,omitempty"`
	AppVersion string `json:"app_version,omitempty"`
	Rooted     bool   `json:"rooted,omitempty"`
	Push       bool   `json:"push"`
	SignedInAt string `json:"signed_in_at"`
	LastSeen   string `json:"last_seen"`
	Created    string `json:"created"`
	Status     string `json:"status"`
	RevokedAt  string `json:"revoked_at,omitempty"`
	RevokedBy  string `json:"revoked_by,omitempty"`
	RevokeNote string `json:"revoke_note,omitempty"`
	ReauthBy   string `json:"reauth_by,omitempty"` // the date the password is needed again
}

func (d MobileDevice) view(reauthDays int) DeviceView {
	v := DeviceView{ID: d.ID, User: d.User, App: d.App, Platform: d.Platform, Name: d.Name, Model: d.Model, AppVersion: d.AppVersion, Rooted: d.Rooted,
		Push: d.PushToken != "", SignedInAt: d.SignedInAt, LastSeen: d.LastSeen, Created: d.Created, Status: d.Status, RevokedAt: d.RevokedAt, RevokedBy: d.RevokedBy, RevokeNote: d.RevokeNote}
	if t, err := time.Parse(time.RFC3339, d.SignedInAt); err == nil && d.Status == "Active" {
		v.ReauthBy = t.AddDate(0, 0, reauthDays).Format("2006-01-02")
	}
	return v
}

// DeviceReq is what the app sends when it registers a phone.
type DeviceReq struct {
	App        string `json:"app"`
	Platform   string `json:"platform"`
	Name       string `json:"name"`
	Model      string `json:"model"`
	AppVersion string `json:"app_version"`
	PushToken  string `json:"push_token"`
	Rooted     bool   `json:"rooted"`
}

var rePushToken = regexp.MustCompile(`^[A-Za-z0-9_:\-.]{20,1000}$`)

func cleanLine(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
	if r := []rune(s); len(r) > n {
		s = string(r[:n])
	}
	return s
}

// appFor is the app a user's role signs in to.
func appFor(role string) string {
	if role == RoleClient {
		return AppClient
	}
	return AppStaff
}

// mobileAppOpen checks the app is on and open to the user.
func (o *Ops) mobileAppOpen(ctx context.Context, tenant, user, app string) error {
	if app == AppClient {
		if !o.mobileSettings(ctx, tenant).Client.On {
			return fmt.Errorf("%w: the client app is not switched on for this firm", ErrForbidden)
		}
		return nil
	}
	return o.StaffAppAccess(ctx, tenant, user)
}

// RegisterDevice adds the phone the signed-in user is using and returns
// it with the device secret, which is shown this once (the app keeps it in
// the keychain or keystore). tok is the session the registration is made
// from: a password sign-in in the last 15 minutes. The session is bound to
// the device, so the app's short idle time-out applies to it from now on.
func (o *Ops) RegisterDevice(ctx context.Context, tenant, actor, tok string, req DeviceReq) (DeviceView, string, error) {
	role, err := o.RoleOf(ctx, tenant, actor)
	if err != nil {
		return DeviceView{}, "", err
	}
	if IsSupportActor(actor) {
		return DeviceView{}, "", fmt.Errorf("%w: Ledgerline support cannot register phones", ErrForbidden)
	}
	req.App = strings.ToLower(strings.TrimSpace(req.App))
	if req.App != AppStaff && req.App != AppClient {
		return DeviceView{}, "", invalid("app is staff or client")
	}
	if req.App != appFor(role) {
		if role == RoleClient {
			return DeviceView{}, "", fmt.Errorf("%w: a client login uses the client app", ErrForbidden)
		}
		return DeviceView{}, "", fmt.Errorf("%w: staff use the staff app", ErrForbidden)
	}
	if err := o.mobileAppOpen(ctx, tenant, actor, req.App); err != nil {
		return DeviceView{}, "", err
	}
	req.Platform = strings.ToLower(strings.TrimSpace(req.Platform))
	if req.Platform != "ios" && req.Platform != "android" {
		return DeviceView{}, "", invalid("platform is ios or android")
	}
	req.Name = cleanLine(req.Name, 60)
	if req.Name == "" {
		req.Name = map[string]string{"ios": "iPhone", "android": "Android phone"}[req.Platform]
	}
	req.Model, req.AppVersion = cleanLine(req.Model, 60), cleanLine(req.AppVersion, 20)
	if req.PushToken != "" && !rePushToken.MatchString(req.PushToken) {
		return DeviceView{}, "", invalid("the push token is not one the phone's push service gives")
	}
	// The session: the user's own, from a password sign-in, recent.
	sid := tokenID(tok)
	s, ver, err := docs.Get[Session](ctx, o.Docs, tenant, cSession, sid)
	if err != nil || s.User != actor || s.Revoked {
		return DeviceView{}, "", fmt.Errorf("%w: sign in with your password on the phone to register it", ErrForbidden)
	}
	now := o.now()
	created, _ := time.Parse(time.RFC3339, s.Created)
	if s.Device != "" || now.Sub(created) > registerWithin {
		return DeviceView{}, "", fmt.Errorf("%w: sign in again with your password to register this phone", ErrForbidden)
	}
	c, _, err := o.cred(ctx, tenant, actor)
	if err != nil {
		return DeviceView{}, "", err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return DeviceView{}, "", err
	}
	secret := base64.RawURLEncoding.EncodeToString(raw)
	idb := make([]byte, 8)
	rand.Read(idb)
	d := MobileDevice{ID: "MD-" + hex.EncodeToString(idb), User: actor, App: req.App, Platform: req.Platform, Name: req.Name, Model: req.Model, AppVersion: req.AppVersion,
		Rooted: req.Rooted, PushToken: req.PushToken, SecretHash: secretHash(secret), Epoch: c.Epoch, SignedInAt: now.Format(time.RFC3339),
		LastSeen: now.Format(time.RFC3339), Created: now.Format(time.RFC3339), Status: "Active"}
	// At most five phones a person: the oldest goes.
	mine, err := o.devicesOf(ctx, tenant, actor)
	if err != nil {
		return DeviceView{}, "", err
	}
	for len(mine) >= maxDevicesPerUser {
		old := mine[0]
		mine = mine[1:]
		if _, err := o.revokeDevice(ctx, tenant, actor, old.ID, "replaced by a newer phone"); err != nil {
			return DeviceView{}, "", err
		}
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cMobileDev, d.ID, d, actor); err != nil {
		return DeviceView{}, "", err
	}
	s.Device = d.ID
	docs.Put(ctx, o.Docs, tenant, cSession, sid, s, ver, actor) // best effort: the next request may have touched it
	o.log(ctx, tenant, actor, "mobile.device_registered", d.ID, map[string]any{"app": d.App, "platform": d.Platform, "name": d.Name, "push": d.PushToken != "", "rooted": d.Rooted})
	if d.Rooted {
		o.SecurityEvent(ctx, tenant, SecEvent{Kind: "mobile.rooted_device", Actor: actor, Subject: actor, Message: fmt.Sprintf("%s registered a phone that looks rooted or jailbroken (%s, %s)", actor, d.Platform, d.Name)})
	}
	return d.view(o.mobileSettings(ctx, tenant).ReauthDays), secret, nil
}

func secretHash(s string) string {
	h := sha256.Sum256([]byte("ledgerline-device:" + s))
	return hex.EncodeToString(h[:])
}

// devicesOf lists a user's active devices, oldest first.
func (o *Ops) devicesOf(ctx context.Context, tenant, user string) ([]MobileDevice, error) {
	all, err := docs.List[MobileDevice](ctx, o.Docs, tenant, cMobileDev, "")
	if err != nil {
		return nil, err
	}
	var out []MobileDevice
	for _, d := range all {
		if d.User == user && d.Status == "Active" {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created < out[j].Created })
	return out, nil
}

// MobileDevices lists phones: the user's own; with all set, an Admin sees every
// staff phone (all = "staff") or one person's (all = a user ID).
func (o *Ops) MobileDevices(ctx context.Context, tenant, actor, all string) ([]DeviceView, error) {
	if all != "" {
		if err := o.require(ctx, tenant, actor, "Viewing the firm's registered phones", RoleAdmin); err != nil {
			return nil, err
		}
	}
	ds, err := docs.List[MobileDevice](ctx, o.Docs, tenant, cMobileDev, "")
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	if us, err := o.Users(ctx, tenant); err == nil {
		for _, u := range us {
			names[u.ID] = u.Name
		}
	}
	m := o.mobileSettings(ctx, tenant)
	out := []DeviceView{}
	for _, d := range ds {
		switch {
		case all == "":
			if d.User != actor {
				continue
			}
		case all == AppStaff:
			if d.App != AppStaff {
				continue
			}
		default:
			if d.User != strings.ToLower(all) {
				continue
			}
		}
		v := d.view(m.ReauthDays)
		v.UserName = names[d.User]
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		if (out[i].Status == "Active") != (out[j].Status == "Active") {
			return out[i].Status == "Active"
		}
		return out[i].LastSeen > out[j].LastSeen
	})
	return out, nil
}

// RevokeDevice stops a phone working: its owner can, and an Admin can
// revoke any phone (a lost staff phone, or a client who calls the help
// desk). Its sessions end at their next request.
func (o *Ops) RevokeDevice(ctx context.Context, tenant, actor, id, note string) (DeviceView, error) {
	d, _, err := docs.Get[MobileDevice](ctx, o.Docs, tenant, cMobileDev, id)
	if err != nil {
		return DeviceView{}, ErrNotFound
	}
	if d.User != actor {
		if err := o.require(ctx, tenant, actor, "Revoking another person's phone", RoleAdmin); err != nil {
			return DeviceView{}, ErrNotFound // others' phones are not even listed
		}
		if IsSupportActor(actor) {
			return DeviceView{}, fmt.Errorf("%w: Ledgerline support cannot revoke phones", ErrForbidden)
		}
		if strings.TrimSpace(note) == "" {
			return DeviceView{}, invalid("say why the phone is revoked (lost, left the firm…)")
		}
	}
	d, err = o.revokeDevice(ctx, tenant, actor, id, firstOf(cleanLine(note, 200), "removed from the app"))
	return d.view(o.mobileSettings(ctx, tenant).ReauthDays), err
}

func (o *Ops) revokeDevice(ctx context.Context, tenant, actor, id, note string) (MobileDevice, error) {
	d, err := docs.Update(ctx, o.Docs, tenant, cMobileDev, id, actor, func(d *MobileDevice) error {
		if d.Status != "Active" {
			return fmt.Errorf("%w: this phone is already revoked", ErrState)
		}
		d.Status, d.RevokedAt, d.RevokedBy, d.RevokeNote, d.PushToken = "Revoked", o.now().Format(time.RFC3339), actor, note, ""
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "mobile.device_revoked", id, map[string]any{"user": d.User, "app": d.App, "note": note})
	}
	return d, err
}

// SetPushToken records the phone's new push token (the push service
// changes it now and then). Only the phone's owner, from the phone.
func (o *Ops) SetPushToken(ctx context.Context, tenant, actor, id, token string) (DeviceView, error) {
	token = strings.TrimSpace(token)
	if token != "" && !rePushToken.MatchString(token) {
		return DeviceView{}, invalid("the push token is not one the phone's push service gives")
	}
	d, err := docs.Update(ctx, o.Docs, tenant, cMobileDev, id, actor, func(d *MobileDevice) error {
		if d.User != actor {
			return ErrNotFound
		}
		if d.Status != "Active" {
			return fmt.Errorf("%w: this phone is revoked; sign in again", ErrState)
		}
		d.PushToken = token
		return nil
	})
	if err != nil {
		return DeviceView{}, err
	}
	o.log(ctx, tenant, actor, "mobile.push_token", id, map[string]any{"push": token != ""}) // never the token
	return d.view(o.mobileSettings(ctx, tenant).ReauthDays), nil
}

// ErrDeviceRevoked is an unlock with a phone that no longer works; the
// app forgets its secret and asks for the password.
var ErrDeviceRevoked = errors.New("this phone has been signed out; sign in with your password")

// ErrReauth is an unlock after ReauthDays without the password.
var ErrReauth = errors.New("sign in with your password: it is a while since you last did on this phone")

// Unlock opens an app session with a device secret (after the phone's
// biometric check released it). It returns the session token.
func (o *Ops) Unlock(ctx context.Context, tenant, id, secret, ip, agent string) (string, AuthState, MobileDevice, error) {
	d, ver, err := docs.Get[MobileDevice](ctx, o.Docs, tenant, cMobileDev, id)
	if errors.Is(err, vault.ErrKeyUnavailable) {
		return "", AuthState{}, d, err
	}
	if err != nil || d.Status != "Active" {
		return "", AuthState{}, d, ErrDeviceRevoked
	}
	now := o.now()
	if subtle.ConstantTimeCompare([]byte(secretHash(secret)), []byte(d.SecretHash)) != 1 {
		d.Failures++
		revoke := d.Failures >= maxUnlockFailures
		docs.Put(ctx, o.Docs, tenant, cMobileDev, id, d, ver, d.User)
		o.log(ctx, tenant, d.User, "mobile.unlock_failed", id, map[string]any{"ip": ip, "failures": d.Failures})
		if revoke {
			o.revokeDevice(ctx, tenant, SystemActor, id, "five wrong unlock attempts")
			o.SecurityEvent(ctx, tenant, SecEvent{Kind: "mobile.unlock_failed", Actor: d.User, Subject: d.User, IP: ip, Message: "A phone of " + d.User + " was revoked after five wrong unlock attempts"})
		}
		return "", AuthState{}, d, ErrDeviceRevoked
	}
	m := o.mobileSettings(ctx, tenant)
	u, uerr := o.user(ctx, tenant, d.User)
	c, _, cerr := o.cred(ctx, tenant, d.User)
	if uerr != nil || cerr != nil || !u.Active || c.Epoch != d.Epoch || appFor(u.Role) != d.App {
		o.revokeDevice(ctx, tenant, SystemActor, id, "password or two-factor reset, or user deactivated")
		return "", AuthState{}, d, ErrDeviceRevoked
	}
	if err := o.mobileAppOpen(ctx, tenant, d.User, d.App); err != nil {
		return "", AuthState{}, d, err
	}
	signed, _ := time.Parse(time.RFC3339, d.SignedInAt)
	if now.Sub(signed) > time.Duration(m.ReauthDays)*24*time.Hour {
		return "", AuthState{}, d, ErrReauth
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", AuthState{}, d, err
	}
	tok := base64.RawURLEncoding.EncodeToString(raw)
	s := Session{User: d.User, Created: now.Format(time.RFC3339), LastSeen: now.Format(time.RFC3339), Expires: now.Add(sessionMaxAge).Format(time.RFC3339), Epoch: c.Epoch, IP: ip, Agent: trim(agent, 200), Device: d.ID}
	if _, err := docs.Put(ctx, o.Docs, tenant, cSession, tokenID(tok), s, 0, d.User); err != nil {
		return "", AuthState{}, d, err
	}
	d.Failures, d.LastSeen = 0, now.Format(time.RFC3339)
	docs.Put(ctx, o.Docs, tenant, cMobileDev, id, d, ver, d.User) // best effort
	o.log(ctx, tenant, d.User, "mobile.unlock", id, map[string]any{"ip": ip, "app": d.App})
	st, err := o.authState(ctx, tenant, d.User)
	return tok, st, d, err
}

// mobileSessionIdle is how long an app session may be idle, and whether
// its device still works.
func (o *Ops) mobileSessionIdle(ctx context.Context, tenant, device string) (time.Duration, bool) {
	d, _, err := docs.Get[MobileDevice](ctx, o.Docs, tenant, cMobileDev, device)
	if err != nil || d.Status != "Active" {
		return 0, false
	}
	return time.Duration(o.mobileSettings(ctx, tenant).IdleMinutes) * time.Minute, true
}

// SessionDevice is the device a session token belongs to ("" for a
// browser).
func (o *Ops) SessionDevice(ctx context.Context, tenant, tok string) string {
	s, _, err := docs.Get[Session](ctx, o.Docs, tenant, cSession, tokenID(tok))
	if err != nil {
		return ""
	}
	return s.Device
}

// ---- what the apps learn before sign-in ----

// MobileAppCfg is what /m/ and /app/ show on the sign-in screen: the firm's
// name and, for the client app, its brand and the disclosures a broker app
// must carry. Nothing about any client.
type MobileAppCfg struct {
	Firm        string            `json:"firm"`
	Tenant      string            `json:"tenant"`
	App         string            `json:"app"`
	On          bool              `json:"on"`
	IdleMinutes int               `json:"idle_minutes"`
	LockSeconds int               `json:"lock_seconds"`
	ReauthDays  int               `json:"reauth_days"`
	Date        string            `json:"date"` // the business date the apps show
	Brand       *ClientAppBrand   `json:"brand,omitempty"`
	SEBIReg     string            `json:"sebi_reg,omitempty"`
	MemberCodes map[string]string `json:"member_codes,omitempty"`
	Compliance  string            `json:"compliance_email,omitempty"`
	// Links every Indian broker app shows: SEBI SCORES and the Online
	// Dispute Resolution portal (SEBI circular of 31 July 2023).
	ScoresURL string `json:"scores_url,omitempty"`
	ODRURL    string `json:"odr_url,omitempty"`
}

// MobileDate is the date the apps show when they name none: today, or the
// last business day on a holiday or weekend.
func (o *Ops) MobileDate(ctx context.Context, tenant string) string {
	return o.businessDate(ctx, tenant)
}

// MobileAppConfig builds the pre-sign-in configuration of an app.
func (o *Ops) MobileAppConfig(ctx context.Context, tenant, app string) (MobileAppCfg, error) {
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return MobileAppCfg{}, err
	}
	m := o.mobileSettings(ctx, tenant)
	v := MobileAppCfg{Firm: set.Firm, Tenant: tenant, App: app, IdleMinutes: m.IdleMinutes, LockSeconds: m.LockSeconds, ReauthDays: m.ReauthDays, Date: o.businessDate(ctx, tenant)}
	if app == AppClient {
		b := m.Client
		if b.AppName == "" {
			b.AppName = set.Firm
		}
		v.On, v.Brand = b.On, &b
		v.SEBIReg, v.MemberCodes, v.Compliance = set.SEBIReg, set.MemberCodes, set.ComplianceEmail
		v.ScoresURL, v.ODRURL = "https://scores.sebi.gov.in/", "https://smartodr.in/login"
		return v, nil
	}
	v.On = m.StaffOn
	return v, nil
}
