package ops

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
)

// ---- CDSL report setup API (CDSL API Handbook §5.21) ----
//
// The DP asks CDSL for a report through the API: one report per call, by
// module ID, report ID and the report's parameters ("rptparamater", whose
// form depends on the report: DP97 takes "from_to~F|I", DPZ6 a BO ID or
// account status and date range, "~" separated). CDSL answers at once with
// whether the setup was taken, and sends the report to the call-back
// address. That address carries an unguessable token; what arrives there
// (a file, or CDSL's encrypted notice) is recorded, and a file whose layout
// Ledgerline knows (cdsl-* formats) is loaded exactly like an uploaded one.
//
// Standing setups ("subscriptions") are sent each business day at their
// time with {prev} and {today} filled in as ddmmyyyy.
//
// Fetching reports from an SFTP server needs an SSH client, which the
// standard library does not have and go.mod does not carry; until it is
// added, reports come in through the call-back or are loaded on Files.

const (
	cCDSLRptSetup = "cdslrptsetup" // by request sequence number
	cCDSLRptSub   = "cdslrptsub"   // standing setups
	cCDSLRptIn    = "cdslrptin"    // what arrived at the call-back
	cCDSLRptToken = "cdslrpttoken" // "token": the call-back token and address
)

// CDSLReportSetup is one setup request.
type CDSLReportSetup struct {
	SeqNo   string `json:"requestseqno"`
	Module  string `json:"module"`
	Report  string `json:"report"`
	Param   string `json:"param"`
	Status  string `json:"status"` // Taken, Refused, Error, Received
	ResID   string `json:"resid,omitempty"`
	Error   string `json:"error,omitempty"`
	At      string `json:"at"`
	By      string `json:"by"`
	Sub     string `json:"subscription,omitempty"`
	Arrived string `json:"arrived,omitempty"`
}

// CDSLReportSub is a standing daily setup.
type CDSLReportSub struct {
	ID     string `json:"id"`
	Module string `json:"module"`
	Report string `json:"report"`
	Param  string `json:"param"` // {prev} {today} become ddmmyyyy
	At     string `json:"at"`    // HH:MM IST
	Active bool   `json:"active"`
	By     string `json:"by,omitempty"`
}

// CDSLReportIn is something CDSL sent to the call-back.
type CDSLReportIn struct {
	ID     string `json:"id"`
	At     string `json:"at"`
	Name   string `json:"name,omitempty"`
	Bytes  int    `json:"bytes"`
	SeqNo  string `json:"requestseqno,omitempty"`
	Loaded string `json:"loaded"`
}

type cdslRptToken struct {
	Token string `json:"token"`
	Base  string `json:"base"`
}

// cdslRptCallback is the call-back address, made once per firm.
func (o *Ops) cdslRptCallback(ctx context.Context, tenant, base string) (string, error) {
	t, _, _ := docs.GetOr(ctx, o.Docs, tenant, cCDSLRptToken, "token", cdslRptToken{})
	if t.Token == "" {
		b := make([]byte, 18)
		_, _ = rand.Read(b)
		t.Token = hex.EncodeToString(b)
	}
	if base = strings.TrimRight(base, "/"); base != "" {
		t.Base = base
	}
	if t.Base == "" {
		return "", invalid("set the server's public address (LEDGERLINE_PUBLIC_URL) so CDSL can send the reports back")
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cCDSLRptToken, "token", t, SystemActor); err != nil {
		return "", err
	}
	return t.Base + "/hooks/" + tenant + "/cdsl/report/" + t.Token, nil
}

func (o *Ops) cdslReportLists(ctx context.Context, tenant string) ([]CDSLReportSetup, []CDSLReportSub) {
	rs, _ := docs.List[CDSLReportSetup](ctx, o.Docs, tenant, cCDSLRptSetup, "")
	sort.Slice(rs, func(i, j int) bool { return rs[i].At > rs[j].At })
	if len(rs) > 60 {
		rs = rs[:60]
	}
	subs, _ := docs.List[CDSLReportSub](ctx, o.Docs, tenant, cCDSLRptSub, "")
	if rs == nil {
		rs = []CDSLReportSetup{}
	}
	if subs == nil {
		subs = []CDSLReportSub{}
	}
	return rs, subs
}

func cdslRptParam(p string, now time.Time, set Settings) string {
	d := now.In(ist).Format("2006-01-02")
	prev := set.PrevBusinessDay(d)
	f := func(x string) string { return x[8:10] + x[5:7] + x[:4] }
	return strings.NewReplacer("{today}", f(d), "{prev}", f(prev)).Replace(p)
}

func checkRptFields(module, report, param string) error {
	if module == "" || len(module) > 2 || strings.Trim(module, "0123456789") != "" {
		return invalid("the module ID is a number of up to 2 digits")
	}
	if report == "" || len(report) > 4 {
		return invalid("the report ID has up to 4 letters and digits (DP97, DPZ6…)")
	}
	if param == "" || len(param) > 2000 {
		return invalid("give the report's parameters")
	}
	return nil
}

// SetupCDSLReport asks CDSL for one report (§5.21.7).
func (o *Ops) SetupCDSLReport(ctx context.Context, tenant, actor, module, report, param, base string) (CDSLReportSetup, error) {
	if err := o.require(ctx, tenant, actor, "Setting up CDSL reports", RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return CDSLReportSetup{}, err
	}
	module, report, param = strings.TrimSpace(module), strings.ToUpper(strings.TrimSpace(report)), strings.TrimSpace(param)
	if err := checkRptFields(module, report, param); err != nil {
		return CDSLReportSetup{}, err
	}
	c := o.cdxConn(ctx, tenant)
	if !c.on(cdxFamReport) {
		return CDSLReportSetup{}, fmt.Errorf("%w: the CDSL report API is off: switch it on in Settings → Connections → CDSL APIs (DP)", ErrState)
	}
	key := c.key(cdxFamReport)
	if c.dpID == "" || key == "" {
		return CDSLReportSetup{}, invalid("the CDSL APIs connection needs the DP ID and the report API key")
	}
	cb, err := o.cdslRptCallback(ctx, tenant, base)
	if err != nil {
		return CDSLReportSetup{}, err
	}
	now := o.now()
	r := CDSLReportSetup{SeqNo: cdxNumericID(now, 24, "20060102150405"), Module: module, Report: report, Param: param, At: now.Format(time.RFC3339), By: actor}
	plain, _ := json.Marshal(struct {
		DPID     string `json:"dpid"`
		Module   string `json:"moduleid"`
		Report   string `json:"reportid"`
		Param    string `json:"rptparamater"`
		Callback string `json:"dpcallbackurl"`
	}{c.dpID, module, report, param, cb})
	enc, err := cdxEncrypt(key, plain)
	if err != nil {
		return r, err
	}
	body, _ := json.Marshal(map[string]string{"encryptreqdtl": enc})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/ReportSetup/v1.0", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("dpid", c.dpID)
	req.Header.Set("version", "1.0")
	req.Header.Set("requestseqno", r.SeqNo)
	code, b, err := cdxDo(req)
	switch {
	case err != nil:
		r.Status, r.Error = "Error", err.Error()
	default:
		m, derr := cdxDecryptReply(key, b)
		if derr != nil {
			r.Status, r.Error = "Error", fmt.Sprintf("HTTP %d: %v", code, derr)
			break
		}
		m = m.in("setupresdtls", "body")
		r.ResID = m.str("resid")
		if m.str("resstatus") == "0" && code == 200 {
			r.Status = "Taken"
		} else {
			r.Status, r.Error = "Refused", firstOf(strings.TrimSpace(m.str("reqerrorcode")+" "+m.str("reqerrormsg")), fmt.Sprintf("HTTP %d", code))
		}
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cCDSLRptSetup, r.SeqNo, r, actor); err != nil {
		return r, err
	}
	o.log(ctx, tenant, actor, "cdsl.report_setup", r.SeqNo, map[string]any{"module": module, "report": report, "status": r.Status, "error": r.Error})
	if r.Status != "Taken" {
		return r, fmt.Errorf("%w: CDSL did not take the report setup: %s", ErrState, r.Error)
	}
	return r, nil
}

// SaveCDSLReportSub adds, changes or stops a standing daily setup.
func (o *Ops) SaveCDSLReportSub(ctx context.Context, tenant, actor string, s CDSLReportSub, base string) (CDSLReportSub, error) {
	if err := o.require(ctx, tenant, actor, "Setting up CDSL reports", RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return s, err
	}
	s.Module, s.Report, s.Param, s.At = strings.TrimSpace(s.Module), strings.ToUpper(strings.TrimSpace(s.Report)), strings.TrimSpace(s.Param), strings.TrimSpace(s.At)
	if err := checkRptFields(s.Module, s.Report, s.Param); err != nil {
		return s, err
	}
	if _, err := time.Parse("15:04", s.At); err != nil {
		return s, invalid("the time is HH:MM")
	}
	if _, err := o.cdslRptCallback(ctx, tenant, base); err != nil {
		return s, err
	}
	if s.ID == "" {
		n, err := o.next(ctx, tenant, "cdslrptsub", actor)
		if err != nil {
			return s, err
		}
		s.ID = fmt.Sprintf("RS-%03d", n)
	}
	s.By = actor
	if err := docs.Upsert(ctx, o.Docs, tenant, cCDSLRptSub, s.ID, s, actor); err != nil {
		return s, err
	}
	o.log(ctx, tenant, actor, "cdsl.report_subscription", s.ID, map[string]any{"report": s.Report, "active": s.Active, "at": s.At})
	return s, nil
}

func (o *Ops) cdslReportTick(ctx context.Context, tenant string, now time.Time) {
	subs, _ := docs.List[CDSLReportSub](ctx, o.Docs, tenant, cCDSLRptSub, "")
	if len(subs) == 0 {
		return
	}
	set, _ := o.Settings(ctx, tenant)
	day, hm := now.In(ist).Format("2006-01-02"), now.In(ist).Format("15:04")
	for _, s := range subs {
		if !s.Active || hm < s.At {
			continue
		}
		st := o.cdxState(ctx, tenant)
		k := "rpt:" + s.ID + ":" + day
		if st.Done[k] != "" {
			continue
		}
		st.Done[k] = now.Format(time.RFC3339)
		for x := range st.Done { // keep a week
			if i := strings.LastIndex(x, ":"); i > 0 && x[i+1:] < addDays(day, -7) {
				delete(st.Done, x)
			}
		}
		o.cdxSaveState(ctx, tenant, st)
		r, err := o.SetupCDSLReport(ctx, tenant, SystemActor, s.Module, s.Report, cdslRptParam(s.Param, now, set), "")
		if err != nil {
			o.raiseWork(ctx, tenant, "cdsl", "rptsetup:"+s.ID+":"+day, "CDSL did not take the "+s.Report+" report setup", err.Error(), "", RoleMaker, "cdsltxn", day)
			continue
		}
		_, _ = docs.Update(ctx, o.Docs, tenant, cCDSLRptSetup, r.SeqNo, SystemActor, func(x *CDSLReportSetup) error { x.Sub = s.ID; return nil })
	}
}

// CDSLReportFile is a file that arrived at the call-back.
type CDSLReportFile struct {
	Name string
	Data []byte
}

// CDSLReportCallback takes what CDSL sends to the report call-back (not
// signed in: the token in the address admits it). Files are loaded through
// the exchange-file readers; CDSL's encrypted notices are matched to their
// setup.
func (o *Ops) CDSLReportCallback(ctx context.Context, tenant, token string, form url.Values, files []CDSLReportFile, raw []byte) (string, error) {
	t, _, err := docs.Get[cdslRptToken](ctx, o.Docs, tenant, cCDSLRptToken, "token")
	if err != nil || t.Token == "" || subtle.ConstantTimeCompare([]byte(t.Token), []byte(token)) != 1 {
		return "", ErrNotFound
	}
	c := o.cdxConn(ctx, tenant)
	key := c.key(cdxFamReport)
	now := o.now()
	record := func(in CDSLReportIn) {
		n, _ := o.next(ctx, tenant, "cdslrptin", SystemActor)
		in.ID, in.At = fmt.Sprintf("RI-%06d", n), now.Format(time.RFC3339)
		_ = docs.Upsert(ctx, o.Docs, tenant, cCDSLRptIn, in.ID, in, SystemActor)
		if in.SeqNo != "" {
			_, _ = docs.Update(ctx, o.Docs, tenant, cCDSLRptSetup, in.SeqNo, SystemActor, func(x *CDSLReportSetup) error {
				x.Status, x.Arrived = "Received", in.At
				return nil
			})
		}
	}
	load := func(name string, data []byte, seq string) string {
		name = path.Base(strings.ReplaceAll(name, "\\", "/"))
		res, err := o.LoadExchangeFile(ctx, tenant, SystemActor, name, data, true)
		loaded := "Kept"
		switch {
		case err != nil:
			loaded = "Not loaded: " + err.Error()
		case len(res) > 0:
			loaded = firstOf(res[0].Loaded, firstOf(res[0].Note, "Checked"))
		}
		record(CDSLReportIn{Name: name, Bytes: len(data), SeqNo: seq, Loaded: loaded})
		return name + ": " + loaded
	}
	var done []string
	for _, f := range files {
		done = append(done, load(f.Name, f.Data, form.Get("requestseqno")))
	}
	// An encrypted notice, as a form field or the body itself.
	var texts []string
	for _, vs := range form {
		texts = append(texts, vs...)
	}
	if len(files) == 0 && len(form) == 0 && len(raw) > 0 {
		texts = append(texts, string(raw))
	}
	for _, s := range texts {
		m, err := cdxDecryptReply(key, []byte(s))
		if err != nil || len(m) == 0 {
			continue
		}
		m = m.in("setupresdtls", "body")
		seq := m.str("requestseqno")
		if b64 := firstOf(m.str("responsefilebuffer"), m.str("filebuffer")); b64 != "" {
			if data, err := base64.StdEncoding.DecodeString(b64); err == nil {
				done = append(done, load(firstOf(m.str("responsefilename"), firstOf(m.str("filename"), "cdsl-report-"+seq)), data, seq))
				continue
			}
		}
		if seq != "" {
			record(CDSLReportIn{SeqNo: seq, Loaded: "Notice: status " + firstOf(m.str("resstatus"), "?") + " " + m.str("reqerrormsg")})
			done = append(done, "notice for "+seq)
		}
	}
	if len(done) == 0 {
		record(CDSLReportIn{Bytes: len(raw), Loaded: "Nothing readable"})
		return "nothing readable", nil
	}
	o.log(ctx, tenant, SystemActor, "cdsl.report_received", "callback", map[string]any{"items": done})
	return strings.Join(done, "; "), nil
}
