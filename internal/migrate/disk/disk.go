// Package disk tells the migration tool how much room is left on a disk.
package disk

import (
	"os"
	"path/filepath"
)

// Free returns the bytes free to this user on the disk holding path. When
// path does not exist yet, its nearest existing parent is used.
func Free(path string) (int64, error) {
	p, err := filepath.Abs(path)
	if err != nil {
		return 0, err
	}
	for {
		if _, err := os.Stat(p); err == nil {
			break
		}
		parent := filepath.Dir(p)
		if parent == p {
			break
		}
		p = parent
	}
	return free(p)
}
