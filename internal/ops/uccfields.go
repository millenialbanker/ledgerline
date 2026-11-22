package ops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"ledgerline/internal/docs"
	"ledgerline/internal/xfile"
)

// ---- client details at the exchanges against Ledgerline's ----
//
// Exchanges penalise a member whose UCC records do not carry the mobile
// number and e-mail address the client gave in the registration documents:
// NSE's inspections list it among the common violations (NSE/INSP/73503,
// circular 9/2026 of 27 March 2026, Annexure A-B items 46 and 47; and
// NSE/INSP/65693 of 19 December 2024), and penalty orders have turned on
// it (Profitmart: mobile numbers differed in 142 cases and e-mail addresses
// in 140; an NSE order of 8 May 2024: 37 cases). The UCC master circular
// (NSE/ISC/73973 of 30 April 2026) has a client's details changed at the
// exchange whenever they change in the member's records.
//
// Two checks, one self-inspection test (ucc_fields):
//
//   - The exchanges' UCC downloads (NSE UCI Online, BSE and MCX), dropped on
//     Exchange files with nse_ucc_download, bse_ucc_download or
//     mcx_ucc_download in the name. No layout is published, so the columns
//     are read by their header names (config/formats/*-ucc-download.json;
//     xfile/headers.go). The file itself is not kept: for each client only
//     fingerprints (one-way hashes) of the PAN, mobile, e-mail, primary
//     bank account with IFSC and correspondence address, with a masked tail
//     of each for the screen. A download older than 45 days is ignored.
//   - When NSE's UCC API accepts a client (nseuci.go), the fingerprints of
//     the same fields are kept with the registration. A client whose
//     details have changed since and has not been sent again is reported.
//
// Values are normalised before hashing, so formatting is not a difference:
// PAN in capitals; the mobile's last ten digits; the e-mail in lower case;
// the account number's digits without leading zeros, with the IFSC in
// capitals; the address lines and PIN as letters and digits only (the
// city and state are left out: the exchanges hold them as codes).

const (
	cUCCDl      = "uccdownload" // key: EXCHANGE|UCC, a client's fingerprints from the exchange's download
	cUCCDlFile  = "uccdlfile"   // key: EXCHANGE, the last download loaded
	uccDlMaxAge = 45            // days after which a download is ignored
)

// uccDownloadSpecs are the download layouts, by exchange.
var uccDownloadSpecs = map[string]string{"nse-ucc-download": "NSE", "bse-ucc-download": "BSE", "mcx-ucc-download": "MCX"}

// uccFieldNames are the fields compared, in order, as people read them.
var (
	uccFieldOrder = []string{"pan", "mobile", "email", "bank", "address"}
	uccFieldNames = map[string]string{"pan": "PAN", "mobile": "mobile", "email": "e-mail", "bank": "bank account", "address": "address"}
)

// UCCDownloadRec is one client as an exchange's download had it: a
// fingerprint and a masked tail per field present in the file.
type UCCDownloadRec struct {
	Exchange string            `json:"exchange"`
	UCC      string            `json:"ucc"`
	Date     string            `json:"date"` // the download's date
	File     string            `json:"file"` // the file's name
	Prints   map[string]string `json:"prints"`
	Tails    map[string]string `json:"tails"`
}

// UCCDownloadFile is the last download of an exchange.
type UCCDownloadFile struct {
	Exchange string   `json:"exchange"`
	File     string   `json:"file"`
	Date     string   `json:"date"`
	Clients  int      `json:"clients"`
	Fields   []string `json:"fields"` // the fields the file had
	At       string   `json:"at"`
	By       string   `json:"by"`
}

// uccFieldPrint is a value's fingerprint: a hash of the field's name and
// its normalised value ("" for an empty value).
func uccFieldPrint(field, norm string) string {
	if norm == "" {
		return ""
	}
	h := sha256.Sum256([]byte("ledgerline-ucc|" + field + "|" + norm))
	return hex.EncodeToString(h[:12])
}

var reNotAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// uccNorm normalises a field's value for comparing.
func uccNorm(field string, v ...string) string {
	switch field {
	case "pan":
		return strings.ToUpper(strings.TrimSpace(v[0]))
	case "mobile":
		d := digitsOf(v[0])
		if len(d) > 10 {
			d = d[len(d)-10:]
		}
		return d
	case "email":
		return strings.ToLower(strings.TrimSpace(v[0]))
	case "bank":
		acct := strings.TrimLeft(digitsOrUpper(v[0]), "0")
		if acct == "" {
			return ""
		}
		return acct + "|" + strings.ToUpper(strings.TrimSpace(v[1]))
	case "address":
		return reNotAlnum.ReplaceAllString(strings.ToLower(strings.Join(v, " ")), "")
	}
	return ""
}

// digitsOrUpper keeps an account number's letters and digits (a few banks
// have letters in account numbers).
func digitsOrUpper(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		if unicode.IsDigit(r) || (r >= 'A' && r <= 'Z') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// uccTail is a masked tail of a value, for the screen.
func uccTail(field string, v ...string) string {
	last := func(s string, n int) string {
		if len(s) <= n {
			return s
		}
		return "…" + s[len(s)-n:]
	}
	switch field {
	case "pan":
		p := strings.ToUpper(strings.TrimSpace(v[0]))
		return last(p, 2)
	case "mobile":
		return last(digitsOf(v[0]), 3)
	case "email":
		e := strings.ToLower(strings.TrimSpace(v[0]))
		if at := strings.LastIndex(e, "@"); at > 0 {
			return e[:1] + "…@" + e[at+1:]
		}
		return last(e, 2)
	case "bank":
		return last(digitsOrUpper(v[0]), 4)
	case "address":
		return "PIN " + last(digitsOf(v[len(v)-1]), 2)
	}
	return ""
}

// uccValues are a field's raw values: from a download row, or from the
// client and its UCC details.
type uccValues map[string][]string

func uccFromRow(r xfile.Row) uccValues {
	g := func(k string) string { return strings.TrimSpace(r.Get(k)) }
	return uccValues{"pan": {g("pan")}, "mobile": {g("mobile")}, "email": {g("email")}, "bank": {g("bank_account"), g("ifsc")},
		"address": {g("address1"), g("address2"), g("address3"), g("pin")}}
}

func uccFromClient(c Client, d UCCDetails) uccValues {
	return uccValues{"pan": {c.PAN}, "mobile": {c.Mobile}, "email": {c.Email}, "bank": {c.Bank.Account, c.Bank.IFSC},
		"address": {d.Address1, d.Address2, d.Address3, d.PIN}}
}

// prints are each field's fingerprint ("" when empty).
func (v uccValues) prints() map[string]string {
	out := map[string]string{}
	for _, f := range uccFieldOrder {
		out[f] = uccFieldPrint(f, uccNorm(f, v[f]...))
	}
	return out
}

func (v uccValues) tails() map[string]string {
	out := map[string]string{}
	for _, f := range uccFieldOrder {
		if uccNorm(f, v[f]...) != "" {
			out[f] = uccTail(f, v[f]...)
		}
	}
	return out
}

// uccClientPrints are a client's fingerprints as Ledgerline has the client.
func uccClientPrints(c Client, d UCCDetails) map[string]string {
	return uccFromClient(c, d).prints()
}

var reNameDate = regexp.MustCompile(`(\d{8})`)

// uccDlDate is the date in a download's name (YYYYMMDD or DDMMYYYY), or
// today.
func uccDlDate(name, today string) string {
	for _, m := range reNameDate.FindAllString(name, -1) {
		for _, d := range []string{m[:4] + "-" + m[4:6] + "-" + m[6:], m[4:] + "-" + m[2:4] + "-" + m[:2]} {
			if validDate(d) && d <= today {
				return d
			}
		}
	}
	return today
}

// isUCCDownload says whether a layout is an exchange's UCC download.
func isUCCDownload(id string) bool { return uccDownloadSpecs[id] != "" }

// loadUCCDownloadXFile keeps an exchange's UCC download, dropped on
// Exchange files, as fingerprints. ok is false for other files.
func (o *Ops) loadUCCDownloadXFile(ctx context.Context, tenant, actor string, s *xfile.Spec, f xfile.File, t *xfile.Table, r *XFileResult) bool {
	ex := uccDownloadSpecs[s.ID]
	if ex == "" {
		return false
	}
	if err := o.require(ctx, tenant, actor, "Loading the exchanges' UCC downloads", RoleMaker, RoleChecker, RoleCompliance); err != nil {
		r.Loaded = "Not loaded: " + err.Error()
		return true
	}
	if t.Header == nil {
		r.Loaded = "Not loaded: " + s.NoHeaderMessage()
		return true
	}
	// The fields the file has: a column that is not there is not compared.
	has := map[string]bool{}
	for _, c := range s.Columns {
		for _, h := range t.Header {
			if c.Key != "" && headerIs(c, h) {
				has[c.Key] = true
			}
		}
	}
	fields := []string{}
	for _, fd := range uccFieldOrder {
		ok := false
		switch fd {
		case "bank":
			ok = has["bank_account"]
		case "address":
			ok = has["address1"] || has["pin"]
		default:
			ok = has[fd]
		}
		if ok {
			fields = append(fields, fd)
		}
	}
	today := o.now().In(ist).Format("2006-01-02")
	date := uccDlDate(f.Name, today)
	seen := map[string]bool{}
	n := 0
	for _, row := range dataRows(t) {
		ucc := strings.ToUpper(strings.TrimSpace(row.Get("ucc")))
		if ucc == "" || seen[ucc] {
			continue // a segment-wise download lists a client once per segment
		}
		seen[ucc] = true
		v := uccFromRow(row)
		rec := UCCDownloadRec{Exchange: ex, UCC: ucc, Date: date, File: f.Name, Prints: map[string]string{}, Tails: map[string]string{}}
		p, tl := v.prints(), v.tails()
		for _, fd := range fields {
			rec.Prints[fd] = p[fd]
			if tl[fd] != "" {
				rec.Tails[fd] = tl[fd]
			}
		}
		if err := docs.Upsert(ctx, o.Docs, tenant, cUCCDl, ex+"|"+ucc, rec, actor); err != nil {
			r.Loaded = "Not loaded: " + err.Error()
			return true
		}
		n++
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cUCCDlFile, ex, UCCDownloadFile{Exchange: ex, File: f.Name, Date: date, Clients: n, Fields: fields, At: o.now().Format(time.RFC3339), By: actor}, actor)
	o.log(ctx, tenant, actor, "ucc.download_loaded", ex, map[string]any{"file": f.Name, "date": date, "clients": n, "fields": fields})
	r.Loaded, r.Added = fmt.Sprintf("Kept as fingerprints of %s's UCC records (the file itself is not kept); compared by the self-inspection", ex), n
	names := []string{}
	for _, fd := range fields {
		names = append(names, uccFieldNames[fd])
	}
	r.Note = fmt.Sprintf("Fields compared: %s. Dated %s; ignored after %d days.", strings.Join(names, ", "), fmtDay(date), uccDlMaxAge)
	if len(fields) == 0 {
		r.Note = "No column of the file was recognised as PAN, mobile, e-mail, bank account or address: nothing can be compared. Add the file's header texts to config/formats/" + s.ID + ".json."
	}
	return true
}

// headerIs says whether a header text is a column's name or alias, as the
// layout compares them (letters and digits, any case).
func headerIs(c xfile.Column, h string) bool {
	n := func(s string) string { return reNotAlnum.ReplaceAllString(strings.ToLower(s), "") }
	want := n(h)
	if want == "" {
		return false
	}
	for _, a := range append([]string{c.Name}, c.Aliases...) {
		if n(a) == want {
			return true
		}
	}
	return false
}

// uccDlStub is what is kept in place of a download's content.
func uccDlStub(name string) []byte {
	return []byte("The exchange's UCC download " + name + " is not kept: only fingerprints of each client's details are (ops/uccfields.go).\n")
}

// ---- the self-inspection test ----

func siUCCFields(e *siEnv) (siOut, error) {
	recs, err := docs.List[UCCDownloadRec](e.ctx, e.o.Docs, e.tenant, cUCCDl, "")
	if err != nil {
		return siOut{}, err
	}
	regs, err := docs.List[NSEUCIReg](e.ctx, e.o.Docs, e.tenant, cUCCReg, "NSE|")
	if err != nil {
		return siOut{}, err
	}
	from := addDays(e.date, -uccDlMaxAge)
	fresh := recs[:0]
	dates := map[string]string{}
	for _, r := range recs {
		if r.Date >= from && r.Date <= e.date {
			fresh = append(fresh, r)
			if r.Date > dates[r.Exchange] {
				dates[r.Exchange] = r.Date
			}
		}
	}
	accepted := 0
	for _, r := range regs {
		if len(r.FieldPrints) > 0 {
			accepted++
		}
	}
	if len(fresh) == 0 && accepted == 0 {
		return siSkip(fmt.Sprintf("Waiting for data: no UCC download from the last %d days (drop NSE's, BSE's or MCX's UCC download on Exchange files, with nse_ucc_download, bse_ucc_download or mcx_ucc_download in its name) and no client accepted by NSE's UCC API since this check began.", uccDlMaxAge))
	}
	var s siOut
	for _, ex := range sortedKeys(dates) {
		s.input(ex+" UCC download", dates[ex])
	}
	if accepted > 0 {
		s.input("NSE UCC API acceptances", e.date)
	}
	details := e.o.uccSources(e.ctx, e.tenant).details
	byClient := map[string][]UCCDownloadRec{}
	for _, r := range fresh {
		byClient[r.UCC] = append(byClient[r.UCC], r)
	}
	regBy := map[string]NSEUCIReg{}
	for _, r := range regs {
		regBy[r.UCC] = r
	}
	fieldCount := map[string]int{}
	changed := 0
	for _, u := range sortedKeys(e.clients) {
		c := e.clients[u]
		if c.Status != "Active" || c.DPOnly {
			continue
		}
		mine := uccClientPrints(c, details[u])
		rs := byClient[u]
		reg, hasReg := regBy[u]
		if len(rs) == 0 && (!hasReg || len(reg.FieldPrints) == 0) {
			continue
		}
		s.checked++
		var why []string
		sort.Slice(rs, func(i, j int) bool { return rs[i].Exchange < rs[j].Exchange })
		for _, r := range rs {
			var diff []string
			for _, f := range uccFieldOrder {
				theirs, ok := r.Prints[f]
				if !ok || (theirs == "" && mine[f] == "") || theirs == mine[f] {
					continue
				}
				fieldCount[f]++
				switch {
				case theirs == "":
					diff = append(diff, uccFieldNames[f]+" blank at the exchange")
				case mine[f] == "":
					diff = append(diff, uccFieldNames[f]+" missing in Ledgerline (exchange "+r.Tails[f]+")")
				default:
					diff = append(diff, uccFieldNames[f]+" differs (exchange "+r.Tails[f]+")")
				}
			}
			if len(diff) > 0 {
				why = append(why, r.Exchange+": "+strings.Join(diff, ", "))
			}
		}
		if hasReg && len(reg.FieldPrints) > 0 {
			var diff []string
			for _, f := range uccFieldOrder {
				if p, ok := reg.FieldPrints[f]; ok && p != mine[f] {
					diff = append(diff, uccFieldNames[f])
				}
			}
			if len(diff) > 0 {
				changed++
				why = append(why, fmt.Sprintf("changed since NSE's UCC API accepted it on %s (%s), not sent again", fmtDay(dayOf(reg.FieldPrintsAt)), strings.Join(diff, ", ")))
			}
		}
		if len(why) > 0 {
			s.bad(siClient(u) + " · " + strings.Join(why, "; "))
		}
	}
	var counts []string
	for _, f := range uccFieldOrder {
		if fieldCount[f] > 0 {
			counts = append(counts, fmt.Sprintf("%s %d", uccFieldNames[f], fieldCount[f]))
		}
	}
	s.summary = fmt.Sprintf("%d active clients compared with the exchanges' UCC records; %d differ", s.checked, s.found)
	if len(counts) > 0 {
		s.summary += " (" + strings.Join(counts, ", ") + ")"
	}
	if changed > 0 {
		s.summary += fmt.Sprintf("; %d changed since NSE accepted them and not sent again", changed)
	}
	s.summary += "."
	return s, nil
}
