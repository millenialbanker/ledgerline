package ops

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
	"ledgerline/internal/xfile"
)

// ---- HDFC Bank ENet bulk payout file ----
//
// An approved payout batch (payments.go) goes to the bank as an HDFC ENet
// bulk upload: layout hdfc-enet-payout (config/formats), 28 comma-separated
// fields a line, one line per payment, no header or trailer, CRLF. The
// layout was confirmed against a broker's live upload file (structure only).
//
// Transaction type: an HDFC IFSC is an internal transfer (I); otherwise
// RTGS (R) from the RTGS threshold or when the payout is marked RTGS;
// otherwise NEFT (N). Settings can turn I off or force one type.
//
// Every file is kept (name, sequence, rows, total, content, downloads). The
// same file may be downloaded again; a second file for the same payouts
// needs the first one cancelled. Any failed check blocks the file and every
// problem is listed.

const (
	cHDFCFile = "hdfc_enet_file"
	cHDFCSeq  = "hdfc_enet_seq"

	hdfcSpecID = "hdfc-enet-payout"
	// HDFCDefaultFileName is the default name template: the firm's ENet
	// client ID, the date as DDMM and a 3-digit sequence (BALJIT19910411.817).
	HDFCDefaultFileName = "{hdfc_client_id}{date:0201}.{seq:3}"
	// HDFCSeqRunning counts files across days (the default); HDFCSeqDaily
	// restarts at 001 each day. Both wrap from 999 to 001.
	HDFCSeqRunning = "running"
	HDFCSeqDaily   = "daily"
	hdfcInstrLen   = 14
)

// HDFC does not publish per-field lengths for the ENet bulk upload; these
// are safe limits (35 is the usual NEFT/RTGS name and address length).
var hdfcLimits = map[string]int{"Beneficiary Code": 20, "Beneficiary Name": 35, "Customer Reference Number": 20, "Beneficiary Bank Name": 35, "Beneficiary Bank Branch Name": 35, "Beneficiary Email ID": 100}

// rtgsMinimum is RBI's RTGS minimum, ₹2 lakh.
const rtgsMinimum money.Amount = 20000000

var (
	reHDFCClientID = regexp.MustCompile(`^[A-Z0-9]{1,20}$`)
	reHDFCSuffix   = regexp.MustCompile(`^[A-Z0-9]{1,6}$`)
	reDigits       = regexp.MustCompile(`^[0-9]*$`)
	reFileNameOK   = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	reTemplateKey  = regexp.MustCompile(`\{([a-z_]+)(?::([^}]+))?\}`)
)

// rtgsThreshold is the amount from which payouts go by RTGS (default ₹2 lakh).
func (s Settings) rtgsThreshold() money.Amount {
	if a, err := money.ParseAmount(strings.TrimSpace(s.RTGSThreshold)); err == nil && a > 0 {
		return a
	}
	return rtgsMinimum
}

func (s Settings) hdfcFileTemplate() string {
	if t := strings.TrimSpace(s.HDFCFileName); t != "" {
		return t
	}
	return HDFCDefaultFileName
}

func (s Settings) hdfcSeqMode() string {
	if s.HDFCSeqMode == HDFCSeqDaily {
		return HDFCSeqDaily
	}
	return HDFCSeqRunning
}

func (s Settings) hdfcSuffix() string {
	if x := strings.TrimSpace(s.HDFCCustRefSuffix); x != "" {
		return strings.ToUpper(x)
	}
	return "PAY"
}

// checkHDFCSettings validates and normalises the ENet settings.
func checkHDFCSettings(s *Settings) error {
	s.HDFCClientID = strings.ToUpper(strings.TrimSpace(s.HDFCClientID))
	if s.HDFCClientID != "" && !reHDFCClientID.MatchString(s.HDFCClientID) {
		return invalid("HDFC ENet client ID is letters and digits only (up to 20)")
	}
	s.HDFCCustRefSuffix = strings.ToUpper(strings.TrimSpace(s.HDFCCustRefSuffix))
	if s.HDFCCustRefSuffix != "" && !reHDFCSuffix.MatchString(s.HDFCCustRefSuffix) {
		return invalid("customer reference suffix is up to 6 letters or digits (PAY by default)")
	}
	s.HDFCInstrPrefix = strings.TrimSpace(s.HDFCInstrPrefix)
	if !reDigits.MatchString(s.HDFCInstrPrefix) || len(s.HDFCInstrPrefix) > 8 {
		return invalid("instruction reference prefix is up to 8 digits; the rest of the 14 digits is a running number")
	}
	switch s.HDFCSeqMode {
	case "", HDFCSeqRunning, HDFCSeqDaily:
	default:
		return invalid("file sequence is running (across days) or daily (restarts at 001)")
	}
	s.HDFCForceType = strings.ToUpper(strings.TrimSpace(s.HDFCForceType))
	switch s.HDFCForceType {
	case "", "N", "R", "I":
	default:
		return invalid("force the transaction type to N, R or I, or leave it to Ledgerline")
	}
	if t := strings.TrimSpace(s.RTGSThreshold); t != "" {
		a, err := money.ParseAmount(t)
		if err != nil || a < rtgsMinimum {
			return invalid("RTGS threshold must be an amount of at least ₹2,00,000 (RTGS minimum)")
		}
	}
	s.HDFCFileName = strings.TrimSpace(s.HDFCFileName)
	if s.HDFCFileName != "" {
		if _, err := hdfcFileName(s.HDFCFileName, "SAMPLE1", "2026-11-04", 1); err != nil {
			return err
		}
	}
	return nil
}

// hdfcFileName fills the name template: {hdfc_client_id}, {date:layout}
// (Go layout, 0201 = DDMM) and {seq:n}; anything else is fixed text.
func hdfcFileName(tmpl, clientID, date string, seq int64) (string, error) {
	hasSeq := false
	for _, m := range reTemplateKey.FindAllStringSubmatch(tmpl, -1) {
		switch m[1] {
		case "hdfc_client_id", "date":
		case "seq":
			hasSeq = true
		default:
			return "", invalid("file name template: {%s} is not known; use {hdfc_client_id}, {date:0201} and {seq:3}", m[1])
		}
	}
	if !hasSeq {
		return "", invalid("file name template needs the sequence, {seq:3}")
	}
	if strings.Contains(tmpl, "{hdfc_client_id}") && clientID == "" {
		return "", invalid("enter the firm's HDFC ENet client ID in Settings → Receipts and payouts")
	}
	spec := xfile.Spec{ID: hdfcSpecID, Template: tmpl}
	name, err := spec.FileName(xfile.Vars{"hdfc_client_id": clientID, "date": date, "seq": strconv.FormatInt(seq, 10)})
	if err != nil {
		return "", invalid("file name template: %v", err)
	}
	if !reFileNameOK.MatchString(name) {
		return "", invalid("file name %q may have only letters, digits, dot, hyphen and underscore", name)
	}
	return name, nil
}

// hdfcTxnType picks N, R or I for a payout.
func hdfcTxnType(set Settings, it PayoutItem) string {
	if set.HDFCForceType != "" {
		return set.HDFCForceType
	}
	if !set.HDFCNoInternal && strings.HasPrefix(strings.ToUpper(it.IFSC), "HDFC") {
		return "I"
	}
	if it.Mode == "RTGS" || it.Amount >= set.rtgsThreshold() {
		return "R"
	}
	return "N"
}

// hdfcClean takes out what would break a comma-separated line: commas,
// quotes, tabs and line breaks become spaces; spaces are collapsed.
func hdfcClean(s string) string {
	s = strings.Map(func(r rune) rune {
		switch r {
		case ',', '"', '\r', '\n', '\t':
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// ---- the file record and sequence ----

// HDFCFile is one ENet payout file produced for a batch.
type HDFCFile struct {
	ID           string           `json:"id"`
	Batch        string           `json:"batch"`
	Name         string           `json:"name"`
	Series       string           `json:"series"`
	Seq          int64            `json:"seq"`
	Date         string           `json:"date"`
	Rows         int              `json:"rows"`
	Total        money.Amount     `json:"total"`
	Types        map[string]int   `json:"types"`
	Requests     []string         `json:"requests"`
	Content      string           `json:"content,omitempty"`
	Status       string           `json:"status"` // Active, Cancelled
	By           string           `json:"by"`
	At           string           `json:"at"`
	Downloads    []HDFCFileAccess `json:"downloads"`
	CancelledBy  string           `json:"cancelled_by,omitempty"`
	CancelledAt  string           `json:"cancelled_at,omitempty"`
	CancelReason string           `json:"cancel_reason,omitempty"`
}

// HDFCFileAccess is one download of a file.
type HDFCFileAccess struct {
	By string `json:"by"`
	At string `json:"at"`
}

type hdfcSeqDoc struct {
	Next int64  `json:"next"`
	Date string `json:"date"`
}

// HDFC file series: the main one numbers every file by default. The
// broker's log shows an occasional second series under the same client ID
// (probably another debit account or payment type; unconfirmed), so a file
// may be numbered from a second, separate series instead.
const (
	HDFCSeriesMain   = "main"
	HDFCSeriesSecond = "second"
)

func hdfcSeries(s string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", HDFCSeriesMain:
		return HDFCSeriesMain, nil
	case HDFCSeriesSecond, "2":
		return HDFCSeriesSecond, nil
	}
	return "", invalid("file series is main or second")
}

// HDFCSeq is the next file sequence number as Settings shows it.
type HDFCSeq struct {
	Series   string `json:"series"`    // main or second
	Next     int64  `json:"next"`      // for a file on Date
	Date     string `json:"date"`      // the date asked about
	Mode     string `json:"mode"`      // running or daily
	LastDate string `json:"last_date"` // date of the last file numbered
	Sample   string `json:"sample,omitempty"`
	Problem  string `json:"problem,omitempty"`
}

func wrapSeq(n int64) int64 {
	if n < 1 || n > 999 {
		return 1
	}
	return n
}

// seqFor is the number the next file on date gets.
func (d hdfcSeqDoc) seqFor(mode, date string) int64 {
	if mode == HDFCSeqDaily && d.Date != date {
		return 1
	}
	return wrapSeq(d.Next)
}

// HDFCNextSeq shows the sequence the next ENet file on date will take.
func (o *Ops) HDFCNextSeq(ctx context.Context, tenant, series, date string) (HDFCSeq, error) {
	series, err := hdfcSeries(series)
	if err != nil {
		return HDFCSeq{}, err
	}
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return HDFCSeq{}, err
	}
	d, _, err := docs.GetOr(ctx, o.Docs, tenant, cHDFCSeq, series, hdfcSeqDoc{Next: 1})
	if err != nil {
		return HDFCSeq{}, err
	}
	v := HDFCSeq{Series: series, Next: d.seqFor(set.hdfcSeqMode(), date), Date: date, Mode: set.hdfcSeqMode(), LastDate: d.Date}
	if n, err := hdfcFileName(set.hdfcFileTemplate(), set.HDFCClientID, date, v.Next); err == nil {
		v.Sample = n
	} else {
		v.Problem = strings.TrimPrefix(err.Error(), "invalid: ")
	}
	return v, nil
}

// SetHDFCNextSeq sets the next file sequence (to carry on from the last
// file another system sent), for files from date.
func (o *Ops) SetHDFCNextSeq(ctx context.Context, tenant, actor, series, date string, next int64) (HDFCSeq, error) {
	if err := o.require(ctx, tenant, actor, "Changing settings", RoleAdmin); err != nil {
		return HDFCSeq{}, err
	}
	series, err := hdfcSeries(series)
	if err != nil {
		return HDFCSeq{}, err
	}
	if next < 1 || next > 999 {
		return HDFCSeq{}, invalid("the next file number is 1 to 999")
	}
	if err := o.ensureHDFCSeq(ctx, tenant, actor, series); err != nil {
		return HDFCSeq{}, err
	}
	if _, err := docs.Update(ctx, o.Docs, tenant, cHDFCSeq, series, actor, func(d *hdfcSeqDoc) error {
		d.Next, d.Date = next, date
		return nil
	}); err != nil {
		return HDFCSeq{}, err
	}
	o.log(ctx, tenant, actor, "hdfc.seq.set", "hdfc-enet-"+series, map[string]any{"next": next, "date": date})
	return o.HDFCNextSeq(ctx, tenant, series, date)
}

func (o *Ops) ensureHDFCSeq(ctx context.Context, tenant, actor, series string) error {
	if _, _, err := docs.Get[hdfcSeqDoc](ctx, o.Docs, tenant, cHDFCSeq, series); errors.Is(err, docs.ErrNotFound) {
		if _, err := docs.Put(ctx, o.Docs, tenant, cHDFCSeq, series, hdfcSeqDoc{Next: 1}, 0, actor); err != nil && !errors.Is(err, docs.ErrStale) {
			return err
		}
	} else if err != nil {
		return err
	}
	return nil
}

// takeHDFCSeq numbers a file on date from a series. Several files a day
// each take the next number; 999 wraps to 001.
func (o *Ops) takeHDFCSeq(ctx context.Context, tenant, actor, series, mode, date string) (int64, error) {
	if err := o.ensureHDFCSeq(ctx, tenant, actor, series); err != nil {
		return 0, err
	}
	var n int64
	_, err := docs.Update(ctx, o.Docs, tenant, cHDFCSeq, series, actor, func(d *hdfcSeqDoc) error {
		n = d.seqFor(mode, date)
		d.Next, d.Date = n%999+1, date
		return nil
	})
	return n, err
}

// ---- checks and the file ----

// HDFCLine is one payment as the file will carry it.
type HDFCLine struct {
	Request string       `json:"request"`
	UCC     string       `json:"ucc"`
	Type    string       `json:"type"`
	Amount  money.Amount `json:"amount"`
	Ref     string       `json:"ref,omitempty"`
}

// HDFCFileView is a batch's ENet file state: the checks, the lines and
// the files produced.
type HDFCFileView struct {
	Batch    string     `json:"batch"`
	Ready    bool       `json:"ready"`
	Problems []string   `json:"problems"`
	Lines    []HDFCLine `json:"lines"`
	Files    []HDFCFile `json:"files"`
	Active   string     `json:"active,omitempty"`
	NextName string     `json:"next_name,omitempty"`
}

// PayoutFileBlocked lists every problem that stops the file.
type PayoutFileBlocked struct {
	Batch    string
	Problems []string
}

func (e *PayoutFileBlocked) Error() string {
	return fmt.Sprintf("invalid: the HDFC ENet file for %s is blocked by %d problem(s): %s", e.Batch, len(e.Problems), strings.Join(e.Problems, "; "))
}
func (e *PayoutFileBlocked) Unwrap() error { return ErrInvalid }

// hdfcRows builds the file's rows for a batch and checks them. refs gives
// each request's instruction reference (a placeholder when checking only).
func (o *Ops) hdfcRows(ctx context.Context, tenant string, set Settings, b PayoutBatch, refs map[string]string) ([]xfile.Row, []HDFCLine, []string) {
	var rows []xfile.Row
	var lines []HDFCLine
	var probs []string
	bad := func(it PayoutItem, f string, a ...any) {
		probs = append(probs, it.Request+" "+it.UCC+": "+fmt.Sprintf(f, a...))
	}
	if b.Status == "Confirmed" {
		probs = append(probs, "batch "+b.ID+" is already confirmed with the bank's result")
	}
	seen := map[string]string{}
	var total money.Amount
	prs, _ := docs.List[PayoutRequest](ctx, o.Docs, tenant, cPayout, "")
	reqs := map[string]PayoutRequest{}
	for _, p := range prs {
		reqs[p.ID] = p
	}
	var approved money.Amount
	for _, p := range prs {
		if p.Batch == b.ID {
			approved += p.Amount
		}
	}
	for _, it := range b.Items {
		c, err := o.Client(ctx, tenant, it.UCC)
		if err != nil {
			bad(it, "client not found")
			continue
		}
		typ := hdfcTxnType(set, it)
		ifsc := strings.ToUpper(strings.TrimSpace(it.IFSC))
		acct := strings.TrimSpace(it.Account)
		// Only an active registered bank account of the client (as for
		// receipts): the one chosen for the payout or the primary
		// (clientbanks.go).
		reg, isReg := registeredBank(c, acct, ifsc)
		if !isReg || ifsc == "" {
			pb := primaryBank(c)
			bad(it, "account %s %s is not the client's registered bank account (primary now %s %s)", MaskAccount(acct), ifsc, MaskAccount(pb.Account), strings.ToUpper(pb.IFSC))
		}
		if len(ifsc) != 11 || !reIFSC.MatchString(ifsc) {
			bad(it, "IFSC %q is not valid (11 characters, the 5th is 0)", ifsc)
		}
		if !reAcct.MatchString(acct) {
			bad(it, "account number must be 9 to 18 digits only")
		}
		if it.Amount <= 0 {
			bad(it, "amount must be above zero")
		}
		if p, ok := reqs[it.Request]; !ok {
			bad(it, "payout request not found")
		} else {
			if p.Amount != it.Amount {
				bad(it, "amount ₹%s differs from the approved ₹%s", formatINR(it.Amount.Dec()), formatINR(p.Amount.Dec()))
			}
			if p.Batch != b.ID || p.Status != "Approved" {
				bad(it, "request is %s in batch %q, not an approved payout of %s", p.Status, p.Batch, b.ID)
			}
		}
		if prev, dup := seen[it.UCC]; dup && !set.HDFCRepeatClient {
			bad(it, "client is already in this file (%s); allow repeat clients in Settings to pay both", prev)
		}
		seen[it.UCC] = it.Request
		switch {
		case typ == "I" && !strings.HasPrefix(ifsc, "HDFC"):
			bad(it, "type I (HDFC to HDFC) needs an HDFC IFSC, not %s", ifsc)
		case typ == "R" && it.Amount < rtgsMinimum:
			bad(it, "RTGS needs at least ₹2,00,000; ₹%s goes by NEFT", formatINR(it.Amount.Dec()))
		}
		name, bank, branch := hdfcClean(c.Name), hdfcClean(it.Bank), hdfcClean(firstOf(reg.Branch, it.Branch))
		email := strings.TrimSpace(c.Email)
		if name == "" {
			bad(it, "beneficiary name is empty")
		}
		if !reEmail.MatchString(email) {
			bad(it, "the client has no valid registered email")
		}
		cref := it.UCC + "-" + set.hdfcSuffix()
		for _, f := range []struct{ field, v string }{{"Beneficiary Code", it.UCC}, {"Beneficiary Name", name}, {"Customer Reference Number", cref}, {"Beneficiary Bank Name", bank}, {"Beneficiary Bank Branch Name", branch}, {"Beneficiary Email ID", email}} {
			if n := utf8.RuneCountInString(f.v); n > hdfcLimits[f.field] {
				bad(it, "%s is %d characters; the limit is %d", f.field, n, hdfcLimits[f.field])
			}
			for _, r := range f.v {
				if r < 0x20 || r > 0x7e {
					bad(it, "%s has a character the bank file cannot carry (%q)", f.field, r)
					break
				}
			}
		}
		ref := refs[it.Request]
		total += it.Amount
		lines = append(lines, HDFCLine{Request: it.Request, UCC: it.UCC, Type: typ, Amount: it.Amount, Ref: ref})
		rows = append(rows, xfile.Row{Values: map[string]string{
			"txn_type": typ, "bene_code": it.UCC, "account": acct, "amount": it.Amount.String(), "name": name,
			"instruction_ref": ref, "customer_ref": cref, "txn_date": b.Date, "ifsc": ifsc, "bank_name": bank, "branch_name": branch, "email": email,
		}})
	}
	if len(b.Items) == 0 {
		probs = append(probs, "the batch has no payouts")
	}
	if total != b.Total || total != approved {
		probs = append(probs, fmt.Sprintf("file total ₹%s does not equal the batch's approved total ₹%s (approved payouts in the batch ₹%s)", formatINR(total.Dec()), formatINR(b.Total.Dec()), formatINR(approved.Dec())))
	}
	return rows, lines, probs
}

// hdfcFiles are a batch's files, newest first, without their content.
func (o *Ops) hdfcFiles(ctx context.Context, tenant, batch string) ([]HDFCFile, error) {
	fs, err := docs.List[HDFCFile](ctx, o.Docs, tenant, cHDFCFile, batch+"-")
	out := fs[:0]
	for _, f := range fs {
		if f.Batch == batch {
			out = append(out, f)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At > out[j].At || out[i].At == out[j].At && out[i].ID > out[j].ID })
	return out, err
}

func placeholderRefs(set Settings, b PayoutBatch) map[string]string {
	refs := map[string]string{}
	for _, it := range b.Items {
		refs[it.Request] = it.InstrRef
		if refs[it.Request] == "" {
			refs[it.Request] = set.HDFCInstrPrefix + strings.Repeat("0", hdfcInstrLen-len(set.HDFCInstrPrefix))
		}
	}
	return refs
}

// HDFCPayoutFile shows a batch's ENet file: the checks, each payment's
// transaction type and the files produced so far.
func (o *Ops) HDFCPayoutFile(ctx context.Context, tenant, batch string) (HDFCFileView, error) {
	b, _, err := docs.Get[PayoutBatch](ctx, o.Docs, tenant, cPOBatch, batch)
	if err != nil {
		return HDFCFileView{}, err
	}
	set, _ := o.Settings(ctx, tenant)
	v := HDFCFileView{Batch: b.ID, Problems: []string{}, Files: []HDFCFile{}}
	_, v.Lines, v.Problems = o.hdfcRows(ctx, tenant, set, b, placeholderRefs(set, b))
	if v.Problems == nil {
		v.Problems = []string{}
	}
	fs, err := o.hdfcFiles(ctx, tenant, b.ID)
	if err != nil {
		return v, err
	}
	for _, f := range fs {
		f.Content = ""
		v.Files = append(v.Files, f)
		if f.Status == "Active" && v.Active == "" {
			v.Active = f.ID
		}
	}
	if v.Active == "" {
		if s, err := o.HDFCNextSeq(ctx, tenant, HDFCSeriesMain, b.Date); err == nil {
			if s.Problem != "" {
				v.Problems = append(v.Problems, s.Problem)
			}
			v.NextName = s.Sample
		}
	}
	v.Ready = len(v.Problems) == 0 || v.Active != ""
	return v, nil
}

// MakeHDFCPayoutFile produces the ENet file for a batch, or returns the
// batch's file already produced (a re-download). A new file for the same
// payouts needs the earlier one cancelled first.
func (o *Ops) MakeHDFCPayoutFile(ctx context.Context, tenant, actor, batch, series string) (HDFCFile, error) {
	if err := o.require(ctx, tenant, actor, "Producing the payout bank file", RoleMaker, RoleChecker); err != nil {
		return HDFCFile{}, err
	}
	series, err := hdfcSeries(series)
	if err != nil {
		return HDFCFile{}, err
	}
	b, _, err := docs.Get[PayoutBatch](ctx, o.Docs, tenant, cPOBatch, batch)
	if err != nil {
		return HDFCFile{}, err
	}
	fs, err := o.hdfcFiles(ctx, tenant, b.ID)
	if err != nil {
		return HDFCFile{}, err
	}
	for _, f := range fs {
		if f.Status == "Active" {
			return f, nil
		}
	}
	// No payout may be in another active file either.
	all, err := docs.List[HDFCFile](ctx, o.Docs, tenant, cHDFCFile, "")
	if err != nil {
		return HDFCFile{}, err
	}
	in := map[string]string{}
	for _, f := range all {
		if f.Status == "Active" {
			for _, r := range f.Requests {
				in[r] = f.Name
			}
		}
	}
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return HDFCFile{}, err
	}
	_, _, probs := o.hdfcRows(ctx, tenant, set, b, placeholderRefs(set, b))
	for _, it := range b.Items {
		if n, ok := in[it.Request]; ok {
			probs = append(probs, fmt.Sprintf("%s %s is already in file %s; cancel that file first", it.Request, it.UCC, n))
		}
	}
	if _, err := hdfcFileName(set.hdfcFileTemplate(), set.HDFCClientID, b.Date, 1); err != nil {
		probs = append(probs, strings.TrimPrefix(err.Error(), "invalid: "))
	}
	if len(probs) > 0 {
		return HDFCFile{}, &PayoutFileBlocked{Batch: b.ID, Problems: probs}
	}
	// Instruction references: given once per payment and kept, so a
	// replacement file carries the same references.
	b, err = o.assignInstrRefs(ctx, tenant, actor, set, b.ID)
	if err != nil {
		return HDFCFile{}, err
	}
	refs := map[string]string{}
	for _, it := range b.Items {
		refs[it.Request] = it.InstrRef
	}
	rows, lines, probs := o.hdfcRows(ctx, tenant, set, b, refs)
	if len(probs) > 0 {
		return HDFCFile{}, &PayoutFileBlocked{Batch: b.ID, Problems: probs}
	}
	lib, err := XFiles()
	if err != nil {
		return HDFCFile{}, err
	}
	spec, ok := lib.Get(hdfcSpecID)
	if !ok {
		return HDFCFile{}, fmt.Errorf("layout %s is missing", hdfcSpecID)
	}
	content, err := spec.Write(rows)
	if err != nil {
		return HDFCFile{}, err
	}
	seq, err := o.takeHDFCSeq(ctx, tenant, actor, series, set.hdfcSeqMode(), b.Date)
	if err != nil {
		return HDFCFile{}, err
	}
	name, err := hdfcFileName(set.hdfcFileTemplate(), set.HDFCClientID, b.Date, seq)
	if err != nil {
		return HDFCFile{}, err
	}
	f := HDFCFile{ID: fmt.Sprintf("%s-%02d", b.ID, len(fs)+1), Batch: b.ID, Name: name, Series: series, Seq: seq, Date: b.Date, Rows: len(rows), Types: map[string]int{},
		Content: string(content), Status: "Active", By: actor, At: o.now().Format("2006-01-02 15:04:05"), Downloads: []HDFCFileAccess{}}
	for _, l := range lines {
		f.Total += l.Amount
		f.Types[l.Type]++
		f.Requests = append(f.Requests, l.Request)
	}
	if _, err := docs.Put(ctx, o.Docs, tenant, cHDFCFile, f.ID, f, 0, actor); err != nil {
		if errors.Is(err, docs.ErrStale) {
			// Someone produced it at the same moment: theirs stands.
			if fs, e := o.hdfcFiles(ctx, tenant, b.ID); e == nil {
				for _, x := range fs {
					if x.Status == "Active" {
						return x, nil
					}
				}
			}
		}
		return HDFCFile{}, err
	}
	o.log(ctx, tenant, actor, "payout.bank_file", f.ID, map[string]any{"batch": b.ID, "name": name, "seq": seq, "rows": f.Rows, "total": f.Total.String(), "types": f.Types})
	return f, nil
}

func (o *Ops) assignInstrRefs(ctx context.Context, tenant, actor string, set Settings, batch string) (PayoutBatch, error) {
	b, _, err := docs.Get[PayoutBatch](ctx, o.Docs, tenant, cPOBatch, batch)
	if err != nil {
		return b, err
	}
	refs := map[string]string{}
	for _, it := range b.Items {
		if it.InstrRef != "" {
			continue
		}
		n, err := o.next(ctx, tenant, "hdfc-instruction", actor)
		if err != nil {
			return b, err
		}
		w := hdfcInstrLen - len(set.HDFCInstrPrefix)
		num := fmt.Sprintf("%0*d", w, n)
		if len(num) > w {
			return b, invalid("instruction references with prefix %q have run out of digits; shorten the prefix", set.HDFCInstrPrefix)
		}
		refs[it.Request] = set.HDFCInstrPrefix + num
	}
	if len(refs) == 0 {
		return b, nil
	}
	return docs.Update(ctx, o.Docs, tenant, cPOBatch, batch, actor, func(x *PayoutBatch) error {
		for i := range x.Items {
			if r := refs[x.Items[i].Request]; r != "" && x.Items[i].InstrRef == "" {
				x.Items[i].InstrRef = r
			}
		}
		return nil
	})
}

// DownloadHDFCPayoutFile returns a file's content and records the download.
func (o *Ops) DownloadHDFCPayoutFile(ctx context.Context, tenant, actor, id string) (string, []byte, error) {
	if err := o.require(ctx, tenant, actor, "Producing the payout bank file", RoleMaker, RoleChecker); err != nil {
		return "", nil, err
	}
	f, err := docs.Update(ctx, o.Docs, tenant, cHDFCFile, id, actor, func(x *HDFCFile) error {
		if x.Status != "Active" {
			return fmt.Errorf("%w: file %s was cancelled; produce a new one", ErrState, x.Name)
		}
		x.Downloads = append(x.Downloads, HDFCFileAccess{By: actor, At: o.now().Format("2006-01-02 15:04:05")})
		return nil
	})
	if err != nil {
		return "", nil, err
	}
	o.log(ctx, tenant, actor, "payout.bank_file.downloaded", f.ID, map[string]any{"name": f.Name, "downloads": len(f.Downloads)})
	return f.Name, []byte(f.Content), nil
}

// CancelHDFCPayoutFile cancels a file (not uploaded, or rejected by the
// bank) so that a new one may be produced for its payouts.
func (o *Ops) CancelHDFCPayoutFile(ctx context.Context, tenant, actor, id, reason string) (HDFCFile, error) {
	if err := o.require(ctx, tenant, actor, "Cancelling a payout bank file", RoleChecker); err != nil {
		return HDFCFile{}, err
	}
	if strings.TrimSpace(reason) == "" {
		return HDFCFile{}, invalid("give the reason for cancelling the file (for example: not uploaded, rejected by the bank)")
	}
	f, err := docs.Update(ctx, o.Docs, tenant, cHDFCFile, id, actor, func(x *HDFCFile) error {
		if x.Status != "Active" {
			return fmt.Errorf("%w: file %s is already cancelled", ErrState, x.Name)
		}
		x.Status, x.CancelledBy, x.CancelledAt, x.CancelReason = "Cancelled", actor, o.now().Format("2006-01-02 15:04:05"), strings.TrimSpace(reason)
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "payout.bank_file.cancelled", f.ID, map[string]any{"name": f.Name, "reason": f.CancelReason})
		f.Content = ""
	}
	return f, err
}

// MarkPayoutRTGS marks an approved or requested payout, not yet released,
// to go by RTGS whatever its amount (at least ₹2 lakh).
func (o *Ops) MarkPayoutRTGS(ctx context.Context, tenant, actor, id string, rtgs bool) (PayoutRequest, error) {
	if err := o.require(ctx, tenant, actor, "Entering a payout request", RoleMaker, RoleChecker, RoleBranch, RoleAP); err != nil {
		return PayoutRequest{}, err
	}
	p, err := docs.Update(ctx, o.Docs, tenant, cPayout, id, actor, func(x *PayoutRequest) error {
		if x.Batch != "" || (x.Status != "Requested" && x.Status != "Approved") {
			return fmt.Errorf("%w: payout %s is %s and released already or closed", ErrState, id, x.Status)
		}
		if rtgs && x.Amount < rtgsMinimum {
			return invalid("RTGS needs at least ₹2,00,000")
		}
		x.RTGS = rtgs
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "payout.rtgs", id, map[string]any{"rtgs": rtgs})
	}
	return p, err
}
