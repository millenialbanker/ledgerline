package ops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"ledgerline/internal/ingest"
	"ledgerline/internal/xfile"
)

// ---- lists read by their header names: the revocation lists, the
// KRAs' demise list and NSE Clearing's cash-equivalent list ----
//
// These lists have no published layout (config/formats lists the header
// texts each column may have; xfile/headers.go). They are loaded on their
// own screens, where the person says which list a file is, or dropped on
// Exchange files, which recognises them by name and header. Either way a
// file whose header row cannot be found is refused, and a row that does
// not fit (a PAN not in the PAN form) is counted and left out rather than
// stopping the whole list.

// listLayouts are the layouts loaded here (mnrl.go, deceased.go, cashequiv.go).
var listLayouts = map[string]bool{MNRLListNSE: true, MNRLListDoT: true, MNRLListNSDL: true, MNRLListReac: true, DemiseListNSE: true, CashEqListNCL: true}

func isListLayout(id string) bool { return listLayouts[id] }

// listFile is a list ready to read.
type listFile struct {
	spec   *xfile.Spec
	name   string
	fileID string
	date   string
	issues int // rows that did not fit, once fed
	// feed hands each row that fits to each.
	feed func(each func(xfile.Row)) error
}

// openListFile unpacks a list file, checks its header row, keeps the file
// and gets it ready to read.
func (o *Ops) openListFile(ctx context.Context, tenant, format, name, date string, data []byte) (*listFile, error) {
	if len(data) == 0 || len(data) > MaxXFileBytes {
		return nil, invalid("files must be between 1 byte and 100 MB")
	}
	if date == "" {
		date = o.now().Format("2006-01-02")
	}
	if !validDate(date) {
		return nil, invalid("give the list's date (YYYY-MM-DD)")
	}
	lib, err := XFiles()
	if err != nil {
		return nil, err
	}
	s, ok := lib.Get(format)
	if !ok || !isListLayout(format) {
		return nil, invalid("unknown list %q", format)
	}
	files, err := xfile.Unpack(firstOf(name, format), data)
	if err != nil {
		return nil, invalid("%v", err)
	}
	if len(files) != 1 {
		return nil, invalid("load one list at a time: the archive has %d files", len(files))
	}
	f := files[0]
	if !s.HeaderFound(f.Data) {
		return nil, invalid("%s: %s", f.Name, s.NoHeaderMessage())
	}
	sum := sha256.Sum256(f.Data)
	rf, err := o.App.Ingest.Raw.Put(ctx, ingest.RawFile{Tenant: tenant, Source: "XFILE-" + strings.ToUpper(s.ID), Name: f.Name, SHA256: hex.EncodeToString(sum[:]), Content: f.Data})
	if err != nil {
		return nil, err
	}
	lf := &listFile{spec: s, name: f.Name, fileID: rf.ID, date: date}
	lf.feed = func(each func(xfile.Row)) error {
		t := xfile.Table{Name: f.Name}
		seen := 0
		err := s.Scan(f.Data, &t, func(r xfile.Row) {
			// A row's own problems are listed just before it is handed on.
			if len(t.Issues) > seen {
				seen = len(t.Issues)
				return
			}
			each(r)
		})
		lf.issues = len(t.Issues)
		return err
	}
	return lf, nil
}

// rowsFeed hands on the rows Exchange files has read, leaving out those
// with problems.
func rowsFeed(t *xfile.Table) func(func(xfile.Row)) error {
	bad := map[int]bool{}
	for _, is := range t.Issues {
		bad[is.Line] = true
	}
	return func(each func(xfile.Row)) error {
		for _, r := range dataRows(t) {
			if !bad[r.Line] {
				each(r)
			}
		}
		return nil
	}
}

// loadListXFile loads a list dropped on Exchange files (xfiles.go): the
// date is today's, and the demise list's cut-off the usual one (it can be
// changed on Compliance → Deceased clients). ok is false for other files.
func (o *Ops) loadListXFile(ctx context.Context, tenant, actor string, s *xfile.Spec, f xfile.File, t *xfile.Table, r *XFileResult) bool {
	if !isListLayout(s.ID) {
		return false
	}
	if t.Header == nil && len(dataRows(t)) == 0 {
		r.Loaded = "Not loaded: " + s.NoHeaderMessage()
		return true
	}
	// The same people may load a list here as on its own screen.
	var err error
	switch s.ID {
	case DemiseListNSE:
		err = o.require(ctx, tenant, actor, "Loading the KRAs' demise list", RoleCompliance, RoleChecker, RoleMaker)
	case CashEqListNCL:
		err = o.require(ctx, tenant, actor, "Loading the cash-equivalent collateral list", RoleRisk, RoleMaker, RoleChecker)
	default:
		err = o.require(ctx, tenant, actor, "Loading revocation lists", RoleMaker, RoleChecker, RoleCompliance)
	}
	if err != nil {
		r.Loaded = "Not loaded: " + err.Error()
		return true
	}
	lf := &listFile{spec: s, name: f.Name, fileID: r.FileID, date: o.now().Format("2006-01-02"), feed: rowsFeed(t), issues: len(t.Issues)}
	if s.ID == CashEqListNCL {
		l, err := o.applyCashEqFile(ctx, tenant, actor, lf)
		if err != nil {
			r.Loaded = "Not loaded: " + strings.TrimPrefix(err.Error(), "invalid: ")
			return true
		}
		r.Loaded, r.Added, r.Skipped = "Loaded as the cash-equivalent collateral list (Finspot notional cash, risk)", len(l.ISINs), l.NonCash+l.Bad
		r.Note = l.summary()
		return true
	}
	if s.ID == DemiseListNSE {
		l, err := o.applyDemiseFile(ctx, tenant, actor, lf, "", "")
		if err != nil {
			r.Loaded = "Not loaded: " + strings.TrimPrefix(err.Error(), "invalid: ")
			return true
		}
		r.Loaded, r.Added, r.Skipped = "Loaded on Compliance → Deceased clients: debits blocked for each client matched", l.Matched, l.NotClients
		r.Note = l.summary()
		return true
	}
	l, err := o.applyMNRLFile(ctx, tenant, actor, lf)
	if err != nil {
		r.Loaded = "Not loaded: " + strings.TrimPrefix(err.Error(), "invalid: ")
		return true
	}
	r.Loaded, r.Added, r.Skipped = "Loaded on Clients & onboarding → Revoked mobiles", l.New, l.Skipped
	r.Note = l.summary()
	return true
}
