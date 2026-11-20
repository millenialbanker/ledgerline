package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// ---- a firm's exit: the operator's record and the purge ----
//
// When a firm closes its Ledgerline account (SEBI's cloud framework: data
// portability and a clear expunging clause), its data is deleted from the
// live store and a destruction certificate is issued. The exit's record is
// the operator's, not the firm's: it is kept outside the firm's data, so it
// survives the purge, lets the certificate be issued again, and is
// Ledgerline's own audit of which firms left and when. It holds the firm's
// name and code, the dates, who asked and who approved, the row counts
// deleted and the certificate; nothing else of the firm.
//
// The purge is a store-level operation and refuses to run unless the
// tenant's exit record is approved by a second person, its cooling-off
// period has ended and its final export was downloaded (PurgeAllowed). The
// PostgreSQL store checks the same in the database (see
// db/migrations/0004_tenant_exit.sql).

// Exit states.
const (
	ExitRequested = "Requested" // waiting for a second person's approval
	ExitApproved  = "Approved"  // cooling-off, then waiting for the final export to be downloaded
	ExitCancelled = "Cancelled"
	ExitPurged    = "Purged"
)

// ExitRecord is the operator's record of one firm's exit.
type ExitRecord struct {
	Tenant         string    `json:"tenant"`
	ID             string    `json:"id"`
	Firm           string    `json:"firm"`
	FirmCode       string    `json:"firm_code"` // SEBI registration and member codes
	State          string    `json:"state"`
	RequestedBy    string    `json:"requested_by"`
	RequestedAt    time.Time `json:"requested_at"`
	ApprovedBy     string    `json:"approved_by,omitempty"`
	ApprovedRole   string    `json:"approved_role,omitempty"`
	ApprovedAt     time.Time `json:"approved_at,omitzero"`
	CoolingOffDays int       `json:"cooling_off_days"`
	CoolingOffEnds time.Time `json:"cooling_off_ends,omitzero"`
	CancelledBy    string    `json:"cancelled_by,omitempty"`
	CancelledAt    time.Time `json:"cancelled_at,omitzero"`
	// The final export: its ID, the SHA-256 of its manifest, and when an
	// Admin first downloaded it.
	ExportID           string    `json:"export_id,omitempty"`
	ExportSHA256       string    `json:"export_sha256,omitempty"`
	ExportDownloadedAt time.Time `json:"export_downloaded_at,omitzero"`
	// After the purge.
	PurgedAt          time.Time      `json:"purged_at,omitzero"`
	Deleted           map[string]int `json:"deleted,omitempty"` // rows deleted per table
	BackupsGoneBy     string         `json:"backups_gone_by,omitempty"`
	CertificateSHA256 string         `json:"certificate_sha256,omitempty"`
	Certificate       []byte         `json:"certificate,omitempty"` // the signed PDF
	// CertificateTokenHash is the SHA-256 of the secret in the link the
	// firm was given to download the certificate after its data is gone.
	CertificateTokenHash string   `json:"certificate_token_hash,omitempty"`
	Notes                []string `json:"notes,omitempty"`
}

// Public is the record without the certificate's bytes and link secret,
// for screens and lists.
func (r ExitRecord) Public() ExitRecord {
	r.Certificate, r.CertificateTokenHash = nil, ""
	return r
}

// PurgeAllowed says whether the firm's data may be deleted now under this
// record, and why not.
func (r ExitRecord) PurgeAllowed(now time.Time) error {
	switch {
	case r.State != ExitApproved:
		return fmt.Errorf("%w: the exit is %s, not approved", ErrState, r.State)
	case r.RequestedBy == "" || r.ApprovedBy == "" || r.ApprovedBy == r.RequestedBy:
		return fmt.Errorf("%w: the exit needs a second person's approval", ErrState)
	case r.CoolingOffEnds.IsZero() || now.Before(r.CoolingOffEnds):
		return fmt.Errorf("%w: the cooling-off period has not ended", ErrState)
	case r.ExportID == "" || r.ExportSHA256 == "" || r.ExportDownloadedAt.IsZero():
		return fmt.Errorf("%w: the final export has not been downloaded", ErrState)
	}
	return nil
}

// ExitStore keeps the operator's exit records, outside every firm's data.
type ExitStore interface {
	// SaveExit inserts or replaces a record. A purged record stays purged.
	SaveExit(ctx context.Context, r ExitRecord) error
	GetExit(ctx context.Context, tenant, id string) (ExitRecord, error)
	// ListExits returns the records of one firm, or of every firm when
	// tenant is empty (the operator's list), oldest first.
	ListExits(ctx context.Context, tenant string) ([]ExitRecord, error)
}

// Purger deletes a firm whose exit was approved.
type Purger interface {
	// PurgeTenant deletes every row the tenant has in every store, all or
	// nothing, only when the tenant's exit record exitID allows it at now
	// (PurgeAllowed). finish is given the rows deleted per table before
	// anything is final and returns the record to keep (state Purged, with
	// the certificate); an error from finish undoes the deletion. Every
	// other tenant is untouched.
	PurgeTenant(ctx context.Context, tenant, exitID string, now time.Time, finish func(deleted map[string]int) (ExitRecord, error)) (ExitRecord, error)
}

// ErrExitNotFound is returned for an unknown exit record.
var ErrExitNotFound = errors.New("exit record not found")

// MemExitStore is an in-memory ExitStore.
type MemExitStore struct {
	mu sync.Mutex
	m  map[string]ExitRecord // tenant|id
}

func NewMemExitStore() *MemExitStore { return &MemExitStore{m: map[string]ExitRecord{}} }

func (s *MemExitStore) SaveExit(_ context.Context, r ExitRecord) error {
	if r.Tenant == "" || r.ID == "" {
		return errors.New("app: an exit record needs a tenant and an id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur, ok := s.m[r.Tenant+"|"+r.ID]; ok && cur.State == ExitPurged && r.State != ExitPurged {
		return fmt.Errorf("%w: exit %s is purged", ErrState, r.ID)
	}
	r.Certificate = append([]byte(nil), r.Certificate...)
	s.m[r.Tenant+"|"+r.ID] = r
	return nil
}

func (s *MemExitStore) GetExit(_ context.Context, tenant, id string) (ExitRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.m[tenant+"|"+id]
	if !ok {
		return ExitRecord{}, fmt.Errorf("%w: %s", ErrExitNotFound, id)
	}
	return r, nil
}

func (s *MemExitStore) ListExits(_ context.Context, tenant string) ([]ExitRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []ExitRecord{}
	for _, r := range s.m {
		if tenant == "" || r.Tenant == tenant {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].RequestedAt.Equal(out[j].RequestedAt) {
			return out[i].RequestedAt.Before(out[j].RequestedAt)
		}
		return out[i].Tenant+out[i].ID < out[j].Tenant+out[j].ID
	})
	return out, nil
}

// PurgeTenant implements Purger for the in-memory stores. The stores have
// no transaction, so the tenant's rows are copied first and put back if
// finish fails.
func (m *MemSnapshot) PurgeTenant(ctx context.Context, tenant, exitID string, now time.Time, finish func(map[string]int) (ExitRecord, error)) (ExitRecord, error) {
	if m.Exits == nil {
		return ExitRecord{}, errors.New("app: this storage keeps no exit records")
	}
	m.purgeMu.Lock()
	defer m.purgeMu.Unlock()
	r, err := m.Exits.GetExit(ctx, tenant, exitID)
	if err != nil {
		return ExitRecord{}, err
	}
	if r.Tenant != tenant {
		return ExitRecord{}, fmt.Errorf("%w: exit %s is not of firm %s", ErrState, exitID, tenant)
	}
	if err := r.PurgeAllowed(now); err != nil {
		return ExitRecord{}, err
	}
	raws, trades, breaks, ents, audit, ds := m.Raw.ExportTenant(tenant), m.Trades.ExportTenant(tenant), m.Breaks.ExportTenant(tenant),
		m.Ledger.ExportTenant(tenant), m.Audit.ExportTenant(tenant), m.Docs.ExportTenant(tenant)
	n := map[string]int{}
	n["raw_file"] = m.Raw.PurgeTenant(tenant)
	n["trade_event"] = m.Trades.PurgeTenant(tenant)
	n["recon_break"] = m.Breaks.PurgeTenant(tenant)
	n["journal_entry"], n["posting"] = m.Ledger.PurgeTenant(tenant)
	n["audit_log"] = m.Audit.PurgeTenant(tenant)
	n["doc"] = m.Docs.PurgeTenant(tenant)
	out, err := finish(n)
	if err == nil {
		if out.Tenant != tenant || out.ID != exitID || out.State != ExitPurged {
			err = fmt.Errorf("%w: the purge must end with exit %s purged", ErrState, exitID)
		} else {
			err = m.Exits.SaveExit(ctx, out)
		}
	}
	if err != nil {
		// Put everything back as it was.
		_ = m.Raw.ImportTenant(tenant, raws)
		_ = m.Trades.ImportTenant(tenant, trades)
		_ = m.Breaks.ImportTenant(tenant, breaks)
		_ = m.Ledger.ImportTenant(tenant, ents)
		_ = m.Audit.ImportTenant(tenant, audit)
		_ = m.Docs.ImportTenant(tenant, ds)
		return ExitRecord{}, err
	}
	return out, nil
}
