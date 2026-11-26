package ops

import (
	"context"
	"fmt"
	"strings"
	"time"

	"ledgerline/internal/docs"
)

// ---- the account opening kit, the electronic contract note authorisation
// and the client master on a new demat account ----
//
//   - SEBI master circular for stock brokers (SEBI/HO/MIRSD/MIRSD-PoD/P/
//     CIR/2025/90) para 21.1: the kit has an index of documents, the
//     account opening form, the rights and obligations, the risk
//     disclosure document, the guidance note (do's and don'ts) and the
//     most important terms and conditions (MITC); para 21.2: the client
//     gets the tariff sheet and the details of senior officials and the
//     investor grievance cell; para 21.4: separate specific authorisations
//     (running account, electronic contract notes) and the MITC
//     acknowledged; para 21.12: the kit in the form the client prefers,
//     with a log proving electronic delivery.
//   - Paras 23.2.1 and 52.3.1-52.3.2: electronic contract notes only to a
//     client who authorised them specifically, signed by the client (an
//     Aadhaar e-Signed form online, a signed letter otherwise); the e-mail
//     must be the client's own, never created by the broker, and changed
//     only by a physical letter or through the client's secured login.
//   - SEBI master circular for depositories (SEBI/HO/MRD/MRD-PoD-1/P/CIR/
//     2024/168) paras 1.3.2-1.3.3 and CDSL DPOI 2.4.3: the BO is given the
//     rights and obligations of the BO and the DP and acknowledges them.
//   - CDSL DPOI 2.4.21: on opening, the BO is sent a system-generated
//     account opening confirmation (client master) by post with proof of
//     dispatch, or by e-mail falling back to paper when it bounces, and the
//     delivery is kept: it goes through the dispatch register.
//
// Online, the declarations are accepted on the review step and printed in
// the e-Signed form; the kit is then sent as a signed PDF through the
// dispatch register (a bounce falls back to paper). For a client opened
// on paper, staff send the kit from the client's page.

const (
	DocKit = "account_kit"
	DocCMR = "client_master"

	declMITC     = "mitc"
	declGuidance = "guidance"
	declDPRights = "dp_rights"
	declECN      = "ecn"
)

// ECNText is the optional authorisation for electronic contract notes.
const ECNText = "I authorise you to send my contract notes, statements and other documents electronically, digitally signed, to my e-mail address given in this form, which is my own. I can ask for printed copies instead at any time. (Optional: without it, they are sent by post.)"

func init() {
	DocKindNames[DocKit] = "Account opening kit"
	DocKindNames[DocCMR] = "Demat account opening confirmation (client master)"
	AppDeclarations[declMITC] = "I have read the Most Important Terms and Conditions (MITC) of my account."
	AppDeclarations[declGuidance] = "I have read the guidance note on do's and don'ts for trading on the exchanges."
	AppDeclarations[declDPRights] = "Where my demat account is opened with you: I have read the rights and obligations of the beneficial owner and the depository participant."
}

// kitDeclarations drops the DP's rights and obligations for a firm that is
// not a DP (unknown keeps it: shown, as the firm has not said).
func (o *Ops) kitDeclarations(ctx context.Context, tenant string, d map[string]string) map[string]string {
	if v, _ := dutyNeed("dp", o.firmFacts(ctx, tenant)); v != triNo {
		return d
	}
	out := map[string]string{}
	for k, v := range d {
		if k != declDPRights {
			out[k] = v
		}
	}
	return out
}

// ECNAuthorisation is the client's specific authorisation for electronic
// contract notes and statements.
type ECNAuthorisation struct {
	On     bool   `json:"on"`
	At     string `json:"at"`
	Source string `json:"source"` // Account opening form (e-Signed), Signed letter, Client portal, Migrated
	Ref    string `json:"ref,omitempty"`
	By     string `json:"by"`
}

// SetECNAuthorisation records a client's choice of electronic or printed
// contract notes. Electronic needs the client's signed authorisation
// attached (a client document); the client portal's secured login counts
// as the client's own request.
func (o *Ops) SetECNAuthorisation(ctx context.Context, tenant, actor, ucc string, on bool, doc string) (Client, error) {
	role, err := o.RoleOf(ctx, tenant, actor)
	if err != nil {
		return Client{}, err
	}
	ucc = strings.ToUpper(strings.TrimSpace(ucc))
	src := "Signed letter"
	if role == RoleClient {
		accts, _ := o.PortalAccounts(ctx, tenant, actor)
		if !contains(accts, ucc) {
			return Client{}, ErrNotFound
		}
		src = "Client portal (secured login)"
	} else if err := o.require(ctx, tenant, actor, "Electronic contract note authorisation", RoleMaker, RoleChecker); err != nil {
		return Client{}, err
	} else if on {
		if _, ok := o.clientDoc(ctx, tenant, ucc, strings.TrimSpace(doc)); !ok {
			return Client{}, invalid("attach the client's signed authorisation for electronic contract notes (a client document) and give its ID")
		}
	}
	if !on && src == "Signed letter" {
		src = "Client's request"
	}
	c, err := docs.Update(ctx, o.Docs, tenant, cClient, ucc, actor, func(x *Client) error {
		x.PhysicalNotes = !on
		x.ECNAuth = &ECNAuthorisation{On: on, At: o.now().Format(time.RFC3339), Source: src, Ref: strings.TrimSpace(doc), By: actor}
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "client.ecn", ucc, map[string]any{"on": on, "source": src, "doc": doc})
	}
	return c, err
}

// emailDomain is the part after @, lower case.
func emailDomain(e string) string {
	_, d, ok := strings.Cut(strings.ToLower(strings.TrimSpace(e)), "@")
	if !ok {
		return ""
	}
	return d
}

// firmDomains are the firm's own e-mail domains (from Settings), which a
// client's registered e-mail must not be on.
func firmDomains(set Settings) map[string]bool {
	out := map[string]bool{}
	for _, e := range []string{set.ComplianceEmail} {
		if d := emailDomain(e); d != "" && !publicMailDomain(d) {
			out[d] = true
		}
	}
	for _, x := range set.Escalation {
		if d := emailDomain(x.Email); d != "" && !publicMailDomain(d) {
			out[d] = true
		}
	}
	return out
}

// publicMailDomain: a mail service anyone can use, so sharing it with the
// firm says nothing.
func publicMailDomain(d string) bool {
	switch d {
	case "gmail.com", "yahoo.com", "yahoo.co.in", "outlook.com", "hotmail.com", "rediffmail.com", "icloud.com", "live.com", "protonmail.com", "zoho.com", "example.com", "example.test", "mail.example":
		return true
	}
	return false
}

// ---- the kit ----

// accountKit is the kit's statement: the index of documents, the client's
// choices, the tariff and the grievance contacts.
func (o *Ops) accountKit(ctx context.Context, tenant, ucc, appID string) (ClientReport, Settings, error) {
	c, err := o.Client(ctx, tenant, ucc)
	if err != nil {
		return ClientReport{}, Settings{}, err
	}
	set, _ := o.Settings(ctx, tenant)
	today := o.now().In(ist).Format("2006-01-02")
	rep := ClientReport{Kind: "kit", Title: "Account opening kit", UCC: c.UCC, Name: c.Name, PAN: MaskPAN(c.PAN), Address: strings.Join(nonEmpty(c.City, c.State), ", "), To: today, Period: "Account opened " + fmtDay(firstOf(c.KYCDate, today)),
		Generated: o.now().Format("02-Jan-2006 15:04 IST")}
	form := "The account opening form you signed (Part I KYC, Part II trading details)"
	if appID != "" {
		form += ", application " + appID
	}
	idx := Section{Title: "Index of documents", Columns: []Column{{"doc", "Document", ColText}, {"what", "What it is", ColText}}, Rows: [][]string{
		{"Account opening form", form},
		{"Rights and obligations of the stock broker and the client", "As SEBI prescribes; voluntary clauses, if any, are in a separate document"},
		{"Risk disclosure document", "For the capital and derivatives markets"},
		{"Guidance note", "Do's and don'ts for trading on the exchanges"},
		{"Most important terms and conditions (MITC)", "The key terms of your account"},
		{"Tariff sheet", "The brokerage and charges you accepted"},
		{"Policies and procedures", "Including the running account settlement and the voluntary freeze of online trading"},
	}}
	if len(o.boAccountsOf(ctx, tenant, c.UCC)) > 0 {
		idx.Rows = append(idx.Rows, []string{"Rights and obligations of the beneficial owner and the depository participant", "For your demat account with us"})
	}
	idx.Note = "The documents are on our website and in the client portal; ask us for printed copies at any time."
	notes := "Electronic: by e-mail to " + maskEmail(c.Email)
	if c.PhysicalNotes {
		notes = "Printed, by post"
	}
	choices := Section{Title: "Your choices", Columns: []Column{{"item", "Item", ColText}, {"value", "Your choice", ColText}}, Rows: [][]string{
		{"Segments", firstOf(strings.Join(c.Segments, ", "), "-")},
		{"Contract notes and statements", notes},
		{"Running account settlement", firstOf(c.RAS, "Quarterly")},
		{"Nomination", c.StatementNomination()},
	}}
	tariff := Section{Title: "Tariff", Columns: []Column{{"item", "Item", ColText}}, Rows: [][]string{{"Brokerage and charges as in the tariff sheet you accepted; any change is told to you at least 30 days before it applies."}}}
	g := Section{Title: "Senior officials and the investor grievance cell", Columns: []Column{{"level", "Escalation", ColText}}}
	for _, l := range strings.Split(escalationText(set), "\n") {
		g.Rows = append(g.Rows, []string{l})
	}
	g.Rows = append(g.Rows, []string{odrLine})
	rep.Sections = []Section{idx, choices, tariff, g}
	rep.Notes = []string{"Keep this kit with your records. Tell us within 30 days if anything in it is not as you asked."}
	rep.normalise()
	return rep, set, nil
}

// SendAccountKit sends the client the account opening kit through the
// dispatch register.
func (o *Ops) SendAccountKit(ctx context.Context, tenant, actor, ucc string) (Dispatch, error) {
	if err := o.require(ctx, tenant, actor, "Sending the account opening kit", RoleMaker, RoleChecker); err != nil {
		return Dispatch{}, err
	}
	return o.sendAccountKit(ctx, tenant, actor, strings.ToUpper(strings.TrimSpace(ucc)), "")
}

func (o *Ops) sendAccountKit(ctx context.Context, tenant, actor, ucc, appID string) (Dispatch, error) {
	rep, set, err := o.accountKit(ctx, tenant, ucc, appID)
	if err != nil {
		return Dispatch{}, err
	}
	return o.dispatchDoc(ctx, tenant, actor, docRequest{Kind: DocKit, Ref: "KIT-" + ucc, Date: rep.To, UCC: ucc, Report: reportPDF(rep, set), Title: rep.Title,
		FileName: "AccountOpeningKit_" + ucc + ".pdf",
		Text:     fmt.Sprintf("Dear %s,\n\nWelcome. Your account opening kit is attached: the index of documents, your choices, the tariff and whom to contact with a complaint.", rep.Name)})
}

// ---- the client master of a new demat account ----

// sendClientMaster sends the account opening confirmation of a demat
// account the firm's DP opened.
func (o *Ops) sendClientMaster(ctx context.Context, tenant, actor, ucc string, b BOAccount) {
	c, err := o.Client(ctx, tenant, ucc)
	if err != nil {
		return
	}
	set, _ := o.Settings(ctx, tenant)
	day := firstOf(b.Opened, o.now().In(ist).Format("2006-01-02"))
	rep := ClientReport{Kind: "cmr", Title: "Demat account opening confirmation (client master)", UCC: c.UCC, Name: dematHolderNames(c.Name, b.JointHolders), PAN: MaskPAN(c.PAN), Address: strings.Join(nonEmpty(c.City, c.State), ", "),
		To: day, Period: "Opened " + fmtDay(day), Generated: o.now().Format("02-Jan-2006 15:04 IST")}
	yes := map[bool]string{true: "Yes", false: "No"}
	rows := [][]string{
		{"BO ID", b.BOID}, {"Depository", b.Depository}, {"Opened on", fmtDay(day)}, {"Status", b.Status},
		{"Holders", dematHolderNames(c.Name, b.JointHolders)}, {"First holder's PAN", MaskPAN(c.PAN)},
		{"Mobile", maskMobile(c.Mobile)}, {"E-mail", maskEmail(c.Email)},
		{"Bank account for credits", strings.TrimSpace(c.Bank.Bank + " " + MaskAccount(c.Bank.Account) + " " + c.Bank.IFSC)},
		{"Nomination", c.StatementNomination()}, {"DDPI given", yes[b.DDPI]}, {"Basic services demat account", yes[b.BSDA]},
	}
	for _, j := range b.JointHolders {
		rows = append(rows, []string{"Joint holder", j.Name + " · PAN " + MaskPAN(j.PAN)})
	}
	rep.Sections = []Section{{Title: "Your demat account", Columns: []Column{{"item", "Item", ColText}, {"value", "Detail", ColText}}, Rows: rows}}
	rep.Notes = []string{"Check these details. If anything is wrong, tell the DP at once. The rights and obligations of the beneficial owner and the depository participant apply to this account.", odrLine}
	rep.normalise()
	if _, err := o.dispatchDoc(ctx, tenant, actor, docRequest{Kind: DocCMR, Ref: b.BOID, Date: day, UCC: c.UCC, Report: reportPDF(rep, set), Title: rep.Title,
		FileName: "ClientMaster_" + b.BOID + ".pdf", Text: fmt.Sprintf("Dear %s,\n\nYour demat account %s is open. Its client master is attached; check the details and tell us if anything is wrong.", c.Name, b.BOID)}); err != nil {
		o.log(ctx, tenant, actor, "dispatch.error", c.UCC, map[string]any{"kind": DocCMR, "error": err.Error()})
	}
}
