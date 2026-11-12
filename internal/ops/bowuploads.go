package ops

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"ledgerline/internal/charges"
	"ledgerline/internal/docs"
	"ledgerline/internal/money"
	"ledgerline/internal/xfile"
)

// ---- BOW upload files: every other upload of BOW's format workbook ----
//
// Besides the deposit and combined holding files (bowfiles.go), Ledgerline
// writes every upload in BOW File_Formats.xlsx, each in its sheet's layout
// (config/formats/bow-*.json, the kind is the layout's id), comma-separated
// (SLB pipe-separated), no header, CRLF. BOW's uploader takes them from
// 17:30 to 08:00 (user deposits and stocks also during market hours).
//
// Where the data comes from:
//   - users (IBT, offline/CTCL, dealers), client dealer mapping, NNF
//     location IDs: the client master, the dealer register (terminals with
//     their location and trading IDs) and the firm's values in Connections →
//     BOW upload files (Owner Id, offline client groups, dealer IDs, client
//     trader and location IDs). Passwords are NEVER written: BOW's Password
//     field is left empty though BOW wants one, and the file's note says so.
//   - trading rights: the client's segment status, per BOW exchange segment
//     the firm trades on (open: YYYYYYYYYYNY; otherwise NNNNNNNNNNNY with
//     the reason).
//   - risk preferences, contract margins, right limits and profile names:
//     the firm's lines in the connection, for each active client with the
//     segment open. A NOT NULL field with no Ledgerline source and no value
//     in the connection refuses the file, naming the field and the setting.
//   - holdings (DP stocks, DP only, pool only): as the combined file.
//   - PAN, banks (every registered account, primary first), KRA status:
//     the client master. CP codes: the NRI details (F&O).
//   - brokerage master and client mapping: the brokerage plans in force (the
//     modes BOW can express; others listed in the note) with the charge
//     schedule's statutory rates, and each client's plan.
//   - manual positions: the carried derivatives positions at the previous
//     business day's close.
//   - SLB holdings: Ledgerline keeps none; the file is empty with a note.
//
// The client and user files carry the clients (or dealers) that are new or
// changed since the last file of that kind, by what the file says about
// them (cBOWSent), and those already written the same day; with all, every
// one. A file with nothing to send is empty, with a note.

const cBOWSent = "bowsent" // kind|key -> bowSentState

// bowUploadKinds are the uploads of this file, by layout id.
var bowUploadKinds = []string{"bow-ibt-users", "bow-offline-users", "bow-dealer-users", "bow-dealer-mapping", "bow-dp-stocks", "bow-only-dp-holding", "bow-only-pool-holding",
	"bow-nnf-location", "bow-trading-rights", "bow-risk-preferences", "bow-contract-margin", "bow-right-limits", "bow-profile-mapping", "bow-slb-holding",
	"bow-update-pan", "bow-bank", "bow-kra-status", "bow-brokerage-master", "bow-brokerage-mapping", "bow-cp-code", "bow-manual-position"}

func init() {
	for _, k := range bowUploadKinds {
		xWrittenOn[k] = "Live RMS → Front-office files → BOW uploads"
	}
}

type bowSentState struct {
	Kind string `json:"kind"`
	Key  string `json:"key"`
	Hash string `json:"hash"`
	On   string `json:"on"`
}

// bowItem is what a client or user file says about one client or user.
type bowItem struct {
	Key  string
	Rows []xfile.Row
	// Known: written only when an earlier file had it (a client no longer
	// active, whose rights are taken back).
	Known bool
}

// bowBuilt is a file's content before it is written.
type bowBuilt struct {
	Rows    []xfile.Row // files not tracked by client
	Items   []bowItem   // tracked files
	Tracked bool
	Notes   []string
	Empty   string // the note when there is nothing to send
}

func (b *bowBuilt) note(f string, a ...any) { b.Notes = append(b.Notes, fmt.Sprintf(f, a...)) }

// bowLeftOut notes the clients or users left out and why (the first 20).
func (b *bowBuilt) leftOut(what string, xs []string) {
	if len(xs) == 0 {
		return
	}
	more := ""
	if len(xs) > 20 {
		more, xs = fmt.Sprintf(" and %d more", len(xs)-20), xs[:20]
	}
	b.note("%d left out (%s): %s%s", len(xs), what, strings.Join(xs, ", "), more)
}

// bowNeed refuses a file: a NOT NULL field of BOW's with no Ledgerline
// source and no value in the connection.
func bowNeed(field, setting string) error {
	return invalid("BOW's %s is NOT NULL and Ledgerline has no value for it: fill %s in Connections → BOW upload files", field, setting)
}

// bowText makes a value safe in BOW's comma-separated files: BOW reads no
// quoting, so a comma, quote, pipe or line break becomes a space.
func bowText(s string) string {
	return strings.Join(strings.Fields(strings.NewReplacer(",", " ", "\"", " ", "|", " ", "\r", " ", "\n", " ").Replace(s)), " ")
}

// bowList splits a setting's list (commas, semicolons, spaces).
func bowList(s string) []string {
	var out []string
	for _, x := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '\n' }) {
		out = append(out, strings.ToUpper(x))
	}
	return out
}

// bowEntries splits a setting's lines (semicolons or line breaks).
func bowEntries(s string) []string {
	var out []string
	for _, x := range strings.FieldsFunc(s, func(r rune) bool { return r == ';' || r == '\n' }) {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

// bowNames splits a name into BOW's first and last name: the first word,
// and the rest (the first again for a one-word name: both are NOT NULL).
func bowNames(name string) (string, string) {
	f := strings.Fields(bowText(name))
	if len(f) == 0 {
		return "", ""
	}
	if len(f) == 1 {
		return f[0], f[0]
	}
	return f[0], strings.Join(f[1:], " ")
}

// BOW's exchanges, and its market names for Ledgerline's segments.
var (
	bowExchanges  = []string{"BSE", "NSE", "MCX", "NCDEX"}
	bowMarketName = map[string]string{"CM": "EQUITY", "FO": "DERIVATIVE", "CD": "CURRENCY", "COM": "COMMODITIES"}
	bowMarketSeg  = map[string]string{"EQUITY": "CM", "EQ": "CM", "DERIVATIVE": "FO", "DERIVATIVES": "FO", "FO": "FO", "CURRENCY": "CD", "CUR": "CD", "CDS": "CD",
		"COMMODITIES": "COM", "COMMODITY": "COM", "CF": "COM", "COM": "COM"}
)

// bowSeg is an exchange segment the firm trades on BOW's exchanges.
type bowSeg struct{ Exchange, Segment string }

func bowFirmSegments(set Settings) []bowSeg {
	var out []bowSeg
	for _, c := range set.activeSegments() {
		if contains(bowExchanges, c.Exchange) {
			out = append(out, bowSeg{c.Exchange, c.Segment})
		}
	}
	return out
}

// bowDealerIDs are the BOW dealer IDs the connection gives Ledgerline's
// dealers (DL-0001=DEALER1; …).
func bowDealerIDs(cfg bowFilesCfg) (map[string]string, error) {
	m := map[string]string{}
	for _, e := range bowEntries(strings.ReplaceAll(cfg.v["dealer_ids"], ",", ";")) {
		k, v, ok := strings.Cut(e, "=")
		if !ok || strings.TrimSpace(k) == "" || strings.TrimSpace(v) == "" {
			return nil, invalid("BOW dealer IDs: %q is not DL-0001=DEALER1", e)
		}
		m[strings.ToUpper(strings.TrimSpace(k))] = strings.ToUpper(strings.TrimSpace(v))
	}
	return m, nil
}

func bowDealerID(ids map[string]string, d Dealer) string {
	if x := ids[strings.ToUpper(d.ID)]; x != "" {
		return x
	}
	return strings.ToUpper(strings.ReplaceAll(d.ID, "-", ""))
}

// bowTrack picks the tracked items to write (new, changed, already written
// on date; with all, every one) and the states to keep once written.
func (o *Ops) bowTrack(ctx context.Context, tenant, kind, date string, all bool, items []bowItem) ([]xfile.Row, []bowSentState) {
	prior, _ := docs.List[bowSentState](ctx, o.Docs, tenant, cBOWSent, kind+"|")
	last := map[string]bowSentState{}
	for _, p := range prior {
		last[p.Key] = p
	}
	var rows []xfile.Row
	var changed []bowSentState
	for _, it := range items {
		was, known := last[it.Key]
		if it.Known && !known {
			continue
		}
		h := finspotHash(it.Rows)
		if !all && known && was.Hash == h && was.On != date {
			continue
		}
		rows = append(rows, it.Rows...)
		if !known || was.Hash != h {
			changed = append(changed, bowSentState{Kind: kind, Key: it.Key, Hash: h, On: date})
		}
	}
	return rows, changed
}

// bowUpload writes one of bowUploadKinds.
func (o *Ops) bowUpload(ctx context.Context, tenant, actor, kind, date string, all bool) (BOWUpload, error) {
	if err := o.require(ctx, tenant, actor, "Downloading BOW upload files", RoleMaker, RoleChecker, RoleRisk); err != nil {
		return BOWUpload{}, err
	}
	if !validDate(date) {
		return BOWUpload{}, invalid("give a date (YYYY-MM-DD)")
	}
	s, err := finspotSpec(kind)
	if err != nil {
		return BOWUpload{}, err
	}
	cfg, err := o.bowFilesConfig(ctx, tenant)
	if err != nil {
		return BOWUpload{}, err
	}
	bowFileMu.Lock()
	defer bowFileMu.Unlock()
	var b bowBuilt
	switch kind {
	case "bow-ibt-users", "bow-offline-users":
		b, err = o.bowUserAdds(ctx, tenant, cfg, kind == "bow-offline-users")
	case "bow-dealer-users":
		b, err = o.bowDealerUsers(ctx, tenant, cfg)
	case "bow-dealer-mapping":
		b, err = o.bowDealerMapping(ctx, tenant, cfg)
	case "bow-dp-stocks", "bow-only-dp-holding", "bow-only-pool-holding":
		b, err = o.bowStockFile(ctx, tenant, cfg, kind, date)
	case "bow-slb-holding":
		b.Empty = "Ledgerline keeps no SLB (securities lending and borrowing) positions, so there is nothing to send; upload BOW's SLB holdings from the SLB records if the firm lends or borrows"
	case "bow-nnf-location":
		b, err = o.bowNNF(ctx, tenant, cfg, date)
	case "bow-trading-rights":
		b, err = o.bowTradingRights(ctx, tenant)
	case "bow-risk-preferences":
		b, err = o.bowRiskPrefs(ctx, tenant, cfg)
	case "bow-contract-margin":
		b, err = o.bowContractMargin(ctx, tenant, cfg)
	case "bow-right-limits":
		b, err = o.bowRightLimits(ctx, tenant, cfg)
	case "bow-profile-mapping":
		b, err = o.bowProfiles(ctx, tenant, cfg)
	case "bow-update-pan":
		b, err = o.bowPAN(ctx, tenant)
	case "bow-bank":
		b, err = o.bowBanks(ctx, tenant)
	case "bow-kra-status":
		b, err = o.bowKRA(ctx, tenant)
	case "bow-brokerage-master":
		b, err = o.bowBrokerageMaster(ctx, tenant, cfg, date)
	case "bow-brokerage-mapping":
		b, err = o.bowBrokerageMapping(ctx, tenant, date)
	case "bow-cp-code":
		b, err = o.bowCPCodes(ctx, tenant)
	case "bow-manual-position":
		b, err = o.bowManualPositions(ctx, tenant, cfg, date)
	default:
		return BOWUpload{}, fmt.Errorf("%w: no BOW file %s", ErrNotFound, kind)
	}
	if err != nil {
		return BOWUpload{}, err
	}
	rows := b.Rows
	var changed []bowSentState
	if b.Tracked {
		rows, changed = o.bowTrack(ctx, tenant, kind, date, all, b.Items)
	}
	data, err := s.Write(rows)
	if err != nil {
		return BOWUpload{}, err
	}
	for _, x := range changed {
		if err := docs.Upsert(ctx, o.Docs, tenant, cBOWSent, kind+"|"+x.Key, x, actor); err != nil {
			return BOWUpload{}, err
		}
	}
	name, err := s.FileName(xfile.Vars{"date": date})
	if err != nil {
		return BOWUpload{}, err
	}
	notes := b.Notes
	if len(rows) == 0 {
		why := b.Empty
		switch {
		case why != "":
		case b.Tracked && len(b.Items) > 0:
			why = "Nothing to send: nothing changed since the last file (download with every client to send them all again)"
		default:
			why = "Nothing to send"
		}
		notes = append([]string{why}, notes...)
	}
	u := BOWUpload{Name: name, Data: data, Lines: len(rows), Note: strings.Join(notes, ". ")}
	o.log(ctx, tenant, actor, "frontoffice."+strings.ReplaceAll(kind, "-", "_"), name, map[string]any{"date": date, "lines": len(rows), "all": all, "note": u.Note})
	return u, nil
}

// ---- users ----

func (o *Ops) bowOwner(cfg bowFilesCfg) (string, error) {
	owner := cfg.v["owner_id"]
	if owner == "" {
		return "", bowNeed("Owner Id", "Owner Id for user additions")
	}
	if strings.ContainsAny(owner, ",\"| \r\n") {
		return "", invalid("the BOW Owner Id has no spaces, commas or quotes")
	}
	return owner, nil
}

const bowPasswordNote = "Password left empty: Ledgerline never writes passwords, and BOW wants one ('Password should not be blank'), so set the initial password in BOW or in the file just before uploading"

// bowUserAdds are the IBT (internet) or offline (CTCL) client additions:
// clients in the connection's offline groups (default OFFLINE) are
// offline, everyone else IBT.
func (o *Ops) bowUserAdds(ctx context.Context, tenant string, cfg bowFilesCfg, offline bool) (bowBuilt, error) {
	b := bowBuilt{Tracked: true}
	owner, err := o.bowOwner(cfg)
	if err != nil {
		return b, err
	}
	groups := bowList(firstOf(cfg.v["offline_groups"], "OFFLINE"))
	cs, err := o.tradingClientMap(ctx, tenant)
	if err != nil {
		return b, err
	}
	var missing []string
	for _, u := range sortedKeys(cs) {
		c := cs[u]
		if c.Status != "Active" {
			continue
		}
		off := false
		for _, t := range c.Tags {
			if contains(groups, strings.ToUpper(t)) {
				off = true
			}
		}
		if off != offline {
			continue
		}
		first, last := bowNames(c.Name)
		var lack []string
		if len(u) > 10 {
			lack = append(lack, "client ID over 10 characters")
		}
		if first == "" {
			lack = append(lack, "name")
		}
		if !reEmail.MatchString(c.Email) {
			lack = append(lack, "e-mail")
		}
		if strings.TrimSpace(c.Mobile) == "" {
			lack = append(lack, "mobile")
		}
		if !rePAN.MatchString(c.PAN) {
			lack = append(lack, "PAN")
		}
		if len(lack) > 0 {
			missing = append(missing, u+" ("+strings.Join(lack, ", ")+")")
			continue
		}
		b.Items = append(b.Items, bowItem{Key: u, Rows: []xfile.Row{{Values: map[string]string{
			"LoginID": u, "OwnerID": owner, "UserID": u, "UserType": "1", "Password": "", "FirstName": first, "LastName": last,
			"Email": bowText(c.Email), "DefaultMarketwatch": "N", "Mobile": bowText(c.Mobile), "PAN": c.PAN,
			"MarginProductPref": bowText(cfg.v["margin_product_pref"]), "DeliveryProductPref": bowText(cfg.v["delivery_product_pref"])}}}})
	}
	if len(b.Items) > 0 {
		b.note(bowPasswordNote)
	}
	b.leftOut("missing details BOW needs", missing)
	if len(b.Items) == 0 {
		if offline {
			b.Empty = fmt.Sprintf("Nothing to send: no active client is in the offline client groups (%s)", strings.Join(groups, ", "))
		} else {
			b.Empty = "Nothing to send: no active internet client"
		}
	}
	return b, nil
}

// bowDealerUsers are the dealer additions: every active dealer.
func (o *Ops) bowDealerUsers(ctx context.Context, tenant string, cfg bowFilesCfg) (bowBuilt, error) {
	b := bowBuilt{Tracked: true}
	owner, err := o.bowOwner(cfg)
	if err != nil {
		return b, err
	}
	ids, err := bowDealerIDs(cfg)
	if err != nil {
		return b, err
	}
	ds, err := o.allDealers(ctx, tenant)
	if err != nil {
		return b, err
	}
	var missing []string
	for _, d := range ds {
		if d.Status != DealerActive {
			continue
		}
		id := bowDealerID(ids, d)
		first, last := bowText(d.FirstName), bowText(strings.TrimSpace(d.MiddleName+" "+d.LastName))
		if last == "" {
			last = first
		}
		var lack []string
		if len(id) > 10 || strings.ContainsAny(id, ",\"| ") {
			lack = append(lack, "BOW dealer ID "+id+" over 10 characters or with spaces")
		}
		if first == "" {
			lack = append(lack, "name")
		}
		if !reEmail.MatchString(strings.TrimSpace(d.Email)) {
			lack = append(lack, "e-mail")
		}
		if strings.TrimSpace(d.Mobile) == "" {
			lack = append(lack, "mobile")
		}
		if !rePAN.MatchString(strings.ToUpper(strings.TrimSpace(d.PAN))) {
			lack = append(lack, "PAN")
		}
		if len(lack) > 0 {
			missing = append(missing, d.ID+" ("+strings.Join(lack, ", ")+")")
			continue
		}
		b.Items = append(b.Items, bowItem{Key: d.ID, Rows: []xfile.Row{{Values: map[string]string{
			"LoginID": id, "OwnerID": owner, "UserID": id, "UserType": "2", "Password": "", "FirstName": first, "LastName": last,
			"Email": bowText(d.Email), "DefaultMarketwatch": "N", "Mobile": bowText(d.Mobile), "PAN": strings.ToUpper(strings.TrimSpace(d.PAN))}}}})
	}
	if len(b.Items) > 0 {
		b.note(bowPasswordNote)
	}
	b.leftOut("missing details BOW needs", missing)
	if len(b.Items) == 0 {
		b.Empty = "Nothing to send: no active dealer in the dealer register"
	}
	return b, nil
}

// bowDealerMapping maps each active client to the connection's dealers for
// every client and the active dealers of the client's authorised person.
func (o *Ops) bowDealerMapping(ctx context.Context, tenant string, cfg bowFilesCfg) (bowBuilt, error) {
	b := bowBuilt{Tracked: true}
	ids, err := bowDealerIDs(cfg)
	if err != nil {
		return b, err
	}
	every := bowList(cfg.v["client_dealers"])
	ds, err := o.allDealers(ctx, tenant)
	if err != nil {
		return b, err
	}
	byAP := map[string][]string{}
	for _, d := range ds {
		if d.Status == DealerActive && strings.TrimSpace(d.Partner) != "" {
			p := strings.ToUpper(strings.TrimSpace(d.Partner))
			byAP[p] = append(byAP[p], bowDealerID(ids, d))
		}
	}
	cs, err := o.tradingClientMap(ctx, tenant)
	if err != nil {
		return b, err
	}
	for _, u := range sortedKeys(cs) {
		c := cs[u]
		if c.Status != "Active" {
			continue
		}
		var rows []xfile.Row
		seen := map[string]bool{}
		for _, d := range append(append([]string{}, every...), byAP[strings.ToUpper(strings.TrimSpace(c.Partner))]...) {
			if d == "" || seen[d] {
				continue
			}
			seen[d] = true
			rows = append(rows, xfile.Row{Values: map[string]string{"ClientID": u, "DealerID": bowText(d)}})
		}
		if len(rows) > 0 {
			b.Items = append(b.Items, bowItem{Key: u, Rows: rows})
		}
	}
	if len(b.Items) == 0 {
		b.Empty = "Nothing to send: no client has a dealer. Give the dealers every client is mapped to in Connections → BOW upload files, or the authorised persons' dealers in the dealer register"
	}
	return b, nil
}

// ---- holdings ----

func (o *Ops) bowStockFile(ctx context.Context, tenant string, cfg bowFilesCfg, kind, date string) (bowBuilt, error) {
	var b bowBuilt
	hs, unknown, err := o.bowHoldings(ctx, tenant, date)
	if err != nil {
		return b, err
	}
	for _, h := range hs {
		v := map[string]string{"BackOfficeID": h.UCC, "NSESymbol": h.Symbol, "Series": h.Series, "BSEScripCode": h.BSECode}
		switch kind {
		case "bow-dp-stocks":
			if h.DP == 0 {
				continue
			}
			v = map[string]string{"ClientID": h.UCC, "Symbol": h.Symbol, "Series": h.Series, "Quantity": strconv.FormatInt(h.DP, 10), "Filler": "0", "BSEScripCode": h.BSECode}
		case "bow-only-dp-holding":
			if h.DP == 0 {
				continue
			}
			v["DPQuantity"], v["DPAvePrice"], v["DPCFSHaircut"] = strconv.FormatInt(h.DP, 10), h.DPAvg.Fixed(2), cfg.DPHaircut.String()
		default:
			if h.Pool == 0 {
				continue
			}
			v["PoolQuantity"], v["PoolAvePrice"], v["PoolCFSHaircut"] = strconv.FormatInt(h.Pool, 10), h.PoolAvg.Fixed(2), cfg.PoolHcut.String()
		}
		b.Rows = append(b.Rows, xfile.Row{Values: v})
	}
	if len(unknown) > 0 {
		b.note("%d securities left out: neither an NSE symbol and series nor a BSE code (load the NSE security master and BSE scrip file on Exchange files)", len(unknown))
	}
	if kind == "bow-only-pool-holding" {
		b.Empty = "Nothing to send: no active client has delivery buys from the previous business day not yet received"
	} else {
		b.Empty = "Nothing to send: no active client has a sellable demat holding"
	}
	return b, nil
}

// ---- exchange identifiers and rights ----

// bowNNF: each active dealer's open terminals with a location ID, and each
// active client's trader and location IDs from the connection for the
// segments open to them.
func (o *Ops) bowNNF(ctx context.Context, tenant string, cfg bowFilesCfg, date string) (bowBuilt, error) {
	b := bowBuilt{Tracked: true}
	set, _ := o.Settings(ctx, tenant)
	broker := func(ex string) (string, error) {
		m := strings.TrimSpace(set.MemberCodes[ex])
		if m == "" {
			return "", invalid("BOW's Broker Id is NOT NULL: set the firm's %s member code in Settings", ex)
		}
		return bowText(m), nil
	}
	type entry struct{ Exch, Market, Seg, Trader, Location string }
	var es []entry
	for _, e := range bowEntries(cfg.v["client_nnf"]) {
		k, v, ok := strings.Cut(e, "=")
		f := strings.Fields(strings.ToUpper(k))
		t, l, ok2 := strings.Cut(v, ":")
		if !ok || !ok2 || len(f) != 2 || !contains(bowExchanges, f[0]) || bowMarketSeg[f[1]] == "" || strings.TrimSpace(t) == "" || strings.TrimSpace(l) == "" {
			return b, invalid("client trader and location IDs: %q is not EXCHANGE MARKET=TraderId:LocationId (e.g. BSE EQUITY=123400901:1111111111111012)", e)
		}
		es = append(es, entry{f[0], f[1], bowMarketSeg[f[1]], bowText(t), bowText(l)})
	}
	ids, err := bowDealerIDs(cfg)
	if err != nil {
		return b, err
	}
	ds, err := o.allDealers(ctx, tenant)
	if err != nil {
		return b, err
	}
	for _, d := range ds {
		if d.Status != DealerActive {
			continue
		}
		id := bowDealerID(ids, d)
		var rows []xfile.Row
		for _, t := range d.openTerminals(date) {
			if strings.TrimSpace(t.LocationID) == "" || t.pattern() || !contains(bowExchanges, t.Exchange) {
				continue
			}
			bid, err := broker(t.Exchange)
			if err != nil {
				return b, err
			}
			for _, sg := range t.Segments {
				if bowMarketName[sg] == "" {
					continue
				}
				rows = append(rows, xfile.Row{Values: map[string]string{"Exchange": t.Exchange, "Market": bowMarketName[sg], "TraderID": bowText(firstOf(t.TradingID, t.TerminalID)),
					"BrokerID": bid, "LocationID": bowText(t.LocationID), "LoginID": id}})
			}
		}
		if len(rows) > 0 {
			b.Items = append(b.Items, bowItem{Key: "dealer:" + d.ID, Rows: rows})
		}
	}
	if len(es) > 0 {
		cs, err := o.tradingClientMap(ctx, tenant)
		if err != nil {
			return b, err
		}
		for _, u := range sortedKeys(cs) {
			c := cs[u]
			if c.Status != "Active" {
				continue
			}
			var rows []xfile.Row
			for _, e := range es {
				if !c.SegmentOpen(e.Seg) {
					continue
				}
				bid, err := broker(e.Exch)
				if err != nil {
					return b, err
				}
				rows = append(rows, xfile.Row{Values: map[string]string{"Exchange": e.Exch, "Market": e.Market, "TraderID": e.Trader, "BrokerID": bid, "LocationID": e.Location, "LoginID": u}})
			}
			if len(rows) > 0 {
				b.Items = append(b.Items, bowItem{Key: "client:" + u, Rows: rows})
			}
		}
	}
	if len(b.Items) == 0 {
		b.Empty = "Nothing to send: no active dealer has an open terminal with a location ID, and no client Trader Id and Location ID are set in Connections → BOW upload files"
	}
	return b, nil
}

const (
	bowAllow = "YYYYYYYYYYNY" // everything for self and others, no pro orders, broadcast
	bowBlock = "NNNNNNNNNNNY" // nothing but broadcast
)

// bowTradingRights: per client, per BOW exchange segment the firm trades
// on, allowed when the client's segment is open, blocked otherwise; a
// client no longer active who was in an earlier file is blocked everywhere.
func (o *Ops) bowTradingRights(ctx context.Context, tenant string) (bowBuilt, error) {
	b := bowBuilt{Tracked: true}
	set, _ := o.Settings(ctx, tenant)
	segs := bowFirmSegments(set)
	if len(segs) == 0 {
		b.Empty = "Nothing to send: the firm trades on none of BOW's exchanges (Settings → member codes and clearing)"
		return b, nil
	}
	cs, err := o.tradingClientMap(ctx, tenant)
	if err != nil {
		return b, err
	}
	for _, u := range sortedKeys(cs) {
		c := cs[u]
		active := c.Status == "Active"
		var rows []xfile.Row
		for _, sg := range segs {
			perm, why := bowAllow, ""
			switch {
			case !active:
				why = "client " + strings.ToLower(firstOf(c.Status, "not active"))
			case !contains(c.Segments, sg.Segment):
				why = "segment not registered"
			case !c.SegmentOpen(sg.Segment):
				why = "segment " + strings.ToLower(c.SegmentStatus(sg.Segment))
			}
			cat := "SECURITIES"
			if sg.Segment == "COM" {
				cat = bowCatCommodities
			}
			seg := ""
			if sg.Segment == "CM" {
				seg = "EQUITY"
			}
			rem := ""
			if why != "" {
				perm, rem = bowBlock, bowText("BLOCKED: "+why)
			}
			rows = append(rows, xfile.Row{Values: map[string]string{"ClientID": u, "Default2": "1", "Default3": "1", "Permission": perm, "Category": cat,
				"Exchange": sg.Exchange, "Market": bowMarketName[sg.Segment], "Segment": seg, "Remarks": rem, "DeleteFlag": "N"}})
		}
		b.Items = append(b.Items, bowItem{Key: u, Rows: rows, Known: !active})
	}
	return b, nil
}

var bowRiskKeys = []string{"RealisedProfit", "RealisedLoss", "UnrealisedProfit", "UnrealisedLoss", "OptionsRealisedPL", "OptionBuyUnrealisedPL", "OptionSellUnrealisedPL", "NetRealisedLoss", "NetUnrealisedLoss"}

func (o *Ops) bowRiskPrefs(ctx context.Context, tenant string, cfg bowFilesCfg) (bowBuilt, error) {
	b := bowBuilt{Tracked: true}
	vals := bowList(cfg.v["risk_preferences"])
	if len(vals) == 0 {
		return b, bowNeed("Consider Realised Profit … Consider Net UnRealised Loss (the nine risk preference values)", "Risk preferences")
	}
	if len(vals) != len(bowRiskKeys) {
		return b, invalid("risk preferences: give the nine values in BOW's order, each 1 (margin), 2 (MTM) or 3 (both)")
	}
	for _, v := range vals {
		if v != "1" && v != "2" && v != "3" {
			return b, invalid("risk preferences: %q is not 1 (margin), 2 (MTM) or 3 (both)", v)
		}
	}
	scope := bowList(firstOf(cfg.v["risk_scope"], "ALL,ALL,ALL"))
	if len(scope) != 3 {
		return b, invalid("risk preferences Exchange, Market, Product: give three values (e.g. ALL,ALL,ALL)")
	}
	cs, err := o.tradingClientMap(ctx, tenant)
	if err != nil {
		return b, err
	}
	for _, u := range sortedKeys(cs) {
		if cs[u].Status != "Active" {
			continue
		}
		v := map[string]string{"ClientCode": u, "Exchange": scope[0], "Market": scope[1], "Product": scope[2]}
		for i, k := range bowRiskKeys {
			v[k] = vals[i]
		}
		b.Items = append(b.Items, bowItem{Key: u, Rows: []xfile.Row{{Values: v}}})
	}
	if len(b.Items) == 0 {
		b.Empty = "Nothing to send: no active client"
	}
	return b, nil
}

func (o *Ops) bowContractMargin(ctx context.Context, tenant string, cfg bowFilesCfg) (bowBuilt, error) {
	b := bowBuilt{Tracked: true}
	type line struct{ Exch, Market, Segment, Seg, Intra, Deliv string }
	var ls []line
	for _, e := range bowEntries(cfg.v["contract_margins"]) {
		k, v, _ := strings.Cut(e, "=")
		f := strings.Split(k, ",")
		in, de, ok := strings.Cut(v, ":")
		bad := invalid("contract margin lines: %q is not Exchange,Market,Segment=Intraday:Delivery (e.g. BSE,EQUITY,EQUITY=20:100)", e)
		if len(f) != 3 || !ok {
			return b, bad
		}
		for i := range f {
			f[i] = strings.ToUpper(bowText(f[i]))
		}
		in, de = strings.TrimSpace(in), strings.TrimSpace(de)
		x, e1 := money.ParseDec(in)
		y, e2 := money.ParseDec(de)
		if !contains(bowExchanges, f[0]) || bowMarketSeg[f[1]] == "" || f[2] == "" || e1 != nil || e2 != nil || x.Sign() <= 0 || y.Sign() <= 0 {
			return b, bad
		}
		ls = append(ls, line{f[0], f[1], f[2], bowMarketSeg[f[1]], in, de})
	}
	if len(ls) == 0 {
		return b, bowNeed("Intraday Margin and Delivery Margin", "Contract margin lines")
	}
	cs, err := o.tradingClientMap(ctx, tenant)
	if err != nil {
		return b, err
	}
	for _, u := range sortedKeys(cs) {
		c := cs[u]
		if c.Status != "Active" {
			continue
		}
		var rows []xfile.Row
		for _, l := range ls {
			if c.SegmentOpen(l.Seg) {
				rows = append(rows, xfile.Row{Values: map[string]string{"BackofficeID": u, "Exchange": l.Exch, "Market": l.Market, "Segment": l.Segment, "IntradayMargin": l.Intra, "DeliveryMargin": l.Deliv}})
			}
		}
		if len(rows) > 0 {
			b.Items = append(b.Items, bowItem{Key: u, Rows: rows})
		}
	}
	if len(b.Items) == 0 {
		b.Empty = "Nothing to send: no active client has a segment of the contract margin lines open"
	}
	return b, nil
}

var bowRightLimitFields = []string{"MaxOrderQuantity", "MaxOrderValue", "TradeTurnoverLimit", "TurnoverLimit", "TradeGrossExposure", "TradeNetExposure", "GrossExposure", "NetExposure",
	"BuyAmount", "SellAmount", "BuyOutstandingAmount", "SellOutstandingAmount", "TotalBuyAmount", "TotalSellAmount"}

func (o *Ops) bowRightLimits(ctx context.Context, tenant string, cfg bowFilesCfg) (bowBuilt, error) {
	b := bowBuilt{Tracked: true}
	type line struct {
		v   map[string]string
		com bool
	}
	var ls []line
	for _, e := range bowEntries(cfg.v["right_limits"]) {
		k, rest, _ := strings.Cut(e, ":")
		f := strings.Split(k, ",")
		bad := invalid("right limit lines: %q is not Category,EXCHANGE,MARKET,SEGMENT:Field=value,… (e.g. Securities,BSE,EQ,EQ:MaxOrderValue=500000)", e)
		if len(f) != 4 {
			return b, bad
		}
		for i := range f {
			f[i] = bowText(f[i])
		}
		cat := f[0]
		if !strings.EqualFold(cat, "securities") && !strings.EqualFold(cat, "commodities") || !contains(bowExchanges, strings.ToUpper(f[1])) || f[2] == "" {
			return b, bad
		}
		v := map[string]string{"UserLevel": "USER", "Category": cat, "Exchange": strings.ToUpper(f[1]), "Market": strings.ToUpper(f[2]), "Segment": strings.ToUpper(f[3])}
		n := 0
		for _, kv := range strings.Split(rest, ",") {
			if strings.TrimSpace(kv) == "" {
				continue
			}
			fk, fv, ok := strings.Cut(kv, "=")
			fk, fv = strings.TrimSpace(fk), strings.TrimSpace(fv)
			key := ""
			for _, x := range bowRightLimitFields {
				if strings.EqualFold(x, fk) {
					key = x
				}
			}
			if d, err := money.ParseDec(fv); !ok || key == "" || err != nil || d.Sign() < 0 {
				return b, invalid("right limit lines: %q is not one of BOW's limits (%s) with an amount", kv, strings.Join(bowRightLimitFields, ", "))
			}
			v[key] = fv
			n++
		}
		if n == 0 {
			return b, invalid("right limit lines: %q sets no limit", e)
		}
		ls = append(ls, line{v, strings.EqualFold(cat, "commodities")})
	}
	if len(ls) == 0 {
		return b, bowNeed("Category, EXCHANGE, MARKET and SEGMENT", "Right limit lines")
	}
	cs, err := o.tradingClientMap(ctx, tenant)
	if err != nil {
		return b, err
	}
	for _, u := range sortedKeys(cs) {
		c := cs[u]
		if c.Status != "Active" {
			continue
		}
		com, eq := false, false
		for _, s := range c.OpenSegments() {
			if s == "COM" {
				com = true
			} else {
				eq = true
			}
		}
		var rows []xfile.Row
		for _, l := range ls {
			if l.com && com || !l.com && eq {
				v := map[string]string{"LoginID": u}
				for k, x := range l.v {
					v[k] = x
				}
				rows = append(rows, xfile.Row{Values: v})
			}
		}
		if len(rows) > 0 {
			b.Items = append(b.Items, bowItem{Key: u, Rows: rows})
		}
	}
	if len(b.Items) == 0 {
		b.Empty = "Nothing to send: no active client has the category of the right limit lines open"
	}
	return b, nil
}

var bowProfileKeys = []struct{ Short, Key string }{{"TR", "TRProfile"}, {"CM", "CMProfile"}, {"RL", "RLProfile"}, {"UCON", "UCONProfile"}, {"MTM", "MTMProfile"}, {"IP", "IPProfile"}, {"EXC", "ExcIdeProfile"}}

func (o *Ops) bowProfiles(ctx context.Context, tenant string, cfg bowFilesCfg) (bowBuilt, error) {
	b := bowBuilt{Tracked: true}
	p := map[string]string{}
	for _, e := range bowEntries(strings.ReplaceAll(cfg.v["profiles"], ",", ";")) {
		k, v, ok := strings.Cut(e, "=")
		key := ""
		for _, x := range bowProfileKeys {
			if strings.EqualFold(strings.TrimSpace(k), x.Short) {
				key = x.Key
			}
		}
		if !ok || key == "" {
			return b, invalid("profile names: %q is not TR, CM, RL, UCON, MTM, IP or EXC = a profile name", e)
		}
		if v = bowText(v); v != "" {
			p[key] = v
		}
	}
	if len(p) == 0 {
		b.Empty = "Nothing to send: no BOW profile names are set in Connections → BOW upload files (Profile names)"
		return b, nil
	}
	cs, err := o.tradingClientMap(ctx, tenant)
	if err != nil {
		return b, err
	}
	for _, u := range sortedKeys(cs) {
		if cs[u].Status != "Active" {
			continue
		}
		v := map[string]string{"LoginID": u}
		for k, x := range p {
			v[k] = x
		}
		b.Items = append(b.Items, bowItem{Key: u, Rows: []xfile.Row{{Values: v}}})
	}
	return b, nil
}

// ---- client details ----

func (o *Ops) bowPAN(ctx context.Context, tenant string) (bowBuilt, error) {
	b := bowBuilt{Tracked: true}
	cs, err := o.tradingClientMap(ctx, tenant)
	if err != nil {
		return b, err
	}
	var bad []string
	for _, u := range sortedKeys(cs) {
		c := cs[u]
		if c.Status != "Active" {
			continue
		}
		if !rePAN.MatchString(c.PAN) {
			bad = append(bad, u)
			continue
		}
		b.Items = append(b.Items, bowItem{Key: u, Rows: []xfile.Row{{Values: map[string]string{"ClientID": u, "PAN": c.PAN}}}})
	}
	b.leftOut("no valid PAN", bad)
	return b, nil
}

var reDigitsOnly = regexp.MustCompile(`^[0-9]{10,}$`)

func (o *Ops) bowBanks(ctx context.Context, tenant string) (bowBuilt, error) {
	b := bowBuilt{Tracked: true}
	cs, err := o.tradingClientMap(ctx, tenant)
	if err != nil {
		return b, err
	}
	var bad []string
	ifscBranch := 0
	for _, u := range sortedKeys(cs) {
		c := cs[u]
		if c.Status != "Active" {
			continue
		}
		var rows []xfile.Row
		for _, a := range registeredBanks(c) {
			acct := strings.TrimSpace(a.Account)
			if !reDigitsOnly.MatchString(acct) || bowText(a.Bank) == "" {
				bad = append(bad, u+" "+maskAcct(acct))
				continue
			}
			br := bowText(a.Branch)
			if br == "" {
				br, ifscBranch = bowText(a.IFSC), ifscBranch+1
			}
			if br == "" {
				bad = append(bad, u+" "+maskAcct(acct))
				continue
			}
			st := "active"
			if !a.active() {
				st = "inactive"
			}
			rows = append(rows, xfile.Row{Values: map[string]string{"BackOfficeID": u, "BankName": bowText(a.Bank), "AccountNo": acct, "BranchName": br, "Status": st}})
		}
		if len(rows) > 0 {
			b.Items = append(b.Items, bowItem{Key: u, Rows: rows})
		}
	}
	if ifscBranch > 0 {
		b.note("%d accounts have no branch name in Ledgerline: their IFSC is written as the Branch Name", ifscBranch)
	}
	b.leftOut("accounts BOW cannot take: not digits only, shorter than 10, or no bank name", bad)
	return b, nil
}

// maskAcct shows an account number's last four digits only.
func maskAcct(a string) string {
	if len(a) <= 4 {
		return "account " + a
	}
	return "account …" + a[len(a)-4:]
}

// bowKRACodes map Ledgerline's KRA statuses to BOW's.
var bowKRACodes = map[string]string{"Validated": "A", "Registered": "A", "Rejected": "R", "On hold": "H", "Under process": "S"}

func (o *Ops) bowKRA(ctx context.Context, tenant string) (bowBuilt, error) {
	b := bowBuilt{Tracked: true}
	cs, err := o.tradingClientMap(ctx, tenant)
	if err != nil {
		return b, err
	}
	none := 0
	for _, u := range sortedKeys(cs) {
		c := cs[u]
		if c.Status != "Active" {
			continue
		}
		code := bowKRACodes[c.KRAStatus]
		if code == "" {
			none++
			continue
		}
		b.Items = append(b.Items, bowItem{Key: u, Rows: []xfile.Row{{Values: map[string]string{"BackOfficeID": u, "KRAStatus": code}}}})
	}
	if none > 0 {
		b.note("%d active clients left out: their KRA status is not loaded or not available at the KRA (load the KRA status on Compliance)", none)
	}
	return b, nil
}

func (o *Ops) bowCPCodes(ctx context.Context, tenant string) (bowBuilt, error) {
	b := bowBuilt{Tracked: true}
	set, _ := o.Settings(ctx, tenant)
	var fo []string
	for _, sg := range bowFirmSegments(set) {
		if sg.Segment == "FO" && (sg.Exchange == "BSE" || sg.Exchange == "NSE") {
			fo = append(fo, sg.Exchange)
		}
	}
	ns, err := docs.List[NRIDetails](ctx, o.Docs, tenant, cNRI, "")
	if err != nil {
		return b, err
	}
	cs, err := o.tradingClientMap(ctx, tenant)
	if err != nil {
		return b, err
	}
	sort.Slice(ns, func(i, j int) bool { return ns[i].UCC < ns[j].UCC })
	for _, n := range ns {
		c, ok := cs[n.UCC]
		cp := bowText(strings.ToUpper(n.CPCode))
		if !ok || c.Status != "Active" || cp == "" || !contains(c.Segments, "FO") {
			continue
		}
		var rows []xfile.Row
		for _, ex := range fo {
			rows = append(rows, xfile.Row{Values: map[string]string{"BackofficeID": n.UCC, "Exchange": ex, "Market": "DERIVATIVE", "CPCode": cp, "Default": "Y"}})
		}
		if len(rows) > 0 {
			b.Items = append(b.Items, bowItem{Key: n.UCC, Rows: rows})
		}
	}
	if len(b.Items) == 0 {
		b.Empty = "Nothing to send: no active NRI client trading derivatives has a custodial participant (CP) code"
	}
	return b, nil
}

// ---- brokerage ----

// bowBrokLine is one brokerage master line being built.
type bowBrokLine struct {
	v    map[string]string
	spec int // a line for one exchange or channel beats one for all
}

// bowBrokType maps a plan rule to BOW's brokerage type and values, or says
// why BOW cannot express it.
func bowBrokType(r charges.BrokerageRule) (string, string, string, string) {
	z := money.Dec{}
	switch {
	case r.MinPerUnit.Sign() > 0:
		return "", "", "", "a minimum per share"
	case r.MinPerLot.Sign() > 0:
		return "", "", "", "a minimum per lot"
	case r.MinPerNote.Sign() > 0:
		return "", "", "", "a minimum per contract note"
	}
	switch r.Mode {
	case charges.PlanZero:
		return "1", "0", "0", ""
	case charges.PlanPct:
		if r.Max.Sign() > 0 {
			return "", "", "", "a maximum per order"
		}
		if r.Min.Sign() > 0 {
			return "4", r.Pct.String(), r.Min.String(), ""
		}
		return "1", r.Pct.String(), "0", ""
	case charges.PlanFlat:
		return "2", "0", r.Flat.String(), ""
	case charges.PlanLowerOf:
		if r.Max.Sign() > 0 || r.Min.Sign() > 0 {
			return "", "", "", "a minimum or maximum with lower of flat and percent"
		}
		return "3", r.Pct.String(), r.Flat.String(), ""
	case charges.PlanPerLot:
		if r.Max.Sign() > 0 || r.Min.Sign() > 0 {
			return "", "", "", "a minimum or maximum per order with per lot"
		}
		return "5", z.String(), r.PerLot.String(), ""
	case charges.PlanPerUnit:
		return "", "", "", "per share"
	}
	return "", "", "", "mode " + r.Mode
}

// bowInstrument is BOW's instrument for index or stock derivatives.
func bowInstrument(exch string, fut, index bool) string {
	if exch == "BSE" {
		return map[[2]bool]string{{true, true}: "IF", {true, false}: "SF", {false, true}: "IO", {false, false}: "SO"}[[2]bool{fut, index}]
	}
	return map[[2]bool]string{{true, true}: "FUTIDX", {true, false}: "FUTSTK", {false, true}: "OPTIDX", {false, false}: "OPTSTK"}[[2]bool{fut, index}]
}

func (o *Ops) bowBrokerageMaster(ctx context.Context, tenant string, cfg bowFilesCfg, date string) (bowBuilt, error) {
	var b bowBuilt
	set, _ := o.Settings(ctx, tenant)
	firmEx := map[string][]string{}
	for _, sg := range bowFirmSegments(set) {
		firmEx[sg.Segment] = append(firmEx[sg.Segment], sg.Exchange)
	}
	channel := firstOf(cfg.v["brokerage_channel"], "online")
	dpSell := money.Dec{}
	if x := cfg.v["sell_dp_charge"]; x != "" {
		d, err := money.ParseDec(x)
		if err != nil || d.Sign() < 0 {
			return b, invalid("the DP charge per delivery sell is an amount in rupees")
		}
		dpSell = d
	}
	var disc [3]string
	if x := cfg.v["disclaimers"]; x != "" {
		for i, m := range strings.SplitN(x, "|", 3) {
			m = bowText(m)
			if len([]rune(m)) > 50 {
				return b, invalid("BOW's disclaimer messages hold 50 characters; %q is longer", m)
			}
			disc[i] = m
		}
	}
	bb, err := o.brokerageBook(ctx, tenant, date)
	if err != nil {
		return b, err
	}
	sched := o.App.Schedule
	var plans []Plan
	for _, p := range bb.plans {
		if !p.Archived {
			plans = append(plans, p)
		}
	}
	sort.Slice(plans, func(i, j int) bool { return plans[i].Name < plans[j].Name })
	unmapped := map[string]int{}
	noRate := map[string]bool{}
	otherChannel := 0
	for _, p := range plans {
		v, ok := p.At(date)
		name := bowText(p.Name)
		if !ok || name == "" {
			continue
		}
		lines := map[string]*bowBrokLine{}
		for _, key := range sortedKeys(v.Rules) {
			r := v.Rules[key]
			f := strings.Split(key, "|")
			for len(f) < 5 {
				f = append(f, charges.Any)
			}
			bucket, exch, chn, cs, tgt := charges.Bucket(f[0]), strings.ToUpper(f[1]), f[2], f[3], f[4]
			seg := segmentOf(bucket)
			if bowMarketName[seg] == "" {
				continue
			}
			if chn != charges.Any && !strings.EqualFold(chn, channel) {
				otherChannel++
				continue
			}
			typ, v1, v2, why := bowBrokType(r)
			fut := strings.HasSuffix(string(bucket), "-FUT")
			index := false
			switch cs {
			case charges.Any:
			case charges.CaseIndex, charges.CaseStock:
				if seg != "FO" {
					why = "the " + cs + " case outside equity derivatives"
				}
				index = cs == charges.CaseIndex
			default:
				why = "the " + cs + " case"
			}
			sym := ""
			switch {
			case tgt == charges.Any:
			case strings.HasPrefix(tgt, charges.TargetScrip):
				sym = strings.TrimPrefix(tgt, charges.TargetScrip)
			case strings.HasPrefix(tgt, charges.TargetUnderlying):
				sym = strings.TrimPrefix(tgt, charges.TargetUnderlying)
			default:
				why = "a scrip group"
			}
			if why != "" {
				unmapped[why]++
				continue
			}
			exs := []string{exch}
			if exch == charges.Any {
				exs = firmEx[seg]
			} else if !contains(bowExchanges, exch) {
				continue
			}
			sp := 0
			if exch != charges.Any {
				sp += 2
			}
			if chn != charges.Any {
				sp++
			}
			for _, ex := range exs {
				inst := ""
				if cs != charges.Any {
					inst = bowInstrument(ex, fut, index)
				}
				lk := ex + "|" + string(bucket) + "|" + inst + "|" + sym
				if l := lines[lk]; l != nil && l.spec > sp {
					continue
				}
				segName, prod := "EQUITY", "ALL"
				switch bucket {
				case charges.CMDelivery:
					prod = "CNC"
				case charges.CMIntraday:
					prod = "INTRA"
				default:
					segName = map[bool]string{true: "FUTURES", false: "OPTIONS"}[fut]
				}
				market := bowMarketName[seg]
				if seg == "FO" {
					market = "DERIVATIVES"
				}
				sr, err := sched.Rule(bucket, ex, date)
				if err != nil {
					noRate[string(bucket)+" "+ex] = true
				}
				sellType, sellV2 := "1", "0"
				if bucket == charges.CMDelivery && dpSell.Sign() > 0 {
					sellType, sellV2 = "2", dpSell.String()
				}
				lines[lk] = &bowBrokLine{spec: sp, v: map[string]string{
					"PlanName": name, "Exchange": ex, "Market": market, "Segment": segName, "InstrumentSeries": inst, "ProductID": prod, "Symbol": bowText(sym),
					"BuyType": typ, "Value1Buy": v1, "Value2Buy": v2, "SellType": typ, "Value1Sell": v1, "Value2Sell": v2,
					"STTBuy": sr.STTBuyPct.String(), "STTSell": sr.STTSellPct.String(), "TranCharges": sr.TxnPct.String(), "GST": sched.GSTPct.String(), "SEBICharges": sched.SEBIFeePct.String(),
					"StampType": "1", "StampValue1": sr.StampBuyPct.String(), "StampValue2": "0",
					"BuyDPType": "1", "BuyDPValue1": "0", "BuyDPValue2": "0", "SellDPType": sellType, "SellDPValue1": "0", "SellDPValue2": sellV2,
					"Disclaimer1": disc[0], "Disclaimer2": disc[1], "Disclaimer3": disc[2], "DeleteFlag": "N"}}
			}
		}
		for _, k := range sortedKeys(lines) {
			b.Rows = append(b.Rows, xfile.Row{Values: lines[k].v})
		}
	}
	if len(unmapped) > 0 {
		var xs []string
		for _, k := range sortedKeys(unmapped) {
			xs = append(xs, fmt.Sprintf("%s (%d)", k, unmapped[k]))
		}
		b.note("Rate lines BOW's brokerage master cannot express were left out: %s", strings.Join(xs, ", "))
	}
	if otherChannel > 0 {
		b.note("%d rate lines for channels other than %s were left out", otherChannel, channel)
	}
	if len(noRate) > 0 {
		b.note("No charge schedule rate for %s: STT, transaction charges and stamp duty written as 0", strings.Join(sortedKeys(noRate), ", "))
	}
	b.Empty = "Nothing to send: no brokerage plan is in force on the date"
	return b, nil
}

func (o *Ops) bowBrokerageMapping(ctx context.Context, tenant, date string) (bowBuilt, error) {
	b := bowBuilt{Tracked: true}
	bb, err := o.brokerageBook(ctx, tenant, date)
	if err != nil {
		return b, err
	}
	sched, cards := 0, 0
	for _, u := range sortedKeys(bb.clients) {
		if bb.clients[u].Status != "Active" {
			continue
		}
		ep, p, ok := bb.resolve(u)
		if len(ep.Segments) > 0 {
			cards++
		}
		if !ok || bowText(p.Name) == "" {
			sched++
			continue
		}
		b.Items = append(b.Items, bowItem{Key: u, Rows: []xfile.Row{{Values: map[string]string{"BackofficeID": u, "PlanName": bowText(p.Name)}}}})
	}
	if sched > 0 {
		b.note("%d active clients are on the charge schedule alone (no plan) and are left out", sched)
	}
	if cards > 0 {
		b.note("%d clients have segment cards, which BOW cannot take (one plan per client): their main plan is mapped", cards)
	}
	if len(b.Items) == 0 && b.Empty == "" {
		b.Empty = "Nothing to send: no active client is on a brokerage plan"
	}
	return b, nil
}

// ---- positions ----

var bowPosMarkets = map[string]string{"BSE-FO": "12", "NSE-FO": "22", "BSE-CD": "14", "NSE-CD": "24", "BSE-COM": "13", "NSE-COM": "23", "MCX-COM": "53", "NCDEX-COM": "33"}

func (o *Ops) bowManualPositions(ctx context.Context, tenant string, cfg bowFilesCfg, date string) (bowBuilt, error) {
	var b bowBuilt
	set, _ := o.Settings(ctx, tenant)
	member := bowText(set.MemberCodes["BSE"])
	if member == "" {
		return b, invalid("BOW's BSE Member ID is NOT NULL: set the firm's BSE member code in Settings")
	}
	prod := firstOf(cfg.v["position_product"], "2")
	if prod != "1" && prod != "2" {
		return b, invalid("the manual position Product Type is 1 (margin / intraday) or 2 (CNC / delivery)")
	}
	prev := set.PrevBusinessDay(date)
	rv, err := o.Risk(ctx, tenant, prev)
	if err != nil {
		return b, err
	}
	_, look, _ := o.Market(ctx, tenant, prev)
	var noLot []string
	for _, u := range sortedKeys(rv.Positions) {
		ps := append([]PositionView(nil), rv.Positions[u]...)
		sort.SliceStable(ps, func(i, j int) bool { return ps[i].Exchange+ps[i].Instrument < ps[j].Exchange+ps[j].Instrument })
		for _, p := range ps {
			code := bowPosMarkets[p.Exchange+"-"+p.Segment]
			it, sym, exp, strike, opt, ok := finspotContract(p)
			if code == "" || p.Qty == 0 || !ok {
				continue
			}
			if p.Exchange == "BSE" && p.Segment == "FO" {
				it = map[string]string{"FUTIDX": "IF", "FUTSTK": "SF", "OPTIDX": "IO", "OPTSTK": "SO"}[it]
			}
			if opt == "XX" {
				opt, strike = "", ""
			}
			qty := abs(p.Qty)
			if p.Segment == "COM" && p.Exchange != "NCDEX" { // BOW takes lots there
				i, ok := look.Get(p.Exchange, p.Segment, p.Instrument)
				if !ok || i.Lot <= 0 || qty%i.Lot != 0 {
					noLot = append(noLot, u+" "+p.Name)
					continue
				}
				qty /= i.Lot
			}
			v := map[string]string{"MemberID": member, "ExchangeMarket": code, "ProductType": prod, "ClientID": u, "InstrumentName": it, "Symbol": bowText(sym),
				"ExpiryDate": exp, "StrikePrice": strike, "OptionType": opt, "Filler17": "1"}
			amt := p.AvgPrice.MulInt(abs(p.Qty)).Fixed(2)
			if p.Qty > 0 {
				v["BuyQty"], v["BuyValue"] = strconv.FormatInt(qty, 10), amt
			} else {
				v["SellQty"], v["SellValue"] = strconv.FormatInt(qty, 10), amt
			}
			b.Rows = append(b.Rows, xfile.Row{Values: v})
		}
	}
	b.leftOut("commodity positions whose lot size is not in the day's market data", noLot)
	b.Empty = "Nothing to send: no carried derivatives position at the previous business day's close"
	return b, nil
}
