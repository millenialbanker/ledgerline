package ops

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"io"
	"math"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
)

// ---- Boards: templates and extras ----
//
//   - Board templates: a gallery of Ledgerline's own templates for a
//     broking firm's teams (made into a board when used, never stored
//     until then) beside the firm's own team boards marked as templates.
//   - Mirror cards: a card on another team board that shows a card's
//     title, description and dates, kept up to date whenever its board is
//     read. Changes made on a mirror go to the original, as the person who
//     made them, or are undone with a notice saying why. A mirror shows
//     nothing of its original unless everyone who can see the mirror's
//     board can also see the original's.
//   - Email-to-board: a team board's admins switch on an address that a
//     mail provider posts inbound mail to; each email becomes a card.
//     While it is on, each card has an address too: mail to it is a
//     comment on the card.
//   - Stickers on cards. (The AI on cards is in boardai.go; import,
//     export, collections and recently viewed boards in boardio.go.)
//
// Card templates, custom fields, covers, backgrounds and votes use the core
// operations in boards.go; their screens are in web/boardextras.js.

const (
	cBoardMirror = "boardmirror" // "<original board>|<original card>|<mirror board>|<mirror card>"
	cBoardMail   = "boardmail"   // "<board>|<hash of the Message-ID>"

	mirrorActor = "system:mirror"
	mailActor   = "system:email"

	// BoardMaxMail is the largest email a board takes.
	BoardMaxMail = 10 << 20

	mirrorHiddenTitle = "Mirror of a card on another board"
)

func init() {
	boardReadHooks = append(boardReadHooks, func(ctx context.Context, o *Ops, tenant string, b Board) error {
		return o.syncMirrors(ctx, tenant, b, "")
	})
	OnBoardEvent(func(ctx context.Context, o *Ops, tenant string, ev BoardEvent) {
		if ev.Kind == "card.archived" && ev.Card != "" {
			o.archiveMirrorsOf(ctx, tenant, ev.Board, ev.Card)
		}
	})
}

// ---- board templates ----

type tplChecklist struct {
	Name  string
	Items []string
}

type tplCard struct {
	List       int // index into the template's lists
	Title      string
	About      string
	Labels     []string // label names
	Checklists []tplChecklist
	Template   bool // a card template on the new board
}

type boardTpl struct {
	ID     string
	Name   string
	Team   string
	About  string
	Color  string
	Lists  []string
	Labels []BoardLabel // name and colour
	Fields []BoardField // name, type, options, on front
	Cards  []tplCard
}

func chk(name string, items ...string) tplChecklist { return tplChecklist{Name: name, Items: items} }

func lbl(name, color string) BoardLabel { return BoardLabel{Name: name, Color: color} }

func fld(name, typ string, front bool, options ...string) BoardField {
	return BoardField{Name: name, Type: typ, OnFront: front, Options: options}
}

// boardTemplates are Ledgerline's own board templates. They name the work
// in general terms: the dates and formats come from the circulars in force,
// which each card asks the team to check.
var boardTemplates = []boardTpl{
	{
		ID: "bt-onboarding", Name: "Client onboarding follow-ups", Team: "Client onboarding", Color: "green",
		About: "Every new account from the application to the first login: documents, verification, the KRA and exchange uploads, and the welcome.",
		Lists: []string{"New applications", "Waiting on the client", "Verification", "Uploads to KRA and exchanges", "Activated"},
		Labels: []BoardLabel{lbl("Individual", "green"), lbl("Non-individual", "purple"), lbl("NRI", "sky"), lbl("Documents missing", "orange"),
			lbl("Derivatives requested", "blue"), lbl("Urgent", "red")},
		Fields: []BoardField{fld("Application no.", "text", true), fld("Came through", "list", true, "Branch", "Authorised person", "Online", "Referral"),
			fld("Received on", "date", false), fld("Branch or AP code", "text", false)},
		Cards: []tplCard{
			{List: 0, Title: "How this board works", About: "One card per application. Move a card to the right as it moves on. While you are chasing papers keep it in Waiting on the client with the Documents missing label, and note in a comment what was asked for and when. Archive cards a month after activation."},
			{List: 0, Template: true, Title: "Follow-up: client name", About: "Make a card from this template for each new application (Create from template in the list) and put the client's name in the title.",
				Checklists: []tplChecklist{
					chk("Documents", "Account opening form complete and signed", "PAN verified", "Proof of address", "Bank proof (cancelled cheque or bank statement)",
						"Photograph and signature", "Proof of income, if derivatives are requested", "Nomination details or the opt-out declaration"),
					chk("Verification and uploads", "In-person or video verification done", "KYC uploaded to the KRA and the CKYC record updated",
						"Client code (UCC) uploaded to the exchanges", "Demat account opened or linked", "Welcome email with the login details sent"),
				}},
			{List: 1, Title: "Chase applications waiting on the client", Labels: []string{"Documents missing"},
				Checklists: []tplChecklist{chk("Each week", "Call or email every client in this list", "Note the reply on their card", "Escalate applications waiting more than a month to the branch head")}},
		},
	},
	{
		ID: "bt-month-end", Name: "Month-end close", Team: "Accounts", Color: "blue",
		About: "The month's closing work in order: interest on client debits, DP bills with GST, authorised person commission, statements and the books.",
		Lists: []string{"To do", "In progress", "For review", "Done"},
		Labels: []BoardLabel{lbl("Interest", "yellow"), lbl("DP billing", "sky"), lbl("AP commission", "purple"), lbl("Statements", "green"),
			lbl("Books", "blue"), lbl("Blocked", "red")},
		Fields: []BoardField{fld("Month", "text", true), fld("Reviewed by", "text", true), fld("Amount (Rs)", "number", false), fld("Posted on", "date", false)},
		Cards: []tplCard{
			{List: 0, Title: "Interest on client debit balances", Labels: []string{"Interest"},
				Checklists: []tplChecklist{chk("Steps", "Check the interest rate and the clients on a rate of their own", "Run the interest calculation for the month",
					"Review the largest charges and any client in dispute", "Post the interest entries", "Tell clients as the firm's practice requires")}},
			{List: 0, Title: "DP bills with GST", Labels: []string{"DP billing"},
				Checklists: []tplChecklist{chk("Steps", "Load the month's depository transactions and holdings", "Run DP billing for the month",
					"Check GST on a sample: CGST and SGST, or IGST, by the client's state", "Post the bills to the clients' ledgers",
					"Match the depository's own bill to the firm's records")}},
			{List: 0, Title: "Authorised person commission", Labels: []string{"AP commission"},
				Checklists: []tplChecklist{chk("Steps", "Confirm the brokerage earned through each authorised person", "Apply the sharing in each agreement",
					"Deduct TDS where it applies", "Collect the GST invoice from registered authorised persons", "Approve and pay")}},
			{List: 0, Title: "Client statements", Labels: []string{"Statements"},
				Checklists: []tplChecklist{chk("Steps", "Generate the statements of account for the month", "Check a few against the ledger",
					"Send them and note the emails that bounced", "Follow up the bounced emails")}},
			{List: 0, Title: "Bank reconciliation", Labels: []string{"Books"},
				Checklists: []tplChecklist{chk("Steps", "Reconcile each client bank account", "Reconcile the firm's own bank accounts", "Explain or clear items older than the month")}},
			{List: 0, Title: "Close the books", Labels: []string{"Books"},
				Checklists: []tplChecklist{chk("Steps", "Suspense and unallocated receipts cleared or explained", "Trial balance reviewed", "Month locked after the review")}},
		},
	},
	{
		ID: "bt-compliance", Name: "Compliance calendar", Team: "Compliance", Color: "purple",
		About:  "Recurring filings and reviews, by how often they fall due. Set each card's due date from the circular in force, and copy the card for the next period once it is filed.",
		Lists:  []string{"Monthly", "Quarterly", "Half-yearly", "Yearly", "As your category requires", "Filed"},
		Labels: []BoardLabel{lbl("NSE", "blue"), lbl("BSE", "sky"), lbl("MCX", "orange"), lbl("Depository", "green"), lbl("SEBI", "purple"), lbl("Internal", "black")},
		Fields: []BoardField{fld("Period", "text", true), fld("Filed with", "list", true, "NSE", "BSE", "MCX", "NCDEX", "CDSL", "NSDL", "SEBI", "Other"),
			fld("Filed on", "date", false), fld("Acknowledgement no.", "text", false)},
		Cards: []tplCard{
			{List: 0, Template: true, Title: "Filing: name and period", About: "Make a card from this template for each filing, with the due date the circular sets.",
				Checklists: []tplChecklist{chk("Steps", "Data prepared", "Reviewed by the compliance officer", "Filed on the portal", "Acknowledgement saved on this card", "Card made for the next period")}},
			{List: 0, Title: "Running account settlement", About: "Settle running accounts on the dates the exchanges publish for the year, for clients on monthly and on quarterly settlement.",
				Checklists: []tplChecklist{chk("Each settlement", "Next settlement date checked in the exchanges' calendar", "Settlement run and money paid out",
					"Retention statements sent", "Payment proofs kept")}},
			{List: 0, Title: "Reconcile client funds and securities", Labels: []string{"Internal"},
				Checklists: []tplChecklist{chk("Steps", "Client bank balances against the client ledger", "Depository holdings against the back office", "Unexplained differences raised with the compliance officer")}},
			{List: 0, Title: "Client complaints", Labels: []string{"Internal"},
				Checklists: []tplChecklist{chk("Steps", "Every open complaint has an owner and a reply date", "SCORES and exchange complaints answered in time", "The month's complaints entered in the register")}},
			{List: 1, Title: "Surveillance report for the quarter", Labels: []string{"NSE", "BSE"}, About: "The status of the quarter's surveillance alerts, to each exchange the firm trades on, by the date the circular in force sets.",
				Checklists: []tplChecklist{chk("Steps", "Alerts by name: opened, closed, referred and pending", "Each alert referred to the exchange, with the action taken",
					"Any other major surveillance action", "A nil report where there was nothing", "Acknowledgement saved on this card")}},
			{List: 2, Title: "Net worth certificate", About: "The chartered accountant's net worth certificate for the half year, filed as the exchanges require.",
				Checklists: []tplChecklist{chk("Steps", "Accounts for the half year closed", "Certificate signed by the chartered accountant", "Filed and acknowledgement saved")}},
			{List: 2, Title: "Internal audit", About: "The internal audit for the half year by an independent auditor, and its report to the exchanges as they require.",
				Checklists: []tplChecklist{chk("Steps", "Auditor appointed and the scope agreed", "Records shared with the auditor", "Each observation answered with the action taken", "Report submitted and acknowledgement saved")}},
			{List: 3, Title: "Annual accounts and returns", Labels: []string{"Internal"},
				Checklists: []tplChecklist{chk("Steps", "Audited accounts adopted", "Annual returns to the exchanges and depositories as they require", "Acknowledgements saved")}},
			{List: 3, Title: "Review the firm's policies", Labels: []string{"Internal"},
				Checklists: []tplChecklist{chk("Policies", "KYC and anti-money laundering", "Risk management", "Surveillance", "Cyber security", "Approval recorded")}},
			{List: 4, Title: "System audit", About: "How often depends on the member type; set the due dates from the exchange's circular in force.",
				Checklists: []tplChecklist{chk("Steps", "Audit plan agreed with the auditor", "Preliminary report submitted", "Action taken report on any non-compliance", "Acknowledgements saved")}},
			{List: 4, Title: "Cyber security audit and VAPT", Labels: []string{"SEBI"}, About: "Under SEBI's cyber security and cyber resilience framework (CSCRF), at the frequency the firm's category requires.",
				Checklists: []tplChecklist{chk("Steps", "The firm's CSCRF category confirmed", "Cyber audit and VAPT done", "Findings closed and retested", "Reports submitted as required")}},
		},
	},
	{
		ID: "bt-inspection", Name: "Inspection and audit preparation", Team: "Compliance", Color: "orange",
		About: "Get ready for an exchange, depository or SEBI inspection or an internal audit: what was asked for, who prepares it, what went in, and the observations to close.",
		Lists: []string{"Asked for", "Preparing", "Ready for review", "Submitted", "Observations", "Closed"},
		Labels: []BoardLabel{lbl("Exchange", "blue"), lbl("Depository", "green"), lbl("SEBI", "purple"), lbl("Internal audit", "sky"),
			lbl("Observation", "orange"), lbl("Overdue", "red")},
		Fields: []BoardField{fld("Asked for by", "list", true, "NSE", "BSE", "MCX", "NCDEX", "CDSL", "NSDL", "SEBI", "Internal auditor", "Other"),
			fld("Submit by", "date", true), fld("Reference", "text", false), fld("Period covered", "text", false)},
		Cards: []tplCard{
			{List: 0, Title: "Start here: the inspection notice",
				Checklists: []tplChecklist{chk("Steps", "Period under inspection and the records asked for noted", "One person named to coordinate",
					"A card for each record asked for, with an owner and a date", "Room and system access arranged for the inspection team")}},
			{List: 0, Title: "Client KYC sample",
				Checklists: []tplChecklist{chk("Steps", "Account opening forms of the sampled clients pulled", "KYC, nomination, bank and demat proofs checked", "Gaps fixed or explained before submission")}},
			{List: 0, Title: "Client funds and securities",
				Checklists: []tplChecklist{chk("Steps", "Client bank balances against the ledger for the dates asked", "Depository holdings against the back office", "Differences explained in writing")}},
			{List: 0, Title: "Contract notes and statements",
				Checklists: []tplChecklist{chk("Steps", "Contract notes for the sampled dates", "Proof they were sent (email logs)", "Statements of account and retention statements")}},
			{List: 0, Title: "Complaints",
				Checklists: []tplChecklist{chk("Steps", "Complaints received and resolved in the period", "SCORES and exchange complaints with their status")}},
			{List: 4, Template: true, Title: "Observation: short title", Labels: []string{"Observation"},
				Checklists: []tplChecklist{chk("Steps", "Observation in the inspector's words", "Root cause", "Action taken", "Evidence attached to this card", "Reply sent and acknowledged")}},
		},
	},
	{
		ID: "bt-visits", Name: "Branch and AP visits", Team: "Branches and partners", Color: "sky",
		About:  "Plan and record visits to branches and authorised persons: what was checked, what was found and the follow-up.",
		Lists:  []string{"To plan", "Scheduled", "Visited", "Follow-up", "Closed"},
		Labels: []BoardLabel{lbl("Branch", "blue"), lbl("Authorised person", "purple"), lbl("Issue found", "orange"), lbl("Serious issue", "red"), lbl("All clear", "green")},
		Fields: []BoardField{fld("Code", "text", true), fld("Visit date", "date", true), fld("Visited by", "text", false),
			fld("Outcome", "list", false, "Satisfactory", "Needs improvement", "Unsatisfactory")},
		Cards: []tplCard{
			{List: 0, Title: "Plan this quarter's visits",
				Checklists: []tplChecklist{chk("Steps", "Branches and authorised persons due a visit under the firm's policy listed", "Locations with complaints or alerts first",
					"A visit card for each, with a date and who goes")}},
			{List: 0, Template: true, Title: "Visit: branch or AP name", About: "Make a card from this template for each visit and put the location on the card.",
				Checklists: []tplChecklist{
					chk("At the office", "Registration details and required notices displayed", "Client documents kept as the firm requires",
						"Terminals and user IDs match the approved list", "No client money or securities taken by the authorised person",
						"Complaints at this location reviewed", "Photos and notes attached to this card"),
					chk("After the visit", "Visit report shared with the branch or authorised person", "Each issue given an owner and a date", "Follow-up visit booked if needed"),
				}},
		},
	},
	{
		ID: "bt-cyber", Name: "IT and cyber security tasks", Team: "IT", Color: "black",
		About:  "Recurring IT and cyber security work: SEBI's cyber security and cyber resilience framework (CSCRF), VAPT, backups, access reviews and incidents.",
		Lists:  []string{"Backlog", "This month", "In progress", "Waiting on a vendor", "Done"},
		Labels: []BoardLabel{lbl("CSCRF", "purple"), lbl("VAPT", "orange"), lbl("Backups", "green"), lbl("Access review", "blue"), lbl("Incident", "red"), lbl("Vendor", "sky")},
		Fields: []BoardField{fld("System", "text", true), fld("Severity", "list", true, "Low", "Medium", "High", "Critical"), fld("Vendor", "text", false), fld("Next due", "date", false)},
		Cards: []tplCard{
			{List: 0, Title: "CSCRF: what applies to the firm", Labels: []string{"CSCRF"},
				Checklists: []tplChecklist{chk("Steps", "The firm's category under CSCRF confirmed", "Cyber security policy reviewed and approved",
					"Cyber audit at the frequency the category requires", "Reports to the exchanges and depositories as required")}},
			{List: 0, Title: "VAPT", Labels: []string{"VAPT"},
				Checklists: []tplChecklist{chk("Steps", "Scope agreed: internet-facing and critical systems", "Tested by a CERT-In empanelled auditor", "Findings fixed and retested", "Report submitted as required")}},
			{List: 1, Title: "Backups and a restore test", Labels: []string{"Backups"},
				Checklists: []tplChecklist{chk("Steps", "Last night's backups completed", "A backup restored to a test system", "Time to restore recorded", "An offline or immutable copy kept")}},
			{List: 1, Title: "User access review", Labels: []string{"Access review"},
				Checklists: []tplChecklist{chk("Steps", "Every user of the back office, trading and depository systems listed", "Leavers and unused accounts removed",
					"Administrator rights still needed", "Review recorded with who did it")}},
			{List: 1, Title: "Patches and updates",
				Checklists: []tplChecklist{chk("Steps", "Operating system and browser updates applied", "Endpoint protection up to date", "Firewall rules reviewed")}},
			{List: 0, Template: true, Title: "Incident: what happened", Labels: []string{"Incident"},
				Checklists: []tplChecklist{chk("Steps", "Contained: affected systems isolated", "Timeline recorded", "Reported to CERT-In within 6 hours, and to the exchanges and depositories as required",
					"Root cause found and fixed", "Lessons noted for the next drill")}},
		},
	},
	{
		ID: "bt-weekly", Name: "Weekly team meeting", Team: "Any team", Color: "lime",
		About:  "An agenda everyone adds to, the decisions taken, and actions with an owner and a date.",
		Lists:  []string{"Agenda", "Discussed", "Actions", "Done"},
		Labels: []BoardLabel{lbl("Operations", "blue"), lbl("Risk", "orange"), lbl("Accounts", "yellow"), lbl("Compliance", "purple"), lbl("IT", "black"), lbl("Decision", "green")},
		Fields: []BoardField{fld("Meeting", "date", true), fld("Owner", "text", true)},
		Cards: []tplCard{
			{List: 0, Title: "How we run this meeting", About: "Add a card to Agenda before the meeting. In the meeting move each card to Discussed, and make an action card, with a member and a due date, for anything someone has to do. Start each meeting with the open actions.",
				Checklists: []tplChecklist{chk("Every week", "Open actions reviewed first", "Agenda cards discussed", "Each action has an owner and a due date", "Done cards archived")}},
			{List: 0, Template: true, Title: "Agenda item",
				Checklists: []tplChecklist{chk("Before the meeting", "What we need to decide", "Figures or papers attached")}},
			{List: 2, Template: true, Title: "Action: what and who", About: "Give the card a member and a due date."},
		},
	},
}

func builtinTemplate(id string) (boardTpl, bool) {
	for _, t := range boardTemplates {
		if t.ID == id {
			return t, true
		}
	}
	return boardTpl{}, false
}

// BoardTemplateInfo is a template in the gallery.
type BoardTemplateInfo struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Team        string       `json:"team,omitempty"`
	Description string       `json:"description,omitempty"`
	Color       string       `json:"color"`
	Builtin     bool         `json:"builtin"`
	Lists       []string     `json:"lists"`
	Labels      []BoardLabel `json:"labels"`
	Fields      []string     `json:"fields"`
	Cards       []string     `json:"cards"` // starter card titles
	Templates   int          `json:"templates"`
	Items       int          `json:"items"` // checklist items on the starter cards
	By          string       `json:"by,omitempty"`
	UpdatedAt   string       `json:"updated_at,omitempty"`
	CanEdit     bool         `json:"can_edit,omitempty"` // the person is an admin of the template board
}

// BoardTemplates is the gallery: Ledgerline's templates, then the firm's
// own template boards the person can see.
func (o *Ops) BoardTemplates(ctx context.Context, tenant, actor string) ([]BoardTemplateInfo, error) {
	u, err := o.boardUser(ctx, tenant, actor)
	if err != nil {
		return nil, err
	}
	out := []BoardTemplateInfo{}
	for _, t := range boardTemplates {
		in := BoardTemplateInfo{ID: t.ID, Name: t.Name, Team: t.Team, Description: t.About, Color: t.Color, Builtin: true, Lists: t.Lists, Labels: []BoardLabel{}, Fields: []string{}, Cards: []string{}}
		for _, l := range t.Labels {
			in.Labels = append(in.Labels, BoardLabel{Name: l.Name, Color: l.Color})
		}
		for _, f := range t.Fields {
			in.Fields = append(in.Fields, f.Name)
		}
		for _, c := range t.Cards {
			in.Cards = append(in.Cards, c.Title)
			if c.Template {
				in.Templates++
			}
			for _, cl := range c.Checklists {
				in.Items += len(cl.Items)
			}
		}
		out = append(out, in)
	}
	bs, err := docs.List[Board](ctx, o.Docs, tenant, cBoard, "")
	if err != nil {
		return nil, err
	}
	var firm []BoardTemplateInfo
	for _, b := range bs {
		r := boardRole(b, u)
		if !b.Template || b.Deleted || b.Closed || b.Kind != BoardTeam || r == "" {
			continue
		}
		in := BoardTemplateInfo{ID: b.ID, Name: b.Name, Description: b.Description, Color: b.Background, Lists: []string{}, Labels: []BoardLabel{}, Fields: []string{}, Cards: []string{},
			By: b.CreatedBy, UpdatedAt: b.UpdatedAt, CanEdit: r == BoardAdmin}
		if !boardColorOK(in.Color) || in.Color == "" {
			in.Color = "blue"
		}
		for _, l := range b.Lists {
			if !l.Archived {
				in.Lists = append(in.Lists, l.Name)
			}
		}
		for _, l := range b.Labels {
			in.Labels = append(in.Labels, BoardLabel{Name: l.Name, Color: l.Color})
		}
		for _, f := range b.Fields {
			in.Fields = append(in.Fields, f.Name)
		}
		cs, _ := o.boardCards(ctx, tenant, b.ID)
		for _, c := range cs {
			if c.Archived {
				continue
			}
			in.Cards = append(in.Cards, c.Title)
			if c.Template {
				in.Templates++
			}
			for _, cl := range c.Checklists {
				in.Items += len(cl.Items)
			}
		}
		firm = append(firm, in)
	}
	sort.Slice(firm, func(i, j int) bool { return strings.ToLower(firm[i].Name) < strings.ToLower(firm[j].Name) })
	return append(out, firm...), nil
}

// BoardTemplateUse is how a board is made from a template.
type BoardTemplateUse struct {
	Name       string `json:"name"`
	Visibility string `json:"visibility"`
	Cards      bool   `json:"cards"` // keep the starter cards (with their checklists)
}

// UseBoardTemplate makes a new team board from a template: one of
// Ledgerline's (id "bt-…") or a template board of the firm's.
func (o *Ops) UseBoardTemplate(ctx context.Context, tenant, actor, id string, in BoardTemplateUse) (Board, error) {
	u, err := o.boardUser(ctx, tenant, actor)
	if err != nil {
		return Board{}, err
	}
	vis := firstOf(in.Visibility, "firm")
	if vis != "firm" && vis != "private" {
		return Board{}, invalid("a board is firm-wide or private")
	}
	if u.Role == RoleBranch || u.Role == RoleAP {
		vis = "private" // branch and partner staff keep their boards private
	}
	name := strings.TrimSpace(in.Name)
	if t, ok := builtinTemplate(id); ok {
		return o.boardFromBuiltin(ctx, tenant, actor, t, firstOf(name, t.Name), vis, in.Cards)
	}
	src, _, err := o.boardFor(ctx, tenant, actor, id)
	if err != nil {
		return Board{}, fmt.Errorf("%w: no such template", ErrNotFound)
	}
	if !src.Template || src.Kind != BoardTeam {
		return Board{}, invalid("%s is not a template; one of its admins can make it one", src.Name)
	}
	b, err := o.CopyBoard(ctx, tenant, actor, id, firstOf(name, src.Name), in.Cards)
	if err != nil {
		return b, err
	}
	if b.Visibility != vis {
		if b, err = o.UpdateBoard(ctx, tenant, actor, b.ID, BoardInput{Visibility: vis}); err != nil {
			return b, err
		}
	}
	o.boardLog(ctx, tenant, actor, b, "", "board.from_template", "made this board from the template "+src.Name, map[string]string{"template": src.ID})
	o.log(ctx, tenant, actor, "board.from_template", b.ID, map[string]any{"template": src.ID, "cards": in.Cards})
	return b, nil
}

func (o *Ops) boardFromBuiltin(ctx context.Context, tenant, actor string, t boardTpl, name, vis string, withCards bool) (Board, error) {
	b, err := o.CreateBoard(ctx, tenant, actor, BoardInput{Name: name, Description: &t.About, Visibility: vis, Background: t.Color, Lists: t.Lists})
	if err != nil {
		return b, err
	}
	labels := map[string]string{}
	b, err = docs.Update(ctx, o.Docs, tenant, cBoard, b.ID, actor, func(x *Board) error {
		x.Labels, x.Fields = nil, nil
		for _, l := range t.Labels {
			nl := BoardLabel{ID: boardID("LB-", 4), Name: l.Name, Color: l.Color}
			labels[l.Name] = nl.ID
			x.Labels = append(x.Labels, nl)
		}
		for _, f := range t.Fields {
			nf := BoardField{ID: boardID("F-", 4), Name: f.Name, Type: f.Type, OnFront: f.OnFront, Options: append([]string(nil), f.Options...)}
			x.Fields = append(x.Fields, nf)
		}
		return nil
	})
	if err != nil {
		return b, err
	}
	o.boardLog(ctx, tenant, actor, b, "", "board.from_template", "made this board from the Ledgerline template "+t.Name, map[string]string{"template": t.ID})
	o.log(ctx, tenant, actor, "board.from_template", b.ID, map[string]any{"template": t.ID, "cards": withCards})
	if !withCards {
		return b, nil
	}
	for _, c := range t.Cards {
		nc := Card{Title: c.Title, Description: c.About, Template: c.Template}
		for _, l := range c.Labels {
			if id, ok := labels[l]; ok {
				nc.Labels = append(nc.Labels, id)
			}
		}
		for _, cl := range c.Checklists {
			ncl := Checklist{ID: boardID("CL-", 4), Name: cl.Name}
			for _, it := range cl.Items {
				ncl.Items = append(ncl.Items, CheckItem{ID: boardID("I-", 4), Text: it})
			}
			nc.Checklists = append(nc.Checklists, ncl)
		}
		if _, err := o.addCard(ctx, tenant, actor, b, b.Lists[c.List].ID, nc, math.NaN()); err != nil {
			return b, err
		}
	}
	b, _, err = docs.Get[Board](ctx, o.Docs, tenant, cBoard, b.ID)
	return b, err
}

// ---- mirror cards ----

// boardMirrorDoc ties a mirror to its original and keeps what the mirror
// was last given, so a change made on the mirror itself can be told apart
// from a change to the original.
type boardMirrorDoc struct {
	Source      CardRef `json:"source"`
	Mirror      CardRef `json:"mirror"`
	Title       string  `json:"title"`
	Description string  `json:"description,omitempty"`
	Due         string  `json:"due,omitempty"`
	DueDone     bool    `json:"due_done,omitempty"`
	Hidden      bool    `json:"hidden,omitempty"` // not everyone on the mirror's board can see the original
	Gone        bool    `json:"gone,omitempty"`   // this pairing ended (the original moved)
	At          string  `json:"at"`
}

func mirrorKey(src CardRef, board, card string) string {
	return src.Board + "|" + src.Card + "|" + board + "|" + card
}

// audienceSees reports whether everyone who can see board a can see board b.
func audienceSees(us []User, a, b Board) bool {
	for _, u := range us {
		if u.Active && u.Role != RoleClient && boardRole(a, u) != "" && boardRole(b, u) == "" {
			return false
		}
	}
	return true
}

// mirrorView is what a mirror on board mb shows of its original on sb.
func mirrorView(us []User, mb, sb Board, src Card) boardMirrorDoc {
	if !audienceSees(us, mb, sb) {
		return boardMirrorDoc{Title: mirrorHiddenTitle, Hidden: true}
	}
	return boardMirrorDoc{Title: src.Title, Description: src.Description, Due: src.Due, DueDone: src.DueDone}
}

func (v boardMirrorDoc) shownOn(c Card) bool {
	return c.Title == v.Title && c.Description == v.Description && c.Due == v.Due && c.DueDone == v.DueDone
}

// MirrorTarget is a board a card can be mirrored to, with its open lists.
type MirrorTarget struct {
	ID    string      `json:"id"`
	Name  string      `json:"name"`
	Lists []BoardList `json:"lists"`
}

// MirrorTargets are the team boards, other than this one, where the person
// can add cards.
func (o *Ops) MirrorTargets(ctx context.Context, tenant, actor, board string) ([]MirrorTarget, error) {
	bs, err := o.Boards(ctx, tenant, actor)
	if err != nil {
		return nil, err
	}
	out := []MirrorTarget{}
	for _, s := range bs {
		if s.ID == board || s.Kind != BoardTeam || s.Closed || (s.Role != BoardAdmin && s.Role != BoardMember) {
			continue
		}
		b, _, err := docs.Get[Board](ctx, o.Docs, tenant, cBoard, s.ID)
		if err != nil {
			continue
		}
		t := MirrorTarget{ID: b.ID, Name: b.Name, Lists: []BoardList{}}
		for _, l := range b.Lists {
			if !l.Archived {
				t.Lists = append(t.Lists, l)
			}
		}
		sort.SliceStable(t.Lists, func(i, j int) bool { return t.Lists[i].Pos < t.Lists[j].Pos })
		if len(t.Lists) > 0 {
			out = append(out, t)
		}
	}
	return out, nil
}

// MirrorCard puts a mirror of a card on another team board.
func (o *Ops) MirrorCard(ctx context.Context, tenant, actor, board, card string, to CardMove) (Card, error) {
	src, sb, err := o.GetCard(ctx, tenant, actor, board, card)
	if err != nil {
		return Card{}, err
	}
	if src.Mirror != nil { // a mirror of a mirror shows the original
		orig, ob, err := o.GetCard(ctx, tenant, actor, src.Mirror.Board, src.Mirror.Card)
		if err != nil {
			return Card{}, invalid("#%d is a mirror of a card you cannot see, so it cannot be mirrored", src.No)
		}
		src, sb = orig, ob
	}
	if src.Archived {
		return Card{}, invalid("restore #%d before mirroring it", src.No)
	}
	if to.Board == "" || to.Board == sb.ID {
		return Card{}, invalid("a mirror goes on another board")
	}
	tb, _, err := o.boardFor(ctx, tenant, actor, to.Board, BoardAdmin, BoardMember)
	if err != nil {
		if errors.Is(err, ErrForbidden) {
			return Card{}, fmt.Errorf("%w: you can mirror cards only to boards where you can add cards, and you are an observer of %s", ErrForbidden, tb.Name)
		}
		return Card{}, err
	}
	if tb.Kind != BoardTeam {
		return Card{}, invalid("mirrors go on team boards; a Ledgerline board's cards come from its records")
	}
	ref := CardRef{Board: sb.ID, Card: src.ID}
	snaps, _ := docs.List[boardMirrorDoc](ctx, o.Docs, tenant, cBoardMirror, ref.Board+"|"+ref.Card+"|")
	for _, s := range snaps {
		if s.Gone || s.Mirror.Board != tb.ID {
			continue
		}
		if m, _, err := docs.Get[Card](ctx, o.Docs, tenant, cBoardCard, cardKey(s.Mirror.Board, s.Mirror.Card)); err == nil && !m.Deleted && !m.Archived {
			return Card{}, invalid("#%d already has a mirror on %s (#%d)", src.No, tb.Name, m.No)
		}
	}
	us, _ := o.Users(ctx, tenant)
	v := mirrorView(us, tb, sb, src)
	pos := math.NaN()
	if to.Pos != nil {
		pos = *to.Pos
	}
	m, err := o.addCard(ctx, tenant, actor, tb, to.List, Card{Title: v.Title, Description: v.Description, Due: v.Due, DueDone: v.DueDone, Mirror: &ref}, pos)
	if err != nil {
		return Card{}, err
	}
	v.Source, v.Mirror, v.At = ref, CardRef{Board: tb.ID, Card: m.ID}, o.now().Format(time.RFC3339)
	if err := docs.Upsert(ctx, o.Docs, tenant, cBoardMirror, mirrorKey(ref, tb.ID, m.ID), v, actor); err != nil {
		return m, err
	}
	where := "another board"
	if audienceSees(us, sb, tb) {
		where = "the board " + tb.Name
	}
	o.boardLog(ctx, tenant, actor, sb, src.ID, "card.mirrored", fmt.Sprintf("mirrored #%d to %s", src.No, where), map[string]string{"board": tb.ID, "card": m.ID})
	o.log(ctx, tenant, actor, "board.card_mirrored", sb.ID, map[string]any{"card": src.ID, "to_board": tb.ID, "mirror": m.ID})
	return m, nil
}

// syncMirrors brings a board's mirrors up to date with their originals
// (only one card's when only is given).
func (o *Ops) syncMirrors(ctx context.Context, tenant string, b Board, only string) error {
	if b.Kind != BoardTeam {
		return nil
	}
	cs, err := o.boardCards(ctx, tenant, b.ID)
	if err != nil {
		return err
	}
	var us []User
	loaded := false
	var first error
	for _, c := range cs {
		if c.Mirror == nil || c.Archived || (only != "" && c.ID != only) {
			continue
		}
		if !loaded {
			us, _ = o.Users(ctx, tenant)
			loaded = true
		}
		if err := o.syncMirror(ctx, tenant, b, c, us); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// syncMirrorsOf brings every mirror of a card up to date.
func (o *Ops) syncMirrorsOf(ctx context.Context, tenant string, ref CardRef) {
	snaps, _ := docs.List[boardMirrorDoc](ctx, o.Docs, tenant, cBoardMirror, ref.Board+"|"+ref.Card+"|")
	for _, s := range snaps {
		if s.Gone {
			continue
		}
		if mb, _, err := docs.Get[Board](ctx, o.Docs, tenant, cBoard, s.Mirror.Board); err == nil && !mb.Deleted {
			_ = o.syncMirrors(ctx, tenant, mb, s.Mirror.Card)
		}
	}
}

func (o *Ops) syncMirror(ctx context.Context, tenant string, b Board, c Card, us []User) error {
	now := o.now().Format(time.RFC3339)
	ref := *c.Mirror
	src, _, err := docs.Get[Card](ctx, o.Docs, tenant, cBoardCard, cardKey(ref.Board, ref.Card))
	for i := 0; i < 5 && err == nil && src.Deleted && src.MovedTo != ""; i++ { // the original went to another board
		nb, nc, _ := strings.Cut(src.MovedTo, "|")
		ref = CardRef{Board: nb, Card: nc}
		src, _, err = docs.Get[Card](ctx, o.Docs, tenant, cBoardCard, cardKey(ref.Board, ref.Card))
	}
	var sb Board
	if err == nil && !src.Deleted {
		sb, _, err = docs.Get[Board](ctx, o.Docs, tenant, cBoard, ref.Board)
	}
	if err != nil || src.Deleted || sb.Deleted || src.Archived {
		why := "its original card was deleted"
		if err == nil && !src.Deleted && !sb.Deleted {
			why = "its original card was archived"
		}
		m, uerr := docs.Update(ctx, o.Docs, tenant, cBoardCard, cardKey(b.ID, c.ID), mirrorActor, func(x *Card) error {
			if x.Deleted || x.Archived {
				return errCardGone
			}
			x.Archived, x.UpdatedAt = true, now
			return nil
		})
		if uerr == nil {
			o.boardLog(ctx, tenant, mirrorActor, b, c.ID, "card.archived", fmt.Sprintf("archived the mirror #%d: %s", m.No, why), nil)
		}
		return nil
	}
	oldKey := mirrorKey(*c.Mirror, b.ID, c.ID)
	snap, _, serr := docs.Get[boardMirrorDoc](ctx, o.Docs, tenant, cBoardMirror, oldKey)
	if ref != *c.Mirror {
		r := ref
		if c, err = docs.Update(ctx, o.Docs, tenant, cBoardCard, cardKey(b.ID, c.ID), mirrorActor, func(x *Card) error { x.Mirror = &r; return nil }); err != nil {
			return err
		}
		if serr == nil {
			gone := snap
			gone.Gone = true
			_ = docs.Upsert(ctx, o.Docs, tenant, cBoardMirror, oldKey, gone, mirrorActor)
		}
	}
	// A change made on the mirror itself goes to the original.
	if serr == nil && !snap.shownOn(c) {
		if s2, ok := o.forwardMirrorEdit(ctx, tenant, b, c, snap, ref); ok {
			src = s2
		}
	}
	v := mirrorView(us, b, sb, src)
	if !v.shownOn(c) {
		if _, err := docs.Update(ctx, o.Docs, tenant, cBoardCard, cardKey(b.ID, c.ID), mirrorActor, func(x *Card) error {
			if x.Deleted {
				return errCardGone
			}
			if x.Due != v.Due {
				x.Reminded = ""
			}
			x.Title, x.Description, x.Due, x.DueDone, x.UpdatedAt = v.Title, v.Description, v.Due, v.DueDone, now
			return nil
		}); err != nil {
			return err
		}
	}
	if serr != nil || !v.shownOn(Card{Title: snap.Title, Description: snap.Description, Due: snap.Due, DueDone: snap.DueDone}) || snap.Hidden != v.Hidden || snap.Source != ref {
		v.Source, v.Mirror, v.At = ref, CardRef{Board: b.ID, Card: c.ID}, now
		return docs.Upsert(ctx, o.Docs, tenant, cBoardMirror, mirrorKey(ref, b.ID, c.ID), v, mirrorActor)
	}
	return nil
}

// forwardMirrorEdit makes a change someone made on a mirror on its
// original instead, as that person; if they may not, the change is undone
// (by the caller) and they are told why.
func (o *Ops) forwardMirrorEdit(ctx context.Context, tenant string, b Board, c Card, snap boardMirrorDoc, ref CardRef) (Card, bool) {
	who := ""
	if d, err := o.Docs.Get(ctx, tenant, cBoardCard, cardKey(b.ID, c.ID)); err == nil {
		who = d.UpdatedBy
	}
	var p CardPatch
	if c.Title != snap.Title {
		t := c.Title
		p.Title = &t
	}
	if c.Description != snap.Description {
		s := c.Description
		p.Description = &s
	}
	if c.Due != snap.Due {
		s := c.Due
		p.Due = &s
	}
	if c.DueDone != snap.DueDone {
		d := c.DueDone
		p.DueDone = &d
	}
	var ferr error
	if who == "" || strings.HasPrefix(who, "system:") {
		ferr = errors.New("it was not made by a person")
	} else if ferr = o.mirrorEditAllowed(ctx, tenant, who, ref); ferr == nil {
		src, err := o.UpdateCard(ctx, tenant, who, ref.Board, ref.Card, p)
		if err == nil {
			return src, true
		}
		ferr = err
	}
	o.boardLog(ctx, tenant, mirrorActor, b, c.ID, "mirror.undone", fmt.Sprintf("undid a change to the mirror #%d: changes to a mirror are made on its original card, which the person who made it cannot change", c.No), map[string]string{"by": who})
	if who != "" && !strings.HasPrefix(who, "system:") {
		msg := strings.TrimPrefix(strings.TrimPrefix(ferr.Error(), ErrForbidden.Error()+": "), "invalid: ")
		o.boardNotify(ctx, tenant, mirrorActor, b, c.ID, []string{who}, fmt.Sprintf("Your change to the mirror #%d on %s was undone: %s", c.No, b.Name, msg))
	}
	return Card{}, false
}

// mirrorEditAllowed says whether the person may change a mirror's
// original, with the reason when not.
func (o *Ops) mirrorEditAllowed(ctx context.Context, tenant, actor string, ref CardRef) error {
	sb, v, err := o.boardFor(ctx, tenant, actor, ref.Board)
	if err != nil {
		return fmt.Errorf("%w: you cannot see the original card, so you cannot change it through its mirror", ErrForbidden)
	}
	if !v.can(BoardAdmin, BoardMember) {
		return fmt.Errorf("%w: the original card is on %s, where you can only view and comment; ask one of its members to change it", ErrForbidden, sb.Name)
	}
	if sb.Closed {
		return fmt.Errorf("%w: the original card's board %s is closed", ErrState, sb.Name)
	}
	return nil
}

var errMirrorSkip = errors.New("skip")

// archiveMirrorsOf archives the mirrors of a card that was archived.
func (o *Ops) archiveMirrorsOf(ctx context.Context, tenant, board, card string) {
	snaps, err := docs.List[boardMirrorDoc](ctx, o.Docs, tenant, cBoardMirror, board+"|"+card+"|")
	if err != nil {
		return
	}
	for _, s := range snaps {
		if s.Gone {
			continue
		}
		mb, _, err := docs.Get[Board](ctx, o.Docs, tenant, cBoard, s.Mirror.Board)
		if err != nil || mb.Deleted {
			continue
		}
		m, err := docs.Update(ctx, o.Docs, tenant, cBoardCard, cardKey(s.Mirror.Board, s.Mirror.Card), mirrorActor, func(x *Card) error {
			if x.Deleted || x.Archived || x.Mirror == nil || x.Mirror.Board != board || x.Mirror.Card != card {
				return errMirrorSkip
			}
			x.Archived, x.UpdatedAt = true, o.now().Format(time.RFC3339)
			return nil
		})
		if err == nil {
			o.boardLog(ctx, tenant, mirrorActor, mb, m.ID, "card.archived", fmt.Sprintf("archived the mirror #%d with its original", m.No), nil)
		}
	}
}

// MirrorInfo is a mirror's original, as the person may see it.
type MirrorInfo struct {
	Visible     bool   `json:"visible"`        // the person can see the original
	Gone        bool   `json:"gone,omitempty"` // the original no longer exists
	Hidden      bool   `json:"hidden"`         // the mirror shows nothing on its own board
	Board       string `json:"board,omitempty"`
	BoardName   string `json:"board_name,omitempty"`
	Card        string `json:"card,omitempty"`
	No          int64  `json:"no,omitempty"`
	List        string `json:"list,omitempty"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Due         string `json:"due,omitempty"`
	DueDone     bool   `json:"due_done,omitempty"`
	Archived    bool   `json:"archived,omitempty"`
	CanEdit     bool   `json:"can_edit"`
	Why         string `json:"why,omitempty"` // why the person cannot change it
}

// MirrorSource is the original of a mirror card. Someone who cannot see
// the original's board learns nothing about it.
func (o *Ops) MirrorSource(ctx context.Context, tenant, actor, board, card string) (MirrorInfo, error) {
	c, b, err := o.GetCard(ctx, tenant, actor, board, card)
	if err != nil {
		return MirrorInfo{}, err
	}
	if c.Mirror == nil {
		return MirrorInfo{}, invalid("#%d is not a mirror", c.No)
	}
	if !c.Archived {
		_ = o.syncMirrors(ctx, tenant, b, c.ID)
		if c, _, err = o.GetCard(ctx, tenant, actor, board, card); err != nil {
			return MirrorInfo{}, err
		}
	}
	snap, _, _ := docs.Get[boardMirrorDoc](ctx, o.Docs, tenant, cBoardMirror, mirrorKey(*c.Mirror, b.ID, c.ID))
	sb, _, err := o.boardFor(ctx, tenant, actor, c.Mirror.Board)
	if err != nil {
		return MirrorInfo{Hidden: snap.Hidden, Why: "You cannot see the original card's board."}, nil
	}
	src, _, err := docs.Get[Card](ctx, o.Docs, tenant, cBoardCard, cardKey(sb.ID, c.Mirror.Card))
	if err != nil || src.Deleted {
		return MirrorInfo{Visible: true, Gone: true, Hidden: snap.Hidden, Board: sb.ID, BoardName: sb.Name}, nil
	}
	l, _ := boardList(sb, src.List)
	out := MirrorInfo{Visible: true, Hidden: snap.Hidden, Board: sb.ID, BoardName: sb.Name, Card: src.ID, No: src.No, List: l.Name, Title: src.Title, Description: src.Description,
		Due: src.Due, DueDone: src.DueDone, Archived: src.Archived}
	if err := o.mirrorEditAllowed(ctx, tenant, actor, *c.Mirror); err != nil {
		out.Why = strings.TrimPrefix(strings.TrimPrefix(err.Error(), ErrForbidden.Error()+": "), ErrState.Error()+": ")
	} else {
		out.CanEdit = true
	}
	return out, nil
}

// MirrorEdit changes a mirror's original.
type MirrorEdit struct {
	Title       *string `json:"title,omitempty"`
	Description *string `json:"description,omitempty"`
	Due         *string `json:"due,omitempty"`
	DueDone     *bool   `json:"due_done,omitempty"`
}

// EditMirrorSource changes the original of a mirror, with the person's
// rights on the original's board, and brings its mirrors up to date.
func (o *Ops) EditMirrorSource(ctx context.Context, tenant, actor, board, card string, p MirrorEdit) (MirrorInfo, error) {
	c, b, err := o.GetCard(ctx, tenant, actor, board, card)
	if err != nil {
		return MirrorInfo{}, err
	}
	if c.Mirror == nil {
		return MirrorInfo{}, invalid("#%d is not a mirror", c.No)
	}
	_ = o.syncMirrors(ctx, tenant, b, c.ID) // follow an original that moved
	if c, _, err = o.GetCard(ctx, tenant, actor, board, card); err != nil {
		return MirrorInfo{}, err
	}
	if err := o.mirrorEditAllowed(ctx, tenant, actor, *c.Mirror); err != nil {
		return MirrorInfo{}, err
	}
	if _, err := o.UpdateCard(ctx, tenant, actor, c.Mirror.Board, c.Mirror.Card, CardPatch{Title: p.Title, Description: p.Description, Due: p.Due, DueDone: p.DueDone}); err != nil {
		return MirrorInfo{}, err
	}
	o.syncMirrorsOf(ctx, tenant, *c.Mirror)
	return o.MirrorSource(ctx, tenant, actor, board, card)
}

// ---- email-to-board ----

// BoardEmailInfo is a board's email-to-board settings, for its admins.
type BoardEmailInfo struct {
	On    bool        `json:"on"`
	Path  string      `json:"path,omitempty"` // where the mail provider posts: /inbound/boards/<tenant>/<key>
	List  string      `json:"list"`
	Lists []BoardList `json:"lists"`
}

// BoardEmailInput switches email-to-board on or off, chooses the list new
// cards go to, or gives the board a new address.
type BoardEmailInput struct {
	On     bool   `json:"on"`
	List   string `json:"list"`
	Rotate bool   `json:"rotate"`
}

func boardEmailInfo(tenant string, b Board) BoardEmailInfo {
	in := BoardEmailInfo{On: b.EmailKey != "", List: b.EmailList, Lists: []BoardList{}}
	if in.On {
		in.Path = "/inbound/boards/" + url.PathEscape(tenant) + "/" + b.EmailKey
	}
	for _, l := range b.Lists {
		if !l.Archived {
			in.Lists = append(in.Lists, l)
		}
	}
	sort.SliceStable(in.Lists, func(i, j int) bool { return in.Lists[i].Pos < in.Lists[j].Pos })
	if l, ok := boardList(b, in.List); (!ok || l.Archived) && len(in.Lists) > 0 {
		in.List = in.Lists[0].ID
	}
	return in
}

// BoardEmail is a board's email-to-board settings (board admins).
func (o *Ops) BoardEmail(ctx context.Context, tenant, actor, id string) (BoardEmailInfo, error) {
	b, _, err := o.boardFor(ctx, tenant, actor, id, BoardAdmin)
	if err != nil {
		return BoardEmailInfo{}, err
	}
	if b.Kind != BoardTeam {
		return BoardEmailInfo{}, invalid("email-to-board is for team boards")
	}
	return boardEmailInfo(tenant, b), nil
}

// SetBoardEmail changes a board's email-to-board settings (board admins).
func (o *Ops) SetBoardEmail(ctx context.Context, tenant, actor, id string, in BoardEmailInput) (BoardEmailInfo, error) {
	b, _, err := o.boardFor(ctx, tenant, actor, id, BoardAdmin)
	if err != nil {
		return BoardEmailInfo{}, err
	}
	if b.Kind != BoardTeam {
		return BoardEmailInfo{}, invalid("email-to-board is for team boards")
	}
	var what []string
	b, err = docs.Update(ctx, o.Docs, tenant, cBoard, id, actor, func(x *Board) error {
		what = nil
		if in.List != "" && in.List != x.EmailList {
			l, ok := boardList(*x, in.List)
			if !ok || l.Archived {
				return invalid("choose an open list for the cards made from email")
			}
			x.EmailList = in.List
			what = append(what, "cards made from email now go to "+l.Name)
		}
		switch {
		case !in.On && x.EmailKey != "":
			x.EmailKey = ""
			what = append(what, "switched off email-to-board")
		case in.On && x.EmailKey == "":
			x.EmailKey = boardID("", 32)
			what = append(what, "switched on email-to-board")
		case in.On && in.Rotate:
			x.EmailKey = boardID("", 32)
			what = append(what, "gave the board a new email address; the old one no longer works")
		}
		if l, ok := boardList(*x, x.EmailList); x.EmailKey != "" && (!ok || l.Archived) {
			info := boardEmailInfo(tenant, *x)
			if len(info.Lists) == 0 {
				return invalid("the board needs an open list for the cards made from email")
			}
			x.EmailList = info.Lists[0].ID
		}
		x.UpdatedAt = o.now().Format(time.RFC3339)
		return nil
	})
	if err != nil {
		return BoardEmailInfo{}, err
	}
	for _, w := range what {
		o.boardLog(ctx, tenant, actor, b, "", "board.email", w, nil)
		o.log(ctx, tenant, actor, "board.email", b.ID, map[string]any{"change": w})
	}
	return boardEmailInfo(tenant, b), nil
}

func (o *Ops) boardByEmailKey(ctx context.Context, tenant, key string) (Board, bool) {
	if len(key) < 32 || len(key) > 128 {
		return Board{}, false
	}
	bs, err := docs.List[Board](ctx, o.Docs, tenant, cBoard, "")
	if err != nil {
		return Board{}, false
	}
	var found Board
	ok := false
	for _, b := range bs {
		if b.EmailKey != "" && !b.Deleted && subtle.ConstantTimeCompare([]byte(b.EmailKey), []byte(key)) == 1 {
			found, ok = b, true
		}
	}
	return found, ok
}

// BoardEmailKnown reports whether a key is a board's email address, or a
// card's on a board that takes mail.
func (o *Ops) BoardEmailKnown(ctx context.Context, tenant, key string) bool {
	if cardMailKeyRe.MatchString(key) {
		_, _, ok := o.cardByEmailKey(ctx, tenant, key)
		return ok
	}
	_, ok := o.boardByEmailKey(ctx, tenant, key)
	return ok
}

type boardMailDoc struct {
	Board string `json:"board"`
	Card  string `json:"card"`
	From  string `json:"from"`
	At    string `json:"at"`
}

// BoardInboundMail makes a card from an email (a raw RFC 822 message) sent
// to a board's address: the subject is the title, the text the
// description, the attachments the card's attachments. dup is true when
// the same message (by its Message-ID) already made a card. Mail to a
// card's address is a comment on that card instead (cardInboundMail).
func (o *Ops) BoardInboundMail(ctx context.Context, tenant, key string, raw []byte) (c Card, dup bool, err error) {
	ctx = ingest.WithActor(ctx, mailActor) // the malware scanner's audit entries
	if cardMailKeyRe.MatchString(key) {    // a card's address: a comment on it
		return o.cardInboundMail(ctx, tenant, key, raw)
	}
	b, ok := o.boardByEmailKey(ctx, tenant, key)
	if !ok {
		return Card{}, false, fmt.Errorf("%w: no board takes mail at this address", ErrNotFound)
	}
	if b.Closed {
		return Card{}, false, fmt.Errorf("%w: the board %s is closed and takes no mail", ErrState, b.Name)
	}
	if b.Kind != BoardTeam {
		return Card{}, false, invalid("email-to-board is for team boards")
	}
	if len(raw) == 0 || len(raw) > BoardMaxMail {
		return Card{}, false, invalid("an email can be up to %d MB", BoardMaxMail>>20)
	}
	pm, err := parseBoardMail(raw)
	if err != nil {
		return Card{}, false, err
	}
	var seen string
	if pm.MessageID != "" {
		sum := sha256.Sum256([]byte(pm.MessageID))
		seen = b.ID + "|" + hex.EncodeToString(sum[:16])
		if d, _, err := docs.Get[boardMailDoc](ctx, o.Docs, tenant, cBoardMail, seen); err == nil {
			if c, _, err := docs.Get[Card](ctx, o.Docs, tenant, cBoardCard, cardKey(b.ID, d.Card)); err == nil && !c.Deleted {
				return c, true, nil
			}
		}
	}
	info := boardEmailInfo(tenant, b)
	if len(info.Lists) == 0 {
		return Card{}, false, invalid("the board %s has no open list for new cards", b.Name)
	}
	if len(pm.Files) > 0 { // attachments need the malware scanner: refused while it is down, so the mail is sent again
		if err := o.scannerReady(ctx); err != nil {
			return Card{}, false, err
		}
	}
	sender := firstOf(pm.From, "an unknown sender")
	from := sender
	if pm.FromName != "" && pm.From != "" {
		from = pm.FromName + " <" + pm.From + ">"
	}
	desc := "From: " + from
	if t := strings.TrimSpace(pm.Text); t != "" {
		desc += "\n\n" + clip(t, 18000)
	}
	title := clip(firstOf(pm.Subject, "Email from "+sender), 480)
	c, err = o.addCard(ctx, tenant, mailActor, b, info.List, Card{Title: title, Description: desc}, math.NaN())
	if err != nil {
		return Card{}, false, err
	}
	attached := 0
	var skipped []string
	for i, f := range pm.Files {
		if i >= 25 {
			skipped = append(skipped, fmt.Sprintf("%d more files (25 at most from one email)", len(pm.Files)-i))
			break
		}
		nc, err := o.AttachFile(ctx, tenant, mailActor, b.ID, c.ID, f.Name, f.Data)
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("%s (%s)", clip(firstOf(f.Name, "a file with no name"), 80), strings.TrimPrefix(err.Error(), "invalid: ")))
			continue
		}
		c = nc
		attached++
	}
	if len(skipped) > 0 {
		if nc, err := docs.Update(ctx, o.Docs, tenant, cBoardCard, cardKey(b.ID, c.ID), mailActor, func(x *Card) error {
			x.Description = clip(x.Description+"\n\nNot attached: "+strings.Join(skipped, "; "), 19900)
			return nil
		}); err == nil {
			c = nc
		}
	}
	now := o.now().Format(time.RFC3339)
	if seen != "" {
		_ = docs.Upsert(ctx, o.Docs, tenant, cBoardMail, seen, boardMailDoc{Board: b.ID, Card: c.ID, From: pm.From, At: now}, mailActor)
	}
	text := fmt.Sprintf("received an email from %s as #%d %s", sender, c.No, clip(title, 80))
	if attached > 0 {
		text += fmt.Sprintf(" (%d attachment%s)", attached, map[bool]string{true: "", false: "s"}[attached == 1])
	}
	o.boardLog(ctx, tenant, mailActor, b, c.ID, "email.received", text, map[string]string{"from": pm.From, "attached": fmt.Sprint(attached), "not_attached": fmt.Sprint(len(skipped))})
	o.log(ctx, tenant, mailActor, "board.email_received", b.ID, map[string]any{"card": c.ID, "from": pm.From, "attached": attached, "not_attached": len(skipped)})
	return c, false, nil
}

// ---- email to a card ----
//
// While a team board has email-to-board on, each of its cards has an
// address too (Trello's "email for this card"). Mail to it goes to the
// same inbound route as the board's; it adds a comment with the sender and
// the text, and attaches the files that pass the checks of an upload. A
// card's key is its number and board, signed with the board's own key, so
// it shows nothing of the board's address and changes with it.

var cardMailKeyRe = regexp.MustCompile(`^c([1-9][0-9]{0,8})-([A-Za-z0-9_-]{1,64})-([0-9a-f]{32})$`)

// cardMailKey is a card's inbound key ("" when the board takes no mail).
func cardMailKey(b Board, c Card) string {
	if b.EmailKey == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(b.EmailKey))
	mac.Write([]byte("card|" + b.ID + "|" + c.ID))
	return fmt.Sprintf("c%d-%s-%s", c.No, b.ID, hex.EncodeToString(mac.Sum(nil)[:16]))
}

// cardByEmailKey finds the card whose address a key is.
func (o *Ops) cardByEmailKey(ctx context.Context, tenant, key string) (Board, Card, bool) {
	m := cardMailKeyRe.FindStringSubmatch(key)
	if m == nil {
		return Board{}, Card{}, false
	}
	b, _, err := docs.Get[Board](ctx, o.Docs, tenant, cBoard, m[2])
	if err != nil || b.Deleted || b.EmailKey == "" {
		return Board{}, Card{}, false
	}
	cs, err := o.boardCards(ctx, tenant, b.ID)
	if err != nil {
		return Board{}, Card{}, false
	}
	for _, c := range cs {
		if fmt.Sprint(c.No) == m[1] && subtle.ConstantTimeCompare([]byte(cardMailKey(b, c)), []byte(key)) == 1 {
			return b, c, true
		}
	}
	return Board{}, Card{}, false
}

// CardEmailInfo is a card's inbound address, for the board's members.
type CardEmailInfo struct {
	On   bool   `json:"on"`
	Path string `json:"path,omitempty"` // /inbound/boards/<tenant>/<card key>
	Why  string `json:"why,omitempty"`  // why the card takes no mail
}

// CardEmail is a card's inbound address (members and admins of the board).
func (o *Ops) CardEmail(ctx context.Context, tenant, actor, board, card string) (CardEmailInfo, error) {
	c, b, err := o.GetCard(ctx, tenant, actor, board, card)
	if err != nil {
		return CardEmailInfo{}, err
	}
	_, v, _ := o.boardFor(ctx, tenant, actor, board)
	if !v.can(BoardAdmin, BoardMember) {
		return CardEmailInfo{}, fmt.Errorf("%w: only the board's members see a card's email address (your role on it: %s)", ErrForbidden, v.board)
	}
	switch {
	case b.Kind == BoardInbox:
		return CardEmailInfo{Why: "Cards in your Inbox take no email. Move the card to a team board to give it an address."}, nil
	case b.Kind != BoardTeam:
		return CardEmailInfo{Why: "Cards on Ledgerline boards take no email."}, nil
	case b.EmailKey == "":
		return CardEmailInfo{Why: "Email-to-board is off on this board. When a board admin switches it on (board menu, Email-to-board), each card has its own address too."}, nil
	case b.Closed:
		return CardEmailInfo{Why: "The board is closed and takes no email."}, nil
	case c.Archived:
		return CardEmailInfo{Why: "This card is archived and takes no email. Send it to the board to use its address."}, nil
	}
	return CardEmailInfo{On: true, Path: "/inbound/boards/" + url.PathEscape(tenant) + "/" + cardMailKey(b, c)}, nil
}

// cardInboundMail adds an email sent to a card's address as a comment, by
// Ledgerline's email actor, with the files attached.
func (o *Ops) cardInboundMail(ctx context.Context, tenant, key string, raw []byte) (Card, bool, error) {
	b, c, ok := o.cardByEmailKey(ctx, tenant, key)
	if !ok {
		return Card{}, false, fmt.Errorf("%w: no card takes mail at this address", ErrNotFound)
	}
	if b.Kind != BoardTeam { // Ledgerline boards and Inboxes take no mail
		return Card{}, false, invalid("email to a card is for team boards; Ledgerline boards and Inboxes take no email")
	}
	if b.Closed {
		return Card{}, false, fmt.Errorf("%w: the board %s is closed and takes no mail", ErrState, b.Name)
	}
	if c.Archived {
		return Card{}, false, fmt.Errorf("%w: the card #%d is archived and takes no mail", ErrState, c.No)
	}
	if len(raw) == 0 || len(raw) > BoardMaxMail {
		return Card{}, false, invalid("an email can be up to %d MB", BoardMaxMail>>20)
	}
	pm, err := parseBoardMail(raw)
	if err != nil {
		return Card{}, false, err
	}
	var seen string
	if pm.MessageID != "" {
		sum := sha256.Sum256([]byte(pm.MessageID + "|card|" + c.ID))
		seen = b.ID + "|" + hex.EncodeToString(sum[:16])
		if d, _, err := docs.Get[boardMailDoc](ctx, o.Docs, tenant, cBoardMail, seen); err == nil && d.Card == c.ID {
			if x, _, err := docs.Get[Card](ctx, o.Docs, tenant, cBoardCard, cardKey(b.ID, c.ID)); err == nil && !x.Deleted {
				return x, true, nil
			}
		}
	}
	if len(pm.Files) > 0 {
		if err := o.scannerReady(ctx); err != nil {
			return Card{}, false, err
		}
	}
	sender := firstOf(pm.From, "an unknown sender")
	from := sender
	if pm.FromName != "" && pm.From != "" {
		from = pm.FromName + " (" + pm.From + ")"
	}
	var names, skipped []string
	for i, f := range pm.Files {
		if i >= 25 {
			skipped = append(skipped, fmt.Sprintf("%d more files (25 at most from one email)", len(pm.Files)-i))
			break
		}
		nc, err := o.AttachFile(ctx, tenant, mailActor, b.ID, c.ID, f.Name, f.Data)
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("%s (%s)", clip(firstOf(f.Name, "a file with no name"), 80), strings.TrimPrefix(err.Error(), "invalid: ")))
			continue
		}
		c = nc
		names = append(names, f.Name)
	}
	head := "Email from " + from
	if pm.Subject != "" {
		head += ": " + pm.Subject
	}
	var foot []string
	if len(names) > 0 {
		foot = append(foot, "Attached: "+strings.Join(names, ", "))
	}
	if len(skipped) > 0 {
		foot = append(foot, "Not attached: "+strings.Join(skipped, "; "))
	}
	tail := ""
	if len(foot) > 0 {
		tail = clip("\n\n"+strings.Join(foot, "\n"), 1200)
	}
	body := strings.TrimSpace(pm.Text)
	if body == "" {
		body = "(The email had no text.)"
	}
	text := clip(head, 600) + "\n\n" + clip(body, 4990-utf8.RuneCountInString(clip(head, 600))-utf8.RuneCountInString(tail)-2) + tail
	if c, err = o.AddComment(ctx, tenant, mailActor, b.ID, c.ID, text); err != nil {
		return Card{}, false, err
	}
	if seen != "" {
		_ = docs.Upsert(ctx, o.Docs, tenant, cBoardMail, seen, boardMailDoc{Board: b.ID, Card: c.ID, From: pm.From, At: o.now().Format(time.RFC3339)}, mailActor)
	}
	note := fmt.Sprintf("received an email from %s on #%d", sender, c.No)
	if len(names) > 0 {
		note += fmt.Sprintf(" (%d attachment%s)", len(names), map[bool]string{true: "", false: "s"}[len(names) == 1])
	}
	o.boardLog(ctx, tenant, mailActor, b, c.ID, "email.received", note, map[string]string{"from": pm.From, "card_mail": "1", "attached": fmt.Sprint(len(names)), "not_attached": fmt.Sprint(len(skipped))})
	o.log(ctx, tenant, mailActor, "board.card_email_received", b.ID, map[string]any{"card": c.ID, "from": pm.From, "attached": len(names), "not_attached": len(skipped)})
	return c, false, nil
}

// ---- stickers ----

// BoardStickers are the stickers a card can carry; web/boardextras.js
// draws them.
var BoardStickers = []string{"tick", "star", "flag", "warning", "fire", "rocket", "thumbsup", "heart", "clock", "pin", "rupee", "chartup",
	"chartdown", "bell", "lock", "bug", "bulb", "coffee", "trophy", "eyes", "hundred", "party", "question", "stop"}

// MaxCardStickers is the most stickers one card carries.
const MaxCardStickers = 10

// StickerOp adds a sticker to a card or takes one off (by its place).
type StickerOp struct {
	Add    string `json:"add,omitempty"`
	Remove *int   `json:"remove,omitempty"`
}

// CardSticker adds or removes a sticker (members and admins of the board).
func (o *Ops) CardSticker(ctx context.Context, tenant, actor, board, card string, op StickerOp) (Card, error) {
	b, _, err := o.boardFor(ctx, tenant, actor, board, BoardAdmin, BoardMember)
	if err != nil {
		return Card{}, err
	}
	if (op.Add == "") == (op.Remove == nil) {
		return Card{}, invalid("add one sticker or remove one")
	}
	if op.Add != "" && !contains(BoardStickers, op.Add) {
		return Card{}, invalid("unknown sticker %q", op.Add)
	}
	var what string
	c, err := docs.Update(ctx, o.Docs, tenant, cBoardCard, cardKey(board, card), actor, func(x *Card) error {
		if x.Deleted {
			return errCardGone
		}
		if op.Add != "" {
			if len(x.Stickers) >= MaxCardStickers {
				return invalid("a card can have at most %d stickers; remove one first", MaxCardStickers)
			}
			x.Stickers = append(x.Stickers, op.Add)
			what = fmt.Sprintf("added a %s sticker to #%d", op.Add, x.No)
		} else {
			i := *op.Remove
			if i < 0 || i >= len(x.Stickers) {
				return invalid("no such sticker on the card")
			}
			what = fmt.Sprintf("removed a %s sticker from #%d", x.Stickers[i], x.No)
			x.Stickers = append(x.Stickers[:i:i], x.Stickers[i+1:]...)
		}
		x.UpdatedAt = o.now().Format(time.RFC3339)
		return nil
	})
	if err != nil {
		return c, err
	}
	o.boardLog(ctx, tenant, actor, b, card, "card.sticker", what, nil)
	return c, nil
}

// ---- reading an email ----

type boardMail struct {
	From, FromName, Subject, MessageID string
	Text                               string
	Files                              []boardMailFile
}

type boardMailFile struct {
	Name string
	Data []byte
}

func mailCharset(charset string, in io.Reader) (io.Reader, error) {
	b, err := io.ReadAll(in)
	if err != nil {
		return nil, err
	}
	return strings.NewReader(mailText(b, charset)), nil
}

// mailText is text in a charset as UTF-8: UTF-8 and ASCII as they are,
// Latin-1 and Windows-1252 byte by byte; anything else is kept where it is
// valid UTF-8.
func mailText(b []byte, charset string) string {
	switch strings.ToLower(strings.TrimSpace(charset)) {
	case "iso-8859-1", "latin1", "latin-1", "windows-1252", "cp1252", "iso-8859-15":
		r := make([]rune, len(b))
		for i, c := range b {
			r[i] = rune(c)
		}
		return string(r)
	}
	return strings.ToValidUTF8(string(b), "�")
}

var mailWords = &mime.WordDecoder{CharsetReader: mailCharset}

func mailHeader(s string) string {
	d, err := mailWords.DecodeHeader(s)
	if err != nil {
		d = s
	}
	return strings.Join(strings.Fields(d), " ")
}

func parseBoardMail(raw []byte) (boardMail, error) {
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return boardMail{}, invalid("this is not an email message (RFC 822): %v", err)
	}
	pm := boardMail{Subject: mailHeader(m.Header.Get("Subject")), MessageID: strings.TrimSpace(m.Header.Get("Message-Id"))}
	if a, err := (&mail.AddressParser{WordDecoder: mailWords}).Parse(m.Header.Get("From")); err == nil {
		pm.From, pm.FromName = strings.ToLower(a.Address), strings.Join(strings.Fields(a.Name), " ")
	} else {
		pm.From = clip(mailHeader(m.Header.Get("From")), 200)
	}
	var plain, htm string
	if err := walkMail(textproto.MIMEHeader(m.Header), m.Body, 0, &plain, &htm, &pm.Files); err != nil && plain == "" && htm == "" && len(pm.Files) == 0 {
		return boardMail{}, invalid("the email could not be read: %v", err)
	}
	pm.Text = tidyMailText(plain)
	if pm.Text == "" && htm != "" {
		pm.Text = tidyMailText(htmlText(htm))
	}
	return pm, nil
}

func transferDecoder(cte string, r io.Reader) io.Reader {
	switch strings.ToLower(strings.TrimSpace(cte)) {
	case "base64":
		return base64.NewDecoder(base64.StdEncoding, r)
	case "quoted-printable":
		return quotedprintable.NewReader(r)
	}
	return r
}

func walkMail(h textproto.MIMEHeader, body io.Reader, depth int, plain, htm *string, files *[]boardMailFile) error {
	mt, params, err := mime.ParseMediaType(firstOf(h.Get("Content-Type"), "text/plain"))
	if err != nil {
		mt, params = "text/plain", map[string]string{}
	}
	if strings.HasPrefix(mt, "multipart/") {
		if depth > 8 || params["boundary"] == "" {
			return nil
		}
		mr := multipart.NewReader(body, params["boundary"])
		for {
			p, err := mr.NextPart()
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return err
			}
			if err := walkMail(p.Header, p, depth+1, plain, htm, files); err != nil {
				return err
			}
		}
	}
	data, err := io.ReadAll(io.LimitReader(transferDecoder(h.Get("Content-Transfer-Encoding"), body), BoardMaxMail))
	if err != nil {
		return err
	}
	disp, dp, _ := mime.ParseMediaType(h.Get("Content-Disposition"))
	name := mailHeader(firstOf(dp["filename"], params["name"]))
	if disp == "attachment" || (name != "" && mt != "text/plain" && mt != "text/html") {
		if name == "" {
			if exts, _ := mime.ExtensionsByType(mt); len(exts) > 0 {
				name = "attachment" + exts[0]
			}
		}
		*files = append(*files, boardMailFile{Name: name, Data: data})
		return nil
	}
	switch mt {
	case "text/plain":
		if *plain == "" {
			*plain = mailText(data, params["charset"])
		}
	case "text/html":
		if *htm == "" {
			*htm = mailText(data, params["charset"])
		}
	}
	return nil
}

var (
	mailDropRe  = regexp.MustCompile(`(?is)<(script|style|head|title)\b[^>]*>.*?</(script|style|head|title)\s*>`)
	mailBreakRe = regexp.MustCompile(`(?i)<\s*(br|/p|/div|/li|/tr|/h[1-6]|/table|/blockquote)\b[^>]*>`)
	mailItemRe  = regexp.MustCompile(`(?i)<\s*li\b[^>]*>`)
	mailTagRe   = regexp.MustCompile(`(?s)<[^>]*>`)
	mailGapRe   = regexp.MustCompile(`\n{3,}`)
)

// htmlText is an HTML email's text: the tags taken out, the line breaks
// kept.
func htmlText(s string) string {
	s = mailDropRe.ReplaceAllString(s, "")
	s = mailBreakRe.ReplaceAllString(s, "\n")
	s = mailItemRe.ReplaceAllString(s, "\n- ")
	s = mailTagRe.ReplaceAllString(s, "")
	return html.UnescapeString(s)
}

func tidyMailText(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(strings.ReplaceAll(l, " ", " "), " \t")
	}
	s = mailGapRe.ReplaceAllString(strings.Join(lines, "\n"), "\n\n")
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "�")
	}
	return strings.TrimSpace(s)
}
