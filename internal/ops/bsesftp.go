package ops

import (
	"context"
	"fmt"
	"net"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"ledgerline/internal/docs"
)

// ---- BSE SFTP fetch ----
//
// BSE puts the member's extranet files (trades, stamp duty, bhavcopy, the
// member reports in bseextranet.go) on its SFTP server. Access: the firm
// submits its static IP address to BSE Technologies or BSE Index Services
// Pvt. Ltd. (BISPL) for firewall whitelisting, then connects on the port
// BSE assigns for production, or on port 3313 for the DR/BCP server.
//
// Every half hour from 06:30 to 23:00, when switched on, Ledgerline signs
// in, looks in the folders given (by default the member's own folder and
// Common) and in their date folders of the last few days, and loads each
// file whose layout it reads, once, as if uploaded on Files. The server's
// host key must be saved first, as for CDSL's and MCX's SFTP.

const (
	connBSESFTP   = "bse-sftp"
	cBSESFTPState = "bsesftpstate" // one document: the last run
	cBSESFTPFile  = "bsesftpfile"  // key: folder|name|size|modified
	bseDRPort     = 3313
)

var bseSFTPMu sync.Mutex

func init() {
	ConnProviders = append(ConnProviders, ConnProvider{ID: connBSESFTP, Name: "BSE SFTP (member files)", Group: "Exchanges", Test: "none",
		Purpose: "Downloads the firm's BSE extranet files (trades, stamp duty, bhavcopy, member reports) from BSE's SFTP server and loads the ones Ledgerline reads. Submit the server's static IP to BSE Technologies or BISPL for firewall whitelisting first; production uses the port BSE assigns, the DR/BCP server port 3313",
		Fields: []ConnField{
			{Key: "host", Label: "Production SFTP server (host)"},
			{Key: "port", Label: "Production port (assigned by BSE)", Placeholder: "e.g. 22"},
			{Key: "dr_host", Label: "DR/BCP SFTP server (host)", Optional: true},
			{Key: "site", Label: "Connect to", Options: []string{"Production", "DR/BCP"}},
			{Key: "user", Label: "SFTP user"},
			{Key: "password", Label: "SFTP password", Secret: true},
			{Key: "member", Label: "Member code (the folder name)", Placeholder: "e.g. 0999"},
			{Key: "folders", Label: "Folders to look in (comma separated; empty: the member folder and Common)", Optional: true},
			{Key: "hostkey", Label: "Server's host key (SHA256:… fingerprint or the full key line). Required; the first try shows the key the server offers", Optional: true},
			{Key: "fetch", Label: "Fetch new files automatically", Options: []string{"On", "Off"}},
			fNotes}})
}

type bseSFTPConn struct {
	addr, user, password, hostKey string
	folders                       []string
}

func (o *Ops) bseSFTPConn(ctx context.Context, tenant string) (bseSFTPConn, error) {
	cn, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connBSESFTP)
	if err != nil || cn.Status == "Not set" {
		return bseSFTPConn{}, fmt.Errorf("%w: set up BSE SFTP in Settings → Connections", ErrState)
	}
	return parseBSESFTP(cn)
}

func parseBSESFTP(cn Connection) (bseSFTPConn, error) {
	v := func(k string) string { return strings.TrimSpace(cn.Values[k]) }
	x := bseSFTPConn{user: v("user"), password: cn.Secrets["password"], hostKey: v("hostkey")}
	member := strings.Trim(v("member"), "/\\")
	host, port := v("host"), 0
	if p := v("port"); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n <= 0 || n > 65535 {
			return x, invalid("the BSE SFTP port is a number, as BSE assigned it")
		}
		port = n
	}
	if v("site") == "DR/BCP" {
		if v("dr_host") == "" {
			return x, invalid("give the DR/BCP SFTP server to connect to it")
		}
		host, port = v("dr_host"), bseDRPort
	}
	if host == "" || x.user == "" || member == "" {
		return x, invalid("BSE SFTP needs the server, user and member code")
	}
	if h, p, err := net.SplitHostPort(host); err == nil { // a port written with the host
		host = h
		if port == 0 {
			port, _ = strconv.Atoi(p)
		}
	}
	if port == 0 {
		port = 22
	}
	x.addr = net.JoinHostPort(strings.Trim(host, "[]"), strconv.Itoa(port))
	for _, f := range strings.Split(v("folders"), ",") {
		if f = strings.ReplaceAll(strings.TrimSpace(f), "\\", "/"); f != "" {
			x.folders = append(x.folders, f)
		}
	}
	if len(x.folders) == 0 {
		x.folders = []string{"/" + member, "/Common"}
	}
	return x, nil
}

// BSESFTPFetch fetches and loads new files from BSE's SFTP server. The
// run, file and view types are MCX's.
func (o *Ops) BSESFTPFetch(ctx context.Context, tenant, actor string) (MCXSFTPRun, error) {
	var run MCXSFTPRun
	if actor != SystemActor {
		if err := o.require(ctx, tenant, actor, "Fetching files from BSE's SFTP", RoleMaker, RoleChecker, RoleRisk); err != nil {
			return run, err
		}
	}
	x, err := o.bseSFTPConn(ctx, tenant)
	if err != nil {
		return run, err
	}
	bseSFTPMu.Lock()
	defer bseSFTPMu.Unlock()
	now := o.now()
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cBSESFTPState, "state", MCXSFTPState{})
	st.LastRun = now.Format(time.RFC3339)
	save := func() { _ = docs.Upsert(ctx, o.Docs, tenant, cBSESFTPState, "state", st, SystemActor) }
	cctx, cancel := context.WithTimeout(ctx, mcxSFTPTimeout)
	defer cancel()
	fs, err := sftpDial(cctx, x.addr, x.user, x.password, x.hostKey)
	if err != nil {
		if strings.Contains(err.Error(), "timeout") || strings.Contains(err.Error(), "refused") {
			err = fmt.Errorf("%v (has BSE whitelisted this server's static IP, and is the port right: the assigned one for production, 3313 for DR/BCP?)", err)
		}
		st.LastError = err.Error()
		save()
		return run, err
	}
	defer fs.Close()
	var days []time.Time
	for i := 0; i < mcxSFTPDays; i++ {
		days = append(days, now.In(ist).AddDate(0, 0, -i))
	}
	cut := now.Add(-time.Duration(mcxSFTPDays) * 24 * time.Hour)
	type dir struct {
		p     string
		dated bool
	}
	var dirs []dir
	for _, f := range x.folders {
		dirs = append(dirs, dir{f, false})
		if es, err := fs.ReadDir(f); err == nil {
			for _, e := range es {
				if e.IsDir() && mcxDateFolder(e.Name(), days) {
					dirs = append(dirs, dir{path.Join(f, e.Name()), true})
				}
			}
		}
	}
	for _, d := range dirs {
		if len(run.Fetched) >= mcxSFTPPerRun {
			break
		}
		es, err := fs.ReadDir(d.p)
		if err != nil {
			run.Problems = append(run.Problems, fmt.Sprintf("%s: %v", d.p, err))
			continue
		}
		run.Folders = append(run.Folders, d.p)
		sort.Slice(es, func(i, j int) bool { return es[i].ModTime().Before(es[j].ModTime()) })
		for _, e := range es {
			if !e.Mode().IsRegular() || (!d.dated && e.ModTime().Before(cut)) {
				continue
			}
			if !xfileKnown(e.Name()) {
				run.Unknown++
				continue
			}
			mod := e.ModTime().UTC().Format(time.RFC3339)
			key := strings.Join([]string{d.p, e.Name(), fmt.Sprint(e.Size()), mod}, "|")
			if _, _, err := docs.Get[MCXSFTPFile](ctx, o.Docs, tenant, cBSESFTPFile, key); err == nil {
				run.Skipped++
				continue
			}
			if len(run.Fetched) >= mcxSFTPPerRun {
				break
			}
			if e.Size() > sftpMaxBytes {
				run.Problems = append(run.Problems, e.Name()+": larger than 64 MB, skipped")
				continue
			}
			data, err := fs.readSmall(path.Join(d.p, e.Name()))
			if err != nil {
				run.Problems = append(run.Problems, fmt.Sprintf("%s: %v", e.Name(), err))
				continue
			}
			rec := MCXSFTPFile{Folder: d.p, Name: e.Name(), Modified: mod, Bytes: len(data), At: now.Format(time.RFC3339)}
			if len(data) == 0 {
				rec.Loaded = "Empty file"
			} else {
				res, lerr := o.LoadExchangeFile(ctx, tenant, SystemActor, e.Name(), data, true)
				switch {
				case lerr != nil:
					rec.Loaded = "Not loaded: " + lerr.Error()
				case len(res) > 0:
					rec.Format, rec.Loaded = res[0].Format, firstOf(res[0].Loaded, firstOf(res[0].Note, "Checked"))
				}
			}
			_ = docs.Upsert(ctx, o.Docs, tenant, cBSESFTPFile, key, rec, SystemActor)
			run.Fetched = append(run.Fetched, rec)
		}
	}
	st.Fetched, st.Loaded = len(run.Fetched), 0
	for _, f := range run.Fetched {
		if !strings.HasPrefix(f.Loaded, "Not loaded") && f.Loaded != "Empty file" {
			st.Loaded++
		}
	}
	st.LastError = ""
	if len(run.Problems) > 0 {
		st.LastError = run.Problems[0]
	} else {
		st.LastOK = now.Format(time.RFC3339)
	}
	save()
	o.log(ctx, tenant, actor, "bse.sftp_fetched", now.In(ist).Format("2006-01-02"), map[string]any{"files": len(run.Fetched), "folders": len(run.Folders), "problems": len(run.Problems)})
	return run, nil
}

// BSESFTPStatus is the last run and the latest files.
func (o *Ops) BSESFTPStatus(ctx context.Context, tenant string) MCXSFTPView {
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cBSESFTPState, "state", MCXSFTPState{})
	fs, _ := docs.List[MCXSFTPFile](ctx, o.Docs, tenant, cBSESFTPFile, "")
	sort.SliceStable(fs, func(i, j int) bool { return fs[i].At > fs[j].At })
	if len(fs) > 100 {
		fs = fs[:100]
	}
	if fs == nil {
		fs = []MCXSFTPFile{}
	}
	return MCXSFTPView{State: st, Files: fs}
}

// bseSFTPTick fetches every half hour, 06:30 to 23:00, when switched on.
// A failed run raises one work item a day.
func (o *Ops) bseSFTPTick(ctx context.Context, tenant string, now time.Time) {
	cn, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connBSESFTP)
	if err != nil || cn.Status == "Not set" || cn.Values["fetch"] != "On" {
		return
	}
	if hm := now.In(ist).Format("15:04"); hm < "06:30" || hm > "23:00" {
		return
	}
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cBSESFTPState, "state", MCXSFTPState{})
	if t, err := time.Parse(time.RFC3339, st.LastRun); err == nil && now.Sub(t) < 30*time.Minute {
		return
	}
	run, err := o.BSESFTPFetch(ctx, tenant, SystemActor)
	if err == nil && len(run.Problems) > 0 && len(run.Folders) == 0 {
		err = fmt.Errorf("%s", run.Problems[0])
	}
	if err != nil {
		o.raiseWork(ctx, tenant, "bse", "sftp:"+now.In(ist).Format("2006-01-02"), "BSE SFTP fetch failed", err.Error(), "", RoleMaker, "xfiles", "")
	}
}
