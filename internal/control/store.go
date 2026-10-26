package control

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"ledgerline/internal/docs"
)

// FileStore keeps documents in memory and writes every change to a journal
// file, replayed at start. Bodies reach it already encrypted (vault.Docs
// wraps it), so the journal holds no customer data in clear. It is
// compacted each time Control starts.
type FileStore struct {
	*docs.Mem
	mu   sync.Mutex
	f    *os.File
	path string
}

type journalLine struct {
	Tenant string   `json:"t"`
	Doc    docs.Doc `json:"d"`
}

// OpenFileStore opens (or creates) the journal in dir.
func OpenFileStore(dir string) (*FileStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	p := filepath.Join(dir, "control.journal")
	m := docs.NewMem()
	byTenant := map[string]map[string]docs.Doc{}
	if f, err := os.Open(p); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 64<<20)
		n := 0
		for sc.Scan() {
			n++
			var l journalLine
			if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
				// A line cut short by a crash is the last one; anything else is damage.
				if !sc.Scan() {
					break
				}
				f.Close()
				return nil, fmt.Errorf("control journal line %d is damaged: %v", n, err)
			}
			if byTenant[l.Tenant] == nil {
				byTenant[l.Tenant] = map[string]docs.Doc{}
			}
			byTenant[l.Tenant][l.Doc.Collection+"\x00"+l.Doc.ID] = l.Doc
		}
		f.Close()
		if err := sc.Err(); err != nil {
			return nil, err
		}
	}
	for t, ds := range byTenant {
		list := make([]docs.Doc, 0, len(ds))
		for _, d := range ds {
			list = append(list, d)
		}
		if err := m.ImportTenant(t, list); err != nil {
			return nil, err
		}
	}
	// Rewrite the journal with each document's latest version only.
	if len(byTenant) > 0 {
		tmp := p + ".tmp"
		w, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return nil, err
		}
		bw := bufio.NewWriter(w)
		for t := range byTenant {
			for _, d := range m.ExportTenant(t) {
				b, _ := json.Marshal(journalLine{Tenant: t, Doc: d})
				bw.Write(append(b, '\n'))
			}
		}
		if err := bw.Flush(); err != nil {
			w.Close()
			return nil, err
		}
		if err := w.Sync(); err != nil {
			w.Close()
			return nil, err
		}
		w.Close()
		if err := os.Rename(tmp, p); err != nil {
			return nil, err
		}
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &FileStore{Mem: m, f: f, path: p}, nil
}

// Put stores and journals a change.
func (s *FileStore) Put(ctx context.Context, t, c, id string, body json.RawMessage, expect int64, upsert bool, actor string) (docs.Doc, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.Mem.Put(ctx, t, c, id, body, expect, upsert, actor)
	if err != nil {
		return d, err
	}
	b, _ := json.Marshal(journalLine{Tenant: t, Doc: d})
	if _, err := s.f.Write(append(b, '\n')); err != nil {
		return d, fmt.Errorf("control journal: %w", err)
	}
	if err := s.f.Sync(); err != nil {
		return d, fmt.Errorf("control journal: %w", err)
	}
	return d, nil
}

// Close closes the journal.
func (s *FileStore) Close() error { return s.f.Close() }
