package ops

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/pdf"
)

// ---- the dispatch register ----
//
// Every document Ledgerline sends to a client (contract notes, margin,
// running-account, DP and ledger statements) is made as a PDF, signed with
// the firm's certificate, kept exactly as sent, and recorded here: what,
// to whom, when, the file's fingerprint and signature, and what happened
// (sent, bounced, held). This is the "log of dispatch" SEBI's electronic
// contract note rules ask brokers to keep as proof of delivery. Bounced
// mail and clients who chose paper raise a work item to send a physical
// copy, which is recorded here with its courier reference.

const (
	cDispatch = "dispatch"
	srcDispatch = "DISPATCH"
)

// Kinds of documents.
const (
	DocContractNote = "contract_note"
	DocMargin       = "margin_statement"
	DocRAS          = "ras_statement"
	DocDP           = "dp_statement"
	DocLedger       = "ledger_statement"
)

// DocKindNames name the kinds for people.
var DocKindNames = map[string]string{
	DocContractNote: "Contract note", DocMargin: "Daily margin statement", DocRAS: "Running account settlement statement",
	DocDP: "DP transaction and holding statement", DocLedger: "Statement of account",
}

// DispatchFile is a document as sent.
type DispatchFile struct {
	Name     string `json:"name"`
	RawID    string `json:"raw_id"`
	SHA256   string `json:"sha256"`
	Bytes    int    `json:"bytes"`
	Signed   bool   `json:"signed"`
	Signer   string `json:"signer,omitempty"`
	Serial   string `json:"cert_serial,omitempty"`
	Issuer   string `json:"cert_issuer,omitempty"`
	SignedAt string `json:"signed_at,omitempty"`
}

// Dispatch is one document sent (or held) to one recipient.
type Dispatch struct {
	ID        string         `json:"id"`
	Key       string         `json:"key"` // kind|ref|ucc: the same document's attempts share it
	Kind      string         `json:"kind"`
	Ref       string         `json:"ref"`
	Date      string         `json:"date"` // the document's date (trade date, period end)
	UCC       string         `json:"ucc"`
	Name      string         `json:"name"`
	Channel   string         `json:"channel"` // email, physical
	To        string         `json:"to"`
	Subject   string         `json:"subject"`
	Files     []DispatchFile `json:"files"`
	Status    string         `json:"status"` // Sent, Bounced, Failed, Held, Physical pending, Physical sent
	Reason    string         `json:"reason,omitempty"`
	MessageID string         `json:"message_id,omitempty"`
	Simulated bool           `json:"simulated,omitempty"` // development: no real mail server
	Attempt   int            `json:"attempt"`
	Created   string         `json:"created"`
	SentAt    string         `json:"sent_at,omitempty"`
	BouncedAt string         `json:"bounced_at,omitempty"`
	Courier   string         `json:"courier,omitempty"` // physical copy: courier and reference
	CourierAt string         `json:"courier_at,omitempty"`
	By        string         `json:"by"`
}

// docRequest is what a sender wants dispatched.
type docRequest struct {
	Kind, Ref, Date, UCC string
	Report               pdf.Report
	Title, FileName      string
	Text                 string // email body
}

func (o *Ops) mailFrom(set Settings) string {
	if m, ok := o.Mailer.(*SMTPMailer); ok {
		return m.From
	}
	return firstOf(set.ComplianceEmail, "noreply@ledgerline.invalid")
}

// dispatchDoc signs, keeps and sends one document, and records it.
func (o *Ops) dispatchDoc(ctx context.Context, tenant, actor string, r docRequest) (Dispatch, error) {
	c, err := o.Client(ctx, tenant, r.UCC)
	if err != nil {
		return Dispatch{}, err
	}
	set, _ := o.Settings(ctx, tenant)
	key := r.Kind + "|" + r.Ref + "|" + c.UCC
	prev, _ := o.dispatchesFor(ctx, tenant, key)
	n, err := o.next(ctx, tenant, "dispatch", actor)
	if err != nil {
		return Dispatch{}, err
	}
	now := o.now()
	d := Dispatch{ID: fmt.Sprintf("DSP-%07d", n), Key: key, Kind: r.Kind, Ref: r.Ref, Date: r.Date, UCC: c.UCC, Name: c.Name, Channel: "email", To: c.Email,
		Subject: fmt.Sprintf("%s %s · %s", DocKindNames[r.Kind], r.Ref, set.Firm), Attempt: len(prev) + 1, Created: now.Format(time.RFC3339), By: actor, Files: []DispatchFile{}}
	if c.PhysicalNotes {
		d.Channel = "physical"
	}
	save := func() (Dispatch, error) {
		return d, docs.Upsert(ctx, o.Docs, tenant, cDispatch, d.ID, d, actor)
	}
	data, info, err := o.signPDF(ctx, tenant, r.Report, r.Title, DocKindNames[r.Kind])
	if err != nil {
		d.Status, d.Reason = "Held", "not signed: "+err.Error()
		o.raiseWork(ctx, tenant, "dispatch", "unsigned", "Documents are held: no digital signature",
			"Contract notes and statements cannot be sent unsigned. Set up the firm's certificate in Settings → Digital signature, then send the held documents from the Dispatch register.", "", RoleAdmin, "dispatch", r.Date)
		return save()
	}
	sum := sha256Hex(data)
	rf, err := o.App.Ingest.Raw.Put(ctx, ingest.RawFile{Tenant: tenant, Source: srcDispatch, Name: d.ID + " " + r.FileName, SHA256: sum, Content: data})
	if err != nil {
		return d, err
	}
	d.Files = append(d.Files, DispatchFile{Name: r.FileName, RawID: rf.ID, SHA256: sum, Bytes: len(data), Signed: true, Signer: info.Name, Serial: info.Serial, Issuer: info.Issuer, SignedAt: now.Format(time.RFC3339)})
	if d.Channel == "physical" {
		d.Status, d.Reason = "Physical pending", "client chose paper contract notes and statements"
		o.raiseWork(ctx, tenant, "dispatch", d.ID, "Send a printed copy to "+c.Name, fmt.Sprintf("%s %s: print the signed PDF from the Dispatch register and send it by courier; record the courier reference.", DocKindNames[r.Kind], r.Ref), c.UCC, RoleMaker, "dispatch:"+d.ID, r.Date)
		return save()
	}
	if !reEmail.MatchString(c.Email) {
		d.Status, d.Reason = "Bounced", "no valid email address"
		d.BouncedAt = now.Format(time.RFC3339)
		o.bounceWork(ctx, tenant, d)
		return save()
	}
	mail := Mail{From: o.mailFrom(set), To: c.Email, Subject: d.Subject, Text: r.Text + "\n\n" + set.Firm + "\nThe attached PDF is digitally signed. If you did not expect this message, write to " + firstOf(set.ComplianceEmail, "the firm") + ".",
		Attach: []MailFile{{Name: r.FileName, Type: "application/pdf", Data: data}}}
	switch {
	case o.Mailer != nil:
		id, err := o.Mailer.Send(ctx, mail)
		var rej *Rejected
		switch {
		case err == nil:
			d.Status, d.MessageID, d.SentAt = "Sent", id, o.now().Format(time.RFC3339)
		case errors.As(err, &rej):
			d.Status, d.Reason, d.BouncedAt = "Bounced", rej.Reason, o.now().Format(time.RFC3339)
			o.bounceWork(ctx, tenant, d)
		default:
			d.Status, d.Reason = "Failed", err.Error()
		}
	case o.DevCodes || o.devFirm(ctx, tenant):
		// Development: no mail server; reserved test domains bounce as a real relay would.
		d.Simulated = true
		if strings.HasSuffix(c.Email, ".invalid") {
			d.Status, d.Reason, d.BouncedAt = "Bounced", "550 mailbox does not exist (simulated)", now.Format(time.RFC3339)
			o.bounceWork(ctx, tenant, d)
		} else {
			d.Status, d.SentAt, d.MessageID = "Sent", now.Format(time.RFC3339), newMessageID("ledgerline.invalid")
		}
	default:
		d.Status, d.Reason = "Held", "no mail server is set up (LEDGERLINE_SMTP_URL)"
	}
	return save()
}

func (o *Ops) bounceWork(ctx context.Context, tenant string, d Dispatch) {
	o.raiseWork(ctx, tenant, "dispatch", d.ID, "Bounced: send "+d.Name+" a printed copy",
		fmt.Sprintf("%s %s could not be delivered to %s (%s). SEBI requires a physical copy when an electronic one is not delivered. Print the signed PDF and record the courier reference; update the client's email.", DocKindNames[d.Kind], d.Ref, d.To, d.Reason),
		d.UCC, RoleMaker, "dispatch:"+d.ID, d.Date)
}

// raiseWork opens (or refreshes) a work item outside an Autopilot run.
func (o *Ops) raiseWork(ctx context.Context, tenant, process, key, title, detail, ucc, role, view, date string) {
	rc := &runCtx{o: o, ctx: ctx, tenant: tenant, date: firstOf(date, o.now().Format("2006-01-02")), process: process, keys: map[string]bool{}}
	rc.queue(key, title, detail, ucc, role, view, "", nil)
}

func (o *Ops) dispatchesFor(ctx context.Context, tenant, key string) ([]Dispatch, error) {
	all, err := docs.List[Dispatch](ctx, o.Docs, tenant, cDispatch, "")
	var out []Dispatch
	for _, d := range all {
		if d.Key == key {
			out = append(out, d)
		}
	}
	return out, err
}

// DispatchFilter narrows the register.
type DispatchFilter struct {
	From, To, Kind, Status, UCC string
}

// DispatchView is the register page.
type DispatchView struct {
	Rows     []Dispatch          `json:"rows"`
	ByStatus map[string]int      `json:"by_status"`
	ByKind   map[string]int      `json:"by_kind"`
	Kinds    map[string]string   `json:"kinds"`
	Signing  SigningView         `json:"signing"`
	Mail     string              `json:"mail"` // smtp, simulated, none
}

func (o *Ops) canSeeDispatch(ctx context.Context, tenant, actor string) error {
	return o.require(ctx, tenant, actor, "Viewing the dispatch register", RoleMaker, RoleChecker, RoleCompliance, RoleAdmin, RoleRisk)
}

// DispatchRegister lists dispatches, newest first.
func (o *Ops) DispatchRegister(ctx context.Context, tenant, actor string, f DispatchFilter) (DispatchView, error) {
	if err := o.canSeeDispatch(ctx, tenant, actor); err != nil {
		return DispatchView{}, err
	}
	all, err := docs.List[Dispatch](ctx, o.Docs, tenant, cDispatch, "")
	if err != nil {
		return DispatchView{}, err
	}
	v := DispatchView{Rows: []Dispatch{}, ByStatus: map[string]int{}, ByKind: map[string]int{}, Kinds: DocKindNames}
	f.UCC = strings.ToUpper(strings.TrimSpace(f.UCC))
	for _, d := range all {
		day := d.Created[:10]
		if (f.From != "" && day < f.From) || (f.To != "" && day > f.To) || (f.Kind != "" && d.Kind != f.Kind) || (f.UCC != "" && d.UCC != f.UCC) {
			continue
		}
		v.ByStatus[d.Status]++
		v.ByKind[d.Kind]++
		if f.Status != "" && d.Status != f.Status {
			continue
		}
		v.Rows = append(v.Rows, d)
	}
	sort.Slice(v.Rows, func(i, j int) bool { return v.Rows[i].ID > v.Rows[j].ID })
	if len(v.Rows) > 2000 {
		v.Rows = v.Rows[:2000]
	}
	v.Signing, _ = o.Signing(ctx, tenant, SystemActor)
	switch {
	case o.Mailer != nil:
		v.Mail = "smtp"
	case o.DevCodes || o.devFirm(ctx, tenant):
		v.Mail = "simulated"
	default:
		v.Mail = "none"
	}
	return v, nil
}

// DispatchCSV is the register for inspection.
func (o *Ops) DispatchCSV(ctx context.Context, tenant, actor string, f DispatchFilter) ([]byte, error) {
	v, err := o.DispatchRegister(ctx, tenant, actor, f)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	w.Write([]string{"Dispatch ID", "Document", "Reference", "Document date", "UCC", "Client", "Channel", "To", "Status", "Reason", "Created", "Sent at", "Bounced at", "Message ID", "File", "SHA-256", "Signed by", "Certificate serial", "Courier", "Attempt", "By"})
	for _, d := range v.Rows {
		var file, hash, signer, serial string
		if len(d.Files) > 0 {
			file, hash, signer, serial = d.Files[0].Name, d.Files[0].SHA256, d.Files[0].Signer, d.Files[0].Serial
		}
		st := d.Status
		if d.Simulated {
			st += " (simulated)"
		}
		w.Write([]string{d.ID, DocKindNames[d.Kind], d.Ref, d.Date, d.UCC, d.Name, d.Channel, d.To, st, d.Reason, d.Created, d.SentAt, d.BouncedAt, d.MessageID, file, hash, signer, serial, d.Courier, fmt.Sprint(d.Attempt), d.By})
	}
	w.Flush()
	return b.Bytes(), nil
}

// DispatchFile returns a document exactly as sent. Clients get their own
// through the portal; staff through the register.
func (o *Ops) DispatchFile(ctx context.Context, tenant, actor, id string, portal bool) (string, []byte, error) {
	d, _, err := docs.Get[Dispatch](ctx, o.Docs, tenant, cDispatch, id)
	if err != nil || len(d.Files) == 0 {
		return "", nil, fmt.Errorf("%w: no such document", ErrNotFound)
	}
	if portal {
		accts, err := o.PortalAccounts(ctx, tenant, actor)
		if err != nil || !contains(accts, d.UCC) {
			return "", nil, fmt.Errorf("%w: no such document", ErrNotFound)
		}
	} else if err := o.canSeeDispatch(ctx, tenant, actor); err != nil {
		return "", nil, err
	}
	rf, err := o.App.Ingest.Raw.Get(ctx, tenant, d.Files[0].RawID)
	if err != nil {
		return "", nil, err
	}
	if sha256Hex(rf.Content) != d.Files[0].SHA256 {
		return "", nil, errors.New("the stored document does not match its fingerprint")
	}
	if !portal {
		o.log(ctx, tenant, actor, "dispatch.file", d.ID, map[string]any{"ucc": d.UCC})
	}
	return d.Files[0].Name, rf.Content, nil
}

// VerifyDispatch re-checks a sent document's fingerprint and signature.
func (o *Ops) VerifyDispatch(ctx context.Context, tenant, actor, id string) (map[string]any, error) {
	_, data, err := o.DispatchFile(ctx, tenant, actor, id, false)
	if err != nil {
		return nil, err
	}
	v, whole, err := VerifyPDF(data)
	if err != nil {
		return map[string]any{"ok": false, "problem": err.Error()}, nil
	}
	return map[string]any{"ok": whole, "signer": v.Signer, "signing_time": v.SigningTime, "covers_whole_file": whole}, nil
}

// MarkBounced records a bounce reported later by the mail server or the
// mail provider's bounce report.
func (o *Ops) MarkBounced(ctx context.Context, tenant, actor, id, reason string) (Dispatch, error) {
	if err := o.require(ctx, tenant, actor, "Recording a bounce", RoleMaker, RoleChecker, RoleAdmin); err != nil {
		return Dispatch{}, err
	}
	d, err := docs.Update(ctx, o.Docs, tenant, cDispatch, id, actor, func(x *Dispatch) error {
		if x.Channel != "email" || (x.Status != "Sent" && x.Status != "Failed") {
			return invalid("only a sent email can bounce")
		}
		x.Status, x.Reason, x.BouncedAt = "Bounced", firstOf(strings.TrimSpace(reason), "bounced"), o.now().Format(time.RFC3339)
		return nil
	})
	if err != nil {
		return d, err
	}
	o.bounceWork(ctx, tenant, d)
	o.log(ctx, tenant, actor, "dispatch.bounced", d.ID, map[string]any{"ucc": d.UCC, "reason": d.Reason})
	return d, nil
}

// ImportBounces marks bounces from a mail provider's report: lines of
// email address (or message ID), date, reason. Each matches the latest
// email sent to it on or before that date.
func (o *Ops) ImportBounces(ctx context.Context, tenant, actor string, data []byte) (int, error) {
	if err := o.require(ctx, tenant, actor, "Recording bounces", RoleMaker, RoleChecker, RoleAdmin); err != nil {
		return 0, err
	}
	rows, err := csv.NewReader(bytes.NewReader(data)).ReadAll()
	if err != nil {
		return 0, invalid("bounce report must be CSV: address or message ID, date, reason")
	}
	all, _ := docs.List[Dispatch](ctx, o.Docs, tenant, cDispatch, "")
	sort.Slice(all, func(i, j int) bool { return all[i].ID > all[j].ID })
	n := 0
	for _, r := range rows {
		if len(r) == 0 || strings.EqualFold(strings.TrimSpace(r[0]), "email") {
			continue
		}
		who := strings.ToLower(strings.TrimSpace(r[0]))
		day, reason := "", "bounced"
		if len(r) > 1 {
			day = strings.TrimSpace(r[1])
		}
		if len(r) > 2 {
			reason = strings.TrimSpace(r[2])
		}
		for _, d := range all {
			if d.Status != "Sent" || d.Channel != "email" {
				continue
			}
			if strings.ToLower(d.To) != who && d.MessageID != r[0] {
				continue
			}
			if day != "" && d.Created[:10] > day {
				continue
			}
			if _, err := o.MarkBounced(ctx, tenant, actor, d.ID, reason); err == nil {
				n++
			}
			break
		}
	}
	return n, nil
}

// MarkPhysical records a printed copy sent by courier.
func (o *Ops) MarkPhysical(ctx context.Context, tenant, actor, id, courier string) (Dispatch, error) {
	if err := o.require(ctx, tenant, actor, "Recording a physical dispatch", RoleMaker, RoleChecker, RoleAdmin); err != nil {
		return Dispatch{}, err
	}
	courier = strings.TrimSpace(courier)
	if len(courier) < 3 {
		return Dispatch{}, invalid("give the courier and its reference number")
	}
	d, err := docs.Update(ctx, o.Docs, tenant, cDispatch, id, actor, func(x *Dispatch) error {
		if x.Status != "Bounced" && x.Status != "Physical pending" {
			return invalid("a printed copy is recorded for bounced or paper documents")
		}
		x.Status, x.Courier, x.CourierAt = "Physical sent", courier, o.now().Format(time.RFC3339)
		return nil
	})
	if err != nil {
		return d, err
	}
	o.closeWork(ctx, tenant, actor, "dispatch", d.ID, "sent by "+courier)
	o.log(ctx, tenant, actor, "dispatch.physical", d.ID, map[string]any{"ucc": d.UCC, "courier": courier})
	return d, nil
}

// Resend sends a held, failed or bounced document again (to the client's
// current email). Each attempt is its own entry.
func (o *Ops) Resend(ctx context.Context, tenant, actor, id string) (Dispatch, error) {
	if err := o.require(ctx, tenant, actor, "Sending documents", RoleMaker, RoleChecker); err != nil {
		return Dispatch{}, err
	}
	d, _, err := docs.Get[Dispatch](ctx, o.Docs, tenant, cDispatch, id)
	if err != nil {
		return d, fmt.Errorf("%w: no such dispatch", ErrNotFound)
	}
	if d.Status == "Sent" || d.Status == "Physical sent" {
		return d, invalid("this document was delivered; send it again from where it was made")
	}
	return o.sendAgain(ctx, tenant, actor, d)
}

func (o *Ops) sendAgain(ctx context.Context, tenant, actor string, d Dispatch) (Dispatch, error) {
	switch d.Kind {
	case DocContractNote:
		n, err := o.noteByNo(ctx, tenant, d.Ref)
		if err != nil {
			return d, err
		}
		return o.sendNote(ctx, tenant, actor, n)
	case DocMargin:
		return o.sendMarginStatement(ctx, tenant, actor, d.Date, d.UCC)
	case DocRAS:
		return o.sendRASStatement(ctx, tenant, actor, d.Date, d.UCC)
	case DocDP:
		return o.sendDPStatement(ctx, tenant, actor, d.Date[:7], d.UCC)
	case DocLedger:
		return o.sendLedgerStatement(ctx, tenant, actor, d.Ref[:10], d.Date, d.UCC)
	}
	return d, invalid("cannot resend a %s", d.Kind)
}

// ---- senders ----

func (o *Ops) noteByNo(ctx context.Context, tenant, no string) (ContractNote, error) {
	ns, _ := docs.List[ContractNote](ctx, o.Docs, tenant, cNote, "")
	for _, n := range ns {
		if n.No == no {
			return n, nil
		}
	}
	return ContractNote{}, fmt.Errorf("%w: contract note %s", ErrNotFound, no)
}

func (o *Ops) sendNote(ctx context.Context, tenant, actor string, n ContractNote) (Dispatch, error) {
	r, _, err := o.notePDF(ctx, tenant, n.Date, n.UCC, n.Part)
	if err != nil {
		return Dispatch{}, err
	}
	return o.dispatchDoc(ctx, tenant, actor, docRequest{Kind: DocContractNote, Ref: n.No, Date: n.Date, UCC: n.UCC, Report: r, Title: "Contract note " + n.No,
		FileName: "ContractNote_" + strings.NewReplacer("/", "-", " ", "_").Replace(n.No) + ".pdf",
		Text:     fmt.Sprintf("Dear %s,\n\nPlease find attached your contract note %s for trades on %s.", n.Name, n.No, n.Date)})
}

func (o *Ops) sendMarginStatement(ctx context.Context, tenant, actor, date, ucc string) (Dispatch, error) {
	rep, err := o.MarginStatement(ctx, tenant, date, ucc)
	if err != nil {
		return Dispatch{}, err
	}
	set, _ := o.Settings(ctx, tenant)
	return o.dispatchDoc(ctx, tenant, actor, docRequest{Kind: DocMargin, Ref: date, Date: date, UCC: ucc, Report: reportPDF(rep, set), Title: "Daily margin statement " + date,
		FileName: "MarginStatement_" + rep.UCC + "_" + date + ".pdf", Text: fmt.Sprintf("Dear %s,\n\nPlease find attached your daily margin statement for %s.", rep.Name, date)})
}

func (o *Ops) sendRASStatement(ctx context.Context, tenant, actor, settleDate, ucc string) (Dispatch, error) {
	rep, err := o.RASStatement(ctx, tenant, settleDate, ucc)
	if err != nil {
		return Dispatch{}, err
	}
	set, _ := o.Settings(ctx, tenant)
	return o.dispatchDoc(ctx, tenant, actor, docRequest{Kind: DocRAS, Ref: settleDate, Date: settleDate, UCC: ucc, Report: reportPDF(rep, set), Title: "Running account settlement " + settleDate,
		FileName: "RunningAccountSettlement_" + rep.UCC + "_" + settleDate + ".pdf", Text: fmt.Sprintf("Dear %s,\n\nYour running account was settled on %s. The attached statement shows the funds held, retained and paid to your bank.", rep.Name, settleDate)})
}

func (o *Ops) sendDPStatement(ctx context.Context, tenant, actor, month, ucc string) (Dispatch, error) {
	rep, err := o.DPStatement(ctx, tenant, month, ucc)
	if err != nil {
		return Dispatch{}, err
	}
	set, _ := o.Settings(ctx, tenant)
	return o.dispatchDoc(ctx, tenant, actor, docRequest{Kind: DocDP, Ref: month, Date: rep.To, UCC: ucc, Report: reportPDF(rep, set), Title: "DP statement " + month,
		FileName: "DPStatement_" + rep.UCC + "_" + month + ".pdf", Text: fmt.Sprintf("Dear %s,\n\nPlease find attached the statement of transactions and holdings of your demat account for %s.", rep.Name, month)})
}

func (o *Ops) sendLedgerStatement(ctx context.Context, tenant, actor, from, to, ucc string) (Dispatch, error) {
	rep, err := o.ClientReportFor(ctx, tenant, "ledger", ucc, from, to)
	if err != nil {
		return Dispatch{}, err
	}
	set, _ := o.Settings(ctx, tenant)
	return o.dispatchDoc(ctx, tenant, actor, docRequest{Kind: DocLedger, Ref: from + " to " + to, Date: to, UCC: ucc, Report: reportPDF(rep, set), Title: "Statement of account " + from + " to " + to,
		FileName: "StatementOfAccount_" + rep.UCC + "_" + to + ".pdf", Text: fmt.Sprintf("Dear %s,\n\nPlease find attached your statement of account for %s to %s.", rep.Name, from, to)})
}

// SendStatementsResult reports a bulk send.
type SendStatementsResult struct {
	Kind    string   `json:"kind"`
	Period  string   `json:"period"`
	Sent    int      `json:"sent"`
	Bounced int      `json:"bounced"`
	Held    int      `json:"held"`
	Paper   int      `json:"paper"`
	Failed  int      `json:"failed"`
	Skipped int      `json:"skipped"` // already delivered
	Errors  []string `json:"errors,omitempty"`
}

func (r *SendStatementsResult) add(d Dispatch, err error) {
	if err != nil {
		r.Errors = append(r.Errors, err.Error())
		return
	}
	switch d.Status {
	case "Sent":
		r.Sent++
	case "Bounced":
		r.Bounced++
	case "Held":
		r.Held++
	case "Physical pending":
		r.Paper++
	default:
		r.Failed++
	}
}

func (o *Ops) delivered(ctx context.Context, tenant, kind, ref, ucc string) bool {
	ds, _ := o.dispatchesFor(ctx, tenant, kind+"|"+ref+"|"+ucc)
	for _, d := range ds {
		if d.Status == "Sent" || d.Status == "Physical sent" || d.Status == "Physical pending" {
			return true
		}
	}
	return false
}

// SendStatements sends one kind of statement to every client it applies
// to: margin (date: clients with margin that day), ras (date: the
// settlement batch), dp (month YYYY-MM: every DP account), ledger (month).
// Clients already sent the same document are skipped.
func (o *Ops) SendStatements(ctx context.Context, tenant, actor, kind, period string) (SendStatementsResult, error) {
	if actor != SystemActor {
		if err := o.require(ctx, tenant, actor, "Sending statements", RoleMaker, RoleChecker); err != nil {
			return SendStatementsResult{}, err
		}
	}
	res := SendStatementsResult{Kind: kind, Period: period}
	switch kind {
	case DocMargin:
		if !validDate(period) {
			return res, invalid("give the trade date")
		}
		rep, err := o.Margins(ctx, tenant, period)
		if err != nil {
			return res, err
		}
		for _, r := range rep.Rows {
			if o.delivered(ctx, tenant, kind, period, r.UCC) {
				res.Skipped++
				continue
			}
			res.add(o.sendMarginStatement(ctx, tenant, actor, period, r.UCC))
		}
	case DocRAS:
		bs, err := o.RASBatches(ctx, tenant, period)
		if err != nil {
			return res, err
		}
		for _, b := range bs {
			for _, it := range b.Items {
				if o.delivered(ctx, tenant, kind, period, it.UCC) {
					res.Skipped++
					continue
				}
				res.add(o.sendRASStatement(ctx, tenant, actor, period, it.UCC))
			}
		}
	case DocDP:
		if len(period) != 7 {
			return res, invalid("give the month (YYYY-MM)")
		}
		bos, _ := docs.List[BOAccount](ctx, o.Docs, tenant, cBO, "")
		for _, b := range bos {
			if b.Status == "Closed" && b.ClosedOn < period+"-01" {
				continue
			}
			if o.delivered(ctx, tenant, kind, period, b.UCC) {
				res.Skipped++
				continue
			}
			res.add(o.sendDPStatement(ctx, tenant, actor, period, b.UCC))
		}
	case DocLedger:
		if len(period) != 7 {
			return res, invalid("give the month (YYYY-MM)")
		}
		from := period + "-01"
		to := addDays(addMonths(from, 1), -1)
		cs, err := o.Clients(ctx, tenant)
		if err != nil {
			return res, err
		}
		for _, c := range cs {
			if c.Status != "Active" {
				continue
			}
			ref := from + " to " + to
			if o.delivered(ctx, tenant, kind, ref, c.UCC) {
				res.Skipped++
				continue
			}
			res.add(o.sendLedgerStatement(ctx, tenant, actor, from, to, c.UCC))
		}
	default:
		return res, invalid("unknown statement %q", kind)
	}
	o.log(ctx, tenant, actor, "statements.sent", kind+" "+period, map[string]any{"sent": res.Sent, "bounced": res.Bounced, "held": res.Held, "paper": res.Paper, "failed": res.Failed})
	return res, nil
}

