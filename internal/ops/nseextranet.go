package ops

import (
	"bytes"
	"context"
	"crypto/aes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"ledgerline/internal/docs"
)

// ---- NSE extranet API ----
//
// NSE's extranet API lets a member list and download its own files
// (trade files, order logs, limit reports…) and the common files (bhavcopy,
// contract and security files) without the member portal. Ledgerline logs
// in, looks through the folders the firm chooses, downloads each new file
// whose layout it knows, and loads it exactly as an uploaded exchange file
// would be loaded.
//
// From NSE's "FAQ on Extranet API" (v6.0, March 2026) and its "Extranet API
// path" guide (v6, October 2026):
//   - Internet: https://www.connect2nse.com/extranet-api/ ; lease line:
//     https://172.19.125.70:443/extranet-api/
//   - POST /login/{version} {memberCode, loginId, password}, the password
//     encrypted AES-256-ECB (PKCS#7) with the secret key NSE emails after
//     registration (base64), sent as base64; the reply's token lasts an
//     hour, and only the latest token of a login is valid.
//   - GET /member/content/{v}?segment=&folderPath= and /common/content/…
//     list folders; GET /member/file/download/{v}?segment=&folderPath=
//     &filename=[&date=DD-MM-YYYY] and /common/file/download/… download.
//   - Bearer token; response code 601 is success, 803 an expired token.
//   - Live service 06:00 to 05:00 the next day (not 05:00 to 06:00).
//
// The full API document (circular MSD55065) gives the reply layouts; until
// it is in hand the folder listing is read leniently.

const (
	connNSEExtranet = "nse-extranet"
	cExtranetState  = "extranetstate" // one document: login and last run
	cExtranetFile   = "extranetfile"  // key: seg|kind|folder|name|modified

	extranetLive  = "https://www.connect2nse.com/extranet-api"
	extranetLease = "https://172.19.125.70:443/extranet-api"
	// extranetPerRun caps downloads in one scheduled run.
	extranetPerRun = 40
)

func init() {
	ConnProviders = append(ConnProviders, ConnProvider{ID: connNSEExtranet, Name: "NSE extranet API", Group: "Exchanges", Test: "https",
		Purpose: "Downloads the firm's NSE files (trade files, order logs, limit reports) and common files from NSE's extranet and loads the ones Ledgerline reads. Register on NSE's Member Portal (Extranet API registration); NSE emails the secret key",
		Fields: []ConnField{{Key: "member", Label: "Member code"}, {Key: "login", Label: "Extranet API login ID"},
			{Key: "password", Label: "Extranet API password (set at registration)", Secret: true}, {Key: "api_key", Label: "Secret key (from NSE's email)", Secret: true},
			{Key: "network", Label: "Network", Options: []string{"Internet", "Lease line"}},
			{Key: "version", Label: "API version", Options: []string{"2.0", "1.0"}, Optional: true},
			{Key: "segments", Label: "Segments", Options: []string{"CM", "CM, FO", "CM, FO, CD", "CM, FO, CD, CO", "FO", "CM, FO, CD, CO, SLB"}},
			{Key: "folders", Label: "Folders to look in (member: or common:, comma separated)", Optional: true, Placeholder: "member:/Onlinebackup, member:/Reports"},
			{Key: "fetch", Label: "Fetch new files automatically", Options: []string{"On", "Off"}},
			{Key: "url", Label: "Address (leave empty for NSE's)", Optional: true, Placeholder: extranetLive}, fNotes}})
}

// ExtranetState is the login and the last run.
type ExtranetState struct {
	Token     string         `json:"token,omitempty"`
	TokenExp  string         `json:"token_exp,omitempty"`
	LastRun   string         `json:"last_run,omitempty"`
	LastOK    string         `json:"last_ok,omitempty"`
	LastError string         `json:"last_error,omitempty"`
	Fetched   int            `json:"fetched"` // files downloaded in the last run
	Loaded    int            `json:"loaded"`  // of them, loaded
	Total     map[string]int `json:"total,omitempty"`
}

// ExtranetEntry is one folder or file in a listing.
type ExtranetEntry struct {
	Name     string `json:"name"`
	Folder   bool   `json:"folder"`
	Modified string `json:"modified,omitempty"`
	Size     int64  `json:"size,omitempty"`
}

// ExtranetFile records a file Ledgerline has downloaded.
type ExtranetFile struct {
	Segment  string `json:"segment"`
	Kind     string `json:"kind"` // member or common
	Folder   string `json:"folder"`
	Name     string `json:"name"`
	Modified string `json:"modified,omitempty"`
	At       string `json:"at"`
	Bytes    int    `json:"bytes"`
	Format   string `json:"format,omitempty"`
	Loaded   string `json:"loaded"`
}

type extranetConn struct {
	base, version, member, login, password, key string
	segments                                    []string
	folders                                     [][2]string // kind, path
	fetch                                       bool
}

func parseExtranetConn(c Connection) (extranetConn, error) {
	x := extranetConn{member: strings.TrimSpace(c.Values["member"]), login: strings.TrimSpace(c.Values["login"]), password: c.Secrets["password"], key: c.Secrets["api_key"],
		version: firstOf(c.Values["version"], "2.0"), segments: nclmSegments(firstOf(c.Values["segments"], "CM")), fetch: c.Values["fetch"] == "On"}
	if x.member == "" || x.login == "" || x.password == "" || x.key == "" {
		return x, invalid("the NSE extranet connection needs the member code, login ID, password and secret key")
	}
	x.base = extranetLive
	if c.Values["network"] == "Lease line" {
		x.base = extranetLease
	}
	if u := strings.TrimRight(c.Values["url"], "/"); u != "" {
		x.base = u
	}
	for _, f := range strings.Split(firstOf(c.Values["folders"], "member:/Onlinebackup, member:/Reports"), ",") {
		kind, p, ok := strings.Cut(strings.TrimSpace(f), ":")
		kind = strings.ToLower(strings.TrimSpace(kind))
		if !ok || (kind != "member" && kind != "common") || !strings.HasPrefix(strings.TrimSpace(p), "/") {
			return x, invalid("folders are written member:/path or common:/path, separated by commas")
		}
		x.folders = append(x.folders, [2]string{kind, strings.ReplaceAll(strings.TrimSpace(p), "\\", "/")})
	}
	return x, nil
}

// extranetPassword encrypts the password for the login: AES-256-ECB with
// PKCS#7 padding under the base64 secret key, as base64.
func extranetPassword(secretKey, password string) (string, error) {
	var key []byte
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(strings.TrimSpace(secretKey)); err == nil && len(b) == 32 {
			key = b
			break
		}
	}
	if key == nil {
		return "", invalid("the extranet secret key must be NSE's 44-character base64 key")
	}
	blk, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	pad := aes.BlockSize - len(password)%aes.BlockSize
	p := append([]byte(password), bytes.Repeat([]byte{byte(pad)}, pad)...)
	out := make([]byte, len(p))
	for i := 0; i < len(p); i += aes.BlockSize { // ECB: each block on its own
		blk.Encrypt(out[i:i+aes.BlockSize], p[i:i+aes.BlockSize])
	}
	return base64.StdEncoding.EncodeToString(out), nil
}

// extranetHTTP is replaced in tests.
var extranetHTTP = &http.Client{Timeout: 3 * time.Minute}

// One extranet session per login across the server: NSE keeps only the
// latest token valid.
var extranetMu sync.Mutex

// extranetCodes reads a reply's response codes ("code": ["601"] or "601").
func extranetCodes(b []byte) (codes []string, status string, raw map[string]json.RawMessage) {
	if json.Unmarshal(b, &raw) != nil {
		return nil, "", nil
	}
	if c, ok := raw["code"]; ok {
		var list []any
		if json.Unmarshal(c, &list) == nil {
			for _, x := range list {
				codes = append(codes, strings.TrimSpace(fmt.Sprint(x)))
			}
		} else {
			var one any
			_ = json.Unmarshal(c, &one)
			codes = append(codes, strings.TrimSpace(fmt.Sprint(one)))
		}
	}
	if s, ok := raw["status"]; ok {
		_ = json.Unmarshal(s, &status)
	}
	return codes, status, raw
}

var extranetCodeText = map[string]string{
	"601": "success", "605": "the password must be the encrypted password, not the secret key", "701": "member code, login ID or password not accepted",
	"702": "login disabled (5 wrong passwords): reset it on the Member Portal", "703": "member code or login ID not valid", "704": "member not eligible for this segment",
	"706": "request not valid", "710": "date not valid", "712": "folder not found", "715": "file not found in that folder", "720": "this login has no access to the segment (admin user grants it on the Member Portal)",
	"721": "this login has no access to common files (admin user grants it on the Member Portal)", "803": "token expired or replaced", "920": "API version not valid",
	"921": "request syntax not valid", "923": "address not valid", "925": "NSE's login service is down",
}

func extranetErr(codes []string, status int, b []byte) error {
	for _, c := range codes {
		if c != "601" {
			return fmt.Errorf("NSE extranet answered %s: %s", c, firstOf(extranetCodeText[c], "see NSE's code list"))
		}
	}
	return fmt.Errorf("NSE extranet answered %d: %s", status, trimBody(b))
}

func (x extranetConn) do(ctx context.Context, method, p, token string, body any) (int, http.Header, []byte, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, x.base+p, rd)
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("Accept", "application/json, application/octet-stream, */*")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := extranetHTTP.Do(req)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("could not reach NSE's extranet: %v", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, MaxXFileBytes+1))
	return resp.StatusCode, resp.Header, b, err
}

func (o *Ops) extranetLogin(ctx context.Context, x extranetConn, st *ExtranetState, now time.Time) (string, error) {
	if exp, err := time.Parse(time.RFC3339, st.TokenExp); err == nil && now.Before(exp) && st.Token != "" {
		return st.Token, nil
	}
	pw, err := extranetPassword(x.key, x.password)
	if err != nil {
		return "", err
	}
	code, _, b, err := x.do(ctx, http.MethodPost, "/login/"+x.version, "", map[string]string{"memberCode": x.member, "loginId": x.login, "password": pw})
	if err != nil {
		return "", err
	}
	codes, _, raw := extranetCodes(b)
	var tok string
	if t, ok := raw["token"]; ok {
		_ = json.Unmarshal(t, &tok)
	}
	if code != 200 || tok == "" {
		return "", extranetErr(codes, code, b)
	}
	st.Token, st.TokenExp = tok, now.Add(55*time.Minute).Format(time.RFC3339)
	return tok, nil
}

// extranetList reads a folder listing, whatever its exact layout: the
// entries are the objects in the reply's list, with their name, whether
// they are folders, when they changed and their size.
func extranetList(b []byte) []ExtranetEntry {
	var v any
	if json.Unmarshal(b, &v) != nil {
		return nil
	}
	var objs []map[string]any
	var find func(any)
	find = func(v any) {
		switch t := v.(type) {
		case []any:
			for _, e := range t {
				if m, ok := e.(map[string]any); ok {
					objs = append(objs, m)
				} else {
					find(e)
				}
			}
		case map[string]any:
			keys := make([]string, 0, len(t))
			for k := range t {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if strings.EqualFold(k, "code") {
					continue
				}
				find(t[k])
			}
		}
	}
	find(v)
	get := func(m map[string]any, names ...string) (any, bool) {
		for k, x := range m {
			for _, n := range names {
				if strings.EqualFold(k, n) {
					return x, true
				}
			}
		}
		return nil, false
	}
	var out []ExtranetEntry
	for _, m := range objs {
		n, ok := get(m, "name", "fileName", "file_name", "filename", "folderName", "fileOrFolderName")
		if !ok {
			continue
		}
		e := ExtranetEntry{Name: strings.TrimSpace(fmt.Sprint(n))}
		if t, ok := get(m, "type", "fileType", "isFolder", "isDirectory", "folder", "directory"); ok {
			s := strings.ToLower(fmt.Sprint(t))
			e.Folder = s == "true" || strings.Contains(s, "folder") || strings.Contains(s, "dir") || s == "d"
		}
		if t, ok := get(m, "lastModified", "lastUpdated", "lastModifiedTime", "modified", "modifiedDate", "lastModifiedDate"); ok {
			e.Modified = strings.TrimSpace(fmt.Sprint(t))
		}
		if s, ok := get(m, "size", "fileSize", "sizeInBytes"); ok {
			e.Size, _ = strconv.ParseInt(strings.Split(fmt.Sprint(s), ".")[0], 10, 64)
		}
		if e.Name != "" {
			out = append(out, e)
		}
	}
	return out
}

// extranetDated says whether a file belongs to the day: its name carries
// the date (DDMMYYYY, YYYYMMDD or DDMM), or it changed that day.
func extranetDated(e ExtranetEntry, day time.Time) bool {
	for _, f := range []string{"02012006", "20060102", "02-01-2006", "02Jan2006"} {
		if strings.Contains(strings.ToUpper(e.Name), strings.ToUpper(day.Format(f))) {
			return true
		}
	}
	m := strings.TrimSpace(e.Modified)
	for _, f := range []string{"02/01/2006", "2006-01-02", "02-01-2006", "02-Jan-2006"} {
		if strings.HasPrefix(m, day.Format(f)) {
			return true
		}
	}
	if ms, err := strconv.ParseInt(m, 10, 64); err == nil && ms > 1e12 { // milliseconds, UTC
		return time.UnixMilli(ms).In(ist).Format("2006-01-02") == day.Format("2006-01-02")
	}
	return false
}

// xfileKnown says whether Ledgerline reads files of this name.
func xfileKnown(name string) bool {
	lib, err := XFiles()
	if err != nil {
		return false
	}
	for _, n := range []string{name, strings.TrimSuffix(name, ".gz"), strings.TrimSuffix(name, ".zip")} {
		for _, s := range lib.Specs {
			if s.Direction == "in" && s.MatchesName(n) {
				return true
			}
		}
	}
	return false
}

// ExtranetRun is what one run did.
type ExtranetRun struct {
	Looked   int            `json:"folders_looked"`
	Fetched  []ExtranetFile `json:"fetched"`
	Skipped  int            `json:"already_have"`
	Unknown  int            `json:"not_read_by_ledgerline"`
	Problems []string       `json:"problems,omitempty"`
}

// ExtranetFetch looks through the configured folders for the day's new
// files that Ledgerline reads, downloads them and loads them.
func (o *Ops) ExtranetFetch(ctx context.Context, tenant, actor, date string) (ExtranetRun, error) {
	var run ExtranetRun
	if actor != SystemActor {
		if err := o.require(ctx, tenant, actor, "Fetching files from NSE's extranet", RoleMaker, RoleChecker, RoleRisk); err != nil {
			return run, err
		}
	}
	cn, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connNSEExtranet)
	if err != nil || cn.Status == "Not set" {
		return run, fmt.Errorf("%w: set up the NSE extranet API in Settings → Connections", ErrState)
	}
	x, err := parseExtranetConn(cn)
	if err != nil {
		return run, err
	}
	now := o.now()
	if hm := now.In(ist).Format("15:04"); hm >= "05:00" && hm < "06:00" {
		return run, fmt.Errorf("%w: NSE's extranet API is closed from 05:00 to 06:00", ErrState)
	}
	day := now.In(ist)
	if date != "" {
		if day, err = time.ParseInLocation("2006-01-02", date, ist); err != nil {
			return run, invalid("date is YYYY-MM-DD")
		}
	}
	extranetMu.Lock()
	defer extranetMu.Unlock()
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cExtranetState, "state", ExtranetState{})
	save := func() { _ = docs.Upsert(ctx, o.Docs, tenant, cExtranetState, "state", st, SystemActor) }
	st.LastRun = now.Format(time.RFC3339)
	tok, err := o.extranetLogin(ctx, x, &st, now)
	if err != nil {
		st.LastError = err.Error()
		save()
		return run, err
	}
	save()
	// get calls the API, logging in again once if the token was replaced.
	get := func(p string) (int, http.Header, []byte, error) {
		code, h, b, err := x.do(ctx, http.MethodGet, p, tok, nil)
		if err == nil && code != 200 {
			if codes, _, _ := extranetCodes(b); contains(codes, "803") || code == 401 {
				st.Token = ""
				if tok, err = o.extranetLogin(ctx, x, &st, now); err != nil {
					return code, h, b, err
				}
				save()
				code, h, b, err = x.do(ctx, http.MethodGet, p, tok, nil)
			}
		}
		return code, h, b, err
	}
	type folder struct {
		seg, kind, path string
		depth           int
	}
	var queue []folder
	for _, seg := range x.segments {
		for _, f := range x.folders {
			queue = append(queue, folder{seg, f[0], f[1], 0})
		}
	}
	for len(queue) > 0 && len(run.Fetched) < extranetPerRun {
		f := queue[0]
		queue = queue[1:]
		q := url.Values{"segment": {f.seg}, "folderPath": {f.path}}
		code, _, b, err := get("/" + f.kind + "/content/" + x.version + "?" + q.Encode())
		run.Looked++
		if err != nil || code != 200 {
			if err == nil {
				codes, _, _ := extranetCodes(b)
				err = extranetErr(codes, code, b)
			}
			run.Problems = append(run.Problems, fmt.Sprintf("%s %s %s: %v", f.seg, f.kind, f.path, err))
			continue
		}
		for _, e := range extranetList(b) {
			if e.Folder {
				if f.depth < 2 {
					queue = append(queue, folder{f.seg, f.kind, path.Join(f.path, e.Name), f.depth + 1})
				}
				continue
			}
			if !extranetDated(e, day) {
				continue
			}
			if !xfileKnown(e.Name) {
				run.Unknown++
				continue
			}
			key := strings.Join([]string{f.seg, f.kind, f.path, e.Name, e.Modified}, "|")
			if _, _, err := docs.Get[ExtranetFile](ctx, o.Docs, tenant, cExtranetFile, key); err == nil {
				run.Skipped++
				continue
			}
			if len(run.Fetched) >= extranetPerRun {
				break
			}
			dq := url.Values{"segment": {f.seg}, "folderPath": {f.path}, "filename": {e.Name}}
			code, h, data, err := get("/" + f.kind + "/file/download/" + x.version + "?" + dq.Encode())
			if err == nil && code == 200 && strings.Contains(h.Get("Content-Type"), "json") {
				// A JSON reply is an error, or the file inside it.
				codes, _, raw := extranetCodes(data)
				var b64 string
				for _, k := range []string{"data", "content", "file", "fileContent"} {
					if v, ok := raw[k]; ok && json.Unmarshal(v, &b64) == nil && b64 != "" {
						break
					}
				}
				if d, derr := base64.StdEncoding.DecodeString(b64); b64 != "" && derr == nil {
					data = d
				} else if len(codes) > 0 {
					err = extranetErr(codes, code, data)
				}
			} else if err == nil && code != 200 {
				codes, _, _ := extranetCodes(data)
				err = extranetErr(codes, code, data)
			}
			if err != nil {
				run.Problems = append(run.Problems, fmt.Sprintf("%s %s: %v", f.seg, e.Name, err))
				continue
			}
			rec := ExtranetFile{Segment: f.seg, Kind: f.kind, Folder: f.path, Name: e.Name, Modified: e.Modified, At: now.Format(time.RFC3339), Bytes: len(data)}
			res, lerr := o.LoadExchangeFile(ctx, tenant, SystemActor, e.Name, data, true)
			switch {
			case lerr != nil:
				rec.Loaded = "Not loaded: " + lerr.Error()
			case len(res) > 0:
				rec.Format, rec.Loaded = res[0].Format, firstOf(res[0].Loaded, firstOf(res[0].Note, "Checked"))
			}
			_ = docs.Upsert(ctx, o.Docs, tenant, cExtranetFile, key, rec, SystemActor)
			run.Fetched = append(run.Fetched, rec)
		}
	}
	st.Fetched, st.Loaded = len(run.Fetched), 0
	for _, f := range run.Fetched {
		if !strings.HasPrefix(f.Loaded, "Not loaded") {
			st.Loaded++
		}
	}
	if st.Total == nil {
		st.Total = map[string]int{}
	}
	st.Total[day.Format("2006-01-02")] += len(run.Fetched)
	st.LastError = ""
	if len(run.Problems) > 0 {
		st.LastError = run.Problems[0]
	} else {
		st.LastOK = now.Format(time.RFC3339)
	}
	save()
	o.log(ctx, tenant, actor, "extranet.fetched", day.Format("2006-01-02"), map[string]any{"files": len(run.Fetched), "folders": run.Looked, "problems": len(run.Problems)})
	return run, nil
}

// ExtranetBrowse lists one folder, for the screen.
func (o *Ops) ExtranetBrowse(ctx context.Context, tenant, actor, seg, kind, folder string) ([]ExtranetEntry, error) {
	if err := o.require(ctx, tenant, actor, "Looking at NSE's extranet", RoleMaker, RoleChecker, RoleRisk, RoleCompliance); err != nil {
		return nil, err
	}
	if kind != "member" && kind != "common" {
		return nil, invalid("kind is member or common")
	}
	cn, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connNSEExtranet)
	if err != nil || cn.Status == "Not set" {
		return nil, fmt.Errorf("%w: set up the NSE extranet API in Settings → Connections", ErrState)
	}
	x, err := parseExtranetConn(cn)
	if err != nil {
		return nil, err
	}
	extranetMu.Lock()
	defer extranetMu.Unlock()
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cExtranetState, "state", ExtranetState{})
	tok, err := o.extranetLogin(ctx, x, &st, o.now())
	if err != nil {
		return nil, err
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cExtranetState, "state", st, SystemActor)
	q := url.Values{"segment": {strings.ToUpper(seg)}, "folderPath": {firstOf(strings.ReplaceAll(folder, "\\", "/"), "/")}}
	code, _, b, err := x.do(ctx, http.MethodGet, "/"+kind+"/content/"+x.version+"?"+q.Encode(), tok, nil)
	if err != nil {
		return nil, err
	}
	if code != 200 {
		codes, _, _ := extranetCodes(b)
		return nil, extranetErr(codes, code, b)
	}
	return extranetList(b), nil
}

// ExtranetView is the screen: the feed's state and the latest files.
type ExtranetView struct {
	State ExtranetState  `json:"state"`
	Files []ExtranetFile `json:"files"`
}

// ExtranetStatus shows the last run and the files fetched most recently.
func (o *Ops) ExtranetStatus(ctx context.Context, tenant string) ExtranetView {
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cExtranetState, "state", ExtranetState{})
	st.Token = ""
	fs, _ := docs.List[ExtranetFile](ctx, o.Docs, tenant, cExtranetFile, "")
	sort.SliceStable(fs, func(i, j int) bool { return fs[i].At > fs[j].At })
	if len(fs) > 100 {
		fs = fs[:100]
	}
	if fs == nil {
		fs = []ExtranetFile{}
	}
	return ExtranetView{State: st, Files: fs}
}

// extranetTick fetches new files every 15 minutes when switched on, outside
// NSE's 05:00 to 06:00 maintenance hour.
func (o *Ops) extranetTick(ctx context.Context, tenant string, now time.Time) {
	cn, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connNSEExtranet)
	if err != nil || cn.Status == "Not set" || cn.Values["fetch"] != "On" {
		return
	}
	if hm := now.In(ist).Format("15:04"); hm >= "05:00" && hm < "06:30" {
		return
	}
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cExtranetState, "state", ExtranetState{})
	if t, err := time.Parse(time.RFC3339, st.LastRun); err == nil && now.Sub(t) < 15*time.Minute {
		return
	}
	_, _ = o.ExtranetFetch(ctx, tenant, SystemActor, "")
}
