package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// A client may have more than one demat account.
//
// SEBI sets no limit on the demat accounts an investor holds, with the same
// DP or with different DPs (only a Basic Services Demat Account must be the
// investor's only one, SEBI circular CIR/MRD/DP/22/2012 and its successors
// in the Master Circular for Depositories). The exchanges' UCC records carry
// several depository accounts per client, with the first as the primary
// (NSE UCI: DP 1 to 3; BSE UCC: depository 1 to 3; MCX: 1 to 3), and a
// broker that is also a DP keeps every one of its own BO accounts.
//
// How Ledgerline keeps them, following the bank accounts (clientbanks.go):
//
//   - Client.Demat is the primary account, so everything that needs one
//     account for a client keeps working; Client.Demats lists all of them
//     once there is more than one, each Active, Inactive (no longer used
//     by the client for trading) or Closed, and whether it is with the
//     firm's own DP (OwnDP) or another DP.
//   - The DP's own accounts (BOAccount, dp.go) are kept by BO ID with the
//     client's UCC on them, so one client can have several. Files and
//     instructions name a BO ID (CDSL's files all do): it is matched to the
//     client through the accounts, never by assuming one per client.
//   - Holdings are kept per account: UCC|ISIN for the primary account (and
//     for holdings loaded with no account named), UCC|ISIN|BOID for any
//     other. Views of a client as a whole (risk, collateral, front-office
//     holding files, portfolio, P&L, statements' totals, corporate-action
//     entitlements) add the accounts up; an instruction moves securities in
//     one account: a delivery instruction or eDIS debits the account it
//     names (the primary by default), a margin pledge is made from one
//     account, and a securities pay-out is credited to the account the
//     client chose for pay-outs, else the primary.
//   - A BO ID belongs to one client. Adding an account, making another the
//     primary or marking one inactive is a change of the client's details
//     made the way a bank account change is: requested by the client or by
//     staff with the client's signed request, approved by a second person,
//     and sent to the exchanges as a UCC modification.

const (
	DematActive   = "Active"
	DematInactive = "Inactive"
	DematClosed   = "Closed"
)

// DematStatuses are the states of a client's demat account.
var DematStatuses = []string{DematActive, DematInactive, DematClosed}

// JointHolder is the second or third holder of a joint demat account.
type JointHolder struct {
	Name string `json:"name"`
	PAN  string `json:"pan,omitempty"`
}

// maxJointHolders: a demat account has at most three holders.
const maxJointHolders = 2

func (d Demat) active() bool { return d.Status == "" || d.Status == DematActive }

// details is the account without its place in the client's list.
func (d Demat) details() Demat { return Demat{Depository: d.Depository, ID: d.ID} }

func dematDepository(id string) string {
	if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(id)), "IN") {
		return "NSDL"
	}
	return "CDSL"
}

func normDematStatus(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "active", "a", "frozen":
		return DematActive
	case "inactive", "i", "not opened":
		return DematInactive
	case "closed", "c":
		return DematClosed
	}
	return strings.TrimSpace(s)
}

// dematStatusOfBO is how a client's list shows the DP's account status: a
// frozen account is still the client's (its freeze shows on the DP desk).
func dematStatusOfBO(s string) string {
	switch s {
	case "Closed":
		return DematClosed
	case "Not opened":
		return DematInactive
	}
	return DematActive
}

func normDemat(d *Demat) {
	d.ID = normDematID(d.ID)
	d.Depository = strings.ToUpper(strings.TrimSpace(d.Depository))
	if d.Depository == "" && d.ID != "" {
		d.Depository = dematDepository(d.ID)
	}
	d.Status = normDematStatus(d.Status)
}

// dematIDError checks an account ID for its depository ("" when it is fine).
func dematIDError(dep, id string) string {
	switch dep {
	case "CDSL":
		if !reCDSLBO.MatchString(id) {
			return "a CDSL BO ID is 16 digits"
		}
	case "NSDL":
		if !reNSDLBO.MatchString(id) {
			return "an NSDL account is IN, the 6-digit DP ID and the 8-digit client ID"
		}
	default:
		return "the depository must be CDSL or NSDL"
	}
	return ""
}

// demats are all the client's demat accounts, primary first: for a client
// with one, Demat as the only (primary, active) one.
func demats(c Client) []Demat {
	if len(c.Demats) == 0 {
		if strings.TrimSpace(c.Demat.ID) == "" {
			return nil
		}
		d := c.Demat
		d.Primary, d.Status = true, DematActive
		return []Demat{d}
	}
	out := append([]Demat(nil), c.Demats...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Primary && !out[j].Primary })
	return out
}

// activeDemats are the active accounts, primary first.
func activeDemats(c Client) []Demat {
	var out []Demat
	for _, d := range demats(c) {
		if d.active() {
			out = append(out, d)
		}
	}
	return out
}

// primaryDemat is the client's primary account (Demat, with its place in
// the list).
func primaryDemat(c Client) Demat {
	for _, d := range demats(c) {
		if d.Primary && d.active() {
			return d
		}
	}
	return c.Demat
}

func indexDemat(ds []Demat, id string) int {
	id = normDematID(id)
	for i, d := range ds {
		if id != "" && normDematID(d.ID) == id {
			return i
		}
	}
	return -1
}

// dematOf finds one of the client's accounts by its ID.
func dematOf(c Client, id string) (Demat, bool) {
	ds := demats(c)
	if i := indexDemat(ds, id); i >= 0 {
		return ds[i], true
	}
	return Demat{}, false
}

// holdingAcct is the account part of a holding's key for a BO ID: empty
// for the client's primary account (and for no account named), else the
// BO ID.
func holdingAcct(c Client, boid string) string {
	boid = normDematID(boid)
	if boid == "" || boid == normDematID(primaryDemat(c).ID) {
		return ""
	}
	return boid
}

// payoutDemat is the account pay-outs of securities are credited to: the
// one the client chose, while it is active, else the primary.
func payoutDemat(c Client) Demat {
	if c.PayoutDemat != "" {
		if d, ok := dematOf(c, c.PayoutDemat); ok && d.active() {
			return d
		}
	}
	return primaryDemat(c)
}

// syncDemats keeps Demat and Demats in step. Demat is the primary: when it
// names an account not in Demats (something that knows one account, such
// as a client master re-import, changed it) that account is added and
// becomes the primary; the old primary stays as another account when it
// is with the firm's DP and is made inactive when only the client master
// knew it. A primary that is no longer active gives way to an active
// account; a client whose accounts are all closed has no primary, and
// Demat keeps the first for the record.
func syncDemats(c *Client) {
	if len(c.Demats) == 0 {
		return
	}
	var ds []Demat
	for _, d := range c.Demats {
		normDemat(&d)
		if d.ID == "" || indexDemat(ds, d.ID) >= 0 {
			continue
		}
		ds = append(ds, d)
	}
	c.Demats = ds
	if len(ds) == 0 {
		c.Demats = nil
		return
	}
	p := -1
	if id := normDematID(c.Demat.ID); id != "" {
		p = indexDemat(c.Demats, id)
		if p < 0 {
			for j := range c.Demats {
				if c.Demats[j].Primary && c.Demats[j].active() && !c.Demats[j].OwnDP {
					c.Demats[j].Status, c.Demats[j].InactiveWhy = DematInactive, "Replaced as the primary account"
				}
			}
			nd := Demat{Depository: c.Demat.Depository, ID: id, Status: DematActive}
			normDemat(&nd)
			c.Demats = append(c.Demats, nd)
			p = len(c.Demats) - 1
		}
	}
	if p < 0 || !c.Demats[p].active() {
		p = -1
		for j, d := range c.Demats {
			if d.Primary && d.active() && p < 0 {
				p = j
			}
		}
		for j, d := range c.Demats {
			if d.active() && p < 0 {
				p = j
			}
		}
	}
	for j := range c.Demats {
		c.Demats[j].Primary = j == p
	}
	sort.SliceStable(c.Demats, func(x, y int) bool { return c.Demats[x].Primary && !c.Demats[y].Primary })
	if p >= 0 || c.Demat.ID == "" {
		c.Demat = c.Demats[0].details()
	}
}

// checkDemats validates the list of accounts.
func checkDemats(ds []Demat) []FieldError {
	var fe []FieldError
	seen := map[string]bool{}
	primaries := 0
	for i, d := range ds {
		f := fmt.Sprintf("demats.%d", i)
		if m := dematIDError(d.Depository, d.ID); m != "" {
			fe = append(fe, FieldError{f + ".id", fmt.Sprintf("Demat account %d (%s): %s", i+1, d.ID, m)})
		}
		if !contains(DematStatuses, d.Status) {
			fe = append(fe, FieldError{f + ".status", fmt.Sprintf("Demat account %d: status must be Active, Inactive or Closed", i+1)})
		}
		if d.Primary {
			primaries++
			if !d.active() {
				fe = append(fe, FieldError{f + ".primary", "The primary demat account must be active"})
			}
		}
		if k := normDematID(d.ID); seen[k] {
			fe = append(fe, FieldError{f + ".id", fmt.Sprintf("Demat account %s is registered twice", d.ID)})
		} else {
			seen[k] = true
		}
	}
	if primaries > 1 {
		fe = append(fe, FieldError{"demats", "Only one demat account can be the primary"})
	}
	return fe
}

// dematHolderNames is the account's holders as a statement prints them:
// "First, Second & Third".
func dematHolderNames(first string, jh []JointHolder) string {
	names := []string{first}
	for _, j := range jh {
		if n := strings.TrimSpace(j.Name); n != "" {
			names = append(names, n)
		}
	}
	if len(names) == 1 {
		return first
	}
	return strings.Join(names[:len(names)-1], ", ") + " & " + names[len(names)-1]
}

// uccDematSlots is how many depository accounts each exchange's UCC record
// holds: NSE UCI 5 (ccdDeposName..ccdDeposName5), BSE UCC 5
// (DEPOSITORYNAME1..5), MCX UCC 3 (Depository1..3).
var uccDematSlots = map[string]int{"NSE": 5, "BSE": 5, "MCX": 3}

// uccDemats are the accounts an exchange's UCC record carries: the active
// ones, primary first, up to the record's slots; more is a warning (the
// rest are not reported to that exchange).
func uccDemats(c Client, exch string, warn func(string, ...any)) []Demat {
	ds := activeDemats(c)
	if slots := uccDematSlots[exch]; len(ds) > slots {
		var left []string
		for _, d := range ds[slots:] {
			left = append(left, d.ID)
		}
		warn("The client has %d active demat accounts; the %s UCC record takes %d, so %s %s not reported to %s", len(ds), exch, slots, strings.Join(left, ", "), map[bool]string{true: "is", false: "are"}[len(left) == 1], exch)
		ds = ds[:slots]
	}
	return ds
}

// ---- the firm's own DP ----

// firmDP is the firm's own DP IDs: CDSL's (the DP ID part of a BO ID,
// without leading zeros) from the CDSL connection or Settings → member
// codes (CDSL), and NSDL's (IN and 6 digits) from the member codes (NSDL).
type firmDP struct{ cdsl, nsdl string }

func (o *Ops) firmDP(ctx context.Context, tenant string) firmDP {
	var f firmDP
	if x, ok := o.cdxConn(ctx, tenant); ok {
		f.cdsl = strings.TrimLeft(strings.TrimSpace(x.DPID), "0")
	}
	set, _ := o.Settings(ctx, tenant)
	if v := strings.TrimSpace(set.MemberCodes["CDSL"]); f.cdsl == "" && v != "" && strings.Trim(v, "0123456789") == "" {
		f.cdsl = strings.TrimLeft(cdxDPID(v), "0")
	}
	if v := strings.ToUpper(strings.TrimSpace(set.MemberCodes["NSDL"])); len(v) == 8 && strings.HasPrefix(v, "IN") {
		f.nsdl = v
	}
	return f
}

// owns says whether an account ID is at the firm's DP by its DP ID.
func (f firmDP) owns(id string) bool {
	id = normDematID(id)
	switch {
	case reCDSLBO.MatchString(id):
		return f.cdsl != "" && cdxBODP(id) == f.cdsl
	case strings.HasPrefix(id, "IN") && len(id) >= 8:
		return f.nsdl != "" && id[:8] == f.nsdl
	}
	return false
}

// withDemats fills in each of the client's accounts' OwnDP: with the firm's
// DP when Ledgerline keeps it as a BO account of this client or its DP ID
// is the firm's. For display; the list (Demats) is filled in for a client
// with one account too.
func (o *Ops) withDemats(ctx context.Context, tenant string, c Client) Client {
	ds := demats(c)
	if len(ds) == 0 {
		return c
	}
	f := o.firmDP(ctx, tenant)
	for i := range ds {
		b, ok := o.boAccount(ctx, tenant, ds[i].ID)
		ds[i].OwnDP = ds[i].OwnDP || (ok && b.UCC == c.UCC) || f.owns(ds[i].ID)
	}
	c.Demats = ds
	return c
}

// ---- the DP's BO accounts, by BO ID ----

// boKey is where a BO account is stored: its BO ID, or for an account not
// yet given one, the client's UCC behind a marker.
func boKey(b BOAccount) string {
	if id := normDematID(b.BOID); id != "" {
		return id
	}
	return "NOBO|" + b.UCC
}

// rekeyBO moves BO accounts stored the old way, by UCC, to their BO ID
// (once per firm and server run, and whenever a list finds one): the record
// is copied to its new key, unless an account is already there, and the old
// one is left as a marker pointing to it. Running it again changes nothing.
func (o *Ops) rekeyBO(ctx context.Context, tenant string) error {
	ds, err := o.Docs.List(ctx, tenant, cBO, "")
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, d := range ds {
		have[d.ID] = true
	}
	moved := 0
	for _, d := range ds {
		var b BOAccount
		if err := json.Unmarshal(d.Body, &b); err != nil || b.MovedTo != "" {
			continue
		}
		k := boKey(b)
		if d.ID == k {
			continue
		}
		if b.UCC == "" {
			b.UCC = d.ID
			if k = boKey(b); d.ID == k {
				continue
			}
		}
		if !have[k] {
			if err := docs.Upsert(ctx, o.Docs, tenant, cBO, k, b, SystemActor); err != nil {
				return err
			}
			have[k] = true
		}
		if err := docs.Upsert(ctx, o.Docs, tenant, cBO, d.ID, BOAccount{UCC: b.UCC, MovedTo: k}, SystemActor); err != nil {
			return err
		}
		moved++
	}
	if moved > 0 {
		o.log(ctx, tenant, SystemActor, "dp.bo_rekeyed", "", map[string]any{"accounts": moved})
	}
	return nil
}

// boReady re-keys a firm's BO accounts the first time they are used.
func (o *Ops) boReady(ctx context.Context, tenant string) {
	if _, done := o.boKeyed.Load(tenant); done {
		return
	}
	if err := o.rekeyBO(ctx, tenant); err == nil {
		o.boKeyed.Store(tenant, true)
	}
}

// boAccounts lists every BO account, ordered by UCC and then as stored.
func (o *Ops) boAccounts(ctx context.Context, tenant string) []BOAccount {
	o.boReady(ctx, tenant)
	bs, err := docs.List[BOAccount](ctx, o.Docs, tenant, cBO, "")
	if err != nil {
		return nil
	}
	var out []BOAccount
	stale := false
	for _, b := range bs {
		if b.MovedTo != "" {
			continue
		}
		if b.UCC == "" {
			stale = true // kept by UCC with no UCC on it: the re-key below fills it
			continue
		}
		out = append(out, b)
	}
	if stale {
		o.boKeyed.Delete(tenant)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].UCC < out[j].UCC })
	return out
}

// boAccount is one BO account by its BO ID.
func (o *Ops) boAccount(ctx context.Context, tenant, boid string) (BOAccount, bool) {
	boid = normDematID(boid)
	if boid == "" {
		return BOAccount{}, false
	}
	o.boReady(ctx, tenant)
	b, _, err := docs.Get[BOAccount](ctx, o.Docs, tenant, cBO, boid)
	if err != nil || b.MovedTo != "" {
		return BOAccount{}, false
	}
	return b, true
}

// boAccountsOf are a client's BO accounts with the DP, the primary first.
func (o *Ops) boAccountsOf(ctx context.Context, tenant, ucc string) []BOAccount {
	c, _ := o.Client(ctx, tenant, ucc)
	return o.boIndex(ctx, tenant).of(c)
}

// boIdx is the BO accounts by client and by BO ID.
type boIdx struct {
	byUCC  map[string][]BOAccount
	byBOID map[string]BOAccount
}

func (o *Ops) boIndex(ctx context.Context, tenant string) boIdx {
	x := boIdx{byUCC: map[string][]BOAccount{}, byBOID: map[string]BOAccount{}}
	for _, b := range o.boAccounts(ctx, tenant) {
		x.byUCC[b.UCC] = append(x.byUCC[b.UCC], b)
		x.byBOID[normDematID(b.BOID)] = b
	}
	return x
}

// of are the client's accounts, the primary first, then as the client's
// list has them.
func (x boIdx) of(c Client) []BOAccount {
	bs := append([]BOAccount(nil), x.byUCC[c.UCC]...)
	ds := demats(c)
	rank := func(b BOAccount) int {
		if i := indexDemat(ds, b.BOID); i >= 0 {
			return i
		}
		return len(ds)
	}
	sort.SliceStable(bs, func(i, j int) bool { return rank(bs[i]) < rank(bs[j]) })
	return bs
}

// boClients maps every demat account Ledgerline knows (the DP's BO
// accounts and every client's accounts) to its client, by normalised ID.
func (o *Ops) boClients(ctx context.Context, tenant string) map[string]string {
	cs, _ := o.ClientMap(ctx, tenant)
	by := map[string]string{}
	for _, u := range sortedKeys(cs) {
		for _, d := range demats(cs[u]) {
			if id := normDematID(d.ID); id != "" {
				if _, taken := by[id]; !taken {
					by[id] = u
				}
			}
		}
	}
	// The DP's own record wins over a client master.
	for _, b := range o.boAccounts(ctx, tenant) {
		if id := normDematID(b.BOID); id != "" {
			by[id] = b.UCC
		}
	}
	return by
}

// boFor is the DP's record of one of the client's accounts (blank: the
// primary), or one made from the client's list when Ledgerline has no
// record of it (an account with another DP, or not yet loaded).
func (o *Ops) boFor(ctx context.Context, tenant string, c Client, boid string) BOAccount {
	d := primaryDemat(c)
	if boid = normDematID(boid); boid != "" {
		if x, ok := dematOf(c, boid); ok {
			d = x
		} else {
			d = Demat{Depository: dematDepository(boid), ID: boid}
		}
	}
	if b, ok := o.boAccount(ctx, tenant, d.ID); ok && b.UCC == c.UCC {
		return b
	}
	if d.ID == "" {
		// No account on the client: the DP's first account of the client.
		if bs := o.boAccountsOf(ctx, tenant, c.UCC); len(bs) > 0 {
			return bs[0]
		}
	}
	b := BOAccount{UCC: c.UCC, BOID: d.ID, Depository: firstOf(d.Depository, "CDSL"), Status: "Active", Opened: c.KYCDate}
	if c.Status != "Active" {
		b.Status = "Not opened"
	}
	if d.Status == DematClosed {
		b.Status = "Closed"
	}
	return b
}

// putBO stores a BO account under its BO ID (a record kept for the client
// before its BO ID was known is marked as moved) and keeps the client's
// list of accounts in step: the account is added, and its status follows
// the DP's (closed when the DP closed it).
func (o *Ops) putBO(ctx context.Context, tenant, actor string, b BOAccount) error {
	o.boReady(ctx, tenant)
	b.BOID = normDematID(b.BOID)
	b.MovedTo = ""
	k := boKey(b)
	if err := docs.Upsert(ctx, o.Docs, tenant, cBO, k, b, actor); err != nil {
		return err
	}
	if b.BOID != "" {
		if old, _, err := docs.Get[BOAccount](ctx, o.Docs, tenant, cBO, "NOBO|"+b.UCC); err == nil && old.MovedTo == "" {
			_ = docs.Upsert(ctx, o.Docs, tenant, cBO, "NOBO|"+b.UCC, BOAccount{UCC: b.UCC, MovedTo: k}, actor)
		}
		return o.noteClientDemat(ctx, tenant, actor, b)
	}
	return nil
}

// noteClientDemat adds a BO account to its client's accounts, or brings its
// status there in step with the DP's.
func (o *Ops) noteClientDemat(ctx context.Context, tenant, actor string, b BOAccount) error {
	c, err := o.Client(ctx, tenant, b.UCC)
	if err != nil {
		return nil // the client may come later (DP-ACCOUNTS before the client master)
	}
	x := c
	x.Demats = append([]Demat(nil), c.Demats...)
	addBODemat(&x, b)
	if x.Demat == c.Demat && sameJSON(x.Demats, c.Demats) {
		return nil
	}
	_, err = o.updateDemats(ctx, tenant, actor, c.UCC, func(y *Client) error {
		addBODemat(y, b)
		return nil
	})
	return err
}

// updateDemats changes a client's accounts and, when the primary changes,
// moves the holdings kept for the primary (holdingIDAt) with it.
func (o *Ops) updateDemats(ctx context.Context, tenant, actor, ucc string, fn func(*Client) error) (Client, error) {
	var before string
	c, err := docs.Update(ctx, o.Docs, tenant, cClient, strings.ToUpper(ucc), actor, func(x *Client) error {
		before = primaryDemat(*x).ID
		return fn(x)
	})
	if err != nil {
		return c, err
	}
	if after := primaryDemat(c).ID; normDematID(after) != normDematID(before) {
		if err := o.moveHoldingsToPrimary(ctx, tenant, actor, c.UCC, before, after); err != nil {
			return c, err
		}
	}
	return c, nil
}

// addBODemat puts one of the DP's accounts in the client's list.
func addBODemat(c *Client, b BOAccount) {
	ds := demats(*c)
	want := dematStatusOfBO(b.Status)
	if i := indexDemat(ds, b.BOID); i >= 0 {
		ds[i].OwnDP = true
		if want != DematActive || ds[i].Status == DematClosed {
			ds[i].Status = want
			if want == DematClosed && ds[i].InactiveOn == "" {
				ds[i].InactiveOn = b.ClosedOn
			}
		}
	} else {
		ds = append(ds, Demat{Depository: firstOf(b.Depository, dematDepository(b.BOID)), ID: normDematID(b.BOID), Status: want, AddedOn: b.Opened, OwnDP: true, InactiveOn: b.ClosedOn})
	}
	c.Demats = ds
	syncDemats(c)
	collapseDemats(c)
}

// collapseDemats keeps a client with a single active account as before:
// Demat alone, no list.
func collapseDemats(c *Client) {
	if len(c.Demats) == 1 && c.Demats[0].Primary && c.Demats[0].Status == DematActive && c.PayoutDemat == "" {
		c.Demat, c.Demats = c.Demats[0].details(), nil
	}
}

// ---- holdings by account ----

// holdingIDAt is where a holding in one of the client's accounts is kept:
// UCC|ISIN for the primary account (acct empty), UCC|ISIN|BOID for another.
func holdingIDAt(ucc, isin, acct string) string {
	if acct = normDematID(acct); acct == "" {
		return holdingID(ucc, isin)
	}
	return ucc + "|" + isin + "|" + acct
}

// holdingBO is the BO ID a holding is in, for display: the primary's for
// one kept without an account.
func holdingBO(c Client, h Holding) string {
	return firstOf(h.BOID, primaryDemat(c).ID)
}

// sumHoldings adds a client's accounts up: one holding per client and ISIN,
// with the quantities, pledges and a weighted average cost of them all
// (BOID empty).
func sumHoldings(hs []Holding) []Holding {
	by := map[string]*Holding{}
	cost := map[string]money.Dec{}
	var order []string
	for _, h := range hs {
		k := holdingID(h.UCC, h.ISIN)
		x := by[k]
		if x == nil {
			x = &Holding{UCC: h.UCC, ISIN: h.ISIN, Symbol: h.Symbol}
			by[k] = x
			order = append(order, k)
		}
		if x.Symbol == "" {
			x.Symbol = h.Symbol
		}
		x.Qty = x.Qty.Add(h.Qty)
		x.Pledged = x.Pledged.Add(h.Pledged)
		cost[k] = cost[k].Add(h.AvgCost.Mul(h.Qty))
	}
	out := make([]Holding, 0, len(order))
	for _, k := range order {
		x := by[k]
		if x.Qty.Sign() > 0 {
			x.AvgCost, _ = cost[k].Quo(x.Qty)
		}
		out = append(out, *x)
	}
	return out
}

// clientHoldings are each client's holdings added up across accounts.
func (o *Ops) clientHoldings(ctx context.Context, tenant, ucc string) ([]Holding, error) {
	hs, err := o.Holdings(ctx, tenant, ucc)
	if err != nil {
		return nil, err
	}
	return sumHoldings(hs), nil
}

// holdingsByClient are the holdings added up per client and ISIN, by
// holdingID(UCC, ISIN).
func holdingsByClient(hs []Holding) map[string]Holding {
	out := map[string]Holding{}
	for _, h := range sumHoldings(hs) {
		out[holdingID(h.UCC, h.ISIN)] = h
	}
	return out
}

// adjustHoldingAt changes a holding in one account (acct empty: the
// primary), by exact quantities.
func (o *Ops) adjustHoldingAt(ctx context.Context, tenant, actor, ucc, acct, isin, sym string, dQty, dPledged money.Dec) (Holding, error) {
	acct = normDematID(acct)
	id := holdingIDAt(ucc, isin, acct)
	if _, _, err := docs.Get[Holding](ctx, o.Docs, tenant, cHolding, id); errors.Is(err, docs.ErrNotFound) {
		if dQty.Sign() < 0 || dPledged.Sign() < 0 {
			return Holding{}, fmt.Errorf("%w: %s holds no %s%s", ErrState, ucc, sym, acctNote(acct))
		}
		h := Holding{UCC: ucc, ISIN: isin, Symbol: sym, BOID: acct}
		if _, err := docs.Put(ctx, o.Docs, tenant, cHolding, id, h, 0, actor); err != nil && !errors.Is(err, docs.ErrStale) {
			return Holding{}, err
		}
	}
	return docs.Update(ctx, o.Docs, tenant, cHolding, id, actor, func(h *Holding) error {
		h.Qty = h.Qty.Add(dQty)
		h.Pledged = h.Pledged.Add(dPledged)
		if h.Pledged.Sign() < 0 || h.Qty.Sign() < 0 || h.Pledged.Cmp(h.Qty) > 0 {
			return fmt.Errorf("%w: %s %s would hold %s with %s pledged%s", ErrState, ucc, sym, fmtQty(h.Qty), fmtQty(h.Pledged), acctNote(acct))
		}
		return nil
	})
}

func acctNote(acct string) string {
	if acct == "" {
		return ""
	}
	return " in demat account " + cdslMaskBO(acct)
}

// debitAcross takes qty free securities out of a client's accounts, the
// primary first, then the others in the client's order (an on-market
// pay-in, which the depository takes from whichever account the client
// delivers from). Trades are in whole units, so the fraction of a holding
// stays where it is.
func (o *Ops) debitAcross(ctx context.Context, tenant, actor string, c Client, isin, sym string, qty int64) error {
	hs, _ := o.Holdings(ctx, tenant, c.UCC)
	free := map[string]int64{}
	for _, h := range hs {
		if h.ISIN == isin {
			free[h.BOID] += wholeUnits(h.Free())
		}
	}
	order := []string{""}
	for _, d := range demats(c) {
		if a := holdingAcct(c, d.ID); a != "" {
			order = append(order, a)
		}
	}
	for _, h := range hs {
		if h.ISIN == isin && !contains(order, h.BOID) {
			order = append(order, h.BOID)
		}
	}
	left := qty
	for _, a := range order {
		take := min(left, max(free[a], 0))
		if take <= 0 {
			continue
		}
		if _, err := o.adjustHoldingAt(ctx, tenant, actor, c.UCC, a, isin, sym, money.DecInt(-take), money.Dec{}); err != nil {
			return err
		}
		left -= take
		if left == 0 {
			return nil
		}
	}
	if left > 0 {
		// What no account has free: as before, from the primary (which fails
		// with the reason when it cannot).
		if _, err := o.adjustHoldingAt(ctx, tenant, actor, c.UCC, "", isin, sym, money.DecInt(-left), money.Dec{}); err != nil {
			return err
		}
	}
	return nil
}

// unpledgeAcross releases qty whole units of margin pledge in a client's
// accounts, the primary first (to deliver pledged shares on a pay-in).
func (o *Ops) unpledgeAcross(ctx context.Context, tenant, actor string, c Client, isin, sym string, qty int64) error {
	hs, _ := o.Holdings(ctx, tenant, c.UCC)
	sort.SliceStable(hs, func(i, j int) bool { return hs[i].BOID == "" && hs[j].BOID != "" })
	left := qty
	for _, h := range hs {
		if h.ISIN != isin || left <= 0 {
			continue
		}
		take := min(left, wholeUnits(h.Pledged))
		if take <= 0 {
			continue
		}
		if _, err := o.adjustHoldingAt(ctx, tenant, actor, c.UCC, h.BOID, isin, sym, money.Dec{}, money.DecInt(-take)); err != nil {
			return err
		}
		left -= take
	}
	if left > 0 {
		_, err := o.adjustHoldingAt(ctx, tenant, actor, c.UCC, "", isin, sym, money.Dec{}, money.DecInt(-left))
		return err
	}
	return nil
}

// ---- fractional quantities ----

// maxQtyDecimals is the most decimal places a demat quantity is kept to
// (CDSL's files carry up to 3).
const maxQtyDecimals = 4

// wholeUnits is a quantity's whole units (rounded down), for trades,
// deliveries and layouts that take whole numbers; the fraction stays.
func wholeUnits(q money.Dec) int64 {
	s := q.String()
	ip, frac, _ := strings.Cut(s, ".")
	n, _ := strconv.ParseInt(ip, 10, 64)
	if frac != "" && q.Sign() < 0 {
		n--
	}
	return n
}

// truncQty is a quantity to 4 decimal places, rounded down.
func truncQty(q money.Dec) money.Dec {
	n := wholeUnits(q.MulInt(10000))
	x, _ := money.DecInt(n).Quo(money.DecInt(10000))
	return x
}

// isWhole says whether a quantity has no fraction.
func isWhole(q money.Dec) bool { return !strings.Contains(q.String(), ".") }

// fmtQty shows a quantity with its decimals only when it has some
// (125.432, 100).
func fmtQty(q money.Dec) string { return q.String() }

// parseQty reads a demat quantity: at least zero, up to 4 decimal places.
func parseQty(s string) (money.Dec, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return money.Dec{}, nil
	}
	q, err := money.ParseDec(s)
	if err != nil || q.Sign() < 0 {
		return money.Dec{}, fmt.Errorf("must be a quantity of at least zero, got %q", s)
	}
	if _, frac, ok := strings.Cut(q.String(), "."); ok && len(frac) > maxQtyDecimals {
		return money.Dec{}, fmt.Errorf("has more than %d decimal places: %q", maxQtyDecimals, s)
	}
	return q, nil
}

// moveHoldingsToPrimary re-keys a client's holdings when the primary
// account changes: the old primary's go to their own BO ID, the new
// primary's to UCC|ISIN.
func (o *Ops) moveHoldingsToPrimary(ctx context.Context, tenant, actor, ucc, oldPrimary, newPrimary string) error {
	oldPrimary, newPrimary = normDematID(oldPrimary), normDematID(newPrimary)
	if oldPrimary == newPrimary {
		return nil
	}
	hs, err := o.Holdings(ctx, tenant, ucc)
	if err != nil {
		return err
	}
	put := map[string]Holding{}
	var order []string
	add := func(k string, h Holding) {
		x, ok := put[k]
		if !ok {
			order = append(order, k)
			put[k] = h
			return
		}
		x.Qty = x.Qty.Add(h.Qty)
		x.Pledged = x.Pledged.Add(h.Pledged)
		x.Symbol = firstOf(x.Symbol, h.Symbol)
		put[k] = x
	}
	empty := map[string]Holding{}
	for _, h := range hs {
		var to string
		switch {
		case h.BOID == "" && oldPrimary != "":
			to = oldPrimary
		case h.BOID != "" && h.BOID == newPrimary:
			to = ""
		default:
			continue
		}
		from := holdingIDAt(ucc, h.ISIN, h.BOID)
		empty[from] = Holding{UCC: ucc, ISIN: h.ISIN, Symbol: h.Symbol, BOID: h.BOID}
		m := h
		m.BOID = to
		add(holdingIDAt(ucc, h.ISIN, to), m)
	}
	for _, k := range sortedKeys(empty) {
		if _, ok := put[k]; !ok {
			if err := docs.Upsert(ctx, o.Docs, tenant, cHolding, k, empty[k], actor); err != nil {
				return err
			}
		}
	}
	for _, k := range order {
		if err := docs.Upsert(ctx, o.Docs, tenant, cHolding, k, put[k], actor); err != nil {
			return err
		}
	}
	if len(order) > 0 {
		o.log(ctx, tenant, actor, "holding.primary_moved", ucc, map[string]any{"from": cdslMaskBO(oldPrimary), "to": cdslMaskBO(newPrimary), "holdings": len(order)})
	}
	return nil
}

// DematRow is one line of the client screen's list of demat accounts: each with the
// DP's record when it is with the firm's DP, and its holdings.
type DematRow struct {
	Demat
	BOStatus     string        `json:"bo_status,omitempty"`
	Freeze       string        `json:"freeze,omitempty"`
	BSDA         bool          `json:"bsda,omitempty"`
	DDPI         bool          `json:"ddpi,omitempty"`
	JointHolders []JointHolder `json:"joint_holders,omitempty"`
	Holders      string        `json:"holders"`
	ISINs        int           `json:"isins"`
	Value        money.Dec     `json:"value"`
	Payout       bool          `json:"payout,omitempty"`
}

func (o *Ops) dematRows(ctx context.Context, tenant string, c Client, hs []HoldingView) []DematRow {
	c = o.withDemats(ctx, tenant, c)
	out := []DematRow{}
	pay := normDematID(payoutDemat(c).ID)
	for _, d := range demats(c) {
		r := DematRow{Demat: d, Holders: c.Name, Payout: normDematID(d.ID) == pay && d.active()}
		if b, ok := o.boAccount(ctx, tenant, d.ID); ok && b.UCC == c.UCC {
			r.BOStatus, r.Freeze, r.BSDA, r.DDPI, r.JointHolders = b.Status, b.freezeFor(), b.BSDA, b.DDPI, maskJoint(b.JointHolders)
			r.Holders = dematHolderNames(c.Name, b.JointHolders)
		}
		acct := holdingAcct(c, d.ID)
		for _, h := range hs {
			if h.BOID == acct || (acct == "" && normDematID(h.BOID) == normDematID(d.ID)) {
				if h.Qty.Sign() > 0 {
					r.ISINs++
				}
				r.Value = r.Value.Add(h.Value)
			}
		}
		out = append(out, r)
	}
	return out
}

func maskJoint(jh []JointHolder) []JointHolder {
	out := make([]JointHolder, len(jh))
	for i, j := range jh {
		j.PAN = MaskPAN(j.PAN)
		out[i] = j
	}
	return out
}
