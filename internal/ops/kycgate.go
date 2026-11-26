package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/xfile"
)

// ---- the KYC trading gate ----
//
// The rules (read November 2026):
//   - SEBI KYC master circular SEBI/HO/MIRSD/SECFATF/P/CIR/2023/169, para
//     83 (a client transacts only once the KYC process is complete) and
//     para 99 as amended by SEBI/HO/MIRSD/SECFATF/P/CIR/2024/41 (no
//     transaction until the KRA has verified the KYC attributes: PAN,
//     name, address, mobile, e-mail); SEBI's FAQ of May 2024 on that
//     circular (FAQ 42 and 45): a client the KRA cannot verify is blocked,
//     except that clients existing on 31 March 2024 may exit their
//     positions or redeem after due diligence.
//   - SEBI AML/CFT master circular SEBI/HO/MIRSD/MIRSDSECFATF/P/CIR/2024/78,
//     para 17: no transaction or account relationship before CDD is done.
//   - NSE/ISC/75724 (circular 37/2026) items 3-4, NSE/ISC/75249 (33/2026)
//     para 4, NSE/ISC/69627 (28/2025) and the monthly cycles (latest
//     NSE/ISC/77375, 50/2026): trading only for KRA status "KYC
//     Registered" or "KYC Validated"; each month NSE posts on ENIT (Log >
//     Downloads) <TMcode>_KRA_NOT_AVAILABLE (PANs not uploaded to a KRA),
//     <TMcode>_Non-Validated_Clients_by_KRA (KYCs on hold to be validated)
//     and the NPTT file (PANs Not Permitted To Trade from the cycle's
//     date: no fresh trades and no square-off, reinstated T+1 once the
//     KRA is compliant).
//   - BSE notice 20251111-43 and its monthly successors (latest
//     20261109-28), requirements 1-2 and 7-8: the
//     Non_Validated_Clients_by_KRAs file and BSE's UCC lists of PANs on
//     hold, invalid or not available at the KRA, acted on each month.
//   - MCX/S&I/522/2026 (monthly series): KRA status Registered or
//     Validated for trading; the monthly "KYC On Hold" list (by e-mail)
//     blocks fresh trades and square-off alike, so open positions run to
//     expiry and are monitored. MCX/S&I/176/2026 para 2: UCCs whose KRA
//     attributes are not verified are marked inactive in MCX's UCC
//     database until verified.
//
// What Ledgerline does:
//   - The gate (Settings.KYCBlockOff off, the default) blocks on the
//     front ends (Finspot block file and API, BOW trading rights, every
//     limit file) an active client whose KRA status is not Registered or
//     Validated, a status never loaded included, and a client on any of
//     the exchanges' KYC lists. A client onboarded by 31 March 2024 may
//     still close positions (fresh orders blocked: Finspot NON-SQROFF);
//     anyone else, and anyone the exchange has made Not Permitted to Trade
//     from a date now passed, is blocked for square-off too (Finspot ALL,
//     BOW trading rights off).
//   - The lists are loaded on Compliance → KYC trading gate. None has a
//     published file layout: like the KRAs' demise list, each is read by
//     its header names (config/formats, "headers" layouts) and matched by
//     PAN, the client code, or (CDSL) the BO ID. A client on a list gets a
//     hold (Client.KYCHolds); the next list of the same kind that does not
//     have the client lifts it, as does the compliance officer once the
//     KRA has verified the KYC. Each load raises one work item to act on
//     the list before the exchange's date.
//   - The KRA upload clock (three working days from the KYC, or a change
//     of KYC details, to the KRA upload; KYC master circular para 84) is
//     checked by the kra_upload_3days self-inspection and by a daily work
//     item for uploads due or late.

const (
	cKYCList  = "kyclist" // key: KL-nnnnnn
	cKYCState = "kycgatestate"
	wKYCGate  = "kycgate"

	// KYCExitOnlyBy: clients onboarded on or before it may still close
	// positions while their KYC is not verified (SEBI's FAQ of May 2024 on
	// SEBI/HO/MIRSD/SECFATF/P/CIR/2024/41, FAQ 42 and 45).
	KYCExitOnlyBy = "2024-03-31"

	// kraUploadDays: working days from the KYC (or a change of its
	// details) to the KRA upload (SEBI KYC master circular, para 84).
	kraUploadDays = 3
)

// The KYC lists (xfile layout IDs).
const (
	KYCListNSENotAvailable = "nse-kra-not-available"
	KYCListNSENonValidated = "nse-kra-non-validated"
	KYCListNSENPTT         = "nse-kyc-nptt"
	KYCListBSENonValidated = "bse-kra-non-validated"
	KYCListBSEPANStatus    = "bse-kra-pan-status"
	KYCListMCXHold         = "mcx-kyc-hold"
	KYCListCDSLDeficient   = "cdsl-kra-deficient"
)

// KYCListSource is one list Ledgerline reads, for the screen.
type KYCListSource struct {
	ID       string `json:"id"`
	Exchange string `json:"exchange"` // NSE, BSE, MCX, CDSL
	Title    string `json:"title"`
	From     string `json:"from"`  // where it is downloaded
	When     string `json:"when"`  // how often
	Basis    string `json:"basis"` // the circulars
	Action   string `json:"action"`
	// DateLabel names the date the person may give with the list: the
	// exchange's Not Permitted to Trade date, or CDSL's freeze date.
	DateLabel string `json:"date_label,omitempty"`
	// NoSquareOff: from the list's date (NPTT) the client cannot even
	// square off; NowNoSquareOff: from the day the list is loaded.
	NoSquareOff    bool `json:"no_square_off,omitempty"`
	NowNoSquareOff bool `json:"now_no_square_off,omitempty"`
	// Demat: a depository's list, about demat accounts, not trading.
	Demat bool   `json:"demat,omitempty"`
	Last  string `json:"last,omitempty"` // date of the latest list loaded
}

var kycListSources = []KYCListSource{
	{ID: KYCListNSENotAvailable, Exchange: "NSE", Title: "NSE: client PANs not available at a KRA", From: "ENIT > Log > Downloads: <TMcode>_KRA_NOT_AVAILABLE", When: "Monthly cycle",
		Basis: "NSE/ISC/77375 (50/2026), para 2", Action: "Upload each client's KYC to a KRA before NSE's date; until the KRA verifies it the client is blocked.", DateLabel: "Not permitted to trade from (the cycle's date)", NoSquareOff: true},
	{ID: KYCListNSENonValidated, Exchange: "NSE", Title: "NSE: non-validated clients by KRA", From: "ENIT > Log > Downloads: <TMcode>_Non-Validated_Clients_by_KRA", When: "Monthly cycle",
		Basis: "NSE/ISC/69627 (28/2025) points 1-4; NSE/ISC/77375 (50/2026)", Action: "Get each KYC on hold validated at the KRA before NSE's date; from that date NSE stops the PAN, square-off included.", DateLabel: "Not permitted to trade from (the cycle's date)", NoSquareOff: true},
	{ID: KYCListNSENPTT, Exchange: "NSE", Title: "NSE: PANs not permitted to trade (NPTT)", From: "ENIT > Log > Downloads: the cycle's NPTT file", When: "Monthly cycle",
		Basis: "NSE/ISC/77375 (50/2026), final para", Action: "These PANs cannot trade or square off on NSE until the KRA marks them compliant (reinstated T+1). Get the KYC validated; watch their open positions.", DateLabel: "Not permitted to trade from", NoSquareOff: true, NowNoSquareOff: true},
	{ID: KYCListBSENonValidated, Exchange: "BSE", Title: "BSE: non-validated clients by KRAs", From: "BSE's member portal, the month's Non_Validated_Clients_by_KRAs file", When: "Monthly",
		Basis: "BSE notice 20251111-43 and monthly successors (latest 20261109-28), requirements 1-2", Action: "Get each KYC validated at the KRA, or close the UCC.", DateLabel: "Not permitted to trade from (the notice's date)", NoSquareOff: true},
	{ID: KYCListBSEPANStatus, Exchange: "BSE", Title: "BSE: PANs on hold, invalid or not available at the KRA", From: "BSE UCC: the lists of PANs On Hold / Invalid / Not Available at KRA", When: "Monthly",
		Basis: "BSE notice 20251111-43 and monthly successors (latest 20261109-28), requirements 7-8", Action: "Correct the PAN or the KYC at the KRA (upload it if not there), or close the UCC.", DateLabel: "Not permitted to trade from (the notice's date)", NoSquareOff: true},
	{ID: KYCListMCXHold, Exchange: "MCX", Title: "MCX: clients with KYC on hold", From: "MCX's monthly e-mail (KYC On Hold list)", When: "Monthly",
		Basis: "MCX/S&I/522/2026 paras 4-5", Action: "No fresh trades and no square-off on MCX: open positions run to expiry and are watched. Trading resumes T+1 after the KRA marks the KYC compliant.", NoSquareOff: true, NowNoSquareOff: true},
	{ID: KYCListCDSLDeficient, Exchange: "CDSL", Title: "CDSL: BOs with KRA-deficient KYC (DP-wise)", From: "CDSL's billing folder: the DP-wise list of BOs with KYC not validated by a KRA", When: "Each cycle",
		Basis: "CDSL/OPS/DP/POLCY/2026/325 and the monthly KRA-validation series; CDSL/OPS/DP/POLCY/2026/234 and 2026/770", Action: "Follow up with each holder (joint holders too) to get the KYC validated before CDSL's freeze date; CDSL freezes the account under reason 27 if not.", DateLabel: "CDSL freezes from", Demat: true},
}

func kycListSourceOf(id string) (KYCListSource, bool) {
	for _, s := range kycListSources {
		if s.ID == id {
			return s, true
		}
	}
	return KYCListSource{}, false
}

func init() {
	for _, s := range kycListSources {
		listLayouts[s.ID] = true
	}
}

// KYCHold is a client found on an exchange's or the depository's KYC list.
type KYCHold struct {
	List     string `json:"list"` // the list's layout ID
	Exchange string `json:"exchange"`
	ListID   string `json:"list_id"` // KL-nnnnnn that last had the client
	Since    string `json:"since"`   // date of the first list that had the client
	Last     string `json:"last"`    // date of the latest list that had it
	// NPTTFrom: from this date the exchange stops the PAN, square-off
	// included ("" for a list that does not, or no date given).
	NPTTFrom string `json:"nptt_from,omitempty"`
	Remark   string `json:"remark,omitempty"` // the list's own remark or status
	// For the depository's list: the account and holder, and CDSL's
	// freeze date.
	BOID     string `json:"bo_id,omitempty"`
	Holder   string `json:"holder,omitempty"` // First, Second, Third
	FreezeOn string `json:"freeze_on,omitempty"`
}

// demat: the hold is about a demat account, not trading.
func (h KYCHold) demat() bool { s, _ := kycListSourceOf(h.List); return s.Demat }

// ---- the gate ----

// kraVerified: the KRA has verified the KYC (KYC Registered or KYC
// Validated, as the KRA status file or an exchange writes it).
func kraVerified(status string) bool {
	st := strings.ToLower(strings.TrimSpace(status))
	st = strings.TrimSpace(strings.TrimPrefix(st, "kyc "))
	return st == "validated" || st == "registered"
}

// kycNotVerifiedWhy says how the KRA status falls short ("" when verified).
func kycNotVerifiedWhy(c Client) string {
	if kraVerified(c.KRAStatus) {
		return ""
	}
	if strings.TrimSpace(c.KRAStatus) == "" {
		return "KYC not verified by a KRA (no KRA status loaded)"
	}
	return "KYC " + strings.ToLower(strings.TrimSpace(c.KRAStatus)) + " at the KRA"
}

// kycExisting: the client was onboarded on or before 31 March 2024, so may
// exit positions while the KYC is not verified. A client without an
// account opening date is not taken as one.
func kycExisting(c Client) bool {
	d := strings.TrimSpace(c.KYCDate)
	return validDate(d) && d <= KYCExitOnlyBy
}

// kycBlock is how the KYC rules hold a client back on the front ends.
type kycBlock struct {
	Why string // "" = not held back
	// All: square-off is blocked too (else fresh orders only: exit-only).
	All bool
}

// kycGate is the KYC block of a client on a date ("" date: today's
// holds counted whatever their date).
func (s Settings) kycGate(c Client, date string) kycBlock {
	if s.KYCBlockOff || c.Status != "Active" || c.DPOnly {
		return kycBlock{}
	}
	var why []string
	nptt := false
	if w := kycNotVerifiedWhy(c); w != "" {
		why = append(why, w)
	}
	for _, h := range c.KYCHolds {
		if h.demat() {
			continue
		}
		src, _ := kycListSourceOf(h.List)
		why = append(why, "on "+firstOf(src.Title, h.List))
		if h.NPTTFrom != "" && (date == "" || h.NPTTFrom <= date) {
			nptt = true
		}
	}
	if len(why) == 0 {
		return kycBlock{}
	}
	b := kycBlock{Why: strings.Join(why, "; "), All: nptt || !kycExisting(c)}
	if !b.All {
		b.Why += " (may close positions: client since " + fmtDay(c.KYCDate) + ")"
	}
	return b
}

// frontEndBlockWhy is finspotBlockWhy with the KYC gate ("" = may take new
// positions).
func (s Settings) frontEndBlockWhy(c Client, date string) string {
	if w := finspotBlockWhy(c); w != "" {
		return w
	}
	return s.kycGate(c, date).Why
}

// frontEndBlock is frontEndBlockWhy with whether square-off is blocked
// too: only the KYC gate blocks square-off; the other blocks stop fresh
// orders.
func (s Settings) frontEndBlock(c Client, date string) kycBlock {
	if w := finspotBlockWhy(c); w != "" {
		k := s.kycGate(c, date)
		return kycBlock{Why: w, All: k.All && k.Why != ""}
	}
	return s.kycGate(c, date)
}

// ---- loading a list ----

// KYCList is one list as loaded.
type KYCList struct {
	ID         string   `json:"id"`
	List       string   `json:"list"`
	Exchange   string   `json:"exchange"`
	File       string   `json:"file"`
	FileID     string   `json:"file_id,omitempty"`
	Date       string   `json:"date"`
	From       string   `json:"from,omitempty"` // NPTT or freeze date
	Circular   string   `json:"circular,omitempty"`
	By         string   `json:"by"`
	At         string   `json:"at"`
	Rows       int      `json:"rows"`
	Matched    int      `json:"matched"`     // clients on it
	New        int      `json:"new"`         // not on the previous list
	Lifted     int      `json:"lifted"`      // on the previous list, not on this one
	NotClients int      `json:"not_clients"` // PANs of nobody of the firm's
	Issues     int      `json:"issues"`
	Notes      []string `json:"notes,omitempty"`
	UCCs       []string `json:"uccs,omitempty"`
}

// KYCListLoadReq is a list as loaded on the screen.
type KYCListLoadReq struct {
	List     string `json:"list"`
	Name     string `json:"name"`
	Date     string `json:"date"`     // the list's date (empty: today)
	From     string `json:"from"`     // NPTT / freeze date (optional)
	Circular string `json:"circular"` // the month's circular (optional)
}

// LoadKYCList reads an exchange's or CDSL's KYC list and holds each client
// on it.
func (o *Ops) LoadKYCList(ctx context.Context, tenant, actor string, req KYCListLoadReq, data []byte) (KYCList, error) {
	if err := o.require(ctx, tenant, actor, "Loading KYC lists", RoleCompliance, RoleChecker, RoleMaker); err != nil {
		return KYCList{}, err
	}
	if _, ok := kycListSourceOf(req.List); !ok {
		return KYCList{}, invalid("unknown KYC list %q", req.List)
	}
	if req.From != "" && !validDate(req.From) {
		return KYCList{}, invalid("the date is YYYY-MM-DD")
	}
	lf, err := o.openListFile(ctx, tenant, req.List, req.Name, req.Date, data)
	if err != nil {
		return KYCList{}, err
	}
	l, err := o.applyKYCList(ctx, tenant, actor, lf, req.From, req.Circular)
	if err != nil {
		return l, err
	}
	o.log(ctx, tenant, actor, "xfile.received", lf.name, map[string]any{"format": req.List, "rows": l.Rows, "problems": l.Issues, "file": lf.fileID, "loaded": "KYC trading gate"})
	o.noteDailyFile(ctx, tenant, actor, req.List, lf.name, "")
	return l, nil
}

// kycRowHit is one client a list's row matched.
type kycRowHit struct {
	ucc, holder, boid, remark string
}

// applyKYCList matches a list's rows with the clients (by PAN, client code
// or BO ID), holds each one found and lifts the holds of the clients the
// previous list of the kind had and this one does not.
func (o *Ops) applyKYCList(ctx context.Context, tenant, actor string, lf *listFile, from, circular string) (KYCList, error) {
	src, _ := kycListSourceOf(lf.spec.ID)
	n, err := o.next(ctx, tenant, "kyclist", actor)
	if err != nil {
		return KYCList{}, err
	}
	now := o.now()
	l := KYCList{ID: fmt.Sprintf("KL-%06d", n), List: src.ID, Exchange: src.Exchange, File: lf.name, FileID: lf.fileID, Date: lf.date, From: from,
		Circular: strings.TrimSpace(circular), By: actor, At: now.Format(time.RFC3339)}
	if src.NowNoSquareOff && l.From == "" {
		l.From = lf.date
	}
	cs, err := o.ClientMap(ctx, tenant)
	if err != nil {
		return l, err
	}
	byPAN := map[string][]string{}
	for _, u := range sortedKeys(cs) {
		if c := cs[u]; c.PAN != "" && c.Status != "Rejected" {
			byPAN[strings.ToUpper(c.PAN)] = append(byPAN[strings.ToUpper(c.PAN)], u)
		}
	}
	// For the depository's list: accounts by BO ID, and joint holders by PAN.
	type acct struct{ ucc, holder string }
	byBO := map[string]BOAccount{}
	jointPAN := map[string][]acct{}
	if src.Demat {
		for _, b := range o.boAccounts(ctx, tenant) {
			byBO[normDematID(b.BOID)] = b
			for i, j := range b.JointHolders {
				if p := strings.ToUpper(strings.TrimSpace(j.PAN)); p != "" {
					jointPAN[p] = append(jointPAN[p], acct{b.UCC + "|" + b.BOID, []string{"Second", "Third"}[min(i, 1)]})
				}
			}
		}
	}
	hits := map[string]kycRowHit{} // by UCC (and BO ID for the depository's list)
	notes := map[string]int{}
	err = lf.feed(func(r xfile.Row) {
		l.Rows++
		pan := strings.ToUpper(strings.TrimSpace(r.Get("pan")))
		ucc := strings.ToUpper(strings.TrimSpace(r.Get("ucc")))
		boid := normDematID(r.Get("boid"))
		remark := strings.TrimSpace(firstOf(r.Get("status"), r.Get("remark")))
		found := false
		add := func(u, holder, bo string) {
			found = true
			k := u + "|" + bo + "|" + holder
			if _, ok := hits[k]; !ok {
				hits[k] = kycRowHit{ucc: u, holder: holder, boid: bo, remark: remark}
			}
		}
		if src.Demat && boid != "" {
			if b, ok := byBO[boid]; ok {
				holder := "First"
				if c := cs[b.UCC]; pan != "" && !strings.EqualFold(c.PAN, pan) {
					for i, j := range b.JointHolders {
						if strings.EqualFold(j.PAN, pan) {
							holder = []string{"Second", "Third"}[min(i, 1)]
						}
					}
				}
				add(b.UCC, holder, b.BOID)
			}
		}
		if !found && rePAN.MatchString(pan) {
			for _, u := range byPAN[pan] {
				if src.Demat {
					c := cs[u]
					for _, d := range demats(c) {
						if b, ok := byBO[normDematID(d.ID)]; ok && b.UCC == u {
							add(u, "First", b.BOID) // the DP's own CDSL accounts only
						}
					}
					continue
				}
				add(u, "", "")
			}
			for _, a := range jointPAN[pan] {
				p := strings.SplitN(a.ucc, "|", 2)
				add(p[0], a.holder, p[1])
			}
		}
		if !found && ucc != "" {
			if c, ok := cs[ucc]; ok {
				if pan != "" && !strings.EqualFold(c.PAN, pan) {
					notes["a client code whose PAN in Ledgerline is another (check the client; nothing was held)"]++
					return
				}
				add(ucc, "", "")
			}
		}
		if !found {
			if pan == "" && ucc == "" && boid == "" {
				notes["no PAN, client code or BO ID"]++
				return
			}
			l.NotClients++
		}
	})
	if err != nil {
		return l, err
	}
	// Hold every client found; lift the kind's holds of clients not on it.
	on := map[string][]kycRowHit{}
	for _, k := range sortedKeys(hits) {
		h := hits[k]
		on[h.ucc] = append(on[h.ucc], h)
	}
	for _, u := range sortedKeys(cs) {
		c := cs[u]
		had := false
		for _, h := range c.KYCHolds {
			had = had || h.List == src.ID
		}
		rows := on[u]
		if len(rows) == 0 && !had {
			continue
		}
		var lifted bool
		_, err := docs.Update(ctx, o.Docs, tenant, cClient, u, actor, func(x *Client) error {
			lifted = false
			var keep []KYCHold
			prev := map[string]KYCHold{}
			for _, h := range x.KYCHolds {
				if h.List != src.ID {
					keep = append(keep, h)
					continue
				}
				if h.Last > lf.date {
					keep = append(keep, h) // a later list is already in: this older one changes nothing
					continue
				}
				prev[h.BOID+"|"+h.Holder] = h
			}
			for _, r := range rows {
				h := KYCHold{List: src.ID, Exchange: src.Exchange, ListID: l.ID, Since: lf.date, Last: lf.date, Remark: r.remark, BOID: r.boid, Holder: r.holder}
				if p, ok := prev[r.boid+"|"+r.holder]; ok {
					h.Since = p.Since
					delete(prev, r.boid+"|"+r.holder)
				}
				if src.Demat {
					h.FreezeOn = l.From
				} else if src.NoSquareOff {
					h.NPTTFrom = l.From
				}
				keep = append(keep, h)
			}
			lifted = len(prev) > 0
			x.KYCHolds = keep
			return nil
		})
		if err != nil {
			return l, err
		}
		if len(rows) > 0 {
			l.Matched++
			l.UCCs = append(l.UCCs, u)
			if !had {
				l.New++
				o.log(ctx, tenant, actor, "kycgate.held", u, map[string]any{"list": l.ID, "kind": src.ID})
			}
		} else if lifted {
			l.Lifted++
			o.log(ctx, tenant, actor, "kycgate.lifted", u, map[string]any{"list": l.ID, "kind": src.ID, "why": "not on the later list"})
		}
	}
	for _, k := range sortedKeys(notes) {
		l.Notes = append(l.Notes, fmt.Sprintf("%d row%s with %s", notes[k], plural(notes[k]), k))
	}
	l.Issues = lf.issues
	if _, err := docs.Put(ctx, o.Docs, tenant, cKYCList, l.ID, l, 0, actor); err != nil {
		return l, err
	}
	o.log(ctx, tenant, actor, "kycgate.list.loaded", l.ID, map[string]any{"file": l.File, "kind": src.ID, "rows": l.Rows, "matched": l.Matched, "new": l.New, "lifted": l.Lifted})
	o.kycListWork(ctx, tenant, l)
	return l, nil
}

// kycListWork raises the list's work item (one per list loaded) for the
// compliance officer, with the date to act by.
func (o *Ops) kycListWork(ctx context.Context, tenant string, l KYCList) {
	if l.Matched == 0 {
		return
	}
	src, _ := kycListSourceOf(l.List)
	by := ""
	if l.From != "" {
		by = " by " + fmtDay(l.From)
		if src.Demat {
			by = " before CDSL's freeze on " + fmtDay(l.From)
		}
	}
	shown := l.UCCs
	if len(shown) > 10 {
		shown = shown[:10]
	}
	detail := fmt.Sprintf("%d client%s on %s (%s, %s). %s Clients: %s%s. Work the list on Compliance → KYC trading gate.",
		l.Matched, plural(l.Matched), src.Title, l.File, fmtDay(l.Date), src.Action, strings.Join(shown, ", "), map[bool]string{true: " and more", false: ""}[len(l.UCCs) > len(shown)])
	o.raiseWork(ctx, tenant, wKYCGate, "list:"+l.ID, fmt.Sprintf("Act on %s%s: %d client%s", src.Title, by, l.Matched, plural(l.Matched)), detail, "", RoleCompliance, "kycgate", l.Date)
}

// ---- lifting a hold by hand ----

// LiftKYCHold lifts a client's hold from a list once the KRA has verified
// the KYC (the exchange reinstates T+1; its next list will not have the
// client). Compliance only, with what was checked.
func (o *Ops) LiftKYCHold(ctx context.Context, tenant, actor, ucc, list, note string) (Client, error) {
	if err := o.require(ctx, tenant, actor, "Lifting KYC holds", RoleCompliance); err != nil {
		return Client{}, err
	}
	note = strings.TrimSpace(note)
	if len(note) < 4 {
		return Client{}, invalid("say what was checked: the KRA status and the date it was validated")
	}
	c, err := o.Client(ctx, tenant, ucc)
	if err != nil {
		return c, err
	}
	if !kraVerified(c.KRAStatus) {
		return c, fmt.Errorf("%w: the KRA status of %s is %q: load the KRA status showing it Registered or Validated first", ErrState, c.UCC, firstOf(c.KRAStatus, "not loaded"))
	}
	found := false
	c, err = docs.Update(ctx, o.Docs, tenant, cClient, c.UCC, actor, func(x *Client) error {
		var keep []KYCHold
		found = false
		for _, h := range x.KYCHolds {
			if h.List == list {
				found = true
				continue
			}
			keep = append(keep, h)
		}
		if !found {
			return fmt.Errorf("%w: %s is not on that list", ErrState, x.UCC)
		}
		x.KYCHolds = keep
		return nil
	})
	if err != nil {
		return c, err
	}
	o.log(ctx, tenant, actor, "kycgate.lifted", c.UCC, map[string]any{"kind": list, "why": note})
	return c, nil
}

// ---- the KRA upload clock ----

// kraUploadItem is a client whose KYC (or a change of it) is due at the
// KRA.
type kraUploadItem struct {
	UCC, Name, Kind, From, Due string // Kind: New, Modify
	Uploaded                   string // the day it was uploaded ("" not yet)
	Late                       bool
}

// kraUploads lists, for clients whose KYC was done or changed from since,
// when the KRA upload was due (three working days) and when it was made:
// the day the KRA file holding the client was marked uploaded, or (for a
// new client without one) the day a KRA status was first known.
func (o *Ops) kraUploads(ctx context.Context, tenant string, set Settings, since, date string) []kraUploadItem {
	cs, _ := o.Clients(ctx, tenant)
	bs, _ := docs.List[KRABatch](ctx, o.Docs, tenant, cKRABatch, "")
	sort.Slice(bs, func(i, j int) bool { return bs[i].Uploaded < bs[j].Uploaded })
	var out []kraUploadItem
	for _, c := range cs {
		if c.Status == "Rejected" || c.Status == "Application" {
			continue
		}
		for _, k := range []struct{ kind, from string }{{"New", c.KYCDate}, {"Modify", c.KYCUpdatedOn}} {
			if !validDate(k.from) || k.from < since || k.from > date {
				continue
			}
			it := kraUploadItem{UCC: c.UCC, Name: c.Name, Kind: k.kind, From: k.from, Due: addWorkDays(set, k.from, kraUploadDays)}
			for _, b := range bs {
				if b.Kind != KRABatchKYC || b.Uploaded == "" || b.Uploaded[:min(10, len(b.Uploaded))] < k.from || !contains(b.UCCs, c.UCC) {
					continue
				}
				it.Uploaded = b.Uploaded[:min(10, len(b.Uploaded))]
				break
			}
			it.Late = it.Uploaded == "" && it.Due < date || it.Uploaded > it.Due
			if it.Uploaded == "" && k.kind == "New" && c.KRAStatus != "" && c.KRAStatus != "Not available" {
				// The KRA already holds it (uploaded outside Ledgerline):
				// only the date cannot be shown.
				it.Uploaded, it.Late = "at the KRA", false
			}
			out = append(out, it)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Due+out[i].UCC < out[j].Due+out[j].UCC })
	return out
}

// addWorkDays is the date n working days after date.
func addWorkDays(set Settings, date string, n int) string {
	d := date
	for i := 0; i < n; i++ {
		d = set.NextBusinessDay(d)
	}
	return d
}

// ---- the daily run ----

type kycGateState struct {
	Day string `json:"day"`
}

// kycGateTick runs once a business day (after 9 am IST): a work item for
// each KRA upload due today or late, the MNRL notices that have expired
// (kycgate_cdsl.go), and the list work items closed once nothing on the
// list is held any more.
func (o *Ops) kycGateTick(ctx context.Context, tenant string, now time.Time) {
	t := now.In(ist)
	day := t.Format("2006-01-02")
	if t.Hour() < 9 {
		return
	}
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cKYCState, "tick", kycGateState{})
	if st.Day == day {
		return
	}
	st.Day = day
	_ = docs.Upsert(ctx, o.Docs, tenant, cKYCState, "tick", st, SystemActor)
	o.kycGateDaily(ctx, tenant, day)
}

func (o *Ops) kycGateDaily(ctx context.Context, tenant, day string) {
	set, _ := o.Settings(ctx, tenant)
	for _, it := range o.kraUploads(ctx, tenant, set, addDays(day, -60), day) {
		key := "kra:" + it.UCC + ":" + it.Kind + ":" + it.From
		switch {
		case it.Uploaded != "":
			o.closeWork(ctx, tenant, SystemActor, wKYCGate, key, "uploaded "+it.Uploaded)
		case it.Due <= day:
			what := map[string]string{"New": "the new account's KYC", "Modify": "the changed KYC details"}[it.Kind]
			o.workDue(ctx, tenant, wKYCGate, key, "Upload "+what+" of "+it.UCC+" to the KRA", fmt.Sprintf("%s's KYC was %s on %s: the KRA upload is due within %d working days, by %s. Make the KRA upload file on Compliance → KYC & PMLA, upload it and mark it uploaded.",
				it.UCC, map[string]string{"New": "completed", "Modify": "changed"}[it.Kind], fmtDay(it.From), kraUploadDays, fmtDay(it.Due)), it.UCC, RoleCompliance, "kyc", it.From, it.Due+" 17:00")
		}
	}
	o.mnrlNoticeDaily(ctx, tenant, day)
	o.closedBalanceDaily(ctx, tenant, day) // selfinspect_b6stmts.go
	o.onboardDaily(ctx, tenant, day)
	ls, _ := docs.List[KYCList](ctx, o.Docs, tenant, cKYCList, "")
	cs, _ := o.ClientMap(ctx, tenant)
	for _, l := range ls {
		open := 0
		for _, u := range l.UCCs {
			for _, h := range cs[u].KYCHolds {
				if h.ListID == l.ID {
					open++
				}
			}
		}
		if open == 0 {
			o.closeWork(ctx, tenant, SystemActor, wKYCGate, "list:"+l.ID, "nothing on the list is held any more")
		}
	}
}

// ---- the screen ----

// KYCGateRow is a client the gate holds back, or one on a list.
type KYCGateRow struct {
	UCC       string    `json:"ucc"`
	Name      string    `json:"name"`
	KRAStatus string    `json:"kra_status"`
	KYCDate   string    `json:"kyc_date,omitempty"`
	Why       string    `json:"why"`
	Mode      string    `json:"mode"` // Exit only, No trading, Gate off
	Holds     []KYCHold `json:"holds,omitempty"`
	Positions int       `json:"positions"`     // open derivatives positions
	MCX       bool      `json:"mcx,omitempty"` // on MCX's hold list: positions run to expiry
	Segments  []string  `json:"segments,omitempty"`
}

// KRAUploadRow is a KRA upload due or made.
type KRAUploadRow struct {
	UCC      string `json:"ucc"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	From     string `json:"from"`
	Due      string `json:"due"`
	Uploaded string `json:"uploaded,omitempty"`
	Late     bool   `json:"late"`
}

// KYCGateView is Compliance → KYC trading gate.
type KYCGateView struct {
	Date      string          `json:"date"`
	On        bool            `json:"on"`
	Blocked   []KYCGateRow    `json:"blocked"`
	Counts    map[string]int  `json:"counts"`
	Sources   []KYCListSource `json:"sources"`
	Lists     []KYCList       `json:"lists"`
	Uploads   []KRAUploadRow  `json:"uploads"`
	Demat     DematKYCView    `json:"demat"`
	PROI      []PROIRow       `json:"proi"`
	PROIFrom  string          `json:"proi_from"`
	CanLoad   bool            `json:"can_load"`
	CanLift   bool            `json:"can_lift"`
	CanFreeze bool            `json:"can_freeze"`
	CanPROI   bool            `json:"can_proi"`
}

// KYCGate is the screen: the clients held back and why, the lists, the
// KRA upload clock, the demat side and the PROI onboarding checks.
func (o *Ops) KYCGate(ctx context.Context, tenant, actor, date string) (KYCGateView, error) {
	v := KYCGateView{Date: date, Blocked: []KYCGateRow{}, Counts: map[string]int{}, Lists: []KYCList{}, Uploads: []KRAUploadRow{}, PROI: []PROIRow{}, PROIFrom: PROIFrom}
	if err := o.require(ctx, tenant, actor, "Viewing the KYC trading gate", RoleCompliance, RoleChecker, RoleMaker, RoleRisk); err != nil {
		return v, err
	}
	if !validDate(date) {
		v.Date = o.businessDate(ctx, tenant)
		date = v.Date
	}
	set, _ := o.Settings(ctx, tenant)
	v.On = !set.KYCBlockOff
	v.CanLoad = o.require(ctx, tenant, actor, "", RoleCompliance, RoleChecker, RoleMaker) == nil
	v.CanLift = o.require(ctx, tenant, actor, "", RoleCompliance) == nil
	v.CanFreeze = o.require(ctx, tenant, actor, "", RoleCompliance, RoleChecker, RoleMaker) == nil
	v.CanPROI = o.require(ctx, tenant, actor, "", RoleCompliance, RoleChecker, RoleMaker) == nil
	cs, err := o.ClientMap(ctx, tenant)
	if err != nil {
		return v, err
	}
	pos := map[string]int{}
	if rv, err := o.Risk(ctx, tenant, date); err == nil {
		for _, r := range rv.Rows {
			pos[r.UCC] = r.Positions
		}
	}
	on := set
	on.KYCBlockOff = false
	for _, u := range sortedKeys(cs) {
		c := cs[u]
		if c.Status != "Active" || c.DPOnly {
			continue
		}
		k := on.kycGate(c, date)
		if k.Why == "" {
			continue
		}
		r := KYCGateRow{UCC: u, Name: c.Name, KRAStatus: c.KRAStatus, KYCDate: c.KYCDate, Why: k.Why, Positions: pos[u], Segments: c.Segments}
		for _, h := range c.KYCHolds {
			if !h.demat() {
				r.Holds = append(r.Holds, h)
				r.MCX = r.MCX || h.List == KYCListMCXHold
			}
		}
		switch {
		case set.KYCBlockOff:
			r.Mode = "Gate off"
		case k.All:
			r.Mode = "No trading"
		default:
			r.Mode = "Exit only"
		}
		v.Counts[r.Mode]++
		if c.KRAStatus == "" {
			v.Counts["No KRA status"]++
		}
		v.Blocked = append(v.Blocked, r)
	}
	for _, s := range kycListSources {
		v.Sources = append(v.Sources, s)
	}
	ls, _ := docs.List[KYCList](ctx, o.Docs, tenant, cKYCList, "")
	sort.Slice(ls, func(i, j int) bool { return ls[i].At > ls[j].At })
	for _, l := range ls {
		for i := range v.Sources {
			if v.Sources[i].ID == l.List && l.Date > v.Sources[i].Last {
				v.Sources[i].Last = l.Date
			}
		}
		if len(v.Lists) < 50 {
			l.UCCs = nil
			v.Lists = append(v.Lists, l)
		}
	}
	for _, it := range o.kraUploads(ctx, tenant, set, addDays(date, -30), date) {
		v.Uploads = append(v.Uploads, KRAUploadRow(it))
		if it.Late {
			v.Counts["KRA uploads late"]++
		} else if it.Uploaded == "" {
			v.Counts["KRA uploads due"]++
		}
	}
	v.Demat = o.dematKYC(ctx, tenant, cs, date)
	v.PROI = o.proiRows(ctx, tenant, cs)
	return v, nil
}

// ---- activation ----

// activationProblems is what stops a client being activated: the CDD is
// not complete (SEBI AML/CFT master circular para 17; KYC master circular
// para 83). The KYC attributes on record (the address apart), details the
// previous system lacked, a KYC the KRA put on hold or rejected, and (from
// 3 Dec 2026) a PROI client's onboarding checks. A KYC the KRA has not yet
// verified does not stop activation: the client is activated and stays
// blocked on the front ends by the KYC gate until the KRA verifies it.
func (o *Ops) activationProblems(ctx context.Context, tenant string, c Client) []string {
	var out []string
	var app *Application
	if a, ok := o.uccSources(ctx, tenant).apps[c.UCC]; ok {
		app = &a
	}
	// The address is kept on the UCC details, often completed after the
	// account is opened: the exchanges' UCC paths and the KRA upload refuse
	// a client without it, and until the KRA verifies the address the
	// gate keeps the client from trading.
	var miss []string
	for _, m := range siKYCMissing(c, o.UCCDetailsFor(ctx, tenant, c.UCC), app) {
		if m != "address" {
			miss = append(miss, m)
		}
	}
	if len(miss) > 0 {
		out = append(out, "KYC details missing: "+strings.Join(miss, ", "))
	}
	if len(c.Incomplete) > 0 {
		out = append(out, "details missing from the previous system: "+strings.ToLower(strings.Join(c.Incomplete, ", ")))
	}
	st := strings.ToLower(c.KRAStatus)
	if strings.Contains(st, "hold") || strings.Contains(st, "reject") {
		out = append(out, "the KYC is "+c.KRAStatus+" at the KRA"+map[bool]string{true: " (" + c.KRARemark + ")", false: ""}[c.KRARemark != ""])
	}
	today := o.now().In(ist).Format("2006-01-02")
	if proiApplies(c, today) {
		for _, p := range proiProblems(c, today) {
			out = append(out, "PROI: "+p)
		}
	}
	// The firm's own codes, a second code for the PAN, VIPV, guardian and
	// MCX board resolution (onboardx.go).
	out = append(out, o.onboardBlocks(ctx, tenant, c)...)
	return out
}

func (l KYCList) summary() string {
	s := fmt.Sprintf("%d rows: %d client%s on it (%d new), %d lifted, %d not the firm's clients", l.Rows, l.Matched, plural(l.Matched), l.New, l.Lifted, l.NotClients)
	if l.Issues > 0 {
		s += fmt.Sprintf(", %d left out", l.Issues)
	}
	if len(l.Notes) > 0 {
		s += "; " + strings.Join(l.Notes, "; ")
	}
	return s
}
