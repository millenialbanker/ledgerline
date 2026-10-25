package ops

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/docs"
)

var ctxT = context.Background()

func pairPC(t *testing.T, o *Ops, user string) (RPAAgent, string) {
	t.Helper()
	code, _, err := o.RPAPairCode(ctxT, T, user)
	if err != nil {
		t.Fatal(err)
	}
	a, tok, err := o.RPAPair(ctxT, T, strings.ToLower(code), "Desk PC", "windows", "1.0")
	if err != nil {
		t.Fatal(err)
	}
	return a, tok
}

func TestRPAPairingAndAccess(t *testing.T) {
	o, ctx := setup(t)
	ctxT = ctx
	if _, err := o.RPAHome(ctx, T, "sunrise.ap"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("AP used automations: %v", err)
	}
	code, _, _ := o.RPAPairCode(ctx, T, "priya.nair")
	a, tok, err := o.RPAPair(ctx, T, code, "Priya's desk", "windows", "1.0")
	if err != nil || a.User != "priya.nair" || !strings.HasPrefix(tok, "llbot_") {
		t.Fatalf("pair: %v %+v", err, a)
	}
	if _, _, err := o.RPAPair(ctx, T, code, "again", "", ""); err == nil {
		t.Fatal("code used twice")
	}
	if got, err := o.RPAAgentAuth(ctx, T, tok); err != nil || got.ID != a.ID {
		t.Fatalf("auth: %v", err)
	}
	if _, err := o.RPAAgentAuth(ctx, T, tok+"x"); err == nil {
		t.Fatal("wrong token accepted")
	}
	// Expired codes do not work.
	code2, _, _ := o.RPAPairCode(ctx, T, "priya.nair")
	o.Now = func() time.Time { return time.Now().Add(11 * time.Minute) }
	if _, _, err := o.RPAPair(ctx, T, code2, "late", "", ""); err == nil {
		t.Fatal("expired code accepted")
	}
	o.Now = time.Now
	// Someone else cannot remove it; the owner can, and the token stops working.
	if err := o.RevokeRPAAgent(ctx, T, "farhan.khan", a.ID); err == nil {
		t.Fatal("other user revoked")
	}
	if err := o.RevokeRPAAgent(ctx, T, "priya.nair", a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := o.RPAAgentAuth(ctx, T, tok); err == nil {
		t.Fatal("revoked token works")
	}
}

func TestRPASecretsOnlyDuringTheirStep(t *testing.T) {
	o, ctx := setup(t)
	ctxT = ctx
	cred, err := o.SaveRPACred(ctx, T, "priya.nair", "", RPACredInput{Name: "NSE ENIT", Site: "https://enit.nseindia.com", Username: "BRK001", Password: "s3cret!", TOTP: "JBSWY3DPEHPK3PXP"})
	if err != nil {
		t.Fatal(err)
	}
	if cred.HasPassword != true || cred.HasTOTP != true {
		t.Fatal("view")
	}
	h, _ := o.RPAHome(ctx, T, "priya.nair")
	for _, c := range h.Creds {
		if c.ID == cred.ID && (strings.Contains(strings.ToLower(c.Notes+c.Username), "s3cret")) {
			t.Fatal("password in view")
		}
	}
	// Another user's password cannot be used in my workflow.
	other, _ := o.SaveRPACred(ctx, T, "farhan.khan", "", RPACredInput{Name: "Mine", Site: "x.example", Username: "f", Password: "p"})
	steps := []RPAStep{
		{Kind: "open", URL: "https://enit.nseindia.com/login"},
		{Kind: "type", Target: &RPATarget{CSS: "#user"}, Value: "{{secret:" + cred.ID + ".username}}"},
		{Kind: "type", Target: &RPATarget{CSS: "#pass"}, Value: "{{secret:" + cred.ID + ".password}}"},
		{Kind: "type", Target: &RPATarget{CSS: "#otp"}, Value: "{{secret:" + cred.ID + ".totp}}"},
		{Kind: "click", Target: &RPATarget{Text: "Login"}},
		{Kind: "type", Target: &RPATarget{CSS: "#date"}, Value: "{{tradedate:DD-MM-YYYY}}"},
	}
	if _, err := o.SaveRPAFlow(ctx, T, "priya.nair", "", RPAFlowInput{Name: "x", Steps: append(steps, RPAStep{Kind: "type", Target: &RPATarget{CSS: "#x"}, Value: "{{secret:" + other.ID + ".password}}"})}); err == nil {
		t.Fatal("used another person's password")
	}
	if _, err := o.SaveRPAFlow(ctx, T, "priya.nair", "", RPAFlowInput{Name: "x", Steps: []RPAStep{{Kind: "type", Target: &RPATarget{CSS: "#a"}, Value: "{{nope}}"}}}); err == nil {
		t.Fatal("unknown value accepted")
	}
	f, err := o.SaveRPAFlow(ctx, T, "priya.nair", "", RPAFlowInput{Name: "Download NSE files", Shared: true, Steps: steps})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := pairPC(t, o, "priya.nair")
	fa, _ := pairPC(t, o, "farhan.khan")
	// A shared workflow can't run with the owner's passwords for someone else.
	if _, err := o.StartRPARun(ctx, T, "farhan.khan", f.ID, fa.ID); err == nil {
		t.Fatal("ran with someone else's password")
	}
	// Nor on someone else's computer.
	if _, err := o.StartRPARun(ctx, T, "priya.nair", f.ID, fa.ID); err == nil {
		t.Fatal("ran on someone else's computer")
	}
	r, err := o.StartRPARun(ctx, T, "priya.nair", f.ID, a.ID)
	if err != nil || r.Context["tradedate"] == "" {
		t.Fatalf("run %v %+v", err, r.Context)
	}
	// Not yet claimed: no secrets.
	if _, err := o.RPASecret(ctx, T, a, r.ID, 2, cred.ID, "password"); err == nil {
		t.Fatal("secret before the run started")
	}
	j, _ := o.RPANextJob(ctx, T, a)
	if j == nil || j.Kind != "run" || j.Run.ID != r.ID {
		t.Fatalf("job %+v", j)
	}
	if v, err := o.RPASecret(ctx, T, a, r.ID, 2, cred.ID, "password"); err != nil || v != "s3cret!" {
		t.Fatalf("password %q %v", v, err)
	}
	if v, err := o.RPASecret(ctx, T, a, r.ID, 1, cred.ID, "username"); err != nil || v != "BRK001" {
		t.Fatalf("username %q %v", v, err)
	}
	if v, err := o.RPASecret(ctx, T, a, r.ID, 3, cred.ID, "totp"); err != nil || len(v) != 6 {
		t.Fatalf("totp %q %v", v, err)
	}
	// Only for the step that uses it, and only to the run's computer.
	if _, err := o.RPASecret(ctx, T, a, r.ID, 1, cred.ID, "password"); err == nil {
		t.Fatal("password released for the username step")
	}
	if _, err := o.RPASecret(ctx, T, fa, r.ID, 2, cred.ID, "password"); err == nil {
		t.Fatal("password released to another computer")
	}
	c, _, _ := docs.Get[RPACred](ctx, o.Docs, T, cRPACred, cred.ID)
	if c.Uses != 3 {
		t.Errorf("uses %d", c.Uses)
	}
	// Pause, continue, finish.
	if ctl, _ := o.RPARunEvent(ctx, T, a, r.ID, RPAEvent{Step: 4, Status: "Paused", Message: "Solve the captcha"}); ctl.Resume {
		t.Fatal("resumed at once")
	}
	rr, _ := o.RPARunGet(ctx, T, "priya.nair", r.ID)
	if rr.Status != "Paused" || rr.Pause != "Solve the captcha" {
		t.Fatalf("paused %+v", rr.Status)
	}
	if _, err := o.ControlRPARun(ctx, T, "priya.nair", r.ID, "continue"); err != nil {
		t.Fatal(err)
	}
	if ctl, _ := o.RPARunEvent(ctx, T, a, r.ID, RPAEvent{Step: 4, Status: "Paused"}); !ctl.Resume {
		t.Fatal("continue not passed on")
	}
	o.RPARunEvent(ctx, T, a, r.ID, RPAEvent{Step: 4, Status: "Running"})
	if rr, _ := o.RPARunGet(ctx, T, "priya.nair", r.ID); rr.Status != "Running" || rr.Pause != "" {
		t.Fatalf("not running again: %s", rr.Status)
	}
	o.RPARunEvent(ctx, T, a, r.ID, RPAEvent{Step: 4, Status: "Done", Ms: 120})
	if err := o.RPARunDone(ctx, T, a, r.ID, "Succeeded", "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := o.RPASecret(ctx, T, a, r.ID, 2, cred.ID, "password"); err == nil {
		t.Fatal("secret after the run ended")
	}
	ff, _ := o.RPAFlowGet(ctx, T, "priya.nair", f.ID)
	if ff.LastStatus != "Succeeded" {
		t.Errorf("flow last status %q", ff.LastStatus)
	}
	// Deleting a login removes its secrets.
	if err := o.DeleteRPACred(ctx, T, "priya.nair", cred.ID); err != nil {
		t.Fatal(err)
	}
	c, _, _ = docs.Get[RPACred](ctx, o.Docs, T, cRPACred, cred.ID)
	if c.Password != "" || c.TOTP != "" || c.canUse("priya.nair") {
		t.Fatal("deleted login still usable")
	}
}

func TestRPARecordingBindsPasswords(t *testing.T) {
	o, ctx := setup(t)
	ctxT = ctx
	cred, _ := o.SaveRPACred(ctx, T, "priya.nair", "", RPACredInput{Name: "CDSL", Site: "cdslindia.com", Username: "DPUSER", Password: "pw"})
	a, _ := pairPC(t, o, "priya.nair")
	rec, err := o.StartRPARecording(ctx, T, "priya.nair", a.ID, "", "CDSL holdings", "https://web.cdslindia.com/login")
	if err != nil {
		t.Fatal(err)
	}
	j, _ := o.RPANextJob(ctx, T, a)
	if j == nil || j.Kind != "record" {
		t.Fatal("no recording job")
	}
	stop, err := o.RPARecordSteps(ctx, T, a, rec.ID, []RPAStep{
		{Kind: "open", URL: "https://web.cdslindia.com/login"},
		{Kind: "click", Target: &RPATarget{CSS: "#u"}},
		{Kind: "type", Target: &RPATarget{CSS: "#u"}, Value: "dpuser", NeedsSecret: "username"},
		{Kind: "type", Target: &RPATarget{CSS: "#p"}, Value: "", NeedsSecret: "password"},
		{Kind: "type", Target: &RPATarget{CSS: "#otp"}, NeedsSecret: "otp"},
		{Kind: "click", Target: &RPATarget{Text: "Sign in"}},
	})
	if err != nil || stop {
		t.Fatal(err)
	}
	if _, err := o.StopRPARecording(ctx, T, "priya.nair", rec.ID, false); err != nil {
		t.Fatal(err)
	}
	if stop, _ := o.RPARecordSteps(ctx, T, a, rec.ID, nil); !stop {
		t.Fatal("stop not passed on")
	}
	done, err := o.RPARecordDone(ctx, T, a, rec.ID)
	if err != nil || done.Saved == "" {
		t.Fatal(err)
	}
	f, _ := o.RPAFlowGet(ctx, T, "priya.nair", done.Saved)
	kinds := []string{}
	for _, s := range f.Steps {
		kinds = append(kinds, s.Kind)
	}
	if strings.Join(kinds, ",") != "open,type,type,pause,click" {
		t.Fatalf("steps %v", kinds)
	}
	if f.Steps[1].Value != "{{secret:"+cred.ID+".username}}" || f.Steps[2].Value != "{{secret:"+cred.ID+".password}}" {
		t.Fatalf("not bound: %q %q", f.Steps[1].Value, f.Steps[2].Value)
	}
	// Saving as-is checks it all.
	if _, err := o.SaveRPAFlow(ctx, T, "priya.nair", f.ID, RPAFlowInput{Name: f.Name, Steps: f.Steps}); err != nil {
		t.Fatal(err)
	}
	// Someone else's shared view: read, not change.
	o.SaveRPAFlow(ctx, T, "priya.nair", f.ID, RPAFlowInput{Name: f.Name, Steps: f.Steps, Shared: true})
	if _, err := o.RPAFlowGet(ctx, T, "farhan.khan", f.ID); err != nil {
		t.Fatal("shared not visible")
	}
	if _, err := o.SaveRPAFlow(ctx, T, "farhan.khan", f.ID, RPAFlowInput{Name: "mine now", Steps: f.Steps}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("other user changed it: %v", err)
	}
}

func TestRPAScheduleAndStall(t *testing.T) {
	o, ctx := setup(t)
	ctxT = ctx
	a, _ := pairPC(t, o, "priya.nair")
	f, err := o.SaveRPAFlow(ctx, T, "priya.nair", "", RPAFlowInput{Name: "Bhavcopy", Steps: []RPAStep{{Kind: "open", URL: "https://www.nseindia.com"}},
		Schedule: &RPASchedule{Active: true, At: "07:30", When: "daily", Agent: a.ID}})
	if err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 10, 27, 7, 31, 0, 0, ist)
	if err := o.rpaTick(ctx, T, day, true); err != nil {
		t.Fatal(err)
	}
	o.rpaTick(ctx, T, day.Add(time.Minute), true) // not twice a day
	rs, _ := docs.List[RPARun](ctx, o.Docs, T, cRPARun, "")
	if len(rs) != 1 || rs[0].Trigger != "schedule" || rs[0].Flow != f.ID {
		t.Fatalf("scheduled runs %d", len(rs))
	}
	o.RPANextJob(ctx, T, a)
	o.Now = func() time.Time { return time.Now().Add(20 * time.Minute) }
	o.rpaTick(ctx, T, o.Now(), false)
	o.Now = time.Now
	r, _ := o.RPARunGet(ctx, T, "priya.nair", rs[0].ID)
	if r.Status != "Failed" || !strings.Contains(r.Error, "stopped responding") {
		t.Fatalf("stalled run %s %s", r.Status, r.Error)
	}
	if _, err := o.SaveRPAFlow(ctx, T, "priya.nair", f.ID, RPAFlowInput{Name: "x", Steps: f.Steps, Schedule: &RPASchedule{Active: true, At: "25:00", When: "daily", Agent: a.ID}}); err == nil {
		t.Error("bad time accepted")
	}
}

func TestTOTP(t *testing.T) {
	// RFC 6238 test vector (SHA-1, 8 digits 94287082 at 59 s → last six digits).
	seed := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ" // "12345678901234567890"
	if c, err := totpCode(seed, time.Unix(59, 0)); err != nil || c != "287082" {
		t.Fatalf("totp %s %v", c, err)
	}
	if c, _ := totpCode(seed, time.Unix(1111111109, 0)); c != "081804" {
		t.Fatalf("totp %s", c)
	}
}
