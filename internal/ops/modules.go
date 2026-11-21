package ops

import (
	"context"
	"fmt"
	"time"

	"ledgerline/internal/docs"
)

// ---- priced modules a firm switches on ----
//
// Some parts of Ledgerline are sold separately from the edition: HRMS (the
// firm's own people: employee master, joiners and leavers, certificates,
// attendance and leave, self-service) and Payroll inside it. Each firm has
// its own switch, set by the firm's Admin (Settings → Modules) or by the
// provider from Ledgerline Control, the way an edition reaches a firm: a
// "module" command in the reply to the firm's report (controlclient.go,
// ApplyCommand). The firm's report carries the modules that are on, so
// Control bills each month a module was on.
//
// With a module off nothing of it shows: its routes answer 404 (needModule)
// and the screens leave the People group out. Its records are kept, so
// switching it on again brings everything back.

const (
	cModules   = "modules"
	ModHRMS    = "hrms"
	ModPayroll = "payroll"
)

// ErrModuleOff is returned by every call of a module the firm has not
// switched on. It wraps ErrNotFound, so the routes answer 404: to a firm
// without the module, the module does not exist.
var ErrModuleOff = fmt.Errorf("%w: this module is not switched on for the firm (Settings → Modules)", ErrNotFound)

// ModuleSwitches are a firm's modules.
type ModuleSwitches struct {
	HRMS    bool   `json:"hrms"`
	Payroll bool   `json:"payroll"`
	By      string `json:"by,omitempty"` // who changed them last (system:control from Control)
	At      string `json:"at,omitempty"`
}

// On reports whether a module is on. Payroll is on only with HRMS.
func (m ModuleSwitches) On(mod string) bool {
	switch mod {
	case ModHRMS:
		return m.HRMS
	case ModPayroll:
		return m.HRMS && m.Payroll
	}
	return false
}

// List is the modules that are on, for the report to Control.
func (m ModuleSwitches) List() []string {
	var out []string
	for _, x := range []string{ModHRMS, ModPayroll} {
		if m.On(x) {
			out = append(out, x)
		}
	}
	return out
}

// Modules returns the firm's module switches (all off until set).
func (o *Ops) Modules(ctx context.Context, tenant string) ModuleSwitches {
	m, _, _ := docs.GetOr(ctx, o.Docs, tenant, cModules, "current", ModuleSwitches{})
	return m
}

// needModule refuses a call of a module the firm has not switched on.
func (o *Ops) needModule(ctx context.Context, tenant, mod string) error {
	if !o.Modules(ctx, tenant).On(mod) {
		return ErrModuleOff
	}
	return nil
}

// ModuleOn reports whether a module is on (for the API and screens).
func (o *Ops) ModuleOn(ctx context.Context, tenant, mod string) bool {
	return o.Modules(ctx, tenant).On(mod)
}

// SetModules switches the firm's modules (Settings → Modules). Payroll
// needs HRMS: payroll on switches HRMS on.
func (o *Ops) SetModules(ctx context.Context, tenant, actor string, m ModuleSwitches) (ModuleSwitches, error) {
	if err := notSupport(actor, "Switching modules"); err != nil {
		return ModuleSwitches{}, err
	}
	if err := o.require(ctx, tenant, actor, "Switching modules on or off", RoleAdmin); err != nil {
		return ModuleSwitches{}, err
	}
	return o.saveModules(ctx, tenant, actor, m)
}

func (o *Ops) saveModules(ctx context.Context, tenant, actor string, m ModuleSwitches) (ModuleSwitches, error) {
	if m.Payroll {
		m.HRMS = true
	}
	prev := o.Modules(ctx, tenant)
	m.By, m.At = actor, o.now().Format(time.RFC3339)
	if err := docs.Upsert(ctx, o.Docs, tenant, cModules, "current", m, actor); err != nil {
		return prev, err
	}
	for _, x := range []string{ModHRMS, ModPayroll} {
		if prev.On(x) != m.On(x) {
			o.log(ctx, tenant, actor, "module.switched", x, map[string]any{"on": m.On(x)})
		}
	}
	return m, nil
}

// applyModule carries out a module command from Ledgerline Control.
func (o *Ops) applyModule(ctx context.Context, tenant, mod string, on bool) error {
	m := o.Modules(ctx, tenant)
	switch mod {
	case ModHRMS:
		m.HRMS = on
		if !on {
			m.Payroll = false
		}
	case ModPayroll:
		m.Payroll = on
	default:
		return invalid("unknown module %q", mod)
	}
	_, err := o.saveModules(ctx, tenant, "system:control", m)
	return err
}
