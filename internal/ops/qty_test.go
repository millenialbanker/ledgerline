package ops

import (
	"context"

	"ledgerline/internal/money"
)

// dq is a whole quantity as an exact demat quantity, for tests.
func dq(n int64) money.Dec { return money.DecInt(n) }

// wq is a demat quantity's whole units, for tests.
func wq(d money.Dec) int64 { return wholeUnits(d) }

// testBO is the DP's record of a client's primary demat account.
func testBO(ctx context.Context, o *Ops, tenant, ucc string) (BOAccount, int64, error) {
	c, err := o.Client(ctx, tenant, ucc)
	if err != nil {
		return BOAccount{}, 0, err
	}
	return o.bo(ctx, tenant, c), 0, nil
}
