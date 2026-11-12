package ops

import (
	"context"
	"fmt"
	"strings"
	"time"

	"ledgerline/internal/docs"
)

// ---- clients of the DP only ----
//
// A firm that is also a depository participant has clients who hold a demat
// account with it and trade through another broker, or not at all: SEBI's
// Master Circular for Depositories lets a DP open a demat account for any
// investor, with or without a trading account. Such a client (DPOnly) is a
// full client of the firm for KYC (KRA, CKYC, PMLA risk and periodic
// updation), contact details, bank account, nominees, the DP's accounts and
// bills, DP and holding statements, the ledger the DP bills post to and its
// statements, payouts of a credit balance, documents and the client portal.
//
// It is not a trading client, so it is left out of everything that is
// about trading: the exchanges' UCC records (NSE, BSE, MCX), the front
// office's files and limits (Finspot, BOW and the generic limit file), RMS,
// contract notes and trading bills, running account settlement (SEBI's
// settlement of a trading client's funds), margin reporting, SEGMON and the
// clearing corporation's allocation, the exchanges' 24-month inactivity
// rules for trading accounts, and brokerage plans.
//
// A DP-only client opens a trading account by the same path as any
// application (OpenTradingAccount): the segments and what they need
// (income proof for derivatives, the running account settlement choice) are
// recorded, the client (whose KYC is done) is at the UCC upload stage, where
// the exchanges' UCC pages send it as a new client, and a checker who did
// not make the request activates it.

// tradingClient says whether a client trades through the firm.
func (c Client) tradingClient() bool { return !c.DPOnly }

// tradingClientMap is ClientMap without the DP-only clients: for what is
// about trading.
func (o *Ops) tradingClientMap(ctx context.Context, tenant string) (map[string]Client, error) {
	cs, err := o.ClientMap(ctx, tenant)
	if err != nil {
		return cs, err
	}
	for u, c := range cs {
		if c.DPOnly {
			delete(cs, u)
		}
	}
	return cs, nil
}

// tradingClients is Clients without the DP-only clients.
func (o *Ops) tradingClients(ctx context.Context, tenant string) ([]Client, error) {
	cs, err := o.Clients(ctx, tenant)
	if err != nil {
		return cs, err
	}
	out := cs[:0]
	for _, c := range cs {
		if !c.DPOnly {
			out = append(out, c)
		}
	}
	return out, nil
}

// dpOnlySet are the DP-only clients' UCCs, to leave out of data that is
// read by client (holdings, balances).
func (o *Ops) dpOnlySet(ctx context.Context, tenant string) map[string]bool {
	cs, _ := o.Clients(ctx, tenant)
	out := map[string]bool{}
	for _, c := range cs {
		if c.DPOnly {
			out[c.UCC] = true
		}
	}
	return out
}

// TradingAccount is what opening a trading account for a DP-only client
// takes: the segments, income proof for derivatives, and the running
// account settlement choice.
type TradingAccount struct {
	Segments    []string `json:"segments"`
	IncomeProof bool     `json:"income_proof"`
	Income      string   `json:"income,omitempty"`
	RAS         string   `json:"ras"`
	// RiskDisclosure: the client acknowledged the risk disclosure document
	// (needed for trading).
	RiskDisclosure bool   `json:"risk_disclosure"`
	Doc            string `json:"doc,omitempty"` // the client's signed request or application
	Note           string `json:"note,omitempty"`
}

// OpenTradingAccount turns a DP-only client into a trading client. The
// trading details are checked as any application's are; the client is at
// the UCC upload stage (the exchanges' UCC pages send it as a new client)
// until a checker who did not make the request activates it (AdvanceStage).
func (o *Ops) OpenTradingAccount(ctx context.Context, tenant, actor, ucc string, t TradingAccount) (Client, error) {
	if err := o.require(ctx, tenant, actor, "Opening a trading account", RoleMaker, RoleChecker); err != nil {
		return Client{}, err
	}
	c, err := o.Client(ctx, tenant, ucc)
	if err != nil {
		return Client{}, err
	}
	if !c.DPOnly {
		return c, fmt.Errorf("%w: %s already has a trading account", ErrState, c.UCC)
	}
	if !t.RiskDisclosure {
		return c, &ValidationError{[]FieldError{{"risk_disclosure", "Risk disclosure acknowledgement is required"}}}
	}
	if len(c.Incomplete) > 0 {
		return c, invalid("%s's details are incomplete (%s): complete them first", c.UCC, strings.Join(c.Incomplete, ", "))
	}
	if d, hit := o.IsDebarredPAN(ctx, tenant, c.PAN, o.now().Format("2006-01-02")); hit {
		return c, fmt.Errorf("%w: %s's PAN is debarred from the securities market (%s order %s)", ErrForbidden, c.UCC, d.Regulator, d.OrderNo)
	}
	next := c
	next.DPOnly = false
	next.Segments, next.IncomeProof, next.RAS, next.RiskDisclosure = t.Segments, t.IncomeProof || c.IncomeProof, t.RAS, true
	if strings.TrimSpace(t.Income) != "" {
		next.Income = strings.TrimSpace(t.Income)
	}
	next.Normalise()
	if err := next.Validate(); err != nil {
		return c, err
	}
	was := c.Status
	c, err = docs.Update(ctx, o.Docs, tenant, cClient, c.UCC, actor, func(x *Client) error {
		if !x.DPOnly {
			return fmt.Errorf("%w: %s already has a trading account", ErrState, x.UCC)
		}
		x.DPOnly = false
		x.Segments, x.IncomeProof, x.RAS, x.Income, x.RiskDisclosure = next.Segments, next.IncomeProof, next.RAS, next.Income, true
		// The trading account goes through activation like any new one:
		// the client's KYC is done (as a client of the DP), so it is at the
		// UCC upload stage, where the exchanges' UCC pages send it as a new
		// client, made by this person, so a different checker activates it
		// (AdvanceStage). The DP's accounts carry on meanwhile.
		x.Status, x.CreatedBy, x.ActivatedBy = "UCC upload", actor, ""
		// A new trading account: the 24-month inactivity count starts now.
		x.Inactive, x.InactiveSince, x.ReactivatedOn = false, "", o.now().Format("2006-01-02")
		return nil
	})
	if err != nil {
		return c, err
	}
	o.log(ctx, tenant, actor, "client.trading_opened", c.UCC, map[string]any{"segments": c.Segments, "ras": c.RAS, "was": was, "doc": t.Doc, "note": trim(t.Note, 200), "at": o.now().Format(time.RFC3339)})
	o.raiseWork(ctx, tenant, "trading", c.UCC, "Activate "+c.UCC+"'s new trading account",
		fmt.Sprintf("%s, a client of the DP only, asked to open a trading account (%s). Check the KYC and the trading documents and activate the client; the exchanges' UCC records are then sent.", c.Name, strings.Join(c.Segments, ", ")), c.UCC, RoleChecker, "clients", "")
	return c, nil
}
