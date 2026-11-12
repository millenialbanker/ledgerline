package pkg

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"
)

func init() { scryptN = 1 << 10 }

func build(t *testing.T, docs int, big int) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := Create(&buf, "correct horse battery", "test 1")
	if err != nil {
		t.Fatal(err)
	}
	w.Manifest().Source = Source{System: "SharePro", Backups: []Backup{{Database: "NC2627", Role: "company", FY: "2026-27", Used: true}}}
	w.Manifest().Firms = []Firm{{Code: "06", Name: "Test Securities"}}
	c, err := w.Master("06", "CLIENT-MASTER", []string{"UCC", "Name"})
	if err != nil {
		t.Fatal(err)
	}
	c.Write([]string{"T001", "Test, One"})
	c.Write([]string{"T002", "Two \"quoted\""})
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	h, _ := w.History("06", "2025-26", HistoryLedger, []string{"Date", "Account", "Debit", "Credit"})
	for i := 0; i < big; i++ {
		h.Write([]string{"2025-04-01", "CLIENT:T001", "100.00", ""})
	}
	h.Close()
	for i := 0; i < docs; i++ {
		data := bytes.Repeat([]byte{byte(i)}, 1000+i)
		if err := w.AddDocument("06", DocMeta{ID: "CN-" + strings.Repeat("x", i%3) + string(rune('a'+i%26)) + "-" + string(rune('0'+i%10)) + "-" + itoa(i), UCC: "T001", Type: DocContractNote, Date: time.Date(2025, 5, 2, 0, 0, 0, 0, time.UTC), FY: "2025-26", Signed: true, Format: "pdf"}, data); err != nil {
			t.Fatal(err)
		}
	}
	w.AddCheck(Check{Firm: "06", Name: CheckClients, Value: "2"})
	w.AddDropped("06", "Client web password", "never migrated", 2)
	w.AddDropped("06", "Client web password", "never migrated", 3)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func itoa(i int) string {
	s := ""
	for {
		s = string(rune('0'+i%10)) + s
		i /= 10
		if i == 0 {
			return s
		}
	}
}

func TestRoundTrip(t *testing.T) {
	b := build(t, 5, 40000) // over one chunk
	r, err := Open(bytes.NewReader(b), "correct horse battery", t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if r.Manifest.Firms[0].Name != "Test Securities" || r.Manifest.Dropped[0].Count != 5 {
		t.Fatalf("manifest %+v", r.Manifest)
	}
	var rows [][]string
	if err := r.CSV("firms/06/master/CLIENT-MASTER.csv", func(h, row []string) error {
		rows = append(rows, append([]string(nil), row...))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0][1] != "Test, One" || rows[1][1] != `Two "quoted"` {
		t.Fatalf("rows %v", rows)
	}
	docs := 0
	for _, f := range r.Manifest.Files {
		if f.Kind == KindDocuments {
			docs += f.Rows
		}
		if f.Kind == KindHistory && f.Rows != 40000 {
			t.Fatalf("history rows %d", f.Rows)
		}
	}
	if docs != 5 {
		t.Fatalf("docs %d", docs)
	}
	if bad := r.VerifyAll(); len(bad) > 0 {
		t.Fatal(bad)
	}
}

func TestWrongPassAndTamper(t *testing.T) {
	b := build(t, 1, 40000)
	if _, err := Open(bytes.NewReader(b), "wrong passphrase!", t.TempDir(), 0); err != ErrPassphrase {
		t.Fatalf("wrong pass: %v", err)
	}
	c := append([]byte(nil), b...)
	c[len(c)-40] ^= 1
	if _, err := Open(bytes.NewReader(c), "correct horse battery", t.TempDir(), 0); err != ErrPassphrase {
		t.Fatalf("tamper: %v", err)
	}
	// drop the final chunk
	if _, err := Open(bytes.NewReader(b[:len(b)-100]), "correct horse battery", t.TempDir(), 0); err == nil {
		t.Fatal("truncated package opened")
	}
	if _, err := Open(io.LimitReader(bytes.NewReader(b), 5), "x", t.TempDir(), 0); err == nil {
		t.Fatal("garbage opened")
	}
}

func TestBadPaths(t *testing.T) {
	var buf bytes.Buffer
	w, _ := Create(&buf, "correct horse battery", "t")
	if _, err := w.Begin(File{Path: "../x"}, true); err == nil {
		t.Fatal("accepted ../x")
	}
	if err := w.AddDocument("06", DocMeta{ID: "a/b"}, nil); err == nil {
		t.Fatal("accepted a/b")
	}
	if _, err := Create(&buf, "short", "t"); err == nil {
		t.Fatal("short passphrase")
	}
}
