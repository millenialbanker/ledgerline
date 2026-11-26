package ops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode"

	"ledgerline/internal/money"
)

// ---- CDSL's and MCX's surveillance alerts, loaded from their systems ----
//
// CDSL gives its DPs two lists on CDAS: the surveillance alerts (CDAS
// Surveillance) and the FIU alerts (CDAS Surveillance FIU Inquiry /
// Download). The CDSL Master Circular for DPs (CDSL/OPS/DP/POLCY/2026/257,
// 14.5.8-14.5.12) has the Designated Director and the Principal Officer
// make sure the FIU alerts are downloaded periodically and analysed, and an
// STR on one cites the CDSL alert. MCX sends members its transactional
// alerts on MRS (MCX/S&I/251/2026 para 16). Ledgerline loads each as alerts
// with a 45-day clock (60 for CDSL's half-yearly multiple-account alerts)
// and logs every download, or every day the firm looked and found none.
//
// Neither depository nor exchange has published the download layout to
// Ledgerline, so the loader is tolerant and the layouts are unconfirmed: it
// takes a CSV, tilde-, pipe-, semicolon- or tab-separated file with a row
// of headings, finds the columns by the names such files are known to use
// (alias lists below), and keeps the whole row in the alert's detail. A
// file without a row of headings it can read is refused with the names it
// looks for. When CDSL or MCX publishes the layout, it becomes an exchange
// file format like the others.

// survImportKind is one kind of alert file.
type survImportKind struct {
	Kind, Source, Area, Title string
	FIU                       bool
	Severity                  string
}

var survImportKinds = map[string]survImportKind{
	"cdsl-surv": {Kind: "cdsl-surv", Source: "CDSL", Area: AreaDP, Title: "CDSL surveillance alerts (CDAS Surveillance)", Severity: "Medium"},
	"cdsl-fiu":  {Kind: "cdsl-fiu", Source: "CDSL", Area: AreaDP, Title: "CDSL FIU alerts (CDAS Surveillance FIU Inquiry / Download)", FIU: true, Severity: "High"},
	"mcx-mrs":   {Kind: "mcx-mrs", Source: "MCX", Title: "MCX transactional alerts (MRS)", Severity: "Medium"},
}

// survAliases are the headings each field is found by, compared without
// case, spaces or punctuation.
var survAliases = map[string][]string{
	"ref":    {"alertid", "alertno", "alertnumber", "alertrefno", "alertreferenceno", "alertreferencenumber", "referenceno", "refno", "alertseqno", "alertsequenceno", "uniquealertid"},
	"date":   {"alertdate", "alertgenerationdate", "alertgenerateddate", "generationdate", "generatedon", "dateofalert", "alertgeneratedon", "reportdate", "date"},
	"type":   {"alerttype", "alertname", "typeofalert", "alertcategory", "theme", "alertcode", "category", "scenario", "alertscenario", "rulename"},
	"bo":     {"boid", "bo", "beneficiaryownerid", "beneficiaryid", "boacno", "boaccountno", "dpclientid", "demataccount", "dematacno", "clientid", "bnfclownrid"},
	"ucc":    {"ucc", "clientcode", "uniqueclientcode", "tradingcode", "clientcd"},
	"pan":    {"pan", "panno", "pannumber", "firstholderpan"},
	"name":   {"boname", "clientname", "name", "holdername", "firstholdername", "accountholdername"},
	"isin":   {"isin", "isincode"},
	"sec":    {"securityname", "isinname", "scripname", "scrip", "symbol", "contract", "commodity", "securitydescription"},
	"qty":    {"quantity", "qty", "totalquantity", "txnqty", "transactionquantity"},
	"value":  {"value", "amount", "totalvalue", "txnvalue", "transactionvalue", "valueinrs", "valuers", "turnover"},
	"from":   {"fromdate", "periodfrom", "txnfromdate", "transactionfromdate", "startdate", "transactiondate", "txndate", "tradedate", "executiondate"},
	"to":     {"todate", "periodto", "txntodate", "transactiontodate", "enddate"},
	"due":    {"duedate", "responseduedate", "lastdatetorespond", "lastdateforresponse", "replybydate"},
	"detail": {"description", "alertdescription", "remarks", "details", "narration", "observation", "reason", "alertreason"},
	"ctr":    {"counterboid", "targetboid", "ctrboid", "counterpartyboid", "cntrbnfclownrid", "targetaccount", "counterparty"},
	"freq":   {"frequency", "alertfrequency", "periodicity"},
}

func survHead(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// survDelim is the file's separator: the candidate seen most often on a
// line among the first ten (a title line above the headings has none).
func survDelim(data []byte) rune {
	best, n := ',', 0
	for i, l := range strings.SplitN(string(data), "\n", 11) {
		if i == 10 {
			break
		}
		for _, d := range []rune{',', '~', '|', '\t', ';'} {
			if c := strings.Count(l, string(d)); c > n {
				best, n = d, c
			}
		}
	}
	return best
}

// survMap finds the row of headings within the first lines and which
// column each field is in.
func survMap(recs [][]string) (int, map[string]int) {
	for i := 0; i < len(recs) && i < 10; i++ {
		m := map[string]int{}
		for j, h := range recs[i] {
			k := survHead(h)
			for f, as := range survAliases {
				if _, done := m[f]; done {
					continue
				}
				for _, a := range as {
					if k == a {
						m[f] = j
						break
					}
				}
			}
		}
		_, ref := m["ref"]
		_, bo := m["bo"]
		_, ucc := m["ucc"]
		_, pan := m["pan"]
		if len(m) >= 2 && (ref || bo || ucc || pan) {
			return i, m
		}
	}
	return -1, nil
}

// SurvImportResult is what a load did.
type SurvImportResult struct {
	Download SurvDownload `json:"download"`
	Alerts   []string     `json:"alerts"`
	// Unmatched lists rows whose account or client is not on record (they
	// are still raised, without a client).
	Unmatched []string `json:"unmatched"`
}

// ImportSurvAlerts loads a CDSL surveillance, CDSL FIU or MCX MRS alert file.
func (o *Ops) ImportSurvAlerts(ctx context.Context, tenant, actor, kind, name string, data []byte) (SurvImportResult, error) {
	if err := o.require(ctx, tenant, actor, "Loading surveillance alerts", RoleCompliance, RoleChecker); err != nil {
		return SurvImportResult{}, err
	}
	k, ok := survImportKinds[kind]
	if !ok {
		return SurvImportResult{}, invalid("unknown alert source %q (cdsl-surv, cdsl-fiu or mcx-mrs)", kind)
	}
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if len(bytes.TrimSpace(data)) == 0 {
		return SurvImportResult{}, invalid("the file is empty: if there were no alerts, record a nil download instead")
	}
	r := csv.NewReader(bytes.NewReader(data))
	r.Comma, r.FieldsPerRecord, r.LazyQuotes, r.TrimLeadingSpace = survDelim(data), -1, true, true
	recs, err := r.ReadAll()
	if err != nil {
		return SurvImportResult{}, invalid("the file could not be read: %v", err)
	}
	hi, col := survMap(recs)
	if hi < 0 {
		return SurvImportResult{}, invalid("no row of headings found: Ledgerline looks for an alert number (Alert ID, Alert No, Reference No) or the account (BO ID, Client Code, PAN) and at least one more column such as Alert Date, Alert Type, ISIN, Quantity or Value")
	}
	if kind == "mcx-mrs" {
		// MCX names clients by their client code: a "Client ID" column is
		// the UCC there, not a demat account.
		if j, ok := col["bo"]; ok {
			if _, has := col["ucc"]; !has {
				col["ucc"] = j
			}
			delete(col, "bo")
		}
		if _, ok := col["ucc"]; !ok {
			if _, ok := col["pan"]; !ok {
				return SurvImportResult{}, invalid("an MCX alert file needs the client code (UCC or Client Code) or PAN column")
			}
		}
	}
	get := func(rec []string, f string) string {
		if j, ok := col[f]; ok && j < len(rec) {
			return strings.TrimSpace(rec[j])
		}
		return ""
	}
	byBO := o.boClients(ctx, tenant)
	cs, _ := o.ClientMap(ctx, tenant)
	byPAN := map[string]string{}
	for _, u := range sortedKeys(cs) {
		if p := strings.ToUpper(cs[u].PAN); p != "" {
			if _, taken := byPAN[p]; !taken {
				byPAN[p] = u
			}
		}
	}
	sum := sha256.Sum256(data)
	rf, err := o.putRaw(ctx, tenant, "SURV-"+strings.ToUpper(kind), name, data)
	if err != nil {
		return SurvImportResult{}, err
	}
	res := SurvImportResult{Alerts: []string{}, Unmatched: []string{}}
	dl := SurvDownload{Kind: kind, Title: k.Title, File: name, SHA256: hex.EncodeToString(sum[:]), FileID: rf.ID}
	heads := recs[hi]
	today := o.today()
	for li, rec := range recs[hi+1:] {
		if strings.TrimSpace(strings.Join(rec, "")) == "" {
			continue
		}
		dl.Rows++
		ref := get(rec, "ref")
		bo := normDematID(get(rec, "bo"))
		ucc := strings.ToUpper(get(rec, "ucc"))
		if u, ok := byBO[bo]; ok && bo != "" {
			ucc = u
		}
		if _, ok := cs[ucc]; !ok {
			ucc = ""
			if u, ok := byPAN[strings.ToUpper(get(rec, "pan"))]; ok {
				ucc = u
			}
		}
		date := firstOf(anyDate(get(rec, "date")), today)
		typ := firstOf(get(rec, "type"), "Alert")
		a := Alert{Source: k.Source, Area: k.Area, Ref: ref, Date: date, UCC: ucc, Severity: k.Severity, FIU: k.FIU, BOID: bo,
			ISIN: strings.ToUpper(get(rec, "isin")), Qty: get(rec, "qty"), TxnFrom: anyDate(get(rec, "from")), TxnTo: anyDate(get(rec, "to"))}
		if v := strings.ReplaceAll(get(rec, "value"), ",", ""); v != "" {
			if d, err := money.ParseDec(v); err == nil {
				a.Value = d.Fixed(2)
			}
		}
		if a.TxnTo == "" {
			a.TxnTo = a.TxnFrom
		}
		lt := strings.ToLower(typ + " " + get(rec, "freq"))
		a.HalfYearly = k.Source == "CDSL" && (strings.Contains(lt, "half") || strings.Contains(lt, "multiple account") || strings.Contains(lt, "multiple demat"))
		a.Type = map[bool]string{true: "CDSL FIU alert: ", false: map[string]string{"CDSL": "CDSL alert: ", "MCX": "MCX alert: "}[k.Source]}[k.FIU] + typ
		if due := anyDate(get(rec, "due")); due != "" {
			a.Due = due // the earlier of this and the 45/60 days (raiseAlert)
		}
		// The whole row, as the source wrote it, so nothing is lost.
		var parts []string
		for j, v := range rec {
			if v = strings.TrimSpace(v); v != "" && j < len(heads) {
				h := strings.TrimSpace(heads[j])
				if f := survHead(h); f == "pan" || f == "panno" || f == "firstholderpan" {
					v = MaskPAN(strings.ToUpper(v))
				}
				if jb, has := col["bo"]; has && j == jb && bo != "" {
					v = MaskAccount(bo)
				}
				parts = append(parts, h+": "+v)
			}
		}
		a.Detail = clip(strings.Join(parts, "; "), 2000)
		if ucc == "" {
			dl.Unmatched++
			res.Unmatched = append(res.Unmatched, fmt.Sprintf("line %d: %s", hi+li+2, firstOf(ref, typ)))
		}
		key := kind + ":" + ref
		if ref == "" {
			h := sha256.Sum256([]byte(strings.Join(rec, "\x1f")))
			key = kind + ":row-" + hex.EncodeToString(h[:6])
		}
		if o.raiseAlert(ctx, tenant, actor, a, key) {
			dl.Added++
			res.Alerts = append(res.Alerts, "A-"+strings.NewReplacer(":", "-", "|", "-", " ", "").Replace(key))
		} else {
			dl.Duplicates++
		}
	}
	if dl.Rows == 0 {
		dl.Nil, dl.Note = true, "The file had headings and no alerts"
	}
	o.closeDownloadWork(ctx, tenant, actor, kind)
	res.Download, err = o.logSurvDownload(ctx, tenant, actor, dl)
	return res, err
}
