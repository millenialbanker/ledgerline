package ingest

import (
	"context"
	"errors"
	"fmt"
)

// ---- malware scanning of stored files (a CSCRF anti-malware control) ----
//
// Every file Ledgerline keeps goes through RawStore.Put. With a scanner
// connected (LEDGERLINE_CLAMD), ScanningRawStore checks a file before it is
// stored, by what kind of file it is (the Kind function, from its Source):
//
//   - ScanDocument: files from people and from outside (board attachments
//     and mail to a board or card, KYC and client documents, vendor bills,
//     evidence, chat). Always scanned. A file too large for the scanner is
//     refused, never stored unscanned.
//   - ScanBulk: data files from the exchanges, clearing corporations and
//     depositories, whether fetched from their servers or uploaded through
//     the console, and migration packages. These can be large: they are
//     scanned up to the size limit, and a larger one is stored unscanned
//     with an audit entry saying so, so the day's settlement never stops on
//     a scanner limit.
//   - ScanSkip: what Ledgerline itself writes (reports, contract notes,
//     files it sends out). Not scanned.
//
// An infected file is refused ("This file was refused: it contains …") and
// written to the audit trail. If the scanner cannot be reached the file is
// refused too (fail closed): once scanning is on, no file is stored
// unscanned because the scanner was down.

// ScanResult is a scanner's verdict.
type ScanResult struct {
	Infected  bool
	Signature string
}

// Scanner checks bytes for malware.
type Scanner interface {
	Scan(ctx context.Context, data []byte) (ScanResult, error)
	Ping(ctx context.Context) error
	String() string
}

// Kinds of file, for scanning.
const (
	ScanDocument = "document"
	ScanBulk     = "bulk"
	ScanSkip     = "skip"
)

// DefaultScanMax is clamd's own default StreamMaxLength (25 MB).
const DefaultScanMax = 25 << 20

// ErrScanUnavailable means the scanner could not check a file, so it was
// not stored.
var ErrScanUnavailable = errors.New("ingest: the malware scanner is unavailable")

// InfectedError refuses a file the scanner found malware in.
type InfectedError struct{ Signature string }

func (e *InfectedError) Error() string { return "This file was refused: it contains " + e.Signature }
func (e *InfectedError) Unwrap() error { return ErrRejected }

// scanRefused is a file refused for another reason (too large to scan).
type scanRefused struct{ msg string }

func (e *scanRefused) Error() string { return e.msg }
func (e *scanRefused) Unwrap() error { return ErrRejected }

// scanDown is the scanner not answering.
type scanDown struct{ msg string }

func (e *scanDown) Error() string { return e.msg }
func (e *scanDown) Unwrap() error { return ErrScanUnavailable }

type actorKey struct{}

// WithActor notes who is storing files, for the audit entries the scanner
// writes.
func WithActor(ctx context.Context, actor string) context.Context {
	return context.WithValue(ctx, actorKey{}, actor)
}

func actorFrom(ctx context.Context) string {
	if a, _ := ctx.Value(actorKey{}).(string); a != "" {
		return a
	}
	return "system:malware-scan"
}

// ScanningRawStore scans files before the store beneath keeps them.
type ScanningRawStore struct {
	RawStore
	Scanner Scanner
	Max     int64                      // largest file sent to the scanner (DefaultScanMax when 0)
	Kind    func(source string) string // ScanDocument when nil
	// Log writes the audit trail.
	Log func(ctx context.Context, tenant, actor, action, object string, detail map[string]any)
}

func (s *ScanningRawStore) log(ctx context.Context, f RawFile, action string, detail map[string]any) {
	if s.Log == nil {
		return
	}
	d := map[string]any{"source": f.Source, "name": f.Name, "size": len(f.Content), "sha256": f.SHA256, "scanner": s.Scanner.String()}
	for k, v := range detail {
		d[k] = v
	}
	s.Log(ctx, f.Tenant, actorFrom(ctx), action, f.Name, d)
}

// Put scans the file by its kind, then stores it, or refuses it.
func (s *ScanningRawStore) Put(ctx context.Context, f RawFile) (RawFile, error) {
	kind := ScanDocument
	if s.Kind != nil {
		kind = s.Kind(f.Source)
	}
	if kind == ScanSkip {
		return s.RawStore.Put(ctx, f)
	}
	max := s.Max
	if max <= 0 {
		max = DefaultScanMax
	}
	tooLarge := func() (RawFile, error) {
		if kind == ScanBulk {
			s.log(ctx, f, "file.not_scanned", map[string]any{"reason": fmt.Sprintf("larger than the scanner takes (%d MB)", max>>20)})
			return s.RawStore.Put(ctx, f)
		}
		s.log(ctx, f, "file.scan_refused", map[string]any{"reason": fmt.Sprintf("larger than the scanner takes (%d MB)", max>>20)})
		return RawFile{}, &scanRefused{fmt.Sprintf("This file was refused: at %.1f MB it is larger than the malware scanner takes (%d MB).", float64(len(f.Content))/(1<<20), max>>20)}
	}
	if int64(len(f.Content)) > max {
		return tooLarge()
	}
	r, err := s.Scanner.Scan(ctx, f.Content)
	switch {
	case errors.Is(err, ErrScanTooLarge):
		return tooLarge()
	case err != nil:
		s.log(ctx, f, "file.scan_failed", map[string]any{"error": err.Error()})
		return RawFile{}, &scanDown{"Files cannot be stored now: the malware scanner cannot be reached, so nothing was saved. Try again in a few minutes; if it goes on, ask your administrator to check the ClamAV server."}
	case r.Infected:
		s.log(ctx, f, "file.malware_found", map[string]any{"signature": r.Signature})
		return RawFile{}, &InfectedError{Signature: r.Signature}
	}
	return s.RawStore.Put(ctx, f)
}

// Ready says whether the scanner answers (nil when it does).
func (s *ScanningRawStore) Ready(ctx context.Context) error {
	if err := s.Scanner.Ping(ctx); err != nil {
		return &scanDown{"Files cannot be stored now: the malware scanner cannot be reached. Try again in a few minutes; if it goes on, ask your administrator to check the ClamAV server."}
	}
	return nil
}
