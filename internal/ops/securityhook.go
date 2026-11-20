package ops

import "context"

// securityEvent hands a security-relevant event (kind such as
// "support_access.approved", severity "info" or "warning") to the security
// events and incidents module.
//
// TODO: that module is being built separately; when it lands, this stub
// is replaced by its call (or forwards to it). Until then it does nothing:
// every event passed here is also written to the audit trail by the
// caller, so nothing is lost.
func (o *Ops) securityEvent(ctx context.Context, tenant, kind, severity, actor, object string, detail map[string]any) {
	_, _, _, _, _, _, _ = ctx, tenant, kind, severity, actor, object, detail
}
