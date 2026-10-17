package ops

import "context"

// notify raises a client-facing event for the messaging layer.
func (o *Ops) notify(ctx context.Context, tenant, actor, event, ucc string, data map[string]string) {}
