package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/help"
)

// ---- AI system inventory ----
//
// The register of every AI use: one entry for each code path in
// Ledgerline that can call a model (aiFeatures; a test fails if a call
// path has no entry), with what it is for, the provider and model
// connected, what is sent, who can use it, its controls and whether it is
// on for this firm; and the AI tools the firm uses outside Ledgerline,
// added by the compliance officer. It feeds the AI & ML disclosure.

const cAITool = "aitool"

// AISystem is one AI use.
type AISystem struct {
	ID        string   `json:"id"`
	Source    string   `json:"source"` // ledgerline, firm
	Name      string   `json:"name"`
	Purpose   string   `json:"purpose"`
	Provider  string   `json:"provider"`
	Model     string   `json:"model"`
	DataSent  string   `json:"data_sent"`
	WhoCanUse string   `json:"who_can_use"`
	Controls  []string `json:"controls"`
	On        bool     `json:"on"`
	OffWhy    string   `json:"off_why,omitempty"`
	Switch    string   `json:"switch,omitempty"` // where it is switched on
	// For the AI & ML disclosure (Y/N, or Y/N/NA for the system audit).
	OrderExecution string `json:"order_execution"`
	Discretionary  string `json:"discretionary"`
	Advice         string `json:"advice"`
	CyberSecurity  string `json:"cyber_security"`
	Claims         string `json:"claims"`
	Tool           string `json:"tool"`
	Implemented    string `json:"implemented"`
	InSystemAudit  string `json:"in_system_audit"`
	Description    string `json:"description"`
	Safeguards     string `json:"safeguards"`
	Categories     string `json:"categories"` // which of SEBI's categories it falls in
	// Firm-added tools.
	Vendor     string `json:"vendor,omitempty"`
	HostedAt   string `json:"hosted_at,omitempty"` // where the tool runs (for the SaaS report)
	SaaS       bool   `json:"saas,omitempty"`      // a cloud service: also a SaaS report row
	DataNature string `json:"data_nature,omitempty"`
	InUseFrom  string `json:"in_use_from,omitempty"`
	InUseTo    string `json:"in_use_to,omitempty"`
	Active     bool   `json:"active"`
	By         string `json:"by,omitempty"`
	At         string `json:"at,omitempty"`
}

// aiCommonControls are the controls every Ledgerline AI path has.
func aiCommonControls() []string {
	return []string{
		"Kill switch: the firm (Admin or compliance officer) can switch all AI or one provider off, with the reason in the audit trail; the server can be switched off for every firm (LEDGERLINE_AI_OFF). Takes effect on the next call.",
		"Feature switch in Settings, off until an Admin switches it on.",
		"Every call is logged (user, feature, provider, model, tokens, charge, masked prompt and answer, outcome) for the compliance officer, with a retention period.",
		"Each call is metered and paid from prepaid AI credit; with too little credit the call is not made.",
		"Refused in India-only mode (LEDGERLINE_INDIA_ONLY).",
		"No autonomous actions: the model's reply is only shown to a person; it cannot place orders, move money or change records.",
	}
}

// ledgerlineAISystems are the AI code paths in Ledgerline.
func (o *Ops) ledgerlineAISystems() []AISystem {
	prov, model, tool, impl := "", "", "Not connected", "Vendor (Ledgerline); no AI model connected on this server"
	if o.LedgyAI != nil {
		prov, model = o.LedgyAI.Provider(), o.LedgyAI.Model()
		info := aiProviderInfo[prov]
		tool = fmt.Sprintf("%s (%s), through Ledgerline", info.Name, model)
		impl = fmt.Sprintf("Vendor (Ledgerline) using the %s model %s (%s), through its API", info.Name, model, info.Company)
	}
	safeguards := "Kill switch (firm-wide, per provider and server-wide) that stops the next call; identifiers masked before sending; every call logged with the masked prompt and answer for the compliance officer; answers capped in length; no autonomous actions: the reply is text for a person and every decision and change in Ledgerline is made by staff (maker-checker); prepaid credit cap; on any error or refusal the feature falls back to its non-AI behaviour."
	return []AISystem{
		{
			ID: AIFeatureLedgy, Source: "ledgerline", Name: "Ledgy AI phrasing (in-app help)",
			Purpose:  "Rewrites Ledgy's answer to a staff member's how-to question in plainer words, using only the help article that answers it.",
			Provider: prov, Model: model,
			DataSent:  "The staff member's question, with client names, client codes, PAN, Aadhaar-like numbers, phone numbers, emails, bank account numbers and staff names replaced by tokens; the matching help articles and permission lines. No client records, balances, holdings or trades.",
			WhoCanUse: "Every signed-in staff member who asks Ledgy (not client portal users).",
			Controls: append(aiCommonControls(),
				"Identifiers masked before sending and restored only in the answer shown to the user (PAN, Aadhaar and account numbers always).",
				"The model is told to answer only from the help articles provided and never give investment advice; answers are capped at "+fmt.Sprint(help.MaxOutputTokens)+" tokens.",
				"Without AI, or on any error, Ledgy gives its standard article answer."),
			Switch:         "Settings → Ledgy: Let Ledgy write answers with AI",
			OrderExecution: "N", Discretionary: "N", Advice: "N", CyberSecurity: "N",
			Claims:        "Phrases answers to staff questions about using the back-office software from Ledgerline's own help articles. No claims of investment, trading, advisory or security capability are made.",
			Tool:          tool,
			Implemented:   impl,
			InSystemAudit: "Y",
			Description:   "Ledgy is the help assistant inside the Ledgerline back office. It finds the help article that answers a staff member's question by a rule-based search; when the firm switches AI phrasing on, a large language model (natural language processing) rewrites that answer in plainer words from the article text only. It answers staff operations questions; it does not see client records and cannot act in the system.",
			Safeguards:    safeguards,
			Categories:    "Natural language processing (large language model)",
		},
		{
			ID: AIFeatureBillScan, Source: "ledgerline", Name: "Reading scanned vendor bills with AI",
			Purpose:  "Reads the details (vendor, GSTIN, bill number, dates, amounts, bank) of a scanned or photographed purchase bill that the server cannot read itself, to fill the bill form for a person to check.",
			Provider: prov, Model: model,
			DataSent:  "The image or PDF of the firm's own vendor bill and a fixed instruction. No client data. An image cannot be masked: the bill is sent as it is; the reply is masked in the answer log.",
			WhoCanUse: "Ops makers and Ops checkers, when they press Read with AI on a scanned bill.",
			Controls: append(aiCommonControls(),
				"Only bills the server cannot read itself are offered; PDFs with text are read on the server.",
				"The details read only fill the bill form: a person checks them against the bill and saves, an Ops checker posts the voucher, and changed bank details are flagged against the vendor master."),
			Switch:         "Settings → Ledgy: Read scanned bills with AI",
			OrderExecution: "N", Discretionary: "N", Advice: "N", CyberSecurity: "N",
			Claims:        "Reads the printed details of the firm's own purchase bills to save typing. No investment, trading, advisory or security claims are made.",
			Tool:          tool,
			Implemented:   impl,
			InSystemAudit: "Y",
			Description:   "In the vendor bills screen of the Ledgerline back office, a scanned or photographed bill can be read by an AI model that understands documents (a neural-network model); it returns the bill's details, which fill the bill form for a person to check before saving. It is used for the firm's own accounts payable only.",
			Safeguards:    safeguards,
			Categories:    "Neural networks (document understanding), natural language processing",
		},
	}
}

// aiInventory is every AI use, with whether it is on for this firm now.
func (o *Ops) aiInventory(ctx context.Context, tenant string) ([]AISystem, error) {
	out := o.ledgerlineAISystems()
	for i := range out {
		out[i].On, out[i].OffWhy = o.aiFeatureState(ctx, tenant, out[i].ID)
		out[i].Active = true
	}
	ts, err := docs.List[AISystem](ctx, o.Docs, tenant, cAITool, "")
	if err != nil {
		return out, err
	}
	today := o.businessDate(ctx, tenant)
	sort.SliceStable(ts, func(i, j int) bool { return ts[i].ID < ts[j].ID })
	for _, t := range ts {
		t.Source = "firm"
		t.On = t.Active && (t.InUseFrom == "" || t.InUseFrom <= today) && (t.InUseTo == "" || t.InUseTo >= today)
		if !t.On {
			t.OffWhy = "Not in use on " + today
		}
		out = append(out, t)
	}
	return out, nil
}

// AIInventory lists every AI use.
func (o *Ops) AIInventory(ctx context.Context, tenant, actor string) ([]AISystem, error) {
	if err := o.require(ctx, tenant, actor, "Viewing AI controls", RoleCompliance); err != nil {
		return nil, err
	}
	return o.aiInventory(ctx, tenant)
}

func ynOK(v string, na bool) bool { return v == "Y" || v == "N" || (na && v == "NA") }

// SaveAITool adds or changes an AI tool the firm uses outside Ledgerline.
func (o *Ops) SaveAITool(ctx context.Context, tenant, actor string, t AISystem) (AISystem, error) {
	if err := o.require(ctx, tenant, actor, "Changing the AI inventory", RoleCompliance); err != nil {
		return t, err
	}
	t.Name, t.Purpose = strings.TrimSpace(t.Name), strings.TrimSpace(t.Purpose)
	if t.Name == "" || t.Purpose == "" {
		return t, invalid("an AI tool needs a name and what it is used for")
	}
	for _, f := range []struct{ n, v string }{{"order initiation, routing and execution", t.OrderExecution}, {"discretionary investment or portfolio management", t.Discretionary}, {"investment or trading advice", t.Advice}, {"cyber security", t.CyberSecurity}} {
		if !ynOK(f.v, false) {
			return t, invalid("answer Y or N for %s", f.n)
		}
	}
	if t.InSystemAudit == "" {
		t.InSystemAudit = "NA"
	}
	if !ynOK(t.InSystemAudit, true) {
		return t, invalid("answer Y, N or NA for the system audit")
	}
	for _, d := range []string{t.InUseFrom, t.InUseTo} {
		if d != "" && !validDate(d) {
			return t, invalid("dates must be YYYY-MM-DD")
		}
	}
	if t.InUseFrom != "" && t.InUseTo != "" && t.InUseTo < t.InUseFrom {
		return t, invalid("in use until is before in use from")
	}
	if t.ID == "" || !strings.HasPrefix(t.ID, "AIT-") {
		n, err := o.next(ctx, tenant, "aitool", actor)
		if err != nil {
			return t, err
		}
		t.ID = fmt.Sprintf("AIT-%04d", n)
	}
	t.Source, t.On, t.OffWhy, t.Switch = "firm", false, "", ""
	t.By, t.At = actor, o.now().Format(time.RFC3339)
	if t.Controls == nil {
		t.Controls = []string{}
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cAITool, t.ID, t, actor); err != nil {
		return t, err
	}
	o.log(ctx, tenant, actor, "ai.tool_saved", t.ID, map[string]any{"name": t.Name, "active": t.Active})
	return t, nil
}

// aiSystemsDuring are the AI systems in use at any time in a period: a
// Ledgerline feature that could be used then, a firm tool in use then.
func (o *Ops) aiSystemsDuring(ctx context.Context, tenant, from, to string) ([]AISystem, []string) {
	inv, _ := o.aiInventory(ctx, tenant)
	var out []AISystem
	var notes []string
	for _, s := range inv {
		switch s.Source {
		case "ledgerline":
			on, why := o.aiOnDuring(ctx, tenant, s.ID, from, to)
			notes = append(notes, s.Name+": "+map[bool]string{true: "in use", false: "not in use"}[on]+" ("+why+")")
			if on {
				out = append(out, s)
			}
		default:
			if s.Active && (s.InUseFrom == "" || s.InUseFrom <= to) && (s.InUseTo == "" || s.InUseTo >= from) {
				out = append(out, s)
			}
		}
	}
	return out, notes
}
