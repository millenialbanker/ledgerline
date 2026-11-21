package ops

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
)

// ---- attendance: punches, phone check-in, kiosk, hybrid work ----
//
// A day's attendance comes from punches (in and out), made:
//   - from the phone app (POST /v1/hr/attendance/punch): one registered
//     phone per person, bound on first use; a different phone needs an HR
//     manager's approval. The app unlocks with the phone's own biometric
//     before it punches; Ledgerline only receives the app's statement that
//     it did (biometric_unlocked) and never receives or stores biometrics.
//   - from the console (web check-in);
//   - at a tablet kiosk at the office entrance (employee code and PIN, a
//     photo for the manager);
//   - by the manager, for a day that was missed;
//   - from any attendance device's CSV export (a fallback).
//
// Ways of working (modes): office, home, field (client visit) and on duty.
// An office punch is accepted only from the office network (the offices'
// networks in the HR policy, and the sign-in "firm's networks" when the
// policy says so) or within the geofence of an office (100 m by default).
// Home is allowed only where the person's work policy allows it on that
// day or with an approved work-from-home request; a field punch needs the
// location and a note. A person who deals on exchange terminals may not
// work from home unless the firm has recorded that their home is a dealing
// location the exchange approved (Employee.HomeDealing): dealing from a
// location the exchange has not approved needs the exchange's permission.
//
// A punch may carry a photo, kept for the manager's review only (no face
// matching), and deleted after the policy's days (90 by default).

const (
	cAttend  = "hr_attend"
	cDevice  = "hr_device"
	cKiosk   = "hr_kiosk"
	cPIN     = "hr_pin"
	cSelfie  = "hr_selfie"
	cWFH     = "hr_wfh"
	maxPhoto = 2 << 20
	// PINTries is how many wrong PINs lock a person's kiosk PIN.
	PINTries = 5
)

// PunchModes are the ways of working a punch records.
var PunchModes = []string{"office", "home", "field", "onduty"}

// Punch is one in or out.
type Punch struct {
	At     string  `json:"at"`   // RFC3339
	Kind   string  `json:"kind"` // in, out
	Mode   string  `json:"mode"`
	Source string  `json:"source"` // phone, web, kiosk, device, manager
	Lat    float64 `json:"lat,omitempty"`
	Lng    float64 `json:"lng,omitempty"`
	HasLoc bool    `json:"has_loc,omitempty"`
	Site   string  `json:"site,omitempty"` // the office it was accepted at
	How    string  `json:"how,omitempty"`  // network or geofence (office punches)
	IP     string  `json:"ip,omitempty"`
	Note   string  `json:"note,omitempty"`
	Selfie string  `json:"selfie,omitempty"` // photo ID
	By     string  `json:"by,omitempty"`
}

// Attendance is one person's day.
type Attendance struct {
	ID       string  `json:"id"` // code|date
	Employee string  `json:"employee"`
	Date     string  `json:"date"`
	Punches  []Punch `json:"punches"`
	In       string  `json:"in,omitempty"`  // HH:MM
	Out      string  `json:"out,omitempty"` // HH:MM
	Mode     string  `json:"mode,omitempty"`
	Status   string  `json:"status"`           // Present, Half day, Absent, On duty
	Marked   string  `json:"marked,omitempty"` // a manager's or HR's mark overrides the punches
	Note     string  `json:"note,omitempty"`
}

// settle works out the day from its punches (unless a person marked it).
func (a *Attendance) settle(sh Shift) {
	if a.Marked != "" {
		return
	}
	sort.SliceStable(a.Punches, func(i, j int) bool { return a.Punches[i].At < a.Punches[j].At })
	a.In, a.Out, a.Mode = "", "", ""
	var tin, tout time.Time
	for _, p := range a.Punches {
		t, err := time.Parse(time.RFC3339, p.At)
		if err != nil {
			continue
		}
		t = t.In(ist)
		if p.Kind == "in" && a.In == "" {
			a.In, a.Mode, tin = t.Format("15:04"), p.Mode, t
		}
		if p.Kind == "out" {
			a.Out, tout = t.Format("15:04"), t
		}
	}
	switch {
	case a.In == "":
		a.Status = "Absent"
	case a.Mode == "onduty":
		a.Status = "On duty"
	default:
		a.Status = "Present"
		if !tout.IsZero() {
			if h, err := parseDays(sh.HalfHours); err == nil && h > 0 && tout.Sub(tin).Hours() < h {
				a.Status = "Half day"
			}
		}
	}
}

// policyFor is the person's work policy: their own, else one for their
// department, else the first.
func policyFor(p HRPolicy, e Employee) WorkPolicy {
	for _, w := range p.WorkPolicies {
		if w.ID == e.WorkPolicy && e.WorkPolicy != "" {
			return w
		}
	}
	for _, w := range p.WorkPolicies {
		if contains(w.Departments, e.Department) {
			return w
		}
	}
	if len(p.WorkPolicies) > 0 {
		return p.WorkPolicies[0]
	}
	return WorkPolicy{ID: "OFFICE", Modes: []string{"office", "field", "onduty"}}
}

func shiftFor(p HRPolicy, e Employee) Shift {
	for _, s := range p.Shifts {
		if s.Code == e.Shift {
			return s
		}
	}
	return p.Shifts[0]
}

// dayKind: "working", "weekly off" or the holiday's name, for a person.
func (o *Ops) dayKind(p HRPolicy, set Settings, e Employee, day string) string {
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		return "working"
	}
	for _, h := range p.Holidays {
		if h.Date == day && (len(h.Branches) == 0 || contains(h.Branches, e.Branch)) {
			return h.Name
		}
	}
	if p.ExchangeHols {
		if ok, name := set.IsHoliday(day); ok {
			return firstOf(name, "Holiday")
		}
	}
	if contains(shiftFor(p, e).WeeklyOff, t.Weekday().String()[:3]) {
		return "weekly off"
	}
	return "working"
}

// officeNets are the networks an office punch is accepted from.
func (o *Ops) officeNets(ctx context.Context, tenant string, p HRPolicy) []string {
	var nets []string
	for _, s := range p.Sites {
		nets = append(nets, s.Networks...)
	}
	if p.UseSignInNets {
		nets = append(nets, o.SecuritySettings(ctx, tenant).AllowedNetworks...)
	}
	return nets
}

func validNet(n string) bool {
	if _, _, err := net.ParseCIDR(n); err == nil {
		return true
	}
	return net.ParseIP(n) != nil
}

// metres between two points (haversine).
func metres(lat1, lng1, lat2, lng2 float64) float64 {
	const r = 6371000.0
	rad := math.Pi / 180
	dl, dg := (lat2-lat1)*rad, (lng2-lng1)*rad
	a := math.Sin(dl/2)*math.Sin(dl/2) + math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dg/2)*math.Sin(dg/2)
	return 2 * r * math.Asin(math.Sqrt(a))
}

// atOffice checks an office punch: the network first, then the geofence
// of each office that has coordinates. It returns the office and how.
func (o *Ops) atOffice(ctx context.Context, tenant string, p HRPolicy, ip string, hasLoc bool, lat, lng float64) (string, string, bool) {
	for _, s := range p.Sites {
		if ip != "" && len(s.Networks) > 0 && inNetworks(ip, s.Networks) {
			return firstOf(s.Branch, "Head office"), "network", true
		}
	}
	if ip != "" && p.UseSignInNets {
		if nets := o.SecuritySettings(ctx, tenant).AllowedNetworks; len(nets) > 0 && inNetworks(ip, nets) {
			return "Head office", "network", true
		}
	}
	if hasLoc {
		for _, s := range p.Sites {
			if s.Lat == 0 && s.Lng == 0 {
				continue
			}
			r := s.RadiusM
			if r <= 0 {
				r = 100
			}
			if metres(lat, lng, s.Lat, s.Lng) <= float64(r) {
				return firstOf(s.Branch, "Head office"), "geofence", true
			}
		}
	}
	return "", "", false
}

// PunchReq is a punch as the phone app, the console or the kiosk sends it.
type PunchReq struct {
	Kind     string  `json:"kind"` // in, out
	Mode     string  `json:"mode"` // office, home, field, onduty
	Lat      float64 `json:"lat,omitempty"`
	Lng      float64 `json:"lng,omitempty"`
	HasLoc   bool    `json:"has_location,omitempty"`
	DeviceID string  `json:"device_id,omitempty"` // the phone's install ID (phone app)
	Unlocked bool    `json:"biometric_unlocked"`  // the app unlocked with the phone's biometric first
	Selfie   []byte  `json:"selfie,omitempty"`    // JPEG or PNG, optional
	Note     string  `json:"note,omitempty"`
	IP       string  `json:"-"`
	source   string
}

// PunchResult is what the person is told.
type PunchResult struct {
	Employee string `json:"employee"`
	Name     string `json:"name"`
	At       string `json:"at"`
	Kind     string `json:"kind"`
	Mode     string `json:"mode"`
	Site     string `json:"site,omitempty"`
	Status   string `json:"status"`
}

// PunchAttendance is a punch by the signed-in person, from the phone app
// (device_id set) or the console.
func (o *Ops) PunchAttendance(ctx context.Context, tenant, actor string, r PunchReq) (PunchResult, error) {
	if err := o.needModule(ctx, tenant, ModHRMS); err != nil {
		return PunchResult{}, err
	}
	e, ok := o.employeeOf(ctx, tenant, actor)
	if !ok {
		return PunchResult{}, fmt.Errorf("%w: your login is not linked to an employee", ErrForbidden)
	}
	r.source = "web"
	if r.DeviceID != "" {
		r.source = "phone"
		if !r.Unlocked {
			return PunchResult{}, invalid("unlock the app with the phone's fingerprint or face first")
		}
		if err := o.bindDevice(ctx, tenant, actor, e, r.DeviceID); err != nil {
			return PunchResult{}, err
		}
	}
	return o.punch(ctx, tenant, actor, e, r)
}

func (o *Ops) punch(ctx context.Context, tenant, actor string, e Employee, r PunchReq) (PunchResult, error) {
	now := o.now()
	day := now.Format("2006-01-02")
	if !e.inService(day) {
		return PunchResult{}, fmt.Errorf("%w: %s is not on the rolls today", ErrForbidden, e.Code)
	}
	if r.Kind != "in" && r.Kind != "out" {
		return PunchResult{}, invalid("kind is in or out")
	}
	if r.Mode == "" {
		r.Mode = "office"
	}
	if !contains(PunchModes, r.Mode) {
		return PunchResult{}, invalid("mode is %s", strings.Join(PunchModes, ", "))
	}
	p := o.hrPolicy(ctx, tenant)
	wp := policyFor(p, e)
	if !contains(wp.Modes, r.Mode) {
		return PunchResult{}, fmt.Errorf("%w: your work policy (%s) does not allow %s", ErrForbidden, firstOf(wp.Name, wp.ID), modeName(r.Mode))
	}
	pu := Punch{At: now.Format(time.RFC3339), Kind: r.Kind, Mode: r.Mode, Source: r.source, IP: r.IP, Note: trim(strings.TrimSpace(r.Note), 300), By: actor}
	if r.HasLoc {
		if r.Lat < -90 || r.Lat > 90 || r.Lng < -180 || r.Lng > 180 {
			return PunchResult{}, invalid("the location is out of range")
		}
		pu.Lat, pu.Lng, pu.HasLoc = r.Lat, r.Lng, true
	}
	switch r.Mode {
	case "office":
		site, how, ok := o.atOffice(ctx, tenant, p, r.IP, r.HasLoc, r.Lat, r.Lng)
		if !ok {
			return PunchResult{}, fmt.Errorf("%w: an office punch is accepted only on the office network or at the office (within its radius); you are not", ErrForbidden)
		}
		pu.Site, pu.How = site, how
	case "home":
		if r.Kind == "in" {
			if err := o.homeAllowed(ctx, tenant, p, wp, e, day); err != nil {
				return PunchResult{}, err
			}
		}
	case "field":
		if !r.HasLoc || pu.Note == "" {
			return PunchResult{}, invalid("a field punch needs your location and a note (whom you are visiting)")
		}
	case "onduty":
		if pu.Note == "" {
			return PunchResult{}, invalid("say where you are on duty")
		}
	}
	if len(r.Selfie) > 0 {
		id, err := o.keepPhoto(ctx, tenant, e.Code, day, r.Selfie, r.source)
		if err != nil {
			return PunchResult{}, err
		}
		pu.Selfie = id
	}
	a, err := o.addPunch(ctx, tenant, actor, e, day, pu, p)
	if err != nil {
		return PunchResult{}, err
	}
	o.log(ctx, tenant, actor, "hr.attendance.punch", e.Code, map[string]any{"kind": r.Kind, "mode": r.Mode, "source": r.source, "site": pu.Site, "how": pu.How, "photo": pu.Selfie != ""})
	return PunchResult{Employee: e.Code, Name: e.Name, At: pu.At, Kind: r.Kind, Mode: r.Mode, Site: pu.Site, Status: a.Status}, nil
}

func modeName(m string) string {
	return map[string]string{"office": "office", "home": "working from home", "field": "field or client visits", "onduty": "on duty"}[m]
}

func (o *Ops) addPunch(ctx context.Context, tenant, actor string, e Employee, day string, pu Punch, p HRPolicy) (Attendance, error) {
	id := e.Code + "|" + day
	sh := shiftFor(p, e)
	a, _, err := docs.Get[Attendance](ctx, o.Docs, tenant, cAttend, id)
	if errors.Is(err, docs.ErrNotFound) {
		a = Attendance{ID: id, Employee: e.Code, Date: day, Punches: []Punch{pu}}
		a.settle(sh)
		if _, err := docs.Put(ctx, o.Docs, tenant, cAttend, id, a, 0, actor); err == nil {
			return a, nil
		} else if !errors.Is(err, docs.ErrStale) {
			return a, err
		}
	} else if err != nil {
		return a, err
	}
	return docs.Update(ctx, o.Docs, tenant, cAttend, id, actor, func(x *Attendance) error {
		if len(x.Punches) >= 40 {
			return invalid("too many punches today")
		}
		x.Punches = append(x.Punches, pu)
		x.settle(sh)
		return nil
	})
}

// homeAllowed applies the work policy and the dealer rule to a home day.
func (o *Ops) homeAllowed(ctx context.Context, tenant string, p HRPolicy, wp WorkPolicy, e Employee, day string) error {
	if dealerHomeBlocked(p, e) {
		// Dealing on an exchange terminal from a location the exchange
		// has not approved needs the exchange's permission: the firm
		// records an approved home location before a dealer works from home.
		return fmt.Errorf("%w: you deal on exchange terminals and your home is not recorded as an exchange-approved dealing location; ask compliance", ErrForbidden)
	}
	if o.wfhApproved(ctx, tenant, e.Code, day) {
		return nil
	}
	t, _ := time.Parse("2006-01-02", day)
	if contains(wp.FixedHomeDays, t.Weekday().String()[:3]) {
		return nil
	}
	if wp.AnyHomePerWeek > 0 {
		n := 0
		for _, d := range weekOf(day) {
			if d == day {
				continue
			}
			if a, _, err := docs.Get[Attendance](ctx, o.Docs, tenant, cAttend, e.Code+"|"+d); err == nil && a.Mode == "home" {
				n++
			}
		}
		if n < wp.AnyHomePerWeek {
			return nil
		}
		return fmt.Errorf("%w: your policy allows %d home day(s) a week and you have used them; ask your manager for a work-from-home day", ErrForbidden, wp.AnyHomePerWeek)
	}
	return fmt.Errorf("%w: your work policy does not allow working from home today; ask your manager for a work-from-home day", ErrForbidden)
}

// dealerHomeBlocked: the person deals on exchange terminals, their home is
// not recorded as an exchange-approved dealing location, and the firm keeps
// the rule (HRPolicy.DealersHomeFree off, the default). The exchanges let
// a member's approved users trade only from locations the exchange has
// approved for the terminal (CTCL/terminal location rules); dealing from
// anywhere else needs the exchange's permission first.
func dealerHomeBlocked(p HRPolicy, e Employee) bool {
	return e.dealer() && !e.HomeDealing.Approved && !p.DealersHomeFree
}

// weekOf is the Monday-to-Sunday week of a day.
func weekOf(day string) []string {
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		return nil
	}
	off := (int(t.Weekday()) + 6) % 7
	start := t.AddDate(0, 0, -off)
	out := make([]string, 7)
	for i := range out {
		out[i] = start.AddDate(0, 0, i).Format("2006-01-02")
	}
	return out
}

// ---- phones ----

// Device is a person's registered phone.
type Device struct {
	Employee  string   `json:"employee"`
	DeviceID  string   `json:"device_id"`
	BoundAt   string   `json:"bound_at"`
	Pending   string   `json:"pending,omitempty"` // a new phone waiting for HR's approval
	PendingAt string   `json:"pending_at,omitempty"`
	History   []string `json:"history,omitempty"`
}

func devHash(id string) string {
	s := sha256.Sum256([]byte(strings.TrimSpace(id)))
	return hex.EncodeToString(s[:12])
}

// bindDevice binds the first phone; a different one waits for HR.
func (o *Ops) bindDevice(ctx context.Context, tenant, actor string, e Employee, dev string) error {
	if len(dev) < 8 || len(dev) > 200 {
		return invalid("the phone's ID is missing")
	}
	h := devHash(dev)
	d, _, err := docs.Get[Device](ctx, o.Docs, tenant, cDevice, e.Code)
	if errors.Is(err, docs.ErrNotFound) {
		d = Device{Employee: e.Code, DeviceID: h, BoundAt: o.now().Format(time.RFC3339)}
		if _, err := docs.Put(ctx, o.Docs, tenant, cDevice, e.Code, d, 0, actor); err != nil {
			return err
		}
		o.log(ctx, tenant, actor, "hr.device.bound", e.Code, map[string]any{"device": h})
		return nil
	} else if err != nil {
		return err
	}
	if d.DeviceID == h {
		return nil
	}
	if d.Pending != h {
		d.Pending, d.PendingAt = h, o.now().Format(time.RFC3339)
		_ = docs.Upsert(ctx, o.Docs, tenant, cDevice, e.Code, d, actor)
		o.log(ctx, tenant, actor, "hr.device.change_requested", e.Code, map[string]any{"device": h})
		o.raiseWork(ctx, tenant, wHR, e.Code+":device:"+h, fmt.Sprintf("%s (%s) wants to punch from a new phone", e.Name, e.Code),
			"Approve the new phone on People → Attendance → Phones if the person changed their phone; otherwise leave it.", "", RoleHR, "people", o.today())
	}
	return fmt.Errorf("%w: this is not your registered phone; HR must approve the new phone first (asked)", ErrForbidden)
}

// ApproveDevice moves a person's punches to the new phone. HR managers.
func (o *Ops) ApproveDevice(ctx context.Context, tenant, actor, code string) (Device, error) {
	if err := o.needModule(ctx, tenant, ModHRMS); err != nil {
		return Device{}, err
	}
	if err := o.require(ctx, tenant, actor, "Approving a new phone for attendance", RoleHR); err != nil {
		return Device{}, err
	}
	code = strings.ToUpper(code)
	d, err := docs.Update(ctx, o.Docs, tenant, cDevice, code, actor, func(d *Device) error {
		if d.Pending == "" {
			return fmt.Errorf("%w: no new phone is waiting", ErrState)
		}
		d.History = append(d.History, d.DeviceID)
		d.DeviceID, d.Pending, d.PendingAt, d.BoundAt = d.Pending, "", "", o.now().Format(time.RFC3339)
		return nil
	})
	if err == nil {
		o.closeWork(ctx, tenant, actor, wHR, code+":device:"+d.DeviceID, "approved")
		o.log(ctx, tenant, actor, "hr.device.approved", code, map[string]any{"device": d.DeviceID})
	}
	return d, err
}

// Devices lists registered phones and pending changes. HR managers.
func (o *Ops) Devices(ctx context.Context, tenant, actor string) ([]Device, error) {
	if err := o.needModule(ctx, tenant, ModHRMS); err != nil {
		return nil, err
	}
	if err := o.require(ctx, tenant, actor, "Seeing registered phones and kiosks", RoleHR); err != nil {
		return nil, err
	}
	ds, err := docs.List[Device](ctx, o.Docs, tenant, cDevice, "")
	if ds == nil {
		ds = []Device{}
	}
	return ds, err
}

// ---- photos ----

// PunchPhoto is a photo taken at a punch, kept for the manager's review.
type PunchPhoto struct {
	ID       string `json:"id"`
	Employee string `json:"employee"`
	Date     string `json:"date"`
	Source   string `json:"source"`
	File     string `json:"file"`
	Deleted  string `json:"deleted,omitempty"`
}

var rePhotoID = regexp.MustCompile(`^PH-[0-9a-f]{16}$`)

func (o *Ops) photoDir(tenant string) (string, error) {
	if !reTenantDir.MatchString(tenant) {
		return "", invalid("bad firm id %q", tenant)
	}
	w := o.WorkDir
	if w == "" {
		w = filepath.Join(os.TempDir(), "ledgerline-work")
	}
	d := filepath.Join(w, tenant, "punch-photos")
	return d, os.MkdirAll(d, 0o700)
}

// keepPhoto stores a punch photo outside the database (so it can really
// be deleted when its time is up), sealed with the firm's data key when
// the server has one.
func (o *Ops) keepPhoto(ctx context.Context, tenant, code, day string, img []byte, source string) (string, error) {
	if len(img) > maxPhoto {
		return "", invalid("the photo is larger than 2 MB")
	}
	if ct := httpType(img); ct != "image/jpeg" && ct != "image/png" {
		return "", invalid("the photo must be a JPEG or PNG")
	}
	var rb [8]byte
	_, _ = rand.Read(rb[:])
	id := "PH-" + hex.EncodeToString(rb[:])
	dir, err := o.photoDir(tenant)
	if err != nil {
		return "", err
	}
	data := img
	if o.Sealer != nil {
		if data, err = o.Sealer.Seal(tenant, []byte(id), img); err != nil {
			return "", err
		}
	}
	f := filepath.Join(dir, id+".bin")
	if err := os.WriteFile(f, data, 0o600); err != nil {
		return "", err
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cSelfie, id, PunchPhoto{ID: id, Employee: code, Date: day, Source: source, File: id + ".bin"}, SystemActor); err != nil {
		return "", err
	}
	return id, nil
}

// PunchPhotoFile returns a photo to the person's manager or HR; each view
// is recorded.
func (o *Ops) PunchPhotoFile(ctx context.Context, tenant, actor, id string) ([]byte, error) {
	if err := o.needModule(ctx, tenant, ModHRMS); err != nil {
		return nil, err
	}
	if !rePhotoID.MatchString(id) {
		return nil, ErrNotFound
	}
	ph, _, err := docs.Get[PunchPhoto](ctx, o.Docs, tenant, cSelfie, id)
	if err != nil || ph.Deleted != "" {
		return nil, ErrNotFound
	}
	sc, err := o.scopeFor(ctx, tenant, actor)
	if err != nil {
		return nil, err
	}
	if !(sc.role == RoleHR || sc.role == RoleAdmin || sc.team[ph.Employee]) {
		return nil, fmt.Errorf("%w: punch photos are for the person's manager and HR", ErrForbidden)
	}
	dir, err := o.photoDir(tenant)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(filepath.Join(dir, ph.File))
	if err != nil {
		return nil, ErrNotFound
	}
	if o.Sealer != nil {
		if b, err = o.Sealer.Open(tenant, []byte(id), b); err != nil {
			return nil, err
		}
	}
	o.log(ctx, tenant, actor, "hr.punch_photo.viewed", ph.Employee, map[string]any{"photo": id, "date": ph.Date})
	return b, nil
}

// photoPurge deletes photos older than the policy's days.
func (o *Ops) photoPurge(ctx context.Context, tenant string, now time.Time) int {
	p := o.hrPolicy(ctx, tenant)
	cut := now.In(ist).AddDate(0, 0, -p.SelfieDays).Format("2006-01-02")
	ps, _ := docs.List[PunchPhoto](ctx, o.Docs, tenant, cSelfie, "")
	dir, err := o.photoDir(tenant)
	if err != nil {
		return 0
	}
	n := 0
	for _, ph := range ps {
		if ph.Deleted != "" || ph.Date >= cut {
			continue
		}
		if err := os.Remove(filepath.Join(dir, ph.File)); err != nil && !os.IsNotExist(err) {
			continue
		}
		ph.Deleted = now.Format(time.RFC3339)
		_ = docs.Upsert(ctx, o.Docs, tenant, cSelfie, ph.ID, ph, SystemActor)
		n++
	}
	if n > 0 {
		o.log(ctx, tenant, SystemActor, "hr.punch_photos.deleted", "", map[string]any{"count": n, "older_than": cut})
	}
	return n
}

// ---- kiosk ----

// Kiosk is a shared tablet at an office entrance.
type Kiosk struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Branch    string `json:"branch,omitempty"`
	PairHash  string `json:"pair_hash,omitempty"`
	PairUntil string `json:"pair_until,omitempty"`
	TokenHash string `json:"token_hash,omitempty"`
	Status    string `json:"status"` // Waiting, Active, Revoked
	CreatedBy string `json:"created_by"`
	PairedAt  string `json:"paired_at,omitempty"`
	PairedIP  string `json:"paired_ip,omitempty"`
	LastUsed  string `json:"last_used,omitempty"`
}

func hashHex(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

func randDigits(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = '0' + b[i]%10
	}
	return string(b)
}

// CreateKiosk registers a kiosk and returns its one-time pairing code
// (valid 30 minutes), typed into the tablet at /kiosk/<firm>. HR managers.
func (o *Ops) CreateKiosk(ctx context.Context, tenant, actor, name, branch string) (Kiosk, string, error) {
	if err := o.needModule(ctx, tenant, ModHRMS); err != nil {
		return Kiosk{}, "", err
	}
	if err := o.require(ctx, tenant, actor, "Registering attendance kiosks", RoleHR); err != nil {
		return Kiosk{}, "", err
	}
	if strings.TrimSpace(name) == "" {
		return Kiosk{}, "", invalid("name the kiosk (e.g. Mumbai entrance)")
	}
	if len(o.officeNets(ctx, tenant, o.hrPolicy(ctx, tenant))) == 0 {
		return Kiosk{}, "", invalid("set the office networks first (People → Policies → Offices): a kiosk works only on the office network")
	}
	n, err := o.next(ctx, tenant, "hr-kiosk", actor)
	if err != nil {
		return Kiosk{}, "", err
	}
	code := randDigits(8)
	k := Kiosk{ID: fmt.Sprintf("K%03d", n), Name: strings.TrimSpace(name), Branch: branch, PairHash: hashHex(tenant + "|" + code), PairUntil: o.now().Add(30 * time.Minute).Format(time.RFC3339), Status: "Waiting", CreatedBy: actor}
	if err := docs.Upsert(ctx, o.Docs, tenant, cKiosk, k.ID, k, actor); err != nil {
		return k, "", err
	}
	o.log(ctx, tenant, actor, "hr.kiosk.created", k.ID, map[string]any{"name": k.Name})
	k.PairHash = ""
	return k, code, nil
}

// PairKiosk turns a pairing code into the tablet's token, from the office
// network only. The token is shown once and kept by the tablet.
func (o *Ops) PairKiosk(ctx context.Context, tenant, code, ip string) (Kiosk, string, error) {
	if err := o.needModule(ctx, tenant, ModHRMS); err != nil {
		return Kiosk{}, "", err
	}
	p := o.hrPolicy(ctx, tenant)
	if nets := o.officeNets(ctx, tenant, p); !inNetworks(ip, nets) {
		return Kiosk{}, "", fmt.Errorf("%w: a kiosk is paired only on the office network", ErrForbidden)
	}
	ks, _ := docs.List[Kiosk](ctx, o.Docs, tenant, cKiosk, "")
	h := hashHex(tenant + "|" + strings.TrimSpace(code))
	for _, k := range ks {
		if k.Status != "Waiting" || k.PairHash != h {
			continue
		}
		if t, err := time.Parse(time.RFC3339, k.PairUntil); err != nil || o.now().After(t) {
			return Kiosk{}, "", invalid("the pairing code has expired; make a new one")
		}
		var rb [32]byte
		_, _ = rand.Read(rb[:])
		tok := hex.EncodeToString(rb[:])
		k.TokenHash, k.PairHash, k.PairUntil, k.Status, k.PairedAt, k.PairedIP = hashHex(tok), "", "", "Active", o.now().Format(time.RFC3339), ip
		if err := docs.Upsert(ctx, o.Docs, tenant, cKiosk, k.ID, k, SystemActor); err != nil {
			return k, "", err
		}
		o.log(ctx, tenant, SystemActor, "hr.kiosk.paired", k.ID, map[string]any{"ip": ip})
		k.TokenHash = ""
		return k, k.ID + "." + tok, nil
	}
	return Kiosk{}, "", fmt.Errorf("%w: wrong pairing code", ErrForbidden)
}

// RevokeKiosk stops a kiosk. HR managers.
func (o *Ops) RevokeKiosk(ctx context.Context, tenant, actor, id string) error {
	if err := o.needModule(ctx, tenant, ModHRMS); err != nil {
		return err
	}
	if err := o.require(ctx, tenant, actor, "Registering attendance kiosks", RoleHR); err != nil {
		return err
	}
	_, err := docs.Update(ctx, o.Docs, tenant, cKiosk, id, actor, func(k *Kiosk) error { k.Status, k.TokenHash, k.PairHash = "Revoked", "", ""; return nil })
	if err == nil {
		o.log(ctx, tenant, actor, "hr.kiosk.revoked", id, nil)
	}
	return err
}

// Kiosks lists the firm's kiosks. HR managers.
func (o *Ops) Kiosks(ctx context.Context, tenant, actor string) ([]Kiosk, error) {
	if err := o.needModule(ctx, tenant, ModHRMS); err != nil {
		return nil, err
	}
	if err := o.require(ctx, tenant, actor, "Seeing registered phones and kiosks", RoleHR); err != nil {
		return nil, err
	}
	ks, err := docs.List[Kiosk](ctx, o.Docs, tenant, cKiosk, "")
	for i := range ks {
		ks[i].TokenHash, ks[i].PairHash = "", ""
	}
	if ks == nil {
		ks = []Kiosk{}
	}
	return ks, err
}

// staffPIN is a person's kiosk PIN (hashed) and wrong tries.
type staffPIN struct {
	Hash   string `json:"hash"`
	Fails  int    `json:"fails"`
	Locked string `json:"locked,omitempty"`
	SetAt  string `json:"set_at"`
}

var reKioskPIN = regexp.MustCompile(`^[0-9]{4,6}$`)

// SetKioskPIN sets the signed-in person's kiosk PIN (4 to 6 digits); it
// also clears a lock.
func (o *Ops) SetKioskPIN(ctx context.Context, tenant, actor, pin string) error {
	if err := o.needModule(ctx, tenant, ModHRMS); err != nil {
		return err
	}
	e, ok := o.employeeOf(ctx, tenant, actor)
	if !ok {
		return fmt.Errorf("%w: your login is not linked to an employee", ErrForbidden)
	}
	if !reKioskPIN.MatchString(pin) || strings.Count(pin, pin[:1]) == len(pin) || strings.Contains("0123456789", pin) {
		return invalid("the PIN is 4 to 6 digits, not all the same and not a run like 1234")
	}
	h, err := hashPassword(e.Code + ":" + pin)
	if err != nil {
		return err
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cPIN, e.Code, staffPIN{Hash: h, SetAt: o.now().Format(time.RFC3339)}, actor); err != nil {
		return err
	}
	o.log(ctx, tenant, actor, "hr.kiosk_pin.set", e.Code, nil)
	return nil
}

// UnlockKioskPIN clears a locked PIN. HR managers.
func (o *Ops) UnlockKioskPIN(ctx context.Context, tenant, actor, code string) error {
	if err := o.needModule(ctx, tenant, ModHRMS); err != nil {
		return err
	}
	if err := o.require(ctx, tenant, actor, "Unlocking kiosk PINs", RoleHR); err != nil {
		return err
	}
	_, err := docs.Update(ctx, o.Docs, tenant, cPIN, strings.ToUpper(code), actor, func(p *staffPIN) error { p.Fails, p.Locked = 0, ""; return nil })
	if err == nil {
		o.log(ctx, tenant, actor, "hr.kiosk_pin.unlocked", strings.ToUpper(code), nil)
	}
	return err
}

// KioskPunchReq is a punch at the kiosk.
type KioskPunchReq struct {
	Employee string `json:"employee"`
	PIN      string `json:"pin"`
	Kind     string `json:"kind"`
	Photo    []byte `json:"photo,omitempty"`
}

// KioskPunch is a punch at a kiosk: the kiosk's token, the office network,
// then the person's code and PIN. Five wrong PINs lock the PIN until the
// person sets a new one or HR unlocks it. Only the name and time are
// returned.
func (o *Ops) KioskPunch(ctx context.Context, tenant, token, ip string, r KioskPunchReq) (PunchResult, error) {
	if err := o.needModule(ctx, tenant, ModHRMS); err != nil {
		return PunchResult{}, err
	}
	kid, tok, _ := strings.Cut(token, ".")
	k, _, err := docs.Get[Kiosk](ctx, o.Docs, tenant, cKiosk, kid)
	if err != nil || k.Status != "Active" || tok == "" || hashHex(tok) != k.TokenHash {
		return PunchResult{}, fmt.Errorf("%w: this kiosk is not registered", ErrForbidden)
	}
	p := o.hrPolicy(ctx, tenant)
	if !inNetworks(ip, o.officeNets(ctx, tenant, p)) {
		return PunchResult{}, fmt.Errorf("%w: the kiosk works only on the office network", ErrForbidden)
	}
	code := strings.ToUpper(strings.TrimSpace(r.Employee))
	bad := fmt.Errorf("%w: wrong employee code or PIN", ErrForbidden)
	pinDoc, _, err := docs.Get[staffPIN](ctx, o.Docs, tenant, cPIN, code)
	if err != nil {
		checkPassword("pbkdf2-sha256$1000$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", r.PIN)
		return PunchResult{}, bad
	}
	if pinDoc.Locked != "" {
		return PunchResult{}, fmt.Errorf("%w: the PIN is locked after %d wrong tries; set a new PIN in My profile or ask HR", ErrForbidden, PINTries)
	}
	if !checkPassword(pinDoc.Hash, code+":"+r.PIN) {
		pinDoc, _ = docs.Update(ctx, o.Docs, tenant, cPIN, code, SystemActor, func(x *staffPIN) error {
			x.Fails++
			if x.Fails >= PINTries {
				x.Locked = o.now().Format(time.RFC3339)
			}
			return nil
		})
		o.log(ctx, tenant, SystemActor, "hr.kiosk_pin.failed", code, map[string]any{"kiosk": k.ID, "fails": pinDoc.Fails, "locked": pinDoc.Locked != ""})
		return PunchResult{}, bad
	}
	if pinDoc.Fails > 0 {
		_, _ = docs.Update(ctx, o.Docs, tenant, cPIN, code, SystemActor, func(x *staffPIN) error { x.Fails = 0; return nil })
	}
	e, err := o.employee(ctx, tenant, code)
	if err != nil {
		return PunchResult{}, bad
	}
	res, err := o.punch(ctx, tenant, "kiosk:"+k.ID, e, PunchReq{Kind: r.Kind, Mode: "office", IP: ip, Selfie: r.Photo, source: "kiosk"})
	if err != nil {
		return PunchResult{}, err
	}
	k.LastUsed = o.now().Format(time.RFC3339)
	_ = docs.Upsert(ctx, o.Docs, tenant, cKiosk, k.ID, k, SystemActor)
	return PunchResult{Name: res.Name, At: res.At, Kind: res.Kind, Mode: res.Mode}, nil
}

// ---- manager and device entries ----

// MarkAttendance is a manager's or HR's entry for a day: a status (and
// times), overriding the punches. Not for oneself.
func (o *Ops) MarkAttendance(ctx context.Context, tenant, actor, code, day, status, in, out, mode, note string) (Attendance, error) {
	if err := o.needModule(ctx, tenant, ModHRMS); err != nil {
		return Attendance{}, err
	}
	sc, err := o.scopeFor(ctx, tenant, actor)
	if err != nil {
		return Attendance{}, err
	}
	code = strings.ToUpper(code)
	if code == sc.self || !(sc.team[code] || sc.role == RoleHR || sc.role == RoleAdmin) {
		return Attendance{}, fmt.Errorf("%w: attendance is marked by the person's manager or HR", ErrForbidden)
	}
	if !validDay(day) || day > o.today() {
		return Attendance{}, invalid("give a day up to today")
	}
	if !contains([]string{"Present", "Half day", "Absent", "On duty"}, status) {
		return Attendance{}, invalid("status is Present, Half day, Absent or On duty")
	}
	if (in != "" && !reHHMM.MatchString(in)) || (out != "" && !reHHMM.MatchString(out)) {
		return Attendance{}, invalid("times are HH:MM")
	}
	if mode != "" && !contains(PunchModes, mode) {
		return Attendance{}, invalid("mode is %s", strings.Join(PunchModes, ", "))
	}
	if strings.TrimSpace(note) == "" {
		return Attendance{}, invalid("say why the day is marked by hand")
	}
	e, err := o.employee(ctx, tenant, code)
	if err != nil {
		return Attendance{}, err
	}
	id := code + "|" + day
	a, _, err := docs.GetOr(ctx, o.Docs, tenant, cAttend, id, Attendance{ID: id, Employee: e.Code, Date: day, Punches: []Punch{}})
	if err != nil {
		return a, err
	}
	a.Status, a.In, a.Out, a.Mode, a.Marked, a.Note = status, in, out, firstOf(mode, a.Mode), actor, trim(note, 300)
	if a.Mode == "" && status != "Absent" {
		a.Mode = "office"
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cAttend, id, a, actor); err != nil {
		return a, err
	}
	o.log(ctx, tenant, actor, "hr.attendance.marked", code, map[string]any{"date": day, "status": status, "note": a.Note})
	return a, nil
}

// ImportResult is what an attendance file did.
type ImportResult struct {
	Days     int      `json:"days"`
	Punches  int      `json:"punches"`
	Unknown  []string `json:"unknown"` // device user IDs that match no one
	Problems []string `json:"problems"`
}

// ImportAttendanceCSV loads an attendance device's export: a header row
// with an ID column (EmployeeCode, EmpCode, Code, UserID or DeviceID), and
// either DateTime (or Punch), or Date and Time. A row is a punch; each
// day's first punch is the in, the last the out. The ID is the employee
// code or the person's device user ID. HR managers.
func (o *Ops) ImportAttendanceCSV(ctx context.Context, tenant, actor string, data []byte) (ImportResult, error) {
	if err := o.needModule(ctx, tenant, ModHRMS); err != nil {
		return ImportResult{}, err
	}
	if err := o.require(ctx, tenant, actor, "Loading attendance files", RoleHR); err != nil {
		return ImportResult{}, err
	}
	r := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))))
	r.FieldsPerRecord, r.TrimLeadingSpace = -1, true
	head, err := r.Read()
	if err != nil {
		return ImportResult{}, invalid("the file is empty or not CSV")
	}
	col := map[string]int{}
	for i, h := range head {
		col[strings.ToLower(strings.ReplaceAll(strings.TrimSpace(h), " ", ""))] = i
	}
	pick := func(names ...string) int {
		for _, n := range names {
			if i, ok := col[n]; ok {
				return i
			}
		}
		return -1
	}
	ci, cdt, cd, ct := pick("employeecode", "empcode", "code", "userid", "deviceid", "enrollno"), pick("datetime", "punch", "punchtime", "logtime"), pick("date"), pick("time")
	if ci < 0 || (cdt < 0 && (cd < 0 || ct < 0)) {
		return ImportResult{}, invalid("the header needs an ID column (EmployeeCode or UserID) and DateTime, or Date and Time")
	}
	es, _ := o.employees(ctx, tenant)
	byID := map[string]Employee{}
	for _, e := range es {
		byID[e.Code] = e
		if e.DeviceID != "" {
			byID[strings.ToUpper(e.DeviceID)] = e
		}
	}
	res := ImportResult{Unknown: []string{}, Problems: []string{}}
	type key struct{ code, day string }
	got := map[key][]time.Time{}
	line := 1
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		line++
		if err != nil {
			res.Problems = append(res.Problems, fmt.Sprintf("line %d: %v", line, err))
			continue
		}
		get := func(i int) string {
			if i < 0 || i >= len(rec) {
				return ""
			}
			return strings.TrimSpace(rec[i])
		}
		id := strings.ToUpper(get(ci))
		e, ok := byID[id]
		if !ok {
			if !contains(res.Unknown, id) {
				res.Unknown = append(res.Unknown, id)
			}
			continue
		}
		s := get(cdt)
		if cdt < 0 {
			s = get(cd) + " " + get(ct)
		}
		t, ok := parsePunchTime(s)
		if !ok {
			res.Problems = append(res.Problems, fmt.Sprintf("line %d: %q is not a date and time", line, s))
			continue
		}
		k := key{e.Code, t.Format("2006-01-02")}
		got[k] = append(got[k], t)
	}
	p := o.hrPolicy(ctx, tenant)
	for k, ts := range got {
		sort.Slice(ts, func(i, j int) bool { return ts[i].Before(ts[j]) })
		e := byID[k.code]
		id := k.code + "|" + k.day
		a, _, _ := docs.GetOr(ctx, o.Docs, tenant, cAttend, id, Attendance{ID: id, Employee: k.code, Date: k.day, Punches: []Punch{}})
		var keep []Punch
		for _, x := range a.Punches {
			if x.Source != "device" {
				keep = append(keep, x) // a file loaded again replaces its own punches only
			}
		}
		a.Punches = append(keep, Punch{At: ts[0].Format(time.RFC3339), Kind: "in", Mode: "office", Source: "device", By: actor})
		if len(ts) > 1 {
			a.Punches = append(a.Punches, Punch{At: ts[len(ts)-1].Format(time.RFC3339), Kind: "out", Mode: "office", Source: "device", By: actor})
		}
		a.settle(shiftFor(p, e))
		if err := docs.Upsert(ctx, o.Docs, tenant, cAttend, id, a, actor); err != nil {
			return res, err
		}
		res.Days++
		res.Punches += len(ts)
	}
	o.log(ctx, tenant, actor, "hr.attendance.imported", "", map[string]any{"days": res.Days, "punches": res.Punches, "unknown": len(res.Unknown)})
	return res, nil
}

func parsePunchTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, f := range []string{"2006-01-02 15:04:05", "2006-01-02 15:04", "02/01/2006 15:04:05", "02/01/2006 15:04", "02-01-2006 15:04:05", "02-01-2006 15:04", "2006-01-02T15:04:05"} {
		if t, err := time.ParseInLocation(f, s, ist); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func httpType(b []byte) string {
	if len(b) > 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF {
		return "image/jpeg"
	}
	if len(b) > 8 && string(b[1:4]) == "PNG" {
		return "image/png"
	}
	return ""
}

// ---- work from home requests ----

// WFHRequest asks for a work-from-home day outside the policy.
type WFHRequest struct {
	ID        string `json:"id"`
	Employee  string `json:"employee"`
	Name      string `json:"name"`
	Date      string `json:"date"`
	Reason    string `json:"reason"`
	Status    string `json:"status"` // Pending, Approved, Rejected
	DecidedBy string `json:"decided_by,omitempty"`
	At        string `json:"at"`
}

// RequestWFH asks the manager for a work-from-home day.
func (o *Ops) RequestWFH(ctx context.Context, tenant, actor, day, reason string) (WFHRequest, error) {
	if err := o.needModule(ctx, tenant, ModHRMS); err != nil {
		return WFHRequest{}, err
	}
	e, ok := o.employeeOf(ctx, tenant, actor)
	if !ok {
		return WFHRequest{}, fmt.Errorf("%w: your login is not linked to an employee", ErrForbidden)
	}
	if !validDay(day) || day < o.today() {
		return WFHRequest{}, invalid("choose a day from today")
	}
	if strings.TrimSpace(reason) == "" {
		return WFHRequest{}, invalid("give a reason")
	}
	if dealerHomeBlocked(o.hrPolicy(ctx, tenant), e) {
		return WFHRequest{}, fmt.Errorf("%w: you deal on exchange terminals and your home is not recorded as an exchange-approved dealing location; ask compliance", ErrForbidden)
	}
	w := WFHRequest{ID: e.Code + "|" + day, Employee: e.Code, Name: e.Name, Date: day, Reason: trim(reason, 300), Status: "Pending", At: o.now().Format(time.RFC3339)}
	if err := docs.Upsert(ctx, o.Docs, tenant, cWFH, w.ID, w, actor); err != nil {
		return w, err
	}
	o.log(ctx, tenant, actor, "hr.wfh.requested", e.Code, map[string]any{"date": day})
	o.notifyManager(ctx, tenant, e, "wfh:"+w.ID, fmt.Sprintf("Work from home on %s: %s (%s)", fmtDay(day), e.Name, e.Code), "Approve or refuse on People → My team.")
	return w, nil
}

// DecideWFH approves or refuses: the person's manager or HR.
func (o *Ops) DecideWFH(ctx context.Context, tenant, actor, id string, approve bool) (WFHRequest, error) {
	if err := o.needModule(ctx, tenant, ModHRMS); err != nil {
		return WFHRequest{}, err
	}
	sc, err := o.scopeFor(ctx, tenant, actor)
	if err != nil {
		return WFHRequest{}, err
	}
	w, err := docs.Update(ctx, o.Docs, tenant, cWFH, id, actor, func(w *WFHRequest) error {
		if w.Employee == sc.self || !(sc.team[w.Employee] || sc.role == RoleHR || sc.role == RoleAdmin) {
			return fmt.Errorf("%w: the person's manager or HR decides", ErrForbidden)
		}
		if w.Status != "Pending" {
			return fmt.Errorf("%w: already %s", ErrState, strings.ToLower(w.Status))
		}
		w.Status, w.DecidedBy = map[bool]string{true: "Approved", false: "Rejected"}[approve], actor
		return nil
	})
	if err == nil {
		o.closeWork(ctx, tenant, actor, wHR, "wfh:"+w.ID, strings.ToLower(w.Status))
		o.log(ctx, tenant, actor, "hr.wfh."+strings.ToLower(w.Status), w.Employee, map[string]any{"date": w.Date})
	}
	return w, err
}

func (o *Ops) wfhApproved(ctx context.Context, tenant, code, day string) bool {
	w, _, err := docs.Get[WFHRequest](ctx, o.Docs, tenant, cWFH, code+"|"+day)
	return err == nil && w.Status == "Approved"
}

// notifyManager raises a work item for the person's manager's role,
// naming the manager (HR when there is no manager with a login).
func (o *Ops) notifyManager(ctx context.Context, tenant string, e Employee, key, title, detail string) {
	role, who := RoleHR, "HR"
	if m, err := o.employee(ctx, tenant, e.Manager); err == nil && m.User != "" {
		if r, err := o.RoleOf(ctx, tenant, m.User); err == nil {
			role, who = r, m.Name
		}
	}
	o.raiseWork(ctx, tenant, wHR, key, title+" (for "+who+")", detail, "", role, "people", o.today())
}

// ---- views ----

// AttendanceDay is a person's day for the screens.
type AttendanceDay struct {
	Date    string  `json:"date"`
	Kind    string  `json:"kind"` // working, weekly off, or the holiday's name
	Status  string  `json:"status"`
	In      string  `json:"in,omitempty"`
	Out     string  `json:"out,omitempty"`
	Mode    string  `json:"mode,omitempty"`
	Leave   string  `json:"leave,omitempty"`
	Marked  string  `json:"marked,omitempty"`
	Note    string  `json:"note,omitempty"`
	Punches []Punch `json:"punches"`
}

// monthDays lists the days of a YYYY-MM month.
func monthDays(month string) []string {
	t, err := time.Parse("2006-01", month)
	if err != nil {
		return nil
	}
	var out []string
	for d := t; d.Month() == t.Month(); d = d.AddDate(0, 0, 1) {
		out = append(out, d.Format("2006-01-02"))
	}
	return out
}

// personMonth is a person's attendance for a month, with leave and
// holidays.
func (o *Ops) personMonth(ctx context.Context, tenant string, e Employee, month string, p HRPolicy, set Settings, leaves []LeaveRequest) []AttendanceDay {
	as, _ := docs.List[Attendance](ctx, o.Docs, tenant, cAttend, e.Code+"|"+month)
	byDay := map[string]Attendance{}
	for _, a := range as {
		byDay[a.Date] = a
	}
	out := []AttendanceDay{}
	for _, d := range monthDays(month) {
		x := AttendanceDay{Date: d, Kind: o.dayKind(p, set, e, d), Punches: []Punch{}}
		if a, ok := byDay[d]; ok {
			x.Status, x.In, x.Out, x.Mode, x.Marked, x.Note, x.Punches = a.Status, a.In, a.Out, a.Mode, a.Marked, a.Note, a.Punches
			for i := range x.Punches {
				x.Punches[i].IP = "" // not shown
			}
		}
		for _, l := range leaves {
			if l.Employee == e.Code && l.Status == "Approved" && l.From <= d && d <= l.To {
				x.Leave = l.Type
				if l.Half {
					x.Leave += " (half)"
				}
			}
		}
		out = append(out, x)
	}
	return out
}

// AttendanceView is one person's month.
type AttendanceView struct {
	Employee string          `json:"employee"`
	Name     string          `json:"name"`
	Month    string          `json:"month"`
	Days     []AttendanceDay `json:"days"`
	Policy   WorkPolicy      `json:"policy"`
	Summary  WorkSummary     `json:"summary"`
	Device   string          `json:"device,omitempty"` // registered phone (hashed ID), self only
	PINSet   bool            `json:"pin_set"`
	WFH      []WFHRequest    `json:"wfh"`
}

// PersonAttendance is a person's month: themselves, their manager, HR.
func (o *Ops) PersonAttendance(ctx context.Context, tenant, actor, code, month string) (AttendanceView, error) {
	if err := o.needModule(ctx, tenant, ModHRMS); err != nil {
		return AttendanceView{}, err
	}
	sc, err := o.scopeFor(ctx, tenant, actor)
	if err != nil {
		return AttendanceView{}, err
	}
	if code == "" {
		code = sc.self
	}
	code = strings.ToUpper(code)
	if code == "" || !(code == sc.self || sc.team[code] || sc.role == RoleHR || sc.role == RoleAdmin || sc.role == RolePayroll) {
		return AttendanceView{}, ErrNotFound
	}
	if month == "" {
		month = o.now().Format("2006-01")
	}
	e, err := o.employee(ctx, tenant, code)
	if err != nil {
		return AttendanceView{}, err
	}
	p := o.hrPolicy(ctx, tenant)
	set, _ := o.Settings(ctx, tenant)
	ls, _ := docs.List[LeaveRequest](ctx, o.Docs, tenant, cLeaveReq, "")
	v := AttendanceView{Employee: e.Code, Name: e.Name, Month: month, Days: o.personMonth(ctx, tenant, e, month, p, set, ls), Policy: policyFor(p, e), WFH: []WFHRequest{}}
	v.Summary = workSummary(e, v.Days, v.Policy)
	if code == sc.self {
		if d, _, err := docs.Get[Device](ctx, o.Docs, tenant, cDevice, code); err == nil {
			v.Device = d.DeviceID
		}
		_, _, err := docs.Get[staffPIN](ctx, o.Docs, tenant, cPIN, code)
		v.PINSet = err == nil
	}
	ws, _ := docs.List[WFHRequest](ctx, o.Docs, tenant, cWFH, code+"|"+month)
	v.WFH = append(v.WFH, ws...)
	return v, nil
}

// WorkSummary is a month's ways of working against the policy.
type WorkSummary struct {
	Office     int      `json:"office"`
	Home       int      `json:"home"`
	Field      int      `json:"field"`
	OnDuty     int      `json:"on_duty"`
	Absent     int      `json:"absent"`
	Leave      int      `json:"leave"`
	Working    int      `json:"working"`
	NeedOffice int      `json:"need_office"` // office days the policy requires in the month
	Exceptions []string `json:"exceptions"`
}

func workSummary(e Employee, days []AttendanceDay, wp WorkPolicy) WorkSummary {
	s := WorkSummary{Exceptions: []string{}}
	weekOffice := map[string]int{}
	weekWorking := map[string]int{}
	for _, d := range days {
		if d.Kind != "working" {
			continue
		}
		wk := weekOf(d.Date)[0]
		s.Working++
		weekWorking[wk]++
		switch {
		case d.Leave != "":
			s.Leave++
		case d.Mode == "office" && d.Status != "Absent":
			s.Office++
			weekOffice[wk]++
		case d.Mode == "home":
			s.Home++
		case d.Mode == "field":
			s.Field++
		case d.Mode == "onduty":
			s.OnDuty++
		case d.Status == "Absent" || d.Status == "":
			s.Absent++
		}
	}
	if wp.OfficePerMonth > 0 {
		s.NeedOffice = wp.OfficePerMonth
	}
	if wp.OfficePerWeek > 0 {
		n := 0
		for _, wk := range sortedKeys(weekWorking) {
			need := min(wp.OfficePerWeek, weekWorking[wk])
			n += need
			if weekOffice[wk] < need {
				s.Exceptions = append(s.Exceptions, fmt.Sprintf("week of %s: %d office day(s), policy %d", fmtDay(wk), weekOffice[wk], need))
			}
		}
		s.NeedOffice = max(s.NeedOffice, n)
	}
	if wp.OfficePerMonth > 0 && s.Office < wp.OfficePerMonth {
		s.Exceptions = append(s.Exceptions, fmt.Sprintf("%d office day(s) in the month, policy %d", s.Office, wp.OfficePerMonth))
	}
	return s
}

// WorkReportRow is one person's month.
type WorkReportRow struct {
	Code    string      `json:"code"`
	Name    string      `json:"name"`
	Policy  string      `json:"policy"`
	Summary WorkSummary `json:"summary"`
}

// WorkReport is office against home days per person for a month, against
// each person's policy. With raise, each person with exceptions raises a
// work item for their manager. Managers see their team; HR everyone.
func (o *Ops) WorkReport(ctx context.Context, tenant, actor, month string, raise bool) ([]WorkReportRow, error) {
	if err := o.needModule(ctx, tenant, ModHRMS); err != nil {
		return nil, err
	}
	sc, err := o.scopeFor(ctx, tenant, actor)
	if err != nil {
		return nil, err
	}
	if !sc.all && len(sc.team) == 0 {
		return nil, fmt.Errorf("%w: the work report is for managers and HR", ErrForbidden)
	}
	if _, err := time.Parse("2006-01", month); err != nil {
		return nil, invalid("month is YYYY-MM")
	}
	return o.workReport(ctx, tenant, sc, month, raise), nil
}

func (o *Ops) workReport(ctx context.Context, tenant string, sc hrScope, month string, raise bool) []WorkReportRow {
	p := o.hrPolicy(ctx, tenant)
	set, _ := o.Settings(ctx, tenant)
	es, _ := o.employees(ctx, tenant)
	ls, _ := docs.List[LeaveRequest](ctx, o.Docs, tenant, cLeaveReq, "")
	out := []WorkReportRow{}
	for _, e := range es {
		if e.Status == EmpLeft || !(sc.all || sc.team[e.Code]) {
			continue
		}
		wp := policyFor(p, e)
		row := WorkReportRow{Code: e.Code, Name: e.Name, Policy: firstOf(wp.Name, wp.ID), Summary: workSummary(e, o.personMonth(ctx, tenant, e, month, p, set, ls), wp)}
		out = append(out, row)
		if raise && len(row.Summary.Exceptions) > 0 {
			o.notifyManager(ctx, tenant, e, e.Code+":work-policy:"+month, fmt.Sprintf("%s (%s): office days below the work policy in %s", e.Name, e.Code, month), strings.Join(row.Summary.Exceptions, "; ")+".")
		}
	}
	return out
}

// attendanceTick: punch photos past their time are deleted every day; on
// the 1st, last month's work-policy exceptions go to the managers.
func (o *Ops) attendanceTick(ctx context.Context, tenant string, now time.Time) {
	today := now.In(ist).Format("2006-01-02")
	last, _, _ := docs.GetOr(ctx, o.Docs, tenant, cHRTick, "attendance", hrTickDoc{})
	if last.Date >= today {
		return
	}
	o.photoPurge(ctx, tenant, now)
	if now.In(ist).Day() == 1 {
		prev := now.In(ist).AddDate(0, -1, 0).Format("2006-01")
		o.workReport(ctx, tenant, hrScope{all: true}, prev, true)
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cHRTick, "attendance", hrTickDoc{Date: today}, SystemActor)
}
