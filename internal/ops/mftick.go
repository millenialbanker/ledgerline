package ops

import (
	"context"
	"errors"
	"strings"
	"time"

	"ledgerline/internal/docs"
)

// ---- mutual funds on the scheduler ----

func init() {
	EventVars["mf.sip.due"] = []string{"scheme", "amount", "due", "bank", "platform"}
	EventVars["mf.sip.cancelled"] = []string{"scheme", "amount", "reason"}
	EventVars["mf.order.placed"] = []string{"scheme", "amount", "platform", "order"}
}

// mfTemplates are installed the first time a mutual fund message is sent.
func mfTemplates() []Template {
	t := func(ev, ch, cat, subj, body string) Template {
		x := Template{ID: "TPL-" + strings.NewReplacer(".", "-", "_", "-").Replace(ev) + "-" + strings.ToLower(ch), Event: ev, Channel: ch, Category: cat, Subject: subj, Body: body, Active: true}
		if ch == ChWhatsApp {
			x.WAName = strings.NewReplacer(".", "_").Replace(ev)
		}
		return x
	}
	return []Template{
		t("mf.sip.due", ChEmail, "Transactional", "SIP instalment of ₹{amount} due on {due}", "Dear {name},\n\nThe instalment of ₹{amount} of your SIP in {scheme} will be debited on {due} from your bank account {bank} by your mandate in favour of the clearing corporation ({platform}). Please keep the amount in that account.\n\n{firm}"),
		t("mf.sip.due", ChWhatsApp, "Transactional", "", "{name}, your SIP instalment of ₹{amount} in {scheme} will be debited on {due} from {bank}. Please keep the amount in the account."),
		t("mf.sip.cancelled", ChEmail, "Transactional", "Your SIP in {scheme} has been cancelled", "Dear {name},\n\nYour SIP of ₹{amount} in {scheme} has been cancelled because {reason}. The units you hold stay in your account. To start again, contact us or use the client portal.\n\n{firm}"),
		t("mf.order.placed", ChEmail, "Transactional", "Mutual fund order {order}: ₹{amount} in {scheme}", "Dear {name},\n\nYour order to invest ₹{amount} in {scheme} has been placed on {platform} (order {order}). The exchange will send you a link to authorise it and pay; payment goes directly to the clearing corporation, not to us. Units are allotted after the payment reaches the fund house.\n\n{firm}"),
	}
}

// mfNotify sends a mutual fund message, installing the templates first.
func (o *Ops) mfNotify(ctx context.Context, tenant, actor, event, ucc string, data map[string]string) {
	ts, err := o.templates(ctx, tenant)
	if err == nil {
		have := map[string]bool{}
		for _, t := range ts {
			have[t.ID] = true
		}
		for _, t := range mfTemplates() {
			if !have[t.ID] {
				_ = docs.Upsert(ctx, o.Docs, tenant, cTemplate, t.ID, t, "system")
			}
		}
	}
	o.notify(ctx, tenant, actor, event, ucc, data)
}

// MFPollState is where polling a platform stands.
type MFPollState struct {
	Platform  string            `json:"platform"`
	LastTry   string            `json:"last_try,omitempty"`
	LastOK    string            `json:"last_ok,omitempty"`
	Applied   int               `json:"applied"`
	NoAPI     map[string]string `json:"no_api,omitempty"` // call -> why it is not available
	LastError string            `json:"last_error,omitempty"`
}

// mfTick is the scheduler's mutual fund work for a firm:
//   - AMFI's NAV file, from 21:30 IST until the day's NAVs are in (and
//     the next morning before 11:00 if the evening fetch failed), at most
//     once an hour;
//   - each connected platform with checking on: order status, allotments
//     and mandate status every 30 minutes from 09:00 to 21:00;
//   - SIP reminders to clients a few days before each instalment, once
//     per instalment, from 09:00.
func (o *Ops) mfTick(ctx context.Context, tenant string, now time.Time) {
	set, err := o.MFSettingsGet(ctx, tenant)
	if err != nil {
		return
	}
	now = now.In(ist)
	hm := now.Format("15:04")
	if set.NAVFetch {
		o.mfNAVTick(ctx, tenant, now)
	}
	if set.Route == "" {
		return
	}
	if hm >= "09:00" && hm <= "21:00" {
		for _, p := range set.Platforms {
			o.mfPollTick(ctx, tenant, p, now)
		}
		o.mfSIPAlerts(ctx, tenant, set, now)
	}
}

func (o *Ops) mfNAVTick(ctx context.Context, tenant string, now time.Time) {
	hm := now.Format("15:04")
	if hm < "21:30" && hm >= "11:00" {
		return
	}
	gs, _ := o.Settings(ctx, tenant)
	today := now.Format("2006-01-02")
	want := today
	if hm < "21:30" {
		want = gs.PrevBusinessDay(today)
	} else if h, _ := gs.IsHoliday(today); h || now.Weekday() == time.Saturday || now.Weekday() == time.Sunday {
		want = gs.PrevBusinessDay(today)
	}
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cMFState, "nav", MFNAVState{})
	if st.NAVDate >= want {
		return
	}
	if t, err := time.Parse(time.RFC3339, st.LastTry); err == nil && now.Sub(t) < time.Hour {
		return
	}
	_, _ = o.FetchMFNAV(ctx, tenant, SystemActor)
}

func (o *Ops) mfPollTick(ctx context.Context, tenant, platform string, now time.Time) {
	cn, err := o.mfConn(ctx, tenant, platform)
	if err != nil || !cn.Fetch {
		return
	}
	key := "poll-" + platform
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cMFState, key, MFPollState{Platform: platform})
	if t, err := time.Parse(time.RFC3339, st.LastTry); err == nil && now.Sub(t) < 30*time.Minute {
		return
	}
	st.LastTry, st.LastError = now.Format(time.RFC3339), ""
	if st.NoAPI == nil {
		st.NoAPI = map[string]string{}
	}
	from, to := now.AddDate(0, 0, -7).Format("2006-01-02"), now.Format("2006-01-02")
	plat := mfPlatformFor(platform)
	note := func(call string, err error) bool {
		switch {
		case err == nil:
			delete(st.NoAPI, call)
			return true
		case errors.Is(err, errMFNoAPI):
			st.NoAPI[call] = err.Error()
		default:
			st.LastError = call + ": " + err.Error()
		}
		return false
	}
	applied := 0
	if rows, err := plat.OrderStatus(ctx, cn, from, to); note("order status", err) {
		n, _ := o.mfApplyStatuses(ctx, tenant, SystemActor, rows)
		applied += n
	}
	if rows, err := plat.Allotments(ctx, cn, from, to); note("allotments", err) {
		n, _, _ := o.mfApplyAllotments(ctx, tenant, SystemActor, "platform", rows)
		applied += n
	}
	if rows, err := plat.MandateStatus(ctx, cn, from, to); note("mandate status", err) {
		n, _ := o.mfApplyMandates(ctx, tenant, SystemActor, rows)
		applied += n
	}
	st.Applied = applied
	if st.LastError == "" && len(st.NoAPI) < 3 {
		st.LastOK = st.LastTry
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cMFState, key, st, SystemActor)
	if applied > 0 {
		o.log(ctx, tenant, SystemActor, "mf.platform.polled", platform, map[string]any{"applied": applied})
	}
}

// mfSIPAlerts reminds clients of an instalment a few days ahead, once.
func (o *Ops) mfSIPAlerts(ctx context.Context, tenant string, set MFSettings, now time.Time) {
	days := set.alertDays()
	if days <= 0 {
		return
	}
	// Once an hour is enough: each instalment is reminded of once.
	type lastRun struct {
		At string `json:"at"`
	}
	lr, _, _ := docs.GetOr(ctx, o.Docs, tenant, cMFState, "sip-alerts", lastRun{})
	if t, err := time.Parse(time.RFC3339, lr.At); err == nil && now.Sub(t) < time.Hour && now.Sub(t) >= 0 {
		return
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cMFState, "sip-alerts", lastRun{At: now.Format(time.RFC3339)}, SystemActor)
	today := now.Format("2006-01-02")
	ss, err := docs.List[MFSIP](ctx, o.Docs, tenant, cMFSIP, "")
	if err != nil {
		return
	}
	for _, s := range ss {
		if s.Status != "Active" {
			continue
		}
		due, _ := s.nextDue(today)
		if due == "" || s.AlertedFor == due || daysBetween(today, due) > days {
			continue
		}
		if _, err := docs.Update(ctx, o.Docs, tenant, cMFSIP, s.ID, SystemActor, func(x *MFSIP) error {
			if x.AlertedFor == due {
				return errAlreadyDone
			}
			x.AlertedFor = due
			return nil
		}); err != nil {
			continue
		}
		bank := ""
		if p, err := o.MFProfile(ctx, tenant, s.UCC); err == nil {
			if m, ok := mfMandateOf(p, s.MandateID); ok {
				bank = m.Bank + " " + m.Account
			}
		}
		o.mfNotify(ctx, tenant, SystemActor, "mf.sip.due", s.UCC, map[string]string{"scheme": s.Scheme, "amount": formatINR(s.Amount.Dec()), "due": due,
			"bank": strings.TrimSpace(bank), "platform": MFPlatformNames[s.Platform][1]})
	}
}

// MFStatusView is the platforms' and feeds' state for the screens.
type MFStatusView struct {
	Settings   MFSettings          `json:"settings"`
	Platforms  []MFPlatformStatus  `json:"platforms"`
	NAV        MFNAVState          `json:"nav"`
	Schemes    int                 `json:"schemes"`
	Kinds      []MFImportKind      `json:"import_kinds"`
	Mappings   []MFImportMapping   `json:"mappings"`
	Bases      []string            `json:"direct_bases"`
	Users      []User              `json:"users"`
	Routes     []MFRouteLock       `json:"routes"`
	Cutoffs    map[string]MFCutoff `json:"default_cutoffs"`
	Frequences []string            `json:"frequencies"`
}

// MFPlatformStatus is one platform's connection and polling.
type MFPlatformStatus struct {
	ID         string      `json:"id"`
	Name       string      `json:"name"`
	Clearing   string      `json:"clearing"`
	Enabled    bool        `json:"enabled"`
	Connection string      `json:"connection"` // Not set, Saved, Working, Problem
	Env        string      `json:"env,omitempty"`
	Poll       MFPollState `json:"poll"`
	API        []string    `json:"api"`    // what Ledgerline sends through the API
	Manual     []string    `json:"manual"` // what staff do on the platform's site
}

var mfAPIs = map[string][2][]string{
	MFBSE: {{"Purchase and redemption orders", "XSIP registration and cancellation"},
		{"Client registration", "Switches", "Mandate registration and status", "Order status, allotment and redemption statements (load the files)"}},
	MFNSE: {{"Client registration (183 columns)", "Purchase and redemption orders", "Switches", "Order cancellation", "XSIP registration"},
		{"XSIP cancellation and pauses", "Mandate registration and status", "Order status, allotment and redemption statements (load the files)"}},
}

// MFStatus returns the settings, platforms and feeds.
func (o *Ops) MFStatus(ctx context.Context, tenant string) (MFStatusView, error) {
	set, err := o.MFSettingsGet(ctx, tenant)
	if err != nil {
		return MFStatusView{}, err
	}
	v := MFStatusView{Settings: set, Kinds: MFImportKinds, Bases: MFDirectBases, Cutoffs: mfDefaultCutoffs, Frequences: MFSIPFrequencies}
	v.NAV, _, _ = docs.GetOr(ctx, o.Docs, tenant, cMFState, "nav", MFNAVState{})
	if ss, err := docs.List[MFScheme](ctx, o.Docs, tenant, cMFScheme, ""); err == nil {
		v.Schemes = len(ss)
	}
	v.Mappings, _ = o.MFImportMappings(ctx, tenant)
	if v.Mappings == nil {
		v.Mappings = []MFImportMapping{}
	}
	us, _ := o.Users(ctx, tenant)
	for _, u := range us {
		if u.Role != RoleClient && u.Active {
			v.Users = append(v.Users, User{ID: u.ID, Name: u.Name, Role: u.Role})
		}
	}
	v.Routes, _ = o.MFRoutes(ctx, tenant)
	for _, p := range []string{MFBSE, MFNSE} {
		ps := MFPlatformStatus{ID: p, Name: MFPlatformNames[p][0], Clearing: MFPlatformNames[p][1], Enabled: set.enabled(p), Connection: "Not set", API: mfAPIs[p][0], Manual: mfAPIs[p][1]}
		if c, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, mfConnIDs[p]); err == nil {
			ps.Connection, ps.Env = c.Status, c.Values["env"]
		}
		ps.Poll, _, _ = docs.GetOr(ctx, o.Docs, tenant, cMFState, "poll-"+p, MFPollState{Platform: p})
		v.Platforms = append(v.Platforms, ps)
	}
	return v, nil
}
