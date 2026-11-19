package hdfcapi

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
)

// The CBX (Corporate Banking Experience) batch payment APIs, product
// "Corporate Banking Experience - Batch Payments v1": initiate up to 500
// payments, inquire a batch, and find a batch's number from the firm's
// file reference. Payments wait in CBX for the firm's own approvers as
// set up for its domain; the API only initiates, as the maker.
const (
	PathBulkPayment = "v1/cbx-nodal-bulkPayment"
	PathBulkInquiry = "v1/cbx-nodal-bulkPaymentInq"
	PathBatchNumber = "v2/cbx-getBatchNo"
	MaxBatch        = 500
)

// Transaction types (cdflag).
const (
	TypeInternal = "I" // HDFC to HDFC
	TypeNEFT     = "N"
	TypeRTGS     = "R"
	TypeIMPS     = "M"
)

// Str takes a JSON string or number (the bank's inquiry answers carry
// some fields as numbers).
type Str string

func (s *Str) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) > 0 && b[0] == '"' {
		var x string
		if err := json.Unmarshal(b, &x); err != nil {
			return err
		}
		*s = Str(x)
		return nil
	}
	if string(b) == "null" {
		*s = ""
		return nil
	}
	*s = Str(strings.TrimSpace(string(b)))
	return nil
}

// Payment is one line of a batch.
type Payment struct {
	Type      string `json:"cdflag"`
	BeneCode  string `json:"code,omitempty"`
	Account   string `json:"accno,omitempty"`
	Amount    string `json:"amount"`
	Name      string `json:"name,omitempty"`
	InstrRef  string `json:"instrefno,omitempty"`
	CustRef   string `json:"custrefno"` // unique per payment: how answers are matched
	Info1     string `json:"payaddinfo1,omitempty"`
	Info2     string `json:"payaddinfo2,omitempty"`
	Date      string `json:"reqdexctndt"` // DD/MM/YYYY
	IFSC      string `json:"ifsc,omitempty"`
	BankName  string `json:"bankname,omitempty"`
	Branch    string `json:"branch,omitempty"`
	Email     string `json:"email,omitempty"`
	LEIExpiry string `json:"adrline,omitempty"` // high-value transfers only
	LEICode   string `json:"prtlctn,omitempty"`
}

// BatchRequest initiates a batch.
type BatchRequest struct {
	ClientCode string    `json:"clientcode"`
	GroupID    string    `json:"groupid"`
	Maker      string    `json:"iduser"`
	Count      string    `json:"nooftran"`
	FileRef    string    `json:"filerefno"` // up to 6 characters, chosen by the firm
	Payments   []Payment `json:"trans"`
}

// BatchReply is the bank's acknowledgement.
type BatchReply struct {
	Text     Str `json:"txtstatus"` // ACCEPTED
	Code     Str `json:"codstatus"` // 0
	BatchNum Str `json:"batchnum"`
	FileRef  Str `json:"filerefno"`
	Count    Str `json:"nooftran"`
}

// Accepted reports whether the bank took the batch.
func (r BatchReply) Accepted() bool {
	return r.BatchNum != "" && (strings.EqualFold(string(r.Text), "ACCEPTED") || r.Code == "0")
}

// InquiryRequest asks for a batch's state.
type InquiryRequest struct {
	GroupID  string `json:"gcif"`
	Maker    string `json:"iduser"`
	BatchNum string `json:"batchnum"`
	Date     string `json:"reqdexctndt"` // the day the batch was sent, DD/MM/YYYY
	FileRef  string `json:"filerefno"`
}

// LineStatus is one payment in an inquiry answer.
type LineStatus struct {
	Type      Str `json:"cdflag"`
	BeneCode  Str `json:"code"`
	Name      Str `json:"name"`
	Amount    Str `json:"amount"`
	CustRef   Str `json:"custrefno"`
	Account   Str `json:"accno"`
	BankRef   Str `json:"bankrefno"` // FT number for HDFC-to-HDFC
	Status    Str `json:"codstatus"` // P processing, R rejected, C complete
	Reason    Str `json:"txtreason"`
	IFSC      Str `json:"ifsc"`
	UTR       Str `json:"refno"`
	RBIStatus Str `json:"rbistatus"` // TXSETT, TXSIP, TXREJE, TXDSETT
	RBIReason Str `json:"rbireason"`
}

// InquiryReply is a batch's state. The bank answers "accepted, enquire
// again" and "under process" before the real state, with no lines.
type InquiryReply struct {
	BatchNum Str          `json:"batchnum"`
	Count    Str          `json:"nooftran"`
	Code     Str          `json:"codstatus"`
	Message  Str          `json:"message"`
	FileRef  Str          `json:"filerefno"`
	Lines    []LineStatus `json:"trans"`
}

// BatchNumberRequest finds a batch by the firm's file reference.
type BatchNumberRequest struct {
	GroupID  string `json:"gcif"`
	Maker    string `json:"iduser"`
	BatchNum string `json:"batchnum,omitempty"`
	Date     string `json:"reqdexctndt"`
	FileRef  string `json:"filerefno"`
}

// BatchNumberReply is the batch found, if any.
type BatchNumberReply struct {
	BatchNum Str `json:"batchnum"`
	Code     Str `json:"codstatus"`
	Message  Str `json:"message"`
	FileRef  Str `json:"filerefno"`
	Count    Str `json:"nooftran"`
}

// SendBatch initiates a batch.
func (c *Client) SendBatch(ctx context.Context, txnID string, r BatchRequest) (BatchReply, error) {
	var out BatchReply
	err := c.Call(ctx, PathBulkPayment, txnID, r, &out)
	return out, err
}

// Inquire asks for a batch's state.
func (c *Client) Inquire(ctx context.Context, r InquiryRequest) (InquiryReply, error) {
	var out InquiryReply
	err := c.Call(ctx, PathBulkInquiry, "", r, &out)
	return out, err
}

// BatchNumber finds the bank's batch number for a file reference.
func (c *Client) BatchNumber(ctx context.Context, r BatchNumberRequest) (BatchNumberReply, error) {
	var out BatchNumberReply
	err := c.Call(ctx, PathBatchNumber, "", r, &out)
	return out, err
}
