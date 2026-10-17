package recon

// Playbook is how to investigate one break type: what to check, then what
// to do for each thing the checks can show. Notes are templates for the
// resolution note; {trade}, {ucc}, {exchange}, {oms}, {order} and
// {terminal} are filled in by the console.
type Playbook struct {
	Type     BreakType `json:"type"`
	Check    []string  `json:"check"`
	Outcomes []Outcome `json:"outcomes"`
}

// Outcome is one finding and the steps that put it right.
type Outcome struct {
	If   string   `json:"if"`
	Fix  []string `json:"fix"`
	Note string   `json:"note"`
	// CCM marks the outcome that needs a client code modification.
	CCM bool `json:"ccm,omitempty"`
}

// Playbooks lists every break type's playbook in priority order.
func Playbooks() []Playbook {
	out := make([]Playbook, 0, len(order))
	for _, t := range order {
		p := playbooks[t]
		p.Type = t
		out = append(out, p)
	}
	return out
}

var playbooks = map[BreakType]Playbook{
	MissingInOMS: {
		Check: []string{
			"Look at the Terminal on the exchange record: is it one of the firm's terminals (dealer, RMS square-off, CTCL)?",
			"Filter the OMS trade book by the client and time for an order that may not have reached the OMS feed.",
			"Ask the dealer whether this trade was placed directly on the exchange terminal.",
		},
		Outcomes: []Outcome{
			{If: "A dealer placed it from a firm terminal",
				Fix:  []string{"Book the trade into the OMS against the client so their position and margin are right.", "Check the client's margin on Live RMS.", "Mark resolved. The exchange record is already billed."},
				Note: "Trade {trade} placed from firm terminal {terminal} by dealer ___ for {ucc}; booked into the OMS."},
			{If: "Nobody recognises the trade",
				Fix:  []string{"Escalate to the compliance officer at once as a possible unauthorised trade.", "Tell the client and record the complaint if they dispute it.", "Resolve only after compliance decides. The trade stays billed as the exchange reported it."},
				Note: "Trade {trade} for {ucc} not recognised by any dealer; escalated to compliance on ___; decision: ___."},
		},
	},
	MissingInExchange: {
		Check: []string{
			"Make sure every exchange and segment file for the day is loaded (Files). A missing file makes every OMS fill look unconfirmed.",
			"Search the exchange trades for the same client, contract, side and time. It may be there under a different trade number.",
			"Check the order in the exchange's order log: rejected, cancelled or expired?",
		},
		Outcomes: []Outcome{
			{If: "An exchange file was missing",
				Fix:  []string{"Load the missing file on Files → Inbox.", "Run reconciliation again. The break clears itself when the trade matches."},
				Note: "Exchange file for ___ was not loaded; loaded and reconciled again."},
			{If: "The order was rejected or cancelled at the exchange",
				Fix:  []string{"Cancel the fill in the OMS so the client's position is removed.", "Tell the client the trade did not happen.", "Mark resolved. Nothing was billed."},
				Note: "OMS fill {trade} for {ucc} not confirmed by the exchange (order {order} rejected/cancelled); OMS fill cancelled, client informed."},
		},
	},
	TradeNoMismatch: {
		Check: []string{
			"Compare time, client, contract, side, quantity and price between the two records. All must be equal.",
		},
		Outcomes: []Outcome{
			{If: "Everything but the trade number matches",
				Fix:  []string{"Correct the OMS reference to the exchange trade number (vendor ticket if needed).", "Mark resolved. The exchange number is kept."},
				Note: "Same trade recorded under a different number in the OMS; exchange number {trade} kept."},
			{If: "Anything else differs too",
				Fix:  []string{"Treat it as the break for that field (quantity, price, client) and follow its steps."},
				Note: "Trade numbers differ and ___ also differs; handled as ___."},
		},
	},
	UCCMismatch: {
		Check: []string{
			"Ask the dealer which client placed the order.",
			"Get the evidence: order log, call recording or the client's written instruction.",
			"Check the time against the exchange's cut-off for client code modifications today.",
			"Check today's count of client code changes at the top of Trade reconciliation.",
		},
		Outcomes: []Outcome{
			{If: "The OMS client is right (punching error at the terminal)",
				Fix:  []string{"Before the cut-off, modify the client code on the exchange's own system.", "Here, click Client code modification → {oms}. It moves the trade and closes the break.", "Billing → Rebill changed trades.", "Contract notes → Generate notes, then Send pending."},
				Note: "Punching error: trade {trade} modified from {exchange} to {oms} on the exchange at ___; dealer ___ confirmed; evidence: ___.",
				CCM:  true},
			{If: "The exchange client is right",
				Fix:  []string{"Have the OMS corrected to the exchange client.", "Mark resolved. Both clients' bills and notes go out on the next run."},
				Note: "Exchange client {exchange} is correct for trade {trade}; OMS showed {oms} in error and has been corrected (ticket ___)."},
			{If: "The cut-off has passed",
				Fix:  []string{"The trade stays with the exchange client; it can no longer be modified.", "Escalate to the compliance officer, who decides how the error is put right between the clients and the firm.", "Mark resolved with the decision."},
				Note: "Trade {trade} found after the modification cut-off; stays with {exchange}. Compliance decision on ___: ___."},
		},
	},
	SideMismatch: {
		Check: []string{
			"The exchange side is final. Ask the dealer what the client asked for.",
			"Open the client on Live RMS: with the side reversed, their OMS position is wrong by twice the quantity.",
		},
		Outcomes: []Outcome{
			{If: "The OMS has the side wrong",
				Fix:  []string{"Correct the OMS so the client's position matches the exchange before the next session.", "Check the client's margin on Live RMS and act on any shortfall.", "Tell the client their actual position.", "Mark resolved."},
				Note: "Exchange side final for {trade}; OMS showed the opposite side; OMS corrected, margin checked, client informed."},
			{If: "The client wanted the other side (order punched wrong)",
				Fix:  []string{"The trade stands as executed. Escalate to the compliance officer as a dealer error.", "Mark resolved with their decision."},
				Note: "Order {order} for {ucc} punched on the wrong side; trade stands; escalated to compliance on ___: ___."},
		},
	},
	InstrumentMismatch: {
		Check: []string{
			"Compare symbol, expiry, strike and option type. The exchange contract is final.",
			"Check whether the OMS contract master is out of date (new expiry, changed lot size).",
		},
		Outcomes: []Outcome{
			{If: "The OMS shows the wrong contract",
				Fix:  []string{"Correct the OMS fill and, if needed, its contract master.", "Check the client's position and margin on Live RMS.", "Mark resolved."},
				Note: "Exchange contract is final for {trade}; OMS showed a different contract; OMS corrected, position checked."},
		},
	},
	QtyMismatch: {
		Check: []string{
			"Note the order number: {order}.",
			"Set the status filter to All and search for the order number: are there other exchange trades on the same order?",
			"Add up the exchange fills on the order and compare the total with the OMS quantity.",
		},
		Outcomes: []Outcome{
			{If: "Other fills add up to the OMS quantity",
				Fix:  []string{"Nothing is wrong with the client's position; the OMS merged the fills.", "Resolve this break and the matching Missing in OMS break with the same note."},
				Note: "Order {order}: the exchange filled it in parts (trade {trade} and ___); the OMS combined them into one fill of {oms}."},
			{If: "There is no other trade",
				Fix: []string{"The client got {exchange}, not {oms}. Have the OMS position corrected before the next session.",
					"If that can't be done by the open, ask the risk manager to cut the client's sell limit for this stock so they can't sell shares they don't have.",
					"Tell the client's RM, who tells the client. The contract note already shows {exchange}.",
					"Raise a ticket with the OMS vendor.",
					"Mark resolved. No rebilling is needed."},
				Note: "Exchange filled {exchange} (trade {trade}); OMS recorded {oms}. No other exchange trade on order {order}. OMS position corrected, vendor ticket ___, client informed by ___ on ___."},
		},
	},
	PriceMismatch: {
		Check: []string{
			"Check whether the OMS price is the average of several fills on the same order.",
			"Check the size of the difference and the trade time against the market at that moment.",
		},
		Outcomes: []Outcome{
			{If: "The OMS shows an average price",
				Fix:  []string{"Nothing to correct. Mark resolved."},
				Note: "OMS shows the average price of order {order}; exchange price {exchange} for trade {trade} is billed."},
			{If: "The OMS price is simply wrong",
				Fix:  []string{"Have the OMS fill price corrected.", "If the client was quoted the OMS price, tell them the contract note uses the exchange price.", "Mark resolved."},
				Note: "Exchange price {exchange} is final for {trade}; OMS showed {oms}; OMS corrected (ticket ___)."},
		},
	},
	AlgoIDMissing: {
		Check: []string{
			"Find the exchange's algo ID in the firm's list of strategies registered with the exchange.",
			"Check the client is approved to use that strategy.",
			"Check whether the OMS is set up to carry the algo ID on this client's orders.",
		},
		Outcomes: []Outcome{
			{If: "The ID is registered and the client is approved",
				Fix:  []string{"Have the OMS tagging fixed so the ID comes through (vendor ticket).", "Mark resolved."},
				Note: "Algo ID {exchange} on trade {trade} is registered strategy ___ approved for {ucc}; OMS missed the tag, ticket ___."},
			{If: "The ID is not registered, or the client is not approved",
				Fix:  []string{"Do not close it as a data problem. Escalate to the compliance officer, who decides on reporting to the exchange.", "Stop the client's API access if compliance asks.", "Mark resolved with their decision."},
				Note: "Algo ID {exchange} on trade {trade} is not registered/approved for {ucc}; escalated to compliance on ___; decision: ___."},
		},
	},
}
