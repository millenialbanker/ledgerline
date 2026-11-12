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

	// Imports being added on the Ledgerline side (fixed layouts; not yet in
	// ops.MasterSources on this branch).
	srcUCCDetails = "UCC-DETAILS"
	srcContacts   = "UCC-CONTACTS"
	srcBranches   = "BRANCHES"
	srcPartners   = "PARTNERS"
	srcDPAccounts = "DP-ACCOUNTS"
)

var headers = map[string][]string{
	// Inactive and InactiveSince are not read by the import yet: they carry
	// the "no active segment" state (Plan.DormantMode inactive).
	srcClients: {"UCC", "Name", "Type", "PAN", "DOB", "Mobile", "Email", "City", "Segments", "Risk", "Income", "RAS", "Bank", "IFSC", "Account",
		"Depository", "DematID", "Nominee", "NomineeName", "KYCDate", "Status", "API", "LastTrade", "UPIBlock",
		"Branch", "State", "GSTIN", "ECN", "NominationDate", "NominationMode", "AP", "RM", "Groups", "Inactive", "InactiveSince"},
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

	srcUCCDetails: {"UCC", "Address1", "City", "State", "PIN",
		"Category", "Address2", "Address3", "Country", "PermAddress1", "PermAddress2", "PermAddress3", "PermCity", "PermState", "PermCountry", "PermPIN",
		"Gender", "Guardian", "Marital", "Nationality", "Occupation", "OccupationDetail", "IncomeAsOn", "PEP", "IPV", "POAFunds", "POASecurities",
		"UPI", "AgreementDate", "CIN", "BankType", "MobileRelation", "EmailRelation"},
	srcContacts:   {"UCC", "Name", "Designation", "PAN", "Address", "Phone", "Email", "DIN"},
	srcBranches:   {"Code", "Name", "City", "Manager", "Parent", "Kind", "Active"},
	srcPartners:   {"Code", "Name", "Kind", "Branch", "Parent", "PAN", "GSTIN", "RegNo", "SharePct", "Bank", "Account", "IFSC", "Email", "Active"},
	srcDPAccounts: {"UCC", "BOID", "Depository", "Status", "Opened", "ClosedOn", "BSDA", "Freeze", "FreezeReason", "DDPI"},
}

// required columns of the imports not yet in ops.MasterSources (for the
// test; MasterSources is compared once it lists them).
var pendingRequired = map[string][]string{
	srcUCCDetails: {"UCC", "Address1", "City", "State", "PIN"},
	srcContacts:   {"UCC", "Name", "Designation"},
	srcBranches:   {"Code", "Name"},
	srcPartners:   {"Code", "Name", "Kind"},
	srcDPAccounts: {"UCC", "BOID", "Depository", "Status"},
}

// optionalNotYetRead are header columns the Ledgerline import does not read
// yet on this branch (it ignores unknown columns).
var optionalNotYetRead = map[string][]string{
	srcClients: {"AP", "RM", "Groups", "Inactive", "InactiveSince"},
	srcOpening: {"Name"},
}

// rec builds a row for a header from named values.
func rec(h []string, v map[string]string) []string {
	out := make([]string, len(h))
	for i, c := range h {
		out[i] = v[c]
	}
	return out
}
