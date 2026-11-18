package ops

// ---- FIU-IND FINGate 2.0: suspicious transaction reports ----
//
// When the Principal Officer decides an alert is suspicious (Surveillance &
// AML → File STR), the STR is due on FIU-IND's FINGate 2.0 within 7
// working days (PML Rules 8(2)). Ledgerline writes the FINGate "Bulk Data
// Upload" for the decided STRs (FINGate 2.0 Reports user manual v1.6,
// section 2 and Annexure 12.1; FIU-IND Reporting Format v1.14, section 2.5
// Brokerage Firms): a .zip with no folders holding one CSV per format, named
// as FINGate names them, each row tied to its report by the report
// reference number:
//
//   - KC1 / KC2: the client's KYC (individual / non-individual);
//   - Account_Detail and Account_Person_Relation: the trading account and
//     the client's relation to it;
//   - TS7: the client's trades in the STR's period (Brokerage Firms STR);
//   - GS1: the grounds of suspicion, from the Principal Officer's reasons.
//
// The Principal Officer uploads it on FINGate (My Reports → Bulk Data
// Upload, fresh data), creates the batch, signs it (DSC or eSign) and
// records the batch ID and report ID on the alert, which closes the work
// item. FINGate's own report API needs FIU-IND's separate API
// specification, not yet in hand.

const strDueWorkingDays = 7

// AlertSTR is the STR decided on an alert and its filing.
type AlertSTR struct {
	DecidedOn string `json:"decided_on"`
	DecidedBy string `json:"decided_by"`
	Due       string `json:"due"`
	Grounds   string `json:"grounds"`            // the Principal Officer's reasons (GS1 narration)
	From      string `json:"from"`               // the transactions reported: trade dates
	To        string `json:"to"`                 //
	Exported  string `json:"exported,omitempty"` // when the FINGate upload was last written
	Filing    string `json:"filing,omitempty"`   // FINGate batch and report IDs
	FiledOn   string `json:"filed_on,omitempty"`
	FiledBy   string `json:"filed_by,omitempty"`
}
