// Package control is Ledgerline Control: the provider's own console. Every
// Ledgerline installation (the hosted server, and brokers' own servers)
// reports in with what each firm runs and uses; Control keeps customers,
// usage, billing and support requests.
//
// This file is the report both sides share.
package control

// AppVersion is Ledgerline's version (set at build with -ldflags "-X ledgerline/internal/control.AppVersion=...").
var AppVersion = "1.5.0"

// Report is what an installation sends, every quarter hour.
type Report struct {
	Installation InstallInfo  `json:"installation"`
	Firms        []FirmReport `json:"firms"`
	Tickets      []TicketSync `json:"tickets,omitempty"`   // new requests and customer replies since the last report
	Acks         []CommandAck `json:"acks,omitempty"`      // commands carried out
	Delivered    []string     `json:"delivered,omitempty"` // MsgIDs of ticket updates received
	SentAt       string       `json:"sent_at"`
}

// InstallInfo describes the server.
type InstallInfo struct {
	Version   string `json:"version"`
	Region    string `json:"region"`
	IndiaOnly bool   `json:"india_only"`
	Database  string `json:"database"` // postgres or memory
	Started   string `json:"started"`
	Host      string `json:"host"`
	Go        string `json:"go"`
	OS        string `json:"os"`
}

// FirmReport is one firm (broker) on the installation.
type FirmReport struct {
	Tenant     string `json:"tenant"`
	Firm       string `json:"firm"`
	SEBIReg    string `json:"sebi_reg"`
	Edition    string `json:"edition"` // manual, autopilot, full
	Month      string `json:"month"`   // YYYY-MM the monthly counts are for
	BusinessDt string `json:"business_date"`

	Users        []UserInfo     `json:"users"`
	StaffActive  int            `json:"staff_active"` // active staff logins (not clients)
	ActiveToday  int            `json:"active_today"` // staff seen today
	Active30     int            `json:"active_30d"`   // staff seen in 30 days
	Clients      int            `json:"clients"`      // client accounts
	ClientsByStg map[string]int `json:"clients_by_stage"`
	TradedMonth  int            `json:"traded_month"` // clients who traded this month (billable)
	TradesMonth  int            `json:"trades_month"`
	KYCMonth     int            `json:"kyc_month"` // online applications verified (DigiLocker or offline e-KYC)
	AI           AIUse          `json:"ai"`
	Bots         BotUse         `json:"bots"`
	Features     map[string]int `json:"features"` // actions in the last 30 days, by area
	Health       Health         `json:"health"`
	LastActivity string         `json:"last_activity"`
}

// UserInfo is one login of the firm (staff, branches, APs).
type UserInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Role      string `json:"role"`
	Active    bool   `json:"active"`
	LastLogin string `json:"last_login,omitempty"`
	LastSeen  string `json:"last_seen,omitempty"`
}

// AIUse is the firm's AI metering for the month (amounts in paise).
type AIUse struct {
	Answers  int    `json:"answers"`
	CostINR  string `json:"cost_inr"`
	Charged  int64  `json:"charged"`
	Balance  int64  `json:"balance"`
	ToppedUp int64  `json:"topped_up"`
	On       bool   `json:"on"`
}

// BotUse is the firm's automations.
type BotUse struct {
	Computers int `json:"computers"`
	Workflows int `json:"workflows"`
	RunsMonth int `json:"runs_month"`
	Failed    int `json:"failed_month"`
}

// Health is how the firm's day is going.
type Health struct {
	EOD           string `json:"eod"` // open, closed
	LastBackup    string `json:"last_backup,omitempty"`
	BackupProblem string `json:"backup_problem,omitempty"`
	OpenWork      int    `json:"open_work"`
	OpenBreaks    int    `json:"open_breaks"`
}

// TicketSync is a support request, or a customer's reply to one.
type TicketSync struct {
	LocalID  string `json:"local_id"` // the request's ID in Ledgerline
	Tenant   string `json:"tenant"`
	User     string `json:"user"`
	UserName string `json:"user_name"`
	Role     string `json:"role"`
	Subject  string `json:"subject,omitempty"`
	Priority string `json:"priority,omitempty"`
	Category string `json:"category,omitempty"`
	Screen   string `json:"screen,omitempty"`
	Text     string `json:"text"`
	At       string `json:"at"`
	MsgID    string `json:"msg_id"` // unique per message, so repeats are ignored
	Close    bool   `json:"close,omitempty"`
}

// ReportReply is Control's answer: replies to support requests and
// commands for the installation.
type ReportReply struct {
	Updates  []TicketUpdate `json:"updates,omitempty"`
	Commands []Command      `json:"commands,omitempty"`
	Received int            `json:"received"`
}

// TicketUpdate is the provider's reply or a status change.
type TicketUpdate struct {
	LocalID string `json:"local_id"`
	Tenant  string `json:"tenant"`
	Ticket  string `json:"ticket"` // Control's ticket number
	Status  string `json:"status"`
	Text    string `json:"text,omitempty"`
	From    string `json:"from,omitempty"`
	At      string `json:"at"`
	MsgID   string `json:"msg_id"`
}

// Command is something Control asks the installation to do.
type Command struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"` // ai_credit
	Tenant string `json:"tenant"`
	Amount int64  `json:"amount,omitempty"` // paise
	Ref    string `json:"ref,omitempty"`
}

// CommandAck reports a command done (or failed).
type CommandAck struct {
	ID    string `json:"id"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}
