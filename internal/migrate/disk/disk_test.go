package disk

import (
	"path/filepath"
	"testing"
)

func TestFree(t *testing.T) {
	d := t.TempDir()
	n, err := Free(d)
	if err != nil || n <= 0 {
		t.Fatalf("Free(%s) = %d, %v", d, n, err)
	}
	// a folder that does not exist yet: its parent's disk
	m, err := Free(filepath.Join(d, "not", "yet"))
	if err != nil || m <= 0 {
		t.Fatalf("Free(missing) = %d, %v", m, err)
	}
}
