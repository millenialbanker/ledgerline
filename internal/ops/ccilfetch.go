package ops

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"ledgerline/internal/docs"
)

// ---- CCIL's G-sec and T-bill prices, fetched ----
//
// CCIL publishes its valuation (MTM) price of every Treasury bill,
// government security and state development loan each business day, as a
// workbook named MTMPRICE<ddmmyyyy>, on its public website
// (www.ccilindia.com/mtm-prices), with no login. With the "CCIL prices"
// connection's fetch on, Ledgerline downloads the day's file in the evening
// and loads it as if uploaded on Exchange files (ccil-gsec-price): G-secs
// and T-bills held or given as collateral are valued at CCIL's price, and
// the monthly task "CCIL prices of G-secs and T-bills" is done by itself
// once the month-end file is in. A day missed in the evening is fetched
// the next morning.
//
// Where the file is: the connection may give its address, with {DD}, {MM}
// and {YYYY} for the date. Without one, Ledgerline reads CCIL's MTM prices
// page and follows the link it finds to MTMPRICE<ddmmyyyy>. CCIL's page
// (the ProcessNotificationViewMTM portlet) builds its list of files in the
// browser from encrypted data and zips the files chosen (crypto-js and
// jszip), and the site runs a bot check: the page may have no link, and
// the site may refuse a server. Either way the fetch says why, and the
// monthly task falls back to a work item to load the file by hand. To be
// checked on the firm's own server: whether a day's file has an address
// the server can download.

const (
	connCCIL     = "ccil-prices"
	cCCILState   = "ccilstate" // one document: the last fetch
	ccilPage     = "https://www.ccilindia.com/mtm-prices"
	ccilMaxBytes = 20 << 20
	ccilFromHM   = "19:00" // CCIL's end-of-day prices are out by then
)

// ccilHTTP is the client for CCIL's public website (tests replace it).
var ccilHTTP = &http.Client{Timeout: 60 * time.Second}

func init() {
	ConnProviders = append(ConnProviders, ConnProvider{ID: connCCIL, Name: "CCIL prices (G-secs and T-bills)", Group: "Exchanges", Test: "none",
		Purpose: "Downloads CCIL's daily valuation prices of Treasury bills, government securities and SDLs (MTMPRICE, on CCIL's public website, no login) each business day evening and loads them, to value G-secs and T-bills held or given as collateral, and for the month-end prices",
		Fields: []ConnField{
			{Key: "file_url", Label: "Address of the day's price file, with {DD}, {MM} and {YYYY} for the date (leave empty to look for the link on CCIL's MTM prices page)", Optional: true, Placeholder: "https://www.ccilindia.com/…/MTMPRICE{DD}{MM}{YYYY}.xls"},
			{Key: "page", Label: "CCIL's MTM prices page", Optional: true, Placeholder: ccilPage},
			{Key: "fetch", Label: "Fetch the prices automatically", Options: []string{"On", "Off"}},
			fNotes}})
}

// CCILState is the last fetch, for the screen.
type CCILState struct {
	LastRun   string `json:"last_run,omitempty"`
	LastDate  string `json:"last_date,omitempty"` // the price date last tried
	LastOK    string `json:"last_ok,omitempty"`   // the price date last loaded
	LastFile  string `json:"last_file,omitempty"`
	LastError string `json:"last_error,omitempty"`
}

// CCILRun is what one fetch did.
type CCILRun struct {
	Date   string   `json:"date"`             // the price date loaded
	File   string   `json:"file,omitempty"`   // as loaded: MTMPRICE<ddmmyyyy>.xls(x)
	From   string   `json:"from,omitempty"`   // the address it came from
	Loaded string   `json:"loaded,omitempty"` // what loading it did
	Prices int      `json:"prices"`
	Tried  []string `json:"tried,omitempty"` // dates tried before it, and why not
}

// ccilConn is the connection, with the page default.
func (o *Ops) ccilConn(ctx context.Context, tenant string) (Connection, bool) {
	cn, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connCCIL)
	if err != nil || cn.Status == "Not set" {
		return cn, false
	}
	if cn.Values == nil {
		cn.Values = map[string]string{}
	}
	return cn, true
}

// ccilAddress puts a date into the connection's file address.
func ccilAddress(tmpl, date string) string {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return ""
	}
	r := strings.NewReplacer("{DD}", t.Format("02"), "{MM}", t.Format("01"), "{YYYY}", t.Format("2006"), "{YY}", t.Format("06"),
		"{dd}", t.Format("02"), "{mm}", t.Format("01"), "{yyyy}", t.Format("2006"), "{DDMMYYYY}", t.Format("02012006"))
	return r.Replace(strings.TrimSpace(tmpl))
}

// ccilGet downloads one address: https only, at most ccilMaxBytes.
func ccilGet(ctx context.Context, addr string) ([]byte, string, error) {
	u, err := url.Parse(addr)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, "", fmt.Errorf("%q is not an https address", addr)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; Ledgerline back office)")
	req.Header.Set("Accept", "*/*")
	resp, err := ccilHTTP.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, ccilMaxBytes+1))
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("%s answered %s", u.Host, resp.Status)
	}
	if len(b) > ccilMaxBytes {
		return nil, "", fmt.Errorf("the file from %s is larger than %d MB", u.Host, ccilMaxBytes>>20)
	}
	return b, resp.Request.URL.String(), nil
}

// ccilWorkbook says what kind of workbook the bytes are ("" when none: an
// error page, say).
func ccilWorkbook(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte("PK\x03\x04")):
		return ".xlsx"
	case bytes.HasPrefix(b, []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}):
		return ".xls"
	}
	return ""
}

var reHref = regexp.MustCompile(`(?is)href\s*=\s*["']([^"']+)["']`)

// ccilLinkOn finds the link to a date's file on CCIL's page.
func ccilLinkOn(page []byte, base, date string) string {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return ""
	}
	want := "MTMPRICE" + t.Format("02012006")
	b, err := url.Parse(base)
	if err != nil {
		return ""
	}
	for _, m := range reHref.FindAllSubmatch(page, -1) {
		h := strings.TrimSpace(strings.ReplaceAll(string(m[1]), "&amp;", "&"))
		if !strings.Contains(strings.ToUpper(h), want) {
			continue
		}
		if u, err := b.Parse(h); err == nil && u.Scheme == "https" {
			return u.String()
		}
	}
	return ""
}

// CCILFetch downloads CCIL's prices of a date and loads them. With back,
// up to that many business days before it are tried in turn when the date
// has no file (a CCIL holiday at month end).
func (o *Ops) CCILFetch(ctx context.Context, tenant, actor, date string, back int) (CCILRun, error) {
	if actor != SystemActor {
		if err := o.require(ctx, tenant, actor, "Fetching CCIL's prices", RoleMaker, RoleChecker, RoleRisk); err != nil {
			return CCILRun{}, err
		}
	}
	if !validDate(date) {
		return CCILRun{}, invalid("give the price date (YYYY-MM-DD)")
	}
	cn, ok := o.ccilConn(ctx, tenant)
	if !ok {
		return CCILRun{}, fmt.Errorf("%w: set up CCIL prices in Settings → Connections", ErrState)
	}
	set, _ := o.Settings(ctx, tenant)
	tmpl, pageAddr := strings.TrimSpace(cn.Values["file_url"]), firstOf(strings.TrimSpace(cn.Values["page"]), ccilPage)
	var page []byte
	var pageErr error
	run := CCILRun{}
	d := date
	for i := 0; i <= back; i++ {
		if i > 0 {
			d = set.PrevBusinessDay(d)
		}
		addr := ""
		if tmpl != "" {
			addr = ccilAddress(tmpl, d)
		} else {
			if page == nil && pageErr == nil {
				page, _, pageErr = ccilGet(ctx, pageAddr)
			}
			if pageErr != nil {
				return run, fmt.Errorf("CCIL's MTM prices page: %v", pageErr)
			}
			if addr = ccilLinkOn(page, pageAddr, d); addr == "" {
				run.Tried = append(run.Tried, d+": no link to MTMPRICE"+ccilDay(d)+" on CCIL's page")
				continue
			}
		}
		b, from, err := ccilGet(ctx, addr)
		if err != nil {
			run.Tried = append(run.Tried, d+": "+err.Error())
			continue
		}
		ext := ccilWorkbook(b)
		if ext == "" {
			run.Tried = append(run.Tried, d+": the address gave no workbook (CCIL's page, or no file for the day)")
			continue
		}
		name := "MTMPRICE" + ccilDay(d) + ext
		res, err := o.LoadExchangeFile(ctx, tenant, SystemActor, name, b, true)
		if err != nil {
			return run, err
		}
		if len(res) != 1 || res[0].Format != "ccil-gsec-price" || !strings.HasPrefix(res[0].Loaded, "Loaded") {
			why := "not read as CCIL's prices"
			if len(res) == 1 {
				why = firstOf(res[0].Loaded, firstOf(res[0].Note, why))
			}
			run.Tried = append(run.Tried, d+": "+why)
			continue
		}
		run.Date, run.File, run.From, run.Loaded, run.Prices = d, name, from, res[0].Loaded, res[0].Added
		o.log(ctx, tenant, actor, "ccil.prices_fetched", d, map[string]any{"file": name, "prices": run.Prices})
		return run, nil
	}
	why := "no file"
	if len(run.Tried) > 0 {
		why = run.Tried[len(run.Tried)-1]
		if strings.HasPrefix(why, date+": ") || len(run.Tried) == 1 {
			why = strings.Join(run.Tried, "; ")
		}
	}
	if tmpl == "" {
		why += ". Give the file's address in Settings → Connections → CCIL prices"
	}
	return run, fmt.Errorf("CCIL's prices of %s were not fetched: %s", fmtDay(date), why)
}

// ccilDay is a date as CCIL's file names write it (2026-10-16 → 16102026).
func ccilDay(d string) string {
	t, err := time.Parse("2006-01-02", d)
	if err != nil {
		return d
	}
	return t.Format("02012006")
}

// ccilTick fetches the latest business day's prices when the fetch is on:
// the day's own from 19:00, else the day before. It tries every half
// hour until they are in; a day still missing at 23:00, or by 10:00 the
// next morning, raises one work item.
func (o *Ops) ccilTick(ctx context.Context, tenant string, now time.Time) {
	cn, ok := o.ccilConn(ctx, tenant)
	if !ok || cn.Values["fetch"] != "On" {
		return
	}
	set, _ := o.Settings(ctx, tenant)
	loc := now.In(ist)
	today, hm := loc.Format("2006-01-02"), loc.Format("15:04")
	d := today
	if hm < ccilFromHM || !set.IsBusinessDay(today) {
		d = set.PrevBusinessDay(today)
	}
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cCCILState, "state", CCILState{})
	if st.LastOK == d {
		return
	}
	if _, _, err := docs.Get[GSecPrices](ctx, o.Docs, tenant, cGSecPrice, d); err == nil {
		return // loaded by hand
	}
	if t, err := time.Parse(time.RFC3339, st.LastRun); err == nil && st.LastDate == d && now.Sub(t) < 30*time.Minute {
		return
	}
	st.LastRun, st.LastDate = now.Format(time.RFC3339), d
	run, err := o.CCILFetch(ctx, tenant, SystemActor, d, 0)
	if err == nil {
		st.LastOK, st.LastFile, st.LastError = d, run.File, ""
		o.closeWork(ctx, tenant, SystemActor, "periodic", "ccilfetch:"+d, "fetched")
	} else {
		st.LastError = err.Error()
		if (d == today && hm >= "23:00") || (d != today && hm >= "10:00" && hm < ccilFromHM) {
			o.raiseWork(ctx, tenant, "periodic", "ccilfetch:"+d, "CCIL's prices of "+fmtDay(d)+" not fetched",
				err.Error()+". Download MTMPRICE"+ccilDay(d)+" from CCIL and load it on Exchange files, or check the CCIL prices connection.", "", RoleMaker, "xfiles", "")
		}
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cCCILState, "state", st, SystemActor)
}

// CCILView is the fetch for the Exchange files screen.
type CCILView struct {
	Set    bool      `json:"set"`
	Fetch  bool      `json:"fetch"`
	State  CCILState `json:"state"`
	Latest string    `json:"latest,omitempty"` // the latest price date loaded
	Prices int       `json:"prices,omitempty"`
}

// CCILStatus is the CCIL fetch's state and the latest prices loaded.
func (o *Ops) CCILStatus(ctx context.Context, tenant string) CCILView {
	cn, ok := o.ccilConn(ctx, tenant)
	v := CCILView{Set: ok, Fetch: ok && cn.Values["fetch"] == "On"}
	v.State, _, _ = docs.GetOr(ctx, o.Docs, tenant, cCCILState, "state", CCILState{})
	if p, ok := o.gsecPricesOn(ctx, tenant, o.now().In(ist).Format("2006-01-02")); ok {
		v.Latest, v.Prices = p.Date, len(p.Prices)
	}
	return v
}

// CCILFetchNow is "Fetch now": the date's prices, or the latest business
// day's when no date is given, going back up to 4 business days.
func (o *Ops) CCILFetchNow(ctx context.Context, tenant, actor, date string) (CCILRun, error) {
	if date == "" {
		set, _ := o.Settings(ctx, tenant)
		now := o.now().In(ist)
		date = now.Format("2006-01-02")
		if now.Format("15:04") < ccilFromHM || !set.IsBusinessDay(date) {
			date = set.PrevBusinessDay(date)
		}
	}
	run, err := o.CCILFetch(ctx, tenant, actor, date, 4)
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cCCILState, "state", CCILState{})
	st.LastRun, st.LastDate = o.now().Format(time.RFC3339), date
	if err == nil {
		st.LastOK, st.LastFile, st.LastError = run.Date, run.File, ""
	} else {
		st.LastError = err.Error()
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cCCILState, "state", st, SystemActor)
	return run, err
}
