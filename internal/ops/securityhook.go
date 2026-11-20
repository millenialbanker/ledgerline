package ops

import "context"

// securityEvent hands a support-access event (kind such as
// "support_access.approved", severity "info" or "warning") to the security
// events and incidents module (secevents.go). The caller also writes it to
// the audit trail.
func (o *Ops) securityEvent(ctx context.Context, tenant, kind, severity, actor, object string, detail map[string]any) {
	sev := SevInfo
	if severity == "warning" {
		sev = SevMedium
	}
	o.SecurityEvent(ctx, tenant, SecEvent{Kind: kind, Severity: sev, Actor: actor, Subject: object, Detail: detail})
}
