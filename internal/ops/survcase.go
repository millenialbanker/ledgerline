package ops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
)

// ---- working a surveillance alert: due dates, the checklist, documents,
// adverse observations, the compliance officer's sample of closures ----
//
// What the rules ask, as the firm's compliance gap report of 23 Nov 2026
// summarises them (the circular texts are not kept in Ledgerline):
//
//   - CDSL/DP/SURV/2026/521 (communique DP2026-521 of 31 Jul 2026, the
//     comprehensive surveillance framework for DPs). Part I D: a register of
//     every DP surveillance alert, each with the transaction's rationale, the
//     demat statement and supporting documents checked and the observations
//     recorded. Part I E: before a CDSL alert is answered, a nine-point
//     checklist is worked (the BO's explanation, the relation of source and
//     target BOs, the purpose, the documents verified, the BO's response
//     evaluated, the DP's analysis, whether it is suspicious, the documents
//     attached, the response filed in time). Alerts are disposed of within
//     45 days of generation, 60 days for the half-yearly multiple-account
//     alerts. Part I I.2-I.7: the DP's own analysis of demat transactions
//     identifies adverse observations within two months of the transaction
//     and reports them on the CDSL Surveillance System, with the analysis,
//     rationale and action taken, within 7 days; CDSL's own alerts are not
//     reported again as the DP's. A sample of closures is checked by the
//     compliance officer.
//   - MCX/S&I/251/2026 (MCX master circular on surveillance) para 16: each
//     MCX transactional alert is worked with the client's explanation, bank
//     statements for 15 days either side of the alert and warehouse
//     receipts where relevant; findings are recorded and adverse ones
//     reported to MCX within 45 days.
//
// Where a rule is not precise the stricter reading is taken: the 7 days to
// report an adverse observation are calendar days, and the two months run
// from the last transaction the observation is about.

const (
	cSurvDownload = "survdownload" // key: kind|time: each CDSL or MCX alert file loaded
	cDPSurvSet    = "dpsurvset"    // key: "settings"

	// AreaDP marks an alert of the firm's depository business.
	AreaDP = "DP"

	alertDisposeDays     = 45 // CDSL DP2026-521; NSE/SURV/48818; MCX/S&I/251/2026 para 16
	alertDisposeDaysHalf = 60 // CDSL's half-yearly multiple-account alerts
	adverseReportDays    = 7  // CDSL DP2026-521: adverse observations reported within 7 days
	ownObsMonths         = 2  // CDSL DP2026-521 I.2: identified within two months of the transaction
	checkInTime          = "in_time"
	checkAttached        = "attached"
	checkNA              = "Not applicable"
	checkDone            = "Done"
)

// AlertCheck is one point of an alert's checklist.
type AlertCheck struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	State string `json:"state,omitempty"` // "", Done, Not applicable
	Note  string `json:"note,omitempty"`
	By    string `json:"by,omitempty"`
	At    string `json:"at,omitempty"`
}

// AlertDoc is a document kept with an alert.
type AlertDoc struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ContentType string `json:"content_type"`
	SHA256      string `json:"sha256"`
	FileID      string `json:"file_id"`
	By          string `json:"by"`
	At          string `json:"at"`
}

// AlertAdverse is an adverse observation on an alert and its report to the
// depository or exchange.
type AlertAdverse struct {
	DecidedOn   string `json:"decided_on"`
	DecidedBy   string `json:"decided_by"`
	Observation string `json:"observation"`
	To          string `json:"to"`  // CDSL, MCX, NSE or BSE
	Due         string `json:"due"` // reported by
	// Late is why an own observation was identified more than two months
	// after the transaction.
	Late       string `json:"late,omitempty"`
	ReportedOn string `json:"reported_on,omitempty"`
	ReportRef  string `json:"report_ref,omitempty"`
	ReportedBy string `json:"reported_by,omitempty"`
}

// AlertSample is the compliance officer's check of a closed alert.
type AlertSample struct {
	Quarter string `json:"quarter"`
	Result  string `json:"result"` // Agree, Disagree
	Note    string `json:"note,omitempty"`
	By      string `json:"by"`
	On      string `json:"on"`
}

// alertDue is the day an alert must be disposed of by.
func alertDue(a Alert) string {
	if !validDate(a.Date) {
		return ""
	}
	if a.HalfYearly {
		return addDays(a.Date, alertDisposeDaysHalf)
	}
	return addDays(a.Date, alertDisposeDays)
}

// alertDueOf is an alert's due date, worked out for alerts raised before
// due dates were kept.
func alertDueOf(a Alert) string {
	if a.Due != "" {
		return a.Due
	}
	if a.Source == "CDSL" {
		a.Area = AreaDP
	}
	return alertDue(a)
}

// cdslChecklist is CDSL DP2026-521 Part I E, in its order.
var cdslChecklist = [][2]string{
	{"bo_explanation", "The BO's explanation of the transaction obtained"},
	{"relationship", "The relation between the source and target BOs established"},
	{"purpose", "The purpose of the transaction ascertained"},
	{"documents_verified", "Supporting documents obtained and verified (account opening form, correspondence, gift deed, sale agreement, bank statement)"},
	{"response_evaluated", "The BO's response evaluated against the KYC, income and the account's history"},
	{"analysis", "The DP's analysis recorded"},
	{"suspicion", "Whether it is suspicious assessed (adverse observation, STR)"},
	{checkAttached, "The documents attached to the alert"},
	{checkInTime, "The response filed on CDSL's surveillance system within the due date"},
}

// mcxChecklist is MCX/S&I/251/2026 para 16.
var mcxChecklist = [][2]string{
	{"client_explanation", "The client's explanation sought and recorded"},
	{"bank_statements", "The client's bank statements for 15 days before and after the alert obtained"},
	{"warehouse_receipts", "Warehouse receipts obtained (commodities delivered)"},
	{"analysis", "The findings recorded"},
	{checkAttached, "The documents attached to the alert"},
	{checkInTime, "Disposed of, and any adverse finding reported to MCX, within 45 days"},
}

// checklistFor is the checklist an alert is worked with: CDSL's for the
// DP's alerts, MCX's for MCX's; none for the others.
func checklistFor(a Alert) []AlertCheck {
	var src [][2]string
	switch {
	case a.Area == AreaDP || a.Source == "CDSL":
		src = cdslChecklist
	case a.Source == "MCX":
		src = mcxChecklist
	default:
		return nil
	}
	out := make([]AlertCheck, len(src))
	for i, x := range src {
		out[i] = AlertCheck{Key: x[0], Label: x[1]}
	}
	return out
}

// checklistGaps lists the points still to work (the last, filing in time,
// is recorded when the alert is answered).
func checklistGaps(a Alert) []string {
	var out []string
	for _, c := range a.Checklist {
		switch {
		case c.Key == checkInTime:
		case c.State == "":
			out = append(out, c.Label)
		case c.Key == checkAttached && c.State == checkDone && len(a.Attachments) == 0:
			out = append(out, "attach the documents")
		}
	}
	return out
}

// markInTime records the last point when the alert is answered.
func markInTime(a *Alert, day, note string) {
	due := alertDueOf(*a)
	for i := range a.Checklist {
		if a.Checklist[i].Key != checkInTime {
			continue
		}
		c := &a.Checklist[i]
		c.State, c.At = checkDone, day
		if due != "" && day > due {
			c.Note = fmt.Sprintf("Answered on %s, %d days after the due date %s: %s", day, daysBetween(due, day), due, strings.TrimSpace(note))
		} else {
			c.Note = "Answered on " + day + ", due " + due
		}
	}
}

func (o *Ops) caseAlert(ctx context.Context, tenant, id string) (Alert, error) {
	a, _, err := docs.Get[Alert](ctx, o.Docs, tenant, cAlert, id)
	return a, err
}

// SaveAlertChecklist records points of an alert's checklist. A point not
// applicable says why; "documents attached" needs a document.
func (o *Ops) SaveAlertChecklist(ctx context.Context, tenant, actor, id string, items []AlertCheck) (Alert, error) {
	if err := o.require(ctx, tenant, actor, "Working an alert's checklist", RoleCompliance, RoleChecker); err != nil {
		return Alert{}, err
	}
	now := o.now().Format(time.RFC3339)
	a, err := docs.Update(ctx, o.Docs, tenant, cAlert, id, actor, func(x *Alert) error {
		if x.Status == "Closed" || x.Status == "STR filed" {
			return fmt.Errorf("%w: alert is already %s", ErrState, x.Status)
		}
		if len(x.Checklist) == 0 {
			x.Checklist = checklistFor(*x)
		}
		if len(x.Checklist) == 0 {
			return invalid("this alert has no checklist: record the review in its notes")
		}
		at := map[string]int{}
		for i, c := range x.Checklist {
			at[c.Key] = i
		}
		for _, in := range items {
			i, ok := at[in.Key]
			if !ok {
				return invalid("unknown checklist point %q", in.Key)
			}
			if in.Key == checkInTime {
				continue // recorded when the alert is answered
			}
			st, note := strings.TrimSpace(in.State), strings.TrimSpace(in.Note)
			switch st {
			case "", checkDone:
			case checkNA:
				if note == "" {
					return invalid("say why %q does not apply", x.Checklist[i].Label)
				}
			default:
				return invalid("a checklist point is Done, Not applicable or blank")
			}
			if st == checkDone && in.Key == checkAttached && len(x.Attachments) == 0 {
				return invalid("attach the documents to the alert first")
			}
			c := &x.Checklist[i]
			if c.State == st && c.Note == note {
				continue
			}
			c.State, c.Note, c.By, c.At = st, note, actor, now
			if st == "" {
				c.By, c.At = "", ""
			}
		}
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "alert.checklist", id, map[string]any{"points": len(items)})
	}
	return a, err
}

// AddAlertDoc keeps a document (PDF, JPEG or PNG) with an alert.
func (o *Ops) AddAlertDoc(ctx context.Context, tenant, actor, id, name string, data []byte) (Alert, error) {
	if err := o.require(ctx, tenant, actor, "Adding documents to an alert", RoleCompliance, RoleChecker); err != nil {
		return Alert{}, err
	}
	if _, err := o.caseAlert(ctx, tenant, id); err != nil {
		return Alert{}, err
	}
	if len(data) == 0 || len(data) > MaxDocBytes {
		return Alert{}, invalid("files must be between 1 byte and 5 MB")
	}
	ct := detectDoc(data)
	if ct == "" {
		return Alert{}, invalid("only PDF, JPEG and PNG files are accepted")
	}
	rf, err := o.putRaw(ctx, tenant, "ALERT-DOC", name, data)
	if err != nil {
		return Alert{}, err
	}
	n, err := o.next(ctx, tenant, "alert-doc", actor)
	if err != nil {
		return Alert{}, err
	}
	d := AlertDoc{ID: fmt.Sprintf("AD-%06d", n), Name: firstOf(strings.TrimSpace(name), "document"), ContentType: ct, SHA256: rf.SHA256, FileID: rf.ID, By: actor, At: o.now().Format(time.RFC3339)}
	a, err := docs.Update(ctx, o.Docs, tenant, cAlert, id, actor, func(x *Alert) error {
		x.Attachments = append(x.Attachments, d)
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "alert.document", id, map[string]any{"doc": d.ID, "sha256": d.SHA256})
	}
	return a, err
}

// AlertDocFile is a document kept with an alert.
func (o *Ops) AlertDocFile(ctx context.Context, tenant, actor, id, doc string) (AlertDoc, []byte, error) {
	if err := o.require(ctx, tenant, actor, "Seeing an alert's documents", RoleCompliance, RoleChecker); err != nil {
		return AlertDoc{}, nil, err
	}
	a, err := o.caseAlert(ctx, tenant, id)
	if err != nil {
		return AlertDoc{}, nil, err
	}
	for _, d := range a.Attachments {
		if d.ID == doc {
			rf, err := o.App.Ingest.Raw.Get(ctx, tenant, d.FileID)
			if err != nil {
				return d, nil, err
			}
			o.log(ctx, tenant, actor, "alert.document.viewed", id, map[string]any{"doc": doc})
			return d, rf.Content, nil
		}
	}
	return AlertDoc{}, nil, docs.ErrNotFound
}

// adverseTo is who an adverse observation on the alert is reported to.
func adverseTo(a Alert) string {
	switch {
	case a.Area == AreaDP:
		return "CDSL"
	case a.Source == "MCX" || a.Source == "BSE" || a.Source == "NSE":
		return a.Source
	}
	return "the exchange"
}

// ownObsDeadline is the last day an own DP observation on the alert's
// transactions may be identified (two months after the last of them).
func ownObsDeadline(a Alert) string {
	if a.Area != AreaDP || a.Source != "Internal" {
		return ""
	}
	last := firstOf(a.TxnTo, a.TxnFrom)
	if !validDate(last) {
		return ""
	}
	return addMonths(last, ownObsMonths)
}

// MarkAdverse records the compliance officer's adverse observation on an
// alert: it is then due on CDSL's surveillance system (or with the
// exchange) within 7 days. CDSL's own alerts are answered as adverse on
// CDSL's system, never reported again as the DP's own observation.
func (o *Ops) MarkAdverse(ctx context.Context, tenant, actor, id, observation, late string) (Alert, error) {
	if err := o.require(ctx, tenant, actor, "Recording an adverse observation", RoleCompliance); err != nil {
		return Alert{}, err
	}
	observation, late = strings.TrimSpace(observation), strings.TrimSpace(late)
	if len(observation) < 20 {
		return Alert{}, invalid("write the observation: the analysis, the rationale and the action taken")
	}
	today := o.now().In(ist).Format("2006-01-02")
	a, err := docs.Update(ctx, o.Docs, tenant, cAlert, id, actor, func(x *Alert) error {
		if x.Status == "Closed" || x.Status == "STR filed" {
			return fmt.Errorf("%w: alert is already %s", ErrState, x.Status)
		}
		if x.Adverse != nil {
			return fmt.Errorf("%w: the adverse observation is already recorded", ErrState)
		}
		if gaps := checklistGaps(*x); len(gaps) > 0 {
			return invalid("work the checklist first: %s", strings.Join(gaps, "; "))
		}
		if dl := ownObsDeadline(*x); dl != "" && today > dl && late == "" {
			return invalid("the transactions are more than two months old (identify by %s): say why the observation is late", dl)
		}
		x.Adverse = &AlertAdverse{DecidedOn: today, DecidedBy: actor, Observation: observation, To: adverseTo(*x), Due: addDays(today, adverseReportDays), Late: late}
		x.Notes = append(x.Notes, actor+": adverse observation: "+observation)
		return nil
	})
	if err != nil {
		return a, err
	}
	o.log(ctx, tenant, actor, "alert.adverse", id, map[string]any{"to": a.Adverse.To, "due": a.Adverse.Due})
	how := "Report it on CDSL's surveillance system as the DP's own observation, with the analysis, rationale and action taken."
	if a.Source == "CDSL" {
		how = "Answer CDSL's alert as adverse on CDSL's surveillance system (do not report it again as the DP's own observation)."
	} else if a.Adverse.To != "CDSL" {
		how = "Report it to " + a.Adverse.To + " with the analysis and the action taken."
	}
	o.raiseWork(ctx, tenant, "survadverse", id, "Report adverse observation on alert "+id+" to "+a.Adverse.To,
		how+" Then record the date and reference on the alert. Due within 7 days of the observation ("+a.Adverse.Due+").", "", RoleCompliance, "surv", a.Adverse.Due)
	return a, nil
}

// RecordAdverseReported records that the adverse observation was reported:
// the alert counts as disposed of (referred) in the quarterly status.
func (o *Ops) RecordAdverseReported(ctx context.Context, tenant, actor, id, date, ref string) (Alert, error) {
	if err := o.require(ctx, tenant, actor, "Recording an adverse report", RoleCompliance); err != nil {
		return Alert{}, err
	}
	ref = strings.TrimSpace(ref)
	if !validDate(date) || ref == "" {
		return Alert{}, invalid("give the date reported and the reference CDSL or the exchange gave")
	}
	a, err := docs.Update(ctx, o.Docs, tenant, cAlert, id, actor, func(x *Alert) error {
		if x.Adverse == nil {
			return fmt.Errorf("%w: record the adverse observation first", ErrState)
		}
		if x.Adverse.ReportedOn != "" {
			return fmt.Errorf("%w: already reported on %s", ErrState, x.Adverse.ReportedOn)
		}
		if date < x.Adverse.DecidedOn || date > o.today() {
			return invalid("the report date is between the observation (%s) and today", x.Adverse.DecidedOn)
		}
		x.Adverse.ReportedOn, x.Adverse.ReportRef, x.Adverse.ReportedBy = date, ref, actor
		markInTime(x, date, "reported as adverse, ref "+ref)
		x.Notes = append(x.Notes, fmt.Sprintf("%s: reported to %s on %s (%s)", actor, x.Adverse.To, date, ref))
		return nil
	})
	if err != nil {
		return a, err
	}
	l, _, _ := docs.GetOr(ctx, o.Docs, tenant, cAlertLife, id, AlertLife{AlertID: id})
	if l.ReferredOn == "" {
		l.ReferredOn, l.Observation, l.By = date, a.Adverse.Observation, actor
		_ = docs.Upsert(ctx, o.Docs, tenant, cAlertLife, id, l, actor)
	}
	o.log(ctx, tenant, actor, "alert.adverse.reported", id, map[string]any{"date": date, "ref": ref})
	o.closeWork(ctx, tenant, actor, "survadverse", id, "reported "+date+" ("+ref+")")
	return a, nil
}

// ---- the compliance officer's sample of closures ----

// AlertSampleView is a quarter's closed alerts and the sample to check.
type AlertSampleView struct {
	Quarter string  `json:"quarter"`
	Closed  int     `json:"closed"`
	Size    int     `json:"size"`
	Checked int     `json:"checked"`
	Sample  []Alert `json:"sample"`
}

// sampleRank orders closures for the sample: a fixed draw by quarter and
// alert, so nobody picks which alerts are checked.
func sampleRank(quarter, id string) string {
	h := sha256.Sum256([]byte(quarter + "|" + id))
	return hex.EncodeToString(h[:8])
}

// AlertSample is the quarter's sample of closed alerts ("": the quarter
// ended before the business date): the larger of the settings' share and
// minimum, drawn from the alerts closed in the quarter.
func (o *Ops) AlertSample(ctx context.Context, tenant, actor, quarter string) (AlertSampleView, error) {
	if err := o.require(ctx, tenant, actor, "Checking alert closures", RoleCompliance); err != nil {
		return AlertSampleView{}, err
	}
	return o.alertSample(ctx, tenant, firstOf(quarter, lastEndedQuarter(o.businessDate(ctx, tenant))))
}

func (o *Ops) alertSample(ctx context.Context, tenant, quarter string) (AlertSampleView, error) {
	from, to, err := survQuarter(quarter)
	if err != nil {
		return AlertSampleView{}, err
	}
	v := AlertSampleView{Quarter: quarter, Sample: []Alert{}}
	as, err := o.Alerts(ctx, tenant)
	if err != nil {
		return v, err
	}
	var closed []Alert
	for _, a := range as {
		if a.Status != "Closed" {
			continue
		}
		on := a.ClosedOn
		if on == "" {
			if l, _, err := docs.Get[AlertLife](ctx, o.Docs, tenant, cAlertLife, a.ID); err == nil {
				on = l.ClosedOn
			}
		}
		if on >= from && on <= to {
			closed = append(closed, a)
		}
	}
	set := o.DPSurvConfig(ctx, tenant)
	v.Closed = len(closed)
	v.Size = int(math.Ceil(float64(len(closed)) * float64(set.SamplePct) / 100))
	if v.Size < set.SampleMin {
		v.Size = set.SampleMin
	}
	if v.Size > len(closed) {
		v.Size = len(closed)
	}
	sort.Slice(closed, func(i, j int) bool { return sampleRank(quarter, closed[i].ID) < sampleRank(quarter, closed[j].ID) })
	v.Sample = closed[:v.Size]
	for _, a := range v.Sample {
		if a.Sample != nil && a.Sample.Quarter == quarter {
			v.Checked++
		}
	}
	return v, nil
}

// RecordAlertSample records the compliance officer's check of a sampled
// closure: Agree, or Disagree with what is to be done.
func (o *Ops) RecordAlertSample(ctx context.Context, tenant, actor, id, quarter, result, note string) (Alert, error) {
	if err := o.require(ctx, tenant, actor, "Checking alert closures", RoleCompliance); err != nil {
		return Alert{}, err
	}
	if result != "Agree" && result != "Disagree" {
		return Alert{}, invalid("the result is Agree or Disagree")
	}
	note = strings.TrimSpace(note)
	if result == "Disagree" && note == "" {
		return Alert{}, invalid("say what is wrong with the closure and what is to be done")
	}
	v, err := o.alertSample(ctx, tenant, quarter)
	if err != nil {
		return Alert{}, err
	}
	in := false
	for _, a := range v.Sample {
		in = in || a.ID == id
	}
	if !in {
		return Alert{}, invalid("alert %s is not in the sample for %s", id, quarter)
	}
	a, err := docs.Update(ctx, o.Docs, tenant, cAlert, id, actor, func(x *Alert) error {
		x.Sample = &AlertSample{Quarter: quarter, Result: result, Note: note, By: actor, On: o.today()}
		return nil
	})
	if err != nil {
		return a, err
	}
	o.log(ctx, tenant, actor, "alert.sample", id, map[string]any{"quarter": quarter, "result": result})
	if v, err := o.alertSample(ctx, tenant, quarter); err == nil && v.Checked >= v.Size {
		o.closeWork(ctx, tenant, actor, "survsample", "q:"+quarter, "sample checked")
	}
	if result == "Disagree" {
		o.raiseWork(ctx, tenant, "survsample", id, "Closure of alert "+id+" questioned in the sample check",
			"The compliance officer did not agree with how alert "+id+" was closed: "+note, "", RoleCompliance, "surv", o.today())
	}
	return a, nil
}

// ---- the download log ----

// SurvDownload is one CDSL or MCX alert file loaded, or a day the firm
// looked and found none.
type SurvDownload struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"` // cdsl-surv, cdsl-fiu, mcx-mrs
	Title      string `json:"title"`
	File       string `json:"file,omitempty"`
	SHA256     string `json:"sha256,omitempty"`
	FileID     string `json:"file_id,omitempty"`
	Rows       int    `json:"rows"`
	Added      int    `json:"added"`
	Duplicates int    `json:"duplicates"`
	Unmatched  int    `json:"unmatched"`
	Nil        bool   `json:"nil,omitempty"`
	Note       string `json:"note,omitempty"`
	By         string `json:"by"`
	At         string `json:"at"`
}

// SurvDownloads lists the log, newest first.
func (o *Ops) SurvDownloads(ctx context.Context, tenant, actor string) ([]SurvDownload, error) {
	if err := o.require(ctx, tenant, actor, "Seeing the alert download log", RoleCompliance, RoleChecker); err != nil {
		return nil, err
	}
	xs, err := docs.List[SurvDownload](ctx, o.Docs, tenant, cSurvDownload, "")
	sort.SliceStable(xs, func(i, j int) bool { return xs[i].At > xs[j].At })
	if xs == nil {
		xs = []SurvDownload{}
	}
	return xs, err
}

func (o *Ops) logSurvDownload(ctx context.Context, tenant, actor string, d SurvDownload) (SurvDownload, error) {
	d.By, d.At = actor, o.now().Format(time.RFC3339)
	n, err := o.next(ctx, tenant, "survdownload", actor)
	if err != nil {
		return d, err
	}
	d.ID = fmt.Sprintf("%s|%s|%06d", d.Kind, o.now().In(ist).Format("20060102"), n)
	if err := docs.Upsert(ctx, o.Docs, tenant, cSurvDownload, d.ID, d, actor); err != nil {
		return d, err
	}
	o.log(ctx, tenant, actor, "surveillance.download", d.ID, map[string]any{"kind": d.Kind, "rows": d.Rows, "added": d.Added, "nil": d.Nil})
	return d, nil
}

// RecordNilDownload records that the firm looked for alerts on CDSL's or
// MCX's system and there were none to download.
func (o *Ops) RecordNilDownload(ctx context.Context, tenant, actor, kind, note string) (SurvDownload, error) {
	if err := o.require(ctx, tenant, actor, "Recording an alert download", RoleCompliance, RoleChecker); err != nil {
		return SurvDownload{}, err
	}
	k, ok := survImportKinds[kind]
	if !ok {
		return SurvDownload{}, invalid("unknown alert source %q", kind)
	}
	o.closeDownloadWork(ctx, tenant, actor, kind)
	return o.logSurvDownload(ctx, tenant, actor, SurvDownload{Kind: kind, Title: k.Title, Nil: true, Note: firstOf(strings.TrimSpace(note), "No alerts to download")})
}

// closeDownloadWork closes the reminder for a list about to be logged.
func (o *Ops) closeDownloadWork(ctx context.Context, tenant, actor, kind string) {
	xs, _ := docs.List[SurvDownload](ctx, o.Docs, tenant, cSurvDownload, "")
	o.closeWork(ctx, tenant, actor, "survdownload", dlWorkKey(kind, lastDownload(xs, kind), o.DPSurvConfig(ctx, tenant).DownloadDays), "downloaded")
}

// lastDownload is the latest day the kind was downloaded ("" never).
func lastDownload(xs []SurvDownload, kind string) string {
	last := ""
	for _, x := range xs {
		if x.Kind == kind && x.At > last {
			last = x.At
		}
	}
	if len(last) >= 10 {
		return last[:10]
	}
	return last
}

// ---- thresholds ----

// DPSurvSettings are the thresholds of the DP's own surveillance rules
// (dpsurv.go) and of the case work. Value and count thresholds are scaled
// by the client's risk category (RiskPct): a high-risk client is watched
// more closely.
type DPSurvSettings struct {
	// Off-market debits and pledges in 30 days above this many times the
	// declared annual income (its range's midpoint).
	IncomeTimes int `json:"income_times"`
	// Changes of mobile, e-mail, address, bank or demat account in a
	// calendar month above this count.
	ModsPerMonth int `json:"mods_per_month"`
	// An off-market or inter-depository debit within this many days of a
	// change of contact, address, bank or demat details.
	AfterModDays int `json:"after_mod_days"`
	// An account with no transaction for this many days is dormant.
	DormantDays int `json:"dormant_days"`
	// One account sending off-market to this many others in 30 days (or
	// receiving from this many of the DP's accounts).
	FanCount int `json:"fan_count"`
	// Clusters: more than this many accounts sharing a name, address or
	// bank account.
	ClusterSize int `json:"cluster_size"`
	// Pass-through: receipts of at least PassMin rupees in PassDays, at
	// least PassPct per cent paid back out, with trades of under TradePct
	// per cent of the receipts.
	PassMin  int64 `json:"pass_min"`
	PassDays int   `json:"pass_days"`
	PassPct  int   `json:"pass_pct"`
	TradePct int   `json:"trade_pct"`
	// Staff trades this many minutes or less before a client trade of at
	// least FrontValue rupees in the same contract and direction.
	FrontMinutes int   `json:"front_minutes"`
	FrontValue   int64 `json:"front_value"`
	// The CDSL alert files are downloaded at least every DownloadDays.
	DownloadDays int `json:"download_days"`
	// The compliance officer checks SamplePct per cent of each quarter's
	// closures, at least SampleMin.
	SamplePct int `json:"sample_pct"`
	SampleMin int `json:"sample_min"`
	// RiskPct scales value and count thresholds by the client's risk
	// category (Low, Medium, High), in per cent.
	RiskPct   map[string]int `json:"risk_pct"`
	UpdatedBy string         `json:"updated_by,omitempty"`
	UpdatedAt string         `json:"updated_at,omitempty"`
}

// defaultDPSurv are the thresholds until the firm sets its own. They are a
// starting point; the firm's surveillance policy sets the real ones.
func defaultDPSurv() DPSurvSettings {
	return DPSurvSettings{IncomeTimes: 1, ModsPerMonth: 2, AfterModDays: 30, DormantDays: 180, FanCount: 5, ClusterSize: 20,
		PassMin: 100000, PassDays: 30, PassPct: 80, TradePct: 10, FrontMinutes: 15, FrontValue: 1000000, DownloadDays: 7,
		SamplePct: 10, SampleMin: 5, RiskPct: map[string]int{"Low": 100, "Medium": 75, "High": 50}}
}

// DPSurvConfig is the firm's thresholds, defaults filled in.
func (o *Ops) DPSurvConfig(ctx context.Context, tenant string) DPSurvSettings {
	s, _, err := docs.Get[DPSurvSettings](ctx, o.Docs, tenant, cDPSurvSet, "settings")
	d := defaultDPSurv()
	if err != nil {
		return d
	}
	fill := func(p *int, v int) {
		if *p <= 0 {
			*p = v
		}
	}
	fill(&s.IncomeTimes, d.IncomeTimes)
	fill(&s.ModsPerMonth, d.ModsPerMonth)
	fill(&s.AfterModDays, d.AfterModDays)
	fill(&s.DormantDays, d.DormantDays)
	fill(&s.FanCount, d.FanCount)
	fill(&s.ClusterSize, d.ClusterSize)
	fill(&s.PassDays, d.PassDays)
	fill(&s.PassPct, d.PassPct)
	fill(&s.TradePct, d.TradePct)
	fill(&s.FrontMinutes, d.FrontMinutes)
	fill(&s.DownloadDays, d.DownloadDays)
	fill(&s.SamplePct, d.SamplePct)
	fill(&s.SampleMin, d.SampleMin)
	if s.PassMin <= 0 {
		s.PassMin = d.PassMin
	}
	if s.FrontValue <= 0 {
		s.FrontValue = d.FrontValue
	}
	if s.RiskPct == nil {
		s.RiskPct = map[string]int{}
	}
	for k, v := range d.RiskPct {
		if s.RiskPct[k] <= 0 {
			s.RiskPct[k] = v
		}
	}
	return s
}

// SaveDPSurvSettings sets the thresholds (compliance officer or Admin).
func (o *Ops) SaveDPSurvSettings(ctx context.Context, tenant, actor string, s DPSurvSettings) (DPSurvSettings, error) {
	if err := o.require(ctx, tenant, actor, "Setting surveillance thresholds", RoleCompliance); err != nil {
		return s, err
	}
	for k, v := range s.RiskPct {
		if k != "Low" && k != "Medium" && k != "High" {
			return s, invalid("risk categories are Low, Medium and High")
		}
		if v < 10 || v > 200 {
			return s, invalid("a risk scale is between 10 and 200 per cent")
		}
	}
	if s.PassPct > 100 || s.TradePct > 100 || s.SamplePct > 100 {
		return s, invalid("percentages are at most 100")
	}
	if s.ClusterSize > 0 && s.ClusterSize < 2 {
		return s, invalid("a cluster is more than 2 accounts")
	}
	s.UpdatedBy, s.UpdatedAt = actor, o.now().Format(time.RFC3339)
	if err := docs.Upsert(ctx, o.Docs, tenant, cDPSurvSet, "settings", s, actor); err != nil {
		return s, err
	}
	o.log(ctx, tenant, actor, "surveillance.thresholds", "settings", nil)
	return o.DPSurvConfig(ctx, tenant), nil
}

// riskScale is the client's risk category's scale, in per cent.
func (s DPSurvSettings) riskScale(risk string) int {
	r := strings.ToUpper(strings.TrimSpace(risk))
	for k, v := range s.RiskPct {
		if strings.ToUpper(k) == r {
			return v
		}
	}
	return 100
}

// scaleCount scales a count threshold, never below 2.
func (s DPSurvSettings) scaleCount(n int, risk string) int {
	v := int(math.Ceil(float64(n) * float64(s.riskScale(risk)) / 100))
	if v < 2 {
		v = 2
	}
	return v
}

// dlWorkKey is the work item for a list not downloaded since last.
func dlWorkKey(kind, last string, days int) string {
	if last == "" {
		return kind + ":never"
	}
	return kind + ":" + addDays(last, days)
}

// survClocks raises the case work's reminders: CDSL's alert lists not
// downloaded within the firm's interval, and the quarter's sample of
// closures to check.
func (o *Ops) survClocks(ctx context.Context, tenant, date string) {
	set := o.DPSurvConfig(ctx, tenant)
	if o.firmFacts(ctx, tenant).CDSL != triNo {
		xs, _ := docs.List[SurvDownload](ctx, o.Docs, tenant, cSurvDownload, "")
		for _, k := range []string{"cdsl-surv", "cdsl-fiu"} {
			last := lastDownload(xs, k)
			if last != "" && daysBetween(last, date) <= set.DownloadDays {
				continue
			}
			when := "has never been loaded"
			if last != "" {
				when = "was last loaded on " + last
			}
			o.raiseWork(ctx, tenant, "survdownload", dlWorkKey(k, last, set.DownloadDays), "Download "+survImportKinds[k].Title,
				"The list "+when+". Download it from CDAS and load it on Surveillance & AML, or record that there was nothing to download. CDSL's Master Circular for DPs (14.5.8-14.5.12) has the Designated Director and Principal Officer make sure CDSL's FIU alerts are downloaded and analysed.",
				"", RoleCompliance, "surv", date)
		}
	}
	q := lastEndedQuarter(date)
	if v, err := o.alertSample(ctx, tenant, q); err == nil && v.Size > v.Checked {
		_, end, _ := survQuarter(q)
		o.raiseWork(ctx, tenant, "survsample", "q:"+q, fmt.Sprintf("Check the sample of %s's alert closures (%d of %d)", q, v.Size, v.Closed),
			"CDSL's surveillance framework has the compliance officer check a sample of alert closures. Open Surveillance & AML → Sample of closures and record Agree or Disagree on each.", "", RoleCompliance, "surv", addDays(end, 15))
	}
}
