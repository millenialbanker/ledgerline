package dataexport

import (
	"bytes"
	"io"
	"testing"
	"time"

	"ledgerline/internal/vault"
)

func TestSealedRoundTripAndDamage(t *testing.T) {
	v := vault.New(vault.Ephemeral())
	var buf bytes.Buffer
	w, err := NewSealedWriter(&buf, v, "alpha", "export:DX-1")
	if err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte("ledgerline "), 300000) // more than two frames
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	if bytes.Contains(b, []byte("ledgerline ledgerline")) {
		t.Fatal("stored in clear")
	}
	read := func(b []byte, tenant, label string) ([]byte, error) {
		r, err := NewSealedReader(bytes.NewReader(b), v, tenant, label)
		if err != nil {
			return nil, err
		}
		return io.ReadAll(r)
	}
	got, err := read(b, "alpha", "export:DX-1")
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("round trip: %v", err)
	}
	if _, err := read(b[:len(b)-40], "alpha", "export:DX-1"); err == nil {
		t.Fatal("a cut file opened")
	}
	if _, err := read(b, "beta", "export:DX-1"); err == nil {
		t.Fatal("opened as another firm's")
	}
	if _, err := read(b, "alpha", "export:DX-2"); err == nil {
		t.Fatal("opened as another export")
	}
	bad := append([]byte(nil), b...)
	bad[len(bad)/2] ^= 1
	if _, err := read(bad, "alpha", "export:DX-1"); err == nil {
		t.Fatal("a changed file opened")
	}
}

func TestArchiveManifestVerifies(t *testing.T) {
	var buf bytes.Buffer
	a := NewArchive(&buf, time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC))
	w, _ := a.Create("collections/client.jsonl", "collection")
	w.Write([]byte(`{"id":"C1"}` + "\n"))
	a.Count(1)
	w, _ = a.Create("../../etc/passwd", "file")
	w.Write([]byte("x"))
	w, _ = a.Create("manifest.json", "file") // cannot pass for the manifest
	w.Write([]byte("{}"))
	info, sign := Ed25519Signer()
	mb, err := a.Finish(Manifest{ExportID: "DX-1", Tenant: "alpha"}, info, sign)
	if err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	v, err := Verify(bytes.NewReader(b), int64(len(b)), info.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if v.Files != 3 || v.ManifestSHA256 != ManifestSHA256(mb) || v.Manifest.Files[1].Path != "etc/passwd" || v.Manifest.Files[2].Path != "_manifest.json" || v.Manifest.Files[0].Records != 1 {
		t.Fatalf("%+v", v.Manifest.Files)
	}
	other, _ := Ed25519Signer()
	if _, err := Verify(bytes.NewReader(b), int64(len(b)), other.PublicKey); err == nil {
		t.Fatal("verified against another key")
	}
}
