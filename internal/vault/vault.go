// Package vault encrypts what Ledgerline stores about clients: every
// document (client master, applications, users, settings, workflow records)
// and every stored file (KYC documents, exchange and bank files, generated
// reports). It also seals the audit trail so an edited or deleted entry is
// detected.
//
// Encryption is AES-256-GCM. Each firm (tenant) has its own keys, derived
// with HKDF-SHA256 from a master key, so one firm's data cannot be read with
// another firm's key. Master keys are versioned: new writes use the current
// version and older versions stay available for reading, so keys can be
// rotated without rewriting history. The Keys interface is where a cloud key
// service (AWS KMS in Mumbai, or a firm's own key) plugs in later.
//
// The stores below sit between the application and the database. The
// database only ever sees ciphertext for document bodies and file contents;
// identifiers (UCC, document IDs), dates and ledger amounts stay in clear so
// the database can still index and enforce its rules.
package vault

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"ledgerline/internal/app"
	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
)

// ErrDecrypt means stored data could not be decrypted: the wrong key, a
// missing key version, or data that was altered in the database.
var ErrDecrypt = errors.New("vault: stored data could not be decrypted (wrong key or altered data)")

// Keys supplies per-tenant key material.
type Keys interface {
	// Current is the key version used for new writes.
	Current() int
	// Key returns 32 bytes of key material for a tenant, purpose and version.
	Key(tenant, purpose string, version int) ([]byte, error)
	// Describe says where keys come from, for the security page.
	Describe() string
}

// Keyring derives tenant keys from versioned 32-byte master keys held by
// the server (an environment variable or file today; a key service later).
type Keyring struct {
	masters map[int][]byte
	current int
	source  string
}

// NewKeyring builds a keyring. current must be one of the versions.
func NewKeyring(masters map[int][]byte, current int, source string) (*Keyring, error) {
	if len(masters) == 0 {
		return nil, errors.New("vault: no master keys")
	}
	for v, k := range masters {
		if v <= 0 {
			return nil, fmt.Errorf("vault: key version %d must be positive", v)
		}
		if len(k) != 32 {
			return nil, fmt.Errorf("vault: master key version %d must be 32 bytes, got %d", v, len(k))
		}
	}
	if _, ok := masters[current]; !ok {
		return nil, fmt.Errorf("vault: current key version %d not present", current)
	}
	return &Keyring{masters: masters, current: current, source: source}, nil
}

// ParseKeyring reads "2:<base64>,1:<base64>". The first entry is the
// current key; the others are older keys kept for reading.
func ParseKeyring(spec, source string) (*Keyring, error) {
	masters, current := map[int][]byte{}, 0
	for i, part := range strings.Split(strings.TrimSpace(spec), ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		vs, b64, ok := strings.Cut(part, ":")
		if !ok {
			return nil, errors.New("vault: keys must look like 1:<base64 of 32 bytes>")
		}
		v, err := strconv.Atoi(vs)
		if err != nil {
			return nil, fmt.Errorf("vault: bad key version %q", vs)
		}
		k, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, fmt.Errorf("vault: key version %d is not base64", v)
		}
		if _, dup := masters[v]; dup {
			return nil, fmt.Errorf("vault: key version %d given twice", v)
		}
		masters[v] = k
		if i == 0 {
			current = v
		}
	}
	return NewKeyring(masters, current, source)
}

// NewKey returns a fresh master key in the keyring format for version v.
func NewKey(v int) string {
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		panic(err)
	}
	return fmt.Sprintf("%d:%s", v, base64.StdEncoding.EncodeToString(k))
}

// Ephemeral returns a random keyring that lives only as long as the
// process; for in-memory development servers and tests.
func Ephemeral() *Keyring {
	k, err := ParseKeyring(NewKey(1), "temporary key (development; data is lost on exit)")
	if err != nil {
		panic(err)
	}
	return k
}

func (k *Keyring) Current() int     { return k.current }
func (k *Keyring) Describe() string { return k.source }

// Versions lists the key versions held, newest first.
func (k *Keyring) Versions() []int {
	out := make([]int, 0, len(k.masters))
	for v := range k.masters {
		out = append(out, v)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(out)))
	return out
}

func (k *Keyring) Key(tenant, purpose string, version int) ([]byte, error) {
	m, ok := k.masters[version]
	if !ok {
		return nil, fmt.Errorf("vault: key version %d is not loaded", version)
	}
	return hkdf.Key(sha256.New, m, []byte("ledgerline-vault-v1"), purpose+"\x00"+tenant, 32)
}

// Vault encrypts and decrypts with per-tenant keys, caching derived keys.
type Vault struct {
	keys  Keys
	mu    sync.Mutex
	aeads map[string]cipher.AEAD
	macs  map[string][]byte
}

// New returns a Vault over keys.
func New(keys Keys) *Vault {
	return &Vault{keys: keys, aeads: map[string]cipher.AEAD{}, macs: map[string][]byte{}}
}

// Keys returns the key source.
func (v *Vault) Keys() Keys { return v.keys }

func (v *Vault) aead(tenant string, version int) (cipher.AEAD, error) {
	ck := tenant + "\x00" + strconv.Itoa(version)
	v.mu.Lock()
	defer v.mu.Unlock()
	if a, ok := v.aeads[ck]; ok {
		return a, nil
	}
	k, err := v.keys.Key(tenant, "data", version)
	if err != nil {
		return nil, err
	}
	b, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	a, err := cipher.NewGCM(b)
	if err != nil {
		return nil, err
	}
	v.aeads[ck] = a
	return a, nil
}

// magic marks encrypted binary content: "LLV1", key version (4 bytes),
// nonce (12 bytes), ciphertext and tag.
var magic = []byte("LLV1")

// Seal encrypts pt for tenant, bound to aad (the record's identity, so a
// ciphertext cannot be moved to another record).
func (v *Vault) Seal(tenant string, aad, pt []byte) ([]byte, error) {
	ver := v.keys.Current()
	a, err := v.aead(tenant, ver)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(magic)+4+a.NonceSize()+len(pt)+a.Overhead())
	out = append(out, magic...)
	out = binary.BigEndian.AppendUint32(out, uint32(ver))
	nonce := make([]byte, a.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out = append(out, nonce...)
	return a.Seal(out, nonce, pt, aad), nil
}

// Sealed reports whether b was produced by Seal.
func Sealed(b []byte) bool { return len(b) > len(magic)+4 && bytes.Equal(b[:len(magic)], magic) }

// Open decrypts b. Data that was never encrypted (written before encryption
// was switched on) is returned as it is.
func (v *Vault) Open(tenant string, aad, b []byte) ([]byte, error) {
	if !Sealed(b) {
		return b, nil
	}
	ver := int(binary.BigEndian.Uint32(b[len(magic):]))
	a, err := v.aead(tenant, ver)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDecrypt, err)
	}
	rest := b[len(magic)+4:]
	if len(rest) < a.NonceSize()+a.Overhead() {
		return nil, ErrDecrypt
	}
	pt, err := a.Open(nil, rest[:a.NonceSize()], rest[a.NonceSize():], aad)
	if err != nil {
		return nil, ErrDecrypt
	}
	return pt, nil
}

// MAC returns a keyed hash (HMAC-SHA256) for tenant with the current audit
// key, as "version:hex".
func (v *Vault) MAC(tenant string, msg []byte) (string, error) {
	return v.macWith(tenant, v.keys.Current(), msg)
}

func (v *Vault) macWith(tenant string, version int, msg []byte) (string, error) {
	ck := tenant + "\x00" + strconv.Itoa(version)
	v.mu.Lock()
	k, ok := v.macs[ck]
	v.mu.Unlock()
	if !ok {
		var err error
		if k, err = v.keys.Key(tenant, "audit", version); err != nil {
			return "", err
		}
		v.mu.Lock()
		v.macs[ck] = k
		v.mu.Unlock()
	}
	m := hmac.New(sha256.New, k)
	m.Write(msg)
	return strconv.Itoa(version) + ":" + hex.EncodeToString(m.Sum(nil)), nil
}

// CheckMAC verifies a value produced by MAC, with whichever key version it
// names.
func (v *Vault) CheckMAC(tenant string, msg []byte, mac string) bool {
	vs, _, ok := strings.Cut(mac, ":")
	if !ok {
		return false
	}
	ver, err := strconv.Atoi(vs)
	if err != nil {
		return false
	}
	want, err := v.macWith(tenant, ver, msg)
	return err == nil && hmac.Equal([]byte(want), []byte(mac))
}

// ---- stores ----

// Protect wraps a set of stores so document bodies and file contents are
// encrypted and the audit trail is sealed. Ledger, trade and break stores
// hold identifiers and amounts only and pass through.
func Protect(s app.Stores, v *Vault) app.Stores {
	s.Docs = &Docs{Inner: s.Docs, V: v}
	s.Raw = &Raw{Inner: s.Raw, V: v}
	s.Audit = &Audit{Inner: s.Audit, V: v}
	return s
}

// An encrypted document body is stored as one JSON string,
// "llv1:<base64 of the sealed bytes>". Documents themselves are always JSON
// objects, so a body that is a string is known to be encrypted, and the
// database column (jsonb) stays valid without changing type.
const envPrefix = `"llv1:`

func docAAD(tenant, coll, id string) []byte {
	return []byte("doc\x00" + tenant + "\x00" + coll + "\x00" + id)
}

// Docs is an encrypting docs.Store.
type Docs struct {
	Inner docs.Store
	V     *Vault
}

func (d *Docs) open(tenant string, x docs.Doc) (docs.Doc, error) {
	body := bytes.TrimSpace(x.Body)
	if !bytes.HasPrefix(body, []byte(envPrefix)) {
		return x, nil // written before encryption was switched on
	}
	if len(body) < len(envPrefix)+1 || body[len(body)-1] != '"' {
		return docs.Doc{}, fmt.Errorf("%s/%s: %w", x.Collection, x.ID, ErrDecrypt)
	}
	b64 := body[len(envPrefix) : len(body)-1]
	ct := make([]byte, base64.StdEncoding.DecodedLen(len(b64)))
	n, err := base64.StdEncoding.Decode(ct, b64)
	if err != nil {
		return docs.Doc{}, fmt.Errorf("%s/%s: %w", x.Collection, x.ID, ErrDecrypt)
	}
	pt, err := d.V.Open(tenant, docAAD(tenant, x.Collection, x.ID), ct[:n])
	if err != nil {
		return docs.Doc{}, fmt.Errorf("%s/%s: %w", x.Collection, x.ID, err)
	}
	x.Body = pt
	return x, nil
}

func (d *Docs) Get(ctx context.Context, tenant, coll, id string) (docs.Doc, error) {
	x, err := d.Inner.Get(ctx, tenant, coll, id)
	if err != nil {
		return x, err
	}
	if x.Collection == "" {
		x.Collection = coll
	}
	if x.ID == "" {
		x.ID = id
	}
	return d.open(tenant, x)
}

func (d *Docs) List(ctx context.Context, tenant, coll, prefix string) ([]docs.Doc, error) {
	xs, err := d.Inner.List(ctx, tenant, coll, prefix)
	if err != nil {
		return nil, err
	}
	for i := range xs {
		if xs[i].Collection == "" {
			xs[i].Collection = coll
		}
		if xs[i], err = d.open(tenant, xs[i]); err != nil {
			return nil, err
		}
	}
	return xs, nil
}

func (d *Docs) Put(ctx context.Context, tenant, coll, id string, body json.RawMessage, expect int64, upsert bool, actor string) (docs.Doc, error) {
	ct, err := d.V.Seal(tenant, docAAD(tenant, coll, id), body)
	if err != nil {
		return docs.Doc{}, err
	}
	env := make([]byte, 0, len(envPrefix)+base64.StdEncoding.EncodedLen(len(ct))+1)
	env = append(env, envPrefix...)
	env = base64.StdEncoding.AppendEncode(env, ct)
	env = append(env, '"')
	x, err := d.Inner.Put(ctx, tenant, coll, id, env, expect, upsert, actor)
	if err != nil {
		return x, err
	}
	x.Body = body
	return x, nil
}

func rawAAD(f ingest.RawFile) []byte {
	return []byte("raw\x00" + f.Tenant + "\x00" + f.Source + "\x00" + f.SHA256)
}

// Raw is an encrypting ingest.RawStore. The SHA-256 and size recorded are
// those of the original file, so duplicates are still detected and the
// hash shown to users is the file's own.
type Raw struct {
	Inner ingest.RawStore
	V     *Vault
}

func (r *Raw) Put(ctx context.Context, f ingest.RawFile) (ingest.RawFile, error) {
	plain := f.Content
	f.Size = int64(len(plain))
	ct, err := r.V.Seal(f.Tenant, rawAAD(f), plain)
	if err != nil {
		return ingest.RawFile{}, err
	}
	f.Content = ct
	out, err := r.Inner.Put(ctx, f)
	if err != nil {
		return out, err
	}
	out.Content, out.Size = plain, int64(len(plain))
	return out, nil
}

func (r *Raw) Get(ctx context.Context, tenant, id string) (ingest.RawFile, error) {
	f, err := r.Inner.Get(ctx, tenant, id)
	if err != nil {
		return f, err
	}
	if f.Tenant == "" {
		f.Tenant = tenant
	}
	pt, err := r.V.Open(tenant, rawAAD(f), f.Content)
	if err != nil {
		return ingest.RawFile{}, fmt.Errorf("file %s: %w", id, err)
	}
	f.Content, f.Size = pt, int64(len(pt))
	return f, nil
}

// Audit seals each entry into the tenant's chain when the underlying store
// supports it (both built-in stores do).
type Audit struct {
	Inner app.AuditStore
	V     *Vault
}

func (a *Audit) Log(ctx context.Context, e app.AuditEvent) error {
	c, ok := a.Inner.(app.ChainedAudit)
	if !ok {
		return a.Inner.Log(ctx, e)
	}
	return c.LogSealed(ctx, e, func(e app.AuditEvent) (string, error) {
		b, err := app.AuditCanonical(e)
		if err != nil {
			return "", err
		}
		return a.V.MAC(e.Tenant, b)
	})
}

func (a *Audit) List(ctx context.Context, tenant string, limit int) ([]app.AuditEvent, error) {
	return a.Inner.List(ctx, tenant, limit)
}

// AuditCheck is the result of verifying a tenant's audit trail.
type AuditCheck struct {
	OK        bool           `json:"ok"`
	Entries   int            `json:"entries"`
	Sealed    int            `json:"sealed"`
	Unsealed  int            `json:"unsealed"` // written before sealing was switched on
	Problems  []AuditProblem `json:"problems"`
	First     string         `json:"first,omitempty"` // time of the first sealed entry
	Last      string         `json:"last,omitempty"`
	LastHash  string         `json:"last_hash,omitempty"`
	KeySource string         `json:"key_source"`
}

// AuditProblem is one place the chain does not hold.
type AuditProblem struct {
	Seq    int64  `json:"seq"`
	At     string `json:"at"`
	Action string `json:"action"`
	Object string `json:"object"`
	Issue  string `json:"issue"`
}

// VerifyAudit walks the tenant's whole audit trail and checks every seal
// and every link. An entry changed in the database fails its seal; a
// removed or inserted entry breaks the link to its neighbour. Removing the
// newest entries cannot be seen from the chain alone, so the last hash is
// reported for recording elsewhere.
func (a *Audit) VerifyAudit(ctx context.Context, tenant string) (AuditCheck, error) {
	r := AuditCheck{OK: true, Problems: []AuditProblem{}, KeySource: a.V.Keys().Describe()}
	c, ok := a.Inner.(app.ChainedAudit)
	if !ok {
		return r, errors.New("vault: this audit store cannot be verified")
	}
	prev, after := "", int64(0)
	add := func(e app.AuditEvent, issue string) {
		r.OK = false
		if len(r.Problems) < 100 {
			r.Problems = append(r.Problems, AuditProblem{Seq: e.Seq, At: e.At.UTC().Format(time.RFC3339), Action: e.Action, Object: e.Object, Issue: issue})
		}
	}
	for {
		evs, err := c.Scan(ctx, tenant, after, 5000)
		if err != nil {
			return r, err
		}
		if len(evs) == 0 {
			break
		}
		for _, e := range evs {
			after = e.Seq
			r.Entries++
			if e.Hash == "" {
				if r.Sealed > 0 {
					add(e, "entry has no seal but sealing was already on: added directly to the database")
				} else {
					r.Unsealed++
				}
				continue
			}
			r.Sealed++
			if r.First == "" {
				r.First = e.At.UTC().Format(time.RFC3339)
			}
			if e.Prev != prev {
				add(e, "link broken: an entry before this one was removed, inserted or changed")
			}
			b, err := app.AuditCanonical(e)
			if err != nil || !a.V.CheckMAC(tenant, b, e.Hash) {
				add(e, "seal does not match: this entry was changed after it was written")
			}
			prev = e.Hash
			r.Last = e.At.UTC().Format(time.RFC3339)
		}
	}
	r.LastHash = prev
	return r, nil
}
