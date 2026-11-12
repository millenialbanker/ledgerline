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
)

// ---- clients migrated with details missing ----
//
// A firm keeps its clients' records for eight years, so a migration brings
// across every client, including the many that have not traded for years
// (inactive, suspended or closed). The previous system often does not have
// all their details, or has them in a form Ledgerline cannot accept: a
// blank mobile or PAN, a bank account number masked as the KRA shows it
// ("xxxx1234"), no segments, a nominee without a relationship, an address
// without its PIN. The go-live imports are written for onboarding live
// clients, so such a record would be rejected, and with it the balances,
// holdings, history and documents that belong to it.
//
// For an inactive client (the exchanges' Inactive flag, which stops
// trading) a detail that would be rejected is instead a warning: the
// record loads as it was (a blank stays blank; an invalid value is kept
// where it can be stored), and the detail is listed in Client.Incomplete.
// The client cannot be reactivated until each is completed. Active clients
// are checked in full, as before.
//
// Labels in Client.Incomplete are of two kinds: the client record's
// (Validate's fields), recomputed whenever the client is edited, and the
// exchange UCC details' (address, PIN, state, contact persons), dropped
// when the UCC details are saved complete.

// incompleteNote ends the warning for a detail kept as the previous system
// had it.
const incompleteNote = " — kept as in the previous system; complete it before reactivating the client"

// inactiveAcctNote ends the warning for a closed (inactive) bank account
// kept as it was: it is never used, so nothing needs completing.
const inactiveAcctNote = " — an inactive account, kept as in the previous system"

// bankOverLimitWhy marks an account the previous system had as active that
// was loaded as inactive because the client had more than the firm's limit.
const bankOverLimitWhy = "More active accounts than the firm's limit when migrated"

// bankInvalidWhy marks an active client's other account that came across
// with an invalid number or IFSC.
const bankInvalidWhy = "Invalid account number or IFSC in the previous system"

// incompleteOrder is the order the labels are listed in.
var incompleteOrder = []string{"Mobile", "Email", "PAN", "Client type", "Bank account number", "IFSC", "Bank name", "Bank accounts", "City", "Segments",
	"Income proof", "Running account settlement", "Nominee details", "Address", "PIN", "State", "Contact persons", "UCC details", "Client details"}

// clientLabels are the labels of the client record (from Validate).
var clientLabels = map[string]bool{"Mobile": true, "Email": true, "PAN": true, "Client type": true, "Bank account number": true, "IFSC": true, "Bank name": true,
	"Bank accounts": true, "City": true, "Segments": true, "Income proof": true, "Running account settlement": true, "Nominee details": true, "Client details": true}

// uccLabels are the labels of the exchange UCC details (uccdetails.go).
var uccLabels = map[string]bool{"Address": true, "PIN": true, "State": true, "Contact persons": true, "UCC details": true}

var bankLabels = []string{"Bank accounts", "Bank account number", "IFSC", "Bank name"}

// clientGapLabel names the detail a Validate field error is about; empty
// for the UCC and the name, which must always be right.
func clientGapLabel(field string) string {
	switch {
	case field == "ucc" || field == "name":
		return ""
	case field == "pan":
		return "PAN"
	case field == "type":
		return "Client type"
	case field == "mobile":
		return "Mobile"
	case field == "email":
		return "Email"
	case field == "city":
		return "City"
	case field == "bank.bank":
		return "Bank name"
	case field == "bank.ifsc":
		return "IFSC"
	case field == "bank.account":
		return "Bank account number"
	case field == "banks" || strings.HasPrefix(field, "banks."):
		return "Bank accounts"
	case field == "segments":
		return "Segments"
	case field == "income_proof":
		return "Income proof"
	case field == "ras":
		return "Running account settlement"
	case field == "nominee_name" || field == "nominees" || strings.HasPrefix(field, "nominees."):
		return "Nominee details"
	}
	return "Client details"
}

// uccGapLabel names the detail a UCC details problem (by import column) is
// about.
func uccGapLabel(field string) string {
	switch field {
	case "Address1", "City":
		return "Address"
	case "PIN", "PermPIN":
		return "PIN"
	case "State":
		return "State"
	case "Contacts", "Name", "Designation", "PAN", "Email":
		return "Contact persons"
	}
	return "UCC details"
}

// bankIndex is the account a "banks.N.…" field is about (-1 for others).
func bankIndex(field string) int {
	p := strings.Split(field, ".")
	if len(p) < 3 || p[0] != "banks" {
		return -1
	}
	i, err := strconv.Atoi(p[1])
	if err != nil {
		return -1
	}
	return i
}

// clientGaps lists the client record's details Validate does not accept,
// as labels. A problem with an inactive bank account is not one: the
// account is kept for the record and never used.
func clientGaps(c Client) []string {
	var ve *ValidationError
	if !errors.As(c.Validate(), &ve) {
		return nil
	}
	var out []string
	for _, f := range ve.Fields {
		if i := bankIndex(f.Field); i >= 0 && i < len(c.Banks) && !c.Banks[i].active() {
			continue
		}
		if l := clientGapLabel(f.Field); l != "" {
			out = append(out, l)
		}
	}
	return sortIncomplete(out)
}

// issues lists what the UCC-DETAILS import does not accept in normalised
// details, as (column, message): problems, an Indian PIN that is not 6
// digits, and the address, city, state and PIN, which are required.
func (d UCCDetails) issues() [][2]string {
	out := d.problems()
	for _, a := range []struct{ pin, country, field string }{{d.PIN, d.Country, "PIN"}, {d.PermPIN, d.PermCountry, "PermPIN"}} {
		if a.pin != "" && inIndia(a.country) && !rePIN.MatchString(a.pin) {
			out = append(out, [2]string{a.field, fmt.Sprintf("an Indian PIN is 6 digits, got %q", a.pin)})
		}
	}
	for _, x := range []struct{ v, field string }{{d.Address1, "Address1"}, {d.City, "City"}, {d.State, "State"}, {d.PIN, "PIN"}} {
		if x.v == "" {
			out = append(out, [2]string{x.field, "is required"})
		}
	}
	return out
}

// contactIssues lists what the UCC-CONTACTS import does not accept in a
// contact person, as (column, message).
func contactIssues(c UCCContact) [][2]string {
	var out [][2]string
	if strings.TrimSpace(c.Name) == "" {
		out = append(out, [2]string{"Name", "is required"})
	}
	if strings.TrimSpace(c.Designation) == "" {
		out = append(out, [2]string{"Designation", "is required (partner, karta, director, trustee …)"})
	}
	if c.PAN != "" && !rePAN.MatchString(c.PAN) {
		out = append(out, [2]string{"PAN", "PAN must be 5 letters, 4 digits, 1 letter"})
	}
	if c.Email != "" && !reEmail.MatchString(c.Email) {
		out = append(out, [2]string{"Email", "is not a valid email address"})
	}
	return out
}

// uccGaps lists the UCC details' problems as labels.
func uccGaps(d UCCDetails) []string {
	var out []string
	for _, p := range d.issues() {
		out = append(out, uccGapLabel(p[0]))
	}
	for _, c := range d.Contacts {
		if len(contactIssues(c)) > 0 {
			out = append(out, "Contact persons")
		}
	}
	return sortIncomplete(out)
}

// sortIncomplete de-duplicates labels and puts them in incompleteOrder
// (others after, as they came).
func sortIncomplete(ls []string) []string {
	rank := func(l string) int {
		for i, x := range incompleteOrder {
			if x == l {
				return i
			}
		}
		return len(incompleteOrder)
	}
	var out []string
	for _, l := range ls {
		if l = strings.TrimSpace(l); l != "" && !contains(out, l) {
			out = append(out, l)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return rank(out[i]) < rank(out[j]) })
	return out
}

// addIncomplete adds labels to the client's list.
func addIncomplete(c *Client, labels ...string) {
	c.Incomplete = sortIncomplete(append(append([]string{}, c.Incomplete...), labels...))
}

// refreshIncomplete brings the list of a client with details missing up
// to date: the client record's labels are what Validate now finds, and,
// when the client's exchange UCC details d are given, a UCC details label
// stays only while those details still lack it. A client with nothing
// listed is left alone.
func refreshIncomplete(c *Client, d *UCCDetails) {
	if len(c.Incomplete) == 0 {
		return
	}
	var ug []string
	if d != nil {
		ug = uccGaps(*d)
	}
	var out []string
	for _, l := range c.Incomplete {
		switch {
		case clientLabels[l]:
		case d != nil && uccLabels[l] && !contains(ug, l):
		default:
			out = append(out, l)
		}
	}
	c.Incomplete = sortIncomplete(append(out, clientGaps(*c)...))
}

// keepCompleted keeps the details on record that are right when a client
// master row brings them blank or in a form Ledgerline cannot accept (an
// inactive client's row, as the previous system still has it), so a
// re-import does not undo what was completed in Ledgerline.
func keepCompleted(c *Client, old Client) {
	now := clientGaps(*c)
	if len(now) == 0 {
		return
	}
	had := clientGaps(old)
	gap := func(ls []string, labels ...string) bool {
		for _, l := range labels {
			if contains(ls, l) {
				return true
			}
		}
		return false
	}
	keep := func(labels ...string) bool { return gap(now, labels...) && !gap(had, labels...) }
	if keep("Mobile") {
		c.Mobile = old.Mobile
	}
	if keep("Email") {
		c.Email = old.Email
	}
	if keep("City") {
		c.City = old.City
	}
	if keep("PAN") {
		c.PAN = old.PAN
	}
	if keep("Client type") {
		c.Type = old.Type
	}
	if keep("Bank account number", "IFSC", "Bank name") {
		c.Bank = old.Bank
	}
	if keep("Segments", "Income proof") {
		c.Segments, c.IncomeProof = old.Segments, old.IncomeProof
	}
	if keep("Running account settlement") {
		c.RAS = old.RAS
	}
}

// incompleteErr refuses to reactivate a client whose details are not
// complete.
func incompleteErr(c Client) error {
	if len(c.Incomplete) == 0 {
		return nil
	}
	return invalid("Complete the client's details first: %s. They were missing in the previous system.", strings.Join(c.Incomplete, ", "))
}

// validateEdit validates a client after an edit (contact details, a change
// request). A client migrated with details missing is completed a detail
// at a time: a problem with a detail still listed as missing, and left as
// it was by this edit, does not stop the edit, nor does one with a bank
// account already on record that the edit did not touch (an inactive one,
// kept for the record). Everything the edit changed must be right. The
// list of missing details is then brought up to date.
func validateEdit(before Client, c *Client) error {
	err := c.Validate()
	var ve *ValidationError
	if err != nil && !errors.As(err, &ve) {
		return err
	}
	if err != nil {
		var hard []FieldError
		for _, f := range ve.Fields {
			if !gapUntouched(before, *c, f.Field) {
				hard = append(hard, f)
			}
		}
		if len(hard) > 0 {
			return &ValidationError{hard}
		}
	}
	refreshIncomplete(c, nil)
	return nil
}

// gapUntouched says whether a problem on field of the edited client after
// was already there, unchanged, and allowed (see validateEdit).
func gapUntouched(before, after Client, field string) bool {
	listed := func(ls ...string) bool {
		for _, l := range ls {
			if contains(before.Incomplete, l) {
				return true
			}
		}
		return false
	}
	if i := bankIndex(field); i >= 0 {
		if i >= len(after.Banks) {
			return false
		}
		b := after.Banks[i]
		had := false
		for _, x := range registeredBanks(before) {
			had = had || x.details() == b.details()
		}
		return had && (!b.active() || listed(bankLabels...))
	}
	l := clientGapLabel(field)
	if l == "" || !listed(l) {
		return false
	}
	js := func(ns []Nominee) string {
		if len(ns) == 0 {
			return ""
		}
		b, _ := json.Marshal(ns)
		return string(b)
	}
	switch field {
	case "pan", "type":
		return before.PAN == after.PAN && before.Type == after.Type
	case "mobile":
		return before.Mobile == after.Mobile
	case "email":
		return before.Email == after.Email
	case "city":
		return before.City == after.City
	case "bank.bank":
		return before.Bank.Account == after.Bank.Account && before.Bank.Bank == after.Bank.Bank
	case "bank.ifsc":
		return before.Bank.Account == after.Bank.Account && before.Bank.IFSC == after.Bank.IFSC
	case "bank.account":
		return before.Bank.Account == after.Bank.Account
	case "banks":
		return true // no primary among the accounts on record; making one primary completes it
	case "segments", "income_proof":
		return strings.Join(before.Segments, ";") == strings.Join(after.Segments, ";") && before.IncomeProof == after.IncomeProof
	case "ras":
		return before.RAS == after.RAS
	}
	if l == "Nominee details" {
		return before.Nominee == after.Nominee && before.NomineeName == after.NomineeName && js(before.Nominees) == js(after.Nominees)
	}
	return false
}

// noteIncomplete records on the client the details found missing (add) and
// brings the rest of the list up to date; d is the client's exchange UCC
// details as now saved, or nil. Nothing is written when nothing changes.
func (o *Ops) noteIncomplete(ctx context.Context, tenant, actor, ucc string, d *UCCDetails, add ...string) error {
	_, err := docs.Update(ctx, o.Docs, tenant, cClient, ucc, actor, func(c *Client) error {
		was := strings.Join(c.Incomplete, "|")
		refreshIncomplete(c, d)
		addIncomplete(c, add...)
		if strings.Join(c.Incomplete, "|") == was {
			return errAlreadyDone
		}
		return nil
	})
	if errors.Is(err, errAlreadyDone) {
		return nil
	}
	return err
}

// payable says whether money can be sent to an account: a full account
// number and IFSC. A client migrated with a masked or missing account is
// not paid until the account is completed.
func payable(b BankAccount) bool {
	return reAcct.MatchString(b.Account) && reIFSC.MatchString(b.IFSC)
}
