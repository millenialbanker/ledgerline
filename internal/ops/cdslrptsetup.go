package ops

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
// form depends on the report: DP97 takes from_to~F or I, DPZ6 a BO ID or an
// account status with a date type and range, "~" separated). The call goes
// with the DP ID, version and a request sequence number as headers, and the
// details AES-encrypted under the report module's key. CDSL answers at once
// whether the setup was taken and sends its response to the call-back
// address given. That address carries an unguessable token; what arrives
// there (a file, or CDSL's encrypted notice) is recorded, and a file whose
// layout Ledgerline knows (the cdsl-* formats) is loaded exactly like one
// uploaded on Files.
//
// Standing setups are sent each business day at their time, with {prev}
// and {today} filled in as ddmmyyyy.
//
// SFTP: the handbook names the API after the SFTP delivery of the reports
// but says nothing of how a DP reaches that server. When CDSL gives one,
// its address and login go in the connection, and the tick fetches new
// files from it every half hour and loads them through the same readers.
// The SSH client this needs is not in the standard library and go.mod
// carries no other modules, so cdslSFTPFetch is nil in this build: the
// screen says so and reports come in through the call-back or Files.

const (
	cCDSLRptSetup = "cdslrptsetup" // by request sequence number
	cCDSLRptSub   = "cdslrptsub"   // standing setups
	cCDSLRptIn    = "cdslrptin"    // what arrived (call-back or SFTP)
	cCDSLRptToken = "cdslrpttoken" // "token": the call-back token and address

	cdxPathReport = "ReportSetup/v1.0" // §5.21.5
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

// CDSLReportIn is something CDSL sent: to the call-back or on SFTP.
type CDSLReportIn struct {
	ID     string `json:"id"`
	At     string `json:"at"`
	Via    string `json:"via"` // Call-back, SFTP
	Name   string `json:"name,omitempty"`
	Bytes  int    `json:"bytes"`
	SeqNo  string `json:"requestseqno,omitempty"`
	Loaded string `json:"loaded"`
}

type cdslRptToken struct {
	Token     string            `json:"token"`
	Base      string            `json:"base"`
	SFTPSeen  map[string]string `json:"sftp_seen,omitempty"` // name|size -> when
	SFTPLast  string            `json:"sftp_last,omitempty"`
	SFTPError string            `json:"sftp_error,omitempty"`
}

func (o *Ops) cdslRptState(ctx context.Context, tenant string) cdslRptToken {
	t, _, _ := docs.GetOr(ctx, o.Docs, tenant, cCDSLRptToken, "token", cdslRptToken{})
	if t.SFTPSeen == nil {
		t.SFTPSeen = map[string]string{}
	}
	return t
}

// cdslRptCallback is the call-back address, made once per firm.
func (o *Ops) cdslRptCallback(ctx context.Context, tenant, base string) (string, error) {
	t := o.cdslRptState(ctx, tenant)
	if t.Token == "" {
		b := make([]byte, 18)
		_, _ = rand.Read(b)
		t.Token = hex.EncodeToString(b)
	}
	if x, ok := o.cdxConn(ctx, tenant); ok && x.Callback != "" {
		base = x.Callback
	}
	if base = strings.TrimRight(base, "/"); base != "" {
		t.Base = base
	}
	if t.Base == "" {
		return "", invalid("set Ledgerline's public address in the CDSL APIs connection so CDSL can send the reports back")
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cCDSLRptToken, "token", t, SystemActor); err != nil {
		return "", err
	}
	return t.Base + "/hooks/" + tenant + "/cdsl/report/" + t.Token, nil
}

func (o *Ops) cdslReportLists(ctx context.Context, tenant string) ([]CDSLReportSetup, []CDSLReportSub, []CDSLReportIn) {
	rs, _ := docs.List[CDSLReportSetup](ctx, o.Docs, tenant, cCDSLRptSetup, "")
	sort.Slice(rs, func(i, j int) bool { return rs[i].At > rs[j].At })
	if len(rs) > 60 {
		rs = rs[:60]
	}
	subs, _ := docs.List[CDSLReportSub](ctx, o.Docs, tenant, cCDSLRptSub, "")
	in, _ := docs.List[CDSLReportIn](ctx, o.Docs, tenant, cCDSLRptIn, "")
	sort.Slice(in, func(i, j int) bool { return in[i].ID > in[j].ID })
	if len(in) > 60 {
		in = in[:60]
	}
	if rs == nil {
		rs = []CDSLReportSetup{}
	}
	if subs == nil {
		subs = []CDSLReportSub{}
	}
	if in == nil {
		in = []CDSLReportIn{}
	}
	return rs, subs, in
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
	if report == "" || len(report) > 4 || strings.Trim(strings.ToUpper(report), "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789") != "" {
		return invalid("the report ID has up to 4 letters and digits (DP97, DPZ6…)")
	}
	if param == "" || len(param) > 2000 {
		return invalid("give the report's parameters")
	}
	// The two reports the handbook describes (§5.21.7).
	tmpl := strings.Contains(param, "{")
	switch strings.ToUpper(report) {
	case "DP97":
		p := strings.Split(param, "~")
		if len(p) != 2 || (p[1] != "F" && p[1] != "I") || (!tmpl && len(p[0]) != 17) {
			return invalid("DP97 takes from_to~F or from_to~I, dates as ddmmyyyy (01112024_14112024~F)")
		}
	case "DPZ6":
		p := strings.Split(param, "~")
		if len(p) != 7 {
			return invalid("DPZ6 takes seven ~-separated values: BO ID~account status~status code~sub status~date type~from~to")
		}
		if p[0] != "" {
			if len(p[0]) != 16 || strings.Join(p[1:], "") != "" {
				return invalid("DPZ6 for one BO: the 16-digit BO ID and the other values blank")
			}
		} else if p[1] == "" || (p[4] != "M" && p[4] != "S") || p[5] == "" || p[6] == "" {
			return invalid("DPZ6 without a BO ID needs the account status, the date type (M or S) and the date range")
		}
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
	x, ok := o.cdxConn(ctx, tenant)
	if !ok || !x.On(cdslFamReport) {
		return CDSLReportSetup{}, fmt.Errorf("%w: the CDSL report setup API is off: switch it on in Settings → Connections → CDSL APIs (DP)", ErrState)
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
		Param    string `json:"rptparamater"` // CDSL's spelling
		Callback string `json:"dpcallbackurl"`
	}{x.DPID, module, report, param, cb})
	enc, err := cdxSeal(x, cdxKeyReport, plain)
	if err != nil {
		return r, err
	}
	body, _ := json.Marshal(map[string]string{"encryptreqdtl": enc})
	resp, err := x.do(ctx, cdslRequest{Path: cdxPathReport, JSON: body, Header: map[string]string{"dpid": x.DPID, "version": "1.0", "requestseqno": r.SeqNo}})
	switch {
	case err != nil && len(resp.Body) == 0:
		r.Status, r.Error = "Error", err.Error()
	default:
		m, derr := cdxOpenReply(x, cdxKeyReport, resp.Body)
		if derr != nil {
			r.Status, r.Error = "Error", fmt.Sprintf("HTTP %d: %v", resp.Status, derr)
			break
		}
		m = m.in("setupresdtls", "body")
		r.ResID = m.str("resid")
		if m.str("resstatus") == "0" && resp.Status == 200 {
			r.Status = "Taken"
		} else {
			r.Status, r.Error = "Refused", firstOf(strings.TrimSpace(m.str("reqerrorcode")+" "+m.str("reqerrormsg")), fmt.Sprintf("HTTP %d", resp.Status))
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
	o.cdslSFTPTick(ctx, tenant, now)
	subs, _ := docs.List[CDSLReportSub](ctx, o.Docs, tenant, cCDSLRptSub, "")
	if len(subs) == 0 {
		return
	}
	set, _ := o.Settings(ctx, tenant)
	day, hm := now.In(ist).Format("2006-01-02"), now.In(ist).Format("15:04")
	for _, s := range subs {
		if !s.Active || hm < s.At || !o.cdxOnce(ctx, tenant, "rpt:"+s.ID+":"+day, now) {
			continue
		}
		r, err := o.SetupCDSLReport(ctx, tenant, SystemActor, s.Module, s.Report, cdslRptParam(s.Param, now, set), "")
		if err != nil {
			o.raiseWork(ctx, tenant, "cdsl", "rptsetup:"+s.ID+":"+day, "CDSL did not take the "+s.Report+" report setup", err.Error(), "", RoleMaker, "cdsltxn", day)
			continue
		}
		_, _ = docs.Update(ctx, o.Docs, tenant, cCDSLRptSetup, r.SeqNo, SystemActor, func(x *CDSLReportSetup) error { x.Sub = s.ID; return nil })
	}
}

// CDSLReportFile is a report file that arrived.
type CDSLReportFile struct {
	Name string
	Data []byte
}

// cdslLoadReport records a report file and loads it through the
// exchange-file readers.
func (o *Ops) cdslLoadReport(ctx context.Context, tenant, via, name string, data []byte, seq string) string {
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	res, err := o.LoadExchangeFile(ctx, tenant, SystemActor, name, data, true)
	loaded := "Kept"
	switch {
	case err != nil:
		loaded = "Not loaded: " + err.Error()
	case len(res) > 0:
		loaded = firstOf(res[0].Loaded, firstOf(res[0].Note, "Checked"))
	}
	o.cdslRecordReport(ctx, tenant, CDSLReportIn{Via: via, Name: name, Bytes: len(data), SeqNo: seq, Loaded: loaded})
	return name + ": " + loaded
}

func (o *Ops) cdslRecordReport(ctx context.Context, tenant string, in CDSLReportIn) {
	n, _ := o.next(ctx, tenant, "cdslrptin", SystemActor)
	in.ID, in.At = fmt.Sprintf("RI-%06d", n), o.now().Format(time.RFC3339)
	_ = docs.Upsert(ctx, o.Docs, tenant, cCDSLRptIn, in.ID, in, SystemActor)
	if in.SeqNo != "" {
		_, _ = docs.Update(ctx, o.Docs, tenant, cCDSLRptSetup, in.SeqNo, SystemActor, func(x *CDSLReportSetup) error {
			x.Status, x.Arrived = "Received", in.At
			return nil
		})
	}
}

// CDSLReportCallback takes what CDSL sends to the report call-back (not
// signed in: the token in the address admits it, and a notice must decrypt
// under the report key). Files are loaded through the exchange-file
// readers; CDSL's encrypted notices are matched to their setup.
func (o *Ops) CDSLReportCallback(ctx context.Context, tenant, token string, form url.Values, files []CDSLReportFile, raw []byte) (string, error) {
	t := o.cdslRptState(ctx, tenant)
	if t.Token == "" || subtle.ConstantTimeCompare([]byte(t.Token), []byte(token)) != 1 {
		return "", ErrNotFound
	}
	x, _ := o.cdxConn(ctx, tenant)
	var done []string
	for _, f := range files {
		done = append(done, o.cdslLoadReport(ctx, tenant, "Call-back", f.Name, f.Data, form.Get("requestseqno")))
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
		m, err := cdxOpenReply(x, cdxKeyReport, []byte(s))
		if err != nil || len(m) == 0 {
			continue
		}
		m = m.in("setupresdtls", "body")
		seq := m.str("requestseqno")
		if b64 := firstOf(m.str("responsefilebuffer"), m.str("filebuffer")); b64 != "" {
			if data, err := base64.StdEncoding.DecodeString(b64); err == nil {
				done = append(done, o.cdslLoadReport(ctx, tenant, "Call-back", firstOf(m.str("responsefilename"), firstOf(m.str("filename"), "cdsl-report-"+seq)), data, seq))
				continue
			}
		}
		if seq != "" {
			if _, _, err := docs.Get[CDSLReportSetup](ctx, o.Docs, tenant, cCDSLRptSetup, seq); err != nil {
				continue // not a setup sent from here
			}
			o.cdslRecordReport(ctx, tenant, CDSLReportIn{Via: "Call-back", SeqNo: seq, Loaded: "Notice: status " + firstOf(m.str("resstatus"), "?") + " " + m.str("reqerrormsg")})
			done = append(done, "notice for "+seq)
		}
	}
	if len(done) == 0 {
		o.cdslRecordReport(ctx, tenant, CDSLReportIn{Via: "Call-back", Bytes: len(raw), Loaded: "Nothing readable"})
		return "nothing readable", nil
	}
	o.log(ctx, tenant, SystemActor, "cdsl.report_received", "callback", map[string]any{"items": done})
	return strings.Join(done, "; "), nil
}

// ---- SFTP fetch ----

// CDSLSFTPConfig is where CDSL's report SFTP server is.
type CDSLSFTPConfig struct {
	Host, User, Password, Dir, HostKey string
}

// cdslSFTPFetch lists and reads the files in the folder; files already
// fetched are skipped by the caller. It is nil in this build: an SSH client
// (golang.org/x/crypto/ssh and an SFTP package) is not available, as go.mod
// keeps to the standard library. Tests set it.
var cdslSFTPFetch func(ctx context.Context, cfg CDSLSFTPConfig) ([]CDSLReportFile, error)

var errNoSFTP = errors.New("this build of Ledgerline has no SFTP client; reports come in through the call-back or on Files")

func cdslSFTPConfig(x cdslConn) (CDSLSFTPConfig, bool) {
	c := CDSLSFTPConfig{Host: strings.TrimSpace(x.c.Values["sftp_host"]), User: strings.TrimSpace(x.c.Values["sftp_user"]), Password: x.c.Secrets["sftp_password"],
		Dir: firstOf(strings.TrimSpace(x.c.Values["sftp_dir"]), "/"), HostKey: strings.TrimSpace(x.c.Values["sftp_hostkey"])}
	return c, c.Host != "" && c.User != ""
}

// cdslSFTPState is the SFTP line on the screen.
func (o *Ops) cdslSFTPState(ctx context.Context, tenant string) string {
	x, ok := o.cdxConn(ctx, tenant)
	if !ok {
		return "Not set"
	}
	if _, ok := cdslSFTPConfig(x); !ok {
		return "Not set"
	}
	if cdslSFTPFetch == nil {
		return errNoSFTP.Error()
	}
	t := o.cdslRptState(ctx, tenant)
	switch {
	case t.SFTPError != "":
		return "Last fetch failed: " + t.SFTPError
	case t.SFTPLast != "":
		return "Last fetched " + t.SFTPLast
	}
	return "Not fetched yet"
}

// FetchCDSLSFTP fetches new report files from CDSL's SFTP folder and loads
// them.
func (o *Ops) FetchCDSLSFTP(ctx context.Context, tenant, actor string) ([]string, error) {
	if err := o.require(ctx, tenant, actor, "Setting up CDSL reports", RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return nil, err
	}
	x, ok := o.cdxConn(ctx, tenant)
	if !ok || !x.On(cdslFamReport) {
		return nil, fmt.Errorf("%w: the CDSL report API is off", ErrState)
	}
	cfg, ok := cdslSFTPConfig(x)
	if !ok {
		return nil, invalid("enter the report SFTP server and user in the CDSL APIs connection")
	}
	if cdslSFTPFetch == nil {
		return nil, fmt.Errorf("%w: %v", ErrState, errNoSFTP)
	}
	files, err := cdslSFTPFetch(ctx, cfg)
	t := o.cdslRptState(ctx, tenant)
	t.SFTPLast, t.SFTPError = o.now().Format(time.RFC3339), ""
	if err != nil {
		t.SFTPError = err.Error()
		_ = docs.Upsert(ctx, o.Docs, tenant, cCDSLRptToken, "token", t, SystemActor)
		return nil, err
	}
	var done []string
	for _, f := range files {
		k := path.Base(f.Name) + "|" + fmt.Sprint(len(f.Data))
		if t.SFTPSeen[k] != "" {
			continue
		}
		done = append(done, o.cdslLoadReport(ctx, tenant, "SFTP", f.Name, f.Data, ""))
		t.SFTPSeen[k] = t.SFTPLast
	}
	cut := o.now().AddDate(0, 0, -30).Format(time.RFC3339)
	for k, v := range t.SFTPSeen {
		if v < cut {
			delete(t.SFTPSeen, k)
		}
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cCDSLRptToken, "token", t, SystemActor)
	if len(done) > 0 {
		o.log(ctx, tenant, actor, "cdsl.report_received", "sftp", map[string]any{"items": done})
	}
	return done, nil
}

// cdslSFTPTick fetches every half hour, 07:00–22:00.
func (o *Ops) cdslSFTPTick(ctx context.Context, tenant string, now time.Time) {
	x, ok := o.cdxConn(ctx, tenant)
	if !ok || cdslSFTPFetch == nil {
		return
	}
	if _, ok := cdslSFTPConfig(x); !ok {
		return
	}
	hm := now.In(ist).Format("15:04")
	if hm < "07:00" || hm > "22:00" {
		return
	}
	if last, err := time.Parse(time.RFC3339, o.cdslRptState(ctx, tenant).SFTPLast); err == nil && now.Sub(last) < 30*time.Minute {
		return
	}
	if _, err := o.FetchCDSLSFTP(ctx, tenant, SystemActor); err != nil {
		o.raiseWork(ctx, tenant, "cdsl", "sftp:"+now.In(ist).Format("2006-01-02"), "CDSL report SFTP fetch failed", err.Error(), "", RoleMaker, "cdsltxn", "")
	}
}
