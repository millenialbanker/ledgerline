package kyc

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"ledgerline/internal/vault"
)

// Session is one investor's DigiLocker consent and what came of it.
type Session struct {
	ID        string   `json:"id"`
	Customer  string   `json:"customer"`
	Reference string   `json:"reference"` // the customer's own ID for the application
	ReturnURL string   `json:"return_url"`
	Hint      Hint     `json:"hint"`
	Status    string   `json:"status"` // Created, Started, Completed, Failed, Expired
	Error     string   `json:"error,omitempty"`
	Created   string   `json:"created"`
	Expires   string   `json:"expires"`
	Completed string   `json:"completed_at,omitempty"`
	Aadhaar   *Aadhaar `json:"aadhaar,omitempty"`
	PAN       *PAN     `json:"pan,omitempty"`
	Checks    []Check  `json:"checks,omitempty"`

	TokenHash string `json:"token_hash,omitempty"` // the start link's one-time token
	Nonce     string `json:"nonce,omitempty"`      // part of the OAuth state
	Verifier  string `json:"verifier,omitempty"`   // PKCE, until the code is exchanged
}

// Hint is what the investor told the customer, to compare with DigiLocker.
type Hint struct {
	Name string `json:"name,omitempty"`
	DOB  string `json:"dob,omitempty"` // YYYY-MM-DD
	PAN  string `json:"pan,omitempty"`
}

// Check is one comparison.
type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// Public is the session as the customer's API sees it.
func (s Session) Public() Session {
	s.TokenHash, s.Nonce, s.Verifier = "", "", ""
	switch s.Status {
	case "Created", "Started":
		s.Status = "Pending"
	}
	return s
}

// Store keeps sessions encrypted on disk (one file each, sealed with the
// customer's key) and monthly usage counts.
type Store struct {
	Dir string
	V   *vault.Vault
	mu  sync.Mutex
}

var reID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

func (s *Store) path(customer, id string) (string, error) {
	if !reID.MatchString(customer) || !reID.MatchString(id) {
		return "", errors.New("bad id")
	}
	return filepath.Join(s.Dir, "sessions", customer, id+".bin"), nil
}

func aad(customer, id string) []byte { return []byte("llkyc\x00" + customer + "\x00" + id) }

// Put saves a session.
func (s *Store) Put(x Session) error {
	p, err := s.path(x.Customer, x.ID)
	if err != nil {
		return err
	}
	b, err := json.Marshal(x)
	if err != nil {
		return err
	}
	ct, err := s.V.Seal(x.Customer, aad(x.Customer, x.ID), b)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, ct, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// ErrNoSession means there is no such session (or it was deleted).
var ErrNoSession = errors.New("no such session")

// Get reads a session.
func (s *Store) Get(customer, id string) (Session, error) {
	p, err := s.path(customer, id)
	if err != nil {
		return Session{}, ErrNoSession
	}
	ct, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return Session{}, ErrNoSession
	}
	if err != nil {
		return Session{}, err
	}
	b, err := s.V.Open(customer, aad(customer, id), ct)
	if err != nil {
		return Session{}, err
	}
	var x Session
	return x, json.Unmarshal(b, &x)
}

// Delete removes a session and everything fetched for it.
func (s *Store) Delete(customer, id string) error {
	p, err := s.path(customer, id)
	if err != nil {
		return ErrNoSession
	}
	if err := os.Remove(p); errors.Is(err, os.ErrNotExist) {
		return ErrNoSession
	} else {
		return err
	}
}

// Purge deletes sessions that were never finished an hour after they
// expired, and finished ones after the retention period, so personal data
// is not kept longer than needed. It returns how many it deleted.
func (s *Store) Purge(now time.Time, retention time.Duration) (int, error) {
	root := filepath.Join(s.Dir, "sessions")
	custs, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	n := 0
	for _, c := range custs {
		files, _ := os.ReadDir(filepath.Join(root, c.Name()))
		for _, f := range files {
			id, ok := strings.CutSuffix(f.Name(), ".bin")
			if !ok {
				continue
			}
			x, err := s.Get(c.Name(), id)
			if err != nil {
				continue
			}
			drop := false
			if t, err := time.Parse(time.RFC3339, x.Completed); err == nil && x.Completed != "" {
				drop = now.Sub(t) > retention
			} else if t, err := time.Parse(time.RFC3339, x.Expires); err == nil {
				drop = now.Sub(t) > time.Hour
			}
			if drop && s.Delete(c.Name(), id) == nil {
				n++
			}
		}
	}
	return n, nil
}

// Usage is a customer's count for a month.
type Usage struct {
	Customer  string `json:"customer"`
	Month     string `json:"month"`
	Started   int    `json:"started"`
	Completed int    `json:"completed"` // billable: DigiLocker returned the documents
	Failed    int    `json:"failed"`
}

func (s *Store) usagePath(customer, month string) string {
	return filepath.Join(s.Dir, "usage", customer, month+".json")
}

// Count adds one to a customer's count for the month.
func (s *Store) Count(customer, kind string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	month := at.Format("2006-01")
	u, _ := s.usage(customer, month)
	switch kind {
	case "started":
		u.Started++
	case "completed":
		u.Completed++
	case "failed":
		u.Failed++
	default:
		return fmt.Errorf("unknown count %q", kind)
	}
	p := s.usagePath(customer, month)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, _ := json.Marshal(u)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func (s *Store) usage(customer, month string) (Usage, error) {
	u := Usage{Customer: customer, Month: month}
	b, err := os.ReadFile(s.usagePath(customer, month))
	if err == nil {
		err = json.Unmarshal(b, &u)
	}
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	return u, err
}

// Usage returns a customer's count for a month.
func (s *Store) Usage(customer, month string) (Usage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.usage(customer, month)
}

// AllUsage returns every customer's count for a month.
func (s *Store) AllUsage(customers []string, month string) []Usage {
	out := []Usage{}
	for _, c := range customers {
		u, _ := s.Usage(c, month)
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Customer < out[j].Customer })
	return out
}
