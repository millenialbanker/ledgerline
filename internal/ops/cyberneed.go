package ops

import (
	"context"
	"fmt"
	"strings"
)

// ---- conditions from the firm modules, with Ledgerline's own fallback ----
//
// The technical glitch framework, the specified stockbroker's duties and
// the Qualified Stock Broker's half-yearly cycle switch on from the firm
// modules (Settings → Firm modules: ibt:10k, specified, qsb, read through
// dutyNeed). Until the firm has answered, or on a server whose firm
// modules do not know a condition yet, Ledgerline falls back to what it
// can see itself:
//
//   - ibt:10k: the firm's answer on internet or app trading (Firm profile)
//     and the registered clients Ledgerline counts on the last 31 March
//     (glitchCheck, resilience.go). No online trading: no; online trading
//     with 10,000 or fewer: no; with more: yes; not answered: not sure.
//   - specified: no when ibt:10k is no (NSE names specified stockbrokers
//     only among those over 10,000 clients); otherwise not sure.
//   - qsb: not sure. Only the exchanges' list says who is a QSB.
//
// Not sure means the screens show it and nothing nags or blocks.

// cyberNeed reads a condition ("a|b" is any of them).
func (o *Ops) cyberNeed(ctx context.Context, tenant, cond string) (tri, string) {
	f := o.firmFacts(ctx, tenant)
	if strings.Contains(cond, "|") {
		var unk, no []string
		for _, p := range strings.Split(cond, "|") {
			switch v, why := o.cyberNeedOne(ctx, tenant, strings.TrimSpace(p), f); v {
			case triYes:
				return triYes, why
			case triNo:
				no = append(no, why)
			default:
				unk = append(unk, why)
			}
		}
		if len(unk) > 0 {
			return triUnknown, strings.Join(unk, "; or ")
		}
		return triNo, strings.Join(no, ", and ")
	}
	return o.cyberNeedOne(ctx, tenant, cond, f)
}

func (o *Ops) cyberNeedOne(ctx context.Context, tenant, cond string, f firmFacts) (tri, string) {
	v, why := dutyNeed(cond, f)
	if v != triUnknown || !strings.HasPrefix(why, "unknown condition") {
		return v, why
	}
	switch cond {
	case "ibt:10k":
		g := o.glitchCheck(ctx, tenant, o.today())
		return glitchFallback(f.IBT, g.Clients, g.AsOf)
	case "specified":
		if x, _ := o.cyberNeedOne(ctx, tenant, "ibt:10k", f); x == triNo {
			return triNo, "NSE names specified stockbrokers only among brokers with more than 10,000 online clients"
		}
		return triUnknown, "tell Ledgerline whether NSE named the firm a specified stockbroker (Settings → Firm modules)"
	case "qsb":
		return triUnknown, "tell Ledgerline whether the firm is a Qualified Stock Broker (Settings → Firm modules)"
	}
	return v, why
}

// glitchFallback is the ibt:10k answer from the firm's online-trading
// answer and Ledgerline's count of registered clients on asOf.
func glitchFallback(ibt tri, clients int, asOf string) (tri, string) {
	switch {
	case ibt == triNo:
		return triNo, "clients don't trade through your website or app"
	case ibt == triUnknown:
		return triUnknown, "tell Ledgerline whether clients trade through the firm's website or app (Settings → Firm profile)"
	case clients <= 10000:
		return triNo, fmt.Sprintf("%d registered clients on %s, not more than 10,000", clients, fmtDay(asOf))
	}
	return triYes, fmt.Sprintf("online trading and %d registered clients on %s, more than 10,000", clients, fmtDay(asOf))
}
