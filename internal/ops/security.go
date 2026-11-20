package ops

import (
	"context"
	"fmt"
	"strings"

	"ledgerline/internal/vault"
)

// ---- data protection: encryption status, audit seal check, reveal ----

// SecurityStatus is what the Security panel shows and what a broker's
// auditor asks for: how client data is protected at rest and whether the
// audit trail is intact.
type SecurityStatus struct {
	Encrypted   bool              `json:"encrypted"`
	Cipher      string            `json:"cipher"`
	KeySource   string            `json:"key_source"`
	KeyVersion  int               `json:"key_version"`
	KeyVersions []int             `json:"key_versions"`
	FirmKey     bool              `json:"firm_key"` // the firm holds its own key (firmkey.go)
	Covers      []string          `json:"covers"`
	InClear     []string          `json:"in_clear"`
	Audit       *vault.AuditCheck `json:"audit,omitempty"`
	IndiaOnly   bool              `json:"india_only"`
	Residency   []ResidencyLine   `json:"residency"`
	MalwareScan MalwareScanStatus `json:"malware_scan"` // uploaded files scanned for malware (malware.go)
}

func (o *Ops) vaultAudit() *vault.Audit {
	a, _ := o.App.Audit.(*vault.Audit)
	return a
}

// Security reports encryption and, when verify is set, checks every seal in
// the audit trail. Compliance and administrators only.
func (o *Ops) Security(ctx context.Context, tenant, actor string, verify bool) (SecurityStatus, error) {
	if err := o.require(ctx, tenant, actor, "view data protection", RoleCompliance); err != nil {
		return SecurityStatus{}, err
	}
	s := SecurityStatus{KeyVersions: []int{}, Covers: []string{}, InClear: []string{}, IndiaOnly: o.Residency.IndiaOnly, Residency: o.ResidencyLines(), MalwareScan: o.MalwareScan(ctx, tenant)}
	va := o.vaultAudit()
	if va == nil {
		s.KeySource = "Not encrypted: the server was started without data keys."
		return s, nil
	}
	k := va.V.Keys()
	s.Encrypted, s.Cipher, s.KeySource, s.KeyVersion = true, "AES-256-GCM, a separate key per firm (HKDF-SHA256)", k.Describe(), k.Current()
	if kr, ok := k.(*vault.Keyring); ok {
		s.KeyVersions = kr.Versions()
	}
	if fk, err := va.V.FirmKey(ctx, tenant); err == nil && fk.Own {
		name := "its own key"
		if p, ok := connProvider(fk.Conn); ok {
			name = strings.TrimPrefix(p.Name, "Firm's own encryption key: ")
		}
		s.FirmKey, s.KeyVersion, s.KeyVersions = true, fk.Current, []int{}
		s.KeySource = fmt.Sprintf("The firm's own key (%s), root version %d. Records saved before the firm moved stay under Ledgerline's key until they are next saved, and are served only while the firm's key is available.", name, fk.Current)
		for i, l := range s.Residency {
			if l.What != "Encryption keys" {
				continue
			}
			s.Residency[i].Where, s.Residency[i].Status, s.Residency[i].Note = "The firm's own key: "+name, "check", "Keep the firm's key service in India."
			for _, r := range fk.Roots {
				if reg, _, err := vault.ParseKMSKeyARN(r.KeyID); err == nil && r.Version == fk.Current {
					s.Residency[i].Where, s.Residency[i].Status, s.Residency[i].Note = "The firm's own AWS KMS key, "+reg, "india", "Ledgerline accepts KMS keys in Mumbai (ap-south-1) or Hyderabad (ap-south-2) only."
				}
			}
		}
	}
	s.Covers = []string{
		"Client master: name, PAN, date of birth, mobile, email, address, bank account, nominee",
		"Online applications, change requests, KYC and PMLA records",
		"Users, sign-in sessions, settings and every workflow record",
		"Every stored file: KYC documents, exchange, depository and bank files, generated reports and contract notes",
		"The change history of every record",
	}
	s.InClear = []string{
		"Identifiers the database indexes: client code (UCC), record and file IDs, dates",
		"Ledger postings (account codes and amounts) and trade records, which the database checks for balance and duplicates",
		"Audit trail entries (sealed against change with Ledgerline's audit key, not encrypted, so auditors can read them)",
	}
	if verify {
		r, err := va.VerifyAudit(ctx, tenant)
		if err != nil {
			return s, err
		}
		s.Audit = &r
		o.log(ctx, tenant, actor, "security.audit.verified", "", map[string]any{"ok": r.OK, "sealed": r.Sealed, "problems": len(r.Problems), "last_hash": r.LastHash})
	}
	return s, nil
}

// RevealFields are the masked client fields a user may ask to see in full.
var RevealFields = map[string]string{"pan": "PAN", "account": "bank account number"}

// RevealClient returns one masked field in full, for a stated reason, and
// records who saw what and why. Ops checkers, compliance and administrators.
func (o *Ops) RevealClient(ctx context.Context, tenant, actor, ucc, field, reason string) (string, error) {
	if err := notSupport(actor, "Seeing full client identifiers"); err != nil {
		return "", err
	}
	if err := o.require(ctx, tenant, actor, "see full client identifiers", RoleChecker, RoleCompliance); err != nil {
		return "", err
	}
	label, ok := RevealFields[field]
	if !ok {
		return "", invalid("field must be pan or account")
	}
	reason = strings.TrimSpace(reason)
	if len(reason) < 5 {
		return "", invalid("give the reason for seeing the full " + label + " (it is recorded)")
	}
	c, err := o.Client(ctx, tenant, ucc)
	if err != nil {
		return "", err
	}
	v := c.PAN
	if field == "account" {
		v = c.Bank.Account
	}
	o.log(ctx, tenant, actor, "client.reveal", c.UCC, map[string]any{"field": label, "reason": trim(reason, 200)})
	return v, nil
}
