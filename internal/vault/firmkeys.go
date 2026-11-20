package vault

import (
	"bytes"
	"context"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"ledgerline/internal/docs"
)

// Keys the firm holds: the per-firm root key, stored only wrapped by a key
// the firm controls, and the cache of unwrapped roots. The design is in the
// package comment.

// FirmVersionBase is the first key version that names a firm's own root:
// root version n is key version FirmVersionBase+n in a ciphertext. Server
// master keys must stay below it.
const FirmVersionBase = 1 << 24

// KeyCollection holds each firm's key record (one document, ID "current").
// It is sealed under Ledgerline's key: it has to be read before the firm's
// key can be reached, and the roots in it are useless without that key.
const KeyCollection = "firmkey"

const keyRecordID = "current"

// Cache window: how long unwrapped roots stay in memory before the firm's
// key is asked again. A revocation takes effect within one window.
const (
	DefaultCacheMinutes = 15
	MinCacheMinutes     = 1
	MaxCacheMinutes     = 60
)

const (
	// recheckUnmanaged: how often a firm on Ledgerline's key is checked for
	// a key record written by another server.
	recheckUnmanaged = 5 * time.Minute
	// retryDown: while the firm's key is refused, it is asked again at most
	// this often; requests in between fail at once.
	retryDown = 30 * time.Second
	// callTimeout bounds one round of calls to the firm's key.
	callTimeout = 15 * time.Second
)

// ErrKeyUnavailable means the firm holds its own key and Ledgerline cannot
// use it now: the firm's key service refused (key disabled, grant or access
// revoked) or could not be reached. Nothing of the firm's is served or
// written until it is available again; nothing falls back to Ledgerline's
// master key.
var ErrKeyUnavailable = errors.New("your firm's encryption key is not available (revoked or unreachable)")

// KeyError is ErrKeyUnavailable with the reason.
type KeyError struct {
	Reason  string
	Refused bool // the key service said no; otherwise it could not be reached
}

func (e *KeyError) Error() string {
	if e.Reason == "" {
		return ErrKeyUnavailable.Error()
	}
	return ErrKeyUnavailable.Error() + ": " + e.Reason
}

// Is makes errors.Is(err, ErrKeyUnavailable) true.
func (e *KeyError) Is(target error) bool { return target == ErrKeyUnavailable }

// ProviderError is a key service's answer other than success. Refused is a
// definite no (the key is disabled or deleted, access was revoked, the
// ciphertext or encryption context does not match); otherwise the service
// failed or could not be reached and may answer later.
type ProviderError struct {
	Refused bool
	Code    string // the service's error code, such as DisabledException
	Msg     string
}

func (e *ProviderError) Error() string {
	if e.Code != "" {
		return e.Code + ": " + e.Msg
	}
	return e.Msg
}

// KeyProvider wraps and unwraps a firm's root key with a key the firm
// controls. bind is the encryption context (see Bind): the provider must
// bind the wrapped key to it, so a wrapped root cannot be unwrapped for
// another firm, purpose or version. keyID names the firm's key that wrapped
// the blob (the KMS key ARN).
type KeyProvider interface {
	Wrap(ctx context.Context, root []byte, bind map[string]string) (blob []byte, keyID string, err error)
	Unwrap(ctx context.Context, blob []byte, keyID string, bind map[string]string) ([]byte, error)
}

// Encryption context keys sent with every wrap and unwrap. A firm's key
// policy can require them (kms:EncryptionContext:ledgerline-tenant).
const (
	CtxTenant  = "ledgerline-tenant"
	CtxPurpose = "ledgerline-purpose"
	CtxVersion = "ledgerline-root-version"
	// PurposeRoot is the purpose of every wrapped root. A test from the
	// screen wraps a throwaway key with the same purpose and version 0.
	PurposeRoot = "firm-root-key"
)

// Bind is the encryption context for root version of tenant.
func Bind(tenant string, version int) map[string]string {
	return map[string]string{CtxTenant: tenant, CtxPurpose: PurposeRoot, CtxVersion: strconv.Itoa(version)}
}

// FirmKeyRecord is a firm's key record.
type FirmKeyRecord struct {
	Conn         string        `json:"conn"`    // the connection with the firm's key (Settings → Connections)
	Current      int           `json:"current"` // root version for new writes
	Roots        []WrappedRoot `json:"roots"`
	CacheMinutes int           `json:"cache_minutes"`
	Since        time.Time     `json:"since"`
	SetBy        string        `json:"set_by"`
	RotatedAt    time.Time     `json:"rotated_at,omitzero"`
	RotatedBy    string        `json:"rotated_by,omitempty"`
	Outage       *KeyOutage    `json:"outage,omitempty"` // the key is refused now; raised once per outage
}

// WrappedRoot is one root version as stored: wrapped by the firm's key.
type WrappedRoot struct {
	Version   int       `json:"version"`
	Blob      []byte    `json:"blob"`
	KeyID     string    `json:"key_id"`
	WrappedAt time.Time `json:"wrapped_at"`
}

// KeyOutage is an outage of the firm's key that has been announced.
type KeyOutage struct {
	Since   time.Time `json:"since"`
	Reason  string    `json:"reason"`
	Refused bool      `json:"refused"`
}

// Kinds of KeyEvent.
const (
	KeyLost = "unavailable"
	KeyBack = "restored"
)

// KeyEvent tells the application that a firm's key was lost or came back.
// Each is sent once per outage, even across restarts (the outage is kept
// in the key record).
type KeyEvent struct {
	Kind    string
	Reason  string
	Refused bool
	Since   time.Time // when the outage began
	At      time.Time
}

// FirmKeyHooks connect the vault to the application. Resolve builds the
// provider for a firm from its connection; it must read only records that
// are ServerKeyed (it runs while the firm's data is locked). Notify is told
// of outages (an audit entry and a work item). Now is the clock.
type FirmKeyHooks struct {
	Resolve func(ctx context.Context, tenant string, rec FirmKeyRecord) (KeyProvider, error)
	Notify  func(tenant string, ev KeyEvent)
	Now     func() time.Time
}

// SetFirmKeyHooks installs the hooks.
func (v *Vault) SetFirmKeyHooks(h FirmKeyHooks) {
	v.fk.mu.Lock()
	v.fk.hooks = h
	v.fk.mu.Unlock()
}

// PinFirmKeys lists firms this server knows hold their own key. For them a
// missing key record is an error, not a firm on Ledgerline's key, so
// removing the record in the database cannot move new writes back to
// Ledgerline's key.
func (v *Vault) PinFirmKeys(tenants ...string) {
	v.fk.mu.Lock()
	for _, t := range tenants {
		if t = strings.TrimSpace(t); t != "" {
			v.fk.pinned[t] = true
		}
	}
	v.fk.mu.Unlock()
}

// serverKeyedRecs are the documents kept under Ledgerline's own key even
// for a firm that holds its own key (see the package comment).
var serverKeyedRecs = []struct{ coll, prefix string }{{KeyCollection, ""}}

// ServerKeyed marks documents (a collection, and an ID prefix) that stay
// under Ledgerline's key: the connection with the firm key's credentials
// and the work item raised when it is refused. Call it from init only.
// Only records Ledgerline needs to reach the firm's key or to tell the firm
// it is gone belong here; never client data.
func ServerKeyed(coll, idPrefix string) {
	serverKeyedRecs = append(serverKeyedRecs, struct{ coll, prefix string }{coll, idPrefix})
}

func serverKeyed(coll, id string) bool {
	for _, r := range serverKeyedRecs {
		if r.coll == coll && strings.HasPrefix(id, r.prefix) {
			return true
		}
	}
	return false
}

type firmKeys struct {
	mu      sync.Mutex
	store   docs.Store // the unencrypted store the key records are in
	hooks   FirmKeyHooks
	pinned  map[string]bool
	tenants map[string]*firmState
}

func newFirmKeys() *firmKeys {
	return &firmKeys{pinned: map[string]bool{}, tenants: map[string]*firmState{}}
}

// firmState is what the vault knows of one firm's key. Its lock is held
// while the firm's key is called, so a firm's requests wait for one call
// instead of each making its own.
type firmState struct {
	mu      sync.Mutex
	loaded  bool
	checked time.Time
	rec     *FirmKeyRecord // nil: the firm is on Ledgerline's key
	roots   map[int][]byte // unwrapped, by root version; nil when not held
	aeads   map[int]cipher.AEAD
	got     time.Time // when the roots were unwrapped
	until   time.Time // when they are dropped
	lastOK  time.Time // last successful unwrap
	down    *KeyError // the key is refused now
	downAt  time.Time
	tried   time.Time // the last call while down
}

func (st *firmState) drop() {
	for _, k := range st.roots {
		clear(k)
	}
	st.roots, st.aeads = nil, nil
}

func (v *Vault) bindStore(s docs.Store) {
	v.fk.mu.Lock()
	if v.fk.store == nil {
		v.fk.store = s
	}
	v.fk.mu.Unlock()
}

func (v *Vault) now() time.Time {
	v.fk.mu.Lock()
	f := v.fk.hooks.Now
	v.fk.mu.Unlock()
	if f == nil {
		return time.Now()
	}
	return f()
}

func (v *Vault) firmState(tenant string) (*firmState, docs.Store) {
	v.fk.mu.Lock()
	defer v.fk.mu.Unlock()
	if v.fk.store == nil {
		return nil, nil
	}
	st := v.fk.tenants[tenant]
	if st == nil {
		st = &firmState{}
		v.fk.tenants[tenant] = st
	}
	return st, v.fk.store
}

func cacheFor(rec *FirmKeyRecord) time.Duration {
	m := rec.CacheMinutes
	if m == 0 {
		m = DefaultCacheMinutes
	}
	return time.Duration(min(max(m, MinCacheMinutes), MaxCacheMinutes)) * time.Minute
}

// stale: at least d since t, or the clock went back past t.
func stale(now, t time.Time, d time.Duration) bool { return now.Before(t) || now.Sub(t) >= d }

// firmAEAD is the cipher for a firm on its own key: for a new write (the
// current root) or for stored key version ver. managed is false for a firm
// on Ledgerline's key, whose caller then uses the server keys.
func (v *Vault) firmAEAD(tenant string, ver int, write bool) (a cipher.AEAD, outVer int, managed bool, err error) {
	st, store := v.firmState(tenant)
	if st == nil {
		return nil, 0, false, nil // stores not bound (a bare vault): server keys only
	}
	var ev *KeyEvent
	st.mu.Lock()
	a, outVer, managed, err = v.firmAEADLocked(st, store, tenant, ver, write, &ev)
	st.mu.Unlock()
	if ev != nil {
		v.announce(tenant, store, *ev)
	}
	return a, outVer, managed, err
}

func (v *Vault) firmAEADLocked(st *firmState, store docs.Store, tenant string, ver int, write bool, ev **KeyEvent) (cipher.AEAD, int, bool, error) {
	now := v.now()
	if err := v.loadLocked(st, store, tenant, now); err != nil {
		return nil, 0, true, err // fail closed: never guess the firm is on Ledgerline's key
	}
	if st.rec == nil {
		return nil, 0, false, nil
	}
	if err := v.unwrapLocked(st, tenant, now, false, ev); err != nil {
		return nil, 0, true, err
	}
	if write {
		ver = FirmVersionBase + st.rec.Current
	}
	if ver < FirmVersionBase {
		// Written before the firm moved to its own key: readable, but only
		// while the firm's key is available (checked just above).
		a, err := v.serverAEAD(tenant, "data", ver)
		return a, ver, true, err
	}
	rv := ver - FirmVersionBase
	if a, ok := st.aeads[rv]; ok {
		return a, ver, true, nil
	}
	root, ok := st.roots[rv]
	if !ok {
		return nil, ver, true, fmt.Errorf("vault: root version %d is not in the firm's key record", rv)
	}
	k, err := hkdf.Key(sha256.New, root, []byte("ledgerline-firm-root-v1"), "data\x00"+tenant, 32)
	if err != nil {
		return nil, ver, true, err
	}
	a, err := newGCM(k)
	if err != nil {
		return nil, ver, true, err
	}
	if st.aeads == nil {
		st.aeads = map[int]cipher.AEAD{}
	}
	st.aeads[rv] = a
	return a, ver, true, nil
}

func (v *Vault) readRecord(ctx context.Context, store docs.Store, tenant string) (FirmKeyRecord, int64, error) {
	return docs.Get[FirmKeyRecord](ctx, &Docs{Inner: store, V: v}, tenant, KeyCollection, keyRecordID)
}

// loadLocked reads the firm's key record when it is not known or is due a
// fresh look.
func (v *Vault) loadLocked(st *firmState, store docs.Store, tenant string, now time.Time) error {
	ttl := recheckUnmanaged
	if st.rec != nil {
		ttl = cacheFor(st.rec)
	}
	if st.loaded && !stale(now, st.checked, ttl) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	rec, _, err := v.readRecord(ctx, store, tenant)
	if errors.Is(err, docs.ErrNotFound) || (err == nil && len(rec.Roots) == 0) {
		v.fk.mu.Lock()
		pinned := v.fk.pinned[tenant]
		v.fk.mu.Unlock()
		if st.rec != nil || pinned {
			return &KeyError{Reason: "the firm's key record is missing from the database"}
		}
		st.loaded, st.checked = true, now
		return nil
	}
	if err != nil {
		return fmt.Errorf("vault: the firm's key record cannot be read: %w", err)
	}
	if st.rec == nil || !sameRoots(st.rec, &rec) {
		st.drop() // moved or rotated (perhaps by another server): unwrap afresh
	}
	st.rec, st.loaded, st.checked = &rec, true, now
	return nil
}

func sameRoots(a, b *FirmKeyRecord) bool {
	if a.Current != b.Current || len(a.Roots) != len(b.Roots) {
		return false
	}
	for i := range a.Roots {
		if a.Roots[i].Version != b.Roots[i].Version || !bytes.Equal(a.Roots[i].Blob, b.Roots[i].Blob) {
			return false
		}
	}
	return true
}

// unwrapLocked makes sure the firm's roots are in memory, asking the firm's
// key when they are not or the cache window has passed.
func (v *Vault) unwrapLocked(st *firmState, tenant string, now time.Time, force bool, ev **KeyEvent) error {
	if !force && st.roots != nil && !stale(now, st.got, st.until.Sub(st.got)) {
		return nil
	}
	st.drop()
	if !force && st.down != nil && !stale(now, st.tried, retryDown) {
		return st.down
	}
	roots, err := v.unwrapAll(tenant, *st.rec)
	if err != nil {
		ke := toKeyError(err)
		if st.down == nil {
			st.downAt = now
			*ev = &KeyEvent{Kind: KeyLost, Reason: ke.Reason, Refused: ke.Refused, Since: now, At: now}
		}
		st.down, st.tried = ke, now
		return ke
	}
	st.roots, st.got, st.until, st.lastOK = roots, now, now.Add(cacheFor(st.rec)), now
	if st.down != nil || st.rec.Outage != nil {
		*ev = &KeyEvent{Kind: KeyBack, Since: st.downAt, At: now}
	}
	st.down = nil
	return nil
}

func toKeyError(err error) *KeyError {
	var ke *KeyError
	if errors.As(err, &ke) {
		return ke
	}
	var pe *ProviderError
	if errors.As(err, &pe) {
		return &KeyError{Reason: pe.Error(), Refused: pe.Refused}
	}
	return &KeyError{Reason: err.Error()}
}

func (v *Vault) unwrapAll(tenant string, rec FirmKeyRecord) (map[int][]byte, error) {
	v.fk.mu.Lock()
	resolve := v.fk.hooks.Resolve
	v.fk.mu.Unlock()
	if resolve == nil {
		return nil, &KeyError{Reason: "this server is not set up to reach firms' own keys"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	p, err := resolve(ctx, tenant, rec)
	if err != nil {
		return nil, err
	}
	return unwrapRoots(ctx, p, tenant, rec)
}

func unwrapRoots(ctx context.Context, p KeyProvider, tenant string, rec FirmKeyRecord) (map[int][]byte, error) {
	roots := map[int][]byte{}
	for _, r := range rec.Roots {
		k, err := p.Unwrap(ctx, r.Blob, r.KeyID, Bind(tenant, r.Version))
		if err == nil && len(k) != 32 {
			err = &ProviderError{Refused: true, Msg: fmt.Sprintf("root version %d unwrapped to %d bytes, not 32", r.Version, len(k))}
		}
		if err != nil {
			for _, x := range roots {
				clear(x)
			}
			return nil, err
		}
		roots[r.Version] = k
	}
	return roots, nil
}

var errNoChange = errors.New("no change")

// announce records an outage (or its end) in the key record and tells the
// application, once per outage.
func (v *Vault) announce(tenant string, store docs.Store, ev KeyEvent) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	first := false
	rec, err := docs.Update(ctx, &Docs{Inner: store, V: v}, tenant, KeyCollection, keyRecordID, "ledgerline", func(r *FirmKeyRecord) error {
		switch ev.Kind {
		case KeyLost:
			if r.Outage != nil {
				return errNoChange // announced already, before a restart
			}
			r.Outage = &KeyOutage{Since: ev.Since, Reason: ev.Reason, Refused: ev.Refused}
		case KeyBack:
			if r.Outage == nil {
				return errNoChange
			}
			ev.Since = r.Outage.Since
			r.Outage = nil
		}
		first = true
		return nil
	})
	if err == nil {
		if st, _ := v.firmState(tenant); st != nil {
			st.mu.Lock()
			if st.rec != nil {
				st.rec.Outage = rec.Outage
			}
			st.mu.Unlock()
		}
	} else if !errors.Is(err, errNoChange) {
		first = true // the record could not be updated: better told twice than not at all
	}
	v.fk.mu.Lock()
	notify := v.fk.hooks.Notify
	v.fk.mu.Unlock()
	if first && notify != nil {
		notify(tenant, ev)
	}
}

// ---- moving, rotating and status ----

// FirmKeyStatus is what the vault knows of a firm's key, for the screen.
type FirmKeyStatus struct {
	Own          bool       `json:"own"` // the firm holds its own key
	Conn         string     `json:"conn,omitempty"`
	Current      int        `json:"current,omitempty"`
	Roots        []RootInfo `json:"roots,omitempty"`
	CacheMinutes int        `json:"cache_minutes,omitempty"`
	Since        time.Time  `json:"since,omitzero"`
	SetBy        string     `json:"set_by,omitempty"`
	RotatedAt    time.Time  `json:"rotated_at,omitzero"`
	RotatedBy    string     `json:"rotated_by,omitempty"`
	Available    bool       `json:"available"`               // the roots are in memory now
	Problem      string     `json:"problem,omitempty"`       // why the key is not available
	Refused      bool       `json:"refused,omitempty"`       // the firm's key said no
	Outage       *KeyOutage `json:"outage,omitempty"`        // as recorded
	LastOK       time.Time  `json:"last_ok,omitzero"`        // last successful unwrap by this server
	CachedUntil  time.Time  `json:"cached_until,omitzero"`   // roots dropped from memory then
	Pinned       bool       `json:"pinned,omitempty"`        // the server expects this firm on its own key
	MasterSource string     `json:"master_source,omitempty"` // Ledgerline's keys (for older records and seals)
}

// RootInfo is one root version.
type RootInfo struct {
	Version   int       `json:"version"`
	KeyID     string    `json:"key_id"`
	WrappedAt time.Time `json:"wrapped_at"`
}

func (v *Vault) statusLocked(st *firmState, tenant string) FirmKeyStatus {
	s := FirmKeyStatus{MasterSource: v.keys.Describe()}
	v.fk.mu.Lock()
	s.Pinned = v.fk.pinned[tenant]
	v.fk.mu.Unlock()
	if st.rec == nil {
		return s
	}
	r := st.rec
	s.Own, s.Conn, s.Current, s.CacheMinutes = true, r.Conn, r.Current, int(cacheFor(r)/time.Minute)
	s.Since, s.SetBy, s.RotatedAt, s.RotatedBy, s.Outage = r.Since, r.SetBy, r.RotatedAt, r.RotatedBy, r.Outage
	for _, x := range r.Roots {
		s.Roots = append(s.Roots, RootInfo{Version: x.Version, KeyID: x.KeyID, WrappedAt: x.WrappedAt})
	}
	sort.Slice(s.Roots, func(i, j int) bool { return s.Roots[i].Version > s.Roots[j].Version })
	s.LastOK = st.lastOK
	now := v.now()
	if st.roots != nil && !stale(now, st.got, st.until.Sub(st.got)) {
		s.Available, s.CachedUntil = true, st.until
	}
	if st.down != nil {
		s.Problem, s.Refused = st.down.Reason, st.down.Refused
	}
	return s
}

func (v *Vault) lockedState(tenant string) (*firmState, docs.Store, error) {
	st, store := v.firmState(tenant)
	if st == nil {
		return nil, nil, errors.New("vault: this server's stores are not protected by the vault")
	}
	return st, store, nil
}

// FirmKey reports the firm's key without calling it.
func (v *Vault) FirmKey(ctx context.Context, tenant string) (FirmKeyStatus, error) {
	st, store, err := v.lockedState(tenant)
	if err != nil {
		return FirmKeyStatus{MasterSource: v.keys.Describe()}, nil
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if err := v.loadLocked(st, store, tenant, v.now()); err != nil && st.rec == nil {
		s := v.statusLocked(st, tenant)
		s.Problem = toKeyError(err).Reason
		return s, nil
	}
	return v.statusLocked(st, tenant), nil
}

// CheckFirmKey asks the firm's key now, whatever the cache holds, and
// reports the result. A refusal is announced as any other.
func (v *Vault) CheckFirmKey(ctx context.Context, tenant string) (FirmKeyStatus, error) {
	st, store, err := v.lockedState(tenant)
	if err != nil {
		return FirmKeyStatus{}, err
	}
	var ev *KeyEvent
	st.mu.Lock()
	now := v.now()
	st.loaded = false
	err = v.loadLocked(st, store, tenant, now)
	if err == nil && st.rec == nil {
		err = errors.New("the firm is on Ledgerline's key")
	}
	if err == nil {
		err = v.unwrapLocked(st, tenant, now, true, &ev)
	}
	s := v.statusLocked(st, tenant)
	st.mu.Unlock()
	if ev != nil {
		v.announce(tenant, store, *ev)
	}
	return s, err
}

func newRoot() []byte {
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		panic(err)
	}
	return k
}

func clampCache(m int) int {
	if m == 0 {
		return DefaultCacheMinutes
	}
	return min(max(m, MinCacheMinutes), MaxCacheMinutes)
}

// wrapChecked wraps root and unwraps it again, so a key that can encrypt
// but not decrypt (a policy missing kms:Decrypt) is caught before any data
// is written under it.
func wrapChecked(ctx context.Context, p KeyProvider, tenant string, version int, root []byte) (WrappedRoot, error) {
	b := Bind(tenant, version)
	blob, keyID, err := p.Wrap(ctx, root, b)
	if err != nil {
		return WrappedRoot{}, fmt.Errorf("the firm's key did not wrap the key: %w", err)
	}
	back, err := p.Unwrap(ctx, blob, keyID, b)
	if err != nil {
		return WrappedRoot{}, fmt.Errorf("the firm's key wrapped the key but did not unwrap it: %w", err)
	}
	if !bytes.Equal(back, root) {
		return WrappedRoot{}, &ProviderError{Refused: true, Msg: "the firm's key unwrapped a different key"}
	}
	clear(back)
	return WrappedRoot{Version: version, Blob: blob, KeyID: keyID}, nil
}

// TestFirmKey wraps and unwraps a throwaway key with p, as a check that
// the key and its policy work for this firm. Nothing is stored.
func TestFirmKey(ctx context.Context, p KeyProvider, tenant string) (keyID string, err error) {
	k := newRoot()
	defer clear(k)
	w, err := wrapChecked(ctx, p, tenant, 0, k)
	return w.KeyID, err
}

func (v *Vault) putRecord(ctx context.Context, store docs.Store, tenant string, rec FirmKeyRecord, expect int64, actor string) error {
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	_, err = (&Docs{Inner: store, V: v}).Put(ctx, tenant, KeyCollection, keyRecordID, b, expect, false, actor)
	return err
}

// UseFirmKey moves a firm onto its own key: a new root, wrapped by p (the
// firm's key, through connection conn), checked and stored. New writes are
// under it at once; older records stay readable while the key is available.
func (v *Vault) UseFirmKey(ctx context.Context, tenant, conn string, p KeyProvider, cacheMinutes int, actor string) (FirmKeyStatus, error) {
	st, store, err := v.lockedState(tenant)
	if err != nil {
		return FirmKeyStatus{}, err
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	cur, ver, err := v.readRecord(ctx, store, tenant)
	if err != nil && !errors.Is(err, docs.ErrNotFound) {
		return FirmKeyStatus{}, err
	}
	if err == nil && len(cur.Roots) > 0 {
		return FirmKeyStatus{}, errors.New("the firm is already on its own key: rotate it instead")
	}
	root := newRoot()
	w, err := wrapChecked(ctx, p, tenant, 1, root)
	if err != nil {
		clear(root)
		return FirmKeyStatus{}, err
	}
	now := v.now()
	w.WrappedAt = now
	rec := FirmKeyRecord{Conn: conn, Current: 1, Roots: []WrappedRoot{w}, CacheMinutes: clampCache(cacheMinutes), Since: now, SetBy: actor}
	if err := v.putRecord(ctx, store, tenant, rec, ver, actor); err != nil {
		clear(root)
		return FirmKeyStatus{}, err
	}
	st.drop()
	st.rec, st.loaded, st.checked = &rec, true, now
	st.roots, st.got, st.until, st.lastOK, st.down = map[int][]byte{1: root}, now, now.Add(cacheFor(&rec)), now, nil
	return v.statusLocked(st, tenant), nil
}

// RotateFirmKey makes a new root version for new writes and re-wraps every
// older root under p, the firm's key as its connection now names it. The
// old roots are unwrapped with the key that wrapped each, so the old key
// must still work until this has finished; afterwards it can be retired.
func (v *Vault) RotateFirmKey(ctx context.Context, tenant string, p KeyProvider, actor string) (FirmKeyStatus, error) {
	st, store, err := v.lockedState(tenant)
	if err != nil {
		return FirmKeyStatus{}, err
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	rec, ver, err := v.readRecord(ctx, store, tenant)
	if errors.Is(err, docs.ErrNotFound) || (err == nil && len(rec.Roots) == 0) {
		return FirmKeyStatus{}, errors.New("the firm is on Ledgerline's key: set up its own key first")
	}
	if err != nil {
		return FirmKeyStatus{}, err
	}
	old, err := unwrapRoots(ctx, p, tenant, rec)
	if err != nil {
		return FirmKeyStatus{}, fmt.Errorf("the firm's key did not unwrap the current keys: %w", err)
	}
	now := v.now()
	next := 0
	for n := range old {
		next = max(next, n)
	}
	next++
	old[next] = newRoot()
	var roots []WrappedRoot
	for _, n := range sortedVersions(old) {
		w, err := wrapChecked(ctx, p, tenant, n, old[n])
		if err != nil {
			for _, x := range old {
				clear(x)
			}
			return FirmKeyStatus{}, err
		}
		w.WrappedAt = now
		roots = append(roots, w)
	}
	rec.Roots, rec.Current, rec.RotatedAt, rec.RotatedBy = roots, next, now, actor
	if err := v.putRecord(ctx, store, tenant, rec, ver, actor); err != nil {
		for _, x := range old {
			clear(x)
		}
		return FirmKeyStatus{}, err
	}
	st.drop()
	st.rec, st.loaded, st.checked = &rec, true, now
	st.roots, st.got, st.until, st.lastOK = old, now, now.Add(cacheFor(&rec)), now
	return v.statusLocked(st, tenant), nil
}

func sortedVersions(m map[int][]byte) []int {
	out := make([]int, 0, len(m))
	for n := range m {
		out = append(out, n)
	}
	sort.Ints(out)
	return out
}

// SetFirmKeyCache changes how long the firm's unwrapped keys stay in
// memory. It applies from the next unwrap.
func (v *Vault) SetFirmKeyCache(ctx context.Context, tenant string, minutes int, actor string) (FirmKeyStatus, error) {
	st, store, err := v.lockedState(tenant)
	if err != nil {
		return FirmKeyStatus{}, err
	}
	if minutes < MinCacheMinutes || minutes > MaxCacheMinutes {
		return FirmKeyStatus{}, fmt.Errorf("the cache window must be from %d to %d minutes", MinCacheMinutes, MaxCacheMinutes)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	rec, err := docs.Update(ctx, &Docs{Inner: store, V: v}, tenant, KeyCollection, keyRecordID, actor, func(r *FirmKeyRecord) error {
		if len(r.Roots) == 0 {
			return errors.New("the firm is on Ledgerline's key")
		}
		r.CacheMinutes = minutes
		return nil
	})
	if errors.Is(err, docs.ErrNotFound) {
		return FirmKeyStatus{}, errors.New("the firm is on Ledgerline's key")
	}
	if err != nil {
		return FirmKeyStatus{}, err
	}
	if st.rec != nil && sameRoots(st.rec, &rec) && st.roots != nil {
		st.until = st.got.Add(cacheFor(&rec))
	} else {
		st.drop()
	}
	st.rec, st.loaded, st.checked = &rec, true, v.now()
	return v.statusLocked(st, tenant), nil
}

// VerifyFirmKeyProvider unwraps the firm's stored roots with p and changes
// nothing: a check that new connection details still reach the firm's key.
func (v *Vault) VerifyFirmKeyProvider(ctx context.Context, tenant string, p KeyProvider) error {
	_, store, err := v.lockedState(tenant)
	if err != nil {
		return err
	}
	rec, _, err := v.readRecord(ctx, store, tenant)
	if err != nil {
		return err
	}
	roots, err := unwrapRoots(ctx, p, tenant, rec)
	for _, k := range roots {
		clear(k)
	}
	return err
}
