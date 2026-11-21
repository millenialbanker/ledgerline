// Package screens is the one list of the console's staff screens: the
// sidebar sections, the screens in each, and the tabs of the screens that
// are grouped. The console reads it (served as /screens.js), the role
// editor shows it as a tree, and the server checks a role's permissions
// against the same keys, so the three cannot drift apart.
package screens

import (
	"encoding/json"
	"strings"
)

// Item is one sidebar entry: a screen (Key is its view name) or a group of
// screens shown as tabs (Key is the group's name, Tabs its screens).
type Item struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Tabs  []Item `json:"tabs,omitempty"`
}

// Section is one heading of the sidebar.
type Section struct {
	Name  string `json:"name"`
	Items []Item `json:"items"`
	// Admin marks the Admin section: its screens change how the whole
	// firm's system works (connections, users, security, data and exit).
	Admin bool `json:"admin,omitempty"`
}

func s(k, l string) Item               { return Item{Key: k, Label: l} }
func g(k, l string, tabs ...Item) Item { return Item{Key: k, Label: l, Tabs: tabs} }

// Nav is the staff console's sidebar, in order. Screens that do one job
// are grouped: the sidebar shows the group once and the screen opens with
// a tab for each of its parts. Every screen keeps its own key (links, help
// articles and bookmarks to it still work).
var Nav = []Section{
	{Name: "Overview", Items: []Item{s("dashboard", "Dashboard"), s("boards", "Boards"), s("chat", "Team messages"),
		g("automation", "Automation", s("autopilot", "Autopilot & work queue"), s("steps", "Steps & reminders"), s("jobs", "Scheduled jobs"), s("rpa", "Browser automations")),
		g("calendar", "Calendar & circulars", s("periodic", "Periodic work"), s("circulars", "Circulars & duties"))}},
	{Name: "Operations", Items: []Item{
		g("filesg", "Files", s("xfiles", "Exchange files"), s("ccdata", "Clearing data"), s("files", "Other uploads & migration"), s("migrate", "Migration from old system")),
		s("recon", "Trade reconciliation"), s("billing", "Billing"), s("settlement", "Settlement"), s("notes", "Contract notes"),
		g("clientsg", "Clients & onboarding", s("clients", "Clients"), s("apps", "Online applications"), s("changes", "Change requests"), s("uccapi", "Exchange UCC"), s("mnrl", "Revoked mobiles")),
		s("corp", "Corporate actions"),
		g("docsg", "Client documents", s("statements", "Client reports"), s("dispatch", "Dispatch register"), s("messages", "Client messages"), s("yearend", "Year-end packs"), s("archive", "Old documents archive"))}},
	{Name: "Risk", Items: []Item{
		g("riskg", "RMS & margin", s("rms", "RMS & limits"), s("live", "Live (intraday)"), s("margin", "Margin reporting"), s("payouthold", "Pay-out holds")),
		g("collg", "Collateral", s("collateral", "Client collateral & pledge"), s("cdslpledge", "Pledges (CDSL)"), s("cdslepi", "Early pay-in"), s("allocation", "Clearing allocation"), s("deposits", "Clearing deposits")),
		g("debtg", "Debtors", s("debits", "Debtors & interest"), s("legacy", "Legacy debts")),
		s("mtf", "Margin trading (MTF)")}},
	{Name: "Accounts", Items: []Item{s("ledger", "Ledger"),
		g("moneyg", "Receipts & payouts", s("money", "Receipts & payouts"), s("bank", "Bank reconciliation"), s("ras", "Running account"), s("clientmoney", "Client money reconciliation")),
		s("books", "Firm accounts & GST"),
		g("brokg", "Brokerage & charges", s("brokerage", "Brokerage plans"), s("charges", "Charges calculator"), s("prepaid", "Prepaid schemes"))}},
	{Name: "Compliance", Items: []Item{
		g("regg", "Regulatory reports", s("reports", "Regulatory reports"), s("exdisc", "Exchange disclosures"), s("aictl", "AI controls")),
		s("kyc", "KYC & PMLA"), s("helpdesk", "Investor complaints"), s("nri", "NRI (PIS)"), s("surv", "Surveillance & AML"), s("inactive", "Inactive accounts"),
		s("deceased", "Deceased clients"), s("selfinspect", "Self-inspection"), s("inspack", "Inspection packs"), s("audit", "Audit trail"),
		g("secg", "Security", s("incidents", "Incidents"), s("logexport", "Log export"))}},
	{Name: "Investments", Items: []Item{s("ipo", "IPO & public issues"), s("rfq", "Bonds (RFQ deals)"), s("cbrics", "Corporate bonds (CBRICS)"),
		g("mfg", "Mutual funds", s("mforders", "MF orders"), s("mfsips", "SIPs"), s("mfmandates", "MF mandates"), s("mfunits", "MF units"), s("mfcomm", "MF commission"), s("mfsettings", "MF settings")),
		s("stocksip", "Stock SIPs")}},
	{Name: "Depository", Items: []Item{
		g("dpg", "Depository", s("dp", "Demat accounts & DIS"), s("cdslreq", "CDSL requests"), s("edis", "eDIS authorisations"), s("cdsltxn", "Transactions (API)"))}},
	{Name: "Business", Items: []Item{s("insights", "Client insights"), s("crm", "Leads & CRM"),
		g("partnersg", "Branches & partners", s("partners", "Branches & partners"), s("dealers", "Dealers & terminals"))}},
	{Name: "Admin", Admin: true, Items: []Item{s("connections", "Connections"), s("backups", "Backups"), s("dataexit", "Data & exit"),
		g("settingsg", "Settings", s("settings", "Settings"), s("practices", "Firm practices")),
		g("usersg", "Users & roles", s("users", "Users"), s("roles", "Roles")),
		g("securityg", "Security", s("supportaccess", "Support access")),
		s("layout", "Screen layout")}},
}

// Screen is one screen (a leaf of the tree) with where it sits.
type Screen struct {
	Key     string `json:"key"`
	Label   string `json:"label"`
	Section string `json:"section"`
	Group   string `json:"group,omitempty"`       // the group's key when the screen is a tab
	GroupOf string `json:"group_label,omitempty"` // the group's label
	Admin   bool   `json:"admin,omitempty"`
}

var (
	leaves []Screen
	byKey  = map[string]Screen{}
)

func init() {
	for _, sec := range Nav {
		for _, it := range sec.Items {
			if len(it.Tabs) == 0 {
				add(Screen{Key: it.Key, Label: it.Label, Section: sec.Name, Admin: sec.Admin})
				continue
			}
			for _, t := range it.Tabs {
				add(Screen{Key: t.Key, Label: t.Label, Section: sec.Name, Group: it.Key, GroupOf: it.Label, Admin: sec.Admin})
			}
		}
	}
}

func add(x Screen) {
	if _, dup := byKey[x.Key]; dup {
		panic("screens: " + x.Key + " is listed twice")
	}
	leaves = append(leaves, x)
	byKey[x.Key] = x
}

// All is every screen, in sidebar order.
func All() []Screen { return append([]Screen(nil), leaves...) }

// Get returns a screen by key.
func Get(key string) (Screen, bool) { x, ok := byKey[key]; return x, ok }

// IsAdmin reports a screen of the Admin section.
func IsAdmin(key string) bool { return byKey[key].Admin }

// Name is the screen's label with its group, for messages: "Receipts &
// payouts · Bank reconciliation".
func Name(key string) string {
	x, ok := byKey[key]
	if !ok {
		return key
	}
	if x.GroupOf != "" && x.GroupOf != x.Label {
		return x.GroupOf + " · " + x.Label
	}
	return x.Label
}

// JS is the catalogue as the console's script: the GROUPS and NAV
// constants app.js draws the sidebar and screen tabs from.
func JS() []byte {
	var b strings.Builder
	b.WriteString("'use strict';\n/* The staff screens: served by the server from internal/screens, so the console,\n   the role editor and the permission checks share one list. */\n")
	groups := map[string]any{}
	var order []string
	nav := make([]any, 0, len(Nav))
	for _, sec := range Nav {
		items := make([]any, 0, len(sec.Items))
		for _, it := range sec.Items {
			if len(it.Tabs) == 0 {
				items = append(items, []string{it.Key, it.Label})
				continue
			}
			tabs := make([][]string, 0, len(it.Tabs))
			for _, t := range it.Tabs {
				tabs = append(tabs, []string{t.Key, t.Label})
			}
			groups[it.Key] = []any{it.Label, tabs}
			order = append(order, it.Key)
			items = append(items, []string{"@" + it.Key})
		}
		nav = append(nav, []any{sec.Name, items})
	}
	b.WriteString("const GROUPS = {\n")
	for _, k := range order {
		b.WriteString("  " + k + ": " + js(groups[k]) + ",\n")
	}
	b.WriteString("};\nconst NAV = ")
	b.WriteString(js(nav))
	b.WriteString(";\nconst ADMIN_SCREENS = new Set(")
	var adm []string
	for _, x := range leaves {
		if x.Admin {
			adm = append(adm, x.Key)
		}
	}
	b.WriteString(js(adm))
	b.WriteString(");\n")
	return []byte(b.String())
}

// js encodes v as a JavaScript literal ("&" kept as it is).
func js(v any) string {
	var b strings.Builder
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	_ = e.Encode(v)
	return strings.TrimSpace(b.String())
}
