package ops

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
)

// ---- ASBA bids through NSE's eIPO API ----
//
// An ASBA bid, with the money blocked in the client's own bank account
// rather than by a UPI mandate (bids above ₹5 lakh, non-individuals), goes
// to eIPO's transactions/add with upiFlag N and two codes NSE gives:
// bankCode, the bank's, and locationCode, the location's, six characters
// each and mandatory for a member's ASBA bid (Web API 1.20.7 and 2.0.0).
// The protocol has no list of them, so the firm enters each bank's codes
// once, from eIPO, keyed by the first four letters of the bank's IFSC, and
// one location code on the connection for banks without their own. A bid
// whose bank has no codes yet stays for the bid file, and one work item a
// bank asks for them; entering them closes it and the next send takes the
// bid. A member sends a blocking reference only for a 3-in-1 account bid
// (prefix T); Ledgerline sends none.
//
// The API replaces the upload, not the form: the client's signed ASBA form
// still goes to the bank's branch for syndicate ASBA, which blocks the
// money. Each day bids go, one work item per issue lists how many forms go
// to which bank.

const cEIPOBank = "eipobank" // key: IFSC prefix

// EIPOBankCode is one bank's eIPO codes.
type EIPOBankCode struct {
	Prefix       string `json:"prefix"` // the IFSC's first four letters
	Bank         string `json:"bank,omitempty"`
	BankCode     string `json:"bank_code"`
	LocationCode string `json:"location_code,omitempty"` // empty: the connection's
	Updated      string `json:"updated,omitempty"`
	By           string `json:"by,omitempty"`
}

var (
	reEIPOCode   = regexp.MustCompile(`^[A-Z0-9]{1,6}$`)
	reIFSCPrefix = regexp.MustCompile(`^[A-Z]{4}$`)
)

// ifscPrefix is an IFSC's bank part (its first four letters), or "".
func ifscPrefix(ifsc string) string {
	s := strings.ToUpper(strings.TrimSpace(ifsc))
	if len(s) < 4 || !reIFSCPrefix.MatchString(s[:4]) {
		return ""
	}
	return s[:4]
}

// EIPOBankCodes lists the banks' codes, by IFSC prefix.
func (o *Ops) EIPOBankCodes(ctx context.Context, tenant string) []EIPOBankCode {
	l, _ := docs.List[EIPOBankCode](ctx, o.Docs, tenant, cEIPOBank, "")
	out := []EIPOBankCode{}
	for _, b := range l {
		if b.BankCode != "" {
			out = append(out, b)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Prefix < out[j].Prefix })
	return out
}

func (o *Ops) eipoBankCodeMap(ctx context.Context, tenant string) map[string]EIPOBankCode {
	m := map[string]EIPOBankCode{}
	for _, b := range o.EIPOBankCodes(ctx, tenant) {
		m[b.Prefix] = b
	}
	return m
}

// eipoASBACodes is the bank and location code for a client's bank.
func eipoASBACodes(codes map[string]EIPOBankCode, conn eipoConn, ifsc string) (bank, loc string, ok bool) {
	b, found := codes[ifscPrefix(ifsc)]
	if !found || b.BankCode == "" {
		return "", "", false
	}
	loc = firstOf(b.LocationCode, conn.asbaLocation)
	return b.BankCode, loc, loc != ""
}

// eipoNeedBank raises the work item asking for a bank's codes.
func (o *Ops) eipoNeedBank(ctx context.Context, tenant string, bank BankAccount, conn eipoConn, codes map[string]EIPOBankCode) {
	p := ifscPrefix(bank.IFSC)
	if p == "" {
		return
	}
	name := firstOf(firstOf(codes[p].Bank, bank.Bank), p)
	what := "the bank code and location code NSE gives"
	if codes[p].BankCode != "" {
		what = "a location code as well as the bank code (the bank's own, or the firm's on the connection)"
	}
	o.raiseWork(ctx, tenant, "ipo", "eipo-bank-"+p,
		fmt.Sprintf("ASBA bids for %s wait for its eIPO codes", name),
		fmt.Sprintf("NSE's eIPO API takes an ASBA bid only with %s for the bank (IFSC starting %s). Find them on eIPO and enter them on IPO → Files & eIPO API → ASBA bank codes; until then these bids go on the bid file.", what, p),
		"", RoleChecker, "ipo", "")
}

// SetEIPOBankCode enters or changes one bank's codes; an empty bank code
// removes the entry.
func (o *Ops) SetEIPOBankCode(ctx context.Context, tenant, actor string, in EIPOBankCode) (EIPOBankCode, error) {
	if err := o.require(ctx, tenant, actor, "Entering ASBA bank codes", RoleAdmin, RoleChecker); err != nil {
		return EIPOBankCode{}, err
	}
	b, err := cleanEIPOBank(in)
	if err != nil {
		return b, err
	}
	b.Updated, b.By = o.now().Format(time.RFC3339), actor
	if err := docs.Upsert(ctx, o.Docs, tenant, cEIPOBank, b.Prefix, b, actor); err != nil {
		return b, err
	}
	conn, _ := o.eipoConnection(ctx, tenant)
	if b.BankCode != "" && firstOf(b.LocationCode, conn.asbaLocation) != "" {
		o.closeWork(ctx, tenant, actor, "ipo", "eipo-bank-"+b.Prefix, "codes entered")
	}
	o.log(ctx, tenant, actor, "ipo.eipo_bank_code", b.Prefix, map[string]any{"bank_code": b.BankCode, "location_code": b.LocationCode})
	return b, nil
}

func cleanEIPOBank(in EIPOBankCode) (EIPOBankCode, error) {
	b := EIPOBankCode{Prefix: ifscPrefix(in.Prefix), Bank: strings.TrimSpace(in.Bank), BankCode: strings.ToUpper(strings.TrimSpace(in.BankCode)), LocationCode: strings.ToUpper(strings.TrimSpace(in.LocationCode))}
	if b.Prefix == "" {
		return b, invalid("give the bank's IFSC or its first four letters")
	}
	if b.BankCode != "" && !reEIPOCode.MatchString(b.BankCode) {
		return b, invalid("the eIPO bank code is up to 6 letters and digits")
	}
	if b.LocationCode != "" && !reEIPOCode.MatchString(b.LocationCode) {
		return b, invalid("the eIPO location code is up to 6 letters and digits")
	}
	return b, nil
}

// LoadEIPOBankCodes reads a CSV of IFSC (or its first four letters), bank
// code, and optionally location code and bank name; a header row is
// skipped.
func (o *Ops) LoadEIPOBankCodes(ctx context.Context, tenant, actor string, data []byte) (int, error) {
	if err := o.require(ctx, tenant, actor, "Entering ASBA bank codes", RoleAdmin, RoleChecker); err != nil {
		return 0, err
	}
	r := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))))
	r.FieldsPerRecord, r.TrimLeadingSpace = -1, true
	var rows []EIPOBankCode
	for line := 1; ; line++ {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, invalid("line %d: %v", line, err)
		}
		if len(rec) < 2 || strings.TrimSpace(strings.Join(rec, "")) == "" {
			continue
		}
		if h := strings.ToLower(strings.TrimSpace(rec[0])); line == 1 && (h == "ifsc" || h == "prefix" || h == "ifsc prefix") {
			continue
		}
		get := func(i int) string {
			if i < len(rec) {
				return rec[i]
			}
			return ""
		}
		b, err := cleanEIPOBank(EIPOBankCode{Prefix: rec[0], BankCode: rec[1], LocationCode: get(2), Bank: get(3)})
		if err != nil {
			if line == 1 {
				continue // a header
			}
			return 0, invalid("line %d: %v", line, err)
		}
		if b.BankCode == "" {
			return 0, invalid("line %d: no bank code", line)
		}
		rows = append(rows, b)
	}
	if len(rows) == 0 {
		return 0, invalid("no bank codes found: give IFSC (or its first four letters), bank code, location code, bank name")
	}
	for _, b := range rows {
		if _, err := o.SetEIPOBankCode(ctx, tenant, actor, b); err != nil {
			return 0, err
		}
	}
	return len(rows), nil
}

// asbaFormsWork lists, for an issue, the ASBA forms of bids first sent to
// the exchange today, by bank: they go to the banks' branches for
// syndicate ASBA.
func (o *Ops) asbaFormsWork(ctx context.Context, tenant string, iss IPOIssue, now time.Time) {
	day := now.In(ist).Format("2006-01-02")
	bs, _ := docs.List[IPOBatch](ctx, o.Docs, tenant, cIPOBatch, "")
	ids := map[string]bool{}
	for _, b := range bs {
		if b.Issue == iss.ID && strings.HasPrefix(laterIST(b.Created), day) {
			for id, act := range b.Bids {
				if act == "New" {
					ids[id] = true
				}
			}
		}
	}
	bids, _ := o.issueBids(ctx, tenant, iss.ID)
	cm, _ := o.ClientMap(ctx, tenant)
	by, n := map[string]int{}, 0
	for _, x := range bids {
		if ids[x.ID] && x.Mode == ModeASBA {
			by[firstOf(cm[x.UCC].Bank.Bank, "bank not named")]++
			n++
		}
	}
	if n == 0 {
		return
	}
	names := make([]string, 0, len(by))
	for k := range by {
		names = append(names, k)
	}
	sort.Strings(names)
	var parts []string
	for _, k := range names {
		parts = append(parts, fmt.Sprintf("%s %d", k, by[k]))
	}
	o.raiseWork(ctx, tenant, "ipo", "asba-forms-"+iss.ID+"-"+day,
		fmt.Sprintf("%s: send %d ASBA form(s) to the banks", iss.Name, n),
		fmt.Sprintf("ASBA bids sent to the exchange today, by bank: %s. Send each client's signed ASBA form to the bank's branch for syndicate ASBA so it blocks the money before the issue closes; the bids show the bank once the blocking is reported.", strings.Join(parts, ", ")),
		"", RoleMaker, "ipo", day)
}
