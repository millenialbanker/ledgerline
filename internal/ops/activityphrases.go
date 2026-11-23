package ops

// The activity feed's phrase table: how each common audit action reads as a
// sentence after the person's name, which area it belongs to and which
// screen it opens. Actions not in the table read from their module (the
// part before the first dot) with a plain fallback, so a new action shows
// up sensibly before anyone writes its phrase.
//
// A phrase is a template: {key} is a detail value (or {object}, the
// entry's object), {key|fmt|…} formats it, and a part in [square
// brackets] is left out when a value in it is missing. Formats:
//
//	inr              ₹ in lakh and crore ("₹3.2 lakh")
//	n|noun[|plural]  a count with its noun ("14 payouts")
//	len|noun[|plural] the number of items in a list, with its noun
//	file             a file layout's name ("NSE CM trade file")
//	date             a date as "16 Oct"
//	stage            "eod:2026-10-16" as "eod"
//	target           "ucc:BH1003" as "BH1003", "tag:HNI" as "tag HNI"
//	lower, words     lower case; underscores and dashes as spaces
//	bool|yes|no      one of two words
//
// Free text (reasons, notes) is never put in a sentence: the screen the
// entry opens shows it to those who may read it.

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

type actPhrase struct {
	area, view string
	text       string
	fn         func(x activityText) string
	secure     bool   // Admin and Compliance only
	quiet      bool   // left out of the feed (still in the audit trail)
	objUCC     bool   // the object is a client code
	uccKey     string // the detail value naming the client, when not "ucc"
}

type actModule struct {
	area, view, label string
	secure            bool
}

// activityModules: by the action's first part, or its first two parts
// where one module spans two areas ("cdsl.pledge").
var activityModules = map[string]actModule{
	// Operations
	"file":           {AreaOperations, "files", "file", false},
	"xfile":          {AreaOperations, "xfiles", "exchange file", false},
	"extranet":       {AreaOperations, "xfiles", "NSE extranet", false},
	"bse":            {AreaOperations, "xfiles", "BSE files", false},
	"mcx":            {AreaOperations, "xfiles", "MCX files", false},
	"notis":          {AreaOperations, "xfiles", "NOTIS", false},
	"frontoffice":    {AreaOperations, "xfiles", "front office file", false},
	"finspot_api":    {AreaOperations, "xfiles", "Finspot API", false},
	"bow":            {AreaOperations, "xfiles", "BOW", false},
	"ncms":           {AreaOperations, "ccdata", "NCMS", false},
	"ccil":           {AreaOperations, "ccdata", "CCIL prices", false},
	"recon":          {AreaOperations, "recon", "trade reconciliation", false},
	"trades":         {AreaOperations, "recon", "trades", false},
	"ccm":            {AreaOperations, "recon", "client code modification", false},
	"billing":        {AreaOperations, "billing", "billing", false},
	"bill":           {AreaOperations, "billing", "bill", false},
	"settlement":     {AreaOperations, "settlement", "settlement", false},
	"shortage":       {AreaOperations, "settlement", "shortage", false},
	"derivatives":    {AreaOperations, "settlement", "derivatives settlement", false},
	"positions":      {AreaOperations, "settlement", "positions", false},
	"eod":            {AreaOperations, "dashboard", "day close", false},
	"notes":          {AreaOperations, "notes", "contract notes", false},
	"client":         {AreaOperations, "clients", "client", false},
	"client_banks":   {AreaOperations, "clients", "client bank accounts", false},
	"client_demats":  {AreaOperations, "clients", "client demat accounts", false},
	"nominees":       {AreaOperations, "clients", "nominees", false},
	"external_trade": {AreaOperations, "clients", "trade elsewhere", false},
	"verify":         {AreaOperations, "clients", "verification", false},
	"portal":         {AreaOperations, "clients", "client portal", false},
	"ucc":            {AreaOperations, "clients", "UCC details", false},
	"change":         {AreaOperations, "changes", "change request", false},
	"eapply":         {AreaOperations, "apps", "online application", false},
	"onboarding":     {AreaOperations, "apps", "onboarding", false},
	"uccapi":         {AreaOperations, "uccapi", "exchange UCC", false},
	"bseucc":         {AreaOperations, "uccapi", "BSE UCC", false},
	"mcxucc":         {AreaOperations, "uccapi", "MCX UCC", false},
	"segment_status": {AreaOperations, "uccapi", "segment status", false},
	"mnrl":           {AreaOperations, "mnrl", "revoked mobiles list", false},
	"corpaction":     {AreaOperations, "corp", "corporate action", false},
	"dividend":       {AreaOperations, "corp", "dividend", false},
	"buyback":        {AreaOperations, "corp", "buyback", false},
	"client_report":  {AreaOperations, "statements", "client report", false},
	"statements":     {AreaOperations, "dispatch", "statements", false},
	"dispatch":       {AreaOperations, "dispatch", "dispatch", false},
	"message":        {AreaOperations, "messages", "client messages", false},
	"whatsapp":       {AreaOperations, "messages", "WhatsApp", false},
	"template":       {AreaOperations, "messages", "message template", false},
	"letter":         {AreaOperations, "messages", "letter", false},
	"yearend":        {AreaOperations, "yearend", "year-end packs", false},
	"archive":        {AreaOperations, "archive", "old documents archive", false},
	"history":        {AreaOperations, "archive", "history", false},
	"autopilot":      {AreaOperations, "autopilot", "autopilot", false},
	"work":           {AreaOperations, "autopilot", "work item", false},
	"job":            {AreaOperations, "jobs", "scheduled job", false},
	"periodic":       {AreaOperations, "periodic", "periodic work", false},
	"rpa":            {AreaOperations, "rpa", "browser automation", false},
	"migration":      {AreaOperations, "migrate", "migration", false},
	"lead":           {AreaOperations, "crm", "lead", false},
	"insights":       {AreaOperations, "insights", "client insights", false},
	"insight":        {AreaOperations, "insights", "client insight", false},
	"partner":        {AreaOperations, "partners", "partner", false},
	"branch":         {AreaOperations, "partners", "branch", false},
	"ap":             {AreaOperations, "partners", "authorised person", false},
	"dealer":         {AreaOperations, "dealers", "dealer", false},
	"notice":         {AreaOperations, "dashboard", "notice", false},
	"dashboard":      {AreaOperations, "dashboard", "dashboard", false},
	// Risk
	"rms":              {AreaRisk, "rms", "RMS", false},
	"bow.funds":        {AreaRisk, "rms", "BOW limits", false},
	"margin":           {AreaRisk, "margin", "margin", false},
	"nclmargins":       {AreaRisk, "margin", "NCL margins", false},
	"segmon":           {AreaRisk, "margin", "segment monitoring", false},
	"payouthold":       {AreaRisk, "payouthold", "pay-out hold", false},
	"collateral":       {AreaRisk, "collateral", "collateral", false},
	"cuspa":            {AreaRisk, "collateral", "CUSPA", false},
	"pledge":           {AreaRisk, "collateral", "pledge", false},
	"cdsl.pledge":      {AreaRisk, "cdslpledge", "CDSL pledge", false},
	"cdsl.npledge":     {AreaRisk, "cdslpledge", "CDSL margin pledge", false},
	"cdsl.repledge":    {AreaRisk, "cdslpledge", "CDSL re-pledge", false},
	"cdsl.unpledge":    {AreaRisk, "cdslpledge", "CDSL unpledge", false},
	"cdsl.invocation":  {AreaRisk, "cdslpledge", "CDSL invocation", false},
	"cdsl.release":     {AreaRisk, "cdslpledge", "CDSL release", false},
	"cdsl.epi":         {AreaRisk, "cdslepi", "CDSL early pay-in", false},
	"allocation":       {AreaRisk, "allocation", "clearing allocation", false},
	"short_allocation": {AreaRisk, "allocation", "short allocation", false},
	"ccdeposit":        {AreaRisk, "deposits", "clearing deposit", false},
	"debit":            {AreaRisk, "debits", "debit balance", false},
	"interest":         {AreaRisk, "debits", "interest", false},
	"legacy":           {AreaRisk, "legacy", "legacy debt", false},
	"mtf":              {AreaRisk, "mtf", "MTF", false},
	// Accounts
	"payout":     {AreaAccounts, "money", "payout", false},
	"receipt":    {AreaAccounts, "money", "receipt", false},
	"gateway":    {AreaAccounts, "money", "payment gateway", false},
	"upi":        {AreaAccounts, "money", "UPI", false},
	"upix":       {AreaAccounts, "money", "UPI", false},
	"hdfc":       {AreaAccounts, "money", "HDFC Bank", false},
	"hdfcpay":    {AreaAccounts, "money", "HDFC payments", false},
	"funds":      {AreaAccounts, "clientmoney", "client funds", false},
	"bank":       {AreaAccounts, "bank", "bank", false},
	"ras":        {AreaAccounts, "ras", "running account", false},
	"money":      {AreaAccounts, "clientmoney", "client money", false},
	"journal":    {AreaAccounts, "ledger", "journal", false},
	"ledger":     {AreaAccounts, "ledger", "ledger", false},
	"account":    {AreaAccounts, "ledger", "account", false},
	"voucher":    {AreaAccounts, "books", "voucher", false},
	"invoice":    {AreaAccounts, "books", "invoice", false},
	"vendor":     {AreaAccounts, "books", "vendor", false},
	"vendorbill": {AreaAccounts, "books", "vendor bill", false},
	"firmbank":   {AreaAccounts, "books", "firm bank account", false},
	"referrals":  {AreaAccounts, "books", "referral payouts", false},
	"brokerage":  {AreaAccounts, "brokerage", "brokerage", false},
	"prepaid":    {AreaAccounts, "prepaid", "prepaid scheme", false},
	// Compliance
	"kyc":            {AreaCompliance, "kyc", "KYC", false},
	"kra":            {AreaCompliance, "kyc", "KRA", false},
	"pmla":           {AreaCompliance, "kyc", "PMLA", false},
	"fatca":          {AreaCompliance, "kyc", "FATCA/CRS", false},
	"debarred":       {AreaCompliance, "kyc", "debarred list", false},
	"sanctions":      {AreaCompliance, "kyc", "sanctions screening", false},
	"alert":          {AreaCompliance, "surv", "surveillance alert", false},
	"surveillance":   {AreaCompliance, "surv", "surveillance", false},
	"otr":            {AreaCompliance, "surv", "order-to-trade ratio", false},
	"inactive":       {AreaCompliance, "inactive", "inactive accounts", false},
	"reactivation":   {AreaCompliance, "inactive", "reactivation", false},
	"deceased":       {AreaCompliance, "deceased", "deceased client", false},
	"nri":            {AreaCompliance, "nri", "NRI (PIS)", false},
	"ticket":         {AreaCompliance, "helpdesk", "investor complaint", false},
	"raia":           {AreaCompliance, "helpdesk", "RA/IA complaints", false},
	"impersonation":  {AreaCompliance, "helpdesk", "impersonation case", false},
	"report":         {AreaCompliance, "reports", "regulatory report", false},
	"disclosure":     {AreaCompliance, "exdisc", "exchange disclosure", false},
	"ai":             {AreaCompliance, "aictl", "AI controls", false},
	"circular":       {AreaCompliance, "circulars", "circular", false},
	"obligation":     {AreaCompliance, "circulars", "compliance duty", false},
	"compliance":     {AreaCompliance, "circulars", "compliance profile", false},
	"selfinspect":    {AreaCompliance, "selfinspect", "self-inspection", false},
	"inspack":        {AreaCompliance, "inspack", "inspection pack", false},
	"security":       {AreaCompliance, "incidents", "security", true},
	"support_access": {AreaAdmin, "supportaccess", "support access", true},
	// Depository
	"dp":      {AreaDepository, "dp", "demat account", false},
	"dis":     {AreaDepository, "dp", "DIS", false},
	"holding": {AreaDepository, "dp", "holding statement", false},
	"cdsl":    {AreaDepository, "cdslreq", "CDSL", false},
	"edis":    {AreaDepository, "edis", "eDIS", false},
	// Investments
	"ipo":          {AreaInvestments, "ipo", "IPO", false},
	"fingate":      {AreaInvestments, "ipo", "Fingate", false},
	"mf":           {AreaInvestments, "mforders", "mutual fund", false},
	"mf.sip":       {AreaInvestments, "mfsips", "MF SIP", false},
	"mf.mandate":   {AreaInvestments, "mfmandates", "MF mandate", false},
	"mf.units":     {AreaInvestments, "mfunits", "MF units", false},
	"mf.nav":       {AreaInvestments, "mfunits", "MF NAV", false},
	"mf.trail":     {AreaInvestments, "mfcomm", "MF commission", false},
	"mf.settings":  {AreaInvestments, "mfsettings", "MF settings", false},
	"mf.profile":   {AreaInvestments, "mfsettings", "MF profile", false},
	"mf.route":     {AreaInvestments, "mfsettings", "MF route", false},
	"mf.platform":  {AreaInvestments, "mfsettings", "MF platform", false},
	"stocksip":     {AreaInvestments, "stocksip", "stock SIP", false},
	"rfq":          {AreaInvestments, "rfq", "RFQ bond deal", false},
	"cbrics":       {AreaInvestments, "cbrics", "CBRICS", false},
	"cbrics.party": {AreaInvestments, "cbrics", "CBRICS party", false},
	// Admin
	"user":             {AreaAdmin, "settings", "user", true},
	"auth":             {AreaAdmin, "settings", "sign-in", true},
	"settings":         {AreaAdmin, "settings", "settings", false},
	"firm":             {AreaAdmin, "settings", "firm profile", false},
	"holidays":         {AreaAdmin, "settings", "holidays", false},
	"edition":          {AreaAdmin, "settings", "edition", false},
	"connection":       {AreaAdmin, "connections", "connection", false},
	"backup":           {AreaAdmin, "backups", "backup", false},
	"firmkey":          {AreaAdmin, "settings", "encryption key", true},
	"signing":          {AreaAdmin, "settings", "document signing", false},
	"support":          {AreaAdmin, "settings", "Ledgerline support", false},
	"support_operator": {AreaAdmin, "supportaccess", "support operator", true},
	"dataexport":       {AreaAdmin, "dataexit", "data export", true},
	"exit":             {AreaAdmin, "dataexit", "account closing", true},
	"ui":               {AreaAdmin, "layout", "screen layout", false},
}

// activityPhrases: the common actions. Status actions built at run time
// ("payout."+status) are listed under each status they can take.
var activityPhrases = map[string]actPhrase{
	// Files and exchange files.
	"file.ingested":        {fn: phFileLoaded},
	"file.rejected":        {text: "could not load {source|file}[ ({row_errors|n|row error})]"},
	"file.malware_found":   {text: "had file {object} refused: malware found", secure: true},
	"file.scan_failed":     {text: "could not scan file {object} for malware"},
	"file.scan_refused":    {text: "had file {object} refused: too large to scan"},
	"file.not_scanned":     {text: "stored file {object} without a malware scan (too large)"},
	"xfile.received":       {text: "received exchange file {object}[ ({rows|n|row})][: {loaded|lower}]"},
	"xfile.written":        {text: "made exchange file {object}[ ({clients|n|client})]"},
	"xfile.missing":        {text: "flagged a missing file: {title}[ from {party}]"},
	"xfile.missing_digest": {text: "sent the missing-files alert[ ({files|n|file})]"},
	"xfile.alert_settings": {text: "changed the missing-file alerts"},
	"extranet.fetched":     {text: "fetched NSE extranet files[ ({files|n|file})][ for {object|date}]"},
	"bse.sftp_fetched":     {text: "fetched BSE files[ ({files|n|file})]"},
	"mcx.sftp_fetched":     {text: "fetched MCX files[ ({files|n|file})]"},
	"notis.fetched":        {quiet: true}, // every few minutes in market hours
	"ccil.prices_fetched":  {text: "fetched CCIL prices[ ({prices|n|price})]"},
	"ncms.fetched":         {text: "fetched NCMS data"},
	// The trading day.
	"recon.run":            {text: "ran trade reconciliation[ for {date|date}][: {matched|n|trade} matched][, {breaks|n|break}]"},
	"recon.break.resolved": {text: "resolved reconciliation break {object}[ ({type|words})]"},
	"billing.run":          {fn: phBilling, uccKey: "client"},
	"journal.posted":       {text: "posted journal entry {object}"},
	"journal.reversed":     {text: "reversed journal entry {object}"},
	"notes.generated":      {text: "made contract notes[ for {object|date}][ ({count|n|note})]"},
	"notes.sent":           {text: "sent contract notes[ for {object|date}][ ({sent|n|note} sent, {bounced|n|bounce})]"},
	"notes.revised":        {text: "revised a contract note for {object}", objUCC: true},
	"eod.closed":           {text: "closed[ {part}] for {object|date}"},
	"settlement.marked":    {text: "marked settlement {object}"},
	"shortage.recorded":    {text: "recorded a shortage[ of {qty|n|share}][ for {ucc}]"},
	"corpaction.applied":   {text: "applied corporate action {object}[ ({type|lower})][ to {clients|n|client}]"},
	"dividend.credited":    {text: "credited dividend {object}[ ({credited|inr})]"},
	"autopilot.stage":      {text: "ran the {object|stage} autopilot stage[ ({runs|n|step})]"},
	"autopilot.policy":     {text: "changed autopilot setting {object}"},
	"autopilot.schedule":   {text: "[{on|bool|switched on|switched off} ]the autopilot schedule"},
	"work.done":            {text: "approved work item {object}"},
	"work.dismissed":       {text: "dismissed work item {object}"},
	"job.run":              {text: "ran scheduled job {object}[: {status|lower}]"},
	"job.saved":            {text: "changed scheduled job {object}"},
	"periodic.run":         {text: "ran periodic work {object}[: {status|lower}]"},
	// Clients.
	"client.application.created": {text: "opened an application for {object}", objUCC: true},
	"client.stage":               {text: "moved {object} to {stage}", objUCC: true},
	"client.trading_opened":      {text: "opened trading for {object}", objUCC: true},
	"client.segment_status":      {text: "changed {object}'s[ {segment}] segment[ to {to}]", objUCC: true},
	"client.mapped":              {text: "changed {object}'s branch and partner mapping", objUCC: true},
	"client.groups":              {text: "changed {object}'s client groups", objUCC: true},
	"client.privacy":             {text: "[{private|bool|made {object} private|made {object} visible to all}]", objUCC: true},
	"client.document.added":      {text: "added a document[ ({kind|words})] for {object}", objUCC: true},
	"client.document.viewed":     {quiet: true}, // every look at a document; in the audit trail
	"client.reveal":              {text: "looked at {object}'s full[ {field|lower}]", objUCC: true, secure: true},
	"client.inactive":            {text: "flagged {object} inactive", objUCC: true, area: AreaCompliance, view: "inactive"},
	"client.kyc_updated":         {text: "updated {object}'s KYC details", objUCC: true, area: AreaCompliance, view: "kyc"},
	"client.debarred":            {text: "blocked {object}: on a regulator's debarred list", objUCC: true, area: AreaCompliance, view: "kyc"},
	"client.whatsapp":            {text: "[{opt_in|bool|recorded WhatsApp consent|recorded a WhatsApp opt-out}] for {object}", objUCC: true, view: "messages"},
	"client_report.downloaded":   {text: "downloaded a[ {kind|words}] report for {object}", objUCC: true},
	"client_report.printed":      {text: "printed a[ {kind|words}] report for {object}", objUCC: true},
	"change.requested":           {text: "raised change request {object}[ ({kind|words})][ for {ucc}]"},
	"change.approved":            {text: "approved change request {object}[ ({kind|words})][ for {ucc}]"},
	"change.rejected":            {text: "rejected change request {object}[ for {ucc}]"},
	"change.applied":             {text: "applied change request {object}[ for {ucc}]"},
	"change.client_confirmed":    {text: "confirmed change request {object}"},
	"change.client_refused":      {text: "refused change request {object}"},
	"change.expired":             {text: "closed change request {object}: the client did not confirm"},
	"eapply.submitted":           {text: "submitted online application {object}"},
	"eapply.rejected":            {text: "rejected online application {object}"},
	"eapply.started":             {text: "started online application {object}"},
	"eapply.document.viewed":     {quiet: true},
	"uccapi.nse.sent":            {text: "sent clients to NSE UCC[ ({clients|n|client}, {accepted|n|accepted|accepted}, {rejected|n|rejected|rejected})]"},
	"bseucc.file_uploaded":       {text: "uploaded the BSE UCC file[ ({clients|n|client})]"},
	"kyc.updated":                {text: "recorded a KYC update for {object}", objUCC: true},
	"pmla.risk":                  {text: "set {object}'s PMLA risk[ to {risk}]", objUCC: true},
	"kra.batch.uploaded":         {text: "uploaded KRA batch {object}"},
	"reactivation.requested":     {text: "asked to reactivate {object}", objUCC: true},
	"reactivation.approved":      {text: "reactivated {object}", objUCC: true},
	"reactivation.rejected":      {text: "refused to reactivate {object}", objUCC: true},
	"alert.raised":               {text: "raised a surveillance alert {object}[ ({type|words})][ for {ucc}]"},
	"alert.referred":             {text: "referred surveillance alert {object}"},
	"surveillance.rules.run":     {text: "ran the surveillance rules[: {new_alerts|n|new alert}]"},
	"ticket.opened":              {text: "opened complaint {object}[ ({category|words})][ for {ucc}]"},
	"ticket.updated":             {text: "updated complaint {object}[: {status|lower}]"},
	"statements.sent":            {fn: phStatements},
	"yearend.sent":               {text: "sent year-end packs for {object}[ ({sent|n|pack})]"},
	"message.bulk":               {text: "sent a bulk message ({object})"},
	"lead.created":               {text: "added lead {object}"},
	// Money.
	"receipt.recorded":            {text: "recorded receipt {object}[ of {amount|inr}][ from {ucc}]"},
	"receipt.approved":            {text: "approved receipt {object}[ ({amount|inr})][ for {ucc}]"},
	"receipt.rejected":            {text: "rejected receipt {object}[ for {ucc}]"},
	"receipt.gateway":             {text: "received {amount|inr} by payment gateway[ from {ucc}]"},
	"payout.requested":            {text: "raised payout {object}[ of {amount|inr}][ for {ucc}]"},
	"payout.approved":             {text: "approved payout {object}[ ({amount|inr})][ for {ucc}]"},
	"payout.rejected":             {text: "rejected payout {object}[ for {ucc}]"},
	"payout.cancelled":            {text: "cancelled payout {object}[ for {ucc}]"},
	"payout.batch":                {text: "released payout batch {object}[ ({items|n|payout}, {total|inr})]"},
	"payout.confirmed":            {text: "confirmed payout batch {object}[: {paid|n|payout} paid][, {failed|n|failed|failed}]"},
	"payout.bank_file":            {text: "made bank payment file {name}[ ({rows|n|payment}, {total|inr})]"},
	"payout.bank_file.downloaded": {text: "downloaded bank payment file {name}"},
	"payout.hdfc_api_sent":        {text: "sent payout batch {object} to HDFC Bank[ ({lines|n|payment}, {total|inr})]"},
	"payout.hdfc_api_lookup":      {quiet: true}, // polling the bank
	"payout.returned":             {text: "matched a returned payout {object}[ ({amount|inr})]"},
	"bank.credit.identified":      {text: "matched a bank credit[ of {amount|inr}][ to {ucc}]"},
	"bank.credit.live":            {text: "received a bank credit[ of {amount|inr}][ for {ucc}]"},
	"bank.credit.returned":        {text: "returned a bank credit[ of {amount|inr}]"},
	"ras.run":                     {text: "ran the running-account settlement[ for {object|date}][ ({clients|n|client}, {total|inr})]"},
	"money.recon.prepared":        {text: "prepared client money reconciliation for {object|date}"},
	"money.recon.signed":          {text: "signed client money reconciliation for {object|date}"},
	"money.transfer.recorded":     {text: "recorded a client money transfer[ of {amount|inr}]"},
	"interest.posted":             {text: "posted interest[ of {amount|inr}][ for {clients|n|client}]"},
	"voucher.entered":             {text: "entered voucher {object}[ ({total|inr})]"},
	"vendorbill.booked":           {text: "booked vendor bill {bill_no}[ ({total|inr})]"},
	"ledger.transfer.posted":      {text: "moved {amount|inr} between ledgers[ for {ucc}]"},
	"brokerage.assigned":          {text: "moved {object|target} to brokerage plan {plan}"},
	"brokerage.plan_saved":        {text: "saved brokerage plan {object}"},
	// Risk.
	"rms.override.created":      {text: "asked for an RMS limit override[ of {amount|inr}][ for {ucc}]"},
	"rms.override.approved":     {text: "approved an RMS limit override[ of {amount|inr}][ for {ucc}]"},
	"rms.override.rejected":     {text: "rejected an RMS limit override[ for {ucc}]"},
	"rms.squareoff":             {text: "squared off[ {clients|len|client}][ ({instructions|n|instruction})]"},
	"margin.penalties.prepared": {text: "prepared margin penalties[ for {object|date}][ ({items|n|item})]"},
	"mtf.funded":                {text: "funded an MTF position[ of {funded|inr}][ for {ucc}]"},
	"mtf.calls":                 {text: "raised MTF margin calls[ ({raised|n|call})]"},
	"debit.blocked":             {text: "blocked {object} for an old debit[ of {debit|inr}]", objUCC: true},
	"debit.unblocked":           {text: "unblocked {object}", objUCC: true},
	"legacy.written_off":        {text: "wrote off a legacy debt[ of {amount|inr}] for {object}", objUCC: true},
	"payouthold.applied":        {text: "applied pay-out holds[ ({holds|n|hold}, {total|inr})]"},
	"payouthold.proposed":       {text: "proposed a pay-out hold[ of {hold|inr}][ for {ucc}]"},
	"allocation.sent":           {text: "sent clearing allocation {object}[ ({records|n|record})]"},
	"allocation.prepared":       {text: "prepared clearing allocation[ for {date|date}][ ({records|n|record})]"},
	"cuspa.invoked":             {text: "moved unpaid shares to CUSPA[ for {ucc}]"},
	"cuspa.sold":                {text: "sold CUSPA shares[ ({qty|n|share})]"},
	"cdsl.pledge.requested":     {text: "asked for a pledge[ of {qty|n|share}][ from {ucc}]"},
	"cdsl.pledge.accepted":      {text: "recorded a pledge accepted by CDSL[ ({qty|n|share})]"},
	"nclmargins.fetched":        {text: "fetched NCL margins[ ({rows|n|row})]"},
	// Depository.
	"dp.bo_opened":      {text: "opened a demat account for {object}", objUCC: true},
	"dp.billed":         {text: "made DP bills for {object}[ ({bills|n|bill})]"},
	"dp.request":        {text: "raised DP request {object}[ ({kind|words})][ for {ucc}]"},
	"dis.entered":       {text: "entered DIS slip[ {slip}][ for {ucc}]"},
	"dis.uploaded":      {text: "uploaded DIS instructions[ ({instructions|n|instruction})]"},
	"edis.requested":    {text: "asked for an eDIS authorisation[ from {ucc}]"},
	"cdsl.request":      {text: "raised CDSL request {object}[ ({kind|words})][ for {ucc}]"},
	"cdsl.upload_sent":  {text: "sent a CDSL upload[ ({kind|words})]"},
	"cdsl.uploaded":     {text: "uploaded CDSL instructions[ ({instructions|n|instruction})]"},
	"cdsl.file_viewed":  {quiet: true},
	"holding.submitted": {text: "submitted holdings to the exchange[ ({records|n|record})]"},
	// Investments.
	"ipo.issue_saved":      {text: "saved issue {object}"},
	"ipo.bid_placed":       {text: "placed an IPO bid[ in {issue}][ of {amount|inr}][ for {ucc}]"},
	"ipo.bid_cancel":       {text: "cancelled an IPO bid {object}"},
	"ipo.order_placed":     {text: "placed an issue order[ in {issue}][ for {ucc}]"},
	"ipo.orders_sent":      {text: "sent issue orders for {object}[ ({sent|n|order})]"},
	"ipo.eipo_sent":        {text: "sent bids to NSE eIPO[ for {issue}][ ({applications|n|application})]"},
	"ipo.allotment_posted": {text: "posted allotment for {object}[ ({posted|n|client})]"},
	"mf.order.placed":      {text: "placed a mutual fund order[ of {amount|inr}][ for {ucc}]"},
	"mf.order.allotted":    {text: "recorded allotment of MF order {object}"},
	"mf.sip.registered":    {text: "registered an MF SIP[ of {amount|inr}][ for {ucc}]"},
	"mf.nav.loaded":        {text: "loaded MF NAVs[ ({schemes|n|scheme})]"},
	"mf.platform.polled":   {quiet: true},
	"stocksip.created":     {text: "started a stock SIP[ in {symbol}][ for {ucc}]"},
	"stocksip.run":         {text: "ran stock SIPs for {object|date}"},
	"stocksip.sent":        {text: "sent stock SIP orders[ ({placed|n|order})]"},
	"rfq.settled":          {text: "settled RFQ deal {object}"},
	"cbrics.deal.entered":  {text: "entered CBRICS deal {object}[ for {ucc}]"},
	// Compliance work.
	"report.downloaded":          {text: "downloaded report {object}[ for {date|date}]"},
	"report.generate":            {text: "made regulatory report {object}[ for {date|date}][ ({rows|n|row})]"},
	"report.validate":            {text: "checked regulatory report {object}[ for {date|date}]"},
	"report.upload":              {text: "marked regulatory report {object} uploaded[ for {date|date}]"},
	"debarred.screened":          {text: "checked clients against the debarred lists[: {blocked|n|client} blocked]"},
	"pledge.accepted":            {text: "accepted a pledge[ of {qty|n|share}][ from {ucc}]"},
	"pledge.rejected":            {text: "rejected a pledge[ from {ucc}]"},
	"ap.mapping.changed":         {text: "changed {object}'s authorised person[ to {to}]", objUCC: true},
	"branch.saved":               {text: "saved branch {object}"},
	"partner.saved":              {text: "saved partner {object}"},
	"prepaid.scheme.saved":       {text: "saved prepaid scheme {object}"},
	"obligation.done":            {text: "marked duty {object} done[ for {period}]"},
	"selfinspect.run":            {text: "ran self-inspection[: {passed|n|check} passed][, {failed|n|failed|failed}]"},
	"inspack.built":              {text: "built inspection pack {object}[ ({files|n|file})]"},
	"disclosure.submitted":       {text: "submitted exchange disclosure {object}"},
	"security.incident.opened":   {text: "opened security incident {object}[ ({severity|lower})]"},
	"security.incident.recorded": {text: "recorded security incident {object}[ ({severity|lower})]"},
	"security.incident.updated":  {text: "updated security incident {object}[: {status|lower}]"},
	"security.audit.verified":    {text: "checked the audit trail seals[: {ok|bool|intact|problems found}]"},
	// Admin and security.
	"auth.login":                     {text: "signed in"},
	"auth.login_failed":              {text: "failed to sign in[ ({reason})]"},
	"auth.logout":                    {quiet: true},
	"auth.password_changed":          {text: "changed their password"},
	"auth.2fa_enabled":               {text: "switched on two-factor sign-in"},
	"user.saved":                     {text: "saved user {object}[ ({role})]"},
	"user.created":                   {text: "added user {object}[ ({role})]"},
	"user.role_changed":              {text: "changed {object}'s role[ to {to}]"},
	"user.deactivated":               {text: "deactivated user {object}"},
	"user.password_reset":            {text: "reset {object}'s password"},
	"user.2fa_reset":                 {text: "reset {object}'s two-factor sign-in"},
	"settings.saved":                 {text: "changed the firm's settings"},
	"settings.security_changed":      {text: "changed sign-in security settings", secure: true},
	"connection.saved":               {text: "changed connection {object}"},
	"connection.removed":             {text: "removed connection {object}"},
	"connection.tested":              {text: "tested connection {object}[: {status|lower}]"},
	"connection.used":                {quiet: true}, // every call through a connection
	"connection.secret_rotated":      {text: "changed the password of connection {object}", secure: true},
	"backup.created":                 {text: "made backup {object}"},
	"backup.failed":                  {text: "could not make backup {object}"},
	"backup.verified":                {text: "checked backup {object}"},
	"firmkey.moved":                  {text: "moved the firm onto its own encryption key"},
	"firmkey.rotated":                {text: "rotated the firm's encryption key"},
	"firmkey.unavailable":            {text: "could not reach the firm's encryption key"},
	"firmkey.restored":               {text: "reached the firm's encryption key again"},
	"signing.agent.key_reset":        {text: "reset the USB token signer's key", secure: true},
	"hdfcpay.key_made":               {text: "made a new HDFC payments key", secure: true},
	"nclmargins.key_made":            {text: "made a new NCL margins key", secure: true},
	"rpa.cred.save":                  {text: "saved a browser automation sign-in", secure: true},
	"rpa.cred.delete":                {text: "removed a browser automation sign-in", secure: true},
	"rpa.secret.use":                 {quiet: true},
	"rpa.run.start":                  {text: "started browser automation {object}"},
	"rpa.run.end":                    {text: "finished browser automation {object}"},
	"support_access.requested":       {text: "asked for support access {object}"},
	"support_access.approved":        {text: "approved support access {object}"},
	"support_access.refused":         {text: "refused support access {object}"},
	"support_access.revoked":         {text: "ended support access {object}"},
	"support_access.session_started": {text: "started support session {object}"},
	"support_access.expired":         {text: "ended support access {object}"},
	"support_access.action":          {quiet: true}, // every request a support session makes
	"support_access.action_refused":  {text: "was refused a request in support session {object}"},
	"migration.loaded":               {text: "loaded migration {object}"},
	"migration.approved":             {text: "approved migration {object}"},
	"dataexport.requested":           {text: "asked for full data export {object}"},
	"dataexport.approved":            {text: "approved full data export {object}"},
	"dataexport.ready":               {text: "finished full data export {object}"},
	"dataexport.downloaded":          {text: "downloaded full data export {object}"},
	"exit.requested":                 {text: "asked to close the firm's Ledgerline account"},
	"exit.approved":                  {text: "approved closing the firm's Ledgerline account"},
	"exit.cancelled":                 {text: "cancelled closing the firm's Ledgerline account"},
	"ui.layout_mine":                 {quiet: true},
	"ui.layout_policy":               {text: "changed the firm's screen layout"},
	"ai.metered":                     {quiet: true},
	"ledgy.feedback":                 {quiet: true},
	"inspack.index_viewed":           {quiet: true},
}

// activityPhraseFor finds an action's phrase and module. Team messages and
// boards are left out: they have their own notifications, and private
// channels, direct messages and private boards are not the firm's to read.
func activityPhraseFor(action string) (actPhrase, actModule) {
	if strings.HasPrefix(action, "chat.") || strings.HasPrefix(action, "board.") {
		return actPhrase{quiet: true}, actModule{}
	}
	mod, ok := actModule{}, false
	parts := strings.Split(action, ".")
	if len(parts) > 2 {
		mod, ok = activityModules[parts[0]+"."+parts[1]]
	}
	if !ok {
		mod, ok = activityModules[parts[0]]
	}
	if !ok {
		mod = actModule{area: AreaOperations, view: "audit", label: strings.ReplaceAll(parts[0], "_", " ")}
	}
	if len(parts) == 2 && parts[0] != "" {
		if m2, ok2 := activityModules[action]; ok2 { // "bow.funds" as a whole action
			mod = m2
		}
	}
	return activityPhrases[action], mod
}

// activityFallback reads an action without a phrase: "recorded MCX UCC
// API sync (B-12)". The verb is the action's last part when it reads as
// one ("approved", "sent"); otherwise "recorded".
func activityFallback(action string, mod actModule, x activityText) string {
	parts := strings.Split(action, ".")
	rest := parts[1:]
	if len(rest) > 1 {
		if _, two := activityModules[parts[0]+"."+rest[0]]; two {
			rest = rest[1:] // the module's name already says "CDSL pledge"
		}
	}
	verb := "recorded"
	if n := len(rest); n > 0 && activityVerbs[rest[n-1]] != "" {
		verb, rest = activityVerbs[rest[n-1]], rest[:n-1]
	}
	what := mod.label
	if len(rest) > 0 {
		what += " " + strings.ToLower(strings.NewReplacer("_", " ", "-", " ").Replace(strings.Join(rest, " ")))
	}
	s := verb + " " + strings.TrimSpace(what)
	if x.obj != "" {
		s += " (" + clip(x.obj, 60) + ")"
	}
	return s
}

var activityVerbs = map[string]string{
	"created": "created", "added": "added", "saved": "saved", "changed": "changed", "updated": "updated", "deleted": "deleted", "removed": "removed",
	"approved": "approved", "rejected": "rejected", "cancelled": "cancelled", "sent": "sent", "uploaded": "uploaded", "downloaded": "downloaded",
	"loaded": "loaded", "imported": "imported", "exported": "exported", "fetched": "fetched", "posted": "posted", "recorded": "recorded",
	"requested": "asked for", "confirmed": "confirmed", "settled": "settled", "closed": "closed", "opened": "opened", "run": "ran", "ran": "ran",
	"failed": "had a failure in", "proposed": "proposed", "released": "released", "applied": "applied", "verified": "verified", "submitted": "submitted",
	"entered": "entered", "built": "built", "made": "made", "issued": "issued", "booked": "booked", "resolved": "resolved", "done": "finished",
}

// render fills the phrase's template.
func (p actPhrase) render(x activityText) string {
	if p.fn != nil {
		return p.fn(x)
	}
	var b strings.Builder
	t := p.text
	for t != "" {
		if t[0] == '[' {
			j := strings.IndexByte(t, ']')
			if j < 0 {
				j = len(t)
			}
			if s, ok := activityFill(t[1:j], x); ok {
				b.WriteString(s)
			}
			t = t[min(j+1, len(t)):]
			continue
		}
		j := strings.IndexByte(t, '[')
		if j < 0 {
			j = len(t)
		}
		s, _ := activityFill(t[:j], x)
		b.WriteString(s)
		t = t[j:]
	}
	return strings.TrimSpace(b.String())
}

// activityFill replaces each {…} in seg; false when one has no value. A
// value inside a bool's words ({on|bool|made {object} private|…}) is
// filled after the word is chosen.
func activityFill(seg string, x activityText) (string, bool) {
	var b strings.Builder
	ok := true
	for seg != "" {
		i := strings.IndexByte(seg, '{')
		if i < 0 {
			b.WriteString(seg)
			break
		}
		b.WriteString(seg[:i])
		// The matching brace, allowing one level inside (bool words).
		depth, j := 0, i
		for ; j < len(seg); j++ {
			if seg[j] == '{' {
				depth++
			} else if seg[j] == '}' {
				depth--
				if depth == 0 {
					break
				}
			}
		}
		if j >= len(seg) {
			b.WriteString(seg[i:])
			break
		}
		s, has := activityPlace(seg[i+1:j], x)
		ok = ok && has
		b.WriteString(s)
		seg = seg[j+1:]
	}
	return b.String(), ok
}

// activityPlace formats one placeholder.
func activityPlace(spec string, x activityText) (string, bool) {
	args := activitySplit(spec)
	key, f := args[0], ""
	if len(args) > 1 {
		f = args[1]
	}
	v, ok := x.val(key)
	if !ok {
		return "", false
	}
	arg := func(i int) string {
		if len(args) > i {
			return args[i]
		}
		return ""
	}
	switch f {
	case "inr":
		n, ok := activityNum(v)
		if !ok {
			return "", false
		}
		return activityINR(n), true
	case "n", "len":
		n, ok := activityNum(v)
		if f == "len" {
			n, ok = float64(activityLen(v)), true
		}
		if !ok {
			return "", false
		}
		noun := arg(2)
		if n != 1 {
			noun = firstOf(arg(3), noun+"s")
		}
		return strings.TrimSpace(activityCount(n) + " " + noun), true
	case "file":
		return activityLayoutName(activityPlain(v)), true
	case "date":
		s := activityPlain(v)
		if t, err := time.Parse("2006-01-02", s[:min(10, len(s))]); err == nil {
			return t.Format("2 Jan"), true
		}
		return s, true
	case "lower":
		return strings.ToLower(activityPlain(v)), true
	case "words":
		return strings.ToLower(strings.NewReplacer("_", " ", "-", " ").Replace(activityPlain(v))), true
	case "stage":
		s, _, _ := strings.Cut(activityPlain(v), ":")
		return strings.ToLower(strings.NewReplacer("_", " ", "-", " ").Replace(s)), true
	case "target": // "ucc:BH1003" is the client; "tag:HNI" is the group HNI
		kind, val, found := strings.Cut(activityPlain(v), ":")
		if !found {
			return kind, true
		}
		if kind == "ucc" {
			return val, true
		}
		return strings.ReplaceAll(kind, "_", " ") + " " + val, true
	case "bool":
		yes := false
		switch t := v.(type) {
		case bool:
			yes = t
		case string:
			yes = t == "true" || t == "yes"
		case float64:
			yes = t != 0
		}
		w := arg(3)
		if yes {
			w = arg(2)
		}
		return activityFill(w, x)
	}
	return activityPlain(v), true
}

// activitySplit splits a placeholder on "|" outside braces.
func activitySplit(s string) []string {
	var out []string
	depth, from := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
		case '|':
			if depth == 0 {
				out = append(out, s[from:i])
				from = i + 1
			}
		}
	}
	return append(out, s[from:])
}

// activityPlain is a value as plain text: whole numbers with Indian digit
// grouping, lists as the first few items.
func activityPlain(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return activityCount(t)
	case bool:
		if t {
			return "yes"
		}
		return "no"
	case []any:
		var xs []string
		for _, y := range t {
			if s := activityPlain(y); s != "" {
				xs = append(xs, s)
			}
		}
		if len(xs) > 3 {
			return strings.Join(xs[:3], ", ") + fmt.Sprintf(" and %d more", len(xs)-3)
		}
		return strings.Join(xs, ", ")
	}
	return ""
}

func activityNum(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case string:
		f, err := strconv.ParseFloat(strings.ReplaceAll(t, ",", ""), 64)
		return f, err == nil
	}
	return 0, false
}

func activityLen(v any) int {
	if xs, ok := v.([]any); ok {
		return len(xs)
	}
	if s, ok := v.(string); ok && s != "" {
		return len(strings.Split(s, ","))
	}
	return 0
}

// activityCount is a count with Indian digit grouping (1,20,400).
func activityCount(n float64) string {
	if n != math.Trunc(n) {
		return strconv.FormatFloat(n, 'f', -1, 64)
	}
	neg := n < 0
	s := strconv.FormatInt(int64(math.Abs(n)), 10)
	if len(s) > 3 {
		head, tail := s[:len(s)-3], s[len(s)-3:]
		var parts []string
		for len(head) > 2 {
			parts = append([]string{head[len(head)-2:]}, parts...)
			head = head[:len(head)-2]
		}
		if head != "" {
			parts = append([]string{head}, parts...)
		}
		s = strings.Join(parts, ",") + "," + tail
	}
	if neg {
		return "-" + s
	}
	return s
}

// activityINR is an amount as people say it: ₹12,345; ₹3.2 lakh; ₹1.25
// crore.
func activityINR(n float64) string {
	sign := ""
	if n < 0 {
		sign, n = "−", -n
	}
	trimmed := func(f float64, d int) string {
		s := strconv.FormatFloat(f, 'f', d, 64)
		if strings.Contains(s, ".") {
			s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
		}
		return s
	}
	switch {
	case n >= 1e7:
		return sign + "₹" + trimmed(n/1e7, 2) + " crore"
	case n >= 1e5:
		return sign + "₹" + trimmed(n/1e5, 1) + " lakh"
	}
	return sign + "₹" + activityCount(math.Round(n))
}

// activityLayoutName reads a file layout's name: "NSE-CM-TRADE" is "NSE CM
// trade file", "XFILE-NSE-CM-BHAV" is "NSE CM bhav file".
func activityLayoutName(s string) string {
	s = strings.TrimPrefix(strings.TrimSpace(s), "XFILE-")
	if s == "" {
		return "a file"
	}
	ws := strings.FieldsFunc(s, func(r rune) bool { return r == '-' || r == '_' || r == ' ' })
	for i, w := range ws {
		if !activityUpperWords[strings.ToUpper(w)] {
			ws[i] = strings.ToLower(w)
		} else {
			ws[i] = strings.ToUpper(w)
		}
	}
	out := strings.Join(ws, " ")
	if !strings.HasSuffix(out, " file") {
		out += " file"
	}
	return out
}

var activityUpperWords = map[string]bool{"NSE": true, "BSE": true, "MCX": true, "NCL": true, "ICCL": true, "MCXCCL": true, "CM": true, "FO": true, "CD": true, "COM": true,
	"OMS": true, "CDSL": true, "NSDL": true, "KRA": true, "UCC": true, "MF": true, "IPO": true, "SLB": true, "MTF": true, "RMS": true, "DP": true, "BO": true, "PAN": true, "SGB": true}

// phFileLoaded: "loaded NSE CM trade file (1,204 trades)".
func phFileLoaded(x activityText) string {
	src := x.str("source")
	s := "loaded " + activityLayoutName(src)
	noun := "row"
	if strings.Contains(strings.ToUpper(src), "TRADE") {
		noun = "trade"
	}
	if n, ok := x.num("added"); ok {
		if n == 1 {
			s += " (1 " + noun
		} else {
			s += " (" + activityCount(n) + " " + noun + "s"
		}
		if d, ok := x.num("duplicates"); ok && d > 0 {
			s += ", " + activityCount(d) + " already loaded"
		}
		s += ")"
	}
	return s
}

// phBilling: a day's billing run, or one client billed again.
func phBilling(x activityText) string {
	day := ""
	if t, err := time.Parse("2006-01-02", x.obj); err == nil {
		day = " for " + t.Format("2 Jan")
	}
	if c := x.str("client"); c != "" {
		return "billed " + c + " again" + day
	}
	s := "ran billing" + day
	if n, ok := x.num("posted"); ok {
		s += ": " + activityCount(n) + " bill"
		if n != 1 {
			s += "s"
		}
		s += " posted"
	}
	return s
}

// activityStatementKinds name the statements bulk sends carry.
var activityStatementKinds = map[string]string{"ras_statement": "running-account", "ledger": "ledger", "holding": "holding", "dp_statement": "DP", "margin": "margin"}

// phStatements: "sent running-account statements for 16 Oct (42 sent)".
func phStatements(x activityText) string {
	kind, period, _ := strings.Cut(x.obj, " ")
	name := activityStatementKinds[kind]
	if name == "" {
		name = strings.ToLower(strings.NewReplacer("_", " ", "-", " ").Replace(strings.TrimSuffix(kind, "_statement")))
	}
	s := "sent " + name + " statements"
	if t, err := time.Parse("2006-01-02", period); err == nil {
		s += " for " + t.Format("2 Jan")
	} else if period != "" {
		s += " for " + period
	}
	if n, ok := x.num("sent"); ok {
		s += " (" + activityCount(n) + " sent"
		if b, ok := x.num("bounced"); ok && b > 0 {
			s += ", " + activityCount(b) + " bounced"
		}
		s += ")"
	}
	return s
}
