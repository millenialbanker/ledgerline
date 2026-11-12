package sharepro

// The go-live files, in Ledgerline's import layouts (ops.MasterSources).
// Each header is the import's required columns followed by the optional
// columns the import reads; the test checks them against MasterSources.
const (
	srcClients    = "CLIENT-MASTER"
	srcBanks      = "CLIENT-BANKS"
	srcNominees   = "NOMINEES"
	srcSegStatus  = "SEGMENT-STATUS"
	srcOpening    = "OPENING-BALANCES"
	srcHoldings   = "DP-HOLDINGS"
	srcPositions  = "POSITIONS"
	srcBrokRates  = "BROKERAGE-CLIENT-RATES"
	srcInterest   = "CLIENT-INTEREST"
	srcKRA        = "KRA-STATUS"
	srcCKYC       = "CKYC-NUMBERS"
	srcHoldLots   = "HOLDING-LOTS"
	srcBrokCards  = "BROKERAGE-CARDS"
	srcBrokClient = "BROKERAGE-CLIENTS"
)

var headers = map[string][]string{
	// Inactive and InactiveSince are not read by the import yet: they carry
	// the "no active segment" state (Plan.DormantMode inactive).
	srcClients: {"UCC", "Name", "Type", "PAN", "DOB", "Mobile", "Email", "City", "Segments", "Risk", "Income", "RAS", "Bank", "IFSC", "Account",
		"Depository", "DematID", "Nominee", "NomineeName", "KYCDate", "Status", "API", "LastTrade", "UPIBlock",
		"Branch", "State", "GSTIN", "ECN", "NominationDate", "NominationMode", "Inactive", "InactiveSince"},
	srcBanks:     {"UCC", "Bank", "IFSC", "Account", "Primary", "Status", "Branch", "MICR", "Type", "AddedOn"},
	srcNominees:  {"UCC", "Name", "Relation", "Share", "IDType", "ID", "DOB", "Guardian", "GuardianRelation", "Mobile", "Email", "Address", "DeclaredOn"},
	srcSegStatus: {"UCC", "Segment", "Status", "EffectiveDate", "Reason"},
	// Name is for people reading the file; the import ignores it.
	srcOpening:   {"Account", "Debit", "Credit", "Since", "Name"},
	srcHoldings:  {"UCC", "ISIN", "Symbol", "Qty", "Pledged", "AvgCost"},
	srcPositions: {"UCC", "Exchange", "Segment", "Symbol", "Type", "Expiry", "Strike", "Qty", "AvgPrice"},
	srcBrokRates: {"UCC", "From", "Product", "Mode", "Exchange", "Case", "Scrip", "Underlying", "Pct", "Flat", "PerLot", "PerShare",
		"MinPerShare", "MinPerLot", "Min", "Max", "MinPerNote", "Basis", "Note"},
	srcInterest: {"UCC", "From", "Rate", "GraceDays", "Free", "Note"},
	srcKRA:      {"PAN", "Status", "Remarks"},
	srcCKYC:     {"PAN", "CKYCNumber"},
}

// optionalNotYetRead are header columns the Ledgerline import does not read
// yet (it ignores unknown columns).
var optionalNotYetRead = map[string][]string{
	srcClients: {"Inactive", "InactiveSince"},
	srcOpening: {"Name"},
}

func colIndex(h []string) map[string]int {
	m := make(map[string]int, len(h))
	for i, c := range h {
		m[c] = i
	}
	return m
}

// rec builds a row for a header from named values.
func rec(h []string, v map[string]string) []string {
	out := make([]string, len(h))
	for i, c := range h {
		out[i] = v[c]
	}
	return out
}
