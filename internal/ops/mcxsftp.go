package ops

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"ledgerline/internal/docs"
)

// ---- MCX SFTP fetch ----
//
// MCX and MCXCCL put a trading member's files on their SFTP server
// (MCX/TECH/394/2023 and MCXCCL File Formats v1, "File Path"):
//
//   - FTP Root path\Common\ : the product master, bhavcopy, CP master and
//     other common files;
//   - FTP Root path\<Trading Member ID>\<Date Folder>\ : the member's own
//     files (trades, obligation, margin and peak margin, CTT, stamp duty,
//     positions, allocation, collateral, penalties).
//
// Every half hour from 06:30 to 23:00, when switched on, Ledgerline signs
// in, looks in Common and in the date folders of the last few days, and
// downloads each file whose layout it reads (the mcx-* and mcxccl-*
// formats; the hundreds of SPAN risk-parameter and software files there
// are left alone). Each is loaded as if uploaded on Files, once: a file is
// fetched again only if its size or time changes. The server's host key
// must be saved first, as for CDSL's SFTP.

const (
	connMCXSFTP    = "mcx-sftp"
	cMCXSFTPState  = "mcxsftpstate" // one document: the last run
	cMCXSFTPFile   = "mcxsftpfile"  // key: folder|name|size|modified
	mcxSFTPPerRun  = 80
	mcxSFTPDays    = 4 // date folders of today and the 3 days before
	mcxSFTPTimeout = 5 * time.Minute
)

var mcxSFTPMu sync.Mutex

func init() {
	ConnProviders = append(ConnProviders, ConnProvider{ID: connMCXSFTP, Name: "MCX SFTP (member files)", Group: "Exchanges", Test: "none",
		Purpose: "Downloads the firm's MCX and MCXCCL files (trades, obligation, margins, CTT, stamp duty, positions, allocation, penalties) and the common files from MCX's SFTP server, and loads the ones Ledgerline reads. MCX gives the login with trading membership; for a member clearing through another CM the trading-member folder is still the firm's own",
		Fields: []ConnField{
			{Key: "host", Label: "SFTP server (host or host:port)"},
			{Key: "user", Label: "SFTP user"},
			{Key: "password", Label: "SFTP password", Secret: true},
			{Key: "member", Label: "Trading member ID (the folder name)", Placeholder: "e.g. 56845"},
			{Key: "root", Label: "Root folder", Optional: true, Placeholder: "/"},
			{Key: "hostkey", Label: "Server's host key (SHA256:… fingerprint or the full key line). Required; the first try shows the key the server offers", Optional: true},
			{Key: "fetch", Label: "Fetch new files automatically", Options: []string{"On", "Off"}},
			fNotes}})
}

// MCXSFTPState is the last run.
type MCXSFTPState struct {
	LastRun   string `json:"last_run,omitempty"`
	LastOK    string `json:"last_ok,omitempty"`
	LastError string `json:"last_error,omitempty"`
	Fetched   int    `json:"fetched"`
	Loaded    int    `json:"loaded"`
}

// MCXSFTPFile is one file fetched.
type MCXSFTPFile struct {
	Folder   string `json:"folder"`
	Name     string `json:"name"`
	Modified string `json:"modified"`
	Bytes    int    `json:"bytes"`
	At       string `json:"at"`
	Format   string `json:"format,omitempty"`
	Loaded   string `json:"loaded"`
}

// MCXSFTPRun is what one run did.
type MCXSFTPRun struct {
	Folders  []string      `json:"folders_looked"`
	Fetched  []MCXSFTPFile `json:"fetched"`
	Skipped  int           `json:"already_have"`
	Unknown  int           `json:"not_read_by_ledgerline"`
	Problems []string      `json:"problems,omitempty"`
}

// MCXSFTPView is the screen.
type MCXSFTPView struct {
	State MCXSFTPState  `json:"state"`
	Files []MCXSFTPFile `json:"files"`
}

type mcxSFTPConn struct {
	host, user, password, member, root, hostKey string
}

func (o *Ops) mcxSFTPConn(ctx context.Context, tenant string) (mcxSFTPConn, Connection, error) {
	cn, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connMCXSFTP)
	if err != nil || cn.Status == "Not set" {
		return mcxSFTPConn{}, cn, fmt.Errorf("%w: set up MCX SFTP in Settings → Connections", ErrState)
	}
	x := mcxSFTPConn{host: strings.TrimSpace(cn.Values["host"]), user: strings.TrimSpace(cn.Values["user"]), password: cn.Secrets["password"],
		member: strings.Trim(strings.TrimSpace(cn.Values["member"]), "/\\"), root: strings.TrimSpace(cn.Values["root"]), hostKey: strings.TrimSpace(cn.Values["hostkey"])}
	if x.root == "" {
		x.root = "/"
	}
	x.root = strings.ReplaceAll(x.root, "\\", "/")
	if x.host == "" || x.user == "" || x.member == "" {
		return x, cn, invalid("MCX SFTP needs the server, user and trading member ID")
	}
	return x, cn, nil
}

// mcxDateFolder says whether a folder name is one of the given days, in
// any of the date spellings exchanges use.
func mcxDateFolder(name string, days []time.Time) bool {
	n := strings.ToUpper(strings.TrimSpace(name))
	for _, d := range days {
		for _, f := range []string{"02012006", "20060102", "02-01-2006", "2006-01-02", "02Jan2006", "02-Jan-2006", "02_01_2006", "2006_01_02"} {
			if n == strings.ToUpper(d.Format(f)) {
				return true
			}
		}
	}
	return false
}

// MCXSFTPFetch fetches and loads new files from MCX's SFTP server.
func (o *Ops) MCXSFTPFetch(ctx context.Context, tenant, actor string) (MCXSFTPRun, error) {
	var run MCXSFTPRun
	if actor != SystemActor {
		if err := o.require(ctx, tenant, actor, "Fetching files from MCX's SFTP", RoleMaker, RoleChecker, RoleRisk); err != nil {
			return run, err
		}
	}
	x, _, err := o.mcxSFTPConn(ctx, tenant)
	if err != nil {
		return run, err
	}
	mcxSFTPMu.Lock()
	defer mcxSFTPMu.Unlock()
	now := o.now()
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cMCXSFTPState, "state", MCXSFTPState{})
	st.LastRun = now.Format(time.RFC3339)
	save := func() { _ = docs.Upsert(ctx, o.Docs, tenant, cMCXSFTPState, "state", st, SystemActor) }

	cctx, cancel := context.WithTimeout(ctx, mcxSFTPTimeout)
	defer cancel()
	fs, err := sftpDial(cctx, x.host, x.user, x.password, x.hostKey)
	if err != nil {
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

	// The folders: Common, the member folder itself, and its date folders.
	folders := []string{path.Join(x.root, "Common"), path.Join(x.root, x.member)}
	if es, err := fs.ReadDir(path.Join(x.root, x.member)); err != nil {
		run.Problems = append(run.Problems, fmt.Sprintf("%s: %v", path.Join(x.root, x.member), err))
	} else {
		for _, e := range es {
			if e.IsDir() && mcxDateFolder(e.Name(), days) {
				folders = append(folders, path.Join(x.root, x.member, e.Name()))
			}
		}
	}
	for _, dir := range folders {
		if len(run.Fetched) >= mcxSFTPPerRun {
			break
		}
		es, err := fs.ReadDir(dir)
		if err != nil {
			if dir != folders[1] { // the member folder's error is already noted
				run.Problems = append(run.Problems, fmt.Sprintf("%s: %v", dir, err))
			}
			continue
		}
		run.Folders = append(run.Folders, dir)
		sort.Slice(es, func(i, j int) bool { return es[i].ModTime().Before(es[j].ModTime()) })
		dated := dir != folders[0] && dir != folders[1]
		for _, e := range es {
			if !e.Mode().IsRegular() {
				continue
			}
			// Outside the date folders only recent files are read.
			if !dated && e.ModTime().Before(cut) {
				continue
			}
			if !xfileKnown(e.Name()) {
				run.Unknown++
				continue
			}
			mod := e.ModTime().UTC().Format(time.RFC3339)
			key := strings.Join([]string{dir, e.Name(), fmt.Sprint(e.Size()), mod}, "|")
			if _, _, err := docs.Get[MCXSFTPFile](ctx, o.Docs, tenant, cMCXSFTPFile, key); err == nil {
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
			data, err := fs.readSmall(path.Join(dir, e.Name()))
			if err != nil {
				run.Problems = append(run.Problems, fmt.Sprintf("%s: %v", e.Name(), err))
				continue
			}
			rec := MCXSFTPFile{Folder: dir, Name: e.Name(), Modified: mod, Bytes: len(data), At: now.Format(time.RFC3339)}
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
			_ = docs.Upsert(ctx, o.Docs, tenant, cMCXSFTPFile, key, rec, SystemActor)
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
	o.log(ctx, tenant, actor, "mcx.sftp_fetched", now.In(ist).Format("2006-01-02"), map[string]any{"files": len(run.Fetched), "folders": len(run.Folders), "problems": len(run.Problems)})
	return run, nil
}

// MCXSFTPStatus is the last run and the latest files.
func (o *Ops) MCXSFTPStatus(ctx context.Context, tenant string) MCXSFTPView {
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cMCXSFTPState, "state", MCXSFTPState{})
	fs, _ := docs.List[MCXSFTPFile](ctx, o.Docs, tenant, cMCXSFTPFile, "")
	sort.SliceStable(fs, func(i, j int) bool { return fs[i].At > fs[j].At })
	if len(fs) > 100 {
		fs = fs[:100]
	}
	if fs == nil {
		fs = []MCXSFTPFile{}
	}
	return MCXSFTPView{State: st, Files: fs}
}

// mcxSFTPTick fetches every half hour, 06:30 to 23:00, when switched on.
// A failed run raises one work item a day.
func (o *Ops) mcxSFTPTick(ctx context.Context, tenant string, now time.Time) {
	cn, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connMCXSFTP)
	if err != nil || cn.Status == "Not set" || cn.Values["fetch"] != "On" {
		return
	}
	if hm := now.In(ist).Format("15:04"); hm < "06:30" || hm > "23:00" {
		return
	}
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cMCXSFTPState, "state", MCXSFTPState{})
	if t, err := time.Parse(time.RFC3339, st.LastRun); err == nil && now.Sub(t) < 30*time.Minute {
		return
	}
	run, err := o.MCXSFTPFetch(ctx, tenant, SystemActor)
	if err == nil && len(run.Problems) > 0 && len(run.Folders) == 0 {
		err = fmt.Errorf("%s", run.Problems[0])
	}
	if err != nil {
		o.raiseWork(ctx, tenant, "mcx", "sftp:"+now.In(ist).Format("2006-01-02"), "MCX SFTP fetch failed", err.Error(), "", RoleMaker, "xfiles", "")
	}
}
